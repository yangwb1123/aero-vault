package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
)

// OSSConfig configures the Alibaba Cloud OSS backend.
// Timeouts control the underlying HTTP client; zero uses the SDK default.
type OSSConfig struct {
	Endpoint  string // e.g. https://oss-cn-hangzhou.aliyuncs.com
	Bucket    string
	AccessKey string
	SecretKey string
	Timeouts  TimeoutConfig
}

// OSSStorage implements Storage on top of Alibaba Cloud OSS using the native SDK
// (enables provider features beyond the S3-compatible endpoint).
type OSSStorage struct {
	cfg    OSSConfig
	client *oss.Client
	bucket *oss.Bucket
}

// NewOSS creates an OSS backend.
func NewOSS(cfg OSSConfig) (*OSSStorage, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" {
		return nil, errors.New("oss storage: endpoint and bucket are required")
	}
	client, err := oss.New(cfg.Endpoint, cfg.AccessKey, cfg.SecretKey, oss.HTTPClient(NewHTTPClient(cfg.Timeouts)))
	if err != nil {
		return nil, fmt.Errorf("oss client: %w", err)
	}
	bucket, err := client.Bucket(cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("oss bucket: %w", err)
	}
	return &OSSStorage{cfg: cfg, client: client, bucket: bucket}, nil
}

func (s *OSSStorage) Backend() string { return "oss" }

func (s *OSSStorage) Put(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) (ObjectInfo, error) {
	if err := validateObjectKey(key); err != nil {
		return ObjectInfo{}, err
	}
	var respHeader http.Header
	options := []oss.Option{oss.GetResponseHeader(&respHeader)}
	if opts.ContentType != "" {
		options = append(options, oss.ContentType(opts.ContentType))
	}
	if size > 0 {
		options = append(options, oss.ContentLength(size))
	}
	for k, v := range opts.Metadata {
		options = append(options, oss.Meta(k, v))
	}
	if err := s.bucket.PutObject(key, r, options...); err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{
		Key:          key,
		Size:         size,
		ETag:         strings.Trim(respHeader.Get("ETag"), `"`),
		ContentType:  opts.ContentType,
		LastModified: time.Now().UTC(),
		Metadata:     opts.Metadata,
	}, nil
}

func (s *OSSStorage) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	if err := validateObjectKey(key); err != nil {
		return nil, ObjectInfo{}, err
	}
	var respHeader http.Header
	body, err := s.bucket.GetObject(key, oss.GetResponseHeader(&respHeader))
	if err != nil {
		if isOSSNotFound(err) {
			return nil, ObjectInfo{}, ErrNotFound
		}
		return nil, ObjectInfo{}, err
	}
	return body, ossObjectInfo(key, respHeader), nil
}

func (s *OSSStorage) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	if err := validateObjectKey(key); err != nil {
		return ObjectInfo{}, err
	}
	header, err := s.bucket.GetObjectDetailedMeta(key)
	if err != nil {
		if isOSSNotFound(err) {
			return ObjectInfo{}, ErrNotFound
		}
		return ObjectInfo{}, err
	}
	return ossObjectInfo(key, header), nil
}

func (s *OSSStorage) Delete(ctx context.Context, key string) error {
	if err := validateObjectKey(key); err != nil {
		return err
	}
	return s.bucket.DeleteObject(key)
}

func (s *OSSStorage) List(ctx context.Context, prefix, marker string, limit int) (ListResult, error) {
	if err := validateListPrefix(prefix); err != nil {
		return ListResult{}, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	options := []oss.Option{oss.MaxKeys(limit)}
	if prefix != "" {
		options = append(options, oss.Prefix(prefix))
	}
	if marker != "" {
		options = append(options, oss.ContinuationToken(marker))
	}
	res, err := s.bucket.ListObjectsV2(options...)
	if err != nil {
		return ListResult{}, err
	}
	out := ListResult{HasMore: res.IsTruncated}
	if res.IsTruncated {
		out.NextMarker = res.NextContinuationToken
	}
	for _, o := range res.Objects {
		out.Objects = append(out.Objects, ObjectInfo{
			Key:          o.Key,
			Size:         o.Size,
			ETag:         strings.Trim(o.ETag, `"`),
			LastModified: o.LastModified,
		})
	}
	return out, nil
}

func (s *OSSStorage) PresignGet(ctx context.Context, key string, expiry time.Duration) (string, error) {
	if err := validateObjectKey(key); err != nil {
		return "", err
	}
	return s.bucket.SignURL(key, oss.HTTPGet, int64(expiry.Seconds()))
}

func (s *OSSStorage) PresignPut(ctx context.Context, key string, expiry time.Duration) (string, error) {
	if err := validateObjectKey(key); err != nil {
		return "", err
	}
	return s.bucket.SignURL(key, oss.HTTPPut, int64(expiry.Seconds()))
}

// imur reconstructs the OSS multipart handle from the stateless (key, uploadID)
// the Storage interface carries between calls.
func (s *OSSStorage) imur(key, uploadID string) oss.InitiateMultipartUploadResult {
	return oss.InitiateMultipartUploadResult{Bucket: s.cfg.Bucket, Key: key, UploadID: uploadID}
}

func (s *OSSStorage) InitMultipart(ctx context.Context, key string, opts PutOptions) (MultipartInit, error) {
	if err := validateObjectKey(key); err != nil {
		return MultipartInit{}, err
	}
	var options []oss.Option
	if opts.ContentType != "" {
		options = append(options, oss.ContentType(opts.ContentType))
	}
	for k, v := range opts.Metadata {
		options = append(options, oss.Meta(k, v))
	}
	res, err := s.bucket.InitiateMultipartUpload(key, options...)
	if err != nil {
		return MultipartInit{}, err
	}
	return MultipartInit{Key: key, UploadID: res.UploadID}, nil
}

func (s *OSSStorage) UploadPart(ctx context.Context, key, uploadID string, partNumber int32, r io.Reader, size int64) (MultipartPart, error) {
	if err := validateObjectKey(key); err != nil {
		return MultipartPart{}, err
	}
	part, err := s.bucket.UploadPart(s.imur(key, uploadID), r, size, int(partNumber))
	if err != nil {
		return MultipartPart{}, err
	}
	return MultipartPart{PartNumber: partNumber, ETag: strings.Trim(part.ETag, `"`)}, nil
}

func (s *OSSStorage) CompleteMultipart(ctx context.Context, key, uploadID string, parts []MultipartPart) (ObjectInfo, error) {
	if err := validateObjectKey(key); err != nil {
		return ObjectInfo{}, err
	}
	ossParts := make([]oss.UploadPart, 0, len(parts))
	for _, p := range parts {
		ossParts = append(ossParts, oss.UploadPart{PartNumber: int(p.PartNumber), ETag: p.ETag})
	}
	res, err := s.bucket.CompleteMultipartUpload(s.imur(key, uploadID), ossParts)
	if err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{Key: key, ETag: strings.Trim(res.ETag, `"`), LastModified: time.Now().UTC()}, nil
}

func (s *OSSStorage) AbortMultipart(ctx context.Context, key, uploadID string) error {
	if err := validateObjectKey(key); err != nil {
		return err
	}
	return s.bucket.AbortMultipartUpload(s.imur(key, uploadID))
}

func (s *OSSStorage) CleanupParts(ctx context.Context, key, uploadID string) error {
	if err := validateObjectKey(key); err != nil {
		return err
	}
	// OSS storage-level cleanup uses the same AbortMultipart API.
	// If the upload is already gone, the OSS SDK returns a 404 which we treat
	// as success (idempotent).
	err := s.bucket.AbortMultipartUpload(s.imur(key, uploadID))
	if err != nil && isOSSNotFound(err) {
		return nil
	}
	return err
}

func ossObjectInfo(key string, h http.Header) ObjectInfo {
	info := ObjectInfo{
		Key:         key,
		ETag:        strings.Trim(h.Get("ETag"), `"`),
		ContentType: h.Get("Content-Type"),
		Metadata:    map[string]string{},
	}
	if cl := h.Get("Content-Length"); cl != "" {
		var n int64
		_, _ = fmt.Sscan(cl, &n)
		info.Size = n
	}
	if lm := h.Get("Last-Modified"); lm != "" {
		if t, err := http.ParseTime(lm); err == nil {
			info.LastModified = t
		}
	}
	const metaPrefix = "X-Oss-Meta-"
	for k := range h {
		if strings.HasPrefix(k, metaPrefix) {
			info.Metadata[strings.ToLower(strings.TrimPrefix(k, metaPrefix))] = h.Get(k)
		}
	}
	return info
}

func (s *OSSStorage) CanCopy() bool { return true }

func (s *OSSStorage) Copy(ctx context.Context, srcKey, dstKey string, opts CopyOptions) (ObjectInfo, error) {
	if err := validateObjectKeys(srcKey, dstKey); err != nil {
		return ObjectInfo{}, err
	}
	// Use OSS CopyObject API (server-side copy, does not read body into memory).
	hdrs := []oss.Option{}
	if opts.MetadataDirective == "REPLACE" {
		hdrs = append(hdrs, oss.MetadataDirective(oss.MetaReplace))
		if opts.ContentType != "" {
			hdrs = append(hdrs, oss.ContentType(opts.ContentType))
		}
		if len(opts.Metadata) > 0 {
			for k, v := range opts.Metadata {
				hdrs = append(hdrs, oss.Meta(k, v))
			}
		}
	} else {
		hdrs = append(hdrs, oss.MetadataDirective(oss.MetaCopy))
	}
	// oss.CopyObjectResult contains ETag and LastModified
	result, err := s.bucket.CopyObject(srcKey, dstKey, hdrs...)
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("oss copy: %w", err)
	}
	etag := strings.Trim(result.ETag, `"`)
	return ObjectInfo{Key: dstKey, ETag: etag}, nil
}

func (s *OSSStorage) UploadPartCopy(ctx context.Context, dstKey, uploadID string, partNumber int32, srcKey string, srcOffset, length int64) (MultipartPart, error) {
	if err := validateObjectKeys(dstKey, srcKey); err != nil {
		return MultipartPart{}, err
	}
	// OSS SDK requires InitiateMultipartUploadResult which is not available at this
	// abstraction level. Use Copy (which calls CopyObject server-side) for full
	// objects, or fall back to the client-stream copy in the service layer.
	return MultipartPart{}, ErrUnsupported
}

func isOSSNotFound(err error) bool {
	var se oss.ServiceError
	if errors.As(err, &se) {
		return se.StatusCode == http.StatusNotFound || se.Code == "NoSuchKey"
	}
	return false
}
