package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

func putContentSelectionFixture(t *testing.T, svc *service.FileService, key string, data []byte, contentType string) repository.Object {
	t.Helper()
	ctx := access.WithPrincipal(context.Background(), access.Principal{
		SubjectID: "reader", TenantID: service.DefaultTenant, Kind: access.PrincipalUser, Scopes: []string{"read", "write"},
	})
	object, err := svc.Put(ctx, "", "", key, bytes.NewReader(data), int64(len(data)), service.PutOptions{
		ContentType: contentType, Metadata: map[string]string{"label": key}, Tags: map[string]string{"label": key},
	})
	if err != nil {
		t.Fatal(err)
	}
	return object
}

func contentSelectionURL(base, key, representation, version string) string {
	query := url.Values{"representation": {representation}}
	if version != "" {
		query.Set("version", version)
	}
	return base + "/files/" + strings.ReplaceAll(url.PathEscape(key), "%2F", "/") + "?" + query.Encode()
}

// Versioned blobs allow both parent and parent/child keys on path-based local FS.
func newContentSelectionTest(t *testing.T) (*service.FileService, *httptest.Server) {
	t.Helper()
	svc, _, server := setupTest(t)
	if err := svc.SetBucketVersioning(context.Background(), "", "", true); err != nil {
		t.Fatal(err)
	}
	return svc, server
}

func TestObjectContentSelectionReadsLiteralSuffix(t *testing.T) {
	svc, server := newContentSelectionTest(t)
	putContentSelectionFixture(t, svc, "literal", []byte("parent"), "text/plain")
	for _, suffix := range []string{"tags", "versions", "acl", "metadata", "thumbnail"} {
		t.Run(suffix, func(t *testing.T) {
			key := "literal/" + suffix
			putContentSelectionFixture(t, svc, key, []byte("selected bytes"), "text/plain")
			response, body := req(t, http.MethodGet, contentSelectionURL(server.URL, key, "content", ""), nil,
				map[string]string{"Range": "bytes=0-7"})
			if response.StatusCode != http.StatusPartialContent || string(body) != "selected" || response.Header.Get("Content-Type") != "text/plain" {
				t.Fatalf("suffix=%s status=%d body=%q headers=%v", suffix, response.StatusCode, body, response.Header)
			}
		})
	}
}

func TestObjectContentSelectionOpaqueKey(t *testing.T) {
	svc, server := newContentSelectionTest(t)
	key := " docs/100% report.txt /metadata"
	putContentSelectionFixture(t, svc, key, []byte("selected bytes"), "text/plain")
	response, body := req(t, http.MethodGet, contentSelectionURL(server.URL, key, "content", ""), nil, nil)
	if response.StatusCode != http.StatusOK || string(body) != "selected bytes" {
		t.Fatalf("opaque full key changed: status=%d body=%q", response.StatusCode, body)
	}
}

func TestObjectContentSelectionMissingLiteralDoesNotReadParent(t *testing.T) {
	svc, server := newContentSelectionTest(t)
	putContentSelectionFixture(t, svc, "parent", pngBytes(t, 64, 48), "image/png")
	for _, suffix := range []string{"tags", "versions", "acl", "metadata", "thumbnail"} {
		response, body := req(t, http.MethodGet, contentSelectionURL(server.URL, "parent/"+suffix, "content", ""), nil, nil)
		if response.StatusCode != http.StatusNotFound {
			t.Errorf("missing full key suffix=%s fell back: status=%d body=%q", suffix, response.StatusCode, body)
		}
	}
}

func TestObjectContentSelectionRetainsDefaultSubresources(t *testing.T) {
	svc, server := newContentSelectionTest(t)
	putContentSelectionFixture(t, svc, "parent", []byte("parent"), "text/plain")
	putContentSelectionFixture(t, svc, "parent/tags", []byte("literal"), "text/plain")
	response, body := req(t, http.MethodGet, server.URL+"/files/parent/tags", nil, nil)
	var result struct{ Tags map[string]string }
	if err := json.Unmarshal(body, &result); err != nil || response.StatusCode != http.StatusOK || result.Tags["label"] != "parent" {
		t.Fatalf("legacy tag route changed: status=%d body=%q err=%v", response.StatusCode, body, err)
	}
}

func TestObjectContentSelectionThumbnailIgnoresNeighbour(t *testing.T) {
	svc, server := newContentSelectionTest(t)
	putContentSelectionFixture(t, svc, "photo", pngBytes(t, 64, 48), "image/png")
	putContentSelectionFixture(t, svc, "photo/thumbnail", []byte("unrelated neighbour"), "text/plain")
	response, body := req(t, http.MethodGet, contentSelectionURL(server.URL, "photo", "thumbnail", "")+"&w=8&h=6", nil, nil)
	image, err := jpeg.Decode(bytes.NewReader(body))
	if err != nil || response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("explicit derivation status=%d body=%q err=%v", response.StatusCode, body, err)
	}
	if image.Bounds().Dx() != 8 || image.Bounds().Dy() != 6 {
		t.Fatalf("dimensions=%v, want 8x6", image.Bounds())
	}
	response, body = req(t, http.MethodGet, server.URL+"/files/photo/thumbnail", nil, nil)
	if response.StatusCode != http.StatusOK || string(body) != "unrelated neighbour" {
		t.Fatalf("default exact-key precedence changed: status=%d body=%q", response.StatusCode, body)
	}
}

func TestObjectContentSelectionHeadPinsVersion(t *testing.T) {
	svc, server := newContentSelectionTest(t)
	if err := svc.SetBucketVersioning(context.Background(), "", "", true); err != nil {
		t.Fatal(err)
	}
	old := putContentSelectionFixture(t, svc, "literal/metadata", []byte("old"), "text/plain")
	putContentSelectionFixture(t, svc, "literal/metadata", []byte("new longer content"), "application/octet-stream")
	response, body := req(t, http.MethodHead, contentSelectionURL(server.URL, old.Key, "content", old.VersionID), nil, nil)
	if response.StatusCode != http.StatusOK || len(body) != 0 || response.Header.Get("Content-Length") != strconv.FormatInt(old.Size, 10) ||
		response.Header.Get("ETag") != `"`+old.ETag+`"` || response.Header.Get("X-Version-Id") != old.VersionID {
		t.Fatalf("historical HEAD did not stay pinned: status=%d headers=%v body=%q", response.StatusCode, response.Header, body)
	}
	response, _ = req(t, http.MethodHead, contentSelectionURL(server.URL, old.Key, "content", "missing"), nil, nil)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("HEAD unknown pin fell back: status=%d", response.StatusCode)
	}
}

func TestObjectContentSelectionUnknownThumbnailPinDoesNotDeriveParent(t *testing.T) {
	svc, server := newContentSelectionTest(t)
	if err := svc.SetBucketVersioning(context.Background(), "", "", true); err != nil {
		t.Fatal(err)
	}
	parent := putContentSelectionFixture(t, svc, "photo", pngBytes(t, 32, 24), "image/png")
	putContentSelectionFixture(t, svc, "photo/thumbnail", []byte("literal"), "text/plain")
	response, body := req(t, http.MethodGet, contentSelectionURL(server.URL, "photo/thumbnail", "content", parent.VersionID), nil, nil)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("wrong full-key pin derived another object's version: status=%d body=%q", response.StatusCode, body)
	}
}

func TestObjectContentSelectionDeletedAndMarkerDoNotFallback(t *testing.T) {
	svc, server := newContentSelectionTest(t)
	key := "parent/thumbnail"
	old := putContentSelectionFixture(t, svc, key, pngBytes(t, 32, 24), "image/png")
	if err := svc.Delete(context.Background(), "", "", key, false); err != nil {
		t.Fatal(err)
	}
	marker, err := svc.CreateDeleteMarker(context.Background(), "", "", key)
	if err != nil {
		t.Fatal(err)
	}
	for _, representation := range []string{"content", "thumbnail"} {
		for _, version := range []string{"", marker.VersionID} {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				response, body := req(t, method, contentSelectionURL(server.URL, key, representation, version), nil, nil)
				if response.StatusCode != http.StatusNotFound {
					t.Fatalf("deleted/marker fallback method=%s representation=%s version=%q status=%d body=%q", method, representation, version, response.StatusCode, body)
				}
			}
		}
	}
	response, body := req(t, http.MethodHead, contentSelectionURL(server.URL, key, "content", old.VersionID), nil, nil)
	if response.StatusCode != http.StatusOK || len(body) != 0 || response.Header.Get("X-Version-Id") != old.VersionID {
		t.Fatalf("readable soft-deleted version lost HEAD identity: status=%d headers=%v", response.StatusCode, response.Header)
	}
}

func TestObjectContentSelectionRejectsInvalidRepresentation(t *testing.T) {
	svc, server := newContentSelectionTest(t)
	putContentSelectionFixture(t, svc, "parent", []byte("parent"), "text/plain")
	for _, query := range []string{"representation=", "representation=other", "representation=content&representation=thumbnail", "representation=%", "representation=content;other=1"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			response, body := req(t, method, server.URL+"/files/parent/tags?"+query, nil, nil)
			if response.StatusCode != http.StatusBadRequest {
				t.Errorf("method=%s query=%q status=%d body=%q", method, query, response.StatusCode, body)
			}
		}
	}
}
