package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRedirectWebUI(t *testing.T) {
	rec := httptest.NewRecorder()
	redirectWebUI(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusFound)
	}
	if location := rec.Header().Get("Location"); location != "/ui/" {
		t.Fatalf("Location=%q want /ui/", location)
	}
}
