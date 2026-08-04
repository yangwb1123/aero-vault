package config

import (
	"errors"
	"fmt"
	"strings"
)

func validateAuth(cfg AuthConfig) error {
	if err := validateClientTenantMapping(cfg.JWKSClientTenantRaw); err != nil {
		return err
	}
	switch cfg.JWKSTenantClaim {
	case "", "ten", "tenant_id", "sub":
	default:
		return errors.New("AUTH_JWKS_TENANT_CLAIM must be ten, tenant_id, or sub")
	}
	for _, scope := range cfg.JWKSDefaultScopes {
		if scope != "read" && scope != "write" && scope != "admin" {
			return fmt.Errorf("AUTH_JWKS_DEFAULT_SCOPES contains unknown scope %q", scope)
		}
	}
	if cfg.JWKSAudience != "" && cfg.JWKSEndpoint == "" {
		return errors.New("AUTH_JWKS_ENDPOINT is required with AUTH_JWKS_AUDIENCE")
	}
	if cfg.JWKSEndpoint != "" && cfg.JWTIssuer == "" {
		return errors.New("AUTH_JWT_ISSUER is required by the Snaplink resource-server SDK")
	}
	if !oidcConfigured(cfg) {
		return nil
	}
	if cfg.OIDCIssuer == "" || cfg.OIDCClientID == "" || cfg.OIDCRedirectURI == "" {
		return errors.New("AUTH_OIDC_ISSUER, AUTH_OIDC_CLIENT_ID and AUTH_OIDC_REDIRECT_URI are required together")
	}
	if cfg.JWKSEndpoint == "" || cfg.JWTIssuer != cfg.OIDCIssuer {
		return errors.New("OIDC login requires AUTH_JWKS_ENDPOINT and matching AUTH_JWT_ISSUER")
	}
	if cfg.JWKSAudience != cfg.OIDCClientID {
		return errors.New("AUTH_JWKS_AUDIENCE must match AUTH_OIDC_CLIENT_ID")
	}
	return nil
}

func validateClientTenantMapping(raw string) error {
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		parts := strings.SplitN(item, ":", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return errors.New("AUTH_JWKS_CLIENT_TENANTS must contain client:tenant pairs")
		}
	}
	return nil
}

func oidcConfigured(cfg AuthConfig) bool {
	return cfg.OIDCIssuer != "" || cfg.OIDCClientID != "" || cfg.OIDCRedirectURI != "" ||
		cfg.OIDCAuthorizeURL != "" || cfg.OIDCTokenURL != ""
}
