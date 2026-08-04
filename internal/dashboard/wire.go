package dashboard

import (
	"context"
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

	if d.Mock {
		objects = mockStack()
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

		nseQuoter = YahooNSEQuoter{Client: &nserotator.YahooClient{
			Suffix:  nserotator.YahooSuffix,
			Timeout: 30 * time.Second,
			Retries: 3,
			Log:     log,
		}}
	}

	svc, err := NewService(d, Deps{
		Objects: objects, NSEQuoter: nseQuoter, Log: log,
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

func mockStack() ObjectFetcher {
	objects := StaticObjectFetcher{}
	for uri, body := range mockNSEState(time.Now().UTC()) {
		objects[uri] = body
	}
	return objects
}
