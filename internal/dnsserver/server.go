package dnsserver

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"time"

	"github.com/miekg/dns"
	"github.com/rs/zerolog/log"

	"github.com/Tanq16/kamui/internal/cache"
	"github.com/Tanq16/kamui/internal/config"
	"github.com/Tanq16/kamui/internal/filter"
	"github.com/Tanq16/kamui/internal/store"
	"github.com/Tanq16/kamui/internal/upstream"
)

type Deps struct {
	Config   *config.Manager
	Filter   *filter.Filter
	Cache    *cache.Cache
	Upstream *upstream.Resolver
	Store    *store.Store
}

type Server struct {
	addr string
	d    Deps
	udp  *dns.Server
	tcp  *dns.Server
}

func New(addr string, d Deps) *Server {
	return &Server{addr: addr, d: d}
}

func (s *Server) Start() error {
	pc, err := net.ListenPacket("udp", s.addr)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		pc.Close()
		return err
	}
	s.udp = &dns.Server{PacketConn: pc, Handler: s, UDPSize: dns.DefaultMsgSize}
	s.tcp = &dns.Server{Listener: ln, Handler: s}
	for _, srv := range []*dns.Server{s.udp, s.tcp} {
		go func() {
			if err := srv.ActivateAndServe(); err != nil {
				log.Error().Err(err).Msg("DNS listener stopped")
			}
		}()
	}
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	return errors.Join(s.udp.ShutdownContext(ctx), s.tcp.ShutdownContext(ctx))
}

func clientAddr(a net.Addr) netip.Addr {
	switch v := a.(type) {
	case *net.UDPAddr:
		return v.AddrPort().Addr().Unmap()
	case *net.TCPAddr:
		return v.AddrPort().Addr().Unmap()
	}
	if ap, err := netip.ParseAddrPort(a.String()); err == nil {
		return ap.Addr().Unmap()
	}
	return netip.Addr{}
}

func (s *Server) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	start := time.Now()
	if req.Opcode != dns.OpcodeQuery || len(req.Question) != 1 {
		m := new(dns.Msg)
		m.SetRcode(req, dns.RcodeNotImplemented)
		if len(req.Question) != 1 {
			m.Rcode = dns.RcodeFormatError
		}
		w.WriteMsg(m)
		return
	}

	q := req.Question[0]
	client := clientAddr(w.RemoteAddr())
	qr := query{
		qname:  q.Name,
		name:   filter.Normalize(q.Name),
		qtype:  q.Qtype,
		qclass: q.Qclass,
		cd:     req.CheckingDisabled,
		opt:    upstreamOPT(req.IsEdns0()),
		client: client,
	}
	out := s.resolve(context.Background(), qr, 0, map[string]bool{})

	_, udp := w.RemoteAddr().(*net.UDPAddr)
	resp := reply(req, out.msg, udp)
	if err := w.WriteMsg(resp); err != nil {
		log.Debug().Err(err).Str("client", client.String()).Msg("failed to write DNS response")
	}

	s.d.Store.Log(store.Entry{
		Time:       start.UnixMilli(),
		Client:     client.String(),
		Domain:     qr.name,
		Type:       dns.Type(q.Qtype).String(),
		Result:     out.result,
		Reason:     out.reason,
		Upstream:   out.upstream,
		Cached:     out.cached,
		Rcode:      dns.RcodeToString[resp.Rcode],
		DurationMs: float64(time.Since(start).Microseconds()) / 1000,
	})
}

func upstreamOPT(opt *dns.OPT) *dns.OPT {
	if opt == nil {
		return nil
	}
	o := &dns.OPT{Hdr: opt.Hdr}
	for _, e := range opt.Option {
		if e.Option() != dns.EDNS0SUBNET {
			o.Option = append(o.Option, e)
		}
	}
	return o
}

func reply(req, out *dns.Msg, udp bool) *dns.Msg {
	r := new(dns.Msg)
	r.SetReply(req)
	r.RecursionAvailable = true
	r.Rcode = out.Rcode
	r.AuthenticatedData = out.AuthenticatedData
	r.Answer = out.Answer
	r.Ns = out.Ns
	for _, rr := range out.Extra {
		if rr.Header().Rrtype != dns.TypeOPT {
			r.Extra = append(r.Extra, rr)
		}
	}
	size := dns.MinMsgSize
	if opt := req.IsEdns0(); opt != nil {
		size = max(int(opt.UDPSize()), dns.MinMsgSize)
		o := &dns.OPT{Hdr: dns.RR_Header{Name: ".", Rrtype: dns.TypeOPT}}
		o.SetUDPSize(uint16(size))
		o.SetDo(opt.Do())
		r.Extra = append(r.Extra, o)
	}
	if udp {
		r.Truncate(size)
	} else {
		r.Compress = true
	}
	return r
}
