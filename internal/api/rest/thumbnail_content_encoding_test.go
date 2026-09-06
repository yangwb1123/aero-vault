package rest

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"image"
	"net/http"
	"testing"

	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

func gzipThumbnailSource(t *testing.T, source []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	if _, err := zw.Write(source); err != nil {
		t.Fatalf("gzip source: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close gzip source: %v", err)
	}
	return out.Bytes()
}

func TestThumbnailContentEncoding(t *testing.T) {
	srv, store, _, _ := newThumbnailCacheREST(t, true, 0)
	source := gzipThumbnailSource(t, jpegBytesForThumbnailTest(t, 32, 16))
	for i, tc := range []struct {
		contentType string
		encoding    string
	}{
		{"image/jpeg", "gzip"},
		{"application/octet-stream", "gzip"},
		{"image/jpeg", "identity"},
		{"image/jpeg", "gzip, br"},
		{"text/plain", "gzip"},
		{"image/webp", "gzip"},
	} {
		pre := snapshotThumbCounters(t)
		u := srv.URL + "/v1/files/encoded-" + string(rune('a'+i))
		resp, _ := req(t, http.MethodPut, u, source, map[string]string{
			"Content-Type": tc.contentType, "Content-Encoding": tc.encoding,
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT %s: status=%d", tc.encoding, resp.StatusCode)
		}
		resp, body := req(t, http.MethodGet, u+"/thumbnail?w=invalid&h=-1", nil,
			map[string]string{"If-None-Match": "*"})
		if resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Fatalf("GET %s: status=%d want 415 (body=%s)", tc.encoding, resp.StatusCode, body)
		}
		if !bytes.Contains(body, []byte(`"code":"UnsupportedMediaType"`)) ||
			!bytes.Contains(body, []byte(tc.encoding)) {
			t.Fatalf("GET %s: body=%s must name the encoding and 415 code", tc.encoding, body)
		}
		for _, header := range []string{"ETag", "Last-Modified", "X-Version-Id", "Cache-Control", "Content-Encoding"} {
			if value := resp.Header.Get(header); value != "" {
				t.Errorf("GET %s: %s=%q must be absent", tc.encoding, header, value)
			}
		}
		if got := store.gets.Load(); got != 0 {
			t.Fatalf("GET %s opened storage %d times; encoded sources must stop before Get", tc.encoding, got)
		}
		assertThumbDeltas(t, pre, "content_encoding", 0)

		headPre := snapshotThumbCounters(t)
		headResp, headBody := req(t, http.MethodHead, u+"/thumbnail", nil, map[string]string{
			"Accept-Encoding": "gzip", "If-Modified-Since": "Wed, 21 Oct 2015 07:28:00 GMT",
		})
		if headResp.StatusCode != http.StatusUnsupportedMediaType || len(headBody) != 0 {
			t.Fatalf("HEAD %s: status=%d body=%d want 415 and no body", tc.encoding, headResp.StatusCode, len(headBody))
		}
		assertThumbDeltas(t, headPre, "content_encoding", 0)
	}
}

func TestUnsupportedThumbnailContentEncodingClassification(t *testing.T) {
	err := unsupportedThumbnailContentEncodingError("gzip")
	if !errors.Is(err, thumbnail.ErrUnsupportedContentEncoding) {
		t.Fatal("content-encoding error does not retain its sentinel")
	}
	if errors.Is(err, thumbnail.ErrUnsupportedFormat) {
		t.Fatal("content-encoding error must be distinct from unsupported format")
	}
	code, message, status := classify(err)
	if code != "UnsupportedMediaType" || status != http.StatusUnsupportedMediaType || !bytes.Contains([]byte(message), []byte("gzip")) {
		t.Fatalf("classify content encoding = (%q, %q, %d), want 415 with gzip", code, message, status)
	}
	reason, silent := thumbnailRejectionReason(err)
	if reason != "content_encoding" || silent {
		t.Fatalf("rejection reason = (%q, %v), want content_encoding/false", reason, silent)
	}
}

func TestThumbnailContentEncodingPinnedMetadata(t *testing.T) {
	srv, repo := newRESTTestWithRepo(t)
	enableVersioningForCoexistence(t, repo)
	u := srv.URL + "/v1/files/pinned.jpg"
	jpeg := jpegBytesForThumbnailTest(t, 32, 16)
	if resp, _ := req(t, http.MethodPut, u, jpeg, map[string]string{"Content-Type": "image/jpeg"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT unencoded version: status=%d", resp.StatusCode)
	}
	versions, err := repo.ListObjectVersions(context.Background(), "default", "default", "pinned.jpg")
	if err != nil || len(versions) == 0 {
		t.Fatalf("list first version: %v", err)
	}
	unencodedVersion := versions[0].VersionID
	encoded := gzipThumbnailSource(t, jpeg)
	if resp, _ := req(t, http.MethodPut, u, encoded, map[string]string{
		"Content-Type": "image/jpeg", "Content-Encoding": "gzip",
	}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT encoded version: status=%d", resp.StatusCode)
	}
	versions, err = repo.ListObjectVersions(context.Background(), "default", "default", "pinned.jpg")
	if err != nil || len(versions) < 2 {
		t.Fatalf("list second version: %v (n=%d)", err, len(versions))
	}
	encodedVersion := versions[0].VersionID

	resp, body := req(t, http.MethodGet, u+"/thumbnail", nil, nil)
	if resp.StatusCode != http.StatusUnsupportedMediaType || !bytes.Contains(body, []byte("gzip")) {
		t.Fatalf("current encoded version: status=%d body=%s", resp.StatusCode, body)
	}
	resp, body = req(t, http.MethodGet, u+"/thumbnail?version="+unencodedVersion, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pinned unencoded version: status=%d body=%s", resp.StatusCode, body)
	}
	if _, format, err := image.Decode(bytes.NewReader(body)); err != nil || format != "jpeg" {
		t.Fatalf("pinned unencoded body: format=%q err=%v", format, err)
	}
	if got := resp.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("pinned unencoded derived response Content-Encoding=%q", got)
	}
	resp, body = req(t, http.MethodGet, u+"/thumbnail?version="+encodedVersion, nil, nil)
	if resp.StatusCode != http.StatusUnsupportedMediaType || !bytes.Contains(body, []byte("gzip")) {
		t.Fatalf("pinned encoded version: status=%d body=%s", resp.StatusCode, body)
	}
}
