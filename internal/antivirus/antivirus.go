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
// always includes the EICAR test signature. Scanning is streaming: the whole
// object is consumed through a sliding window (clean only after EOF), so a
// signature beyond 32 MiB is still detected while memory stays bounded by
// maxSigLen + one 64 KiB chunk.
type SignatureScanner struct {
	sigs      map[string][]byte
	maxSigLen int // longest configured signature; 0 = no signatures configured
}

// scanChunkSize is the streaming read granularity; the matcher keeps one
// chunk plus the maxSigLen tail that could still complete a signature.
const scanChunkSize = 64 << 10

// NewSignatureScanner builds the default scanner. extra maps a threat name to a
// byte signature to additionally flag.
func NewSignatureScanner(extra map[string]string) *SignatureScanner {
	s := &SignatureScanner{sigs: map[string][]byte{"EICAR-Test-File": []byte(EICAR)}}
	for name, sig := range extra {
		if sig != "" {
			s.sigs[name] = []byte(sig)
		}
	}
	for _, sig := range s.sigs {
		if len(sig) > s.maxSigLen {
			s.maxSigLen = len(sig)
		}
	}
	return s
}

func (s *SignatureScanner) Name() string { return "signature" }

// Scan streams r through a sliding window and reports clean only after the
// whole stream is consumed (EOF): tail malware beyond any fixed cap is still
// detected. Signatures straddling chunk boundaries are matched via the
// retained window. The zero value (no signatures configured) certifies clean
// without reading anything.
func (s *SignatureScanner) Scan(ctx context.Context, r io.Reader) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if s.maxSigLen == 0 {
		return Result{Clean: true}, nil
	}
	chunk := make([]byte, scanChunkSize)
	window := make([]byte, s.maxSigLen)
	carried := 0
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			if hit, name := s.match(chunk[:n], window[:carried]); hit {
				return Result{Clean: false, Signature: name}, nil
			}
			// Retain the tail that could still complete a signature across
			// the next chunk boundary (maxSigLen-1 bytes; in-place copy).
			keep := s.maxSigLen - 1
			if keep > n {
				keep = n
			}
			copy(window, chunk[n-keep:n])
			carried = keep
		}
		if err == io.EOF {
			return Result{Clean: true}, nil
		}
		if err != nil {
			return Result{}, err
		}
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
	}
}

// match reports whether any signature is contained in chunk or spans the
// window→chunk junction (window holds the previous tail). The signature's
// remainder is truncated to the chunk length: a signature may span several
// chunks, and the retained window always holds its in-flight bytes
// (window ≥ maxSigLen-1). Allocation-free.
func (s *SignatureScanner) match(chunk, window []byte) (bool, string) {
	for name, sig := range s.sigs {
		if bytes.Contains(chunk, sig) {
			return true, name
		}
		maxPref := len(sig) - 1
		if maxPref > len(window) {
			maxPref = len(window)
		}
		for pref := maxPref; pref > 0; pref-- {
			if !bytes.Equal(window[len(window)-pref:], sig[:pref]) {
				continue
			}
			rest := sig[pref:]
			if len(rest) > len(chunk) {
				rest = rest[:len(chunk)]
			}
			if bytes.HasPrefix(chunk, rest) {
				return true, name
			}
		}
	}
	return false, ""
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
	// Bound the response: a hostile endpoint streaming >1 MiB of JSON is cut
	// off, the decode fails and the scan fails closed — no unbounded
	// allocation, no clean verdict from a garbage body.
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxScanResponseBytes+1)).Decode(&body); err != nil {
		return Result{}, errors.New("antivirus: malformed scanner response")
	}
	return Result{Clean: body.Clean, Signature: body.Signature}, nil
}

// maxScanResponseBytes bounds the remote scanner JSON reply (F4).
const maxScanResponseBytes = 1 << 20
