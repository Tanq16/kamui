package upstream

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/miekg/dns"
)

const minBootstrapTTL = 60 * time.Second

type bootEntry struct {
	addrs   []netip.Addr
	expires time.Time
}

type Bootstrap struct {
	servers []string
	client  *dns.Client
	mu      sync.Mutex
	cache   map[string]bootEntry
}

func NewBootstrap(servers []string) *Bootstrap {
	addrs := make([]string, 0, len(servers))
	for _, s := range servers {
		addrs = append(addrs, withDefaultPort(s, "53"))
	}
	return &Bootstrap{
		servers: addrs,
		client:  &dns.Client{Net: "udp", Timeout: 2 * time.Second},
		cache:   map[string]bootEntry{},
	}
}

func withDefaultPort(addr, port string) string {
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}
	return net.JoinHostPort(addr, port)
}

func (b *Bootstrap) Resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a}, nil
	}
	b.mu.Lock()
	e, ok := b.cache[host]
	b.mu.Unlock()
	if ok && time.Now().Before(e.expires) {
		return e.addrs, nil
	}
	addrs, ttl, err := b.lookup(ctx, host)
	if err != nil {
		if ok {
			return e.addrs, nil
		}
		return nil, err
	}
	b.mu.Lock()
	b.cache[host] = bootEntry{addrs: addrs, expires: time.Now().Add(max(ttl, minBootstrapTTL))}
	b.mu.Unlock()
	return addrs, nil
}

func (b *Bootstrap) lookup(ctx context.Context, host string) ([]netip.Addr, time.Duration, error) {
	var addrs []netip.Addr
	var ttl uint32
	var lastErr error
	for _, qtype := range []uint16{dns.TypeA, dns.TypeAAAA} {
		m := new(dns.Msg)
		m.SetQuestion(dns.Fqdn(host), qtype)
		for _, srv := range b.servers {
			resp, _, err := b.client.ExchangeContext(ctx, m, srv)
			if err != nil {
				lastErr = err
				continue
			}
			if resp.Rcode != dns.RcodeSuccess {
				lastErr = fmt.Errorf("bootstrap %s: %s for %s", srv, dns.RcodeToString[resp.Rcode], host)
				continue
			}
			for _, rr := range resp.Answer {
				var ip net.IP
				switch v := rr.(type) {
				case *dns.A:
					ip = v.A
				case *dns.AAAA:
					ip = v.AAAA
				default:
					continue
				}
				if a, ok := netip.AddrFromSlice(ip); ok {
					addrs = append(addrs, a.Unmap())
					if ttl == 0 || rr.Header().Ttl < ttl {
						ttl = rr.Header().Ttl
					}
				}
			}
			break
		}
	}
	if len(addrs) == 0 {
		if lastErr == nil {
			lastErr = fmt.Errorf("bootstrap: no addresses for %s", host)
		}
		return nil, 0, lastErr
	}
	return addrs, time.Duration(ttl) * time.Second, nil
}

func (b *Bootstrap) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addrs, err := b.Resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	var errs []error
	for _, a := range addrs {
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(a.String(), port))
		if err == nil {
			return conn, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}
