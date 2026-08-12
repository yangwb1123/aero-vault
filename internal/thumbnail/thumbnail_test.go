package thumbnail

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"runtime"
	"testing"
)

func makePNG(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// headerOnlyPNG builds a PNG containing only the signature + IHDR chunk
// (valid CRC) — no IDAT/IEND — so no pixel buffer is ever allocated. The
// declared dimensions, IHDR bit depth and color type are attacker-controlled.
func headerOnlyPNG(t testing.TB, w, h int, bitDepth, colorType byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}) // signature
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:8], uint32(h))
	// bit depth / color type at IHDR bytes 8/9 (compression 0, filter 0,
	// interlace 0 follow).
	ihdr[8], ihdr[9], ihdr[10], ihdr[11], ihdr[12] = bitDepth, colorType, 0, 0, 0
	var chunk bytes.Buffer
	chunk.WriteString("IHDR")
	chunk.Write(ihdr)
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], 13)
	buf.Write(l[:])
	buf.Write(chunk.Bytes())
	crc := crc32.NewIEEE()
	_, _ = crc.Write(chunk.Bytes())
	binary.BigEndian.PutUint32(l[:], crc.Sum32())
	buf.Write(l[:])
	return buf.Bytes()
}

func TestGenerateRejectsOversizedImage(t *testing.T) {
	bomb := headerOnlyPNG(t, 100000, 100000, 8, 6) // 10¹⁰ declared pixels
	if len(bomb) > 60 {
		t.Fatalf("bomb fixture unexpectedly large: %d bytes", len(bomb))
	}
	if _, err := Generate(bytes.NewReader(bomb), 100, 100); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("expected ErrImageTooLarge, got %v", err)
	}
	// Dimension-driven, not format-driven: a header-only PNG has no IDAT, so
	// the in-cap 8×8 case must fail as unsupported, NOT as too large — and a
	// complete in-cap PNG must decode cleanly.
	if _, err := Generate(bytes.NewReader(headerOnlyPNG(t, 8, 8, 8, 6)), 100, 100); errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("in-cap dims must not trigger ErrImageTooLarge")
	}
	if _, err := Generate(bytes.NewReader(makePNG(t, 8, 8)), 100, 100); err != nil {
		t.Fatalf("in-cap image must decode: %v", err)
	}
	// AC1: an 8-bit PNG at exactly MaxSourceDim passes the dims gate and the
	// depth-16 gate (bit depth 8); with no IDAT it must fall through to
	// ErrUnsupported — the 8-bit baseline at the old boundary is unchanged.
	_, err := Generate(bytes.NewReader(headerOnlyPNG(t, MaxSourceDim, MaxSourceDim, 8, 6)), 100, 100)
	if errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("8-bit at MaxSourceDim must not trigger ErrImageTooLarge")
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("8-bit header-only at MaxSourceDim: want ErrUnsupported, got %v", err)
	}
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// endlessZeros never returns EOF: an unbounded caller-side stream.
type endlessZeros struct{}

func (endlessZeros) Read(p []byte) (int, error) { return len(p), nil }

func TestGenerateTruncatedInput(t *testing.T) {
	// (a) Valid header, payload truncated mid-IDAT → ErrUnsupported.
	png := makePNG(t, 100, 100)
	if _, err := Generate(io.LimitReader(bytes.NewReader(png), int64(len(png)/2)), 100, 100); err != ErrUnsupported {
		t.Fatalf("truncated input: expected ErrUnsupported, got %v", err)
	}
	// (b) Endless stream, externally capped: prompt ErrUnsupported, bounded reads.
	cnt := &countingReader{r: endlessZeros{}}
	if _, err := Generate(io.LimitReader(cnt, 4096), 100, 100); err != ErrUnsupported {
		t.Fatalf("endless input: expected ErrUnsupported, got %v", err)
	}
	if cnt.n > 4096 {
		t.Fatalf("read %d bytes, want ≤ 4096", cnt.n)
	}
}

func TestGenerateDownscalesPreservingAspect(t *testing.T) {
	src := makePNG(t, 400, 200) // 2:1
	out, err := Generate(bytes.NewReader(src), 100, 100)
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
	// 400x200 capped to 100x100 box, aspect preserved → 100x50.
	if b.Dx() != 100 || b.Dy() != 50 {
		t.Fatalf("expected 100x50, got %dx%d", b.Dx(), b.Dy())
	}
}

func TestGenerateNeverUpscales(t *testing.T) {
	src := makePNG(t, 50, 40)
	out, err := Generate(bytes.NewReader(src), 500, 500)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	img, _, _ := image.Decode(bytes.NewReader(out))
	if img.Bounds().Dx() != 50 || img.Bounds().Dy() != 40 {
		t.Fatalf("should not upscale: got %dx%d", img.Bounds().Dx(), img.Bounds().Dy())
	}
}

func TestGenerateDefaults(t *testing.T) {
	src := makePNG(t, 1000, 1000)
	out, err := Generate(bytes.NewReader(src), 0, 0) // default 256
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	img, _, _ := image.Decode(bytes.NewReader(out))
	if img.Bounds().Dx() != DefaultMax || img.Bounds().Dy() != DefaultMax {
		t.Fatalf("expected %dx%d, got %dx%d", DefaultMax, DefaultMax, img.Bounds().Dx(), img.Bounds().Dy())
	}
}

// appnPaddedJPEG builds an 8×8 baseline JPEG whose pre-SOF region carries
// totalMetaBytes of APP1 segment payload (marker 0xE1), matching the verified
// attack: image/jpeg's config scan reads every pre-SOF segment in full, and
// the 16-bit length field caps a single APP1 at 65533 bytes so many segments
// are needed. The result is still a valid image — decoders skip the APP1
// payload as metadata.
func appnPaddedJPEG(t testing.TB, totalMetaBytes int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 32), uint8(y * 32), 128, 255})
		}
	}
	var base bytes.Buffer
	if err := jpeg.Encode(&base, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode base jpeg: %v", err)
	}
	b := base.Bytes()
	if len(b) < 2 || b[0] != 0xFF || b[1] != 0xD8 {
		t.Fatalf("unexpected jpeg header: % x", b[:2])
	}
	var out bytes.Buffer
	out.Write(b[:2])            // SOI; APP1 segments splice in before the rest of the image.
	const maxSegPayload = 65533 // 0xFFFF minus the 2 length bytes
	payload := bytes.Repeat([]byte{0x42}, maxSegPayload)
	remaining := totalMetaBytes
	for remaining > 0 {
		n := remaining
		if n > maxSegPayload {
			n = maxSegPayload
		}
		var seg [4]byte
		seg[0], seg[1] = 0xFF, 0xE1 // APP1
		binary.BigEndian.PutUint16(seg[2:4], uint16(n+2))
		out.Write(seg[:])
		out.Write(payload[:n])
		remaining -= n
	}
	out.Write(b[2:])
	return out.Bytes()
}

func TestGenerateRejectsOversizedMetadata(t *testing.T) {
	// (a) rejection + (b) fail-fast/bounded reads on a 9 MiB pre-SOF payload.
	payload := appnPaddedJPEG(t, MaxMetadataBytes+1<<20)
	cnt := &countingReader{r: bytes.NewReader(payload)}
	img, err := Generate(cnt, 100, 100)
	if img != nil || !errors.Is(err, ErrMetadataTooLarge) {
		t.Fatalf("expected ErrMetadataTooLarge with nil payload, got img!=nil=%v err=%v", img != nil, err)
	}
	if cnt.n > MaxMetadataBytes+64<<10 {
		t.Fatalf("read %d bytes, want ≤ %d", cnt.n, MaxMetadataBytes+64<<10)
	}

	// (c) no payload buffering: total allocated bytes must stay below the
	// payload size. Measured with runtime.ReadMemStats TotalAlloc (monotonic;
	// testing.AllocsPerRun returns a malloc *count*, not bytes, and would be
	// vacuous here). The 64 MiB fixture gives a deterministic margin over the
	// fixed-path allocation (~16 MiB: bytes.Buffer doubling up to the
	// 8 MiB cap) AND over -race detector overhead; it stays red on the
	// unbounded baseline (head doubling + replay ≥ the payload).
	huge := appnPaddedJPEG(t, 64<<20)
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	img, err = Generate(bytes.NewReader(huge), 100, 100)
	runtime.ReadMemStats(&m1)
	if img != nil || !errors.Is(err, ErrMetadataTooLarge) {
		t.Fatalf("expected ErrMetadataTooLarge with nil payload, got img!=nil=%v err=%v", img != nil, err)
	}
	if d := m1.TotalAlloc - m0.TotalAlloc; d > uint64(len(huge)) {
		t.Fatalf("allocated %d bytes for %d-byte input: payload buffered", d, len(huge))
	}

	// (d) in-budget control: 1 MiB of pre-SOF metadata (EXIF/ICC class) still
	// thumbnails to a valid JPEG; also validates the fixture builder.
	ok, err := Generate(bytes.NewReader(appnPaddedJPEG(t, 1<<20)), 100, 100)
	if err != nil {
		t.Fatalf("in-budget metadata must decode: %v", err)
	}
	if _, format, err := image.Decode(bytes.NewReader(ok)); err != nil || format != "jpeg" {
		t.Fatalf("expected jpeg thumbnail, got format=%q err=%v", format, err)
	}
}

func TestGenerateRejectsOversizedMetadata32MiB(t *testing.T) {
	// Fails fast instead of returning a valid thumbnail: on the unbounded
	// baseline this call succeeded after reading the full 32 MiB+; now it must
	// abort at the budget with bounded reads and no decode.
	payload := appnPaddedJPEG(t, 32<<20)
	cnt := &countingReader{r: bytes.NewReader(payload)}
	img, err := Generate(cnt, 100, 100)
	if img != nil || !errors.Is(err, ErrMetadataTooLarge) {
		t.Fatalf("expected ErrMetadataTooLarge with nil payload, got img!=nil=%v err=%v", img != nil, err)
	}
	if cnt.n > MaxMetadataBytes+64<<10 {
		t.Fatalf("read %d bytes, want ≤ %d", cnt.n, MaxMetadataBytes+64<<10)
	}
}

func TestLimitedBufferOverflowSemantics(t *testing.T) {
	// The (0, err) write semantics are load-bearing: image/jpeg's decoder fill
	// treats a read returning n > 0 together with an error as success, so a
	// partial write would swallow the abort and reads would continue past the
	// budget. Pin the contract directly with a non-aligned budget, independent
	// of decoder fill alignment (MaxMetadataBytes = 2048 × 4096 happens to be
	// aligned, which would mask a partial-write regression in the integration
	// tests).
	l := &limitedBuffer{remaining: 100}
	n, err := l.Write(make([]byte, 4096))
	if n != 0 || !errors.Is(err, errMetadataBudgetExceeded) || l.buf.Len() != 0 {
		t.Fatalf("overflow write must be (0, errBudget) writing nothing, got n=%d err=%v len=%d", n, err, l.buf.Len())
	}
	// Sticky: after the overflow the budget is zeroed, so even a small write
	// keeps failing (DecodeConfig aborts on the first overflow, so this only
	// matters for the writer contract itself).
	if n, err = l.Write(make([]byte, 1)); n != 0 || !errors.Is(err, errMetadataBudgetExceeded) {
		t.Fatalf("sticky failure expected, got n=%d err=%v", n, err)
	}
	// Exact-boundary acceptance: len(p) == remaining succeeds and decrements.
	l2 := &limitedBuffer{remaining: 4}
	if n, _ := l2.Write(make([]byte, 4)); n != 4 || l2.remaining != 0 {
		t.Fatalf("write exactly at remaining must succeed, got n=%d remaining=%d", n, l2.remaining)
	}
}

// endlessAPP1Stream yields a JPEG SOI followed by an endless sequence of valid
// APP1 segments and never returns EOF — the attacker's streaming flood shape
// for the budget path (the existing endlessZeros fixture never reaches the tee
// because the format sniff fails first).
type endlessAPP1Stream struct{ soiBytes int }

func (s *endlessAPP1Stream) Read(p []byte) (int, error) {
	n := 0
	for s.soiBytes < 2 && n < len(p) { // emit 0xFF 0xD8 one byte at a time
		if s.soiBytes == 0 {
			p[n] = 0xFF
		} else {
			p[n] = 0xD8
		}
		s.soiBytes++
		n++
	}
	for n < len(p) {
		avail := len(p) - n
		if avail < 4 { // no room for a segment header; caller re-Reads
			break
		}
		payload := avail - 4
		if payload > 4096 {
			payload = 4096
		}
		p[n], p[n+1] = 0xFF, 0xE1 // APP1
		binary.BigEndian.PutUint16(p[n+2:n+4], uint16(payload+2))
		for i := 0; i < payload; i++ {
			p[n+4+i] = 0x42
		}
		n += 4 + payload
	}
	return n, nil
}

func TestGenerateEndlessMetadata(t *testing.T) {
	// An endless stream of valid APP1 segments must abort at the budget with
	// ErrMetadataTooLarge (budget wins over EOF/unsupported) and bounded
	// reads — no hang, no full-stream consumption. The 128 MiB LimitReader
	// caps the stream, so this cannot hang the suite.
	cnt := &countingReader{r: &endlessAPP1Stream{}}
	img, err := Generate(cnt, 100, 100)
	if img != nil || !errors.Is(err, ErrMetadataTooLarge) {
		t.Fatalf("expected ErrMetadataTooLarge, got img!=nil=%v err=%v", img != nil, err)
	}
	if cnt.n > MaxMetadataBytes+64<<10 {
		t.Fatalf("read %d bytes, want ≤ %d", cnt.n, MaxMetadataBytes+64<<10)
	}
}

func TestGenerateMetadataAtExactBudget(t *testing.T) {
	// A fixture whose APP1 payloads sum to exactly MaxMetadataBytes still
	// rejects: the tee also counts the SOI, segment headers and the trailing
	// marker bytes after the last segment, so total consumption exceeds the
	// budget. Documents that the writer's accept-exactly semantics apply at
	// the write level, not the payload level.
	payload := appnPaddedJPEG(t, MaxMetadataBytes)
	img, err := Generate(bytes.NewReader(payload), 100, 100)
	if img != nil || !errors.Is(err, ErrMetadataTooLarge) {
		t.Fatalf("expected ErrMetadataTooLarge, got img!=nil=%v err=%v", img != nil, err)
	}
}

// maxSourceBytesJPEGPayload emits a valid JPEG prefix (SOI through the end of
// the SOS header) followed by endless zero bytes — a stream that passes
// DecodeConfig, completes the 8×8 scan, then feeds image/jpeg's liberal marker
// scan (extraneous non-marker data is silently ignored, stdlib reader.go)
// until the source cap. It never EOFs.
type maxSourceBytesJPEGPayload struct {
	prefix []byte
	off    int
}

func (s *maxSourceBytesJPEGPayload) Read(p []byte) (int, error) {
	n := copy(p, s.prefix[s.off:])
	s.off += n
	for i := n; i < len(p); i++ {
		p[i] = 0
	}
	return len(p), nil
}

// TestGenerateSourceBytesBound pins the MaxSourceBytes (128 MiB) read cap —
// the only budget branch the 64 KiB fuzz cap cannot reach. A payload that
// never terminates must abort at the cap with ErrUnsupported and bounded
// reads (no hang, no unbounded consumption). Deleting the LimitReader in
// Generate would fail this test and no other.
func TestGenerateSourceBytesBound(t *testing.T) {
	base := appnPaddedJPEG(t, 0) // plain 8×8 JPEG, zero APP1 segments
	i := bytes.Index(base, []byte{0xFF, 0xDA})
	if i < 0 {
		t.Fatal("no SOS marker in fixture")
	}
	n := int(binary.BigEndian.Uint16(base[i+2 : i+4]))
	prefix := base[:i+2+n] // entropy data cut; replaced by endless zeros
	cnt := &countingReader{r: &maxSourceBytesJPEGPayload{prefix: prefix}}
	img, err := Generate(cnt, 100, 100)
	if img != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported with nil payload, got img!=nil=%v err=%v", img != nil, err)
	}
	if cnt.n > MaxSourceBytes+64<<10 {
		t.Fatalf("read %d bytes, want <= %d", cnt.n, MaxSourceBytes+64<<10)
	}
	if cnt.n < MaxSourceBytes-1<<20 {
		t.Fatalf("read %d bytes: cap not reached (want ~%d)", cnt.n, MaxSourceBytes)
	}
}

func TestGenerateRejectsNonImage(t *testing.T) {
	if _, err := Generate(bytes.NewReader([]byte("not an image")), 100, 100); err != ErrUnsupported {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}

// FuzzGenerate drives Generate with arbitrary mutations of the existing
// fixture shapes, asserting the package's documented contract: no panic;
// errors are always nil or one of the three sentinels; a nil error yields a
// decodable JPEG no larger than the requested bounds. The 64 KiB input cap
// bounds per-iteration work (a hang surfaces as a fuzz worker timeout); the
// module's MaxSourceBytes/MaxMetadataBytes budgets and the MaxSourceDim
// pre-check bound the decoder side regardless of mutation. ErrMetadataTooLarge
// is unreachable under the 64 KiB cap (budget is 8 MiB) and is pinned by
// TestGenerateRejectsOversizedMetadata* / TestGenerateEndlessMetadata; it
// stays in the accepted set so coverage becomes automatic if budgets change.
// The error-set assertion depends on jpeg.Encode's error being unreachable:
// both of its error sources (the dims guard at >= 1<<16 and the sink write
// path) are structurally impossible here — post-scale bounds are <= 64x64
// and the decoded source is <= MaxSourceDim < 1<<16, and a bytes.Buffer
// never fails. Re-open deliberately if the sink or the scale bounds change.
// Run: go test -fuzz=FuzzGenerate -fuzztime=60s ./internal/thumbnail/
func FuzzGenerate(f *testing.F) {
	// Fixture builders are typed testing.TB (REQ-8) so *testing.F and
	// *testing.T both work (F embeds common, not T).
	f.Add(headerOnlyPNG(f, 8, 8, 8, 6))                                  // ErrUnsupported: no IDAT
	f.Add(headerOnlyPNG(f, 100000, 100000, 8, 6))                        // ErrImageTooLarge: dims > MaxSourceDim (33 B)
	f.Add(headerOnlyPNG(f, Max16BitSourceDim+1, 1024, 16, 6))            // ErrImageTooLarge: depth-16 > Max16BitSourceDim
	f.Add(headerOnlyPNG(f, Max16BitSourceDim, Max16BitSourceDim, 16, 6)) // boundary: ErrUnsupported (no IDAT)
	f.Add(appnPaddedJPEG(f, 1<<16))                                      // APP1 flood, truncated at the input cap
	prefix := make([]byte, 64<<10)
	_, _ = io.ReadFull(&endlessAPP1Stream{}, prefix) // streaming-flood shape, finite prefix
	f.Add(prefix)
	png := makePNG(f, 400, 200) // 744 B: known-good decode + downscale seed (REQ-4)
	f.Add(png)
	f.Add(png[:len(png)/2])                         // mid-IDAT truncation
	f.Add(headerOnlyProgressiveJPEG(f, 8192, 8192)) // ErrImageTooLarge: progressive > MaxProgressiveSourceDim (~130 B)
	f.Add(realProgressiveJPEG(f, 8, 8))             // valid progressive decode seed (338 B)
	// EXIF-carrying seeds (qa F2): a real JPEG with a valid orientation-6
	// APP1 spliced before the scan, and the same payload after a SOF0 header
	// (the walker must stop at the SOF family and ignore it).
	if jpg := realBaselineJPEG(f, 16, 16, 82); len(jpg) > 2 {
		payload := exifPayload(6, binary.LittleEndian)
		var exifJPEG []byte
		{
			var b bytes.Buffer
			b.Write([]byte{0xFF, 0xD8})
			var seg [4]byte
			seg[0], seg[1] = 0xFF, 0xE1
			binary.BigEndian.PutUint16(seg[2:4], uint16(len(payload)+2))
			b.Write(seg[:])
			b.Write(payload)
			b.Write(jpg[2:])
			exifJPEG = b.Bytes()
		}
		f.Add(exifJPEG)                     // valid decode + orientation-6 applied
		f.Add(jpegWithPostSOFExif(payload)) // post-SOF APP1 must be ignored
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		out, err := Generate(io.LimitReader(bytes.NewReader(data), 64<<10), 64, 64)
		if err != nil {
			if !errors.Is(err, ErrUnsupported) && !errors.Is(err, ErrImageTooLarge) &&
				!errors.Is(err, ErrMetadataTooLarge) {
				t.Fatalf("Generate returned non-sentinel error: %v", err)
			}
			return
		}
		img, format, derr := image.Decode(bytes.NewReader(out))
		if derr != nil || format != "jpeg" || img == nil ||
			img.Bounds().Dx() > 64 || img.Bounds().Dy() > 64 {
			dims := "nil"
			if img != nil {
				dims = img.Bounds().String()
			}
			t.Fatalf("invalid thumbnail: format=%q dims=%s decodeErr=%v", format, dims, derr)
		}
	})
}

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

func TestEffectiveDims(t *testing.T) {
	// The effective-bound rule is the single source of truth shared by
	// generateLocked (the decode pipeline) and the REST handler's cache
	// validator: every requested pair maps to exactly the pair the pipeline
	// applies. All pure comparisons — no arithmetic, no overflow at any int.
	cases := []struct {
		name         string
		w, h         int
		wantW, wantH int
	}{
		{"defaults", 0, 0, DefaultMax, DefaultMax},
		{"negative defaults", -5, 0, DefaultMax, DefaultMax},
		{"in-range identity", 256, 256, 256, 256},
		{"identity at HardMax", 2048, 2048, HardMax, HardMax},
		{"oversized w defaults h", 9999, 0, HardMax, DefaultMax},
		{"oversized both", 9999, 9999, HardMax, HardMax},
		{"small identity", 100, 100, 100, 100},
		{"independent clamp w", 2048, 100, HardMax, 100},
		{"independent clamp h", 100, 9999, 100, HardMax},
		{"max int clamped", 1 << 62, 1 << 62, HardMax, HardMax},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotW, gotH := EffectiveDims(c.w, c.h)
			if gotW != c.wantW || gotH != c.wantH {
				t.Fatalf("EffectiveDims(%d, %d) = (%d, %d), want (%d, %d)",
					c.w, c.h, gotW, gotH, c.wantW, c.wantH)
			}
		})
	}

	// Byte-stability through the refactor: clamping via EffectiveDims must
	// not change any produced bytes — Generate(9999, 9999) and
	// Generate(2048, 2048) already agreed before (both clamped to HardMax)
	// and must still agree after generateLocked delegates to EffectiveDims.
	src := makePNG(t, 3000, 2000)
	oversized, err := Generate(bytes.NewReader(src), 9999, 9999)
	if err != nil {
		t.Fatalf("Generate(9999, 9999): %v", err)
	}
	atHardMax, err := Generate(bytes.NewReader(src), 2048, 2048)
	if err != nil {
		t.Fatalf("Generate(2048, 2048): %v", err)
	}
	if !bytes.Equal(oversized, atHardMax) {
		t.Fatal("Generate(9999, 9999) and Generate(2048, 2048) differ; EffectiveDims changed produced bytes")
	}
}
