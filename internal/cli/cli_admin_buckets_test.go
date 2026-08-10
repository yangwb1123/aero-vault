package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

// --------------------------------------------------------------------------
// FR-1/FR-2 — non-numeric and negative bucket quota/lifecycle args are
// rejected with exit code 2 before any HTTP request (AC-1/AC-2). Test names
// follow the mandated TestAdminBucketQuota/TestAdminBucketLifecycle prefixes
// so the AC-1 acceptance filter is non-vacuous.
// --------------------------------------------------------------------------

func TestAdminBucketQuota_NonNumeric_Returns2(t *testing.T) {
	var hit int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit++
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	out := captureStderr(t, func() {
		if code := c.cmdAdminBuckets("quota", []string{"b", "abc", "xyz"}); code != 2 {
			t.Errorf("bucket quota non-numeric = %d; want 2", code)
		}
	})
	if hit != 0 {
		t.Errorf("server received %d requests; want 0", hit)
	}
	if !strings.Contains(out, "max_bytes") {
		t.Errorf("stderr %q missing max_bytes role name", out)
	}
	if !strings.Contains(out, "usage:") {
		t.Errorf("stderr %q missing usage line", out)
	}
}

func TestAdminBucketQuota_Negative_Returns2(t *testing.T) {
	var hit int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit++
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	captureStderr(t, func() {
		if code := c.cmdAdminBuckets("quota", []string{"b", "-5", "10"}); code != 2 {
			t.Errorf("bucket quota negative = %d; want 2", code)
		}
	})
	if hit != 0 {
		t.Errorf("server received %d requests; want 0", hit)
	}
}

func TestAdminBucketQuota_Valid_Body(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)
		fmt.Fprint(w, `{"bucket":"b"}`)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	captureStdout(t, func() {
		if code := c.cmdAdminBuckets("quota", []string{"b", "1048576", "1000"}); code != 0 {
			t.Errorf("bucket quota valid = %d; want 0", code)
		}
	})
	if gotMethod != "PUT" {
		t.Errorf("method = %q; want PUT", gotMethod)
	}
	if gotPath != "/v1/admin/buckets/b/quota" {
		t.Errorf("path = %q; want /v1/admin/buckets/b/quota", gotPath)
	}
	if mb, _ := gotBody["max_bytes"].(float64); mb != 1048576 {
		t.Errorf("max_bytes = %v; want 1048576", gotBody["max_bytes"])
	}
	if mo, _ := gotBody["max_objects"].(float64); mo != 1000 {
		t.Errorf("max_objects = %v; want 1000", gotBody["max_objects"])
	}
}

func TestAdminBucketLifecycle_NonNumeric_Returns2(t *testing.T) {
	var hit int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit++
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	out := captureStderr(t, func() {
		if code := c.cmdAdminBuckets("lifecycle", []string{"b", "abc"}); code != 2 {
			t.Errorf("bucket lifecycle non-numeric = %d; want 2", code)
		}
	})
	if hit != 0 {
		t.Errorf("server received %d requests; want 0", hit)
	}
	if !strings.Contains(out, "days") {
		t.Errorf("stderr %q missing days role name", out)
	}
	if !strings.Contains(out, "usage:") {
		t.Errorf("stderr %q missing usage line", out)
	}
}

func TestAdminBucketLifecycle_Negative_Returns2(t *testing.T) {
	var hit int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit++
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	captureStderr(t, func() {
		if code := c.cmdAdminBuckets("lifecycle", []string{"b", "-1"}); code != 2 {
			t.Errorf("bucket lifecycle negative = %d; want 2", code)
		}
	})
	if hit != 0 {
		t.Errorf("server received %d requests; want 0", hit)
	}
}

func TestAdminBucketLifecycle_Valid_Body(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)
		fmt.Fprint(w, `{"bucket":"b"}`)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	captureStdout(t, func() {
		if code := c.cmdAdminBuckets("lifecycle", []string{"b", "30", "--action", "hard_delete"}); code != 0 {
			t.Errorf("bucket lifecycle valid = %d; want 0", code)
		}
	})
	if gotMethod != "PUT" {
		t.Errorf("method = %q; want PUT", gotMethod)
	}
	if gotPath != "/v1/buckets/b/lifecycle" {
		t.Errorf("path = %q; want /v1/buckets/b/lifecycle", gotPath)
	}
	if d, _ := gotBody["days"].(float64); d != 30 {
		t.Errorf("days = %v; want 30", gotBody["days"])
	}
	if a, _ := gotBody["action"].(string); a != "hard_delete" {
		t.Errorf("action = %v; want hard_delete", gotBody["action"])
	}
}
