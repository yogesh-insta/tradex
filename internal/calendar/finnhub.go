package calendar

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const finnhubEconomicURL = "https://finnhub.io/api/v1/calendar/economic"

// FinnhubClient fetches the Finnhub economic calendar (primary path).
type FinnhubClient struct {
	APIKey     string
	HTTPClient *http.Client
	BaseURL    string // override for tests
}

func (c *FinnhubClient) http() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *FinnhubClient) base() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return finnhubEconomicURL
}

type finnhubResponse struct {
	EconomicCalendar []finnhubEvent `json:"economicCalendar"`
}

type finnhubEvent struct {
	Country string `json:"country"`
	Event   string `json:"event"`
	Impact  string `json:"impact"`
	Time    string `json:"time"`
}

// FetchRaw loads Finnhub events for [now, now+lookaheadDays] in UTC.
// Hard failures (HTTP error, 403, parse error) return err so the poller
// can fall back to Gemini. A successful empty calendar is not an error —
// the caller treats 0 filtered matches as fallback.
func (c *FinnhubClient) FetchRaw(ctx context.Context, now time.Time, lookaheadDays int) ([]RawEvent, error) {
	if c.APIKey == "" {
		return nil, fmt.Errorf("finnhub: API key empty")
	}
	if lookaheadDays <= 0 {
		lookaheadDays = 7
	}
	from := now.UTC().Format("2006-01-02")
	to := now.UTC().AddDate(0, 0, lookaheadDays).Format("2006-01-02")

	u, err := url.Parse(c.base())
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("from", from)
	q.Set("to", to)
	q.Set("token", c.APIKey)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("finnhub: request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("finnhub: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("finnhub: HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var parsed finnhubResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("finnhub: parse: %w", err)
	}
	out := make([]RawEvent, 0, len(parsed.EconomicCalendar))
	for _, ev := range parsed.EconomicCalendar {
		t, err := parseFinnhubTime(ev.Time)
		if err != nil {
			continue
		}
		out = append(out, RawEvent{
			Country: ev.Country,
			Title:   ev.Event,
			Impact:  ev.Impact,
			Time:    t,
		})
	}
	return out, nil
}

// parseFinnhubTime accepts "2006-01-02 15:04:05" (UTC) and RFC3339.
func parseFinnhubTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.UTC); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04", s, time.UTC); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("unrecognized time %q", s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
