package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aero-vault/aero-vault/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// storeTracer is a package-level OTel tracer for creating nested spans in
// storage backend operations. When called from FileService.Get/Put (which
// already carry a tracing context), the span created here becomes a child
// of the service-level span, forming a trace like:
//
//	HTTP → FileService.Get → LocalStorage.Get → Repository.GetObject
var storeTracer = telemetry.Tracer("aero-vault/storage")

func (s *LocalStorage) metaPath(p string) string { return p + localMetaSuffix }

func (s *LocalStorage) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	return s.GetWithOptions(ctx, key, GetOptions{})
}

func (s *LocalStorage) GetGenerationBound(ctx context.Context, key string, expected ObjectInfo) (io.ReadCloser, ObjectInfo, error) {
	s.generationMu.RLock()
	defer s.generationMu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, ObjectInfo{}, err
	}
	path, err := s.objectPath(key)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	beforePath, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ObjectInfo{}, ErrNotFound
		}
		return nil, ObjectInfo{}, err
	}
	before, err := s.statObject(key, nil)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	if !boundInfoMatches(before, expected) {
		return nil, before, generationProofError(before, expected)
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ObjectInfo{}, ErrNotFound
		}
		return nil, ObjectInfo{}, err
	}
	openedPath, statErr := f.Stat()
	afterPath, pathErr := os.Stat(path)
	after, metaErr := s.statObject(key, nil)
	if statErr != nil || pathErr != nil || metaErr != nil ||
		!os.SameFile(beforePath, openedPath) || !os.SameFile(openedPath, afterPath) ||
		!boundInfoMatches(after, expected) {
		_ = f.Close()
		if errors.Is(metaErr, os.ErrNotExist) || errors.Is(pathErr, os.ErrNotExist) {
			return nil, ObjectInfo{}, ErrNotFound
		}
		if metaErr != nil {
			return nil, ObjectInfo{}, metaErr
		}
		return nil, after, ErrGenerationMismatch
	}
	meta, err := readMeta(s.metaPath(path))
	if err != nil {
		_ = f.Close()
		return nil, ObjectInfo{}, err
	}
	if meta.Envelope == "" {
		return f, after, nil
	}
	enc, err := s.readEncrypter(meta.Envelope, GetOptions{})
	if err != nil {
		_ = f.Close()
		return nil, ObjectInfo{}, err
	}
	rc, err := decryptReader(f, meta.Envelope, enc)
	_ = f.Close()
	if err != nil {
		return nil, ObjectInfo{}, fmt.Errorf("sse decrypt: %w", err)
	}
	return rc, after, nil
}

func boundInfoMatches(got, expected ObjectInfo) bool {
	generation := expected.Metadata[GenerationMetadataKey]
	return generation != "" && got.Key == expected.Key && got.ETag == expected.ETag &&
		got.Size == expected.Size && got.Metadata[GenerationMetadataKey] == generation
}

func generationProofError(got, expected ObjectInfo) error {
	if expected.Metadata[GenerationMetadataKey] == "" || got.Metadata[GenerationMetadataKey] == "" {
		return ErrUnsupported
	}
	return ErrGenerationMismatch
}

func (s *LocalStorage) GetWithOptions(ctx context.Context, key string, opts GetOptions) (io.ReadCloser, ObjectInfo, error) {
	ctx, span := storeTracer.Start(ctx, "LocalStorage.Get",
		trace.WithAttributes(attribute.String("key", key)),
	)
	defer span.End()

	info, err := s.StatWithOptions(ctx, key, opts)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	path, _ := s.objectPath(key)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ObjectInfo{}, ErrNotFound
		}
		return nil, ObjectInfo{}, err
	}
	meta, mErr := readMeta(s.metaPath(path))
	if mErr == nil && meta.Envelope != "" {
		enc, err := s.readEncrypter(meta.Envelope, opts)
		if err != nil {
			_ = f.Close()
			return nil, ObjectInfo{}, err
		}
		rc, err := decryptReader(f, meta.Envelope, enc)
		_ = f.Close()
		if err != nil {
			return nil, ObjectInfo{}, fmt.Errorf("sse decrypt: %w", err)
		}
		return rc, info, nil
	}
	return f, info, nil
}

func (s *LocalStorage) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	return s.statObject(key, nil)
}

func (s *LocalStorage) StatWithOptions(ctx context.Context, key string, opts GetOptions) (ObjectInfo, error) {
	return s.statObject(key, &opts)
}

func (s *LocalStorage) statObject(key string, opts *GetOptions) (ObjectInfo, error) {
	path, err := s.objectPath(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	meta, err := readMeta(s.metaPath(path))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if _, statErr := os.Stat(path); statErr == nil {
				// object exists without sidecar — fabricate metadata
				info, _ := os.Stat(path)
				return ObjectInfo{
					Key:          key,
					Size:         info.Size(),
					LastModified: info.ModTime().UTC(),
				}, nil
			}
			return ObjectInfo{}, ErrNotFound
		}
		return ObjectInfo{}, err
	}
	if meta.Envelope != "" && opts != nil {
		if _, err := s.readEncrypter(meta.Envelope, *opts); err != nil {
			return ObjectInfo{}, err
		}
	}
	return meta.toInfo(), nil
}

func (s *LocalStorage) readEncrypter(envelope string, opts GetOptions) (*envelopeEncrypter, error) {
	env, err := parseEnvelope(envelope)
	if err != nil {
		return nil, err
	}
	if env.Kid == ssecKeyID {
		if len(opts.SSECustomerKey) != masterKeyLen {
			return nil, ErrSSECustomerKeyRequired
		}
		enc := newEnvelopeEncrypter(newSSECProvider(opts.SSECustomerKey))
		if err := enc.validateEnvelope(envelope); err != nil {
			return nil, ErrInvalidSSECustomerKey
		}
		return enc, nil
	}
	if len(opts.SSECustomerKey) != 0 {
		return nil, ErrInvalidSSECustomerKey
	}
	if s.enc == nil {
		return nil, errors.New("sse: encrypted object but no server-side key is configured")
	}
	if err := s.enc.validateEnvelope(envelope); err != nil {
		return nil, err
	}
	return s.enc, nil
}

func (s *LocalStorage) SupportsSSEC() bool { return true }

func (s *LocalStorage) PresignGet(ctx context.Context, key string, expiry time.Duration) (string, error) {
	return s.presign(key, "GET", expiry)
}

func (s *LocalStorage) PresignPut(ctx context.Context, key string, expiry time.Duration) (string, error) {
	return s.presign(key, "PUT", expiry)
}

func (s *LocalStorage) presign(key, method string, expiry time.Duration) (string, error) {
	if s.cfg.PublicURL == "" || s.cfg.SignKey == "" {
		return "", errors.New("local presign disabled: configure PublicURL and SignKey")
	}
	if _, err := s.objectPath(key); err != nil {
		return "", err
	}
	exp := time.Now().Add(expiry).Unix()
	sig := signLocal(s.cfg.SignKey, method, key, exp)
	base := strings.TrimRight(s.cfg.PublicURL, "/")
	q := url.Values{}
	q.Set("expires", fmt.Sprintf("%d", exp))
	q.Set("sig", sig)
	q.Set("method", method)
	return fmt.Sprintf("%s/%s?%s", base, url.PathEscape(key), q.Encode()), nil
}

// VerifyLocalSig validates a presigned-URL signature. Exposed so the HTTP layer can gate downloads.
func VerifyLocalSig(signKey, method, key string, expires int64, sig string) bool {
	if signKey == "" {
		return false
	}
	if time.Now().Unix() > expires {
		return false
	}
	want := signLocal(signKey, method, key, expires)
	return hmacEqual(want, sig)
}
