package filter

import (
	"net"
	"net/netip"
	"slices"

	"github.com/miekg/dns"
)

func internalIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	a = a.Unmap()
	return a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsUnspecified()
}

func pointsInternal(rr dns.RR) bool {
	switch v := rr.(type) {
	case *dns.A:
		return internalIP(v.A)
	case *dns.AAAA:
		return internalIP(v.AAAA)
	case *dns.HTTPS:
		return hintsInternal(v.Value)
	case *dns.SVCB:
		return hintsInternal(v.Value)
	}
	return false
}

func hintsInternal(kv []dns.SVCBKeyValue) bool {
	for _, p := range kv {
		var hints []net.IP
		switch h := p.(type) {
		case *dns.SVCBIPv4Hint:
			hints = h.Hint
		case *dns.SVCBIPv6Hint:
			hints = h.Hint
		}
		if slices.ContainsFunc(hints, internalIP) {
			return true
		}
	}
	return false
}

func dropInternal(rrs []dns.RR) ([]dns.RR, bool) {
	out := rrs[:0]
	dropped := false
	for _, rr := range rrs {
		if pointsInternal(rr) {
			dropped = true
			continue
		}
		out = append(out, rr)
	}
	return out, dropped
}

func StripRebinding(m *dns.Msg) bool {
	var droppedAnswer, droppedExtra bool
	m.Answer, droppedAnswer = dropInternal(m.Answer)
	m.Extra, droppedExtra = dropInternal(m.Extra)
	return droppedAnswer || droppedExtra
}

func (f *Filter) CNAMEBlocked(m *dns.Msg) (string, bool) {
	for _, rr := range m.Answer {
		c, ok := rr.(*dns.CNAME)
		if !ok {
			continue
		}
		target := Normalize(c.Target)
		if _, allowed := f.Allowed(target); allowed {
			continue
		}
		if reason, blocked := f.Blocked(target); blocked {
			return "CNAME " + target + ", " + reason, true
		}
	}
	return "", false
}
