package rest

import (
	"errors"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/events"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

// RouterResourceReaders captures actual read ports before write wrappers. An
// explicit zero value replaces discovery and remains a fault at consumers.
// It does not install a permissive authorization provider or unwrap writes.
type RouterResourceReaders struct {
	service.SpaceBoundaryReaders
	EventAuthority *events.EventAuthority
}

func DiscoverRouterResourceReaders(repo repository.Repository) RouterResourceReaders {
	authority, _ := events.NewEventAuthority(repo)
	return RouterResourceReaders{SpaceBoundaryReaders: service.DiscoverSpaceBoundaryReaders(repo), EventAuthority: authority}
}

// ValidateRouterResourceReaders is a composition-root preflight, not a cached
// absence proof. It performs no protected work and checks independent ports.
func ValidateRouterResourceReaders(readers RouterResourceReaders) error {
	if err := service.ValidateSpaceBoundaryReaders(readers.SpaceBoundaryReaders); err != nil {
		return err
	}
	if readers.EventAuthority == nil {
		return replayFault(errors.New("missing event authority"))
	}
	return nil
}

func (h *Handler) WithResourceReaders(readers RouterResourceReaders) *Handler {
	h.resourceReaders = &readers
	return h
}

func (h *Handler) directReadConsumers(repo repository.Repository, aih *AIHandler, sse *SSEHandler, adm *AdminHandler) {
	readers := DiscoverRouterResourceReaders(repo)
	if h.resourceReaders != nil {
		readers = *h.resourceReaders
	} else {
		h.resourceReaders = &readers
	}
	adm.boundary = access.NewResourceBoundary(readers.Binding, readers.Coverage)
	adm.jobReader, adm.jobResources = readers.Jobs, readers.Resources
	aih.WithLineageReaders(LineageReaders{Binding: readers.Binding, Coverage: readers.Coverage, Resources: readers.Resources})
	sse.WithResourceBoundary(readers.Binding)
	sse.authority = readers.EventAuthority
}
