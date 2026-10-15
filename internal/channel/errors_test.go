package channel

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestPermanentErrorsWrapAndUnwrap(t *testing.T) {
	cause := errors.New("invalid account")
	err := Permanent(cause)
	if !IsPermanent(fmt.Errorf("startup: %w", err)) || !errors.Is(err, cause) || err.Error() != cause.Error() {
		t.Fatal(err)
	}
	if Permanent(err) != err || Permanent(nil) != nil || IsPermanent(cause) || IsPermanent(nil) {
		t.Fatal("unexpected permanent classification")
	}
}

func TestRegistryConcurrentSnapshots(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("channel-%02d", i)
			if err := r.Register(stubAdapter(id)); err != nil {
				t.Error(err)
			}
			if _, ok := r.Get(id); !ok {
				t.Error("registered adapter missing")
			}
			for _, a := range r.All() {
				if a.ID() == "" {
					t.Error("invalid snapshot")
				}
			}
		}()
	}
	wg.Wait()
	if len(r.All()) != 32 {
		t.Fatal("lost adapter")
	}
}
