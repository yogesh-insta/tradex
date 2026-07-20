package calendar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"time"

	"google.golang.org/genai"
)

// GeminiClient is the Live Search fallback using the official Google GenAI Go
// SDK (google.golang.org/genai). Model defaults to gemini-2.5-flash-lite with
// Google Search grounding enabled.
//
// Note: older generative-ai-go is deprecated; this package uses Models.GenerateContent
// with Tool{GoogleSearch: &genai.GoogleSearch{}} (not GoogleSearchRetrieval).
type GeminiClient struct {
	APIKey string
	Model  string
	// MaxRetries is additional attempts after the first (default 4 → 5 total).
	MaxRetries int
	// BackoffBase / BackoffMax tune exponential backoff + full jitter between retries.
	BackoffBase time.Duration
	BackoffMax  time.Duration
	// NewClient allows tests to inject a stub; production uses genai.NewClient.
	NewClient func(ctx context.Context, cfg *genai.ClientConfig) (*genai.Client, error)
	// Generate overrides the SDK call for unit tests.
	Generate func(ctx context.Context, client *genai.Client, model string, prompt string) (string, error)
	// Sleep overrides time.After for unit tests (must respect ctx cancellation).
	Sleep func(ctx context.Context, d time.Duration) error
	Log   *slog.Logger
}

const (
	defaultGeminiModel       = "gemini-2.5-flash-lite"
	defaultGeminiMaxRetries  = 4
	defaultGeminiBackoffBase = 2 * time.Second
	defaultGeminiBackoffMax  = 30 * time.Second
)

// FetchState asks Gemini (with Google Search) for the next lookaheadDays of
// high-impact EU/US/JP events and returns a parsed State. as_of is stamped
// by the caller after success. Transient generate failures (503/429/etc.) are
// retried with exponential backoff + full jitter.
func (c *GeminiClient) FetchState(ctx context.Context, now time.Time, lookaheadDays int) (State, error) {
	if c.APIKey == "" {
		return State{}, fmt.Errorf("gemini: API key empty")
	}
	model := c.Model
	if model == "" {
		model = defaultGeminiModel
	}
	if lookaheadDays <= 0 {
		lookaheadDays = 7
	}

	prompt := geminiPrompt(now, lookaheadDays)
	text, err := c.generateWithRetry(ctx, model, prompt)
	if err != nil {
		return State{}, err
	}
	st, err := parseGeminiJSON(text)
	if err != nil {
		return State{}, err
	}
	// Re-filter through the same allowlist as Finnhub.
	raw := make([]RawEvent, 0, len(st.Events))
	for _, ev := range st.Events {
		raw = append(raw, RawEvent{
			Country: ev.Region, // already region codes from the model
			Title:   ev.Title,
			Impact:  ev.Impact,
			Time:    ev.Time,
		})
	}
	filtered := FilterNormalize(raw)
	return State{Events: filtered}, nil
}

func (c *GeminiClient) generateWithRetry(ctx context.Context, model, prompt string) (string, error) {
	retries := c.MaxRetries
	if retries <= 0 {
		retries = defaultGeminiMaxRetries
	}
	base := c.BackoffBase
	if base <= 0 {
		base = defaultGeminiBackoffBase
	}
	max := c.BackoffMax
	if max <= 0 {
		max = defaultGeminiBackoffMax
	}
	log := c.Log
	if log == nil {
		log = slog.Default()
	}
	sleep := c.Sleep
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) error {
			select {
			case <-time.After(d):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			d := base << (attempt - 1)
			if d > max {
				d = max
			}
			d += time.Duration(rand.Int63n(int64(d) + 1)) // full jitter
			log.Warn("gemini generate retrying",
				"attempt", attempt, "backoff", d.String(), "error", lastErr)
			if err := sleep(ctx, d); err != nil {
				return "", err
			}
		}
		text, err := c.generateOnce(ctx, model, prompt)
		if err == nil {
			return text, nil
		}
		lastErr = err
		if !geminiTransient(err) || attempt == retries {
			return "", err
		}
	}
	return "", lastErr
}

func (c *GeminiClient) generateOnce(ctx context.Context, model, prompt string) (string, error) {
	if c.Generate != nil {
		return c.Generate(ctx, nil, model, prompt)
	}
	newClient := c.NewClient
	if newClient == nil {
		newClient = genai.NewClient
	}
	client, err := newClient(ctx, &genai.ClientConfig{
		APIKey:  c.APIKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return "", fmt.Errorf("gemini: client: %w", err)
	}
	return generateWithSearch(ctx, client, model, prompt)
}

// geminiTransient reports whether err is worth retrying (overload, rate limit,
// gateway blips). Permanent auth/schema errors return false.
func geminiTransient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var ae genai.APIError
	if errors.As(err, &ae) {
		switch ae.Code {
		case 408, 429, 500, 502, 503, 504:
			return true
		}
		status := strings.ToUpper(ae.Status)
		switch {
		case strings.Contains(status, "UNAVAILABLE"),
			strings.Contains(status, "RESOURCE_EXHAUSTED"),
			strings.Contains(status, "ABORTED"),
			strings.Contains(status, "DEADLINE"):
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "high demand"),
		strings.Contains(msg, "unavailable"),
		strings.Contains(msg, "resource_exhausted"),
		strings.Contains(msg, "empty response"),
		strings.Contains(msg, "connection reset"),
		strings.Contains(msg, "temporary"):
		return true
	case strings.Contains(msg, "error 429"),
		strings.Contains(msg, "error 500"),
		strings.Contains(msg, "error 502"),
		strings.Contains(msg, "error 503"),
		strings.Contains(msg, "error 504"):
		return true
	}
	return false
}

func generateWithSearch(ctx context.Context, client *genai.Client, model, prompt string) (string, error) {
	resp, err := client.Models.GenerateContent(ctx, model, genai.Text(prompt), &genai.GenerateContentConfig{
		Tools: []*genai.Tool{
			{GoogleSearch: &genai.GoogleSearch{}},
		},
		// Prefer JSON-ish output; we still extract a fenced/bare object below.
		Temperature: genai.Ptr(float32(0.1)),
	})
	if err != nil {
		return "", fmt.Errorf("gemini: generate: %w", err)
	}
	text := strings.TrimSpace(resp.Text())
	if text == "" {
		return "", fmt.Errorf("gemini: empty response")
	}
	return text, nil
}

func geminiPrompt(now time.Time, lookaheadDays int) string {
	return fmt.Sprintf(`You are building a trading economic calendar JSON for EU equity indices and USD/JPY.

Using Google Search, list HIGH-IMPACT macroeconomic events from %s UTC through the next %d days.

Include only:
- EU (region "EU"): ECB rate decision / press conference, Eurozone CPI/HICP, German CPI, major GDP when high impact. Treat DE/FR/IT high-impact as EU.
- US (region "US"): NFP, CPI, PPI, PCE, FOMC rate decision / press conference, GDP Advance.
- JP (region "JP"): BOJ rate decision / MPM, Japan CPI, Tankan, major employment/GDP if high impact.

Exclude medium/low impact, speeches (unless rate/press), unrelated countries.

Return ONLY one JSON object (no markdown fences if possible) matching:
{"as_of":"<ISO-8601 Z>","events":[{"region":"EU"|"US"|"JP","title":"...","impact":"high","time":"YYYY-MM-DDTHH:MM:SSZ"}]}

Rules: every impact must be exactly "high"; every time UTC with Z; merge all regions into one events array.`,
		now.UTC().Format(time.RFC3339), lookaheadDays)
}

// parseGeminiJSON extracts a State from model text (bare JSON or fenced block).
func parseGeminiJSON(text string) (State, error) {
	raw := extractJSONObject(text)
	if raw == "" {
		return State{}, fmt.Errorf("gemini: no JSON object in response")
	}
	var st State
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return State{}, fmt.Errorf("gemini: schema parse: %w", err)
	}
	for i := range st.Events {
		st.Events[i].Time = st.Events[i].Time.UTC()
		st.Events[i].Impact = strings.ToLower(strings.TrimSpace(st.Events[i].Impact))
		st.Events[i].Region = strings.ToUpper(strings.TrimSpace(st.Events[i].Region))
	}
	return st, nil
}

func extractJSONObject(text string) string {
	text = strings.TrimSpace(text)
	// Prefer fenced ```json ... ```
	if i := strings.Index(text, "```"); i >= 0 {
		rest := text[i+3:]
		rest = strings.TrimPrefix(rest, "json")
		rest = strings.TrimPrefix(rest, "JSON")
		rest = strings.TrimSpace(rest)
		if j := strings.Index(rest, "```"); j >= 0 {
			rest = rest[:j]
		}
		text = strings.TrimSpace(rest)
	}
	// Take the first brace-balanced object. LastIndex("}") wrongly swallows a
	// second trailing object (Gemini sometimes emits }{...}), which yields
	// "invalid character '{' after top-level value".
	start := strings.Index(text, "{")
	if start < 0 {
		return ""
	}
	depth := 0
	inString := false
	escape := false
	for i := start; i < len(text); i++ {
		c := text[i]
		if inString {
			if escape {
				escape = false
				continue
			}
			if c == '\\' {
				escape = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start : i+1]
			}
		}
	}
	return ""
}
