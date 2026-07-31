package nserotator

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

type mockTelegram struct {
	calls int
}

func (m *mockTelegram) SendMessage(ctx context.Context, _ string) error {
	m.calls++
	return nil
}

func TestNotifyFailureUsesDetachedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tg := &mockTelegram{}
	notifyFailure(ctx, Deps{Telegram: tg, Log: slog.Default()}, "2026-07", errors.New("boom"))
	if tg.calls == 0 {
		t.Fatal("expected failure notify attempts with detached context")
	}
}

func TestSendWithRetryUsesDetachedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tg := &mockTelegram{}
	if err := sendWithRetry(ctx, tg, "hello"); err != nil {
		t.Fatalf("sendWithRetry: %v", err)
	}
	if tg.calls == 0 {
		t.Fatal("expected send attempts with detached context")
	}
}

func TestOrderApproxValueMatchesLivePrice(t *testing.T) {
	o := Order{Qty: 10, LastClose: 1000, ApproxValue: 10000}
	qPrice := 1100.0
	o.LastClose = qPrice
	o.ApproxValue = orderApproxValue(o.Qty, qPrice)
	if o.ApproxValue != 11000 {
		t.Fatalf("approx = %v want 11000", o.ApproxValue)
	}
}

func TestFormatMessageConfigurableEMA(t *testing.T) {
	msg := FormatMessage(Recommendation{
		Month:          "2026-07",
		RegimeInvested: true,
		NiftyClose:     24000,
		NiftyEMA:       23500,
	}, 6, 100)
	if !strings.Contains(msg, "EMA100") {
		t.Fatalf("expected EMA100 in message:\n%s", msg)
	}
}

func TestFilterPolicyExcludedRemovesFromRanking(t *testing.T) {
	scores := map[string]float64{"ADANIENSOL": 0.9, "TITAN": 0.5}
	notes := filterPolicyExcluded(scores, "ADANIENSOL")
	if len(notes) != 1 || notes[0] != "ADANIENSOL (excluded by policy)" {
		t.Fatalf("notes: %v", notes)
	}
	if _, ok := scores["ADANIENSOL"]; ok {
		t.Fatal("ADANIENSOL should be removed from scores")
	}
	ranked := Rank(scores)
	if len(ranked) != 1 || ranked[0].Symbol != "TITAN" {
		t.Fatalf("ranked: %+v", ranked)
	}
}
