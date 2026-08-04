package rest

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/service"
)

func TestGetMetadataSubresource(t *testing.T) {
	svc, _, server := setupTest(t)
	_, err := svc.Put(
		t.Context(), "", "", "docs/report.txt",
		strings.NewReader("report"), 6,
		service.PutOptions{Metadata: map[string]string{"author": "Ada"}},
	)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	resp, err := http.Get(server.URL + "/files/docs/report.txt/metadata")
	if err != nil {
		t.Fatalf("get metadata: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var metadata map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&metadata); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if metadata["author"] != "Ada" {
		t.Fatalf("metadata=%v", metadata)
	}
}
