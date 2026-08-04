package aerovault

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"time"
)

type ShareRequest struct {
	Bucket        string `json:"bucket,omitempty"`
	Key           string `json:"key"`
	Name          string `json:"name,omitempty"`
	Password      string `json:"password,omitempty"`
	AllowPreview  bool   `json:"allow_preview,omitempty"`
	AllowDownload bool   `json:"allow_download,omitempty"`
	MaxUses       int64  `json:"max_uses,omitempty"`
	TTLSeconds    int64  `json:"ttl_seconds,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
}

type Share struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenant_id"`
	Bucket        string    `json:"bucket"`
	Key           string    `json:"key"`
	Name          string    `json:"name,omitempty"`
	AllowPreview  bool      `json:"allow_preview"`
	AllowDownload bool      `json:"allow_download"`
	MaxUses       int64     `json:"max_uses,omitempty"`
	UseCount      int64     `json:"use_count"`
	ExpiresAt     time.Time `json:"expires_at,omitempty"`
	CreatedBy     string    `json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`
}

type ShareLink struct {
	Share Share  `json:"share"`
	Token string `json:"token"`
	URL   string `json:"url"`
}

type PublishAssetRequest struct {
	Bucket       string `json:"bucket,omitempty"`
	Key          string `json:"key"`
	Slug         string `json:"slug"`
	CacheControl string `json:"cache_control,omitempty"`
}

type PublicAsset struct {
	ID           string    `json:"id"`
	TenantID     string    `json:"tenant_id"`
	Bucket       string    `json:"bucket"`
	Key          string    `json:"key"`
	Slug         string    `json:"slug"`
	CacheControl string    `json:"cache_control"`
	PublishedBy  string    `json:"published_by"`
	PublishedAt  time.Time `json:"published_at"`
}

type PublishedAsset struct {
	Asset PublicAsset `json:"asset"`
	URL   string      `json:"url"`
}

type ACLRequest struct {
	Bucket        string   `json:"bucket,omitempty"`
	Key           string   `json:"key,omitempty"`
	ResourceKind  string   `json:"resource_kind"`
	PrincipalType string   `json:"principal_type"`
	PrincipalID   string   `json:"principal_id,omitempty"`
	Actions       []string `json:"actions"`
	Effect        string   `json:"effect"`
	Inherit       bool     `json:"inherit,omitempty"`
}

type Department struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	ParentID  string    `json:"parent_id,omitempty"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type DepartmentMember struct {
	TenantID     string    `json:"tenant_id"`
	DepartmentID string    `json:"department_id"`
	SubjectID    string    `json:"subject_id"`
	Role         string    `json:"role"`
	CreatedAt    time.Time `json:"created_at"`
}

type DepartmentDetails struct {
	Department Department         `json:"department"`
	Members    []DepartmentMember `json:"members"`
}

type ResourceACLEntry struct {
	ID            string `json:"id"`
	TenantID      string `json:"tenant_id"`
	Bucket        string `json:"bucket"`
	Key           string `json:"key,omitempty"`
	ResourceKind  string `json:"resource_kind"`
	PrincipalType string `json:"principal_type"`
	PrincipalID   string `json:"principal_id,omitempty"`
	Action        string `json:"action"`
	Effect        string `json:"effect"`
	Inherit       bool   `json:"inherit"`
}

func (c *Client) CreateShare(ctx context.Context, request ShareRequest) (*ShareLink, error) {
	var out ShareLink
	err := c.postEnterpriseJSON(ctx, "/v1/shares", request, &out)
	return &out, err
}

func (c *Client) ListShares(ctx context.Context, bucket, key string) ([]Share, error) {
	request, err := c.newRequest(ctx, http.MethodGet, "/v1/shares", nil, withQuery(map[string]string{
		"bucket": bucket, "key": key,
	}))
	if err != nil {
		return nil, err
	}
	var out struct {
		Shares []Share `json:"shares"`
	}
	if err := c.doJSON(request, &out); err != nil {
		return nil, err
	}
	return out.Shares, nil
}

func (c *Client) RevokeShare(ctx context.Context, id string) error {
	request, err := c.newRequest(ctx, http.MethodDelete, "/v1/shares/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	return c.doJSON(request, nil)
}

func (c *Client) PublishAsset(ctx context.Context, request PublishAssetRequest) (*PublishedAsset, error) {
	var out PublishedAsset
	err := c.postEnterpriseJSON(ctx, "/v1/assets", request, &out)
	return &out, err
}

func (c *Client) UnpublishAsset(ctx context.Context, slug string) error {
	request, err := c.newRequest(ctx, http.MethodDelete, "/v1/assets/"+escapeKey(slug), nil)
	if err != nil {
		return err
	}
	return c.doJSON(request, nil)
}

func (c *Client) ListAssets(ctx context.Context) ([]PublicAsset, error) {
	request, err := c.newRequest(ctx, http.MethodGet, "/v1/assets", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Assets []PublicAsset `json:"assets"`
	}
	if err := c.doJSON(request, &out); err != nil {
		return nil, err
	}
	return out.Assets, nil
}

func (c *Client) PutACL(ctx context.Context, request ACLRequest) (map[string]any, error) {
	var out map[string]any
	err := c.putEnterpriseJSON(ctx, "/v1/access/acl", request, &out)
	return out, err
}

func (c *Client) ListResourceACL(
	ctx context.Context,
	bucket, key, resourceKind string,
) ([]ResourceACLEntry, error) {
	request, err := c.newRequest(ctx, http.MethodGet, "/v1/access/acl", nil, withQuery(map[string]string{
		"bucket": bucket, "key": key, "kind": resourceKind,
	}))
	if err != nil {
		return nil, err
	}
	var out struct {
		Entries []ResourceACLEntry `json:"entries"`
	}
	if err := c.doJSON(request, &out); err != nil {
		return nil, err
	}
	return out.Entries, nil
}

func (c *Client) DeleteResourceACL(ctx context.Context, id string) error {
	request, err := c.newRequest(ctx, http.MethodDelete, "/v1/access/acl/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	return c.doJSON(request, nil)
}

func (c *Client) CreateDepartment(ctx context.Context, name, parentID string) (*Department, error) {
	var out Department
	err := c.postEnterpriseJSON(ctx, "/v1/admin/departments", map[string]string{
		"name": name, "parent_id": parentID,
	}, &out)
	return &out, err
}

func (c *Client) ListDepartments(ctx context.Context) ([]Department, error) {
	request, err := c.newRequest(ctx, http.MethodGet, "/v1/admin/departments", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Departments []Department `json:"departments"`
	}
	if err := c.doJSON(request, &out); err != nil {
		return nil, err
	}
	return out.Departments, nil
}

func (c *Client) GetDepartment(ctx context.Context, id string) (*DepartmentDetails, error) {
	request, err := c.newRequest(ctx, http.MethodGet, "/v1/admin/departments/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	var out DepartmentDetails
	if err := c.doJSON(request, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteDepartment(ctx context.Context, id string) error {
	request, err := c.newRequest(ctx, http.MethodDelete, "/v1/admin/departments/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	return c.doJSON(request, nil)
}

func (c *Client) PutDepartmentMember(ctx context.Context, departmentID, subjectID, role string) error {
	body, jsonOption, err := jsonBody(map[string]string{"role": role})
	if err != nil {
		return err
	}
	path := "/v1/admin/departments/" + url.PathEscape(departmentID) + "/members/" + url.PathEscape(subjectID)
	request, err := c.newRequest(ctx, http.MethodPut, path, body, jsonOption)
	if err != nil {
		return err
	}
	return c.doJSON(request, nil)
}

func (c *Client) DeleteDepartmentMember(ctx context.Context, departmentID, subjectID string) error {
	path := "/v1/admin/departments/" + url.PathEscape(departmentID) + "/members/" + url.PathEscape(subjectID)
	request, err := c.newRequest(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	return c.doJSON(request, nil)
}

// ExportArchive returns a portable tar.gz stream. The caller must close it.
func (c *Client) ExportArchive(ctx context.Context, bucket, prefix string) (io.ReadCloser, error) {
	request, err := c.newRequest(ctx, http.MethodGet, "/v1/exports/archive", nil, withQuery(map[string]string{
		"bucket": bucket, "prefix": prefix,
	}))
	if err != nil {
		return nil, err
	}
	response, err := c.do(request)
	if err != nil {
		return nil, err
	}
	return response.Body, nil
}

func (c *Client) postEnterpriseJSON(ctx context.Context, path string, input, output any) error {
	return c.enterpriseJSON(ctx, http.MethodPost, path, input, output)
}

func (c *Client) putEnterpriseJSON(ctx context.Context, path string, input, output any) error {
	return c.enterpriseJSON(ctx, http.MethodPut, path, input, output)
}

func (c *Client) enterpriseJSON(ctx context.Context, method, path string, input, output any) error {
	body, jsonOption, err := jsonBody(input)
	if err != nil {
		return err
	}
	request, err := c.newRequest(ctx, method, path, body, jsonOption)
	if err != nil {
		return err
	}
	return c.doJSON(request, output)
}
