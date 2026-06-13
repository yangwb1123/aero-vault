package rest

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/aero-vault/aero-vault/internal/events"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
)

// SSEHandler streams object lifecycle events as text/event-stream so browsers
// and Agent clients can react in real time. Tenant-scoped: only events for
// the caller's tenant are sent.
type SSEHandler struct {
	bus    *events.Bus
	repo   repository.Repository
	logger *slog.Logger
}

func NewSSEHandler(bus *events.Bus, repo repository.Repository, logger *slog.Logger) *SSEHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &SSEHandler{bus: bus, repo: repo, logger: logger}
}

// Stream is the GET /v1/events/stream handler.
//
//	curl -N -H 'X-Aero-Tenant: acme' http://host/v1/events/stream
//	# (or in a browser EventSource)
//
// Supports `Last-Event-ID` for replay on reconnect: missed events between the
// reported id and the current head are flushed from the DB before the live
// subscription starts.
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
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	// Replay missed events.
	var lastID int64
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			lastID = n
		}
	}
	if lastID > 0 {
		if backlog, err := h.repo.NextUnconsumedEvents(r.Context(), 200); err == nil {
			for _, e := range backlog {
				if e.ID <= lastID || e.TenantID != tenant {
					continue
				}
				if !writeEvent(w, flusher, e) {
					return
				}
			}
		}
	}

	// Subscribe to live events.
	sub := h.bus.Subscribe()
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
			if e.TenantID != tenant {
				continue
			}
			if !writeEvent(w, flusher, e) {
				return
			}
		case <-keepalive.C:
			// SSE comment line keeps the connection through L7 proxies.
			if _, err := fmt.Fprintf(w, ": keepalive %d\n\n", time.Now().Unix()); err != nil {
				return
			}
			flusher.Flush()
		}
	}
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
