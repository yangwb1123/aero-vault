package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/spaces"
	"github.com/jackc/pgx/v5/pgconn"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func spaceTestTrigger(t *testing.T, s *sqlStore, event, table, raise string) func() {
	t.Helper()
	_, err := s.db.ExecContext(context.Background(), `CREATE TRIGGER space_test_fault BEFORE `+event+` ON `+table+` BEGIN SELECT RAISE(`+raise+`); END`)
	if err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if _, err := s.db.ExecContext(context.Background(), `DROP TRIGGER space_test_fault`); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSpaceCreateRollbackEachStep(t *testing.T) {
	for _, table := range []string{"buckets", "spaces", "space_members", "space_history"} {
		t.Run(table, func(t *testing.T) {
			s, _ := spaceTestDB(t)
			drop := spaceTestTrigger(t, s, "INSERT", table, "ABORT,'injected create failure'")
			c := spaceTestCreate("alpha", spaces.Personal, spaceTestSubject("a", "u"))
			_, err := s.CreateSpace(context.Background(), c)
			spaceTestError(t, err, spaces.ErrPersistence)
			var sq *sqlite.Error
			if !errors.As(err, &sq) || sq.Code() != sqlite3.SQLITE_CONSTRAINT_TRIGGER {
				t.Fatalf("lost trigger cause: %v", err)
			}
			for _, table := range []string{"buckets", "spaces", "space_members", "space_history"} {
				spaceTestCount(t, s, table, 0)
			}
			drop()
			spaceTestSpace(t, s, c)
			for _, table := range []string{"buckets", "spaces", "space_members", "space_history"} {
				spaceTestCount(t, s, table, 1)
			}
		})
	}
}

func TestSpaceMemberRollbackEachStep(t *testing.T) {
	for _, step := range []struct {
		table, event, raise string
		add                 bool
	}{
		{"spaces", "UPDATE", "ABORT,'CAS fault'", false},
		{"space_members", "UPDATE", "ABORT,'member fault'", false},
		{"space_history", "INSERT", "ABORT,'history fault'", false},
		{"space_members", "INSERT", "ABORT,'add fault'", true},
		{"space_members", "UPDATE", "IGNORE", false},
		{"space_members", "INSERT", "IGNORE", true},
	} {
		t.Run(step.table+step.event+step.raise, func(t *testing.T) {
			s, _ := spaceTestDB(t)
			ctx := context.Background()
			v := spaceTestSpace(t, s, spaceTestCreate("alpha", spaces.Team, spaceTestSubject("a", "u")))
			u := spaceTestSubject("b", "u")
			spaceTestApply(t, s, &v, spaceTestChange(v, u, spaces.AddMember, spaces.Viewer))
			before, err := s.GetSpace(ctx, "alpha", v.ID)
			if err != nil {
				t.Fatal(err)
			}
			member, err := s.GetSpaceMember(ctx, "alpha", v.ID, u)
			if err != nil {
				t.Fatal(err)
			}
			c := spaceTestChange(v, u, spaces.ChangeRole, spaces.Editor)
			if step.add {
				c.Target = spaceTestSubject("c", "new")
				c.Action = spaces.AddMember
			}
			drop := spaceTestTrigger(t, s, step.event, step.table, step.raise)
			_, err = s.ChangeSpaceMember(ctx, c)
			spaceTestError(t, err, spaces.ErrPersistence)
			got, err := s.GetSpace(ctx, "alpha", v.ID)
			if err != nil || !reflect.DeepEqual(got, before) {
				t.Fatalf("root changed=%+v %v", got, err)
			}
			gotMember, err := s.GetSpaceMember(ctx, "alpha", v.ID, u)
			if err != nil || !reflect.DeepEqual(gotMember, member) {
				t.Fatalf("member changed=%+v %v", gotMember, err)
			}
			spaceTestCount(t, s, "space_members", 2)
			spaceTestRevision(t, s, v, 2)
			drop()
			spaceTestApply(t, s, &v, c)
			spaceTestRevision(t, s, v, 3)
		})
	}
}

func TestSpaceBusyIsRecoverableAcrossOpen(t *testing.T) {
	s, dsn := spaceTestDB(t)
	other := spaceTestOpen(t, dsn)
	ctx := context.Background()
	v := spaceTestSpace(t, s, spaceTestCreate("alpha", spaces.Team, spaceTestSubject("a", "u")))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE spaces SET revision=revision`); err != nil {
		t.Fatal(err)
	}
	c := spaceTestChange(v, spaceTestSubject("b", "u"), spaces.AddMember, spaces.Editor)
	_, err = other.ChangeSpaceMember(ctx, c)
	spaceTestError(t, err, spaces.ErrConcurrent)
	var sq *sqlite.Error
	if !errors.As(err, &sq) || sq.Code()&0xff != sqlite3.SQLITE_BUSY {
		t.Fatalf("lost BUSY cause: %v", err)
	}
	_, err = other.CreateSpace(ctx, spaceTestCreate("alpha", spaces.Personal, v.CreatedBy))
	spaceTestError(t, err, spaces.ErrConcurrent)
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	spaceTestRevision(t, other, v, 1)
	spaceTestApply(t, other, &v, c)
	spaceTestRevision(t, s, v, 2)
}

func spaceTestPortErrors(ctx context.Context, s *sqlStore, v spaces.Space) []error {
	out := make([]error, 0)
	_, err := s.CreateSpace(ctx, spaceTestCreate(v.TenantID, spaces.Team, v.CreatedBy))
	out = append(out, err)
	_, err = s.GetSpace(ctx, v.TenantID, v.ID)
	out = append(out, err)
	_, err = s.GetPersonalSpace(ctx, v.TenantID, v.CreatedBy)
	out = append(out, err)
	_, err = s.GetSpaceByBucket(ctx, v.TenantID, v.BackingBucket)
	out = append(out, err)
	_, err = s.GetSpaceMember(ctx, v.TenantID, v.ID, v.CreatedBy)
	out = append(out, err)
	_, err = s.ListSpaceMembers(ctx, v.TenantID, v.ID, spaces.MemberPage{Limit: 1})
	out = append(out, err)
	_, err = s.ListSpacesForSubject(ctx, v.TenantID, v.CreatedBy, spaces.SpacePage{Limit: 1})
	out = append(out, err)
	_, err = s.ListSpaceHistory(ctx, v.TenantID, v.ID, spaces.HistoryPage{Limit: 1})
	out = append(out, err)
	_, err = s.ChangeSpaceMember(ctx, spaceTestChange(v, v.CreatedBy, spaces.ChangeRole, spaces.Viewer))
	out = append(out, err)
	return out
}

func TestSpaceContextErrorsRetainCauseAcrossPort(t *testing.T) {
	s, _ := spaceTestDB(t)
	v := spaceTestSpace(t, s, spaceTestCreate("alpha", spaces.Personal, spaceTestSubject("a", "u")))
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer stop()
	for _, ctx := range []context.Context{cancelled, expired} {
		for i, err := range spaceTestPortErrors(ctx, s, v) {
			spaceTestError(t, err, spaces.ErrPersistence)
			if !errors.Is(err, ctx.Err()) {
				t.Errorf("method %d lost context: %v", i, err)
			}
			if errors.Is(err, spaces.ErrNotFound) || errors.Is(err, spaces.ErrConcurrent) {
				t.Errorf("misclassified cancellation: %v", err)
			}
		}
	}
	spaceTestRevision(t, s, v, 1)
}

func TestSpaceClosedDBIsNotEmptySuccess(t *testing.T) {
	s, _ := spaceTestDB(t)
	v := spaceTestSpace(t, s, spaceTestCreate("alpha", spaces.Personal, spaceTestSubject("a", "u")))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, err := range spaceTestPortErrors(context.Background(), s, v) {
		spaceTestError(t, err, spaces.ErrPersistence)
	}
}

func TestSpaceSQLTypedErrorClassification(t *testing.T) {
	for _, test := range []struct {
		code, constraint string
		want             error
	}{
		{"23505", "spaces_personal_subject_uq", spaces.ErrConflict}, {"23505", "buckets_pkey", spaces.ErrConflict},
		{"23505", "unknown_unique", spaces.ErrPersistence}, {"23503", "spaces_bucket_uq", spaces.ErrPersistence},
		{"23514", "", spaces.ErrPersistence}, {"40001", "", spaces.ErrConcurrent},
		{"40P01", "", spaces.ErrConcurrent}, {"55P03", "", spaces.ErrConcurrent}, {"57014", "", spaces.ErrPersistence},
	} {
		cause := &pgconn.PgError{Code: test.code, ConstraintName: test.constraint}
		err := spaceSQLError(context.Background(), "synthetic PG (not real DB)", fmt.Errorf("wrapped: %w", cause))
		spaceTestError(t, err, test.want)
		var got *pgconn.PgError
		if !errors.As(err, &got) || got != cause {
			t.Fatalf("lost PG cause: %v", err)
		}
	}
	fake := errors.New("UNIQUE constraint failed; SQLITE_BUSY; 23505")
	err := spaceSQLError(context.Background(), "not a driver error", fake)
	spaceTestError(t, err, spaces.ErrPersistence)
	spaceTestError(t, err, fake)
}

type spaceFailScanner struct{ err error }

func (f spaceFailScanner) Scan(...any) error { return f.err }

type spaceTestResult struct {
	n   int64
	err error
}

func (r spaceTestResult) LastInsertId() (int64, error) { return 0, r.err }
func (r spaceTestResult) RowsAffected() (int64, error) { return r.n, r.err }

func TestSpaceScanRowsAndAffectedErrors(t *testing.T) {
	ctx := context.Background()
	cause := errors.New("scan/affected failure")
	_, err := scanSpace(spaceFailScanner{cause})
	spaceTestError(t, spaceSQLError(ctx, "scan", err), cause)
	_, err = scanSpaceMember(spaceFailScanner{cause})
	spaceTestError(t, spaceSQLError(ctx, "scan", err), cause)
	_, err = scanSpaceHistory(spaceFailScanner{cause})
	spaceTestError(t, spaceSQLError(ctx, "scan", err), cause)
	for _, result := range []spaceTestResult{{n: 0}, {n: 2}, {n: 1, err: cause}} {
		err := spaceSQLError(ctx, "affected", spaceOneRow(result, nil))
		spaceTestError(t, err, spaces.ErrPersistence)
		if result.err != nil {
			spaceTestError(t, err, cause)
		}
	}
	s, _ := spaceTestDB(t)
	rows, err := s.db.QueryContext(ctx, `SELECT 1 UNION ALL SELECT abs(-9223372036854775808)`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := collectSpaceRows(rows, func(row spaceScanner) (int, error) { var n int; err := row.Scan(&n); return n, err })
	if err == nil || out != nil {
		t.Fatalf("partial rows returned: %v %v", out, err)
	}
	spaceTestError(t, spaceSQLError(ctx, "rows.Err", err), spaces.ErrPersistence)
	_, err = spaceRead(ctx, s, "alpha", func(tx *sql.Tx) (int, error) { _ = tx.Rollback(); return 1, nil })
	spaceTestError(t, err, spaces.ErrPersistence)
	spaceTestError(t, err, sql.ErrTxDone)
}

func TestSpacePublicQueryScanFailure(t *testing.T) {
	s, _ := spaceTestDB(t)
	v := spaceTestSpace(t, s, spaceTestCreate("alpha", spaces.Team, spaceTestSubject("a", "u")))
	cols := strings.Replace(spaceColumns, "revision", "'bad-integer' AS revision", 1)
	_, err := s.db.ExecContext(context.Background(), `ALTER TABLE spaces RENAME TO saved_spaces; CREATE VIEW spaces AS SELECT `+cols+` FROM saved_spaces`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSpace(context.Background(), v.TenantID, v.ID)
	spaceTestError(t, err, spaces.ErrPersistence)
	if got.ID != "" {
		t.Fatalf("partial root=%+v", got)
	}
}
