// Package marketdata owns the pricing-stream consumption path: it converts
// stream messages into Ticks, keeps the latest price per instrument in a
// race-safe snapshot, forwards ticks to the candle builder, and detects
// staleness per docs/specs/02-market-data-stream.md.
package marketdata

import (
	"sync"
	"time"

	"github.com/yogesh-insta/tradex/internal/oanda"
	"github.com/yogesh-insta/tradex/pkg/types"
)

// Snapshot is the shared latest-price map. Reads never block the ingest path.
type Snapshot struct {
	mu    sync.RWMutex
	ticks map[string]types.Tick
}

// NewSnapshot returns an empty snapshot.
func NewSnapshot() *Snapshot {
	return &Snapshot{ticks: map[string]types.Tick{}}
}

// Set stores the latest tick for its instrument.
func (s *Snapshot) Set(t types.Tick) {
	s.mu.Lock()
	s.ticks[t.Instrument] = t
	s.mu.Unlock()
}

// Get returns the latest tick for an instrument.
func (s *Snapshot) Get(instrument string) (types.Tick, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.ticks[instrument]
	return t, ok
}

// Mid returns the latest mid price, or 0 if unknown.
func (s *Snapshot) Mid(instrument string) float64 {
	t, ok := s.Get(instrument)
	if !ok {
		return 0
	}
	return t.Mid
}

// Spread returns the latest ask-bid, or 0 if unknown.
func (s *Snapshot) Spread(instrument string) float64 {
	t, ok := s.Get(instrument)
	if !ok {
		return 0
	}
	return t.Spread
}

// Consumer adapts stream messages to ticks and tracks staleness.
type Consumer struct {
	snapshot  *Snapshot
	tickCh    chan types.Tick
	staleHalt time.Duration

	mu         sync.Mutex
	lastTickAt map[string]time.Time
	lastMsgAt  time.Time
}

// NewConsumer wires a consumer to the shared snapshot. tickBuf sizes the
// channel to the candle builder.
func NewConsumer(snapshot *Snapshot, tickBuf int, staleHalt time.Duration) *Consumer {
	return &Consumer{
		snapshot:   snapshot,
		tickCh:     make(chan types.Tick, tickBuf),
		staleHalt:  staleHalt,
		lastTickAt: map[string]time.Time{},
	}
}

// Ticks is the buffered channel consumed by the candle builder.
func (c *Consumer) Ticks() <-chan types.Tick { return c.tickCh }

// HandleMessage is the oanda.StreamHandler: parses PRICE messages into Ticks,
// updates the snapshot, and forwards to the tick channel (dropping if the
// buffer is full rather than blocking the ingest goroutine).
func (c *Consumer) HandleMessage(msg oanda.StreamMessage) {
	c.mu.Lock()
	c.lastMsgAt = time.Now()
	c.mu.Unlock()

	if msg.Type != "PRICE" || len(msg.Bids) == 0 || len(msg.Asks) == 0 {
		return
	}
	bid := float64(msg.Bids[0].Price)
	ask := float64(msg.Asks[0].Price)
	tick := types.Tick{
		Instrument: msg.Instrument,
		Time:       msg.Time, // OANDA event time (UTC), not local clock
		Bid:        bid,
		Ask:        ask,
		Mid:        (bid + ask) / 2,
		Spread:     ask - bid,
		Tradeable:  msg.Tradeable || msg.Status == "tradeable",
	}

	// Record for staleness even when non-tradeable; only tradeable prices
	// update the trigger snapshot / candle path (spec 02 §2).
	c.mu.Lock()
	c.lastTickAt[msg.Instrument] = tick.Time
	c.mu.Unlock()

	if !tick.Tradeable {
		return
	}
	c.snapshot.Set(tick)
	select {
	case c.tickCh <- tick:
	default:
		// Buffer full: drop rather than block ingest; REST confirm corrects candles.
	}
}

// Stale reports whether no ticks have arrived for the instrument within
// staleHalt. Callers gate this on "session open" — outside session hours a
// silent stream is normal.
func (c *Consumer) Stale(instrument string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	last, ok := c.lastTickAt[instrument]
	if !ok {
		return true
	}
	return now.Sub(last) > c.staleHalt
}

// LastTickAt returns the last tick time seen for the instrument.
func (c *Consumer) LastTickAt(instrument string) (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.lastTickAt[instrument]
	return t, ok
}
