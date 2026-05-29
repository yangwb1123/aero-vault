//go:build integration

// Package integration holds end-to-end tests for the opt-in Postgres-backed
// adapters (pgvector, pgFTS, LISTEN/NOTIFY transport) that the default
// SQLite+stdlib CI gate cannot exercise. Run against a live pgvector Postgres:
//
//	docker run -d --name aero-pg -e POSTGRES_USER=aero -e POSTGRES_PASSWORD=aero \
//	  -e POSTGRES_DB=aero -p 55432:5432 pgvector/pgvector:pg16
//	go test -tags=integration ./internal/integration/ -v
//
// Override the DSN with AERO_PG_DSN.
package integration

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/ai"
	"github.com/aero-vault/aero-vault/internal/events"
	"github.com/aero-vault/aero-vault/internal/repository"
)

func pgDSN() string {
	if d := os.Getenv("AERO_PG_DSN"); d != "" {
		return d
	}
	return "postgres://aero:aero@localhost:55432/aero?sslmode=disable"
}

// freshRepo resets the public schema and re-applies all migrations, returning a
// migrated repository plus a raw *sql.DB for test setup.
func freshRepo(t *testing.T) (repository.Repository, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("pgx", pgDSN())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.PingContext(ctx); err != nil {
		t.Skipf("no Postgres at %s: %v", pgDSN(), err)
	}
	for _, stmt := range []string{`DROP SCHEMA public CASCADE`, `CREATE SCHEMA public`} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("reset schema: %v", err)
		}
	}
	repo, err := repository.Open(ctx, "postgres", pgDSN())
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close(); _ = db.Close() })
	return repo, db
}

func seedObject(t *testing.T, repo repository.Repository, key string) repository.Object {
	t.Helper()
	ctx := context.Background()
	if err := repo.CreateBucket(ctx, "default", "default"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	obj, err := repo.UpsertObject(ctx, repository.Object{
		TenantID: "default", Bucket: "default", Key: key, Backend: "local",
		StorageKey: "default/default/" + key, Size: 1, ETag: "e",
	})
	if err != nil {
		t.Fatalf("upsert object: %v", err)
	}
	return obj
}

// TestPostgresMigrationsApply proves all migrations apply cleanly on real
// Postgres (CI only ever runs them on SQLite) and a basic CRUD round-trips.
func TestPostgresMigrationsApply(t *testing.T) {
	ctx := context.Background()
	repo, _ := freshRepo(t)
	obj := seedObject(t, repo, "hello.txt")
	got, err := repo.GetObject(ctx, "default", "default", "hello.txt")
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	if got.ID != obj.ID || got.StorageKey != obj.StorageKey {
		t.Fatalf("round-trip mismatch: %+v vs %+v", got, obj)
	}
}

// TestPgVectorSearch verifies the pgvector adapter returns nearest-neighbour
// hits against a real vector column.
func TestPgVectorSearch(t *testing.T) {
	ctx := context.Background()
	repo, db := freshRepo(t)
	obj := seedObject(t, repo, "v.txt")

	chunks := []repository.Chunk{
		{ObjectID: obj.ID, TenantID: "default", Bucket: "default", ObjectKey: "v.txt", Seq: 0, Content: "near", Embedding: []float32{1, 0, 0}, Dim: 3, EmbedModel: "test"},
		{ObjectID: obj.ID, TenantID: "default", Bucket: "default", ObjectKey: "v.txt", Seq: 1, Content: "far", Embedding: []float32{0, 1, 0}, Dim: 3, EmbedModel: "test"},
	}
	if err := repo.InsertChunks(ctx, chunks); err != nil {
		t.Fatalf("insert chunks: %v", err)
	}

	// Operator-provisioned pgvector column (the adapter's documented prerequisite).
	for _, stmt := range []string{
		`CREATE EXTENSION IF NOT EXISTS vector`,
		`ALTER TABLE chunks ADD COLUMN IF NOT EXISTS embedding_vec vector(3)`,
		`UPDATE chunks SET embedding_vec = '[1,0,0]' WHERE content = 'near'`,
		`UPDATE chunks SET embedding_vec = '[0,1,0]' WHERE content = 'far'`,
		`CREATE INDEX IF NOT EXISTS chunks_vec_idx ON chunks USING hnsw (embedding_vec vector_cosine_ops)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("provision pgvector (%q): %v", stmt, err)
		}
	}

	idx := ai.NewPgVectorIndex(db, ai.PgVectorOptions{VectorColumn: "embedding_vec"})
	hits, err := idx.SearchVectors(ctx, "default", "default", []float32{0.9, 0.1, 0}, 5)
	if err != nil {
		t.Fatalf("SearchVectors: %v", err)
	}
	if len(hits) < 1 || hits[0].Chunk.Content != "near" {
		t.Fatalf("expected 'near' as top hit, got %+v", hits)
	}
}

// TestPgFTSSearch verifies the Postgres full-text lexical adapter.
func TestPgFTSSearch(t *testing.T) {
	ctx := context.Background()
	repo, db := freshRepo(t)
	obj := seedObject(t, repo, "f.txt")

	if err := repo.InsertChunks(ctx, []repository.Chunk{
		{ObjectID: obj.ID, TenantID: "default", Bucket: "default", ObjectKey: "f.txt", Seq: 0, Content: "the quick brown fox jumps", Dim: 0, EmbedModel: "test"},
		{ObjectID: obj.ID, TenantID: "default", Bucket: "default", ObjectKey: "f.txt", Seq: 1, Content: "a lazy dog sleeps all day", Dim: 0, EmbedModel: "test"},
	}); err != nil {
		t.Fatalf("insert chunks: %v", err)
	}

	idx := ai.NewPgFTSIndex(db, ai.PgFTSOptions{})
	hits, err := idx.SearchLexical(ctx, "default", "default", "fox", 5)
	if err != nil {
		t.Fatalf("SearchLexical: %v", err)
	}
	if len(hits) < 1 || !strings.Contains(hits[0].Chunk.Content, "fox") {
		t.Fatalf("expected a 'fox' hit on top, got %+v", hits)
	}
}

// TestPgEventTransport verifies the LISTEN/NOTIFY round-trip: a published event
// is delivered to a listener on (logically) another instance.
func TestPgEventTransport(t *testing.T) {
	// Ensure connectivity (skip cleanly if no PG).
	db, err := sql.Open("pgx", pgDSN())
	if err == nil {
		if perr := db.PingContext(context.Background()); perr != nil {
			t.Skipf("no Postgres: %v", perr)
		}
		_ = db.Close()
	}

	tr := events.NewPostgresTransport(pgDSN(), "aero_test_events")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	got := make(chan repository.Event, 1)
	go func() { _ = tr.Run(ctx, func(e repository.Event) { got <- e }) }()

	// Give the listener time to establish LISTEN before NOTIFY (not durable).
	time.Sleep(800 * time.Millisecond)

	if err := tr.Publish(context.Background(), repository.Event{Type: repository.EventCreated, TenantID: "default", Key: "evt-key"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case e := <-got:
		if e.Key != "evt-key" || e.Type != repository.EventCreated {
			t.Fatalf("delivered event mismatch: %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for LISTEN/NOTIFY delivery")
	}
}
