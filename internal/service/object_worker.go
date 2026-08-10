package service

import (
	"context"
	"errors"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/events"
	"github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
)

// SetObjectTagsByID updates tags on one exact version. It is intended for
// asynchronous workers whose event can become stale after a newer upload.
func (s *FileService) SetObjectTagsByID(
	ctx context.Context, objectID int64, tags map[string]string,
) error {
	obj, err := s.repo.GetObjectByID(ctx, objectID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	if err := s.authorizeObject(ctx, access.ActionWrite, obj); err != nil {
		return err
	}
	if err := s.repo.UpdateObjectTagsByID(ctx, objectID, tags); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// quarantineReason is the deletion-reason vocabulary's first entry: the
// antivirus quarantine path. It feeds both the audit_log Detail (so quarantine
// rows are distinguishable from user soft/hard deletes — the actor string
// alone is not unique) and the deleted@1.1 fact's reason field.
const quarantineReason = "av_infected"

// QuarantineObjectByID soft-deletes one exact version while preserving the
// FileService deletion side effects: chunk cleanup, quota accounting and event
// publication. The soft delete, the audit_log row and both outbox facts
// (deleted@1.1 + notify@1.1) commit in one transaction. signature is the
// scanner-reported threat name carried in the notify@1.1 payload (the worker
// passes it explicitly — the service does not couple to antivirus tag keys).
// Repeated delivery is idempotent.
func (s *FileService) QuarantineObjectByID(ctx context.Context, objectID int64, signature string) error {
	obj, err := s.repo.GetObjectByID(ctx, objectID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	if err := s.authorizeObject(ctx, access.ActionDelete, obj); err != nil {
		return err
	}
	// A non-current version is already represented as a tombstone row, so it
	// cannot be soft-deleted a second time without conflating two states.
	// Remove only that infected version; the current version remains intact.
	if obj.VersionTombstone {
		return s.DeleteVersion(ctx, obj.TenantID, obj.Bucket, obj.Key, obj.VersionID)
	}
	if obj.DeletedAt != nil {
		return nil
	}
	if err := s.preflightQuota(ctx, obj.TenantID, 0, 0); err != nil {
		return err
	}
	if s.chunkCleaner != nil && !IsDeleteMarker(obj) {
		if err := s.chunkCleaner.DeleteObjectChunks(ctx, obj.ID); err != nil {
			s.logger.Warn("chunk cleanup on quarantine failed", "object_id", obj.ID, "err", err)
		}
	}
	if err := s.repo.SoftDeleteObjectByIDWithEvent(ctx, obj.ID, s.quarantineAuditEntry(ctx, obj), s.quarantineFacts(ctx, obj, signature)); err != nil {
		return err
	}
	bytes, objects := countedObjectUsage([]repository.Object{obj})
	if _, err := s.addTenantUsage(ctx, obj.TenantID, UsageQuarantine, -bytes, -objects); err != nil {
		return err
	}
	s.emit(ctx, obj, repository.EventDeleted)
	return nil
}

// quarantineAuditEntry builds the audit_log row committed atomically with the
// quarantine soft delete. actor comes from the access principal (FR-4 pins it
// to system:antivirus on the antivirus path; empty is legal); detail carries
// quarantineReason so quarantine rows are identified by (action=file.delete,
// detail=av_infected) rather than by actor alone.
func (s *FileService) quarantineAuditEntry(ctx context.Context, obj repository.Object) repository.AuditEntry {
	actor := ""
	if principal, ok := access.PrincipalFrom(ctx); ok {
		actor = principal.SubjectID
	}
	return repository.AuditEntry{
		Actor:    actor,
		Action:   repository.AuditActionFileDelete,
		Target:   obj.Bucket + "/" + obj.Key,
		TenantID: obj.TenantID,
		Detail:   quarantineReason,
	}
}

// quarantineFacts builds the two versioned outbox facts (deleted@1.1 +
// notify@1.1) for one quarantine, fully self-contained at emit time (FR-2).
// actor/request_id mirror deleteFacts; reason and signature are the quarantine
// additions (FR-3).
func (s *FileService) quarantineFacts(ctx context.Context, obj repository.Object, signature string) []repository.OutboxFact {
	actor := ""
	if principal, ok := access.PrincipalFrom(ctx); ok {
		actor = principal.SubjectID
	}
	requestID := middleware.RequestIDFrom(ctx)
	return []repository.OutboxFact{
		{
			EventType: repository.EventTypeFileDeleted11,
			OriginID:  obj.ID,
			TenantID:  obj.TenantID,
			Payload:   events.BuildDeletedFact(obj, actor, requestID, obj.TenantID, quarantineReason),
		},
		{
			EventType: repository.EventTypeFileNotify11,
			OriginID:  obj.ID,
			TenantID:  obj.TenantID,
			Payload:   events.BuildNotifyFact(obj, actor, requestID, obj.TenantID, "", signature),
		},
	}
}
