package channel

import (
	"context"
	"testing"
)

func TestRouteValidationAndSessionKey(t *testing.T) {
	r := Route{Channel: "feishu", AccountID: "default", ChatID: "oc_xxx"}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := BuildSessionKey(r); got != "channel:feishu:account:default:chat:oc_xxx" {
		t.Fatal(got)
	}
	r.ThreadID = "123.456"
	if got := BuildSessionKey(r); got != "channel:feishu:account:default:chat:oc_xxx:thread:123.456" {
		t.Fatal(got)
	}
	for _, r := range []Route{{}, {Channel: "x", ChatID: "c"}, {Channel: "x", AccountID: "a"}, {Channel: " ", AccountID: "a", ChatID: "c"}} {
		if r.Validate() == nil {
			t.Fatalf("invalid route accepted: %+v", r)
		}
	}
}

func TestSessionKeyEscapesAllComponentsWithoutCollisions(t *testing.T) {
	values := []string{"", ":", "/", `\`, "%", "%3A", " ", "+", "你好😀", "chat:a:thread:b", "a\x00b"}
	seen := map[string]Route{}
	for _, c := range values {
		for _, a := range values {
			for _, chat := range values {
				for _, thread := range values {
					r := Route{c, a, chat, thread}
					key := BuildSessionKey(r)
					if prev, ok := seen[key]; ok && prev != r {
						t.Fatalf("collision: %+v and %+v = %s", prev, r, key)
					}
					if BuildSessionKey(r) != key {
						t.Fatal("unstable key")
					}
					seen[key] = r
				}
			}
		}
	}
}

type stubAdapter string

func (a stubAdapter) ID() string                                         { return string(a) }
func (a stubAdapter) Start(context.Context, InboundHandler) error        { return nil }
func (a stubAdapter) Send(context.Context, Route, OutboundMessage) error { return nil }

func TestRegistry(t *testing.T) {
	var r Registry
	if r.Register(nil) == nil || r.Register(stubAdapter("")) == nil {
		t.Fatal("invalid adapter accepted")
	}
	if err := r.Register(stubAdapter("test")); err != nil {
		t.Fatal(err)
	}
	if r.Register(stubAdapter("test")) == nil {
		t.Fatal("duplicate adapter accepted")
	}
	if _, ok := r.Get("missing"); ok {
		t.Fatal("unknown channel exists")
	}
	if a, ok := r.Get("test"); !ok || a.ID() != "test" {
		t.Fatal("registered channel missing")
	}
}
