package rest

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
)

const (
	maxAccountSummaryQueryBytes = 8 << 10
	maxAccountSummaryValueBytes = 512
)

var vaultAccountDatasets = map[string]struct{}{
	"aero-vault.tenants": {},
	"aero-vault.buckets": {},
	"aero-vault.usage":   {},
}

type vaultAccountSummaryRequest struct {
	AccountID    string
	CanonicalUID string
	TenantID     string
	Region       string
	Datasets     []string
}

type vaultAccountSummaryResponse struct {
	SourceRegion string           `json:"source_region"`
	Version      int64            `json:"version"`
	GeneratedAt  time.Time        `json:"generated_at"`
	Complete     bool             `json:"complete"`
	Datasets     map[string]any   `json:"datasets"`
	Sources      []map[string]any `json:"sources"`
	Memberships  []map[string]any `json:"memberships"`
}

// AccountSummary exposes the bounded, read-only Aero ID source contract.
// It is intentionally restricted to Snaplink client_credentials principals;
// browser access tokens cannot use this tenant-level aggregation surface.
func (h *AdminHandler) AccountSummary(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Vary", "Authorization, X-Aero-Tenant")
	principal, ok := access.PrincipalFrom(r.Context())
	if !ok || principal.Kind != access.PrincipalService {
		h.writeAccountSummaryError(w, r, http.StatusForbidden, "Forbidden", "machine credentials required")
		return
	}
	request, ok := parseVaultAccountSummaryRequest(r, principal)
	if !ok {
		h.writeAccountSummaryError(w, r, http.StatusBadRequest, "InvalidArgument", "invalid account summary request")
		return
	}
	region := vaultAccountSourceRegion()
	if request.Region != "" && request.Region != region {
		h.writeAccountSummaryError(w, r, http.StatusBadRequest, "InvalidArgument", "requested region is not served")
		return
	}
	if h.repo == nil {
		h.writeAccountSummaryError(w, r, http.StatusServiceUnavailable, "Unavailable", "account summary source unavailable")
		return
	}

	buckets, err := h.repo.ListBuckets(r.Context(), request.TenantID)
	if err != nil {
		h.writeAccountSummaryError(w, r, http.StatusServiceUnavailable, "Unavailable", "account summary source unavailable")
		return
	}
	quota, err := h.repo.GetTenantQuota(r.Context(), request.TenantID)
	if err != nil {
		h.writeAccountSummaryError(w, r, http.StatusServiceUnavailable, "Unavailable", "account summary source unavailable")
		return
	}
	sort.Strings(buckets)
	now := time.Now().UTC()
	version := quota.UpdatedAt.UTC().UnixMilli()
	if version < 1 {
		version = now.UnixMilli()
	}
	datasets := make(map[string]any, len(request.Datasets))
	for _, dataset := range request.Datasets {
		switch dataset {
		case "aero-vault.tenants":
			datasets[dataset] = map[string]any{"items": []map[string]any{{
				"id": request.TenantID, "status": "active",
			}}}
		case "aero-vault.buckets":
			datasets[dataset] = map[string]any{"items": buckets}
		case "aero-vault.usage":
			datasets[dataset] = map[string]any{
				"tenant_id": request.TenantID, "used_bytes": quota.UsedBytes,
				"used_objects": quota.UsedObjects, "max_bytes": quota.MaxBytes,
				"max_objects": quota.MaxObjects,
			}
		}
	}
	detail, _ := json.Marshal(map[string]any{
		"account_id": request.AccountID, "datasets": request.Datasets,
	})
	if err := h.repo.RecordAudit(r.Context(), repository.AuditEntry{
		Actor: principal.SubjectID, Action: "integration.account_summary.read",
		Target: request.CanonicalUID, TenantID: request.TenantID, Detail: string(detail),
	}); err != nil {
		h.writeAccountSummaryError(w, r, http.StatusServiceUnavailable, "Unavailable", "account summary audit unavailable")
		return
	}

	writeJSON(w, http.StatusOK, vaultAccountSummaryResponse{
		SourceRegion: region, Version: version, GeneratedAt: now, Complete: true,
		Datasets: datasets,
		Sources: []map[string]any{{
			"source_account_id": request.CanonicalUID, "scope_type": "tenant",
			"scope_id": request.TenantID, "status": "active", "data": map[string]any{},
		}},
		Memberships: []map[string]any{{
			"scope_type": "tenant", "scope_id": request.TenantID,
			"source_member_id": request.CanonicalUID, "role": "member",
			"status": "active", "data": map[string]any{},
		}},
	})
}

func (h *AdminHandler) writeAccountSummaryError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: errorPayload{
		Code: code, Message: message, RequestID: mw.RequestIDFrom(r.Context()),
	}})
}

func parseVaultAccountSummaryRequest(r *http.Request, principal access.Principal) (vaultAccountSummaryRequest, bool) {
	if r == nil || len(r.URL.RawQuery) > maxAccountSummaryQueryBytes {
		return vaultAccountSummaryRequest{}, false
	}
	accountID := strings.TrimSpace(r.URL.Query().Get("account_id"))
	canonicalUID := strings.TrimSpace(r.Header.Get("X-Aero-Canonical-UID"))
	tenantID := mw.TenantFrom(r.Context())
	if _, present := mw.TenantFromContext(r.Context()); !present && principal.TenantID != "" {
		tenantID = principal.TenantID
	}
	assertedTenant := strings.TrimSpace(r.Header.Get("X-Aero-Tenant-ID"))
	region := strings.TrimSpace(r.URL.Query().Get("region"))
	if !validAccountSummaryValue(accountID) || !validAccountSummaryValue(canonicalUID) ||
		!validAccountSummaryValue(tenantID) || len(region) > maxAccountSummaryValueBytes ||
		containsControl(region) || (principal.TenantID != "" && principal.TenantID != tenantID) ||
		(assertedTenant != "" && assertedTenant != tenantID) {
		return vaultAccountSummaryRequest{}, false
	}
	datasets, ok := requestedVaultAccountDatasets(r.URL.Query())
	if !ok {
		return vaultAccountSummaryRequest{}, false
	}
	return vaultAccountSummaryRequest{
		AccountID: accountID, CanonicalUID: canonicalUID, TenantID: tenantID,
		Region: region, Datasets: datasets,
	}, true
}

func requestedVaultAccountDatasets(query url.Values) ([]string, bool) {
	requested := query["dataset"]
	if len(requested) == 0 {
		requested = make([]string, 0, len(vaultAccountDatasets))
		for dataset := range vaultAccountDatasets {
			requested = append(requested, dataset)
		}
	}
	if len(requested) > len(vaultAccountDatasets) {
		return nil, false
	}
	seen := make(map[string]struct{}, len(requested))
	result := make([]string, 0, len(requested))
	for _, dataset := range requested {
		if _, allowed := vaultAccountDatasets[dataset]; !allowed {
			return nil, false
		}
		if _, duplicate := seen[dataset]; !duplicate {
			seen[dataset] = struct{}{}
			result = append(result, dataset)
		}
	}
	sort.Strings(result)
	return result, true
}

func validAccountSummaryValue(value string) bool {
	return value != "" && len(value) <= maxAccountSummaryValueBytes && !containsControl(value)
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}

func vaultAccountSourceRegion() string {
	if region := strings.TrimSpace(os.Getenv("AERO_VAULT_ACCOUNT_SOURCE_REGION")); region != "" {
		return region
	}
	return "local"
}
