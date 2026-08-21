package billing

import "time"

const (
	ScopeEntitlementRead = "billing:entitlement:read"
	ScopeMeteringWrite   = "metering:write"

	pathEntitlement  = "/api/v1/metering/entitlement"
	pathUsage        = "/api/v1/metering/usage"
	pathReservations = "/api/v1/metering/reservations"
)

type limitGrant struct {
	Soft      int64 `json:"soft"`
	Hard      int64 `json:"hard"`
	Unlimited bool  `json:"unlimited,omitempty"`
}

type entitlementSnapshot struct {
	TenantID    string                `json:"tenant_id"`
	Revision    uint64                `json:"revision"`
	Active      bool                  `json:"active"`
	Features    map[string]bool       `json:"features"`
	Limits      map[string]limitGrant `json:"limits"`
	EffectiveAt time.Time             `json:"effective_at"`
	ExpiresAt   time.Time             `json:"expires_at,omitempty"`
}

type entitlementEnvelope struct {
	Entitlement entitlementSnapshot `json:"entitlement"`
}

type usageRequest struct {
	ID         string            `json:"id"`
	Dimension  string            `json:"dimension"`
	Quantity   int64             `json:"quantity"`
	OccurredAt time.Time         `json:"occurred_at"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

type reservationRequest struct {
	ID         string `json:"id,omitempty"`
	Dimension  string `json:"dimension"`
	Quantity   int64  `json:"quantity"`
	TTLSeconds int64  `json:"ttl_seconds"`
}

type reservationEnvelope struct {
	Reservation struct {
		ID string `json:"id"`
	} `json:"reservation"`
}

type commitRequest struct {
	FactID   string            `json:"fact_id,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type apiError struct {
	Status int
	Code   string
}

func (e *apiError) Error() string {
	return "snaplink billing request failed: status=" + statusText(e.Status) + " code=" + e.Code
}
