package upstream

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
)

var ErrNoRouter = errors.New("no router configured")

type Config struct {
	Upstreams []string
	Bootstrap []string
	Hedge     time.Duration
	Timeout   time.Duration
	Router    string
}

type tracked struct {
	url     string
	host    string
	client  client
	latency atomic.Int64
}

func (t *tracked) observe(d time.Duration) {
	us := d.Microseconds()
	for {
		old := t.latency.Load()
		next := us
		if old != 0 {
			next = (old*7 + us) / 8
		}
		if t.latency.CompareAndSwap(old, next) {
			return
		}
	}
}

type state struct {
	cfg        Config
	boot       *Bootstrap
	upstreams  []*tracked
	router     *plain
	routerAddr netip.Addr
}

type Resolver struct {
	st atomic.Pointer[state]
}

type Latency struct {
	URL       string
	LatencyMs float64
}

func New(cfg Config) (*Resolver, error) {
	r := &Resolver{}
	if err := r.Apply(cfg); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Resolver) Apply(cfg Config) error {
	old := r.st.Load()
	if old != nil && slices.Equal(old.cfg.Upstreams, cfg.Upstreams) && slices.Equal(old.cfg.Bootstrap, cfg.Bootstrap) {
		next := *old
		next.cfg = cfg
		if err := next.setRouter(cfg.Router); err != nil {
			return err
		}
		r.st.Store(&next)
		return nil
	}

	next := &state{cfg: cfg, boot: NewBootstrap(cfg.Bootstrap)}
	for _, raw := range cfg.Upstreams {
		c, host, err := newClient(raw, next.boot)
		if err != nil {
			return fmt.Errorf("upstream %s: %w", raw, err)
		}
		next.upstreams = append(next.upstreams, &tracked{url: raw, host: host, client: c})
	}
	if err := next.setRouter(cfg.Router); err != nil {
		return err
	}
	r.st.Store(next)
	if old != nil {
		for _, u := range old.upstreams {
			u.client.close()
		}
	}
	return nil
}

func (s *state) setRouter(router string) error {
	s.router, s.routerAddr = nil, netip.Addr{}
	if router == "" {
		return nil
	}
	addr := withDefaultPort(router, "53")
	ap, err := netip.ParseAddrPort(addr)
	if err != nil {
		return fmt.Errorf("router %s: %w", router, err)
	}
	s.router = newPlain(addr)
	s.routerAddr = ap.Addr().Unmap()
	return nil
}

func (r *Resolver) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return r.st.Load().boot.DialContext(ctx, network, address)
}

func (r *Resolver) Latencies() []Latency {
	st := r.st.Load()
	out := make([]Latency, 0, len(st.upstreams))
	for _, u := range st.upstreams {
		out = append(out, Latency{URL: u.url, LatencyMs: float64(u.latency.Load()) / 1000})
	}
	return out
}

func (r *Resolver) HasRouter() bool {
	return r.st.Load().router != nil
}

func (r *Resolver) IsRouter(client netip.Addr) bool {
	st := r.st.Load()
	return st.router != nil && st.routerAddr == client.Unmap()
}

func (r *Resolver) Router(ctx context.Context, m *dns.Msg) (*dns.Msg, error) {
	st := r.st.Load()
	if st.router == nil {
		return nil, ErrNoRouter
	}
	ctx, cancel := context.WithTimeout(ctx, st.cfg.Timeout)
	defer cancel()
	return st.router.exchange(ctx, m)
}

type result struct {
	resp *dns.Msg
	up   *tracked
	err  error
}

func failed(res result) bool {
	return res.err != nil || res.resp.Rcode == dns.RcodeServerFailure || res.resp.Rcode == dns.RcodeRefused
}

func (r *Resolver) Forward(ctx context.Context, m *dns.Msg) (*dns.Msg, string, error) {
	st := r.st.Load()
	ctx, cancel := context.WithTimeout(ctx, st.cfg.Timeout)
	defer cancel()

	ups := st.upstreams
	results := make(chan result, len(ups))
	next, inflight := 0, 0
	launch := func() {
		if next >= len(ups) {
			return
		}
		u := ups[next]
		next++
		inflight++
		q := m.Copy()
		go func() {
			start := time.Now()
			resp, err := u.client.exchange(ctx, q)
			if err == nil {
				u.observe(time.Since(start))
			}
			results <- result{resp: resp, up: u, err: err}
		}()
	}

	launch()
	hedge := time.NewTimer(st.cfg.Hedge)
	defer hedge.Stop()
	var lastErr error
	for inflight > 0 {
		select {
		case res := <-results:
			inflight--
			if !failed(res) {
				return res.resp, res.up.host, nil
			}
			lastErr = res.err
			if lastErr == nil {
				lastErr = fmt.Errorf("%s answered %s", res.up.host, dns.RcodeToString[res.resp.Rcode])
			}
			launch()
		case <-hedge.C:
			launch()
			hedge.Reset(st.cfg.Hedge)
		case <-ctx.Done():
			return nil, "", fmt.Errorf("upstreams timed out: %w", ctx.Err())
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no upstreams configured")
	}
	return nil, "", lastErr
}
