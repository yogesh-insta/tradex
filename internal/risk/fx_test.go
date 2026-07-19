package risk

import (
	"testing"
	"time"

	"github.com/yogesh-insta/tradex/internal/calendar"
	"github.com/yogesh-insta/tradex/internal/config"
	"github.com/yogesh-insta/tradex/internal/execution"
	"github.com/yogesh-insta/tradex/pkg/types"
)

func fxHarness() *harness {
	h := defaultHarness()
	h.account.name = "fx-usdjpy"
	h.meta = execution.InstrumentMeta{
		Symbol: "USD_JPY", PricePrecision: 3, PipLocation: -2,
		MinUnits: 1, MarginRate: 0.03, PointValue: 0.01, UnitsPrecision: 0,
	}
	h.price = 150.25
	return h
}

func fxSignal() types.Signal {
	return types.Signal{
		Instrument: "USD_JPY",
		Strategy:   "fx_trld",
		Direction:  types.DirectionLong,
		OrderType:  types.OrderTypeMarket,
		EntryPrice: 150.40,
		StopLoss:   149.90, // 0.5 ATR-ish
		TakeProfit: 151.90,
		At:         time.Date(2026, 7, 15, 7, 30, 0, 0, time.UTC), // Wed 16:30 JST
	}
}

func TestFXGates(t *testing.T) {
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	cfg := testRiskConfig()
	cfg.NewsBlockBefore = config.Duration(60 * time.Minute)
	cfg.CorrelationGroups = nil

	t.Run("spread too wide", func(t *testing.T) {
		h := fxHarness()
		eng := h.engine(cfg)
		eng.WithFX(FXProfile{
			MaxSpreadPips: 1.5, PipSize: 0.01, RequireSpread: true,
			OneTradePerDay: true, SessionTZ: tokyo,
			FridayNoEntryTZ: ny, FridayHardFlattenTZ: ny,
		}, FXDeps{
			SpreadPips:  func(string) (float64, bool) { return 2.0, true },
			TradedToday: func(string, string) bool { return false },
		})
		_, err := eng.Evaluate(fxSignal(), time.Date(2026, 7, 15, 7, 30, 0, 0, time.UTC))
		if rejectionReason(t, err) != ReasonSpreadTooWide {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("already traded today", func(t *testing.T) {
		h := fxHarness()
		eng := h.engine(cfg)
		eng.WithFX(FXProfile{
			MaxSpreadPips: 1.5, PipSize: 0.01, RequireSpread: true,
			OneTradePerDay: true, SessionTZ: tokyo,
			FridayNoEntryTZ: ny, FridayHardFlattenTZ: ny,
		}, FXDeps{
			SpreadPips:  func(string) (float64, bool) { return 1.0, true },
			TradedToday: func(string, string) bool { return true },
		})
		_, err := eng.Evaluate(fxSignal(), time.Date(2026, 7, 15, 7, 30, 0, 0, time.UTC))
		if rejectionReason(t, err) != ReasonAlreadyTradedToday {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("friday entry cut", func(t *testing.T) {
		h := fxHarness()
		eng := h.engine(cfg)
		eng.WithFX(FXProfile{
			MaxSpreadPips: 1.5, PipSize: 0.01, RequireSpread: true,
			FridayNoEntry: "12:00:00", FridayNoEntryTZ: ny,
			FridayHardFlattenTZ: ny, SessionTZ: tokyo,
		}, FXDeps{
			SpreadPips: func(string) (float64, bool) { return 1.0, true },
		})
		// Friday 2026-07-17 13:00 America/New_York
		now := time.Date(2026, 7, 17, 17, 0, 0, 0, time.UTC) // 13:00 EDT
		_, err := eng.Evaluate(fxSignal(), now)
		if rejectionReason(t, err) != ReasonFridayEntryCut {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("accepts when clear", func(t *testing.T) {
		h := fxHarness()
		h.news = calendar.NoImminent
		eng := h.engine(cfg)
		eng.WithFX(FXProfile{
			MaxSpreadPips: 1.5, PipSize: 0.01, RequireSpread: true,
			OneTradePerDay: true, SessionTZ: tokyo,
			FridayNoEntry: "12:00:00", FridayNoEntryTZ: ny,
			FridayHardFlatten: "16:00:00", FridayHardFlattenTZ: ny,
		}, FXDeps{
			SpreadPips:  func(string) (float64, bool) { return 1.0, true },
			TradedToday: func(string, string) bool { return false },
		})
		req, err := eng.Evaluate(fxSignal(), time.Date(2026, 7, 15, 7, 30, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		if req.Instrument != "USD_JPY" || req.Units <= 0 {
			t.Fatalf("%+v", req)
		}
	})
}
