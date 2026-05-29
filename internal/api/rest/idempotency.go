package rest

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/telemetry"
)

// idempotency returns middleware that makes a write request carrying an
// Idempotency-Key header safe to retry: a replayed request returns the original
// response verbatim instead of re-executing the handler (so a retried PUT does
// not create a duplicate version). It is opt-in and inert for reads and for
// requests without the header.
//
// Semantics (Stripe-style):
//   - First request with a given (tenant, key): claim it, run the handler,
//     capture the response, and store it — unless the status is 5xx, in which
//     case the claim is released so a retry can re-run (transient failures are
//     not memoized).
//   - Replay of a completed key with the same request fingerprint: the stored
//     response is returned with an Idempotency-Replayed: true header.
//   - Same key, different request (fingerprint mismatch), or a request still
//     in progress: 409 Conflict.
//   - Idempotency store error: fail closed with 500 (never risk a silent
//     duplicate write).
func idempotency(repo repository.Repository, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get("Idempotency-Key")
			if key == "" || !isWriteMethod(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			ctx := r.Context()
			tenant := mw.TenantFrom(ctx)
			reqID := mw.RequestIDFrom(ctx)
			fp := fingerprint(r)

			rec, claimed, err := repo.ClaimIdempotencyKey(ctx, tenant, key, fp, reqID)
			if err != nil {
				logger.Warn("idempotency claim failed", "err", err, "key", key, "tenant", tenant)
				writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorPayload{
					Code: "InternalError", Message: "idempotency store unavailable", RequestID: reqID,
				}})
				return
			}

			if !claimed {
				switch {
				case rec.Fingerprint != fp:
					idemConflict(w, reqID, "Idempotency-Key reused for a different request")
				case rec.Status == "completed":
					telemetry.IncIdempotencyReplay(ctx)
					replay(w, rec)
				default: // in_progress
					idemConflict(w, reqID, "a request with this Idempotency-Key is already in progress")
				}
				return
			}

			// We own the claim. Capture the response while streaming it to the
			// client, then persist it (or release the claim on failure).
			cap := &idemCapture{ResponseWriter: w, status: http.StatusOK}
			done := false
			defer func() {
				// Panic path: the handler blew up before we committed. Release
				// the claim so a retry can re-run, then let the panic propagate
				// to the global Recoverer.
				if !done {
					if delErr := repo.DeleteIdempotencyKey(ctx, tenant, key); delErr != nil {
						logger.Warn("idempotency release (panic) failed", "err", delErr, "key", key, "tenant", tenant)
					}
				}
			}()

			next.ServeHTTP(cap, r)
			done = true

			if cap.status >= 500 {
				if delErr := repo.DeleteIdempotencyKey(ctx, tenant, key); delErr != nil {
					logger.Warn("idempotency release failed", "err", delErr, "key", key, "tenant", tenant)
				}
				return
			}
			if cmpErr := repo.CompleteIdempotencyKey(ctx, tenant, key, cap.status, cap.body, cap.Header().Get("Content-Type")); cmpErr != nil {
				logger.Warn("idempotency complete failed", "err", cmpErr, "key", key, "tenant", tenant)
			}
		})
	}
}

// idemCapture is an http.ResponseWriter that passes writes through to the real
// client while capturing the status and body so they can be memoized.
type idemCapture struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	body        []byte
}

func (c *idemCapture) WriteHeader(status int) {
	if c.wroteHeader {
		return
	}
	c.status = status
	c.wroteHeader = true
	c.ResponseWriter.WriteHeader(status)
}

func (c *idemCapture) Write(b []byte) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}
	c.body = append(c.body, b...)
	return c.ResponseWriter.Write(b)
}

func replay(w http.ResponseWriter, rec repository.IdempotencyRecord) {
	if rec.ResponseCT != "" {
		w.Header().Set("Content-Type", rec.ResponseCT)
	}
	w.Header().Set("Idempotency-Replayed", "true")
	w.WriteHeader(rec.ResponseStatus)
	_, _ = w.Write(rec.ResponseBody)
}

func idemConflict(w http.ResponseWriter, reqID, msg string) {
	writeJSON(w, http.StatusConflict, errorBody{Error: errorPayload{
		Code: "IdempotencyConflict", Message: msg, RequestID: reqID,
	}})
}

// fingerprint identifies the request so the same key reused for a different
// method/path is rejected. Bodies are not hashed (uploads are not re-readable),
// so this guards against same-key/different-target, not same-key/different-bytes.
func fingerprint(r *http.Request) string {
	sum := sha256.Sum256([]byte(r.Method + " " + r.URL.Path))
	return hex.EncodeToString(sum[:])
}

func isWriteMethod(m string) bool {
	switch m {
	case http.MethodPut, http.MethodPost, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}
