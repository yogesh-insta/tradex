package dashboard

import (
	"context"
)

// ObjectFetcher loads a durable JSON object (GCS or local file). The ETF and
// NSE lanes both read their reports through it; it is the dashboard's only
// remaining backend now that the broker and P&L surfaces are gone.
type ObjectFetcher interface {
	Fetch(ctx context.Context, uri string) ([]byte, error)
}

// ObjectPutter is an optional write side for analytics state (equity history).
// Trading remains read-only; a missing or failing Put must never break the UI.
type ObjectPutter interface {
	Put(ctx context.Context, uri string, data []byte) error
}
