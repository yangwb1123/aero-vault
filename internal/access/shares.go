package access

import (
	"context"
	"crypto/hmac"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type CreateShareRequest struct {
	TenantID      string
	Bucket        string
	Key           string
	Name          string
	Password      string
	AllowPreview  bool
	AllowDownload bool
	MaxUses       int64
	ExpiresAt     time.Time
	OwnerID       string
}

func (m *Manager) CreateShare(ctx context.Context, request CreateShareRequest) (Share, string, error) {
	resource := Resource{
		TenantID: request.TenantID, Bucket: request.Bucket, Key: request.Key,
		Kind: ResourceObject, OwnerID: request.OwnerID,
	}
	if err := m.require(ctx, ActionShare, resource); err != nil {
		return Share{}, "", err
	}
	if request.Key == "" || request.MaxUses < 0 || len(request.Name) > 200 || len(request.Password) > 1024 {
		return Share{}, "", fmt.Errorf("%w: invalid share target or max_uses", ErrInvalidArgument)
	}
	if !request.AllowPreview && !request.AllowDownload {
		request.AllowPreview = true
	}
	token, err := randomToken(32)
	if err != nil {
		return Share{}, "", err
	}
	share := Share{
		ID: uuid.NewString(), TenantID: request.TenantID, Bucket: request.Bucket, Key: request.Key,
		Name: request.Name, TokenHash: tokenHash(token), AllowPreview: request.AllowPreview,
		AllowDownload: request.AllowDownload, MaxUses: request.MaxUses, ExpiresAt: request.ExpiresAt,
		CreatedBy: subjectFromContext(ctx), CreatedAt: time.Now().UTC(), OwnerID: request.OwnerID,
	}
	share.PasswordMAC = m.passwordMAC(share.ID, request.Password)
	if err := m.store.CreateShare(ctx, share); err != nil {
		return Share{}, "", err
	}
	return share, token, nil
}

func (m *Manager) ListShares(ctx context.Context, tenant, bucket, key, ownerID string) ([]Share, error) {
	resource := Resource{TenantID: tenant, Bucket: bucket, Key: key, Kind: ResourceObject, OwnerID: ownerID}
	if err := m.require(ctx, ActionShare, resource); err != nil {
		return nil, err
	}
	return m.store.ListShares(ctx, tenant, bucket, key)
}

func (m *Manager) RevokeShare(ctx context.Context, tenant, id string) error {
	share, err := m.store.GetShare(ctx, tenant, id)
	if err != nil {
		return err
	}
	if subjectFromContext(ctx) != share.CreatedBy {
		resource := Resource{
			TenantID: tenant, Bucket: share.Bucket, Key: share.Key,
			Kind: ResourceObject, OwnerID: share.OwnerID,
		}
		if err := m.require(ctx, ActionShare, resource); err != nil {
			return err
		}
	}
	return m.store.RevokeShare(ctx, tenant, id, time.Now().UTC().Format(time.RFC3339Nano))
}

func (m *Manager) ResolveShare(
	ctx context.Context,
	token, password string,
	action Action,
	consume bool,
) (Share, Principal, error) {
	share, err := m.store.GetShareByTokenHash(ctx, tokenHash(token))
	if err != nil {
		return Share{}, Principal{}, err
	}
	if err := m.validateShare(share, password, action); err != nil {
		return Share{}, Principal{}, err
	}
	if consume {
		share, err = m.store.ConsumeShare(ctx, share.TokenHash, time.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			return Share{}, Principal{}, err
		}
	}
	capability := Capability{
		ID: share.ID, TenantID: share.TenantID, Bucket: share.Bucket, Key: share.Key,
		Actions: shareActions(share),
	}
	return share, CapabilityPrincipal(PrincipalShare, capability), nil
}

func (m *Manager) validateShare(share Share, password string, action Action) error {
	now := time.Now()
	if !share.RevokedAt.IsZero() || !share.ExpiresAt.IsZero() && !now.Before(share.ExpiresAt) ||
		share.MaxUses > 0 && share.UseCount >= share.MaxUses {
		return ErrShareExpired
	}
	if share.PasswordMAC != "" {
		provided := m.passwordMAC(share.ID, password)
		if !hmac.Equal([]byte(provided), []byte(share.PasswordMAC)) {
			return ErrBadPassword
		}
	}
	if !shareAllows(share, action) {
		return errors.Join(ErrDenied, errors.New("share action not granted"))
	}
	return nil
}

func shareAllows(share Share, action Action) bool {
	if action == ActionDownload {
		return share.AllowDownload
	}
	return action == ActionRead || action == ActionPreview && share.AllowPreview
}

func shareActions(share Share) []Action {
	actions := []Action{ActionRead}
	if share.AllowPreview {
		actions = append(actions, ActionPreview)
	}
	if share.AllowDownload {
		actions = append(actions, ActionDownload)
	}
	return actions
}

func (m *Manager) PublishAsset(ctx context.Context, asset PublicAsset) (PublicAsset, error) {
	resource := Resource{
		TenantID: asset.TenantID, Bucket: asset.Bucket, Key: asset.Key,
		Kind: ResourceObject, OwnerID: asset.OwnerID,
	}
	if err := m.require(ctx, ActionPublish, resource); err != nil {
		return PublicAsset{}, err
	}
	asset.Slug = strings.Trim(strings.TrimSpace(asset.Slug), "/")
	if asset.Slug == "" || strings.Contains(asset.Slug, "..") || strings.ContainsAny(asset.Slug, "?#") {
		return PublicAsset{}, fmt.Errorf("%w: invalid asset slug", ErrInvalidArgument)
	}
	existing, err := m.store.GetPublicAsset(ctx, asset.Slug)
	if err == nil {
		if existing.TenantID != asset.TenantID {
			return PublicAsset{}, fmt.Errorf("%w: asset slug is already in use", ErrInvalidArgument)
		}
		if existing.PublishedBy != subjectFromContext(ctx) {
			current := Resource{
				TenantID: existing.TenantID, Bucket: existing.Bucket, Key: existing.Key,
				Kind: ResourceObject, OwnerID: existing.OwnerID,
			}
			if err := m.require(ctx, ActionPublish, current); err != nil {
				return PublicAsset{}, err
			}
		}
		asset.ID = existing.ID
	} else if !errors.Is(err, ErrNotFound) {
		return PublicAsset{}, err
	}
	if asset.ID == "" {
		asset.ID = uuid.NewString()
	}
	if asset.CacheControl == "" {
		asset.CacheControl = "public, max-age=3600"
	}
	if len(asset.CacheControl) > 512 || strings.ContainsAny(asset.CacheControl, "\r\n") {
		return PublicAsset{}, fmt.Errorf("%w: invalid cache_control", ErrInvalidArgument)
	}
	asset.PublishedBy = subjectFromContext(ctx)
	asset.PublishedAt = time.Now().UTC()
	if err := m.store.PutPublicAsset(ctx, asset); err != nil {
		return PublicAsset{}, err
	}
	return asset, nil
}

func (m *Manager) ResolvePublicAsset(ctx context.Context, slug string) (PublicAsset, Principal, error) {
	asset, err := m.store.GetPublicAsset(ctx, strings.Trim(slug, "/"))
	if err != nil {
		return PublicAsset{}, Principal{}, err
	}
	capability := Capability{
		ID: asset.ID, TenantID: asset.TenantID, Bucket: asset.Bucket, Key: asset.Key,
		Actions: []Action{ActionRead, ActionPreview, ActionDownload},
	}
	return asset, CapabilityPrincipal(PrincipalPublic, capability), nil
}

func (m *Manager) ListPublicAssets(ctx context.Context, tenant string) ([]PublicAsset, error) {
	principal, _ := PrincipalFrom(ctx)
	if !tenantMatches(principal, Resource{TenantID: tenant}) {
		return nil, ErrDenied
	}
	assets, err := m.store.ListPublicAssets(ctx, tenant)
	if err != nil {
		return nil, err
	}
	if canPublish(principal) || isAdministrator(principal) {
		return assets, nil
	}
	visible := make([]PublicAsset, 0, len(assets))
	for _, asset := range assets {
		if asset.PublishedBy == principal.SubjectID {
			visible = append(visible, asset)
		}
	}
	return visible, nil
}

func canPublish(principal Principal) bool {
	if isAdministrator(principal) {
		return true
	}
	for _, role := range principal.Roles {
		if role == "vault.publisher" || role == "vault.publisher_admin" {
			return true
		}
	}
	return false
}

func (m *Manager) UnpublishAsset(ctx context.Context, tenant, slug string) error {
	asset, err := m.store.GetPublicAsset(ctx, slug)
	if err != nil {
		return err
	}
	if subjectFromContext(ctx) != asset.PublishedBy {
		resource := Resource{
			TenantID: tenant, Bucket: asset.Bucket, Key: asset.Key,
			Kind: ResourceObject, OwnerID: asset.OwnerID,
		}
		if err := m.require(ctx, ActionPublish, resource); err != nil {
			return err
		}
	}
	return m.store.DeletePublicAsset(ctx, tenant, slug)
}
