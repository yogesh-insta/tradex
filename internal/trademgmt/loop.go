// Package trademgmt implements the timer-driven trade-management loop per
// docs/specs/08-trade-management-loop.md: breakeven at R, time-cutoff
// flatten, and news flatten — level-triggered and idempotent, on top of the
// always-on broker brackets.
package trademgmt

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/yogesh-insta/tradex/internal/execution"
	"github.com/yogesh-insta/tradex/pkg/types"
	"github.com/yogesh-insta/tradex/pkg/utils"
)

// Config tunes the loop (mgmt config keys).
type Config struct {
	TickInterval      time.Duration
	ReconcileInterval time.Duration
	NewsBlockBefore   time.Duration
}

// Deps are the loop's pull-based inputs.
type Deps struct {
	Executor execution.OrderExecutor
	// Price returns the latest mid from the shared snapshot (0 = unknown).
	Price func(instrument string) float64
	// TimeToNews returns time until the next high-impact event for a region
	// (0 = imminent / fail-safe).
	TimeToNews func(region string, now time.Time) time.Duration
	// Region maps an instrument to its news region ("EU").
	Region func(instrument string) string
	// SystemState gates the loop: it manages positions in every state except
	// DISABLED (post-FLATTEN there is nothing left to manage). PAUSED trades
	// keep being managed to their exit per spec 09.
	SystemState func() types.SystemState
	// OnVanished fires when a trade disappears from OpenTrades() between
	// reconciles (bracket exit): the portfolio settles realized P&L.
	OnVanished func(t types.OpenTrade)
	Now        func() time.Time
}

// Loop is the shared, market-agnostic management loop.
type Loop struct {
	cfg  Config
	deps Deps
	log  *slog.Logger
	fx   FXConfig

	mu            sync.Mutex
	trades        []types.OpenTrade // last reconciled view
	lastReconcile time.Time
}

// NewLoop builds the loop.
func NewLoop(cfg Config, deps Deps, log *slog.Logger) *Loop {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &Loop{cfg: cfg, deps: deps, log: log}
}

// Run drives the loop until ctx is cancelled.
func (l *Loop) Run(ctx context.Context) {
	ticker := time.NewTicker(l.cfg.TickInterval)
	defer ticker.Stop()
	l.reconcile(ctx) // seed the open-trades view on start (boot recovery)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.Pass(ctx)
		}
	}
}

// Pass executes one management pass (exported for tests/backtest).
func (l *Loop) Pass(ctx context.Context) {
	now := l.deps.Now()
	l.mu.Lock()
	due := now.Sub(l.lastReconcile) >= l.cfg.ReconcileInterval
	l.mu.Unlock()
	if due {
		l.reconcile(ctx)
	}
	if l.deps.SystemState() == types.StateDisabled {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.trades {
		l.manage(ctx, &l.trades[i], now)
	}
}

// manage applies the policy to one trade. Level-triggered: it checks state
// ("is the stop already at entry?"), never events, so a missed pass or
// restart is always corrected on the next pass.
func (l *Loop) manage(ctx context.Context, t *types.OpenTrade, now time.Time) {
	if t.TradeID == "" {
		return // already closed within this reconcile window
	}
	p := t.Policy

	// 1. Time-cutoff: unconditional flatten.
	if !p.TimeCutoff.IsZero() && !now.Before(p.TimeCutoff) {
		if err := l.deps.Executor.Close(ctx, t.TradeID); err != nil {
			l.log.Error("time-cutoff close failed; will retry next pass", "trade_id", t.TradeID, "error", err)
			return
		}
		l.log.Info("time-cutoff flatten", "trade_id", t.TradeID, "instrument", t.Instrument)
		t.TradeID = "" // drop from this pass; reconcile refreshes the view
		return
	}

	// 1b. FX extras (Friday NY flatten, soft-cutoff weak flatten, news underwater).
	if l.manageFXExtras(ctx, t, now) {
		return
	}

	// 2. News flatten: stops → breakeven ahead of high-impact events
	// (fail-safe: unknown calendar means imminent). EU path; FX news handled above.
	if !l.fx.NewsUnderwaterFlat && l.deps.TimeToNews(l.deps.Region(t.Instrument), now) <= l.cfg.NewsBlockBefore {
		l.moveStopToEntry(ctx, t, "news")
		return
	}

	// 3. Breakeven once profit >= BreakevenAtR × risk.
	if p.BreakevenAtR <= 0 || t.RiskDistance <= 0 {
		return
	}
	price := l.deps.Price(t.Instrument)
	if price <= 0 {
		return // no snapshot yet; brackets protect regardless
	}
	profit := price - t.Entry
	if t.Direction() == types.DirectionShort {
		profit = t.Entry - price
	}
	if profit/t.RiskDistance >= p.BreakevenAtR {
		l.moveStopToEntry(ctx, t, "breakeven")
	}
}

func (l *Loop) moveStopToEntry(ctx context.Context, t *types.OpenTrade, why string) {
	target := t.Entry
	if l.stopAt(t, target) {
		return // idempotent: already applied
	}
	if err := l.deps.Executor.ModifyStop(ctx, t.TradeID, target); err != nil {
		l.log.Error("modify stop failed; will retry next pass", "trade_id", t.TradeID, "why", why, "error", err)
		return
	}
	t.CurrentSL = target
	l.log.Info("stop moved to entry", "trade_id", t.TradeID, "instrument", t.Instrument, "why", why)
}

// stopAt compares the current stop to the target at instrument precision, so
// a broker-rounded stop is recognized as already-applied.
func (l *Loop) stopAt(t *types.OpenTrade, target float64) bool {
	prec := 5
	if meta, err := l.deps.Executor.Instrument(t.Instrument); err == nil {
		prec = meta.PricePrecision
	}
	return utils.RoundToPrecision(t.CurrentSL, prec) == utils.RoundToPrecision(target, prec)
}

// reconcile refreshes the open-trades view from the executor (OANDA is
// truth). Vanished trades were closed by their bracket → reported and dropped.
func (l *Loop) reconcile(ctx context.Context) {
	fresh, err := l.deps.Executor.OpenTrades(ctx)
	if err != nil {
		l.log.Error("reconcile failed; keeping previous view", "error", err)
		return
	}
	l.mu.Lock()
	var vanished []types.OpenTrade
	if l.deps.OnVanished != nil {
		freshIDs := map[string]bool{}
		for _, t := range fresh {
			freshIDs[t.TradeID] = true
		}
		for _, prev := range l.trades {
			if prev.TradeID != "" && !freshIDs[prev.TradeID] {
				vanished = append(vanished, prev)
			}
		}
	}
	l.trades = fresh
	l.lastReconcile = l.deps.Now()
	l.mu.Unlock()
	for _, t := range vanished {
		l.deps.OnVanished(t)
	}
}

// OpenTrades exposes the last reconciled view (STATUS command, risk gates).
func (l *Loop) OpenTrades() []types.OpenTrade {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]types.OpenTrade, 0, len(l.trades))
	for _, t := range l.trades {
		if t.TradeID != "" {
			out = append(out, t)
		}
	}
	return out
}
