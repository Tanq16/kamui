package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/Tanq16/kamui/internal/blocklist"
	"github.com/Tanq16/kamui/internal/cache"
	"github.com/Tanq16/kamui/internal/config"
	"github.com/Tanq16/kamui/internal/dnsserver"
	"github.com/Tanq16/kamui/internal/filter"
	"github.com/Tanq16/kamui/internal/server"
	"github.com/Tanq16/kamui/internal/store"
	"github.com/Tanq16/kamui/internal/upstream"
)

type Config struct {
	DNSAddr  string
	HTTPAddr string
	DataDir  string
	Version  string
}

func upstreamConfig(s config.Settings) upstream.Config {
	return upstream.Config{
		Upstreams: s.Upstreams,
		Bootstrap: s.Bootstrap,
		Hedge:     time.Duration(s.HedgeDelayMs) * time.Millisecond,
		Timeout:   time.Duration(s.TimeoutMs) * time.Millisecond,
		Router:    s.Router,
	}
}

func Run(ctx context.Context, c Config) error {
	if err := os.MkdirAll(filepath.Join(c.DataDir, "lists"), 0o700); err != nil {
		return fmt.Errorf("create data directory %s: %w", c.DataDir, err)
	}
	cfgMgr, err := config.Load(filepath.Join(c.DataDir, "config.json"))
	if err != nil {
		return fmt.Errorf("load config.json: %w", err)
	}
	cfg := cfgMgr.Get()
	set := cfg.Settings

	up, err := upstream.New(upstreamConfig(set))
	if err != nil {
		return fmt.Errorf("set up upstreams: %w", err)
	}
	listClient := &http.Client{Transport: &http.Transport{
		DialContext:           up.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	}}
	lists := blocklist.NewManager(filepath.Join(c.DataDir, "lists"), listClient)
	flt := filter.New(lists, cfg)
	respCache := cache.New(set.CacheSize, set.CacheMinTTL, set.CacheMaxTTL)
	st, err := store.Open(filepath.Join(c.DataDir, "kamui.db"), set.LogSize, set.RetentionDays, func(msg string, err error) {
		log.Error().Err(err).Msg(msg)
	})
	if err != nil {
		return fmt.Errorf("open kamui.db: %w", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			log.Warn().Err(err).Msg("failed to close kamui.db")
		}
	}()

	web := server.New(ctx, c.HTTPAddr, server.Deps{
		Version:  c.Version,
		Config:   cfgMgr,
		Lists:    lists,
		Filter:   flt,
		Cache:    respCache,
		Upstream: up,
		Store:    st,
	})
	if err := web.Setup(); err != nil {
		return fmt.Errorf("set up web server: %w", err)
	}
	web.SyncLists(cfg.Lists)

	cfgMgr.Subscribe(func(old, cur *config.Config) {
		flt.Update(cur)
		if err := up.Apply(upstreamConfig(cur.Settings)); err != nil {
			log.Error().Err(err).Msg("failed to apply upstream settings")
		}
		respCache.SetLimits(cur.Settings.CacheSize, cur.Settings.CacheMinTTL, cur.Settings.CacheMaxTTL)
		st.SetLimits(cur.Settings.LogSize, cur.Settings.RetentionDays)
		web.SyncLists(cur.Lists)
		if old.Blocking != cur.Blocking {
			respCache.Flush()
			log.Info().Str("mode", cur.Blocking.Mode).Msg("blocking state changed")
		}
	})

	dnsSrv := dnsserver.New(c.DNSAddr, dnsserver.Deps{
		Config:   cfgMgr,
		Filter:   flt,
		Cache:    respCache,
		Upstream: up,
		Store:    st,
	})
	if err := dnsSrv.Start(); err != nil {
		return fmt.Errorf("bind DNS listener on %s: %w", c.DNSAddr, err)
	}
	log.Info().Str("addr", c.DNSAddr).Msg("DNS server listening on UDP and TCP")

	webErr := make(chan error, 1)
	go func() { webErr <- web.Run() }()
	go web.RunListRefresh()

	var runErr error
	select {
	case <-ctx.Done():
	case err := <-webErr:
		if err != nil {
			runErr = fmt.Errorf("web server on %s: %w", c.HTTPAddr, err)
		}
	}

	log.Info().Msg("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := dnsSrv.Shutdown(shutdownCtx); err != nil {
		log.Warn().Err(err).Msg("DNS shutdown incomplete")
	}
	if err := web.Shutdown(shutdownCtx); err != nil {
		log.Warn().Err(err).Msg("web shutdown incomplete")
	}
	return runErr
}
