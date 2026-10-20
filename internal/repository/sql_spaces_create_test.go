package repository

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/spaces"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/google/uuid"
)

func TestSpaceCreateAtomicFacts(t *testing.T) {
	s, _ := spaceTestDB(t)
	ctx := context.Background()
	c := spaceTestCreate("alpha", spaces.Personal, spaceTestSubject("oidc:a", "same"))
	v := spaceTestSpace(t, s, c)
	spaceTestEqual(t, "root ID", v.ID, c.ID)
	spaceTestEqual(t, "root tenant", v.TenantID, c.TenantID)
	spaceTestEqual(t, "initial revision", v.Revision, int64(1))
	spaceTestEqual(t, "derived bucket", v.BackingBucket, "spc-"+c.ID)
	spaceTestEqual(t, "personal subject", v.PersonalSubject, &c.Actor)
	spaceTestEqual(t, "creator", v.CreatedBy, c.Actor)
	spaceTestEqual(t, "updater", v.UpdatedBy, c.Actor)
	if v.CreatedAt.UnixNano() <= 0 || !v.CreatedAt.Equal(v.UpdatedAt) {
		t.Fatalf("bad times: %+v", v)
	}
	m, err := s.GetSpaceMember(ctx, "alpha", v.ID, c.Actor)
	spaceTestOK(t, err)
	spaceTestEqual(t, "initial member", m, spaces.Member{TenantID: "alpha", SpaceID: v.ID, Subject: c.Actor,
		Role: spaces.Admin, State: spaces.Active, ChangedRevision: 1, ChangedBy: c.Actor, JoinedAt: v.CreatedAt, UpdatedAt: v.CreatedAt})
	got, err := s.GetSpaceByBucket(ctx, "alpha", v.BackingBucket)
	if err != nil || !reflect.DeepEqual(v, got) {
		t.Fatalf("bucket lookup=%+v err=%v", got, err)
	}
	for _, table := range []string{"spaces", "space_members", "space_history", "buckets"} {
		spaceTestCount(t, s, table, 1)
	}
	spaceTestRevision(t, s, v, 1)
}

func TestSpaceCreatePersonalUniqueAndTeamNames(t *testing.T) {
	s, _ := spaceTestDB(t)
	a := spaceTestSubject("source:a", "same")
	c := spaceTestCreate("alpha", spaces.Personal, a)
	first := spaceTestSpace(t, s, c)
	_, err := s.CreateSpace(context.Background(), spaceTestCreate("alpha", spaces.Personal, a))
	spaceTestError(t, err, spaces.ErrConflict)
	for _, c := range []spaces.CreateCommand{
		spaceTestCreate("beta", spaces.Personal, a),
		spaceTestCreate("alpha", spaces.Personal, spaceTestSubject("source:b", "same")),
		spaceTestCreate("alpha", spaces.Team, a), spaceTestCreate("alpha", spaces.Team, a),
	} {
		spaceTestSpace(t, s, c)
	}
	got, err := s.GetPersonalSpace(context.Background(), "alpha", a)
	if err != nil || got.ID != first.ID {
		t.Fatalf("personal lookup=%+v err=%v", got, err)
	}
	for _, table := range []string{"spaces", "space_members", "space_history", "buckets"} {
		spaceTestCount(t, s, table, 5)
	}
}

func TestSpaceCreateNeverClaimsExistingBucket(t *testing.T) {
	for _, populated := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "populated"}[populated], func(t *testing.T) {
			s, _ := spaceTestDB(t)
			c := spaceTestCreate("alpha", spaces.Team, spaceTestSubject("a", "u"))
			bucket := "spc-" + c.ID
			if err := s.SetBucketQuota(context.Background(), "alpha", bucket, 123, 7); err != nil {
				t.Fatal(err)
			}
			checkObject := func() {}
			if populated {
				checkObject = spaceTestUnclaimedObject(t, s, bucket)
			}
			before, err := s.GetBucketConfig(context.Background(), "alpha", bucket)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.CreateSpace(context.Background(), c)
			spaceTestError(t, err, spaces.ErrConflict)
			after, err := s.GetBucketConfig(context.Background(), "alpha", bucket)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("bucket changed: %+v %v", after, err)
			}
			checkObject()
			spaceTestCount(t, s, "spaces", 0)
			spaceTestCount(t, s, "buckets", 1)
			spaceTestCount(t, s, "objects", map[bool]int{false: 0, true: 1}[populated])
		})
	}
}

func spaceTestUnclaimedObject(t *testing.T, s *sqlStore, bucket string) func() {
	t.Helper()
	ctx := context.Background()
	local, err := storage.NewLocal(storage.LocalConfig{Root: t.TempDir()})
	spaceTestOK(t, err)
	key, content := "alpha/"+bucket+"/old", "existing object bytes"
	info, err := local.Put(ctx, key, strings.NewReader(content), int64(len(content)), storage.PutOptions{})
	spaceTestOK(t, err)
	obj, err := s.UpsertObject(ctx, Object{TenantID: "alpha", Bucket: bucket, Key: "old", StorageKey: key,
		Backend: "local", ETag: info.ETag, Size: info.Size, Metadata: map[string]string{"old": "unchanged"}})
	spaceTestOK(t, err)
	before, err := s.GetObjectByID(ctx, obj.ID)
	spaceTestOK(t, err)
	return func() {
		t.Helper()
		after, err := s.GetObjectByID(ctx, obj.ID)
		spaceTestOK(t, err)
		spaceTestEqual(t, "unclaimed object metadata", after, before)
		r, _, err := local.Get(ctx, key)
		spaceTestOK(t, err)
		body, readErr := io.ReadAll(r)
		closeErr := r.Close()
		spaceTestOK(t, readErr)
		spaceTestOK(t, closeErr)
		spaceTestEqual(t, "unclaimed object bytes", string(body), content)
	}
}

func TestSpaceCreateDuplicateIDsAndCommand(t *testing.T) {
	s, _ := spaceTestDB(t)
	c := spaceTestCreate("alpha", spaces.Team, spaceTestSubject("a", "u"))
	spaceTestSpace(t, s, c)
	_, err := s.CreateSpace(context.Background(), c)
	spaceTestError(t, err, spaces.ErrConflict)
	c.ID = uuid.NewString()
	_, err = s.CreateSpace(context.Background(), c)
	spaceTestError(t, err, spaces.ErrConflict)
	for _, table := range []string{"spaces", "space_members", "space_history", "buckets"} {
		spaceTestCount(t, s, table, 1)
	}
}
