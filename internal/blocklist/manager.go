package blocklist

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxListBytes = 256 << 20
	retryAfter   = 15 * time.Minute
)

type Source struct {
	ID      string
	Name    string
	URL     string
	Enabled bool
}

type Status struct {
	Domains int
	Skipped int
	Updated time.Time
	Error   string
}

type Result struct {
	ID      string
	Name    string
	Domains int
	Skipped int
	Err     error
}

type Match struct {
	ID      string
	Name    string
	Enabled bool
}

type list struct {
	src       atomic.Pointer[Source]
	index     atomic.Pointer[Index]
	refresh   sync.Mutex
	mu        sync.Mutex
	status    Status
	attempted time.Time
}

func (l *list) setStatus(fn func(s *Status)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fn(&l.status)
}

type Manager struct {
	dir    string
	client *http.Client
	mu     sync.Mutex
	byID   map[string]*list
	order  atomic.Pointer[[]*list]
}

func NewManager(dir string, client *http.Client) *Manager {
	m := &Manager{dir: dir, client: client, byID: map[string]*list{}}
	m.order.Store(&[]*list{})
	return m
}

func (m *Manager) path(id string) string {
	return filepath.Join(m.dir, id+".txt")
}

func (m *Manager) Sync(sources []Source) []Result {
	m.mu.Lock()
	defer m.mu.Unlock()
	var loaded []Result
	keep := map[string]bool{}
	order := make([]*list, 0, len(sources))
	for _, src := range sources {
		keep[src.ID] = true
		l, ok := m.byID[src.ID]
		if ok && l.src.Load().URL != src.URL {
			l.mu.Lock()
			l.src.Store(&src)
			l.index.Store(nil)
			l.status, l.attempted = Status{}, time.Time{}
			l.mu.Unlock()
			os.Remove(m.path(src.ID))
		}
		if !ok {
			l = &list{}
			l.src.Store(&src)
			m.byID[src.ID] = l
			if r, found := m.loadFile(l); found {
				loaded = append(loaded, r)
			}
		}
		l.src.Store(&src)
		order = append(order, l)
	}
	for id := range m.byID {
		if !keep[id] {
			delete(m.byID, id)
			os.Remove(m.path(id))
		}
	}
	m.order.Store(&order)
	return loaded
}

func (m *Manager) loadFile(l *list) (Result, bool) {
	src := l.src.Load()
	f, err := os.Open(m.path(src.ID))
	if err != nil {
		return Result{}, false
	}
	defer f.Close()
	r := Result{ID: src.ID, Name: src.Name}
	info, err := f.Stat()
	if err != nil {
		r.Err = err
		return r, true
	}
	ix, skipped, err := Parse(f)
	if err != nil {
		r.Err = err
		l.setStatus(func(s *Status) { s.Error = err.Error() })
		return r, true
	}
	if ix.Len() == 0 {
		return Result{}, false
	}
	l.index.Store(ix)
	l.setStatus(func(s *Status) { *s = Status{Domains: ix.Len(), Skipped: skipped, Updated: info.ModTime()} })
	r.Domains, r.Skipped = ix.Len(), skipped
	return r, true
}

func (m *Manager) get(id string) *list {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byID[id]
}

func (m *Manager) Refresh(ctx context.Context, id string) (Result, bool) {
	l := m.get(id)
	if l == nil {
		return Result{}, false
	}
	return m.refresh(ctx, l), true
}

func (m *Manager) refresh(ctx context.Context, l *list) Result {
	l.mu.Lock()
	l.attempted = time.Now()
	l.mu.Unlock()
	l.refresh.Lock()
	defer l.refresh.Unlock()
	src := l.src.Load()
	r := Result{ID: src.ID, Name: src.Name}

	data, err := m.download(ctx, src.URL)
	var ix *Index
	if err == nil {
		ix, r.Skipped, err = Parse(bytes.NewReader(data))
	}
	if err == nil && ix.Len() == 0 {
		err = errors.New("no domains found in list")
	}
	l.mu.Lock()
	if l.src.Load().URL != src.URL {
		l.mu.Unlock()
		r.Err = errors.New("list URL changed during download, result discarded")
		return r
	}
	if err != nil {
		l.status.Error = err.Error()
		l.mu.Unlock()
		r.Err = err
		return r
	}
	l.index.Store(ix)
	l.status = Status{Domains: ix.Len(), Skipped: r.Skipped, Updated: time.Now()}
	l.mu.Unlock()
	r.Domains = ix.Len()
	if err := writeAtomic(m.path(src.ID), data); err != nil {
		r.Err = fmt.Errorf("list indexed but not saved: %w", err)
	}
	if m.get(src.ID) != l || l.src.Load().URL != src.URL {
		os.Remove(m.path(src.ID))
	}
	return r
}

func (m *Manager) download(ctx context.Context, url string) ([]byte, error) {
	var lastErr error
	for attempt := range 3 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		data, err := m.fetch(ctx, url)
		if err == nil {
			return data, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (m *Manager) fetch(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxListBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxListBytes {
		return nil, fmt.Errorf("list larger than %d MiB", maxListBytes>>20)
	}
	return data, nil
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".list-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
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
	return os.Rename(tmp.Name(), path)
}

func (m *Manager) RefreshAll(ctx context.Context) []Result {
	return m.refreshMany(ctx, *m.order.Load())
}

func (m *Manager) RefreshStale(ctx context.Context, maxAge time.Duration) []Result {
	var stale []*list
	for _, l := range *m.order.Load() {
		l.mu.Lock()
		updated, attempted := l.status.Updated, l.attempted
		due := updated.IsZero() || (maxAge > 0 && time.Since(updated) >= maxAge)
		if due && (attempted.IsZero() || time.Since(attempted) >= retryAfter) {
			l.attempted = time.Now()
			stale = append(stale, l)
		}
		l.mu.Unlock()
	}
	return m.refreshMany(ctx, stale)
}

func (m *Manager) refreshMany(ctx context.Context, lists []*list) []Result {
	results := make([]Result, len(lists))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, l := range lists {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			results[i] = m.refresh(ctx, l)
		})
	}
	wg.Wait()
	return results
}

func (l *list) Status() Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.status
}

func (m *Manager) Status(id string) Status {
	if l := m.get(id); l != nil {
		return l.Status()
	}
	return Status{}
}

func (m *Manager) First(name string) (string, bool) {
	for _, l := range *m.order.Load() {
		src := l.src.Load()
		if src.Enabled && l.index.Load().Match(name) {
			return src.Name, true
		}
	}
	return "", false
}

func (m *Manager) All(name string) []Match {
	var out []Match
	for _, l := range *m.order.Load() {
		src := l.src.Load()
		if l.index.Load().Match(name) {
			out = append(out, Match{ID: src.ID, Name: src.Name, Enabled: src.Enabled})
		}
	}
	return out
}

func (m *Manager) Counts() (enabled, total, domains int) {
	for _, l := range *m.order.Load() {
		total++
		if l.src.Load().Enabled {
			enabled++
			domains += l.index.Load().Len()
		}
	}
	return enabled, total, domains
}
