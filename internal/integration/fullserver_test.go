package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	_ "modernc.org/sqlite"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/api/rest"
	"github.com/aero-vault/aero-vault/internal/api/s3compat"
	dav "github.com/aero-vault/aero-vault/internal/api/webdav"
	"github.com/aero-vault/aero-vault/internal/auth"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/events"
	"github.com/aero-vault/aero-vault/internal/mcp"
	"github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/server"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/aero-vault/aero-vault/internal/webui"
)

// allowAllProvider satisfies both access.Authorizer and
// s3compat.AuthorizationProvider (identical method shape): everything is
// allowed, so baseline harness tests exercise CRUD mechanics, not the
// fail-closed delete gates (which have dedicated tests).
type allowAllProvider struct{}

func (allowAllProvider) Authorize(context.Context, access.Principal, access.Action, access.Resource) (access.Decision, error) {
	return access.Decision{Allowed: true, Reason: "test_allow_all"}, nil
}

// fullServerHarness exposes the loopback server plus the repo/DSN the tests
// need to assert outbox/audit state (AC-3/AC-5).
type fullServerHarness struct {
	ts   *httptest.Server
	repo repository.Repository
	dsn  string
}

// startFullServer builds a production-shaped server with SQLite + local storage,
// no auth (MVP mode), no AI, all protocols mounted, and the event-outbox relay
// running with default options (always-on, production shape).
func startFullServer(t *testing.T) *httptest.Server {
	return startFullServerWithRelay(t, &events.EventOutboxRelayOptions{}).ts
}

// startFullServerWithRelay builds the same server but lets the caller inject
// the relay options (short poll, AuditSink) — the AC-3/AC-5 injection point
// (the harness constructs the relay directly; no env-var indirection).
func startFullServerWithRelay(t *testing.T, relayOpts *events.EventOutboxRelayOptions) *fullServerHarness {
	return startFullServerWithConfig(t, relayOpts, "", &config.Config{})
}

// startFullServerWithAuthAndRelay is startFullServerWithRelay plus static
// API-key auth (authKeys in Parse format, e.g. "opsecret:*:admin"); the
// empty-string case is exactly the no-auth harness.
func startFullServerWithAuthAndRelay(t *testing.T, relayOpts *events.EventOutboxRelayOptions, authKeys string) *fullServerHarness {
	return startFullServerWithConfig(t, relayOpts, authKeys, &config.Config{})
}

// startFullServerWithConfig is the parameterized core: relay options, static
// API-key auth and a config override (currently only App.MaxBodySize feeds
// the middleware chain).
func startFullServerWithConfig(t *testing.T, relayOpts *events.EventOutboxRelayOptions, authKeys string, cfg *config.Config) *fullServerHarness {
	t.Helper()
	ctx := context.Background()
	if cfg == nil {
		cfg = &config.Config{}
	}

	dsn := "file:" + filepath.Join(t.TempDir(), "x.db")
	repo, err := repository.Open(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	store, err := storage.NewLocal(storage.LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("local storage: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// CI-baseline shape: no access control configured, so the fail-closed
	// delete gate is opted out (ACCESS_DELETE_FAIL_CLOSED=false equivalent) —
	// this harness exercises protocol/CRUD mechanics, not the gate.
	svc := service.NewFileService(store, repo, logger).
		WithTenantStatusEnforcement().
		WithDeleteFailOpen(true)

	authReg, _ := auth.Parse(authKeys)
	authReg.WithPutPresigner(auth.NewPutPresigner("integration-presign-secret-32-bytes"))
	var rl, aiRL *middleware.RateLimiter

	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	r.Get("/readyz", func(w http.ResponseWriter, req *http.Request) {
		if err := repo.Ping(req.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	r.Get("/openapi.json", rest.OpenAPISpecHandler())
	r.Get("/docs", rest.SwaggerUIHandler())
	r.Mount("/v1", rest.NewRouter(svc, repo, nil, nil, nil, nil, authReg, logger, false, aiRL, nil, 0, false))
	r.Mount("/s3", s3compat.NewRouter(svc, logger, allowAllProvider{}))

	mcpServer := mcp.NewServer(svc, repo, nil, "default", logger)
	r.Method(http.MethodPost, "/mcp", mcp.HTTPHandler(mcpServer))
	r.Mount("/ui", webui.Handler())

	davH := dav.Handler("/webdav", svc, logger)
	dispatcher := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasPrefix(req.URL.Path, "/webdav") {
			davH.ServeHTTP(w, req)
			return
		}
		r.ServeHTTP(w, req)
	})

	var finalHandler http.Handler = dispatcher
	// Production 12-ring chain via the shared assembly point: the harness and
	// cmd/server cannot drift (internal-integration-harness-12ring-chain).
	// MaxBodySize comes from the config override; rl/concurrency are the
	// disabled (no-op) shapes.
	finalHandler = server.ApplyMiddleware(finalHandler, repo, authReg, rl, cfg, logger,
		middleware.NewConcurrencyLimiter(0).Middleware(), nil)

	ts := httptest.NewServer(finalHandler)
	t.Cleanup(ts.Close)
	if relayOpts != nil {
		relay := events.NewEventOutboxRelay(repo, logger, *relayOpts)
		relayCtx, relayCancel := context.WithCancel(ctx)
		go relay.Run(relayCtx)
		t.Cleanup(relayCancel)
	}
	return &fullServerHarness{ts: ts, repo: repo, dsn: dsn}
}

func httpPut(url, contentType, body string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	return http.DefaultClient.Do(req)
}

func TestFullServer_Healthz(t *testing.T) {
	ts := startFullServer(t)
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz: got %d, want 200", resp.StatusCode)
	}
}

func TestFullServer_Readyz(t *testing.T) {
	ts := startFullServer(t)
	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz: got %d, want 200", resp.StatusCode)
	}
}

func TestFullServer_REST_CRUD(t *testing.T) {
	ts := startFullServer(t)
	body := "hello world"
	key := "test/hello.txt"
	putURL := ts.URL + "/v1/files/" + key

	// PUT
	putResp, err := httpPut(putURL, "text/plain", body)
	if err != nil {
		t.Fatal(err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != 201 && putResp.StatusCode != 200 {
		t.Fatalf("PUT: got %d, want 201", putResp.StatusCode)
	}

	// GET
	getResp, err := http.Get(putURL)
	if err != nil {
		t.Fatal(err)
	}
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET: got %d, want 200", getResp.StatusCode)
	}
	got, _ := io.ReadAll(getResp.Body)
	getResp.Body.Close()
	if string(got) != body {
		t.Fatalf("GET body: got %q, want %q", string(got), body)
	}
	if getResp.Header.Get("Content-Type") != "text/plain" {
		t.Fatalf("Content-Type: got %q, want text/plain", getResp.Header.Get("Content-Type"))
	}
	etag := getResp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("ETag missing")
	}

	// HEAD
	headResp, err := http.Head(putURL)
	if err != nil {
		t.Fatal(err)
	}
	headResp.Body.Close()
	if headResp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD: got %d, want 200", headResp.StatusCode)
	}
	if headResp.Header.Get("ETag") != etag {
		t.Fatalf("HEAD ETag: got %q, want %q", headResp.Header.Get("ETag"), etag)
	}

	// 404
	nfResp, err := http.Get(ts.URL + "/v1/files/nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	nfResp.Body.Close()
	if nfResp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET nonexistent: got %d, want 404", nfResp.StatusCode)
	}

	// List
	listResp, err := http.Get(ts.URL + "/v1/files")
	if err != nil {
		t.Fatal(err)
	}
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("LIST: got %d, want 200", listResp.StatusCode)
	}
	var listRes struct {
		Objects []any `json:"objects"`
	}
	json.NewDecoder(listResp.Body).Decode(&listRes)
	listResp.Body.Close()
	if len(listRes.Objects) != 1 {
		t.Fatalf("LIST objects: got %d, want 1", len(listRes.Objects))
	}

	// DELETE
	delReq, _ := http.NewRequest(http.MethodDelete, putURL, nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE: got %d, want 204", delResp.StatusCode)
	}

	// GET after DELETE
	getAfterResp, _ := http.Get(putURL)
	getAfterResp.Body.Close()
	if getAfterResp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after DELETE: got %d, want 404", getAfterResp.StatusCode)
	}
}

func TestFullServer_Tags(t *testing.T) {
	ts := startFullServer(t)
	key := "tagged/doc.txt"
	putURL := ts.URL + "/v1/files/" + key

	httpPut(putURL, "text/plain", "tagged content")

	tags := map[string]string{"env": "test", "owner": "integration"}
	tagBody, _ := json.Marshal(tags)
	tagReq, _ := http.NewRequest(http.MethodPut, putURL+"/tags", bytes.NewReader(tagBody))
	tagReq.Header.Set("Content-Type", "application/json")
	tagResp, err := http.DefaultClient.Do(tagReq)
	if err != nil {
		t.Fatal(err)
	}
	tagResp.Body.Close()
	if tagResp.StatusCode != http.StatusOK && tagResp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT tags: got %d", tagResp.StatusCode)
	}

	// GET tags
	tagGetResp, err := http.Get(putURL + "/tags")
	if err != nil {
		t.Fatal(err)
	}
	defer tagGetResp.Body.Close()
	if tagGetResp.StatusCode != http.StatusOK {
		t.Fatalf("GET tags: got %d, want 200", tagGetResp.StatusCode)
	}
}

func TestFullServer_S3Compat(t *testing.T) {
	ts := startFullServer(t)
	s3Key := "s3-test/object.bin"
	s3URL := ts.URL + "/s3/default/" + s3Key
	body := "s3 content"

	// S3 PUT
	putResp, err := httpPut(s3URL, "application/octet-stream", body)
	if err != nil {
		t.Fatal(err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("S3 PUT: got %d, want 200", putResp.StatusCode)
	}

	// S3 GET
	getResp, err := http.Get(s3URL)
	if err != nil {
		t.Fatal(err)
	}
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("S3 GET: got %d, want 200", getResp.StatusCode)
	}
	got, _ := io.ReadAll(getResp.Body)
	getResp.Body.Close()
	if string(got) != body {
		t.Fatalf("S3 GET body: got %q, want %q", string(got), body)
	}

	// S3 HEAD
	headResp, err := http.Head(s3URL)
	if err != nil {
		t.Fatal(err)
	}
	headResp.Body.Close()
	if headResp.StatusCode != http.StatusOK {
		t.Fatalf("S3 HEAD: got %d, want 200", headResp.StatusCode)
	}
}

func TestFullServer_SearchDisabled(t *testing.T) {
	ts := startFullServer(t)
	body := `{"query":"test","k":5}`
	resp, err := http.Post(ts.URL+"/v1/search", "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("search disabled: got %d, want 503", resp.StatusCode)
	}
}

func TestFullServer_MCP(t *testing.T) {
	ts := startFullServer(t)

	httpPut(ts.URL+"/v1/files/mcp-test/doc.txt", "text/plain", "mcp content")

	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
	}
	body, _ := json.Marshal(req)
	resp, err := http.Post(ts.URL+"/mcp", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("MCP tools/list: got %d, want 200", resp.StatusCode)
	}
	var mcpRes map[string]any
	json.NewDecoder(resp.Body).Decode(&mcpRes)
	resp.Body.Close()
	if mcpRes["result"] == nil {
		t.Fatal("MCP tools/list: result is nil")
	}
}

func TestFullServer_WebUI(t *testing.T) {
	ts := startFullServer(t)
	resp, err := http.Get(ts.URL + "/ui/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("WebUI: got %d, want 200", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("WebUI Content-Type: got %q, want text/html", ct)
	}
}

func TestFullServer_OpenAPI(t *testing.T) {
	ts := startFullServer(t)
	resp, err := http.Get(ts.URL + "/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("OpenAPI: got %d, want 200", resp.StatusCode)
	}
}

func TestFullServer_CORS(t *testing.T) {
	ts := startFullServer(t)
	req, _ := http.NewRequest(http.MethodOptions, ts.URL+"/v1/files", nil)
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	_ = resp.Header.Get("Access-Control-Allow-Origin")
}

func TestFullServer_ProtocolInterop(t *testing.T) {
	ts := startFullServer(t)
	body := "interop content"
	key := "interop/shared.txt"

	// Write through REST
	putResp, err := httpPut(ts.URL+"/v1/files/"+key, "text/plain", body)
	if err != nil {
		t.Fatal(err)
	}
	putResp.Body.Close()

	// Verify it's accessible through S3-compat endpoint
	s3Resp, err := http.Get(ts.URL + "/s3/default/" + key)
	if err != nil {
		t.Fatal(err)
	}
	s3Resp.Body.Close()
	if s3Resp.StatusCode != http.StatusOK {
		t.Fatalf("S3 GET after REST PUT: got %d, want 200", s3Resp.StatusCode)
	}

	// Also accessible through MCP resources endpoint
	mcpReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "resources/list",
	}
	mcpBody, _ := json.Marshal(mcpReq)
	mcpResp, err := http.Post(ts.URL+"/mcp", "application/json", bytes.NewReader(mcpBody))
	if err != nil {
		t.Fatal(err)
	}
	mcpResp.Body.Close()
	if mcpResp.StatusCode != http.StatusOK {
		t.Fatalf("MCP resources/list: got %d, want 200", mcpResp.StatusCode)
	}

	// LIST via REST now has 1 object (the one we created)
	listResp, err := http.Get(ts.URL + "/v1/files")
	if err != nil {
		t.Fatal(err)
	}
	var listRes struct {
		Objects []any `json:"objects"`
	}
	json.NewDecoder(listResp.Body).Decode(&listRes)
	listResp.Body.Close()
	if len(listRes.Objects) == 0 {
		t.Fatal("interop: LIST returned 0 objects")
	}
}

func TestFullServer_RangeRequest(t *testing.T) {
	ts := startFullServer(t)
	body := "0123456789"
	key := "range/test.txt"

	httpPut(ts.URL+"/v1/files/"+key, "text/plain", body)

	req, _ := http.NewRequest("GET", ts.URL+"/v1/files/"+key, nil)
	req.Header.Set("Range", "bytes=0-4")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("Range: got %d, want 206", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if string(got) != "01234" {
		t.Fatalf("Range body: got %q, want %q", string(got), "01234")
	}
	if resp.Header.Get("Content-Range") == "" {
		t.Fatal("Range: Content-Range header missing")
	}
}

func TestFullServer_AdminEndpoints(t *testing.T) {
	ts := startFullServer(t)
	base := ts.URL + "/v1/admin"

	// The registry exists even with no configured credentials, so the list is
	// available and empty in the unauthenticated baseline.
	resp, err := http.Get(base + "/keys")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin/keys: got %d, want 200", resp.StatusCode)
	}
}

func TestFullServer_QuotaAndBudget(t *testing.T) {
	ts := startFullServer(t)
	cl := ts.Client()
	base := ts.URL + "/v1/admin"

	// Set quota
	body := `{"max_bytes":1000000,"max_objects":100}`
	req, _ := http.NewRequest(http.MethodPut, base+"/tenants/default/quota", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatalf("SetQuota: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SetQuota: got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Set budget
	body2 := `{"daily_budget_usd":5.0}`
	req2, _ := http.NewRequest(http.MethodPut, base+"/tenants/default/budget", strings.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	resp, err = cl.Do(req2)
	if err != nil {
		t.Fatalf("SetBudget: %v", err)
	}
	resp.Body.Close()

	// Get usage
	resp, err = cl.Get(ts.URL + "/v1/usage")
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Usage: got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestFullServer_MultipartUpload(t *testing.T) {
	srv := startFullServer(t)
	cl := srv.Client()
	base := srv.URL

	// Step 1: Initiate multipart upload.
	initBody := `{"key":"multi.txt","content_type":"text/plain"}`
	resp, err := cl.Post(base+"/v1/multipart", "application/json", strings.NewReader(initBody))
	if err != nil {
		t.Fatalf("InitMultipart: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("InitMultipart: got %d", resp.StatusCode)
	}
	var initResp struct {
		UploadID string `json:"upload_id"`
		Key      string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&initResp); err != nil {
		t.Fatalf("decode init: %v", err)
	}
	resp.Body.Close()
	if initResp.UploadID == "" {
		t.Fatal("empty upload_id")
	}

	// Step 2: Upload a part.
	partURL := fmt.Sprintf("%s/v1/multipart/%s/parts/1", base, initResp.UploadID)
	uploadReq, _ := http.NewRequest(http.MethodPut, partURL, strings.NewReader("part1 data"))
	resp, err = cl.Do(uploadReq)
	if err != nil {
		t.Fatalf("UploadPart: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("UploadPart: got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Step 3: Complete multipart.
	completeURL := fmt.Sprintf("%s/v1/multipart/%s/complete", base, initResp.UploadID)
	resp, err = cl.Post(completeURL, "application/json", nil)
	if err != nil {
		t.Fatalf("CompleteMultipart: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CompleteMultipart: got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Step 4: Verify the object exists.
	getResp, err := cl.Get(base + "/v1/files/multi.txt")
	if err != nil {
		t.Fatalf("GET after multipart: %v", err)
	}
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET after multipart: got %d", getResp.StatusCode)
	}
	body, _ := io.ReadAll(getResp.Body)
	getResp.Body.Close()
	if string(body) != "part1 data" {
		t.Fatalf("body = %q, want %q", string(body), "part1 data")
	}
}

func TestFullServer_ConcurrentCRUD(t *testing.T) {
	ts := startFullServer(t)
	n := 5
	errCh := make(chan error, n)

	for i := 0; i < n; i++ {
		i := i
		go func() {
			key := fmt.Sprintf("concurrent/%d", i)
			body := fmt.Sprintf("body-%d", i)
			resp, err := httpPut(ts.URL+"/v1/files/"+key, "text/plain", body)
			if err != nil {
				errCh <- err
				return
			}
			resp.Body.Close()
			if resp.StatusCode != 201 && resp.StatusCode != 200 {
				errCh <- fmt.Errorf("PUT %s: got %d", key, resp.StatusCode)
				return
			}

			getResp, err := http.Get(ts.URL + "/v1/files/" + key)
			if err != nil {
				errCh <- err
				return
			}
			got, _ := io.ReadAll(getResp.Body)
			getResp.Body.Close()
			if string(got) != body {
				errCh <- fmt.Errorf("GET %s: got %q, want %q", key, string(got), body)
				return
			}
			errCh <- nil
		}()
	}

	for i := 0; i < n; i++ {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
}

// ── AC-3: durable_async — DELETE never waits for L2 delivery ────────────────

// TestDeleteResponse_DoesNotBlockOnDelivery is signal-based, not wall-clock: a
// synchronous implementation cannot return the DELETE response while the L2
// target is still blocked (its POST would hang until the 5s relay timeout), so
// the 4s hang-guard (strictly below the 5s timeout) is a deterministic
// discriminator. After the response, the outbox fact must be pending/inflight
// (delivered is unreachable while the target blocks), and the audit_log row
// must already exist (L0 is never affected by delivery, F4).
func TestDeleteResponse_DoesNotBlockOnDelivery(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	var mu sync.Mutex
	var posts int

	l2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // block the L2 POST until the test releases it
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		posts++
		mu.Unlock()
		w.Header().Set("X-Audit-Fact-Id", r.Header.Get("X-Audit-Fact-Id"))
		w.WriteHeader(http.StatusOK)
	}))
	// Cleanup order (LIFO): release → l2.Close → relay cancel → ts.Close.
	// close(release) MUST run before l2.Close so the in-flight POST completes
	// instead of leaking a goroutine under -race (AC-3 ⑤).
	t.Cleanup(l2.Close)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	sink, err := events.NewAuditSinkL2(l2.URL, map[string]string{"default": "e2e-l2-token-0123456789"},
		&http.Client{Timeout: 5 * time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := startFullServerWithRelay(t, &events.EventOutboxRelayOptions{
		PollInterval: 50 * time.Millisecond, BatchSize: 32,
		ClaimTTL: 30 * time.Second, HTTPTimeout: 5 * time.Second,
		MaxAttempts: 3, AuditSink: sink,
	})
	ts := h.ts

	key := "ac3-blocked.txt"
	if resp, err := httpPut(ts.URL+"/v1/files/"+key, "text/plain", "body"); err != nil || resp.StatusCode >= 300 {
		t.Fatalf("PUT: status=%v err=%v", resp.StatusCode, err)
	} else {
		resp.Body.Close()
	}
	obj, err := h.repo.GetObject(context.Background(), "default", "default", key)
	if err != nil {
		t.Fatalf("get object: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/v1/files/"+key+"?hard=1", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- err
			return
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			done <- fmt.Errorf("DELETE status = %d, want 204", resp.StatusCode)
			return
		}
		done <- nil
	}()

	// The L2 target is still blocked here: a synchronous implementation
	// cannot have returned the response (its POST would hang past the 5s
	// relay timeout; our guard is 4s < 5s, so the discriminator is exact).
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("DELETE failed: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("DELETE response blocked behind L2 delivery (durable_async violated)")
	}

	// The outbox fact must not be delivered while the target is blocked;
	// pending (not yet claimed) and inflight (claimed, POST in progress) are
	// both legal and race-free — delivered is unreachable.
	status := outboxStatus(t, h.dsn, obj.ID, "vault.file.deleted@1.1")
	if status != "pending" && status != "inflight" {
		t.Fatalf("outbox status = %q, want pending or inflight while target blocked", status)
	}
	assertAuditRowFor(t, h.repo, "default", "hard")

	// Recovery: release the target; the in-flight POST completes and the relay
	// completes the fact (in-flight POST ≤5s + 50ms poll + 1s±25% backoff →
	// ~15s bound).
	releaseOnce.Do(func() { close(release) })
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if outboxStatus(t, h.dsn, obj.ID, "vault.file.deleted@1.1") == "delivered" {
			mu.Lock()
			got := posts
			mu.Unlock()
			if got < 1 {
				t.Fatalf("L2 received %d POSTs, want ≥1", got)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("fact never reached delivered after target recovery")
}

// ── AC-5: composition — bound tenant delivers, unbound tenant degrades ──────

func TestComposition_AuditSinkL2BoundTenant(t *testing.T) {
	var mu sync.Mutex
	var bodies [][]byte
	l2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.Header().Set("X-Audit-Fact-Id", r.Header.Get("X-Audit-Fact-Id"))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(l2.Close)
	sink, err := events.NewAuditSinkL2(l2.URL, map[string]string{"t1": "e2e-l2-t1-token-0123456789"},
		&http.Client{Timeout: 5 * time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := startFullServerWithRelay(t, &events.EventOutboxRelayOptions{
		PollInterval: 50 * time.Millisecond, BatchSize: 32,
		ClaimTTL: 30 * time.Second, HTTPTimeout: 5 * time.Second,
		MaxAttempts: 3, AuditSink: sink,
	})
	ts := h.ts
	ctx := context.Background()

	// ② bound tenant t1: PUT → DELETE → L2 receives exactly one POST with the
	// AC-4 envelope; audit_log has the t1 row.
	putWithTenant(t, ts, "t1", "ac5/t1.txt")
	t1obj, err := h.repo.GetObject(ctx, "t1", "default", "ac5/t1.txt")
	if err != nil {
		t.Fatal(err)
	}
	deleteWithTenant(t, ts, "t1", "ac5/t1.txt", true)
	waitForBodies(t, func() int { mu.Lock(); defer mu.Unlock(); return len(bodies) }, 1, 5*time.Second)
	mu.Lock()
	body := string(bodies[0])
	mu.Unlock()
	if !strings.Contains(body, `"event_type":"vault.file.deleted@1.1"`) ||
		!strings.Contains(body, `"tenant":"t1"`) ||
		!strings.Contains(body, `"object_id":`+fmt.Sprintf("%d", t1obj.ID)) {
		t.Errorf("L2 payload missing AC-4 identity: %s", body)
	}
	assertAuditRowFor(t, h.repo, "t1", "hard")

	// ③ unbound tenant t2: PUT → DELETE → L2 receives no POST; audit_log still
	// has the t2 row (L0 is per-tenant always-on).
	putWithTenant(t, ts, "t2", "ac5/t2.txt")
	deleteWithTenant(t, ts, "t2", "ac5/t2.txt", true)
	time.Sleep(400 * time.Millisecond) // several relay polls at 50ms
	mu.Lock()
	got := len(bodies)
	mu.Unlock()
	if got != 1 {
		t.Fatalf("L2 received %d POSTs after unbound delete, want 1 (t2 must not deliver)", got)
	}
	assertAuditRowFor(t, h.repo, "t2", "hard")

	// ④ no-L2 control server: delete still 2xx + audit row, and the always-on
	// relay completes the deleted@1.1 fact (degraded record-retention, C3).
	h2 := startFullServerWithRelay(t, &events.EventOutboxRelayOptions{})
	putWithTenant(t, h2.ts, "default", "ac5/control.txt")
	control, err := h2.repo.GetObject(ctx, "default", "default", "ac5/control.txt")
	if err != nil {
		t.Fatal(err)
	}
	deleteWithTenant(t, h2.ts, "default", "ac5/control.txt", true)
	assertAuditRowFor(t, h2.repo, "default", "hard")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if outboxStatus(t, h2.dsn, control.ID, "vault.file.deleted@1.1") == "delivered" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("control server fact never completed by the always-on relay")
}

// ── helpers ─────────────────────────────────────────────────────────────────

func putWithTenant(t *testing.T, ts *httptest.Server, tenant, key string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, ts.URL+"/v1/files/"+key, strings.NewReader("body"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "text/plain")
	if tenant != "" {
		req.Header.Set(middleware.TenantHeader, tenant)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("PUT %s: status %d", key, resp.StatusCode)
	}
}

func deleteWithTenant(t *testing.T, ts *httptest.Server, tenant, key string, hard bool) {
	t.Helper()
	hardQuery := ""
	if hard {
		hardQuery = "?hard=1"
	}
	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/v1/files/"+key+hardQuery, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tenant != "" {
		req.Header.Set(middleware.TenantHeader, tenant)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE %s (tenant=%s): status %d, want 204", key, tenant, resp.StatusCode)
	}
}

// outboxStatus reads one outbox fact's status through a raw SQLite connection
// (the Repository interface exposes no status reader; the relay owns claims).
func outboxStatus(t *testing.T, dsn string, originID int64, eventType string) string {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var status string
	if err := db.QueryRow(`SELECT status FROM event_outbox
WHERE origin_id=? AND event_type=? ORDER BY id DESC LIMIT 1`, originID, eventType).Scan(&status); err != nil {
		t.Fatalf("query outbox status: %v", err)
	}
	return status
}

// assertAuditRowFor asserts a file.delete audit row exists for the tenant with
// the expected detail (newest first).
func assertAuditRowFor(t *testing.T, repo repository.Repository, tenant, detail string) {
	t.Helper()
	rows, err := repo.ListAudit(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Action == repository.AuditActionFileDelete && row.TenantID == tenant {
			if row.Detail != detail {
				t.Errorf("audit detail = %q, want %q", row.Detail, detail)
			}
			return
		}
	}
	t.Fatalf("no file.delete audit row for tenant %s", tenant)
}

func waitForBodies(t *testing.T, count func() int, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if count() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("L2 received %d POSTs, want %d", count(), want)
}
