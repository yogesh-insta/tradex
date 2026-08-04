package asxrotator

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yogesh-insta/tradex/internal/calendar"
)

type mockTelegram struct {
	calls int
	last  string
}

func (m *mockTelegram) SendMessage(_ context.Context, text string) error {
	m.calls++
	m.last = text
	return nil
}

func TestNotifyFailureUsesDetachedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tg := &mockTelegram{}
	notifyFailure(ctx, Deps{Telegram: tg, Log: slog.Default()}, "2026-07", errors.New("boom"))
	if tg.calls == 0 {
		t.Fatal("expected failure notify attempts with detached context")
	}
}

func TestSendWithRetryUsesDetachedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tg := &mockTelegram{}
	if err := sendWithRetry(ctx, tg, "hello"); err != nil {
		t.Fatalf("sendWithRetry: %v", err)
	}
	if tg.calls == 0 {
		t.Fatal("expected send attempts")
	}
}

// chartFor renders a Yahoo v8 chart payload: `days` daily candles ending today,
// priced by px(i).
func chartFor(days int, px func(i int) float64) []byte {
	now := time.Now().UTC()
	ts := make([]int64, days)
	closes := make([]*float64, days)
	for i := 0; i < days; i++ {
		ts[i] = now.AddDate(0, 0, -(days - 1 - i)).Unix()
		v := px(i)
		closes[i] = &v
	}
	type adj struct {
		Adjclose []*float64 `json:"adjclose"`
	}
	payload := map[string]any{
		"chart": map[string]any{
			"result": []map[string]any{{
				"meta":      map[string]any{"longName": "Test Co"},
				"timestamp": ts,
				"indicators": map[string]any{
					"adjclose": []adj{{Adjclose: closes}},
				},
			}},
		},
	}
	b, _ := json.Marshal(payload)
	return b
}

// yahooStub serves chart data per symbol. Quote/spark endpoints 404 — the
// rotator treats those as best-effort, which this also verifies.
func yahooStub(t *testing.T, series map[string][]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v8/finance/chart/") {
			http.NotFound(w, r)
			return
		}
		sym := strings.TrimPrefix(r.URL.Path, "/v8/finance/chart/")
		body, ok := series[sym]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
}

func testDeps(t *testing.T, srv *httptest.Server, portfolio string) (Deps, string, *mockTelegram) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "portfolio.json"), []byte(portfolio), 0o644); err != nil {
		t.Fatal(err)
	}
	hol, err := calendar.LoadHolidays("../../config/holidays-asx.yaml")
	if err != nil {
		t.Fatal(err)
	}
	tg := &mockTelegram{}
	return Deps{
		Yahoo:    &YahooClient{BaseURL: srv.URL, Suffix: YahooSuffix, Retries: 1, HTTPClient: srv.Client()},
		Telegram: tg,
		Store:    &StateStore{LocalDir: dir},
		Holidays: hol,
		Log:      slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		Now:      time.Now,
	}, dir, tg
}

// End-to-end: a back-adjusted, near-zero symbol with enormous momentum must not
// reach the ranking, the orders, or the recommendation's top list — while a
// normal symbol on the same run does. This is the whole reason the lane exists.
func TestRunExcludesBelowFloorSymbolEndToEnd(t *testing.T) {
	const days = 400
	stub := yahooStub(t, map[string][]byte{
		"^AXJO": chartFor(days, func(i int) float64 { return 7000 + float64(i) }),
		// TAH: the artifact — every price tiny, smooth ramp, momentum ~+1800%.
		"TAH.AX": chartFor(days, func(i int) float64 { return 0.0001 * pow(1.0085, float64(i)) }),
		// BHP: an ordinary, investable uptrend.
		"BHP.AX": chartFor(days, func(i int) float64 { return 30 + float64(i)*0.02 }),
		"CBA.AX": chartFor(days, func(i int) float64 { return 100 + float64(i)*0.01 }),
	})
	defer stub.Close()

	d, dir, tg := testDeps(t, stub, `{"as_of":"`+time.Now().Format("2006-01-02")+
		`","total_capital_aud":100000,"holdings":[]}`)

	p := RunParams{
		Universe:           []string{"TAH", "BHP", "CBA"},
		LookbackMonths:     6,
		ExitLookbackMonths: 12,
		TopK:               2,
		ExitRankN:          3,
		RegimeEMADays:      200,
		Market:             "XASX",
		Force:              true,
		MinPriceAUD:        DefaultMinPriceAUD,
	}
	res, err := Run(context.Background(), p, d)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Skipped {
		t.Fatal("Force should skip the trading-day gate")
	}

	raw, err := os.ReadFile(filepath.Join(dir, "recommendation-"+res.Month+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var rec Recommendation
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}

	for _, r := range rec.TopRanked {
		if r.Symbol == "TAH" {
			t.Errorf("TAH reached the ranking with momentum %.1f — the price floor did not hold", r.Momentum)
		}
	}
	for _, o := range rec.Orders {
		if o.Symbol == "TAH" {
			t.Errorf("TAH reached the orders: %+v", o)
		}
	}
	if !contains(rec.BelowMinPrice, "TAH") {
		t.Errorf("below_min_price = %v, want it to report TAH", rec.BelowMinPrice)
	}
	// The screen must be visible, not silent.
	if !strings.Contains(rec.MessageText, "price floor") {
		t.Errorf("message should surface the price floor:\n%s", rec.MessageText)
	}
	// And the ordinary names must still be traded normally.
	if len(rec.TopRanked) != 2 {
		t.Errorf("top_ranked = %d names, want 2 (BHP, CBA)", len(rec.TopRanked))
	}
	if len(rec.Orders) == 0 {
		t.Error("expected BUY orders for the investable names")
	}
	if tg.calls == 0 {
		t.Error("expected a Telegram delivery")
	}
}

// A held name that falls below the floor must be SOLD, not kept alive by the
// slow list. Getting this wrong strands a position in a corrupted series.
func TestRunSellsHeldSymbolThatFallsBelowFloor(t *testing.T) {
	const days = 400
	stub := yahooStub(t, map[string][]byte{
		"^AXJO":  chartFor(days, func(i int) float64 { return 7000 + float64(i) }),
		"ZIP.AX": chartFor(days, func(i int) float64 { return 0.0001 * pow(1.0085, float64(i)) }),
		"BHP.AX": chartFor(days, func(i int) float64 { return 30 + float64(i)*0.02 }),
	})
	defer stub.Close()

	d, dir, _ := testDeps(t, stub, `{"as_of":"`+time.Now().Format("2006-01-02")+
		`","total_capital_aud":100000,"holdings":[{"symbol":"ZIP","qty":500,"avg_price":2.10}]}`)

	res, err := Run(context.Background(), RunParams{
		Universe: []string{"ZIP", "BHP"}, LookbackMonths: 6, ExitLookbackMonths: 12,
		TopK: 2, ExitRankN: 3, RegimeEMADays: 200, Market: "XASX",
		Force: true, MinPriceAUD: DefaultMinPriceAUD,
	}, d)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "recommendation-"+res.Month+".json"))
	var rec Recommendation
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	sold := false
	for _, o := range rec.Orders {
		if o.Symbol == "ZIP" && o.Side == "SELL" {
			sold = true
		}
	}
	if !sold {
		t.Errorf("held ZIP fell below the floor and must be SELL; orders = %+v", rec.Orders)
	}
	if contains(rec.Holds, "ZIP") {
		t.Errorf("ZIP must not be held: %v", rec.Holds)
	}
}

// Drift checking that never fires must not read as a clean result.
func TestUnconfiguredDriftCheckWarns(t *testing.T) {
	w := driftWarnings(context.Background(), RunParams{DriftCheck: true, ConstituentsURL: ""},
		Deps{Log: slog.Default()})
	if len(w) != 1 || !strings.Contains(w[0], "NOT CHECKED") {
		t.Errorf("expected an explicit not-checked warning, got %v", w)
	}
}

func TestFormatMessageShowsRegimeAndFloor(t *testing.T) {
	off := false
	msg := FormatMessage(Recommendation{
		Month: "2026-07", RegimeInvested: false, RegimeFilter: &off,
		IndexClose: 8000, IndexEMA: 8100,
		Params:        RunParamsRecord{MinPriceAUD: 1.00, ExitRankN: 30},
		BelowMinPrice: []string{"TAH"},
	}, 6, 200)
	if !strings.Contains(msg, "staying invested") {
		t.Errorf("filter-off message must not read as a liquidation order:\n%s", msg)
	}
	if !strings.Contains(msg, "EMA200") {
		t.Errorf("expected EMA200 label:\n%s", msg)
	}
	if !strings.Contains(msg, "TAH") {
		t.Errorf("expected the below-floor list:\n%s", msg)
	}
}

func TestAUDFormatting(t *testing.T) {
	if got := aud(1234567); got != "A$1,234,567" {
		t.Errorf("aud = %q, want A$1,234,567", got)
	}
	if got := aud(950); got != "A$950" {
		t.Errorf("aud = %q, want A$950", got)
	}
	if got := formatStockPriceAUD(3.5); got != "A$3.50" {
		t.Errorf("price = %q, want A$3.50 (cents matter on the ASX)", got)
	}
	if got := formatMarketCapAUD(2.8e10); got != "A$28.0B" {
		t.Errorf("mcap = %q, want A$28.0B", got)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func pow(base, exp float64) float64 {
	out := 1.0
	for i := 0; i < int(exp); i++ {
		out *= base
	}
	return out
}
