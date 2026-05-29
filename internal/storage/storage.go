package storage

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	ErrNotFound      = errors.New("object not found")
	ErrAlreadyExists = errors.New("object already exists")
	ErrInvalidKey    = errors.New("invalid object key")
)

// ObjectInfo describes a stored object.
type ObjectInfo struct {
	Key          string
	Size         int64
	ETag         string
	ContentType  string
	LastModified time.Time
	Metadata     map[string]string
}

// PutOptions controls how an object is written.
type PutOptions struct {
	ContentType string
	Metadata    map[string]string
}

// ListResult is one page of a List call.
type ListResult struct {
	Objects    []ObjectInfo
	NextMarker string
	HasMore    bool
}

// MultipartInit returns the upload ID used by subsequent part uploads.
type MultipartInit struct {
	Key      string
	UploadID string
}

// MultipartPart describes an uploaded part to be completed.
type MultipartPart struct {
	PartNumber int32
	ETag       string
}

// Storage is the contract every backend (local FS, S3, OSS, ...) implements.
//
// Implementations must be safe for concurrent use.
type Storage interface {
	// Put writes the contents of r under key. The size hint may be -1 if unknown.
	Put(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) (ObjectInfo, error)

	// Get streams an object. The caller MUST close the returned reader.
	Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)

	// Stat returns metadata without the body.
	Stat(ctx context.Context, key string) (ObjectInfo, error)

	// Delete removes an object. Deleting a missing key is not an error.
	Delete(ctx context.Context, key string) error

	// List enumerates objects sharing a prefix. Use marker for pagination.
	List(ctx context.Context, prefix, marker string, limit int) (ListResult, error)

	// PresignGet returns a time-limited URL that downloads the object.
	PresignGet(ctx context.Context, key string, expiry time.Duration) (string, error)

	// PresignPut returns a time-limited URL that uploads to the object.
	PresignPut(ctx context.Context, key string, expiry time.Duration) (string, error)

	// InitMultipart starts a multipart upload session.
	InitMultipart(ctx context.Context, key string, opts PutOptions) (MultipartInit, error)

	// UploadPart writes a single part.
	UploadPart(ctx context.Context, key, uploadID string, partNumber int32, r io.Reader, size int64) (MultipartPart, error)

	// CompleteMultipart finalizes the upload by stitching parts together.
	CompleteMultipart(ctx context.Context, key, uploadID string, parts []MultipartPart) (ObjectInfo, error)

	// AbortMultipart cancels a multipart upload and discards its parts.
	AbortMultipart(ctx context.Context, key, uploadID string) error

	// Backend identifies the underlying provider (e.g. "local", "s3").
	Backend() string
}
