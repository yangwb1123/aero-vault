package repository

import (
	"context"
	"testing"

	"github.com/aero-vault/aero-vault/internal/spaces"
	"github.com/google/uuid"
)

func TestSpaceMemberTransitionsAndFailedCAS(t *testing.T) {
	s, _ := spaceTestDB(t)
	ctx := context.Background()
	v := spaceTestSpace(t, s, spaceTestCreate("alpha", spaces.Personal, spaceTestSubject("a", "owner")))
	u := spaceTestSubject("b", "owner")
	add := spaceTestChange(v, u, spaces.AddMember, spaces.Editor)
	spaceTestApply(t, s, &v, add)
	initial, err := s.GetSpaceMember(ctx, "alpha", v.ID, u)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ChangeSpaceMember(ctx, add)
	spaceTestError(t, err, spaces.ErrConflict)
	_, err = s.ChangeSpaceMember(ctx, spaceTestChange(v, u, spaces.AddMember, spaces.Viewer))
	spaceTestError(t, err, spaces.ErrMemberExists)
	_, err = s.ChangeSpaceMember(ctx, spaceTestChange(v, u, spaces.ChangeRole, spaces.Editor))
	spaceTestError(t, err, spaces.ErrConflict)
	spaceTestRevision(t, s, v, 2)
	spaceTestApply(t, s, &v, spaceTestChange(v, u, spaces.ChangeRole, spaces.Admin))
	spaceTestApply(t, s, &v, spaceTestChange(v, v.CreatedBy, spaces.RevokeMember, ""))
	personal, err := s.GetPersonalSpace(ctx, "alpha", v.CreatedBy)
	if err != nil || personal.ID != v.ID {
		t.Fatalf("personal binding lost: %+v %v", personal, err)
	}
	_, err = s.CreateSpace(ctx, spaceTestCreate("alpha", spaces.Personal, v.CreatedBy))
	spaceTestError(t, err, spaces.ErrConflict) // Revoking a member never releases the original binding.
	spaceTestApply(t, s, &v, spaceTestChange(v, v.CreatedBy, spaces.ReactivateMember, spaces.Viewer))
	spaceTestApply(t, s, &v, spaceTestChange(v, v.CreatedBy, spaces.RevokeMember, ""))
	for _, action := range []spaces.MemberAction{spaces.AddMember, spaces.RevokeMember, spaces.ChangeRole} {
		role := spaces.Viewer
		if action == spaces.RevokeMember {
			role = ""
		}
		_, err = s.ChangeSpaceMember(ctx, spaceTestChange(v, v.CreatedBy, action, role))
		spaceTestError(t, err, spaces.ErrMemberRevoked)
	}
	current, err := s.GetSpaceMember(ctx, "alpha", v.ID, u)
	if err != nil || !current.JoinedAt.Equal(initial.JoinedAt) || current.ChangedRevision != 3 {
		t.Fatalf("member facts=%+v err=%v", current, err)
	}
	spaceTestRevision(t, s, v, 6)
}

func TestSpaceMemberLastUserAdmin(t *testing.T) {
	s, _ := spaceTestDB(t)
	ctx := context.Background()
	v := spaceTestSpace(t, s, spaceTestCreate("alpha", spaces.Team, spaceTestSubject("a", "owner")))
	service := spaces.SubjectRef{Source: "a", Kind: spaces.Service, ID: "owner"}
	spaceTestApply(t, s, &v, spaceTestChange(v, service, spaces.AddMember, spaces.Admin))
	for _, c := range []spaces.MemberCommand{
		spaceTestChange(v, v.CreatedBy, spaces.RevokeMember, ""),
		spaceTestChange(v, v.CreatedBy, spaces.ChangeRole, spaces.Editor),
	} {
		_, err := s.ChangeSpaceMember(ctx, c)
		spaceTestError(t, err, spaces.ErrLastAdmin)
		spaceTestRevision(t, s, v, 2)
	}
	other := spaceTestSubject("b", "owner")
	spaceTestApply(t, s, &v, spaceTestChange(v, other, spaces.AddMember, spaces.Admin))
	spaceTestApply(t, s, &v, spaceTestChange(v, other, spaces.RevokeMember, ""))
	_, err := s.ChangeSpaceMember(ctx, spaceTestChange(v, v.CreatedBy, spaces.RevokeMember, ""))
	spaceTestError(t, err, spaces.ErrLastAdmin)
	spaceTestRevision(t, s, v, 4)
}

func TestSpaceMemberRevocationTimeAndReactivation(t *testing.T) {
	s, _ := spaceTestDB(t)
	ctx := context.Background()
	v := spaceTestSpace(t, s, spaceTestCreate("alpha", spaces.Team, spaceTestSubject("a", "owner")))
	u := spaceTestSubject("b", "u")
	spaceTestApply(t, s, &v, spaceTestChange(v, u, spaces.AddMember, spaces.Editor))
	joined, err := s.GetSpaceMember(ctx, "alpha", v.ID, u)
	spaceTestOK(t, err)
	spaceTestApply(t, s, &v, spaceTestChange(v, u, spaces.RevokeMember, ""))
	revoked, err := s.GetSpaceMember(ctx, "alpha", v.ID, u)
	spaceTestOK(t, err)
	if revoked.RevokedAt == nil {
		t.Fatal("revocation time missing")
	}
	spaceTestEqual(t, "revocation time", *revoked.RevokedAt, revoked.UpdatedAt)
	spaceTestEqual(t, "revoked state", revoked.State, spaces.Revoked)
	var ns int64
	var kind string
	spaceTestOK(t, s.db.QueryRowContext(ctx, `SELECT revoked_at_ns,typeof(revoked_at_ns) FROM space_members WHERE state='revoked'`).Scan(&ns, &kind))
	spaceTestEqual(t, "revoked timestamp storage type", kind, "integer")
	spaceTestEqual(t, "revoked timestamp nanoseconds", ns, revoked.RevokedAt.UnixNano())
	spaceTestApply(t, s, &v, spaceTestChange(v, u, spaces.ReactivateMember, spaces.Viewer))
	reactivated, err := s.GetSpaceMember(ctx, "alpha", v.ID, u)
	spaceTestOK(t, err)
	spaceTestEqual(t, "first joined time retained", reactivated.JoinedAt, joined.JoinedAt)
	spaceTestEqual(t, "reactivated state", reactivated.State, spaces.Active)
	spaceTestEqual(t, "reactivated role", reactivated.Role, spaces.Viewer)
	if reactivated.RevokedAt != nil {
		t.Fatal("reactivation retained revoked timestamp")
	}
	spaceTestRevision(t, s, v, 4)
}

func TestSpaceMemberUnknownAndStateErrors(t *testing.T) {
	s, _ := spaceTestDB(t)
	ctx := context.Background()
	v := spaceTestSpace(t, s, spaceTestCreate("alpha", spaces.Team, spaceTestSubject("a", "owner")))
	u := spaceTestSubject("a", "missing")
	for _, action := range []spaces.MemberAction{spaces.ChangeRole, spaces.RevokeMember, spaces.ReactivateMember} {
		role := spaces.Viewer
		if action == spaces.RevokeMember {
			role = ""
		}
		_, err := s.ChangeSpaceMember(ctx, spaceTestChange(v, u, action, role))
		spaceTestError(t, err, spaces.ErrNotFound)
	}
	_, err := s.ChangeSpaceMember(ctx, spaceTestChange(v, v.CreatedBy, spaces.ReactivateMember, spaces.Admin))
	spaceTestError(t, err, spaces.ErrMemberExists)
	c := spaceTestChange(v, u, spaces.AddMember, spaces.Viewer)
	c.SpaceID = uuid.NewString()
	_, err = s.ChangeSpaceMember(ctx, c)
	spaceTestError(t, err, spaces.ErrNotFound)
	spaceTestRevision(t, s, v, 1)
}
