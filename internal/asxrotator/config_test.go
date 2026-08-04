package asxrotator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const minimalCfg = `
env: dev
asxrotator:
  universe_file: config/universe-asx200.yaml
  holidays_file: config/holidays-asx.yaml
  local_state_dir: /tmp/asx
`

func TestConfigDefaults(t *testing.T) {
	c, err := LoadConfig(writeCfg(t, minimalCfg))
	if err != nil {
		t.Fatal(err)
	}
	r := c.Rotator
	if r.LookbackMonths != 6 || r.ExitLookbackMonths != 12 {
		t.Errorf("lookbacks = %d/%d, want 6/12", r.LookbackMonths, r.ExitLookbackMonths)
	}
	if r.TopK != 10 || r.ExitRankN != 30 {
		t.Errorf("topK/exitN = %d/%d, want 10/30", r.TopK, r.ExitRankN)
	}
	if r.MinPriceAUD != DefaultMinPriceAUD {
		t.Errorf("min_price_aud = %v, want %v", r.MinPriceAUD, DefaultMinPriceAUD)
	}
	if r.Market != "XASX" {
		t.Errorf("market = %q, want XASX", r.Market)
	}
	// An absent regime_filter key must mean ON, never silently off.
	if r.RegimeFilter == nil || !*r.RegimeFilter {
		t.Error("absent regime_filter must default to ON")
	}
}

// A negative floor is rejected rather than clamped: it almost certainly means
// the operator meant to disable the screen and got the sign wrong, and
// disabling it is exactly the failure the floor guards against.
func TestConfigRejectsNegativeMinPrice(t *testing.T) {
	_, err := LoadConfig(writeCfg(t, minimalCfg+"  min_price_aud: -1\n"))
	if err == nil {
		t.Fatal("expected an error for negative min_price_aud")
	}
	if !strings.Contains(err.Error(), "min_price_aud") {
		t.Errorf("error should name the field: %v", err)
	}
}

func TestConfigRejectsExitRankBelowTopK(t *testing.T) {
	_, err := LoadConfig(writeCfg(t, minimalCfg+"  top_k: 10\n  exit_rank_n: 5\n"))
	if err == nil {
		t.Fatal("expected an error for exit_rank_n < top_k")
	}
}

func TestConfigRequiresStateLocation(t *testing.T) {
	_, err := LoadConfig(writeCfg(t, `
env: dev
asxrotator:
  universe_file: u.yaml
  holidays_file: h.yaml
`))
	if err == nil {
		t.Fatal("expected an error when neither gcs_prefix nor local_state_dir is set")
	}
}

// The shipped configs must actually load, and must agree with each other on
// every parameter that changes what the strategy advises — a dev run that
// differs from prod is worse than no dev run.
func TestShippedConfigsLoadAndAgree(t *testing.T) {
	dev, err := LoadConfig("../../config/config.asxrotator.dev.yaml")
	if err != nil {
		t.Fatalf("dev config: %v", err)
	}
	prod, err := LoadConfig("../../config/config.asxrotator.cloudrun.yaml")
	if err != nil {
		t.Fatalf("cloudrun config: %v", err)
	}
	d, p := dev.Rotator, prod.Rotator
	if d.LookbackMonths != p.LookbackMonths || d.ExitLookbackMonths != p.ExitLookbackMonths ||
		d.TopK != p.TopK || d.ExitRankN != p.ExitRankN ||
		d.MinPriceAUD != p.MinPriceAUD || *d.RegimeFilter != *p.RegimeFilter {
		t.Errorf("dev and cloudrun strategy params diverge:\n dev  %+v\n prod %+v", d, p)
	}
	if p.MinPriceAUD <= 0 {
		t.Error("shipped config must keep the price floor on")
	}
}

func TestLoadUniverseShipped(t *testing.T) {
	u, err := LoadUniverse("../../config/universe-asx200.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(u) < 150 {
		t.Errorf("universe has %d symbols, expected the ASX 200", len(u))
	}
	seen := map[string]bool{}
	for _, s := range u {
		if seen[s] {
			t.Errorf("duplicate symbol %q", s)
		}
		seen[s] = true
		if s != strings.ToUpper(s) || strings.ContainsAny(s, " .") {
			t.Errorf("symbol %q should be a bare uppercase ticker (no .AX suffix)", s)
		}
	}
}
