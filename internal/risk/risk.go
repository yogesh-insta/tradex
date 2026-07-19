// Package risk is the unified risk module per docs/specs/06-risk-management.md:
// the single source of truth for "can we take this trade, and at what size?".
// It consumes a Signal, applies the gate chain and sizing, and either emits a
// sized OrderRequest or rejects with a machine reason. Evaluated per account.
package risk

import (
	"fmt"
	"math"
	"time"

	"github.com/yogesh-insta/tradex/internal/config"
	"github.com/yogesh-insta/tradex/internal/execution"
	"github.com/yogesh-insta/tradex/pkg/types"
)

// Rejection reason codes (spec 06 gate order).
const (
	ReasonSystemNotActive     = "system_not_active"
	ReasonDailyLossBreaker    = "daily_loss_breaker"
	ReasonStaleMarketData     = "stale_market_data"
	ReasonNewsBlackout        = "news_blackout"
	ReasonAlreadyOpen         = "already_open"
	ReasonCorrelatedOpen      = "correlated_open"
	ReasonMaxConcurrent       = "max_concurrent"
	ReasonConsecutiveLossHalt = "consecutive_loss_breaker"
	ReasonSizeTooSmall        = "size_too_small"
	ReasonMarginGate          = "margin_gate"
	ReasonAccountStateUnknown = "account_state_unknown"
	ReasonATRUnavailable      = "atr_unavailable"
)

// Rejection is a gate failure with a machine-readable reason.
type Rejection struct {
	Reason string
	Detail string
}

func (r *Rejection) Error() string {
	if r.Detail == "" {
		return "risk: rejected: " + r.Reason
	}
	return fmt.Sprintf("risk: rejected: %s (%s)", r.Reason, r.Detail)
}

// AccountState is the per-account view risk consults (implemented by
// portfolio.Account). Values reflect the last reconcile.
type AccountState interface {
	Name() string
	Equity() float64
	EquityKnown() bool
	DailyRealizedPL() float64 // vs baseline (negative = loss)
	ConsecutiveLosses() int
}

// Deps are the pull-based inputs to the gate chain. All are required.
type Deps struct {
	SystemState func() types.SystemState
	// ForceLock trips the breaker → SYSTEM_LOCKED (control plane owns re-arm).
	ForceLock func(reason string)
	// Stale reports market-data staleness for the instrument during session.
	Stale func(instrument string, now time.Time) bool
	// TimeToNews returns time until the next high-impact event for a region
	// (0 = imminent / fail-safe).
	TimeToNews func(region string, now time.Time) time.Duration
	// OpenTrades is the reconciled per-account view (never calls OANDA inline).
	OpenTrades func() []types.OpenTrade
	// Meta looks up instrument metadata from the executor's cache.
	Meta func(sym string) (execution.InstrumentMeta, error)
	// Price returns the latest mid for margin math (0 = unknown).
	Price func(instrument string) float64
}

// Engine evaluates signals for one account.
type Engine struct {
	cfg     config.RiskConfig
	account AccountState
	region  string // news region for this account's instruments ("EU" or "FX")
	deps    Deps
	fx      *FXProfile
	fxDeps  FXDeps
}

// NewEngine builds a per-account engine with the merged risk config.
func NewEngine(cfg config.RiskConfig, account AccountState, region string, deps Deps) *Engine {
	return &Engine{cfg: cfg, account: account, region: region, deps: deps}
}

// Evaluate runs the gate chain (fail fast, spec order) and sizes the order.
func (e *Engine) Evaluate(sig types.Signal, now time.Time) (types.OrderRequest, error) {
	var zero types.OrderRequest

	// 1. System state.
	if st := e.deps.SystemState(); st != types.StateActive {
		return zero, &Rejection{Reason: ReasonSystemNotActive, Detail: string(st)}
	}
	// 2. Kill/breaker: daily loss.
	if !e.account.EquityKnown() {
		return zero, &Rejection{Reason: ReasonAccountStateUnknown}
	}
	if e.account.DailyRealizedPL() <= -e.cfg.DailyLossLimit {
		e.deps.ForceLock(ReasonDailyLossBreaker)
		return zero, &Rejection{Reason: ReasonDailyLossBreaker,
			Detail: fmt.Sprintf("realized %.2f <= -%.2f", e.account.DailyRealizedPL(), e.cfg.DailyLossLimit)}
	}
	// 3. Data health.
	if e.deps.Stale(sig.Instrument, now) {
		return zero, &Rejection{Reason: ReasonStaleMarketData}
	}
	// 4. News window (fail-safe: unknown calendar => 0 => blackout).
	if e.deps.TimeToNews(e.region, now) <= e.cfg.NewsBlockBefore.D() {
		return zero, &Rejection{Reason: ReasonNewsBlackout}
	}
	open := e.deps.OpenTrades()
	// 5. Concurrency per instrument.
	for _, t := range open {
		if t.Instrument == sig.Instrument {
			return zero, &Rejection{Reason: ReasonAlreadyOpen, Detail: t.TradeID}
		}
	}
	// 5b. FX: one trade / Tokyo session day (before correlation — single-instrument lane).
	if err := e.evaluateFXGates(sig, now); err != nil {
		return zero, err
	}
	// 6. Correlation guard.
	if inst := e.correlatedOpen(sig.Instrument, open); inst != "" {
		return zero, &Rejection{Reason: ReasonCorrelatedOpen, Detail: inst}
	}
	// 7. Max concurrent.
	if len(open) >= e.cfg.MaxConcurrent {
		return zero, &Rejection{Reason: ReasonMaxConcurrent, Detail: fmt.Sprintf("%d open", len(open))}
	}
	// 8. Consecutive losses.
	if e.account.ConsecutiveLosses() >= e.cfg.ConsecutiveLossHalt {
		e.deps.ForceLock(ReasonConsecutiveLossHalt)
		return zero, &Rejection{Reason: ReasonConsecutiveLossHalt,
			Detail: fmt.Sprintf("%d consecutive losses", e.account.ConsecutiveLosses())}
	}
	// 9. Sizing.
	units, err := e.size(sig)
	if err != nil {
		return zero, err
	}
	if sig.Direction == types.DirectionShort {
		units = -units
	}
	return types.OrderRequest{
		Instrument:    sig.Instrument,
		Units:         units,
		OrderType:     sig.OrderType,
		LimitPrice:    limitPrice(sig),
		StopLoss:      sig.StopLoss,
		TakeProfit:    sig.TakeProfit,
		Policy:        sig.Policy,
		ClientOrderID: ClientOrderID(sig),
		Account:       e.account.Name(),
		Strategy:      sig.Strategy,
		Direction:     sig.Direction,
		Reason:        sig.Reason,
	}, nil
}

// size computes |units| per spec 06 sizing + margin/leverage gate. Units are
// floored to the instrument's unit precision (fractional CFD sizes allowed).
func (e *Engine) size(sig types.Signal) (float64, error) {
	meta, err := e.deps.Meta(sig.Instrument)
	if err != nil {
		return 0, &Rejection{Reason: ReasonAccountStateUnknown, Detail: err.Error()}
	}
	stopDist := math.Abs(sig.EntryPrice - sig.StopLoss)
	if stopDist <= 0 || math.IsNaN(stopDist) {
		return 0, &Rejection{Reason: ReasonATRUnavailable, Detail: "zero stop distance"}
	}
	equity := e.account.Equity()
	if equity <= 0 {
		return 0, &Rejection{Reason: ReasonAccountStateUnknown, Detail: "equity <= 0"}
	}

	pointSize := math.Pow10(meta.PipLocation)
	stopPoints := stopDist / pointSize
	riskCapital := equity * e.cfg.RiskPerTrade
	unitStep := math.Pow10(-meta.UnitsPrecision) // 1 for whole units, 0.1 for CFDs
	floorToStep := func(u float64) float64 { return math.Floor(u/unitStep) * unitStep }

	units := floorToStep(riskCapital / (stopPoints * meta.PointValue))
	if units < meta.MinUnits {
		return 0, &Rejection{Reason: ReasonSizeTooSmall,
			Detail: fmt.Sprintf("units %g < min %g", units, meta.MinUnits)}
	}

	price := e.deps.Price(sig.Instrument)
	if price <= 0 {
		price = sig.EntryPrice
	}
	// Margin/leverage gate: scale down until required margin fits AND
	// effective leverage is under the cap; reject if even min size violates.
	for units >= meta.MinUnits {
		notional := units * price
		margin := notional * meta.MarginRate
		if margin <= e.cfg.MaxMarginFrac*equity && notional/equity < e.cfg.MaxLeverage {
			return units, nil
		}
		units = floorToStep(units - unitStep)
	}
	return 0, &Rejection{Reason: ReasonMarginGate}
}

func (e *Engine) correlatedOpen(instrument string, open []types.OpenTrade) string {
	for _, group := range e.cfg.CorrelationGroups {
		inGroup := false
		for _, g := range group {
			if g == instrument {
				inGroup = true
				break
			}
		}
		if !inGroup {
			continue
		}
		for _, t := range open {
			if t.Instrument == instrument {
				continue // gate 5 already handles same-instrument
			}
			for _, g := range group {
				if t.Instrument == g {
					return t.Instrument
				}
			}
		}
	}
	return ""
}

// CheckBreakers re-evaluates the loss breakers outside a signal path (called
// after each trade close) so locks trip promptly, not only at the next signal.
func (e *Engine) CheckBreakers() {
	if !e.account.EquityKnown() {
		return
	}
	if e.account.DailyRealizedPL() <= -e.cfg.DailyLossLimit {
		e.deps.ForceLock(ReasonDailyLossBreaker)
		return
	}
	if e.account.ConsecutiveLosses() >= e.cfg.ConsecutiveLossHalt {
		e.deps.ForceLock(ReasonConsecutiveLossHalt)
	}
}

// ClientOrderID builds the idempotency key per spec 01:
// {strategy}-{instrument}-{yyyymmdd}-{hhmm} from the signal time.
func ClientOrderID(sig types.Signal) string {
	return fmt.Sprintf("%s-%s-%s", sig.Strategy, sig.Instrument, sig.At.Format("20060102-1504"))
}

func limitPrice(sig types.Signal) float64 {
	if sig.OrderType == types.OrderTypeLimit {
		return sig.EntryPrice
	}
	return 0
}
