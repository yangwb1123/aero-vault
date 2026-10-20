package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// Prewarm through actual pinned HTTP derivations, not synthetic cache entries.
// Both version rows, Blob bytes, generation proofs and JPEGs are real. The
// existing seam returns A for the first current Stat and B for the re-Stat.
type thumbnailRevalidationCacheFixture struct {
	*thumbnailEncodingOpenFixture
	olderJPEG, currentJPEG []byte
}

func newThumbnailRevalidationCacheFixture(t *testing.T) *thumbnailRevalidationCacheFixture {
	t.Helper()
	f := newThumbnailEncodingOpenFixture(t, "image/png", "", true)
	response, older := f.request(t, http.MethodGet, f.paths()[0]+"&version="+f.older.VersionID)
	assertThumbnailEncodingOpenHealthy(t, response, older, http.MethodGet)
	response, current := f.request(t, http.MethodGet, f.paths()[0]+"&version="+f.current.VersionID)
	assertThumbnailEncodingOpenHealthy(t, response, current, http.MethodGet)
	if bytes.Equal(older, current) || f.cache.Len() != 2 {
		t.Fatal("fixture must hold distinct real A/B JPEG cache entries")
	}
	return &thumbnailRevalidationCacheFixture{f, older, current}
}

func thumbnailRevalidationCacheConditions(f *thumbnailRevalidationCacheFixture) []map[string]string {
	return []map[string]string{
		{"If-None-Match": thumbnailEncodingValidator(f.older)},
		{"If-Modified-Since": f.current.UpdatedAt.Add(-time.Hour).UTC().Format(http.TimeFormat)},
	}
}

func assertThumbnailRevalidationCacheResponse(t *testing.T, f *thumbnailRevalidationCacheFixture, response *http.Response, body []byte, method string) {
	t.Helper()
	assertThumbnailEncodingOpenHealthy(t, response, body, method)
	if response.Header.Get("ETag") != thumbnailEncodingValidator(f.current) || response.Header.Get("X-Version-Id") != f.current.VersionID ||
		response.Header.Get("Last-Modified") != f.current.UpdatedAt.UTC().Format(http.TimeFormat) ||
		response.Header.Get("Content-Length") != strconv.Itoa(len(f.currentJPEG)) {
		t.Errorf("revalidated B response reverted to A metadata: headers=%v want version=%s", response.Header, f.current.VersionID)
	}
	if method == http.MethodGet && !bytes.Equal(body, f.currentJPEG) {
		t.Errorf("revalidated B response served stale A JPEG: equals A=%v", bytes.Equal(body, f.olderJPEG))
	}
}

func assertThumbnailRevalidationCachePipeline(t *testing.T, f *thumbnailRevalidationCacheFixture, hot bool) {
	t.Helper()
	wantStats := int64(2)
	if !hot {
		wantStats = 3
		assertThumbnailEncodingOpenSource(t, f.thumbnailEncodingOpenFixture, f.current, true)
	} else if f.store.opens.Load() != 0 || f.store.reads.Load() != 0 || f.store.closes.Load() != 0 {
		t.Errorf("hot B did not bypass opener: opens=%d reads=%d closes=%d", f.store.opens.Load(), f.store.reads.Load(), f.store.closes.Load())
	}
	if f.repo.calls.Load() != wantStats || f.cache.Len() != 2 {
		t.Errorf("revalidation selected wrong cache/source: stats=%d want=%d entries=%d", f.repo.calls.Load(), wantStats, f.cache.Len())
	}
}

func checkThumbnailRevalidationCacheSelection(t *testing.T, f *thumbnailRevalidationCacheFixture, method, endpoint string, condition map[string]string, hot bool) {
	t.Helper()
	if !hot {
		f.cache.InvalidateSource(thumbnailSourceIdentity(f.current))
	}
	before := snapshotThumbCounters(t)
	before304, beforeAccess := thumbnail304Counter(t), f.sink.count(repository.EventAccessed)
	hits, _, _, _ := f.cache.Stats()
	response, body := thumbnailMediaRevalidationRequest(t, f.thumbnailEncodingOpenFixture, method, endpoint, condition)
	assertThumbnailRevalidationCacheResponse(t, f, response, body, method)
	assertThumbnailRevalidationCachePipeline(t, f, hot)
	assertThumbDeltas(t, before, "", 1)
	afterHits, _, _, _ := f.cache.Stats()
	wantHits := hits
	if hot {
		wantHits++
	}
	if thumbnail304Counter(t) != before304 || f.sink.count(repository.EventAccessed) != beforeAccess+1 || afterHits != wantHits {
		t.Errorf("revalidation outcome/access/cache counters changed: hits=%d want=%d", afterHits, wantHits)
	}
}

func TestThumbnailRevalidationCacheSelectsFreshObservation(t *testing.T) {
	for _, hot := range []bool{false, true} {
		f := newThumbnailRevalidationCacheFixture(t)
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, endpoint := range f.paths() {
				for _, condition := range thumbnailRevalidationCacheConditions(f) {
					checkThumbnailRevalidationCacheSelection(t, f, method, endpoint, condition, hot)
				}
			}
		}
	}
}

func TestThumbnailRevalidationCacheAuthorizationFailureIsTerminal(t *testing.T) {
	for _, failure := range []error{nil, errors.New("revalidation authorization unavailable")} {
		f := newThumbnailRevalidationCacheFixture(t)
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, endpoint := range f.paths() {
				provider := &readBoundaryProvider{rejectAt: 2, failure: failure}
				f.svc.WithAuthorizer(provider)
				beforeAccess := f.sink.count(repository.EventAccessed)
				hits, misses, _, _ := f.cache.Stats()
				response, _ := thumbnailMediaRevalidationRequest(t, f.thumbnailEncodingOpenFixture, method, endpoint, thumbnailRevalidationCacheConditions(f)[0])
				want := http.StatusForbidden
				if failure != nil {
					want = http.StatusInternalServerError
				}
				if response.StatusCode != want || provider.reads.Load() != 2 || f.sink.count(repository.EventAccessed) != beforeAccess {
					t.Errorf("revalidation failure bypassed through cache: status=%d checks=%d", response.StatusCode, provider.reads.Load())
				}
				assertThumbnailMediaOpenErrorHeaders(t, response)
				assertThumbnailRevalidationCacheUntouched(t, f, hits, misses)
			}
		}
	}
}

func assertThumbnailRevalidationCacheUntouched(t *testing.T, f *thumbnailRevalidationCacheFixture, hits, misses uint64) {
	t.Helper()
	afterHits, afterMisses, _, _ := f.cache.Stats()
	if afterHits != hits || afterMisses != misses || f.cache.Len() != 2 || f.repo.calls.Load() != 2 {
		t.Errorf("metadata-only outcome touched cache: hits=%d misses=%d entries=%d stats=%d", afterHits, afterMisses, f.cache.Len(), f.repo.calls.Load())
	}
	if f.store.opens.Load() != 0 || f.store.reads.Load() != 0 || f.store.closes.Load() != 0 {
		t.Error("metadata-only outcome touched source")
	}
}

func TestThumbnailRevalidationCacheMatchingFreshStill304(t *testing.T) {
	f := newThumbnailRevalidationCacheFixture(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, endpoint := range f.paths() {
			before304, beforeAccess := thumbnail304Counter(t), f.sink.count(repository.EventAccessed)
			hits, misses, _, _ := f.cache.Stats()
			condition := map[string]string{"If-None-Match": thumbnailEncodingValidator(f.older) + ", " + thumbnailEncodingValidator(f.current)}
			response, body := thumbnailMediaRevalidationRequest(t, f.thumbnailEncodingOpenFixture, method, endpoint, condition)
			if response.StatusCode != http.StatusNotModified || len(body) != 0 || response.Header.Get("ETag") != thumbnailEncodingValidator(f.current) {
				t.Errorf("matching fresh observation lost 304: status=%d headers=%v body=%q", response.StatusCode, response.Header, body)
			}
			if thumbnail304Counter(t) != before304+1 || f.sink.count(repository.EventAccessed) != beforeAccess {
				t.Error("304 changed outcome/access counters")
			}
			assertThumbnailRevalidationCacheUntouched(t, f, hits, misses)
		}
	}
}

func TestThumbnailRevalidationCacheMissStillReauthorizesOpen(t *testing.T) {
	for _, failure := range []error{nil, errors.New("opening authorization unavailable")} {
		f := newThumbnailRevalidationCacheFixture(t)
		f.cache.InvalidateSource(thumbnailSourceIdentity(f.current))
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, endpoint := range f.paths() {
				provider := &readBoundaryProvider{rejectAt: 3, failure: failure}
				f.svc.WithAuthorizer(provider)
				beforeAccess := f.sink.count(repository.EventAccessed)
				response, _ := thumbnailMediaRevalidationRequest(t, f.thumbnailEncodingOpenFixture, method, endpoint, thumbnailRevalidationCacheConditions(f)[0])
				want := http.StatusForbidden
				if failure != nil {
					want = http.StatusInternalServerError
				}
				if response.StatusCode != want || provider.reads.Load() != 3 || f.sink.count(repository.EventAccessed) != beforeAccess {
					t.Errorf("revalidation Stat used as open ticket: status=%d checks=%d", response.StatusCode, provider.reads.Load())
				}
				assertThumbnailMediaOpenErrorHeaders(t, response)
				assertThumbnailRevalidationCacheDeniedMiss(t, f)
			}
		}
	}
}

func assertThumbnailRevalidationCacheDeniedMiss(t *testing.T, f *thumbnailRevalidationCacheFixture) {
	t.Helper()
	if f.repo.calls.Load() != 3 || f.cache.Len() != 1 || f.store.opens.Load() != 0 {
		t.Errorf("denied open retried/reverted to cache A: stats=%d entries=%d opens=%d", f.repo.calls.Load(), f.cache.Len(), f.store.opens.Load())
	}
}

func TestThumbnailRevalidationCacheUsesFreshGenerationEligibility(t *testing.T) {
	f := newThumbnailRevalidationCacheFixture(t)
	// Model a legacy current row without the optional generation marker. Real
	// local Blob generations and both prewarmed cache entries are untouched.
	if err := f.repo.DeleteObjectMetaKey(context.Background(), service.DefaultTenant, service.DefaultBucket, f.current.Key, storage.GenerationMetadataKey); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, endpoint := range f.paths() {
			hits, misses, _, _ := f.cache.Stats()
			response, body := thumbnailMediaRevalidationRequest(t, f.thumbnailEncodingOpenFixture, method, endpoint, thumbnailRevalidationCacheConditions(f)[0])
			assertThumbnailEncodingOpenHealthy(t, response, body, method)
			assertThumbnailMediaOpenIdentity(t, f.thumbnailEncodingOpenFixture, response, false)
			assertThumbnailRevalidationCachePipeline(t, f, false)
			assertThumbnailRevalidationCacheNoLookup(t, f, hits, misses)
			if method == http.MethodGet && !bytes.Equal(body, f.currentJPEG) {
				t.Error("legacy B fallback served A bytes")
			}
		}
	}
}

func assertThumbnailRevalidationCacheNoLookup(t *testing.T, f *thumbnailRevalidationCacheFixture, hits, misses uint64) {
	t.Helper()
	afterHits, afterMisses, _, _ := f.cache.Stats()
	if afterHits != hits || afterMisses != misses || f.cache.Len() != 2 {
		t.Errorf("fresh bypass eligibility used cache: hits=%d misses=%d entries=%d", afterHits, afterMisses, f.cache.Len())
	}
}

func TestThumbnailRevalidationCacheUsesFreshEncryptionGate(t *testing.T) {
	f := newThumbnailRevalidationCacheFixture(t)
	// Seed the existing provider-managed metadata gate, not an actual KMS.
	if err := f.repo.SetObjectMetaKey(context.Background(), service.DefaultTenant, service.DefaultBucket, f.current.Key, "_aero_sse_algorithm", "aws:kms"); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, endpoint := range f.paths() {
			hits, misses, _, _ := f.cache.Stats()
			response, body := thumbnailMediaRevalidationRequest(t, f.thumbnailEncodingOpenFixture, method, endpoint, thumbnailRevalidationCacheConditions(f)[0])
			assertThumbnailRevalidationCacheResponse(t, f, response, body, method)
			assertThumbnailRevalidationCachePipeline(t, f, false)
			assertThumbnailRevalidationCacheNoLookup(t, f, hits, misses)
		}
	}
}

func TestThumbnailRevalidationCacheFreshProofDoesNotDependOnOldRow(t *testing.T) {
	f := newThumbnailRevalidationCacheFixture(t)
	// Only the initial seam observation models a legacy row. B's row/proof
	// remains complete, so its hot cache response must retain B's validator.
	f.repo.stale.Metadata = maps.Clone(f.older.Metadata)
	delete(f.repo.stale.Metadata, storage.GenerationMetadataKey)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, endpoint := range f.paths() {
			checkThumbnailRevalidationCacheSelection(t, f, method, endpoint, thumbnailRevalidationCacheConditions(f)[1], true)
		}
	}
}

func TestThumbnailRevalidationCacheHistoricalPinKeepsItsOwnEntry(t *testing.T) {
	f := newThumbnailRevalidationCacheFixture(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, endpoint := range f.paths() {
			checkThumbnailRevalidationCacheSelection(t, f, method, endpoint, thumbnailRevalidationCacheConditions(f)[0], true)
			response, body := f.request(t, method, endpoint+"&version="+f.older.VersionID)
			assertThumbnailEncodingOpenHealthy(t, response, body, method)
			if response.Header.Get("ETag") != thumbnailEncodingValidator(f.older) || response.Header.Get("X-Version-Id") != f.older.VersionID {
				t.Errorf("historical pin replaced with B: headers=%v", response.Header)
			}
			if method == http.MethodGet && !bytes.Equal(body, f.olderJPEG) {
				t.Error("historical A lost its cached bytes")
			}
			if f.repo.calls.Load() != 0 || f.store.opens.Load() != 0 || f.cache.Len() != 2 {
				t.Errorf("historical cache hit consulted current/opened: stats=%d opens=%d entries=%d", f.repo.calls.Load(), f.store.opens.Load(), f.cache.Len())
			}
		}
	}
}

func TestThumbnailRevalidationCacheOpenAPIDocumentsSelection(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]struct{ Summary string }
	}
	if err := json.Unmarshal(globalSpec.JSON(), &spec); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct{ path, method string }{
		{"/v1/files/{key}", "get"}, {"/v1/files/{key}", "head"}, {"/v1/files/{key}/thumbnail", "get"},
	} {
		summary := spec.Paths[check.path][check.method].Summary
		if !strings.Contains(summary, "newer authorized observation") {
			t.Errorf("%s %s missing revalidated cache/source selection: %q", check.method, check.path, summary)
		}
	}
}
