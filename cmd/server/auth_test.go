package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/config"
)

func TestBuildAuthRegistryMalformedKeysFailsClosed(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	cfg := &config.Config{Auth: config.AuthConfig{Keys: "valid:acme:read,malformed"}}

	reg := buildAuthRegistry(context.Background(), cfg, logger, nil)
	if reg == nil {
		t.Fatal("buildAuthRegistry returned nil")
	}
	if !reg.Enabled() {
		t.Fatal("malformed AUTH_KEYS must leave authentication enabled")
	}

	called := false
	protected := reg.Middleware()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/files/secret.txt", nil)
	req.Header.Set("Authorization", "Bearer valid")
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusUnauthorized)
	}
	if called {
		t.Fatal("protected handler ran under malformed AUTH_KEYS")
	}

	logged := logs.String()
	if !strings.Contains(logged, "authentication locked down") {
		t.Fatalf("missing locked-down log message: %s", logged)
	}
	if strings.Contains(logged, "running without auth") {
		t.Fatalf("misleading fail-open log message remains: %s", logged)
	}
}

func TestBuildAuthRegistryMalformedSigV4FailsClosed(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	cfg := &config.Config{Auth: config.AuthConfig{SigV4Credentials: "malformed"}}

	reg := buildAuthRegistry(context.Background(), cfg, logger, nil)
	if reg == nil || !reg.Enabled() {
		t.Fatal("malformed SigV4 credentials must enable fail-closed authentication")
	}
	called := false
	handler := reg.Middleware()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/s3/bucket/key", nil))
	if rec.Code != http.StatusUnauthorized || called {
		t.Fatalf("status=%d called=%v", rec.Code, called)
	}
	if !strings.Contains(logs.String(), "authentication locked down") {
		t.Fatalf("missing fail-closed log: %s", logs.String())
	}
}
