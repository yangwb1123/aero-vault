package rest

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/auth"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

func newEnterpriseRESTTest(t *testing.T) (*httptest.Server, string, string, repository.Repository) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(dir, "enterprise.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatal(err)
	}
	accessStore, ok := repo.(access.Store)
	if !ok {
		t.Fatal("repository does not implement access.Store")
	}
	manager, err := access.NewManager(accessStore, access.Config{
		Enabled: true, DefaultPolicy: access.DefaultTenant,
		ShareSecret: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := service.NewFileService(store, repo, nil).WithDeleteFailOpen(true).
		WithAuthorizer(manager).
		WithTenantStatusEnforcement()
	reg, err := auth.Parse("alice:default:read+write+vault.file.delete,operator:*:admin")
	if err != nil {
		t.Fatal(err)
	}
	reg.WithPutPresigner(auth.NewPutPresigner("enterprise-presign-secret-32-bytes"))
	v1 := NewRouter(
		svc, repo, nil, nil, nil, nil, reg, slog.Default(), false,
		nil, nil, 0, false,
		func(handler *Handler) { handler.WithAccessManager(manager, "") },
	)
	root := chi.NewRouter()
	root.Mount("/v1", v1)
	public := NewPublicAccessHandler(svc, manager, nil)
	root.Get("/share/{token}", public.Share)
	root.Head("/share/{token}", public.Share)
	root.Get("/public/assets/*", public.Asset)
	root.Head("/public/assets/*", public.Asset)
	tenantMW := mw.TenantWithStatus(func(ctx context.Context, tenant string) (string, bool, error) {
		record, found, lookupErr := repo.GetTenant(ctx, tenant)
		return record.Status, found, lookupErr
	})
	handler := reg.Middleware()(tenantMW(root))
	server := httptest.NewServer(handler)
	t.Cleanup(func() { server.Close(); _ = repo.Close() })
	return server, "Bearer alice", "Bearer operator", repo
}

func TestEnterpriseImageSharePublishAndExport(t *testing.T) {
	server, alice, operator, _ := newEnterpriseRESTTest(t)
	imageURL := server.URL + "/v1/files/blog/hero.jpg"
	response, body := req(t, http.MethodPut, imageURL, []byte("jpeg-image"), map[string]string{
		"Authorization": alice, "Content-Type": "image/jpeg",
	})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("upload status=%d body=%s", response.StatusCode, body)
	}

	response, body = req(t, http.MethodPost, server.URL+"/v1/shares", []byte(`{
		"key":"blog/hero.jpg","name":"review","allow_preview":true,
		"allow_download":true,"password":"secret","max_uses":2
	}`), map[string]string{"Authorization": alice, "Content-Type": "application/json"})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("share status=%d body=%s", response.StatusCode, body)
	}
	var shareResponse struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &shareResponse); err != nil || shareResponse.URL == "" {
		t.Fatalf("share response=%s err=%v", body, err)
	}
	response, body = req(t, http.MethodGet, shareResponse.URL, nil, map[string]string{
		"X-Aero-Share-Password": "secret", "Range": "bytes=5-9",
	})
	if response.StatusCode != http.StatusPartialContent || string(body) != "image" {
		t.Fatalf("share range status=%d body=%q", response.StatusCode, body)
	}

	response, body = req(t, http.MethodPost, server.URL+"/v1/assets", []byte(`{
		"key":"blog/hero.jpg","slug":"blog/hero.jpg","cache_control":"public, max-age=86400"
	}`), map[string]string{"Authorization": alice, "Content-Type": "application/json"})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("publish status=%d body=%s", response.StatusCode, body)
	}
	var assetResponse struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &assetResponse); err != nil || assetResponse.URL == "" {
		t.Fatalf("asset response=%s err=%v", body, err)
	}
	response, body = req(t, http.MethodGet, assetResponse.URL, nil, map[string]string{"Range": "bytes=0-3"})
	if response.StatusCode != http.StatusPartialContent || string(body) != "jpeg" {
		t.Fatalf("asset range status=%d body=%q", response.StatusCode, body)
	}
	if response.Header.Get("Cache-Control") != "public, max-age=86400" {
		t.Fatalf("asset cache-control=%q", response.Header.Get("Cache-Control"))
	}

	response, body = req(t, http.MethodGet, server.URL+"/v1/exports/archive?prefix=blog/", nil,
		map[string]string{"Authorization": alice})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("export status=%d body=%s", response.StatusCode, body)
	}
	assertPortableArchive(t, body, "blog/hero.jpg", "jpeg-image")

	response, body = req(t, http.MethodPost, server.URL+"/v1/admin/departments",
		[]byte(`{"name":"engineering"}`), map[string]string{
			"Authorization": operator, "Content-Type": "application/json",
		})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create department status=%d body=%s", response.StatusCode, body)
	}
}

func TestInvalidShareRangeDoesNotConsumeUse(t *testing.T) {
	server, alice, _, _ := newEnterpriseRESTTest(t)
	objectURL := server.URL + "/v1/files/review/limited.jpg"
	response, body := req(t, http.MethodPut, objectURL, []byte("image"), map[string]string{
		"Authorization": alice, "Content-Type": "image/jpeg",
	})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("upload status=%d body=%s", response.StatusCode, body)
	}
	shareURL := createLimitedShare(t, server.URL, alice, "review/limited.jpg")

	response, _ = req(t, http.MethodGet, shareURL, nil, map[string]string{"Range": "bytes=99-100"})
	if response.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("invalid range status=%d, want 416", response.StatusCode)
	}
	response, body = req(t, http.MethodGet, shareURL, nil, nil)
	if response.StatusCode != http.StatusOK || string(body) != "image" {
		t.Fatalf("first valid read status=%d body=%q", response.StatusCode, body)
	}
	response, _ = req(t, http.MethodGet, shareURL, nil, nil)
	if response.StatusCode != http.StatusGone {
		t.Fatalf("exhausted share status=%d, want 410", response.StatusCode)
	}
}

func TestDeletedObjectDoesNotReactivatePublicCapabilities(t *testing.T) {
	server, alice, _, _ := newEnterpriseRESTTest(t)
	objectURL := server.URL + "/v1/files/public/lifecycle.jpg"
	response, body := req(t, http.MethodPut, objectURL, []byte("old-image"), map[string]string{
		"Authorization": alice, "Content-Type": "image/jpeg",
	})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("upload status=%d body=%s", response.StatusCode, body)
	}
	shareURL := createLimitedShare(t, server.URL, alice, "public/lifecycle.jpg")
	response, body = req(t, http.MethodPost, server.URL+"/v1/assets", []byte(`{
		"key":"public/lifecycle.jpg","slug":"public/lifecycle.jpg"
	}`), map[string]string{"Authorization": alice, "Content-Type": "application/json"})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("publish status=%d body=%s", response.StatusCode, body)
	}
	var published struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &published); err != nil || published.URL == "" {
		t.Fatalf("asset response=%s err=%v", body, err)
	}
	response, _ = req(t, http.MethodDelete, objectURL, nil, map[string]string{"Authorization": alice})
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("soft delete status=%d", response.StatusCode)
	}
	response, _ = req(t, http.MethodPut, objectURL, []byte("new-image"), map[string]string{
		"Authorization": alice, "Content-Type": "image/jpeg",
	})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("re-upload status=%d", response.StatusCode)
	}
	for name, publicURL := range map[string]string{"share": shareURL, "asset": published.URL} {
		response, body = req(t, http.MethodGet, publicURL, nil, nil)
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("stale %s status=%d body=%s", name, response.StatusCode, body)
		}
	}
}

func TestDisabledTenantBlocksAuthenticatedAndPublicReads(t *testing.T) {
	server, alice, _, repo := newEnterpriseRESTTest(t)
	objectURL := server.URL + "/v1/files/blog/disabled.jpg"
	response, body := req(t, http.MethodPut, objectURL, []byte("image"), map[string]string{
		"Authorization": alice, "Content-Type": "image/jpeg",
	})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("upload status=%d body=%s", response.StatusCode, body)
	}
	shareURL := createShare(t, server.URL, alice, "blog/disabled.jpg")
	assetURL := createAsset(t, server.URL, alice, "blog/disabled.jpg", "blog/disabled.jpg")
	response, body = req(t, http.MethodPost, objectURL+"/presign?op=get&expires=60", nil,
		map[string]string{"Authorization": alice})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("presign status=%d body=%s", response.StatusCode, body)
	}
	var presigned struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &presigned); err != nil || presigned.URL == "" {
		t.Fatalf("presign response=%s err=%v", body, err)
	}
	if err := repo.UpsertTenant(context.Background(), repository.TenantRecord{
		TenantID: "default", DisplayName: "Default", Status: "disabled",
	}); err != nil {
		t.Fatal(err)
	}
	for name, testCase := range map[string]struct {
		url     string
		headers map[string]string
	}{
		"authenticated": {url: objectURL, headers: map[string]string{"Authorization": alice}},
		"presigned":     {url: presigned.URL},
		"share":         {url: shareURL},
		"asset":         {url: assetURL},
	} {
		response, body = req(t, http.MethodGet, testCase.url, nil, testCase.headers)
		if response.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "TenantDisabled") {
			t.Fatalf("%s status=%d body=%s", name, response.StatusCode, body)
		}
	}
	if err := repo.UpsertTenant(context.Background(), repository.TenantRecord{
		TenantID: "default", DisplayName: "Default", Status: "active",
	}); err != nil {
		t.Fatal(err)
	}
	response, body = req(t, http.MethodGet, shareURL, nil, nil)
	if response.StatusCode != http.StatusOK || string(body) != "image" {
		t.Fatalf("reactivated share status=%d body=%q", response.StatusCode, body)
	}
	response, body = req(t, http.MethodGet, presigned.URL, nil, nil)
	if response.StatusCode != http.StatusOK || string(body) != "image" {
		t.Fatalf("reactivated presign status=%d body=%q", response.StatusCode, body)
	}
}

func createShare(t *testing.T, baseURL, authorization, key string) string {
	t.Helper()
	response, body := req(t, http.MethodPost, baseURL+"/v1/shares", []byte(`{
		"key":"`+key+`","allow_preview":true
	}`), map[string]string{"Authorization": authorization, "Content-Type": "application/json"})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("share status=%d body=%s", response.StatusCode, body)
	}
	var created struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.URL == "" {
		t.Fatalf("share response=%s err=%v", body, err)
	}
	return created.URL
}

func createAsset(t *testing.T, baseURL, authorization, key, slug string) string {
	t.Helper()
	response, body := req(t, http.MethodPost, baseURL+"/v1/assets", []byte(`{
		"key":"`+key+`","slug":"`+slug+`"
	}`), map[string]string{"Authorization": authorization, "Content-Type": "application/json"})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("asset status=%d body=%s", response.StatusCode, body)
	}
	var created struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.URL == "" {
		t.Fatalf("asset response=%s err=%v", body, err)
	}
	return created.URL
}

func createLimitedShare(t *testing.T, baseURL, authorization, key string) string {
	t.Helper()
	response, body := req(t, http.MethodPost, baseURL+"/v1/shares", []byte(`{
		"key":"`+key+`","allow_preview":true,"max_uses":1
	}`), map[string]string{"Authorization": authorization, "Content-Type": "application/json"})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("share status=%d body=%s", response.StatusCode, body)
	}
	var created struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.URL == "" {
		t.Fatalf("share response=%s err=%v", body, err)
	}
	return created.URL
}

func assertPortableArchive(t *testing.T, payload []byte, wantedKey, wantedBody string) {
	t.Helper()
	gzipReader, err := gzip.NewReader(strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	defer gzipReader.Close()
	tape := tar.NewReader(gzipReader)
	foundManifest, foundObject := false, false
	for {
		header, err := tape.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		contents, err := io.ReadAll(tape)
		if err != nil {
			t.Fatal(err)
		}
		switch header.Name {
		case "manifest.json":
			foundManifest = strings.Contains(string(contents), wantedKey)
		case "objects/" + wantedKey:
			foundObject = string(contents) == wantedBody
		}
	}
	if !foundManifest || !foundObject {
		t.Fatalf("archive manifest=%v object=%v", foundManifest, foundObject)
	}
}

// P2-2 reframed: REQ-2 surfaces as the REST contract — PUT /v1/access/acl
// with a wildcard folder key returns 400 InvalidArgument, while object keys
// containing %/_ stay accepted (exact-match only, no leak).
func TestPutACLWildcardFolderKeyRejected(t *testing.T) {
	server, _, operator, _ := newEnterpriseRESTTest(t)
	headers := map[string]string{"Authorization": operator, "Content-Type": "application/json"}

	response, body := req(t, http.MethodPut, server.URL+"/v1/access/acl", []byte(`{
		"key":"a_/","resource_kind":"folder","principal_type":"user","principal_id":"alice",
		"actions":["object:read"],"effect":"allow","inherit":true
	}`), headers)
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "InvalidArgument") {
		t.Fatalf("wildcard folder status=%d body=%s, want 400 InvalidArgument", response.StatusCode, body)
	}

	// Object keys with wildcard metacharacters remain accepted (201).
	response, body = req(t, http.MethodPut, server.URL+"/v1/access/acl", []byte(`{
		"key":"50%_off.txt","resource_kind":"object","principal_type":"user","principal_id":"alice",
		"actions":["object:read"],"effect":"allow"
	}`), headers)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("wildcard object status=%d body=%s, want 201", response.StatusCode, body)
	}
}
