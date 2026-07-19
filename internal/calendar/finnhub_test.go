package calendar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFinnhubFetchRaw(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"economicCalendar":[
			{"country":"US","event":"Non-Farm Payrolls","impact":"high","time":"2026-07-20 12:30:00"},
			{"country":"JP","event":"BOJ Rate Decision","impact":"high","time":"2026-07-21T03:00:00Z"}
		]}`))
	}))
	defer srv.Close()

	c := &FinnhubClient{APIKey: "test-key", HTTPClient: srv.Client(), BaseURL: srv.URL}
	raw, err := c.FetchRaw(context.Background(), time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 2 {
		t.Fatalf("got %d raw events", len(raw))
	}
	evs := FilterNormalize(raw)
	if len(evs) != 2 {
		t.Fatalf("filtered %d", len(evs))
	}
}

func TestFinnhubHTTPErrorTriggersFallbackPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	c := &FinnhubClient{APIKey: "k", HTTPClient: srv.Client(), BaseURL: srv.URL}
	_, err := c.FetchRaw(context.Background(), time.Now().UTC(), 7)
	if err == nil {
		t.Fatal("expected 403 error")
	}
}

func TestParseFinnhubTime(t *testing.T) {
	t1, err := parseFinnhubTime("2026-07-20 12:30:00")
	if err != nil || !t1.Equal(time.Date(2026, 7, 20, 12, 30, 0, 0, time.UTC)) {
		t.Fatalf("got %v err=%v", t1, err)
	}
	t2, err := parseFinnhubTime("2026-07-20T12:30:00Z")
	if err != nil || !t2.Equal(t1) {
		t.Fatalf("rfc got %v err=%v", t2, err)
	}
}
