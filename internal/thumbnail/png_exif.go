package thumbnail

// PNG eXIf-chunk EXIF orientation extraction. A PNG (Third Edition, 2017)
// carries the same 0x0112 orientation tag as a JPEG APP1 segment, but in the
// registered eXIf chunk, which sits mid-stream — unlike JPEG (whose APP1
// DecodeConfig consumed into head), a PNG's DecodeConfig reads only the
// signature + IHDR (33 bytes), so the orientation can never be read from
// head alone. This file implements the bounded pre-IDAT chunk walk (pngOrientation)
// and the dual-layout eXIf payload parser (pngExifOrientation).

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
)

// pngChunkCursor serves the post-IHDR chunk stream of a PNG whose signature +
// IHDR were validated by DecodeConfig. It reads from head first (free — the
// bytes are already counted against MaxMetadataBytes in generateLocked's tee),
// then from r, charging only the r-sourced bytes against the walk's remaining
// budget. readN checks the budget BEFORE the r-read (FR-3): a chunk whose
// declared size cannot fit is rejected before any buffer is allocated.
//
// headAvail is the number of unread bytes remaining in head at a pre-check
// point: head bytes were already counted against MaxMetadataBytes by
// generateLocked's config-scan tee, so the declared-size pre-checks credit
// them against the r-budget (readN already charges only r-bytes).
func (c *pngChunkCursor) headAvail() int { return max(0, len(c.head)-c.off) }

// In generateLocked the walk's r is io.TeeReader(stream, replay), so every
// byte served from the stream also lands in the replay buffer and Decode
// re-reads the identical byte stream (FR-4). Chunk headers/data straddling
// the head/r seam are stitched uniformly: the head portion is copied, the
// r-portion is read through the tee.
type pngChunkCursor struct {
	head      []byte
	off       int // position within head (starts at 33: past signature + IHDR)
	r         io.Reader
	remaining int // budget left for r-bytes (MaxMetadataBytes − len(head))
}

// readN fills p from the cursor (head portion first, then r), charging only
// r-bytes against remaining. The budget check precedes the r-read. The
// declared-size pre-checks are advisory for the bound — this check and the
// replay tee are the enforcement.
func (c *pngChunkCursor) readN(p []byte) error {
	k := copy(p, c.head[c.off:])
	c.off += k
	if k == len(p) {
		return nil // fully in head: no budget charge, no read
	}
	if c.remaining < len(p)-k {
		return errMetadataBudgetExceeded // before the read; writes nothing
	}
	n, err := io.ReadFull(c.r, p[k:])
	c.remaining -= n
	return err
}

// skipN discards n bytes (chunk data + CRC) with a fixed 512-byte scratch —
// zero large allocations on the metadata-only walk.
func (c *pngChunkCursor) skipN(n int) error {
	var tmp [512]byte
	for n > 0 {
		m := n
		if m > len(tmp) {
			m = len(tmp)
		}
		if err := c.readN(tmp[:m]); err != nil {
			return err
		}
		n -= m
	}
	return nil
}

// pngOrientation returns the EXIF orientation (1–8) recorded in the first
// eXIf chunk of a PNG whose header (8-byte signature + 25-byte IHDR chunk)
// was validated by DecodeConfig and is in head. It walks the chunk stream
// from byte 33 — in memory over head[33:] first (already counted against
// MaxMetadataBytes), then over r through generateLocked's replay tee.
//
// Bounded: head + walk ≤ MaxMetadataBytes (budget = MaxMetadataBytes −
// len(head); checked before every read from r using the declared 4-byte BE
// chunk length, so no buffer beyond the budget is ever allocated; breach →
// errMetadataBudgetExceeded, mapped by generateLocked to ErrMetadataTooLarge).
// A chunk's bytes already resident in head (headAvail = max(0, len(head) −
// off) at the pre-check) are credited against the declared-size check — the
// head bytes were counted against MaxMetadataBytes by the config-scan tee, so
// a chunk whose declared size exceeds the r-budget but whose true r-cost fits
// is accepted; the total head + walk ≤ MaxMetadataBytes contract is unchanged.
//
// Stop conditions: the first eXIf chunk wins (its data is parsed, the CRC is
// left for Decode — first-wins mirrors the JPEG "first pre-SOS APP1 wins"
// rule; PNG 3rd ed. §11.3.4.5 allows only one eXIf, placed before IDAT), the
// first IDAT chunk (compressed data is never scanned — the ordering table's
// "eXIf … Before IDAT"), IEND, an invalid length (≥ 2³¹, image/png's
// FormatError threshold — Decode re-reads the same bytes and classifies), or
// a non-context read error (deferred to Decode, which re-encounters the same
// bytes; FR-5). Context sentinels and errMetadataBudgetExceeded are returned
// raw for generateLocked to classify with the exact Decode-site block. Never
// panics; returns 1..8.
func pngOrientation(ctx context.Context, head []byte, r io.Reader) (int, error) {
	if len(head) < 33 {
		return 1, nil // DecodeConfig validated the header; defensive
	}
	budget := MaxMetadataBytes - len(head)
	if budget < 0 {
		budget = 0 // unreachable: head ≤ MaxMetadataBytes by construction
	}
	c := &pngChunkCursor{head: head, off: 33, r: r, remaining: budget}
	var hdr [8]byte
	for {
		if err := ctx.Err(); err != nil {
			return 0, err // cheap, non-allocating, at every chunk boundary
		}
		if err := c.readN(hdr[:]); err != nil {
			return 0, err // raw; generateLocked classifies (FR-5)
		}
		length := int64(binary.BigEndian.Uint32(hdr[:4]))
		switch {
		case hdr[4] == 'I' && hdr[5] == 'D' && hdr[6] == 'A' && hdr[7] == 'T',
			hdr[4] == 'I' && hdr[5] == 'E' && hdr[6] == 'N' && hdr[7] == 'D':
			return 1, nil // pixel data / terminator: never scanned
		case hdr[4] == 'e' && hdr[5] == 'X' && hdr[6] == 'I' && hdr[7] == 'f':
			if length > int64(c.remaining)+int64(c.headAvail()) {
				return 0, errMetadataBudgetExceeded // declared length check, before any allocation
			}
			var data []byte
			if c.off+int(length) <= len(c.head) {
				data = c.head[c.off : c.off+int(length)] // free slice (common: head holds the whole small PNG)
				c.off += int(length)
			} else {
				data = make([]byte, int(length)) // bounded scratch: length ≤ remaining + headAvail ≤ MaxMetadataBytes − off < 8 MiB
				if err := c.readN(data); err != nil {
					return 0, err // r-bytes land in the replay tee for Decode
				}
			}
			return pngExifOrientation(data), nil // first eXIf wins; CRC left for Decode
		default:
			if length > 0x7fffffff {
				return 1, nil // image/png FormatError; Decode re-reads and classifies → ErrUnsupported
			}
			if length+4 > int64(c.remaining)+int64(c.headAvail()) {
				return 0, errMetadataBudgetExceeded
			}
			if err := c.skipN(int(length) + 4); err != nil {
				return 0, err
			}
		}
	}
}

// pngExifOrientation returns the EXIF orientation (1–8) of a PNG eXIf chunk's
// data field, or 1 on any anomaly. Two layouts are parsed:
//
//   - the conformant bare Exif profile (TIFF header at offset 0 — PNG Third
//     Edition §11.3.4.5: the eXIf data is the CIPA DC-008 profile with the
//     "Exif" ID code, NULL and padding byte NOT included; every one of the
//     reference host's real eXIf chunks — Pillow-written and Adwaita icon
//     files included — uses this layout); and
//   - the tolerated "Exif\x00\x00"+TIFF deviation (the JPEG APP1 payload
//     layout; libpng documents both forms in the wild).
//
// Bare is primary — a prefixed-only assumption silently returns 1 on 100% of
// conformant files (the exact failure class this feature exists to fix), so
// do not "simplify" this adapter back to a single layout. Signature-less or
// malformed payloads → 1 (defensive; never panics).
func pngExifOrientation(data []byte) int {
	base := 0
	switch {
	case len(data) >= 6 && bytes.Equal(data[:6], []byte(exifSignature)):
		base = exifTiffBase // tolerated "Exif\x00\x00"+TIFF deviation (JPEG APP1 layout)
	case len(data) >= 2 && (data[0] == 'I' || data[0] == 'M'):
		// bare Exif profile — the conformant PNG eXIf layout; the byte-order
		// pair is validated by tiffOrientationFrom.
	default:
		return 1
	}
	return tiffOrientationFrom(data, base)
}
