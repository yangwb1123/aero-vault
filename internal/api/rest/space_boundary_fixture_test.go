package rest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/auth"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/spaces"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// Existing source-selection doubles hide optional methods behind Repository.
// Forward only the real underlying reader: never invoke GetObject (and change
// its count/selection oracle), synthesize unbound, or bypass a failed read.
func fixtureProbeBinding(ctx context.Context, repo repository.Repository, tenant, bucket string) (spaces.BindingResult, error) {
	reader, ok := repo.(spaces.BindingReader)
	if !ok || adminPortMissing(reader) {
		return spaces.BindingResult{}, fmt.Errorf("%w: fixture binding reader missing", access.ErrResourceBoundaryFault)
	}
	return reader.ProbeSpaceBinding(ctx, tenant, bucket)
}

func fixtureProbeCoverage(ctx context.Context, repo repository.Repository, scope spaces.CoverageScope) (spaces.CoverageResult, error) {
	reader, ok := repo.(spaces.CoverageReader)
	if !ok || adminPortMissing(reader) {
		return spaces.CoverageResult{}, fmt.Errorf("%w: fixture coverage reader missing", access.ErrResourceBoundaryFault)
	}
	return reader.ProbeSpaceCoverage(ctx, scope)
}

func (r *openedContentRepo) ProbeSpaceBinding(ctx context.Context, tenant, bucket string) (spaces.BindingResult, error) {
	return fixtureProbeBinding(ctx, r.Repository, tenant, bucket)
}

func (r *openedContentRepo) ProbeSpaceCoverage(ctx context.Context, scope spaces.CoverageScope) (spaces.CoverageResult, error) {
	return fixtureProbeCoverage(ctx, r.Repository, scope)
}

func (r *thumbnailAccessSourceRepo) ProbeSpaceBinding(ctx context.Context, tenant, bucket string) (spaces.BindingResult, error) {
	return fixtureProbeBinding(ctx, r.Repository, tenant, bucket)
}

func (r *thumbnailAccessSourceRepo) ProbeSpaceCoverage(ctx context.Context, scope spaces.CoverageScope) (spaces.CoverageResult, error) {
	return fixtureProbeCoverage(ctx, r.Repository, scope)
}

func (r *etagSwapRepo) ProbeSpaceBinding(ctx context.Context, tenant, bucket string) (spaces.BindingResult, error) {
	return fixtureProbeBinding(ctx, r.Repository, tenant, bucket)
}

func (r *etagSwapRepo) ProbeSpaceCoverage(ctx context.Context, scope spaces.CoverageScope) (spaces.CoverageResult, error) {
	return fixtureProbeCoverage(ctx, r.Repository, scope)
}

func (r *thumbnailPreconditionSwapRepo) ProbeSpaceBinding(ctx context.Context, tenant, bucket string) (spaces.BindingResult, error) {
	return fixtureProbeBinding(ctx, r.Repository, tenant, bucket)
}

func (r *thumbnailPreconditionSwapRepo) ProbeSpaceCoverage(ctx context.Context, scope spaces.CoverageScope) (spaces.CoverageResult, error) {
	return fixtureProbeCoverage(ctx, r.Repository, scope)
}

// The replay fixture keeps all writes on the wrapper, while admission and
// exact-resource authority are explicitly captured from the real SQL store.
type replayMutationRepo struct {
	repository.Repository
	mutations atomic.Int64
	usage     atomic.Int64
}

func (r *replayMutationRepo) UpsertObject(ctx context.Context, obj repository.Object) (repository.Object, error) {
	r.mutations.Add(1)
	return r.Repository.UpsertObject(ctx, obj)
}
func (r *replayMutationRepo) UpdateTags(ctx context.Context, tenant, bucket, key string, tags map[string]string) error {
	r.mutations.Add(1)
	return r.Repository.UpdateTags(ctx, tenant, bucket, key, tags)
}
func (r *replayMutationRepo) SetObjectACL(ctx context.Context, tenant, bucket, key, acl string) error {
	r.mutations.Add(1)
	return r.Repository.SetObjectACL(ctx, tenant, bucket, key, acl)
}
func (r *replayMutationRepo) ReplaceObjectMetadata(ctx context.Context, tenant, bucket, key string, meta map[string]string) error {
	r.mutations.Add(1)
	return r.Repository.ReplaceObjectMetadata(ctx, tenant, bucket, key, meta)
}
func (r *replayMutationRepo) SetObjectMetaKeys(ctx context.Context, tenant, bucket, key string, meta map[string]string) error {
	r.mutations.Add(1)
	return r.Repository.SetObjectMetaKeys(ctx, tenant, bucket, key, meta)
}
func (r *replayMutationRepo) DeleteObjectMetaKey(ctx context.Context, tenant, bucket, key, name string) error {
	r.mutations.Add(1)
	return r.Repository.DeleteObjectMetaKey(ctx, tenant, bucket, key, name)
}
func (r *replayMutationRepo) SetObjectRetention(ctx context.Context, id int64, until time.Time, meta map[string]string) error {
	r.mutations.Add(1)
	return r.Repository.SetObjectRetention(ctx, id, until, meta)
}
func (r *replayMutationRepo) RestoreObject(ctx context.Context, tenant, bucket, key string) error {
	r.mutations.Add(1)
	return r.Repository.RestoreObject(ctx, tenant, bucket, key)
}
func (r *replayMutationRepo) HardDeleteObjectWithEvent(ctx context.Context, tenant, bucket, key string, audit repository.AuditEntry, facts []repository.OutboxFact) error {
	r.mutations.Add(1)
	return r.Repository.HardDeleteObjectWithEvent(ctx, tenant, bucket, key, audit, facts)
}
func (r *replayMutationRepo) SoftDeleteObjectWithEvent(ctx context.Context, tenant, bucket, key string, audit repository.AuditEntry, facts []repository.OutboxFact) error {
	r.mutations.Add(1)
	return r.Repository.SoftDeleteObjectWithEvent(ctx, tenant, bucket, key, audit, facts)
}
func (r *replayMutationRepo) AddTenantUsage(ctx context.Context, tenant string, bytes, objects int64) (repository.TenantQuota, error) {
	r.usage.Add(1)
	return r.Repository.AddTenantUsage(ctx, tenant, bytes, objects)
}

type replayBindingGate struct {
	spaces.BindingReader
	mode atomic.Int64
}

func (g *replayBindingGate) ProbeSpaceBinding(ctx context.Context, tenant, bucket string) (spaces.BindingResult, error) {
	if g.mode.Load() == 2 {
		return spaces.BindingResult{}, errors.Join(repository.ErrNotFound, access.ErrDenied, errors.New("private persistence fault"))
	}
	row, err := g.BindingReader.ProbeSpaceBinding(ctx, tenant, bucket)
	if err == nil && g.mode.Load() == 1 {
		// A complete programmable observation models a newly bound legacy
		// default bucket. It is not a production binding mutation or guess.
		row.State, row.TenantKnown, row.TenantStatus = spaces.BindingBound, true, "active"
		row.SpaceID, row.Revision = "12345678-1234-1234-1234-123456789abc", 1
	}
	return row, err
}

type replayActionProvider struct {
	action access.Action
	key    string
	deny   atomic.Bool
	fault  atomic.Bool
	calls  atomic.Int64
}

func (p *replayActionProvider) Authorize(_ context.Context, _ access.Principal, action access.Action, resource access.Resource) (access.Decision, error) {
	p.calls.Add(1)
	if p.fault.Load() {
		return access.Decision{}, errors.Join(access.ErrDenied, repository.ErrNotFound, errors.New("provider fault"))
	}
	return access.Decision{Allowed: !p.deny.Load() && action == p.action && resource.TenantID == service.DefaultTenant && resource.Bucket == service.DefaultBucket && resource.Key == p.key}, nil
}

type replayObservedStore struct {
	storage.Storage
	puts atomic.Int64
	gets atomic.Int64
}

func (s *replayObservedStore) Put(ctx context.Context, key string, body io.Reader, size int64, opts storage.PutOptions) (storage.ObjectInfo, error) {
	s.puts.Add(1)
	return s.Storage.Put(ctx, key, body, size, opts)
}
func (s *replayObservedStore) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	s.gets.Add(1)
	return s.Storage.Get(ctx, key)
}

type replayObservedSink struct{ calls atomic.Int64 }

func (s *replayObservedSink) Publish(context.Context, repository.Event) { s.calls.Add(1) }

type replayFixture struct {
	raw     repository.Repository
	repo    *replayMutationRepo
	store   *replayObservedStore
	sink    *replayObservedSink
	svc     *service.FileService
	gate    *replayBindingGate
	readers RouterResourceReaders
	router  http.Handler
}

func newReplayFixture(t *testing.T) *replayFixture {
	t.Helper()
	dir := t.TempDir()
	raw, err := repository.Open(t.Context(), "sqlite", "file:"+filepath.Join(dir, "replay.db"))
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
	repo := &replayMutationRepo{Repository: raw}
	store := &replayObservedStore{Storage: local}
	sink := &replayObservedSink{}
	readers := DiscoverRouterResourceReaders(raw)
	gate := &replayBindingGate{BindingReader: readers.Binding}
	readers.Binding = gate
	svc := service.NewFileService(store, repo, idemSilentLogger()).WithEventSink(sink).
		WithSpaceBoundary(service.NewSpaceBoundary(readers.SpaceBoundaryReaders))
	f := &replayFixture{raw: raw, repo: repo, store: store, svc: svc, gate: gate, sink: sink, readers: readers}
	f.router = NewRouter(svc, repo, nil, nil, nil, nil, nil, idemSilentLogger(), false, nil, nil, 0, false,
		func(h *Handler) {
			h.WithResourceReaders(readers).WithPutPresigner(auth.NewPutPresigner(strings.Repeat("s", 32)))
		})
	return f
}

func (f *replayFixture) request(method, path, key string, body io.Reader, contentType string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://aero.test"+path, body)
	r.Header.Set("Idempotency-Key", key)
	r.Header.Set(mw.TenantHeader, service.DefaultTenant)
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	r = r.WithContext(access.WithPrincipal(r.Context(), access.Principal{Kind: access.PrincipalUser, SubjectID: "reader", TenantID: service.DefaultTenant, Scopes: []string{"read", "write"}}))
	w := httptest.NewRecorder()
	mw.Tenant(f.router).ServeHTTP(w, r)
	return w
}

func (f *replayFixture) effects() [5]int64 {
	return [5]int64{f.repo.mutations.Load(), f.repo.usage.Load(), f.store.puts.Load(), f.store.gets.Load(), f.sink.calls.Load()}
}
