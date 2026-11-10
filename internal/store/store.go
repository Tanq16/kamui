package store

import (
	"bufio"
	"compress/gzip"
	"encoding/gob"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

const (
	pruneInterval    = time.Hour
	domainStatsDays  = 30
	maxDomainsPerDay = 10_000
)

type Entry struct {
	ID         int64   `json:"id"`
	Time       int64   `json:"time"`
	Client     string  `json:"client"`
	ClientName string  `json:"client_name"`
	Domain     string  `json:"domain"`
	Type       string  `json:"type"`
	Result     string  `json:"result"`
	Reason     string  `json:"reason"`
	Upstream   string  `json:"upstream"`
	Cached     bool    `json:"cached"`
	Rcode      string  `json:"rcode"`
	DurationMs float64 `json:"duration_ms"`
}

type counts struct {
	Total   int64
	Blocked int64
}

type snapshot struct {
	NextID  int64
	Log     []Entry
	Days    map[string]counts
	Domains map[string]map[string]counts
}

type Store struct {
	path   string
	quit   chan struct{}
	done   chan struct{}
	retime chan time.Duration
	onErr  func(msg string, err error)

	mu        sync.RWMutex
	buf       []Entry
	head      int
	size      int
	retention int
	saveEvery time.Duration
	nextID    int64
	days      map[string]counts
	domains   map[string]map[string]counts
	dirty     bool
}

func Open(path string, logSize, retentionDays int, saveEvery time.Duration, onErr func(msg string, err error)) *Store {
	snap, err := readSnapshot(path)
	if err != nil {
		bad := path + ".bad"
		msg := "unreadable query log snapshot moved to " + filepath.Base(bad) + ", starting empty"
		if rerr := os.Rename(path, bad); rerr != nil {
			msg, err = "unreadable query log snapshot, starting empty", errors.Join(err, rerr)
		}
		onErr(msg, err)
		snap = emptySnapshot()
	}
	s := &Store{
		path:      path,
		quit:      make(chan struct{}),
		done:      make(chan struct{}),
		retime:    make(chan time.Duration, 1),
		onErr:     onErr,
		buf:       slices.Clone(snap.Log[max(len(snap.Log)-logSize, 0):]),
		size:      logSize,
		retention: retentionDays,
		saveEvery: saveEvery,
		nextID:    max(snap.NextID, 1),
		days:      snap.Days,
		domains:   snap.Domains,
	}
	s.prune()
	go s.run(saveEvery)
	return s
}

func (s *Store) SetLimits(logSize, retentionDays int, saveEvery time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retention = retentionDays
	if saveEvery != s.saveEvery {
		s.saveEvery = saveEvery
		select {
		case <-s.retime:
		default:
		}
		s.retime <- saveEvery
	}
	if logSize == s.size {
		return
	}
	entries := s.ordered()
	s.buf = slices.Clone(entries[max(len(entries)-logSize, 0):])
	s.head = 0
	s.size = logSize
	s.dirty = true
}

func (s *Store) Log(e Entry) {
	date := time.UnixMilli(e.Time).Format(time.DateOnly)
	blocked := int64(0)
	if e.Result == "blocked" {
		blocked = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e.ID = s.nextID
	s.nextID++
	if len(s.buf) < s.size {
		s.buf = append(s.buf, e)
	} else {
		s.buf[s.head] = e
		s.head = (s.head + 1) % len(s.buf)
	}
	d := s.days[date]
	d.Total++
	d.Blocked += blocked
	s.days[date] = d
	doms := s.domains[date]
	if doms == nil {
		doms = map[string]counts{}
		s.domains[date] = doms
	}
	if c, ok := doms[e.Domain]; ok || len(doms) < maxDomainsPerDay {
		c.Total++
		c.Blocked += blocked
		doms[e.Domain] = c
	}
	s.dirty = true
}

func (s *Store) Close() error {
	close(s.quit)
	<-s.done
	return s.save()
}

func (s *Store) run(saveEvery time.Duration) {
	defer close(s.done)
	save := time.NewTicker(time.Minute)
	defer save.Stop()
	setSave := func(d time.Duration) {
		if d > 0 {
			save.Reset(d)
		} else {
			save.Stop()
		}
	}
	setSave(saveEvery)
	prune := time.Tick(pruneInterval)
	for {
		select {
		case d := <-s.retime:
			setSave(d)
		case <-save.C:
			if err := s.save(); err != nil {
				s.onErr("failed to save query log snapshot", err)
			}
		case <-prune:
			s.prune()
		case <-s.quit:
			return
		}
	}
}

func (s *Store) ordered() []Entry {
	out := make([]Entry, 0, len(s.buf))
	out = append(out, s.buf[s.head:]...)
	return append(out, s.buf[:s.head]...)
}

func (s *Store) prune() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	cutoff := windowStart(now, s.retention)
	for date := range s.days {
		if date < cutoff {
			delete(s.days, date)
			s.dirty = true
		}
	}
	domainCutoff := windowStart(now, min(s.retention, domainStatsDays))
	for date := range s.domains {
		if date < domainCutoff {
			delete(s.domains, date)
			s.dirty = true
		}
	}
}

func (s *Store) save() error {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	snap := snapshot{NextID: s.nextID, Log: s.ordered(), Days: maps.Clone(s.days), Domains: make(map[string]map[string]counts, len(s.domains))}
	for date, doms := range s.domains {
		snap.Domains[date] = maps.Clone(doms)
	}
	s.dirty = false
	s.mu.Unlock()

	if err := writeSnapshot(s.path, &snap); err != nil {
		s.mu.Lock()
		s.dirty = true
		s.mu.Unlock()
		return err
	}
	return nil
}

func emptySnapshot() *snapshot {
	return &snapshot{Days: map[string]counts{}, Domains: map[string]map[string]counts{}}
}

func readSnapshot(path string) (*snapshot, error) {
	snap := emptySnapshot()
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return snap, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(bufio.NewReader(f))
	if err != nil {
		return nil, err
	}
	if err := gob.NewDecoder(zr).Decode(snap); err != nil {
		return nil, err
	}
	return snap, nil
}

func writeSnapshot(path string, snap *snapshot) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	err = encodeSnapshot(f, snap)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func encodeSnapshot(w io.Writer, snap *snapshot) error {
	bw := bufio.NewWriter(w)
	zw, err := gzip.NewWriterLevel(bw, gzip.BestSpeed)
	if err != nil {
		return err
	}
	if err := gob.NewEncoder(zw).Encode(snap); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return bw.Flush()
}

func windowStart(now time.Time, days int) string {
	return now.AddDate(0, 0, -(days - 1)).Format(time.DateOnly)
}
