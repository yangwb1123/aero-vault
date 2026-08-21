package events

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// Payload builders for the deletion transactional outbox (FR-2). Both facts
// are self-contained and byte-stable for a fixed input: struct field order is
// fixed and encoding/json emits keys in struct order, so golden-byte tests
// (AC-3) are exact. The payload is built at delete time, never re-derived at
// delivery time.

// newSequencer produces the S3 notification sequencer at emit time. It is a
// package-level variable so tests can inject a fixed value. crypto/rand 16
// bytes → 32 hex chars, unique per delete occurrence — obj.ID is NOT usable:
// RestoreObject reuses the objects row id across restore→re-delete (D6/N1).
var newSequencer = func() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return hex.EncodeToString(buf[:]) // rand.Read never fails on modern platforms
	}
	return hex.EncodeToString(buf[:])
}

// deletedFact is the vault.file.deleted@1.1 lifecycle fact. Field order is
// pinned: object_id sits right after key (D6) so golden-byte tests are exact.
// object_id is the objects row id at delete time (informational; the payload
// as a whole is the receiver-visible identity — C1/G5).
type deletedFact struct {
	SchemaVersion string   `json:"schema_version"`
	EventType     string   `json:"event_type"`
	Tenant        string   `json:"tenant"`
	Bucket        string   `json:"bucket"`
	Key           string   `json:"key"`
	ObjectID      int64    `json:"object_id"`
	VersionID     string   `json:"version_id"`
	Size          int64    `json:"size"`
	ETag          string   `json:"etag"`
	Backend       string   `json:"backend"`
	RequestID     string   `json:"request_id"`
	Actor         string   `json:"actor"`
	ShareIDs      []string `json:"share_ids,omitempty"`
	VersionCount  int      `json:"version_count,omitempty"`
	ChunkCount    int      `json:"chunk_count,omitempty"`
	// Reason is the deletion reason vocabulary (quarantine uses
	// "av_infected"). omitempty keeps REST-path goldens byte-identical.
	Reason string `json:"reason,omitempty"`
}

// notifyFact is the vault.file.notify@1.1 S3-notification-shaped fact. Its
// records are carried verbatim by the relay; the recipient sees the same bytes
// the delete saw.
type notifyFact struct {
	SchemaVersion string         `json:"schema_version"`
	EventType     string         `json:"event_type"`
	Tenant        string         `json:"tenant"`
	Bucket        string         `json:"bucket"`
	Key           string         `json:"key"`
	VersionID     string         `json:"version_id"`
	Size          int64          `json:"size"`
	ETag          string         `json:"etag"`
	Backend       string         `json:"backend"`
	RequestID     string         `json:"request_id"`
	Actor         string         `json:"actor"`
	Records       []notifyRecord `json:"records"`
	// Signature carries the antivirus threat name on quarantine
	// notifications (self-contained payload). omitempty keeps REST-path
	// goldens byte-identical.
	Signature string `json:"signature,omitempty"`
}

type notifyRecord struct {
	EventVersion string         `json:"eventVersion"`
	EventSource  string         `json:"eventSource"`
	AWSRegion    string         `json:"awsRegion"`
	EventName    string         `json:"eventName"`
	UserIdentity notifyIdentity `json:"userIdentity"`
	S3           notifyS3Entity `json:"s3"`
}

type notifyIdentity struct {
	PrincipalID string `json:"principalId"`
}

type notifyS3Entity struct {
	SchemaVersion string       `json:"s3SchemaVersion"`
	Bucket        notifyBucket `json:"bucket"`
	Object        notifyObject `json:"object"`
}

type notifyBucket struct {
	Name string `json:"name"`
	ARN  string `json:"arn"`
}

type notifyObject struct {
	Key       string `json:"key"`
	Size      int64  `json:"size"`
	ETag      string `json:"eTag"`
	VersionID string `json:"versionId"`
	Sequencer string `json:"sequencer"`
}

// BuildDeletedFact builds the vault.file.deleted@1.1 fact payload. actor may
// be empty (anonymous/no-principal contexts are legal — no new identity
// pipeline is introduced). reason is optional and appended only when non-empty
// (additive schema: REST-path goldens stay byte-identical).
func BuildDeletedFact(obj repository.Object, actor, requestID, tenant string, reason ...string) []byte {
	return buildDeletedFact(obj, actor, requestID, tenant, nil, 0, 0, reason...)
}

// BuildDeletedFactWithRefs adds best-effort pre-delete capability and index
// reference counts to the privileged delete fact. Zero-valued fields remain
// omitted so ordinary delete payloads retain their byte-stable shape.
func BuildDeletedFactWithRefs(
	obj repository.Object, actor, requestID, tenant string, shareIDs []string,
	versionCount, chunkCount int, reason ...string,
) []byte {
	return buildDeletedFact(obj, actor, requestID, tenant, shareIDs, versionCount, chunkCount, reason...)
}

func buildDeletedFact(
	obj repository.Object, actor, requestID, tenant string, shareIDs []string,
	versionCount, chunkCount int, reason ...string,
) []byte {
	reasonValue := ""
	if len(reason) > 0 {
		reasonValue = reason[0]
	}
	payload, _ := json.Marshal(deletedFact{
		SchemaVersion: "1.1",
		EventType:     string(repository.EventTypeFileDeleted11),
		Tenant:        tenant,
		Bucket:        obj.Bucket,
		Key:           obj.Key,
		ObjectID:      obj.ID,
		VersionID:     obj.VersionID,
		Size:          obj.Size,
		ETag:          obj.ETag,
		Backend:       obj.Backend,
		RequestID:     requestID,
		Actor:         actor,
		ShareIDs:      shareIDs,
		VersionCount:  versionCount,
		ChunkCount:    chunkCount,
		Reason:        reasonValue,
	})
	return payload
}

// BuildNotifyFact builds the vault.file.notify@1.1 fact payload with a
// fully self-contained S3 records section (size/etag/versionId/sequencer all
// captured at emit time — the E7 fix). sequencer is normally produced by
// newSequencer; tests inject a fixed value. signature is optional and appended
// only when non-empty (additive schema: REST-path goldens stay byte-identical).
func BuildNotifyFact(obj repository.Object, actor, requestID, tenant, sequencer string, signature ...string) []byte {
	if sequencer == "" {
		sequencer = newSequencer()
	}
	signatureValue := ""
	if len(signature) > 0 {
		signatureValue = signature[0]
	}
	payload, _ := json.Marshal(notifyFact{
		SchemaVersion: "1.1",
		EventType:     string(repository.EventTypeFileNotify11),
		Tenant:        tenant,
		Bucket:        obj.Bucket,
		Key:           obj.Key,
		VersionID:     obj.VersionID,
		Size:          obj.Size,
		ETag:          obj.ETag,
		Backend:       obj.Backend,
		RequestID:     requestID,
		Actor:         actor,
		Records: []notifyRecord{{
			EventVersion: "2.1",
			EventSource:  "aws:s3",
			AWSRegion:    "us-east-1",
			EventName:    "s3:ObjectRemoved:Delete",
			UserIdentity: notifyIdentity{PrincipalID: tenant},
			S3: notifyS3Entity{
				SchemaVersion: "1.0",
				Bucket: notifyBucket{
					Name: obj.Bucket,
					ARN:  "arn:aws:s3:::" + obj.Bucket,
				},
				Object: notifyObject{
					Key:       obj.Key,
					Size:      obj.Size,
					ETag:      obj.ETag,
					VersionID: obj.VersionID,
					Sequencer: sequencer,
				},
			},
		}},
		Signature: signatureValue,
	})
	return payload
}
