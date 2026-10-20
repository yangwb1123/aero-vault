package repository

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/spaces"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/google/uuid"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestSpaceMigrationFourFilesStaticPGSymmetry(t *testing.T) {
	body := map[string]string{}
	for _, dialect := range []string{"sqlite", "postgres"} {
		for _, direction := range []string{"up", "down"} {
			key := dialect + "/" + direction
			b, err := fs.ReadFile(migrationsFS, "migrations/"+dialect+"/0048_spaces_foundation."+direction+".sql")
			if err != nil || len(b) == 0 {
				t.Fatalf("%s: %v", key, err)
			}
			body[key] = string(b)
		}
	}
	// Only a static equivalence check, NOT a PostgreSQL migration execution.
	sq := regexp.MustCompile(`typeof\([a-z_]+\) = 'integer' AND `).ReplaceAllString(body["sqlite/up"], "")
	sq = strings.ReplaceAll(sq, "INTEGER", "BIGINT")
	sq = strings.ReplaceAll(sq, "OR (revoked_at_ns > 0))", "OR revoked_at_ns > 0)")
	if sq != body["postgres/up"] {
		t.Fatal("unexpected dialect divergence")
	}
	if body["sqlite/down"] != body["postgres/down"] {
		t.Fatal("down dialect divergence")
	}
}

func spaceTestConstraint(t *testing.T, err error, code int) {
	t.Helper()
	var sq *sqlite.Error
	if !errors.As(err, &sq) || sq.Code() != code {
		t.Fatalf("error=%v want SQLite code %d", err, code)
	}
	want := spaces.ErrPersistence
	if code == sqlite3.SQLITE_CONSTRAINT_UNIQUE || code == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY {
		want = spaces.ErrConflict
	}
	spaceTestError(t, spaceSQLError(context.Background(), "real SQLite constraint", err), want)
}

// All substituted values get new ordered placeholders, including equal values.
func spaceTestClone(s *sqlStore, table, cols, tenant string, replacements map[string]any) error {
	parts := strings.Split(cols, ",")
	args := make([]any, 0)
	for i, col := range parts {
		col = strings.TrimSpace(col)
		parts[i] = col
		if value, ok := replacements[col]; ok {
			args = append(args, value)
			parts[i] = fmt.Sprintf("$%d", len(args))
		}
	}
	args = append(args, tenant)
	query := fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM %s WHERE tenant_id=$%d LIMIT 1", table, cols, strings.Join(parts, ","), table, len(args))
	_, err := s.db.ExecContext(context.Background(), s.rebind(query), args...)
	return err
}

func TestSpaceSQLiteUniqueAndTenantForeignKeys(t *testing.T) {
	s, _ := spaceTestDB(t)
	ctx := context.Background()
	v := spaceTestSpace(t, s, spaceTestCreate("alpha", spaces.Personal, spaceTestSubject("a", "same")))
	if err := s.CreateBucket(ctx, "alpha", "candidate"); err != nil {
		t.Fatal(err)
	}
	err := spaceTestClone(s, "spaces", spaceColumns, "alpha", map[string]any{"id": uuid.NewString(), "backing_bucket": "candidate"})
	spaceTestConstraint(t, err, sqlite3.SQLITE_CONSTRAINT_UNIQUE) // personal uniqueness, despite new ID/bucket
	// Isolate the PK: a team has no personal unique key and uses another bucket.
	err = spaceTestClone(s, "spaces", spaceColumns, "alpha", map[string]any{
		"kind": "team", "personal_source": nil, "personal_kind": nil, "personal_subject_id": nil, "backing_bucket": "candidate",
	})
	spaceTestConstraint(t, err, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY)
	err = spaceTestClone(s, "spaces", spaceColumns, "alpha", map[string]any{"tenant_id": "beta"})
	spaceTestConstraint(t, err, sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY) // no beta bucket with this name
	if err := s.CreateBucket(ctx, "ghost", v.BackingBucket); err != nil {
		t.Fatal(err)
	}
	err = spaceTestClone(s, "spaces", spaceColumns, "alpha", map[string]any{"tenant_id": "ghost"})
	spaceTestConstraint(t, err, sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY) // bucket exists, tenant does not
	err = spaceTestClone(s, "space_members", spaceMemberColumns, "alpha", map[string]any{"tenant_id": "beta"})
	spaceTestConstraint(t, err, sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY)
	err = spaceTestClone(s, "space_history", spaceHistoryColumns, "alpha", map[string]any{"tenant_id": "beta"})
	spaceTestConstraint(t, err, sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY)
	// SQLite implements immediate ON DELETE RESTRICT via internal triggers:
	// inserts above yield FOREIGNKEY(787), these deletes yield TRIGGER(1811).
	_, err = s.db.ExecContext(ctx, s.rebind(`DELETE FROM buckets WHERE tenant_id='alpha' AND name=$1`), v.BackingBucket)
	spaceTestConstraint(t, err, sqlite3.SQLITE_CONSTRAINT_TRIGGER)
	_, err = s.db.ExecContext(ctx, `DELETE FROM tenants WHERE tenant_id='alpha'`)
	spaceTestConstraint(t, err, sqlite3.SQLITE_CONSTRAINT_TRIGGER)
	spaceTestPersonalIndex(t, s)
	spaceTestRevision(t, s, v, 1)
}

func spaceTestPersonalIndex(t *testing.T, s *sqlStore) {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(), `PRAGMA index_info(spaces_personal_subject_uq)`)
	if err != nil {
		t.Fatal(err)
	}
	cols, err := collectSpaceRows(rows, func(row spaceScanner) (string, error) {
		var seq, cid int
		var name string
		err := row.Scan(&seq, &cid, &name)
		return name, err
	})
	if err != nil || !reflect.DeepEqual(cols, []string{"tenant_id", "personal_source", "personal_kind", "personal_subject_id"}) {
		t.Fatalf("index=%v %v", cols, err)
	}
}

func TestSpaceSQLiteMemberIdentityAndHistoryKeys(t *testing.T) {
	s, _ := spaceTestDB(t)
	v := spaceTestSpace(t, s, spaceTestCreate("alpha", spaces.Team, spaceTestSubject("a", "same")))
	for _, replacement := range []map[string]any{{"subject_kind": "service"}, {"subject_source": "b"}} {
		if err := spaceTestClone(s, "space_members", spaceMemberColumns, "alpha", replacement); err != nil {
			t.Fatal(err)
		}
	}
	err := spaceTestClone(s, "space_members", spaceMemberColumns, "alpha", nil)
	spaceTestConstraint(t, err, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY)
	spaceTestCount(t, s, "space_members", 3)
	err = spaceTestClone(s, "space_history", spaceHistoryColumns, "alpha", map[string]any{"id": uuid.NewString()})
	spaceTestConstraint(t, err, sqlite3.SQLITE_CONSTRAINT_UNIQUE)
	err = spaceTestClone(s, "space_history", spaceHistoryColumns, "alpha", map[string]any{"id": uuid.NewString(), "command_id": uuid.NewString()})
	spaceTestConstraint(t, err, sqlite3.SQLITE_CONSTRAINT_UNIQUE) // distinct command, same root revision
	spaceTestRevision(t, s, v, 1)
}

func TestSpaceSQLiteChecksRejectNullEnumAndNonInteger(t *testing.T) {
	s, _ := spaceTestDB(t)
	v := spaceTestSpace(t, s, spaceTestCreate("alpha", spaces.Personal, spaceTestSubject("a", "same")))
	cases := map[string][]string{
		"spaces":        {"kind='unknown'", "personal_source=NULL", "personal_subject_id=NULL", "personal_kind=NULL", "personal_kind='service'", "personal_source=''", "revision=0", "created_by_kind='employee'", "updated_by_source=''"},
		"space_members": {"role='owner'", "state='gone'", "subject_source=''", "subject_kind='employee'", "changed_revision=0", "revoked_at_ns=1", "state='revoked'", "changed_by_kind='employee'"},
		"space_history": {"command_type='unknown'", "before_revision=1", "after_revision=0", "actor_source=''", "target_kind='employee'", "before_role='admin'", "before_state='active'", "after_state='revoked'"},
	}
	for table, setters := range cases {
		for _, set := range setters {
			_, err := s.db.ExecContext(context.Background(), `UPDATE `+table+` SET `+set)
			spaceTestConstraint(t, err, sqlite3.SQLITE_CONSTRAINT_CHECK)
		}
	}
	for table, columns := range map[string][]string{
		"spaces":        {"revision", "created_at_ns", "updated_at_ns"},
		"space_members": {"changed_revision", "joined_at_ns", "updated_at_ns", "revoked_at_ns"},
		"space_history": {"before_revision", "after_revision", "occurred_at_ns"},
	} {
		for _, col := range columns {
			for _, value := range []string{"1.5", "'2026-10-17T00:00:00Z'"} {
				_, err := s.db.ExecContext(context.Background(), `UPDATE `+table+` SET `+col+`=`+value)
				spaceTestConstraint(t, err, sqlite3.SQLITE_CONSTRAINT_CHECK)
			}
		}
	}
	spaceTestRevision(t, s, v, 1)
	spaceTestNanoseconds(t, s, v)
}

func TestSpaceSQLiteTeamRejectsPersonalFields(t *testing.T) {
	s, _ := spaceTestDB(t)
	v := spaceTestSpace(t, s, spaceTestCreate("alpha", spaces.Team, spaceTestSubject("a", "u")))
	spaceTestEqual(t, "team has no personal association", v.PersonalSubject, (*spaces.SubjectRef)(nil))
	for _, setter := range []string{"personal_source='a'", "personal_kind='user'", "personal_subject_id='u'", "kind='personal'"} {
		_, err := s.db.ExecContext(context.Background(), `UPDATE spaces SET `+setter)
		spaceTestConstraint(t, err, sqlite3.SQLITE_CONSTRAINT_CHECK)
	}
	spaceTestRevision(t, s, v, 1)
}

func spaceTestNanoseconds(t *testing.T, s *sqlStore, v spaces.Space) {
	t.Helper()
	var n int64
	var kind string
	err := s.db.QueryRowContext(context.Background(), `SELECT created_at_ns,typeof(created_at_ns) FROM spaces`).Scan(&n, &kind)
	if err != nil || n != v.CreatedAt.UnixNano() || kind != "integer" {
		t.Fatalf("nanoseconds=%d %s %v", n, kind, err)
	}
	m, err := s.GetSpaceMember(context.Background(), "alpha", v.ID, v.CreatedBy)
	if err != nil || m.JoinedAt.UnixNano() != n {
		t.Fatalf("member nanoseconds=%+v %v", m, err)
	}
	h, err := s.ListSpaceHistory(context.Background(), "alpha", v.ID, spaces.HistoryPage{Limit: 1})
	if err != nil || len(h) != 1 || h[0].OccurredAt.UnixNano() != n {
		t.Fatalf("history nanoseconds=%+v %v", h, err)
	}
}

func TestSpaceReopenAndMigrateSkip0048(t *testing.T) {
	s, dsn := spaceTestDB(t)
	ctx := context.Background()
	v := spaceTestSpace(t, s, spaceTestCreate("alpha", spaces.Personal, spaceTestSubject("a", "u")))
	spaceTestApply(t, s, &v, spaceTestChange(v, spaceTestSubject("b", "u"), spaces.AddMember, spaces.Editor))
	before, err := s.GetSpace(ctx, "alpha", v.ID)
	spaceTestOK(t, err)
	members, err := s.ListSpaceMembers(ctx, "alpha", v.ID, spaces.MemberPage{Limit: 100})
	spaceTestOK(t, err)
	history, err := s.ListSpaceHistory(ctx, "alpha", v.ID, spaces.HistoryPage{Limit: 100})
	spaceTestOK(t, err)
	spaceTestOK(t, s.Close())
	reopened := spaceTestOpen(t, dsn)
	for i := 0; i < 2; i++ {
		spaceTestOK(t, reopened.Migrate(ctx))
	}
	got, err := reopened.GetPersonalSpace(ctx, "alpha", v.CreatedBy)
	spaceTestOK(t, err)
	spaceTestEqual(t, "reopened root", got, before)
	gotMembers, err := reopened.ListSpaceMembers(ctx, "alpha", v.ID, spaces.MemberPage{Limit: 100})
	spaceTestOK(t, err)
	spaceTestEqual(t, "reopened members", gotMembers, members)
	gotHistory, err := reopened.ListSpaceHistory(ctx, "alpha", v.ID, spaces.HistoryPage{Limit: 100})
	spaceTestOK(t, err)
	spaceTestEqual(t, "reopened history", gotHistory, history)
	var count int
	spaceTestOK(t, reopened.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version='0048_spaces_foundation'`).Scan(&count))
	spaceTestEqual(t, "0048 applied exactly once", count, 1)
}

func spaceTest0047DB(t *testing.T) *sqlStore {
	t.Helper()
	s := spaceTestOpen(t, "file:"+filepath.Join(t.TempDir(), "legacy.db"))
	ctx := context.Background()
	_, err := s.db.ExecContext(ctx, `CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT '')`)
	if err != nil {
		t.Fatal(err)
	}
	files, err := listMigrationFiles("migrations/sqlite")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.version >= "0048" {
			continue
		}
		if err := s.applyMigration(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func spaceTestLegacyObjects(t *testing.T, s *sqlStore, local *storage.LocalStorage) []Object {
	t.Helper()
	ctx := context.Background()
	for _, bucket := range []string{"default", "legacy"} {
		if err := s.SetBucketQuota(ctx, "", bucket, 4096, 12); err != nil {
			t.Fatal(err)
		}
	}
	objects := make([]Object, 0)
	for i, bucket := range []string{"default", "default", "legacy"} {
		vid := NewVersionID()
		key := path.Join("default", bucket, "old@v-key")
		if i < 2 {
			key += "@v" + vid
		}
		content := fmt.Sprintf("legacy bytes %d", i)
		info, err := local.Put(ctx, key, strings.NewReader(content), int64(len(content)), storage.PutOptions{})
		if err != nil {
			t.Fatal(err)
		}
		obj := Object{Bucket: bucket, Key: "old@v-key", VersionID: vid, Backend: "local", StorageKey: key, Size: info.Size, ETag: info.ETag, Metadata: map[string]string{"old": content}}
		if i < 2 {
			obj, err = s.InsertObjectVersion(ctx, obj)
		} else {
			obj, err = s.UpsertObject(ctx, obj)
		}
		if err != nil {
			t.Fatal(err)
		}
		objects = append(objects, obj)
	}
	for i, obj := range objects {
		var err error
		objects[i], err = s.GetObjectVersion(ctx, "default", obj.Bucket, obj.Key, obj.VersionID)
		if err != nil {
			t.Fatal(err)
		}
	}
	return objects
}

func spaceTestLegacyUnchanged(t *testing.T, s *sqlStore, local *storage.LocalStorage, want []Object, configs []BucketConfig, share access.Share) {
	t.Helper()
	ctx := context.Background()
	for _, obj := range want {
		got, err := s.GetObjectVersion(ctx, "", obj.Bucket, obj.Key, obj.VersionID)
		spaceTestOK(t, err)
		spaceTestEqual(t, "legacy object", got, obj)
		r, _, err := local.Get(ctx, got.StorageKey)
		spaceTestOK(t, err)
		body, readErr := io.ReadAll(r)
		closeErr := r.Close()
		spaceTestOK(t, readErr)
		spaceTestOK(t, closeErr)
		spaceTestEqual(t, "legacy blob", string(body), obj.Metadata["old"])
	}
	for _, config := range configs {
		got, err := s.GetBucketConfig(ctx, "", config.Name)
		spaceTestOK(t, err)
		spaceTestEqual(t, "legacy bucket", got, config)
	}
	gotShare, err := s.GetShareByTokenHash(ctx, share.TokenHash)
	spaceTestOK(t, err)
	spaceTestEqual(t, "legacy share", gotShare, share)
}

func spaceTestEmptyDown(t *testing.T, s *sqlStore) {
	t.Helper()
	ctx := context.Background()
	down, err := fs.ReadFile(migrationsFS, "migrations/sqlite/0048_spaces_foundation.down.sql")
	spaceTestOK(t, err)
	_, err = s.db.ExecContext(ctx, string(down))
	spaceTestOK(t, err)
	var count int
	spaceTestOK(t, s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name IN ('spaces','space_members','space_history')`).Scan(&count))
	spaceTestEqual(t, "down removed only foundation tables", count, 0)
}

func TestSpaceMigrationLegacyZeroBackfillAndEmptyDown(t *testing.T) {
	s := spaceTest0047DB(t)
	ctx := context.Background()
	local, err := storage.NewLocal(storage.LocalConfig{Root: t.TempDir()})
	spaceTestOK(t, err)
	objects := spaceTestLegacyObjects(t, s, local)
	configs := make([]BucketConfig, 0)
	for _, bucket := range []string{"default", "legacy"} {
		config, err := s.GetBucketConfig(ctx, "", bucket)
		if err != nil {
			t.Fatal(err)
		}
		configs = append(configs, config)
	}
	share := access.Share{ID: uuid.NewString(), TenantID: "default", Bucket: "default", Key: objects[0].Key, TokenHash: "legacy-token-hash", AllowDownload: true, CreatedAt: time.Now().UTC()}
	if err := s.CreateShare(ctx, share); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"spaces", "space_members", "space_history", "tenants"} {
		spaceTestCount(t, s, table, 0)
	}
	spaceTestLegacyUnchanged(t, s, local, objects, configs, share)
	spaceTestEmptyDown(t, s)
	spaceTestLegacyUnchanged(t, s, local, objects, configs, share)
	// Existing implicit-default CRUD remains available after manual empty down.
	latest, err := s.GetObject(ctx, "", "legacy", objects[2].Key)
	if err != nil {
		t.Fatal(err)
	}
	latest.Size++
	if _, err := s.UpsertObject(ctx, latest); err != nil {
		t.Fatal(err)
	}
	if err := s.SoftDeleteObject(ctx, "", "legacy", latest.Key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetObject(ctx, "", "legacy", latest.Key); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
