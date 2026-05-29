package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// PersistedKey is a hashed API-key record in the optional persistent store.
// The plaintext token is never stored — only its sha256 hash (TokenHash).
type PersistedKey struct {
	TokenHash  string
	TenantID   string
	Scopes     string // "+"-joined, e.g. "read+write"
	Label      string // redacted hint / human name (shown in listings)
	CreatedAt  string
	ExpiresAt  string // RFC3339, "" = no expiry
	LastUsedAt string
}

// PersistentStore is the optional backing store for runtime API keys. The
// repository satisfies it via a thin adapter wired in main(). When set, keys
// added through the Admin API persist hashed-at-rest, survive restart, and are
// shared across replicas. Env-seeded keys (AUTH_KEYS) remain in memory.
type PersistentStore interface {
	PutAPIKey(ctx context.Context, k PersistedKey) error
	GetAPIKeyByHash(ctx context.Context, hash string) (PersistedKey, bool, error)
	DeleteAPIKeyByHash(ctx context.Context, hash string) (bool, error)
	ListAPIKeys(ctx context.Context, tenant string) ([]PersistedKey, error)
	TouchAPIKey(ctx context.Context, hash, when string) error
}

// HashToken returns the sha256 hex digest used as a persisted key's identity.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// scopesToString serializes a scope set deterministically (sorted) for storage.
func scopesToString(m map[Scope]bool) string {
	parts := make([]string, 0, len(m))
	for s, ok := range m {
		if ok {
			parts = append(parts, string(s))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "+")
}

// parseScopeString is the inverse of scopesToString.
func parseScopeString(s string) map[Scope]bool {
	out := map[Scope]bool{}
	for _, sc := range strings.Split(s, "+") {
		sc = strings.TrimSpace(sc)
		if sc != "" {
			out[Scope(sc)] = true
		}
	}
	return out
}
