package rest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/ai"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

// AIHandler exposes /v1/search, /v1/lineage, /v1/chat, /v1/chat/stream,
// /v1/agent on top of the FileService's repository.
type AIHandler struct {
	repo     repository.Repository
	search   *ai.Search
	chat     *ai.Chat
	agent    *ai.Agent
	logger   *slog.Logger
	degraded bool // when true, all AI endpoints return 503 immediately
}

func NewAIHandler(repo repository.Repository, search *ai.Search, chat *ai.Chat, agent *ai.Agent, logger *slog.Logger, degraded bool) *AIHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &AIHandler{repo: repo, search: search, chat: chat, agent: agent, logger: logger, degraded: degraded}
}

// aiDegraded checks whether AI is in degraded mode and returns early with 503.
func (h *AIHandler) aiDegraded(w http.ResponseWriter, r *http.Request) bool {
	if h.degraded {
		writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: errorPayload{Code: "Unavailable", Message: "AI service is temporarily unavailable (degraded mode)"}})
		return true
	}
	return false
}

// POST /v1/search
func (h *AIHandler) Search(w http.ResponseWriter, r *http.Request) {
	if h.aiDegraded(w, r) {
		return
	}
	if h.search == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: errorPayload{Code: "Unavailable", Message: "search disabled (no embedder configured)"}})
		return
	}
	var req searchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: errorPayload{Code: "InvalidArgument", Message: fmt.Sprintf("invalid JSON: %v", err)}})
		return
	}
	if req.Query == "" {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: errorPayload{Code: "InvalidArgument", Message: "query is required"}})
		return
	}
	if req.K == 0 {
		req.K = 10
	}
	hits, err := h.search.Query(r.Context(), ai.Request{
		Tenant: mw.TenantFrom(r.Context()),
		Bucket: req.Bucket,
		Query:  req.Query,
		K:      req.K,
		Mode:   req.Mode,
		Caller: "rest:search",
		ReqID:  mw.RequestIDFrom(r.Context()),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorPayload{Code: "InternalError", Message: err.Error()}})
		return
	}
	writeJSON(w, http.StatusOK, searchResponse{Query: req.Query, Hits: hits})
}

// POST /v1/chat/stream — SSE response. Token chunks use event:token and the
// final event:done frame carries the answer and citations together.
func (h *AIHandler) ChatStream(w http.ResponseWriter, r *http.Request) {
	if h.aiDegraded(w, r) {
		return
	}
	if h.chat == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: errorPayload{Code: "Unavailable", Message: "chat disabled"}})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	var req struct {
		Query  string `json:"query"`
		K      int    `json:"k,omitempty"`
		Mode   string `json:"mode,omitempty"`
		Bucket string `json:"bucket,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Query == "" {
		http.Error(w, "query required", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	streamCtx, cancel := context.WithCancel(r.Context())
	defer cancel()
	var writeFailed atomic.Bool
	resp, err := h.chat.AnswerStream(streamCtx, ai.ChatReq{
		Tenant: mw.TenantFrom(r.Context()), Bucket: req.Bucket,
		Query: req.Query, K: req.K, Mode: req.Mode,
		Caller: "rest:chat-stream", ReqID: mw.RequestIDFrom(r.Context()),
	}, func(chunk string) {
		if writeFailed.Load() || streamCtx.Err() != nil {
			return
		}
		// each token chunk is a JSON-encoded string so newlines stay safe.
		b, _ := json.Marshal(chunk)
		if _, writeErr := fmt.Fprintf(w, "event: token\ndata: %s\n\n", string(b)); writeErr != nil {
			writeFailed.Store(true)
			cancel()
			return
		}
		flusher.Flush()
	})
	if writeFailed.Load() || streamCtx.Err() != nil {
		return
	}
	if err != nil {
		if errors.Is(err, ai.ErrBudgetExceeded) {
			writeSSEError(w, flusher, "BudgetExceeded", "tenant AI budget exceeded")
		} else {
			writeSSEError(w, flusher, "InternalError", err.Error())
		}
		return
	}
	// Final frame: full answer + citations as JSON
	final, _ := json.Marshal(resp)
	if _, err := fmt.Fprintf(w, "event: done\ndata: %s\n\n", string(final)); err != nil {
		return
	}
	flusher.Flush()
}

// writeSSEError emits a structured SSE error frame after headers have been
// sent. The payload is a JSON object so clients can parse code and message
// independently. json.Marshal is used for the message field to ensure correct
// escaping of quotes and control characters.
func writeSSEError(w http.ResponseWriter, f http.Flusher, code, message string) {
	type sseErrPayload struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	body, _ := json.Marshal(sseErrPayload{Code: code, Message: message})
	fmt.Fprintf(w, "event: error\ndata: %s\n\n", string(body))
	f.Flush()
}

// POST /v1/agent — runs a tool-calling loop and returns the final answer.
func (h *AIHandler) Agent(w http.ResponseWriter, r *http.Request) {
	if h.aiDegraded(w, r) {
		return
	}
	if h.agent == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: errorPayload{Code: "Unavailable", Message: "agent disabled (no LLM configured)"}})
		return
	}
	var req struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Query == "" {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: errorPayload{Code: "InvalidArgument", Message: "query required"}})
		return
	}
	resp, err := h.agent.Run(r.Context(), ai.AgentReq{
		Tenant: mw.TenantFrom(r.Context()), Query: req.Query, ReqID: mw.RequestIDFrom(r.Context()),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorPayload{Code: "InternalError", Message: err.Error()}})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// POST /v1/chat
func (h *AIHandler) Chat(w http.ResponseWriter, r *http.Request) {
	if h.aiDegraded(w, r) {
		return
	}
	if h.chat == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: errorPayload{Code: "Unavailable", Message: "chat disabled (no LLM configured)"}})
		return
	}
	var req struct {
		Query       string           `json:"query"`
		Bucket      string           `json:"bucket,omitempty"`
		K           int              `json:"k,omitempty"`
		Mode        string           `json:"mode,omitempty"`
		Temperature float64          `json:"temperature,omitempty"`
		Prior       []ai.ChatMessage `json:"prior,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: errorPayload{Code: "InvalidArgument", Message: fmt.Sprintf("invalid JSON: %v", err)}})
		return
	}
	if req.Query == "" {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: errorPayload{Code: "InvalidArgument", Message: "query is required"}})
		return
	}
	resp, err := h.chat.Answer(r.Context(), ai.ChatReq{
		Tenant: mw.TenantFrom(r.Context()), Bucket: req.Bucket,
		Query: req.Query, K: req.K, Mode: req.Mode, Temperature: req.Temperature,
		Caller: "rest:chat", ReqID: mw.RequestIDFrom(r.Context()), Prior: req.Prior,
	})
	if err != nil {
		if errors.Is(err, ai.ErrBudgetExceeded) {
			writeJSON(w, http.StatusPaymentRequired, errorBody{Error: errorPayload{Code: "BudgetExceeded", Message: "tenant AI budget exceeded", RequestID: mw.RequestIDFrom(r.Context())}})
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorPayload{Code: "InternalError", Message: err.Error()}})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// GET /v1/lineage/objects/{id}
func (h *AIHandler) Lineage(w http.ResponseWriter, r *http.Request) {
	if h.aiDegraded(w, r) {
		return
	}
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: errorPayload{Code: "InvalidArgument", Message: "id must be int"}})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	usages, err := h.repo.ListUsageForObject(r.Context(), mw.TenantFrom(r.Context()), id, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorPayload{Code: "InternalError", Message: err.Error()}})
		return
	}
	entries := make([]lineageEntry, 0, len(usages))
	for _, u := range usages {
		entries = append(entries, lineageEntry{
			UsageID: u.ID, Caller: u.Caller, Query: u.Query,
			ChunkIDs: u.ChunkIDs, ObjectIDs: u.ObjectIDs,
			RequestID: u.RequestID, CreatedAt: u.CreatedAt,
			Model: u.Model, PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens,
			TotalTokens: u.TotalTokens, LatencyMs: u.LatencyMs, CostMicros: u.CostMicros,
		})
	}
	writeJSON(w, http.StatusOK, lineageResponse{ObjectID: id, Entries: entries})
}

// avoid unused warnings if service is referenced elsewhere in the package
var _ = service.DefaultBucket
