package repository

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func openIdempotencyReadFaultStore(t *testing.T) *sqlStore {
	t.Helper()
	repo, err := Open(t.Context(), "sqlite", "file:"+filepath.Join(t.TempDir(), "idempotency-read.db"))
	if err != nil {
		t.Fatalf("open idempotency store: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(t.Context()); err != nil {
		t.Fatalf("migrate idempotency store: %v", err)
	}
	store, ok := repo.(*sqlStore)
	if !ok {
		t.Fatalf("repo type %T, want *sqlStore", repo)
	}
	return store
}

func TestIdempotencyPersistedHeaderFaults(t *testing.T) {
	store := openIdempotencyReadFaultStore(t)
	tests := []struct {
		name   string
		raw    string
		syntax bool
	}{
		{"truncated_json", `{"Etag":`, true},
		{"array_not_map", `[]`, false},
		{"value_not_string_slice", `{"Etag":"not-a-list"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := "corrupt-" + tt.name
			completeIdempotencyFixture(t, store, key, 201, []byte(`{"ok":true}`), "application/json",
				map[string][]string{"Etag": {`"original"`}})
			overwriteIdempotencyHeaders(t, store, key, tt.raw)

			rec, claimed, err := store.ClaimIdempotencyKey(t.Context(), "tenant", key, "fp", "req")
			if err == nil {
				t.Fatalf("corrupt persisted headers replayed as %+v without an error", rec)
			}
			if claimed {
				t.Fatalf("corrupt header replay claimed the existing key")
			}
			assertJSONDecodeCause(t, err, tt.syntax)
			if rec.Key != "" || rec.ResponseStatus != 0 || rec.ResponseHeaders != nil || rec.ResponseBody != nil {
				t.Fatalf("corrupt header fault returned a partial record: %+v", rec)
			}
		})
	}
}

func TestIdempotencyValidEmptyHeadersReplay(t *testing.T) {
	store := openIdempotencyReadFaultStore(t)
	emptyObject := "{}"
	jsonNull := "null"
	tests := []struct {
		name    string
		headers map[string][]string
		raw     *string
	}{
		{"nil_headers", nil, nil},
		{"empty_map", map[string][]string{}, nil},
		{"empty_object", nil, &emptyObject},
		{"json_null", nil, &jsonNull},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := "empty-" + tt.name
			completeIdempotencyFixture(t, store, key, 204, nil, "", tt.headers)
			if tt.raw != nil {
				overwriteIdempotencyHeaders(t, store, key, *tt.raw)
			}

			rec, claimed, err := store.ClaimIdempotencyKey(t.Context(), "tenant", key, "fp", "req")
			if err != nil {
				t.Fatalf("replay valid empty headers: %v", err)
			}
			if claimed || rec.Status != IdempotencyCompleted || rec.ResponseStatus != 204 {
				t.Fatalf("replay record = %+v claimed=%v", rec, claimed)
			}
			if len(rec.ResponseHeaders) != 0 {
				t.Fatalf("valid empty headers decoded as %#v", rec.ResponseHeaders)
			}
			if len(rec.ResponseBody) != 0 {
				t.Fatalf("valid empty body decoded as %q", rec.ResponseBody)
			}
		})
	}
}

func TestIdempotencyCorruptHeadersDoNotAffectOtherRows(t *testing.T) {
	store := openIdempotencyReadFaultStore(t)
	completeIdempotencyFixture(t, store, "bad", 200, []byte("bad"), "text/plain",
		map[string][]string{"X": {"bad"}})
	overwriteIdempotencyHeaders(t, store, "bad", `{"X":`)
	completeIdempotencyFixture(t, store, "good", 200, []byte("good"), "text/plain",
		map[string][]string{"X": {"good"}})

	if _, _, err := store.ClaimIdempotencyKey(t.Context(), "tenant", "bad", "fp", "req"); err == nil {
		t.Fatalf("corrupt row unexpectedly replayed")
	}
	rec, claimed, err := store.ClaimIdempotencyKey(t.Context(), "tenant", "good", "fp", "req")
	if err != nil || claimed {
		t.Fatalf("valid row after corrupt row: claimed=%v err=%v", claimed, err)
	}
	if got := rec.ResponseHeaders["X"]; len(got) != 1 || got[0] != "good" {
		t.Fatalf("valid row headers = %#v", rec.ResponseHeaders)
	}
}

func completeIdempotencyFixture(t *testing.T, store *sqlStore, key string, status int, body []byte, contentType string, headers map[string][]string) {
	t.Helper()
	ctx := t.Context()
	if _, claimed, err := store.ClaimIdempotencyKey(ctx, "tenant", key, "fp", "req"); err != nil || !claimed {
		t.Fatalf("initial claim %q: claimed=%v err=%v", key, claimed, err)
	}
	if err := store.CompleteIdempotencyKey(ctx, "tenant", key, status, body, contentType, headers); err != nil {
		t.Fatalf("complete %q: %v", key, err)
	}
}

func overwriteIdempotencyHeaders(t *testing.T, store *sqlStore, key, raw string) {
	t.Helper()
	result, err := store.db.ExecContext(t.Context(), store.rebind(
		`UPDATE idempotency_keys SET response_headers=$1 WHERE tenant_id=$2 AND idem_key=$3`),
		raw, "tenant", key)
	if err != nil {
		t.Fatalf("persist headers for %q: %v", key, err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		t.Fatalf("headers affected=%d err=%v for %q, want 1", changed, err, key)
	}
}

func assertJSONDecodeCause(t *testing.T, err error, syntax bool) {
	t.Helper()
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
