package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

// DeterministicFactID derives the B3-3 audit-governance fact ID:
// hex(SHA-256(frame))[:32] — the first 32 hex chars of the hex digest
// (128-bit), lowercase. The frame is the canonical byte string of the six
// formula inputs in fixed order with NUL separators:
//
//	source \x00 tenant \x00 eventType \x00 originKind \x00
//	decimal(originID) \x00 decimal(unixSeconds(occurredBucket))
//
// where occurredBucket = occurredAt.UTC().Truncate(time.Second). NUL cannot
// occur in any field (source is "aero-vault." + base64url, tenant/action are
// constrained by the redactor's normalizers (tenantSourceID rejects control
// chars, safeAction restricts to [a-z0-9._:-]), the rest are decimal digits),
// so the framing is unambiguous.
//
// Pure: no randomness, no mutable state, no clock — identical inputs yield
// identical output in any process, any restart. It is the single definition
// of the campaign formula, applied by the repository write methods after
// origin assignment (store-authoritative) and by factFromGap (B3.3 / T-4).
func DeterministicFactID(
	source, tenant, eventType, originKind string,
	originID int64, occurredAt time.Time,
) string {
	bucket := occurredAt.UTC().Truncate(time.Second).Unix()
	frame := source + "\x00" + tenant + "\x00" + eventType + "\x00" + originKind +
		"\x00" + strconv.FormatInt(originID, 10) + "\x00" + strconv.FormatInt(bucket, 10)
	sum := sha256.Sum256([]byte(frame))
	return hex.EncodeToString(sum[:])[:32]
}
