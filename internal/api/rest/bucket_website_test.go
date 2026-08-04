package rest

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestBucketWebsiteCRUD(t *testing.T) {
	_, _, server := setupTest(t)
	endpoint := server.URL + "/buckets/site/website"
	input := []byte(`{
		"index_document":{"suffix":"index.html"},
		"error_document":{"key":"error.html"}
	}`)

	response, body := req(t, http.MethodPut, endpoint, input, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("PUT status=%d, want 200; body=%s", response.StatusCode, body)
	}
	response, body = req(t, http.MethodGet, endpoint, nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET status=%d, want 200; body=%s", response.StatusCode, body)
	}
	var got repository.WebsiteConfig
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.IndexDocument.Suffix != "index.html" ||
		got.ErrorDocument.Key != "error.html" {
		t.Fatalf("GET config=%+v", got)
	}

	response, body = req(t, http.MethodDelete, endpoint, nil, nil)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status=%d, want 204; body=%s", response.StatusCode, body)
	}
	response, body = req(t, http.MethodGet, endpoint, nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET after delete status=%d, want 200; body=%s", response.StatusCode, body)
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.IndexDocument.Suffix != "" || got.ErrorDocument.Key != "" {
		t.Fatalf("GET after delete config=%+v, want empty", got)
	}
}

func TestBucketWebsiteRequiresIndexDocument(t *testing.T) {
	_, _, server := setupTest(t)
	response, body := req(
		t, http.MethodPut, server.URL+"/buckets/site/website",
		[]byte(`{"error_document":{"key":"error.html"}}`), nil,
	)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT status=%d, want 400; body=%s", response.StatusCode, body)
	}
}
