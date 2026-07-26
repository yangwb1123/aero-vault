package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

func adminUsage() {
	fmt.Fprintln(os.Stderr, `usage: aero-vault cli admin <resource> <action> [args]

resources:
  keys list
  keys add <token> [--scopes read,write] [--label name] [--tenant t]
  keys revoke <token>
  tenants list
  tenants create <id> [--display-name name]
  tenants delete <id>
  tenants status <id> <active|suspended>
  tenants quota <id> <max_bytes> <max_objects>
  tenants budget <id> <daily_budget_usd>
  jobs list [--status s] [--type t] [--limit N]
  jobs retry <id>
  audit list [--limit N]
  buckets lifecycle <bucket> <days> [--action soft_delete|hard_delete]
  buckets encryption <bucket> <algorithm> [--kms-key-id <id>]
  buckets website <bucket> --index <suffix> [--error <key>]
  buckets quota <bucket> <max_bytes> <max_objects>
  buckets delete <bucket>`)
}

func (c *Client) cmdAdmin(args []string) int {
	if len(args) < 2 {
		adminUsage()
		return 2
	}
	resource, action := args[0], args[1]
	rest := args[2:]
	switch resource {
	case "keys":
		return c.cmdAdminKeys(action, rest)
	case "tenants":
		return c.cmdAdminTenants(action, rest)
	case "jobs":
		return c.cmdAdminJobs(action, rest)
	case "audit":
		return c.cmdAdminAudit(action, rest)
	case "buckets":
		return c.cmdAdminBuckets(action, rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown admin resource: %s\n", resource)
		adminUsage()
		return 2
	}
}

func (c *Client) cmdAdminKeys(action string, args []string) int {
	switch action {
	case "list":
		return c.adminKeysList()
	case "add":
		return c.adminKeysAdd(args)
	case "revoke":
		return c.adminKeysRevoke(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown keys action: %s\n", action)
		return 2
	}
}

func (c *Client) adminKeysList() int {
	resp, err := c.do(http.MethodGet, "/v1/admin/keys", nil, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	var env struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		fmt.Fprintln(os.Stderr, "decode:", err)
		return 1
	}
	for _, k := range env.Keys {
		var m map[string]any
		_ = json.Unmarshal(k, &m)
		fmt.Printf("%-40s tenant=%-20s scopes=%-15s label=%s\n",
			strOrEmpty(m["token_hash"]), strOrEmpty(m["tenant_id"]),
			strOrEmpty(m["scopes"]), strOrEmpty(m["label"]))
	}
	return 0
}

func (c *Client) adminKeysAdd(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: admin keys add <token> [--scopes read,write] [--label name] [--tenant t]")
		return 2
	}
	token := args[0]
	var scopes, label, tenant string
	for i := 1; i < len(args)-1; i++ {
		switch args[i] {
		case "--scopes":
			scopes = args[i+1]
			i++
		case "--label":
			label = args[i+1]
			i++
		case "--tenant":
			tenant = args[i+1]
			i++
		}
	}
	body := map[string]any{"token": token, "tenant": tenant, "label": label}
	if scopes != "" {
		body["scopes"] = strings.Split(scopes, ",")
	}
	b, _ := json.Marshal(body)
	resp, err := c.do(http.MethodPost, "/v1/admin/keys", bytes.NewReader(b), map[string]string{"Content-Type": "application/json"})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	io.Copy(os.Stdout, resp.Body)
	fmt.Println()
	return 0
}

func (c *Client) adminKeysRevoke(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: admin keys revoke <token>")
		return 2
	}
	resp, err := c.do(http.MethodDelete, "/v1/admin/keys/"+url.PathEscape(args[0]), nil, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	fmt.Println("revoked")
	return 0
}

func (c *Client) cmdAdminTenants(action string, args []string) int {
	switch action {
	case "list":
		return c.adminTenantList()
	case "create":
		return c.adminTenantCreate(args)
	case "delete":
		return c.adminTenantDelete(args)
	case "status":
		return c.adminTenantStatus(args)
	case "quota":
		return c.adminTenantQuota(args)
	case "budget":
		return c.adminTenantBudget(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown tenants action: %s\n", action)
		return 2
	}
}

func (c *Client) adminTenantList() int {
	resp, err := c.do(http.MethodGet, "/v1/admin/tenants", nil, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	var env struct {
		Tenants []json.RawMessage `json:"tenants"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		fmt.Fprintln(os.Stderr, "decode:", err)
		return 1
	}
	for _, t := range env.Tenants {
		var m map[string]any
		_ = json.Unmarshal(t, &m)
		fmt.Printf("%-30s display=%-30s status=%s\n",
			strOrEmpty(m["tenant_id"]), strOrEmpty(m["display_name"]), strOrEmpty(m["status"]))
	}
	return 0
}

func (c *Client) adminTenantCreate(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: admin tenants create <id> [--display-name name]")
		return 2
	}
	id := args[0]
	var displayName string
	for i := 1; i < len(args)-1; i++ {
		if args[i] == "--display-name" {
			displayName = args[i+1]
			i++
		}
	}
	body := map[string]any{"tenant_id": id, "display_name": displayName}
	b, _ := json.Marshal(body)
	resp, err := c.do(http.MethodPost, "/v1/admin/tenants", bytes.NewReader(b), map[string]string{"Content-Type": "application/json"})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	io.Copy(os.Stdout, resp.Body)
	fmt.Println()
	return 0
}

func (c *Client) adminTenantDelete(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: admin tenants delete <id>")
		return 2
	}
	resp, err := c.do(http.MethodDelete, "/v1/admin/tenants/"+url.PathEscape(args[0]), nil, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	fmt.Println("deleted")
	return 0
}

func (c *Client) adminTenantStatus(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: admin tenants status <id> <active|suspended>")
		return 2
	}
	body := map[string]any{"status": args[1]}
	b, _ := json.Marshal(body)
	resp, err := c.do(http.MethodPut, "/v1/admin/tenants/"+url.PathEscape(args[0])+"/status", bytes.NewReader(b), map[string]string{"Content-Type": "application/json"})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	fmt.Println("updated")
	return 0
}

func (c *Client) adminTenantQuota(args []string) int {
	if len(args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: admin tenants quota <id> <max_bytes> <max_objects>")
		return 2
	}
	var maxBytes, maxObjects int64
	fmt.Sscanf(args[1], "%d", &maxBytes)
	fmt.Sscanf(args[2], "%d", &maxObjects)
	body := map[string]any{"max_bytes": maxBytes, "max_objects": maxObjects}
	b, _ := json.Marshal(body)
	resp, err := c.do(http.MethodPut, "/v1/admin/tenants/"+url.PathEscape(args[0])+"/quota", bytes.NewReader(b), map[string]string{"Content-Type": "application/json"})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	io.Copy(os.Stdout, resp.Body)
	fmt.Println()
	return 0
}

func (c *Client) adminTenantBudget(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: admin tenants budget <id> <daily_budget_usd>")
		return 2
	}
	var budget float64
	fmt.Sscanf(args[1], "%f", &budget)
	body := map[string]any{"daily_budget_usd": budget}
	b, _ := json.Marshal(body)
	resp, err := c.do(http.MethodPut, "/v1/admin/tenants/"+url.PathEscape(args[0])+"/budget", bytes.NewReader(b), map[string]string{"Content-Type": "application/json"})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	io.Copy(os.Stdout, resp.Body)
	fmt.Println()
	return 0
}

func (c *Client) cmdAdminJobs(action string, args []string) int {
	switch action {
	case "list":
		return c.adminJobsList(args)
	case "retry":
		return c.adminJobsRetry(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown jobs action: %s\n", action)
		return 2
	}
}

func (c *Client) adminJobsList(args []string) int {
	q := url.Values{}
	for i := 0; i < len(args)-1; i++ {
		switch args[i] {
		case "--status":
			q.Set("status", args[i+1])
			i++
		case "--type":
			q.Set("type", args[i+1])
			i++
		case "--limit":
			q.Set("limit", args[i+1])
			i++
		}
	}
	resp, err := c.do(http.MethodGet, "/v1/admin/jobs?"+q.Encode(), nil, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "HTTP %d: %s\n", resp.StatusCode, string(respBody))
		return 1
	}
	fmt.Println(string(respBody))
	return 0
}

func (c *Client) adminJobsRetry(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: admin jobs retry <id>")
		return 2
	}
	resp, err := c.do(http.MethodPost, "/v1/admin/jobs/"+url.PathEscape(args[0])+"/retry", nil, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "HTTP %d: %s\n", resp.StatusCode, string(respBody))
		return 1
	}
	fmt.Println(string(respBody))
	return 0
}

func (c *Client) cmdAdminAudit(action string, args []string) int {
	switch action {
	case "list":
		q := url.Values{}
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "--limit" {
				q.Set("limit", args[i+1])
				i++
			}
		}
		resp, err := c.do(http.MethodGet, "/v1/admin/audit?"+q.Encode(), nil, nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 300 {
			fmt.Fprintf(os.Stderr, "HTTP %d: %s\n", resp.StatusCode, string(respBody))
			return 1
		}
		fmt.Println(string(respBody))
		return 0

	default:
		fmt.Fprintf(os.Stderr, "unknown audit action: %s\n", action)
		return 2
	}
}

func (c *Client) cmdAdminBuckets(action string, args []string) int {
	switch action {
	case "lifecycle":
		return c.adminBucketLifecycle(args)
	case "encryption":
		return c.adminBucketEncryption(args)
	case "website":
		return c.adminBucketWebsite(args)
	case "quota":
		return c.adminBucketQuota(args)
	case "delete":
		return c.adminBucketDelete(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown buckets action: %s\n", action)
		return 2
	}
}

func (c *Client) adminBucketLifecycle(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: admin buckets lifecycle <bucket> <days> [--action soft_delete|hard_delete]")
		return 2
	}
	bucket := args[0]
	var days int
	fmt.Sscanf(args[1], "%d", &days)
	action := "soft_delete"
	for i := 2; i < len(args); i++ {
		if args[i] == "--action" && i+1 < len(args) {
			action = args[i+1]
			i++
		}
	}
	body := map[string]any{"days": days, "action": action}
	b, _ := json.Marshal(body)
	resp, err := c.do(http.MethodPut, "/v1/buckets/"+url.PathEscape(bucket)+"/lifecycle", bytes.NewReader(b), map[string]string{"Content-Type": "application/json"})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	io.Copy(os.Stdout, resp.Body)
	fmt.Println()
	return 0
}

func (c *Client) adminBucketEncryption(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: admin buckets encryption <bucket> <algorithm> [--kms-key-id <id>]")
		return 2
	}
	bucket := args[0]
	alg := args[1]
	kmsKeyID := ""
	for i := 2; i < len(args); i++ {
		if args[i] == "--kms-key-id" && i+1 < len(args) {
			kmsKeyID = args[i+1]
			i++
		}
	}
	body := map[string]any{"sse_algorithm": alg, "sse_kms_key_id": kmsKeyID}
	b, _ := json.Marshal(body)
	resp, err := c.do(http.MethodPut, "/v1/buckets/"+url.PathEscape(bucket)+"/encryption", bytes.NewReader(b), map[string]string{"Content-Type": "application/json"})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	io.Copy(os.Stdout, resp.Body)
	fmt.Println()
	return 0
}

func (c *Client) adminBucketWebsite(args []string) int {
	bucket := ""
	var idxDoc, errDoc string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--index":
			if i+1 < len(args) {
				idxDoc = args[i+1]
				i++
			}
		case "--error":
			if i+1 < len(args) {
				errDoc = args[i+1]
				i++
			}
		default:
			if bucket == "" {
				bucket = args[i]
			}
		}
	}
	if bucket == "" || idxDoc == "" {
		fmt.Fprintln(os.Stderr, "usage: admin buckets website <bucket> --index <suffix> [--error <key>]")
		return 2
	}
	body := map[string]any{"index_document": map[string]string{"suffix": idxDoc}}
	if errDoc != "" {
		body["error_document"] = map[string]string{"key": errDoc}
	}
	b, _ := json.Marshal(body)
	resp, err := c.do(http.MethodPut, "/v1/buckets/"+url.PathEscape(bucket)+"/encryption", bytes.NewReader(b), map[string]string{"Content-Type": "application/json"})
	_ = resp
	_ = err
	// Use the admin-only endpoint for website config (TODO: add REST endpoint)
	fmt.Fprintln(os.Stderr, "website: not yet supported via admin API, use S3 endpoint directly")
	return 1
}

func (c *Client) adminBucketQuota(args []string) int {
	if len(args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: admin buckets quota <bucket> <max_bytes> <max_objects>")
		return 2
	}
	bucket := args[0]
	var maxBytes, maxObjects int64
	fmt.Sscanf(args[1], "%d", &maxBytes)
	fmt.Sscanf(args[2], "%d", &maxObjects)
	body := map[string]any{"max_bytes": maxBytes, "max_objects": maxObjects}
	b, _ := json.Marshal(body)
	resp, err := c.do(http.MethodPut, "/v1/admin/buckets/"+url.PathEscape(bucket)+"/quota", bytes.NewReader(b), map[string]string{"Content-Type": "application/json"})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	io.Copy(os.Stdout, resp.Body)
	fmt.Println()
	return 0
}

func (c *Client) adminBucketDelete(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: admin buckets delete <bucket>")
		return 2
	}
	resp, err := c.do(http.MethodDelete, "/v1/buckets/"+url.PathEscape(args[0]), nil, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	io.Copy(os.Stdout, resp.Body)
	fmt.Println()
	return 0
}

func strOrEmpty(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func (c *Client) cmdListBuckets(args []string) int {
	resp, err := c.do("GET", "/v1/buckets", nil, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "HTTP %d: %s\n", resp.StatusCode, string(body))
		return 1
	}
	fmt.Println(string(body))
	return 0
}

func (c *Client) cmdDeleteBucket(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "bucket-rm requires <name>")
		return 2
	}
	resp, err := c.do("DELETE", "/v1/buckets/"+args[0], nil, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "HTTP %d: %s\n", resp.StatusCode, string(body))
		return 1
	}
	fmt.Println("bucket deleted")
	return 0
}
