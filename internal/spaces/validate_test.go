package spaces

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestSpaceValidateCreate(t *testing.T) {
	valid := CreateCommand{TenantID: "tenant", ID: uuid.NewString(), CommandID: uuid.NewString(), Kind: Personal, Name: " 空间 ", Actor: SubjectRef{Source: "oidc:a", Kind: User, ID: "opaque-id"}}
	if err := ValidateCreate(valid); err != nil {
		t.Fatal(err)
	}
	cases := []func(*CreateCommand){
		func(c *CreateCommand) { c.TenantID = "" }, func(c *CreateCommand) { c.TenantID = "*" },
		func(c *CreateCommand) { c.TenantID = " tenant" }, func(c *CreateCommand) { c.ID = uuid.Nil.String() },
		func(c *CreateCommand) { c.ID = strings.ToUpper("11111111-1111-4111-8111-11111111111a") }, func(c *CreateCommand) { c.CommandID = "bad" },
		func(c *CreateCommand) { c.Kind = "unknown" }, func(c *CreateCommand) { c.Name = " \t" },
		func(c *CreateCommand) { c.Name = strings.Repeat("界", 201) }, func(c *CreateCommand) { c.Actor.Source = "" },
		func(c *CreateCommand) { c.Actor.Kind = Service }, func(c *CreateCommand) { c.Actor.ID = "\xff" },
		func(c *CreateCommand) { c.Actor.ID = "a\x00b" }, func(c *CreateCommand) { c.Actor.Kind = "employee" },
	}
	for i, mutate := range cases {
		c := valid
		mutate(&c)
		if err := ValidateCreate(c); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("case %d: %v", i, err)
		}
	}
	valid.Name = strings.Repeat("界", 200)
	if err := ValidateCreate(valid); err != nil {
		t.Fatal(err)
	}
}

func TestSpaceValidateMember(t *testing.T) {
	u := SubjectRef{Source: "a", Kind: User, ID: "id"}
	c := MemberCommand{TenantID: "t", SpaceID: uuid.NewString(), CommandID: uuid.NewString(), ExpectedRevision: 1, Actor: u, Target: u, Action: AddMember, Role: Viewer}
	if err := ValidateMemberCommand(c); err != nil {
		t.Fatal(err)
	}
	for _, revision := range []int64{-1, 0, math.MaxInt64} {
		bad := c
		bad.ExpectedRevision = revision
		if err := ValidateMemberCommand(bad); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("revision %d: %v", revision, err)
		}
	}
	for _, change := range []func(*MemberCommand){
		func(c *MemberCommand) { c.Action = "delete" }, func(c *MemberCommand) { c.Role = "owner" },
		func(c *MemberCommand) { c.Target.Source = " " }, func(c *MemberCommand) { c.Actor.Kind = "" },
		func(c *MemberCommand) { c.Action = RevokeMember },
	} {
		bad := c
		change(&bad)
		if err := ValidateMemberCommand(bad); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("bad command: %+v %v", bad, err)
		}
	}
	c.ExpectedRevision = math.MaxInt64 - 1
	if err := ValidateMemberCommand(c); err != nil {
		t.Fatal(err)
	}
}

func TestSpaceValidatePages(t *testing.T) {
	for _, limit := range []int{0, -1, 1001} {
		if err := ValidateMemberPage(MemberPage{Limit: limit}); !errors.Is(err, ErrInvalidArgument) {
			t.Error(err)
		}
	}
	if err := ValidateMemberPage(MemberPage{Limit: 1, After: &SubjectRef{ID: "id"}}); !errors.Is(err, ErrInvalidArgument) {
		t.Error(err)
	}
	if err := ValidateSpacePage(SpacePage{Limit: 1, AfterID: "bad"}); !errors.Is(err, ErrInvalidArgument) {
		t.Error(err)
	}
	if err := ValidateHistoryPage(HistoryPage{Limit: 1, AfterRevision: -1}); !errors.Is(err, ErrInvalidArgument) {
		t.Error(err)
	}
	if err := ValidateHistoryPage(HistoryPage{Limit: 1000}); err != nil {
		t.Fatal(err)
	}
}
