package nserotator

import (
	"fmt"
	"strings"
)

// FormatMessage renders the Telegram text for one recommendation (spec 20).
// Plain text (no Markdown parse mode) so symbols like M&M never break parsing.
func FormatMessage(rec Recommendation, topK int) string {
	var b strings.Builder
	regime := "CASH — exit all positions"
	if rec.RegimeInvested {
		regime = "INVESTED"
	}
	fmt.Fprintf(&b, "NSE ROTATOR — %s\n", rec.Month)
	fmt.Fprintf(&b, "Regime: %s (Nifty %.0f vs EMA200 %.0f)\n", regime, rec.NiftyClose, rec.NiftyEMA)

	for _, w := range rec.Warnings {
		fmt.Fprintf(&b, "⚠ %s\n", w)
	}
	b.WriteString("\n")

	if len(rec.Orders) == 0 {
		b.WriteString("ORDERS: none — no changes this month.\n")
	} else {
		b.WriteString("ORDERS (execute at next open):\n")
		for _, o := range rec.Orders {
			if o.Side == "SELL" {
				fmt.Fprintf(&b, "  SELL %-12s qty %-6d (~₹%s)\n", o.Symbol, o.Qty, inr(o.ApproxValue))
			}
		}
		for _, o := range rec.Orders {
			if o.Side == "BUY" {
				fmt.Fprintf(&b, "  BUY  %-12s qty %-6d @ ~%.2f (~₹%s)\n", o.Symbol, o.Qty, o.LastClose, inr(o.ApproxValue))
			}
		}
	}

	if len(rec.Holds) > 0 {
		fmt.Fprintf(&b, "\nHOLD: %s\n", strings.Join(rec.Holds, ", "))
	}

	if len(rec.TopRanked) > 0 {
		b.WriteString("\nTop momentum (12m): ")
		parts := make([]string, 0, len(rec.TopRanked))
		for _, r := range rec.TopRanked {
			parts = append(parts, fmt.Sprintf("%s %+.0f%%", r.Symbol, r.Momentum*100))
		}
		b.WriteString(strings.Join(parts, " | "))
		b.WriteString("\n")
	}

	if len(rec.Excluded) > 0 {
		fmt.Fprintf(&b, "\nExcluded: %s\n", strings.Join(rec.Excluded, "; "))
	}
	b.WriteString("\nAdvisory only — you place all orders. Update portfolio.json after executing.")
	return b.String()
}

// inr formats a rupee amount with Indian digit grouping (12,34,567).
func inr(v float64) string {
	n := int64(v + 0.5)
	neg := n < 0
	if neg {
		n = -n
	}
	s := fmt.Sprintf("%d", n)
	if len(s) > 3 {
		head := s[:len(s)-3]
		tail := s[len(s)-3:]
		var parts []string
		for len(head) > 2 {
			parts = append([]string{head[len(head)-2:]}, parts...)
			head = head[:len(head)-2]
		}
		if head != "" {
			parts = append([]string{head}, parts...)
		}
		s = strings.Join(parts, ",") + "," + tail
	}
	if neg {
		return "-" + s
	}
	return s
}
