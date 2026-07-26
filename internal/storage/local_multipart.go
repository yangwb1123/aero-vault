package storage

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/google/uuid"
)

func (s *LocalStorage) InitMultipart(ctx context.Context, key string, opts PutOptions) (MultipartInit, error) {
	if _, err := s.objectPath(key); err != nil {
		return MultipartInit{}, err
	}
	uploadID := uuid.NewString()
	dir := filepath.Join(s.cfg.Root, ".multipart", uploadID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return MultipartInit{}, err
	}
	s.mu.Lock()
	s.uploads[uploadID] = &localUpload{key: key, dir: dir, createdAt: time.Now(), opts: opts}
	s.mu.Unlock()
	return MultipartInit{Key: key, UploadID: uploadID}, nil
}

func (s *LocalStorage) UploadPart(ctx context.Context, key, uploadID string, partNumber int32, r io.Reader, size int64) (MultipartPart, error) {
	s.mu.RLock()
	up, ok := s.uploads[uploadID]
	s.mu.RUnlock()
	if !ok || up.key != key {
		return MultipartPart{}, fmt.Errorf("unknown upload %s", uploadID)
	}
	path := filepath.Join(up.dir, fmt.Sprintf("part-%05d", partNumber))
	f, err := os.Create(path)
	if err != nil {
		return MultipartPart{}, err
	}
	h := md5.New()
	if _, err := io.Copy(io.MultiWriter(f, h), r); err != nil {
		_ = f.Close()
		return MultipartPart{}, err
	}
	if err := f.Close(); err != nil {
		return MultipartPart{}, err
	}
	return MultipartPart{PartNumber: partNumber, ETag: hex.EncodeToString(h.Sum(nil))}, nil
}

// multipartETag computes the AWS-style multipart ETag: the hex MD5 of the
// concatenated binary part MD5s, suffixed with "-<partCount>" — what real S3
// returns for a completed multipart object (vs a whole-object MD5). Parts must be
// in ascending part-number order.
func multipartETag(parts []MultipartPart) (string, error) {
	sum := md5.New()
	for _, p := range parts {
		raw, err := hex.DecodeString(p.ETag)
		if err != nil {
			return "", fmt.Errorf("multipart: part %d has a non-hex etag %q: %w", p.PartNumber, p.ETag, err)
		}
		sum.Write(raw)
	}
	return fmt.Sprintf("%s-%d", hex.EncodeToString(sum.Sum(nil)), len(parts)), nil
}

func (s *LocalStorage) CompleteMultipart(ctx context.Context, key, uploadID string, parts []MultipartPart) (ObjectInfo, error) {
	s.mu.Lock()
	up, ok := s.uploads[uploadID]
	if ok {
		delete(s.uploads, uploadID)
	}
	s.mu.Unlock()
	if !ok || up.key != key {
		return ObjectInfo{}, fmt.Errorf("unknown upload %s", uploadID)
	}
	defer os.RemoveAll(up.dir)

	sort.Slice(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber })

	dst, err := s.objectPath(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	total, envelope, err := s.mergeParts(ctx, up, parts, dst)
	if err != nil {
		return ObjectInfo{}, err
	}
	etag, err := multipartETag(parts)
	if err != nil {
		return ObjectInfo{}, err
	}
	meta := localMeta{
		Key:          key,
		Size:         total,
		ETag:         etag,
		ContentType:  up.opts.ContentType,
		LastModified: time.Now().UTC(),
		Metadata:     up.opts.Metadata,
		Envelope:     envelope,
	}
	if err := writeMeta(s.metaPath(dst), meta); err != nil {
		_ = os.Remove(dst)
		return ObjectInfo{}, err
	}
	return meta.toInfo(), nil
}

func (s *LocalStorage) AbortMultipart(ctx context.Context, key, uploadID string) error {
	s.mu.Lock()
	up, ok := s.uploads[uploadID]
	if ok {
		delete(s.uploads, uploadID)
	}
	s.mu.Unlock()
	if ok {
		_ = os.RemoveAll(up.dir)
	}
	return nil
}

func (s *LocalStorage) UploadPartCopy(ctx context.Context, dstKey, uploadID string, partNumber int32, srcKey string, srcOffset, length int64) (MultipartPart, error) {
	s.mu.RLock()
	up, ok := s.uploads[uploadID]
	s.mu.RUnlock()
	if !ok || up.key != dstKey {
		return MultipartPart{}, fmt.Errorf("unknown upload %s", uploadID)
	}

	srcPath, err := s.objectPath(srcKey)
	if err != nil {
		return MultipartPart{}, err
	}
	srcFile, err := os.Open(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return MultipartPart{}, fmt.Errorf("%w: source %s not found", os.ErrNotExist, srcKey)
		}
		return MultipartPart{}, err
	}
	defer srcFile.Close()

	var srcReader io.Reader
	if srcOffset >= 0 {
		if _, err := srcFile.Seek(srcOffset, io.SeekStart); err != nil {
			return MultipartPart{}, err
		}
		srcReader = io.LimitReader(srcFile, length)
	} else {
		// Whole file copy — need to figure out the actual length for the copy.
		fi, err := srcFile.Stat()
		if err != nil {
			return MultipartPart{}, err
		}
		length = fi.Size()
		srcReader = srcFile
	}

	partPath := filepath.Join(up.dir, fmt.Sprintf("part-%05d", partNumber))
	dst, err := os.Create(partPath)
	if err != nil {
		return MultipartPart{}, err
	}
	h := md5.New()
	if _, err := io.Copy(io.MultiWriter(dst, h), srcReader); err != nil {
		_ = dst.Close()
		return MultipartPart{}, err
	}
	if err := dst.Close(); err != nil {
		return MultipartPart{}, err
	}
	return MultipartPart{PartNumber: partNumber, ETag: hex.EncodeToString(h.Sum(nil))}, nil
}

func (s *LocalStorage) CleanupParts(ctx context.Context, key, uploadID string) error {
	// Remove the .multipart/<uploadID>/ directory if it exists.
	dir := filepath.Join(s.cfg.Root, ".multipart", uploadID)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil // already clean
	}
	s.mu.Lock()
	delete(s.uploads, uploadID)
	s.mu.Unlock()
	return os.RemoveAll(dir)
}

func (s *LocalStorage) mergeParts(ctx context.Context, up *localUpload, parts []MultipartPart, dst string) (total int64, envelope string, err error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".assemble-*")
	if err != nil {
		return 0, "", err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	if s.enc != nil {
		total, envelope, err = s.mergeEncrypted(up.dir, parts, tmp)
	} else {
		total, err = writePartsTo(up.dir, parts, tmp)
	}
	if err != nil {
		return 0, "", err
	}
	if err := tmp.Close(); err != nil {
		return 0, "", err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return 0, "", err
	}
	return total, envelope, nil
}

func (s *LocalStorage) mergeEncrypted(dir string, parts []MultipartPart, w io.Writer) (total int64, envelope string, err error) {
	var buf bytes.Buffer
	total, err = writePartsTo(dir, parts, &buf)
	if err != nil {
		return 0, "", err
	}
	ct, env, err := s.enc.encrypt(buf.Bytes())
	if err != nil {
		return 0, "", err
	}
	if _, err := w.Write(ct); err != nil {
		return 0, "", err
	}
	return total, env, nil
}

func writePartsTo(dir string, parts []MultipartPart, w io.Writer) (int64, error) {
	var total int64
	for _, p := range parts {
		partPath := filepath.Join(dir, fmt.Sprintf("part-%05d", p.PartNumber))
		f, err := os.Open(partPath)
		if err != nil {
			return 0, err
		}
		n, err := io.Copy(w, f)
		_ = f.Close()
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}
