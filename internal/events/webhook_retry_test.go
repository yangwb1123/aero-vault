package events

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// retryOne must not panic on a malformed URL: http.NewRequest returns an error and
// a nil request, so the old `req, _ :=` would nil-deref on req.Header.Set. The fix
// records the failed attempt and returns instead.
func TestWebhook_RetryOne_MalformedURLDoesNotPanic(t *testing.T) {
	w := &Webhook{client: &http.Client{}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	// No repo wired → persistFailure just logs; the assertion is simply: no panic.
	w.retryOne(context.Background(), repository.WebhookFailure{EventID: 1, URL: "://not a url", Payload: "{}"})
}
