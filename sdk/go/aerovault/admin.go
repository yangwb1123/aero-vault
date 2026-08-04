package aerovault

import (
	"context"
	"net/http"
	"strconv"
)

// ── Server info ─────────────────────────────────────────────────────────────

func (c *Client) Usage(ctx context.Context) (*Usage, error) {
	r, err := c.newRequest(ctx, http.MethodGet, "/v1/usage", nil)
	if err != nil {
		return nil, err
	}
	var u Usage
	if err := c.doJSON(r, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

func (c *Client) Health(ctx context.Context) (bool, error) {
	r, err := c.newRequest(ctx, http.MethodGet, "/healthz", nil)
	if err != nil {
		return false, err
	}
	resp, err := c.httpClient.Do(r)
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK, nil
}

// ── API Key management ──────────────────────────────────────────────────────

func (c *Client) AddKey(ctx context.Context, req AddKeyRequest) (map[string]any, error) {
	body, jOpt, err := jsonBody(req)
	if err != nil {
		return nil, err
	}
	r, err := c.newRequest(ctx, http.MethodPost, "/v1/admin/keys", body, jOpt)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := c.doJSON(r, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ListKeys(ctx context.Context) ([]APIKey, error) {
	r, err := c.newRequest(ctx, http.MethodGet, "/v1/admin/keys", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Keys []APIKey `json:"keys"`
	}
	if err := c.doJSON(r, &out); err != nil {
		return nil, err
	}
	return out.Keys, nil
}

func (c *Client) RevokeKey(ctx context.Context, token string) error {
	r, err := c.newRequest(ctx, http.MethodDelete, "/v1/admin/keys/"+token, nil)
	if err != nil {
		return err
	}
	return c.doJSON(r, nil)
}

func (c *Client) IssueJWT(ctx context.Context, req IssueJWTRequest) (*IssueJWTResponse, error) {
	body, jOpt, err := jsonBody(req)
	if err != nil {
		return nil, err
	}
	r, err := c.newRequest(ctx, http.MethodPost, "/v1/admin/jwt", body, jOpt)
	if err != nil {
		return nil, err
	}
	var out IssueJWTResponse
	if err := c.doJSON(r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ── Admin operations ────────────────────────────────────────────────────────

func (c *Client) ListWebhookFailures(ctx context.Context) ([]WebhookFailure, error) {
	r, err := c.newRequest(ctx, http.MethodGet, "/v1/admin/webhook-failures", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Failures []WebhookFailure `json:"failures"`
	}
	if err := c.doJSON(r, &out); err != nil {
		return nil, err
	}
	return out.Failures, nil
}

func (c *Client) ListJobs(ctx context.Context) (map[string]any, error) {
	r, err := c.newRequest(ctx, http.MethodGet, "/v1/admin/jobs", nil)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := c.doJSON(r, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) RetryJob(ctx context.Context, jobID int64) (map[string]any, error) {
	r, err := c.newRequest(ctx, http.MethodPost, "/v1/admin/jobs/"+strconv.FormatInt(jobID, 10)+"/retry", nil)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := c.doJSON(r, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ── Tenant management ───────────────────────────────────────────────────────

func (c *Client) CreateTenant(ctx context.Context, tenantID, displayName string) (*TenantRecord, error) {
	body, jOpt, err := jsonBody(map[string]string{"tenant_id": tenantID, "display_name": displayName})
	if err != nil {
		return nil, err
	}
	r, err := c.newRequest(ctx, http.MethodPost, "/v1/admin/tenants", body, jOpt)
	if err != nil {
		return nil, err
	}
	var out TenantRecord
	if err := c.doJSON(r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListTenants(ctx context.Context) ([]TenantRecord, error) {
	r, err := c.newRequest(ctx, http.MethodGet, "/v1/admin/tenants", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Tenants []TenantRecord `json:"tenants"`
	}
	if err := c.doJSON(r, &out); err != nil {
		return nil, err
	}
	return out.Tenants, nil
}

func (c *Client) DeleteTenant(ctx context.Context, tenantID string) error {
	r, err := c.newRequest(ctx, http.MethodDelete, "/v1/admin/tenants/"+tenantID, nil)
	if err != nil {
		return err
	}
	return c.doJSON(r, nil)
}

func (c *Client) SetTenantStatus(ctx context.Context, tenantID, status string) (*TenantRecord, error) {
	body, jOpt, err := jsonBody(map[string]string{"status": status})
	if err != nil {
		return nil, err
	}
	r, err := c.newRequest(ctx, http.MethodPut, "/v1/admin/tenants/"+tenantID+"/status", body, jOpt)
	if err != nil {
		return nil, err
	}
	var out TenantRecord
	if err := c.doJSON(r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListAudit(ctx context.Context, limit int, before string) ([]AuditEntry, error) {
	path := "/v1/admin/audit"
	params := map[string]string{}
	if limit > 0 {
		params["limit"] = strconv.Itoa(limit)
	}
	if before != "" {
		params["before"] = before
	}
	r, err := c.newRequest(ctx, http.MethodGet, path, nil, withQuery(params))
	if err != nil {
		return nil, err
	}
	var out struct {
		Entries []AuditEntry `json:"entries"`
	}
	if err := c.doJSON(r, &out); err != nil {
		return nil, err
	}
	return out.Entries, nil
}

func (c *Client) SetQuota(ctx context.Context, tenantID string, maxBytes, maxObjects int64) (map[string]any, error) {
	body, jOpt, err := jsonBody(map[string]any{"max_bytes": maxBytes, "max_objects": maxObjects})
	if err != nil {
		return nil, err
	}
	r, err := c.newRequest(ctx, http.MethodPut, "/v1/admin/tenants/"+tenantID+"/quota", body, jOpt)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := c.doJSON(r, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) SetBudget(ctx context.Context, tenantID string, dailyUSD float64) (map[string]any, error) {
	body, jOpt, err := jsonBody(map[string]float64{"daily_budget_usd": dailyUSD})
	if err != nil {
		return nil, err
	}
	r, err := c.newRequest(ctx, http.MethodPut, "/v1/admin/tenants/"+tenantID+"/budget", body, jOpt)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := c.doJSON(r, &out); err != nil {
		return nil, err
	}
	return out, nil
}
