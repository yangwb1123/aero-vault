package rest

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// User metadata set on PUT (via X-Meta-* or X-Amz-Meta-*) must be returned on
// GET and HEAD as X-Meta-* headers — previously it was write-only.
func TestMetadataRoundTripsThroughGetHead(t *testing.T) {
	s := newRESTTest(t)
	u := s.URL + "/v1/files/meta/obj.txt"

	resp, _ := req(t, "PUT", u, []byte("hello"), map[string]string{
		"X-Meta-Color":     "blue",
		"X-Amz-Meta-Shape": "round",
	})
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("put status %d", resp.StatusCode)
	}

	for _, m := range []string{"GET", "HEAD"} {
		r, _ := req(t, m, u, nil, nil)
		if got := r.Header.Get("X-Meta-Color"); got != "blue" {
			t.Fatalf("%s X-Meta-Color = %q, want blue", m, got)
		}
		if got := r.Header.Get("X-Meta-Shape"); got != "round" {
			t.Fatalf("%s X-Meta-Shape = %q, want round (from x-amz-meta-)", m, got)
		}
	}
}

func TestMetadataHeadersHideInternalFields(t *testing.T) {
	w := httptest.NewRecorder()
	writeMetadataHeaders(w, map[string]string{
		"author":       "Ada",
		"_aero_owner":  "subject-1",
		"_AeRo_secret": "hidden",
	})

	if got := w.Header().Get("X-Meta-Author"); got != "Ada" {
		t.Fatalf("X-Meta-Author = %q, want Ada", got)
	}
	for _, key := range []string{"X-Meta-_aero_owner", "X-Meta-_AeRo_secret"} {
		if got := w.Header().Get(key); got != "" {
			t.Errorf("internal metadata leaked through %s: %q", key, got)
		}
	}
}
