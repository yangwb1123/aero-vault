package repository_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// seedMetaObject inserts one active object with empty metadata in a fresh
// "default" bucket and returns its key.
func seedMetaObject(t *testing.T, repo repository.Repository, key string) {
	t.Helper()
	ctx := context.Background()
	if err := repo.CreateBucket(ctx, "default", "default"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if _, err := repo.UpsertObject(ctx, repository.Object{
		TenantID: "default", Bucket: "default", Key: key,
		Backend: "local", StorageKey: "default/default/" + key, Size: 1, ETag: "e",
	}); err != nil {
		t.Fatalf("upsert object: %v", err)
	}
}

// TestSetObjectMetaKeysConcurrentMerge (AC-1a) races two writers holding
// disjoint key sets (8+8) against the same object for 25 barrier-synchronized
// rounds. The pre-fix SELECT→Go-merge→UPDATE implementation loses the earlier
// writer's keys (lost update); the single-statement in-DB merge must keep all
// 16 keys after every round.
func TestSetObjectMetaKeysConcurrentMerge(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	seedMetaObject(t, repo, "merge.txt")

	const rounds = 25
	for round := 0; round < rounds; round++ {
		start := make(chan struct{})
		var wg sync.WaitGroup
		for w := 0; w < 2; w++ {
			// Keys unique per round: a lost update in ANY round leaves
			// permanent evidence, making the red side deterministic
			// (fixed key names would let later rounds mask an early loss).
			patch := map[string]string{}
			for i := 0; i < 8; i++ {
				patch[fmt.Sprintf("w%d_k%d_r%d", w, i, round)] = fmt.Sprintf("v%d", w)
			}
			wg.Add(1)
			go func(p map[string]string) {
				defer wg.Done()
				<-start
				if err := repo.SetObjectMetaKeys(ctx, "default", "default", "merge.txt", p); err != nil {
					t.Errorf("round %d SetObjectMetaKeys: %v", round, err)
				}
			}(patch)
		}
		close(start)
		wg.Wait()
	}

	obj, err := repo.GetObject(ctx, "default", "default", "merge.txt")
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
}

// TestSetObjectMetaKeyScrubMarkerSurvivesConcurrentUserWrites (AC-1b) is the
// direction's motivating shape at the repository layer: the scrub worker writes
// the real `_aero_scrub_status=corrupt` marker while 8 user writers update
// metadata on the same object. The marker must survive every round — if the
// marker were lost, a corrupt object would stay unguarded.
func TestSetObjectMetaKeyScrubMarkerSurvivesConcurrentUserWrites(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	seedMetaObject(t, repo, "scrub.txt")

	const rounds = 25
	for round := 0; round < rounds; round++ {
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := repo.SetObjectMetaKey(ctx, "default", "default", "scrub.txt",
				"_aero_scrub_status", "corrupt"); err != nil {
				t.Errorf("round %d scrub marker write: %v", round, err)
			}
		}()
		for w := 0; w < 8; w++ {
			writer := w
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if err := repo.SetObjectMetaKey(ctx, "default", "default", "scrub.txt",
					fmt.Sprintf("user_%d_r%d", writer, round), fmt.Sprintf("v%d", writer)); err != nil {
					t.Errorf("round %d user write %d: %v", round, writer, err)
				}
			}()
		}
		close(start)
		wg.Wait()
	}

	obj, err := repo.GetObject(ctx, "default", "default", "scrub.txt")
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	if obj.Metadata["_aero_scrub_status"] != "corrupt" {
		t.Fatalf("scrub marker lost: _aero_scrub_status=%q (metadata len=%d)",
			obj.Metadata["_aero_scrub_status"], len(obj.Metadata))
	}
	for w := 0; w < 8; w++ {
		for r := 0; r < rounds; r++ {
			k := fmt.Sprintf("user_%d_r%d", w, r)
			if _, ok := obj.Metadata[k]; !ok {
				t.Errorf("user key %q missing (metadata len=%d)", k, len(obj.Metadata))
			}
		}
	}
}

// TestSetObjectMetaKeyConcurrentHardDelete (AC-2b) races metadata writes
// against a hard delete. Every observed error must be ErrNotFound (the
// pre-fix code silently returned nil on a 0-row UPDATE); a nil error is only
// legal for a write that landed before the delete.
func TestSetObjectMetaKeyConcurrentHardDelete(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()
	seedMetaObject(t, repo, "race-delete.txt")

	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var bad []error
	record := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		bad = append(bad, err)
	}
	for w := 0; w < 8; w++ {
		writer := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 25; i++ {
				err := repo.SetObjectMetaKey(ctx, "default", "default", "race-delete.txt",
					fmt.Sprintf("w%d_i%d", writer, i), "v")
				if err != nil && !errors.Is(err, repository.ErrNotFound) {
					record(err)
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		// Hard-delete once mid-flight; writers racing after it must observe
		// ErrNotFound, never a silent success on 0 affected rows.
		if err := repo.HardDeleteObject(ctx, "default", "default", "race-delete.txt"); err != nil {
			record(err)
		}
	}()
	close(start)
	wg.Wait()

	if len(bad) != 0 {
		t.Fatalf("unexpected errors from concurrent write/delete: %v", bad)
	}
	// Object must be gone: the delete itself never resurrects.
	if _, err := repo.GetObject(ctx, "default", "default", "race-delete.txt"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("object still present after hard delete: %v", err)
	}
}
