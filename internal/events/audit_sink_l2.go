package events

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// audit_sink_l2.go — the L2 adapter of the AuditSink port (FR-2/FR-4). It
// POSTs the verbatim deleted@1.1 payload to a per-tenant-configured HTTPS (or
// loopback HTTP) endpoint with a static bearer token. Security baseline
// H1–H6 mirrors the audit-governance Publisher precedent — deliberately NOT
// the notify path's loose shape (no endpoint validation, redirects followed):
// the payload carries object key/bucket/metadata/actor unredacted (H3) and
// the token is a long-lived secret.

const (
	auditSinkL2MaxPayloadBytes  = 1 << 20  // F9: defense in depth (insert-time check is authoritative)
	auditSinkL2MaxResponseBytes = 64 << 10 // H3: bounded response read (governance maxResponseBytes precedent)
	auditSinkL2FactIDHeader     = "X-Audit-Fact-Id"
)

// AuditSinkL2 is the configuration-driven HTTP adapter. It holds no
// per-request state and is safe for concurrent use by the relay's per-fact
// goroutines.
type AuditSinkL2 struct {
	endpoint string
	bindings map[string]string // tenant → static bearer token
	client   *http.Client
	logger   *slog.Logger
}

// NewAuditSinkL2 builds the adapter. bindings maps tenant → bearer token.
// client is used with its Timeout/Transport but its redirect policy is
// always forced to "never follow" (H6 — even a caller-provided client that
// follows redirects cannot forward the payload or the Authorization header
// to a redirect target). The endpoint is re-validated here (H1 defense in
// depth): config.Validate already rejected bad shapes at startup; this
// constructor is the second enforcement point so a programmatically built
// adapter cannot bypass the TLS-or-loopback rule.
func NewAuditSinkL2(
	endpoint string, bindings map[string]string, client *http.Client, logger *slog.Logger,
) (*AuditSinkL2, error) {
	if err := validateAuditSinkL2Endpoint(endpoint); err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	// H6: copy the client and force CheckRedirect. The relay's own client
	// follows ≤10 redirects by default; reusing it would forward the payload
	// on 307/308 and keep Authorization on same-host redirects.
	redirectFree := *client
	redirectFree.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &AuditSinkL2{
		endpoint: endpoint,
		bindings: bindings,
		client:   &redirectFree,
		logger:   logger,
	}, nil
}

// validateAuditSinkL2Endpoint enforces H1/H6: absolute URL, no
// userinfo/query/fragment, scheme https or loopback http. Mirrors
// validateAuditGovernanceURL; errors never include URL contents beyond the
// shape description (H5).
func validateAuditSinkL2Endpoint(raw string) error {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint == nil || endpoint.Host == "" ||
		endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return errors.New("audit sink: L2 endpoint must be an absolute URL without credentials, query, or fragment")
	}
	if endpoint.Scheme == "https" {
		return nil
	}
	if endpoint.Scheme != "http" || !auditSinkL2Loopback(endpoint.Hostname()) {
		return errors.New("audit sink: L2 endpoint must use HTTPS or loopback HTTP")
	}
	return nil
}

func auditSinkL2Loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// NewAuditSinkL2Client builds an http.Client with the given timeout and
// redirects disabled, for wiring in cmd/server (the relay's own client must
// not be shared with the adapter — H6).
func NewAuditSinkL2Client(timeout time.Duration) *http.Client {
	client := &http.Client{Timeout: timeout}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return client
}

// DeliverDeleted implements AuditSink. The payload is POSTed byte-exact —
// never re-marshalled, never enriched (verbatim invariant, D6). Success is
// 2xx AND the response echoing the exact X-Audit-Fact-Id value (echo
// receipt, D5/FR-2); a 2xx without the echo is an error so the relay retries
// (the receiver's commit point is 2xx+echo — C11).
func (s *AuditSinkL2) DeliverDeleted(ctx context.Context, tenant string, factID int64, payload []byte) error {
	token, ok := s.bindings[tenant]
	if !ok {
		return ErrSinkNotBound
	}
	if len(payload) == 0 || len(payload) > auditSinkL2MaxPayloadBytes {
		return errors.New("audit sink: L2 payload size out of range") // H5: no sizes/bytes in the error
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(payload))
	if err != nil {
		return errors.New("audit sink: L2 request could not be built")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set(auditSinkL2FactIDHeader, strconv.FormatInt(factID, 10))
	request.Header.Set("Content-Type", "application/json")

	response, err := s.client.Do(request)
	if err != nil {
		// H5: collapse transport errors to a generic class — the raw error
		// may embed the endpoint/URL, which must not reach last_error/logs.
		return errors.New("audit sink: L2 delivery failed")
	}
	defer response.Body.Close()
	// H3: bounded read; the body is never logged, never echoed into errors.
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, auditSinkL2MaxResponseBytes+1))

	// H2: 401/403 short-circuit before the receipt check — a static token
	// has no refresh path, so retrying is pointless (and re-POSTs the
	// sensitive payload at a rejecting target).
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return ErrSinkUnauthorized
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("audit sink: L2 returned HTTP %d", response.StatusCode) // H5: status code only
	}
	if got := strings.TrimSpace(response.Header.Get(auditSinkL2FactIDHeader)); got != strconv.FormatInt(factID, 10) {
		return errors.New("audit sink: L2 response did not echo X-Audit-Fact-Id")
	}
	return nil
}
