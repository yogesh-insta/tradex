package etfmonitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// YahooClient fetches daily history from the Yahoo Finance v8 chart API.
// No API key. BaseURL overridable for tests. Same shape as the NSE rotator's
// client; the retry loop is factored into one helper here rather than repeated
// per call site.
type YahooClient struct {
	HTTPClient *http.Client
	BaseURL    string        // default https://query1.finance.yahoo.com
	Timeout    time.Duration // per attempt
	Retries    int           // total attempts (default 3)
}

func (c *YahooClient) base() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return "https://query1.finance.yahoo.com"
}

func (c *YahooClient) http() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *YahooClient) attempts() int {
	if c.Retries > 0 {
		return c.Retries
	}
	return 3
}

type chartResponse struct {
	Chart struct {
		Result []struct {
			Timestamp  []int64 `json:"timestamp"`
			Indicators struct {
				Quote []struct {
					Close []*float64 `json:"close"`
				} `json:"quote"`
				Adjclose []struct {
					Adjclose []*float64 `json:"adjclose"`
				} `json:"adjclose"`
			} `json:"indicators"`
		} `json:"result"`
		Error *struct {
			Code        string `json:"code"`
			Description string `json:"description"`
		} `json:"error"`
	} `json:"chart"`
}

// ToYahooSymbol maps an ASX ticker to its Yahoo symbol.
func ToYahooSymbol(ticker string) string { return ticker + ".AX" }

// FetchDaily returns up to rangeYears of daily candles for an ASX ticker.
func (c *YahooClient) FetchDaily(ctx context.Context, ticker string, rangeYears int) (Series, error) {
	u := fmt.Sprintf("%s/v8/finance/chart/%s?range=%dy&interval=1d&events=div%%2Csplit",
		c.base(), url.PathEscape(ToYahooSymbol(ticker)), rangeYears)

	var s Series
	err := retry(ctx, c.attempts(), func() error {
		var err error
		s, err = c.fetchOnce(ctx, u, ticker)
		return err
	})
	if err != nil {
		return Series{}, fmt.Errorf("yahoo %s: %w", ticker, err)
	}
	return s, nil
}

// retry runs fn up to attempts times with 2^i-second backoff, honouring ctx
// cancellation between attempts (a cancelled context aborts immediately rather
// than sleeping out the remaining retries).
func retry(ctx context.Context, attempts int, fn func() error) error {
	var last error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(1<<i) * time.Second):
			}
		}
		if err := fn(); err == nil {
			return nil
		} else {
			last = err
		}
	}
	return last
}

func (c *YahooClient) fetchOnce(ctx context.Context, u, ticker string) (Series, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(rctx, http.MethodGet, u, nil)
	if err != nil {
		return Series{}, err
	}
	// Yahoo rejects requests without a browser-ish UA.
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) tradex-etfmonitor/1.0")
	resp, err := c.http().Do(req)
	if err != nil {
		return Series{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Series{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Series{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var cr chartResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		return Series{}, fmt.Errorf("parse: %w", err)
	}
	if cr.Chart.Error != nil {
		return Series{}, fmt.Errorf("%s: %s", cr.Chart.Error.Code, cr.Chart.Error.Description)
	}
	if len(cr.Chart.Result) == 0 || len(cr.Chart.Result[0].Timestamp) == 0 {
		return Series{}, fmt.Errorf("empty result")
	}
	r := cr.Chart.Result[0]
	// Prefer adjusted closes (splits/dividends), matching screen.py's
	// auto_adjust=True. The split guard exists because this is not always
	// applied correctly by the feed.
	var closes []*float64
	if len(r.Indicators.Adjclose) > 0 && len(r.Indicators.Adjclose[0].Adjclose) == len(r.Timestamp) {
		closes = r.Indicators.Adjclose[0].Adjclose
	} else if len(r.Indicators.Quote) > 0 {
		closes = r.Indicators.Quote[0].Close
	}
	if len(closes) != len(r.Timestamp) {
		return Series{}, fmt.Errorf("close/timestamp length mismatch")
	}
	s := Series{Ticker: ticker}
	for i, ts := range r.Timestamp {
		if closes[i] == nil || *closes[i] <= 0 {
			continue // Yahoo emits nulls for suspended sessions
		}
		s.Candles = append(s.Candles, Candle{Date: time.Unix(ts, 0).UTC(), Close: *closes[i]})
	}
	if len(s.Candles) == 0 {
		return Series{}, fmt.Errorf("no valid closes")
	}
	return s, nil
}
