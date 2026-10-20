package rest

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/auth"
	"github.com/aero-vault/aero-vault/internal/config"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/server"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

type legacyDenialFixture struct {
	svc   *service.FileService
	store *readBoundaryStore
	repo  repository.Repository
	web   *httptest.Server
	old   repository.Object
}

func newLegacyDenialFixture(t *testing.T) *legacyDenialFixture {
	t.Helper()
	_, repo, _ := setupTest(t)
	local, err := storage.NewLocal(storage.LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	store := &readBoundaryStore{Storage: local}
	svc := service.NewFileService(store, repo, nil).WithAuthorizer(allowAllProvider{}).WithTenantStatusEnforcement()
	if err := svc.SetBucketVersioning(context.Background(), "", "", true); err != nil {
		t.Fatal(err)
	}
	old := putContentSelectionFixture(t, svc, "literal/thumbnail", []byte("old selected bytes"), "text/plain")
	putContentSelectionFixture(t, svc, old.Key, []byte("current selected bytes"), "text/plain")
	putContentSelectionFixture(t, svc, "literal", pngBytes(t, 64, 32), "image/png")
	return &legacyDenialFixture{svc: svc, store: store, repo: repo, old: old, web: serveLegacyDenialFixture(t, svc, repo)}
}

func serveLegacyDenialFixture(t *testing.T, svc *service.FileService, repo repository.Repository) *httptest.Server {
	t.Helper()
	registry, err := auth.Parse("reader:default:read+write")
	if err != nil {
		t.Fatal(err)
	}
	registry.WithAnonymousPublicRead(true)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := chi.NewRouter()
	router.Mount("/v1", NewRouter(svc, repo, nil, nil, nil, nil, registry, logger, false, nil, nil, 0, false))
	handler := server.ApplyMiddleware(router, repo, registry, mw.NewRateLimiter(0, 0), &config.Config{}, logger,
		mw.NewConcurrencyLimiter(0).Middleware(), nil)
	web := httptest.NewServer(handler)
	t.Cleanup(web.Close)
	return web
}

func (f *legacyDenialFixture) endpoint(pinned bool) string {
	endpoint := f.web.URL + "/v1/files/" + f.old.Key
	if pinned {
		endpoint += "?version=" + url.QueryEscape(f.old.VersionID)
	}
	return endpoint
}

type legacyDenialRead struct {
	name   string
	method string
	header string
	value  string
}

func legacyDenialReads() []legacyDenialRead {
	return []legacyDenialRead{
		{name: "GET", method: http.MethodGet},
		{name: "HEAD", method: http.MethodHead},
		{name: "GET validator", method: http.MethodGet, header: "If-None-Match", value: "*"},
		{name: "HEAD validator", method: http.MethodHead, header: "If-None-Match", value: "*"},
		{name: "GET range", method: http.MethodGet, header: "Range", value: "bytes=0-3"},
		{name: "HEAD range", method: http.MethodHead, header: "Range", value: "bytes=0-3"},
	}
}

func TestLegacyThumbnailDenialAuthenticatedStatDoesNotRetry(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		for _, read := range legacyDenialReads() {
			t.Run(read.name+"/pinned="+strconv.FormatBool(pinned), func(t *testing.T) {
				fixture := newLegacyDenialFixture(t)
				provider := &readBoundaryProvider{rejectAt: 1}
				fixture.svc.WithAuthorizer(provider)
				headers := map[string]string{"Authorization": "Bearer reader"}
				if read.header != "" {
					headers[read.header] = read.value
				}
				response, body := req(t, read.method, fixture.endpoint(pinned), nil, headers)
				assertLegacyDenialTerminal(t, fixture, provider, response, body, http.StatusForbidden)
			})
		}
	}
}

func assertLegacyDenialTerminal(t *testing.T, fixture *legacyDenialFixture, provider *readBoundaryProvider, response *http.Response, body []byte, status int) {
	t.Helper()
	if response.StatusCode != status || provider.reads.Load() != 1 || fixture.store.gets.Load() != 0 {
		t.Fatalf("failed exact-key lookup was retried: status=%d want=%d checks=%d blobs=%d body=%q", response.StatusCode, status, provider.reads.Load(), fixture.store.gets.Load(), body)
	}
	if response.Header.Get("ETag") != "" || response.Header.Get("X-Version-Id") != "" || response.Header.Get("Content-Range") != "" {
		t.Fatalf("denied exact key returned content validators: %v", response.Header)
	}
}

func TestLegacyThumbnailDenialProviderErrorDoesNotRetry(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			fixture := newLegacyDenialFixture(t)
			provider := &readBoundaryProvider{rejectAt: 1, failure: errors.New("read provider unavailable")}
			fixture.svc.WithAuthorizer(provider)
			response, body := req(t, method, fixture.endpoint(pinned), nil, map[string]string{"Authorization": "Bearer reader"})
			assertLegacyDenialTerminal(t, fixture, provider, response, body, http.StatusInternalServerError)
		}
	}
}

func TestLegacyThumbnailDenialSuccessfulLookupStillReauthorizes(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			fixture := newLegacyDenialFixture(t)
			provider := &readBoundaryProvider{rejectAt: 2}
			fixture.svc.WithAuthorizer(provider)
			response, body := req(t, method, fixture.endpoint(pinned), nil, map[string]string{"Authorization": "Bearer reader"})
			if response.StatusCode != http.StatusForbidden || provider.reads.Load() != 2 || fixture.store.gets.Load() != 0 {
				t.Fatalf("successful discriminator bypassed final authorization: status=%d checks=%d blobs=%d body=%q", response.StatusCode, provider.reads.Load(), fixture.store.gets.Load(), body)
			}
		}
	}
}

func (f *legacyDenialFixture) enablePublicReadManager(t *testing.T, publicKey string) *access.Manager {
	t.Helper()
	if err := f.svc.SetObjectACL(context.Background(), "", "", publicKey, service.ACLPublicRead); err != nil {
		t.Fatal(err)
	}
	store, ok := f.repo.(access.Store)
	if !ok {
		t.Fatal("repository does not implement access.Store")
	}
	manager, err := access.NewManager(store, access.Config{Enabled: true, DefaultPolicy: access.DefaultTenant,
		ShareSecret: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.WithAuthorizer(manager)
	return manager
}

func TestLegacyThumbnailDenialAnonymousPublicReadStillWorks(t *testing.T) {
	fixture := newLegacyDenialFixture(t)
	fixture.enablePublicReadManager(t, fixture.old.Key)
	for _, pinned := range []bool{false, true} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			response, body := req(t, method, fixture.endpoint(pinned), nil, nil)
			want := "current selected bytes"
			if pinned {
				want = "old selected bytes"
			}
			assertLegacyAnonymousExactRead(t, response, body, method, want)
		}
	}
	if fixture.store.gets.Load() != 2 {
		t.Fatalf("anonymous exact HEAD opened a Blob: opens=%d", fixture.store.gets.Load())
	}
}

func assertLegacyAnonymousExactRead(t *testing.T, response *http.Response, body []byte, method, want string) {
	t.Helper()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/plain" || response.Header.Get("Content-Length") != strconv.Itoa(len(want)) {
		t.Fatalf("anonymous full-key capability failed: status=%d headers=%v body=%q", response.StatusCode, response.Header, body)
	}
	if method == http.MethodHead {
		want = ""
	}
	if string(body) != want {
		t.Fatalf("anonymous exact key fell back/read on HEAD: method=%s body=%q want=%q", method, body, want)
	}
}

func TestLegacyThumbnailDenialPrivateFullKeyDoesNotUsePublicParent(t *testing.T) {
	fixture := newLegacyDenialFixture(t)
	fixture.enablePublicReadManager(t, "literal")
	for _, pinned := range []bool{false, true} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			response, body := req(t, method, fixture.endpoint(pinned), nil, nil)
			if response.StatusCode != http.StatusForbidden || fixture.store.gets.Load() != 0 || response.Header.Get("ETag") != "" {
				t.Fatalf("private full key fell back to public parent: status=%d opens=%d body=%q", response.StatusCode, fixture.store.gets.Load(), body)
			}
		}
	}
}

func TestLegacyThumbnailDenialPublicCapabilityDoesNotOverrideDeny(t *testing.T) {
	fixture := newLegacyDenialFixture(t)
	manager := fixture.enablePublicReadManager(t, fixture.old.Key)
	ctx := access.WithPrincipal(context.Background(), access.Principal{SubjectID: "operator", TenantID: "*", Kind: access.PrincipalUser, Scopes: []string{"admin"}})
	_, err := manager.PutACL(ctx, access.ACLEntry{TenantID: "default", Bucket: "default", Key: fixture.old.Key,
		ResourceKind: access.ResourceObject, PrincipalType: access.PrincipalTypeEveryone, Action: access.ActionRead, Effect: access.EffectDeny})
	if err != nil {
		t.Fatal(err)
	}
	for _, pinned := range []bool{false, true} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			response, body := req(t, method, fixture.endpoint(pinned), nil, map[string]string{"If-None-Match": "*"})
			if response.StatusCode != http.StatusForbidden || fixture.store.gets.Load() != 0 || response.Header.Get("ETag") != "" {
				t.Fatalf("public capability overrode explicit Deny: status=%d opens=%d body=%q", response.StatusCode, fixture.store.gets.Load(), body)
			}
		}
	}
}

func TestLegacyThumbnailDenialTenantSuspensionStopsBeforeLookup(t *testing.T) {
	fixture := newLegacyDenialFixture(t)
	provider := &readBoundaryProvider{}
	fixture.svc.WithAuthorizer(provider)
	if err := fixture.repo.UpsertTenant(context.Background(), repository.TenantRecord{TenantID: "default", DisplayName: "Default", Status: "disabled"}); err != nil {
		t.Fatal(err)
	}
	for _, pinned := range []bool{false, true} {
		for _, headers := range []map[string]string{nil, {"Authorization": "Bearer reader"}} {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				response, body := req(t, method, fixture.endpoint(pinned), nil, headers)
				if response.StatusCode != http.StatusForbidden || provider.reads.Load() != 0 || fixture.store.gets.Load() != 0 {
					t.Fatalf("disabled tenant reached exact-key lookup: status=%d checks=%d opens=%d body=%q", response.StatusCode, provider.reads.Load(), fixture.store.gets.Load(), body)
				}
			}
		}
	}
}

func TestLegacyThumbnailDenialExplicitNewRequestCanRecover(t *testing.T) {
	fixture := newLegacyDenialFixture(t)
	provider := &readBoundaryProvider{rejectAt: 1}
	fixture.svc.WithAuthorizer(provider)
	headers := map[string]string{"Authorization": "Bearer reader"}
	response, body := req(t, http.MethodGet, fixture.endpoint(false), nil, headers)
	assertLegacyDenialTerminal(t, fixture, provider, response, body, http.StatusForbidden)
	response, body = req(t, http.MethodGet, fixture.endpoint(false), nil, headers)
	if response.StatusCode != http.StatusOK || string(body) != "current selected bytes" || provider.reads.Load() != 3 || fixture.store.gets.Load() != 1 {
		t.Fatalf("explicit new request did not recover: status=%d checks=%d opens=%d body=%q", response.StatusCode, provider.reads.Load(), fixture.store.gets.Load(), body)
	}
}
