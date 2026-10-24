package replication

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/jobs"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/spaces"
	"github.com/aero-vault/aero-vault/internal/storage"
)

type replicationScopeStorage struct {
	storage.Storage
	gets int
	puts int
}

func (s *replicationScopeStorage) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	s.gets++
	return s.Storage.Get(ctx, key)
}

func (s *replicationScopeStorage) Put(ctx context.Context, key string, r io.Reader, size int64, opts storage.PutOptions) (storage.ObjectInfo, error) {
	s.puts++
	return s.Storage.Put(ctx, key, r, size, opts)
}

type replicationScopeTagger struct{ calls int }

func (t *replicationScopeTagger) SetObjectTagsByID(context.Context, int64, map[string]string) error {
	t.calls++
	return nil
}

type replicationScopeBindingFunc func(context.Context, string, string) (spaces.BindingResult, error)

func (f replicationScopeBindingFunc) ProbeSpaceBinding(ctx context.Context, tenant, bucket string) (spaces.BindingResult, error) {
	return f(ctx, tenant, bucket)
}

func replicationScopeLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newReplicationScopeFixture(t *testing.T) (context.Context, repository.Repository, *service.FileService, storage.Storage, storage.Storage, string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "scope.db")
	repo, err := repository.Open(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	primary, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "primary")})
	if err != nil {
		t.Fatalf("primary storage: %v", err)
	}
	replica, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "replica")})
	if err != nil {
		t.Fatalf("replica storage: %v", err)
	}
	return ctx, repo, service.NewFileService(primary, repo, nil), primary, replica, dsn
}

func replicationScopeBoundSpace(t *testing.T, ctx context.Context, repo repository.Repository) spaces.Space {
	t.Helper()
	if err := repo.UpsertTenant(ctx, repository.TenantRecord{TenantID: "default", Status: "active"}); err != nil {
		t.Fatalf("upsert tenant: %v", err)
	}
	store, ok := repo.(spaces.Store)
	if !ok {
		t.Fatal("repository does not implement spaces.Store")
	}
	space, err := store.CreateSpace(ctx, spaces.CreateCommand{
		TenantID: "default", ID: uuid.NewString(), Kind: spaces.Team, Name: "replication scope",
		Actor:     spaces.SubjectRef{Source: "local", Kind: spaces.User, ID: "owner"},
		CommandID: uuid.NewString(), Reason: "scope test", RequestID: "scope-request",
	})
	if err != nil {
		t.Fatalf("create space: %v", err)
	}
	return space
}

func replicationScopeAssertRefused(t *testing.T, repo repository.Repository, objectID int64,
	primary, replica *replicationScopeStorage, tagger *replicationScopeTagger) {
	t.Helper()
	if primary.gets != 0 {
		t.Fatalf("primary read %d times after refusal", primary.gets)
	}
	if replica.puts != 0 {
		t.Fatalf("replica written %d times after refusal", replica.puts)
	}
	if tagger.calls != 0 {
		t.Fatalf("status tagger called %d times after refusal", tagger.calls)
	}
	obj, err := repo.GetObjectByID(context.Background(), objectID)
	if err != nil {
		t.Fatalf("reload object: %v", err)
	}
	if obj.Tags[TagStatus] != "" {
		t.Fatalf("refused replication wrote %s=%q", TagStatus, obj.Tags[TagStatus])
	}
}

func TestReplicateObjectByIDRejectsBoundBucketBeforeSideEffects(t *testing.T) {
	ctx, repo, svc, primary, replica, _ := newReplicationScopeFixture(t)
	space := replicationScopeBoundSpace(t, ctx, repo)
	obj, err := svc.Put(ctx, "default", space.BackingBucket, "bound.txt",
		strings.NewReader("bound body"), 10, service.PutOptions{})
	if err != nil {
		t.Fatalf("put bound object: %v", err)
	}
	primaryScope := &replicationScopeStorage{Storage: primary}
	replicaScope := &replicationScopeStorage{Storage: replica}
	tagger := &replicationScopeTagger{}
	worker := NewWorker(repo, primaryScope, replicaScope, nil, replicationScopeLogger()).
		WithObjectTagger(tagger)

	err = worker.ReplicateObjectByID(access.SystemContext(ctx, "default"), obj.ID)
	if !errors.Is(err, access.ErrSpaceBound) || !access.IsResourceBoundaryDenied(err) {
		t.Fatalf("bound bucket error = %v, want explicit Space denial", err)
	}
	replicationScopeAssertRefused(t, repo, obj.ID, primaryScope, replicaScope, tagger)
}

func TestReplicateObjectByIDRejectsBoundaryFaultBeforeSideEffects(t *testing.T) {
	ctx, repo, svc, primary, replica, _ := newReplicationScopeFixture(t)
	obj, err := svc.Put(ctx, "default", "default", "fault.txt",
		strings.NewReader("body"), 4, service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	boom := errors.New("binding store unavailable")
	fault := replicationScopeBindingFunc(func(context.Context, string, string) (spaces.BindingResult, error) {
		return spaces.BindingResult{}, boom
	})
	cases := []struct {
		name   string
		reader spaces.BindingReader
		cause  error
	}{
		{"reader fault", fault, boom},
		{"missing reader", nil, nil},
		{"typed-nil reader", replicationScopeBindingFunc(nil), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			primaryScope := &replicationScopeStorage{Storage: primary}
			replicaScope := &replicationScopeStorage{Storage: replica}
			tagger := &replicationScopeTagger{}
			worker := NewWorker(repo, primaryScope, replicaScope, nil, replicationScopeLogger()).
				WithObjectTagger(tagger).
				WithResourceBoundary(tc.reader)

			err := worker.ReplicateObjectByID(access.SystemContext(ctx, "default"), obj.ID)
			if !errors.Is(err, access.ErrResourceBoundaryFault) {
				t.Fatalf("boundary fault = %v, want fault", err)
			}
			if access.IsResourceBoundaryDenied(err) {
				t.Fatalf("boundary fault classified as ordinary denial: %v", err)
			}
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatalf("boundary fault = %v, want cause %v", err, tc.cause)
			}
			replicationScopeAssertRefused(t, repo, obj.ID, primaryScope, replicaScope, tagger)
		})
	}
}

func TestReplicateObjectByIDRejectsInexactJobTenantBeforeSideEffects(t *testing.T) {
	ctx, repo, svc, primary, replica, _ := newReplicationScopeFixture(t)
	obj, err := svc.Put(ctx, "default", "default", "tenant.txt",
		strings.NewReader("body"), 4, service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	for _, tenant := range []string{"other-tenant", "", "*"} {
		t.Run("tenant="+tenant, func(t *testing.T) {
			primaryScope := &replicationScopeStorage{Storage: primary}
			replicaScope := &replicationScopeStorage{Storage: replica}
			tagger := &replicationScopeTagger{}
			worker := NewWorker(repo, primaryScope, replicaScope, nil, replicationScopeLogger()).
				WithObjectTagger(tagger)
			err := worker.ReplicateObjectByID(access.SystemContext(ctx, tenant), obj.ID)
			if err == nil || !strings.Contains(err.Error(), "tenant") {
				t.Fatalf("job tenant %q error = %v, want tenant mismatch", tenant, err)
			}
			replicationScopeAssertRefused(t, repo, obj.ID, primaryScope, replicaScope, tagger)
		})
	}
}

func TestReplicateObjectByIDRejectsMalformedStoredScopeBeforeSideEffects(t *testing.T) {
	ctx, repo, _, primary, replica, _ := newReplicationScopeFixture(t)
	invalid, err := repo.InsertObjectVersion(ctx, repository.Object{
		TenantID: "default", Bucket: "", Key: "invalid-scope.txt",
		Backend: "local", StorageKey: "default//invalid-scope.txt", Size: 1,
	})
	if err != nil {
		t.Fatalf("insert malformed row: %v", err)
	}
	primaryScope := &replicationScopeStorage{Storage: primary}
	replicaScope := &replicationScopeStorage{Storage: replica}
	tagger := &replicationScopeTagger{}
	worker := NewWorker(repo, primaryScope, replicaScope, nil, replicationScopeLogger()).
		WithObjectTagger(tagger)

	err = worker.ReplicateObjectByID(ctx, invalid.ID)
	if !errors.Is(err, access.ErrInvalidArgument) {
		t.Fatalf("malformed row error = %v, want invalid scope", err)
	}
	replicationScopeAssertRefused(t, repo, invalid.ID, primaryScope, replicaScope, tagger)
}

func TestReplicateObjectByIDUnboundControlCopiesExactly(t *testing.T) {
	ctx, repo, svc, primary, replica, _ := newReplicationScopeFixture(t)
	obj, err := svc.Put(ctx, "default", "default", "control.txt",
		strings.NewReader("control body"), 12, service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	primaryScope := &replicationScopeStorage{Storage: primary}
	replicaScope := &replicationScopeStorage{Storage: replica}
	worker := NewWorker(repo, primaryScope, replicaScope, nil, replicationScopeLogger()).
		WithObjectTagger(svc)

	if err := worker.ReplicateObjectByID(access.SystemContext(ctx, "default"), obj.ID); err != nil {
		t.Fatalf("unbound control replication: %v", err)
	}
	if primaryScope.gets != 1 || replicaScope.puts != 1 {
		t.Fatalf("control effects: primary reads=%d replica writes=%d", primaryScope.gets, replicaScope.puts)
	}
	rc, _, err := replica.Get(ctx, obj.StorageKey)
	if err != nil {
		t.Fatalf("replica get: %v", err)
	}
	defer rc.Close()
	body, _ := io.ReadAll(rc)
	if string(body) != "control body" {
		t.Fatalf("replica body = %q", body)
	}
	tagged, err := repo.GetObjectByID(ctx, obj.ID)
	if err != nil || tagged.Tags[TagStatus] != "replicated" {
		t.Fatalf("control status = %v err=%v, want replicated", tagged.Tags, err)
	}
}

func TestResyncSkipsBoundRowsAndCountsSuccessSeparately(t *testing.T) {
	ctx, repo, svc, queue := newResyncFixture(t)
	space := replicationScopeBoundSpace(t, ctx, repo)
	if _, err := svc.Put(ctx, "default", space.BackingBucket, "bound.txt",
		strings.NewReader("bound"), 5, service.PutOptions{}); err != nil {
		t.Fatalf("put bound object: %v", err)
	}
	normal, err := svc.Put(ctx, "default", "default", "normal.txt",
		strings.NewReader("normal"), 6, service.PutOptions{})
	if err != nil {
		t.Fatalf("put normal object: %v", err)
	}

	counts := NewResyncer(repo, queue, 0, replicationScopeLogger()).sweepDetailed(ctx)
	if counts.scanned != 2 || counts.enqueued != 1 || counts.skipped != 1 || counts.faults != 0 {
		t.Fatalf("resync counts = %+v, want scanned=2 enqueued=1 skipped=1 faults=0", counts)
	}
	pending := pendingReplicationJobs(t, ctx, repo)
	if len(pending) != 1 || pending[0].TenantID != normal.TenantID || pending[0].DedupeKey != fmt.Sprintf("replicate:%d", normal.ID) {
		t.Fatalf("pending jobs = %+v, want only normal object %d", pending, normal.ID)
	}
}

func TestResyncBoundaryFaultRefusesEnqueueAndCountsFault(t *testing.T) {
	ctx, repo, svc, queue := newResyncFixture(t)
	if _, err := svc.Put(ctx, "default", "default", "fault.txt",
		strings.NewReader("body"), 4, service.PutOptions{}); err != nil {
		t.Fatalf("put: %v", err)
	}
	boom := errors.New("binding store unavailable")
	reader := replicationScopeBindingFunc(func(context.Context, string, string) (spaces.BindingResult, error) {
		return spaces.BindingResult{}, boom
	})
	counts := NewResyncer(repo, queue, 0, replicationScopeLogger()).
		WithResourceBoundary(reader).
		sweepDetailed(ctx)
	if counts.scanned != 1 || counts.enqueued != 0 || counts.skipped != 0 || counts.faults != 1 {
		t.Fatalf("resync counts = %+v, want scanned=1 enqueued=0 skipped=0 faults=1", counts)
	}
	if jobs := pendingReplicationJobs(t, ctx, repo); len(jobs) != 0 {
		t.Fatalf("boundary fault enqueued %d jobs", len(jobs))
	}
}

func TestResyncRejectsMalformedListedRowBeforeEnqueue(t *testing.T) {
	ctx, repo, svc, _, _, dsn := newReplicationScopeFixture(t)
	if _, err := svc.Put(ctx, "default", "default", "normal.txt",
		strings.NewReader("body"), 4, service.PutOptions{}); err != nil {
		t.Fatalf("put normal object: %v", err)
	}
	// A real SQLite row with a non-positive primary key and a valid list scope:
	// the repository's normal writer cannot produce it, so insert it through a
	// second connection and verify the sweep refuses it before enqueue.
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	defer raw.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := raw.ExecContext(ctx, `INSERT INTO objects
(id, tenant_id, bucket, key, version_id, backend, storage_key, size, etag, content_type, metadata, tags, storage_class, created_at, updated_at, deleted_at, locked_until)
VALUES (-7,'default','default','malformed.txt','vbad','local','default/default/malformed.txt',1,'','text/plain','{}','{}','STANDARD',?,?,NULL,NULL)`, now, now); err != nil {
		t.Fatalf("insert malformed row: %v", err)
	}
	queue := jobs.NewQueue(repo)
	counts := NewResyncer(repo, queue, 0, replicationScopeLogger()).sweepDetailed(ctx)
	if counts.scanned != 2 || counts.enqueued != 1 || counts.skipped != 0 || counts.faults != 1 {
		t.Fatalf("resync counts = %+v, want scanned=2 enqueued=1 skipped=0 faults=1", counts)
	}
	pending := pendingReplicationJobs(t, ctx, repo)
	if len(pending) != 1 {
		t.Fatalf("pending jobs = %+v, want one normal job", pending)
	}
	if id, err := DecodeObjectID(pending[0].Payload); err != nil || id == -7 {
		t.Fatalf("malformed row reached enqueue: job=%+v decoded=%d err=%v", pending[0], id, err)
	}
}

func TestResyncDedupeRemainsDistinctFromSuccess(t *testing.T) {
	ctx, repo, svc, queue := newResyncFixture(t)
	obj, err := svc.Put(ctx, "default", "default", "dedupe.txt",
		strings.NewReader("body"), 4, service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, deduped, err := queue.Enqueue(ctx, replicateJob(obj.TenantID, obj.ID)); err != nil || deduped {
		t.Fatalf("seed job: deduped=%v err=%v", deduped, err)
	}
	counts := NewResyncer(repo, queue, 0, replicationScopeLogger()).sweepDetailed(ctx)
	if counts.scanned != 1 || counts.enqueued != 0 || counts.skipped != 1 || counts.faults != 0 {
		t.Fatalf("resync counts = %+v, want scanned=1 enqueued=0 skipped=1 faults=0", counts)
	}
	if jobs := pendingReplicationJobs(t, ctx, repo); len(jobs) != 1 {
		t.Fatalf("pending jobs=%d, want 1 after dedupe", len(jobs))
	}
}
