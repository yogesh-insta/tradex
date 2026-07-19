package logging

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/yogesh-insta/tradex/pkg/types"
)

type recordingSender struct{ messages chan string }

func (s recordingSender) SendMessage(_ context.Context, text string) error {
	s.messages <- text
	return nil
}

func TestTelegramPublisherQueuesLifecycleMessage(t *testing.T) {
	sender := recordingSender{messages: make(chan string, 1)}
	publisher := NewTelegramPublisher(NopPublisher{}, sender, slog.Default())
	defer publisher.Flush()
	publisher.Publish(types.TradeEvent{
		Type: "opened", Account: "fx", Direction: "LONG",
		Instrument: "USD_JPY", TradeID: "123",
	})
	select {
	case message := <-sender.messages:
		if message == "" || message[:13] != "tradex OPENED" {
			t.Fatalf("message = %q", message)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for telegram message")
	}
}

func TestTelegramTextIgnoresNonLifecycleEvents(t *testing.T) {
	if _, ok := telegramText(types.TradeEvent{Type: "modified"}); ok {
		t.Fatal("modified event should not alert")
	}
}
