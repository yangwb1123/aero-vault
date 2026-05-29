// Package antivirus scans stored objects for malware. It ships a dependency-free
// signature scanner (which detects the industry-standard EICAR test file, so the
// pipeline is verifiable without a real engine) and an HTTP scanner that defers
// to an external service (ClamAV REST/ICAP shim, etc.). Scanning runs
// asynchronously via the background job queue.
package antivirus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Result is the outcome of a scan.
type Result struct {
	Clean     bool
	Signature string // name of the detected threat when not clean
}

// Scanner inspects bytes for malware.
type Scanner interface {
	Scan(ctx context.Context, r io.Reader) (Result, error)
	Name() string
}

// EICAR is the standard antivirus test string. Any compliant scanner flags it;
// the built-in SignatureScanner detects it so the feature is testable offline.
const EICAR = `X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`

// SignatureScanner flags content containing any of its byte signatures. It
// always includes the EICAR test signature.
type SignatureScanner struct {
	sigs map[string][]byte
}

// NewSignatureScanner builds the default scanner. extra maps a threat name to a
// byte signature to additionally flag.
func NewSignatureScanner(extra map[string]string) *SignatureScanner {
	s := &SignatureScanner{sigs: map[string][]byte{"EICAR-Test-File": []byte(EICAR)}}
	for name, sig := range extra {
		if sig != "" {
			s.sigs[name] = []byte(sig)
		}
	}
	return s
}

func (s *SignatureScanner) Name() string { return "signature" }

func (s *SignatureScanner) Scan(ctx context.Context, r io.Reader) (Result, error) {
	// Bounded read: scan up to 32 MiB (signatures are small; large binaries are
	// streamed past for a remote engine, not the local matcher).
	const max = 32 << 20
	data, err := io.ReadAll(io.LimitReader(r, max))
	if err != nil {
		return Result{}, err
	}
	for name, sig := range s.sigs {
		if bytes.Contains(data, sig) {
			return Result{Clean: false, Signature: name}, nil
		}
	}
	return Result{Clean: true}, nil
}

// HTTPScanner POSTs the object bytes to an external scanning service and expects
// a JSON reply of the form {"clean":bool,"signature":"..."}.
type HTTPScanner struct {
	endpoint string
	apiKey   string
	client   *http.Client
}

func NewHTTPScanner(endpoint, apiKey string) *HTTPScanner {
	return &HTTPScanner{endpoint: endpoint, apiKey: apiKey, client: &http.Client{Timeout: 60 * time.Second}}
}

func (h *HTTPScanner) Name() string { return "http" }

func (h *HTTPScanner) Scan(ctx context.Context, r io.Reader) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.endpoint, r)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if h.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.apiKey)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return Result{}, fmt.Errorf("antivirus: scanner returned %d", resp.StatusCode)
	}
	var body struct {
		Clean     bool   `json:"clean"`
		Signature string `json:"signature"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Result{}, errors.New("antivirus: malformed scanner response")
	}
	return Result{Clean: body.Clean, Signature: body.Signature}, nil
}
