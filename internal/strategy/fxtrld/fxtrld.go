// Package fxtrld implements FX TRLD (Tokyo Range → London Drive) per
// docs/specs/17-strategy-fx-trld.md: a pure decision function that signals when
// an M5 candle closes fully outside the Tokyo 09:00–11:00 range during the
// London drive window, on the correct side of VWAP with volume expansion.
package fxtrld

import (
	"fmt"
	"math"
	"time"

	"github.com/yogesh-insta/tradex/pkg/types"
	"github.com/yogesh-insta/tradex/pkg/utils"
)

// Name is the registry key and Signal.Strategy value (matches EU style).
const Name = "fx_trld"

// Config carries strategy tunables (strategies.fx_trld + session clocks).
type Config struct {
	VolumeSpikeMult  float64
	SLATRMult        float64
	TPATRMult        float64
	BreakevenAtR     float64
	MinATRFrac       float64
	MaxATRFrac       float64
	MaxSpreadPips    float64
	PipSize          float64 // USD_JPY = 0.01
	TradeWindowStart string  // Asia/Tokyo
	EntryWindowEnd   string  // Asia/Tokyo
	FridayCutoff     string  // America/New_York hard flatten → Policy.TimeCutoff
	FridayCutoffLoc  *time.Location
	TrailAfterR      float64
	Location         *time.Location // Asia/Tokyo
}

// Strategy is stateless across calls beyond SessionState.
type Strategy struct {
	cfg Config
}

// New validates config and returns the strategy.
func New(cfg Config) (*Strategy, error) {
	if cfg.Location == nil {
		return nil, fmt.Errorf("fxtrld: location required")
	}
	if cfg.PipSize <= 0 {
		cfg.PipSize = 0.01
	}
	if cfg.VolumeSpikeMult <= 0 || cfg.SLATRMult <= 0 || cfg.TPATRMult <= 0 || cfg.BreakevenAtR <= 0 {
		return nil, fmt.Errorf("fxtrld: multipliers must be > 0")
	}
	if cfg.MinATRFrac <= 0 || cfg.MaxATRFrac < cfg.MinATRFrac {
		return nil, fmt.Errorf("fxtrld: invalid atr frac band")
	}
	for _, clk := range []string{cfg.TradeWindowStart, cfg.EntryWindowEnd} {
		if _, _, _, err := utils.ParseClock(clk); err != nil {
			return nil, fmt.Errorf("fxtrld: %w", err)
		}
	}
	return &Strategy{cfg: cfg}, nil
}

// Name implements strategy.Strategy.
func (s *Strategy) Name() string { return Name }

// Analyze implements the FX TRLD entry matrix.
func (s *Strategy) Analyze(ev types.MarketEvent, st types.SessionState) *types.Signal {
	if ev.Timeframe != types.M5 || !ev.Last.Complete {
		return nil
	}
	if !st.RangeLocked {
		return nil
	}
	if st.DailyATR <= 0 || math.IsNaN(st.DailyATR) {
		return nil
	}
	if st.VolMA12 <= 0 {
		return nil
	}
	if !s.inEntryWindow(ev.Now) {
		return nil
	}

	width := st.OpeningHigh - st.OpeningLow
	minW := s.cfg.MinATRFrac * st.DailyATR
	maxW := s.cfg.MaxATRFrac * st.DailyATR
	if width < minW || width > maxW {
		return nil
	}

	// Strategy-local spread gate when MarketEvent exposes spread.
	if ev.Spread > 0 && s.cfg.MaxSpreadPips > 0 {
		pips := ev.Spread / s.cfg.PipSize
		if pips > s.cfg.MaxSpreadPips {
			return nil
		}
	}

	c := ev.Last
	volumeSpike := float64(c.Volume) > s.cfg.VolumeSpikeMult*st.VolMA12

	var dir string
	switch {
	case c.Close > st.OpeningHigh && c.Close > st.VWAP && volumeSpike:
		dir = types.DirectionLong
	case c.Close < st.OpeningLow && c.Close < st.VWAP && volumeSpike:
		dir = types.DirectionShort
	default:
		return nil
	}

	entry := c.Close
	slDist := s.cfg.SLATRMult * st.DailyATR
	tpDist := s.cfg.TPATRMult * st.DailyATR
	var sl, tp float64
	if dir == types.DirectionLong {
		sl, tp = entry-slDist, entry+tpDist
	} else {
		sl, tp = entry+slDist, entry-tpDist
	}

	trail := ""
	if s.cfg.TrailAfterR > 0 {
		trail = "after_r" // broker trail id resolved by executor conventions if enabled
	}

	return &types.Signal{
		Instrument: ev.Instrument,
		Strategy:   Name,
		Direction:  dir,
		OrderType:  types.OrderTypeMarket,
		EntryPrice: entry,
		StopLoss:   sl,
		TakeProfit: tp,
		Policy: types.ManagementPolicy{
			BreakevenAtR: s.cfg.BreakevenAtR,
			TimeCutoff:   s.timeCutoff(ev.Now),
			Trail:        trail,
		},
		Reason: fmt.Sprintf(
			"%s: close=%.3f %s tokyo[%.3f,%.3f] W=%.3f∈[%.3f,%.3f]×ATR, close %s vwap=%.3f, vol=%d > %.2f×volMA12=%.2f",
			dir, c.Close, breakoutWord(dir), st.OpeningLow, st.OpeningHigh,
			width, minW, maxW, sideWord(dir), st.VWAP, c.Volume, s.cfg.VolumeSpikeMult, st.VolMA12),
		At: ev.Now,
	}
}

func (s *Strategy) inEntryWindow(now time.Time) bool {
	start, err := utils.AtClock(now, s.cfg.TradeWindowStart, s.cfg.Location)
	if err != nil {
		return false
	}
	end, err := utils.AtClock(now, s.cfg.EntryWindowEnd, s.cfg.Location)
	if err != nil {
		return false
	}
	return !now.Before(start) && now.Before(end)
}

// timeCutoff sets Policy.TimeCutoff for Friday NY hard flatten only.
// Soft-cutoff weak-R flatten (< soft_cutoff_flatten_r) is owned by trademgmt
// (spec 18) — never seed TimeCutoff with the soft cutoff, or winning trades
// would flatten unconditionally at 21:00 JST.
func (s *Strategy) timeCutoff(now time.Time) time.Time {
	if s.cfg.FridayCutoff == "" || s.cfg.FridayCutoffLoc == nil {
		return time.Time{}
	}
	ny := now.In(s.cfg.FridayCutoffLoc)
	if ny.Weekday() != time.Friday {
		return time.Time{}
	}
	t, err := utils.AtClock(now, s.cfg.FridayCutoff, s.cfg.FridayCutoffLoc)
	if err != nil {
		return time.Time{}
	}
	return t
}

func breakoutWord(dir string) string {
	if dir == types.DirectionLong {
		return "above"
	}
	return "below"
}

func sideWord(dir string) string {
	if dir == types.DirectionLong {
		return ">"
	}
	return "<"
}
