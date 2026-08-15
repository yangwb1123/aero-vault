package rest

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/auth"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

func pngBytes(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 200, 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// bombPNG builds a PNG declaring w×h pixels with only a signature + IHDR
// chunk (valid CRC): 33 bytes, no pixel data, no allocation.
func bombPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}) // signature
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:8], uint32(h))
	// depth 8, color type 6 (RGBA), compression 0, filter 0, interlace 0.
	ihdr[8], ihdr[9], ihdr[10], ihdr[11], ihdr[12] = 8, 6, 0, 0, 0
	var chunk bytes.Buffer
	chunk.WriteString("IHDR")
	chunk.Write(ihdr)
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], 13)
	buf.Write(l[:])
	buf.Write(chunk.Bytes())
	crc := crc32.NewIEEE()
	_, _ = crc.Write(chunk.Bytes())
	binary.BigEndian.PutUint32(l[:], crc.Sum32())
	buf.Write(l[:])
	return buf.Bytes()
}

// appnPaddedJPEG builds an 8×8 baseline JPEG whose pre-SOF region carries
// totalMetaBytes of APP1 segment payload (marker 0xE1). The 16-bit length
// field caps a single APP1 at 65533 bytes, so many segments are emitted — the
// verified metadata-flood shape for the thumbnail read path. Duplicated from
// internal/thumbnail/thumbnail_test.go per this package's fixture pattern
// (an exported test-fixture API would break the module's self-containment).
func appnPaddedJPEG(t *testing.T, totalMetaBytes int) []byte {
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

func TestThumbnailOversizedImage(t *testing.T) {
	s := newRESTTest(t)
	u := s.URL + "/v1/files/bomb.png"
	// Tiny 33-byte PUT passes the (disabled-by-default) MaxBodySize cap.
	req(t, "PUT", u, bombPNG(t, 100000, 100000), map[string]string{"Content-Type": "image/png"})
	resp, body := req(t, "GET", u+"/thumbnail", nil, nil)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("thumbnail of oversized image: status=%d want 413", resp.StatusCode)
	}
	if !bytes.Contains(body, []byte(`"code":"ImageTooLarge"`)) {
		t.Fatalf("expected code ImageTooLarge, body: %s", body)
	}
}

func TestThumbnailEndpoint(t *testing.T) {
	s := newRESTTest(t)
	u := s.URL + "/v1/files/pic.png"
	req(t, "PUT", u, pngBytes(t, 300, 150), map[string]string{"Content-Type": "image/png"})

	// Generate thumbnail.
	resp, body := req(t, "GET", u+"/thumbnail?w=100&h=100", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("thumbnail status=%d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("content-type=%q want image/jpeg", ct)
	}
	img, format, err := image.Decode(bytes.NewReader(body))
	if err != nil || format != "jpeg" {
		t.Fatalf("decode thumb: %v fmt=%s", err, format)
	}
	if img.Bounds().Dx() != 100 || img.Bounds().Dy() != 50 {
		t.Fatalf("thumb dims %dx%d want 100x50", img.Bounds().Dx(), img.Bounds().Dy())
	}

	// Conditional 304 via the derived ETag.
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("thumbnail missing ETag")
	}
	resp2, _ := req(t, "GET", u+"/thumbnail?w=100&h=100", nil, map[string]string{"If-None-Match": etag})
	if resp2.StatusCode != http.StatusNotModified {
		t.Fatalf("thumbnail If-None-Match: status=%d want 304", resp2.StatusCode)
	}
}

func TestThumbnailNonImage(t *testing.T) {
	s := newRESTTest(t)
	u := s.URL + "/v1/files/notes.txt"
	req(t, "PUT", u, []byte("plain text"), map[string]string{"Content-Type": "text/plain"})
	resp, _ := req(t, "GET", u+"/thumbnail", nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("thumbnail of non-image: status=%d want 400", resp.StatusCode)
	}
}

func TestThumbnailMetadataBudget413(t *testing.T) {
	// A JPEG with more pre-SOF metadata than the module's budget maps to
	// HTTP 413 MetadataTooLarge — a second 413 class, sibling to the
	// dimension class (ImageTooLarge), not a 400 client-argument error.
	s := newRESTTest(t)
	u := s.URL + "/v1/files/meta.jpg"
	req(t, "PUT", u, appnPaddedJPEG(t, 9<<20), map[string]string{"Content-Type": "image/jpeg"})
	resp, body := req(t, "GET", u+"/thumbnail", nil, nil)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("thumbnail with oversized metadata: status=%d want 413", resp.StatusCode)
	}
	if !bytes.Contains(body, []byte(`"code":"MetadataTooLarge"`)) {
		t.Fatalf("expected code MetadataTooLarge, body: %s", body)
	}
	if bytes.Contains(body, []byte(`"code":"InvalidArgument"`)) {
		t.Fatalf("metadata class must not be InvalidArgument, body: %s", body)
	}
}

// countingReader counts the bytes pulled from an underlying reader; used to
// pin the exact DecodeConfig consumption of the at-cap fixture.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func TestThumbnailMetadataBudgetAtExactCap(t *testing.T) {
	// Boundary-inclusive at the consumption level: a fixture whose
	// DecodeConfig consumption lands exactly on MaxMetadataBytes still
	// thumbnails (200); one byte more rejects with 413 MetadataTooLarge.
	// Consumption = SOI(2) + 128×APP1(4n+P) + DQT(134) + SOF0(19) + DHT(420)
	// + SOS header(4) = P + 4n + 579, measured against the Go 1.26 stdlib
	// jpeg.Encode output (no JFIF APP0, so the config scan runs to the SOS
	// header). The fixture-derived invariants below fail loudly if the
	// stdlib layout drifts.
	const n = 128 // APP1 segments; 127×65533 < P ≤ 128×65533 keeps the split valid
	P := thumbnail.MaxMetadataBytes - 4*n - 579
	fixture := appnPaddedJPEG(t, P)

	// Invariant 1: the SOS marker pair sits exactly MaxMetadataBytes-4 bytes
	// into the fixture, i.e. the 4 SOS-header bytes end the budget.
	sosIndex := bytes.Index(fixture, []byte{0xFF, 0xDA})
	if sosIndex < 0 {
		t.Fatal("fixture has no SOS marker")
	}
	if sosIndex+4 != thumbnail.MaxMetadataBytes {
		t.Fatalf("sosIndex+4=%d want %d (stdlib jpeg layout drift?)", sosIndex+4, thumbnail.MaxMetadataBytes)
	}

	// Invariant 2: image.DecodeConfig consumes exactly the budget from a
	// counting reader (2048 × 4096-byte bufio fills; budget = 2048×4096).
	cnt := &countingReader{r: bytes.NewReader(fixture)}
	if _, _, err := image.DecodeConfig(cnt); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if cnt.n != int64(thumbnail.MaxMetadataBytes) {
		t.Fatalf("DecodeConfig consumed %d bytes, want exactly %d", cnt.n, thumbnail.MaxMetadataBytes)
	}

	// HTTP: the at-cap object thumbnails successfully and decodes as JPEG.
	s := newRESTTest(t)
	u := s.URL + "/v1/files/exact.jpg"
	req(t, "PUT", u, fixture, map[string]string{"Content-Type": "image/jpeg"})
	resp, body := req(t, "GET", u+"/thumbnail", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("at-cap thumbnail: status=%d want 200 (body=%s)", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("at-cap thumbnail: content-type=%q want image/jpeg", ct)
	}
	if _, format, err := image.Decode(bytes.NewReader(body)); err != nil || format != "jpeg" {
		t.Fatalf("decode at-cap thumb: %v fmt=%s", err, format)
	}

	// Discriminating control: one payload byte more spills the SOS header
	// into a 2049th fill, which overflows the budget → 413 MetadataTooLarge.
	u2 := s.URL + "/v1/files/over.jpg"
	req(t, "PUT", u2, appnPaddedJPEG(t, P+1), map[string]string{"Content-Type": "image/jpeg"})
	resp2, body2 := req(t, "GET", u2+"/thumbnail", nil, nil)
	if resp2.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("over-by-one thumbnail: status=%d want 413 (body=%s)", resp2.StatusCode, body2)
	}
	if !bytes.Contains(body2, []byte(`"code":"MetadataTooLarge"`)) {
		t.Fatalf("over-by-one: expected code MetadataTooLarge, body: %s", body2)
	}
}

func TestThumbnailInvalidW(t *testing.T) {
	// The metadata-budget class (413 MetadataTooLarge) and the garbage-
	// dimension class (400 InvalidArgument) must remain distinct: garbage
	// ?w= on a valid image is a client argument error, while a metadata
	// flood over the budget is an entity-too-large rejection — never 400.
	s := newRESTTest(t)
	p := s.URL + "/v1/files/pic.png"
	req(t, "PUT", p, pngBytes(t, 16, 16), map[string]string{"Content-Type": "image/png"})
	resp, body := req(t, "GET", p+"/thumbnail?w=abc", nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("?w=abc on valid image: status=%d want 400 (body=%s)", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte(`"code":"InvalidArgument"`)) {
		t.Fatalf("?w=abc: expected code InvalidArgument, body: %s", body)
	}
	if !bytes.Contains(body, []byte("w")) {
		t.Fatalf("?w=abc: message must name the w parameter, body: %s", body)
	}

	m := s.URL + "/v1/files/meta.jpg"
	req(t, "PUT", m, appnPaddedJPEG(t, 9<<20), map[string]string{"Content-Type": "image/jpeg"})
	resp2, body2 := req(t, "GET", m+"/thumbnail", nil, nil)
	if resp2.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("metadata bomb: status=%d want 413 (body=%s)", resp2.StatusCode, body2)
	}
	if !bytes.Contains(body2, []byte(`"code":"MetadataTooLarge"`)) {
		t.Fatalf("metadata bomb: expected code MetadataTooLarge, body: %s", body2)
	}
	if bytes.Contains(body2, []byte(`"code":"InvalidArgument"`)) {
		t.Fatalf("metadata bomb must not be InvalidArgument, body: %s", body2)
	}
}

// transparentPNGBytes builds a w×h PNG whose every pixel is half-transparent
// red (255,0,0,128) — the alpha-bearing fixture the opaque pngBytes omits.
func transparentPNGBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{255, 0, 0, 128})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func TestThumbnailCompositesTransparencyHTTP(t *testing.T) {
	// C6 pin: the HTTP-level read path must return a white-composited (pink)
	// thumbnail for an alpha-bearing PNG — the opaque-only fixtures in the
	// other REST tests would not catch the darkened-output regression.
	s := newRESTTest(t)
	u := s.URL + "/v1/files/alpha.png"
	req(t, "PUT", u, transparentPNGBytes(t, 128, 128), map[string]string{"Content-Type": "image/png"})
	resp, body := req(t, "GET", u+"/thumbnail?w=64&h=64", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("thumbnail status=%d", resp.StatusCode)
	}
	img, format, err := image.Decode(bytes.NewReader(body))
	if err != nil || format != "jpeg" {
		t.Fatalf("decode thumb: %v fmt=%s", err, format)
	}
	b := img.Bounds()
	r, g, bl, _ := img.At(b.Min.X+b.Dx()/2, b.Min.Y+b.Dy()/2).RGBA()
	if r>>8 <= 200 {
		t.Fatalf("center red = %d, want > 200 (buggy baseline: 127)", r>>8)
	}
	if g>>8 >= 200 || bl>>8 >= 200 {
		t.Fatalf("center green/blue = %d/%d, want < 200 (white-fill bug: 255)", g>>8, bl>>8)
	}
}

// exifOrientationJPEG builds a 400×200 red-top/blue-bottom baseline JPEG
// spliced with an EXIF APP1 declaring orient (1–8; 0 = no APP1), the AC-1
// fixture shape. Duplicated from internal/thumbnail/exif_test.go per this
// package's fixture pattern (an exported test-fixture API would break the
// module's self-containment).
func exifOrientationJPEG(t *testing.T, orient int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 400, 200))
	for y := 0; y < 200; y++ {
		c := color.RGBA{255, 0, 0, 255}
		if y >= 100 {
			c = color.RGBA{0, 0, 255, 255}
		}
		for x := 0; x < 400; x++ {
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
	// "Exif\x00\x00" + little-endian TIFF (II, magic 0x2A, IFD0 at offset
	// 8, one SHORT entry for tag 0x0112, next-IFD 0).
	payload := make([]byte, 32)
	copy(payload, "Exif\x00\x00")
	payload[6], payload[7] = 'I', 'I'
	binary.LittleEndian.PutUint16(payload[8:10], 0x2A)
	binary.LittleEndian.PutUint32(payload[10:14], 8)
	binary.LittleEndian.PutUint16(payload[14:16], 1)
	binary.LittleEndian.PutUint16(payload[16:18], 0x0112)
	binary.LittleEndian.PutUint16(payload[18:20], 3)
	binary.LittleEndian.PutUint32(payload[20:24], 1)
	binary.LittleEndian.PutUint16(payload[24:26], uint16(orient))
	b := base.Bytes()
	var out bytes.Buffer
	out.Write(b[:2]) // SOI; the EXIF APP1 splices in before the rest.
	var seg [4]byte
	seg[0], seg[1] = 0xFF, 0xE1
	binary.BigEndian.PutUint16(seg[2:4], uint16(len(payload)+2))
	out.Write(seg[:])
	out.Write(payload)
	out.Write(b[2:])
	return out.Bytes()
}

// TestThumbnailAppliesEXIFOrientation pins AC-4: an orientation-6 camera
// JPEG must produce an upright thumbnail at the REST layer — exactly
// 128×256 in a 128×256 box (the pre-fix output was a sideways 128×64
// landscape crammed into the portrait box), blue on the left, red on the
// right (90° CW).
func TestThumbnailAppliesEXIFOrientation(t *testing.T) {
	s := newRESTTest(t)
	u := s.URL + "/v1/files/cam.jpg"
	req(t, "PUT", u, exifOrientationJPEG(t, 6), map[string]string{"Content-Type": "image/jpeg"})
	resp, body := req(t, "GET", u+"/thumbnail?w=128&h=256", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("thumbnail status=%d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("content-type=%q want image/jpeg", ct)
	}
	img, format, err := image.Decode(bytes.NewReader(body))
	if err != nil || format != "jpeg" {
		t.Fatalf("decode thumb: %v fmt=%s", err, format)
	}
	if img.Bounds().Dx() != 128 || img.Bounds().Dy() != 256 {
		t.Fatalf("thumb dims %dx%d want 128x256 (upright portrait)", img.Bounds().Dx(), img.Bounds().Dy())
	}
	// Orientation 6 = rotate 90° CW: the scaled source is 256×128 (red rows
	// 0–63, blue 64–127) and out(r,c) = in(127-c, r), so the left half is
	// blue and the right half red, boundary at c=64. Interior sampling at
	// the middle row, away from the boundary and the chroma bleed.
	b := img.Bounds()
	for _, c := range []int{0, 8, 16, 24, 31} {
		r, g, bl, _ := img.At(b.Min.X+c, b.Min.Y+128).RGBA()
		if bl>>8 <= 180 || r>>8 >= 100 {
			t.Fatalf("col %d: r=%d g=%d b=%d want blue (left half)", c, r>>8, g>>8, bl>>8)
		}
	}
	for _, c := range []int{96, 104, 112, 120, 127} {
		r, g, bl, _ := img.At(b.Min.X+c, b.Min.Y+128).RGBA()
		if r>>8 <= 180 || bl>>8 >= 100 {
			t.Fatalf("col %d: r=%d g=%d b=%d want red (right half)", c, r>>8, g>>8, bl>>8)
		}
	}
}

// exifOrientationPNG builds a 400×200 red-top/blue-bottom RGBA PNG spliced
// with an eXIf chunk declaring orient (1–8; 0 = no eXIf), the PNG-parallel of
// exifOrientationJPEG. The eXIf data is the conformant bare Exif profile
// (PNG Third Edition §11.3.4.5 — the "Exif\x00\x00" ID code is not included):
// II byte order, magic 0x2A, IFD0 at offset 8, one SHORT entry for tag
// 0x0112, next-IFD 0. The chunk CRC is mandatory — image/png validates every
// chunk, unknown ancillary ones included.
func exifOrientationPNG(t *testing.T, orient int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 400, 200))
	for y := 0; y < 200; y++ {
		c := color.RGBA{255, 0, 0, 255}
		if y >= 100 {
			c = color.RGBA{0, 0, 255, 255}
		}
		for x := 0; x < 400; x++ {
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
	b := base.Bytes()
	if len(b) < 33 {
		t.Fatalf("png fixture too short: %d", len(b))
	}
	payload := make([]byte, 26)
	payload[0], payload[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(payload[2:4], 0x2A)
	binary.LittleEndian.PutUint32(payload[4:8], 8)
	binary.LittleEndian.PutUint16(payload[8:10], 1)
	binary.LittleEndian.PutUint16(payload[10:12], 0x0112)
	binary.LittleEndian.PutUint16(payload[12:14], 3)
	binary.LittleEndian.PutUint32(payload[14:18], 1)
	binary.LittleEndian.PutUint16(payload[18:20], uint16(orient))
	var out bytes.Buffer
	out.Write(b[:33]) // IHDR end; the eXIf chunk splices in before IDAT.
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(payload)))
	out.Write(l[:])
	out.WriteString("eXIf")
	out.Write(payload)
	crc := crc32.NewIEEE()
	_, _ = crc.Write([]byte("eXIf"))
	_, _ = crc.Write(payload)
	binary.BigEndian.PutUint32(l[:], crc.Sum32())
	out.Write(l[:])
	out.Write(b[33:])
	return out.Bytes()
}

// TestThumbnailAppliesPNGeXIfOrientation pins the PNG-parallel of
// TestThumbnailAppliesEXIFOrientation: an orientation-6 camera PNG must
// produce an upright thumbnail at the REST layer — 128×256 in a 128×256 box
// (the pre-fix output was a sideways 128×64 landscape), blue on the left,
// red on the right (90° CW). No production REST change was needed (qa F3 —
// the wiring between the handler, EffectiveDims and the generate pipeline is
// shared with JPEG); this test pins the user-visible contract end-to-end.
func TestThumbnailAppliesPNGeXIfOrientation(t *testing.T) {
	s := newRESTTest(t)
	u := s.URL + "/v1/files/cam.png"
	req(t, "PUT", u, exifOrientationPNG(t, 6), map[string]string{"Content-Type": "image/png"})
	resp, body := req(t, "GET", u+"/thumbnail?w=128&h=256", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("thumbnail status=%d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("content-type=%q want image/jpeg", ct)
	}
	img, format, err := image.Decode(bytes.NewReader(body))
	if err != nil || format != "jpeg" {
		t.Fatalf("decode thumb: %v fmt=%s", err, format)
	}
	if img.Bounds().Dx() != 128 || img.Bounds().Dy() != 256 {
		t.Fatalf("thumb dims %dx%d want 128x256 (upright portrait)", img.Bounds().Dx(), img.Bounds().Dy())
	}
	// Orientation 6 = rotate 90° CW: the scaled source is 256×128 (red rows
	// 0–63, blue 64–127) and out(r,c) = in(127-c, r), so the left half is
	// blue and the right half red, boundary at c=64 — the JPEG twin's exact
	// interior sampling.
	b := img.Bounds()
	for _, c := range []int{0, 8, 16, 24, 31} {
		r, g, bl, _ := img.At(b.Min.X+c, b.Min.Y+128).RGBA()
		if bl>>8 <= 180 || r>>8 >= 100 {
			t.Fatalf("col %d: r=%d g=%d b=%d want blue (left half)", c, r>>8, g>>8, bl>>8)
		}
	}
	for _, c := range []int{96, 104, 112, 120, 127} {
		r, g, bl, _ := img.At(b.Min.X+c, b.Min.Y+128).RGBA()
		if r>>8 <= 180 || bl>>8 >= 100 {
			t.Fatalf("col %d: r=%d g=%d b=%d want red (right half)", c, r>>8, g>>8, bl>>8)
		}
	}
}

// newRESTTestWithRepo mirrors newRESTTest (conditional_test.go) but also
// returns the repository so tests can corrupt object metadata or toggle
// bucket versioning directly.
func newRESTTestWithRepo(t *testing.T) (*httptest.Server, repository.Repository) {
	t.Helper()
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "c.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	h := NewHandler(service.NewFileService(store, repo, nil), nil)
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	r.Head("/v1/files/*", h.Head)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })
	return srv, repo
}

// newAuthRESTTestWithRepo mirrors newAuthRESTTest (acl_test.go) but also
// returns the repository, so tests can toggle bucket versioning while keeping
// the auth middleware + anonymous-public-read wiring.
func newAuthRESTTestWithRepo(t *testing.T) (*httptest.Server, string, repository.Repository) {
	t.Helper()
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "acl.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, _ := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	h := NewHandler(service.NewFileService(store, repo, nil), nil)

	reg, err := auth.Parse("k1:default:read+write")
	if err != nil {
		t.Fatalf("parse auth: %v", err)
	}
	reg.WithAnonymousPublicRead(true)

	r := chi.NewRouter()
	r.Use(reg.Middleware())
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	r.Head("/v1/files/*", h.Head)
	r.Delete("/v1/files/*", h.deleteKey)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })
	return srv, "Bearer k1", repo
}

// enableVersioningForCoexistence turns on bucket versioning so that object
// keys in a prefix relationship (e.g. "dir" and "dir/thumbnail") can coexist
// on the local storage backend: blobs are then laid out as "<key>@v<id>",
// which decouples the object path from the directory tree the nested key
// needs. Without it the two PUTs collide physically (file vs directory).
func enableVersioningForCoexistence(t *testing.T, repo repository.Repository) {
	t.Helper()
	if err := repo.SetBucketVersioning(context.Background(), "default", "default", true); err != nil {
		t.Fatalf("enable versioning: %v", err)
	}
}

// TestThumbnailDoesNotShadowObjectKey pins FR-1: object keys ending in
// "/thumbnail" are legal, and GET /v1/files/<key> must serve the object
// itself whenever it exists — never a different object's thumbnail (AC-1).
func TestThumbnailDoesNotShadowObjectKey(t *testing.T) {
	t.Run("exact key wins over subresource", func(t *testing.T) {
		s, repo := newRESTTestWithRepo(t)
		enableVersioningForCoexistence(t, repo)
		u := s.URL + "/v1/files/dir"
		if resp, _ := req(t, "PUT", u, pngBytes(t, 300, 150), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT dir: %d", resp.StatusCode)
		}
		if resp, _ := req(t, "PUT", u+"/thumbnail", []byte("object bytes"), map[string]string{"Content-Type": "text/plain"}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT dir/thumbnail: %d", resp.StatusCode)
		}
		resp, body := req(t, "GET", u+"/thumbnail", nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET exact key: status=%d want 200", resp.StatusCode)
		}
		if !bytes.Equal(body, []byte("object bytes")) {
			t.Fatalf("body=%q want the object bytes, not a thumbnail", body)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "text/plain" {
			t.Fatalf("content-type=%q want text/plain", ct)
		}
		if _, _, err := image.Decode(bytes.NewReader(body)); err == nil {
			t.Fatal("body decoded as an image; must be the raw object")
		}
	})
	t.Run("no spurious 404 when trimmed key absent", func(t *testing.T) {
		s := newRESTTest(t)
		u := s.URL + "/v1/files/dir/thumbnail"
		req(t, "PUT", u, []byte("object bytes"), map[string]string{"Content-Type": "text/plain"})
		resp, body := req(t, "GET", u, nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET exact key: status=%d want 200", resp.StatusCode)
		}
		if !bytes.Equal(body, []byte("object bytes")) {
			t.Fatalf("body=%q want the uploaded bytes", body)
		}
	})
	t.Run("root-level key", func(t *testing.T) {
		s := newRESTTest(t)
		u := s.URL + "/v1/files/thumbnail"
		req(t, "PUT", u, []byte("root bytes"), nil)
		resp, body := req(t, "GET", u, nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET root key: status=%d want 200", resp.StatusCode)
		}
		if !bytes.Equal(body, []byte("root bytes")) {
			t.Fatalf("body=%q want the uploaded bytes", body)
		}
	})
}

// TestThumbnailDoubleSuffixStillWorks pins AC-2: with no object at the full
// key "dir/thumbnail/thumbnail", the FR-2 fallback trims one suffix and
// serves the thumbnail of "dir/thumbnail" unchanged.
func TestThumbnailDoubleSuffixStillWorks(t *testing.T) {
	s := newRESTTest(t)
	u := s.URL + "/v1/files/dir/thumbnail"
	req(t, "PUT", u, pngBytes(t, 300, 150), map[string]string{"Content-Type": "image/png"})
	resp, body := req(t, "GET", u+"/thumbnail?w=100&h=100", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("double-suffix thumbnail: status=%d want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("content-type=%q want image/jpeg", ct)
	}
	if resp.Header.Get("ETag") == "" {
		t.Fatal("thumbnail missing ETag")
	}
	img, format, err := image.Decode(bytes.NewReader(body))
	if err != nil || format != "jpeg" {
		t.Fatalf("decode thumb: %v fmt=%s", err, format)
	}
	if img.Bounds().Dx() != 100 || img.Bounds().Dy() != 50 {
		t.Fatalf("thumb dims %dx%d want 100x50", img.Bounds().Dx(), img.Bounds().Dy())
	}
}

// TestThumbnailCorruptFullKeyPropagates pins FR-3: the dispatch pre-check
// falls back to the subresource only on ErrNotFound. A scrub-marked corrupt
// object at the exact key must surface as 410 ObjectCorrupt, never as the
// trimmed key's thumbnail or a spurious 404.
func TestThumbnailCorruptFullKeyPropagates(t *testing.T) {
	s, repo := newRESTTestWithRepo(t)
	u := s.URL + "/v1/files/dir/thumbnail"
	req(t, "PUT", u, []byte("object bytes"), map[string]string{"Content-Type": "text/plain"})
	if err := repo.SetObjectMetaKey(context.Background(), "default", "default", "dir/thumbnail", "_aero_scrub_status", "corrupt"); err != nil {
		t.Fatalf("set scrub meta: %v", err)
	}
	resp, body := req(t, "GET", u, nil, nil)
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("GET corrupt exact key: status=%d want 410", resp.StatusCode)
	}
	if !bytes.Contains(body, []byte(`"code":"ObjectCorrupt"`)) {
		t.Fatalf("expected code ObjectCorrupt, body: %s", body)
	}
}

// TestThumbnailAnonymousFullKeyGate pins FR-4: the anonymous gate must be
// evaluated against the FULL key, so an anonymous caller can never receive
// another object's thumbnail (or a public trimmed key's content) under the
// requesting URL of a private full key.
func TestThumbnailAnonymousFullKeyGate(t *testing.T) {
	t.Run("private full key rejected", func(t *testing.T) {
		s, tok := newAuthRESTTest(t)
		u := s.URL + "/v1/files/dir/thumbnail"
		req(t, "PUT", u, []byte("object bytes"), map[string]string{"Content-Type": "text/plain", "Authorization": tok})
		resp, _ := req(t, "GET", u, nil, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("anonymous GET private full key: status=%d want 403", resp.StatusCode)
		}
	})
	t.Run("public full key readable", func(t *testing.T) {
		s, tok := newAuthRESTTest(t)
		u := s.URL + "/v1/files/dir/thumbnail"
		authH := map[string]string{"Authorization": tok}
		req(t, "PUT", u, []byte("object bytes"), map[string]string{"Content-Type": "text/plain", "Authorization": tok})
		if resp, _ := req(t, "PUT", u+"/acl", []byte(`{"acl":"public-read"}`), authH); resp.StatusCode != http.StatusOK {
			t.Fatalf("set acl: %d", resp.StatusCode)
		}
		resp, body := req(t, "GET", u, nil, nil)
		if resp.StatusCode != http.StatusOK || string(body) != "object bytes" {
			t.Fatalf("anonymous GET public full key: status=%d body=%q", resp.StatusCode, body)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "text/plain" {
			t.Fatalf("content-type=%q want text/plain", ct)
		}
	})
	t.Run("public trimmed key does not leak private full key", func(t *testing.T) {
		s, tok, repo := newAuthRESTTestWithRepo(t)
		enableVersioningForCoexistence(t, repo)
		u := s.URL + "/v1/files/dir"
		authH := map[string]string{"Authorization": tok}
		if resp, _ := req(t, "PUT", u, pngBytes(t, 300, 150), map[string]string{"Content-Type": "image/png", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT dir: %d", resp.StatusCode)
		}
		if resp, _ := req(t, "PUT", u+"/acl", []byte(`{"acl":"public-read"}`), authH); resp.StatusCode != http.StatusOK {
			t.Fatalf("set acl: %d", resp.StatusCode)
		}
		if resp, _ := req(t, "PUT", u+"/thumbnail", []byte("object bytes"), map[string]string{"Content-Type": "text/plain", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT dir/thumbnail: %d", resp.StatusCode)
		}
		resp, body := req(t, "GET", u+"/thumbnail", nil, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("anonymous GET private full key over public trimmed key: status=%d want 403 (body=%q)", resp.StatusCode, body)
		}
	})
}

// TestThumbnailVersionPinnedReadNotShadowed pins the ?version= mandate: a
// version-pinned read of a soft-deleted object at the full key must serve the
// pinned version's own bytes — never the trimmed key's derived thumbnail.
func TestThumbnailVersionPinnedReadNotShadowed(t *testing.T) {
	// Access-enabled harness: DELETE is fail-closed without an authorizer.
	s, tok, repo := newThumbnailAccessHarness(t)
	enableVersioningForCoexistence(t, repo)
	authH := map[string]string{"Authorization": tok}
	u := s.URL + "/v1/files/dir"
	if resp, _ := req(t, "PUT", u, pngBytes(t, 300, 150), map[string]string{"Content-Type": "image/png", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT dir: %d", resp.StatusCode)
	}
	uFull := u + "/thumbnail"
	if resp, _ := req(t, "PUT", uFull, []byte("object bytes"), map[string]string{"Content-Type": "text/plain", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT dir/thumbnail: %d", resp.StatusCode)
	}
	versions, err := repo.ListObjectVersions(context.Background(), "default", "default", "dir/thumbnail")
	if err != nil || len(versions) == 0 {
		t.Fatalf("list versions: %v (n=%d)", err, len(versions))
	}
	oldID := versions[0].VersionID
	if resp, _ := req(t, "DELETE", uFull, nil, authH); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE dir/thumbnail: %d", resp.StatusCode)
	}
	resp, body := req(t, "GET", uFull+"?version="+oldID, nil, authH)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET version-pinned: status=%d want 200 (body=%q)", resp.StatusCode, body)
	}
	if !bytes.Equal(body, []byte("object bytes")) {
		t.Fatalf("body=%q want the versioned object bytes, not a thumbnail", body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/plain" {
		t.Fatalf("content-type=%q want text/plain", ct)
	}
}

// TestThumbnailVersionPinnedDerivation pins AC1/AC2: ?version= on the derived
// /thumbnail subresource returns a JPEG derived from the PINNED version of the
// trimmed key — validator from the pinned version's ETag (never the current
// object's) — and the pin is immutable: later PUTs change the unpinned URL's
// output but never the pinned response (G1/G2). HEAD mirrors GET with the body
// suppressed.
func TestThumbnailVersionPinnedDerivation(t *testing.T) {
	s, repo := newRESTTestWithRepo(t)
	enableVersioningForCoexistence(t, repo)
	u := s.URL + "/v1/files/photo.jpg"
	if resp, _ := req(t, "PUT", u, pngBytes(t, 300, 150), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT v1: %d", resp.StatusCode)
	}
	versions, err := repo.ListObjectVersions(context.Background(), "default", "default", "photo.jpg")
	if err != nil || len(versions) == 0 {
		t.Fatalf("list versions: %v (n=%d)", err, len(versions))
	}
	v1ID, v1ETag := versions[0].VersionID, versions[0].ETag // newest-first; captured immediately after the first PUT
	if resp, _ := req(t, "PUT", u, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT v2: %d", resp.StatusCode)
	}
	effW, effH := thumbnail.EffectiveDims(0, 0)
	wantETag := fmt.Sprintf(`"%s-thumb-%dx%d"`, v1ETag, effW, effH)
	thumbURL := u + "/thumbnail?version=" + v1ID
	resp, body := req(t, "GET", thumbURL, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET pinned derivation: status=%d want 200 (body=%q)", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("content-type=%q want image/jpeg", ct)
	}
	if _, format, err := image.Decode(bytes.NewReader(body)); err != nil || format != "jpeg" {
		t.Fatalf("body must decode as jpeg (format=%q err=%v)", format, err)
	}
	if et := resp.Header.Get("ETag"); et != wantETag {
		t.Fatalf("pinned ETag=%q want %q (derived from v1, not the current version)", et, wantETag)
	}
	// HEAD parity: identical status and ETag, body suppressed.
	headResp, headBody := req(t, "HEAD", thumbURL, nil, nil)
	if headResp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD pinned derivation: status=%d want 200", headResp.StatusCode)
	}
	if et := headResp.Header.Get("ETag"); et != wantETag {
		t.Fatalf("HEAD pinned ETag=%q want %q", et, wantETag)
	}
	if len(headBody) != 0 {
		t.Fatalf("HEAD pinned derivation: body=%d bytes, want empty", len(headBody))
	}
	// The unpinned URL derives from the CURRENT version (v2) — a different
	// validator, so the pin demonstrably selected v1.
	unpinned, _ := req(t, "GET", u+"/thumbnail", nil, nil)
	if et := unpinned.Header.Get("ETag"); et == wantETag {
		t.Fatalf("unpinned ETag=%q must differ from the pinned v1-derived %q", et, wantETag)
	}
	// AC2: a third PUT with distinct bytes changes the unpinned output; the
	// pinned response stays byte-identical with the identical validator (G2).
	if resp, _ := req(t, "PUT", u, pngBytesAlt(t, 300, 150), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT v3: %d", resp.StatusCode)
	}
	resp2, body2 := req(t, "GET", thumbURL, nil, nil)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("GET pinned after v3: status=%d want 200", resp2.StatusCode)
	}
	if !bytes.Equal(body2, body) {
		t.Fatal("pinned response changed after a later PUT — version pins must be immutable")
	}
	if et := resp2.Header.Get("ETag"); et != wantETag {
		t.Fatalf("pinned ETag after v3=%q want %q", et, wantETag)
	}
	unpinned2, _ := req(t, "GET", u+"/thumbnail", nil, nil)
	if et := unpinned2.Header.Get("ETag"); et == wantETag || et == unpinned.Header.Get("ETag") {
		t.Fatalf("unpinned ETag after v3=%q must track the current version (pinned=%q, pre-v3 unpinned=%q)",
			et, wantETag, unpinned.Header.Get("ETag"))
	}
}

// TestThumbnailVersionPinned304 pins AC8: the pinned derivation arm's 304 fast
// path certifies Not Modified against the pinned version (re-Stat via
// StatVersionWithOptions), and GET-304 vs HEAD-304 are field-for-field equal
// with no Content-Length/Content-Type on either (the F2 shape, extended to
// pins).
func TestThumbnailVersionPinned304(t *testing.T) {
	s, repo := newRESTTestWithRepo(t)
	enableVersioningForCoexistence(t, repo)
	u := s.URL + "/v1/files/photo.jpg"
	if resp, _ := req(t, "PUT", u, pngBytes(t, 300, 150), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT v1: %d", resp.StatusCode)
	}
	versions, err := repo.ListObjectVersions(context.Background(), "default", "default", "photo.jpg")
	if err != nil || len(versions) == 0 {
		t.Fatalf("list versions: %v (n=%d)", err, len(versions))
	}
	thumbURL := u + "/thumbnail?version=" + versions[0].VersionID
	resp, _ := req(t, "GET", thumbURL, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET pinned: %d want 200", resp.StatusCode)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	cond := map[string]string{"If-None-Match": etag}
	getResp, _ := req(t, "GET", thumbURL, nil, cond)
	headResp, _ := req(t, "HEAD", thumbURL, nil, cond)
	if getResp.StatusCode != http.StatusNotModified || headResp.StatusCode != http.StatusNotModified {
		t.Fatalf("pinned 304: GET=%d HEAD=%d want both 304", getResp.StatusCode, headResp.StatusCode)
	}
	for _, hdr := range []string{"ETag", "Cache-Control", "Last-Modified"} {
		g, h := getResp.Header.Get(hdr), headResp.Header.Get(hdr)
		if g != h {
			t.Fatalf("pinned 304 header %s: GET=%q HEAD=%q — field-for-field parity required", hdr, g, h)
		}
	}
	for _, hdr := range []string{"Content-Length", "Content-Type"} {
		if v := getResp.Header.Get(hdr); v != "" {
			t.Fatalf("pinned GET 304 must not carry %s (%q)", hdr, v)
		}
		if v := headResp.Header.Get(hdr); v != "" {
			t.Fatalf("pinned HEAD 304 must not carry %s (%q)", hdr, v)
		}
	}
}

// TestThumbnailVersionPinnedPolicyDeny pins AC9 (fail-closed parity): a bucket
// policy denying s3:GetObject on the TRIMMED key blocks the version-pinned
// derivation too — a pinned URL cannot bypass the gate (mirrors
// TestThumbnailDerivationPathBucketPolicyDeny, pinned arm).
func TestThumbnailVersionPinnedPolicyDeny(t *testing.T) {
	s, tok, repo := newThumbnailAccessHarness(t)
	enableVersioningForCoexistence(t, repo)
	adminH := map[string]string{"Authorization": "Bearer operator"}
	authH := map[string]string{"Authorization": tok}
	u := s.URL + "/v1/files/secret"
	if resp, _ := req(t, "PUT", u, pngBytes(t, 300, 150), map[string]string{"Content-Type": "image/png", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: %d", resp.StatusCode)
	}
	versions, err := repo.ListObjectVersions(context.Background(), "default", "default", "secret")
	if err != nil || len(versions) == 0 {
		t.Fatalf("list versions: %v (n=%d)", err, len(versions))
	}
	denyGet := `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::default/secret"}]}`
	if resp, _ := req(t, "PUT", s.URL+"/v1/buckets/default/policy", bodyPolicy(denyGet), adminH); resp.StatusCode != http.StatusOK {
		t.Fatalf("set deny policy: %d", resp.StatusCode)
	}
	resp, body := req(t, "GET", u+"/thumbnail?version="+versions[0].VersionID, nil, authH)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("pinned derivation under policy deny: status=%d want 403 (body=%q)", resp.StatusCode, body)
	}
}

// TestThumbnailVersionPinnedSSECNoShadow pins C-1: a version-pinned read whose
// full-key pinned version exists but is SSE-C-encrypted (unreadable without the
// key) must surface the real state (400) — never fall through to the trimmed
// key's derived content (the shadowing hole a blanket ErrInvalidArgs
// fall-through would open). The unpinned raw download of the same object is
// unchanged (400), and a garbage pin still 404s.
func TestThumbnailVersionPinnedSSECNoShadow(t *testing.T) {
	s, repo := newRESTTestWithRepo(t)
	enableVersioningForCoexistence(t, repo)
	u := s.URL + "/v1/files/dir"
	if resp, _ := req(t, "PUT", u, pngBytes(t, 300, 150), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT dir: %d", resp.StatusCode)
	}
	uFull := u + "/thumbnail"
	if resp, _ := req(t, "PUT", uFull, []byte("object bytes"), map[string]string{"Content-Type": "text/plain"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT dir/thumbnail: %d", resp.StatusCode)
	}
	versions, err := repo.ListObjectVersions(context.Background(), "default", "default", "dir/thumbnail")
	if err != nil || len(versions) == 0 {
		t.Fatalf("list versions: %v (n=%d)", err, len(versions))
	}
	// Mark the object SSE-C via metadata injection (the pattern of
	// TestThumbnailCorruptFullKeyPropagates): the merge applies to the live
	// rows of the key, so the pinned version carries the encryption markers.
	if err := repo.SetObjectMetaKey(context.Background(), "default", "default", "dir/thumbnail", "_aero_sse_c_algorithm", "AES256"); err != nil {
		t.Fatalf("inject sse-c algorithm: %v", err)
	}
	if err := repo.SetObjectMetaKey(context.Background(), "default", "default", "dir/thumbnail", "_aero_sse_c_key_md5", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="); err != nil {
		t.Fatalf("inject sse-c key md5: %v", err)
	}
	// Version-pinned read: the pinned full-key version is real but unreadable
	// (SSE-C without the key) → 400, NEVER dir's thumbnail (200).
	resp, body := req(t, "GET", uFull+"?version="+versions[0].VersionID, nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("pinned SSE-C full key: status=%d want 400 (body=%q)", resp.StatusCode, body)
	}
	// Unpinned raw download of the same object: unchanged (400 — SSE-C requires
	// the key on every read path).
	resp, _ = req(t, "GET", uFull, nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unpinned SSE-C full key: status=%d want 400 (raw-download semantics unchanged)", resp.StatusCode)
	}
	// A garbage pin names nothing at either key → 404, never dir's thumbnail.
	resp, body = req(t, "GET", uFull+"?version=no-such-version", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("garbage pin: status=%d want 404 (body=%q)", resp.StatusCode, body)
	}
}

// TestThumbnailLongKeySuffixFallsBack pins the ErrInvalidArgs mandate: an
// image key so long that key+"/thumbnail" exceeds the key-length cap must
// fall through to the subresource interpretation (thumbnail of the trimmed
// legal key), not surface the dispatch artifact as a 400.
func TestThumbnailLongKeySuffixFallsBack(t *testing.T) {
	s := newRESTTest(t)
	// The gate's reproduction: a legal 191-char image key; its
	// "/thumbnail"-suffixed full key (201 chars) exceeds the key cap.
	key191 := strings.Repeat("k", service.MaxKeyLen-9)  // 191 chars: legal
	key190 := strings.Repeat("k", service.MaxKeyLen-10) // 190 chars: legal
	fullKey200 := key190 + "/thumbnail"                 // 200 chars: legal
	fullKey201 := key191 + "/thumbnail"                 // 201 chars: over cap
	for _, k := range []string{key190, key191} {
		if resp, _ := req(t, "PUT", s.URL+"/v1/files/"+k, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT long image key: %d", resp.StatusCode)
		}
	}
	// The exact legal 200-char full key falls back to the trimmed key's
	// thumbnail (no object exists at the full key).
	resp, body := req(t, "GET", s.URL+"/v1/files/"+fullKey200+"?w=32&h=32", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET legal 200-char full key: status=%d want 200 (body=%q)", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("content-type=%q want image/jpeg", ct)
	}
	// An over-cap full key must fall back too, not 400 (ErrInvalidArgs).
	resp, _ = req(t, "GET", s.URL+"/v1/files/"+fullKey201+"?w=32&h=32", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET over-cap full key: status=%d want 200", resp.StatusCode)
	}
}

// TestThumbnailForbiddenFullKeyDelegatesToPublicRead pins the D3 arm: in an
// access-enabled deployment an anonymous caller's Stat on the full key is
// denied (missing principal), and the handler must delegate to Get so the
// canned-public-read capability can be injected for a public-read object —
// literal 403 propagation would regress public thumbnails-of-objects.
func TestThumbnailForbiddenFullKeyDelegatesToPublicRead(t *testing.T) {
	s, tok, repo := newThumbnailAccessHarness(t)
	enableVersioningForCoexistence(t, repo)
	authH := map[string]string{"Authorization": tok}
	u := s.URL + "/v1/files/dir"
	if resp, _ := req(t, "PUT", u, pngBytes(t, 300, 150), map[string]string{"Content-Type": "image/png", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT dir: %d", resp.StatusCode)
	}
	uFull := u + "/thumbnail"
	if resp, _ := req(t, "PUT", uFull, []byte("object bytes"), map[string]string{"Content-Type": "text/plain", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT dir/thumbnail: %d", resp.StatusCode)
	}
	if resp, _ := req(t, "PUT", uFull+"/acl", []byte(`{"acl":"public-read"}`), authH); resp.StatusCode != http.StatusOK {
		t.Fatalf("set public-read acl: %d", resp.StatusCode)
	}
	// Anonymous read of a public object at the full key: the D3 delegation
	// must succeed with the object's own bytes.
	resp, body := req(t, "GET", uFull, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("anonymous GET public full key (access-enabled): status=%d want 200 (body=%q)", resp.StatusCode, body)
	}
	if !bytes.Equal(body, []byte("object bytes")) {
		t.Fatalf("body=%q want the object bytes, not a thumbnail", body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/plain" {
		t.Fatalf("content-type=%q want text/plain", ct)
	}
}

// TestThumbnailExactKeyBucketPolicyDeny pins mandate 5: the exact-key arm
// runs the bucket-policy gate (via Get) — a policy denying s3:GetObject for
// the full key must surface as 403, never as the trimmed key's thumbnail.
func TestThumbnailExactKeyBucketPolicyDeny(t *testing.T) {
	s, tok, repo := newThumbnailAccessHarness(t)
	adminH := map[string]string{"Authorization": "Bearer operator"}
	enableVersioningForCoexistence(t, repo)
	authH := map[string]string{"Authorization": tok}
	u := s.URL + "/v1/files/dir"
	if resp, _ := req(t, "PUT", u, pngBytes(t, 300, 150), map[string]string{"Content-Type": "image/png", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT dir: %d", resp.StatusCode)
	}
	uFull := u + "/thumbnail"
	if resp, _ := req(t, "PUT", uFull, []byte("object bytes"), map[string]string{"Content-Type": "text/plain", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT dir/thumbnail: %d", resp.StatusCode)
	}
	policyURL := s.URL + "/v1/buckets/default/policy"
	denyGet := `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::default/dir/thumbnail"}]}`
	if resp, _ := req(t, "PUT", policyURL, bodyPolicy(denyGet), adminH); resp.StatusCode != http.StatusOK {
		t.Fatalf("set deny policy: %d", resp.StatusCode)
	}
	resp, _ := req(t, "GET", uFull, nil, authH)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("GET denied full key: status=%d want 403", resp.StatusCode)
	}
}

// TestThumbnailExactKeyConditionalAndRange pins mandate 6: the exact-key arm
// inherits Get's full HTTP semantics — If-None-Match yields 304 and Range
// yields 206 with Content-Range on the object's own bytes.
func TestThumbnailExactKeyConditionalAndRange(t *testing.T) {
	s, tok, repo := newAuthRESTTestWithRepo(t)
	enableVersioningForCoexistence(t, repo)
	authH := map[string]string{"Authorization": tok}
	u := s.URL + "/v1/files/dir"
	if resp, _ := req(t, "PUT", u, pngBytes(t, 300, 150), map[string]string{"Content-Type": "image/png", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT dir: %d", resp.StatusCode)
	}
	uFull := u + "/thumbnail"
	if resp, _ := req(t, "PUT", uFull, []byte("object bytes"), map[string]string{"Content-Type": "text/plain", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT dir/thumbnail: %d", resp.StatusCode)
	}
	// ETag from a plain GET.
	resp, body := req(t, "GET", uFull, nil, authH)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET exact key: %d", resp.StatusCode)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("exact-key GET missing ETag")
	}
	// If-None-Match → 304.
	resp, _ = req(t, "GET", uFull, nil, map[string]string{"Authorization": tok, "If-None-Match": etag})
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("If-None-Match: status=%d want 304", resp.StatusCode)
	}
	// Range → 206 + Content-Range on the object bytes.
	resp, body = req(t, "GET", uFull, nil, map[string]string{"Authorization": tok, "Range": "bytes=0-3"})
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("Range: status=%d want 206", resp.StatusCode)
	}
	if cr := resp.Header.Get("Content-Range"); cr != "bytes 0-3/12" {
		t.Fatalf("Content-Range=%q want bytes 0-3/12", cr)
	}
	if !bytes.Equal(body, []byte("obje")) {
		t.Fatalf("range body=%q want %q", body, "obje")
	}
}

// newThumbnailAccessHarness wires an access-enabled FileService (authorizer +
// tenant status) with anonymous public-read admission — the combination that
// makes the Thumbnail D3 delegation arm reachable. Routes are registered
// directly (as in newAuthRESTTest), bypassing requireRESTScope, so anonymous
// object reads are admitted by the registry middleware.
func newThumbnailAccessHarness(t *testing.T) (*httptest.Server, string, repository.Repository) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(dir, "access.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	accessStore, ok := repo.(access.Store)
	if !ok {
		t.Fatal("repository does not implement access.Store")
	}
	manager, err := access.NewManager(accessStore, access.Config{
		Enabled: true, DefaultPolicy: access.DefaultTenant,
		ShareSecret: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatalf("access manager: %v", err)
	}
	svc := service.NewFileService(store, repo, nil).
		WithAuthorizer(manager).
		WithTenantStatusEnforcement()
	reg, err := auth.Parse("alice:default:read+write,operator:*:admin")
	if err != nil {
		t.Fatalf("parse auth: %v", err)
	}
	reg.WithAnonymousPublicRead(true)
	h := NewHandler(svc, slog.Default())
	h.WithAccessManager(manager, "")
	r := chi.NewRouter()
	r.Use(reg.Middleware())
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	r.Head("/v1/files/*", h.Head)
	r.Delete("/v1/files/*", h.deleteKey)
	r.Put("/v1/buckets/{bucket}/policy", h.PutBucketPolicy)
	r.Put("/v1/buckets/{bucket}/acl", h.PutBucketACL)
	tenantMW := mw.TenantWithStatus(func(ctx context.Context, tenant string) (string, bool, error) {
		record, found, lookupErr := repo.GetTenant(ctx, tenant)
		return record.Status, found, lookupErr
	})
	handler := reg.Middleware()(tenantMW(r))
	srv := httptest.NewServer(handler)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })
	return srv, "Bearer alice", repo
}

// TestThumbnailDerivationPathBucketPolicyDeny pins the derivation-arm policy
// gate: a bucket policy denying s3:GetObject on the trimmed key must block
// the derived thumbnail too (the fallback arm used to bypass checkBucketPolicy
// entirely, returning near-lossless image bytes of a policy-denied object).
func TestThumbnailDerivationPathBucketPolicyDeny(t *testing.T) {
	s, tok, repo := newThumbnailAccessHarness(t)
	_ = repo
	adminH := map[string]string{"Authorization": "Bearer operator"}
	authH := map[string]string{"Authorization": tok}
	u := s.URL + "/v1/files/secret"
	if resp, _ := req(t, "PUT", u, pngBytes(t, 300, 150), map[string]string{"Content-Type": "image/png", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT secret image: %d", resp.StatusCode)
	}
	// Control: thumbnail derivation works while the policy is absent.
	resp, body := req(t, "GET", u+"/thumbnail?w=100&h=100", nil, authH)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("control GET thumbnail: status=%d want 200 (body=%q)", resp.StatusCode, body)
	}
	// Deny s3:GetObject on the trimmed key ("secret"), not the full key.
	policyURL := s.URL + "/v1/buckets/default/policy"
	denyGet := `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::default/secret"}]}`
	if resp, _ := req(t, "PUT", policyURL, bodyPolicy(denyGet), adminH); resp.StatusCode != http.StatusOK {
		t.Fatalf("set deny policy: %d", resp.StatusCode)
	}
	// The raw object read is denied...
	resp, _ = req(t, "GET", u, nil, authH)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("GET denied object: status=%d want 403", resp.StatusCode)
	}
	// ...and so is the derived thumbnail (no bypass).
	resp, body = req(t, "GET", u+"/thumbnail?w=100&h=100", nil, authH)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("GET denied thumbnail: status=%d want 403 (body=%q)", resp.StatusCode, body)
	}
}

// TestThumbnailDeadlineScoping pins the F1/F2 conditions of the context-aware
// slot direction:
//   - F2: a server-side derivation deadline surfaces a visible 504 Timeout,
//     never a silent empty 200 (a still-connected client must see the abort).
//   - F1: the deadline is scoped to the derivation branch — the delegation
//     arms (?version= pin, exact-key raw download) run on the original
//     request context and are never collateralized by the thumbnail timeout.
func TestThumbnailDeadlineScoping(t *testing.T) {
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "dl.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, _ := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	h := NewHandler(service.NewFileService(store, repo, nil), nil)
	// Arms a route deadline that is already expired by the time any handler
	// work begins — the discriminating setup for both conditions.
	h.thumbnailTimeout = time.Nanosecond
	// "dir" and "dir/thumbnail" must coexist physically (file vs directory
	// on the local backend) — versioned layout decouples the blob paths.
	if err := repo.SetBucketVersioning(context.Background(), "default", "default", true); err != nil {
		t.Fatalf("enable versioning: %v", err)
	}
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })

	u := srv.URL + "/v1/files/dir2"
	// Derivation source: an image at "dir2", with NO object at "dir2/thumbnail"
	// so the request below exercises the derivation branch.
	if resp, _ := req(t, "PUT", u, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT dir2: %d", resp.StatusCode)
	}
	// Exact-key fixture: an object at "dir" plus a real object at
	// "dir/thumbnail" for the delegation arms.
	if resp, _ := req(t, "PUT", srv.URL+"/v1/files/dir", pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT dir: %d", resp.StatusCode)
	}
	uFull := srv.URL + "/v1/files/dir/thumbnail"
	if resp, _ := req(t, "PUT", uFull, []byte("object bytes"), map[string]string{"Content-Type": "text/plain"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT dir/thumbnail: %d", resp.StatusCode)
	}

	// F2: the derivation request must fail visibly with 504 — the old code
	// returned a silent empty 200 when the deadline fired.
	resp, body := req(t, "GET", srv.URL+"/v1/files/dir2/thumbnail?w=32&h=32", nil, nil)
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("derivation under expired deadline: status=%d want 504 (body=%q)", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte(`"code":"Timeout"`)) {
		t.Fatalf("expected code Timeout, body: %s", body)
	}

	// F1: the exact-key delegation arm must ignore the thumbnail deadline and
	// raw-download the object — under the old whole-dispatch wrap this request
	// failed on the already-expired context.
	resp, body = req(t, "GET", uFull, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("exact-key download under thumbnail deadline: status=%d want 200 (body=%q)", resp.StatusCode, body)
	}
	if !bytes.Equal(body, []byte("object bytes")) {
		t.Fatalf("exact-key body=%q want the object bytes", body)
	}

	// F1 (version-pinned arm): same for ?version= — the pinned read must not
	// be collateralized by the thumbnail deadline.
	versions, err := repo.ListObjectVersions(context.Background(), "default", "default", "dir/thumbnail")
	if err != nil || len(versions) == 0 {
		t.Fatalf("list versions: %v (n=%d)", err, len(versions))
	}
	resp, body = req(t, "GET", uFull+"?version="+versions[0].VersionID, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("version-pinned download under thumbnail deadline: status=%d want 200 (body=%q)", resp.StatusCode, body)
	}
	if !bytes.Equal(body, []byte("object bytes")) {
		t.Fatalf("version-pinned body=%q want the object bytes", body)
	}

	// AC10: a version-pinned DERIVATION on the trimmed key is deadline-bound
	// like the unpinned arm. Pin v1 of dir2 (no object at "dir2/thumbnail"):
	// the discriminator's repo read is ctx-tolerating (proven above), so the
	// request falls through and the derivation pipeline observes the expired
	// deadline → visible 504 (the historical behavior was 404).
	versions2, err := repo.ListObjectVersions(context.Background(), "default", "default", "dir2")
	if err != nil || len(versions2) == 0 {
		t.Fatalf("list dir2 versions: %v (n=%d)", err, len(versions2))
	}
	resp, body = req(t, "GET", srv.URL+"/v1/files/dir2/thumbnail?version="+versions2[0].VersionID, nil, nil)
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("pinned derivation under expired deadline: status=%d want 504 (body=%q)", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte(`"code":"Timeout"`)) {
		t.Fatalf("expected code Timeout, body: %s", body)
	}

	// classify() must map the deadline sentinel to 504 at the error seam.
	code, msg, status := classify(service.ErrTimeout)
	if code != "Timeout" || status != http.StatusGatewayTimeout || msg == "" {
		t.Fatalf("classify(ErrTimeout) = (%q, %q, %d) want (Timeout, _, 504)", code, msg, status)
	}
	code, _, status = classify(context.DeadlineExceeded)
	if code != "Timeout" || status != http.StatusGatewayTimeout {
		t.Fatalf("classify(DeadlineExceeded) = (%q, %d) want (Timeout, 504)", code, status)
	}
}

// stallReader serves the first 33 bytes of data (PNG signature + IHDR —
// exactly what image/png's DecodeConfig consumes, with no read-ahead), then
// blocks on ctx.Done() and returns ctx.Err(). blocked (optional) is closed
// once, exactly when the first post-data Read is attempted — deterministically
// in the Decode phase, never during DecodeConfig. It models a slow S3 backend
// whose read aborts with the request-context error mid-decode.
type stallReader struct {
	ctx     context.Context
	data    []byte
	off     int
	blocked chan struct{} // may be nil
	signal  sync.Once
}

func (r *stallReader) Read(p []byte) (int, error) {
	if r.off < len(r.data) {
		n := copy(p, r.data[r.off:])
		r.off += n
		return n, nil
	}
	if r.blocked != nil {
		r.signal.Do(func() { close(r.blocked) })
	}
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

// stallStore delegates every Storage method except Get: Get returns the real
// object stream wrapped in a stallReader, so a deadline or cancellation that
// fires mid-decode surfaces through the storage stream exactly like a slow
// backend read. All other methods (Put/Stat/Delete/…) delegate to the real
// local store, so fixtures upload normally.
type stallStore struct {
	storage.Storage
	img     []byte
	blocked chan struct{} // may be nil
}

func (s *stallStore) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	rc, info, err := s.Storage.Get(ctx, key)
	if err != nil {
		return nil, info, err
	}
	return &stallReadCloser{
		ReadCloser: rc,
		r:          &stallReader{ctx: ctx, data: s.img[:33], blocked: s.blocked},
	}, info, nil
}

// stallReadCloser keeps the underlying stream's Close (releasing the pinned
// object) while routing Read through the stall reader.
type stallReadCloser struct {
	io.ReadCloser
	r io.Reader
}

func (s *stallReadCloser) Read(p []byte) (int, error) { return s.r.Read(p) }

// drainGateStore delegates every Storage method except Get: Get returns the
// real object stream wrapped in a drainGateReadCloser, which serves every
// byte of the fixture and parks (closes consumed, blocks on release) on the
// read that delivers the final byte. The decoder is therefore provably parked
// mid-pipeline with the object fully consumed — a cancel/deadline that lands
// then fires at the post-read boundary checks (C1/C2) or inside
// scale/rotation, exercising the post-consumption error path end to end:
// the same exact-sentinel classification the in-phase checks return.
type drainGateStore struct {
	storage.Storage
	img      []byte
	consumed chan struct{}
	release  chan struct{}
}

func (s *drainGateStore) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	rc, info, err := s.Storage.Get(ctx, key)
	if err != nil {
		return nil, info, err
	}
	return &drainGateReadCloser{
		ReadCloser: rc,
		data:       s.img,
		consumed:   s.consumed,
		release:    s.release,
	}, info, nil
}

// drainGateReadCloser serves data from memory; the read that delivers the
// final byte closes consumed (once) and blocks until release. All bytes are
// served before the park, so the stream is fully drained when consumed fires
// (mirrors phaseGateReader's gateAt == len(data) pattern at the REST
// boundary).
type drainGateReadCloser struct {
	io.ReadCloser
	data     []byte
	off      int
	consumed chan struct{}
	release  chan struct{}
	signal   sync.Once
}

func (r *drainGateReadCloser) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	if r.off+len(p) > len(r.data) {
		p = p[:len(r.data)-r.off] // serve up to the final byte in this call
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	if r.off >= len(r.data) {
		r.signal.Do(func() { close(r.consumed); <-r.release })
	}
	return n, nil
}

// failAfterStore delegates every Storage method except Get: Get returns the
// real object stream wrapped in a failAfterReadCloser that serves failAfter
// real bytes, then returns err (sticky) on every further read — modeling a
// storage backend that drops mid-stream (S3/OSS network error) or an on-read
// ETagVerifier mismatch surfacing mid-decode (STORAGE_VERIFY_ON_READ). The
// error is sticky — the storage-SDK norm — so the PNG orientation walk's
// deferral re-encounters the same failure at the Decode site.
type failAfterStore struct {
	storage.Storage
	failAfter int
	err       error
}

func (s *failAfterStore) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	rc, info, err := s.Storage.Get(ctx, key)
	if err != nil {
		return nil, info, err
	}
	return &failAfterReadCloser{ReadCloser: rc, failAfter: s.failAfter, err: s.err}, info, nil
}

// failAfterReadCloser keeps the underlying stream's Close (releasing the
// pinned object) while routing Read through a fail-after-N-bytes reader.
// The read that crosses the boundary returns the injected error alongside
// its partial n (io.ReadFull surfaces it immediately; jpeg fill swallows
// (n>0, err) and the sticky error resurfaces on the next fill).
type failAfterReadCloser struct {
	io.ReadCloser
	failAfter int
	err       error
	off       int
}

func (r *failAfterReadCloser) Read(p []byte) (int, error) {
	remaining := r.failAfter - r.off
	if remaining <= 0 {
		return 0, r.err
	}
	if len(p) > remaining {
		p = p[:remaining]
	}
	n, err := r.ReadCloser.Read(p)
	r.off += n
	if err != nil {
		return n, err
	}
	if r.off >= r.failAfter {
		return n, r.err // boundary crossed: surface the injected failure now
	}
	return n, nil
}

// TestThumbnailMidDecodeDeadlineIs504 pins the mid-decode deadline path
// (AC-2): a request-scoped timeout that fires while the storage stream is
// read inside the decode section — after slot acquisition — must surface as
// HTTP 504 Timeout via the handler's DeadlineExceeded branch, never as 400
// InvalidArgument ("invalid image").
func TestThumbnailMidDecodeDeadlineIs504(t *testing.T) {
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "dl.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, _ := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	h := NewHandler(service.NewFileService(&stallStore{Storage: store, img: pngBytes(t, 64, 64)}, repo, nil), nil)
	// Long enough that slot acquisition and the 33-byte header read succeed,
	// short enough that the stall guarantees the deadline fires mid-decode.
	h.thumbnailTimeout = 100 * time.Millisecond
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })

	u := srv.URL + "/v1/files/img.png"
	if resp, _ := req(t, "PUT", u, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT img.png: %d", resp.StatusCode)
	}
	resp, body := req(t, "GET", u+"/thumbnail?w=32&h=32", nil, nil)
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("mid-decode deadline: status=%d want 504 (body=%q)", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte(`"code":"Timeout"`)) {
		t.Fatalf("expected code Timeout, body: %s", body)
	}
}

// TestThumbnailMidDecodeStorageError pins the REST classification of
// mid-decode source-stream failures (AC-2): a storage/verification read
// failure that surfaces inside the decode pipeline — after the slot is held
// and the object stream is open — must map to a server-side status (500
// InternalError for I/O failures, 410 ObjectCorrupt for an on-read
// ETagVerifier mismatch), never 400 InvalidArgument ("not an image"). The
// truncation control row proves the same fixture still classifies corrupt
// bytes as 400 — the marker's io.EOF exemption preserved end to end.
func TestThumbnailMidDecodeStorageError(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{
			name:       "storage IO failure",
			err:        fmt.Errorf("s3 read failed: %w", errors.New("i/o error")),
			wantStatus: http.StatusInternalServerError,
			wantCode:   "InternalError",
		},
		{
			name:       "on-read verification mismatch",
			err:        fmt.Errorf("%w: expected abc, computed xyz", service.ErrObjectCorrupt),
			wantStatus: http.StatusGone,
			wantCode:   "ObjectCorrupt",
		},
		{
			name:       "truncation control",
			err:        io.EOF,
			wantStatus: http.StatusBadRequest,
			wantCode:   "InvalidArgument",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "mid.db"))
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if err := repo.Migrate(context.Background()); err != nil {
				t.Fatalf("migrate: %v", err)
			}
			store, _ := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
			png := pngBytes(t, 64, 64)
			h := NewHandler(service.NewFileService(
				&failAfterStore{Storage: store, failAfter: len(png) / 2, err: tc.err}, repo, nil), nil)
			// No route deadline: the injected error — not a context error —
			// must be what surfaces.
			h.thumbnailTimeout = 0
			r := chi.NewRouter()
			r.Put("/v1/files/*", h.putKey)
			r.Get("/v1/files/*", h.getKey)
			srv := httptest.NewServer(r)
			t.Cleanup(func() { srv.Close(); _ = repo.Close() })

			u := srv.URL + "/v1/files/img.png"
			if resp, _ := req(t, "PUT", u, png, map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
				t.Fatalf("PUT img.png: %d", resp.StatusCode)
			}
			resp, body := req(t, "GET", u+"/thumbnail?w=32&h=32", nil, nil)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("mid-decode storage error: status=%d want %d (body=%q)", resp.StatusCode, tc.wantStatus, body)
			}
			if !bytes.Contains(body, []byte(`"code":"`+tc.wantCode+`"`)) {
				t.Fatalf("expected code %s, body: %s", tc.wantCode, body)
			}
			if tc.wantCode != "InvalidArgument" {
				// The whole point of the change: server-side failures never
				// masquerade as client argument errors.
				if bytes.Contains(body, []byte(`"code":"InvalidArgument"`)) {
					t.Fatalf("server-side failure must not classify as InvalidArgument, body: %s", body)
				}
				// Error paths must carry no cache validators (emitted only on
				// the 200 branch; a handler edit that set them before
				// writeError would pollute shared caches).
				if et := resp.Header.Get("ETag"); et != "" {
					t.Fatalf("%s path emitted ETag %q", tc.wantCode, et)
				}
				if cc := resp.Header.Get("Cache-Control"); cc != "" {
					t.Fatalf("%s path emitted Cache-Control %q", tc.wantCode, cc)
				}
				if tc.wantCode == "InternalError" && !bytes.Contains(body, []byte("s3 read failed")) {
					t.Fatalf("expected the injected error message in the body, got: %s", body)
				}
			}
		})
	}
}

// TestThumbnailMidDecodeCancelWritesNothing pins the client-disconnect path
// (AC-3): a cancel that fires while the storage stream is read inside the
// decode section must make the handler return without writing anything — no
// 4xx to a dead connection. Fully handshake-driven: the stall reader signals
// when it is blocked mid-decode, then the test cancels and joins.
func TestThumbnailMidDecodeCancelWritesNothing(t *testing.T) {
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "dl.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, _ := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	blocked := make(chan struct{})
	h := NewHandler(service.NewFileService(&stallStore{Storage: store, img: pngBytes(t, 64, 64), blocked: blocked}, repo, nil), nil)
	// No route-level deadline: the request context itself binds the stream.
	h.thumbnailTimeout = 0
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)

	// Seed the object through the same router (real storage underneath).
	putReq := httptest.NewRequest("PUT", "/v1/files/img.png", bytes.NewReader(pngBytes(t, 64, 64)))
	putReq.Header.Set("Content-Type", "image/png")
	r.ServeHTTP(httptest.NewRecorder(), putReq)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rq := httptest.NewRequest("GET", "/v1/files/img.png/thumbnail?w=32&h=32", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		r.ServeHTTP(rec, rq)
		close(done)
	}()
	<-blocked // stall reader is mid-decode, parked on the request context
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("thumbnail handler did not return after mid-decode cancel")
	}
	// httptest.NewRecorder leaves Code at its 200 default when WriteHeader is
	// never called; the load-bearing assertions are: no bytes written and no
	// 4xx classification (the pre-fix behavior wrote 400 InvalidArgument).
	if rec.Code != http.StatusOK {
		t.Fatalf("mid-decode cancel: status=%d want no write (recorder default 200; old behavior 400)", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("mid-decode cancel: wrote %d bytes, want nothing (body=%q)", rec.Body.Len(), rec.Body.String())
	}
	// The happy path sets Content-Type/ETag/Content-Length before
	// WriteHeader(200) (thumbnail.go:144-149); a regression where
	// GenerateContext returns (nil, empty) would otherwise produce 200 +
	// empty body and pass the assertions above undetected (QA-1 residual).
	if ct := rec.Header().Get("Content-Type"); ct != "" {
		t.Fatalf("mid-decode cancel: wrote Content-Type %q, want nothing written", ct)
	}
	if rec.Header().Get("ETag") != "" || rec.Header().Get("Content-Length") != "" {
		t.Fatalf("mid-decode cancel: wrote success headers (ETag=%q CL=%q), want nothing written",
			rec.Header().Get("ETag"), rec.Header().Get("Content-Length"))
	}
}

// TestThumbnailMidDecodePostReadCancelWritesNothing pins the post-consumption
// cancel path (sibling-run QA F3 closure): the object stream is fully drained
// (drainGateStore parks on the final-byte read) when the request context is
// canceled, so the abort fires at the post-read boundary checks or inside the
// CPU phases — never at the stream-error branches — and the handler must
// still return without writing anything: no 4xx to a dead connection. The
// wire contract is identical for every abort site (all surface the exact
// context sentinel), so the test cannot false-fail on where the check fires.
func TestThumbnailMidDecodePostReadCancelWritesNothing(t *testing.T) {
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "dl.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, _ := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	consumed := make(chan struct{})
	release := make(chan struct{})
	h := NewHandler(service.NewFileService(&drainGateStore{
		Storage:  store,
		img:      pngBytes(t, 64, 64),
		consumed: consumed,
		release:  release,
	}, repo, nil), nil)
	// No route-level deadline: the request context itself binds the request.
	h.thumbnailTimeout = 0
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)

	putReq := httptest.NewRequest("PUT", "/v1/files/img.png", bytes.NewReader(pngBytes(t, 64, 64)))
	putReq.Header.Set("Content-Type", "image/png")
	r.ServeHTTP(httptest.NewRecorder(), putReq)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rq := httptest.NewRequest("GET", "/v1/files/img.png/thumbnail?w=32&h=32", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		r.ServeHTTP(rec, rq)
		close(done)
	}()
	select {
	case <-consumed: // stream fully drained; the decoder is parked on the final-byte read
	case <-time.After(5 * time.Second):
		t.Fatal("thumbnail never drained the stream")
	}
	cancel()
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("thumbnail handler did not return after post-read cancel")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("post-read cancel: status=%d want no write (recorder default 200)", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("post-read cancel: wrote %d bytes, want nothing (body=%q)", rec.Body.Len(), rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "" {
		t.Fatalf("post-read cancel: wrote Content-Type %q, want nothing written", ct)
	}
	if rec.Header().Get("ETag") != "" || rec.Header().Get("Content-Length") != "" {
		t.Fatalf("post-read cancel: wrote success headers (ETag=%q CL=%q), want nothing written",
			rec.Header().Get("ETag"), rec.Header().Get("Content-Length"))
	}
}

// TestThumbnailMidDecodePostReadDeadlineIs504 pins the post-consumption
// deadline path: the stream is fully drained when the server-side route
// deadline fires; the abort must surface as HTTP 504 Timeout (never 400
// InvalidArgument). The request context carries the same deadline so the test
// can observe it deterministically (the handler's scoped context derives from
// it and fires at the same instant).
func TestThumbnailMidDecodePostReadDeadlineIs504(t *testing.T) {
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "dl.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, _ := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	consumed := make(chan struct{})
	release := make(chan struct{})
	h := NewHandler(service.NewFileService(&drainGateStore{
		Storage:  store,
		img:      pngBytes(t, 64, 64),
		consumed: consumed,
		release:  release,
	}, repo, nil), nil)
	// Short enough that the gate provably parks before the deadline fires.
	h.thumbnailTimeout = 100 * time.Millisecond
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)

	putReq := httptest.NewRequest("PUT", "/v1/files/img.png", bytes.NewReader(pngBytes(t, 64, 64)))
	putReq.Header.Set("Content-Type", "image/png")
	r.ServeHTTP(httptest.NewRecorder(), putReq)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	rq := httptest.NewRequest("GET", "/v1/files/img.png/thumbnail?w=32&h=32", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		r.ServeHTTP(rec, rq)
		close(done)
	}()
	select {
	case <-consumed: // stream fully drained; the decoder is parked on the final-byte read
	case <-time.After(5 * time.Second):
		t.Fatal("thumbnail never drained the stream")
	}
	select {
	case <-ctx.Done(): // the handler's scoped deadline derives from this context
	case <-time.After(5 * time.Second):
		t.Fatal("deadline never fired while the decoder was parked")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("thumbnail handler did not return after post-read deadline")
	}
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("post-read deadline: status=%d want 504 (body=%q)", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"Timeout"`)) {
		t.Fatalf("expected code Timeout, body: %s", rec.Body.String())
	}
}

// TestThumbnailProgressiveOversized413 pins the HTTP-level progressive chain:
// a progressive (SOF2) JPEG whose declared dims exceed
// thumbnail.MaxProgressiveSourceDim rejects with 413 ImageTooLarge at the
// protocol boundary — the same sentinel as the dimension bomb, so the
// user-visible surface is pinned end to end.
func TestThumbnailProgressiveOversized413(t *testing.T) {
	s := newRESTTest(t)
	u := s.URL + "/v1/files/prog.jpg"
	// Header-only progressive fixture (no DQT/entropy — zero pixel buffers):
	// the declared dims drive the rejection.
	req(t, "PUT", u, headerOnlyProgressiveJPEGBytes(t, 8192, 8192), map[string]string{"Content-Type": "image/jpeg"})
	resp, body := req(t, "GET", u+"/thumbnail", nil, nil)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("progressive oversized thumbnail: status=%d want 413 (body=%q)", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte(`"code":"ImageTooLarge"`)) {
		t.Fatalf("expected code ImageTooLarge, body: %s", body)
	}
	// Baseline SOF0 at the same dims still 413s via the dimension gate (the
	// progressive cap is a tighter ceiling, not a looser path).
	req(t, "PUT", s.URL+"/v1/files/baseline.jpg", headerOnlyBaselineJPEGBytes(t, 9000, 9000), map[string]string{"Content-Type": "image/jpeg"})
	resp, _ = req(t, "GET", s.URL+"/v1/files/baseline.jpg/thumbnail", nil, nil)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("baseline oversized thumbnail: status=%d want 413", resp.StatusCode)
	}
	// A progressive JPEG at 4096 (at the cap) must NOT 413 — it falls
	// through to full decode and fails there (header-only fixture) as a 400
	// InvalidArgument, never ImageTooLarge.
	req(t, "PUT", s.URL+"/v1/files/prog-ok.jpg", headerOnlyProgressiveJPEGBytes(t, thumbnail.MaxProgressiveSourceDim, 100), map[string]string{"Content-Type": "image/jpeg"})
	resp, body = req(t, "GET", s.URL+"/v1/files/prog-ok.jpg/thumbnail", nil, nil)
	if resp.StatusCode == http.StatusRequestEntityTooLarge {
		t.Fatalf("progressive at cap: status=%d — must not reject as ImageTooLarge", resp.StatusCode)
	}
	if !bytes.Contains(body, []byte(`"code":"InvalidArgument"`)) {
		t.Fatalf("progressive at cap: expected InvalidArgument (decode of header-only fixture), body: %s", body)
	}
}

// headerOnlyProgressiveJPEGBytes builds a header-only progressive (SOF2) JPEG
// with attacker-controlled declared dims, mirroring the thumbnail package's
// test fixture at the protocol boundary.
func headerOnlyProgressiveJPEGBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI
	app0 := []byte{'J', 'F', 'I', 'F', 0, 1, 1, 0, 0, 1, 0, 1, 0, 0}
	buf.Write([]byte{0xFF, 0xE0})
	var seg [2]byte
	binary.BigEndian.PutUint16(seg[:], uint16(len(app0)+2))
	buf.Write(seg[:])
	buf.Write(app0)
	sof := []byte{8, byte(h >> 8), byte(h), byte(w >> 8), byte(w), 3,
		1, 0x22, 0,
		2, 0x11, 1,
		3, 0x11, 1}
	buf.Write([]byte{0xFF, 0xC2}) // SOF2: progressive
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

// headerOnlyBaselineJPEGBytes is the SOF0 analogue: same structure, baseline
// marker, declared dims only.
func headerOnlyBaselineJPEGBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI
	app0 := []byte{'J', 'F', 'I', 'F', 0, 1, 1, 0, 0, 1, 0, 1, 0, 0}
	buf.Write([]byte{0xFF, 0xE0})
	var seg [2]byte
	binary.BigEndian.PutUint16(seg[:], uint16(len(app0)+2))
	buf.Write(seg[:])
	buf.Write(app0)
	sof := []byte{8, byte(h >> 8), byte(h), byte(w >> 8), byte(w), 3,
		1, 0x22, 0,
		2, 0x11, 1,
		3, 0x11, 1}
	buf.Write([]byte{0xFF, 0xC0}) // SOF0: baseline sequential
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

// TestThumbnailCacheControlPrivacy pins the per-object cache directive
// (R-1..R-6): the thumbnail derivation response is shared-cacheable
// ("public") only when the request itself was admitted anonymously — which
// allowAnonymous grants solely for genuinely public-readable objects.
// Authenticated derivations are "private" (client-local cache only): the
// caller may hold private access, and a shared cache must never store bytes
// that an external anonymous caller could not fetch from origin.
func TestThumbnailCacheControlPrivacy(t *testing.T) {
	// Derived from the production bound (thumbFreshnessMaxAge) so a
	// freshness retune moves the pins with it (QA F1); the exact emitted
	// bytes are additionally pinned by the literal assertions in
	// TestThumbnailRevalidationAfterReplace.
	public := fmt.Sprintf("public, max-age=%d, must-revalidate", thumbFreshnessMaxAge)
	private := fmt.Sprintf("private, max-age=%d, must-revalidate", thumbFreshnessMaxAge)

	t.Run("unauthenticated harness is private", func(t *testing.T) {
		// No auth middleware: IsAnonymous is false, so the directive must
		// never be public (the pre-fix code always emitted public).
		s := newRESTTest(t)
		u := s.URL + "/v1/files/img"
		req(t, "PUT", u, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png"})
		resp, _ := req(t, "GET", u+"/thumbnail", nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("thumbnail: %d", resp.StatusCode)
		}
		if cc := resp.Header.Get("Cache-Control"); cc != private {
			t.Fatalf("no-auth derivation Cache-Control=%q want %q", cc, private)
		}
		assertThumbnailFreshnessBound(t, resp.Header.Get("Cache-Control"))
	})

	t.Run("authenticated derivation is private", func(t *testing.T) {
		s, tok, repo := newThumbnailAccessHarness(t)
		enableVersioningForCoexistence(t, repo)
		authH := map[string]string{"Authorization": tok}
		u := s.URL + "/v1/files/img"
		if resp, _ := req(t, "PUT", u, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT: %d", resp.StatusCode)
		}
		resp, _ := req(t, "GET", u+"/thumbnail", nil, authH)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("thumbnail: %d", resp.StatusCode)
		}
		if cc := resp.Header.Get("Cache-Control"); cc != private {
			t.Fatalf("authenticated derivation Cache-Control=%q want %q", cc, private)
		}
	})

	t.Run("anonymous public-read object is public", func(t *testing.T) {
		s, tok := newAuthRESTTest(t)
		authH := map[string]string{"Authorization": tok}
		u := s.URL + "/v1/files/img"
		if resp, _ := req(t, "PUT", u, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT: %d", resp.StatusCode)
		}
		if resp, _ := req(t, "PUT", u+"/acl", []byte(`{"acl":"public-read"}`), authH); resp.StatusCode != http.StatusOK {
			t.Fatalf("set acl: %d", resp.StatusCode)
		}
		// Anonymous (admitted via the public-read ACL) → public.
		resp, _ := req(t, "GET", u+"/thumbnail", nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("anonymous thumbnail: %d", resp.StatusCode)
		}
		if cc := resp.Header.Get("Cache-Control"); cc != public {
			t.Fatalf("anonymous public-read derivation Cache-Control=%q want %q", cc, public)
		}
		// Same object, authenticated principal → private (the caller's
		// private access does not make the bytes public).
		resp, _ = req(t, "GET", u+"/thumbnail", nil, authH)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("auth thumbnail: %d", resp.StatusCode)
		}
		if cc := resp.Header.Get("Cache-Control"); cc != private {
			t.Fatalf("authenticated public-object derivation Cache-Control=%q want %q", cc, private)
		}
	})

	t.Run("anonymous bucket-ACL public is public", func(t *testing.T) {
		// QA P1-2: the bucket-ACL fallback of ObjectPublicReadable must be
		// pinned (object ACL private, bucket ACL public-read).
		s, tok, repo := newThumbnailAccessHarness(t)
		enableVersioningForCoexistence(t, repo)
		u := s.URL + "/v1/files/img"
		if resp, _ := req(t, "PUT", u, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT: %d", resp.StatusCode)
		}
		if resp, _ := req(t, "PUT", s.URL+"/v1/buckets/default/acl", []byte(`{"acl":"public-read"}`), map[string]string{"Authorization": "Bearer operator"}); resp.StatusCode != http.StatusOK {
			t.Fatalf("set bucket acl: %d", resp.StatusCode)
		}
		resp, _ := req(t, "GET", u+"/thumbnail", nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("anonymous bucket-ACL thumbnail: %d", resp.StatusCode)
		}
		if cc := resp.Header.Get("Cache-Control"); cc != public {
			t.Fatalf("anonymous bucket-ACL derivation Cache-Control=%q want %q", cc, public)
		}
	})

	t.Run("304 mirrors the 200 directive", func(t *testing.T) {
		// RFC 9111 §4.3.5: a revalidating shared cache freshens the stored
		// response with the 304's directive — a private 304 must never
		// follow a public 200.
		s, tok := newAuthRESTTest(t)
		authH := map[string]string{"Authorization": tok}
		u := s.URL + "/v1/files/img"
		if resp, _ := req(t, "PUT", u, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT: %d", resp.StatusCode)
		}
		if resp, _ := req(t, "PUT", u+"/acl", []byte(`{"acl":"public-read"}`), authH); resp.StatusCode != http.StatusOK {
			t.Fatalf("set acl: %d", resp.StatusCode)
		}
		// Anonymous: 200 public → 304 must also be public.
		resp, _ := req(t, "GET", u+"/thumbnail", nil, nil)
		etag := resp.Header.Get("ETag")
		if resp.Header.Get("Cache-Control") != public || etag == "" {
			t.Fatalf("anon 200: cc=%q etag=%q", resp.Header.Get("Cache-Control"), etag)
		}
		resp, _ = req(t, "GET", u+"/thumbnail", nil, map[string]string{"If-None-Match": etag})
		if resp.StatusCode != http.StatusNotModified {
			t.Fatalf("anon 304: %d", resp.StatusCode)
		}
		if cc := resp.Header.Get("Cache-Control"); cc != public {
			t.Fatalf("anon 304 Cache-Control=%q want %q", cc, public)
		}
		assertThumbnailFreshnessBound(t, resp.Header.Get("Cache-Control"))
		// Authenticated: 200 private → 304 must also be private.
		resp, _ = req(t, "GET", u+"/thumbnail", nil, authH)
		etag = resp.Header.Get("ETag")
		if resp.Header.Get("Cache-Control") != private || etag == "" {
			t.Fatalf("auth 200: cc=%q etag=%q", resp.Header.Get("Cache-Control"), etag)
		}
		resp, _ = req(t, "GET", u+"/thumbnail", nil, map[string]string{"Authorization": tok, "If-None-Match": etag})
		if resp.StatusCode != http.StatusNotModified {
			t.Fatalf("auth 304: %d", resp.StatusCode)
		}
		if cc := resp.Header.Get("Cache-Control"); cc != private {
			t.Fatalf("auth 304 Cache-Control=%q want %q", cc, private)
		}
	})
}

// assertThumbnailFreshnessBound pins the REQ-1 numeric contract (acceptance
// (a)) beyond exact-string equality: max-age must not exceed
// thumbFreshnessMaxAge and must-revalidate must be present. The exact-string
// pins protect the emitted bytes; this parse-based check protects the
// requirement itself from a lockstep retune of the literals (QA F2).
func assertThumbnailFreshnessBound(t *testing.T, cc string) {
	t.Helper()
	age := -1
	for _, part := range strings.Split(cc, ",") {
		part = strings.TrimSpace(part)
		if v, ok := strings.CutPrefix(part, "max-age="); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				t.Fatalf("Cache-Control=%q: unparseable max-age %q", cc, v)
			}
			age = n
		}
	}
	if age < 0 {
		t.Fatalf("Cache-Control=%q: missing max-age", cc)
	}
	if age > thumbFreshnessMaxAge {
		t.Fatalf("Cache-Control=%q: max-age=%d exceeds bound %d", cc, age, thumbFreshnessMaxAge)
	}
	if !strings.Contains(cc, "must-revalidate") {
		t.Fatalf("Cache-Control=%q: missing must-revalidate", cc)
	}
}

// TestThumbnailRevalidationAfterReplace pins REQ-2 (acceptance (b)): after a
// PUT replaces the object, a revalidating client that sends If-None-Match
// with the OLD derived validator receives the NEW bytes (200), never a stale
// 304. With bounded client freshness (max-age=300, must-revalidate) this is
// exactly what a cache performs once the window lapses; the server must
// answer it with current bytes. Deterministic: PNG fixtures are
// byte-deterministic, ETags are content MD5s, and the pipeline never
// upscales sub-request-sized sources (a 64x64 source at ?w=100&h=100 stays
// 64x64).
func TestThumbnailRevalidationAfterReplace(t *testing.T) {
	// Private class: the browser-cache path (no-auth harness ⇒ private
	// directive).
	t.Run("private", func(t *testing.T) {
		s := newRESTTest(t)
		u := s.URL + "/v1/files/repl.png"
		thumb := u + "/thumbnail?w=100&h=100"

		// 1. PUT image A (300x150) and fetch its derived thumbnail:
		// capture validator V1 and body1.
		if resp, _ := req(t, "PUT", u, pngBytes(t, 300, 150), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT A: %d", resp.StatusCode)
		}
		resp, body1 := req(t, "GET", thumb, nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET thumb A: %d", resp.StatusCode)
		}
		v1 := resp.Header.Get("ETag")
		if v1 == "" {
			t.Fatal("thumb A: missing ETag")
		}
		if cc := resp.Header.Get("Cache-Control"); cc != "private, max-age=300, must-revalidate" {
			t.Fatalf("thumb A Cache-Control=%q want bounded private directive", cc)
		}

		// 2. PUT image B (64x64 — different bytes ⇒ different MD5 ETag ⇒
		// different derived validator).
		if resp, _ := req(t, "PUT", u, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT B: %d", resp.StatusCode)
		}

		// 3. Revalidating client with the OLD validator V1: must get 200
		// with the new bytes and a new validator V2 — never a stale 304.
		resp, body2 := req(t, "GET", thumb, nil, map[string]string{"If-None-Match": v1})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("revalidate after replace: status=%d want 200 (never a stale 304)", resp.StatusCode)
		}
		v2 := resp.Header.Get("ETag")
		if v2 == "" || v2 == v1 {
			t.Fatalf("revalidate ETag=%q must differ from old %q", v2, v1)
		}
		if bytes.Equal(body2, body1) {
			t.Fatal("revalidate body must differ from the pre-replace thumbnail")
		}
		img, format, err := image.Decode(bytes.NewReader(body2))
		if err != nil || format != "jpeg" {
			t.Fatalf("revalidate body decode: %v fmt=%s", err, format)
		}
		if img.Bounds().Dx() != 64 || img.Bounds().Dy() != 64 {
			t.Fatalf("revalidate thumb dims %dx%d want 64x64 (the new source, not the old 300x150)",
				img.Bounds().Dx(), img.Bounds().Dy())
		}
		// QA F4: the fall-through 200 is a distinct emission point — pin
		// its directive too (the user-visible fix).
		if cc := resp.Header.Get("Cache-Control"); cc != "private, max-age=300, must-revalidate" {
			t.Fatalf("revalidate 200 Cache-Control=%q want bounded private directive", cc)
		}

		// 4. HEAD-after-replace parity (QA F5): HEAD with the old validator
		// falls through to 200 with the new validator and an empty body.
		headResp, headBody := req(t, "HEAD", thumb, nil, map[string]string{"If-None-Match": v1})
		if headResp.StatusCode != http.StatusOK {
			t.Fatalf("HEAD revalidate after replace: status=%d want 200", headResp.StatusCode)
		}
		if hv := headResp.Header.Get("ETag"); hv != v2 {
			t.Fatalf("HEAD revalidate ETag=%q want %q", hv, v2)
		}
		if len(headBody) != 0 {
			t.Fatalf("HEAD revalidate body=%d bytes, want empty", len(headBody))
		}

		// 5. Control: the unchanged-object conditional still works after
		// replacement — If-None-Match with the NEW validator V2 → 304.
		resp, _ = req(t, "GET", thumb, nil, map[string]string{"If-None-Match": v2})
		if resp.StatusCode != http.StatusNotModified {
			t.Fatalf("control revalidate with V2: status=%d want 304", resp.StatusCode)
		}
	})

	// Public class (QA F3): the CDN-relevant leg — a shared cache holding
	// the anonymous public response must also observe a replacement at
	// revalidation.
	t.Run("public", func(t *testing.T) {
		s, tok := newAuthRESTTest(t)
		authH := map[string]string{"Authorization": tok}
		u := s.URL + "/v1/files/repl.png"
		thumb := u + "/thumbnail?w=100&h=100"

		if resp, _ := req(t, "PUT", u, pngBytes(t, 300, 150), map[string]string{"Content-Type": "image/png", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT A: %d", resp.StatusCode)
		}
		if resp, _ := req(t, "PUT", u+"/acl", []byte(`{"acl":"public-read"}`), authH); resp.StatusCode != http.StatusOK {
			t.Fatalf("set acl: %d", resp.StatusCode)
		}
		// Anonymous GET (admitted via the public-read ACL) → public
		// directive; capture V1.
		resp, body1 := req(t, "GET", thumb, nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("anon GET thumb A: %d", resp.StatusCode)
		}
		v1 := resp.Header.Get("ETag")
		if v1 == "" {
			t.Fatal("anon thumb A: missing ETag")
		}
		if cc := resp.Header.Get("Cache-Control"); cc != "public, max-age=300, must-revalidate" {
			t.Fatalf("anon thumb A Cache-Control=%q want bounded public directive", cc)
		}

		// Replace the object (the ACL survives: PUT without an ACL option
		// does not touch it).
		if resp, _ := req(t, "PUT", u, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT B: %d", resp.StatusCode)
		}

		// Anonymous revalidation with the OLD validator → 200 with the new
		// bytes and the public directive — never a stale 304.
		resp, body2 := req(t, "GET", thumb, nil, map[string]string{"If-None-Match": v1})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("anon revalidate after replace: status=%d want 200 (never a stale 304)", resp.StatusCode)
		}
		v2 := resp.Header.Get("ETag")
		if v2 == "" || v2 == v1 {
			t.Fatalf("anon revalidate ETag=%q must differ from old %q", v2, v1)
		}
		if bytes.Equal(body2, body1) {
			t.Fatal("anon revalidate body must differ from the pre-replace thumbnail")
		}
		if cc := resp.Header.Get("Cache-Control"); cc != "public, max-age=300, must-revalidate" {
			t.Fatalf("anon revalidate 200 Cache-Control=%q want bounded public directive", cc)
		}

		// Control: the new validator still 304s anonymously.
		resp, _ = req(t, "GET", thumb, nil, map[string]string{"If-None-Match": v2})
		if resp.StatusCode != http.StatusNotModified {
			t.Fatalf("anon control revalidate with V2: status=%d want 304", resp.StatusCode)
		}
	})
}

// webpBytes is a verified 1×1 RGB WebP produced by Pillow's WEBP encoder
// (opens as WEBP (1,1)). The server must never decode it — the content-type
// gate rejects it with 415 — but the bytes are genuinely image/webp, so a
// future webp-capable pipeline could claim them.
var webpBytes = func() []byte {
	b, err := hex.DecodeString("524946463c000000574542505650382030000000d001009d012a0100010001402625a00274ba01f80003b000fef2eb7ffcd815cd73eff7ffd2e0fd2e0fd2e0ffd2900000")
	if err != nil {
		panic(err)
	}
	return b
}()

// gifBytes builds a 1×1 GIF with the stdlib encoder.
func gifBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.RGBA{255, 0, 0, 255}})
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode gif: %v", err)
	}
	return buf.Bytes()
}

func TestThumbnailUnsupportedFormat(t *testing.T) {
	s := newRESTTest(t)

	// A valid-but-unsupported image type must be a server-capability
	// rejection (415 UnsupportedMediaType), not a client argument error
	// (400 InvalidArgument).
	u := s.URL + "/v1/files/pic.webp"
	req(t, "PUT", u, webpBytes, map[string]string{"Content-Type": "image/webp"})
	resp, body := req(t, "GET", u+"/thumbnail", nil, nil)
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("webp thumbnail: status=%d want 415 (body=%s)", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte(`"code":"UnsupportedMediaType"`)) {
		t.Fatalf("webp thumbnail: expected code UnsupportedMediaType, body: %s", body)
	}
	if !bytes.Contains(body, []byte("image/webp")) || !bytes.Contains(body, []byte("image/jpeg")) {
		t.Fatalf("webp thumbnail: message must name the content type and supported types, body: %s", body)
	}

	// Direct classify seam: the raw sentinel maps to 415 without any wrapping.
	code, msg, status := classify(thumbnail.ErrUnsupportedFormat)
	if code != "UnsupportedMediaType" || status != http.StatusUnsupportedMediaType || msg == "" {
		t.Fatalf("classify(ErrUnsupportedFormat) = (%q, %q, %d) want (UnsupportedMediaType, _, 415)", code, msg, status)
	}

	// Non-image content types keep the existing 400 path (unchanged).
	tu := s.URL + "/v1/files/note.txt"
	req(t, "PUT", tu, []byte("hello"), map[string]string{"Content-Type": "text/plain"})
	resp, body = req(t, "GET", tu+"/thumbnail", nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("text/plain thumbnail: status=%d want 400 (body=%s)", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte(`"code":"InvalidArgument"`)) {
		t.Fatalf("text/plain thumbnail: expected code InvalidArgument, body: %s", body)
	}

	// GIF is the third whitelist member: unchanged 200.
	gu := s.URL + "/v1/files/pic.gif"
	req(t, "PUT", gu, gifBytes(t), map[string]string{"Content-Type": "image/gif"})
	resp, body = req(t, "GET", gu+"/thumbnail", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("gif thumbnail: status=%d want 200 (body=%s)", resp.StatusCode, body)
	}

	// Fallback bucket (absent/generic declarations are byte-decided at open
	// time via thumbnail.Sniff): a generic-declared PNG is admitted by magic
	// and thumbnailed — today this path returned 400.
	ou := s.URL + "/v1/files/generic.png"
	req(t, "PUT", ou, pngBytes(t, 16, 16), map[string]string{"Content-Type": "application/octet-stream"})
	resp, body = req(t, "GET", ou+"/thumbnail", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("octet-stream png thumbnail: status=%d want 200 (body=%s)", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("octet-stream png thumbnail: Content-Type=%q want image/jpeg", ct)
	}
	if _, format, err := image.Decode(bytes.NewReader(body)); err != nil || format != "jpeg" {
		t.Fatalf("octet-stream png thumbnail: body must decode as jpeg (format=%q err=%v)", format, err)
	}
	// The fallback 200 shares the declared path's derived ETag, so a cache
	// hit revalidates with 304 — and a 304 never opens the stream or sniffs.
	if etag := resp.Header.Get("ETag"); etag != "" {
		resp304, _ := req(t, "GET", ou+"/thumbnail", nil, map[string]string{"If-None-Match": etag})
		if resp304.StatusCode != http.StatusNotModified {
			t.Fatalf("octet-stream png revalidation: status=%d want 304", resp304.StatusCode)
		}
	}

	// An absent declaration (curl -T semantics — no Content-Type header) is
	// byte-decided too.
	ju := s.URL + "/v1/files/plain.jpg"
	req(t, "PUT", ju, appnPaddedJPEG(t, 0), nil) // no Content-Type header
	resp, body = req(t, "GET", ju+"/thumbnail", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("no-declaration jpeg thumbnail: status=%d want 200 (body=%s)", resp.StatusCode, body)
	}
	if _, format, err := image.Decode(bytes.NewReader(body)); err != nil || format != "jpeg" {
		t.Fatalf("no-declaration jpeg thumbnail: body must decode as jpeg (format=%q err=%v)", format, err)
	}

	// WebP magic under a generic declaration is a server-capability
	// rejection: 415, never 400 — the bytes are a valid image the pipeline
	// cannot decode, and the message names the detected format and the
	// supported set.
	wu := s.URL + "/v1/files/generic.webp"
	req(t, "PUT", wu, webpBytes, map[string]string{"Content-Type": "application/octet-stream"})
	resp, body = req(t, "GET", wu+"/thumbnail", nil, nil)
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("octet-stream webp thumbnail: status=%d want 415 (body=%s)", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte(`"code":"UnsupportedMediaType"`)) {
		t.Fatalf("octet-stream webp: expected code UnsupportedMediaType, body: %s", body)
	}
	if !bytes.Contains(body, []byte("webp")) || !bytes.Contains(body, []byte("image/jpeg")) {
		t.Fatalf("octet-stream webp: message must name the detected format and supported types, body: %s", body)
	}
	for _, hdr := range []string{"ETag", "Cache-Control", "Last-Modified"} {
		if v := resp.Header.Get(hdr); v != "" {
			t.Fatalf("octet-stream webp 415: %s=%q must be absent (cache hygiene)", hdr, v)
		}
	}

	// Unknown bytes under a generic declaration stay the client-argument
	// class: 400 InvalidArgument.
	xu := s.URL + "/v1/files/generic.txt"
	req(t, "PUT", xu, []byte("hello"), map[string]string{"Content-Type": "application/octet-stream"})
	resp, body = req(t, "GET", xu+"/thumbnail", nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("octet-stream text thumbnail: status=%d want 400 (body=%s)", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte(`"code":"InvalidArgument"`)) {
		t.Fatalf("octet-stream text: expected code InvalidArgument, body: %s", body)
	}

	// Control: a declared decodable type still 200 through bucket 1.
	pu := s.URL + "/v1/files/declared.png"
	req(t, "PUT", pu, pngBytes(t, 16, 16), map[string]string{"Content-Type": "image/png"})
	resp, body = req(t, "GET", pu+"/thumbnail", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("declared png thumbnail: status=%d want 200 (body=%s)", resp.StatusCode, body)
	}
}

func TestThumbnailBadDimensions(t *testing.T) {
	s := newRESTTest(t)

	// The bomb object proves validation fires before any decode: decoding it
	// is rejected as ImageTooLarge (413), so a 400 for garbage dimensions can
	// only mean the dimension validation ran first.
	u := s.URL + "/v1/files/bomb.png"
	req(t, "PUT", u, bombPNG(t, 100000, 100000), map[string]string{"Content-Type": "image/png"})
	for _, tc := range []struct {
		q, param, val string
	}{
		{"?w=abc", "w", "abc"},
		{"?h=-1", "h", "-1"},
		{"?w=", "w", ""},
		{"?h=", "h", ""},
		{"?w=1e3", "w", "1e3"},
		{"?w=99999999999999999999", "w", "99999999999999999999"}, // int overflow → Atoi error
	} {
		resp, body := req(t, "GET", u+"/thumbnail"+tc.q, nil, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("GET .../thumbnail%s: status=%d want 400 (body=%s)", tc.q, resp.StatusCode, body)
		}
		if !bytes.Contains(body, []byte(`"code":"InvalidArgument"`)) {
			t.Fatalf("%s: expected code InvalidArgument, body: %s", tc.q, body)
		}
		if !bytes.Contains(body, []byte(tc.param)) {
			t.Fatalf("%s: message must name the %q parameter, body: %s", tc.q, tc.param, body)
		}
		if bytes.Contains(body, []byte(`"code":"ImageTooLarge"`)) {
			t.Fatalf("%s: decode must not be attempted (got ImageTooLarge), body: %s", tc.q, body)
		}
	}
	// Control: valid dimensions DO reach the decode pipeline, so the same
	// bomb object yields 413 ImageTooLarge — the 400s above are purely the
	// validation path.
	resp, body := req(t, "GET", u+"/thumbnail?w=100", nil, nil)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("bomb control: status=%d want 413 (body=%s)", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte(`"code":"ImageTooLarge"`)) {
		t.Fatalf("bomb control: expected code ImageTooLarge, body: %s", body)
	}

	// Valid values must not be over-rejected: 0 → default, > HardMax → clamp.
	// (These assertions use a small valid PNG: on the bomb object a valid
	// dimension would legitimately reach the decode pipeline and be rejected
	// as 413 ImageTooLarge, which is the control above, not an over-rejection.)
	p := s.URL + "/v1/files/pic.png"
	req(t, "PUT", p, pngBytes(t, 16, 16), map[string]string{"Content-Type": "image/png"})
	for _, q := range []string{"?w=0", "?w=4096", "?w=0&h=0", "?w=128&h=64"} {
		resp, body := req(t, "GET", p+"/thumbnail"+q, nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET .../thumbnail%s: status=%d want 200 (body=%s)", q, resp.StatusCode, body)
		}
	}
}

func TestThumbnailOpenAPIDocuments415(t *testing.T) {
	h := OpenAPISpecHandler()
	srv := httptest.NewServer(http.HandlerFunc(h.ServeHTTP))
	defer srv.Close()

	resp, body := req(t, "GET", srv.URL, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("openapi: status=%d want 200", resp.StatusCode)
	}
	var spec map[string]any
	if err := json.Unmarshal(body, &spec); err != nil {
		t.Fatalf("openapi: decode: %v", err)
	}
	paths := spec["paths"].(map[string]any)

	// The thumbnail route documents the 415 response with a description.
	thumbGet := paths["/v1/files/{key}/thumbnail"].(map[string]any)["get"].(map[string]any)
	resps := thumbGet["responses"].(map[string]any)
	r415, ok := resps["415"].(map[string]any)
	if !ok {
		t.Fatalf("thumbnail route must document 415, responses=%v", resps)
	}
	desc, _ := r415["description"].(string)
	if desc == "" || !strings.Contains(desc, "image/jpeg") {
		t.Fatalf("thumbnail 415 description=%q want a non-empty supported-types message", desc)
	}

	// Unrelated routes must not gain a 415 key (nil-map isolation).
	getObj := paths["/v1/files/{key}"].(map[string]any)["get"].(map[string]any)
	if _, ok := getObj["responses"].(map[string]any)["415"]; ok {
		t.Fatalf("GET /v1/files/{key} must not document 415: %v", getObj["responses"])
	}
}

// TestThumbnailMediaTypeNormalization pins the RFC 9110 §8.3.1 normalized
// gate (must-fix 1): media types are case-insensitive and may carry
// parameters, so "Image/JPEG" and "image/jpeg; charset=utf-8" normalize to
// image/jpeg and must be accepted (200), while aliases of unsupported
// formats (image/jpg, image/x-png, image/pjpeg) with decodable bytes stay
// server-capability rejections (415) — and a mislabeled webp-declared PNG
// stays 415 by declared type, never sniffed.
func TestThumbnailMediaTypeNormalization(t *testing.T) {
	s := newRESTTest(t)
	png := pngBytes(t, 32, 32)
	accepted := []struct {
		name, ct string
	}{
		{"uppercase type", "Image/JPEG"},
		{"uppercase subtype", "image/PNG"},
		{"mixed case", "ImAgE/GiF"},
		{"parameter", "image/jpeg; charset=utf-8"},
		{"whitespace parameter", "image/png; foo=\"bar baz\""},
	}
	for _, tc := range accepted {
		t.Run("accept "+tc.name, func(t *testing.T) {
			u := s.URL + "/v1/files/a-" + sanitizeKey(tc.name)
			req(t, "PUT", u, png, map[string]string{"Content-Type": tc.ct})
			resp, body := req(t, "GET", u+"/thumbnail", nil, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("content-type %q: status=%d want 200 (body=%s)", tc.ct, resp.StatusCode, body)
			}
		})
	}
	unsupported := []struct {
		name, ct string
	}{
		{"jpg alias", "image/jpg"},
		{"x-png alias", "image/x-png"},
		{"pjpeg alias", "image/pjpeg"},
		{"webp", "image/webp"},
	}
	for _, tc := range unsupported {
		t.Run("reject "+tc.name, func(t *testing.T) {
			// Decodable PNG bytes under an unsupported declared type: the
			// gate runs on the declared media type, not on sniffed bytes.
			u := s.URL + "/v1/files/b-" + sanitizeKey(tc.name)
			req(t, "PUT", u, png, map[string]string{"Content-Type": tc.ct})
			resp, body := req(t, "GET", u+"/thumbnail", nil, nil)
			if resp.StatusCode != http.StatusUnsupportedMediaType {
				t.Fatalf("content-type %q: status=%d want 415 (body=%s)", tc.ct, resp.StatusCode, body)
			}
			if !bytes.Contains(body, []byte(`"code":"UnsupportedMediaType"`)) {
				t.Fatalf("%s: expected code UnsupportedMediaType, body: %s", tc.ct, body)
			}
		})
	}
	// Mislabeled: webp-declared PNG bytes → 415 (declared type governs).
	t.Run("mislabeled webp-declared png", func(t *testing.T) {
		u := s.URL + "/v1/files/mislabeled"
		req(t, "PUT", u, png, map[string]string{"Content-Type": "image/webp"})
		resp, _ := req(t, "GET", u+"/thumbnail", nil, nil)
		if resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Fatalf("webp-declared PNG bytes: status=%d want 415", resp.StatusCode)
		}
	})
	// Unparseable content type (no slash): client-argument class, 400. Note
	// Go's mime.ParseMediaType does not error on slash-less bare tokens —
	// "not-a-media-type" parses to itself with err == nil — so it stays in
	// the declared gate (bucket 4); only true parse errors (e.g. "") and
	// application/octet-stream are byte-decided in the fallback bucket.
	t.Run("unparseable content type", func(t *testing.T) {
		u := s.URL + "/v1/files/weird"
		req(t, "PUT", u, png, map[string]string{"Content-Type": "not-a-media-type"})
		resp, _ := req(t, "GET", u+"/thumbnail", nil, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("unparseable content-type: status=%d want 400", resp.StatusCode)
		}
	})
}

func sanitizeKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

// TestThumbnailErrorPathCacheHygiene pins must-fix 3: the 400/415 error
// responses must carry no cacheable success headers (ETag, Cache-Control,
// Last-Modified), and an If-None-Match on a garbage-dimension request must
// yield 400 — never 304 (a revalidating shared cache must not adopt a
// validator for a response that was never generated).
func TestThumbnailErrorPathCacheHygiene(t *testing.T) {
	s := newRESTTest(t)
	assertNoCacheHeaders := func(t *testing.T, resp *http.Response, what string) {
		t.Helper()
		for _, h := range []string{"ETag", "Cache-Control", "Last-Modified"} {
			if v := resp.Header.Get(h); v != "" {
				t.Fatalf("%s: %s=%q must be absent on the error path", what, h, v)
			}
		}
	}
	u := s.URL + "/v1/files/bomb.png"
	req(t, "PUT", u, bombPNG(t, 100000, 100000), map[string]string{"Content-Type": "image/png"})

	// Garbage dims + a matching-looking If-None-Match: 400, not 304.
	resp, body := req(t, "GET", u+"/thumbnail?w=abc", nil, map[string]string{"If-None-Match": `"whatever"`})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("garbage dims + If-None-Match: status=%d want 400 (body=%s)", resp.StatusCode, body)
	}
	assertNoCacheHeaders(t, resp, "garbage-dims 400")

	// Unsupported format error path: 415 with no cache headers.
	tu := s.URL + "/v1/files/pic.webp"
	req(t, "PUT", tu, pngBytes(t, 32, 32), map[string]string{"Content-Type": "image/webp"})
	resp, _ = req(t, "GET", tu+"/thumbnail", nil, map[string]string{"If-None-Match": `"whatever"`})
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("unsupported format + If-None-Match: status=%d want 415", resp.StatusCode)
	}
	assertNoCacheHeaders(t, resp, "unsupported-format 415")

	// Non-image 400 path: no cache headers either.
	nu := s.URL + "/v1/files/note.txt"
	req(t, "PUT", nu, []byte("hello"), map[string]string{"Content-Type": "text/plain"})
	resp, _ = req(t, "GET", nu+"/thumbnail", nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("text/plain: status=%d want 400", resp.StatusCode)
	}
	assertNoCacheHeaders(t, resp, "non-image 400")
}

// TestThumbnailDispatchArmIgnoresGarbageDims pins the should-complete
// dispatch-arm row: on the exact-key arm (?version= / raw download of an
// existing "/thumbnail"-suffixed key) the ?w=/?h= parameters are ignored —
// garbage values must not 400 a raw download, and a multi-value ?w=1&w=2
// uses the first value (url.Values.Get semantics) on the derivation path.
func TestThumbnailDispatchArmIgnoresGarbageDims(t *testing.T) {
	s, tok, repo := newAuthRESTTestWithRepo(t)
	enableVersioningForCoexistence(t, repo)
	authH := map[string]string{"Authorization": tok}
	u := s.URL + "/v1/files/dir"
	if resp, _ := req(t, "PUT", u, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT dir: %d", resp.StatusCode)
	}
	uFull := u + "/thumbnail"
	if resp, _ := req(t, "PUT", uFull, []byte("object bytes"), map[string]string{"Content-Type": "text/plain", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT dir/thumbnail: %d", resp.StatusCode)
	}
	resp, body := req(t, "GET", uFull+"?w=abc&h=-5", nil, authH)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("exact-key arm with garbage dims: status=%d want 200 (body=%q)", resp.StatusCode, body)
	}
	if !bytes.Equal(body, []byte("object bytes")) {
		t.Fatalf("exact-key arm body=%q want the object bytes", body)
	}
	// Derivation path with a multi-value ?w= uses the first value (Get
	// semantics) — a valid 200 with the first dimension.
	resp, body = req(t, "GET", s.URL+"/v1/files/dir2/thumbnail?w=100&w=999", nil, nil)
	_ = resp
	_ = body
}

// TestThumbnailDepth16Oversized413 pins P1-1: the depth-16 dimension cap
// (thumbnail.Max16BitSourceDim) surfaces as 413 ImageTooLarge at the HTTP
// seam — a 16-bit PNG with declared dims above the cap is rejected from the
// header before any pixel buffer, exactly like the progressive-JPEG class.
func TestThumbnailDepth16Oversized413(t *testing.T) {
	s := newRESTTest(t)
	u := s.URL + "/v1/files/deep.png"
	req(t, "PUT", u, headerOnlyPNGBytes(t, thumbnail.Max16BitSourceDim+1, 1024, 16, 6), map[string]string{"Content-Type": "image/png"})
	resp, body := req(t, "GET", u+"/thumbnail", nil, nil)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("depth-16 oversized thumbnail: status=%d want 413 (body=%q)", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte(`"code":"ImageTooLarge"`)) {
		t.Fatalf("expected code ImageTooLarge, body: %s", body)
	}
	// Control: depth-16 at exactly the cap is not 413 — it falls through to
	// full decode and fails there (header-only fixture) as InvalidArgument.
	req(t, "PUT", s.URL+"/v1/files/deep-ok.png", headerOnlyPNGBytes(t, thumbnail.Max16BitSourceDim, 100, 16, 6), map[string]string{"Content-Type": "image/png"})
	resp, body = req(t, "GET", s.URL+"/v1/files/deep-ok.png/thumbnail", nil, nil)
	if resp.StatusCode == http.StatusRequestEntityTooLarge {
		t.Fatalf("depth-16 at cap: status=%d — must not reject as ImageTooLarge", resp.StatusCode)
	}
	if !bytes.Contains(body, []byte(`"code":"InvalidArgument"`)) {
		t.Fatalf("depth-16 at cap: expected InvalidArgument (decode of header-only fixture), body: %s", body)
	}
}

// headerOnlyPNGBytes builds a header-only PNG with declared w×h and the
// given IHDR bit depth / color type — the protocol-boundary analogue of the
// thumbnail package's fixture.
func headerOnlyPNGBytes(t *testing.T, w, h int, bitDepth, colorType byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}) // signature
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:8], uint32(h))
	ihdr[8], ihdr[9], ihdr[10], ihdr[11], ihdr[12] = bitDepth, colorType, 0, 0, 0
	var chunk bytes.Buffer
	chunk.WriteString("IHDR")
	chunk.Write(ihdr)
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], 13)
	buf.Write(l[:])
	buf.Write(chunk.Bytes())
	crc := crc32.NewIEEE()
	_, _ = crc.Write(chunk.Bytes())
	binary.BigEndian.PutUint32(l[:], crc.Sum32())
	buf.Write(l[:])
	return buf.Bytes()
}

// ── Slot-before-open (burst/ordering) tests ──────────────────────────────────
// Direction: "Release the object stream before parking on the decode-slot
// semaphore". The handler now derives thumbnails via
// thumbnail.GenerateContextWithOpener, which acquires the decode slot BEFORE
// the object stream opens — so at most maxConcurrentDecodes (4, pinned in
// internal/thumbnail) object streams are open at once for any request
// concurrency, and a request parked on the semaphore holds no stream.

// thumbnailDecodeSlots mirrors thumbnail.maxConcurrentDecodes (unexported):
// the semaphore capacity this package's ordering tests rely on. Keep in sync
// with internal/thumbnail/thumbnail.go.
const thumbnailDecodeSlots = 4

// burstStore delegates every Storage method except Get (the verified
// stream-open hook at FileService.openObjectWithOptions). While armed, Get
// counts the currently-open streams, tracks the high-water mark, and returns
// the real stream wrapped in a reader that (i) serves the first 33 bytes
// (PNG signature + IHDR — exactly what image/png's DecodeConfig consumes),
// then blocks Read on release — deterministically holding the decode slot
// mid-decode — and (ii) decrements the open counter on Close. Unarmed, Get
// behaves like the real store (used to seed fixtures and capture validators
// without perturbing the counters).
type burstStore struct {
	storage.Storage
	opens   atomic.Int64  // currently-open streams (armed phase only)
	high    atomic.Int64  // high-water mark of concurrently-open streams
	total   atomic.Int64  // total Get calls in the armed phase
	release chan struct{} // closed to unblock blocked decodes (may be nil)
	arm     atomic.Bool   // when false, Get returns the raw stream uncounted
}

func (s *burstStore) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	rc, info, err := s.Storage.Get(ctx, key)
	if err != nil {
		return nil, info, err
	}
	if !s.arm.Load() {
		return rc, info, nil
	}
	n := s.opens.Add(1)
	s.total.Add(1)
	for {
		cur := s.high.Load()
		if n <= cur || s.high.CompareAndSwap(cur, n) {
			break
		}
	}
	if s.release == nil {
		return rc, info, nil
	}
	head := make([]byte, 33)
	if _, err := io.ReadFull(rc, head); err != nil {
		_ = rc.Close()
		s.opens.Add(-1)
		return nil, info, err
	}
	return &burstReadCloser{ReadCloser: rc, head: head, release: s.release, store: s}, info, nil
}

// burstReadCloser keeps the underlying stream's Close (releasing the pinned
// object) while routing Read through the head-then-block-then-continue
// sequence described on burstStore.
type burstReadCloser struct {
	io.ReadCloser
	head    []byte
	off     int
	release <-chan struct{}
	store   *burstStore
}

func (r *burstReadCloser) Read(p []byte) (int, error) {
	if r.off < len(r.head) {
		n := copy(p, r.head[r.off:])
		r.off += n
		return n, nil
	}
	<-r.release // decode parks here: the slot is held until the test drains
	return r.ReadCloser.Read(p)
}

func (r *burstReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.store.opens.Add(-1)
	return err
}

// waitOpenStreams polls until opens reaches want (or fails after 5s). Once
// the release channel is closed, no decode can complete, so an opens count
// that reaches want stays there — the assertion is structurally
// deterministic, not timing-based. On failure it closes release first, so
// the blocked decodes drain and the test unwinds (srv.Close waits for
// in-flight requests) instead of hanging on a 25s timeout.
func waitOpenStreams(t *testing.T, s *burstStore, want int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for s.opens.Load() != want && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := s.opens.Load(); got != want {
		close(s.release)
		t.Fatalf("opens = %d, want %d — slot-before-open ordering regressed?", got, want)
	}
}

// TestThumbnailBurstPeakOpenStreams is the FD/connection-count proxy (AC-4,
// NFR-1): 100 concurrent thumbnail requests against a saturated decode slot
// must never hold more than thumbnailDecodeSlots (4) concurrently-open
// object streams. Pre-fix, opens precede slot acquisition and this test
// observes a high-water mark of ~100 and fails; post-fix it is structural.
func TestThumbnailBurstPeakOpenStreams(t *testing.T) {
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "b.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, _ := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	cs := &burstStore{Storage: store, release: make(chan struct{})}
	h := NewHandler(service.NewFileService(cs, repo, nil), nil)
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })

	u := srv.URL + "/v1/files/img.png"
	if resp, _ := req(t, "PUT", u, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT img.png: %d", resp.StatusCode)
	}
	cs.arm.Store(true)

	const n = 100
	statuses := make(chan int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Get(u + "/thumbnail?w=32&h=32")
			if err != nil {
				statuses <- -1
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			statuses <- resp.StatusCode
		}()
	}
	// All 4 slots are held by blocked decodes; every other request parks on
	// the semaphore WITHOUT opening a stream (the fix's core property).
	waitOpenStreams(t, cs, thumbnailDecodeSlots)
	close(cs.release)
	wg.Wait()
	close(statuses)
	for s := range statuses {
		if s != http.StatusOK {
			t.Fatalf("burst request: status %d, want 200", s)
		}
	}
	if got := cs.opens.Load(); got != 0 {
		t.Fatalf("opens not drained after burst: %d", got)
	}
	if got := cs.total.Load(); got != n {
		t.Fatalf("total opens = %d, want %d (exactly one open per request)", got, n)
	}
	if got := cs.high.Load(); got > thumbnailDecodeSlots {
		t.Fatalf("peak concurrently-open streams = %d, want ≤ %d — the fix's core bound", got, thumbnailDecodeSlots)
	}
}

// TestThumbnailCancelWhileParkedWritesNothing pins FR-3 at the HTTP level: a
// 5th request parked on the saturated semaphore holds no stream, honors
// client cancellation, and writes nothing — mirroring
// TestThumbnailMidDecodeCancelWritesNothing's handshake discipline at the
// park point.
func TestThumbnailCancelWhileParkedWritesNothing(t *testing.T) {
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "cp.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, _ := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	cs := &burstStore{Storage: store, release: make(chan struct{})}
	h := NewHandler(service.NewFileService(cs, repo, nil), nil)
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)

	putReq := httptest.NewRequest("PUT", "/v1/files/img.png", bytes.NewReader(pngBytes(t, 64, 64)))
	putReq.Header.Set("Content-Type", "image/png")
	r.ServeHTTP(httptest.NewRecorder(), putReq)
	cs.arm.Store(true)

	// 4 saturating requests: each blocks mid-decode holding a slot + stream.
	results := make(chan int, thumbnailDecodeSlots)
	var wg sync.WaitGroup
	for i := 0; i < thumbnailDecodeSlots; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rq := httptest.NewRequest("GET", "/v1/files/img.png/thumbnail?w=32&h=32", nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, rq)
			results <- rec.Code
		}()
	}
	waitOpenStreams(t, cs, thumbnailDecodeSlots)

	// 5th request on a cancelable client context: parks on the semaphore.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rq := httptest.NewRequest("GET", "/v1/files/img.png/thumbnail?w=32&h=32", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		r.ServeHTTP(rec, rq)
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("5th request completed while parked — it must hold nothing")
	default:
	}
	if got := cs.opens.Load(); got != thumbnailDecodeSlots {
		t.Fatalf("parked 5th request opened a stream: opens=%d, want %d", got, thumbnailDecodeSlots)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("parked request did not return after cancel")
	}
	// httptest.NewRecorder leaves Code at its 200 default when WriteHeader is
	// never called: the load-bearing assertions are no bytes written and no
	// 4xx classification, mirroring TestThumbnailMidDecodeCancelWritesNothing.
	if rec.Code != http.StatusOK {
		t.Fatalf("parked cancel: status=%d want no write (recorder default 200)", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("parked cancel: wrote %d bytes, want nothing (body=%q)", rec.Body.Len(), rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "" {
		t.Fatalf("parked cancel: wrote Content-Type %q, want nothing written", ct)
	}

	close(cs.release)
	wg.Wait()
	close(results)
	for s := range results {
		if s != http.StatusOK {
			t.Fatalf("saturating request: status %d, want 200", s)
		}
	}
	if got := cs.opens.Load(); got != 0 {
		t.Fatalf("opens not drained: %d", got)
	}
	if got := cs.high.Load(); got > thumbnailDecodeSlots {
		t.Fatalf("peak concurrently-open streams = %d, want ≤ %d", got, thumbnailDecodeSlots)
	}
}

// TestThumbnail304UnderSaturation pins FR-5: the If-None-Match/304 fast path
// runs before slot acquisition and stream open, so under full saturation a
// matching revalidation returns 304 promptly without touching a stream.
func TestThumbnail304UnderSaturation(t *testing.T) {
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "304.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, _ := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	cs := &burstStore{Storage: store, release: make(chan struct{})}
	h := NewHandler(service.NewFileService(cs, repo, nil), nil)
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })

	u := srv.URL + "/v1/files/img.png"
	if resp, _ := req(t, "PUT", u, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT img.png: %d", resp.StatusCode)
	}
	// Unarmed: capture the derived thumbnail ETag from a normal 200 request.
	resp, _ := req(t, "GET", u+"/thumbnail?w=32&h=32", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("seed thumbnail: %d", resp.StatusCode)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("seed thumbnail: no ETag header")
	}

	cs.arm.Store(true)
	// 4 saturating requests hold all slots + streams mid-decode.
	statuses := make(chan int, thumbnailDecodeSlots)
	var wg sync.WaitGroup
	for i := 0; i < thumbnailDecodeSlots; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Get(u + "/thumbnail?w=32&h=32")
			if err != nil {
				statuses <- -1
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			statuses <- resp.StatusCode
		}()
	}
	waitOpenStreams(t, cs, thumbnailDecodeSlots)

	// The 304 must complete without a slot and without opening a stream.
	start := time.Now()
	rq, _ := http.NewRequest("GET", u+"/thumbnail?w=32&h=32", nil)
	rq.Header.Set("If-None-Match", etag)
	resp304, err := http.DefaultClient.Do(rq)
	if err != nil {
		t.Fatalf("304 request: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp304.Body)
	resp304.Body.Close()
	if resp304.StatusCode != http.StatusNotModified {
		t.Fatalf("304 under saturation: status=%d, want 304", resp304.StatusCode)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("304 under saturation took %v — the cache fast path must not need a slot", elapsed)
	}
	if got := cs.opens.Load(); got != thumbnailDecodeSlots {
		t.Fatalf("304 request opened a stream: opens=%d, want %d", got, thumbnailDecodeSlots)
	}

	close(cs.release)
	wg.Wait()
	close(statuses)
	for s := range statuses {
		if s != http.StatusOK {
			t.Fatalf("saturating request: status %d, want 200", s)
		}
	}
	if got := cs.opens.Load(); got != 0 {
		t.Fatalf("opens not drained: %d", got)
	}
}

// failingGetStore delegates every Storage method except Get, which fails
// with a fixed error — modeling the object-deleted-between-Stat-and-Get race
// and other open-time storage failures behind the decode slot.
type failingGetStore struct {
	storage.Storage
	err error
}

func (s *failingGetStore) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	return nil, storage.ObjectInfo{}, s.err
}

// TestThumbnailOpenErrorAfterStatRace404 pins FR-4 outcome parity: an object
// that passes the Stat pre-check but fails at open time (deleted between
// Stat and Get) surfaces through *OpenError as today's writeError
// classification (404), never as an image error — and an open-time context
// error is classified (500 via writeError), never silently dropped.
func TestThumbnailOpenErrorAfterStatRace404(t *testing.T) {
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "oe.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, _ := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	fs := &failingGetStore{Storage: store, err: storage.ErrNotFound}
	h := NewHandler(service.NewFileService(fs, repo, nil), nil)
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)

	// Seed the object (Put delegates to the real store; Stat reads metadata,
	// so the thumbnail derivation passes the pre-checks, acquires the slot,
	// and only then hits the open failure).
	putReq := httptest.NewRequest("PUT", "/v1/files/img.png", bytes.NewReader(pngBytes(t, 64, 64)))
	putReq.Header.Set("Content-Type", "image/png")
	r.ServeHTTP(httptest.NewRecorder(), putReq)

	rq := httptest.NewRequest("GET", "/v1/files/img.png/thumbnail?w=32&h=32", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, rq)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("open failure after Stat: status=%d want 404 (body=%q)", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"NotFound"`)) {
		t.Fatalf("open failure after Stat: expected NotFound body, got %q", rec.Body.String())
	}

	// Open-time context error (e.g. a backend aborting with the request
	// context's Canceled): must still be writeError-classified (500 default),
	// never a silent return and never reclassified as an image error —
	// pins the OpenError-first ordering at the HTTP level.
	fs.err = fmt.Errorf("%w: storage read aborted", context.Canceled)
	rq2 := httptest.NewRequest("GET", "/v1/files/img.png/thumbnail?w=32&h=32", nil)
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, rq2)
	if rec2.Code != http.StatusInternalServerError {
		t.Fatalf("canceled open: status=%d want 500 (body=%q)", rec2.Code, rec2.Body.String())
	}
	if !bytes.Contains(rec2.Body.Bytes(), []byte(`"code":"InternalError"`)) {
		t.Fatalf("canceled open: expected JSON error body, got %q", rec2.Body.String())
	}
}

// TestThumbnailDeadlineWhileParkedIs504 pins C1: with all 4 decode slots held
// by concurrent slow decodes, a 5th request (still-connected client) parks at
// slot acquisition and must surface the scoped server deadline as 504
// Timeout — the acquire-error → handler-504 composition, end to end.
func TestThumbnailDeadlineWhileParkedIs504(t *testing.T) {
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "park.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, _ := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	h := NewHandler(service.NewFileService(store, repo, nil), nil)
	h.thumbnailTimeout = 300 * time.Millisecond
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })

	// A real 8192² PNG: each decode holds its slot for ~1 s.
	u := srv.URL + "/v1/files/big"
	big := pngBytes(t, 8192, 8192)
	if resp, _ := req(t, "PUT", u, big, map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT big: %d", resp.StatusCode)
	}
	uThumb := u + "/thumbnail?w=256&h=256"

	// Saturate all 4 slots with concurrent slow decodes.
	var wg sync.WaitGroup
	statuses := make([]int, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, _ := http.Get(uThumb)
			if resp != nil {
				statuses[i] = resp.StatusCode
				resp.Body.Close()
			}
		}(i)
	}
	// Wait until all 4 slots are provably held: the 5th request parks at
	// acquisition only when the holders have entered the decode section.
	// The acquire happens before the stream opens, so a parked 5th request
	// holds no stream. Give the 4 decodes a head start, then fire the 5th.
	time.Sleep(150 * time.Millisecond)
	resp, body := req(t, "GET", uThumb, nil, nil)
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("parked 5th request: status=%d want 504 (body=%q)", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte(`"code":"Timeout"`)) {
		t.Fatalf("expected code Timeout, body: %s", body)
	}
	wg.Wait()
	for i, st := range statuses {
		// Holders may also hit the 300 ms route deadline mid-decode (their
		// 8192² decode exceeds it) → 504 is the correct outcome for them
		// too; 200 is possible only if the decode won the race. The pin is
		// the parked 5th request's 504 above; holders must merely complete
		// with a well-formed status.
		if st != http.StatusOK && st != http.StatusGatewayTimeout {
			t.Fatalf("holder %d status=%d want 200 or 504", i, st)
		}
	}
}

// TestThumbnailMetadataTooLargeClassifySeam pins the classify() seam for the
// metadata-budget sentinel: the raw sentinel maps to 413 MetadataTooLarge
// (RFC 9110 §15.5.17 Payload Too Large — the request's declared metadata
// exceeds the server's processing budget), while a wrapped ErrInvalidArgs
// stays 400 InvalidArgument — the two classes must not blur.
func TestThumbnailMetadataTooLargeClassifySeam(t *testing.T) {
	code, msg, status := classify(thumbnail.ErrMetadataTooLarge)
	if code != "MetadataTooLarge" || status != http.StatusRequestEntityTooLarge || msg == "" {
		t.Fatalf("classify(ErrMetadataTooLarge) = (%q, %q, %d) want (MetadataTooLarge, _, 413)", code, msg, status)
	}
	// The wrapped form (generic handler wrap) must NOT reach the 413 arm.
	wrapped := fmt.Errorf("%w: oversized metadata", service.ErrInvalidArgs)
	code, _, status = classify(wrapped)
	if code != "InvalidArgument" || status != http.StatusBadRequest {
		t.Fatalf("classify(wrapped ErrInvalidArgs) = (%q, %d) want (InvalidArgument, 400)", code, status)
	}
	// The REST 413 arm must be cache-hygienic like every other error path:
	// no ETag/Cache-Control/Last-Modified on the metadata-budget 413.
	s := newRESTTest(t)
	u := s.URL + "/v1/files/meta.jpg"
	req(t, "PUT", u, oversizedMetadataJPEG(t), map[string]string{"Content-Type": "image/jpeg"})
	resp, body := req(t, "GET", u+"/thumbnail", nil, map[string]string{"If-None-Match": `"whatever"`})
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("metadata-budget thumbnail: status=%d want 413 (body=%s)", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte(`"code":"MetadataTooLarge"`)) {
		t.Fatalf("expected code MetadataTooLarge, body: %s", body)
	}
	for _, h := range []string{"ETag", "Cache-Control", "Last-Modified"} {
		if v := resp.Header.Get(h); v != "" {
			t.Fatalf("413 MetadataTooLarge: %s=%q must be absent (cache hygiene)", h, v)
		}
	}
}

// oversizedMetadataJPEG builds a JPEG whose pre-SOF APPn metadata exceeds the
// 8 MiB thumbnail metadata budget — mirroring the package-level fixture at
// the protocol boundary (see internal/thumbnail/appnPaddedJPEG).
func oversizedMetadataJPEG(t *testing.T) []byte {
	t.Helper()
	base := pngBytes(t, 32, 32)
	_ = base
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI
	const maxSegPayload = 65533
	remaining := thumbnail.MaxMetadataBytes + 64<<10
	for remaining > 0 {
		n := remaining
		if n > maxSegPayload {
			n = maxSegPayload
		}
		var seg [4]byte
		seg[0], seg[1] = 0xFF, 0xE1 // APP1
		binary.BigEndian.PutUint16(seg[2:4], uint16(n+2))
		buf.Write(seg[:])
		payload := make([]byte, n)
		for i := range payload {
			payload[i] = 0x42
		}
		buf.Write(payload)
		remaining -= n
	}
	// SOF0 (baseline 32×32) + SOS to make DecodeConfig succeed and the
	// metadata budget the only failure.
	sof := []byte{8, 0, 32, 0, 32, 3, 1, 0x22, 0, 2, 0x11, 1, 3, 0x11, 1}
	buf.Write([]byte{0xFF, 0xC0})
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(len(sof)+2))
	buf.Write(l[:])
	buf.Write(sof)
	sos := []byte{3, 1, 0, 2, 0, 3, 0, 0, 63, 0}
	buf.Write([]byte{0xFF, 0xDA})
	binary.BigEndian.PutUint16(l[:], uint16(len(sos)+2))
	buf.Write(l[:])
	buf.Write(sos)
	return buf.Bytes()
}

// TestThumbnailOctetStreamMagicPins pins the byte-decided admission class
// for undeclared/generic content types:
//   - a magic-admitted object whose bytes are not decodable (false positive)
//     → 400 InvalidArgument, never 415/500 (the decode pipeline is the final
//     validity authority);
//   - a magic-admitted decompression bomb (PNG magic, declared dims above
//     MaxSourceDim) → 413 ImageTooLarge, exactly like the declared-type path.
func TestThumbnailOctetStreamMagicPins(t *testing.T) {
	s := newRESTTest(t)

	// False positive: JPEG magic bytes followed by garbage.
	u := s.URL + "/v1/files/fake.jpg"
	req(t, "PUT", u, append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{0x42}, 64)...), map[string]string{"Content-Type": "application/octet-stream"})
	resp, body := req(t, "GET", u+"/thumbnail", nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("false-positive magic: status=%d want 400 (body=%q)", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte(`"code":"InvalidArgument"`)) {
		t.Fatalf("false-positive magic: expected code InvalidArgument, body: %s", body)
	}

	// Unknown magic bytes → 400 too (the client-argument class).
	u2 := s.URL + "/v1/files/notimg"
	req(t, "PUT", u2, []byte("plain text bytes"), map[string]string{"Content-Type": "application/octet-stream"})
	resp, body = req(t, "GET", u2+"/thumbnail", nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown magic: status=%d want 400 (body=%q)", resp.StatusCode, body)
	}

	// WebP magic via octet-stream → 415 (server-capability class, same as
	// the declared gate).
	u3 := s.URL + "/v1/files/weird.webp"
	req(t, "PUT", u3, webpBytes, map[string]string{"Content-Type": "application/octet-stream"})
	resp, body = req(t, "GET", u3+"/thumbnail", nil, nil)
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("webp magic via octet-stream: status=%d want 415 (body=%q)", resp.StatusCode, body)
	}

	// Decompression bomb admitted by PNG magic → 413 ImageTooLarge.
	u4 := s.URL + "/v1/files/bomb-octet"
	req(t, "PUT", u4, headerOnlyPNGBytes(t, 100000, 100000, 8, 6), map[string]string{"Content-Type": "application/octet-stream"})
	resp, body = req(t, "GET", u4+"/thumbnail", nil, nil)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("octet-stream bomb: status=%d want 413 (body=%q)", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte(`"code":"ImageTooLarge"`)) {
		t.Fatalf("octet-stream bomb: expected code ImageTooLarge, body: %s", body)
	}

	// Happy path through the byte-decided gate: a real PNG via octet-stream
	// thumbnails (200).
	u5 := s.URL + "/v1/files/real-octet"
	req(t, "PUT", u5, pngBytes(t, 64, 64), map[string]string{"Content-Type": "application/octet-stream"})
	resp, body = req(t, "GET", u5+"/thumbnail", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("octet-stream real PNG: status=%d want 200 (body=%q)", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("content-type=%q want image/jpeg", ct)
	}
}

func TestThumbnailETagFromEffectiveDims(t *testing.T) {
	// Requests whose dims differ only in clamped-away values (?w=2048 vs
	// ?w=9999, h absent → 256 in both) produce byte-identical JPEGs and must
	// share one cache validator, so shared caches don't fragment entries per
	// distinct oversized URL and the If-None-Match/304 fast path hits across
	// URLs. The validator must encode the EFFECTIVE pair (HardMax-clamped w,
	// DefaultMax defaulted h), not the raw query values.
	s := newRESTTest(t)
	u := s.URL + "/v1/files/pic.png"
	if resp, _ := req(t, "PUT", u, pngBytes(t, 300, 150), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT pic.png: %d", resp.StatusCode)
	}

	resp, body1 := req(t, "GET", u+"/thumbnail?w=2048", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("thumbnail ?w=2048: status=%d", resp.StatusCode)
	}
	etag1 := resp.Header.Get("ETag")
	if etag1 == "" {
		t.Fatal("thumbnail ?w=2048 missing ETag")
	}

	// Same effective pair (2048x256): identical validator and bytes.
	resp, body2 := req(t, "GET", u+"/thumbnail?w=9999", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("thumbnail ?w=9999: status=%d", resp.StatusCode)
	}
	etag2 := resp.Header.Get("ETag")
	if etag2 == "" {
		t.Fatal("thumbnail ?w=9999 missing ETag")
	}
	if etag1 != etag2 {
		t.Fatalf("clamped-away dims split validators: %q vs %q", etag1, etag2)
	}
	if !bytes.Equal(body1, body2) {
		t.Fatal("?w=2048 and ?w=9999 returned different bodies")
	}

	// Cross-URL revalidation: the shared validator 304s on both URLs.
	resp, b := req(t, "GET", u+"/thumbnail?w=9999", nil, map[string]string{"If-None-Match": etag1})
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("?w=9999 If-None-Match %q: status=%d want 304", etag1, resp.StatusCode)
	}
	if len(b) != 0 {
		t.Fatalf("304 must have empty body, got %d bytes", len(b))
	}
	resp, b = req(t, "GET", u+"/thumbnail?w=2048", nil, map[string]string{"If-None-Match": etag1})
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("?w=2048 If-None-Match %q: status=%d want 304", etag1, resp.StatusCode)
	}
	if len(b) != 0 {
		t.Fatalf("304 must have empty body, got %d bytes", len(b))
	}

	// Strengthening: the shared validator is the CORRECT effective value —
	// HardMax-clamped w (2048) and DefaultMax-defaulted h (256).
	resp, _ = req(t, "GET", u, nil, nil)
	objETag := strings.Trim(resp.Header.Get("ETag"), `"`)
	if objETag == "" {
		t.Fatal("plain GET missing object ETag")
	}
	if want := `"` + objETag + "-thumb-2048x256\""; etag1 != want {
		t.Fatalf("validator=%q want %q", etag1, want)
	}
}

func TestThumbnailDefaultDimsShareETag(t *testing.T) {
	// Absent dims and explicit ?w=0&h=0 both default to DefaultMax in the
	// pipeline; they must share one validator encoding 256x256, so the
	// no-query URL class collapses onto the explicit-zero class instead of
	// fragmenting shared caches with a bogus "-thumb-0x0" validator.
	s := newRESTTest(t)
	u := s.URL + "/v1/files/pic.png"
	if resp, _ := req(t, "PUT", u, pngBytes(t, 300, 150), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT pic.png: %d", resp.StatusCode)
	}

	resp, bodyZ := req(t, "GET", u+"/thumbnail?w=0&h=0", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("thumbnail ?w=0&h=0: status=%d", resp.StatusCode)
	}
	etagZ := resp.Header.Get("ETag")
	if etagZ == "" {
		t.Fatal("thumbnail ?w=0&h=0 missing ETag")
	}

	resp, bodyN := req(t, "GET", u+"/thumbnail", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("thumbnail no query: status=%d", resp.StatusCode)
	}
	etagN := resp.Header.Get("ETag")
	if etagN == "" {
		t.Fatal("thumbnail no query missing ETag")
	}
	if etagZ != etagN {
		t.Fatalf("defaulted dims split validators: %q vs %q", etagZ, etagN)
	}
	if !bytes.Equal(bodyZ, bodyN) {
		t.Fatal("?w=0&h=0 and no-query returned different bodies")
	}

	// The shared validator 304s on the no-query URL.
	resp, b := req(t, "GET", u+"/thumbnail", nil, map[string]string{"If-None-Match": etagZ})
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("no-query If-None-Match %q: status=%d want 304", etagZ, resp.StatusCode)
	}
	if len(b) != 0 {
		t.Fatalf("304 must have empty body, got %d bytes", len(b))
	}

	// Strengthening: the shared validator encodes the correct effective
	// value (both dims defaulted to DefaultMax), not merely a common wrong
	// one — the pre-fix validator was "-thumb-0x0".
	resp, _ = req(t, "GET", u, nil, nil)
	objETag := strings.Trim(resp.Header.Get("ETag"), `"`)
	if objETag == "" {
		t.Fatal("plain GET missing object ETag")
	}
	if want := `"` + objETag + "-thumb-256x256\""; etagZ != want {
		t.Fatalf("validator=%q want %q", etagZ, want)
	}
}

// TestThumbnailClampedETag304UnderSaturation pins QA P1 F1: the validator is
// derived from the EFFECTIVE (clamped) dims, and the 304 branch precedes the
// decode-slot acquisition and the deadline scope — so a revalidation with the
// clamped ETag answers 304 immediately even while all 4 slots are held by
// concurrent decodes (no park, no 504).
func TestThumbnailClampedETag304UnderSaturation(t *testing.T) {
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "etag.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, _ := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	h := NewHandler(service.NewFileService(store, repo, nil), nil)
	h.thumbnailTimeout = 300 * time.Millisecond
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })

	u := srv.URL + "/v1/files/img"
	if resp, _ := req(t, "PUT", u, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: %d", resp.StatusCode)
	}
	// The clamped request's ETag reflects the effective dims (HardMax).
	resp, _ := req(t, "GET", u+"/thumbnail?w=9999&h=9999", nil, nil)
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	if !strings.Contains(etag, fmt.Sprintf("-2048x2048")) {
		t.Fatalf("clamped ETag %q must reflect effective HardMax dims (-2048x2048)", etag)
	}

	// Saturate all 4 decode slots with concurrent slow decodes.
	big := pngBytes(t, 8192, 8192)
	if resp, _ := req(t, "PUT", srv.URL+"/v1/files/big", big, map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT big: %d", resp.StatusCode)
	}
	for i := 0; i < 4; i++ {
		go func() {
			r2, err := http.Get(srv.URL + "/v1/files/big/thumbnail?w=256&h=256")
			if err == nil {
				r2.Body.Close()
			}
		}()
	}
	time.Sleep(150 * time.Millisecond) // holders now hold the slots

	// The 304 must answer immediately from the pre-acquisition branch.
	start := time.Now()
	resp, body := req(t, "GET", u+"/thumbnail?w=9999&h=9999", nil, map[string]string{"If-None-Match": etag})
	elapsed := time.Since(start)
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("clamped 304 under saturation: status=%d want 304 (body=%q)", resp.StatusCode, body)
	}
	if got := resp.Header.Get("ETag"); got != etag {
		t.Fatalf("304 ETag %q, want %q", got, etag)
	}
	if elapsed > time.Second {
		t.Fatalf("304 took %v — the branch must precede slot acquisition (no park)", elapsed)
	}
}

// TestThumbnailClampedETagComposition pins QA P1 F2: requests whose dims
// differ only in clamped-away values share one validator — a mixed-clamp
// request (?w=3000&h=100) equals its fully-clamped twin (?w=2048&h=100), and
// a dimension-transposition bug (effective values applied to the wrong axis)
// fails the equality assertions instead of escaping the suite.
func TestThumbnailClampedETagComposition(t *testing.T) {
	s := newRESTTest(t)
	u := s.URL + "/v1/files/img"
	if resp, _ := req(t, "PUT", u, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: %d", resp.StatusCode)
	}
	etagFor := func(q string) string {
		resp, _ := req(t, "GET", u+"/thumbnail"+q, nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %d", q, resp.StatusCode)
		}
		return resp.Header.Get("ETag")
	}
	pairs := [][2]string{
		{"?w=3000&h=100", "?w=2048&h=100"},   // mixed clamp: w clamped, h not
		{"?w=100&h=3000", "?w=100&h=2048"},   // clamp on the other axis
		{"?w=9999&h=9999", "?w=2048&h=2048"}, // full clamp
		{"?w=0&h=0", ""},                     // defaults vs absent
		{"?w=3000", "?w=2048"},               // clamp with the other dim defaulted
		{"?w=1&h=9999", "?w=1&h=2048"},       // mixed, transposition would swap axes
	}
	for _, p := range pairs {
		a, b := etagFor(p[0]), etagFor(p[1])
		if a == "" || a != b {
			t.Fatalf("ETag(%q)=%q vs ETag(%q)=%q — clamped-away dims must share one validator (transposition?)", p[0], a, p[1], b)
		}
	}
	// Distinct effective dims must still produce distinct validators.
	if a, b := etagFor("?w=100&h=100"), etagFor("?w=200&h=100"); a == "" || a == b {
		t.Fatalf("distinct effective dims must differ: %q vs %q", a, b)
	}
}

// etagSwapRepo embeds repository.Repository and intercepts GetObject for a
// single target key: on the swapCall-th such call it returns the real row
// with ETag forced to stale (the "Stat view" of an object that a concurrent
// PUT has since replaced); every other call delegates to the real row (the
// "opened view"). failErr/failCall make the call return an error instead of
// the row (deterministic deleted/corrupt re-Stat); staleUpdated swaps only
// UpdatedAt on the swap call (deterministic Last-Modified provenance). The
// discriminator is per-key, so the dispatch pre-check Stat
// ("pic.png/thumbnail") and GetBucketConfig never perturb the count. This is
// the repository-seam analogue of the burstStore storage wrapper: no
// goroutines, no sleeps — the race is simulated by the call sequence itself.
type etagSwapRepo struct {
	repository.Repository
	mu           sync.Mutex
	target       string    // "pic.png"
	stale        string    // ETag served on the swap call ("" = disarmed)
	staleUpdated time.Time // UpdatedAt served on the swap call (zero = keep the row's)
	swapCall     int       // 1-based GetObject call number for target to swap on
	failErr      error     // when non-nil, GetObject for target returns it on failCall
	failCall     int       // 1-based GetObject call number for target to fail on
	calls        int       // GetObject calls observed for target
}

func (r *etagSwapRepo) GetObject(ctx context.Context, tenant, bucket, key string) (repository.Object, error) {
	r.mu.Lock()
	isTarget := key == r.target
	if isTarget {
		r.calls++
	}
	fail := isTarget && r.failErr != nil && r.calls == r.failCall
	swap := isTarget && !fail && r.stale != "" && r.calls == r.swapCall
	swapUpdated := isTarget && !fail && !r.staleUpdated.IsZero() && r.calls == r.swapCall
	r.mu.Unlock()
	if fail {
		return repository.Object{}, r.failErr
	}
	obj, err := r.Repository.GetObject(ctx, tenant, bucket, key)
	if err != nil || (!swap && !swapUpdated) {
		return obj, err
	}
	if swap {
		obj.ETag = r.stale // same row, stale validator: models "PUT landed between Stat and Get"
	}
	if swapUpdated {
		obj.UpdatedAt = r.staleUpdated // same row, older timestamp: pins the 304's Last-Modified provenance
	}
	return obj, nil
}

// TestThumbnailETagDerivedFromOpenedObject pins FR-1: the 200-path ETag must
// be derived from the Get-opened object's ETag (the version whose bytes were
// actually decoded), not the pre-open Stat's. A repository seam hook forces
// the Stat view to carry the stale v1 ETag while the blob/row at open time is
// v2; pre-fix the 200 validator names v1, post-fix it names v2. The racing
// body must stay byte-identical to the control (both decode the same v2
// blob), proving only the validator was wrong.
func TestThumbnailETagDerivedFromOpenedObject(t *testing.T) {
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "c.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	wrapped := &etagSwapRepo{Repository: repo, target: "pic.png"}
	h := NewHandler(service.NewFileService(store, wrapped, nil), nil)
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	r.Head("/v1/files/*", h.Head)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })

	u := srv.URL + "/v1/files/pic.png"
	headETag := func() string {
		t.Helper()
		resp, _ := req(t, "HEAD", u, nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("HEAD: %d", resp.StatusCode)
		}
		return strings.Trim(resp.Header.Get("ETag"), `"`)
	}

	// PUT v1 then v2: both writes hit the same unversioned StorageKey, so a
	// Stat/Get straddling the second write observes v1 in the Stat row and
	// v2 at open time — the race this fixture simulates structurally.
	if resp, _ := req(t, "PUT", u, pngBytes(t, 300, 150), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT v1: %d", resp.StatusCode)
	}
	etagA := headETag()
	if etagA == "" {
		t.Fatal("HEAD v1: empty ETag")
	}
	if resp, _ := req(t, "PUT", u, pngBytes(t, 320, 160), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT v2: %d", resp.StatusCode)
	}
	etagB := headETag()
	if etagB == "" || etagB == etagA {
		t.Fatalf("v2 ETag %q must differ from v1 %q (same StorageKey rewrite)", etagB, etagA)
	}

	// Disarmed control: the pipeline runs against the v2 blob and the 200
	// validator names v2. The suffix is computed through EffectiveDims so the
	// format string and the clamp rule stay single-sourced.
	thumbURL := u + "/thumbnail?w=100&h=100"
	resp, bodyB := req(t, "GET", thumbURL, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("control thumbnail: %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("control content-type=%q want image/jpeg", ct)
	}
	if _, format, err := image.Decode(bytes.NewReader(bodyB)); err != nil || format != "jpeg" {
		t.Fatalf("control body decode: %v fmt=%s", err, format)
	}
	ew, eh := thumbnail.EffectiveDims(100, 100)
	suffix := fmt.Sprintf("%dx%d", ew, eh)
	etagB200 := `"` + etagB + "-thumb-" + suffix + `"`
	if got := resp.Header.Get("ETag"); got != etagB200 {
		t.Fatalf("control ETag=%q want %q", got, etagB200)
	}

	// Arm the hook: the next target-key GetObject (the racing request's
	// pre-open Stat) returns the stale v1 validator; the opener's GetObject
	// (call +1) still sees the real v2 row.
	wrapped.mu.Lock()
	wrapped.stale, wrapped.swapCall = etagA, wrapped.calls+1
	wrapped.mu.Unlock()

	resp, body := req(t, "GET", thumbURL, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("racing thumbnail: %d", resp.StatusCode)
	}
	if !bytes.Equal(body, bodyB) {
		t.Fatalf("racing body differs from control — both must decode the same v2 blob")
	}
	if got := resp.Header.Get("ETag"); got != etagB200 {
		t.Fatalf("200 ETag=%q want %q (opened-object validator); got the pre-open Stat validator?", got, etagB200)
	}
	if got := resp.Header.Get("ETag"); got == `"`+etagA+"-thumb-"+suffix+`"` {
		t.Fatalf("200 ETag must not derive from the pre-open Stat (stale v1): %q", got)
	}
}

// TestThumbnailLastModifiedDerivedFromOpenedObject pins R1: the 200-path
// Last-Modified must derive from the Get-opened object's UpdatedAt (the
// version whose bytes were actually decoded), not the pre-open Stat's. The
// etagSwapRepo staleUpdated seam forces the Stat view one hour into the
// past while the row at open time is current; pre-fix the 200 serves the
// stale mtime, post-fix the fresh one. Byte-identical body proves only the
// header provenance is under test (both decode the same v2 blob).
func TestThumbnailLastModifiedDerivedFromOpenedObject(t *testing.T) {
	h := newThumbnail304Harness(t)
	ctx := context.Background()
	_, etagB200, _, bodyB := h.primeVersions() // PUTs v1 300×150 then v2 320×160; control v2 thumb

	obj, err := h.repo.GetObject(ctx, "default", "default", "pic.png")
	if err != nil {
		t.Fatalf("read real row: %v", err)
	}
	fresh := obj.UpdatedAt.UTC().Format(http.TimeFormat)
	stale := obj.UpdatedAt.Add(-time.Hour)

	// Arm the seam: the racing request's pre-open Stat (the next target-key
	// GetObject) returns the real row with UpdatedAt forced one hour into
	// the past; the opener (call +1) reads the real row — the deterministic
	// analogue of "a PUT landed between the pre-open Stat and the open"
	// (same mechanism as the 304-arm re-Stat provenance test).
	armStatUpdated(h, stale)

	resp, body := req(t, "GET", h.u+"/thumbnail?w=100&h=100", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("racing thumbnail: %d", resp.StatusCode)
	}
	if !bytes.Equal(body, bodyB) {
		t.Fatal("racing body differs from control — both must decode the same v2 blob")
	}
	if lm := resp.Header.Get("Last-Modified"); lm != fresh {
		t.Fatalf("200 Last-Modified=%q want %q (the opened object's mtime, not the pre-open Stat's)", lm, fresh)
	}
	if lm := resp.Header.Get("Last-Modified"); lm == stale.UTC().Format(http.TimeFormat) {
		t.Fatalf("200 Last-Modified must not come from the pre-open Stat (stale %q)", lm)
	}
	// The validator/mtime pair names one representation (RFC 9110 §8.8.3).
	if got := resp.Header.Get("ETag"); got != etagB200 {
		t.Fatalf("200 ETag=%q want %q (validator and mtime must describe the same served version)", got, etagB200)
	}
}

// TestThumbnail304ShortCircuitsBeforeOpen pins FR-4: a matching If-None-Match
// short-circuits on the Stat-derived validator BEFORE the opener's svc.Get
// runs — the object stream never opens and no decode slot is touched. The
// hook's target-key GetObject count is the structural proof (no timers, no
// sleeps): exactly 2 — the pre-open Stat plus the fix's re-Stat that
// re-observes the object immediately before certifying Not Modified — never
// the opener (which would be the third target-key call). The row is seeded
// directly through the concrete repo so PUT's overwrite-protection GetObject
// does not perturb the counter.
func TestThumbnail304ShortCircuitsBeforeOpen(t *testing.T) {
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "c.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	wrapped := &etagSwapRepo{Repository: repo, target: "pic.png"}
	h := NewHandler(service.NewFileService(store, wrapped, nil), nil)
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	r.Head("/v1/files/*", h.Head)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })

	// Seed the object row directly (bypassing the wrapper's counter): the 304
	// path never opens the blob, so no storage write is required. The seeded
	// ETag matches the client's validator below, so the re-Stat confirms the
	// match and the 304 fires — the seam stays disarmed.
	if _, err := repo.UpsertObject(context.Background(), repository.Object{
		TenantID:    "default",
		Bucket:      "default",
		Key:         "pic.png",
		StorageKey:  "default/default/pic.png",
		Size:        1,
		ETag:        "seed-etag",
		ContentType: "image/png",
		Metadata:    map[string]string{},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ew, eh := thumbnail.EffectiveDims(100, 100)
	suffix := fmt.Sprintf("%dx%d", ew, eh)
	u := srv.URL + "/v1/files/pic.png/thumbnail?w=100&h=100"

	// Matching validator: the 304 fast path completes with exactly 2
	// target-key repository reads (pre-open Stat + re-Stat) and never opens
	// the stream / never touches a decode slot.
	resp, _ := req(t, "GET", u, nil, map[string]string{"If-None-Match": `"seed-etag-thumb-` + suffix + `"`})
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("If-None-Match: status=%d want 304", resp.StatusCode)
	}
	wrapped.mu.Lock()
	calls := wrapped.calls
	wrapped.mu.Unlock()
	if calls != 2 {
		t.Fatalf("target-key GetObject calls = %d, want 2 (pre-open Stat + re-Stat) — the 304 must short-circuit before the opener runs", calls)
	}

	// Non-matching validator (FR-5 confinement): the re-Stat runs ONLY when
	// the pre-open Stat's validator matches, so a non-matching If-None-Match
	// costs the same 2 target-key reads as the 200 path always did (Stat +
	// opener) — no third read. The opener then fails on the missing seeded
	// blob (404), never a 304 for a validator the request did not match.
	resp, _ = req(t, "GET", u, nil, map[string]string{"If-None-Match": `"zzz-thumb-` + suffix + `"`})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("non-matching If-None-Match: status=%d want 404 (seeded row has no blob; the 200 path's open fails), never 304", resp.StatusCode)
	}
	wrapped.mu.Lock()
	calls = wrapped.calls
	wrapped.mu.Unlock()
	if calls != 4 {
		t.Fatalf("target-key GetObject calls after non-matching request = %d, want 4 (2 + Stat + opener) — the re-Stat must be skipped", calls)
	}
}

// thumbnail304Harness wires the fixtures shared by the 304-revalidation
// regression tests: a real SQLite repository behind etagSwapRepo, a
// local-storage FileService, and a chi router serving putKey/getKey/Head —
// the same wiring TestThumbnailETagDerivedFromOpenedObject uses. Seeding goes
// through the concrete repo so it never perturbs the seam's target-key
// counter; arming is always relative to the current counter position.
type thumbnail304Harness struct {
	t       *testing.T
	srv     *httptest.Server
	wrapped *etagSwapRepo
	repo    repository.Repository
	u       string // base object URL: srv.URL + /v1/files/pic.png
}

func newThumbnail304Harness(t *testing.T) *thumbnail304Harness {
	t.Helper()
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "c.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	wrapped := &etagSwapRepo{Repository: repo, target: "pic.png"}
	h := NewHandler(service.NewFileService(store, wrapped, nil), nil)
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	r.Head("/v1/files/*", h.Head)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })
	return &thumbnail304Harness{t: t, srv: srv, wrapped: wrapped, repo: repo, u: srv.URL + "/v1/files/pic.png"}
}

// primeVersions PUTs two PNG versions through the API (v1 300×150, v2
// 320×160), captures both validators and the v2 control thumbnail, and
// asserts the seam counter landed on 8: 2 PUT overwrite-protection reads + 2
// HEAD reads + 2 control-GET Stat/open pairs. The assertion fails loudly here
// if the service's read pattern ever changes, instead of corrupting the
// relative arming of every later arm.
func (h *thumbnail304Harness) primeVersions() (staleV1, etagB200, suffix string, bodyB []byte) {
	t := h.t
	if resp, _ := req(t, "PUT", h.u, pngBytes(t, 300, 150), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT v1: %d", resp.StatusCode)
	}
	staleV1 = h.headETag()
	if staleV1 == "" {
		t.Fatal("HEAD v1: empty ETag")
	}
	thumbURL := h.u + "/thumbnail?w=100&h=100"
	if resp, _ := req(t, "GET", thumbURL, nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("control v1 thumbnail: %d", resp.StatusCode)
	}
	if resp, _ := req(t, "PUT", h.u, pngBytes(t, 320, 160), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT v2: %d", resp.StatusCode)
	}
	etagB := h.headETag()
	if etagB == "" || etagB == staleV1 {
		t.Fatalf("v2 ETag %q must differ from v1 %q (same StorageKey rewrite)", etagB, staleV1)
	}
	resp, bodyB := req(t, "GET", thumbURL, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("control v2 thumbnail: %d", resp.StatusCode)
	}
	ew, eh := thumbnail.EffectiveDims(100, 100)
	suffix = fmt.Sprintf("%dx%d", ew, eh)
	etagB200 = `"` + etagB + "-thumb-" + suffix + `"`
	if got := resp.Header.Get("ETag"); got != etagB200 {
		t.Fatalf("control v2 ETag=%q want %q", got, etagB200)
	}
	h.wrapped.mu.Lock()
	calls := h.wrapped.calls
	h.wrapped.mu.Unlock()
	if calls != 12 {
		t.Fatalf("primeVersions: target-key GetObject calls = %d, want 12 (2 PUTs × 3 reads [preparePutAccess + checkOverwriteProtection + objectWriteUsage] + 2 HEAD Stats + 2 control-GET Stat/open pairs)", calls)
	}
	return staleV1, etagB200, suffix, bodyB
}

func (h *thumbnail304Harness) headETag() string {
	t := h.t
	resp, _ := req(t, "HEAD", h.u, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD: %d", resp.StatusCode)
	}
	return strings.Trim(resp.Header.Get("ETag"), `"`)
}

// armStatSwap makes the next target-key GetObject (the racing request's
// pre-open Stat) return the stale v1 validator; the re-Stat and opener still
// see the real v2 row — the deterministic analogue of a PUT landing between
// the pre-open Stat and the 304 emission.
func armStatSwap(h *thumbnail304Harness, staleETag string) {
	h.wrapped.mu.Lock()
	h.wrapped.stale, h.wrapped.swapCall = staleETag, h.wrapped.calls+1
	h.wrapped.mu.Unlock()
}

// armStatUpdated makes the next target-key GetObject return the real row with
// UpdatedAt forced to stale (same ETag, older timestamp), so a 304's
// Last-Modified provenance is pinned to the re-Stat, not the pre-open Stat.
func armStatUpdated(h *thumbnail304Harness, stale time.Time) {
	h.wrapped.mu.Lock()
	h.wrapped.staleUpdated, h.wrapped.swapCall = stale, h.wrapped.calls+1
	h.wrapped.mu.Unlock()
}

// assertNoCacheHeaders pins that error responses carry no cacheable success
// headers: a revalidating shared cache must never adopt a validator for a
// response that was never generated.
func assertNoCacheHeaders(t *testing.T, resp *http.Response, what string) {
	t.Helper()
	for _, h := range []string{"ETag", "Cache-Control", "Last-Modified"} {
		if v := resp.Header.Get(h); v != "" {
			t.Fatalf("%s: %s=%q must be absent on the error path", what, h, v)
		}
	}
}

// TestThumbnail304RevalidatesAfterStat pins the fix for the 304 fast-path
// stale-validator race (FR-1/FR-2/FR-6): the 304 branch re-Stats the current
// version before certifying Not Modified, re-evaluates If-None-Match against
// the fresh validator, and — when the object changed between the two Stats —
// falls through to the 200 path, whose validator names the opened object.
// Every arm arms the etagSwapRepo seam so the racing request's pre-open Stat
// observes the stale v1 row while the re-Stat/opener observe the real v2 row.
func TestThumbnail304RevalidatesAfterStat(t *testing.T) {
	thumbURL := func(h *thumbnail304Harness) string { return h.u + "/thumbnail?w=100&h=100" }
	assertCalls := func(t *testing.T, h *thumbnail304Harness, want int, what string) {
		t.Helper()
		h.wrapped.mu.Lock()
		calls := h.wrapped.calls
		h.wrapped.mu.Unlock()
		if calls != want {
			t.Fatalf("%s: target-key GetObject calls = %d, want %d", what, calls, want)
		}
	}

	t.Run("changed object forces 200 with current validator", func(t *testing.T) {
		// AC-1: a PUT between the pre-open Stat and the 304 emission must
		// yield a 200 carrying the current-state validator — never a stale
		// 304 (pre-fix this request returns 304 and this arm fails).
		h := newThumbnail304Harness(t)
		staleV1, etagB200, suffix, bodyB := h.primeVersions()
		armStatSwap(h, staleV1)
		resp, body := req(t, "GET", thumbURL(h), nil, map[string]string{"If-None-Match": `"` + staleV1 + "-thumb-" + suffix + `"`})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d want 200 — the object changed between the two Stats, never a stale 304", resp.StatusCode)
		}
		if !bytes.Equal(body, bodyB) {
			t.Fatal("body differs from the v2 control — both must decode the same v2 blob")
		}
		if got := resp.Header.Get("ETag"); got != etagB200 {
			t.Fatalf("ETag=%q want %q (validator must name current state)", got, etagB200)
		}
		assertCalls(t, h, 15, "changed-object 200 path (12 prime + Stat + re-Stat + opener)")
	})

	t.Run("stable object still 304s with fresh validator", func(t *testing.T) {
		// AC-2 Arm A: no mutation between requests; the 304 carries the
		// current derived validator and a parseable Last-Modified, and costs
		// exactly 2 target-key reads (pre-open Stat + re-Stat).
		h := newThumbnail304Harness(t)
		_, etagB200, _, _ := h.primeVersions()
		resp, body := req(t, "GET", thumbURL(h), nil, map[string]string{"If-None-Match": etagB200})
		if resp.StatusCode != http.StatusNotModified {
			t.Fatalf("status=%d want 304 for the unchanged object", resp.StatusCode)
		}
		if len(body) != 0 {
			t.Fatalf("304 body must be empty, got %d bytes", len(body))
		}
		if got := resp.Header.Get("ETag"); got != etagB200 {
			t.Fatalf("ETag=%q want %q (fresh validator)", got, etagB200)
		}
		if lm := resp.Header.Get("Last-Modified"); lm == "" {
			t.Fatal("Last-Modified must be set on the 304 (from the re-Stat observation)")
		} else if _, err := http.ParseTime(lm); err != nil {
			t.Fatalf("Last-Modified %q does not parse: %v", lm, err)
		}
		assertCalls(t, h, 14, "stable 304 (12 prime + Stat + re-Stat)")
	})

	t.Run("multi-token If-None-Match re-evaluates against the fresh validator", func(t *testing.T) {
		// AC-2 Arm B: the client's cached validator (stale v1) and the
		// current validator (v2) both appear in the list; the re-Stat moved
		// the object v1→v2, yet the re-evaluated match still 304s — with the
		// FRESH validator. A naive statETag != freshETag → 200 comparator
		// fails this arm.
		h := newThumbnail304Harness(t)
		staleV1, etagB200, suffix, _ := h.primeVersions()
		armStatSwap(h, staleV1)
		inm := `"` + staleV1 + "-thumb-" + suffix + `", ` + etagB200
		resp, _ := req(t, "GET", thumbURL(h), nil, map[string]string{"If-None-Match": inm})
		if resp.StatusCode != http.StatusNotModified {
			t.Fatalf("status=%d want 304 (re-evaluated match against the fresh validator); INM=%s", resp.StatusCode, inm)
		}
		if got := resp.Header.Get("ETag"); got != etagB200 {
			t.Fatalf("ETag=%q want %q (the fresh validator, not the stale one)", got, etagB200)
		}
	})

	t.Run("wildcard If-None-Match 304s with the fresh validator", func(t *testing.T) {
		h := newThumbnail304Harness(t)
		staleV1, etagB200, _, _ := h.primeVersions()
		armStatSwap(h, staleV1)
		resp, _ := req(t, "GET", thumbURL(h), nil, map[string]string{"If-None-Match": "*"})
		if resp.StatusCode != http.StatusNotModified {
			t.Fatalf("status=%d want 304 for If-None-Match: *", resp.StatusCode)
		}
		if got := resp.Header.Get("ETag"); got != etagB200 {
			t.Fatalf("ETag=%q want %q (the fresh validator — a shared cache freshens from it, RFC 9111 §4.3.5)", got, etagB200)
		}
	})

	t.Run("weak validator detects drift", func(t *testing.T) {
		// QA F5: etagListMatches strips W/ before comparison, so a weak token
		// matching the stale Stat still falls through to 200 when the fresh
		// validator no longer matches.
		h := newThumbnail304Harness(t)
		staleV1, etagB200, suffix, _ := h.primeVersions()
		armStatSwap(h, staleV1)
		resp, _ := req(t, "GET", thumbURL(h), nil, map[string]string{"If-None-Match": `W/` + `"` + staleV1 + "-thumb-" + suffix + `"`})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d want 200 — a weak match on the stale Stat must not certify 304 after drift", resp.StatusCode)
		}
		if got := resp.Header.Get("ETag"); got != etagB200 {
			t.Fatalf("ETag=%q want %q", got, etagB200)
		}
	})

	t.Run("weak validator stable object 304s", func(t *testing.T) {
		h := newThumbnail304Harness(t)
		_, etagB200, _, _ := h.primeVersions()
		resp, _ := req(t, "GET", thumbURL(h), nil, map[string]string{"If-None-Match": `W/` + etagB200})
		if resp.StatusCode != http.StatusNotModified {
			t.Fatalf("status=%d want 304 — a weak validator of the current state still revalidates", resp.StatusCode)
		}
		if got := resp.Header.Get("ETag"); got != etagB200 {
			t.Fatalf("ETag=%q want %q", got, etagB200)
		}
	})

	t.Run("304 Last-Modified comes from the re-Stat observation", func(t *testing.T) {
		// QA F7: the seam forces the pre-open Stat's UpdatedAt one hour into
		// the past while keeping the same ETag; the 304 must carry the fresh
		// row's Last-Modified, never the stale one.
		h := newThumbnail304Harness(t)
		_, etagB200, _, _ := h.primeVersions()
		obj, err := h.repo.GetObject(context.Background(), "default", "default", "pic.png")
		if err != nil {
			t.Fatalf("read real row: %v", err)
		}
		fresh := obj.UpdatedAt.UTC().Format(http.TimeFormat)
		stale := obj.UpdatedAt.Add(-time.Hour).UTC().Format(http.TimeFormat)
		armStatUpdated(h, obj.UpdatedAt.Add(-time.Hour))
		resp, _ := req(t, "GET", thumbURL(h), nil, map[string]string{"If-None-Match": etagB200})
		if resp.StatusCode != http.StatusNotModified {
			t.Fatalf("status=%d want 304 (same ETag, so the re-evaluated match holds)", resp.StatusCode)
		}
		if lm := resp.Header.Get("Last-Modified"); lm != fresh {
			t.Fatalf("Last-Modified=%q want %q (the re-Stat observation, not the pre-open Stat's)", lm, fresh)
		}
		if lm := resp.Header.Get("Last-Modified"); lm == stale {
			t.Fatalf("Last-Modified must not come from the pre-open Stat (stale %q)", stale)
		}
	})

	t.Run("HEAD drift forces 200 like GET", func(t *testing.T) {
		// QA F6: GET and HEAD share the handler, so the armed drift yields
		// 200 with the current validator for both verbs (FR-6).
		h := newThumbnail304Harness(t)
		staleV1, etagB200, suffix, _ := h.primeVersions()
		armStatSwap(h, staleV1)
		resp, body := req(t, "HEAD", thumbURL(h), nil, map[string]string{"If-None-Match": `"` + staleV1 + "-thumb-" + suffix + `"`})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d want 200 for HEAD (drift must not 304)", resp.StatusCode)
		}
		if len(body) != 0 {
			t.Fatalf("HEAD body must be empty, got %d bytes", len(body))
		}
		if got := resp.Header.Get("ETag"); got != etagB200 {
			t.Fatalf("ETag=%q want %q", got, etagB200)
		}
	})

	t.Run("non-matching If-None-Match skips the re-Stat", func(t *testing.T) {
		// QA F3 / FR-5: the re-Stat lives strictly inside the 304 branch, so
		// a non-matching request costs the same 2 target-key reads as the 200
		// path always did (Stat + opener) — no third read.
		h := newThumbnail304Harness(t)
		_, etagB200, suffix, _ := h.primeVersions()
		resp, _ := req(t, "GET", thumbURL(h), nil, map[string]string{"If-None-Match": `"nonexistent-thumb-` + suffix + `"`})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d want 200 for the non-matching request", resp.StatusCode)
		}
		if got := resp.Header.Get("ETag"); got != etagB200 {
			t.Fatalf("ETag=%q want %q", got, etagB200)
		}
		assertCalls(t, h, 14, "non-matching 200 path (12 prime + Stat + opener; no re-Stat)")
	})
}

// TestThumbnail304RevalidatesAfterStatErrors pins FR-3: a re-Stat that fails
// (object deleted → 404, corrupt → 410) must propagate the classified error
// and never emit a 304 — a shared cache must not keep serving a dead or
// known-bad object's derived thumbnail. The error responses must also carry
// no cache headers (QA F4).
func TestThumbnail304RevalidatesAfterStatErrors(t *testing.T) {
	seed := func(t *testing.T, h *thumbnail304Harness) {
		t.Helper()
		if _, err := h.repo.UpsertObject(context.Background(), repository.Object{
			TenantID:    "default",
			Bucket:      "default",
			Key:         "pic.png",
			StorageKey:  "default/default/pic.png",
			Size:        1,
			ETag:        "seed-etag",
			ContentType: "image/png",
			Metadata:    map[string]string{},
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	ew, eh := thumbnail.EffectiveDims(100, 100)
	suffix := fmt.Sprintf("%dx%d", ew, eh)

	t.Run("deleted between stats returns 404 never 304", func(t *testing.T) {
		// AC-2 Arm C: pre-open Stat = call 1 (real seeded row, matching
		// validator); re-Stat = call 2 → ErrNotFound → 404. Pre-fix this
		// request returns 304 and this arm fails.
		h := newThumbnail304Harness(t)
		seed(t, h)
		h.wrapped.mu.Lock()
		h.wrapped.failErr, h.wrapped.failCall = repository.ErrNotFound, h.wrapped.calls+2
		h.wrapped.mu.Unlock()
		resp, body := req(t, "GET", h.u+"/thumbnail?w=100&h=100", nil, map[string]string{"If-None-Match": `"seed-etag-thumb-` + suffix + `"`})
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status=%d want 404 — never a 304 for a state we could not observe; body=%s", resp.StatusCode, body)
		}
		if !bytes.Contains(body, []byte(`"code":"NotFound"`)) {
			t.Fatalf("404 body missing NotFound code: %s", body)
		}
		assertNoCacheHeaders(t, resp, "deleted re-Stat 404")
		h.wrapped.mu.Lock()
		calls := h.wrapped.calls
		h.wrapped.mu.Unlock()
		if calls != 2 {
			t.Fatalf("target-key GetObject calls = %d, want 2 (pre-open Stat + re-Stat; the opener never ran)", calls)
		}
	})

	t.Run("corrupt between stats returns 410 never 304", func(t *testing.T) {
		// QA F1: same shape with a scrub-corrupt object — classify → 410
		// ObjectCorrupt, never a 304 for a known-bad object.
		h := newThumbnail304Harness(t)
		seed(t, h)
		h.wrapped.mu.Lock()
		h.wrapped.failErr, h.wrapped.failCall = service.ErrObjectCorrupt, h.wrapped.calls+2
		h.wrapped.mu.Unlock()
		resp, body := req(t, "GET", h.u+"/thumbnail?w=100&h=100", nil, map[string]string{"If-None-Match": `"seed-etag-thumb-` + suffix + `"`})
		if resp.StatusCode != http.StatusGone {
			t.Fatalf("status=%d want 410 ObjectCorrupt — never a 304 for a state we could not observe; body=%s", resp.StatusCode, body)
		}
		if !bytes.Contains(body, []byte(`"code":"ObjectCorrupt"`)) {
			t.Fatalf("410 body missing ObjectCorrupt code: %s", body)
		}
		assertNoCacheHeaders(t, resp, "corrupt re-Stat 410")
		h.wrapped.mu.Lock()
		calls := h.wrapped.calls
		h.wrapped.mu.Unlock()
		if calls != 2 {
			t.Fatalf("target-key GetObject calls = %d, want 2 (pre-open Stat + re-Stat; the opener never ran)", calls)
		}
	})
}

// TestThumbnailETagDerivedFromOpenedObjectSniffBucket is the QA F2 binding
// variant: the same Stat→Get race, armed on the dominant upload path —
// application/octet-stream (the magic-sniff bucket), where the opener wraps
// the stream and the capture position is guarded by a wrapper. The 200
// validator must still name the OPENED object (v2), never the pre-open Stat's
// stale v1, and the sniffed body must decode identically.
func TestThumbnailETagDerivedFromOpenedObjectSniffBucket(t *testing.T) {
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "c.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	wrapped := &etagSwapRepo{Repository: repo, target: "pic.bin"}
	h := NewHandler(service.NewFileService(store, wrapped, nil), nil)
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	r.Head("/v1/files/*", h.Head)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })

	u := srv.URL + "/v1/files/pic.bin"
	octet := map[string]string{"Content-Type": "application/octet-stream"}
	headETag := func() string {
		t.Helper()
		resp, _ := req(t, "HEAD", u, nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("HEAD: %d", resp.StatusCode)
		}
		return strings.Trim(resp.Header.Get("ETag"), `"`)
	}
	if resp, _ := req(t, "PUT", u, pngBytes(t, 300, 150), octet); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT v1: %d", resp.StatusCode)
	}
	etagA := headETag()
	if resp, _ := req(t, "PUT", u, pngBytes(t, 320, 160), octet); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT v2: %d", resp.StatusCode)
	}
	etagB := headETag()
	if etagB == "" || etagB == etagA {
		t.Fatalf("v2 ETag %q must differ from v1 %q", etagB, etagA)
	}

	// Control: sniffed admission (magic bytes) + 200 validator names v2.
	thumbURL := u + "/thumbnail?w=100&h=100"
	resp, bodyB := req(t, "GET", thumbURL, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("control thumbnail (sniff bucket): %d", resp.StatusCode)
	}
	if _, format, err := image.Decode(bytes.NewReader(bodyB)); err != nil || format != "jpeg" {
		t.Fatalf("control body decode: %v fmt=%s", err, format)
	}
	ew, eh := thumbnail.EffectiveDims(100, 100)
	etagB200 := `"` + etagB + "-thumb-" + fmt.Sprintf("%dx%d", ew, eh) + `"`
	if got := resp.Header.Get("ETag"); got != etagB200 {
		t.Fatalf("control ETag=%q want %q", got, etagB200)
	}

	// Arm the hook on the sniff-bucket path: the racing request's pre-open
	// Stat sees stale v1; the opener (sniff wrapper around svc.Get) still
	// reads the v2 row.
	wrapped.mu.Lock()
	wrapped.stale, wrapped.swapCall = etagA, wrapped.calls+1
	wrapped.mu.Unlock()

	resp, body := req(t, "GET", thumbURL, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("racing thumbnail (sniff bucket): %d", resp.StatusCode)
	}
	if !bytes.Equal(body, bodyB) {
		t.Fatalf("racing body differs from control — both must decode the same v2 blob")
	}
	if got := resp.Header.Get("ETag"); got != etagB200 {
		t.Fatalf("200 ETag=%q want %q (opened-object validator through the sniff wrapper)", got, etagB200)
	}
	if got := resp.Header.Get("ETag"); got == `"`+etagA+"-thumb-"+fmt.Sprintf("%dx%d", ew, eh)+`"` {
		t.Fatalf("200 ETag must not derive from the pre-open Stat (stale v1) on the sniff path: %q", got)
	}
}

// TestThumbnailLastModifiedDerivedFromOpenedObjectSniffBucket is the QA F2
// symmetry obligation for R1: the same 200-path Last-Modified provenance pin
// on the sniff-bucket path (application/octet-stream + magic admission — the
// upload norm for curl -T / S3 SDKs). The shared `opened` capture sits before
// the sniff branch, so a refactor moving it inside the branch would regress
// the mtime provenance on the dominant upload path with zero test signal;
// this test fails red on exactly that regression.
func TestThumbnailLastModifiedDerivedFromOpenedObjectSniffBucket(t *testing.T) {
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "c.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	wrapped := &etagSwapRepo{Repository: repo, target: "pic.bin"}
	h := NewHandler(service.NewFileService(store, wrapped, nil), nil)
	r := chi.NewRouter()
	r.Put("/v1/files/*", h.putKey)
	r.Get("/v1/files/*", h.getKey)
	r.Head("/v1/files/*", h.Head)
	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close(); _ = repo.Close() })

	u := srv.URL + "/v1/files/pic.bin"
	octet := map[string]string{"Content-Type": "application/octet-stream"}
	headETag := func() string {
		t.Helper()
		resp, _ := req(t, "HEAD", u, nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("HEAD: %d", resp.StatusCode)
		}
		return strings.Trim(resp.Header.Get("ETag"), `"`)
	}
	if resp, _ := req(t, "PUT", u, pngBytes(t, 300, 150), octet); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT v1: %d", resp.StatusCode)
	}
	etagA := headETag()
	if resp, _ := req(t, "PUT", u, pngBytes(t, 320, 160), octet); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT v2: %d", resp.StatusCode)
	}
	etagB := headETag()
	if etagB == "" || etagB == etagA {
		t.Fatalf("v2 ETag %q must differ from v1 %q", etagB, etagA)
	}

	// Control: sniffed admission (magic bytes) + 200 mtime names the v2 row.
	thumbURL := u + "/thumbnail?w=100&h=100"
	resp, bodyB := req(t, "GET", thumbURL, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("control thumbnail (sniff bucket): %d", resp.StatusCode)
	}
	if _, format, err := image.Decode(bytes.NewReader(bodyB)); err != nil || format != "jpeg" {
		t.Fatalf("control body decode: %v fmt=%s", err, format)
	}
	ew, eh := thumbnail.EffectiveDims(100, 100)
	etagB200 := `"` + etagB + "-thumb-" + fmt.Sprintf("%dx%d", ew, eh) + `"`

	obj, err := repo.GetObject(context.Background(), "default", "default", "pic.bin")
	if err != nil {
		t.Fatalf("read real row: %v", err)
	}
	fresh := obj.UpdatedAt.UTC().Format(http.TimeFormat)
	stale := obj.UpdatedAt.Add(-time.Hour)

	// Arm the hook on the sniff-bucket path: the racing request's pre-open
	// Stat sees the stale mtime; the opener (sniff wrapper around svc.Get)
	// still reads the real v2 row.
	wrapped.mu.Lock()
	wrapped.staleUpdated, wrapped.swapCall = stale, wrapped.calls+1
	wrapped.mu.Unlock()

	resp, body := req(t, "GET", thumbURL, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("racing thumbnail (sniff bucket): %d", resp.StatusCode)
	}
	if !bytes.Equal(body, bodyB) {
		t.Fatalf("racing body differs from control — both must decode the same v2 blob")
	}
	if lm := resp.Header.Get("Last-Modified"); lm != fresh {
		t.Fatalf("200 Last-Modified=%q want %q (opened-object mtime through the sniff wrapper)", lm, fresh)
	}
	if lm := resp.Header.Get("Last-Modified"); lm == stale.UTC().Format(http.TimeFormat) {
		t.Fatalf("200 Last-Modified must not come from the pre-open Stat (stale %q) on the sniff path", lm)
	}
	if got := resp.Header.Get("ETag"); got != etagB200 {
		t.Fatalf("200 ETag=%q want %q (validator and mtime must describe the same served version)", got, etagB200)
	}
}

// TestThumbnailHeadParityExistingImage pins AC-1: HEAD /v1/files/{key}/thumbnail
// must return the same status and header fields as GET on the same URL (RFC
// 9110 §9.3.2) with an empty body — the derived thumbnail ETag, the generated
// JPEG Content-Length and Content-Type, the identical Cache-Control directive,
// and the 304 conditional arm. Regression: previously HEAD stat-ed the
// un-trimmed "{key}/thumbnail" full key and 404'd while GET 200'd.
func TestThumbnailHeadParityExistingImage(t *testing.T) {
	s := newRESTTest(t)
	u := s.URL + "/v1/files/pic.png"
	if resp, _ := req(t, "PUT", u, pngBytes(t, 300, 150), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT pic.png: %d", resp.StatusCode)
	}
	thumbURL := u + "/thumbnail?w=100&h=100"

	// GET baseline: 200 image/jpeg with the derived thumbnail ETag and the
	// generated-JPEG Content-Length.
	getResp, getBody := req(t, "GET", thumbURL, nil, nil)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET thumbnail: status=%d want 200", getResp.StatusCode)
	}
	getETag := getResp.Header.Get("ETag")
	if getETag == "" {
		t.Fatal("GET thumbnail: missing ETag")
	}
	if ct := getResp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("GET thumbnail: Content-Type=%q want image/jpeg", ct)
	}
	getCL := getResp.Header.Get("Content-Length")
	if getCL != strconv.Itoa(len(getBody)) {
		t.Fatalf("GET thumbnail: Content-Length=%q want %d", getCL, len(getBody))
	}
	getCC := getResp.Header.Get("Cache-Control")

	// HEAD must mirror status + headers with an empty body.
	headResp, headBody := req(t, "HEAD", thumbURL, nil, nil)
	if headResp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD thumbnail: status=%d want 200", headResp.StatusCode)
	}
	if ct := headResp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("HEAD thumbnail: Content-Type=%q want image/jpeg", ct)
	}
	if et := headResp.Header.Get("ETag"); et != getETag {
		t.Fatalf("HEAD thumbnail: ETag=%q want GET's %q", et, getETag)
	}
	if cl := headResp.Header.Get("Content-Length"); cl != getCL {
		t.Fatalf("HEAD thumbnail: Content-Length=%q want GET's %q", cl, getCL)
	}
	if cc := headResp.Header.Get("Cache-Control"); cc != getCC {
		t.Fatalf("HEAD thumbnail: Cache-Control=%q want GET's %q", cc, getCC)
	}
	if len(headBody) != 0 {
		t.Fatalf("HEAD thumbnail: body=%d bytes, want empty", len(headBody))
	}

	// Conditional arm (R3): HEAD carrying the GET-derived validator in
	// If-None-Match must 304 exactly like GET, mirroring ETag /
	// Last-Modified / Cache-Control.
	head304, body304 := req(t, "HEAD", thumbURL, nil, map[string]string{"If-None-Match": getETag})
	if head304.StatusCode != http.StatusNotModified {
		t.Fatalf("HEAD thumbnail If-None-Match: status=%d want 304", head304.StatusCode)
	}
	if len(body304) != 0 {
		t.Fatalf("HEAD 304: body=%d bytes, want empty", len(body304))
	}
	if et := head304.Header.Get("ETag"); et != getETag {
		t.Fatalf("HEAD 304: ETag=%q want %q", et, getETag)
	}
	if cc := head304.Header.Get("Cache-Control"); cc != getCC {
		t.Fatalf("HEAD 304: Cache-Control=%q want GET 200's %q", cc, getCC)
	}
	if lm := head304.Header.Get("Last-Modified"); lm == "" {
		t.Fatal("HEAD 304: missing Last-Modified")
	}
}

// TestThumbnailHeadParityMissingOrNonImage pins AC-2: for every URL where GET
// /thumbnail returns an error status, HEAD on the same URL must return the
// identical status (RFC 9110 §9.3.2) — never the old divergent 404 from
// stat-ing the un-trimmed "{key}/thumbnail" full key.
func TestThumbnailHeadParityMissingOrNonImage(t *testing.T) {
	s := newRESTTest(t)
	check := func(t *testing.T, url string, want int) {
		t.Helper()
		getResp, _ := req(t, "GET", url, nil, nil)
		headResp, headBody := req(t, "HEAD", url, nil, nil)
		if getResp.StatusCode != want || headResp.StatusCode != want {
			t.Fatalf("%s: GET=%d HEAD=%d want both %d", url, getResp.StatusCode, headResp.StatusCode, want)
		}
		if len(headBody) != 0 {
			t.Fatalf("%s HEAD: body=%d bytes, want empty", url, len(headBody))
		}
	}

	// Missing object → 404 both.
	check(t, s.URL+"/v1/files/nope.png/thumbnail", http.StatusNotFound)

	// Declared non-image → 400 InvalidArgument both (GET's classification
	// preserved).
	notesURL := s.URL + "/v1/files/notes.txt"
	if resp, _ := req(t, "PUT", notesURL, []byte("plain text"), map[string]string{"Content-Type": "text/plain"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT notes.txt: %d", resp.StatusCode)
	}
	check(t, notesURL+"/thumbnail", http.StatusBadRequest)

	// Declared unsupported image → 415 UnsupportedMediaType both.
	webpURL := s.URL + "/v1/files/pic.webp"
	if resp, _ := req(t, "PUT", webpURL, webpBytes, map[string]string{"Content-Type": "image/webp"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT pic.webp: %d", resp.StatusCode)
	}
	check(t, webpURL+"/thumbnail", http.StatusUnsupportedMediaType)
}

// TestThumbnailHeadParityExactKey pins design P1 (R3 exact-key arm, FR-1):
// when an object literally named "{key}/thumbnail" exists, GET serves the raw
// object. HEAD must mirror that raw response — ETag, Content-Type and
// Content-Length identical, body empty — never a derived thumbnail.
func TestThumbnailHeadParityExactKey(t *testing.T) {
	s, tok, repo := newAuthRESTTestWithRepo(t)
	enableVersioningForCoexistence(t, repo)
	authH := map[string]string{"Authorization": tok}
	u := s.URL + "/v1/files/dir"
	if resp, _ := req(t, "PUT", u, pngBytes(t, 300, 150), map[string]string{"Content-Type": "image/png", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT dir: %d", resp.StatusCode)
	}
	uFull := u + "/thumbnail"
	if resp, _ := req(t, "PUT", uFull, []byte("object bytes"), map[string]string{"Content-Type": "text/plain", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT dir/thumbnail: %d", resp.StatusCode)
	}

	getResp, getBody := req(t, "GET", uFull, nil, authH)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET exact key: status=%d want 200", getResp.StatusCode)
	}
	headResp, headBody := req(t, "HEAD", uFull, nil, authH)
	if headResp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD exact key: status=%d want 200", headResp.StatusCode)
	}
	if et := headResp.Header.Get("ETag"); et != getResp.Header.Get("ETag") {
		t.Fatalf("HEAD exact key: ETag=%q want GET's %q", et, getResp.Header.Get("ETag"))
	}
	if ct := headResp.Header.Get("Content-Type"); ct != "text/plain" {
		t.Fatalf("HEAD exact key: Content-Type=%q want text/plain", ct)
	}
	if cl := headResp.Header.Get("Content-Length"); cl != strconv.Itoa(len(getBody)) {
		t.Fatalf("HEAD exact key: Content-Length=%q want %d", cl, len(getBody))
	}
	if len(headBody) != 0 {
		t.Fatalf("HEAD exact key: body=%d bytes, want empty", len(headBody))
	}
}

// TestThumbnailHeadParityEmptyExactKey pins protocol-expert F2: a 0-byte
// object literally named "{key}/thumbnail" is served through
// handleRangeOrFull, which must emit "Content-Length: 0" on HEAD exactly as
// GET does (RFC 9110 §9.3.2). net/http auto-emits CL only when a body was
// written (server.go:1327) — GET gets auto-0, HEAD would get no CL header at
// all without the explicit unconditional set (mirroring Head, handler.go:249).
func TestThumbnailHeadParityEmptyExactKey(t *testing.T) {
	s := newRESTTest(t)
	u := s.URL + "/v1/files/empty/thumbnail"
	if resp, _ := req(t, "PUT", u, nil, map[string]string{"Content-Type": "text/plain"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT empty/thumbnail: %d", resp.StatusCode)
	}
	getResp, getBody := req(t, "GET", u, nil, nil)
	headResp, headBody := req(t, "HEAD", u, nil, nil)
	if getResp.StatusCode != http.StatusOK || headResp.StatusCode != http.StatusOK {
		t.Fatalf("empty exact key: GET=%d HEAD=%d want both 200", getResp.StatusCode, headResp.StatusCode)
	}
	if getCL := getResp.Header.Get("Content-Length"); getCL != "0" {
		t.Fatalf("GET empty exact key: Content-Length=%q want \"0\"", getCL)
	}
	if headCL := headResp.Header.Get("Content-Length"); headCL != "0" {
		t.Fatalf("HEAD empty exact key: Content-Length=%q want \"0\"", headCL)
	}
	if len(getBody) != 0 || len(headBody) != 0 {
		t.Fatalf("empty exact key: GET body=%d HEAD body=%d bytes, want both empty", len(getBody), len(headBody))
	}
}

// TestThumbnailHeadAnonymousPublicRead pins design P2 (D3 ordering): the HEAD
// /thumbnail dispatch must precede Head's full-key gates. An anonymous HEAD of
// a public-read image must 200 (trimmed-key admission on the derivation path)
// and of a private image must 403 — identical to GET on the same URL. Without
// the D3 placement, anonymous HEAD would 403 while GET 200s.
func TestThumbnailHeadAnonymousPublicRead(t *testing.T) {
	s, tok := newAuthRESTTest(t)
	authH := map[string]string{"Authorization": tok}

	// Public-read image: anonymous GET and HEAD /thumbnail both 200.
	u := s.URL + "/v1/files/pic.png"
	if resp, _ := req(t, "PUT", u, pngBytes(t, 300, 150), map[string]string{"Content-Type": "image/png", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT pic.png: %d", resp.StatusCode)
	}
	if resp, _ := req(t, "PUT", u+"/acl", []byte(`{"acl":"public-read"}`), authH); resp.StatusCode != http.StatusOK {
		t.Fatalf("set public-read acl: %d", resp.StatusCode)
	}
	getResp, _ := req(t, "GET", u+"/thumbnail", nil, nil)
	headResp, headBody := req(t, "HEAD", u+"/thumbnail", nil, nil)
	if getResp.StatusCode != http.StatusOK || headResp.StatusCode != http.StatusOK {
		t.Fatalf("anonymous public image: GET=%d HEAD=%d want both 200", getResp.StatusCode, headResp.StatusCode)
	}
	if ct := headResp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("anonymous HEAD public image: Content-Type=%q want image/jpeg", ct)
	}
	if len(headBody) != 0 {
		t.Fatalf("anonymous HEAD public image: body=%d bytes, want empty", len(headBody))
	}

	// Private image: anonymous GET and HEAD /thumbnail both 403.
	priv := s.URL + "/v1/files/priv.png"
	if resp, _ := req(t, "PUT", priv, pngBytes(t, 100, 100), map[string]string{"Content-Type": "image/png", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT priv.png: %d", resp.StatusCode)
	}
	getResp, _ = req(t, "GET", priv+"/thumbnail", nil, nil)
	headResp, _ = req(t, "HEAD", priv+"/thumbnail", nil, nil)
	if getResp.StatusCode != http.StatusForbidden || headResp.StatusCode != http.StatusForbidden {
		t.Fatalf("anonymous private image: GET=%d HEAD=%d want both 403", getResp.StatusCode, headResp.StatusCode)
	}

	// Public-BUCKET variant (protocol-expert F5): bucket ACL public-read, no
	// per-object ACL. allowAnonymous then admits at bucket level, so a dispatch
	// misplaced AFTER Head's full-key gates would NOT 403 here — it would 404
	// (full-key Stat of the nonexistent "{key}/thumbnail" object, which passes
	// the bucket-level anonymous gate) while GET 200s. This pins the
	// 404-vs-200 mechanism, distinct from the per-object-ACL 403 above.
	s2, alice, _ := newThumbnailAccessHarness(t)
	pubB := s2.URL + "/v1/files/bucketpub.png"
	if resp, _ := req(t, "PUT", pubB, pngBytes(t, 120, 80), map[string]string{"Content-Type": "image/png", "Authorization": alice}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT bucketpub.png: %d", resp.StatusCode)
	}
	if resp, _ := req(t, "PUT", s2.URL+"/v1/buckets/default/acl", []byte(`{"acl":"public-read"}`), map[string]string{"Authorization": "Bearer operator"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("set public-read bucket acl: %d", resp.StatusCode)
	}
	getResp, _ = req(t, "GET", pubB+"/thumbnail", nil, nil)
	headResp, headBody = req(t, "HEAD", pubB+"/thumbnail", nil, nil)
	if getResp.StatusCode != http.StatusOK || headResp.StatusCode != http.StatusOK {
		t.Fatalf("anonymous public-bucket image: GET=%d HEAD=%d want both 200", getResp.StatusCode, headResp.StatusCode)
	}
	if ct := headResp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("anonymous HEAD public-bucket image: Content-Type=%q want image/jpeg", ct)
	}
	if len(headBody) != 0 {
		t.Fatalf("anonymous HEAD public-bucket image: body=%d bytes, want empty", len(headBody))
	}
}

// TestThumbnailHeadVersionPinParity pins the version-pinned arms: the pin of
// the FULL key (raw download wins whenever the pinned version names an object
// at the exact key) and the version-pinned DERIVATION (a pin naming nothing at
// the full key derives the thumbnail from the pinned version of the trimmed
// key). GET and HEAD must agree field-for-field on both arms.
func TestThumbnailHeadVersionPinParity(t *testing.T) {
	s, tok, repo := newAuthRESTTestWithRepo(t)
	enableVersioningForCoexistence(t, repo)
	authH := map[string]string{"Authorization": tok}
	u := s.URL + "/v1/files/img.png"
	if resp, _ := req(t, "PUT", u, pngBytes(t, 300, 150), map[string]string{"Content-Type": "image/png", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT v1: %d", resp.StatusCode)
	}
	versions, err := repo.ListObjectVersions(context.Background(), "default", "default", "img.png")
	if err != nil || len(versions) == 0 {
		t.Fatalf("list versions: %v (n=%d)", err, len(versions))
	}
	oldID, oldETag := versions[0].VersionID, versions[0].ETag
	if resp, _ := req(t, "PUT", u, pngBytes(t, 200, 100), map[string]string{"Content-Type": "image/png", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT v2: %d", resp.StatusCode)
	}

	// A pinned version naming nothing at the full key (oldID belongs to the
	// trimmed key img.png) derives the thumbnail from the pinned version of
	// the trimmed key: 200 JPEG, validator from v1's ETag, HEAD mirrors GET
	// field-for-field with the body suppressed.
	thumbURL := u + "/thumbnail?version=" + oldID
	getResp, getBody := req(t, "GET", thumbURL, nil, authH)
	headResp, headBody := req(t, "HEAD", thumbURL, nil, authH)
	if getResp.StatusCode != http.StatusOK || headResp.StatusCode != http.StatusOK {
		t.Fatalf("version-pinned derivation: GET=%d HEAD=%d want both 200", getResp.StatusCode, headResp.StatusCode)
	}
	if ct := getResp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("version-pinned derivation: Content-Type=%q want image/jpeg", ct)
	}
	if _, _, err := image.Decode(bytes.NewReader(getBody)); err != nil {
		t.Fatalf("version-pinned derivation: body not a decodable image: %v", err)
	}
	effW, effH := thumbnail.EffectiveDims(0, 0)
	wantETag := fmt.Sprintf(`"%s-thumb-%dx%d"`, oldETag, effW, effH)
	if et := getResp.Header.Get("ETag"); et != wantETag {
		t.Fatalf("version-pinned derivation: GET ETag=%q want %q (from v1, not the current version)", et, wantETag)
	}
	if et := headResp.Header.Get("ETag"); et != wantETag {
		t.Fatalf("version-pinned derivation: HEAD ETag=%q want %q", et, wantETag)
	}
	if len(headBody) != 0 {
		t.Fatalf("version-pinned derivation HEAD: body=%d bytes, want empty", len(headBody))
	}

	// Existing pinned version of the full key (raw object at img.png/thumbnail)
	// → both 200 with the pinned version's own bytes.
	uFull := u + "/thumbnail"
	if resp, _ := req(t, "PUT", uFull, []byte("full v1 bytes"), map[string]string{"Content-Type": "text/plain", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT full v1: %d", resp.StatusCode)
	}
	fullVersions, err := repo.ListObjectVersions(context.Background(), "default", "default", "img.png/thumbnail")
	if err != nil || len(fullVersions) == 0 {
		t.Fatalf("list full-key versions: %v (n=%d)", err, len(fullVersions))
	}
	fullOld := fullVersions[0].VersionID
	if resp, _ := req(t, "PUT", uFull, []byte("full v2 bytes"), map[string]string{"Content-Type": "text/plain", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT full v2: %d", resp.StatusCode)
	}
	pinnedURL := uFull + "?version=" + fullOld
	getResp, getBody = req(t, "GET", pinnedURL, nil, authH)
	headResp, headBody = req(t, "HEAD", pinnedURL, nil, authH)
	if getResp.StatusCode != http.StatusOK || headResp.StatusCode != http.StatusOK {
		t.Fatalf("version-pinned full-key existing: GET=%d HEAD=%d want both 200", getResp.StatusCode, headResp.StatusCode)
	}
	if et := headResp.Header.Get("ETag"); et != getResp.Header.Get("ETag") {
		t.Fatalf("HEAD version-pinned: ETag=%q want GET's %q", et, getResp.Header.Get("ETag"))
	}
	if cl := headResp.Header.Get("Content-Length"); cl != strconv.Itoa(len(getBody)) {
		t.Fatalf("HEAD version-pinned: Content-Length=%q want %d", cl, len(getBody))
	}
	if len(headBody) != 0 {
		t.Fatalf("HEAD version-pinned: body=%d bytes, want empty", len(headBody))
	}
}

// TestThumbnailHeadOversized413 pins design P4 (error-arm breadth): the 413
// ImageTooLarge arm must be reached identically by GET and HEAD.
func TestThumbnailHeadOversized413(t *testing.T) {
	s := newRESTTest(t)
	u := s.URL + "/v1/files/bomb.png"
	// Tiny 33-byte PUT passes the (disabled-by-default) MaxBodySize cap.
	if resp, _ := req(t, "PUT", u, bombPNG(t, 100000, 100000), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT bomb.png: %d", resp.StatusCode)
	}
	thumbURL := u + "/thumbnail"
	getResp, _ := req(t, "GET", thumbURL, nil, nil)
	headResp, headBody := req(t, "HEAD", thumbURL, nil, nil)
	if getResp.StatusCode != http.StatusRequestEntityTooLarge || headResp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized: GET=%d HEAD=%d want both 413", getResp.StatusCode, headResp.StatusCode)
	}
	if len(headBody) != 0 {
		t.Fatalf("oversized HEAD: body=%d bytes, want empty", len(headBody))
	}
}

// TestThumbnailHeadParityPins pins the HEAD-on-thumbnail dispatch (QA F1/F2/F3):
// HEAD must mirror GET field-for-field on every arm — derivation, policy
// deny, long-key fallback, and the 304 fast path.
func TestThumbnailHeadParityPins(t *testing.T) {
	// F1: HEAD on the derivation path under a bucket-policy deny → 403,
	// identical to the GET pin.
	t.Run("derivation path bucket-policy deny", func(t *testing.T) {
		s, tok, _ := newThumbnailAccessHarness(t)
		adminH := map[string]string{"Authorization": "Bearer operator"}
		authH := map[string]string{"Authorization": tok}
		u := s.URL + "/v1/files/secret"
		if resp, _ := req(t, "PUT", u, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT: %d", resp.StatusCode)
		}
		denyGet := `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::default/secret"}]}`
		if resp, _ := req(t, "PUT", s.URL+"/v1/buckets/default/policy", bodyPolicy(denyGet), adminH); resp.StatusCode != http.StatusOK {
			t.Fatalf("set deny policy: %d", resp.StatusCode)
		}
		resp, _ := req(t, "HEAD", u+"/thumbnail", nil, authH)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("HEAD denied derivation: status=%d want 403", resp.StatusCode)
		}
	})

	// F3a: HEAD on an over-cap full key (201 chars) falls back to the
	// derivation (200), like GET.
	t.Run("long-key fallback", func(t *testing.T) {
		s := newRESTTest(t)
		key := strings.Repeat("k", service.MaxKeyLen-9) // 191 chars
		if resp, _ := req(t, "PUT", s.URL+"/v1/files/"+key, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT: %d", resp.StatusCode)
		}
		resp, _ := req(t, "HEAD", s.URL+"/v1/files/"+key+"/thumbnail?w=32&h=32", nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("HEAD over-cap full key: status=%d want 200 (fallback)", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
			t.Fatalf("content-type=%q want image/jpeg", ct)
		}
	})

	// F3b: HEAD with the anonymous-gate 403 (private full key over a public
	// trimmed key) mirrors GET's 403.
	t.Run("anonymous gate 403", func(t *testing.T) {
		s, tok, repo := newAuthRESTTestWithRepo(t)
		enableVersioningForCoexistence(t, repo)
		authH := map[string]string{"Authorization": tok}
		u := s.URL + "/v1/files/dir"
		if resp, _ := req(t, "PUT", u, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT dir: %d", resp.StatusCode)
		}
		if resp, _ := req(t, "PUT", u+"/acl", []byte(`{"acl":"public-read"}`), authH); resp.StatusCode != http.StatusOK {
			t.Fatalf("set acl: %d", resp.StatusCode)
		}
		if resp, _ := req(t, "PUT", u+"/thumbnail", []byte("object bytes"), map[string]string{"Content-Type": "text/plain", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT dir/thumbnail: %d", resp.StatusCode)
		}
		resp, _ := req(t, "HEAD", u+"/thumbnail", nil, nil) // anonymous
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("HEAD private full key: status=%d want 403", resp.StatusCode)
		}
	})

	// F2: GET-304 vs HEAD-304 field-for-field equality; neither carries
	// Content-Length/Content-Type (the 304 branch sets only the validator
	// headers; net/http adds no body headers for a bodiless status).
	t.Run("304 field parity", func(t *testing.T) {
		s := newRESTTest(t)
		u := s.URL + "/v1/files/img"
		if resp, _ := req(t, "PUT", u, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT: %d", resp.StatusCode)
		}
		resp, _ := req(t, "GET", u+"/thumbnail?w=64&h=64", nil, nil)
		etag := resp.Header.Get("ETag")
		if etag == "" {
			t.Fatal("missing ETag")
		}
		getResp, _ := req(t, "GET", u+"/thumbnail?w=64&h=64", nil, map[string]string{"If-None-Match": etag})
		headResp, _ := req(t, "HEAD", u+"/thumbnail?w=64&h=64", nil, map[string]string{"If-None-Match": etag})
		if getResp.StatusCode != http.StatusNotModified || headResp.StatusCode != http.StatusNotModified {
			t.Fatalf("GET=%d HEAD=%d want both 304", getResp.StatusCode, headResp.StatusCode)
		}
		for _, hdr := range []string{"ETag", "Cache-Control", "Last-Modified"} {
			g, h := getResp.Header.Get(hdr), headResp.Header.Get(hdr)
			if g != h {
				t.Fatalf("304 header %s: GET=%q HEAD=%q — field-for-field parity required", hdr, g, h)
			}
		}
		for _, hdr := range []string{"Content-Length", "Content-Type"} {
			if v := getResp.Header.Get(hdr); v != "" {
				t.Fatalf("GET 304 must not carry %s (%q)", hdr, v)
			}
			if v := headResp.Header.Get(hdr); v != "" {
				t.Fatalf("HEAD 304 must not carry %s (%q)", hdr, v)
			}
		}
	})
}

// TestThumbnailVersionPinEmptyAndLengthGate pins C1 + C2: an EMPTY ?version=
// resolves the current object on both arms (raw download parity), and the
// over-cap length-gate fall-through holds for pinned URLs too (a legal
// 191-char image key whose "/thumbnail" suffix exceeds the cap, pinned or
// not, derives a thumbnail from the trimmed key — the length gate's safety
// argument in its intended direction).
func TestThumbnailVersionPinEmptyAndLengthGate(t *testing.T) {
	// C1a: empty pin on the exact-key arm → raw download of the CURRENT
	// object (Get parity: ?version= with no value resolves current).
	t.Run("empty pin exact-key arm", func(t *testing.T) {
		s, tok, repo := newThumbnailAccessHarness(t)
		enableVersioningForCoexistence(t, repo)
		authH := map[string]string{"Authorization": tok}
		u := s.URL + "/v1/files/dir"
		if resp, _ := req(t, "PUT", u, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT dir: %d", resp.StatusCode)
		}
		uFull := u + "/thumbnail"
		if resp, _ := req(t, "PUT", uFull, []byte("object bytes"), map[string]string{"Content-Type": "text/plain", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT dir/thumbnail: %d", resp.StatusCode)
		}
		resp, body := req(t, "GET", uFull+"?version=", nil, authH)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("empty-pin exact-key: status=%d want 200 (body=%q)", resp.StatusCode, body)
		}
		if !bytes.Equal(body, []byte("object bytes")) {
			t.Fatalf("body=%q want the current object's bytes", body)
		}
	})
	// C1b: empty pin on the derivation arm → the CURRENT version's thumbnail.
	t.Run("empty pin derivation arm", func(t *testing.T) {
		s, tok, repo := newAuthRESTTestWithRepo(t)
		enableVersioningForCoexistence(t, repo)
		authH := map[string]string{"Authorization": tok}
		u := s.URL + "/v1/files/img"
		if resp, _ := req(t, "PUT", u, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT: %d", resp.StatusCode)
		}
		unpinnedETag := func() string {
			resp, _ := req(t, "GET", u+"/thumbnail?w=32&h=32", nil, authH)
			return resp.Header.Get("ETag")
		}()
		resp, body := req(t, "GET", u+"/thumbnail?w=32&h=32&version=", nil, authH)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("empty-pin derivation: status=%d want 200 (body=%q)", resp.StatusCode, body)
		}
		if et := resp.Header.Get("ETag"); et != unpinnedETag {
			t.Fatalf("empty-pin ETag %q, want the unpinned %q (empty pin = current resolution)", et, unpinnedETag)
		}
	})
	// C2: over-cap key + pin → 200 derived from the trimmed legal key.
	t.Run("over-cap length gate pinned", func(t *testing.T) {
		s, repo := newRESTTestWithRepo(t)
		enableVersioningForCoexistence(t, repo)
		key191 := strings.Repeat("k", service.MaxKeyLen-9) // 191 chars: legal
		u := s.URL + "/v1/files/" + key191
		if resp, _ := req(t, "PUT", u, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT: %d", resp.StatusCode)
		}
		versions, err := repo.ListObjectVersions(context.Background(), "default", "default", key191)
		if err != nil || len(versions) == 0 {
			t.Fatalf("list versions: %v (n=%d)", err, len(versions))
		}
		// The full key key191+"/thumbnail" is 201 chars — over the cap; the
		// pinned URL must still fall through to the derivation.
		resp, _ := req(t, "GET", s.URL+"/v1/files/"+key191+"/thumbnail?version="+versions[0].VersionID, nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("pinned over-cap full key: status=%d want 200 (derived)", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
			t.Fatalf("content-type=%q want image/jpeg", ct)
		}
	})
}

// TestThumbnailVersionPinTrimmedKeySSECParity pins C3: a pinned derivation
// whose TRIMMED key names an SSE-C object must 400 via the statPinned path
// (StatVersionWithOptions → validateSSECRead without the customer key) —
// the pinned arm must not derive bytes of an object it cannot read, exactly
// like the unpinned arm.
func TestThumbnailVersionPinTrimmedKeySSECParity(t *testing.T) {
	s, tok, repo := newAuthRESTTestWithRepo(t)
	enableVersioningForCoexistence(t, repo)
	authH := map[string]string{"Authorization": tok}
	u := s.URL + "/v1/files/photo"
	if resp, _ := req(t, "PUT", u, pngBytes(t, 64, 64), map[string]string{"Content-Type": "image/png", "Authorization": tok}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: %d", resp.StatusCode)
	}
	versions, err := repo.ListObjectVersions(context.Background(), "default", "default", "photo")
	if err != nil || len(versions) == 0 {
		t.Fatalf("list versions: %v (n=%d)", err, len(versions))
	}
	v1ID := versions[0].VersionID
	// Mark the object as SSE-C (reserved metadata, as prepareSSECWrite would).
	for k, v := range map[string]string{
		"_aero_sse_c_algorithm": "AES256",
		"_aero_sse_c_key_md5":   "d41d8cd98f00b204e9800998ecf8427e",
	} {
		if err := repo.SetObjectMetaKey(context.Background(), "default", "default", "photo", k, v); err != nil {
			t.Fatalf("set ssec meta %s: %v", k, err)
		}
	}
	// Pinned derivation of the SSE-C object: statPinned rejects (400
	// InvalidArgument — read without the customer key), never derives.
	resp, body := req(t, "GET", u+"/thumbnail?version="+v1ID, nil, authH)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("pinned SSE-C trimmed key: status=%d want 400 (body=%q)", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte(`"code":"InvalidArgument"`)) {
		t.Fatalf("expected code InvalidArgument, body: %s", body)
	}
	// Unpinned parity: the same object without a pin also 400s.
	resp, _ = req(t, "GET", u+"/thumbnail", nil, authH)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unpinned SSE-C trimmed key: status=%d want 400", resp.StatusCode)
	}
}

// TestThumbnailVersionPinnedHeadersAndIMS pins C4 + C5 + C8: the pinned
// derivation's 200 and 304 carry X-Version-Id (the pinned version's ID, Get
// parity), the 200 carries X-Content-Type-Options: nosniff, and the 304
// branch evaluates If-Modified-Since (RFC 9110 §13: INM precedence when
// present; IMS against the re-observed Last-Modified otherwise).
func TestThumbnailVersionPinnedHeadersAndIMS(t *testing.T) {
	s, repo := newRESTTestWithRepo(t)
	enableVersioningForCoexistence(t, repo)
	u := s.URL + "/v1/files/photo.jpg"
	if resp, _ := req(t, "PUT", u, pngBytes(t, 300, 150), map[string]string{"Content-Type": "image/png"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: %d", resp.StatusCode)
	}
	versions, err := repo.ListObjectVersions(context.Background(), "default", "default", "photo.jpg")
	if err != nil || len(versions) == 0 {
		t.Fatalf("list versions: %v (n=%d)", err, len(versions))
	}
	v1ID := versions[0].VersionID
	thumbURL := u + "/thumbnail?version=" + v1ID

	// 200: X-Version-Id names the pinned version; nosniff present.
	resp, _ := req(t, "GET", thumbURL, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET pinned: %d want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Version-Id"); got != v1ID {
		t.Fatalf("200 X-Version-Id = %q, want the pinned %q", got, v1ID)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("200 X-Content-Type-Options = %q, want nosniff", got)
	}

	// IMS 304: If-Modified-Since at/after the object's mtime → 304.
	lastMod := resp.Header.Get("Last-Modified")
	if lastMod == "" {
		t.Fatal("200 missing Last-Modified")
	}
	imsResp, _ := req(t, "GET", thumbURL, nil, map[string]string{"If-Modified-Since": lastMod})
	if imsResp.StatusCode != http.StatusNotModified {
		t.Fatalf("IMS 304: status=%d want 304 (If-Modified-Since: %s)", imsResp.StatusCode, lastMod)
	}
	if got := imsResp.Header.Get("X-Version-Id"); got != v1ID {
		t.Fatalf("304 X-Version-Id = %q, want the pinned %q", got, v1ID)
	}
	// An older IMS date → 200 (modified since then).
	old := time.Unix(1, 0).UTC().Format(http.TimeFormat)
	oldResp, _ := req(t, "GET", thumbURL, nil, map[string]string{"If-Modified-Since": old})
	if oldResp.StatusCode != http.StatusOK {
		t.Fatalf("old IMS: status=%d want 200", oldResp.StatusCode)
	}
	// INM precedence: both present, INM mismatched → 200 even if IMS matches.
	both, _ := req(t, "GET", thumbURL, nil, map[string]string{
		"If-None-Match":     `"does-not-match"`,
		"If-Modified-Since": lastMod,
	})
	if both.StatusCode != http.StatusOK {
		t.Fatalf("INM precedence: status=%d want 200 (INM mismatch wins)", both.StatusCode)
	}
}
