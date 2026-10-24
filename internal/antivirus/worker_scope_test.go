package antivirus

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/spaces"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// workerScopeStore counts storage reads so refusal tests prove the worker
// stopped before touching object bytes.
type workerScopeStore struct {
	storage.Storage
	gets int
}

func (s *workerScopeStore) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	s.gets++
	return s.Storage.Get(ctx, key)
}

// workerScopeScanner records whether the scanner was invoked. Infected is the
// default so any accidental invocation would also be visible as a tag or
// quarantine attempt.
type workerScopeScanner struct {
	calls int
}

func (s *workerScopeScanner) Scan(context.Context, io.Reader) (Result, error) {
	s.calls++
	return Result{Clean: false, Signature: "scope-test"}, nil
}

func (s *workerScopeScanner) Name() string { return "scope-test" }

// workerScopeController records controller mutations. It is deliberately a spy
// so a boundary refusal cannot hide behind a nil/no-op FileService.
type workerScopeController struct {
	tags       int
	quarantine int
}

func (c *workerScopeController) SetObjectTagsByID(context.Context, int64, map[string]string) error {
	c.tags++
	return nil
}

func (c *workerScopeController) QuarantineObjectByID(context.Context, int64, string) error {
	c.quarantine++
	return nil
}

type workerScopeBindingFunc func(context.Context, string, string) (spaces.BindingResult, error)

func (f workerScopeBindingFunc) ProbeSpaceBinding(ctx context.Context, tenant, bucket string) (spaces.BindingResult, error) {
	return f(ctx, tenant, bucket)
}

func workerScopeBoundSpace(t *testing.T, ctx context.Context, repo repository.Repository) spaces.Space {
	t.Helper()
	if err := repo.UpsertTenant(ctx, repository.TenantRecord{TenantID: "default", Status: "active"}); err != nil {
		t.Fatalf("upsert tenant: %v", err)
	}
	store, ok := repo.(spaces.Store)
	if !ok {
		t.Fatal("repository does not implement spaces.Store")
	}
	space, err := store.CreateSpace(ctx, spaces.CreateCommand{
		TenantID: "default", ID: uuid.NewString(), Kind: spaces.Team, Name: "scope-test",
		Actor:     spaces.SubjectRef{Source: "local", Kind: spaces.User, ID: "owner"},
		CommandID: uuid.NewString(), Reason: "scope test", RequestID: "scope-request",
	})
	if err != nil {
		t.Fatalf("create space: %v", err)
	}
	return space
}

func workerScopeAssertUntouched(t *testing.T, repo repository.Repository, objectID int64,
	scanner *workerScopeScanner, store *workerScopeStore, controller *workerScopeController) {
	t.Helper()
	if scanner.calls != 0 {
		t.Fatalf("scanner called %d times after refusal", scanner.calls)
	}
	if store.gets != 0 {
		t.Fatalf("storage read %d times after refusal", store.gets)
	}
	if controller.tags != 0 || controller.quarantine != 0 {
		t.Fatalf("controller mutations after refusal: tags=%d quarantine=%d", controller.tags, controller.quarantine)
	}
	obj, err := repo.GetObjectByID(context.Background(), objectID)
	if err != nil {
		t.Fatalf("reload object: %v", err)
	}
	if obj.DeletedAt != nil || obj.Tags[TagStatus] != "" || obj.Tags[TagSignature] != "" {
		t.Fatalf("refused scan mutated object: deleted_at=%v tags=%v", obj.DeletedAt, obj.Tags)
	}
}

func TestScanObjectByIDRejectsMalformedStoredScopeBeforeSideEffects(t *testing.T) {
	ctx := context.Background()
	repo, svc := setupSvc(t)
	// A real persisted row with an incomplete scope. InsertObjectVersion is a
	// lower-level repository operation; the worker must not trust its row just
	// because it came from the database.
	invalid, err := repo.InsertObjectVersion(ctx, repository.Object{
		TenantID: "default", Bucket: "", Key: "invalid-scope.txt",
		Backend: "local", StorageKey: "default//invalid-scope.txt", Size: 1,
	})
	if err != nil {
		t.Fatalf("insert malformed row: %v", err)
	}
	scanner := &workerScopeScanner{}
	store := &workerScopeStore{Storage: svc.Storage()}
	controller := &workerScopeController{}
	worker := NewWorker(repo, store, scanner, nil, true, quietLogger()).
		WithObjectController(controller)

	err = worker.ScanObjectByID(ctx, invalid.ID)
	if !errors.Is(err, access.ErrInvalidArgument) {
		t.Fatalf("malformed row error = %v, want invalid scope", err)
	}
	workerScopeAssertUntouched(t, repo, invalid.ID, scanner, store, controller)
}

func TestScanObjectByIDRejectsInexactJobTenantBeforeSideEffects(t *testing.T) {
	ctx := context.Background()
	repo, svc := setupSvc(t)
	obj, err := svc.Put(ctx, "default", "default", "tenant.txt", strings.NewReader("body"), 4, service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	for _, tenant := range []string{"other-tenant", "", "*"} {
		t.Run("tenant="+tenant, func(t *testing.T) {
			scanner := &workerScopeScanner{}
			store := &workerScopeStore{Storage: svc.Storage()}
			controller := &workerScopeController{}
			worker := NewWorker(repo, store, scanner, nil, true, quietLogger()).
				WithObjectController(controller)
			err := worker.ScanObjectByID(access.SystemContext(ctx, tenant), obj.ID)
			if err == nil || !strings.Contains(err.Error(), "tenant") {
				t.Fatalf("job tenant %q error = %v, want tenant mismatch", tenant, err)
			}
			workerScopeAssertUntouched(t, repo, obj.ID, scanner, store, controller)
		})
	}
}

func TestScanObjectByIDRejectsBoundBucketBeforeSideEffects(t *testing.T) {
	ctx := context.Background()
	repo, svc := setupSvc(t)
	space := workerScopeBoundSpace(t, ctx, repo)
	obj, err := svc.Put(ctx, "default", space.BackingBucket, "bound.txt",
		strings.NewReader("bound body"), 10, service.PutOptions{})
	if err != nil {
		t.Fatalf("put bound object: %v", err)
	}
	scanner := &workerScopeScanner{}
	store := &workerScopeStore{Storage: svc.Storage()}
	controller := &workerScopeController{}
	worker := NewWorker(repo, store, scanner, nil, true, quietLogger()).
		WithObjectController(controller)

	err = worker.ScanObjectByID(access.AntivirusContext(ctx, "default"), obj.ID)
	if !errors.Is(err, access.ErrSpaceBound) || !access.IsResourceBoundaryDenied(err) {
		t.Fatalf("bound bucket error = %v, want explicit Space denial", err)
	}
	workerScopeAssertUntouched(t, repo, obj.ID, scanner, store, controller)
}

func TestScanObjectByIDRejectsBoundaryFaultBeforeSideEffects(t *testing.T) {
	ctx := context.Background()
	repo, svc := setupSvc(t)
	obj, err := svc.Put(ctx, "default", "default", "fault.txt", strings.NewReader("body"), 4, service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	boom := errors.New("binding store unavailable")
	fault := workerScopeBindingFunc(func(context.Context, string, string) (spaces.BindingResult, error) {
		return spaces.BindingResult{}, boom
	})
	cases := []struct {
		name   string
		reader spaces.BindingReader
		cause  error
	}{
		{"reader fault", fault, boom},
		{"missing reader", nil, nil},
		{"typed-nil reader", workerScopeBindingFunc(nil), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scanner := &workerScopeScanner{}
			store := &workerScopeStore{Storage: svc.Storage()}
			controller := &workerScopeController{}
			worker := NewWorker(repo, store, scanner, nil, true, quietLogger()).
				WithObjectController(controller).
				WithResourceBoundary(tc.reader)

			err := worker.ScanObjectByID(access.AntivirusContext(ctx, "default"), obj.ID)
			if !errors.Is(err, access.ErrResourceBoundaryFault) {
				t.Fatalf("boundary fault = %v, want fault", err)
			}
			if access.IsResourceBoundaryDenied(err) {
				t.Fatalf("boundary fault classified as ordinary denial: %v", err)
			}
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatalf("boundary fault = %v, want cause %v", err, tc.cause)
			}
			workerScopeAssertUntouched(t, repo, obj.ID, scanner, store, controller)
		})
	}
}

func TestScanObjectByIDRejectsNonPositiveIDBeforeSideEffects(t *testing.T) {
	ctx := context.Background()
	repo, svc := setupSvc(t)
	scanner := &workerScopeScanner{}
	store := &workerScopeStore{Storage: svc.Storage()}
	controller := &workerScopeController{}
	worker := NewWorker(repo, store, scanner, nil, true, quietLogger()).
		WithObjectController(controller)

	if err := worker.ScanObjectByID(ctx, 0); err == nil {
		t.Fatal("zero object ID was accepted")
	}
	if scanner.calls != 0 || store.gets != 0 || controller.tags != 0 || controller.quarantine != 0 {
		t.Fatalf("non-positive ID reached side effects: scanner=%d storage=%d tags=%d quarantine=%d",
			scanner.calls, store.gets, controller.tags, controller.quarantine)
	}
}
