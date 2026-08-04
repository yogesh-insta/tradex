package asxrotator

import (
	"strings"
	"testing"
	"time"
)

// seriesFrom builds a daily series ending today, one candle per day.
func seriesFrom(sym string, closes ...float64) Series {
	s := Series{Symbol: sym}
	start := time.Now().UTC().AddDate(0, 0, -len(closes))
	for i, c := range closes {
		s.Candles = append(s.Candles, Candle{Date: start.AddDate(0, 0, i), Close: c})
	}
	return s
}

// monthlySeries builds one candle per month for n months ending this month,
// so MonthEnds/MomentumReturn see a real month-end history.
func monthlySeries(sym string, closes ...float64) Series {
	s := Series{Symbol: sym}
	now := time.Now().UTC()
	for i, c := range closes {
		d := now.AddDate(0, -(len(closes) - 1 - i), 0)
		s.Candles = append(s.Candles, Candle{
			Date:  time.Date(d.Year(), d.Month(), 15, 0, 0, 0, 0, time.UTC),
			Close: c,
		})
	}
	return s
}

func TestBelowMinPrice(t *testing.T) {
	tests := []struct {
		name  string
		s     Series
		floor float64
		want  bool
	}{
		{"above floor", seriesFrom("BHP", 40, 41, 42), 1.00, false},
		{"exactly at floor is allowed", seriesFrom("X", 5, 1.00), 1.00, false},
		{"below floor", seriesFrom("ZIP", 2.0, 0.85), 1.00, true},
		{"back-adjusted to zero", seriesFrom("TAH", 0.0001, 0.0001), 1.00, true},
		{"empty series counts as below", Series{}, 1.00, true},
		{"floor disabled", seriesFrom("TAH", 0.0001), 0, false},
		{"negative floor disables", seriesFrom("TAH", 0.0001), -1, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := BelowMinPrice(tc.s, tc.floor); got != tc.want {
				t.Errorf("BelowMinPrice = %v, want %v", got, tc.want)
			}
		})
	}
}

// The defect this lane exists to prevent. Yahoo back-adjusts for demergers by
// scaling the whole prior history down; TAH.AX reads A$0.0000 for 103 months
// (Echo 2011, Lottery Corp 2022). Momentum off a ~zero base reads ~+1800% and
// ranks #1 every month. Crucially the jump screens do NOT fire — the series is
// smooth, merely scaled — so only the price floor catches it. Unguarded, the
// 15y backtest reads 41-51% CAGR of pure artifact against 25.0% with the floor.
func TestBackAdjustmentArtifactIsScreenedOnlyByPriceFloor(t *testing.T) {
	// 13 month-ends ramping smoothly from A$0.0001 to A$0.0019. This is the
	// shape the real defect takes: every price in the corrupted era is tiny,
	// no single step is violent (~28%/month, well under the 50% jump screen),
	// yet the 12-month base is 19x smaller than the latest close, so momentum
	// reads ~+1800%. A cliff-shaped fixture would be caught by BadJumpUp and
	// would not exercise the gap this test is about.
	closes := make([]float64, 13)
	closes[0] = 0.0001
	for i := 1; i < len(closes); i++ {
		closes[i] = closes[i-1] * 1.28
	}
	corrupted := monthlySeries("TAH", closes...)

	me := MonthEnds(corrupted)
	mom, ok := MomentumReturn(me, 12)
	if !ok {
		t.Fatal("expected 12m momentum to compute")
	}
	// Momentum is a fraction, so 18.3 means +1830% — the phantom return that
	// puts a delisted-price series at the top of the ranking.
	if mom < 10 {
		t.Fatalf("fixture is not reproducing the artifact: 12m momentum = %.1f (want >= 10, i.e. +1000%%)", mom)
	}

	// The existing screens are blind to it — this is the point of the test.
	window := BadJumpWindowDays(12)
	if BadJumpUp(corrupted, window, 0.5) {
		t.Error("fixture no longer exercises the gap: BadJumpUp caught it, " +
			"so the price floor is not the only thing standing between this and the ranking")
	}

	// The floor is what catches it, and it must reject the name outright.
	if !BelowMinPrice(corrupted, DefaultMinPriceAUD) {
		t.Error("price floor failed to screen a series back-adjusted to ~zero")
	}
}

// A name below the floor must be absent from BOTH lists. Keeping it on the slow
// list would let it stay in the book via exit hysteresis — the opposite of what
// the screen is for.
func TestBelowFloorNameIsExcludedFromEntryAndKeepSet(t *testing.T) {
	fast := []Ranked{{Symbol: "TAH", Momentum: 18.0}, {Symbol: "BHP", Momentum: 0.4}, {Symbol: "CBA", Momentum: 0.3}}
	slow := []Ranked{{Symbol: "TAH", Momentum: 18.0}, {Symbol: "BHP", Momentum: 0.5}}

	// With TAH scored, it takes the top slot and survives as a holding.
	got := BuildTarget(fast, slow, []Holding{{Symbol: "TAH", Qty: 1}}, 2, 3)
	if got[0] != "TAH" {
		t.Fatalf("precondition failed: unfiltered target = %v, expected TAH first", got)
	}

	// Run drops below-floor names before scoring, so they reach BuildTarget in
	// neither list — and a held TAH is then sold rather than kept.
	fastF := []Ranked{{Symbol: "BHP", Momentum: 0.4}, {Symbol: "CBA", Momentum: 0.3}}
	slowF := []Ranked{{Symbol: "BHP", Momentum: 0.5}}
	got = BuildTarget(fastF, slowF, []Holding{{Symbol: "TAH", Qty: 1}}, 2, 3)
	for _, s := range got {
		if s == "TAH" {
			t.Fatalf("TAH survived after filtering: %v", got)
		}
	}
	if strings.Join(got, ",") != "BHP,CBA" {
		t.Errorf("target = %v, want [BHP CBA]", got)
	}
}

// The hysteresis itself is shared with the NSE lane (internal/momentum); this
// confirms the ASX wrapper passes holdings and both lists through correctly.
func TestBuildTargetKeepsHoldingAliveViaSlowList(t *testing.T) {
	fast := []Ranked{{Symbol: "A", Momentum: 9}, {Symbol: "B", Momentum: 8}, {Symbol: "C", Momentum: 7}}
	// D is nowhere on the fast list but high on the slow one.
	slow := []Ranked{{Symbol: "D", Momentum: 9}, {Symbol: "A", Momentum: 8}}

	got := BuildTarget(fast, slow, []Holding{{Symbol: "D", Qty: 1}}, 2, 3)
	if got[0] != "D" {
		t.Errorf("target = %v, want D kept via the slow list", got)
	}
	if len(got) > 2 {
		t.Errorf("target %v exceeds topK=2", got)
	}
}

func TestRankIsDescendingWithAlphabeticalTieBreak(t *testing.T) {
	r := Rank(map[string]float64{"BHP": 0.5, "ANZ": 0.5, "CSL": 0.9})
	if r[0].Symbol != "CSL" || r[1].Symbol != "ANZ" || r[2].Symbol != "BHP" {
		t.Errorf("Rank = %v, want CSL, ANZ, BHP", r)
	}
}
