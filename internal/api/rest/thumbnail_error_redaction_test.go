package rest

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

func TestWriteThumbnailSourceErrorRedactsAndLogsUnknownCause(t *testing.T) {
	const detail = "provider=https://objects.example.test request-token=redact-123"
	var logs bytes.Buffer
	h := &Handler{logger: slog.New(slog.NewTextHandler(&logs, nil))}
	r := httptest.NewRequest(http.MethodGet, "/v1/files/photo.png/thumbnail", nil)
	rec := httptest.NewRecorder()

	h.writeThumbnailSourceError(rec, r, errors.New(detail))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusInternalServerError)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"code":"InternalError"`) ||
		!strings.Contains(body, `"message":"internal server error"`) {
		t.Fatalf("body=%q lacks stable generic error", body)
	}
	if strings.Contains(body, detail) {
		t.Fatalf("provider detail leaked into response body: %q", body)
	}
	if !strings.Contains(logs.String(), detail) {
		t.Fatalf("provider detail was not retained in server log: %q", logs.String())
	}
}

func TestWriteThumbnailSourceErrorPreservesSafeKnownErrors(t *testing.T) {
	h := &Handler{}
	r := httptest.NewRequest(http.MethodGet, "/v1/files/photo.png/thumbnail", nil)
	rec := httptest.NewRecorder()

	h.writeThumbnailSourceError(rec, r, thumbnail.ErrNilSource)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusInternalServerError)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"code":"InternalError"`) ||
		!strings.Contains(body, `"message":"thumbnail source unavailable"`) {
		t.Fatalf("body=%q lacks safe nil-source classification", body)
	}
}
