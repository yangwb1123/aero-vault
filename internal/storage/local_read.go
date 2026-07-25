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
	ctx, span := storeTracer.Start(ctx, "LocalStorage.Get",
		trace.WithAttributes(attribute.String("key", key)),
	)
	defer span.End()

	info, err := s.Stat(ctx, key)
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
	// Decrypt on the fly if the object has an SSE envelope.
	if s.enc != nil {
		meta, mErr := readMeta(s.metaPath(path))
		if mErr == nil && meta.Envelope != "" {
			rc, err := decryptReader(f, meta.Envelope, s.enc)
			_ = f.Close()
			if err != nil {
				return nil, ObjectInfo{}, fmt.Errorf("sse decrypt: %w", err)
			}
			return rc, info, nil
		}
	}
	return f, info, nil
}

func (s *LocalStorage) Stat(ctx context.Context, key string) (ObjectInfo, error) {
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
	return meta.toInfo(), nil
}

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
