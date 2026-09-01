package calendar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

// TelegramClient posts messages via the Bot API sendMessage method.
// Shared by the NSE rotator and the ASX ETF monitor.
type TelegramClient struct {
	BotToken   string
	ChatID     string
	HTTPClient *http.Client
	BaseURL    string // override for tests; default https://api.telegram.org
}

func (c *TelegramClient) http() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *TelegramClient) apiRoot() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return "https://api.telegram.org"
}

// TelegramMaxChars is the Bot API sendMessage text cap. Going over it is a
// hard 400 ("message is too long") — that is what failed the Aug 2026 NSE run.
const TelegramMaxChars = 4096

// telegramPartLimit leaves room for a "(n/m)\n" continuation prefix.
const telegramPartLimit = 4000

func (c *TelegramClient) SendMessage(ctx context.Context, text string) error {
	if c.BotToken == "" || c.ChatID == "" {
		return fmt.Errorf("telegram: bot token or chat id empty")
	}
	for _, part := range SplitTelegramText(text) {
		if err := c.sendOne(ctx, part); err != nil {
			return err
		}
	}
	return nil
}

func (c *TelegramClient) sendOne(ctx context.Context, text string) error {
	payload := map[string]any{
		"chat_id": c.ChatID,
		"text":    text,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/bot%s/sendMessage", c.apiRoot(), c.BotToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http().Do(req)
	if err != nil {
		return fmt.Errorf("telegram: sendMessage: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram: HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}
	var tg struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(respBody, &tg); err != nil {
		return fmt.Errorf("telegram: parse response: %w", err)
	}
	if !tg.OK {
		return fmt.Errorf("telegram: not ok: %s", tg.Description)
	}
	return nil
}

// SplitTelegramText breaks text into Bot API-legal parts, preferring newlines.
func SplitTelegramText(text string) []string {
	if utf8.RuneCountInString(text) <= TelegramMaxChars {
		return []string{text}
	}
	parts := packTelegramLines(text, telegramPartLimit)
	if len(parts) <= 1 {
		return parts
	}
	n := len(parts)
	out := make([]string, n)
	for i, p := range parts {
		out[i] = fmt.Sprintf("(%d/%d)\n%s", i+1, n, p)
	}
	return out
}

func packTelegramLines(text string, limit int) []string {
	lines := strings.Split(text, "\n")
	var parts []string
	var cur strings.Builder
	curRunes := 0
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		parts = append(parts, strings.TrimRight(cur.String(), "\n"))
		cur.Reset()
		curRunes = 0
	}
	for _, line := range lines {
		lineRunes := utf8.RuneCountInString(line)
		need := lineRunes
		if cur.Len() > 0 {
			need++ // newline
		}
		if cur.Len() > 0 && curRunes+need > limit {
			flush()
			need = lineRunes
		}
		if lineRunes > limit {
			flush()
			parts = append(parts, chunkRunes(line, limit)...)
			continue
		}
		if cur.Len() > 0 {
			cur.WriteByte('\n')
			curRunes++
		}
		cur.WriteString(line)
		curRunes += lineRunes
	}
	flush()
	if len(parts) == 0 {
		return []string{""}
	}
	return parts
}

func chunkRunes(s string, limit int) []string {
	if limit <= 0 {
		return []string{s}
	}
	var out []string
	for s != "" {
		if utf8.RuneCountInString(s) <= limit {
			out = append(out, s)
			break
		}
		i := 0
		for n := 0; n < limit && i < len(s); n++ {
			_, size := utf8.DecodeRuneInString(s[i:])
			i += size
		}
		out = append(out, s[:i])
		s = s[i:]
	}
	return out
}

// truncate caps an error body so a failed send cannot dump a whole response
// into the logs. Moved here from the economic-calendar poller.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
