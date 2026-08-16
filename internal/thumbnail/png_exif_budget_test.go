package thumbnail

// PNG eXIf-walk budget pre-checks must credit the chunk bytes already
// resident in head (direction: "PNG eXIf-walk budget pre-checks over-reject
// head-resident chunks — false ErrMetadataTooLarge/413 on within-budget
// PNGs"). head bytes were already counted against MaxMetadataBytes by
// generateLocked's config-scan tee, so the declared-size pre-checks compare
// against remaining + headAvail; readN charges only r-bytes, so the fixed
// pre-check rejects iff the true r-cost exceeds the budget — never over-,
// never under-rejecting. T1 is the generate-level acceptance (AC-1), T2 the
// white-box boundary/charge-invariant table over both arms × both head
// shapes (AC-2), plus the headAvail=0 no-op control (REQ-F5).

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// TestGeneratePNGOrientationHeadResidentBudget is the generate-level
// acceptance pin (AC-1): a first post-IHDR tEXt chunk whose declared data
// length is MaxMetadataBytes−3000. The fixture ≫ 4 KiB makes DecodeConfig's
// 4096-byte bufio fill deterministic (head = 4096), so the chunk's leading
// 4063 bytes land in the tee head and headAvail = 4055 at the pre-check: the
// true r-cost (8,381,557 B) fits the r-budget even though the declared size
// exceeds it. Today's code falsely rejects with ErrMetadataTooLarge (REST
// 413); the fixed pre-check accepts and generates the thumbnail.
//
// Stdlib coupling (qa F4): the deterministic head = 4096 is bufio's
// defaultBufSize (image/format.go's asReader wraps DecodeConfig's reader in
// bufio.NewReader; the same coupling TestGeneratePNGMetadataAtExactBudget
// relies on). If Go ever changes the fill size, the seam arithmetic here
// shifts silently — T2's white-box table below (explicit head/off, no bufio)
// is the seam source of truth, and this generate-level test is its e2e
// mirror: the fixture-sanity guards in T2 keep the boundary pinned either
// way.
func TestGeneratePNGOrientationHeadResidentBudget(t *testing.T) {
	base := makePNG(t, 64, 64)

	// The fix's target: a tEXt chunk of MaxMetadataBytes−3000 declared bytes
	// as the FIRST post-IHDR chunk (byte 33), the direction's worked example.
	bigText := append([]byte("Comment\x00"), make([]byte, MaxMetadataBytes-3000-len("Comment\x00"))...)
	fixture := splicePNGChunk(t, base, "tEXt", bigText)

	cnt := &countingReader{r: bytes.NewReader(fixture)}
	out, err := Generate(cnt, 64, 64)
	if err != nil {
		t.Fatalf("Generate (head-resident within-budget tEXt): %v", err)
	}
	img, format, err := image.Decode(bytes.NewReader(out))
	if err != nil || format != "jpeg" {
		t.Fatalf("decode thumb: %v fmt=%s", err, format)
	}
	if img.Bounds().Dx() != 64 || img.Bounds().Dy() != 64 {
		t.Fatalf("thumb dims %dx%d want 64x64", img.Bounds().Dx(), img.Bounds().Dy())
	}
	if cnt.n > MaxMetadataBytes+64<<10 {
		t.Fatalf("read %d bytes, want ≤ %d", cnt.n, MaxMetadataBytes+64<<10)
	}

	// No-under-rejection control: one structure byte beyond the credited
	// budget (length+4 = remaining + headAvail + 1) must still reject with
	// ErrMetadataTooLarge and nil payload — the fix must not make the walk
	// lenient.
	overData := append([]byte("Comment\x00"), make([]byte, MaxMetadataBytes-44-len("Comment\x00"))...)
	overFixture := splicePNGChunk(t, base, "tEXt", overData)
	cnt2 := &countingReader{r: bytes.NewReader(overFixture)}
	img2, err := Generate(cnt2, 64, 64)
	if img2 != nil || !errors.Is(err, ErrMetadataTooLarge) {
		t.Fatalf("over-by-one: expected ErrMetadataTooLarge with nil payload, got img!=nil=%v err=%v", img2 != nil, err)
	}
	if cnt2.n > MaxMetadataBytes+64<<10 {
		t.Fatalf("over-by-one read %d bytes, want ≤ %d", cnt2.n, MaxMetadataBytes+64<<10)
	}
}

// TestPNGOrientationHeadResidentBudgetBoundary is the white-box boundary
// table (AC-2): for chunks straddling the head/r seam the total charge
// (len(head) + r-bytes) must never exceed MaxMetadataBytes — at-cap lands
// exactly on the budget and succeeds, one structure byte more is rejected
// before any r-read of the over-budget chunk. Both pre-check arms (eXIf,
// tEXt skip) × both head shapes (non-paletted 33+read-ahead, paletted
// wide-window) are covered, plus the headAvail = 0 no-op control (REQ-F5).
func TestPNGOrientationHeadResidentBudgetBoundary(t *testing.T) {
	ihdr := makePNG(t, 64, 64)[:33]
	_, plte := palettedBase(t)
	trns := pngChunkBytes(t, "tRNS", []byte{255, 255})
	// Paletted wide-window prefix: IHDR + PLTE + a 4 MiB tEXt chunk (fully
	// resident, declared size > remaining — the false-rejection headline)
	// + tRNS, the shape image/png's config loop accumulates into head. This
	// head is ARTIFICIALLY large: production PNG heads are ≤ 4096 B
	// (DecodeConfig's bufio fill), so the >4 MiB shape is an accounting-
	// invariant pin, not a production-reachable scenario (the production bug
	// is the 4 KiB seam-straddle, T1).
	var palettedWide bytes.Buffer
	palettedWide.Write(ihdr)
	palettedWide.Write(plte)
	palettedWide.Write(pngChunkBytes(t, "tEXt", make([]byte, 4<<20)))
	palettedWide.Write(trns)

	tests := []struct {
		name        string
		prefix      []byte
		chunkType   string
		headDataLen int // target-chunk data bytes resident in head
		isExif      bool
		wantOrient  int
	}{
		{"exif non-paletted seam", ihdr, "eXIf", 4055, true, 6},
		{"exif paletted wide", palettedWide.Bytes(), "eXIf", 2048, true, 6},
		{"text non-paletted seam", ihdr, "tEXt", 4055, false, 1},
		{"text paletted wide", palettedWide.Bytes(), "tEXt", 2048, false, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			off := len(tt.prefix) + 8 // head offset at the chunk data start
			headLen := len(tt.prefix) + 8 + tt.headDataLen
			headAvail := headLen - off // = tt.headDataLen
			remaining := MaxMetadataBytes - headLen

			// At-cap declared length: the credited bound (remaining +
			// headAvail = MaxMetadataBytes − off) minus the 8-byte IDAT
			// header the tEXt arm must still charge (the eXIf arm stops at
			// the chunk data itself).
			atCap := remaining + headAvail
			if !tt.isExif {
				atCap -= 12
			}
			// The at-cap fixture must be genuinely head-resident-meaningful:
			// its declared size exceeds the uncredited r-budget (old code
			// rejects it) while the true r-cost fits (fixed code accepts).
			if tt.isExif {
				if atCap <= remaining {
					t.Fatalf("fixture not meaningful: declared %d ≤ remaining %d", atCap, remaining)
				}
			} else if atCap+4 <= remaining {
				t.Fatalf("fixture not meaningful: declared+4 %d ≤ remaining %d", atCap+4, remaining)
			}

			build := func(declared int) ([]byte, []byte) {
				var data []byte
				if tt.isExif {
					payload := bareExifPayload(6, binary.LittleEndian)
					data = make([]byte, declared)
					copy(data, payload) // TIFF structure head-resident; padding ignored by the parser
				} else {
					data = make([]byte, declared)
				}
				chunk := pngChunkBytes(t, tt.chunkType, data)
				head := make([]byte, 0, len(tt.prefix)+8+tt.headDataLen)
				head = append(head, tt.prefix...)
				head = append(head, chunk[:8+tt.headDataLen]...)
				r := append([]byte(nil), chunk[8+tt.headDataLen:]...) // data tail (+ CRC)
				if !tt.isExif {
					r = append(r, pngChunkHeader(t, "IDAT", 0)...) // terminate at IDAT
				}
				return head, r
			}

			// At-cap: the seam-straddling chunk lands exactly on the budget —
			// total charge (head + r-bytes) reaches MaxMetadataBytes without
			// breaching it (REQ-F4) and the walk succeeds.
			head, r := build(atCap)
			cnt := &countingReader{r: bytes.NewReader(r)}
			orient, err := pngOrientation(context.Background(), head, cnt)
			if err != nil || orient != tt.wantOrient {
				t.Fatalf("at-cap: orient=%d err=%v want %d,nil", orient, err, tt.wantOrient)
			}
			if cnt.n != int64(remaining) {
				t.Fatalf("at-cap r-charge %d, want remaining %d", cnt.n, remaining)
			}
			if int64(len(head))+cnt.n != MaxMetadataBytes {
				t.Fatalf("at-cap total charge %d, want MaxMetadataBytes %d", int64(len(head))+cnt.n, MaxMetadataBytes)
			}

			// Over-by-one: one structure byte beyond the credited bound is
			// rejected BEFORE any r-read of the over-budget chunk (REQ-F3's
			// pre-read guard) — no under-rejection (REQ-F4).
			over := atCap + 1
			if !tt.isExif {
				over = atCap + 9 // length+4 = remaining + headAvail + 1
			}
			head2, r2 := build(over)
			cnt2 := &countingReader{r: bytes.NewReader(r2)}
			if _, err := pngOrientation(context.Background(), head2, cnt2); !errors.Is(err, errMetadataBudgetExceeded) {
				t.Fatalf("over-by-one: err=%v want errMetadataBudgetExceeded", err)
			}
			if cnt2.n != 0 {
				t.Fatalf("over-by-one: %d r-bytes read, want 0 (pre-read guard)", cnt2.n)
			}
		})
	}

	// The paletted wide-window headline: a chunk whose declared size exceeds
	// the r-budget but is FULLY resident in head is accepted with zero
	// r-charge for the chunk itself — only the terminating IDAT header is
	// read from r. (White-box only: with production head ≤ 4096 a chunk fully
	// resident in head can never exceed the r-budget, so this row pins the
	// accounting invariant, not a production path.)
	t.Run("text paletted wide fully in head", func(t *testing.T) {
		head := palettedWide.Bytes() // IHDR + PLTE + 4 MiB tEXt + tRNS, all resident
		remaining := MaxMetadataBytes - len(head)
		if remaining < 0 {
			t.Fatalf("head %d exceeds MaxMetadataBytes", len(head))
		}
		// The resident tEXt's declared size must exceed the uncredited
		// r-budget for the row to be regression-meaningful: the old code
		// rejects it; the fixed pre-check credits the head-resident bytes.
		if int64(4<<20)+4 <= int64(remaining) {
			t.Fatalf("fixture not wide: tEXt %d+4 ≤ remaining %d", 4<<20, remaining)
		}
		cnt := &countingReader{r: bytes.NewReader(pngChunkHeader(t, "IDAT", 0))}
		orient, err := pngOrientation(context.Background(), head, cnt)
		if err != nil || orient != 1 {
			t.Fatalf("fully-head-resident tEXt: orient=%d err=%v want 1,nil", orient, err)
		}
		if cnt.n != 8 {
			t.Fatalf("r-charge %d, want 8 (IDAT header only)", cnt.n)
		}
		if int64(len(head))+cnt.n > MaxMetadataBytes {
			t.Fatalf("total charge %d exceeds MaxMetadataBytes", int64(len(head))+cnt.n)
		}
	})

	// The eXIf-arm analogue of the fully-in-head headline (qa F2): a chunk
	// whose declared size exceeds the r-budget but is FULLY resident in head
	// is accepted through the free-slice path (data = c.head[c.off:c.off+len],
	// no allocation, no read) — the walk returns at the eXIf chunk itself, so
	// zero r-bytes are consumed (no IDAT-header read, unlike the tEXt arm).
	// Unreachable in the real pipeline today (production head ≤ 4096 ⟹ eXIf
	// length ≤ 4096 ≪ remaining, so the old code never rejected it), but any
	// change to head size or MaxMetadataBytes makes it reachable — the only
	// accept-flip case of the eXIf arm without a dedicated pin.
	t.Run("exif fully in head declared over budget", func(t *testing.T) {
		const dataLen = 5 << 20 // 5 MiB > remaining ≈ 3 MiB, fully in head
		head := make([]byte, 0, 41+dataLen)
		head = append(head, ihdr...)
		payload := bareExifPayload(6, binary.LittleEndian)
		data := make([]byte, dataLen)
		copy(data, payload)                                    // TIFF structure head-resident; padding ignored by the parser
		head = append(head, pngChunkBytes(t, "eXIf", data)...) // header + data + CRC, all resident
		remaining := MaxMetadataBytes - len(head)
		if int64(dataLen) <= int64(remaining) {
			t.Fatalf("fixture not wide: declared %d ≤ remaining %d", dataLen, remaining)
		}
		cnt := &countingReader{r: bytes.NewReader(pngChunkHeader(t, "IDAT", 0))}
		orient, err := pngOrientation(context.Background(), head, cnt)
		if err != nil || orient != 6 {
			t.Fatalf("fully-head-resident eXIf: orient=%d err=%v want 6,nil", orient, err)
		}
		if cnt.n != 0 {
			t.Fatalf("r-charge %d, want 0 (free-slice path, no read)", cnt.n)
		}
		if int64(len(head)) > MaxMetadataBytes {
			t.Fatalf("head %d exceeds MaxMetadataBytes", len(head))
		}
	})

	// No-op control (REQ-F5): the chunk starts exactly at the seam
	// (headAvail = 0) — the pre-checks are byte-identical to today's code,
	// and the at-cap fixture still lands exactly on the budget.
	t.Run("text no-op headAvail zero", func(t *testing.T) {
		head := append([]byte(nil), ihdr...)
		off := len(head) + 8 // chunk header fully in r → headAvail = 0
		atCap := MaxMetadataBytes - off - 12
		chunk := pngChunkBytes(t, "tEXt", make([]byte, atCap))
		r := append(append([]byte(nil), chunk...), pngChunkHeader(t, "IDAT", 0)...)
		cnt := &countingReader{r: bytes.NewReader(r)}
		orient, err := pngOrientation(context.Background(), head, cnt)
		if err != nil || orient != 1 {
			t.Fatalf("no-op at-cap: orient=%d err=%v want 1,nil", orient, err)
		}
		if cnt.n != int64(MaxMetadataBytes-len(head)) {
			t.Fatalf("r-charge %d, want remaining %d", cnt.n, MaxMetadataBytes-len(head))
		}
		if int64(len(head))+cnt.n != MaxMetadataBytes {
			t.Fatalf("total charge %d, want MaxMetadataBytes", int64(len(head))+cnt.n)
		}
	})
}

// palettedBase returns the 33-byte IHDR prefix and the full PLTE chunk of a
// real 2-color paletted PNG (png.Encode of an image.Paletted writes IHDR,
// PLTE, IDAT, IEND) — the paletted head-prefix material the wide-window rows
// build on.
func palettedBase(t testing.TB) (ihdr, plte []byte) {
	t.Helper()
	pal := color.Palette{
		color.RGBA{255, 0, 0, 255},
		color.RGBA{0, 0, 255, 255},
	}
	img := image.NewPaletted(image.Rect(0, 0, 4, 4), pal)
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.SetColorIndex(x, y, uint8((x+y)%2))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode paletted png: %v", err)
	}
	base := buf.Bytes()
	return base[:33], extractChunk(t, base, "PLTE")
}

// extractChunk returns the complete chunk bytes (header + data + CRC) of the
// first occurrence of the named chunk type in a png.Encode-produced PNG.
func extractChunk(t testing.TB, data []byte, name string) []byte {
	t.Helper()
	for i := 8; i+8 <= len(data); {
		length := int(binary.BigEndian.Uint32(data[i : i+4]))
		if string(data[i+4:i+8]) == name {
			end := i + 8 + length + 4
			if end > len(data) {
				t.Fatalf("chunk %q overruns fixture", name)
			}
			return data[i:end]
		}
		i += 8 + length + 4
	}
	t.Fatalf("chunk %q not found", name)
	return nil
}
