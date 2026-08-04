package asxrotator

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
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
	// FetchOfficial optional override for the drift check (nil = fetch
	// RunParams.ConstituentsURL with the default HTTP client).
	FetchOfficial func(ctx context.Context) ([]string, error)
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
	ConstituentsURL string
	ExcludedSymbols []string
	// FrozenSymbols are held but untradeable: no orders either way, no slot
	// consumed, no stale-price warning. See Config.FrozenSymbols.
	FrozenSymbols []string
	// ExitLookbackMonths / ExitRankN drive the exit hysteresis: a holding is
	// sold only when outside the top ExitRankN on BOTH lookbacks.
	ExitLookbackMonths int
	ExitRankN          int
	// MinPriceAUD is the mandatory price floor. See Config.MinPriceAUD.
	MinPriceAUD float64
	// RegimeFilter gates the book to cash below the index EMA. When false the
	// regime is still computed and reported but never empties the target.
	RegimeFilter bool
}

const (
	indexSymbol           = "^AXJO"
	historyYears          = 5
	staleAfter            = 10 * 24 * time.Hour // ~7 trading days
	badJumpMaxMove        = 0.5
	portfolioStale        = 45 * 24 * time.Hour
	maxFetchFailFrac      = 0.2
	fetchWorkers          = 8
	topRankedDisplayCount = 20
	notifyTimeout         = 30 * time.Second
	// exchangeTZ observes daylight saving, unlike the NSE lane's fixed IST.
	// Month-end boundaries must be evaluated in local exchange time or a run
	// near midnight lands in the wrong month for half the year.
	exchangeTZ = "Australia/Sydney"
)

// RunResult summarizes a completed run.
type RunResult struct {
	Skipped bool // not the last trading day
	Month   string
	Orders  int
}

// Run executes one advisory cycle per spec 22. On failure it best-effort
// notifies Telegram ("RUN FAILED — ...") and returns the error (exit non-zero).
func Run(ctx context.Context, p RunParams, d Deps) (RunResult, error) {
	now := d.Now()
	loc, err := time.LoadLocation(exchangeTZ)
	if err != nil {
		return RunResult{}, fmt.Errorf("load %s: %w", exchangeTZ, err)
	}
	local := now.In(loc)
	month := local.Format("2006-01")

	if !p.Force && !d.Holidays.IsLastTradingDayOfMonth(now, p.Market, loc) {
		d.Log.Info("not the last trading day — exiting", "date", local.Format("2006-01-02"))
		return RunResult{Skipped: true, Month: month}, nil
	}

	res, err := runCore(ctx, p, d, now, loc, month)
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
	text := fmt.Sprintf("ASX ROTATOR %s — RUN FAILED\n%v\nNo orders. Investigate before month-end.", month, cause)
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

func runCore(ctx context.Context, p RunParams, d Deps, now time.Time, loc *time.Location, month string) (RunResult, error) {
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
	invested, idxClose, idxEMA, ok := RegimeInvested(idx, p.RegimeEMADays)
	if !ok {
		return RunResult{}, fmt.Errorf("index data too short for EMA%d", p.RegimeEMADays)
	}

	// 3. Fetch universe + held symbols (bounded concurrency).
	all := map[string]bool{}
	for _, s := range p.Universe {
		all[s] = true
	}
	for _, h := range pf.Holdings {
		all[h.Symbol] = true
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
	var excluded, belowFloor []string
	scores := map[string]float64{}
	scoresSlow := map[string]float64{} // exit lookback (12m) — keeps holdings alive
	lastClose := map[string]float64{}
	for sym, s := range series {
		if px, ok := s.LastClose(); ok {
			lastClose[sym] = px
		}
	}
	jumpWindow := BadJumpWindowDays(max(p.LookbackMonths, p.ExitLookbackMonths))
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
		// The price floor comes BEFORE scoring and applies to both lists. A
		// name below it is not merely unbuyable — it must not be able to keep
		// itself alive via the slow list either, which is exactly how the
		// back-adjustment artifact would otherwise persist in the book.
		if BelowMinPrice(s, p.MinPriceAUD) {
			px, _ := s.LastClose()
			d.Log.Info("below min price — excluded from both momentum lists",
				"symbol", sym, "last_close", px, "min_price_aud", p.MinPriceAUD)
			belowFloor = append(belowFloor, sym)
			continue
		}
		// The jump screen must reach back over the longer of the two lookbacks,
		// or stale data a year old could keep a holding alive via the slow list.
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
	sort.Strings(belowFloor)
	if len(belowFloor) > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"%d symbol(s) below the A$%.2f price floor — not ranked: %s",
			len(belowFloor), p.MinPriceAUD, strings.Join(belowFloor, ", ")))
	}

	for _, sym := range p.ExcludedSymbols {
		excluded = append(excluded, filterPolicyExcluded(scores, sym)...)
		// Must also drop from the slow list, or a blocked name would be kept
		// alive by it — the exact opposite of what the blocklist is for.
		filterPolicyExcluded(scoresSlow, sym)
	}
	// Frozen names leave the rankings too, so they are never bought and never
	// occupy a slot. Unlike the blocklist they are also withheld from
	// BuildOrders below, so they produce no SELL either.
	frozen := make(map[string]bool, len(p.FrozenSymbols))
	for _, sym := range p.FrozenSymbols {
		sym = strings.ToUpper(strings.TrimSpace(sym))
		if sym == "" {
			continue
		}
		frozen[sym] = true
		filterPolicyExcluded(scores, sym)
		filterPolicyExcluded(scoresSlow, sym)
	}
	// Held symbols with stale data are a loud warning (corporate action?).
	// Frozen names are stale by definition — that is why they are frozen.
	for _, h := range pf.Holdings {
		if frozen[h.Symbol] {
			continue
		}
		s, ok := series[h.Symbol]
		if !ok || Stale(s, now, staleAfter) {
			warnings = append(warnings, fmt.Sprintf("HELD %s: no fresh price data — possible rename/corporate action", h.Symbol))
		}
	}

	ranked := Rank(scores)
	rankedSlow := Rank(scoresSlow)
	slowIdx := RankIndex(rankedSlow)

	// 5. Target picks. Entry on the fast list, exit only when outside the
	// buffer on BOTH lists. With RegimeFilter on, a below-EMA index leaves the
	// target empty and BuildOrders sells the whole book.
	var target []string
	if ShouldHoldEquity(invested, p.RegimeFilter) {
		target = BuildTarget(ranked, rankedSlow, pf.Holdings, p.TopK, p.ExitRankN)
	}
	diff := BuildOrders(pf.Holdings, target, lastClose, pf.TotalCapitalAUD, p.TopK, frozen)

	// Company name, live price and market cap for the displayed list and orders
	// (best-effort). The displayed list must reach the exit buffer, or a holding
	// kept alive at rank 25 would never appear and the hold could not be explained.
	topDisplay := head(ranked, max(topRankedDisplayCount, p.ExitRankN))
	quotes := d.Yahoo.FetchQuoteDetails(ctx, symbolsForQuotes(topDisplay, diff))
	resolveName := func(sym string) string {
		if s, ok := series[sym]; ok && strings.TrimSpace(s.CompanyName) != "" {
			return s.CompanyName
		}
		return quotes[sym].CompanyName
	}
	for i := range topDisplay {
		r := &topDisplay[i]
		q := quotes[r.Symbol]
		r.CompanyName = resolveName(r.Symbol)
		r.MarketCap = q.MarketCap
		r.MomentumSlow = scoresSlow[r.Symbol] // zero when unranked on the slow list
		r.RankSlow = slowIdx[r.Symbol]
		if q.Price > 0 {
			r.LastClose = q.Price
		} else if px, ok := lastClose[r.Symbol]; ok {
			r.LastClose = px
		}
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

	// 6. Universe drift (best-effort, never blocks). Unlike NSE there is no
	// official free endpoint, so an unconfigured source warns rather than
	// silently passing — see drift.go.
	if p.DriftCheck {
		warnings = append(warnings, driftWarnings(ctx, p, d)...)
	}

	// 7. Holiday-file staleness (December ritual).
	if local := now.In(loc); int(local.Month()) == 12 && d.Holidays.Year() <= local.Year() {
		warnings = append(warnings, fmt.Sprintf("holidays-asx.yaml covers %d — add next year before January", d.Holidays.Year()))
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
		IndexClose:     idxClose,
		IndexEMA:       idxEMA,
		Orders:         append(append([]Order{}, diff.Sells...), diff.Buys...),
		Holds:          diff.Holds,
		HoldsInfo:      holdsInfo,
		Frozen:         diff.Frozen,
		TopRanked:      topDisplay, // already enriched in place
		Params: RunParamsRecord{
			LookbackMonths:     p.LookbackMonths,
			ExitLookbackMonths: p.ExitLookbackMonths,
			TopK:               p.TopK,
			ExitRankN:          p.ExitRankN,
			MinPriceAUD:        p.MinPriceAUD,
		},
		Excluded:      excluded,
		Warnings:      warnings,
		BelowMinPrice: belowFloor,
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

// driftWarnings runs the universe drift check, returning any warnings. An
// unconfigured source is itself a warning: a check that never fires must not
// read as a clean result.
func driftWarnings(ctx context.Context, p RunParams, d Deps) []string {
	fetch := d.FetchOfficial
	if fetch == nil {
		if strings.TrimSpace(p.ConstituentsURL) == "" {
			return []string{"UNIVERSE DRIFT NOT CHECKED: constituents_url is unset. " +
				"S&P publishes ASX 200 membership behind a login — point it at an " +
				"ASX 200 ETF holdings CSV (STW/IOZ/A200), or refresh " +
				"config/universe-asx200.yaml by hand each quarter."}
		}
		fetch = func(ctx context.Context) ([]string, error) {
			return FetchConstituents(ctx, &http.Client{Timeout: 30 * time.Second}, p.ConstituentsURL)
		}
	}
	official, err := fetch(ctx)
	if err != nil {
		d.Log.Warn("universe drift check failed", "error", err)
		return []string{fmt.Sprintf("universe drift check failed: %v", err)}
	}
	added, removed := DiffUniverse(p.Universe, official)
	if len(added)+len(removed) == 0 {
		return nil
	}
	return []string{fmt.Sprintf(
		"UNIVERSE DRIFT — update config/universe-asx200.yaml. index adds: %s | index drops: %s",
		joinOrNone(added), joinOrNone(removed))}
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

func retryLinearBackoff(ctx context.Context, attempt int) error {
	if attempt <= 0 {
		return nil
	}
	t := time.NewTimer(time.Duration(attempt) * 2 * time.Second)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
