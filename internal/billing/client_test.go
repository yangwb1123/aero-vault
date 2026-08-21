package billing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/yangwb1123/snaplink/interfaces/ssoclient"
)

type fakeCredentialsClient struct {
	mu     sync.Mutex
	calls  int
	scopes []string
}

func (f *fakeCredentialsClient) ClientCredentials(
	_ context.Context, scopes ...string,
) (*ssoclient.TokenResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.scopes = append([]string(nil), scopes...)
	return &ssoclient.TokenResponse{AccessToken: "machine-token", ExpiresIn: 300}, nil
}

func TestClientUsesMachineTokenAndServerBoundMeteringBody(t *testing.T) {
	var usageSeen bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer machine-token" {
			t.Errorf("authorization header = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case pathEntitlement:
			_, _ = w.Write([]byte(`{"entitlement":{"tenant_id":"acme","revision":7,"active":true,"features":{"vault":true},"limits":{"storage_bytes":{"hard":100},"storage_objects":{"hard":10}},"effective_at":"2026-01-01T00:00:00Z"}}`))
		case pathUsage:
			usageSeen = true
			if r.Header.Get("Idempotency-Key") != "fact-1" {
				t.Errorf("idempotency key = %q", r.Header.Get("Idempotency-Key"))
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode usage: %v", err)
			}
			if _, exists := body["tenant_id"]; exists {
				t.Error("usage body must not carry tenant_id")
			}
			if _, exists := body["source_system"]; exists {
				t.Error("usage body must not carry source_system")
			}
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	credentials := &fakeCredentialsClient{}
	tokens := &tokenSource{client: credentials, now: time.Now}
	client := newClient(server.URL, server.Client(), tokens)
	snapshot, err := client.Entitlement(context.Background())
	if err != nil || snapshot.TenantID != "acme" || snapshot.Revision != 7 {
		t.Fatalf("entitlement=%+v err=%v", snapshot, err)
	}
	err = client.AppendUsage(context.Background(), "fact-1",
		"storage_bytes_allocated", 12, time.Now().UTC(), map[string]string{"operation": "object_write"})
	if err != nil || !usageSeen {
		t.Fatalf("append usage: seen=%v err=%v", usageSeen, err)
	}
	credentials.mu.Lock()
	defer credentials.mu.Unlock()
	if credentials.calls != 1 {
		t.Fatalf("token calls=%d, want cached single call", credentials.calls)
	}
	if !slices.Equal(credentials.scopes, []string{ScopeEntitlementRead, ScopeMeteringWrite}) {
		t.Fatalf("token scopes=%v", credentials.scopes)
	}
}

func TestClientInvalidatesRejectedToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_token"}`))
	}))
	defer server.Close()
	credentials := &fakeCredentialsClient{}
	tokens := &tokenSource{client: credentials, now: time.Now}
	client := newClient(server.URL, server.Client(), tokens)
	if _, err := client.Entitlement(context.Background()); err == nil {
		t.Fatal("expected unauthorized entitlement request to fail")
	}
	if tokens.token != "" {
		t.Fatal("rejected token remained cached")
	}
}

func TestClientUsesSnaplinkReservationCommitWireShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != pathReservations+"/reservation-1/commit" {
			t.Errorf("commit path = %q", r.URL.EscapedPath())
		}
		if r.Header.Get("Idempotency-Key") != "commit-1" {
			t.Errorf("idempotency key = %q", r.Header.Get("Idempotency-Key"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode commit: %v", err)
		}
		if body["fact_id"] != "fact-1" {
			t.Errorf("fact_id = %#v", body["fact_id"])
		}
		if _, exists := body["id"]; exists {
			t.Error("commit body used unsupported id field")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	credentials := &fakeCredentialsClient{}
	client := newClient(server.URL, server.Client(),
		&tokenSource{client: credentials, now: time.Now})
	if err := client.CommitReservation(context.Background(), "reservation-1", "fact-1",
		"commit-1", map[string]string{"operation": "object_write"}); err != nil {
		t.Fatalf("commit reservation: %v", err)
	}
}
