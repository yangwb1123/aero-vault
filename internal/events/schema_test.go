package events

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// AC-3: golden JSON pinning both fact schemas byte-exact. Fixed inputs, fixed
// struct field order → canonical marshal is byte-stable. The payloads are
// produced by the production builders (never hand-written in the test).

const goldenDeletedFact = `{"schema_version":"1.1","event_type":"vault.file.deleted@1.1","tenant":"default","bucket":"default","key":"docs/a.txt","object_id":42,"version_id":"v-abc","size":42,"etag":"etag-1","backend":"local","request_id":"req-1","actor":"alice"}`

const goldenNotifyFact = `{"schema_version":"1.1","event_type":"vault.file.notify@1.1","tenant":"default","bucket":"default","key":"docs/a.txt","version_id":"v-abc","size":42,"etag":"etag-1","backend":"local","request_id":"req-1","actor":"alice","records":[{"eventVersion":"2.1","eventSource":"aws:s3","awsRegion":"us-east-1","eventName":"s3:ObjectRemoved:Delete","userIdentity":{"principalId":"default"},"s3":{"s3SchemaVersion":"1.0","bucket":{"name":"default","arn":"arn:aws:s3:::default"},"object":{"key":"docs/a.txt","size":42,"eTag":"etag-1","versionId":"v-abc","sequencer":"8f3a2c9e4b1d7f06a5c0e2d94b7a1f3e"}}}]}`

func goldenObject() repository.Object {
	return repository.Object{
		ID:        42, // pinned: prevents a silent "object_id":0 in the golden bytes
		Bucket:    "default",
		Key:       "docs/a.txt",
		VersionID: "v-abc",
		Size:      42,
		ETag:      "etag-1",
		Backend:   "local",
	}
}

func TestEventSchema_GoldenJSON(t *testing.T) {
	deleted := string(BuildDeletedFact(goldenObject(), "alice", "req-1", "default"))
	if deleted != goldenDeletedFact {
		t.Errorf("deleted@1.1 golden mismatch\n got: %s\nwant: %s", deleted, goldenDeletedFact)
	}
	notify := string(BuildNotifyFact(goldenObject(), "alice", "req-1", "default", "8f3a2c9e4b1d7f06a5c0e2d94b7a1f3e"))
	if notify != goldenNotifyFact {
		t.Errorf("notify@1.1 golden mismatch\n got: %s\nwant: %s", notify, goldenNotifyFact)
	}
}

func TestEventSchema_RequiredFields(t *testing.T) {
	deleted := BuildDeletedFact(goldenObject(), "alice", "req-1", "default")
	var doc map[string]any
	if err := json.Unmarshal(deleted, &doc); err != nil {
		t.Fatalf("deleted payload is not JSON: %v", err)
	}
	if doc["schema_version"] != "1.1" {
		t.Errorf("schema_version = %v, want 1.1", doc["schema_version"])
	}
	for _, field := range []string{"schema_version", "event_type", "tenant", "bucket", "key",
		"object_id", "version_id", "size", "etag", "backend", "request_id", "actor"} {
		if _, ok := doc[field]; !ok {
			t.Errorf("deleted@1.1 missing required field %q", field)
		}
	}
	if id, ok := doc["object_id"].(float64); !ok || id != 42 {
		t.Errorf("object_id = %v, want 42 (== obj.ID)", doc["object_id"])
	}
	if _, ok := doc["records"]; ok {
		t.Error("deleted@1.1 must not carry records")
	}

	notify := BuildNotifyFact(goldenObject(), "alice", "req-1", "default", "8f3a2c9e4b1d7f06a5c0e2d94b7a1f3e")
	var ndoc struct {
		Records []struct {
			EventName string `json:"eventName"`
			S3        struct {
				Object struct {
					Key       string `json:"key"`
					Size      int64  `json:"size"`
					ETag      string `json:"eTag"`
					VersionID string `json:"versionId"`
					Sequencer string `json:"sequencer"`
				} `json:"object"`
			} `json:"s3"`
		} `json:"records"`
	}
	if err := json.Unmarshal(notify, &ndoc); err != nil {
		t.Fatalf("notify payload is not JSON: %v", err)
	}
	if len(ndoc.Records) != 1 {
		t.Fatalf("records = %d, want 1", len(ndoc.Records))
	}
	record := ndoc.Records[0]
	if record.EventName != "s3:ObjectRemoved:Delete" {
		t.Errorf("eventName = %q", record.EventName)
	}
	if record.S3.Object.Key != "docs/a.txt" || record.S3.Object.Size != 42 ||
		record.S3.Object.ETag != "etag-1" || record.S3.Object.VersionID != "v-abc" ||
		record.S3.Object.Sequencer != "8f3a2c9e4b1d7f06a5c0e2d94b7a1f3e" {
		t.Errorf("notify@1.1 s3.object not self-contained: %+v", record.S3.Object)
	}
}

func TestEventSchema_Deleted11Envelope(t *testing.T) {
	// AC-4: the deleted@1.1 envelope must carry the full identity set —
	// schema_version/event_type/tenant/bucket/key/object_id/actor plus the
	// WIP fields — and must NOT carry S3-notification records. (Golden bytes
	// and required fields are pinned by TestEventSchema_GoldenJSON and
	// TestEventSchema_RequiredFields; this test documents the AC mapping.)
	payload := BuildDeletedFact(goldenObject(), "alice", "req-1", "default")
	var doc struct {
		SchemaVersion string `json:"schema_version"`
		EventType     string `json:"event_type"`
		Tenant        string `json:"tenant"`
		Bucket        string `json:"bucket"`
		Key           string `json:"key"`
		ObjectID      int64  `json:"object_id"`
		Actor         string `json:"actor"`
	}
	if err := json.Unmarshal(payload, &doc); err != nil {
		t.Fatalf("deleted payload is not JSON: %v", err)
	}
	if doc.SchemaVersion != "1.1" || doc.EventType != "vault.file.deleted@1.1" {
		t.Errorf("version/type = %q/%q", doc.SchemaVersion, doc.EventType)
	}
	if doc.Tenant != "default" || doc.Bucket != "default" || doc.Key != "docs/a.txt" {
		t.Errorf("identity = %q/%q/%q", doc.Tenant, doc.Bucket, doc.Key)
	}
	if doc.ObjectID != 42 {
		t.Errorf("object_id = %d, want 42 (== obj.ID)", doc.ObjectID)
	}
	if doc.Actor != "alice" {
		t.Errorf("actor = %q, want alice", doc.Actor)
	}
	if strings.Contains(string(payload), `"records"`) {
		t.Error("deleted@1.1 must not carry records")
	}
}

func TestEventSchema_SequencerUniquePerCall(t *testing.T) {
	// Two facts built in the same process must never share a sequencer
	// (unlike obj.ID, which RestoreObject reuses across restore→re-delete).
	first := string(BuildNotifyFact(goldenObject(), "", "", "default", ""))
	second := string(BuildNotifyFact(goldenObject(), "", "", "default", ""))
	if first == second {
		t.Fatal("sequencer collision: two facts identical")
	}
	if !strings.Contains(first, `"sequencer":"`) || !strings.Contains(second, `"sequencer":"`) {
		t.Fatalf("sequencer missing: %s / %s", first, second)
	}
}
