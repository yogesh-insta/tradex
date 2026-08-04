package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yogesh-insta/tradex/internal/config"
)

func testCfg() config.DashboardConfig {
	return config.DashboardConfig{
		ListenAddr: ":0",
		Auth:       config.DashboardAuthConfig{Mode: "bearer", Token: "secret"},
		CacheTTL:   config.DashboardCacheTTLConfig{Overview: config.Duration(time.Minute)},
		UI: config.DashboardUIConfig{
			RefreshInterval: config.Duration(20 * time.Second),
			ReportingTZ:     "UTC",
		},
	}
}

// testService builds a Service over a fixed set of stored objects. Objects are
// the dashboard's only backend now that the broker and P&L surfaces are gone.
func testService(t *testing.T, objects StaticObjectFetcher) *Service {
	t.Helper()
	if objects == nil {
		objects = StaticObjectFetcher{}
	}
	svc, err := NewService(testCfg(), Deps{Objects: objects})
	if err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC) }
	return svc
}

func TestAuthRejectsUnauthenticated(t *testing.T) {
	svc := testService(t, nil)
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
	svc := testService(t, nil)
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
	svc := testService(t, nil)
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
	svc := testService(t, nil)
	srv := NewServer(svc, BearerAuth{Token: "secret"}, nil)
	h := srv.Handler()

	for _, path := range []string{"/", "/etf", "/nse", "/asx"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer secret")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rr.Code)
		}
		for _, want := range []string{`id="sec-etf"`, `id="sec-nse"`, `id="sec-asx"`, `id="lanes"`} {
			if !strings.Contains(rr.Body.String(), want) {
				t.Errorf("GET %s: body missing %s", path, want)
			}
		}
	}

	// Unauthenticated lane requests must be rejected like any other page.
	for _, path := range []string{"/etf", "/nse", "/asx"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusUnauthorized && rr.Code != http.StatusForbidden {
			t.Errorf("GET %s without token = %d, want 401/403", path, rr.Code)
		}
	}
}
