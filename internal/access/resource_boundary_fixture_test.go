package access_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

// These independent facts cover only the resource declared by the old ACL
// doubles. Their original lookup counters and ACL fault oracles are unchanged.
func (f *failingACLStore) ProbeSpaceBinding(ctx context.Context, tenant, bucket string) (spaces.BindingResult, error) {
	return aclFixtureBinding(ctx, tenant, bucket)
}

func (s *trackingACLStore) ProbeSpaceBinding(ctx context.Context, tenant, bucket string) (spaces.BindingResult, error) {
	return aclFixtureBinding(ctx, tenant, bucket)
}

func aclFixtureBinding(ctx context.Context, tenant, bucket string) (spaces.BindingResult, error) {
	if err := ctx.Err(); err != nil {
		return spaces.BindingResult{}, err
	}
	if tenant != "acme" || bucket != "default" {
		return spaces.BindingResult{}, fmt.Errorf("undeclared ACL fixture binding: %q/%q", tenant, bucket)
	}
	return spaces.BindingResult{TenantID: tenant, Bucket: bucket, State: spaces.BindingUnbound}, nil
}

const managerBoundarySpaceID = "12345678-1234-1234-1234-123456789abc"

type managerBoundaryStore struct {
	access.Store
	facts          map[[2]string]spaces.BindingResult
	bindingErr     error
	bindingFaults  map[[2]string]error
	bindingCalls   int
	coverage       spaces.CoverageResult
	coverageErr    error
	coverageCalls  int
	listCalls      int
	assetRows      []access.PublicAsset
	assetLookupErr error
	afterList      func()
}

func managerBoundaryFacts(store access.Store) *managerBoundaryStore {
	facts := make(map[[2]string]spaces.BindingResult)
	for _, bucket := range []string{"default", "destination", "spc-legacy"} {
		facts[[2]string{"acme", bucket}] = spaces.BindingResult{
			TenantID: "acme", Bucket: bucket, State: spaces.BindingUnbound,
		}
	}
	return &managerBoundaryStore{Store: store, facts: facts, coverage: spaces.CoverageResult{
		Scope: spaces.CoverageScope{Kind: spaces.CoverageTenant, TenantID: "acme"},
		State: spaces.CoverageUnbound, Complete: true,
	}}
}

func (s *managerBoundaryStore) ProbeSpaceBinding(ctx context.Context, tenant, bucket string) (spaces.BindingResult, error) {
	s.bindingCalls++
	if err := ctx.Err(); err != nil {
		return spaces.BindingResult{}, err
	}
	if s.bindingErr != nil {
		return spaces.BindingResult{}, s.bindingErr
	}
	if err := s.bindingFaults[[2]string{tenant, bucket}]; err != nil {
		return spaces.BindingResult{}, err
	}
	fact, ok := s.facts[[2]string{tenant, bucket}]
	if !ok {
		return spaces.BindingResult{}, fmt.Errorf("undeclared manager binding: %q/%q", tenant, bucket)
	}
	return fact, nil
}

func (s *managerBoundaryStore) ProbeSpaceCoverage(ctx context.Context, scope spaces.CoverageScope) (spaces.CoverageResult, error) {
	s.coverageCalls++
	if err := ctx.Err(); err != nil {
		return spaces.CoverageResult{}, err
	}
	if scope != s.coverage.Scope {
		return spaces.CoverageResult{}, fmt.Errorf("undeclared manager coverage: %+v", scope)
	}
	return s.coverage, s.coverageErr
}

func (s *managerBoundaryStore) ListPublicAssets(ctx context.Context, tenant string) ([]access.PublicAsset, error) {
	s.listCalls++
	assets := s.assetRows
	var err error
	if assets == nil {
		assets, err = s.Store.ListPublicAssets(ctx, tenant)
	}
	if s.afterList != nil {
		s.afterList()
	}
	return assets, err
}

func (s *managerBoundaryStore) GetPublicAsset(ctx context.Context, slug string) (access.PublicAsset, error) {
	if s.assetLookupErr != nil {
		return access.PublicAsset{}, s.assetLookupErr
	}
	return s.Store.GetPublicAsset(ctx, slug)
}

func (s *managerBoundaryStore) bind(bucket string) {
	fact := s.facts[[2]string{"acme", bucket}]
	fact.State, fact.SpaceID, fact.Revision = spaces.BindingBound, managerBoundarySpaceID, 1
	fact.TenantKnown, fact.TenantStatus = true, "active"
	s.facts[[2]string{"acme", bucket}] = fact
}

func (s *managerBoundaryStore) bindCoverage() {
	s.bind("default")
	s.coverage.State = spaces.CoverageBound
	s.coverage.TenantKnown, s.coverage.TenantStatus = true, "active"
	s.coverage.Witnesses = []spaces.BindingResult{s.facts[[2]string{"acme", "default"}]}
}

type managerBindingOnly struct {
	access.Store
	spaces.BindingReader
}

type managerCoverageOnly struct {
	access.Store
	spaces.CoverageReader
}

func boundaryManager(t *testing.T, store access.Store, cfg access.Config) *access.Manager {
	t.Helper()
	cfg.ShareSecret = []byte("0123456789abcdef0123456789abcdef")
	manager, err := access.NewManager(store, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

type managerCapabilityFixture struct {
	manager *access.Manager
	store   access.Store
	reader  *managerBoundaryStore
	ctx     context.Context
	share   access.Share
	token   string
	asset   access.PublicAsset
	acl     access.ACLEntry
}

func managerCapabilities(t *testing.T) *managerCapabilityFixture {
	t.Helper()
	manager, store, ctx := testManager(t, access.DefaultDeny)
	f := &managerCapabilityFixture{store: store, ctx: adminContext(ctx, "acme")}
	var err error
	f.share, f.token, err = manager.CreateShare(f.ctx, access.CreateShareRequest{
		TenantID: "acme", Bucket: "default", Key: "photo.jpg", AllowPreview: true, MaxUses: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.asset, err = manager.PublishAsset(f.ctx, access.PublicAsset{
		TenantID: "acme", Bucket: "default", Key: "photo.jpg", Slug: "photo",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.acl = access.ACLEntry{ID: "creator-acl", TenantID: "acme", Bucket: "default", Key: "photo.jpg",
		ResourceKind: access.ResourceObject, PrincipalType: access.PrincipalTypeUser,
		PrincipalID: "reader", Action: access.ActionRead, Effect: access.EffectAllow,
		CreatedBy: "admin", CreatedAt: time.Now().UTC()}
	seedACL(t, store, f.acl)
	f.reader = managerBoundaryFacts(store)
	f.manager = boundaryManager(t, f.reader, access.Config{Enabled: true, DefaultPolicy: access.DefaultDeny})
	return f
}

func (f *managerCapabilityFixture) assertUnchanged(t *testing.T) {
	t.Helper()
	acl, err := f.store.GetACLEntry(f.ctx, "acme", f.acl.ID)
	if err != nil || acl != f.acl {
		t.Fatalf("ACL changed: %+v, %v", acl, err)
	}
	share, err := f.store.GetShare(f.ctx, "acme", f.share.ID)
	if err != nil || share.UseCount != 0 || !share.RevokedAt.IsZero() {
		t.Fatalf("share consumed or revoked: %+v, %v", share, err)
	}
	shares, err := f.store.ListShares(f.ctx, "acme", "default", "photo.jpg")
	if err != nil || len(shares) != 1 {
		t.Fatalf("shares changed: %+v, %v", shares, err)
	}
	asset, err := f.store.GetPublicAsset(f.ctx, f.asset.Slug)
	if err != nil || asset != f.asset {
		t.Fatalf("asset changed: %+v, %v", asset, err)
	}
}

type managerBoundaryOperation struct {
	name, bucket string
	run          func() error
}

func (f *managerCapabilityFixture) operations() []managerBoundaryOperation {
	return []managerBoundaryOperation{
		{"delete ACL creator", "default", func() error { return f.manager.DeleteACL(f.ctx, "acme", f.acl.ID) }},
		{"list ACL", "default", func() error {
			_, err := f.manager.ListACL(f.ctx, access.Resource{TenantID: "acme", Bucket: "default"})
			return err
		}},
		{"put ACL", "default", func() error { _, err := f.manager.PutACL(f.ctx, f.acl); return err }},
		{"create share", "default", func() error {
			_, _, err := f.manager.CreateShare(f.ctx, access.CreateShareRequest{TenantID: "acme", Bucket: "default", Key: "photo.jpg"})
			return err
		}},
		{"list shares", "default", func() error {
			_, err := f.manager.ListShares(f.ctx, "acme", "default", "photo.jpg", "")
			return err
		}},
		{"revoke share creator", "default", func() error { return f.manager.RevokeShare(f.ctx, "acme", f.share.ID) }},
		{"resolve share", "default", func() error {
			_, _, err := f.manager.ResolveShare(f.ctx, f.token, "", access.ActionPreview, false)
			return err
		}},
		{"consume share", "default", func() error {
			_, _, err := f.manager.ResolveShare(f.ctx, f.token, "", access.ActionPreview, true)
			return err
		}},
		{"resolve asset", "default", func() error { _, _, err := f.manager.ResolvePublicAsset(f.ctx, "photo"); return err }},
		{"unpublish creator", "default", func() error { return f.manager.UnpublishAsset(f.ctx, "acme", "photo") }},
		{"replace old bound target", "default", f.replaceAsset},
		{"replace new bound target", "destination", f.replaceAsset},
	}
}

func (f *managerCapabilityFixture) replaceAsset() error {
	asset := f.asset
	asset.Bucket = "destination"
	_, err := f.manager.PublishAsset(f.ctx, asset)
	return err
}
