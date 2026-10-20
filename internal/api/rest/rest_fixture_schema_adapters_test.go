package rest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// Keep the caller's real Open/Migrate and cleanup; only seed its private file.
func restFixtureSchemaDir(t *testing.T, ctx context.Context, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := sharedRESTFixtureSchema.seed(ctx, filepath.Join(dir, name)); err != nil {
		t.Fatalf("seed fixture schema: %v", err)
	}
	return dir
}

func assertRESTFixtureSchemaAdapterFile(t *testing.T, dir, name string) os.FileInfo {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != name {
		t.Fatalf("fixed database filename: entries=%v want=%q err=%v", entries, name, err)
	}
	path := filepath.Join(dir, name)
	image, err := os.ReadFile(path)
	if err != nil || len(image) == 0 || !bytes.Equal(image, sharedRESTFixtureSchema.image) {
		t.Fatalf("adapter did not copy the closed schema: bytes=%d err=%v", len(image), err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("database permissions=%o want 600", info.Mode().Perm())
	}
	return info
}

func TestRESTFixtureSchemaAdaptersFixedNamesAndIndependentDirectories(t *testing.T) {
	for _, name := range []string{"acl.db", "c.db", "thumb.db", "access.db", "fg.db"} {
		t.Run(name, func(t *testing.T) {
			a := restFixtureSchemaDir(t, context.Background(), name)
			b := restFixtureSchemaDir(t, context.Background(), name)
			infoA := assertRESTFixtureSchemaAdapterFile(t, a, name)
			infoB := assertRESTFixtureSchemaAdapterFile(t, b, name)
			if a == b || os.SameFile(infoA, infoB) {
				t.Fatal("adapter shared a writable directory or database file")
			}
		})
	}
}

func newRESTFixtureSchemaAdapterCase(t *testing.T, name string) restFixtureCase {
	t.Helper()
	root := restFixtureSchemaDir(t, context.Background(), name)
	path := filepath.Join(root, name)
	repo := openRESTFixtureRepo(t, path)
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return restFixtureCase{root: root, path: path, repo: repo, svc: restFixtureService(t, root, repo)}
}

func TestRESTFixtureSchemaAdaptersWriteReopenIsolation(t *testing.T) {
	for _, name := range []string{"acl.db", "c.db", "thumb.db", "access.db", "fg.db"} {
		t.Run(name, func(t *testing.T) {
			a, b := newRESTFixtureSchemaAdapterCase(t, name), newRESTFixtureSchemaAdapterCase(t, name)
			digest := sha256.Sum256(sharedRESTFixtureSchema.image)
			assertRESTFixtureFilesIndependent(t, a, b)
			putContentSelectionFixture(t, a.svc, "same.txt", []byte("adapter A"), "text/plain")
			putContentSelectionFixture(t, b.svc, "same.txt", []byte("adapter B"), "text/plain")
			assertRESTFixtureContent(t, a.svc, "adapter A")
			assertRESTFixtureContent(t, b.svc, "adapter B")
			if err := a.repo.Close(); err != nil {
				t.Fatal(err)
			}
			if err := restFixtureSchemaSidecarsClosed(a.path); err != nil {
				t.Fatal(err)
			}
			reopened := openRESTFixtureRepo(t, a.path)
			if err := reopened.Migrate(context.Background()); err != nil {
				t.Fatal(err)
			}
			assertRESTFixtureContent(t, restFixtureService(t, a.root, reopened), "adapter A")
			assertRESTFixtureContent(t, b.svc, "adapter B")
			c := newRESTFixtureSchemaAdapterCase(t, name)
			if _, err := c.repo.GetObject(context.Background(), "default", "default", "same.txt"); !errors.Is(err, repository.ErrNotFound) {
				t.Fatalf("later adapter inherited a business object: %v", err)
			}
			if digest != sha256.Sum256(sharedRESTFixtureSchema.image) {
				t.Fatal("adapter writes changed the shared schema bytes")
			}
		})
	}
}
