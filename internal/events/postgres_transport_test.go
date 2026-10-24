package events

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
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

func TestPostgresTransportPublishAdmission(t *testing.T) {
	event := repository.Event{
		TenantID: "t1", Bucket: "b1", Key: "k", Type: repository.EventCreated,
	}
	t.Run("missing boundary", func(t *testing.T) {
		err := NewPostgresTransport("not-a-dsn", "").Publish(context.Background(), event)
		if !IsResourceBoundaryFault(err) {
			t.Fatalf("missing boundary error = %v, want boundary fault before connect", err)
		}
	})
	t.Run("bound", func(t *testing.T) {
		tr := NewPostgresTransport("not-a-dsn", "").
			WithResourceBoundary(busTestBoundary(spaces.BindingBound, nil))
		err := tr.Publish(context.Background(), event)
		if !errors.Is(err, ErrSpaceBound) || !IsResourceBoundaryDenied(err) {
			t.Fatalf("bound error = %v, want explicit boundary denial", err)
		}
	})
	t.Run("fault", func(t *testing.T) {
		tr := NewPostgresTransport("not-a-dsn", "").
			WithResourceBoundary(busTestBoundary(spaces.BindingUnbound, errors.New("probe fault")))
		err := tr.Publish(context.Background(), event)
		if !IsResourceBoundaryFault(err) || IsResourceBoundaryDenied(err) {
			t.Fatalf("probe fault = %v, want boundary fault before connect", err)
		}
	})
	t.Run("canceled before admission", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		tr := NewPostgresTransport("not-a-dsn", "").
			WithResourceBoundary(busTestBoundary(spaces.BindingUnbound, nil))
		err := tr.Publish(ctx, event)
		if !IsResourceBoundaryFault(err) || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled error = %v, want boundary fault preserving cancellation", err)
		}
	})
}

func TestPostgresTransportPublishAdmissionBeforeEgress(t *testing.T) {
	event := repository.Event{
		TenantID: "t1", Bucket: "b1", Key: "k", Type: repository.EventCreated,
	}
	t.Run("admission precedes encoding", func(t *testing.T) {
		bad := event
		bad.CreatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
		err := NewPostgresTransport("not-a-dsn", "").Publish(context.Background(), bad)
		if !IsResourceBoundaryFault(err) {
			t.Fatalf("unencodable event error = %v, want boundary refusal before encode", err)
		}
	})
	t.Run("unbound reaches connect", func(t *testing.T) {
		tr := NewPostgresTransport("not-a-dsn", "").
			WithResourceBoundary(busTestBoundary(spaces.BindingUnbound, nil))
		err := tr.Publish(context.Background(), event)
		if err == nil || IsResourceBoundaryFault(err) || IsResourceBoundaryDenied(err) {
			t.Fatalf("unbound error = %v, want a post-admission connect error", err)
		}
	})
}

const busTestSpaceID = "12345678-1234-1234-1234-123456789abc"

type busTestBindingFunc func(context.Context, string, string) (spaces.BindingResult, error)

func (f busTestBindingFunc) ProbeSpaceBinding(ctx context.Context, tenant, bucket string) (spaces.BindingResult, error) {
	return f(ctx, tenant, bucket)
}

func busTestBoundary(state spaces.BindingState, cause error) *ResourceBoundary {
	return NewResourceBoundary(busTestBindingFunc(func(_ context.Context, tenant, bucket string) (spaces.BindingResult, error) {
		if cause != nil {
			return spaces.BindingResult{}, cause
		}
		result := spaces.BindingResult{TenantID: tenant, Bucket: bucket, TenantKnown: true,
			TenantStatus: "active", State: state}
		if state == spaces.BindingBound {
			result.SpaceID, result.Revision = busTestSpaceID, 1
		}
		return result, nil
	}))
}

type busTestAuthorityRepo struct {
	*fakeRepo
	rows       []repository.Event
	err        error
	listCalls  int
	lastAfter  int64
	lastTenant string
}

func (r *busTestAuthorityRepo) ListEventsAfter(_ context.Context, tenant string, afterID int64, _ int) ([]repository.Event, error) {
	r.listCalls++
	r.lastTenant, r.lastAfter = tenant, afterID
	return r.rows, r.err
}

func busTestDeliverEvent() repository.Event {
	objID := int64(7)
	return repository.Event{
		ID: 1, TenantID: "t1", Bucket: "b1", Key: "k", Type: repository.EventCreated,
		ObjectID: &objID, RequestID: "input", Payload: map[string]string{"v": "1"},
	}
}

// busTestWrappedRepo is a durable-wrapper stand-in that also exposes the raw
// binding port. Local publish must not read either channel.
type busTestWrappedRepo struct {
	*fakeRepo
	listCalls  int
	probeCalls int
}

func (r *busTestWrappedRepo) ListEventsAfter(context.Context, string, int64, int) ([]repository.Event, error) {
	r.listCalls++
	return nil, nil
}

func (r *busTestWrappedRepo) ProbeSpaceBinding(_ context.Context, tenant, bucket string) (spaces.BindingResult, error) {
	r.probeCalls++
	return spaces.BindingResult{TenantID: tenant, Bucket: bucket, State: spaces.BindingUnbound}, nil
}
