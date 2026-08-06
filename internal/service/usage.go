package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/telemetry"
)

type objectWriteUsage struct {
	previousSize int64
	newObject    bool
}

type countingReader struct {
	reader io.Reader
	total  int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.total += int64(n)
	return n, err
}

func (u objectWriteUsage) deltas(newSize int64) (int64, int64) {
	if u.newObject {
		return newSize, 1
	}
	return newSize - u.previousSize, 0
}

func (s *FileService) objectWriteUsage(ctx context.Context, tenant, bucket, key string, versioning bool) (objectWriteUsage, error) {
	if versioning {
		return objectWriteUsage{newObject: true}, nil
	}
	current, err := s.repo.GetObject(ctx, tenant, bucket, key)
	if errors.Is(err, repository.ErrNotFound) {
		return objectWriteUsage{newObject: true}, nil
	}
	if err != nil {
		return objectWriteUsage{}, err
	}
	return objectWriteUsage{previousSize: current.Size}, nil
}

func (s *FileService) accountObjectUsage(
	ctx context.Context, tenant string, usage objectWriteUsage, actualSize int64,
) error {
	deltaBytes, deltaObjects := usage.deltas(actualSize)
	if _, err := s.addTenantUsage(ctx, tenant, UsageObjectWrite, deltaBytes, deltaObjects); err != nil {
		return fmt.Errorf("record quota usage: %w", err)
	}
	return nil
}

func (s *FileService) validateStoredSize(
	ctx context.Context, storageKey string, expected, read, stored int64,
) error {
	if read == expected && stored == expected {
		return nil
	}
	telemetry.IncStorageSizeMismatch(ctx)
	s.logger.Error(
		"put size mismatch",
		"storage_key", storageKey,
		"expected", expected,
		"read", read,
		"stored", stored,
	)
	if err := s.store.Delete(ctx, storageKey); err != nil {
		s.logger.Warn(
			"orphaned blob cleanup failed after size mismatch",
			"storage_key", storageKey,
			"err", err,
		)
	}
	return fmt.Errorf(
		"%w: expected %d bytes, read %d and stored %d",
		ErrSizeMismatch, expected, read, stored,
	)
}

func (s *FileService) validateConsumedSize(
	ctx context.Context, operation string, expected, read int64,
) error {
	if read == expected {
		return nil
	}
	telemetry.IncStorageSizeMismatch(ctx)
	s.logger.Error(
		operation+" size mismatch",
		"expected", expected,
		"read", read,
	)
	return fmt.Errorf(
		"%w: expected %d bytes, read %d",
		ErrSizeMismatch, expected, read,
	)
}

func materializeUnknownSize(reader io.Reader, size int64) (io.Reader, int64, func(), error) {
	if size >= 0 {
		return reader, size, func() {}, nil
	}
	tmp, err := os.CreateTemp("", "aero-vault-upload-*")
	if err != nil {
		return nil, 0, nil, fmt.Errorf("create upload spool: %w", err)
	}
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}
	written, err := io.Copy(tmp, reader)
	if err != nil {
		cleanup()
		return nil, 0, nil, fmt.Errorf("spool upload: %w", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, 0, nil, fmt.Errorf("rewind upload spool: %w", err)
	}
	return tmp, written, cleanup, nil
}
