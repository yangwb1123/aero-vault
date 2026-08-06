package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/telemetry"
)

// Notifier delivers bucket notification events (S3-style) to configured
// SNS/SQS/Lambda/HTTP endpoints. It subscribes to the EventBus and for each
// event checks the source bucket's notification rules; matching rules trigger
// an HTTP POST with the standard S3 notification event format.
type Notifier struct {
	repo   repository.Repository
	client *http.Client
	logger *slog.Logger
}

// NewNotifier creates a notifier that delivers bucket notification events.
func NewNotifier(repo repository.Repository, logger *slog.Logger) *Notifier {
	if logger == nil {
		logger = slog.Default()
	}
	return &Notifier{
		repo:   repo,
		client: &http.Client{Timeout: 10 * time.Second},
		logger: logger,
	}
}

// Run subscribes to events and delivers notifications. Blocks until ctx is done.
func (n *Notifier) Run(ctx context.Context, sub <-chan repository.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-sub:
			if !ok {
				return
			}
			n.deliver(ctx, e)
		}
	}
}

// deliver matches one event against the bucket's notification rules and posts
// to every matching target.
func (n *Notifier) deliver(ctx context.Context, e repository.Event) {
	rules, err := n.repo.GetBucketNotifications(ctx, e.TenantID, e.Bucket)
	if err != nil || len(rules) == 0 {
		return // no rules configured
	}

	eventName := s3EventName(e.Type)
	if eventName == "" {
		return // not an S3-style event
	}

	// D2: deletes that committed through the transactional outbox are delivered
	// by the relay from the self-contained notify@1.1 payload. Skip the bus path
	// only when the outbox row exists (any status — the WithEvent transaction
	// commits before s.emit, so the row is visible regardless of relay progress;
	// no race). E14 paths (DeleteVersion / delete-marker / quarantine) have no
	// outbox row and keep the bus path — never silently drop them.
	if e.Type == repository.EventDeleted && e.ObjectID != nil {
		has, err := n.repo.HasEventOutboxFact(ctx, *e.ObjectID, repository.EventTypeFileNotify11)
		if err != nil {
			n.logger.Warn("notification outbox check failed; falling back to bus delivery",
				"key", e.Key, "err", err)
		} else if has {
			return
		}
	}

	for _, rule := range rules {
		if !ruleMatches(rule, eventName, e.Key) {
			continue
		}
		n.dispatchToTargets(ctx, e, rule, eventName)
	}
}

// dispatchToTargets sends the notification to all configured targets in the rule.
func (n *Notifier) dispatchToTargets(ctx context.Context, e repository.Event, rule repository.NotificationRule, eventName string) {
	payload := buildS3Event(e, eventName)
	body, _ := json.Marshal(payload)

	targets := resolveTargets(rule)
	for _, target := range targets {
		if err := n.postEvent(ctx, target, body); err != nil {
			n.logger.Warn("notification delivery failed",
				"target", target, "event", eventName, "key", e.Key, "err", err)
			telemetry.IncNotificationDeliveryFailed(ctx, target)
		} else {
			telemetry.IncNotificationDelivered(ctx, target)
			n.logger.Debug("notification delivered",
				"target", target, "event", eventName, "key", e.Key)
		}
	}
}

// resolveTargets returns the HTTP URL(s) to deliver to for this rule. Shared
// by the bus Notifier and the outbox relay.
func resolveTargets(rule repository.NotificationRule) []string {
	var targets []string
	if rule.EndpointURL != "" {
		targets = append(targets, rule.EndpointURL)
	}
	if rule.TopicARN != "" {
		targets = append(targets, arnToHTTP(rule.TopicARN, "sns"))
	}
	if rule.QueueARN != "" {
		targets = append(targets, arnToHTTP(rule.QueueARN, "sqs"))
	}
	if rule.LambdaARN != "" {
		targets = append(targets, arnToHTTP(rule.LambdaARN, "lambda"))
	}
	return targets
}

// postEvent sends an HTTP POST with the S3 notification payload.
func (n *Notifier) postEvent(ctx context.Context, url string, body []byte) error {
	return postEventTo(ctx, n.client, url, body)
}

// postEventTo POSTs body to url with the standard notification headers. Shared
// by the bus Notifier and the outbox relay (which posts the stored payload
// byte-exact).
func postEventTo(ctx context.Context, client *http.Client, url string, body []byte) error {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "aero-vault/notifier")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}

// s3EventName converts an internal EventType to an S3 event name string.
func s3EventName(et repository.EventType) string {
	switch et {
	case repository.EventCreated:
		return "s3:ObjectCreated:Put"
	case repository.EventDeleted:
		return "s3:ObjectRemoved:Delete"
	default:
		return ""
	}
}

// ruleMatches checks whether a notification rule should fire for the given event.
func ruleMatches(rule repository.NotificationRule, eventName, key string) bool {
	if len(rule.Events) == 0 {
		return false
	}
	matched := false
	for _, re := range rule.Events {
		if matchEvent(re, eventName) {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	// Apply prefix/suffix filter if configured.
	if rule.FilterKey != "" {
		if !strings.HasPrefix(key, rule.FilterKey) {
			return false
		}
	}
	return true
}

// matchEvent checks if a rule event pattern matches an S3 event name.
// Supports wildcard suffix like "s3:ObjectCreated:*".
func matchEvent(pattern, eventName string) bool {
	if pattern == eventName {
		return true
	}
	if strings.HasSuffix(pattern, ":*") {
		prefix := strings.TrimSuffix(pattern, ":*")
		return strings.HasPrefix(eventName, prefix+":")
	}
	return false
}

// arnToHTTP converts an AWS ARN to a best-effort HTTP URL for local/self-hosted
// delivery. For production AWS integration, use the AWS SDK instead.
func arnToHTTP(arn, service string) string {
	// arn:partition:service:region:account:resource
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 {
		return arn // fallback: use ARN as-is
	}
	region := parts[3]
	account := parts[4]
	resource := parts[5]
	// Build an SNS/SQS/Lambda HTTP endpoint URL (works with localstack).
	switch service {
	case "sns":
		return fmt.Sprintf("http://sns.%s.amazonaws.com/?Action=Publish&TopicArn=%s", region, arn)
	case "sqs":
		queueName := resource
		if idx := strings.LastIndex(resource, ":"); idx >= 0 {
			queueName = resource[idx+1:]
		}
		return fmt.Sprintf("http://sqs.%s.amazonaws.com/%s/%s", region, account, queueName)
	case "lambda":
		return fmt.Sprintf("http://lambda.%s.amazonaws.com/2015-03-31/functions/%s/invocations", region, resource)
	default:
		return arn
	}
}

// buildS3Event constructs the standard S3 event notification payload.
type s3NotificationEvent struct {
	Records []s3EventRecord `json:"Records"`
}

type s3EventRecord struct {
	EventVersion      string            `json:"eventVersion"`
	EventSource       string            `json:"eventSource"`
	AWSRegion         string            `json:"awsRegion"`
	EventName         string            `json:"eventName"`
	UserIdentity      s3UserIdentity    `json:"userIdentity"`
	RequestParameters map[string]string `json:"requestParameters"`
	ResponseElements  map[string]string `json:"responseElements"`
	S3                s3Entity          `json:"s3"`
}

type s3UserIdentity struct {
	PrincipalID string `json:"principalId"`
}

type s3Entity struct {
	SchemaVersion string   `json:"s3SchemaVersion"`
	Bucket        s3Bucket `json:"bucket"`
	Object        s3Object `json:"object"`
}

type s3Bucket struct {
	Name string `json:"name"`
	ARN  string `json:"arn"`
}

type s3Object struct {
	Key       string `json:"key"`
	Size      int64  `json:"size,omitempty"`
	ETag      string `json:"eTag,omitempty"`
	Sequencer string `json:"sequencer"`
}

func buildS3Event(e repository.Event, eventName string) s3NotificationEvent {
	return s3NotificationEvent{
		Records: []s3EventRecord{{
			EventVersion:      "2.1",
			EventSource:       "aws:s3",
			AWSRegion:         "us-east-1",
			EventName:         eventName,
			UserIdentity:      s3UserIdentity{PrincipalID: e.TenantID},
			RequestParameters: map[string]string{"sourceIPAddress": "127.0.0.1"},
			ResponseElements:  map[string]string{"x-amz-request-id": fmt.Sprintf("%d", e.ID)},
			S3: s3Entity{
				SchemaVersion: "1.0",
				Bucket: s3Bucket{
					Name: e.Bucket,
					ARN:  fmt.Sprintf("arn:aws:s3:::%s", e.Bucket),
				},
				Object: s3Object{
					Key:       e.Key,
					Sequencer: fmt.Sprintf("%020d", e.ID),
				},
			},
		}},
	}
}
