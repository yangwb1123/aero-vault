package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAdminBucketWebsiteWritesRESTConfig(t *testing.T) {
	var gotPath string
	var gotBody map[string]map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := newTestClient(t, server)
	if code := client.adminBucketWebsite([]string{
		"site", "--index", "index.html", "--error", "error.html",
	}); code != 0 {
		t.Fatalf("adminBucketWebsite code=%d, want 0", code)
	}
	if gotPath != "/v1/buckets/site/website" {
		t.Fatalf("path=%q", gotPath)
	}
	if gotBody["index_document"]["suffix"] != "index.html" ||
		gotBody["error_document"]["key"] != "error.html" {
		t.Fatalf("body=%v", gotBody)
	}
}

func TestAdminBucketWebsiteReturnsFailureForHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rejected", http.StatusBadRequest)
	}))
	defer server.Close()

	client := newTestClient(t, server)
	var code int
	captureStderr(t, func() {
		code = client.adminBucketWebsite([]string{"site", "--index", "index.html"})
	})
	if code != 1 {
		t.Fatalf("adminBucketWebsite code=%d, want 1", code)
	}
}
