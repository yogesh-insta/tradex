package etfmonitor

import (
	"fmt"
	"strings"
)

// FormatMessage renders the Telegram text for one run (spec 21 §Outputs).
// PLAIN TEXT, no Markdown parse mode — fund names contain "&" and "+" and a
// parse-mode message would break on them.
//
// Order is deliberate: exits first (the only part that demands action today),
// then the top 10, then what was disqualified and why, then the leveraged
// sideshow, then housekeeping.
func FormatMessage(r Report, topN int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "ASX ETF MONITOR — %s\n", r.Month)

	if len(r.Exits) > 0 {
		// No markdown metacharacters anywhere in this message: it is sent with
		// no parse_mode, and a future flip to Markdown must not turn a banner
		// into broken formatting.
		b.WriteString("\n!! EXIT ALERTS — held funds below trend !!\n")
		for _, e := range r.Exits {
			fmt.Fprintf(&b, "  EXIT %s — %s\n", e.Ticker, e.Reason)
		}
	}

	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "\n! %s\n", w)
	}

	if len(r.Top) == 0 {
		b.WriteString("\nTOP: none — no fund is above its 200-day trend this month.\n")
		b.WriteString("That is itself the signal: nothing is trending, stay in cash.\n")
	} else {
		fmt.Fprintf(&b, "\nTOP %d — trending up, ranked by recency-tilted momentum\n", len(r.Top))
		b.WriteString("(score = 0.5x3m + 0.3x6m + 0.2x12m; all are above their 200-day)\n")
		for i, s := range r.Top {
			fmt.Fprintf(&b, "%2d. %s\n", i+1, fundLabel(s))
			fmt.Fprintf(&b, "    3m %s | 6m %s | 12m %s | vol %s | maxDD %s\n",
				pct(s.Ret3M), pct(s.Ret6M), pct(s.Ret12M), pctf(s.Vol), pctf(s.MaxDD))
			fmt.Fprintf(&b, "    %s | suggested weight %s\n", s.Classification, pctf(s.Weight))
		}
		b.WriteString("\nWeights are inverse-volatility across these " +
			fmt.Sprintf("%d", len(r.Top)) + ".\n")
		b.WriteString("Satellite sizing suggestion, 5-10% of portfolio, not core.\n")
	}

	if len(r.BelowTrend) > 0 {
		fmt.Fprintf(&b, "\nBelow trend — not eligible (%d): %s\n",
			len(r.BelowTrend), strings.Join(tickers(r.BelowTrend), ", "))
	}

	if len(r.Watchlist) > 0 {
		fmt.Fprintf(&b, "\nWatchlist — too new to rank (%d):\n", len(r.Watchlist))
		for _, s := range r.Watchlist {
			fmt.Fprintf(&b, "  %s | 3m %s | %d/%d sessions | ~%s to go\n",
				fundLabel(s), pct(s.Ret3M), s.Sessions, s.SessionsNeeded,
				monthsRemaining(s.SessionsNeeded-s.Sessions))
		}
		b.WriteString("A fund needs a full 200-day line before it can rank —\n")
		b.WriteString("without one there is no exit signal, so there is no entry either.\n")
	}

	if len(r.Geared) > 0 {
		b.WriteString("\nGEARED / FX — leverage, not signal:\n")
		for _, s := range r.Geared {
			fmt.Fprintf(&b, "  %s | 3m %s | vol %s%s\n",
				s.Ticker, pct(s.Ret3M), pctf(s.Vol), gearedNote(s))
		}
	}
	if len(r.Inverse) > 0 {
		fmt.Fprintf(&b, "\nInverse/bear funds tracked but never ranked: %s\n",
			strings.Join(tickers(r.Inverse), ", "))
	}

	if len(r.Rejected) > 0 {
		fmt.Fprintf(&b, "\nNot scored (%d): %s\n", len(r.Rejected), strings.Join(r.Rejected, "; "))
	}
	for _, d := range r.DriftNotes {
		fmt.Fprintf(&b, "\n%s\n", d)
	}

	b.WriteString("\nAdvisory only — you place all orders. Exit rule: sell when a held fund closes below its 200-day.")
	return b.String()
}

func gearedNote(s Scored) string {
	if s.Group() == GroupGeared {
		return " | GEARED 2-3x — high risk, size tiny"
	}
	return ""
}

// Group reports which universe group a scored fund came from. Stored on the
// struct so the report does not need the universe again.
func (s Scored) Group() string { return s.group }

func fundLabel(s Scored) string {
	label := s.Ticker
	if strings.TrimSpace(s.Name) != "" {
		label += " — " + s.Name
	}
	if s.Issuer != "" {
		label += " [" + s.Issuer + ", buyable via broker]"
	}
	return label
}

func tickers(in []Scored) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = s.Ticker
	}
	return out
}

// monthsRemaining converts a shortfall in trading sessions into a rough
// calendar estimate (~21 trading days per month). Deliberately vague: no
// listing date is tracked anywhere, so this is arithmetic on the session count,
// not a real eligibility date. A data gap would make it optimistic.
func monthsRemaining(sessions int) string {
	if sessions <= 0 {
		return "ready next run"
	}
	months := float64(sessions) / 21.0
	if months < 1.5 {
		if months < 1 {
			return "under a month"
		}
		return "about a month"
	}
	return fmt.Sprintf("%.0f months", months)
}

// pct renders a possibly-absent trailing return.
func pct(r Ret) string {
	if !r.OK {
		return "n/a"
	}
	return fmt.Sprintf("%+.0f%%", r.Value*100)
}

// pctf renders a plain fraction as a percentage.
func pctf(v float64) string {
	return fmt.Sprintf("%.0f%%", v*100)
}
