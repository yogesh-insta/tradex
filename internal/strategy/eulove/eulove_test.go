package eulove

import (
	"math"
	"testing"
	"time"

	"github.com/yogesh-insta/tradex/pkg/types"
)

func mustBerlin(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
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
		TradeWindowStart: "09:05:00",
		EntryWindowEnd:   "11:00:00",
		FridayCutoff:     "17:30:00",
		Location:         mustBerlin(t),
	}
}

// baseState: locked range 24000–24100, ATR 120, VWAP 24050, VolMA12 100.
func baseState() types.SessionState {
	return types.SessionState{
		Instrument:  "DE30_EUR",
		OpeningHigh: 24100,
		OpeningLow:  24000,
		RangeLocked: true,
		DailyATR:    120,
		VWAP:        24050,
		VolMA12:     100,
	}
}

// eventAt builds an M5 close event at the given Berlin wall-clock on a
// Wednesday (2026-07-15).
func eventAt(t *testing.T, clock string, c types.Candle) types.MarketEvent {
	t.Helper()
	loc := mustBerlin(t)
	now, err := time.ParseInLocation("2006-01-02 15:04:05", "2026-07-15 "+clock, loc)
	if err != nil {
		t.Fatal(err)
	}
	c.Instrument = "DE30_EUR"
	c.Timeframe = types.M5
	c.Complete = true
	c.Start = now.Add(-5 * time.Minute)
	return types.MarketEvent{
		Instrument: "DE30_EUR",
		Now:        now,
		Timeframe:  types.M5,
		Last:       c,
		Price:      c.Close,
	}
}

func TestAnalyzeEntryMatrix(t *testing.T) {
	tests := []struct {
		name    string
		clock   string
		candle  types.Candle
		mutate  func(*types.SessionState)
		wantDir string // "" = expect nil
	}{
		{
			name:    "long: close above range, above VWAP, volume spike",
			clock:   "09:30:00",
			candle:  types.Candle{Open: 24090, High: 24120, Low: 24085, Close: 24110, Volume: 150},
			wantDir: types.DirectionLong,
		},
		{
			name:    "short: close below range, below VWAP, volume spike",
			clock:   "09:30:00",
			candle:  types.Candle{Open: 24010, High: 24015, Low: 23980, Close: 23990, Volume: 150},
			wantDir: types.DirectionShort,
		},
		{
			name:   "wick-only break: high above range but close inside",
			clock:  "09:30:00",
			candle: types.Candle{Open: 24080, High: 24130, Low: 24075, Close: 24095, Volume: 150},
		},
		{
			name:   "low volume: breakout close but volume under VolMA12",
			clock:  "09:30:00",
			candle: types.Candle{Open: 24090, High: 24120, Low: 24085, Close: 24110, Volume: 80},
		},
		{
			name:   "volume exactly at threshold is not a spike (strict >)",
			clock:  "09:30:00",
			candle: types.Candle{Open: 24090, High: 24120, Low: 24085, Close: 24110, Volume: 100},
		},
		{
			name:   "below VWAP: close above range high but under VWAP",
			clock:  "09:30:00",
			candle: types.Candle{Open: 24090, High: 24120, Low: 24085, Close: 24110, Volume: 150},
			mutate: func(st *types.SessionState) { st.VWAP = 24115 },
		},
		{
			name:   "range not locked",
			clock:  "09:30:00",
			candle: types.Candle{Open: 24090, High: 24120, Low: 24085, Close: 24110, Volume: 150},
			mutate: func(st *types.SessionState) { st.RangeLocked = false },
		},
		{
			name:   "ATR unavailable",
			clock:  "09:30:00",
			candle: types.Candle{Open: 24090, High: 24120, Low: 24085, Close: 24110, Volume: 150},
			mutate: func(st *types.SessionState) { st.DailyATR = 0 },
		},
		{
			name:   "ATR NaN",
			clock:  "09:30:00",
			candle: types.Candle{Open: 24090, High: 24120, Low: 24085, Close: 24110, Volume: 150},
			mutate: func(st *types.SessionState) { st.DailyATR = math.NaN() },
		},
		{
			name:   "no VolMA12 baseline",
			clock:  "09:30:00",
			candle: types.Candle{Open: 24090, High: 24120, Low: 24085, Close: 24110, Volume: 150},
			mutate: func(st *types.SessionState) { st.VolMA12 = 0 },
		},
		{
			name:   "before trade window (09:00 close)",
			clock:  "09:00:00",
			candle: types.Candle{Open: 24090, High: 24120, Low: 24085, Close: 24110, Volume: 150},
		},
		{
			name:    "first eligible close (09:05)",
			clock:   "09:05:00",
			candle:  types.Candle{Open: 24090, High: 24120, Low: 24085, Close: 24110, Volume: 150},
			wantDir: types.DirectionLong,
		},
		{
			name:   "after entry window end (11:00)",
			clock:  "11:00:00",
			candle: types.Candle{Open: 24090, High: 24120, Low: 24085, Close: 24110, Volume: 150},
		},
		{
			name:   "close inside range on both sides",
			clock:  "09:30:00",
			candle: types.Candle{Open: 24040, High: 24060, Low: 24030, Close: 24050, Volume: 150},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := New(testConfig(t))
			if err != nil {
				t.Fatal(err)
			}
			st := baseState()
			if tt.mutate != nil {
				tt.mutate(&st)
			}
			sig := s.Analyze(eventAt(t, tt.clock, tt.candle), st)
			if tt.wantDir == "" {
				if sig != nil {
					t.Fatalf("expected nil signal, got %+v", sig)
				}
				return
			}
			if sig == nil {
				t.Fatal("expected signal, got nil")
			}
			if sig.Direction != tt.wantDir {
				t.Fatalf("direction = %s, want %s", sig.Direction, tt.wantDir)
			}
			if sig.OrderType != types.OrderTypeMarket {
				t.Fatalf("order type = %s, want MARKET", sig.OrderType)
			}
		})
	}
}

func TestAnalyzeStopsAndTargets(t *testing.T) {
	s, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	st := baseState() // ATR 120 → SL dist 60, TP dist 180

	long := s.Analyze(eventAt(t, "09:30:00",
		types.Candle{Open: 24090, High: 24120, Low: 24085, Close: 24110, Volume: 150}), st)
	if long == nil {
		t.Fatal("expected long signal")
	}
	if long.EntryPrice != 24110 || long.StopLoss != 24050 || long.TakeProfit != 24290 {
		t.Fatalf("long entry/SL/TP = %.1f/%.1f/%.1f, want 24110/24050/24290",
			long.EntryPrice, long.StopLoss, long.TakeProfit)
	}
	// R:R = tp_atr_mult / sl_atr_mult = 3.0
	rr := (long.TakeProfit - long.EntryPrice) / (long.EntryPrice - long.StopLoss)
	if math.Abs(rr-3.0) > 1e-9 {
		t.Fatalf("R:R = %.4f, want 3.0", rr)
	}

	short := s.Analyze(eventAt(t, "09:30:00",
		types.Candle{Open: 24010, High: 24015, Low: 23980, Close: 23990, Volume: 150}), st)
	if short == nil {
		t.Fatal("expected short signal")
	}
	if short.StopLoss != 24050 || short.TakeProfit != 23810 {
		t.Fatalf("short SL/TP = %.1f/%.1f, want 24050/23810", short.StopLoss, short.TakeProfit)
	}
	if short.Policy.BreakevenAtR != 1.0 {
		t.Fatalf("BreakevenAtR = %v, want 1.0", short.Policy.BreakevenAtR)
	}
}

func TestAnalyzeDeterministic(t *testing.T) {
	s, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	ev := eventAt(t, "09:30:00", types.Candle{Open: 24090, High: 24120, Low: 24085, Close: 24110, Volume: 150})
	st := baseState()
	a, b := s.Analyze(ev, st), s.Analyze(ev, st)
	if a == nil || b == nil || *a != *b {
		t.Fatalf("Analyze not deterministic: %+v vs %+v", a, b)
	}
}

func TestTimeCutoffPolicy(t *testing.T) {
	s, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	loc := mustBerlin(t)
	st := baseState()
	candle := types.Candle{Open: 24090, High: 24120, Low: 24085, Close: 24110, Volume: 150,
		Instrument: "DE30_EUR", Timeframe: types.M5, Complete: true}

	// Friday 2026-07-17 → cutoff at 17:30 Berlin.
	friday, _ := time.ParseInLocation("2006-01-02 15:04:05", "2026-07-17 09:30:00", loc)
	sig := s.Analyze(types.MarketEvent{Instrument: "DE30_EUR", Now: friday, Timeframe: types.M5, Last: candle}, st)
	if sig == nil {
		t.Fatal("expected signal on Friday")
	}
	wantCutoff, _ := time.ParseInLocation("2006-01-02 15:04:05", "2026-07-17 17:30:00", loc)
	if !sig.Policy.TimeCutoff.Equal(wantCutoff) {
		t.Fatalf("Friday cutoff = %v, want %v", sig.Policy.TimeCutoff, wantCutoff)
	}

	// Wednesday → no cutoff (eu_daily_cutoff empty in v1).
	wednesday, _ := time.ParseInLocation("2006-01-02 15:04:05", "2026-07-15 09:30:00", loc)
	sig = s.Analyze(types.MarketEvent{Instrument: "DE30_EUR", Now: wednesday, Timeframe: types.M5, Last: candle}, st)
	if sig == nil {
		t.Fatal("expected signal on Wednesday")
	}
	if !sig.Policy.TimeCutoff.IsZero() {
		t.Fatalf("Wednesday cutoff = %v, want zero", sig.Policy.TimeCutoff)
	}
}
