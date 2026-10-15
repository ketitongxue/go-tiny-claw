package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ketitongxue/go-tiny-claw/internal/channel"
)

const (
	apiBaseURL         = "https://api.telegram.org"
	pollTimeoutSeconds = 30
	maxResponseBytes   = 8 << 20
)

type client struct {
	token, baseURL string
	http           *http.Client
}

func validateToken(token string) error {
	parts := strings.Split(token, ":")
	valid := len(parts) == 2
	if valid {
		id, err := strconv.ParseInt(parts[0], 10, 64)
		valid = err == nil && id > 0 && parts[1] != ""
		for _, r := range parts[1] {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
				valid = false
			}
		}
	}
	if !valid {
		return channel.Permanent(errors.New("invalid TELEGRAM_BOT_TOKEN; expected bot-id:secret"))
	}
	return nil
}

func newClient(token string) *client {
	return &client{token: token, baseURL: apiBaseURL, http: &http.Client{
		Timeout: (pollTimeoutSeconds + 10) * time.Second,
		// Never forward a bot-token URL to a redirect destination.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *client) getMe(ctx context.Context) (user, error) {
	var result user
	err := c.call(ctx, "getMe", struct{}{}, &result)
	return result, err
}

func (c *client) getWebhookInfo(ctx context.Context) (webhookInfo, error) {
	var result webhookInfo
	err := c.call(ctx, "getWebhookInfo", struct{}{}, &result)
	return result, err
}

func (c *client) getUpdates(ctx context.Context, offset int64) ([]update, error) {
	var result []update
	err := c.call(ctx, "getUpdates", struct {
		Offset         int64    `json:"offset"`
		Timeout        int      `json:"timeout"`
		Limit          int      `json:"limit"`
		AllowedUpdates []string `json:"allowed_updates"`
	}{offset, pollTimeoutSeconds, 100, []string{"message"}}, &result)
	return result, err
}

func (c *client) sendMessage(ctx context.Context, chatID, text string) error {
	return c.call(ctx, "sendMessage", struct {
		ChatID string `json:"chat_id"`
		Text   string `json:"text"`
	}{chatID, text}, nil) // No parse_mode: even tool output is plain text.
}

func (c *client) call(ctx context.Context, method string, body, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	timeout := 10 * time.Second
	if method == "getUpdates" {
		timeout = (pollTimeoutSeconds + 10) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	data, err := json.Marshal(body)
	if err != nil {
		return channel.Permanent(fmt.Errorf("telegram %s: invalid request", method))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.baseURL, "/")+"/bot"+c.token+"/"+method, bytes.NewReader(data))
	if err != nil {
		return channel.Permanent(errors.New("telegram: invalid API endpoint"))
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// net/http errors include the request URL, which contains the secret.
		return fmt.Errorf("telegram %s: network request failed", method)
	}
	defer resp.Body.Close()
	data, err = io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || len(data) > maxResponseBytes {
		return fmt.Errorf("telegram %s: unreadable or oversized response", method)
	}
	var envelope struct {
		OK         bool            `json:"ok"`
		Result     json.RawMessage `json:"result"`
		ErrorCode  int             `json:"error_code"`
		Parameters struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	decodeErr := json.Unmarshal(data, &envelope)
	if resp.StatusCode != http.StatusOK || decodeErr == nil && !envelope.OK {
		code := envelope.ErrorCode
		if code == 0 {
			code = resp.StatusCode
		}
		return classifyAPIError(method, code, envelope.Parameters.RetryAfter)
	}
	if decodeErr != nil || !envelope.OK || len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return fmt.Errorf("telegram %s: invalid API response", method)
	}
	if result != nil && json.Unmarshal(envelope.Result, result) != nil {
		return fmt.Errorf("telegram %s: invalid result", method)
	}
	return nil
}

type rateLimitError struct{ delay time.Duration }

func (e *rateLimitError) Error() string             { return "telegram: rate limited" }
func (e *rateLimitError) RetryAfter() time.Duration { return e.delay }

func classifyAPIError(method string, code, retryAfter int) error {
	if code == http.StatusTooManyRequests {
		return &rateLimitError{delay: time.Duration(min(max(retryAfter, 1), 86400)) * time.Second}
	}
	var err error
	switch code {
	case http.StatusUnauthorized, http.StatusNotFound:
		err = fmt.Errorf("telegram %s: invalid bot token (code=%d)", method, code)
	case http.StatusConflict:
		err = fmt.Errorf("telegram %s: webhook or another polling client is active; stop the other update consumer (code=%d)", method, code)
	default:
		err = fmt.Errorf("telegram %s: API request failed (code=%d)", method, code)
	}
	if code >= 400 && code < 500 && code != http.StatusRequestTimeout && code != http.StatusTooEarly {
		return channel.Permanent(err)
	}
	return err
}
