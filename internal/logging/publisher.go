package logging

import (
	"log/slog"
	"sync/atomic"

	"github.com/yogesh-insta/tradex/pkg/types"
)

// Publisher is the async trade-event sink (11-trade-ledger-persistence.md).
// v1 ships a stdout/JSON implementation; a Pub/Sub publisher slots in behind
// the same interface later. Publish must never block the hot path.
type Publisher interface {
	Publish(ev types.TradeEvent)
	// Flush drains buffered events (bounded wait), called on SIGTERM.
	Flush()
}

// LogPublisher writes trade events to the structured audit log.
type LogPublisher struct {
	log *slog.Logger
	seq atomic.Int64
}

// NewLogPublisher returns a publisher backed by the given logger.
func NewLogPublisher(log *slog.Logger) *LogPublisher {
	return &LogPublisher{log: log}
}

// Publish assigns an event sequence and logs the event.
func (p *LogPublisher) Publish(ev types.TradeEvent) {
	if ev.EventSeq == 0 {
		ev.EventSeq = p.seq.Add(1)
	}
	Audit(p.log, ev)
}

// Flush is a no-op for the log-backed publisher.
func (p *LogPublisher) Flush() {}

// NopPublisher discards events (backtests that don't need an audit stream).
type NopPublisher struct{}

func (NopPublisher) Publish(types.TradeEvent) {}
func (NopPublisher) Flush()                   {}
