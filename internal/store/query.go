package store

import (
	"cmp"
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

func (s *Store) Query(f Filter) []Entry {
	needle := strings.ToLower(f.Search)
	out := []Entry{}
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := len(s.buf)
	for i := range n {
		if len(out) >= f.Limit {
			break
		}
		e := s.buf[(s.head-1-i+n)%n]
		if f.Before > 0 && e.ID >= f.Before {
			continue
		}
		if f.Result != "" && f.Result != "all" && e.Result != f.Result {
			continue
		}
		if needle != "" && !strings.Contains(e.Domain, needle) && !strings.Contains(e.Client, needle) && !slices.Contains(f.ClientIPs, e.Client) {
			continue
		}
		out = append(out, e)
	}
	return out
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

func (s *Store) Stats(windowDays, retentionDays int) *Stats {
	now := time.Now()
	today := now.Format(time.DateOnly)
	retentionStart := windowStart(now, retentionDays)
	windowFrom := windowStart(now, windowDays)
	st := &Stats{WindowDays: windowDays, RetentionDays: retentionDays, Days: make([]Day, 0, windowDays)}
	queried, blocked := map[string]int64{}, map[string]int64{}

	s.mu.RLock()
	for date, c := range s.days {
		if date < retentionStart || date > today {
			continue
		}
		st.Totals.Total += c.Total
		st.Totals.Blocked += c.Blocked
	}
	c := s.days[today]
	st.Today = Totals{Total: c.Total, Blocked: c.Blocked}
	for i := windowDays - 1; i >= 0; i-- {
		date := now.AddDate(0, 0, -i).Format(time.DateOnly)
		c := s.days[date]
		st.Days = append(st.Days, Day{Date: date, Total: c.Total, Blocked: c.Blocked})
	}
	for date, doms := range s.domains {
		if date < windowFrom || date > today {
			continue
		}
		for dom, c := range doms {
			queried[dom] += c.Total
			if c.Blocked > 0 {
				blocked[dom] += c.Blocked
			}
		}
	}
	s.mu.RUnlock()

	st.TopQueried = top(queried)
	st.TopBlocked = top(blocked)
	return st
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
