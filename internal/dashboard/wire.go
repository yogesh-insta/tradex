package dashboard

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/storage"

	"github.com/yogesh-insta/tradex/internal/config"
	"github.com/yogesh-insta/tradex/internal/oanda"
)

// BuildFromConfig constructs Service + Server from the environment config.
// Mock mode never touches OANDA/GCS/BQ.
func BuildFromConfig(ctx context.Context, cfg *config.Config, log *slog.Logger) (*Server, error) {
	d := cfg.Dashboard
	if d.Mock {
		if len(d.Accounts) == 0 {
			d.Accounts = []config.DashboardAccountConfig{{Name: "eu-indices", OANDAID: "mock-eu-indices"}}
		}
		for i := range d.Accounts {
			if d.Accounts[i].OANDAID == "" {
				d.Accounts[i].OANDAID = "mock-" + d.Accounts[i].Name
			}
		}
	}
	auth, err := newAuth(d)
	if err != nil {
		return nil, err
	}

	var accounts AccountReader
	var objects ObjectFetcher
	var ledger LedgerQuerier
	calURI := d.CalendarFile
	if calURI == "" {
		calURI = d.GCS.CalendarObject
	}
	statusURI := d.StatusFile
	if statusURI == "" {
		statusURI = d.GCS.StatusObject
	}

	if d.Mock {
		accounts, objects, ledger, calURI, statusURI = mockStack(d)
	} else {
		client := oanda.NewClient(d.OANDA.Host, "", d.OANDA.Token, oanda.Options{
			RequestTimeout: 8 * time.Second,
			MaxRetries:     2,
		})
		accounts = OANDAReader{Client: client}

		fetcher := FileOrGCSFetcher{}
		if needsGCS(calURI) || needsGCS(statusURI) {
			gcs, err := storage.NewClient(ctx)
			if err != nil {
				return nil, fmt.Errorf("gcs client: %w", err)
			}
			fetcher.GCS = gcs
		}
		objects = fetcher

		if d.LedgerFile != "" {
			fl, err := LoadFileLedger(d.LedgerFile)
			if err != nil {
				return nil, fmt.Errorf("ledger file: %w", err)
			}
			ledger = fl
		} else {
			bq, err := bigquery.NewClient(ctx, d.BigQuery.Project)
			if err != nil {
				return nil, fmt.Errorf("bigquery client: %w", err)
			}
			ledger = &BigQueryLedger{
				Client: bq, Project: d.BigQuery.Project,
				Dataset: d.BigQuery.Dataset, Table: d.BigQuery.Table,
			}
		}
	}

	svc, err := NewService(d, Deps{
		Accounts: accounts, Objects: objects, Ledger: ledger,
		Log: log, CalendarURI: calURI, StatusURI: statusURI,
	})
	if err != nil {
		return nil, err
	}
	return NewServer(svc, auth, log), nil
}

func needsGCS(uri string) bool {
	return len(uri) >= 5 && uri[:5] == "gs://"
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

func mockStack(d config.DashboardConfig) (AccountReader, ObjectFetcher, LedgerQuerier, string, string) {
	id := "mock-account-1"
	if len(d.Accounts) > 0 && d.Accounts[0].OANDAID != "" {
		id = d.Accounts[0].OANDAID
	}
	name := "eu-indices"
	if len(d.Accounts) > 0 {
		name = d.Accounts[0].Name
	}
	accounts := MockAccountReader{
		Summaries: map[string]oanda.AccountSummary{
			id: {
				ID: id, Currency: "USD", Balance: 5000, NAV: 5025.5,
				MarginUsed: 120, MarginAvailable: 4880, UnrealizedPL: 25.5,
				ResettablePL: 40, PL: 210,
			},
		},
		Trades: map[string][]oanda.RESTTrade{
			id: {{
				ID: "1001", Instrument: "DE30_EUR", Price: 18450.2,
				OpenTime:     time.Now().UTC().Add(-2 * time.Hour),
				CurrentUnits: 1, UnrealizedPL: 25.5,
				StopLossOrder:   &oanda.DependentOrder{Price: 18380},
				TakeProfitOrder: &oanda.DependentOrder{Price: 18600},
			}},
		},
	}
	calURI := "mock://calendar"
	statusURI := "mock://status"
	now := time.Now().UTC()
	calJSON := []byte(fmt.Sprintf(`{"as_of":%q,"events":[{"region":"EU","title":"ECB Rate Decision","impact":"high","time":%q}]}`,
		now.Add(-10*time.Minute).Format(time.RFC3339),
		now.Add(3*time.Hour).Format(time.RFC3339)))
	statusJSON := []byte(fmt.Sprintf(`{"as_of":%q,"accounts":[{"name":%q,"state":"ACTIVE","stream_up":true,"last_tick_age_ms":800,"last_heartbeat_at":%q,"last_reconcile_ok":true,"market_data_stale":false}]}`,
		now.Format(time.RFC3339), name, now.Add(-5*time.Second).Format(time.RFC3339)))
	objects := StaticObjectFetcher{calURI: calJSON, statusURI: statusJSON}

	var trades []ClosedTrade
	for i := 0; i < 35; i++ {
		day := now.AddDate(0, 0, -i)
		pl := float64((i%5)-2) * 12.5
		trades = append(trades, ClosedTrade{
			TradeID: fmt.Sprintf("t%d", i), Account: name,
			RealizedPL: pl, CloseTime: time.Date(day.Year(), day.Month(), day.Day(), 12, 0, 0, 0, time.UTC),
		})
	}
	return accounts, objects, MemoryLedger(trades), calURI, statusURI
}
