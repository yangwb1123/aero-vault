package reconcile

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// Keep all original repositoryChunkCleaner callers and ID callbacks intact.
func (c repositoryChunkCleaner) DeleteScopedObjectChunks(ctx context.Context, ref access.ChunkRef) error {
	if err := access.ValidateChunkRef(ref); err != nil {
		return err
	}
	return c.DeleteObjectChunks(ctx, ref.ObjectID)
}

type scopedReconcileCleaner struct {
	refs     []access.ChunkRef
	oldCalls int
	repo     repository.Repository
	err      error
	before   func(context.Context, access.ChunkRef)
}

func (c *scopedReconcileCleaner) DeleteObjectChunks(context.Context, int64) error {
	c.oldCalls++
	return errors.New("ID-only reconcile cleanup dispatched")
}

func (c *scopedReconcileCleaner) DeleteScopedObjectChunks(ctx context.Context, ref access.ChunkRef) error {
	if c.before != nil {
		c.before(ctx, ref)
	}
	c.refs = append(c.refs, ref)
	if c.err != nil || c.repo == nil {
		return c.err
	}
	return c.repo.DeleteChunksForObject(ctx, ref.ObjectID)
}

type legacyReconcileCleaner struct{ calls int }

func (c *legacyReconcileCleaner) DeleteObjectChunks(context.Context, int64) error {
	c.calls++
	return errors.New("legacy reconcile cleanup dispatched")
}

func reconcileChunkRef(obj repository.Object) access.ChunkRef {
	return access.ChunkRef{TenantID: obj.TenantID, Bucket: obj.Bucket, Key: obj.Key, ObjectID: obj.ID}
}

func TestScopedReconcileCapabilities(t *testing.T) {
	legacy := &legacyReconcileCleaner{}
	var typedNil *scopedReconcileCleaner
	var legacyNil *legacyReconcileCleaner
	obj := repository.Object{ID: 7, TenantID: "alpha", Bucket: "docs", Key: "nested/key@v-user"}
	for _, tc := range []struct {
		name    string
		cleaner ChunkCleaner
		fault   bool
	}{
		{"nil", nil, false}, {"legacy-only", legacy, true},
		{"typed-nil-scoped", typedNil, true}, {"typed-nil-legacy", legacyNil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := deleteScopedObjectChunks(context.Background(), tc.cleaner, obj)
			if errors.Is(err, errScopedChunkCleanerRequired) != tc.fault {
				t.Fatalf("dispatch error=%v want fault=%v", err, tc.fault)
			}
		})
	}
	if legacy.calls != 0 {
		t.Fatalf("legacy calls=%d", legacy.calls)
	}
	if err := deleteScopedObjectChunks(context.Background(), nil, repository.Object{}); err != nil {
		t.Fatalf("true nil must remain optional: %v", err)
	}
}

func TestScopedReconcileValidatesCompleteRow(t *testing.T) {
	obj := repository.Object{ID: 7, TenantID: "alpha", Bucket: "docs", Key: "nested/key@v-user", StorageKey: "opaque/not/scope"}
	for _, mutate := range []func(*repository.Object){
		func(o *repository.Object) { o.ID = 0 }, func(o *repository.Object) { o.ID = -1 },
		func(o *repository.Object) { o.TenantID = "" }, func(o *repository.Object) { o.Bucket = "" },
		func(o *repository.Object) { o.Key = "" },
	} {
		bad := obj
		mutate(&bad)
		cleaner := &scopedReconcileCleaner{}
		if err := deleteScopedObjectChunks(context.Background(), cleaner, bad); !errors.Is(err, access.ErrInvalidArgument) {
			t.Fatalf("invalid row=%+v error=%v", bad, err)
		}
		assertReconcileChunkRefs(t, cleaner)
	}
	remoteErr := errors.New("remote unavailable")
	cleaner := &scopedReconcileCleaner{err: remoteErr}
	if err := deleteScopedObjectChunks(context.Background(), cleaner, obj); !errors.Is(err, remoteErr) {
		t.Fatalf("remote error cause lost: %v", err)
	}
	assertReconcileChunkRefs(t, cleaner, obj)
}

func newReconcileChunkScope(t *testing.T) (repository.Repository, storage.Storage, []repository.Object) {
	t.Helper()
	repo, store := openTestRepo(t), openTestStore(t)
	ctx := context.Background()
	svc := service.NewFileService(store, repo, nil).WithAuthorizer(allowAllAuthz{})
	if err := svc.SetBucketVersioning(ctx, "alpha", "docs", true); err != nil {
		t.Fatal(err)
	}
	digest := md5.Sum([]byte("body"))
	var versions []repository.Object
	for range 2 {
		obj, err := svc.Put(ctx, "alpha", "docs", "nested/key@v-user", strings.NewReader("body"), 4,
			service.PutOptions{ContentMD5: base64.StdEncoding.EncodeToString(digest[:])})
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.InsertChunks(ctx, []repository.Chunk{{
			ObjectID: obj.ID, TenantID: obj.TenantID, Bucket: obj.Bucket, ObjectKey: obj.Key, Content: "body",
		}}); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, obj)
	}
	return repo, store, versions
}

func assertReconcileChunkRefs(t *testing.T, cleaner *scopedReconcileCleaner, objects ...repository.Object) {
	t.Helper()
	var want []access.ChunkRef
	for _, obj := range objects {
		want = append(want, reconcileChunkRef(obj))
	}
	if cleaner.oldCalls != 0 || !reflect.DeepEqual(cleaner.refs, want) {
		t.Fatalf("refs=%+v want=%+v legacy calls=%d", cleaner.refs, want, cleaner.oldCalls)
	}
}

type reconcileChunkStorage struct {
	storage.Storage
	beforeDelete func(context.Context, string)
}

func (s reconcileChunkStorage) Delete(ctx context.Context, key string) error {
	s.beforeDelete(ctx, key)
	return s.Storage.Delete(ctx, key)
}

func observeReconcileChunkOrder(
	t *testing.T, repo repository.Repository, store storage.Storage, versions []repository.Object,
	cleaner *scopedReconcileCleaner, bytes, objects int64,
) storage.Storage {
	t.Helper()
	cleaner.before = func(ctx context.Context, ref access.ChunkRef) {
		obj, err := repo.GetObjectByID(ctx, ref.ObjectID)
		if err != nil || reconcileChunkRef(obj) != ref {
			t.Fatalf("cleanup must use still-present exact row: %+v %+v %v", ref, obj, err)
		}
		if _, err := store.Stat(ctx, obj.StorageKey); err != nil {
			t.Fatalf("cleanup must precede blob deletion: %v", err)
		}
		assertReconcileChunkUsage(t, repo, bytes, objects)
	}
	return reconcileChunkStorage{Storage: store, beforeDelete: func(ctx context.Context, key string) {
		var deletedRef access.ChunkRef
		for _, obj := range versions {
			if _, err := repo.GetObjectByID(ctx, obj.ID); err != nil {
				t.Fatalf("blob deletion must precede row deletion: %v", err)
			}
			if obj.StorageKey == key {
				deletedRef = reconcileChunkRef(obj)
			}
		}
		found := false
		for _, ref := range cleaner.refs {
			found = found || ref == deletedRef
		}
		if !found {
			t.Fatalf("blob %q deleted before its scoped cleanup", key)
		}
		assertReconcileChunkUsage(t, repo, bytes, objects)
	}}
}

func assertReconcileChunkUsage(t *testing.T, repo repository.Repository, bytes, objects int64) {
	t.Helper()
	quota, err := repo.GetTenantQuota(context.Background(), "alpha")
	if err != nil || quota.UsedBytes != bytes || quota.UsedObjects != objects {
		t.Fatalf("quota=%+v err=%v want=%d bytes/%d objects", quota, err, bytes, objects)
	}
}

func TestScopedReconcileCleanupPaths(t *testing.T) {
	for _, mode := range []string{"key", "version", "soft", "retention", "lifecycle-soft", "lifecycle-hard", "scrub"} {
		t.Run(mode, func(t *testing.T) {
			for _, fail := range []bool{false, true} {
				name := "success"
				if fail {
					name = "remote-failure"
				}
				t.Run(name, func(t *testing.T) { testScopedReconcilePath(t, mode, fail) })
			}
		})
	}
}

func testScopedReconcilePath(t *testing.T, mode string, fail bool) {
	t.Helper()
	repo, store, versions := newReconcileChunkScope(t)
	initialBytes, initialObjects := int64(8), int64(2)
	if mode == "retention" {
		svc := service.NewFileService(store, repo, nil).WithAuthorizer(allowAllAuthz{})
		if err := svc.Delete(context.Background(), "alpha", "docs", versions[1].Key, false); err != nil {
			t.Fatal(err)
		}
		initialBytes, initialObjects = 4, 1
	}
	cleaner := &scopedReconcileCleaner{repo: repo}
	if fail {
		cleaner.err = errors.New("remote scoped cleanup unavailable")
	}
	store = observeReconcileChunkOrder(t, repo, store, versions, cleaner, initialBytes, initialObjects)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	runReconcileChunkPath(t, mode, repo, store, cleaner, versions, logger)
	want := []repository.Object{versions[1]}
	switch mode {
	case "key", "retention":
		want = []repository.Object{versions[1], versions[0]}
	case "version":
		want = []repository.Object{versions[0]}
	}
	assertReconcileChunkRefs(t, cleaner, want...)
	assertReconcileCleanupWarnings(t, logs.String(), cleaner.err, len(want))
	assertReconcileChunkState(t, mode, repo, store, versions)
	if !fail {
		assertReconcileChunksEmpty(t, repo, want)
	}
}

func assertReconcileCleanupWarnings(t *testing.T, logs string, remoteErr error, calls int) {
	t.Helper()
	warnings := strings.Count(logs, "chunk cleanup during reconcile delete failed")
	if remoteErr != nil {
		if warnings != calls || !strings.Contains(logs, remoteErr.Error()) {
			t.Fatalf("remote cleanup failure warnings=%d want=%d: %s", warnings, calls, logs)
		}
	} else if warnings != 0 {
		t.Fatalf("unexpected cleanup warnings: %s", logs)
	}
}

func assertReconcileChunksEmpty(t *testing.T, repo repository.Repository, objects []repository.Object) {
	t.Helper()
	for _, obj := range objects {
		chunks, err := repo.ListChunksForObject(context.Background(), obj.ID)
		if err != nil || len(chunks) != 0 {
			t.Fatalf("scoped cleanup left chunks: %+v %v", chunks, err)
		}
	}
}

func runReconcileChunkPath(t *testing.T, mode string, repo repository.Repository, store storage.Storage,
	cleaner ChunkCleaner, versions []repository.Object, logger *slog.Logger,
) {
	t.Helper()
	ctx := context.Background()
	current := versions[1]
	var err error
	switch mode {
	case "key":
		err = hardDeleteKey(ctx, repo, store, cleaner, current, logger)
	case "version":
		err = hardDeleteVersion(ctx, repo, store, cleaner, versions[0], logger)
	case "soft":
		err = softDeleteKey(ctx, repo, cleaner, current, logger)
	case "retention":
		NewRetention(repo, store, time.Minute, -time.Hour, logger).WithChunkCleaner(cleaner).purgeSoftDeleted(ctx)
	case "lifecycle-soft", "lifecycle-hard":
		runReconcileChunkLifecycle(t, mode, NewLifecycle(repo, store, time.Minute, logger).WithChunkCleaner(cleaner), current)
	case "scrub":
		runReconcileChunkScrub(t, New(repo, store, 0, false, 0, []string{"alpha"}, logger).WithChunkCleaner(cleaner), current)
	default:
		t.Fatal("unknown test mode")
	}
	if err != nil {
		t.Fatalf("cleanup failure blocked deletion: %v", err)
	}
}

func runReconcileChunkLifecycle(t *testing.T, mode string, job *LifecycleJob, obj repository.Object) {
	t.Helper()
	action := "soft_delete"
	if mode == "lifecycle-hard" {
		action = "hard_delete"
	}
	if !job.handleExpiredObject(context.Background(), obj, action) {
		t.Fatal("lifecycle did not complete deletion")
	}
}

func runReconcileChunkScrub(t *testing.T, job *Job, obj repository.Object) {
	t.Helper()
	ctx := context.Background()
	if _, err := job.store.Put(ctx, obj.StorageKey, strings.NewReader("tamper"), 6, storage.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := job.scrubObject(ctx, obj); err == nil {
		t.Fatal("scrub must report corruption even if cleanup fails")
	}
}

func assertReconcileChunkState(t *testing.T, mode string, repo repository.Repository, store storage.Storage, versions []repository.Object) {
	t.Helper()
	switch mode {
	case "key", "retention":
		for _, obj := range versions {
			assertReconcileChunkGone(t, repo, store, obj)
		}
		assertReconcileChunkUsage(t, repo, 0, 0)
	case "version":
		assertReconcileChunkGone(t, repo, store, versions[0])
		assertReconcileChunkUsage(t, repo, 4, 1)
	case "lifecycle-hard":
		assertReconcileChunkGone(t, repo, store, versions[1])
		assertReconcileChunkUsage(t, repo, 4, 1)
	case "soft", "lifecycle-soft":
		assertReconcileChunkSoftDeleted(t, repo, store, versions[1])
		assertReconcileChunkUsage(t, repo, 4, 1)
	case "scrub":
		assertReconcileChunkCorrupt(t, repo, versions[1])
		assertReconcileChunkUsage(t, repo, 8, 2)
	}
}

func assertReconcileChunkSoftDeleted(t *testing.T, repo repository.Repository, store storage.Storage, expected repository.Object) {
	t.Helper()
	obj, err := repo.GetObjectByID(context.Background(), expected.ID)
	if err != nil || obj.DeletedAt == nil {
		t.Fatalf("row not soft-deleted: %+v %v", obj, err)
	}
	if _, err := store.Stat(context.Background(), obj.StorageKey); err != nil {
		t.Fatalf("soft deletion removed blob: %v", err)
	}
}

func assertReconcileChunkCorrupt(t *testing.T, repo repository.Repository, expected repository.Object) {
	t.Helper()
	obj, err := repo.GetObjectByID(context.Background(), expected.ID)
	if err != nil || obj.Metadata["_aero_scrub_status"] != "corrupt" {
		t.Fatalf("scrub flag missing: %+v %v", obj, err)
	}
}

func assertReconcileChunkGone(t *testing.T, repo repository.Repository, store storage.Storage, obj repository.Object) {
	t.Helper()
	if _, err := repo.GetObjectByID(context.Background(), obj.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("row %d survived deletion: %v", obj.ID, err)
	}
	if _, err := store.Stat(context.Background(), obj.StorageKey); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("blob %q survived deletion: %v", obj.StorageKey, err)
	}
}

func TestScopedReconcileCapabilityFaultWarnsAndContinues(t *testing.T) {
	legacy := &legacyReconcileCleaner{}
	var typedNil *scopedReconcileCleaner
	for _, cleaner := range []ChunkCleaner{legacy, typedNil} {
		repo, store, versions := newReconcileChunkScope(t)
		var logs bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logs, nil))
		if err := hardDeleteKey(context.Background(), repo, store, cleaner, versions[1], logger); err != nil {
			t.Fatalf("configured capability fault blocked deletion: %v", err)
		}
		if strings.Count(logs.String(), errScopedChunkCleanerRequired.Error()) != 2 {
			t.Fatalf("missing per-version capability warnings: %s", &logs)
		}
		assertReconcileChunkState(t, "key", repo, store, versions)
	}
	if legacy.calls != 0 {
		t.Fatalf("legacy calls=%d", legacy.calls)
	}
}

func TestScopedReconcileSkipsMarkers(t *testing.T) {
	repo, store, versions := newReconcileChunkScope(t)
	ctx := context.Background()
	marker, err := repo.InsertDeleteMarker(ctx, repository.Object{
		TenantID: "alpha", Bucket: "docs", Key: versions[1].Key, Metadata: map[string]string{"_aero_delete_marker": "true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cleaner := &scopedReconcileCleaner{repo: repo}
	if err := hardDeleteVersion(ctx, repo, store, cleaner, marker, newSilentLogger()); err != nil {
		t.Fatal(err)
	}
	assertReconcileChunkRefs(t, cleaner)
	marker, err = repo.InsertDeleteMarker(ctx, repository.Object{
		TenantID: "alpha", Bucket: "docs", Key: versions[1].Key, Metadata: map[string]string{"_aero_delete_marker": "true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := hardDeleteKey(ctx, repo, store, cleaner, marker, newSilentLogger()); err != nil {
		t.Fatal(err)
	}
	assertReconcileChunkRefs(t, cleaner, versions[1], versions[0])
	assertReconcileChunkState(t, "key", repo, store, versions)
}

func TestScopedReconcileProtectionPrecedesCleanup(t *testing.T) {
	repo, store, versions := newReconcileChunkScope(t)
	ctx := context.Background()
	if err := repo.PutLegalHold(ctx, repository.LegalHold{ObjectID: versions[0].ID, TenantID: "alpha", VersionID: versions[0].VersionID}); err != nil {
		t.Fatal(err)
	}
	cleaner := &scopedReconcileCleaner{repo: repo}
	if err := hardDeleteKey(ctx, repo, store, cleaner, versions[1], newSilentLogger()); !errors.Is(err, errKeyProtected) {
		t.Fatalf("protected key deletion=%v", err)
	}
	if err := hardDeleteVersion(ctx, repo, store, cleaner, versions[0], newSilentLogger()); !errors.Is(err, errKeyProtected) {
		t.Fatalf("protected version deletion=%v", err)
	}
	assertReconcileChunkRefs(t, cleaner)
	for _, obj := range versions {
		if _, err := repo.GetObjectByID(ctx, obj.ID); err != nil {
			t.Fatalf("protected row lost: %v", err)
		}
		if _, err := store.Stat(ctx, obj.StorageKey); err != nil {
			t.Fatalf("protected blob lost: %v", err)
		}
	}
	assertReconcileChunkUsage(t, repo, 8, 2)
}
