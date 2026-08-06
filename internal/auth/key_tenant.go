package auth

import "context"

// TenantForKey resolves only the tenant needed to attribute a revoke audit.
// Persistent keys are addressed by their hash; plaintext tokens are never
// returned or written to the audit record.
func (r *Registry) TenantForKey(ctx context.Context, token string) (string, bool, error) {
	r.mu.RLock()
	key, found := r.keys[token]
	store := r.store
	r.mu.RUnlock()
	if found {
		return key.Tenant, true, nil
	}
	if store == nil {
		return "", false, nil
	}
	record, found, err := store.GetAPIKeyByHash(ctx, HashToken(token))
	if err != nil || !found {
		return "", found, err
	}
	return record.TenantID, true, nil
}
