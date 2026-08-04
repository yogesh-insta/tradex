package asxrotator

import (
	"fmt"
	"strings"
)

// FormatMessage renders the Telegram text for one recommendation (spec 22).
// Plain text (no Markdown parse mode) so symbols never break parsing.
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
	fmt.Fprintf(&b, "ASX ROTATOR — %s\n", rec.Month)
	fmt.Fprintf(&b, "Regime: %s (XJO %.0f vs EMA%d %.0f)\n", regime, rec.IndexClose, emaDays, rec.IndexEMA)

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
				fmt.Fprintf(&b, "  SELL %s · qty %-6d @ %s (~%s) · %s\n",
					formatOrderLabel(o), o.Qty, formatStockPriceAUD(o.LastClose),
					aud(o.ApproxValue), formatMarketCapAUD(o.MarketCap))
			}
		}
		for _, o := range rec.Orders {
			if o.Side == "BUY" {
				fmt.Fprintf(&b, "  BUY  %s · qty %-6d @ %s (~%s) · %s\n",
					formatOrderLabel(o), o.Qty, formatStockPriceAUD(o.LastClose),
					aud(o.ApproxValue), formatMarketCapAUD(o.MarketCap))
			}
		}
	}

	// A HOLD can sit well below the entry rank, so show which list saved it.
	if len(rec.HoldsInfo) > 0 {
		fmt.Fprintf(&b, "\nHOLD (kept while inside top %d on either list):\n", rec.Params.ExitRankN)
		for _, h := range rec.HoldsInfo {
			fmt.Fprintf(&b, "  %-10s %s · %s\n", h.Symbol,
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
				slow = fmt.Sprintf(" (%dm %+.0f%%, #%d)",
					rec.Params.ExitLookbackMonths, r.MomentumSlow*100, r.RankSlow)
			}
			fmt.Fprintf(&b, "%s%2d. %s · %s · %+.0f%%%s · %s\n",
				marker, i+1, formatRankedLabel(r), formatStockPriceAUD(r.LastClose),
				r.Momentum*100, slow, formatMarketCapAUD(r.MarketCap))
		}
	}

	// The price floor is a real filter that can remove a name the user holds;
	// reporting it keeps the screen visible instead of silently dropping names.
	if len(rec.BelowMinPrice) > 0 {
		fmt.Fprintf(&b, "\nBelow A$%.2f price floor (not ranked): %s\n",
			rec.Params.MinPriceAUD, strings.Join(rec.BelowMinPrice, ", "))
	}

	if len(rec.Excluded) > 0 {
		fmt.Fprintf(&b, "\nExcluded: %s\n", strings.Join(rec.Excluded, "; "))
	}
	b.WriteString("\nAdvisory only — you place all orders. Update portfolio.json after executing.")
	b.WriteString("\nPre-tax. Turnover here mostly misses the 12-month 50% CGT discount — see spec 22.")
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

// formatMarketCapAUD renders Yahoo market cap (AUD) in millions/billions —
// the units Australian market data actually uses, unlike the NSE lane's crore.
func formatMarketCapAUD(v float64) string {
	if v <= 0 {
		return "MCap n/a"
	}
	switch {
	case v >= 1e9:
		return fmt.Sprintf("A$%.1fB", v/1e9)
	case v >= 1e6:
		return fmt.Sprintf("A$%.0fM", v/1e6)
	default:
		return fmt.Sprintf("A$%.0f", v)
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

// formatStockPriceAUD keeps cents for ordinary share prices — an ASX book is
// full of names under A$10 where whole dollars would hide the actual quote.
func formatStockPriceAUD(v float64) string {
	if v <= 0 {
		return "price n/a"
	}
	return fmt.Sprintf("A$%.2f", v)
}

// aud formats an amount with thousands separators (1,234,567).
func aud(v float64) string {
	n := int64(v + 0.5)
	neg := n < 0
	if neg {
		n = -n
	}
	s := fmt.Sprintf("%d", n)
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	if s != "" {
		parts = append([]string{s}, parts...)
	}
	out := "A$" + strings.Join(parts, ",")
	if neg {
		return "-" + out
	}
	return out
}
