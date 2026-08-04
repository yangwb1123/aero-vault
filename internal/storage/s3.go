package storage

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsv4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3Config configures the S3 backend. Endpoint is optional; leave it empty for
// AWS, or point it at MinIO / OSS S3 / COS S3 endpoints for those providers.
// Timeouts control the underlying HTTP client; zero uses the SDK default.
type S3Config struct {
	Endpoint       string
	Region         string
	Bucket         string
	AccessKey      string
	SecretKey      string
	ForcePathStyle bool
	Timeouts       TimeoutConfig
}

// S3Storage implements Storage on top of an S3-compatible object store.
type S3Storage struct {
	cfg       S3Config
	client    *s3.Client
	presigner *s3.PresignClient
}

// NewS3 creates an S3 backend using static credentials when supplied, otherwise
// the default AWS credential chain.
func NewS3(ctx context.Context, cfg S3Config) (*S3Storage, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("s3 storage: bucket is required")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}

	loadOpts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
	}
	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		loadOpts = append(loadOpts,
			awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")),
		)
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	awsCfg.HTTPClient = NewHTTPClient(cfg.Timeouts)

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.ForcePathStyle
	})

	return &S3Storage{
		cfg:       cfg,
		client:    client,
		presigner: s3.NewPresignClient(client),
	}, nil
}

func (s *S3Storage) Backend() string { return "s3" }

func (s *S3Storage) SupportsServerSideEncryption(algorithm, keyID string) bool {
	switch algorithm {
	case "AES256":
		return keyID == ""
	case "aws:kms":
		return true
	default:
		return false
	}
}

func (s *S3Storage) Put(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) (ObjectInfo, error) {
	if err := validateObjectKey(key); err != nil {
		return ObjectInfo{}, err
	}
	if opts.SSEAlgorithm != "" &&
		(!s.SupportsServerSideEncryption(opts.SSEAlgorithm, opts.SSEKMSKeyID) ||
			len(opts.SSECustomerKey) != 0) {
		return ObjectInfo{}, ErrUnsupported
	}
	body, cleanup, err := replayableS3Body(ctx, r)
	if err != nil {
		return ObjectInfo{}, err
	}
	defer cleanup()
	input := &s3.PutObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(key),
		Body:   body,
	}
	if opts.ContentType != "" {
		input.ContentType = aws.String(opts.ContentType)
	}
	if len(opts.Metadata) > 0 {
		input.Metadata = opts.Metadata
	}
	if size > 0 {
		input.ContentLength = aws.Int64(size)
	}
	if len(opts.SSECustomerKey) > 0 {
		input.SSECustomerAlgorithm = aws.String("AES256")
		input.SSECustomerKey = aws.String(base64.StdEncoding.EncodeToString(opts.SSECustomerKey))
		input.SSECustomerKeyMD5 = aws.String(base64.StdEncoding.EncodeToString(opts.SSECustomerKeyMD5))
	}
	if opts.SSEAlgorithm != "" {
		input.ServerSideEncryption = s3types.ServerSideEncryption(opts.SSEAlgorithm)
		if opts.SSEKMSKeyID != "" {
			input.SSEKMSKeyId = aws.String(opts.SSEKMSKeyID)
		}
	}
	out, err := s.client.PutObject(ctx, input, s3BodyOptions(r)...)
	if err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{
		Key:          key,
		Size:         size,
		ETag:         strings.Trim(aws.ToString(out.ETag), `"`),
		ContentType:  opts.ContentType,
		LastModified: time.Now().UTC(),
		Metadata:     opts.Metadata,
	}, nil
}

func (s *S3Storage) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	return s.GetWithOptions(ctx, key, GetOptions{})
}

func (s *S3Storage) GetWithOptions(ctx context.Context, key string, opts GetOptions) (io.ReadCloser, ObjectInfo, error) {
	if err := validateObjectKey(key); err != nil {
		return nil, ObjectInfo{}, err
	}
	input := &s3.GetObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(key),
	}
	applySSECGetInput(input, opts)
	out, err := s.client.GetObject(ctx, input)
	if err != nil {
		if isS3NotFound(err) {
			return nil, ObjectInfo{}, ErrNotFound
		}
		return nil, ObjectInfo{}, err
	}
	info := ObjectInfo{
		Key:          key,
		Size:         aws.ToInt64(out.ContentLength),
		ETag:         strings.Trim(aws.ToString(out.ETag), `"`),
		ContentType:  aws.ToString(out.ContentType),
		LastModified: aws.ToTime(out.LastModified),
		Metadata:     out.Metadata,
	}
	return out.Body, info, nil
}

func (s *S3Storage) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	return s.StatWithOptions(ctx, key, GetOptions{})
}

func (s *S3Storage) StatWithOptions(ctx context.Context, key string, opts GetOptions) (ObjectInfo, error) {
	if err := validateObjectKey(key); err != nil {
		return ObjectInfo{}, err
	}
	input := &s3.HeadObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(key),
	}
	if len(opts.SSECustomerKey) > 0 {
		input.SSECustomerAlgorithm = aws.String("AES256")
		input.SSECustomerKey = aws.String(base64.StdEncoding.EncodeToString(opts.SSECustomerKey))
		input.SSECustomerKeyMD5 = aws.String(base64.StdEncoding.EncodeToString(opts.SSECustomerKeyMD5))
	}
	out, err := s.client.HeadObject(ctx, input)
	if err != nil {
		if isS3NotFound(err) {
			return ObjectInfo{}, ErrNotFound
		}
		return ObjectInfo{}, err
	}
	return ObjectInfo{
		Key:          key,
		Size:         aws.ToInt64(out.ContentLength),
		ETag:         strings.Trim(aws.ToString(out.ETag), `"`),
		ContentType:  aws.ToString(out.ContentType),
		LastModified: aws.ToTime(out.LastModified),
		Metadata:     out.Metadata,
	}, nil
}

func (s *S3Storage) Delete(ctx context.Context, key string) error {
	if err := validateObjectKey(key); err != nil {
		return err
	}
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(key),
	})
	return err
}

func (s *S3Storage) List(ctx context.Context, prefix, marker string, limit int) (ListResult, error) {
	if err := validateListPrefix(prefix); err != nil {
		return ListResult{}, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	input := &s3.ListObjectsV2Input{
		Bucket:  aws.String(s.cfg.Bucket),
		MaxKeys: aws.Int32(int32(limit)),
	}
	if prefix != "" {
		input.Prefix = aws.String(prefix)
	}
	if marker != "" {
		input.ContinuationToken = aws.String(marker)
	}
	out, err := s.client.ListObjectsV2(ctx, input)
	if err != nil {
		return ListResult{}, err
	}
	res := ListResult{
		HasMore: aws.ToBool(out.IsTruncated),
	}
	if res.HasMore {
		res.NextMarker = aws.ToString(out.NextContinuationToken)
	}
	for _, o := range out.Contents {
		res.Objects = append(res.Objects, ObjectInfo{
			Key:          aws.ToString(o.Key),
			Size:         aws.ToInt64(o.Size),
			ETag:         strings.Trim(aws.ToString(o.ETag), `"`),
			LastModified: aws.ToTime(o.LastModified),
		})
	}
	return res, nil
}

func (s *S3Storage) PresignGet(ctx context.Context, key string, expiry time.Duration) (string, error) {
	if err := validateObjectKey(key); err != nil {
		return "", err
	}
	req, err := s.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", err
	}
	return req.URL, nil
}

func (s *S3Storage) PresignPut(ctx context.Context, key string, expiry time.Duration) (string, error) {
	if err := validateObjectKey(key); err != nil {
		return "", err
	}
	req, err := s.presigner.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", err
	}
	return req.URL, nil
}

func (s *S3Storage) InitMultipart(ctx context.Context, key string, opts PutOptions) (MultipartInit, error) {
	if err := validateObjectKey(key); err != nil {
		return MultipartInit{}, err
	}
	if opts.SSEAlgorithm != "" &&
		(!s.SupportsServerSideEncryption(opts.SSEAlgorithm, opts.SSEKMSKeyID) ||
			len(opts.SSECustomerKey) != 0) {
		return MultipartInit{}, ErrUnsupported
	}
	input := &s3.CreateMultipartUploadInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(key),
	}
	if opts.ContentType != "" {
		input.ContentType = aws.String(opts.ContentType)
	}
	if len(opts.Metadata) > 0 {
		input.Metadata = opts.Metadata
	}
	if len(opts.SSECustomerKey) > 0 {
		input.SSECustomerAlgorithm = aws.String("AES256")
		input.SSECustomerKey = aws.String(base64.StdEncoding.EncodeToString(opts.SSECustomerKey))
		input.SSECustomerKeyMD5 = aws.String(base64.StdEncoding.EncodeToString(opts.SSECustomerKeyMD5))
	}
	if opts.SSEAlgorithm != "" {
		input.ServerSideEncryption = s3types.ServerSideEncryption(opts.SSEAlgorithm)
		if opts.SSEKMSKeyID != "" {
			input.SSEKMSKeyId = aws.String(opts.SSEKMSKeyID)
		}
	}
	out, err := s.client.CreateMultipartUpload(ctx, input)
	if err != nil {
		return MultipartInit{}, err
	}
	return MultipartInit{Key: key, UploadID: aws.ToString(out.UploadId)}, nil
}

func (s *S3Storage) UploadPart(ctx context.Context, key, uploadID string, partNumber int32, r io.Reader, size int64) (MultipartPart, error) {
	return s.UploadPartWithOptions(ctx, key, uploadID, partNumber, r, size, PutOptions{})
}

func (s *S3Storage) UploadPartWithOptions(ctx context.Context, key, uploadID string, partNumber int32, r io.Reader, size int64, opts PutOptions) (MultipartPart, error) {
	if err := validateObjectKey(key); err != nil {
		return MultipartPart{}, err
	}
	body, cleanup, err := replayableS3Body(ctx, r)
	if err != nil {
		return MultipartPart{}, err
	}
	defer cleanup()
	input := &s3.UploadPartInput{
		Bucket:     aws.String(s.cfg.Bucket),
		Key:        aws.String(key),
		UploadId:   aws.String(uploadID),
		PartNumber: aws.Int32(partNumber),
		Body:       body,
	}
	if size > 0 {
		input.ContentLength = aws.Int64(size)
	}
	if len(opts.SSECustomerKey) > 0 {
		input.SSECustomerAlgorithm = aws.String("AES256")
		input.SSECustomerKey = aws.String(base64.StdEncoding.EncodeToString(opts.SSECustomerKey))
		input.SSECustomerKeyMD5 = aws.String(base64.StdEncoding.EncodeToString(opts.SSECustomerKeyMD5))
	}
	out, err := s.client.UploadPart(ctx, input, s3BodyOptions(r)...)
	if err != nil {
		return MultipartPart{}, err
	}
	return MultipartPart{PartNumber: partNumber, ETag: strings.Trim(aws.ToString(out.ETag), `"`)}, nil
}

func s3BodyOptions(body io.Reader) []func(*s3.Options) {
	if _, ok := body.(io.Seeker); ok {
		return nil
	}
	return []func(*s3.Options){
		func(opts *s3.Options) {
			opts.APIOptions = append(
				opts.APIOptions,
				awsv4.SwapComputePayloadSHA256ForUnsignedPayloadMiddleware,
			)
		},
	}
}

func (s *S3Storage) CompleteMultipart(ctx context.Context, key, uploadID string, parts []MultipartPart) (ObjectInfo, error) {
	return s.CompleteMultipartWithOptions(ctx, key, uploadID, parts, PutOptions{})
}

func (s *S3Storage) CompleteMultipartWithOptions(ctx context.Context, key, uploadID string, parts []MultipartPart, opts PutOptions) (ObjectInfo, error) {
	if err := validateObjectKey(key); err != nil {
		return ObjectInfo{}, err
	}
	completed := make([]s3types.CompletedPart, 0, len(parts))
	for _, p := range parts {
		completed = append(completed, s3types.CompletedPart{
			ETag:       aws.String(`"` + p.ETag + `"`),
			PartNumber: aws.Int32(p.PartNumber),
		})
	}
	input := &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(s.cfg.Bucket),
		Key:             aws.String(key),
		UploadId:        aws.String(uploadID),
		MultipartUpload: &s3types.CompletedMultipartUpload{Parts: completed},
	}
	if len(opts.SSECustomerKey) > 0 {
		input.SSECustomerAlgorithm = aws.String("AES256")
		input.SSECustomerKey = aws.String(base64.StdEncoding.EncodeToString(opts.SSECustomerKey))
		input.SSECustomerKeyMD5 = aws.String(base64.StdEncoding.EncodeToString(opts.SSECustomerKeyMD5))
	}
	out, err := s.client.CompleteMultipartUpload(ctx, input)
	if err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{
		Key:          key,
		ETag:         strings.Trim(aws.ToString(out.ETag), `"`),
		LastModified: time.Now().UTC(),
	}, nil
}

func (s *S3Storage) SupportsSSEC() bool { return true }

func applySSECGetInput(input *s3.GetObjectInput, opts GetOptions) {
	if len(opts.SSECustomerKey) == 0 {
		return
	}
	input.SSECustomerAlgorithm = aws.String("AES256")
	input.SSECustomerKey = aws.String(base64.StdEncoding.EncodeToString(opts.SSECustomerKey))
	input.SSECustomerKeyMD5 = aws.String(base64.StdEncoding.EncodeToString(opts.SSECustomerKeyMD5))
}

func (s *S3Storage) AbortMultipart(ctx context.Context, key, uploadID string) error {
	if err := validateObjectKey(key); err != nil {
		return err
	}
	_, err := s.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(s.cfg.Bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
	})
	return err
}

func (s *S3Storage) CleanupParts(ctx context.Context, key, uploadID string) error {
	if err := validateObjectKey(key); err != nil {
		return err
	}
	// S3 storage-level cleanup is the same as aborting the multipart upload.
	_, err := s.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(s.cfg.Bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
	})
	// If the upload no longer exists (e.g. already completed), this is idempotent.
	if err != nil {
		// AWS returns 404 when the upload ID is not found; treat as success.
		if isS3NotFound(err) {
			return nil
		}
	}
	return err
}

func isS3NotFound(err error) bool {
	if err == nil {
		return false
	}
	var nsk *s3types.NoSuchKey
	if errors.As(err, &nsk) {
		return true
	}
	var nf *s3types.NotFound
	return errors.As(err, &nf)
}
