package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"cloud.google.com/go/storage"

	"github.com/yogesh-insta/tradex/internal/config"
	"github.com/yogesh-insta/tradex/internal/nserotator"
)

// BuildFromConfig constructs Service + Server from the environment config.
// Mock mode never touches GCS.
func BuildFromConfig(ctx context.Context, cfg *config.Config, log *slog.Logger) (*Server, error) {
	d := cfg.Dashboard
	auth, err := newAuth(d)
	if err != nil {
		return nil, err
	}

	var objects ObjectFetcher
	var nseQuoter NSEQuoter
	var nseSeries DailyFetcher
	var etfSeries DailyFetcher

	if d.Mock {
		objects = mockStack(time.Now().UTC())
	} else {
		fetcher := FileOrGCSFetcher{}
		// Both report lanes live on GCS; the client is only built when one of
		// them actually points there.
		gcs, err := storage.NewClient(ctx)
		if err != nil {
			return nil, fmt.Errorf("gcs client: %w", err)
		}
		fetcher.GCS = gcs
		objects = fetcher

		nseClient := &nserotator.YahooClient{
			Suffix:  nserotator.YahooSuffix,
			Timeout: 30 * time.Second,
			Retries: 3,
			Log:     log,
		}
		nseQuoter = YahooNSEQuoter{Client: nseClient}
		nseSeries = nseClient
		etfSeries = &nserotator.YahooClient{
			Suffix:  ".AX",
			Timeout: 30 * time.Second,
			Retries: 3,
			Log:     log,
		}
	}

	svc, err := NewService(d, Deps{
		Objects: objects, NSEQuoter: nseQuoter,
		NSESeries: nseSeries, ETFSeries: etfSeries, Log: log,
	})
	if err != nil {
		return nil, err
	}
	return NewServer(svc, auth, log), nil
}

func newAuth(d config.DashboardConfig) (Authenticator, error) {
	switch d.Auth.Mode {
	case "bearer", "":
		if d.Auth.Token == "" && !d.Mock {
			return nil, fmt.Errorf("dashboard auth: bearer token empty (fail closed)")
		}
		tok := d.Auth.Token
		if tok == "" && d.Mock {
			tok = "mock-token"
		}
		return BearerAuth{Token: tok}, nil
	case "iap":
		allow := map[string]struct{}{}
		for _, e := range d.Auth.IAPAllowEmails {
			allow[strings.ToLower(strings.TrimSpace(e))] = struct{}{}
		}
		return IAPAuth{AllowEmails: allow}, nil
	default:
		return nil, fmt.Errorf("dashboard auth: unknown mode %q", d.Auth.Mode)
	}
}

func mockStack(now time.Time) ObjectFetcher {
	objects := StaticObjectFetcher{}
	for uri, body := range mockNSEState(now) {
		objects[uri] = body
	}
	for uri, body := range mockETFState(now) {
		objects[uri] = body
	}
	return objects
}

func mockETFState(now time.Time) map[string][]byte {
	const prefix = "gs://tradex-demo-state/etfmonitor"
	report := map[string]any{
		"month":  now.Format("2006-01"),
		"run_at": now.Format(time.RFC3339),
		"top": []map[string]any{
			{"ticker": "SEMI", "name": "Semiconductors", "score": 0.42},
			{"ticker": "HACK", "name": "Global Cybersecurity", "score": 0.31},
			{"ticker": "ASIA", "name": "Asia Technology Tigers", "score": 0.28},
		},
		"exit_alerts": []any{},
		"warnings":    []string{"mock data — not a real report"},
	}
	holdings := map[string]any{
		"as_of": now.Format("2006-01-02"),
		"holdings": []map[string]any{
			{"ticker": "ASIA", "qty": 10, "avg_price": 0},
			{"ticker": "CLDD", "qty": 20, "avg_price": 0},
			{"ticker": "HACK", "qty": 30, "avg_price": 0},
			{"ticker": "SEMI", "qty": 40, "avg_price": 0},
		},
	}
	repJSON, _ := json.Marshal(report)
	hJSON, _ := json.Marshal(holdings)
	eqJSON, _ := json.Marshal(mockEquity("etf", "AUD", now, 0, 10000, 1.0012))
	return map[string][]byte{
		prefix + "/report-" + now.Format("2006-01") + ".json": repJSON,
		prefix + "/holdings.json":                             hJSON,
		prefix + "/equity-history.json":                       eqJSON,
	}
}
