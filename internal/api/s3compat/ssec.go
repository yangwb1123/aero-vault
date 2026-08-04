package s3compat

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"fmt"
	"net/http"

	"github.com/aero-vault/aero-vault/internal/service"
)

const (
	ssecHeaderPrefix      = "x-amz-server-side-encryption-customer"
	ssecCopyHeaderPrefix  = "x-amz-copy-source-server-side-encryption-customer"
	ssecCustomerAlgorithm = "AES256"
)

type ssecRequest struct {
	key    []byte
	keyMD5 []byte
}

func parseSSECRequest(header http.Header, prefix string) (ssecRequest, error) {
	algorithm := header.Get(prefix + "-algorithm")
	keyB64 := header.Get(prefix + "-key")
	md5B64 := header.Get(prefix + "-key-MD5")
	if algorithm == "" && keyB64 == "" && md5B64 == "" {
		return ssecRequest{}, nil
	}
	if algorithm != ssecCustomerAlgorithm || keyB64 == "" || md5B64 == "" {
		return ssecRequest{}, fmt.Errorf("%w: complete AES256 SSE-C headers are required", service.ErrInvalidArgs)
	}
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil || len(key) != 32 {
		return ssecRequest{}, fmt.Errorf("%w: SSE-C key must be base64-encoded AES-256", service.ErrInvalidArgs)
	}
	keyMD5, err := base64.StdEncoding.DecodeString(md5B64)
	if err != nil || len(keyMD5) != md5.Size {
		clearBytes(key)
		return ssecRequest{}, fmt.Errorf("%w: SSE-C key MD5 is invalid", service.ErrBadDigest)
	}
	sum := md5.Sum(key)
	if !bytes.Equal(keyMD5, sum[:]) {
		clearBytes(key)
		clearBytes(keyMD5)
		return ssecRequest{}, fmt.Errorf("%w: SSE-C key MD5 mismatch", service.ErrBadDigest)
	}
	return ssecRequest{key: key, keyMD5: keyMD5}, nil
}

func (request ssecRequest) readOptions() service.ReadOptions {
	return service.ReadOptions{
		SSECustomerKey:    request.key,
		SSECustomerKeyMD5: request.keyMD5,
	}
}

func (request ssecRequest) applyPutOptions(opts *service.PutOptions) {
	opts.SSECustomerKey = request.key
	opts.SSECustomerKeyMD5 = request.keyMD5
}

func (request ssecRequest) clear() {
	clearBytes(request.key)
	clearBytes(request.keyMD5)
}

func clearBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

func writeSSECHeaders(w http.ResponseWriter, meta map[string]string) {
	algorithm, keyMD5, ok := service.SSECustomerInfo(meta)
	if !ok {
		return
	}
	w.Header().Set(ssecHeaderPrefix+"-algorithm", algorithm)
	w.Header().Set(ssecHeaderPrefix+"-key-MD5", keyMD5)
}
