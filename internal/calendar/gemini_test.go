package calendar

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"google.golang.org/genai"
)

func TestFetchStateRetriesTransientThenSucceeds(t *testing.T) {
	body := `{"as_of":"2026-07-20T08:00:00Z","events":[
		{"region":"US","title":"CPI","impact":"high","time":"2026-07-22T12:30:00Z"}
	]}`
	var calls int
	var sleeps []time.Duration
	c := &GeminiClient{
		APIKey:      "k",
		MaxRetries:  3,
		BackoffBase: time.Second,
		BackoffMax:  8 * time.Second,
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sleep: func(ctx context.Context, d time.Duration) error {
			sleeps = append(sleeps, d)
			return nil
		},
		Generate: func(ctx context.Context, client *genai.Client, model, prompt string) (string, error) {
			calls++
			if calls < 3 {
				return "", fmtWrap(genai.APIError{
					Code: 503, Status: "UNAVAILABLE",
					Message: "This model is currently experiencing high demand.",
				})
			}
			return body, nil
		},
	}
	st, err := c.FetchState(context.Background(), time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC), 7)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("calls=%d want 3", calls)
	}
	if len(sleeps) != 2 {
		t.Fatalf("sleeps=%v want 2", sleeps)
	}
	// Exp backoff before jitter: 1s then 2s; jitter adds [0,d].
	if sleeps[0] < time.Second || sleeps[0] > 2*time.Second {
		t.Fatalf("first backoff %v out of range", sleeps[0])
	}
	if sleeps[1] < 2*time.Second || sleeps[1] > 4*time.Second {
		t.Fatalf("second backoff %v out of range", sleeps[1])
	}
	if len(st.Events) != 1 || st.Events[0].Region != "US" {
		t.Fatalf("%+v", st)
	}
}

func TestFetchStateNoRetryOnAuthError(t *testing.T) {
	var calls int
	c := &GeminiClient{
		APIKey:     "k",
		MaxRetries: 4,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sleep: func(ctx context.Context, d time.Duration) error {
			t.Fatal("should not sleep on permanent error")
			return nil
		},
		Generate: func(ctx context.Context, client *genai.Client, model, prompt string) (string, error) {
			calls++
			return "", fmtWrap(genai.APIError{Code: 403, Status: "PERMISSION_DENIED", Message: "nope"})
		},
	}
	_, err := c.FetchState(context.Background(), time.Now().UTC(), 7)
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Fatalf("calls=%d want 1", calls)
	}
}

func TestGeminiTransient(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{context.Canceled, false},
		{context.DeadlineExceeded, false},
		{genai.APIError{Code: 503, Status: "UNAVAILABLE"}, true},
		{genai.APIError{Code: 429, Status: "RESOURCE_EXHAUSTED"}, true},
		{genai.APIError{Code: 403, Status: "PERMISSION_DENIED"}, false},
		{fmtWrap(genai.APIError{Code: 503, Message: "high demand"}), true},
		{errors.New("gemini: empty response"), true},
		{errors.New("gemini: schema parse: unexpected end"), false},
	}
	for _, tc := range cases {
		if got := geminiTransient(tc.err); got != tc.want {
			t.Fatalf("geminiTransient(%v)=%v want %v", tc.err, got, tc.want)
		}
	}
}

func fmtWrap(err error) error {
	return fmt.Errorf("gemini: generate: %w", err)
}
