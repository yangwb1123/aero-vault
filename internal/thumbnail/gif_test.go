package thumbnail

// GIF decode-pipeline test coverage (direction: "GIF decode-pipeline test
// coverage (palette transparency composite, animated first-frame policy,
// dimension cap)"). GIF is a first-class supported input format (FormatGIF /
// AdmitByMagic admission, image/gif side-effect import, REST declared-type
// gate case "image/gif"), yet the suite exercised it only at classification
// level: makeGIF (sniff_test.go) feeds only Sniff, and gifConfigPrefix
// (context_test.go) is a config-scan blocking probe that produces no output
// bytes. Under the suite's byte-identity discipline, a silent output change
// on the GIF path — a stdlib image/gif decoder behavior shift, or a
// compositeOnWhite/scale change affecting paletted sources — would go
// undetected.
//
// These tests pin the three unpinned GIF behaviors: (R1) palette transparency
// → white composite at pixel precision (no downscale, so no bilinear gap —
// the tolerance-sampling rationale of TestGenerateCompositesTransparentGIF
// does not apply), (R2) the first-frame policy of animated GIFs plus
// output determinism, (R3) the format-class dimension gate (GIF is subject
// only to MaxSourceDim; Max16BitSourceDim/MaxProgressiveSourceDim are
// PNG/JPEG-class), and (R4) GIF seeds for FuzzGenerate. No production code
// changes; a regression these tests expose is a follow-up, not this
// direction.

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"runtime"
	"testing"
)

// headerOnlyGIF builds the 13-byte GIF89a Logical Screen Descriptor with no
// global color table (packed 0x00) and no image data. image/gif's
// DecodeConfig returns the LSD dims after exactly these 13 bytes
// (readHeaderAndScreenDescriptor; the configOnly path returns immediately,
// no image descriptor/data required), so it is the GIF analogue of
// headerOnlyPNG: a dims-only probe that never allocates a pixel buffer.
// Full Decode then fails → ErrUnsupported. Dims are uint16 (GIF's
// width/height fields are 16-bit LE), which is why the over-cap arm can
// declare 65535 (> MaxSourceDim 8192).
func headerOnlyGIF(t testing.TB, w, h uint16) []byte {
	t.Helper()
	return []byte{
		'G', 'I', 'F', '8', '9', 'a', // signature
		byte(w), byte(w >> 8), // width (LE)
		byte(h), byte(h >> 8), // height (LE)
		0x00, // packed: no global color table, no interlace
		0x00, // background color index
		0x00, // pixel aspect ratio
	}
}

// makeTransparent1x1GIF builds the R1 fixture: a 1×1 Paletted whose palette
// has exactly two entries — index 0 transparent (0,0,0,0), index 1 opaque
// red (255,0,0,255) — with the pixel set to the transparent index. The
// opaque entry is load-bearing: it pins that a *single* transparent entry
// flips Opaque() == false (the compositeOnWhite fast-path skip is
// all-or-nothing).
func makeTransparent1x1GIF(t testing.TB) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{
		color.RGBA{0, 0, 0, 0},     // transparent
		color.RGBA{255, 0, 0, 255}, // opaque red
	})
	img.SetColorIndex(0, 0, 0) // the transparent index
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode gif: %v", err)
	}
	return buf.Bytes()
}

// makeAnimatedGIF builds the R2 fixture: two 64×64 frames, frame 0 all red,
// frame 1 all blue, encoded with gif.EncodeAll (Delay 10,10). Frames are
// 1-entry palettes filled via direct Pix writes (gif.EncodeAll pads a
// 1-entry table to 2 entries with black; the fill index 0 everywhere keeps
// content pure — decoded frame 0 is red, frame 1 blue).
func makeAnimatedGIF(t testing.TB) []byte {
	t.Helper()
	frame := func(c color.Color) *image.Paletted {
		img := image.NewPaletted(image.Rect(0, 0, 64, 64), color.Palette{c})
		for i := range img.Pix {
			img.Pix[i] = 0 // palette index 0 everywhere
		}
		return img
	}
	anim := &gif.GIF{
		Image: []*image.Paletted{
			frame(color.RGBA{255, 0, 0, 255}),
			frame(color.RGBA{0, 0, 255, 255}),
		},
		Delay: []int{10, 10},
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, anim); err != nil {
		t.Fatalf("encode animated gif: %v", err)
	}
	return buf.Bytes()
}

// makeWideGIF builds a decodable w×h GIF with a two-tone opaque palette,
// filled via direct Pix writes (index = (x+y)&1). Used by R3(d) at
// 4097×16: a genuinely decodable source whose long side exceeds
// Max16BitSourceDim, proving the lower cap is PNG-class and never applies
// to GIF.
func makeWideGIF(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, w, h), color.Palette{
		color.RGBA{255, 0, 0, 255},
		color.RGBA{0, 0, 255, 255},
	})
	for y := 0; y < h; y++ {
		base := y * img.Stride
		for x := 0; x < w; x++ {
			img.Pix[base+x] = uint8((x + y) & 1)
		}
	}
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode wide gif: %v", err)
	}
	return buf.Bytes()
}

func TestGenerateGIF(t *testing.T) {
	// R1: 1×1 GIF with transparent + opaque palette entries composites the
	// transparent pixel onto white. At 1×1 there is no downscale (scale's
	// ratio = min(64/1, 64/1) = 64 ≥ 1 → returned unchanged), so no bilinear
	// sampling gap exists and the pixel is composited and JPEG-encoded
	// directly — pixel-precise, the complement to
	// TestGenerateCompositesTransparentGIF's tolerance-based 64×64 pin.
	src := makeTransparent1x1GIF(t)

	// Fixture self-check: decodes as GIF at 1×1, and Opaque() is false — the
	// single transparent entry flips it despite the opaque entry (pins the
	// load-bearing role of both palette entries).
	cfg, format, err := image.DecodeConfig(bytes.NewReader(src))
	if err != nil || format != "gif" || cfg.Width != 1 || cfg.Height != 1 {
		t.Fatalf("fixture self-check: got %dx%d %q err=%v", cfg.Width, cfg.Height, format, err)
	}
	decoded, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		t.Fatalf("fixture self-check decode: %v", err)
	}
	if pal, ok := decoded.(*image.Paletted); !ok || pal.Opaque() {
		t.Fatalf("fixture self-check: Opaque() must be false for a transparent palette entry (got %T)", decoded)
	}

	out, err := Generate(bytes.NewReader(src), 64, 64)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	thumb, format, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode thumbnail: %v", err)
	}
	if format != "jpeg" {
		t.Fatalf("expected jpeg, got %s", format)
	}
	b := thumb.Bounds()
	if b.Dx() != 1 || b.Dy() != 1 {
		t.Fatalf("expected 1x1 thumbnail, got %dx%d", b.Dx(), b.Dy())
	}
	// Pixel compare: transparent → white (≥ 235/channel); the buggy
	// black-composite baseline would render (0,0,0).
	r, g, bl, _ := thumb.At(0, 0).RGBA()
	if r>>8 < 235 || g>>8 < 235 || bl>>8 < 235 {
		t.Fatalf("pixel (0,0) = (%d,%d,%d), want all ≥ 235 (white composite; buggy baseline: black)", r>>8, g>>8, bl>>8)
	}
}

func TestGenerateAnimatedGIF(t *testing.T) {
	// R2: image/gif's Decode returns d.image[0] only (Go 1.26 reader.go:570;
	// the decode loop stops after the first frame when !keepAllFrames &&
	// len(d.image) == 1), so Generate thumbnails frame 0 and ignores later
	// frames. Pinned against a hypothetical last-frame or averaged output:
	// frame 0 is red, frame 1 blue, so a blue (r=0) or averaged
	// (127,0,127) result both fail the center assertions.
	src := makeAnimatedGIF(t)

	// Fixture self-check: genuinely animated — exactly 2 frames.
	anim, err := gif.DecodeAll(bytes.NewReader(src))
	if err != nil {
		t.Fatalf("fixture self-check decode all: %v", err)
	}
	if len(anim.Image) != 2 {
		t.Fatalf("fixture self-check: got %d frames, want 2", len(anim.Image))
	}

	out, err := Generate(bytes.NewReader(src), 64, 64)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	thumb, format, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode thumbnail: %v", err)
	}
	if format != "jpeg" {
		t.Fatalf("expected jpeg, got %s", format)
	}
	r, g, bl, _ := thumb.At(32, 32).RGBA()
	if r>>8 <= 200 || bl>>8 >= 100 {
		t.Fatalf("center = (%d,%d,%d), want frame-0 red-dominant (r>>8 > 200, b>>8 < 100)", r>>8, g>>8, bl>>8)
	}

	// Determinism pin: identical input bytes → identical output bytes across
	// in-process repeated runs (the suite's determinism proxy).
	out2, err := Generate(bytes.NewReader(src), 64, 64)
	if err != nil {
		t.Fatalf("generate (repeat): %v", err)
	}
	if !bytes.Equal(out, out2) {
		t.Fatal("animated GIF output is not deterministic across repeated Generate calls")
	}
}

func TestGenerateGIFAtDimensionCap(t *testing.T) {
	// R3: the dimension gate is format-class. GIF is subject only to
	// MaxSourceDim (8192) — the lower Max16BitSourceDim (4096,
	// format=="png" && depth-16) and MaxProgressiveSourceDim (4096,
	// format=="jpeg") caps do not apply — and GIF dims are uint16 (max
	// 65535 > 8192), so an over-cap declaration is constructible.

	// Fixture self-check: the header-only builder is a valid config source
	// reporting exactly the declared dims.
	cfg, format, err := image.DecodeConfig(bytes.NewReader(headerOnlyGIF(t, 4097, 16)))
	if err != nil || format != "gif" || cfg.Width != 4097 || cfg.Height != 16 {
		t.Fatalf("fixture self-check: got %dx%d %q err=%v", cfg.Width, cfg.Height, format, err)
	}

	// (a) Over cap, rejected before pixel allocation: 65535² declared dims
	// followed by 16 MiB of junk. The rejection precedes image.Decode, so
	// the junk is never consumed — bounded reads (gif DecodeConfig's bufio
	// read-ahead is 4096) and no payload buffering.
	junk := append(append([]byte{}, headerOnlyGIF(t, 65535, 65535)...), make([]byte, 16<<20)...)
	cnt := &countingReader{r: bytes.NewReader(junk)}
	img, err := Generate(cnt, 100, 100)
	if img != nil || !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("expected ErrImageTooLarge with nil payload, got img!=nil=%v err=%v", img != nil, err)
	}
	if cnt.n > 4096 {
		t.Fatalf("read %d bytes, want ≤ 4096 (payload never read)", cnt.n)
	}
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	img, err = Generate(bytes.NewReader(junk), 100, 100)
	runtime.ReadMemStats(&m1)
	if img != nil || !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("alloc arm: expected ErrImageTooLarge, got img!=nil=%v err=%v", img != nil, err)
	}
	if d := m1.TotalAlloc - m0.TotalAlloc; d > uint64(len(junk)) {
		t.Fatalf("allocated %d bytes for %d-byte input: payload buffered", d, len(junk))
	}

	// (b) Exact boundary: 8192² is admitted through the strict-`>` gate and
	// falls through to decode → ErrUnsupported (no image data), not
	// ErrImageTooLarge.
	img, err = Generate(bytes.NewReader(headerOnlyGIF(t, 8192, 8192)), 100, 100)
	if img != nil || errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("GIF at exactly MaxSourceDim must not be rejected as too large (img!=nil=%v err=%v)", img != nil, err)
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("GIF at exactly MaxSourceDim: want ErrUnsupported (no image data), got %v", err)
	}

	// (c) PNG-class cap not applied to GIF: one side above Max16BitSourceDim
	// passes the gate (Max16BitSourceDim is format=="png" && depth-16-gated;
	// MaxProgressiveSourceDim is format=="jpeg"-gated; neither applies) →
	// ErrUnsupported from the missing image data, never ErrImageTooLarge.
	img, err = Generate(bytes.NewReader(headerOnlyGIF(t, 4097, 16)), 100, 100)
	if img != nil || errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("GIF with a side in (4096, 8192] must not be rejected as too large (img!=nil=%v err=%v)", img != nil, err)
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("GIF 4097x16 header-only: want ErrUnsupported, got %v", err)
	}

	// (d) End-to-end gate admission: a genuinely decodable 4097×16 GIF
	// thumbnails to a valid JPEG ≤ 64×64 — admission is not merely
	// "not rejected" but fully thumbnailable at the 4097 side.
	out, err := Generate(bytes.NewReader(makeWideGIF(t, 4097, 16)), 64, 64)
	if err != nil {
		t.Fatalf("generate decodable 4097x16 gif: %v", err)
	}
	thumb, format, err := image.Decode(bytes.NewReader(out))
	if err != nil || format != "jpeg" {
		t.Fatalf("thumbnail not a decodable jpeg: format=%q err=%v", format, err)
	}
	if thumb.Bounds().Dx() > 64 || thumb.Bounds().Dy() > 64 {
		t.Fatalf("thumbnail exceeds bounds: %s", thumb.Bounds())
	}
}

// makeTransparentGIF builds a uniform fully-transparent w×h GIF (single
// palette entry, transparent index everywhere) — the F1 downscale fixture.
func makeTransparentGIF(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, w, h), color.Palette{
		color.RGBA{0, 0, 0, 0}, // transparent
	})
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetColorIndex(x, y, 0)
		}
	}
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode gif: %v", err)
	}
	return buf.Bytes()
}

// TestGenerateTransparentGIFDownscale pins F1 (P1): a fully-transparent
// paletted GIF driven through the bilinear DOWNSCALE path (ratio < 1) must
// composite onto white — the downscaled buffer samples only transparent
// pixels, and the output must be white, never black (premultiplied-at-0
// rendering) or red.
func TestGenerateTransparentGIFDownscale(t *testing.T) {
	fix := makeTransparentGIF(t, 128, 128)
	decoded, format, err := image.Decode(bytes.NewReader(fix))
	if err != nil || format != "gif" {
		t.Fatalf("fixture self-check: %v fmt=%q", err, format)
	}
	if o, ok := decoded.(interface{ Opaque() bool }); !ok || o.Opaque() {
		t.Fatal("fixture must be transparent (Opaque() == false)")
	}
	out, err := Generate(bytes.NewReader(fix), 64, 64) // ratio < 1: real downscale
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	thumb, _, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode thumbnail: %v", err)
	}
	// Every sampled pixel is fully transparent → composited onto white.
	for _, p := range []image.Point{{0, 0}, {32, 32}, {63, 63}, {10, 50}} {
		r, g, b, _ := thumb.At(p.X, p.Y).RGBA()
		if r>>8 < 235 || g>>8 < 235 || b>>8 < 235 {
			t.Fatalf("downscaled transparent GIF at %v = (%d,%d,%d), want white (≥235)", p, r>>8, g>>8, b>>8)
		}
	}
}

// TestGenerateGIFOneSideOverDim pins F3 (P2): the dimension gate's per-side
// `||` semantics — one side over MaxSourceDim (65535, uint16 max) with the
// other under (16) must still reject with ErrImageTooLarge, before any
// pixel allocation.
func TestGenerateGIFOneSideOverDim(t *testing.T) {
	junk := append(append([]byte{}, headerOnlyGIF(t, 65535, 16)...), make([]byte, 16<<20)...)
	cnt := &countingReader{r: bytes.NewReader(junk)}
	img, err := Generate(cnt, 100, 100)
	if img != nil || !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("65535x16: expected ErrImageTooLarge, got img!=nil=%v err=%v", img != nil, err)
	}
	if cnt.n > 4096 {
		t.Fatalf("read %d bytes, want ≤ 4096 (payload never read)", cnt.n)
	}
	// The mirrored axis: 16×65535.
	img, err = Generate(bytes.NewReader(headerOnlyGIF(t, 16, 65535)), 100, 100)
	if img != nil || !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("16x65535: expected ErrImageTooLarge, got img!=nil=%v err=%v", img != nil, err)
	}
}
