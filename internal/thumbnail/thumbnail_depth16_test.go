package thumbnail

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"runtime"
	"testing"
)

// TestGenerateRejectsOversized16BitPNG is acceptance AC2+AC4: a depth-16 PNG
// at 8192×8192 (8 B/px decode class, stdlib image/png) rejects with the
// dimension sentinel from the header alone — before any pixel buffer is
// allocated and before the poisoned payload is read. The bit-depth-16 cap is
// format-class: gray+alpha (color type 4) is rejected the same way.
func TestGenerateRejectsOversized16BitPNG(t *testing.T) {
	// Fixture self-check: DecodeConfig must report exactly the declared dims
	// (a header-only depth-16 PNG is a valid config source).
	fix := headerOnlyPNG(t, 8192, 8192, 16, 6)
	cfg, format, err := image.DecodeConfig(bytes.NewReader(fix))
	if err != nil || format != "png" || cfg.Width != 8192 || cfg.Height != 8192 {
		t.Fatalf("fixture self-check: got %dx%d %q err=%v", cfg.Width, cfg.Height, format, err)
	}

	// (a) Depth-16 8192² + poisoned payload: rejected with the dimension
	// sentinel, nil output, bounded reads (the 16 MiB junk payload is never
	// consumed — the rejection precedes image.Decode).
	junk := append(append([]byte{}, fix...), make([]byte, 16<<20)...)
	cnt := &countingReader{r: bytes.NewReader(junk)}
	img, err := Generate(cnt, 100, 100)
	if img != nil || !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("expected ErrImageTooLarge with nil payload, got img!=nil=%v err=%v", img != nil, err)
	}
	if cnt.n > MaxMetadataBytes+64<<10 {
		t.Fatalf("read %d bytes, want ≤ %d", cnt.n, MaxMetadataBytes+64<<10)
	}

	// (b) No payload buffering: total allocated bytes stay below the 16 MiB
	// junk payload (the fixed-path allocation is only the ≤ 33-byte head).
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

	// (c) AC4: gray+alpha (color type 4, depth 16 — decodes to
	// *image.NRGBA64) is also rejected: the cap is format-class (bit depth
	// 16 regardless of color type).
	ga := headerOnlyPNG(t, 8192, 8192, 16, 4)
	cnt3 := &countingReader{r: bytes.NewReader(ga)}
	img, err = Generate(cnt3, 100, 100)
	if img != nil || !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("gray+alpha: expected ErrImageTooLarge, got img!=nil=%v err=%v", img != nil, err)
	}
	if cnt3.n > MaxMetadataBytes+64<<10 {
		t.Fatalf("gray+alpha: read %d bytes, want ≤ %d", cnt3.n, MaxMetadataBytes+64<<10)
	}
}

// realDepth16PNG builds a genuinely stdlib-decodable depth-16 PNG (color
// type 6, depth 16) of uniform color via png.Encode — direct Pix writes,
// not per-pixel Set loops (16.7 MP at 4096²). Alpha is translucent
// (0x8000), not opaque: the stdlib encoder drops alpha to color type 2
// (cbTC16) for opaque NRGBA64 sources, and DecodeConfig would then report
// RGBA64Model — the engagement guard pins the 8 B/px NRGBA64 class, so the
// fixture must stay on the cbTCA16 path.
func realDepth16PNG(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA64(image.Rect(0, 0, w, h))
	for i := 0; i+8 <= len(img.Pix); i += 8 {
		img.Pix[i], img.Pix[i+1] = 0x10, 0x20   // R = 0x1020
		img.Pix[i+2], img.Pix[i+3] = 0x30, 0x40 // G = 0x3040
		img.Pix[i+4], img.Pix[i+5] = 0x50, 0x60 // B = 0x5060
		img.Pix[i+6], img.Pix[i+7] = 0x80, 0x00 // A = 0x8000 (translucent)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode depth-16 png: %v", err)
	}
	return buf.Bytes()
}

// TestGenerate16BitPNGWithinBound is acceptance AC3: a real depth-16 PNG at
// the cap still thumbnails, and the strict-`>` boundary pins hold on both
// axes (exactly Max16BitSourceDim accepted; one pixel over rejected).
func TestGenerate16BitPNGWithinBound(t *testing.T) {
	// (a) Real depth-16 PNG at the cap → valid JPEG thumbnail ≤ bounds;
	// the config self-check also pins the 8 B/px class (NRGBA64 model).
	png16 := realDepth16PNG(t, Max16BitSourceDim, Max16BitSourceDim)
	cfg, format, err := image.DecodeConfig(bytes.NewReader(png16))
	if err != nil || format != "png" || cfg.Width != Max16BitSourceDim || cfg.Height != Max16BitSourceDim {
		t.Fatalf("fixture self-check: got %dx%d %q err=%v", cfg.Width, cfg.Height, format, err)
	}
	if cfg.ColorModel != color.NRGBA64Model {
		t.Fatalf("depth-16 fixture decoded to %v, want NRGBA64Model (8 B/px class)", cfg.ColorModel)
	}
	out, err := Generate(bytes.NewReader(png16), 256, 256)
	if err != nil {
		t.Fatalf("generate depth-16 %dx%d: %v", Max16BitSourceDim, Max16BitSourceDim, err)
	}
	img, format, err := image.Decode(bytes.NewReader(out))
	if err != nil || format != "jpeg" {
		t.Fatalf("thumbnail not a decodable jpeg: format=%q err=%v", format, err)
	}
	if img.Bounds().Dx() > 256 || img.Bounds().Dy() > 256 {
		t.Fatalf("thumbnail exceeds bounds: %s", img.Bounds())
	}

	// (b) Boundary pins: exactly Max16BitSourceDim is accepted (falls
	// through to ErrUnsupported — no IDAT); one pixel over on either axis
	// is rejected.
	if _, err := Generate(bytes.NewReader(headerOnlyPNG(t, Max16BitSourceDim, Max16BitSourceDim, 16, 6)), 100, 100); errors.Is(err, ErrImageTooLarge) {
		t.Fatal("depth-16 at exactly Max16BitSourceDim must not be rejected")
	}
	if _, err := Generate(bytes.NewReader(headerOnlyPNG(t, Max16BitSourceDim+1, 1024, 16, 6)), 100, 100); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("depth-16 above Max16BitSourceDim: expected ErrImageTooLarge, got %v", err)
	}
	if _, err := Generate(bytes.NewReader(headerOnlyPNG(t, 1024, Max16BitSourceDim+1, 16, 6)), 100, 100); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("depth-16 height above Max16BitSourceDim: expected ErrImageTooLarge, got %v", err)
	}
}

// TestPNGBitDepth pins the defensive helper contract (AC8): the IHDR
// bit-depth byte at offset 24 is returned for parseable heads, and any head
// shorter than 25 bytes defaults to 8 — a malformed or truncated head must
// never add a rejection.
func TestPNGBitDepth(t *testing.T) {
	if got := pngBitDepth(headerOnlyPNG(t, 8, 8, 16, 6)); got != 16 {
		t.Fatalf("depth-16 fixture: pngBitDepth = %d, want 16", got)
	}
	if got := pngBitDepth(headerOnlyPNG(t, 8, 8, 8, 6)); got != 8 {
		t.Fatalf("depth-8 fixture: pngBitDepth = %d, want 8", got)
	}
	if got := pngBitDepth([]byte{0x89, 'P', 'N', 'G'}); got != 8 {
		t.Fatalf("truncated head: pngBitDepth = %d, want 8 (default must never add a rejection)", got)
	}
	fix := headerOnlyPNG(t, 8, 8, 16, 6)
	if got := pngBitDepth(fix[:24]); got != 8 {
		t.Fatalf("24-byte prefix (offset-24 byte absent): pngBitDepth = %d, want 8", got)
	}
}

// TestGenerate16BitColorTypeArms pins the format-class rule (qa F2 / sec F3):
// the depth-16 cap keys on the IHDR bit-depth byte, NOT the color type — a
// 16-bit gray source (color type 0, 2 B/px) is conservatively over-rejected
// above Max16BitSourceDim, closing the gray16+tRNS regression hole (the
// stdlib decodes gray16+tRNS to *image.NRGBA64 at 8 B/px, the full class
// cost). Color type 2 (truecolor 16-bit, 6 B/px → RGBA64 at 8 B/px) is the
// main class and must reject too; both accept exactly at the cap.
func TestGenerate16BitColorTypeArms(t *testing.T) {
	for _, ct := range []struct {
		colorType byte
		name      string
	}{
		{0, "gray16 (2 B/px, conservative over-rejection)"},
		{2, "truecolor16 (6 B/px)"},
		{4, "grayalpha16 (4 B/px)"},
	} {
		t.Run(ct.name, func(t *testing.T) {
			// Above the cap: rejected from the header.
			big := headerOnlyPNG(t, Max16BitSourceDim+1, 1024, 16, ct.colorType)
			if _, err := Generate(bytes.NewReader(big), 100, 100); !errors.Is(err, ErrImageTooLarge) {
				t.Fatalf("color type %d above cap: expected ErrImageTooLarge, got %v", ct.colorType, err)
			}
			// Exactly at the cap: accepted through the depth-16 gate (the
			// header-only fixture then fails at decode → ErrUnsupported).
			at := headerOnlyPNG(t, Max16BitSourceDim, Max16BitSourceDim, 16, ct.colorType)
			if _, err := Generate(bytes.NewReader(at), 100, 100); errors.Is(err, ErrImageTooLarge) {
				t.Fatalf("color type %d at cap: must not be rejected as ImageTooLarge", ct.colorType)
			}
			// The 8-bit same-color-type control is unaffected by the cap.
			c8 := headerOnlyPNG(t, 8192, 8192, 8, ct.colorType)
			if _, err := Generate(bytes.NewReader(c8), 100, 100); errors.Is(err, ErrImageTooLarge) {
				t.Fatalf("8-bit color type %d at MaxSourceDim: must not be rejected", ct.colorType)
			}
		})
	}
}

// realOpaqueDepth16PNG builds a genuinely stdlib-decodable depth-16 PNG of
// the OPAQUE class: color type 2 (cbTC16) — the stdlib encoder drops alpha
// to type 2 for opaque NRGBA64 sources, and DecodeConfig reports
// RGBA64Model. The RGBA64 generate-level alloc arm's self-check.
func realOpaqueDepth16PNG(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA64(image.Rect(0, 0, w, h))
	for i := 0; i+8 <= len(img.Pix); i += 8 {
		img.Pix[i], img.Pix[i+1] = 0x10, 0x20   // R = 0x1020
		img.Pix[i+2], img.Pix[i+3] = 0x30, 0x40 // G = 0x3040
		img.Pix[i+4], img.Pix[i+5] = 0x50, 0x60 // B = 0x5060
		img.Pix[i+6], img.Pix[i+7] = 0xFF, 0xFF // A = opaque → cbTC16 → RGBA64
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode opaque depth-16 png: %v", err)
	}
	return buf.Bytes()
}

// realGray16PNG builds a genuinely stdlib-decodable depth-16 gray PNG (color
// type 0) — the Gray16 generate-level alloc arm's fixture.
func realGray16PNG(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewGray16(image.Rect(0, 0, w, h))
	for i := 0; i+2 <= len(img.Pix); i += 2 {
		img.Pix[i], img.Pix[i+1] = 0x12, 0x34 // Y = 0x1234
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode gray16 png: %v", err)
	}
	return buf.Bytes()
}
