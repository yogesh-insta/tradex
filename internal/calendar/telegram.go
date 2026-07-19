package calendar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Source labels for the Telegram review header (exact strings from the spec).
const (
	SourceFinnhubPrimary = "Finnhub Primary"
	SourceGeminiFallback = "Gemini Fallback Pipeline"
)

// TelegramClient posts calendar review messages via Bot API sendMessage.
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

// SendCalendarReview posts: source header + fenced json block.
func (c *TelegramClient) SendCalendarReview(ctx context.Context, source string, state State) error {
	if c.BotToken == "" || c.ChatID == "" {
		return fmt.Errorf("telegram: bot token or chat id empty")
	}
	bodyJSON, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	text := source + "\n\n```json\n" + string(bodyJSON) + "\n```"
	return c.SendMessage(ctx, text)
}

// SendMessage posts a plain text message via Bot API sendMessage.
func (c *TelegramClient) SendMessage(ctx context.Context, text string) error {
	if c.BotToken == "" || c.ChatID == "" {
		return fmt.Errorf("telegram: bot token or chat id empty")
	}
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
