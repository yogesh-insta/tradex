package etfmonitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- Score / weighting: pure unit tests -------------------------------------

// TestRecencyTiltPrefersAccelerator is the point of the 0.5/0.3/0.2 weights:
// two funds with the SAME average trailing return must not tie — the one whose
// strength is recent wins, and the one that has already rolled over loses.
func TestRecencyTiltPrefersAccelerator(t *testing.T) {
	w := []float64{0.5, 0.3, 0.2}

	// mean of both = (0.30 + 0.20 + 0.10)/3 = 0.20
	accel := []Ret{{0.30, true}, {0.20, true}, {0.10, true}} // strongest recently
	decel := []Ret{{0.10, true}, {0.20, true}, {0.30, true}} // strongest a year ago

	as, ok1 := Score(accel, w)
	ds, ok2 := Score(decel, w)
	if !ok1 || !ok2 {
		t.Fatal("both should score")
	}
	if as <= ds {
		t.Errorf("accelerator %.4f should out-score decelerator %.4f at equal mean return", as, ds)
	}

	// And the hard case from the spec: a big 12m with a NEGATIVE 3m must lose
	// to a fresh mover even when its trailing average is higher.
	stale := []Ret{{-0.05, true}, {0.10, true}, {0.60, true}} // mean 0.2167
	fresh := []Ret{{0.20, true}, {0.15, true}, {0.10, true}}  // mean 0.1500
	ss, _ := Score(stale, w)
	fs, _ := Score(fresh, w)
	if fs <= ss {
		t.Errorf("fresh mover %.4f should out-score the decelerator %.4f despite a lower mean", fs, ss)
	}
}

func TestScoreRenormalizesWeights(t *testing.T) {
	w := []float64{0.5, 0.3, 0.2}
	cases := []struct {
		name string
		in   []Ret
		want float64
		ok   bool
	}{
		{"all three", []Ret{{0.10, true}, {0.20, true}, {0.30, true}}, 0.5*0.10 + 0.3*0.20 + 0.2*0.30, true},
		{"3m only -> weight 1.0", []Ret{{0.10, true}, {}, {}}, 0.10, true},
		{"3m+6m -> 0.625/0.375", []Ret{{0.10, true}, {0.20, true}, {}}, 0.625*0.10 + 0.375*0.20, true},
		{"no 3m -> not scored", []Ret{{}, {0.20, true}, {0.30, true}}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Score(tc.in, w)
			if ok != tc.ok {
				t.Fatalf("ok = %v want %v", ok, tc.ok)
			}
			if ok && !closeTo(got, tc.want, 1e-12) {
				t.Errorf("score = %.12f want %.12f", got, tc.want)
			}
		})
	}
}

func TestInverseVolWeights(t *testing.T) {
	in := []Scored{
		{Ticker: "CALM", Vol: 0.10},
		{Ticker: "WILD", Vol: 0.40},
	}
	w := InverseVolWeights(in)
	if !closeTo(w[0]+w[1], 1.0, 1e-12) {
		t.Errorf("weights must sum to 1, got %.6f", w[0]+w[1])
	}
	if w[0] <= w[1] {
		t.Errorf("the calmer fund should get more weight: %.3f vs %.3f", w[0], w[1])
	}
	// 1/0.1 : 1/0.4  =  10 : 2.5  ->  0.8 / 0.2
	if !closeTo(w[0], 0.8, 1e-12) || !closeTo(w[1], 0.2, 1e-12) {
		t.Errorf("weights = %.4f/%.4f want 0.8/0.2", w[0], w[1])
	}
}

func TestInverseVolWeightsZeroVolIsNotInfinite(t *testing.T) {
	w := InverseVolWeights([]Scored{{Ticker: "FLAT", Vol: 0}, {Ticker: "REAL", Vol: 0.2}})
	if w[0] != 0 {
		t.Errorf("zero-vol fund should get zero weight, got %v", w[0])
	}
	if !closeTo(w[1], 1.0, 1e-12) {
		t.Errorf("the only real fund should take the full sleeve, got %v", w[1])
	}
}

// --- Run-level: trend gate, exits, report -----------------------------------

type capturedSender struct{ texts []string }

func (c *capturedSender) SendMessage(_ context.Context, text string) error {
	c.texts = append(c.texts, text)
	return nil
}

// chartServer serves Yahoo-shaped chart JSON for synthetic funds.
func chartServer(t *testing.T, closes map[string][]float64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /v8/finance/chart/TICK.AX
		parts := strings.Split(r.URL.Path, "/")
		sym := strings.TrimSuffix(parts[len(parts)-1], ".AX")
		px, ok := closes[sym]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"chart":{"result":null,"error":{"code":"Not Found","description":"no data"}}}`)
			return
		}
		start := time.Now().AddDate(0, 0, -len(px)).Unix()
		ts := make([]int64, len(px))
		for i := range px {
			ts[i] = start + int64(i)*86400
		}
		resp := map[string]any{"chart": map[string]any{"result": []any{map[string]any{
			"timestamp": ts,
			"indicators": map[string]any{
				"adjclose": []any{map[string]any{"adjclose": px}},
				"quote":    []any{map[string]any{"close": px}},
			},
		}}}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// risingThenFalling builds a series that ends BELOW its 200-day average while
// still carrying a large 12-month trailing return — the exact shape the trend
// gate exists to reject.
func risingThenFalling() []float64 {
	out := make([]float64, 0, 300)
	for i := 0; i < 200; i++ {
		out = append(out, 100+float64(i)*1.5) // 100 -> ~400
	}
	for i := 0; i < 100; i++ {
		out = append(out, 400-float64(i)*1.8) // rolls over hard
	}
	return out
}

func steadyRise() []float64 {
	out := make([]float64, 0, 300)
	for i := 0; i < 300; i++ {
		out = append(out, 100*pow(1.0015, i))
	}
	return out
}

func pow(b float64, n int) float64 {
	v := 1.0
	for i := 0; i < n; i++ {
		v *= b
	}
	return v
}

func testDeps(t *testing.T, srv *httptest.Server, sender *capturedSender) Deps {
	t.Helper()
	return Deps{
		Yahoo:    &YahooClient{BaseURL: srv.URL, HTTPClient: srv.Client(), Retries: 1},
		Telegram: sender,
		Store:    &StateStore{LocalDir: t.TempDir()},
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      func() time.Time { return time.Now() },
	}
}

func TestTrendGateExcludesHighReturnFundBelow200Day(t *testing.T) {
	srv := chartServer(t, map[string][]float64{
		"FALLR": risingThenFalling(),
		"RISER": steadyRise(),
	})
	defer srv.Close()

	sender := &capturedSender{}
	p := RunParams{
		Universe: Universe{Standard: []Fund{
			{Ticker: "FALLR", Name: "Rolled Over", Group: GroupStandard},
			{Ticker: "RISER", Name: "Still Trending", Group: GroupStandard},
		}},
		LookbacksTD: []int{63, 126, 252}, Weights: []float64{0.5, 0.3, 0.2},
		TrendSMADays: 200, TopN: 10,
	}
	res, err := Run(context.Background(), p, testDeps(t, srv, sender))
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	msg := sender.texts[0]
	if !strings.Contains(msg, "RISER") {
		t.Errorf("trending fund missing from report:\n%s", msg)
	}
	if strings.Contains(topSection(msg), "FALLR") {
		t.Errorf("FALLR is below its 200-day and must not appear in the top list:\n%s", msg)
	}
	if !strings.Contains(msg, "Below trend — not eligible") || !strings.Contains(msg, "FALLR") {
		t.Errorf("FALLR should be listed as below trend:\n%s", msg)
	}
	if res.TopN != 1 {
		t.Errorf("expected exactly 1 eligible fund, got %d", res.TopN)
	}
}

// topSection isolates the ranked block, so a ticker named in a LATER section
// (below-trend, rejected, geared) is not mistaken for a ranked entry. It ends
// at whichever following section header appears first.
func topSection(msg string) string {
	start := strings.Index(msg, "TOP ")
	if start < 0 {
		return ""
	}
	rest := msg[start:]
	end := len(rest)
	for _, marker := range []string{"Below trend", "Watchlist", "GEARED / FX", "Inverse/bear", "Not scored", "Advisory only"} {
		if i := strings.Index(rest, marker); i >= 0 && i < end {
			end = i
		}
	}
	return rest[:end]
}

func TestExitAlertForHeldFundBelowTrend(t *testing.T) {
	srv := chartServer(t, map[string][]float64{
		"FALLR": risingThenFalling(),
		"RISER": steadyRise(),
	})
	defer srv.Close()

	dir := t.TempDir()
	holdings := Holdings{
		AsOf: time.Now().Format("2006-01-02"),
		Holdings: []Holding{
			{Ticker: "FALLR", Qty: 100, AvgPrice: 300},
			{Ticker: "RISER", Qty: 50, AvgPrice: 110},
		},
	}
	raw, _ := json.MarshalIndent(holdings, "", "  ")
	if err := writeFile(dir, "holdings.json", raw); err != nil {
		t.Fatal(err)
	}

	sender := &capturedSender{}
	d := testDeps(t, srv, sender)
	d.Store = &StateStore{LocalDir: dir}

	p := RunParams{
		Universe: Universe{Standard: []Fund{
			{Ticker: "FALLR", Name: "Rolled Over", Group: GroupStandard},
			{Ticker: "RISER", Name: "Still Trending", Group: GroupStandard},
		}},
		LookbacksTD: []int{63, 126, 252}, Weights: []float64{0.5, 0.3, 0.2},
		TrendSMADays: 200, TopN: 10,
	}
	res, err := Run(context.Background(), p, d)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Exits != 1 {
		t.Fatalf("expected 1 exit alert, got %d", res.Exits)
	}

	msg := sender.texts[0]
	if !strings.Contains(msg, "EXIT FALLR") {
		t.Errorf("missing exit alert for the held broken fund:\n%s", msg)
	}
	if strings.Contains(msg, "EXIT RISER") {
		t.Errorf("RISER is still trending and must NOT trigger an exit:\n%s", msg)
	}
	// Exits must lead the message — they are the only part demanding action.
	if idx := strings.Index(msg, "EXIT ALERTS"); idx < 0 || idx > strings.Index(msg, "TOP ") {
		t.Errorf("exit alerts must appear before the top list:\n%s", msg)
	}
}

func TestSplitCorruptedFundNeverRanks(t *testing.T) {
	// BBUS shape: clean history, then an unadjusted consolidation.
	corrupted := make([]float64, 0, 300)
	for i := 0; i < 200; i++ {
		corrupted = append(corrupted, 30-float64(i)*0.05)
	}
	corrupted = append(corrupted, 2.0) // -90% in one session
	for i := 0; i < 99; i++ {
		corrupted = append(corrupted, 20+float64(i)*0.05)
	}

	srv := chartServer(t, map[string][]float64{"SPLIT": corrupted, "RISER": steadyRise()})
	defer srv.Close()

	sender := &capturedSender{}
	p := RunParams{
		Universe: Universe{Standard: []Fund{
			{Ticker: "SPLIT", Name: "Unadjusted Split", Group: GroupStandard},
			{Ticker: "RISER", Name: "Still Trending", Group: GroupStandard},
		}},
		LookbacksTD: []int{63, 126, 252}, Weights: []float64{0.5, 0.3, 0.2},
		TrendSMADays: 200, TopN: 10,
	}
	if _, err := Run(context.Background(), p, testDeps(t, srv, sender)); err != nil {
		t.Fatalf("run: %v", err)
	}
	msg := sender.texts[0]
	if strings.Contains(topSection(msg), "SPLIT") {
		t.Errorf("split-corrupted fund must never be ranked:\n%s", msg)
	}
	if !strings.Contains(msg, "unadjusted split") {
		t.Errorf("rejection should be reported with its reason:\n%s", msg)
	}
}

func TestReportFormatIsPlainTextAndComplete(t *testing.T) {
	r := Report{
		Month: "2026-07",
		Exits: []ExitAlert{{Ticker: "URNM", Name: "Global Uranium", Reason: "below 200-day — momentum broken"}},
		Top: []Scored{{
			Ticker: "SEMI", Name: "Semiconductors", Issuer: "Global X",
			Ret3M: Ret{0.26, true}, Ret6M: Ret{0.49, true}, Ret12M: Ret{1.22, true},
			Vol: 0.34, MaxDD: -0.33, Classification: "accelerating", Weight: 0.18,
		}},
		BelowTrend: []Scored{{Ticker: "GAME"}},
		Rejected:   []string{"BBUS (unadjusted split 2025-12-01 — data unusable)"},
	}
	msg := FormatMessage(r, 10)

	for _, want := range []string{
		"ASX ETF MONITOR — 2026-07",
		"EXIT URNM",
		"SEMI — Semiconductors [Global X, buyable via broker]",
		"3m +26%", "6m +49%", "12m +122%",
		"vol 34%", "maxDD -33%",
		"accelerating", "suggested weight 18%",
		"Below trend — not eligible",
		"unadjusted split",
		"Advisory only",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}
	// Plain text only: no Markdown parse-mode metacharacters that Telegram
	// would choke on for fund names containing & or +.
	for _, bad := range []string{"**", "__", "```"} {
		if strings.Contains(msg, bad) {
			t.Errorf("message contains markdown %q — must be plain text:\n%s", bad, msg)
		}
	}
}

func TestFetchFailureThresholdFailsRun(t *testing.T) {
	// Only 1 of 3 standard funds resolves -> 67% failure, above the 20% bar.
	srv := chartServer(t, map[string][]float64{"OK1": steadyRise()})
	defer srv.Close()

	sender := &capturedSender{}
	p := RunParams{
		Universe: Universe{Standard: []Fund{
			{Ticker: "OK1", Group: GroupStandard},
			{Ticker: "GONE1", Group: GroupStandard},
			{Ticker: "GONE2", Group: GroupStandard},
		}},
		LookbacksTD: []int{63, 126, 252}, Weights: []float64{0.5, 0.3, 0.2},
		TrendSMADays: 200, TopN: 10,
	}
	_, err := Run(context.Background(), p, testDeps(t, srv, sender))
	if err == nil {
		t.Fatal("expected the run to fail when most of the universe is unfetchable")
	}
	if len(sender.texts) != 1 || !strings.Contains(sender.texts[0], "RUN FAILED") {
		t.Errorf("a failure must still reach Telegram, got %v", sender.texts)
	}
}

func writeFile(dir, name string, raw []byte) error {
	return os.WriteFile(filepath.Join(dir, name), raw, 0o644)
}

// --- Delisted / stale funds --------------------------------------------------

// TestStaleFundIsRejected pins the IPAY defect: a delisted fund keeps returning
// years of history, so its trailing returns still compute and it will rank on
// prices that no longer exist. IPAY last traded 2025-02-14 and ranked #7.
func TestStaleFundIsRejected(t *testing.T) {
	srv := chartServer(t, map[string][]float64{"LIVE": steadyRise()})
	defer srv.Close()

	sender := &capturedSender{}
	d := testDeps(t, srv, sender)
	// Freeze "now" two years after the synthetic series ends: every fund the
	// server returns is stale relative to it.
	d.Now = func() time.Time { return time.Now().AddDate(2, 0, 0) }

	p := RunParams{
		Universe:    Universe{Standard: []Fund{{Ticker: "LIVE", Group: GroupStandard}}},
		LookbacksTD: []int{63, 126, 252}, Weights: []float64{0.5, 0.3, 0.2},
		TrendSMADays: 200, TopN: 10,
	}
	// All standard funds stale -> nothing fetchable passes -> run fails loudly
	// rather than reporting a top list built on dead prices.
	_, err := Run(context.Background(), p, d)
	if err == nil {
		t.Fatal("expected failure when the entire universe is stale")
	}
	msg := sender.texts[0]
	if !strings.Contains(msg, "RUN FAILED") {
		t.Errorf("expected a failure alert, got:\n%s", msg)
	}
}

func TestStaleFundRejectedWhileFreshOneRanks(t *testing.T) {
	// 1 dead of 10 = 10%, under the 20% data-outage bar, so the run proceeds
	// and the delisted fund is rejected individually.
	live := []string{"FR1", "FR2", "FR3", "FR4", "FR5", "FR6", "FR7", "FR8", "FR9"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		sym := strings.TrimSuffix(parts[len(parts)-1], ".AX")
		px := steadyRise()
		endsAt := time.Now()
		if sym == "DEAD" {
			endsAt = time.Now().AddDate(0, 0, -400) // delisted long ago
		} else if !strings.HasPrefix(sym, "FR") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"chart":{"result":null,"error":{"code":"NF","description":"x"}}}`)
			return
		}
		start := endsAt.AddDate(0, 0, -len(px)).Unix()
		ts := make([]int64, len(px))
		for i := range px {
			ts[i] = start + int64(i)*86400
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"chart": map[string]any{"result": []any{map[string]any{
			"timestamp":  ts,
			"indicators": map[string]any{"adjclose": []any{map[string]any{"adjclose": px}}},
		}}}})
	}))
	defer srv.Close()

	funds := []Fund{{Ticker: "DEAD", Group: GroupStandard}}
	for _, tk := range live {
		funds = append(funds, Fund{Ticker: tk, Group: GroupStandard})
	}

	sender := &capturedSender{}
	p := RunParams{
		Universe:    Universe{Standard: funds},
		LookbacksTD: []int{63, 126, 252}, Weights: []float64{0.5, 0.3, 0.2},
		TrendSMADays: 200, TopN: 10,
	}
	if _, err := Run(context.Background(), p, testDeps(t, srv, sender)); err != nil {
		t.Fatalf("run: %v", err)
	}
	msg := sender.texts[0]
	if strings.Contains(topSection(msg), "DEAD") {
		t.Errorf("a delisted fund must never rank:\n%s", msg)
	}
	if !strings.Contains(msg, "likely delisted/renamed") {
		t.Errorf("staleness rejection should name its reason:\n%s", msg)
	}
	if !strings.Contains(topSection(msg), "FR1") {
		t.Errorf("live funds should still rank:\n%s", msg)
	}
}

// --- Watchlist ---------------------------------------------------------------

func TestYoungFundGoesToWatchlistNotSilence(t *testing.T) {
	srv := chartServer(t, map[string][]float64{
		"YOUNG": nSessionsRising(150),
		"OLD":   steadyRise(),
	})
	defer srv.Close()

	sender := &capturedSender{}
	p := RunParams{
		Universe: Universe{Standard: []Fund{
			{Ticker: "YOUNG", Name: "New Thing", Group: GroupStandard},
			{Ticker: "OLD", Name: "Established", Group: GroupStandard},
		}},
		LookbacksTD: []int{63, 126, 252}, Weights: []float64{0.5, 0.3, 0.2},
		TrendSMADays: 200, TopN: 10,
	}
	if _, err := Run(context.Background(), p, testDeps(t, srv, sender)); err != nil {
		t.Fatalf("run: %v", err)
	}
	msg := sender.texts[0]
	if strings.Contains(topSection(msg), "YOUNG") {
		t.Errorf("a fund with no 200-day line must not rank:\n%s", msg)
	}
	if !strings.Contains(msg, "Watchlist — too new to rank") || !strings.Contains(msg, "150/200 sessions") {
		t.Errorf("young fund should appear on the watchlist with its progress:\n%s", msg)
	}
}

func nSessionsRising(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = 100 * pow(1.002, i)
	}
	return out
}

// --- Drift ------------------------------------------------------------------

// TestDriftFailureIsReported is the fix for a silently-dead monitor: both live
// sources are third-party and bot-protected, so the check WILL fail eventually.
// Failing quietly would leave the user believing the universe is watched.
func TestDriftFailureIsReported(t *testing.T) {
	srv := chartServer(t, map[string][]float64{"OK": steadyRise()})
	defer srv.Close()

	sender := &capturedSender{}
	d := testDeps(t, srv, sender)
	d.FetchLive = func(context.Context) ([]LiveFund, error) {
		return nil, fmt.Errorf("HTTP 403")
	}
	p := RunParams{
		Universe:    Universe{Standard: []Fund{{Ticker: "OK", Group: GroupStandard}}},
		LookbacksTD: []int{63, 126, 252}, Weights: []float64{0.5, 0.3, 0.2},
		TrendSMADays: 200, TopN: 10, DriftCheck: true,
	}
	if _, err := Run(context.Background(), p, d); err != nil {
		t.Fatalf("drift failure must not fail the run: %v", err)
	}
	msg := sender.texts[0]
	if !strings.Contains(msg, "DRIFT CHECK DID NOT RUN") || !strings.Contains(msg, "403") {
		t.Errorf("a failed drift check must be visible in the message:\n%s", msg)
	}
}

func TestDriftReportsAddedRemovedAndCleanState(t *testing.T) {
	universe := []Fund{
		{Ticker: "KEEP", Group: GroupStandard},
		{Ticker: "GONE", Group: GroupStandard},
		{Ticker: "OTHER", Group: GroupStandard, Issuer: "Global X", DriftExempt: true},
	}
	live := []LiveFund{{Ticker: "KEEP"}, {Ticker: "BRAND"}}

	added, removed := DiffUniverse(universe, live)
	if len(added) != 1 || added[0] != "BRAND" {
		t.Errorf("added = %v want [BRAND]", added)
	}
	if len(removed) != 1 || removed[0] != "GONE" {
		t.Errorf("removed = %v want [GONE] (exempt funds must not warn every month)", removed)
	}

	// Clean state must say so explicitly — silence is indistinguishable from
	// a check that never ran.
	added, removed = DiffUniverse([]Fund{{Ticker: "KEEP"}}, []LiveFund{{Ticker: "KEEP"}})
	if len(added) != 0 || len(removed) != 0 {
		t.Errorf("expected no drift, got added=%v removed=%v", added, removed)
	}
}
