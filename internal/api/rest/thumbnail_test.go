package rest

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
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
