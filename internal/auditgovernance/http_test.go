package auditgovernance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/repository"
)

func publisherConfig(baseURL string) config.AuditGovernanceConfig {
	return config.AuditGovernanceConfig{Enabled: true, BaseURL: baseURL, TokenURL: baseURL + "/token",
		HMACKey: "audit-governance-hmac-key-32-bytes-minimum",
		Bindings: []config.AuditGovernanceBinding{{TenantID: "acme", ClientID: "vault-audit",
			ClientSecretEnv: "AUDIT_GOVERNANCE_CLIENT_SECRET_ACME", State: "active",
			ClientSecret: "machine-secret"}}}
}

func TestTenantSourceIDIsKeyedOpaqueAndDomainSeparated(t *testing.T) {
	redactor, err := newRedactor("audit-governance-hmac-key-32-bytes-minimum")
	if err != nil {
		t.Fatal(err)
	}
	got, err := redactor.tenantSourceID("acme")
	second, secondErr := redactor.tenantSourceID("acme")
	other, otherErr := redactor.tenantSourceID("other")
	if err != nil || secondErr != nil || otherErr != nil || got != second || got == other ||
		!strings.HasPrefix(got, SourcePrefix+".") || strings.Contains(got, "acme") {
		t.Fatalf("source=%q second=%q other=%q errors=%v/%v/%v", got, second, other,
			err, secondErr, otherErr)
	}
	if _, err := redactor.tenantSourceID(" acme"); err == nil {
		t.Fatal("ambiguous tenant was accepted")
	}
}

func TestPublisherUsesResourceBoundTokenAndRedactedFixedSchema(t *testing.T) {
	var tokenCalls atomic.Int32
	var eventCalls atomic.Int32
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokenCalls.Add(1)
			assertTokenRequest(t, r)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"machine-token","token_type":"Bearer","expires_in":300,"scope":"audit:event:write"}`))
		case "/api/v1/events":
			eventCalls.Add(1)
			if r.URL.Query().Get("wait_for") != "ledgered" || r.Header.Get("Authorization") != "Bearer machine-token" {
				t.Errorf("event request query/header invalid")
			}
			if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
				t.Errorf("decode event: %v", err)
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"receipt":{"event_id":"fact-1","tenant_id":"acme","status":"ledgered","accepted_at":"2026-08-04T00:00:00Z"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, transport := noRedirectClient(time.Second)
	defer transport.CloseIdleConnections()
	publisher, err := newPublisher(publisherConfig(server.URL), client)
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}
	redactor, _ := newRedactor(publisherConfig(server.URL).HMACKey)
	fact := repository.AuditGovernanceFact{ID: "fact-1", TenantID: "acme", FactKind: "file",
		Action: "file.created", TargetDigest: redactor.digest("acme", "target", "private/path"),
		RequestID:       redactor.digest("acme", "request", "request-1"),
		DetailSHA256:    redactor.digest("acme", "detail", "secret detail"),
		ObjectSizeBytes: 12, StorageBackend: "local", OccurredAt: time.Now().UTC()}
	if err := publisher.Publish(context.Background(), fact); err != nil {
		t.Fatalf("publish first: %v", err)
	}
	if err := publisher.Publish(context.Background(), fact); err != nil {
		t.Fatalf("publish duplicate: %v", err)
	}
	if tokenCalls.Load() != 1 || eventCalls.Load() != 2 {
		t.Fatalf("token calls=%d event calls=%d", tokenCalls.Load(), eventCalls.Load())
	}
	assertGovernanceBody(t, captured)
}

func assertTokenRequest(t *testing.T, request *http.Request) {
	t.Helper()
	clientID, secret, ok := request.BasicAuth()
	if !ok || clientID != "vault-audit" || secret != "machine-secret" {
		t.Errorf("client authentication invalid")
	}
	if err := request.ParseForm(); err != nil {
		t.Errorf("parse token form: %v", err)
	}
	if request.Form.Get("grant_type") != "client_credentials" ||
		request.Form.Get("scope") != RequiredScope || request.Form.Get("resource") != RequiredResource {
		t.Errorf("token form=%v", request.Form)
	}
}

func assertGovernanceBody(t *testing.T, body map[string]any) {
	t.Helper()
	if _, exists := body["tenant_id"]; exists {
		t.Fatal("event body selected tenant")
	}
	if body["event_type"] != SchemaID || body["schema_id"] != SchemaID ||
		body["data_classification"] != Classification || body["action"] != "file.created" {
		t.Fatalf("fixed schema fields=%v", body)
	}
	redactor, _ := newRedactor(publisherConfig("").HMACKey)
	source, _ := redactor.tenantSourceID("acme")
	if body["source_system"] != source {
		t.Fatalf("source_system=%v want=%s", body["source_system"], source)
	}
	payload, ok := body["payload"].(map[string]any)
	if !ok || len(payload) != 5 || payload["fact_kind"] != "file" {
		t.Fatalf("payload=%#v", body["payload"])
	}
	encoded, _ := json.Marshal(body)
	for _, forbidden := range []string{"private/path", "secret detail", "machine-secret", "machine-token"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("event leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestPublisherRejectsRedirectAndInvalidReceipt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"token","token_type":"Bearer","expires_in":60}`))
			return
		}
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, transport := noRedirectClient(time.Second)
	defer transport.CloseIdleConnections()
	publisher, err := newPublisher(publisherConfig(server.URL), client)
	if err != nil {
		t.Fatal(err)
	}
	fact := repository.AuditGovernanceFact{ID: "fact", TenantID: "acme", FactKind: "admin",
		Action: "quota.set", OccurredAt: time.Now().UTC()}
	if err := publisher.Publish(context.Background(), fact); err == nil {
		t.Fatal("redirect response was accepted")
	}
}

func TestTokenSourceRejectsExpandedScope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"token","token_type":"Bearer","expires_in":60,"scope":"audit:event:write admin"}`))
	}))
	defer server.Close()
	client, transport := noRedirectClient(time.Second)
	defer transport.CloseIdleConnections()
	tokens, err := newTokenSource(server.URL, "client", "secret", client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tokens.AccessToken(context.Background()); err == nil {
		t.Fatal("over-privileged token was accepted")
	}
}

func TestSecureEndpointRejectsRemoteHTTPAndCredentials(t *testing.T) {
	for _, raw := range []string{"http://example.com", "https://user:pass@example.com", "https://example.com?q=1"} {
		if _, err := secureEndpoint(raw); err == nil {
			t.Fatalf("secureEndpoint(%q) succeeded", raw)
		}
	}
	if endpoint, err := secureEndpoint("http://[::1]:8080"); err != nil || endpoint.Hostname() != "::1" {
		t.Fatalf("loopback endpoint=%v err=%v", endpoint, err)
	}
	if _, err := url.Parse("https://example.com"); err != nil {
		t.Fatal(err)
	}
}
