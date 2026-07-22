// Command etfmonitor runs one monthly ASX ETF momentum advisory cycle
// (docs/specs/21-asx-etf-monitor.md). Advisory-only: it never places orders;
// output is a Telegram message + durable GCS record.
//
// Modes (mirrors cmd/nserotator):
//
//	one-shot (default): go run ./cmd/etfmonitor -config config/config.etfmonitor.dev.yaml
//	HTTP (Cloud Run):   PORT=8080 → POST/GET /run (add ?force=1 to bypass guards)
//
// Cloud Scheduler fires monthly on the 1st. Required env for delivery:
// TELEGRAM_BOT_TOKEN, TELEGRAM_CHAT_ID (or values in the config file). GCS via ADC.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"cloud.google.com/go/storage"

	"github.com/yogesh-insta/tradex/internal/calendar"
	"github.com/yogesh-insta/tradex/internal/etfmonitor"
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
	var force bool
	flag.StringVar(&configPath, "config", "config/config.etfmonitor.cloudrun.yaml", "path to etfmonitor config YAML")
	flag.BoolVar(&force, "force", false, "bypass freshness guards (manual/test runs)")
	flag.Parse()

	cfg, err := etfmonitor.LoadConfig(configPath)
	if err != nil {
		return err
	}
	log := logging.New("etfmonitor", cfg.Env, slog.LevelInfo)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	listen := os.Getenv("LISTEN_ADDR")
	if listen == "" {
		if p := os.Getenv("PORT"); p != "" {
			listen = ":" + p
		}
	}
	if listen != "" {
		return serveHTTP(ctx, listen, cfg, log)
	}

	res, err := runOnce(ctx, cfg, log, force)
	if err != nil {
		return err
	}
	log.Info("etfmonitor complete", "month", res.Month, "top", res.TopN, "exits", res.Exits)
	return nil
}

func runOnce(ctx context.Context, cfg *etfmonitor.Config, log *slog.Logger, force bool) (etfmonitor.RunResult, error) {
	universe, err := etfmonitor.LoadUniverse(cfg.Monitor.UniverseFile)
	if err != nil {
		return etfmonitor.RunResult{}, err
	}

	tgToken := cfg.Telegram.BotToken
	if tgToken == "" {
		tgToken = os.Getenv("TELEGRAM_BOT_TOKEN")
	}
	tgChat := cfg.Telegram.ChatID
	if tgChat == "" {
		tgChat = os.Getenv("TELEGRAM_CHAT_ID")
	}

	store := &etfmonitor.StateStore{
		GCSPrefix: cfg.Monitor.GCSPrefix,
		LocalDir:  cfg.Monitor.LocalStateDir,
	}
	if cfg.Monitor.GCSPrefix != "" {
		gcs, err := storage.NewClient(ctx)
		if err != nil {
			return etfmonitor.RunResult{}, fmt.Errorf("gcs client: %w", err)
		}
		defer gcs.Close()
		store.Client = gcs
	}

	deps := etfmonitor.Deps{
		Yahoo: &etfmonitor.YahooClient{
			Timeout: time.Duration(cfg.Monitor.YahooTimeoutS) * time.Second,
		},
		Telegram: &calendar.TelegramClient{BotToken: tgToken, ChatID: tgChat},
		Store:    store,
		Log:      log,
		Now:      time.Now,
	}
	params := etfmonitor.RunParams{
		Universe:     universe,
		LookbacksTD:  cfg.Monitor.MomentumLookbacksTD,
		Weights:      cfg.Monitor.RecencyWeights,
		TrendSMADays: cfg.Monitor.TrendSMADays,
		TopN:         cfg.Monitor.TopN,
		DriftCheck:   cfg.Monitor.DriftCheck,
		Force:        force,
	}
	return etfmonitor.Run(ctx, params, deps)
}

func serveHTTP(ctx context.Context, listen string, cfg *etfmonitor.Config, log *slog.Logger) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/run" {
			http.NotFound(w, r)
			return
		}
		force, _ := strconv.ParseBool(r.URL.Query().Get("force"))
		// Budget: ~85 Yahoo fetches (8 workers, retries) + drift CSV + Telegram + GCS.
		runCtx, cancel := context.WithTimeout(r.Context(), 8*time.Minute)
		defer cancel()
		res, err := runOnce(runCtx, cfg, log, force)
		if err != nil {
			log.Error("etfmonitor run failed", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Info("etfmonitor complete", "month", res.Month, "top", res.TopN, "exits", res.Exits)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":    true,
			"month": res.Month,
			"top":   res.TopN,
			"exits": res.Exits,
		})
	})

	srv := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	log.Info("etfmonitor listening", "addr", listen)
	err := srv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
