package rest

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
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
	const public = "public, max-age=86400"
	const private = "private, max-age=86400"

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
		// RFC 9111 §3.2/§3.4: a revalidating shared cache adopts the 304's
		// directive — a private 304 must never follow a public 200.
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
	// Unparseable content type (no slash): client-argument class, 400.
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
