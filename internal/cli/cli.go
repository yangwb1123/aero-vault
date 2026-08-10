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
	"fmt"
	"io"
	"net/http"
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

var cliHandlers map[string]func(*Client, []string) int

func init() {
	cliHandlers = map[string]func(*Client, []string) int{
		"upload":    func(c *Client, a []string) int { return c.cmdUpload(a) },
		"get":       func(c *Client, a []string) int { return c.cmdGet(a) },
		"ls":        func(c *Client, a []string) int { return c.cmdList(a) },
		"rm":        func(c *Client, a []string) int { return c.cmdRemove(a) },
		"search":    func(c *Client, a []string) int { return c.cmdSearch(a) },
		"tag":       func(c *Client, a []string) int { return c.cmdTag(a) },
		"versions":  func(c *Client, a []string) int { return c.cmdVersions(a) },
		"lineage":   func(c *Client, a []string) int { return c.cmdLineage(a) },
		"snapshot":  func(_ *Client, a []string) int { return cmdSnapshot(a) },
		"admin":     func(c *Client, a []string) int { return c.cmdAdmin(a) },
		"lsbuckets": func(c *Client, a []string) int { return c.cmdListBuckets(a) },
		"bucket-rm": func(c *Client, a []string) int { return c.cmdDeleteBucket(a) },
		"help":      func(_ *Client, _ []string) int { usage(); return 0 },
		"-h":        func(_ *Client, _ []string) int { usage(); return 0 },
		"--help":    func(_ *Client, _ []string) int { usage(); return 0 },
	}
}

// Run dispatches `aero-vault cli <subcommand> …`. Returns os.Exit code.
func Run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	c := NewClient()
	h, ok := cliHandlers[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		usage()
		return 2
	}
	return h(c, args[1:])
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
  lsbuckets                  list buckets
  bucket-rm <bucket>         delete a bucket and its objects
  snapshot create <out.tgz> [--db file:./var/aero.db] [--objects ./var/objects]
  snapshot restore <in.tgz> [--db file:./var/aero.db] [--objects ./var/objects]
  admin keys list                list API keys
  admin keys add <token> [--scopes read,write] [--label name] [--tenant t]
  admin keys revoke <token>      revoke an API key
  admin tenants list             list tenants
  admin tenants create <id> [--display-name name]
  admin tenants delete <id>
  admin tenants status <id> <active|suspended>
  admin tenants quota <id> <max_bytes> <max_objects>
  admin tenants budget <id> <daily_budget_usd>
  admin jobs list [--status s] [--type t] [--limit N]
  admin jobs retry <id>
  admin audit list [--limit N]
  admin files delete <tenant> <key> [--hard]

env: AERO_ENDPOINT, AERO_API_KEY, AERO_TENANT`)
}
