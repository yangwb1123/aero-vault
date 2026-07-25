package service

import (
	"compress/gzip"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"
	"sync"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// ── Get ─────────────────────────────────────────────────────────────────────

func (s *FileService) Get(ctx context.Context, tenant, bucket, key string) (io.ReadCloser, repository.Object, error) {
	ctx, span := tracer.Start(ctx, "FileService.Get",
		trace.WithAttributes(
			attribute.String("tenant", tenant),
			attribute.String("bucket", bucket),
			attribute.String("key", key),
		),
	)
	defer span.End()
	tenant, bucket = defaults(tenant, bucket)
	obj, err := s.repo.GetObject(ctx, tenant, bucket, key)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, repository.Object{}, ErrNotFound
		}
		return nil, repository.Object{}, err
	}
	if err := checkCorrupt(obj); err != nil {
		return nil, repository.Object{}, err
	}
	rc, _, err := s.store.Get(ctx, obj.StorageKey)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, repository.Object{}, ErrNotFound
		}
		return nil, repository.Object{}, err
	}
	s.emit(ctx, obj, repository.EventAccessed)
	if obj.Metadata["_aero_content_encoding"] == "gzip" {
		gr, err := gzip.NewReader(rc)
		if err != nil {
			rc.Close()
			return nil, repository.Object{}, fmt.Errorf("gzip decompress: %w", err)
		}
		rc = &gzipReadCloser{gr, rc}
	}
	if s.readVerify.Enabled {
		expected := obj.ETag
		if md5Val, ok := obj.Metadata["_aero_content_md5"]; ok && md5Val != "" {
			expected = md5Val
		}
		if expected != "" {
			rc = NewSamplingETagVerifier(rc, ETagVerifierConfig{
				Expected:   expected,
				MaxSize:    s.readVerify.MaxSize,
				Sample:     s.readVerify.Sample,
				ObjectSize: obj.Size,
			})
		}
	}
	return rc, obj, nil
}

// gzipReadCloser wraps a gzip.Reader so it closes the underlying reader.
type gzipReadCloser struct {
	*gzip.Reader
	raw io.ReadCloser
}

func (g *gzipReadCloser) Close() error {
	err := g.Reader.Close()
	if rerr := g.raw.Close(); rerr != nil && err == nil {
		err = rerr
	}
	return err
}

// ── Stat ────────────────────────────────────────────────────────────────────

func (s *FileService) Stat(ctx context.Context, tenant, bucket, key string) (repository.Object, error) {
	tenant, bucket = defaults(tenant, bucket)
	obj, err := s.repo.GetObject(ctx, tenant, bucket, key)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return repository.Object{}, ErrNotFound
		}
		return repository.Object{}, err
	}
	if err := checkCorrupt(obj); err != nil {
		return repository.Object{}, err
	}
	return obj, nil
}

// ── ETag Verification ───────────────────────────────────────────────────────

// ETagVerifierConfig controls the behaviour of ETagVerifier / SamplingETagVerifier.
type ETagVerifierConfig struct {
	Expected   string // expected content hash (hex-encoded)
	MaxSize    int64  // objects larger than this trigger sampling
	Sample     bool   // enable byte-range sampling for large objects
	ObjectSize int64  // total object size
}

// ETagVerifier is an io.ReadCloser that computes the MD5 of the content while
// reading and compares it against the expected ETag (hex-encoded) on close.
type ETagVerifier struct {
	r        io.ReadCloser
	expected string
	md5      hash.Hash
	mu       sync.Mutex
	verified bool
}

func NewETagVerifier(r io.ReadCloser, expected string) *ETagVerifier {
	return &ETagVerifier{r: r, expected: expected, md5: md5.New()}
}

func (v *ETagVerifier) Read(p []byte) (int, error) {
	n, err := v.r.Read(p)
	if n > 0 {
		v.mu.Lock()
		v.md5.Write(p[:n])
		v.mu.Unlock()
	}
	return n, err
}

func (v *ETagVerifier) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.verified {
		return v.r.Close()
	}
	v.verified = true
	computed := hex.EncodeToString(v.md5.Sum(nil))
	if !strings.EqualFold(computed, v.expected) {
		_ = v.r.Close()
		return fmt.Errorf("%w: expected %s, computed %s", ErrObjectCorrupt, v.expected, computed)
	}
	return v.r.Close()
}

// NewSamplingETagVerifier creates an ETagVerifier that uses sampling for large
// objects. When the object exceeds MaxSize and Sample is enabled, the verifier
// reads the full content (the verifier wraps the full reader) — the sampling
// is a cue for whether to verify the whole body (small) or use a byte-range
// approach (large). For now, it always verifies the full content.
func NewSamplingETagVerifier(r io.ReadCloser, cfg ETagVerifierConfig) *ETagVerifier {
	return NewETagVerifier(r, cfg.Expected)
}
