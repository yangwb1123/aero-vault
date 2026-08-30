package thumbnail

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"testing"
)

// bareExifPayload is the conformant PNG eXIf chunk data layout: the TIFF
// structure WITHOUT the "Exif\x00\x00" signature (PNG Third Edition
// §11.3.4.5 — the Exif ID code is not included). It is exifPayload's body
// with the 6-byte signature stripped, so the same orientation/byte-order
// fixtures drive both the JPEG APP1 parser and the PNG eXIf parser.
func bareExifPayload(o int, bo binary.ByteOrder) []byte {
	return exifPayload(o, bo)[6:]
}

// pillowExifPayload is the exact bytes Pillow 12.3.0 wrote for an eXIf chunk
// declaring orientation 6 (verified on the reference host): bare big-endian
// TIFF (MM, magic 0x2A, IFD0 at offset 8), one SHORT entry for tag 0x0112,
// count 1, value 6 left-justified at entry[8:10] — TIFF 6.0 §2 inline-SHORT
// semantics, the interop regression guard recommended by the fix-exif-mm gate.
func pillowExifPayload() []byte {
	return []byte{
		'M', 'M', 0x00, 0x2A, 0x00, 0x00, 0x00, 0x08, // TIFF header, IFD0 at 8
		0x00, 0x01, // one entry
		0x01, 0x12, // tag 0x0112 (orientation)
		0x00, 0x03, // type SHORT
		0x00, 0x00, 0x00, 0x01, // count 1
		0x00, 0x06, 0x00, 0x00, // value 6 (left-justified), padding
		0x00, 0x00,
	}
}

// adwaitaClassPayload is a bare big-endian Exif profile with a single
// 0x8769 (ExifIFDPointer) entry and no 0x0112 orientation tag — the class of
// real-world icon-theme eXIf chunks (e.g. Adwaita) that must resolve to 1.
func adwaitaClassPayload() []byte {
	p := make([]byte, 24)
	p[0], p[1] = 'M', 'M'
	binary.BigEndian.PutUint16(p[2:4], 0x2A)
	binary.BigEndian.PutUint32(p[4:8], 8)
	binary.BigEndian.PutUint16(p[8:10], 1)       // one entry
	binary.BigEndian.PutUint16(p[10:12], 0x8769) // ExifIFDPointer (not 0x0112)
	binary.BigEndian.PutUint16(p[12:14], 4)      // type LONG
	binary.BigEndian.PutUint32(p[14:18], 1)
	binary.BigEndian.PutUint32(p[18:22], 0)
	return p
}

// pngChunkBytes builds a complete PNG chunk: 4-byte BE length + 4-byte type +
// data + CRC32(IEEE) over type+data. The CRC is mandatory (C4): image/png
// validates every chunk — unknown ancillary chunks included — and a bad CRC
// fails Decode with ErrUnsupported.
func pngChunkBytes(t testing.TB, chunkType string, data []byte) []byte {
	t.Helper()
	if len(chunkType) != 4 {
		t.Fatalf("chunk type %q must be 4 bytes", chunkType)
	}
	var out bytes.Buffer
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(data)))
	out.Write(l[:])
	out.WriteString(chunkType)
	out.Write(data)
	crc := crc32.NewIEEE()
	_, _ = crc.Write([]byte(chunkType))
	_, _ = crc.Write(data)
	binary.BigEndian.PutUint32(l[:], crc.Sum32())
	out.Write(l[:])
	return out.Bytes()
}

// pngChunkHeader builds an 8-byte chunk header (length + type) without data
// or CRC — enough for the walk's white-box stop/defensive rows.
func pngChunkHeader(t testing.TB, chunkType string, length uint32) []byte {
	t.Helper()
	if len(chunkType) != 4 {
		t.Fatalf("chunk type %q must be 4 bytes", chunkType)
	}
	var h [8]byte
	binary.BigEndian.PutUint32(h[:4], length)
	copy(h[4:], chunkType)
	return h[:]
}

// splicePNGChunk inserts a complete chunk immediately after the IHDR chunk
// (byte offset 33) of a png.Encode-produced PNG — IHDR is always its first
// chunk, so byte 33 is exactly its end and the splice lands before IDAT.
func splicePNGChunk(t testing.TB, base []byte, chunkType string, data []byte) []byte {
	t.Helper()
	if len(base) < 33 {
		t.Fatalf("png fixture too short: %d", len(base))
	}
	var out bytes.Buffer
	out.Write(base[:33])
	out.Write(pngChunkBytes(t, chunkType, data))
	out.Write(base[33:])
	return out.Bytes()
}

// spliceChunkAfter inserts chunk immediately after the first full occurrence
// of the named chunk type (header + data + CRC) — used for PLTE (paletted
// fixtures, head > 33) and IDAT (post-IDAT eXIf, AC-5b).
func spliceChunkAfter(t testing.TB, base []byte, after, chunkType string, data []byte) []byte {
	t.Helper()
	for i := 8; i+8 <= len(base); {
		length := int(binary.BigEndian.Uint32(base[i : i+4]))
		if string(base[i+4:i+8]) == after {
			end := i + 8 + length + 4
			if end > len(base) {
				t.Fatalf("chunk %q overruns fixture", after)
			}
			var out bytes.Buffer
			out.Write(base[:end])
			out.Write(pngChunkBytes(t, chunkType, data))
			out.Write(base[end:])
			return out.Bytes()
		}
		i += 8 + length + 4
	}
	t.Fatalf("chunk %q not found in fixture", after)
	return nil
}

// spliceAfterIDAT inserts chunk immediately after the first IDAT chunk's CRC:
// the AC-5b shape — a post-IDAT eXIf must be ignored (the walk never scans
// pixel data; image/png skips the unknown chunk with CRC validation).
func spliceAfterIDAT(t testing.TB, base []byte, chunkType string, data []byte) []byte {
	t.Helper()
	return spliceChunkAfter(t, base, "IDAT", chunkType, data)
}

// orientedPNG builds a w×h RGBA PNG with a red top half and blue bottom half
// (the AC-1 fixture shape, mirroring orientationJPEG), optionally spliced with
// an eXIf chunk declaring orient (1–8; 0 = no eXIf) immediately after IHDR.
// prefixed selects the tolerated "Exif\x00\x00"+TIFF deviation; the default
// is the conformant bare Exif profile.
func orientedPNG(t testing.TB, w, h, orient int, prefixed bool) []byte {
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
	if err := png.Encode(&base, img); err != nil {
		t.Fatalf("encode base png: %v", err)
	}
	if orient < 1 || orient > 8 {
		return base.Bytes()
	}
	payload := bareExifPayload(orient, binary.LittleEndian)
	if prefixed {
		payload = exifPayload(orient, binary.LittleEndian)
	}
	return splicePNGChunk(t, base.Bytes(), "eXIf", payload)
}

// tEXtChunk builds a tEXt chunk whose data carries the "Comment\0" keyword
// plus padding to dataLen bytes; each chunk contributes dataLen + 12 structure
// bytes (8 header + data + 4 CRC).
func tEXtChunk(t testing.TB, dataLen int) []byte {
	t.Helper()
	const prefix = "Comment\x00"
	if dataLen < len(prefix) {
		dataLen = len(prefix)
	}
	return pngChunkBytes(t, "tEXt", append([]byte(prefix), make([]byte, dataLen-len(prefix))...))
}

// pngTextPaddedPNG builds a valid small PNG whose pre-IDAT region (between
// IHDR and IDAT) carries at least totalBytes of tEXt chunk structure — the
// PNG metadata-flood shape (image/png validates every chunk, so each tEXt
// carries a valid CRC).
func pngTextPaddedPNG(t testing.TB, totalBytes int) []byte {
	t.Helper()
	base := makePNG(t, 8, 8)
	var out bytes.Buffer
	out.Write(base[:33])
	remaining := totalBytes
	for remaining > 0 {
		dataLen := remaining - 12
		if dataLen > 1<<20-12 {
			dataLen = 1<<20 - 12
		}
		if dataLen < 16 {
			dataLen = 16
		}
		out.Write(tEXtChunk(t, dataLen))
		remaining -= 12 + dataLen
	}
	out.Write(base[33:])
	return out.Bytes()
}

// slowReader returns at most max bytes per Read — the ≤16 B/read shape that
// makes DecodeConfig's bufio fill collect head in small chunks, so the eXIf
// chunk straddles the head/r seam and the walk must read the r-portion
// through the replay tee (FR-4's load-bearing path).
type slowReader struct {
	r   io.Reader
	max int
}

func (s *slowReader) Read(p []byte) (int, error) {
	if len(p) > s.max {
		p = p[:s.max]
	}
	return s.r.Read(p)
}

// eXIfHeaderReader serves a single 8-byte eXIf chunk header, then fails any
// further read — pinning that the walk's budget pre-check fires before the
// data buffer is allocated (qa F1): a regression that allocated first would
// reach the marker error instead of errMetadataBudgetExceeded.
type eXIfHeaderReader struct {
	hdr []byte
}

var unexpectedReadErr = errors.New("png_exif_test: unexpected read past the eXIf header")

func (r *eXIfHeaderReader) Read(p []byte) (int, error) {
	if len(r.hdr) > 0 {
		n := copy(p, r.hdr)
		r.hdr = r.hdr[n:]
		return n, nil
	}
	return 0, unexpectedReadErr
}

// fmtWrappedDeadline returns a wrapped context.DeadlineExceeded instance —
// the QA-3 fallback shape (same instance must survive the walk).
func fmtWrappedDeadline() error {
	return fmt.Errorf("wrapped: %w", context.DeadlineExceeded)
}

func canceledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// firstWinsPNG builds a PNG with two eXIf chunks in order (o=3 then o=6): the
// walk must stop at the first (PNG 3rd ed. allows only one eXIf; first-wins
// mirrors the JPEG first-APP1 rule).
func firstWinsPNG(t testing.TB) []byte {
	t.Helper()
	base := makePNG(t, 64, 64)
	var out bytes.Buffer
	out.Write(base[:33])
	out.Write(pngChunkBytes(t, "eXIf", bareExifPayload(3, binary.LittleEndian)))
	out.Write(pngChunkBytes(t, "eXIf", bareExifPayload(6, binary.LittleEndian)))
	out.Write(base[33:])
	return out.Bytes()
}

// noExifAtBudgetIDAT builds a 33-byte IHDR head plus a reader serving tEXt
// chunks totaling exactly MaxMetadataBytes − 41 (+over bytes) followed by the
// IDAT chunk header: with head = 33 the walk's r-consumption is T + 8, and
// at-cap lands exactly on the budget at the IDAT header read.
func noExifAtBudgetIDAT(t testing.TB, over int) ([]byte, io.Reader) {
	t.Helper()
	head := makePNG(t, 64, 64)[:33]
	var pre bytes.Buffer
	for i := 0; i < 7; i++ {
		pre.Write(tEXtChunk(t, 1<<20-12)) // 1 MiB structure each
	}
	pre.Write(tEXtChunk(t, MaxMetadataBytes-41+over-7<<20-12))
	pre.Write(pngChunkHeader(t, "IDAT", 0))
	return head, bytes.NewReader(pre.Bytes())
}

// atBudgetPreIDAT builds a 33-byte IHDR head plus a reader serving 7 full
// 1 MiB tEXt chunks, one remainder tEXt chunk and an eXIf chunk (o=6) — total
// pre-IDAT structure T = MaxMetadataBytes − 29 (+over bytes). With head = 33
// the walk's r-consumption is T − 4 (all structure from r except the eXIf
// CRC, which the walk never reads): at-cap lands exactly on the budget at
// the eXIf data read, one byte more cannot fit it.
func atBudgetPreIDAT(t testing.TB, over int) ([]byte, io.Reader) {
	t.Helper()
	head := makePNG(t, 64, 64)[:33]
	var pre bytes.Buffer
	for i := 0; i < 7; i++ {
		pre.Write(tEXtChunk(t, 1<<20-12)) // 1 MiB structure each
	}
	pre.Write(tEXtChunk(t, MaxMetadataBytes-29+over-38-7<<20-12))
	pre.Write(pngChunkBytes(t, "eXIf", bareExifPayload(6, binary.LittleEndian)))
	return head, bytes.NewReader(pre.Bytes())
}

// buildBudgetFixture splices 7 full 1 MiB tEXt chunks, one remainder tEXt
// chunk and the eXIf chunk (o=6) between IHDR and IDAT, sizing the pre-IDAT
// structure to exactly MaxMetadataBytes − 29 (+over). With head = 4096 the
// walk's r-consumption is T − 4067: at-cap lands exactly on the budget at
// the eXIf data read.
func buildBudgetFixture(t testing.TB, base []byte, over int) []byte {
	var out bytes.Buffer
	out.Write(base[:33])
	for i := 0; i < 7; i++ {
		out.Write(tEXtChunk(t, 1<<20-12)) // 1 MiB structure each
	}
	out.Write(tEXtChunk(t, MaxMetadataBytes-29+over-38-7<<20-12))
	out.Write(pngChunkBytes(t, "eXIf", bareExifPayload(6, binary.LittleEndian)))
	out.Write(base[33:]) // IDAT + IEND
	return out.Bytes()
}
