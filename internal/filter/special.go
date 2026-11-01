package filter

import (
	"net/netip"
	"strconv"
	"strings"

	"github.com/miekg/dns"
)

type Special struct {
	Zone  string
	Rcode int
	Addrs []netip.Addr
}

var (
	loopback4 = netip.MustParseAddr("127.0.0.1")
	loopback6 = netip.IPv6Loopback()
)

var nxZones = map[string]bool{
	"local":   true,
	"invalid": true,
	"test":    true,
	"onion":   true,
}

var privateRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("::1/128"),
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

var infraTypes = map[uint16]bool{
	dns.TypeDS:     true,
	dns.TypeDNSKEY: true,
	dns.TypeNS:     true,
	dns.TypeSOA:    true,
}

func (f *Filter) Special(name string, qtype uint16) (Special, bool) {
	if name == "localhost" || strings.HasSuffix(name, ".localhost") {
		s := Special{Zone: "localhost", Rcode: dns.RcodeSuccess}
		switch qtype {
		case dns.TypeA:
			s.Addrs = []netip.Addr{loopback4}
		case dns.TypeAAAA:
			s.Addrs = []netip.Addr{loopback6}
		case dns.TypeANY:
			s.Addrs = []netip.Addr{loopback4, loopback6}
		}
		return s, true
	}
	if zone, ok := suffixMatch(nxZones, name); ok {
		return Special{Zone: zone, Rcode: dns.RcodeNameError}, true
	}
	if name == "home.arpa" || strings.HasSuffix(name, ".home.arpa") {
		return Special{Zone: "home.arpa", Rcode: dns.RcodeNameError}, true
	}
	if name == "resolver.arpa" || strings.HasSuffix(name, ".resolver.arpa") {
		return Special{Zone: "resolver.arpa", Rcode: dns.RcodeSuccess}, true
	}
	if p, ok := reversePrefix(name); ok && p.Bits() >= 16 && cgnat.Contains(p.Addr()) {
		return Special{Zone: "100.64.0.0/10 reverse", Rcode: dns.RcodeNameError}, true
	}
	if name != "" && !strings.Contains(name, ".") && !infraTypes[qtype] {
		return Special{Zone: "single-label name", Rcode: dns.RcodeNameError}, true
	}
	return Special{}, false
}

func PrivatePTR(name string, qtype uint16) bool {
	if qtype != dns.TypePTR {
		return false
	}
	p, ok := reversePrefix(name)
	if !ok {
		return false
	}
	for _, r := range privateRanges {
		if r.Bits() <= p.Bits() && r.Contains(p.Addr()) {
			return true
		}
	}
	return false
}

func reversePrefix(name string) (netip.Prefix, bool) {
	if rest, ok := strings.CutSuffix(name, ".in-addr.arpa"); ok {
		labels := strings.Split(rest, ".")
		if len(labels) > 4 {
			return netip.Prefix{}, false
		}
		var b [4]byte
		for i, l := range labels {
			n, err := strconv.ParseUint(l, 10, 8)
			if err != nil {
				return netip.Prefix{}, false
			}
			b[len(labels)-1-i] = byte(n)
		}
		return netip.PrefixFrom(netip.AddrFrom4(b), 8*len(labels)), true
	}
	if rest, ok := strings.CutSuffix(name, ".ip6.arpa"); ok {
		labels := strings.Split(rest, ".")
		if len(labels) > 32 {
			return netip.Prefix{}, false
		}
		var b [16]byte
		for i, l := range labels {
			n, err := strconv.ParseUint(l, 16, 4)
			if err != nil || len(l) != 1 {
				return netip.Prefix{}, false
			}
			pos := len(labels) - 1 - i
			if pos%2 == 0 {
				b[pos/2] |= byte(n) << 4
			} else {
				b[pos/2] |= byte(n)
			}
		}
		return netip.PrefixFrom(netip.AddrFrom16(b), 4*len(labels)), true
	}
	return netip.Prefix{}, false
}
