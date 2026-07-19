package calendar

import (
	"context"
	"encoding/json"
	"fmt"
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
	// NewClient allows tests to inject a stub; production uses genai.NewClient.
	NewClient func(ctx context.Context, cfg *genai.ClientConfig) (*genai.Client, error)
	// Generate overrides the SDK call for unit tests.
	Generate func(ctx context.Context, client *genai.Client, model string, prompt string) (string, error)
}

const defaultGeminiModel = "gemini-2.5-flash-lite"

// FetchState asks Gemini (with Google Search) for the next lookaheadDays of
// high-impact EU/US/JP events and returns a parsed State. as_of is stamped
// by the caller after success.
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

	var text string
	var err error
	if c.Generate != nil {
		text, err = c.Generate(ctx, nil, model, prompt)
	} else {
		newClient := c.NewClient
		if newClient == nil {
			newClient = genai.NewClient
		}
		client, cerr := newClient(ctx, &genai.ClientConfig{
			APIKey:  c.APIKey,
			Backend: genai.BackendGeminiAPI,
		})
		if cerr != nil {
			return State{}, fmt.Errorf("gemini: client: %w", cerr)
		}
		text, err = generateWithSearch(ctx, client, model, prompt)
	}
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
