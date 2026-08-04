package rest

import (
	"net/http/httptest"
	"testing"
)

func TestRestObjectTargetUsesExternalHTTPS(t *testing.T) {
	req := httptest.NewRequest("POST", "http://internal:8080/v1/files/a/presign", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	target, err := (&Handler{}).restObjectTarget(req, "folder/a file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if target != "https://internal:8080/v1/files/folder/a%20file.txt" {
		t.Fatalf("forwarded target=%q", target)
	}

	h := &Handler{publicBaseURL: "https://source.example/proxy"}
	target, err = h.restObjectTarget(req, "folder/a file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if target != "https://source.example/v1/files/folder/a%20file.txt" {
		t.Fatalf("canonical target=%q", target)
	}
}
