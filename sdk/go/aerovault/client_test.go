package aerovault

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recorded captures what the stub server saw on a single request. path is the
// decoded URL path; escPath is the on-the-wire (percent-encoded) form, used to
// assert key escaping.
type recorded struct {
	method  string
	path    string
	escPath string
	rawQ    string
	header  http.Header
	body    []byte
}

// newStub spins up an httptest server whose handler records the request and
// then runs respond to write the reply. The request body is captured and then
// restored on r.Body so respond can still read it. It returns a Client pointed
// at the server plus a pointer to the latest recorded request.
func newStub(t *testing.T, respond func(w http.ResponseWriter, r *http.Request), opts ...Option) (*Client, *recorded) {
	t.Helper()
	rec := &recorded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		rec.method = r.Method
		rec.path = r.URL.Path
		rec.escPath = r.URL.EscapedPath()
		rec.rawQ = r.URL.RawQuery
		rec.header = r.Header.Clone()
		rec.body = body
		r.Body = io.NopCloser(bytes.NewReader(body)) // let respond re-read it
		respond(w, r)
	}))
	t.Cleanup(srv.Close)

	all := append([]Option{WithToken("tok-123"), WithTenant("acme")}, opts...)
	c, err := New(srv.URL, all...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, rec
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ---- header / auth construction ------------------------------------------

func TestCommonHeaders(t *testing.T) {
	tests := []struct {
		name       string
		opts       []Option
		wantAuth   string
		wantAPIKey string
		wantTenant string
	}{
		{
			name:       "bearer token default tenant",
			opts:       []Option{WithToken("abc")},
			wantAuth:   "Bearer abc",
			wantTenant: "default",
		},
		{
			name:       "api key header",
			opts:       []Option{WithToken("abc"), WithAPIKeyHeader(), WithTenant("t2")},
			wantAPIKey: "abc",
			wantTenant: "t2",
		},
		{
			name:       "no token",
			opts:       []Option{WithTenant("t3")},
			wantTenant: "t3",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorded{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				rec.header = r.Header.Clone()
				writeJSON(w, 200, map[string]any{})
			}))
			defer srv.Close()
			c, err := New(srv.URL, tc.opts...)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Usage(context.Background()); err != nil {
				t.Fatalf("Usage: %v", err)
			}
			if got := rec.header.Get("Authorization"); got != tc.wantAuth {
				t.Errorf("Authorization = %q, want %q", got, tc.wantAuth)
			}
			if got := rec.header.Get("X-Api-Key"); got != tc.wantAPIKey {
				t.Errorf("X-Api-Key = %q, want %q", got, tc.wantAPIKey)
			}
			if got := rec.header.Get("X-Aero-Tenant"); got != tc.wantTenant {
				t.Errorf("X-Aero-Tenant = %q, want %q", got, tc.wantTenant)
			}
			if got := rec.header.Get("Accept"); got == "" {
				t.Errorf("Accept header not set")
			}
		})
	}
}

func TestNewTrimsTrailingSlashAndValidates(t *testing.T) {
	c, err := New("http://x.example:8080/")
	if err != nil {
		t.Fatal(err)
	}
	if c.baseURL != "http://x.example:8080" {
		t.Errorf("baseURL = %q, want trailing slash trimmed", c.baseURL)
	}
	if _, err := New(""); err == nil {
		t.Errorf("New(\"\") should error")
	}
}

// ---- upload ---------------------------------------------------------------

func TestUpload(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		opts     UploadOptions
		body     string
		wantCT   string
		wantPath string
		wantMeta map[string]string
		wantCLen int64
	}{
		{
			name:     "explicit content type and metadata",
			key:      "docs/readme.txt",
			opts:     UploadOptions{ContentType: "text/plain", Metadata: map[string]string{"author": "ada", "team": "core"}},
			body:     "hello world",
			wantCT:   "text/plain",
			wantPath: "/v1/files/docs/readme.txt",
			wantMeta: map[string]string{"X-Meta-author": "ada", "X-Meta-team": "core"},
		},
		{
			name:     "inferred content type from extension",
			key:      "a/b/data.json",
			opts:     UploadOptions{},
			body:     `{"k":1}`,
			wantCT:   "application/json",
			wantPath: "/v1/files/a/b/data.json",
		},
		{
			name:     "unknown extension falls back to octet-stream",
			key:      "blob.weirdext",
			opts:     UploadOptions{},
			body:     "x",
			wantCT:   "application/octet-stream",
			wantPath: "/v1/files/blob.weirdext",
		},
		{
			name:     "key with leading slash and space gets escaped",
			key:      "/my docs/a+b.txt",
			opts:     UploadOptions{ContentType: "text/plain", Size: 3},
			body:     "abc",
			wantCT:   "text/plain",
			wantPath: "/v1/files/my%20docs/a+b.txt",
			wantCLen: 3,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := newStub(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, 201, Object{Bucket: "default", Key: r.URL.Path, Size: int64(len(tc.body)), ETag: "etag-1", ContentType: tc.wantCT})
			})
			obj, err := c.Upload(context.Background(), tc.key, strings.NewReader(tc.body), tc.opts)
			if err != nil {
				t.Fatalf("Upload: %v", err)
			}
			if rec.method != http.MethodPut {
				t.Errorf("method = %s, want PUT", rec.method)
			}
			// wantPath is the on-the-wire (percent-encoded) form; compare against
			// the escaped path so key escaping is verified.
			if rec.escPath != tc.wantPath {
				t.Errorf("escaped path = %q, want %q", rec.escPath, tc.wantPath)
			}
			if got := rec.header.Get("Content-Type"); got != tc.wantCT {
				t.Errorf("Content-Type = %q, want %q", got, tc.wantCT)
			}
			if string(rec.body) != tc.body {
				t.Errorf("body = %q, want %q", rec.body, tc.body)
			}
			for k, v := range tc.wantMeta {
				if got := rec.header.Get(k); got != v {
					t.Errorf("header %s = %q, want %q", k, got, v)
				}
			}
			if obj.ETag != "etag-1" {
				t.Errorf("decoded ETag = %q, want etag-1", obj.ETag)
			}
		})
	}
}

// ---- get / range / download / stat ---------------------------------------

func TestGetAndDownload(t *testing.T) {
	payload := "the quick brown fox"
	c, rec := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("ETag", `"abc123"`)
		w.Header().Set("Content-Length", "19")
		w.Header().Set("Last-Modified", "Wed, 21 Oct 2026 07:28:00 GMT")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, payload)
	})

	rc, obj, err := c.Get(context.Background(), "docs/a.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != payload {
		t.Errorf("body = %q, want %q", got, payload)
	}
	if rec.method != http.MethodGet || rec.path != "/v1/files/docs/a.txt" {
		t.Errorf("got %s %s", rec.method, rec.path)
	}
	if obj.ETag != "abc123" {
		t.Errorf("ETag = %q, want abc123 (quotes stripped)", obj.ETag)
	}
	if obj.Size != 19 {
		t.Errorf("Size = %d, want 19", obj.Size)
	}
	if obj.ContentType != "text/plain" {
		t.Errorf("ContentType = %q", obj.ContentType)
	}
	if obj.UpdatedAt.IsZero() {
		t.Errorf("UpdatedAt not parsed from Last-Modified")
	}
}

func TestGetVersionQuery(t *testing.T) {
	c, rec := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "v2 bytes")
	})
	rc, _, err := c.GetVersion(context.Background(), "f.txt", "ver-9")
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
	if rec.rawQ != "version=ver-9" {
		t.Errorf("query = %q, want version=ver-9", rec.rawQ)
	}
}

func TestGetRange(t *testing.T) {
	tests := []struct {
		name      string
		offset    int64
		length    int64
		wantRange string
	}{
		{"bounded range", 10, 5, "bytes=10-14"},
		{"open ended range", 100, 0, "bytes=100-"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := newStub(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Range", "bytes 10-14/100")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = io.WriteString(w, "abcde")
			})
			rc, _, err := c.GetRange(context.Background(), "f.bin", tc.offset, tc.length)
			if err != nil {
				t.Fatalf("GetRange: %v", err)
			}
			rc.Close()
			if got := rec.header.Get("Range"); got != tc.wantRange {
				t.Errorf("Range = %q, want %q", got, tc.wantRange)
			}
		})
	}
}

func TestDownloadWritesBytes(t *testing.T) {
	c, _ := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "0123456789")
	})
	var sb strings.Builder
	n, err := c.Download(context.Background(), "f", &sb)
	if err != nil {
		t.Fatal(err)
	}
	if n != 10 || sb.String() != "0123456789" {
		t.Errorf("n=%d body=%q", n, sb.String())
	}
}

func TestStatAndExists(t *testing.T) {
	c, rec := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "missing") {
			writeJSON(w, 404, errEnvelope("NotFound", "object not found", "req-1"))
			return
		}
		w.Header().Set("Content-Length", "42")
		w.Header().Set("ETag", `"e1"`)
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(200)
	})

	obj, err := c.Stat(context.Background(), "img.png")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if rec.method != http.MethodHead {
		t.Errorf("method = %s, want HEAD", rec.method)
	}
	if obj.Size != 42 || obj.ETag != "e1" || obj.ContentType != "image/png" {
		t.Errorf("stat got %+v", obj)
	}

	ok, err := c.Exists(context.Background(), "img.png")
	if err != nil || !ok {
		t.Errorf("Exists(present) = %v, %v; want true, nil", ok, err)
	}
	ok, err = c.Exists(context.Background(), "missing")
	if err != nil || ok {
		t.Errorf("Exists(missing) = %v, %v; want false, nil", ok, err)
	}
}

// ---- list / iterate -------------------------------------------------------

func TestList(t *testing.T) {
	c, rec := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, ListPage{
			Objects:    []Object{{Key: "p/a"}, {Key: "p/b"}},
			NextMarker: "p/b",
			HasMore:    true,
		})
	})
	page, err := c.List(context.Background(), ListOptions{Prefix: "p/", Limit: 2, Marker: "m0"})
	if err != nil {
		t.Fatal(err)
	}
	q := parseQuery(rec.rawQ)
	if q["prefix"] != "p/" || q["limit"] != "2" || q["marker"] != "m0" {
		t.Errorf("query = %v", q)
	}
	if len(page.Objects) != 2 || !page.HasMore || page.NextMarker != "p/b" {
		t.Errorf("page = %+v", page)
	}
}

func TestIterObjectsPaginates(t *testing.T) {
	// Two pages: first has_more=true with marker, second has_more=false.
	var calls int
	c, _ := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		marker := r.URL.Query().Get("marker")
		if marker == "" {
			writeJSON(w, 200, ListPage{Objects: []Object{{Key: "1"}, {Key: "2"}}, NextMarker: "2", HasMore: true})
		} else {
			writeJSON(w, 200, ListPage{Objects: []Object{{Key: "3"}}, HasMore: false})
		}
	})

	var keys []string
	err := c.IterObjects(context.Background(), "", 0, func(o Object) error {
		keys = append(keys, o.Key)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(keys, ",") != "1,2,3" {
		t.Errorf("keys = %v", keys)
	}
	if calls != 2 {
		t.Errorf("server calls = %d, want 2", calls)
	}
}

func TestIterObjectsCallbackStops(t *testing.T) {
	c, _ := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, ListPage{Objects: []Object{{Key: "1"}, {Key: "2"}}, NextMarker: "2", HasMore: true})
	})
	sentinel := errors.New("stop")
	var seen int
	err := c.IterObjects(context.Background(), "", 0, func(o Object) error {
		seen++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel", err)
	}
	if seen != 1 {
		t.Errorf("callback ran %d times, want 1", seen)
	}
}

// ---- delete ---------------------------------------------------------------

func TestDelete(t *testing.T) {
	tests := []struct {
		name  string
		hard  bool
		wantQ string
	}{
		{"soft delete", false, ""},
		{"hard delete", true, "hard=1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := newStub(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})
			if err := c.Delete(context.Background(), "f.txt", tc.hard); err != nil {
				t.Fatal(err)
			}
			if rec.method != http.MethodDelete {
				t.Errorf("method = %s", rec.method)
			}
			if rec.rawQ != tc.wantQ {
				t.Errorf("query = %q, want %q", rec.rawQ, tc.wantQ)
			}
		})
	}
}

// ---- tags / versions / acl / thumbnail / presign --------------------------

func TestTags(t *testing.T) {
	// GET tags returns the {"tags":{...}} envelope.
	c, rec := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, 200, map[string]any{"tags": map[string]string{"env": "prod"}})
		case http.MethodPut:
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			writeJSON(w, 200, map[string]any{"tags": body})
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		}
	})

	got, err := c.GetTags(context.Background(), "f")
	if err != nil {
		t.Fatal(err)
	}
	if got["env"] != "prod" {
		t.Errorf("GetTags = %v", got)
	}
	if rec.path != "/v1/files/f/tags" {
		t.Errorf("path = %q", rec.path)
	}

	put, err := c.PutTags(context.Background(), "f", map[string]string{"a": "1", "b": "2"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodPut || put["a"] != "1" || put["b"] != "2" {
		t.Errorf("PutTags method=%s result=%v", rec.method, put)
	}
	if ct := rec.header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("PutTags Content-Type = %q", ct)
	}

	if err := c.DeleteTags(context.Background(), "f"); err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodDelete {
		t.Errorf("DeleteTags method = %s", rec.method)
	}
}

func TestListVersions(t *testing.T) {
	c, rec := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"versions": []map[string]any{
			{"version_id": "v1", "size": 10, "etag": "e1"},
			{"version_id": "v2", "size": 20, "etag": "e2"},
		}})
	})
	vs, err := c.ListVersions(context.Background(), "f.txt")
	if err != nil {
		t.Fatal(err)
	}
	if rec.path != "/v1/files/f.txt/versions" {
		t.Errorf("path = %q", rec.path)
	}
	if len(vs) != 2 || vs[0].VersionID != "v1" || vs[1].Size != 20 {
		t.Errorf("versions = %+v", vs)
	}
}

func TestACL(t *testing.T) {
	c, rec := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			writeJSON(w, 200, map[string]string{"acl": b["acl"]})
			return
		}
		writeJSON(w, 200, map[string]string{"acl": "private"})
	})

	acl, err := c.GetACL(context.Background(), "f")
	if err != nil || acl != "private" {
		t.Fatalf("GetACL = %q, %v", acl, err)
	}
	if rec.path != "/v1/files/f/acl" {
		t.Errorf("path = %q", rec.path)
	}

	if err := c.SetACL(context.Background(), "f", "public-read"); err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodPut {
		t.Errorf("SetACL method = %s", rec.method)
	}
	var sent map[string]string
	_ = json.Unmarshal(rec.body, &sent)
	if sent["acl"] != "public-read" {
		t.Errorf("SetACL body = %v", sent)
	}
}

func TestThumbnail(t *testing.T) {
	c, rec := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(200)
		_, _ = w.Write([]byte{0xFF, 0xD8, 0xFF}) // JPEG magic
	})
	b, err := c.Thumbnail(context.Background(), "img.png", 128, 64)
	if err != nil {
		t.Fatal(err)
	}
	if rec.path != "/v1/files/img.png/thumbnail" {
		t.Errorf("path = %q", rec.path)
	}
	q := parseQuery(rec.rawQ)
	if q["w"] != "128" || q["h"] != "64" {
		t.Errorf("query = %v", q)
	}
	if len(b) != 3 || b[0] != 0xFF {
		t.Errorf("thumb bytes = %v", b)
	}
	if got := rec.header.Get("Accept"); got != "image/jpeg" {
		t.Errorf("Accept = %q, want image/jpeg", got)
	}
}

func TestPresign(t *testing.T) {
	c, rec := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"url": "http://signed/url?sig=x", "expires": "2026-05-24T12:00:00Z"})
	})
	p, err := c.Presign(context.Background(), "f", "put", 600)
	if err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodPost || rec.path != "/v1/files/f/presign" {
		t.Errorf("got %s %s", rec.method, rec.path)
	}
	q := parseQuery(rec.rawQ)
	if q["op"] != "put" || q["expires"] != "600" {
		t.Errorf("query = %v", q)
	}
	if p.URL != "http://signed/url?sig=x" {
		t.Errorf("url = %q", p.URL)
	}
	if p.Expires.IsZero() {
		t.Errorf("expires not parsed")
	}
}

// ---- search / chat / agent / usage ---------------------------------------

func TestSearch(t *testing.T) {
	c, rec := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		var req SearchRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		writeJSON(w, 200, SearchResponse{
			Query: req.Query,
			Hits: []SearchHit{
				{Score: 0.91, Chunk: "alpha", ObjectKey: "a.txt", Seq: 0, EmbedModel: "m1"},
				{Score: 0.42, Chunk: "beta", ObjectKey: "b.txt", Seq: 1},
			},
		})
	})
	res, err := c.Search(context.Background(), SearchRequest{Query: "hello", K: 5, Mode: "hybrid", Bucket: "docs"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodPost || rec.path != "/v1/search" {
		t.Errorf("got %s %s", rec.method, rec.path)
	}
	// Verify the JSON body was encoded with the documented field names.
	var sent map[string]any
	_ = json.Unmarshal(rec.body, &sent)
	if sent["query"] != "hello" || sent["mode"] != "hybrid" || sent["bucket"] != "docs" {
		t.Errorf("encoded body = %v", sent)
	}
	if sent["k"].(float64) != 5 {
		t.Errorf("k = %v", sent["k"])
	}
	if len(res.Hits) != 2 || res.Hits[0].Score != 0.91 || res.Hits[0].ObjectKey != "a.txt" {
		t.Errorf("hits = %+v", res.Hits)
	}
}

func TestSearchOmitsZeroFields(t *testing.T) {
	c, rec := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, SearchResponse{})
	})
	if _, err := c.Search(context.Background(), SearchRequest{Query: "q"}); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	_ = json.Unmarshal(rec.body, &sent)
	if _, ok := sent["k"]; ok {
		t.Errorf("k should be omitted when zero, body=%v", sent)
	}
	if _, ok := sent["mode"]; ok {
		t.Errorf("mode should be omitted when empty, body=%v", sent)
	}
}

func TestChat(t *testing.T) {
	c, rec := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, ChatResponse{
			Answer:    "42",
			Model:     "gpt-test",
			Citations: []SearchHit{{ObjectKey: "doc.txt", Score: 0.7}},
		})
	})
	resp, err := c.Chat(context.Background(), ChatRequest{Query: "meaning?", K: 3, Temperature: 0.2})
	if err != nil {
		t.Fatal(err)
	}
	if rec.path != "/v1/chat" {
		t.Errorf("path = %q", rec.path)
	}
	var sent map[string]any
	_ = json.Unmarshal(rec.body, &sent)
	if sent["query"] != "meaning?" || sent["temperature"].(float64) != 0.2 {
		t.Errorf("body = %v", sent)
	}
	if resp.Answer != "42" || resp.Model != "gpt-test" || len(resp.Citations) != 1 {
		t.Errorf("resp = %+v", resp)
	}
}

func TestAgent(t *testing.T) {
	c, rec := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"answer": "done",
			"model":  "agent-1",
			"steps":  []map[string]any{{"tool": "search", "input": "x"}},
		})
	})
	resp, err := c.Agent(context.Background(), "investigate")
	if err != nil {
		t.Fatal(err)
	}
	if rec.path != "/v1/agent" {
		t.Errorf("path = %q", rec.path)
	}
	var sent map[string]any
	_ = json.Unmarshal(rec.body, &sent)
	if sent["query"] != "investigate" {
		t.Errorf("body = %v", sent)
	}
	if resp.Answer != "done" || len(resp.Steps) != 1 || resp.Steps[0]["tool"] != "search" {
		t.Errorf("resp = %+v", resp)
	}
}

func TestLineage(t *testing.T) {
	c, rec := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, LineageResponse{
			ObjectID: 42,
			Entries:  []LineageEntry{{UsageID: 1, Caller: "rest:chat", TotalTokens: 12}},
		})
	})
	resp, err := c.Lineage(context.Background(), 42, 5)
	if err != nil {
		t.Fatal(err)
	}
	if rec.path != "/v1/lineage/objects/42" {
		t.Errorf("path = %q", rec.path)
	}
	if q := parseQuery(rec.rawQ); q["limit"] != "5" {
		t.Errorf("query = %v", q)
	}
	if resp.ObjectID != 42 || len(resp.Entries) != 1 || resp.Entries[0].Caller != "rest:chat" {
		t.Errorf("resp = %+v", resp)
	}
}

func TestLineageOmitsZeroLimit(t *testing.T) {
	c, rec := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, LineageResponse{ObjectID: 7})
	})
	if _, err := c.Lineage(context.Background(), 7, 0); err != nil {
		t.Fatal(err)
	}
	if rec.rawQ != "" {
		t.Errorf("expected no query, got %q", rec.rawQ)
	}
}

func TestBucketACL(t *testing.T) {
	c, rec := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			writeJSON(w, 200, map[string]string{"acl": b["acl"]})
			return
		}
		writeJSON(w, 200, map[string]string{"acl": "public-read"})
	})

	acl, err := c.GetBucketACL(context.Background(), "my-bucket")
	if err != nil || acl != "public-read" {
		t.Fatalf("GetBucketACL = %q, %v", acl, err)
	}
	if rec.path != "/v1/buckets/my-bucket/acl" {
		t.Errorf("path = %q", rec.path)
	}

	if err := c.SetBucketACL(context.Background(), "my-bucket", "private"); err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodPut || rec.path != "/v1/buckets/my-bucket/acl" {
		t.Errorf("SetBucketACL method/path = %s %q", rec.method, rec.path)
	}
	var sent map[string]string
	_ = json.Unmarshal(rec.body, &sent)
	if sent["acl"] != "private" {
		t.Errorf("SetBucketACL body = %v", sent)
	}
}

func TestUsage(t *testing.T) {
	c, rec := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, Usage{Tenant: "acme", UsedBytes: 1024, UsedObjects: 7, MaxBytes: 1 << 30, MaxObjects: 1000})
	})
	u, err := c.Usage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rec.path != "/v1/usage" {
		t.Errorf("path = %q", rec.path)
	}
	if u.Tenant != "acme" || u.UsedBytes != 1024 || u.MaxObjects != 1000 {
		t.Errorf("usage = %+v", u)
	}
}

func TestHealth(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   bool
	}{
		{"healthy", 200, true},
		{"unhealthy", 503, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := newStub(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			})
			ok, err := c.Health(context.Background())
			if err != nil {
				t.Fatalf("Health err = %v", err)
			}
			if ok != tc.want {
				t.Errorf("Health = %v, want %v", ok, tc.want)
			}
			if rec.path != "/healthz" {
				t.Errorf("path = %q, want /healthz", rec.path)
			}
		})
	}
}

// ---- SSE chat-stream parsing ----------------------------------------------

func TestChatStream(t *testing.T) {
	// token frames carry JSON-encoded strings; the done frame carries the full
	// ChatResponse JSON. Includes a keepalive comment and an event with
	// embedded newlines to exercise the parser.
	const stream = "" +
		": keepalive 1\n\n" +
		"event: token\n" +
		"data: \"Hello\"\n\n" +
		"event: token\n" +
		"data: \", \"\n\n" +
		"event: token\n" +
		"data: \"world\\n!\"\n\n" +
		"event: done\n" +
		"data: {\"answer\":\"Hello, world\\n!\",\"model\":\"m1\",\"citations\":[{\"object_key\":\"a.txt\",\"score\":0.5}]}\n\n"

	c, rec := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, stream)
	})

	var tokens []string
	resp, err := c.ChatStream(context.Background(), ChatRequest{Query: "hi"}, func(tok string) {
		tokens = append(tokens, tok)
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if rec.path != "/v1/chat/stream" {
		t.Errorf("path = %q", rec.path)
	}
	if got := rec.header.Get("Accept"); got != "text/event-stream" {
		t.Errorf("Accept = %q, want text/event-stream", got)
	}
	if strings.Join(tokens, "") != "Hello, world\n!" {
		t.Errorf("tokens joined = %q, want %q", strings.Join(tokens, ""), "Hello, world\n!")
	}
	if len(tokens) != 3 {
		t.Errorf("token count = %d, want 3", len(tokens))
	}
	if resp.Answer != "Hello, world\n!" || resp.Model != "m1" {
		t.Errorf("final resp = %+v", resp)
	}
	if len(resp.Citations) != 1 || resp.Citations[0].ObjectKey != "a.txt" {
		t.Errorf("citations = %+v", resp.Citations)
	}
}

func TestChatStreamErrorFrame(t *testing.T) {
	// The server writes the error frame with %q, i.e. a JSON-quoted string.
	const stream = "event: token\n" +
		"data: \"partial\"\n\n" +
		"event: error\n" +
		"data: \"backend exploded\"\n\n"

	c, _ := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, stream)
	})
	_, err := c.ChatStream(context.Background(), ChatRequest{Query: "hi"}, nil)
	if err == nil {
		t.Fatal("expected error from error frame")
	}
	var ae *Error
	if !AsError(err, &ae) {
		t.Fatalf("err type = %T, want *Error", err)
	}
	if ae.Code != "StreamError" || ae.Message != "backend exploded" {
		t.Errorf("error = %+v", ae)
	}
}

func TestChatStreamNilCallback(t *testing.T) {
	const stream = "event: token\ndata: \"x\"\n\nevent: done\ndata: {\"answer\":\"x\"}\n\n"
	c, _ := newStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = io.WriteString(w, stream)
	})
	resp, err := c.ChatStream(context.Background(), ChatRequest{Query: "hi"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Answer != "x" {
		t.Errorf("answer = %q", resp.Answer)
	}
}

// ---- error mapping --------------------------------------------------------

func TestErrorMapping(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        any
		raw         string
		wantStatus  int
		wantCode    string
		wantMessage string
		wantReqID   string
	}{
		{
			name:        "structured envelope",
			status:      404,
			body:        errEnvelope("NotFound", "object not found", "req-abc"),
			wantStatus:  404,
			wantCode:    "NotFound",
			wantMessage: "object not found",
			wantReqID:   "req-abc",
		},
		{
			name:        "quota exceeded",
			status:      507,
			body:        errEnvelope("QuotaExceeded", "tenant over quota", ""),
			wantStatus:  507,
			wantCode:    "QuotaExceeded",
			wantMessage: "tenant over quota",
		},
		{
			name:        "non-json body falls back to text",
			status:      500,
			raw:         "internal boom",
			wantStatus:  500,
			wantMessage: "internal boom",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newStub(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.raw != "" {
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.raw)
					return
				}
				writeJSON(w, tc.status, tc.body)
			})
			_, err := c.Usage(context.Background())
			if err == nil {
				t.Fatal("expected error")
			}
			var ae *Error
			if !AsError(err, &ae) {
				t.Fatalf("err type = %T, want *Error", err)
			}
			if ae.Status != tc.wantStatus {
				t.Errorf("Status = %d, want %d", ae.Status, tc.wantStatus)
			}
			if ae.Code != tc.wantCode {
				t.Errorf("Code = %q, want %q", ae.Code, tc.wantCode)
			}
			if ae.Message != tc.wantMessage {
				t.Errorf("Message = %q, want %q", ae.Message, tc.wantMessage)
			}
			if ae.RequestID != tc.wantReqID {
				t.Errorf("RequestID = %q, want %q", ae.RequestID, tc.wantReqID)
			}
			// errors.As / errors.Is interop.
			if !errors.As(err, &ae) {
				t.Errorf("errors.As failed")
			}
		})
	}
}

func TestErrorString(t *testing.T) {
	e := &Error{Status: 404, Code: "NotFound", Message: "nope", RequestID: "r1"}
	got := e.Error()
	if !strings.Contains(got, "404") || !strings.Contains(got, "NotFound") || !strings.Contains(got, "r1") {
		t.Errorf("Error() = %q", got)
	}
	// no request id
	e2 := &Error{Status: 500, Code: "InternalError", Message: "x"}
	if strings.Contains(e2.Error(), "request_id") {
		t.Errorf("Error() should omit request_id when empty: %q", e2.Error())
	}
}

// ---- helpers --------------------------------------------------------------

func errEnvelope(code, message, reqID string) map[string]any {
	e := map[string]any{"code": code, "message": message}
	if reqID != "" {
		e["request_id"] = reqID
	}
	return map[string]any{"error": e}
}

// parseQuery is a tiny query-string splitter for assertions (avoids importing
// net/url in tests where it would only obscure intent).
func parseQuery(raw string) map[string]string {
	out := map[string]string{}
	for _, kv := range strings.Split(raw, "&") {
		if kv == "" {
			continue
		}
		k, v, _ := strings.Cut(kv, "=")
		out[unescape(k)] = unescape(v)
	}
	return out
}

func unescape(s string) string {
	s = strings.ReplaceAll(s, "+", " ")
	s = strings.ReplaceAll(s, "%2F", "/")
	s = strings.ReplaceAll(s, "%20", " ")
	return s
}
