package rest

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// Fresh retains the original on-disk Open/Migrate/Close initialization cost.
func BenchmarkRESTFixtureSchemaFresh(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		benchmarkRESTFixtureRepository(b, filepath.Join(b.TempDir(), "x.db"))
	}
}

// Cold includes migration, close, capture, copy and the per-case Migrate.
func BenchmarkRESTFixtureSchemaCold(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var cache restFixtureSchemaCache
		path := filepath.Join(b.TempDir(), "x.db")
		if err := cache.seed(context.Background(), path); err != nil {
			b.Fatal(err)
		}
		benchmarkRESTFixtureRepository(b, path)
	}
}

// Amortized includes the first cold build in measured time; no warmup/reset.
func BenchmarkRESTFixtureSchemaAmortized(b *testing.B) {
	b.ReportAllocs()
	var cache restFixtureSchemaCache
	for i := 0; i < b.N; i++ {
		path := filepath.Join(b.TempDir(), "x.db")
		if err := cache.seed(context.Background(), path); err != nil {
			b.Fatal(err)
		}
		benchmarkRESTFixtureRepository(b, path)
	}
}

func benchmarkRESTFixtureRepository(b *testing.B, path string) {
	b.Helper()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+path)
	if err != nil {
		b.Fatal(err)
	}
	migrateErr := repo.Migrate(ctx)
	closeErr := repo.Close()
	if migrateErr != nil || closeErr != nil {
		b.Fatalf("migrate=%v close=%v", migrateErr, closeErr)
	}
}
