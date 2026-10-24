package access_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

type managerShortcutCase struct {
	name      string
	principal access.Principal
	action    access.Action
	cfg       access.Config
	reason    string
}

func managerShortcutCases() []managerShortcutCase {
	user := access.Principal{SubjectID: "alice", TenantID: "acme", Kind: access.PrincipalUser}
	owner, admin, operator, capability, deletion := user, user, user, user, user
	admin.SubjectID, admin.Scopes = "admin", []string{"admin"}
	operator.SubjectID, operator.TenantID = "operator", "*"
	capability = access.CapabilityPrincipal(access.PrincipalShare, access.Capability{
		TenantID: "acme", Bucket: "default", Key: "photo.jpg", Actions: []access.Action{access.ActionRead},
	})
	deletion.Scopes = []string{access.PermissionVaultFileDelete}
	cfg := access.Config{Enabled: true, DefaultPolicy: access.DefaultDeny}
	return []managerShortcutCase{
		{"system", access.Principal{Kind: access.PrincipalSystem}, access.ActionDelete, cfg, "trusted_system"},
		{"disabled read", user, access.ActionRead, access.Config{}, "access_control_disabled"},
		{"delete opt out", user, access.ActionDelete, access.Config{DeleteFailOpen: true}, "access_control_disabled"},
		{"owner", owner, access.ActionRead, cfg, "owner"},
		{"administrator", admin, access.ActionRead, cfg, "administrator"},
		{"operator", operator, access.ActionRead, cfg, "administrator"},
		{"capability", capability, access.ActionRead, cfg, "capability"},
		{"explicit delete", deletion, access.ActionDelete, cfg, "explicit_delete_grant"},
	}
}

func TestManagerBoundaryPrecedesLegacyShortcuts(t *testing.T) {
	_, store, ctx := testManager(t, access.DefaultDeny)
	cause := errors.New("binding reader down")
	for _, mode := range []string{"unbound", "bound", "fault", "disabled tenant", "malformed echo"} {
		for _, tc := range managerShortcutCases() {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				reader := managerBoundaryFacts(store)
				managerBoundaryMode(reader, mode, cause)
				manager := boundaryManager(t, reader, tc.cfg)
				decision, err := manager.Authorize(ctx, tc.principal, tc.action, access.Resource{
					TenantID: "acme", Bucket: "default", Key: "photo.jpg", OwnerID: "alice",
				})
				if mode == "unbound" {
					if err != nil || !decision.Allowed || decision.Reason != tc.reason {
						t.Fatalf("legacy decision=%+v err=%v", decision, err)
					}
				} else {
					if decision.Allowed {
						t.Fatalf("shortcut bypassed boundary: %+v", decision)
					}
					assertManagerBoundaryError(t, mode, err, cause)
				}
				if reader.bindingCalls != 1 {
					t.Fatalf("binding calls=%d, want 1", reader.bindingCalls)
				}
			})
		}
	}
}

func managerBoundaryMode(reader *managerBoundaryStore, mode string, cause error) {
	fact := reader.facts[[2]string{"acme", "default"}]
	switch mode {
	case "bound":
		reader.bind("default")
	case "fault":
		reader.bindingErr = cause
	case "disabled tenant":
		fact.TenantKnown, fact.TenantStatus = true, "disabled"
		reader.facts[[2]string{"acme", "default"}] = fact
	case "malformed echo":
		fact.Bucket = "destination"
		reader.facts[[2]string{"acme", "default"}] = fact
	}
}

func assertManagerBoundaryError(t *testing.T, mode string, err, cause error) {
	t.Helper()
	switch mode {
	case "bound":
		if !access.IsResourceBoundaryDenied(err) || !errors.Is(err, access.ErrSpaceBound) {
			t.Fatalf("want Space denial, got %v", err)
		}
	case "disabled tenant":
		if !access.IsResourceBoundaryDenied(err) || !errors.Is(err, spaces.ErrTenantUnavailable) {
			t.Fatalf("want tenant denial, got %v", err)
		}
	default:
		assertManagerBoundaryFault(t, err)
		if mode == "fault" && !errors.Is(err, cause) {
			t.Fatalf("lost cause %v: %v", cause, err)
		}
	}
}

func assertManagerBoundaryFault(t *testing.T, err error) {
	t.Helper()
	if !access.IsResourceBoundaryFault(err) || access.IsResourceBoundaryDenied(err) {
		t.Fatalf("want boundary fault, got %v", err)
	}
}

func TestManagerBoundaryStopsCapabilitiesAndMutations(t *testing.T) {
	for _, mode := range []string{"bound", "fault"} {
		f := managerCapabilities(t)
		cause := errors.New("private binding failure")
		for _, op := range f.operations() {
			t.Run(mode+"/"+op.name, func(t *testing.T) {
				f.reader.facts = managerBoundaryFacts(nil).facts
				if mode == "bound" {
					f.reader.bind(op.bucket)
				} else {
					f.reader.bindingFaults = map[[2]string]error{{"acme", op.bucket}: cause}
				}
				err := op.run()
				assertManagerBoundaryError(t, mode, err, cause)
				if mode == "fault" && errors.Is(err, access.ErrDenied) {
					t.Fatalf("boundary fault was joined with ordinary denial: %v", err)
				}
				f.assertUnchanged(t)
			})
		}
	}
}

func TestManagerBoundaryUnboundLegacyControls(t *testing.T) {
	// Each operation has its own persisted state so creator and publisher
	// shortcuts are positive controls, not merely failed authorization cases.
	for index := range (&managerCapabilityFixture{}).operations() {
		f := managerCapabilities(t)
		op := f.operations()[index]
		t.Run(op.name, func(t *testing.T) {
			if err := op.run(); err != nil {
				t.Fatalf("unbound legacy operation: %v", err)
			}
		})
	}
}

func TestManagerBoundaryFaultCausePriority(t *testing.T) {
	f := managerCapabilities(t)
	for _, cause := range []error{access.ErrDenied, access.ErrNotFound, spaces.ErrTenantUnavailable,
		context.Canceled, context.DeadlineExceeded} {
		f.reader.bindingErr = fmt.Errorf("provider: %w", cause)
		for _, op := range f.operations() {
			err := op.run()
			assertManagerBoundaryError(t, "fault", err, cause)
			f.assertUnchanged(t)
		}
	}
}

type managerStoreWithoutReaders struct{ access.Store }

func TestManagerBoundaryMissingAndTypedNilPorts(t *testing.T) {
	for _, store := range []access.Store{&managerStoreWithoutReaders{}, (*managerBoundaryStore)(nil)} {
		manager := boundaryManager(t, store, access.Config{DeleteFailOpen: true})
		ctx := access.SystemContext(t.Context(), "acme")
		principal, _ := access.PrincipalFrom(ctx)
		decision, err := manager.Authorize(ctx, principal, access.ActionDelete, access.Resource{TenantID: "acme", Bucket: "default"})
		assertManagerBoundaryError(t, "fault", err, access.ErrResourceBoundaryFault)
		if decision.Allowed {
			t.Fatal("missing binding reader bypassed by system/delete opt-out")
		}
		assets, err := manager.ListPublicAssets(adminContext(t.Context(), "acme"), "acme")
		assertManagerBoundaryError(t, "fault", err, access.ErrResourceBoundaryFault)
		if assets != nil {
			t.Fatal("missing coverage reader returned an empty-success page")
		}
	}
}

func TestManagerPublicAssetsCoverageBracketsEmptyMaterialization(t *testing.T) {
	cause := errors.New("coverage unavailable")
	fault := func(s *managerBoundaryStore) { s.coverageErr = cause }
	cases := []struct {
		name, mode string
		before     bool
		mutate     func(*managerBoundaryStore)
	}{
		{"unbound", "unbound", false, func(*managerBoundaryStore) {}},
		{"P0 bound", "bound", true, (*managerBoundaryStore).bindCoverage},
		{"P0 fault", "fault", true, fault},
		{"P1 bound", "bound", false, (*managerBoundaryStore).bindCoverage},
		{"P1 fault", "fault", false, fault},
		{"P1 partial", "partial", false, func(s *managerBoundaryStore) { s.coverage.Complete = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := managerBoundaryFacts(nil)
			reader.assetRows = []access.PublicAsset{}
			reader.afterList = func() { tc.mutate(reader) }
			wantLists, wantProbes := 1, 2
			if tc.before {
				tc.mutate(reader)
				wantLists, wantProbes = 0, 1
			}
			manager := boundaryManager(t, reader, access.Config{})
			assets, err := manager.ListPublicAssets(adminContext(t.Context(), "acme"), "acme")
			assertManagerAssetPage(t, tc.mode, assets, err, cause)
			if reader.listCalls != wantLists || reader.coverageCalls != wantProbes {
				t.Fatalf("lists/probes=%d/%d, want %d/%d", reader.listCalls, reader.coverageCalls, wantLists, wantProbes)
			}
		})
	}
}

func assertManagerAssetPage(t *testing.T, mode string, assets []access.PublicAsset, err, cause error) {
	t.Helper()
	if mode == "unbound" {
		if err != nil || len(assets) != 0 {
			t.Fatalf("legacy empty page=%+v, %v", assets, err)
		}
		return
	}
	assertManagerBoundaryError(t, mode, err, cause)
	if assets != nil {
		t.Fatalf("coverage bypass: assets=%+v err=%v", assets, err)
	}
}

func TestManagerPublicAssetsChecksRowsBeforePublisherAndVisibilityShortcuts(t *testing.T) {
	for _, publisher := range []bool{false, true} {
		for _, mode := range []string{"unbound", "bound", "fault", "foreign tenant"} {
			t.Run(fmt.Sprintf("publisher=%v/%s", publisher, mode), func(t *testing.T) {
				checkManagerAssetRows(t, publisher, mode)
			})
		}
	}
}

func checkManagerAssetRows(t *testing.T, publisher bool, mode string) {
	t.Helper()
	reader := managerBoundaryFacts(nil)
	reader.assetRows = []access.PublicAsset{{TenantID: "acme", Bucket: "default", PublishedBy: "someone-else"}}
	cause := errors.New("row binding failure")
	managerBoundaryMode(reader, mode, cause)
	if mode == "foreign tenant" {
		reader.assetRows[0].TenantID = "other"
	}
	principal := access.Principal{SubjectID: "viewer", TenantID: "acme"}
	want := 0
	if publisher {
		principal.Roles = []string{"vault.publisher"}
		want = 1
	}
	manager := boundaryManager(t, reader, access.Config{})
	assets, err := manager.ListPublicAssets(access.WithPrincipal(t.Context(), principal), "acme")
	if mode != "unbound" {
		assertManagerBoundaryError(t, mode, err, cause)
		if assets != nil {
			t.Fatal("partial assets escaped")
		}
		return
	}
	if err != nil || len(assets) != want || reader.coverageCalls != 2 {
		t.Fatalf("legacy visibility assets=%+v err=%v probes=%d", assets, err, reader.coverageCalls)
	}
}

func TestManagerCapabilityLookupDefersOnlyUnboundTenantSuspension(t *testing.T) {
	f := managerCapabilities(t)
	managerBoundaryMode(f.reader, "disabled tenant", nil)
	_, sharePrincipal, err := f.manager.ResolveShare(f.ctx, f.token, "", access.ActionPreview, false)
	if err != nil {
		t.Fatalf("non-consuming lookup must retain FileService TenantDisabled path: %v", err)
	}
	_, assetPrincipal, err := f.manager.ResolvePublicAsset(f.ctx, f.asset.Slug)
	if err != nil {
		t.Fatalf("asset lookup must retain FileService TenantDisabled path: %v", err)
	}
	for _, principal := range []access.Principal{sharePrincipal, assetPrincipal} {
		decision, err := f.manager.Authorize(f.ctx, principal, access.ActionRead, access.Resource{
			TenantID: "acme", Bucket: "default", Key: "photo.jpg",
		})
		assertManagerBoundaryError(t, "disabled tenant", err, nil)
		if decision.Allowed {
			t.Fatal("resolved capability authorized a disabled tenant")
		}
	}
	_, _, err = f.manager.ResolveShare(f.ctx, f.token, "", access.ActionPreview, true)
	assertManagerBoundaryError(t, "disabled tenant", err, nil)
	f.assertUnchanged(t)
	f.reader.facts = managerBoundaryFacts(nil).facts
	if _, _, err := f.manager.ResolveShare(f.ctx, f.token, "", access.ActionPreview, true); err != nil {
		t.Fatalf("unbound reactivation: %v", err)
	}
	share, err := f.store.GetShare(f.ctx, "acme", f.share.ID)
	if err != nil || share.UseCount != 1 {
		t.Fatalf("reactivated use count=%d err=%v", share.UseCount, err)
	}
}

func TestManagerBoundaryRealBindingAndTenantIsolation(t *testing.T) {
	_, store, ctx := testManager(t, access.DefaultDeny)
	repo := store.(repository.Repository)
	if err := repo.UpsertTenant(ctx, repository.TenantRecord{TenantID: "acme", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	space, err := store.(spaces.Store).CreateSpace(ctx, spaces.CreateCommand{
		TenantID: "acme", ID: managerBoundarySpaceID, Kind: spaces.Team, Name: "team",
		Actor:     spaces.SubjectRef{Source: "local", Kind: spaces.User, ID: "owner"},
		CommandID: "22345678-1234-1234-1234-123456789abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	manager := boundaryManager(t, store, access.Config{DeleteFailOpen: true})
	system := access.Principal{Kind: access.PrincipalSystem}
	resource := access.Resource{TenantID: "acme", Bucket: space.BackingBucket}
	decision, err := manager.Authorize(ctx, system, access.ActionDelete, resource)
	assertManagerBoundaryError(t, "bound", err, nil)
	if decision.Allowed {
		t.Fatal("real bound bucket admitted")
	}
	resource.TenantID = "other"
	decision, err = manager.Authorize(ctx, system, access.ActionDelete, resource)
	if err != nil || !decision.Allowed {
		t.Fatalf("exact unbound other-tenant control=%+v err=%v", decision, err)
	}
	resource.TenantID, resource.Bucket = "acme", "spc-legacy"
	decision, err = manager.Authorize(ctx, system, access.ActionDelete, resource)
	if err != nil || !decision.Allowed {
		t.Fatalf("bucket prefix incorrectly inferred binding: %+v err=%v", decision, err)
	}
}

func TestManagerBoundaryCapabilitiesDoNotSubstituteForEachOther(t *testing.T) {
	reader := managerBoundaryFacts(nil)
	reader.assetRows = []access.PublicAsset{}
	cfg := access.Config{DeleteFailOpen: true}
	principal := access.Principal{Kind: access.PrincipalSystem}
	resource := access.Resource{TenantID: "acme", Bucket: "default"}
	manager := boundaryManager(t, &managerBindingOnly{Store: reader, BindingReader: reader}, cfg)
	decision, err := manager.Authorize(t.Context(), principal, access.ActionRead, resource)
	if err != nil || !decision.Allowed {
		t.Fatalf("binding-only exact resource: %+v, %v", decision, err)
	}
	assets, err := manager.ListPublicAssets(adminContext(t.Context(), "acme"), "acme")
	assertManagerBoundaryFault(t, err)
	if assets != nil || reader.listCalls != 0 {
		t.Fatal("binding reader substituted for coverage")
	}
	manager = boundaryManager(t, &managerCoverageOnly{Store: reader, CoverageReader: reader}, cfg)
	decision, err = manager.Authorize(t.Context(), principal, access.ActionRead, resource)
	assertManagerBoundaryFault(t, err)
	if decision.Allowed {
		t.Fatal("coverage reader substituted for binding")
	}
	assets, err = manager.ListPublicAssets(adminContext(t.Context(), "acme"), "acme")
	if err != nil || len(assets) != 0 || reader.coverageCalls != 2 {
		t.Fatalf("coverage-only empty list: %+v, %v", assets, err)
	}
}

func TestManagerBoundaryAlwaysReobservesCapturedReaders(t *testing.T) {
	reader := managerBoundaryFacts(nil)
	manager := boundaryManager(t, reader, access.Config{DeleteFailOpen: true})
	cause := errors.New("later binding outage")
	for _, mode := range []string{"unbound", "bound", "fault"} {
		managerBoundaryMode(reader, mode, cause)
		decision, err := manager.Authorize(t.Context(), access.Principal{Kind: access.PrincipalSystem},
			access.ActionDelete, access.Resource{TenantID: "acme", Bucket: "default"})
		if mode == "unbound" {
			if err != nil || !decision.Allowed {
				t.Fatalf("unbound control: %+v, %v", decision, err)
			}
		} else {
			assertManagerBoundaryError(t, mode, err, cause)
			if decision.Allowed {
				t.Fatal("cached earlier admission")
			}
		}
	}
	if reader.bindingCalls != 3 {
		t.Fatalf("fresh binding calls=%d, want 3", reader.bindingCalls)
	}
}

func TestManagerPublishLookupBoundaryFaultDoesNotBecomeAbsence(t *testing.T) {
	f := managerCapabilities(t)
	f.reader.bindingErr = access.ErrNotFound
	f.reader.assetLookupErr = access.NewResourceBoundary(f.reader, nil).CheckBucket(f.ctx, "acme", "default")
	f.reader.bindingErr = nil
	asset, err := f.manager.PublishAsset(f.ctx, f.asset)
	assertManagerBoundaryError(t, "fault", err, access.ErrNotFound)
	if asset.ID != "" {
		t.Fatal("lookup fault produced a published asset")
	}
	f.assertUnchanged(t)
}

func TestACLBoundaryFixtureFactsAreNarrow(t *testing.T) {
	for _, reader := range []spaces.BindingReader{&failingACLStore{}, &trackingACLStore{}} {
		for _, scope := range [][2]string{{"acme", "default"}, {"other", "default"}, {"acme", "other"}} {
			fact, err := reader.ProbeSpaceBinding(t.Context(), scope[0], scope[1])
			if scope == [2]string{"acme", "default"} {
				if err != nil || fact.State != spaces.BindingUnbound {
					t.Fatalf("declared fact=%+v err=%v", fact, err)
				}
			} else if err == nil || fact != (spaces.BindingResult{}) {
				t.Fatalf("undeclared scope accepted: %+v, %v", fact, err)
			}
		}
	}
}
