package rest

import (
	"net/http"
	"strconv"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// jobDTO is the JSON view of a job; zero timestamps are omitted.
type jobDTO struct {
	ID          int64      `json:"id"`
	Tenant      string     `json:"tenant"`
	Type        string     `json:"type"`
	Payload     string     `json:"payload,omitempty"`
	Status      string     `json:"status"`
	Priority    int        `json:"priority,omitempty"`
	Attempts    int        `json:"attempts"`
	MaxAttempts int        `json:"max_attempts"`
	RunAfter    *time.Time `json:"run_after,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
	Worker      string     `json:"worker,omitempty"`
	Result      string     `json:"result,omitempty"`
	CreatedAt   *time.Time `json:"created_at,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

func toJobDTO(j repository.Job) jobDTO {
	d := jobDTO{
		ID: j.ID, Tenant: j.TenantID, Type: j.Type, Payload: j.Payload,
		Status: j.Status, Priority: j.Priority, Attempts: j.Attempts,
		MaxAttempts: j.MaxAttempts, LastError: j.LastError, Worker: j.Worker, Result: j.Result,
	}
	d.RunAfter = nonZeroTime(j.RunAfter)
	d.CreatedAt = nonZeroTime(j.CreatedAt)
	d.StartedAt = nonZeroTime(j.StartedAt)
	d.FinishedAt = nonZeroTime(j.FinishedAt)
	return d
}

func nonZeroTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// GET /v1/admin/jobs?status=&type=&limit=
// Returns recent jobs plus a status histogram for the dashboard.
func (h *AdminHandler) ListJobs(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	rows, err := h.repo.ListJobs(r.Context(), q.Get("status"), q.Get("type"), limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorPayload{Code: "InternalError", Message: err.Error()}})
		return
	}
	stats, err := h.repo.JobStats(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorPayload{Code: "InternalError", Message: err.Error()}})
		return
	}
	out := make([]jobDTO, 0, len(rows))
	for _, j := range rows {
		out = append(out, toJobDTO(j))
	}
	writeJSON(w, http.StatusOK, map[string]any{"stats": stats, "jobs": out})
}

// POST /v1/admin/jobs/{id}/retry — requeue a job (e.g. after permanent failure).
func (h *AdminHandler) RetryJob(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	id, err := strconv.ParseInt(chiURLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: errorPayload{Code: "InvalidArgument", Message: "id must be int"}})
		return
	}
	if err := h.repo.RetryJob(r.Context(), id, "manual retry", time.Now()); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorPayload{Code: "InternalError", Message: err.Error()}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": "pending"})
}
