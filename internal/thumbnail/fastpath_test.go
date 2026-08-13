package thumbnail

// Byte-identity and allocation pins for the direct-Pix fast paths
// (pixfast.go).
//
// T1 (TestFastPathByteIdentity) is the load-bearing parity battery: it
// compares the dispatch entries (scale / applyOrientation) against the
// preserved generic references (scaleGeneric / applyOrientationGeneric) on a
// deterministic matrix — source types (RGBA/NRGBA × opaque/semi-transparent
// incl. α=0 with RGB≠0 and the anti-correlated α/c ramp; YCbCr × 420/422/444
// × ramp/clamp-extreme; Gray × ramp/solid; Paletted × opaque/gradient/
// semi-transparent-NRGBA/gray-entries; builders in fastpath_more_test.go),
// sizes (1×1 through the 1024² benchmark shape), scale targets (downscales,
// equal-dims no-op, upscale no-op, empty source), sub-images with nonzero
// Min (incl. the odd-Min YCbCr 420 case that discriminates the
// non-canceling subsampled chroma COffset form), and every orientation.
// Identical Bounds/Stride/Pix bytes on every case is what makes fast ≡
// generic imply fast ≡ today's bytes (the generic bodies are the pre-fix
// loops, verbatim).
//
// T2 (TestGenerateDownscaleAllocCeiling) is the machine-checkable alloc
// gate shared by both codec arms: the PNG arm (RGBA fast path, ≈ 53
// allocs/op) and the JPEG arm (YCbCr/Paletted fast kernels, ≈ 42–48
// allocs/op post-fix — decode 7 + encode 9 + ~26 scaffolding) must stay
// below the same 40000 threshold, which trips on any second full per-pixel
// boxing layer (12 allocs/pixel ≈ 786,470; the pre-kernel generic class
// measured 393,251). It skips under -race, matching the package's
// allocation-delta convention. Pre-kernel baselines are recorded in
// bench_test.go.
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

	// The RGBA/NRGBA fixtures run the full size matrix in every mode. The
	// YCbCr/Gray/Paletted fixtures (moreSizes) add the 1024² shape only in
	// normal mode: under -race a generic 1024² rotate arm boxes ≈ 2M
	// objects (the performance review measured the untrimmed suite past the
	// 120s test-race-thumbnail budget). With the trim the full -race -short
	// suite measures 102.5s on this box (parity battery 14.4s; Go 1.26.5,
	// HEAD 57a5e2d + pixfast_more.go) — ≥ 15s under the gate — and the
	// 1024² arms still run in normal mode. Byte identity is deterministic
	// and size-independent — identical code paths at every size, no
	// size-dependent branching in kernel or generic — so the race run keeps
	// full coverage at ≤ 300×200 for the new types.
	moreSizes := sizes
	if raceEnabled {
		moreSizes = sizes[:len(sizes)-1]
	}
	runFixtureArms := func(w, h int, name string, src image.Image) {
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
	for _, sz := range sizes {
		w, h := sz[0], sz[1]
		for name, src := range map[string]image.Image{
			"rgba-opaque":  buildRGBA(w, h),
			"rgba-alpha":   buildRGBAAlpha(w, h),
			"nrgba-opaque": buildNRGBA(w, h),
			"nrgba-ramp":   buildNRGBARamp(w, h),
		} {
			runFixtureArms(w, h, name, src)
		}
	}
	for _, sz := range moreSizes {
		w, h := sz[0], sz[1]
		for name, src := range map[string]image.Image{
			"ycbcr420-ramp":     buildYCbCr(w, h, image.YCbCrSubsampleRatio420, ycbcrModeRamp),
			"ycbcr420-extreme":  buildYCbCr(w, h, image.YCbCrSubsampleRatio420, ycbcrModeExtreme),
			"ycbcr422-ramp":     buildYCbCr(w, h, image.YCbCrSubsampleRatio422, ycbcrModeRamp),
			"ycbcr422-extreme":  buildYCbCr(w, h, image.YCbCrSubsampleRatio422, ycbcrModeExtreme),
			"ycbcr444-ramp":     buildYCbCr(w, h, image.YCbCrSubsampleRatio444, ycbcrModeRamp),
			"ycbcr444-extreme":  buildYCbCr(w, h, image.YCbCrSubsampleRatio444, ycbcrModeExtreme),
			"gray-ramp":         buildGray(w, h),
			"gray-solid":        buildGraySolid(w, h),
			"paletted-opaque":   buildPaletted(w, h, palModeOpaque),
			"paletted-gradient": buildPaletted(w, h, palModeGradient),
			"paletted-nrgba":    buildPaletted(w, h, palModeNRGBA),
			"paletted-gray":     buildPaletted(w, h, palModeGray),
		} {
			runFixtureArms(w, h, name, src)
		}
	}

	// Empty source: same-instance no-op (sw==0 branch).
	t.Run("empty-source", func(t *testing.T) {
		scaleParity(t, ctx, empty, 32, 32)
	})

	// Sub-image arm (FR-4/C-2): nonzero Min with shared Stride, for RGBA,
	// NRGBA, YCbCr and Paletted. Sampling at b.Min+(x0,y0) must cancel Min
	// in PixOffset/YOffset and read the same bytes the generic At path reads;
	// the YCbCr 420 entry also exercises the non-canceling subsampled chroma
	// COffset form at odd Min (k = w/4).
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
		for name, sub := range map[string]image.Image{
			"rgba":   buildRGBA(w, h).SubImage(image.Rect(k, k, w, h)),
			"nrgba":  buildNRGBA(w, h).SubImage(image.Rect(k, k, w, h)),
			"nrgba2": buildNRGBARamp(w, h).SubImage(image.Rect(k, k, w, h)),
		} {
			t.Run(fmt.Sprintf("subimage-%dx%d/%s", w, h, name), func(t *testing.T) {
				scaleParity(t, ctx, sub, (subW+1)/2, (subH+1)/2)
				for o := 2; o <= 8; o++ {
					rotateParity(t, ctx, sub, o)
				}
			})
		}
	}
	for _, sz := range moreSizes {
		w, h := sz[0], sz[1]
		if w < 4 || h < 4 {
			continue
		}
		k := w / 4
		if k < 1 {
			k = 1
		}
		subW, subH := w-k, h-k
		for name, sub := range map[string]image.Image{
			"ycbcr420":          buildYCbCr(w, h, image.YCbCrSubsampleRatio420, ycbcrModeRamp).SubImage(image.Rect(k, k, w, h)),
			"paletted-gradient": buildPaletted(w, h, palModeGradient).SubImage(image.Rect(k, k, w, h)),
			"paletted-nrgba":    buildPaletted(w, h, palModeNRGBA).SubImage(image.Rect(k, k, w, h)),
		} {
			t.Run(fmt.Sprintf("subimage-%dx%d/%s", w, h, name), func(t *testing.T) {
				scaleParity(t, ctx, sub, (subW+1)/2, (subH+1)/2)
				for o := 2; o <= 8; o++ {
					rotateParity(t, ctx, sub, o)
				}
			})
		}
	}
}

// TestGenerateDownscaleAllocCeiling is T2, the machine-checkable alloc gate:
// per-pixel interface boxing must be eliminated on every codec shape. The
// fixtures are built OUTSIDE the measured closures (makePNG/makeJPEG
// themselves allocate — the benchmark's own pattern). Both arms share the
// 40000 threshold: the PNG arm measures ≈ 53 allocs/op on the RGBA fast
// path, and the JPEG arm ≈ 42–48 allocs/op post-kernels (decode 7 + encode
// 9 + ~26 scaffolding, measured this box) — ≈ 800× margin, and any
// regression to a second per-pixel boxing layer (12 allocs/pixel ≈ 786,470)
// trips it.

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

	// JPEG arm (AC-2): the fixture decodes to *image.YCbCr, which takes the
	// scaleYCbCr/rotateYCbCr kernels — the production shape for every JPEG
	// thumbnail. Decode-type self-check pins the fixture class (a fixture
	// that did not take the YCbCr path would pass any ceiling vacuously);
	// the 40000 threshold is shared with the PNG arm. Same raceEnabled skip.
	fixtureJPEG := makeJPEG(t, 1024, 1024)
	dec, _, err := image.Decode(bytes.NewReader(fixtureJPEG))
	if err != nil {
		t.Fatalf("jpeg fixture decode: %v", err)
	}
	if _, ok := dec.(*image.YCbCr); !ok {
		t.Fatalf("jpeg fixture decoded to %T, want *image.YCbCr", dec)
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
	if nJPEG >= 40000 {
		t.Fatalf("Generate JPEG downscale allocated %.0f objects/op, want < 40000 (pre-kernel baseline: 393,251)", nJPEG)
	}
}
