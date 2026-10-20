package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/aero-vault/aero-vault/internal/spaces"
)

type spaceRaceResult struct {
	index int
	err   error
}

func spaceTestRace(ops [2]func() error) [2]spaceRaceResult {
	start := make(chan struct{})
	ready := make(chan struct{}, 2)
	done := make(chan spaceRaceResult, 2)
	for i, op := range ops {
		go func() {
			ready <- struct{}{}
			<-start
			done <- spaceRaceResult{index: i, err: op()}
		}()
	}
	<-ready
	<-ready
	close(start)
	return [2]spaceRaceResult{<-done, <-done}
}

func TestSpaceConcurrentPersonalAcrossOpen(t *testing.T) {
	s, dsn := spaceTestDB(t)
	other := spaceTestOpen(t, dsn)
	if s.db.DB == other.db.DB {
		t.Fatal("not independent pools")
	}
	a := spaceTestSubject("source:a", "same")
	cmd := [2]spaces.CreateCommand{spaceTestCreate("alpha", spaces.Personal, a), spaceTestCreate("alpha", spaces.Personal, a)}
	stores := [2]*sqlStore{s, other}
	result := spaceTestRace([2]func() error{
		func() error { _, err := stores[0].CreateSpace(context.Background(), cmd[0]); return err },
		func() error { _, err := stores[1].CreateSpace(context.Background(), cmd[1]); return err },
	})
	wins := 0
	winner := ""
	for _, r := range result {
		if r.err == nil {
			wins++
			winner = cmd[r.index].ID
			continue
		}
		if !errors.Is(r.err, spaces.ErrConflict) && !errors.Is(r.err, spaces.ErrConcurrent) {
			t.Fatal(r.err)
		}
	}
	if wins != 1 {
		t.Fatalf("winners=%d result=%+v", wins, result)
	}
	for _, store := range stores {
		got, err := store.GetPersonalSpace(context.Background(), "alpha", a)
		spaceTestOK(t, err)
		spaceTestEqual(t, "recovered winner", got.ID, winner)
	}
	for _, table := range []string{"spaces", "space_members", "space_history", "buckets"} {
		spaceTestCount(t, other, table, 1)
	}
}

func TestSpaceConcurrentAdminsAcrossOpen(t *testing.T) {
	for _, actions := range [][2]spaces.MemberAction{
		{spaces.RevokeMember, spaces.RevokeMember},
		{spaces.ChangeRole, spaces.ChangeRole},
		{spaces.RevokeMember, spaces.ChangeRole},
	} {
		t.Run(string(actions[0])+"/"+string(actions[1]), func(t *testing.T) {
			s, dsn := spaceTestDB(t)
			other := spaceTestOpen(t, dsn)
			v := spaceTestSpace(t, s, spaceTestCreate("alpha", spaces.Team, spaceTestSubject("a", "one")))
			two := spaceTestSubject("b", "two")
			spaceTestApply(t, s, &v, spaceTestChange(v, two, spaces.AddMember, spaces.Admin))
			cmd := [2]spaces.MemberCommand{spaceTestChange(v, two, actions[0], ""), spaceTestChange(v, v.CreatedBy, actions[1], "")}
			cmd[1].Actor = two
			for i := range cmd {
				if cmd[i].Action == spaces.ChangeRole {
					cmd[i].Role = spaces.Editor
				}
			}
			spaceTestAdminRace(t, [2]*sqlStore{s, other}, v, cmd)
		})
	}
}

func spaceTestAdminRace(t *testing.T, stores [2]*sqlStore, v spaces.Space, cmd [2]spaces.MemberCommand) {
	t.Helper()
	ctx := context.Background()
	r := spaceTestRace([2]func() error{
		func() error { _, err := stores[0].ChangeSpaceMember(ctx, cmd[0]); return err },
		func() error { _, err := stores[1].ChangeSpaceMember(ctx, cmd[1]); return err },
	})
	wins, loser := 0, -1
	for _, item := range r {
		if item.err == nil {
			wins++
			continue
		}
		loser = item.index
		if !errors.Is(item.err, spaces.ErrConflict) && !errors.Is(item.err, spaces.ErrConcurrent) {
			t.Fatal(item.err)
		}
	}
	if wins != 1 || loser < 0 {
		t.Fatalf("result=%+v", r)
	}
	spaceTestRevision(t, stores[1], v, 3)
	cmd[loser].ExpectedRevision = 3
	_, err := stores[loser].ChangeSpaceMember(ctx, cmd[loser])
	spaceTestError(t, err, spaces.ErrLastAdmin)
	spaceTestRevision(t, stores[0], v, 3)
	var admins int
	err = stores[1].db.QueryRowContext(ctx, `SELECT COUNT(*) FROM space_members WHERE state='active' AND role='admin' AND subject_kind='user'`).Scan(&admins)
	if err != nil || admins != 1 {
		t.Fatalf("admins=%d %v", admins, err)
	}
}
