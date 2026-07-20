// Command calendarpoller runs one Finnhub → Gemini → Telegram → durable write
// cycle for the economic calendar (docs/specs/10-economic-calendar.md).
//
// Modes:
//
//	one-shot (default): go run ./cmd/calendarpoller -config config/config.dev.yaml
//	HTTP (Cloud Run):   LISTEN_ADDR=:8080 or PORT=8080 → POST/GET /run
//
// Required env: FINNHUB_API_KEY, GEMINI_API_KEY, TELEGRAM_* (when review on).
// GCS writes use Application Default Credentials.
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
	"syscall"
	"time"

	"cloud.google.com/go/storage"

	"github.com/yogesh-insta/tradex/internal/calendar"
	"github.com/yogesh-insta/tradex/internal/config"
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
	flag.StringVar(&configPath, "config", "config/config.dev.yaml", "path to environment config YAML")
	flag.Parse()

	cfg, err := config.Load(configPath, config.EnvResolver{})
	if err != nil {
		return err
	}

	log := logging.New("calendarpoller", cfg.Env, slog.LevelInfo)
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
	res, err := runOnce(ctx, cfg, log)
	if err != nil {
		return err
	}
	log.Info("calendar poll complete",
		"source", res.Source,
		"events", len(res.State.Events),
		"wrote", res.Wrote,
		"as_of", res.State.AsOf)
	return nil
}

func runOnce(ctx context.Context, cfg *config.Config, log *slog.Logger) (calendar.Result, error) {
	finnhubKey := os.Getenv("FINNHUB_API_KEY")
	geminiKey := os.Getenv("GEMINI_API_KEY")
	tgToken := cfg.Observability.Telegram.BotToken
	if tgToken == "" {
		tgToken = os.Getenv("TELEGRAM_BOT_TOKEN")
	}
	tgChat := cfg.Observability.Telegram.ChatID
	if tgChat == "" {
		tgChat = os.Getenv("TELEGRAM_CHAT_ID")
	}

	ec := cfg.Calendar.Economic
	localFile := ec.LocalFile
	gcsObject := ec.GCSObject
	if localFile == "" && gcsObject == "" {
		localFile = ec.StateFile
	}
	if localFile == "" && gcsObject == "" {
		localFile = "data/calendar-state.json"
	}
	lookahead := ec.LookaheadDays
	if lookahead <= 0 {
		lookahead = 7
	}
	model := ec.GeminiModel
	if model == "" {
		model = "gemini-2.5-flash-lite"
	}

	telegramReview := true
	if ec.TelegramReview != nil {
		telegramReview = *ec.TelegramReview
	}
	autoWrite := true
	if ec.AutoWriteOnTelegramOK != nil {
		autoWrite = *ec.AutoWriteOnTelegramOK
	}

	deps := calendar.PollerDeps{
		Finnhub:  &calendar.FinnhubClient{APIKey: finnhubKey},
		Gemini:   &calendar.GeminiClient{APIKey: geminiKey, Model: model},
		Telegram: &calendar.TelegramClient{BotToken: tgToken, ChatID: tgChat},
		Log:      log,
		Now:      time.Now,
	}
	if gcsObject != "" {
		gcs, err := storage.NewClient(ctx)
		if err != nil {
			return calendar.Result{}, fmt.Errorf("gcs client: %w", err)
		}
		defer gcs.Close()
		deps.GCS = gcs
	}

	return calendar.Run(ctx, calendar.PollerConfig{
		LookaheadDays:         lookahead,
		LocalFile:             localFile,
		GCSObject:             gcsObject,
		TelegramReview:        telegramReview,
		AutoWriteOnTelegramOK: autoWrite,
		GeminiModel:           model,
	}, deps)
}

func serveHTTP(ctx context.Context, listen string, cfg *config.Config, log *slog.Logger) error {
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
		// Budget covers Finnhub + Gemini retries (≤5 attempts, exp backoff ≤~1m) + Telegram/GCS.
		runCtx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		res, err := runOnce(runCtx, cfg, log)
		if err != nil {
			log.Error("calendar poll failed", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Info("calendar poll complete",
			"source", res.Source,
			"events", len(res.State.Events),
			"wrote", res.Wrote,
			"as_of", res.State.AsOf)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"source": res.Source,
			"events": len(res.State.Events),
			"wrote":  res.Wrote,
			"as_of":  res.State.AsOf,
		})
	})

	srv := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	log.Info("calendarpoller listening", "addr", listen)
	err := srv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
