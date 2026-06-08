package storage

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// httpKMS is a DataKeyWrapper backed by an HTTP key-management endpoint that
// wraps/unwraps data keys remotely — the wrapping (master) key never leaves the
// KMS. It speaks a small generic shape, compatible with a thin proxy in front of
// AWS KMS / GCP KMS / Vault Transit:
//
//	POST {endpoint}/wrap   {"key_id":"…","plaintext":"<base64 data key>"}
//	  -> {"key_id":"…","ciphertext":"<base64>"}
//	POST {endpoint}/unwrap {"key_id":"…","ciphertext":"<base64>"}
//	  -> {"plaintext":"<base64 data key>"}
//
// The /wrap response may echo a more specific key_id (e.g. a versioned ARN); that
// id is recorded in the object envelope and passed back to /unwrap.
type httpKMS struct {
	endpoint string
	keyID    string
	token    string
	client   *http.Client
}

func newHTTPKMS(endpoint, keyID, token string) *httpKMS {
	return &httpKMS{
		endpoint: strings.TrimRight(endpoint, "/"),
		keyID:    keyID,
		token:    token,
		client:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (k *httpKMS) WrapKey(dataKey []byte) ([]byte, string, error) {
	var out struct {
		KeyID      string `json:"key_id"`
		Ciphertext string `json:"ciphertext"`
	}
	if err := k.call("/wrap", map[string]string{
		"key_id":    k.keyID,
		"plaintext": base64.StdEncoding.EncodeToString(dataKey),
	}, &out); err != nil {
		return nil, "", err
	}
	wrapped, err := base64.StdEncoding.DecodeString(out.Ciphertext)
	if err != nil {
		return nil, "", fmt.Errorf("kms: decode ciphertext: %w", err)
	}
	if len(wrapped) == 0 {
		return nil, "", fmt.Errorf("kms: empty wrapped key in response")
	}
	keyID := out.KeyID
	if keyID == "" {
		keyID = k.keyID
	}
	return wrapped, keyID, nil
}

func (k *httpKMS) UnwrapKey(wrapped []byte, keyID string) ([]byte, error) {
	var out struct {
		Plaintext string `json:"plaintext"`
	}
	if err := k.call("/unwrap", map[string]string{
		"key_id":     keyID,
		"ciphertext": base64.StdEncoding.EncodeToString(wrapped),
	}, &out); err != nil {
		return nil, err
	}
	dataKey, err := base64.StdEncoding.DecodeString(out.Plaintext)
	if err != nil {
		return nil, fmt.Errorf("kms: decode plaintext: %w", err)
	}
	if len(dataKey) != masterKeyLen {
		return nil, fmt.Errorf("kms: unwrapped data key is %d bytes, want %d", len(dataKey), masterKeyLen)
	}
	return dataKey, nil
}

func (k *httpKMS) call(path string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, k.endpoint+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if k.token != "" {
		req.Header.Set("Authorization", "Bearer "+k.token)
	}
	resp, err := k.client.Do(req)
	if err != nil {
		return fmt.Errorf("kms %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("kms %s http %d: %s", path, resp.StatusCode, string(msg))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}
