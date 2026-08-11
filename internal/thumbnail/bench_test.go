package thumbnail

// Throughput benchmarks for the decode pipeline (perf condition (b) of the
// slot-before-open direction): the 4-slot semaphore ceiling's cost is the
// park under load, so the parallel benchmark with a hot fixture shows the
// steady-state ceiling. Run: go test -bench=. -benchmem ./internal/thumbnail/
import (
	"bytes"
	"context"
	"io"
	"testing"
)

func benchFixture(tb testing.TB, w, h int) []byte {
	tb.Helper()
	return makePNG(tb, w, h)
}

// BenchmarkGenerateSmall covers the common small-image path (no downscale,
// composite fast path for the opaque fixture).
func BenchmarkGenerateSmall(b *testing.B) {
	fixture := benchFixture(b, 64, 64)
	b.SetBytes(int64(len(fixture)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Generate(bytes.NewReader(fixture), 64, 64); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGenerateDownscale covers the dominant production shape: a large
// source reduced to a 256×256 thumbnail.
func BenchmarkGenerateDownscale(b *testing.B) {
	fixture := benchFixture(b, 1024, 1024)
	b.SetBytes(int64(len(fixture)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Generate(bytes.NewReader(fixture), 256, 256); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGenerateContextWithOpener covers the REST handler's actual entry
// point (slot-before-open): the opener cost is a no-op stream wrap, so the
// benchmark isolates the acquire→open→decode→close→release sequence.
func BenchmarkGenerateContextWithOpener(b *testing.B) {
	fixture := benchFixture(b, 256, 256)
	open := func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(fixture)), nil
	}
	b.SetBytes(int64(len(fixture)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := GenerateContextWithOpener(context.Background(), 128, 128, open); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGenerateParallel shows the 4-slot semaphore ceiling under
// concurrency: b.N is split across workers, so total throughput is bounded
// by maxConcurrentDecodes × per-request latency. A slot-leak regression
// (capacity drift) shows up as a throughput cliff rather than a failure.
func BenchmarkGenerateParallel(b *testing.B) {
	fixture := benchFixture(b, 256, 256)
	b.SetBytes(int64(len(fixture)))
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := Generate(bytes.NewReader(fixture), 128, 128); err != nil {
				b.Error(err)
				return
			}
		}
	})
}
