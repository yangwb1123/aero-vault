package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/events"
	"github.com/aero-vault/aero-vault/internal/repository"
)

// ─────────────────────────────────────────────────────────────
// AC-2: MaxBodySize ring — oversize body → 413 (Content-Length
// early-reject path), with under-limit and exactly-at-limit
// control groups.
// ─────────────────────────────────────────────────────────────

func TestFullServer_MaxBodySize413(t *testing.T) {
	ts := startFullServerWithConfig(t, &events.EventOutboxRelayOptions{}, "",
		&config.Config{App: config.AppConfig{MaxBodySize: 1024}}).ts

	putStatus := func(body []byte) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodPut, ts.URL+"/v1/files/body-limit.txt", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("put %d-byte body: %v", len(body), err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}

	// Oversize (4x limit): early-reject 413 via Content-Length.
	if got := putStatus(bytes.Repeat([]byte("a"), 4096)); got != http.StatusRequestEntityTooLarge {
		t.Fatalf("4096-byte body: got %d, want 413", got)
	}
	// Exactly at limit: strict `>` semantics (RFC 9112 §6.3 / 9110 §15.5.11) —
	// ContentLength == maxBytes must NOT be rejected.
	if got := putStatus(bytes.Repeat([]byte("b"), 1024)); got < 200 || got >= 300 {
		t.Fatalf("1024-byte body (== limit): got %d, want 2xx", got)
	}
	// Under limit control: proves the 413 comes from the size gate, not some
	// other ring.
	if got := putStatus(bytes.Repeat([]byte("c"), 512)); got < 200 || got >= 300 {
		t.Fatalf("512-byte body: got %d, want 2xx", got)
	}
}

// ─────────────────────────────────────────────────────────────
// AC-3: SecureHeaders ring — security headers at the HTTP
// boundary, plus the outermost ring's X-Request-ID echo.
// ─────────────────────────────────────────────────────────────

func TestFullServer_SecureHeaders(t *testing.T) {
	ts := startFullServer(t)
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz: got %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options: got %q, want %q", got, "nosniff")
	}
	if got := resp.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("X-Frame-Options: got %q, want %q", got, "DENY")
	}
	// Outermost ring contract (QA P1 / protocol C4): RequestID must echo an
	// X-Request-ID at the HTTP boundary.
	if got := resp.Header.Get("X-Request-ID"); got == "" {
		t.Fatal("X-Request-ID header missing — request_id ring not applied")
	}
}

// ─────────────────────────────────────────────────────────────
// AC-4: TenantWithStatus ring — disabled tenant → 403
// TenantDisabled at a non-bypass path; unknown tenants pass;
// bypass paths stay 200 for disabled tenants.
// ─────────────────────────────────────────────────────────────

func TestFullServer_DisabledTenant403(t *testing.T) {
	h := startFullServerWithRelay(t, &events.EventOutboxRelayOptions{})
	ctx := context.Background()
	if err := h.repo.UpsertTenant(ctx, repository.TenantRecord{
		TenantID:    "suspended",
		DisplayName: "suspended",
		Status:      "disabled",
	}); err != nil {
		t.Fatalf("upsert disabled tenant: %v", err)
	}

	get := func(path, tenant string) (*http.Response, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, h.ts.URL+path, nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("X-Aero-Tenant", tenant)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("get %s as %q: %v", path, tenant, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(body)
	}

	// Disabled tenant on a non-bypass path → 403 with the exact static JSON
	// payload (security fold: no-bleed pin).
	resp, body := get("/v1/files", "suspended")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("disabled tenant /v1/files: got %d, want 403", resp.StatusCode)
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("403 body is not JSON: %q (%v)", body, err)
	}
	if payload.Error.Code != "TenantDisabled" {
		t.Fatalf("error.code: got %q, want %q", payload.Error.Code, "TenantDisabled")
	}
	const wantBody = `{"error":{"code":"TenantDisabled","message":"tenant is disabled"}}` + "\n"
	if body != wantBody {
		t.Fatalf("403 body bleeds: got %q, want %q", body, wantBody)
	}

	// Ghost control: unknown tenants stay allowed (back-compat implicit tenant).
	resp, _ = get("/v1/files", "ghost-tenant")
	if resp.StatusCode == http.StatusForbidden {
		t.Fatal("unknown tenant got 403 — implicit-tenant back-compat broken")
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unknown tenant /v1/files: got %d, want 200", resp.StatusCode)
	}

	// Bypass flip side (QA P1 / security C8): health probes and the web UI must
	// stay 200 for disabled tenants — LB probes must not 403 for a suspended
	// tenant, and the bypass list is a deliberate availability decision.
	resp, _ = get("/healthz", "suspended")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disabled tenant /healthz: got %d, want 200", resp.StatusCode)
	}
	resp, _ = get("/ui/", "suspended")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disabled tenant /ui/: got %d, want 200", resp.StatusCode)
	}
}

// ─────────────────────────────────────────────────────────────
// AC-4: MaxBodySize ring — chunked (unknown Content-Length)
// oversize upload must be rejected with 4xx and leave NO object
// or blob behind. Regression for the io.LimitReader bug that
// silently truncated oversize chunked bodies and stored corrupt
// objects with a 200 + ETag. Includes the REST write-path mapping
// (FR-2) and an under-limit chunked control group.
// ─────────────────────────────────────────────────────────────

func TestFullServer_MaxBodySizeChunkedS3Put413(t *testing.T) {
	h := startFullServerWithConfig(t, &events.EventOutboxRelayOptions{}, "",
		&config.Config{App: config.AppConfig{MaxBodySize: 1024}})

	// chunkedPut sends a real Transfer-Encoding: chunked request (unknown
	// length, ContentLength == -1) — the MaxBodySize early-reject path is
	// skipped and the body flows through the wrapping limit reader.
	chunkedPut := func(path string, body []byte) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPut, h.ts.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.ContentLength = -1
		req.TransferEncoding = []string{"chunked"}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("chunked put %d bytes to %s: %v", len(body), path, err)
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(respBody)
	}

	ctx := context.Background()
	const s3Path = "/s3/default/chunked-oversize.bin"

	// Oversize (4x limit) chunked S3 PUT: must be a 4xx — never a 200 that
	// stores a silently truncated object (the pre-fix behavior).
	status, _ := chunkedPut(s3Path, bytes.Repeat([]byte("a"), 4096))
	if status < 400 || status >= 500 {
		t.Fatalf("oversize chunked S3 PUT: got %d, want 4xx (target 413)", status)
	}

	// No residue at the HTTP layer: GET must 404 (S3 NoSuchKey).
	getResp, err := http.Get(h.ts.URL + s3Path)
	if err != nil {
		t.Fatalf("get after rejected put: %v", err)
	}
	_, _ = io.Copy(io.Discard, getResp.Body)
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after rejected put: got %d, want 404", getResp.StatusCode)
	}

	// No residue at the repository layer: writePutObject never ran.
	if _, err := h.repo.GetObject(ctx, "default", "default", "chunked-oversize.bin"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("repo lookup after rejected put: err = %v, want repository.ErrNotFound", err)
	}

	// Control group: identical chunked construction under the limit must
	// succeed and round-trip byte-identically — proves the 4xx comes from
	// the size gate, not from the chunked encoding itself.
	ctrl := bytes.Repeat([]byte("c"), 512)
	status, _ = chunkedPut("/s3/default/chunked-ok.bin", ctrl)
	if status < 200 || status >= 300 {
		t.Fatalf("under-limit chunked S3 PUT: got %d, want 2xx", status)
	}
	getResp, err = http.Get(h.ts.URL + "/s3/default/chunked-ok.bin")
	if err != nil {
		t.Fatalf("get under-limit object: %v", err)
	}
	got, _ := io.ReadAll(getResp.Body)
	getResp.Body.Close()
	if !bytes.Equal(got, ctrl) {
		t.Fatalf("under-limit round trip: got %d bytes, want %d identical", len(got), len(ctrl))
	}

	// FR-2 (D6b): the REST write path maps the same sentinel — chunked
	// oversize PUT to /v1/files must be 413 BodyTooLarge, not a 500 leak.
	status, body := chunkedPut("/v1/files/chunked-oversize.txt", bytes.Repeat([]byte("d"), 4096))
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize chunked REST PUT: got %d, want 413", status)
	}
	if !strings.Contains(body, "BodyTooLarge") {
		t.Fatalf("REST 413 body = %q, want code %q", body, "BodyTooLarge")
	}
}

// ─────────────────────────────────────────────────────────────
// AC-5: TestFullServer_CORS rewritten in place in fullserver_test.go
// (FR-6) — this file holds AC-2/3/4 only.
// ─────────────────────────────────────────────────────────────
