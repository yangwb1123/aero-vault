package thumbnail

import (
	"bytes"
	"encoding/binary"
)

// exifSignature is the 6-byte payload prefix of an EXIF APP1 segment: the
// segment payload is "Exif\x00\x00" followed by the TIFF structure.
const exifSignature = "Exif\x00\x00"

// exifOrientation returns the EXIF orientation (1–8) recorded in the JPEG
// header bytes buf, or 1 when the tag is absent or invalid. buf is the
// budget-capped DecodeConfig tee buffer (head), so the walk is bounded by
// MaxMetadataBytes by construction and allocates nothing.
//
// The marker discipline mirrors progressiveJPEG (Annex B fill, 16-bit
// big-endian segment-length skip, standalone markers) and stops at
// SOS/EOI — entropy-coded data is never scanned, where coincidental
// 0xFF 0xE1 byte pairs are ordinary data. The first pre-SOS APP1 whose
// payload begins with "Exif\x00\x00" wins, even when its TIFF content is
// invalid (invalid → 1, walk stops). Any parse anomaly yields 1 (no-op);
// this function never panics and never returns outside 1..8.
func exifOrientation(buf []byte) int {
	if len(buf) < 2 || buf[0] != 0xFF || buf[1] != 0xD8 {
		return 1 // no SOI: not a parseable JPEG header
	}
	i := 2 // past SOI
	for i < len(buf) {
		for i < len(buf) && buf[i] == 0xFF { // marker fill (Annex B)
			i++
		}
		if i >= len(buf) {
			return 1
		}
		m := buf[i]
		i++
		switch {
		case m == 0x00: // byte-stuffed 0xFF 0x00 at marker level: unparseable
			return 1
		case m == 0xD8, m == 0xD9, m == 0xDA: // SOI/EOI/SOS: stop before entropy
			return 1
		case m >= 0xC0 && m <= 0xCF && m != 0xC4 && m != 0xC8 && m != 0xCC:
			// SOF-family markers (SOF0–SOF15; 0xC4 DHT / 0xC8 JPG / 0xCC DAC
			// are tables, not frames): EXIF is a pre-SOF segment. A JPEG with
			// no JFIF APP0 can legally carry APPn after the SOF, where the
			// bytes are scan-adjacent data rather than metadata — stopping
			// here keeps the walk metadata-only (mirrors the stdlib's
			// configOnly && d.jfif rule).
			return 1
		case m == 0x01, m >= 0xD0 && m <= 0xD7: // standalone: TEM, RSTn
		default: // segment marker: skip payload by 16-bit length
			if i+2 > len(buf) {
				return 1
			}
			n := int(buf[i])<<8 | int(buf[i+1])
			if n < 2 || i+n > len(buf) {
				return 1
			}
			if m == 0xE1 && n >= 8 && bytes.HasPrefix(buf[i+2:i+n], []byte(exifSignature)) {
				return tiffOrientation(buf[i+2 : i+n])
			}
			i += n // length includes its own 2 bytes
		}
	}
	return 1
}

// exifTiffBase is the offset of the TIFF header within an EXIF payload of
// the JPEG APP1 layout: the 6-byte "Exif\x00\x00" signature precedes it.
const exifTiffBase = 6

// tiffOrientation parses the TIFF header + IFD0 of an EXIF payload (p starts
// at "Exif\x00\x00") and returns the value of tag 0x0112 (type SHORT, count
// ≥ 1): 1–8, or 1 on any anomaly. Both II (little-endian) and MM
// (big-endian) byte orders are supported; every read is bounds-checked and
// any structural anomaly yields 1 — never a panic, never an error.
func tiffOrientation(p []byte) int {
	return tiffOrientationFrom(p, exifTiffBase)
}

// tiffOrientationFrom is tiffOrientation's body with the TIFF-header offset
// parameterized: base is the offset of the TIFF header within p (6 for the
// "Exif\x00\x00"+TIFF layout — JPEG APP1 / prefixed PNG eXIf; 0 for a bare
// Exif profile — the conformant PNG eXIf layout). Every read position shifts
// by base: byte order at p[base:base+2], magic at p[base+2:base+4], IFD0
// offset at p[base+4:base+8]; the ifd/vo lower-bound guards become base+8
// (the 32-bit wrap guards from e8d9072 are preserved verbatim). The count==1
// inline SHORT value read stays at entry[8:10] for both byte orders — TIFF
// 6.0 §2 left-justifies the inline value (the fix-exif-mm gate's verdict),
// and the base shift is a mechanical offset that cannot alter the geometry.
func tiffOrientationFrom(p []byte, base int) int {
	if len(p) < base+8 { // TIFF header (8 B)
		return 1
	}
	var bo binary.ByteOrder
	switch {
	case p[base] == 'I' && p[base+1] == 'I':
		bo = binary.LittleEndian
	case p[base] == 'M' && p[base+1] == 'M':
		bo = binary.BigEndian
	default:
		return 1
	}
	if bo.Uint16(p[base+2:base+4]) != 0x2A {
		return 1 // TIFF magic
	}
	// All TIFF offsets are relative to the start of the TIFF header.
	ifd := base + int(bo.Uint32(p[base+4:base+8]))
	if ifd < base+8 || ifd+2 > len(p) {
		return 1 // IFD0 offset must lie within the payload
	}
	count := int(bo.Uint16(p[ifd : ifd+2]))
	if count == 0 || ifd+2+12*count > len(p) {
		return 1 // every entry must lie fully within the payload
	}
	for e := 0; e < count; e++ {
		off := ifd + 2 + 12*e
		entry := p[off : off+12]
		if bo.Uint16(entry[0:2]) != 0x0112 {
			continue // not the orientation tag
		}
		if bo.Uint16(entry[2:4]) != 3 { // type must be SHORT
			return 1
		}
		cnt := bo.Uint32(entry[4:8])
		if cnt == 0 {
			return 1
		}
		var v uint16
		if cnt == 1 {
			v = bo.Uint16(entry[8:10]) // value inline (left-justified, both byte orders)
		} else {
			vo := base + int(bo.Uint32(entry[8:12])) // value field is an offset
			// Lower bound is load-bearing on 32-bit targets: base +
			// 0xFFFFFFFF wraps int to a small negative, which passes a
			// one-sided vo+2 > len(p) check and panics on the slice below
			// (verified GOARCH=386 reproduction). The offset must point
			// into the payload proper (after the TIFF header), not before it.
			if vo < base+8 || vo+2 > len(p) {
				return 1
			}
			v = bo.Uint16(p[vo : vo+2])
		}
		if v >= 1 && v <= 8 {
			return int(v)
		}
		return 1 // value 0 or ≥ 9: treat as orientation 1
	}
	return 1 // tag 0x0112 absent
}
