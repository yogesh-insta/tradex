// Package session implements the session controller used by EU LOVE
// (docs/specs/04-eu-session-controller.md) and FX TRLD
// (docs/specs/16-fx-session-controller.md): it distills candles into the
// read-only SessionState strategies consume (opening range, 14-day Wilder ATR,
// running VWAP, VolMA12). It never emits signals or touches orders.
// FX clocks/instruments are supplied via Config (see Hub + trader wiring).
package session

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/yogesh-insta/tradex/internal/calendar"
	"github.com/yogesh-insta/tradex/internal/oanda"
	"github.com/yogesh-insta/tradex/pkg/types"
	"github.com/yogesh-insta/tradex/pkg/utils"
)

// RESTSource is the candle-history slice of the OANDA client (mockable).
type RESTSource interface {
	Candles(ctx context.Context, instrument, granularity string, count int, from, to time.Time) (oanda.CandlesResponse, error)
}

// Config mirrors the eu_session / fx_session config keys.
type Config struct {
	TZ               string
	RangeStart       string // "08:00:00" / FX "09:00:00"
	RangeEnd         string // "09:00:00" / FX "11:00:00"
	TradeWindowStart string // "09:05:00" / FX "16:00:00"
	ATRPeriodDays    int
	VolMACandles     int
	Instruments      []string
	Markets          map[string]string // instrument -> holiday market code (XETR/XPAR)
	// SkipWeekends closes Sat/Sun in the controller's TZ (FX Tokyo calendar).
	SkipWeekends bool
}

type instrumentState struct {
	sessionDay  string // local date the state belongs to (YYYY-MM-DD)
	openingHigh float64
	openingLow  float64
	rangeLocked bool
	dailyATR    float64

	// running VWAP accumulators (from range start)
	sumTypVol float64
	sumVol    float64

	// day's M5 volumes, oldest first (VolMA12 = avg of the 12 before last close)
	volumes []int64
	volMA   float64

	asOf time.Time
}

// Controller computes per-instrument EU SessionState. Safe for concurrent use.
type Controller struct {
	cfg      Config
	loc      *time.Location
	rest     RESTSource
	holidays *calendar.Holidays
	log      *slog.Logger

	mu    sync.RWMutex
	insts map[string]*instrumentState
}

// NewController validates the timezone and builds the controller.
func NewController(cfg Config, rest RESTSource, holidays *calendar.Holidays, log *slog.Logger) (*Controller, error) {
	loc, err := time.LoadLocation(cfg.TZ)
	if err != nil {
		return nil, fmt.Errorf("session: tz %q: %w", cfg.TZ, err)
	}
	c := &Controller{cfg: cfg, loc: loc, rest: rest, holidays: holidays, log: log, insts: map[string]*instrumentState{}}
	for _, in := range cfg.Instruments {
		c.insts[in] = &instrumentState{}
	}
	return c, nil
}

// Location returns the session's IANA location (Europe/Berlin).
func (c *Controller) Location() *time.Location { return c.loc }

// IsTradingDay applies weekend skip + holiday calendar for the instrument.
func (c *Controller) IsTradingDay(instrument string, now time.Time) bool {
	return c.isTradingDayLocked(instrument, now)
}

// clockAt resolves a config wall-clock on now's local day.
func (c *Controller) clockAt(now time.Time, clock string) time.Time {
	t, err := utils.AtClock(now, clock, c.loc)
	if err != nil {
		// Config is validated at boot; a parse failure here is impossible.
		panic(err)
	}
	return t
}

// TradeWindowOpen reports whether now is at/after trade_window_start (09:05).
func (c *Controller) TradeWindowOpen(now time.Time) bool {
	return !now.Before(c.clockAt(now, c.cfg.TradeWindowStart))
}

// OnCandle folds a closed candle into the session state. M5 candles drive
// VWAP/VolMA12 and range accumulation; the H1 candle spanning the range
// window is authoritative for the locked range.
func (c *Controller) OnCandle(ev types.MarketEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.insts[ev.Instrument]
	if !ok {
		return
	}
	now := ev.Now
	if !c.isTradingDayLocked(ev.Instrument, now) {
		return
	}
	c.resetIfNewDay(st, now)

	rangeStart := c.clockAt(now, c.cfg.RangeStart)
	rangeEnd := c.clockAt(now, c.cfg.RangeEnd)
	candleLocal := ev.Last.Start.In(c.loc)

	inRange := !candleLocal.Before(rangeStart) && candleLocal.Before(rangeEnd)

	switch ev.Timeframe {
	case types.M5:
		if inRange {
			if st.openingHigh == 0 || ev.Last.High > st.openingHigh {
				st.openingHigh = ev.Last.High
			}
			if st.openingLow == 0 || ev.Last.Low < st.openingLow {
				st.openingLow = ev.Last.Low
			}
		}
		// VWAP runs from the range start through the session.
		if !candleLocal.Before(rangeStart) {
			typ := (ev.Last.High + ev.Last.Low + ev.Last.Close) / 3
			st.sumTypVol += typ * float64(ev.Last.Volume)
			st.sumVol += float64(ev.Last.Volume)
			// VolMA12 = average of the 12 candles preceding this close.
			st.volMA = trailingAvg(st.volumes, c.cfg.VolMACandles)
			st.volumes = append(st.volumes, ev.Last.Volume)
		}
	case types.H1:
		// The H1 candle that opens exactly at range start spans the whole
		// window: adopt its high/low as the authoritative range.
		if candleLocal.Equal(rangeStart) && rangeEnd.Sub(rangeStart) == time.Hour {
			st.openingHigh = ev.Last.High
			st.openingLow = ev.Last.Low
		}
	}

	// Lock once the range window has fully elapsed.
	if !st.rangeLocked && !now.Before(rangeEnd) && st.openingHigh > 0 {
		st.rangeLocked = true
		c.log.Info("EU range locked",
			"instrument", ev.Instrument,
			"opening_high", st.openingHigh,
			"opening_low", st.openingLow)
	}
	st.asOf = now
}

// State returns the current SessionState. ok=false when the controller is
// idle (holiday) or has no state yet for the instrument.
func (c *Controller) State(instrument string, now time.Time) (types.SessionState, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	st, ok := c.insts[instrument]
	if !ok {
		return types.SessionState{}, false
	}
	if !c.isTradingDayLocked(instrument, now) {
		return types.SessionState{}, false
	}
	vwap := 0.0
	if st.sumVol > 0 {
		vwap = st.sumTypVol / st.sumVol
	}
	return types.SessionState{
		Instrument:  instrument,
		OpeningHigh: st.openingHigh,
		OpeningLow:  st.openingLow,
		RangeLocked: st.rangeLocked,
		DailyATR:    st.dailyATR,
		VWAP:        vwap,
		VolMA12:     st.volMA,
		AsOf:        st.asOf,
	}, true
}

// RefreshATR pulls daily candles and recomputes the Wilder ATR for every
// instrument (pre-session job, ~07:30 local, and on boot).
func (c *Controller) RefreshATR(ctx context.Context) error {
	for _, inst := range c.cfg.Instruments {
		resp, err := c.rest.Candles(ctx, inst, "D", c.cfg.ATRPeriodDays*3, time.Time{}, time.Time{})
		if err != nil {
			return fmt.Errorf("session: daily candles %s: %w", inst, err)
		}
		var daily []types.Candle
		for _, rc := range resp.Candles {
			if rc.Complete {
				daily = append(daily, types.Candle{
					High:  float64(rc.Mid.H),
					Low:   float64(rc.Mid.L),
					Close: float64(rc.Mid.C),
				})
			}
		}
		atr, err := WilderATR(daily, c.cfg.ATRPeriodDays)
		if err != nil {
			// ATR unknown = cannot size: leave 0; risk blocks entries.
			c.log.Error("ATR unavailable; entries blocked", "instrument", inst, "error", err)
			continue
		}
		c.mu.Lock()
		c.insts[inst].dailyATR = atr
		c.mu.Unlock()
		c.log.Info("daily ATR refreshed", "instrument", inst, "atr", atr)
	}
	return nil
}

// RecoverRange rebuilds the locked range and VWAP/VolMA from REST M5 candles
// when the process boots mid-session (after the range window opened).
func (c *Controller) RecoverRange(ctx context.Context, now time.Time) error {
	rangeStart := c.clockAt(now, c.cfg.RangeStart)
	rangeEnd := c.clockAt(now, c.cfg.RangeEnd)
	if now.Before(rangeStart) {
		return nil // normal lifecycle will run
	}
	for _, inst := range c.cfg.Instruments {
		if !c.IsTradingDay(inst, now) {
			continue
		}
		resp, err := c.rest.Candles(ctx, inst, "M5", 0, rangeStart, now)
		if err != nil {
			return fmt.Errorf("session recover %s: %w", inst, err)
		}
		c.mu.Lock()
		st := c.insts[inst]
		*st = instrumentState{sessionDay: now.In(c.loc).Format("2006-01-02"), dailyATR: st.dailyATR}
		for _, rc := range resp.Candles {
			if !rc.Complete {
				continue
			}
			candleLocal := rc.Time.In(c.loc)
			h, l, cl := float64(rc.Mid.H), float64(rc.Mid.L), float64(rc.Mid.C)
			if candleLocal.Before(rangeEnd) {
				if st.openingHigh == 0 || h > st.openingHigh {
					st.openingHigh = h
				}
				if st.openingLow == 0 || l < st.openingLow {
					st.openingLow = l
				}
			}
			typ := (h + l + cl) / 3
			st.sumTypVol += typ * float64(rc.Volume)
			st.sumVol += float64(rc.Volume)
			st.volMA = trailingAvg(st.volumes, c.cfg.VolMACandles)
			st.volumes = append(st.volumes, rc.Volume)
		}
		if !now.Before(rangeEnd) && st.openingHigh > 0 {
			st.rangeLocked = true
		}
		st.asOf = now
		c.mu.Unlock()
		c.log.Info("session state recovered from REST",
			"instrument", inst, "range_locked", st.rangeLocked,
			"opening_high", st.openingHigh, "opening_low", st.openingLow)
	}
	return nil
}

func (c *Controller) resetIfNewDay(st *instrumentState, now time.Time) {
	day := now.In(c.loc).Format("2006-01-02")
	if st.sessionDay != day {
		atr := st.dailyATR // ATR is refreshed by its own pre-session job
		*st = instrumentState{sessionDay: day, dailyATR: atr}
	}
}

// isTradingDayLocked assumes the caller holds a lock (or needs no lock).
func (c *Controller) isTradingDayLocked(instrument string, now time.Time) bool {
	if c.cfg.SkipWeekends {
		wd := now.In(c.loc).Weekday()
		if wd == time.Saturday || wd == time.Sunday {
			return false
		}
	}
	if c.holidays == nil {
		return true
	}
	return c.holidays.IsTradingDay(now, c.cfg.Markets[instrument])
}

func trailingAvg(vols []int64, n int) float64 {
	if len(vols) == 0 || n <= 0 {
		return 0
	}
	start := len(vols) - n
	if start < 0 {
		start = 0
	}
	sum := int64(0)
	for _, v := range vols[start:] {
		sum += v
	}
	return float64(sum) / float64(len(vols)-start)
}

// WilderATR computes the Wilder-smoothed ATR over the last `period` true
// ranges of the given daily candles (oldest first). Needs period+1 candles.
func WilderATR(daily []types.Candle, period int) (float64, error) {
	if period <= 0 {
		return 0, fmt.Errorf("atr: period must be > 0")
	}
	if len(daily) < period+1 {
		return 0, fmt.Errorf("atr: need %d daily candles, have %d", period+1, len(daily))
	}
	trs := make([]float64, 0, len(daily)-1)
	for i := 1; i < len(daily); i++ {
		h, l, pc := daily[i].High, daily[i].Low, daily[i-1].Close
		tr := h - l
		if d := abs(h - pc); d > tr {
			tr = d
		}
		if d := abs(l - pc); d > tr {
			tr = d
		}
		trs = append(trs, tr)
	}
	// Seed with the SMA of the first `period` TRs, then Wilder-smooth.
	atr := 0.0
	for _, tr := range trs[:period] {
		atr += tr
	}
	atr /= float64(period)
	for _, tr := range trs[period:] {
		atr = (atr*float64(period-1) + tr) / float64(period)
	}
	return atr, nil
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
