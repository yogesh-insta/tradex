package oanda

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTransactionsRequestsTodayOrderFills(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/accounts/account-1/transactions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"transactions":[{"id":"1","type":"ORDER_FILL","time":"2026-07-19T01:00:00Z","pl":"-12.50"}]}`))
	}))
	defer server.Close()

	client := NewClient("unused", "unused", "token", Options{})
	client.restBase = server.URL
	from := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	to := from.Add(10 * time.Hour)
	resp, err := client.Transactions(context.Background(), "account-1", from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Transactions) != 1 || float64(resp.Transactions[0].PL) != -12.5 {
		t.Fatalf("transactions = %#v", resp.Transactions)
	}
	if got, want := gotQuery, "from=2026-07-19T00%3A00%3A00Z&pageSize=1000&to=2026-07-19T10%3A00%3A00Z&type=ORDER_FILL"; got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
}

func TestSetTradeClientExtensions(t *testing.T) {
	var got TradeClientExtensionsBody
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("method = %s", r.Method)
		}
		if r.URL.Path != "/v3/accounts/account-1/trades/trade-1/clientExtensions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := NewClient("unused", "unused", "token", Options{})
	client.restBase = server.URL
	err := client.SetTradeClientExtensions(context.Background(), "account-1", "trade-1", ClientExtensions{
		ID: "order-1", Tag: "fx-trld", Comment: "tradex:initial-risk=0.00625",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ClientExtensions.ID != "order-1" || got.ClientExtensions.Tag != "fx-trld" || got.ClientExtensions.Comment != "tradex:initial-risk=0.00625" {
		t.Fatalf("extensions = %#v", got.ClientExtensions)
	}
}
