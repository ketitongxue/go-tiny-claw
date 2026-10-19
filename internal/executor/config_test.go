package executor

import (
	"context"
	"errors"
	"testing"
)

func TestConfiguredLocalIsExplicitAndCannotClaimReadOnly(t *testing.T) {
	work := t.TempDir()
	e, err := NewConfigured(context.Background(), ModeLocal, "", work, CodingPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := e.(*LocalExecutor); !ok {
		t.Fatalf("explicit local mode selected %T", e)
	}
	if e, err := NewConfigured(context.Background(), ModeLocal, "", work, ExplorerPolicy()); err == nil || e != nil {
		t.Fatalf("LocalExecutor claimed read-only isolation: %T (%v)", e, err)
	}
	if e, err := NewConfigured(context.Background(), "invalid", DefaultImage, work, CodingPolicy()); err == nil || e != nil {
		t.Fatalf("unknown backend accepted: %T (%v)", e, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewConfigured(ctx, ModeLocal, "", work, CodingPolicy()); !errors.Is(err, context.Canceled) {
		t.Fatalf("ignored canceled startup: %v", err)
	}
}
