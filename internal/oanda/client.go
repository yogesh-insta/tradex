package oanda

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Client talks to the OANDA v20 REST API. The host (fxpractice vs fxtrade)
// comes from config — never a boolean flag.
type Client struct {
	restBase   string // https://api-fxpractice.oanda.com
	streamBase string // https://stream-fxpractice.oanda.com
	token      string
	http       *http.Client

	maxRetries  int
	backoffBase time.Duration
}

// Options tunes the client. Zero values get sane defaults.
type Options struct {
	RequestTimeout time.Duration
	MaxRetries     int
	BackoffBase    time.Duration
}

// NewClient builds a client for the given REST + stream hosts.
func NewClient(host, streamHost, token string, opts Options) *Client {
	if opts.RequestTimeout == 0 {
		opts.RequestTimeout = 5 * time.Second
	}
	if opts.MaxRetries == 0 {
		opts.MaxRetries = 3
	}
	if opts.BackoffBase == 0 {
		opts.BackoffBase = 250 * time.Millisecond
	}
	return &Client{
		restBase:    "https://" + host,
		streamBase:  "https://" + streamHost,
		token:       token,
		http:        &http.Client{Timeout: opts.RequestTimeout},
		maxRetries:  opts.MaxRetries,
		backoffBase: opts.BackoffBase,
	}
}

// APIError is a non-2xx OANDA response.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("oanda: HTTP %d %s %s", e.Status, e.Code, e.Message)
}

// Transient reports whether the error is retryable (5xx / 429).
func (e *APIError) Transient() bool { return e.Status >= 500 || e.Status == 429 }

// do executes one request with bounded retries on network errors and
// transient (5xx/429) API errors. Retries are safe for the idempotent v20
// calls we make: order creation carries clientExtensions.id, so a replay
// cannot double-fill.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("oanda: marshal body: %w", err)
		}
	}
	u := c.restBase + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			d := c.backoffBase << (attempt - 1)
			d += time.Duration(rand.Int63n(int64(d) + 1)) // full jitter
			select {
			case <-time.After(d):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		var rdr io.Reader
		if payload != nil {
			rdr = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rdr)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept-Datetime-Format", "RFC3339")

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue // network error: retry
		}
		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if out == nil {
				return nil
			}
			if err := json.Unmarshal(respBody, out); err != nil {
				return fmt.Errorf("oanda: decode %s %s: %w", method, path, err)
			}
			return nil
		}
		apiErr := &APIError{Status: resp.StatusCode}
		var errBody struct {
			ErrorCode    string `json:"errorCode"`
			ErrorMessage string `json:"errorMessage"`
		}
		if json.Unmarshal(respBody, &errBody) == nil {
			apiErr.Code = errBody.ErrorCode
			apiErr.Message = errBody.ErrorMessage
		}
		if !apiErr.Transient() {
			// Decode the reject payload too, when the caller wants it.
			if out != nil {
				_ = json.Unmarshal(respBody, out)
			}
			return apiErr
		}
		lastErr = apiErr
	}
	return fmt.Errorf("oanda: %s %s failed after %d attempts: %w", method, path, c.maxRetries+1, lastErr)
}

// Candles fetches up to count most-recent candles for instrument/granularity
// (mid price). If from/to are non-zero they bound the request instead.
func (c *Client) Candles(ctx context.Context, instrument, granularity string, count int, from, to time.Time) (CandlesResponse, error) {
	q := url.Values{}
	q.Set("granularity", granularity)
	q.Set("price", "M")
	if !from.IsZero() {
		q.Set("from", from.UTC().Format(time.RFC3339))
		if !to.IsZero() {
			q.Set("to", to.UTC().Format(time.RFC3339))
		}
	} else if count > 0 {
		q.Set("count", strconv.Itoa(count))
	}
	var out CandlesResponse
	err := c.do(ctx, http.MethodGet, "/v3/instruments/"+instrument+"/candles", q, nil, &out)
	return out, err
}

// Instruments fetches account instrument metadata, optionally filtered.
func (c *Client) Instruments(ctx context.Context, accountID string, names []string) (InstrumentsResponse, error) {
	q := url.Values{}
	if len(names) > 0 {
		joined := ""
		for i, n := range names {
			if i > 0 {
				joined += ","
			}
			joined += n
		}
		q.Set("instruments", joined)
	}
	var out InstrumentsResponse
	err := c.do(ctx, http.MethodGet, "/v3/accounts/"+accountID+"/instruments", q, nil, &out)
	return out, err
}

// MidPrice returns the current midpoint for an account-tradeable instrument.
func (c *Client) MidPrice(ctx context.Context, accountID, instrument string) (float64, error) {
	q := url.Values{}
	q.Set("instruments", instrument)
	var out PricingResponse
	if err := c.do(ctx, http.MethodGet, "/v3/accounts/"+accountID+"/pricing", q, nil, &out); err != nil {
		return 0, err
	}
	if len(out.Prices) != 1 || len(out.Prices[0].Bids) == 0 || len(out.Prices[0].Asks) == 0 {
		return 0, fmt.Errorf("pricing: no bid/ask for %s", instrument)
	}
	bid, ask := float64(out.Prices[0].Bids[0].Price), float64(out.Prices[0].Asks[0].Price)
	if bid <= 0 || ask <= 0 {
		return 0, fmt.Errorf("pricing: invalid bid/ask for %s", instrument)
	}
	return (bid + ask) / 2, nil
}

// AccountSummary fetches equity/margin state for sizing.
func (c *Client) AccountSummary(ctx context.Context, accountID string) (AccountSummary, error) {
	var out AccountSummaryResponse
	err := c.do(ctx, http.MethodGet, "/v3/accounts/"+accountID+"/summary", nil, nil, &out)
	return out.Account, err
}

// CreateOrder posts a bracketed order (atomic entry + SL/TP).
func (c *Client) CreateOrder(ctx context.Context, accountID string, order MarketOrder) (CreateOrderResponse, error) {
	var out CreateOrderResponse
	err := c.do(ctx, http.MethodPost, "/v3/accounts/"+accountID+"/orders", nil, OrderBody{Order: order}, &out)
	return out, err
}

// OpenTrades lists live trades for the account.
func (c *Client) OpenTrades(ctx context.Context, accountID string) (OpenTradesResponse, error) {
	var out OpenTradesResponse
	err := c.do(ctx, http.MethodGet, "/v3/accounts/"+accountID+"/openTrades", nil, nil, &out)
	return out, err
}

// Trade fetches one trade by id (open or closed — closed trades carry
// realizedPL, used to settle daily P&L after bracket exits).
func (c *Client) Trade(ctx context.Context, accountID, tradeID string) (RESTTrade, error) {
	var out TradeResponse
	err := c.do(ctx, http.MethodGet, "/v3/accounts/"+accountID+"/trades/"+tradeID, nil, nil, &out)
	return out.Trade, err
}

// Transactions returns ORDER_FILL transactions over [from, to]. ORDER_FILL is
// the OANDA transaction that realizes trade P&L on a full or partial close;
// opening fills report zero PL and are harmless to callers that aggregate it.
func (c *Client) Transactions(ctx context.Context, accountID string, from, to time.Time) (TransactionsResponse, error) {
	q := url.Values{}
	q.Set("from", from.UTC().Format(time.RFC3339))
	q.Set("to", to.UTC().Format(time.RFC3339))
	q.Set("type", "ORDER_FILL")
	q.Set("pageSize", "1000")
	var out TransactionsResponse
	err := c.do(ctx, http.MethodGet, "/v3/accounts/"+accountID+"/transactions", q, nil, &out)
	return out, err
}

// SetTradeOrders replaces a trade's dependent SL/TP orders.
func (c *Client) SetTradeOrders(ctx context.Context, accountID, tradeID string, body TradeOrdersBody) error {
	return c.do(ctx, http.MethodPut, "/v3/accounts/"+accountID+"/trades/"+tradeID+"/orders", nil, body, nil)
}

// SetTradeClientExtensions persists broker-visible metadata for a live trade.
func (c *Client) SetTradeClientExtensions(ctx context.Context, accountID, tradeID string, extensions ClientExtensions) error {
	return c.do(ctx, http.MethodPut, "/v3/accounts/"+accountID+"/trades/"+tradeID+"/clientExtensions", nil, TradeClientExtensionsBody{
		ClientExtensions: extensions,
	}, nil)
}

// CloseTrade market-closes a trade in full.
func (c *Client) CloseTrade(ctx context.Context, accountID, tradeID string) (CloseTradeResponse, error) {
	var out CloseTradeResponse
	err := c.do(ctx, http.MethodPut, "/v3/accounts/"+accountID+"/trades/"+tradeID+"/close", nil, CloseTradeBody{Units: "ALL"}, &out)
	return out, err
}

// PendingOrders lists resting orders (US path, future).
func (c *Client) PendingOrders(ctx context.Context, accountID string) (PendingOrdersResponse, error) {
	var out PendingOrdersResponse
	err := c.do(ctx, http.MethodGet, "/v3/accounts/"+accountID+"/pendingOrders", nil, nil, &out)
	return out, err
}

// CancelOrder cancels a pending order by OANDA order id.
func (c *Client) CancelOrder(ctx context.Context, accountID, orderID string) error {
	return c.do(ctx, http.MethodPut, "/v3/accounts/"+accountID+"/orders/"+orderID+"/cancel", nil, nil, nil)
}
