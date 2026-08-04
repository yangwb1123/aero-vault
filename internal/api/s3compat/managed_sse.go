package s3compat

import (
	"net/http"

	"github.com/aero-vault/aero-vault/internal/service"
)

const (
	managedSSEHeader    = "x-amz-server-side-encryption"
	managedSSEKeyHeader = "x-amz-server-side-encryption-aws-kms-key-id"
)

func applyManagedSSEHeaders(header http.Header, opts *service.PutOptions) {
	opts.SSEAlgorithm = header.Get(managedSSEHeader)
	opts.SSEKMSKeyID = header.Get(managedSSEKeyHeader)
}

func writeEncryptionHeaders(w http.ResponseWriter, meta map[string]string) {
	writeSSECHeaders(w, meta)
	algorithm, keyID, ok := service.ServerSideEncryptionInfo(meta)
	if !ok {
		return
	}
	w.Header().Set(managedSSEHeader, algorithm)
	if keyID != "" {
		w.Header().Set(managedSSEKeyHeader, keyID)
	}
}
