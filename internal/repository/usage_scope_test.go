package repository

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func openUsageScopeTestStore(t *testing.T) *sqlStore {
	t.Helper()
	repo, err := Open(t.Context(), "sqlite", "file:"+filepath.Join(t.TempDir(), "usage-scope.db"))
	if err != nil {
		t.Fatalf("open usage store: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(t.Context()); err != nil {
		t.Fatalf("migrate usage store: %v", err)
	}
	store, ok := repo.(*sqlStore)
	if !ok {
		t.Fatalf("repo type %T, want *sqlStore", repo)
	}
	return store
}

func TestUsageReferenceArrayDecodeFaults(t *testing.T) {
	store := openUsageScopeTestStore(t)
	tests := []struct {
		name   string
		column string
		raw    string
		syntax bool
	}{
		{"chunk_ids_truncated", "chunk_ids", `[1,`, true},
		{"chunk_ids_wrong_type", "chunk_ids", `["1"]`, false},
		{"object_ids_truncated", "object_ids", `[42,`, true},
		{"object_ids_wrong_type", "object_ids", `[{"id":42}]`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const tenant = "usage-fault"
			requestID := "request-" + tt.name
			recordUsagePair(t, store, tenant, requestID)
			overwriteUsageArray(t, store, tenant, requestID, tt.column, tt.raw)

			rows, err := store.ListUsageForObject(t.Context(), tenant, 42, 10)
			assertUsageDecodeFault(t, rows, err, tt.column, tt.syntax)
		})
	}
}

func TestUsageReferenceEmptyArrays(t *testing.T) {
	ctx := t.Context()
	const tenant = "usage-empty"
	store := openUsageScopeTestStore(t)
	if err := store.RecordUsage(ctx, Usage{
		TenantID: tenant, Caller: "search", Query: "empty-chunks",
		ObjectIDs: []int64{42}, RequestID: "empty-chunks", Model: "m",
	}); err != nil {
		t.Fatalf("record chunk usage: %v", err)
	}
	if err := store.RecordUsage(ctx, Usage{
		TenantID: tenant, Caller: "search", Query: "empty-objects",
		ChunkIDs: []int64{7}, RequestID: "empty-objects", Model: "m",
	}); err != nil {
		t.Fatalf("record object usage: %v", err)
	}
	overwriteUsageArray(t, store, tenant, "empty-chunks", "chunk_ids", "[]")
	overwriteUsageArray(t, store, tenant, "empty-objects", "object_ids", "[]")

	rows, err := store.ListUsageForObject(ctx, tenant, 42, 10)
	if err != nil {
		t.Fatalf("read empty chunk array: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("empty chunk array returned %d rows, want 1", len(rows))
	}
	if rows[0].RequestID != "empty-chunks" || len(rows[0].ChunkIDs) != 0 || rows[0].ObjectIDs[0] != 42 {
		t.Fatalf("empty chunk array decoded incorrectly: %+v", rows[0])
	}

	rows, err = store.ListUsageForObject(ctx, tenant, 7, 10)
	if err != nil {
		t.Fatalf("read empty object array: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("empty object array matched %d rows, want none", len(rows))
	}
}

func recordUsagePair(t *testing.T, store *sqlStore, tenant, requestID string) {
	t.Helper()
	ctx := t.Context()
	if err := store.RecordUsage(ctx, Usage{
		TenantID: tenant, Caller: "search", Query: "q", ChunkIDs: []int64{7},
		ObjectIDs: []int64{42}, RequestID: requestID, Model: "m",
	}); err != nil {
		t.Fatalf("record usage: %v", err)
	}
	if err := store.RecordUsage(ctx, Usage{
		TenantID: tenant, Caller: "search", Query: "valid", ChunkIDs: []int64{8},
		ObjectIDs: []int64{42}, RequestID: requestID + "-valid", Model: "m",
	}); err != nil {
		t.Fatalf("record valid sibling usage: %v", err)
	}
}

func assertUsageDecodeFault(t *testing.T, rows []Usage, err error, column string, syntax bool) {
	t.Helper()
	if err == nil {
		t.Fatalf("malformed %s returned %d usage rows without an error", column, len(rows))
	}
	if rows != nil {
		t.Fatalf("malformed %s returned partial rows: %+v", column, rows)
	}
	if syntax {
		var syntaxErr *json.SyntaxError
		if !errors.As(err, &syntaxErr) {
			t.Fatalf("error %v does not retain the JSON syntax cause", err)
		}
		return
	}
	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) {
		t.Fatalf("error %v does not retain the JSON type cause", err)
	}
}

func overwriteUsageArray(t *testing.T, store *sqlStore, tenant, requestID, column, raw string) {
	t.Helper()
	result, err := store.db.ExecContext(t.Context(), store.rebind(
		`UPDATE ai_usage SET `+column+`=$1 WHERE tenant_id=$2 AND request_id=$3`),
		raw, tenant, requestID)
	if err != nil {
		t.Fatalf("persist %s for %q: %v", column, requestID, err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		t.Fatalf("%s affected=%d err=%v for %q, want 1", column, changed, err, requestID)
	}
}
