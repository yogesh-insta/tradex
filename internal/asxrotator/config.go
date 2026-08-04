// Package asxrotator implements the monthly, advisory-only S&P/ASX 200
// momentum rotation lane. It never places orders; it ranks, diffs against a
// user-maintained portfolio, and reports via Telegram.
//
// READ FIRST: docs/specs/22-asx-momentum-rotator.md. Same rule set as the NSE
// lane (enter the top 10 by 6-month momentum, hold anything still in the top
// 30 on either the 6m or the 12m list, stay invested through downtrends), with
// three ASX-specific departures that the spec explains in full:
//
//  1. MinPriceAUD — mandatory. Yahoo's back-adjustment scales some ASX
//     histories to ~zero (TAH.AX reads A$0.0000 for 103 months), which the
//     jump screens cannot catch because the corrupted series is smooth.
//  2. Survivorship — the backtest universe is today's index membership pulled
//     back 15 years; roughly +6%/yr of the headline result is that bias.
//  3. Turnover vs the 50% CGT discount and franking credits.
package asxrotator

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the standalone YAML schema for cmd/asxrotator. Secrets appear
// only as ${ENV_VAR} references, resolved at load (repo standard).
type Config struct {
	Env string `yaml:"env"`

	Rotator struct {
		LookbackMonths int    `yaml:"lookback_months"`
		TopK           int    `yaml:"top_k"`
		RegimeEMADays  int    `yaml:"regime_ema_days"`
		UniverseFile   string `yaml:"universe_file"`
		HolidaysFile   string `yaml:"holidays_file"`
		Market         string `yaml:"market"` // key into holidays file, e.g. XASX
		// GCSPrefix like gs://bucket/asxrotator — portfolio.json, heartbeat.json
		// and recommendation-YYYY-MM.json live under it. Empty = local files
		// under LocalStateDir (dev).
		GCSPrefix     string `yaml:"gcs_prefix"`
		LocalStateDir string `yaml:"local_state_dir"`
		YahooTimeoutS int    `yaml:"yahoo_timeout_s"`
		DriftCheck    bool   `yaml:"drift_check"`
		// ConstituentsURL is a CSV of current index holdings used for the drift
		// check. Unlike NSE, S&P publishes ASX 200 membership behind a login,
		// so there is no official free endpoint; an index-ETF holdings CSV is
		// the practical substitute. Empty with DriftCheck on raises a warning
		// on every run rather than silently never checking.
		ConstituentsURL string `yaml:"constituents_url"`
		// ExcludedSymbols are never ranked or bought (policy blocklist). Held
		// names still appear in SELL orders when not in the target portfolio.
		ExcludedSymbols []string `yaml:"excluded_symbols"`
		// FrozenSymbols are held but untradeable — suspended, illiquid, or
		// otherwise stuck. Never ranked, bought, or sold; never consume a slot;
		// never raise the stale-price warning. Distinct from ExcludedSymbols,
		// which yields a SELL every month — useless advice for a position that
		// cannot be exited.
		FrozenSymbols []string `yaml:"frozen_symbols"`
		// ExitLookbackMonths is the slower momentum list that keeps a holding
		// alive: a name is sold only when it sits outside the top ExitRankN on
		// BOTH the LookbackMonths and ExitLookbackMonths lists.
		ExitLookbackMonths int `yaml:"exit_lookback_months"`
		// ExitRankN is the exit rank buffer; must be >= TopK. Defaults to
		// 3*TopK. Setting it equal to TopK restores plain top-K rotation.
		ExitRankN int `yaml:"exit_rank_n"`
		// MinPriceAUD drops symbols whose last close is below this from BOTH
		// momentum lists — entry and the keep set alike.
		//
		// This has no NSE equivalent and is not optional. Yahoo back-adjusts
		// for demergers by scaling the whole prior history down, which for
		// TAH.AX (Echo 2011, Lottery Corp 2022) yields A$0.0000 for 103
		// consecutive months. Momentum off a near-zero base reads ~+1800% and
		// ranks first every month. The BadJumpUp/LargeDownJump screens do not
		// fire: the corrupted series is smooth, merely scaled. Only an absolute
		// floor catches it. Unguarded, the 15y backtest reads 41-51% CAGR of
		// pure artifact against 25.0% with the floor on.
		//
		// It doubles as a penny-stock/liquidity screen, which is why it is
		// configurable rather than hardcoded. Defaults to 1.00.
		MinPriceAUD float64 `yaml:"min_price_aud"`
		// RegimeFilter gates the whole book to cash while ^AXJO trades below
		// its EMA(RegimeEMADays). Pointer so an absent key still means ON — a
		// plain bool would silently disable the filter for every existing
		// config. Ships false; see spec 22 § Regime filter for the measurement.
		RegimeFilter *bool `yaml:"regime_filter"`
	} `yaml:"asxrotator"`

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
		return nil, fmt.Errorf("asxrotator config: %w", err)
	}
	resolved := envRef.ReplaceAllStringFunc(string(raw), func(m string) string {
		return os.Getenv(envRef.FindStringSubmatch(m)[1])
	})
	var c Config
	if err := yaml.Unmarshal([]byte(resolved), &c); err != nil {
		return nil, fmt.Errorf("asxrotator config parse: %w", err)
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) validate() error {
	r := &c.Rotator
	if r.LookbackMonths <= 0 {
		r.LookbackMonths = 6
	}
	if r.TopK <= 0 {
		r.TopK = 10
	}
	if r.RegimeEMADays <= 0 {
		r.RegimeEMADays = 200
	}
	if r.ExitLookbackMonths <= 0 {
		r.ExitLookbackMonths = 12
	}
	if r.ExitRankN <= 0 {
		r.ExitRankN = 3 * r.TopK
	}
	if r.ExitRankN < r.TopK {
		return fmt.Errorf("asxrotator config: exit_rank_n (%d) must be >= top_k (%d)", r.ExitRankN, r.TopK)
	}
	// A negative floor is a config error, not something to clamp silently: it
	// almost certainly means the operator meant to disable the screen and got
	// the sign wrong, and disabling it is exactly the failure this guards.
	if r.MinPriceAUD < 0 {
		return fmt.Errorf("asxrotator config: min_price_aud (%v) must be >= 0", r.MinPriceAUD)
	}
	if r.MinPriceAUD == 0 {
		r.MinPriceAUD = DefaultMinPriceAUD
	}
	if r.RegimeFilter == nil {
		on := true // absent key = filter ON, matching the NSE lane
		r.RegimeFilter = &on
	}
	if r.YahooTimeoutS <= 0 {
		r.YahooTimeoutS = 30
	}
	if r.Market == "" {
		r.Market = "XASX"
	}
	if r.UniverseFile == "" {
		return fmt.Errorf("asxrotator config: universe_file required")
	}
	if r.HolidaysFile == "" {
		return fmt.Errorf("asxrotator config: holidays_file required")
	}
	if r.GCSPrefix == "" && r.LocalStateDir == "" {
		return fmt.Errorf("asxrotator config: one of gcs_prefix or local_state_dir required")
	}
	if r.GCSPrefix != "" && !strings.HasPrefix(r.GCSPrefix, "gs://") {
		return fmt.Errorf("asxrotator config: gcs_prefix must start with gs://")
	}
	if c.Env == "" {
		c.Env = "prod"
	}
	return nil
}

// UniverseFile is the schema of config/universe-asx200.yaml.
type UniverseFile struct {
	Universe []string `yaml:"universe"`
}

// LoadUniverse reads and validates the checked-in universe list.
func LoadUniverse(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("universe: %w", err)
	}
	var f UniverseFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("universe parse: %w", err)
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range f.Universe {
		s = strings.ToUpper(strings.TrimSpace(s))
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	if len(out) < 50 {
		return nil, fmt.Errorf("universe: only %d symbols — file looks truncated", len(out))
	}
	return out, nil
}
