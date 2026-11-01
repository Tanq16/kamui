package cmd

import (
	"cmp"
	"context"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/Tanq16/kamui/internal/blocklist"
	"github.com/Tanq16/kamui/internal/cache"
	"github.com/Tanq16/kamui/internal/config"
	"github.com/Tanq16/kamui/internal/dnsserver"
	"github.com/Tanq16/kamui/internal/filter"
	"github.com/Tanq16/kamui/internal/server"
	"github.com/Tanq16/kamui/internal/store"
	"github.com/Tanq16/kamui/internal/upstream"
)

var serveFlags struct {
	dnsAddr  string
	httpAddr string
	dataDir  string
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the DNS server and the web UI",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		serve()
	},
}

func init() {
	serveCmd.Flags().StringVar(&serveFlags.dnsAddr, "dns-addr", cmp.Or(os.Getenv("KAMUI_DNS_ADDR"), "0.0.0.0:53"), "DNS listen address for UDP and TCP (or KAMUI_DNS_ADDR env)")
	serveCmd.Flags().StringVar(&serveFlags.httpAddr, "http-addr", cmp.Or(os.Getenv("KAMUI_HTTP_ADDR"), "0.0.0.0:8053"), "Web UI and API listen address (or KAMUI_HTTP_ADDR env)")
	serveCmd.Flags().StringVar(&serveFlags.dataDir, "data-dir", cmp.Or(os.Getenv("KAMUI_DATA_DIR"), "./data"), "Directory for config.json, kamui.db, and lists (or KAMUI_DATA_DIR env)")
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

func serve() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dataDir := serveFlags.dataDir
	if err := os.MkdirAll(filepath.Join(dataDir, "lists"), 0o700); err != nil {
		log.Fatal().Err(err).Str("dir", dataDir).Msg("failed to create data directory")
	}
	cfgMgr, err := config.Load(filepath.Join(dataDir, "config.json"))
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config.json")
	}
	cfg := cfgMgr.Get()
	set := cfg.Settings

	up, err := upstream.New(upstreamConfig(set))
	if err != nil {
		log.Fatal().Err(err).Msg("failed to set up upstreams")
	}
	listClient := &http.Client{Transport: &http.Transport{
		DialContext:           up.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	}}
	lists := blocklist.NewManager(filepath.Join(dataDir, "lists"), listClient)
	flt := filter.New(lists, cfg)
	respCache := cache.New(set.CacheSize, set.CacheMinTTL, set.CacheMaxTTL)
	st, err := store.Open(filepath.Join(dataDir, "kamui.db"), set.LogSize, set.RetentionDays)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to open kamui.db")
	}

	web := server.New(ctx, serveFlags.httpAddr, server.Deps{
		Version:  AppVersion,
		Config:   cfgMgr,
		Lists:    lists,
		Filter:   flt,
		Cache:    respCache,
		Upstream: up,
		Store:    st,
	})
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

	dnsSrv := dnsserver.New(serveFlags.dnsAddr, dnsserver.Deps{
		Config:   cfgMgr,
		Filter:   flt,
		Cache:    respCache,
		Upstream: up,
		Store:    st,
	})
	if err := dnsSrv.Start(); err != nil {
		log.Fatal().Err(err).Str("addr", serveFlags.dnsAddr).Msg("failed to bind DNS listener")
	}
	log.Info().Str("addr", serveFlags.dnsAddr).Msg("DNS server listening on UDP and TCP")

	if err := web.Setup(); err != nil {
		log.Fatal().Err(err).Msg("failed to set up web server")
	}
	webErr := make(chan error, 1)
	go func() { webErr <- web.Run() }()
	go web.RunListRefresh()

	failed := false
	select {
	case <-ctx.Done():
	case err := <-webErr:
		if err != nil {
			failed = true
			log.Error().Err(err).Str("addr", serveFlags.httpAddr).Msg("web server failed")
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
	if err := st.Close(); err != nil {
		log.Warn().Err(err).Msg("failed to close kamui.db")
	}
	if failed {
		os.Exit(1)
	}
}
