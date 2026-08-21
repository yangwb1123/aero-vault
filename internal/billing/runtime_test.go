package billing

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

func openRuntimeTestStore(t *testing.T) (repository.Repository, Store) {
	t.Helper()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, ok := repo.(Store)
	if !ok {
		t.Fatal("repository does not implement billing Store")
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo, store
}

type projectionErrorStore struct {
	Store
	err error
}

func (s *projectionErrorStore) GetBillingProjection(
	context.Context, string,
) (repository.BillingProjection, bool, error) {
	return repository.BillingProjection{}, false, s.err
}

func seedRuntimeProjection(t *testing.T, store Store) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := store.ApplyBillingProjection(context.Background(), repository.BillingProjection{
		TenantID: "acme", Revision: 1, Active: true,
		Bytes: repository.BillingLimit{Hard: 1 << 30}, Objects: repository.BillingLimit{Hard: 1000},
		EffectiveAt: now.Add(-time.Minute), ProjectedAt: now,
	}); err != nil {
		t.Fatalf("apply projection: %v", err)
	}
}

func TestRuntimeFailsUnknownProjectionClosed(t *testing.T) {
	_, store := openRuntimeTestStore(t)
	runtime := &Runtime{
		store: store, bindings: map[string]*Client{"acme": nil},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	err := runtime.CheckQuota(context.Background(), "acme",
		repository.TenantQuota{TenantID: "acme"}, 1, 1)
	if !errors.Is(err, service.ErrEntitlementUnavailable) {
		t.Fatalf("missing projection error=%v", err)
	}
	if err := runtime.Ready(context.Background()); !errors.Is(err, service.ErrEntitlementUnavailable) {
		t.Fatalf("readiness error=%v", err)
	}
}

func TestRuntimeEnforcesExplicitZeroAndPreservesProjectedUse(t *testing.T) {
	ctx := context.Background()
	repo, store := openRuntimeTestStore(t)
	now := time.Now().UTC()
	_, err := store.ApplyBillingProjection(ctx, repository.BillingProjection{
		TenantID: "acme", Revision: 1, Active: true,
		Bytes:       repository.BillingLimit{Hard: 0},
		Objects:     repository.BillingLimit{Hard: 10},
		EffectiveAt: now.Add(-time.Minute), ProjectedAt: now,
	})
	if err != nil {
		t.Fatalf("apply projection: %v", err)
	}
	runtime := &Runtime{store: store, bindings: map[string]*Client{"acme": nil}}
	quota, err := repo.GetTenantQuota(ctx, "acme")
	if err != nil {
		t.Fatalf("get quota: %v", err)
	}
	if err := runtime.CheckQuota(ctx, "acme", quota, 1, 0); !errors.Is(err, service.ErrQuotaExceeded) {
		t.Fatalf("explicit hard zero was not enforced: %v", err)
	}
	if err := runtime.CheckQuota(ctx, "acme", quota, 0, 0); err != nil {
		t.Fatalf("non-growing mutation should be allowed: %v", err)
	}
}

func TestBillingRuntimeReadyDegradesOnBacklogLag(t *testing.T) {
	ctx := context.Background()
	_, store := openRuntimeTestStore(t)
	seedRuntimeProjection(t, store)
	if _, _, err := store.ApplyBillingUsage(ctx, repository.BillingUsageMutation{
		OperationID: "lag-1", TenantID: "acme", Kind: "object_write",
		DeltaBytes: 1, OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed pending usage: %v", err)
	}
	runtime := &Runtime{
		store: store, bindings: map[string]*Client{"acme": nil}, maxLag: time.Millisecond,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	time.Sleep(3 * time.Millisecond)
	if err := runtime.Ready(ctx); err != nil {
		t.Fatalf("lagged runtime failed readiness: %v", err)
	}
	if !runtime.Degraded() || runtime.BacklogAge() <= time.Millisecond {
		t.Fatalf("degraded=%v backlog_age=%v, want degraded with positive lag", runtime.Degraded(), runtime.BacklogAge())
	}
}

func TestBillingRuntimeBacklogAgeZeroWhenNoPending(t *testing.T) {
	_, store := openRuntimeTestStore(t)
	seedRuntimeProjection(t, store)
	runtime := &Runtime{
		store: store, bindings: map[string]*Client{"acme": nil}, maxLag: time.Minute,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if age, ok, err := runtime.PendingBacklogAge(context.Background()); err != nil || ok || age != 0 {
		t.Fatalf("empty backlog probe: age=%v ok=%v err=%v", age, ok, err)
	}
	if err := runtime.Ready(context.Background()); err != nil {
		t.Fatalf("empty backlog failed readiness: %v", err)
	}
	if runtime.Degraded() || runtime.BacklogAge() != 0 {
		t.Fatalf("empty backlog cache: degraded=%v age=%v", runtime.Degraded(), runtime.BacklogAge())
	}
}

func TestBillingCheckQuotaDegradesOnProjectionLookupFailure(t *testing.T) {
	_, base := openRuntimeTestStore(t)
	store := &projectionErrorStore{Store: base, err: errors.New("temporary projection outage")}
	runtime := &Runtime{
		store: store, bindings: map[string]*Client{"acme": nil},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if err := runtime.CheckQuota(context.Background(), "acme", repository.TenantQuota{TenantID: "acme"}, 1, 1); err != nil {
		t.Fatalf("projection lookup outage did not degrade: %v", err)
	}
}

func TestPermanentBillingErrorsAreClassifiedWithoutMaskingTransientFailures(t *testing.T) {
	for _, status := range []int{409, 422} {
		if !isPermanentBillingError(&apiError{Status: status}) {
			t.Errorf("status %d was not classified as permanent", status)
		}
	}
	for _, status := range []int{399, 500, 503} {
		if isPermanentBillingError(&apiError{Status: status}) {
			t.Errorf("status %d was classified as permanent", status)
		}
	}
	if isPermanentBillingError(errors.New("transport failed")) {
		t.Fatal("transport failure was classified as permanent")
	}
}

func TestRuntimeTerminalizesPermanentUsageResponse(t *testing.T) {
	for _, status := range []int{http.StatusConflict, http.StatusUnprocessableEntity} {
		t.Run(statusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != pathUsage {
					t.Errorf("usage path=%q", r.URL.Path)
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"permanent"}`))
			}))
			defer server.Close()

			ctx := context.Background()
			_, store := openRuntimeTestStore(t)
			_, _, err := store.ApplyBillingUsage(ctx, repository.BillingUsageMutation{
				OperationID: "permanent-" + statusText(status), TenantID: "acme", Kind: "object_write",
				DeltaBytes: 1, OccurredAt: time.Now().UTC(),
			})
			if err != nil {
				t.Fatalf("apply usage: %v", err)
			}
			facts, err := store.ClaimBillingUsage(ctx, "worker", 10, time.Minute)
			if err != nil || len(facts) != 1 {
				t.Fatalf("claim usage: len=%d err=%v", len(facts), err)
			}
			client := newClient(server.URL, server.Client(), &tokenSource{
				client: &fakeCredentialsClient{}, now: time.Now,
			})
			runtime := &Runtime{
				store: store, bindings: map[string]*Client{"acme": client},
				logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			}
			runtime.deliverFact(ctx, facts[0])
			if again, err := store.ClaimBillingUsage(ctx, "worker-2", 10, time.Minute); err != nil || len(again) != 0 {
				t.Fatalf("permanent response left fact claimable: facts=%v err=%v", again, err)
			}
		})
	}
}
