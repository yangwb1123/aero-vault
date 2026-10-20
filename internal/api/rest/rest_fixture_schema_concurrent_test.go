package rest

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

type restFixtureConcurrentResult struct {
	index  int
	path   string
	digest [sha256.Size]byte
	repo   repository.Repository
	err    error
}

func startRESTFixtureCopy(cache *restFixtureSchemaCache, index int, path string) restFixtureConcurrentResult {
	result := restFixtureConcurrentResult{index: index, path: path}
	ctx := context.Background()
	if result.err = cache.seed(ctx, path); result.err != nil {
		return result
	}
	image, err := os.ReadFile(path)
	if err != nil {
		result.err = err
		return result
	}
	result.digest = sha256.Sum256(image)
	result.repo, result.err = repository.Open(ctx, "sqlite", "file:"+path)
	if result.err != nil {
		return result
	}
	if result.err = result.repo.Migrate(ctx); result.err != nil {
		return result
	}
	result.err = result.repo.CreateBucket(ctx, "default", fmt.Sprintf("case-%d", index))
	return result
}

func registerRESTFixtureConcurrentCleanup(t *testing.T, result restFixtureConcurrentResult) {
	t.Helper()
	if result.repo != nil {
		t.Cleanup(func() {
			if err := result.repo.Close(); err != nil {
				t.Errorf("close concurrent case %d: %v", result.index, err)
			}
		})
	}
}

func TestRESTFixtureSchemaConcurrentFirstUse(t *testing.T) {
	const count = 8
	var cache restFixtureSchemaCache
	start := make(chan struct{})
	results := make(chan restFixtureConcurrentResult, count)
	for i := 0; i < count; i++ {
		path := filepath.Join(t.TempDir(), "x.db")
		go func(index int, path string) {
			<-start
			results <- startRESTFixtureCopy(&cache, index, path)
		}(i, path)
	}
	close(start)
	copies := make([]restFixtureConcurrentResult, 0, count)
	for i := 0; i < count; i++ {
		result := <-results
		registerRESTFixtureConcurrentCleanup(t, result)
		if result.err != nil {
			t.Errorf("concurrent case %d: %v", result.index, result.err)
		}
		copies = append(copies, result)
	}
	if t.Failed() {
		return
	}
	digest := sha256.Sum256(cache.image)
	for _, result := range copies {
		assertRESTFixtureConcurrentCopy(t, copies[0], result, digest)
	}
	if err := copies[0].repo.Close(); err != nil {
		t.Fatal(err)
	}
	for _, result := range copies[1:] {
		if err := result.repo.Ping(context.Background()); err != nil {
			t.Errorf("closing another copy broke case %d: %v", result.index, err)
		}
	}
}

func assertRESTFixtureConcurrentCopy(t *testing.T, first, result restFixtureConcurrentResult, digest [sha256.Size]byte) {
	t.Helper()
	if result.digest != digest {
		t.Errorf("case %d did not receive the single complete cold image", result.index)
	}
	if result.index != first.index {
		firstInfo, errA := os.Stat(first.path)
		info, errB := os.Stat(result.path)
		if err := errors.Join(errA, errB); err != nil {
			t.Fatal(err)
		}
		if filepath.Dir(first.path) == filepath.Dir(result.path) || result.repo == first.repo || os.SameFile(firstInfo, info) {
			t.Fatal("concurrent copies share a directory, file or repository connection")
		}
	}
	want := []string{fmt.Sprintf("case-%d", result.index)}
	got, err := result.repo.ListBuckets(context.Background(), "default")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("case %d received another copy's write: buckets=%v err=%v", result.index, got, err)
	}
}
