package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestExecuteChunkJobV2SurvivesPruning(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	emb := NewHashEmbedder(64)
	obj := env.putObject(t, "durable.txt", "text/plain", "durable delete content")
	env.seedChunks(t, obj, emb, "durable delete content")
	event := persistEvent(t, env, repository.Event{
		TenantID: testTenant, Bucket: testBucket, Key: obj.Key,
		Type: repository.EventDeleted, ObjectID: &obj.ID,
	})
	enq := &repoEnqueuer{repo: env.repo}
	remoteErr := errors.New("remote cleanup down")
	sink := &scopedRecordingSink{deleteErr: remoteErr}
	ix := NewIndexer(env.repo, env.store, NewDefaultExtractor(), NewChunker(), emb, nil).
		WithQueue(enq).
		WithChunkSink(sink)
	ix.handle(ctx, event)
	if len(enq.jobs) != 1 {
		t.Fatalf("expected one durable delete job, got %d", len(enq.jobs))
	}

	job, ok, err := env.repo.ClaimJob(ctx, "w1")
	if err != nil || !ok {
		t.Fatalf("claim v2 job: ok=%v err=%v", ok, err)
	}
	if job.Type != JobDeleteChunks {
		t.Fatalf("job type: %q", job.Type)
	}
	p, err := decodeDeleteChunksV2(job.Payload)
	if err != nil {
		t.Fatalf("decode v2 payload: %v", err)
	}
	wantRef := access.ChunkRef{TenantID: obj.TenantID, Bucket: obj.Bucket, Key: obj.Key, ObjectID: obj.ID}
	if p.ref() != wantRef || p.EventID != event.ID || p.SchemaVersion != 2 {
		t.Fatalf("v2 payload: %+v want ref=%+v event=%d", p, wantRef, event.ID)
	}
	if job.DedupeKey == fmt.Sprintf("%s:%d", JobDeleteChunks, obj.ID) ||
		!strings.Contains(job.DedupeKey, "v2") || !strings.Contains(job.DedupeKey, fmt.Sprint(event.ID)) {
		t.Fatalf("v2 dedupe key is not versioned/event-scoped: %q", job.DedupeKey)
	}

	// Repeating the same event must coalesce into the one persisted live job.
	if err := ix.processEvent(ctx, event); err != nil {
		t.Fatalf("repeat event: %v", err)
	}
	all, err := env.repo.ListJobs(ctx, "", JobDeleteChunks, 10)
	if err != nil || len(all) != 1 {
		t.Fatalf("dedupe should keep one v2 job: jobs=%d err=%v", len(all), err)
	}

	if err := ix.ExecuteChunkJob(ctx, job); !errors.Is(err, remoteErr) {
		t.Fatalf("first cleanup should surface sink failure, got %v", err)
	}
	if chunks, _ := env.repo.ListChunksForObject(ctx, obj.ID); len(chunks) != 0 {
		t.Fatalf("local chunk deletion should precede the failed sink, left %d", len(chunks))
	}
	if err := env.repo.HardDeleteObjectByID(ctx, obj.ID); err != nil {
		t.Fatalf("delete source object: %v", err)
	}
	deleteEventRow(t, env.dsn, event.ID)
	if err := env.repo.RetryJob(ctx, job.ID, errString(remoteErr), time.Now()); err != nil {
		t.Fatalf("retry v2 job: %v", err)
	}
	if err := env.repo.Close(); err != nil {
		t.Fatalf("close repo: %v", err)
	}

	repo2, err := repository.Open(ctx, "sqlite", env.dsn)
	if err != nil {
		t.Fatalf("reopen repo: %v", err)
	}
	t.Cleanup(func() { _ = repo2.Close() })
	sink2 := &scopedRecordingSink{}
	ix2 := NewIndexer(repo2, env.store, NewDefaultExtractor(), NewChunker(), emb, nil).
		WithChunkSink(sink2)
	job2, ok, err := repo2.ClaimJob(ctx, "w2")
	if err != nil || !ok {
		t.Fatalf("reclaim after reopen: ok=%v err=%v", ok, err)
	}
	if job2.ID != job.ID || job2.Payload != job.Payload {
		t.Fatalf("reclaimed job identity changed: id=%d payload=%s want id=%d payload=%s",
			job2.ID, job2.Payload, job.ID, job.Payload)
	}
	if err := ix2.ExecuteChunkJob(ctx, job2); err != nil {
		t.Fatalf("durable v2 retry: %v", err)
	}
	if sink2.deleteCalls != 1 || sink2.deletes[0] != wantRef {
		t.Fatalf("durable retry ref: calls=%d refs=%+v want %+v", sink2.deleteCalls, sink2.deletes, wantRef)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestExecuteChunkJobRejectsInvalidFacts(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	obj := env.putObject(t, "invalid.txt", "text/plain", "keep me")
	ref := access.ChunkRef{TenantID: obj.TenantID, Bucket: obj.Bucket, Key: obj.Key, ObjectID: obj.ID}
	created := persistEvent(t, env, repository.Event{
		TenantID: testTenant, Bucket: testBucket, Key: obj.Key,
		Type: repository.EventCreated, ObjectID: &obj.ID,
	})
	deleted := persistEvent(t, env, repository.Event{
		TenantID: testTenant, Bucket: testBucket, Key: obj.Key,
		Type: repository.EventDeleted, ObjectID: &obj.ID,
	})
	validPayload, err := encodeDeleteChunksV2(ref, deleted.ID)
	if err != nil {
		t.Fatal(err)
	}
	wrongTypePayload, err := encodeDeleteChunksV2(ref, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	wrongScope := ref
	wrongScope.Key = "other.txt"
	wrongScopePayload, err := encodeDeleteChunksV2(wrongScope, deleted.ID)
	if err != nil {
		t.Fatal(err)
	}

	sink := &scopedRecordingSink{}
	ix := NewIndexer(env.repo, env.store, NewDefaultExtractor(), NewChunker(), NewHashEmbedder(64), nil).
		WithChunkSink(sink)
	cases := []struct {
		name string
		job  repository.Job
	}{
		{"malformed", repository.Job{TenantID: testTenant, Type: JobDeleteChunks, Payload: `{`}},
		{"unknown-schema", repository.Job{TenantID: testTenant, Type: JobDeleteChunks, Payload: `{"schema_version":1,"object_id":1,"tenant_id":"default","bucket":"default","key":"k","event_id":1}`}},
		{"wrong-job-tenant", repository.Job{TenantID: "other", Type: JobDeleteChunks, Payload: validPayload}},
		{"wrong-event-type", repository.Job{TenantID: testTenant, Type: JobDeleteChunks, Payload: wrongTypePayload}},
		{"wrong-event-scope", repository.Job{TenantID: testTenant, Type: JobDeleteChunks, Payload: wrongScopePayload}},
		{"v1-wrong-tenant", repository.Job{TenantID: "other", Type: JobDeleteChunks, Payload: EncodeObjectID(obj.ID)}},
		{"unknown-job-type", repository.Job{TenantID: testTenant, Type: "delete_chunks_v9", Payload: validPayload}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.job.ID = int64(i + 1)
			if err := ix.ExecuteChunkJob(ctx, tc.job); err == nil {
				t.Fatal("invalid job must be refused")
			}
			if sink.deleteCalls != 0 {
				t.Fatalf("invalid job reached the sink %d times", sink.deleteCalls)
			}
		})
	}
}

func TestExecuteChunkJobV1UsesChunkEvidence(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	emb := NewHashEmbedder(64)
	obj := env.putObject(t, "v1.txt", "text/plain", "v1 chunk evidence")
	env.seedChunks(t, obj, emb, "v1 chunk evidence")
	sink := &scopedRecordingSink{}
	ix := NewIndexer(&missingObjectByIDRepo{Repository: env.repo}, env.store,
		NewDefaultExtractor(), NewChunker(), emb, nil).WithChunkSink(sink)
	job := repository.Job{ID: 1, TenantID: testTenant, Type: JobDeleteChunks, Payload: EncodeObjectID(obj.ID)}
	if err := ix.ExecuteChunkJob(ctx, job); err != nil {
		t.Fatalf("v1 chunks-only resolution: %v", err)
	}
	wantRef := access.ChunkRef{TenantID: obj.TenantID, Bucket: obj.Bucket, Key: obj.Key, ObjectID: obj.ID}
	if sink.deleteCalls != 1 || sink.deletes[0] != wantRef {
		t.Fatalf("v1 delete ref: calls=%d refs=%+v want %+v", sink.deleteCalls, sink.deletes, wantRef)
	}
}

func TestExecuteChunkJobV1UnresolvedAfterSourceRemoved(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	obj := env.putObject(t, "gone.txt", "text/plain", "gone")
	if err := env.repo.HardDeleteObjectByID(ctx, obj.ID); err != nil {
		t.Fatalf("hard delete: %v", err)
	}
	sink := &scopedRecordingSink{}
	ix := NewIndexer(env.repo, env.store, NewDefaultExtractor(), NewChunker(), NewHashEmbedder(64), nil).
		WithChunkSink(sink)
	job := repository.Job{ID: 1, TenantID: testTenant, Type: JobDeleteChunks, Payload: EncodeObjectID(obj.ID)}
	err := ix.ExecuteChunkJob(ctx, job)
	if err == nil || !strings.Contains(err.Error(), "unresolved") {
		t.Fatalf("v1 without evidence must fail unresolved, got %v", err)
	}
	if sink.deleteCalls != 0 {
		t.Fatalf("unresolved v1 must not delete externally, calls=%d", sink.deleteCalls)
	}
}

func TestProcessEventRequiresExactPersistedFact(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	obj := env.putObject(t, "event.txt", "text/plain", "event")
	first := persistEvent(t, env, repository.Event{
		TenantID: testTenant, Bucket: testBucket, Key: obj.Key,
		Type: repository.EventCreated, ObjectID: &obj.ID,
	})
	second := persistEvent(t, env, repository.Event{
		TenantID: testTenant, Bucket: testBucket, Key: "different.txt",
		Type: repository.EventDeleted, ObjectID: &obj.ID,
	})
	enq := &repoEnqueuer{repo: env.repo}
	ix := NewIndexer(env.repo, env.store, NewDefaultExtractor(), NewChunker(), NewHashEmbedder(64), nil).
		WithQueue(enq)

	mismatched := first
	mismatched.ID = second.ID
	if err := ix.processEvent(ctx, mismatched); err == nil {
		t.Fatal("event whose persisted row has different scope must be rejected")
	}
	gapped := first
	gapped.ID = second.ID + 100
	if err := ix.processEvent(ctx, gapped); err == nil {
		t.Fatal("event that is not persisted must be rejected")
	}
	emptyTenant := first
	emptyTenant.TenantID = ""
	if err := ix.processEvent(ctx, emptyTenant); err == nil {
		t.Fatal("event with empty tenant must be rejected")
	}
	zeroID := first
	zeroID.ID = 0
	if err := ix.processEvent(ctx, zeroID); err == nil {
		t.Fatal("event with nonpositive ID must be rejected")
	}
	if len(enq.jobs) != 0 {
		t.Fatalf("rejected events produced %d jobs", len(enq.jobs))
	}
}
