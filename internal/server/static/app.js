const $ = (s, el = document) => el.querySelector(s);
const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);
const icon = (name, cls = 'size-4') => `<i data-lucide="${name}" class="${cls}"></i>`;
const fmt = (n) => n.toLocaleString('en-US');
const compact = (n) => Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 2 }).format(n);

const cls = {
  card: 'bg-mantle rounded-3xl p-5 sm:p-6',
  input: 'h-10 rounded-full bg-surface0 px-4 text-sm text-text placeholder:text-overlay0 outline-none focus:ring-2 focus:ring-mauve/40 pointer-coarse:text-[16px]',
  btn: 'inline-flex items-center justify-center gap-2 h-10 px-4 rounded-full bg-surface0 text-sm text-text whitespace-nowrap hover:bg-surface1 cursor-pointer transition',
  primary: 'inline-flex items-center justify-center gap-2 h-10 px-5 rounded-full bg-mauve text-sm font-medium text-crust whitespace-nowrap hover:opacity-90 cursor-pointer transition',
  iconBtn: 'grid place-items-center size-8 shrink-0 rounded-full text-overlay1 hover:text-text hover:bg-surface0 cursor-pointer transition disabled:opacity-30 disabled:cursor-not-allowed',
  chip: 'inline-flex items-center h-6 px-2.5 rounded-full bg-surface0 text-xs text-subtext1 whitespace-nowrap',
  h2: 'font-display text-lg text-text',
  hint: 'text-xs text-overlay1',
};
const addBtn = (label = 'Add') => `<button class="${cls.btn}">${icon('plus', 'size-4 text-mauve')}${label}</button>`;

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
  refreshCustom: false,
};

const LOG_LIMIT = 100;
const LOG_KEEP = 5 * LOG_LIMIT;
const REFRESH_PRESETS = [0, 6, 12, 24, 168];
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
const hoursLabel = (h) => (h === 168 ? '7 d' : `${h} h`);

function hostOf(u) {
  try {
    return new URL(u.includes('://') ? u.replace(/^tls:/, 'https:') : `dns://${u}`).hostname;
  } catch {
    return u;
  }
}

const toggle = (on, attrs) => `<button role="switch" aria-checked="${on}" ${attrs} class="group relative inline-flex h-6 w-10 shrink-0 rounded-full bg-surface1 aria-checked:bg-green cursor-pointer transition"><span class="absolute top-1 left-1 size-4 rounded-full bg-text group-aria-checked:translate-x-4 group-aria-checked:bg-crust transition"></span></button>`;

const checkbox = (name, checked, label) => `<label class="group inline-flex items-center gap-2 text-sm cursor-pointer select-none">
  <input type="checkbox" name="${name}" ${checked ? 'checked' : ''} class="sr-only">
  <span class="grid place-items-center size-5 rounded-md bg-surface1 text-crust group-has-checked:bg-mauve group-has-focus-visible:ring-2 group-has-focus-visible:ring-mauve/40 transition">${icon('check', 'size-3.5 opacity-0 group-has-checked:opacity-100')}</span>${label}</label>`;

const pills = (items, active, action, size = 'h-8') => items.map(([k, label, ic]) => {
  const on = String(k) === String(active);
  return `<button data-action="${action}" data-to="${k}" title="${label}" class="flex items-center gap-2 ${size} px-4 rounded-full text-sm whitespace-nowrap cursor-pointer transition ${on ? 'bg-surface0 text-text' : 'text-overlay1 hover:text-text'}">${ic ? icon(ic, `size-4 ${on ? 'text-mauve' : ''}`) : ''}<span class="${ic && !on ? 'max-sm:hidden' : ''}">${label}</span></button>`;
}).join('');

const pillBar = (items, active, action, bg = 'bg-mantle') => `<div class="inline-flex max-w-full gap-1 p-1 ${bg} rounded-full overflow-x-auto">${pills(items, active, action)}</div>`;

const chip = (text, action, attrs) => `<span class="inline-flex items-center gap-1 h-8 pl-3.5 pr-1 rounded-full bg-surface0 font-mono text-xs text-text">${esc(text)}<button data-action="${action}" ${attrs} class="grid place-items-center size-6 rounded-full text-overlay1 hover:text-text hover:bg-surface1 cursor-pointer">${icon('x', 'size-3.5')}</button></span>`;

const field = (label, hint, control) => `<div class="flex flex-wrap items-center justify-between gap-x-6 gap-y-2 py-3"><div class="min-w-0 flex-1 basis-40"><p class="text-sm text-text">${label}</p><p class="${cls.hint}">${hint}</p></div>${control}</div>`;

const numInput = (key, value, unit, attrs = '') => `<label class="flex items-center h-10 rounded-full bg-surface0 pr-4 focus-within:ring-2 focus-within:ring-mauve/40">
  <input type="number" data-bind="${key}" value="${value}" ${attrs} class="w-24 bg-transparent pl-4 pr-2 text-right text-sm text-text tabular-nums outline-none [appearance:textfield] [&::-webkit-inner-spin-button]:appearance-none pointer-coarse:text-[16px]">
  <span class="${cls.hint}">${unit}</span></label>`;

const pageHead = (title, sub, right = '') => `<div class="flex flex-wrap items-end justify-between gap-4 mb-6"><div><h1 class="font-display text-2xl text-text">${title}</h1>${sub ? `<p class="text-sm mt-1">${sub}</p>` : ''}</div>${right}</div>`;

const settingCard = (title, hint, control) => `<section class="${cls.card}"><h2 class="${cls.h2}">${title}</h2><p class="${cls.hint} mt-1">${hint}</p><div class="mt-4">${control}</div></section>`;

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
  on: { title: 'Blocking is on', icon: 'shield-check', badge: 'bg-green/15 text-green' },
  paused: { title: 'Blocking is paused', icon: 'shield-alert', badge: 'bg-yellow/15 text-yellow' },
  off: { title: 'Blocking is off', icon: 'shield-off', badge: 'bg-red/15 text-red' },
};

function stateTag() {
  const mode = S.status?.blocking.mode;
  if (!mode || mode === 'on') return '';
  const text = mode === 'paused' ? `Paused <span data-countdown class="tabular-nums">${remaining()}</span>` : 'Blocking off';
  return `<button data-action="goto" data-to="#/dashboard" class="inline-flex items-center gap-1.5 h-7 px-3 rounded-full text-xs font-medium cursor-pointer ${STATES[mode].badge}">${icon(STATES[mode].icon, 'size-3.5')}${text}</button>`;
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
  const pauseBtn = (attrs, label) => `<button ${attrs} class="h-8 px-3 rounded-full text-sm text-text hover:bg-surface0 cursor-pointer transition">${label}</button>`;
  const controls = {
    on: `<div class="flex items-center gap-1 p-1 rounded-full bg-base">
        <span class="${cls.hint} pl-3 pr-1">Pause</span>
        ${pauseBtn('data-action="pause" data-sec="300"', '5 min')}
        ${pauseBtn('data-action="pause" data-sec="3600"', '1 h')}
        ${pauseBtn('data-action="pause-custom"', 'Custom')}
      </div>`,
    paused: `<span class="text-sm">Resumes in <span data-countdown class="text-text tabular-nums">${remaining()}</span></span>
      <button data-action="resume" class="${cls.primary}">${icon('play')}Resume now</button>`,
    off: '',
  }[mode];
  return `<section class="${cls.card} lg:col-span-2 2xl:col-span-4 flex flex-wrap items-center gap-x-6 gap-y-5">
    <span class="grid place-items-center size-14 shrink-0 rounded-full ${st.badge}">${icon(st.icon, 'size-7')}</span>
    <div class="flex-1 min-w-48">
      <h1 class="font-display text-2xl text-text">${st.title}</h1>
      <ul class="flex flex-wrap gap-x-5 gap-y-1 mt-1.5 text-sm">${facts.map(([ic, t]) => `<li class="flex items-center gap-1.5">${icon(ic, 'size-3.5 text-overlay0')}${esc(t)}</li>`).join('')}</ul>
    </div>
    ${controls ? `<div class="flex flex-wrap items-center gap-3 max-sm:order-last max-sm:w-full">${controls}</div>` : ''}
    ${toggle(mode !== 'off', 'data-action="block-toggle" aria-label="Blocking"')}
  </section>`;
}

function tile(label, ic, value, sub, meter) {
  return `<div class="${cls.card}">
    <div class="flex items-center justify-between"><p class="text-sm text-overlay1">${label}</p>${icon(ic, 'size-4 text-overlay0')}</div>
    <p class="mt-3 text-3xl sm:text-4xl font-semibold text-text">${value}</p>
    ${meter === undefined
      ? `<p class="mt-2 ${cls.hint}">${sub}</p>`
      : `<div class="flex items-center gap-3 mt-2"><span class="flex-1 h-1.5 rounded-full bg-blocked/20"><span class="block h-full rounded-full bg-blocked" style="width:${meter * 100}%"></span></span><span class="${cls.hint}">${sub}</span></div>`}
  </div>`;
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
  const legend = `<div class="flex items-center gap-4 text-xs">
    <span class="flex items-center gap-1.5"><span class="size-2.5 rounded-sm bg-allowed"></span>Allowed</span>
    <span class="flex items-center gap-1.5"><span class="size-2.5 rounded-sm bg-blocked"></span>Blocked</span></div>`;
  const chart = `<div id="chart" class="flex-1 grid grid-cols-[auto_1fr] grid-rows-[minmax(14rem,1fr)_auto] gap-x-3 mt-6">
      <div class="flex flex-col justify-between -my-1.5 text-right text-xs text-overlay0 tabular-nums">${[max, max / 2, 0].map((v) => `<span>${compact(v)}</span>`).join('')}</div>
      <div class="relative">
        ${['top-0', 'top-1/2', 'top-full'].map((t) => `<span class="absolute inset-x-0 h-px bg-surface0 ${t}"></span>`).join('')}
        ${empty ? `<p class="absolute inset-0 grid place-items-center text-sm text-overlay1">No queries yet</p>` : ''}
        <div class="relative flex h-full items-end">${data.map((d, i) => `
          <div data-day="${i}" tabindex="0" class="group flex-1 flex h-full items-end justify-center px-[2px] outline-none">
            <div class="flex w-full max-w-6 flex-col gap-[2px] group-hover:brightness-125 group-focus-visible:brightness-125 transition" style="height:${(d.total / max) * 100}%">
              <span class="rounded-t-[4px] bg-blocked" style="flex:${d.blocked}"></span>
              <span class="bg-allowed" style="flex:${d.total - d.blocked}"></span>
            </div>
          </div>`).join('')}
        </div>
        <div id="chart-tip" class="hidden absolute top-2 z-10 -translate-x-1/2 pointer-events-none rounded-2xl bg-surface0 px-3 py-2 text-xs shadow-lg whitespace-nowrap"></div>
      </div>
      <span></span>
      <div class="flex mt-2 text-xs text-overlay0">${data.map((d, i) => `<span class="flex-1 text-center">${n <= 7 || i % 5 === (data.length - 1) % 5 ? label(d) : ''}</span>`).join('')}</div>
    </div>`;
  const table = `<div class="mt-4 max-h-72 overflow-y-auto"><table class="w-full text-sm">
      <thead><tr class="text-left text-xs text-overlay1 [&>th]:px-2 [&>th]:py-2 [&>th]:font-medium"><th>Date</th><th class="text-right">Allowed</th><th class="text-right">Blocked</th><th class="text-right">Total</th><th class="text-right">Blocked %</th></tr></thead>
      <tbody>${[...data].reverse().map((d) => `<tr class="[&>td]:px-2 [&>td]:py-1.5 tabular-nums"><td>${d.date.toLocaleDateString('en-US', { month: 'short', day: 'numeric' })}</td><td class="text-right text-text">${fmt(d.total - d.blocked)}</td><td class="text-right text-text">${fmt(d.blocked)}</td><td class="text-right">${fmt(d.total)}</td><td class="text-right">${pct(d.blocked, d.total)}</td></tr>`).join('')}</tbody>
    </table></div>`;
  return `<section class="${cls.card} lg:col-span-2 flex flex-col">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div><h2 class="${cls.h2}">Queries per day</h2><p class="${cls.hint} mt-0.5">Last ${n} days</p></div>
      <div class="flex items-center gap-4">${legend}${pillBar([['chart', 'Chart', 'chart-column'], ['table', 'Table', 'table-2']], S.chartView, 'chart-view', 'bg-base')}</div>
    </div>
    ${S.chartView === 'chart' ? chart : table}
  </section>`;
}

function showTip(col) {
  const tip = $('#chart-tip');
  if (!tip) return;
  S.tipDay = Number(col.dataset.day);
  const d = chartDays()[S.tipDay];
  const row = (color, value, name) => `<div class="flex items-center gap-2 mt-1"><span class="w-3 h-0.5 rounded-full ${color}"></span><span class="text-text font-medium tabular-nums">${fmt(value)}</span><span class="text-overlay1">${name}</span></div>`;
  tip.innerHTML = `<p class="text-overlay1">${d.date.toLocaleDateString('en-US', { weekday: 'short', month: 'short', day: 'numeric' })}</p>
    ${row('bg-allowed', d.total - d.blocked, 'allowed')}${row('bg-blocked', d.blocked, `blocked, ${pct(d.blocked, d.total)}`)}`;
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
  return `<section class="${cls.card}">
    <div class="flex items-baseline justify-between mb-4"><h2 class="${cls.h2}">${title}</h2><span class="${cls.hint}">Last ${S.stats.window_days} days</span></div>
    ${rows.length ? '' : `<p class="py-6 text-center text-sm text-overlay1">${emptyText}</p>`}
    <ol class="space-y-1">${rows.map(({ domain, count }, i) => `
      <li class="flex items-center gap-3 rounded-2xl px-2 py-1.5 hover:bg-surface0/40">
        <span class="w-4 text-right text-xs text-overlay0 tabular-nums">${i + 1}</span>
        <div class="flex-1 min-w-0">
          <div class="flex items-baseline justify-between gap-3"><span class="font-mono text-sm text-text truncate">${esc(domain)}</span><span class="text-sm tabular-nums">${fmt(count)}</span></div>
          <div class="h-1 mt-1.5 rounded-full ${allow ? 'bg-blocked/15' : 'bg-allowed/15'}"><div class="h-full rounded-full ${allow ? 'bg-blocked' : 'bg-allowed'}" style="width:${(count / max) * 100}%"></div></div>
        </div>
        <button data-action="quick-rule" data-kind="${kind}" data-domain="${esc(domain)}" title="${allow ? 'Unblock' : 'Block'} ${esc(domain)}" class="${cls.iconBtn} ${allow ? 'hover:text-green' : 'hover:text-red'}">${icon(allow ? 'shield-check' : 'ban')}</button>
      </li>`).join('')}
    </ol>
  </section>`;
}

function viewDashboard() {
  const { today, totals, retention_days: retention } = S.stats;
  return `<div class="grid grid-cols-1 gap-4 lg:grid-cols-2 2xl:grid-cols-4">
    ${blockingCard()}
    <div class="grid grid-cols-2 xl:grid-cols-4 gap-4 lg:col-span-2 2xl:col-span-4">
      ${tile('Queries today', 'activity', fmt(today.total), 'since midnight')}
      ${tile('Blocked today', 'shield-ban', fmt(today.blocked), pct(today.blocked, today.total), ratio(today.blocked, today.total))}
      ${tile('Total queries', 'history', compact(totals.total), `last ${retention} days`)}
      ${tile('Total blocked', 'calendar-range', compact(totals.blocked), pct(totals.blocked, totals.total), ratio(totals.blocked, totals.total))}
    </div>
    ${chartCard()}
    ${topCard('Top queried domains', S.stats.top_queried ?? [], 'block', 'No queries yet')}
    ${topCard('Top blocked domains', S.stats.top_blocked ?? [], 'allow', 'Nothing blocked yet')}
  </div>`;
}

const RESULT = {
  allowed: ['circle-check', 'text-green', 'Allowed'],
  blocked: ['ban', 'text-red', 'Blocked'],
  rewritten: ['arrow-left-right', 'text-blue', 'Rewritten'],
  local: ['house', 'text-teal', 'Local'],
};

function logRows() {
  if (!S.log.length) {
    const filtered = S.logSearch.trim() || S.logFilter !== 'all';
    return `<tr><td colspan="8" class="py-12 text-center text-overlay1">${filtered ? 'No queries match' : 'No queries yet'}</td></tr>`;
  }
  return S.log.map((r) => {
    const [ic, color, label] = RESULT[r.result];
    const name = r.client_name;
    const action = r.result === 'blocked' ? ['Unblock', 'shield-check', 'hover:text-green'] : r.result === 'allowed' ? ['Block', 'ban', 'hover:text-red'] : null;
    const upstream = r.cached ? 'cache' : r.upstream ? `${esc(r.upstream)} <span class="text-overlay0">${duration(r.duration_ms)}</span>` : '';
    return `<tr class="hover:bg-surface0/40 max-sm:[&>td]:h-14 [&>td]:h-11 [&>td]:px-2 sm:[&>td]:px-3 [&>td:first-child]:rounded-l-full [&>td:last-child]:rounded-r-full">
      <td class="max-sm:hidden font-mono text-xs text-overlay1 tabular-nums whitespace-nowrap">${clock(r.time)}</td>
      <td class="max-sm:hidden whitespace-nowrap">${name ? `<span class="text-text">${esc(name)}</span> <span class="text-xs text-overlay0 max-lg:hidden">${esc(r.client)}</span>` : `<span class="font-mono text-xs">${esc(r.client)}</span>`}</td>
      <td class="w-full max-w-0" title="${esc(r.domain)}">
        <p class="font-mono text-text truncate">${esc(r.domain)}</p>
        <p class="sm:hidden text-xs text-overlay1 truncate">${esc(name || r.client)} <span class="font-mono tabular-nums text-overlay0">${clock(r.time)}</span></p>
      </td>
      <td class="max-sm:hidden"><span class="${cls.chip} font-mono">${esc(r.type)}</span></td>
      <td class="whitespace-nowrap" title="${label}"><span class="inline-flex items-center gap-1.5">${icon(ic, `size-4 ${color}`)}<span class="max-md:hidden">${label}</span></span></td>
      <td class="max-md:hidden text-overlay1 max-w-64 truncate" title="${esc(r.reason)}">${esc(r.reason)}</td>
      <td class="max-md:hidden text-right whitespace-nowrap tabular-nums text-overlay1">${upstream}</td>
      <td class="text-right">${action ? `<button data-action="log-rule" data-id="${r.id}" title="${action[0]}" class="${cls.iconBtn} ${action[2]} ml-auto">${icon(action[1])}</button>` : ''}</td>
    </tr>`;
  }).join('');
}

const logFoot = () => (S.logEnd ? '' : `<div class="flex justify-center pt-3"><button data-action="log-more" class="${cls.btn}">${icon('chevrons-down')}Load older</button></div>`);

function viewQueryLog() {
  const filters = [['all', 'All', 'list'], ['blocked', 'Blocked', 'ban'], ['allowed', 'Allowed', 'circle-check'], ['rewritten', 'Rewritten', 'arrow-left-right']];
  return `
    ${pageHead('Query log', `Keeping the last ${fmt(S.settings.log_size)} queries`, `<button data-action="log-export" class="${cls.btn}">${icon('download')}Export</button>`)}
    <div class="flex flex-wrap items-center gap-3">
      <label class="relative flex-1 min-w-56">
        ${icon('search', 'size-4 absolute left-4 top-1/2 -translate-y-1/2 text-overlay0')}
        <input id="log-search" value="${esc(S.logSearch)}" placeholder="Search domain or client" class="${cls.input} w-full pl-10">
      </label>
      ${pillBar(filters, S.logFilter, 'log-filter')}
      <button data-action="log-live" class="${cls.btn}">
        <span class="size-2 rounded-full ${S.live ? 'bg-green animate-pulse' : 'bg-overlay0'}"></span>${S.live ? 'Live' : 'Paused'}
      </button>
    </div>
    <div class="${cls.card} px-2 sm:px-3 py-3 mt-4">
      <table class="w-full text-sm">
        <thead><tr class="text-left text-xs text-overlay1 [&>th]:px-2 sm:[&>th]:px-3 [&>th]:pb-2 [&>th]:font-medium">
          <th class="max-sm:hidden">Time</th><th class="max-sm:hidden">Client</th><th>Domain</th><th class="max-sm:hidden">Type</th><th>Result</th><th class="max-md:hidden">Reason</th><th class="max-md:hidden text-right">Upstream</th><th class="w-10"></th>
        </tr></thead>
        <tbody id="log-body">${logRows()}</tbody>
      </table>
      <div id="log-foot">${logFoot()}</div>
    </div>`;
}

function viewUpstreams() {
  const ups = S.settings.upstreams;
  const n = ups.length;
  const latency = (u) => S.status.upstreams?.find((x) => x.url === u)?.latency_ms;
  return `
    <div class="grid grid-cols-1 lg:grid-cols-3 gap-4 items-start">
      <section class="${cls.card} lg:col-span-2">
        <h2 class="${cls.h2}">Upstream servers</h2>
        <p class="${cls.hint} mt-1">Queried in order. The next server joins after the hedge delay or when one fails.</p>
        <ol class="mt-4 space-y-1">${ups.map((u, i) => `
          <li class="flex items-center gap-3 rounded-full pl-2 pr-1 py-1 hover:bg-surface0/40">
            <span class="grid place-items-center size-7 shrink-0 rounded-full bg-surface0 text-xs text-text">${i + 1}</span>
            <span class="flex-1 min-w-0 font-mono text-sm text-text truncate">${esc(u)}</span>
            <span class="${cls.chip} max-sm:hidden">${upstreamProto(u)}</span>
            <span class="w-12 text-right text-xs text-overlay1 tabular-nums max-sm:hidden">${latency(u) ? `${Math.round(latency(u))} ms` : ''}</span>
            <span class="flex">
              <button data-action="up-move" data-i="${i}" data-dir="-1" ${i === 0 ? 'disabled' : ''} title="Move up" class="${cls.iconBtn}">${icon('arrow-up')}</button>
              <button data-action="up-move" data-i="${i}" data-dir="1" ${i === n - 1 ? 'disabled' : ''} title="Move down" class="${cls.iconBtn}">${icon('arrow-down')}</button>
              <button data-action="up-remove" data-i="${i}" title="Remove" class="${cls.iconBtn} hover:text-red">${icon('trash-2')}</button>
            </span>
          </li>`).join('')}
        </ol>
        <form data-submit="up-add" class="flex gap-2 mt-4">
          <input name="url" required placeholder="https://host/dns-query, tls://host, or 192.168.1.1" class="${cls.input} flex-1 min-w-0 font-mono">
          ${addBtn()}
        </form>
      </section>
      <div class="grid grid-cols-1 gap-4">
        <section class="${cls.card}">
          <h2 class="${cls.h2}">Bootstrap</h2>
          <p class="${cls.hint} mt-1">Plain DNS servers that resolve upstream hostnames.</p>
          <div class="flex flex-wrap gap-2 mt-4">${S.settings.bootstrap.map((b, i) => chip(b, 'chip-remove', `data-key="bootstrap" data-i="${i}"`)).join('')}</div>
          <form data-submit="chip-add" data-key="bootstrap" class="flex gap-2 mt-3">
            <input name="value" required placeholder="IP address" class="${cls.input} flex-1 min-w-0 font-mono">
            ${addBtn()}
          </form>
        </section>
        <section class="${cls.card}">
          <h2 class="${cls.h2}">Timing</h2>
          ${field('Hedge delay', 'Wait before asking the next server', numInput('hedge_delay_ms', S.settings.hedge_delay_ms, 'ms', 'min="50" step="50"'))}
          ${field('Timeout', 'Answer SERVFAIL after', numInput('timeout_ms', S.settings.timeout_ms, 'ms', 'min="500" step="250"'))}
        </section>
      </div>
    </div>`;
}

function viewLists() {
  const h = S.settings.refresh_hours;
  const every = h ? `Refreshed every ${hoursLabel(h)}` : 'Automatic refresh is off';
  return `
    <section class="${cls.card}">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div><h2 class="${cls.h2}">Block lists</h2><p class="${cls.hint} mt-1">${every}. Every entry also blocks its subdomains.</p></div>
        <button data-action="list-refresh-all" class="${cls.btn}">${icon('refresh-cw')}Refresh all</button>
      </div>
      <form data-submit="list-add" class="flex flex-wrap gap-2 mt-4">
        <input name="name" required placeholder="Name" class="${cls.input} w-48 max-sm:flex-1">
        <input name="url" type="url" required placeholder="https://example.com/list.txt" class="${cls.input} flex-1 min-w-56 font-mono">
        ${addBtn('Add list')}
      </form>
      <table class="w-full text-sm mt-4">
        <thead><tr class="text-left text-xs text-overlay1 [&>th]:px-3 [&>th]:pb-2 [&>th]:font-medium">
          <th class="w-0"></th><th>List</th><th class="text-right">Domains</th><th class="text-right max-md:hidden">Skipped</th><th class="max-md:hidden">Updated</th><th class="w-0"></th>
        </tr></thead>
        <tbody>${S.lists.map((l) => {
          const updated = l.updated ? ago(Date.now() - l.updated) : 'Never';
          return `
          <tr class="hover:bg-surface0/40 [&>td]:px-3 [&>td]:py-3 [&>td:first-child]:rounded-l-full [&>td:last-child]:rounded-r-full">
            <td>${toggle(l.enabled, `data-action="list-toggle" data-id="${esc(l.id)}" aria-label="${esc(l.name)}"`)}</td>
            <td class="w-full max-w-0 ${l.enabled ? '' : 'opacity-60'}">
              <p class="text-text truncate">${esc(l.name)}${l.error ? icon('triangle-alert', 'inline size-3.5 ml-2 -mt-0.5 text-peach md:hidden') : ''}</p>
              <p class="font-mono text-xs text-overlay0 truncate" title="${esc(l.url)}">${esc(l.url)}</p>
            </td>
            <td class="text-right tabular-nums text-text ${l.enabled ? '' : 'opacity-60'}">${fmt(l.domains)}</td>
            <td class="text-right tabular-nums max-md:hidden ${l.enabled ? '' : 'opacity-60'}">${fmt(l.skipped)}</td>
            <td class="whitespace-nowrap max-md:hidden">${l.error
              ? `<span class="inline-flex items-center gap-1.5">${icon('triangle-alert', 'size-4 text-peach')}${updated}</span><p class="text-xs text-overlay0">${esc(l.error)}</p>`
              : updated}</td>
            <td><span class="flex">
              <button data-action="list-refresh" data-id="${esc(l.id)}" title="Refresh" class="${cls.iconBtn}">${icon('refresh-cw')}</button>
              <button data-action="list-remove" data-id="${esc(l.id)}" title="Remove" class="${cls.iconBtn} hover:text-red">${icon('trash-2')}</button>
            </span></td>
          </tr>`;
        }).join('')}
        </tbody>
      </table>
    </section>`;
}

function ruleCard(kind) {
  const isAllow = kind === 'allow';
  const items = S[kind];
  return `<section class="${cls.card}">
    <div class="flex items-center gap-2">${icon(isAllow ? 'shield-check' : 'ban', `size-5 ${isAllow ? 'text-green' : 'text-red'}`)}<h2 class="${cls.h2}">${isAllow ? 'Allowlist' : 'Custom blocks'}</h2><span class="${cls.hint} ml-auto">${items.length} domains</span></div>
    <form data-submit="rule-add" data-kind="${kind}" class="flex flex-wrap items-center gap-x-4 gap-y-2 mt-4">
      <input name="domain" required placeholder="example.com" class="${cls.input} flex-1 min-w-48 font-mono">
      ${checkbox('sub', !isAllow, 'Subdomains')}
      ${addBtn()}
    </form>
    <ul class="mt-3 space-y-0.5">${items.map((r) => `
      <li class="flex items-center gap-3 rounded-full pl-4 pr-1 py-1 hover:bg-surface0/40">
        <span class="font-mono text-sm text-text truncate">${esc(r.domain)}</span>
        ${r.subdomains ? `<span class="${cls.chip}">+ subdomains</span>` : ''}
        <button data-action="rule-remove" data-kind="${kind}" data-id="${esc(r.id)}" title="Remove" class="${cls.iconBtn} ml-auto hover:text-red">${icon('trash-2')}</button>
      </li>`).join('')}
    </ul>
  </section>`;
}

const mono = (s, strong = true) => `<span class="font-mono${strong ? ' text-text' : ''}">${esc(s)}</span>`;

function inactive(m) {
  if (m.enabled) return '';
  const mode = S.status.blocking.mode;
  return mode === 'on' ? ' (disabled)' : ` (blocking ${mode})`;
}

const MATCH = {
  rewrite: (m) => ['arrow-left-right', 'text-blue', `Rewritten to ${mono(m.detail)} by ${mono(m.rule, false)}`],
  allow: (m) => ['shield-check', 'text-green', `Allowed by rule ${mono(m.rule)}`],
  block: (m) => ['ban', m.enabled ? 'text-red' : 'text-overlay0', `Custom block ${mono(m.rule)}${inactive(m)}`],
  list: (m) => ['list', m.enabled ? 'text-red' : 'text-overlay0', `Listed in <span class="text-text">${esc(m.rule)}</span>${inactive(m)}`],
  special: (m) => ['circle-dot', 'text-teal', `Special-use name ${mono(m.rule)}${m.detail ? `, answered ${esc(m.detail)}` : ''}`],
  canary: (m) => ['shield-x', 'text-red', `Canary ${mono(m.rule)}${m.detail ? `, answered ${esc(m.detail)}` : ''}`],
  local: (m) => ['house', 'text-teal', `Local domain ${mono(m.rule)}${m.detail ? `, ${esc(m.detail)}` : ''}`],
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
  const lines = (S.test.matches ?? []).map((m) => (MATCH[m.kind] ?? ((x) => ['circle-dot', 'text-overlay1', `${esc(x.kind)} ${mono(x.rule)}`]))(m));
  lines.push(['arrow-right', 'text-overlay1', `Result: <span class="text-text">${esc(VERDICT[S.test.verdict] ?? S.test.verdict)}</span>`]);
  return `<ul class="mt-4 space-y-2 text-sm">${lines.map(([ic, color, text]) => `<li class="flex items-center gap-2">${icon(ic, `size-4 shrink-0 ${color}`)}<span>${text}</span></li>`).join('')}</ul>`;
}

function viewRules() {
  return `
    <section class="${cls.card}">
      <h2 class="${cls.h2}">Test a domain</h2>
      <p class="${cls.hint} mt-1">Checks rewrites, rules, and every list, enabled or not. Nothing is sent upstream.</p>
      <form data-submit="test" class="flex gap-2 mt-4">
        <label class="relative flex-1 min-w-0">
          ${icon('flask-conical', 'size-4 absolute left-4 top-1/2 -translate-y-1/2 text-overlay0')}
          <input name="domain" required value="${esc(S.test?.domain ?? '')}" placeholder="ads.example.com" class="${cls.input} w-full pl-10 font-mono">
        </label>
        <button class="${cls.primary}">Test</button>
      </form>
      ${testResult()}
    </section>
    <div class="grid grid-cols-1 md:grid-cols-2 gap-4 mt-4 items-start">${ruleCard('allow')}${ruleCard('block')}</div>`;
}

function viewRewrites() {
  return `
    <section class="${cls.card}">
      <h2 class="${cls.h2}">Rewrites</h2>
      <p class="${cls.hint} mt-1">Exact names beat wildcards. A name rewritten to IPv4 only answers AAAA, HTTPS, and SVCB with an empty response.</p>
      <form data-submit="rewrite-add" class="flex flex-wrap items-center gap-2 mt-4">
        <input name="pattern" required placeholder="*.mydomain.dev" class="${cls.input} flex-1 min-w-44 font-mono">
        ${icon('arrow-right', 'size-4 text-overlay0 max-sm:hidden')}
        <input name="answer" required placeholder="192.168.1.20 or host.name" class="${cls.input} flex-1 min-w-44 font-mono">
        ${addBtn()}
      </form>
      <ul class="mt-4 space-y-0.5">${S.rewrites.map((r) => `
        <li class="grid grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)_3.5rem_auto] sm:grid-cols-[minmax(0,20rem)_auto_minmax(0,1fr)_4rem_auto] items-center gap-3 rounded-full pl-4 pr-1 py-1 hover:bg-surface0/40">
          <span class="font-mono text-sm text-text truncate">${esc(r.pattern)}</span>
          ${icon('arrow-right', 'size-4 text-overlay0')}
          <span class="font-mono text-sm truncate">${esc(r.answer)}</span>
          <span class="${cls.chip} font-mono justify-self-start">${answerType(r.answer)}</span>
          <button data-action="rewrite-remove" data-id="${esc(r.id)}" title="Remove" class="${cls.iconBtn} hover:text-red">${icon('trash-2')}</button>
        </li>`).join('')}
      </ul>
    </section>`;
}

function viewAdvanced() {
  const s = S.settings;
  const c = S.status.cache;
  return `
    <div class="grid grid-cols-1 lg:grid-cols-2 gap-4 items-start">
      <section class="${cls.card}">
        <h2 class="${cls.h2}">Local network</h2>
        <p class="${cls.hint} mt-1">Local names and private reverse lookups go to the router.</p>
        ${field('Router', 'Plain DNS address', `<input data-bind="router" value="${esc(s.router)}" placeholder="192.168.1.1" class="${cls.input} w-44 font-mono">`)}
        <p class="text-sm text-text mt-3">Local domains</p>
        <div class="flex flex-wrap items-center gap-2 mt-2">
          ${s.local_domains.map((d, i) => chip(d, 'chip-remove', `data-key="local_domains" data-i="${i}"`)).join('')}
          <form data-submit="chip-add" data-key="local_domains"><input name="value" required placeholder="Add domain" class="${cls.input} h-8 w-36 font-mono"></form>
        </div>
      </section>
      <section class="${cls.card}">
        <div class="flex items-center justify-between gap-3">
          <div><h2 class="${cls.h2}">Cache</h2><p class="${cls.hint} mt-1">${fmt(c.entries)} entries, ${Math.round(c.hit_rate * 100)}% hit rate today</p></div>
          <button data-action="cache-flush" class="${cls.btn}">${icon('eraser')}Flush</button>
        </div>
        ${field('Size', 'Maximum entries', numInput('cache_size', s.cache_size, 'entries'))}
        ${field('Minimum TTL', 'Raise shorter TTLs to this', numInput('cache_min_ttl', s.cache_min_ttl, 's'))}
        ${field('Maximum TTL', 'Cap longer TTLs at this', numInput('cache_max_ttl', s.cache_max_ttl, 's'))}
      </section>
      <section class="${cls.card} lg:col-span-2">
        <h2 class="${cls.h2}">Protection</h2>
        ${field('Rebinding protection', 'Drop upstream answers that point at private addresses', toggle(s.rebinding, 'data-action="flip" data-key="rebinding" aria-label="Rebinding protection"'))}
        <div class="flex flex-wrap items-center gap-2 pb-3 ${s.rebinding ? '' : 'opacity-50'}">
          <span class="${cls.hint} mr-1">Exempt</span>
          ${s.rebinding_exempt.map((d, i) => chip(d, 'chip-remove', `data-key="rebinding_exempt" data-i="${i}"`)).join('')}
          <form data-submit="chip-add" data-key="rebinding_exempt"><input name="value" required placeholder="Add domain" class="${cls.input} h-8 w-36 font-mono"></form>
        </div>
        ${field('Block iCloud Private Relay', 'Answer NXDOMAIN for mask.icloud.com so Safari keeps using kamui', toggle(s.relay_canary, 'data-action="flip" data-key="relay_canary" aria-label="Block iCloud Private Relay"'))}
      </section>
    </div>`;
}

function viewDNS(sub) {
  const body = { upstreams: viewUpstreams, lists: viewLists, rules: viewRules, rewrites: viewRewrites, advanced: viewAdvanced }[sub]();
  return `${pageHead('DNS settings', '', pillBar(DNS_TABS, sub, 'dns-tab'))}${body}`;
}

function viewApp(sub) {
  const s = S.settings;
  const refreshOpts = [[0, 'Off'], [6, '6 h'], [12, '12 h'], [24, '24 h'], [168, '7 d'], ['custom', 'Custom']];
  const custom = S.refreshCustom || !REFRESH_PRESETS.includes(s?.refresh_hours);
  const body = {
    general: () => `<div class="grid grid-cols-1 lg:grid-cols-2 gap-4 items-start">
      ${settingCard('Block list refresh', 'How often every list is downloaded again.', `<div class="flex flex-wrap items-center gap-3">${pillBar(refreshOpts, custom ? 'custom' : s.refresh_hours, 'set-refresh', 'bg-base')}${custom ? numInput('refresh_hours', s.refresh_hours, 'hours', 'min="1"') : ''}</div>`)}
      ${settingCard('Query log size', 'The oldest queries are dropped past this many rows.', numInput('log_size', s.log_size, 'rows', 'min="1000" max="1000000" step="1000"'))}
    </div>`,
    stats: () => `<div class="grid grid-cols-1 lg:grid-cols-2 gap-4 items-start">
      ${settingCard('Retention', 'Statistics older than this are deleted.', numInput('retention_days', s.retention_days, 'days', 'min="1"'))}
      ${settingCard('Dashboard window', 'Range for the daily chart and the top domains.', pillBar([[7, '7 days'], [30, '30 days']], s.stats_window_days, 'set-window', 'bg-base'))}
    </div>`,
    clients: () => `<section class="${cls.card}">
      <h2 class="${cls.h2}">Client names</h2>
      <p class="${cls.hint} mt-1">Shown in the query log instead of raw IPs.</p>
      <form data-submit="client-add" class="flex flex-wrap gap-2 mt-4">
        <input name="ip" required placeholder="192.168.1.50" class="${cls.input} w-48 font-mono">
        <input name="name" required placeholder="Name" class="${cls.input} flex-1 min-w-32">
        ${addBtn()}
      </form>
      <ul class="mt-4 grid gap-1 md:grid-cols-2 2xl:grid-cols-3">${s.clients.map((c, i) => `
        <li class="flex items-center gap-3 rounded-full pl-4 pr-1 py-1 hover:bg-surface0/40">
          <span class="font-mono text-sm w-32">${esc(c.ip)}</span>
          <span class="flex-1 text-text">${esc(c.name)}</span>
          <button data-action="client-remove" data-i="${i}" title="Remove" class="${cls.iconBtn} hover:text-red">${icon('trash-2')}</button>
        </li>`).join('')}
      </ul>
    </section>`,
    backup: () => `<div class="grid grid-cols-1 lg:grid-cols-2 gap-4 items-start">
      ${settingCard('Export', 'Download settings, lists, rules, and rewrites as config.json.', `<button data-action="export" class="${cls.btn}">${icon('download')}Export</button>`)}
      ${settingCard('Import', 'Replace the current configuration from a file.', `<label class="${cls.btn}">${icon('upload')}Import<input type="file" accept="application/json" data-change="import" class="hidden"></label>`)}
    </div>`,
  }[sub]();
  return `${pageHead('App settings', '', pillBar(APP_TABS, sub, 'app-tab'))}${body}`;
}

const VIEWS = { dashboard: viewDashboard, querylog: viewQueryLog, dns: viewDNS, app: viewApp };

function render() {
  const r = route();
  $('#nav').innerHTML = pills(NAV, r.page, 'nav', 'h-9');
  $('#state-tag').innerHTML = stateTag();
  $('#view').innerHTML = S.status && needs(r).every((k) => S[k]) ? VIEWS[r.page](r.sub) : '';
  lucide.createIcons();
  const col = S.tipDay !== null && $(`[data-day="${S.tipDay}"]`);
  if (col) showTip(col);
}

function renderStatus() {
  if (route().page === 'dashboard') return render();
  $('#state-tag').innerHTML = stateTag();
  lucide.createIcons();
}

function renderLog() {
  const body = $('#log-body');
  if (!body) return;
  body.innerHTML = logRows();
  $('#log-foot').innerHTML = logFoot();
  lucide.createIcons();
}

let toastTimer;
function toast(msg, ic = 'check', error = false) {
  const t = $('#toast');
  t.innerHTML = `${icon(ic, `size-4 shrink-0 ${error ? 'text-red' : 'text-green'}`)}<span class="min-w-0">${esc(msg)}</span>`;
  lucide.createIcons();
  t.classList.toggle('ring-1', error);
  t.classList.toggle('ring-red/40', error);
  t.classList.remove('opacity-0', 'translate-y-4');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.add('opacity-0', 'translate-y-4'), error ? 4500 : 2200);
}
const fail = (e) => toast(e.message, 'circle-alert', true);

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

function modal(html, onOk) {
  const d = $('#modal');
  d.innerHTML = `<form method="dialog" class="space-y-4">${html}</form>`;
  lucide.createIcons();
  d.returnValue = '';
  d.onclose = () => {
    if (d.returnValue !== 'ok') return;
    const f = new FormData($('form', d));
    run(() => onOk(f));
  };
  d.showModal();
}

const modalButtons = (ok) => `<div class="flex justify-end gap-2 pt-2"><button value="cancel" formnovalidate class="${cls.btn}">Cancel</button><button value="ok" class="${cls.primary}">${ok}</button></div>`;

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
  modal(`
    <h2 class="${cls.h2}">${allow ? 'Unblock' : 'Block'} domain</h2>
    <p class="font-mono text-sm text-text break-all">${esc(domain)}</p>
    ${reason ? `<p class="text-sm">Blocked by <span class="text-text">${esc(reason)}</span></p>` : ''}
    ${checkbox('sub', !allow, 'Include subdomains')}
    ${modalButtons(allow ? 'Unblock' : 'Block')}`, (f) => addRule(kind, domain, f.has('sub')));
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

const findList = (id) => S.lists.find((l) => l.id === id);
const putList = (l) => { S.lists = S.lists.map((x) => (x.id === l.id ? l : x)); };

const actions = {
  nav: (el) => { location.hash = `#/${el.dataset.to}`; },
  goto: (el) => { location.hash = el.dataset.to; },
  'dns-tab': (el) => { location.hash = `#/dns/${el.dataset.to}`; },
  'app-tab': (el) => { location.hash = `#/app/${el.dataset.to}`; },
  'block-toggle': () => {
    const enabled = S.status.blocking.mode === 'off';
    return setBlocking({ enabled }, `Blocking turned ${enabled ? 'on' : 'off'}`);
  },
  pause: (el) => setBlocking({ pause_seconds: Number(el.dataset.sec) }, 'Blocking paused', 'pause'),
  'pause-custom': () => modal(`
    <h2 class="${cls.h2}">Pause blocking</h2>
    ${numInput('', 15, 'minutes', 'name="min" min="1" required')}
    ${modalButtons('Pause')}`, (f) => setBlocking({ pause_seconds: Number(f.get('min')) * 60 }, 'Blocking paused', 'pause')),
  resume: () => setBlocking({ enabled: true }, 'Blocking resumed'),
  'quick-rule': (el) => ruleModal(el.dataset.kind, el.dataset.domain),
  'chart-view': (el) => {
    S.chartView = el.dataset.to;
    S.tipDay = null;
  },
  'log-filter': (el) => {
    S.logFilter = el.dataset.to;
    return loadLog();
  },
  'log-live': () => {
    S.live = !S.live;
    if (S.live) return pollLog();
  },
  'log-more': () => olderLog(),
  'log-export': () => {
    const format = (value, label, checked = '') => `<label class="flex items-center h-8 px-4 rounded-full text-sm text-overlay1 hover:text-text has-checked:bg-surface0 has-checked:text-text cursor-pointer transition"><input type="radio" name="format" value="${value}" ${checked} class="sr-only">${label}</label>`;
    modal(`
      <h2 class="${cls.h2}">Export query log</h2>
      <p class="text-sm">Downloads every entry matching the current filter and search.</p>
      <div class="inline-flex gap-1 p-1 bg-base rounded-full">${format('csv', 'CSV', 'checked')}${format('json', 'JSON')}</div>
      ${modalButtons('Download')}`, (f) => {
      download(`/api/querylog/export?${logParams({ format: f.get('format') })}`, `querylog.${f.get('format')}`);
    });
  },
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
  'chip-remove': (el) => saveSettings((s) => { s[el.dataset.key].splice(Number(el.dataset.i), 1); }),
  'list-toggle': async (el) => {
    const l = findList(el.dataset.id);
    const next = await api('PATCH', `lists/${encodeURIComponent(l.id)}`, { enabled: !l.enabled });
    putList(next);
    toast(`${next.name} ${next.enabled ? 'enabled' : 'disabled'}`);
  },
  'list-refresh': async (el) => {
    const l = findList(el.dataset.id);
    toast(`Refreshing ${l.name}`, 'refresh-cw');
    const next = await api('POST', `lists/${encodeURIComponent(l.id)}/refresh`);
    putList(next);
    if (next.error) toast(`${next.name}: ${next.error}`, 'triangle-alert', true);
    else toast(`${next.name} refreshed`, 'refresh-cw');
  },
  'list-refresh-all': async () => {
    toast('Refreshing all lists', 'refresh-cw');
    S.lists = (await api('POST', 'lists/refresh')) ?? [];
    const failed = S.lists.filter((l) => l.error).length;
    if (failed) toast(`${failed} of ${S.lists.length} lists failed to refresh`, 'triangle-alert', true);
    else toast('All lists refreshed', 'refresh-cw');
  },
  'list-remove': (el) => {
    const l = findList(el.dataset.id);
    modal(`<h2 class="${cls.h2}">Remove list</h2><p class="text-sm">Remove <span class="text-text">${esc(l.name)}</span> and its downloaded copy?</p>${modalButtons('Remove')}`, async () => {
      await api('DELETE', `lists/${encodeURIComponent(l.id)}`);
      S.lists = S.lists.filter((x) => x.id !== l.id);
      toast('List removed', 'trash-2');
    });
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
  flip: (el) => saveSettings((s) => { s[el.dataset.key] = !s[el.dataset.key]; }),
  'set-refresh': (el) => {
    S.refreshCustom = el.dataset.to === 'custom';
    if (!S.refreshCustom) return saveSettings((s) => { s.refresh_hours = Number(el.dataset.to); });
  },
  'set-window': (el) => saveSettings((s) => { s.stats_window_days = Number(el.dataset.to); }),
  'client-remove': (el) => saveSettings((s) => { s.clients.splice(Number(el.dataset.i), 1); }),
  export: () => download('/api/config/export', 'config.json'),
};

const submits = {
  'up-add': (f) => saveSettings((s) => { s.upstreams.push(f.get('url').trim()); }, 'Upstream added'),
  'chip-add': (f, form) => saveSettings((s) => { s[form.dataset.key].push(f.get('value').trim()); }),
  'list-add': async (f) => {
    toast('Downloading list', 'download');
    const l = await api('POST', 'lists', { name: f.get('name').trim(), url: f.get('url').trim() });
    S.lists.push(l);
    if (l.error) toast(`${l.name} added, download failed: ${l.error}`, 'triangle-alert', true);
    else toast('List added and downloaded');
  },
  'rule-add': (f, form) => addRule(form.dataset.kind, f.get('domain').trim().toLowerCase(), f.has('sub')),
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

document.addEventListener('click', (e) => {
  const el = e.target.closest('[data-action]');
  if (!el || !actions[el.dataset.action]) return;
  e.preventDefault();
  run(() => actions[el.dataset.action](el));
});

document.addEventListener('submit', (e) => {
  const form = e.target;
  if (!form.dataset.submit) return;
  e.preventDefault();
  run(() => submits[form.dataset.submit](new FormData(form), form), false);
});

document.addEventListener('change', (e) => {
  const el = e.target;
  if (el.dataset.bind) {
    const value = el.type === 'number' ? Number(el.value) : el.value.trim();
    run(() => saveSettings((s) => { s[el.dataset.bind] = value; }));
  }
  if (el.dataset.change === 'import' && el.files.length) importConfig(el.files[0]).catch(fail);
});

let searchTimer;
document.addEventListener('input', (e) => {
  if (e.target.id !== 'log-search') return;
  S.logSearch = e.target.value;
  clearTimeout(searchTimer);
  searchTimer = setTimeout(() => loadLog().then(renderLog, fail), 300);
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

window.addEventListener('hashchange', load);

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
