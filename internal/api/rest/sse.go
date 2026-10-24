package rest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/aero-vault/aero-vault/internal/events"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

// SSEHandler streams object lifecycle events as text/event-stream so browsers
// and Agent clients can react in real time. Tenant-scoped: only events for
// the caller's tenant are sent. Replay history comes directly from durable
// storage; live events are reread from their persisted row. Every event is
// admitted against the deny-only resource boundary before any bytes are written.
type SSEHandler struct {
	bus       *events.Bus
	repo      repository.Repository
	logger    *slog.Logger
	boundary  *events.ResourceBoundary
	authority *events.EventAuthority
}

var (
	errSSEUnavailable = errors.New("event stream unavailable")
	errSSEWrite       = errors.New("event stream write failed")
)

func NewSSEHandler(bus *events.Bus, repo repository.Repository, logger *slog.Logger) *SSEHandler {
	if logger == nil {
		logger = slog.Default()
	}
	reader, _ := repo.(spaces.BindingReader)
	authority, _ := events.NewEventAuthority(repo)
	return &SSEHandler{
		bus: bus, repo: repo, logger: logger,
		boundary: events.NewResourceBoundary(reader), authority: authority,
	}
}

// WithResourceBoundary injects the raw deny-only binding reader. An explicit
// nil or typed-nil reader stays a fault; it never falls back to the
// constructor-discovered port.
func (h *SSEHandler) WithResourceBoundary(reader spaces.BindingReader) *SSEHandler {
	if h == nil {
		return nil
	}
	h.boundary = events.NewResourceBoundary(reader)
	return h
}

// Stream is the GET /v1/events/stream handler.
//
//	curl -N -H 'X-Aero-Tenant: acme' http://host/v1/events/stream
//	# (or in a browser EventSource)
//
// Supports `Last-Event-ID` for replay on reconnect: missed events between the
// reported id and the current head are flushed from the DB before queued live
// events are consumed.
func parseLastEventID(r *http.Request) int64 {
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return 0
}

func (h *SSEHandler) listEvents(ctx context.Context, tenant string, afterID int64, limit int) ([]repository.Event, error) {
	if h == nil || h.repo == nil {
		return nil, errSSEUnavailable
	}
	return h.repo.ListEventsAfter(ctx, tenant, afterID, limit)
}

// validateReplayEvent rejects a repository page that crosses tenant scope or
// regresses the cursor. Such a page is not authoritative replay history.
func (h *SSEHandler) validateReplayEvent(tenant string, afterID int64, e repository.Event) error {
	if e.TenantID != tenant {
		return sseBoundaryFault("event replay tenant mismatch")
	}
	if e.ID <= afterID {
		return sseBoundaryFault("event replay identity regression")
	}
	return nil
}

// admitEvent checks the exact event tenant/bucket. Missing capabilities,
// malformed rows and observation faults stop the stream; an unbound result
// only preserves the caller's existing policy.
func (h *SSEHandler) admitEvent(ctx context.Context, tenant string, e repository.Event) error {
	if h == nil || h.boundary == nil {
		return sseBoundaryFault("missing resource boundary")
	}
	if e.TenantID != tenant {
		return sseBoundaryFault("event tenant mismatch")
	}
	if err := h.boundary.CheckBucket(ctx, e.TenantID, e.Bucket); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", events.ErrResourceBoundaryFault, err)
	}
	return nil
}

// preflightReplay admits the persisted cursor and the first undelivered event
// before any SSE header is written. A refusal or fault therefore remains a
// normal HTTP error instead of a healthy 200 stream that fails immediately.
func (h *SSEHandler) preflightReplay(ctx context.Context, tenant string, lastID int64) error {
	if lastID <= 0 {
		return nil
	}
	if err := h.admitCursor(ctx, tenant, lastID); err != nil {
		return err
	}
	rows, err := h.listEvents(ctx, tenant, lastID, 1)
	if err != nil {
		return fmt.Errorf("event replay preflight: %w", err)
	}
	if len(rows) > 1 {
		return sseBoundaryFault("invalid event replay page")
	}
	if len(rows) == 0 {
		return nil
	}
	if err := h.validateReplayEvent(tenant, lastID, rows[0]); err != nil {
		return err
	}
	return h.admitEvent(ctx, tenant, rows[0])
}

func (h *SSEHandler) admitCursor(ctx context.Context, tenant string, lastID int64) error {
	rows, err := h.listEvents(ctx, tenant, lastID-1, 1)
	if err != nil {
		return fmt.Errorf("event replay preflight: %w", err)
	}
	if len(rows) > 1 {
		return sseBoundaryFault("invalid event replay page")
	}
	if len(rows) == 0 || rows[0].ID != lastID {
		return nil
	}
	if err := h.validateReplayEvent(tenant, lastID-1, rows[0]); err != nil {
		return err
	}
	return h.admitEvent(ctx, tenant, rows[0])
}

// replayMissed flushes the authoritative persisted backlog. It returns the last
// successfully written event id and the fault that stopped replay, so Stream
// never falls through to a healthy live stream after an unsafe continuation.
func (h *SSEHandler) replayMissed(w http.ResponseWriter, flusher http.Flusher, r *http.Request, tenant string, lastID int64) (int64, error) {
	if lastID <= 0 {
		return lastID, nil
	}
	const pageSize = 200
	afterID := lastID
	for {
		backlog, err := h.listEvents(r.Context(), tenant, afterID, pageSize)
		if err != nil {
			h.logger.Warn("event replay failed", "tenant", tenant, "after_id", afterID, "error_class", "persistence")
			return afterID, fmt.Errorf("event replay: %w", err)
		}
		for _, e := range backlog {
			if err := h.validateReplayEvent(tenant, afterID, e); err != nil {
				h.logger.Warn("event replay rejected", "tenant", tenant, "after_id", afterID, "error_class", "authority")
				return afterID, err
			}
			if err := h.admitEvent(r.Context(), tenant, e); err != nil {
				h.logger.Warn("event replay rejected", "tenant", tenant, "after_id", afterID, "error_class", "boundary")
				return afterID, err
			}
			if !writeEvent(w, flusher, e) {
				return afterID, errSSEWrite
			}
			afterID = e.ID
		}
		if len(backlog) < pageSize {
			return afterID, nil
		}
	}
}

// authoritativeEvent rereads the exact persisted row before a live event is
// delivered. The caller's channel value is only a wakeup; forged or mismatched
// fields never reach writeEvent.
func (h *SSEHandler) authoritativeEvent(ctx context.Context, e repository.Event) (repository.Event, error) {
	if h == nil || h.authority == nil {
		return repository.Event{}, sseBoundaryFault("missing event authority")
	}
	authoritative, err := h.authority.Reread(ctx, e)
	if err != nil {
		return repository.Event{}, err
	}
	if err := ctx.Err(); err != nil {
		return repository.Event{}, fmt.Errorf("%w: %w", events.ErrResourceBoundaryFault, err)
	}
	return authoritative, nil
}

func (h *SSEHandler) liveStream(
	w http.ResponseWriter,
	r *http.Request,
	flusher http.Flusher,
	tenant string,
	sub <-chan repository.Event,
	lastSentID int64,
) {
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-sub:
			if !ok {
				return
			}
			if e.TenantID != tenant || e.ID <= lastSentID {
				continue
			}
			authoritative, err := h.authoritativeEvent(r.Context(), e)
			if err != nil {
				h.logger.Warn("event live delivery rejected", "tenant", tenant, "error_class", "authority")
				return
			}
			if err := h.admitEvent(r.Context(), tenant, authoritative); err != nil {
				h.logger.Warn("event live delivery rejected", "tenant", tenant, "error_class", "boundary")
				return
			}
			if !writeEvent(w, flusher, authoritative) {
				return
			}
			lastSentID = authoritative.ID
		case <-keepalive.C:
			if _, err := fmt.Fprintf(w, ": keepalive %d\n\n", time.Now().Unix()); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (h *SSEHandler) Stream(w http.ResponseWriter, r *http.Request) {
	if h.bus == nil {
		http.Error(w, "events disabled", http.StatusServiceUnavailable)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	tenant := mw.TenantFrom(r.Context())
	lastID := parseLastEventID(r)
	if err := h.preflightReplay(r.Context(), tenant, lastID); err != nil {
		h.writePreflightError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	sub, cancel := h.bus.Subscribe()
	defer cancel()
	lastSentID, err := h.replayMissed(w, flusher, r, tenant, lastID)
	if err != nil {
		if !errors.Is(err, errSSEWrite) {
			h.logger.Warn("event stream stopped after replay fault", "tenant", tenant, "error_class", "replay")
		}
		return
	}
	h.liveStream(w, r, flusher, tenant, sub, lastSentID)
}

// writePreflightError keeps pre-header failures sanitized. Boundary faults win
// over nested NotFound/Denied causes; deadlines retain the existing 504 policy.
func (h *SSEHandler) writePreflightError(w http.ResponseWriter, r *http.Request, err error) {
	if _, message, status, ok := classifyBoundary(err); ok {
		http.Error(w, message, status)
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		http.Error(w, "request timed out", http.StatusGatewayTimeout)
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	http.Error(w, errSSEUnavailable.Error(), http.StatusInternalServerError)
}

func sseBoundaryFault(detail string) error {
	return fmt.Errorf("%w: %s", events.ErrResourceBoundaryFault, detail)
}

func writeEvent(w http.ResponseWriter, flusher http.Flusher, e repository.Event) bool {
	payload := map[string]any{
		"id":         e.ID,
		"tenant":     e.TenantID,
		"bucket":     e.Bucket,
		"key":        e.Key,
		"type":       string(e.Type),
		"object_id":  e.ObjectID,
		"created_at": e.CreatedAt.Format(time.RFC3339Nano),
	}
	body, _ := json.Marshal(payload)
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Type, string(body)); err != nil {
		return false
	}
	flusher.Flush()
	return true
}
