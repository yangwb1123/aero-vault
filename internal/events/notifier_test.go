package events

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestRuleMatches(t *testing.T) {
	tests := []struct {
		name      string
		rule      repository.NotificationRule
		eventName string
		key       string
		want      bool
	}{
		{
			name: "exact match",
			rule: repository.NotificationRule{
				Events: []string{"s3:ObjectCreated:Put"},
			},
			eventName: "s3:ObjectCreated:Put",
			key:       "file.txt",
			want:      true,
		},
		{
			name: "wildcard match",
			rule: repository.NotificationRule{
				Events: []string{"s3:ObjectCreated:*"},
			},
			eventName: "s3:ObjectCreated:Put",
			key:       "file.txt",
			want:      true,
		},
		{
			name: "no match different event",
			rule: repository.NotificationRule{
				Events: []string{"s3:ObjectCreated:Put"},
			},
			eventName: "s3:ObjectRemoved:Delete",
			key:       "file.txt",
			want:      false,
		},
		{
			name: "filter key prefix match",
			rule: repository.NotificationRule{
				Events:    []string{"s3:ObjectCreated:*"},
				FilterKey: "images/",
			},
			eventName: "s3:ObjectCreated:Put",
			key:       "images/photo.jpg",
			want:      true,
		},
		{
			name: "filter key prefix no match",
			rule: repository.NotificationRule{
				Events:    []string{"s3:ObjectCreated:*"},
				FilterKey: "images/",
			},
			eventName: "s3:ObjectCreated:Put",
			key:       "docs/file.txt",
			want:      false,
		},
		{
			name: "empty events",
			rule: repository.NotificationRule{
				Events: []string{},
			},
			eventName: "s3:ObjectCreated:Put",
			key:       "file.txt",
			want:      false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ruleMatches(tt.rule, tt.eventName, tt.key)
			if got != tt.want {
				t.Errorf("ruleMatches(%v, %q, %q) = %v, want %v", tt.rule, tt.eventName, tt.key, got, tt.want)
			}
		})
	}
}

func TestS3EventName(t *testing.T) {
	if got := s3EventName(repository.EventCreated); got != "s3:ObjectCreated:Put" {
		t.Errorf("EventCreated -> %q", got)
	}
	if got := s3EventName(repository.EventDeleted); got != "s3:ObjectRemoved:Delete" {
		t.Errorf("EventDeleted -> %q", got)
	}
	if got := s3EventName("unknown"); got != "" {
		t.Errorf("unknown -> %q", got)
	}
}

func TestBuildS3Event(t *testing.T) {
	e := repository.Event{ID: 42, TenantID: "t1", Bucket: "b1", Key: "k1", Type: repository.EventCreated}
	ev := buildS3Event(e, "s3:ObjectCreated:Put")
	if len(ev.Records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(ev.Records))
	}
	r := ev.Records[0]
	if r.EventName != "s3:ObjectCreated:Put" {
		t.Errorf("event name = %q", r.EventName)
	}
	if r.S3.Bucket.Name != "b1" {
		t.Errorf("bucket = %q", r.S3.Bucket.Name)
	}
	if r.S3.Object.Key != "k1" {
		t.Errorf("key = %q", r.S3.Object.Key)
	}
}

func TestArnToHTTP(t *testing.T) {
	arn := "arn:aws:sns:us-east-1:123456789012:MyTopic"
	url := arnToHTTP(arn, "sns")
	if url == "" || url == arn {
		t.Errorf("sns conversion failed: %q", url)
	}
	if !contains(url, "MyTopic") {
		t.Errorf("expected MyTopic in url: %s", url)
	}
}

func TestNotifierDelivery(t *testing.T) {
	// Start a test HTTP server to receive the notification.
	received := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create repo with notification rules.
	dbPath := t.TempDir() + "/test.db"
	repo, err := repository.Open(ctx, "sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer repo.Close()
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Set up notification rule with EndpointURL.
	err = repo.SetBucketNotifications(ctx, "default", "default", []repository.NotificationRule{{
		ID:          "test-rule",
		Events:      []string{"s3:ObjectCreated:*"},
		EndpointURL: srv.URL,
	}})
	if err != nil {
		t.Fatalf("set notifications: %v", err)
	}

	n := NewNotifier(repo, nil)
	sub := make(chan repository.Event, 1)
	go n.Run(ctx, sub)

	// Send an event.
	sub <- repository.Event{
		ID:       1,
		TenantID: "default",
		Bucket:   "default",
		Key:      "test-file.txt",
		Type:     repository.EventCreated,
	}

	select {
	case <-received:
		// success
	case <-ctx.Done():
		t.Fatal("timeout waiting for notification delivery")
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
