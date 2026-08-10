package main

// TestGovernanceE2EActivationGateEmptyBindingsBootFails — AC-2 first half:
// an enabled ∧ ¬drain ∧ empty-bindings boot is refused by auditgovernance.New
// with the "bindings" error text and NO runtime may be constructed, and the
// refused boot captures zero events. The wiring is the harness's real
// FileService+EventBus path (main.go order) with the runtime absent, so
// WrapRepository(repo, nil) must short-circuit to the raw repository
// (repository.go:15-19): the object_events row exists (gate-1 fallthrough —
// the write path itself works), but no outbox row may exist and the receiver
// must see zero POSTs and zero token calls. Structurally race-free: no
// Runtime ⇒ Start never runs ⇒ no relay goroutine exists, so the counter
// assertions are not temporal (no quiesce needed — FM-5). Any future change
// that stops WrapRepository short-circuiting a nil runtime fails this test
// loudly at wiring time (FM-6).
//
// Design: docs/requirements/internal-middleware-audit-governance-b3-6-activation-gate-v1.design.md
// §2.2-C (spec R2.2). New file: no gate pressure on the 500-line harness file.

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/auditgovernance"
	"github.com/aero-vault/aero-vault/internal/events"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

func TestGovernanceE2EActivationGateEmptyBindingsBootFails(t *testing.T) {
	receiver := newGovReceiver("202-echo")
	ctx := context.Background()
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "e2e.db")
	repo, err := repository.Open(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := governanceE2EConfig() // shared harness envelope (refactor B)
	cfg.Bindings = nil           // enabled ∧ ¬drain ∧ empty
	// 1. refuses boot: New errors with the bindings text, no runtime escapes.
	rt, err := auditgovernance.New(cfg, repo.(auditgovernance.Store), logger)
	if err == nil || !strings.Contains(err.Error(), "bindings") {
		t.Fatalf("enabled empty-bindings New accepted, err=%v", err)
	}
	if rt != nil {
		t.Fatalf("refused boot returned runtime %v", rt)
	}
	// 2. no persisted mutation: fresh DB still disable-safe (no binding row,
	// no undelivered outbox row — any pre-gate write flips it).
	gstore := repo.(auditgovernance.Store)
	if safe, err := gstore.AuditGovernanceCanDisable(ctx); err != nil || !safe {
		t.Fatalf("refused boot mutated governance state: safe=%v err=%v", safe, err)
	}
	// Control-revision probe: CanDisable checks bindings + undelivered outbox
	// only — a control-row-only write before the gate would bump the singleton
	// revision unseen, so pin revision == 0 directly (same idiom as
	// TestRuntimeNewRejectsEmptyBindingsBeforeStoreIO).
	probe := []repository.AuditGovernanceBindingState{{
		TenantID: e2eTenant, State: repository.AuditGovernanceBindingActive,
	}}
	if err := gstore.ApplyAuditGovernanceBindings(ctx, 1, "probe-digest", probe); err != nil {
		t.Fatalf("control row written before refused boot (revision != 0): %v", err)
	}
	// 3. wiring with a nil runtime: WrapRepository(repo, nil) returns the raw
	// repo (repository.go:15-19) — no auditedRepository, no outbox path, no
	// relay goroutine (FM-5/FM-6).
	wrepo := auditgovernance.WrapRepository(repo, nil)
	bus := events.New(wrepo, logger)
	bus.WithRepository(wrepo)
	svc := service.NewFileService(store, wrepo, logger).WithEventSink(bus)
	obj := putObject(t, svc, e2eTenant, "boot-gate.txt")
	originID := eventRowID(t, dsn, obj.ID) // gate-1 fallthrough: events row exists
	if _, err := outboxRow(t, dsn, originID); err != sql.ErrNoRows {
		t.Fatalf("refused boot produced an outbox row: %v", err)
	}
	// 4. zero events captured — structurally: no runtime ⇒ relay never
	// started ⇒ no goroutines ⇒ the counters are 0 without quiesce/startRelay.
	if receiver.postCount.Load() != 0 || receiver.tokenCalls.Load() != 0 {
		t.Fatalf("events captured under refused boot: post=%d token=%d",
			receiver.postCount.Load(), receiver.tokenCalls.Load())
	}
	t.Cleanup(receiver.server.Close)
	t.Cleanup(func() { _ = repo.Close() })
}
