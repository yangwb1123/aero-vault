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
