package etfmonitor

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
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
	Log      *slog.Logger
	Now      func() time.Time
	// FetchLive optionally overrides the drift-check fetch (nil = the real
	// Betashares fund index).
	FetchLive func(ctx context.Context) ([]LiveFund, error)
}

// RunParams carries the resolved config into Run.
type RunParams struct {
	Universe     Universe
	LookbacksTD  []int
	Weights      []float64
	TrendSMADays int
	TopN         int
	DriftCheck   bool
	Force        bool
}

const (
	historyYears     = 3
	staleAfter       = 10 * 24 * time.Hour
	holdingsStale    = 60 * 24 * time.Hour
	maxFetchFailFrac = 0.2
	fetchWorkers     = 8
	notifyTimeout    = 30 * time.Second
)

// RunResult summarizes a completed run.
type RunResult struct {
	Month string
	TopN  int
	Exits int
}

// Run executes one monthly advisory cycle per spec 21. On failure it
// best-effort notifies Telegram ("RUN FAILED — ...") and returns the error.
func Run(ctx context.Context, p RunParams, d Deps) (RunResult, error) {
	now := d.Now()
	month := now.UTC().Format("2006-01")

	res, err := runCore(ctx, p, d, now, month)
	if err != nil {
		notifyFailure(ctx, d, month, err)
		return RunResult{Month: month}, err
	}
	return res, nil
}

// notifyFailure delivers the failure alert on a context detached from the run's.
// The most common failure IS the run context expiring (Cloud Run deadline), and
// a cancelled context would make every Telegram attempt fail instantly — losing
// exactly the alert the operator needs.
func notifyFailure(ctx context.Context, d Deps, month string, cause error) {
	if d.Telegram == nil {
		return
	}
	nctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), notifyTimeout)
	defer cancel()
	text := fmt.Sprintf("ASX ETF MONITOR %s — RUN FAILED\n%v\nNo recommendations. Investigate.", month, cause)
	if err := sendWithRetry(nctx, d.Telegram, text); err != nil {
		d.Log.Error("failure notification could not be delivered", "cause", cause, "error", err)
	}
}

func sendWithRetry(ctx context.Context, tg MessageSender, text string) error {
	if tg == nil {
		return fmt.Errorf("telegram sender not configured")
	}
	return retry(ctx, 3, func() error { return tg.SendMessage(ctx, text) })
}

func runCore(ctx context.Context, p RunParams, d Deps, now time.Time, month string) (RunResult, error) {
	var warnings []string
	rep := Report{RunAt: now.UTC().Format(time.RFC3339), Month: month}

	// 1. Holdings (user-maintained; absent is fine — report still goes out).
	held, err := d.Store.ReadHoldings(ctx)
	if err != nil {
		return RunResult{}, fmt.Errorf("holdings state: %w", err)
	}
	if len(held.Holdings) > 0 {
		if asOf := held.AsOfTime(); asOf.IsZero() || now.Sub(asOf) > holdingsStale {
			warnings = append(warnings, fmt.Sprintf("STALE HOLDINGS: as_of=%q — update holdings.json", held.AsOf))
		}
	}

	// 2. Fetch every priced fund plus anything held outside the universe.
	lookup := p.Universe.Lookup()
	want := map[string]bool{}
	for _, f := range p.Universe.Priced() {
		want[f.Ticker] = true
	}
	for _, t := range held.Tickers() {
		want[t] = true
	}
	series, fetchErrs := fetchAll(ctx, d.Yahoo, sortedKeys(want))

	// Unusable = never fetched OR stale. Staleness counts here deliberately: one
	// stale fund is just a delisting, but a stale *universe* means the feed is
	// serving dead data, and reporting a confident top 10 from it would be
	// worse than failing. Per-fund conditions like a split or short history are
	// NOT counted — those are legitimate states, not data outages.
	stdUnusable := 0
	for _, f := range p.Universe.Standard {
		s, ok := series[f.Ticker]
		if !ok || Stale(s, now, staleAfter) {
			stdUnusable++
		}
	}
	if n := len(p.Universe.Standard); n > 0 {
		if frac := float64(stdUnusable) / float64(n); frac > maxFetchFailFrac {
			return RunResult{}, fmt.Errorf(
				"data: %d/%d standard funds unusable (unfetchable or stale)", stdUnusable, n)
		}
	}
	if len(fetchErrs) > 0 {
		d.Log.Warn("some fetches failed", "count", len(fetchErrs))
	}

	// 3. Score every priced fund; reject bad data outright.
	scored := map[string]Scored{}
	metrics := map[string]Metrics{}
	for _, f := range p.Universe.Priced() {
		s, ok := series[f.Ticker]
		if !ok {
			rep.Rejected = append(rep.Rejected, f.Ticker+" (fetch failed)")
			continue
		}
		// Staleness BEFORE anything else. A delisted fund keeps returning years
		// of history from the price feed, so its trailing returns still compute
		// and it will happily rank on prices that no longer exist. Observed:
		// IPAY last traded 2025-02-14 and ranked #7 on 17-month-old data.
		if Stale(s, now, staleAfter) {
			last := "no candles"
			if n := len(s.Candles); n > 0 {
				last = s.Candles[n-1].Date.Format("2006-01-02")
			}
			rep.Rejected = append(rep.Rejected,
				fmt.Sprintf("%s (stale — last close %s, likely delisted/renamed)", f.Ticker, last))
			continue
		}
		if at, broken := DataBreak(s, DataBreakThreshold); broken {
			// An unadjusted split corrupts returns, vol AND max drawdown, and
			// keeps corrupting vol/maxDD long after it leaves every return
			// window. Reject rather than rank (spec 21 §Bad-data guard).
			rep.Rejected = append(rep.Rejected,
				fmt.Sprintf("%s (unadjusted split %s — data unusable)", f.Ticker, at.Format("2006-01-02")))
			continue
		}
		m := Compute(s, p.LookbacksTD, p.TrendSMADays)
		score, ok := Score(m.Returns, p.Weights)
		if !ok {
			rep.Rejected = append(rep.Rejected, fmt.Sprintf("%s (only %d sessions — too short to score)", f.Ticker, m.Days))
			continue
		}
		sc := Scored{
			Ticker: f.Ticker, Name: f.Name, Issuer: f.Issuer,
			Score: score, Returns: m.Returns, TrendUp: m.TrendUp,
			Vol: m.Vol, MaxDD: m.MaxDD,
			Classification: Classification(m.Returns, p.LookbacksTD),
		}
		sc.SetGroup(f.Group)
		sc.fillConvenienceReturns()
		scored[f.Ticker] = sc
		metrics[f.Ticker] = m
	}

	// 4. Top N: standard group only, hard trend gate, then recency-tilted rank.
	var eligible, below []Scored
	for _, f := range p.Universe.Standard {
		sc, ok := scored[f.Ticker]
		if !ok {
			continue
		}
		m := metrics[f.Ticker]
		if !m.HasTrend {
			// No 200-day line yet, so it cannot pass a gate it has no data for.
			// Surfaced as a watchlist entry rather than buried in "not scored":
			// the trend gate IS the exit rule, and recommending a fund with no
			// sell signal would be incoherent — but the user should still see
			// what is coming and roughly when.
			w := sc
			w.Sessions = m.Days
			w.SessionsNeeded = p.TrendSMADays
			rep.Watchlist = append(rep.Watchlist, w)
			continue
		}
		if sc.TrendUp {
			eligible = append(eligible, sc)
		} else {
			below = append(below, sc)
		}
	}
	sort.Slice(rep.Watchlist, func(i, j int) bool {
		if rep.Watchlist[i].Sessions != rep.Watchlist[j].Sessions {
			return rep.Watchlist[i].Sessions > rep.Watchlist[j].Sessions // closest first
		}
		return rep.Watchlist[i].Ticker < rep.Watchlist[j].Ticker
	})
	eligible = RankByScore(eligible)
	rep.Top = head(eligible, p.TopN)
	weights := InverseVolWeights(rep.Top)
	for i := range rep.Top {
		rep.Top[i].Weight = weights[i]
	}
	rep.BelowTrend = RankByScore(below)

	// 5. Geared + FX ranked separately; inverse tracked, never ranked.
	// These skip the trend gate (they are informational, not recommendations),
	// but they still need the SAME minimum history — otherwise a fund too young
	// to appear in the standard list shows up here with a 3m-only score, which
	// is an arbitrary inconsistency between groups.
	var gearedFX, inverse []Scored
	for _, f := range append(append([]Fund{}, p.Universe.Geared...), p.Universe.FX...) {
		if sc, ok := scored[f.Ticker]; ok && metrics[f.Ticker].HasTrend {
			gearedFX = append(gearedFX, sc)
		}
	}
	for _, f := range p.Universe.Inverse {
		if sc, ok := scored[f.Ticker]; ok && metrics[f.Ticker].HasTrend {
			inverse = append(inverse, sc)
		}
	}
	rep.Geared = RankByScore(gearedFX)
	rep.Inverse = RankByScore(inverse)

	// 6. Exit alerts for held funds — the disciplined sell rule.
	for _, h := range held.Holdings {
		name := lookup[h.Ticker].Name
		s, ok := series[h.Ticker]
		if !ok || Stale(s, now, staleAfter) {
			warnings = append(warnings, fmt.Sprintf(
				"HELD %s: no fresh price data — possible rename/corporate action", h.Ticker))
			continue
		}
		if at, broken := DataBreak(s, DataBreakThreshold); broken {
			warnings = append(warnings, fmt.Sprintf(
				"HELD %s: unadjusted split %s — cannot judge trend, check manually",
				h.Ticker, at.Format("2006-01-02")))
			continue
		}
		m := Compute(s, p.LookbacksTD, p.TrendSMADays)
		if !m.HasTrend {
			warnings = append(warnings, fmt.Sprintf(
				"HELD %s: only %d sessions — no 200-day trend to judge yet", h.Ticker, m.Days))
			continue
		}
		if !m.TrendUp {
			e := ExitAlert{
				Ticker: h.Ticker, Name: name,
				Reason: "below 200-day — momentum broken",
				Vol:    m.Vol,
			}
			if len(m.Returns) > 0 {
				e.Ret3M = m.Returns[0]
			}
			rep.Exits = append(rep.Exits, e)
		}
	}
	sort.Slice(rep.Exits, func(i, j int) bool { return rep.Exits[i].Ticker < rep.Exits[j].Ticker })

	// 7. Universe drift (best-effort, never blocks the run — but ALWAYS reports).
	if p.DriftCheck {
		fetchLive := d.FetchLive
		if fetchLive == nil {
			fetchLive = func(ctx context.Context) ([]LiveFund, error) {
				return FetchBetasharesFunds(ctx, http.DefaultClient, "")
			}
		}
		live, err := fetchLive(ctx)
		switch {
		case err != nil:
			// A silently-dead drift check is worse than none: you would believe
			// the universe is being watched while nothing is watching it. The
			// scrape depends on a third-party page layout and bot-protection,
			// so failure is expected eventually — and must be visible.
			d.Log.Warn("universe drift check failed", "error", err)
			warnings = append(warnings, fmt.Sprintf(
				"DRIFT CHECK DID NOT RUN: %v — the universe was not verified this month", err))
		default:
			added, removed := DiffUniverse(p.Universe.All(), live)
			// New funds cannot rank for ~10 months anyway (no 200-day history),
			// so this is housekeeping, not a blocker — and never auto-applied.
			if len(added) > 0 {
				rep.DriftNotes = append(rep.DriftNotes, fmt.Sprintf(
					"NEW FUNDS (categorize & add to config/universe-asx-etf.yaml): %s",
					strings.Join(added, ", ")))
			}
			if len(removed) > 0 {
				rep.DriftNotes = append(rep.DriftNotes, fmt.Sprintf(
					"Possibly delisted/renamed (no longer on the Betashares fund index): %s",
					strings.Join(removed, ", ")))
			}
			if len(added) == 0 && len(removed) == 0 {
				rep.DriftNotes = append(rep.DriftNotes, fmt.Sprintf(
					"Universe verified against %d live Betashares funds — no drift.", len(live)))
			}
		}
	}

	// 8. Compose, deliver, persist.
	rep.Warnings = warnings
	rep.MessageText = FormatMessage(rep, p.TopN)

	if err := d.Store.WriteReport(ctx, rep); err != nil {
		return RunResult{}, fmt.Errorf("write report: %w", err)
	}
	if err := sendWithRetry(ctx, d.Telegram, rep.MessageText); err != nil {
		return RunResult{}, fmt.Errorf("telegram delivery (report IS saved to %s): %w",
			"report-"+month+".json", err)
	}
	if err := d.Store.WriteHeartbeat(ctx, Heartbeat{LastSuccess: now.UTC().Format(time.RFC3339), Month: month}); err != nil {
		d.Log.Warn("heartbeat write failed", "error", err)
	}
	d.Log.Info("run complete", "month", month, "top", len(rep.Top), "exits", len(rep.Exits))
	return RunResult{Month: month, TopN: len(rep.Top), Exits: len(rep.Exits)}, nil
}

// Stale reports whether the series' latest candle is older than maxAge.
func Stale(s Series, now time.Time, maxAge time.Duration) bool {
	if len(s.Candles) == 0 {
		return true
	}
	return now.Sub(s.Candles[len(s.Candles)-1].Date) > maxAge
}

func fetchAll(ctx context.Context, y *YahooClient, tickers []string) (map[string]Series, map[string]error) {
	var mu sync.Mutex
	out := map[string]Series{}
	errs := map[string]error{}
	sem := make(chan struct{}, fetchWorkers)
	var wg sync.WaitGroup
	for _, t := range tickers {
		wg.Add(1)
		sem <- struct{}{}
		go func(t string) {
			defer wg.Done()
			defer func() { <-sem }()
			s, err := y.FetchDaily(ctx, t, historyYears)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs[t] = err
				return
			}
			out[t] = s
		}(t)
	}
	wg.Wait()
	return out, errs
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// head returns a COPY of the first n elements, so later mutation (weights)
// cannot alias the caller's slice.
func head(in []Scored, n int) []Scored {
	if len(in) < n {
		n = len(in)
	}
	return append([]Scored(nil), in[:n]...)
}
