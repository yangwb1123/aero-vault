package events

import (
	"reflect"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestNewPostgresTransportDefaultChannel(t *testing.T) {
	tr := NewPostgresTransport("", "")
	if tr == nil {
		t.Fatal("NewPostgresTransport returned nil")
	}
	if got := tr.Channel(); got != DefaultChannel {
		t.Fatalf("default channel = %q, want %q", got, DefaultChannel)
	}
}

func TestNewPostgresTransportExplicitChannel(t *testing.T) {
	tr := NewPostgresTransport("postgres://ignored", "custom_chan")
	if got := tr.Channel(); got != "custom_chan" {
		t.Fatalf("channel = %q, want %q", got, "custom_chan")
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	objID := int64(42)
	want := repository.Event{
		ID:        7,
		TenantID:  "tenant-1",
		Bucket:    "docs",
		Key:       "reports/q1.pdf",
		Type:      repository.EventCreated,
		ObjectID:  &objID,
		RequestID: "req-abc",
		Payload:   map[string]string{"size": "1024", "ct": "application/pdf"},
		// Use a fixed UTC instant so JSON RFC3339 round-trips exactly.
		CreatedAt: time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC),
	}

	b, err := encodeEvent(want)
	if err != nil {
		t.Fatalf("encodeEvent: %v", err)
	}

	got, err := decodeEvent(b)
	if err != nil {
		t.Fatalf("decodeEvent: %v", err)
	}

	if got.ObjectID == nil || *got.ObjectID != *want.ObjectID {
		t.Fatalf("ObjectID = %v, want %v", got.ObjectID, want.ObjectID)
	}
	// Compare the rest by value; nil out pointers already checked so reflect
	// does not compare differing pointer addresses.
	got.ObjectID, want.ObjectID = nil, nil
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch:\n got  %+v\n want %+v", got, want)
	}
}

func TestDecodeEventEmpty(t *testing.T) {
	if _, err := decodeEvent(nil); err == nil {
		t.Fatal("decodeEvent(nil) expected error, got nil")
	}
}
