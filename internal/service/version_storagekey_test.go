package service

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// On a versioned bucket, a PUT must derive its per-version storage key from the
// authoritative version_id (suffix == "@v"+version_id), and repeated writes to
// the same key must never collide onto a single blob. The previous scheme used
// time.Now().UnixNano() as the suffix, which both diverged from the real
// version_id and could collide for same-instant writes.
func TestPut_VersionedStorageKeyMatchesVersionID(t *testing.T) {
	ctx := context.Background()
	svc, _ := newQuotaTestSvc(t)
	const key = "report.bin"

	if err := svc.SetBucketVersioning(ctx, "default", "", true); err != nil {
		t.Fatal(err)
	}

	seen := map[string]string{} // storageKey -> versionID
	for i := 0; i < 5; i++ {
		body := []byte("payload")
		obj, err := svc.Put(ctx, "default", "", key, bytes.NewReader(body), int64(len(body)), PutOptions{})
		if err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
		if obj.VersionID == "" {
			t.Fatalf("put %d: versioned object must have a version_id", i)
		}
		// The storage key suffix must be exactly the authoritative version_id.
		if want := "@v" + obj.VersionID; !strings.HasSuffix(obj.StorageKey, want) {
			t.Fatalf("put %d: storage key %q must end with %q (suffix == version_id)", i, obj.StorageKey, want)
		}
		if prev, dup := seen[obj.StorageKey]; dup {
			t.Fatalf("put %d: storage key %q collided (already used by version %s)", i, obj.StorageKey, prev)
		}
		seen[obj.StorageKey] = obj.VersionID
	}

	versions, err := svc.ListVersions(ctx, "default", "", key)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 5 {
		t.Fatalf("want 5 distinct versions, got %d", len(versions))
	}
}
