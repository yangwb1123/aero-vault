package rest

import (
	"net/http"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

// Pure transport tests deliberately return synthetic bytes, not object DTOs.
// Their fixture supplies actual SQL admission and FileService action dispatch;
// the production Handler resolver is exercised separately through NewRouter.
type idemBoundaryRepository struct {
	repository.Repository
	IdempotencyReplayBoundary
}

type idemTransportAdmission struct{ svc *service.FileService }

func idemBoundaryRepo(t *testing.T, repo repository.Repository) repository.Repository {
	t.Helper()
	svc := service.NewFileService(nil, repo, idemSilentLogger()).
		WithSpaceBoundary(service.NewSpaceBoundary(service.DiscoverSpaceBoundaryReaders(repo)))
	if err := svc.ValidateSpaceBoundary(); err != nil {
		t.Fatal(err)
	}
	return idemBoundaryRepository{Repository: repo, IdempotencyReplayBoundary: &idemTransportAdmission{svc: svc}}
}

func idemTransportBoundary(repo repository.Repository) IdempotencyReplayBoundary {
	boundary, _ := repo.(IdempotencyReplayBoundary)
	return boundary
}

func (f *idemTransportAdmission) AdmitReplay(r *http.Request, rec repository.IdempotencyRecord) error {
	target, err := resolveReplayTarget(r)
	if err != nil {
		return err
	}
	tenant := mw.TenantFrom(r.Context())
	resource := access.Resource{TenantID: tenant, Bucket: service.DefaultBucket, Key: target.key, Kind: access.ResourceObject}
	// Trusted, tenant-pinned fixture work keeps the ordinary delete-provider
	// exception real. It cannot bypass the independent Space boundary.
	ctx := access.SystemContext(r.Context(), tenant)
	return f.svc.AuthorizeReplayAdmission(ctx, service.ReplayAdmission{Action: target.actions[0], Resource: resource}, service.ReadOptions{})
}
