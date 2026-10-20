package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
)

func presignSelectedContent(t *testing.T, server *httptest.Server, auth, key string) string {
	t.Helper()
	response, body := req(t, http.MethodPost, server.URL+"/v1/files/"+key+"/presign?op=get&expires=60", nil,
		map[string]string{"Authorization": auth})
	var result struct{ URL string }
	if err := json.Unmarshal(body, &result); err != nil || response.StatusCode != http.StatusOK || result.URL == "" {
		t.Fatalf("presign status=%d body=%s err=%v", response.StatusCode, body, err)
	}
	return result.URL
}

func TestObjectContentSelectionPresignedFullKey(t *testing.T) {
	server, alice, _, _ := newEnterpriseRESTTest(t)
	for _, suffix := range []string{"tags", "versions", "acl", "metadata", "thumbnail"} {
		key := "literal/" + suffix
		uploadContentSelectionForm(t, server, alice, key, []byte("selected bytes"), "text/plain")
		signed := presignSelectedContent(t, server, alice, key)
		parsed, err := url.Parse(signed)
		if err != nil || parsed.Query().Has("representation") {
			t.Fatalf("presign format/allowlist changed: url=%q err=%v", signed, err)
		}
		response, body := req(t, http.MethodGet, signed, nil, nil)
		if response.StatusCode != http.StatusOK || string(body) != "selected bytes" {
			t.Fatalf("presigned full key suffix=%s status=%d body=%q", suffix, response.StatusCode, body)
		}
		response, body = req(t, http.MethodHead, signed, nil, nil)
		if response.StatusCode != http.StatusOK || len(body) != 0 || response.Header.Get("Content-Length") != "14" {
			t.Fatalf("presigned HEAD suffix=%s status=%d headers=%v", suffix, response.StatusCode, response.Header)
		}
	}
}

func TestObjectContentSelectionPresignCannotExpandRepresentationOrPath(t *testing.T) {
	server, alice, operator, _ := newEnterpriseRESTTest(t)
	key := "literal/metadata"
	uploadContentSelectionForm(t, server, alice, key, []byte("selected bytes"), "text/plain")
	signed := presignSelectedContent(t, server, alice, key)
	for _, extra := range []string{"representation=content", "representation=thumbnail", "version=another"} {
		response, body := req(t, http.MethodGet, signed+"&"+extra, nil, nil)
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("signed query expansion accepted: extra=%s status=%d body=%q", extra, response.StatusCode, body)
		}
	}
	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/v1/files/literal"
	response, body := req(t, http.MethodGet, parsed.String(), nil, nil)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("signed path change accepted: status=%d body=%q", response.StatusCode, body)
	}
	putWorkbenchACL(t, server, operator, key, access.ResourceObject, access.EffectDeny, "object:read")
	response, body = req(t, http.MethodGet, signed, nil, nil)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("valid signature bypassed later Deny: status=%d body=%q", response.StatusCode, body)
	}
}

func TestObjectContentSelectionSignatureLookingQueryIsNotTrusted(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/files/literal/tags?x-aero-presign-operation=get&x-aero-presign-signature=unverified", nil)
	representation, err := fileRepresentation(request)
	if err != nil || representation != "" {
		t.Fatalf("unverified query selected capability representation=%q err=%v", representation, err)
	}
}

func TestObjectContentSelectionHeadNeverOpensBlob(t *testing.T) {
	handler, store, object := newVersionCancellationFixture(t)
	ctx := access.WithPrincipal(context.Background(), access.Principal{
		SubjectID: "reader", TenantID: "default", Kind: access.PrincipalUser, Scopes: []string{"read"},
	})
	request := httptest.NewRequest(http.MethodHead, contentSelectionURL("", object.Key, "content", object.VersionID), nil).WithContext(ctx)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Length") != strconv.FormatInt(object.Size, 10) ||
		response.Header().Get("X-Version-Id") != object.VersionID || store.source != nil {
		t.Fatalf("historical HEAD opened a blob or lost identity: status=%d headers=%v opened=%q", response.Code, response.Header(), store.opened)
	}
}
