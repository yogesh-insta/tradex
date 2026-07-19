// Package backtest replays historical candles through the SAME strategy +
// risk + trade-management code path as live trading, with a simulated
// executor that models spread and configurable slippage.
package backtest

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/yogesh-insta/tradex/internal/execution"
	"github.com/yogesh-insta/tradex/pkg/types"
)

// ClosedTrade is one completed simulated round trip.
type ClosedTrade struct {
	Trade      types.OpenTrade
	ExitPrice  float64
	ExitReason string // tp / sl / time_cutoff / flatten / breakeven
	ClosedAt   time.Time
	RealizedPL float64
}

// SimExecutor implements execution.OrderExecutor over a virtual book.
// Fill model:
//   - MARKET entries fill at the current mid ± spread/2 ± slippage (adverse).
//   - Stops fill at the stop price minus slippage (adverse); targets fill at
//     the target price exactly.
//   - If a candle touches both SL and TP, the SL is assumed to fill first
//     (conservative).
type SimExecutor struct {
	meta     map[string]execution.InstrumentMeta
	spread   float64 // full spread, price points
	slippage float64 // adverse slippage per fill, price points
	now      func() time.Time
	price    func(instrument string) float64 // current mid
	onClose  func(ClosedTrade)

	mu     sync.Mutex
	nextID int
	open   map[string]*types.OpenTrade
	byCOID map[string]string // clientOrderID -> tradeID (idempotency)
}

// NewSimExecutor builds the simulator.
func NewSimExecutor(meta map[string]execution.InstrumentMeta, spread, slippage float64,
	now func() time.Time, price func(string) float64, onClose func(ClosedTrade)) *SimExecutor {
	return &SimExecutor{
		meta:     meta,
		spread:   spread,
		slippage: slippage,
		now:      now,
		price:    price,
		onClose:  onClose,
		open:     map[string]*types.OpenTrade{},
		byCOID:   map[string]string{},
	}
}

// Open implements OrderExecutor (market fill at current mid + costs).
func (s *SimExecutor) Open(_ context.Context, req types.OrderRequest) (types.OpenTrade, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, dup := s.byCOID[req.ClientOrderID]; dup {
		if t, ok := s.open[id]; ok {
			return *t, nil // idempotent replay
		}
		return types.OpenTrade{}, fmt.Errorf("sim: duplicate client order id %s on closed trade", req.ClientOrderID)
	}
	mid := s.price(req.Instrument)
	if mid <= 0 {
		return types.OpenTrade{}, fmt.Errorf("sim: no price for %s", req.Instrument)
	}
	adverse := s.spread/2 + s.slippage
	entry := mid + adverse
	if req.Units < 0 {
		entry = mid - adverse
	}
	s.nextID++
	t := types.OpenTrade{
		TradeID:       fmt.Sprintf("sim-%d", s.nextID),
		ClientOrderID: req.ClientOrderID,
		Account:       req.Account,
		Instrument:    req.Instrument,
		Units:         req.Units,
		Entry:         entry,
		CurrentSL:     req.StopLoss,
		CurrentTP:     req.TakeProfit,
		RiskDistance:  math.Abs(entry - req.StopLoss),
		Policy:        req.Policy,
		OpenedAt:      s.now(),
	}
	s.open[t.TradeID] = &t
	s.byCOID[req.ClientOrderID] = t.TradeID
	return t, nil
}

// ModifyStop implements OrderExecutor.
func (s *SimExecutor) ModifyStop(_ context.Context, tradeID string, price float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.open[tradeID]
	if !ok {
		return nil // vanished = already closed; reconcile drops it (spec 07)
	}
	t.CurrentSL = price
	return nil
}

// Close implements OrderExecutor (market close at current mid ± costs).
func (s *SimExecutor) Close(_ context.Context, tradeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.open[tradeID]
	if !ok {
		return nil
	}
	mid := s.price(t.Instrument)
	adverse := s.spread/2 + s.slippage
	exit := mid - adverse
	if t.Units < 0 {
		exit = mid + adverse
	}
	s.settle(t, exit, "time_cutoff")
	return nil
}

// CancelOrder implements OrderExecutor (no resting orders in v1 sim).
func (s *SimExecutor) CancelOrder(context.Context, string) error { return nil }

// OpenTrades implements OrderExecutor.
func (s *SimExecutor) OpenTrades(context.Context) ([]types.OpenTrade, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]types.OpenTrade, 0, len(s.open))
	for _, t := range s.open {
		out = append(out, *t)
	}
	return out, nil
}

// Instrument implements OrderExecutor.
func (s *SimExecutor) Instrument(sym string) (execution.InstrumentMeta, error) {
	m, ok := s.meta[sym]
	if !ok {
		return execution.InstrumentMeta{}, fmt.Errorf("sim: unknown instrument %s", sym)
	}
	return m, nil
}

// OnCandle checks bracket exits for every open trade against the candle's
// range. Called by the engine for each replayed M5 candle before Analyze.
func (s *SimExecutor) OnCandle(c types.Candle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.open {
		if t.Instrument != c.Instrument {
			continue
		}
		long := t.Units > 0
		slHit := (long && c.Low <= t.CurrentSL) || (!long && c.High >= t.CurrentSL)
		tpHit := (long && c.High >= t.CurrentTP) || (!long && c.Low <= t.CurrentTP)
		switch {
		case slHit: // SL-first when both touch (conservative)
			exit := t.CurrentSL - s.slippage
			if !long {
				exit = t.CurrentSL + s.slippage
			}
			s.settle(t, exit, stopReason(t))
		case tpHit:
			s.settle(t, t.CurrentTP, "tp")
		}
	}
}

// settle removes the trade and reports the realized P&L (caller holds mu).
func (s *SimExecutor) settle(t *types.OpenTrade, exit float64, reason string) {
	pl := t.Units * (exit - t.Entry)
	delete(s.open, t.TradeID)
	if s.onClose != nil {
		s.onClose(ClosedTrade{
			Trade:      *t,
			ExitPrice:  exit,
			ExitReason: reason,
			ClosedAt:   s.now(),
			RealizedPL: pl,
		})
	}
}

// stopReason distinguishes an original stop from a breakeven stop.
func stopReason(t *types.OpenTrade) string {
	if t.CurrentSL == t.Entry {
		return "breakeven"
	}
	return "sl"
}
