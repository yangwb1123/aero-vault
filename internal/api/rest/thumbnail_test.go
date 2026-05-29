package rest

import (
	"bytes"
	"image"
	"image/color"
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
