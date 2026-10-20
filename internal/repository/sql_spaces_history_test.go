package repository

import (
	"context"
	"reflect"
	"testing"

	"github.com/aero-vault/aero-vault/internal/spaces"
)

func TestSpaceHistoryTrueBeforeAfterAndPaging(t *testing.T) {
	s, _ := spaceTestDB(t)
	ctx := context.Background()
	create := spaceTestCreate("alpha", spaces.Team, spaceTestSubject("oidc:a", "same"))
	v := spaceTestSpace(t, s, create)
	target := spaces.SubjectRef{Source: "key:b", Kind: spaces.Service, ID: "same"}
	actor := spaces.SubjectRef{Source: "job:c", Kind: spaces.Service, ID: "same"}
	commands := []spaces.MemberCommand{}
	for _, change := range []struct {
		action spaces.MemberAction
		role   spaces.Role
	}{
		{spaces.AddMember, spaces.Viewer}, {spaces.ChangeRole, spaces.Editor},
		{spaces.RevokeMember, ""}, {spaces.ReactivateMember, spaces.Admin},
	} {
		c := spaceTestChange(v, target, change.action, change.role)
		c.Actor = actor
		spaceTestApply(t, s, &v, c)
		commands = append(commands, c)
	}
	history, err := s.ListSpaceHistory(ctx, "alpha", v.ID, spaces.HistoryPage{Limit: 100})
	spaceTestOK(t, err)
	spaceTestEqual(t, "history length", len(history), 5)
	spaceTestEqual(t, "create history", history[0], spaces.History{TenantID: "alpha", ID: history[0].ID, SpaceID: v.ID,
		CommandID: create.CommandID, CommandType: "create", Actor: create.Actor, Target: create.Actor,
		AfterRevision: 1, AfterRole: spaces.Admin, AfterState: spaces.Active,
		Reason: create.Reason, RequestID: create.RequestID, OccurredAt: v.CreatedAt})
	beforeRoles := []spaces.Role{"", spaces.Viewer, spaces.Editor, spaces.Editor}
	beforeStates := []spaces.MemberState{"", spaces.Active, spaces.Active, spaces.Revoked}
	afterRoles := []spaces.Role{spaces.Viewer, spaces.Editor, spaces.Editor, spaces.Admin}
	for i, c := range commands {
		spaceTestMemberHistory(t, history[i+1], c, beforeRoles[i], beforeStates[i], afterRoles[i])
	}
	spaceTestHistoryPaging(t, s, v, history)
	got, err := s.GetSpace(ctx, "alpha", v.ID)
	spaceTestOK(t, err)
	v.UpdatedBy, v.UpdatedAt = actor, history[len(history)-1].OccurredAt
	spaceTestEqual(t, "immutable root and update facts", got, v)
}

func spaceTestMemberHistory(t *testing.T, got spaces.History, c spaces.MemberCommand, beforeRole spaces.Role, beforeState spaces.MemberState, afterRole spaces.Role) {
	t.Helper()
	afterState := spaces.Active
	if c.Action == spaces.RevokeMember {
		afterState = spaces.Revoked
	}
	spaceTestEqual(t, "member history", got, spaces.History{TenantID: c.TenantID, ID: got.ID, SpaceID: c.SpaceID,
		CommandID: c.CommandID, CommandType: string(c.Action), Actor: c.Actor, Target: c.Target,
		BeforeRevision: c.ExpectedRevision, AfterRevision: c.ExpectedRevision + 1,
		BeforeRole: beforeRole, BeforeState: beforeState, AfterRole: afterRole, AfterState: afterState,
		Reason: c.Reason, RequestID: c.RequestID, OccurredAt: got.OccurredAt})
}

func spaceTestHistoryPaging(t *testing.T, s *sqlStore, v spaces.Space, want []spaces.History) {
	t.Helper()
	ctx := context.Background()
	got := make([]spaces.History, 0)
	var after int64
	for i := 0; i <= len(want); i++ {
		page, err := s.ListSpaceHistory(ctx, v.TenantID, v.ID, spaces.HistoryPage{AfterRevision: after, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		got = append(got, page...)
		after = page[len(page)-1].AfterRevision
	}
	spaceTestEqual(t, "history pages", got, want)
	seen := map[string]bool{}
	for i, h := range got {
		spaceTestEqual(t, "before revision", h.BeforeRevision, int64(i))
		spaceTestEqual(t, "after revision", h.AfterRevision, int64(i+1))
		if h.OccurredAt.UnixNano() <= 0 {
			t.Fatalf("revision/time=%+v", h)
		}
		if seen[h.ID] {
			t.Fatal("duplicate history UUID")
		}
		seen[h.ID] = true
		if err := spaces.ValidateID(h.ID); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSpaceHistoryDuplicateCommandRollsBackRootAndMember(t *testing.T) {
	s, _ := spaceTestDB(t)
	ctx := context.Background()
	create := spaceTestCreate("alpha", spaces.Team, spaceTestSubject("a", "u"))
	v := spaceTestSpace(t, s, create)
	u := spaceTestSubject("b", "u")
	c := spaceTestChange(v, u, spaces.AddMember, spaces.Admin)
	c.CommandID = create.CommandID
	_, err := s.ChangeSpaceMember(ctx, c)
	spaceTestError(t, err, spaces.ErrConflict)
	_, err = s.GetSpaceMember(ctx, "alpha", v.ID, u)
	spaceTestError(t, err, spaces.ErrNotFound)
	got, err := s.GetSpace(ctx, "alpha", v.ID)
	if err != nil || !reflect.DeepEqual(v, got) {
		t.Fatalf("failed history changed root=%+v %v", got, err)
	}
	spaceTestRevision(t, s, v, 1)
}
