package rest

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

type restFixtureCase struct {
	root, path string
	repo       repository.Repository
	svc        *service.FileService
}

func newRESTFixtureCase(t *testing.T, cache *restFixtureSchemaCache) restFixtureCase {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "x.db")
	if err := cache.seed(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	repo := openRESTFixtureRepo(t, path)
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return restFixtureCase{root: root, path: path, repo: repo, svc: restFixtureService(t, root, repo)}
}

func restFixtureService(t *testing.T, root string, repo repository.Repository) *service.FileService {
	t.Helper()
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(root, "objects")})
	if err != nil {
		t.Fatal(err)
	}
	return service.NewFileService(store, repo, nil).WithAuthorizer(allowAllProvider{})
}

func assertRESTFixtureContent(t *testing.T, svc *service.FileService, want string) {
	t.Helper()
	rc, _, err := svc.Get(context.Background(), "", "", "same.txt")
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(rc)
	closeErr := rc.Close()
	if readErr != nil || closeErr != nil || string(body) != want {
		t.Fatalf("fixture content=%q want=%q read=%v close=%v", body, want, readErr, closeErr)
	}
}

func assertRESTFixtureFilesIndependent(t *testing.T, a, b restFixtureCase) {
	t.Helper()
	infoA, errA := os.Stat(a.path)
	infoB, errB := os.Stat(b.path)
	if errA != nil || errB != nil {
		t.Fatalf("stat copies: %v, %v", errA, errB)
	}
	if a.root == b.root || a.repo == b.repo || os.SameFile(infoA, infoB) {
		t.Fatal("cases share a directory, database file or connection")
	}
}

func TestRESTFixtureSchemaWriteReopenAndSampleIsolation(t *testing.T) {
	var cache restFixtureSchemaCache
	a, b := newRESTFixtureCase(t, &cache), newRESTFixtureCase(t, &cache)
	digest := sha256.Sum256(cache.image)
	assertRESTFixtureFilesIndependent(t, a, b)
	putContentSelectionFixture(t, a.svc, "same.txt", []byte("first body"), "text/plain")
	putContentSelectionFixture(t, b.svc, "same.txt", []byte("other body"), "text/plain")
	assertRESTFixtureContent(t, a.svc, "first body")
	assertRESTFixtureContent(t, b.svc, "other body")
	if err := a.repo.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(a.root); err != nil {
		t.Fatal(err)
	}
	assertRESTFixtureContent(t, b.svc, "other body")
	if err := b.repo.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openRESTFixtureRepo(t, b.path)
	if err := reopened.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertRESTFixtureContent(t, restFixtureService(t, b.root, reopened), "other body")
	c := newRESTFixtureCase(t, &cache)
	if _, err := c.repo.GetObject(context.Background(), "default", "default", "same.txt"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("later copy inherited business data: %v", err)
	}
	if buckets, err := c.repo.ListBuckets(context.Background(), "default"); err != nil || len(buckets) != 0 {
		t.Fatalf("later copy inherited buckets: %v err=%v", buckets, err)
	}
	if digest != sha256.Sum256(cache.image) {
		t.Fatal("case writes changed the cached schema bytes")
	}
}

func beginRESTFixtureTransaction(t *testing.T, db *sql.DB) *sql.Tx {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Errorf("rollback: %v", err)
		}
	})
	return tx
}

func TestRESTFixtureSchemaTransactionsIndependent(t *testing.T) {
	var cache restFixtureSchemaCache
	a, b := newRESTFixtureCase(t, &cache), newRESTFixtureCase(t, &cache)
	dbA, dbB := openRESTFixtureSQL(t, a.path), openRESTFixtureSQL(t, b.path)
	txA, txB := beginRESTFixtureTransaction(t, dbA), beginRESTFixtureTransaction(t, dbB)
	if _, err := txA.Exec(`UPDATE audit_governance_control SET revision=17`); err != nil {
		t.Fatal(err)
	}
	// Both write transactions remain live: sharing one file would lock here.
	if _, err := txB.Exec(`UPDATE audit_governance_control SET revision=23`); err != nil {
		t.Fatal("second case could not write independently:", err)
	}
	if err := txA.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := txB.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := restFixtureRows(t, dbA, `SELECT revision FROM audit_governance_control`); !reflect.DeepEqual(got, []string{`[0]`}) {
		t.Fatalf("rollback leaked between copies: %v", got)
	}
	if err := dbB.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.repo.Close(); err != nil {
		t.Fatal(err)
	}
	if got := restFixtureRows(t, openRESTFixtureSQL(t, b.path), `SELECT revision FROM audit_governance_control`); !reflect.DeepEqual(got, []string{`[23]`}) {
		t.Fatalf("committed copy lost independent persisted write: %v", got)
	}
}

func assertRESTFixtureHTTPContents(t *testing.T, servers []string) {
	t.Helper()
	for i, server := range servers {
		body := []byte{byte('A' + i)}
		response, data := req(t, http.MethodPut, server+"/files/same.txt", body, nil)
		if response.StatusCode != http.StatusCreated {
			t.Fatalf("case %d PUT: status=%d body=%q", i, response.StatusCode, data)
		}
	}
	for i, server := range servers {
		response, body := req(t, http.MethodGet, server+"/files/same.txt", nil, nil)
		if response.StatusCode != http.StatusOK || string(body) != string(rune('A'+i)) {
			t.Fatalf("case %d read another case's blob/row: status=%d body=%q", i, response.StatusCode, body)
		}
	}
}

func TestRESTFixtureSchemaSetupKeepsHTTPAndLocalFSIndependent(t *testing.T) {
	_, repoA, serverA := setupTest(t)
	_, repoB, serverB := setupTest(t)
	if len(sharedRESTFixtureSchema.image) == 0 {
		t.Fatal("setupTestWith did not lazily initialize the closed schema image")
	}
	if repoA == repoB || serverA == serverB {
		t.Fatal("setupTestWith shared a repository or HTTP server")
	}
	assertRESTFixtureHTTPContents(t, []string{serverA.URL, serverB.URL})
	serverA.Close()
	if err := repoA.Close(); err != nil {
		t.Fatal(err)
	}
	response, body := req(t, http.MethodGet, serverB.URL+"/files/same.txt", nil, nil)
	if response.StatusCode != http.StatusOK || string(body) != "B" {
		t.Fatalf("closing first case broke second case: status=%d body=%q", response.StatusCode, body)
	}
	_, _, serverC := setupTest(t)
	response, body = req(t, http.MethodGet, serverC.URL+"/files/same.txt", nil, nil)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("later setup inherited a business object: status=%d body=%q", response.StatusCode, body)
	}
}
