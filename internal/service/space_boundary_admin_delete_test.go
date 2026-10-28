package service

import (
	"context"
	"errors"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

type adminBoundaryLookup struct {
	repository.Repository
	gets int
}

func (r *adminBoundaryLookup) GetObject(ctx context.Context, tenant, bucket, key string) (repository.Object, error) {
	r.gets++
	return r.Repository.GetObject(ctx, tenant, bucket, key)
}

func TestSpaceBoundaryAdminMissingAdmittedBeforeLookup(t *testing.T) {
	f := newSpaceFixture(t)
	space := createBoundarySpace(t, f.raw, "alpha")
	lookup := &adminBoundaryLookup{Repository: f.repo}
	f.svc.repo = lookup
	provider := &spaceActionProvider{action: access.ActionAdminDelete}
	f.svc.WithAuthorizer(provider)
	if err := f.svc.AdminDelete(t.Context(), "alpha", space.BackingBucket, "missing", true); !errors.Is(err, access.ErrSpaceBound) {
		t.Fatalf("bound missing: %v", err)
	}
	if lookup.gets != 0 || provider.calls != 0 {
		t.Fatalf("bound lookup/provider invoked: %d %d", lookup.gets, provider.calls)
	}
	if err := f.svc.AdminDelete(t.Context(), "alpha", "plain", "missing", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unbound missing: %v", err)
	}
	if lookup.gets != 1 || provider.calls != 0 {
		t.Fatal("unbound 404 control changed")
	}
	cause := errors.Join(repository.ErrNotFound, access.ErrDenied)
	readers := DiscoverSpaceBoundaryReaders(f.raw)
	readers.Binding = spaceBindingFunc(func(context.Context, string, string) (spaces.BindingResult, error) {
		return spaces.BindingResult{}, cause
	})
	f.svc.WithSpaceBoundary(NewSpaceBoundary(readers))
	if err := f.svc.AdminDelete(t.Context(), "alpha", "plain", "missing", false); !access.IsResourceBoundaryFault(err) || !errors.Is(err, cause) {
		t.Fatalf("fault became 404/denial: %v", err)
	}
	if lookup.gets != 1 {
		t.Fatal("fault performed lookup")
	}
}

type forgedAdminBoundaryLookup struct {
	repository.Repository
	object repository.Object
}

func (r forgedAdminBoundaryLookup) GetObject(context.Context, string, string, string) (repository.Object, error) {
	return r.object, nil
}

func TestSpaceBoundaryAdminValidatesActualLookupScope(t *testing.T) {
	f := newSpaceFixture(t)
	obj := boundaryPut(t, f.svc, "alpha", "docs", "file")
	provider := &spaceActionProvider{action: access.ActionAdminDelete}
	f.svc.WithAuthorizer(provider)
	before := *f.effects
	for _, mutate := range []func(*repository.Object){
		func(o *repository.Object) { o.TenantID = "other" },
		func(o *repository.Object) { o.Bucket = "other" },
		func(o *repository.Object) { o.Key = "other" },
		func(o *repository.Object) { o.ID = 0 },
	} {
		bad := obj
		mutate(&bad)
		f.svc.repo = forgedAdminBoundaryLookup{Repository: f.raw, object: bad}
		if err := f.svc.AdminDelete(t.Context(), "alpha", "docs", "file", true); !access.IsResourceBoundaryFault(err) {
			t.Fatalf("forged admin row accepted: %v", err)
		}
	}
	if provider.calls != 0 || *f.effects != before {
		t.Fatal("bad lookup reached provider or effects")
	}
}

func TestSpaceBoundaryAdminRetainsIndependentProvider(t *testing.T) {
	f := newSpaceFixture(t)
	obj := boundaryPut(t, f.svc, "alpha", "docs", "file")
	before := *f.effects
	if err := f.svc.AdminDelete(t.Context(), "alpha", "docs", obj.Key, true); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unbound admission granted admin deletion: %v", err)
	}
	failure := errors.New("provider failed")
	provider := &spaceActionProvider{action: access.ActionAdminDelete, err: failure}
	f.svc.WithAuthorizer(provider)
	if err := f.svc.AdminDelete(t.Context(), "alpha", "docs", obj.Key, true); !errors.Is(err, failure) || !access.IsResourceBoundaryFault(err) {
		t.Fatalf("provider failure: %v", err)
	}
	if *f.effects != before {
		t.Fatal("missing/failed provider performed effects")
	}
	provider.err = nil
	if err := f.svc.AdminDelete(t.Context(), "alpha", "docs", obj.Key, true); err != nil {
		t.Fatalf("explicit admin action: %v", err)
	}
	if _, err := f.raw.GetObject(t.Context(), "alpha", "docs", obj.Key); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("admin control did not delete: %v", err)
	}
}
