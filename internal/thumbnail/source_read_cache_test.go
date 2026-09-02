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
	var opened, closed atomic.Int32
	beforeLen, beforeBytes := cache.Len(), cache.Bytes()
	open := func() (io.ReadCloser, OpenedSource, error) {
		opened.Add(1)
		source := &completePayloadErrorReader{payload: makeJPEG(t, 8, 8), err: injected}
		return &recordingReadCloser{Reader: source, closed: &closed}, OpenedSource{
			Identity: identity, ETag: etagA, Bound: true,
		}, nil
	}
	for i := 0; i < 2; i++ {
		img, fromCache, err := GenerateContextWithOpenerCached(
			context.Background(), cache, identity, etagA, 32, 32, open,
		)
		assertSourceReadFailure(t, img, err, injected)
		if fromCache {
			t.Fatalf("request %d fromCache = true, want false for a failed generation", i+1)
		}
		if cache.Len() != beforeLen || cache.Bytes() != beforeBytes {
			t.Fatalf("request %d changed cache: len=%d/%d bytes=%d/%d", i+1,
				cache.Len(), beforeLen, cache.Bytes(), beforeBytes)
		}
	}
	if got := opened.Load(); got != 2 {
		t.Fatalf("opener calls = %d, want two fresh source streams", got)
	}
	if got := closed.Load(); got != 2 {
		t.Fatalf("source close count = %d, want exactly two", got)
	}
}
