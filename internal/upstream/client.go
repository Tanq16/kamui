package upstream

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/miekg/dns"
)

type client interface {
	exchange(ctx context.Context, m *dns.Msg) (*dns.Msg, error)
	close()
}

func newClient(raw string, boot *Bootstrap) (client, string, error) {
	switch {
	case strings.HasPrefix(raw, "https://"):
		u, err := url.Parse(raw)
		if err != nil {
			return nil, "", err
		}
		return newDoH(raw, boot), u.Hostname(), nil
	case strings.HasPrefix(raw, "tls://"):
		hostport := withDefaultPort(strings.TrimPrefix(raw, "tls://"), "853")
		host, _, err := net.SplitHostPort(hostport)
		if err != nil {
			return nil, "", err
		}
		return newDoT(hostport, host, boot), host, nil
	default:
		addr := withDefaultPort(raw, "53")
		host, _, _ := net.SplitHostPort(addr)
		return newPlain(addr), host, nil
	}
}

type plain struct {
	addr string
	udp  *dns.Client
	tcp  *dns.Client
}

func newPlain(addr string) *plain {
	return &plain{
		addr: addr,
		udp:  &dns.Client{Net: "udp", UDPSize: dns.DefaultMsgSize},
		tcp:  &dns.Client{Net: "tcp"},
	}
}

func (p *plain) exchange(ctx context.Context, m *dns.Msg) (*dns.Msg, error) {
	resp, _, err := p.udp.ExchangeContext(ctx, m, p.addr)
	if err != nil || !resp.Truncated {
		return resp, err
	}
	resp, _, err = p.tcp.ExchangeContext(ctx, m, p.addr)
	return resp, err
}

func (p *plain) close() {}

type dot struct {
	addr   string
	client *dns.Client
	tlsCfg *tls.Config
	boot   *Bootstrap
	idle   chan *dns.Conn
}

func newDoT(addr, host string, boot *Bootstrap) *dot {
	return &dot{
		addr:   addr,
		client: &dns.Client{Net: "tcp-tls"},
		tlsCfg: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12},
		boot:   boot,
		idle:   make(chan *dns.Conn, 8),
	}
}

func (d *dot) dial(ctx context.Context) (*dns.Conn, error) {
	raw, err := d.boot.DialContext(ctx, "tcp", d.addr)
	if err != nil {
		return nil, err
	}
	tc := tls.Client(raw, d.tlsCfg)
	if err := tc.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	return &dns.Conn{Conn: tc}, nil
}

func (d *dot) exchange(ctx context.Context, m *dns.Msg) (*dns.Msg, error) {
	for {
		var conn *dns.Conn
		pooled := false
		select {
		case conn = <-d.idle:
			pooled = true
		default:
			c, err := d.dial(ctx)
			if err != nil {
				return nil, err
			}
			conn = c
		}
		resp, _, err := d.client.ExchangeWithConnContext(ctx, m, conn)
		if err != nil {
			conn.Close()
			if pooled && ctx.Err() == nil {
				continue
			}
			return nil, err
		}
		conn.SetDeadline(time.Time{})
		select {
		case d.idle <- conn:
		default:
			conn.Close()
		}
		return resp, nil
	}
}

func (d *dot) close() {
	for {
		select {
		case c := <-d.idle:
			c.Close()
		default:
			return
		}
	}
}

type doh struct {
	url    string
	client *http.Client
}

func newDoH(raw string, boot *Bootstrap) *doh {
	return &doh{
		url: raw,
		client: &http.Client{Transport: &http.Transport{
			DialContext:           boot.DialContext,
			ForceAttemptHTTP2:     true,
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
			MaxIdleConns:          16,
			MaxIdleConnsPerHost:   16,
			IdleConnTimeout:       90 * time.Second,
		}},
	}
}

func (h *doh) exchange(ctx context.Context, m *dns.Msg) (*dns.Msg, error) {
	id := m.Id
	q := m.Copy()
	q.Id = 0
	body, err := q.Pack()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DoH status %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, dns.MaxMsgSize))
	if err != nil {
		return nil, err
	}
	out := new(dns.Msg)
	if err := out.Unpack(data); err != nil {
		return nil, err
	}
	out.Id = id
	return out, nil
}

func (h *doh) close() {
	h.client.CloseIdleConnections()
}
