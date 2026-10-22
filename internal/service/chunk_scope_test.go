package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestScopedChunkFixturePreservesCallback(t *testing.T) {
	ctx := context.Background()
	ref := access.ChunkRef{TenantID: "alpha", Bucket: "docs", Key: "key", ObjectID: 7}
	callbackErr := errors.New("original callback failure")
	calls := 0
	cleaner := &mockChunkCleaner{fn: func(gotCtx context.Context, id int64) error {
		calls++
		if gotCtx != ctx || id != ref.ObjectID {
			t.Fatalf("callback context/ID changed: %v %d", gotCtx, id)
		}
		return callbackErr
	}}
	if err := cleaner.DeleteScopedObjectChunks(ctx, ref); !errors.Is(err, callbackErr) || calls != 1 {
		t.Fatalf("callback error=%v calls=%d", err, calls)
	}
	if err := cleaner.DeleteScopedObjectChunks(ctx, access.ChunkRef{}); err == nil || calls != 1 {
		t.Fatalf("invalid fixture ref invoked callback: err=%v calls=%d", err, calls)
	}
}

func TestScopedChunkBucketPreservesAbortAndBlobDedupOrder(t *testing.T) {
	svc, repo := newTestSvc(t)
	versions := seedChunkScopeSharedBlob(t, svc, repo)
	ctx := context.Background()
	upload, err := svc.InitMultipart(ctx, "alpha", "docs", "pending-upload", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	store := &bucketChunkScopeStorage{Storage: svc.store}
	svc.store = store
	cleaner := &chunkScopeCleaner{before: func(context.Context, access.ChunkRef) {
		store.sequence = append(store.sequence, "chunks")
	}}
	svc.WithChunkCleaner(cleaner)
	if err := svc.DeleteBucket(ctx, "alpha", "docs"); err != nil {
		t.Fatal(err)
	}
	assertChunkScopeRefs(t, cleaner, versions[1], versions[0])
	if want := []string{"abort", "chunks", "blob", "chunks"}; !reflect.DeepEqual(store.sequence, want) {
		t.Fatalf("bucket cleanup order=%v want=%v", store.sequence, want)
	}
	if _, err := repo.GetUpload(ctx, upload.ID); !errors.Is(err, repository.ErrUploadNotFound) {
		t.Fatalf("multipart row survived bucket deletion: %v", err)
	}
	assertChunkScopeDeleteState(t, svc, repo, versions, "bucket")
}

func TestChunkCleanupCapabilities(t *testing.T) {
	legacy := &legacyChunkScopeCleaner{}
	var typedNil *chunkScopeCleaner
	var legacyNil *legacyChunkScopeCleaner
	obj := repository.Object{ID: 7, TenantID: "alpha", Bucket: "docs", Key: "key"}
	for _, tc := range []struct {
		name    string
		cleaner ChunkCleaner
		fault   bool
	}{
		{"nil", nil, false}, {"legacy-only", legacy, true},
		{"typed-nil-scoped", typedNil, true}, {"typed-nil-legacy", legacyNil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := new(FileService).WithChunkCleaner(tc.cleaner)
			if svc.ChunkCleaner() != tc.cleaner {
				t.Fatal("configured cleaner was discarded")
			}
			err := svc.cleanObjectChunks(context.Background(), obj, chunkScopeRef(obj))
			if errors.Is(err, errScopedChunkCleanerRequired) != tc.fault {
				t.Fatalf("dispatch error=%v want fault=%v", err, tc.fault)
			}
			svc.WithChunkCleaner(nil)
			if svc.ChunkCleaner() != nil || svc.cleanObjectChunks(context.Background(), repository.Object{}, access.ChunkRef{}) != nil {
				t.Fatal("true nil must clear the configured cleaner")
			}
		})
	}
	if legacy.calls != 0 {
		t.Fatalf("legacy calls=%d", legacy.calls)
	}
}

func TestChunkCleanupRejectsIncompleteOrUnexpectedRef(t *testing.T) {
	obj := repository.Object{ID: 7, TenantID: "alpha", Bucket: "docs", Key: "nested/key@v-user", StorageKey: "unrelated/opaque"}
	for _, mutate := range []func(*repository.Object){
		func(o *repository.Object) { o.ID = 0 }, func(o *repository.Object) { o.ID = -1 },
		func(o *repository.Object) { o.TenantID = "" }, func(o *repository.Object) { o.Bucket = "" },
		func(o *repository.Object) { o.Key = "" }, func(o *repository.Object) { o.ID++ },
		func(o *repository.Object) { o.TenantID = "beta" }, func(o *repository.Object) { o.Bucket = "other" },
		func(o *repository.Object) { o.Key = "different" },
	} {
		cleaner := &chunkScopeCleaner{}
		svc := new(FileService).WithChunkCleaner(cleaner)
		bad := obj
		mutate(&bad)
		if err := svc.cleanObjectChunks(context.Background(), bad, chunkScopeRef(obj)); !errors.Is(err, access.ErrInvalidArgument) {
			t.Fatalf("invalid row=%+v error=%v", bad, err)
		}
		assertChunkScopeRefs(t, cleaner)
	}
	remoteErr := errors.New("remote unavailable")
	cleaner := &chunkScopeCleaner{err: remoteErr}
	svc := new(FileService).WithChunkCleaner(cleaner)
	if err := svc.cleanObjectChunks(context.Background(), obj, chunkScopeRef(obj)); !errors.Is(err, remoteErr) {
		t.Fatalf("remote error cause lost: %v", err)
	}
	assertChunkScopeRefs(t, cleaner, obj)
}

func TestScopedChunkCleanupDeletePaths(t *testing.T) {
	for _, mode := range []string{"hard", "soft", "version", "current-version", "quarantine", "historical-quarantine", "bucket"} {
		t.Run(mode, func(t *testing.T) {
			for _, fail := range []bool{false, true} {
				name := "success"
				if fail {
					name = "remote-failure"
				}
				t.Run(name, func(t *testing.T) { testScopedChunkDeletePath(t, mode, fail) })
			}
		})
	}
}

func testScopedChunkDeletePath(t *testing.T, mode string, fail bool) {
	t.Helper()
	svc, repo, versions := newChunkScopeService(t)
	var remoteErr error
	if fail {
		remoteErr = errors.New("remote scoped cleanup unavailable")
	}
	cleaner, logs := attachChunkScopeCleaner(t, svc, repo, versions, remoteErr)
	if err := runChunkScopeDelete(svc, versions, mode); err != nil {
		t.Fatalf("delete must continue after cleanup failure: %v", err)
	}
	switch mode {
	case "hard", "bucket":
		assertChunkScopeRefs(t, cleaner, versions[1], versions[0])
	case "version", "historical-quarantine":
		assertChunkScopeRefs(t, cleaner, versions[0])
	default:
		assertChunkScopeRefs(t, cleaner, versions[1])
	}
	wantWarnings := 0
	if fail {
		wantWarnings = len(cleaner.refs)
		if !strings.Contains(logs.String(), remoteErr.Error()) {
			t.Fatalf("remote failure not warned: %s", logs)
		}
	}
	if got := strings.Count(logs.String(), "level=WARN"); got != wantWarnings {
		t.Fatalf("warnings=%d want=%d: %s", got, wantWarnings, logs)
	}
	assertChunkScopeDeleteState(t, svc, repo, versions, mode)
}

func TestChunkCleanupCapabilityFaultWarnsAndContinues(t *testing.T) {
	legacy := &legacyChunkScopeCleaner{}
	var typedNil *chunkScopeCleaner
	for _, cleaner := range []ChunkCleaner{legacy, typedNil} {
		svc, repo, versions := newChunkScopeService(t)
		var logs bytes.Buffer
		svc.logger = slog.New(slog.NewTextHandler(&logs, nil))
		svc.WithChunkCleaner(cleaner)
		if err := runChunkScopeDelete(svc, versions, "soft"); err != nil {
			t.Fatalf("capability fault blocked soft delete: %v", err)
		}
		if !strings.Contains(logs.String(), "chunk cleanup on soft delete failed") ||
			!strings.Contains(logs.String(), errScopedChunkCleanerRequired.Error()) {
			t.Fatalf("missing configured-capability warning: %s", &logs)
		}
		assertChunkScopeDeleteState(t, svc, repo, versions, "soft")
	}
	if legacy.calls != 0 {
		t.Fatalf("legacy cleaner called %d times", legacy.calls)
	}
}

func TestScopedChunkCleanupMarkerUsesPreviousObject(t *testing.T) {
	svc, _, versions := newChunkScopeService(t)
	remoteErr := errors.New("marker remote failure")
	cleaner := &chunkScopeCleaner{err: remoteErr}
	var logs bytes.Buffer
	svc.logger = slog.New(slog.NewTextHandler(&logs, nil))
	svc.WithChunkCleaner(cleaner)
	ctx := context.Background()
	marker, err := svc.CreateDeleteMarker(ctx, "alpha", "docs", versions[1].Key)
	if err != nil {
		t.Fatal(err)
	}
	assertChunkScopeRefs(t, cleaner, versions[1])
	if marker.ID == versions[1].ID || !IsDeleteMarker(marker) {
		t.Fatalf("not a distinct marker: %+v", marker)
	}
	if !strings.Contains(logs.String(), "chunk cleanup on delete marker failed") || !strings.Contains(logs.String(), remoteErr.Error()) {
		t.Fatalf("marker failure not warned: %s", &logs)
	}
	cleaner.refs = nil
	if err := svc.DeleteVersion(ctx, "alpha", "docs", marker.Key, marker.VersionID); err != nil {
		t.Fatal(err)
	}
	assertChunkScopeRefs(t, cleaner)
	if _, err := svc.CreateDeleteMarker(ctx, "alpha", "docs", "never-present"); err != nil {
		t.Fatal(err)
	}
	assertChunkScopeRefs(t, cleaner)
}

func TestScopedChunkCleanupSkipsEnumeratedMarkers(t *testing.T) {
	for _, mode := range []string{"hard", "bucket"} {
		t.Run(mode, func(t *testing.T) {
			svc, repo, versions := newChunkScopeService(t)
			ctx := context.Background()
			marker, err := svc.CreateDeleteMarker(ctx, "alpha", "docs", versions[1].Key)
			if err != nil {
				t.Fatal(err)
			}
			cleaner, _ := attachChunkScopeCleaner(t, svc, repo, versions, nil)
			if mode == "hard" {
				err = svc.hardDeleteObject(ctx, marker, "alpha", "docs", marker.Key)
			} else {
				err = svc.DeleteBucket(ctx, "alpha", "docs")
			}
			if err != nil {
				t.Fatal(err)
			}
			assertChunkScopeRefs(t, cleaner, versions[1], versions[0])
			if _, err := repo.GetObjectByID(ctx, marker.ID); !errors.Is(err, repository.ErrNotFound) {
				t.Fatalf("marker row survived deletion: %v", err)
			}
		})
	}
}

func TestScopedChunkCleanupBlobFailureKeepsRowsAndUsage(t *testing.T) {
	for _, mode := range []string{"hard", "version", "bucket"} {
		t.Run(mode, func(t *testing.T) {
			svc, repo, versions := newChunkScopeService(t)
			_, _ = attachChunkScopeCleaner(t, svc, repo, versions, nil)
			store := svc.store.(chunkScopeStorage)
			blobErr := errors.New("blob delete unavailable")
			store.deleteErr = blobErr
			svc.store = store
			if err := runChunkScopeDelete(svc, versions, mode); !errors.Is(err, blobErr) {
				t.Fatalf("blob failure cause lost: %v", err)
			}
			for _, obj := range versions {
				if _, err := repo.GetObjectByID(context.Background(), obj.ID); err != nil {
					t.Fatalf("blob failure removed row: %v", err)
				}
				if _, err := svc.store.Stat(context.Background(), obj.StorageKey); err != nil {
					t.Fatalf("failed blob deletion removed data: %v", err)
				}
			}
			assertChunkScopeUsage(t, repo, 8, 2)
		})
	}
}

func TestScopedChunkCleanupDoesNotBypassProtection(t *testing.T) {
	for _, protection := range []string{"hold", "worm"} {
		t.Run(protection, func(t *testing.T) {
			svc, repo, versions := newChunkScopeService(t)
			ctx := context.Background()
			if protection == "hold" {
				if err := repo.PutLegalHold(ctx, repository.LegalHold{ObjectID: versions[0].ID, TenantID: "alpha", VersionID: versions[0].VersionID}); err != nil {
					t.Fatal(err)
				}
			} else if _, err := svc.SetObjectRetention(ctx, "alpha", "docs", versions[0].Key, versions[0].VersionID, "COMPLIANCE", time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			cleaner, _ := attachChunkScopeCleaner(t, svc, repo, versions, nil)
			if err := runChunkScopeDelete(svc, versions, "hard"); !errors.Is(err, ErrLocked) {
				t.Fatalf("protected hard delete: %v", err)
			}
			if err := runChunkScopeDelete(svc, versions, "version"); !errors.Is(err, ErrLocked) {
				t.Fatalf("protected version delete: %v", err)
			}
			assertChunkScopeRefs(t, cleaner)
			for _, obj := range versions {
				if _, err := svc.store.Stat(ctx, obj.StorageKey); err != nil {
					t.Fatalf("protected blob lost: %v", err)
				}
			}
		})
	}
}
