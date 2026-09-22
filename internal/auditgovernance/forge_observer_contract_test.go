package auditgovernance

import (
	"bytes"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

// This is a standalone copy of Catalyst's canonical observer value. The test
// is deliberately receiver-side and contract-only: Vault does not publish,
// enqueue, authorize, or schedule Forge work from this evidence.
//
//go:embed testdata/forge-run-observed-v1.json
var forgeRunObservedFixture []byte

type forgeRunObservedContract struct {
	APIVersion       string                    `json:"api_version"`
	OwnerRef         string                    `json:"owner_ref"`
	ConversationID   string                    `json:"conversation_id"`
	RunID            string                    `json:"run_id"`
	PromptID         string                    `json:"prompt_id"`
	CreatedAtMS      int64                     `json:"created_at_ms"`
	LatestSequence   int64                     `json:"latest_sequence"`
	Status           string                    `json:"status"`
	MetadataObserved bool                      `json:"metadata_observed"`
	ContentIncluded  bool                      `json:"content_included"`
	Authority        forgeRunObservedAuthority `json:"authority"`
}

type forgeRunObservedAuthority struct {
	IdentityVerified          bool `json:"identity_verified"`
	OwnerAuthorized           bool `json:"owner_authorized"`
	RunAuthoritative          bool `json:"run_authoritative"`
	PersistenceAttested       bool `json:"persistence_attested"`
	ContentProvenanceVerified bool `json:"content_provenance_verified"`
	ReservationCreated        bool `json:"reservation_created"`
	ExecutionAuthorized       bool `json:"execution_authorized"`
	DispatchPerformed         bool `json:"dispatch_performed"`
}

func decodeForgeRunObserved(raw []byte) (forgeRunObservedContract, error) {
	if err := rejectForgeDuplicateJSONKeys(raw); err != nil {
		return forgeRunObservedContract{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var fixture forgeRunObservedContract
	if err := decoder.Decode(&fixture); err != nil {
		return forgeRunObservedContract{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return forgeRunObservedContract{}, io.ErrUnexpectedEOF
		}
		return forgeRunObservedContract{}, err
	}
	return fixture, nil
}

func rejectForgeDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := scanForgeJSONValue(decoder); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	return nil
}

func scanForgeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			keyText, ok := key.(string)
			if !ok {
				return fmt.Errorf("Forge observer object key is not a string")
			}
			if _, duplicate := seen[keyText]; duplicate {
				return fmt.Errorf("duplicate Forge observer object key %q", keyText)
			}
			seen[keyText] = struct{}{}
			if err := scanForgeJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return fmt.Errorf("Forge observer object did not close")
		}
	case '[':
		for decoder.More() {
			if err := scanForgeJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return fmt.Errorf("Forge observer array did not close")
		}
	case '}', ']':
		return fmt.Errorf("unexpected Forge observer delimiter %q", delim)
	}
	return nil
}

func TestForgeRunObservedCanonicalFixture(t *testing.T) {
	fixture, err := decodeForgeRunObserved(forgeRunObservedFixture)
	if err != nil {
		t.Fatalf("decode Forge observer fixture: %v", err)
	}
	if fixture.APIVersion != "forge.run.observed.v1" || fixture.Status != "nonterminal" {
		t.Fatalf("unexpected Forge observer identity: %#v", fixture)
	}
	if len(fixture.OwnerRef) != 64 {
		t.Fatalf("owner_ref length = %d, want SHA-256 hex", len(fixture.OwnerRef))
	}
	if _, err := hex.DecodeString(fixture.OwnerRef); err != nil {
		t.Fatalf("owner_ref is not SHA-256 hex: %v", err)
	}
	for name, value := range map[string]string{
		"conversation_id": fixture.ConversationID,
		"run_id":          fixture.RunID,
		"prompt_id":       fixture.PromptID,
	} {
		if strings.TrimSpace(value) == "" {
			t.Fatalf("%s is empty", name)
		}
	}
	if fixture.CreatedAtMS < 0 || fixture.LatestSequence <= 0 || !fixture.MetadataObserved || fixture.ContentIncluded {
		t.Fatalf("invalid Forge observer bounds: %#v", fixture)
	}
	if fixture.Authority != (forgeRunObservedAuthority{}) {
		t.Fatalf("Forge observer evidence must not grant authority: %#v", fixture.Authority)
	}
}

func TestForgeRunObservedRejectsUnknownContentAndDuplicateFields(t *testing.T) {
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(forgeRunObservedFixture, &shape); err != nil {
		t.Fatal(err)
	}
	shape["prompt"] = json.RawMessage(`"raw prompt must not cross the observer boundary"`)
	unknown, err := json.Marshal(shape)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeForgeRunObserved(unknown); err == nil {
		t.Fatal("Forge observer decoder accepted unknown content")
	}

	rootDuplicate := strings.TrimSpace(string(forgeRunObservedFixture))
	rootDuplicate = strings.TrimSuffix(rootDuplicate, "}") + `,"api_version":"forge.run.observed.v1"}`
	if _, err := decodeForgeRunObserved([]byte(rootDuplicate)); err == nil {
		t.Fatal("Forge observer decoder accepted a duplicate root field")
	}
	nestedDuplicate := strings.Replace(
		string(forgeRunObservedFixture),
		"    \"dispatch_performed\": false\n  }",
		"    \"dispatch_performed\": false,\n    \"dispatch_performed\": false\n  }",
		1,
	)
	if nestedDuplicate == string(forgeRunObservedFixture) {
		t.Fatal("nested duplicate mutation did not match fixture")
	}
	if _, err := decodeForgeRunObserved([]byte(nestedDuplicate)); err == nil {
		t.Fatal("Forge observer decoder accepted a duplicate nested field")
	}
}
