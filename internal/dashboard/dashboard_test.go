package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yogesh-insta/tradex/internal/config"
	"github.com/yogesh-insta/tradex/internal/oanda"
)

func testCfg() config.DashboardConfig {
	return config.DashboardConfig{
		ListenAddr: ":0",
		Auth:       config.DashboardAuthConfig{Mode: "bearer", Token: "secret"},
		Accounts:   []config.DashboardAccountConfig{{Name: "eu-indices", OANDAID: "acc-1"}},
		CacheTTL: config.DashboardCacheTTLConfig{
			Overview: config.Duration(time.Minute),
			Calendar: config.Duration(time.Minute),
			PL:       config.Duration(3 * time.Minute),
		},
		UI: config.DashboardUIConfig{
			RefreshInterval:     config.Duration(20 * time.Second),
			PLDailyLookbackDays: 30,
			ReportingTZ:         "UTC",
		},
		Health: config.DashboardHealthConfig{
			HeartbeatStale: config.Duration(120 * time.Second),
			TickStale:      config.Duration(60 * time.Second),
			CalendarStale:  config.Duration(90 * time.Minute),
		},
	}
}

func testService(t *testing.T, ledger []ClosedTrade, calJSON, statusJSON []byte) *Service {
	t.Helper()
	cfg := testCfg()
	objects := StaticObjectFetcher{}
	if calJSON != nil {
		objects["cal"] = calJSON
	}
	if statusJSON != nil {
		objects["status"] = statusJSON
	}
	accounts := MockAccountReader{
		Summaries: map[string]oanda.AccountSummary{
			"acc-1": {ID: "acc-1", Currency: "USD", NAV: 5000, Balance: 5000, ResettablePL: 10},
		},
		Trades: map[string][]oanda.RESTTrade{
			"acc-1": {{
				ID: "t1", Instrument: "DE30_EUR", Price: 18000, CurrentUnits: 1,
				OpenTime: time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC), UnrealizedPL: 5,
			}},
		},
	}
	svc, err := NewService(cfg, Deps{
		Accounts: accounts, Objects: objects, Ledger: MemoryLedger(ledger),
		CalendarURI: "cal", StatusURI: "status",
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC) }
	return svc
}

func TestAuthRejectsUnauthenticated(t *testing.T) {
	svc := testService(t, nil, []byte(`{"as_of":"2026-07-19T11:00:00Z","events":[]}`), nil)
	srv := NewServer(svc, BearerAuth{Token: "secret"}, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	for _, path := range []string{
		"/", "/api/overview", "/api/calendar", "/api/pl?account=eu-indices",
		"/api/pl/daily?account=eu-indices", "/api/etf", "/api/nse", "/api/ui-config",
	} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s: want 401, got %d", path, resp.StatusCode)
		}
	}
	// Health is open.
	resp, err := http.Get(ts.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health: %d", resp.StatusCode)
	}
}

func TestAuthAcceptsBearer(t *testing.T) {
	svc := testService(t, nil, []byte(`{"as_of":"2026-07-19T11:00:00Z","events":[]}`), nil)
	srv := NewServer(svc, BearerAuth{Token: "secret"}, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/overview", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d", resp.StatusCode)
	}
}

func TestOverviewOpenTrades(t *testing.T) {
	svc := testService(t, nil, []byte(`{"as_of":"2026-07-19T11:00:00Z","events":[]}`), nil)
	ov := svc.Overview(context.Background())
	if len(ov.Trades) != 1 || ov.Trades[0].Instrument != "DE30_EUR" {
		t.Fatalf("trades: %+v", ov.Trades)
	}
	if ov.Accounts[0].NAV != 5000 {
		t.Fatalf("nav: %v", ov.Accounts[0].NAV)
	}
}

func TestCalendarParsesStateAndFailSafe(t *testing.T) {
	cal := []byte(`{
		"as_of":"2026-07-19T11:00:00Z",
		"events":[
			{"region":"EU","title":"ECB","impact":"high","time":"2026-07-19T14:00:00Z"},
			{"region":"EU","title":"old","impact":"high","time":"2026-07-18T14:00:00Z"},
			{"region":"EU","title":"low","impact":"low","time":"2026-07-20T14:00:00Z"}
		]
	}`)
	svc := testService(t, nil, cal, nil)
	c := svc.Calendar(context.Background())
	if !c.Fresh || len(c.Events) != 1 || c.Events[0].Title != "ECB" {
		t.Fatalf("calendar: %+v", c)
	}
	if c.Events[0].TimeBerlin == "" {
		t.Fatal("expected Berlin wall time")
	}

	// Missing object.
	svc2 := testService(t, nil, nil, nil)
	c2 := svc2.Calendar(context.Background())
	if !c2.Missing || c2.Warning == "" {
		t.Fatalf("want fail-safe missing: %+v", c2)
	}
}

func TestCalendarSortsSoonestFirst(t *testing.T) {
	cal := []byte(`{
		"as_of":"2026-07-19T11:00:00Z",
		"events":[
			{"region":"EU","title":"Soon","impact":"high","time":"2026-07-19T14:00:00Z"},
			{"region":"US","title":"Later","impact":"high","time":"2026-07-29T18:00:00Z"},
			{"region":"JP","title":"Mid","impact":"high","time":"2026-07-23T12:00:00Z"}
		]
	}`)
	svc := testService(t, nil, cal, nil)
	c := svc.Calendar(context.Background())
	if len(c.Events) != 3 {
		t.Fatalf("want 3 events, got %+v", c.Events)
	}
	want := []string{"Soon", "Mid", "Later"}
	for i, title := range want {
		if c.Events[i].Title != title {
			t.Fatalf("events[%d]=%q want %q (soonest-first)", i, c.Events[i].Title, title)
		}
	}
	for i := 1; i < len(c.Events); i++ {
		if c.Events[i-1].TimeUTC.After(c.Events[i].TimeUTC) {
			t.Fatalf("not ascending: %v after %v", c.Events[i-1].TimeUTC, c.Events[i].TimeUTC)
		}
	}
}

func TestPLDailyAndSevenDayConsistency(t *testing.T) {
	// Build known daily PLs across 10 days.
	var trades []ClosedTrade
	base := time.Date(2026, 7, 19, 15, 0, 0, 0, time.UTC)
	expected := map[string]float64{}
	for i := 0; i < 10; i++ {
		day := base.AddDate(0, 0, -i)
		pl := float64(i + 1) // 1..10
		key := day.Format("2006-01-02")
		expected[key] += pl
		trades = append(trades, ClosedTrade{
			Account: "eu-indices", RealizedPL: pl, CloseTime: day,
			TradeID: day.Format("20060102"),
		})
		// second trade same day
		trades = append(trades, ClosedTrade{
			Account: "eu-indices", RealizedPL: 0.5, CloseTime: day.Add(time.Hour),
			TradeID: day.Format("20060102") + "b",
		})
		expected[key] += 0.5
	}
	svc := testService(t, trades, []byte(`{"as_of":"2026-07-19T11:00:00Z","events":[]}`), nil)
	from := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	daily := svc.DailyPLSeries(context.Background(), "eu-indices", from, to)
	if len(daily.Days) != 10 {
		t.Fatalf("days: %d %+v", len(daily.Days), daily)
	}
	sum7 := 0.0
	for i, d := range daily.Days {
		if i < 7 {
			sum7 += d.RealizedPL
		}
		if d.RealizedPL != expected[d.Day] {
			t.Fatalf("day %s: got %v want %v", d.Day, d.RealizedPL, expected[d.Day])
		}
	}
	if daily.Total7d != sum7 {
		t.Fatalf("total_7d %v != sum last 7 %v", daily.Total7d, sum7)
	}
	pl7 := svc.PL(context.Background(), "eu-indices", "7d")
	if pl7.TotalPL != sum7 {
		t.Fatalf("pl 7d %v != %v", pl7.TotalPL, sum7)
	}
	all := svc.PL(context.Background(), "eu-indices", "all")
	if all.TradeCount != 20 {
		t.Fatalf("all count %d", all.TradeCount)
	}
}

func TestDailyPLFromOANDATransactionsExcludesOpeningFills(t *testing.T) {
	from := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	days := dailyPLFromTransactions([]oanda.Transaction{
		{Type: "ORDER_FILL", Time: from.Add(time.Hour), PL: 0}, // opening fill
		{Type: "ORDER_FILL", Time: from.Add(2 * time.Hour), PL: 4.5},
		{Type: "ORDER_FILL", Time: from.AddDate(0, 0, 1).Add(time.Hour), PL: -2},
		{Type: "TRANSFER_FUNDS", Time: from.Add(time.Hour), PL: 99},
	}, from, from.AddDate(0, 0, 1), time.UTC)
	if len(days) != 2 {
		t.Fatalf("days = %+v", days)
	}
	if days[0].Day != "2026-07-19" || days[0].RealizedPL != -2 || days[0].TradeCount != 1 || days[0].Losses != 1 {
		t.Fatalf("newest day = %+v", days[0])
	}
	if days[1].Day != "2026-07-18" || days[1].RealizedPL != 4.5 || days[1].TradeCount != 1 || days[1].Wins != 1 {
		t.Fatalf("oldest day = %+v", days[1])
	}
}

func TestFallbackLedgerUsesOANDAWhenPrimaryEmpty(t *testing.T) {
	fallback := MemoryLedger([]ClosedTrade{{
		Account: "eu-indices", RealizedPL: 7, CloseTime: time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC),
	}})
	ledger := FallbackLedger{Primary: MemoryLedger(nil), Fallback: fallback}
	days, err := ledger.DailyPL(context.Background(), "eu-indices",
		time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC), time.UTC)
	if err != nil || len(days) != 1 || days[0].RealizedPL != 7 {
		t.Fatalf("fallback daily = %+v, %v", days, err)
	}
	total, count, err := ledger.AllTimePL(context.Background(), "eu-indices")
	if err != nil || total != 7 || count != 1 {
		t.Fatalf("fallback all = %v, %d, %v", total, count, err)
	}
}

func TestPLCacheAvoidsRequery(t *testing.T) {
	counting := &countingLedger{inner: MemoryLedger([]ClosedTrade{{
		Account: "eu-indices", RealizedPL: 1, CloseTime: time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC),
	}})}
	cfg := testCfg()
	svc, err := NewService(cfg, Deps{
		Accounts: MockAccountReader{Summaries: map[string]oanda.AccountSummary{"acc-1": {ID: "acc-1"}}},
		Objects:  StaticObjectFetcher{"cal": []byte(`{"as_of":"2026-07-19T11:00:00Z","events":[]}`)},
		Ledger:   counting, CalendarURI: "cal",
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC) }
	_ = svc.PL(context.Background(), "eu-indices", "7d")
	_ = svc.PL(context.Background(), "eu-indices", "7d")
	if counting.dailyCalls != 1 {
		t.Fatalf("expected 1 daily call within TTL, got %d", counting.dailyCalls)
	}
}

type countingLedger struct {
	inner      LedgerQuerier
	dailyCalls int
	allCalls   int
}

func (c *countingLedger) DailyPL(ctx context.Context, account string, from, to time.Time, loc *time.Location) ([]DailyPL, error) {
	c.dailyCalls++
	return c.inner.DailyPL(ctx, account, from, to, loc)
}
func (c *countingLedger) AllTimePL(ctx context.Context, account string) (float64, int64, error) {
	c.allCalls++
	return c.inner.AllTimePL(ctx, account)
}

func TestDayBucketUsesReportingTZ(t *testing.T) {
	// close_time 2026-07-19 01:30 UTC → still 2026-07-19 in UTC, but 2026-07-19 03:30 in Berlin (CEST).
	// Use America/New_York: 2026-07-18 22:30 → day 2026-07-18.
	trades := []ClosedTrade{{
		Account: "eu-indices", RealizedPL: 9,
		CloseTime: time.Date(2026, 7, 19, 2, 30, 0, 0, time.UTC), // 22:30 previous evening ET
	}}
	cfg := testCfg()
	cfg.UI.ReportingTZ = "America/New_York"
	svc, err := NewService(cfg, Deps{
		Accounts: MockAccountReader{Summaries: map[string]oanda.AccountSummary{"acc-1": {}}},
		Objects:  StaticObjectFetcher{"cal": []byte(`{"as_of":"2026-07-19T11:00:00Z","events":[]}`)},
		Ledger:   MemoryLedger(trades), CalendarURI: "cal",
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC) }
	from := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	daily := svc.DailyPLSeries(context.Background(), "eu-indices", from, to)
	if len(daily.Days) != 1 || daily.Days[0].Day != "2026-07-18" {
		t.Fatalf("want day 2026-07-18 in America/New_York, got %+v", daily.Days)
	}
}

func TestHealthDegradesWithoutStatus(t *testing.T) {
	svc := testService(t, nil, []byte(`{"as_of":"2026-07-19T11:00:00Z","events":[]}`), nil)
	ov := svc.Overview(context.Background())
	if len(ov.Health) != 1 || ov.Health[0].Level == HealthGreen {
		t.Fatalf("expected degraded health without status.json: %+v", ov.Health)
	}
	if ov.Health[0].StatusSource != "derived" {
		t.Fatalf("status source: %s", ov.Health[0].StatusSource)
	}
}

func TestIAPAuth(t *testing.T) {
	a := IAPAuth{AllowEmails: map[string]struct{}{"ops@example.com": {}}}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if err := a.Authorize(req); err == nil {
		t.Fatal("expected reject without header")
	}
	req.Header.Set("X-Goog-Authenticated-User-Email", "accounts.google.com:ops@example.com")
	if err := a.Authorize(req); err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Goog-Authenticated-User-Email", "accounts.google.com:other@example.com")
	if err := a.Authorize(req); err == nil {
		t.Fatal("expected forbid")
	}
}

func TestBearerFailClosedEmptyToken(t *testing.T) {
	a := BearerAuth{Token: ""}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer x")
	if err := a.Authorize(req); err == nil {
		t.Fatal("expected fail closed")
	}
}

func TestUIServesHTML(t *testing.T) {
	svc := testService(t, nil, []byte(`{"as_of":"2026-07-19T11:00:00Z","events":[]}`), nil)
	srv := NewServer(svc, BearerAuth{Token: "secret"}, nil)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 || rr.Header().Get("Content-Type") == "" {
		t.Fatalf("ui: %d %s", rr.Code, rr.Header().Get("Content-Type"))
	}
	if !strings.Contains(rr.Body.String(), "Tradex") {
		t.Fatal("missing brand")
	}
}

func TestLatestNSEReportIncludesPortfolio(t *testing.T) {
	const prefix = "gs://tradex-demo-state/nserotator"
	rec := []byte(`{"month":"2026-07","orders":[{"side":"SELL","symbol":"FOO","qty":1}],"top_ranked":[]}`)
	pf := []byte(`{"as_of":"2026-07-20","total_capital_inr":100000,"holdings":[{"symbol":"BAR","qty":10,"avg_price":50}]}`)
	objects := StaticObjectFetcher{
		prefix + "/recommendation-2026-07.json": rec,
		prefix + "/portfolio.json":              pf,
	}
	svc, err := NewService(testCfg(), Deps{Objects: objects})
	if err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC) }

	out, err := svc.LatestNSEReport(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out["month"] != "2026-07" {
		t.Fatalf("month: %v", out["month"])
	}
	pfOut, ok := out["portfolio"].(map[string]any)
	if !ok {
		t.Fatalf("portfolio: %#v", out["portfolio"])
	}
	if pfOut["as_of"] != "2026-07-20" {
		t.Fatalf("as_of: %v", pfOut["as_of"])
	}
	holdings, ok := pfOut["holdings"].([]any)
	if !ok || len(holdings) != 1 {
		t.Fatalf("holdings: %#v", pfOut["holdings"])
	}
}

// ETF and NSE live on their own paths so neither page renders the other's
// tables. All three serve the same document; the client picks the lane from
// location.pathname. Auth must still apply — /nse is not a bypass.
func TestUILanesServeAndStayProtected(t *testing.T) {
	svc := testService(t, nil, []byte(`{"as_of":"2026-07-19T11:00:00Z","events":[]}`), nil)
	srv := NewServer(svc, BearerAuth{Token: "secret"}, nil)
	h := srv.Handler()

	for _, path := range []string{"/", "/etf", "/nse"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer secret")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rr.Code)
		}
		for _, want := range []string{`id="sec-etf"`, `id="sec-nse"`, `id="lanes"`} {
			if !strings.Contains(rr.Body.String(), want) {
				t.Errorf("GET %s: body missing %s", path, want)
			}
		}
	}

	// Unauthenticated lane requests must be rejected like any other page.
	for _, path := range []string{"/etf", "/nse"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusUnauthorized && rr.Code != http.StatusForbidden {
			t.Errorf("GET %s without token = %d, want 401/403", path, rr.Code)
		}
	}
}
