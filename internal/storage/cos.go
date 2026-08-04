package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tencentyun/cos-go-sdk-v5"
)

// COSConfig configures the Tencent Cloud COS backend. BucketURL is the full
// per-bucket endpoint, e.g. https://my-bucket-1250000000.cos.ap-guangzhou.myqcloud.com
// Timeouts control the underlying HTTP client; zero uses the SDK default.
type COSConfig struct {
	BucketURL string
	SecretID  string
	SecretKey string
	Timeouts  TimeoutConfig
}

// COSStorage implements Storage on top of Tencent Cloud COS using the native SDK.
type COSStorage struct {
	cfg    COSConfig
	client *cos.Client
}

// NewCOS creates a COS backend.
func NewCOS(cfg COSConfig) (*COSStorage, error) {
	if cfg.BucketURL == "" {
		return nil, errors.New("cos storage: bucket URL is required")
	}
	u, err := url.Parse(cfg.BucketURL)
	if err != nil {
		return nil, fmt.Errorf("cos bucket url: %w", err)
	}
	hc := NewHTTPClient(cfg.Timeouts)
	client := cos.NewClient(&cos.BaseURL{BucketURL: u}, &http.Client{
		Timeout: hc.Timeout,
		Transport: &cos.AuthorizationTransport{
			SecretID:  cfg.SecretID,
			SecretKey: cfg.SecretKey,
			Transport: hc.Transport,
		},
	})
	return &COSStorage{cfg: cfg, client: client}, nil
}

func (s *COSStorage) Backend() string { return "cos" }

func (s *COSStorage) Put(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) (ObjectInfo, error) {
	if err := validateObjectKey(key); err != nil {
		return ObjectInfo{}, err
	}
	opt := &cos.ObjectPutOptions{ObjectPutHeaderOptions: &cos.ObjectPutHeaderOptions{}}
	if opts.ContentType != "" {
		opt.ContentType = opts.ContentType
	}
	if size > 0 {
		opt.ContentLength = size
	}
	if len(opts.Metadata) > 0 {
		h := &http.Header{}
		for k, v := range opts.Metadata {
			h.Set("x-cos-meta-"+k, v)
		}
		opt.XOptionHeader = h
	}
	resp, err := s.client.Object.Put(ctx, key, r, opt)
	if err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{
		Key:          key,
		Size:         size,
		ETag:         strings.Trim(resp.Header.Get("ETag"), `"`),
		ContentType:  opts.ContentType,
		LastModified: time.Now().UTC(),
		Metadata:     opts.Metadata,
	}, nil
}

func (s *COSStorage) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	if err := validateObjectKey(key); err != nil {
		return nil, ObjectInfo{}, err
	}
	resp, err := s.client.Object.Get(ctx, key, nil)
	if err != nil {
		if isCOSNotFound(err) {
			return nil, ObjectInfo{}, ErrNotFound
		}
		return nil, ObjectInfo{}, err
	}
	return resp.Body, cosObjectInfo(key, resp.Header), nil
}

func (s *COSStorage) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	if err := validateObjectKey(key); err != nil {
		return ObjectInfo{}, err
	}
	resp, err := s.client.Object.Head(ctx, key, nil)
	if err != nil {
		if isCOSNotFound(err) {
			return ObjectInfo{}, ErrNotFound
		}
		return ObjectInfo{}, err
	}
	return cosObjectInfo(key, resp.Header), nil
}

func (s *COSStorage) Delete(ctx context.Context, key string) error {
	if err := validateObjectKey(key); err != nil {
		return err
	}
	_, err := s.client.Object.Delete(ctx, key)
	return err
}

func (s *COSStorage) List(ctx context.Context, prefix, marker string, limit int) (ListResult, error) {
	if err := validateListPrefix(prefix); err != nil {
		return ListResult{}, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	res, _, err := s.client.Bucket.Get(ctx, &cos.BucketGetOptions{
		Prefix:  prefix,
		Marker:  marker,
		MaxKeys: limit,
	})
	if err != nil {
		return ListResult{}, err
	}
	out := ListResult{HasMore: res.IsTruncated}
	if res.IsTruncated {
		out.NextMarker = res.NextMarker
	}
	for _, o := range res.Contents {
		info := ObjectInfo{Key: o.Key, Size: o.Size, ETag: strings.Trim(o.ETag, `"`)}
		if t, err := time.Parse(time.RFC3339, o.LastModified); err == nil {
			info.LastModified = t
		}
		out.Objects = append(out.Objects, info)
	}
	return out, nil
}

func (s *COSStorage) PresignGet(ctx context.Context, key string, expiry time.Duration) (string, error) {
	if err := validateObjectKey(key); err != nil {
		return "", err
	}
	u, err := s.client.Object.GetPresignedURL(ctx, http.MethodGet, key, s.cfg.SecretID, s.cfg.SecretKey, expiry, nil)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func (s *COSStorage) PresignPut(ctx context.Context, key string, expiry time.Duration) (string, error) {
	if err := validateObjectKey(key); err != nil {
		return "", err
	}
	u, err := s.client.Object.GetPresignedURL(ctx, http.MethodPut, key, s.cfg.SecretID, s.cfg.SecretKey, expiry, nil)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func (s *COSStorage) InitMultipart(ctx context.Context, key string, opts PutOptions) (MultipartInit, error) {
	if err := validateObjectKey(key); err != nil {
		return MultipartInit{}, err
	}
	opt := &cos.InitiateMultipartUploadOptions{ObjectPutHeaderOptions: &cos.ObjectPutHeaderOptions{}}
	if opts.ContentType != "" {
		opt.ContentType = opts.ContentType
	}
	res, _, err := s.client.Object.InitiateMultipartUpload(ctx, key, opt)
	if err != nil {
		return MultipartInit{}, err
	}
	return MultipartInit{Key: key, UploadID: res.UploadID}, nil
}

func (s *COSStorage) UploadPart(ctx context.Context, key, uploadID string, partNumber int32, r io.Reader, size int64) (MultipartPart, error) {
	if err := validateObjectKey(key); err != nil {
		return MultipartPart{}, err
	}
	opt := &cos.ObjectUploadPartOptions{}
	if size > 0 {
		opt.ContentLength = size
	}
	resp, err := s.client.Object.UploadPart(ctx, key, uploadID, int(partNumber), r, opt)
	if err != nil {
		return MultipartPart{}, err
	}
	return MultipartPart{PartNumber: partNumber, ETag: strings.Trim(resp.Header.Get("ETag"), `"`)}, nil
}

func (s *COSStorage) CompleteMultipart(ctx context.Context, key, uploadID string, parts []MultipartPart) (ObjectInfo, error) {
	if err := validateObjectKey(key); err != nil {
		return ObjectInfo{}, err
	}
	opt := &cos.CompleteMultipartUploadOptions{}
	for _, p := range parts {
		opt.Parts = append(opt.Parts, cos.Object{PartNumber: int(p.PartNumber), ETag: `"` + p.ETag + `"`})
	}
	res, _, err := s.client.Object.CompleteMultipartUpload(ctx, key, uploadID, opt)
	if err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{Key: key, ETag: strings.Trim(res.ETag, `"`), LastModified: time.Now().UTC()}, nil
}

func (s *COSStorage) AbortMultipart(ctx context.Context, key, uploadID string) error {
	if err := validateObjectKey(key); err != nil {
		return err
	}
	_, err := s.client.Object.AbortMultipartUpload(ctx, key, uploadID)
	return err
}

func (s *COSStorage) CleanupParts(ctx context.Context, key, uploadID string) error {
	if err := validateObjectKey(key); err != nil {
		return err
	}
	_, err := s.client.Object.AbortMultipartUpload(ctx, key, uploadID)
	if err != nil && isCOSNotFound(err) {
		return nil
	}
	return err
}

func cosObjectInfo(key string, h http.Header) ObjectInfo {
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
	const metaPrefix = "X-Cos-Meta-"
	for k := range h {
		if strings.HasPrefix(k, metaPrefix) {
			info.Metadata[strings.ToLower(strings.TrimPrefix(k, metaPrefix))] = h.Get(k)
		}
	}
	return info
}

func (s *COSStorage) CanCopy() bool { return true }

func (s *COSStorage) Copy(ctx context.Context, srcKey, dstKey string, opts CopyOptions) (ObjectInfo, error) {
	if err := validateObjectKeys(srcKey, dstKey); err != nil {
		return ObjectInfo{}, err
	}
	sourceURL := fmt.Sprintf("%s/%s", s.cfg.BucketURL, srcKey)
	hdr := &cos.ObjectCopyHeaderOptions{
		XCosCopySource: sourceURL,
	}
	if opts.MetadataDirective == "REPLACE" {
		hdr.XCosMetadataDirective = "Replaced"
		if opts.ContentType != "" {
			hdr.ContentType = opts.ContentType
		}
		if len(opts.Metadata) > 0 {
			meta := http.Header{}
			for k, v := range opts.Metadata {
				meta.Set("x-cos-meta-"+k, v)
			}
			hdr.XCosMetaXXX = &meta
		}
	} else {
		hdr.XCosMetadataDirective = "Copy"
	}
	src := &cos.ObjectCopyOptions{ObjectCopyHeaderOptions: hdr}
	result, _, err := s.client.Object.Copy(ctx, dstKey, sourceURL, src)
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("cos copy: %w", err)
	}
	etag := strings.Trim(result.ETag, `"`)
	return ObjectInfo{Key: dstKey, ETag: etag}, nil
}

func (s *COSStorage) UploadPartCopy(ctx context.Context, dstKey, uploadID string, partNumber int32, srcKey string, srcOffset, length int64) (MultipartPart, error) {
	if err := validateObjectKeys(dstKey, srcKey); err != nil {
		return MultipartPart{}, err
	}
	// COS SDK does not provide a direct UploadPartCopy method on the ObjectService.
	// Use Copy (which calls PutObjectCopy server-side) for full objects, or fall
	// back to the client-stream copy in the service layer.
	return MultipartPart{}, ErrUnsupported
}

func isCOSNotFound(err error) bool {
	var e *cos.ErrorResponse
	if errors.As(err, &e) && e.Response != nil {
		return e.Response.StatusCode == http.StatusNotFound
	}
	return false
}
