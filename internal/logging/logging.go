// Package logging configures structured JSON logging (log/slog) for the
// dashboard, NSE rotator and ASX ETF monitor.
package logging

import (
	"log/slog"
	"os"
)

// New returns a JSON slog.Logger writing to stdout at the given level, tagged
// with the service name and environment.
func New(service, env string, level slog.Level) *slog.Logger {
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(h).With("service", service, "env", env)
}
