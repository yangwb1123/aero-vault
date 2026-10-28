package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/ai"
	"github.com/aero-vault/aero-vault/internal/api/rest"
	"github.com/aero-vault/aero-vault/internal/auditgovernance"
	"github.com/aero-vault/aero-vault/internal/billing"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/events"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/spaces"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// These are observations from the raw migrated repository, not write-wrapper
// discovery or authorization grants. Each server instance owns its own ports.
type spaceReaders struct {
	router rest.RouterResourceReaders
	shares service.ShareLister
}

func spaceStartupFault(cause error) error {
	return fmt.Errorf("%w: startup: %w", access.ErrResourceBoundaryFault, cause)
}

func captureSpaceReaders(raw repository.Repository) (spaceReaders, error) {
	binding, _ := raw.(spaces.BindingReader)
	coverage, _ := raw.(spaces.CoverageReader)
	jobReader, ok := raw.(repository.JobReader)
	if !ok {
		var err error
		jobReader, err = repository.NewJobReader(raw)
		if err != nil {
			return spaceReaders{}, spaceStartupFault(err)
		}
	}
	readers := service.SpaceBoundaryReaders{
		Binding: binding, Coverage: coverage, Jobs: jobReader, Resources: raw,
	}
	if err := service.ValidateSpaceBoundaryReaders(readers); err != nil {
		return spaceReaders{}, err
	}
	authority, err := events.NewEventAuthority(raw)
	if err != nil {
		return spaceReaders{}, spaceStartupFault(err)
	}
	shares, ok := raw.(service.ShareLister)
	if !ok {
		return spaceReaders{}, spaceStartupFault(errors.New("missing raw share lister"))
	}
	ports := spaceReaders{router: rest.RouterResourceReaders{
		SpaceBoundaryReaders: readers, EventAuthority: authority,
	}, shares: shares}
	return ports, ports.validate()
}

func (r spaceReaders) validate() error {
	if err := rest.ValidateRouterResourceReaders(r.router); err != nil {
		return err
	}
	if r.shares == nil {
		return spaceStartupFault(errors.New("missing raw share lister"))
	}
	return nil
}

func (r spaceReaders) wireService(svc *service.FileService) {
	svc.WithSpaceBoundary(service.NewSpaceBoundary(r.router.SpaceBoundaryReaders)).
		WithShareLister(r.shares)
}

func (r spaceReaders) wireBus(bus *events.Bus) {
	bus.WithEventAuthority(r.router.EventAuthority).
		WithResourceBoundary(events.NewResourceBoundary(r.router.Binding))
}

func (r spaceReaders) wireSearch(search *ai.Search, svc *service.FileService) {
	search.WithCanonicalResources(r.router.Resources).
		WithResourceBoundary(r.router.Binding, r.router.Coverage).
		WithHitAuthorizer(svc) // deny-only service admission also applies with ACL off
}

// Writes retain the existing billing -> audit chain. Managers and all narrow
// read consumers retain the raw ports captured before either wrapper existed.
type spaceAssembly struct {
	repo    repository.Repository
	svc     *service.FileService
	manager *access.Manager
	billing *billing.Runtime
	audit   *auditgovernance.Runtime
}

func prepareSpaceAssembly(cfg *config.Config, raw repository.Repository, store storage.Storage, bus *events.Bus, readers spaceReaders, logger *slog.Logger) (*spaceAssembly, error) {
	if err := readers.validate(); err != nil {
		return nil, err
	}
	manager, err := buildAccessManager(cfg, raw)
	if err != nil {
		return nil, fmt.Errorf("configure enterprise access: %w", err)
	}
	br, err := buildBillingRuntime(cfg, raw, logger)
	if err != nil {
		return nil, err
	}
	ar, err := buildAuditGovernanceRuntime(cfg, raw, logger)
	if err != nil {
		if br != nil {
			br.Close()
		}
		return nil, err
	}
	a := &spaceAssembly{repo: raw, manager: manager, billing: br, audit: ar}
	if br != nil {
		a.repo = wrapBillingRepository(a.repo, br)
	}
	if ar != nil {
		a.repo = auditgovernance.WrapRepository(a.repo, ar)
	}
	readers.wireBus(bus)
	bus.WithRepository(a.repo)
	a.svc = newBoundaryFileService(cfg, store, a.repo, bus, readers, a, logger)
	if err := a.svc.ValidateSpaceBoundary(); err != nil {
		a.close()
		return nil, err
	}
	return a, nil
}

func newBoundaryFileService(cfg *config.Config, store storage.Storage, repo repository.Repository, bus *events.Bus, readers spaceReaders, a *spaceAssembly, logger *slog.Logger) *service.FileService {
	svc := service.NewFileService(store, repo, logger).
		WithEventSink(bus).
		WithDeleteFailOpen(!cfg.Access.DeleteFailClosed).
		WithTenantStatusEnforcement().
		WithReadVerification(service.ReadVerificationConfig{
			Enabled: cfg.Storage.VerifyOnRead, MaxSize: cfg.Storage.VerifyMaxSize, Sample: cfg.Storage.VerifySample,
		})
	readers.wireService(svc)
	if a.manager != nil {
		svc.WithAuthorizer(a.manager)
	}
	if a.billing != nil {
		svc.WithUsageAccountant(a.billing)
	}
	if cfg.Storage.VerifyOnRead {
		logger.Info("read-path verification enabled", "max_size", cfg.Storage.VerifyMaxSize, "sample", cfg.Storage.VerifySample)
	}
	return svc
}

func (a *spaceAssembly) startRuntimes(ctx context.Context) {
	if a.billing != nil {
		a.billing.Start(ctx)
	}
	if a.audit != nil {
		a.audit.Start(ctx)
	}
}

func (a *spaceAssembly) close() {
	if a.audit != nil {
		a.audit.Close()
	}
	if a.billing != nil {
		a.billing.Close()
	}
}
