const $ = (s, el = document) => el.querySelector(s);
const icon = (name, cls = 'size-4') => `<i data-lucide="${name}" class="${cls}"></i>`;
const fmt = (n) => n.toLocaleString('en-US');
const compact = (n) => Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 2 }).format(n);

const S = {
  status: null,
  stats: null,
  settings: null,
  lists: null,
  allow: null,
  block: null,
  rewrites: null,
  log: null,
  logEnd: false,
  logFilter: 'all',
  logSearch: '',
  live: true,
  chartView: 'chart',
  tipDay: null,
  test: null,
  customPreset: {},
};

const LOG_LIMIT = 100;
const LOG_KEEP = 5 * LOG_LIMIT;
const SETTINGS_LISTS = ['upstreams', 'bootstrap', 'local_domains', 'rebinding_exempt', 'clients'];

async function api(method, path, body) {
  const init = { method };
  if (['POST', 'PUT', 'PATCH'].includes(method)) {
    init.headers = { 'Content-Type': 'application/json' };
    init.body = typeof body === 'string' ? body : JSON.stringify(body ?? {});
  }
  const res = await fetch(`/api/${path}`, init);
  if (!res.ok) {
    const err = await res.json().catch(() => null);
    throw new Error(err?.error || `${res.status} ${res.statusText}`);
  }
  return res.status === 204 ? null : res.json();
}

const normSettings = (s) => {
  SETTINGS_LISTS.forEach((k) => { s[k] ??= []; });
  return s;
};

const loaders = {
  status: async () => { S.status = await api('GET', 'status'); },
  stats: async () => { S.stats = await api('GET', 'stats'); },
  settings: async () => { S.settings = normSettings(await api('GET', 'settings')); },
  lists: async () => { S.lists = (await api('GET', 'lists')) ?? []; },
  allow: async () => { S.allow = (await api('GET', 'rules/allow')) ?? []; },
  block: async () => { S.block = (await api('GET', 'rules/block')) ?? []; },
  rewrites: async () => { S.rewrites = (await api('GET', 'rewrites')) ?? []; },
  log: () => loadLog(),
};

const NEEDS = {
  dashboard: ['stats'],
  querylog: ['settings', 'log'],
  upstreams: ['settings'],
  lists: ['lists', 'settings'],
  rules: ['allow', 'block'],
  rewrites: ['rewrites'],
  advanced: ['settings'],
  general: ['settings'],
  stats: ['settings'],
  clients: ['settings'],
  backup: [],
};
const needs = (r) => NEEDS[r.sub ?? r.page];

let logSeq = 0;
function logParams(extra) {
  const p = new URLSearchParams({ result: S.logFilter, ...extra });
  if (S.logSearch.trim()) p.set('search', S.logSearch.trim());
  return p;
}

function logQuery(before) {
  const p = logParams({ limit: LOG_LIMIT });
  if (before) p.set('before', before);
  return api('GET', `querylog?${p}`);
}

async function loadLog() {
  const seq = ++logSeq;
  const entries = (await logQuery()).entries ?? [];
  if (seq !== logSeq) return;
  S.log = entries;
  S.logEnd = entries.length < LOG_LIMIT;
}

async function pollLog() {
  const seq = logSeq;
  const entries = (await logQuery()).entries ?? [];
  if (seq !== logSeq || !S.log) return;
  const newest = S.log[0]?.id ?? 0;
  const fresh = entries.filter((e) => e.id > newest);
  if (!fresh.length) return;
  if (fresh.length === LOG_LIMIT) {
    S.log = fresh;
    S.logEnd = false;
  } else {
    S.log = [...fresh, ...S.log];
  }
  if (S.log.length > LOG_KEEP) {
    S.log.length = LOG_KEEP;
    S.logEnd = false;
  }
  renderLog();
}

async function olderLog() {
  const seq = logSeq;
  const oldest = S.log?.at(-1)?.id;
  if (!oldest) return;
  const entries = (await logQuery(oldest)).entries ?? [];
  if (seq !== logSeq) return;
  S.log.push(...entries);
  S.logEnd = entries.length < LOG_LIMIT;
}

const ago = (ms) => {
  const m = Math.round(ms / 60e3);
  if (m < 1) return 'just now';
  if (m < 60) return `${m} min ago`;
  const h = Math.round(m / 60);
  return h < 48 ? `${h} h ago` : `${Math.round(h / 24)} d ago`;
};
const clock = (t) => new Date(t).toLocaleTimeString('en-GB', { hour12: false });
const duration = (ms) => `${ms < 10 ? ms.toFixed(1) : Math.round(ms)} ms`;
const answerType = (a) => (/^\d{1,3}(\.\d{1,3}){3}$/.test(a) ? 'A' : a.includes(':') ? 'AAAA' : 'CNAME');
const upstreamProto = (u) => (u.startsWith('https://') ? 'DoH' : u.startsWith('tls://') ? 'DoT' : 'Plain');
const ratio = (a, b) => (b ? a / b : 0);
const pct = (a, b) => `${(ratio(a, b) * 100).toFixed(1)}%`;
const localDate = (s) => {
  const [y, m, d] = s.split('-').map(Number);
  return new Date(y, m - 1, d);
};

function hostOf(u) {
  try {
    return new URL(u.includes('://') ? u.replace(/^tls:/, 'https:') : `dns://${u}`).hostname;
  } catch {
    return u;
  }
}

const TH = 'px-3 pb-2 text-left text-[11px] font-medium uppercase tracking-wider whitespace-nowrap text-overlay1';
const TR = 'group transition-colors hover:bg-surface0/50 [&>td:first-child]:rounded-l-2xl [&>td:last-child]:rounded-r-2xl';

const pageHead = (title, right = '') => `<header class="mb-5 flex flex-wrap items-center justify-between gap-3"><h1 class="font-display text-2xl text-text">${title}</h1>${right}</header>`;

const gallery = (inner) => `<div data-scroll="lane" class="-mr-3.5 h-full overflow-y-auto">${inner}</div>`;

const panel = (title, body, { lead = '', right = '', span = '' } = {}) => `<section class="${ui.cls.panel} flex min-w-0 flex-col gap-4 p-5 ${span}">
  ${title ? `<header class="flex flex-wrap items-center gap-x-3 gap-y-2">${lead}<h2 class="font-display text-[16px] font-medium text-text">${title}</h2>${right ? `<span class="ml-auto flex flex-wrap items-center gap-3">${right}</span>` : ''}</header>` : ''}
  ${body}
</section>`;

const settingLabel = (label, hint, cls = '') => `<p class="text-sm text-text ${cls}">${label}${hint ? `<span class="block text-xs text-overlay1">${hint}</span>` : ''}</p>`;

const settingRow = (label, hint, control, key = '') => `<div ${key ? `data-setting="${key}"` : ''} class="flex flex-wrap items-center justify-between gap-x-6 gap-y-2">${settingLabel(label, hint, 'min-w-0 flex-1 basis-40')}<div class="min-w-0 max-w-full">${control}</div></div>`;

const settingBlock = (label, control, cls = '') => `<div class="flex flex-col gap-2 ${cls}">${settingLabel(label, '')}${control}</div>`;

const num = (key, unit, { min, max, step } = {}) => `<span class="block w-44">${ui.number({ name: key, value: S.settings[key], min, max, step, unit })}</span>`;

const numRow = (label, hint, key, unit, limits) => settingRow(label, hint, num(key, unit, limits), key);

const tagField = (key, hint = '') => `<div class="flex flex-col gap-1.5">
  <div data-tags="${key}" class="flex flex-wrap items-center gap-1.5 rounded-2xl bg-surface0 p-1.5 focus-within:ring-2 focus-within:ring-mauve/40">
    ${S.settings[key].map((t) => ui.chip(t, { mono: true, remove: 'tag-remove' }).replace('bg-surface0', 'bg-surface1')).join('')}
    <input name="${key}-add" data-tag-input placeholder="Add and press Enter" class="min-w-44 flex-1 bg-transparent px-2 py-1 text-sm text-text placeholder:text-overlay0 outline-none pointer-coarse:text-[16px]">
  </div>
  ${hint ? `<p class="px-1 text-xs text-overlay1">${hint}</p>` : ''}
</div>`;

const grow = (control, cls = 'min-w-48') => `<span class="flex-1 ${cls}">${control}</span>`;

const addForm = (submit, controls, { button = ui.button('Add', { icon: 'plus', type: 'submit' }), hint = '', attrs = '' } = {}) => `<form data-submit="${submit}" ${attrs} novalidate class="flex flex-col gap-1.5">
  <div class="flex flex-wrap items-center gap-2">${controls}${button}</div>
  ${hint ? `<p class="px-1 text-xs text-overlay1">${hint}</p>` : ''}
</form>`;

const removeButton = (action, attrs) => `<span class="${ui.cls.reveal}">${ui.iconButton('trash-2', { tip: 'Remove', size: 'sm', variant: 'danger', action, attrs })}</span>`;

const checked = (root, name) => $(`[data-name="${name}"]`, root).getAttribute('aria-checked') === 'true';

const presetPicker = (key, presets, unit, limits) => {
  const v = S.settings[key];
  const custom = S.customPreset[key] || !presets.some(([k]) => k === v);
  const options = [...presets, ['custom', 'Custom']].map(([value, label]) => ({ value: String(value), label }));
  return `<div class="flex flex-wrap items-center gap-3">${ui.segmented({ name: `preset:${key}`, value: custom ? 'custom' : String(v), options })}${custom ? num(key, unit, limits) : ''}</div>`;
};

const tabs = (page, list, sub) => ui.segmented({ name: `tab:${page}`, value: sub, ground: true, options: list.map(([value, label, ic]) => ({ value, label, icon: ic, compact: true })) });

const NAV = [
  ['dashboard', 'Dashboard', 'layout-dashboard'],
  ['querylog', 'Query log', 'scroll-text'],
  ['dns', 'DNS', 'globe'],
  ['app', 'Settings', 'settings-2'],
];
const DNS_TABS = [
  ['upstreams', 'Upstreams', 'server'],
  ['lists', 'Block lists', 'list'],
  ['rules', 'Rules', 'list-checks'],
  ['rewrites', 'Rewrites', 'arrow-left-right'],
  ['advanced', 'Advanced', 'sliders-horizontal'],
];
const APP_TABS = [
  ['general', 'General', 'settings-2'],
  ['stats', 'Statistics', 'chart-column'],
  ['clients', 'Clients', 'users'],
  ['backup', 'Backup', 'archive'],
];

function route() {
  const [page = 'dashboard', sub] = location.hash.replace(/^#\/?/, '').split('/');
  if (page === 'dns') return { page, sub: DNS_TABS.some(([k]) => k === sub) ? sub : 'upstreams' };
  if (page === 'app') return { page, sub: APP_TABS.some(([k]) => k === sub) ? sub : 'general' };
  return { page: NAV.some(([k]) => k === page) ? page : 'dashboard' };
}

function remaining() {
  const s = Math.max(0, Math.round((S.status.blocking.until - Date.now()) / 1000));
  const h = Math.floor(s / 3600);
  const mm = String(Math.floor((s % 3600) / 60)).padStart(2, '0');
  const ss = String(s % 60).padStart(2, '0');
  return h ? `${h}:${mm}:${ss}` : `${mm}:${ss}`;
}

const STATES = {
  on: { title: 'Blocking is on', icon: 'shield-check', tone: 'green' },
  paused: { title: 'Blocking is paused', icon: 'shield-alert', tone: 'yellow' },
  off: { title: 'Blocking is off', icon: 'shield-off', tone: 'red' },
};

function stateTag() {
  const mode = S.status?.blocking.mode;
  if (!mode || mode === 'on') return '';
  const text = mode === 'paused' ? `Paused <span data-countdown class="tabular-nums">${remaining()}</span>` : 'Blocking off';
  return `<a href="#/dashboard" class="inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium whitespace-nowrap ${ui.tone.chip(STATES[mode].tone)} ${ui.cls.focus}">${icon(STATES[mode].icon, 'size-3.5')}${text}</a>`;
}

function blockingCard() {
  const { blocking, lists, upstreams, cache } = S.status;
  const mode = blocking.mode;
  const st = STATES[mode];
  const primary = upstreams?.[0];
  const facts = [
    ['list', `${lists.enabled} of ${lists.total} lists`],
    ['shield-ban', `${compact(lists.domains)} domains`],
    ['server', primary ? `${hostOf(primary.url)}${primary.latency_ms ? ` ${Math.round(primary.latency_ms)} ms` : ''}` : 'No upstream'],
    ['zap', `${Math.round(cache.hit_rate * 100)}% cache hits`],
  ];
  const controls = {
    on: `<span class="text-xs text-overlay1">Pause</span>
      ${ui.button('5 min', { size: 'sm', action: 'pause', attrs: 'data-sec="300"' })}
      ${ui.button('1 h', { size: 'sm', action: 'pause', attrs: 'data-sec="3600"' })}
      ${ui.button('Custom', { size: 'sm', action: 'pause-custom' })}`,
    paused: `<span class="text-sm">Resumes in <span data-countdown class="text-text tabular-nums">${remaining()}</span></span>
      ${ui.button('Resume now', { icon: 'play', variant: 'primary', action: 'resume' })}`,
    off: '',
  }[mode];
  return `<section class="${ui.cls.panel} flex flex-wrap items-center gap-x-6 gap-y-5 p-5 lg:col-span-2 2xl:col-span-4">
    ${ui.tile(st.icon, st.tone, 'lg')}
    <div class="min-w-48 flex-1">
      <h1 class="font-display text-2xl text-text">${st.title}</h1>
      <ul class="mt-1.5 flex flex-wrap gap-x-5 gap-y-1 text-sm">${facts.map(([ic, t]) => `<li class="flex items-center gap-1.5">${icon(ic, 'size-3.5 text-overlay1')}${ui.esc(t)}</li>`).join('')}</ul>
    </div>
    ${controls ? `<div class="flex flex-wrap items-center gap-2 max-sm:order-last max-sm:w-full">${controls}</div>` : ''}
    ${ui.toggle({ name: 'blocking', checked: mode !== 'off', label: 'Blocking' })}
  </section>`;
}

function stat(label, ic, tone, value, sub, share) {
  return `<section class="${ui.cls.panel} flex items-center gap-3 p-4">
    ${ui.tile(ic, tone)}
    <div class="min-w-0 flex-1">
      <p class="text-xs text-overlay1">${label}</p>
      <p class="font-display text-2xl text-text tabular-nums">${value}</p>
      ${share === undefined
        ? `<p class="text-xs text-overlay1">${sub}</p>`
        : `<p class="flex items-center gap-2 text-xs text-overlay1 tabular-nums">${ui.meter(share * 100, 'red', 'h-1.5 flex-1')}${sub}</p>`}
    </div>
  </section>`;
}

function niceMax(v) {
  const p = 10 ** Math.floor(Math.log10(v));
  return [1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10].map((m) => m * p).find((m) => m >= v);
}

const chartDays = () => (S.stats.days ?? []).map((d) => ({ ...d, date: localDate(d.date) }));

function chartCard() {
  const data = chartDays();
  const n = S.stats.window_days;
  const max = niceMax(Math.max(10, ...data.map((d) => d.total)));
  const empty = data.every((d) => !d.total);
  const label = (d) => (n <= 7 ? d.date.toLocaleDateString('en-US', { weekday: 'short' }) : String(d.date.getDate()));
  const legend = `<span class="flex items-center gap-4 text-xs">
    <span class="flex items-center gap-1.5"><span class="size-2.5 rounded-sm bg-green"></span>Allowed</span>
    <span class="flex items-center gap-1.5"><span class="size-2.5 rounded-sm bg-red"></span>Blocked</span></span>`;
  const chart = `<div id="chart" class="grid flex-1 grid-cols-[auto_1fr] grid-rows-[minmax(14rem,1fr)_auto] gap-x-3 pt-2">
      <div class="-my-1.5 flex flex-col justify-between text-right text-xs text-overlay1 tabular-nums">${[max, max / 2, 0].map((v) => `<span>${compact(v)}</span>`).join('')}</div>
      <div class="relative">
        ${['top-0', 'top-1/2', 'top-full'].map((t) => `<span class="absolute inset-x-0 h-px bg-surface0 ${t}"></span>`).join('')}
        ${empty ? '<p class="absolute inset-0 grid place-items-center text-sm text-overlay1">No queries yet</p>' : ''}
        <div class="relative flex h-full items-end">${data.map((d, i) => `
          <div data-day="${i}" tabindex="0" class="group flex h-full flex-1 items-end justify-center px-[2px] outline-none">
            <div class="flex w-full max-w-6 flex-col gap-[2px] transition group-hover:brightness-125 group-focus-visible:brightness-125" style="height:${(d.total / max) * 100}%">
              <span class="rounded-t-[4px] bg-red" style="flex:${d.blocked}"></span>
              <span class="bg-green" style="flex:${d.total - d.blocked}"></span>
            </div>
          </div>`).join('')}
        </div>
        <div id="chart-tip" class="pointer-events-none absolute top-2 z-10 hidden -translate-x-1/2 rounded-xl bg-surface0 px-2.5 py-1.5 text-xs whitespace-nowrap shadow-lg shadow-shade/60"></div>
      </div>
      <span></span>
      <div class="mt-2 flex text-xs text-overlay1">${data.map((d, i) => `<span class="flex-1 text-center">${n <= 7 || i % 5 === (data.length - 1) % 5 ? label(d) : ''}</span>`).join('')}</div>
    </div>`;
  const td = 'px-3 py-2 text-right font-mono text-xs tabular-nums';
  const table = `<div class="relative -mr-3.5"><div data-scroll="lane" class="max-h-72 overflow-auto"><table class="w-full border-separate border-spacing-0 text-sm">
      <thead class="sticky top-0 z-10 bg-mantle"><tr><th class="${TH} w-full">Date</th><th class="${TH} text-right">Allowed</th><th class="${TH} text-right">Blocked</th><th class="${TH} text-right max-sm:hidden">Total</th><th class="${TH} text-right">Blocked %</th></tr></thead>
      <tbody>${[...data].reverse().map((d) => `<tr class="${TR}"><td class="px-3 py-2 whitespace-nowrap text-text">${d.date.toLocaleDateString('en-US', { month: 'short', day: 'numeric' })}</td><td class="${td} text-text">${fmt(d.total - d.blocked)}</td><td class="${td} text-text">${fmt(d.blocked)}</td><td class="${td} max-sm:hidden">${fmt(d.total)}</td><td class="${td}">${pct(d.blocked, d.total)}</td></tr>`).join('')}</tbody>
    </table></div></div>`;
  return panel('Queries per day', S.chartView === 'chart' ? chart : table, {
    span: 'lg:col-span-2',
    right: `${legend}${ui.segmented({ name: 'chart-view', value: S.chartView, options: [{ value: 'chart', label: 'Chart', icon: 'chart-column', compact: true }, { value: 'table', label: 'Table', icon: 'table-2', compact: true }] })}`,
  });
}

function showTip(col) {
  const tip = $('#chart-tip');
  if (!tip) return;
  S.tipDay = Number(col.dataset.day);
  const d = chartDays()[S.tipDay];
  const row = (color, value, name) => `<div class="mt-1 flex items-center gap-2"><span class="h-0.5 w-3 rounded-full ${color}"></span><span class="font-medium text-text tabular-nums">${fmt(value)}</span><span class="text-overlay1">${name}</span></div>`;
  tip.innerHTML = `<p class="text-overlay1">${d.date.toLocaleDateString('en-US', { weekday: 'short', month: 'short', day: 'numeric' })}</p>
    ${row('bg-green', d.total - d.blocked, 'allowed')}${row('bg-red', d.blocked, `blocked, ${pct(d.blocked, d.total)}`)}`;
  tip.classList.remove('hidden');
  const box = tip.parentElement.getBoundingClientRect();
  const c = col.getBoundingClientRect();
  const half = tip.offsetWidth / 2;
  tip.style.left = `${Math.min(Math.max(c.left - box.left + c.width / 2, half), box.width - half)}px`;
}

function hideTip() {
  S.tipDay = null;
  $('#chart-tip')?.classList.add('hidden');
}

function topCard(title, rows, kind, emptyText) {
  const max = rows[0]?.count || 1;
  const allow = kind === 'allow';
  const list = `<ol class="-mx-2 flex flex-col gap-0.5">${rows.map(({ domain, count }, i) => `
    <li class="group flex items-center gap-3 ${ui.cls.row} px-2 py-1.5">
      <span class="w-4 shrink-0 text-right text-xs text-overlay1 tabular-nums">${i + 1}</span>
      <div class="min-w-0 flex-1">
        <p class="flex items-baseline justify-between gap-3"><span data-tip="${ui.esc(domain)}" class="min-w-0 ${ui.cls.rowText} font-mono text-sm text-text">${ui.esc(domain)}</span><span class="shrink-0 text-sm tabular-nums">${fmt(count)}</span></p>
        ${ui.meter((count / max) * 100, allow ? 'red' : 'green', 'mt-1.5 h-1')}
      </div>
      <span class="${ui.cls.reveal}">${ui.iconButton(allow ? 'shield-check' : 'ban', { tip: allow ? 'Unblock' : 'Block', size: 'sm', action: 'quick-rule', attrs: `data-kind="${kind}" data-domain="${ui.esc(domain)}"` })}</span>
    </li>`).join('')}
  </ol>`;
  return panel(title, rows.length ? list : ui.empty({ icon: allow ? 'shield-ban' : 'activity', title: emptyText, fill: false }), {
    right: `<span class="text-xs text-overlay1">Last ${S.stats.window_days} days</span>`,
  });
}

function viewDashboard() {
  const { today, totals, retention_days: retention } = S.stats;
  return gallery(`<div class="grid grid-cols-1 gap-4 lg:grid-cols-2 2xl:grid-cols-4">
    ${blockingCard()}
    <div class="grid grid-cols-2 gap-4 md:grid-cols-4 lg:col-span-2 2xl:col-span-4">
      ${stat('Queries today', 'activity', 'blue', fmt(today.total), 'since midnight')}
      ${stat('Blocked today', 'shield-ban', 'red', fmt(today.blocked), pct(today.blocked, today.total), ratio(today.blocked, today.total))}
      ${stat('Total queries', 'history', 'blue', compact(totals.total), `last ${retention} days`)}
      ${stat('Total blocked', 'calendar-range', 'red', compact(totals.blocked), pct(totals.blocked, totals.total), ratio(totals.blocked, totals.total))}
    </div>
    ${chartCard()}
    ${topCard('Top queried domains', S.stats.top_queried ?? [], 'block', 'No queries yet')}
    ${topCard('Top blocked domains', S.stats.top_blocked ?? [], 'allow', 'Nothing blocked yet')}
  </div>`);
}

const RESULT = {
  allowed: ['circle-check', 'green', 'Allowed'],
  blocked: ['ban', 'red', 'Blocked'],
  rewritten: ['arrow-left-right', 'blue', 'Rewritten'],
  local: ['house', 'teal', 'Local'],
};

function logRows() {
  if (!S.log.length) {
    const filtered = S.logSearch.trim() || S.logFilter !== 'all';
    return `<tr><td colspan="8">${ui.empty({ icon: filtered ? 'search-x' : 'scroll-text', title: filtered ? 'No queries match' : 'No queries yet', fill: false })}</td></tr>`;
  }
  const td = 'px-3 py-2.5';
  return S.log.map((r) => {
    const [ic, tone, label] = RESULT[r.result];
    const name = r.client_name;
    const action = r.result === 'blocked' ? ['Unblock', 'shield-check'] : r.result === 'allowed' ? ['Block', 'ban'] : null;
    const upstream = r.cached ? 'cache' : r.upstream ? `${ui.esc(r.upstream)} <span class="text-overlay0">${duration(r.duration_ms)}</span>` : '';
    return `<tr class="${TR}">
      <td class="${td} font-mono text-xs whitespace-nowrap text-overlay1 tabular-nums max-sm:hidden">${clock(r.time)}</td>
      <td class="${td} whitespace-nowrap max-sm:hidden">${name ? `<span class="text-text">${ui.esc(name)}</span> <span class="text-xs text-overlay1 max-lg:hidden">${ui.esc(r.client)}</span>` : `<span class="font-mono text-xs">${ui.esc(r.client)}</span>`}</td>
      <td class="${td} w-full max-w-0">
        <p data-tip="${ui.esc(r.domain)}" class="${ui.cls.rowText} font-mono text-xs text-text">${ui.esc(r.domain)}</p>
        <p class="truncate text-xs text-overlay1 sm:hidden">${ui.esc(name || r.client)} <span class="font-mono tabular-nums">${clock(r.time)}</span></p>
      </td>
      <td class="${td} max-sm:hidden">${ui.chip(r.type, { mono: true })}</td>
      <td class="${td} whitespace-nowrap"><span data-tip="${label}" data-tip-collapse class="inline-flex items-center gap-1.5 text-xs ${ui.tone.text(tone)}">${icon(ic)}<span data-label class="max-md:hidden">${label}</span></span></td>
      <td data-tip="${ui.esc(r.reason)}" class="${td} max-w-64 truncate text-xs text-overlay1 max-md:hidden">${ui.esc(r.reason)}</td>
      <td class="${td} text-right text-xs whitespace-nowrap text-overlay1 tabular-nums max-md:hidden">${upstream}</td>
      <td class="px-1 py-1 text-right">${action ? `<span class="${ui.cls.reveal}">${ui.iconButton(action[1], { tip: action[0], size: 'sm', action: 'log-rule', attrs: `data-id="${r.id}"` })}</span>` : ''}</td>
    </tr>`;
  }).join('');
}

const logFoot = () => (S.logEnd ? '' : ui.button('Load older', { icon: 'chevrons-down', action: 'log-more' }));

function viewQueryLog() {
  const filters = [['all', 'All', 'list'], ['blocked', 'Blocked', 'ban'], ['allowed', 'Allowed', 'circle-check'], ['rewritten', 'Rewritten', 'arrow-left-right']];
  const live = ui.button(S.live ? 'Live' : 'Paused', { action: 'log-live' }).replace('<span>', `${ui.dot(S.live ? 'green' : 'overlay0', { pulse: S.live })}<span>`);
  return `<div class="flex h-full min-h-0 flex-col">
    ${pageHead('Query log', ui.button('Export', { icon: 'download', action: 'log-export' }))}
    <section class="${ui.cls.panel} flex min-h-0 flex-1 flex-col gap-3 p-4">
      <header class="flex flex-wrap items-center gap-2">
        ${grow(ui.search({ name: 'log-search', placeholder: 'Search domain or client', value: S.logSearch }), 'min-w-56')}
        ${ui.segmented({ name: 'log-filter', value: S.logFilter, options: filters.map(([value, label, ic]) => ({ value, label, icon: ic, compact: true })) })}
        ${live}
      </header>
      <div class="relative -mr-3.5 flex min-h-0 flex-1 flex-col"><div data-scroll="lane" class="min-h-0 flex-1 overflow-auto">
        <table class="w-full border-separate border-spacing-0 text-sm">
          <thead class="sticky top-0 z-10 bg-mantle"><tr>
            <th class="${TH} max-sm:hidden">Time</th><th class="${TH} max-sm:hidden">Client</th><th class="${TH} w-full">Domain</th><th class="${TH} max-sm:hidden">Type</th><th class="${TH}">Result</th><th class="${TH} max-md:hidden">Reason</th><th class="${TH} text-right max-md:hidden">Upstream</th><th class="${TH} w-10"></th>
          </tr></thead>
          <tbody id="log-body">${logRows()}</tbody>
        </table>
        <div id="log-foot" class="flex justify-center pt-3 empty:hidden">${logFoot()}</div>
      </div></div>
    </section>
  </div>`;
}

function viewUpstreams() {
  const ups = S.settings.upstreams;
  const latency = (u) => S.status.upstreams?.find((x) => x.url === u)?.latency_ms;
  const rows = ups.map((u, i) => `
    <li class="group flex items-center gap-3 ${ui.cls.row} py-1 pr-1 pl-3">
      ${ui.count(i + 1)}
      <span data-tip="${ui.esc(u)}" class="min-w-0 flex-1 ${ui.cls.rowText} font-mono text-sm text-text">${ui.esc(u)}</span>
      <span class="max-sm:hidden">${ui.chip(upstreamProto(u))}</span>
      <span class="w-12 text-right text-xs text-overlay1 tabular-nums max-sm:hidden">${latency(u) ? `${Math.round(latency(u))} ms` : ''}</span>
      <span class="flex ${ui.cls.reveal}">
        ${ui.iconButton('arrow-up', { tip: 'Move up', size: 'sm', action: 'up-move', disabled: i === 0, attrs: `data-i="${i}" data-dir="-1"` })}
        ${ui.iconButton('arrow-down', { tip: 'Move down', size: 'sm', action: 'up-move', disabled: i === ups.length - 1, attrs: `data-i="${i}" data-dir="1"` })}
        ${ui.iconButton('trash-2', { tip: 'Remove', size: 'sm', variant: 'danger', action: 'up-remove', attrs: `data-i="${i}"` })}
      </span>
    </li>`).join('');
  return `<div class="grid grid-cols-1 items-start gap-4 lg:grid-cols-3">
    ${panel('Upstream servers', `<ol class="-mx-3 flex flex-col gap-0.5">${rows}</ol>
      ${addForm('up-add', grow(ui.input({ name: 'url', placeholder: 'https://host/dns-query, tls://host, or 192.168.1.1', mono: true, attrs: 'required' })))}`, { span: 'lg:col-span-2' })}
    <div class="flex flex-col gap-4">
      ${panel('Bootstrap', tagField('bootstrap', 'Plain DNS servers that resolve upstream hostnames'))}
      ${panel('Timing', `${numRow('Hedge delay', 'Wait before asking the next server', 'hedge_delay_ms', 'ms', { min: 50, step: 50 })}
        ${numRow('Timeout', 'Answer SERVFAIL after', 'timeout_ms', 'ms', { min: 500, step: 250 })}`)}
    </div>
  </div>`;
}

function viewLists() {
  const td = 'px-3 py-2.5';
  const rows = S.lists.map((l) => {
    const updated = l.updated ? ago(Date.now() - l.updated) : 'Never';
    const dim = l.enabled ? '' : 'opacity-60';
    return `<tr class="${TR}">
      <td class="${td}">${ui.toggle({ name: `list:${l.id}`, checked: l.enabled, label: l.name })}</td>
      <td class="${td} w-full max-w-0 ${dim}">
        <p class="${ui.cls.rowText} text-text">${ui.esc(l.name)}${l.error ? icon('triangle-alert', 'inline size-3.5 ml-2 -mt-0.5 text-peach md:hidden') : ''}</p>
        <p data-tip="${ui.esc(l.url)}" class="${ui.cls.rowText} font-mono text-xs text-overlay1">${ui.esc(l.url)}</p>
      </td>
      <td class="${td} text-right font-mono text-xs text-text tabular-nums ${dim}">${fmt(l.domains)}</td>
      <td class="${td} text-right font-mono text-xs tabular-nums max-md:hidden ${dim}">${fmt(l.skipped)}</td>
      <td class="${td} text-xs whitespace-nowrap text-overlay1 max-md:hidden">${l.error
        ? `<span class="inline-flex items-center gap-1.5 text-peach">${icon('triangle-alert', 'size-3.5')}${updated}</span><p>${ui.esc(l.error)}</p>`
        : updated}</td>
      <td class="px-1 py-1"><span class="flex ${ui.cls.reveal}">
        ${ui.iconButton('refresh-cw', { tip: 'Refresh', size: 'sm', action: 'list-refresh', attrs: `data-id="${ui.esc(l.id)}"` })}
        ${ui.iconButton('trash-2', { tip: 'Remove', size: 'sm', variant: 'danger', action: 'list-remove', attrs: `data-id="${ui.esc(l.id)}"` })}
      </span></td>
    </tr>`;
  }).join('');
  return panel('Block lists', `${addForm('list-add', `${grow(ui.input({ name: 'name', placeholder: 'Name', attrs: 'required' }), 'basis-40 sm:w-48 sm:flex-none')}${grow(ui.input({ name: 'url', type: 'url', placeholder: 'https://example.com/list.txt', mono: true, attrs: 'required' }), 'min-w-56')}`, {
    button: ui.button('Add list', { icon: 'plus', type: 'submit' }),
    hint: 'Every entry also blocks its subdomains',
  })}
    <div class="-mx-3"><table class="w-full border-separate border-spacing-0 text-sm">
      <thead><tr><th class="${TH} w-0"></th><th class="${TH}">List</th><th class="${TH} text-right">Domains</th><th class="${TH} text-right max-md:hidden">Skipped</th><th class="${TH} max-md:hidden">Updated</th><th class="${TH} w-0"></th></tr></thead>
      <tbody>${rows}</tbody>
    </table></div>`, { right: ui.button('Refresh all', { icon: 'refresh-cw', action: 'list-refresh-all' }) });
}

function ruleCard(kind) {
  const isAllow = kind === 'allow';
  const items = S[kind];
  const rows = items.map((r) => `
    <li class="group flex items-center gap-3 ${ui.cls.row} py-1 pr-1 pl-3">
      <span data-tip="${ui.esc(r.domain)}" class="min-w-0 ${ui.cls.rowText} font-mono text-sm text-text">${ui.esc(r.domain)}</span>
      ${r.subdomains ? ui.chip('+ subdomains') : ''}
      <span class="ml-auto">${removeButton('rule-remove', `data-kind="${kind}" data-id="${ui.esc(r.id)}"`)}</span>
    </li>`).join('');
  return panel(isAllow ? 'Allowlist' : 'Custom blocks', `${addForm('rule-add', `${grow(ui.input({ name: 'domain', placeholder: 'example.com', mono: true, attrs: 'required' }))}<span class="shrink-0">${ui.checkbox({ name: 'sub', label: 'Subdomains', checked: !isAllow })}</span>`, { attrs: `data-kind="${kind}"` })}
    ${items.length ? `<ul class="-mx-3 flex flex-col gap-0.5">${rows}</ul>` : ''}`, {
    lead: icon(isAllow ? 'shield-check' : 'ban', `size-5 ${isAllow ? 'text-green' : 'text-red'}`),
    right: `<span class="text-xs text-overlay1">${items.length} domains</span>`,
  });
}

const mono = (s, strong = true) => `<span class="font-mono${strong ? ' text-text' : ''}">${ui.esc(s)}</span>`;

function inactive(m) {
  if (m.enabled) return '';
  const mode = S.status.blocking.mode;
  return mode === 'on' ? ' (disabled)' : ` (blocking ${mode})`;
}

const MATCH = {
  rewrite: (m) => ['arrow-left-right', 'text-blue', `Rewritten to ${mono(m.detail)} by ${mono(m.rule, false)}`],
  allow: (m) => ['shield-check', 'text-green', `Allowed by rule ${mono(m.rule)}`],
  block: (m) => ['ban', m.enabled ? 'text-red' : 'text-overlay0', `Custom block ${mono(m.rule)}${inactive(m)}`],
  list: (m) => ['list', m.enabled ? 'text-red' : 'text-overlay0', `Listed in <span class="text-text">${ui.esc(m.rule)}</span>${inactive(m)}`],
  special: (m) => ['circle-dot', 'text-teal', `Special-use name ${mono(m.rule)}${m.detail ? `, answered ${ui.esc(m.detail)}` : ''}`],
  canary: (m) => ['shield-x', 'text-red', `Canary ${mono(m.rule)}${m.detail ? `, answered ${ui.esc(m.detail)}` : ''}`],
  local: (m) => ['house', 'text-teal', `Local domain ${mono(m.rule)}${m.detail ? `, ${ui.esc(m.detail)}` : ''}`],
};
const VERDICT = {
  rewritten: 'rewritten',
  blocked: 'blocked',
  allowed: 'allowed, forwarded upstream',
  local: 'forwarded to the router',
  special: 'answered locally',
};

function testResult() {
  if (!S.test) return '';
  const lines = (S.test.matches ?? []).map((m) => (MATCH[m.kind] ?? ((x) => ['circle-dot', 'text-overlay1', `${ui.esc(x.kind)} ${mono(x.rule)}`]))(m));
  lines.push(['arrow-right', 'text-overlay1', `Result: <span class="text-text">${ui.esc(VERDICT[S.test.verdict] ?? S.test.verdict)}</span>`]);
  return `<ul class="flex flex-col gap-2 text-sm">${lines.map(([ic, color, text]) => `<li class="flex items-center gap-2">${icon(ic, `size-4 shrink-0 ${color}`)}<span>${text}</span></li>`).join('')}</ul>`;
}

function viewRules() {
  const test = addForm('test', grow(ui.input({ name: 'domain', value: S.test?.domain ?? '', placeholder: 'ads.example.com', icon: 'flask-conical', mono: true, attrs: 'required' }), 'min-w-0'), {
    button: ui.button('Test', { variant: 'primary', type: 'submit' }),
    hint: 'Checks rewrites, rules, and every list, enabled or not, without querying upstream',
  });
  return `<div class="flex flex-col gap-4">
    ${panel('Test a domain', `${test}${testResult()}`)}
    <div class="grid grid-cols-1 items-start gap-4 md:grid-cols-2">${ruleCard('allow')}${ruleCard('block')}</div>
  </div>`;
}

function viewRewrites() {
  const rows = S.rewrites.map((r) => `
    <li class="group grid grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)_3.5rem_auto] items-center gap-3 ${ui.cls.row} py-1 pr-1 pl-3 sm:grid-cols-[minmax(0,20rem)_auto_minmax(0,1fr)_4rem_auto]">
      <span data-tip="${ui.esc(r.pattern)}" class="${ui.cls.rowText} font-mono text-sm text-text">${ui.esc(r.pattern)}</span>
      ${icon('arrow-right', 'size-4 text-overlay1')}
      <span data-tip="${ui.esc(r.answer)}" class="${ui.cls.rowText} font-mono text-sm">${ui.esc(r.answer)}</span>
      <span class="justify-self-start">${ui.chip(answerType(r.answer), { mono: true })}</span>
      ${removeButton('rewrite-remove', `data-id="${ui.esc(r.id)}"`)}
    </li>`).join('');
  const controls = `${grow(ui.input({ name: 'pattern', placeholder: '*.mydomain.dev', mono: true, attrs: 'required' }), 'min-w-44')}
    ${icon('arrow-right', 'size-4 text-overlay1 max-sm:hidden')}
    ${grow(ui.input({ name: 'answer', placeholder: '192.168.1.20 or host.name', mono: true, attrs: 'required' }), 'min-w-44')}`;
  return panel('Rewrites', `${addForm('rewrite-add', controls, { hint: 'Exact names beat wildcards. A name rewritten to IPv4 only answers AAAA, HTTPS, and SVCB with an empty response.' })}
    ${S.rewrites.length ? `<ul class="-mx-3 flex flex-col gap-0.5">${rows}</ul>` : ''}`);
}

function viewAdvanced() {
  const s = S.settings;
  const c = S.status.cache;
  return `<div class="grid grid-cols-1 items-start gap-4 lg:grid-cols-2">
    ${panel('Local network', `${settingRow('Router', 'Plain DNS server for local names and private reverse lookups', `<span class="block w-44">${ui.input({ name: 'router', value: s.router, placeholder: '192.168.1.1', mono: true })}</span>`, 'router')}
      ${settingBlock('Local domains', tagField('local_domains'))}`)}
    ${panel('Cache', `${numRow('Size', 'Maximum entries', 'cache_size', 'entries')}
      ${numRow('Minimum TTL', 'Raise shorter TTLs to this', 'cache_min_ttl', 's')}
      ${numRow('Maximum TTL', 'Cap longer TTLs at this', 'cache_max_ttl', 's')}`, {
      right: `<span class="text-xs text-overlay1 tabular-nums">${fmt(c.entries)} entries, ${Math.round(c.hit_rate * 100)}% hits today</span>${ui.button('Flush', { icon: 'eraser', size: 'sm', action: 'cache-flush' })}`,
    })}
    ${panel('Protection', `${settingRow('Rebinding protection', 'Drop upstream answers that point at private addresses', ui.toggle({ name: 'flip:rebinding', checked: s.rebinding, label: 'Rebinding protection' }))}
      ${settingBlock('Exempt domains', tagField('rebinding_exempt'), s.rebinding ? '' : 'opacity-50')}
      ${settingRow('Block iCloud Private Relay', 'Answer NXDOMAIN for mask.icloud.com so Safari keeps using kamui', ui.toggle({ name: 'flip:relay_canary', checked: s.relay_canary, label: 'Block iCloud Private Relay' }))}`, { span: 'lg:col-span-2' })}
  </div>`;
}

function viewDNS(sub) {
  const body = { upstreams: viewUpstreams, lists: viewLists, rules: viewRules, rewrites: viewRewrites, advanced: viewAdvanced }[sub]();
  return gallery(`${pageHead('DNS settings', tabs('dns', DNS_TABS, sub))}${body}`);
}

function viewApp(sub) {
  const s = S.settings;
  const body = {
    general: () => panel('', `${settingRow('Block list refresh', 'How often every list is downloaded again', presetPicker('refresh_hours', [[0, 'Off'], [6, '6 h'], [12, '12 h'], [24, '24 h'], [168, '7 d']], 'hours', { min: 1 }), 'refresh_hours')}
      ${numRow('Query log size', 'The oldest queries are dropped past this many rows', 'log_size', 'rows', { min: 1000, max: 1000000, step: 1000 })}
      ${settingRow('Snapshot', 'How often the query log and statistics are saved to disk', presetPicker('snapshot_minutes', [[0, 'Shutdown only'], [5, '5 min'], [10, '10 min'], [30, '30 min'], [60, '1 h']], 'minutes', { min: 0, max: 1440 }), 'snapshot_minutes')}`),
    stats: () => panel('', `${numRow('Retention', 'Statistics older than this are deleted', 'retention_days', 'days', { min: 1 })}
      ${settingRow('Dashboard window', 'Range for the daily chart and the top domains', ui.segmented({ name: 'window', value: String(s.stats_window_days), options: [{ value: '7', label: '7 days' }, { value: '30', label: '30 days' }] }))}`),
    clients: () => {
      const rows = s.clients.map((c, i) => `
        <li class="group flex items-center gap-3 ${ui.cls.row} py-1 pr-1 pl-3">
          <span class="w-32 shrink-0 font-mono text-sm">${ui.esc(c.ip)}</span>
          <span class="min-w-0 flex-1 ${ui.cls.rowText} text-text">${ui.esc(c.name)}</span>
          ${removeButton('client-remove', `data-i="${i}"`)}
        </li>`).join('');
      const controls = `${grow(ui.input({ name: 'ip', placeholder: '192.168.1.50', mono: true, attrs: 'required' }), 'basis-40 sm:w-48 sm:flex-none')}${grow(ui.input({ name: 'name', placeholder: 'Name', attrs: 'required' }), 'min-w-32')}`;
      return panel('Client names', `${addForm('client-add', controls, { hint: 'Shown in the query log instead of raw IPs' })}
        ${s.clients.length ? `<ul class="-mx-3 grid gap-0.5 md:grid-cols-2 2xl:grid-cols-3">${rows}</ul>` : ''}`);
    },
    backup: () => panel('', `${settingRow('Export', 'Settings, lists, rules, and rewrites as config.json', ui.button('Export', { icon: 'download', action: 'export' }))}
      ${settingRow('Import', 'Replace the current configuration from a file', `${ui.button('Import', { icon: 'upload', action: 'import' })}<input type="file" accept="application/json" data-change="import" class="hidden">`)}`),
  }[sub]();
  return gallery(`${pageHead('App settings', tabs('app', APP_TABS, sub))}${body}`);
}

const VIEWS = { dashboard: viewDashboard, querylog: viewQueryLog, dns: viewDNS, app: viewApp };

function renderNav(page) {
  const nav = $('#nav');
  nav.innerHTML = NAV.map(([key, label, ic]) => {
    const on = page === key;
    return `<a href="#/${key}" class="flex h-9 items-center gap-2 rounded-full px-4 text-sm whitespace-nowrap transition-colors max-sm:flex-1 max-sm:justify-center max-sm:px-3 ${on ? 'bg-surface0 text-text' : 'text-overlay1 hover:text-text'}" ${on ? 'aria-current="page"' : `data-tip="${label}" data-tip-side="bottom"`}>${icon(ic, `size-4 ${on ? 'text-mauve' : ''}`)}<span class="${on ? 'max-sm:hidden' : 'max-xl:hidden'}">${label}</span></a>`;
  }).join('');
  ui.icons(nav);
}

function renderStateTag() {
  const tag = $('#state-tag');
  tag.innerHTML = stateTag();
  ui.icons(tag);
}

let shown = '';
function render() {
  const r = route();
  const key = `${r.page}/${r.sub ?? ''}`;
  renderNav(r.page);
  renderStateTag();
  const view = $('#view');
  const scrolls = key === shown ? [...view.querySelectorAll('[data-scroll]')].map((el) => el.scrollTop) : [];
  view.innerHTML = S.status && needs(r).every((k) => S[k]) ? VIEWS[r.page](r.sub) : '';
  view.querySelectorAll('[data-scroll]').forEach((el, i) => { el.scrollTop = scrolls[i] ?? 0; });
  shown = key;
  ui.icons(view);
  const col = S.tipDay !== null && $(`[data-day="${S.tipDay}"]`);
  if (col) showTip(col);
}

function renderStatus() {
  if (route().page === 'dashboard') return render();
  renderStateTag();
}

function renderLog() {
  const body = $('#log-body');
  if (!body) return;
  body.innerHTML = logRows();
  const foot = $('#log-foot');
  foot.innerHTML = logFoot();
  ui.icons(body);
  ui.icons(foot);
}

const toast = (msg, ic = 'circle-check') => ui.toast(msg, { tone: 'green', icon: ic });
const warn = (msg) => ui.toast(msg, { tone: 'red', icon: 'triangle-alert', timeout: 4500 });
const fail = (e) => ui.toast(e.message, { tone: 'red', icon: 'circle-alert', timeout: 4500 });

async function run(fn, renderOnError = true) {
  try {
    await fn();
  } catch (e) {
    fail(e);
    if (!renderOnError) return;
  }
  render();
}

async function load() {
  const r = route();
  S.tipDay = null;
  try {
    await Promise.all(['status', ...needs(r)].map((k) => loaders[k]()));
  } catch (e) {
    fail(e);
  }
  render();
}

function formModal({ id, title, icon: ic, tone = 'mauve', label, body, validate = () => true, onSubmit }) {
  const m = ui.modal.open({
    title,
    icon: ic,
    tone,
    size: 'sm',
    body: `<form id="${id}" novalidate class="grid gap-4 pt-1">${body}</form>`,
    footer: `${ui.button('Cancel', { variant: 'ghost', attrs: 'data-close' })}${ui.button(label, { variant: 'primary', type: 'submit', attrs: `form="${id}"` })}`,
  });
  m.el.addEventListener('click', (e) => { if (e.target.closest('[data-close]')) m.close(); });
  m.el.addEventListener('input', (e) => { if (e.target.getAttribute('aria-invalid') === 'true') ui.invalid(e.target); });
  m.el.querySelector('form').addEventListener('submit', (e) => {
    e.preventDefault();
    if (!validate(e.target)) return;
    m.close();
    run(() => onSubmit(e.target));
  });
  m.el.querySelector('input')?.focus();
}

function download(href, name) {
  const a = document.createElement('a');
  a.href = href;
  a.download = name;
  a.click();
}

async function addRule(kind, domain, subdomains) {
  const rule = await api('POST', `rules/${kind}`, { domain, subdomains });
  if (S[kind]) S[kind] = [rule, ...S[kind].filter((r) => r.id !== rule.id)];
  toast(`${kind === 'allow' ? 'Added to allowlist' : 'Added to custom blocks'}: ${rule.domain}`);
}

function ruleModal(kind, domain, reason) {
  const allow = kind === 'allow';
  formModal({
    id: 'rule-form',
    title: `${allow ? 'Unblock' : 'Block'} domain`,
    icon: allow ? 'shield-check' : 'ban',
    tone: allow ? 'green' : 'red',
    label: allow ? 'Unblock' : 'Block',
    body: `<p class="font-mono text-sm text-text wrap-anywhere">${ui.esc(domain)}</p>
      ${reason ? `<p class="text-sm">Blocked by <span class="text-text">${ui.esc(reason)}</span></p>` : ''}
      <span class="-mx-3">${ui.checkbox({ name: 'sub', label: 'Include subdomains', checked: !allow })}</span>`,
    onSubmit: (f) => addRule(kind, domain, checked(f, 'sub')),
  });
}

async function setBlocking(body, msg, ic) {
  S.status.blocking = await api('PUT', 'blocking', body);
  toast(msg, ic);
}

async function saveSettings(change, msg = 'Saved') {
  const next = structuredClone(S.settings);
  change(next);
  S.settings = normSettings(await api('PUT', 'settings', next));
  toast(msg);
}

const commit = (key, value) => run(() => saveSettings((s) => { s[key] = value; }));

const findList = (id) => S.lists.find((l) => l.id === id);
const putList = (l) => { S.lists = S.lists.map((x) => (x.id === l.id ? l : x)); };

const actions = {
  pause: (el) => setBlocking({ pause_seconds: Number(el.dataset.sec) }, 'Blocking paused', 'pause'),
  'pause-custom': () => formModal({
    id: 'pause-form',
    title: 'Pause blocking',
    icon: 'pause',
    label: 'Pause',
    body: ui.field('Duration', ui.number({ name: 'minutes', value: 15, min: 1, unit: 'minutes' })),
    validate: (f) => {
      const input = $('[name="minutes"]', f);
      const ok = Number.isInteger(Number(input.value)) && Number(input.value) >= 1;
      if (!ok) ui.invalid(input, 'Enter a whole number of minutes');
      return ok;
    },
    onSubmit: (f) => setBlocking({ pause_seconds: Number($('[name="minutes"]', f).value) * 60 }, 'Blocking paused', 'pause'),
  }),
  resume: () => setBlocking({ enabled: true }, 'Blocking resumed'),
  'quick-rule': (el) => ruleModal(el.dataset.kind, el.dataset.domain),
  'log-live': () => {
    S.live = !S.live;
    if (S.live) return pollLog();
  },
  'log-more': () => olderLog(),
  'log-export': () => formModal({
    id: 'export-form',
    title: 'Export query log',
    icon: 'download',
    label: 'Download',
    body: ui.field('Format', ui.segmented({ name: 'export-format', value: 'csv', options: [{ value: 'csv', label: 'CSV' }, { value: 'json', label: 'JSON' }] }), { group: true, hint: 'Every entry matching the current filter and search' }),
    onSubmit: (f) => {
      const format = $('[data-name="export-format"]', f).dataset.value;
      download(`/api/querylog/export?${logParams({ format })}`, `querylog.${format}`);
    },
  }),
  'log-rule': (el) => {
    const r = S.log.find((x) => x.id === Number(el.dataset.id));
    const blocked = r.result === 'blocked';
    ruleModal(blocked ? 'allow' : 'block', r.domain, blocked ? r.reason : '');
  },
  'up-move': (el) => saveSettings((s) => {
    const i = Number(el.dataset.i);
    const j = i + Number(el.dataset.dir);
    [s.upstreams[i], s.upstreams[j]] = [s.upstreams[j], s.upstreams[i]];
  }),
  'up-remove': (el) => saveSettings((s) => { s.upstreams.splice(Number(el.dataset.i), 1); }, 'Upstream removed'),
  'tag-remove': (el) => {
    const key = el.closest('[data-tags]').dataset.tags;
    return saveSettings((s) => { s[key] = s[key].filter((v) => v !== el.dataset.value); });
  },
  'list-refresh': async (el) => {
    const l = findList(el.dataset.id);
    toast(`Refreshing ${l.name}`, 'refresh-cw');
    const next = await api('POST', `lists/${encodeURIComponent(l.id)}/refresh`);
    putList(next);
    if (next.error) warn(`${next.name}: ${next.error}`);
    else toast(`${next.name} refreshed`, 'refresh-cw');
  },
  'list-refresh-all': async () => {
    toast('Refreshing all lists', 'refresh-cw');
    S.lists = (await api('POST', 'lists/refresh')) ?? [];
    const failed = S.lists.filter((l) => l.error).length;
    if (failed) warn(`${failed} of ${S.lists.length} lists failed to refresh`);
    else toast('All lists refreshed', 'refresh-cw');
  },
  'list-remove': async (el) => {
    const l = findList(el.dataset.id);
    const ok = await ui.confirm({ title: 'Remove list?', message: `<span class="text-text">${ui.esc(l.name)}</span> and its downloaded copy are removed.`, confirmLabel: 'Remove', danger: true, icon: 'trash-2' });
    if (!ok) return;
    await api('DELETE', `lists/${encodeURIComponent(l.id)}`);
    S.lists = S.lists.filter((x) => x.id !== l.id);
    toast('List removed', 'trash-2');
  },
  'rule-remove': async (el) => {
    const { kind, id } = el.dataset;
    await api('DELETE', `rules/${kind}/${encodeURIComponent(id)}`);
    S[kind] = S[kind].filter((r) => r.id !== id);
  },
  'rewrite-remove': async (el) => {
    await api('DELETE', `rewrites/${encodeURIComponent(el.dataset.id)}`);
    S.rewrites = S.rewrites.filter((r) => r.id !== el.dataset.id);
  },
  'cache-flush': async () => {
    await api('POST', 'cache/flush');
    await loaders.status();
    toast('Cache flushed', 'eraser');
  },
  'client-remove': (el) => saveSettings((s) => { s.clients.splice(Number(el.dataset.i), 1); }),
  export: () => download('/api/config/export', 'config.json'),
};

const changes = {
  blocking: (on) => setBlocking({ enabled: on }, `Blocking turned ${on ? 'on' : 'off'}`),
  list: async (on, id) => {
    const next = await api('PATCH', `lists/${encodeURIComponent(id)}`, { enabled: on });
    putList(next);
    toast(`${next.name} ${next.enabled ? 'enabled' : 'disabled'}`);
  },
  flip: (on, key) => saveSettings((s) => { s[key] = on; }),
  preset: (value, key) => {
    S.customPreset[key] = value === 'custom';
    if (value !== 'custom') return saveSettings((s) => { s[key] = Number(value); });
  },
  window: (value) => saveSettings((s) => { s.stats_window_days = Number(value); }),
  'chart-view': (value) => {
    S.chartView = value;
    S.tipDay = null;
  },
  'log-filter': (value) => {
    S.logFilter = value;
    return loadLog();
  },
};

const submits = {
  'up-add': (f) => saveSettings((s) => { s.upstreams.push(f.get('url').trim()); }, 'Upstream added'),
  'list-add': async (f) => {
    toast('Downloading list', 'download');
    const l = await api('POST', 'lists', { name: f.get('name').trim(), url: f.get('url').trim() });
    S.lists.push(l);
    if (l.error) warn(`${l.name} added, download failed: ${l.error}`);
    else toast('List added and downloaded');
  },
  'rule-add': (f, form) => addRule(form.dataset.kind, f.get('domain').trim().toLowerCase(), checked(form, 'sub')),
  'rewrite-add': async (f) => {
    const r = await api('POST', 'rewrites', { pattern: f.get('pattern').trim().toLowerCase(), answer: f.get('answer').trim() });
    S.rewrites.unshift(r);
    toast('Rewrite added');
  },
  test: async (f) => { S.test = await api('GET', `test?domain=${encodeURIComponent(f.get('domain').trim())}`); },
  'client-add': (f) => saveSettings((s) => { s.clients.push({ ip: f.get('ip').trim(), name: f.get('name').trim() }); }),
};

async function importConfig(file) {
  const text = await file.text();
  JSON.parse(text);
  await api('POST', 'config/import', text);
  Object.assign(S, { settings: null, lists: null, allow: null, block: null, rewrites: null, stats: null, test: null });
  await load();
  toast(`Imported ${file.name}`, 'upload');
}

ui.initDelegation();
ui.tooltip.init();
ui.initScrollObserver();

Object.entries(actions).forEach(([name, fn]) => ui.on(name, (el) => run(() => fn(el))));
ui.on('import', () => $('[data-change="import"]').click());

let searchTimer;
document.addEventListener('ui-change', (e) => {
  const { name = '', value } = e.detail;
  const i = name.indexOf(':');
  const [kind, arg] = i < 0 ? [name, ''] : [name.slice(0, i), name.slice(i + 1)];
  if (kind === 'tab') {
    location.hash = `#/${arg}/${value}`;
  } else if (kind === 'log-search') {
    S.logSearch = value;
    clearTimeout(searchTimer);
    searchTimer = setTimeout(() => loadLog().then(renderLog, fail), 300);
  } else if (changes[kind]) {
    run(() => changes[kind](value, arg));
  }
});

let stepTimer;
document.addEventListener('click', (e) => {
  const step = e.target.closest('[data-setting] [data-ui="step"]');
  if (!step) return;
  const input = step.closest('[data-number]').querySelector('input');
  const key = step.closest('[data-setting]').dataset.setting;
  clearTimeout(stepTimer);
  stepTimer = setTimeout(() => commit(key, Number(input.value)), 500);
});

document.addEventListener('change', (e) => {
  const el = e.target;
  const row = el.closest('[data-setting]');
  if (row && el.matches('input')) {
    clearTimeout(stepTimer);
    commit(row.dataset.setting, el.type === 'number' ? Number(el.value) : el.value.trim());
  }
  if (el.dataset.change === 'import' && el.files.length) importConfig(el.files[0]).catch(fail);
});

document.addEventListener('submit', (e) => {
  const form = e.target;
  if (!form.dataset.submit) return;
  e.preventDefault();
  const bad = [...form.querySelectorAll('input')].find((input) => !input.checkValidity());
  if (bad) {
    bad.setAttribute('aria-invalid', 'true');
    bad.focus();
    return;
  }
  run(() => submits[form.dataset.submit](new FormData(form), form), false);
});

document.addEventListener('input', (e) => {
  if (e.target.closest('[data-submit]') && e.target.getAttribute('aria-invalid') === 'true') e.target.setAttribute('aria-invalid', 'false');
});

document.addEventListener('keydown', (e) => {
  if (e.key !== 'Enter' || !e.target.matches?.('[data-tag-input]')) return;
  const value = e.target.value.trim();
  if (!value) return;
  e.preventDefault();
  const key = e.target.closest('[data-tags]').dataset.tags;
  run(() => saveSettings((s) => { s[key].push(value); }), false).then(() => $(`[data-tags="${key}"] [data-tag-input]`)?.focus());
});

document.addEventListener('pointerover', (e) => {
  const col = e.target.closest('[data-day]');
  if (col) showTip(col);
});
document.addEventListener('focusin', (e) => {
  const col = e.target.closest('[data-day]');
  if (col) showTip(col);
});
document.addEventListener('pointerout', (e) => {
  if (e.target.closest('[data-day]') && !e.relatedTarget?.closest('[data-day]')) hideTip();
});

window.addEventListener('hashchange', () => {
  ui.popover.close();
  ui.tooltip.hide();
  load();
});

async function poll() {
  if (document.visibilityState !== 'visible') return;
  const page = route().page;
  try {
    await Promise.all([
      loaders.status(),
      page === 'dashboard' && loaders.stats(),
      page === 'querylog' && S.live && S.log && pollLog(),
    ]);
  } catch {
    return;
  }
  if (route().page === page) renderStatus();
}

setInterval(poll, 5000);
document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'visible') poll();
});

let expiring = false;
setInterval(() => {
  if (S.status?.blocking.mode !== 'paused') return;
  if (Date.now() < S.status.blocking.until) {
    document.querySelectorAll('[data-countdown]').forEach((el) => { el.textContent = remaining(); });
    return;
  }
  if (expiring) return;
  expiring = true;
  loaders.status()
    .then(() => {
      if (S.status.blocking.mode === 'on') toast('Blocking resumed');
      renderStatus();
    })
    .catch(() => {})
    .finally(() => { expiring = false; });
}, 1000);

load();
