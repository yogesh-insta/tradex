// Command nserotator runs one monthly NSE momentum-rotation advisory cycle
// (docs/specs/20-nse-momentum-rotator.md). Advisory-only: it never places
// orders; output is a Telegram message + durable GCS record.
//
// Modes:
//
//	one-shot (default): go run ./cmd/nserotator -config config/config.nserotator.cloudrun.yaml
//	HTTP (Cloud Run):   PORT=8080 → POST/GET /run (add ?force=1 to skip the date gate)
//
// The date gate exits 0 on non-last-trading-days; Cloud Scheduler fires every
// weekday and the gate decides. Required env for delivery: TELEGRAM_BOT_TOKEN,
// TELEGRAM_CHAT_ID (or values in the config file). GCS via ADC.
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
	"github.com/yogesh-insta/tradex/internal/logging"
	"github.com/yogesh-insta/tradex/internal/nserotator"
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
	flag.StringVar(&configPath, "config", "config/config.nserotator.cloudrun.yaml", "path to nserotator config YAML")
	flag.BoolVar(&force, "force", false, "skip the last-trading-day gate (manual/test runs)")
	flag.Parse()

	cfg, err := nserotator.LoadConfig(configPath)
	if err != nil {
		return err
	}
	log := logging.New("nserotator", cfg.Env, slog.LevelInfo)

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
	log.Info("nserotator complete", "month", res.Month, "orders", res.Orders, "skipped", res.Skipped)
	return nil
}

func runOnce(ctx context.Context, cfg *nserotator.Config, log *slog.Logger, force bool) (nserotator.RunResult, error) {
	universe, err := nserotator.LoadUniverse(cfg.Rotator.UniverseFile)
	if err != nil {
		return nserotator.RunResult{}, err
	}
	hol, err := calendar.LoadHolidays(cfg.Rotator.HolidaysFile)
	if err != nil {
		return nserotator.RunResult{}, err
	}

	tgToken := cfg.Telegram.BotToken
	if tgToken == "" {
		tgToken = os.Getenv("TELEGRAM_BOT_TOKEN")
	}
	tgChat := cfg.Telegram.ChatID
	if tgChat == "" {
		tgChat = os.Getenv("TELEGRAM_CHAT_ID")
	}

	store := &nserotator.StateStore{
		GCSPrefix: cfg.Rotator.GCSPrefix,
		LocalDir:  cfg.Rotator.LocalStateDir,
	}
	if cfg.Rotator.GCSPrefix != "" {
		gcs, err := storage.NewClient(ctx)
		if err != nil {
			return nserotator.RunResult{}, fmt.Errorf("gcs client: %w", err)
		}
		defer gcs.Close()
		store.Client = gcs
	}

	deps := nserotator.Deps{
		Yahoo: &nserotator.YahooClient{
			Timeout: time.Duration(cfg.Rotator.YahooTimeoutS) * time.Second,
		},
		Telegram: &calendar.TelegramClient{BotToken: tgToken, ChatID: tgChat},
		Store:    store,
		Holidays: hol,
		Log:      log,
		Now:      time.Now,
	}
	params := nserotator.RunParams{
		Universe:           universe,
		LookbackMonths:     cfg.Rotator.LookbackMonths,
		TopK:               cfg.Rotator.TopK,
		RegimeEMADays:      cfg.Rotator.RegimeEMADays,
		Market:             cfg.Rotator.Market,
		Force:              force,
		DriftCheck:         cfg.Rotator.DriftCheck,
		ExcludedSymbols:    cfg.Rotator.ExcludedSymbols,
		FrozenSymbols:      cfg.Rotator.FrozenSymbols,
		ExitLookbackMonths: cfg.Rotator.ExitLookbackMonths,
		ExitRankN:          cfg.Rotator.ExitRankN,
		RegimeFilter:       *cfg.Rotator.RegimeFilter, // validate() guarantees non-nil
	}
	return nserotator.Run(ctx, params, deps)
}

func serveHTTP(ctx context.Context, listen string, cfg *nserotator.Config, log *slog.Logger) error {
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
		// Budget: ~210 Yahoo fetches (8 workers, retries) + NSE CSV + Telegram + GCS.
		runCtx, cancel := context.WithTimeout(r.Context(), 8*time.Minute)
		defer cancel()
		res, err := runOnce(runCtx, cfg, log, force)
		if err != nil {
			log.Error("nserotator run failed", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Info("nserotator complete", "month", res.Month, "orders", res.Orders, "skipped", res.Skipped)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"month":   res.Month,
			"orders":  res.Orders,
			"skipped": res.Skipped,
		})
	})

	srv := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	log.Info("nserotator listening", "addr", listen)
	err := srv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
