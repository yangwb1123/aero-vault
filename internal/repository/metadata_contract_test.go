package repository_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// TestSetObjectMetaKeysContractTable (FR-2) pins the deterministic
// pre-existing contract of the three metadata key methods, scenario by
// scenario. Unlike the concurrency tests (timing-amplified by nature), every
// row here is a non-probabilistic regression anchor that must hold on both
// dialects and on old and new code alike.
func TestSetObjectMetaKeysContractTable(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	if err := repo.CreateBucket(ctx, "default", "default"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	const key = "contract.txt"
	if _, err := repo.UpsertObject(ctx, repository.Object{
		TenantID: "default", Bucket: "default", Key: key,
		Backend: "local", StorageKey: "default/default/" + key, Size: 1, ETag: "e",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	cases := []struct {
		name string
		run  func() error
		want error
	}{
		{"missing object → SetObjectMetaKey ErrNotFound", func() error {
			return repo.SetObjectMetaKey(ctx, "default", "default", "nope.txt", "k", "v")
		}, repository.ErrNotFound},
		{"missing object → SetObjectMetaKeys ErrNotFound", func() error {
			return repo.SetObjectMetaKeys(ctx, "default", "default", "nope.txt", map[string]string{"k": "v"})
		}, repository.ErrNotFound},
		{"missing object → DeleteObjectMetaKey ErrNotFound", func() error {
			return repo.DeleteObjectMetaKey(ctx, "default", "default", "nope.txt", "k")
		}, repository.ErrNotFound},
		{"soft-deleted object → SetObjectMetaKey ErrNotFound", func() error {
			if err := repo.SoftDeleteObject(ctx, "default", "default", key); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := repo.RestoreObject(ctx, "default", "default", key); err != nil {
					t.Fatal(err)
				}
			}()
			return repo.SetObjectMetaKey(ctx, "default", "default", key, "k", "v")
		}, repository.ErrNotFound},
		{"soft-deleted object → SetObjectMetaKeys ErrNotFound", func() error {
			if err := repo.SoftDeleteObject(ctx, "default", "default", key); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := repo.RestoreObject(ctx, "default", "default", key); err != nil {
					t.Fatal(err)
				}
			}()
			return repo.SetObjectMetaKeys(ctx, "default", "default", key, map[string]string{"k": "v"})
		}, repository.ErrNotFound},
		{"soft-deleted object → DeleteObjectMetaKey ErrNotFound", func() error {
			if err := repo.SoftDeleteObject(ctx, "default", "default", key); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := repo.RestoreObject(ctx, "default", "default", key); err != nil {
					t.Fatal(err)
				}
			}()
			return repo.DeleteObjectMetaKey(ctx, "default", "default", key, "k")
		}, repository.ErrNotFound},
		{"empty patch → nil even for missing object (zero DB access)", func() error {
			return repo.SetObjectMetaKeys(ctx, "default", "default", "nope.txt", nil)
		}, nil},
		{"empty patch map → nil even for missing object (zero DB access)", func() error {
			return repo.SetObjectMetaKeys(ctx, "default", "default", "nope.txt", map[string]string{})
		}, nil},
		{"delete key on empty metadata → nil", func() error {
			return repo.DeleteObjectMetaKey(ctx, "default", "default", key, "absent")
		}, nil},
		{"delete missing key → nil", func() error {
			if err := repo.SetObjectMetaKey(ctx, "default", "default", key, "present", "1"); err != nil {
				return err
			}
			return repo.DeleteObjectMetaKey(ctx, "default", "default", key, "never-set")
		}, nil},
		{"set same value again → nil (idempotent, row matched)", func() error {
			return repo.SetObjectMetaKey(ctx, "default", "default", key, "present", "1")
		}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if tc.want == nil && err != nil {
				t.Fatalf("got %v, want nil", err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}

	// The soft-delete subtests restore the object; leave a clean final state.
	if _, err := repo.GetObject(ctx, "default", "default", key); err != nil {
		t.Fatalf("object should be live after contract table: %v", err)
	}
}

// TestSetObjectMetaKeysPreservesUnpatchedKeys (AC-3) pins merge fidelity and
// the empty-string-vs-null semantics: patches overwrite only their own keys,
// unpatched keys survive, "" is a value (not an RFC 7396 null delete), and
// keys with JSON-path-reserved characters round-trip through set → get →
// delete exactly.
func TestSetObjectMetaKeysPreservesUnpatchedKeys(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	if err := repo.CreateBucket(ctx, "default", "default"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	const key = "merge-fidelity.txt"
	if _, err := repo.UpsertObject(ctx, repository.Object{
		TenantID: "default", Bucket: "default", Key: key,
		Backend: "local", StorageKey: "default/default/" + key, Size: 1, ETag: "e",
		Metadata: map[string]string{"seed": "0"},
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := repo.SetObjectMetaKeys(ctx, "default", "default", key, map[string]string{
		"a": "1", "b": "2",
	}); err != nil {
		t.Fatalf("set keys: %v", err)
	}
	obj, err := repo.GetObject(ctx, "default", "default", key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if obj.Metadata["seed"] != "0" {
		t.Fatalf("unpatched key lost: seed=%q", obj.Metadata["seed"])
	}
	if obj.Metadata["a"] != "1" || obj.Metadata["b"] != "2" {
		t.Fatalf("patched keys wrong: %v", obj.Metadata)
	}

	// Empty string is a value, not a delete.
	if err := repo.SetObjectMetaKey(ctx, "default", "default", key, "empty", ""); err != nil {
		t.Fatalf("set empty value: %v", err)
	}
	obj, _ = repo.GetObject(ctx, "default", "default", key)
	if v, ok := obj.Metadata["empty"]; !ok || v != "" {
		t.Fatalf("empty-string key should persist as value: %q present=%v", v, ok)
	}

	// JSON-path reserved / arbitrary character keys round-trip exactly.
	weird := []string{
		"with space", "quote\"in", "back\\slash", "line\nbreak",
		"carriage\rreturn", "tab\there", "ctl\x01char",
		"a.b$c[d]", "", "ключ-unicode", "emoji-🔑",
	}
	for _, k := range weird {
		if err := repo.SetObjectMetaKey(ctx, "default", "default", key, k, "w"); err != nil {
			t.Fatalf("set weird key %q: %v", k, err)
		}
	}
	obj, err = repo.GetObject(ctx, "default", "default", key)
	if err != nil {
		t.Fatalf("get after weird keys: %v", err)
	}
	for _, k := range weird {
		if obj.Metadata[k] != "w" {
			t.Fatalf("weird key %q lost after set (len=%d)", k, len(obj.Metadata))
		}
	}
	for _, k := range weird {
		if err := repo.DeleteObjectMetaKey(ctx, "default", "default", key, k); err != nil {
			t.Fatalf("delete weird key %q: %v", k, err)
		}
	}
	obj, err = repo.GetObject(ctx, "default", "default", key)
	if err != nil {
		t.Fatalf("get after weird deletes: %v", err)
	}
	for _, k := range weird {
		if _, ok := obj.Metadata[k]; ok {
			t.Fatalf("weird key %q survived delete", k)
		}
	}
	if obj.Metadata["seed"] != "0" || obj.Metadata["a"] != "1" {
		t.Fatalf("unrelated keys damaged by deletes: %v", obj.Metadata)
	}
}
