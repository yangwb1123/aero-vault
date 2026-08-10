package repository

import (
	"context"
	"strings"
	"testing"
)

// TestJsonPathEscapes (gate Required #3) pins the jsonPath helper directly:
// every key shape must be addressed as exactly one quoted "$." segment with
// JSON-string escaping, so SQLite json_remove deletes that key and only that
// key. Expected strings are the literal SQLite JSON path texts.
func TestJsonPathEscapes(t *testing.T) {
	cases := []struct {
		key  string
		want string
	}{
		{"plain", `$."plain"`},
		{"with space", `$."with space"`},
		{`quote"in`, `$."quote\"in"`},
		{`back\slash`, `$."back\\slash"`},
		{"line\nbreak", `$."line\nbreak"`},
		{"carriage\rreturn", `$."carriage\rreturn"`},
		{"tab\there", `$."tab\there"`},
		{"ctl\x01char", `$."ctl\u0001char"`},
		{"a.b$c[d]", `$."a.b$c[d]"`},
		{"", `$.""`},
		{"ключ-unicode", `$."ключ-unicode"`},
		{"emoji-🔑", `$."emoji-🔑"`},
	}
	for _, tc := range cases {
		if got := jsonPath(tc.key); got != tc.want {
			t.Errorf("jsonPath(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

// TestSetObjectMetaKeyMalformedStoredJSONFailsLoud pins the one registered
// behavior change of the atomic rewrite: when the stored metadata JSON is
// malformed (only reachable via external DB tampering), the pre-fix code
// silently overwrote it; the single-statement merge now fails loudly and does
// not destroy the tampered data.
func TestSetObjectMetaKeyMalformedStoredJSONFailsLoud(t *testing.T) {
	ctx := context.Background()
	s := openTestSQLite(t)
	if err := s.CreateBucket(ctx, "default", "default"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if _, err := s.UpsertObject(ctx, Object{
		TenantID: "default", Bucket: "default", Key: "tampered.txt",
		Backend: "local", StorageKey: "default/default/tampered.txt", Size: 1, ETag: "e",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Simulate external tampering: corrupt the JSON blob directly.
	if _, err := s.db.ExecContext(ctx, `UPDATE objects SET metadata='{bad' WHERE key='tampered.txt'`); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	if err := s.SetObjectMetaKey(ctx, "default", "default", "tampered.txt", "k", "v"); err == nil {
		t.Fatal("SetObjectMetaKey on malformed stored JSON: got nil, want fail-loud error")
	} else if !strings.Contains(err.Error(), "malformed JSON") {
		t.Fatalf("SetObjectMetaKey error = %v, want malformed JSON", err)
	}
	if err := s.DeleteObjectMetaKey(ctx, "default", "default", "tampered.txt", "k"); err == nil {
		t.Fatal("DeleteObjectMetaKey on malformed stored JSON: got nil, want fail-loud error")
	} else if !strings.Contains(err.Error(), "malformed JSON") {
		t.Fatalf("DeleteObjectMetaKey error = %v, want malformed JSON", err)
	}

	// The failed operations must not destroy the tampered data.
	var raw string
	if err := s.db.QueryRowContext(ctx,
		`SELECT metadata FROM objects WHERE key='tampered.txt'`).Scan(&raw); err != nil {
		t.Fatalf("read raw metadata: %v", err)
	}
	if raw != "{bad" {
		t.Fatalf("tampered metadata destroyed by failed op: %q", raw)
	}
}
