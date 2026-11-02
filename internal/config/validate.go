package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

func NormalizeDomain(s string) (string, error) {
	d := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
	if d == "" || len(d) > 253 {
		return "", fmt.Errorf("invalid domain %q", s)
	}
	if _, err := netip.ParseAddr(d); err == nil {
		return "", fmt.Errorf("invalid domain %q: an IP address is not a domain", s)
	}
	for label := range strings.SplitSeq(d, ".") {
		if !validLabel(label) {
			return "", fmt.Errorf("invalid domain %q", s)
		}
	}
	return d, nil
}

func validLabel(l string) bool {
	if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
		return false
	}
	for _, c := range l {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func NormalizePattern(s string) (string, error) {
	p := strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(p, "*."); ok {
		d, err := NormalizeDomain(rest)
		if err != nil {
			return "", fmt.Errorf("invalid pattern %q", s)
		}
		return "*." + d, nil
	}
	return NormalizeDomain(p)
}

func ParseAddrPort(s string, defaultPort uint16) (netip.AddrPort, error) {
	s = strings.TrimSpace(s)
	if ap, err := netip.ParseAddrPort(s); err == nil && ap.Port() != 0 && ap.Addr().Zone() == "" {
		return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()), nil
	}
	if a, err := netip.ParseAddr(s); err == nil && a.Zone() == "" {
		return netip.AddrPortFrom(a.Unmap(), defaultPort), nil
	}
	return netip.AddrPort{}, fmt.Errorf("invalid address %q, expected ip or ip:port", s)
}

func normalizeDNSAddr(s string) (string, error) {
	ap, err := ParseAddrPort(s, 53)
	if err != nil {
		return "", err
	}
	if ap.Port() == 53 {
		return ap.Addr().String(), nil
	}
	return ap.String(), nil
}

func ValidateUpstream(s string) error {
	switch {
	case strings.HasPrefix(s, "https://"):
		u, err := url.Parse(s)
		if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("invalid DoH upstream %q, expected https://host/path", s)
		}
		return validatePort(u.Port(), s)
	case strings.HasPrefix(s, "tls://"):
		hostport := strings.TrimPrefix(s, "tls://")
		host, port := hostport, ""
		if h, p, err := net.SplitHostPort(hostport); err == nil {
			host, port = h, p
		}
		if host == "" || strings.ContainsAny(host, "/?#@") {
			return fmt.Errorf("invalid DoT upstream %q, expected tls://host[:port]", s)
		}
		if _, err := netip.ParseAddr(host); err != nil {
			if _, err := NormalizeDomain(host); err != nil {
				return fmt.Errorf("invalid DoT upstream %q, expected tls://host[:port]", s)
			}
		}
		return validatePort(port, s)
	default:
		if _, err := ParseAddrPort(s, 53); err != nil {
			return fmt.Errorf("invalid upstream %q, expected https://host/path, tls://host[:port], or ip[:port]", s)
		}
		return nil
	}
}

func validatePort(p, s string) error {
	if p == "" {
		return nil
	}
	if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("invalid port in %q", s)
	}
	return nil
}

func ValidateListURL(s string) error {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return fmt.Errorf("invalid list URL %q, expected http(s)://...", s)
	}
	return nil
}

func inRange(name string, v, lo, hi int) error {
	if v < lo || v > hi {
		return fmt.Errorf("%s must be between %d and %d", name, lo, hi)
	}
	return nil
}

func (s *Settings) Validate() error {
	if len(s.Upstreams) == 0 {
		return errors.New("at least one upstream is required")
	}
	for i, u := range s.Upstreams {
		u = strings.TrimSpace(u)
		if err := ValidateUpstream(u); err != nil {
			return err
		}
		if !strings.Contains(u, "://") {
			u, _ = normalizeDNSAddr(u)
		}
		s.Upstreams[i] = u
	}
	if len(s.Bootstrap) == 0 {
		return errors.New("at least one bootstrap server is required")
	}
	for i, b := range s.Bootstrap {
		n, err := normalizeDNSAddr(b)
		if err != nil {
			return fmt.Errorf("bootstrap: %w", err)
		}
		s.Bootstrap[i] = n
	}
	if err := inRange("hedge_delay_ms", s.HedgeDelayMs, 50, 30000); err != nil {
		return err
	}
	if err := inRange("timeout_ms", s.TimeoutMs, 100, 60000); err != nil {
		return err
	}
	s.Router = strings.TrimSpace(s.Router)
	if s.Router != "" {
		n, err := normalizeDNSAddr(s.Router)
		if err != nil {
			return fmt.Errorf("router: %w", err)
		}
		s.Router = n
	}
	if s.Router == "" && len(s.LocalDomains) > 0 {
		return errors.New("local domains need a router address")
	}
	for i, d := range s.LocalDomains {
		n, err := NormalizeDomain(d)
		if err != nil {
			return fmt.Errorf("local domains: %w", err)
		}
		s.LocalDomains[i] = n
	}
	if err := inRange("cache_size", s.CacheSize, 0, 1000000); err != nil {
		return err
	}
	if err := inRange("cache_min_ttl", s.CacheMinTTL, 0, 604800); err != nil {
		return err
	}
	if err := inRange("cache_max_ttl", s.CacheMaxTTL, 0, 604800); err != nil {
		return err
	}
	if s.CacheMinTTL > s.CacheMaxTTL {
		return errors.New("cache_min_ttl must not exceed cache_max_ttl")
	}
	for i, d := range s.RebindingExempt {
		n, err := NormalizeDomain(d)
		if err != nil {
			return fmt.Errorf("rebinding exemptions: %w", err)
		}
		s.RebindingExempt[i] = n
	}
	if err := inRange("refresh_hours", s.RefreshHours, 0, 8760); err != nil {
		return err
	}
	if err := inRange("log_size", s.LogSize, 1000, 1000000); err != nil {
		return err
	}
	if err := inRange("retention_days", s.RetentionDays, 1, 3650); err != nil {
		return err
	}
	if s.StatsWindowDays != 7 && s.StatsWindowDays != 30 {
		return errors.New("stats_window_days must be 7 or 30")
	}
	if s.StatsWindowDays > s.RetentionDays {
		return errors.New("stats_window_days must not exceed retention_days")
	}
	seen := map[string]bool{}
	for i, c := range s.Clients {
		a, err := netip.ParseAddr(strings.TrimSpace(c.IP))
		if err != nil {
			return fmt.Errorf("invalid client IP %q", c.IP)
		}
		ip := a.Unmap().String()
		if seen[ip] {
			return fmt.Errorf("duplicate client IP %s", ip)
		}
		seen[ip] = true
		name := strings.TrimSpace(c.Name)
		if name == "" || len(name) > 64 {
			return fmt.Errorf("client %s needs a name of 1 to 64 characters", ip)
		}
		s.Clients[i] = Client{IP: ip, Name: name}
	}
	return nil
}

func ValidateRewrites(rewrites []Rewrite) error {
	kinds := map[string]string{}
	for _, r := range rewrites {
		kind := "ip"
		if _, err := netip.ParseAddr(r.Answer); err != nil {
			kind = "host"
			if r.Answer == r.Pattern {
				return fmt.Errorf("rewrite %s points at itself", r.Pattern)
			}
		}
		prev, ok := kinds[r.Pattern]
		switch {
		case !ok:
			kinds[r.Pattern] = kind
		case prev != kind:
			return fmt.Errorf("pattern %s holds either one hostname or IP addresses, not both", r.Pattern)
		case kind == "host":
			return fmt.Errorf("pattern %s already has a hostname answer", r.Pattern)
		}
	}
	return nil
}

func NormalizeRewrite(r Rewrite) (Rewrite, error) {
	p, err := NormalizePattern(r.Pattern)
	if err != nil {
		return r, err
	}
	r.Pattern = p
	ans := strings.TrimSpace(r.Answer)
	if a, err := netip.ParseAddr(ans); err == nil {
		if a.Zone() != "" {
			return r, fmt.Errorf("invalid answer %q", r.Answer)
		}
		r.Answer = a.Unmap().String()
		return r, nil
	}
	h, err := NormalizeDomain(ans)
	if err != nil {
		return r, fmt.Errorf("answer must be an IPv4 address, an IPv6 address, or a hostname: %w", err)
	}
	r.Answer = h
	return r, nil
}

func (c *Config) Validate() error {
	if err := c.Settings.Validate(); err != nil {
		return err
	}
	switch c.Blocking.Mode {
	case ModeOn, ModeOff:
		c.Blocking.Until = 0
	case ModePaused:
		if c.Blocking.Until <= 0 {
			return errors.New("paused blocking needs an end time")
		}
	default:
		return fmt.Errorf("invalid blocking mode %q", c.Blocking.Mode)
	}
	ids := map[string]bool{}
	for i, l := range c.Lists {
		if l.ID == "" || ids[l.ID] || strings.ContainsAny(l.ID, `/\.`) {
			return fmt.Errorf("invalid or duplicate list id %q", l.ID)
		}
		ids[l.ID] = true
		c.Lists[i].Name = strings.TrimSpace(l.Name)
		if c.Lists[i].Name == "" {
			return errors.New("list name is required")
		}
		c.Lists[i].URL = strings.TrimSpace(l.URL)
		if err := ValidateListURL(c.Lists[i].URL); err != nil {
			return err
		}
	}
	for _, rules := range [][]Rule{c.Allow, c.Block} {
		for i, r := range rules {
			if r.ID == "" {
				return errors.New("rule id is required")
			}
			d, err := NormalizeDomain(r.Domain)
			if err != nil {
				return err
			}
			rules[i].Domain = d
		}
	}
	for i, r := range c.Rewrites {
		if r.ID == "" {
			return errors.New("rewrite id is required")
		}
		n, err := NormalizeRewrite(r)
		if err != nil {
			return err
		}
		c.Rewrites[i] = n
	}
	return ValidateRewrites(c.Rewrites)
}
