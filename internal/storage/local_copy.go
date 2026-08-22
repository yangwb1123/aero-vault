package storage

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// CanCopy returns true — the local backend supports server-side copy via
// direct file I/O without allocating large intermediate buffers.
func (s *LocalStorage) CanCopy() bool { return true }

// Copy duplicates the object at srcKey to dstKey on the local filesystem.
// It streams the source body through a 32 KiB buffer (io.Copy's default)
// without loading the entire object into memory.
//
// SSE envelopes are preserved: the copied object retains the same encrypted
// blob and envelope, so no re-encryption is needed.
//
// Metadata handling:
//   - "COPY" (default): source metadata and content type are preserved.
//   - "REPLACE": the provided Metadata and ContentType from opts are used.
func (s *LocalStorage) Copy(ctx context.Context, srcKey, dstKey string, opts CopyOptions) (ObjectInfo, error) {
	ctx, span := storeTracer.Start(ctx, "LocalStorage.Copy",
		trace.WithAttributes(
			attribute.String("src_key", srcKey),
			attribute.String("dst_key", dstKey),
			attribute.String("metadata_directive", orDefault(opts.MetadataDirective, "COPY")),
		),
	)
	defer span.End()

	// Resolve source path and read its metadata.
	srcPath, err := s.objectPath(srcKey)
	if err != nil {
		return ObjectInfo{}, err
	}
	meta, err := readMeta(s.metaPath(srcPath))
	if err != nil {
		if os.IsNotExist(err) {
			return ObjectInfo{}, ErrNotFound
		}
		return ObjectInfo{}, err
	}

	// Resolve destination path and ensure parent directory exists.
	dstPath, err := s.objectPath(dstKey)
	if err != nil {
		return ObjectInfo{}, err
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return ObjectInfo{}, err
	}

	// Stream source to destination via temp file (atomic write).
	srcFile, err := os.Open(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ObjectInfo{}, ErrNotFound
		}
		return ObjectInfo{}, err
	}
	defer srcFile.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dstPath), ".upload-*")
	if err != nil {
		return ObjectInfo{}, err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	written, err := io.Copy(tmp, srcFile)
	if err != nil {
		return ObjectInfo{}, err
	}
	if err := tmp.Sync(); err != nil {
		return ObjectInfo{}, err
	}
	if err := tmp.Close(); err != nil {
		return ObjectInfo{}, err
	}
	srcFile.Close()

	if err := os.Rename(tmpName, dstPath); err != nil {
		return ObjectInfo{}, err
	}

	// Build destination metadata: copy source, then apply REPLACE overrides.
	dstMeta := meta
	dstMeta.Key = dstKey
	// The copied blob and its sidecar envelope come from the source.  The
	// destination instance may have a different SSE configuration, so its
	// encrypter is not authoritative for the number of plaintext bytes.
	dstMeta.Size = plaintextSize(written, meta.Envelope != "")
	if opts.MetadataDirective == "REPLACE" {
		if opts.Metadata != nil {
			dstMeta.Metadata = cloneMap(opts.Metadata)
		}
		if opts.ContentType != "" {
			dstMeta.ContentType = opts.ContentType
		}
	}

	if err := writeMeta(s.metaPath(dstPath), dstMeta); err != nil {
		_ = os.Remove(dstPath)
		return ObjectInfo{}, err
	}

	return dstMeta.toInfo(), nil
}

// cloneMap returns a shallow copy of m (nil-safe).
func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// orDefault returns value if non-empty, otherwise fallback.
func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
