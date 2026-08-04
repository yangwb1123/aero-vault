package service

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"
	"sync"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// ── Get ─────────────────────────────────────────────────────────────────────

func (s *FileService) Get(ctx context.Context, tenant, bucket, key string) (io.ReadCloser, repository.Object, error) {
	return s.GetWithOptions(ctx, tenant, bucket, key, ReadOptions{})
}

func (s *FileService) GetWithOptions(ctx context.Context, tenant, bucket, key string, opts ReadOptions) (io.ReadCloser, repository.Object, error) {
	ctx, span := tracer.Start(ctx, "FileService.Get",
		trace.WithAttributes(
			attribute.String("tenant", tenant),
			attribute.String("bucket", bucket),
			attribute.String("key", key),
		),
	)
	defer span.End()
	tenant, bucket, err := checkedObjectDefaults(tenant, bucket, key)
	if err != nil {
		return nil, repository.Object{}, err
	}
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
	if err := s.authorizeObject(ctx, access.ActionRead, obj); err != nil {
		return nil, repository.Object{}, err
	}
	if err := validateSSECRead(obj.Metadata, opts); err != nil {
		return nil, repository.Object{}, err
	}
	rc, err := s.openObjectWithOptions(ctx, obj, opts)
	if err != nil {
		return nil, repository.Object{}, err
	}
	return rc, obj, nil
}

func (s *FileService) openObject(ctx context.Context, obj repository.Object) (io.ReadCloser, error) {
	return s.openObjectWithOptions(ctx, obj, ReadOptions{})
}

func (s *FileService) openObjectWithOptions(ctx context.Context, obj repository.Object, opts ReadOptions) (io.ReadCloser, error) {
	var (
		rc  io.ReadCloser
		err error
	)
	if objectUsesSSEC(obj.Metadata) {
		secure, ok := s.store.(storage.SSECStorage)
		if !ok || !secure.SupportsSSEC() {
			return nil, fmt.Errorf("%w: storage backend does not support SSE-C", ErrInvalidArgs)
		}
		rc, _, err = secure.GetWithOptions(ctx, obj.StorageKey, storageGetOptions(opts))
	} else {
		rc, _, err = s.store.Get(ctx, obj.StorageKey)
	}
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	s.emit(ctx, obj, repository.EventAccessed)
	if expected, ok := objectVerificationMD5(obj); s.readVerify.Enabled && ok {
		rc = NewSamplingETagVerifier(rc, ETagVerifierConfig{
			Expected:   expected,
			MaxSize:    s.readVerify.MaxSize,
			Sample:     s.readVerify.Sample,
			ObjectSize: obj.Size,
		})
	}
	return rc, nil
}

func objectVerificationMD5(obj repository.Object) (string, bool) {
	if encoded := obj.Metadata["_aero_content_md5"]; encoded != "" {
		if digest, err := base64.StdEncoding.DecodeString(encoded); err == nil && len(digest) == md5.Size {
			return hex.EncodeToString(digest), true
		}
		if digest, ok := normalizeMD5Hex(encoded); ok {
			return digest, true
		}
	}
	return normalizeMD5Hex(obj.ETag)
}

func normalizeMD5Hex(value string) (string, bool) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "W/")
	value = strings.Trim(value, `"`)
	if len(value) != md5.Size*2 {
		return "", false
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", false
	}
	return strings.ToLower(value), true
}

// ── Stat ────────────────────────────────────────────────────────────────────

func (s *FileService) Stat(ctx context.Context, tenant, bucket, key string) (repository.Object, error) {
	return s.statObject(ctx, tenant, bucket, key, nil)
}

func (s *FileService) StatWithOptions(ctx context.Context, tenant, bucket, key string, opts ReadOptions) (repository.Object, error) {
	return s.statObject(ctx, tenant, bucket, key, &opts)
}

func (s *FileService) statObject(ctx context.Context, tenant, bucket, key string, opts *ReadOptions) (repository.Object, error) {
	tenant, bucket, err := checkedObjectDefaults(tenant, bucket, key)
	if err != nil {
		return repository.Object{}, err
	}
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
	if err := s.authorizeObject(ctx, access.ActionRead, obj); err != nil {
		return repository.Object{}, err
	}
	if opts != nil {
		if err := validateSSECRead(obj.Metadata, *opts); err != nil {
			return repository.Object{}, err
		}
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
// reading and compares it against the expected ETag (hex-encoded) after the
// complete object has been consumed. Closing a partial read does not mark a
// healthy object corrupt.
type ETagVerifier struct {
	r            io.ReadCloser
	expected     string
	expectedSize int64
	md5          hash.Hash
	mu           sync.Mutex
	bytesRead    int64
	reachedEOF   bool
	verified     bool
	verifyErr    error
	closed       bool
	closeErr     error
}

func NewETagVerifier(r io.ReadCloser, expected string) *ETagVerifier {
	return newETagVerifier(r, expected, -1)
}

func newETagVerifier(r io.ReadCloser, expected string, expectedSize int64) *ETagVerifier {
	return &ETagVerifier{
		r: r, expected: expected, expectedSize: expectedSize, md5: md5.New(),
	}
}

func (v *ETagVerifier) Read(p []byte) (int, error) {
	n, err := v.r.Read(p)
	v.mu.Lock()
	if n > 0 {
		v.md5.Write(p[:n])
		v.bytesRead += int64(n)
	}
	if errors.Is(err, io.EOF) {
		v.reachedEOF = true
		if verifyErr := v.verifyLocked(); verifyErr != nil {
			err = verifyErr
		}
	}
	v.mu.Unlock()
	return n, err
}

func (v *ETagVerifier) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return v.closeErr
	}
	v.closed = true
	v.closeErr = v.r.Close()
	if verifyErr := v.verifyLocked(); verifyErr != nil {
		v.closeErr = verifyErr
	}
	return v.closeErr
}

func (v *ETagVerifier) verifyLocked() error {
	complete := v.reachedEOF ||
		(v.expectedSize > 0 && v.bytesRead == v.expectedSize)
	if !complete || v.verified {
		return v.verifyErr
	}
	v.verified = true
	computed := hex.EncodeToString(v.md5.Sum(nil))
	if !strings.EqualFold(computed, v.expected) {
		v.verifyErr = fmt.Errorf(
			"%w: expected %s, computed %s", ErrObjectCorrupt, v.expected, computed,
		)
	}
	return v.verifyErr
}

// NewSamplingETagVerifier creates an ETagVerifier that uses sampling for large
// objects. When the object exceeds MaxSize and Sample is enabled, the verifier
// reads the full content (the verifier wraps the full reader) — the sampling
// is a cue for whether to verify the whole body (small) or use a byte-range
// approach (large). For now, it always verifies the full content.
func NewSamplingETagVerifier(r io.ReadCloser, cfg ETagVerifierConfig) *ETagVerifier {
	return newETagVerifier(r, cfg.Expected, cfg.ObjectSize)
}
