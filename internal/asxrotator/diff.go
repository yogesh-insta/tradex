package asxrotator

import (
	"math"
	"sort"
)

// DiffResult is the order plan for one run.
type DiffResult struct {
	Sells []Order
	Buys  []Order
	Holds []string
	// Frozen are held-but-untradeable symbols, reported so the position stays
	// visible but deliberately absent from Sells, Buys and Holds.
	Frozen []string
}

// BuildOrders diffs target picks against current holdings (spec 22 §Algorithm):
//   - SELL: held symbol not in target (full quantity). Regime=cash ⇒ target
//     is empty ⇒ everything held becomes a SELL.
//   - BUY: target symbol not held; qty = floor(capital/topK/lastClose).
//   - HOLD: in both — never resized (no-rebalance decision).
//   - FROZEN: held and in frozen — reported, never traded either way.
//
// lastClose must contain prices for all target symbols; SELLs of symbols with
// no known price get ApproxValue 0 (still listed — user knows their position).
func BuildOrders(holdings []Holding, target []string, lastClose map[string]float64, capitalAUD float64, topK int, frozen map[string]bool) DiffResult {
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
		if frozen[h.Symbol] {
			res.Frozen = append(res.Frozen, h.Symbol)
			continue
		}
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
			ApproxValue: orderApproxValue(h.Qty, px),
		})
	}
	if topK <= 0 {
		topK = len(target)
	}
	perSlot := 0.0
	if topK > 0 {
		perSlot = capitalAUD / float64(topK)
	}
	for _, t := range target {
		if _, ok := held[t]; ok {
			continue
		}
		if frozen[t] {
			continue // unreachable via Run (frozen names leave the rankings), but
			// BuildOrders is called directly in tests and must not emit a buy.
		}
		px := lastClose[t]
		if px <= 0 {
			continue // no price, no buy — flagged upstream as excluded
		}
		qty := int64(math.Floor(perSlot / px))
		if qty <= 0 {
			continue // slot smaller than one share — skip, noted upstream
		}
		res.Buys = append(res.Buys, Order{
			Side:        "BUY",
			Symbol:      t,
			Qty:         qty,
			LastClose:   px,
			ApproxValue: orderApproxValue(qty, px),
		})
	}
	sort.Slice(res.Sells, func(i, j int) bool { return res.Sells[i].Symbol < res.Sells[j].Symbol })
	sort.Slice(res.Buys, func(i, j int) bool { return res.Buys[i].Symbol < res.Buys[j].Symbol })
	sort.Strings(res.Holds)
	sort.Strings(res.Frozen)
	return res
}

func orderApproxValue(qty int64, price float64) float64 {
	return math.Round(float64(qty) * price)
}
