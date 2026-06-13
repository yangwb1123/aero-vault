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
//
// Every method that performs I/O takes a context.Context as its first argument.
package aerovault

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

// Version is the SDK release version.
const Version = "0.4.0"

// DefaultTenant is used when WithTenant is not supplied.
const DefaultTenant = "default"

// Client is an HTTP client for an aero-vault server. It is safe for concurrent
// use by multiple goroutines. Construct one with New.
type Client struct {
	baseURL      string
	httpClient   *http.Client
	token        string
	tenant       string
	apiKeyHeader bool // send token as X-Api-Key instead of Authorization: Bearer
	userAgent    string
}

// Option configures a Client in New.
type Option func(*Client)

// WithToken sets the API key or JWT, sent as "Authorization: Bearer <token>"
// (or as "X-Api-Key" when WithAPIKeyHeader is also set).
func WithToken(token string) Option {
	return func(c *Client) { c.token = token }
}

// WithTenant sets the value of the X-Aero-Tenant header. Defaults to "default".
func WithTenant(tenant string) Option {
	return func(c *Client) {
		if tenant != "" {
			c.tenant = tenant
		}
	}
}

// WithAPIKeyHeader makes the client send its token in the X-Api-Key header
// rather than as an Authorization: Bearer credential.
func WithAPIKeyHeader() Option {
	return func(c *Client) { c.apiKeyHeader = true }
}

// WithHTTPClient supplies a custom *http.Client (for timeouts, transports,
// proxies, etc.). If not set, a fresh *http.Client is used.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		if hc != nil {
			c.httpClient = hc
		}
	}
}

// WithUserAgent overrides the default User-Agent header.
func WithUserAgent(ua string) Option {
	return func(c *Client) { c.userAgent = ua }
}

// New constructs a Client for the given base URL (e.g. "http://localhost:8080").
// A trailing slash on baseURL is trimmed. New never returns a nil Client on
// success; the error is reserved for an unparseable base URL.
func New(baseURL string, opts ...Option) (*Client, error) {
	trimmed := strings.TrimRight(baseURL, "/")
	if trimmed == "" {
		return nil, fmt.Errorf("aerovault: base URL is required")
	}
	if _, err := url.Parse(trimmed); err != nil {
		return nil, fmt.Errorf("aerovault: invalid base URL %q: %w", baseURL, err)
	}
	c := &Client{
		baseURL:    trimmed,
		httpClient: &http.Client{},
		tenant:     DefaultTenant,
		userAgent:  "aero-vault-go/" + Version,
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// ---- low-level HTTP -------------------------------------------------------

// reqOpt mutates an *http.Request before it is sent.
type reqOpt func(*http.Request)

// withQuery appends non-empty query parameters.
func withQuery(params map[string]string) reqOpt {
	return func(r *http.Request) {
		q := r.URL.Query()
		for k, v := range params {
			if v != "" {
				q.Set(k, v)
			}
		}
		r.URL.RawQuery = q.Encode()
	}
}

// withHeader sets a single header when the value is non-empty.
func withHeader(k, v string) reqOpt {
	return func(r *http.Request) {
		if v != "" {
			r.Header.Set(k, v)
		}
	}
}

// newRequest builds a request with auth + tenant headers applied. The path is
// joined onto baseURL verbatim (callers pre-encode object keys via escapeKey).
func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader, opts ...reqOpt) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("aerovault: building request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Aero-Tenant", c.tenant)
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	if c.token != "" {
		if c.apiKeyHeader {
			req.Header.Set("X-Api-Key", c.token)
		} else {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
	}
	for _, o := range opts {
		o(req)
	}
	return req, nil
}

// do executes a request. On a non-2xx response it drains and closes the body
// and returns an *Error. On success the caller owns resp.Body.
func (c *Client) do(req *http.Request) (*http.Response, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("aerovault: %s %s: %w", req.Method, req.URL.Path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, parseError(resp)
	}
	return resp, nil
}

// doJSON executes a request and decodes a JSON response into out (which may be
// nil to discard). The response body is always closed.
func (c *Client) doJSON(req *http.Request, out any) error {
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		if err == io.EOF { // empty body, e.g. 204
			return nil
		}
		return fmt.Errorf("aerovault: decoding response: %w", err)
	}
	return nil
}

// jsonBody marshals v and returns a reader plus a reqOpt setting the
// Content-Type to application/json.
func jsonBody(v any) (io.Reader, reqOpt, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, nil, fmt.Errorf("aerovault: encoding request: %w", err)
	}
	return bytes.NewReader(b), withHeader("Content-Type", "application/json"), nil
}

// parseError reads a non-2xx response and maps it to an *Error, honoring the
// platform's {"error":{...}} envelope and falling back to the raw body text.
func parseError(resp *http.Response) error {
	e := &Error{Status: resp.StatusCode}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var env struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) == nil && (env.Error.Code != "" || env.Error.Message != "") {
		e.Code = env.Error.Code
		e.Message = env.Error.Message
		e.RequestID = env.Error.RequestID
		return e
	}
	// Non-JSON body (e.g. plain http.Error text); surface it as the message.
	e.Message = strings.TrimSpace(string(body))
	return e
}

// escapeKey percent-encodes a key's segments while preserving "/" separators,
// mirroring the Python client's _escape_key.
func escapeKey(key string) string {
	key = strings.TrimPrefix(key, "/")
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// filesPath builds "/v1/files/<escaped-key><suffix>".
func filesPath(key, suffix string) string {
	return "/v1/files/" + escapeKey(key) + suffix
}

// ---- files: upload --------------------------------------------------------

// UploadOptions configures an Upload. ContentType, when empty, is inferred
// from the key's extension and falls back to application/octet-stream — the
// server's text extractor skips bodies with no/unknown Content-Type, so always
// sending one keeps the object eligible for indexing.
type UploadOptions struct {
	ContentType string
	Metadata    map[string]string // sent as X-Meta-<key> headers
	// Size, when > 0, sets Content-Length explicitly. If 0 and r is a
	// *bytes.Reader / *bytes.Buffer / *strings.Reader, the length is detected;
	// otherwise the body is sent without a Content-Length (chunked).
	Size int64
}

// Upload writes the bytes from r to key (PUT /v1/files/<key>) and returns the
// stored object's metadata.
func (c *Client) Upload(ctx context.Context, key string, r io.Reader, opts UploadOptions) (*Object, error) {
	ct := opts.ContentType
	if ct == "" {
		ct = guessContentType(key)
	}
	req, err := c.newRequest(ctx, http.MethodPut, filesPath(key, ""), r,
		withHeader("Content-Type", ct))
	if err != nil {
		return nil, err
	}
	for mk, mv := range opts.Metadata {
		req.Header.Set("X-Meta-"+mk, mv)
	}
	if opts.Size > 0 {
		req.ContentLength = opts.Size
	}
	var obj Object
	if err := c.doJSON(req, &obj); err != nil {
		return nil, err
	}
	return &obj, nil
}

// guessContentType infers a MIME type from the key's extension, falling back to
// application/octet-stream.
func guessContentType(key string) string {
	if ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(key))); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// ---- files: download ------------------------------------------------------

// Get downloads an object (GET /v1/files/<key>). The caller MUST close the
// returned ReadCloser. The returned *Object carries metadata derived from the
// response headers (size, etag, content-type, last-modified).
func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, *Object, error) {
	return c.getWith(ctx, key, "")
}

// GetVersion downloads a specific historical version (GET /v1/files/<key>?version=ID).
func (c *Client) GetVersion(ctx context.Context, key, version string) (io.ReadCloser, *Object, error) {
	return c.getWith(ctx, key, version)
}

func (c *Client) getWith(ctx context.Context, key, version string) (io.ReadCloser, *Object, error) {
	req, err := c.newRequest(ctx, http.MethodGet, filesPath(key, ""), nil,
		withQuery(map[string]string{"version": version}))
	if err != nil {
		return nil, nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, nil, err
	}
	return resp.Body, objectFromHeaders(key, resp), nil
}

// GetRange downloads a byte range of an object (Range: bytes=...), returning a
// 206 Partial Content body. A length <= 0 means "to end of object". The caller
// MUST close the returned ReadCloser.
func (c *Client) GetRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, *Object, error) {
	var rangeHdr string
	if length > 0 {
		rangeHdr = fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)
	} else {
		rangeHdr = fmt.Sprintf("bytes=%d-", offset)
	}
	req, err := c.newRequest(ctx, http.MethodGet, filesPath(key, ""), nil,
		withHeader("Range", rangeHdr))
	if err != nil {
		return nil, nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, nil, err
	}
	return resp.Body, objectFromHeaders(key, resp), nil
}

// Download streams an object to dst and returns the number of bytes written.
func (c *Client) Download(ctx context.Context, key string, dst io.Writer) (int64, error) {
	rc, _, err := c.Get(ctx, key)
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	return io.Copy(dst, rc)
}

// objectFromHeaders builds an Object from a GET/HEAD response's headers.
func objectFromHeaders(key string, resp *http.Response) *Object {
	obj := &Object{
		Key:         key,
		ContentType: resp.Header.Get("Content-Type"),
		ETag:        strings.Trim(resp.Header.Get("ETag"), `"`),
	}
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		if n, err := strconv.ParseInt(cl, 10, 64); err == nil {
			obj.Size = n
		}
	}
	if lm := resp.Header.Get("Last-Modified"); lm != "" {
		if t, err := http.ParseTime(lm); err == nil {
			obj.UpdatedAt = t.UTC()
		}
	}
	return obj
}

// ---- files: stat / list / delete -----------------------------------------

// Stat issues a HEAD for an object and returns metadata from the response
// headers (HEAD /v1/files/<key>). A missing object yields an *Error with
// Status 404.
func (c *Client) Stat(ctx context.Context, key string) (*Object, error) {
	req, err := c.newRequest(ctx, http.MethodHead, filesPath(key, ""), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return objectFromHeaders(key, resp), nil
}

// Exists reports whether an object exists. A 404 yields (false, nil); any other
// error is returned.
func (c *Client) Exists(ctx context.Context, key string) (bool, error) {
	_, err := c.Stat(ctx, key)
	if err == nil {
		return true, nil
	}
	var ae *Error
	if AsError(err, &ae) && ae.Status == http.StatusNotFound {
		return false, nil
	}
	return false, err
}

// ListOptions parameterizes a single List page.
type ListOptions struct {
	Prefix string
	Marker string
	Limit  int
}

// List returns one page of objects (GET /v1/files).
func (c *Client) List(ctx context.Context, opts ListOptions) (*ListPage, error) {
	q := map[string]string{"prefix": opts.Prefix, "marker": opts.Marker}
	if opts.Limit > 0 {
		q["limit"] = strconv.Itoa(opts.Limit)
	}
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/files", nil, withQuery(q))
	if err != nil {
		return nil, err
	}
	var page ListPage
	if err := c.doJSON(req, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

// IterObjects auto-paginates every object under prefix, invoking fn for each.
// Return a non-nil error from fn to stop early (it is propagated). A pageSize
// <= 0 lets the server pick its default. Iteration also stops if ctx is done.
func (c *Client) IterObjects(ctx context.Context, prefix string, pageSize int, fn func(Object) error) error {
	marker := ""
	for {
		page, err := c.List(ctx, ListOptions{Prefix: prefix, Marker: marker, Limit: pageSize})
		if err != nil {
			return err
		}
		for _, o := range page.Objects {
			if err := fn(o); err != nil {
				return err
			}
		}
		if !page.HasMore || page.NextMarker == "" {
			return nil
		}
		marker = page.NextMarker
	}
}

// Delete removes an object (DELETE /v1/files/<key>). When hard is true the
// underlying bytes are physically removed (?hard=1); otherwise it is a soft
// delete.
func (c *Client) Delete(ctx context.Context, key string, hard bool) error {
	q := map[string]string{}
	if hard {
		q["hard"] = "1"
	}
	req, err := c.newRequest(ctx, http.MethodDelete, filesPath(key, ""), nil, withQuery(q))
	if err != nil {
		return err
	}
	return c.doJSON(req, nil)
}

// ---- files: tags / versions / acl / thumbnail / presign ------------------

// tagsEnvelope matches the server's {"tags": {...}} response shape.
type tagsEnvelope struct {
	Tags map[string]string `json:"tags"`
}

// GetTags returns an object's tag map (GET /v1/files/<key>/tags).
func (c *Client) GetTags(ctx context.Context, key string) (map[string]string, error) {
	req, err := c.newRequest(ctx, http.MethodGet, filesPath(key, "/tags"), nil)
	if err != nil {
		return nil, err
	}
	var env tagsEnvelope
	if err := c.doJSON(req, &env); err != nil {
		return nil, err
	}
	if env.Tags == nil {
		env.Tags = map[string]string{}
	}
	return env.Tags, nil
}

// PutTags replaces an object's tags (PUT /v1/files/<key>/tags). The body is the
// raw tag map. Returns the resulting tags as echoed by the server.
func (c *Client) PutTags(ctx context.Context, key string, tags map[string]string) (map[string]string, error) {
	if tags == nil {
		tags = map[string]string{}
	}
	body, ctOpt, err := jsonBody(tags)
	if err != nil {
		return nil, err
	}
	req, err := c.newRequest(ctx, http.MethodPut, filesPath(key, "/tags"), body, ctOpt)
	if err != nil {
		return nil, err
	}
	var env tagsEnvelope
	if err := c.doJSON(req, &env); err != nil {
		return nil, err
	}
	return env.Tags, nil
}

// DeleteTags clears all tags on an object (DELETE /v1/files/<key>/tags).
func (c *Client) DeleteTags(ctx context.Context, key string) error {
	req, err := c.newRequest(ctx, http.MethodDelete, filesPath(key, "/tags"), nil)
	if err != nil {
		return err
	}
	return c.doJSON(req, nil)
}

// ListVersions returns an object's version history (GET /v1/files/<key>/versions).
func (c *Client) ListVersions(ctx context.Context, key string) ([]ObjectVersion, error) {
	req, err := c.newRequest(ctx, http.MethodGet, filesPath(key, "/versions"), nil)
	if err != nil {
		return nil, err
	}
	var env struct {
		Versions []ObjectVersion `json:"versions"`
	}
	if err := c.doJSON(req, &env); err != nil {
		return nil, err
	}
	return env.Versions, nil
}

// GetACL returns an object's canned ACL (GET /v1/files/<key>/acl).
func (c *Client) GetACL(ctx context.Context, key string) (string, error) {
	req, err := c.newRequest(ctx, http.MethodGet, filesPath(key, "/acl"), nil)
	if err != nil {
		return "", err
	}
	var env struct {
		ACL string `json:"acl"`
	}
	if err := c.doJSON(req, &env); err != nil {
		return "", err
	}
	return env.ACL, nil
}

// SetACL sets an object's canned ACL (PUT /v1/files/<key>/acl). Valid values:
// private, public-read, public-read-write, authenticated-read.
func (c *Client) SetACL(ctx context.Context, key, acl string) error {
	body, ctOpt, err := jsonBody(map[string]string{"acl": acl})
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, http.MethodPut, filesPath(key, "/acl"), body, ctOpt)
	if err != nil {
		return err
	}
	return c.doJSON(req, nil)
}

// Thumbnail returns an on-demand JPEG thumbnail of an image object
// (GET /v1/files/<key>/thumbnail?w=&h=). A w or h <= 0 is omitted, letting the
// server use its default (256).
func (c *Client) Thumbnail(ctx context.Context, key string, w, h int) ([]byte, error) {
	q := map[string]string{}
	if w > 0 {
		q["w"] = strconv.Itoa(w)
	}
	if h > 0 {
		q["h"] = strconv.Itoa(h)
	}
	req, err := c.newRequest(ctx, http.MethodGet, filesPath(key, "/thumbnail"), nil, withQuery(q))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "image/jpeg")
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// Presign creates a presigned URL for direct object access (POST
// /v1/files/<key>/presign?op=get|put&expires=<sec>). expires <= 0 uses the
// server default.
func (c *Client) Presign(ctx context.Context, key, op string, expires int) (*Presigned, error) {
	if op == "" {
		op = "get"
	}
	q := map[string]string{"op": op}
	if expires > 0 {
		q["expires"] = strconv.Itoa(expires)
	}
	req, err := c.newRequest(ctx, http.MethodPost, filesPath(key, "/presign"), nil, withQuery(q))
	if err != nil {
		return nil, err
	}
	var p Presigned
	if err := c.doJSON(req, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ---- AI: search / chat / agent -------------------------------------------

// Search runs semantic / lexical / hybrid retrieval (POST /v1/search).
func (c *Client) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	body, ctOpt, err := jsonBody(req)
	if err != nil {
		return nil, err
	}
	hreq, err := c.newRequest(ctx, http.MethodPost, "/v1/search", body, ctOpt)
	if err != nil {
		return nil, err
	}
	var out SearchResponse
	if err := c.doJSON(hreq, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Chat runs a RAG chat turn and returns the answer plus citations
// (POST /v1/chat).
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	body, ctOpt, err := jsonBody(req)
	if err != nil {
		return nil, err
	}
	hreq, err := c.newRequest(ctx, http.MethodPost, "/v1/chat", body, ctOpt)
	if err != nil {
		return nil, err
	}
	var out ChatResponse
	if err := c.doJSON(hreq, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ChatStream runs a streaming RAG chat (POST /v1/chat/stream, SSE). onToken is
// invoked for each answer token as it arrives. The final ChatResponse (answer +
// citations) parsed from the "done" frame is returned. onToken may be nil.
func (c *Client) ChatStream(ctx context.Context, req ChatRequest, onToken func(token string)) (*ChatResponse, error) {
	body, ctOpt, err := jsonBody(req)
	if err != nil {
		return nil, err
	}
	hreq, err := c.newRequest(ctx, http.MethodPost, "/v1/chat/stream", body, ctOpt)
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Accept", "text/event-stream")
	resp, err := c.do(hreq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var final *ChatResponse
	err = scanSSE(resp.Body, func(event, data string) error {
		switch event {
		case "token":
			var tok string
			if err := json.Unmarshal([]byte(data), &tok); err != nil {
				// Tolerate a bare (non-JSON) token rather than failing the stream.
				tok = data
			}
			if onToken != nil {
				onToken(tok)
			}
		case "error":
			return &Error{Status: http.StatusBadGateway, Code: "StreamError", Message: unquote(data)}
		case "done":
			var cr ChatResponse
			if err := json.Unmarshal([]byte(data), &cr); err != nil {
				return fmt.Errorf("aerovault: decoding done frame: %w", err)
			}
			final = &cr
			return errStopSSE
		}
		return nil
	})
	if err != nil && err != errStopSSE {
		return final, err
	}
	if final == nil {
		final = &ChatResponse{}
	}
	return final, nil
}

// Agent runs the tool-calling agent loop (POST /v1/agent).
func (c *Client) Agent(ctx context.Context, query string) (*AgentResponse, error) {
	body, ctOpt, err := jsonBody(map[string]string{"query": query})
	if err != nil {
		return nil, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/agent", body, ctOpt)
	if err != nil {
		return nil, err
	}
	var out AgentResponse
	if err := c.doJSON(req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- ops ------------------------------------------------------------------

// Usage returns the current tenant's consumption and quota (GET /v1/usage).
func (c *Client) Usage(ctx context.Context) (*Usage, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/usage", nil)
	if err != nil {
		return nil, err
	}
	var u Usage
	if err := c.doJSON(req, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// Health reports whether the server's liveness probe returns 200
// (GET /healthz). A transport/network error is returned; a non-200 status
// simply yields (false, nil).
func (c *Client) Health(ctx context.Context) (bool, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/healthz", nil)
	if err != nil {
		return false, err
	}
	resp, err := c.do(req)
	if err != nil {
		var ae *Error
		if AsError(err, &ae) {
			return false, nil // reachable but not healthy
		}
		return false, err // transport error
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return true, nil
}

// ---- admin ----------------------------------------------------------------

// AddKey creates a persisted API key (POST /v1/admin/keys).
func (c *Client) AddKey(ctx context.Context, req AddKeyRequest) (map[string]any, error) {
	body, ctOpt, err := jsonBody(req)
	if err != nil {
		return nil, err
	}
	r, err := c.newRequest(ctx, http.MethodPost, "/v1/admin/keys", body, ctOpt)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	return out, c.doJSON(r, &out)
}

// ListKeys returns all persisted API keys (GET /v1/admin/keys).
func (c *Client) ListKeys(ctx context.Context) ([]APIKey, error) {
	r, err := c.newRequest(ctx, http.MethodGet, "/v1/admin/keys", nil)
	if err != nil {
		return nil, err
	}
	var env struct {
		Keys []APIKey `json:"keys"`
	}
	return env.Keys, c.doJSON(r, &env)
}

// RevokeKey deletes a persisted API key (DELETE /v1/admin/keys/{token}).
func (c *Client) RevokeKey(ctx context.Context, token string) error {
	r, err := c.newRequest(ctx, http.MethodDelete, "/v1/admin/keys/"+url.PathEscape(token), nil)
	if err != nil {
		return err
	}
	return c.doJSON(r, nil)
}

// IssueJWT signs and returns a short-lived JWT (POST /v1/admin/jwt).
func (c *Client) IssueJWT(ctx context.Context, req IssueJWTRequest) (*IssueJWTResponse, error) {
	body, ctOpt, err := jsonBody(req)
	if err != nil {
		return nil, err
	}
	r, err := c.newRequest(ctx, http.MethodPost, "/v1/admin/jwt", body, ctOpt)
	if err != nil {
		return nil, err
	}
	var out IssueJWTResponse
	return &out, c.doJSON(r, &out)
}

// ListWebhookFailures returns undelivered webhook attempts
// (GET /v1/admin/webhook-failures).
func (c *Client) ListWebhookFailures(ctx context.Context) ([]WebhookFailure, error) {
	r, err := c.newRequest(ctx, http.MethodGet, "/v1/admin/webhook-failures", nil)
	if err != nil {
		return nil, err
	}
	var env struct {
		Failures []WebhookFailure `json:"failures"`
	}
	return env.Failures, c.doJSON(r, &env)
}

// ListJobs returns background jobs and their status (GET /v1/admin/jobs).
func (c *Client) ListJobs(ctx context.Context) (map[string]any, error) {
	r, err := c.newRequest(ctx, http.MethodGet, "/v1/admin/jobs", nil)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	return out, c.doJSON(r, &out)
}

// RetryJob re-queues a failed job (POST /v1/admin/jobs/{id}/retry).
func (c *Client) RetryJob(ctx context.Context, jobID int64) (map[string]any, error) {
	path := fmt.Sprintf("/v1/admin/jobs/%d/retry", jobID)
	r, err := c.newRequest(ctx, http.MethodPost, path, nil)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	return out, c.doJSON(r, &out)
}

// CreateTenant creates or upserts a tenant record (POST /v1/admin/tenants).
func (c *Client) CreateTenant(ctx context.Context, tenantID, displayName string) (*TenantRecord, error) {
	body, ctOpt, err := jsonBody(map[string]string{"tenant_id": tenantID, "display_name": displayName})
	if err != nil {
		return nil, err
	}
	r, err := c.newRequest(ctx, http.MethodPost, "/v1/admin/tenants", body, ctOpt)
	if err != nil {
		return nil, err
	}
	var out TenantRecord
	return &out, c.doJSON(r, &out)
}

// ListTenants returns all tenant records (GET /v1/admin/tenants).
func (c *Client) ListTenants(ctx context.Context) ([]TenantRecord, error) {
	r, err := c.newRequest(ctx, http.MethodGet, "/v1/admin/tenants", nil)
	if err != nil {
		return nil, err
	}
	var env struct {
		Tenants []TenantRecord `json:"tenants"`
	}
	return env.Tenants, c.doJSON(r, &env)
}

// DeleteTenant removes a tenant (DELETE /v1/admin/tenants/{tenant}).
func (c *Client) DeleteTenant(ctx context.Context, tenantID string) error {
	r, err := c.newRequest(ctx, http.MethodDelete, "/v1/admin/tenants/"+url.PathEscape(tenantID), nil)
	if err != nil {
		return err
	}
	return c.doJSON(r, nil)
}

// SetTenantStatus sets a tenant's status to "active" or "disabled"
// (PUT /v1/admin/tenants/{tenant}/status).
func (c *Client) SetTenantStatus(ctx context.Context, tenantID, status string) (*TenantRecord, error) {
	body, ctOpt, err := jsonBody(map[string]string{"status": status})
	if err != nil {
		return nil, err
	}
	r, err := c.newRequest(ctx, http.MethodPut, "/v1/admin/tenants/"+url.PathEscape(tenantID)+"/status", body, ctOpt)
	if err != nil {
		return nil, err
	}
	var out TenantRecord
	return &out, c.doJSON(r, &out)
}

// ListAudit returns audit log entries (GET /v1/admin/audit).
// limit<=0 uses the server default (50). before is an optional RFC3339 cursor.
func (c *Client) ListAudit(ctx context.Context, limit int, before string) ([]AuditEntry, error) {
	params := map[string]string{}
	if limit > 0 {
		params["limit"] = strconv.Itoa(limit)
	}
	if before != "" {
		params["before"] = before
	}
	r, err := c.newRequest(ctx, http.MethodGet, "/v1/admin/audit", nil, withQuery(params))
	if err != nil {
		return nil, err
	}
	var env struct {
		Entries []AuditEntry `json:"entries"`
	}
	return env.Entries, c.doJSON(r, &env)
}

// SetQuota updates a tenant's storage quota
// (PUT /v1/admin/tenants/{tenant}/quota).
// Zero values are omitted (server keeps existing limits).
func (c *Client) SetQuota(ctx context.Context, tenantID string, maxBytes, maxObjects int64) (map[string]any, error) {
	m := map[string]int64{}
	if maxBytes > 0 {
		m["max_bytes"] = maxBytes
	}
	if maxObjects > 0 {
		m["max_objects"] = maxObjects
	}
	body, ctOpt, err := jsonBody(m)
	if err != nil {
		return nil, err
	}
	r, err := c.newRequest(ctx, http.MethodPut, "/v1/admin/tenants/"+url.PathEscape(tenantID)+"/quota", body, ctOpt)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	return out, c.doJSON(r, &out)
}

// SetBudget sets a tenant's daily AI spend limit in USD
// (PUT /v1/admin/tenants/{tenant}/budget).
func (c *Client) SetBudget(ctx context.Context, tenantID string, dailyUSD float64) (map[string]any, error) {
	body, ctOpt, err := jsonBody(map[string]float64{"daily_budget_usd": dailyUSD})
	if err != nil {
		return nil, err
	}
	r, err := c.newRequest(ctx, http.MethodPut, "/v1/admin/tenants/"+url.PathEscape(tenantID)+"/budget", body, ctOpt)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	return out, c.doJSON(r, &out)
}

// AsError unwraps err into an *Error if it (or anything it wraps) is one,
// storing it in *target and reporting true. It is a thin wrapper over
// errors.As kept here so callers need not import errors for the common case.
func AsError(err error, target **Error) bool {
	return errorsAs(err, target)
}
