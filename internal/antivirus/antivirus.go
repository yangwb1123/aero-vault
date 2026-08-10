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
	// Streaming matcher: single pass over the whole stream, O(maxSigLen) memory.
	// The window keeps the last maxSigLen-1 bytes; every signature occurrence is
	// fully contained in window∪chunk at the iteration where its final byte
	// arrives (its start stays in the window until then), so no offset is
	// missed. Clean is returned only after the stream reports EOF — a partial
	// read can never produce a clean verdict, so >32 MiB tails are scanned
	// instead of being silently truncated.
	maxSigLen := 0
	for _, sig := range s.sigs {
		if len(sig) > maxSigLen {
			maxSigLen = len(sig)
		}
	}
	if maxSigLen == 0 {
		return Result{Clean: true}, nil // no signatures configured
	}
	win := make([]byte, 0, maxSigLen-1+64<<10)
	chunk := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		n, err := r.Read(chunk)
		if n > 0 {
			win = append(win, chunk[:n]...)
			for name, sig := range s.sigs {
				if len(sig) > 0 && bytes.Contains(win, sig) {
					return Result{Clean: false, Signature: name}, nil
				}
			}
			if keep := maxSigLen - 1; len(win) > keep {
				win = append(win[:0], win[len(win)-keep:]...) // trim front; overlap-safe (memmove semantics)
			}
		}
		if err != nil {
			if err == io.EOF {
				return Result{Clean: true}, nil
			}
			return Result{}, err
		}
	}
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
	// Bound the response: a verdict is at most a few hundred bytes, so 1 MiB is
	// ample for any real engine; a hostile or broken endpoint must not be able
	// to stream unbounded JSON into worker memory until the 60 s client timeout.
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return Result{}, errors.New("antivirus: malformed scanner response")
	}
	return Result{Clean: body.Clean, Signature: body.Signature}, nil
}
