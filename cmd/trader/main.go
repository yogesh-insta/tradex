// Command trader is the live engine: the single Go binary on the VM that owns
// the entire trade hot path (market data → candles → session → strategy →
// risk → executor → trade management) plus the control-plane webhook and the
// housekeeping scheduler. It is the ONLY writer to OANDA.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/yogesh-insta/tradex/internal/calendar"
	"github.com/yogesh-insta/tradex/internal/candles"
	"github.com/yogesh-insta/tradex/internal/config"
	"github.com/yogesh-insta/tradex/internal/controlplane"
	"github.com/yogesh-insta/tradex/internal/execution"
	"github.com/yogesh-insta/tradex/internal/logging"
	"github.com/yogesh-insta/tradex/internal/marketdata"
	"github.com/yogesh-insta/tradex/internal/metrics"
	"github.com/yogesh-insta/tradex/internal/oanda"
	"github.com/yogesh-insta/tradex/internal/portfolio"
	"github.com/yogesh-insta/tradex/internal/risk"
	"github.com/yogesh-insta/tradex/internal/scheduler"
	"github.com/yogesh-insta/tradex/internal/session"
	"github.com/yogesh-insta/tradex/internal/strategy"
	"github.com/yogesh-insta/tradex/internal/strategy/eulove"
	"github.com/yogesh-insta/tradex/internal/strategy/fxtrld"
	"github.com/yogesh-insta/tradex/internal/trademgmt"
	"github.com/yogesh-insta/tradex/pkg/types"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	var configPath string
	flag.StringVar(&configPath, "config", "config/config.dev.yaml", "path to environment config YAML")
	flag.Parse()

	cfg, err := config.Load(configPath, config.EnvResolver{})
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config validation failed:\n%w", err)
	}

	log := logging.New("trader", cfg.Env, slog.LevelInfo)
	log.Info("effective config", "config", cfg.Redacted())
	mets := metrics.NewMemory()
	pub := logging.NewLogPublisher(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// --- OANDA client (host from config: fxpractice vs fxtrade) ---
	client := oanda.NewClient(cfg.OANDA.Host, cfg.OANDA.StreamHost, cfg.OANDA.Token, oanda.Options{
		RequestTimeout: cfg.Executor.RequestTimeout.D(),
		MaxRetries:     cfg.Executor.MaxRetries,
		BackoffBase:    cfg.Executor.RetryBackoffBase.D(),
	})

	// --- calendars ---
	holidays, err := calendar.LoadHolidays(cfg.Calendar.Holidays.File)
	if err != nil {
		return err
	}
	if holidays.Year() != time.Now().Year() {
		log.Warn("holiday calendar year mismatch — validate against exchange calendars",
			"file_year", holidays.Year(), "current_year", time.Now().Year())
	}
	var calProvider calendar.Provider
	if cfg.Calendar.Economic.Provider == "gcs" {
		calProvider = calendar.GCSProvider{Object: cfg.Calendar.Economic.GCSObject}
	} else {
		calProvider = calendar.FileProvider{Path: cfg.Calendar.Economic.StateFile}
	}
	calCache := calendar.NewCache(calProvider, cfg.Calendar.Economic.StalenessMax.D())
	if err := calCache.Refresh(); err != nil {
		log.Warn("calendar state unavailable at boot — fail-safe (entries blocked near unknown events)", "error", err)
	}

	// --- portfolio: accounts → instruments → strategies ---
	pm, err := portfolio.NewManager(cfg)
	if err != nil {
		return err
	}

	// --- control plane state machine ---
	machine := controlplane.NewMachine(types.StateActive, log, func(from, to types.SystemState, reason string) {
		mets.Set("system_state_"+string(to), 1)
		pub.Publish(types.TradeEvent{Type: "state_change", Reason: fmt.Sprintf("%s -> %s: %s", from, to, reason), At: time.Now().UTC()})
	})

	// --- market data: one snapshot; one pricing stream per account ---
	snapshot := marketdata.NewSnapshot()
	consumer := marketdata.NewConsumer(snapshot, cfg.Stream.TickChannelBuf, cfg.Stream.StaleHalt.D())

	// --- candle builder ---
	tfs := map[string][]types.Timeframe{}
	for inst, list := range cfg.Candles.Timeframes {
		for _, tf := range list {
			tfs[inst] = append(tfs[inst], types.Timeframe(tf))
		}
	}
	builder := candles.NewBuilder(candles.Config{
		Timeframes:     tfs,
		WindowMax:      cfg.Candles.WindowMax,
		ConfirmDelay:   cfg.Candles.RESTConfirmDelay.D(),
		ConfirmTimeout: cfg.Candles.RESTConfirmTimeout.D(),
	}, client, log)

	// --- session controllers (EU + optional FX; never share mutable state) ---
	sessHub := session.NewHub()
	euSess, err := session.NewController(session.Config{
		TZ:               cfg.EUSession.TZ,
		RangeStart:       cfg.EUSession.RangeStart,
		RangeEnd:         cfg.EUSession.RangeEnd,
		TradeWindowStart: cfg.EUSession.TradeWindowStart,
		ATRPeriodDays:    cfg.EUSession.ATRPeriodDays,
		VolMACandles:     cfg.EUSession.VolMACandles,
		Instruments:      cfg.EUSession.Instruments,
		Markets:          map[string]string{"DE30_EUR": "XETR", "FR40_EUR": "XPAR"},
	}, client, holidays, log)
	if err != nil {
		return err
	}
	if err := sessHub.Add(euSess); err != nil {
		return err
	}

	fxActive := false
	for _, ac := range cfg.Accounts {
		if ac.Active && ac.Strategy == fxtrld.Name {
			fxActive = true
			break
		}
	}
	var fxSess *session.Controller
	if fxActive && cfg.FXSession.Enabled() {
		fxSess, err = session.NewController(session.Config{
			TZ:               cfg.FXSession.TZ,
			RangeStart:       cfg.FXSession.RangeStart,
			RangeEnd:         cfg.FXSession.RangeEnd,
			TradeWindowStart: cfg.FXSession.TradeWindowStart,
			ATRPeriodDays:    cfg.FXSession.ATRPeriodDays,
			VolMACandles:     cfg.FXSession.VolMACandles,
			Instruments:      cfg.FXSession.Instruments,
			SkipWeekends:     true,
		}, client, nil, log)
		if err != nil {
			return err
		}
		if err := sessHub.Add(fxSess); err != nil {
			return err
		}
	}

	// --- strategy registry + router ---
	strategy.Register(eulove.Name, func() (strategy.Strategy, error) {
		return eulove.New(eulove.Config{
			VolumeSpikeMult:  cfg.Strategies.EULove.VolumeSpikeMult,
			SLATRMult:        cfg.Strategies.EULove.SLATRMult,
			TPATRMult:        cfg.Strategies.EULove.TPATRMult,
			BreakevenAtR:     cfg.Strategies.EULove.BreakevenAtR,
			TradeWindowStart: cfg.EUSession.TradeWindowStart,
			EntryWindowEnd:   cfg.Strategies.EULove.EntryWindowEnd,
			FridayCutoff:     cfg.Mgmt.EUFridayCutoff,
			DailyCutoff:      cfg.Mgmt.EUDailyCutoff,
			Location:         euSess.Location(),
		})
	})
	if fxActive {
		ny, err := time.LoadLocation(firstNonEmpty(cfg.Mgmt.FXFridayCutoffTZ, "America/New_York"))
		if err != nil {
			return err
		}
		fxLoc := euSess.Location()
		if fxSess != nil {
			fxLoc = fxSess.Location()
		}
		twStart := cfg.Strategies.FXTRLD.TradeWindowStart
		if twStart == "" {
			twStart = cfg.FXSession.TradeWindowStart
		}
		strategy.Register(fxtrld.Name, func() (strategy.Strategy, error) {
			return fxtrld.New(fxtrld.Config{
				VolumeSpikeMult:  cfg.Strategies.FXTRLD.VolumeSpikeMult,
				SLATRMult:        cfg.Strategies.FXTRLD.SLATRMult,
				TPATRMult:        cfg.Strategies.FXTRLD.TPATRMult,
				BreakevenAtR:     cfg.Strategies.FXTRLD.BreakevenAtR,
				MinATRFrac:       cfg.Strategies.FXTRLD.MinATRFrac,
				MaxATRFrac:       cfg.Strategies.FXTRLD.MaxATRFrac,
				MaxSpreadPips:    cfg.Strategies.FXTRLD.MaxSpreadPips,
				PipSize:          cfg.FXRisk.PipSize,
				TradeWindowStart: twStart,
				EntryWindowEnd:   cfg.Strategies.FXTRLD.EntryWindowEnd,
				FridayCutoff:     firstNonEmpty(cfg.Mgmt.FXFridayCutoff, cfg.FXRisk.FridayHardFlatten),
				FridayCutoffLoc:  ny,
				SoftCutoff:       firstNonEmpty(cfg.Mgmt.FXSoftCutoff, cfg.FXSession.SoftCutoff),
				TrailAfterR:      cfg.Strategies.FXTRLD.TrailAfterR,
				Location:         fxLoc,
			})
		})
	}
	router := strategy.NewRouter()

	// --- per-account executors, risk engines, management loops ---
	executors := map[string]*execution.OANDAExecutor{}
	engines := map[string]*risk.Engine{}
	loops := map[string]*trademgmt.Loop{}
	instrumentRegion := map[string]string{}
	for _, ac := range cfg.Accounts {
		if !ac.Active {
			continue
		}
		acct, _ := pm.Account(ac.Name)
		exec := execution.NewOANDAExecutor(client, ac.OANDAID, ac.Name, cfg.Executor.TimeInForce, log.With("account", ac.Name), pub)
		if err := exec.LoadInstruments(ctx, ac.Instruments); err != nil {
			return err // fail boot: never trade blind (spec 07)
		}
		executors[ac.Name] = exec

		strat, err := strategy.New(ac.Strategy)
		if err != nil {
			return err
		}
		newsRegion := "EU"
		if ac.Strategy == fxtrld.Name {
			newsRegion = "FX"
		}
		for _, inst := range ac.Instruments {
			if err := router.Assign(inst, strat); err != nil {
				return err
			}
			instrumentRegion[inst] = newsRegion
		}

		timeToNews := func(region string, now time.Time) time.Duration {
			if region == "FX" {
				regions := cfg.FXRisk.CalendarRegions
				if len(regions) == 0 {
					regions = []string{"US", "JP"}
				}
				return calendar.MinTimeToHighImpact(calCache, regions, now)
			}
			return calCache.TimeToHighImpact(region, now)
		}
		regionFn := func(inst string) string {
			if r, ok := instrumentRegion[inst]; ok {
				return r
			}
			return "EU"
		}

		var loop *trademgmt.Loop
		eng := risk.NewEngine(pm.RiskConfig(ac.Name), acct, newsRegion, risk.Deps{
			SystemState: machine.State,
			ForceLock:   machine.ForceLock,
			Stale:       consumer.Stale,
			TimeToNews:  timeToNews,
			OpenTrades:  func() []types.OpenTrade { return loop.OpenTrades() },
			Meta:        exec.Instrument,
			Price:       snapshot.Mid,
		})
		if ac.Strategy == fxtrld.Name {
			fxLoc := time.UTC
			if fxSess != nil {
				fxLoc = fxSess.Location()
			}
			prof, err := risk.FXProfileFromConfig(cfg.FXRisk, fxLoc)
			if err != nil {
				return err
			}
			if prof != nil {
				pip := prof.PipSize
				eng.WithFX(*prof, risk.FXDeps{
					SpreadPips: func(instrument string) (float64, bool) {
						sp := snapshot.Spread(instrument)
						if sp <= 0 {
							return 0, false
						}
						return sp / pip, true
					},
					TradedToday: acct.TradedOn,
					MarkTraded:  acct.MarkTraded,
				})
			}
		}
		engines[ac.Name] = eng

		settle := onVanished(ctx, client, ac.OANDAID, acct, eng, log.With("account", ac.Name), pub, mets)
		newsBlock := cfg.Mgmt.NewsBlockBefore.D()
		if ac.Strategy == fxtrld.Name && cfg.FXRisk.NewsBlockBefore > 0 {
			newsBlock = cfg.FXRisk.NewsBlockBefore.D()
		}
		loop = trademgmt.NewLoop(trademgmt.Config{
			TickInterval:      cfg.Mgmt.TickInterval.D(),
			ReconcileInterval: cfg.Mgmt.ReconcileInterval.D(),
			NewsBlockBefore:   newsBlock,
		}, trademgmt.Deps{
			Executor:    exec,
			Price:       snapshot.Mid,
			TimeToNews:  timeToNews,
			Region:      regionFn,
			SystemState: machine.State,
			OnVanished:  settle,
		}, log.With("account", ac.Name))
		if ac.Strategy == fxtrld.Name {
			softTZ, _ := time.LoadLocation(firstNonEmpty(cfg.Mgmt.FXSoftCutoffTZ, "Asia/Tokyo"))
			friTZ, _ := time.LoadLocation(firstNonEmpty(cfg.Mgmt.FXFridayCutoffTZ, "America/New_York"))
			loop.WithFX(trademgmt.FXConfig{
				Enabled:            true,
				NewsUnderwaterFlat: true,
				SoftCutoffTZ:       softTZ,
				SoftCutoff:         firstNonEmpty(cfg.Mgmt.FXSoftCutoff, cfg.FXSession.SoftCutoff),
				SoftCutoffFlattenR: cfg.FXRisk.SoftCutoffFlattenR,
				FridayCutoffTZ:     friTZ,
				FridayCutoff:       firstNonEmpty(cfg.Mgmt.FXFridayCutoff, cfg.FXRisk.FridayHardFlatten),
			})
		}
		loops[ac.Name] = loop
	}

	// --- boot recovery: equity, candles backfill, session state ---
	for _, ac := range cfg.Accounts {
		if !ac.Active {
			continue
		}
		acct, _ := pm.Account(ac.Name)
		if err := refreshEquity(ctx, executors[ac.Name], acct); err != nil {
			return fmt.Errorf("boot: account %s equity: %w", ac.Name, err)
		}
	}
	if err := builder.Backfill(ctx); err != nil {
		return fmt.Errorf("boot: candle backfill: %w", err)
	}
	if err := sessHub.RefreshATR(ctx); err != nil {
		log.Error("boot: ATR refresh failed; entries blocked until it succeeds", "error", err)
	}
	if err := sessHub.RecoverRange(ctx, time.Now()); err != nil {
		log.Error("boot: session range recovery failed", "error", err)
	}

	var wg sync.WaitGroup

	// --- goroutine: pricing stream per account ---
	for _, ac := range cfg.Accounts {
		if !ac.Active {
			continue
		}
		wg.Add(1)
		go func(accountID string, instruments []string) {
			defer wg.Done()
			err := client.RunPricingStream(ctx, accountID, oanda.StreamConfig{
				Instruments:      instruments,
				HeartbeatTimeout: cfg.Stream.HeartbeatTimeout.D(),
				BackoffBase:      cfg.Stream.BackoffBase.D(),
				BackoffMax:       cfg.Stream.BackoffMax.D(),
			}, log, func(msg oanda.StreamMessage) {
				mets.Inc("stream_messages")
				consumer.HandleMessage(msg)
			})
			if err != nil && !errors.Is(err, context.Canceled) {
				log.Error("pricing stream terminated", "error", err)
			}
		}(ac.OANDAID, ac.Instruments)
	}

	// --- goroutine: candle builder ---
	wg.Add(1)
	go func() {
		defer wg.Done()
		builder.Run(ctx, consumer.Ticks())
	}()

	// --- goroutine: strategy engine (candle-close → signal → risk → order) ---
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-builder.Events():
				handleEvent(ctx, ev, sessHub, snapshot, router, pm, engines, executors, log, mets)
			}
		}
	}()

	// --- goroutine: trade-management loops ---
	for _, loop := range loops {
		wg.Add(1)
		go func(l *trademgmt.Loop) {
			defer wg.Done()
			l.Run(ctx)
		}(loop)
	}

	// --- goroutine: control-plane webhook ---
	cpServer := controlplane.NewServer(machine, controlplane.Actions{
		Flatten: func(ctx context.Context) error {
			var errs []error
			for name, exec := range executors {
				if err := exec.CancelAllOrders(ctx); err != nil {
					errs = append(errs, fmt.Errorf("%s cancel: %w", name, err))
				}
				trades, err := exec.OpenTrades(ctx)
				if err != nil {
					errs = append(errs, fmt.Errorf("%s trades: %w", name, err))
					continue
				}
				for _, t := range trades {
					if err := exec.Close(ctx, t.TradeID); err != nil {
						errs = append(errs, fmt.Errorf("%s close %s: %w", name, t.TradeID, err))
					}
				}
			}
			return errors.Join(errs...)
		},
		ReArm: func(ctx context.Context) error {
			var errs []error
			for _, acct := range pm.Accounts() {
				if err := refreshEquity(ctx, executors[acct.Name()], acct); err != nil {
					errs = append(errs, err)
					continue
				}
				acct.SnapshotBaseline()
			}
			return errors.Join(errs...)
		},
		Status: func(ctx context.Context) any {
			accounts := map[string]any{}
			for _, acct := range pm.Accounts() {
				accounts[acct.Name()] = map[string]any{
					"equity":             acct.Equity(),
					"baseline_equity":    acct.BaselineEquity(),
					"daily_realized_pl":  acct.DailyRealizedPL(),
					"consecutive_losses": acct.ConsecutiveLosses(),
					"open_trades":        loops[acct.Name()].OpenTrades(),
				}
			}
			return map[string]any{"state": machine.State(), "accounts": accounts, "metrics": mets.Snapshot()}
		},
	}, controlplane.AuthConfig{
		Secret:   []byte(cfg.ControlPlane.Auth.Secret),
		MaxSkew:  cfg.ControlPlane.Auth.MaxSkew.D(),
		NonceTTL: cfg.ControlPlane.Auth.NonceTTL.D(),
	}, log)
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := cpServer.ListenAndServe(ctx, cfg.ControlPlane.Listen, cfg.ControlPlane.TLSCert, cfg.ControlPlane.TLSKey); err != nil {
			log.Error("control plane server failed", "error", err)
			stop() // control plane down = no kill switch: shut down safely
		}
	}()

	// --- goroutine: housekeeping scheduler (NOT the signal trigger) ---
	sched := scheduler.New(log)
	sched.Add(scheduler.Job{
		Name: "calendar_refresh", Interval: cfg.Calendar.Economic.VMRefresh.D(), RunAtStart: false,
		Fn: func(context.Context) error { return calCache.Refresh() },
	})
	sched.Add(scheduler.Job{
		Name: "equity_reconcile", Interval: cfg.Observability.ReconcileInterval.D(),
		Fn: func(ctx context.Context) error {
			var errs []error
			for _, acct := range pm.Accounts() {
				if err := refreshEquity(ctx, executors[acct.Name()], acct); err != nil {
					acct.MarkUnknown() // risk rejects entries until readable again
					errs = append(errs, err)
				}
			}
			return errors.Join(errs...)
		},
	})
	sched.Add(scheduler.Job{
		Name: "atr_refresh", Interval: 30 * time.Minute,
		Fn: func(ctx context.Context) error { return sessHub.RefreshATR(ctx) },
	})
	sched.Add(scheduler.Job{
		Name: "daily_baseline", Interval: time.Minute,
		Fn: newDailyBaselineJob(pm, euSess, log),
	})
	sched.Add(scheduler.Job{
		Name: "heartbeat", Interval: cfg.Observability.HeartbeatInterval.D(), RunAtStart: true,
		Fn: func(context.Context) error {
			mets.Set("heartbeat_unix", float64(time.Now().Unix()))
			log.Info("heartbeat", "state", machine.State(), "metrics", mets.Snapshot())
			return nil
		},
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		sched.Run(ctx)
	}()

	log.Info("trader started", "host", cfg.OANDA.Host, "listen", cfg.ControlPlane.Listen)
	<-ctx.Done()
	log.Info("shutdown: flushing publisher and stopping goroutines")
	pub.Flush()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		log.Warn("shutdown timed out waiting for goroutines")
	}
	return nil
}

// handleEvent is the hot-path glue: candle close → session → router → risk →
// executor. Synchronous and in-process; the only network call is the order.
func handleEvent(ctx context.Context, ev types.MarketEvent,
	sess *session.Hub, snap *marketdata.Snapshot, router *strategy.Router, pm *portfolio.Manager,
	engines map[string]*risk.Engine, executors map[string]*execution.OANDAExecutor,
	log *slog.Logger, mets *metrics.Memory) {

	sess.OnCandle(ev)
	if ev.Timeframe != types.M5 {
		return // H1 feeds the session controller only
	}
	mets.Inc("m5_closes")

	acct, ok := pm.AccountFor(ev.Instrument)
	if !ok {
		return
	}
	st, ok := sess.State(ev.Instrument, ev.Now)
	if !ok {
		return // holiday / controller idle
	}
	ev.Price = snap.Mid(ev.Instrument)
	ev.Spread = snap.Spread(ev.Instrument)
	sig := router.Dispatch(ev, st)
	if sig == nil {
		return
	}
	log.Info("signal", "instrument", sig.Instrument, "direction", sig.Direction, "reason", sig.Reason)
	mets.Inc("signals")

	eng := engines[acct.Name()]
	now := time.Now()
	req, err := eng.Evaluate(*sig, now)
	if err != nil {
		log.Warn("signal rejected by risk", "instrument", sig.Instrument, "error", err)
		mets.Inc("risk_rejections")
		return
	}
	trade, err := executors[acct.Name()].Open(ctx, req)
	if err != nil {
		log.Error("order failed", "instrument", sig.Instrument, "error", err)
		mets.Inc("order_failures")
		return
	}
	eng.MarkAccepted(*sig, now)
	mets.Inc("orders_opened")
	log.Info("order placed", "trade_id", trade.TradeID, "units", trade.Units)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// onVanished settles realized P&L when a trade disappears from OpenTrades()
// (bracket exit, manual close at the console, etc.): fetch the closed trade
// from OANDA, record it against the account, and re-check the breakers.
func onVanished(ctx context.Context, client *oanda.Client, accountID string,
	acct *portfolio.Account, eng *risk.Engine, log *slog.Logger, pub logging.Publisher, mets *metrics.Memory) func(types.OpenTrade) {
	return func(t types.OpenTrade) {
		rt, err := client.Trade(ctx, accountID, t.TradeID)
		if err != nil {
			log.Error("could not settle vanished trade; P&L counters may lag until reconcile", "trade_id", t.TradeID, "error", err)
			return
		}
		pl := float64(rt.RealizedPL)
		acct.RecordClose(pl)
		eng.CheckBreakers() // trip daily-loss / consecutive-loss locks promptly
		mets.Add("daily_realized_pl", pl)
		pub.Publish(types.TradeEvent{
			Type: "closed", TradeID: t.TradeID, ClientOrderID: t.ClientOrderID,
			Account: t.Account, Instrument: t.Instrument, Units: t.Units,
			EntryPrice: t.Entry, ExitPrice: float64(rt.AverageClosePrice),
			RealizedPL: pl, ExitReason: "bracket", At: time.Now().UTC(),
		})
		log.Info("trade settled", "trade_id", t.TradeID, "realized_pl", pl,
			"daily_realized", acct.DailyRealizedPL(), "consecutive_losses", acct.ConsecutiveLosses())
	}
}

func refreshEquity(ctx context.Context, exec *execution.OANDAExecutor, acct *portfolio.Account) error {
	summary, err := exec.AccountSummary(ctx)
	if err != nil {
		return err
	}
	acct.SetEquity(float64(summary.NAV))
	return nil
}

// newDailyBaselineJob snapshots the per-account daily baseline once per local
// session day (the daily P&L reference; RE_ARM snapshots explicitly).
func newDailyBaselineJob(pm *portfolio.Manager, sess *session.Controller, log *slog.Logger) func(context.Context) error {
	lastDay := ""
	return func(context.Context) error {
		day := time.Now().In(sess.Location()).Format("2006-01-02")
		if day == lastDay {
			return nil
		}
		lastDay = day
		for _, acct := range pm.Accounts() {
			acct.SnapshotBaseline()
		}
		log.Info("daily baseline snapshotted", "day", day)
		return nil
	}
}
