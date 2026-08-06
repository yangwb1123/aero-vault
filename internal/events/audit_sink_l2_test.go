package events

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Security-baseline tests for the L2 adapter (H1/H5/H6) plus the payload cap
// (F9 defense in depth). Delivery-semantics tests live in
// event_outbox_relay_test.go (AC-2) and config-level tests in
// internal/config/config_audit_sink_l2_test.go (H1/H4).

const redactionToken = "super-secret-token-0123456789"

func l2SinkForTest(t *testing.T, endpoint string) *AuditSinkL2 {
	t.Helper()
	sink, err := NewAuditSinkL2(endpoint, map[string]string{"default": redactionToken},
		&http.Client{Timeout: 5 * time.Second}, nil)
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	return sink
}

// ── H5: errors must never leak token, payload, or response bytes ────────────

func TestAuditSinkL2_ErrorsRedactTokenAndPayload(t *testing.T) {
	payload := []byte(`{"schema_version":"1.1","key":"docs/secret.txt","actor":"alice"}`)
	bodyEcho := "response-body-must-never-leak-0123456789"

	scenarios := []struct {
		name    string
		handler http.HandlerFunc
		wantErr error // nil → any non-sentinel error allowed
	}{
		{
			name: "500 status",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(bodyEcho))
			},
		},
		{
			name: "401 rejected",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(bodyEcho))
			},
			wantErr: ErrSinkUnauthorized,
		},
		{
			name: "echo missing",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK) // 2xx but no X-Audit-Fact-Id echo
				_, _ = w.Write([]byte(bodyEcho))
			},
		},
	}
	for _, tc := range scenarios {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			sink := l2SinkForTest(t, srv.URL)

			err := sink.DeliverDeleted(context.Background(), "default", 7, payload)
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if err == nil {
				t.Fatal("expected an error")
			}
			message := err.Error()
			for _, forbidden := range []string{redactionToken, "super-secret", string(payload),
				"docs/secret.txt", bodyEcho, srv.URL} {
				if strings.Contains(message, forbidden) {
					t.Errorf("error leaks %q: %s", forbidden, message)
				}
			}
		})
	}

	t.Run("transport error collapsed to generic class", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		url := srv.URL
		srv.Close() // closed listener → dial error
		sink := l2SinkForTest(t, url)
		err := sink.DeliverDeleted(context.Background(), "default", 7, payload)
		if err == nil {
			t.Fatal("expected transport error")
		}
		if strings.Contains(err.Error(), url) || strings.Contains(err.Error(), "127.0.0.1") {
			t.Errorf("transport error leaks endpoint: %s", err.Error())
		}
	})

	t.Run("payload size cap rejected without POST (F9 defense in depth)", func(t *testing.T) {
		var hits int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits++
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		sink := l2SinkForTest(t, srv.URL)
		huge := bytes.Repeat([]byte("x"), auditSinkL2MaxPayloadBytes+1)
		if err := sink.DeliverDeleted(context.Background(), "default", 7, huge); err == nil {
			t.Fatal("expected payload size error")
		}
		if hits != 0 {
			t.Fatalf("oversized payload was POSTed (%d requests)", hits)
		}
	})

	t.Run("unbound tenant returns ErrSinkNotBound without POST", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		sink := l2SinkForTest(t, srv.URL)
		if err := sink.DeliverDeleted(context.Background(), "no-such-tenant", 7, payload); !errors.Is(err, ErrSinkNotBound) {
			t.Fatalf("err = %v, want ErrSinkNotBound", err)
		}
	})
}

// ── H6: redirects are never followed (payload + Authorization must not move) ─

func TestAuditSinkL2_RejectsRedirect(t *testing.T) {
	for _, code := range []int{http.StatusMovedPermanently, http.StatusTemporaryRedirect, http.StatusFound} {
		t.Run("HTTP "+http.StatusText(code), func(t *testing.T) {
			var mu sync.Mutex
			var movedHits, targetHits int
			mux := http.NewServeMux()
			mux.HandleFunc("/moved", func(w http.ResponseWriter, _ *http.Request) {
				mu.Lock()
				movedHits++
				mu.Unlock()
				w.WriteHeader(http.StatusOK)
			})
			mux.HandleFunc("/target", func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				targetHits++
				mu.Unlock()
				http.Redirect(w, r, "/moved", code)
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			sink := l2SinkForTest(t, srv.URL+"/target")

			err := sink.DeliverDeleted(context.Background(), "default", 7, []byte(`{"schema_version":"1.1"}`))
			if err == nil {
				t.Fatal("redirect target accepted as delivery success")
			}
			mu.Lock()
			defer mu.Unlock()
			if movedHits != 0 {
				t.Fatalf("redirect was followed: %d hits on /moved (payload/Authorization forwarded)", movedHits)
			}
			if targetHits != 1 {
				t.Fatalf("target hits = %d, want 1", targetHits)
			}
		})
	}
}

// ── H1: constructor re-validates the endpoint (defense in depth) ────────────

func TestAuditSinkL2_EndpointRejectedAtConstruction(t *testing.T) {
	for _, endpoint := range []string{
		"http://169.254.169.254/latest/meta-data", // metadata target, non-loopback HTTP
		"http://10.0.0.1/audit",
		"https://user:pass@example.com/audit", // userinfo
		"https://example.com/audit?q=1",       // query
		"https://example.com/audit#frag",      // fragment
		"ftp://example.com/audit",
		"not-a-url",
		"",
	} {
		if _, err := NewAuditSinkL2(endpoint, nil, nil, nil); err == nil {
			t.Errorf("endpoint %q accepted", endpoint)
		}
	}
	for _, endpoint := range []string{
		"https://audit.example.com/ingest",
		"http://localhost:8080/ingest",
		"http://127.0.0.1:8080/ingest",
		"http://[::1]:8080/ingest",
	} {
		if _, err := NewAuditSinkL2(endpoint, nil, nil, nil); err != nil {
			t.Errorf("endpoint %q rejected: %v", endpoint, err)
		}
	}
}
