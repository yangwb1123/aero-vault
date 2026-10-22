package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

// deleteChunksPayloadV2 is the durable scoped delete contract. It is generated
// only from an authoritative persisted deleted event and survives object,
// chunk and event pruning retries.
type deleteChunksPayloadV2 struct {
	SchemaVersion int    `json:"schema_version"`
	ObjectID      int64  `json:"object_id"`
	TenantID      string `json:"tenant_id"`
	Bucket        string `json:"bucket"`
	Key           string `json:"key"`
	EventID       int64  `json:"event_id"`
}

func (p deleteChunksPayloadV2) ref() access.ChunkRef {
	return access.ChunkRef{TenantID: p.TenantID, Bucket: p.Bucket, Key: p.Key, ObjectID: p.ObjectID}
}

func (ix *Indexer) processEvent(ctx context.Context, e repository.Event) error {
	switch e.Type {
	case repository.EventCreated:
		return ix.handleCreatedEvent(ctx, e)
	case repository.EventDeleted:
		return ix.handleDeletedEvent(ctx, e)
	case repository.EventUpdated:
		// no-op (created/updated both emit EventCreated today)
	case repository.EventAccessed:
		// no-op (used only for audit)
	}
	return nil
}

func (ix *Indexer) handleCreatedEvent(ctx context.Context, e repository.Event) error {
	auth, err := ix.authoritativeEvent(ctx, e)
	if err != nil {
		return err
	}
	if auth.ObjectID == nil {
		return nil
	}
	ref, err := eventRef(auth)
	if err != nil {
		return err
	}
	if ix.queue == nil {
		_, err := ix.indexObjectByID(ctx, ref.ObjectID, ref.TenantID, &ref)
		return err
	}
	job := repository.Job{
		TenantID:  auth.TenantID,
		Type:      JobIndexObject,
		Payload:   EncodeObjectID(ref.ObjectID),
		DedupeKey: fmt.Sprintf("%s:%d", JobIndexObject, ref.ObjectID),
	}
	if _, _, err := ix.queue.Enqueue(ctx, job); err != nil {
		return fmt.Errorf("enqueue %s for object %d: %w", JobIndexObject, ref.ObjectID, err)
	}
	return nil
}

func (ix *Indexer) handleDeletedEvent(ctx context.Context, e repository.Event) error {
	auth, err := ix.authoritativeEvent(ctx, e)
	if err != nil {
		return err
	}
	if auth.ObjectID == nil {
		return nil
	}
	ref, err := eventRef(auth)
	if err != nil {
		return err
	}
	if ix.queue == nil {
		return ix.deleteRefChunks(ctx, ref)
	}
	payload, err := encodeDeleteChunksV2(ref, auth.ID)
	if err != nil {
		return err
	}
	job := repository.Job{
		TenantID:  auth.TenantID,
		Type:      JobDeleteChunks,
		Payload:   payload,
		DedupeKey: deleteChunksDedupeV2(ref, auth.ID),
	}
	if _, _, err := ix.queue.Enqueue(ctx, job); err != nil {
		return fmt.Errorf("enqueue %s for object %d: %w", JobDeleteChunks, ref.ObjectID, err)
	}
	return nil
}

func (ix *Indexer) authoritativeEvent(ctx context.Context, e repository.Event) (repository.Event, error) {
	if e.ID <= 0 {
		return repository.Event{}, fmt.Errorf("event identity requires a positive persisted ID")
	}
	if e.TenantID == "" {
		return repository.Event{}, fmt.Errorf("event %d has empty tenant", e.ID)
	}
	rows, err := ix.repo.ListEventsAfter(ctx, e.TenantID, e.ID-1, 1)
	if err != nil {
		return repository.Event{}, fmt.Errorf("reread event %d: %w", e.ID, err)
	}
	if len(rows) == 0 {
		return repository.Event{}, fmt.Errorf("event %d is not persisted", e.ID)
	}
	got := rows[0]
	if got.ID != e.ID {
		return repository.Event{}, fmt.Errorf("event gap: requested %d, first row is %d", e.ID, got.ID)
	}
	if got.Type != e.Type || got.TenantID != e.TenantID || got.Bucket != e.Bucket ||
		got.Key != e.Key || !sameObjectID(got.ObjectID, e.ObjectID) ||
		!payloadEqual(got.Payload, e.Payload) {
		return repository.Event{}, fmt.Errorf("event %d does not match its persisted fact", e.ID)
	}
	return got, nil
}

func sameObjectID(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func payloadEqual(a, b map[string]string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return maps.Equal(a, b)
}

func eventRef(e repository.Event) (access.ChunkRef, error) {
	if e.ObjectID == nil {
		return access.ChunkRef{}, fmt.Errorf("event %d has no object ID", e.ID)
	}
	ref := access.ChunkRef{TenantID: e.TenantID, Bucket: e.Bucket, Key: e.Key, ObjectID: *e.ObjectID}
	if err := access.ValidateChunkRef(ref); err != nil {
		return access.ChunkRef{}, fmt.Errorf("event %d chunk ref: %w", e.ID, err)
	}
	return ref, nil
}

func encodeDeleteChunksV2(ref access.ChunkRef, eventID int64) (string, error) {
	if err := access.ValidateChunkRef(ref); err != nil {
		return "", fmt.Errorf("encode delete_chunks v2: %w", err)
	}
	if eventID <= 0 {
		return "", fmt.Errorf("encode delete_chunks v2: event ID must be positive")
	}
	raw, err := json.Marshal(deleteChunksPayloadV2{
		SchemaVersion: 2, ObjectID: ref.ObjectID, TenantID: ref.TenantID,
		Bucket: ref.Bucket, Key: ref.Key, EventID: eventID,
	})
	if err != nil {
		return "", fmt.Errorf("encode delete_chunks v2: %w", err)
	}
	return string(raw), nil
}

func decodeDeleteChunksV2(payload string) (deleteChunksPayloadV2, error) {
	var p deleteChunksPayloadV2
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return deleteChunksPayloadV2{}, fmt.Errorf("decode delete_chunks v2: %w", err)
	}
	if p.SchemaVersion != 2 {
		return deleteChunksPayloadV2{}, fmt.Errorf("delete_chunks schema_version %d is unsupported", p.SchemaVersion)
	}
	if p.EventID <= 0 {
		return deleteChunksPayloadV2{}, fmt.Errorf("delete_chunks v2 event_id must be positive")
	}
	if err := access.ValidateChunkRef(p.ref()); err != nil {
		return deleteChunksPayloadV2{}, fmt.Errorf("delete_chunks v2 ref: %w", err)
	}
	return p, nil
}

func deleteChunksDedupeV2(ref access.ChunkRef, eventID int64) string {
	return fmt.Sprintf("delete_chunks:v2:%q:%q:%q:%d:%d",
		ref.TenantID, ref.Bucket, ref.Key, ref.ObjectID, eventID)
}

// ExecuteChunkJob executes one complete claimed persistent job. It validates
// the kind, schema, positive IDs, job tenant and currently observed evidence
// before any external deletion.
func (ix *Indexer) ExecuteChunkJob(ctx context.Context, job repository.Job) error {
	if job.Type == "" {
		return fmt.Errorf("chunk job: empty job type")
	}
	if job.TenantID == "" {
		return fmt.Errorf("chunk job %d: empty tenant", job.ID)
	}
	switch job.Type {
	case JobIndexObject:
		return ix.executeIndexChunkJob(ctx, job)
	case JobDeleteChunks:
		return ix.executeDeleteChunkJob(ctx, job)
	default:
		return fmt.Errorf("chunk job %d: unknown type %q", job.ID, job.Type)
	}
}

func (ix *Indexer) executeIndexChunkJob(ctx context.Context, job repository.Job) error {
	id, err := DecodeObjectID(job.Payload)
	if err != nil {
		return fmt.Errorf("index_object job %d: %w", job.ID, err)
	}
	if id <= 0 {
		return fmt.Errorf("index_object job %d: object ID must be positive", job.ID)
	}
	_, err = ix.indexObjectByID(ctx, id, job.TenantID, nil)
	return err
}

func (ix *Indexer) executeDeleteChunkJob(ctx context.Context, job repository.Job) error {
	var head struct {
		SchemaVersion *int `json:"schema_version"`
	}
	if err := json.Unmarshal([]byte(job.Payload), &head); err != nil {
		return fmt.Errorf("delete_chunks job %d: %w", job.ID, err)
	}
	if head.SchemaVersion == nil {
		return ix.executeDeleteChunkJobV1(ctx, job)
	}
	if *head.SchemaVersion != 2 {
		return fmt.Errorf("delete_chunks job %d: unknown schema_version %d", job.ID, *head.SchemaVersion)
	}
	return ix.executeDeleteChunkJobV2(ctx, job)
}

func (ix *Indexer) executeDeleteChunkJobV1(ctx context.Context, job repository.Job) error {
	id, err := DecodeObjectID(job.Payload)
	if err != nil {
		return fmt.Errorf("delete_chunks job %d: %w", job.ID, err)
	}
	if id <= 0 {
		return fmt.Errorf("delete_chunks job %d: object ID must be positive", job.ID)
	}
	ref, err := ix.resolveObjectRef(ctx, id, job.TenantID)
	if err != nil {
		return fmt.Errorf("delete_chunks job %d unresolved: %w", job.ID, err)
	}
	return ix.deleteRefChunks(ctx, ref)
}

func (ix *Indexer) executeDeleteChunkJobV2(ctx context.Context, job repository.Job) error {
	p, err := decodeDeleteChunksV2(job.Payload)
	if err != nil {
		return fmt.Errorf("delete_chunks job %d: %w", job.ID, err)
	}
	if job.TenantID != p.TenantID {
		return fmt.Errorf("delete_chunks job %d tenant %q conflicts with payload tenant %q", job.ID, job.TenantID, p.TenantID)
	}
	if err := ix.validateV2Event(ctx, p); err != nil {
		return fmt.Errorf("delete_chunks job %d: %w", job.ID, err)
	}
	return ix.deleteRefChunks(ctx, p.ref())
}

func (ix *Indexer) validateV2Event(ctx context.Context, p deleteChunksPayloadV2) error {
	rows, err := ix.repo.ListEventsAfter(ctx, p.TenantID, p.EventID-1, 1)
	if err != nil {
		return fmt.Errorf("reread event %d: %w", p.EventID, err)
	}
	if len(rows) == 0 {
		return nil // event pruned; the durable v2 payload remains authoritative
	}
	got := rows[0]
	if got.ID != p.EventID {
		// The target event has been pruned (a later tenant event is the first
		// row after eventID-1). The durable v2 payload remains authoritative.
		return nil
	}
	if got.Type != repository.EventDeleted {
		return fmt.Errorf("event %d is %q, not deleted", p.EventID, got.Type)
	}
	ref, err := eventRef(got)
	if err != nil {
		return err
	}
	if ref != p.ref() {
		return refConflict(ref, fmt.Sprintf("event %d", p.EventID))
	}
	return nil
}
