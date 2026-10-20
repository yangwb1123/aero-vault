package rest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestRESTFixtureSchemaInitializationFailureStaysFailed(t *testing.T) {
	var cache restFixtureSchemaCache
	parent := filepath.Join(t.TempDir(), "missing")
	path := filepath.Join(parent, "x.db")
	first := cache.seed(context.Background(), path)
	if !errors.Is(first, os.ErrNotExist) || len(cache.image) != 0 {
		t.Fatalf("missing parent did not fail cold initialization: %v", first)
	}
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := cache.seed(context.Background(), path); !errors.Is(err, os.ErrNotExist) || len(cache.image) != 0 {
		t.Fatalf("failed initialization silently retried/published an image: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed initialization wrote a destination: %v", err)
	}
}

func TestRESTFixtureSchemaCanceledBuildCleansAndCaptureCloses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	root := t.TempDir()
	image, err := buildRESTFixtureSchema(ctx, root)
	if !errors.Is(err, context.Canceled) || len(image) != 0 {
		t.Fatalf("canceled build published an image: bytes=%d err=%v", len(image), err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed build left sample files: %v err=%v", entries, err)
	}
	path := filepath.Join(root, "x.db")
	repo := openRESTFixtureRepo(t, path)
	image, err = captureRESTFixtureSchema(ctx, path, repo)
	if !errors.Is(err, context.Canceled) || len(image) != 0 {
		t.Fatalf("canceled Migrate published an image: bytes=%d err=%v", len(image), err)
	}
	if err := repo.Ping(context.Background()); err == nil {
		t.Fatal("failed Migrate left the sample connection open")
	}
}

func TestRESTFixtureSchemaRealMigrationFailureDoesNotPublish(t *testing.T) {
	ctx := context.Background()
	var cache restFixtureSchemaCache
	path := filepath.Join(t.TempDir(), "x.db")
	if err := cache.seed(ctx, path); err != nil {
		t.Fatal(err)
	}
	repo, db := openRESTFixtureRepo(t, path), openRESTFixtureSQL(t, path)
	// Force the real runner to reapply non-idempotent DDL against a current DB.
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version='0001_init'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	image, err := captureRESTFixtureSchema(ctx, path, repo)
	if err == nil || len(image) != 0 {
		t.Fatalf("real migration failure ignored: bytes=%d err=%v", len(image), err)
	}
	if err := repo.Ping(ctx); err == nil {
		t.Fatal("failed migration left the sample connection open")
	}
	if err := restFixtureSchemaSidecarsClosed(path); err != nil {
		t.Fatal(err)
	}
	if got := restFixtureRows(t, openRESTFixtureSQL(t, path), `SELECT version FROM schema_migrations WHERE version='0001_init'`); len(got) != 0 {
		t.Fatalf("failed migration was falsely recorded: %v", got)
	}
	fresh := newRESTFixtureCase(t, &cache)
	if err := fresh.repo.Migrate(ctx); err != nil {
		t.Fatal("a failed copy changed the cached schema:", err)
	}
}

type restFixtureClosingRepo struct {
	repository.Repository
	closeErr   error
	afterClose func() error
	closes     int
}

func (r *restFixtureClosingRepo) Close() error {
	r.closes++
	err := errors.Join(r.Repository.Close(), r.closeErr)
	if r.afterClose != nil {
		err = errors.Join(err, r.afterClose())
	}
	return err
}

func TestRESTFixtureSchemaCloseAndReadFailuresDoNotPublish(t *testing.T) {
	closeFailure := errors.New("fixture close failure")
	var cache restFixtureSchemaCache
	for _, name := range []string{"close", "read"} {
		t.Run(name, func(t *testing.T) {
			fixture := newRESTFixtureCase(t, &cache)
			repo := &restFixtureClosingRepo{Repository: fixture.repo, closeErr: closeFailure}
			want := closeFailure
			if name == "read" {
				repo.closeErr = nil
				repo.afterClose = func() error { return os.Remove(fixture.path) }
				want = os.ErrNotExist
			}
			image, err := captureRESTFixtureSchema(context.Background(), fixture.path, repo)
			if !errors.Is(err, want) || len(image) != 0 || repo.closes != 1 {
				t.Fatalf("failed capture published bytes or hid error: bytes=%d closes=%d err=%v", len(image), repo.closes, err)
			}
			if err := fixture.repo.Ping(context.Background()); err == nil {
				t.Fatal("capture did not close its real connection")
			}
		})
	}
}

func TestRESTFixtureSchemaRejectsRemainingSidecars(t *testing.T) {
	var cache restFixtureSchemaCache
	for _, suffix := range []string{"-wal", "-shm"} {
		for _, body := range []string{"", "uncheckpointed data"} {
			t.Run(suffix+body, func(t *testing.T) {
				fixture := newRESTFixtureCase(t, &cache)
				repo := &restFixtureClosingRepo{Repository: fixture.repo, afterClose: func() error {
					return os.WriteFile(fixture.path+suffix, []byte(body), 0o600)
				}}
				image, err := captureRESTFixtureSchema(context.Background(), fixture.path, repo)
				if err == nil || len(image) != 0 || repo.closes != 1 {
					t.Fatalf("capture accepted a remaining SQLite sidecar: bytes=%d closes=%d err=%v", len(image), repo.closes, err)
				}
				if err := restFixtureSchemaSidecarsClosed(fixture.path); err == nil {
					t.Fatal("accepted a remaining SQLite sidecar")
				}
			})
		}
	}
}

func assertRESTFixtureExistingPaths(t *testing.T, file, dir string) {
	t.Helper()
	body, err := os.ReadFile(file)
	if err != nil || string(body) != "keep existing file" {
		t.Fatalf("copy overwrote/removed an existing file: body=%q err=%v", body, err)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("copy removed/replaced an existing directory: %v", err)
	}
}

func TestRESTFixtureSchemaCopyErrorsPreserveExistingPaths(t *testing.T) {
	var cache restFixtureSchemaCache
	root := t.TempDir()
	if err := cache.seed(context.Background(), filepath.Join(root, "initial.db")); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "existing.db")
	if err := os.WriteFile(file, []byte("keep existing file"), 0o640); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "directory")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, dir, filepath.Join(root, "missing", "x.db")} {
		if err := cache.seed(context.Background(), path); err == nil {
			t.Fatalf("bad copy destination accepted: %s", path)
		}
	}
	assertRESTFixtureExistingPaths(t, file, dir)
	if err := cache.seed(context.Background(), filepath.Join(root, "later.db")); err != nil {
		t.Fatal("copy failure poisoned an already-valid schema image:", err)
	}
}
