package s3compat

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"testing"
)

func TestContentEncodingUsesUploadedRepresentationForGetRangeAndCopy(t *testing.T) {
	server := newTestServer(t)
	encoded := gzipBytes(t, []byte("the encoded representation must remain byte-for-byte stable"))
	base := server.URL + "/bucket"

	response, body := do(t, http.MethodPut, base+"/encoded.gz", encoded, map[string]string{
		"Content-Encoding": "gzip",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("put status=%d body=%s", response.StatusCode, body)
	}

	response, body = do(t, http.MethodGet, base+"/encoded.gz", nil, map[string]string{
		"Accept-Encoding": "identity",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("get status=%d body=%s", response.StatusCode, body)
	}
	if response.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("content-encoding=%q, want gzip", response.Header.Get("Content-Encoding"))
	}
	if !bytes.Equal(body, encoded) {
		t.Fatal("GET changed the uploaded encoded representation")
	}

	response, body = do(t, http.MethodGet, base+"/encoded.gz", nil, map[string]string{
		"Accept-Encoding": "identity",
		"Range":           "bytes=2-7",
	})
	if response.StatusCode != http.StatusPartialContent {
		t.Fatalf("range status=%d body=%s", response.StatusCode, body)
	}
	if !bytes.Equal(body, encoded[2:8]) {
		t.Fatalf("range body=%v, want=%v", body, encoded[2:8])
	}

	response, body = do(t, http.MethodPut, base+"/copy.gz", nil, map[string]string{
		"x-amz-copy-source": "/bucket/encoded.gz",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("copy status=%d body=%s", response.StatusCode, body)
	}
	response, body = do(t, http.MethodGet, base+"/copy.gz", nil, map[string]string{
		"Accept-Encoding": "identity",
	})
	if response.StatusCode != http.StatusOK || !bytes.Equal(body, encoded) {
		t.Fatalf("copied encoded object status=%d body_changed=%t", response.StatusCode, !bytes.Equal(body, encoded))
	}
	if response.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("copied content-encoding=%q, want gzip", response.Header.Get("Content-Encoding"))
	}
}

func TestS3RejectsReservedUserMetadata(t *testing.T) {
	server := newTestServer(t)
	base := server.URL + "/bucket/reserved.txt"

	response, body := do(t, http.MethodPut, base, []byte("body"), map[string]string{
		"x-amz-meta-_aero_scrub_status": "corrupt",
	})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("put status=%d, want 400: %s", response.StatusCode, body)
	}
	response, _ = do(t, http.MethodGet, base, nil, nil)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("rejected object must not exist, status=%d", response.StatusCode)
	}
}

func gzipBytes(t *testing.T, body []byte) []byte {
	t.Helper()
	var encoded bytes.Buffer
	writer := gzip.NewWriter(&encoded)
	if _, err := writer.Write(body); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return encoded.Bytes()
}
