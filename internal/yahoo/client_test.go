package yahoo

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchQuoteDetails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v7/finance/spark"):
			_, _ = w.Write([]byte(`{
				"spark": {
					"result": [
						{
							"symbol": "TITAN.NS",
							"response": [{
								"meta": {
									"symbol": "TITAN.NS",
									"longName": "Titan Company Limited",
									"regularMarketPrice": 4721.0
								}
							}]
						},
						{
							"symbol": "BAJFINANCE.NS",
							"response": [{
								"meta": {
									"symbol": "BAJFINANCE.NS",
									"longName": "Bajaj Finance Limited",
									"regularMarketPrice": 1060.2
								}
							}]
						}
					]
				}
			}`))
		case r.URL.Path == "/v1/test/getcrumb":
			_, _ = w.Write([]byte("testcrumb"))
		case strings.HasPrefix(r.URL.Path, "/v10/finance/quoteSummary/"):
			_, _ = w.Write([]byte(`{
				"quoteSummary": {
					"result": [{
						"summaryDetail": {
							"marketCap": {"raw": 2450000000000}
						}
					}]
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	jar, _ := cookiejar.New(nil)
	c := &YahooClient{
		BaseURL:    srv.URL,
		Retries:    1,
		HTTPClient: &http.Client{Jar: jar},
		Suffix:     ".NS",
	}
	quotes := c.FetchQuoteDetails(context.Background(), []string{"TITAN", "BAJFINANCE"})
	if quotes["TITAN"].CompanyName != "Titan Company Limited" {
		t.Fatalf("TITAN name = %q", quotes["TITAN"].CompanyName)
	}
	if quotes["TITAN"].Price != 4721.0 {
		t.Fatalf("TITAN price = %v", quotes["TITAN"].Price)
	}
	if quotes["TITAN"].MarketCap != 2.45e12 {
		t.Fatalf("TITAN cap = %v", quotes["TITAN"].MarketCap)
	}
	if quotes["BAJFINANCE"].CompanyName != "Bajaj Finance Limited" {
		t.Fatalf("BAJFINANCE name = %q", quotes["BAJFINANCE"].CompanyName)
	}
}

func TestEnsureCrumbToleratesBootstrap404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bootstrap-404":
			http.NotFound(w, r)
		case "/v1/test/getcrumb":
			_, _ = w.Write([]byte("testcrumb"))
		case "/v10/finance/quoteSummary/TITAN.NS":
			_, _ = w.Write([]byte(`{"quoteSummary":{"result":[{"summaryDetail":{"marketCap":{"raw":1e12}}}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	jar, _ := cookiejar.New(nil)
	c := &YahooClient{BaseURL: srv.URL, Retries: 1, HTTPClient: &http.Client{Jar: jar}, Suffix: ".NS"}
	if _, err := c.getSessionBootstrap(context.Background(), srv.URL+"/bootstrap-404"); err != nil {
		t.Fatalf("bootstrap 404: %v", err)
	}
	if err := c.ensureCrumb(context.Background()); err != nil {
		t.Fatalf("ensureCrumb: %v", err)
	}
	cap, err := c.fetchMarketCap(context.Background(), "TITAN")
	if err != nil || cap != 1e12 {
		t.Fatalf("fetchMarketCap = %v err=%v", cap, err)
	}
}
