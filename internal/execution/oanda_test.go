package execution

import (
	"math"
	"testing"

	"github.com/yogesh-insta/tradex/internal/oanda"
)

func TestPointValueForAccount(t *testing.T) {
	tests := []struct {
		name      string
		meta      InstrumentMeta
		account   string
		available map[string]oanda.RESTInstrument
		mids      map[string]float64
		want      float64
	}{
		{
			name:    "EUR index in EUR account",
			meta:    InstrumentMeta{Symbol: "DE30_EUR", PipLocation: 0},
			account: "EUR",
			want:    1,
		},
		{
			name:    "USD JPY in USD account",
			meta:    InstrumentMeta{Symbol: "USD_JPY", PipLocation: -2},
			account: "USD",
			available: map[string]oanda.RESTInstrument{
				"USD_JPY": {Name: "USD_JPY"},
			},
			mids: map[string]float64{"USD_JPY": 150},
			want: 0.01 / 150,
		},
		{
			name:    "EUR quote converted directly to USD",
			meta:    InstrumentMeta{Symbol: "DE30_EUR", PipLocation: 0},
			account: "USD",
			available: map[string]oanda.RESTInstrument{
				"EUR_USD": {Name: "EUR_USD"},
			},
			mids: map[string]float64{"EUR_USD": 1.08},
			want: 1.08,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pointValueForAccount(tt.meta, tt.account, tt.available, func(instrument string) (float64, error) {
				return tt.mids[instrument], nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if math.Abs(got-tt.want) > 1e-12 {
				t.Fatalf("point value = %.12f, want %.12f", got, tt.want)
			}
		})
	}
}

func TestInitialRiskExtensionRoundTrip(t *testing.T) {
	for _, distance := range []float64{0.01, 60, 0.006250000000001} {
		comment := initialRiskComment(distance)
		got, ok := initialRiskFromExtensions(&oanda.ClientExtensions{Comment: comment})
		if !ok || got != distance {
			t.Fatalf("risk extension %q = (%v, %v), want (%v, true)", comment, got, ok, distance)
		}
	}
}

func TestInitialRiskExtensionRejectsInvalidValues(t *testing.T) {
	for _, comment := range []string{
		"",
		"other:initial-risk=60",
		initialRiskCommentPrefix + "0",
		initialRiskCommentPrefix + "-1",
		initialRiskCommentPrefix + "not-a-number",
	} {
		if _, ok := initialRiskFromExtensions(&oanda.ClientExtensions{Comment: comment}); ok {
			t.Fatalf("accepted invalid risk extension %q", comment)
		}
	}
}
