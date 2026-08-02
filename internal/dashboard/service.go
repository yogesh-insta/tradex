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

// LatestETFReport fetches the most recent ASX ETF monitor report from GCS.
func (s *Service) LatestETFReport(ctx context.Context) (map[string]any, error) {
	const prefix = "gs://tradex-demo-state/etfmonitor"
	now := s.now().UTC()
	data, err := s.deps.Objects.Fetch(ctx, prefix+"/report-"+now.Format("2006-01")+".json")
	if err != nil {
		prevMonth := now.AddDate(0, -1, 0)
		data, err = s.deps.Objects.Fetch(ctx, prefix+"/report-"+prevMonth.Format("2006-01")+".json")
		if err != nil {
			return map[string]any{"error": "no ETF report found"}, nil
		}
	}
	var report map[string]any
	if err := json.Unmarshal(data, &report); err != nil {
		return map[string]any{"error": "failed to parse report"}, nil
	}
	return report, nil
}

// LatestNSEReport fetches the most recent NSE momentum rotator recommendation from GCS.
func (s *Service) LatestNSEReport(ctx context.Context) (map[string]any, error) {
	const prefix = "gs://tradex-demo-state/nserotator"
	now := s.now().UTC()
	data, err := s.deps.Objects.Fetch(ctx, prefix+"/recommendation-"+now.Format("2006-01")+".json")
	if err != nil {
		prevMonth := now.AddDate(0, -1, 0)
		data, err = s.deps.Objects.Fetch(ctx, prefix+"/recommendation-"+prevMonth.Format("2006-01")+".json")
		if err != nil {
			return map[string]any{"error": "no NSE recommendation found"}, nil
		}
	}
	var report map[string]any
	if err := json.Unmarshal(data, &report); err != nil {
		return map[string]any{"error": "failed to parse recommendation"}, nil
	}
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
