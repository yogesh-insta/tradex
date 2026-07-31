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
