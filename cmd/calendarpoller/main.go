// Command calendarpoller runs one Finnhub → Gemini → Telegram → durable write
// cycle for the economic calendar (docs/specs/10-economic-calendar.md).
//
// Intended for Cloud Scheduler → Cloud Run (or a local cron). The VM trader
// only reads the durable file/GCS object; it never calls these providers.
//
//	go run ./cmd/calendarpoller -config config/config.dev.yaml
//
// Required env:
//
//	FINNHUB_API_KEY, GEMINI_API_KEY, TELEGRAM_BOT_TOKEN, TELEGRAM_CHAT_ID
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

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
	if localFile == "" {
		localFile = ec.StateFile
	}
	if localFile == "" {
		localFile = "data/calendar-state.json"
	}
	lookahead := ec.LookaheadDays
	if lookahead <= 0 {
		lookahead = 7
	}
	model := ec.GeminiModel
	if model == "" {
		model = "gemini-2.5-flash"
	}

	telegramReview := true
	if ec.TelegramReview != nil {
		telegramReview = *ec.TelegramReview
	}
	autoWrite := true
	if ec.AutoWriteOnTelegramOK != nil {
		autoWrite = *ec.AutoWriteOnTelegramOK
	}

	res, err := calendar.Run(ctx, calendar.PollerConfig{
		LookaheadDays:         lookahead,
		LocalFile:             localFile,
		TelegramReview:        telegramReview,
		AutoWriteOnTelegramOK: autoWrite,
		GeminiModel:           model,
	}, calendar.PollerDeps{
		Finnhub:  &calendar.FinnhubClient{APIKey: finnhubKey},
		Gemini:   &calendar.GeminiClient{APIKey: geminiKey, Model: model},
		Telegram: &calendar.TelegramClient{BotToken: tgToken, ChatID: tgChat},
		Log:      log,
		Now:      time.Now,
	})
	if err != nil {
		return err
	}
	log.Info("calendar poll complete",
		"source", res.Source,
		"events", len(res.State.Events),
		"wrote", res.Wrote,
		"as_of", res.State.AsOf,
		"path", localFile)
	return nil
}
