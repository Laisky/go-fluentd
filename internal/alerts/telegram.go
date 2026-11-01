package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// DeliveryResult describes a transport outcome without exposing response bodies.
type DeliveryResult struct {
	Delivered, Retry bool
	Delay            time.Duration
}

// Notifier is the narrow transport boundary; it must honor cancellation.
type Notifier interface {
	Send(context.Context, string) DeliveryResult
}

type telegramNotifier struct {
	token, chatID, endpoint string
	client                  *http.Client
}

func newTelegramNotifier(cfg Config, endpoint string, client *http.Client) telegramNotifier {
	if endpoint == "" {
		endpoint = "https://api.telegram.org"
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return telegramNotifier{token: cfg.Token, chatID: cfg.ChatID, endpoint: strings.TrimRight(endpoint, "/"), client: client}
}

// sendMessage disables link previews: they would make Telegram fetch any
// bare domain that survives URL redaction, disclosing log content to that host.
type sendMessage struct {
	ChatID             string `json:"chat_id"`
	Text               string `json:"text"`
	LinkPreviewOptions struct {
		IsDisabled bool `json:"is_disabled"`
	} `json:"link_preview_options"`
}

// Send posts one plain-text notification to the fixed sendMessage method.
func (n telegramNotifier) Send(ctx context.Context, text string) DeliveryResult {
	message := sendMessage{ChatID: n.chatID, Text: text}
	message.LinkPreviewOptions.IsDisabled = true
	body, _ := json.Marshal(message)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.endpoint+"/bot"+n.token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return DeliveryResult{}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return DeliveryResult{Retry: true, Delay: time.Second}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var result struct {
		OK         bool `json:"ok"`
		Parameters struct {
			RetryAfter int64 `json:"retry_after"`
		} `json:"parameters"`
	}
	decodeErr := json.Unmarshal(b, &result)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && result.OK {
		return DeliveryResult{Delivered: true}
	}
	if resp.StatusCode == 429 {
		seconds := result.Parameters.RetryAfter
		if decodeErr != nil || seconds < 0 || seconds > 3600 {
			return DeliveryResult{}
		}
		delay := time.Duration(seconds) * time.Second
		if delay < time.Second {
			delay = time.Second
		}
		return DeliveryResult{Retry: true, Delay: delay}
	}
	if resp.StatusCode >= 500 {
		return DeliveryResult{Retry: true, Delay: time.Second}
	}
	return DeliveryResult{}
}
