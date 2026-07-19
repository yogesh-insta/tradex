// Package execution defines the broker-abstracted OrderExecutor interface and
// its OANDA implementation — the single writer to OANDA, per
// docs/specs/07-order-executor.md.
package execution

import (
	"context"

	"github.com/yogesh-insta/tradex/pkg/types"
)

// OrderExecutor is the broker abstraction. The OANDA implementation is the
// only component that writes to the broker; the backtester provides a
// simulated implementation over the same interface.
type OrderExecutor interface {
	// Open places an atomic entry + bracket. Idempotent by ClientOrderID.
	Open(ctx context.Context, req types.OrderRequest) (types.OpenTrade, error)
	// ModifyStop replaces the trade's stop (breakeven / news flatten).
	// No-op if the stop is already at the target price (level-triggered).
	ModifyStop(ctx context.Context, tradeID string, price float64) error
	// Close market-closes the trade in full.
	Close(ctx context.Context, tradeID string) error
	// CancelOrder cancels a resting order by client id (US path, future).
	CancelOrder(ctx context.Context, clientOrderID string) error
	// OpenTrades is the reconcile source of truth.
	OpenTrades(ctx context.Context) ([]types.OpenTrade, error)
	// Instrument returns cached metadata for sizing/precision.
	Instrument(sym string) (InstrumentMeta, error)
}

// InstrumentMeta is the metadata layer that keeps sizing broker-agnostic.
type InstrumentMeta struct {
	Symbol         string
	DisplayName    string
	PricePrecision int     // decimal places for price
	PipLocation    int     // OANDA pipLocation (10^PipLocation = one pip in price)
	UnitsPrecision int     // decimal places for trade units (OANDA tradeUnitsPrecision)
	MinUnits       float64 // OANDA minimumTradeSize (0.1 for index CFDs)
	MarginRate     float64
	PointValue     float64 // account-currency value per 1.0 price point per unit
}
