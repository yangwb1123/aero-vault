package access

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// PutACL creates or updates an ACL. An explicit ID is bound to the resource
// that already owns it; an empty ID retains natural-key idempotence.
func (m *Manager) PutACL(ctx context.Context, entry ACLEntry) (ACLEntry, error) {
	normalizeACLResource(&entry)
	if err := validateACL(entry); err != nil {
		return ACLEntry{}, err
	}
	if entry.ID != "" {
		return m.putACLWithID(ctx, entry)
	}
	return m.putACLByNaturalKey(ctx, entry)
}

func (m *Manager) putACLWithID(ctx context.Context, entry ACLEntry) (ACLEntry, error) {
	persisted, err := m.store.GetACLEntryByID(ctx, entry.ID)
	if err == nil {
		if !sameACLResourceIdentity(entry, persisted) {
			return ACLEntry{}, fmt.Errorf("%w: ACL ID is bound to another resource", ErrInvalidArgument)
		}
		if err := m.authorizeACLWrite(ctx, entry); err != nil {
			return ACLEntry{}, err
		}
		preserveACLIdentity(&entry, persisted)
		return m.writeACL(ctx, entry)
	}
	if !errors.Is(err, ErrNotFound) {
		return ACLEntry{}, fmt.Errorf("lookup ACL %q: %w", entry.ID, err)
	}
	return m.createACLWithID(ctx, entry)
}

func (m *Manager) createACLWithID(ctx context.Context, entry ACLEntry) (ACLEntry, error) {
	if err := m.authorizeACLWrite(ctx, entry); err != nil {
		return ACLEntry{}, err
	}
	setACLCreationMetadata(ctx, &entry)
	return m.writeACL(ctx, entry)
}

func (m *Manager) putACLByNaturalKey(ctx context.Context, entry ACLEntry) (ACLEntry, error) {
	if err := m.authorizeACLWrite(ctx, entry); err != nil {
		return ACLEntry{}, err
	}
	existing, err := m.findMatchingACL(ctx, entry)
	if err != nil {
		return ACLEntry{}, err
	}
	if existing.ID != "" {
		entry.ID, entry.CreatedBy, entry.CreatedAt = existing.ID, existing.CreatedBy, existing.CreatedAt
	} else {
		entry.ID = uuid.NewString()
	}
	setACLCreationMetadata(ctx, &entry)
	return m.writeACL(ctx, entry)
}

func (m *Manager) authorizeACLWrite(ctx context.Context, entry ACLEntry) error {
	if err := m.validateACLPrincipal(ctx, entry); err != nil {
		return err
	}
	return m.require(ctx, ActionManageACL, entry.resource())
}

func (m *Manager) writeACL(ctx context.Context, entry ACLEntry) (ACLEntry, error) {
	if err := m.store.PutACLEntry(ctx, entry); err != nil {
		return ACLEntry{}, err
	}
	return entry, nil
}

func setACLCreationMetadata(ctx context.Context, entry *ACLEntry) {
	if entry.CreatedAt.IsZero() {
		entry.CreatedBy = subjectFromContext(ctx)
		entry.CreatedAt = time.Now().UTC()
	}
}

func sameACLResourceIdentity(left, right ACLEntry) bool {
	normalizeACLResource(&left)
	normalizeACLResource(&right)
	return left.TenantID == right.TenantID &&
		left.Bucket == right.Bucket &&
		left.Key == right.Key &&
		left.ResourceKind == right.ResourceKind
}

func preserveACLIdentity(entry *ACLEntry, persisted ACLEntry) {
	entry.ID = persisted.ID
	entry.TenantID = persisted.TenantID
	entry.Bucket = persisted.Bucket
	entry.Key = persisted.Key
	entry.ResourceKind = persisted.ResourceKind
	entry.CreatedBy = persisted.CreatedBy
	entry.CreatedAt = persisted.CreatedAt
}
