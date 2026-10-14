package sessionstore

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/ketitongxue/go-tiny-claw/internal/schema"
)

// Each sample appends one message to exactly 100/1000 historical messages.
// Restoring the known fixture (including its cache) is excluded from timing and
// allocations. No full replay is hidden in the timed Append operation.
func BenchmarkFileStoreAppend(b *testing.B) {
	for _, count := range []int{100, 1000} {
		b.Run(fmt.Sprintf("events=%d", count), func(b *testing.B) {
			f := newTestFileStore(b)
			ownSession(b, f, "benchmark")
			ctx := context.Background()
			seed := snapshot("benchmark")
			output := strings.Repeat("tool stdout/stderr 代码\n", 100)
			for range count {
				seed.History = append(seed.History, schema.Message{Role: schema.RoleUser, Content: output})
			}
			if err := f.Create(ctx, seed); err != nil {
				b.Fatal(err)
			}
			st := f.state("benchmark")
			base, offset := st.snapshot, st.committedOffset
			event := messageEvent(output)
			// An accidental read/replay on the fast path is a benchmark failure,
			// independently of noisy filesystem timings.
			f.read = func(string) ([]byte, error) {
				b.Fatal("Append replayed historical JSONL")
				return nil, nil
			}
			reset := func() {
				file, err := os.OpenFile(f.path("benchmark"), os.O_WRONLY, 0600)
				if err != nil {
					b.Fatal(err)
				}
				if err := file.Truncate(offset); err != nil {
					_ = file.Close()
					b.Fatal(err)
				}
				if err := file.Sync(); err != nil {
					_ = file.Close()
					b.Fatal(err)
				}
				info, err := file.Stat()
				closeErr := file.Close()
				if err != nil || closeErr != nil {
					b.Fatalf("reset stat/close: %v / %v", err, closeErr)
				}
				st.snapshot, st.committedOffset, st.fileSize, st.info = base, offset, offset, info
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := range b.N {
				if i > 0 {
					b.StopTimer()
					reset()
					b.StartTimer()
				}
				if err := f.Append(ctx, "benchmark", event); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
