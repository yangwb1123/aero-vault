package storage

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

var (
	ErrNotFound      = errors.New("object not found")
	ErrAlreadyExists = errors.New("object already exists")
	ErrInvalidKey    = errors.New("invalid object key")
	ErrUnsupported   = errors.New("operation not supported by this backend")
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
	// SSECustomerKey is an AES-256 key for server-side encryption with
	// customer-provided key (SSE-C). When set, the key is used to encrypt the
	// object on write and MUST be provided on subsequent read/stat/delete.
	// Length must be 32 bytes for AES-256.
	SSECustomerKey []byte
	// SSECustomerKeyMD5 is the MD5 of SSECustomerKey for integrity validation.
	SSECustomerKeyMD5 []byte
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

// CopyOptions controls how Copy transfers an object between keys.
type CopyOptions struct {
	// MetadataDirective controls metadata handling during copy.
	// "COPY" preserves source metadata (default).
	// "REPLACE" uses the Metadata and ContentType fields.
	MetadataDirective string

	// Metadata replaces source metadata when MetadataDirective is "REPLACE".
	Metadata map[string]string

	// ContentType overrides the content type when MetadataDirective is "REPLACE".
	ContentType string
}

// TimeoutConfig controls HTTP client timeouts for cloud storage backends
// (S3, OSS, COS). Zero values disable the corresponding timeout.
type TimeoutConfig struct {
	ConnectTimeout time.Duration
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
}

// DefaultTimeoutConfig returns recommended defaults for production use.
func DefaultTimeoutConfig() TimeoutConfig {
	return TimeoutConfig{
		ConnectTimeout: 5 * time.Second,
		ReadTimeout:    30 * time.Second,
		WriteTimeout:   30 * time.Second,
	}
}

// NewHTTPClient creates an *http.Client with connect/read/write timeouts set at
// the transport level. Pass TimeoutConfig{} (zero) to get a client with no
// explicit timeout (http.DefaultClient behaviour).
func NewHTTPClient(tc TimeoutConfig) *http.Client {
	if tc.ConnectTimeout == 0 && tc.ReadTimeout == 0 && tc.WriteTimeout == 0 {
		return http.DefaultClient
	}
	overall := max(tc.ConnectTimeout, max(tc.ReadTimeout, tc.WriteTimeout))
	if overall == 0 {
		overall = 60 * time.Second
	}
	return &http.Client{
		Timeout: overall,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   nonZero(tc.ConnectTimeout, 5*time.Second),
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ResponseHeaderTimeout: nonZero(tc.ReadTimeout, 30*time.Second),
			TLSHandshakeTimeout:   nonZero(tc.ConnectTimeout, 5*time.Second),
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

func nonZero(d, fallback time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return fallback
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

	// UploadPartCopy copies a range of bytes from srcKey into partNumber of the
	// multipart upload identified by uploadID. srcOffset and length control the
	// byte range; if srcOffset < 0 the entire source is used.
	// Returns ErrUnsupported when the backend cannot perform server-side copy.
	UploadPartCopy(ctx context.Context, dstKey, uploadID string, partNumber int32, srcKey string, srcOffset, length int64) (MultipartPart, error)

	// AbortMultipart cancels a multipart upload and discards its parts.
	AbortMultipart(ctx context.Context, key, uploadID string) error

	// CleanupParts removes any storage-level artifacts (files, S3 parts) for an
	// expired or aborted multipart upload. It is used by the upload GC sweep to
	// ensure no orphaned parts remain after the DB row has been cleaned up.
	// Implementations should be idempotent: cleaning an already-clean upload is
	// not an error.
	CleanupParts(ctx context.Context, key, uploadID string) error

	// Backend identifies the underlying provider (e.g. "local", "s3").
	Backend() string

	// CanCopy returns true when the backend supports server-side Copy between
	// keys. If false, callers must fall back to Get+Put (client-stream copy).
	CanCopy() bool

	// Copy duplicates the object at srcKey to dstKey within the same backend.
	// Implementations SHOULD avoid reading the body into server memory when
	// the underlying storage supports server-side copy (e.g. S3 CopyObject).
	// Returns ErrUnsupported when the backend cannot perform the copy.
	Copy(ctx context.Context, srcKey, dstKey string, opts CopyOptions) (ObjectInfo, error)
}
