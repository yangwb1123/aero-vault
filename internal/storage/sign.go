package storage

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// signLocal builds the canonical HMAC-SHA256 signature for the local presign scheme.
func signLocal(key, method, objectKey string, expires int64) string {
	canonical := fmt.Sprintf("%s\n%s\n%d", method, objectKey, expires)
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(canonical))
	return hex.EncodeToString(mac.Sum(nil))
}

func hmacEqual(a, b string) bool {
	return hmac.Equal([]byte(a), []byte(b))
}
