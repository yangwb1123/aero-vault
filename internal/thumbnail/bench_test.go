package thumbnail

// Throughput benchmarks for the decode pipeline (perf condition (b) of the
// slot-before-open direction): the 4-slot semaphore ceiling's cost is the
// park under load, so the parallel benchmark with a hot fixture shows the
// steady-state ceiling. The encode-path pair (BenchmarkGenerateHardMaxEncode
// + BenchmarkEncodeCtxWriterOverhead, QA F4) pins the per-emitted-byte
// ctx.Err() check of the in-encode cancellation boundary against the pre-fix
// raw jpeg.Encode(&buf) form, with the baseline recorded in their doc
// comments. Run: go test -bench=. -benchmem ./internal/thumbnail/
import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
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

// BenchmarkGenerateHardMaxEncode covers the worst-case encode-dominated
// shape at the HardMax ceiling (QA F4): a 2048² opaque PNG at a HardMax box —
// scale is a ratio-≥1 no-op, compositeOnWhite is an opaque no-op, no EXIF
// (PNG source) — so jpeg.Encode is the hot path and every one of the
// ~208 K emitted bytes pays the ctxWriter ctx.Err() atomic load. Baseline
// with the ctxWriter applied (Go 1.26.5, HEAD d7b76e4 + encode_writer.go,
// this box, 2×1s runs): ≈ 65 ms/op (64.9/66.7), 48 allocs/op, ≈ 17.4 MB/op;
// BenchmarkEncodeCtxWriterOverhead isolates the writer delta against the
// pre-fix raw jpeg.Encode(&buf) form.
// Run: go test -bench='BenchmarkGenerateHardMaxEncode$' -benchmem ./internal/thumbnail/
func BenchmarkGenerateHardMaxEncode(b *testing.B) {
	fixture := benchFixture(b, HardMax, HardMax)
	b.SetBytes(int64(len(fixture)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := GenerateContext(context.Background(), bytes.NewReader(fixture), HardMax, HardMax); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEncodeCtxWriterOverhead isolates the cost of the in-encode
// cancellation check on the encode-dominated 2048² frame (QA F4). Both arms
// jpeg.Encode the identical opaque 2048² frame (decoded once from the same
// PNG fixture) at the pipeline's quality 82. The "raw" arm writes to a plain
// bytes.Buffer — the pre-fix HEAD form jpeg.Encode(&buf), which stdlib wraps
// in a heap-allocated bufio.Writer (bytes.Buffer lacks Flush, writer.go:581)
// — and the "ctxWriter" arm writes through &ctxWriter{ctx, &buf} — the
// post-fix form, which implements the writer interface and drops the bufio.
// Output is byte-identical in both arms (checked in setup; deterministically
// pinned by TestEncodeWriterHonorsContext and the A2 anchors), so the ns/op
// delta is the net cost of one ctx.Err() atomic load per emitted byte
// (~208 K loads/encode) against the bufio copy it replaced. Measured on this
// box (Go 1.26.5, HEAD d7b76e4 + encode_writer.go, 2×3s runs): raw
// 35.0/38.4 ms/op vs ctxWriter 35.6/36.6 ms/op — the arm delta (≈ ±1.5 ms,
// < 5 %) sits inside the run-to-run spread, so the per-byte atomic load is
// within noise (the design's claim, now measured, not assumed); -benchmem
// shows the bufio removal drops allocs/op 7→6 and ≈ 4 KB/op. No perf
// hard-gate (house style): this recorded baseline + periodic -benchmem spot
// check is the guard (QA F4 P2).
// Run: go test -bench='BenchmarkEncodeCtxWriterOverhead' -benchmem ./internal/thumbnail/
func BenchmarkEncodeCtxWriterOverhead(b *testing.B) {
	fixture := benchFixture(b, HardMax, HardMax)
	dst, _, err := image.Decode(bytes.NewReader(fixture))
	if err != nil {
		b.Fatal(err)
	}
	// Byte-identity sanity at benchmark scale: the two forms must emit the
	// same bytes while ctx is alive, so the measured delta is purely the
	// per-write check (the deterministic pin is TestEncodeWriterHonorsContext).
	var ref, viaWriter bytes.Buffer
	if err := jpeg.Encode(&ref, dst, &jpeg.Options{Quality: quality}); err != nil {
		b.Fatal(err)
	}
	if err := jpeg.Encode(&ctxWriter{ctx: context.Background(), buf: &viaWriter}, dst, &jpeg.Options{Quality: quality}); err != nil {
		b.Fatal(err)
	}
	if !bytes.Equal(ref.Bytes(), viaWriter.Bytes()) {
		b.Fatalf("raw and ctxWriter outputs differ: %d vs %d bytes", ref.Len(), viaWriter.Len())
	}

	b.Run("raw", func(b *testing.B) {
		var buf bytes.Buffer
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			buf.Reset()
			if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: quality}); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("ctxWriter", func(b *testing.B) {
		var buf bytes.Buffer
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			buf.Reset()
			if err := jpeg.Encode(&ctxWriter{ctx: context.Background(), buf: &buf}, dst, &jpeg.Options{Quality: quality}); err != nil {
				b.Fatal(err)
			}
		}
	})
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
