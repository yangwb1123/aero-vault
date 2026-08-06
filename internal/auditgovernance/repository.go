package auditgovernance

import (
	"context"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

type auditedRepository struct {
	repository.Repository
	runtime *Runtime
}

func WrapRepository(repo repository.Repository, runtime *Runtime) repository.Repository {
	if runtime == nil {
		return repo
	}
	return &auditedRepository{Repository: repo, runtime: runtime}
}

func (r *auditedRepository) RecordAudit(
	ctx context.Context, entry repository.AuditEntry,
) error {
	entry.TenantID = normalizedTenant(entry.TenantID)
	if !r.runtime.Capture(entry.TenantID) {
		return r.Repository.RecordAudit(ctx, entry)
	}
	if entry.CreatedAt == "" {
		entry.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	fact := r.runtime.redactor.factFromAudit(entry, time.Now().UTC())
	return r.runtime.store.RecordAuditWithGovernance(ctx, entry, fact)
}

func (r *auditedRepository) InsertEvent(
	ctx context.Context, event repository.Event,
) (int64, error) {
	event.TenantID = normalizedTenant(event.TenantID)
	if !r.runtime.Capture(event.TenantID) {
		return r.Repository.InsertEvent(ctx, event)
	}
	fact := r.runtime.redactor.factFromEvent(event, time.Now().UTC())
	return r.runtime.store.InsertEventWithGovernance(ctx, event, fact)
}

var _ repository.Repository = (*auditedRepository)(nil)
