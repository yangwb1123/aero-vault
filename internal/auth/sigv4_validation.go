package auth

import (
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

func validateCredentialScope(scope, amzDate string) error {
	parts := strings.Split(scope, "/")
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" ||
		parts[2] != sigV4Service || parts[3] != "aws4_request" {
		return errors.New("sigv4: invalid credential scope")
	}
	if len(amzDate) < 8 || parts[0] != amzDate[:8] {
		return errors.New("sigv4: credential date mismatch")
	}
	return nil
}

func validateHeaderTime(amzDate string, now time.Time) error {
	signedAt, err := time.Parse("20060102T150405Z", amzDate)
	if err != nil {
		return errors.New("sigv4: invalid X-Amz-Date")
	}
	if signedAt.Before(now.Add(-maxHeaderSkew)) || signedAt.After(now.Add(maxHeaderSkew)) {
		return errors.New("sigv4: request time outside allowed skew")
	}
	return nil
}

func validateSignedHeaders(headers []string) error {
	seen := make(map[string]struct{}, len(headers))
	hasHost := false
	for _, header := range headers {
		header = strings.ToLower(strings.TrimSpace(header))
		if header == "" {
			return errors.New("sigv4: empty signed header")
		}
		if _, duplicate := seen[header]; duplicate {
			return errors.New("sigv4: duplicate signed header")
		}
		seen[header] = struct{}{}
		hasHost = hasHost || header == "host"
	}
	if !hasHost {
		return errors.New("sigv4: host must be signed")
	}
	return nil
}

func validatePayloadHash(payloadHash string) error {
	if payloadHash == unsignedPayload || payloadHash == streamingPayload {
		return nil
	}
	if len(payloadHash) != sha256HexLength {
		return errors.New("sigv4: invalid payload hash")
	}
	if _, err := hex.DecodeString(payloadHash); err != nil {
		return errors.New("sigv4: invalid payload hash")
	}
	return nil
}
