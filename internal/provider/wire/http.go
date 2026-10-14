// Package wire provides a small transport with deliberately redacted errors.
package wire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

func Client(client *http.Client) *http.Client {
	var c http.Client
	if client != nil {
		c = *client
	} else {
		c.Timeout = 2 * time.Minute
	}
	// A redirect must never forward credentials to a different endpoint.
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}
func Post(ctx context.Context, client *http.Client, endpoint string, headers map[string]string, body any) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, errors.New("cannot encode model request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("invalid model endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// url.Error and SDK errors may contain credentials, URLs or request data.
		return nil, errors.New("model HTTP request failed (transport error)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		// Do not echo arbitrary server bodies: gateways can echo Authorization.
		return nil, fmt.Errorf("model HTTP request failed (status %d)", resp.StatusCode)
	}
	return resp, nil
}
