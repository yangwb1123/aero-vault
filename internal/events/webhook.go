package events

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// Webhook is an outbound fanout: every event seen on the bus is POSTed to the
// configured URL(s). Failed deliveries are recorded in the repository so the
// retry worker can re-send with exponential backoff.
type Webhook struct {
	urls   []string
	secret []byte
	client *http.Client
	logger *slog.Logger
	repo   repository.Repository
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
	body, _ := json.Marshal(map[string]any{
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
	var sig string
	if len(w.secret) > 0 {
		mac := hmac.New(sha256.New, w.secret)
		mac.Write(body)
		sig = "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	for _, u := range w.urls {
		w.postOne(ctx, e.ID, u, body, sig, 1)
	}
}

// postOne handles a single delivery attempt + persists a retry row on failure.
func (w *Webhook) postOne(ctx context.Context, eventID int64, url string, body []byte, sig string, attempt int) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		w.persistFailure(ctx, eventID, url, body, err.Error(), 0, attempt)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if sig != "" {
		req.Header.Set("X-Aero-Signature", sig)
		req.Header.Set("X-Aero-Event-Id", fmtInt(eventID))
	}
	resp, err := w.client.Do(req)
	if err != nil {
		w.persistFailure(ctx, eventID, url, body, err.Error(), 0, attempt)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		w.persistFailure(ctx, eventID, url, body, "non-2xx", resp.StatusCode, attempt)
		return
	}
}

func (w *Webhook) persistFailure(ctx context.Context, eventID int64, url string, body []byte, lastErr string, status, attempt int) {
	w.logger.Warn("webhook delivery failed", "url", url, "status", status, "err", lastErr)
	if w.repo == nil {
		return
	}
	// exp backoff: 30s * 2^(attempt-1), cap 1h
	backoff := time.Duration(30) * time.Second
	for i := 1; i < attempt && backoff < time.Hour; i++ {
		backoff *= 2
	}
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
	if w.repo == nil {
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
	body := []byte(f.Payload)
	var sig string
	if len(w.secret) > 0 {
		mac := hmac.New(sha256.New, w.secret)
		mac.Write(body)
		sig = "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, f.URL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if sig != "" {
		req.Header.Set("X-Aero-Signature", sig)
		req.Header.Set("X-Aero-Event-Id", fmtInt(f.EventID))
		req.Header.Set("X-Aero-Retry-Attempt", fmtInt(int64(f.Attempts+1)))
	}
	resp, err := w.client.Do(req)
	attempts := f.Attempts + 1
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode < 300 {
			_ = w.repo.MarkWebhookSucceeded(ctx, f.ID)
			return
		}
		err = fmt.Errorf("non-2xx: %d", resp.StatusCode)
	}
	// give up after 10 attempts
	if attempts >= 10 {
		_ = w.repo.UpdateWebhookFailure(ctx, f.ID, err.Error(), 0, time.Now().Add(24*time.Hour), attempts)
		return
	}
	backoff := time.Duration(30) * time.Second
	for i := 1; i < attempts && backoff < time.Hour; i++ {
		backoff *= 2
	}
	_ = w.repo.UpdateWebhookFailure(ctx, f.ID, err.Error(), 0, time.Now().Add(backoff), attempts)
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
