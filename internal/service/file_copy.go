package service

import (
	"context"
	"io"
	"strings"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// CopyObject copies an object through FileService so encryption, quota,
// protection and event rules are applied consistently.
func (s *FileService) CopyObject(
	ctx context.Context,
	tenant, srcBucket, srcKey, srcVersionID, dstBucket, dstKey string,
	source ReadOptions,
	destination PutOptions,
	replaceMetadata bool,
	replaceTags bool,
) (repository.Object, error) {
	var (
		rc  io.ReadCloser
		src repository.Object
		err error
	)
	if srcVersionID == "" {
		rc, src, err = s.GetWithOptions(ctx, tenant, srcBucket, srcKey, source)
	} else {
		rc, src, err = s.GetVersionWithOptions(ctx, tenant, srcBucket, srcKey, srcVersionID, source)
	}
	if err != nil {
		return repository.Object{}, err
	}
	defer rc.Close()
	if !replaceMetadata {
		destination.ContentType = src.ContentType
		destination.Metadata = userMetadata(src.Metadata)
		destination.ContentDisposition = src.Metadata["_aero_content_disposition"]
		destination.ContentEncoding = src.Metadata["_aero_content_encoding"]
		destination.ContentMD5 = src.Metadata["_aero_content_md5"]
		destination.StorageClass = src.StorageClass
	}
	if !replaceTags {
		destination.Tags = cloneMetadata(src.Tags)
	}
	return s.Put(ctx, tenant, dstBucket, dstKey, rc, src.Size, destination)
}

func metadataWithoutEncryption(meta map[string]string) map[string]string {
	copied := metadataWithoutManagedSSE(meta)
	delete(copied, ssecAlgorithmMeta)
	delete(copied, ssecKeyMD5Meta)
	return copied
}

func userMetadata(meta map[string]string) map[string]string {
	user := make(map[string]string, len(meta))
	for key, value := range meta {
		if !strings.HasPrefix(strings.ToLower(key), "_aero_") {
			user[key] = value
		}
	}
	if len(user) == 0 {
		return nil
	}
	return user
}

func systemMetadata(meta map[string]string) map[string]string {
	system := make(map[string]string)
	for key, value := range meta {
		if strings.HasPrefix(strings.ToLower(key), "_aero_") {
			system[key] = value
		}
	}
	return system
}

// copyRangeReader returns a bounded plaintext source reader.
func copyRangeReader(rc io.ReadCloser, offset, length, objectSize int64) (io.Reader, int64, error) {
	if offset < 0 {
		return rc, objectSize, nil
	}
	if _, err := io.CopyN(io.Discard, rc, offset); err != nil {
		return nil, 0, err
	}
	return io.LimitReader(rc, length), length, nil
}
