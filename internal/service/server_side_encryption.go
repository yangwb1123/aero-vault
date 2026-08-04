package service

import (
	"fmt"
	"strings"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
)

const (
	sseAlgorithmMetadata = "_aero_sse_algorithm"
	sseKMSKeyIDMetadata  = "_aero_sse_kms_key_id"
)

func (s *FileService) prepareServerSideEncryption(
	opts *PutOptions, bucket repository.BucketConfig,
) error {
	if len(opts.SSECustomerKey) != 0 {
		if opts.SSEAlgorithm != "" || opts.SSEKMSKeyID != "" {
			return fmt.Errorf("%w: SSE-C and provider-managed SSE are mutually exclusive", ErrInvalidArgs)
		}
		return nil
	}
	algorithm := strings.TrimSpace(opts.SSEAlgorithm)
	keyID := strings.TrimSpace(opts.SSEKMSKeyID)
	if algorithm == "" {
		if keyID != "" {
			return fmt.Errorf("%w: KMS key requires aws:kms", ErrInvalidArgs)
		}
		algorithm, keyID = bucket.SSEAlgorithm, bucket.SSEKMSKeyId
	} else if algorithm == "aws:kms" && keyID == "" && bucket.SSEAlgorithm == "aws:kms" {
		keyID = bucket.SSEKMSKeyId
	}
	if algorithm == "" {
		opts.Metadata = metadataWithoutManagedSSE(opts.Metadata)
		return nil
	}
	if algorithm != "AES256" && algorithm != "aws:kms" {
		return fmt.Errorf("%w: unsupported SSE algorithm", ErrInvalidArgs)
	}
	if algorithm == "AES256" && keyID != "" {
		return fmt.Errorf("%w: KMS key is valid only with aws:kms", ErrInvalidArgs)
	}
	if !storage.SupportsServerSideEncryption(s.store, algorithm, keyID) {
		return fmt.Errorf("%w: storage backend cannot satisfy requested SSE", ErrInvalidArgs)
	}
	opts.SSEAlgorithm, opts.SSEKMSKeyID = algorithm, keyID
	opts.Metadata = metadataWithoutManagedSSE(opts.Metadata)
	opts.Metadata[sseAlgorithmMetadata] = algorithm
	if keyID != "" {
		opts.Metadata[sseKMSKeyIDMetadata] = keyID
	}
	return nil
}

func metadataWithoutManagedSSE(meta map[string]string) map[string]string {
	copied := cloneMetadata(meta)
	delete(copied, sseAlgorithmMetadata)
	delete(copied, sseKMSKeyIDMetadata)
	return copied
}

// ServerSideEncryptionInfo returns response-safe provider-managed SSE metadata.
func ServerSideEncryptionInfo(meta map[string]string) (algorithm, keyID string, ok bool) {
	algorithm = meta[sseAlgorithmMetadata]
	if algorithm != "AES256" && algorithm != "aws:kms" {
		return "", "", false
	}
	return algorithm, meta[sseKMSKeyIDMetadata], true
}
