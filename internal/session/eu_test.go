package session

import (
	"context"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/yogesh-insta/tradex/internal/oanda"
	"github.com/yogesh-insta/tradex/pkg/types"
)

func discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

type fakeRest struct {
	byGran map[string][]oanda.RESTCandle
}

func (f fakeRest) Candles(_ context.Context, _ string, gran string, count int, from, to time.Time) (oanda.CandlesResponse, error) {
	all := f.byGran[gran]
	var picked []oanda.RESTCandle
	for _, c := range all {
		if !from.IsZero() && c.Time.Before(from) {
			continue
		}
		if !to.IsZero() && !c.Time.Before(to) {
			continue
		}
		picked = append(picked, c)
	}
	if count > 0 && len(picked) > count {
		picked = picked[len(picked)-count:]
	}
	return oanda.CandlesResponse{Candles: picked}, nil
}

func testCfg() Config {
	return Config{
		TZ:               "Europe/Berlin",
		RangeStart:       "08:00:00",
		RangeEnd:         "09:00:00",
		TradeWindowStart: "09:05:00",
		ATRPeriodDays:    3,
		VolMACandles:     12,
		Instruments:      []string{"DE30_EUR"},
	}
}

func TestWilderATR(t *testing.T) {
	daily := []types.Candle{
		{High: 100, Low: 100, Close: 100},
		{High: 105, Low: 95, Close: 102},  // TR 10
		{High: 110, Low: 100, Close: 108}, // TR 10
		{High: 109, Low: 99, Close: 100},  // TR 10
		{High: 106, Low: 98, Close: 104},  // TR 8
	}
	// seed = avg(10,10,10) = 10; wilder: (10*2 + 8)/3 = 9.3333…
	atr, err := WilderATR(daily, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := (10.0*2 + 8) / 3
	if math.Abs(atr-want) > 1e-9 {
		t.Fatalf("ATR = %v, want %v", atr, want)
	}

	if _, err := WilderATR(daily[:3], 3); err == nil {
		t.Fatal("expected error with insufficient candles")
	}
	if _, err := WilderATR(daily, 0); err == nil {
		t.Fatal("expected error with zero period")
	}
}

// m5Event builds an M5 candle event; start is UTC (Berlin = UTC+2 in July,
// so the 08:00–09:00 Berlin range = 06:00–07:00 UTC).
func m5Event(startUTC time.Time, high, low, close float64, vol int64) types.MarketEvent {
	c := types.Candle{
		Instrument: "DE30_EUR", Timeframe: types.M5, Start: startUTC,
		Open: (high + low) / 2, High: high, Low: low, Close: close,
		Volume: vol, Complete: true,
	}
	return types.MarketEvent{
		Instrument: "DE30_EUR", Now: startUTC.Add(5 * time.Minute),
		Timeframe: types.M5, Last: c,
	}
}

func TestRangeLockLifecycle(t *testing.T) {
	ctrl, err := NewController(testCfg(), fakeRest{}, nil, discard())
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC) // Wednesday

	// Twelve M5 candles across 06:00–07:00 UTC (= 08:00–09:00 Berlin).
	for i := 0; i < 12; i++ {
		start := day.Add(6*time.Hour + time.Duration(i*5)*time.Minute)
		ev := m5Event(start, 24000+float64(i), 23990-float64(i), 23995, 100)
		ctrl.OnCandle(ev)

		st, ok := ctrl.State("DE30_EUR", ev.Now)
		if !ok {
			t.Fatal("expected state")
		}
		if i < 11 && st.RangeLocked {
			t.Fatalf("range locked early at candle %d", i)
		}
	}

	// The candle closing at 07:00 UTC (09:00 Berlin) locks the range.
	st, _ := ctrl.State("DE30_EUR", day.Add(7*time.Hour))
	if !st.RangeLocked {
		t.Fatal("range should be locked at 09:00 Berlin")
	}
	if st.OpeningHigh != 24011 || st.OpeningLow != 23979 {
		t.Fatalf("range = [%v, %v], want [23979, 24011]", st.OpeningLow, st.OpeningHigh)
	}

	// The H1 candle spanning 08:00–09:00 Berlin is authoritative.
	h1 := types.MarketEvent{
		Instrument: "DE30_EUR", Now: day.Add(7 * time.Hour), Timeframe: types.H1,
		Last: types.Candle{
			Instrument: "DE30_EUR", Timeframe: types.H1,
			Start: day.Add(6 * time.Hour),
			High:  24012, Low: 23978, Close: 23995, Volume: 1200, Complete: true,
		},
	}
	ctrl.OnCandle(h1)
	st, _ = ctrl.State("DE30_EUR", day.Add(7*time.Hour))
	if st.OpeningHigh != 24012 || st.OpeningLow != 23978 {
		t.Fatalf("H1 range not adopted: [%v, %v]", st.OpeningLow, st.OpeningHigh)
	}

	// VWAP: all candles identical typical prices except tiny variation —
	// verify against the definition.
	if st.VWAP <= 0 {
		t.Fatal("VWAP should be positive")
	}

	// VolMA12 after the first post-range candle = avg of preceding 12 = 100.
	post := m5Event(day.Add(7*time.Hour), 24020, 24000, 24015, 250)
	ctrl.OnCandle(post)
	st, _ = ctrl.State("DE30_EUR", post.Now)
	if st.VolMA12 != 100 {
		t.Fatalf("VolMA12 = %v, want 100", st.VolMA12)
	}

	// New day resets the session (ATR carried until its own refresh).
	next := m5Event(day.Add(24*time.Hour+6*time.Hour), 24100, 24090, 24095, 100)
	ctrl.OnCandle(next)
	st, _ = ctrl.State("DE30_EUR", next.Now)
	if st.RangeLocked {
		t.Fatal("range must reset on a new session day")
	}
}

func TestVWAPMatchesDefinition(t *testing.T) {
	ctrl, err := NewController(testCfg(), fakeRest{}, nil, discard())
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)

	type bar struct {
		h, l, c float64
		v       int64
	}
	bars := []bar{{24010, 23990, 24000, 100}, {24020, 24000, 24015, 200}, {24030, 24010, 24025, 50}}
	var sumTV, sumV float64
	for i, b := range bars {
		start := day.Add(6*time.Hour + time.Duration(i*5)*time.Minute)
		ctrl.OnCandle(m5Event(start, b.h, b.l, b.c, b.v))
		typ := (b.h + b.l + b.c) / 3
		sumTV += typ * float64(b.v)
		sumV += float64(b.v)
	}
	st, _ := ctrl.State("DE30_EUR", day.Add(6*time.Hour+15*time.Minute))
	want := sumTV / sumV
	if math.Abs(st.VWAP-want) > 1e-9 {
		t.Fatalf("VWAP = %v, want %v", st.VWAP, want)
	}
}

func TestRefreshATRFromDailyCandles(t *testing.T) {
	day := time.Date(2026, 7, 1, 21, 0, 0, 0, time.UTC)
	mk := func(i int, h, l, c float64) oanda.RESTCandle {
		return oanda.RESTCandle{
			Complete: true, Time: day.AddDate(0, 0, i),
			Mid: oanda.OHLC{O: oanda.Num((h + l) / 2), H: oanda.Num(h), L: oanda.Num(l), C: oanda.Num(c)},
		}
	}
	rest := fakeRest{byGran: map[string][]oanda.RESTCandle{
		"D": {
			mk(0, 100, 100, 100),
			mk(1, 105, 95, 102),
			mk(2, 110, 100, 108),
			mk(3, 109, 99, 100),
			mk(4, 106, 98, 104),
		},
	}}
	ctrl, err := NewController(testCfg(), rest, nil, discard())
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.RefreshATR(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ := ctrl.State("DE30_EUR", time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC))
	want := (10.0*2 + 8) / 3
	if math.Abs(st.DailyATR-want) > 1e-9 {
		t.Fatalf("DailyATR = %v, want %v", st.DailyATR, want)
	}
}

func TestRecoverRangeMidSession(t *testing.T) {
	day := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	var m5 []oanda.RESTCandle
	for i := 0; i < 24; i++ { // 06:00–08:00 UTC = 08:00–10:00 Berlin
		start := day.Add(6*time.Hour + time.Duration(i*5)*time.Minute)
		m5 = append(m5, oanda.RESTCandle{
			Complete: true, Volume: 100, Time: start,
			Mid: oanda.OHLC{
				O: 24000, H: oanda.Num(24000 + float64(i)),
				L: oanda.Num(23990 - float64(i)), C: 23995,
			},
		})
	}
	rest := fakeRest{byGran: map[string][]oanda.RESTCandle{"M5": m5}}
	ctrl, err := NewController(testCfg(), rest, nil, discard())
	if err != nil {
		t.Fatal(err)
	}
	// Boot at 10:00 Berlin (08:00 UTC): the range must rebuild + lock.
	bootNow := day.Add(8 * time.Hour)
	if err := ctrl.RecoverRange(context.Background(), bootNow); err != nil {
		t.Fatal(err)
	}
	st, ok := ctrl.State("DE30_EUR", bootNow)
	if !ok {
		t.Fatal("expected state after recovery")
	}
	if !st.RangeLocked {
		t.Fatal("range must be locked after mid-session recovery")
	}
	// Only candles inside 06:00–07:00 UTC count for the range (i = 0..11).
	if st.OpeningHigh != 24011 || st.OpeningLow != 23979 {
		t.Fatalf("recovered range = [%v, %v], want [23979, 24011]", st.OpeningLow, st.OpeningHigh)
	}
	if st.VWAP <= 0 || st.VolMA12 != 100 {
		t.Fatalf("VWAP/VolMA not rebuilt: vwap=%v volma=%v", st.VWAP, st.VolMA12)
	}
}
