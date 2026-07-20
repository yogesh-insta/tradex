package calendar

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"cloud.google.com/go/storage"
)

// PollerConfig configures one Finnhub → Gemini → Telegram → durable write run.
type PollerConfig struct {
	LookaheadDays         int
	LocalFile             string // optional local calendar-state.json path
	GCSObject             string // optional gs://bucket/object
	TelegramReview        bool
	AutoWriteOnTelegramOK bool
	GeminiModel           string
}

// PollerDeps are the injectable clients for the fetch pipeline.
type PollerDeps struct {
	Finnhub  *FinnhubClient
	Gemini   *GeminiClient
	Telegram *TelegramClient
	// GCS optional; required when PollerConfig.GCSObject is set.
	GCS *storage.Client
	Log *slog.Logger
	Now func() time.Time
}

// Result summarizes one poller run.
type Result struct {
	Source string
	State  State
	Wrote  bool
}

// Run executes the resilient calendar pipeline once:
// Finnhub primary → (on failure / 0 matches) Gemini fallback → Telegram → write.
func Run(ctx context.Context, cfg PollerConfig, deps PollerDeps) (Result, error) {
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	nowFn := deps.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn().UTC()
	if cfg.LookaheadDays <= 0 {
		cfg.LookaheadDays = 7
	}
	if cfg.LocalFile == "" && cfg.GCSObject == "" {
		cfg.LocalFile = "data/calendar-state.json"
	}

	var (
		events []Event
		source string
	)

	// 1. Finnhub primary
	if deps.Finnhub != nil {
		raw, err := deps.Finnhub.FetchRaw(ctx, now, cfg.LookaheadDays)
		if err != nil {
			log.Warn("finnhub primary failed; invoking Gemini fallback", "error", err)
		} else {
			events = FilterNormalize(raw)
			if len(events) == 0 {
				log.Warn("finnhub returned 0 matching high-impact events; invoking Gemini fallback")
			} else {
				source = SourceFinnhubPrimary
			}
		}
	} else {
		log.Warn("finnhub client missing; invoking Gemini fallback")
	}

	// 2. Gemini fallback
	if source == "" {
		if deps.Gemini == nil {
			return Result{}, fmt.Errorf("calendar poller: Finnhub yielded no events and Gemini client is nil")
		}
		deps.Gemini.Model = firstNonEmpty(deps.Gemini.Model, cfg.GeminiModel, defaultGeminiModel)
		if deps.Gemini.Log == nil {
			deps.Gemini.Log = log
		}
		st, err := deps.Gemini.FetchState(ctx, now, cfg.LookaheadDays)
		if err != nil {
			return Result{}, fmt.Errorf("calendar poller: Gemini fallback failed: %w", err)
		}
		events = st.Events
		if len(events) == 0 {
			return Result{}, fmt.Errorf("calendar poller: Gemini returned 0 matching events")
		}
		source = SourceGeminiFallback
	}

	state := State{AsOf: now, Events: events}

	// 3. Telegram review
	if cfg.TelegramReview {
		if deps.Telegram == nil {
			return Result{}, fmt.Errorf("calendar poller: telegram_review enabled but Telegram client is nil")
		}
		if err := deps.Telegram.SendCalendarReview(ctx, source, state); err != nil {
			return Result{Source: source, State: state}, fmt.Errorf("calendar poller: Telegram send failed (durable state not written): %w", err)
		}
		log.Info("telegram calendar review sent", "source", source, "events", len(state.Events))
	}

	// 4. Durable write (after Telegram OK when configured; or always if review off)
	wrote := false
	shouldWrite := !cfg.TelegramReview || cfg.AutoWriteOnTelegramOK
	if shouldWrite {
		if cfg.LocalFile != "" {
			if err := WriteStateFile(cfg.LocalFile, state); err != nil {
				return Result{Source: source, State: state}, fmt.Errorf("calendar poller: write local state: %w", err)
			}
			wrote = true
			log.Info("wrote durable calendar state", "path", cfg.LocalFile, "as_of", state.AsOf, "events", len(state.Events))
		}
		if cfg.GCSObject != "" {
			if deps.GCS == nil {
				return Result{Source: source, State: state}, fmt.Errorf("calendar poller: gcs_object set but GCS client is nil")
			}
			if err := WriteStateGCS(ctx, deps.GCS, cfg.GCSObject, state); err != nil {
				return Result{Source: source, State: state}, fmt.Errorf("calendar poller: write gcs state: %w", err)
			}
			wrote = true
			log.Info("wrote durable calendar state", "gcs", cfg.GCSObject, "as_of", state.AsOf, "events", len(state.Events))
		}
	}

	return Result{Source: source, State: state, Wrote: wrote}, nil
}

// WriteStateFile atomically writes calendar-state.json (temp + rename).
func WriteStateFile(path string, state State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
