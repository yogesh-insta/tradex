// Package oanda implements the OANDA v20 REST + pricing-stream client used by
// market data, the candle builder, and the executor. It follows the documented
// v20 JSON structures (numbers arrive as strings and are decoded via Num).
package oanda

import (
	"bytes"
	"strconv"
	"time"
)

// Num decodes OANDA's string-encoded decimal numbers ("18450.0") as float64.
type Num float64

// UnmarshalJSON accepts both quoted and bare numbers.
func (n *Num) UnmarshalJSON(b []byte) error {
	b = bytes.Trim(b, `"`)
	if len(b) == 0 || string(b) == "null" {
		*n = 0
		return nil
	}
	f, err := strconv.ParseFloat(string(b), 64)
	if err != nil {
		return err
	}
	*n = Num(f)
	return nil
}

// --- Candles: GET /v3/instruments/{instrument}/candles ---

// CandlesResponse is the REST candles payload.
type CandlesResponse struct {
	Instrument  string       `json:"instrument"`
	Granularity string       `json:"granularity"`
	Candles     []RESTCandle `json:"candles"`
}

// RESTCandle is one candlestick with mid prices.
type RESTCandle struct {
	Complete bool      `json:"complete"`
	Volume   int64     `json:"volume"`
	Time     time.Time `json:"time"`
	Mid      OHLC      `json:"mid"`
}

// OHLC holds string-encoded open/high/low/close.
type OHLC struct {
	O Num `json:"o"`
	H Num `json:"h"`
	L Num `json:"l"`
	C Num `json:"c"`
}

// --- Pricing stream: GET {streamHost}/v3/accounts/{id}/pricing/stream ---

// StreamMessage is one newline-delimited stream object: type PRICE or HEARTBEAT.
type StreamMessage struct {
	Type       string       `json:"type"` // "PRICE" | "HEARTBEAT"
	Time       time.Time    `json:"time"`
	Instrument string       `json:"instrument"`
	Bids       []PriceLevel `json:"bids"`
	Asks       []PriceLevel `json:"asks"`
	Status     string       `json:"status"`    // deprecated but still sent: "tradeable" | "non-tradeable"
	Tradeable  bool         `json:"tradeable"` // authoritative flag
}

// PriceLevel is one bid/ask ladder entry.
type PriceLevel struct {
	Price     Num   `json:"price"`
	Liquidity int64 `json:"liquidity"`
}

// --- Account instruments: GET /v3/accounts/{id}/instruments ---

// InstrumentsResponse lists the tradeable instruments for an account.
type InstrumentsResponse struct {
	Instruments []RESTInstrument `json:"instruments"`
}

// PricingResponse contains the current bid/ask ladders requested for an account.
type PricingResponse struct {
	Prices []StreamMessage `json:"prices"`
}

// RESTInstrument is OANDA's instrument metadata.
type RESTInstrument struct {
	Name                        string `json:"name"`
	Type                        string `json:"type"`
	DisplayName                 string `json:"displayName"`
	PipLocation                 int    `json:"pipLocation"`
	DisplayPrecision            int    `json:"displayPrecision"`
	TradeUnitsPrecision         int    `json:"tradeUnitsPrecision"`
	MinimumTradeSize            Num    `json:"minimumTradeSize"`
	MarginRate                  Num    `json:"marginRate"`
	MinimumTrailingStopDistance Num    `json:"minimumTrailingStopDistance"`
}

// --- Account summary: GET /v3/accounts/{id}/summary ---

// AccountSummaryResponse wraps the account summary.
type AccountSummaryResponse struct {
	Account AccountSummary `json:"account"`
}

// AccountSummary carries the fields sizing needs.
type AccountSummary struct {
	ID              string `json:"id"`
	Currency        string `json:"currency"`
	Balance         Num    `json:"balance"`
	NAV             Num    `json:"NAV"`
	MarginUsed      Num    `json:"marginUsed"`
	MarginAvailable Num    `json:"marginAvailable"`
	UnrealizedPL    Num    `json:"unrealizedPL"`
	PL              Num    `json:"pl"`           // lifetime realized PL
	ResettablePL    Num    `json:"resettablePL"` // realized since last reset (often ≈ today)
	OpenTradeCount  int    `json:"openTradeCount"`
}

// --- Orders: POST /v3/accounts/{id}/orders ---

// OrderBody is the request wrapper.
type OrderBody struct {
	Order MarketOrder `json:"order"`
}

// MarketOrder covers MARKET and LIMIT entries with bracket on-fill details.
type MarketOrder struct {
	Type                   string            `json:"type"` // "MARKET" | "LIMIT"
	Instrument             string            `json:"instrument"`
	Units                  string            `json:"units"`           // signed decimal string
	Price                  string            `json:"price,omitempty"` // LIMIT only
	TimeInForce            string            `json:"timeInForce"`
	GTDTime                string            `json:"gtdTime,omitempty"`
	PositionFill           string            `json:"positionFill"`
	TakeProfitOnFill       *OnFillPrice      `json:"takeProfitOnFill,omitempty"`
	StopLossOnFill         *OnFillPrice      `json:"stopLossOnFill,omitempty"`
	TrailingStopLossOnFill *OnFillDistance   `json:"trailingStopLossOnFill,omitempty"`
	ClientExtensions       *ClientExtensions `json:"clientExtensions,omitempty"`
}

// OnFillPrice is an absolute-price bracket leg.
type OnFillPrice struct {
	Price       string `json:"price"`
	TimeInForce string `json:"timeInForce,omitempty"`
}

// OnFillDistance is a distance-based bracket leg (trailing stop).
type OnFillDistance struct {
	Distance    string `json:"distance"`
	TimeInForce string `json:"timeInForce,omitempty"`
}

// ClientExtensions carries the idempotency key.
type ClientExtensions struct {
	ID      string `json:"id,omitempty"`
	Tag     string `json:"tag,omitempty"`
	Comment string `json:"comment,omitempty"`
}

// TradeClientExtensionsBody updates metadata attached to an existing trade.
// The OANDA endpoint expects the extension object under clientExtensions.
type TradeClientExtensionsBody struct {
	ClientExtensions ClientExtensions `json:"clientExtensions"`
}

// CreateOrderResponse is the POST /orders result.
type CreateOrderResponse struct {
	OrderCreateTransaction *Transaction `json:"orderCreateTransaction"`
	OrderFillTransaction   *Transaction `json:"orderFillTransaction"`
	OrderCancelTransaction *Transaction `json:"orderCancelTransaction"`
	OrderRejectTransaction *Transaction `json:"orderRejectTransaction"`
	LastTransactionID      string       `json:"lastTransactionID"`
	ErrorMessage           string       `json:"errorMessage"`
	ErrorCode              string       `json:"errorCode"`
}

// Transaction is the subset of v20 transaction fields we consume.
type Transaction struct {
	ID           string       `json:"id"`
	Time         time.Time    `json:"time"`
	Type         string       `json:"type"`
	Instrument   string       `json:"instrument"`
	Units        Num          `json:"units"`
	Price        Num          `json:"price"`
	PL           Num          `json:"pl"`
	Reason       string       `json:"reason"`
	RejectReason string       `json:"rejectReason"`
	TradeOpened  *TradeOpened `json:"tradeOpened"`
}

// TransactionsResponse lists account transactions returned by OANDA's
// transaction-history endpoint.
type TransactionsResponse struct {
	Transactions      []Transaction `json:"transactions"`
	LastTransactionID string        `json:"lastTransactionID"`
}

// TradeOpened links a fill to the created trade.
type TradeOpened struct {
	TradeID string `json:"tradeID"`
	Units   Num    `json:"units"`
	Price   Num    `json:"price"`
}

// --- Trades: GET /v3/accounts/{id}/openTrades, /trades/{id} ---

// OpenTradesResponse lists live trades.
type OpenTradesResponse struct {
	Trades []RESTTrade `json:"trades"`
}

// TradeResponse wraps a single trade lookup.
type TradeResponse struct {
	Trade RESTTrade `json:"trade"`
}

// RESTTrade is OANDA's trade view.
type RESTTrade struct {
	ID                string            `json:"id"`
	Instrument        string            `json:"instrument"`
	Price             Num               `json:"price"` // average entry
	OpenTime          time.Time         `json:"openTime"`
	State             string            `json:"state"` // OPEN | CLOSED
	InitialUnits      Num               `json:"initialUnits"`
	CurrentUnits      Num               `json:"currentUnits"`
	RealizedPL        Num               `json:"realizedPL"`
	UnrealizedPL      Num               `json:"unrealizedPL"`
	AverageClosePrice Num               `json:"averageClosePrice"`
	ClientExtensions  *ClientExtensions `json:"clientExtensions"`
	StopLossOrder     *DependentOrder   `json:"stopLossOrder"`
	TakeProfitOrder   *DependentOrder   `json:"takeProfitOrder"`
}

// DependentOrder is an attached SL/TP order.
type DependentOrder struct {
	ID    string `json:"id"`
	Price Num    `json:"price"`
}

// --- Trade modify/close ---

// TradeOrdersBody replaces a trade's dependent orders
// (PUT /v3/accounts/{id}/trades/{tradeID}/orders).
type TradeOrdersBody struct {
	StopLoss   *OnFillPrice `json:"stopLoss,omitempty"`
	TakeProfit *OnFillPrice `json:"takeProfit,omitempty"`
}

// CloseTradeBody requests a (full) close.
type CloseTradeBody struct {
	Units string `json:"units"` // "ALL"
}

// CloseTradeResponse carries the closing fill.
type CloseTradeResponse struct {
	OrderFillTransaction *Transaction `json:"orderFillTransaction"`
}

// --- Pending orders ---

// PendingOrdersResponse lists resting orders (US path, future).
type PendingOrdersResponse struct {
	Orders []RESTOrder `json:"orders"`
}

// RESTOrder is a pending order summary.
type RESTOrder struct {
	ID               string            `json:"id"`
	Type             string            `json:"type"`
	Instrument       string            `json:"instrument"`
	Units            Num               `json:"units"`
	Price            Num               `json:"price"`
	State            string            `json:"state"`
	ClientExtensions *ClientExtensions `json:"clientExtensions"`
}
