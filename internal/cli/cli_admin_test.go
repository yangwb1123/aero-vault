package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --------------------------------------------------------------------------
// cmdAdmin — dispatch and adminUsage
// --------------------------------------------------------------------------

func TestCmdAdmin_TooFewArgs_Returns2(t *testing.T) {
	c := &Client{http: &http.Client{}}
	captureStderr(t, func() {
		if code := c.cmdAdmin([]string{}); code != 2 {
			t.Errorf("cmdAdmin([]) = %d; want 2", code)
		}
	})
}

func TestCmdAdmin_TooFewArgs_Single_Returns2(t *testing.T) {
	c := &Client{http: &http.Client{}}
	captureStderr(t, func() {
		if code := c.cmdAdmin([]string{"keys"}); code != 2 {
			t.Errorf("cmdAdmin([keys]) = %d; want 2", code)
		}
	})
}

func TestCmdAdmin_UnknownResource_Returns2(t *testing.T) {
	c := &Client{http: &http.Client{}}
	captureStderr(t, func() {
		if code := c.cmdAdmin([]string{"frobnicate", "list"}); code != 2 {
			t.Errorf("cmdAdmin([frobnicate list]) = %d; want 2", code)
		}
	})
}

// --------------------------------------------------------------------------
// cmdAdminKeys — dispatch
// --------------------------------------------------------------------------

func TestCmdAdminKeys_UnknownAction_Returns2(t *testing.T) {
	c := &Client{http: &http.Client{}}
	captureStderr(t, func() {
		if code := c.cmdAdminKeys("frob", nil); code != 2 {
			t.Errorf("cmdAdminKeys frob = %d; want 2", code)
		}
	})
}

func TestCmdAdminKeys_Add_TooFewArgs_Returns2(t *testing.T) {
	c := &Client{http: &http.Client{}}
	captureStderr(t, func() {
		if code := c.cmdAdminKeys("add", nil); code != 2 {
			t.Errorf("cmdAdminKeys add no args = %d; want 2", code)
		}
	})
}

func TestCmdAdminKeys_Revoke_TooFewArgs_Returns2(t *testing.T) {
	c := &Client{http: &http.Client{}}
	captureStderr(t, func() {
		if code := c.cmdAdminKeys("revoke", nil); code != 2 {
			t.Errorf("cmdAdminKeys revoke no args = %d; want 2", code)
		}
	})
}

// --------------------------------------------------------------------------
// adminKeysList
// --------------------------------------------------------------------------

func TestCmdAdminKeys_List_Success(t *testing.T) {
	var gotMethod, gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"keys":[{"token_hash":"abc","tenant_id":"acme","scopes":"read","label":"test"}]}`)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	out := captureStdout(t, func() {
		if code := c.cmdAdminKeys("list", nil); code != 0 {
			t.Errorf("adminKeys list = %d; want 0", code)
		}
	})
	if gotMethod != "GET" {
		t.Errorf("method = %q; want GET", gotMethod)
	}
	if gotPath != "/v1/admin/keys" {
		t.Errorf("path = %q; want /v1/admin/keys", gotPath)
	}
	if !strings.Contains(out, "abc") {
		t.Errorf("output %q missing token hash", out)
	}
}

func TestCmdAdminKeys_List_HTTPError_Returns1(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	var code int
	captureStderr(t, func() {
		code = c.cmdAdminKeys("list", nil)
	})
	if code != 1 {
		t.Errorf("adminKeys list on 500 = %d; want 1", code)
	}
}

// T13 — F-D byte pin: the readSuccessfulResponse 2xx branch is not migrated,
// so the keys-list success bytes stay byte-identical for scripts.
func TestCmdAdminKeys_List_2xx_SuccessBytes(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"keys":[{"token_hash":"abc","tenant_id":"acme","scopes":"read","label":"test"}]}`)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	out := captureStdout(t, func() {
		if code := c.cmdAdminKeys("list", nil); code != 0 {
			t.Errorf("adminKeys list = %d; want 0", code)
		}
	})
	want := fmt.Sprintf("%-40s tenant=%-20s scopes=%-15s label=%s\n", "abc", "acme", "read", "test")
	if out != want {
		t.Errorf("stdout = %q; want %q", out, want)
	}
}

// --------------------------------------------------------------------------
// adminKeysAdd
// --------------------------------------------------------------------------

func TestCmdAdminKeys_Add_Success(t *testing.T) {
	var gotMethod, gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"token_hash":"xyz"}`)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	captureStdout(t, func() {
		if code := c.cmdAdminKeys("add", []string{"my-token", "--scopes", "read,write", "--label", "my-key", "--tenant", "acme"}); code != 0 {
			t.Errorf("adminKeys add = %d; want 0", code)
		}
	})
	if gotMethod != "POST" {
		t.Errorf("method = %q; want POST", gotMethod)
	}
	if gotPath != "/v1/admin/keys" {
		t.Errorf("path = %q; want /v1/admin/keys", gotPath)
	}
}

func TestCmdAdminKeys_Add_MinimalArgs(t *testing.T) {
	var gotMethod, gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"token_hash":"min"}`)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	captureStdout(t, func() {
		if code := c.cmdAdminKeys("add", []string{"minimal-token"}); code != 0 {
			t.Errorf("adminKeys add minimal = %d; want 0", code)
		}
	})
	if gotMethod != "POST" {
		t.Errorf("method = %q; want POST", gotMethod)
	}
	if gotPath != "/v1/admin/keys" {
		t.Errorf("path = %q; want /v1/admin/keys", gotPath)
	}
}

func TestCmdAdminKeys_Add_HTTPError_Returns1(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "conflict", http.StatusConflict)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	var code int
	out := captureStderr(t, func() {
		code = c.cmdAdminKeys("add", []string{"tok", "--scopes", "read"})
	})
	if code != 1 {
		t.Errorf("adminKeys add on 409 = %d; want 1", code)
	}
	if !strings.Contains(out, "HTTP 409") {
		t.Errorf("stderr %q missing HTTP 409", out)
	}
}

// --------------------------------------------------------------------------
// adminKeysRevoke
// --------------------------------------------------------------------------

func TestCmdAdminKeys_Revoke_Success(t *testing.T) {
	var gotMethod, gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{}`)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	captureStdout(t, func() {
		if code := c.cmdAdminKeys("revoke", []string{"token-to-revoke"}); code != 0 {
			t.Errorf("adminKeys revoke = %d; want 0", code)
		}
	})
	if gotMethod != "DELETE" {
		t.Errorf("method = %q; want DELETE", gotMethod)
	}
	if gotPath != "/v1/admin/keys/token-to-revoke" {
		t.Errorf("path = %q; want /v1/admin/keys/token-to-revoke", gotPath)
	}
}

func TestCmdAdminKeys_Revoke_HTTPError_Returns1(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusGone)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	var code int
	out := captureStderr(t, func() {
		code = c.cmdAdminKeys("revoke", []string{"missing"})
	})
	if code != 1 {
		t.Errorf("adminKeys revoke on 410 = %d; want 1", code)
	}
	if !strings.Contains(out, "HTTP 410") {
		t.Errorf("stderr %q missing HTTP 410", out)
	}
}

// --------------------------------------------------------------------------
// cmdAdminTenants — list, create, delete, status
// --------------------------------------------------------------------------

func TestCmdAdminTenants_UnknownAction_Returns2(t *testing.T) {
	c := &Client{http: &http.Client{}}
	captureStderr(t, func() {
		if code := c.cmdAdminTenants("frob", nil); code != 2 {
			t.Errorf("cmdAdminTenants frob = %d; want 2", code)
		}
	})
}

func TestCmdAdminTenants_List_Success(t *testing.T) {
	var gotMethod, gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"tenants":[{"tenant_id":"acme","display_name":"Acme Corp","status":"active"}]}`)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	out := captureStdout(t, func() {
		if code := c.cmdAdminTenants("list", nil); code != 0 {
			t.Errorf("adminTenants list = %d; want 0", code)
		}
	})
	if gotMethod != "GET" {
		t.Errorf("method = %q; want GET", gotMethod)
	}
	if gotPath != "/v1/admin/tenants" {
		t.Errorf("path = %q; want /v1/admin/tenants", gotPath)
	}
	if !strings.Contains(out, "acme") {
		t.Errorf("output %q missing tenant id", out)
	}
}

func TestCmdAdminTenants_List_Empty(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tenants":[]}`)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	out := captureStdout(t, func() {
		if code := c.cmdAdminTenants("list", nil); code != 0 {
			t.Errorf("adminTenants list empty = %d; want 0", code)
		}
	})
	if out != "{\"tenants\":[]}\n" && out != "" {
		t.Logf("output was: %q", out)
	}
}

func TestCmdAdminTenants_List_HTTPError_Returns1(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	var code int
	captureStderr(t, func() {
		code = c.cmdAdminTenants("list", nil)
	})
	if code != 1 {
		t.Errorf("adminTenants list on 502 = %d; want 1", code)
	}
}

func TestCmdAdminTenants_Create_Success(t *testing.T) {
	var gotMethod, gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"tenant_id":"newco"}`)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	captureStdout(t, func() {
		if code := c.cmdAdminTenants("create", []string{"newco", "--display-name", "New Co"}); code != 0 {
			t.Errorf("adminTenants create = %d; want 0", code)
		}
	})
	if gotMethod != "POST" {
		t.Errorf("method = %q; want POST", gotMethod)
	}
	if gotPath != "/v1/admin/tenants" {
		t.Errorf("path = %q; want /v1/admin/tenants", gotPath)
	}
}

func TestCmdAdminTenants_Create_NoDisplayName(t *testing.T) {
	var gotMethod string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		fmt.Fprint(w, `{"tenant_id":"min"}`)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	captureStdout(t, func() {
		if code := c.cmdAdminTenants("create", []string{"min"}); code != 0 {
			t.Errorf("adminTenants create minimal = %d; want 0", code)
		}
	})
	if gotMethod != "POST" {
		t.Errorf("method = %q; want POST", gotMethod)
	}
}

func TestCmdAdminTenants_Create_TooFewArgs_Returns2(t *testing.T) {
	c := &Client{http: &http.Client{}}
	captureStderr(t, func() {
		if code := c.cmdAdminTenants("create", nil); code != 2 {
			t.Errorf("adminTenants create no args = %d; want 2", code)
		}
	})
}

func TestCmdAdminTenants_Delete_Success(t *testing.T) {
	var gotMethod, gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		fmt.Fprint(w, `{}`)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	captureStdout(t, func() {
		if code := c.cmdAdminTenants("delete", []string{"acme"}); code != 0 {
			t.Errorf("adminTenants delete = %d; want 0", code)
		}
	})
	if gotMethod != "DELETE" {
		t.Errorf("method = %q; want DELETE", gotMethod)
	}
	if gotPath != "/v1/admin/tenants/acme" {
		t.Errorf("path = %q; want /v1/admin/tenants/acme", gotPath)
	}
}

func TestCmdAdminTenants_Delete_HTTPError_Returns1(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	var code int
	out := captureStderr(t, func() {
		code = c.cmdAdminTenants("delete", []string{"missing"})
	})
	if code != 1 {
		t.Errorf("adminTenants delete on 404 = %d; want 1", code)
	}
	if !strings.Contains(out, "HTTP 404") {
		t.Errorf("stderr %q missing HTTP 404", out)
	}
}

func TestCmdAdminTenants_Delete_TooFewArgs_Returns2(t *testing.T) {
	c := &Client{http: &http.Client{}}
	captureStderr(t, func() {
		if code := c.cmdAdminTenants("delete", nil); code != 2 {
			t.Errorf("adminTenants delete no args = %d; want 2", code)
		}
	})
}

func TestCmdAdminTenants_Status_Success(t *testing.T) {
	var gotMethod, gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		fmt.Fprint(w, `{}`)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	captureStdout(t, func() {
		if code := c.cmdAdminTenants("status", []string{"acme", "suspended"}); code != 0 {
			t.Errorf("adminTenants status = %d; want 0", code)
		}
	})
	if gotMethod != "PUT" {
		t.Errorf("method = %q; want PUT", gotMethod)
	}
	if gotPath != "/v1/admin/tenants/acme/status" {
		t.Errorf("path = %q; want /v1/admin/tenants/acme/status", gotPath)
	}
}

func TestCmdAdminTenants_Status_HTTPError_Returns1(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	var code int
	out := captureStderr(t, func() {
		code = c.cmdAdminTenants("status", []string{"acme", "invalid"})
	})
	if code != 1 {
		t.Errorf("adminTenants status on 400 = %d; want 1", code)
	}
	if !strings.Contains(out, "HTTP 400") {
		t.Errorf("stderr %q missing HTTP 400", out)
	}
}

func TestCmdAdminTenants_Status_TooFewArgs_Returns2(t *testing.T) {
	c := &Client{http: &http.Client{}}
	captureStderr(t, func() {
		if code := c.cmdAdminTenants("status", []string{"acme"}); code != 2 {
			t.Errorf("adminTenants status 1 arg = %d; want 2", code)
		}
	})
}

// --------------------------------------------------------------------------
// strOrEmpty
// --------------------------------------------------------------------------

func TestStrOrEmpty_Nil_ReturnsEmpty(t *testing.T) {
	if got := strOrEmpty(nil); got != "" {
		t.Errorf("strOrEmpty(nil) = %q; want empty", got)
	}
}

func TestStrOrEmpty_String_ReturnsString(t *testing.T) {
	if got := strOrEmpty("hello"); got != "hello" {
		t.Errorf("strOrEmpty(\"hello\") = %q; want \"hello\"", got)
	}
}

func TestStrOrEmpty_NonString_ReturnsFormatted(t *testing.T) {
	if got := strOrEmpty(42); got != "42" {
		t.Errorf("strOrEmpty(42) = %q; want \"42\"", got)
	}
}

// --------------------------------------------------------------------------
// Run dispatch — admin keys and tenants (through cmdAdmin)
// --------------------------------------------------------------------------

func TestRun_AdminKeys_List_Dispatches(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"keys":[]}`)
	}))
	defer ts.Close()

	t.Setenv("AERO_ENDPOINT", ts.URL)
	t.Setenv("AERO_API_KEY", "")
	t.Setenv("AERO_TENANT", "")
	captureStdout(t, func() {
		if code := Run([]string{"admin", "keys", "list"}); code != 0 {
			t.Errorf("Run([admin keys list]) = %d; want 0", code)
		}
	})
	if gotPath != "/v1/admin/keys" {
		t.Errorf("path = %q; want /v1/admin/keys", gotPath)
	}
}

func TestRun_AdminTenants_List_Dispatches(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"tenants":[]}`)
	}))
	defer ts.Close()

	t.Setenv("AERO_ENDPOINT", ts.URL)
	t.Setenv("AERO_API_KEY", "")
	t.Setenv("AERO_TENANT", "")
	captureStdout(t, func() {
		if code := Run([]string{"admin", "tenants", "list"}); code != 0 {
			t.Errorf("Run([admin tenants list]) = %d; want 0", code)
		}
	})
	if gotPath != "/v1/admin/tenants" {
		t.Errorf("path = %q; want /v1/admin/tenants", gotPath)
	}
}

// T12 — F-D byte pin: bucket-rm 204 success output stays byte-identical for
// scripts (stdout exactly "bucket deleted\n", exit 0).
func TestCmdDeleteBucket_204_SuccessBytes(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	out := captureStdout(t, func() {
		if code := c.cmdDeleteBucket([]string{"old-bucket"}); code != 0 {
			t.Errorf("cmdDeleteBucket = %d; want 0", code)
		}
	})
	if out != "bucket deleted\n" {
		t.Errorf("stdout = %q; want %q", out, "bucket deleted\n")
	}
}

// --------------------------------------------------------------------------
// cmdAdminFiles — dispatch and admin files delete (AC-1 / F10 / F13)
// --------------------------------------------------------------------------

func TestCmdAdminFiles_UnknownAction_Returns2(t *testing.T) {
	c := &Client{http: &http.Client{}}
	out := captureStderr(t, func() {
		if code := c.cmdAdminFiles("frob", nil); code != 2 {
			t.Errorf("cmdAdminFiles frob = %d; want 2", code)
		}
	})
	if !strings.Contains(out, "unknown files action") {
		t.Errorf("stderr %q missing unknown action message", out)
	}
}

func TestCmdAdminFiles_Delete_TooFewArgs_Returns2(t *testing.T) {
	c := &Client{http: &http.Client{}}
	captureStderr(t, func() {
		if code := c.cmdAdminFiles("delete", nil); code != 2 {
			t.Errorf("cmdAdminFiles delete no args = %d; want 2", code)
		}
	})
	captureStderr(t, func() {
		if code := c.adminFilesDelete([]string{"acme"}); code != 2 {
			t.Errorf("adminFilesDelete one arg = %d; want 2", code)
		}
	})
}

// F13 — empty tenant must be rejected (exit 2) before any request: otherwise
// the server-side defaults("") would silently target the default tenant.
func TestCmdAdminFiles_Delete_EmptyTenant_Returns2(t *testing.T) {
	var hit int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	out := captureStderr(t, func() {
		if code := c.cmdAdminFiles("delete", []string{"", "k.txt", "--hard"}); code != 2 {
			t.Errorf("adminFilesDelete empty tenant = %d; want 2", code)
		}
	})
	if hit != 0 {
		t.Errorf("server received %d requests; want 0", hit)
	}
	if !strings.Contains(out, "tenant must not be empty") {
		t.Errorf("stderr %q missing empty-tenant message", out)
	}
}

func TestCmdAdminFiles_Delete_Success(t *testing.T) {
	var gotMethod, gotPath, gotQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	out := captureStdout(t, func() {
		if code := c.cmdAdminFiles("delete", []string{"acme", "docs/a.txt"}); code != 0 {
			t.Errorf("adminFilesDelete = %d; want 0", code)
		}
	})
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q; want DELETE", gotMethod)
	}
	if gotPath != "/v1/admin/files/acme/docs/a.txt" {
		t.Errorf("path = %q; want /v1/admin/files/acme/docs/a.txt (multi-segment key escaped per segment)", gotPath)
	}
	if gotQuery != "" {
		t.Errorf("query = %q; want empty without --hard", gotQuery)
	}
	if out != "deleted\n" {
		t.Errorf("stdout = %q; want %q", out, "deleted\n")
	}
}

func TestCmdAdminFiles_Delete_HardFlag(t *testing.T) {
	var gotPath, gotQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	captureStdout(t, func() {
		if code := c.cmdAdminFiles("delete", []string{"acme", "docs/a.txt", "--hard"}); code != 0 {
			t.Errorf("adminFilesDelete --hard = %d; want 0", code)
		}
	})
	if gotPath != "/v1/admin/files/acme/docs/a.txt" {
		t.Errorf("path = %q; want /v1/admin/files/acme/docs/a.txt", gotPath)
	}
	if gotQuery != "hard=1" {
		t.Errorf("query = %q; want hard=1", gotQuery)
	}
}

func TestCmdAdminFiles_Delete_HTTPError_Returns1(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	var code int
	out := captureStderr(t, func() {
		code = c.cmdAdminFiles("delete", []string{"acme", "missing.txt"})
	})
	if code != 1 {
		t.Errorf("adminFilesDelete on 404 = %d; want 1", code)
	}
	if !strings.Contains(out, "HTTP 404") {
		t.Errorf("stderr %q missing HTTP 404", out)
	}
}

// AC-1 — both usage surfaces list the new command.
func TestCmdAdminUsage_ListsFilesDelete(t *testing.T) {
	out := captureStderr(t, adminUsage)
	if !strings.Contains(out, "files delete <tenant> <key> [--hard]") {
		t.Errorf("adminUsage missing files delete entry:\n%s", out)
	}
	out2 := captureStderr(t, usage)
	if !strings.Contains(out2, "admin files delete <tenant> <key> [--hard]") {
		t.Errorf("top-level usage missing admin files delete entry:\n%s", out2)
	}
}

// --------------------------------------------------------------------------
// FR-1/FR-2 — non-numeric and negative tenant quota/budget args are rejected
// with exit code 2 before any HTTP request (AC-1/AC-2). Test names follow the
// mandated TestAdminTenantQuota/TestAdminTenantBudget prefixes so the AC-1
// acceptance filter is non-vacuous.
// --------------------------------------------------------------------------

func TestAdminTenantQuota_NonNumeric_Returns2(t *testing.T) {
	var hit int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit++
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	out := captureStderr(t, func() {
		if code := c.cmdAdminTenants("quota", []string{"acme", "abc", "xyz"}); code != 2 {
			t.Errorf("quota non-numeric = %d; want 2", code)
		}
	})
	if hit != 0 {
		t.Errorf("server received %d requests; want 0", hit)
	}
	if !strings.Contains(out, "max_bytes") {
		t.Errorf("stderr %q missing max_bytes role name", out)
	}
	if !strings.Contains(out, "usage:") {
		t.Errorf("stderr %q missing usage line", out)
	}
}

func TestAdminTenantQuota_Negative_Returns2(t *testing.T) {
	cases := [][]string{
		{"acme", "-5", "10"},
		{"acme", "10", "-5"},
	}
	for _, args := range cases {
		var hit int
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hit++
			w.WriteHeader(http.StatusOK)
		}))
		c := newTestClient(t, ts)
		captureStderr(t, func() {
			if code := c.cmdAdminTenants("quota", args); code != 2 {
				t.Errorf("quota %v = %d; want 2", args, code)
			}
		})
		ts.Close()
		if hit != 0 {
			t.Errorf("quota %v: server received %d requests; want 0", args, hit)
		}
	}
}

func TestAdminTenantQuota_TrailingGarbage_Returns2(t *testing.T) {
	var hit int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit++
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	captureStderr(t, func() {
		// "10abc" must be rejected by full-string parsing (Sscanf accepted it).
		if code := c.cmdAdminTenants("quota", []string{"acme", "10abc", "5"}); code != 2 {
			t.Errorf("quota trailing garbage = %d; want 2", code)
		}
	})
	if hit != 0 {
		t.Errorf("server received %d requests; want 0", hit)
	}
}

func TestAdminTenantQuota_ZeroAllowed(t *testing.T) {
	var hit int
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit++
		json.NewDecoder(r.Body).Decode(&gotBody)
		fmt.Fprint(w, `{"tenant":"acme"}`)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	captureStdout(t, func() {
		if code := c.cmdAdminTenants("quota", []string{"acme", "0", "0"}); code != 0 {
			t.Errorf("quota zero = %d; want 0", code)
		}
	})
	if hit != 1 {
		t.Errorf("server received %d requests; want 1", hit)
	}
	if mb, _ := gotBody["max_bytes"].(float64); mb != 0 {
		t.Errorf("max_bytes = %v; want 0", gotBody["max_bytes"])
	}
	if mo, _ := gotBody["max_objects"].(float64); mo != 0 {
		t.Errorf("max_objects = %v; want 0", gotBody["max_objects"])
	}
}

func TestAdminTenantBudget_NonNumeric_Returns2(t *testing.T) {
	var hit int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit++
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	out := captureStderr(t, func() {
		if code := c.cmdAdminTenants("budget", []string{"acme", "abc"}); code != 2 {
			t.Errorf("budget non-numeric = %d; want 2", code)
		}
	})
	if hit != 0 {
		t.Errorf("server received %d requests; want 0", hit)
	}
	if !strings.Contains(out, "daily_budget_usd") {
		t.Errorf("stderr %q missing daily_budget_usd role name", out)
	}
}

func TestAdminTenantBudget_Negative_Returns2(t *testing.T) {
	var hit int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit++
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	captureStderr(t, func() {
		if code := c.cmdAdminTenants("budget", []string{"acme", "-1.5"}); code != 2 {
			t.Errorf("budget negative = %d; want 2", code)
		}
	})
	if hit != 0 {
		t.Errorf("server received %d requests; want 0", hit)
	}
}

func TestAdminTenantBudget_NaN_Returns2(t *testing.T) {
	var hit int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit++
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	captureStderr(t, func() {
		// ParseFloat accepts "NaN" with err == nil; explicit IsNaN check required.
		if code := c.cmdAdminTenants("budget", []string{"acme", "NaN"}); code != 2 {
			t.Errorf("budget NaN = %d; want 2", code)
		}
	})
	if hit != 0 {
		t.Errorf("server received %d requests; want 0", hit)
	}
}

func TestAdminTenantBudget_ZeroAllowed(t *testing.T) {
	var hit int
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit++
		json.NewDecoder(r.Body).Decode(&gotBody)
		fmt.Fprint(w, `{"tenant":"acme"}`)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	captureStdout(t, func() {
		if code := c.cmdAdminTenants("budget", []string{"acme", "0"}); code != 0 {
			t.Errorf("budget zero = %d; want 0", code)
		}
	})
	if hit != 1 {
		t.Errorf("server received %d requests; want 1", hit)
	}
	if b, _ := gotBody["daily_budget_usd"].(float64); b != 0 {
		t.Errorf("daily_budget_usd = %v; want 0", gotBody["daily_budget_usd"])
	}
}
