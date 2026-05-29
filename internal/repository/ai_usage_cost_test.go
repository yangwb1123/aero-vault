package repository_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// TestRecordUsage_CostFields verifies the cost-accounting fields added in
// migration 0014 round-trip through RecordUsage and ListUsageForObject.
func TestRecordUsage_CostFields(t *testing.T) {
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	const tenant = "acme"
	u := repository.Usage{
		TenantID:         tenant,
		Caller:           "rest:search",
		Query:            "q",
		ObjectIDs:        []int64{42},
		RequestID:        "req-1",
		Model:            "gpt",
		PromptTokens:     10,
		CompletionTokens: 20,
		TotalTokens:      30,
		LatencyMs:        123,
		CostMicros:       4500,
	}
	if err := repo.RecordUsage(ctx, u); err != nil {
		t.Fatalf("record usage: %v", err)
	}

	rows, err := repo.ListUsageForObject(ctx, tenant, 42, 10)
	if err != nil {
		t.Fatalf("list usage: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	got := rows[0]
	if got.Model != "gpt" {
		t.Errorf("Model = %q, want %q", got.Model, "gpt")
	}
	if got.PromptTokens != 10 {
		t.Errorf("PromptTokens = %d, want 10", got.PromptTokens)
	}
	if got.CompletionTokens != 20 {
		t.Errorf("CompletionTokens = %d, want 20", got.CompletionTokens)
	}
	if got.TotalTokens != 30 {
		t.Errorf("TotalTokens = %d, want 30", got.TotalTokens)
	}
	if got.LatencyMs != 123 {
		t.Errorf("LatencyMs = %d, want 123", got.LatencyMs)
	}
	if got.CostMicros != 4500 {
		t.Errorf("CostMicros = %d, want 4500", got.CostMicros)
	}
}
