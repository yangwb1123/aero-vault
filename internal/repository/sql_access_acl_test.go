package repository

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
)

// openACLTestRepo opens a migrated sqlite repository for access-ACL tests
// (idiom per access_cleanup_test.go).
func openACLTestRepo(t *testing.T) (access.Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	repo, err := Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "acl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store, ok := repo.(access.Store)
	if !ok {
		t.Fatal("repository does not implement access.Store")
	}
	return store, ctx
}

func seedACLEntry(t *testing.T, store access.Store, id, key string, effect access.Effect, inherit bool) {
	t.Helper()
	entry := access.ACLEntry{
		ID: id, TenantID: "acme", Bucket: "default", Key: key,
		ResourceKind: access.ResourceFolder, PrincipalType: access.PrincipalTypeUser,
		PrincipalID: "alice", Action: access.ActionRead, Effect: effect,
		Inherit: inherit, CreatedBy: "seed", CreatedAt: time.Now().UTC(),
	}
	if err := store.PutACLEntry(context.Background(), entry); err != nil {
		t.Fatalf("seed ACL %q: %v", id, err)
	}
}

// assertACLIDs asserts ListApplicableACL returns exactly the given entry IDs,
// in order (ORDER BY LENGTH(resource_key) DESC, created_at, id).
func assertACLIDs(t *testing.T, store access.Store, key string, want ...string) {
	t.Helper()
	entries, err := store.ListApplicableACL(context.Background(), "acme", "default", key)
	if err != nil {
		t.Fatalf("ListApplicableACL(%q): %v", key, err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.ID)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ListApplicableACL(%q) = %v, want %v", key, got, want)
	}
}

// AC-1: folder ACL keys containing _ or % must match literally — no entries
// for sibling keys, while genuine children still inherit.
func TestListApplicableACLFolderWildcardIsLiteral(t *testing.T) {
	store, _ := openACLTestRepo(t)
	seedACLEntry(t, store, "acl-underscore", "report_2026/", access.EffectAllow, true)
	seedACLEntry(t, store, "acl-percent", "50%/", access.EffectDeny, true)

	// Siblings must NOT match (pre-fix these each returned 1 entry via LIKE).
	assertACLIDs(t, store, "reportX2026/x")
	assertACLIDs(t, store, "50x/y")

	// Positive controls: genuine children still inherit (guards the slash
	// boundary and the literal interpretation).
	assertACLIDs(t, store, "report_2026/x", "acl-underscore")
	assertACLIDs(t, store, "50%/x", "acl-percent")
}

// QA F1 matrix: the full literal-prefix semantics of ListApplicableACL.
func TestListApplicableACLPrefixSemantics(t *testing.T) {
	cases := []struct {
		name string
		seed []struct {
			id, key string
			kind    access.ResourceKind
			effect  access.Effect
			inherit bool
		}
		key  string
		want []string
	}{
		{
			name: "case sibling does not match",
			// SQLite LIKE was ASCII case-insensitive; the literal comparison
			// is case-sensitive (deliberate change, S3 keys are case-sensitive).
			seed: []struct {
				id, key string
				kind    access.ResourceKind
				effect  access.Effect
				inherit bool
			}{{id: "acl-docs", key: "Docs/", kind: access.ResourceFolder, effect: access.EffectAllow, inherit: true}},
			key:  "docs/x",
			want: nil,
		},
		{
			name: "genuine child inherits",
			seed: []struct {
				id, key string
				kind    access.ResourceKind
				effect  access.Effect
				inherit bool
			}{{id: "acl-docs", key: "docs/", kind: access.ResourceFolder, effect: access.EffectAllow, inherit: true}},
			key:  "docs/readme.txt",
			want: []string{"acl-docs"},
		},
		{
			name: "slash boundary",
			seed: []struct {
				id, key string
				kind    access.ResourceKind
				effect  access.Effect
				inherit bool
			}{{id: "acl-docs", key: "docs/", kind: access.ResourceFolder, effect: access.EffectAllow, inherit: true}},
			key:  "docsx/x",
			want: nil,
		},
		{
			name: "empty folder key is bucket-wide",
			seed: []struct {
				id, key string
				kind    access.ResourceKind
				effect  access.Effect
				inherit bool
			}{{id: "acl-wide", key: "", kind: access.ResourceFolder, effect: access.EffectAllow, inherit: true}},
			key:  "any/deep/key.txt",
			want: []string{"acl-wide"},
		},
		{
			name: "inherit disabled excluded from prefix match",
			seed: []struct {
				id, key string
				kind    access.ResourceKind
				effect  access.Effect
				inherit bool
			}{{id: "acl-docs", key: "docs/", kind: access.ResourceFolder, effect: access.EffectAllow, inherit: false}},
			key:  "docs/x",
			want: nil,
		},
		{
			name: "exact folder match ignores inherit flag",
			seed: []struct {
				id, key string
				kind    access.ResourceKind
				effect  access.Effect
				inherit bool
			}{{id: "acl-docs", key: "docs/", kind: access.ResourceFolder, effect: access.EffectAllow, inherit: false}},
			key:  "docs/",
			want: []string{"acl-docs"},
		},
		{
			name: "object exact match with wildcard characters",
			seed: []struct {
				id, key string
				kind    access.ResourceKind
				effect  access.Effect
				inherit bool
			}{{id: "acl-obj", key: "a_b.txt", kind: access.ResourceObject, effect: access.EffectAllow, inherit: false}},
			key:  "a_b.txt",
			want: []string{"acl-obj"},
		},
		{
			name: "bucket scope matches every key",
			seed: []struct {
				id, key string
				kind    access.ResourceKind
				effect  access.Effect
				inherit bool
			}{{id: "acl-bucket", key: "", kind: access.ResourceBucket, effect: access.EffectAllow, inherit: false}},
			key:  "x/y",
			want: []string{"acl-bucket"},
		},
		{
			name: "deep folder wins over shallow in ordering",
			seed: []struct {
				id, key string
				kind    access.ResourceKind
				effect  access.Effect
				inherit bool
			}{
				{id: "acl-shallow", key: "a/", kind: access.ResourceFolder, effect: access.EffectDeny, inherit: true},
				{id: "acl-deep", key: "a/b/", kind: access.ResourceFolder, effect: access.EffectAllow, inherit: true},
			},
			key:  "a/b/x",
			want: []string{"acl-deep", "acl-shallow"},
		},
		{
			name: "legacy no-slash folder key keeps genuine children",
			seed: []struct {
				id, key string
				kind    access.ResourceKind
				effect  access.Effect
				inherit bool
			}{{id: "acl-legacy", key: "legacy", kind: access.ResourceFolder, effect: access.EffectAllow, inherit: true}},
			key:  "legacy/x",
			want: []string{"acl-legacy"},
		},
		{
			name: "legacy no-slash folder key keeps literal prefix matches",
			// A pre-existing row stored without the trailing slash (only
			// reachable via store seeding; PutACL normalizes) still matches
			// keys by literal prefix — same as LIKE 'legacy%' before the fix
			// (no under-match, documented in the spec §2).
			seed: []struct {
				id, key string
				kind    access.ResourceKind
				effect  access.Effect
				inherit bool
			}{{id: "acl-legacy", key: "legacy", kind: access.ResourceFolder, effect: access.EffectAllow, inherit: true}},
			key:  "legacyx/y",
			want: []string{"acl-legacy"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := openACLTestRepo(t)
			ctx := context.Background()
			now := time.Now().UTC()
			for i, s := range tc.seed {
				if err := store.PutACLEntry(ctx, access.ACLEntry{
					ID: s.id, TenantID: "acme", Bucket: "default", Key: s.key,
					ResourceKind: s.kind, PrincipalType: access.PrincipalTypeUser,
					PrincipalID: "alice", Action: access.ActionRead, Effect: s.effect,
					Inherit: s.inherit, CreatedBy: "seed", CreatedAt: now.Add(time.Duration(i) * time.Second),
				}); err != nil {
					t.Fatalf("seed ACL %q: %v", s.id, err)
				}
			}
			assertACLIDs(t, store, tc.key, tc.want...)
		})
	}
}
