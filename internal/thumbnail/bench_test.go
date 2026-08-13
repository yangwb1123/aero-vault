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

// BenchmarkGenerateJPEGDownscale covers the generic-path shape that every
// JPEG thumbnail takes in production: a 1024² JPEG decodes to *image.YCbCr,
// which misses the dispatcher's *image.RGBA/*image.NRGBA switch
// (thumbnail.go:380-384) and traverses scaleGeneric (≈ 6 allocs/pixel: 4
// At() boxes + Set box + rgbamodel re-box, pixfast.go:1-10). The scaled
// output is fully opaque (YCbCr→RGBA A=65535), so the post-scale
// compositeOnWhite (slot_open.go:232) is a no-op. Fixture = makePNG content
// re-encoded as JPEG at quality 82, so PNG-vs-JPEG deltas are codec-only.
// Baseline (Go 1.26.5, HEAD 48c2976, this box, 2×1s runs): ≈ 9.9 ms/op
// (9.60/10.20), 393,250 allocs/op, 52,360-byte fixture — 393,250 ≈ 6/pixel ×
// 65,536 dst pixels + ~34 fixed, matching pixfast.go's theoretical figure to
// 0.01 %. The 52-alloc/op PNG control (BenchmarkGenerateDownscale) is the
// fast-path contrast; the difference is the direction-1 (YCbCr kernels) ROI.
// Run: go test -bench='BenchmarkGenerate(JPEG|GIF)Downscale$' -benchmem ./internal/thumbnail/
func BenchmarkGenerateJPEGDownscale(b *testing.B) {
	fixture := makeJPEG(b, 1024, 1024)
	b.SetBytes(int64(len(fixture)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Generate(bytes.NewReader(fixture), 256, 256); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGenerateGIFDownscale covers the transparent-GIF generic-path
// shape: a 1024² transparent GIF decodes to *image.Paletted and traverses
// scaleGeneric — at ≈ 2 allocs/pixel (131,122 allocs/op): Paletted.At returns
// a pre-materialized palette interface (no boxing), so only the dst.Set box
// and rgbamodel re-box allocate. Opaque()==false (a transparent palette
// entry) makes the post-scale scaled RGBA (A=0) pay the generic
// compositeOnWhite draw.Over flatten (slot_open.go:232) — the fuller
// production shape; the opaque-GIF control (makeWideGIF) measures 131,117
// (Δ5 = the composite).
// Baseline (Go 1.26.5, HEAD 48c2976, this box, 2×1s runs): ≈ 5.8 ms/op
// (5.76/5.84), 131,122 allocs/op, 1,790-byte fixture.
// Run: go test -bench='BenchmarkGenerate(JPEG|GIF)Downscale$' -benchmem ./internal/thumbnail/
func BenchmarkGenerateGIFDownscale(b *testing.B) {
	fixture := makeTransparentGIF(b, 1024, 1024)
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

// BenchmarkGeneratePNGWithEXIF covers the PNG-with-eXIf shape (F8): the
// pre-Decode orientation walk runs before Decode — for the small fixture a
// few chunk-header reads from head, no r reads, no replay — the baseline for
// the walk's per-request cost; BenchmarkGeneratePNGPlain is the no-eXIf
// control (the two should be within noise).
// Run: go test -bench='BenchmarkGeneratePNG(WithEXIF|Plain)$' -benchmem ./internal/thumbnail/
func BenchmarkGeneratePNGWithEXIF(b *testing.B) {
	fixture := orientedPNG(b, 64, 64, 6, false)
	b.SetBytes(int64(len(fixture)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Generate(bytes.NewReader(fixture), 64, 64); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGeneratePNGPlain is the no-eXIf control for the walk benchmark.
func BenchmarkGeneratePNGPlain(b *testing.B) {
	fixture := orientedPNG(b, 64, 64, 0, false)
	b.SetBytes(int64(len(fixture)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Generate(bytes.NewReader(fixture), 64, 64); err != nil {
			b.Fatal(err)
		}
	}
}
