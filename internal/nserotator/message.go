package nserotator

import (
	"fmt"
	"strings"
)

// FormatMessage renders the Telegram text for one recommendation (spec 20).
// Plain text (no Markdown parse mode) so symbols like M&M never break parsing.
func FormatMessage(rec Recommendation, lookbackMonths, emaDays int) string {
	var b strings.Builder
	// With the filter off the index reading is advisory only — it must not say
	// "exit all positions" directly above a list of BUY orders.
	regime := "CASH — exit all positions"
	switch {
	case rec.RegimeInvested:
		regime = "INVESTED"
	case rec.RegimeFilter != nil && !*rec.RegimeFilter:
		regime = "BELOW EMA — staying invested (regime filter off)"
	}
	fmt.Fprintf(&b, "NSE ROTATOR — %s\n", rec.Month)
	fmt.Fprintf(&b, "Regime: %s (Nifty %.0f vs EMA%d %.0f)\n", regime, rec.NiftyClose, emaDays, rec.NiftyEMA)

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

	// A HOLD can now sit well below the entry rank, so show which list saved it.
	if len(rec.HoldsInfo) > 0 {
		fmt.Fprintf(&b, "\nHOLD (kept while inside top %d on either list):\n", rec.Params.ExitRankN)
		for _, h := range rec.HoldsInfo {
			fmt.Fprintf(&b, "  %-12s %s · %s\n", h.Symbol,
				formatRank(h.Rank, rec.Params.LookbackMonths),
				formatRank(h.RankSlow, rec.Params.ExitLookbackMonths))
		}
	} else if len(rec.Holds) > 0 {
		fmt.Fprintf(&b, "\nHOLD: %s\n", strings.Join(rec.Holds, ", "))
	}

	if len(rec.Frozen) > 0 {
		fmt.Fprintf(&b, "\nFROZEN (untradeable — outside the strategy): %s\n",
			strings.Join(rec.Frozen, ", "))
	}

	if len(rec.TopRanked) > 0 {
		if rec.Params.ExitLookbackMonths > 0 {
			fmt.Fprintf(&b, "\nTop momentum (%dm entry ★ / hold to rank %d):\n",
				lookbackMonths, rec.Params.ExitRankN)
		} else {
			fmt.Fprintf(&b, "\nTop momentum (%dm):\n", lookbackMonths)
		}
		for i, r := range rec.TopRanked {
			marker := "  "
			if i < rec.Params.TopK {
				marker = "★ "
			}
			slow := ""
			if r.RankSlow > 0 {
				slow = fmt.Sprintf(" (12m %+.0f%%, #%d)", r.MomentumSlow*100, r.RankSlow)
			}
			fmt.Fprintf(&b, "%s%2d. %s · %s · %+.0f%%%s · %s\n",
				marker, i+1, formatRankedLabel(r), formatStockPriceINR(r.LastClose),
				r.Momentum*100, slow, formatMarketCapINR(r.MarketCap))
		}
	}

	if len(rec.Excluded) > 0 {
		fmt.Fprintf(&b, "\nExcluded: %s\n", strings.Join(rec.Excluded, "; "))
	}
	b.WriteString("\nAdvisory only — you place all orders. Update portfolio.json after executing.")
	return b.String()
}

// formatRank renders a 1-based list position; 0 means the symbol is not on
// that list at all (too little history, or ranked below everything shown).
func formatRank(rank, months int) string {
	if rank <= 0 {
		return fmt.Sprintf("%dm —", months)
	}
	return fmt.Sprintf("%dm #%d", months, rank)
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
