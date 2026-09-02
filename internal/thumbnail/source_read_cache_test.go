package thumbnail

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
)

func TestCachedGenerationRejectsCompletePayloadSourceError(t *testing.T) {
	cache := NewCache(1<<20, 0)
	identity := testIdentity("source-error-tenant")
	injected := errors.New("source failed after producing the cached image")
	source := &completePayloadErrorReader{payload: makeJPEG(t, 8, 8), err: injected}
	var closed atomic.Int32
	rc := &recordingReadCloser{Reader: source, closed: &closed}
	beforeLen, beforeBytes := cache.Len(), cache.Bytes()

	img, fromCache, err := GenerateContextWithOpenerCached(
		context.Background(), cache, identity, etagA, 32, 32,
		func() (io.ReadCloser, OpenedSource, error) {
			return rc, OpenedSource{Identity: identity, ETag: etagA, Bound: true}, nil
		},
	)
	assertSourceReadFailure(t, img, err, injected)
	if fromCache {
		t.Fatal("fromCache = true, want false for a failed generation")
	}
	if cache.Len() != beforeLen || cache.Bytes() != beforeBytes {
		t.Fatalf("cache changed: len=%d/%d bytes=%d/%d", cache.Len(), beforeLen, cache.Bytes(), beforeBytes)
	}
	if got := closed.Load(); got != 1 {
		t.Fatalf("source close count = %d, want exactly one", got)
	}
	if source.reads != 1 {
		t.Fatalf("source reads = %d, want exactly one read", source.reads)
	}
}
