// Command dashboard is the read-only Cloud Run ops UI (spec 14).
// It never places, modifies, or cancels OANDA orders and does not expose
// control-plane commands.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/yogesh-insta/tradex/internal/config"
	"github.com/yogesh-insta/tradex/internal/dashboard"
	"github.com/yogesh-insta/tradex/internal/logging"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	var configPath string
	flag.StringVar(&configPath, "config", "config/config.dashboard.cloudrun.yaml", "path to environment config YAML")
	flag.Parse()

	cfg, err := config.Load(configPath, config.EnvResolver{})
	if err != nil {
		return err
	}
	if err := cfg.ValidateDashboard(); err != nil {
		return fmt.Errorf("dashboard config validation failed:\n%w", err)
	}

	log := logging.New("dashboard", cfg.Env, slog.LevelInfo)
	log.Info("effective config", "config", cfg.Redacted())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	srv, err := dashboard.BuildFromConfig(ctx, cfg, log)
	if err != nil {
		return err
	}

	addr := cfg.Dashboard.ListenAddr
	// Cloud Run injects PORT.
	if p := os.Getenv("PORT"); p != "" {
		addr = ":" + p
	}
	log.Info("dashboard listening", "addr", addr, "mock", cfg.Dashboard.Mock, "auth", cfg.Dashboard.Auth.Mode)
	return srv.ListenAndServe(ctx, addr)
}
