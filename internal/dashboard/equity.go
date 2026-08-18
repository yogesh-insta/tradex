package dashboard

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/yogesh-insta/tradex/internal/momentum"
)

// EquityPoint is one trading day's marked book.
type EquityPoint struct {
	Date      string  `json:"date"` // YYYY-MM-DD
	Cost      float64 `json:"cost"`
	Value     float64 `json:"value"`
	NHoldings int     `json:"n_holdings"`
	Priced    int     `json:"priced,omitempty"`
	BookAsOf  string  `json:"book_as_of,omitempty"`
	Source    string  `json:"source"` // reconstructed | marked_book | live | mock
}

// EquityEvent is a marker on the growth chart (book edit or rotator run).
type EquityEvent struct {
	Date  string `json:"date"`
	Kind  string `json:"kind"` // book | algo
	Label string `json:"label"`
}

// FollowOrder is one recommended order vs the current book.
type FollowOrder struct {
	Side   string `json:"side"`
	Symbol string `json:"symbol"`
	Qty    int64  `json:"qty"`
	Status string `json:"status"` // executed | pending
}

// Allocation is one name's share of the current book.
type Allocation struct {
	Symbol     string  `json:"symbol"`
	Qty        float64 `json:"qty"`
	Avg        float64 `json:"avg"`
	Last       float64 `json:"last,omitempty"`
	Cost       float64 `json:"cost"`
	Value      float64 `json:"value,omitempty"`
	Unrealized float64 `json:"unrealized,omitempty"`
	Weight     float64 `json:"weight,omitempty"`
}

// EquityHistory is the durable GCS record plus the extra fields the UI needs.
// FollowThrough / Allocation are computed on read and not required on disk.
type EquityHistory struct {
	Lane          string        `json:"lane"`
	Currency      string        `json:"currency"`
	UpdatedAt     string        `json:"updated_at,omitempty"`
	Note          string        `json:"note,omitempty"`
	Points        []EquityPoint `json:"points"`
	Events        []EquityEvent `json:"events,omitempty"`
	FollowThrough []FollowOrder `json:"follow_through,omitempty"`
	Allocation    []Allocation  `json:"allocation,omitempty"`
}

type bookHolding struct {
	Symbol string
	Qty    float64
	Avg    float64
}

type bookSlice struct {
	AsOf     string
	Holdings []bookHolding
	Label    string // event label when this book takes effect
}

func parseHoldings(v any) []bookHolding {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]bookHolding, 0, len(arr))
	for _, item := range arr {
		h, ok := item.(map[string]any)
		if !ok {
			continue
		}
		sym, _ := h["symbol"].(string)
		if sym == "" {
			sym, _ = h["ticker"].(string)
		}
		qty, okQty := jsonNumber(h["qty"])
		avg, okAvg := jsonNumber(h["avg_price"])
		if strings.TrimSpace(sym) == "" || !okQty || qty <= 0 {
			continue
		}
		if !okAvg {
			avg = 0
		}
		out = append(out, bookHolding{Symbol: strings.ToUpper(strings.TrimSpace(sym)), Qty: qty, Avg: avg})
	}
	return out
}

func bookFor(slices []bookSlice, day string) (bookSlice, bool) {
	var found bookSlice
	ok := false
	for _, s := range slices {
		if s.AsOf <= day && (!ok || s.AsOf >= found.AsOf) {
			found = s
			ok = true
		}
	}
	return found, ok
}

func markHoldings(holdings []bookHolding, prices map[string]float64) (cost, value float64, priced int) {
	for _, h := range holdings {
		c := h.Qty * h.Avg
		cost += c
		px, ok := prices[h.Symbol]
		if !ok || px <= 0 {
			continue
		}
		value += h.Qty * px
		priced++
	}
	return cost, value, priced
}

func closeOnDate(candles []momentum.Candle, day string) (float64, bool) {
	for i := len(candles) - 1; i >= 0; i-- {
		d := candles[i].Date.UTC().Format("2006-01-02")
		if d == day && candles[i].Close > 0 {
			return candles[i].Close, true
		}
		if d < day {
			return 0, false
		}
	}
	return 0, false
}

// reconstruct marks each book slice across every trading day present in the
// Yahoo series. Days with no close for any holding are skipped.
func reconstruct(slices []bookSlice, series map[string][]momentum.Candle, source string) []EquityPoint {
	if len(slices) == 0 || len(series) == 0 {
		return nil
	}
	days := map[string]struct{}{}
	for _, candles := range series {
		for _, c := range candles {
			if c.Close > 0 {
				days[c.Date.UTC().Format("2006-01-02")] = struct{}{}
			}
		}
	}
	dates := make([]string, 0, len(days))
	start := slices[0].AsOf
	for _, s := range slices {
		if s.AsOf < start {
			start = s.AsOf
		}
	}
	for d := range days {
		if d >= start {
			dates = append(dates, d)
		}
	}
	sort.Strings(dates)

	out := make([]EquityPoint, 0, len(dates))
	for _, day := range dates {
		book, ok := bookFor(slices, day)
		if !ok || len(book.Holdings) == 0 {
			continue
		}
		prices := make(map[string]float64, len(book.Holdings))
		for _, h := range book.Holdings {
			if px, ok := closeOnDate(series[h.Symbol], day); ok {
				prices[h.Symbol] = px
			}
		}
		cost, value, priced := markHoldings(book.Holdings, prices)
		if priced == 0 {
			continue
		}
		out = append(out, EquityPoint{
			Date:      day,
			Cost:      math.Round(cost),
			Value:     math.Round(value),
			NHoldings: len(book.Holdings),
			Priced:    priced,
			BookAsOf:  book.AsOf,
			Source:    source,
		})
	}
	return out
}

func eventsFromBooks(slices []bookSlice) []EquityEvent {
	out := make([]EquityEvent, 0, len(slices))
	for _, s := range slices {
		if s.Label == "" {
			continue
		}
		out = append(out, EquityEvent{Date: s.AsOf, Kind: "book", Label: s.Label})
	}
	return out
}

func mergeHistory(base, extra EquityHistory) EquityHistory {
	out := base
	if extra.Lane != "" {
		out.Lane = extra.Lane
	}
	if extra.Currency != "" {
		out.Currency = extra.Currency
	}
	if extra.Note != "" {
		out.Note = extra.Note
	}
	if extra.UpdatedAt != "" {
		out.UpdatedAt = extra.UpdatedAt
	}

	byDate := make(map[string]EquityPoint, len(out.Points)+len(extra.Points))
	for _, p := range out.Points {
		byDate[p.Date] = p
	}
	for _, p := range extra.Points {
		if p.Date == "" {
			continue
		}
		prev, ok := byDate[p.Date]
		if !ok || sourceRank(p.Source) >= sourceRank(prev.Source) {
			byDate[p.Date] = p
		}
	}
	dates := make([]string, 0, len(byDate))
	for d := range byDate {
		dates = append(dates, d)
	}
	sort.Strings(dates)
	out.Points = make([]EquityPoint, 0, len(dates))
	for _, d := range dates {
		out.Points = append(out.Points, byDate[d])
	}

	seen := map[string]bool{}
	ev := make([]EquityEvent, 0, len(out.Events)+len(extra.Events))
	add := func(e EquityEvent) {
		if e.Date == "" || e.Label == "" {
			return
		}
		k := e.Date + "|" + e.Kind + "|" + e.Label
		if seen[k] {
			return
		}
		seen[k] = true
		ev = append(ev, e)
	}
	for _, e := range out.Events {
		add(e)
	}
	for _, e := range extra.Events {
		add(e)
	}
	sort.Slice(ev, func(i, j int) bool {
		if ev[i].Date != ev[j].Date {
			return ev[i].Date < ev[j].Date
		}
		return ev[i].Kind < ev[j].Kind
	})
	out.Events = ev
	return out
}

func sourceRank(s string) int {
	switch s {
	case "live":
		return 3
	case "mock":
		return 2
	case "reconstructed", "marked_book":
		return 1
	default:
		return 0
	}
}

func livePoint(day string, holdings []bookHolding, prices map[string]float64, bookAsOf string) (EquityPoint, bool) {
	cost, value, priced := markHoldings(holdings, prices)
	if priced == 0 {
		return EquityPoint{}, false
	}
	return EquityPoint{
		Date:      day,
		Cost:      math.Round(cost),
		Value:     math.Round(value),
		NHoldings: len(holdings),
		Priced:    priced,
		BookAsOf:  bookAsOf,
		Source:    "live",
	}, true
}

func allocations(holdings []bookHolding, prices map[string]float64) []Allocation {
	_, totalValue, _ := markHoldings(holdings, prices)
	out := make([]Allocation, 0, len(holdings))
	for _, h := range holdings {
		a := Allocation{Symbol: h.Symbol, Qty: h.Qty, Avg: h.Avg, Cost: math.Round(h.Qty * h.Avg)}
		if px, ok := prices[h.Symbol]; ok && px > 0 {
			a.Last = px
			a.Value = math.Round(h.Qty * px)
			a.Unrealized = math.Round(a.Value - a.Cost)
			if totalValue > 0 {
				a.Weight = (h.Qty * px) / totalValue
			}
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Value > out[j].Value })
	return out
}

func pricesFromHoldingsMap(holdings []any) map[string]float64 {
	out := map[string]float64{}
	for _, item := range holdings {
		h, ok := item.(map[string]any)
		if !ok {
			continue
		}
		sym, _ := h["symbol"].(string)
		if sym == "" {
			sym, _ = h["ticker"].(string)
		}
		px, ok := jsonNumber(h["last_price"])
		if !ok || px <= 0 || sym == "" {
			continue
		}
		out[strings.ToUpper(sym)] = px
	}
	return out
}

func lastCloses(series map[string][]momentum.Candle) map[string]float64 {
	out := make(map[string]float64, len(series))
	for sym, candles := range series {
		if len(candles) == 0 {
			continue
		}
		px := candles[len(candles)-1].Close
		if px > 0 {
			out[sym] = px
		}
	}
	return out
}

func followThrough(orders []any, held map[string]bool) []FollowOrder {
	out := make([]FollowOrder, 0, len(orders))
	for _, item := range orders {
		o, ok := item.(map[string]any)
		if !ok {
			continue
		}
		side, _ := o["side"].(string)
		sym, _ := o["symbol"].(string)
		if side == "" || sym == "" {
			continue
		}
		qty, _ := jsonNumber(o["qty"])
		status := "pending"
		switch strings.ToUpper(side) {
		case "SELL":
			if !held[sym] {
				status = "executed"
			}
		case "BUY":
			if held[sym] {
				status = "executed"
			}
		}
		out = append(out, FollowOrder{
			Side:   strings.ToUpper(side),
			Symbol: strings.ToUpper(sym),
			Qty:    int64(qty),
			Status: status,
		})
	}
	return out
}

func heldSet(holdings []bookHolding) map[string]bool {
	out := make(map[string]bool, len(holdings))
	for _, h := range holdings {
		out[h.Symbol] = true
	}
	return out
}

func algoEventFromReport(report map[string]any) (EquityEvent, bool) {
	month, _ := report["month"].(string)
	runAt, _ := report["run_at"].(string)
	date := month
	if t, err := time.Parse(time.RFC3339, runAt); err == nil {
		date = t.UTC().Format("2006-01-02")
	} else if len(month) == 7 {
		date = month + "-01"
	}
	orders, _ := report["orders"].([]any)
	if date == "" || len(orders) == 0 {
		return EquityEvent{}, false
	}
	parts := make([]string, 0, len(orders))
	for _, item := range orders {
		o, ok := item.(map[string]any)
		if !ok {
			continue
		}
		side, _ := o["side"].(string)
		sym, _ := o["symbol"].(string)
		if side == "" || sym == "" {
			continue
		}
		parts = append(parts, strings.ToUpper(side)+" "+strings.ToUpper(sym))
	}
	if len(parts) == 0 {
		return EquityEvent{}, false
	}
	return EquityEvent{Date: date, Kind: "algo", Label: strings.Join(parts, ", ")}, true
}

func sparseCostPoints(slices []bookSlice) []EquityPoint {
	out := make([]EquityPoint, 0, len(slices))
	for _, s := range slices {
		cost, _, _ := markHoldings(s.Holdings, nil)
		if cost <= 0 {
			continue
		}
		out = append(out, EquityPoint{
			Date:      s.AsOf,
			Cost:      math.Round(cost),
			Value:     math.Round(cost),
			NHoldings: len(s.Holdings),
			BookAsOf:  s.AsOf,
			Source:    "book",
		})
	}
	return out
}
