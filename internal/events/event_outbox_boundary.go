package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
	"github.com/aero-vault/aero-vault/internal/telemetry"
)

// This file is the relay-local admission bridge. It deliberately does not add
// a shared EventReader/platform interface: the claimed outbox row is already
// the durable fact, so the only external observation needed for notify facts
// is the exact tenant/bucket Space binding. Deleted@1.1 facts are audit facts
// and are never subjected to Space membership or binding admission.
var (
	errOutboxFactInvalid   = errors.New("event outbox fact is inconsistent")
	errOutboxBoundaryFault = errors.New("event outbox resource boundary fault")
	errOutboxPolicyDenied  = errors.New("event outbox notify policy denied")
)

const (
	eventOutboxNotifyPolicyReason = "notify policy refused: Space-bound resource"
	eventOutboxTenantPolicyReason = "notify policy refused: tenant unavailable"
)

// outboxBoundary adapts the frozen shared deny-only admission contract to the
// relay. It is not an authorization grant: an unbound observation permits the
// caller's existing notification policy, while a bound observation or a known
// non-active tenant is an explicit policy refusal. Missing, malformed or
// failing observations remain faults and are never treated as unbound.
type outboxBoundary struct {
	boundary *ResourceBoundary
}

func newOutboxBoundary(reader spaces.BindingReader) *outboxBoundary {
	return &outboxBoundary{boundary: NewResourceBoundary(reader)}
}

func newOutboxBoundaryFromRepository(repo repository.Repository) *outboxBoundary {
	boundary, err := NewResourceBoundaryFromRepository(repo)
	if err != nil {
		return nil
	}
	return &outboxBoundary{boundary: boundary}
}

// WithResourceBoundary injects the raw binding reader captured before durable
// wrappers are installed. An explicit nil or typed-nil reader remains a fault
// at delivery time; it is never treated as an unbound observation.
func (r *EventOutboxRelay) WithResourceBoundary(reader spaces.BindingReader) *EventOutboxRelay {
	if r == nil {
		return r
	}
	r.boundary = newOutboxBoundary(reader)
	return r
}

func (b *outboxBoundary) check(ctx context.Context, tenant, bucket string) error {
	if b == nil || b.boundary == nil {
		return newOutboxBoundaryFault(errors.New("missing binding reader"))
	}
	err := b.boundary.CheckBucket(ctx, tenant, bucket)
	switch {
	case err == nil:
		return nil
	case IsResourceBoundaryFault(err):
		return newOutboxBoundaryFault(err)
	case IsResourceBoundaryDenied(err):
		return outboxPolicyDenied(err)
	default:
		return newOutboxBoundaryFault(err)
	}
}

func (r *EventOutboxRelay) admitNotify(ctx context.Context, meta notifyPayloadMeta) error {
	if r == nil || r.boundary == nil {
		return newOutboxBoundaryFault(errors.New("missing binding reader"))
	}
	return r.boundary.check(ctx, meta.Tenant, meta.Bucket)
}

func isOutboxPolicyDenied(err error) bool {
	return errors.Is(err, errOutboxPolicyDenied)
}

// The public text is deliberately safe; Unwrap retains the diagnostic chain
// for internal classification without stringifying raw causes at the edge.
type outboxBoundaryObservationFault struct{ cause error }

func newOutboxBoundaryFault(cause error) error {
	return &outboxBoundaryObservationFault{cause: cause}
}

func (*outboxBoundaryObservationFault) Error() string { return errOutboxBoundaryFault.Error() }
func (e *outboxBoundaryObservationFault) Unwrap() error {
	return e.cause
}
func (*outboxBoundaryObservationFault) Is(target error) bool {
	return target == errOutboxBoundaryFault
}

func outboxPolicyDenied(cause error) error {
	return fmt.Errorf("%w: %w", errOutboxPolicyDenied, cause)
}

// failNotifyPolicy persists a policy refusal as a terminal failed fact using
// the same owner/token fencing as ordinary retries. Passing fact.Attempts as
// the max-attempts argument makes RetryEventOutbox's existing predicate take
// the terminal branch without fabricating a delivery/complete transition.
func (r *EventOutboxRelay) failNotifyPolicy(fact repository.EventOutboxRow, cause error) {
	reason := eventOutboxNotifyPolicyReason
	if errors.Is(cause, spaces.ErrTenantUnavailable) {
		reason = eventOutboxTenantPolicyReason
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.httpTimeout)
	err := r.repo.RetryEventOutbox(ctx, fact.ID, fact.ClaimOwner, fact.ClaimToken,
		reason, time.Now().UTC(), fact.Attempts)
	cancel()
	if err != nil {
		r.logger.Warn("event outbox notify policy persistence failed", "fact", fact.ID, "err", err)
		telemetry.IncEventOutboxClaimLost(context.Background())
		return
	}
	telemetry.IncEventOutboxFailed(context.Background())
	r.logger.Warn("event outbox notify refused by Space policy",
		"fact", fact.ID, "tenant", fact.TenantID, "reason", reason)
}

func handleNotifyAdmissionFailure(r *EventOutboxRelay, fact repository.EventOutboxRow, err error) {
	if isOutboxPolicyDenied(err) {
		r.failNotifyPolicy(fact, err)
		return
	}
	r.retry(fact, err)
}

// ── Claimed fact validation ─────────────────────────────────────────────────

// validateOutboxNotifyFact validates the complete claimed notify fact and
// returns the exact scope used for rule resolution and boundary admission.
// Validation is pure: it never rereads the object or relies on the object
// still existing after its delete transaction.
func validateOutboxNotifyFact(fact repository.EventOutboxRow) (notifyPayloadMeta, error) {
	if err := ValidateClaimedOutboxFact(fact); err != nil {
		return notifyPayloadMeta{}, fmt.Errorf("%w: %w", errOutboxFactInvalid, err)
	}
	meta, err := parseNotifyPayload(fact.Payload)
	if err != nil {
		return notifyPayloadMeta{}, fmt.Errorf("%w: %w", errOutboxFactInvalid, err)
	}
	if err := spaces.ValidateBindingInput(meta.Tenant, meta.Bucket); err != nil {
		return notifyPayloadMeta{}, fmt.Errorf("%w: invalid notify scope: %w", errOutboxFactInvalid, err)
	}
	return meta, nil
}

// validateOutboxDeletedFact validates row/payload consistency only. A
// deleted@1.1 fact is an audit fact: it carries no Space membership check and
// remains deliverable after the source object and its event row are gone.
func validateOutboxDeletedFact(fact repository.EventOutboxRow) error {
	if err := ValidateClaimedOutboxFact(fact); err != nil {
		return fmt.Errorf("%w: %w", errOutboxFactInvalid, err)
	}
	var payload deletedFact
	if err := json.Unmarshal(fact.Payload, &payload); err != nil {
		return fmt.Errorf("%w: decode deleted scope: %w", errOutboxFactInvalid, err)
	}
	if err := spaces.ValidateBindingInput(payload.Tenant, payload.Bucket); err != nil {
		return fmt.Errorf("%w: invalid deleted scope: %w", errOutboxFactInvalid, err)
	}
	return nil
}
