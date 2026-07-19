package backtest

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/yogesh-insta/tradex/internal/config"
	"github.com/yogesh-insta/tradex/pkg/types"
)

func discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func engineTestConfig() *config.Config {
	return &config.Config{
		Env:     "test",
		Candles: config.CandlesConfig{WindowMax: 50},
		EUSession: config.EUSessionConfig{
			TZ:               "Europe/Berlin",
			RangeStart:       "08:00:00",
			RangeEnd:         "09:00:00",
			TradeWindowStart: "09:05:00",
			ATRPeriodDays:    14,
			VolMACandles:     12,
			Instruments:      []string{"DE30_EUR"},
		},
		Strategies: config.StrategiesConfig{EULove: config.EULoveConfig{
			VolumeSpikeMult: 1.0,
			SLATRMult:       0.5,
			TPATRMult:       1.5,
			BreakevenAtR:    1.0,
			EntryWindowEnd:  "11:00:00",
		}},
		Risk: config.RiskConfig{
			RiskPerTrade:        0.01,
			DailyLossLimit:      150,
			ConsecutiveLossHalt: 3,
			MaxConcurrent:       1,
			MaxMarginFrac:       0.10,
			MaxLeverage:         5.0,
			NewsBlockBefore:     config.Duration(30 * time.Minute),
			CorrelationGroups:   [][]string{{"DE30_EUR", "FR40_EUR"}},
		},
		Mgmt: config.MgmtConfig{
			NewsBlockBefore: config.Duration(30 * time.Minute),
			EUFridayCutoff:  "17:30:00",
		},
		Backtest: config.BacktestConfig{
			InitialEquity:  5000,
			SpreadPoints:   2.0,
			SlippagePoints: 0.5,
		},
	}
}

// syntheticDay builds one Wednesday (2026-07-15) of DE30 candles engineered
// to trigger a long EU LOVE breakout that runs to its take profit.
func syntheticDay() InstrumentData {
	day := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC) // Berlin = UTC+2

	var daily []types.Candle
	base := 24000.0
	for i := 20; i >= 1; i-- {
		start := day.AddDate(0, 0, -i)
		// Constant true range of 120 → 14-day Wilder ATR = 120.
		daily = append(daily, types.Candle{
			Instrument: "DE30_EUR", Timeframe: types.D, Start: start,
			Open: base, High: base + 120, Low: base, Close: base + 60,
			Volume: 10000, Complete: true,
		})
	}

	mk := func(hour, minute int, o, h, l, c float64, v int64) types.Candle {
		return types.Candle{
			Instrument: "DE30_EUR", Timeframe: types.M5,
			Start: day.Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute),
			Open:  o, High: h, Low: l, Close: c, Volume: v, Complete: true,
		}
	}

	var m5 []types.Candle
	// Range window 08:00–09:00 Berlin = 06:00–07:00 UTC: 12 quiet candles.
	for i := 0; i < 12; i++ {
		m5 = append(m5, mk(6, i*5, 24000, 24050, 23950, 24000, 100))
	}
	// 09:00–09:30 Berlin: still inside the range (no early signal).
	for i := 0; i < 6; i++ {
		m5 = append(m5, mk(7, i*5, 24000, 24040, 23960, 24010, 100))
	}
	// 09:35 Berlin close: full breakout candle — close above range high AND
	// VWAP, volume 300 > VolMA12 (~100).
	m5 = append(m5, mk(7, 30, 24040, 24120, 24030, 24100, 300))
	// Next candle runs to the take profit (entry+1.5×ATR ≈ 24280). Volume is
	// kept at/below the moving average so it cannot count as a second
	// breakout (same-day re-entry is allowed by spec, but not tested here).
	m5 = append(m5, mk(7, 35, 24100, 24300, 24090, 24290, 100))
	m5 = append(m5, mk(7, 40, 24290, 24295, 24280, 24285, 90))

	// H1 range anchor: 08:00–09:00 Berlin candle.
	h1 := []types.Candle{{
		Instrument: "DE30_EUR", Timeframe: types.H1,
		Start: day.Add(6 * time.Hour),
		Open:  24000, High: 24050, Low: 23950, Close: 24000,
		Volume: 1200, Complete: true,
	}}

	return InstrumentData{M5: m5, H1: h1, Daily: daily}
}

func TestEndToEndBreakoutBacktest(t *testing.T) {
	result, err := Run(context.Background(), Params{
		Cfg:  engineTestConfig(),
		Data: map[string]InstrumentData{"DE30_EUR": syntheticDay()},
		Log:  discard(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Metrics.Trades != 1 {
		t.Fatalf("trades = %d, want exactly 1 (single breakout)", result.Metrics.Trades)
	}
	trade := result.Closed[0]
	if trade.Trade.Direction() != types.DirectionLong {
		t.Fatalf("direction = %s, want LONG", trade.Trade.Direction())
	}
	// Entry = breakout close 24100 + half spread 1.0 + slippage 0.5.
	if trade.Trade.Entry != 24101.5 {
		t.Fatalf("entry = %v, want 24101.5", trade.Trade.Entry)
	}
	if trade.ExitReason != "tp" {
		t.Fatalf("exit reason = %s, want tp", trade.ExitReason)
	}
	if trade.RealizedPL <= 0 {
		t.Fatalf("realized PL = %v, want > 0", trade.RealizedPL)
	}
	if result.Metrics.WinRate != 1.0 || result.Metrics.NetPnL != trade.RealizedPL {
		t.Fatalf("metrics inconsistent: %+v", result.Metrics)
	}
	if result.Metrics.FinalEquity != 5000+trade.RealizedPL {
		t.Fatalf("final equity = %v", result.Metrics.FinalEquity)
	}
}

func TestBacktestRespectsCorrelationAndConcurrency(t *testing.T) {
	// Same engineered day on both correlated instruments: only ONE may open.
	data := map[string]InstrumentData{
		"DE30_EUR": syntheticDay(),
		"FR40_EUR": syntheticDay(),
	}
	// Rename the FR40 copies.
	fr := data["FR40_EUR"]
	for i := range fr.M5 {
		fr.M5[i].Instrument = "FR40_EUR"
	}
	for i := range fr.H1 {
		fr.H1[i].Instrument = "FR40_EUR"
	}
	for i := range fr.Daily {
		fr.Daily[i].Instrument = "FR40_EUR"
	}
	data["FR40_EUR"] = fr

	cfg := engineTestConfig()
	cfg.EUSession.Instruments = []string{"DE30_EUR", "FR40_EUR"}

	result, err := Run(context.Background(), Params{
		Cfg:  cfg,
		Data: data,
		Log:  discard(),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Both signal on the same close; the correlation guard admits one. After
	// its TP exit, the entry window still allows nothing new (no second
	// breakout candle), so exactly one trade total.
	if result.Metrics.Trades != 1 {
		t.Fatalf("trades = %d, want 1 (correlation guard)", result.Metrics.Trades)
	}
}
