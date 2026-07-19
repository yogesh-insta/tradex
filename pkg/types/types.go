// Package types holds the canonical data contracts shared across all tradex
// components, per docs/specs/01-data-contracts.md. Types are the only coupling
// between packages; components depend on these shapes, not on each other's
// internals.
package types

import "time"

// Timeframe identifies a candle granularity buffer.
type Timeframe string

const (
	M5 Timeframe = "M5"
	H1 Timeframe = "H1"
	D  Timeframe = "D" // daily, used for ATR context
)

// Duration returns the wall-clock span of one candle of this timeframe.
func (tf Timeframe) Duration() time.Duration {
	switch tf {
	case M5:
		return 5 * time.Minute
	case H1:
		return time.Hour
	case D:
		return 24 * time.Hour
	}
	return 0
}

// Tick is one OANDA pricing update (mid derived by us).
type Tick struct {
	Instrument string
	Time       time.Time // UTC
	Bid, Ask   float64
	Mid        float64 // (bid+ask)/2
	Spread     float64 // ask-bid
	Volume     int64   // tick volume (running count within the forming candle)
	Tradeable  bool    // OANDA status == "tradeable"
}

// Candle is a closed or forming OHLCV bar for one instrument+timeframe.
type Candle struct {
	Instrument string
	Timeframe  Timeframe
	Start      time.Time // candle open time, UTC
	Open       float64
	High       float64
	Low        float64
	Close      float64
	Volume     int64
	Complete   bool // true once the candle is closed/confirmed
	// Unconfirmed marks a locally-built candle emitted while the REST
	// confirmation was unavailable (03-candle-builder.md).
	Unconfirmed bool
}

// MarketEvent is handed to the strategy router when a candle closes.
type MarketEvent struct {
	Instrument string
	Now        time.Time // instrument market tz (IANA)
	Timeframe  Timeframe // which buffer closed (v1 decisions trigger on M5)
	Last       Candle    // the candle that just closed
	Window     []Candle  // sliding window for this timeframe, newest last (<=50)
	Price      float64   // latest mid from the snapshot
	Spread     float64   // latest ask-bid from the snapshot (0 = unknown)
}

// SessionState is per-day computed context, RAM only, read-only to strategies.
// Only EU fields are populated in v1; others reserved for future markets.
type SessionState struct {
	Instrument string

	// EU LOVE
	OpeningHigh float64 // 08:00–09:00 CET range (locked at 09:00)
	OpeningLow  float64
	RangeLocked bool    // true once the range window has closed
	DailyATR    float64 // 14-day ATR (daily candles), computed pre-session
	VWAP        float64 // running session VWAP
	VolMA12     float64 // avg volume of preceding 12 M5 candles

	// Reserved (US / Asia)
	InitialHigh float64
	InitialLow  float64
	RSI14       float64
	GapPct      float64

	AsOf time.Time
}

// ManagementPolicy describes how the shared loop manages THIS trade after entry.
type ManagementPolicy struct {
	BreakevenAtR float64   // move SL→entry once profit >= this x risk (EU: 1.0)
	TimeCutoff   time.Time // unconditional flatten (instrument tz); zero = none
	Trail        string    // optional broker trailing rule id ("" = none)
}

// Trade direction values.
const (
	DirectionLong  = "LONG"
	DirectionShort = "SHORT"
)

// Order types.
const (
	OrderTypeMarket = "MARKET"
	OrderTypeLimit  = "LIMIT"
)

// Signal is the strategy output: price intent + policy. NOT sized. nil = no setup.
type Signal struct {
	Instrument string
	Strategy   string  // "eu_love" | "fx_trld"
	Direction  string  // "LONG" | "SHORT"
	OrderType  string  // "MARKET" (EU) | "LIMIT" (US, future)
	EntryPrice float64 // for LIMIT; reference for MARKET
	StopLoss   float64
	TakeProfit float64
	Policy     ManagementPolicy
	Reason     string // audit: which conditions fired
	At         time.Time
}

// OrderRequest is the risk output: concrete, sized, idempotent. Rejected if an
// open position for Instrument already exists, or a correlated instrument is open.
type OrderRequest struct {
	Instrument string
	// Units is signed (+long / -short). Fractional units are allowed —
	// OANDA CFDs trade in fractional sizes (minimumTradeSize 0.1); the spec's
	// original int64 cannot express a viable DE30 size on a small account
	// (deviation noted in README). Rounded to the instrument's unit precision.
	Units         float64
	OrderType     string  // "MARKET" | "LIMIT"
	LimitPrice    float64 // for LIMIT only
	StopLoss      float64 // bracket
	TakeProfit    float64 // bracket
	Policy        ManagementPolicy
	ClientOrderID string // idempotency key (also OANDA clientExtensions.id)
	Account       string // which per-market account
	Strategy      string // audit / ledger
	Direction     string // audit / ledger
	Reason        string // entry audit (which conditions fired)
}

// OpenTrade is the reconciled view of a live OANDA trade.
type OpenTrade struct {
	TradeID       string
	ClientOrderID string
	Account       string
	Instrument    string
	Units         float64 // signed; fractional allowed (see OrderRequest.Units)
	Entry         float64
	CurrentSL     float64
	CurrentTP     float64
	// RiskDistance is |entry - initial SL|, captured at entry and never
	// recomputed, so breakeven/R math is stable after the SL is moved.
	RiskDistance float64
	Policy       ManagementPolicy
	OpenedAt     time.Time
}

// Direction returns LONG for positive units, SHORT otherwise.
func (t OpenTrade) Direction() string {
	if t.Units >= 0 {
		return DirectionLong
	}
	return DirectionShort
}

// SystemState is the control-plane state machine.
type SystemState string

const (
	StateActive     SystemState = "ACTIVE"
	StatePaused     SystemState = "PAUSED"        // per-market flag, see control-plane
	StateSystemLock SystemState = "SYSTEM_LOCKED" // daily breaker / manual lock
	StateDisabled   SystemState = "DISABLED"      // after FLATTEN
)

// TradeEvent is the async ledger record (11-trade-ledger-persistence.md).
type TradeEvent struct {
	Type          string // opened | modified | closed | rejected
	TradeID       string
	ClientOrderID string
	Account       string
	Instrument    string
	Strategy      string
	Direction     string
	Units         float64
	EntryPrice    float64
	StopLoss      float64
	TakeProfit    float64
	ExitPrice     float64
	RealizedPL    float64
	OpenTime      time.Time
	CloseTime     time.Time
	ExitReason    string // tp / sl / breakeven / time_cutoff / news / flatten
	Reason        string // entry audit or rejection reason
	EventSeq      int64
	At            time.Time
}
