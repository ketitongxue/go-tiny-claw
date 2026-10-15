package main

import (
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/channel"
)

func TestLegacyFeishuMappingRestrictedToOriginalConversation(t *testing.T) {
	route := channel.Route{Channel: "feishu", AccountID: "default", ChatID: "oc_old"}
	if legacyFeishuSessionID(route) != "oc_old" {
		t.Fatal("legacy lookup missing")
	}
	for _, r := range []channel.Route{
		{Channel: "other", AccountID: "default", ChatID: "oc_old"},
		{Channel: "feishu", AccountID: "another", ChatID: "oc_old"},
		{Channel: "feishu", AccountID: "default", ChatID: "oc_old", ThreadID: "thread"},
	} {
		if legacyFeishuSessionID(r) != "" {
			t.Fatalf("route inherited default-bot legacy history: %+v", r)
		}
	}
}
