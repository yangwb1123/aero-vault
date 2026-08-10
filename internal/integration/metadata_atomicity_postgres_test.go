//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// TestSetObjectMetaKeysPostgresConcurrentMerge (AC-1c) runs the AC-1a
// disjoint-key concurrent-merge shape on real Postgres (8+8 keys × 25
// barrier-synchronized rounds) and additionally pins the dialect-level
// RowsAffected contract on PG command tags: missing row → ErrNotFound,
// same-value re-set → nil, delete of a missing key → nil.
func TestSetObjectMetaKeysPostgresConcurrentMerge(t *testing.T) {
	repo, _ := freshRepo(t)
	ctx := context.Background()
	if err := repo.CreateBucket(ctx, "default", "default"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	const key = "pg-merge.txt"
	if _, err := repo.UpsertObject(ctx, repository.Object{
		TenantID: "default", Bucket: "default", Key: key,
		Backend: "local", StorageKey: "default/default/" + key, Size: 1, ETag: "e",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	const rounds = 25
	for round := 0; round < rounds; round++ {
		start := make(chan struct{})
		var wg sync.WaitGroup
		for w := 0; w < 2; w++ {
			// Keys unique per round so a lost update in ANY round leaves
			// permanent evidence (deterministic red side).
			patch := map[string]string{}
			for i := 0; i < 8; i++ {
				patch[fmt.Sprintf("w%d_k%d_r%d", w, i, round)] = fmt.Sprintf("v%d", w)
			}
			wg.Add(1)
			go func(p map[string]string) {
				defer wg.Done()
				<-start
				if err := repo.SetObjectMetaKeys(ctx, "default", "default", key, p); err != nil {
					t.Errorf("round %d SetObjectMetaKeys: %v", round, err)
				}
			}(patch)
		}
		close(start)
		wg.Wait()
	}

	obj, err := repo.GetObject(ctx, "default", "default", key)
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	for w := 0; w < 2; w++ {
		for i := 0; i < 8; i++ {
			for r := 0; r < rounds; r++ {
				k := fmt.Sprintf("w%d_k%d_r%d", w, i, r)
				if _, ok := obj.Metadata[k]; !ok {
					t.Errorf("lost update: metadata missing key %q (len=%d want %d)",
						k, len(obj.Metadata), 16*rounds)
				}
			}
		}
	}

	// PG RowsAffected contract: UPDATE command tags count matched rows.
	if err := repo.SetObjectMetaKey(ctx, "default", "default", "pg-missing.txt", "k", "v"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("missing object: got %v, want ErrNotFound", err)
	}
	if err := repo.SetObjectMetaKey(ctx, "default", "default", key, "w0_k0", "v0"); err != nil {
		t.Errorf("same-value re-set: got %v, want nil", err)
	}
	if err := repo.DeleteObjectMetaKey(ctx, "default", "default", key, "never-set"); err != nil {
		t.Errorf("delete missing key: got %v, want nil", err)
	}
}
