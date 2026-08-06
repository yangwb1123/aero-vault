package auth

import (
	"context"
	"testing"
)

func TestTenantForKeyResolvesMemoryAndHashedPersistentRecords(t *testing.T) {
	ctx := context.Background()
	memory, err := Parse("memory-secret:tenant-memory:admin")
	if err != nil {
		t.Fatal(err)
	}
	tenant, found, err := memory.TenantForKey(ctx, "memory-secret")
	if err != nil || !found || tenant != "tenant-memory" {
		t.Fatalf("memory tenant=%q found=%v err=%v", tenant, found, err)
	}

	persisted, _ := Parse("")
	store := newFakeStore()
	persisted.WithStore(store)
	if err := persisted.AddKey(ctx, Key{Token: "persistent-secret", Tenant: "tenant-db",
		Scopes: map[Scope]bool{ScopeAdmin: true}}, "", "audit-test"); err != nil {
		t.Fatal(err)
	}
	tenant, found, err = persisted.TenantForKey(ctx, "persistent-secret")
	if err != nil || !found || tenant != "tenant-db" {
		t.Fatalf("persistent tenant=%q found=%v err=%v", tenant, found, err)
	}
	if _, plaintext := store.m["persistent-secret"]; plaintext {
		t.Fatal("tenant resolution stored plaintext token")
	}
}
