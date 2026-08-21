package rest

import (
	"log/slog"

	"github.com/aero-vault/aero-vault/internal/auth"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

type AdminHandler struct {
	svc    *service.FileService
	repo   repository.Repository
	reg    *auth.Registry
	authz  AuthorizationProvider
	logger *slog.Logger
}

// NewAdminHandler accepts the historical three arguments plus optional
// AuthorizationProvider and *slog.Logger values. The flexible tail keeps
// existing protocol tests source-compatible while allowing the composition
// root to pass both new dependencies explicitly.
func NewAdminHandler(svc *service.FileService, repo repository.Repository, reg *auth.Registry, args ...any) *AdminHandler {
	provider := AuthorizationProvider(AdminMatrixProvider{})
	logger := slog.Default()
	providerSet := false
	for _, arg := range args {
		switch value := arg.(type) {
		case AuthorizationProvider:
			provider = value
			providerSet = true
		case *slog.Logger:
			if value != nil {
				logger = value
			}
		case nil:
			if !providerSet {
				provider = nil
				providerSet = true
			}
		}
	}
	return &AdminHandler{svc: svc, repo: repo, reg: reg, authz: provider, logger: logger}
}
