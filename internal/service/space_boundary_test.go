package service

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
	"github.com/aero-vault/aero-vault/internal/storage"
)

type spaceBindingFunc func(context.Context, string, string) (spaces.BindingResult, error)

func (f spaceBindingFunc) ProbeSpaceBinding(ctx context.Context, tenant, bucket string) (spaces.BindingResult, error) {
	return f(ctx, tenant, bucket)
}

type spaceCoverageFunc func(context.Context, spaces.CoverageScope) (spaces.CoverageResult, error)

func (f spaceCoverageFunc) ProbeSpaceCoverage(ctx context.Context, scope spaces.CoverageScope) (spaces.CoverageResult, error) {
	return f(ctx, scope)
}

type spaceJobFunc func(context.Context, int64) (repository.JobLookup, error)

func (f spaceJobFunc) ReadJob(ctx context.Context, id int64) (repository.JobLookup, error) {
	return f(ctx, id)
}

type spaceEffects struct{ puts, gets, inits, completes, aborts, copies, writes, usage, logs, events int }

type spaceStore struct {
	storage.Storage
	effects *spaceEffects
}

func (s *spaceStore) Put(ctx context.Context, key string, r io.Reader, n int64, opts storage.PutOptions) (storage.ObjectInfo, error) {
	s.effects.puts++
	return s.Storage.Put(ctx, key, r, n, opts)
}
func (s *spaceStore) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	s.effects.gets++
	return s.Storage.Get(ctx, key)
}
func (s *spaceStore) InitMultipart(ctx context.Context, key string, opts storage.PutOptions) (storage.MultipartInit, error) {
	s.effects.inits++
	return s.Storage.InitMultipart(ctx, key, opts)
}
func (s *spaceStore) CompleteMultipart(ctx context.Context, key, id string, parts []storage.MultipartPart) (storage.ObjectInfo, error) {
	s.effects.completes++
	return s.Storage.CompleteMultipart(ctx, key, id, parts)
}
func (s *spaceStore) AbortMultipart(ctx context.Context, key, id string) error {
	s.effects.aborts++
	return s.Storage.AbortMultipart(ctx, key, id)
}
func (s *spaceStore) UploadPartCopy(ctx context.Context, key, id string, n int32, source string, offset, length int64) (storage.MultipartPart, error) {
	s.effects.copies++
	return s.Storage.UploadPartCopy(ctx, key, id, n, source, offset, length)
}

type spaceRepository struct {
	repository.Repository
	effects *spaceEffects
	ensure  func()
}

func (r *spaceRepository) CreateBucket(ctx context.Context, tenant, bucket string) error {
	if r.ensure != nil {
		r.ensure()
	}
	return r.Repository.CreateBucket(ctx, tenant, bucket)
}
func (r *spaceRepository) UpsertObject(ctx context.Context, obj repository.Object) (repository.Object, error) {
	r.effects.writes++
	return r.Repository.UpsertObject(ctx, obj)
}
func (r *spaceRepository) AddTenantUsage(ctx context.Context, tenant string, bytes, objects int64) (repository.TenantQuota, error) {
	r.effects.usage++
	return r.Repository.AddTenantUsage(ctx, tenant, bytes, objects)
}
func (r *spaceRepository) WriteAccessLog(ctx context.Context, tenant, bucket, method, key, status, latency, agent string) error {
	r.effects.logs++
	return r.Repository.WriteAccessLog(ctx, tenant, bucket, method, key, status, latency, agent)
}

type spaceSink struct{ effects *spaceEffects }

func (s spaceSink) Publish(context.Context, repository.Event) { s.effects.events++ }

type spaceFixture struct {
	raw     repository.Repository
	repo    *spaceRepository
	store   *spaceStore
	svc     *FileService
	effects *spaceEffects
}

func newSpaceFixture(t *testing.T) *spaceFixture {
	t.Helper()
	dir := t.TempDir()
	raw, err := repository.Open(t.Context(), "sqlite", "file:"+filepath.Join(dir, "space.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	if err := raw.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	local, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatal(err)
	}
	effects := &spaceEffects{}
	repo := &spaceRepository{Repository: raw, effects: effects}
	store := &spaceStore{Storage: local, effects: effects}
	svc := NewFileService(store, repo, nil).WithEventSink(spaceSink{effects}).
		WithSpaceBoundary(NewSpaceBoundary(DiscoverSpaceBoundaryReaders(raw)))
	return &spaceFixture{raw: raw, repo: repo, store: store, svc: svc, effects: effects}
}

func createBoundarySpace(t *testing.T, raw repository.Repository, tenant string) spaces.Space {
	t.Helper()
	if err := raw.UpsertTenant(t.Context(), repository.TenantRecord{TenantID: tenant, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	space, err := raw.(spaces.Store).CreateSpace(t.Context(), spaces.CreateCommand{
		TenantID: tenant, ID: repository.NewVersionID(), Kind: spaces.Team, Name: "private",
		Actor: spaces.SubjectRef{Source: "test", Kind: spaces.User, ID: "owner"}, CommandID: repository.NewVersionID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return space
}

func boundaryPut(t *testing.T, svc *FileService, tenant, bucket, key string) repository.Object {
	t.Helper()
	obj, err := svc.Put(t.Context(), tenant, bucket, key, strings.NewReader("payload"), 7, PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return obj
}

func TestSpaceBoundaryBoundPrecedesSystemAndDeleteOptOut(t *testing.T) {
	f := newSpaceFixture(t)
	space := createBoundarySpace(t, f.raw, "alpha")
	legacy := NewFileService(f.store, f.repo, nil).WithDeleteFailOpen(true)
	obj := boundaryPut(t, legacy, "alpha", space.BackingBucket, "secret")
	f.svc.WithDeleteFailOpen(true)
	ctx := access.SystemContext(t.Context(), "alpha")
	before := *f.effects
	operations := []func() error{
		func() error {
			_, err := f.svc.Put(ctx, "alpha", space.BackingBucket, "new", strings.NewReader("x"), 1, PutOptions{})
			return err
		},
		func() error { _, _, err := f.svc.Get(ctx, "alpha", space.BackingBucket, obj.Key); return err },
		func() error { return f.svc.Delete(ctx, "alpha", space.BackingBucket, obj.Key, true) },
		func() error {
			_, err := f.svc.InitMultipart(ctx, "alpha", space.BackingBucket, "new", PutOptions{})
			return err
		},
		func() error { return f.svc.SetObjectACL(ctx, "alpha", space.BackingBucket, obj.Key, ACLPublicRead) },
	}
	for i, operation := range operations {
		if err := operation(); !errors.Is(err, access.ErrSpaceBound) {
			t.Errorf("operation %d: %v", i, err)
		}
	}
	if *f.effects != before {
		t.Fatalf("bound work caused effects: before=%+v after=%+v", before, *f.effects)
	}
	if got, err := f.raw.GetObject(t.Context(), "alpha", space.BackingBucket, obj.Key); err != nil || got.ID != obj.ID {
		t.Fatalf("object changed: %+v %v", got, err)
	}
}

func TestSpaceBoundaryOptionalNilAndConfiguredFaults(t *testing.T) {
	f := newSpaceFixture(t)
	readers := DiscoverSpaceBoundaryReaders(f.raw)
	if err := f.svc.ValidateSpaceBoundary(); err != nil {
		t.Fatal(err)
	}
	var typedNil *access.ResourceBoundary
	f.svc.WithSpaceBoundary(typedNil)
	if _, err := f.svc.List(t.Context(), "alpha", "docs", "", "", 1); !access.IsResourceBoundaryFault(err) {
		t.Fatalf("typed nil: %v", err)
	}
	f.svc.WithSpaceBoundary(nil)
	boundaryPut(t, f.svc, "alpha", "docs", "legacy")
	if err := f.svc.ValidateSpaceBoundary(); !access.IsResourceBoundaryFault(err) {
		t.Fatalf("nil production preflight: %v", err)
	}
	for _, configure := range []func(*SpaceBoundaryReaders){
		func(r *SpaceBoundaryReaders) { r.Binding = nil },
		func(r *SpaceBoundaryReaders) { r.Binding = spaceBindingFunc(nil) },
		func(r *SpaceBoundaryReaders) { r.Coverage = nil },
		func(r *SpaceBoundaryReaders) { r.Coverage = spaceCoverageFunc(nil) },
		func(r *SpaceBoundaryReaders) { r.Jobs = nil },
		func(r *SpaceBoundaryReaders) { r.Jobs = spaceJobFunc(nil) },
		func(r *SpaceBoundaryReaders) { r.Resources = (*spaceRepository)(nil) },
	} {
		bad := readers
		configure(&bad)
		if err := ValidateSpaceBoundaryReaders(bad); !access.IsResourceBoundaryFault(err) {
			t.Errorf("missing production port: %v", err)
		}
	}
	missing := DiscoverSpaceBoundaryReaders(f.repo) // wrappers do not magically inherit side ports
	f.svc.WithSpaceBoundary(NewSpaceBoundary(missing))
	if _, err := f.svc.List(t.Context(), "alpha", "docs", "", "", 1); !access.IsResourceBoundaryFault(err) {
		t.Fatalf("missing binding: %v", err)
	}
}

func TestSpaceBoundaryCompleteObservationsAndCauses(t *testing.T) {
	f := newSpaceFixture(t)
	cause := errors.Join(repository.ErrNotFound, access.ErrDenied, errors.New("private database detail"))
	readers := DiscoverSpaceBoundaryReaders(f.raw)
	for _, result := range []spaces.BindingResult{
		{}, {TenantID: "other", Bucket: "docs", State: spaces.BindingUnbound},
		{TenantID: "alpha", Bucket: "docs", State: spaces.BindingUnbound, Revision: 1},
		{TenantID: "alpha", Bucket: "docs", State: spaces.BindingBound, SpaceID: "bad", Revision: 1},
	} {
		readers.Binding = spaceBindingFunc(func(context.Context, string, string) (spaces.BindingResult, error) { return result, nil })
		f.svc.WithSpaceBoundary(NewSpaceBoundary(readers))
		if _, err := f.svc.List(t.Context(), "alpha", "docs", "", "", 1); !access.IsResourceBoundaryFault(err) {
			t.Errorf("bad observation accepted: %+v %v", result, err)
		}
	}
	readers.Binding = spaceBindingFunc(func(context.Context, string, string) (spaces.BindingResult, error) {
		return spaces.BindingResult{}, cause
	})
	f.svc.WithSpaceBoundary(NewSpaceBoundary(readers))
	_, err := f.svc.List(t.Context(), "alpha", "docs", "", "", 1)
	if !access.IsResourceBoundaryFault(err) || !errors.Is(err, cause) || access.IsResourceBoundaryDenied(err) {
		t.Fatalf("fault lost cause/priority: %v", err)
	}
	before := *f.effects
	if _, err := f.svc.Put(t.Context(), "alpha", "docs", "x", strings.NewReader("x"), 1, PutOptions{}); !access.IsResourceBoundaryFault(err) {
		t.Fatalf("put fault: %v", err)
	}
	if *f.effects != before {
		t.Fatal("fault performed protected work")
	}
}

func TestSpaceBoundaryFaultsPrecedeWorkAndAnonymousFallback(t *testing.T) {
	f := newSpaceFixture(t)
	readers := DiscoverSpaceBoundaryReaders(f.raw)
	cause := errors.Join(repository.ErrNotFound, access.ErrDenied)
	readers.Binding = spaceBindingFunc(func(context.Context, string, string) (spaces.BindingResult, error) {
		return spaces.BindingResult{}, cause
	})
	f.svc.WithSpaceBoundary(NewSpaceBoundary(readers))
	before := *f.effects
	readable, err := f.svc.ObjectPublicReadableWithError(t.Context(), "alpha", "docs", "missing")
	if readable || !access.IsResourceBoundaryFault(err) || !errors.Is(err, cause) {
		t.Fatalf("anonymous fault became ACL fallback: %v %v", readable, err)
	}
	if err := f.svc.RecordBucketAccessLog(t.Context(), "alpha", "docs", AccessLogEntry{Status: 403}); !access.IsResourceBoundaryFault(err) {
		t.Fatalf("log fault: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.svc.Put(ctx, "alpha", "docs", "cancelled", strings.NewReader("x"), 1, PutOptions{}); !access.IsResourceBoundaryFault(err) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if *f.effects != before {
		t.Fatal("fault/cancellation performed protected work")
	}
}

func TestSpaceBoundaryKnownRawStatusNeverBecomesActive(t *testing.T) {
	f := newSpaceFixture(t)
	readers := DiscoverSpaceBoundaryReaders(f.raw)
	for _, status := range []string{"", "pending", "disabled"} {
		readers.Binding = spaceBindingFunc(func(_ context.Context, tenant, bucket string) (spaces.BindingResult, error) {
			return spaces.BindingResult{TenantID: tenant, Bucket: bucket, TenantKnown: true, TenantStatus: status, State: spaces.BindingUnbound}, nil
		})
		f.svc.WithSpaceBoundary(NewSpaceBoundary(readers))
		if _, err := f.svc.List(access.SystemContext(t.Context(), "alpha"), "alpha", "docs", "", "", 1); !errors.Is(err, spaces.ErrTenantUnavailable) {
			t.Errorf("raw status %q was normalized to active: %v", status, err)
		}
	}
}

func TestSpaceBoundaryTenantRawStatusAndLiteralScope(t *testing.T) {
	f := newSpaceFixture(t)
	boundaryPut(t, f.svc, "unknown", "spc-not-a-binding", "legacy")
	if err := f.raw.UpsertTenant(t.Context(), repository.TenantRecord{TenantID: "alpha", Status: "disabled"}); err != nil {
		t.Fatal(err)
	}
	ctx := access.SystemContext(t.Context(), "alpha")
	if _, err := f.svc.List(ctx, "alpha", "docs", "", "", 1); !errors.Is(err, spaces.ErrTenantUnavailable) {
		t.Fatalf("system raw status: %v", err)
	}
	for _, pair := range [][2]string{{"", "docs"}, {" alpha", "docs"}, {"alpha", "*"}, {"alpha", " docs"}} {
		if err := f.svc.CheckSpaceBucket(t.Context(), pair[0], pair[1]); !access.IsResourceBoundaryFault(err) {
			t.Errorf("normalized literal %q: %v", pair, err)
		}
	}
}
