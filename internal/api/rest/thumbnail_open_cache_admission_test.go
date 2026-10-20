package rest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

// Keep the same real row/Blob/generation/MD5 across the observations. Only the
// first repository observation lacks the seeded encryption metadata; this is a
// metadata-admission discriminator, not a test of actual KMS encryption.
func newThumbnailOpenCacheAdmissionFixture(t *testing.T, algorithm string) *thumbnailEncodingOpenFixture {
	t.Helper()
	f := newThumbnailEncodingOpenFixture(t, "image/png", "", true)
	f.repo.stale = f.current
	if err := f.repo.SetObjectMetaKey(context.Background(), service.DefaultTenant, service.DefaultBucket, f.current.Key, "_aero_sse_algorithm", algorithm); err != nil {
		t.Fatal(err)
	}
	current, err := f.repo.Repository.GetObject(context.Background(), service.DefaultTenant, service.DefaultBucket, f.current.Key)
	if err != nil {
		t.Fatal(err)
	}
	f.current = current
	if thumbnailSourceIdentity(f.repo.stale) != thumbnailSourceIdentity(current) || f.repo.stale.ETag != current.ETag ||
		!thumbnail.ContentMD5ETag(current.ETag) || !f.svc.ThumbnailSourceCacheBound(current) {
		t.Fatal("fixture must retain identical complete identity/MD5 and real generation eligibility")
	}
	return f
}

func assertThumbnailOpenCacheAdmissionMiss(t *testing.T, f *thumbnailEncodingOpenFixture, method, endpoint string, entries int) {
	t.Helper()
	before := snapshotThumbCounters(t)
	before304, beforeAccess := thumbnail304Counter(t), f.sink.count(repository.EventAccessed)
	response, body := f.request(t, method, endpoint)
	assertThumbnailEncodingOpenHealthy(t, response, body, method)
	assertThumbnailEncodingOpenSource(t, f, f.current, true)
	assertThumbnailMediaOpenIdentity(t, f, response, true)
	assertThumbDeltas(t, before, "", 1)
	if f.repo.calls.Load() != 2 || f.cache.Len() != entries || (f.cache.Bytes() == 0) != (entries == 0) {
		t.Errorf("opened encryption admission lost: stats=%d entries=%d bytes=%d want entries=%d", f.repo.calls.Load(), f.cache.Len(), f.cache.Bytes(), entries)
	}
	if thumbnail304Counter(t) != before304 || f.sink.count(repository.EventAccessed) != beforeAccess+1 {
		t.Error("opened admission changed 304/access-attempt semantics")
	}
	assertThumbnailMediaAdmissionAvailable(t, f)
}

func TestThumbnailOpenCacheAdmissionKMSDoesNotSeedResidentCache(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for path := range 2 {
			f := newThumbnailOpenCacheAdmissionFixture(t, "aws:kms")
			endpoint := f.paths()[path]
			assertThumbnailOpenCacheAdmissionMiss(t, f, method, endpoint, 0)
			// Repeating the stale preflight observation must still open the
			// source, not reuse a resident JPEG from the forbidden first miss.
			assertThumbnailOpenCacheAdmissionMiss(t, f, method, endpoint, 0)
		}
	}
}

func TestThumbnailOpenCacheAdmissionAES256StillStores(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for path := range 2 {
			f := newThumbnailOpenCacheAdmissionFixture(t, "AES256")
			endpoint := f.paths()[path]
			assertThumbnailOpenCacheAdmissionMiss(t, f, method, endpoint, 1)
			response, body := f.request(t, method, endpoint)
			assertThumbnailEncodingOpenHealthy(t, response, body, method)
			assertThumbnailMediaOpenIdentity(t, f, response, true)
			if f.store.opens.Load() != 0 || f.store.reads.Load() != 0 || f.store.closes.Load() != 0 || f.cache.Len() != 1 {
				t.Errorf("AES256 over-bypassed cache: opens=%d reads=%d closes=%d entries=%d", f.store.opens.Load(), f.store.reads.Load(), f.store.closes.Load(), f.cache.Len())
			}
		}
	}
}

func TestThumbnailOpenCacheAdmissionKnownKMSBypassesLookup(t *testing.T) {
	f := newThumbnailOpenCacheAdmissionFixture(t, "aws:kms")
	f.repo.stale = f.current
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, endpoint := range f.paths() {
			assertThumbnailOpenCacheAdmissionMiss(t, f, method, endpoint, 0)
		}
	}
	hits, misses, _, _ := f.cache.Stats()
	if hits != 0 || misses != 0 {
		t.Errorf("known encryption gate consulted cache: hits=%d misses=%d", hits, misses)
	}
}

func TestThumbnailOpenCacheAdmissionAuthorizationStillPrecedesOpen(t *testing.T) {
	for _, failure := range []error{nil, errors.New("opened cache authorization unavailable")} {
		f := newThumbnailOpenCacheAdmissionFixture(t, "aws:kms")
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, endpoint := range f.paths() {
				provider := &readBoundaryProvider{rejectAt: 2, failure: failure}
				f.svc.WithAuthorizer(provider)
				beforeAccess := f.sink.count(repository.EventAccessed)
				response, _ := f.request(t, method, endpoint)
				want := http.StatusForbidden
				if failure != nil {
					want = http.StatusInternalServerError
				}
				if response.StatusCode != want || provider.reads.Load() != 2 || f.store.opens.Load() != 0 || f.cache.Len() != 0 || f.sink.count(repository.EventAccessed) != beforeAccess {
					t.Errorf("open authorization bypassed: status=%d checks=%d opens=%d entries=%d", response.StatusCode, provider.reads.Load(), f.store.opens.Load(), f.cache.Len())
				}
				assertThumbnailMediaOpenErrorHeaders(t, response)
			}
		}
	}
}

func TestThumbnailOpenCacheAdmissionSSECStillRejectsBeforeStorage(t *testing.T) {
	f := newThumbnailOpenCacheAdmissionFixture(t, "")
	for name, value := range map[string]string{"_aero_sse_c_algorithm": "AES256", "_aero_sse_c_key_md5": "d41d8cd98f00b204e9800998ecf8427e"} {
		if err := f.repo.SetObjectMetaKey(context.Background(), service.DefaultTenant, service.DefaultBucket, f.current.Key, name, value); err != nil {
			t.Fatal(err)
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, endpoint := range f.paths() {
			response, _ := f.request(t, method, endpoint)
			if response.StatusCode != http.StatusBadRequest || f.store.opens.Load() != 0 || f.cache.Len() != 0 {
				t.Errorf("SSE-C read gate bypassed: status=%d opens=%d entries=%d", response.StatusCode, f.store.opens.Load(), f.cache.Len())
			}
			assertThumbnailMediaOpenErrorHeaders(t, response)
		}
	}
}

func TestThumbnailOpenCacheAdmissionHistoricalPinKeepsOwnPolicy(t *testing.T) {
	f := newThumbnailOpenCacheAdmissionFixture(t, "aws:kms")
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, endpoint := range f.paths() {
			f.cache.InvalidateSource(thumbnailSourceIdentity(f.older))
			response, body := f.request(t, method, endpoint+"&version="+f.older.VersionID)
			assertThumbnailEncodingOpenHealthy(t, response, body, method)
			assertThumbnailEncodingOpenSource(t, f, f.older, true)
			if response.Header.Get("ETag") != thumbnailEncodingValidator(f.older) || response.Header.Get("X-Version-Id") != f.older.VersionID || f.cache.Len() != 1 || f.repo.calls.Load() != 0 {
				t.Errorf("pin inherited current encryption/identity: headers=%v entries=%d stats=%d", response.Header, f.cache.Len(), f.repo.calls.Load())
			}
		}
	}
}

func TestThumbnailOpenCacheAdmissionOpenAPIDocumentsPolicy(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]struct{ Summary string }
	}
	if err := json.Unmarshal(globalSpec.JSON(), &spec); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct{ path, method string }{
		{"/v1/files/{key}", "get"}, {"/v1/files/{key}", "head"}, {"/v1/files/{key}/thumbnail", "get"},
	} {
		if !strings.Contains(spec.Paths[check.path][check.method].Summary, "opened source encryption metadata") {
			t.Errorf("%s %s missing opened cache store policy", check.method, check.path)
		}
	}
}
