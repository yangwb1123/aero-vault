package thumbnail

// Byte-identity and allocation pins for the direct-Pix fast paths
// (pixfast.go).
//
// T1 (TestFastPathByteIdentity) is the load-bearing parity battery: it
// compares the dispatch entries (scale / applyOrientation) against the
// preserved generic references (scaleGeneric / applyOrientationGeneric) on a
// deterministic matrix — source types (RGBA/NRGBA × opaque/semi-transparent
// incl. α=0 with RGB≠0 and the anti-correlated α/c ramp), sizes (1×1 through
// the 1024² benchmark shape), scale targets (downscales, equal-dims no-op,
// upscale no-op, empty source), sub-images with nonzero Min, and every
// orientation. Identical Bounds/Stride/Pix bytes on every case is what makes
// fast ≡ generic imply fast ≡ today's bytes (the generic bodies are the
// pre-fix loops, verbatim).
//
// T2 (TestGenerateDownscaleAllocCeiling) is the machine-checkable AC-1 alloc
// gate: the ≈ 393K allocs/op pre-fix (6 allocs/pixel boxing) must drop below
// 40000 (post-fix the pipeline is dominated by the jpeg encoder's fixed cost
// ≈ 700–1,100 at 256²). It skips under -race, matching the package's
// allocation-delta convention. The JPEG arm (AC-2) floors the generic path:
// its ≈ 393,250 allocs/op baseline — the very per-pixel boxing cost the fast
// kernels eliminated for RGBA — is the regression floor for YCbCr/Paletted
// sources until a kernel direction replaces it (documented on
// jpegGenericAllocCeiling).
import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"testing"
)

// buildRGBA returns a deterministic opaque w×h *image.RGBA with x/y gradient
// content (the makePNG pattern; uint8 wraps identically to x%256).
func buildRGBA(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	return img
}

// buildRGBAAlpha returns a deterministic w×h *image.RGBA with varied alpha
// (x+y)&0xFF — including 0 and partial values — and non-zero RGB even where
// alpha is 0 (legal in straight, non-premultiplied RGBA). It discriminates
// the truncation write path and the plain-shift read path.
func buildRGBAAlpha(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x*3 + 1), uint8(y*5 + 2), uint8(x + y), uint8((x + y) & 0xFF)})
		}
	}
	return img
}

// buildNRGBA returns a deterministic opaque w×h *image.NRGBA (color-ramp
// content). For opaque pixels the NRGBA premultiply is the identity, so this
// discriminates the non-premultiplied classes from the ramp below.
func buildNRGBA(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x), uint8(y * 2), 200, 255})
		}
	}
	return img
}

// buildNRGBARamp returns the anti-correlated α/c ramp of
// TestCompositeOrderingFeatheredByteLevel (α rises left→right, c = 255−α):
// every pixel is semi-transparent with correlated premultiply rounding, the
// strongest discriminator for the NRGBA read conversion.
func buildNRGBARamp(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := uint8(x * 4) // 0..252
			img.SetNRGBA(x, y, color.NRGBA{R: 255 - a, G: 255 - a, B: 255 - a, A: a})
		}
	}
	return img
}

// assertSameRGBA fails unless fast and gen are *image.RGBA with identical
// Bounds, Stride, and Pix bytes.
func assertSameRGBA(t *testing.T, fast, gen image.Image) {
	t.Helper()
	f, fok := fast.(*image.RGBA)
	g, gok := gen.(*image.RGBA)
	if !fok || !gok {
		t.Fatalf("outputs are %T and %T, want *image.RGBA both", fast, gen)
	}
	if f.Bounds() != g.Bounds() {
		t.Fatalf("bounds %v vs %v", f.Bounds(), g.Bounds())
	}
	if f.Stride != g.Stride {
		t.Fatalf("stride %d vs %d", f.Stride, g.Stride)
	}
	if !bytes.Equal(f.Pix, g.Pix) {
		for i := range f.Pix {
			if f.Pix[i] != g.Pix[i] {
				t.Fatalf("pixel bytes differ at offset %d (0x%02x vs 0x%02x)", i, f.Pix[i], g.Pix[i])
			}
		}
	}
}

// scaleParity runs scale (the dispatcher) and, for the downscale cases,
// scaleGeneric (the verbatim generic reference) on the same inputs and
// asserts byte identity; for the no-op cases (empty source, ratio ≥ 1) it
// asserts the same-instance contract (FR-6). tw/th are derived independently
// from the dispatcher's ratio formula so a dims regression in the dispatcher
// fails loudly instead of being compared away.
func scaleParity(t *testing.T, ctx context.Context, src image.Image, maxW, maxH int) {
	t.Helper()
	fast, err := scale(ctx, src, maxW, maxH)
	if err != nil {
		t.Fatalf("scale(%d,%d): %v", maxW, maxH, err)
	}
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw == 0 || sh == 0 {
		if fast != src {
			t.Fatalf("empty-source scale returned a different instance (%T), want same", fast)
		}
		return
	}
	ratio := minF(float64(maxW)/float64(sw), float64(maxH)/float64(sh))
	if ratio >= 1 {
		if fast != src {
			t.Fatalf("no-op scale(%d,%d) on %dx%d returned a different instance (%T), want same", maxW, maxH, sw, sh, fast)
		}
		return
	}
	tw := int(float64(sw) * ratio)
	th := int(float64(sh) * ratio)
	if tw < 1 {
		tw = 1
	}
	if th < 1 {
		th = 1
	}
	gen, err := scaleGeneric(ctx, src, sw, sh, tw, th)
	if err != nil {
		t.Fatalf("scaleGeneric: %v", err)
	}
	assertSameRGBA(t, fast, gen)
}

// rotateParity runs applyOrientation (the dispatcher) against
// applyOrientationGeneric (the verbatim generic reference) for orientations
// 2–8 and asserts byte identity; for o ∈ {0,1,9} it asserts the
// same-instance contract (FR-6).
func rotateParity(t *testing.T, ctx context.Context, img image.Image, o int) {
	t.Helper()
	fast, err := applyOrientation(ctx, img, o)
	if err != nil {
		t.Fatalf("applyOrientation(%d): %v", o, err)
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if o <= 1 || o >= 9 {
		if fast != img {
			t.Fatalf("no-op applyOrientation(%d) returned a different instance (%T), want same", o, fast)
		}
		return
	}
	outW, outH := w, h
	if o >= 5 {
		outW, outH = h, w
	}
	gen, err := applyOrientationGeneric(ctx, img, o, b, w, h, outW, outH)
	if err != nil {
		t.Fatalf("applyOrientationGeneric(%d): %v", o, err)
	}
	assertSameRGBA(t, fast, gen)
}

// TestFastPathByteIdentity is T1: the fast ≡ generic byte-identity battery
// (FR-3/FR-4/FR-6). Deterministic fixtures only — no randomness, matching the
// suite's discipline.
func TestFastPathByteIdentity(t *testing.T) {
	ctx := context.Background()
	sizes := [][2]int{{1, 1}, {2, 3}, {5, 5}, {64, 64}, {300, 200}, {1024, 1024}}
	targets := [][2]int{{1, 1}, {2, 3}, {16, 16}, {64, 64}, {300, 200}}

	empty := image.NewRGBA(image.Rect(0, 0, 0, 0))

	for _, sz := range sizes {
		w, h := sz[0], sz[1]
		fixtures := map[string]image.Image{
			"rgba-opaque":  buildRGBA(w, h),
			"rgba-alpha":   buildRGBAAlpha(w, h),
			"nrgba-opaque": buildNRGBA(w, h),
			"nrgba-ramp":   buildNRGBARamp(w, h),
		}
		for name, src := range fixtures {
			for _, tg := range targets {
				t.Run(fmt.Sprintf("%dx%d/%s/downscale-%dx%d", w, h, name, tg[0], tg[1]), func(t *testing.T) {
					scaleParity(t, ctx, src, tg[0], tg[1])
				})
			}
			// No-op arms: equal dims and upscale must return the same
			// instance; the empty source is checked once below.
			t.Run(fmt.Sprintf("%dx%d/%s/equal-dims", w, h, name), func(t *testing.T) {
				scaleParity(t, ctx, src, w, h)
			})
			t.Run(fmt.Sprintf("%dx%d/%s/upscale", w, h, name), func(t *testing.T) {
				scaleParity(t, ctx, src, w+1, h+1)
			})
			for o := 2; o <= 8; o++ {
				t.Run(fmt.Sprintf("%dx%d/%s/rotate-o%d", w, h, name, o), func(t *testing.T) {
					rotateParity(t, ctx, src, o)
				})
			}
			for _, o := range []int{0, 1, 9} {
				t.Run(fmt.Sprintf("%dx%d/%s/rotate-noop-o%d", w, h, name, o), func(t *testing.T) {
					rotateParity(t, ctx, src, o)
				})
			}
		}
	}

	// Empty source: same-instance no-op (sw==0 branch).
	t.Run("empty-source", func(t *testing.T) {
		scaleParity(t, ctx, empty, 32, 32)
	})

	// Sub-image arm (FR-4/C-2): nonzero Min with shared Stride, for both
	// RGBA and NRGBA. Sampling at b.Min+(x0,y0) must cancel Min in
	// PixOffset and read the same bytes the generic At path reads.
	for _, sz := range sizes {
		w, h := sz[0], sz[1]
		if w < 4 || h < 4 {
			continue
		}
		k := w / 4
		if k < 1 {
			k = 1
		}
		subW, subH := w-k, h-k
		subs := map[string]image.Image{
			"rgba":   buildRGBA(w, h).SubImage(image.Rect(k, k, w, h)),
			"nrgba":  buildNRGBA(w, h).SubImage(image.Rect(k, k, w, h)),
			"nrgba2": buildNRGBARamp(w, h).SubImage(image.Rect(k, k, w, h)),
		}
		for name, sub := range subs {
			t.Run(fmt.Sprintf("subimage-%dx%d/%s", w, h, name), func(t *testing.T) {
				scaleParity(t, ctx, sub, (subW+1)/2, (subH+1)/2)
				for o := 2; o <= 8; o++ {
					rotateParity(t, ctx, sub, o)
				}
			})
		}
	}
}

// TestGenerateDownscaleAllocCeiling is T2, the machine-checkable AC-1 alloc
// gate: per-pixel interface boxing (≈ 393,256 allocs/op pre-fix) must be
// eliminated. The fixture is built OUTSIDE the measured closure (makePNG
// itself allocates ~1M objects — the benchmark's own pattern). The 40000
// threshold carries ~40× margin over the realistic post-fix figure (~700–1,100:
// jpeg encode fixed cost ≈ 660 at 256² dominates) and ~1000× over the
// 90%-reduction bar (39,326).
//
// jpegGenericAllocCeiling is the JPEG-arm threshold of this test (AC-2): the
// generic-path alloc baseline for a 1024² JPEG → 256² downscale is ≈ 393,250
// allocs/op (measured, Go 1.26.5, HEAD 48c2976, this box — matches the
// ≈ 6 allocs/pixel × 65,536 dst pixels ≈ 393,216 theoretical figure in
// pixfast.go to 0.01 %). The ceiling ≈ 2.03 × baseline (~100 % headroom)
// absorbs Go-version alloc-count drift (interface boxing is language-level,
// but escape-analysis shifts of a few percent must not trip CI), sits an
// order above the 52-alloc fast-path class so direction-1 (YCbCr/Paletted
// kernels) progress is measurable across the entire gap, and trips on any
// generic-path regression ≥ 2.04× the baseline (a second full per-pixel
// boxing layer, 12 allocs/pixel ≈ 786,470, lands just under the line and is
// caught instead by the committed allocs/op baselines of
// BenchmarkGenerateJPEGDownscale). The 40000 threshold cannot apply here:
// 40000 was the post-fast-path elimination bar for the RGBA arm, and the
// generic path's per-pixel boxing is the cost this gate exists to floor — a
// JPEG fixture passing at 40000 would prove it was not actually taking
// scaleGeneric (measured: 393,250, ≈ 10× over 40000).
const jpegGenericAllocCeiling = 800000

func TestGenerateDownscaleAllocCeiling(t *testing.T) {
	if raceEnabled {
		t.Skip("alloc count is race-inflated; the byte-identity and ordering pins keep discriminating under -race")
	}
	fixture := makePNG(t, 1024, 1024)
	n := testing.AllocsPerRun(20, func() {
		out, err := Generate(bytes.NewReader(fixture), 256, 256)
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if len(out) == 0 {
			t.Fatal("generate returned empty output")
		}
	})
	if n >= 40000 {
		t.Fatalf("Generate downscale allocated %.0f objects/op, want < 40000 (pre-fix: 393,256)", n)
	}

	// JPEG arm (AC-2, generic path): the fixture decodes to *image.YCbCr,
	// misses the fast-path dispatch, and pays scaleGeneric — the production
	// shape for every JPEG thumbnail. Decode-type self-check pins the fixture
	// class (a fixture that did not take scaleGeneric would pass any ceiling
	// vacuously); the measured baseline is documented on
	// jpegGenericAllocCeiling. Same raceEnabled skip as the PNG arm.
	fixtureJPEG := makeJPEG(t, 1024, 1024)
	dec, _, err := image.Decode(bytes.NewReader(fixtureJPEG))
	if err != nil {
		t.Fatalf("jpeg fixture decode: %v", err)
	}
	if _, ok := dec.(*image.YCbCr); !ok {
		t.Fatalf("jpeg fixture decoded to %T, want *image.YCbCr (generic-path class)", dec)
	}
	nJPEG := testing.AllocsPerRun(20, func() {
		out, err := Generate(bytes.NewReader(fixtureJPEG), 256, 256)
		if err != nil {
			t.Fatalf("generate jpeg: %v", err)
		}
		if len(out) == 0 {
			t.Fatal("generate jpeg returned empty output")
		}
	})
	if nJPEG >= jpegGenericAllocCeiling {
		t.Fatalf("Generate JPEG downscale allocated %.0f objects/op, want < %d (measured baseline: 393,250)", nJPEG, jpegGenericAllocCeiling)
	}
}
