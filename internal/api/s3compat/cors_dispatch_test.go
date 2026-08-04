package s3compat

import (
	"encoding/xml"
	"net/http"
	"testing"
)

func TestDeleteBucketCORSDoesNotDeleteBucket(t *testing.T) {
	server := newTestServer(t)
	base := server.URL
	do(t, http.MethodPut, base+"/cors-bucket", nil, nil)

	body, err := xml.Marshal(corsInput{Rules: []corsRule{{
		AllowedOrigins: []string{"https://example.com"},
		AllowedMethods: []string{http.MethodGet},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	response, responseBody := do(t, http.MethodPut, base+"/cors-bucket?cors", body, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("put CORS status=%d body=%s", response.StatusCode, responseBody)
	}

	response, responseBody = do(t, http.MethodDelete, base+"/cors-bucket?cors", nil, nil)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete CORS status=%d body=%s", response.StatusCode, responseBody)
	}
	response, responseBody = do(t, http.MethodHead, base+"/cors-bucket", nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("bucket was deleted by DELETE ?cors: status=%d body=%s", response.StatusCode, responseBody)
	}
}
