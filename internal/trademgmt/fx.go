package trademgmt

import (
	"context"
	"time"

	"github.com/yogesh-insta/tradex/pkg/types"
	"github.com/yogesh-insta/tradex/pkg/utils"
)

// FXConfig adds FX management policies (spec 18 / mgmt amendments).
type FXConfig struct {
	Enabled            bool
	NewsUnderwaterFlat bool // true: news + underwater → flatten; in profit → BE
	SoftCutoffTZ       *time.Location
	SoftCutoff         string  // "21:00:00" Asia/Tokyo
	SoftCutoffFlattenR float64 // flatten if profitR < this at/after soft cutoff
	FridayCutoffTZ     *time.Location
	FridayCutoff       string // "16:00:00" America/New_York — unconditional
}

// WithFX attaches FX management policies to the loop.
func (l *Loop) WithFX(fx FXConfig) *Loop {
	l.fx = fx
	return l
}

// manageFXExtras applies Friday hard flatten, soft-cutoff weak flatten, and
// FX news underwater flatten. Returns true if the trade was closed/handled.
func (l *Loop) manageFXExtras(ctx context.Context, t *types.OpenTrade, now time.Time) bool {
	if !l.fx.Enabled {
		return false
	}

	// Friday NY hard flatten (unconditional) — even if Policy.TimeCutoff unset.
	if l.fx.FridayCutoff != "" && l.fx.FridayCutoffTZ != nil {
		ny := now.In(l.fx.FridayCutoffTZ)
		if ny.Weekday() == time.Friday {
			cut, err := utils.AtClock(now, l.fx.FridayCutoff, l.fx.FridayCutoffTZ)
			if err == nil && !now.Before(cut) {
				if err := l.deps.Executor.Close(ctx, t.TradeID); err != nil {
					l.log.Error("fx friday flatten failed", "trade_id", t.TradeID, "error", err)
					return true
				}
				l.log.Info("fx friday hard flatten", "trade_id", t.TradeID)
				t.TradeID = ""
				return true
			}
		}
	}

	profitR, ok := l.profitR(t)

	// Soft cutoff: flatten if still open with weak R.
	if ok && l.fx.SoftCutoff != "" && l.fx.SoftCutoffTZ != nil && l.fx.SoftCutoffFlattenR > 0 {
		cut, err := utils.AtClock(now, l.fx.SoftCutoff, l.fx.SoftCutoffTZ)
		if err == nil && !now.Before(cut) && profitR < l.fx.SoftCutoffFlattenR {
			if err := l.deps.Executor.Close(ctx, t.TradeID); err != nil {
				l.log.Error("fx soft-cutoff flatten failed", "trade_id", t.TradeID, "error", err)
				return true
			}
			l.log.Info("fx soft-cutoff weak flatten", "trade_id", t.TradeID, "profit_r", profitR)
			t.TradeID = ""
			return true
		}
	}

	// News: in profit → BE; underwater → flatten (FX delta vs EU always-BE).
	if l.fx.NewsUnderwaterFlat && l.deps.TimeToNews(l.deps.Region(t.Instrument), now) <= l.cfg.NewsBlockBefore {
		if ok && profitR < 0 {
			if err := l.deps.Executor.Close(ctx, t.TradeID); err != nil {
				l.log.Error("fx news underwater flatten failed", "trade_id", t.TradeID, "error", err)
				return true
			}
			l.log.Info("fx news underwater flatten", "trade_id", t.TradeID, "profit_r", profitR)
			t.TradeID = ""
			return true
		}
		// In profit, or price unknown: move to BE (fail-safe like EU).
		l.moveStopToEntry(ctx, t, "news")
		return true
	}
	return false
}

// profitR returns current profit in R multiples.
func (l *Loop) profitR(t *types.OpenTrade) (float64, bool) {
	if t.RiskDistance <= 0 {
		return 0, false
	}
	price := l.deps.Price(t.Instrument)
	if price <= 0 {
		return 0, false
	}
	profit := price - t.Entry
	if t.Direction() == types.DirectionShort {
		profit = t.Entry - price
	}
	return profit / t.RiskDistance, true
}
