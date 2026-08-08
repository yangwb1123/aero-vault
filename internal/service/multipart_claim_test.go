package service

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// failOnceAccountant is the AC-1/AC-2 test double: CheckQuota always passes;
// Apply fails once with a sentinel error, then delegates to repo.AddTenantUsage
// and counts calls. Delegation is the only way usage becomes visible after
// injection (usage_accounting.go: addTenantUsage routes exclusively to Apply).
type failOnceAccountant struct {
	mu         sync.Mutex
	repo       repository.Repository
	applyCalls int
}

var errTransientAccounting = errors.New("transient accounting failure")

func (f *failOnceAccountant) CheckQuota(
	context.Context, string, repository.TenantQuota, int64, int64,
) error {
	return nil
}

func (f *failOnceAccountant) Apply(
	ctx context.Context, m UsageMutation,
) (repository.TenantQuota, error) {
	f.mu.Lock()
	f.applyCalls++
	first := f.applyCalls == 1
	f.mu.Unlock()
	if first {
		return repository.TenantQuota{}, errTransientAccounting
	}
	return f.repo.AddTenantUsage(ctx, m.TenantID, m.DeltaBytes, m.DeltaObjects)
}

// AC-1 (direction acceptance 1): an accounting failure on the first completion
// attempt must not strand the upload — the idempotency claim is released, a
// retry completes cleanly, and tenant usage equals the object size exactly once.
func TestMultipartCompleteClaimReleasedOnUsageFailure(t *testing.T) {
	ctx := context.Background()
	svc, repo := newQuotaTestSvc(t)
	acct := &failOnceAccountant{repo: repo}
	svc.WithUsageAccountant(acct)

	up, err := svc.InitMultipart(ctx, "", "", "ac1.bin", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UploadPart(ctx, up.ID, 1, strings.NewReader("12345"), 5); err != nil {
		t.Fatal(err)
	}

	// First attempt: accounting fails, request errors (but NOT via the replay
	// "already in progress" path — that would prove a stuck claim).
	if _, err := svc.CompleteMultipart(ctx, up.ID); err == nil {
		t.Fatal("first completion should fail when accounting fails")
	} else if errors.Is(err, ErrPreconditionFailed) {
		t.Fatalf("first completion must not hit the replay path: %v", err)
	}

	// Retry: claim was released, so the whole completion re-runs cleanly.
	obj, err := svc.CompleteMultipart(ctx, up.ID)
	if err != nil {
		t.Fatalf("retry completion should succeed after released claim: %v", err)
	}
	if obj.Size != 5 {
		t.Fatalf("completed object size = %d, want 5", obj.Size)
	}
	rc, _, err := svc.Get(ctx, "", "", "ac1.bin")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || string(body) != "12345" {
		t.Fatalf("object body = %q (err=%v), want %q", body, err, "12345")
	}

	// Final usage is exactly one object of exactly its size.
	assertTenantUsage(t, repo, 5, 1)
	acct.mu.Lock()
	calls := acct.applyCalls
	acct.mu.Unlock()
	if calls != 2 {
		t.Fatalf("accountant Apply calls = %d, want 2 (1 failed + 1 succeeded)", calls)
	}
}

// AC-2 (direction acceptance 2): after an accounting failure there must be no
// stuck in_progress idempotency row for _mp_complete:<uploadID> — the next
// ClaimIdempotencyKey returns claimed=true (ON CONFLICT DO NOTHING semantics).
func TestMultipartCompleteNoStuckClaimAfterUsageFailure(t *testing.T) {
	ctx := context.Background()
	svc, repo := newQuotaTestSvc(t)
	acct := &failOnceAccountant{repo: repo}
	svc.WithUsageAccountant(acct)

	up, err := svc.InitMultipart(ctx, "", "", "ac2.bin", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UploadPart(ctx, up.ID, 1, strings.NewReader("12345"), 5); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.CompleteMultipart(ctx, up.ID); err == nil {
		t.Fatal("first completion should fail when accounting fails")
	}

	// Before any retry: the claim row must already be gone.
	key := multipartCompleteKey(up.ID)
	if _, claimed, err := repo.ClaimIdempotencyKey(ctx, "default", key, "", ""); err != nil {
		t.Fatal(err)
	} else if !claimed {
		t.Fatalf("stuck in_progress row exists for %s after accounting failure", key)
	}
	// Defensive no-op cleanup of the probe claim, then retry succeeds.
	if err := repo.DeleteIdempotencyKey(ctx, "default", key); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompleteMultipart(ctx, up.ID); err != nil {
		t.Fatalf("retry completion should succeed after released claim: %v", err)
	}
	assertTenantUsage(t, repo, 5, 1)
}
