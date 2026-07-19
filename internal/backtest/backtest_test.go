package backtest

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/yogesh-insta/tradex/internal/execution"
	"github.com/yogesh-insta/tradex/pkg/types"
)

func ct(pl float64, at time.Time) ClosedTrade {
	return ClosedTrade{RealizedPL: pl, ClosedAt: at}
}

func TestComputeMetrics(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	trades := []ClosedTrade{
		ct(+100, t0),                  // equity 5100, peak 5100
		ct(-50, t0.Add(1*time.Hour)),  // 5050, dd 50
		ct(-70, t0.Add(2*time.Hour)),  // 4980, dd 120
		ct(+200, t0.Add(3*time.Hour)), // 5180, new peak
		ct(-30, t0.Add(4*time.Hour)),  // 5150, dd 30
	}
	m := Compute(5000, trades)

	if m.Trades != 5 || m.Wins != 2 || m.Losses != 3 {
		t.Fatalf("counts = %d/%d/%d, want 5/2/3", m.Trades, m.Wins, m.Losses)
	}
	if m.NetPnL != 150 {
		t.Fatalf("net = %v, want 150", m.NetPnL)
	}
	if m.FinalEquity != 5150 {
		t.Fatalf("final equity = %v, want 5150", m.FinalEquity)
	}
	if math.Abs(m.WinRate-0.4) > 1e-9 {
		t.Fatalf("win rate = %v, want 0.4", m.WinRate)
	}
	if m.GrossProfit != 300 || m.GrossLoss != 150 {
		t.Fatalf("gross = %v/%v, want 300/150", m.GrossProfit, m.GrossLoss)
	}
	if math.Abs(m.ProfitFactor-2.0) > 1e-9 {
		t.Fatalf("profit factor = %v, want 2.0", m.ProfitFactor)
	}
	if m.MaxDrawdown != 120 {
		t.Fatalf("max drawdown = %v, want 120", m.MaxDrawdown)
	}
	if math.Abs(m.MaxDrawdownPct-120.0/5100.0) > 1e-9 {
		t.Fatalf("max drawdown pct = %v, want %v", m.MaxDrawdownPct, 120.0/5100.0)
	}
	if len(m.EquityCurve) != 6 { // initial point + 5 trades
		t.Fatalf("equity curve len = %d, want 6", len(m.EquityCurve))
	}
}

func TestComputeMetricsEdgeCases(t *testing.T) {
	t.Run("no trades", func(t *testing.T) {
		m := Compute(5000, nil)
		if m.Trades != 0 || m.WinRate != 0 || m.MaxDrawdown != 0 || m.FinalEquity != 5000 {
			t.Fatalf("unexpected: %+v", m)
		}
	})
	t.Run("no losses gives infinite profit factor", func(t *testing.T) {
		m := Compute(5000, []ClosedTrade{ct(100, time.Now())})
		if !math.IsInf(m.ProfitFactor, 1) {
			t.Fatalf("profit factor = %v, want +Inf", m.ProfitFactor)
		}
	})
}

func simMeta() map[string]execution.InstrumentMeta {
	m := DefaultSimMeta
	m.Symbol = "DE30_EUR"
	return map[string]execution.InstrumentMeta{"DE30_EUR": m}
}

func TestSimExecutorFillModel(t *testing.T) {
	now := time.Date(2026, 7, 15, 9, 30, 0, 0, time.UTC)
	mid := 24000.0
	var closes []ClosedTrade
	sim := NewSimExecutor(simMeta(), 2.0, 0.5, // spread 2.0 → half 1.0; slippage 0.5
		func() time.Time { return now },
		func(string) float64 { return mid },
		func(c ClosedTrade) { closes = append(closes, c) })

	t.Run("long entry pays half spread plus slippage", func(t *testing.T) {
		tr, err := sim.Open(context.Background(), types.OrderRequest{
			Instrument: "DE30_EUR", Units: 10, OrderType: types.OrderTypeMarket,
			StopLoss: 23940, TakeProfit: 24180, ClientOrderID: "c1",
		})
		if err != nil {
			t.Fatal(err)
		}
		if tr.Entry != 24001.5 { // 24000 + 1.0 + 0.5
			t.Fatalf("entry = %v, want 24001.5", tr.Entry)
		}
		if tr.RiskDistance != 24001.5-23940 {
			t.Fatalf("risk distance = %v", tr.RiskDistance)
		}
	})

	t.Run("duplicate client order id is idempotent", func(t *testing.T) {
		tr2, err := sim.Open(context.Background(), types.OrderRequest{
			Instrument: "DE30_EUR", Units: 10, OrderType: types.OrderTypeMarket,
			StopLoss: 23940, TakeProfit: 24180, ClientOrderID: "c1",
		})
		if err != nil {
			t.Fatal(err)
		}
		open, _ := sim.OpenTrades(context.Background())
		if len(open) != 1 {
			t.Fatalf("open trades = %d, want 1 (no duplicate)", len(open))
		}
		if tr2.TradeID != open[0].TradeID {
			t.Fatal("replay must return the same trade")
		}
	})

	t.Run("SL-first when candle touches both brackets", func(t *testing.T) {
		sim.OnCandle(types.Candle{
			Instrument: "DE30_EUR", Timeframe: types.M5,
			High: 24500, Low: 23900, Close: 24100, // spans both SL and TP
		})
		if len(closes) != 1 {
			t.Fatalf("closes = %d, want 1", len(closes))
		}
		if closes[0].ExitReason != "sl" {
			t.Fatalf("exit reason = %s, want sl (conservative)", closes[0].ExitReason)
		}
		if closes[0].ExitPrice != 23940-0.5 { // stop minus adverse slippage
			t.Fatalf("exit = %v, want 23939.5", closes[0].ExitPrice)
		}
		if closes[0].RealizedPL >= 0 {
			t.Fatalf("stop-out must lose money, pl = %v", closes[0].RealizedPL)
		}
	})

	t.Run("take profit fills at target", func(t *testing.T) {
		closes = nil
		_, err := sim.Open(context.Background(), types.OrderRequest{
			Instrument: "DE30_EUR", Units: 10, OrderType: types.OrderTypeMarket,
			StopLoss: 23940, TakeProfit: 24180, ClientOrderID: "c2",
		})
		if err != nil {
			t.Fatal(err)
		}
		sim.OnCandle(types.Candle{Instrument: "DE30_EUR", High: 24200, Low: 24100, Close: 24150})
		if len(closes) != 1 || closes[0].ExitReason != "tp" {
			t.Fatalf("closes = %+v, want single tp", closes)
		}
		if closes[0].ExitPrice != 24180 {
			t.Fatalf("exit = %v, want 24180", closes[0].ExitPrice)
		}
	})

	t.Run("breakeven stop reports breakeven reason", func(t *testing.T) {
		closes = nil
		tr, err := sim.Open(context.Background(), types.OrderRequest{
			Instrument: "DE30_EUR", Units: 10, OrderType: types.OrderTypeMarket,
			StopLoss: 23940, TakeProfit: 24180, ClientOrderID: "c3",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := sim.ModifyStop(context.Background(), tr.TradeID, tr.Entry); err != nil {
			t.Fatal(err)
		}
		sim.OnCandle(types.Candle{Instrument: "DE30_EUR", High: 24010, Low: 23990, Close: 24000})
		if len(closes) != 1 || closes[0].ExitReason != "breakeven" {
			t.Fatalf("closes = %+v, want breakeven exit", closes)
		}
	})

	t.Run("short entry and close cross the spread", func(t *testing.T) {
		closes = nil
		tr, err := sim.Open(context.Background(), types.OrderRequest{
			Instrument: "DE30_EUR", Units: -10, OrderType: types.OrderTypeMarket,
			StopLoss: 24060, TakeProfit: 23820, ClientOrderID: "c4",
		})
		if err != nil {
			t.Fatal(err)
		}
		if tr.Entry != 24000-1.5 {
			t.Fatalf("short entry = %v, want 23998.5", tr.Entry)
		}
		if err := sim.Close(context.Background(), tr.TradeID); err != nil {
			t.Fatal(err)
		}
		if len(closes) != 1 {
			t.Fatal("expected close")
		}
		// Short closed by buying back at mid + half spread + slippage.
		if closes[0].ExitPrice != 24001.5 {
			t.Fatalf("exit = %v, want 24001.5", closes[0].ExitPrice)
		}
	})
}
