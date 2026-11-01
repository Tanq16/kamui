package dnsserver

import (
	"context"
	"net"
	"net/netip"
	"time"

	"github.com/miekg/dns"
	"github.com/rs/zerolog/log"

	"github.com/Tanq16/kamui/internal/filter"
)

const (
	syntheticTTL  = 60
	rewriteDepth  = 8
	resultAllowed = "allowed"
	resultBlocked = "blocked"
	resultRewrite = "rewritten"
	resultLocal   = "local"
)

type query struct {
	qname     string
	name      string
	qtype     uint16
	qclass    uint16
	cd        bool
	opt       *dns.OPT
	client    netip.Addr
	rewritten bool
}

type outcome struct {
	msg      *dns.Msg
	result   string
	reason   string
	upstream string
	cached   bool
}

func synth(rcode int, answer ...dns.RR) *dns.Msg {
	return &dns.Msg{Rcode: rcode, Answer: answer}
}

func hdr(name string, rrtype uint16) dns.RR_Header {
	return dns.RR_Header{Name: name, Rrtype: rrtype, Class: dns.ClassINET, Ttl: syntheticTTL}
}

func addrRR(name string, a netip.Addr) dns.RR {
	if a.Is4() {
		return &dns.A{Hdr: hdr(name, dns.TypeA), A: net.IP(a.AsSlice())}
	}
	return &dns.AAAA{Hdr: hdr(name, dns.TypeAAAA), AAAA: net.IP(a.AsSlice())}
}

func addrAnswer(name string, qtype uint16, addrs []netip.Addr) []dns.RR {
	var out []dns.RR
	for _, a := range addrs {
		if qtype == dns.TypeANY || (qtype == dns.TypeA && a.Is4()) || (qtype == dns.TypeAAAA && a.Is6()) {
			out = append(out, addrRR(name, a))
		}
	}
	return out
}

func blockedMsg(name string, qtype uint16) *dns.Msg {
	switch qtype {
	case dns.TypeA:
		return synth(dns.RcodeSuccess, addrRR(name, netip.IPv4Unspecified()))
	case dns.TypeAAAA:
		return synth(dns.RcodeSuccess, addrRR(name, netip.IPv6Unspecified()))
	}
	return synth(dns.RcodeSuccess)
}

func (q query) upstreamMsg() *dns.Msg {
	m := &dns.Msg{
		Id: dns.Id(), RecursionDesired: true, CheckingDisabled: q.cd,
		Question: []dns.Question{{Name: q.qname, Qtype: q.qtype, Qclass: q.qclass}},
	}
	if q.opt != nil {
		o := *q.opt
		m.Extra = []dns.RR{&o}
	}
	return m
}

func (q query) do() bool {
	return q.opt != nil && q.opt.Do()
}

func (s *Server) resolve(ctx context.Context, q query, depth int, seen map[string]bool) outcome {
	f := s.d.Filter

	if reason, ok := f.Canary(q.name); ok {
		return outcome{msg: synth(dns.RcodeNameError), result: resultLocal, reason: reason}
	}

	if rw, ok := f.Rewrite(q.name); ok {
		if out, handled := s.rewrite(ctx, q, rw, depth, seen); handled {
			return out
		}
	}

	if _, isLocal := f.LocalDomain(q.name); isLocal || filter.PrivatePTR(q.name, q.qtype) {
		reason := "private PTR"
		if isLocal {
			reason = "local domain"
		}
		return s.local(ctx, q, reason)
	}

	if sp, ok := f.Special(q.name, q.qtype); ok {
		return outcome{msg: synth(sp.Rcode, addrAnswer(q.qname, q.qtype, sp.Addrs)...), result: resultLocal, reason: "special-use name"}
	}

	active := s.d.Config.Get().Blocking.Active(time.Now())
	allowReason, allowed := "", false
	if active {
		if allowReason, allowed = f.Allowed(q.name); !allowed {
			if reason, blocked := f.Blocked(q.name); blocked {
				return outcome{msg: blockedMsg(q.qname, q.qtype), result: resultBlocked, reason: reason}
			}
		}
	}

	out := s.forward(ctx, q)
	out.result = resultAllowed
	if out.reason == "" {
		out.reason = allowReason
	}
	if out.msg.Rcode == dns.RcodeServerFailure {
		return out
	}
	if !q.rewritten && f.RebindingApplies(q.name) && filter.StripRebinding(out.msg) {
		out.result, out.reason = resultBlocked, "rebinding"
		if len(out.msg.Answer) == 0 {
			out.msg.Rcode = dns.RcodeSuccess
			out.msg.Ns = nil
		}
	}
	if active && !allowed {
		if reason, blocked := f.CNAMEBlocked(out.msg); blocked {
			out.msg = blockedMsg(q.qname, q.qtype)
			out.result, out.reason = resultBlocked, reason
		}
	}
	return out
}

func (s *Server) forward(ctx context.Context, q query) outcome {
	question := dns.Question{Name: q.qname, Qtype: q.qtype, Qclass: q.qclass}
	if m := s.d.Cache.Get(question, q.do()); m != nil {
		return outcome{msg: m, cached: true}
	}
	resp, host, err := s.d.Upstream.Forward(ctx, q.upstreamMsg())
	if err != nil {
		log.Debug().Err(err).Str("domain", q.name).Msg("upstream failed")
		return outcome{msg: synth(dns.RcodeServerFailure), reason: "upstreams failed"}
	}
	s.d.Cache.Set(question, q.do(), resp)
	return outcome{msg: resp, upstream: host}
}

func (s *Server) local(ctx context.Context, q query, reason string) outcome {
	out := outcome{result: resultLocal, reason: reason}
	if !s.d.Upstream.HasRouter() || s.d.Upstream.IsRouter(q.client) {
		out.msg = synth(dns.RcodeNameError)
		return out
	}
	resp, err := s.d.Upstream.Router(ctx, q.upstreamMsg())
	if err != nil {
		log.Debug().Err(err).Str("domain", q.name).Msg("router failed")
		out.msg = synth(dns.RcodeServerFailure)
		return out
	}
	out.msg, out.upstream = resp, "router"
	return out
}

func (s *Server) rewrite(ctx context.Context, q query, rw *filter.Rewrite, depth int, seen map[string]bool) (outcome, bool) {
	out := outcome{result: resultRewrite, reason: rw.Pattern}
	if rw.Host == "" {
		switch q.qtype {
		case dns.TypeA, dns.TypeAAAA, dns.TypeANY:
			out.msg = synth(dns.RcodeSuccess, addrAnswer(q.qname, q.qtype, rw.IPs)...)
		case dns.TypeHTTPS, dns.TypeSVCB:
			out.msg = synth(dns.RcodeSuccess)
		default:
			return outcome{}, false
		}
		return out, true
	}

	seen[q.name] = true
	target := dns.Fqdn(rw.Host)
	cname := &dns.CNAME{Hdr: hdr(q.qname, dns.TypeCNAME), Target: target}
	if q.qtype == dns.TypeCNAME {
		out.msg = synth(dns.RcodeSuccess, cname)
		return out, true
	}
	if depth >= rewriteDepth || seen[filter.Normalize(rw.Host)] {
		out.msg = synth(dns.RcodeServerFailure)
		out.reason = rw.Pattern + " (rewrite loop)"
		return out, true
	}
	sub := q
	sub.qname, sub.name, sub.rewritten = target, filter.Normalize(rw.Host), true
	inner := s.resolve(ctx, sub, depth+1, seen)
	out.upstream, out.cached = inner.upstream, inner.cached
	if inner.msg.Rcode == dns.RcodeServerFailure {
		out.msg = synth(dns.RcodeServerFailure)
		if inner.result == resultRewrite {
			out.reason = inner.reason
		}
		return out, true
	}
	out.msg = synth(inner.msg.Rcode, append([]dns.RR{cname}, inner.msg.Answer...)...)
	out.msg.Ns = inner.msg.Ns
	if inner.result == resultBlocked {
		out.result, out.reason = resultBlocked, inner.reason
	}
	return out, true
}
