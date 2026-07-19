package trademgmt

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/yogesh-insta/tradex/internal/calendar"
	"github.com/yogesh-insta/tradex/internal/execution"
	"github.com/yogesh-insta/tradex/pkg/types"
)

func discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

type modify struct {
	id    string
	price float64
}

type fakeExec struct {
	mu       sync.Mutex
	trades   map[string]types.OpenTrade
	modifies []modify
	closes   []string
}

func newFakeExec(trades ...types.OpenTrade) *fakeExec {
	f := &fakeExec{trades: map[string]types.OpenTrade{}}
	for _, t := range trades {
		f.trades[t.TradeID] = t
	}
	return f
}

func (f *fakeExec) Open(context.Context, types.OrderRequest) (types.OpenTrade, error) {
	return types.OpenTrade{}, nil
}

func (f *fakeExec) ModifyStop(_ context.Context, id string, price float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.modifies = append(f.modifies, modify{id, price})
	if t, ok := f.trades[id]; ok {
		t.CurrentSL = price
		f.trades[id] = t
	}
	return nil
}

func (f *fakeExec) Close(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes = append(f.closes, id)
	delete(f.trades, id)
	return nil
}

func (f *fakeExec) CancelOrder(context.Context, string) error { return nil }

func (f *fakeExec) OpenTrades(context.Context) ([]types.OpenTrade, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]types.OpenTrade, 0, len(f.trades))
	for _, t := range f.trades {
		out = append(out, t)
	}
	return out, nil
}

func (f *fakeExec) Instrument(string) (execution.InstrumentMeta, error) {
	return execution.InstrumentMeta{PricePrecision: 1}, nil
}

func longTrade() types.OpenTrade {
	return types.OpenTrade{
		TradeID:      "t1",
		Instrument:   "DE30_EUR",
		Units:        10,
		Entry:        24000,
		CurrentSL:    23940,
		CurrentTP:    24180,
		RiskDistance: 60,
		Policy:       types.ManagementPolicy{BreakevenAtR: 1.0},
	}
}

type deps struct {
	price float64
	news  time.Duration
	state types.SystemState
	now   time.Time
}

func newLoop(f *fakeExec, d *deps, reconcile time.Duration) *Loop {
	return NewLoop(Config{
		TickInterval:      time.Second,
		ReconcileInterval: reconcile,
		NewsBlockBefore:   30 * time.Minute,
	}, Deps{
		Executor:    f,
		Price:       func(string) float64 { return d.price },
		TimeToNews:  func(string, time.Time) time.Duration { return d.news },
		Region:      func(string) string { return "EU" },
		SystemState: func() types.SystemState { return d.state },
		Now:         func() time.Time { return d.now },
	}, discard())
}

func TestBreakevenLevelTriggeredIdempotent(t *testing.T) {
	f := newFakeExec(longTrade())
	d := &deps{price: 24030, news: calendar.NoImminent, state: types.StateActive, now: time.Now()}
	l := newLoop(f, d, time.Hour)
	ctx := context.Background()

	l.Pass(ctx) // seeds reconcile; +0.5R: no action yet
	if len(f.modifies) != 0 {
		t.Fatalf("modified at +0.5R: %+v", f.modifies)
	}

	d.price = 24060 // exactly +1R
	l.Pass(ctx)
	if len(f.modifies) != 1 || f.modifies[0] != (modify{"t1", 24000}) {
		t.Fatalf("modifies = %+v, want one SL→entry", f.modifies)
	}

	l.Pass(ctx) // level-triggered: already applied → no second modify
	l.Pass(ctx)
	if len(f.modifies) != 1 {
		t.Fatalf("modifies = %d, want 1 (idempotent)", len(f.modifies))
	}
}

func TestBreakevenShort(t *testing.T) {
	short := longTrade()
	short.TradeID = "s1"
	short.Units = -10
	short.CurrentSL = 24060
	f := newFakeExec(short)
	d := &deps{price: 23940, news: calendar.NoImminent, state: types.StateActive, now: time.Now()}
	l := newLoop(f, d, time.Hour)
	l.Pass(context.Background()) // entry 24000, price 23940 → +1R for short
	if len(f.modifies) != 1 || f.modifies[0].price != 24000 {
		t.Fatalf("modifies = %+v, want SL→24000", f.modifies)
	}
}

func TestTimeCutoffFlattens(t *testing.T) {
	now := time.Date(2026, 7, 17, 15, 30, 0, 5, time.UTC) // just past cutoff
	tr := longTrade()
	tr.Policy.TimeCutoff = time.Date(2026, 7, 17, 15, 30, 0, 0, time.UTC) // 17:30 Berlin
	f := newFakeExec(tr)
	d := &deps{price: 24010, news: calendar.NoImminent, state: types.StateActive, now: now}
	l := newLoop(f, d, time.Hour)
	l.Pass(context.Background())
	if len(f.closes) != 1 || f.closes[0] != "t1" {
		t.Fatalf("closes = %+v, want [t1]", f.closes)
	}

	// Before the cutoff nothing happens.
	f2 := newFakeExec(longTrade())
	tr2 := longTrade()
	tr2.Policy.TimeCutoff = now.Add(time.Hour)
	f2.trades["t1"] = tr2
	d2 := &deps{price: 24010, news: calendar.NoImminent, state: types.StateActive, now: now}
	l2 := newLoop(f2, d2, time.Hour)
	l2.Pass(context.Background())
	if len(f2.closes) != 0 {
		t.Fatalf("closed before cutoff: %+v", f2.closes)
	}
}

func TestNewsFlattenMovesStopEvenWithoutProfit(t *testing.T) {
	f := newFakeExec(longTrade())
	// Price below entry (losing) but CPI in 20 minutes.
	d := &deps{price: 23980, news: 20 * time.Minute, state: types.StateActive, now: time.Now()}
	l := newLoop(f, d, time.Hour)
	l.Pass(context.Background())
	if len(f.modifies) != 1 || f.modifies[0].price != 24000 {
		t.Fatalf("modifies = %+v, want SL→entry on news", f.modifies)
	}

	// Fail-safe: unknown calendar (0) behaves the same.
	f2 := newFakeExec(longTrade())
	d2 := &deps{price: 23980, news: 0, state: types.StateActive, now: time.Now()}
	l2 := newLoop(f2, d2, time.Hour)
	l2.Pass(context.Background())
	if len(f2.modifies) != 1 {
		t.Fatalf("fail-safe modifies = %+v", f2.modifies)
	}
}

func TestVanishedTradeReported(t *testing.T) {
	f := newFakeExec(longTrade())
	d := &deps{price: 24010, news: calendar.NoImminent, state: types.StateActive, now: time.Now()}
	var vanished []string
	l := NewLoop(Config{TickInterval: time.Second, ReconcileInterval: 0, NewsBlockBefore: 30 * time.Minute},
		Deps{
			Executor:    f,
			Price:       func(string) float64 { return d.price },
			TimeToNews:  func(string, time.Time) time.Duration { return d.news },
			Region:      func(string) string { return "EU" },
			SystemState: func() types.SystemState { return d.state },
			OnVanished:  func(t types.OpenTrade) { vanished = append(vanished, t.TradeID) },
			Now:         func() time.Time { return d.now },
		}, discard())
	ctx := context.Background()
	l.Pass(ctx) // seed
	// Bracket exit at the broker: the trade disappears.
	f.mu.Lock()
	delete(f.trades, "t1")
	f.mu.Unlock()
	l.Pass(ctx)
	if len(vanished) != 1 || vanished[0] != "t1" {
		t.Fatalf("vanished = %+v, want [t1]", vanished)
	}
}

func TestDisabledStateStopsManagement(t *testing.T) {
	f := newFakeExec(longTrade())
	d := &deps{price: 24060, news: calendar.NoImminent, state: types.StateDisabled, now: time.Now()}
	l := newLoop(f, d, time.Hour)
	l.Pass(context.Background())
	if len(f.modifies) != 0 || len(f.closes) != 0 {
		t.Fatal("disabled state must not manage trades")
	}
}

func TestPausedStateStillManages(t *testing.T) {
	f := newFakeExec(longTrade())
	d := &deps{price: 24060, news: calendar.NoImminent, state: types.StatePaused, now: time.Now()}
	l := newLoop(f, d, time.Hour)
	l.Pass(context.Background())
	if len(f.modifies) != 1 {
		t.Fatal("paused trades must still be managed to their exit (spec 09)")
	}
}
