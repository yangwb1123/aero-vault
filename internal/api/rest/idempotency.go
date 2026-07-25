package rest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"os"

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
//
// hashBody (IDEMPOTENCY_HASH_BODY) extends the fingerprint with a SHA-256 of
// the request body (v2): the same key replayed with different bytes is
// rejected instead of replayed. The body is spooled — at most
// idemSpoolThreshold bytes in memory, beyond that in a temp file — and handed
// to the downstream handler unchanged; a body that cannot be read fails closed
// with 500 before any claim is taken.
func idempotency(repo repository.Repository, logger *slog.Logger, hashBody bool) func(http.Handler) http.Handler {
	h := &IdempotencyHandler{repo: repo, logger: logger, hashBody: hashBody}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key, ok := extractIdempotencyKey(r)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}

			ctx := r.Context()
			tenant := mw.TenantFrom(ctx)
			reqID := mw.RequestIDFrom(ctx)
			fp := fingerprint(r)

			if h.hashBody {
				sp, bodyHash, err := spoolBody(r)
				if err != nil {
					h.logger.Warn("idempotency body spool failed", "err", err, "key", key, "tenant", tenant)
					writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorPayload{
						Code: "InternalError", Message: "could not buffer request body", RequestID: reqID,
					}})
					return
				}
				defer sp.Close()
				fp = bodyFingerprint(r, bodyHash)
			}

			if h.handleIdempotentRequest(w, r, key, fp, tenant, reqID) {
				return
			}

			cap := &idemCapture{ResponseWriter: w, status: http.StatusOK}
			done := false
			defer func() {
				if !done {
					if delErr := h.repo.DeleteIdempotencyKey(ctx, tenant, key); delErr != nil {
						h.logger.Warn("idempotency release (panic) failed", "err", delErr, "key", key, "tenant", tenant)
					}
				}
			}()

			next.ServeHTTP(cap, r)
			done = true

			if cap.status >= 500 {
				if delErr := h.repo.DeleteIdempotencyKey(ctx, tenant, key); delErr != nil {
					h.logger.Warn("idempotency release failed", "err", delErr, "key", key, "tenant", tenant)
				}
				return
			}
			if cmpErr := h.repo.CompleteIdempotencyKey(ctx, tenant, key, cap.status, cap.body, cap.Header().Get("Content-Type"), replayableHeaders(cap.Header())); cmpErr != nil {
				h.logger.Warn("idempotency complete failed", "err", cmpErr, "key", key, "tenant", tenant)
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
	// Restore any handler-set headers (ETag, Last-Modified, Location,
	// X-Version-Id, X-Meta-*, …) so a replayed response is byte-for-byte what the
	// original sent, keeping cache validation and client-side metadata intact.
	for k, vals := range rec.ResponseHeaders {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Idempotency-Replayed", "true")
	w.WriteHeader(rec.ResponseStatus)
	_, _ = w.Write(rec.ResponseBody)
}

// replayableHeaders snapshots the handler's response headers for memoization,
// excluding ones that must not be replayed verbatim: Content-Type is stored
// separately, and Content-Length/Date are recomputed by the server on replay.
func replayableHeaders(h http.Header) map[string][]string {
	var out map[string][]string
	for k, vals := range h {
		switch http.CanonicalHeaderKey(k) {
		case "Content-Type", "Content-Length", "Date":
			continue
		}
		if out == nil {
			out = make(map[string][]string, len(h))
		}
		cp := make([]string, len(vals))
		copy(cp, vals)
		out[k] = cp
	}
	return out
}

func idemConflict(w http.ResponseWriter, reqID, msg string) {
	writeJSON(w, http.StatusConflict, errorBody{Error: errorPayload{
		Code: "IdempotencyConflict", Message: msg, RequestID: reqID,
	}})
}

// fingerprint identifies the request so the same key reused for a different
// method/path is rejected. Bodies are not part of this v1 fingerprint, so it
// guards against same-key/different-target, not same-key/different-bytes —
// bodyFingerprint covers that when IDEMPOTENCY_HASH_BODY is on.
func fingerprint(r *http.Request) string {
	sum := sha256.Sum256([]byte(r.Method + " " + r.URL.Path))
	return hex.EncodeToString(sum[:])
}

// bodyFingerprint is the v2 fingerprint: it folds the hex SHA-256 of the
// request body (computed by spoolBody) into the hash so the same key replayed
// with different bytes no longer matches and is rejected with 409.
func bodyFingerprint(r *http.Request, bodyHash string) string {
	sum := sha256.Sum256([]byte(r.Method + " " + r.URL.Path + " " + bodyHash))
	return hex.EncodeToString(sum[:])
}

// idemSpoolThreshold is the number of request-body bytes kept in memory before
// spoolBody switches to an on-disk temp file, so hashing a large upload never
// pins the whole payload in RAM (same bound as the WebDAV spillBuffer).
const idemSpoolThreshold = 8 << 20 // 8 MiB

// idemSpool buffers a request body that was consumed for fingerprint hashing
// so it can be replayed to the downstream handler: at most idemSpoolThreshold
// bytes in memory, anything larger in a temp file. Close releases the memory
// and removes the temp file; it is safe to call multiple times.
type idemSpool struct {
	mem  []byte
	file *os.File
}

func (s *idemSpool) Write(p []byte) (int, error) {
	if s.file == nil && len(s.mem)+len(p) > idemSpoolThreshold {
		f, err := os.CreateTemp("", "aero-idem-*")
		if err != nil {
			return 0, err
		}
		if _, err := f.Write(s.mem); err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
			return 0, err
		}
		s.file = f
		s.mem = nil
	}
	if s.file != nil {
		return s.file.Write(p)
	}
	s.mem = append(s.mem, p...)
	return len(p), nil
}

// reader rewinds the spool and returns a ReadCloser over exactly the buffered
// bytes. Its Close is a no-op (handlers and the server may close the request
// body); idemSpool.Close owns the temp file's lifetime.
func (s *idemSpool) reader() (io.ReadCloser, error) {
	if s.file != nil {
		if _, err := s.file.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		return io.NopCloser(s.file), nil
	}
	return io.NopCloser(bytes.NewReader(s.mem)), nil
}

func (s *idemSpool) Close() error {
	s.mem = nil
	if s.file == nil {
		return nil
	}
	f := s.file
	s.file = nil
	name := f.Name()
	err := f.Close()
	if rmErr := os.Remove(name); rmErr != nil && err == nil {
		err = rmErr
	}
	return err
}

// spoolBody consumes r.Body while hashing it with SHA-256, then swaps in a
// reader that replays the same bytes so the downstream handler sees an
// identical stream (ContentLength is left untouched). The caller must Close
// the returned spool by the end of the request — a deferred Close runs even
// on a handler panic. No or empty body hashes the empty byte string.
func spoolBody(r *http.Request) (*idemSpool, string, error) {
	h := sha256.New()
	sp := &idemSpool{}
	if r.Body != nil {
		if _, err := io.Copy(io.MultiWriter(h, sp), r.Body); err != nil {
			_ = sp.Close()
			return nil, "", err
		}
		_ = r.Body.Close()
		body, err := sp.reader()
		if err != nil {
			_ = sp.Close()
			return nil, "", err
		}
		r.Body = body
	}
	return sp, hex.EncodeToString(h.Sum(nil)), nil
}

type IdempotencyHandler struct {
	repo     repository.Repository
	logger   *slog.Logger
	hashBody bool
}

func extractIdempotencyKey(r *http.Request) (string, bool) {
	key := r.Header.Get("Idempotency-Key")
	return key, key != "" && isWriteMethod(r.Method)
}

func (h *IdempotencyHandler) handleIdempotentRequest(w http.ResponseWriter, r *http.Request, key, fp, tenant, reqID string) bool {
	ctx := r.Context()
	rec, claimed, err := h.repo.ClaimIdempotencyKey(ctx, tenant, key, fp, reqID)
	if err != nil {
		h.logger.Warn("idempotency claim failed", "err", err, "key", key, "tenant", tenant)
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorPayload{
			Code: "InternalError", Message: "idempotency store unavailable", RequestID: reqID,
		}})
		return true
	}
	if !claimed {
		switch {
		case rec.Fingerprint != fp:
			idemConflict(w, reqID, "Idempotency-Key reused for a different request")
		case rec.Status == "completed":
			telemetry.IncIdempotencyReplay(ctx)
			replay(w, rec)
		default:
			idemConflict(w, reqID, "a request with this Idempotency-Key is already in progress")
		}
		return true
	}
	return false
}

func isWriteMethod(m string) bool {
	switch m {
	case http.MethodPut, http.MethodPost, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}
