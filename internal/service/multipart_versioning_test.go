package service

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

// On a versioned bucket, completing a multipart upload must create a NEW version
// (a distinct @v storage key) rather than overwriting the current object — and the
// prior version must remain readable.
func TestMultipart_CreatesVersionOnVersionedBucket(t *testing.T) {
	ctx := context.Background()
	svc, _ := newQuotaTestSvc(t)
	const key = "doc.bin"

	if err := svc.SetBucketVersioning(ctx, "default", "", true); err != nil {
		t.Fatal(err)
	}

	// v1 via a plain PUT.
	v1Body := []byte("first version via put")
	if _, err := svc.Put(ctx, "default", "", key, bytes.NewReader(v1Body), int64(len(v1Body)), PutOptions{}); err != nil {
		t.Fatal(err)
	}

	// v2 via multipart.
	up, err := svc.InitMultipart(ctx, "default", "", key, PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	v2Body := []byte("second version via multipart")
	if _, err := svc.UploadPart(ctx, up.ID, 1, bytes.NewReader(v2Body), int64(len(v2Body))); err != nil {
		t.Fatal(err)
	}
	mpObj, err := svc.CompleteMultipart(ctx, up.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mpObj.StorageKey != storageKey(mpObj.TenantID, mpObj.Bucket, key)+"@v"+mpObj.VersionID {
		t.Fatalf("multipart storage key/version mismatch: key=%q version=%q", mpObj.StorageKey, mpObj.VersionID)
	}

	// Two versions exist, with distinct storage keys (one blob per version).
	versions, err := svc.ListVersions(ctx, "default", "", key)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 {
		t.Fatalf("want 2 versions after put + multipart, got %d", len(versions))
	}
	keys := map[string]bool{}
	for _, v := range versions {
		if !strings.Contains(v.StorageKey, "@v") {
			t.Fatalf("versioned object should use an @v storage key, got %q", v.StorageKey)
		}
		keys[v.StorageKey] = true
	}
	if len(keys) != 2 {
		t.Fatalf("versions must have distinct storage keys, got %v", keys)
	}

	// The multipart object is the current version and reads back correctly.
	rc, _, err := svc.Get(ctx, "default", "", key)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(got, v2Body) {
		t.Fatalf("current version should be the multipart body, got %q", got)
	}

	// The prior (v1) version is still retrievable by its version id.
	var v1ID string
	for _, v := range versions {
		if v.VersionID != mpObj.VersionID {
			v1ID = v.VersionID
		}
	}
	rc1, _, err := svc.GetVersion(ctx, "default", "", key, v1ID)
	if err != nil {
		t.Fatalf("prior version should still be readable: %v", err)
	}
	got1, _ := io.ReadAll(rc1)
	_ = rc1.Close()
	if !bytes.Equal(got1, v1Body) {
		t.Fatalf("prior version content mismatch, got %q", got1)
	}
}
