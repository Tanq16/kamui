package store

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS querylog (
	id INTEGER PRIMARY KEY,
	time INTEGER NOT NULL,
	client TEXT NOT NULL,
	domain TEXT NOT NULL,
	qtype TEXT NOT NULL,
	result TEXT NOT NULL,
	reason TEXT NOT NULL,
	upstream TEXT NOT NULL,
	cached INTEGER NOT NULL,
	rcode TEXT NOT NULL,
	duration REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS daily (
	date TEXT PRIMARY KEY,
	total INTEGER NOT NULL,
	blocked INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS daily_domains (
	date TEXT NOT NULL,
	domain TEXT NOT NULL,
	queries INTEGER NOT NULL,
	blocked INTEGER NOT NULL,
	PRIMARY KEY (date, domain)
);
`

const (
	logBuffer     = 8192
	batchSize     = 512
	batchInterval = time.Second
	statsInterval = 60 * time.Second
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
	total   int64
	blocked int64
}

type pending struct {
	days    map[string]*counts
	domains map[string]map[string]*counts
}

func newPending() *pending {
	return &pending{days: map[string]*counts{}, domains: map[string]map[string]*counts{}}
}

type Store struct {
	db        *sql.DB
	logCh     chan Entry
	quit      chan struct{}
	done      chan struct{}
	dropped   atomic.Int64
	logSize   atomic.Int64
	retention atomic.Int64

	countMu sync.Mutex
	pending *pending
	flushMu sync.RWMutex

	onErr func(msg string, err error)
}

func Open(path string, logSize, retentionDays int, onErr func(msg string, err error)) (*Store, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{
		db:      db,
		logCh:   make(chan Entry, logBuffer),
		quit:    make(chan struct{}),
		done:    make(chan struct{}),
		pending: newPending(),
		onErr:   onErr,
	}
	s.SetLimits(logSize, retentionDays)
	go s.run()
	return s, nil
}

func (s *Store) SetLimits(logSize, retentionDays int) {
	s.logSize.Store(int64(logSize))
	s.retention.Store(int64(retentionDays))
}

func (s *Store) Dropped() int64 {
	return s.dropped.Load()
}

func (s *Store) Log(e Entry) {
	date := time.UnixMilli(e.Time).Format(time.DateOnly)
	blocked := int64(0)
	if e.Result == "blocked" {
		blocked = 1
	}
	s.countMu.Lock()
	d := s.pending.days[date]
	if d == nil {
		d = &counts{}
		s.pending.days[date] = d
	}
	d.total++
	d.blocked += blocked
	doms := s.pending.domains[date]
	if doms == nil {
		doms = map[string]*counts{}
		s.pending.domains[date] = doms
	}
	c := doms[e.Domain]
	if c == nil {
		c = &counts{}
		doms[e.Domain] = c
	}
	c.total++
	c.blocked += blocked
	s.countMu.Unlock()

	select {
	case s.logCh <- e:
	default:
		s.dropped.Add(1)
	}
}

func (s *Store) Close() error {
	close(s.quit)
	<-s.done
	return s.db.Close()
}

func (s *Store) run() {
	defer close(s.done)
	batchTick := time.NewTicker(batchInterval)
	defer batchTick.Stop()
	statsTick := time.NewTicker(statsInterval)
	defer statsTick.Stop()
	pruneTick := time.NewTicker(24 * time.Hour)
	defer pruneTick.Stop()

	s.pruneStats()
	batch := make([]Entry, 0, batchSize)
	write := func() {
		if len(batch) == 0 {
			return
		}
		if err := s.insert(batch); err != nil {
			s.onErr("failed to write query log", err)
		}
		batch = batch[:0]
	}
	for {
		select {
		case e := <-s.logCh:
			batch = append(batch, e)
			if len(batch) >= batchSize {
				write()
			}
		case <-batchTick.C:
			write()
		case <-statsTick.C:
			s.flushStats()
		case <-pruneTick.C:
			s.pruneStats()
		case <-s.quit:
			for len(s.logCh) > 0 {
				batch = append(batch, <-s.logCh)
			}
			write()
			s.flushStats()
			return
		}
	}
}

func (s *Store) insert(batch []Entry) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO querylog (time, client, domain, qtype, result, reason, upstream, cached, rcode, duration) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, e := range batch {
		if _, err := stmt.Exec(e.Time, e.Client, e.Domain, e.Type, e.Result, e.Reason, e.Upstream, e.Cached, e.Rcode, e.DurationMs); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM querylog WHERE id <= (SELECT MAX(id) FROM querylog) - ?`, s.logSize.Load()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) flushStats() {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	s.countMu.Lock()
	p := s.pending
	s.pending = newPending()
	s.countMu.Unlock()
	if len(p.days) == 0 {
		return
	}
	if err := s.writeStats(p); err != nil {
		s.onErr("failed to write statistics", err)
		s.countMu.Lock()
		merge(s.pending, p)
		s.countMu.Unlock()
	}
}

func merge(dst, src *pending) {
	for date, c := range src.days {
		d := dst.days[date]
		if d == nil {
			d = &counts{}
			dst.days[date] = d
		}
		d.total += c.total
		d.blocked += c.blocked
	}
	for date, doms := range src.domains {
		dd := dst.domains[date]
		if dd == nil {
			dd = map[string]*counts{}
			dst.domains[date] = dd
		}
		for dom, c := range doms {
			x := dd[dom]
			if x == nil {
				x = &counts{}
				dd[dom] = x
			}
			x.total += c.total
			x.blocked += c.blocked
		}
	}
}

func (s *Store) writeStats(p *pending) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for date, c := range p.days {
		if _, err := tx.Exec(`INSERT INTO daily (date, total, blocked) VALUES (?, ?, ?)
			ON CONFLICT(date) DO UPDATE SET total = total + excluded.total, blocked = blocked + excluded.blocked`, date, c.total, c.blocked); err != nil {
			return err
		}
	}
	stmt, err := tx.Prepare(`INSERT INTO daily_domains (date, domain, queries, blocked) VALUES (?, ?, ?, ?)
		ON CONFLICT(date, domain) DO UPDATE SET queries = queries + excluded.queries, blocked = blocked + excluded.blocked`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for date, doms := range p.domains {
		for dom, c := range doms {
			if _, err := stmt.Exec(date, dom, c.total, c.blocked); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Store) pruneStats() {
	cutoff := windowStart(time.Now(), int(s.retention.Load()))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, q := range []string{`DELETE FROM daily WHERE date < ?`, `DELETE FROM daily_domains WHERE date < ?`} {
		if _, err := s.db.ExecContext(ctx, q, cutoff); err != nil {
			s.onErr("failed to prune statistics", err)
		}
	}
}

func windowStart(now time.Time, days int) string {
	return now.AddDate(0, 0, -(days - 1)).Format(time.DateOnly)
}
