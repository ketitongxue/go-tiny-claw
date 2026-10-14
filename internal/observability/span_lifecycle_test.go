package observability

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestEndSpanIdempotent(t *testing.T) {
	_, span := StartSpan(context.Background(), "turn")
	time.Sleep(2 * time.Millisecond)
	span.EndSpan()
	end, duration := span.EndTime, span.DurationMs
	time.Sleep(2 * time.Millisecond)
	span.EndSpan()
	if span.EndTime != end || span.DurationMs != duration || duration <= 0 {
		t.Fatalf("second EndSpan changed timing: %v/%d -> %v/%d", end, duration, span.EndTime, span.DurationMs)
	}
}

func TestEndSpanConcurrent(t *testing.T) {
	_, span := StartSpan(context.Background(), "turn")
	span.EndSpan()
	end, duration := span.EndTime, span.DurationMs
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() { span.EndSpan() })
	}
	wg.Wait()
	if span.EndTime != end || span.DurationMs != duration {
		t.Fatal("concurrent EndSpan changed first end time")
	}
}
