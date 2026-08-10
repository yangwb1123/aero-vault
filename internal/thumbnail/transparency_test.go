package thumbnail

// Transparency-compositing tests (direction: "Fix transparency rendering:
// thumbnails of PNG/GIF with alpha come out black/darkened"). All fixtures are
// uniform fills: a single pixel on a transparent background can fall in a
// bilinear sampling gap (probe-verified max red 0) and would make the test
// flaky by construction.

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"runtime"
	"strconv"
	"testing"
)

// uniformPNG builds a PNG of size w×h whose every pixel is c, encoded from an
// NRGBA image — exactly what decoding an 8-bit RGBA PNG yields.
func uniformPNG(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return pngEncodeBytes(t, img)
}

// pngEncodeBytes PNG-encodes img and returns the bytes.
func pngEncodeBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// samplePoints returns the four corners plus the center of img as 8-bit RGB
// triples. JPEG 4:2:0 chroma subsampling and DCT rounding justify sampling
// multiple points instead of a single pixel.
func samplePoints(img image.Image) [5][3]int {
	b := img.Bounds()
	xs := [5]int{b.Min.X, b.Max.X - 1, b.Min.X, b.Max.X - 1, b.Min.X + b.Dx()/2}
	ys := [5]int{b.Min.Y, b.Min.Y, b.Max.Y - 1, b.Max.Y - 1, b.Min.Y + b.Dy()/2}
	var out [5][3]int
	for i := range out {
		r, g, bl, _ := img.At(xs[i], ys[i]).RGBA()
		out[i] = [3]int{int(r >> 8), int(g >> 8), int(bl >> 8)}
	}
	return out
}

// assertWhite fails unless every sampled point is white within JPEG tolerance.
func assertWhite(t *testing.T, label string, img image.Image) {
	t.Helper()
	for i, p := range samplePoints(img) {
		if p[0] < 235 || p[1] < 235 || p[2] < 235 {
			t.Fatalf("%s sample %d: rgb=(%d,%d,%d), want all ≥ 235 (buggy baseline: black)",
				label, i, p[0], p[1], p[2])
		}
	}
}

func TestGenerateCompositesHalfTransparentRedOntoWhite(t *testing.T) {
	// Half-transparent red over white composites to pink (255,127,127); the
	// buggy baseline premultiplies onto black (127,0,0) — exactly half the
	// brightness. r>200 catches the darkening; g,b<200 catches a white-fill
	// bug where the background replaces rather than underlays the color.
	fixture := uniformPNG(t, 128, 128, color.NRGBA{255, 0, 0, 128})
	out, err := Generate(bytes.NewReader(fixture), 64, 64) // downscale path, ratio 0.5
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	img, format, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode thumbnail: %v", err)
	}
	if format != "jpeg" {
		t.Fatalf("expected jpeg, got %s", format)
	}
	b := img.Bounds()
	if b.Dx() != 64 || b.Dy() != 64 {
		t.Fatalf("expected 64x64, got %dx%d", b.Dx(), b.Dy())
	}
	r, g, bl, _ := img.At(b.Min.X+b.Dx()/2, b.Min.Y+b.Dy()/2).RGBA()
	if r>>8 <= 200 {
		t.Fatalf("center red = %d, want > 200 (buggy baseline: 127)", r>>8)
	}
	if g>>8 >= 200 || bl>>8 >= 200 {
		t.Fatalf("center green/blue = %d/%d, want < 200 (white-fill bug: 255)", g>>8, bl>>8)
	}
}

func TestGenerateCompositesTransparentOntoWhite(t *testing.T) {
	// Fully transparent source: every sampled pixel must be white (≥ 235),
	// never black. Both execution paths are exercised: ratio ≥ 1 (scale
	// returns the NRGBA source unchanged, hitting jpeg's generic encoder
	// path) and ratio < 1 (downscale).
	fixture := uniformPNG(t, 64, 64, color.NRGBA{0, 0, 0, 0})
	for _, dim := range []int{64, 32} {
		out, err := Generate(bytes.NewReader(fixture), dim, dim)
		if err != nil {
			t.Fatalf("generate(%d): %v", dim, err)
		}
		img, format, err := image.Decode(bytes.NewReader(out))
		if err != nil {
			t.Fatalf("decode thumbnail(%d): %v", dim, err)
		}
		if format != "jpeg" {
			t.Fatalf("expected jpeg, got %s", format)
		}
		assertWhite(t, "Generate("+strconv.Itoa(dim)+")", img)
	}
}

func TestGenerateOpaqueSourceOutputUnchanged(t *testing.T) {
	// R2 pin: for fully opaque sources compositeOnWhite must be a no-op and
	// output must be byte-identical to the pre-fix encoder path.
	fixture := uniformPNG(t, 64, 64, color.NRGBA{255, 0, 0, 255})
	decoded, _, err := image.Decode(bytes.NewReader(fixture))
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}

	// No-downscale path: reference is jpeg.Encode of the decoded source.
	var ref bytes.Buffer
	if err := jpeg.Encode(&ref, decoded, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatalf("encode reference: %v", err)
	}
	got, err := Generate(bytes.NewReader(fixture), 64, 64)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !bytes.Equal(got, ref.Bytes()) {
		t.Fatalf("opaque no-downscale output changed: %d vs %d bytes", len(got), ref.Len())
	}

	// Downscale path: reference is jpeg.Encode of scale's output.
	ref.Reset()
	if err := jpeg.Encode(&ref, scale(decoded, 32, 32), &jpeg.Options{Quality: quality}); err != nil {
		t.Fatalf("encode reference: %v", err)
	}
	got, err = Generate(bytes.NewReader(fixture), 32, 32)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !bytes.Equal(got, ref.Bytes()) {
		t.Fatalf("opaque downscale output changed: %d vs %d bytes", len(got), ref.Len())
	}
}

func TestGenerateCompositesTransparentGIF(t *testing.T) {
	// A paletted GIF whose palette contains a fully transparent entry models
	// the other format named in the problem statement. Go's gif encoder writes
	// a graphic control extension with the transparent index when a palette
	// entry has alpha 0; the decoder returns a *image.Paletted whose
	// Opaque() is false, so the composite path must apply.
	pal := color.Palette{
		color.RGBA{0, 0, 0, 0}, // transparent
		color.RGBA{255, 0, 0, 255},
	}
	img := image.NewPaletted(image.Rect(0, 0, 64, 64), pal)
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.SetColorIndex(x, y, 0) // transparent index everywhere
		}
	}
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode gif: %v", err)
	}
	out, err := Generate(bytes.NewReader(buf.Bytes()), 64, 64)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	got, format, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode thumbnail: %v", err)
	}
	if format != "jpeg" {
		t.Fatalf("expected jpeg, got %s", format)
	}
	assertWhite(t, "gif", got)
}

func TestGenerateCompositesTransparentNRGBA64(t *testing.T) {
	// 16-bit source (PNG depth 16 decodes to *image.NRGBA64) through the same
	// white-composite assertions as the 8-bit path.
	img := image.NewNRGBA64(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.NRGBA64{0, 0, 0, 0})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	out, err := Generate(bytes.NewReader(buf.Bytes()), 64, 64)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	got, _, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode thumbnail: %v", err)
	}
	assertWhite(t, "nrgba64", got)
}

func TestGenerateCompositesHalfTransparentNoDownscale(t *testing.T) {
	// C3 pin: the ratio≥1 fractional-alpha path (small-logo shape). The
	// source is already within the output bounds, so scale returns it
	// unchanged and compositeOnWhite runs on the undownscaled NRGBA.
	// Half-transparent red over white composites to pink (255,127,127).
	fixture := uniformPNG(t, 64, 64, color.NRGBA{255, 0, 0, 128})
	out, err := Generate(bytes.NewReader(fixture), 64, 64)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	img, format, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode thumbnail: %v", err)
	}
	if format != "jpeg" {
		t.Fatalf("expected jpeg, got %s", format)
	}
	b := img.Bounds()
	if b.Dx() != 64 || b.Dy() != 64 {
		t.Fatalf("expected 64x64, got %dx%d", b.Dx(), b.Dy())
	}
	r, g, bl, _ := img.At(b.Min.X+b.Dx()/2, b.Min.Y+b.Dy()/2).RGBA()
	if r>>8 <= 200 {
		t.Fatalf("center red = %d, want > 200 (buggy baseline: 127)", r>>8)
	}
	if g>>8 >= 200 || bl>>8 >= 200 {
		t.Fatalf("center green/blue = %d/%d, want < 200 (white-fill bug: 255)", g>>8, bl>>8)
	}
}

func TestCompositeOnWhiteNonZeroOrigin(t *testing.T) {
	// C5 pin: compositeOnWhite must respect a non-zero Min origin — both the
	// uniform fill and the source draw are anchored at the source bounds.
	src := image.NewNRGBA(image.Rect(10, 20, 74, 84)) // 64×64 at origin (10,20)
	for y := 20; y < 84; y++ {
		for x := 10; x < 74; x++ {
			src.Set(x, y, color.NRGBA{255, 0, 0, 128})
		}
	}
	out := compositeOnWhite(src)
	b := out.Bounds()
	if b.Min.X != 10 || b.Min.Y != 20 || b.Dx() != 64 || b.Dy() != 64 {
		t.Fatalf("bounds = %v, want origin (10,20) 64x64", b)
	}
	// A freshly filled RGBA is zero-initialized (black, alpha 0); the white
	// fill must cover the full source rect, and the composite must leave the
	// white underneath the half-transparent red.
	for _, p := range []image.Point{{37, 52}, {10, 20}, {73, 83}} {
		r, g, bl, a := out.At(p.X, p.Y).RGBA()
		if a>>8 != 255 {
			t.Fatalf("point %v alpha = %d, want 255 (opaque output)", p, a>>8)
		}
		if r>>8 <= 200 || g>>8 >= 200 || bl>>8 >= 200 {
			t.Fatalf("point %v = (%d,%d,%d), want pink ~(255,127,127)", p, r>>8, g>>8, bl>>8)
		}
	}
	// SubImage with non-zero offset must survive the composite unchanged in
	// bounds and pixels.
	sub := src.SubImage(image.Rect(20, 30, 60, 70))
	out2 := compositeOnWhite(sub)
	b2 := out2.Bounds()
	if b2.Min.X != 20 || b2.Min.Y != 30 || b2.Dx() != 40 || b2.Dy() != 40 {
		t.Fatalf("subimage bounds = %v, want origin (20,30) 40x40", b2)
	}
}

func TestGenerateCompositeAllocationBounded(t *testing.T) {
	// C4 pin: the composite path must not buffer or copy the payload — total
	// allocation for a small transparent source stays O(w×h) (decode buffer
	// + one composite output RGBA + jpeg encoder), far below a 1 MiB ceiling.
	// Red on a path that buffers the payload or allocates per-channel copies.
	// (A large source is deliberately avoided: decoding a 512² PNG legitimately
	// allocates its pixel buffer, which would dominate the measurement.)
	payload := uniformPNG(t, 64, 64, color.NRGBA{255, 0, 0, 128})
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	out, err := Generate(bytes.NewReader(payload), 64, 64)
	runtime.ReadMemStats(&m1)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if d := m1.TotalAlloc - m0.TotalAlloc; d > 1<<20 {
		t.Fatalf("allocated %d bytes, want ≤ 1 MiB (payload buffered?)", d)
	}
	img, _, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode thumbnail: %v", err)
	}
	r, g, bl, _ := img.At(32, 32).RGBA()
	if r>>8 <= 200 || g>>8 >= 200 || bl>>8 >= 200 {
		t.Fatalf("center = (%d,%d,%d), want pink ~(255,127,127)", r>>8, g>>8, bl>>8)
	}
}
