package storage

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func (s *LocalStorage) Put(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) (ObjectInfo, error) {
	ctx, span := storeTracer.Start(ctx, "LocalStorage.Put",
		trace.WithAttributes(
			attribute.String("key", key),
			attribute.Int64("size", size),
		),
	)
	defer span.End()
	if err := s.validateServerSideEncryption(opts); err != nil {
		return ObjectInfo{}, err
	}

	path, err := s.objectPath(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return ObjectInfo{}, err
	}
	meta, err := s.writeObject(ctx, path, key, r, size, opts)
	if err != nil {
		return ObjectInfo{}, err
	}
	if err := writeMeta(s.metaPath(path), meta); err != nil {
		_ = os.Remove(path)
		return ObjectInfo{}, err
	}
	return meta.toInfo(), nil
}

func (s *LocalStorage) validateServerSideEncryption(opts PutOptions) error {
	if opts.SSEAlgorithm == "" {
		if opts.SSEKMSKeyID != "" {
			return ErrUnsupported
		}
		return nil
	}
	if len(opts.SSECustomerKey) != 0 ||
		!s.SupportsServerSideEncryption(opts.SSEAlgorithm, opts.SSEKMSKeyID) {
		return ErrUnsupported
	}
	return nil
}

func (s *LocalStorage) writeObject(ctx context.Context, path, key string, r io.Reader, size int64, opts PutOptions) (localMeta, error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".upload-*")
	if err != nil {
		return localMeta{}, err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	h := md5.New()
	var (
		reader   io.Reader = io.TeeReader(r, h)
		envelope string
		enc      = s.enc
	)
	// SSE-C: use customer-provided key instead of server-side encrypter.
	if len(opts.SSECustomerKey) == 32 {
		enc = newEnvelopeEncrypter(newSSECProvider(opts.SSECustomerKey))
	}
	if enc != nil {
		plain, err := io.ReadAll(reader)
		if err != nil {
			return localMeta{}, err
		}
		ct, env, err := enc.encrypt(plain)
		if err != nil {
			return localMeta{}, err
		}
		envelope = env
		reader = bytesReader(ct)
	}
	written, err := io.Copy(tmp, reader)
	if err != nil {
		return localMeta{}, err
	}
	if err := tmp.Sync(); err != nil {
		return localMeta{}, err
	}
	if err := tmp.Close(); err != nil {
		return localMeta{}, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return localMeta{}, err
	}
	generation, err := newGeneration()
	if err != nil {
		return localMeta{}, err
	}
	meta := cloneStorageMetadata(opts.Metadata)
	meta[GenerationMetadataKey] = generation
	return localMeta{
		Key:          key,
		Size:         plaintextSize(written, enc != nil),
		ETag:         hex.EncodeToString(h.Sum(nil)),
		ContentType:  opts.ContentType,
		LastModified: time.Now().UTC(),
		Metadata:     meta,
		Envelope:     envelope,
	}, nil
}

func plaintextSize(written int64, encrypted bool) int64 {
	if !encrypted {
		return written
	}
	s := written - 16
	if s < 0 {
		return 0
	}
	return s
}

func (s *LocalStorage) Delete(ctx context.Context, key string) error {
	path, err := s.objectPath(key)
	if err != nil {
		return err
	}
	_ = os.Remove(s.metaPath(path))
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func newGeneration() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func cloneStorageMetadata(meta map[string]string) map[string]string {
	if len(meta) == 0 {
		return make(map[string]string, 1)
	}
	clone := make(map[string]string, len(meta)+1)
	for key, value := range meta {
		clone[key] = value
	}
	return clone
}

// bytesReader is a tiny adapter so we can swap a *bytes.Reader into the io.Reader chain.
func bytesReader(b []byte) io.Reader { return &byteSliceReader{b: b} }

type byteSliceReader struct {
	b   []byte
	off int
}

func (r *byteSliceReader) Read(p []byte) (int, error) {
	if r.off >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.off:])
	r.off += n
	return n, nil
}
