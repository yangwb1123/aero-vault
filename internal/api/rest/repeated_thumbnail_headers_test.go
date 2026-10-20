package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

func newRepeatedThumbnailFixture(t *testing.T) (*httptest.Server, repository.Object, repository.Object) {
	t.Helper()
	_, repo, _ := setupTest(t)
	local, err := storage.NewLocal(storage.LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	svc := service.NewFileService(local, repo, nil).WithAuthorizer(allowAllProvider{})
	if err := svc.SetBucketVersioning(context.Background(), "", "", true); err != nil {
		t.Fatal(err)
	}
	older := putContentSelectionFixture(t, svc, "poster", pngBytes(t, 32, 24), "image/png")
	current := putContentSelectionFixture(t, svc, older.Key, pngBytes(t, 64, 48), "image/png")
	return serveLegacyDenialFixture(t, svc, repo), older, current
}

func repeatedThumbnailPaths(base string, selected repository.Object, pinned bool) []string {
	version := ""
	legacy := base + "/v1/files/" + selected.Key + "/thumbnail?w=32&h=24"
	if pinned {
		version = selected.VersionID
		legacy += "&version=" + url.QueryEscape(version)
	}
	return []string{legacy, contentSelectionURL(base+"/v1", selected.Key, "thumbnail", version) + "&w=32&h=24"}
}

func repeatedThumbnailETag(t *testing.T, endpoint string) string {
	t.Helper()
	response, _ := repeatedReadRequest(t, http.MethodGet, endpoint, http.Header{})
	if response.StatusCode != http.StatusOK || response.Header.Get("ETag") == "" {
		t.Fatalf("thumbnail generation proof/validator missing: status=%d headers=%v", response.StatusCode, response.Header)
	}
	return response.Header.Get("ETag")
}

func TestRepeatedThumbnailReadHeadersEntityTagLists(t *testing.T) {
	web, older, current := newRepeatedThumbnailFixture(t)
	for _, selected := range []repository.Object{current, older} {
		for _, endpoint := range repeatedThumbnailPaths(web.URL, selected, selected.VersionID == older.VersionID) {
			etag := repeatedThumbnailETag(t, endpoint)
			for name, status := range map[string]int{"If-Match": http.StatusOK, "If-None-Match": http.StatusNotModified} {
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					response, body := repeatedReadRequest(t, method, endpoint, http.Header{name: {`"wrong"`, etag}})
					if response.StatusCode != status || method == http.MethodHead && len(body) != 0 {
						t.Fatalf("thumbnail ignored repeated %s: method=%s status=%d want=%d body=%d bytes", name, method, response.StatusCode, status, len(body))
					}
				}
			}
		}
	}
}

func TestRepeatedThumbnailReadHeadersEmptyIfNoneMatchSuppressesDate(t *testing.T) {
	web, older, current := newRepeatedThumbnailFixture(t)
	for _, selected := range []repository.Object{current, older} {
		for _, endpoint := range repeatedThumbnailPaths(web.URL, selected, selected.VersionID == older.VersionID) {
			for _, values := range [][]string{{""}, {"*", `"wrong"`}} {
				headers := http.Header{"If-None-Match": values, "If-Modified-Since": {selected.UpdatedAt.Add(time.Hour).UTC().Format(http.TimeFormat)}}
				response, body := repeatedReadRequest(t, http.MethodGet, endpoint, headers)
				if response.StatusCode != http.StatusOK {
					t.Fatalf("empty/invalid INM enabled thumbnail date fallback: status=%d body=%q", response.StatusCode, body)
				}
			}
		}
	}
}

func TestRepeatedThumbnailReadHeadersInvalidDatesAreIgnored(t *testing.T) {
	web, older, current := newRepeatedThumbnailFixture(t)
	for _, selected := range []repository.Object{current, older} {
		for _, endpoint := range repeatedThumbnailPaths(web.URL, selected, selected.VersionID == older.VersionID) {
			past := selected.UpdatedAt.Add(-time.Hour).UTC().Format(http.TimeFormat)
			future := selected.UpdatedAt.Add(time.Hour).UTC().Format(http.TimeFormat)
			for _, headers := range []http.Header{{"If-Modified-Since": {future, past}}, {"If-Unmodified-Since": {past, future}}} {
				response, body := repeatedReadRequest(t, http.MethodGet, endpoint, headers)
				if response.StatusCode != http.StatusOK {
					t.Fatalf("thumbnail picked a repeated date: status=%d want=200 body=%q", response.StatusCode, body)
				}
			}
		}
	}
}
