package utils

import (
	"math"
	"strconv"
)

// RoundToPrecision rounds v to n decimal places (half away from zero).
func RoundToPrecision(v float64, n int) float64 {
	p := math.Pow10(n)
	return math.Round(v*p) / p
}

// FormatPrice renders a price with exactly n decimal places, as OANDA expects.
func FormatPrice(v float64, n int) string {
	return strconv.FormatFloat(RoundToPrecision(v, n), 'f', n, 64)
}
