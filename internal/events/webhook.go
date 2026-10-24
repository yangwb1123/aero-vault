package events

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/telemetry"
)

// jitter returns duration with downward-only jitter in [0.75, 1.0) × d —
// never above the base: offset ∈ [0, d/2) → d - d/4 + offset/2 ∈ [0.75d, d).
// Deliberate (D-7): strictly faster than base shrinks the C-5 at-least-once
// window; TestEventOutboxBackoffBounds pins these exact bounds.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	// [0.75, 1.0) * d — downward-only, not ±25%.
	n, err := rand.Int(rand.Reader, big.NewInt(int64(d/2)))
	if err != nil {
		return d // fallback: no jitter
	}
	offset := n.Int64()
	base := int64(d)
	return time.Duration(base - base/4 + offset/2)
}

// Webhook fans out persisted events only after fresh deny-only admission for
// each configured URL. Failed HTTP deliveries are recorded in the optional
// retry store and re-admitted before re-sending with exponential backoff.
type Webhook struct {
	urls      []string
	secret    []byte
	client    *http.Client
	logger    *slog.Logger
	repo      repository.Repository
	authority *EventAuthority
	boundary  *ResourceBoundary
}

func NewWebhook(urls string, logger *slog.Logger) *Webhook {
	parts := strings.Split(urls, ",")
	cleaned := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			cleaned = append(cleaned, p)
		}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Webhook{urls: cleaned, client: &http.Client{Timeout: 5 * time.Second}, logger: logger}
}

// WithSecret enables HMAC signing for outbound webhooks.
func (w *Webhook) WithSecret(secret string) *Webhook {
	w.secret = []byte(secret)
	return w
}

// WithRetryStore wires the repository so failed deliveries get persisted.
func (w *Webhook) WithRetryStore(repo repository.Repository) *Webhook {
	w.repo = repo
	return w
}

// WithEventAuthority injects the persisted-event reader independently of the
// optional retry store. Missing or nil authority refuses all delivery.
func (w *Webhook) WithEventAuthority(authority *EventAuthority) *Webhook {
	w.authority = authority
	return w
}

// WithResourceBoundary injects deny-only admission for every outbound attempt.
// Missing or nil boundary is a fault, never a legacy allow.
func (w *Webhook) WithResourceBoundary(boundary *ResourceBoundary) *Webhook {
	w.boundary = boundary
	return w
}

// Run loops on the subscription until ctx is canceled. It is intended to be
// `go bus.Subscribe(); go webhook.Run(ctx, bus)`.
func (w *Webhook) Run(ctx context.Context, sub <-chan repository.Event) {
	if len(w.urls) == 0 {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-sub:
			if !ok {
				return
			}
			w.deliver(ctx, e)
		}
	}
}

func (w *Webhook) deliver(ctx context.Context, e repository.Event) {
	// Only persisted fields may form the initial envelope. Each target below
	// gets a fresh authority/boundary observation immediately before its POST.
	e, err := w.authority.Reread(ctx, e)
	if err != nil {
		w.logger.Warn("webhook delivery refused", "error_class", "authority")
		return
	}
	body, err := json.Marshal(map[string]any{
		"id":         e.ID,
		"tenant":     e.TenantID,
		"bucket":     e.Bucket,
		"key":        e.Key,
		"type":       string(e.Type),
		"object_id":  e.ObjectID,
		"request_id": e.RequestID,
		"payload":    e.Payload,
		"created_at": e.CreatedAt.Format(time.RFC3339Nano),
	})
	if err != nil {
		w.logger.Warn("webhook delivery refused", "error_class", "encoding")
		return
	}
	sig := w.signature(body)
	for _, u := range w.urls {
		if _, err := w.admitEvent(ctx, e); err != nil {
			w.logger.Warn("webhook delivery refused", "error_class", "admission")
			continue
		}
		w.postOne(ctx, e.ID, u, body, sig, 1)
	}
}

func (w *Webhook) admitEvent(ctx context.Context, e repository.Event) (repository.Event, error) {
	e, err := w.authority.Reread(ctx, e)
	if err != nil {
		return repository.Event{}, err
	}
	if err := admitEventBucket(ctx, w.boundary, e.TenantID, e.Bucket); err != nil {
		return repository.Event{}, err
	}
	if err := ctx.Err(); err != nil {
		return repository.Event{}, newResourceBoundaryFault(err)
	}
	return e, nil
}

func (w *Webhook) signature(body []byte) string {
	if len(w.secret) == 0 {
		return ""
	}
	mac := hmac.New(sha256.New, w.secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// postOne handles a single delivery attempt + persists a retry row on failure.
func (w *Webhook) postOne(ctx context.Context, eventID int64, url string, body []byte, sig string, attempt int) {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		telemetry.RecordWebhookDelivery(ctx, url, 0)
		w.persistFailure(ctx, eventID, url, body, err.Error(), 0, attempt)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if sig != "" {
		req.Header.Set("X-Aero-Signature", sig)
		req.Header.Set("X-Aero-Event-Id", fmtInt(eventID))
	}
	resp, err := w.client.Do(req)
	latency := time.Since(start).Seconds() * 1000
	if err != nil {
		telemetry.RecordWebhookDelivery(ctx, url, 0)
		telemetry.RecordWebhookDeliveryLatency(ctx, url, latency)
		w.persistFailure(ctx, eventID, url, body, err.Error(), 0, attempt)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	telemetry.RecordWebhookDelivery(ctx, url, resp.StatusCode)
	telemetry.RecordWebhookDeliveryLatency(ctx, url, latency)
	if resp.StatusCode >= 300 {
		w.persistFailure(ctx, eventID, url, body, "non-2xx", resp.StatusCode, attempt)
		return
	}
}

func (w *Webhook) persistFailure(ctx context.Context, eventID int64, url string, body []byte, lastErr string, status, attempt int) {
	w.logger.Warn("webhook delivery failed", "url", url, "status", status, "err", lastErr)
	if eventAuthorityMissing(w.repo) {
		return
	}
	// exp backoff: 30s * 2^(attempt-1), cap 1h, +jitter
	backoff := time.Duration(30) * time.Second
	for i := 1; i < attempt && backoff < time.Hour; i++ {
		backoff *= 2
	}
	backoff = jitter(backoff)
	next := time.Now().Add(backoff)
	if _, err := w.repo.RecordWebhookFailure(ctx, repository.WebhookFailure{
		EventID: eventID, URL: url, Payload: string(body),
		Attempts: attempt, LastError: lastErr, LastStatus: status, NextRetryAt: next,
	}); err != nil {
		w.logger.Warn("persist webhook failure", "err", err)
	}
}

// RetryLoop polls webhook_failures and re-delivers entries whose next_retry_at
// has elapsed. Intended to run as a goroutine.
func (w *Webhook) RetryLoop(ctx context.Context) {
	if eventAuthorityMissing(w.repo) {
		return
	}
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pending, err := w.repo.NextPendingFailures(ctx, 25)
			if err != nil {
				w.logger.Warn("retry pending fetch", "err", err)
				continue
			}
			for _, f := range pending {
				w.retryOne(ctx, f)
			}
		}
	}
}

func (w *Webhook) retryOne(ctx context.Context, f repository.WebhookFailure) {
	e, err := decodeWebhookRetryEvent(f)
	if err != nil {
		w.deadLetterRetry(ctx, f, "invalid webhook event envelope", 0, f.Attempts+1)
		return
	}
	if _, err := w.admitEvent(ctx, e); err != nil {
		// Faults (even with a nested Denied/NotFound cause) remain temporary
		// under the existing finite retry policy. Only explicit refusal is terminal.
		if !IsEventAuthorityFault(err) && IsResourceBoundaryDenied(err) {
			w.deadLetterRetry(ctx, f, "webhook admission denied", 0, f.Attempts+1)
		} else {
			w.handleRetryFailure(ctx, f, "webhook admission fault", 0)
		}
		return
	}
	// Admission must not repair/re-encode the stored body: signatures and
	// delivery bytes remain exactly those persisted by the original failure.
	w.sendRetry(ctx, f)
}

func decodeWebhookRetryEvent(f repository.WebhookFailure) (repository.Event, error) {
	e, err := decodeWebhookEventEnvelope([]byte(f.Payload))
	if err != nil {
		return repository.Event{}, err
	}
	if f.EventID <= 0 || e.ID != f.EventID {
		return repository.Event{}, errors.New("webhook row and envelope event IDs differ")
	}
	if e.TenantID == "" || e.Bucket == "" || e.Key == "" || e.Type == "" {
		return repository.Event{}, errors.New("incomplete webhook event envelope")
	}
	return e, nil
}

func (w *Webhook) sendRetry(ctx context.Context, f repository.WebhookFailure) {
	body := []byte(f.Payload)
	sig := w.signature(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.URL, bytes.NewReader(body))
	if err != nil {
		w.handleRetryFailure(ctx, f, "bad url: "+err.Error(), 0)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if sig != "" {
		req.Header.Set("X-Aero-Signature", sig)
		req.Header.Set("X-Aero-Event-Id", fmtInt(f.EventID))
		req.Header.Set("X-Aero-Retry-Attempt", fmtInt(int64(f.Attempts+1)))
	}
	resp, err := w.client.Do(req)
	telemetry.IncWebhookRetry(ctx, f.URL)
	if err == nil {
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode < 300 {
			if !eventAuthorityMissing(w.repo) {
				if err := w.repo.MarkWebhookSucceeded(ctx, f.ID); err != nil {
					w.logger.Warn("mark webhook succeeded", "id", f.ID, "err", err)
				}
			}
			return
		}
		w.handleRetryFailure(ctx, f, fmt.Sprintf("non-2xx: %d", resp.StatusCode), resp.StatusCode)
		return
	}
	w.handleRetryFailure(ctx, f, err.Error(), 0)
}

func (w *Webhook) handleRetryFailure(
	ctx context.Context, failure repository.WebhookFailure, lastErr string, lastStatus int,
) {
	if eventAuthorityMissing(w.repo) {
		return
	}
	attempts := failure.Attempts + 1
	if attempts >= 10 {
		message := "dead-lettered after " + fmtInt(int64(attempts)) + " attempts: " + lastErr
		w.deadLetterRetry(ctx, failure, message, lastStatus, attempts)
		return
	}
	backoff := time.Duration(30) * time.Second
	for i := 1; i < attempts && backoff < time.Hour; i++ {
		backoff *= 2
	}
	backoff = jitter(backoff)
	if err := w.repo.UpdateWebhookFailure(
		ctx, failure.ID, lastErr, lastStatus, time.Now().Add(backoff), attempts,
	); err != nil {
		w.logger.Warn("update webhook retry", "id", failure.ID, "err", err)
	}
}

func (w *Webhook) deadLetterRetry(
	ctx context.Context, failure repository.WebhookFailure, message string, status, attempts int,
) {
	if eventAuthorityMissing(w.repo) {
		return
	}
	if err := w.repo.MarkWebhookDeadLettered(ctx, failure.ID, message, status, attempts); err != nil {
		w.logger.Warn("mark webhook dead-lettered", "id", failure.ID, "err", err)
		return
	}
	telemetry.IncWebhookDeadLetter(ctx, failure.URL)
}

func fmtInt(n int64) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%10]
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
