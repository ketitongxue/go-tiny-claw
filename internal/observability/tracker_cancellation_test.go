package observability

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	ctxpkg "github.com/ketitongxue/go-tiny-claw/internal/context"
	"github.com/ketitongxue/go-tiny-claw/internal/schema"
	"github.com/ketitongxue/go-tiny-claw/internal/sessionstore"
)

func TestCostTrackerPropagatesCancellationToEveryMutation(t *testing.T) {
	for _, phase := range []string{"usage", "unmetered", "failure"} {
		t.Run(phase, func(t *testing.T) {
			store, err := sessionstore.NewFileStore(filepath.Join(t.TempDir(), "sessions"))
			if err != nil {
				t.Fatal(err)
			}
			s, err := ctxpkg.NewSessionService(store).Create(context.Background(), phase, t.TempDir(), apiModel(), "api_key")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			before := s.UsageSnapshot()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p := providerFunc(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
				cancel()
				switch phase {
				case "usage":
					return &schema.Message{Usage: &schema.Usage{PromptTokens: 100, CompletionTokens: 20}}, nil
				case "unmetered":
					return &schema.Message{}, nil
				default:
					return nil, errors.New("provider stopped")
				}
			})
			if _, err := NewCostTracker(p, apiModel(), s).Generate(ctx, nil, nil); !errors.Is(err, context.Canceled) {
				t.Fatalf("Generate error = %v", err)
			}
			if s.UsageSnapshot() != before {
				t.Fatal("canceled usage mutation committed")
			}
		})
	}
}
