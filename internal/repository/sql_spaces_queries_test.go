package repository

import (
	"context"
	"database/sql"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"testing"

	"github.com/aero-vault/aero-vault/internal/spaces"
	"github.com/google/uuid"
)

func TestSpaceQueriesCompleteTenantAndIdentity(t *testing.T) {
	s, _ := spaceTestDB(t)
	ctx := context.Background()
	a := spaceTestSubject("a", "same")
	c := spaceTestCreate("alpha", spaces.Personal, a)
	v := spaceTestSpace(t, s, c)
	c.TenantID, c.Name = "beta", "beta root"
	beta := spaceTestSpace(t, s, c) // Same UUID/command/subject, different tenant.
	service := a
	service.Kind = spaces.Service
	b := spaceTestSubject("b", "same")
	second := spaceTestSubject("a", "same-z")
	spaceTestApply(t, s, &v, spaceTestChange(v, service, spaces.AddMember, spaces.Viewer))
	spaceTestApply(t, s, &v, spaceTestChange(v, b, spaces.AddMember, spaces.Editor))
	spaceTestApply(t, s, &v, spaceTestChange(v, second, spaces.AddMember, spaces.Viewer))
	for _, subject := range []spaces.SubjectRef{a, service, b, second} {
		m, err := s.GetSpaceMember(ctx, "alpha", v.ID, subject)
		spaceTestOK(t, err)
		spaceTestEqual(t, "member identity", m.Subject, subject)
		list, err := s.ListSpacesForSubject(ctx, "alpha", subject, spaces.SpacePage{Limit: 10})
		spaceTestOK(t, err)
		spaceTestEqual(t, "subject list size", len(list), 1)
		spaceTestEqual(t, "subject list tenant", list[0].TenantID, "alpha")
		spaceTestEqual(t, "subject list root", list[0].ID, v.ID)
	}
	for _, subject := range []spaces.SubjectRef{service, b} {
		_, err := s.GetSpaceMember(ctx, "beta", beta.ID, subject)
		spaceTestError(t, err, spaces.ErrNotFound)
		list, err := s.ListSpacesForSubject(ctx, "beta", subject, spaces.SpacePage{Limit: 10})
		if err != nil || len(list) != 0 {
			t.Fatalf("cross tenant=%+v %v", list, err)
		}
	}
	got, err := s.GetSpace(ctx, "beta", v.ID)
	spaceTestOK(t, err)
	spaceTestEqual(t, "separate tenant root", got, beta)
	_, err = s.GetPersonalSpace(ctx, "alpha", b)
	spaceTestError(t, err, spaces.ErrNotFound)
	_, err = s.GetPersonalSpace(ctx, "alpha", service)
	spaceTestError(t, err, spaces.ErrInvalidArgument)
	spaceTestMemberPaging(t, s, v, []spaces.SubjectRef{a, b, service, second})
}

func spaceTestMemberPaging(t *testing.T, s *sqlStore, v spaces.Space, want []spaces.SubjectRef) {
	t.Helper()
	sort.Slice(want, func(i, j int) bool {
		a, b := want[i], want[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.ID < b.ID
	})
	var after *spaces.SubjectRef
	got := make([]spaces.SubjectRef, 0)
	for i := 0; i <= len(want); i++ {
		page, err := s.ListSpaceMembers(context.Background(), v.TenantID, v.ID, spaces.MemberPage{After: after, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		got = append(got, page[0].Subject)
		a := page[0].Subject
		after = &a
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("member cursor=%+v, want %+v", got, want)
	}
}

func TestSpaceQueriesSubjectPagesExcludeRevoked(t *testing.T) {
	s, _ := spaceTestDB(t)
	ctx := context.Background()
	a := spaceTestSubject("a", "same")
	want := make([]string, 0)
	for i := 0; i < 3; i++ {
		v := spaceTestSpace(t, s, spaceTestCreate("alpha", spaces.Team, a))
		want = append(want, v.ID)
	}
	sort.Strings(want)
	got, after := make([]string, 0), ""
	for i := 0; i < 4; i++ {
		page, err := s.ListSpacesForSubject(ctx, "alpha", a, spaces.SpacePage{AfterID: after, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		after = page[0].ID
		got = append(got, after)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("space cursor=%v want %v", got, want)
	}
	v, err := s.GetSpace(ctx, "alpha", want[0])
	if err != nil {
		t.Fatal(err)
	}
	other := spaceTestSubject("b", "same")
	spaceTestApply(t, s, &v, spaceTestChange(v, other, spaces.AddMember, spaces.Admin))
	spaceTestApply(t, s, &v, spaceTestChange(v, a, spaces.RevokeMember, ""))
	page, err := s.ListSpacesForSubject(ctx, "alpha", a, spaces.SpacePage{Limit: 10})
	if err != nil || len(page) != 2 {
		t.Fatalf("revoked relationship listed: %+v %v", page, err)
	}
}

func TestSpaceQueriesUnknownRootIsNotEmptyPage(t *testing.T) {
	s, _ := spaceTestDB(t)
	ctx := context.Background()
	v := spaceTestSpace(t, s, spaceTestCreate("alpha", spaces.Team, spaceTestSubject("a", "u")))
	for _, id := range []string{uuid.NewString(), v.ID} {
		_, err := s.GetSpace(ctx, "beta", id)
		spaceTestError(t, err, spaces.ErrNotFound)
		_, err = s.GetSpaceByBucket(ctx, "beta", "spc-"+id)
		spaceTestError(t, err, spaces.ErrNotFound)
		_, err = s.ListSpaceMembers(ctx, "beta", id, spaces.MemberPage{Limit: 1})
		spaceTestError(t, err, spaces.ErrNotFound)
		_, err = s.ListSpaceHistory(ctx, "beta", id, spaces.HistoryPage{Limit: 1})
		spaceTestError(t, err, spaces.ErrNotFound)
	}
	h, err := s.ListSpaceHistory(ctx, "alpha", v.ID, spaces.HistoryPage{AfterRevision: 1, Limit: 1})
	if err != nil || len(h) != 0 {
		t.Fatalf("valid empty history=%+v %v", h, err)
	}
	m, err := s.ListSpaceMembers(ctx, "alpha", v.ID, spaces.MemberPage{After: &spaces.SubjectRef{Source: "z", Kind: spaces.User, ID: "z"}, Limit: 1})
	if err != nil || len(m) != 0 {
		t.Fatalf("valid empty members=%+v %v", m, err)
	}
}

func TestSpaceTenantUnavailableAcrossPort(t *testing.T) {
	s, _ := spaceTestDB(t)
	ctx := context.Background()
	c := spaceTestCreate("alpha", spaces.Personal, spaceTestSubject("a", "u"))
	v := spaceTestSpace(t, s, c)
	if err := s.UpsertTenant(ctx, TenantRecord{TenantID: "alpha", Status: "disabled"}); err != nil {
		t.Fatal(err)
	}
	for _, tenant := range []string{"alpha", "missing"} {
		c.TenantID = tenant
		c.ID = uuid.NewString()
		spaceTestPortTenant(t, s, tenant, v, c)
	}
	spaceTestCount(t, s, "tenants", 2)
	for _, table := range []string{"spaces", "space_members", "space_history", "buckets"} {
		spaceTestCount(t, s, table, 1)
	}
}

func spaceTestPortTenant(t *testing.T, s *sqlStore, tenant string, v spaces.Space, c spaces.CreateCommand) {
	t.Helper()
	ctx := context.Background()
	_, err := s.CreateSpace(ctx, c)
	spaceTestError(t, err, spaces.ErrTenantUnavailable)
	_, err = s.GetSpace(ctx, tenant, v.ID)
	spaceTestError(t, err, spaces.ErrTenantUnavailable)
	_, err = s.GetSpaceByBucket(ctx, tenant, v.BackingBucket)
	spaceTestError(t, err, spaces.ErrTenantUnavailable)
	_, err = s.GetPersonalSpace(ctx, tenant, c.Actor)
	spaceTestError(t, err, spaces.ErrTenantUnavailable)
	_, err = s.GetSpaceMember(ctx, tenant, v.ID, c.Actor)
	spaceTestError(t, err, spaces.ErrTenantUnavailable)
	_, err = s.ListSpaceMembers(ctx, tenant, v.ID, spaces.MemberPage{Limit: 1})
	spaceTestError(t, err, spaces.ErrTenantUnavailable)
	_, err = s.ListSpacesForSubject(ctx, tenant, c.Actor, spaces.SpacePage{Limit: 1})
	spaceTestError(t, err, spaces.ErrTenantUnavailable)
	_, err = s.ListSpaceHistory(ctx, tenant, v.ID, spaces.HistoryPage{Limit: 1})
	spaceTestError(t, err, spaces.ErrTenantUnavailable)
	change := spaceTestChange(v, c.Actor, spaces.ChangeRole, spaces.Viewer)
	change.TenantID = tenant
	_, err = s.ChangeSpaceMember(ctx, change)
	spaceTestError(t, err, spaces.ErrTenantUnavailable)
}

func TestSpaceInvalidCommandsLeaveFactsUnchanged(t *testing.T) {
	s, _ := spaceTestDB(t)
	ctx := context.Background()
	v := spaceTestSpace(t, s, spaceTestCreate("alpha", spaces.Personal, spaceTestSubject("a", "u")))
	valid := spaceTestChange(v, spaceTestSubject("b", "u"), spaces.AddMember, spaces.Viewer)
	for _, mutate := range []func(*spaces.MemberCommand){
		func(c *spaces.MemberCommand) { c.TenantID = "" }, func(c *spaces.MemberCommand) { c.TenantID = "*" },
		func(c *spaces.MemberCommand) { c.SpaceID = "bad" }, func(c *spaces.MemberCommand) { c.CommandID = "bad" },
		func(c *spaces.MemberCommand) { c.Actor.Source = "" }, func(c *spaces.MemberCommand) { c.Target.Kind = "unknown" },
		func(c *spaces.MemberCommand) { c.ExpectedRevision = 0 }, func(c *spaces.MemberCommand) { c.ExpectedRevision = -1 },
		func(c *spaces.MemberCommand) { c.ExpectedRevision = math.MaxInt64 }, func(c *spaces.MemberCommand) { c.Role = "owner" },
		func(c *spaces.MemberCommand) { c.Action = "remove" },
	} {
		c := valid
		mutate(&c)
		_, err := s.ChangeSpaceMember(ctx, c)
		spaceTestError(t, err, spaces.ErrInvalidArgument)
	}
	for _, tenant := range []string{"", "*", "alpha\x00"} {
		c := spaceTestCreate(tenant, spaces.Team, v.CreatedBy)
		_, err := s.CreateSpace(ctx, c)
		spaceTestError(t, err, spaces.ErrInvalidArgument)
		_, err = s.GetSpace(ctx, tenant, v.ID)
		spaceTestError(t, err, spaces.ErrInvalidArgument)
	}
	_, err := s.ListSpaceHistory(ctx, "alpha", v.ID, spaces.HistoryPage{AfterRevision: -1, Limit: 1})
	spaceTestError(t, err, spaces.ErrInvalidArgument)
	_, err = s.ListSpaceMembers(ctx, "alpha", v.ID, spaces.MemberPage{Limit: 0})
	spaceTestError(t, err, spaces.ErrInvalidArgument)
	_, err = s.ListSpacesForSubject(ctx, "alpha", v.CreatedBy, spaces.SpacePage{Limit: 1001})
	spaceTestError(t, err, spaces.ErrInvalidArgument)
	got, err := s.GetSpace(ctx, "alpha", v.ID)
	if err != nil || !reflect.DeepEqual(got, v) {
		t.Fatalf("invalid command wrote root: %+v %v", got, err)
	}
	for _, table := range []string{"spaces", "space_members", "space_history", "buckets"} {
		spaceTestCount(t, s, table, 1)
	}
}

func TestSpaceRevisionMaxBoundary(t *testing.T) {
	s, _ := spaceTestDB(t)
	ctx := context.Background()
	v := spaceTestSpace(t, s, spaceTestCreate("alpha", spaces.Team, spaceTestSubject("a", "u")))
	// Isolated artificial near-exhaustion fixture, not a normal command history.
	if _, err := s.db.ExecContext(ctx, s.rebind(`UPDATE spaces SET revision=$1`), int64(math.MaxInt64-1)); err != nil {
		t.Fatal(err)
	}
	v.Revision = math.MaxInt64 - 1
	spaceTestApply(t, s, &v, spaceTestChange(v, spaceTestSubject("b", "u"), spaces.AddMember, spaces.Viewer))
	if v.Revision != math.MaxInt64 {
		t.Fatal(v.Revision)
	}
	_, err := s.ChangeSpaceMember(ctx, spaceTestChange(v, v.CreatedBy, spaces.ChangeRole, spaces.Viewer))
	spaceTestError(t, err, spaces.ErrInvalidArgument)
	got, err := s.GetSpace(ctx, "alpha", v.ID)
	if err != nil || got.Revision != math.MaxInt64 {
		t.Fatalf("overflow: %+v %v", got, err)
	}
	spaceTestCount(t, s, "space_history", 2)
}

func TestSpaceSQLBindingOccurrenceOrder(t *testing.T) {
	for _, text := range []string{"SELECT $1,$1", "SELECT $1,$3", "SELECT $2,$1"} {
		if spaceTestOrderedBinds(text) {
			t.Fatalf("guard missed %s", text)
		}
	}
	for _, file := range []string{"sql_spaces.go", "sql_spaces_commands.go", "sql_spaces_queries.go", "sql_spaces_members.go", "sql_spaces_history.go", "sql_spaces_tx.go"} {
		spaceTestSQLFileBinds(t, file)
	}
	s, _ := spaceTestDB(t)
	q := `SELECT $1,$2,$3,$4`
	if s.rebind(q) != `SELECT ?,?,?,?` {
		t.Fatal(s.rebind(q))
	}
	if (&sqlStore{dialect: dialectPostgres}).rebind(q) != q {
		t.Fatal("PG rebind changed placeholders")
	}
	var got [4]string
	err := s.db.QueryRowContext(context.Background(), s.rebind(q), "alpha", "alpha", "issuer", "user").Scan(&got[0], &got[1], &got[2], &got[3])
	spaceTestOK(t, err)
	spaceTestEqual(t, "I1 ordered bindings", got, [4]string{"alpha", "alpha", "issuer", "user"})
}

func spaceTestSQLFileBinds(t *testing.T, file string) {
	t.Helper()
	body, err := os.ReadFile(file)
	spaceTestOK(t, err)
	node, err := parser.ParseFile(token.NewFileSet(), file, body, 0)
	spaceTestOK(t, err)
	ast.Inspect(node, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		text, err := strconv.Unquote(lit.Value)
		if err != nil || !spaceTestOrderedBinds(text) {
			t.Errorf("invalid I1 SQL literal in %s: %s", file, lit.Value)
		}
		return true
	})
}

func spaceTestOrderedBinds(text string) bool {
	matches := regexp.MustCompile(`\$([0-9]+)`).FindAllStringSubmatch(text, -1)
	previous := 0
	for i, match := range matches {
		n, err := strconv.Atoi(match[1])
		if err != nil || n < 1 {
			return false
		}
		if i > 0 && n != previous+1 {
			return false
		}
		previous = n
	}
	return true
}

func TestSpaceReadKeepsSQLiteTenantSnapshot(t *testing.T) {
	s, dsn := spaceTestDB(t)
	other := spaceTestOpen(t, dsn)
	ctx := context.Background()
	v := spaceTestSpace(t, s, spaceTestCreate("alpha", spaces.Team, spaceTestSubject("a", "u")))
	got, err := spaceRead(ctx, s, "alpha", func(tx *sql.Tx) (spaces.Space, error) {
		if err := other.UpsertTenant(ctx, TenantRecord{TenantID: "alpha", Status: "disabled"}); err != nil {
			return spaces.Space{}, err
		}
		return s.spaceRoot(ctx, tx, "alpha", v.ID)
	})
	if err != nil || got.ID != v.ID {
		t.Fatalf("snapshot=%+v %v", got, err)
	}
	_, err = s.GetSpace(ctx, "alpha", v.ID)
	spaceTestError(t, err, spaces.ErrTenantUnavailable)
}
