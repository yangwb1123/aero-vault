package auditgovernance

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestReceiptConflictMismatchedIdentityIsInvalid(t *testing.T) {
	fact := repository.AuditGovernanceFact{
		ID: "fact-1", TenantID: "acme", FactKind: "admin",
		Action: "tenant.status", OccurredAt: time.Now().UTC(),
	}
	for _, tc := range []struct {
		name, eventID, tenantID string
	}{
		{name: "event id", eventID: "other-fact", tenantID: "acme"},
		{name: "tenant", eventID: "fact-1", tenantID: "other-tenant"},
		{name: "both", eventID: "other-fact", tenantID: "other-tenant"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := receiptResponse(fmt.Sprintf(
				`{"receipt":{"event_id":%q,"tenant_id":%q,"status":"ledgered","accepted_at":"2026-08-04T00:00:00Z","conflict":true}}`,
				tc.eventID, tc.tenantID))
			err := validateReceipt(response, fact)
			if !errors.Is(err, ErrInvalidReceipt) {
				t.Fatalf("error = %v, want ErrInvalidReceipt", err)
			}
			if errors.Is(err, ErrReceiptConflict) {
				t.Fatal("mismatched conflict was classified as a true conflict")
			}
		})
	}
}

func TestReceiptBodyReadErrorRemainsTransient(t *testing.T) {
	fact := repository.AuditGovernanceFact{
		ID: "fact-1", TenantID: "acme", FactKind: "file",
		Action: "file.created", OccurredAt: time.Now().UTC(),
	}
	response := receiptResponseBody(&failingReceiptBody{
		payload: `{"receipt":{"event_id":"fact-1"}}`,
	})
	err := validateReceipt(response, fact)
	if err == nil || err.Error() != "receipt body interrupted" {
		t.Fatalf("error = %v, want transport read error", err)
	}
	if isPermanentDeliveryError(err) {
		t.Fatal("transport read error was classified as permanent")
	}
}

func receiptResponse(body string) *http.Response {
	return receiptResponseBody(io.NopCloser(strings.NewReader(body)))
}

func receiptResponseBody(body io.ReadCloser) *http.Response {
	return &http.Response{
		StatusCode: http.StatusAccepted,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       body,
	}
}

type failingReceiptBody struct {
	payload string
	emitted bool
}

func (r *failingReceiptBody) Read(p []byte) (int, error) {
	if r.emitted {
		return 0, errors.New("receipt body interrupted")
	}
	r.emitted = true
	n := copy(p, r.payload)
	return n, errors.New("receipt body interrupted")
}

func (r *failingReceiptBody) Close() error { return nil }
