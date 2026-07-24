package dashboard

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/yogesh-insta/tradex/internal/nserotator"
)

const nseQuotesCacheTTL = 3 * time.Minute

// NSEQuoter returns live NSE prices for dashboard portfolio marks.
type NSEQuoter interface {
	FetchPrices(ctx context.Context, symbols []string) map[string]float64
}

// YahooNSEQuoter adapts the nserotator Yahoo client (spark quotes).
type YahooNSEQuoter struct {
	Client *nserotator.YahooClient
}

// FetchPrices implements NSEQuoter.
func (q YahooNSEQuoter) FetchPrices(ctx context.Context, symbols []string) map[string]float64 {
	if q.Client == nil {
		return nil
	}
	details := q.Client.FetchQuoteDetails(ctx, symbols)
	out := make(map[string]float64, len(details))
	for sym, d := range details {
		if d.Price > 0 {
			out[sym] = d.Price
		}
	}
	return out
}

// StaticNSEQuoter is a test/mock quoter with fixed prices per symbol.
type StaticNSEQuoter map[string]float64

// FetchPrices implements NSEQuoter.
func (s StaticNSEQuoter) FetchPrices(_ context.Context, symbols []string) map[string]float64 {
	out := make(map[string]float64, len(symbols))
	for _, sym := range symbols {
		if px, ok := s[sym]; ok && px > 0 {
			out[sym] = px
		}
	}
	return out
}

func (s *Service) fetchNSEQuotes(ctx context.Context, symbols []string) map[string]float64 {
	if s.deps.NSEQuoter == nil || len(symbols) == 0 {
		return nil
	}
	uniq := append([]string(nil), symbols...)
	sort.Strings(uniq)
	key := "nse:quotes:" + strings.Join(uniq, ",")
	if v, ok := s.cache.Get(key); ok {
		if prices, ok := v.(map[string]float64); ok {
			return prices
		}
	}
	prices := s.deps.NSEQuoter.FetchPrices(ctx, uniq)
	if prices == nil {
		prices = map[string]float64{}
	}
	s.cache.Set(key, prices, nseQuotesCacheTTL)
	return prices
}

func enrichPortfolioQuotes(portfolio map[string]any, prices map[string]float64) {
	holdings, ok := portfolio["holdings"].([]any)
	if !ok || len(holdings) == 0 {
		return
	}
	var totalCost, totalValue float64
	var priced int
	for _, item := range holdings {
		h, ok := item.(map[string]any)
		if !ok {
			continue
		}
		sym, _ := h["symbol"].(string)
		avg, okAvg := jsonNumber(h["avg_price"])
		qty, okQty := jsonNumber(h["qty"])
		if sym == "" || !okAvg || !okQty || avg <= 0 || qty <= 0 {
			continue
		}
		px, ok := prices[sym]
		if !ok || px <= 0 {
			continue
		}
		cost := qty * avg
		value := qty * px
		h["last_price"] = px
		h["pct_vs_avg"] = (px - avg) / avg
		h["unrealized_inr"] = math.Round(value-cost)
		totalCost += cost
		totalValue += value
		priced++
	}
	if priced == 0 || totalCost <= 0 {
		return
	}
	portfolio["quote_summary"] = map[string]any{
		"priced_holdings": priced,
		"total_holdings":  len(holdings),
		"cost_inr":        math.Round(totalCost),
		"value_inr":       math.Round(totalValue),
		"unrealized_inr":  math.Round(totalValue - totalCost),
		"pct_vs_avg":      (totalValue - totalCost) / totalCost,
	}
}

func jsonNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}
