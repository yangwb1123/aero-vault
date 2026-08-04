package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"net/http"
	"strings"
)

const sha256HexLength = 64

// PrepareBody installs payload verification after the seed request signature
// has been authenticated. It preserves streaming reads for large uploads.
func (v *SigV4Verifier) PrepareBody(req *http.Request) error {
	payloadHash := req.Header.Get("X-Amz-Content-Sha256")
	switch payloadHash {
	case "", unsignedPayload:
		return nil
	case streamingPayload:
		return v.prepareStreamingBody(req)
	}
	if err := validatePayloadHash(payloadHash); err != nil {
		return err
	}
	if req.ContentLength == 0 {
		empty := sha256.Sum256(nil)
		if hex.EncodeToString(empty[:]) != strings.ToLower(payloadHash) {
			return errors.New("sigv4: payload hash mismatch")
		}
		return nil
	}
	if req.Body == nil {
		return errors.New("sigv4: signed payload body is missing")
	}
	req.Body = &payloadVerifier{
		source:   req.Body,
		digest:   sha256.New(),
		expected: strings.ToLower(payloadHash),
		length:   req.ContentLength,
	}
	return nil
}

type payloadVerifier struct {
	source   io.ReadCloser
	digest   hash.Hash
	expected string
	length   int64
	read     int64
	checked  bool
}

func (v *payloadVerifier) Read(p []byte) (int, error) {
	n, err := v.source.Read(p)
	if n > 0 {
		_, _ = v.digest.Write(p[:n])
		v.read += int64(n)
	}
	if !v.checked && (errors.Is(err, io.EOF) || v.length >= 0 && v.read == v.length) {
		v.checked = true
		if hex.EncodeToString(v.digest.Sum(nil)) != v.expected {
			return n, errors.New("sigv4: payload hash mismatch")
		}
	}
	return n, err
}

func (v *payloadVerifier) Close() error {
	return v.source.Close()
}
