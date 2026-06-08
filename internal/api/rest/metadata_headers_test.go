package rest

import (
	"net/http"
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
