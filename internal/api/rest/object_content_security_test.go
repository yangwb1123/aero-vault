package rest

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

// Form upload uses the existing unambiguous POST /files FileService entry;
// modifying the old PUT/DELETE suffix dispatch is outside this read-only task.
func uploadContentSelectionForm(t *testing.T, server *httptest.Server, auth, key string, data []byte, contentType string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="file"; filename="fixture"`)
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("key", key); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	response, result := req(t, http.MethodPost, server.URL+"/v1/files", body.Bytes(), map[string]string{
		"Authorization": auth, "Content-Type": writer.FormDataContentType(),
	})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("form upload status=%d body=%s", response.StatusCode, result)
	}
}

func TestObjectContentSelectionDenyBeforeValidators(t *testing.T) {
	server, alice, operator, _ := newEnterpriseRESTTest(t)
	enablePreviewVersioning(t, server, operator)
	key := "literal/metadata"
	uploadContentSelectionForm(t, server, alice, "literal", pngBytes(t, 32, 24), "image/png")
	uploadContentSelectionForm(t, server, alice, key, pngBytes(t, 64, 48), "image/png")
	url := contentSelectionURL(server.URL+"/v1", key, "thumbnail", "")
	response, body := req(t, http.MethodGet, url, nil, map[string]string{"Authorization": alice})
	if response.StatusCode != http.StatusOK || response.Header.Get("ETag") == "" {
		t.Fatalf("initial thumbnail status=%d body=%q headers=%v", response.StatusCode, body, response.Header)
	}
	etag := response.Header.Get("ETag")
	putWorkbenchACL(t, server, operator, key, access.ResourceObject, access.EffectDeny, "object:read")
	for _, representation := range []string{"content", "thumbnail"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			response, body = req(t, method, contentSelectionURL(server.URL+"/v1", key, representation, ""), nil,
				map[string]string{"Authorization": alice, "If-None-Match": etag, "Range": "bytes=0-3"})
			if response.StatusCode != http.StatusForbidden || bytes.HasPrefix(body, []byte{0xff, 0xd8}) {
				t.Fatalf("deny bypass method=%s representation=%s status=%d body=%q", method, representation, response.StatusCode, body)
			}
		}
	}
}

func TestObjectContentSelectionTenantBoundariesAndSuspension(t *testing.T) {
	server, alice, operator, repo := newEnterpriseRESTTest(t)
	key := "literal/tags"
	uploadContentSelectionForm(t, server, alice, key, pngBytes(t, 32, 24), "image/png")
	assertSelectionTenantRead(t, server, alice, key, "foreign", http.StatusForbidden)
	assertSelectionTenantRead(t, server, operator, key, "foreign", http.StatusNotFound)
	if err := repo.UpsertTenant(context.Background(), repository.TenantRecord{
		TenantID: "default", DisplayName: "Default", Status: "disabled",
	}); err != nil {
		t.Fatal(err)
	}
	assertSelectionTenantRead(t, server, alice, key, "default", http.StatusForbidden)
	assertSelectionTenantRead(t, server, operator, key, "default", http.StatusForbidden)
}

func assertSelectionTenantRead(t *testing.T, server *httptest.Server, auth, key, tenant string, want int) {
	t.Helper()
	for _, representation := range []string{"content", "thumbnail"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			response, body := req(t, method, contentSelectionURL(server.URL+"/v1", key, representation, ""), nil,
				map[string]string{"Authorization": auth, "X-Aero-Tenant": tenant})
			if response.StatusCode != want {
				t.Fatalf("method=%s representation=%s tenant=%s status=%d want=%d body=%q", method, representation, tenant, response.StatusCode, want, body)
			}
		}
	}
}

func TestObjectContentSelectionProviderFailureDoesNotFallback(t *testing.T) {
	svc, server := newContentSelectionTest(t)
	putContentSelectionFixture(t, svc, "parent", pngBytes(t, 32, 24), "image/png")
	putContentSelectionFixture(t, svc, "parent/metadata", pngBytes(t, 64, 48), "image/png")
	svc.WithAuthorizer(folderReadUnavailableProvider{})
	for _, representation := range []string{"content", "thumbnail"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			response, body := req(t, method, contentSelectionURL(server.URL, "parent/metadata", representation, ""), nil, nil)
			if response.StatusCode < 400 || bytes.HasPrefix(body, []byte{0xff, 0xd8}) || bytes.HasPrefix(body, []byte{0x89, 'P', 'N', 'G'}) {
				t.Fatalf("provider failure returned another resource method=%s representation=%s status=%d body=%q", method, representation, response.StatusCode, body)
			}
		}
	}
}

func TestObjectContentSelectionCorruptDoesNotFallback(t *testing.T) {
	svc, repo, server := setupTest(t)
	key := "missing-parent/thumbnail"
	putContentSelectionFixture(t, svc, key, pngBytes(t, 32, 24), "image/png")
	if err := repo.SetObjectMetaKey(context.Background(), "default", "default", key, "_aero_scrub_status", "corrupt"); err != nil {
		t.Fatal(err)
	}
	for _, representation := range []string{"content", "thumbnail"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			response, body := req(t, method, contentSelectionURL(server.URL, key, representation, ""), nil, nil)
			if response.StatusCode != http.StatusGone {
				t.Fatalf("corrupt source method=%s representation=%s status=%d body=%q", method, representation, response.StatusCode, body)
			}
		}
	}
}

func TestObjectContentSelectionPolicyUsesFullKey(t *testing.T) {
	svc, _, server := setupTest(t)
	key := "literal/metadata"
	putContentSelectionFixture(t, svc, key, pngBytes(t, 32, 24), "image/png")
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::default/*"},{"Effect":"Deny","Action":"s3:GetObject","Resource":"arn:aws:s3:::default/literal/metadata"}]}`
	if err := svc.SetBucketPolicy(context.Background(), "", "", policy); err != nil {
		t.Fatal(err)
	}
	for _, representation := range []string{"content", "thumbnail"} {
		response, body := req(t, http.MethodGet, contentSelectionURL(server.URL, key, representation, ""), nil, nil)
		if response.StatusCode != http.StatusForbidden || strings.Contains(string(body), "label") {
			t.Fatalf("full-key bucket deny was bypassed representation=%s status=%d body=%q", representation, response.StatusCode, body)
		}
	}
}
