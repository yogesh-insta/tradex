package nserotator

import (
	"fmt"
	"strings"
	"testing"
)

func TestNseSymbolListEmptyIsNil(t *testing.T) {
	if got := nseSymbolList(nil); got != nil {
		t.Fatalf("nil map: got %#v want nil", got)
	}
	if got := nseSymbolList(map[string]string{}); got != nil {
		t.Fatalf("empty map: got %#v want nil", got)
	}
	got := nseSymbolList(map[string]string{"INFY": "Infosys", "TCS": "TCS"})
	if len(got) != 2 || got[0] != "INFY" || got[1] != "TCS" {
		t.Fatalf("got %v", got)
	}
}

func TestOfficialListUsableRejectsShortList(t *testing.T) {
	if officialListUsable(nil) {
		t.Fatal("nil must be unusable")
	}
	if officialListUsable(make([]string, minOfficialUniverse-1)) {
		t.Fatal("short list must be unusable")
	}
	if !officialListUsable(make([]string, minOfficialUniverse)) {
		t.Fatal("Nifty-200-sized list must be usable")
	}
}

func TestDiffUniverseEmptyOfficialLooksLikeTotalDrop(t *testing.T) {
	// Documents the failure mode: an empty official slice vs a real universe
	// reports every name as a drop. Callers must not run this on empty input.
	universe := []string{"INFY", "TCS", "RELIANCE"}
	_, removed := DiffUniverse(universe, nil)
	if len(removed) != 3 {
		t.Fatalf("empty official removes everything: got %v", removed)
	}
}

func TestFormatSymbolSampleCapsDump(t *testing.T) {
	if got := formatSymbolSample(nil, 20); got != "none" {
		t.Fatalf("empty: %q", got)
	}
	if got := formatSymbolSample([]string{"A", "B"}, 20); got != "A, B" {
		t.Fatalf("short: %q", got)
	}
	names := make([]string, 200)
	for i := range names {
		names[i] = fmt.Sprintf("S%d", i)
	}
	got := formatSymbolSample(names, 20)
	if !strings.Contains(got, "(200 total)") {
		t.Fatalf("expected total count, got %q", got)
	}
	if len(got) > 500 {
		t.Fatalf("sample still too long (%d): %s", len(got), got)
	}
}
