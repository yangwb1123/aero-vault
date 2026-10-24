package rest

import (
	"context"
	"log/slog"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/auth"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

type AdminHandler struct {
	svc          *service.FileService
	repo         repository.Repository
	reg          *auth.Registry
	authz        AuthorizationProvider
	logger       *slog.Logger
	boundary     *access.ResourceBoundary
	jobReader    repository.JobReader
	jobResources AdminJobResourceReader
}

// AdminJobResourceReader retains exact historical facts, never a latest-key
// substitute. The composition root can capture it before repository wrapping.
type AdminJobResourceReader interface {
	GetObjectByID(context.Context, int64) (repository.Object, error)
	ListChunksForObject(context.Context, int64) ([]repository.Chunk, error)
	ListEventsAfter(context.Context, string, int64, int) ([]repository.Event, error)
}

// AdminResourceReaders explicitly replaces constructor discovery, including
// nil/typed-nil fields. Missing ports stay faults; discovery never unwraps a
// billing/audit repository or borrows an unrelated service's authority.
type AdminResourceReaders struct {
	Binding   spaces.BindingReader
	Coverage  spaces.CoverageReader
	Jobs      repository.JobReader
	Resources AdminJobResourceReader
}

func discoverAdminResourceReaders(repo repository.Repository) AdminResourceReaders {
	binding, _ := repo.(spaces.BindingReader)
	coverage, _ := repo.(spaces.CoverageReader)
	jobs, ok := repo.(repository.JobReader)
	if !ok {
		jobs, _ = repository.NewJobReader(repo)
	}
	return AdminResourceReaders{Binding: binding, Coverage: coverage, Jobs: jobs, Resources: repo}
}

// NewAdminHandler accepts the historical three arguments plus optional
// AuthorizationProvider, *slog.Logger and AdminResourceReaders values. The
// flexible tail keeps existing callers source-compatible while allowing the
// composition root to inject independently captured read capabilities.
func NewAdminHandler(svc *service.FileService, repo repository.Repository, reg *auth.Registry, args ...any) *AdminHandler {
	provider := AuthorizationProvider(AdminMatrixProvider{})
	logger := slog.Default()
	providerSet := false
	readers := discoverAdminResourceReaders(repo)
	for _, arg := range args {
		switch value := arg.(type) {
		case AuthorizationProvider:
			provider = value
			providerSet = true
		case *slog.Logger:
			if value != nil {
				logger = value
			}
		case AdminResourceReaders:
			readers = value
		case *AdminResourceReaders:
			readers = AdminResourceReaders{}
			if value != nil {
				readers = *value
			}
		case nil:
			if !providerSet {
				provider = nil
				providerSet = true
			}
		}
	}
	return &AdminHandler{
		svc: svc, repo: repo, reg: reg, authz: provider, logger: logger,
		boundary:  access.NewResourceBoundary(readers.Binding, readers.Coverage),
		jobReader: readers.Jobs, jobResources: readers.Resources,
	}
}
