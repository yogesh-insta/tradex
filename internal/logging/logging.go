// Package logging configures structured JSON logging (log/slog) and provides
// the trade audit log helper.
package logging

import (
	"log/slog"
	"os"

	"github.com/yogesh-insta/tradex/pkg/types"
)

// New returns a JSON slog.Logger writing to stdout at the given level, tagged
// with the service name and environment.
func New(service, env string, level slog.Level) *slog.Logger {
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(h).With("service", service, "env", env)
}

// Audit logs a trade event to the structured audit stream. This is the local
// (always-on) record; the async publisher ships the same events off-box.
func Audit(log *slog.Logger, ev types.TradeEvent) {
	log.Info("trade_event",
		"type", ev.Type,
		"trade_id", ev.TradeID,
		"client_order_id", ev.ClientOrderID,
		"account", ev.Account,
		"instrument", ev.Instrument,
		"strategy", ev.Strategy,
		"direction", ev.Direction,
		"units", ev.Units,
		"entry_price", ev.EntryPrice,
		"stop_loss", ev.StopLoss,
		"take_profit", ev.TakeProfit,
		"exit_price", ev.ExitPrice,
		"realized_pl", ev.RealizedPL,
		"exit_reason", ev.ExitReason,
		"reason", ev.Reason,
		"event_seq", ev.EventSeq,
	)
}
