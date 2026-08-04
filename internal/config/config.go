// Package config loads the dashboard's YAML config, resolves ${ENV_VAR}
// secret references at load time, and fail-fast validates it.
//
// The NSE rotator and ASX ETF monitor each carry their own standalone config
// (internal/nserotator, internal/etfmonitor); this package serves the
// dashboard alone. Secret values are never logged.
package config

import (
	"fmt"
	"os"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration wraps time.Duration for YAML strings like "15s", "30m".
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	if s == "" {
		*d = 0
		return nil
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(dur)
	return nil
}

// D returns the underlying time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// SecretResolver resolves a secret reference name to its value. The v1
// implementation reads environment variables; a Secret Manager implementation
// can be swapped in later without touching consumers.
type SecretResolver interface {
	Resolve(ref string) (string, error)
}

// EnvResolver resolves ${NAME} references from process environment variables.
type EnvResolver struct{}

// Resolve returns the environment variable value, or an error if unset.
func (EnvResolver) Resolve(ref string) (string, error) {
	v, ok := os.LookupEnv(ref)
	if !ok {
		return "", fmt.Errorf("environment variable %s not set", ref)
	}
	return v, nil
}

// Config is the per-environment configuration tree.
type Config struct {
	Env       string          `yaml:"env"`
	Project   string          `yaml:"project"`
	Dashboard DashboardConfig `yaml:"dashboard"`
}

// DashboardConfig — docs/specs/14-dashboard.md. The dashboard is read-only and
// renders two report lanes (ASX ETF, NSE rotator) straight from GCS objects.
type DashboardConfig struct {
	ListenAddr string `yaml:"listen_addr"`
	// Mock enables explicit fixture mode for local demos (no GCS).
	Mock bool `yaml:"mock"`

	Auth DashboardAuthConfig `yaml:"auth"`

	CacheTTL DashboardCacheTTLConfig `yaml:"cache_ttl"`
	UI       DashboardUIConfig       `yaml:"ui"`
}

// DashboardAuthConfig selects bearer token or IAP identity gate.
type DashboardAuthConfig struct {
	Mode  string `yaml:"mode"`  // "bearer" | "iap"
	Token string `yaml:"token"` // ${DASHBOARD_TOKEN}; required for bearer
	// IAPAllowEmails is an optional allowlist when mode=iap (empty = any
	// authenticated IAP identity is accepted).
	IAPAllowEmails []string `yaml:"iap_allow_emails"`
}

// DashboardCacheTTLConfig bounds how long an aggregated response is reused.
type DashboardCacheTTLConfig struct {
	Overview Duration `yaml:"overview"`
}

// DashboardUIConfig controls the embedded page.
type DashboardUIConfig struct {
	RefreshInterval Duration `yaml:"refresh_interval"`
	ReportingTZ     string   `yaml:"reporting_tz"`
}

var envRefRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Load reads, env-resolves and default-fills the config at path.
func Load(path string, resolver SecretResolver) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	expanded := envRefRe.ReplaceAllStringFunc(string(raw), func(m string) string {
		name := envRefRe.FindStringSubmatch(m)[1]
		v, err := resolver.Resolve(name)
		if err != nil {
			// Leave unresolved refs empty; validation reports what is required.
			return ""
		}
		return v
	})
	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.applyDefaults()
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	d := &c.Dashboard
	if d.ListenAddr == "" {
		d.ListenAddr = ":8080"
	}
	if d.Auth.Mode == "" {
		d.Auth.Mode = "bearer"
	}
	if d.CacheTTL.Overview == 0 {
		d.CacheTTL.Overview = Duration(60 * time.Second)
	}
	if d.UI.RefreshInterval == 0 {
		d.UI.RefreshInterval = Duration(20 * time.Second)
	}
	if d.UI.ReportingTZ == "" {
		d.UI.ReportingTZ = "UTC"
	}
}
