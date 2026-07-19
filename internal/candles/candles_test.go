package candles

import (
	"testing"
	"time"

	"github.com/yogesh-insta/tradex/pkg/types"
)

func TestAlignStart(t *testing.T) {
	tests := []struct {
		name string
		in   string
		tf   types.Timeframe
		want string
	}{
		{"M5 mid-candle", "2026-07-15T07:32:41Z", types.M5, "2026-07-15T07:30:00Z"},
		{"M5 exact boundary", "2026-07-15T07:35:00Z", types.M5, "2026-07-15T07:35:00Z"},
		{"M5 just before boundary", "2026-07-15T07:34:59Z", types.M5, "2026-07-15T07:30:00Z"},
		{"H1 mid-hour", "2026-07-15T07:59:59Z", types.H1, "2026-07-15T07:00:00Z"},
		{"H1 top of hour", "2026-07-15T08:00:00Z", types.H1, "2026-07-15T08:00:00Z"},
		{"M5 DST spring-forward day (Berlin 2026-03-29)", "2026-03-29T01:02:00Z", types.M5, "2026-03-29T01:00:00Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, _ := time.Parse(time.RFC3339, tt.in)
			want, _ := time.Parse(time.RFC3339, tt.want)
			if got := AlignStart(in, tt.tf); !got.Equal(want) {
				t.Fatalf("AlignStart(%s, %s) = %s, want %s", tt.in, tt.tf, got, want)
			}
		})
	}
}

func TestFoldTick(t *testing.T) {
	c := types.Candle{Instrument: "DE30_EUR", Timeframe: types.M5}
	FoldTick(&c, types.Tick{Mid: 100})
	FoldTick(&c, types.Tick{Mid: 103})
	FoldTick(&c, types.Tick{Mid: 99})
	FoldTick(&c, types.Tick{Mid: 101})
	if c.Open != 100 || c.High != 103 || c.Low != 99 || c.Close != 101 {
		t.Fatalf("OHLC = %v/%v/%v/%v, want 100/103/99/101", c.Open, c.High, c.Low, c.Close)
	}
	if c.Volume != 4 {
		t.Fatalf("volume = %d, want 4", c.Volume)
	}
}

func TestRingUpsert(t *testing.T) {
	mk := func(startMin int, close float64) types.Candle {
		return types.Candle{
			Instrument: "DE30_EUR", Timeframe: types.M5,
			Start: time.Date(2026, 7, 15, 8, startMin, 0, 0, time.UTC),
			Close: close, Complete: true,
		}
	}

	t.Run("keeps order and caps at max", func(t *testing.T) {
		r := NewRing(3)
		for i, m := range []int{0, 5, 10, 15} {
			r.Upsert(mk(m, float64(i)))
		}
		w := r.Window()
		if len(w) != 3 {
			t.Fatalf("len = %d, want 3", len(w))
		}
		if w[0].Start.Minute() != 5 || w[2].Start.Minute() != 15 {
			t.Fatalf("window boundaries wrong: %v .. %v", w[0].Start, w[2].Start)
		}
	})

	t.Run("replace by start is idempotent (REST reconcile)", func(t *testing.T) {
		r := NewRing(5)
		r.Upsert(mk(0, 1))
		r.Upsert(mk(5, 2))
		r.Upsert(mk(5, 99)) // authoritative REST version replaces
		r.Upsert(mk(5, 99)) // duplicate reconcile is a no-op
		w := r.Window()
		if len(w) != 2 {
			t.Fatalf("len = %d, want 2", len(w))
		}
		if w[1].Close != 99 {
			t.Fatalf("close = %v, want 99 (replaced)", w[1].Close)
		}
	})

	t.Run("out-of-order insert lands sorted", func(t *testing.T) {
		r := NewRing(5)
		r.Upsert(mk(10, 3))
		r.Upsert(mk(0, 1)) // late backfill
		r.Upsert(mk(5, 2))
		w := r.Window()
		for i, wantMin := range []int{0, 5, 10} {
			if w[i].Start.Minute() != wantMin {
				t.Fatalf("w[%d].Start.Minute() = %d, want %d", i, w[i].Start.Minute(), wantMin)
			}
		}
	})

	t.Run("last", func(t *testing.T) {
		r := NewRing(5)
		if _, ok := r.Last(); ok {
			t.Fatal("empty ring should have no last")
		}
		r.Upsert(mk(0, 1))
		r.Upsert(mk(5, 2))
		last, ok := r.Last()
		if !ok || last.Close != 2 {
			t.Fatalf("last = %+v, ok=%v", last, ok)
		}
	})
}
