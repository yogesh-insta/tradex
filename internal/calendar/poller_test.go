package calendar

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/genai"
)

func TestRunFinnhubPrimaryWritesAfterTelegram(t *testing.T) {
	var tgOK bool
	finnhub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"economicCalendar":[
			{"country":"US","event":"CPI","impact":"high","time":"2026-07-22 12:30:00"}
		]}`))
	}))
	defer finnhub.Close()
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tgOK = true
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer tg.Close()

	path := filepath.Join(t.TempDir(), "calendar-state.json")
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	res, err := Run(context.Background(), PollerConfig{
		LookaheadDays:         7,
		LocalFile:             path,
		TelegramReview:        true,
		AutoWriteOnTelegramOK: true,
	}, PollerDeps{
		Finnhub:  &FinnhubClient{APIKey: "k", HTTPClient: finnhub.Client(), BaseURL: finnhub.URL},
		Telegram: &TelegramClient{BotToken: "tok", ChatID: "1", HTTPClient: tg.Client(), BaseURL: tg.URL},
		Log:      slog.Default(),
		Now:      func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != SourceFinnhubPrimary || !res.Wrote || !tgOK {
		t.Fatalf("source=%s wrote=%v tg=%v", res.Source, res.Wrote, tgOK)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if !st.AsOf.Equal(now) || len(st.Events) != 1 || st.Events[0].Region != "US" {
		t.Fatalf("state = %+v", st)
	}
}

func TestRunGeminiFallbackOn403(t *testing.T) {
	finnhub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusForbidden)
	}))
	defer finnhub.Close()
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer tg.Close()

	path := filepath.Join(t.TempDir(), "calendar-state.json")
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	geminiBody := `{"as_of":"2026-07-19T10:00:00Z","events":[
		{"region":"JP","title":"BOJ Rate Decision","impact":"high","time":"2026-07-21T03:00:00Z"}
	]}`
	res, err := Run(context.Background(), PollerConfig{
		LookaheadDays: 7, LocalFile: path, TelegramReview: true, AutoWriteOnTelegramOK: true,
	}, PollerDeps{
		Finnhub: &FinnhubClient{APIKey: "k", HTTPClient: finnhub.Client(), BaseURL: finnhub.URL},
		Gemini: &GeminiClient{
			APIKey: "g",
			Generate: func(ctx context.Context, client *genai.Client, model, prompt string) (string, error) {
				return geminiBody, nil
			},
		},
		Telegram: &TelegramClient{BotToken: "tok", ChatID: "1", HTTPClient: tg.Client(), BaseURL: tg.URL},
		Now:      func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != SourceGeminiFallback || !res.Wrote {
		t.Fatalf("got source=%s wrote=%v", res.Source, res.Wrote)
	}
}

func TestParseGeminiJSON(t *testing.T) {
	st, err := parseGeminiJSON("Here you go:\n```json\n{\"as_of\":\"2026-07-19T10:00:00Z\",\"events\":[{\"region\":\"eu\",\"title\":\"ECB Rate Decision\",\"impact\":\"HIGH\",\"time\":\"2026-07-22T12:15:00Z\"}]}\n```")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Events) != 1 || st.Events[0].Region != "EU" || st.Events[0].Impact != "high" {
		t.Fatalf("%+v", st)
	}
}

func TestParseGeminiJSONTrailingObject(t *testing.T) {
	// Reproduces production: "invalid character '{' after top-level value"
	text := `{"as_of":"2026-07-19T18:00:00Z","events":[{"region":"US","title":"CPI","impact":"high","time":"2026-07-22T12:30:00Z"}]}
{"note":"extra grounding blob"}`
	st, err := parseGeminiJSON(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Events) != 1 || st.Events[0].Title != "CPI" {
		t.Fatalf("%+v", st)
	}
}

func TestTelegramSendFailureSkipsWrite(t *testing.T) {
	finnhub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"economicCalendar":[
			{"country":"US","event":"NFP","impact":"high","time":"2026-07-22 12:30:00"}
		]}`))
	}))
	defer finnhub.Close()
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer tg.Close()

	path := filepath.Join(t.TempDir(), "calendar-state.json")
	_, err := Run(context.Background(), PollerConfig{
		LookaheadDays: 7, LocalFile: path, TelegramReview: true, AutoWriteOnTelegramOK: true,
	}, PollerDeps{
		Finnhub:  &FinnhubClient{APIKey: "k", HTTPClient: finnhub.Client(), BaseURL: finnhub.URL},
		Telegram: &TelegramClient{BotToken: "tok", ChatID: "1", HTTPClient: tg.Client(), BaseURL: tg.URL},
		Now:      func() time.Time { return time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC) },
	})
	if err == nil {
		t.Fatal("expected telegram error")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("durable file should not exist, err=%v", err)
	}
}
