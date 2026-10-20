package repository

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aero-vault/aero-vault/internal/spaces"
	"github.com/google/uuid"
)

func spaceTestOpen(t *testing.T, dsn string) *sqlStore {
	t.Helper()
	r, err := Open(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	s := r.(*sqlStore)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func spaceTestDB(t *testing.T) (*sqlStore, string) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "spaces.db")
	s := spaceTestOpen(t, dsn)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, tenant := range []string{"alpha", "beta"} {
		if err := s.UpsertTenant(context.Background(), TenantRecord{TenantID: tenant}); err != nil {
			t.Fatal(err)
		}
	}
	return s, dsn
}

func spaceTestSubject(source, id string) spaces.SubjectRef {
	return spaces.SubjectRef{Source: source, Kind: spaces.User, ID: id}
}

func spaceTestCreate(tenant string, kind spaces.Kind, actor spaces.SubjectRef) spaces.CreateCommand {
	return spaces.CreateCommand{
		TenantID: tenant, ID: uuid.NewString(), Kind: kind, Name: "同名空间",
		Actor: actor, CommandID: uuid.NewString(), Reason: "test", RequestID: "request",
	}
}

func spaceTestSpace(t *testing.T, s spaces.Store, c spaces.CreateCommand) spaces.Space {
	t.Helper()
	v, err := s.CreateSpace(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func spaceTestChange(v spaces.Space, target spaces.SubjectRef, action spaces.MemberAction, role spaces.Role) spaces.MemberCommand {
	return spaces.MemberCommand{
		TenantID: v.TenantID, SpaceID: v.ID, ExpectedRevision: v.Revision,
		Actor: v.CreatedBy, Target: target, Action: action, Role: role,
		CommandID: uuid.NewString(), Reason: "change", RequestID: "request-2",
	}
}

func spaceTestApply(t *testing.T, s spaces.Store, v *spaces.Space, c spaces.MemberCommand) {
	t.Helper()
	r, err := s.ChangeSpaceMember(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	v.Revision = r
}

func spaceTestOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func spaceTestEqual(t *testing.T, label string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: got=%+v, want=%+v", label, got, want)
	}
}

func spaceTestError(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want errors.Is(%v)", err, want)
	}
}

func spaceTestCount(t *testing.T, s *sqlStore, table string, want int) {
	t.Helper()
	var n int
	if err := s.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != want {
		t.Fatalf("%s rows = %d, want %d", table, n, want)
	}
}

func spaceTestRevision(t *testing.T, s spaces.Store, v spaces.Space, want int64) {
	t.Helper()
	got, err := s.GetSpace(context.Background(), v.TenantID, v.ID)
	if err != nil || got.Revision != want {
		t.Fatalf("root = %+v, err=%v; want revision %d", got, err, want)
	}
	h, err := s.ListSpaceHistory(context.Background(), v.TenantID, v.ID, spaces.HistoryPage{Limit: 100})
	if err != nil || int64(len(h)) != want {
		t.Fatalf("history count=%d err=%v, want %d", len(h), err, want)
	}
}
