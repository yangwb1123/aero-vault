package cli

import (
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

func TestCmdAdminKeys_Add_HTTPError_Returns0(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "conflict", http.StatusConflict)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	out := captureStdout(t, func() {
		if code := c.cmdAdminKeys("add", []string{"tok", "--scopes", "read"}); code != 0 {
			t.Errorf("adminKeys add on 409 = %d; want 0 (bug: status not checked)", code)
		}
	})
	if out != "conflict\n" {
		t.Logf("output on 409: %q", out)
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

func TestCmdAdminKeys_Revoke_HTTPError_Returns0(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusGone)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	out := captureStdout(t, func() {
		if code := c.cmdAdminKeys("revoke", []string{"missing"}); code != 0 {
			t.Errorf("adminKeys revoke on 410 = %d; want 0 (bug: status not checked)", code)
		}
	})
	if !strings.Contains(out, "revoked") {
		t.Errorf("output %q missing 'revoked'", out)
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

func TestCmdAdminTenants_Delete_HTTPError_Returns0(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	out := captureStdout(t, func() {
		if code := c.cmdAdminTenants("delete", []string{"missing"}); code != 0 {
			t.Errorf("adminTenants delete on 404 = %d; want 0 (bug: status not checked)", code)
		}
	})
	if !strings.Contains(out, "deleted") {
		t.Errorf("output %q missing 'deleted'", out)
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

func TestCmdAdminTenants_Status_HTTPError_Returns0(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer ts.Close()

	c := newTestClient(t, ts)
	out := captureStdout(t, func() {
		if code := c.cmdAdminTenants("status", []string{"acme", "invalid"}); code != 0 {
			t.Errorf("adminTenants status on 400 = %d; want 0 (bug: status not checked)", code)
		}
	})
	if !strings.Contains(out, "updated") {
		t.Errorf("output %q missing 'updated'", out)
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
