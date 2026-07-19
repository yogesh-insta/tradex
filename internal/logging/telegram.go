package logging

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/yogesh-insta/tradex/pkg/types"
)

// MessageSender is the small outbound interface used by the asynchronous
// Telegram notifier. It deliberately has no trading-package dependency.
type MessageSender interface {
	SendMessage(context.Context, string) error
}

// TelegramPublisher mirrors selected trade events to Telegram without ever
// blocking the trading path. A full buffer drops the notification only; the
// underlying audit publisher still receives every event.
type TelegramPublisher struct {
	base   Publisher
	sender MessageSender
	log    *slog.Logger
	queue  chan string
	done   chan struct{}
	once   sync.Once
}

// NewTelegramPublisher decorates base with a bounded asynchronous notifier.
func NewTelegramPublisher(base Publisher, sender MessageSender, log *slog.Logger) *TelegramPublisher {
	p := &TelegramPublisher{
		base: base, sender: sender, log: log,
		queue: make(chan string, 64), done: make(chan struct{}),
	}
	go p.run()
	return p
}

// Publish always delegates to the audit publisher and best-effort queues a
// concise lifecycle alert. It never waits on Telegram or the queue.
func (p *TelegramPublisher) Publish(ev types.TradeEvent) {
	p.base.Publish(ev)
	text, ok := telegramText(ev)
	if !ok {
		return
	}
	select {
	case p.queue <- text:
	default:
		p.log.Warn("telegram notification dropped: queue full", "event_type", ev.Type)
	}
}

// Flush drains queued notifications for at most five seconds, then flushes the
// underlying publisher. It is called during graceful shutdown only.
func (p *TelegramPublisher) Flush() {
	p.once.Do(func() {
		close(p.queue)
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
			p.log.Warn("telegram notification flush timed out")
		}
		p.base.Flush()
	})
}

func (p *TelegramPublisher) run() {
	defer close(p.done)
	for text := range p.queue {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := p.sender.SendMessage(ctx, text)
		cancel()
		if err != nil {
			p.log.Warn("telegram notification failed", "error", err)
		}
	}
}

func telegramText(ev types.TradeEvent) (string, bool) {
	switch ev.Type {
	case "opened":
		return fmt.Sprintf("tradex OPENED\naccount: %s\n%s %s %.2f @ %.5f\nSL %.5f / TP %.5f\ntrade: %s",
			ev.Account, ev.Direction, ev.Instrument, ev.Units, ev.EntryPrice, ev.StopLoss, ev.TakeProfit, ev.TradeID), true
	case "closed":
		return fmt.Sprintf("tradex CLOSED\naccount: %s\n%s\nP/L: %.2f\ntrade: %s",
			ev.Account, ev.Instrument, ev.RealizedPL, ev.TradeID), true
	case "state_change":
		return fmt.Sprintf("tradex STATE\n%s", ev.Reason), true
	case "breaker_locked":
		return fmt.Sprintf("tradex BREAKER LOCKED\naccount: %s\nreason: %s\nRE_ARM required before new entries.",
			ev.Account, ev.Reason), true
	default:
		return "", false
	}
}
