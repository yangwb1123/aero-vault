package billing

import (
	"context"
	"errors"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

const (
	limitStorageBytes   = "storage_bytes"
	limitStorageObjects = "storage_objects"
	featureVault        = "vault"
)

func (r *Runtime) runProjector(ctx context.Context) {
	r.projectAll(ctx)
	ticker := time.NewTicker(r.projectEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.projectAll(ctx)
		}
	}
}

func (r *Runtime) projectAll(ctx context.Context) {
	for tenant, client := range r.bindings {
		if ctx.Err() != nil {
			return
		}
		lease := "snaplink-billing-projector:" + tenant
		held, err := r.store.AcquireLease(ctx, lease, r.owner, r.projectLeaseTTL())
		if err != nil {
			r.logger.Warn("billing projector lease failed", "tenant", tenant, "err", err)
			continue
		}
		if !held {
			continue
		}
		if err := r.projectTenant(ctx, tenant, client); err != nil {
			r.logger.Warn("billing entitlement projection failed", "tenant", tenant, "err", err)
		}
	}
}

func (r *Runtime) projectLeaseTTL() time.Duration {
	ttl := r.httpTimeout * 2
	if ttl < 15*time.Second {
		ttl = 15 * time.Second
	}
	return ttl
}

func (r *Runtime) projectTenant(ctx context.Context, tenant string, client *Client) error {
	snapshot, err := client.Entitlement(ctx)
	if err != nil {
		return err
	}
	if snapshot.TenantID != tenant {
		return errors.New("snaplink entitlement tenant mismatch")
	}
	bytes, bytesOK := snapshot.Limits[limitStorageBytes]
	objects, objectsOK := snapshot.Limits[limitStorageObjects]
	if !bytesOK || !objectsOK {
		return errors.New("snaplink entitlement omitted vault storage limits")
	}
	_, err = r.store.ApplyBillingProjection(ctx, repository.BillingProjection{
		TenantID: tenant, Revision: snapshot.Revision,
		Active:      snapshot.Active && snapshot.Features[featureVault],
		Bytes:       repository.BillingLimit{Hard: bytes.Hard, Unlimited: bytes.Unlimited},
		Objects:     repository.BillingLimit{Hard: objects.Hard, Unlimited: objects.Unlimited},
		EffectiveAt: snapshot.EffectiveAt, ExpiresAt: snapshot.ExpiresAt,
		ProjectedAt: time.Now().UTC(),
	})
	return err
}
