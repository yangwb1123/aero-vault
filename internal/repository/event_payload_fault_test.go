package repository

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestEventReadersPayloadFault(t *testing.T) {
	s := openTestSQLite(t)
	firstID := eventPayloadInsert(t, s, "payload-fault", "first", map[string]string{"reason": "valid"})
	badID := eventPayloadInsert(t, s, "payload-fault", "bad", nil)
	tests := []struct {
		name   string
		raw    string
		syntax bool
	}{
		{"truncated_json", `{"reason":`, true},
		{"non_string_value", `{"reason":7}`, false},
		{"non_map_value", `["reason"]`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eventPayloadStore(t, s, badID, tt.raw)
			for _, reader := range eventPayloadReaders(s, "payload-fault", firstID-1, 10) {
				t.Run(reader.name, func(t *testing.T) {
					got, err := reader.read(t.Context())
					eventPayloadAssertFault(t, got, err, tt.syntax)
				})
			}
		})
	}
}

func TestEventReadersEmptyPayload(t *testing.T) {
	s := openTestSQLite(t)
	emptyText, emptyObject, jsonNull := "", "{}", "null"
	tests := []struct {
		name    string
		payload map[string]string
		raw     *string
		wantNil bool
	}{
		{"nil_input", nil, nil, false},
		{"empty_map", map[string]string{}, nil, false},
		{"empty_text", nil, &emptyText, false},
		{"empty_object", nil, &emptyObject, false},
		{"json_null", nil, &jsonNull, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := eventPayloadInsert(t, s, "empty-payload", tt.name, tt.payload)
			if tt.raw != nil {
				eventPayloadStore(t, s, id, *tt.raw)
			}
			for _, reader := range eventPayloadReaders(s, "empty-payload", id-1, 1) {
				t.Run(reader.name, func(t *testing.T) {
					got, err := reader.read(t.Context())
					if err != nil {
						t.Fatalf("read empty payload: %v", err)
					}
					eventPayloadAssertIDs(t, got, []int64{id})
					if len(got[0].Payload) != 0 || (got[0].Payload == nil) != tt.wantNil {
						t.Fatalf("payload = %#v, want empty with nil=%v", got[0].Payload, tt.wantNil)
					}
				})
			}
			if err := s.MarkEventConsumed(t.Context(), id); err != nil {
				t.Fatalf("consume event: %v", err)
			}
		})
	}
}

func TestEventReadersPayloadSelection(t *testing.T) {
	s := openTestSQLite(t)
	firstID := eventPayloadInsert(t, s, "selected", "first", map[string]string{"value": "first"})
	otherID := eventPayloadInsert(t, s, "other", "hidden", nil)
	lastID := eventPayloadInsert(t, s, "selected", "last", map[string]string{"value": "last"})
	eventPayloadStore(t, s, otherID, `{"broken":`)
	for _, id := range []int64{firstID, otherID} {
		if err := s.MarkEventConsumed(t.Context(), id); err != nil {
			t.Fatalf("consume event %d: %v", id, err)
		}
	}
	got, err := s.ListEventsAfter(t.Context(), "selected", 0, 10)
	if err != nil {
		t.Fatalf("tenant history: %v", err)
	}
	eventPayloadAssertIDs(t, got, []int64{firstID, lastID})
	if got[0].Payload["value"] != "first" || got[1].Payload["value"] != "last" {
		t.Fatalf("valid payloads changed: %+v", got)
	}
	for _, reader := range eventPayloadReaders(s, "selected", firstID, 1) {
		t.Run(reader.name, func(t *testing.T) {
			got, err := reader.read(t.Context())
			if err != nil {
				t.Fatalf("selected event: %v", err)
			}
			eventPayloadAssertIDs(t, got, []int64{lastID})
			if got[0].TenantID != "selected" || got[0].Key != "last" || got[0].Payload["value"] != "last" {
				t.Fatalf("selected wrong event: %+v", got[0])
			}
		})
	}
	got, err = s.ListEventsAfter(t.Context(), "selected", lastID, 1)
	if err != nil {
		t.Fatalf("history after last event: %v", err)
	}
	eventPayloadAssertIDs(t, got, nil)
}

type eventPayloadReader struct {
	name string
	read func(context.Context) ([]Event, error)
}

func eventPayloadReaders(s *sqlStore, tenant string, afterID int64, limit int) []eventPayloadReader {
	return []eventPayloadReader{
		{"ListEventsAfter", func(ctx context.Context) ([]Event, error) {
			return s.ListEventsAfter(ctx, tenant, afterID, limit)
		}},
		{"NextUnconsumedEvents", func(ctx context.Context) ([]Event, error) {
			return s.NextUnconsumedEvents(ctx, limit)
		}},
	}
}

func eventPayloadInsert(t *testing.T, s *sqlStore, tenant, key string, payload map[string]string) int64 {
	t.Helper()
	id, err := s.InsertEvent(t.Context(), Event{
		TenantID: tenant, Bucket: "docs", Key: key, Type: EventDeleted, Payload: payload,
	})
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if id <= 0 {
		t.Fatalf("event ID = %d, want positive", id)
	}
	return id
}

func eventPayloadStore(t *testing.T, s *sqlStore, id int64, raw string) {
	t.Helper()
	result, err := s.db.ExecContext(t.Context(), s.rebind(`UPDATE object_events SET payload=$1 WHERE id=$2`), raw, id)
	if err != nil {
		t.Fatalf("persist payload: %v", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		t.Fatalf("payload rows affected = %d, err=%v; want 1", changed, err)
	}
}

func eventPayloadAssertFault(t *testing.T, got []Event, err error, syntax bool) {
	t.Helper()
	if err == nil {
		t.Fatalf("malformed persisted payload returned %d events without an error", len(got))
	}
	if got != nil {
		t.Errorf("malformed payload returned partial events: %+v", got)
	}
	if syntax {
		var cause *json.SyntaxError
		if !errors.As(err, &cause) {
			t.Fatalf("error = %v, want preserved *json.SyntaxError", err)
		}
		return
	}
	var cause *json.UnmarshalTypeError
	if !errors.As(err, &cause) {
		t.Fatalf("error = %v, want preserved *json.UnmarshalTypeError", err)
	}
}

func eventPayloadAssertIDs(t *testing.T, got []Event, want []int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("events = %+v, want IDs %v", got, want)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("event %d ID = %d, want %d", i, got[i].ID, id)
		}
	}
}
