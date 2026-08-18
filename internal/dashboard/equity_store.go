package dashboard

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yogesh-insta/tradex/internal/momentum"
	"github.com/yogesh-insta/tradex/internal/yahoo"
)

const (
	nseEquityURI = "gs://tradex-demo-state/nserotator/equity-history.json"
	etfEquityURI = "gs://tradex-demo-state/etfmonitor/equity-history.json"

	equityCacheTTL   = 3 * time.Minute
	equityHistoryYrs = 1
	fetchWorkers     = 8
)

// DailyFetcher loads Yahoo daily closes. *yahoo.YahooClient satisfies it.
type DailyFetcher interface {
	FetchDaily(ctx context.Context, symbol string, years int) (yahoo.Series, error)
}

func (s *Service) nseEquity(ctx context.Context, report map[string]any) EquityHistory {
	if v, ok := s.cache.Get("nse:equity"); ok {
		if h, ok := v.(EquityHistory); ok {
			return h
		}
	}
	hist := s.loadHistory(ctx, nseEquityURI)
	hist.Lane = "nse"
	hist.Currency = "INR"

	pf, _ := report["portfolio"].(map[string]any)
	var liveHoldings []bookHolding
	bookAsOf := ""
	if pf != nil {
		liveHoldings = parseHoldings(pf["holdings"])
		bookAsOf, _ = pf["as_of"].(string)
	}
	books := nseBookHistory()
	if bookAsOf != "" && len(liveHoldings) > 0 {
		last := &books[len(books)-1]
		if bookAsOf == last.AsOf {
			last.Holdings = liveHoldings
		} else if bookAsOf > last.AsOf {
			books = append(books, bookSlice{AsOf: bookAsOf, Holdings: liveHoldings, Label: "Current book"})
		}
	}

	needFill := equityNeedsFill(hist, s.dayIn("Asia/Kolkata"))
	if needFill && s.deps.NSESeries != nil {
		symbols := bookSymbols(books)
		series := s.fetchSeries(ctx, s.deps.NSESeries, symbols)
		filled := reconstruct(books, series, "reconstructed")
		hist = mergeHistory(hist, EquityHistory{Lane: "nse", Currency: "INR", Points: filled})
	} else if len(hist.Points) == 0 {
		hist.Points = sparseCostPoints(books)
		hist.Note = "Cost basis only — Yahoo history not yet filled."
	}

	day := s.dayIn("Asia/Kolkata")
	if pf != nil {
		if pt, ok := livePoint(day, liveHoldings, pricesFromHoldingsMap(holdingsAny(pf)), bookAsOf); ok {
			hist = mergeHistory(hist, EquityHistory{Points: []EquityPoint{pt}})
		}
	}

	hist.Events = mergeHistory(hist, EquityHistory{Events: eventsFromBooks(nseBookHistory())}).Events
	if ev, ok := algoEventFromReport(report); ok {
		hist = mergeHistory(hist, EquityHistory{Events: []EquityEvent{ev}})
	}
	if len(liveHoldings) > 0 {
		prices := pricesFromHoldingsMap(holdingsAny(pf))
		hist.Allocation = allocations(liveHoldings, prices)
		if orders, _ := report["orders"].([]any); len(orders) > 0 {
			hist.FollowThrough = followThrough(orders, heldSet(liveHoldings))
		}
	}
	if hist.Note == "" {
		hist.Note = "Daily marks from Yahoo closes on each book slice, plus today's live quote. ALCODIS excluded."
	}
	hist.UpdatedAt = s.now().UTC().Format(time.RFC3339)

	s.persistHistory(ctx, nseEquityURI, hist)
	s.cache.Set("nse:equity", hist, equityCacheTTL)
	return hist
}

func (s *Service) etfEquity(ctx context.Context, holdings []bookHolding, asOf string) EquityHistory {
	if v, ok := s.cache.Get("etf:equity"); ok {
		if h, ok := v.(EquityHistory); ok {
			return h
		}
	}
	hist := s.loadHistory(ctx, etfEquityURI)
	hist.Lane = "etf"
	hist.Currency = "AUD"

	books := []bookSlice{}
	if len(holdings) > 0 {
		start := asOf
		if start == "" {
			start = s.dayIn("Australia/Sydney")
		}
		// Mark the current sleeve backward so the graph is useful on day one.
		// Caption in the UI makes this distinct from actual past holdings.
		books = []bookSlice{{AsOf: "2026-07-24", Holdings: holdings, Label: "Current ETF sleeve (marked historically)"}}
		_ = start
	}

	if s.deps.ETFSeries != nil && len(holdings) > 0 {
		series := s.fetchSeries(ctx, s.deps.ETFSeries, bookSymbols(books))
		filled := reconstruct(books, series, "marked_book")
		hist = mergeHistory(hist, EquityHistory{Lane: "etf", Currency: "AUD", Points: filled})
		prices := lastCloses(series)
		hist.Allocation = allocations(holdings, prices)
		day := s.dayIn("Australia/Sydney")
		if pt, ok := livePoint(day, holdings, prices, asOf); ok {
			pt.Source = "live"
			hist = mergeHistory(hist, EquityHistory{Points: []EquityPoint{pt}})
		}
	} else if len(hist.Points) == 0 {
		hist.Points = sparseCostPoints(books)
	}

	hist.Events = eventsFromBooks(books)
	hist.Note = "Current sleeve marked on Yahoo daily closes. Qty inferred from the 2026-08-18 screenshot; avg cost unknown so invested = 0 until you fill avg_price."
	hist.UpdatedAt = s.now().UTC().Format(time.RFC3339)

	s.persistHistory(ctx, etfEquityURI, hist)
	s.cache.Set("etf:equity", hist, equityCacheTTL)
	return hist
}

func (s *Service) loadHistory(ctx context.Context, uri string) EquityHistory {
	if s.deps.Objects == nil {
		return EquityHistory{}
	}
	raw, err := s.deps.Objects.Fetch(ctx, uri)
	if err != nil || len(raw) == 0 {
		return EquityHistory{}
	}
	var h EquityHistory
	if json.Unmarshal(raw, &h) != nil {
		return EquityHistory{}
	}
	return h
}

func (s *Service) persistHistory(ctx context.Context, uri string, hist EquityHistory) {
	putter, ok := s.deps.Objects.(ObjectPutter)
	if !ok {
		return
	}
	disk := hist
	disk.FollowThrough = nil
	disk.Allocation = nil
	raw, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return
	}
	raw = append(raw, '\n')
	if err := putter.Put(ctx, uri, raw); err != nil && s.log != nil {
		s.log.Warn("equity history persist failed", "uri", uri, "error", err)
	}
}

func equityNeedsFill(h EquityHistory, today string) bool {
	newest := ""
	filled := 0
	for _, p := range h.Points {
		if p.Source == "reconstructed" || p.Source == "marked_book" {
			filled++
		}
		if p.Date > newest {
			newest = p.Date
		}
	}
	if filled < 5 {
		return true
	}
	cutoff := today
	if t, err := time.Parse("2006-01-02", today); err == nil {
		cutoff = t.AddDate(0, 0, -7).Format("2006-01-02")
	}
	return newest < cutoff
}

func bookSymbols(slices []bookSlice) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range slices {
		for _, h := range s.Holdings {
			if !seen[h.Symbol] {
				seen[h.Symbol] = true
				out = append(out, h.Symbol)
			}
		}
	}
	sort.Strings(out)
	return out
}

func holdingsAny(pf map[string]any) []any {
	arr, _ := pf["holdings"].([]any)
	return arr
}

func (s *Service) dayIn(locName string) string {
	loc, err := time.LoadLocation(locName)
	if err != nil {
		loc = time.UTC
	}
	return s.now().In(loc).Format("2006-01-02")
}

func (s *Service) fetchSeries(ctx context.Context, f DailyFetcher, symbols []string) map[string][]momentum.Candle {
	out := make(map[string][]momentum.Candle, len(symbols))
	if f == nil || len(symbols) == 0 {
		return out
	}
	type result struct {
		sym     string
		candles []momentum.Candle
	}
	ch := make(chan result, len(symbols))
	sem := make(chan struct{}, fetchWorkers)
	var wg sync.WaitGroup
	for _, sym := range symbols {
		wg.Add(1)
		go func(sym string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ser, err := f.FetchDaily(ctx, sym, equityHistoryYrs)
			if err != nil || len(ser.Candles) == 0 {
				return
			}
			ch <- result{sym: strings.ToUpper(sym), candles: ser.Candles}
		}(sym)
	}
	go func() {
		wg.Wait()
		close(ch)
	}()
	for r := range ch {
		out[r.sym] = r.candles
	}
	return out
}
