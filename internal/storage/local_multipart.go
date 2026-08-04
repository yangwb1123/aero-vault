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
	if err := s.validateServerSideEncryption(opts); err != nil {
		return MultipartInit{}, err
	}
	if _, err := s.objectPath(key); err != nil {
		return MultipartInit{}, err
	}
	uploadID := uuid.NewString()
	dir := filepath.Join(s.cfg.Root, ".multipart", uploadID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return MultipartInit{}, err
	}
	opts.SSECustomerKey = append([]byte(nil), opts.SSECustomerKey...)
	opts.SSECustomerKeyMD5 = append([]byte(nil), opts.SSECustomerKeyMD5...)
	s.mu.Lock()
	s.uploads[uploadID] = &localUpload{key: key, dir: dir, createdAt: time.Now(), opts: opts}
	s.mu.Unlock()
	return MultipartInit{Key: key, UploadID: uploadID}, nil
}

func (s *LocalStorage) UploadPart(ctx context.Context, key, uploadID string, partNumber int32, r io.Reader, size int64) (MultipartPart, error) {
	return s.UploadPartWithOptions(ctx, key, uploadID, partNumber, r, size, PutOptions{})
}

func (s *LocalStorage) UploadPartWithOptions(ctx context.Context, key, uploadID string, partNumber int32, r io.Reader, size int64, opts PutOptions) (MultipartPart, error) {
	s.mu.RLock()
	up, ok := s.uploads[uploadID]
	s.mu.RUnlock()
	if !ok || up.key != key {
		return MultipartPart{}, fmt.Errorf("unknown upload %s", uploadID)
	}
	if err := validateMultipartSSEC(up.opts, opts); err != nil {
		return MultipartPart{}, err
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
	return s.CompleteMultipartWithOptions(ctx, key, uploadID, parts, PutOptions{})
}

func (s *LocalStorage) CompleteMultipartWithOptions(ctx context.Context, key, uploadID string, parts []MultipartPart, opts PutOptions) (ObjectInfo, error) {
	s.mu.Lock()
	up, ok := s.uploads[uploadID]
	if ok && validateMultipartSSEC(up.opts, opts) == nil {
		delete(s.uploads, uploadID)
	}
	s.mu.Unlock()
	if !ok || up.key != key {
		return ObjectInfo{}, fmt.Errorf("unknown upload %s", uploadID)
	}
	if err := validateMultipartSSEC(up.opts, opts); err != nil {
		return ObjectInfo{}, err
	}
	defer clearPutSSEC(&up.opts)
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
		clearPutSSEC(&up.opts)
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

	meta, err := readMeta(s.metaPath(srcPath))
	if err != nil {
		return MultipartPart{}, fmt.Errorf("read source metadata: %w", err)
	}
	// The file on disk contains ciphertext whenever an envelope is present.
	// Copying it directly into a multipart part would encrypt it again during
	// completion. Let FileService fall back to its decrypting stream path.
	if meta.Envelope != "" {
		return MultipartPart{}, ErrUnsupported
	}

	var srcReader io.Reader
	if srcOffset >= 0 {
		fi, err := srcFile.Stat()
		if err != nil {
			return MultipartPart{}, err
		}
		if length <= 0 || srcOffset >= fi.Size() || length > fi.Size()-srcOffset {
			return MultipartPart{}, fmt.Errorf(
				"invalid source range: offset=%d length=%d size=%d",
				srcOffset, length, fi.Size(),
			)
		}
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
	copied, err := io.Copy(io.MultiWriter(dst, h), srcReader)
	if err != nil {
		_ = dst.Close()
		_ = os.Remove(partPath)
		return MultipartPart{}, err
	}
	if copied != length {
		_ = dst.Close()
		_ = os.Remove(partPath)
		return MultipartPart{}, fmt.Errorf(
			"copy source length mismatch: copied=%d expected=%d: %w",
			copied, length, io.ErrUnexpectedEOF,
		)
	}
	if err := dst.Close(); err != nil {
		_ = os.Remove(partPath)
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
	if upload, ok := s.uploads[uploadID]; ok {
		clearPutSSEC(&upload.opts)
		delete(s.uploads, uploadID)
	}
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

	enc := s.enc
	if len(up.opts.SSECustomerKey) == masterKeyLen {
		enc = newEnvelopeEncrypter(newSSECProvider(up.opts.SSECustomerKey))
	}
	if enc != nil {
		total, envelope, err = mergeEncrypted(enc, up.dir, parts, tmp)
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

func mergeEncrypted(enc *envelopeEncrypter, dir string, parts []MultipartPart, w io.Writer) (total int64, envelope string, err error) {
	var buf bytes.Buffer
	total, err = writePartsTo(dir, parts, &buf)
	if err != nil {
		return 0, "", err
	}
	ct, env, err := enc.encrypt(buf.Bytes())
	if err != nil {
		return 0, "", err
	}
	if _, err := w.Write(ct); err != nil {
		return 0, "", err
	}
	return total, env, nil
}

func validateMultipartSSEC(initial, request PutOptions) error {
	if len(initial.SSECustomerKey) == 0 {
		if len(request.SSECustomerKey) != 0 {
			return ErrInvalidSSECustomerKey
		}
		return nil
	}
	if len(request.SSECustomerKey) != masterKeyLen {
		return ErrSSECustomerKeyRequired
	}
	if !bytes.Equal(initial.SSECustomerKey, request.SSECustomerKey) {
		return ErrInvalidSSECustomerKey
	}
	return nil
}

func clearPutSSEC(opts *PutOptions) {
	for i := range opts.SSECustomerKey {
		opts.SSECustomerKey[i] = 0
	}
	for i := range opts.SSECustomerKeyMD5 {
		opts.SSECustomerKeyMD5[i] = 0
	}
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
