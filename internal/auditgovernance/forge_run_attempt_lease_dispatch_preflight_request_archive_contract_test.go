package auditgovernance

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

// This receiver retains a bounded Run/Attempt/lease preflight input as an
// archival observation. It neither authenticates an instance, persists state,
// selects or reserves capacity, issues a lease, dispatches a Runner, executes
// work, nor grants Audit authority.

//go:embed testdata/forge-run-attempt-lease-dispatch-preflight-request-v1.json
var forgeRunAttemptLeaseDispatchPreflightRequestArchiveFixture []byte

type forgePreflightRequestArchive struct {
	Owner          forgePreflightOwner                      `json:"owner"`
	ConversationID string                                   `json:"conversation_id"`
	RunID          string                                   `json:"run_id"`
	RunStatus      string                                   `json:"run_status"`
	DispatchPlan   forgePreflightRequestArchiveDispatchPlan `json:"dispatch_plan"`
}

type forgePreflightRequestArchiveDispatchPlan struct {
	AttemptState string                                `json:"attempt_state"`
	Placement    forgePreflightRequestArchivePlacement `json:"placement_request"`
	Intent       forgePreflightRequestArchiveIntent    `json:"runner_execution_intent"`
	Lease        forgePreflightRequestArchiveLease     `json:"lease"`
}

type forgePreflightRequestArchivePlacement struct {
	SchemaVersion    string                                   `json:"schema_version"`
	EvaluatedAtMS    int64                                    `json:"evaluated_at_ms"`
	Owner            forgePreflightOwner                      `json:"owner"`
	MaxSnapshotAgeMS int64                                    `json:"max_snapshot_age_ms"`
	Requirements     forgePreflightRequestArchiveRequirements `json:"requirements"`
	Devices          []forgePreflightRequestArchiveDevice     `json:"devices"`
}

type forgePreflightRequestArchiveRequirements struct {
	OS                 string                                     `json:"os"`
	Architecture       string                                     `json:"architecture"`
	MinCPUCores        int64                                      `json:"min_cpu_cores"`
	MinMemoryBytes     int64                                      `json:"min_memory_bytes"`
	MinStorageBytes    int64                                      `json:"min_storage_bytes"`
	Runtime            string                                     `json:"runtime"`
	GPU                forgePreflightRequestArchiveRequirementGPU `json:"gpu"`
	DataResidencyZones []string                                   `json:"data_residency_zones"`
	MinimumTrustZone   string                                     `json:"minimum_trust_zone"`
	SandboxFloor       string                                     `json:"sandbox_floor"`
	ConcurrencySlots   int64                                      `json:"concurrency_slots"`
}

type forgePreflightRequestArchiveRequirementGPU struct {
	Required       bool   `json:"required"`
	MinMemoryBytes int64  `json:"min_memory_bytes"`
	Runtime        string `json:"runtime"`
}

type forgePreflightRequestArchiveDevice struct {
	DeviceID              string                                `json:"device_id"`
	Owner                 forgePreflightOwner                   `json:"owner"`
	ApprovalState         string                                `json:"approval_state"`
	CordonState           string                                `json:"cordon_state"`
	Liveness              string                                `json:"liveness"`
	SnapshotObservedAtMS  int64                                 `json:"snapshot_observed_at_ms"`
	LeaseExpiresAtMS      int64                                 `json:"lease_expires_at_ms"`
	OS                    string                                `json:"os"`
	Architecture          string                                `json:"architecture"`
	AvailableCPUCores     int64                                 `json:"available_cpu_cores"`
	AvailableMemoryBytes  int64                                 `json:"available_memory_bytes"`
	AvailableStorageBytes int64                                 `json:"available_storage_bytes"`
	Runtimes              []string                              `json:"runtimes"`
	GPU                   forgePreflightRequestArchiveDeviceGPU `json:"gpu"`
	DataResidencyZones    []string                              `json:"data_residency_zones"`
	TrustZone             string                                `json:"trust_zone"`
	SandboxLevels         []string                              `json:"sandbox_levels"`
	ConcurrencyLimit      int64                                 `json:"concurrency_limit"`
	ActiveConcurrency     int64                                 `json:"active_concurrency"`
}

type forgePreflightRequestArchiveDeviceGPU struct {
	Present     bool   `json:"present"`
	MemoryBytes int64  `json:"memory_bytes"`
	Runtime     string `json:"runtime"`
}

type forgePreflightRequestArchiveIntent struct {
	SchemaVersion    string                                `json:"schema_version"`
	EvaluationMode   string                                `json:"evaluation_mode"`
	Owner            forgePreflightOwner                   `json:"owner"`
	ConversationID   string                                `json:"conversation_id"`
	PromptID         string                                `json:"prompt_id"`
	RunID            string                                `json:"run_id"`
	AttemptID        string                                `json:"attempt_id"`
	CommandID        string                                `json:"command_id"`
	TargetID         string                                `json:"target_id"`
	CommandSHA256    string                                `json:"command_sha256"`
	IdempotencyKey   string                                `json:"idempotency_key"`
	PromptRunBinding bool                                  `json:"prompt_run_binding_valid"`
	CommandBinding   bool                                  `json:"runner_command_binding_valid"`
	PreviewOnly      bool                                  `json:"preview_only"`
	SelectedTargetID *string                               `json:"selected_target_id"`
	Authority        forgePreflightRequestArchiveAuthority `json:"authority"`
}

type forgePreflightRequestArchiveAuthority struct {
	DeviceIdentityVerified bool `json:"device_identity_verified"`
	CommandPersisted       bool `json:"command_persisted"`
	ReservationCreated     bool `json:"reservation_created"`
	ExecutionAuthorized    bool `json:"execution_authorized"`
	DispatchPerformed      bool `json:"dispatch_performed"`
	AuditPublished         bool `json:"audit_published"`
}

type forgePreflightRequestArchiveLease struct {
	V            uint64 `json:"v"`
	AttemptID    string `json:"attempt_id"`
	TargetID     string `json:"target_id"`
	Epoch        uint64 `json:"epoch"`
	FencingToken string `json:"fencing_token"`
	IssuedAtMS   int64  `json:"issued_at_ms"`
	ExpiresAtMS  int64  `json:"expires_at_ms"`
}

func decodeForgeRunAttemptLeaseDispatchPreflightRequestArchive(raw []byte) (forgePreflightRequestArchive, error) {
	if err := rejectDuplicateForgePreflightJSONKeys(raw); err != nil {
		return forgePreflightRequestArchive{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value forgePreflightRequestArchive
	if err := decoder.Decode(&value); err != nil {
		return forgePreflightRequestArchive{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return forgePreflightRequestArchive{}, fmt.Errorf("preflight request has trailing JSON")
		}
		return forgePreflightRequestArchive{}, err
	}
	if err := validateForgeRunAttemptLeaseDispatchPreflightRequestArchive(value); err != nil {
		return forgePreflightRequestArchive{}, err
	}
	return value, nil
}

func validateForgeRunAttemptLeaseDispatchPreflightRequestArchive(value forgePreflightRequestArchive) error {
	expectedOwner := forgePreflightOwner{Issuer: "https://id.example", Subject: "user-1", TenantID: "tenant-1"}
	if value.Owner != expectedOwner || value.ConversationID != "conversation-001" || value.RunID != "run-001" || value.RunStatus != "nonterminal" || value.DispatchPlan.AttemptState != "accepted" {
		return fmt.Errorf("invalid preflight request owner or Run binding")
	}
	placement := value.DispatchPlan.Placement
	if placement.SchemaVersion != "forge.device-placement-dry-run/v1" || placement.EvaluatedAtMS != 200500 || placement.Owner != value.Owner || placement.MaxSnapshotAgeMS != 60000 || !validPlacementRequirements(placement.Requirements) || len(placement.Devices) != 2 {
		return fmt.Errorf("preflight placement crossed its archival boundary")
	}
	for _, device := range placement.Devices {
		if !validPlacementDevice(device, value.Owner) {
			return fmt.Errorf("preflight device declaration crossed its archival boundary")
		}
	}
	intent := value.DispatchPlan.Intent
	if intent.SchemaVersion != "forge.runner-execution-intent/v1" || intent.EvaluationMode != "pure_runner_binding_only" || intent.Owner != value.Owner || intent.ConversationID != value.ConversationID || intent.RunID != value.RunID || intent.PromptID != "prompt-001" || intent.AttemptID != "attempt-001" || intent.CommandID != "command-001" || intent.TargetID != "runner-1" || len(intent.CommandSHA256) != 64 || intent.IdempotencyKey != "run-001:attempt-001:command-001" || !intent.PromptRunBinding || !intent.CommandBinding || !intent.PreviewOnly || intent.SelectedTargetID != nil || intent.Authority != (forgePreflightRequestArchiveAuthority{}) {
		return fmt.Errorf("Runner intent crossed its archival boundary")
	}
	lease := value.DispatchPlan.Lease
	if lease.V != 1 || lease.AttemptID != intent.AttemptID || lease.TargetID != intent.TargetID || lease.Epoch != 1 || lease.FencingToken != "fence-001" || lease.IssuedAtMS != 199500 || lease.ExpiresAtMS != 205500 {
		return fmt.Errorf("lease crossed its archival boundary or drifted from intent")
	}
	return nil
}

func validPlacementRequirements(value forgePreflightRequestArchiveRequirements) bool {
	return value.OS == "linux" && value.Architecture == "amd64" && value.MinCPUCores == 4 && value.MinMemoryBytes == 8589934592 && value.MinStorageBytes == 21474836480 && value.Runtime == "oci" && !value.GPU.Required && value.GPU.MinMemoryBytes == 0 && value.GPU.Runtime == "" && len(value.DataResidencyZones) == 1 && value.DataResidencyZones[0] == "us-west" && value.MinimumTrustZone == "standard" && value.SandboxFloor == "container" && value.ConcurrencySlots == 1
}

func validPlacementDevice(value forgePreflightRequestArchiveDevice, owner forgePreflightOwner) bool {
	return value.DeviceID != "" && value.Owner == owner && value.ApprovalState == "approved" && value.CordonState == "clear" && value.Liveness == "online" && value.SnapshotObservedAtMS == 199500 && value.LeaseExpiresAtMS == 260500 && value.OS == "linux" && value.Architecture == "amd64" && value.AvailableCPUCores == 8 && value.AvailableMemoryBytes == 17179869184 && value.AvailableStorageBytes == 107374182400 && len(value.Runtimes) == 1 && value.Runtimes[0] == "oci" && !value.GPU.Present && value.GPU.MemoryBytes == 0 && value.GPU.Runtime == "" && len(value.DataResidencyZones) == 1 && value.DataResidencyZones[0] == "us-west" && value.TrustZone == "standard" && len(value.SandboxLevels) == 1 && value.SandboxLevels[0] == "container" && value.ConcurrencyLimit == 4 && value.ActiveConcurrency == 1
}

func TestForgePreflightRequestArchiveCanonical(t *testing.T) {
	value, err := decodeForgeRunAttemptLeaseDispatchPreflightRequestArchive(forgeRunAttemptLeaseDispatchPreflightRequestArchiveFixture)
	if err != nil {
		t.Fatal(err)
	}
	if value.DispatchPlan.Intent.TargetID != value.DispatchPlan.Lease.TargetID {
		t.Fatalf("lease target drifted from archival intent: %#v", value)
	}
}

func TestForgePreflightRequestArchiveRejectsEffectsAndBindings(t *testing.T) {
	base := string(forgeRunAttemptLeaseDispatchPreflightRequestArchiveFixture)
	mutations := map[string]string{
		"unknown":        strings.TrimSuffix(strings.TrimSpace(base), "}") + `,"unexpected":true}`,
		"nested_unknown": strings.Replace(base, `"concurrency_slots": 1`, `"concurrency_slots": 1,"unexpected":true`, 1),
		"duplicate":      strings.TrimSuffix(strings.TrimSpace(base), "}") + `,"run_id":"run-001"}`,
		"trailing":       strings.TrimSpace(base) + ` true`,
		"authority":      strings.Replace(base, `"dispatch_performed": false`, `"dispatch_performed": true`, 1),
		"selection":      strings.Replace(base, `"selected_target_id": null`, `"selected_target_id": "runner-1"`, 1),
		"lease_target":   strings.Replace(base, `"target_id": "runner-1"`, `"target_id": "runner-2"`, 1),
	}
	for name, raw := range mutations {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeForgeRunAttemptLeaseDispatchPreflightRequestArchive([]byte(raw)); err == nil {
				t.Fatalf("invalid %s mutation accepted", name)
			}
		})
	}
}
