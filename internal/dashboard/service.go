package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/yogesh-insta/tradex/internal/config"
)

// Deps wires external backends.
type Deps struct {
	Objects   ObjectFetcher
	NSEQuoter NSEQuoter // optional; enriches NSE portfolio with live marks
	Log       *slog.Logger
}

// Service aggregates the ETF and NSE report lanes with TTL caches.
type Service struct {
	cfg  config.DashboardConfig
	deps Deps
	log  *slog.Logger
	now  func() time.Time

	cache *ttlCache
}

// NewService builds the aggregator.
func NewService(cfg config.DashboardConfig, deps Deps) (*Service, error) {
	// Validated here so a bad tz fails at startup rather than in the page.
	if _, err := time.LoadLocation(cfg.UI.ReportingTZ); err != nil {
		return nil, fmt.Errorf("reporting_tz: %w", err)
	}
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		cfg: cfg, deps: deps, log: log, now: time.Now,
		cache: newTTLCache(),
	}, nil
}

// latestMonthly fetches <prefix>/<name>-YYYY-MM.json for the current month,
// falling back to the previous month. A monthly lane writes its file on the
// last trading day, so for most of any month the current-month object does not
// exist yet and the fallback is the normal path, not an error case.
//
// Returns an {"error": ...} map rather than a Go error: the dashboard renders
// the message in place, and one missing lane must not fail the whole page.
func (s *Service) latestMonthly(ctx context.Context, prefix, name, notFoundMsg, parseErrMsg string) map[string]any {
	now := s.now().UTC()
	obj := func(t time.Time) string {
		return prefix + "/" + name + "-" + t.Format("2006-01") + ".json"
	}
	data, err := s.deps.Objects.Fetch(ctx, obj(now))
	if err != nil {
		data, err = s.deps.Objects.Fetch(ctx, obj(now.AddDate(0, -1, 0)))
		if err != nil {
			return map[string]any{"error": notFoundMsg}
		}
	}
	var report map[string]any
	if err := json.Unmarshal(data, &report); err != nil {
		return map[string]any{"error": parseErrMsg}
	}
	return report
}

// LatestETFReport fetches the most recent ASX ETF monitor report from GCS.
func (s *Service) LatestETFReport(ctx context.Context) (map[string]any, error) {
	const prefix = "gs://tradex-demo-state/etfmonitor"
	return s.latestMonthly(ctx, prefix, "report",
		"no ETF report found", "failed to parse report"), nil
}

// LatestASXReport fetches the most recent ASX 200 rotator recommendation from
// GCS, plus the user-maintained portfolio.
//
// Unlike the NSE lane this does not enrich holdings with live quotes: the
// dashboard's quoter is NSE-wired, and the recommendation already carries
// last_close per symbol. Adding an ASX quoter is a separate change — better a
// visibly static mark than one silently priced off the wrong exchange.
func (s *Service) LatestASXReport(ctx context.Context) (map[string]any, error) {
	const prefix = "gs://tradex-demo-state/asxrotator"
	report := s.latestMonthly(ctx, prefix, "recommendation",
		"no ASX recommendation found", "failed to parse recommendation")
	if pdata, err := s.deps.Objects.Fetch(ctx, prefix+"/portfolio.json"); err == nil {
		var portfolio map[string]any
		if json.Unmarshal(pdata, &portfolio) == nil {
			report["portfolio"] = portfolio
		}
	} else {
		report["portfolio"] = map[string]any{"error": "portfolio.json not found"}
	}
	return report, nil
}

// LatestNSEReport fetches the most recent NSE momentum rotator recommendation from GCS.
func (s *Service) LatestNSEReport(ctx context.Context) (map[string]any, error) {
	const prefix = "gs://tradex-demo-state/nserotator"
	report := s.latestMonthly(ctx, prefix, "recommendation",
		"no NSE recommendation found", "failed to parse recommendation")
	if pdata, err := s.deps.Objects.Fetch(ctx, prefix+"/portfolio.json"); err == nil {
		var portfolio map[string]any
		if json.Unmarshal(pdata, &portfolio) == nil {
			if symbols := portfolioSymbols(portfolio); len(symbols) > 0 {
				enrichPortfolioQuotes(portfolio, s.fetchNSEQuotes(ctx, symbols))
			}
			report["portfolio"] = portfolio
		}
	} else {
		report["portfolio"] = map[string]any{"error": "portfolio.json not found"}
	}
	return report, nil
}

func portfolioSymbols(portfolio map[string]any) []string {
	holdings, ok := portfolio["holdings"].([]any)
	if !ok {
		return nil
	}
	symbols := make([]string, 0, len(holdings))
	for _, item := range holdings {
		h, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if sym, _ := h["symbol"].(string); sym != "" {
			symbols = append(symbols, sym)
		}
	}
	return symbols
}

// UIConfig exposes refresh interval etc. to the embedded page.
func (s *Service) UIConfig() map[string]any {
	return map[string]any{
		"refresh_interval_ms": s.cfg.UI.RefreshInterval.D().Milliseconds(),
		"reporting_tz":        s.cfg.UI.ReportingTZ,
		"mock":                s.cfg.Mock,
	}
}
