// Relay-driven acceptance for the WebDAV delete surface
// (docs/requirements/durable-async-delete-outbox-webdav-v1.md, G2/G3):
// the DELETE response never waits on outbox delivery (AC-2), and the L1
// (webhook HMAC + notify rule, D2 dedupe live) and L2 (AuditSinkL2
// bound/unbound tenant) faces all compose from the WebDAV surface (AC-3).
// Harness timings honor C-6 (ClaimTTL > 2×HTTPTimeout): 60s/10s, both
// config-valid (60 > 20, ≤600 cap; 10 ≤29 cap); programmatic relay opts
// bypass config.Validate, so the harness self-honors the bound.
package webdav_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	dav "github.com/aero-vault/aero-vault/internal/api/webdav"
	"github.com/aero-vault/aero-vault/internal/events"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// webdavBusWiring enables the production bus shape (Bus + Notifier +
// Webhook, mirroring cmd/server/workers.go:61-80) — without it the D2
// dedupe (notifier's HasEventOutboxFact skip) is dead code in module tests.
type webdavBusWiring struct {
	webhookURL    string
	webhookSecret string
}

// newWebdavRelayHarness builds the WebDAV surface with an always-on relay:
// SQLite + local FS + FileService + mw.Tenant(webdav.Handler) + relay; with
// busWiring != nil the bus + notifier (+ webhook) are wired. Teardown order
// (LIFO): relayCancel → bus.Close → whCancel → notifCancel → srv.Close →
// repo.Close (registered in reverse below).
func newWebdavRelayHarness(t *testing.T, relayOpts *events.EventOutboxRelayOptions, busWiring *webdavBusWiring) (*httptest.Server, *service.FileService, string) {
	t.Helper()
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "x.db")
	repo, err := repository.Open(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("local storage: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.NewFileService(store, repo, logger).WithAuthorizer(allowAllProvider{})
	if busWiring != nil {
		bus := events.New(repo, logger)
		svc.WithEventSink(bus)
		notif := events.NewNotifier(repo, logger)
		notifCtx, notifCancel := context.WithCancel(ctx)
		sub, _ := bus.Subscribe()
		go notif.Run(notifCtx, sub)
		t.Cleanup(notifCancel)
		if busWiring.webhookURL != "" {
			wh := events.NewWebhook(busWiring.webhookURL, logger)
			if busWiring.webhookSecret != "" {
				wh.WithSecret(busWiring.webhookSecret)
			}
			whCtx, whCancel := context.WithCancel(ctx)
			sub2, _ := bus.Subscribe()
			go wh.Run(whCtx, sub2)
			t.Cleanup(whCancel)
		}
		t.Cleanup(bus.Close)
	}
	h := mw.Tenant(dav.Handler("/webdav", svc, logger))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	if relayOpts != nil {
		relay := events.NewEventOutboxRelay(repo, logger, *relayOpts)
		relayCtx, relayCancel := context.WithCancel(ctx)
		go relay.Run(relayCtx)
		t.Cleanup(relayCancel)
	}
	return srv, svc, dsn
}

// G2 (AC-2): the WebDAV DELETE response must not wait on outbox delivery.
// The L2 target blocks on a release channel while the DELETE is issued
// synchronously: the returned response is the timing witness — a synchronous
// implementation could not return while the POST is blocked (it would hang
// past the 10s relay timeout; the release only lands after the DELETE
// returns, so the test would hang until go test's package timeout — a slow
// fail, never a false pass).
func TestWebDAVDelete_ResponseDoesNotBlockOnDelivery(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	var mu sync.Mutex
	var posts int
	var gotFactID string
	l2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // block the L2 POST until the test releases it
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		posts++
		gotFactID = r.Header.Get("X-Audit-Fact-Id")
		mu.Unlock()
		w.Header().Set("X-Audit-Fact-Id", r.Header.Get("X-Audit-Fact-Id"))
		w.WriteHeader(http.StatusOK)
	}))
	// Cleanup order (LIFO): release → l2.Close → relay cancel → ts.Close →
	// repo.Close. close(release) MUST run before l2.Close so the in-flight
	// POST completes instead of leaking a goroutine under -race.
	t.Cleanup(l2.Close)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	sink, err := events.NewAuditSinkL2(l2.URL, map[string]string{"default": "e2e-l2-token-0123456789"},
		&http.Client{Timeout: 10 * time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts, svc, dsn := newWebdavRelayHarness(t, &events.EventOutboxRelayOptions{
		PollInterval: 50 * time.Millisecond, BatchSize: 32,
		ClaimTTL: 60 * time.Second, HTTPTimeout: 10 * time.Second,
		MaxAttempts: 10, AuditSink: sink,
	}, nil)
	repo := svc.Repo()
	ctx := context.Background()

	do(t, ts, http.MethodPut, "/webdav/k.txt", []byte("body"), nil)
	obj, err := repo.GetObject(ctx, "default", "default", "k.txt")
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	// Release-before-timeout invariant (sibling style, fullserver_test.go):
	// close(release) MUST land well below the 10s HTTPTimeout — the work
	// between DELETE-return and close(release) is one SQLite SELECT (µs–ms),
	// ~100× margin even under pathological -race scheduling; "exactly 1
	// POST" is hard by design (a late release burns attempt 1 and the
	// backoff retry produces POST #2), so never add work between the DELETE
	// and the release.
	resp, _ := do(t, ts, http.MethodDelete, "/webdav/k.txt", nil, nil)
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE: got %d, want 204 or 200 (returned while L2 target blocked)", resp.StatusCode)
	}

	// L0 decoupled: the audit row exists while delivery is still blocked.
	rows := auditRowsFor(t, repo, "default")
	if len(rows) != 1 || rows[0].Detail != "hard" {
		t.Fatalf("audit rows = %+v, want exactly 1 hard row while delivery blocked", rows)
	}
	// The deleted@1.1 fact must not be delivered while the target blocks:
	// pending and inflight are both legal and race-free — delivered is
	// unreachable (complete requires the echo receipt, which requires the
	// release). No delete rule is set (FM-7), so notify@1.1 completes
	// silently with 0 POSTs by construction.
	factID, status, _ := outboxRow(t, dsn, obj.ID, "vault.file.deleted@1.1")
	if status != "pending" && status != "inflight" {
		t.Fatalf("deleted@1.1 status = %q, want pending or inflight while target blocked", status)
	}

	// Recovery: release the target; the in-flight POST completes (200+echo)
	// and the relay completes the fact (in-flight POST ≤10s + 50ms poll →
	// ~15s bound).
	releaseOnce.Do(func() { close(release) })
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if outboxStatus(t, dsn, obj.ID, "vault.file.deleted@1.1") == "delivered" &&
			outboxStatus(t, dsn, obj.ID, "vault.file.notify@1.1") == "delivered" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if time.Now().After(deadline) {
		t.Fatalf("facts never reached delivered: deleted=%q notify=%q",
			outboxStatus(t, dsn, obj.ID, "vault.file.deleted@1.1"),
			outboxStatus(t, dsn, obj.ID, "vault.file.notify@1.1"))
	}
	mu.Lock()
	got := posts
	factIDHeader := gotFactID
	mu.Unlock()
	if got != 1 {
		t.Fatalf("L2 received %d POSTs, want exactly 1", got)
	}
	if factIDHeader != fmt.Sprintf("%d", factID) {
		t.Fatalf("L2 X-Audit-Fact-Id = %q, want %d (the event_outbox row id)", factIDHeader, factID)
	}

	// No-dup window: fixed 5s (≥5× PollInterval; -race/loaded-CI headroom),
	// state-witnessed — complete is state-based (the claim predicate excludes
	// 'delivered'), so a relay-side redelivery is impossible; the window
	// guards any claim-predicate regression.
	time.Sleep(5 * time.Second)
	mu.Lock()
	after := posts
	mu.Unlock()
	if after != 1 {
		t.Fatalf("duplicate L2 POST after complete: %d→%d", got, after)
	}
	if outboxStatus(t, dsn, obj.ID, "vault.file.deleted@1.1") != "delivered" ||
		outboxStatus(t, dsn, obj.ID, "vault.file.notify@1.1") != "delivered" {
		t.Fatal("facts left delivered state after the no-dup window")
	}
}

// G3 (AC-3): L1 + L2 composition from the WebDAV surface. A WebDAV DELETE
// commits both facts; the relay delivers notify@1.1 to the rule target
// byte-verbatim while the bus notifier skips it (D2 — exactly one notify
// POST), the webhook worker HMAC-POSTs the legacy deleted event, and the L2
// AuditSink receives exactly one deleted@1.1 POST carrying the tenant's
// bearer token and the X-Audit-Fact-Id echo receipt. An unbound tenant
// degrades: 0 L2 POSTs, audit row still written (L0 always-on), notify fact
// silent-completes (rule is default-tenant-scoped).
func TestWebDAVDelete_CompositionL1L2(t *testing.T) {
	// notify target: the relay POSTs the @1.1 envelope verbatim.
	var nMu sync.Mutex
	var nBodies [][]byte
	notify := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		nMu.Lock()
		nBodies = append(nBodies, body)
		nMu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(notify.Close)

	// L2: deleted@1.1 audit sink (needs the X-Audit-Fact-Id echo receipt).
	var l2Mu sync.Mutex
	var l2Bodies [][]byte
	var l2Auth []string
	var l2FactIDs []string
	l2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		l2Mu.Lock()
		l2Bodies = append(l2Bodies, body)
		l2Auth = append(l2Auth, r.Header.Get("Authorization"))
		l2FactIDs = append(l2FactIDs, r.Header.Get("X-Audit-Fact-Id"))
		l2Mu.Unlock()
		w.Header().Set("X-Audit-Fact-Id", r.Header.Get("X-Audit-Fact-Id"))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(l2.Close)

	// webhook worker (legacy bus shape, HMAC-SHA256 signed). Note: PUT emits
	// EventCreated (file_crud.go:255) and DELETE emits EventDeleted — the
	// webhook receives BOTH legacy events, so assertions are type-filtered
	// (created vs deleted), never raw-counted.
	const secret = "webdav-g3-secret"
	var wMu sync.Mutex
	var wBodies [][]byte
	var wSigs []string
	var wEventIDs []string
	whTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		wMu.Lock()
		wBodies = append(wBodies, body)
		wSigs = append(wSigs, r.Header.Get("X-Aero-Signature"))
		wEventIDs = append(wEventIDs, r.Header.Get("X-Aero-Event-Id"))
		wMu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(whTarget.Close)

	const l2Token = "webdav-g3-l2-token-0123456789"
	sink, err := events.NewAuditSinkL2(l2.URL, map[string]string{"default": l2Token},
		&http.Client{Timeout: 10 * time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts, svc, dsn := newWebdavRelayHarness(t, &events.EventOutboxRelayOptions{
		PollInterval: 50 * time.Millisecond, BatchSize: 32,
		ClaimTTL: 60 * time.Second, HTTPTimeout: 10 * time.Second,
		MaxAttempts: 10, AuditSink: sink,
	}, &webdavBusWiring{webhookURL: whTarget.URL, webhookSecret: secret})
	repo := svc.Repo()
	ctx := context.Background()

	// FM-7: the rule must exist BEFORE the DELETE — deliverNotify completes
	// silently with zero matching rules (a late insert = 0 POSTs and the
	// len==1 guard below fails loudly).
	setDeleteRule(t, repo, notify.URL)

	// ① Bound tenant "default": PUT → DELETE.
	do(t, ts, http.MethodPut, "/webdav/k.txt", []byte("body"), nil)
	obj, err := repo.GetObject(ctx, "default", "default", "k.txt")
	if err != nil {
		t.Fatal(err)
	}
	resp, _ := do(t, ts, http.MethodDelete, "/webdav/k.txt", nil, nil)
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE: got %d, want 204 or 200", resp.StatusCode)
	}
	assertAuditRowFor(t, repo, "default", "hard")

	// Both facts reach delivered ≤15s (healthy ~1-2s: next 50ms poll + local
	// POSTs). A POST completes before its fact completes, so when the status
	// flips the counters are already settled.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if outboxStatus(t, dsn, obj.ID, "vault.file.deleted@1.1") == "delivered" &&
			outboxStatus(t, dsn, obj.ID, "vault.file.notify@1.1") == "delivered" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if time.Now().After(deadline) {
		t.Fatalf("facts never reached delivered: deleted=%q notify=%q",
			outboxStatus(t, dsn, obj.ID, "vault.file.deleted@1.1"),
			outboxStatus(t, dsn, obj.ID, "vault.file.notify@1.1"))
	}

	// Exactly-once, absolute: counters must equal 1 at the moment both facts
	// are delivered. Guard len==1 before touching any body — a 0-body read
	// would panic, a 2-body read must Fatal, not compare.
	nMu.Lock()
	nGot := len(nBodies)
	nBody := append([][]byte(nil), nBodies...)
	nMu.Unlock()
	l2Mu.Lock()
	l2Got := len(l2Bodies)
	l2Body := append([][]byte(nil), l2Bodies...)
	l2AuthGot := append([]string(nil), l2Auth...)
	l2FactIDsGot := append([]string(nil), l2FactIDs...)
	l2Mu.Unlock()
	wMu.Lock()
	wGot := len(wBodies)
	wBody := append([][]byte(nil), wBodies...)
	wSig := append([]string(nil), wSigs...)
	wEventID := append([]string(nil), wEventIDs...)
	wMu.Unlock()
	if nGot != 1 {
		t.Fatalf("notify target received %d POSTs, want exactly 1 (D2 bus-path duplicate or relay redelivery)", nGot)
	}
	if l2Got != 1 {
		t.Fatalf("L2 received %d POSTs, want exactly 1", l2Got)
	}
	// The webhook sees both legacy bus events (PUT→created, DELETE→deleted).
	whCreated, whDeleted := countWebhookTypes(wBodies)
	if wGot != 2 || whCreated != 1 || whDeleted != 1 {
		t.Fatalf("webhook POSTs = %d (created=%d deleted=%d), want exactly 1 created + 1 deleted",
			wGot, whCreated, whDeleted)
	}

	// Notify: wire body byte-verbatim (relay POSTs the stored row — no
	// re-derivation) + ground-truth content pins.
	if rowPayload := outboxPayload(t, dsn, obj.ID, "vault.file.notify@1.1"); !bytes.Equal(nBody[0], rowPayload) {
		t.Fatalf("notify wire body != stored payload (relay re-derived/transformed):\n wire: %s\n  row: %s",
			nBody[0], rowPayload)
	}
	assertNotifyFact(t, nBody[0], obj)

	// Webhook: HMAC-SHA256 over the exact body, event-id header, legacy
	// shape (type=="deleted").
	var delIdx = -1
	for i, b := range wBody {
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		if m["type"] == "deleted" {
			delIdx = i
			break
		}
	}
	if delIdx < 0 {
		t.Fatal("webhook never received the deleted event")
	}
	if wEventID[delIdx] == "" {
		t.Fatal("webhook missing X-Aero-Event-Id header")
	}
	if !strings.HasPrefix(wSig[delIdx], "sha256=") {
		t.Fatalf("webhook signature %q does not start with sha256=", wSig[delIdx])
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(wBody[delIdx])
	if want := "sha256=" + hex.EncodeToString(mac.Sum(nil)); wSig[delIdx] != want {
		t.Fatalf("webhook HMAC mismatch:\n  got:  %s\n  want: %s", wSig[delIdx], want)
	}

	// L2: bearer token, fact-id header == the outbox row id, envelope
	// identity.
	factID, _, _ := outboxRow(t, dsn, obj.ID, "vault.file.deleted@1.1")
	if l2AuthGot[0] != "Bearer "+l2Token {
		t.Fatalf("L2 Authorization = %q, want the bound tenant's bearer token", l2AuthGot[0])
	}
	if l2FactIDsGot[0] != fmt.Sprintf("%d", factID) {
		t.Fatalf("L2 X-Audit-Fact-Id = %q, want %d (the event_outbox row id)", l2FactIDsGot[0], factID)
	}
	if !strings.Contains(string(l2Body[0]), `"event_type":"vault.file.deleted@1.1"`) ||
		!strings.Contains(string(l2Body[0]), `"tenant":"default"`) ||
		!strings.Contains(string(l2Body[0]), `"object_id":`+fmt.Sprintf("%d", obj.ID)) {
		t.Errorf("L2 payload missing identity: %s", string(l2Body[0]))
	}

	// ② Unbound tenant "other": 0 L2 POSTs (ErrSinkNotBound → graceful
	// complete), audit row still written (L0 always-on), notify fact
	// silent-completes (rule is default-tenant-scoped), webhook grows to 2
	// (the bus is tenant-agnostic — assert exactly this).
	hdrs := map[string]string{mw.TenantHeader: "other"}
	do(t, ts, http.MethodPut, "/webdav/other.txt", []byte("body"), hdrs)
	obj2, err := repo.GetObject(ctx, "other", "default", "other.txt")
	if err != nil {
		t.Fatal(err)
	}
	resp, _ = do(t, ts, http.MethodDelete, "/webdav/other.txt", nil, hdrs)
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE (other): got %d, want 204 or 200", resp.StatusCode)
	}
	assertAuditRowFor(t, repo, "other", "hard")
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if outboxStatus(t, dsn, obj2.ID, "vault.file.deleted@1.1") == "delivered" &&
			outboxStatus(t, dsn, obj2.ID, "vault.file.notify@1.1") == "delivered" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if time.Now().After(deadline) {
		t.Fatalf("unbound facts never delivered: deleted=%q notify=%q",
			outboxStatus(t, dsn, obj2.ID, "vault.file.deleted@1.1"),
			outboxStatus(t, dsn, obj2.ID, "vault.file.notify@1.1"))
	}
	// Several relay polls at 50ms settle all counters; the webhook must
	// observe the second tenant's two bus events (PUT→created, DELETE→deleted).
	waitForBodies(t, func() int { wMu.Lock(); defer wMu.Unlock(); return len(wBodies) }, 4, 5*time.Second)
	time.Sleep(400 * time.Millisecond)
	nMu.Lock()
	if n := len(nBodies); n != 1 {
		t.Fatalf("notify target received %d POSTs after unbound delete, want 1", n)
	}
	nMu.Unlock()
	l2Mu.Lock()
	if n := len(l2Bodies); n != 1 {
		t.Fatalf("L2 received %d POSTs after unbound delete, want 1 (tenant other must not deliver)", n)
	}
	l2Mu.Unlock()
	wMu.Lock()
	wAfterUnbound := append([][]byte(nil), wBodies...)
	wMu.Unlock()
	if c, d := countWebhookTypes(wAfterUnbound); c != 2 || d != 2 {
		t.Fatalf("webhook POSTs after unbound delete: created=%d deleted=%d, want 2+2 (bus is tenant-agnostic)", c, d)
	}

	// No-dup window: fixed 5s, state-witnessed (rows still delivered, not
	// just counters quiet — complete is state-based).
	time.Sleep(5 * time.Second)
	nMu.Lock()
	nAfter := len(nBodies)
	nMu.Unlock()
	l2Mu.Lock()
	l2After := len(l2Bodies)
	l2Mu.Unlock()
	wMu.Lock()
	wAfter := len(wBodies)
	wAfterBodies := append([][]byte(nil), wBodies...)
	wMu.Unlock()
	if nAfter != 1 || l2After != 1 || wAfter != 4 {
		t.Fatalf("duplicate delivery after complete: notify %d→%d, L2 %d→%d, webhook %d→%d",
			nGot, nAfter, l2Got, l2After, wGot, wAfter)
	}
	if c, d := countWebhookTypes(wAfterBodies); c != 2 || d != 2 {
		t.Fatalf("webhook types regressed after no-dup window: created=%d deleted=%d, want 2+2", c, d)
	}
	if outboxStatus(t, dsn, obj.ID, "vault.file.deleted@1.1") != "delivered" ||
		outboxStatus(t, dsn, obj.ID, "vault.file.notify@1.1") != "delivered" {
		t.Fatal("bound-tenant facts left delivered state after the no-dup window")
	}
	if outboxStatus(t, dsn, obj2.ID, "vault.file.deleted@1.1") != "delivered" ||
		outboxStatus(t, dsn, obj2.ID, "vault.file.notify@1.1") != "delivered" {
		t.Fatal("unbound-tenant facts left delivered state after the no-dup window")
	}
}

// countWebhookTypes counts captured webhook bodies by legacy event type
// (PUT emits EventCreated — file_crud.go:255 — DELETE emits EventDeleted).
// Type-filtered counting is immune to interleaving of the two bus events.
func countWebhookTypes(bodies [][]byte) (created, deleted int) {
	for _, b := range bodies {
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			continue
		}
		switch m["type"] {
		case "created":
			created++
		case "deleted":
			deleted++
		}
	}
	return created, deleted
}
