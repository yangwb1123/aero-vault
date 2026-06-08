package rest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/aero-vault/aero-vault/internal/auth"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

// AdminHandler exposes runtime control surfaces:
//
//	GET    /v1/usage                 — current tenant's quota & usage
//	PUT    /v1/admin/tenants/{t}/quota   {"max_bytes":..., "max_objects":...}
//	GET    /v1/admin/keys
//	POST   /v1/admin/keys             {"token","tenant","scopes"}
//	DELETE /v1/admin/keys/{token}
//	POST   /v1/admin/jwt              {"sub","tenant","scopes","ttl_seconds"}
//
// All /v1/admin/* routes require the admin scope; the auth middleware sees
// these on PUT/POST/DELETE/GET and enforces accordingly.
type AdminHandler struct {
	svc  *service.FileService
	repo repository.Repository
	reg  *auth.Registry
}

func NewAdminHandler(svc *service.FileService, repo repository.Repository, reg *auth.Registry) *AdminHandler {
	return &AdminHandler{svc: svc, repo: repo, reg: reg}
}

// GET /v1/usage — any authenticated tenant sees its own row.
func (h *AdminHandler) Usage(w http.ResponseWriter, r *http.Request) {
	q, err := h.svc.Usage(r.Context(), mw.TenantFrom(r.Context()))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorPayload{Code: "InternalError", Message: err.Error()}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant":       q.TenantID,
		"used_bytes":   q.UsedBytes,
		"used_objects": q.UsedObjects,
		"max_bytes":    q.MaxBytes,
		"max_objects":  q.MaxObjects,
		"updated_at":   q.UpdatedAt,
	})
}

// PUT /v1/admin/tenants/{t}/quota
func (h *AdminHandler) SetQuota(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	tenant := chiURLParam(r, "tenant")
	var body struct {
		MaxBytes   int64 `json:"max_bytes"`
		MaxObjects int64 `json:"max_objects"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: errorPayload{Code: "InvalidArgument", Message: fmt.Sprintf("bad JSON: %v", err)}})
		return
	}
	if err := h.svc.SetQuota(r.Context(), tenant, body.MaxBytes, body.MaxObjects); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorPayload{Code: "InternalError", Message: err.Error()}})
		return
	}
	h.audit(r, "quota.set", tenant, fmt.Sprintf("max_bytes=%d max_objects=%d", body.MaxBytes, body.MaxObjects))
	writeJSON(w, http.StatusOK, map[string]any{"tenant": tenant, "max_bytes": body.MaxBytes, "max_objects": body.MaxObjects})
}

// PUT /v1/admin/tenants/{t}/budget — body {"daily_budget_usd": <float>}; 0 clears
// the override so the global AI_TENANT_DAILY_BUDGET_USD default applies again.
func (h *AdminHandler) SetBudget(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	tenant := chiURLParam(r, "tenant")
	var body struct {
		DailyBudgetUSD float64 `json:"daily_budget_usd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: errorPayload{Code: "InvalidArgument", Message: fmt.Sprintf("bad JSON: %v", err)}})
		return
	}
	if body.DailyBudgetUSD < 0 {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: errorPayload{Code: "InvalidArgument", Message: "daily_budget_usd must be >= 0"}})
		return
	}
	micros := int64(body.DailyBudgetUSD * 1_000_000)
	if err := h.repo.SetTenantBudgetMicros(r.Context(), tenant, micros); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorPayload{Code: "InternalError", Message: err.Error()}})
		return
	}
	h.audit(r, "budget.set", tenant, fmt.Sprintf("daily_budget_usd=%g", body.DailyBudgetUSD))
	writeJSON(w, http.StatusOK, map[string]any{"tenant": tenant, "daily_budget_usd": body.DailyBudgetUSD})
}

// GET /v1/admin/keys
func (h *AdminHandler) ListKeys(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	out := h.reg.ListKeys(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

// POST /v1/admin/keys
func (h *AdminHandler) AddKey(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	var body struct {
		Token   string   `json:"token"`
		Tenant  string   `json:"tenant"`
		Scopes  []string `json:"scopes"`
		Expires string   `json:"expires,omitempty"` // RFC3339; "" = no expiry (persisted keys)
		Label   string   `json:"label,omitempty"`   // human-readable hint (persisted keys)
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: errorPayload{Code: "InvalidArgument", Message: err.Error()}})
		return
	}
	if body.Token == "" || body.Tenant == "" || len(body.Scopes) == 0 {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: errorPayload{Code: "InvalidArgument", Message: "token, tenant, scopes required"}})
		return
	}
	k := auth.Key{Token: body.Token, Tenant: body.Tenant, Scopes: map[auth.Scope]bool{}}
	for _, s := range body.Scopes {
		k.Scopes[auth.Scope(s)] = true
	}
	if err := h.reg.AddKey(r.Context(), k, body.Expires, body.Label); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorPayload{Code: "InternalError", Message: err.Error()}})
		return
	}
	h.audit(r, "key.add", body.Tenant, fmt.Sprintf("token=%s scopes=%v", redactToken(body.Token), body.Scopes))
	writeJSON(w, http.StatusCreated, map[string]any{"tenant": body.Tenant, "scopes": body.Scopes})
}

// DELETE /v1/admin/keys/{token}
func (h *AdminHandler) RevokeKey(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	tok := chiURLParam(r, "token")
	revoked, err := h.reg.RevokeKey(r.Context(), tok)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorPayload{Code: "InternalError", Message: err.Error()}})
		return
	}
	if !revoked {
		writeJSON(w, http.StatusNotFound, errorBody{Error: errorPayload{Code: "NotFound", Message: "no such key"}})
		return
	}
	h.audit(r, "key.revoke", redactToken(tok), "")
	w.WriteHeader(http.StatusNoContent)
}

// POST /v1/admin/jwt — issue a signed JWT for a delegated principal.
func (h *AdminHandler) IssueJWT(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	if h.reg.JWT() == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: errorPayload{Code: "Unavailable", Message: "JWT not configured"}})
		return
	}
	var body struct {
		Sub        string   `json:"sub"`
		Tenant     string   `json:"tenant"`
		Scopes     []string `json:"scopes"`
		TTLSeconds int      `json:"ttl_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: errorPayload{Code: "InvalidArgument", Message: err.Error()}})
		return
	}
	if body.Tenant == "" || len(body.Scopes) == 0 {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: errorPayload{Code: "InvalidArgument", Message: "tenant + scopes required"}})
		return
	}
	tok, err := h.reg.JWT().Sign(struct {
		Sub    string
		Tenant string
		Scopes []string
		TTL    time.Duration
	}{Sub: body.Sub, Tenant: body.Tenant, Scopes: body.Scopes, TTL: time.Duration(body.TTLSeconds) * time.Second})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorPayload{Code: "InternalError", Message: err.Error()}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": tok, "tenant": body.Tenant, "scopes": body.Scopes, "ttl_seconds": body.TTLSeconds})
}

// PUT /v1/buckets/{bucket}/lifecycle  {"days":30,"action":"soft_delete"}
func (h *AdminHandler) PutBucketLifecycle(w http.ResponseWriter, r *http.Request) {
	bucket := chiURLParam(r, "bucket")
	var req struct {
		Days   int    `json:"days"`
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: errorPayload{Code: "InvalidArgument", Message: err.Error()}})
		return
	}
	if err := h.svc.SetBucketLifecycle(r.Context(), mw.TenantFrom(r.Context()), bucket, req.Days, req.Action); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorPayload{Code: "InternalError", Message: err.Error()}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": req.Days, "action": req.Action})
}

// GET /v1/admin/webhook-failures
func (h *AdminHandler) ListWebhookFailures(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := h.repo.ListWebhookFailures(r.Context(), limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorPayload{Code: "InternalError", Message: err.Error()}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"failures": rows})
}

// POST /v1/admin/tenants — body {tenant_id, display_name}; creates or updates a
// tenant record (status defaults to 'active'). 201 with the stored record.
func (h *AdminHandler) CreateTenant(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	var body struct {
		TenantID    string `json:"tenant_id"`
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: errorPayload{Code: "InvalidArgument", Message: err.Error()}})
		return
	}
	if body.TenantID == "" {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: errorPayload{Code: "InvalidArgument", Message: "tenant_id required"}})
		return
	}
	tr := repository.TenantRecord{TenantID: body.TenantID, DisplayName: body.DisplayName, Status: "active"}
	if err := h.repo.UpsertTenant(r.Context(), tr); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorPayload{Code: "InternalError", Message: err.Error()}})
		return
	}
	rec, _, err := h.repo.GetTenant(r.Context(), body.TenantID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorPayload{Code: "InternalError", Message: err.Error()}})
		return
	}
	h.audit(r, "tenant.create", body.TenantID, body.DisplayName)
	writeJSON(w, http.StatusCreated, tenantJSON(rec))
}

// GET /v1/admin/tenants — 200 {"tenants":[...]}.
func (h *AdminHandler) ListTenants(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	recs, err := h.repo.ListTenants(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorPayload{Code: "InternalError", Message: err.Error()}})
		return
	}
	out := make([]map[string]any, 0, len(recs))
	for _, rec := range recs {
		out = append(out, tenantJSON(rec))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenants": out})
}

// DELETE /v1/admin/tenants/{tenant} — 204; 404 if not found.
func (h *AdminHandler) DeleteTenant(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	tenant := chiURLParam(r, "tenant")
	deleted, err := h.repo.DeleteTenant(r.Context(), tenant)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorPayload{Code: "InternalError", Message: err.Error()}})
		return
	}
	if !deleted {
		writeJSON(w, http.StatusNotFound, errorBody{Error: errorPayload{Code: "NotFound", Message: "no such tenant"}})
		return
	}
	h.audit(r, "tenant.delete", tenant, "")
	w.WriteHeader(http.StatusNoContent)
}

// PUT /v1/admin/tenants/{tenant}/status — body {status:"active"|"disabled"}; 200.
func (h *AdminHandler) SetTenantStatus(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	tenant := chiURLParam(r, "tenant")
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: errorPayload{Code: "InvalidArgument", Message: err.Error()}})
		return
	}
	if body.Status != "active" && body.Status != "disabled" {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: errorPayload{Code: "InvalidArgument", Message: "status must be active or disabled"}})
		return
	}
	rec, found, err := h.repo.GetTenant(r.Context(), tenant)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorPayload{Code: "InternalError", Message: err.Error()}})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody{Error: errorPayload{Code: "NotFound", Message: "no such tenant"}})
		return
	}
	rec.Status = body.Status
	if err := h.repo.UpsertTenant(r.Context(), rec); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorPayload{Code: "InternalError", Message: err.Error()}})
		return
	}
	h.audit(r, "tenant.status", tenant, body.Status)
	writeJSON(w, http.StatusOK, tenantJSON(rec))
}

// tenantJSON renders a TenantRecord as the wire shape returned by the admin
// tenant endpoints.
func tenantJSON(rec repository.TenantRecord) map[string]any {
	return map[string]any{
		"tenant_id":    rec.TenantID,
		"display_name": rec.DisplayName,
		"status":       rec.Status,
		"created_at":   rec.CreatedAt,
	}
}

// audit records an admin/security action best-effort: it resolves the actor
// from the authenticated principal (the key's tenant when present, else "")
// and writes an audit-log entry. Any error is intentionally swallowed —
// auditing must never break or fail the underlying admin action.
func (h *AdminHandler) audit(r *http.Request, action, target, detail string) {
	actor := ""
	if k, ok := auth.FromContext(r.Context()); ok {
		actor = k.Tenant
	}
	_ = h.repo.RecordAudit(r.Context(), repository.AuditEntry{
		Actor:    actor,
		Action:   action,
		Target:   target,
		TenantID: target,
		Detail:   detail,
	})
}

// ListAudit returns the most recent audit-log entries, newest first.
//
//	GET /v1/admin/audit?limit=N
func (h *AdminHandler) ListAudit(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := h.repo.ListAudit(r.Context(), limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorPayload{Code: "InternalError", Message: err.Error()}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit": rows})
}

// redactToken masks a raw API-key token for audit-log storage, keeping a short
// suffix so an operator can correlate it without persisting the secret.
func redactToken(tok string) string {
	if len(tok) <= 4 {
		return "****"
	}
	return "****" + tok[len(tok)-4:]
}

// requireAdmin gates admin routes when auth is enabled. Without auth, the
// caller is implicitly admin (mirrors the no-auth MVP behaviour).
func (h *AdminHandler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !h.reg.Enabled() {
		return true
	}
	k, ok := auth.FromContext(r.Context())
	if !ok || !k.Has(auth.ScopeAdmin) {
		writeJSON(w, http.StatusForbidden, errorBody{Error: errorPayload{Code: "Forbidden", Message: "admin scope required"}})
		return false
	}
	return true
}
