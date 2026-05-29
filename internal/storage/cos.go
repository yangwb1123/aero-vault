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
type COSConfig struct {
	BucketURL string
	SecretID  string
	SecretKey string
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
	client := cos.NewClient(&cos.BaseURL{BucketURL: u}, &http.Client{
		Transport: &cos.AuthorizationTransport{SecretID: cfg.SecretID, SecretKey: cfg.SecretKey},
	})
	return &COSStorage{cfg: cfg, client: client}, nil
}

func (s *COSStorage) Backend() string { return "cos" }

func (s *COSStorage) Put(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) (ObjectInfo, error) {
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
	_, err := s.client.Object.Delete(ctx, key)
	return err
}

func (s *COSStorage) List(ctx context.Context, prefix, marker string, limit int) (ListResult, error) {
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
	u, err := s.client.Object.GetPresignedURL(ctx, http.MethodGet, key, s.cfg.SecretID, s.cfg.SecretKey, expiry, nil)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func (s *COSStorage) PresignPut(ctx context.Context, key string, expiry time.Duration) (string, error) {
	u, err := s.client.Object.GetPresignedURL(ctx, http.MethodPut, key, s.cfg.SecretID, s.cfg.SecretKey, expiry, nil)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func (s *COSStorage) InitMultipart(ctx context.Context, key string, opts PutOptions) (MultipartInit, error) {
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
	_, err := s.client.Object.AbortMultipartUpload(ctx, key, uploadID)
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

func isCOSNotFound(err error) bool {
	var e *cos.ErrorResponse
	if errors.As(err, &e) && e.Response != nil {
		return e.Response.StatusCode == http.StatusNotFound
	}
	return false
}
