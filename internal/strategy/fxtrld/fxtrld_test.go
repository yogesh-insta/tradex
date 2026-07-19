package fxtrld

import (
	"testing"
	"time"

	"github.com/yogesh-insta/tradex/pkg/types"
)

func mustTokyo(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func mustNY(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func testConfig(t *testing.T) Config {
	return Config{
		VolumeSpikeMult:  1.0,
		SLATRMult:        0.5,
		TPATRMult:        1.5,
		BreakevenAtR:     1.0,
		MinATRFrac:       0.15,
		MaxATRFrac:       1.25,
		MaxSpreadPips:    1.5,
		PipSize:          0.01,
		TradeWindowStart: "16:00:00",
		EntryWindowEnd:   "19:00:00",
		FridayCutoff:     "16:00:00",
		FridayCutoffLoc:  mustNY(t),
		SoftCutoff:       "21:00:00",
		Location:         mustTokyo(t),
	}
}

// Tokyo range 150.00–150.40 (W=0.40), ATR=1.0 → width in [0.15, 1.25].
func baseState() types.SessionState {
	return types.SessionState{
		Instrument:  "USD_JPY",
		OpeningHigh: 150.40,
		OpeningLow:  150.00,
		RangeLocked: true,
		DailyATR:    1.0,
		VWAP:        150.20,
		VolMA12:     100,
	}
}

func eventAt(t *testing.T, clock string, c types.Candle) types.MarketEvent {
	t.Helper()
	loc := mustTokyo(t)
	// Wednesday 2026-07-15 JST
	now, err := time.ParseInLocation("2006-01-02 15:04:05", "2026-07-15 "+clock, loc)
	if err != nil {
		t.Fatal(err)
	}
	c.Instrument = "USD_JPY"
	c.Timeframe = types.M5
	c.Complete = true
	c.Start = now.Add(-5 * time.Minute)
	return types.MarketEvent{
		Instrument: "USD_JPY",
		Now:        now,
		Timeframe:  types.M5,
		Last:       c,
		Price:      c.Close,
	}
}

func TestAnalyzeEntryMatrix(t *testing.T) {
	s, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		clock   string
		candle  types.Candle
		mutate  func(*types.SessionState)
		spread  float64
		wantDir string
	}{
		{
			name:    "long: close above range, above VWAP, volume spike",
			clock:   "16:30:00",
			candle:  types.Candle{Open: 150.35, High: 150.50, Low: 150.30, Close: 150.45, Volume: 150},
			wantDir: types.DirectionLong,
		},
		{
			name:    "short: close below range, below VWAP, volume spike",
			clock:   "16:30:00",
			candle:  types.Candle{Open: 150.05, High: 150.10, Low: 149.90, Close: 149.95, Volume: 150},
			wantDir: types.DirectionShort,
		},
		{
			name:   "wick-only above high: no signal",
			clock:  "16:30:00",
			candle: types.Candle{Open: 150.30, High: 150.50, Low: 150.25, Close: 150.35, Volume: 150},
		},
		{
			name:   "low volume",
			clock:  "16:30:00",
			candle: types.Candle{Open: 150.35, High: 150.50, Low: 150.30, Close: 150.45, Volume: 90},
		},
		{
			name:   "wrong VWAP side",
			clock:  "16:30:00",
			candle: types.Candle{Open: 150.35, High: 150.50, Low: 150.30, Close: 150.45, Volume: 150},
			mutate: func(st *types.SessionState) { st.VWAP = 150.50 },
		},
		{
			name:   "narrow range",
			clock:  "16:30:00",
			candle: types.Candle{Open: 150.35, High: 150.50, Low: 150.30, Close: 150.45, Volume: 150},
			mutate: func(st *types.SessionState) { st.OpeningHigh = 150.10; st.OpeningLow = 150.00 }, // W=0.10 < 0.15
		},
		{
			name:   "wide range",
			clock:  "16:30:00",
			candle: types.Candle{Open: 151.50, High: 151.60, Low: 151.40, Close: 151.55, Volume: 150},
			mutate: func(st *types.SessionState) { st.OpeningHigh = 151.40; st.OpeningLow = 150.00 }, // W=1.40 > 1.25
		},
		{
			name:   "outside entry window",
			clock:  "19:05:00",
			candle: types.Candle{Open: 150.35, High: 150.50, Low: 150.30, Close: 150.45, Volume: 150},
		},
		{
			name:   "spread too wide",
			clock:  "16:30:00",
			candle: types.Candle{Open: 150.35, High: 150.50, Low: 150.30, Close: 150.45, Volume: 150},
			spread: 0.02, // 2.0 pips > 1.5
		},
		{
			name:   "range not locked",
			clock:  "16:30:00",
			candle: types.Candle{Open: 150.35, High: 150.50, Low: 150.30, Close: 150.45, Volume: 150},
			mutate: func(st *types.SessionState) { st.RangeLocked = false },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := baseState()
			if tt.mutate != nil {
				tt.mutate(&st)
			}
			ev := eventAt(t, tt.clock, tt.candle)
			ev.Spread = tt.spread
			sig := s.Analyze(ev, st)
			if tt.wantDir == "" {
				if sig != nil {
					t.Fatalf("expected nil, got %+v", sig)
				}
				return
			}
			if sig == nil {
				t.Fatal("expected signal")
			}
			if sig.Direction != tt.wantDir || sig.Strategy != Name {
				t.Fatalf("dir=%s strategy=%s", sig.Direction, sig.Strategy)
			}
			// R:R = tp/sl mult = 3.0
			slDist := abs(sig.EntryPrice - sig.StopLoss)
			tpDist := abs(sig.TakeProfit - sig.EntryPrice)
			if abs(tpDist/slDist-3.0) > 1e-9 {
				t.Fatalf("R:R = %v, want 3", tpDist/slDist)
			}
		})
	}
}

func TestVolumeThresholdBoundary(t *testing.T) {
	s, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	st := baseState()
	// volMA12=100, mult=1.0 → need volume > 100
	under := eventAt(t, "16:30:00", types.Candle{Open: 150.35, High: 150.50, Low: 150.30, Close: 150.45, Volume: 100})
	if s.Analyze(under, st) != nil {
		t.Fatal("volume == threshold should be nil")
	}
	over := eventAt(t, "16:30:00", types.Candle{Open: 150.35, High: 150.50, Low: 150.30, Close: 150.45, Volume: 101})
	if s.Analyze(over, st) == nil {
		t.Fatal("expected long when volume just over threshold")
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
