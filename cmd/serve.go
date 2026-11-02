package cmd

import (
	"cmp"
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/Tanq16/kamui/internal/app"
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
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		cfg := app.Config{
			DNSAddr:  serveFlags.dnsAddr,
			HTTPAddr: serveFlags.httpAddr,
			DataDir:  serveFlags.dataDir,
			Version:  AppVersion,
		}
		if err := app.Run(ctx, cfg); err != nil {
			log.Fatal().Err(err).Msg("failed to serve")
		}
	},
}

func init() {
	serveCmd.Flags().StringVar(&serveFlags.dnsAddr, "dns-addr", cmp.Or(os.Getenv("KAMUI_DNS_ADDR"), "0.0.0.0:53"), "DNS listen address for UDP and TCP (or KAMUI_DNS_ADDR env)")
	serveCmd.Flags().StringVar(&serveFlags.httpAddr, "http-addr", cmp.Or(os.Getenv("KAMUI_HTTP_ADDR"), "0.0.0.0:8053"), "Web UI and API listen address (or KAMUI_HTTP_ADDR env)")
	serveCmd.Flags().StringVar(&serveFlags.dataDir, "data-dir", cmp.Or(os.Getenv("KAMUI_DATA_DIR"), "./data"), "Directory for config.json, kamui.db, and lists (or KAMUI_DATA_DIR env)")
}
