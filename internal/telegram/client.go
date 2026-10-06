// Package telegram lets a person answer the engine's questions, confirm
// predictions and audit the local LLM's labels from a Telegram chat. It uses
// long polling, so it only needs outbound HTTPS (works behind NAT or in a
// cloud container) and never exposes a port.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is a minimal Telegram Bot API client.
type Client struct {
	Token   string
	BaseURL string // default https://api.telegram.org
	HTTP    *http.Client
}

// Button is an inline keyboard button.
type Button struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}

// Update is an incoming event (only what the bot uses).
type Update struct {
	ID       int64 `json:"update_id"`
	Message  *Msg  `json:"message"`
	Callback *struct {
		ID      string             `json:"id"`
		Data    string             `json:"data"`
		From    struct{ ID int64 } `json:"from"`
		Message *Msg               `json:"message"`
	} `json:"callback_query"`
}

// Msg is an incoming message.
type Msg struct {
	ID   int64  `json:"message_id"`
	Text string `json:"text"`
	Chat struct {
		ID int64 `json:"id"`
	} `json:"chat"`
}

type apiResp struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
}

func (c *Client) call(ctx context.Context, method string, payload any, out any) error {
	base := c.BaseURL
	if base == "" {
		base = "https://api.telegram.org"
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/bot%s/%s", base, c.Token, method), bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	h := c.HTTP
	if h == nil {
		h = &http.Client{Timeout: 45 * time.Second}
	}
	resp, err := h.Do(req)
	if err != nil {
		return redact(err, c.Token)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	var ar apiResp
	if err := json.Unmarshal(raw, &ar); err != nil {
		return fmt.Errorf("telegram %s: HTTP %d, unreadable response", method, resp.StatusCode)
	}
	if !ar.OK {
		return fmt.Errorf("telegram %s: %s", method, ar.Description)
	}
	if out != nil {
		return json.Unmarshal(ar.Result, out)
	}
	return nil
}

// redact keeps the bot token out of error messages and logs (it is part of the URL).
func redact(err error, token string) error {
	if token == "" {
		return err
	}
	msg := bytes.ReplaceAll([]byte(err.Error()), []byte(token), []byte("<token>"))
	return fmt.Errorf("%s", msg)
}

// GetUpdates long-polls for events.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]Update, error) {
	var ups []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset": offset, "timeout": timeoutSec, "allowed_updates": []string{"message", "callback_query"},
	}, &ups)
	return ups, err
}

// SendMessage sends plain text (no parse mode: nothing in an email can inject markup).
func (c *Client) SendMessage(ctx context.Context, chat int64, text string, rows [][]Button) (int64, error) {
	p := map[string]any{"chat_id": chat, "text": text, "disable_web_page_preview": true}
	if len(rows) > 0 {
		p["reply_markup"] = map[string]any{"inline_keyboard": rows}
	}
	var m Msg
	if err := c.call(ctx, "sendMessage", p, &m); err != nil {
		return 0, err
	}
	return m.ID, nil
}

// EditMessage replaces a message's text and removes its keyboard.
func (c *Client) EditMessage(ctx context.Context, chat, msg int64, text string, rows [][]Button) error {
	p := map[string]any{"chat_id": chat, "message_id": msg, "text": text, "disable_web_page_preview": true}
	if len(rows) > 0 {
		p["reply_markup"] = map[string]any{"inline_keyboard": rows}
	} else {
		p["reply_markup"] = map[string]any{"inline_keyboard": [][]Button{}}
	}
	return c.call(ctx, "editMessageText", p, nil)
}

// AnswerCallback acknowledges a button press (stops the spinner).
func (c *Client) AnswerCallback(ctx context.Context, id, text string) error {
	return c.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text}, nil)
}
