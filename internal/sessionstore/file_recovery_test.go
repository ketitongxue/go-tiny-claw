package sessionstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
)

func TestFileStoreRecoveryKeepsV1Semantics(t *testing.T) {
	for _, tail := range []string{"torn", "torn-with-newline", "valid-without-newline", "middle-corruption"} {
		t.Run(tail, func(t *testing.T) {
			ctx := context.Background()
			f := newTestFileStore(t)
			ownSession(t, f, "recovery")
			if err := f.Create(ctx, snapshot("recovery")); err != nil {
				t.Fatal(err)
			}
			if err := f.Append(ctx, "recovery", messageEvent("old committed event")); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(f.path("recovery"))
			if err != nil {
				t.Fatal(err)
			}
			switch tail {
			case "torn":
				data = append(data, []byte(`{"version":`)...)
			case "torn-with-newline":
				data = append(data, []byte("{\"version\":\n")...)
			case "valid-without-newline":
				data = bytes.TrimSuffix(data, []byte{'\n'})
			case "middle-corruption":
				extra, _ := encode([]Event{messageEvent("after corrupt line")})
				data = append(append(data, []byte("broken\n")...), extra...)
			}
			if err := os.WriteFile(f.path("recovery"), data, 0600); err != nil {
				t.Fatal(err)
			}
			s, err := f.Load(ctx, "recovery")
			if tail == "middle-corruption" {
				if !errors.Is(err, ErrCorrupted) {
					t.Fatalf("middle corruption = %v", err)
				}
				if err := f.Append(ctx, "recovery", messageEvent("reject")); !errors.Is(err, ErrCorrupted) {
					t.Fatal(err)
				}
				return
			}
			if err != nil || len(s.History) != 1 {
				t.Fatalf("recovery = %+v, %v", s, err)
			}
			if err := f.Append(ctx, "recovery", messageEvent("after recovery")); err != nil {
				t.Fatal(err)
			}
			reader, err := NewFileStore(f.dir)
			if err != nil {
				t.Fatal(err)
			}
			s, err = reader.Load(ctx, "recovery")
			if err != nil || len(s.History) != 2 || s.History[0].Content != "old committed event" || s.History[1].Content != "after recovery" {
				t.Fatalf("append hid damage: %+v, %v", s, err)
			}
		})
	}
}
