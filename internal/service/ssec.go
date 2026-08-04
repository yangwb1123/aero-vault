package service

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"fmt"

	"github.com/aero-vault/aero-vault/internal/storage"
)

const (
	ssecAlgorithmMeta = "_aero_sse_c_algorithm"
	ssecKeyMD5Meta    = "_aero_sse_c_key_md5"
)

func (s *FileService) prepareSSECWrite(opts *PutOptions) error {
	if len(opts.SSECustomerKey) == 0 {
		if len(opts.SSECustomerKeyMD5) != 0 {
			return fmt.Errorf("%w: SSE-C key is required with key MD5", ErrInvalidArgs)
		}
		if opts.Metadata[ssecAlgorithmMeta] != "" || opts.Metadata[ssecKeyMD5Meta] != "" {
			opts.Metadata = metadataWithoutEncryption(opts.Metadata)
		}
		return nil
	}
	if !storage.SupportsSSEC(s.store) {
		return fmt.Errorf("%w: storage backend does not support SSE-C", ErrInvalidArgs)
	}
	if len(opts.SSECustomerKey) != 32 {
		return fmt.Errorf("%w: SSE-C key must be 32 bytes", ErrInvalidArgs)
	}
	sum := md5.Sum(opts.SSECustomerKey)
	if len(opts.SSECustomerKeyMD5) > 0 && !bytes.Equal(opts.SSECustomerKeyMD5, sum[:]) {
		return fmt.Errorf("%w: SSE-C key MD5 mismatch", ErrBadDigest)
	}
	opts.SSECustomerKeyMD5 = append([]byte(nil), sum[:]...)
	opts.Metadata = cloneMetadata(opts.Metadata)
	opts.Metadata[ssecAlgorithmMeta] = "AES256"
	opts.Metadata[ssecKeyMD5Meta] = base64.StdEncoding.EncodeToString(sum[:])
	return nil
}

func validateSSECRead(meta map[string]string, opts ReadOptions) error {
	storedMD5 := meta[ssecKeyMD5Meta]
	if storedMD5 == "" {
		if len(opts.SSECustomerKey) != 0 || len(opts.SSECustomerKeyMD5) != 0 {
			return fmt.Errorf("%w: object is not encrypted with SSE-C", ErrInvalidArgs)
		}
		return nil
	}
	if len(opts.SSECustomerKey) != 32 {
		return fmt.Errorf("%w: SSE-C key is required", ErrInvalidArgs)
	}
	sum := md5.Sum(opts.SSECustomerKey)
	if len(opts.SSECustomerKeyMD5) > 0 && !bytes.Equal(opts.SSECustomerKeyMD5, sum[:]) {
		return fmt.Errorf("%w: SSE-C key MD5 mismatch", ErrBadDigest)
	}
	if base64.StdEncoding.EncodeToString(sum[:]) != storedMD5 {
		return fmt.Errorf("%w: SSE-C key does not match object", ErrBadDigest)
	}
	return nil
}

func cloneMetadata(meta map[string]string) map[string]string {
	cloned := make(map[string]string, len(meta)+2)
	for key, value := range meta {
		cloned[key] = value
	}
	return cloned
}

func storageGetOptions(opts ReadOptions) storage.GetOptions {
	return storage.GetOptions{
		SSECustomerKey:    opts.SSECustomerKey,
		SSECustomerKeyMD5: opts.SSECustomerKeyMD5,
	}
}

func storagePutOptions(opts ReadOptions) storage.PutOptions {
	return storage.PutOptions{
		SSECustomerKey:    opts.SSECustomerKey,
		SSECustomerKeyMD5: opts.SSECustomerKeyMD5,
	}
}

func objectUsesSSEC(meta map[string]string) bool {
	return meta[ssecAlgorithmMeta] == "AES256" && meta[ssecKeyMD5Meta] != ""
}

// SSECustomerInfo returns response-safe SSE-C metadata. It never exposes the
// customer key.
func SSECustomerInfo(meta map[string]string) (algorithm, keyMD5 string, ok bool) {
	if !objectUsesSSEC(meta) {
		return "", "", false
	}
	return meta[ssecAlgorithmMeta], meta[ssecKeyMD5Meta], true
}
