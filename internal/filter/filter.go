package filter

import (
	"net/netip"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/Tanq16/kamui/internal/blocklist"
	"github.com/Tanq16/kamui/internal/config"
)

const dohCanary = "use-application-dns.net"

var relayCanaries = []string{"mask.icloud.com", "mask-h2.icloud.com"}

type Rewrite struct {
	Pattern string
	IPs     []netip.Addr
	Host    string
}

type ruleMatcher struct {
	exact map[string]bool
	sub   map[string]bool
}

func newRuleMatcher(rules []config.Rule) ruleMatcher {
	m := ruleMatcher{exact: map[string]bool{}, sub: map[string]bool{}}
	for _, r := range rules {
		if r.Subdomains {
			m.sub[r.Domain] = true
		} else {
			m.exact[r.Domain] = true
		}
	}
	return m
}

func (m ruleMatcher) first(name string) (string, bool) {
	if m.exact[name] || m.sub[name] {
		return name, true
	}
	for s := range parents(name) {
		if m.sub[s] {
			return s, true
		}
	}
	return "", false
}

func (m ruleMatcher) all(name string) []string {
	var out []string
	if m.exact[name] || m.sub[name] {
		out = append(out, name)
	}
	for s := range parents(name) {
		if m.sub[s] {
			out = append(out, s)
		}
	}
	return out
}

func parents(name string) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for {
			_, rest, found := strings.Cut(name, ".")
			if !found || rest == "" {
				return
			}
			if !yield(rest) {
				return
			}
			name = rest
		}
	}
}

func suffixMatch(set map[string]bool, name string) (string, bool) {
	if set[name] {
		return name, true
	}
	for s := range parents(name) {
		if set[s] {
			return s, true
		}
	}
	return "", false
}

type ruleset struct {
	allow           ruleMatcher
	block           ruleMatcher
	exactRewrites   map[string]*Rewrite
	wildRewrites    map[string]*Rewrite
	localDomains    map[string]bool
	hasRouter       bool
	rebinding       bool
	rebindingExempt map[string]bool
	relayCanary     bool
}

func build(c *config.Config) *ruleset {
	rs := &ruleset{
		allow:           newRuleMatcher(c.Allow),
		block:           newRuleMatcher(c.Block),
		exactRewrites:   map[string]*Rewrite{},
		wildRewrites:    map[string]*Rewrite{},
		localDomains:    map[string]bool{},
		hasRouter:       c.Settings.Router != "",
		rebinding:       c.Settings.Rebinding,
		rebindingExempt: map[string]bool{},
		relayCanary:     c.Settings.RelayCanary,
	}
	for _, d := range c.Settings.LocalDomains {
		rs.localDomains[d] = true
	}
	for _, d := range c.Settings.RebindingExempt {
		rs.rebindingExempt[d] = true
	}
	for _, r := range c.Rewrites {
		target := rs.exactRewrites
		key := r.Pattern
		if base, ok := strings.CutPrefix(r.Pattern, "*."); ok {
			target, key = rs.wildRewrites, base
		}
		rw := target[key]
		if rw == nil {
			rw = &Rewrite{Pattern: r.Pattern}
			target[key] = rw
		}
		if a, err := netip.ParseAddr(r.Answer); err == nil {
			rw.IPs = append(rw.IPs, a)
		} else {
			rw.Host = r.Answer
		}
	}
	return rs
}

type Filter struct {
	lists *blocklist.Manager
	rs    atomic.Pointer[ruleset]
}

func New(lists *blocklist.Manager, c *config.Config) *Filter {
	f := &Filter{lists: lists}
	f.Update(c)
	return f
}

func (f *Filter) Update(c *config.Config) {
	f.rs.Store(build(c))
}

func Normalize(name string) string {
	return strings.TrimSuffix(strings.ToLower(name), ".")
}

func (f *Filter) Canary(name string) (string, bool) {
	if name == dohCanary {
		return "DoH canary", true
	}
	if f.rs.Load().relayCanary {
		if slices.Contains(relayCanaries, name) {
			return "Private Relay canary", true
		}
	}
	return "", false
}

func (f *Filter) Rewrite(name string) (*Rewrite, bool) {
	rs := f.rs.Load()
	if rw, ok := rs.exactRewrites[name]; ok {
		return rw, true
	}
	for s := range parents(name) {
		if rw, ok := rs.wildRewrites[s]; ok {
			return rw, true
		}
	}
	return nil, false
}

func (f *Filter) LocalDomain(name string) (string, bool) {
	return suffixMatch(f.rs.Load().localDomains, name)
}

func (f *Filter) Allowed(name string) (string, bool) {
	if d, ok := f.rs.Load().allow.first(name); ok {
		return "allow rule " + d, true
	}
	return "", false
}

func (f *Filter) Blocked(name string) (string, bool) {
	if d, ok := f.rs.Load().block.first(name); ok {
		return "custom rule " + d, true
	}
	return f.lists.First(name)
}

func (f *Filter) RebindingApplies(name string) bool {
	rs := f.rs.Load()
	if !rs.rebinding {
		return false
	}
	_, exempt := suffixMatch(rs.rebindingExempt, name)
	return !exempt
}
