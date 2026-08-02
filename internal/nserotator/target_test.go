package nserotator

import (
	"strings"
	"testing"
)

// rankedFrom builds a descending ranked list from symbols already in rank order.
func rankedFrom(syms ...string) []Ranked {
	out := make([]Ranked, 0, len(syms))
	for i, s := range syms {
		out = append(out, Ranked{Symbol: s, Momentum: float64(len(syms) - i)})
	}
	return out
}

func holdingsOf(syms ...string) []Holding {
	out := make([]Holding, 0, len(syms))
	for _, s := range syms {
		out = append(out, Holding{Symbol: s, Qty: 1, AvgPrice: 100})
	}
	return out
}

func TestBuildTarget(t *testing.T) {
	// fast: A..H by 6m rank. slow: different order, so the two lists disagree.
	fast := rankedFrom("A", "B", "C", "D", "E", "F", "G", "H")
	slow := rankedFrom("G", "H", "A", "B", "C", "D", "E", "F")

	tests := []struct {
		name     string
		fast     []Ranked
		slow     []Ranked
		holdings []Holding
		topK     int
		exitN    int
		want     []string
	}{
		{
			name: "no holdings is plain top-K",
			fast: fast, slow: slow, holdings: nil, topK: 3, exitN: 6,
			want: []string{"A", "B", "C"},
		},
		{
			name: "exitN == topK reproduces plain rotation",
			fast: fast, slow: slow, holdings: holdingsOf("D", "E"), topK: 3, exitN: 3,
			// D and E are outside the top 3 on both lists — sold, not kept.
			want: []string{"A", "B", "C"},
		},
		{
			name: "holding inside the buffer on the fast list is kept",
			fast: fast, slow: slow, holdings: holdingsOf("E"), topK: 3, exitN: 6,
			// E is 6m rank 5 — outside topK but inside the buffer, so it keeps
			// its slot and only two new names are bought.
			want: []string{"E", "A", "B"},
		},
		{
			name: "holding kept by the slow list alone",
			fast: fast, slow: slow, holdings: holdingsOf("H"), topK: 3, exitN: 3,
			// H is 6m rank 8 (out) but 12m rank 2 (in) — the core case.
			want: []string{"H", "A", "B"},
		},
		{
			name: "holding outside the buffer on both lists is dropped",
			fast: fast, slow: slow, holdings: holdingsOf("F"), topK: 3, exitN: 4,
			// F is 6m rank 6 and 12m rank 8 — outside 4 on both.
			want: []string{"A", "B", "C"},
		},
		{
			name: "holding absent from both lists is dropped",
			fast: fast, slow: slow, holdings: holdingsOf("ZZZ"), topK: 3, exitN: 6,
			want: []string{"A", "B", "C"},
		},
		{
			name: "survivors never push the target past topK",
			fast: fast, slow: slow, holdings: holdingsOf("C", "D", "E", "F"), topK: 3, exitN: 8,
			want: []string{"C", "D", "E"},
		},
		{
			name: "survivor already in the buy list is not duplicated",
			fast: fast, slow: slow, holdings: holdingsOf("B"), topK: 3, exitN: 6,
			want: []string{"B", "A", "C"},
		},
		{
			name: "empty slow list degrades to fast-only hysteresis",
			fast: fast, slow: nil, holdings: holdingsOf("E"), topK: 3, exitN: 6,
			want: []string{"E", "A", "B"},
		},
		{
			name: "topK zero yields no target",
			fast: fast, slow: slow, holdings: holdingsOf("A"), topK: 0, exitN: 6,
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildTarget(tc.fast, tc.slow, tc.holdings, tc.topK, tc.exitN)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("BuildTarget = %v, want %v", got, tc.want)
			}
			if tc.topK > 0 && len(got) > tc.topK {
				t.Errorf("target length %d exceeds topK %d", len(got), tc.topK)
			}
		})
	}
}

// exitN below topK would sell a name the same run just bought; clamp instead.
func TestBuildTargetClampsExitNBelowTopK(t *testing.T) {
	fast := rankedFrom("A", "B", "C", "D")
	got := BuildTarget(fast, nil, holdingsOf("C"), 3, 1)
	want := []string{"C", "A", "B"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("BuildTarget = %v, want %v", got, want)
	}
}

func TestRankIndexIsOneBased(t *testing.T) {
	idx := RankIndex(rankedFrom("A", "B", "C"))
	if idx["A"] != 1 || idx["C"] != 3 {
		t.Errorf("RankIndex = %v, want A=1 C=3", idx)
	}
	if _, ok := idx["ZZZ"]; ok {
		t.Error("unranked symbol should be absent (zero value means unranked)")
	}
}

// A seeded book with more qualifying names than slots must shed the weakest by
// rank, not the ones last in portfolio.json. Pinned to the real 2026-08 run,
// where file order sold RADICO (6m rank 5) to keep NATIONALUM (6m rank 149).
func TestBuildTargetCutsOverflowByRankNotFileOrder(t *testing.T) {
	fast := rankedFrom("LAURUSLABS", "POWERINDIA", "f3", "BHEL", "RADICO",
		"BHARATFORG", "f7", "CGPOWER", "PREMIERENE", "f10", "NYKAA", "f12",
		"CUMMINSIND", "f14", "f15", "f16", "f17", "f18", "f19", "f20", "f21",
		"f22", "f23", "ENRIN", "f25", "f26", "f27", "f28", "f29", "f30")
	slow := rankedFrom("LAURUSLABS", "NATIONALUM", "BHARATFORG", "s4", "s5",
		"MCX", "BHEL", "SHRIRAMFIN", "POWERINDIA", "RADICO", "NYKAA", "s12",
		"s13", "CUMMINSIND", "s15", "s16", "s17", "s18", "s19", "s20", "s21",
		"s22", "s23", "s24", "s25", "s26", "s27", "s28", "s29", "s30")
	// portfolio.json order: the three sold by the old rule were simply last.
	holdings := holdingsOf("LAURUSLABS", "BHEL", "NATIONALUM", "MCX",
		"BHARATFORG", "SHRIRAMFIN", "CGPOWER", "PREMIERENE", "POWERINDIA",
		"CUMMINSIND", "NYKAA", "RADICO", "ENRIN")

	got := BuildTarget(fast, slow, holdings, 10, 30)
	if len(got) != 10 {
		t.Fatalf("target size = %d, want 10: %v", len(got), got)
	}
	inTarget := map[string]bool{}
	for _, s := range got {
		inTarget[s] = true
	}
	// RADICO (best rank 5) is kept despite sitting second-to-last in the file;
	// NATIONALUM is kept on its 12m rank of 2, not on its file position of 3.
	for _, s := range []string{"RADICO", "NATIONALUM"} {
		if !inTarget[s] {
			t.Errorf("%s should have been kept on rank: %v", s, got)
		}
	}
	// The three weakest qualifiers go: NYKAA 11/11, CUMMINSIND 13/14, ENRIN 24/-.
	for _, s := range []string{"NYKAA", "CUMMINSIND", "ENRIN"} {
		if inTarget[s] {
			t.Errorf("%s should have been cut as a weakest qualifier: %v", s, got)
		}
	}
	// Nothing inside the entry list may be cut, or the next run would rebuy it.
	for i, r := range fast {
		if i >= 10 {
			break
		}
		for _, h := range holdings {
			if h.Symbol == r.Symbol && !inTarget[r.Symbol] {
				t.Errorf("%s is held and inside the top-10 entry list but was cut: %v", r.Symbol, got)
			}
		}
	}
	// Reordering the same names must not change the outcome.
	shuffled := holdingsOf("RADICO", "NYKAA", "ENRIN", "LAURUSLABS", "BHEL",
		"NATIONALUM", "MCX", "BHARATFORG", "SHRIRAMFIN", "CGPOWER",
		"PREMIERENE", "POWERINDIA", "CUMMINSIND")
	got2 := BuildTarget(fast, slow, shuffled, 10, 30)
	set := func(ss []string) map[string]bool {
		m := map[string]bool{}
		for _, s := range ss {
			m[s] = true
		}
		return m
	}
	a, b := set(got), set(got2)
	for s := range a {
		if !b[s] {
			t.Errorf("file order still changes membership: %v vs %v", got, got2)
			break
		}
	}
}
