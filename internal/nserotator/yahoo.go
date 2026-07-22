package nserotator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
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

	mu      sync.Mutex
	session *http.Client
	crumb   string
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
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == nil {
		jar, _ := cookiejar.New(nil)
		c.session = &http.Client{Jar: jar}
	}
	return c.session
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

// QuoteDetail is live quote metadata for one NSE symbol.
type QuoteDetail struct {
	CompanyName string
	Price       float64
	MarketCap   float64
}

const quoteBatchSize = 50

type sparkResponse struct {
	Spark struct {
		Result []struct {
			Symbol   string `json:"symbol"`
			Response []struct {
				Meta struct {
					Symbol             string  `json:"symbol"`
					LongName           string  `json:"longName"`
					ShortName          string  `json:"shortName"`
					RegularMarketPrice float64 `json:"regularMarketPrice"`
				} `json:"meta"`
			} `json:"response"`
		} `json:"result"`
		Error *struct {
			Code        string `json:"code"`
			Description string `json:"description"`
		} `json:"error"`
	} `json:"spark"`
}

type summaryResponse struct {
	QuoteSummary struct {
		Result []struct {
			SummaryDetail struct {
				MarketCap struct {
					Raw float64 `json:"raw"`
				} `json:"marketCap"`
			} `json:"summaryDetail"`
		} `json:"result"`
		Error *struct {
			Code        string `json:"code"`
			Description string `json:"description"`
		} `json:"error"`
	} `json:"quoteSummary"`
}

// FetchQuoteDetails returns company name, live price, and market cap for symbols.
// Spark API supplies name/price; quoteSummary (with session crumb) supplies market cap.
func (c *YahooClient) FetchQuoteDetails(ctx context.Context, symbols []string) map[string]QuoteDetail {
	out := make(map[string]QuoteDetail, len(symbols))
	unique := dedupeSymbols(symbols)
	if len(unique) == 0 {
		return out
	}
	for i := 0; i < len(unique); i += quoteBatchSize {
		end := i + quoteBatchSize
		if end > len(unique) {
			end = len(unique)
		}
		batch, err := c.fetchSparkBatch(ctx, unique[i:end])
		if err != nil {
			continue
		}
		for sym, q := range batch {
			out[sym] = q
		}
	}
	c.attachMarketCaps(ctx, out, unique)
	return out
}

func dedupeSymbols(symbols []string) []string {
	seen := make(map[string]bool, len(symbols))
	unique := make([]string, 0, len(symbols))
	for _, sym := range symbols {
		if sym == "" || seen[sym] {
			continue
		}
		seen[sym] = true
		unique = append(unique, sym)
	}
	return unique
}

func (c *YahooClient) toYahooSymbol(sym string) string {
	if len(sym) > 0 && sym[0] == '^' {
		return sym
	}
	return sym + ".NS"
}

func (c *YahooClient) fetchSparkBatch(ctx context.Context, symbols []string) (map[string]QuoteDetail, error) {
	yahooSyms := make([]string, len(symbols))
	for i, sym := range symbols {
		yahooSyms[i] = c.toYahooSymbol(sym)
	}
	u := fmt.Sprintf("%s/v7/finance/spark?symbols=%s&range=1d&interval=1d",
		c.base(), url.QueryEscape(strings.Join(yahooSyms, ",")))

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
		out, err := c.fetchSparkOnce(ctx, u, symbols, yahooSyms)
		if err == nil {
			return out, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (c *YahooClient) fetchSparkOnce(ctx context.Context, u string, symbols, yahooSyms []string) (map[string]QuoteDetail, error) {
	body, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}
	var sr sparkResponse
	if err := json.Unmarshal(body, &sr); err != nil {
		return nil, fmt.Errorf("parse spark: %w", err)
	}
	if sr.Spark.Error != nil {
		return nil, fmt.Errorf("%s: %s", sr.Spark.Error.Code, sr.Spark.Error.Description)
	}
	yahooToSym := make(map[string]string, len(symbols))
	for i, sym := range symbols {
		yahooToSym[strings.ToUpper(yahooSyms[i])] = sym
	}
	out := make(map[string]QuoteDetail, len(sr.Spark.Result))
	for _, r := range sr.Spark.Result {
		sym, ok := yahooToSym[strings.ToUpper(r.Symbol)]
		if !ok || len(r.Response) == 0 {
			continue
		}
		meta := r.Response[0].Meta
		name := meta.LongName
		if name == "" {
			name = meta.ShortName
		}
		out[sym] = QuoteDetail{
			CompanyName: name,
			Price:       meta.RegularMarketPrice,
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no spark quotes in response")
	}
	return out, nil
}

func (c *YahooClient) attachMarketCaps(ctx context.Context, out map[string]QuoteDetail, symbols []string) {
	if err := c.ensureCrumb(ctx); err != nil {
		return
	}
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, sym := range symbols {
		wg.Add(1)
		sem <- struct{}{}
		go func(sym string) {
			defer wg.Done()
			defer func() { <-sem }()
			cap, err := c.fetchMarketCap(ctx, sym)
			if err != nil || cap <= 0 {
				return
			}
			q := out[sym]
			q.MarketCap = cap
			out[sym] = q
		}(sym)
	}
	wg.Wait()
}

func (c *YahooClient) ensureCrumb(ctx context.Context) error {
	c.mu.Lock()
	if c.crumb != "" {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	if c.base() == "https://query1.finance.yahoo.com" {
		bootstrap := []string{
			"https://fc.yahoo.com/",
			"https://finance.yahoo.com/",
		}
		for _, u := range bootstrap {
			if _, err := c.get(ctx, u); err != nil {
				return err
			}
		}
	}
	body, err := c.get(ctx, c.base()+"/v1/test/getcrumb")
	if err != nil {
		return err
	}
	crumb := strings.TrimSpace(string(body))
	if crumb == "" || strings.HasPrefix(crumb, "{") {
		return fmt.Errorf("invalid crumb")
	}
	c.mu.Lock()
	c.crumb = crumb
	c.mu.Unlock()
	return nil
}

func (c *YahooClient) fetchMarketCap(ctx context.Context, symbol string) (float64, error) {
	c.mu.Lock()
	crumb := c.crumb
	c.mu.Unlock()
	if crumb == "" {
		return 0, fmt.Errorf("missing crumb")
	}
	ySym := c.toYahooSymbol(symbol)
	u := fmt.Sprintf("%s/v10/finance/quoteSummary/%s?modules=summaryDetail&crumb=%s",
		c.base(), url.PathEscape(ySym), url.QueryEscape(crumb))

	attempts := c.Retries
	if attempts <= 0 {
		attempts = 3
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(time.Duration(1<<i) * time.Second):
			}
		}
		body, err := c.get(ctx, u)
		if err != nil {
			lastErr = err
			continue
		}
		var sr summaryResponse
		if err := json.Unmarshal(body, &sr); err != nil {
			lastErr = err
			continue
		}
		if sr.QuoteSummary.Error != nil {
			lastErr = fmt.Errorf("%s: %s", sr.QuoteSummary.Error.Code, sr.QuoteSummary.Error.Description)
			continue
		}
		if len(sr.QuoteSummary.Result) == 0 {
			lastErr = fmt.Errorf("empty quoteSummary")
			continue
		}
		cap := sr.QuoteSummary.Result[0].SummaryDetail.MarketCap.Raw
		if cap <= 0 {
			lastErr = fmt.Errorf("missing market cap")
			continue
		}
		return cap, nil
	}
	return 0, lastErr
}

func (c *YahooClient) get(ctx context.Context, u string) ([]byte, error) {
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
	return body, nil
}
