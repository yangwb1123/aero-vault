package thumbnail

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/jpeg"
	"testing"
)

// dcLumCounts/dcLumValues and acLumCounts/acLumValues mirror the stdlib's
// theHuffmanSpec luminance tables (image/jpeg/writer.go, Annex K.1). They are
// needed because the stdlib has no progressive JPEG encoder: jpeg.Options has
// only Quality, so the real-progressive fixture is hand-built and must carry
// the exact tables the decoder expects.
var (
	dcLumCounts = [16]byte{0, 1, 5, 1, 1, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0, 0}
	dcLumValues = []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
	acLumCounts = [16]byte{0, 2, 1, 3, 3, 2, 4, 3, 5, 5, 4, 4, 0, 0, 1, 125}
	acLumValues = []byte{
		0x01, 0x02, 0x03, 0x00, 0x04, 0x11, 0x05, 0x12,
		0x21, 0x31, 0x41, 0x06, 0x13, 0x51, 0x61, 0x07,
		0x22, 0x71, 0x14, 0x32, 0x81, 0x91, 0xa1, 0x08,
		0x23, 0x42, 0xb1, 0xc1, 0x15, 0x52, 0xd1, 0xf0,
		0x24, 0x33, 0x62, 0x72, 0x82, 0x09, 0x0a, 0x16,
		0x17, 0x18, 0x19, 0x1a, 0x25, 0x26, 0x27, 0x28,
		0x29, 0x2a, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39,
		0x3a, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48, 0x49,
		0x4a, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58, 0x59,
		0x5a, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68, 0x69,
		0x6a, 0x73, 0x74, 0x75, 0x76, 0x77, 0x78, 0x79,
		0x7a, 0x83, 0x84, 0x85, 0x86, 0x87, 0x88, 0x89,
		0x8a, 0x92, 0x93, 0x94, 0x95, 0x96, 0x97, 0x98,
		0x99, 0x9a, 0xa2, 0xa3, 0xa4, 0xa5, 0xa6, 0xa7,
		0xa8, 0xa9, 0xaa, 0xb2, 0xb3, 0xb4, 0xb5, 0xb6,
		0xb7, 0xb8, 0xb9, 0xba, 0xc2, 0xc3, 0xc4, 0xc5,
		0xc6, 0xc7, 0xc8, 0xc9, 0xca, 0xd2, 0xd3, 0xd4,
		0xd5, 0xd6, 0xd7, 0xd8, 0xd9, 0xda, 0xe1, 0xe2,
		0xe3, 0xe4, 0xe5, 0xe6, 0xe7, 0xe8, 0xe9, 0xea,
		0xf1, 0xf2, 0xf3, 0xf4, 0xf5, 0xf6, 0xf7, 0xf8,
		0xf9, 0xfa,
	}
)

// headerOnlyProgressiveJPEG builds a progressive JPEG containing only header
// segments — SOI + APP0 + SOF2 + SOS — plus a junk payload. There is no
// DQT/DHT and no valid entropy data, so no pixel buffer is ever allocated;
// the declared dimensions are attacker-controlled. image.DecodeConfig parses
// the SOF2 segment and returns exactly w×h ("jpeg") without reading the
// payload; a full image.Decode fails (bad spectral selection bounds / missing
// tables), which is what makes the in-bound boundary pins fall through to
// ErrUnsupported rather than decoding.
func headerOnlyProgressiveJPEG(t testing.TB, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI
	app0 := []byte{'J', 'F', 'I', 'F', 0, 1, 1, 0, 0, 1, 0, 1, 0, 0}
	buf.Write([]byte{0xFF, 0xE0})
	var seg [2]byte
	binary.BigEndian.PutUint16(seg[:], uint16(len(app0)+2))
	buf.Write(seg[:])
	buf.Write(app0)
	// SOF2 (progressive): precision 8, height, width, 3 components
	// (4:2:0 sampling, tables 0/1/1). Payload is 15 bytes, so the length
	// field is 17 (0x0011) — the stdlib processSOF requires n == 6+3×3.
	sof := []byte{8, byte(h >> 8), byte(h), byte(w >> 8), byte(w), 3,
		1, 0x22, 0,
		2, 0x11, 1,
		3, 0x11, 1}
	buf.Write([]byte{0xFF, 0xC2})
	binary.BigEndian.PutUint16(seg[:], uint16(len(sof)+2))
	buf.Write(seg[:])
	buf.Write(sof)
	// SOS header (DecodeConfig returns at SOF2 and never reaches it).
	sos := []byte{3, 1, 0, 2, 0, 3, 0, 0, 63, 0}
	buf.Write([]byte{0xFF, 0xDA})
	binary.BigEndian.PutUint16(seg[:], uint16(len(sos)+2))
	buf.Write(seg[:])
	buf.Write(sos)
	buf.Write(bytes.Repeat([]byte{0x42}, 64)) // junk payload
	return buf.Bytes()
}

// appnPaddedProgressiveJPEG builds a header-only progressive JPEG whose
// pre-SOF region carries totalMetaBytes of APP1 segment payload (marker
// 0xE1), mirroring appnPaddedJPEG for the progressive fixture: the config
// scan reads every pre-SOF segment in full, and the 16-bit length field caps
// a single APP1 at 65533 bytes so many segments are needed.
func appnPaddedProgressiveJPEG(t testing.TB, w, h, metaBytes int) []byte {
	t.Helper()
	base := headerOnlyProgressiveJPEG(t, w, h)
	return spliceAPP1(t, base, bytes.Repeat([]byte{0x42}, 4096), metaBytes)
}

// spliceAPP1 inserts APP1 segments carrying repeated payload bytes between a
// JPEG's SOI and the rest of its segments. The payload is opaque to the
// segment-aware header walk, so its content is attacker-chosen metadata
// (XMP/EXIF/ICC class) that a naive 0xFF 0xC2 scan would false-positive on.
func spliceAPP1(t testing.TB, base []byte, payload []byte, totalMetaBytes int) []byte {
	t.Helper()
	if len(base) < 2 || base[0] != 0xFF || base[1] != 0xD8 {
		t.Fatalf("unexpected jpeg header: % x", base[:2])
	}
	const maxSegPayload = 65533 // 0xFFFF minus the 2 length bytes
	var out bytes.Buffer
	out.Write(base[:2]) // SOI; APP1 segments splice in before the rest.
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
		for written := 0; written < n; {
			chunk := n - written
			if chunk > len(payload) {
				chunk = len(payload)
			}
			out.Write(payload[:chunk])
			written += chunk
		}
		remaining -= n
	}
	out.Write(base[2:])
	return out.Bytes()
}

// realProgressiveJPEG builds a fully valid, stdlib-decodable progressive
// (SOF2) JPEG of uniform gray: SOI + APP0 + DQT (flat table) + SOF2
// (1 component, 0x22 sampling) + DHT (Annex K.1 luminance DC/AC, exact
// stdlib theHuffmanSpec bytes) + DC scan (Ss=0 Se=0, per-MCU "00" diff code)
// + AC scan (Ss=1 Se=63, per-MCU EOB "1010") + EOI, with byte-aligned
// padding between scans. The stdlib has no progressive encoder (R1), so the
// fixture is hand-built; DecodeConfig/Decode self-checks in the tests pin it.
func realProgressiveJPEG(t testing.TB, w, h int) []byte {
	t.Helper()
	mcus := ((w + 7) / 8) * ((h + 7) / 8) // MCU count = ⌈w/8⌉·⌈h/8⌉
	var out bytes.Buffer
	out.Write([]byte{0xFF, 0xD8}) // SOI
	app0 := []byte{'J', 'F', 'I', 'F', 0, 1, 1, 0, 0, 1, 0, 1, 0, 0}
	out.Write([]byte{0xFF, 0xE0})
	var seg [2]byte
	binary.BigEndian.PutUint16(seg[:], uint16(len(app0)+2))
	out.Write(seg[:])
	out.Write(app0)
	// DQT: flat quantization table (64 × 1).
	dqt := append([]byte{0}, bytes.Repeat([]byte{1}, 64)...)
	out.Write([]byte{0xFF, 0xDB})
	binary.BigEndian.PutUint16(seg[:], uint16(len(dqt)+2))
	out.Write(seg[:])
	out.Write(dqt)
	// SOF2, 1 component: payload 9 bytes → length 11 (0x000B).
	sof := []byte{8, byte(h >> 8), byte(h), byte(w >> 8), byte(w), 1, 1, 0x22, 0}
	out.Write([]byte{0xFF, 0xC2})
	binary.BigEndian.PutUint16(seg[:], uint16(len(sof)+2))
	out.Write(seg[:])
	out.Write(sof)
	// DHT: luminance DC (table 0) + AC (table 0) in one segment.
	dht := append([]byte{0}, dcLumCounts[:]...)
	dht = append(dht, dcLumValues...)
	dht = append(dht, 0x10)
	dht = append(dht, acLumCounts[:]...)
	dht = append(dht, acLumValues...)
	out.Write([]byte{0xFF, 0xC4})
	binary.BigEndian.PutUint16(seg[:], uint16(len(dht)+2))
	out.Write(seg[:])
	out.Write(dht)
	// SOS 1: DC scan; per-MCU category-0 diff code "00" (2 bits).
	sos1 := []byte{1, 1, 0, 0, 0, 0}
	out.Write([]byte{0xFF, 0xDA})
	binary.BigEndian.PutUint16(seg[:], uint16(len(sos1)+2))
	out.Write(seg[:])
	out.Write(sos1)
	out.Write(make([]byte, (2*mcus+7)/8))
	// SOS 2: AC scan; per-MCU EOB "1010" (4 bits).
	acBytes := make([]byte, (4*mcus+7)/8)
	for i := range acBytes {
		acBytes[i] = 0xA0 // 1010 + zero padding in the trailing byte
		if (i+1)*8 <= 4*mcus {
			acBytes[i] = 0xAA
		}
	}
	sos2 := []byte{1, 1, 0, 1, 63, 0}
	out.Write([]byte{0xFF, 0xDA})
	binary.BigEndian.PutUint16(seg[:], uint16(len(sos2)+2))
	out.Write(seg[:])
	out.Write(sos2)
	out.Write(acBytes)
	out.Write([]byte{0xFF, 0xD9}) // EOI
	return out.Bytes()
}

// realBaselineJPEG encodes a uniform-color RGBA image as a baseline (SOF0)
// JPEG via the stdlib encoder (which never emits progressive frames).
func realBaselineJPEG(t testing.TB, w, h, q int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 128
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err != nil {
		t.Fatalf("encode baseline jpeg %dx%d: %v", w, h, err)
	}
	return buf.Bytes()
}

// headerOnlySOF1JPEG builds a header-only JPEG declaring SOF1 (extended
// sequential) with attacker-controlled dimensions — the SOF1 control for the
// progressive cap: SOF1 in (MaxProgressiveSourceDim, MaxSourceDim] must NOT
// be rejected as progressive. Mirrors headerOnlyProgressiveJPEG's structure
// (APP0 + SOS included; the stdlib config scan reads through SOS).
func headerOnlySOF1JPEG(t testing.TB, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI
	app0 := []byte{'J', 'F', 'I', 'F', 0, 1, 1, 0, 0, 1, 0, 1, 0, 0}
	buf.Write([]byte{0xFF, 0xE0})
	var seg [2]byte
	binary.BigEndian.PutUint16(seg[:], uint16(len(app0)+2))
	buf.Write(seg[:])
	buf.Write(app0)
	// SOF1 (extended sequential): precision 8, height, width, 3 components.
	sof := []byte{8, byte(h >> 8), byte(h), byte(w >> 8), byte(w), 3,
		1, 0x22, 0,
		2, 0x11, 1,
		3, 0x11, 1}
	buf.Write([]byte{0xFF, 0xC1})
	binary.BigEndian.PutUint16(seg[:], uint16(len(sof)+2))
	buf.Write(seg[:])
	buf.Write(sof)
	sos := []byte{3, 1, 0, 2, 0, 3, 0, 0, 63, 0}
	buf.Write([]byte{0xFF, 0xDA})
	binary.BigEndian.PutUint16(seg[:], uint16(len(sos)+2))
	buf.Write(seg[:])
	buf.Write(sos)
	return buf.Bytes()
}
