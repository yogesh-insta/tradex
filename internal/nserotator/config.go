// Package nserotator implements the monthly, advisory-only NSE momentum
// rotation lane (docs/specs/20-nse-momentum-rotator.md). It never places
// orders; it ranks, diffs against a user-maintained portfolio, and reports
// via Telegram. Deliberately self-contained: it does not touch the OANDA
// trading config or hot path.
package nserotator

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the standalone YAML schema for cmd/nserotator. Secrets appear
// only as ${ENV_VAR} references, resolved at load (repo standard).
type Config struct {
	Env string `yaml:"env"`

	Rotator struct {
		LookbackMonths int    `yaml:"lookback_months"`
		TopK           int    `yaml:"top_k"`
		RegimeEMADays  int    `yaml:"regime_ema_days"`
		UniverseFile   string `yaml:"universe_file"`
		HolidaysFile   string `yaml:"holidays_file"`
		Market         string `yaml:"market"` // key into holidays file, e.g. XNSE
		// GCSPrefix like gs://bucket/nserotator — portfolio.json, heartbeat.json
		// and recommendation-YYYY-MM.json live under it. Empty = local files
		// under LocalStateDir (dev).
		GCSPrefix     string `yaml:"gcs_prefix"`
		LocalStateDir string `yaml:"local_state_dir"`
		YahooTimeoutS int    `yaml:"yahoo_timeout_s"`
		DriftCheck    bool   `yaml:"drift_check"`
	} `yaml:"nserotator"`

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
		return nil, fmt.Errorf("nserotator config: %w", err)
	}
	resolved := envRef.ReplaceAllStringFunc(string(raw), func(m string) string {
		key := envRef.FindStringSubmatch(m)[1]
		return os.Getenv(key)
	})
	var cfg Config
	if err := yaml.Unmarshal([]byte(resolved), &cfg); err != nil {
		return nil, fmt.Errorf("nserotator config parse: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) validate() error {
	r := &c.Rotator
	if r.LookbackMonths <= 0 {
		r.LookbackMonths = 6
	}
	if r.TopK <= 0 {
		r.TopK = 8
	}
	if r.RegimeEMADays <= 0 {
		r.RegimeEMADays = 200
	}
	if r.YahooTimeoutS <= 0 {
		r.YahooTimeoutS = 30
	}
	if r.Market == "" {
		r.Market = "XNSE"
	}
	if r.UniverseFile == "" {
		return fmt.Errorf("nserotator config: universe_file required")
	}
	if r.HolidaysFile == "" {
		return fmt.Errorf("nserotator config: holidays_file required")
	}
	if r.GCSPrefix == "" && r.LocalStateDir == "" {
		return fmt.Errorf("nserotator config: one of gcs_prefix or local_state_dir required")
	}
	if r.GCSPrefix != "" && !strings.HasPrefix(r.GCSPrefix, "gs://") {
		return fmt.Errorf("nserotator config: gcs_prefix must start with gs://")
	}
	if c.Env == "" {
		c.Env = "prod"
	}
	return nil
}

// UniverseFile is the schema of config/universe-nse200.yaml.
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
		s = strings.TrimSpace(s)
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
