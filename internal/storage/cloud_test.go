package storage

import (
	"errors"
	"net/http"
	"testing"
)

func TestS3NotFound(t *testing.T) {
	if isS3NotFound(nil) {
		t.Error("isS3NotFound(nil) should be false")
	}
	if isS3NotFound(errors.New("random error")) {
		t.Error("isS3NotFound for random error should be false")
	}
}

func TestOSSNotFound(t *testing.T) {
	if isOSSNotFound(nil) {
		t.Error("isOSSNotFound(nil) should be false")
	}
	if isOSSNotFound(errors.New("random error")) {
		t.Error("isOSSNotFound for random error should be false")
	}
}

func TestCOSNotFound(t *testing.T) {
	if isCOSNotFound(nil) {
		t.Error("isCOSNotFound(nil) should be false")
	}
	if isCOSNotFound(errors.New("random error")) {
		t.Error("isCOSNotFound for random error should be false")
	}
}

func TestS3Backend(t *testing.T) {
	s := &S3Storage{}
	if got := s.Backend(); got != "s3" {
		t.Errorf("Backend() = %q, want s3", got)
	}
}

func TestOSSBackend(t *testing.T) {
	s := &OSSStorage{}
	if got := s.Backend(); got != "oss" {
		t.Errorf("Backend() = %q, want oss", got)
	}
}

func TestCOSBackend(t *testing.T) {
	s := &COSStorage{}
	if got := s.Backend(); got != "cos" {
		t.Errorf("Backend() = %q, want cos", got)
	}
}

func TestCOSObjectInfo(t *testing.T) {
	info := cosObjectInfo("test-key", nil)
	if info.Key != "test-key" {
		t.Errorf("cosObjectInfo key = %q", info.Key)
	}
}

func TestOSSObjectInfo(t *testing.T) {
	info := ossObjectInfo("test-key", nil)
	if info.Key != "test-key" {
		t.Errorf("ossObjectInfo key = %q", info.Key)
	}
}

func TestOSSObjectInfo_WithHeaders(t *testing.T) {
	h := make(http.Header)
	h.Set("ETag", `"abc123"`)
	h.Set("Content-Type", "text/plain")
	h.Set("Content-Length", "42")
	h.Set("Last-Modified", "Mon, 02 Jan 2006 15:04:05 GMT")
	h.Set("X-Oss-Meta-color", "blue")
	h.Set("X-Oss-Meta-size", "large")

	info := ossObjectInfo("test/obj.txt", h)
	if info.ETag != "abc123" {
		t.Errorf("ETag = %q, want abc123", info.ETag)
	}
	if info.ContentType != "text/plain" {
		t.Errorf("ContentType = %q, want text/plain", info.ContentType)
	}
	if info.Size != 42 {
		t.Errorf("Size = %d, want 42", info.Size)
	}
	if info.LastModified.IsZero() {
		t.Error("LastModified should not be zero")
	}
	if info.Metadata["color"] != "blue" {
		t.Errorf("Metadata[color] = %q, want blue", info.Metadata["color"])
	}
	if info.Metadata["size"] != "large" {
		t.Errorf("Metadata[size] = %q, want large", info.Metadata["size"])
	}
}

func TestCOSObjectInfo_WithHeaders(t *testing.T) {
	h := make(http.Header)
	h.Set("ETag", `"def456"`)
	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", "99")
	h.Set("Last-Modified", "Sun, 01 Jan 2006 12:00:00 GMT")
	h.Set("X-Cos-Meta-project", "aero")

	info := cosObjectInfo("cos/obj.bin", h)
	if info.ETag != "def456" {
		t.Errorf("ETag = %q, want def456", info.ETag)
	}
	if info.ContentType != "application/json" {
		t.Errorf("ContentType = %q, want application/json", info.ContentType)
	}
	if info.Size != 99 {
		t.Errorf("Size = %d, want 99", info.Size)
	}
	if info.LastModified.IsZero() {
		t.Error("LastModified should not be zero")
	}
	if info.Metadata["project"] != "aero" {
		t.Errorf("Metadata[project] = %q, want aero", info.Metadata["project"])
	}
}

func TestOSSServiceError(t *testing.T) {
	if isOSSNotFound(errors.New("random")) {
		t.Error("isOSSNotFound should be false for random error")
	}
}

func TestCOSServiceError(t *testing.T) {
	if isCOSNotFound(errors.New("random")) {
		t.Error("isCOSNotFound should be false for random error")
	}
}

func TestOSSImur(t *testing.T) {
	s := &OSSStorage{cfg: OSSConfig{Bucket: "my-bucket"}}
	res := s.imur("test-key", "upload-123")
	if res.Key != "test-key" || res.UploadID != "upload-123" || res.Bucket != "my-bucket" {
		t.Errorf("imur returned unexpected result: %+v", res)
	}
}
