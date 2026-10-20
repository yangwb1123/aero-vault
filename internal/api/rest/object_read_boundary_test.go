package rest

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

type readBoundaryStore struct {
	storage.Storage
	gets atomic.Int64
}

func (s *readBoundaryStore) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	s.gets.Add(1)
	return s.Storage.Get(ctx, key)
}

type readBoundaryProvider struct {
	reads    atomic.Int64
	rejectAt int64
	failure  error
}

func (p *readBoundaryProvider) Authorize(_ context.Context, _ access.Principal, action access.Action, _ access.Resource) (access.Decision, error) {
	if action == access.ActionRead && p.reads.Add(1) == p.rejectAt {
		return access.Decision{Reason: "explicit_deny"}, p.failure
	}
	return access.Decision{Allowed: true}, nil
}

func newReadBoundaryFixture(t *testing.T, key string) (*service.FileService, *readBoundaryStore, *httptest.Server, repository.Object) {
	t.Helper()
	_, repo, _ := setupTest(t)
	local, err := storage.NewLocal(storage.LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	store := &readBoundaryStore{Storage: local}
	svc := service.NewFileService(store, repo, nil).WithAuthorizer(allowAllProvider{})
	if err := svc.SetBucketVersioning(context.Background(), "", "", true); err != nil {
		t.Fatal(err)
	}
	old := putContentSelectionFixture(t, svc, key, []byte("old selected bytes"), "text/plain")
	putContentSelectionFixture(t, svc, key, []byte("current selected bytes"), "text/plain")
	router := NewRouter(svc, repo, nil, nil, nil, nil, nil, slog.Default(), false, nil, nil, 0, false)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal := access.Principal{SubjectID: "reader", TenantID: service.DefaultTenant,
			Kind: access.PrincipalUser, Scopes: []string{"read"}}
		router.ServeHTTP(w, r.WithContext(access.WithPrincipal(r.Context(), principal)))
	}))
	t.Cleanup(server.Close)
	return svc, store, server, old
}

func TestObjectReadBoundaryConditionalStatFailureIsTerminal(t *testing.T) {
	for _, failure := range []error{nil, errors.New("read provider unavailable")} {
		for _, selector := range []string{"", "?representation=content"} {
			for _, header := range []string{"If-None-Match", "If-Match", "Range"} {
				t.Run(header+selector+"/"+strconv.FormatBool(failure != nil), func(t *testing.T) {
					svc, store, server, _ := newReadBoundaryFixture(t, "selected.txt")
					provider := &readBoundaryProvider{rejectAt: 1, failure: failure}
					svc.WithAuthorizer(provider)
					value := "*"
					if header == "Range" {
						value = "bytes=0-3"
					}
					response, body := req(t, http.MethodGet, server.URL+"/files/selected.txt"+selector, nil, map[string]string{header: value})
					want := http.StatusForbidden
					if failure != nil {
						want = http.StatusInternalServerError
					}
					if response.StatusCode != want || provider.reads.Load() != 1 || store.gets.Load() != 0 {
						t.Fatalf("failed Stat was retried: status=%d want=%d authorizations=%d blobs=%d body=%q", response.StatusCode, want, provider.reads.Load(), store.gets.Load(), body)
					}
				})
			}
		}
	}
}

func TestObjectReadBoundarySuccessfulStatStillReauthorizesOpen(t *testing.T) {
	for header, value := range map[string]string{"If-None-Match": `"different"`, "Range": "bytes=0-3"} {
		t.Run(header, func(t *testing.T) {
			svc, store, server, _ := newReadBoundaryFixture(t, "selected.txt")
			provider := &readBoundaryProvider{rejectAt: 2}
			svc.WithAuthorizer(provider)
			response, body := req(t, http.MethodGet, contentSelectionURL(server.URL, "selected.txt", "content", ""), nil, map[string]string{header: value})
			if response.StatusCode != http.StatusForbidden || provider.reads.Load() != 2 || store.gets.Load() != 0 {
				t.Fatalf("successful Stat bypassed open authorization: status=%d calls=%d blobs=%d body=%q", response.StatusCode, provider.reads.Load(), store.gets.Load(), body)
			}
		})
	}
}

func TestObjectReadBoundaryLegacyExactThumbnailHeadDoesNotOpen(t *testing.T) {
	svc, store, server, old := newReadBoundaryFixture(t, "literal/thumbnail")
	current, err := svc.Stat(context.Background(), "", "", old.Key)
	if err != nil {
		t.Fatal(err)
	}
	for _, selected := range []repository.Object{current, old} {
		endpoint := server.URL + "/files/" + selected.Key
		if selected.VersionID == old.VersionID {
			endpoint += "?version=" + url.QueryEscape(old.VersionID)
		}
		response, body := req(t, http.MethodHead, endpoint, nil, nil)
		if response.StatusCode != http.StatusOK || len(body) != 0 || store.gets.Load() != 0 ||
			response.Header.Get("ETag") != `"`+selected.ETag+`"` || response.Header.Get("Content-Length") != strconv.FormatInt(selected.Size, 10) {
			t.Errorf("exact HEAD read bytes/lost metadata: status=%d blobs=%d headers=%v", response.StatusCode, store.gets.Load(), response.Header)
		}
	}
}

func readBoundaryContentPaths(base, key, version string) []string {
	legacy := base + "/files/" + key
	if version != "" {
		legacy += "?version=" + url.QueryEscape(version)
	}
	return []string{contentSelectionURL(base, key, "content", version), legacy}
}

func TestObjectReadBoundaryPinnedHead304KeepsVersionIdentity(t *testing.T) {
	_, store, server, old := newReadBoundaryFixture(t, "literal/thumbnail")
	for _, endpoint := range readBoundaryContentPaths(server.URL, old.Key, old.VersionID) {
		response, body := req(t, http.MethodHead, endpoint, nil, map[string]string{"If-None-Match": `"` + old.ETag + `"`})
		if response.StatusCode != http.StatusNotModified || len(body) != 0 || store.gets.Load() != 0 ||
			response.Header.Get("X-Version-Id") != old.VersionID || response.Header.Get("ETag") != `"`+old.ETag+`"` {
			t.Errorf("pinned HEAD 304 lost identity/opened Blob: status=%d blobs=%d headers=%v", response.StatusCode, store.gets.Load(), response.Header)
		}
	}
}

func TestObjectReadBoundaryHeadIgnoresRangeWithoutOpening(t *testing.T) {
	svc, store, server, old := newReadBoundaryFixture(t, "literal/thumbnail")
	current, err := svc.Stat(context.Background(), "", "", old.Key)
	if err != nil {
		t.Fatal(err)
	}
	for version, size := range map[string]int64{"": current.Size, old.VersionID: old.Size} {
		for _, endpoint := range readBoundaryContentPaths(server.URL, old.Key, version) {
			for _, span := range []string{"bytes=0-3", "bytes=99999-", "invalid"} {
				response, body := req(t, http.MethodHead, endpoint, nil, map[string]string{"Range": span})
				if response.StatusCode != http.StatusOK || len(body) != 0 || store.gets.Load() != 0 || response.Header.Get("Content-Length") != strconv.FormatInt(size, 10) {
					t.Fatalf("HEAD acted on Range: span=%s status=%d blobs=%d headers=%v", span, response.StatusCode, store.gets.Load(), response.Header)
				}
			}
		}
	}
}

func TestObjectReadBoundaryHead304KeepsContentEncoding(t *testing.T) {
	svc, store, server, _ := newReadBoundaryFixture(t, "literal/thumbnail")
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write([]byte("encoded selected bytes")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	object, err := svc.Put(context.Background(), "", "", "encoded/thumbnail", bytes.NewReader(compressed.Bytes()), int64(compressed.Len()),
		service.PutOptions{ContentType: "text/plain", ContentEncoding: "gzip"})
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range readBoundaryContentPaths(server.URL, object.Key, object.VersionID) {
		response, body := req(t, http.MethodHead, endpoint, nil, map[string]string{"If-None-Match": `"` + object.ETag + `"`})
		if response.StatusCode != http.StatusNotModified || len(body) != 0 || store.gets.Load() != 0 ||
			response.Header.Get("Content-Encoding") != "gzip" || response.Header.Get("Last-Modified") != object.UpdatedAt.UTC().Format(http.TimeFormat) {
			t.Fatalf("HEAD 304 discarded representation headers/read Blob: status=%d blobs=%d headers=%v", response.StatusCode, store.gets.Load(), response.Header)
		}
	}
}

func TestObjectReadBoundaryExplicitNewRequestCanReadAfterRecovery(t *testing.T) {
	svc, store, server, _ := newReadBoundaryFixture(t, "selected.txt")
	provider := &readBoundaryProvider{rejectAt: 1, failure: errors.New("read provider unavailable")}
	svc.WithAuthorizer(provider)
	endpoint := contentSelectionURL(server.URL, "selected.txt", "content", "")
	headers := map[string]string{"If-Match": "*"}
	response, _ := req(t, http.MethodGet, endpoint, nil, headers)
	if response.StatusCode != http.StatusInternalServerError || provider.reads.Load() != 1 || store.gets.Load() != 0 {
		t.Fatalf("first request did not fail closed: status=%d calls=%d blobs=%d", response.StatusCode, provider.reads.Load(), store.gets.Load())
	}
	response, body := req(t, http.MethodGet, endpoint, nil, headers)
	if response.StatusCode != http.StatusOK || string(body) != "current selected bytes" || provider.reads.Load() != 3 || store.gets.Load() != 1 {
		t.Fatalf("independent explicit request lost normal checks: status=%d calls=%d blobs=%d body=%q", response.StatusCode, provider.reads.Load(), store.gets.Load(), body)
	}
}
