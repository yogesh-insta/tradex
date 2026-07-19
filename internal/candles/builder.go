package candles

import (
	"context"
	"log/slog"
	"time"

	"github.com/yogesh-insta/tradex/internal/oanda"
	"github.com/yogesh-insta/tradex/pkg/types"
)

// RESTSource is the slice of the OANDA client the builder needs (mockable).
type RESTSource interface {
	Candles(ctx context.Context, instrument, granularity string, count int, from, to time.Time) (oanda.CandlesResponse, error)
}

// Config tunes the builder (03-candle-builder.md config keys).
type Config struct {
	Timeframes     map[string][]types.Timeframe // instrument -> timeframes
	WindowMax      int
	ConfirmDelay   time.Duration // wait after boundary before REST confirm
	ConfirmTimeout time.Duration
}

type key struct {
	instrument string
	timeframe  types.Timeframe
}

// Builder folds ticks into forming candles per (instrument, timeframe),
// confirms closes against REST, and emits MarketEvents. Timeframes are
// sourced independently — M5 is never derived from H1 or vice-versa.
type Builder struct {
	cfg    Config
	rest   RESTSource
	log    *slog.Logger
	now    func() time.Time
	events chan types.MarketEvent

	rings   map[key]*Ring
	forming map[key]*types.Candle
}

// NewBuilder constructs the builder and its buffers.
func NewBuilder(cfg Config, rest RESTSource, log *slog.Logger) *Builder {
	b := &Builder{
		cfg:     cfg,
		rest:    rest,
		log:     log,
		now:     time.Now,
		events:  make(chan types.MarketEvent, 64),
		rings:   map[key]*Ring{},
		forming: map[key]*types.Candle{},
	}
	for inst, tfs := range cfg.Timeframes {
		for _, tf := range tfs {
			b.rings[key{inst, tf}] = NewRing(cfg.WindowMax)
		}
	}
	return b
}

// Events emits a MarketEvent per closed candle (all timeframes; the engine
// routes M5 to strategies and H1 to the session controller).
func (b *Builder) Events() <-chan types.MarketEvent { return b.events }

// Ring exposes the buffer for one instrument+timeframe (session controller
// reads H1/M5 windows directly).
func (b *Builder) Ring(instrument string, tf types.Timeframe) *Ring {
	return b.rings[key{instrument, tf}]
}

// Backfill pulls the last WindowMax candles for every buffer so strategies
// and the session controller have history immediately on boot.
func (b *Builder) Backfill(ctx context.Context) error {
	for k, ring := range b.rings {
		resp, err := b.rest.Candles(ctx, k.instrument, string(k.timeframe), b.cfg.WindowMax, time.Time{}, time.Time{})
		if err != nil {
			return err
		}
		for _, rc := range resp.Candles {
			if !rc.Complete {
				continue
			}
			ring.Upsert(restToCandle(k.instrument, k.timeframe, rc))
		}
		b.log.Info("candles backfilled", "instrument", k.instrument, "timeframe", k.timeframe, "count", len(resp.Candles))
	}
	return nil
}

// Run consumes ticks and drives candle boundaries. A 1s housekeeping ticker
// closes candles even in quiet markets (a boundary must not depend on the
// next tick arriving). Blocks until ctx is done.
func (b *Builder) Run(ctx context.Context, ticks <-chan types.Tick) {
	boundary := time.NewTicker(time.Second)
	defer boundary.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case t, ok := <-ticks:
			if !ok {
				return
			}
			b.onTick(ctx, t)
		case <-boundary.C:
			b.checkBoundaries(ctx)
		}
	}
}

func (b *Builder) onTick(ctx context.Context, t types.Tick) {
	for _, tf := range b.cfg.Timeframes[t.Instrument] {
		k := key{t.Instrument, tf}
		start := AlignStart(t.Time, tf)
		f := b.forming[k]
		if f == nil || !f.Start.Equal(start) {
			if f != nil && f.Start.Before(start) {
				b.closeCandle(ctx, k, *f)
			}
			f = &types.Candle{Instrument: t.Instrument, Timeframe: tf, Start: start}
			b.forming[k] = f
		}
		FoldTick(f, t)
	}
}

// checkBoundaries closes forming candles whose window has fully elapsed plus
// the REST-confirm delay, independent of tick arrival.
func (b *Builder) checkBoundaries(ctx context.Context) {
	now := b.now()
	for k, f := range b.forming {
		if f == nil {
			continue
		}
		if now.After(f.Start.Add(k.timeframe.Duration() + b.cfg.ConfirmDelay)) {
			c := *f
			b.forming[k] = nil
			b.closeCandle(ctx, k, c)
		}
	}
}

// closeCandle confirms the locally-built candle against REST, upserts the
// authoritative version, and emits a MarketEvent. If REST is unavailable the
// local candle is emitted Complete but Unconfirmed (reconciled later).
func (b *Builder) closeCandle(ctx context.Context, k key, local types.Candle) {
	confirmed := local
	confirmed.Complete = true
	confirmed.Unconfirmed = true

	cctx, cancel := context.WithTimeout(ctx, b.cfg.ConfirmTimeout)
	resp, err := b.rest.Candles(cctx, k.instrument, string(k.timeframe), 3, time.Time{}, time.Time{})
	cancel()
	if err == nil {
		for _, rc := range resp.Candles {
			if rc.Complete && rc.Time.UTC().Equal(local.Start) {
				confirmed = restToCandle(k.instrument, k.timeframe, rc)
				break
			}
		}
	} else {
		b.log.Warn("REST candle confirm failed; emitting unconfirmed", "instrument", k.instrument, "timeframe", k.timeframe, "error", err)
	}

	ring := b.rings[k]
	ring.Upsert(confirmed)

	select {
	case b.events <- types.MarketEvent{
		Instrument: k.instrument,
		Now:        b.now(),
		Timeframe:  k.timeframe,
		Last:       confirmed,
		Window:     ring.Window(),
	}:
	default:
		b.log.Error("market event channel full; dropping event", "instrument", k.instrument, "timeframe", k.timeframe)
	}
}

func restToCandle(instrument string, tf types.Timeframe, rc oanda.RESTCandle) types.Candle {
	return types.Candle{
		Instrument: instrument,
		Timeframe:  tf,
		Start:      rc.Time.UTC(),
		Open:       float64(rc.Mid.O),
		High:       float64(rc.Mid.H),
		Low:        float64(rc.Mid.L),
		Close:      float64(rc.Mid.C),
		Volume:     rc.Volume,
		Complete:   rc.Complete,
	}
}
