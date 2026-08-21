package integration

// AC-2 integration leg + AC-4 composition e2e for the admin files delete
// direction: real server (with an ENABLED auth registry so requireAdmin is
// non-vacuous) + real CLI (cli.Run driven by AERO_* env) + DSN-direct outbox
// assertions. All claims are behavioral; no Repository method was added.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/aero-vault/aero-vault/internal/cli"
	"github.com/aero-vault/aero-vault/internal/events"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
)

// ── DSN helpers (R2: event_outbox_delivered has only outbox_id +
// delivered_at_ns; "exactly one" needs the JOIN on event_outbox by
// origin_id + event_type) ────────────────────────────────────────────────────

func deliveredCountFor(t *testing.T, dsn string, originID int64, eventType string) int {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM event_outbox_delivered d
JOIN event_outbox o ON o.id = d.outbox_id
WHERE o.origin_id=? AND o.event_type=?`, originID, eventType).Scan(&n); err != nil {
		t.Fatalf("count delivered rows: %v", err)
	}
	return n
}

func deliveredAt(t *testing.T, dsn string, originID int64, eventType string) int64 {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var ns int64
	if err := db.QueryRow(`SELECT d.delivered_at_ns FROM event_outbox_delivered d
JOIN event_outbox o ON o.id = d.outbox_id
WHERE o.origin_id=? AND o.event_type=? ORDER BY d.outbox_id DESC LIMIT 1`, originID, eventType).Scan(&ns); err != nil {
		t.Fatalf("read delivered_at_ns: %v", err)
	}
	return ns
}

func deliveredTotal(t *testing.T, dsn string) int {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM event_outbox_delivered`).Scan(&n); err != nil {
		t.Fatalf("count delivered table: %v", err)
	}
	return n
}

// putObjectAs seeds an object through the real REST upload path with the
// operator key (the enabled-registry harness requires Authorization).
func putObjectAs(t *testing.T, h *fullServerHarness, tenant, key string) repository.Object {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, h.ts.URL+"/v1/files/"+key, strings.NewReader("body"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Authorization", "Bearer opsecret")
	if tenant != "" {
		req.Header.Set(mw.TenantHeader, tenant)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT %s (tenant=%s): status=%d", key, tenant, resp.StatusCode)
	}
	obj, err := h.repo.GetObject(context.Background(), tenant, "default", key)
	if err != nil {
		t.Fatal(err)
	}
	return obj
}

// TestAC2_AdminDelete_EventTypeFilteredState — AC-2 integration leg: the real
// admin path commits exactly one deleted@1.1 + one notify@1.1 fact (pending —
// no relay), with the full payload contract, and zero delivered rows.
func TestAC2_AdminDelete_EventTypeFilteredState(t *testing.T) {
	h := startFullServerWithAuthAndRelay(t, nil, "opsecret:*:admin")
	ts := h.ts
	ctx := context.Background()

	obj := putObjectAs(t, h, "acme", "docs/a.txt")
	if obj.ID <= 0 {
		t.Fatal("seeded object has no ID")
	}

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/v1/admin/files/acme/docs/a.txt?hard=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer opsecret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("admin DELETE status=%d, want 204", resp.StatusCode)
	}

	if n := outboxCountFor(t, h.dsn, obj.ID, repository.EventTypeFileDeleted11); n != 1 {
		t.Errorf("deleted@1.1 rows = %d, want exactly 1", n)
	}
	if n := outboxCountFor(t, h.dsn, obj.ID, repository.EventTypeFileNotify11); n != 1 {
		t.Errorf("notify@1.1 rows = %d, want exactly 1", n)
	}
	for _, et := range []string{"vault.file.deleted@1.1", "vault.file.notify@1.1"} {
		if status := outboxStatus(t, h.dsn, obj.ID, et); status != "pending" {
			t.Errorf("fact %s status = %q, want pending (no relay)", et, status)
		}
	}
	payload := outboxPayloadFor(t, h.dsn, obj.ID, repository.EventTypeFileDeleted11)
	for _, want := range []string{`"schema_version":"1.1"`, `"tenant":"acme"`, `"key":"docs/a.txt"`} {
		if !strings.Contains(payload, want) {
			t.Errorf("deleted@1.1 payload missing %s: %s", want, payload)
		}
	}
	if n := deliveredTotal(t, h.dsn); n != 0 {
		t.Errorf("event_outbox_delivered rows = %d, want 0 (no relay)", n)
	}
	assertAuditRowFor(t, h.repo, "acme", "hard;permission=vault.file.delete")
	if _, err := h.repo.GetObject(ctx, "acme", "default", "docs/a.txt"); err == nil {
		t.Error("object still readable after admin hard delete")
	}
}

// TestComposition_AdminFilesDeleteEndToEnd — AC-4: real CLI against a real
// server with an enabled registry. Leg 1 is the signal-based non-blocking
// proof (relay active, L2 blocked, 4s hang-guard < 5s relay timeout); Leg 2
// proves relay-down deletion (F9) and start-later recovery with the JOIN-based
// delivered assertions (R2).
func TestComposition_AdminFilesDeleteEndToEnd(t *testing.T) {
	t.Run("leg1 signal-based non-blocking with blocked L2", func(t *testing.T) {
		release := make(chan struct{})
		var releaseOnce sync.Once
		l2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-release // block the L2 POST until the test releases it
			_, _ = io.Copy(io.Discard, r.Body)
			// Echo receipt (D5/FR-2): the relay completes only on 2xx WITH the
			// exact X-Audit-Fact-Id echo (audit_sink_l2.go); a 2xx without it
			// is an error and the fact would retry to terminal failed, never
			// reaching "delivered" — which this leg asserts.
			w.Header().Set("X-Audit-Fact-Id", r.Header.Get("X-Audit-Fact-Id"))
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(l2.Close)
		t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

		sink, err := events.NewAuditSinkL2(l2.URL, map[string]string{"acme": "e2e-l2-token-0123456789"},
			&http.Client{Timeout: 5 * time.Second}, nil)
		if err != nil {
			t.Fatal(err)
		}
		h := startFullServerWithAuthAndRelay(t, &events.EventOutboxRelayOptions{
			PollInterval: 50 * time.Millisecond, BatchSize: 32,
			ClaimTTL: 30 * time.Second, HTTPTimeout: 5 * time.Second,
			MaxAttempts: 3, AuditSink: sink,
		}, "opsecret:*:admin")

		obj := putObjectAs(t, h, "acme", "docs/a.txt")

		t.Setenv("AERO_ENDPOINT", h.ts.URL)
		t.Setenv("AERO_API_KEY", "opsecret")
		t.Setenv("AERO_TENANT", "") // must be pinned: cli.do only sends X-Aero-Tenant when non-empty

		done := make(chan cliResult, 1)
		var stdout string
		go func() {
			// Capture happens inside the goroutine and the result is sent only
			// after both captures complete — a bare channel-send before the
			// stdout assignment races with the select below and can surface
			// stdout == "".
			var res cliResult
			res.stdout = captureStdout(t, func() {
				res.stderr = captureStderr(t, func() {
					res.code = cli.Run([]string{"admin", "files", "delete", "acme", "docs/a.txt", "--hard"})
				})
			})
			done <- res
		}()

		// L2 still blocked here: a synchronous implementation cannot return
		// (its POST would hang past the 5s relay timeout; 4s < 5s is the
		// deterministic discriminator).
		select {
		case res := <-done:
			if res.code != 0 {
				t.Fatalf("cli.Run = %d; want 0 (stderr %q)", res.code, res.stderr)
			}
			stdout = res.stdout
		case <-time.After(4 * time.Second):
			t.Fatal("admin files delete blocked behind L2 delivery (durable_async violated)")
		}
		if stdout != "deleted\n" {
			t.Errorf("stdout = %q, want %q", stdout, "deleted\n")
		}
		status := outboxStatus(t, h.dsn, obj.ID, "vault.file.deleted@1.1")
		if status != "pending" && status != "inflight" {
			t.Fatalf("deleted@1.1 status = %q, want pending or inflight while target blocked", status)
		}

		releaseOnce.Do(func() { close(release) })
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if outboxStatus(t, h.dsn, obj.ID, "vault.file.deleted@1.1") == "delivered" {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("deleted@1.1 never reached delivered after target recovery")
	})

	t.Run("leg2 relay-down delete succeeds, start-later relay recovers", func(t *testing.T) {
		h := startFullServerWithAuthAndRelay(t, nil, "opsecret:*:admin") // no relay (F9)
		ts := h.ts

		obj := putObjectAs(t, h, "acme", "docs/a.txt")

		t.Setenv("AERO_ENDPOINT", ts.URL)
		t.Setenv("AERO_API_KEY", "opsecret")
		t.Setenv("AERO_TENANT", "")

		code := cli.Run([]string{"admin", "files", "delete", "acme", "docs/a.txt", "--hard"})
		if code != 0 {
			t.Fatalf("cli.Run with relay down = %d; want 0 (durable_async, F9)", code)
		}

		// Object gone; share invalidation is covered at the service level, so
		// here we assert the HTTP-visible state.
		getReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/files/docs/a.txt", nil)
		getReq.Header.Set(mw.TenantHeader, "acme")
		getReq.Header.Set("Authorization", "Bearer opsecret")
		getResp, err := http.DefaultClient.Do(getReq)
		if err != nil {
			t.Fatal(err)
		}
		getResp.Body.Close()
		if getResp.StatusCode != http.StatusNotFound {
			t.Errorf("GET after admin delete = %d, want 404", getResp.StatusCode)
		}

		// Both facts stuck pending while no relay runs.
		for _, et := range []string{"vault.file.deleted@1.1", "vault.file.notify@1.1"} {
			if status := outboxStatus(t, h.dsn, obj.ID, et); status != "pending" {
				t.Errorf("fact %s status = %q, want pending (relay down)", et, status)
			}
		}

		// Start-later relay wiring (the harness has no start-later API when
		// relayOpts == nil; construct inline).
		relay := events.NewEventOutboxRelay(h.repo,
			slog.New(slog.NewTextHandler(io.Discard, nil)), events.EventOutboxRelayOptions{
				PollInterval: 50 * time.Millisecond, BatchSize: 32,
				ClaimTTL: 30 * time.Second, HTTPTimeout: 5 * time.Second,
				MaxAttempts: 3,
			})
		relayCtx, relayCancel := context.WithCancel(context.Background())
		go relay.Run(relayCtx)
		t.Cleanup(relayCancel)

		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if outboxStatus(t, h.dsn, obj.ID, "vault.file.deleted@1.1") == "delivered" &&
				outboxStatus(t, h.dsn, obj.ID, "vault.file.notify@1.1") == "delivered" {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if time.Now().After(deadline) {
			t.Fatalf("facts never delivered: deleted=%q notify=%q",
				outboxStatus(t, h.dsn, obj.ID, "vault.file.deleted@1.1"),
				outboxStatus(t, h.dsn, obj.ID, "vault.file.notify@1.1"))
		}
		if ns := deliveredAt(t, h.dsn, obj.ID, "vault.file.deleted@1.1"); ns <= 0 {
			t.Errorf("delivered_at_ns = %d, want > 0", ns)
		}
		// JOIN-based "exactly one" (R2): unfiltered total is 2, so an
		// event_type-less count assertion would be wrong.
		if n := deliveredCountFor(t, h.dsn, obj.ID, "vault.file.deleted@1.1"); n != 1 {
			t.Errorf("delivered deleted@1.1 rows = %d, want exactly 1 (joined)", n)
		}
		if n := deliveredCountFor(t, h.dsn, obj.ID, "vault.file.notify@1.1"); n != 1 {
			t.Errorf("delivered notify@1.1 rows = %d, want exactly 1 (joined)", n)
		}
		if n := deliveredTotal(t, h.dsn); n != 2 {
			t.Errorf("unfiltered delivered rows = %d, want 2 (proves join required)", n)
		}

		// Audit row visible through the admin surface.
		auditReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/admin/audit?limit=10", nil)
		auditReq.Header.Set("Authorization", "Bearer opsecret")
		auditResp, err := http.DefaultClient.Do(auditReq)
		if err != nil {
			t.Fatal(err)
		}
		defer auditResp.Body.Close()
		var env struct {
			Audit []json.RawMessage `json:"audit"`
		}
		if err := json.NewDecoder(auditResp.Body).Decode(&env); err != nil {
			t.Fatalf("decode audit list: %v", err)
		}
		found := false
		for _, raw := range env.Audit {
			var row struct {
				Action   string `json:"action"`
				TenantID string `json:"tenant_id"`
				Detail   string `json:"detail"`
			}
			_ = json.Unmarshal(raw, &row)
			if row.Action == "file.delete" && row.TenantID == "acme" && row.Detail == "hard;permission=vault.file.delete" {
				found = true
			}
		}
		if !found {
			t.Error("audit list missing file.delete/hard row for acme")
		}

	})
}

// cliResult carries the CLI exit code together with the stdout/stderr
// captured during the run (assigned before the channel send, so receivers
// never observe a partially-captured result).
type cliResult struct {
	code   int
	stdout string
	stderr string
}

// captureStdout mirrors the CLI test helper (this package only has
// captureStderr): replaces os.Stdout for the duration of fn.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r) //nolint — read error does not matter here
	r.Close()
	return buf.String()
}
