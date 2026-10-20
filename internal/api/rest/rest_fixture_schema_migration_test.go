package rest

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func openRESTFixtureRepo(t testing.TB, path string) repository.Repository {
	t.Helper()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := repo.Close(); err != nil {
			t.Errorf("close fixture repo: %v", err)
		}
	})
	return repo
}

func openRESTFixtureSQL(t testing.TB, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close fixture SQL: %v", err)
		}
	})
	return db
}

func restFixtureRows(t testing.TB, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result []string
	for rows.Next() {
		values, targets := make([]any, len(columns)), make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		if err := rows.Scan(targets...); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, string(encoded))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(result)
	return result
}

type restFixtureSchemaState struct {
	definitions []string
	versions    []string
	data        map[string][]string
}

func inspectRESTFixtureSchema(t testing.TB, db *sql.DB) restFixtureSchemaState {
	t.Helper()
	state := restFixtureSchemaState{
		definitions: restFixtureRows(t, db, `SELECT type, name, tbl_name, sql FROM sqlite_schema`),
		versions:    restFixtureRows(t, db, `SELECT version FROM schema_migrations`),
		data:        make(map[string][]string),
	}
	tables := restFixtureRows(t, db, `SELECT name FROM sqlite_schema WHERE type='table' AND name<>'schema_migrations'`)
	for _, encoded := range tables {
		var names []string
		if err := json.Unmarshal([]byte(encoded), &names); err != nil {
			t.Fatal(err)
		}
		name := names[0]
		state.data[name] = restFixtureRows(t, db, `SELECT * FROM "`+strings.ReplaceAll(name, `"`, `""`)+`"`)
	}
	return state
}

func assertRESTFixtureIntegrity(t testing.TB, db *sql.DB) {
	t.Helper()
	if got := restFixtureRows(t, db, `PRAGMA integrity_check`); !reflect.DeepEqual(got, []string{`["ok"]`}) {
		t.Fatalf("integrity_check: %v", got)
	}
	if got := restFixtureRows(t, db, `PRAGMA foreign_key_check`); len(got) != 0 {
		t.Fatalf("foreign_key_check: %v", got)
	}
}

func assertRESTFixtureColdCopy(t *testing.T, root, path string, image []byte) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "x.db" {
		t.Fatalf("cold build left sample files: entries=%v err=%v", entries, err)
	}
	copied, err := os.ReadFile(path)
	if err != nil || len(copied) == 0 || !bytes.Equal(copied, image) {
		t.Fatalf("copy differs from closed sample: bytes=%d err=%v", len(copied), err)
	}
}

func TestRESTFixtureSchemaColdMatchesFreshMigrations(t *testing.T) {
	ctx := context.Background()
	var cache restFixtureSchemaCache
	root := t.TempDir()
	copyPath := filepath.Join(root, "x.db")
	if err := cache.seed(ctx, copyPath); err != nil {
		t.Fatal(err)
	}
	assertRESTFixtureColdCopy(t, root, copyPath, cache.image)
	copied := openRESTFixtureRepo(t, copyPath)
	if err := copied.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	freshPath := filepath.Join(t.TempDir(), "fresh.db")
	fresh := openRESTFixtureRepo(t, freshPath)
	if err := fresh.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	copyDB, freshDB := openRESTFixtureSQL(t, copyPath), openRESTFixtureSQL(t, freshPath)
	got, want := inspectRESTFixtureSchema(t, copyDB), inspectRESTFixtureSchema(t, freshDB)
	if len(got.versions) == 0 || !reflect.DeepEqual(got, want) {
		t.Fatalf("copy not equivalent to current real migrations:\ncopy=%+v\nfresh=%+v", got, want)
	}
	for _, pragma := range []string{"journal_mode", "synchronous"} {
		got, want := restFixtureRows(t, copyDB, "PRAGMA "+pragma), restFixtureRows(t, freshDB, "PRAGMA "+pragma)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s changed: copy=%v fresh=%v", pragma, got, want)
		}
	}
	assertRESTFixtureIntegrity(t, copyDB)
	assertRESTFixtureIntegrity(t, freshDB)
}

func TestRESTFixtureSchemaMigrateIdempotentAndAppliesMissingVersion(t *testing.T) {
	ctx := context.Background()
	var cache restFixtureSchemaCache
	path := filepath.Join(t.TempDir(), "x.db")
	if err := cache.seed(ctx, path); err != nil {
		t.Fatal(err)
	}
	repo, db := openRESTFixtureRepo(t, path), openRESTFixtureSQL(t, path)
	before := inspectRESTFixtureSchema(t, db)
	stamps := restFixtureRows(t, db, `SELECT version, applied_at FROM schema_migrations`)
	for i := 0; i < 2; i++ {
		if err := repo.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(before, inspectRESTFixtureSchema(t, db)) ||
		!reflect.DeepEqual(stamps, restFixtureRows(t, db, `SELECT version, applied_at FROM schema_migrations`)) {
		t.Fatal("real Migrate changed an already-current copy")
	}
	if _, err := db.Exec(`DROP TABLE bucket_access_logs; DELETE FROM schema_migrations WHERE version='0047_bucket_access_logs'`); err != nil {
		t.Fatal(err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal("real Migrate did not restore a missing migration:", err)
	}
	if !reflect.DeepEqual(before, inspectRESTFixtureSchema(t, db)) {
		t.Fatal("reapplied migration did not restore the full schema and initial data")
	}
	assertRESTFixtureIntegrity(t, db)
}
