package repository_test

// Performance probe for the access-ACL lookup path (ListApplicableACL). This
// is a permanent guard: currentSQL must mirror the production clause in
// sql_access_acl.go so the benchmark measures the shipped query (the folder
// prefix branch is non-sargable; this file is where any cost regression shows).

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	_ "modernc.org/sqlite"
)

const probeCols = `id, tenant_id, bucket, resource_key, resource_kind,
 principal_type, principal_id, action, effect, inherit_acl, created_by, created_at`

func probeSetup(b testing.TB, folderACLs int) (access.Store, *sql.DB, context.Context, string) {
	b.Helper()
	ctx := context.Background()
	dsn := "file:" + filepath.Join(b.TempDir(), "perf.db")
	repo, err := repository.Open(ctx, "sqlite", dsn)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		b.Fatal(err)
	}
	store := repo.(access.Store)
	now := time.Now().UTC()
	for i := 0; i < folderACLs; i++ {
		if err := store.PutACLEntry(ctx, access.ACLEntry{
			ID: fmt.Sprintf("acl-%06d", i), TenantID: "acme", Bucket: "default",
			Key: fmt.Sprintf("folder%06d/", i), ResourceKind: access.ResourceFolder,
			PrincipalType: access.PrincipalTypeUser, PrincipalID: "alice",
			Action: access.ActionRead, Effect: access.EffectAllow,
			Inherit: true, CreatedBy: "probe", CreatedAt: now,
		}); err != nil {
			b.Fatal(err)
		}
	}
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = raw.Close() })
	return store, raw, ctx, dsn
}

const currentSQL = `SELECT ` + probeCols + ` FROM resource_acls
 WHERE tenant_id=? AND bucket=? AND (
   resource_kind='bucket'
   OR (resource_kind='object' AND resource_key=?)
   OR (resource_kind='folder' AND (resource_key=? OR (inherit_acl=1 AND substr(?, 1, length(resource_key)) = resource_key)))
 ) ORDER BY LENGTH(resource_key) DESC, created_at, id`

func scanRows(rows *sql.Rows) (int64, error) {
	var n int64
	for rows.Next() {
		var id, tenant, bucket, key, kind, ptype, pid, action, effect, createdBy, created string
		var inherit int
		if err := rows.Scan(&id, &tenant, &bucket, &key, &kind, &ptype, &pid, &action, &effect, &inherit, &createdBy, &created); err != nil {
			return 0, err
		}
		n++
	}
	return n, rows.Err()
}

// filterApplicableACL and folderPrefix were removed: they implemented the
// rejected Go-side filter arm (the shipped fix does literal-prefix matching in
// SQL). scanEntries was their row scanner; the surviving benchmarks use
// scanRows.

func BenchmarkListApplicableCurrent(b *testing.B) {
	for _, n := range []int{0, 1000, 10000} {
		b.Run(fmt.Sprintf("folders=%d", n), func(b *testing.B) {
			_, raw, ctx, _ := probeSetup(b, n)
			const key = "folder000001/sub/x.txt"
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				rows, err := raw.QueryContext(ctx, currentSQL, "acme", "default", key, key, key)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := scanRows(rows); err != nil {
					b.Fatal(err)
				}
				rows.Close()
			}
		})
	}
}

const inListSQL = `SELECT ` + probeCols + ` FROM resource_acls
 WHERE tenant_id=? AND bucket=? AND (
   resource_kind='bucket'
   OR (resource_kind='object' AND resource_key=?)
   OR (resource_kind='folder' AND resource_key=?)
   OR (resource_kind='folder' AND inherit_acl=1 AND resource_key IN (?,?,?))
 ) ORDER BY LENGTH(resource_key) DESC, created_at, id`

// ancestors of "folder000001/sub/x.txt" → "", "folder000001/", "folder000001/sub/"
var ancestorKeys = []string{"", "folder000001/", "folder000001/sub/"}

// DB-architect F-2: per-object ancestor IN-list (sargable, ~depth+1 index seeks).
func BenchmarkListApplicableINList(b *testing.B) {
	for _, n := range []int{1000, 10000} {
		b.Run(fmt.Sprintf("folders=%d", n), func(b *testing.B) {
			_, raw, ctx, _ := probeSetup(b, n)
			const key = "folder000001/sub/x.txt"
			args := []any{"acme", "default", key, key, ancestorKeys[0], ancestorKeys[1], ancestorKeys[2]}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				rows, err := raw.QueryContext(ctx, inListSQL, args...)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := scanRows(rows); err != nil {
					b.Fatal(err)
				}
				rows.Close()
			}
		})
	}
}

// N+1 baseline: full Authorize decision per object (ListApplicableACL + ListSubjectDepartments).
func BenchmarkAuthorizePerObject(b *testing.B) {
	for _, n := range []int{0, 1000, 10000} {
		b.Run(fmt.Sprintf("folders=%d", n), func(b *testing.B) {
			store, _, ctx, _ := probeSetup(b, n)
			mgr, err := access.NewManager(store, access.Config{
				Enabled: true, DefaultPolicy: access.DefaultDeny,
				ShareSecret: []byte("0123456789abcdef0123456789abcdef"),
			})
			if err != nil {
				b.Fatal(err)
			}
			principal := access.Principal{SubjectID: "alice", TenantID: "acme", Kind: access.PrincipalUser}
			resource := access.Resource{TenantID: "acme", Bucket: "default", Key: "folder000001/sub/x.txt", Kind: access.ResourceObject, OwnerID: ""}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := mgr.Authorize(ctx, principal, access.ActionRead, resource); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestProbeQueryPlans(t *testing.T) {
	store, raw, ctx, _ := probeSetup(t, 1000)
	_ = store
	for name, q := range map[string]string{"current": currentSQL, "inlist": inListSQL} {
		args := []any{"acme", "default", "folder000001/sub/x.txt", "folder000001/sub/x.txt"}
		if name == "current" {
			args = append(args, "folder000001/sub/x.txt")
		}
		if name == "inlist" {
			args = append(args, ancestorKeys[0], ancestorKeys[1], ancestorKeys[2])
		}
		rows, err := raw.QueryContext(ctx, `EXPLAIN QUERY PLAN `+q, args...)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				t.Fatal(err)
			}
			t.Logf("%s plan: %s", name, detail)
		}
		rows.Close()
	}
}
