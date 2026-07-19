package dashboard

import (
	"context"
	"fmt"
	"time"

	"github.com/yogesh-insta/tradex/internal/oanda"
)

// AccountReader is the read-only OANDA surface the dashboard needs.
type AccountReader interface {
	AccountSummary(ctx context.Context, accountID string) (oanda.AccountSummary, error)
	OpenTrades(ctx context.Context, accountID string) (oanda.OpenTradesResponse, error)
}

// ObjectFetcher loads a durable JSON object (GCS or local file).
type ObjectFetcher interface {
	Fetch(ctx context.Context, uri string) ([]byte, error)
}

// LedgerQuerier returns realized P&L aggregates from trade_ledger (or a file).
type LedgerQuerier interface {
	DailyPL(ctx context.Context, account string, from, to time.Time, loc *time.Location) ([]DailyPL, error)
	AllTimePL(ctx context.Context, account string) (total float64, count int64, err error)
}

// OANDAReader adapts *oanda.Client.
type OANDAReader struct{ Client *oanda.Client }

// AccountSummary implements AccountReader.
func (r OANDAReader) AccountSummary(ctx context.Context, accountID string) (oanda.AccountSummary, error) {
	return r.Client.AccountSummary(ctx, accountID)
}

// OpenTrades implements AccountReader.
func (r OANDAReader) OpenTrades(ctx context.Context, accountID string) (oanda.OpenTradesResponse, error) {
	return r.Client.OpenTrades(ctx, accountID)
}

// MockAccountReader returns fixture account/trade data for local demos.
type MockAccountReader struct {
	Summaries map[string]oanda.AccountSummary
	Trades    map[string][]oanda.RESTTrade
}

// AccountSummary implements AccountReader.
func (m MockAccountReader) AccountSummary(_ context.Context, accountID string) (oanda.AccountSummary, error) {
	s, ok := m.Summaries[accountID]
	if !ok {
		return oanda.AccountSummary{}, fmt.Errorf("mock: unknown account %s", accountID)
	}
	return s, nil
}

// OpenTrades implements AccountReader.
func (m MockAccountReader) OpenTrades(_ context.Context, accountID string) (oanda.OpenTradesResponse, error) {
	return oanda.OpenTradesResponse{Trades: m.Trades[accountID]}, nil
}
