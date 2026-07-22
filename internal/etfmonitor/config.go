// Package etfmonitor implements the monthly, advisory-only ASX ETF momentum
// monitor (docs/specs/21-asx-etf-monitor.md). It never places orders; it ranks
// trending funds, raises exit alerts for funds the user holds, and reports via
// Telegram. Deliberately self-contained: it does not touch the NSE rotator, the
// OANDA trading config, or the hot path.
//
// Signal math is a faithful port of kite/betashares/screen.py, pinned by
// table-driven tests (the same discipline nserotator applies to momentum.py).
package etfmonitor

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the standalone YAML schema for cmd/etfmonitor. Secrets appear only
// as ${ENV_VAR} references, resolved at load (repo standard).
type Config struct {
	Env string `yaml:"env"`

	Monitor struct {
		MomentumLookbacksTD []int     `yaml:"momentum_lookbacks_td"`
		RecencyWeights      []float64 `yaml:"recency_weights"`
		TrendSMADays        int       `yaml:"trend_sma_days"`
		TopN                int       `yaml:"top_n"`
		UniverseFile        string    `yaml:"universe_file"`
		// GCSPrefix like gs://bucket/etfmonitor — holdings.json, heartbeat.json
		// and report-YYYY-MM.json live under it. Empty = local files under
		// LocalStateDir (dev).
		GCSPrefix     string `yaml:"gcs_prefix"`
		LocalStateDir string `yaml:"local_state_dir"`
		YahooTimeoutS int    `yaml:"yahoo_timeout_s"`
		DriftCheck    bool   `yaml:"drift_check"`
	} `yaml:"etfmonitor"`

	Telegram struct {
		BotToken string `yaml:"bot_token"`
		ChatID   string `yaml:"chat_id"`
	} `yaml:"telegram"`
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// LoadConfig reads, env-resolves, and fail-fast validates the config.
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("etfmonitor config: %w", err)
	}
	resolved := envRef.ReplaceAllStringFunc(string(raw), func(m string) string {
		return os.Getenv(envRef.FindStringSubmatch(m)[1])
	})
	var cfg Config
	if err := yaml.Unmarshal([]byte(resolved), &cfg); err != nil {
		return nil, fmt.Errorf("etfmonitor config parse: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) validate() error {
	m := &c.Monitor
	if len(m.MomentumLookbacksTD) == 0 {
		m.MomentumLookbacksTD = []int{63, 126, 252}
	}
	if len(m.RecencyWeights) == 0 {
		m.RecencyWeights = []float64{0.5, 0.3, 0.2}
	}
	if len(m.RecencyWeights) != len(m.MomentumLookbacksTD) {
		return fmt.Errorf("etfmonitor config: recency_weights (%d) must match momentum_lookbacks_td (%d)",
			len(m.RecencyWeights), len(m.MomentumLookbacksTD))
	}
	for i, lb := range m.MomentumLookbacksTD {
		if lb <= 0 {
			return fmt.Errorf("etfmonitor config: momentum_lookbacks_td[%d] must be > 0", i)
		}
		if i > 0 && lb <= m.MomentumLookbacksTD[i-1] {
			return fmt.Errorf("etfmonitor config: momentum_lookbacks_td must be strictly ascending (shortest first)")
		}
	}
	for i, w := range m.RecencyWeights {
		if w <= 0 {
			return fmt.Errorf("etfmonitor config: recency_weights[%d] must be > 0", i)
		}
	}
	if m.TrendSMADays <= 0 {
		m.TrendSMADays = 200
	}
	if m.TopN <= 0 {
		m.TopN = 10
	}
	if m.YahooTimeoutS <= 0 {
		m.YahooTimeoutS = 30
	}
	if m.UniverseFile == "" {
		return fmt.Errorf("etfmonitor config: universe_file required")
	}
	if m.GCSPrefix == "" && m.LocalStateDir == "" {
		return fmt.Errorf("etfmonitor config: one of gcs_prefix or local_state_dir required")
	}
	if m.GCSPrefix != "" && !strings.HasPrefix(m.GCSPrefix, "gs://") {
		return fmt.Errorf("etfmonitor config: gcs_prefix must start with gs://")
	}
	if c.Env == "" {
		c.Env = "prod"
	}
	return nil
}

// Fund groups (spec 21 §Universe).
const (
	GroupStandard = "standard"
	GroupGeared   = "geared"
	GroupInverse  = "inverse"
	GroupFX       = "fx"
	GroupExcluded = "excluded"
)

// Fund is one universe entry.
type Fund struct {
	Ticker string `yaml:"ticker"`
	Name   string `yaml:"name"`
	Issuer string `yaml:"issuer,omitempty"` // set only for non-Betashares funds
	// DriftExempt suppresses the "possibly delisted" drift warning for funds
	// that legitimately never appear on the Betashares fund index (a different
	// issuer, or an unlisted vehicle). Without this they warn every month, which
	// is how a drift alert gets trained into background noise.
	DriftExempt bool   `yaml:"drift_exempt,omitempty"`
	Group       string `yaml:"-"`
}

// Universe is the grouped, checked-in fund list.
type Universe struct {
	Standard []Fund
	Geared   []Fund
	Inverse  []Fund
	FX       []Fund
	Excluded []Fund
}

// Priced returns every fund the monitor fetches prices for: the ranked groups.
// Cash and bond funds are never fetched — momentum on a cash ETF is noise.
func (u Universe) Priced() []Fund {
	out := make([]Fund, 0, len(u.Standard)+len(u.Geared)+len(u.Inverse)+len(u.FX))
	out = append(out, u.Standard...)
	out = append(out, u.Geared...)
	out = append(out, u.Inverse...)
	out = append(out, u.FX...)
	return out
}

// All returns every fund including the excluded cash/bond group (used by the
// drift check, which must not report known cash funds as "new" each month).
func (u Universe) All() []Fund {
	return append(u.Priced(), u.Excluded...)
}

// Lookup indexes the whole universe by ticker.
func (u Universe) Lookup() map[string]Fund {
	out := make(map[string]Fund)
	for _, f := range u.All() {
		out[f.Ticker] = f
	}
	return out
}

type universeFile struct {
	Universe struct {
		Standard []Fund `yaml:"standard"`
		Geared   []Fund `yaml:"geared"`
		Inverse  []Fund `yaml:"inverse"`
		FX       []Fund `yaml:"fx"`
		Excluded []Fund `yaml:"excluded"`
	} `yaml:"universe"`
}

// LoadUniverse reads and validates the checked-in grouped universe.
func LoadUniverse(path string) (Universe, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Universe{}, fmt.Errorf("universe: %w", err)
	}
	var f universeFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return Universe{}, fmt.Errorf("universe parse: %w", err)
	}
	u := Universe{
		Standard: tag(f.Universe.Standard, GroupStandard),
		Geared:   tag(f.Universe.Geared, GroupGeared),
		Inverse:  tag(f.Universe.Inverse, GroupInverse),
		FX:       tag(f.Universe.FX, GroupFX),
		Excluded: tag(f.Universe.Excluded, GroupExcluded),
	}
	seen := map[string]string{}
	for _, fund := range u.All() {
		if fund.Ticker == "" {
			return Universe{}, fmt.Errorf("universe: entry with empty ticker in group %q", fund.Group)
		}
		if prev, dup := seen[fund.Ticker]; dup {
			return Universe{}, fmt.Errorf("universe: %s listed in both %s and %s", fund.Ticker, prev, fund.Group)
		}
		seen[fund.Ticker] = fund.Group
	}
	if len(u.Standard) < 20 {
		return Universe{}, fmt.Errorf("universe: only %d standard funds — file looks truncated", len(u.Standard))
	}
	return u, nil
}

func tag(in []Fund, group string) []Fund {
	out := make([]Fund, 0, len(in))
	for _, f := range in {
		f.Ticker = strings.ToUpper(strings.TrimSpace(f.Ticker))
		f.Group = group
		out = append(out, f)
	}
	return out
}
