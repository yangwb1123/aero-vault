package repository_test

// TEMPORARY performance probe — deleted after the review run.

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
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

const fixedSQL = `SELECT ` + probeCols + ` FROM resource_acls
 WHERE tenant_id=? AND bucket=? AND (
   resource_kind='bucket'
   OR (resource_kind='object' AND resource_key=?)
   OR (resource_kind='folder' AND (resource_key=? OR inherit_acl=1))
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

// filterApplicableACL — copy of the proposed design §2.3 helper.
func filterApplicableACL(entries []access.ACLEntry, key string) []access.ACLEntry {
	out := make([]access.ACLEntry, 0, len(entries))
	for _, entry := range entries {
		switch entry.ResourceKind {
		case access.ResourceBucket:
			out = append(out, entry)
		case access.ResourceObject:
			if entry.Key == key {
				out = append(out, entry)
			}
		case access.ResourceFolder:
			if entry.Key == key || strings.HasPrefix(key, folderPrefix(entry.Key)) {
				out = append(out, entry)
			}
		}
	}
	return out
}

func folderPrefix(folderKey string) string {
	if folderKey == "" || strings.HasSuffix(folderKey, "/") {
		return folderKey
	}
	return folderKey + "/"
}

func scanEntries(rows *sql.Rows) ([]access.ACLEntry, error) {
	out := make([]access.ACLEntry, 0)
	for rows.Next() {
		var e access.ACLEntry
		var kind, ptype, action, effect, created string
		var inherit int
		if err := rows.Scan(&e.ID, &e.TenantID, &e.Bucket, &e.Key, &kind, &ptype,
			&e.PrincipalID, &action, &effect, &inherit, &e.CreatedBy, &created); err != nil {
			return nil, err
		}
		e.ResourceKind = access.ResourceKind(kind)
		e.PrincipalType = access.PrincipalType(ptype)
		e.Action, e.Effect = access.Action(action), access.Effect(effect)
		e.Inherit = inherit != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

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

func BenchmarkListApplicableFixed(b *testing.B) {
	for _, n := range []int{0, 1000, 10000} {
		b.Run(fmt.Sprintf("folders=%d", n), func(b *testing.B) {
			_, raw, ctx, _ := probeSetup(b, n)
			const key = "folder000001/sub/x.txt"
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				rows, err := raw.QueryContext(ctx, fixedSQL, "acme", "default", key, key)
				if err != nil {
					b.Fatal(err)
				}
				entries, err := scanEntries(rows)
				rows.Close()
				if err != nil {
					b.Fatal(err)
				}
				_ = filterApplicableACL(entries, key)
			}
		})
	}
}

// Isolates the Go filter cost with rows fetched once outside the loop.
func BenchmarkGoFilterOnly(b *testing.B) {
	for _, n := range []int{1000, 10000} {
		b.Run(fmt.Sprintf("folders=%d", n), func(b *testing.B) {
			_, raw, ctx, _ := probeSetup(b, n)
			const key = "folder000001/sub/x.txt"
			rows, err := raw.QueryContext(ctx, fixedSQL, "acme", "default", key, key)
			if err != nil {
				b.Fatal(err)
			}
			entries, err := scanEntries(rows)
			rows.Close()
			if err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(len(entries)), "rows/call")
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = filterApplicableACL(entries, key)
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

// Simulated request-scoped cache: fetch bucket+folder scope rows ONCE per list
// request, then filter per object in Go (no per-object SQL for scope rows).
func BenchmarkScopeFetchOnceThenFilter(b *testing.B) {
	for _, n := range []int{1000, 10000} {
		b.Run(fmt.Sprintf("folders=%d", n), func(b *testing.B) {
			_, raw, ctx, _ := probeSetup(b, n)
			// one scope fetch (this is the fixed SQL row set)
			rows, err := raw.QueryContext(ctx, fixedSQL, "acme", "default", "unused", "unused")
			if err != nil {
				b.Fatal(err)
			}
			scope, err := scanEntries(rows)
			rows.Close()
			if err != nil {
				b.Fatal(err)
			}
			// N=1000 objects per list page, each filtered against cached scope
			const objectsPerPage = 1000
			keys := make([]string, objectsPerPage)
			for i := range keys {
				keys[i] = fmt.Sprintf("folder%06d/sub/file%04d.txt", i%n, i)
			}
			b.ReportMetric(float64(len(scope)), "scope-rows")
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for _, k := range keys {
					_ = filterApplicableACL(scope, k)
				}
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
	for name, q := range map[string]string{"current": currentSQL, "fixed": fixedSQL, "inlist": inListSQL} {
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
