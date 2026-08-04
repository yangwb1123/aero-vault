package auth

import (
	"context"
	"slices"

	"github.com/aero-vault/aero-vault/internal/access"
)

func PrincipalForKey(key Key) access.Principal {
	subjectID := key.SubjectID
	if subjectID == "" {
		hash := HashToken(key.Token)
		if len(hash) > 24 {
			hash = hash[:24]
		}
		subjectID = "apikey:" + hash
	}
	scopes := make([]string, 0, len(key.Scopes))
	for scope, enabled := range key.Scopes {
		if enabled {
			scopes = append(scopes, string(scope))
		}
	}
	slices.Sort(scopes)
	return access.Principal{
		SubjectID: subjectID,
		TenantID:  key.Tenant,
		Kind:      access.PrincipalUser,
		Roles:     append([]string(nil), key.Roles...),
		Groups:    append([]string(nil), key.Groups...),
		Scopes:    scopes,
	}
}

func contextWithKey(ctx context.Context, key Key) context.Context {
	ctx = context.WithValue(ctx, ctxKeyKey, key)
	return access.WithPrincipal(ctx, PrincipalForKey(key))
}
