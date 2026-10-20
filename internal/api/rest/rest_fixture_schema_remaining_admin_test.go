package rest

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestRESTFixtureSchemaRemainingAdminDeleteIsolation(t *testing.T) {
	const keys = "user:default:read+write,opsecret:*:admin"
	a, b := newAdminDeleteEnv(t, keys, nil, nil), newAdminDeleteEnv(t, keys, nil, nil)
	pathA, pathB := strings.TrimPrefix(a.dsn, "file:"), strings.TrimPrefix(b.dsn, "file:")
	assertRESTFixtureFilesIndependent(t,
		restFixtureCase{root: filepath.Dir(pathA), path: pathA, repo: a.repo},
		restFixtureCase{root: filepath.Dir(pathB), path: pathB, repo: b.repo})
	digest := sha256.Sum256(sharedRESTFixtureSchema.image)
	assertRESTFixtureRemainingMigrateIsolation(t, a, pathA, pathB)
	objA := a.putObject(t, "acme", "same.txt")
	objB := b.putObject(t, "acme", "same.txt")
	assertRESTFixtureRemainingDeletePermissions(t, a, objA)
	response, body := req(t, http.MethodDelete, a.ts.URL+"/v1/admin/files/acme/same.txt?hard=1", nil,
		map[string]string{"Authorization": "Bearer opsecret"})
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("operator delete: status=%d body=%q", response.StatusCode, body)
	}
	if _, err := a.repo.GetObject(context.Background(), "acme", "default", "same.txt"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("deleted copy retained object: %v", err)
	}
	if _, err := b.store.Stat(context.Background(), objB.StorageKey); err != nil {
		t.Fatalf("delete crossed Local FS roots: %v", err)
	}
	b.assertNoWriteSideEffects(t, objB)
	a.ts.Close()
	if err := a.repo.Close(); err != nil {
		t.Fatal(err)
	}
	response, body = req(t, http.MethodGet, b.ts.URL+"/v1/files/same.txt", nil,
		map[string]string{"Authorization": "Bearer opsecret", "X-Aero-Tenant": "acme"})
	if response.StatusCode != http.StatusOK || string(body) != "payload" {
		t.Fatalf("closing A affected B: status=%d body=%q", response.StatusCode, body)
	}
	c := newAdminDeleteEnv(t, keys, nil, nil)
	if _, err := c.repo.GetObject(context.Background(), "acme", "default", "same.txt"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("later factory inherited data: %v", err)
	}
	if digest != sha256.Sum256(sharedRESTFixtureSchema.image) {
		t.Fatal("factory migration/delete changed the shared schema image")
	}
}

func assertRESTFixtureRemainingMigrateIsolation(t *testing.T, a *adminDeleteEnv, pathA, pathB string) {
	t.Helper()
	dbA, dbB := openRESTFixtureSQL(t, pathA), openRESTFixtureSQL(t, pathB)
	before := inspectRESTFixtureSchema(t, dbA)
	if !reflect.DeepEqual(before, inspectRESTFixtureSchema(t, dbB)) {
		t.Fatal("factory copies do not start at the same empty schema")
	}
	if _, err := dbA.Exec(`DROP TABLE bucket_access_logs; DELETE FROM schema_migrations WHERE version='0047_bucket_access_logs'`); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, inspectRESTFixtureSchema(t, dbB)) {
		t.Fatal("DDL or migration version change crossed factory copies")
	}
	if err := a.repo.Migrate(context.Background()); err != nil {
		t.Fatal("real factory Migrate did not restore its missing version:", err)
	}
	if !reflect.DeepEqual(before, inspectRESTFixtureSchema(t, dbA)) {
		t.Fatal("real factory Migrate did not restore schema and initial data")
	}
	assertRESTFixtureIntegrity(t, dbA)
	assertRESTFixtureIntegrity(t, dbB)
}

func assertRESTFixtureRemainingDeletePermissions(t *testing.T, env *adminDeleteEnv, obj repository.Object) {
	t.Helper()
	for _, tc := range []struct {
		token string
		code  int
	}{{"", http.StatusUnauthorized}, {"Bearer user", http.StatusForbidden}} {
		response, body := req(t, http.MethodDelete, env.ts.URL+"/v1/admin/files/acme/same.txt?hard=1", nil,
			map[string]string{"Authorization": tc.token})
		if response.StatusCode != tc.code {
			t.Fatalf("rejected delete: status=%d body=%q want=%d", response.StatusCode, body, tc.code)
		}
		if _, err := env.repo.GetObject(context.Background(), "acme", "default", "same.txt"); err != nil {
			t.Fatalf("rejected delete lost object: %v", err)
		}
		env.assertNoWriteSideEffects(t, obj)
	}
}

func TestRESTFixtureSchemaRemainingAdminScopeIsolation(t *testing.T) {
	a, regA := newAdminScopeTestServer(t)
	b, regB := newAdminScopeTestServer(t)
	response, body := req(t, http.MethodPost, a.URL+"/v1/admin/keys",
		[]byte(`{"token":"only-a","tenant":"acme","scopes":["read"]}`), adminRequestHeaders("acme-admin", "acme"))
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("same-tenant key creation: status=%d body=%q", response.StatusCode, body)
	}
	if tenant, found, err := regA.TenantForKey(context.Background(), "only-a"); err != nil || !found || tenant != "acme" {
		t.Fatalf("A key not persisted: tenant=%q found=%v err=%v", tenant, found, err)
	}
	if _, found, err := regB.TenantForKey(context.Background(), "only-a"); err != nil || found {
		t.Fatalf("A key leaked into B: found=%v err=%v", found, err)
	}
	a.Close()
	assertRESTFixtureRemainingScopedQuota(t, b.URL)
	_, regC := newAdminScopeTestServer(t)
	if _, found, err := regC.TenantForKey(context.Background(), "only-a"); err != nil || found {
		t.Fatalf("later factory inherited A key: found=%v err=%v", found, err)
	}
}

func assertRESTFixtureRemainingScopedQuota(t *testing.T, baseURL string) {
	t.Helper()
	response, body := req(t, http.MethodPut, baseURL+"/v1/admin/tenants/beta/quota",
		[]byte(`{"max_bytes":123}`), adminRequestHeaders("acme-admin", "acme"))
	if response.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "tenant_mismatch") {
		t.Fatalf("cross-tenant quota: status=%d body=%q", response.StatusCode, body)
	}
	response, body = req(t, http.MethodPut, baseURL+"/v1/admin/tenants/acme/quota",
		[]byte(`{"max_bytes":123}`), adminRequestHeaders("acme-admin", "acme"))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("closing A affected B authorization: status=%d body=%q", response.StatusCode, body)
	}
}
