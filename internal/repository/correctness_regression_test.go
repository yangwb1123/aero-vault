package repository_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestListObjectVersionsWithMarkerUsesDistinctBindings(t *testing.T) {
	ctx := context.Background()
	repo := openQuotaTestRepo(t)
	if err := repo.CreateBucket(ctx, "default", "versions"); err != nil {
		t.Fatal(err)
	}
	for _, versionID := range []string{"v-one", "v-two", "v-three"} {
		if _, err := repo.InsertObjectVersion(ctx, repository.Object{
			TenantID: "default", Bucket: "versions", Key: "doc.txt",
			VersionID: versionID, Backend: "local",
			StorageKey: "default/versions/doc.txt@" + versionID,
			Size:       1, ETag: versionID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	versions, err := repo.ListObjectVersions(ctx, "default", "versions", "doc.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 3 {
		t.Fatalf("versions=%d want 3", len(versions))
	}
	page, err := repo.ListObjectVersionsWithOpts(ctx, "default", "versions", "doc.txt", repository.VersionListOpts{
		VersionIDMarker: versions[0].VersionID,
		Limit:           2,
	})
	if err != nil {
		t.Fatalf("marker query failed: %v", err)
	}
	if len(page.Versions) == 0 {
		t.Fatal("marker query returned no versions")
	}
}

func TestReplaceObjectMetadataMissingReturnsNotFound(t *testing.T) {
	repo := openQuotaTestRepo(t)
	err := repo.ReplaceObjectMetadata(context.Background(), "default", "default", "missing.txt", map[string]string{"k": "v"})
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("error=%v want ErrNotFound", err)
	}
}

func TestListObjectsByTagScansPastSparseRawPage(t *testing.T) {
	ctx := context.Background()
	repo := openQuotaTestRepo(t)
	for _, key := range []string{"a-unmatched", "b-match", "c-match"} {
		if _, err := repo.UpsertObject(ctx, repository.Object{
			TenantID: "default", Bucket: "tags", Key: key,
			Backend: "local", StorageKey: "tags/" + key, ETag: key,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"b-match", "c-match"} {
		if err := repo.UpdateTags(ctx, "default", "tags", key, map[string]string{"team": "blue"}); err != nil {
			t.Fatal(err)
		}
	}

	first, err := repo.ListObjectsByTag(ctx, "default", "tags", "", "", 1, "team", "blue")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Objects) != 1 || first.Objects[0].Key != "b-match" || !first.HasMore {
		t.Fatalf("first tag page = %+v, want b-match with continuation", first)
	}
	second, err := repo.ListObjectsByTag(
		ctx, "default", "tags", "", first.NextMarker, 1, "team", "blue",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Objects) != 1 || second.Objects[0].Key != "c-match" || second.HasMore {
		t.Fatalf("second tag page = %+v, want final c-match", second)
	}
}
