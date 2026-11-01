package store

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"
)

type Filter struct {
	Search    string
	Result    string
	Limit     int
	Before    int64
	ClientIPs []string
}

func (s *Store) Query(ctx context.Context, f Filter) ([]Entry, error) {
	var where []string
	var args []any
	if f.Before > 0 {
		where = append(where, "id < ?")
		args = append(args, f.Before)
	}
	if f.Result != "" && f.Result != "all" {
		where = append(where, "result = ?")
		args = append(args, f.Result)
	}
	if f.Search != "" {
		like := "%" + escapeLike(strings.ToLower(f.Search)) + "%"
		cond := []string{`domain LIKE ? ESCAPE '\'`, `client LIKE ? ESCAPE '\'`}
		args = append(args, like, like)
		if len(f.ClientIPs) > 0 {
			cond = append(cond, "client IN ("+strings.TrimSuffix(strings.Repeat("?,", len(f.ClientIPs)), ",")+")")
			for _, ip := range f.ClientIPs {
				args = append(args, ip)
			}
		}
		where = append(where, "("+strings.Join(cond, " OR ")+")")
	}
	q := `SELECT id, time, client, domain, qtype, result, reason, upstream, cached, rcode, duration FROM querylog`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, f.Limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.Time, &e.Client, &e.Domain, &e.Type, &e.Result, &e.Reason, &e.Upstream, &e.Cached, &e.Rcode, &e.DurationMs); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

type Totals struct {
	Total   int64 `json:"total"`
	Blocked int64 `json:"blocked"`
}

type Day struct {
	Date    string `json:"date"`
	Total   int64  `json:"total"`
	Blocked int64  `json:"blocked"`
}

type DomainCount struct {
	Domain string `json:"domain"`
	Count  int64  `json:"count"`
}

type Stats struct {
	WindowDays    int           `json:"window_days"`
	RetentionDays int           `json:"retention_days"`
	Today         Totals        `json:"today"`
	Totals        Totals        `json:"totals"`
	Days          []Day         `json:"days"`
	TopQueried    []DomainCount `json:"top_queried"`
	TopBlocked    []DomainCount `json:"top_blocked"`
}

const topN = 7

func (s *Store) Stats(ctx context.Context, windowDays, retentionDays int) (*Stats, error) {
	s.flushMu.RLock()
	defer s.flushMu.RUnlock()

	now := time.Now()
	today := now.Format(time.DateOnly)
	retentionStart := windowStart(now, retentionDays)
	windowFrom := windowStart(now, windowDays)

	daily := map[string]*counts{}
	rows, err := s.db.QueryContext(ctx, `SELECT date, total, blocked FROM daily WHERE date >= ?`, retentionStart)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var date string
		c := &counts{}
		if err := rows.Scan(&date, &c.total, &c.blocked); err != nil {
			rows.Close()
			return nil, err
		}
		daily[date] = c
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	queried, err := s.topDomains(ctx, "queries", windowFrom)
	if err != nil {
		return nil, err
	}
	blocked, err := s.topDomains(ctx, "blocked", windowFrom)
	if err != nil {
		return nil, err
	}

	s.countMu.Lock()
	for date, c := range s.pending.days {
		d := daily[date]
		if d == nil {
			d = &counts{}
			daily[date] = d
		}
		d.total += c.total
		d.blocked += c.blocked
	}
	for date, doms := range s.pending.domains {
		if date < windowFrom {
			continue
		}
		for dom, c := range doms {
			queried[dom] += c.total
			if c.blocked > 0 {
				blocked[dom] += c.blocked
			}
		}
	}
	s.countMu.Unlock()

	st := &Stats{WindowDays: windowDays, RetentionDays: retentionDays, Days: make([]Day, 0, windowDays)}
	for date, c := range daily {
		if date < retentionStart || date > today {
			continue
		}
		st.Totals.Total += c.total
		st.Totals.Blocked += c.blocked
	}
	if c := daily[today]; c != nil {
		st.Today = Totals{Total: c.total, Blocked: c.blocked}
	}
	for i := windowDays - 1; i >= 0; i-- {
		date := now.AddDate(0, 0, -i).Format(time.DateOnly)
		d := Day{Date: date}
		if c := daily[date]; c != nil {
			d.Total, d.Blocked = c.total, c.blocked
		}
		st.Days = append(st.Days, d)
	}
	st.TopQueried = top(queried)
	st.TopBlocked = top(blocked)
	return st, nil
}

func (s *Store) topDomains(ctx context.Context, column, from string) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT domain, SUM(`+column+`) AS n FROM daily_domains WHERE date >= ? GROUP BY domain HAVING n > 0 ORDER BY n DESC LIMIT 100`, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var d string
		var n int64
		if err := rows.Scan(&d, &n); err != nil {
			return nil, err
		}
		out[d] = n
	}
	return out, rows.Err()
}

func top(m map[string]int64) []DomainCount {
	out := make([]DomainCount, 0, len(m))
	for d, n := range m {
		out = append(out, DomainCount{Domain: d, Count: n})
	}
	slices.SortFunc(out, func(a, b DomainCount) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Domain, b.Domain))
	})
	return out[:min(len(out), topN)]
}
