package ai

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"

	_ "modernc.org/sqlite"
)

func qdrantTestRef(tenant, bucket, key string, objectID int64) access.ChunkRef {
	return access.ChunkRef{TenantID: tenant, Bucket: bucket, Key: key, ObjectID: objectID}
}

// fakeSink is defined by the legacy sink tests. These test-only scoped adapters
// validate the ref and forward exactly once to the original callback, preserving
// the old counters and error identity.
func (s *fakeSink) UpsertScopedObjectChunks(ctx context.Context, ref access.ChunkRef, chunks []repository.Chunk) error {
	if err := access.ValidateChunkRef(ref); err != nil {
		return err
	}
	return s.UpsertObjectChunks(ctx, ref.ObjectID, chunks)
}

func (s *fakeSink) DeleteScopedObjectChunks(ctx context.Context, ref access.ChunkRef) error {
	if err := access.ValidateChunkRef(ref); err != nil {
		return err
	}
	return s.DeleteObjectChunks(ctx, ref.ObjectID)
}

type scopedSinkCall struct {
	ref    access.ChunkRef
	chunks []repository.Chunk
}

type scopedRecordingSink struct {
	upsertCalls int
	deleteCalls int
	lastUpsert  scopedSinkCall
	deletes     []access.ChunkRef
	upsertErr   error
	deleteErr   error
}

func (s *scopedRecordingSink) UpsertObjectChunks(context.Context, int64, []repository.Chunk) error {
	return fmt.Errorf("legacy ID-only upsert must not be called")
}

func (s *scopedRecordingSink) DeleteObjectChunks(context.Context, int64) error {
	return fmt.Errorf("legacy ID-only delete must not be called")
}

func (s *scopedRecordingSink) UpsertScopedObjectChunks(_ context.Context, ref access.ChunkRef, chunks []repository.Chunk) error {
	s.upsertCalls++
	s.lastUpsert = scopedSinkCall{ref: ref, chunks: chunks}
	return s.upsertErr
}

func (s *scopedRecordingSink) DeleteScopedObjectChunks(_ context.Context, ref access.ChunkRef) error {
	s.deleteCalls++
	s.deletes = append(s.deletes, ref)
	return s.deleteErr
}

type legacyOnlySink struct {
	upsertCalls int
	deleteCalls int
}

func (s *legacyOnlySink) UpsertObjectChunks(context.Context, int64, []repository.Chunk) error {
	s.upsertCalls++
	return nil
}

func (s *legacyOnlySink) DeleteObjectChunks(context.Context, int64) error {
	s.deleteCalls++
	return nil
}

type typedNilScopedSink struct{}

func (*typedNilScopedSink) UpsertObjectChunks(context.Context, int64, []repository.Chunk) error {
	return nil
}
func (*typedNilScopedSink) DeleteObjectChunks(context.Context, int64) error { return nil }
func (*typedNilScopedSink) UpsertScopedObjectChunks(context.Context, access.ChunkRef, []repository.Chunk) error {
	panic("typed nil sink must never be called")
}
func (*typedNilScopedSink) DeleteScopedObjectChunks(context.Context, access.ChunkRef) error {
	panic("typed nil sink must never be called")
}

type listChunksOverrideRepo struct {
	repository.Repository
	rows     []repository.Chunk
	err      error
	override bool
	calls    int
}

func (r *listChunksOverrideRepo) ListChunksForObject(ctx context.Context, objectID int64) ([]repository.Chunk, error) {
	r.calls++
	if r.override {
		return r.rows, r.err
	}
	return r.Repository.ListChunksForObject(ctx, objectID)
}

type repoEnqueuer struct {
	repo repository.Repository
	jobs []repository.Job
}

func (e *repoEnqueuer) Enqueue(ctx context.Context, j repository.Job) (int64, bool, error) {
	id, deduped, err := e.repo.EnqueueJob(ctx, j)
	if err == nil {
		j.ID = id
		e.jobs = append(e.jobs, j)
	}
	return id, deduped, err
}

func persistEvent(t *testing.T, env *testEnv, e repository.Event) repository.Event {
	t.Helper()
	id, err := env.repo.InsertEvent(context.Background(), e)
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	e.ID = id
	return e
}

func deleteEventRow(t *testing.T, dsn string, eventID int64) {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite for event cleanup: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`DELETE FROM object_events WHERE id=?`, eventID); err != nil {
		t.Fatalf("delete event %d: %v", eventID, err)
	}
}

type missingObjectByIDRepo struct {
	repository.Repository
}

func (r *missingObjectByIDRepo) GetObjectByID(context.Context, int64) (repository.Object, error) {
	return repository.Object{}, repository.ErrNotFound
}
