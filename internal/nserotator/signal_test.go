package nserotator

import (
	"math"
	"testing"
	"time"
)

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func seriesFrom(dates []time.Time, closes []float64) Series {
	s := Series{Symbol: "TEST"}
	for i := range dates {
		s.Candles = append(s.Candles, Candle{Date: dates[i], Close: closes[i]})
	}
	return s
}

func TestMonthEndsTakesLastClosePerMonth(t *testing.T) {
	s := seriesFrom(
		[]time.Time{
			day(2026, 1, 5), day(2026, 1, 30),
			day(2026, 2, 2), day(2026, 2, 27),
			day(2026, 3, 31),
		},
		[]float64{10, 11, 12, 13, 14},
	)
	got := MonthEnds(s)
	want := []float64{11, 13, 14}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("month %d: got %v want %v", i, got[i], want[i])
		}
	}
}

// Pinned to kite/backtest/momentum.py: monthly.pct_change(lookback) on the same fixture.
func TestMomentumReturnMatchesPythonBacktest(t *testing.T) {
	me := []float64{50, 52, 51, 55, 58, 57, 60, 62, 61, 65, 64, 66, 70, 75}
	got, ok := MomentumReturn(me, 6)
	if !ok {
		t.Fatal("expected ok")
	}
	want := 0.2096774193548387 // 75/62 - 1
	if math.Abs(got-want) > 1e-12 {
		t.Errorf("momentum=%v want %v", got, want)
	}
	if _, ok := MomentumReturn(me[:6], 6); ok {
		t.Error("6 points cannot support 6-month lookback — want ok=false")
	}

	// 12-month lookback still supported when configured.
	got12, ok := MomentumReturn(me, 12)
	if !ok {
		t.Fatal("expected ok for 12-month lookback")
	}
	want12 := 0.4423076923076923 // 75/52 - 1
	if math.Abs(got12-want12) > 1e-12 {
		t.Errorf("12m momentum=%v want %v", got12, want12)
	}
}

// Pinned to pandas: Series.ewm(span=5, adjust=False).mean().iloc[-1].
func TestEMAMatchesPandasAdjustFalse(t *testing.T) {
	closes := []float64{100, 102, 101.5, 105, 107, 106, 110, 108, 112, 115}
	got, ok := EMA(closes, 5)
	if !ok {
		t.Fatal("expected ok")
	}
	want := 110.74343341970231
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("ema=%v want %v", got, want)
	}
	if _, ok := EMA(closes[:3], 5); ok {
		t.Error("want ok=false with fewer points than span")
	}
}

func TestRegimeInvested(t *testing.T) {
	var dates []time.Time
	var closes []float64
	for i := 0; i < 250; i++ {
		dates = append(dates, day(2025, 1, 1).AddDate(0, 0, i))
		closes = append(closes, 100+float64(i)) // steady uptrend
	}
	idx := seriesFrom(dates, closes)
	invested, last, ema, ok := RegimeInvested(idx, 200)
	if !ok || !invested {
		t.Fatalf("uptrend must be invested (ok=%v invested=%v)", ok, invested)
	}
	if last <= ema {
		t.Errorf("last %v should exceed ema %v", last, ema)
	}

	for i := range closes {
		closes[i] = 350 - float64(i) // steady downtrend
	}
	idx = seriesFrom(dates, closes)
	invested, _, _, ok = RegimeInvested(idx, 200)
	if !ok || invested {
		t.Fatalf("downtrend must be cash (ok=%v invested=%v)", ok, invested)
	}
}

func TestRankDeterministicTieBreak(t *testing.T) {
	r := Rank(map[string]float64{"B": 0.5, "A": 0.5, "C": 0.9})
	if r[0].Symbol != "C" || r[1].Symbol != "A" || r[2].Symbol != "B" {
		t.Errorf("got order %v %v %v", r[0].Symbol, r[1].Symbol, r[2].Symbol)
	}
}

func TestStale(t *testing.T) {
	now := day(2026, 7, 31)
	fresh := seriesFrom([]time.Time{day(2026, 7, 30)}, []float64{100})
	old := seriesFrom([]time.Time{day(2026, 7, 10)}, []float64{100})
	if Stale(fresh, now, 10*24*time.Hour) {
		t.Error("fresh series flagged stale")
	}
	if !Stale(old, now, 10*24*time.Hour) {
		t.Error("3-week-old series not flagged stale")
	}
	if !Stale(Series{}, now, time.Hour) {
		t.Error("empty series must be stale")
	}
}

func TestLargeJumps(t *testing.T) {
	dates := []time.Time{day(2026, 7, 1), day(2026, 7, 2), day(2026, 7, 3)}
	ok := seriesFrom(dates, []float64{100, 110, 105})
	upSpike := seriesFrom(dates, []float64{100, 220, 210})
	downGap := seriesFrom(dates, []float64{774, 272, 270}) // demerger-style
	if BadJumpUp(ok, 90, 0.5) {
		t.Error("10% up move flagged")
	}
	if !BadJumpUp(upSpike, 90, 0.5) {
		t.Error("120% up move not flagged")
	}
	if BadJumpUp(downGap, 90, 0.5) {
		t.Error("down move must not trigger BadJumpUp")
	}
	if !LargeDownJump(downGap, 90, 0.5) {
		t.Error("65% down move not flagged for warning")
	}
	if LargeDownJump(ok, 90, 0.5) {
		t.Error("10% down move flagged")
	}
}

func TestBadJumpWindowCoversMomentumLookback(t *testing.T) {
	const n = 130
	closes := make([]float64, n)
	for i := range closes {
		closes[i] = 100
	}
	spikeAt := n - 110 // glitch ~110 trading days ago — inside 6m momentum, outside 90d screen
	closes[spikeAt] = 300

	dates := make([]time.Time, n)
	for i := range dates {
		dates[i] = day(2025, 1, 1).AddDate(0, 0, i)
	}
	s := seriesFrom(dates, closes)

	if BadJumpUp(s, 90, 0.5) {
		t.Fatal("spike 110d ago should escape the old 90-day window")
	}
	window := BadJumpWindowDays(6)
	if !BadJumpUp(s, window, 0.5) {
		t.Fatalf("spike 110d ago should be caught by %d-day lookback-aligned window", window)
	}
}

func TestBadJumpWindowDays(t *testing.T) {
	if got := BadJumpWindowDays(6); got != 142 {
		t.Fatalf("BadJumpWindowDays(6) = %d want 142", got)
	}
}
