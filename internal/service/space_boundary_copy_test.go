package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

func TestSpaceBoundaryCopyAdmitsBothSidesBeforeOpen(t *testing.T) {
	f := newSpaceFixture(t)
	space := createBoundarySpace(t, f.raw, "alpha")
	legacy := NewFileService(f.store, f.repo, nil)
	boundaryPut(t, f.svc, "alpha", "plain", "source")
	boundaryPut(t, legacy, "alpha", space.BackingBucket, "source")
	for _, pair := range [][2]string{{"plain", space.BackingBucket}, {space.BackingBucket, "plain"}} {
		before := *f.effects
		_, err := f.svc.CopyObject(t.Context(), "alpha", pair[0], "source", "", pair[1], "copy", ReadOptions{}, PutOptions{}, false, false)
		if !errors.Is(err, access.ErrSpaceBound) {
			t.Fatalf("copy %v: %v", pair, err)
		}
		if *f.effects != before {
			t.Fatalf("copy opened/wrote before two-sided admission: %+v -> %+v", before, *f.effects)
		}
	}
	copied, err := f.svc.CopyObject(t.Context(), "alpha", "plain", "source", "", "target", "copy", ReadOptions{}, PutOptions{}, false, false)
	if err != nil || copied.Size != 7 || copied.Bucket != "target" {
		t.Fatalf("unbound copy control: %+v %v", copied, err)
	}
}

func TestSpaceBoundaryPartCopyBothScopes(t *testing.T) {
	f := newSpaceFixture(t)
	space := createBoundarySpace(t, f.raw, "alpha")
	legacy := NewFileService(f.store, f.repo, nil)
	boundaryPut(t, f.svc, "alpha", "plain", "source")
	boundaryPut(t, legacy, "alpha", space.BackingBucket, "source")
	plain, err := f.svc.InitMultipart(t.Context(), "alpha", "plain", "dest", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := legacy.InitMultipart(t.Context(), "alpha", space.BackingBucket, "dest", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct {
		upload repository.Upload
		source string
	}{{plain, space.BackingBucket}, {bound, "plain"}} {
		before := *f.effects
		_, err := f.svc.UploadPartCopyFor(t.Context(), MultipartScope{TenantID: "alpha", Bucket: pair.upload.Bucket, Key: pair.upload.Key}, pair.upload.ID, 1, pair.source, "source", "", -1, 0, ReadOptions{}, ReadOptions{})
		if !errors.Is(err, access.ErrSpaceBound) {
			t.Fatalf("part copy: %v", err)
		}
		if *f.effects != before {
			t.Fatal("refused part copy touched source/storage")
		}
		if parts, err := f.raw.ListParts(t.Context(), pair.upload.ID); err != nil || len(parts) != 0 {
			t.Fatalf("refused copy recorded parts: %v %v", parts, err)
		}
	}
	if _, err := f.svc.UploadPartCopyFor(t.Context(), MultipartScope{TenantID: "alpha"}, plain.ID, 1, "plain", "source", "", -1, 0, ReadOptions{}, ReadOptions{}); err != nil {
		t.Fatalf("unbound part copy control: %v", err)
	}
}

func installSpaceEnsureRace(t *testing.T, f *spaceFixture) string {
	t.Helper()
	// Observe the actual generated bucket in another tenant, then create the
	// same Space identity in alpha during ensure. No name is absence evidence.
	pattern := createBoundarySpace(t, f.raw, "template")
	if err := f.raw.UpsertTenant(t.Context(), repository.TenantRecord{TenantID: "alpha", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	f.repo.ensure = func() {
		f.repo.ensure = nil
		space, err := f.raw.(spaces.Store).CreateSpace(t.Context(), spaces.CreateCommand{
			TenantID: "alpha", ID: pattern.ID, Kind: spaces.Team, Name: "racing space",
			Actor: spaces.SubjectRef{Source: "test", Kind: spaces.User, ID: "owner"}, CommandID: repository.NewVersionID(),
		})
		if err != nil || space.BackingBucket != pattern.BackingBucket {
			t.Fatalf("Space/ensure interleaving: %+v %v", space, err)
		}
	}
	return pattern.BackingBucket
}

func TestSpaceBoundaryEnsureBucketRecheck(t *testing.T) {
	for _, operation := range []string{"put", "multipart init"} {
		t.Run(operation, func(t *testing.T) {
			f := newSpaceFixture(t)
			bucket := installSpaceEnsureRace(t, f)
			before := *f.effects
			var err error
			if operation == "put" {
				_, err = f.svc.Put(t.Context(), "alpha", bucket, "not-written", strings.NewReader("x"), 1, PutOptions{})
			} else {
				_, err = f.svc.InitMultipart(t.Context(), "alpha", bucket, "not-written", PutOptions{})
			}
			if !errors.Is(err, access.ErrSpaceBound) {
				t.Fatalf("binding created during ensure: %v", err)
			}
			if *f.effects != before {
				t.Fatalf("post-ensure admission was too late: %+v -> %+v", before, *f.effects)
			}
			if _, err := f.raw.GetObject(t.Context(), "alpha", bucket, "not-written"); !errors.Is(err, repository.ErrNotFound) {
				t.Fatalf("object committed: %v", err)
			}
		})
	}
}
