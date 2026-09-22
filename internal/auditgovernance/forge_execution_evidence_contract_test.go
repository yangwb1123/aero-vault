package auditgovernance

import (
	"bytes"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// This receiver-side copy of Catalyst's execution-evidence value verifies
// that Vault can inspect bounded metadata without accepting content or
// authority. It does not publish, enqueue, authorize, or schedule Forge work.
//
//go:embed testdata/forge-run-execution-evidence-v1.json
var forgeRunExecutionEvidenceFixture []byte

type forgeRunExecutionEvidenceContract struct {
	APIVersion             string                             `json:"api_version"`
	EvaluationMode         string                             `json:"evaluation_mode"`
	OwnerRef               string                             `json:"owner_ref"`
	ConversationID         string                             `json:"conversation_id"`
	RunID                  string                             `json:"run_id"`
	PromptID               string                             `json:"prompt_id"`
	RunStatus              string                             `json:"run_status"`
	AttemptID              string                             `json:"attempt_id"`
	TargetID               string                             `json:"target_id"`
	CommandID              string                             `json:"command_id"`
	CommandSHA256          string                             `json:"command_sha256"`
	DispositionKind        string                             `json:"disposition_kind"`
	ReceiptObservedAtMS    int64                              `json:"receipt_observed_at_ms"`
	Uncertain              bool                               `json:"uncertain"`
	ReconciliationRequired bool                               `json:"reconciliation_required"`
	MetadataObserved       bool                               `json:"metadata_observed"`
	ContentIncluded        bool                               `json:"content_included"`
	Authority              forgeRunExecutionEvidenceAuthority `json:"authority"`
}

type forgeRunExecutionEvidenceAuthority struct {
	IdentityVerified    bool `json:"identity_verified"`
	OwnerAuthorized     bool `json:"owner_authorized"`
	RunAuthoritative    bool `json:"run_authoritative"`
	ReceiptPersisted    bool `json:"receipt_persisted"`
	ReservationCreated  bool `json:"reservation_created"`
	ExecutionAuthorized bool `json:"execution_authorized"`
	DispatchPerformed   bool `json:"dispatch_performed"`
	AuditPublished      bool `json:"audit_published"`
}

func decodeForgeRunExecutionEvidence(raw []byte) (forgeRunExecutionEvidenceContract, error) {
	if err := rejectForgeDuplicateJSONKeys(raw); err != nil {
		return forgeRunExecutionEvidenceContract{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var fixture forgeRunExecutionEvidenceContract
	if err := decoder.Decode(&fixture); err != nil {
		return forgeRunExecutionEvidenceContract{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return forgeRunExecutionEvidenceContract{}, io.ErrUnexpectedEOF
		}
		return forgeRunExecutionEvidenceContract{}, err
	}
	return fixture, nil
}

func forgeRunExecutionEvidenceSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && strings.ToLower(value) == value
}

func TestForgeRunExecutionEvidenceCanonicalFixture(t *testing.T) {
	fixture, err := decodeForgeRunExecutionEvidence(forgeRunExecutionEvidenceFixture)
	if err != nil {
		t.Fatalf("decode Forge execution evidence fixture: %v", err)
	}
	if fixture.APIVersion != "forge.run.execution-evidence.v1" ||
		fixture.EvaluationMode != "pure_run_execution_evidence_binding" {
		t.Fatalf("unexpected Forge execution evidence identity: %#v", fixture)
	}
	if !forgeRunExecutionEvidenceSHA256(fixture.OwnerRef) || !forgeRunExecutionEvidenceSHA256(fixture.CommandSHA256) {
		t.Fatalf("owner/command digest is not lowercase SHA-256: %#v", fixture)
	}
	for name, value := range map[string]string{
		"conversation_id": fixture.ConversationID, "run_id": fixture.RunID,
		"prompt_id": fixture.PromptID, "run_status": fixture.RunStatus,
		"attempt_id": fixture.AttemptID, "target_id": fixture.TargetID,
		"command_id": fixture.CommandID, "disposition_kind": fixture.DispositionKind,
	} {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, " :/\\") {
			t.Fatalf("unsafe %s=%q", name, value)
		}
	}
	if fixture.ReceiptObservedAtMS < 0 || fixture.DispositionKind != "completed" ||
		fixture.Uncertain || fixture.ReconciliationRequired || !fixture.MetadataObserved || fixture.ContentIncluded {
		t.Fatalf("invalid bounded Forge execution evidence: %#v", fixture)
	}
	if fixture.Authority != (forgeRunExecutionEvidenceAuthority{}) {
		t.Fatalf("Forge execution evidence authority must remain disabled: %#v", fixture.Authority)
	}
}

func TestForgeRunExecutionEvidenceRejectsUnknownContentAndDuplicateFields(t *testing.T) {
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(forgeRunExecutionEvidenceFixture, &shape); err != nil {
		t.Fatal(err)
	}
	shape["output"] = json.RawMessage(`"raw output must not cross the observer boundary"`)
	unknown, err := json.Marshal(shape)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeForgeRunExecutionEvidence(unknown); err == nil {
		t.Fatal("Forge execution evidence decoder accepted unknown content")
	}

	rootDuplicate := strings.TrimSpace(string(forgeRunExecutionEvidenceFixture))
	rootDuplicate = strings.TrimSuffix(rootDuplicate, "}") + `,"run_id":"run-001"}`
	if _, err := decodeForgeRunExecutionEvidence([]byte(rootDuplicate)); err == nil {
		t.Fatal("Forge execution evidence decoder accepted a duplicate root field")
	}
	nestedDuplicate := strings.Replace(
		string(forgeRunExecutionEvidenceFixture),
		"    \"audit_published\": false\n  }",
		"    \"audit_published\": false,\n    \"audit_published\": false\n  }",
		1,
	)
	if nestedDuplicate == string(forgeRunExecutionEvidenceFixture) {
		t.Fatal("nested duplicate mutation did not match fixture")
	}
	if _, err := decodeForgeRunExecutionEvidence([]byte(nestedDuplicate)); err == nil {
		t.Fatal("Forge execution evidence decoder accepted a duplicate nested field")
	}
}
