package feishu

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/channel"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

func TestStartReleasesLifecycleStateAfterCancelledBootstrap(t *testing.T) {
	requests := make(chan struct{}, 2)
	a := &Adapter{appID: "offline-app", appSecret: "secret", client: lark.NewClient("offline-app", "secret",
		lark.WithEnableTokenCache(false), lark.WithHttpClient(&http.Client{Transport: botRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			requests <- struct{}{}
			<-r.Context().Done()
			return nil, r.Context().Err()
		})}))}
	for range 2 {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- a.Start(ctx, func(context.Context, channel.InboundMessage) error { return nil }) }()
		receive(t, requests)
		cancel()
		if err := receive(t, done); !errors.Is(err, context.Canceled) {
			t.Fatal("restart failed", err)
		}
		a.mu.RLock()
		if a.started || a.ctx != nil || a.handler != nil {
			t.Error("Start retained stale lifecycle state")
		}
		a.mu.RUnlock()
	}
}

func TestConnectionFailureAfterReadyReturnsAndCleansUp(t *testing.T) {
	a := &Adapter{}
	for range 2 {
		conn := &fakeConnection{started: make(chan struct{}), closed: make(chan struct{})}
		failed := make(chan error, 1)
		done := make(chan error, 1)
		go func() { done <- a.serveConnectionUntil(context.Background(), conn, failed) }()
		receive(t, conn.started)
		cause := errors.New("disconnected")
		failed <- cause
		if err := receive(t, done); !errors.Is(err, cause) {
			t.Fatal(err)
		}
		receive(t, conn.closed)
	}
}

func TestConnectionErrorClassificationRedactsSDKDetails(t *testing.T) {
	if err := classifyConnectionError(larkws.NewClientError(1234, "private-ticket-secret")); !channel.IsPermanent(err) || strings.Contains(err.Error(), "private-ticket-secret") {
		t.Fatal(err)
	}
	if err := classifyConnectionError(errors.New("https://private-ticket-secret")); err == nil || channel.IsPermanent(err) || strings.Contains(err.Error(), "private-ticket-secret") {
		t.Fatal(err)
	}
}
