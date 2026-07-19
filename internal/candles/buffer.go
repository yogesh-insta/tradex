// Package candles builds authoritative multi-timeframe OHLCV candles (M5 +
// H1) per docs/specs/03-candle-builder.md: streamed ticks give the real-time
// shape, OANDA REST gives the authoritative close, and MarketEvents are
// emitted on every M5 close.
package candles

import (
	"sync"
	"time"

	"github.com/yogesh-insta/tradex/pkg/types"
	"github.com/yogesh-insta/tradex/pkg/utils"
)

// Ring is a bounded, race-safe window of closed candles for one
// (instrument, timeframe), newest last.
type Ring struct {
	mu     sync.RWMutex
	max    int
	window []types.Candle
}

// NewRing returns a ring bounded at max candles.
func NewRing(max int) *Ring {
	return &Ring{max: max}
}

// Upsert inserts or replaces a candle keyed by (instrument, timeframe, start),
// keeping the window sorted by start and bounded. Replacement is idempotent —
// a REST reconcile of an already-present candle is a no-op overwrite.
func (r *Ring) Upsert(c types.Candle) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.window {
		if r.window[i].Start.Equal(c.Start) {
			r.window[i] = c
			return
		}
	}
	// Insert in order (candles almost always arrive in order → append fast path).
	pos := len(r.window)
	for pos > 0 && r.window[pos-1].Start.After(c.Start) {
		pos--
	}
	r.window = append(r.window, types.Candle{})
	copy(r.window[pos+1:], r.window[pos:])
	r.window[pos] = c
	if len(r.window) > r.max {
		r.window = r.window[len(r.window)-r.max:]
	}
}

// Window returns a copy of the current window, newest last.
func (r *Ring) Window() []types.Candle {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]types.Candle, len(r.window))
	copy(out, r.window)
	return out
}

// Last returns the newest candle.
func (r *Ring) Last() (types.Candle, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.window) == 0 {
		return types.Candle{}, false
	}
	return r.window[len(r.window)-1], true
}

// AlignStart floors t to the candle boundary for tf: M5 → :00/:05, H1 → top
// of hour (exchange/UTC aligned).
func AlignStart(t time.Time, tf types.Timeframe) time.Time {
	return utils.FloorTo(t, tf.Duration())
}

// FoldTick merges a tick into a forming candle. If the candle is zero-valued
// it is initialized from the tick.
func FoldTick(c *types.Candle, t types.Tick) {
	if c.Volume == 0 && c.Open == 0 {
		c.Open, c.High, c.Low = t.Mid, t.Mid, t.Mid
	}
	if t.Mid > c.High {
		c.High = t.Mid
	}
	if t.Mid < c.Low || c.Low == 0 {
		c.Low = t.Mid
	}
	c.Close = t.Mid
	c.Volume++
}
