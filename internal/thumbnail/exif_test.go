package thumbnail

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

// exifPayload builds an EXIF APP1 payload declaring orientation o (1–8):
// "Exif\x00\x00" + TIFF header (byte order per bo, magic 0x2A, IFD0 at
// offset 8) + a single IFD0 entry {tag 0x0112, type SHORT(3), count 1,
// value o} + next-IFD 0. All TIFF offsets are relative to the TIFF header.
func exifPayload(o int, bo binary.ByteOrder) []byte {
	p := make([]byte, 32)
	copy(p, exifSignature)
	if bo == binary.BigEndian {
		p[6], p[7] = 'M', 'M'
	} else {
		p[6], p[7] = 'I', 'I'
	}
	bo.PutUint16(p[8:10], 0x2A)    // magic
	bo.PutUint32(p[10:14], 8)      // IFD0 offset (TIFF-relative)
	bo.PutUint16(p[14:16], 1)      // one entry
	bo.PutUint16(p[16:18], 0x0112) // orientation tag
	bo.PutUint16(p[18:20], 3)      // type SHORT
	bo.PutUint32(p[20:24], 1)      // count 1
	bo.PutUint16(p[24:26], uint16(o))
	return p
}

// jpegWithAPP1 wraps a header-only JPEG (SOI + APP1(payload) + EOI) — enough
// for exifOrientation, which never reads past the segment walk.
func jpegWithAPP1(payload []byte) []byte {
	return jpegWithAPP1Len(payload, uint16(len(payload)+2))
}

// jpegWithAPP1Len is jpegWithAPP1 with an explicit (possibly lying) 16-bit
// segment length.
func jpegWithAPP1Len(payload []byte, length uint16) []byte {
	var out bytes.Buffer
	out.Write([]byte{0xFF, 0xD8}) // SOI
	var seg [4]byte
	seg[0], seg[1] = 0xFF, 0xE1 // APP1
	binary.BigEndian.PutUint16(seg[2:4], length)
	out.Write(seg[:])
	out.Write(payload)
	out.Write([]byte{0xFF, 0xD9}) // EOI
	return out.Bytes()
}

// jpegWithAPP1s is jpegWithAPP1 with several consecutive APP1 segments.
func jpegWithAPP1s(payloads ...[]byte) []byte {
	var out bytes.Buffer
	out.Write([]byte{0xFF, 0xD8}) // SOI
	for _, payload := range payloads {
		var seg [4]byte
		seg[0], seg[1] = 0xFF, 0xE1
		binary.BigEndian.PutUint16(seg[2:4], uint16(len(payload)+2))
		out.Write(seg[:])
		out.Write(payload)
	}
	out.Write([]byte{0xFF, 0xD9}) // EOI
	return out.Bytes()
}

// jpegWithPostSOSExif builds SOI + SOS segment + entropy-coded data carrying
// a fake APP1 + EXIF payload: the walker must stop at SOS and never scan the
// entropy region, where 0xFF 0xE1 pairs are ordinary data.
func jpegWithPostSOSExif(entropy []byte) []byte {
	var out bytes.Buffer
	out.Write([]byte{0xFF, 0xD8}) // SOI
	out.Write([]byte{0xFF, 0xDA, 0x00, 0x08, 0x01, 0x01, 0x00, 0x00, 0x3F, 0x00})
	out.Write(entropy)
	return out.Bytes()
}

// orientationJPEG builds a w×h baseline JPEG with a red top half and blue
// bottom half, optionally spliced immediately after SOI with an EXIF APP1
// declaring orient (1–8; 0 = no APP1 at all). This is the synthetic-fixture
// pattern established by appnPaddedJPEG/spliceAPP1: jpeg.Encode in-test +
// APP1 splice.
func orientationJPEG(t *testing.T, w, h, orient int) []byte {
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
	if err := jpeg.Encode(&base, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode base jpeg: %v", err)
	}
	if orient < 1 || orient > 8 {
		return base.Bytes()
	}
	b := base.Bytes()
	var out bytes.Buffer
	out.Write(b[:2]) // SOI; the EXIF APP1 splices in before the rest.
	payload := exifPayload(orient, binary.LittleEndian)
	var seg [4]byte
	seg[0], seg[1] = 0xFF, 0xE1
	binary.BigEndian.PutUint16(seg[2:4], uint16(len(payload)+2))
	out.Write(seg[:])
	out.Write(payload)
	out.Write(b[2:])
	return out.Bytes()
}

// assertThumbColor fails unless the decoded thumbnail pixel at (x, y) is
// clearly red or blue, using interior sampling + generous lossy-JPEG
// thresholds (callers must stay away from color boundaries and edges).
func assertThumbColor(t *testing.T, img image.Image, x, y int, want string) {
	t.Helper()
	r, g, b, _ := img.At(x, y).RGBA()
	r8, g8, b8 := r>>8, g>>8, b>>8
	if want == "red" && (r8 <= 180 || b8 >= 100) {
		t.Fatalf("pixel(%d,%d): r=%d g=%d b=%d want red", x, y, r8, g8, b8)
	}
	if want == "blue" && (b8 <= 180 || r8 >= 100) {
		t.Fatalf("pixel(%d,%d): r=%d g=%d b=%d want blue", x, y, r8, g8, b8)
	}
}

// AC-1: a 400×200 red-top/blue-bottom JPEG with EXIF orientation 6 must
// thumbnail to exactly 64×128 (the swapped box), upright (blue left, red
// right) — 90° CW. The absent-EXIF twin must stay 256×128 red-top/blue-bottom.
func TestGenerateAppliesEXIFOrientation6(t *testing.T) {
	fixture := orientationJPEG(t, 400, 200, 6)
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
	// half is blue and the right half red, with the boundary at c=32.
	for _, c := range []int{0, 8, 16, 24, 31} {
		assertThumbColor(t, img, c, 64, "blue")
	}
	for _, c := range []int{48, 56, 60, 63} {
		assertThumbColor(t, img, c, 64, "red")
	}

	// Absent-EXIF twin: no rotation, no box swap — 256×128, red on top.
	plain := orientationJPEG(t, 400, 200, 0)
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

// AC-2: orientation-1 EXIF must be a true no-op — byte-identical to the
// absent-EXIF output (which is exactly today's encoder path).
func TestGenerateOrientation1OutputUnchanged(t *testing.T) {
	plain := orientationJPEG(t, 64, 64, 0)
	ori1 := orientationJPEG(t, 64, 64, 1)
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

// TestApplyOrientationTable pins the full EXIF transform table on a small
// asymmetric 3×2 image: out dims per the table and every pixel mapped via
// the table formulas; orientation 1 returns the input unchanged (same
// pointer — byte-identity path).
func TestApplyOrientationTable(t *testing.T) {
	w, h := 3, 2
	pixels := map[[2]int]color.RGBA{
		{0, 0}: {255, 0, 0, 255},     // R
		{1, 0}: {0, 255, 0, 255},     // G
		{2, 0}: {0, 0, 255, 255},     // B
		{0, 1}: {255, 255, 255, 255}, // W
		{1, 1}: {0, 0, 0, 255},       // K
		{2, 1}: {255, 255, 0, 255},   // Y
	}
	src := image.NewRGBA(image.Rect(0, 0, w, h))
	for p, c := range pixels {
		src.Set(p[0], p[1], c)
	}
	if got, err := applyOrientation(context.Background(), src, 1); err != nil {
		t.Fatalf("orientation 1: %v", err)
	} else if got != src {
		t.Fatal("orientation 1 must return the input unchanged (same pointer)")
	}
	for o := 2; o <= 8; o++ {
		out, err := applyOrientation(context.Background(), src, o)
		if err != nil {
			t.Fatalf("orientation %d: %v", o, err)
		}
		ow, oh := w, h
		if o >= 5 {
			ow, oh = h, w
		}
		b := out.Bounds()
		if b.Dx() != ow || b.Dy() != oh {
			t.Fatalf("orientation %d: out dims %dx%d want %dx%d", o, b.Dx(), b.Dy(), ow, oh)
		}
		for r := 0; r < oh; r++ {
			for c := 0; c < ow; c++ {
				sr, sc := orientIndex[o](r, c, w, h)
				want := pixels[[2]int{sc, sr}]
				if got := out.At(c, r).(color.RGBA); got != want {
					t.Fatalf("orientation %d: out(%d,%d)=%v want %v (src(%d,%d))", o, r, c, got, want, sr, sc)
				}
			}
		}
	}
}

// TestApplyOrientationEdgeDims: 1×1, 1×N and N×1 frames must rotate without
// panicking and with the table's out dims; out-of-range orientations are
// safe no-ops (same pointer).
func TestApplyOrientationEdgeDims(t *testing.T) {
	for _, dims := range [][2]int{{1, 1}, {1, 5}, {5, 1}} {
		w, h := dims[0], dims[1]
		src := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				src.Set(x, y, color.RGBA{uint8(x * 50), uint8(y * 50), 100, 255})
			}
		}
		for o := 2; o <= 8; o++ {
			out, err := applyOrientation(context.Background(), src, o)
			if err != nil {
				t.Fatalf("orientation %d: %v", o, err)
			}
			ow, oh := w, h
			if o >= 5 {
				ow, oh = h, w
			}
			if out.Bounds().Dx() != ow || out.Bounds().Dy() != oh {
				t.Fatalf("%dx%d orientation %d: out dims %dx%d want %dx%d", w, h, o, out.Bounds().Dx(), out.Bounds().Dy(), ow, oh)
			}
		}
	}
	src := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for _, o := range []int{0, 9, -1} {
		if got, err := applyOrientation(context.Background(), src, o); err != nil {
			t.Fatalf("orientation %d: %v", o, err)
		} else if got != src {
			t.Fatalf("orientation %d must be a safe no-op (same pointer)", o)
		}
	}
}

// TestExifOrientationDefensive pins every defensive branch of the APP1 walk
// and the TIFF parse: any anomaly yields 1, never a panic.
func TestExifOrientationDefensive(t *testing.T) {
	validLE := exifPayload(6, binary.LittleEndian)
	validBE := exifPayload(6, binary.BigEndian)
	// count 2 with an offset-follow value field holding [6, 7]: the first
	// SHORT wins (TIFF-correct offset semantics).
	multi := func() []byte {
		p := make([]byte, 36)
		copy(p, exifSignature)
		p[6], p[7] = 'I', 'I'
		binary.LittleEndian.PutUint16(p[8:10], 0x2A)
		binary.LittleEndian.PutUint32(p[10:14], 8)
		binary.LittleEndian.PutUint16(p[14:16], 1)
		binary.LittleEndian.PutUint16(p[16:18], 0x0112)
		binary.LittleEndian.PutUint16(p[18:20], 3)
		binary.LittleEndian.PutUint32(p[20:24], 2)  // count 2 → value field is an offset
		binary.LittleEndian.PutUint32(p[24:28], 24) // TIFF offset 24 → payload[30]
		binary.LittleEndian.PutUint16(p[30:32], 6)
		binary.LittleEndian.PutUint16(p[32:34], 7)
		return p
	}()
	tests := []struct {
		name string
		in   []byte
		want int
	}{
		{"empty head", nil, 1},
		{"not a jpeg", []byte("GIF89a"), 1},
		{"no soi", []byte{0x00, 0xD8}, 1},
		{"soi only", []byte{0xFF, 0xD8}, 1},
		{"marker fill without marker", []byte{0xFF, 0xD8, 0xFF}, 1},
		{"byte-stuffed marker", []byte{0xFF, 0xD8, 0xFF, 0x00}, 1},
		{"truncated segment length", []byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00}, 1},
		{"segment length overruns head", jpegWithAPP1Len(bytes.Repeat([]byte{0x42}, 4), 0xFFFF), 1},
		{"app1 without exif signature", jpegWithAPP1(bytes.Repeat([]byte{0x42}, 10)), 1},
		{"app1 payload shorter than signature", jpegWithAPP1([]byte{0x42, 0x43}), 1},
		{"exif signature only, no tiff", jpegWithAPP1([]byte("Exif\x00\x00")), 1},
		{"bad byte order", jpegWithAPP1(mutated(validLE, 6, 'X')), 1},
		{"bad tiff magic", jpegWithAPP1(mutated(validLE, 8, 0x2B)), 1},
		{"ifd offset out of bounds", jpegWithAPP1(mutatedU32(validLE, 10, 100)), 1},
		{"entry count overruns payload", jpegWithAPP1(mutatedU16(validLE, 14, 10)), 1},
		{"zero entry count", jpegWithAPP1(mutatedU16(validLE, 14, 0)), 1},
		{"missing orientation tag", jpegWithAPP1(mutatedU16(validLE, 16, 0x0113)), 1},
		{"wrong type (long)", jpegWithAPP1(mutatedU16(validLE, 18, 4)), 1},
		{"zero value count", jpegWithAPP1(mutatedU32(validLE, 20, 0)), 1},
		{"value zero", jpegWithAPP1(mutatedU16(validLE, 24, 0)), 1},
		{"value nine", jpegWithAPP1(mutatedU16(validLE, 24, 9)), 1},
		{"count two offset-follow first wins", jpegWithAPP1(multi), 6},
		{"first exif app1 wins even when invalid", jpegWithAPP1s([]byte("Exif\x00\x00"), validLE), 1},
		{"first valid exif app1 beats later", jpegWithAPP1s(exifPayload(3, binary.LittleEndian), exifPayload(8, binary.LittleEndian)), 3},
		{"post-SOS exif never scanned", jpegWithPostSOSExif(append([]byte{0xFF, 0xE1, 0x00, 0x22}, validLE...)), 1},
		{"standalone markers skipped", jpegWithTEMThenAPP1(validLE), 6},
		{"little-endian valid", jpegWithAPP1(validLE), 6},
		{"big-endian valid", jpegWithAPP1(validBE), 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exifOrientation(tt.in); got != tt.want {
				t.Fatalf("exifOrientation = %d want %d", got, tt.want)
			}
		})
	}
}

// jpegWithTEMThenAPP1 builds SOI + TEM (standalone) + APP1(payload) + EOI:
// standalone markers must be skipped, not treated as segments.
func jpegWithTEMThenAPP1(payload []byte) []byte {
	var out bytes.Buffer
	out.Write([]byte{0xFF, 0xD8}) // SOI
	out.Write([]byte{0xFF, 0x01}) // TEM (standalone)
	var seg [4]byte
	seg[0], seg[1] = 0xFF, 0xE1
	binary.BigEndian.PutUint16(seg[2:4], uint16(len(payload)+2))
	out.Write(seg[:])
	out.Write(payload)
	out.Write([]byte{0xFF, 0xD9}) // EOI
	return out.Bytes()
}

func mutated(p []byte, i int, v byte) []byte {
	q := append([]byte(nil), p...)
	q[i] = v
	return q
}

func mutatedU16(p []byte, i int, v uint16) []byte {
	q := append([]byte(nil), p...)
	binary.LittleEndian.PutUint16(q[i:i+2], v)
	return q
}

func mutatedU32(p []byte, i int, v uint32) []byte {
	q := append([]byte(nil), p...)
	binary.LittleEndian.PutUint32(q[i:i+4], v)
	return q
}

// jpegWithPostSOFExif builds a JPEG whose APP1-EXIF segment appears AFTER a
// SOF0 frame header: SOI + SOF0 + APP1(Exif) + EOI. In a non-JFIF JPEG the
// post-SOF region is scan-adjacent data, not metadata — the walker must stop
// at the SOF family and never read the orientation tag (sec F-1).
func jpegWithPostSOFExif(payload []byte) []byte {
	var out bytes.Buffer
	out.Write([]byte{0xFF, 0xD8}) // SOI
	// SOF0 (baseline): precision 8, 64×64, 3 components.
	sof := []byte{8, 0, 64, 0, 64, 3, 1, 0x22, 0, 2, 0x11, 1, 3, 0x11, 1}
	out.Write([]byte{0xFF, 0xC0})
	var seg [2]byte
	binary.BigEndian.PutUint16(seg[:], uint16(len(sof)+2))
	out.Write(seg[:])
	out.Write(sof)
	// Post-SOF APP1 carrying a valid orientation-6 EXIF payload.
	var seg4 [4]byte
	seg4[0], seg4[1] = 0xFF, 0xE1
	binary.BigEndian.PutUint16(seg4[2:4], uint16(len(payload)+2))
	out.Write(seg4[:])
	out.Write(payload)
	out.Write([]byte{0xFF, 0xD9}) // EOI
	return out.Bytes()
}

// TestExifOrientationPostSOFStops pins sec F-1: the EXIF walk stops at the
// SOF family, so a post-SOF APP1 (scan-adjacent data in non-JFIF JPEGs) is
// never parsed. A pre-SOF APP1 with the same payload still wins.
func TestExifOrientationPostSOFStops(t *testing.T) {
	validLE := exifPayload(6, binary.LittleEndian)
	if got := exifOrientation(jpegWithPostSOFExif(validLE)); got != 1 {
		t.Fatalf("post-SOF APP1 orientation = %d want 1 (metadata-only walk)", got)
	}
	// SOF before APP1 in the OTHER order (APP1 first) still parses.
	pre := jpegWithAPP1(validLE)
	if got := exifOrientation(pre); got != 6 {
		t.Fatalf("pre-SOF APP1 orientation = %d want 6", got)
	}
	// A SOF-family stop also holds for SOF2 (progressive) and SOF1.
	for _, sof := range []byte{0xC0, 0xC1, 0xC2} {
		raw := append([]byte{0xFF, 0xD8, 0xFF, sof, 0x00, 0x11}, bytes.Repeat([]byte{0x42}, 15)...)
		raw = append(raw, []byte{0xFF, 0xE1}...)
		var seg [2]byte
		binary.BigEndian.PutUint16(seg[:], uint16(len(validLE)+2))
		raw = append(raw, seg[:]...)
		raw = append(raw, validLE...)
		if got := exifOrientation(raw); got != 1 {
			t.Fatalf("post-SOF%02X APP1 orientation = %d want 1", sof, got)
		}
	}
}

// TestExifOrientationDefensiveOOB pins qa F3: wraparound-class IFD0 offsets,
// out-of-bounds value-offset follows, and duplicate 0x0112 tags all resolve
// safely — 1 for malformed structures, first-tag-wins for duplicates.
func TestExifOrientationDefensiveOOB(t *testing.T) {
	validLE := exifPayload(6, binary.LittleEndian)
	// 0xFFFFFFFF IFD0 offset: the int conversion must not wrap and the
	// bounds check must reject.
	huge := jpegWithAPP1(mutatedU32(validLE, 10, 0xFFFFFFFF))
	if got := exifOrientation(huge); got != 1 {
		t.Fatalf("0xFFFFFFFF IFD0 offset = %d want 1", got)
	}
	// OOB value-offset follow: count 2 with the value offset pointing past
	// the payload.
	oobPayload := func() []byte {
		p := make([]byte, 30)
		copy(p, exifSignature)
		p[6], p[7] = 'I', 'I'
		binary.LittleEndian.PutUint16(p[8:10], 0x2A)
		binary.LittleEndian.PutUint32(p[10:14], 8)
		binary.LittleEndian.PutUint16(p[14:16], 1)
		binary.LittleEndian.PutUint16(p[16:18], 0x0112)
		binary.LittleEndian.PutUint16(p[18:20], 3)
		binary.LittleEndian.PutUint32(p[20:24], 2)          // count 2 → offset-follow
		binary.LittleEndian.PutUint32(p[24:28], 0x7FFFFFFF) // way past EOF
		return p
	}()
	if got := exifOrientation(jpegWithAPP1(oobPayload)); got != 1 {
		t.Fatalf("OOB value-offset follow = %d want 1", got)
	}
	// Duplicate 0x0112 tags: the first entry wins (orientation 6, not 8).
	dup := func() []byte {
		p := make([]byte, 40)
		copy(p, exifSignature)
		p[6], p[7] = 'I', 'I'
		binary.LittleEndian.PutUint16(p[8:10], 0x2A)
		binary.LittleEndian.PutUint32(p[10:14], 8)
		binary.LittleEndian.PutUint16(p[14:16], 2) // two entries
		binary.LittleEndian.PutUint16(p[16:18], 0x0112)
		binary.LittleEndian.PutUint16(p[18:20], 3)
		binary.LittleEndian.PutUint32(p[20:24], 1)
		binary.LittleEndian.PutUint16(p[24:26], 6)
		binary.LittleEndian.PutUint16(p[28:30], 0x0112)
		binary.LittleEndian.PutUint16(p[30:32], 3)
		binary.LittleEndian.PutUint32(p[32:36], 1)
		binary.LittleEndian.PutUint16(p[36:38], 8)
		return p
	}()
	if got := exifOrientation(jpegWithAPP1(dup)); got != 6 {
		t.Fatalf("duplicate 0x0112 = %d want 6 (first-tag-wins)", got)
	}
}

// TestApplyOrientationLiteralMatrices pins qa F4: orientations 2–8 use
// literal expected matrices (independent of orientIndex) on a 2×3 labeled
// source, so a regression in the index table itself cannot pass the pin.
// Source pixels are labeled 1..6 row-major (w=2, h=3); orientations 5–8
// swap the output axes to 3×2:
//
//	1 2      (o2) 2 1        (o3) 6 5        (o4) 5 6
//	3 4      =    4 3        =    4 3        =    3 4
//	5 6           6 5             2 1             1 2
//
//	(o5) 1 3 5   (o6) 5 3 1   (o7) 6 4 2   (o8) 2 4 6
//	=    2 4 6   =    6 4 2   =    5 3 1   =    1 3 5
func TestApplyOrientationLiteralMatrices(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 2, 3)) // w=2, h=3
	for r := 0; r < 3; r++ {
		for c := 0; c < 2; c++ {
			v := uint8(r*2 + c + 1) // 1..6 row-major
			src.SetNRGBA(c, r, color.NRGBA{R: v, G: 0, B: 0, A: 255})
		}
	}
	type matrix [][]int
	rows := func(rs ...[]int) matrix { return rs }
	expect := map[int]struct {
		out matrix
	}{
		2: {rows([]int{2, 1}, []int{4, 3}, []int{6, 5})},
		3: {rows([]int{6, 5}, []int{4, 3}, []int{2, 1})},
		4: {rows([]int{5, 6}, []int{3, 4}, []int{1, 2})},
		5: {rows([]int{1, 3, 5}, []int{2, 4, 6})},
		6: {rows([]int{5, 3, 1}, []int{6, 4, 2})},
		7: {rows([]int{6, 4, 2}, []int{5, 3, 1})},
		8: {rows([]int{2, 4, 6}, []int{1, 3, 5})},
	}
	for o, want := range expect {
		t.Run("o"+string(rune('0'+o)), func(t *testing.T) {
			got, err := applyOrientation(context.Background(), src, o)
			if err != nil {
				t.Fatalf("orientation %d: %v", o, err)
			}
			if got.Bounds().Dx() != len(want.out[0]) || got.Bounds().Dy() != len(want.out) {
				t.Fatalf("o%d out dims %dx%d want %dx%d", o, got.Bounds().Dx(), got.Bounds().Dy(), len(want.out[0]), len(want.out))
			}
			for r, row := range want.out {
				for c, v := range row {
					gotV := got.At(c, r).(color.RGBA).R
					if gotV != uint8(v) {
						t.Fatalf("o%d at (%d,%d) = %d want %d", o, r, c, gotV, v)
					}
				}
			}
		})
	}
	// o1 identity: same dims, byte-identical buffer reference (no copy).
	if got, err := applyOrientation(context.Background(), src, 1); err != nil {
		t.Fatalf("orientation 1: %v", err)
	} else if got != image.Image(src) {
		t.Fatal("o1 must return the source unchanged")
	}
	if got, err := applyOrientation(context.Background(), src, 0); err != nil {
		t.Fatalf("orientation 0: %v", err)
	} else if got != image.Image(src) {
		t.Fatal("out-of-range orientation must return the source unchanged")
	}
}

// TestGenerateOrientationRatioGe1YCbCr pins qa F5: a real JPEG source (YCbCr)
// no larger than the requested box takes the ratio ≥ 1 path (scale returns the
// source unchanged) and still rotates per EXIF orientation 6 — the upright
// 75×100 output with the blue/red quadrants preserved.
func TestGenerateOrientationRatioGe1YCbCr(t *testing.T) {
	// 50×75 JPEG, right-half red / left-half blue (decodes to *image.YCbCr).
	img := image.NewRGBA(image.Rect(0, 0, 50, 75))
	for y := 0; y < 75; y++ {
		for x := 0; x < 50; x++ {
			if x < 25 {
				img.Set(x, y, color.RGBA{0, 0, 255, 255})
			} else {
				img.Set(x, y, color.RGBA{255, 0, 0, 255})
			}
		}
	}
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	// Splice the EXIF orientation-6 APP1 between SOI and the rest.
	body := jpg.Bytes()[2:] // past SOI
	var out bytes.Buffer
	out.Write([]byte{0xFF, 0xD8})
	payload := exifPayload(6, binary.LittleEndian)
	var seg [4]byte
	seg[0], seg[1] = 0xFF, 0xE1
	binary.BigEndian.PutUint16(seg[2:4], uint16(len(payload)+2))
	out.Write(seg[:])
	out.Write(payload)
	out.Write(body)

	// Box 256×256 ≥ source 50×75: ratio ≥ 1 → scale returns src unchanged,
	// then orientation 6 rotates 90° CW → 75×50... wait, orientation 5–8
	// swap the box: box becomes 256×256 for 50×75? No — boxW,boxH = maxH,maxW
	// for o≥5 → (256,256). scale(50×75 → 256×256) is ratio < 1 in one axis?
	// 50→256 is upscale (ratio ≥ 1 → unchanged), 75→256 likewise. Both axes
	// ratio ≥ 1 → source returned unchanged (50×75 YCbCr). Then o6 rotates
	// to 75×50, upright (blue top, red bottom).
	got, err := Generate(bytes.NewReader(out.Bytes()), 256, 256)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	dec, format, err := image.Decode(bytes.NewReader(got))
	if err != nil || format != "jpeg" {
		t.Fatalf("decode: %v fmt=%q", err, format)
	}
	if dec.Bounds().Dx() != 75 || dec.Bounds().Dy() != 50 {
		t.Fatalf("out dims %dx%d want 75x50 (rotated upright)", dec.Bounds().Dx(), dec.Bounds().Dy())
	}
	// After 90° CW: the source's left (blue) half becomes the top.
	top := dec.At(37, 5)
	bot := dec.At(37, 45)
	tr, _, tb, _ := top.RGBA()
	br, _, bb, _ := bot.RGBA()
	if tb <= tr || bb >= br {
		t.Fatalf("top=(%d,%d) bottom=(%d,%d): want top blue-dominant, bottom red-dominant", tr, tb, br, bb)
	}
}

// FuzzExifOrientation drives the hand-rolled EXIF walker with arbitrary
// mutations: any input must yield 1..8, never panic — including the
// 32-bit int-wrap class (0xFFFFFFFF offsets, verified GOARCH=386 panic
// before the lower-bound guard). The seeds cover the count-2
// offset-follow wrap, a huge IFD0 offset, and a valid orientation-6 frame.
// Run: go test -fuzz=FuzzExifOrientation -fuzztime=60s ./internal/thumbnail/
func FuzzExifOrientation(f *testing.F) {
	validLE := exifPayload(6, binary.LittleEndian)
	f.Add(validLE)
	f.Add(exifPayload(8, binary.BigEndian))
	f.Add(jpegWithAPP1(validLE))
	f.Add(jpegWithPostSOFExif(validLE))
	// count-2 wrap seeds: the value field is an offset; 0xFFFFFFFF and
	// 0x7FFFFFFF must not wrap int on 32-bit targets (the guard rejects).
	wrap := func(off uint32) []byte {
		p := make([]byte, 30)
		copy(p, exifSignature)
		p[6], p[7] = 'I', 'I'
		binary.LittleEndian.PutUint16(p[8:10], 0x2A)
		binary.LittleEndian.PutUint32(p[10:14], 8)
		binary.LittleEndian.PutUint16(p[14:16], 1)
		binary.LittleEndian.PutUint16(p[16:18], 0x0112)
		binary.LittleEndian.PutUint16(p[18:20], 3)
		binary.LittleEndian.PutUint32(p[20:24], 2)
		binary.LittleEndian.PutUint32(p[24:28], off)
		return jpegWithAPP1(p)
	}
	f.Add(wrap(0xFFFFFFFF))
	f.Add(wrap(0x7FFFFFFF))
	f.Add(wrap(0xFFFFFFF8))
	f.Add(jpegWithAPP1(mutatedU32(validLE, 10, 0xFFFFFFFF))) // IFD0 wrap class

	f.Fuzz(func(t *testing.T, data []byte) {
		got := exifOrientation(data)
		if got < 1 || got > 8 {
			t.Fatalf("exifOrientation = %d, want 1..8", got)
		}
	})
}

// FuzzProgressiveJPEG drives the segment-aware SOF2 walker: any input must
// yield true/false, never panic, never scan past the SOS header.
// Run: go test -fuzz=FuzzProgressiveJPEG -fuzztime=60s ./internal/thumbnail/
func FuzzProgressiveJPEG(f *testing.F) {
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x04, 0x42, 0x42, 0xFF, 0xC2})
	f.Add(headerOnlyProgressiveJPEG(f, 64, 64))
	f.Add(realBaselineJPEG(f, 64, 64, 82))
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xFF, 0xFF})
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0xFF, 0xFF, 0x42})
	f.Fuzz(func(t *testing.T, data []byte) {
		_ = progressiveJPEG(data) // must not panic; verdict unconstrained
	})
}
