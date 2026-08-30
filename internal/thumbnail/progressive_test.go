package thumbnail

import (
	"bytes"
	"errors"
	"image"
	"runtime"
	"testing"
)

// TestProgressiveJPEGHeaderWalk pins the segment-aware SOF2 detection: the
// verdict must come from the first SOF-family marker, never from payload or
// entropy bytes. A regression to a naive bytes.Contains fails controls 2/3/8.
func TestProgressiveJPEGHeaderWalk(t *testing.T) {
	// (1) Real baseline (SOF0) → not progressive.
	baseline := realBaselineJPEG(t, 1024, 1024, 82)
	if progressiveJPEG(baseline) {
		t.Fatal("baseline SOF0 reported progressive")
	}
	// (2)/(3) Baseline with APP1 payloads containing 0xFF 0xC2 (XMP/EXIF/ICC
	// class) → still not progressive: segments are skipped by length.
	ffc2 := []byte{0xFF, 0xC2}
	if progressiveJPEG(spliceAPP1(t, baseline, bytes.Repeat(ffc2, 8), 16)) {
		t.Fatal("APP1{FF C2} + baseline reported progressive")
	}
	if progressiveJPEG(spliceAPP1(t, baseline, bytes.Repeat(ffc2, 32), 5*64)) {
		t.Fatal("5× APP1{FF C2} + baseline reported progressive")
	}
	// (4) Real progressive → progressive.
	prog := realProgressiveJPEG(t, 64, 64)
	if !progressiveJPEG(prog) {
		t.Fatal("real progressive SOF2 not reported progressive")
	}
	// (5) Progressive + trailing entropy 0xFF 0xC2 → still progressive: the
	// walk stops at the first SOF marker and never reaches entropy data.
	trailing := append(append([]byte{}, prog...), 0xFF, 0xC2)
	if !progressiveJPEG(trailing) {
		t.Fatal("progressive with trailing entropy not reported progressive")
	}
	// (6) Truncated/markerless SOI → not progressive (defensive).
	if progressiveJPEG([]byte{0xFF}) {
		t.Fatal("truncated SOI reported progressive")
	}
	if progressiveJPEG([]byte{0xFF, 0xD8}) {
		t.Fatal("SOI-only reported progressive")
	}
	// (7) PNG bytes → not progressive (the format gate's double-check).
	if progressiveJPEG(makePNG(t, 8, 8)) {
		t.Fatal("PNG reported progressive")
	}
	// (8) Baseline with dims above MaxProgressiveSourceDim → not progressive
	// (the walk is dims-agnostic; the dims decision lives in GenerateContext).
	if progressiveJPEG(realBaselineJPEG(t, 4097, 100, 82)) {
		t.Fatal("baseline 4097x100 reported progressive")
	}
}

// TestGenerateRejectsOversizedProgressiveJPEG is acceptance AC1: a
// progressive (SOF2) JPEG at 8192×8192 rejects with the dimension-class
// sentinel before full decode, with bounded reads.
func TestGenerateRejectsOversizedProgressiveJPEG(t *testing.T) {
	// Fixture self-check: DecodeConfig must expose exactly the declared dims
	// (the config path parses SOF2 without DQT/entropy).
	fix := headerOnlyProgressiveJPEG(t, 8192, 8192)
	cfg, format, err := image.DecodeConfig(bytes.NewReader(fix))
	if err != nil || format != "jpeg" || cfg.Width != 8192 || cfg.Height != 8192 {
		t.Fatalf("fixture self-check: got %dx%d %q err=%v", cfg.Width, cfg.Height, format, err)
	}

	// (a) 8192² progressive + poisoned payload: rejected with the dimension
	// sentinel, nil output, bounded reads (the junk payload is never
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

	// (b) In-budget APP1 metadata flood still rejects on dims (the budget
	// abort fires inside DecodeConfig, so this arm exercises the dims check
	// with a large but in-budget head).
	padded := appnPaddedProgressiveJPEG(t, 8192, 8192, 1<<20)
	cnt2 := &countingReader{r: bytes.NewReader(padded)}
	img, err = Generate(cnt2, 100, 100)
	if img != nil || !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("APP1-padded: expected ErrImageTooLarge, got img!=nil=%v err=%v", img != nil, err)
	}
	if cnt2.n > 1<<20+64<<10 {
		t.Fatalf("APP1-padded: read %d bytes, want ≤ %d", cnt2.n, 1<<20+64<<10)
	}

	// (c) No payload buffering: total allocated bytes stay below the 16 MiB
	// junk payload (TotalAlloc is monotonic; the fixed-path allocation is
	// only the bounded head buffer, and junk was built before m0).
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

	// (d) Over-budget metadata wins: the APP1 flood aborts with
	// ErrMetadataTooLarge inside DecodeConfig before any dims decision.
	flood := appnPaddedProgressiveJPEG(t, 8192, 8192, MaxMetadataBytes+1<<20)
	img, err = Generate(bytes.NewReader(flood), 100, 100)
	if img != nil || !errors.Is(err, ErrMetadataTooLarge) {
		t.Fatalf("budget arm: expected ErrMetadataTooLarge, got img!=nil=%v err=%v", img != nil, err)
	}
}

// TestGenerateProgressiveJPEGWithinBound is acceptance AC2, first half: a
// progressive JPEG at 1024×1024 still thumbnails, and the boundary pins hold
// (exactly MaxProgressiveSourceDim accepted; one pixel over rejected).
func TestGenerateProgressiveJPEGWithinBound(t *testing.T) {
	// (a) Real progressive 1024×1024 → valid JPEG thumbnail ≤ bounds.
	prog := realProgressiveJPEG(t, 1024, 1024)
	if cfg, format, err := image.DecodeConfig(bytes.NewReader(prog)); err != nil ||
		format != "jpeg" || cfg.Width != 1024 || cfg.Height != 1024 {
		t.Fatalf("fixture self-check: got %dx%d %q err=%v", cfg.Width, cfg.Height, format, err)
	}
	out, err := Generate(bytes.NewReader(prog), 256, 256)
	if err != nil {
		t.Fatalf("generate progressive 1024x1024: %v", err)
	}
	img, format, err := image.Decode(bytes.NewReader(out))
	if err != nil || format != "jpeg" {
		t.Fatalf("thumbnail not a decodable jpeg: format=%q err=%v", format, err)
	}
	if img.Bounds().Dx() > 256 || img.Bounds().Dy() > 256 {
		t.Fatalf("thumbnail exceeds bounds: %s", img.Bounds())
	}

	// (b) Boundary pins: exactly MaxProgressiveSourceDim is accepted (the
	// truncated stream falls through to ErrUnsupported, NOT ErrImageTooLarge);
	// one pixel over on either axis is rejected.
	if _, err := Generate(bytes.NewReader(headerOnlyProgressiveJPEG(t, MaxProgressiveSourceDim, MaxProgressiveSourceDim)), 100, 100); errors.Is(err, ErrImageTooLarge) {
		t.Fatal("progressive at exactly MaxProgressiveSourceDim must not be rejected")
	}
	if _, err := Generate(bytes.NewReader(headerOnlyProgressiveJPEG(t, MaxProgressiveSourceDim+1, 1024)), 100, 100); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("progressive above MaxProgressiveSourceDim: expected ErrImageTooLarge, got %v", err)
	}
	if _, err := Generate(bytes.NewReader(headerOnlyProgressiveJPEG(t, 1024, MaxProgressiveSourceDim+1)), 100, 100); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("progressive height above MaxProgressiveSourceDim: expected ErrImageTooLarge, got %v", err)
	}
}

// TestGenerateBaselineAboveProgressiveBoundDecodes is acceptance AC2, second
// half (cheap arm): a baseline (SOF0) JPEG just above MaxProgressiveSourceDim
// must still decode — only SOF2 sources are tightened.
func TestGenerateBaselineAboveProgressiveBoundDecodes(t *testing.T) {
	base := realBaselineJPEG(t, 4097, 1024, 82)
	out, err := Generate(bytes.NewReader(base), 256, 256)
	if err != nil {
		t.Fatalf("baseline 4097x1024 must decode: %v", err)
	}
	img, format, err := image.Decode(bytes.NewReader(out))
	if err != nil || format != "jpeg" {
		t.Fatalf("thumbnail not a decodable jpeg: format=%q err=%v", format, err)
	}
	if img.Bounds().Dx() > 256 || img.Bounds().Dy() > 256 {
		t.Fatalf("thumbnail exceeds bounds: %s", img.Bounds())
	}
}

// TestGenerateBaselineAtMaxSourceDimDecodes is acceptance AC2, boundary arm:
// the MaxSourceDim boundary itself must remain baseline-decodable — the SOF2
// cap must not collateralize SOF0 sources at the top of the range. The
// transient (~275 MiB progressive coefficient buffers + ~256 MiB decoded 8-bit frame, ≈ 0.5–1 GiB peak) is
// inherent to proving the boundary and runs once per go test; it is skipped
// under -short so make check's race arm (test-race-thumbnail, -race -short
// -timeout 120s) stays within budget — same convention as the aggregate
// semaphore test (semaphore_test.go).
func TestGenerateBaselineAtMaxSourceDimDecodes(t *testing.T) {
	if testing.Short() {
		t.Skip("8192x8192 baseline decode transient (~0.5-1 GiB) skipped under -short")
	}
	base := realBaselineJPEG(t, MaxSourceDim, MaxSourceDim, 1)
	out, err := Generate(bytes.NewReader(base), 256, 256)
	if err != nil {
		t.Fatalf("baseline at MaxSourceDim must decode: %v", err)
	}
	img, format, err := image.Decode(bytes.NewReader(out))
	if err != nil || format != "jpeg" {
		t.Fatalf("thumbnail not a decodable jpeg: format=%q err=%v", format, err)
	}
	if img.Bounds().Dx() > 256 || img.Bounds().Dy() > 256 {
		t.Fatalf("thumbnail exceeds bounds: %s", img.Bounds())
	}
}

// TestProgressiveJPEGDefensiveBranches pins the six defensive branches of
// the marker walk (T1): each malformed header must be rejected as
// not-progressive (behavior equals today's), never panic, never scan into
// entropy data, and never reach a SOF verdict through a truncated/abnormal
// path.
func TestProgressiveJPEGDefensiveBranches(t *testing.T) {
	cases := []struct {
		name string
		head []byte
	}{
		{
			// Fill-run tail: the 0xFF run consumes the buffer, i >= len.
			name: "fill-run tail",
			head: []byte{0xFF, 0xD8, 0xFF, 0xFF, 0xFF},
		},
		{
			// Byte-stuffed 0xFF 0x00 at marker level: unparseable.
			name: "byte-stuffed marker",
			head: []byte{0xFF, 0xD8, 0xFF, 0x00, 0xFF, 0xC2},
		},
		{
			// EOI before any SOF: the walk stops, never a verdict.
			name: "EOI before SOF",
			head: []byte{0xFF, 0xD8, 0xFF, 0xD9, 0xFF, 0xC2},
		},
		{
			// SOS before any SOF: never scan past the SOS header into
			// entropy-coded data where 0xFF 0xC2 is ordinary data.
			name: "SOS before SOF",
			head: []byte{0xFF, 0xD8, 0xFF, 0xDA, 0xFF, 0xC2},
		},
		{
			// Standalone marker (TEM): skipped, the walk continues and the
			// SOF0 verdict wins.
			name: "TEM then SOF0",
			head: []byte{0xFF, 0xD8, 0xFF, 0x01, 0xFF, 0xC0},
		},
		{
			// Truncated segment length: i+2 overruns the buffer.
			name: "truncated length",
			head: []byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00},
		},
		{
			// Segment claims a length shorter than its own length field
			// (n < 2).
			name: "impossible length",
			head: []byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x01},
		},
		{
			// Segment length overruns the buffer (i+n > len).
			name: "length overrun",
			head: []byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x04},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if progressiveJPEG(tc.head) {
				t.Fatalf("malformed header % x reported progressive", tc.head)
			}
		})
	}
}

// TestGenerateAllowsSOF1AboveProgressiveDim pins the SOF1 control (C2): the
// progressive cap is specific to SOF2. A header-only SOF1 JPEG with dims in
// (MaxProgressiveSourceDim, MaxSourceDim] must pass the progressive gate and
// reach full decode (which fails on the missing tables → ErrUnsupported),
// never ErrImageTooLarge.
func TestGenerateAllowsSOF1AboveProgressiveDim(t *testing.T) {
	fix := headerOnlySOF1JPEG(t, MaxProgressiveSourceDim+1, 100)
	cfg, format, err := image.DecodeConfig(bytes.NewReader(fix))
	if err != nil || format != "jpeg" || cfg.Width != MaxProgressiveSourceDim+1 {
		t.Fatalf("fixture self-check: got %dx%d %q err=%v", cfg.Width, cfg.Height, format, err)
	}
	img, err := Generate(bytes.NewReader(fix), 100, 100)
	if img != nil || errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("SOF1 %dx%d: img!=nil=%v err=%v — want non-ErrImageTooLarge (passes the progressive gate, fails later on missing tables)", cfg.Width, cfg.Height, img != nil, err)
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("SOF1 err=%v want ErrUnsupported (header-only fixture decodes nowhere)", err)
	}
	// The same dims as SOF2 must still reject (control direction).
	prog := headerOnlyProgressiveJPEG(t, MaxProgressiveSourceDim+1, 100)
	if _, err := Generate(bytes.NewReader(prog), 100, 100); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("SOF2 same dims: err=%v want ErrImageTooLarge", err)
	}
	// At or below the cap, SOF2 is allowed through the gate (fails later on
	// missing tables, proving the rejection is dims-driven, not marker-only).
	progOK := headerOnlyProgressiveJPEG(t, MaxProgressiveSourceDim, 100)
	if _, err := Generate(bytes.NewReader(progOK), 100, 100); errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("SOF2 at cap: err=%v — must not reject", err)
	}
}
