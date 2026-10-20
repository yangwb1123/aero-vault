package rest

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestContentCloseFailureDoesNotFinishBodyNormally(t *testing.T) {
	fixture := newContentFailureFixture(t, "close")
	for _, selected := range []repository.Object{fixture.current, fixture.older} {
		for _, endpoint := range contentFailurePaths(fixture, selected) {
			for _, partial := range []bool{false, true} {
				headers := map[string]string{}
				if partial {
					headers["Range"] = "bytes=0-3"
				}
				opens := fixture.store.opens.Load()
				response, body, err := contentFailureRequest(t, endpoint, headers)
				assertContentFailureInterrupted(t, fixture, selected, response, body, err, opens+1)
			}
		}
	}
}

func TestContentCloseFailureFullConsumedRangeChecksExistingVerifier(t *testing.T) {
	fixture := newContentFailureFixture(t, "corrupt")
	for _, selected := range []repository.Object{fixture.current, fixture.older} {
		for _, endpoint := range contentFailurePaths(fixture, selected) {
			for _, value := range []string{fmt.Sprintf("bytes=0-%d", selected.Size-1), "bytes=-4"} {
				opens := fixture.store.opens.Load()
				response, body, err := contentFailureRequest(t, endpoint, map[string]string{"Range": value})
				assertContentFailureInterrupted(t, fixture, selected, response, body, err, opens+1)
			}
		}
	}
}

func TestContentCloseFailureDoesNotReplaceMetadataOnlyOutcomes(t *testing.T) {
	fixture := newContentFailureFixture(t, "close")
	for _, selected := range []repository.Object{fixture.current, fixture.older} {
		for _, endpoint := range contentFailurePaths(fixture, selected) {
			for _, check := range []contentMetadataOnlyCase{
				{http.MethodHead, "", "", http.StatusOK},
				{http.MethodGet, "If-None-Match", `"` + selected.ETag + `"`, http.StatusNotModified},
				{http.MethodGet, "If-Match", `"wrong"`, http.StatusPreconditionFailed},
				{http.MethodGet, "Range", "bytes=99999-", http.StatusRequestedRangeNotSatisfiable},
			} {
				assertContentFailureMetadataOnly(t, fixture, selected, endpoint, check)
			}
		}
	}
}

func TestContentCloseFailurePartialVerificationDoesNotReadRemainder(t *testing.T) {
	fixture := newContentFailureFixture(t, "corrupt")
	// Verification remains enabled, but partial reads must not drain the object
	// to obtain a full-object digest. Reset only the transport's mutation mode.
	fixture.store.mode.Store("healthy")
	for _, selected := range []repository.Object{fixture.current, fixture.older} {
		for _, endpoint := range contentFailurePaths(fixture, selected) {
			reads := fixture.store.reads.Load()
			assertHealthyContentFailureRead(t, fixture, selected, endpoint, true)
			if fixture.store.reads.Load() != reads+1 {
				t.Error("partial verification added source reads")
			}
		}
	}
}

func TestContentCloseFailureExplicitNewRequestCanRecover(t *testing.T) {
	fixture := newContentFailureFixture(t, "close")
	endpoint := contentFailurePaths(fixture, fixture.current)[1]
	response, body, err := contentFailureRequest(t, endpoint, nil)
	assertContentFailureInterrupted(t, fixture, fixture.current, response, body, err, 1)
	fixture.store.mode.Store("healthy")
	assertHealthyContentFailureRead(t, fixture, fixture.current, endpoint, false)
}
