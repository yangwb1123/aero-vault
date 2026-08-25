package integration

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
)

func putBinary(url, contentType string, body []byte) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	return http.DefaultClient.Do(req)
}

func thumbnailPNGBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.NRGBA{R: 255, A: 255})
	img.Set(1, 0, color.NRGBA{G: 255, A: 255})
	img.Set(0, 1, color.NRGBA{B: 255, A: 255})
	img.Set(1, 1, color.NRGBA{R: 255, G: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

func TestFullServer_Thumbnail(t *testing.T) {
	ts := startFullServer(t)
	key := "integration/thumbnail-smoke.png"
	putResp, err := putBinary(ts.URL+"/v1/files/"+key, "image/png", thumbnailPNGBytes(t))
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	_, _ = io.Copy(io.Discard, putResp.Body)
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK && putResp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT thumbnail source: got %d, want 200/201", putResp.StatusCode)
	}
	thumbURL := ts.URL + "/v1/files/" + key + "/thumbnail?w=32&h=32"
	first, err := http.Get(thumbURL)
	if err != nil {
		t.Fatalf("GET thumbnail: %v", err)
	}
	body, readErr := io.ReadAll(first.Body)
	first.Body.Close()
	if readErr != nil {
		t.Fatalf("GET thumbnail read: %v", readErr)
	}
	if first.StatusCode != http.StatusOK || len(body) == 0 {
		t.Fatalf("GET thumbnail: status=%d body=%d", first.StatusCode, len(body))
	}
	if ct := first.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/jpeg") {
		t.Fatalf("thumbnail Content-Type=%q, want image/jpeg", ct)
	}
	etag := first.Header.Get("ETag")
	if etag == "" {
		t.Fatal("thumbnail response missing ETag")
	}
	if first.Header.Get("Last-Modified") == "" {
		t.Fatal("thumbnail response missing Last-Modified")
	}
	get304Req, err := http.NewRequest(http.MethodGet, thumbURL, nil)
	if err != nil {
		t.Fatalf("GET revalidation request: %v", err)
	}
	get304Req.Header.Set("If-None-Match", etag)
	get304, err := http.DefaultClient.Do(get304Req)
	if err != nil {
		t.Fatalf("GET revalidation: %v", err)
	}
	get304Body, readErr := io.ReadAll(get304.Body)
	get304.Body.Close()
	if readErr != nil {
		t.Fatalf("GET revalidation read: %v", readErr)
	}
	if get304.StatusCode != http.StatusNotModified || len(get304Body) != 0 {
		t.Fatalf("GET revalidation: status=%d body=%d", get304.StatusCode, len(get304Body))
	}
	if get304.Header.Get("ETag") != etag {
		t.Fatalf("GET 304 ETag=%q, want %q", get304.Header.Get("ETag"), etag)
	}
	head304Req, err := http.NewRequest(http.MethodHead, thumbURL, nil)
	if err != nil {
		t.Fatalf("HEAD revalidation request: %v", err)
	}
	head304Req.Header.Set("If-None-Match", etag)
	head304, err := http.DefaultClient.Do(head304Req)
	if err != nil {
		t.Fatalf("HEAD revalidation: %v", err)
	}
	_, _ = io.Copy(io.Discard, head304.Body)
	head304.Body.Close()
	if head304.StatusCode != http.StatusNotModified {
		t.Fatalf("HEAD revalidation: got %d, want 304", head304.StatusCode)
	}
	if head304.Header.Get("ETag") != etag {
		t.Fatalf("HEAD 304 ETag=%q, want %q", head304.Header.Get("ETag"), etag)
	}
}
