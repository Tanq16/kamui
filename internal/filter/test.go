package filter

import (
	"net/netip"
	"slices"
	"strings"

	"github.com/miekg/dns"
)

type TestMatch struct {
	Kind    string `json:"kind"`
	Rule    string `json:"rule"`
	Detail  string `json:"detail"`
	Enabled bool   `json:"enabled"`
}

type TestResult struct {
	Domain  string      `json:"domain"`
	Verdict string      `json:"verdict"`
	Matches []TestMatch `json:"matches"`
}

func joinAddrs(addrs []netip.Addr) string {
	s := make([]string, len(addrs))
	for i, a := range addrs {
		s[i] = a.String()
	}
	return strings.Join(s, ", ")
}

func (f *Filter) Test(name string, blocking bool) TestResult {
	rs := f.rs.Load()
	res := TestResult{Domain: name, Matches: []TestMatch{}}
	add := func(kind, rule, detail string, enabled bool, verdict string) {
		res.Matches = append(res.Matches, TestMatch{Kind: kind, Rule: rule, Detail: detail, Enabled: enabled})
		if enabled && res.Verdict == "" && verdict != "" {
			res.Verdict = verdict
		}
	}

	if name == dohCanary {
		add("canary", name, "NXDOMAIN", true, "special")
	}
	if slices.Contains(relayCanaries, name) {
		add("canary", name, "NXDOMAIN", rs.relayCanary, "special")
	}
	if rw, ok := f.Rewrite(name); ok {
		detail := rw.Host
		if detail == "" {
			detail = joinAddrs(rw.IPs)
		}
		add("rewrite", rw.Pattern, detail, true, "rewritten")
	}
	if d, ok := f.LocalDomain(name); ok {
		add("local", d, "forwarded to router", true, "local")
	}
	if PrivatePTR(name, dns.TypePTR) {
		detail := "NXDOMAIN"
		if rs.hasRouter {
			detail = "forwarded to router"
		}
		add("local", "private PTR", detail, true, "local")
	}
	if s, ok := f.Special(name, dns.TypeA); ok {
		if _, local := f.LocalDomain(name); !local {
			detail := dns.RcodeToString[s.Rcode]
			if s.Zone == "localhost" {
				detail = "127.0.0.1, ::1"
			}
			add("special", s.Zone, detail, true, "special")
		}
	}
	for _, d := range rs.allow.all(name) {
		add("allow", d, "", blocking, "allowed")
	}
	for _, d := range rs.block.all(name) {
		add("block", d, "", blocking, "blocked")
	}
	for _, m := range f.lists.All(name) {
		add("list", m.Name, "", blocking && m.Enabled, "blocked")
	}
	if res.Verdict == "" {
		res.Verdict = "allowed"
	}
	return res
}
