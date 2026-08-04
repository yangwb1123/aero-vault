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

// TestPostgresQuotaAndBucketVersioning covers values whose SQL syntax or wire
// type differs from SQLite: quota row creation must not abort its transaction,
// and versioning must be sent to PostgreSQL as a boolean.
func TestPostgresQuotaAndBucketVersioning(t *testing.T) {
	ctx := context.Background()
	repo, _ := freshRepo(t)

	quota, err := repo.AddTenantUsage(ctx, "postgres-regression", 125, 2)
	if err != nil {
		t.Fatalf("AddTenantUsage: %v", err)
	}
	if quota.UsedBytes != 125 || quota.UsedObjects != 2 {
		t.Fatalf("usage = (%d, %d), want (125, 2)", quota.UsedBytes, quota.UsedObjects)
	}

	if err := repo.SetBucketVersioning(ctx, "postgres-regression", "versioned", true); err != nil {
		t.Fatalf("SetBucketVersioning(true): %v", err)
	}
	cfg, err := repo.GetBucketConfig(ctx, "postgres-regression", "versioned")
	if err != nil {
		t.Fatalf("GetBucketConfig after enable: %v", err)
	}
	if !cfg.Versioning {
		t.Fatal("versioning is disabled after enabling it")
	}

	if err := repo.SetBucketVersioning(ctx, "postgres-regression", "versioned", false); err != nil {
		t.Fatalf("SetBucketVersioning(false): %v", err)
	}
	cfg, err = repo.GetBucketConfig(ctx, "postgres-regression", "versioned")
	if err != nil {
		t.Fatalf("GetBucketConfig after disable: %v", err)
	}
	if cfg.Versioning {
		t.Fatal("versioning is enabled after disabling it")
	}
}

func TestPostgresVersionTombstoneUsageAndDeleteMarker(t *testing.T) {
	ctx := context.Background()
	repo, _ := freshRepo(t)
	if err := repo.SetBucketVersioning(ctx, "default", "versions", true); err != nil {
		t.Fatalf("enable versioning: %v", err)
	}
	for i, version := range []string{"v1", "v2"} {
		if _, err := repo.InsertObjectVersion(ctx, repository.Object{
			TenantID: "default", Bucket: "versions", Key: "doc.txt",
			VersionID: version, Backend: "local", StorageKey: "doc@" + version,
			Size: int64((i + 1) * 10), ETag: version,
		}); err != nil {
			t.Fatalf("insert %s: %v", version, err)
		}
	}
	usedBytes, usedObjects, err := repo.BucketUsage(ctx, "default", "versions")
	if err != nil {
		t.Fatalf("BucketUsage: %v", err)
	}
	if usedBytes != 30 || usedObjects != 2 {
		t.Fatalf("BucketUsage = (%d, %d), want (30, 2)", usedBytes, usedObjects)
	}
	if _, err := repo.InsertDeleteMarker(ctx, repository.Object{
		TenantID: "default", Bucket: "versions", Key: "doc.txt",
		VersionID: "marker", Metadata: map[string]string{"_aero_delete_marker": "true"},
	}); err != nil {
		t.Fatalf("InsertDeleteMarker: %v", err)
	}
}

func TestPostgresVersionTombstoneRestoreAndRetention(t *testing.T) {
	ctx := context.Background()
	repo, _ := freshRepo(t)
	obj := seedObject(t, repo, "restore.txt")
	if err := repo.SoftDeleteObject(ctx, "default", "default", obj.Key); err != nil {
		t.Fatalf("SoftDeleteObject: %v", err)
	}
	before := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	deleted, err := repo.ListSoftDeletedBefore(ctx, before, 10)
	if err != nil {
		t.Fatalf("ListSoftDeletedBefore: %v", err)
	}
	if len(deleted) != 1 || deleted[0].ID != obj.ID {
		t.Fatalf("soft-deleted objects = %+v, want object %d", deleted, obj.ID)
	}
	if err := repo.RestoreObject(ctx, "default", "default", obj.Key); err != nil {
		t.Fatalf("RestoreObject: %v", err)
	}
	if _, err := repo.GetObject(ctx, "default", "default", obj.Key); err != nil {
		t.Fatalf("GetObject after restore: %v", err)
	}
}

func TestPostgresVersionTombstoneLifecycleAndPromotion(t *testing.T) {
	ctx := context.Background()
	repo, db := freshRepo(t)
	if err := repo.SetBucketNoncurrentVersionLifecycle(ctx, "default", "versions", 1, 0); err != nil {
		t.Fatalf("set noncurrent lifecycle: %v", err)
	}
	first, err := repo.InsertObjectVersion(ctx, repository.Object{
		TenantID: "default", Bucket: "versions", Key: "doc.txt",
		VersionID: "v1", Backend: "local", StorageKey: "doc@v1", Size: 10, ETag: "v1",
	})
	if err != nil {
		t.Fatalf("insert first version: %v", err)
	}
	second, err := repo.InsertObjectVersion(ctx, repository.Object{
		TenantID: "default", Bucket: "versions", Key: "doc.txt",
		VersionID: "v2", Backend: "local", StorageKey: "doc@v2", Size: 20, ETag: "v2",
	})
	if err != nil {
		t.Fatalf("insert second version: %v", err)
	}
	if _, err := db.Exec(`UPDATE objects SET deleted_at=now()-interval '2 days' WHERE id=$1`, first.ID); err != nil {
		t.Fatalf("age first version: %v", err)
	}
	expired, err := repo.ListExpiredNonCurrentVersions(ctx, 10)
	if err != nil || len(expired) != 1 || expired[0].ID != first.ID {
		t.Fatalf("expired versions = %+v, err=%v", expired, err)
	}
	if err := repo.DeleteObjectVersion(ctx, "default", "versions", "doc.txt", second.VersionID); err != nil {
		t.Fatalf("DeleteObjectVersion: %v", err)
	}
	current, err := repo.GetObject(ctx, "default", "versions", "doc.txt")
	if err != nil || current.ID != first.ID || current.VersionTombstone {
		t.Fatalf("promoted object = %+v, err=%v", current, err)
	}
}

func TestPostgresBucketCORSDeleteUsesValidJSON(t *testing.T) {
	ctx := context.Background()
	repo, _ := freshRepo(t)
	rules := []repository.CORSRule{{
		AllowedOrigins: []string{"https://example.test"},
		AllowedMethods: []string{"GET"},
	}}
	if err := repo.SetBucketCORS(ctx, "default", "cors", rules); err != nil {
		t.Fatalf("SetBucketCORS: %v", err)
	}
	got, err := repo.GetBucketCORS(ctx, "default", "cors")
	if err != nil || len(got) != 1 {
		t.Fatalf("GetBucketCORS = %+v, err=%v", got, err)
	}
	if err := repo.DeleteBucketCORS(ctx, "default", "cors"); err != nil {
		t.Fatalf("DeleteBucketCORS: %v", err)
	}
	got, err = repo.GetBucketCORS(ctx, "default", "cors")
	if err != nil || len(got) != 0 {
		t.Fatalf("GetBucketCORS after delete = %+v, err=%v", got, err)
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
