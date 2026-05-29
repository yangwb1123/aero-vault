package auth

import (
	"context"
	"testing"
	"time"
)

// fakeStore is an in-memory PersistentStore for tests, keyed by token hash.
type fakeStore struct{ m map[string]PersistedKey }

func newFakeStore() *fakeStore { return &fakeStore{m: map[string]PersistedKey{}} }

func (f *fakeStore) PutAPIKey(_ context.Context, k PersistedKey) error {
	f.m[k.TokenHash] = k
	return nil
}
func (f *fakeStore) GetAPIKeyByHash(_ context.Context, hash string) (PersistedKey, bool, error) {
	k, ok := f.m[hash]
	return k, ok, nil
}
func (f *fakeStore) DeleteAPIKeyByHash(_ context.Context, hash string) (bool, error) {
	_, ok := f.m[hash]
	delete(f.m, hash)
	return ok, nil
}
func (f *fakeStore) ListAPIKeys(_ context.Context, tenant string) ([]PersistedKey, error) {
	out := make([]PersistedKey, 0, len(f.m))
	for _, k := range f.m {
		if tenant == "" || tenant == "*" || k.TenantID == tenant {
			out = append(out, k)
		}
	}
	return out, nil
}
func (f *fakeStore) TouchAPIKey(_ context.Context, hash, when string) error {
	if k, ok := f.m[hash]; ok {
		k.LastUsedAt = when
		f.m[hash] = k
	}
	return nil
}

func TestPersistedKey_AddLookupRevoke(t *testing.T) {
	ctx := context.Background()
	reg, _ := Parse("")
	fs := newFakeStore()
	reg.WithStore(fs)

	if !reg.Enabled() {
		t.Fatal("attaching a store must enable auth")
	}

	const tok = "super-secret-token"
	if err := reg.AddKey(ctx, Key{Token: tok, Tenant: "acme", Scopes: map[Scope]bool{ScopeRead: true, ScopeWrite: true}}, "", ""); err != nil {
		t.Fatalf("AddKey: %v", err)
	}

	// Stored hashed, never in plaintext.
	if _, plaintext := fs.m[tok]; plaintext {
		t.Fatal("token must not be stored in plaintext")
	}
	if _, hashed := fs.m[HashToken(tok)]; !hashed {
		t.Fatal("token must be stored under its sha256 hash")
	}

	k, ok := reg.Lookup(ctx, tok)
	if !ok {
		t.Fatal("persisted key should resolve via Lookup")
	}
	if k.Tenant != "acme" || !k.Has(ScopeWrite) || !k.Has(ScopeRead) {
		t.Fatalf("resolved key wrong: %+v", k)
	}

	revoked, err := reg.RevokeKey(ctx, tok)
	if err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}
	if !revoked {
		t.Fatal("RevokeKey should report a deletion")
	}
	if _, ok := reg.Lookup(ctx, tok); ok {
		t.Fatal("revoked key must not resolve")
	}
}

func TestPersistedKey_Expired(t *testing.T) {
	ctx := context.Background()
	reg, _ := Parse("")
	reg.WithStore(newFakeStore())

	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if err := reg.AddKey(ctx, Key{Token: "tok", Tenant: "acme", Scopes: map[Scope]bool{ScopeRead: true}}, past, ""); err != nil {
		t.Fatalf("AddKey: %v", err)
	}
	if _, ok := reg.Lookup(ctx, "tok"); ok {
		t.Fatal("expired key must not resolve")
	}
}

func TestPersistedKey_NotExpiredWithFutureExpiry(t *testing.T) {
	ctx := context.Background()
	reg, _ := Parse("")
	reg.WithStore(newFakeStore())

	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if err := reg.AddKey(ctx, Key{Token: "tok", Tenant: "acme", Scopes: map[Scope]bool{ScopeRead: true}}, future, ""); err != nil {
		t.Fatalf("AddKey: %v", err)
	}
	if _, ok := reg.Lookup(ctx, "tok"); !ok {
		t.Fatal("key with a future expiry should resolve")
	}
}

func TestPersistedKey_ListMergesEnvAndStore(t *testing.T) {
	ctx := context.Background()
	reg, _ := Parse("envtok:acme:read")
	reg.WithStore(newFakeStore())
	if err := reg.AddKey(ctx, Key{Token: "dyntok", Tenant: "beta", Scopes: map[Scope]bool{ScopeRead: true}}, "", "my-label"); err != nil {
		t.Fatalf("AddKey: %v", err)
	}
	keys := reg.ListKeys(ctx)
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys (1 env + 1 persisted), got %d: %+v", len(keys), keys)
	}
}

// countingStore wraps a fakeStore and counts GetAPIKeyByHash calls so cache
// tests can assert how often the persistent store is actually queried.
type countingStore struct {
	*fakeStore
	gets int
}

func newCountingStore() *countingStore { return &countingStore{fakeStore: newFakeStore()} }

func (c *countingStore) GetAPIKeyByHash(ctx context.Context, hash string) (PersistedKey, bool, error) {
	c.gets++
	return c.fakeStore.GetAPIKeyByHash(ctx, hash)
}

func TestKeyCache_HitSkipsStore(t *testing.T) {
	ctx := context.Background()
	reg, _ := Parse("")
	cs := newCountingStore()
	reg.WithStore(cs).WithKeyCache(time.Minute, 16)

	const tok = "cache-me"
	if err := reg.AddKey(ctx, Key{Token: tok, Tenant: "acme", Scopes: map[Scope]bool{ScopeRead: true}}, "", ""); err != nil {
		t.Fatalf("AddKey: %v", err)
	}

	k1, ok := reg.Lookup(ctx, tok)
	if !ok || k1.Tenant != "acme" || !k1.Has(ScopeRead) {
		t.Fatalf("first lookup wrong: %+v ok=%v", k1, ok)
	}
	k2, ok := reg.Lookup(ctx, tok)
	if !ok || k2.Tenant != "acme" || !k2.Has(ScopeRead) {
		t.Fatalf("second lookup wrong: %+v ok=%v", k2, ok)
	}
	if cs.gets != 1 {
		t.Fatalf("expected store queried once (second served from cache), got %d", cs.gets)
	}
}

func TestKeyCache_RevokeInvalidates(t *testing.T) {
	ctx := context.Background()
	reg, _ := Parse("")
	cs := newCountingStore()
	reg.WithStore(cs).WithKeyCache(time.Minute, 16)

	const tok = "revoke-me"
	if err := reg.AddKey(ctx, Key{Token: tok, Tenant: "acme", Scopes: map[Scope]bool{ScopeRead: true}}, "", ""); err != nil {
		t.Fatalf("AddKey: %v", err)
	}
	if _, ok := reg.Lookup(ctx, tok); !ok {
		t.Fatal("key should resolve before revoke")
	}
	if _, err := reg.RevokeKey(ctx, tok); err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}
	if _, ok := reg.Lookup(ctx, tok); ok {
		t.Fatal("revoked key must not resolve from cache or store")
	}
}

func TestKeyCache_DisabledPassesThrough(t *testing.T) {
	ctx := context.Background()
	reg, _ := Parse("")
	cs := newCountingStore()
	reg.WithStore(cs) // no WithKeyCache

	const tok = "no-cache"
	if err := reg.AddKey(ctx, Key{Token: tok, Tenant: "acme", Scopes: map[Scope]bool{ScopeRead: true}}, "", ""); err != nil {
		t.Fatalf("AddKey: %v", err)
	}
	reg.Lookup(ctx, tok)
	reg.Lookup(ctx, tok)
	if cs.gets != 2 {
		t.Fatalf("expected store queried twice (cache disabled), got %d", cs.gets)
	}
}

// TestNoStore_DefaultDisabled guards the MVP posture: without env keys / JWT /
// SigV4 / store, auth stays disabled (pass-through).
func TestNoStore_DefaultDisabled(t *testing.T) {
	reg, _ := Parse("")
	if reg.Enabled() {
		t.Fatal("empty registry must be disabled by default")
	}
}
