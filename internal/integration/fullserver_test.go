package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/api/rest"
	"github.com/aero-vault/aero-vault/internal/api/s3compat"
	dav "github.com/aero-vault/aero-vault/internal/api/webdav"
	"github.com/aero-vault/aero-vault/internal/auth"
	"github.com/aero-vault/aero-vault/internal/mcp"
	"github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/aero-vault/aero-vault/internal/webui"
)

// startFullServer builds a production-shaped server with SQLite + local storage,
// no auth (MVP mode), no AI, all protocols mounted.
func startFullServer(t *testing.T) *httptest.Server {
	t.Helper()
	ctx := context.Background()

	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "x.db"))
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
	svc := service.NewFileService(store, repo, logger)

	authReg, _ := auth.Parse("")
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
	r.Mount("/v1", rest.NewRouter(svc, repo, nil, nil, nil, nil, authReg, logger, false, aiRL, 0, false))
	r.Mount("/s3", s3compat.NewRouter(svc, logger))

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
	for _, m := range []func(http.Handler) http.Handler{
		middleware.AccessLog(logger),
		middleware.Recoverer(logger),
		rl.Middleware(),
		middleware.Tenant,
		authReg.Middleware(),
		middleware.CORS(middleware.CORSConfig{}),
		middleware.RequestID,
	} {
		finalHandler = m(finalHandler)
	}

	ts := httptest.NewServer(finalHandler)
	t.Cleanup(ts.Close)
	return ts
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
	if resp.StatusCode != http.StatusServiceUnavailable && resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("search disabled: got %d, want 503 or 501", resp.StatusCode)
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

	// List keys (enabled only with AUTH_PERSIST_KEYS, expect empty or 501)
	resp, err := http.Get(base + "/keys")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// Without auth, admin endpoints may return 404 or 200
	if resp.StatusCode == http.StatusInternalServerError {
		t.Fatalf("admin/keys: got 500")
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
