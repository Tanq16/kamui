package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

const (
	ModeOn     = "on"
	ModeOff    = "off"
	ModePaused = "paused"
)

type Client struct {
	IP   string `json:"ip"`
	Name string `json:"name"`
}

type Settings struct {
	Upstreams       []string `json:"upstreams"`
	Bootstrap       []string `json:"bootstrap"`
	HedgeDelayMs    int      `json:"hedge_delay_ms"`
	TimeoutMs       int      `json:"timeout_ms"`
	Router          string   `json:"router"`
	LocalDomains    []string `json:"local_domains"`
	CacheSize       int      `json:"cache_size"`
	CacheMinTTL     int      `json:"cache_min_ttl"`
	CacheMaxTTL     int      `json:"cache_max_ttl"`
	Rebinding       bool     `json:"rebinding"`
	RebindingExempt []string `json:"rebinding_exempt"`
	RelayCanary     bool     `json:"relay_canary"`
	RefreshHours    int      `json:"refresh_hours"`
	LogSize         int      `json:"log_size"`
	RetentionDays   int      `json:"retention_days"`
	StatsWindowDays int      `json:"stats_window_days"`
	Clients         []Client `json:"clients"`
}

type Blocking struct {
	Mode  string `json:"mode"`
	Until int64  `json:"until"`
}

type List struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Enabled bool   `json:"enabled"`
}

type Rule struct {
	ID         string `json:"id"`
	Domain     string `json:"domain"`
	Subdomains bool   `json:"subdomains"`
}

type Rewrite struct {
	ID      string `json:"id"`
	Pattern string `json:"pattern"`
	Answer  string `json:"answer"`
}

type Config struct {
	Settings Settings  `json:"settings"`
	Blocking Blocking  `json:"blocking"`
	Lists    []List    `json:"lists"`
	Allow    []Rule    `json:"allow"`
	Block    []Rule    `json:"block"`
	Rewrites []Rewrite `json:"rewrites"`
}

func Default() *Config {
	return &Config{
		Settings: Settings{
			Upstreams:       []string{"https://dns.quad9.net/dns-query", "https://cloudflare-dns.com/dns-query"},
			Bootstrap:       []string{"9.9.9.9", "1.1.1.1"},
			HedgeDelayMs:    400,
			TimeoutMs:       3000,
			LocalDomains:    []string{},
			CacheSize:       10000,
			CacheMinTTL:     0,
			CacheMaxTTL:     86400,
			Rebinding:       true,
			RebindingExempt: []string{"plex.direct"},
			RelayCanary:     true,
			RefreshHours:    24,
			LogSize:         100000,
			RetentionDays:   30,
			StatsWindowDays: 7,
			Clients:         []Client{},
		},
		Blocking: Blocking{Mode: ModeOn},
		Lists:    []List{},
		Allow:    []Rule{},
		Block:    []Rule{},
		Rewrites: []Rewrite{},
	}
}

func (b Blocking) Effective(now time.Time) Blocking {
	if b.Mode == ModePaused && now.UnixMilli() >= b.Until {
		return Blocking{Mode: ModeOn}
	}
	return b
}

func (b Blocking) Active(now time.Time) bool {
	return b.Effective(now).Mode == ModeOn
}

func (c *Config) Clone() *Config {
	n := *c
	n.Settings.Upstreams = slices.Clone(c.Settings.Upstreams)
	n.Settings.Bootstrap = slices.Clone(c.Settings.Bootstrap)
	n.Settings.LocalDomains = slices.Clone(c.Settings.LocalDomains)
	n.Settings.RebindingExempt = slices.Clone(c.Settings.RebindingExempt)
	n.Settings.Clients = slices.Clone(c.Settings.Clients)
	n.Lists = slices.Clone(c.Lists)
	n.Allow = slices.Clone(c.Allow)
	n.Block = slices.Clone(c.Block)
	n.Rewrites = slices.Clone(c.Rewrites)
	return &n
}

func NewID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func Parse(data []byte) (*Config, error) {
	c := Default()
	if err := json.Unmarshal(data, c); err != nil {
		return nil, err
	}
	c.fillNil()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) fillNil() {
	s := &c.Settings
	for _, p := range []*[]string{&s.Upstreams, &s.Bootstrap, &s.LocalDomains, &s.RebindingExempt} {
		if *p == nil {
			*p = []string{}
		}
	}
	if s.Clients == nil {
		s.Clients = []Client{}
	}
	if c.Lists == nil {
		c.Lists = []List{}
	}
	if c.Allow == nil {
		c.Allow = []Rule{}
	}
	if c.Block == nil {
		c.Block = []Rule{}
	}
	if c.Rewrites == nil {
		c.Rewrites = []Rewrite{}
	}
	if c.Blocking.Mode == "" {
		c.Blocking.Mode = ModeOn
	}
}

var ErrSave = errors.New("failed to save config")

type Subscriber func(old, cur *Config)

type Manager struct {
	path  string
	mu    sync.Mutex
	cur   atomic.Pointer[Config]
	subs  []Subscriber
	timer *time.Timer
}

func Load(path string) (*Manager, error) {
	m := &Manager{path: path}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		c := Default()
		if err := m.save(c); err != nil {
			return nil, err
		}
		m.cur.Store(c)
	case err != nil:
		return nil, err
	default:
		c, err := Parse(data)
		if err != nil {
			return nil, err
		}
		if eff := c.Blocking.Effective(time.Now()); eff != c.Blocking {
			c.Blocking = eff
			if err := m.save(c); err != nil {
				return nil, err
			}
		}
		m.cur.Store(c)
	}
	m.schedulePause(m.cur.Load())
	return m, nil
}

func (m *Manager) Get() *Config {
	return m.cur.Load()
}

func (m *Manager) Subscribe(fn Subscriber) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subs = append(m.subs, fn)
}

func (m *Manager) Update(fn func(c *Config) error) (*Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.cur.Load()
	next := old.Clone()
	if err := fn(next); err != nil {
		return nil, err
	}
	if err := next.Validate(); err != nil {
		return nil, err
	}
	if err := m.save(next); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSave, err)
	}
	m.cur.Store(next)
	m.schedulePause(next)
	for _, s := range m.subs {
		s(old, next)
	}
	return next, nil
}

func (m *Manager) Marshal() ([]byte, error) {
	return json.Marshal(m.cur.Load(), jsontext.WithIndent("  "))
}

func (m *Manager) save(c *Config) error {
	data, err := json.Marshal(c, jsontext.WithIndent("  "))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(m.path), ".config-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), m.path)
}

func (m *Manager) schedulePause(c *Config) {
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
	if c.Blocking.Mode != ModePaused {
		return
	}
	until := c.Blocking.Until
	m.timer = time.AfterFunc(time.Until(time.UnixMilli(until)), func() {
		m.Update(func(next *Config) error {
			if next.Blocking.Mode == ModePaused && next.Blocking.Until == until {
				next.Blocking = Blocking{Mode: ModeOn}
			}
			return nil
		})
	})
}
