package service

import (
	"context"
	"fmt"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func (s *FileService) checkBucketDeleteProtection(ctx context.Context, tenant, bucket string) error {
	objects, err := s.bucketObjects(ctx, tenant, bucket)
	if err != nil {
		return err
	}
	for _, obj := range objects {
		if err := s.checkObjectProtection(ctx, obj); err != nil {
			return err
		}
	}
	return nil
}

func (s *FileService) bucketObjects(ctx context.Context, tenant, bucket string) ([]repository.Object, error) {
	var objects []repository.Object
	marker := ""
	for {
		keys, next, hasMore, err := s.repo.ListObjectVersionKeys(ctx, tenant, bucket, "", marker, 1000)
		if err != nil {
			return nil, err
		}
		for _, key := range keys {
			versions, err := s.repo.ListObjectVersions(ctx, tenant, bucket, key)
			if err != nil {
				return nil, err
			}
			objects = append(objects, versions...)
		}
		if !hasMore {
			return objects, nil
		}
		marker = next
	}
}

func countedObjectUsage(objects []repository.Object) (bytes, count int64) {
	for _, obj := range objects {
		if obj.DeletedAt == nil || obj.VersionTombstone {
			bytes += obj.Size
			count++
		}
	}
	return bytes, count
}

func (s *FileService) deleteBucketData(ctx context.Context, tenant, bucket string, objects []repository.Object) error {
	uploads, err := s.bucketUploads(ctx, tenant, bucket)
	if err != nil {
		return err
	}
	for _, upload := range uploads {
		if err := s.store.AbortMultipart(ctx, uploadStorageKey(upload), upload.BackendUID); err != nil {
			return fmt.Errorf("abort multipart %q: %w", upload.ID, err)
		}
	}
	deletedKeys := make(map[string]struct{}, len(objects))
	for _, obj := range objects {
		if IsDeleteMarker(obj) {
			continue
		}
		if s.chunkCleaner != nil {
			if err := s.chunkCleaner.DeleteObjectChunks(ctx, obj.ID); err != nil {
				s.logger.Warn("chunk cleanup on bucket delete failed", "key", obj.Key, "err", err)
			}
		}
		if _, deleted := deletedKeys[obj.StorageKey]; deleted {
			continue
		}
		if err := s.store.Delete(ctx, obj.StorageKey); err != nil {
			return fmt.Errorf("storage delete %q: %w", obj.StorageKey, err)
		}
		deletedKeys[obj.StorageKey] = struct{}{}
	}
	return nil
}

func (s *FileService) bucketUploads(ctx context.Context, tenant, bucket string) ([]repository.Upload, error) {
	const pageSize = 10000
	var all []repository.Upload
	keyMarker, uploadMarker := "", ""
	for {
		page, err := s.repo.ListUploads(ctx, tenant, bucket, keyMarker, uploadMarker, pageSize)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < pageSize {
			return all, nil
		}
		last := page[len(page)-1]
		keyMarker, uploadMarker = last.Key, last.ID
	}
}
