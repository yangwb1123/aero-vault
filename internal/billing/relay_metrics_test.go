package billing

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/telemetry"
)

var billingPromHandler http.Handler

func TestMain(m *testing.M) {
	os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "") //nolint:errcheck
	var err error
	billingPromHandler, err = telemetry.EnablePrometheus()
	if err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func billingMetric(t *testing.T, name string) float64 {
	t.Helper()
	if billingPromHandler == nil {
		t.Skip("billing Prometheus handler not initialized")
	}
	rec := httptest.NewRecorder()
	billingPromHandler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics scrape returned %d", rec.Code)
	}
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.SplitN(fields[0], "{", 2)[0] != name {
			continue
		}
		value, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err == nil {
			return value
		}
	}
	return 0
}

func TestBillingRelayCountersTrackDeliveryOutcomes(t *testing.T) {
	var successPosts, retryPosts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			clientID, _, _ := r.BasicAuth()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"token-` + clientID +
				`","token_type":"Bearer","expires_in":3600}`))
			return
		}
		if r.URL.Path != pathUsage {
			http.NotFound(w, r)
			return
		}
		switch r.Header.Get("Authorization") {
		case "Bearer token-success-client":
			successPosts.Add(1)
			w.WriteHeader(http.StatusAccepted)
		case "Bearer token-retry-client":
			if retryPosts.Add(1) == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusAccepted)
		default:
			http.Error(w, "unknown binding", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store := repo.(Store)
	for _, tenant := range []string{"success", "retry"} {
		if _, _, err := store.ApplyBillingUsage(ctx, repository.BillingUsageMutation{
			OperationID: tenant + "-op", TenantID: tenant, Kind: "object_write",
			DeltaBytes: 1, OccurredAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("seed %s usage: %v", tenant, err)
		}
	}
	httpClient := server.Client()
	runtime := &Runtime{
		store: store, owner: "relay-metrics-test", batchSize: 8, claimTTL: time.Minute,
		maxAttempts: 3, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		bindings: map[string]*Client{
			"success": newClient(server.URL, httpClient,
				newTokenSource(server.URL+"/token", "success-client", "secret", httpClient)),
			"retry": newClient(server.URL, httpClient,
				newTokenSource(server.URL+"/token", "retry-client", "secret", httpClient)),
		},
	}
	attemptedBase := billingMetric(t, "billing_relay_attempted_total")
	deliveredBase := billingMetric(t, "billing_relay_delivered_total")
	failedBase := billingMetric(t, "billing_relay_failed_total")
	deadBase := billingMetric(t, "billing_relay_dead_total")

	deadline := time.Now().Add(4 * time.Second)
	for (successPosts.Load() < 1 || retryPosts.Load() < 2) && time.Now().Before(deadline) {
		runtime.deliverBatch(ctx)
		time.Sleep(20 * time.Millisecond)
	}
	if successPosts.Load() != 1 || retryPosts.Load() < 2 {
		t.Fatalf("relay outcomes: success=%d retry=%d", successPosts.Load(), retryPosts.Load())
	}
	runtime.deliverBatch(ctx)

	attempted := billingMetric(t, "billing_relay_attempted_total") - attemptedBase
	delivered := billingMetric(t, "billing_relay_delivered_total") - deliveredBase
	failed := billingMetric(t, "billing_relay_failed_total") - failedBase
	dead := billingMetric(t, "billing_relay_dead_total") - deadBase
	if delivered != 2 {
		t.Fatalf("delivered delta=%v, want 2", delivered)
	}
	if failed < 1 {
		t.Fatalf("failed delta=%v, want at least 1", failed)
	}
	if attempted < 3 {
		t.Fatalf("attempted delta=%v, want at least 3", attempted)
	}
	if dead != 0 {
		t.Fatalf("dead delta=%v, want 0", dead)
	}
}
