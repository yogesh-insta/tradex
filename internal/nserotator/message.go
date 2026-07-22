package nserotator

import (
	"fmt"
	"strings"
)

// FormatMessage renders the Telegram text for one recommendation (spec 20).
// Plain text (no Markdown parse mode) so symbols like M&M never break parsing.
func FormatMessage(rec Recommendation, topK, lookbackMonths int) string {
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
				fmt.Fprintf(&b, "  SELL %s · qty %-6d @ %s (~₹%s) · %s\n",
					formatOrderLabel(o), o.Qty, formatStockPriceINR(o.LastClose), inr(o.ApproxValue), formatMarketCapINR(o.MarketCap))
			}
		}
		for _, o := range rec.Orders {
			if o.Side == "BUY" {
				fmt.Fprintf(&b, "  BUY  %s · qty %-6d @ %s (~₹%s) · %s\n",
					formatOrderLabel(o), o.Qty, formatStockPriceINR(o.LastClose), inr(o.ApproxValue), formatMarketCapINR(o.MarketCap))
			}
		}
	}

	if len(rec.Holds) > 0 {
		fmt.Fprintf(&b, "\nHOLD: %s\n", strings.Join(rec.Holds, ", "))
	}

	if len(rec.TopRanked) > 0 {
		fmt.Fprintf(&b, "\nTop momentum (%dm):\n", lookbackMonths)
		for _, r := range rec.TopRanked {
			fmt.Fprintf(&b, "  %s · %s · %+.0f%% · %s\n",
				formatRankedLabel(r), formatStockPriceINR(r.LastClose), r.Momentum*100, formatMarketCapINR(r.MarketCap))
		}
	}

	if len(rec.Excluded) > 0 {
		fmt.Fprintf(&b, "\nExcluded: %s\n", strings.Join(rec.Excluded, "; "))
	}
	b.WriteString("\nAdvisory only — you place all orders. Update portfolio.json after executing.")
	return b.String()
}

// formatMarketCapINR renders Yahoo market cap (INR) in Indian crore units.
func formatMarketCapINR(v float64) string {
	if v <= 0 {
		return "MCap n/a"
	}
	cr := v / 1e7 // 1 crore = 10 million INR
	switch {
	case cr >= 100000:
		return fmt.Sprintf("₹%.2fL Cr", cr/100000)
	case cr >= 1000:
		return fmt.Sprintf("₹%.1fK Cr", cr/1000)
	default:
		return fmt.Sprintf("₹%.0f Cr", cr)
	}
}

func formatRankedLabel(r Ranked) string {
	name := strings.TrimSpace(r.CompanyName)
	if name == "" {
		return r.Symbol
	}
	return r.Symbol + " — " + name
}

func formatOrderLabel(o Order) string {
	name := strings.TrimSpace(o.CompanyName)
	if name == "" {
		return o.Symbol
	}
	return o.Symbol + " — " + name
}

func formatStockPriceINR(v float64) string {
	if v <= 0 {
		return "price n/a"
	}
	if v >= 100 {
		return "₹" + inr(v)
	}
	return fmt.Sprintf("₹%.2f", v)
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
