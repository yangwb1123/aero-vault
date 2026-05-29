package reconcile

import (
	"context"
	"testing"
	"time"
)

// TestRetention_PurgesIdempotencyKeys verifies the RetentionJob's idempotency
// GC removes keys past their TTL. A negative TTL pushes the cutoff into the
// future so a just-created key qualifies — deterministic, no sleeping.
func TestRetention_PurgesIdempotencyKeys(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	store := openTestStore(t)

	if _, claimed, err := repo.ClaimIdempotencyKey(ctx, "default", "k1", "fp", "r1"); err != nil || !claimed {
		t.Fatalf("seed claim: claimed=%v err=%v", claimed, err)
	}

	// Negative TTL pushes the cutoff into the future so the just-created key
	// qualifies. (sweep() guards idemTTL>0 for enablement; purgeIdempotency is
	// the unit under test.)
	rg := NewRetention(repo, store, time.Minute, 0, newSilentLogger()).WithIdempotencyTTL(-time.Hour)
	rg.purgeIdempotency(ctx)

	// The key should be gone — re-claim succeeds.
	if _, claimed, err := repo.ClaimIdempotencyKey(ctx, "default", "k1", "fp", "r2"); err != nil || !claimed {
		t.Fatalf("expected key purged (re-claim should win): claimed=%v err=%v", claimed, err)
	}
}
