package nserotator

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yogesh-insta/tradex/internal/calendar"
)

// MessageSender abstracts Telegram (satisfied by *calendar.TelegramClient).
type MessageSender interface {
	SendMessage(ctx context.Context, text string) error
}

// Deps wires the run's collaborators (all injectable for tests).
type Deps struct {
	Yahoo    *YahooClient
	Telegram MessageSender
	Store    *StateStore
	Holidays *calendar.Holidays
	Log      *slog.Logger
	Now      func() time.Time
	// FetchOfficial optional override for drift checks (nil = real NSE fetch
	// with the default HTTP client).
	FetchOfficial func(ctx context.Context) ([]string, error)
	// FetchNSEUniverse optional override for company names / drift (nil = live NSE CSV).
	FetchNSEUniverse func(ctx context.Context) (map[string]string, error)
}

// RunParams carries the resolved config into Run.
type RunParams struct {
	Universe        []string
	LookbackMonths  int
	TopK            int
	RegimeEMADays   int
	Market          string
	Force           bool
	DriftCheck      bool
	ExcludedSymbols []string
	// ExitLookbackMonths / ExitRankN drive the exit hysteresis: a holding is
	// sold only when outside the top ExitRankN on BOTH lookbacks. See
	// BuildTarget. Zero values fall back to plain top-K rotation.
	ExitLookbackMonths int
	ExitRankN          int
	// RegimeFilter gates the book to cash below the index EMA. When false the
	// regime is still computed and reported but never empties the target.
	RegimeFilter bool
}

const (
	indexSymbol           = "^NSEI"
	historyYears          = 5
	staleAfter            = 10 * 24 * time.Hour // ~7 trading days
	badJumpMaxMove        = 0.5
	portfolioStale        = 45 * 24 * time.Hour
	maxFetchFailFrac      = 0.2
	fetchWorkers          = 8
	topRankedDisplayCount = 20
	notifyTimeout         = 30 * time.Second
)

// RunResult summarizes a completed run.
type RunResult struct {
	Skipped bool // not the last trading day
	Month   string
	Orders  int
}

// Run executes one advisory cycle per spec 20. On failure it best-effort
// notifies Telegram ("RUN FAILED — ...") and returns the error (exit non-zero).
func Run(ctx context.Context, p RunParams, d Deps) (RunResult, error) {
	now := d.Now()
	ist, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		return RunResult{}, fmt.Errorf("load IST: %w", err)
	}
	local := now.In(ist)
	month := local.Format("2006-01")

	if !p.Force && !IsLastTradingDayOfMonth(now, d.Holidays, p.Market, ist) {
		d.Log.Info("not the last trading day — exiting", "date", local.Format("2006-01-02"))
		return RunResult{Skipped: true, Month: month}, nil
	}

	res, err := runCore(ctx, p, d, now, ist, month)
	if err != nil {
		notifyFailure(ctx, d, month, err)
		return RunResult{Month: month}, err
	}
	return res, nil
}

func notifyFailure(ctx context.Context, d Deps, month string, cause error) {
	if d.Telegram == nil {
		return
	}
	text := fmt.Sprintf("NSE ROTATOR %s — RUN FAILED\n%v\nNo orders. Investigate before month-end.", month, cause)
	notifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), notifyTimeout)
	defer cancel()
	for i := 0; i < 3; i++ {
		if err := d.Telegram.SendMessage(notifyCtx, text); err == nil {
			return
		}
		if err := retryLinearBackoff(notifyCtx, i+1); err != nil {
			break
		}
	}
	d.Log.Error("failure notification could not be delivered", "cause", cause)
}

func runCore(ctx context.Context, p RunParams, d Deps, now time.Time, ist *time.Location, month string) (RunResult, error) {
	var warnings []string
	if len(p.Universe) == 0 {
		return RunResult{}, fmt.Errorf("universe is empty")
	}
	// 1. Portfolio (user-maintained).
	pf, err := d.Store.ReadPortfolio(ctx)
	if err != nil {
		return RunResult{}, fmt.Errorf("portfolio state: %w", err)
	}
	if asOf := pf.AsOfTime(); asOf.IsZero() || now.Sub(asOf) > portfolioStale {
		warnings = append(warnings, fmt.Sprintf("STALE PORTFOLIO: as_of=%q — update portfolio.json", pf.AsOf))
	}

	// 2. Regime from the index.
	idx, err := d.Yahoo.FetchDaily(ctx, indexSymbol, historyYears)
	if err != nil {
		return RunResult{}, fmt.Errorf("index data (%s): %w", indexSymbol, err)
	}
	invested, niftyClose, niftyEMA, ok := RegimeInvested(idx, p.RegimeEMADays)
	if !ok {
		return RunResult{}, fmt.Errorf("index data too short for EMA%d", p.RegimeEMADays)
	}

	// 3. Fetch universe + held symbols (bounded concurrency).
	heldSyms := map[string]bool{}
	for _, h := range pf.Holdings {
		heldSyms[h.Symbol] = true
	}
	all := map[string]bool{}
	for _, s := range p.Universe {
		all[s] = true
	}
	for s := range heldSyms {
		all[s] = true
	}
	series, fetchErrs := fetchAll(ctx, d.Yahoo, keys(all))

	universeFails := 0
	for _, s := range p.Universe {
		if _, ok := series[s]; !ok {
			universeFails++
		}
	}
	if frac := float64(universeFails) / float64(len(p.Universe)); frac > maxFetchFailFrac {
		return RunResult{}, fmt.Errorf("data: %d/%d universe symbols failed to fetch", universeFails, len(p.Universe))
	}

	// 4. Eligibility + momentum scores.
	var excluded []string
	scores := map[string]float64{}
	scoresSlow := map[string]float64{} // exit lookback (12m) — keeps holdings alive
	lastClose := map[string]float64{}
	for sym, s := range series {
		lastClose[sym] = s.Candles[len(s.Candles)-1].Close
	}
	for _, sym := range p.Universe {
		s, ok := series[sym]
		if !ok {
			excluded = append(excluded, sym+" (fetch failed)")
			continue
		}
		if Stale(s, now, staleAfter) {
			excluded = append(excluded, sym+" (stale data)")
			continue
		}
		// The screen must reach back over the longer of the two lookbacks, or
		// stale data a year old could keep a holding alive via the slow list.
		jumpWindow := BadJumpWindowDays(max(p.LookbackMonths, p.ExitLookbackMonths))
		if BadJumpUp(s, jumpWindow, badJumpMaxMove) {
			excluded = append(excluded, sym+" (>50% daily up-move — bad data?)")
			continue
		}
		if LargeDownJump(s, jumpWindow, badJumpMaxMove) {
			warnings = append(warnings, fmt.Sprintf("%s: >50%% daily down-move (likely corporate action) — still ranked", sym))
		}
		me := MonthEnds(s)
		m, ok := MomentumReturn(me, p.LookbackMonths)
		if !ok {
			continue // insufficient history — silent, expected for recent listings
		}
		scores[sym] = m
		if p.ExitLookbackMonths > 0 {
			// Absent is fine: a recent listing simply never joins the slow list.
			if mSlow, ok := MomentumReturn(me, p.ExitLookbackMonths); ok {
				scoresSlow[sym] = mSlow
			}
		}
	}
	for _, sym := range p.ExcludedSymbols {
		excluded = append(excluded, filterPolicyExcluded(scores, sym)...)
		// Must also drop from the slow list, or a blocked name would be kept
		// alive by it — the exact opposite of what the blocklist is for.
		filterPolicyExcluded(scoresSlow, sym)
	}
	// Held symbols with stale data are a loud warning (corporate action?).
	for _, h := range pf.Holdings {
		s, ok := series[h.Symbol]
		if !ok || Stale(s, now, staleAfter) {
			warnings = append(warnings, fmt.Sprintf("HELD %s: no fresh price data — possible rename/corporate action", h.Symbol))
		}
	}

	ranked := Rank(scores)
	rankedSlow := Rank(scoresSlow)
	slowIdx := RankIndex(rankedSlow)

	// 5. Target picks. Entry on the fast list, exit only when outside the
	// buffer on BOTH lists — see BuildTarget. With RegimeFilter on, a below-EMA
	// index leaves the target empty and BuildOrders sells the whole book.
	var target []string
	if ShouldHoldEquity(invested, p.RegimeFilter) {
		target = BuildTarget(ranked, rankedSlow, pf.Holdings, p.TopK, p.ExitRankN)
	}
	diff := BuildOrders(pf.Holdings, target, lastClose, pf.TotalCapitalINR, p.TopK)

	// Company name, live price, and market cap for top-ranked list and orders (best-effort).
	// The displayed list must reach the exit buffer, or a holding kept alive at
	// rank 25 would never appear and the hold could not be explained.
	topDisplay := head(ranked, max(topRankedDisplayCount, p.ExitRankN))
	quoteSymbols := symbolsForQuotes(topDisplay, diff)
	quotes := d.Yahoo.FetchQuoteDetails(ctx, quoteSymbols)
	companyNames := fetchNSECompanyNames(ctx, d)
	resolveName := func(sym string) string {
		if s, ok := series[sym]; ok && strings.TrimSpace(s.CompanyName) != "" {
			return s.CompanyName
		}
		if companyNames != nil {
			if name := companyNames[sym]; name != "" {
				return name
			}
		}
		return quotes[sym].CompanyName
	}
	enrichRanked := func(r *Ranked) {
		q := quotes[r.Symbol]
		r.CompanyName = resolveName(r.Symbol)
		r.MarketCap = q.MarketCap
		r.MomentumSlow = scoresSlow[r.Symbol] // zero when unranked on the slow list
		r.RankSlow = slowIdx[r.Symbol]
		if q.Price > 0 {
			r.LastClose = q.Price
		} else if p, ok := lastClose[r.Symbol]; ok {
			r.LastClose = p
		}
	}
	for i := range topDisplay {
		enrichRanked(&topDisplay[i])
	}
	enrichOrder := func(o *Order) {
		q := quotes[o.Symbol]
		o.CompanyName = resolveName(o.Symbol)
		o.MarketCap = q.MarketCap
		if q.Price > 0 {
			o.LastClose = q.Price
			o.ApproxValue = orderApproxValue(o.Qty, q.Price)
		}
	}
	for i := range diff.Sells {
		enrichOrder(&diff.Sells[i])
	}
	for i := range diff.Buys {
		enrichOrder(&diff.Buys[i])
	}

	for _, t := range target {
		found := false
		for _, b := range diff.Buys {
			if b.Symbol == t {
				found = true
				break
			}
		}
		for _, h := range diff.Holds {
			if h == t {
				found = true
				break
			}
		}
		if !found {
			warnings = append(warnings, fmt.Sprintf("BUY %s skipped: one share exceeds the per-slot budget", t))
		}
	}
	if len(fetchErrs) > 0 {
		d.Log.Warn("some fetches failed", "count", len(fetchErrs))
	}

	// 6. Universe drift (best-effort, never blocks).
	if p.DriftCheck {
		official := nseSymbolList(companyNames)
		if official == nil {
			fetchOfficial := d.FetchOfficial
			if fetchOfficial == nil {
				fetchOfficial = func(ctx context.Context) ([]string, error) {
					return FetchConstituents(ctx, nil, "")
				}
			}
			var err error
			official, err = fetchOfficial(ctx)
			if err != nil {
				d.Log.Warn("universe drift check failed", "error", err)
				official = nil
			}
		}
		if official != nil {
			if added, removed := DiffUniverse(p.Universe, official); len(added)+len(removed) > 0 {
				warnings = append(warnings, fmt.Sprintf(
					"UNIVERSE DRIFT — update config/universe-nse200.yaml. official adds: %s | official drops: %s",
					joinOrNone(added), joinOrNone(removed)))
			}
		}
	}

	// 7. Holiday-file staleness (December ritual).
	if int(inIST(now, ist).Month()) == 12 && d.Holidays.Year() <= inIST(now, ist).Year() {
		warnings = append(warnings, fmt.Sprintf("holidays-nse.yaml covers %d — add next year before January", d.Holidays.Year()))
	}

	// 8. Compose, deliver, persist.
	fastIdx := RankIndex(ranked)
	holdsInfo := make([]HoldInfo, 0, len(diff.Holds))
	for _, h := range diff.Holds {
		holdsInfo = append(holdsInfo, HoldInfo{Symbol: h, Rank: fastIdx[h], RankSlow: slowIdx[h]})
	}
	rec := Recommendation{
		RunAt:          now.UTC().Format(time.RFC3339),
		Month:          month,
		RegimeInvested: invested,
		RegimeFilter:   &p.RegimeFilter,
		NiftyClose:     niftyClose,
		NiftyEMA:       niftyEMA,
		Orders:         append(append([]Order{}, diff.Sells...), diff.Buys...),
		Holds:          diff.Holds,
		HoldsInfo:      holdsInfo,
		TopRanked:      topDisplay, // already enriched in place
		Params: RunParamsRecord{
			LookbackMonths:     p.LookbackMonths,
			ExitLookbackMonths: p.ExitLookbackMonths,
			TopK:               p.TopK,
			ExitRankN:          p.ExitRankN,
		},
		Excluded: excluded,
		Warnings: warnings,
	}
	rec.MessageText = FormatMessage(rec, p.LookbackMonths, p.RegimeEMADays)

	if err := d.Store.WriteRecommendation(ctx, rec); err != nil {
		return RunResult{}, fmt.Errorf("write recommendation: %w", err)
	}
	if err := sendWithRetry(ctx, d.Telegram, rec.MessageText); err != nil {
		return RunResult{}, fmt.Errorf("telegram delivery (recommendation IS saved to GCS %s): %w", "recommendation-"+month+".json", err)
	}
	if err := d.Store.WriteHeartbeat(ctx, Heartbeat{LastSuccess: now.UTC().Format(time.RFC3339), Month: month}); err != nil {
		d.Log.Warn("heartbeat write failed", "error", err)
	}
	d.Log.Info("run complete", "month", month, "orders", len(rec.Orders), "regime_invested", invested)
	return RunResult{Month: month, Orders: len(rec.Orders)}, nil
}

func sendWithRetry(ctx context.Context, tg MessageSender, text string) error {
	if tg == nil {
		return fmt.Errorf("telegram sender not configured")
	}
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), notifyTimeout)
	defer cancel()
	var err error
	for i := 0; i < 3; i++ {
		if err = tg.SendMessage(sendCtx, text); err == nil {
			return nil
		}
		if berr := retryLinearBackoff(sendCtx, i+1); berr != nil {
			return err
		}
	}
	return err
}

func fetchNSECompanyNames(ctx context.Context, d Deps) map[string]string {
	fetch := d.FetchNSEUniverse
	if fetch == nil {
		fetch = func(ctx context.Context) (map[string]string, error) {
			return FetchNSEUniverse(ctx, nil, "")
		}
	}
	names, err := fetch(ctx)
	if err != nil {
		d.Log.Warn("nse company names fetch failed", "error", err)
		return nil
	}
	return names
}

func fetchAll(ctx context.Context, y *YahooClient, symbols []string) (map[string]Series, map[string]error) {
	var mu sync.Mutex
	out := map[string]Series{}
	errs := map[string]error{}
	sem := make(chan struct{}, fetchWorkers)
	var wg sync.WaitGroup
	for _, sym := range symbols {
		wg.Add(1)
		sem <- struct{}{}
		go func(sym string) {
			defer wg.Done()
			defer func() { <-sem }()
			s, err := y.FetchDaily(ctx, sym, historyYears)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs[sym] = err
				return
			}
			out[sym] = s
		}(sym)
	}
	wg.Wait()
	return out, errs
}

func filterPolicyExcluded(scores map[string]float64, sym string) []string {
	sym = strings.ToUpper(strings.TrimSpace(sym))
	if sym == "" {
		return nil
	}
	if _, ok := scores[sym]; !ok {
		return nil
	}
	delete(scores, sym)
	return []string{sym + " (excluded by policy)"}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func head(r []Ranked, n int) []Ranked {
	if len(r) < n {
		n = len(r)
	}
	return r[:n]
}

func inIST(t time.Time, loc *time.Location) time.Time { return t.In(loc) }

func joinOrNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ", ")
}

func symbolsForQuotes(top []Ranked, diff DiffResult) []string {
	seen := map[string]bool{}
	var out []string
	add := func(sym string) {
		if sym == "" || seen[sym] {
			return
		}
		seen[sym] = true
		out = append(out, sym)
	}
	for _, r := range top {
		add(r.Symbol)
	}
	for _, o := range diff.Sells {
		add(o.Symbol)
	}
	for _, o := range diff.Buys {
		add(o.Symbol)
	}
	sort.Strings(out)
	return out
}
