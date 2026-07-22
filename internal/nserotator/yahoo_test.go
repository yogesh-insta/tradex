package nserotator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchMarketCaps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v7/finance/quote" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{
			"quoteResponse": {
				"result": [
					{"symbol": "TITAN.NS", "marketCap": 2450000000000},
					{"symbol": "BAJFINANCE.NS", "marketCap": 8120000000000}
				]
			}
		}`))
	}))
	defer srv.Close()

	c := &YahooClient{BaseURL: srv.URL, Retries: 1}
	mcaps := c.FetchMarketCaps(context.Background(), []string{"TITAN", "BAJFINANCE"})
	if mcaps["TITAN"] != 2.45e12 {
		t.Fatalf("TITAN cap = %v", mcaps["TITAN"])
	}
	if mcaps["BAJFINANCE"] != 8.12e12 {
		t.Fatalf("BAJFINANCE cap = %v", mcaps["BAJFINANCE"])
	}
}
