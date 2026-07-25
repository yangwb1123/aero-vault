package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/aero-vault/aero-vault/internal/auth"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/events"
	"github.com/aero-vault/aero-vault/internal/repository"
)

func buildAuthRegistry(ctx context.Context, cfg *config.Config, logger *slog.Logger, repo repository.Repository) *auth.Registry {
	authReg, err := auth.Parse(cfg.Auth.Keys)
	if err != nil {
		logger.Error("parse auth keys failed; running without auth", "err", err)
		return authReg
	}
	configureAuthSecrets(ctx, authReg, cfg, repo, logger)
	if authReg.Enabled() {
		logger.Info("auth enabled", "api_keys", len(authReg.ListKeys(ctx)), "jwt", cfg.Auth.JWTSecret != "")
	}
	return authReg
}

func configureAuthSecrets(ctx context.Context, reg *auth.Registry, cfg *config.Config, repo repository.Repository, logger *slog.Logger) {
	if cfg.Auth.JWTSecret != "" {
		reg.WithJWT(cfg.Auth.JWTSecret)
		if cfg.Auth.JWTIssuer != "" {
			reg.JWT().WithIssuer(cfg.Auth.JWTIssuer)
			logger.Info("JWT issuer pinning enabled", "issuer", cfg.Auth.JWTIssuer)
		}
	}
	if cfg.Auth.AnonymousPublicRead {
		reg.WithAnonymousPublicRead(true)
		logger.Info("anonymous public-read enabled (ACL-gated object GET/HEAD)")
	}
	if sv, err := auth.ParseSigV4Credentials(cfg.Auth.SigV4Credentials); err != nil {
		logger.Error("parse sigv4 credentials failed", "err", err)
	} else if sv != nil {
		reg.WithSigV4(sv)
		logger.Info("s3 sigv4 verification enabled")
	}
	if cfg.Auth.PersistKeys {
		reg.WithStore(apiKeyStore{repo: repo})
		logger.Info("persistent API keys enabled (hashed, repo-backed)")
		if cfg.Auth.KeyCacheTTLSeconds > 0 {
			reg.WithKeyCache(time.Duration(cfg.Auth.KeyCacheTTLSeconds)*time.Second, 4096)
			logger.Info("persisted API-key lookup cache enabled", "ttl_seconds", cfg.Auth.KeyCacheTTLSeconds)
			if cfg.Events.TransportDSN != "" {
				keyTr := events.NewPostgresTransport(cfg.Events.TransportDSN, "aero_key_invalidate")
				reg.WithKeyChangePublisher(func(ctx context.Context, hash string) {
					if err := keyTr.Publish(ctx, repository.Event{Type: "auth.key.invalidate", Key: hash}); err != nil {
						logger.Warn("key-change publish failed", "err", err)
					}
				})
				go func() {
					_ = keyTr.Run(ctx, func(e repository.Event) { reg.InvalidateCachedKey(e.Key) })
				}()
				logger.Info("cross-replica key-cache invalidation enabled", "channel", keyTr.Channel())
			}
		}
	}
}

// apiKeyStore adapts the repository to auth.PersistentStore, keeping the auth
// package decoupled from the repository types. Wired only when AUTH_PERSIST_KEYS
// is set.
type apiKeyStore struct{ repo repository.Repository }

func (s apiKeyStore) PutAPIKey(ctx context.Context, k auth.PersistedKey) error {
	return s.repo.PutAPIKey(ctx, repository.APIKeyRecord{
		TokenHash: k.TokenHash, TenantID: k.TenantID, Scopes: k.Scopes,
		Label: k.Label, CreatedAt: k.CreatedAt, ExpiresAt: k.ExpiresAt, LastUsedAt: k.LastUsedAt,
	})
}

func (s apiKeyStore) GetAPIKeyByHash(ctx context.Context, hash string) (auth.PersistedKey, bool, error) {
	r, found, err := s.repo.GetAPIKeyByHash(ctx, hash)
	if err != nil || !found {
		return auth.PersistedKey{}, found, err
	}
	return auth.PersistedKey{
		TokenHash: r.TokenHash, TenantID: r.TenantID, Scopes: r.Scopes,
		Label: r.Label, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, LastUsedAt: r.LastUsedAt,
	}, true, nil
}

func (s apiKeyStore) DeleteAPIKeyByHash(ctx context.Context, hash string) (bool, error) {
	return s.repo.DeleteAPIKeyByHash(ctx, hash)
}

func (s apiKeyStore) ListAPIKeys(ctx context.Context, tenant string) ([]auth.PersistedKey, error) {
	recs, err := s.repo.ListAPIKeys(ctx, tenant)
	if err != nil {
		return nil, err
	}
	out := make([]auth.PersistedKey, 0, len(recs))
	for _, r := range recs {
		out = append(out, auth.PersistedKey{
			TokenHash: r.TokenHash, TenantID: r.TenantID, Scopes: r.Scopes,
			Label: r.Label, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, LastUsedAt: r.LastUsedAt,
		})
	}
	return out, nil
}

func (s apiKeyStore) TouchAPIKey(ctx context.Context, hash, when string) error {
	return s.repo.TouchAPIKey(ctx, hash, when)
}
