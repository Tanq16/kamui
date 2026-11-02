package server

import (
	"encoding/csv"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/Tanq16/kamui/internal/blocklist"
	"github.com/Tanq16/kamui/internal/config"
	"github.com/Tanq16/kamui/internal/store"
)

var errNotFound = errors.New("not found")

func (s *Server) update(w http.ResponseWriter, fn func(c *config.Config) error) (*config.Config, bool) {
	c, err := s.d.Config.Update(fn)
	switch {
	case err == nil:
		return c, true
	case errors.Is(err, errNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, config.ErrSave):
		log.Error().Err(err).Msg("failed to save config")
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		badRequest(w, err)
	}
	return nil, false
}

type blockingJSON struct {
	Mode  string `json:"mode"`
	Until int64  `json:"until"`
}

func (s *Server) blocking() blockingJSON {
	b := s.d.Config.Get().Blocking.Effective(time.Now())
	return blockingJSON{Mode: b.Mode, Until: b.Until}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	type listsJSON struct {
		Enabled int `json:"enabled"`
		Total   int `json:"total"`
		Domains int `json:"domains"`
	}
	type upstreamJSON struct {
		URL       string `json:"url"`
		LatencyMs int64  `json:"latency_ms"`
	}
	type cacheJSON struct {
		Entries int     `json:"entries"`
		HitRate float64 `json:"hit_rate"`
	}
	enabled, total, domains := s.d.Lists.Counts()
	ups := []upstreamJSON{}
	for _, l := range s.d.Upstream.Latencies() {
		ups = append(ups, upstreamJSON{URL: l.URL, LatencyMs: int64(math.Round(l.LatencyMs))})
	}
	writeJSON(w, http.StatusOK, struct {
		Version   string         `json:"version"`
		Blocking  blockingJSON   `json:"blocking"`
		Lists     listsJSON      `json:"lists"`
		Upstreams []upstreamJSON `json:"upstreams"`
		Cache     cacheJSON      `json:"cache"`
	}{
		Version:   s.d.Version,
		Blocking:  s.blocking(),
		Lists:     listsJSON{Enabled: enabled, Total: total, Domains: domains},
		Upstreams: ups,
		Cache:     cacheJSON{Entries: s.d.Cache.Len(), HitRate: s.d.Cache.HitRate()},
	})
}

func (s *Server) handleBlocking(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled      *bool `json:"enabled"`
		PauseSeconds *int  `json:"pause_seconds"`
	}
	if !decode(w, r, &body) {
		return
	}
	var next config.Blocking
	switch {
	case body.PauseSeconds != nil:
		if *body.PauseSeconds < 1 || *body.PauseSeconds > 30*86400 {
			writeError(w, http.StatusBadRequest, "pause_seconds must be between 1 and 2592000")
			return
		}
		next = config.Blocking{Mode: config.ModePaused, Until: time.Now().Add(time.Duration(*body.PauseSeconds) * time.Second).UnixMilli()}
	case body.Enabled != nil && *body.Enabled:
		next = config.Blocking{Mode: config.ModeOn}
	case body.Enabled != nil:
		next = config.Blocking{Mode: config.ModeOff}
	default:
		writeError(w, http.StatusBadRequest, "expected enabled or pause_seconds")
		return
	}
	if _, ok := s.update(w, func(c *config.Config) error {
		c.Blocking = next
		return nil
	}); !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.blocking())
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	set := s.d.Config.Get().Settings
	writeJSON(w, http.StatusOK, s.d.Store.Stats(set.StatsWindowDays, set.RetentionDays))
}

func (s *Server) logFilter(w http.ResponseWriter, q url.Values) (store.Filter, map[string]string, bool) {
	f := store.Filter{Search: strings.TrimSpace(q.Get("search")), Result: q.Get("result")}
	switch f.Result {
	case "", "all", "allowed", "blocked", "rewritten", "local":
	default:
		writeError(w, http.StatusBadRequest, "result must be all, allowed, blocked, rewritten, or local")
		return f, nil, false
	}
	names := map[string]string{}
	needle := strings.ToLower(f.Search)
	for _, c := range s.d.Config.Get().Settings.Clients {
		names[c.IP] = c.Name
		if needle != "" && strings.Contains(strings.ToLower(c.Name), needle) {
			f.ClientIPs = append(f.ClientIPs, c.IP)
		}
	}
	return f, names, true
}

func (s *Server) queryLog(f store.Filter, names map[string]string) []store.Entry {
	entries := s.d.Store.Query(f)
	for i := range entries {
		entries[i].ClientName = names[entries[i].Client]
	}
	return entries
}

func (s *Server) handleQueryLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f, names, ok := s.logFilter(w, q)
	if !ok {
		return
	}
	f.Limit = 100
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		f.Limit = min(n, 500)
	}
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "before must be an entry id")
			return
		}
		f.Before = n
	}
	writeJSON(w, http.StatusOK, struct {
		Entries []store.Entry `json:"entries"`
	}{s.queryLog(f, names)})
}

func (s *Server) handleQueryLogExport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	format := q.Get("format")
	if format != "csv" && format != "json" {
		writeError(w, http.StatusBadRequest, "format must be csv or json")
		return
	}
	f, names, ok := s.logFilter(w, q)
	if !ok {
		return
	}
	f.Limit = s.d.Config.Get().Settings.LogSize
	entries := s.queryLog(f, names)
	w.Header().Set("Content-Disposition", `attachment; filename="querylog.`+format+`"`)
	if format == "json" {
		writeJSON(w, http.StatusOK, entries)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	cw := csv.NewWriter(w)
	cw.Write([]string{"id", "time", "client", "client_name", "domain", "type", "result", "reason", "upstream", "cached", "rcode", "duration_ms"})
	for _, e := range entries {
		cw.Write([]string{
			strconv.FormatInt(e.ID, 10),
			time.UnixMilli(e.Time).Format(time.RFC3339Nano),
			e.Client,
			e.ClientName,
			e.Domain,
			e.Type,
			e.Result,
			e.Reason,
			e.Upstream,
			strconv.FormatBool(e.Cached),
			e.Rcode,
			strconv.FormatFloat(e.DurationMs, 'f', -1, 64),
		})
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		log.Error().Err(err).Msg("failed to export query log")
	}
}

type listJSON struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Enabled bool   `json:"enabled"`
	Domains int    `json:"domains"`
	Skipped int    `json:"skipped"`
	Updated int64  `json:"updated"`
	Error   string `json:"error"`
}

func (s *Server) listView(l config.List) listJSON {
	st := s.d.Lists.Status(l.ID)
	out := listJSON{ID: l.ID, Name: l.Name, URL: l.URL, Enabled: l.Enabled, Domains: st.Domains, Skipped: st.Skipped, Error: st.Error}
	if !st.Updated.IsZero() {
		out.Updated = st.Updated.UnixMilli()
	}
	return out
}

func (s *Server) findList(id string) (config.List, bool) {
	lists := s.d.Config.Get().Lists
	i := slices.IndexFunc(lists, func(l config.List) bool { return l.ID == id })
	if i < 0 {
		return config.List{}, false
	}
	return lists[i], true
}

func (s *Server) listsView() []listJSON {
	out := []listJSON{}
	for _, l := range s.d.Config.Get().Lists {
		out = append(out, s.listView(l))
	}
	return out
}

func logLists(action string, results ...blocklist.Result) {
	for _, r := range results {
		if r.Err != nil {
			log.Warn().Err(r.Err).Str("list", r.Name).Msg("block list " + action + " failed")
			continue
		}
		log.Info().Str("list", r.Name).Int("domains", r.Domains).Int("skipped", r.Skipped).Msg("block list " + action)
	}
}

func (s *Server) SyncLists(lists []config.List) {
	sources := make([]blocklist.Source, 0, len(lists))
	for _, l := range lists {
		sources = append(sources, blocklist.Source{ID: l.ID, Name: l.Name, URL: l.URL, Enabled: l.Enabled})
	}
	logLists("load", s.d.Lists.Sync(sources)...)
}

func (s *Server) RunListRefresh() {
	for {
		hours := s.d.Config.Get().Settings.RefreshHours
		logLists("refresh", s.d.Lists.RefreshStale(s.ctx, time.Duration(hours)*time.Hour)...)
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

func (s *Server) handleLists(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.listsView())
}

func (s *Server) handleListAdd(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	if !decode(w, r, &body) {
		return
	}
	l := config.List{ID: config.NewID(), Name: strings.TrimSpace(body.Name), URL: strings.TrimSpace(body.URL), Enabled: true}
	if _, ok := s.update(w, func(c *config.Config) error {
		if slices.ContainsFunc(c.Lists, func(x config.List) bool { return x.URL == l.URL }) {
			return errors.New("a list with this URL already exists")
		}
		c.Lists = append(c.Lists, l)
		return nil
	}); !ok {
		return
	}
	if res, ok := s.d.Lists.Refresh(r.Context(), l.ID); ok {
		logLists("refresh", res)
	}
	writeJSON(w, http.StatusCreated, s.listView(l))
}

func (s *Server) handleListPatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool   `json:"enabled"`
		Name    *string `json:"name"`
	}
	if !decode(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	c, ok := s.update(w, func(c *config.Config) error {
		i := slices.IndexFunc(c.Lists, func(l config.List) bool { return l.ID == id })
		if i < 0 {
			return errNotFound
		}
		if body.Enabled != nil {
			c.Lists[i].Enabled = *body.Enabled
		}
		if body.Name != nil {
			c.Lists[i].Name = *body.Name
		}
		return nil
	})
	if !ok {
		return
	}
	i := slices.IndexFunc(c.Lists, func(l config.List) bool { return l.ID == id })
	writeJSON(w, http.StatusOK, s.listView(c.Lists[i]))
}

func (s *Server) handleListDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.update(w, func(c *config.Config) error {
		n := len(c.Lists)
		c.Lists = slices.DeleteFunc(c.Lists, func(l config.List) bool { return l.ID == id })
		if len(c.Lists) == n {
			return errNotFound
		}
		return nil
	}); !ok {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListsRefresh(w http.ResponseWriter, r *http.Request) {
	logLists("refresh", s.d.Lists.RefreshAll(r.Context())...)
	writeJSON(w, http.StatusOK, s.listsView())
}

func (s *Server) handleListRefresh(w http.ResponseWriter, r *http.Request) {
	l, found := s.findList(r.PathValue("id"))
	if !found {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if res, ok := s.d.Lists.Refresh(r.Context(), l.ID); ok {
		logLists("refresh", res)
	}
	writeJSON(w, http.StatusOK, s.listView(l))
}

func ruleSlice(c *config.Config, kind string) *[]config.Rule {
	switch kind {
	case "allow":
		return &c.Allow
	case "block":
		return &c.Block
	}
	return nil
}

func (s *Server) handleRules(w http.ResponseWriter, r *http.Request) {
	rules := ruleSlice(s.d.Config.Get(), r.PathValue("kind"))
	if rules == nil {
		writeError(w, http.StatusNotFound, "kind must be allow or block")
		return
	}
	writeJSON(w, http.StatusOK, *rules)
}

func (s *Server) handleRuleAdd(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if ruleSlice(s.d.Config.Get(), kind) == nil {
		writeError(w, http.StatusNotFound, "kind must be allow or block")
		return
	}
	var body struct {
		Domain     string `json:"domain"`
		Subdomains bool   `json:"subdomains"`
	}
	if !decode(w, r, &body) {
		return
	}
	domain, err := config.NormalizeDomain(body.Domain)
	if err != nil {
		badRequest(w, err)
		return
	}
	var saved config.Rule
	status := http.StatusCreated
	if _, ok := s.update(w, func(c *config.Config) error {
		rules := ruleSlice(c, kind)
		if i := slices.IndexFunc(*rules, func(x config.Rule) bool { return x.Domain == domain }); i >= 0 {
			(*rules)[i].Subdomains = body.Subdomains
			saved, status = (*rules)[i], http.StatusOK
			return nil
		}
		saved = config.Rule{ID: config.NewID(), Domain: domain, Subdomains: body.Subdomains}
		*rules = slices.Insert(*rules, 0, saved)
		return nil
	}); !ok {
		return
	}
	writeJSON(w, status, saved)
}

func (s *Server) handleRuleDelete(w http.ResponseWriter, r *http.Request) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	if _, ok := s.update(w, func(c *config.Config) error {
		rules := ruleSlice(c, kind)
		if rules == nil {
			return errNotFound
		}
		n := len(*rules)
		*rules = slices.DeleteFunc(*rules, func(x config.Rule) bool { return x.ID == id })
		if len(*rules) == n {
			return errNotFound
		}
		return nil
	}); !ok {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRewrites(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.d.Config.Get().Rewrites)
}

func (s *Server) handleRewriteAdd(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Pattern string `json:"pattern"`
		Answer  string `json:"answer"`
	}
	if !decode(w, r, &body) {
		return
	}
	rw, err := config.NormalizeRewrite(config.Rewrite{ID: config.NewID(), Pattern: body.Pattern, Answer: body.Answer})
	if err != nil {
		badRequest(w, err)
		return
	}
	if _, ok := s.update(w, func(c *config.Config) error {
		if slices.ContainsFunc(c.Rewrites, func(x config.Rewrite) bool { return x.Pattern == rw.Pattern && x.Answer == rw.Answer }) {
			return errors.New("this rewrite already exists")
		}
		c.Rewrites = slices.Insert(c.Rewrites, 0, rw)
		return nil
	}); !ok {
		return
	}
	writeJSON(w, http.StatusCreated, rw)
}

func (s *Server) handleRewriteDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.update(w, func(c *config.Config) error {
		n := len(c.Rewrites)
		c.Rewrites = slices.DeleteFunc(c.Rewrites, func(x config.Rewrite) bool { return x.ID == id })
		if len(c.Rewrites) == n {
			return errNotFound
		}
		return nil
	}); !ok {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTest(w http.ResponseWriter, r *http.Request) {
	domain, err := config.NormalizeDomain(r.URL.Query().Get("domain"))
	if err != nil {
		badRequest(w, err)
		return
	}
	active := s.d.Config.Get().Blocking.Active(time.Now())
	writeJSON(w, http.StatusOK, s.d.Filter.Test(domain, active))
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.d.Config.Get().Settings)
}

func (s *Server) handleSettingsPut(w http.ResponseWriter, r *http.Request) {
	var body config.Settings
	if !decode(w, r, &body) {
		return
	}
	c, ok := s.update(w, func(c *config.Config) error {
		c.Settings = body
		return nil
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, c.Settings)
}

func (s *Server) handleCacheFlush(w http.ResponseWriter, r *http.Request) {
	s.d.Cache.Flush()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	data, err := s.d.Config.Marshal()
	if err != nil {
		log.Error().Err(err).Msg("failed to export config")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="config.json"`)
	w.Write(data)
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		badRequest(w, err)
		return
	}
	c, err := config.Parse(data)
	if err != nil {
		badRequest(w, err)
		return
	}
	if _, ok := s.update(w, func(next *config.Config) error {
		*next = *c
		return nil
	}); !ok {
		return
	}
	go func() {
		logLists("refresh", s.d.Lists.RefreshStale(s.ctx, time.Duration(c.Settings.RefreshHours)*time.Hour)...)
	}()
	w.WriteHeader(http.StatusNoContent)
}
