package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

var (
	// ErrEventAuthorityUnavailable reports that construction was given no usable
	// persistence adapter. It is a configuration fault, never an empty fact.
	ErrEventAuthorityUnavailable = errors.New("event authority unavailable")
	// ErrEventAuthorityFault classifies an observation or consistency fault.
	// The public text is stable; callers inspect causes with errors.Is/As.
	ErrEventAuthorityFault = errors.New("event authority fault")
)

// EventAuthority rereads one persisted lifecycle event by its exact ID. It is
// a narrow consumer of the existing Repository.ListEventsAfter method, not a
// new Repository/Store capability and not an object-liveness oracle.
type EventAuthority struct {
	repo repository.Repository
}

// NewEventAuthority captures a persistence adapter. A nil or typed-nil adapter
// is a configuration fault; it must not be treated as "no event exists".
func NewEventAuthority(repo repository.Repository) (*EventAuthority, error) {
	if eventAuthorityMissing(repo) {
		return nil, ErrEventAuthorityUnavailable
	}
	return &EventAuthority{repo: repo}, nil
}

// Reread loads the tenant's first persisted row after want.ID-1 and requires
// that row to have exactly want's ID, type, tenant, bucket, key, ObjectID and
// payload. nil/empty payload maps are semantically equal; RequestID and
// CreatedAt are not identity fields. The returned row is the persisted fact,
// never a repaired copy of the caller's value. Gaps, missing rows, decode and
// persistence faults are all errors, and no object row is consulted.
func (a *EventAuthority) Reread(ctx context.Context, want repository.Event) (repository.Event, error) {
	if err := validateEventAuthorityInput(ctx, want); err != nil {
		return repository.Event{}, newEventAuthorityFault(err)
	}
	if a == nil || eventAuthorityMissing(a.repo) {
		return repository.Event{}, newEventAuthorityFault(errors.New("missing event persistence"))
	}
	rows, err := a.repo.ListEventsAfter(ctx, want.TenantID, want.ID-1, 1)
	if err = errors.Join(err, ctx.Err()); err != nil {
		return repository.Event{}, newEventAuthorityFault(err)
	}
	if len(rows) == 0 {
		return repository.Event{}, newEventAuthorityFault(errors.New("event is not persisted"))
	}
	got := rows[0]
	if got.ID != want.ID {
		return repository.Event{}, newEventAuthorityFault(
			fmt.Errorf("event gap: requested %d, first row is %d", want.ID, got.ID))
	}
	if !sameEventIdentity(got, want) {
		return repository.Event{}, newEventAuthorityFault(
			fmt.Errorf("event %d does not match its persisted fact", want.ID))
	}
	return got, nil
}

func validateEventAuthorityInput(ctx context.Context, want repository.Event) error {
	if eventAuthorityMissing(ctx) {
		return errors.New("missing event authority context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if want.ID <= 0 {
		return errors.New("event identity requires a positive persisted ID")
	}
	if want.TenantID == "" {
		return errors.New("event identity requires a non-empty tenant")
	}
	return nil
}

func sameEventIdentity(got, want repository.Event) bool {
	return got.Type == want.Type && got.TenantID == want.TenantID &&
		got.Bucket == want.Bucket && got.Key == want.Key &&
		sameEventObjectID(got.ObjectID, want.ObjectID) &&
		sameEventPayload(got.Payload, want.Payload)
}

func sameEventObjectID(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func sameEventPayload(a, b map[string]string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return maps.Equal(a, b)
}

// IsEventAuthorityFault recognizes observation/configuration faults so callers
// do not classify them as absence or fall back to legacy delivery.
func IsEventAuthorityFault(err error) bool {
	return errors.Is(err, ErrEventAuthorityFault) ||
		errors.Is(err, ErrEventAuthorityUnavailable)
}

type eventAuthorityFault struct{ cause error }

func newEventAuthorityFault(cause error) error {
	return &eventAuthorityFault{cause: cause}
}

func (*eventAuthorityFault) Error() string   { return ErrEventAuthorityFault.Error() }
func (e *eventAuthorityFault) Unwrap() error { return e.cause }
func (*eventAuthorityFault) Is(target error) bool {
	return target == ErrEventAuthorityFault
}

func eventAuthorityMissing(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

// ValidateClaimedOutboxFact performs the pure row/payload consistency checks a
// claimed outbox relay must pass before egress. It never rereads the object,
// the event or the outbox row: OriginID is an outbox fact, not Event.ID, and a
// valid deleted fact remains deliverable after the source object is gone.
func ValidateClaimedOutboxFact(fact repository.EventOutboxRow) error {
	if err := validateClaimedOutboxIdentity(fact); err != nil {
		return newEventAuthorityFault(err)
	}
	switch fact.EventType {
	case repository.EventTypeFileDeleted11:
		return validateAuthorityDeletedFact(fact)
	case repository.EventTypeFileNotify11:
		return validateAuthorityNotifyFact(fact)
	default:
		return newEventAuthorityFault(fmt.Errorf("unknown outbox event type %q", fact.EventType))
	}
}

func validateClaimedOutboxIdentity(fact repository.EventOutboxRow) error {
	if fact.ID <= 0 {
		return errors.New("outbox fact ID must be positive")
	}
	if fact.OriginID <= 0 {
		return errors.New("outbox origin ID must be positive")
	}
	if strings.TrimSpace(fact.TenantID) == "" {
		return errors.New("outbox fact tenant is required")
	}
	return nil
}

func validateAuthorityDeletedFact(fact repository.EventOutboxRow) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(fact.Payload, &raw); err != nil {
		return newEventAuthorityFault(fmt.Errorf("decode deleted outbox payload: %w", err))
	}
	var payload deletedFact
	if err := json.Unmarshal(fact.Payload, &payload); err != nil {
		return newEventAuthorityFault(fmt.Errorf("decode deleted outbox payload: %w", err))
	}
	if payload.SchemaVersion != "1.1" ||
		payload.EventType != string(repository.EventTypeFileDeleted11) ||
		payload.Tenant != fact.TenantID || payload.Bucket == "" || payload.Key == "" {
		return newEventAuthorityFault(errors.New("deleted outbox payload contradicts its claimed fact"))
	}
	if encoded, present := raw["object_id"]; present {
		var objectID int64
		if err := json.Unmarshal(encoded, &objectID); err != nil {
			return newEventAuthorityFault(fmt.Errorf("decode deleted outbox object ID: %w", err))
		}
		if objectID <= 0 || objectID != fact.OriginID {
			return newEventAuthorityFault(errors.New("deleted outbox payload object ID contradicts its claimed fact"))
		}
	}
	return nil
}

func validateAuthorityNotifyFact(fact repository.EventOutboxRow) error {
	var payload notifyFact
	if err := json.Unmarshal(fact.Payload, &payload); err != nil {
		return newEventAuthorityFault(fmt.Errorf("decode notify outbox payload: %w", err))
	}
	if payload.SchemaVersion != "1.1" ||
		payload.EventType != string(repository.EventTypeFileNotify11) ||
		payload.Tenant != fact.TenantID || payload.Bucket == "" || payload.Key == "" {
		return newEventAuthorityFault(errors.New("notify outbox payload contradicts its claimed fact"))
	}
	if len(payload.Records) == 0 {
		return newEventAuthorityFault(errors.New("notify outbox payload requires at least one record"))
	}
	for index, record := range payload.Records {
		if err := validateAuthorityNotifyRecord(payload, record); err != nil {
			return newEventAuthorityFault(fmt.Errorf("notify outbox record %d: %w", index, err))
		}
	}
	return nil
}

func validateAuthorityNotifyRecord(payload notifyFact, record notifyRecord) error {
	if record.EventVersion != "2.1" || record.EventSource != "aws:s3" ||
		record.EventName != eventOutboxRemoveEventName ||
		record.UserIdentity.PrincipalID != payload.Tenant {
		return errors.New("notify outbox record identity contradicts its payload")
	}
	bucket, object := record.S3.Bucket, record.S3.Object
	if record.S3.SchemaVersion != "1.0" || bucket.Name != payload.Bucket ||
		bucket.ARN != "arn:aws:s3:::"+payload.Bucket ||
		object.Key != payload.Key || object.Size != payload.Size ||
		object.ETag != payload.ETag || object.VersionID != payload.VersionID ||
		object.Sequencer == "" {
		return errors.New("notify outbox record object contradicts its payload")
	}
	return nil
}

// webhookEventEnvelope is the private decoded wire shape of a persisted
// webhook retry body. Decoding never makes the body authoritative:
// EventAuthority.Reread remains the authority for ID and scope.
type webhookEventEnvelope struct {
	ID        int64                `json:"id"`
	TenantID  string               `json:"tenant"`
	Bucket    string               `json:"bucket"`
	Key       string               `json:"key"`
	Type      repository.EventType `json:"type"`
	ObjectID  *int64               `json:"object_id"`
	RequestID string               `json:"request_id"`
	Payload   map[string]string    `json:"payload"`
	CreatedAt time.Time            `json:"created_at"`
}

func decodeWebhookEventEnvelope(body []byte) (repository.Event, error) {
	var envelope webhookEventEnvelope
	if len(body) == 0 {
		return repository.Event{}, errors.New("empty webhook event envelope")
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return repository.Event{}, fmt.Errorf("decode webhook event envelope: %w", err)
	}
	if envelope.CreatedAt.IsZero() {
		return repository.Event{}, errors.New("webhook event envelope has no created_at")
	}
	return repository.Event{
		ID: envelope.ID, TenantID: envelope.TenantID, Bucket: envelope.Bucket,
		Key: envelope.Key, Type: envelope.Type, ObjectID: envelope.ObjectID,
		RequestID: envelope.RequestID, Payload: envelope.Payload,
		CreatedAt: envelope.CreatedAt,
	}, nil
}
