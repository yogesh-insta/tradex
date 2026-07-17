# Spec: Data Contracts

## Purpose

Canonical Go types shared across components. Types are the *only* coupling between
packages; components depend on these shapes, not on each other's internals.

## Types

```go
package contracts

import "time"

// Timeframe identifies a candle granularity buffer.
type Timeframe string

const (
    M5 Timeframe = "M5"
    H1 Timeframe = "H1"
)

// Tick — one OANDA pricing update (mid derived by us).
type Tick struct {
    Instrument string
    Time       time.Time // UTC
    Bid, Ask   float64
    Mid        float64   // (bid+ask)/2
    Spread     float64   // ask-bid
    Volume     int64     // tick volume (running count within the forming candle)
}

// Candle — closed or forming OHLCV for one instrument+timeframe.
type Candle struct {
    Instrument string
    Timeframe  Timeframe
    Start      time.Time // candle open time, UTC
    Open, High, Low, Close float64
    Volume     int64
    Complete   bool // true once the candle is closed/confirmed
}

// MarketEvent — handed to the strategy router when a candle closes.
type MarketEvent struct {
    Instrument string
    Now        time.Time // instrument market tz (IANA)
    Timeframe  Timeframe // which buffer closed (v1 decisions trigger on M5)
    Last       Candle    // the candle that just closed
    Window     []Candle  // sliding window for this timeframe, newest last (<=50)
    Price      float64   // latest mid from the snapshot
}

// SessionState — per-day computed context, RAM only, read-only to strategies.
// Only EU fields are populated in v1; others reserved for future markets.
type SessionState struct {
    Instrument string

    // EU LOVE
    OpeningHigh, OpeningLow float64   // 08:00–09:00 CET range (locked at 09:00)
    RangeLocked             bool      // true once the range window has closed
    DailyATR                float64   // 14-day ATR (daily candles), computed pre-session
    VWAP                    float64   // running session VWAP
    VolMA12                 float64   // avg volume of preceding 12 M5 candles

    // Reserved (US / Asia)
    InitialHigh, InitialLow float64
    RSI14                   float64
    GapPct                  float64

    AsOf time.Time
}

// ManagementPolicy — how the shared loop manages THIS trade after entry.
type ManagementPolicy struct {
    BreakevenAtR float64   // move SL→entry once profit >= this x risk (EU: 1.0)
    TimeCutoff   time.Time // unconditional flatten (instrument tz); zero = none
    Trail        string    // optional broker trailing rule id ("" = none)
}

// Signal — strategy output: price intent + policy. NOT sized. nil = no setup.
type Signal struct {
    Instrument string
    Strategy   string    // "EU_LOVE"
    Direction  string    // "LONG" | "SHORT"
    OrderType  string    // "MARKET" (EU) | "LIMIT" (US, future)
    EntryPrice float64   // for LIMIT; reference for MARKET
    StopLoss   float64
    TakeProfit float64
    Policy     ManagementPolicy
    Reason     string    // audit: which conditions fired
    At         time.Time
}

// OrderRequest — risk output: concrete, sized, idempotent. Rejected if an open
// position for Instrument already exists, or a correlated instrument is open.
type OrderRequest struct {
    Instrument    string
    Units         int64   // signed: +long / -short
    OrderType     string  // "MARKET" | "LIMIT"
    LimitPrice    float64 // for LIMIT only
    StopLoss      float64 // bracket
    TakeProfit    float64 // bracket
    Policy        ManagementPolicy
    ClientOrderID string  // idempotency key (also OANDA clientExtensions.id)
    Account       string  // which per-market account
}

// OpenTrade — reconciled view of a live OANDA trade.
type OpenTrade struct {
    TradeID      string
    Instrument   string
    Units        int64
    Entry        float64
    CurrentSL    float64
    CurrentTP    float64
    RiskDistance float64 // |entry - initial SL|, for R math
    Policy       ManagementPolicy
    OpenedAt     time.Time
}

// SystemState — control-plane state machine.
type SystemState string

const (
    StateActive      SystemState = "ACTIVE"
    StatePaused      SystemState = "PAUSED"        // per-market flag, see control-plane
    StateSystemLock  SystemState = "SYSTEM_LOCKED" // daily breaker / manual lock
    StateDisabled    SystemState = "DISABLED"      // after FLATTEN
)
```

## Rules

- `ClientOrderID` format: `{strategy}-{instrument}-{yyyymmdd}-{hhmm}` (e.g.
  `eu_love-DE40_EUR-20260717-0905`), unique per intended entry so retries dedupe.
- `RiskDistance` is captured **at entry** from the original SL and never recomputed, so
  breakeven/R math is stable even after the SL is moved.
- Monetary values use `float64` in memory but are **formatted to the instrument's price
  precision** before hitting OANDA (see `07-order-executor.md`).

## Acceptance criteria

- All components import these types; no component redefines a shared shape.
- `Signal` and `SessionState` are treated as **read-only** by consumers.
- A round-trip (`Signal` → `OrderRequest` → OANDA → `OpenTrade`) preserves
  `Instrument`, direction sign, and `ClientOrderID`/`trade_id` linkage.
