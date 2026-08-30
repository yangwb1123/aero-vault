package thumbnail

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"runtime"
	"testing"
)

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
	start := s.off
	if start > len(s.prefix) {
		start = len(s.prefix)
	}
	n := copy(p, s.prefix[start:])
	for i := n; i < len(p); i++ {
		p[i] = 0
	}
	s.off += len(p) // zeros count toward the total
	return len(p), nil
}

// TestGenerateSourceBytesBound pins the MaxSourceBytes (128 MiB) read cap —
// the only budget branch the 64 KiB fuzz cap cannot reach. A payload that
// never terminates must abort at the cap with ErrSourceTooLarge — a
// server-side processing-budget rejection, distinct from the corrupt-input
// class — and bounded reads (no hang, no unbounded consumption). Deleting the
// LimitReader in Generate would fail this test and no other.
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
	if img != nil || !errors.Is(err, ErrSourceTooLarge) {
		t.Fatalf("expected ErrSourceTooLarge with nil payload, got img!=nil=%v err=%v", img != nil, err)
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
