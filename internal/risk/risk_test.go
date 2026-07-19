package risk

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/yogesh-insta/tradex/internal/calendar"
	"github.com/yogesh-insta/tradex/internal/config"
	"github.com/yogesh-insta/tradex/internal/execution"
	"github.com/yogesh-insta/tradex/pkg/types"
)

type fakeAccount struct {
	name         string
	equity       float64
	known        bool
	dailyPL      float64
	consecLosses int
}

func (a *fakeAccount) Name() string             { return a.name }
func (a *fakeAccount) Equity() float64          { return a.equity }
func (a *fakeAccount) EquityKnown() bool        { return a.known }
func (a *fakeAccount) DailyRealizedPL() float64 { return a.dailyPL }
func (a *fakeAccount) ConsecutiveLosses() int   { return a.consecLosses }

type harness struct {
	account *fakeAccount
	state   types.SystemState
	locked  string // ForceLock reason, "" = not called
	stale   bool
	news    time.Duration
	open    []types.OpenTrade
	meta    execution.InstrumentMeta
	price   float64
}

func defaultHarness() *harness {
	return &harness{
		account: &fakeAccount{name: "eu-indices", equity: 5000, known: true},
		state:   types.StateActive,
		news:    calendar.NoImminent,
		meta: execution.InstrumentMeta{
			Symbol: "DE30_EUR", PricePrecision: 1, PipLocation: 0,
			MinUnits: 1, MarginRate: 0.05, PointValue: 1.0,
		},
		price: 100,
	}
}

func testRiskConfig() config.RiskConfig {
	return config.RiskConfig{
		RiskPerTrade:        0.01,
		DailyLossLimit:      150,
		ConsecutiveLossHalt: 3,
		MaxConcurrent:       1,
		MaxMarginFrac:       0.10,
		MaxLeverage:         5.0,
		NewsBlockBefore:     config.Duration(30 * time.Minute),
		CorrelationGroups:   [][]string{{"DE30_EUR", "FR40_EUR"}},
	}
}

func (h *harness) engine(cfg config.RiskConfig) *Engine {
	return NewEngine(cfg, h.account, "EU", Deps{
		SystemState: func() types.SystemState { return h.state },
		ForceLock:   func(reason string) { h.locked = reason },
		Stale:       func(string, time.Time) bool { return h.stale },
		TimeToNews:  func(string, time.Time) time.Duration { return h.news },
		OpenTrades:  func() []types.OpenTrade { return h.open },
		Meta:        func(string) (execution.InstrumentMeta, error) { return h.meta, nil },
		Price:       func(string) float64 { return h.price },
	})
}

// longSignal: entry 100, SL 95 (5-point stop), TP 115.
func longSignal() types.Signal {
	return types.Signal{
		Instrument: "DE30_EUR",
		Strategy:   "eu_love",
		Direction:  types.DirectionLong,
		OrderType:  types.OrderTypeMarket,
		EntryPrice: 100,
		StopLoss:   95,
		TakeProfit: 115,
		At:         time.Date(2026, 7, 15, 9, 30, 0, 0, time.UTC),
	}
}

func rejectionReason(t *testing.T, err error) string {
	t.Helper()
	var rej *Rejection
	if !errors.As(err, &rej) {
		t.Fatalf("expected *Rejection, got %v", err)
	}
	return rej.Reason
}

func TestGateChain(t *testing.T) {
	now := time.Date(2026, 7, 15, 7, 30, 0, 0, time.UTC)
	tests := []struct {
		name       string
		mutate     func(*harness)
		wantReason string
		wantLock   string // expected ForceLock reason ("" = must not lock)
	}{
		{
			name:       "system paused",
			mutate:     func(h *harness) { h.state = types.StatePaused },
			wantReason: ReasonSystemNotActive,
		},
		{
			name:       "system locked",
			mutate:     func(h *harness) { h.state = types.StateSystemLock },
			wantReason: ReasonSystemNotActive,
		},
		{
			name:       "system disabled",
			mutate:     func(h *harness) { h.state = types.StateDisabled },
			wantReason: ReasonSystemNotActive,
		},
		{
			name:       "account state unknown",
			mutate:     func(h *harness) { h.account.known = false },
			wantReason: ReasonAccountStateUnknown,
		},
		{
			name:       "daily loss breaker at exactly -150",
			mutate:     func(h *harness) { h.account.dailyPL = -150 },
			wantReason: ReasonDailyLossBreaker,
			wantLock:   ReasonDailyLossBreaker,
		},
		{
			name:       "stale market data",
			mutate:     func(h *harness) { h.stale = true },
			wantReason: ReasonStaleMarketData,
		},
		{
			name:       "news blackout inside 30m",
			mutate:     func(h *harness) { h.news = 20 * time.Minute },
			wantReason: ReasonNewsBlackout,
		},
		{
			name:       "calendar fail-safe (unknown => imminent)",
			mutate:     func(h *harness) { h.news = 0 },
			wantReason: ReasonNewsBlackout,
		},
		{
			name: "already open on same instrument",
			mutate: func(h *harness) {
				h.open = []types.OpenTrade{{TradeID: "1", Instrument: "DE30_EUR", Units: 1}}
			},
			wantReason: ReasonAlreadyOpen,
		},
		{
			name: "correlated instrument open (FR40 blocks DE30)",
			mutate: func(h *harness) {
				h.open = []types.OpenTrade{{TradeID: "2", Instrument: "FR40_EUR", Units: 1}}
			},
			wantReason: ReasonCorrelatedOpen,
		},
		{
			name: "max concurrent reached by uncorrelated instrument",
			mutate: func(h *harness) {
				h.open = []types.OpenTrade{{TradeID: "3", Instrument: "XAU_USD", Units: 1}}
			},
			wantReason: ReasonMaxConcurrent,
		},
		{
			name:       "consecutive loss halt",
			mutate:     func(h *harness) { h.account.consecLosses = 3 },
			wantReason: ReasonConsecutiveLossHalt,
			wantLock:   ReasonConsecutiveLossHalt,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := defaultHarness()
			tt.mutate(h)
			eng := h.engine(testRiskConfig())
			_, err := eng.Evaluate(longSignal(), now)
			if err == nil {
				t.Fatal("expected rejection")
			}
			if got := rejectionReason(t, err); got != tt.wantReason {
				t.Fatalf("reason = %s, want %s", got, tt.wantReason)
			}
			if h.locked != tt.wantLock {
				t.Fatalf("ForceLock = %q, want %q", h.locked, tt.wantLock)
			}
		})
	}
}

func TestSizing(t *testing.T) {
	now := time.Now()
	t.Run("basic 1% sizing", func(t *testing.T) {
		h := defaultHarness()
		eng := h.engine(testRiskConfig())
		// equity 5000 * 1% = 50 risk; stop 5 points × pointValue 1 → 10 units.
		req, err := eng.Evaluate(longSignal(), now)
		if err != nil {
			t.Fatal(err)
		}
		if req.Units != 10 {
			t.Fatalf("units = %g, want 10", req.Units)
		}
		// A stop-out loses ≈ riskCapital: 10 units × 5 points = $50.
		loss := float64(req.Units) * (req.StopLoss - 100) // negative
		if loss != -50 {
			t.Fatalf("stop-out loss = %.2f, want -50", loss)
		}
		if req.ClientOrderID != "eu_love-DE30_EUR-20260715-0930" {
			t.Fatalf("client order id = %s", req.ClientOrderID)
		}
		if req.Account != "eu-indices" {
			t.Fatalf("account = %s", req.Account)
		}
	})

	t.Run("short sizing carries negative units", func(t *testing.T) {
		h := defaultHarness()
		eng := h.engine(testRiskConfig())
		sig := longSignal()
		sig.Direction = types.DirectionShort
		sig.StopLoss = 105
		sig.TakeProfit = 85
		req, err := eng.Evaluate(sig, now)
		if err != nil {
			t.Fatal(err)
		}
		if req.Units != -10 {
			t.Fatalf("units = %g, want -10", req.Units)
		}
	})

	t.Run("fractional CFD sizing (DE30-realistic)", func(t *testing.T) {
		h := defaultHarness()
		h.meta.UnitsPrecision = 1 // 0.1-unit steps
		h.meta.MinUnits = 0.1
		h.price = 24000
		eng := h.engine(testRiskConfig())
		sig := longSignal()
		// ATR 120 → 0.5×ATR = 60-point stop; $50 risk / 60 = 0.8333 → 0.8 by
		// risk. The margin gate then scales down: 10% of $5,000 = $500 margin
		// cap at 5% rate → max notional $10,000 → 0.4 units at 24,000.
		sig.EntryPrice, sig.StopLoss, sig.TakeProfit = 24000, 23940, 24180
		req, err := eng.Evaluate(sig, now)
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(req.Units-0.4) > 1e-9 {
			t.Fatalf("units = %g, want 0.4 (margin-gated)", req.Units)
		}
	})

	t.Run("pip location scales stop points", func(t *testing.T) {
		h := defaultHarness()
		h.meta.PipLocation = -1 // point = 0.1
		eng := h.engine(testRiskConfig())
		// stop 5.0 price units = 50 points → floor(50/50) = 1 unit.
		req, err := eng.Evaluate(longSignal(), now)
		if err != nil {
			t.Fatal(err)
		}
		if req.Units != 1 {
			t.Fatalf("units = %g, want 1", req.Units)
		}
	})

	t.Run("margin gate scales units down", func(t *testing.T) {
		h := defaultHarness()
		h.meta.MarginRate = 0.5 // absurd margin to force the gate
		eng := h.engine(testRiskConfig())
		// unconstrained: 10 units → margin 10×100×0.5 = 500 = 10% of 5000 (allowed:
		// margin <= 500). Leverage 1000/5000 fine. 10 units passes exactly.
		req, err := eng.Evaluate(longSignal(), now)
		if err != nil {
			t.Fatal(err)
		}
		if req.Units != 10 {
			t.Fatalf("units = %g, want 10", req.Units)
		}
		// Raise the margin rate: 10 units → margin 10×100×0.7 = 700 > 500 →
		// scale down to 7 units (7×100×0.7 = 490 <= 500).
		h.meta.MarginRate = 0.7
		req, err = eng.Evaluate(longSignal(), now)
		if err != nil {
			t.Fatal(err)
		}
		if req.Units != 7 {
			t.Fatalf("units = %g, want 7", req.Units)
		}
	})

	t.Run("leverage gate rejects when even min size violates", func(t *testing.T) {
		h := defaultHarness()
		h.account.equity = 5000
		h.price = 30000 // one unit notional 30000 → leverage 6:1 > 5:1
		eng := h.engine(testRiskConfig())
		sig := longSignal()
		sig.EntryPrice, sig.StopLoss, sig.TakeProfit = 30000, 29950, 30150
		_, err := eng.Evaluate(sig, now)
		if err == nil {
			t.Fatal("expected margin_gate rejection")
		}
		if got := rejectionReason(t, err); got != ReasonMarginGate {
			t.Fatalf("reason = %s, want %s", got, ReasonMarginGate)
		}
	})

	t.Run("size too small", func(t *testing.T) {
		h := defaultHarness()
		h.meta.MinUnits = 100
		eng := h.engine(testRiskConfig())
		_, err := eng.Evaluate(longSignal(), now)
		if got := rejectionReason(t, err); got != ReasonSizeTooSmall {
			t.Fatalf("reason = %s, want %s", got, ReasonSizeTooSmall)
		}
	})
}

func TestCheckBreakers(t *testing.T) {
	h := defaultHarness()
	eng := h.engine(testRiskConfig())

	h.account.dailyPL = -149.99
	eng.CheckBreakers()
	if h.locked != "" {
		t.Fatalf("locked prematurely at %.2f", h.account.dailyPL)
	}
	h.account.dailyPL = -150
	eng.CheckBreakers()
	if h.locked != ReasonDailyLossBreaker {
		t.Fatalf("expected daily loss lock, got %q", h.locked)
	}

	h2 := defaultHarness()
	eng2 := h2.engine(testRiskConfig())
	h2.account.consecLosses = 3
	eng2.CheckBreakers()
	if h2.locked != ReasonConsecutiveLossHalt {
		t.Fatalf("expected consecutive loss lock, got %q", h2.locked)
	}
}
