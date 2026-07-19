package backtest

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/yogesh-insta/tradex/internal/calendar"
	"github.com/yogesh-insta/tradex/internal/candles"
	"github.com/yogesh-insta/tradex/internal/config"
	"github.com/yogesh-insta/tradex/internal/controlplane"
	"github.com/yogesh-insta/tradex/internal/execution"
	"github.com/yogesh-insta/tradex/internal/portfolio"
	"github.com/yogesh-insta/tradex/internal/risk"
	"github.com/yogesh-insta/tradex/internal/session"
	"github.com/yogesh-insta/tradex/internal/strategy"
	"github.com/yogesh-insta/tradex/internal/strategy/eulove"
	"github.com/yogesh-insta/tradex/internal/trademgmt"
	"github.com/yogesh-insta/tradex/pkg/types"
)

// InstrumentData holds the replay series for one instrument.
type InstrumentData struct {
	M5    []types.Candle
	H1    []types.Candle
	Daily []types.Candle
}

// DefaultSimMeta is the instrument metadata used when the run has no live
// OANDA connection (index CFD defaults: 1-decimal price, pip = 1.0 point,
// 5% margin, 1 quote-currency unit per point per unit).
var DefaultSimMeta = execution.InstrumentMeta{
	PricePrecision: 1,
	PipLocation:    0,
	UnitsPrecision: 1,   // OANDA index CFDs trade in 0.1-unit steps
	MinUnits:       0.1, // minimumTradeSize for index CFDs
	MarginRate:     0.05,
	PointValue:     1.0,
}

// Params configures one backtest run.
type Params struct {
	Cfg      *config.Config
	Data     map[string]InstrumentData // instrument -> series
	Log      *slog.Logger
	Holidays *calendar.Holidays                  // optional
	Calendar *calendar.Cache                     // optional; nil = no news blackouts
	Meta     map[string]execution.InstrumentMeta // optional; DefaultSimMeta if absent
}

// Result carries the run outputs.
type Result struct {
	Metrics Metrics
	Closed  []ClosedTrade
}

type replayEvent struct {
	closeTime time.Time
	candle    types.Candle
}

// Run replays the data through the live strategy + risk + management path.
func Run(ctx context.Context, p Params) (Result, error) {
	cfg := p.Cfg
	log := p.Log

	// --- simulation clock ---
	var simNow time.Time
	now := func() time.Time { return simNow }

	// --- history source (bounded at simNow: no lookahead) ---
	hist := NewHistorySource(now)
	instruments := make([]string, 0, len(p.Data))
	for inst, d := range p.Data {
		instruments = append(instruments, inst)
		hist.Add(inst, "M5", d.M5)
		hist.Add(inst, "H1", d.H1)
		hist.Add(inst, "D", d.Daily)
	}
	sort.Strings(instruments)

	// --- session controller (same code as live) ---
	sessCfg := session.Config{
		TZ:               cfg.EUSession.TZ,
		RangeStart:       cfg.EUSession.RangeStart,
		RangeEnd:         cfg.EUSession.RangeEnd,
		TradeWindowStart: cfg.EUSession.TradeWindowStart,
		ATRPeriodDays:    cfg.EUSession.ATRPeriodDays,
		VolMACandles:     cfg.EUSession.VolMACandles,
		Instruments:      instruments,
	}
	sess, err := session.NewController(sessCfg, hist, p.Holidays, log)
	if err != nil {
		return Result{}, err
	}

	// --- strategy (same code as live) ---
	loc := sess.Location()
	strat, err := eulove.New(eulove.Config{
		VolumeSpikeMult:  cfg.Strategies.EULove.VolumeSpikeMult,
		SLATRMult:        cfg.Strategies.EULove.SLATRMult,
		TPATRMult:        cfg.Strategies.EULove.TPATRMult,
		BreakevenAtR:     cfg.Strategies.EULove.BreakevenAtR,
		TradeWindowStart: cfg.EUSession.TradeWindowStart,
		EntryWindowEnd:   cfg.Strategies.EULove.EntryWindowEnd,
		FridayCutoff:     cfg.Mgmt.EUFridayCutoff,
		DailyCutoff:      cfg.Mgmt.EUDailyCutoff,
		Location:         loc,
	})
	if err != nil {
		return Result{}, err
	}
	router := strategy.NewRouter()
	for _, inst := range instruments {
		if err := router.Assign(inst, strat); err != nil {
			return Result{}, err
		}
	}

	// --- virtual account + machine ---
	acct := portfolio.NewAccount("backtest", "SIM")
	acct.SetEquity(cfg.Backtest.InitialEquity)
	machine := controlplane.NewMachine(types.StateActive, log, nil)

	// --- sim executor + fill model ---
	lastMid := map[string]float64{}
	price := func(inst string) float64 { return lastMid[inst] }
	var closed []ClosedTrade
	meta := map[string]execution.InstrumentMeta{}
	for _, inst := range instruments {
		m, ok := p.Meta[inst]
		if !ok {
			m = DefaultSimMeta
			m.Symbol = inst
		}
		meta[inst] = m
	}
	var engine *risk.Engine
	sim := NewSimExecutor(meta, cfg.Backtest.SpreadPoints, cfg.Backtest.SlippagePoints, now, price, func(ct ClosedTrade) {
		closed = append(closed, ct)
		acct.RecordClose(ct.RealizedPL)
		acct.SetEquity(acct.Equity() + ct.RealizedPL)
		engine.CheckBreakers()
	})

	// --- risk (same code as live) ---
	timeToNews := func(region string, at time.Time) time.Duration {
		if p.Calendar == nil {
			return calendar.NoImminent
		}
		return p.Calendar.TimeToHighImpact(region, at)
	}
	engine = risk.NewEngine(cfg.Risk, acct, "EU", risk.Deps{
		SystemState: machine.State,
		ForceLock:   machine.ForceLock,
		Stale:       func(string, time.Time) bool { return false },
		TimeToNews:  timeToNews,
		OpenTrades: func() []types.OpenTrade {
			ts, _ := sim.OpenTrades(ctx)
			return ts
		},
		Meta:  sim.Instrument,
		Price: price,
	})

	// --- management loop (same code as live; Pass driven by the replay) ---
	mgmt := trademgmt.NewLoop(trademgmt.Config{
		TickInterval:      time.Second, // unused: Pass is called directly
		ReconcileInterval: 0,           // reconcile on every pass
		NewsBlockBefore:   cfg.Mgmt.NewsBlockBefore.D(),
	}, trademgmt.Deps{
		Executor:    sim,
		Price:       price,
		TimeToNews:  timeToNews,
		Region:      func(string) string { return "EU" },
		SystemState: machine.State,
		Now:         now,
	}, log)

	// --- replay timeline: M5 + H1 merged by close time (H1 first on ties,
	//     so the authoritative range lands before the M5 decision) ---
	var events []replayEvent
	for _, inst := range instruments {
		d := p.Data[inst]
		for _, c := range d.M5 {
			events = append(events, replayEvent{c.Start.Add(types.M5.Duration()), c})
		}
		for _, c := range d.H1 {
			events = append(events, replayEvent{c.Start.Add(types.H1.Duration()), c})
		}
	}
	sort.SliceStable(events, func(i, j int) bool {
		if !events[i].closeTime.Equal(events[j].closeTime) {
			return events[i].closeTime.Before(events[j].closeTime)
		}
		return events[i].candle.Timeframe == types.H1 && events[j].candle.Timeframe == types.M5
	})

	rings := map[string]*candles.Ring{}
	ringFor := func(inst string, tf types.Timeframe) *candles.Ring {
		k := inst + "|" + string(tf)
		if rings[k] == nil {
			rings[k] = candles.NewRing(cfg.Candles.WindowMax)
		}
		return rings[k]
	}

	curDay := ""
	for _, e := range events {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		c := e.candle
		simNow = e.closeTime

		// New session day: snapshot baseline, refresh ATR, and (backtest-only)
		// re-arm a locked breaker — simulating the operator's daily RE_ARM so
		// one bad day doesn't blank the rest of the dataset.
		if day := simNow.In(loc).Format("2006-01-02"); day != curDay {
			curDay = day
			acct.SnapshotBaseline()
			if machine.State() == types.StateSystemLock {
				_, _ = machine.ReArm()
			}
			if err := sess.RefreshATR(ctx); err != nil {
				log.Debug("ATR refresh failed (not enough history yet)", "error", err)
			}
		}

		if c.Timeframe == types.M5 {
			lastMid[c.Instrument] = c.Close
			sim.OnCandle(c) // bracket exits settle before decisions
		}

		ring := ringFor(c.Instrument, c.Timeframe)
		ring.Upsert(c)
		ev := types.MarketEvent{
			Instrument: c.Instrument,
			Now:        simNow,
			Timeframe:  c.Timeframe,
			Last:       c,
			Window:     ring.Window(),
			Price:      lastMid[c.Instrument],
		}
		sess.OnCandle(ev)
		mgmt.Pass(ctx)

		if c.Timeframe != types.M5 {
			continue
		}
		st, ok := sess.State(c.Instrument, simNow)
		if !ok {
			continue
		}
		sig := router.Dispatch(ev, st)
		if sig == nil {
			continue
		}
		req, err := engine.Evaluate(*sig, simNow)
		if err != nil {
			log.Debug("signal rejected", "instrument", c.Instrument, "error", err)
			continue
		}
		if _, err := sim.Open(ctx, req); err != nil {
			log.Warn("sim open failed", "error", err)
		}
	}

	// Close anything still open at the end of the data at the last price.
	for _, t := range mustTrades(sim, ctx) {
		_ = sim.Close(ctx, t.TradeID)
	}

	sort.Slice(closed, func(i, j int) bool { return closed[i].ClosedAt.Before(closed[j].ClosedAt) })
	return Result{
		Metrics: Compute(cfg.Backtest.InitialEquity, closed),
		Closed:  closed,
	}, nil
}

func mustTrades(sim *SimExecutor, ctx context.Context) []types.OpenTrade {
	ts, err := sim.OpenTrades(ctx)
	if err != nil {
		return nil
	}
	return ts
}

// FormatTrades renders the closed-trade list for stdout.
func FormatTrades(closed []ClosedTrade) string {
	out := ""
	for _, ct := range closed {
		out += fmt.Sprintf("%s  %-9s %-6s units=%-6.1f entry=%.1f exit=%.1f (%s)  pl=%.2f\n",
			ct.ClosedAt.UTC().Format("2006-01-02 15:04"),
			ct.Trade.Instrument, ct.Trade.Direction(), ct.Trade.Units,
			ct.Trade.Entry, ct.ExitPrice, ct.ExitReason, ct.RealizedPL)
	}
	return out
}
