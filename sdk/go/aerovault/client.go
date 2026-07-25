// Package aerovault is a native Go client for the aero-vault AI-native file
// platform. It depends only on the standard library.
//
// Quickstart:
//
//	c, _ := aerovault.New("http://localhost:8080",
//		aerovault.WithToken("prod-rw"),
//		aerovault.WithTenant("acme"))
//
//	ctx := context.Background()
//	c.Upload(ctx, "docs/readme.txt", strings.NewReader("hello world"),
//		aerovault.UploadOptions{ContentType: "text/plain"})
//
//	rc, obj, _ := c.Get(ctx, "docs/readme.txt")
//	defer rc.Close()
//	io.Copy(os.Stdout, rc)
//	_ = obj
//
//	res, _ := c.Search(ctx, aerovault.SearchRequest{Query: "hello", K: 5})
//	for _, h := range res.Hits { fmt.Println(h.ObjectKey, h.Score) }
package aerovault

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Version is the SDK release version.
const Version = "0.4.0"

// DefaultTenant is used when WithTenant is not supplied.
const DefaultTenant = "default"

// Client is an HTTP client for an aero-vault server. Safe for concurrent use.
type Client struct {
	baseURL      string
	httpClient   *http.Client
	token        string
	tenant       string
	apiKeyHeader bool
	userAgent    string
}

// Option configures a Client in New.
type Option func(*Client)

func WithToken(token string) Option {
	return func(c *Client) { c.token = token }
}

func WithTenant(tenant string) Option {
	return func(c *Client) { c.tenant = tenant }
}

func WithAPIKeyHeader() Option {
	return func(c *Client) { c.apiKeyHeader = true }
}

func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.httpClient = hc }
}

func WithUserAgent(ua string) Option {
	return func(c *Client) { c.userAgent = ua }
}

func New(baseURL string, opts ...Option) (*Client, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("aerovault: base URL is required")
	}
	baseURL = strings.TrimRight(baseURL, "/")
	c := &Client{baseURL: baseURL, tenant: DefaultTenant, httpClient: http.DefaultClient}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// ── Internal helpers ────────────────────────────────────────────────────────

type reqOpt func(*http.Request)

func withQuery(params map[string]string) reqOpt {
	return func(req *http.Request) {
		q := req.URL.Query()
		for k, v := range params {
			q.Set(k, v)
		}
		req.URL.RawQuery = q.Encode()
	}
}

func withHeader(k, v string) reqOpt {
	return func(req *http.Request) { req.Header.Set(k, v) }
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader, opts ...reqOpt) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		if c.apiKeyHeader {
			req.Header.Set("X-Api-Key", c.token)
		} else {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
	}
	if c.tenant != "" && c.tenant != DefaultTenant {
		req.Header.Set("X-Aero-Tenant", c.tenant)
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	for _, opt := range opts {
		opt(req)
	}
	return req, nil
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("aerovault: %w", err)
	}
	if resp.StatusCode >= 400 {
		return resp, parseError(resp)
	}
	return resp, nil
}

func (c *Client) doJSON(req *http.Request, out any) error {
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func jsonBody(v any) (io.Reader, reqOpt, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, nil, err
	}
	return bytes.NewReader(b), withHeader("Content-Type", "application/json"), nil
}

func parseError(resp *http.Response) error {
	var e Error
	e.Status = resp.StatusCode
	var env struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			RequestID string `json:"request_id,omitempty"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err == nil && env.Error.Code != "" {
		e.Code = env.Error.Code
		e.Message = env.Error.Message
		e.RequestID = env.Error.RequestID
	}
	if e.Code == "" {
		e.Code = fmt.Sprintf("HTTP%d", resp.StatusCode)
		e.Message = resp.Status
	}
	return &e
}

func escapeKey(key string) string {
	return url.PathEscape(key)
}

func filesPath(key, suffix string) string {
	return "/v1/files/" + escapeKey(key) + suffix
}

// AsError unwraps an error chain looking for *Error. Returns true when found.
func AsError(err error, target **Error) bool {
	if err == nil {
		return false
	}
	var e *Error
	if errors.As(err, &e) {
		*target = e
		return true
	}
	return false
}
