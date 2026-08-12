package reconcile

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

type repositoryChunkCleaner struct {
	repo repository.Repository
}

func (c repositoryChunkCleaner) DeleteObjectChunks(ctx context.Context, objectID int64) error {
	return c.repo.DeleteChunksForObject(ctx, objectID)
}

func TestScrubCorruptionRemovesSearchChunks(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(t.TempDir(), "objects")})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	fileService := service.NewFileService(store, repo, nil).WithAuthorizer(allowAllAuthz{})

	body := "trusted content"
	digest := md5.Sum([]byte(body))
	object, err := fileService.Put(
		ctx, "", "", "scrubbed.txt", strings.NewReader(body), int64(len(body)),
		service.PutOptions{ContentMD5: base64.StdEncoding.EncodeToString(digest[:])},
	)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := repo.InsertChunks(ctx, []repository.Chunk{{
		ObjectID: object.ID, TenantID: object.TenantID, Bucket: object.Bucket,
		ObjectKey: object.Key, Content: body,
	}}); err != nil {
		t.Fatalf("insert chunks: %v", err)
	}
	tampered := "altered content"
	if _, err := store.Put(
		ctx, object.StorageKey, strings.NewReader(tampered), int64(len(tampered)), storage.PutOptions{},
	); err != nil {
		t.Fatalf("tamper storage: %v", err)
	}
	job := New(
		repo, store, 0, false, 0, []string{"default"},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	).WithChunkCleaner(repositoryChunkCleaner{repo: repo})
	if err := job.scrubObject(ctx, object); err == nil {
		t.Fatal("expected corruption result")
	}
	chunks, err := repo.ListChunksForObject(ctx, object.ID)
	if err != nil {
		t.Fatalf("list chunks: %v", err)
	}
	if len(chunks) != 0 {
		t.Fatalf("corrupt object retained %d chunks", len(chunks))
	}
	reloaded, err := repo.GetObjectByID(ctx, object.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Metadata["_aero_scrub_status"] != "corrupt" {
		t.Fatalf("metadata=%v", reloaded.Metadata)
	}
}

// TestScrub_ClearsFlagWhenIntact seeds an object, corrupts its blob, scrubs it
// (flag set), restores the blob to match the recorded MD5, then scrubs again:
// the _aero_scrub_status flag must be removed and Get must no longer return
// ErrObjectCorrupt. On HEAD (no clear path) the second scrub leaves the flag
// and the final Get fails — this test is the regression guard for the fix.
func TestScrub_ClearsFlagWhenIntact(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(t.TempDir(), "objects")})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	fileService := service.NewFileService(store, repo, nil).WithAuthorizer(allowAllAuthz{})
	body := "trusted content"
	digest := md5.Sum([]byte(body))
	object, err := fileService.Put(
		ctx, "", "", "scrubbed.txt", strings.NewReader(body), int64(len(body)),
		service.PutOptions{ContentMD5: base64.StdEncoding.EncodeToString(digest[:])},
	)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	job := New(repo, store, 0, false, 0, []string{"default"}, newSilentLogger()).
		WithChunkCleaner(repositoryChunkCleaner{repo: repo})

	// ① 篡改 blob → scrub → DB 中标记 = corrupt
	tampered := "altered content"
	if _, err := store.Put(ctx, object.StorageKey, strings.NewReader(tampered), int64(len(tampered)), storage.PutOptions{}); err != nil {
		t.Fatalf("tamper storage: %v", err)
	}
	if err := job.scrubObject(ctx, object); err == nil {
		t.Fatal("expected corruption result")
	}
	reloaded, err := repo.GetObjectByID(ctx, object.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Metadata["_aero_scrub_status"] != "corrupt" {
		t.Fatalf("flag not set: %v", reloaded.Metadata)
	}

	// ② 恢复 blob（与 _aero_content_md5 一致）→ 重取对象 → 再次 scrub → 标记清除
	//    必须经 GetObjectByID 重取：Put 返回的 object 是写入时的快照，其
	//    Metadata 不含后来写入的 corrupt 标记，直接复用会使清除守卫恒 false。
	if _, err := store.Put(ctx, object.StorageKey, strings.NewReader(body), int64(len(body)), storage.PutOptions{}); err != nil {
		t.Fatalf("restore blob: %v", err)
	}
	reloaded, err = repo.GetObjectByID(ctx, object.ID)
	if err != nil {
		t.Fatalf("reload after restore: %v", err)
	}
	if err := job.scrubObject(ctx, reloaded); err != nil {
		t.Fatalf("scrub after repair: %v", err)
	}
	reloaded, err = repo.GetObjectByID(ctx, object.ID)
	if err != nil {
		t.Fatalf("reload after clear: %v", err)
	}
	if _, still := reloaded.Metadata["_aero_scrub_status"]; still {
		t.Fatalf("flag not cleared: %v", reloaded.Metadata)
	}

	// ③ Get 不再返回 ErrObjectCorrupt（HEAD 上此步必败）
	rc, _, err := fileService.Get(ctx, "", "", "scrubbed.txt")
	if err != nil {
		t.Fatalf("Get after repair: %v", err)
	}
	rc.Close()
}

// TestScrub_ClearFlagFailureKeepsFlag pins the FR-1 failure contract: when the
// row disappears between verification and flag removal (concurrent hard delete
// / retention), DeleteObjectMetaKey returns ErrNotFound — the scrub must warn
// and return nil, never misreport the object as corrupt again.
func TestScrub_ClearFlagFailureKeepsFlag(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(t.TempDir(), "objects")})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	fileService := service.NewFileService(store, repo, nil).WithAuthorizer(allowAllAuthz{})
	body := "trusted content"
	digest := md5.Sum([]byte(body))
	object, err := fileService.Put(
		ctx, "", "", "scrubbed.txt", strings.NewReader(body), int64(len(body)),
		service.PutOptions{ContentMD5: base64.StdEncoding.EncodeToString(digest[:])},
	)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	job := New(repo, store, 0, false, 0, []string{"default"}, newSilentLogger()).
		WithChunkCleaner(repositoryChunkCleaner{repo: repo})

	// ① seed + 篡改 + scrubObject → corrupt；然后重取（快照陷阱同 AC-1）
	tampered := "altered content"
	if _, err := store.Put(ctx, object.StorageKey, strings.NewReader(tampered), int64(len(tampered)), storage.PutOptions{}); err != nil {
		t.Fatalf("tamper storage: %v", err)
	}
	if err := job.scrubObject(ctx, object); err == nil {
		t.Fatal("expected corruption result")
	}
	flagged, err := repo.GetObjectByID(ctx, object.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if flagged.Metadata["_aero_scrub_status"] != "corrupt" {
		t.Fatalf("flag not set: %v", flagged.Metadata)
	}

	// ② 行消失（并发硬删）
	if err := repo.HardDeleteObjectByID(ctx, object.ID); err != nil {
		t.Fatalf("hard delete: %v", err)
	}

	// ③ 恢复 blob（内容与 _aero_content_md5 一致）
	if _, err := store.Put(ctx, object.StorageKey, strings.NewReader(body), int64(len(body)), storage.PutOptions{}); err != nil {
		t.Fatalf("restore blob: %v", err)
	}

	// ④ 完好分支 → 守卫 true → DeleteObjectMetaKey 返回 ErrNotFound →
	//    warn 且 err == nil（不得误报 corrupt）
	if err := job.scrubObject(ctx, flagged); err != nil {
		t.Fatalf("scrub after row removal: %v", err)
	}
}
