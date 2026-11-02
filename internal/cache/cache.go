package cache

import (
	"container/list"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

type key struct {
	name  string
	qtype uint16
	class uint16
	do    bool
	cd    bool
}

type entry struct {
	key     key
	packed  []byte
	stored  time.Time
	expires time.Time
}

type Cache struct {
	mu      sync.Mutex
	ll      *list.List
	items   map[key]*list.Element
	size    int
	minTTL  uint32
	maxTTL  uint32
	day     string
	hits    int64
	lookups int64
}

func New(size, minTTL, maxTTL int) *Cache {
	c := &Cache{ll: list.New(), items: map[key]*list.Element{}}
	c.SetLimits(size, minTTL, maxTTL)
	return c
}

func (c *Cache) SetLimits(size, minTTL, maxTTL int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.size, c.minTTL, c.maxTTL = size, uint32(minTTL), uint32(maxTTL)
	c.evict()
}

func keyOf(q dns.Question, do, cd bool) key {
	return key{name: strings.ToLower(q.Name), qtype: q.Qtype, class: q.Qclass, do: do, cd: cd}
}

func (c *Cache) Get(q dns.Question, do, cd bool) *dns.Msg {
	now := time.Now()
	c.mu.Lock()
	c.countLookup(now)
	el, ok := c.items[keyOf(q, do, cd)]
	if !ok {
		c.mu.Unlock()
		return nil
	}
	e := el.Value.(*entry)
	if !now.Before(e.expires) {
		c.ll.Remove(el)
		delete(c.items, e.key)
		c.mu.Unlock()
		return nil
	}
	c.ll.MoveToFront(el)
	c.hits++
	packed, stored := e.packed, e.stored
	c.mu.Unlock()

	m := new(dns.Msg)
	if err := m.Unpack(packed); err != nil {
		return nil
	}
	elapsed := uint32(now.Sub(stored) / time.Second)
	for _, sec := range [][]dns.RR{m.Answer, m.Ns, m.Extra} {
		for _, rr := range sec {
			h := rr.Header()
			h.Ttl -= min(h.Ttl, elapsed)
		}
	}
	return m
}

func (c *Cache) countLookup(now time.Time) {
	day := now.Format(time.DateOnly)
	if day != c.day {
		c.day, c.hits, c.lookups = day, 0, 0
	}
	c.lookups++
}

func (c *Cache) Set(q dns.Question, do, cd bool, m *dns.Msg) {
	if m.Truncated || (m.Rcode != dns.RcodeSuccess && m.Rcode != dns.RcodeNameError) {
		return
	}
	c.mu.Lock()
	size, minTTL, maxTTL := c.size, c.minTTL, c.maxTTL
	c.mu.Unlock()
	if size <= 0 {
		return
	}

	cp := m.Copy()
	cp.Extra = stripOPT(cp.Extra)
	ttl, ok := lifetime(cp, minTTL, maxTTL)
	if !ok || ttl == 0 {
		return
	}
	for _, sec := range [][]dns.RR{cp.Answer, cp.Ns, cp.Extra} {
		for _, rr := range sec {
			h := rr.Header()
			h.Ttl = min(max(h.Ttl, minTTL), maxTTL)
		}
	}
	if len(cp.Answer) == 0 {
		for _, rr := range cp.Ns {
			if soa, isSOA := rr.(*dns.SOA); isSOA {
				soa.Hdr.Ttl = ttl
			}
		}
	}
	packed, err := cp.Pack()
	if err != nil {
		return
	}

	now := time.Now()
	k := keyOf(q, do, cd)
	e := &entry{key: k, packed: packed, stored: now, expires: now.Add(time.Duration(ttl) * time.Second)}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, exists := c.items[k]; exists {
		el.Value = e
		c.ll.MoveToFront(el)
		return
	}
	c.items[k] = c.ll.PushFront(e)
	c.evict()
}

func lifetime(m *dns.Msg, minTTL, maxTTL uint32) (uint32, bool) {
	clamp := func(t uint32) uint32 { return min(max(t, minTTL), maxTTL) }
	if m.Rcode == dns.RcodeSuccess && len(m.Answer) > 0 {
		ttl := m.Answer[0].Header().Ttl
		for _, rr := range m.Answer[1:] {
			ttl = min(ttl, rr.Header().Ttl)
		}
		return clamp(ttl), true
	}
	for _, rr := range m.Ns {
		if soa, ok := rr.(*dns.SOA); ok {
			return clamp(min(soa.Hdr.Ttl, soa.Minttl)), true
		}
	}
	return 0, false
}

func stripOPT(rrs []dns.RR) []dns.RR {
	out := rrs[:0]
	for _, rr := range rrs {
		if rr.Header().Rrtype != dns.TypeOPT {
			out = append(out, rr)
		}
	}
	return out
}

func (c *Cache) evict() {
	for c.ll.Len() > max(c.size, 0) {
		el := c.ll.Back()
		c.ll.Remove(el)
		delete(c.items, el.Value.(*entry).key)
	}
}

func (c *Cache) Flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ll.Init()
	clear(c.items)
}

func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

func (c *Cache) HitRate() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.day != time.Now().Format(time.DateOnly) || c.lookups == 0 {
		return 0
	}
	return float64(c.hits) / float64(c.lookups)
}
