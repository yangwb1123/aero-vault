package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

type quotaFailureRepository struct {
	repository.Repository
}

func (r quotaFailureRepository) GetTenantQuota(
	context.Context, string,
) (repository.TenantQuota, error) {
	return repository.TenantQuota{}, errors.New("quota database unavailable")
}

func TestPutFailsClosedWhenQuotaCannotBeRead(t *testing.T) {
	service, repository := newTestSvc(t)
	failing := NewFileService(
		service.Storage(), quotaFailureRepository{Repository: repository}, nil,
	)
	_, err := failing.Put(
		context.Background(), "", "", "not-written.txt",
		strings.NewReader("payload"), 7, PutOptions{},
	)
	if err == nil || !strings.Contains(err.Error(), "quota database unavailable") {
		t.Fatalf("error=%v", err)
	}
	if _, statErr := service.Storage().Stat(
		context.Background(), storageKey(DefaultTenant, DefaultBucket, "not-written.txt"),
	); statErr == nil {
		t.Fatal("blob was written despite quota lookup failure")
	}
}
