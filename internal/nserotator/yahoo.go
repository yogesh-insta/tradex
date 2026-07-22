package nserotator

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

// Candle is one daily close (adjusted when Yahoo provides adjclose).
type Candle struct {
	Date  time.Time
	Close float64
}

// Series is a symbol's daily close history, ascending by date.
type Series struct {
	Symbol  string
	Candles []Candle
}

// YahooClient fetches daily history from the Yahoo Finance v8 chart API.
// No API key. BaseURL overridable for tests.
type YahooClient struct {
	HTTPClient *http.Client
	BaseURL    string        // default https://query1.finance.yahoo.com
	Timeout    time.Duration // per attempt
	Retries    int           // total attempts = Retries (default 3)
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

// FetchDaily returns up to rangeYears of daily candles for an NSE symbol
// (".NS" appended; "^NSEI"-style index symbols passed through).
func (c *YahooClient) FetchDaily(ctx context.Context, symbol string, rangeYears int) (Series, error) {
	ySym := symbol
	if len(symbol) > 0 && symbol[0] != '^' {
		ySym = symbol + ".NS"
	}
	u := fmt.Sprintf("%s/v8/finance/chart/%s?range=%dy&interval=1d&events=div%%2Csplit",
		c.base(), url.PathEscape(ySym), rangeYears)

	attempts := c.Retries
	if attempts <= 0 {
		attempts = 3
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return Series{}, ctx.Err()
			case <-time.After(time.Duration(1<<i) * time.Second):
			}
		}
		s, err := c.fetchOnce(ctx, u, symbol)
		if err == nil {
			return s, nil
		}
		lastErr = err
	}
	return Series{}, fmt.Errorf("yahoo %s: %w", symbol, lastErr)
}

func (c *YahooClient) fetchOnce(ctx context.Context, u, symbol string) (Series, error) {
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
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) tradex-nserotator/1.0")
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
	// Prefer adjusted closes (splits/dividends) when present.
	var closes []*float64
	if len(r.Indicators.Adjclose) > 0 && len(r.Indicators.Adjclose[0].Adjclose) == len(r.Timestamp) {
		closes = r.Indicators.Adjclose[0].Adjclose
	} else if len(r.Indicators.Quote) > 0 {
		closes = r.Indicators.Quote[0].Close
	}
	if len(closes) != len(r.Timestamp) {
		return Series{}, fmt.Errorf("close/timestamp length mismatch")
	}
	s := Series{Symbol: symbol}
	for i, ts := range r.Timestamp {
		if closes[i] == nil || *closes[i] <= 0 {
			continue // Yahoo emits nulls for suspended days
		}
		s.Candles = append(s.Candles, Candle{
			Date:  time.Unix(ts, 0).UTC(),
			Close: *closes[i],
		})
	}
	if len(s.Candles) == 0 {
		return Series{}, fmt.Errorf("no valid closes")
	}
	return s, nil
}

type quoteResponse struct {
	QuoteResponse struct {
		Result []struct {
			Symbol    string   `json:"symbol"`
			MarketCap *float64 `json:"marketCap"`
		} `json:"result"`
		Error *struct {
			Code        string `json:"code"`
			Description string `json:"description"`
		} `json:"error"`
	} `json:"quoteResponse"`
}

const quoteBatchSize = 50

// FetchMarketCaps returns latest market cap (INR for NSE .NS symbols) from
// Yahoo's v7 quote API. Missing or failed symbols are omitted from the map.
func (c *YahooClient) FetchMarketCaps(ctx context.Context, symbols []string) map[string]float64 {
	out := make(map[string]float64, len(symbols))
	if len(symbols) == 0 {
		return out
	}
	// Dedupe while preserving order.
	seen := make(map[string]bool, len(symbols))
	unique := make([]string, 0, len(symbols))
	for _, sym := range symbols {
		if sym == "" || seen[sym] {
			continue
		}
		seen[sym] = true
		unique = append(unique, sym)
	}
	for i := 0; i < len(unique); i += quoteBatchSize {
		end := i + quoteBatchSize
		if end > len(unique) {
			end = len(unique)
		}
		batch := unique[i:end]
		mcaps, err := c.fetchMarketCapBatch(ctx, batch)
		if err != nil {
			continue
		}
		for sym, cap := range mcaps {
			out[sym] = cap
		}
	}
	return out
}

func (c *YahooClient) fetchMarketCapBatch(ctx context.Context, symbols []string) (map[string]float64, error) {
	yahooSyms := make([]string, len(symbols))
	for i, sym := range symbols {
		if len(sym) > 0 && sym[0] == '^' {
			yahooSyms[i] = sym
		} else {
			yahooSyms[i] = sym + ".NS"
		}
	}
	u := fmt.Sprintf("%s/v7/finance/quote?symbols=%s", c.base(), url.QueryEscape(strings.Join(yahooSyms, ",")))

	attempts := c.Retries
	if attempts <= 0 {
		attempts = 3
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(1<<i) * time.Second):
			}
		}
		mcaps, err := c.fetchMarketCapOnce(ctx, u, symbols, yahooSyms)
		if err == nil {
			return mcaps, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (c *YahooClient) fetchMarketCapOnce(ctx context.Context, u string, symbols, yahooSyms []string) (map[string]float64, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(rctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) tradex-nserotator/1.0")
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var qr quoteResponse
	if err := json.Unmarshal(body, &qr); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if qr.QuoteResponse.Error != nil {
		return nil, fmt.Errorf("%s: %s", qr.QuoteResponse.Error.Code, qr.QuoteResponse.Error.Description)
	}
	yahooToSym := make(map[string]string, len(symbols))
	for i, sym := range symbols {
		yahooToSym[strings.ToUpper(yahooSyms[i])] = sym
	}
	out := make(map[string]float64, len(qr.QuoteResponse.Result))
	for _, r := range qr.QuoteResponse.Result {
		sym, ok := yahooToSym[strings.ToUpper(r.Symbol)]
		if !ok || r.MarketCap == nil || *r.MarketCap <= 0 {
			continue
		}
		out[sym] = *r.MarketCap
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no market caps in response")
	}
	return out, nil
}
