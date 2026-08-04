package storage

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func (s *S3Storage) UploadPartCopy(
	ctx context.Context,
	dstKey, uploadID string,
	partNumber int32,
	srcKey string,
	srcOffset, length int64,
) (MultipartPart, error) {
	if err := validateObjectKeys(dstKey, srcKey); err != nil {
		return MultipartPart{}, err
	}
	input := &s3.UploadPartCopyInput{
		Bucket:     aws.String(s.cfg.Bucket),
		Key:        aws.String(dstKey),
		UploadId:   aws.String(uploadID),
		PartNumber: aws.Int32(partNumber),
		CopySource: aws.String(s.cfg.Bucket + "/" + srcKey),
	}
	if srcOffset >= 0 {
		input.CopySourceRange = aws.String(
			fmt.Sprintf("bytes=%d-%d", srcOffset, srcOffset+length-1),
		)
	}
	out, err := s.client.UploadPartCopy(ctx, input)
	if err != nil {
		return MultipartPart{}, fmt.Errorf("s3 upload-part-copy: %w", err)
	}
	etag := ""
	if out.CopyPartResult != nil && out.CopyPartResult.ETag != nil {
		etag = strings.Trim(aws.ToString(out.CopyPartResult.ETag), `"`)
	}
	return MultipartPart{PartNumber: partNumber, ETag: etag}, nil
}

func (s *S3Storage) CanCopy() bool { return true }

func (s *S3Storage) Copy(
	ctx context.Context, srcKey, dstKey string, opts CopyOptions,
) (ObjectInfo, error) {
	if err := validateObjectKeys(srcKey, dstKey); err != nil {
		return ObjectInfo{}, err
	}
	input := &s3.CopyObjectInput{
		Bucket:     aws.String(s.cfg.Bucket),
		CopySource: aws.String(s.cfg.Bucket + "/" + srcKey),
		Key:        aws.String(dstKey),
	}
	if opts.MetadataDirective == "REPLACE" {
		input.MetadataDirective = s3types.MetadataDirectiveReplace
		if opts.ContentType != "" {
			input.ContentType = aws.String(opts.ContentType)
		}
		if len(opts.Metadata) > 0 {
			input.Metadata = opts.Metadata
		}
	} else {
		input.MetadataDirective = s3types.MetadataDirectiveCopy
	}
	out, err := s.client.CopyObject(ctx, input)
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("s3 copy: %w", err)
	}
	info := ObjectInfo{Key: dstKey, Size: -1}
	if out.CopyObjectResult != nil && out.CopyObjectResult.ETag != nil {
		info.ETag = *out.CopyObjectResult.ETag
	}
	if stat, err := s.Stat(ctx, dstKey); err == nil {
		info.Size = stat.Size
		info.LastModified = stat.LastModified
	}
	return info, nil
}
