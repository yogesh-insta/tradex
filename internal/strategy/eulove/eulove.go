// Package eulove implements the EU LOVE (London Open Volatility Extension)
// strategy per docs/specs/05-strategy-eu-love.md: a pure decision function
// that signals when an M5 candle closes fully outside the 08:00–09:00 range
// on the execution side of VWAP with volume expansion.
package eulove

import (
	"fmt"
	"math"
	"time"

	"github.com/yogesh-insta/tradex/pkg/types"
	"github.com/yogesh-insta/tradex/pkg/utils"
)

// Name is the registry key and Signal.Strategy value.
const Name = "eu_love"

// Config carries the strategy tunables (strategies.eu_love + session clocks).
type Config struct {
	VolumeSpikeMult   float64
	SLATRMult         float64
	TPATRMult         float64
	BreakevenAtR      float64
	TradeWindowStart  string // "09:05:00" local
	EntryWindowEnd    string // "11:00:00" local
	FridayCutoff      string // "17:30:00" local; Friday hard flatten
	DailyCutoff       string // "" = none (v1); "17:30:00" for daily cutoff (research)
	RangeWidthMaxATR  float64 // 0 = disabled; skip day if (high-low) > this*DailyATR (research)
	MaxEntriesPerDay  int     // 0 = unlimited; cap same-day re-entries (research)
	Location          *time.Location
}

// Strategy is stateless across calls beyond SessionState; safe to share.
type Strategy struct {
	cfg Config
}

// New validates the config and returns the strategy.
func New(cfg Config) (*Strategy, error) {
	if cfg.Location == nil {
		return nil, fmt.Errorf("eulove: location required")
	}
	if cfg.VolumeSpikeMult <= 0 || cfg.SLATRMult <= 0 || cfg.TPATRMult <= 0 || cfg.BreakevenAtR <= 0 {
		return nil, fmt.Errorf("eulove: multipliers must be > 0")
	}
	for _, clk := range []string{cfg.TradeWindowStart, cfg.EntryWindowEnd, cfg.FridayCutoff} {
		if _, _, _, err := utils.ParseClock(clk); err != nil {
			return nil, fmt.Errorf("eulove: %w", err)
		}
	}
	return &Strategy{cfg: cfg}, nil
}

// Name implements strategy.Strategy.
func (s *Strategy) Name() string { return Name }

// Analyze implements the EU LOVE entry matrix. Deterministic: identical
// inputs always yield the identical Signal/nil.
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
	// Volume expansion needs an established baseline; without VolMA12 the
	// spike condition cannot be evaluated.
	if st.VolMA12 <= 0 {
		return nil
	}
	if !s.inEntryWindow(ev.Now) {
		return nil
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
			Trail:        "", // EU v1: no trailing
		},
		Reason: fmt.Sprintf(
			"%s: close=%.2f %s range[%.2f,%.2f], close %s vwap=%.2f, vol=%d > %.2f×volMA12=%.2f",
			dir, c.Close, breakoutWord(dir), st.OpeningLow, st.OpeningHigh,
			sideWord(dir), st.VWAP, c.Volume, s.cfg.VolumeSpikeMult, st.VolMA12),
		At: ev.Now,
	}
}

// inEntryWindow checks trade_window_start <= now < entry_window_end (local).
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

// timeCutoff resolves the applicable flatten time: Friday 17:30 local, plus
// an optional daily cutoff for non-Fridays (empty in v1).
func (s *Strategy) timeCutoff(now time.Time) time.Time {
	local := now.In(s.cfg.Location)
	if local.Weekday() == time.Friday {
		t, err := utils.AtClock(now, s.cfg.FridayCutoff, s.cfg.Location)
		if err == nil {
			return t
		}
	}
	if s.cfg.DailyCutoff != "" {
		t, err := utils.AtClock(now, s.cfg.DailyCutoff, s.cfg.Location)
		if err == nil {
			return t
		}
	}
	return time.Time{}
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
