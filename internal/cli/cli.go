// Package cli is the operator/dev-facing client. It speaks to a running
// aero-vault server over HTTP. Usage:
//
//	aero-vault cli upload <key> <file>
//	aero-vault cli get    <key>            # streams to stdout
//	aero-vault cli ls     [<prefix>]
//	aero-vault cli rm     <key>
//	aero-vault cli search <query>
//	aero-vault cli tag    <key> k1=v1 k2=v2
//
// Configuration via env:
//
//	AERO_ENDPOINT (default http://localhost:8080)
//	AERO_API_KEY  (optional; sent as "Authorization: Bearer …")
//	AERO_TENANT   (optional; sent as X-Aero-Tenant)
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
	"time"

	"github.com/aero-vault/aero-vault/internal/snapshot"
)

// shims so we keep the snapshot CLI plumbing local to this package.
func snapshotCreate(out, db, objects string) error { return snapshot.Create(out, db, objects) }
func snapshotRestore(in, db, objects string) error { return snapshot.Restore(in, db, objects) }

type Client struct {
	endpoint string
	apiKey   string
	tenant   string
	http     *http.Client
}

func NewClient() *Client {
	ep := os.Getenv("AERO_ENDPOINT")
	if ep == "" {
		ep = "http://localhost:8080"
	}
	return &Client{
		endpoint: strings.TrimRight(ep, "/"),
		apiKey:   os.Getenv("AERO_API_KEY"),
		tenant:   os.Getenv("AERO_TENANT"),
		http:     &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *Client) do(method, path string, body io.Reader, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequest(method, c.endpoint+path, body)
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	if c.tenant != "" {
		req.Header.Set("X-Aero-Tenant", c.tenant)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return c.http.Do(req)
}

// Run dispatches `aero-vault cli <subcommand> …`. Returns os.Exit code.
func Run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	c := NewClient()
	switch args[0] {
	case "upload":
		return c.cmdUpload(args[1:])
	case "get":
		return c.cmdGet(args[1:])
	case "ls":
		return c.cmdList(args[1:])
	case "rm":
		return c.cmdRemove(args[1:])
	case "search":
		return c.cmdSearch(args[1:])
	case "tag":
		return c.cmdTag(args[1:])
	case "versions":
		return c.cmdVersions(args[1:])
	case "lineage":
		return c.cmdLineage(args[1:])
	case "snapshot":
		return cmdSnapshot(args[1:])
	case "admin":
		return c.cmdAdmin(args[1:])
	case "-h", "--help", "help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: aero-vault cli <command> [args]

commands:
  upload <key> <file>       upload a local file
  get <key>                 print object body to stdout
  ls [<prefix>]             list objects
  rm <key>                  delete object
  search <query> [-k N]     semantic search (use --mode hybrid for BM25+vector)
  tag <key> k1=v1 k2=v2     overwrite tags
  versions <key>            list versions
  lineage <objectID>        show AI consumption history
  snapshot create <out.tgz> [--db file:./var/aero.db] [--objects ./var/objects]
  snapshot restore <in.tgz> [--db file:./var/aero.db] [--objects ./var/objects]
  admin keys list                list API keys
  admin keys add <token> [--scopes read,write] [--label name] [--tenant t]
  admin keys revoke <token>      revoke an API key
  admin tenants list             list tenants
  admin tenants create <id> [--display-name name]
  admin tenants delete <id>
  admin tenants status <id> <active|suspended>

env: AERO_ENDPOINT, AERO_API_KEY, AERO_TENANT`)
}

func cmdSnapshot(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "snapshot {create|restore} <file> [--db <dsn>] [--objects <root>]")
		return 2
	}
	mode, file := args[0], args[1]
	db := os.Getenv("DB_DSN")
	if db == "" {
		db = "file:./var/aero.db"
	}
	objects := os.Getenv("STORAGE_LOCAL_ROOT")
	if objects == "" {
		objects = "./var/objects"
	}
	for i := 2; i < len(args)-1; i++ {
		switch args[i] {
		case "--db":
			db = args[i+1]
		case "--objects":
			objects = args[i+1]
		}
	}
	switch mode {
	case "create":
		if err := snapshotCreate(file, db, objects); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("snapshot written:", file)
	case "restore":
		if err := snapshotRestore(file, db, objects); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("snapshot restored from:", file)
	default:
		fmt.Fprintln(os.Stderr, "snapshot mode must be create|restore")
		return 2
	}
	return 0
}

func (c *Client) cmdUpload(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "upload requires <key> <file>")
		return 2
	}
	key, file := args[0], args[1]
	f, err := os.Open(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer f.Close()
	st, _ := f.Stat()
	req, _ := http.NewRequest("PUT", c.endpoint+"/v1/files/"+escapeKey(key), f)
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	if c.tenant != "" {
		req.Header.Set("X-Aero-Tenant", c.tenant)
	}
	if st != nil {
		req.ContentLength = st.Size()
	}
	resp, err := c.http.Do(req)
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

func (c *Client) cmdGet(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "get requires <key>")
		return 2
	}
	resp, err := c.do("GET", "/v1/files/"+escapeKey(args[0]), nil, nil)
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
	if _, err := io.Copy(os.Stdout, resp.Body); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func (c *Client) cmdList(args []string) int {
	q := url.Values{}
	if len(args) > 0 {
		q.Set("prefix", args[0])
	}
	resp, err := c.do("GET", "/v1/files?"+q.Encode(), nil, nil)
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

func (c *Client) cmdRemove(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "rm requires <key>")
		return 2
	}
	resp, err := c.do("DELETE", "/v1/files/"+escapeKey(args[0]), nil, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "HTTP %d\n", resp.StatusCode)
		return 1
	}
	return 0
}

func (c *Client) cmdSearch(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "search requires <query>")
		return 2
	}
	q := args[0]
	k := 10
	mode := "vector"
	for i := 1; i < len(args)-1; i++ {
		switch args[i] {
		case "-k":
			fmt.Sscanf(args[i+1], "%d", &k)
		case "--mode":
			mode = args[i+1]
		}
	}
	body, _ := json.Marshal(map[string]any{"query": q, "k": k, "mode": mode})
	resp, err := c.do("POST", "/v1/search", bytes.NewReader(body), map[string]string{"Content-Type": "application/json"})
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

func (c *Client) cmdTag(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "tag requires <key> [k1=v1 ...]")
		return 2
	}
	key := args[0]
	tags := map[string]string{}
	for _, kv := range args[1:] {
		if i := strings.Index(kv, "="); i > 0 {
			tags[kv[:i]] = kv[i+1:]
		}
	}
	body, _ := json.Marshal(tags)
	resp, err := c.do("PUT", "/v1/files/"+escapeKey(key)+"/tags", bytes.NewReader(body), map[string]string{"Content-Type": "application/json"})
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

func (c *Client) cmdVersions(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "versions requires <key>")
		return 2
	}
	resp, err := c.do("GET", "/v1/files/"+escapeKey(args[0])+"/versions", nil, nil)
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

func (c *Client) cmdLineage(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "lineage requires <objectID>")
		return 2
	}
	resp, err := c.do("GET", "/v1/lineage/objects/"+args[0], nil, nil)
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

func adminUsage() {
	fmt.Fprintln(os.Stderr, `usage: aero-vault cli admin <resource> <action> [args]

resources:
  keys list
  keys add <token> [--scopes read,write] [--label name] [--tenant t]
  keys revoke <token>
  tenants list
  tenants create <id> [--display-name name]
  tenants delete <id>
  tenants status <id> <active|suspended>`)
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
	default:
		fmt.Fprintf(os.Stderr, "unknown admin resource: %s\n", resource)
		adminUsage()
		return 2
	}
}

func (c *Client) cmdAdminKeys(action string, args []string) int {
	switch action {
	case "list":
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

	case "add":
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

	case "revoke":
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

	default:
		fmt.Fprintf(os.Stderr, "unknown keys action: %s\n", action)
		return 2
	}
}

func (c *Client) cmdAdminTenants(action string, args []string) int {
	switch action {
	case "list":
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

	case "create":
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

	case "delete":
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

	case "status":
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

	default:
		fmt.Fprintf(os.Stderr, "unknown tenants action: %s\n", action)
		return 2
	}
}

func strOrEmpty(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func escapeKey(k string) string {
	parts := strings.Split(k, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}
