package auditgovernance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// TestTenantSourceIDRejectsControlChars pins the P-1 hardening: the fact-ID
// frame is NUL-separated, so any C0 control or DEL in the tenant field would
// corrupt its injectivity (tenant is the only unconstrained frame input).
func TestTenantSourceIDRejectsControlChars(t *testing.T) {
	redactor, err := newRedactor("audit-governance-hmac-key-32-bytes-minimum")
	if err != nil {
		t.Fatal(err)
	}
	for _, tenant := range []string{"acme\x00evil", "acme\x01", "acme\tx", "acme\x7f"} {
		if _, err := redactor.tenantSourceID(tenant); err == nil {
			t.Fatalf("tenantSourceID(%q) accepted a control char", tenant)
		}
	}
	for _, tenant := range []string{"acme", "acme space", "default", "tenant.with._-chars"} {
		if _, err := redactor.tenantSourceID(tenant); err != nil {
			t.Fatalf("tenantSourceID(%q) rejected a valid tenant: %v", tenant, err)
		}
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
			_, _ = w.Write([]byte(`{"access_token":"machine-token","token_type":"Bearer","expires_in":300,"scope":"` + RequiredScope + `"}`))
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
	wantTarget := strings.Replace(redactor.digest("acme", "target", "private/path"),
		"hmac-sha256:", "hmac-sha256.", 1)
	if body["aggregate_id"] != wantTarget || strings.Contains(wantTarget, ":") {
		t.Fatalf("aggregate_id=%v want separator-safe %s", body["aggregate_id"], wantTarget)
	}
	targets, ok := body["targets"].([]any)
	if !ok || len(targets) != 1 || targets[0].(map[string]any)["id"] != wantTarget {
		t.Fatalf("targets=%#v want canonical target %s", body["targets"], wantTarget)
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
		_, _ = w.Write([]byte(`{"access_token":"token","token_type":"Bearer","expires_in":60,"scope":"` + RequiredScope + ` admin"}`))
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

// ── Contract A: audit-governance duplicate re-POST semantics ─────────────────
//
// The governance receiver answers an idempotent re-POST (lease re-claim,
// crash re-delivery, at-least-once) with
//   {duplicate:true, conflict:false, status:ledgered}
// and the relay must accept it exactly like a first POST. The receipt model's
// Duplicate field (model.go) is deliberately unused in the acceptance
// predicate — this test pins that contract: toggling Duplicate must never
// change acceptance, and conflict:true must surface as the distinct terminal
// sentinel ErrReceiptConflict (terminal-with-retention, never bounded-backoff
// retried).

func TestReceiptDuplicateSemanticsContract(t *testing.T) {
	base := receiptEnvelope{}
	base.Receipt.EventID, base.Receipt.TenantID = "fact-1", "acme"
	base.Receipt.Status, base.Receipt.AcceptedAt = "ledgered", time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	fact := repository.AuditGovernanceFact{ID: "fact-1", TenantID: "acme", FactKind: "file",
		Action: "file.deleted", OccurredAt: time.Now().UTC()}

	// ① Duplicate must be inert in the predicate: duplicate:false (first POST)
	// and duplicate:true (idempotent re-POST) both match, for every accepted
	// status. The field is contract-documentation only.
	for _, status := range []string{"ledgered", "indexed", "archived"} {
		first, dup := base, base
		first.Receipt.Status, dup.Receipt.Status = status, status
		dup.Receipt.Duplicate = true
		if !receiptMatches(first, fact) || !receiptMatches(dup, fact) {
			t.Fatalf("status=%q: duplicate toggle changed acceptance", status)
		}
	}
	// Mismatched identity must still be rejected — the predicate is not
	// trivially true; only Duplicate is exempt.
	rejected := base
	rejected.Receipt.EventID = "other-fact"
	if receiptMatches(rejected, fact) {
		t.Fatal("mismatched event_id accepted")
	}

	// ② End-to-end: a re-POST answered {duplicate:true, conflict:false,
	// status:ledgered} completes like a first POST.
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"token","token_type":"Bearer","expires_in":60}`))
			return
		}
		posts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"receipt":{"event_id":"fact-1","tenant_id":"acme","status":"ledgered","accepted_at":"2026-08-04T00:00:00Z","duplicate":true,"conflict":false}}`))
	}))
	defer server.Close()
	client, transport := noRedirectClient(time.Second)
	defer transport.CloseIdleConnections()
	publisher, err := newPublisher(publisherConfig(server.URL), client)
	if err != nil {
		t.Fatal(err)
	}
	if err := publisher.Publish(context.Background(), fact); err != nil {
		t.Fatalf("duplicate re-POST rejected: %v (contract A violated)", err)
	}
	if posts.Load() != 1 {
		t.Fatalf("posts=%d want=1", posts.Load())
	}
}

// ③ conflict:true is terminal: a distinct sentinel (not ErrInvalidReceipt),
// so the relay can fail the fact with retention instead of bounded-backoff
// retrying it forever.
func TestReceiptConflictIsTerminalSentinel(t *testing.T) {
	fact := repository.AuditGovernanceFact{ID: "fact-1", TenantID: "acme", FactKind: "admin",
		Action: "tenant.status", OccurredAt: time.Now().UTC()}
	for _, conflict := range []bool{true, false} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/token" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"token","token_type":"Bearer","expires_in":60}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = fmt.Fprintf(w, `{"receipt":{"event_id":"fact-1","tenant_id":"acme","status":"ledgered","accepted_at":"2026-08-04T00:00:00Z","conflict":%v}}`, conflict)
		}))
		client, transport := noRedirectClient(time.Second)
		publisher, err := newPublisher(publisherConfig(server.URL), client)
		if err != nil {
			server.Close()
			transport.CloseIdleConnections()
			t.Fatal(err)
		}
		err = publisher.Publish(context.Background(), fact)
		server.Close()
		transport.CloseIdleConnections()
		if conflict {
			if !errors.Is(err, ErrReceiptConflict) {
				t.Fatalf("conflict:true err=%v want ErrReceiptConflict", err)
			}
			if errors.Is(err, ErrInvalidReceipt) {
				t.Fatal("conflict classified as plain invalid receipt, not terminal")
			}
		} else if err != nil {
			t.Fatalf("conflict:false err=%v want nil", err)
		}
	}
}
