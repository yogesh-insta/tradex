package nserotator

import (
	"math"
	"sort"
)

// DiffResult is the order plan for one run.
type DiffResult struct {
	Sells []Order
	Buys  []Order
	Holds []string
}

// BuildOrders diffs target picks against current holdings (spec 20 §Algorithm):
//   - SELL: held symbol not in target (full quantity). Regime=cash ⇒ target
//     is empty ⇒ everything held becomes a SELL.
//   - BUY: target symbol not held; qty = floor(capital/topK/lastClose).
//   - HOLD: in both — never resized (no-rebalance decision).
//
// lastClose must contain prices for all target symbols; SELLs of symbols with
// no known price get ApproxValue 0 (still listed — user knows their position).
func BuildOrders(holdings []Holding, target []string, lastClose map[string]float64, capitalINR float64, topK int) DiffResult {
	inTarget := map[string]bool{}
	for _, t := range target {
		inTarget[t] = true
	}
	held := map[string]Holding{}
	for _, h := range holdings {
		held[h.Symbol] = h
	}

	var res DiffResult
	for _, h := range holdings {
		if inTarget[h.Symbol] {
			res.Holds = append(res.Holds, h.Symbol)
			continue
		}
		px := lastClose[h.Symbol]
		res.Sells = append(res.Sells, Order{
			Side:        "SELL",
			Symbol:      h.Symbol,
			Qty:         h.Qty,
			LastClose:   px,
			ApproxValue: math.Round(float64(h.Qty) * px),
		})
	}
	if topK <= 0 {
		topK = len(target)
	}
	perSlot := 0.0
	if topK > 0 {
		perSlot = capitalINR / float64(topK)
	}
	for _, t := range target {
		if _, ok := held[t]; ok {
			continue
		}
		px := lastClose[t]
		if px <= 0 {
			continue // no price, no buy — flagged upstream as excluded
		}
		qty := int64(math.Floor(perSlot / px))
		if qty <= 0 {
			continue // slot smaller than one share (e.g. MRF) — skip, note upstream
		}
		res.Buys = append(res.Buys, Order{
			Side:        "BUY",
			Symbol:      t,
			Qty:         qty,
			LastClose:   px,
			ApproxValue: math.Round(float64(qty) * px),
		})
	}
	sort.Slice(res.Sells, func(i, j int) bool { return res.Sells[i].Symbol < res.Sells[j].Symbol })
	sort.Slice(res.Buys, func(i, j int) bool { return res.Buys[i].Symbol < res.Buys[j].Symbol })
	sort.Strings(res.Holds)
	return res
}
