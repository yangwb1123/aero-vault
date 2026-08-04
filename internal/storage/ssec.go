package storage

import (
	"context"
	"io"
)

// GetOptions carries request-scoped read settings. SSE-C keys are never
// persisted by the storage layer.
type GetOptions struct {
	SSECustomerKey    []byte
	SSECustomerKeyMD5 []byte
}

// SSECStorage is implemented by backends that can apply a customer-provided
// encryption key to reads and multipart operations.
type SSECStorage interface {
	SupportsSSEC() bool
	GetWithOptions(ctx context.Context, key string, opts GetOptions) (io.ReadCloser, ObjectInfo, error)
	StatWithOptions(ctx context.Context, key string, opts GetOptions) (ObjectInfo, error)
	UploadPartWithOptions(
		ctx context.Context,
		key, uploadID string,
		partNumber int32,
		r io.Reader,
		size int64,
		opts PutOptions,
	) (MultipartPart, error)
	CompleteMultipartWithOptions(
		ctx context.Context,
		key, uploadID string,
		parts []MultipartPart,
		opts PutOptions,
	) (ObjectInfo, error)
}

// SupportsSSEC reports whether a storage wrapper and its underlying backend
// implement the complete request-scoped SSE-C contract.
func SupportsSSEC(store Storage) bool {
	secure, ok := store.(SSECStorage)
	return ok && secure.SupportsSSEC()
}

// ServerSideEncryptionStorage advertises provider-managed SSE support.
type ServerSideEncryptionStorage interface {
	SupportsServerSideEncryption(algorithm, keyID string) bool
}

func SupportsServerSideEncryption(store Storage, algorithm, keyID string) bool {
	encrypted, ok := store.(ServerSideEncryptionStorage)
	return ok && encrypted.SupportsServerSideEncryption(algorithm, keyID)
}
