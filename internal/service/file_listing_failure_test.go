package service

import (
	"context"
	"errors"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
)

type ownedListingUnavailableProvider struct {
	err error
}

func (p ownedListingUnavailableProvider) Authorize(
	_ context.Context, _ access.Principal, _ access.Action, _ access.Resource,
) (access.Decision, error) {
	return access.Decision{}, p.err
}

func TestMineListingFailsClosedOnAuthorizationError(t *testing.T) {
	svc, _ := newTestSvc(t)
	putOwnedListingObject(t, svc, "alice", "mine.txt")
	unavailable := errors.New("authorization provider unavailable")
	svc.WithAuthorizer(ownedListingUnavailableProvider{err: unavailable})
	page, err := svc.ListWithOptions(principalContext("alice", "read"), "acme", "default", "", "", 10,
		FileListOptions{View: FileListMine})
	if !errors.Is(err, unavailable) || len(page.Objects) != 0 {
		t.Fatalf("provider error returned visible objects: page=%+v err=%v", page, err)
	}
}
