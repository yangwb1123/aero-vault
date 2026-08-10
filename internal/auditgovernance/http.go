package auditgovernance

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/repository"
)

type publisherClient struct {
	tokens   *tokenSource
	sourceID string
}

type Publisher struct {
	endpoint *url.URL
	client   *http.Client
	bindings map[string]publisherClient
}

func newPublisher(
	cfg config.AuditGovernanceConfig, client *http.Client,
) (*Publisher, error) {
	base, err := secureEndpoint(cfg.BaseURL)
	if err != nil || client == nil {
		return nil, ErrInvalidConfig
	}
	endpoint := base.JoinPath(governancePath)
	query := endpoint.Query()
	query.Set("wait_for", "ledgered")
	endpoint.RawQuery = query.Encode()
	redactor, err := newRedactor(cfg.HMACKey)
	if err != nil {
		return nil, err
	}
	bindings := make(map[string]publisherClient, len(cfg.Bindings))
	for _, binding := range cfg.Bindings {
		tokens, tokenErr := newTokenSource(cfg.TokenURL, binding.ClientID, binding.ClientSecret, client)
		if tokenErr != nil {
			return nil, tokenErr
		}
		sourceID, sourceErr := redactor.tenantSourceID(binding.TenantID)
		if sourceErr != nil {
			return nil, sourceErr
		}
		bindings[binding.TenantID] = publisherClient{tokens: tokens, sourceID: sourceID}
	}
	return &Publisher{endpoint: endpoint, client: client, bindings: bindings}, nil
}

func secureEndpoint(raw string) (*url.URL, error) {
	endpoint, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !validEndpointShape(endpoint) {
		return nil, ErrInvalidConfig
	}
	if endpoint.Scheme == "https" {
		return endpoint, nil
	}
	if endpoint.Scheme != "http" || !loopbackHost(endpoint.Hostname()) {
		return nil, ErrInvalidConfig
	}
	return endpoint, nil
}

func validEndpointShape(endpoint *url.URL) bool {
	return endpoint != nil && endpoint.Host != "" && endpoint.User == nil &&
		endpoint.RawQuery == "" && endpoint.Fragment == ""
}

func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func noRedirectClient(timeout time.Duration) (*http.Client, *http.Transport) {
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true}
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = base.Clone()
	}
	client := &http.Client{Transport: transport, Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return client, transport
}

func (p *Publisher) Publish(
	ctx context.Context, fact repository.AuditGovernanceFact,
) error {
	if !validOutboundFact(fact) {
		return ErrInvalidEvent
	}
	binding, ok := p.bindings[fact.TenantID]
	if !ok {
		return ErrInvalidEvent
	}
	token, err := binding.tokens.AccessToken(ctx)
	if err != nil {
		return err
	}
	body, err := json.Marshal(governanceWire(fact, binding.sourceID))
	if err != nil {
		return ErrInvalidEvent
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return ErrInvalidConfig
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := p.client.Do(request)
	if err != nil {
		return err
	}
	if response.StatusCode == http.StatusUnauthorized {
		binding.tokens.Invalidate(token)
	}
	return validateReceipt(response, fact)
}

func validOutboundFact(fact repository.AuditGovernanceFact) bool {
	if fact.ID == "" || fact.TenantID == "" || fact.Action == "" || fact.OccurredAt.IsZero() {
		return false
	}
	if fact.FactKind != "admin" && fact.FactKind != "security" && fact.FactKind != "file" {
		return false
	}
	return fact.ObjectSizeBytes >= 0 && fact.Action == safeAction(fact.Action, "")
}

func governanceWire(fact repository.AuditGovernanceFact, source string) governanceEvent {
	actor := fact.ActorDigest
	if actor == "" {
		actor = SourcePrefix
	}
	event := governanceEvent{
		EventID: fact.ID, SourceSystem: source, EventType: SchemaID, SchemaID: SchemaID,
		SchemaVersion: SchemaVersion, OccurredAt: fact.OccurredAt.UTC(), OperationID: fact.RequestID,
		Actor: governanceActor{ID: actor, Type: "principal"}, AggregateType: fact.FactKind,
		AggregateID: fact.TargetDigest, Action: fact.Action, Outcome: "success",
		Payload: governancePayload(fact), DataClassification: Classification,
		RetentionClass: RetentionClass, IdempotencyKey: fact.ID,
	}
	if fact.TargetDigest != "" {
		event.Targets = []governanceTarget{{Type: fact.FactKind, ID: fact.TargetDigest}}
	}
	return event
}

func governancePayload(fact repository.AuditGovernanceFact) map[string]any {
	payload := map[string]any{"fact_kind": fact.FactKind}
	if fact.RequestID != "" {
		payload["request_id"] = fact.RequestID
	}
	if fact.DetailSHA256 != "" {
		payload["detail_sha256"] = fact.DetailSHA256
	}
	if fact.ObjectSizeBytes > 0 {
		payload["object_size_bytes"] = fact.ObjectSizeBytes
	}
	if fact.StorageBackend != "" {
		payload["storage_backend"] = fact.StorageBackend
	}
	return payload
}

func validateReceipt(response *http.Response, fact repository.AuditGovernanceFact) error {
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		// 调试：403/4xx 时保留响应体供人工诊断（生产无敏感信息）
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
		return &httpStatusError{Status: response.StatusCode, Detail: string(body)}
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return ErrInvalidReceipt
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return ErrInvalidReceipt
	}
	var envelope receiptEnvelope
	if json.Unmarshal(body, &envelope) != nil || !receiptMatches(envelope, fact) {
		return ErrInvalidReceipt
	}
	return nil
}

func receiptMatches(envelope receiptEnvelope, fact repository.AuditGovernanceFact) bool {
	receipt := envelope.Receipt
	if receipt.EventID != fact.ID || receipt.TenantID != fact.TenantID ||
		receipt.AcceptedAt.IsZero() || receipt.Conflict {
		return false
	}
	return receipt.Status == "ledgered" || receipt.Status == "indexed" || receipt.Status == "archived"
}
