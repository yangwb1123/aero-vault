package thumbnail

// PNG eXIf-chunk EXIF orientation tests (direction: honor PNG eXIf orientation
// — thumbnails of portrait PNG photos currently render sideways). Acceptance
// AC-1/AC-2/AC-5a/AC-5b, the interop pins (bare/prefixed/Pillow/Adwaita-class
// layouts), the FR-4 seam byte-exactness pin, the FR-5 error/context identity
// rows, the exact-budget boundary (qa F2), and the fuzz discipline (qa F2
// pattern). Fixtures mirror the JPEG twins in exif_test.go 1:1 (400×200
// red-top/blue-bottom, interior sampling via assertThumbColor).

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"testing"
)

// bareExifPayload is the conformant PNG eXIf chunk data layout: the TIFF
// structure WITHOUT the "Exif\x00\x00" signature (PNG Third Edition
// §11.3.4.5 — the Exif ID code is not included). It is exifPayload's body
// with the 6-byte signature stripped, so the same orientation/byte-order
// fixtures drive both the JPEG APP1 parser and the PNG eXIf parser.
func bareExifPayload(o int, bo binary.ByteOrder) []byte {
	return exifPayload(o, bo)[6:]
}

// pillowExifPayload is the exact bytes Pillow 12.3.0 wrote for an eXIf chunk
// declaring orientation 6 (verified on the reference host): bare big-endian
// TIFF (MM, magic 0x2A, IFD0 at offset 8), one SHORT entry for tag 0x0112,
// count 1, value 6 left-justified at entry[8:10] — TIFF 6.0 §2 inline-SHORT
// semantics, the interop regression guard recommended by the fix-exif-mm gate.
func pillowExifPayload() []byte {
	return []byte{
		'M', 'M', 0x00, 0x2A, 0x00, 0x00, 0x00, 0x08, // TIFF header, IFD0 at 8
		0x00, 0x01, // one entry
		0x01, 0x12, // tag 0x0112 (orientation)
		0x00, 0x03, // type SHORT
		0x00, 0x00, 0x00, 0x01, // count 1
		0x00, 0x06, 0x00, 0x00, // value 6 (left-justified), padding
		0x00, 0x00,
	}
}

// adwaitaClassPayload is a bare big-endian Exif profile with a single
// 0x8769 (ExifIFDPointer) entry and no 0x0112 orientation tag — the class of
// real-world icon-theme eXIf chunks (e.g. Adwaita) that must resolve to 1.
func adwaitaClassPayload() []byte {
	p := make([]byte, 24)
	p[0], p[1] = 'M', 'M'
	binary.BigEndian.PutUint16(p[2:4], 0x2A)
	binary.BigEndian.PutUint32(p[4:8], 8)
	binary.BigEndian.PutUint16(p[8:10], 1)       // one entry
	binary.BigEndian.PutUint16(p[10:12], 0x8769) // ExifIFDPointer (not 0x0112)
	binary.BigEndian.PutUint16(p[12:14], 4)      // type LONG
	binary.BigEndian.PutUint32(p[14:18], 1)
	binary.BigEndian.PutUint32(p[18:22], 0)
	return p
}

// pngChunkBytes builds a complete PNG chunk: 4-byte BE length + 4-byte type +
// data + CRC32(IEEE) over type+data. The CRC is mandatory (C4): image/png
// validates every chunk — unknown ancillary chunks included — and a bad CRC
// fails Decode with ErrUnsupported.
func pngChunkBytes(t testing.TB, chunkType string, data []byte) []byte {
	t.Helper()
	if len(chunkType) != 4 {
		t.Fatalf("chunk type %q must be 4 bytes", chunkType)
	}
	var out bytes.Buffer
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(data)))
	out.Write(l[:])
	out.WriteString(chunkType)
	out.Write(data)
	crc := crc32.NewIEEE()
	_, _ = crc.Write([]byte(chunkType))
	_, _ = crc.Write(data)
	binary.BigEndian.PutUint32(l[:], crc.Sum32())
	out.Write(l[:])
	return out.Bytes()
}

// pngChunkHeader builds an 8-byte chunk header (length + type) without data
// or CRC — enough for the walk's white-box stop/defensive rows.
func pngChunkHeader(t testing.TB, chunkType string, length uint32) []byte {
	t.Helper()
	if len(chunkType) != 4 {
		t.Fatalf("chunk type %q must be 4 bytes", chunkType)
	}
	var h [8]byte
	binary.BigEndian.PutUint32(h[:4], length)
	copy(h[4:], chunkType)
	return h[:]
}

// splicePNGChunk inserts a complete chunk immediately after the IHDR chunk
// (byte offset 33) of a png.Encode-produced PNG — IHDR is always its first
// chunk, so byte 33 is exactly its end and the splice lands before IDAT.
func splicePNGChunk(t testing.TB, base []byte, chunkType string, data []byte) []byte {
	t.Helper()
	if len(base) < 33 {
		t.Fatalf("png fixture too short: %d", len(base))
	}
	var out bytes.Buffer
	out.Write(base[:33])
	out.Write(pngChunkBytes(t, chunkType, data))
	out.Write(base[33:])
	return out.Bytes()
}

// spliceChunkAfter inserts chunk immediately after the first full occurrence
// of the named chunk type (header + data + CRC) — used for PLTE (paletted
// fixtures, head > 33) and IDAT (post-IDAT eXIf, AC-5b).
func spliceChunkAfter(t testing.TB, base []byte, after, chunkType string, data []byte) []byte {
	t.Helper()
	for i := 8; i+8 <= len(base); {
		length := int(binary.BigEndian.Uint32(base[i : i+4]))
		if string(base[i+4:i+8]) == after {
			end := i + 8 + length + 4
			if end > len(base) {
				t.Fatalf("chunk %q overruns fixture", after)
			}
			var out bytes.Buffer
			out.Write(base[:end])
			out.Write(pngChunkBytes(t, chunkType, data))
			out.Write(base[end:])
			return out.Bytes()
		}
		i += 8 + length + 4
	}
	t.Fatalf("chunk %q not found in fixture", after)
	return nil
}

// spliceAfterIDAT inserts chunk immediately after the first IDAT chunk's CRC:
// the AC-5b shape — a post-IDAT eXIf must be ignored (the walk never scans
// pixel data; image/png skips the unknown chunk with CRC validation).
func spliceAfterIDAT(t testing.TB, base []byte, chunkType string, data []byte) []byte {
	t.Helper()
	return spliceChunkAfter(t, base, "IDAT", chunkType, data)
}

// orientedPNG builds a w×h RGBA PNG with a red top half and blue bottom half
// (the AC-1 fixture shape, mirroring orientationJPEG), optionally spliced with
// an eXIf chunk declaring orient (1–8; 0 = no eXIf) immediately after IHDR.
// prefixed selects the tolerated "Exif\x00\x00"+TIFF deviation; the default
// is the conformant bare Exif profile.
func orientedPNG(t testing.TB, w, h, orient int, prefixed bool) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		c := color.RGBA{255, 0, 0, 255}
		if y >= h/2 {
			c = color.RGBA{0, 0, 255, 255}
		}
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var base bytes.Buffer
	if err := png.Encode(&base, img); err != nil {
		t.Fatalf("encode base png: %v", err)
	}
	if orient < 1 || orient > 8 {
		return base.Bytes()
	}
	payload := bareExifPayload(orient, binary.LittleEndian)
	if prefixed {
		payload = exifPayload(orient, binary.LittleEndian)
	}
	return splicePNGChunk(t, base.Bytes(), "eXIf", payload)
}

// tEXtChunk builds a tEXt chunk whose data carries the "Comment\0" keyword
// plus padding to dataLen bytes; each chunk contributes dataLen + 12 structure
// bytes (8 header + data + 4 CRC).
func tEXtChunk(t testing.TB, dataLen int) []byte {
	t.Helper()
	const prefix = "Comment\x00"
	if dataLen < len(prefix) {
		dataLen = len(prefix)
	}
	return pngChunkBytes(t, "tEXt", append([]byte(prefix), make([]byte, dataLen-len(prefix))...))
}

// pngTextPaddedPNG builds a valid small PNG whose pre-IDAT region (between
// IHDR and IDAT) carries at least totalBytes of tEXt chunk structure — the
// PNG metadata-flood shape (image/png validates every chunk, so each tEXt
// carries a valid CRC).
func pngTextPaddedPNG(t testing.TB, totalBytes int) []byte {
	t.Helper()
	base := makePNG(t, 8, 8)
	var out bytes.Buffer
	out.Write(base[:33])
	remaining := totalBytes
	for remaining > 0 {
		dataLen := remaining - 12
		if dataLen > 1<<20-12 {
			dataLen = 1<<20 - 12
		}
		if dataLen < 16 {
			dataLen = 16
		}
		out.Write(tEXtChunk(t, dataLen))
		remaining -= 12 + dataLen
	}
	out.Write(base[33:])
	return out.Bytes()
}

// slowReader returns at most max bytes per Read — the ≤16 B/read shape that
// makes DecodeConfig's bufio fill collect head in small chunks, so the eXIf
// chunk straddles the head/r seam and the walk must read the r-portion
// through the replay tee (FR-4's load-bearing path).
type slowReader struct {
	r   io.Reader
	max int
}

func (s *slowReader) Read(p []byte) (int, error) {
	if len(p) > s.max {
		p = p[:s.max]
	}
	return s.r.Read(p)
}

// eXIfHeaderReader serves a single 8-byte eXIf chunk header, then fails any
// further read — pinning that the walk's budget pre-check fires before the
// data buffer is allocated (qa F1): a regression that allocated first would
// reach the marker error instead of errMetadataBudgetExceeded.
type eXIfHeaderReader struct {
	hdr []byte
}

var unexpectedReadErr = errors.New("png_exif_test: unexpected read past the eXIf header")

func (r *eXIfHeaderReader) Read(p []byte) (int, error) {
	if len(r.hdr) > 0 {
		n := copy(p, r.hdr)
		r.hdr = r.hdr[n:]
		return n, nil
	}
	return 0, unexpectedReadErr
}

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

// fmtWrappedDeadline returns a wrapped context.DeadlineExceeded instance —
// the QA-3 fallback shape (same instance must survive the walk).
func fmtWrappedDeadline() error {
	return fmt.Errorf("wrapped: %w", context.DeadlineExceeded)
}

func canceledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// firstWinsPNG builds a PNG with two eXIf chunks in order (o=3 then o=6): the
// walk must stop at the first (PNG 3rd ed. allows only one eXIf; first-wins
// mirrors the JPEG first-APP1 rule).
func firstWinsPNG(t testing.TB) []byte {
	t.Helper()
	base := makePNG(t, 64, 64)
	var out bytes.Buffer
	out.Write(base[:33])
	out.Write(pngChunkBytes(t, "eXIf", bareExifPayload(3, binary.LittleEndian)))
	out.Write(pngChunkBytes(t, "eXIf", bareExifPayload(6, binary.LittleEndian)))
	out.Write(base[33:])
	return out.Bytes()
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

// noExifAtBudgetIDAT builds a 33-byte IHDR head plus a reader serving tEXt
// chunks totaling exactly MaxMetadataBytes − 41 (+over bytes) followed by the
// IDAT chunk header: with head = 33 the walk's r-consumption is T + 8, and
// at-cap lands exactly on the budget at the IDAT header read.
func noExifAtBudgetIDAT(t testing.TB, over int) ([]byte, io.Reader) {
	t.Helper()
	head := makePNG(t, 64, 64)[:33]
	var pre bytes.Buffer
	for i := 0; i < 7; i++ {
		pre.Write(tEXtChunk(t, 1<<20-12)) // 1 MiB structure each
	}
	pre.Write(tEXtChunk(t, MaxMetadataBytes-41+over-7<<20-12))
	pre.Write(pngChunkHeader(t, "IDAT", 0))
	return head, bytes.NewReader(pre.Bytes())
}

// atBudgetPreIDAT builds a 33-byte IHDR head plus a reader serving 7 full
// 1 MiB tEXt chunks, one remainder tEXt chunk and an eXIf chunk (o=6) — total
// pre-IDAT structure T = MaxMetadataBytes − 29 (+over bytes). With head = 33
// the walk's r-consumption is T − 4 (all structure from r except the eXIf
// CRC, which the walk never reads): at-cap lands exactly on the budget at
// the eXIf data read, one byte more cannot fit it.
func atBudgetPreIDAT(t testing.TB, over int) ([]byte, io.Reader) {
	t.Helper()
	head := makePNG(t, 64, 64)[:33]
	var pre bytes.Buffer
	for i := 0; i < 7; i++ {
		pre.Write(tEXtChunk(t, 1<<20-12)) // 1 MiB structure each
	}
	pre.Write(tEXtChunk(t, MaxMetadataBytes-29+over-38-7<<20-12))
	pre.Write(pngChunkBytes(t, "eXIf", bareExifPayload(6, binary.LittleEndian)))
	return head, bytes.NewReader(pre.Bytes())
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

// buildBudgetFixture splices 7 full 1 MiB tEXt chunks, one remainder tEXt
// chunk and the eXIf chunk (o=6) between IHDR and IDAT, sizing the pre-IDAT
// structure to exactly MaxMetadataBytes − 29 (+over). With head = 4096 the
// walk's r-consumption is T − 4067: at-cap lands exactly on the budget at
// the eXIf data read.
func buildBudgetFixture(t testing.TB, base []byte, over int) []byte {
	var out bytes.Buffer
	out.Write(base[:33])
	for i := 0; i < 7; i++ {
		out.Write(tEXtChunk(t, 1<<20-12)) // 1 MiB structure each
	}
	out.Write(tEXtChunk(t, MaxMetadataBytes-29+over-38-7<<20-12))
	out.Write(pngChunkBytes(t, "eXIf", bareExifPayload(6, binary.LittleEndian)))
	out.Write(base[33:]) // IDAT + IEND
	return out.Bytes()
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

// FuzzPNGOrientation drives the bounded PNG eXIf chunk walk with arbitrary
// mutations: any input must yield either a 1..8 orientation or an error from
// the walk's contract set (budget / context sentinels / plain io errors —
// EOF/truncation surfaces raw and is deferred to Decode), never a panic.
// Each seed is split at a random point into head + r, exercising the seam.
// Run: go test -fuzz=FuzzPNGOrientation -fuzztime=60s ./internal/thumbnail/
func FuzzPNGOrientation(f *testing.F) {
	ihdr := makePNG(f, 64, 64)
	f.Add(ihdr[:33], ihdr[33:]) // clean split: no eXIf
	oriented := orientedPNG(f, 64, 64, 6, false)
	f.Add(oriented[:33], oriented[33:])                                               // eXIf wholly in r
	f.Add(oriented[:40], oriented[40:])                                               // mid-eXIf-header split
	f.Add(oriented[:48], oriented[48:])                                               // mid-eXIf-data split
	f.Add(oriented, []byte(nil))                                                      // whole stream in head
	f.Add(orientedPNG(f, 64, 64, 6, true)[:33], orientedPNG(f, 64, 64, 6, true)[33:]) // prefixed deviation
	after := spliceAfterIDAT(f, orientedPNG(f, 64, 64, 0, false), "eXIf", bareExifPayload(6, binary.LittleEndian))
	f.Add(after[:33], after[33:])         // post-IDAT eXIf (ignored)
	f.Add([]byte("garbage"), []byte(nil)) // not a PNG header
	f.Add(ihdr[:33], []byte{0x00, 0x01})  // truncated chunk header

	f.Fuzz(func(t *testing.T, head, tail []byte) {
		orient, err := pngOrientation(context.Background(), head, bytes.NewReader(tail))
		if err != nil {
			if !errors.Is(err, errMetadataBudgetExceeded) &&
				!errors.Is(err, context.DeadlineExceeded) &&
				!errors.Is(err, context.Canceled) &&
				!errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("unexpected error class: %v", err)
			}
			return
		}
		if orient < 1 || orient > 8 {
			t.Fatalf("pngOrientation = %d, want 1..8", orient)
		}
	})
}
