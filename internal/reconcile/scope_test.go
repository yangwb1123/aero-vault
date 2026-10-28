package reconcile

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
	"github.com/aero-vault/aero-vault/internal/storage"
)

const reconcileScopeSpaceID = "12345678-1234-1234-1234-123456789abc"

type reconcileBindingReader struct {
	bound  bool
	err    error
	calls  int
	tenant string
	bucket string
}

func (r *reconcileBindingReader) ProbeSpaceBinding(_ context.Context, tenant, bucket string) (spaces.BindingResult, error) {
	r.calls++
	r.tenant, r.bucket = tenant, bucket
	if r.err != nil {
		return spaces.BindingResult{}, r.err
	}
	result := spaces.BindingResult{TenantID: tenant, Bucket: bucket, TenantKnown: true,
		TenantStatus: "active", State: spaces.BindingUnbound}
	if r.bound {
		result.State = spaces.BindingBound
		result.SpaceID = reconcileScopeSpaceID
		result.Revision = 1
	}
	return result, nil
}

type faultGetStorage struct {
	storage.Storage
}

func (faultGetStorage) Get(context.Context, string) (io.ReadCloser, storage.ObjectInfo, error) {
	return nil, storage.ObjectInfo{}, errors.New("storage get fault")
}

type faultListStorage struct {
	storage.Storage
}

func (faultListStorage) List(context.Context, string, string, int) (storage.ListResult, error) {
	return storage.ListResult{}, errors.New("storage list fault")
}

func TestScopeMalformedObjectAndUploadFault(t *testing.T) {
	state := newScopeState(nil)
	outcome, err := state.admitObject(context.Background(), repository.Object{
		TenantID: "tenant", Bucket: "bucket", Key: "key",
	}, "", "")
	scopeRequire(t, outcome == scopeFault && errors.Is(err, errScopeRow) && state.faults == 1, "malformed object outcome=%v err=%v faults=%d", outcome, err, state.faults)
	outcome, err = state.admitUpload(context.Background(), repository.Upload{
		ID: "upload", TenantID: "tenant", Bucket: "bucket", Key: "key",
		BackendUID: "backend", StorageKey: "",
	})
	scopeRequire(t, outcome == scopeFault && errors.Is(err, errScopeRow) && state.faults == 2, "malformed upload outcome=%v err=%v faults=%d", outcome, err, state.faults)
}

func TestJobBoundOrFaultRowIsNotSoftDeleted(t *testing.T) {
	ctx := context.Background()
	insertOrphan := func(t *testing.T) (repository.Repository, storage.Storage, repository.Object) {
		repo := openTestRepo(t)
		store := openTestStore(t)
		if err := repo.CreateBucket(ctx, "scope", "bucket"); err != nil {
			t.Fatalf("create bucket: %v", err)
		}
		obj := insertRow(t, repo, "scope", "bucket", "orphan.txt", "scope/bucket/orphan.txt")
		return repo, store, obj
	}
	repo, store, obj := insertOrphan(t)
	reader := &reconcileBindingReader{bound: true}
	job := New(repo, store, time.Minute, false, time.Minute, []string{"scope"}, newSilentLogger()).
		WithResourceBoundary(reader)
	job.sweep(ctx)
	if _, err := repo.GetObject(ctx, "scope", "bucket", "orphan.txt"); err != nil {
		t.Fatalf("bound row was soft-deleted: %v", err)
	}
	scopeRequire(t, job.scope.refused > 0 && reader.calls > 0, "bound row not observed: refused=%d calls=%d", job.scope.refused, reader.calls)

	repo, store, _ = insertOrphan(t)
	fault := &reconcileBindingReader{err: errors.New("binding provider down")}
	job = New(repo, store, time.Minute, false, time.Minute, []string{"scope"}, newSilentLogger()).
		WithResourceBoundary(fault)
	job.sweep(ctx)
	if _, err := repo.GetObject(ctx, "scope", "bucket", "orphan.txt"); err != nil {
		t.Fatalf("fault row was soft-deleted: %v", err)
	}
	scopeRequire(t, job.scope.faults > 0, "binding fault was counted as success")
	_ = obj
}

func TestLifecycleAndRetentionBoundRowsAreNotPurged(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	store := openTestStore(t)
	if err := repo.CreateBucket(ctx, "scope", "bucket"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	sk := "scope/bucket/active.txt"
	putBlob(t, store, sk)
	active := insertRow(t, repo, "scope", "bucket", "active.txt", sk)
	reader := &reconcileBindingReader{bound: true}
	lifecycle := NewLifecycle(repo, store, time.Minute, newSilentLogger()).WithResourceBoundary(reader)
	scopeRequire(t, !lifecycle.handleExpiredObject(ctx, active, "hard_delete"), "lifecycle purged a bound row")
	if _, err := repo.GetObject(ctx, "scope", "bucket", active.Key); err != nil {
		t.Fatalf("lifecycle removed bound row: %v", err)
	}

	deletedKey := "scope/bucket/deleted.txt"
	deletedSK := "scope/bucket/deleted.txt"
	putBlob(t, store, deletedSK)
	insertRow(t, repo, "scope", "bucket", deletedKey, deletedSK)
	if err := repo.SoftDeleteObject(ctx, "scope", "bucket", deletedKey); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	retention := NewRetention(repo, store, time.Minute, -time.Hour, newSilentLogger()).
		WithResourceBoundary(reader)
	candidates, err := repo.ListSoftDeletedBefore(ctx, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), 10)
	scopeRequire(t, err == nil && len(candidates) == 1, "expected expired retention candidate: %v %v", candidates, err)
	retention.purgeSoftDeleted(ctx)
	scopeRequire(t, retention.scope.refused == 1, "retention did not refuse expired bound candidate: %+v", retention.scope)
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	remaining, err := repo.ListSoftDeletedBefore(ctx, future, 10)
	if err != nil {
		t.Fatalf("list soft deleted: %v", err)
	}
	scopeRequire(t, len(remaining) == 1, "retention purged bound row, remaining=%d", len(remaining))
}

func TestUploadGCRejectsMalformedAndBoundUploads(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	store := openTestStore(t)
	init, err := store.InitMultipart(ctx, "scope/bucket/upload.bin", storage.PutOptions{})
	if err != nil {
		t.Fatalf("init multipart: %v", err)
	}
	upload := repository.Upload{
		ID: init.UploadID, TenantID: "scope", Bucket: "bucket", Key: "upload.bin",
		Backend: "local", BackendUID: init.UploadID, StorageKey: init.Key,
	}
	if err := repo.CreateUpload(ctx, upload); err != nil {
		t.Fatalf("create upload: %v", err)
	}
	reader := &reconcileBindingReader{bound: true}
	job := NewUploadGC(repo, store, time.Minute, time.Hour, newSilentLogger()).
		WithResourceBoundary(reader)
	malformed := upload
	malformed.StorageKey = ""
	purged := job.purgeUploads(ctx, []repository.Upload{malformed})
	scopeRequire(t, purged == 0, "malformed upload purged=%d", purged)
	purged = job.purgeUploads(ctx, []repository.Upload{upload})
	scopeRequire(t, purged == 0, "bound upload purged=%d", purged)
	if _, err := repo.GetUpload(ctx, upload.ID); err != nil {
		t.Fatalf("bound/malformed upload metadata disappeared: %v", err)
	}
	scopeRequire(t, job.scope.faults > 0 && job.scope.refused > 0, "upload outcomes faults=%d refused=%d", job.scope.faults, job.scope.refused)

	clean := NewUploadGC(repo, store, time.Minute, time.Hour, newSilentLogger())
	purged = clean.purgeUploads(ctx, []repository.Upload{upload})
	scopeRequire(t, purged == 1, "clean upload purged=%d", purged)
	if _, err := repo.GetUpload(ctx, upload.ID); !errors.Is(err, repository.ErrUploadNotFound) {
		t.Fatalf("upload still present after clean purge: %v", err)
	}
}

func TestScrubFaultDoesNotCountScannedOrCorrupt(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	store := openTestStore(t)
	if err := repo.CreateBucket(ctx, "scope", "bucket"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	sk := "scope/bucket/scrub.txt"
	putBlob(t, store, sk)
	insertRow(t, repo, "scope", "bucket", "scrub.txt", sk)
	digest := md5.Sum([]byte("hello"))
	if err := repo.SetObjectMetaKey(ctx, "scope", "bucket", "scrub.txt", "_aero_content_md5",
		base64.StdEncoding.EncodeToString(digest[:])); err != nil {
		t.Fatalf("set md5: %v", err)
	}
	job := New(repo, faultGetStorage{Storage: store}, time.Minute, false, time.Minute,
		[]string{"scope"}, newSilentLogger())
	scanned, corrupt := job.scrubAll(ctx, "scope", scrubSettings{enabled: true, batch: 10})
	scopeRequire(t, scanned == 0 && corrupt == 0 && job.scope.faults > 0, "scrub fault scanned=%d corrupt=%d faults=%d", scanned, corrupt, job.scope.faults)
}

func TestOrphanBlobIncompleteListingDoesNotDelete(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	store := openTestStore(t)
	if err := repo.CreateBucket(ctx, "scope", "bucket"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	orphan := "scope/bucket/orphan.bin"
	putBlob(t, store, orphan)
	job := New(repo, faultListStorage{Storage: store}, time.Minute, true, 0,
		[]string{"scope"}, newSilentLogger())
	orphans, deleted := job.sweepOrphanBlobs(ctx, "scope")
	scopeRequire(t, orphans == 0 && deleted == 0 && job.scope.faults > 0, "incomplete listing orphans=%d deleted=%d faults=%d", orphans, deleted, job.scope.faults)
	if _, err := store.Stat(ctx, orphan); err != nil {
		t.Fatalf("orphan blob deleted after incomplete listing: %v", err)
	}
}

func scopeRequire(t *testing.T, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Fatalf(format, args...)
	}
}

// The double changes observations only; writes still use the disk repository.
type scopeObservationRepo struct {
	repository.Repository
	versions func(context.Context, string, string, string) ([]repository.Object, error)
	fault    error
}

func (r scopeObservationRepo) ListObjectVersions(ctx context.Context, tenant, bucket, key string) ([]repository.Object, error) {
	return r.versions(ctx, tenant, bucket, key)
}
func (r scopeObservationRepo) SoftDeleteObject(context.Context, string, string, string) error {
	return r.fault
}
func (r scopeObservationRepo) SetObjectMetaKey(context.Context, string, string, string, string, string) error {
	return r.fault
}
func (r scopeObservationRepo) DeleteObjectMetaKey(context.Context, string, string, string, string) error {
	return r.fault
}

type scopeObservationStore struct {
	storage.Storage
	list func(context.Context, string, string, int) (storage.ListResult, error)
	get  func(context.Context, string) (io.ReadCloser, storage.ObjectInfo, error)
}

func (s scopeObservationStore) List(ctx context.Context, prefix, marker string, limit int) (storage.ListResult, error) {
	return s.list(ctx, prefix, marker, limit)
}
func (s scopeObservationStore) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
	return s.get(ctx, key)
}

type scopeCloseReader struct {
	io.ReadCloser
	close func() error
}

func (r scopeCloseReader) Close() error { return errors.Join(r.ReadCloser.Close(), r.close()) }

func TestScopeConfiguredUnknownOrphanNeverDeleted(t *testing.T) {
	ctx := context.Background()
	repo, store := openTestRepo(t), openTestStore(t)
	putBlob(t, store, "scope/unknown/orphan@vopaque")
	coverage := repo.(spaces.CoverageReader)
	observation, err := coverage.ProbeSpaceCoverage(ctx, spaces.CoverageScope{Kind: spaces.CoverageTenant, TenantID: "scope"})
	scopeRequire(t, err == nil && observation.Complete && observation.State == spaces.CoverageUnbound, "all-unbound control: %v %v", observation, err)
	job := New(repo, store, time.Minute, true, 0, []string{"scope"}, newSilentLogger()).WithResourceBoundary(&reconcileBindingReader{}).WithOrphanCoverage(coverage)
	orphans, deleted := job.sweepOrphanBlobs(ctx, "scope")
	scopeRequire(t, orphans == 0 && deleted == 0 && job.scope.faults > 0, "unsafe configured orphan work: %d %d %+v", orphans, deleted, job.scope)
	if _, err := store.Stat(ctx, "scope/unknown/orphan@vopaque"); err != nil {
		t.Fatalf("unknown ownership deleted: %v", err)
	}
	scopeRequire(t, access.IsResourceBoundaryFault(job.ValidateResourceBoundary()), "unsafe orphan-delete preflight passed")
	job.deleteOrphanBlobs = false
	scopeRequire(t, job.ValidateResourceBoundary() == nil, "healthy configured preflight failed")
	job.scope.resetCounts()
	orphans, deleted = job.sweepOrphanBlobs(ctx, "scope")
	scopeRequire(t, orphans == 1 && deleted == 0 && job.scope.faults == 0, "healthy aggregate observation failed")
	job.WithOrphanCoverage(nil)
	scopeRequire(t, access.IsResourceBoundaryFault(job.ValidateResourceBoundary()), "explicit missing coverage preflight passed")
}

func scopeRequireFault(t *testing.T, successful bool, state *scopeState) {
	t.Helper()
	scopeRequire(t, !successful && state.faults > 0, "fault became success: %+v", state)
}

func TestScopePersistedObjectProofBeforeEffects(t *testing.T) {
	for _, effect := range []string{"reconcile", "lifecycle", "retention", "scrub"} {
		t.Run(effect, func(t *testing.T) {
			ctx := context.Background()
			repo, store := openTestRepo(t), openTestStore(t)
			obj := insertRow(t, repo, "scope", "bucket", "key", "scope/bucket/key")
			forged := obj
			forged.StorageKey = "other/private/victim"
			putBlob(t, store, forged.StorageKey)
			switch effect {
			case "reconcile":
				j := New(repo, store, time.Minute, false, 0, nil, newSilentLogger()).WithResourceBoundary(&reconcileBindingReader{})
				orphan, counted := j.reconcileObjectRow(ctx, "scope", "bucket", forged)
				scopeRequireFault(t, orphan || counted, &j.scope)
			case "lifecycle":
				j := NewLifecycle(repo, store, time.Minute, newSilentLogger()).WithResourceBoundary(&reconcileBindingReader{})
				scopeRequireFault(t, j.handleExpiredObject(ctx, forged, "hard_delete"), &j.scope)
			case "retention":
				j := NewRetention(repo, store, time.Minute, time.Hour, newSilentLogger()).WithResourceBoundary(&reconcileBindingReader{})
				scopeRequireFault(t, j.purgeOneSoftDeleted(ctx, forged), &j.scope)
			case "scrub":
				j := New(repo, store, time.Minute, false, 0, nil, newSilentLogger()).WithResourceBoundary(&reconcileBindingReader{})
				result, err := j.scrubObjectResult(ctx, forged, "scope", "bucket")
				scopeRequire(t, result == scrubScopeFault && errors.Is(err, errScopeRow), "forged scrub: %v %v", result, err)
			}
			_, err := repo.GetObjectByID(ctx, obj.ID)
			scopeRequire(t, err == nil, "actual row lost: %v", err)
			_, err = store.Stat(ctx, forged.StorageKey)
			scopeRequire(t, err == nil, "unowned blob lost: %v", err)
		})
	}
}

func TestScopeEveryConsumedVersionEnumerationProved(t *testing.T) {
	for _, corruptCall := range []int{1, 2} {
		t.Run(itoa(corruptCall), func(t *testing.T) {
			ctx := context.Background()
			repo, store := openTestRepo(t), openTestStore(t)
			obj := insertRow(t, repo, "scope", "bucket", "key", "scope/bucket/key")
			putBlob(t, store, obj.StorageKey)
			forged := obj
			forged.StorageKey = "other/private/blob"
			putBlob(t, store, forged.StorageKey)
			calls := 0
			wrapped := scopeObservationRepo{Repository: repo, versions: func(ctx context.Context, tenant, bucket, key string) ([]repository.Object, error) {
				calls++
				if calls == corruptCall {
					return []repository.Object{forged}, nil
				}
				return repo.ListObjectVersions(ctx, tenant, bucket, key)
			}}
			job := NewRetention(wrapped, store, time.Minute, time.Hour, newSilentLogger()).WithResourceBoundary(&reconcileBindingReader{})
			scopeRequire(t, !job.purgeOneSoftDeleted(ctx, obj) && job.scope.faults > 0, "unproved helper enumeration purged")
			_, err := store.Stat(ctx, forged.StorageKey)
			scopeRequire(t, err == nil, "unproved blob lost: %v", err)
			_, err = repo.GetObjectByID(ctx, obj.ID)
			scopeRequire(t, err == nil, "actual row lost: %v", err)
		})
	}
}

func TestScopeStoragePaginationMustBeComplete(t *testing.T) {
	for _, mode := range []string{"empty_marker", "repeated_marker", "duplicate", "wrong_prefix", "partial_error", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			repo, store := openTestRepo(t), openTestStore(t)
			key := "scope/bucket/orphan"
			putBlob(t, store, key)
			calls := 0
			wrapped := scopeObservationStore{Storage: store, list: func(context.Context, string, string, int) (storage.ListResult, error) {
				calls++
				return scopeStoragePage(mode, key, calls, cancel)
			}}
			job := New(repo, wrapped, time.Minute, true, 0, []string{"scope"}, newSilentLogger())
			orphans, deleted := job.sweepOrphanBlobs(ctx, "scope")
			scopeRequire(t, orphans == 0 && deleted == 0 && job.scope.faults > 0, "partial evidence: orphans=%d deleted=%d faults=%d", orphans, deleted, job.scope.faults)
			_, err := store.Stat(context.Background(), key)
			scopeRequire(t, err == nil, "blob lost: %v", err)
		})
	}
}

func scopeStoragePage(mode, key string, calls int, cancel context.CancelFunc) (storage.ListResult, error) {
	page := storage.ListResult{Objects: []storage.ObjectInfo{{Key: key, LastModified: time.Now()}}}
	if mode == "wrong_prefix" {
		page.Objects[0].Key = "other/private/blob"
	}
	if mode == "partial_error" {
		return page, errors.New("partial storage list")
	}
	if mode == "canceled" {
		cancel()
	}
	if calls == 1 && mode == "empty_marker" {
		page.HasMore = true
	}
	if calls <= 2 && mode == "repeated_marker" {
		page.HasMore, page.NextMarker = true, "opaque-token"
	}
	if calls == 1 && mode == "duplicate" {
		page.HasMore, page.NextMarker = true, "opaque-token"
	}
	return page, nil
}

func TestScopeFailedEffectsAreNotSuccessfulOutcomes(t *testing.T) {
	for _, mode := range []string{"soft_delete", "close", "cancel_close", "mark", "clear"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			repo, store := openTestRepo(t), openTestStore(t)
			obj := insertRow(t, repo, "scope", "bucket", "key", "scope/bucket/key")
			cause := errors.New("effect fault")
			wrapped := scopeObservationRepo{Repository: repo, fault: cause}
			job := New(wrapped, store, time.Minute, false, 0, nil, newSilentLogger()).WithResourceBoundary(&reconcileBindingReader{})
			if mode == "soft_delete" {
				orphan, counted := job.reconcileObjectRow(ctx, "scope", "bucket", obj)
				scopeRequireFault(t, orphan || counted, &job.scope)
				return
			}
			putBlob(t, store, obj.StorageKey)
			digest := md5.Sum([]byte("hello"))
			if mode == "mark" {
				digest = md5.Sum([]byte("different"))
			}
			scopeRequire(t, repo.SetObjectMetaKey(ctx, "scope", "bucket", "key", "_aero_content_md5", base64.StdEncoding.EncodeToString(digest[:])) == nil, "seed digest")
			if mode == "clear" {
				scopeRequire(t, repo.SetObjectMetaKey(ctx, "scope", "bucket", "key", "_aero_scrub_status", "corrupt") == nil, "seed corrupt flag")
			}
			obj, err := repo.GetObjectByID(ctx, obj.ID)
			scopeRequire(t, err == nil, "read seeded row: %v", err)
			job.store = scopeObservationStore{Storage: store, get: func(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
				rc, info, err := store.Get(ctx, key)
				return scopeCloseReader{ReadCloser: rc, close: func() error {
					if mode == "cancel_close" {
						cancel()
						return nil
					}
					if mode == "close" {
						return cause
					}
					return nil
				}}, info, err
			}}
			result, err := job.scrubObjectResult(ctx, obj, "scope", "bucket")
			want := cause
			if mode == "cancel_close" {
				want = context.Canceled
			}
			scopeRequire(t, result == scrubFault && errors.Is(err, want), "fault became verification: %v %v", result, err)
		})
	}
}

func TestScopeEmptyConfiguredTimersFaultAndPreflight(t *testing.T) {
	for _, reader := range []spaces.BindingReader{nil, (*reconcileBindingReader)(nil)} {
		repo, store := openTestRepo(t), openTestStore(t)
		j := New(repo, store, time.Minute, false, 0, nil, newSilentLogger()).WithResourceBoundary(reader)
		l := NewLifecycle(repo, store, time.Minute, newSilentLogger()).WithResourceBoundary(reader)
		r := NewRetention(repo, store, time.Minute, time.Hour, newSilentLogger()).WithResourceBoundary(reader)
		u := NewUploadGC(repo, store, time.Minute, time.Hour, newSilentLogger()).WithResourceBoundary(reader)
		for _, timer := range []struct {
			job   interface{ ValidateResourceBoundary() error }
			scope *scopeState
			sweep func(context.Context)
		}{{j, &j.scope, j.sweep}, {l, &l.scope, l.sweep}, {r, &r.scope, r.sweep}, {u, &u.scope, u.sweep}} {
			timer.sweep(context.Background())
			scopeRequire(t, timer.scope.faults > 0, "empty configured %T silently succeeded", timer.job)
			scopeRequire(t, access.IsResourceBoundaryFault(timer.job.ValidateResourceBoundary()), "missing/faulty preflight on %T", timer.job)
		}
		j.scope.resetCounts()
		sc, corrupt := j.scrubAll(context.Background(), "scope", scrubSettings{enabled: true})
		scopeRequire(t, sc == 0 && corrupt == 0 && j.scope.faults > 0, "empty scrub hid missing reader")
	}
}

func TestScopeObjectPagesRejectIncompleteEvidence(t *testing.T) {
	repo := openTestRepo(t)
	obj := insertRow(t, repo, "scope", "bucket", "key", "scope/bucket/key")
	for _, page := range []repository.ListPage{
		{HasMore: true},
		{Objects: []repository.Object{obj}, HasMore: true},
		{Objects: []repository.Object{obj}, HasMore: true, NextMarker: "wrong"},
		{Objects: []repository.Object{obj, obj}},
	} {
		_, err := validateObjectPage(page, "scope", "bucket", "", "")
		scopeRequire(t, err != nil, "incomplete object page admitted: %+v", page)
	}
}

func TestScopeConflictingUploadsNeverCleaned(t *testing.T) {
	ctx := context.Background()
	repo, store := openTestRepo(t), openTestStore(t)
	init, err := store.InitMultipart(ctx, "scope/bucket/key", storage.PutOptions{})
	scopeRequire(t, err == nil, "init upload: %v", err)
	u := repository.Upload{ID: init.UploadID, TenantID: "scope", Bucket: "bucket", Key: "key", Backend: "local", BackendUID: init.UploadID, StorageKey: init.Key}
	scopeRequire(t, repo.CreateUpload(ctx, u) == nil, "create upload")
	forged := u
	forged.BackendUID = "unowned-upload"
	job := NewUploadGC(repo, store, time.Minute, time.Hour, newSilentLogger()).WithResourceBoundary(&reconcileBindingReader{})
	n := job.purgeUploads(ctx, mergeUploadCandidates([]repository.Upload{u}, []repository.Upload{forged}))
	scopeRequire(t, n == 0 && job.scope.faults > 0, "conflicting upload purged: %d %+v", n, job.scope)
	_, err = repo.GetUpload(ctx, u.ID)
	scopeRequire(t, err == nil, "upload lost: %v", err)
}
