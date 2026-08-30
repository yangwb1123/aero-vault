package thumbnail

import (
	"bytes"
	"encoding/binary"
	"testing"
)

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
