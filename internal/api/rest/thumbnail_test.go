package rest

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"testing"
)

func pngBytes(t *testing.T, w, h int) []byte {
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

func TestThumbnailOversizedMetadata(t *testing.T) {
	// A JPEG with more pre-SOF metadata than the module's budget must map to
	// HTTP 400 InvalidArgument through the generic error path — never 413
	// (that is the dimension class) and never 500 (the raw sentinel has no
	// classify case; the handler's ErrInvalidArgs wrap is what pins this).
	s := newRESTTest(t)
	u := s.URL + "/v1/files/meta.jpg"
	req(t, "PUT", u, appnPaddedJPEG(t, 9<<20), map[string]string{"Content-Type": "image/jpeg"})
	resp, body := req(t, "GET", u+"/thumbnail", nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("thumbnail with oversized metadata: status=%d want 400", resp.StatusCode)
	}
	if !bytes.Contains(body, []byte(`"code":"InvalidArgument"`)) {
		t.Fatalf("expected code InvalidArgument, body: %s", body)
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
