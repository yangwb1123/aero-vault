package storage

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type nonSeekReader struct {
	io.Reader
}

type s3StreamRequest struct {
	body        string
	payloadHash string
	query       string
}

func TestS3NonSeekableBodiesUseUnsignedPayload(t *testing.T) {
	var requests []s3StreamRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		requests = append(requests, s3StreamRequest{
			body:        string(body),
			payloadHash: r.Header.Get("X-Amz-Content-Sha256"),
			query:       r.URL.RawQuery,
		})
		w.Header().Set("ETag", `"stream-etag"`)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store, err := NewS3(context.Background(), S3Config{
		Endpoint:       server.URL,
		Region:         "us-east-1",
		Bucket:         "bucket",
		AccessKey:      "access",
		SecretKey:      "secret",
		ForcePathStyle: true,
	})
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}

	putBody := "ordinary streamed upload"
	info, err := store.Put(
		context.Background(),
		"default/put.txt",
		nonSeekReader{Reader: strings.NewReader(putBody)},
		int64(len(putBody)),
		PutOptions{},
	)
	if err != nil {
		t.Fatalf("Put with non-seekable reader: %v", err)
	}
	if info.ETag != "stream-etag" {
		t.Fatalf("Put ETag = %q, want stream-etag", info.ETag)
	}

	partBody := "streamed multipart part"
	part, err := store.UploadPart(
		context.Background(),
		"default/multipart.txt",
		"upload-1",
		1,
		nonSeekReader{Reader: strings.NewReader(partBody)},
		int64(len(partBody)),
	)
	if err != nil {
		t.Fatalf("UploadPart with non-seekable reader: %v", err)
	}
	if part.ETag != "stream-etag" {
		t.Fatalf("UploadPart ETag = %q, want stream-etag", part.ETag)
	}

	if len(requests) != 2 {
		t.Fatalf("received %d requests, want 2", len(requests))
	}
	assertUnsignedS3Request(t, requests[0], putBody, "")
	assertUnsignedS3Request(t, requests[1], partBody, "uploadId=upload-1")
}

func TestS3NonSeekablePutReplaysBodyOnRetry(t *testing.T) {
	const wantBody = "retry this streamed upload"
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		bodies = append(bodies, string(body))
		if len(bodies) == 1 {
			http.Error(w, "retry", http.StatusInternalServerError)
			return
		}
		w.Header().Set("ETag", `"retry-etag"`)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store, err := NewS3(context.Background(), S3Config{
		Endpoint:       server.URL,
		Region:         "us-east-1",
		Bucket:         "bucket",
		AccessKey:      "access",
		SecretKey:      "secret",
		ForcePathStyle: true,
	})
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}

	info, err := store.Put(
		context.Background(),
		"default/retry.txt",
		nonSeekReader{Reader: strings.NewReader(wantBody)},
		int64(len(wantBody)),
		PutOptions{},
	)
	if err != nil {
		t.Fatalf("Put after retry: %v", err)
	}
	if info.ETag != "retry-etag" {
		t.Fatalf("Put ETag = %q, want retry-etag", info.ETag)
	}
	if len(bodies) != 2 {
		t.Fatalf("received %d requests, want 2", len(bodies))
	}
	for i, body := range bodies {
		if body != wantBody {
			t.Fatalf("request %d body = %q, want %q", i+1, body, wantBody)
		}
	}
}

func assertUnsignedS3Request(t *testing.T, got s3StreamRequest, body, queryPart string) {
	t.Helper()
	if got.body != body {
		t.Fatalf("request body = %q, want %q", got.body, body)
	}
	if got.payloadHash != "UNSIGNED-PAYLOAD" {
		t.Fatalf("X-Amz-Content-Sha256 = %q, want UNSIGNED-PAYLOAD", got.payloadHash)
	}
	if queryPart != "" && !strings.Contains(got.query, queryPart) {
		t.Fatalf("request query = %q, want it to contain %q", got.query, queryPart)
	}
}
