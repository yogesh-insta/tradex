package calendar

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitTelegramTextShortUnchanged(t *testing.T) {
	got := SplitTelegramText("hello")
	if len(got) != 1 || got[0] != "hello" {
		t.Fatalf("got %#v", got)
	}
}

func TestSplitTelegramTextBreaksOverCap(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 80; i++ {
		b.WriteString("line ")
		b.WriteString(strings.Repeat("x", 80))
		b.WriteByte('\n')
	}
	text := b.String()
	if utf8.RuneCountInString(text) <= TelegramMaxChars {
		t.Fatalf("fixture too short: %d", utf8.RuneCountInString(text))
	}
	parts := SplitTelegramText(text)
	if len(parts) < 2 {
		t.Fatalf("expected multiple parts, got %d", len(parts))
	}
	joined := ""
	for i, p := range parts {
		if utf8.RuneCountInString(p) > TelegramMaxChars {
			t.Fatalf("part %d is %d runes", i, utf8.RuneCountInString(p))
		}
		if !strings.HasPrefix(p, "(") {
			t.Fatalf("part %d missing continuation prefix: %q", i, p[:min(20, len(p))])
		}
		_, rest, ok := strings.Cut(p, "\n")
		if !ok {
			t.Fatalf("part %d has no body", i)
		}
		joined += rest + "\n"
	}
	if !strings.Contains(joined, "line "+strings.Repeat("x", 80)) {
		t.Fatal("split dropped body text")
	}
}

func TestSplitTelegramTextHardSplitsOversizedLine(t *testing.T) {
	line := strings.Repeat("a", TelegramMaxChars+50)
	parts := SplitTelegramText(line)
	if len(parts) < 2 {
		t.Fatalf("expected split, got %d", len(parts))
	}
	for i, p := range parts {
		if utf8.RuneCountInString(p) > TelegramMaxChars {
			t.Fatalf("part %d is %d runes", i, utf8.RuneCountInString(p))
		}
	}
}

func TestSendMessageSplitsLongText(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Errorf("payload: %v", err)
		}
		bodies = append(bodies, payload.Text)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := &TelegramClient{
		BotToken:   "token",
		ChatID:     "chat",
		HTTPClient: srv.Client(),
		BaseURL:    srv.URL,
	}
	long := strings.Repeat("LINE\n", 1200) // well over 4096
	if err := c.SendMessage(context.Background(), long); err != nil {
		t.Fatal(err)
	}
	if len(bodies) < 2 {
		t.Fatalf("expected multiple sendMessage calls, got %d", len(bodies))
	}
	for i, body := range bodies {
		if utf8.RuneCountInString(body) > TelegramMaxChars {
			t.Fatalf("call %d sent %d runes", i, utf8.RuneCountInString(body))
		}
	}
}
