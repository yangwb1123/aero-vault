package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/yangwb1123/snaplink/interfaces/ssoclient/remote"
	"github.com/yangwb1123/snaplink/interfaces/ssoclient/rs"
)

// SnaplinkConfig contains only Aero-specific identity mapping. Token security,
// key rotation, and claim validation belong to the Snaplink SDK.
type SnaplinkConfig struct {
	Issuer        string
	JWKSURL       string
	Audience      string
	TenantClaim   string
	ClientTenants map[string]string
	DefaultScopes []Scope
	RefreshEvery  time.Duration
}

// SnaplinkVerifier adapts verified Snaplink claims to Aero's principal model.
type SnaplinkVerifier struct {
	sdk           rs.Config
	cache         *rs.JWKSCache
	audience      string
	tenantClaim   string
	clientTenants map[string]string
	defaultScopes []Scope
}

func NewSnaplinkVerifier(ctx context.Context, cfg SnaplinkConfig) (*SnaplinkVerifier, error) {
	if cfg.Issuer == "" || cfg.JWKSURL == "" {
		return nil, errors.New("snaplink: issuer and JWKS URL are required")
	}
	options := []rs.JWKSOption{}
	if cfg.RefreshEvery > 0 {
		options = append(options, remote.WithJWKSRefreshInterval(cfg.RefreshEvery))
	}
	cache := rs.NewJWKSCache(cfg.JWKSURL, options...)
	cache.StartRefresher(ctx)
	return &SnaplinkVerifier{
		sdk: rs.Config{Issuer: cfg.Issuer, JWKSCache: cache}, cache: cache,
		audience: cfg.Audience, tenantClaim: cfg.TenantClaim,
		clientTenants: cloneStrings(cfg.ClientTenants),
		defaultScopes: append([]Scope(nil), cfg.DefaultScopes...),
	}, nil
}

// Close stops the SDK's background JWKS refresher.
func (v *SnaplinkVerifier) Close() {
	if v != nil && v.cache != nil {
		v.cache.Close()
	}
}

func (v *SnaplinkVerifier) Verify(ctx context.Context, token string) (Key, error) {
	claims, err := rs.ValidateToken(ctx, token, v.sdk)
	if err != nil {
		return Key{}, fmt.Errorf("snaplink: validate token: %w", err)
	}
	clientID := claims.ClientID
	if clientID == "" {
		clientID = stringClaim(claims.Raw, "azp")
	}
	if !v.matchesAudience(claims, clientID) {
		return Key{}, errors.New("snaplink: audience/client mismatch")
	}
	tenant := v.resolveTenant(claims.Raw, claims.Subject, clientID)
	if tenant == "" {
		return Key{}, errors.New("snaplink: no tenant mapping")
	}
	if claims.Subject == "" {
		return Key{}, errors.New("snaplink: token omitted subject")
	}
	scopes := v.resolveScopes(claims.Scopes(), stringSliceClaim(claims.Raw, "scopes"))
	if len(scopes) == 0 {
		return Key{}, errors.New("snaplink: token has no recognized scopes")
	}
	return Key{
		Token: token, Tenant: tenant, SubjectID: claims.Subject,
		Roles:  stringSliceClaim(claims.Raw, "roles"),
		Groups: stringSliceClaim(claims.Raw, "groups"),
		Scopes: scopes,
	}, nil
}

func (v *SnaplinkVerifier) matchesAudience(claims *rs.Claims, clientID string) bool {
	return v.audience == "" || claims.HasAudience(v.audience) || clientID == v.audience
}

func (v *SnaplinkVerifier) resolveTenant(raw map[string]any, subject, clientID string) string {
	if len(v.clientTenants) > 0 {
		return v.clientTenants[clientID]
	}
	switch v.tenantClaim {
	case "sub":
		return subject
	case "tenant_id":
		return stringClaim(raw, "tenant_id")
	default:
		return stringClaim(raw, "ten")
	}
}

func (v *SnaplinkVerifier) resolveScopes(values ...[]string) map[Scope]bool {
	resolved := map[Scope]bool{}
	for _, list := range values {
		for _, value := range list {
			scope := Scope(value)
			if knownScope(scope) {
				resolved[scope] = true
			}
		}
	}
	if len(resolved) == 0 {
		for _, scope := range v.defaultScopes {
			if knownScope(scope) {
				resolved[scope] = true
			}
		}
	}
	return resolved
}

func stringClaim(raw map[string]any, name string) string {
	value, _ := raw[name].(string)
	return value
}

func stringSliceClaim(raw map[string]any, name string) []string {
	values, ok := raw[name].([]any)
	if !ok {
		if value, ok := raw[name].(string); ok {
			return strings.Fields(value)
		}
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if item, ok := value.(string); ok {
			out = append(out, item)
		}
	}
	return out
}

func cloneStrings(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
