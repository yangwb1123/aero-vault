package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

func TestLegalHoldGetUsesExactVersion(t *testing.T) {
	svc, _, ts := setupTest(t)
	ctx := context.Background()
	if err := svc.SetBucketVersioning(ctx, "", "", true); err != nil {
		t.Fatal(err)
	}
	first := putRESTTestObject(t, svc, "legal.txt", "first")
	second := putRESTTestObject(t, svc, "legal.txt", "second")
	if err := svc.PutLegalHold(
		ctx, "", "", "legal.txt", second.VersionID, "case", "tester",
	); err != nil {
		t.Fatal(err)
	}

	assertLegalHoldVersion(t, ts.URL, "legal.txt", second.VersionID, http.StatusOK)
	assertLegalHoldVersion(t, ts.URL, "legal.txt", first.VersionID, http.StatusNotFound)
	assertLegalHoldVersion(t, ts.URL, "legal.txt", "missing-version", http.StatusNotFound)
}

func TestRemoveLegalHoldReturnsNoContent(t *testing.T) {
	svc, _, ts := setupTest(t)
	obj := putRESTTestObject(t, svc, "remove-hold.txt", "body")
	if err := svc.PutLegalHold(
		context.Background(), "", "", obj.Key, obj.VersionID, "case", "tester",
	); err != nil {
		t.Fatal(err)
	}

	endpoint := ts.URL + "/legal-hold?key=" + url.QueryEscape(obj.Key) +
		"&versionId=" + url.QueryEscape(obj.VersionID)
	resp, body := req(t, http.MethodDelete, endpoint, nil, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status=%d, want 204; body=%s", resp.StatusCode, body)
	}
	if len(body) != 0 {
		t.Fatalf("DELETE body=%q, want empty", body)
	}
}

func putRESTTestObject(
	t *testing.T, svc *service.FileService, key, body string,
) repository.Object {
	t.Helper()
	obj, err := svc.Put(
		context.Background(), "", "", key, strings.NewReader(body), int64(len(body)),
		service.PutOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	return obj
}

func assertLegalHoldVersion(t *testing.T, base, key, versionID string, wantStatus int) {
	t.Helper()
	endpoint := base + "/legal-hold?key=" + url.QueryEscape(key) +
		"&versionId=" + url.QueryEscape(versionID)
	resp, body := req(t, http.MethodGet, endpoint, nil, nil)
	if resp.StatusCode != wantStatus {
		t.Fatalf("GET version %q status=%d, want %d; body=%s",
			versionID, resp.StatusCode, wantStatus, body)
	}
	if wantStatus != http.StatusOK {
		return
	}
	var got LegalHoldResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.VersionID != versionID {
		t.Fatalf("GET version_id=%q, want %q", got.VersionID, versionID)
	}
}
