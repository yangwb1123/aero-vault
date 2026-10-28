package service

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

func TestSpaceBoundaryEmptyListsAndBucketNames(t *testing.T) {
	f := newSpaceFixture(t)
	space := createBoundarySpace(t, f.raw, "alpha")
	if err := f.svc.CreateBucket(t.Context(), "alpha", "visible"); err != nil {
		t.Fatal(err)
	}
	buckets, err := f.svc.ListBuckets(t.Context(), "alpha")
	if err != nil || !reflect.DeepEqual(buckets, []string{"visible"}) {
		t.Fatalf("bucket names: %v %v", buckets, err)
	}
	for name, operation := range map[string]func() error{
		"objects": func() error { _, err := f.svc.List(t.Context(), "alpha", space.BackingBucket, "", "", 0); return err },
		"deleted": func() error {
			_, err := f.svc.ListDeleted(t.Context(), "alpha", space.BackingBucket, "", "", 0)
			return err
		},
		"versions": func() error {
			_, err := f.svc.ListVersions(t.Context(), "alpha", space.BackingBucket, "missing")
			return err
		},
		"version page": func() error {
			_, err := f.svc.ListObjectVersionsWithOpts(t.Context(), "alpha", space.BackingBucket, "missing", repository.VersionListOpts{})
			return err
		},
		"uploads": func() error {
			_, err := f.svc.ListMultipartUploads(t.Context(), "alpha", space.BackingBucket, "", "", 0)
			return err
		},
		"upload bool": func() error {
			_, err := f.svc.BucketHasMultipartUploads(t.Context(), "alpha", space.BackingBucket)
			return err
		},
	} {
		if err := operation(); !errors.Is(err, access.ErrSpaceBound) {
			t.Errorf("%s empty success: %v", name, err)
		}
	}
	if page, err := f.svc.List(t.Context(), "alpha", "visible", "", "", 0); err != nil || len(page.Objects) != 0 {
		t.Fatalf("unbound empty control: %+v %v", page, err)
	}
}

func TestSpaceBoundaryDisabledTenantBucketNamesNeverEmptySuccess(t *testing.T) {
	f := newSpaceFixture(t)
	boundaryPut(t, f.svc, "alpha", "docs", "a")
	if err := f.raw.UpsertTenant(t.Context(), repository.TenantRecord{TenantID: "alpha", Status: "disabled"}); err != nil {
		t.Fatal(err)
	}
	before := *f.effects
	buckets, err := f.svc.ListBuckets(access.SystemContext(t.Context(), "alpha"), "alpha")
	if !errors.Is(err, ErrTenantDisabled) || buckets != nil {
		t.Fatalf("disabled tenant became healthy filtered names: %v %v", buckets, err)
	}
	if *f.effects != before {
		t.Fatal("disabled names performed effects")
	}
}

type spaceBucketNamesHook struct {
	repository.Repository
	after func()
}

func (r spaceBucketNamesHook) ListBuckets(ctx context.Context, tenant string) ([]string, error) {
	names, err := r.Repository.ListBuckets(ctx, tenant)
	r.after()
	return names, err
}

func TestSpaceBoundaryBucketNamesRechecksTenantAfterFiltering(t *testing.T) {
	f := newSpaceFixture(t)
	createBoundarySpace(t, f.raw, "alpha")
	f.svc.repo = spaceBucketNamesHook{Repository: f.raw, after: func() {
		if err := f.raw.UpsertTenant(t.Context(), repository.TenantRecord{TenantID: "alpha", Status: "disabled"}); err != nil {
			t.Fatal(err)
		}
	}}
	buckets, err := f.svc.ListBuckets(access.SystemContext(t.Context(), "alpha"), "alpha")
	if !errors.Is(err, ErrTenantDisabled) || buckets != nil {
		t.Fatalf("suspension during name filtering became empty success: %v %v", buckets, err)
	}
}

type spaceActionProvider struct {
	action access.Action
	err    error
	calls  int
}

func (p *spaceActionProvider) Authorize(_ context.Context, _ access.Principal, action access.Action, _ access.Resource) (access.Decision, error) {
	p.calls++
	return access.Decision{Allowed: action == p.action}, p.err
}

func TestSpaceBoundaryListingFaultsAreNotFiltered(t *testing.T) {
	f := newSpaceFixture(t)
	obj := boundaryPut(t, f.svc, "alpha", "docs", "a")
	if _, err := f.svc.InitMultipart(t.Context(), "alpha", "docs", "upload", PutOptions{}); err != nil {
		t.Fatal(err)
	}
	cause := errors.Join(ErrForbidden, repository.ErrNotFound, errors.New("provider offline"))
	provider := &spaceActionProvider{err: cause}
	f.svc.WithAuthorizer(provider)
	for name, operation := range map[string]func() error{
		"objects":  func() error { _, err := f.svc.List(t.Context(), "alpha", "docs", "", "", 1); return err },
		"versions": func() error { _, err := f.svc.ListVersions(t.Context(), "alpha", "docs", obj.Key); return err },
		"uploads": func() error {
			_, err := f.svc.ListMultipartUploads(t.Context(), "alpha", "docs", "", "", 1)
			return err
		},
		"buckets": func() error { _, err := f.svc.ListBuckets(t.Context(), "alpha"); return err },
	} {
		if err := operation(); !access.IsResourceBoundaryFault(err) || !errors.Is(err, cause) {
			t.Errorf("%s swallowed fault: %v", name, err)
		}
	}
	result := f.svc.BatchDelete(t.Context(), "alpha", "docs", []string{obj.Key})
	if len(result) != 1 || result[0].Deleted || result[0].Error != access.ErrResourceBoundaryFault.Error() {
		t.Fatalf("fault became successful or unredacted batch delete: %+v", result)
	}
	tags := f.svc.BatchSetTags(t.Context(), "alpha", "docs", []string{obj.Key}, map[string]string{"x": "y"})
	if len(tags) != 1 || tags[0].Error != access.ErrResourceBoundaryFault.Error() {
		t.Fatalf("batch tag fault was swallowed or unredacted: %+v", tags)
	}
}

type spaceQuotaHook struct {
	repository.Repository
	after func()
}

func (r spaceQuotaHook) GetTenantQuota(ctx context.Context, tenant string) (repository.TenantQuota, error) {
	quota, err := r.Repository.GetTenantQuota(ctx, tenant)
	if r.after != nil {
		r.after()
	}
	return quota, err
}

func TestSpaceBoundaryUsageFreshP0DataP1(t *testing.T) {
	f := newSpaceFixture(t)
	calls := 0
	readers := DiscoverSpaceBoundaryReaders(f.raw)
	coverage := readers.Coverage
	readers.Coverage = spaceCoverageFunc(func(ctx context.Context, scope spaces.CoverageScope) (spaces.CoverageResult, error) {
		calls++
		return coverage.ProbeSpaceCoverage(ctx, scope)
	})
	f.svc.WithSpaceBoundary(NewSpaceBoundary(readers))
	if _, err := f.svc.Usage(t.Context(), "alpha"); err != nil || calls != 2 {
		t.Fatalf("empty usage P0/D/P1: calls=%d err=%v", calls, err)
	}
	f.svc.repo = spaceQuotaHook{Repository: f.raw, after: func() { createBoundarySpace(t, f.raw, "alpha") }}
	calls = 0
	if _, err := f.svc.Usage(t.Context(), "alpha"); !errors.Is(err, access.ErrSpaceBound) || calls != 2 {
		t.Fatalf("binding inserted during D: calls=%d err=%v", calls, err)
	}
	calls = 0
	if _, err := f.svc.Usage(t.Context(), "alpha"); !errors.Is(err, access.ErrSpaceBound) || calls != 1 {
		t.Fatalf("bound P0 must stop before D: calls=%d err=%v", calls, err)
	}
}

func TestSpaceBoundaryCoverageRejectsPartialAndWrongEcho(t *testing.T) {
	f := newSpaceFixture(t)
	readers := DiscoverSpaceBoundaryReaders(f.raw)
	for _, scope := range []spaces.CoverageScope{{Kind: spaces.CoverageTenant, TenantID: "alpha"}, {Kind: spaces.CoverageGlobal}} {
		for _, observation := range []spaces.CoverageResult{
			{}, {Scope: scope, State: spaces.CoverageUnknown},
			{Scope: spaces.CoverageScope{Kind: spaces.CoverageTenant, TenantID: "other"}, State: spaces.CoverageUnbound, Complete: true},
		} {
			readers.Coverage = spaceCoverageFunc(func(context.Context, spaces.CoverageScope) (spaces.CoverageResult, error) { return observation, nil })
			if err := NewSpaceBoundary(readers).CheckCoverage(t.Context(), scope); !access.IsResourceBoundaryFault(err) {
				t.Errorf("bad coverage accepted: %+v %v", observation, err)
			}
		}
		if err := NewSpaceBoundary(DiscoverSpaceBoundaryReaders(f.raw)).CheckCoverage(t.Context(), scope); err != nil {
			t.Fatalf("authoritative empty coverage: %v", err)
		}
	}
}

func TestSpaceBoundaryAnonymousAndAccessLogs(t *testing.T) {
	f := newSpaceFixture(t)
	space := createBoundarySpace(t, f.raw, "alpha")
	legacy := NewFileService(f.store, f.repo, nil)
	obj := boundaryPut(t, legacy, "alpha", space.BackingBucket, "public")
	if err := f.raw.SetObjectACL(t.Context(), "alpha", space.BackingBucket, obj.Key, ACLPublicRead); err != nil {
		t.Fatal(err)
	}
	if public, err := f.svc.ObjectPublicReadableWithError(t.Context(), "alpha", space.BackingBucket, obj.Key); public || !errors.Is(err, access.ErrSpaceBound) {
		t.Fatalf("anonymous bound: %v %v", public, err)
	}
	if f.svc.ObjectPublicReadable(t.Context(), "alpha", space.BackingBucket, obj.Key) {
		t.Fatal("bool API granted on refusal")
	}
	if err := f.raw.CreateBucket(t.Context(), "alpha", "logs"); err != nil {
		t.Fatal(err)
	}
	if err := f.raw.SetBucketLogging(t.Context(), "alpha", space.BackingBucket, "logs", ""); err != nil {
		t.Fatal(err)
	}
	before := *f.effects
	entry := AccessLogEntry{Status: 403, Method: "GET", Key: "secret"}
	if err := f.svc.RecordBucketAccessLog(t.Context(), "alpha", space.BackingBucket, entry); !errors.Is(err, access.ErrSpaceBound) {
		t.Fatalf("rejected source logged: %v", err)
	}
	if err := f.raw.SetBucketLogging(t.Context(), "alpha", "logs", space.BackingBucket, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RecordBucketAccessLog(t.Context(), "alpha", "logs", entry); !errors.Is(err, access.ErrSpaceBound) {
		t.Fatalf("bound destination logged: %v", err)
	}
	if *f.effects != before {
		t.Fatalf("access log effects on refusal: %+v -> %+v", before, *f.effects)
	}
}
