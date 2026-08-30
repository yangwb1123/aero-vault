package thumbnail

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"testing"
)

// AC-1: a 400×200 red-top/blue-bottom PNG with an eXIf chunk declaring
// orientation 6 must thumbnail to exactly 64×128 (the swapped box), upright
// (blue left, red right) — 90° CW, the exact swap shape the JPEG twin pins
// for the same fixture. The absent-EXIF twin stays 256×128 red-top/blue-bottom.
func TestGenerateAppliesPNGeXIfOrientation6(t *testing.T) {
	fixture := orientedPNG(t, 400, 200, 6, false)
	out, err := Generate(bytes.NewReader(fixture), 256, 128)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	img, format, err := image.Decode(bytes.NewReader(out))
	if err != nil || format != "jpeg" {
		t.Fatalf("decode thumb: %v fmt=%s", err, format)
	}
	if img.Bounds().Dx() != 64 || img.Bounds().Dy() != 128 {
		t.Fatalf("thumb dims %dx%d want 64x128 (swapped box)", img.Bounds().Dx(), img.Bounds().Dy())
	}
	// Orientation 6 = rotate 90° CW: out(r,c) = in(H-1-c, r). The scaled
	// source is 128×64 (red rows 0–31, blue 32–63), so the output's left
	// half is blue and the right half red, boundary at c=32 — the JPEG
	// twin's exact sample points.
	for _, c := range []int{0, 8, 16, 24, 31} {
		assertThumbColor(t, img, c, 64, "blue")
	}
	for _, c := range []int{48, 56, 60, 63} {
		assertThumbColor(t, img, c, 64, "red")
	}

	// Absent-EXIF twin: no rotation, no box swap — 256×128, red on top.
	plain := orientedPNG(t, 400, 200, 0, false)
	outPlain, err := Generate(bytes.NewReader(plain), 256, 128)
	if err != nil {
		t.Fatalf("Generate plain: %v", err)
	}
	imgPlain, format, err := image.Decode(bytes.NewReader(outPlain))
	if err != nil || format != "jpeg" {
		t.Fatalf("decode plain thumb: %v fmt=%s", err, format)
	}
	if imgPlain.Bounds().Dx() != 256 || imgPlain.Bounds().Dy() != 128 {
		t.Fatalf("plain thumb dims %dx%d want 256x128", imgPlain.Bounds().Dx(), imgPlain.Bounds().Dy())
	}
	assertThumbColor(t, imgPlain, 32, 32, "red")
	assertThumbColor(t, imgPlain, 32, 96, "blue")
}

// AC-2: orientation-1 eXIf must be a true no-op — byte-identical to the
// absent-EXIF output (exactly the JPEG twin's contract).
func TestGeneratePNGeXIfOrientation1OutputUnchanged(t *testing.T) {
	plain := orientedPNG(t, 64, 64, 0, false)
	ori1 := orientedPNG(t, 64, 64, 1, false)
	gotPlain, err := Generate(bytes.NewReader(plain), 64, 64)
	if err != nil {
		t.Fatalf("Generate plain: %v", err)
	}
	gotOri1, err := Generate(bytes.NewReader(ori1), 64, 64)
	if err != nil {
		t.Fatalf("Generate ori1: %v", err)
	}
	if !bytes.Equal(gotPlain, gotOri1) {
		t.Fatalf("orientation-1 output differs from absent-EXIF output (%d vs %d bytes)", len(gotPlain), len(gotOri1))
	}
}

// AC-5a: a small valid PNG with > 8 MiB of pre-IDAT tEXt structure must abort
// with ErrMetadataTooLarge and bounded reads, before image.Decode is reached
// (nil payload — no pixel allocation), the PNG-parallel of the JPEG APP1
// flood gate.
func TestGeneratePNGRejectsOversizedMetadata(t *testing.T) {
	payload := pngTextPaddedPNG(t, MaxMetadataBytes+1<<20)
	cnt := &countingReader{r: bytes.NewReader(payload)}
	img, err := Generate(cnt, 256, 128)
	if img != nil || !errors.Is(err, ErrMetadataTooLarge) {
		t.Fatalf("expected ErrMetadataTooLarge with nil payload, got img!=nil=%v err=%v", img != nil, err)
	}
	if cnt.n > MaxMetadataBytes+64<<10 {
		t.Fatalf("read %d bytes, want ≤ %d", cnt.n, MaxMetadataBytes+64<<10)
	}
	// In-budget control: a 1 MiB pre-IDAT region still thumbnails.
	ok, err := Generate(bytes.NewReader(pngTextPaddedPNG(t, 1<<20)), 100, 100)
	if err != nil {
		t.Fatalf("in-budget pre-IDAT metadata must decode: %v", err)
	}
	if _, format, err := image.Decode(bytes.NewReader(ok)); err != nil || format != "jpeg" {
		t.Fatalf("expected jpeg thumbnail, got format=%q err=%v", format, err)
	}
}

// AC-2: a PNG declaring a pre-IDAT chunk length ≥ 2³¹ (image/png's "Bad
// chunk length" FormatError threshold) must classify ErrUnsupported (HTTP
// 400) byte-for-byte the same whether the chunk is eXIf or any other type
// (tEXt sibling). Pre-fix, the eXIf walk's budget pre-check fired first
// (remaining+headAvail ≤ MaxMetadataBytes−33 < 2³¹) and misclassified the
// input as errMetadataBudgetExceeded → ErrMetadataTooLarge (HTTP 413); the
// walk now stops at the header with (1, nil) and Decode re-reads the same
// bytes through the replay tee and hits the FormatError, in parity with the
// tEXt sibling.
func TestGeneratePNGeXIfInvalidChunkLengthIsUnsupported(t *testing.T) {
	base := makePNG(t, 64, 64)
	exifFixture := append(append([]byte(nil), base[:33]...), append(pngChunkHeader(t, "eXIf", 0x80000000), base[33:]...)...)
	textFixture := append(append([]byte(nil), base[:33]...), append(pngChunkHeader(t, "tEXt", 0x80000000), base[33:]...)...)
	imgExif, errExif := Generate(bytes.NewReader(exifFixture), 64, 64)
	imgText, errText := Generate(bytes.NewReader(textFixture), 64, 64)
	if imgExif != nil || !errors.Is(errExif, ErrUnsupported) {
		t.Fatalf("eXIf fixture: expected ErrUnsupported with nil payload, got img!=nil=%v err=%v", imgExif != nil, errExif)
	}
	if imgText != nil || !errors.Is(errText, ErrUnsupported) {
		t.Fatalf("tEXt fixture: expected ErrUnsupported with nil payload, got img!=nil=%v err=%v", imgText != nil, errText)
	}
	if errors.Is(errExif, ErrMetadataTooLarge) || errors.Is(errText, ErrMetadataTooLarge) {
		t.Fatalf("neither fixture may classify MetadataTooLarge: exif=%v text=%v", errExif, errText)
	}
	if errors.Is(errExif, ErrUnsupported) != errors.Is(errText, ErrUnsupported) || errExif.Error() != errText.Error() {
		t.Fatalf("class parity broken: exif=%v text=%v", errExif, errText)
	}
}

// AC-5b: an eXIf chunk placed AFTER the first IDAT chunk must be ignored —
// the walk terminates at IDAT (compressed data is never scanned), so the
// output is the absent-EXIF 256×128 red-on-top shape.
func TestGeneratePNGeXIfAfterIDATIgnored(t *testing.T) {
	base := orientedPNG(t, 400, 200, 0, false)
	fixture := spliceAfterIDAT(t, base, "eXIf", bareExifPayload(6, binary.LittleEndian))
	out, err := Generate(bytes.NewReader(fixture), 256, 128)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	img, format, err := image.Decode(bytes.NewReader(out))
	if err != nil || format != "jpeg" {
		t.Fatalf("decode thumb: %v fmt=%s", err, format)
	}
	if img.Bounds().Dx() != 256 || img.Bounds().Dy() != 128 {
		t.Fatalf("thumb dims %dx%d want 256x128 (unrotated)", img.Bounds().Dx(), img.Bounds().Dy())
	}
	assertThumbColor(t, img, 32, 32, "red")
	assertThumbColor(t, img, 32, 96, "blue")
}

// TestPNGeXIfLayouts pins the dual-layout adapter: the conformant bare Exif
// profile (PNG 3rd ed. §11.3.4.5 — the Exif ID code is NOT included) in both
// byte orders, the tolerated "Exif\x00\x00"+TIFF deviation, the exact Pillow
// byte-literal, the Adwaita-class profile (no orientation tag), and the
// malformed/defensive rows — each must yield 1..8, never a panic.
func TestPNGeXIfLayouts(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want int
	}{
		{"bare little-endian o6", bareExifPayload(6, binary.LittleEndian), 6},
		{"bare big-endian o6", bareExifPayload(6, binary.BigEndian), 6},
		{"prefixed little-endian o6", exifPayload(6, binary.LittleEndian), 6},
		{"prefixed big-endian o6", exifPayload(6, binary.BigEndian), 6},
		{"pillow bare-mm o6 literal", pillowExifPayload(), 6},
		{"adwaita-class mm (no orientation tag)", adwaitaClassPayload(), 1},
		{"empty", nil, 1},
		{"truncated byte order", []byte{0x49, 0x49}, 1},
		{"signature only", []byte(exifSignature), 1},
		{"bare invalid magic", mutated(bareExifPayload(6, binary.LittleEndian), 2, 0x2B), 1},
		{"bare bad byte order", mutated(bareExifPayload(6, binary.LittleEndian), 0, 'X'), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pngExifOrientation(tt.in); got != tt.want {
				t.Fatalf("pngExifOrientation = %d want %d", got, tt.want)
			}
		})
	}
}

// TestGenerateAppliesPNGeXIfPillowLayout runs the exact Pillow-written bare-MM
// eXIf payload through the full pipeline — the real-writer interop path
// end-to-end (fix-exif-mm gate's recommendation; the fixture that would
// silently no-op under a prefixed-only adapter).
func TestGenerateAppliesPNGeXIfPillowLayout(t *testing.T) {
	base := orientedPNG(t, 400, 200, 0, false)
	fixture := splicePNGChunk(t, base, "eXIf", pillowExifPayload())
	out, err := Generate(bytes.NewReader(fixture), 256, 128)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	img, format, err := image.Decode(bytes.NewReader(out))
	if err != nil || format != "jpeg" {
		t.Fatalf("decode thumb: %v fmt=%s", err, format)
	}
	if img.Bounds().Dx() != 64 || img.Bounds().Dy() != 128 {
		t.Fatalf("thumb dims %dx%d want 64x128 (Pillow layout applied)", img.Bounds().Dx(), img.Bounds().Dy())
	}
	for _, c := range []int{0, 8, 16, 24, 31} {
		assertThumbColor(t, img, c, 64, "blue")
	}
	for _, c := range []int{48, 56, 60, 63} {
		assertThumbColor(t, img, c, 64, "red")
	}
}

// FR-4: a ≤16 B/read stream forces the eXIf chunk to straddle the head/r seam
// (head = 48 B); the walk reads the r-portion through the replay tee, so the
// decoded frame — and therefore the encoded output — must be byte-identical
// to the fully-buffered case.
func TestGeneratePNGeXIfStraddlesHeadSeam(t *testing.T) {
	fixture := orientedPNG(t, 400, 200, 6, false)
	slow, err := Generate(&slowReader{r: bytes.NewReader(fixture), max: 16}, 256, 128)
	if err != nil {
		t.Fatalf("Generate via slow reader: %v", err)
	}
	fast, err := Generate(bytes.NewReader(fixture), 256, 128)
	if err != nil {
		t.Fatalf("Generate via buffered reader: %v", err)
	}
	if !bytes.Equal(slow, fast) {
		t.Fatalf("slow-reader output differs from fully-buffered output (%d vs %d bytes)", len(slow), len(fast))
	}
	// Sanity: the fixture really carries orientation 6 (a vacuous comparison
	// of two unrotated outputs must not pass).
	img, _, err := image.Decode(bytes.NewReader(fast))
	if err != nil {
		t.Fatalf("decode thumb: %v", err)
	}
	if img.Bounds().Dx() != 64 || img.Bounds().Dy() != 128 {
		t.Fatalf("thumb dims %dx%d want 64x128", img.Bounds().Dx(), img.Bounds().Dy())
	}
}

// TestPNGOrientationDefensive pins every defensive branch of the walk:
// stop conditions (IDAT/IEND/oversized length), the eXIf-branch budget guard
// firing before allocation (qa F1), first-eXIf-wins and zero-length eXIf
// (qa F5), the mid-header seam (qa F9), and the FR-5 error/context identity
// rows (raw errors for generateLocked to classify).
func TestPNGOrientationDefensive(t *testing.T) {
	ihdr := makePNG(t, 64, 64)[:33]
	wrapped := fmtWrappedDeadline()
	tests := []struct {
		name string
		head []byte
		r    io.Reader
		ctx  context.Context
		want int
		err  error
	}{
		{"head shorter than 33", makePNG(t, 64, 64)[:16], nil, context.Background(), 1, nil},
		{"idat first stops", ihdr, bytes.NewReader(pngChunkHeader(t, "IDAT", 0)), context.Background(), 1, nil},
		{"iend first stops", ihdr, bytes.NewReader(pngChunkHeader(t, "IEND", 0)), context.Background(), 1, nil},
		{"length over 2^31 stops", ihdr, bytes.NewReader(pngChunkHeader(t, "tEXt", 0x80000000)), context.Background(), 1, nil},
		{"exif length over 2^31 stops", ihdr, bytes.NewReader(pngChunkHeader(t, "eXIf", 0x80000000)), context.Background(), 1, nil},
		{"exif length beyond budget aborts before allocation",
			ihdr,
			&eXIfHeaderReader{hdr: pngChunkHeader(t, "eXIf", uint32(MaxMetadataBytes-33+1))},
			context.Background(), 0, errMetadataBudgetExceeded},
		{"first exif wins",
			firstWinsPNG(t), nil, context.Background(), 3, nil},
		{"zero-length exif is no-op", splicePNGChunk(t, makePNG(t, 64, 64), "eXIf", nil),
			nil, context.Background(), 1, nil},
		{"text then eof from r is deferred", ihdr,
			bytes.NewReader(pngChunkHeader(t, "tEXt", 0)), context.Background(), 0, io.EOF},
		{"ctx canceled at boundary", ihdr, nil, canceledCtx(), 0, context.Canceled},
		{"wrapped deadline from r is raw", ihdr,
			&errAfterDataReader{data: pngChunkHeader(t, "tEXt", 0), err: wrapped}, context.Background(), 0, wrapped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pngOrientation(tt.ctx, tt.head, tt.r)
			if got != tt.want || !errors.Is(err, tt.err) {
				t.Fatalf("pngOrientation = %d, %v want %d, %v", got, err, tt.want, tt.err)
			}
		})
	}

	// qa F9: head ending mid-chunk-header (e.g. 36 bytes: 33 + 3 of the eXIf
	// header) — readN must stitch the seam and still find the chunk.
	full := orientedPNG(t, 64, 64, 6, false)
	got, err := pngOrientation(context.Background(), full[:36], bytes.NewReader(full[36:]))
	if err != nil || got != 6 {
		t.Fatalf("mid-header straddle = %d, %v want 6, nil", got, err)
	}
}

// TestPNGOrientationAtExactBudget pins the walk's exact accounting boundary
// (qa F2, white-box): with a 33-byte head (as DecodeConfig leaves a slow
// stream), budget = MaxMetadataBytes − 33; the walk charges chunk headers +
// data + CRCs, and the pass boundary for a file whose eXIf sits after the
// flood is pre-IDAT structure T ≤ MaxMetadataBytes − 29 (the eXIf header +
// data are the last chargeable reads — the IDAT header is never reached
// because the walk stops at the eXIf). At-cap lands exactly on the budget
// (the eXIf data read consumes the last byte); one structure byte more fails
// with errMetadataBudgetExceeded. The "≤ 8 MiB" prose is off by a constant
// and by the chunk-header/CRC accounting.
func TestPNGOrientationAtExactBudget(t *testing.T) {
	head, r := atBudgetPreIDAT(t, 0)
	orient, err := pngOrientation(context.Background(), head, r)
	if err != nil || orient != 6 {
		t.Fatalf("at-cap: orient=%d err=%v want 6,nil", orient, err)
	}
	head2, r2 := atBudgetPreIDAT(t, 1)
	if _, err := pngOrientation(context.Background(), head2, r2); !errors.Is(err, errMetadataBudgetExceeded) {
		t.Fatalf("over-by-one: err=%v want errMetadataBudgetExceeded", err)
	}

	// The no-eXIf arrangement (the walk runs through to IDAT): the IDAT
	// chunk header (8 B) is charged too, so pre-IDAT structure T ≤ budget −
	// 8 passes while one byte more cannot fit the IDAT header read.
	head3, r3 := noExifAtBudgetIDAT(t, 0)
	orient, err = pngOrientation(context.Background(), head3, r3)
	if err != nil || orient != 1 {
		t.Fatalf("no-exif at-cap: orient=%d err=%v want 1,nil", orient, err)
	}
	head4, r4 := noExifAtBudgetIDAT(t, 1)
	if _, err := pngOrientation(context.Background(), head4, r4); !errors.Is(err, errMetadataBudgetExceeded) {
		t.Fatalf("no-exif over-by-one: err=%v want errMetadataBudgetExceeded", err)
	}
}

// TestGeneratePNGMetadataAtExactBudget is the generate-level boundary pin
// (qa F2): with a > 4 KiB fixture, DecodeConfig's bufio fill makes head =
// 4096 deterministic, so the walk's free in-head consumption is 4063 B and
// the pass boundary is pre-IDAT structure T ≤ MaxMetadataBytes − 29 (the
// eXIf header + data are the last chargeable reads; the walk stops there, so
// the IDAT header is never charged). A fixture sized to exactly that bound
// thumbnails with the eXIf orientation applied (64×128); one structure byte
// more rejects with ErrMetadataTooLarge before any pixel allocation. The
// JPEG twin (TestGenerateMetadataAtExactBudget) documents the same
// write-level accounting for its codec.
func TestGeneratePNGMetadataAtExactBudget(t *testing.T) {
	base := orientedPNG(t, 400, 200, 0, false) // no eXIf yet; added after the flood
	atCap := buildBudgetFixture(t, base, 0)
	out, err := Generate(bytes.NewReader(atCap), 256, 128)
	if err != nil {
		t.Fatalf("at-cap generate: %v", err)
	}
	img, format, err := image.Decode(bytes.NewReader(out))
	if err != nil || format != "jpeg" {
		t.Fatalf("decode at-cap thumb: %v fmt=%s", err, format)
	}
	if img.Bounds().Dx() != 64 || img.Bounds().Dy() != 128 {
		t.Fatalf("at-cap thumb dims %dx%d want 64x128 (orientation applied at the boundary)", img.Bounds().Dx(), img.Bounds().Dy())
	}

	// One structure byte more: the eXIf data read no longer fits → reject.
	over := buildBudgetFixture(t, base, 1)
	cnt := &countingReader{r: bytes.NewReader(over)}
	img2, err := Generate(cnt, 256, 128)
	if img2 != nil || !errors.Is(err, ErrMetadataTooLarge) {
		t.Fatalf("over-by-one: expected ErrMetadataTooLarge with nil payload, got img!=nil=%v err=%v", img2 != nil, err)
	}
	if cnt.n > MaxMetadataBytes+64<<10 {
		t.Fatalf("read %d bytes, want ≤ %d", cnt.n, MaxMetadataBytes+64<<10)
	}
}

// F7: orientation 3 (180°) rotates without a box swap — the 400×200 fixture
// stays 256×128 with blue on top and red on the bottom.
func TestGenerateAppliesPNGeXIfOrientation3(t *testing.T) {
	fixture := orientedPNG(t, 400, 200, 3, false)
	out, err := Generate(bytes.NewReader(fixture), 256, 128)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	img, format, err := image.Decode(bytes.NewReader(out))
	if err != nil || format != "jpeg" {
		t.Fatalf("decode thumb: %v fmt=%s", err, format)
	}
	if img.Bounds().Dx() != 256 || img.Bounds().Dy() != 128 {
		t.Fatalf("thumb dims %dx%d want 256x128 (no box swap for o3)", img.Bounds().Dx(), img.Bounds().Dy())
	}
	assertThumbColor(t, img, 32, 32, "blue") // 180°: top half is the source's bottom (blue)
	assertThumbColor(t, img, 32, 96, "red")
}

// F4: a paletted PNG makes DecodeConfig consume PLTE (head > 33); the walk
// must still find the eXIf chunk from byte 33 (position-independent), spliced
// here after PLTE per real-world writer placement.
func TestGenerateAppliesPNGeXIfPaletted(t *testing.T) {
	pal := color.Palette{
		color.RGBA{255, 0, 0, 255},
		color.RGBA{0, 0, 255, 255},
	}
	img := image.NewPaletted(image.Rect(0, 0, 400, 200), pal)
	for y := 0; y < 200; y++ {
		idx := uint8(0) // red
		if y >= 100 {
			idx = 1 // blue
		}
		for x := 0; x < 400; x++ {
			img.SetColorIndex(x, y, idx)
		}
	}
	var base bytes.Buffer
	if err := png.Encode(&base, img); err != nil {
		t.Fatalf("encode paletted png: %v", err)
	}
	fixture := spliceChunkAfter(t, base.Bytes(), "PLTE", "eXIf", bareExifPayload(6, binary.LittleEndian))
	out, err := Generate(bytes.NewReader(fixture), 256, 128)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	dec, format, err := image.Decode(bytes.NewReader(out))
	if err != nil || format != "jpeg" {
		t.Fatalf("decode thumb: %v fmt=%s", err, format)
	}
	if dec.Bounds().Dx() != 64 || dec.Bounds().Dy() != 128 {
		t.Fatalf("paletted thumb dims %dx%d want 64x128", dec.Bounds().Dx(), dec.Bounds().Dy())
	}
	for _, c := range []int{0, 8, 16, 24, 31} {
		assertThumbColor(t, dec, c, 64, "blue")
	}
	for _, c := range []int{48, 56, 60, 63} {
		assertThumbColor(t, dec, c, 64, "red")
	}
}

// F4: a depth-16 PNG (color type 6/2, 16-bit) within Max16BitSourceDim passes
// the pngBitDepth gate and still applies the eXIf orientation — the 16-bit
// gate + walk combination on a live production input class.
func TestGenerateAppliesPNGeXIf16Bit(t *testing.T) {
	img := image.NewNRGBA64(image.Rect(0, 0, 400, 200))
	for y := 0; y < 200; y++ {
		c := color.NRGBA64{R: 0xFFFF, G: 0, B: 0, A: 0xFFFF}
		if y >= 100 {
			c = color.NRGBA64{R: 0, G: 0, B: 0xFFFF, A: 0xFFFF}
		}
		for x := 0; x < 400; x++ {
			img.SetNRGBA64(x, y, c)
		}
	}
	var base bytes.Buffer
	if err := png.Encode(&base, img); err != nil {
		t.Fatalf("encode depth-16 png: %v", err)
	}
	if pngBitDepth(base.Bytes()) != 16 {
		t.Fatalf("fixture is not depth-16 (pngBitDepth=%d)", pngBitDepth(base.Bytes()))
	}
	fixture := splicePNGChunk(t, base.Bytes(), "eXIf", bareExifPayload(6, binary.LittleEndian))
	out, err := Generate(bytes.NewReader(fixture), 256, 128)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	dec, format, err := image.Decode(bytes.NewReader(out))
	if err != nil || format != "jpeg" {
		t.Fatalf("decode thumb: %v fmt=%s", err, format)
	}
	if dec.Bounds().Dx() != 64 || dec.Bounds().Dy() != 128 {
		t.Fatalf("depth-16 thumb dims %dx%d want 64x128", dec.Bounds().Dx(), dec.Bounds().Dy())
	}
	for _, c := range []int{0, 8, 16, 24, 31} {
		assertThumbColor(t, dec, c, 64, "blue")
	}
	for _, c := range []int{48, 56, 60, 63} {
		assertThumbColor(t, dec, c, 64, "red")
	}
}
