package aerovault

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testServer(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	c, err := New(srv.URL, WithToken("test-token"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(srv.Close)
	return c, srv
}

func TestUploadDownloadRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			w.Header().Set("ETag", `"abc123"`)
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"key":"test.txt","size":5}`)
		case http.MethodGet:
			w.Header().Set("ETag", `"abc123"`)
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Length", "5")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("hello"))
		default:
			http.Error(w, "not implemented", http.StatusNotImplemented)
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL, WithToken("tk"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Upload
	obj, err := c.Upload(context.Background(), "test.txt", strings.NewReader("hello"), UploadOptions{ContentType: "text/plain"})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if obj.Key != "test.txt" {
		t.Errorf("Key = %q", obj.Key)
	}

	// Download
	rc, obj2, err := c.Get(context.Background(), "test.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	if obj2.Size != 5 {
		t.Errorf("Size = %d", obj2.Size)
	}
	data, _ := io.ReadAll(rc)
	if string(data) != "hello" {
		t.Errorf("body = %q", string(data))
	}
}
