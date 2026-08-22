package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFavicon(t *testing.T) {
	rec := httptest.NewRecorder()
	Favicon(rec, httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/svg+xml" {
		t.Fatalf("Content-Type=%q want image/svg+xml", got)
	}
	if !strings.Contains(rec.Body.String(), "<svg") {
		t.Fatal("favicon response is not SVG")
	}
}

func TestConsoleIncludesEnterpriseAccessControls(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"pane-access", "createShare()", "publishAsset()", "exportBackup()",
		"createDepartment()", "putDepartmentMember()", "putResourceACL()",
		"listShares()", "revokeShare()", "listAssets()", "unpublishAsset()",
		"listResourceACL()", "deleteResourceACL()", "deleteDepartment()",
		"downloadObject()", "deleteObject()", "restoreObject()",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("console missing %q", want)
		}
	}
	if strings.Contains(body, "claims.sub;") {
		t.Fatal("Snaplink subject must not be treated as an Aero tenant")
	}
}

func TestConsoleServesIrisApplication(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/app/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q want no-store", got)
	}
	for _, want := range []string{"<title>Aero Vault</title>", "/ui/app/runtime-config.js", "/ui/app/assets/"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("Iris application shell missing %q", want)
		}
	}
}

func TestConsoleRuntimeConfigIsNotCached(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/app/runtime-config.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q want no-store", got)
	}
}

func TestConsoleServesEnterpriseLifecycleScript(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/enterprise.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusOK)
	}
	for _, want := range []string{
		"async function revokeShare", "async function unpublishAsset",
		"async function deleteResourceACL", "async function deleteDepartmentMember",
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("enterprise script missing %q", want)
		}
	}
}
