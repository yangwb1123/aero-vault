package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/aero-vault/aero-vault/internal/repository"
)

type eventAuthorityTestReader struct {
	repository.Repository
	rows    []repository.Event
	err     error
	calls   int
	tenant  string
	afterID int64
	limit   int
	after   func()
}

func (r *eventAuthorityTestReader) ListEventsAfter(
	_ context.Context, tenant string, afterID int64, limit int,
) ([]repository.Event, error) {
	r.calls++
	r.tenant, r.afterID, r.limit = tenant, afterID, limit
	if r.after != nil {
		r.after()
	}
	return r.rows, r.err
}

func eventAuthorityTestRepo(t *testing.T) (repository.Repository, string) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "event-authority.db")
	repo, err := repository.Open(t.Context(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("open event authority repo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(t.Context()); err != nil {
		t.Fatalf("migrate event authority repo: %v", err)
	}
	return repo, dsn
}

func eventAuthorityTestInsert(t *testing.T, repo repository.Repository, event repository.Event) int64 {
	t.Helper()
	id, err := repo.InsertEvent(t.Context(), event)
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if id <= 0 {
		t.Fatalf("inserted event ID = %d", id)
	}
	return id
}

func eventAuthorityTestPersisted(id int64) repository.Event {
	objectID := int64(17)
	return repository.Event{
		ID: id, TenantID: "alpha", Bucket: "docs", Key: "a.txt",
		Type: repository.EventCreated, ObjectID: &objectID,
		RequestID: "persisted-request", Payload: map[string]string{"kind": "wanted"},
		CreatedAt: time.Unix(100, 0),
	}
}

func eventAuthorityTestWant(id int64) repository.Event {
	want := eventAuthorityTestPersisted(id)
	want.RequestID = "caller-request"
	want.CreatedAt = time.Unix(1, 0)
	return want
}

func eventAuthorityTestFault(t *testing.T, err, cause error) {
	t.Helper()
	if err == nil || !IsEventAuthorityFault(err) || !errors.Is(err, ErrEventAuthorityFault) {
		t.Fatalf("error = %v, want event authority fault", err)
	}
	if err.Error() != ErrEventAuthorityFault.Error() {
		t.Fatalf("fault exposed raw text: %q", err.Error())
	}
	if cause != nil && !errors.Is(err, cause) {
		t.Fatalf("fault lost cause %v: %v", cause, err)
	}
}

func eventAuthorityTestEmpty(t *testing.T, got repository.Event) {
	t.Helper()
	if got.ID != 0 || got.TenantID != "" || got.Bucket != "" ||
		got.Key != "" || got.ObjectID != nil || got.Payload != nil {
		t.Fatalf("fault returned a partial event: %+v", got)
	}
}

func TestEventAuthorityRereadsRealPersistedEvent(t *testing.T) {
	repo, _ := eventAuthorityTestRepo(t)
	missingObjectID := int64(999999)
	first := eventAuthorityTestInsert(t, repo, repository.Event{
		TenantID: "alpha", Bucket: "docs", Key: "deleted.txt",
		Type: repository.EventDeleted, ObjectID: &missingObjectID,
		RequestID: "persisted-request", Payload: map[string]string{"kind": "wanted"},
	})
	eventAuthorityTestInsert(t, repo, repository.Event{
		TenantID: "alpha", Bucket: "docs", Key: "later.txt", Type: repository.EventCreated,
	})
	authority, err := NewEventAuthority(repo)
	if err != nil {
		t.Fatalf("new authority: %v", err)
	}
	want := repository.Event{
		ID: first, TenantID: "alpha", Bucket: "docs", Key: "deleted.txt",
		Type: repository.EventDeleted, ObjectID: &missingObjectID,
		RequestID: "caller-value", Payload: map[string]string{"kind": "wanted"},
		CreatedAt: time.Unix(1, 0),
	}
	got, err := authority.Reread(t.Context(), want)
	if err != nil {
		t.Fatalf("reread real persisted event: %v", err)
	}
	if got.ID != first || got.TenantID != "alpha" || got.Bucket != "docs" ||
		got.Key != "deleted.txt" || got.Type != repository.EventDeleted ||
		got.ObjectID == nil || *got.ObjectID != missingObjectID ||
		got.RequestID != "persisted-request" || got.CreatedAt.IsZero() ||
		!sameEventPayload(got.Payload, map[string]string{"kind": "wanted"}) {
		t.Fatalf("reread returned a non-authoritative row: %+v", got)
	}
}

func TestEventAuthorityFirstRowIdentityContradictions(t *testing.T) {
	persisted := eventAuthorityTestPersisted(7)
	reader := &eventAuthorityTestReader{rows: []repository.Event{persisted}}
	authority, err := NewEventAuthority(reader)
	if err != nil {
		t.Fatalf("new authority: %v", err)
	}
	otherObject := int64(18)
	cases := []struct {
		name   string
		mutate func(*repository.Event)
	}{
		{"different_id", func(e *repository.Event) { e.ID = 8 }},
		{"earlier_id", func(e *repository.Event) { e.ID = 6 }},
		{"tenant", func(e *repository.Event) { e.TenantID = "beta" }},
		{"bucket", func(e *repository.Event) { e.Bucket = "other" }},
		{"key", func(e *repository.Event) { e.Key = "b.txt" }},
		{"type", func(e *repository.Event) { e.Type = repository.EventDeleted }},
		{"object_id", func(e *repository.Event) { e.ObjectID = &otherObject }},
		{"nil_object_id", func(e *repository.Event) { e.ObjectID = nil }},
		{"payload", func(e *repository.Event) { e.Payload = map[string]string{"kind": "other"} }},
		{"payload_extra", func(e *repository.Event) { e.Payload = map[string]string{"kind": "wanted", "extra": "x"} }},
		{"payload_missing", func(e *repository.Event) { e.Payload = nil }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			reader.calls = 0
			want := eventAuthorityTestWant(persisted.ID)
			test.mutate(&want)
			got, err := authority.Reread(t.Context(), want)
			eventAuthorityTestEmpty(t, got)
			eventAuthorityTestFault(t, err, nil)
			if reader.calls != 1 || reader.tenant != want.TenantID ||
				reader.afterID != want.ID-1 || reader.limit != 1 {
				t.Fatalf("reader call = calls:%d tenant:%q after:%d limit:%d",
					reader.calls, reader.tenant, reader.afterID, reader.limit)
			}
		})
	}
}

func TestEventAuthorityNonIdentityFieldsAndEmptyPayload(t *testing.T) {
	persisted := eventAuthorityTestPersisted(7)
	persisted.RequestID = "persisted-request"
	persisted.Payload = nil
	reader := &eventAuthorityTestReader{rows: []repository.Event{persisted}}
	authority, err := NewEventAuthority(reader)
	if err != nil {
		t.Fatalf("new authority: %v", err)
	}
	want := eventAuthorityTestWant(persisted.ID)
	want.Payload = map[string]string{}
	want.RequestID = "different-request"
	want.CreatedAt = time.Unix(999, 0)
	got, err := authority.Reread(t.Context(), want)
	if err != nil {
		t.Fatalf("nil and empty payload must be semantically equal: %v", err)
	}
	if got.RequestID != "persisted-request" || got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("reread did not return persisted non-identity fields: %+v", got)
	}
}

func TestEventAuthorityInputAndPersistenceFaults(t *testing.T) {
	for _, test := range []struct {
		name string
		ctx  context.Context
		want repository.Event
	}{
		{"zero_id", context.Background(), repository.Event{TenantID: "alpha"}},
		{"negative_id", context.Background(), repository.Event{ID: -1, TenantID: "alpha"}},
		{"empty_tenant", context.Background(), repository.Event{ID: 1}},
		{"nil_context", nil, repository.Event{ID: 1, TenantID: "alpha"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &eventAuthorityTestReader{}
			authority, err := NewEventAuthority(reader)
			if err != nil {
				t.Fatalf("new authority: %v", err)
			}
			got, rereadErr := authority.Reread(test.ctx, test.want)
			eventAuthorityTestEmpty(t, got)
			eventAuthorityTestFault(t, rereadErr, nil)
			if reader.calls != 0 {
				t.Fatalf("invalid input reached reader %d times", reader.calls)
			}
		})
	}

	type nilContext struct{ context.Context }
	var typedNilContext *nilContext
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name  string
		ctx   context.Context
		cause error
	}{
		{"typed_nil_context", typedNilContext, nil},
		{"canceled_before_read", canceled, context.Canceled},
	} {
		reader := &eventAuthorityTestReader{}
		authority, err := NewEventAuthority(reader)
		if err != nil {
			t.Fatalf("new authority: %v", err)
		}
		_, err = authority.Reread(test.ctx, repository.Event{ID: 1, TenantID: "alpha"})
		eventAuthorityTestFault(t, err, test.cause)
		if reader.calls != 0 {
			t.Fatalf("%s reached reader", test.name)
		}
	}

	cause := errors.New("persistence detail")
	reader := &eventAuthorityTestReader{err: cause}
	authority, err := NewEventAuthority(reader)
	if err != nil {
		t.Fatalf("new authority: %v", err)
	}
	_, err = authority.Reread(t.Context(), repository.Event{ID: 1, TenantID: "alpha"})
	eventAuthorityTestFault(t, err, cause)

	ctx, cancel := context.WithCancel(t.Context())
	reader = &eventAuthorityTestReader{rows: []repository.Event{eventAuthorityTestPersisted(1)}, after: cancel}
	authority, err = NewEventAuthority(reader)
	if err != nil {
		t.Fatalf("new authority: %v", err)
	}
	_, err = authority.Reread(ctx, eventAuthorityTestWant(1))
	eventAuthorityTestFault(t, err, context.Canceled)

	reader = &eventAuthorityTestReader{}
	authority, err = NewEventAuthority(reader)
	if err != nil {
		t.Fatalf("new authority: %v", err)
	}
	_, err = authority.Reread(t.Context(), repository.Event{ID: 5, TenantID: "alpha"})
	eventAuthorityTestFault(t, err, nil)
}

func TestEventAuthorityMissingPersistenceFailsClosed(t *testing.T) {
	if authority, err := NewEventAuthority(nil); authority != nil || !errors.Is(err, ErrEventAuthorityUnavailable) {
		t.Fatalf("nil repository: authority=%v err=%v", authority, err)
	}
	var typedNil *eventAuthorityTestReader
	if authority, err := NewEventAuthority(typedNil); authority != nil || !errors.Is(err, ErrEventAuthorityUnavailable) {
		t.Fatalf("typed-nil repository: authority=%v err=%v", authority, err)
	}
	var missing *EventAuthority
	if _, err := missing.Reread(t.Context(), repository.Event{ID: 1, TenantID: "alpha"}); !IsEventAuthorityFault(err) {
		t.Fatalf("nil authority must fault, got %v", err)
	}
	if _, err := (&EventAuthority{}).Reread(t.Context(), repository.Event{ID: 1, TenantID: "alpha"}); !IsEventAuthorityFault(err) {
		t.Fatalf("empty authority must fault, got %v", err)
	}
}

func TestEventAuthorityRealDecodeFaultPreservesCause(t *testing.T) {
	repo, dsn := eventAuthorityTestRepo(t)
	id := eventAuthorityTestInsert(t, repo, repository.Event{
		TenantID: "alpha", Bucket: "docs", Key: "bad.txt", Type: repository.EventCreated,
	})
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(context.Background(),
		`UPDATE object_events SET payload=$1 WHERE id=$2`, `{"broken":`, id); err != nil {
		t.Fatalf("persist malformed payload: %v", err)
	}
	authority, err := NewEventAuthority(repo)
	if err != nil {
		t.Fatalf("new authority: %v", err)
	}
	want := repository.Event{
		ID: id, TenantID: "alpha", Bucket: "docs", Key: "bad.txt",
		Type: repository.EventCreated,
	}
	_, err = authority.Reread(t.Context(), want)
	var syntaxErr *json.SyntaxError
	if err == nil || !IsEventAuthorityFault(err) || !errors.As(err, &syntaxErr) {
		t.Fatalf("decode fault = %v, want preserved *json.SyntaxError", err)
	}
}

func eventAuthorityTestObject() repository.Object {
	return repository.Object{
		ID: 17, Bucket: "docs", Key: "a.txt", VersionID: "v-1",
		Size: 42, ETag: "etag-1", Backend: "local",
	}
}

func eventAuthorityTestDeletedRow() repository.EventOutboxRow {
	object := eventAuthorityTestObject()
	return repository.EventOutboxRow{
		ID: 23, EventType: repository.EventTypeFileDeleted11, OriginID: object.ID,
		TenantID: "alpha", Payload: BuildDeletedFact(object, "alice", "req-1", "alpha"),
	}
}

func eventAuthorityTestNotifyRow() repository.EventOutboxRow {
	object := eventAuthorityTestObject()
	return repository.EventOutboxRow{
		ID: 24, EventType: repository.EventTypeFileNotify11, OriginID: object.ID,
		TenantID: "alpha", Payload: BuildNotifyFact(object, "alice", "req-1", "alpha", "sequencer-1"),
	}
}

func TestValidateClaimedOutboxFacts(t *testing.T) {
	if err := ValidateClaimedOutboxFact(eventAuthorityTestDeletedRow()); err != nil {
		t.Fatalf("valid deleted fact: %v", err)
	}
	if err := ValidateClaimedOutboxFact(eventAuthorityTestNotifyRow()); err != nil {
		t.Fatalf("valid notify fact: %v", err)
	}
	legacy := eventAuthorityTestDeletedRow()
	var doc map[string]any
	eventAuthorityTestDecode(t, legacy.Payload, &doc)
	delete(doc, "object_id")
	legacy.Payload = eventAuthorityTestEncode(t, doc)
	if err := ValidateClaimedOutboxFact(legacy); err != nil {
		t.Fatalf("legacy deleted fact without object_id: %v", err)
	}
}

func TestValidateClaimedOutboxFactRowContradictions(t *testing.T) {
	valid := eventAuthorityTestDeletedRow()
	cases := []struct {
		name   string
		mutate func(*repository.EventOutboxRow)
	}{
		{"row_id", func(f *repository.EventOutboxRow) { f.ID = 0 }},
		{"origin_id", func(f *repository.EventOutboxRow) { f.OriginID = 0 }},
		{"empty_tenant", func(f *repository.EventOutboxRow) { f.TenantID = "" }},
		{"space_tenant", func(f *repository.EventOutboxRow) { f.TenantID = "  " }},
		{"row_tenant", func(f *repository.EventOutboxRow) { f.TenantID = "beta" }},
		{"row_type", func(f *repository.EventOutboxRow) { f.EventType = repository.EventTypeFileNotify11 }},
		{"unknown_type", func(f *repository.EventOutboxRow) { f.EventType = "vault.file.other@1.1" }},
		{"empty_payload", func(f *repository.EventOutboxRow) { f.Payload = nil }},
		{"malformed_json", func(f *repository.EventOutboxRow) { f.Payload = []byte("{") }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			row := valid
			test.mutate(&row)
			eventAuthorityTestFault(t, ValidateClaimedOutboxFact(row), nil)
		})
	}
}

func TestValidateDeletedOutboxFactContradictions(t *testing.T) {
	object := eventAuthorityTestObject()
	valid := eventAuthorityTestDeletedRow()
	cases := []struct {
		name   string
		mutate func(*deletedFact)
	}{
		{"schema", func(doc *deletedFact) { doc.SchemaVersion = "1.0" }},
		{"event_type", func(doc *deletedFact) { doc.EventType = string(repository.EventTypeFileNotify11) }},
		{"tenant", func(doc *deletedFact) { doc.Tenant = "beta" }},
		{"bucket", func(doc *deletedFact) { doc.Bucket = "" }},
		{"key", func(doc *deletedFact) { doc.Key = "" }},
		{"object_id_negative", func(doc *deletedFact) { doc.ObjectID = -1 }},
		{"object_id_mismatch", func(doc *deletedFact) { doc.ObjectID = object.ID + 1 }},
		{"object_id_zero", func(doc *deletedFact) { doc.ObjectID = 0 }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			row := valid
			var doc deletedFact
			eventAuthorityTestDecode(t, row.Payload, &doc)
			test.mutate(&doc)
			row.Payload = eventAuthorityTestEncode(t, doc)
			eventAuthorityTestFault(t, ValidateClaimedOutboxFact(row), nil)
		})
	}
}

func TestValidateNotifyOutboxFactContradictions(t *testing.T) {
	valid := eventAuthorityTestNotifyRow()
	cases := []struct {
		name   string
		mutate func(*notifyFact)
	}{
		{"schema", func(doc *notifyFact) { doc.SchemaVersion = "1.0" }},
		{"event_type", func(doc *notifyFact) { doc.EventType = string(repository.EventTypeFileDeleted11) }},
		{"tenant", func(doc *notifyFact) { doc.Tenant = "beta" }},
		{"bucket", func(doc *notifyFact) { doc.Bucket = "other" }},
		{"key", func(doc *notifyFact) { doc.Key = "other.txt" }},
		{"no_records", func(doc *notifyFact) { doc.Records = nil }},
		{"record_event_version", func(doc *notifyFact) { doc.Records[0].EventVersion = "2.0" }},
		{"record_event_source", func(doc *notifyFact) { doc.Records[0].EventSource = "aws:other" }},
		{"record_event_name", func(doc *notifyFact) { doc.Records[0].EventName = "s3:ObjectCreated:Put" }},
		{"record_principal", func(doc *notifyFact) { doc.Records[0].UserIdentity.PrincipalID = "beta" }},
		{"record_bucket", func(doc *notifyFact) { doc.Records[0].S3.Bucket.Name = "other" }},
		{"record_arn", func(doc *notifyFact) { doc.Records[0].S3.Bucket.ARN = "arn:aws:s3:::other" }},
		{"record_key", func(doc *notifyFact) { doc.Records[0].S3.Object.Key = "other.txt" }},
		{"record_size", func(doc *notifyFact) { doc.Records[0].S3.Object.Size++ }},
		{"record_etag", func(doc *notifyFact) { doc.Records[0].S3.Object.ETag = "other" }},
		{"record_version", func(doc *notifyFact) { doc.Records[0].S3.Object.VersionID = "other" }},
		{"record_sequencer", func(doc *notifyFact) { doc.Records[0].S3.Object.Sequencer = "" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			row := valid
			var doc notifyFact
			eventAuthorityTestDecode(t, row.Payload, &doc)
			test.mutate(&doc)
			row.Payload = eventAuthorityTestEncode(t, doc)
			eventAuthorityTestFault(t, ValidateClaimedOutboxFact(row), nil)
		})
	}
}

func TestValidateNotifyOutboxFactAllowsConsistentRecords(t *testing.T) {
	row := eventAuthorityTestNotifyRow()
	var doc notifyFact
	eventAuthorityTestDecode(t, row.Payload, &doc)
	doc.Records = append(doc.Records, doc.Records[0])
	row.Payload = eventAuthorityTestEncode(t, doc)
	if err := ValidateClaimedOutboxFact(row); err != nil {
		t.Fatalf("consistent notify records: %v", err)
	}
}

func TestDecodeWebhookEventEnvelope(t *testing.T) {
	body := []byte(`{"id":42,"tenant":"alpha","bucket":"docs","key":"a.txt","type":"created","object_id":17,"request_id":"req-1","payload":{"k":"v"},"created_at":"2025-01-02T03:04:05.000000006Z"}`)
	event, err := decodeWebhookEventEnvelope(body)
	if err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if event.ID != 42 || event.TenantID != "alpha" || event.Bucket != "docs" ||
		event.Key != "a.txt" || event.Type != repository.EventCreated ||
		event.ObjectID == nil || *event.ObjectID != 17 || event.RequestID != "req-1" ||
		event.Payload["k"] != "v" || event.CreatedAt.IsZero() {
		t.Fatalf("decoded envelope = %+v", event)
	}
	for _, body := range [][]byte{
		nil, {}, []byte("{"), []byte(`{"id":42,"tenant":"alpha"}`),
		[]byte(`{"id":1.5,"tenant":"alpha","created_at":"2025-01-02T03:04:05Z"}`),
		[]byte(`{"id":1,"tenant":"alpha","payload":{"k":1},"created_at":"2025-01-02T03:04:05Z"}`),
	} {
		if _, err := decodeWebhookEventEnvelope(body); err == nil {
			t.Fatalf("invalid envelope %q decoded without error", body)
		}
	}
}

func eventAuthorityTestDecode(t *testing.T, payload []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(payload, target); err != nil {
		t.Fatalf("decode test payload: %v", err)
	}
}

func eventAuthorityTestEncode(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode test payload: %v", err)
	}
	return payload
}
