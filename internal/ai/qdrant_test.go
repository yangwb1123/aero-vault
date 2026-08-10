package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// Compile-time conformance: QdrantIndex must satisfy BOTH seams.
var (
	_ VectorIndex = (*QdrantIndex)(nil)
	_ ChunkSink   = (*QdrantIndex)(nil)
)

func TestQdrantSearchVectors(t *testing.T) {
	var gotPath, gotMethod, gotAPIKey, gotContentType string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotAPIKey = r.Header.Get("api-key")
		gotContentType = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"result":[
			{"id":101,"score":0.91,"payload":{"tenant_id":"acme","bucket":"docs","object_id":7,"object_key":"a.txt","seq":0,"content":"first","dim":3,"embed_model":"m"}},
			{"id":102,"score":0.42,"payload":{"tenant_id":"acme","bucket":"docs","object_id":7,"object_key":"a.txt","seq":1,"content":"second","dim":3,"embed_model":"m"}}
		]}`))
	}))
	defer srv.Close()

	qi := NewQdrantIndex(QdrantOptions{BaseURL: srv.URL, Collection: "chunks", APIKey: "tok"})
	hits, err := qi.SearchVectors(context.Background(), "acme", "docs", []float32{0.1, 0.2, 0.3}, 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	if gotAPIKey != "tok" {
		t.Errorf("api-key header not forwarded on search: %q", gotAPIKey)
	}
	if gotPath != "/collections/chunks/points/search" {
		t.Errorf("path: got %q", gotPath)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method: got %q", gotMethod)
	}
	if gotContentType != "application/json" {
		t.Errorf("content-type: got %q", gotContentType)
	}
	if gotBody["with_payload"] != true {
		t.Errorf("with_payload not set: %v", gotBody["with_payload"])
	}
	if v, ok := gotBody["limit"].(float64); !ok || int(v) != 5 {
		t.Errorf("limit: got %v", gotBody["limit"])
	}
	vec, ok := gotBody["vector"].([]any)
	if !ok || len(vec) != 3 {
		t.Fatalf("vector not sent: %v", gotBody["vector"])
	}

	// Filter must carry tenant AND bucket matches.
	mustHaveFilterMatch(t, gotBody, "tenant_id", "acme")
	mustHaveFilterMatch(t, gotBody, "bucket", "docs")

	if len(hits) != 2 {
		t.Fatalf("want 2 hits, got %d", len(hits))
	}
	if hits[0].Chunk.ID != 101 || hits[1].Chunk.ID != 102 {
		t.Errorf("ids out of order: %d, %d", hits[0].Chunk.ID, hits[1].Chunk.ID)
	}
	if hits[0].Score != float32(0.91) || hits[1].Score != float32(0.42) {
		t.Errorf("scores: %v, %v", hits[0].Score, hits[1].Score)
	}
	c := hits[0].Chunk
	if c.TenantID != "acme" || c.Bucket != "docs" || c.ObjectID != 7 || c.ObjectKey != "a.txt" || c.Seq != 0 || c.Content != "first" || c.Dim != 3 || c.EmbedModel != "m" {
		t.Errorf("chunk payload mismatch: %+v", c)
	}
}

func TestQdrantSearchVectorsNoBucketFilter(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"result":[]}`))
	}))
	defer srv.Close()

	qi := NewQdrantIndex(QdrantOptions{BaseURL: srv.URL})
	if _, err := qi.SearchVectors(context.Background(), "acme", "", []float32{1, 2}, 3); err != nil {
		t.Fatalf("search: %v", err)
	}
	mustHaveFilterMatch(t, gotBody, "tenant_id", "acme")
	if filterMatchPresent(gotBody, "bucket") {
		t.Errorf("bucket filter should be absent when bucket is empty")
	}
}

func TestQdrantSearchVectorsClampsLimit(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"result":[]}`))
	}))
	defer srv.Close()

	qi := NewQdrantIndex(QdrantOptions{BaseURL: srv.URL})
	for _, tc := range []struct{ in, want int }{
		{0, 10}, {-5, 10}, {100, 100}, {101, 100}, {200, 100}, {99999, 100},
	} {
		gotBody = nil
		if _, err := qi.SearchVectors(context.Background(), "acme", "", []float32{1}, tc.in); err != nil {
			t.Fatalf("search lim=%d: %v", tc.in, err)
		}
		if v, _ := gotBody["limit"].(float64); int(v) != tc.want {
			t.Errorf("limit=%d should clamp to %d, got %v", tc.in, tc.want, gotBody["limit"])
		}
	}
}

func TestQdrantUpsertObjectChunks(t *testing.T) {
	var (
		mu    sync.Mutex
		calls []recordedCall
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		calls = append(calls, recordedCall{path: r.URL.Path, method: r.Method, query: r.URL.RawQuery, apiKey: r.Header.Get("api-key"), body: body})
		mu.Unlock()
		_, _ = w.Write([]byte(`{"result":{"status":"completed"}}`))
	}))
	defer srv.Close()

	qi := NewQdrantIndex(QdrantOptions{BaseURL: srv.URL, Collection: "chunks", APIKey: "tok"})
	chunks := []repository.Chunk{
		{ID: 1, ObjectID: 9, TenantID: "acme", Bucket: "docs", ObjectKey: "f.txt", Seq: 0, Content: "alpha", Dim: 2, EmbedModel: "m", Embedding: []float32{0.1, 0.2}},
		{ID: 2, ObjectID: 9, TenantID: "acme", Bucket: "docs", ObjectKey: "f.txt", Seq: 1, Content: "no-embed", Dim: 0, EmbedModel: "m"}, // empty embedding -> skipped
		{ID: 3, ObjectID: 9, TenantID: "acme", Bucket: "docs", ObjectKey: "f.txt", Seq: 2, Content: "beta", Dim: 2, EmbedModel: "m", Embedding: []float32{0.3, 0.4}},
	}
	if err := qi.UpsertObjectChunks(context.Background(), 9, chunks); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("want 2 calls (delete then upsert), got %d: %+v", len(calls), calls)
	}

	// 1st call: delete-by-object_id filter.
	del := calls[0]
	if del.path != "/collections/chunks/points/delete" {
		t.Errorf("first call should be delete, got path %q", del.path)
	}
	if del.apiKey != "tok" {
		t.Errorf("api-key not sent on delete: %q", del.apiKey)
	}
	mustHaveFilterMatch(t, del.body, "object_id", float64(9))

	// 2nd call: upsert points with ?wait=true.
	up := calls[1]
	if up.path != "/collections/chunks/points" {
		t.Errorf("second call should be points upsert, got path %q", up.path)
	}
	if !strings.Contains(up.query, "wait=true") {
		t.Errorf("upsert must set wait=true, query=%q", up.query)
	}
	pts, ok := up.body["points"].([]any)
	if !ok || len(pts) != 2 {
		t.Fatalf("want 2 points (empty-embed skipped), got %v", up.body["points"])
	}
	p0 := pts[0].(map[string]any)
	if int(p0["id"].(float64)) != 1 {
		t.Errorf("point id should equal chunk ID 1, got %v", p0["id"])
	}
	v0, _ := p0["vector"].([]any)
	if len(v0) != 2 || v0[0].(float64) != 0.1 {
		t.Errorf("vector mismatch: %v", p0["vector"])
	}
	pay0, _ := p0["payload"].(map[string]any)
	if pay0["tenant_id"] != "acme" || pay0["content"] != "alpha" || int(pay0["object_id"].(float64)) != 9 || int(pay0["seq"].(float64)) != 0 {
		t.Errorf("payload mismatch: %v", pay0)
	}
	p1 := pts[1].(map[string]any)
	if int(p1["id"].(float64)) != 3 {
		t.Errorf("second point should be chunk ID 3 (chunk 2 skipped), got %v", p1["id"])
	}
}

func TestQdrantUpsertNoChunksStillDeletes(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_, _ = w.Write([]byte(`{"result":{"status":"completed"}}`))
	}))
	defer srv.Close()

	qi := NewQdrantIndex(QdrantOptions{BaseURL: srv.URL})
	// Re-index that produced no chunks must still purge stale points.
	if err := qi.UpsertObjectChunks(context.Background(), 9, nil); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if len(paths) != 1 || !strings.HasSuffix(paths[0], "/points/delete") {
		t.Fatalf("empty upsert should issue only a delete, got %v", paths)
	}
}

func TestQdrantDeleteObjectChunks(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"result":{"status":"completed"}}`))
	}))
	defer srv.Close()

	qi := NewQdrantIndex(QdrantOptions{BaseURL: srv.URL, Collection: "chunks"})
	if err := qi.DeleteObjectChunks(context.Background(), 42); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if gotPath != "/collections/chunks/points/delete" {
		t.Errorf("path: got %q", gotPath)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method: got %q", gotMethod)
	}
	mustHaveFilterMatch(t, gotBody, "object_id", float64(42))
}

func TestQdrantTrailingSlashTrimmed(t *testing.T) {
	qi := NewQdrantIndex(QdrantOptions{BaseURL: "http://example.com/"})
	if qi.baseURL != "http://example.com" {
		t.Fatalf("trailing slash not trimmed: %q", qi.baseURL)
	}
	if qi.collection != "aero_chunks" {
		t.Fatalf("default collection: %q", qi.collection)
	}
}

func TestQdrantHTTPErrorsSurface(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "qdrant boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	qi := NewQdrantIndex(QdrantOptions{BaseURL: srv.URL})

	if _, err := qi.SearchVectors(context.Background(), "acme", "", []float32{1}, 3); err == nil || !strings.Contains(err.Error(), "qdrant http 500") {
		t.Errorf("search error: %v", err)
	}
	chunks := []repository.Chunk{{ID: 1, ObjectID: 9, Embedding: []float32{1}}}
	if err := qi.UpsertObjectChunks(context.Background(), 9, chunks); err == nil || !strings.Contains(err.Error(), "qdrant http 500") {
		t.Errorf("upsert error: %v", err)
	}
	if err := qi.DeleteObjectChunks(context.Background(), 9); err == nil || !strings.Contains(err.Error(), "qdrant http 500") {
		t.Errorf("delete error: %v", err)
	}
}

// TestQdrantUpsertPutErrorSurfaces pins the upsert's own PUT /points error path:
// the delete-by-filter that Upsert issues first succeeds (2xx), so the error the
// caller sees must originate from the PUT, not the preceding delete.
func TestQdrantUpsertPutErrorSurfaces(t *testing.T) {
	var putCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/collections/chunks/points" {
			putCalled = true
			http.Error(w, "qdrant boom", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	qi := NewQdrantIndex(QdrantOptions{BaseURL: srv.URL, Collection: "chunks"})

	chunks := []repository.Chunk{{ID: 1, ObjectID: 9, Embedding: []float32{1}}}
	err := qi.UpsertObjectChunks(context.Background(), 9, chunks)
	if err == nil || !strings.Contains(err.Error(), "qdrant http 500") {
		t.Fatalf("upsert PUT error not surfaced: %v", err)
	}
	if !putCalled {
		t.Fatal("PUT /points was never reached (delete masked the upsert path)")
	}
}

// TestQdrantEnsureCollectionCreates verifies EnsureCollection issues
// PUT /collections/{name} with the embedder's vector size and Cosine distance.
func TestQdrantEnsureCollectionCreates(t *testing.T) {
	var gotPath, gotMethod, gotAPIKey string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod, gotAPIKey = r.URL.Path, r.Method, r.Header.Get("api-key")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"result":true,"status":"ok"}`))
	}))
	defer srv.Close()

	qi := NewQdrantIndex(QdrantOptions{BaseURL: srv.URL, Collection: "chunks", APIKey: "tok"})
	if err := qi.EnsureCollection(context.Background(), 384); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method: want PUT, got %q", gotMethod)
	}
	if gotPath != "/collections/chunks" {
		t.Errorf("path: want /collections/chunks, got %q", gotPath)
	}
	if gotAPIKey != "tok" {
		t.Errorf("api-key not forwarded: %q", gotAPIKey)
	}
	vectors, ok := gotBody["vectors"].(map[string]any)
	if !ok {
		t.Fatalf("vectors object missing in body: %v", gotBody)
	}
	if v, _ := vectors["size"].(float64); int(v) != 384 {
		t.Errorf("vectors.size: want 384, got %v", vectors["size"])
	}
	if vectors["distance"] != "Cosine" {
		t.Errorf("vectors.distance: want Cosine, got %v", vectors["distance"])
	}
}

// TestQdrantEnsureCollectionIdempotent confirms an already-existing collection
// (Qdrant may answer 409 / "already exists") is treated as success, not error.
func TestQdrantEnsureCollectionIdempotent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"conflict409", http.StatusConflict, `{"status":{"error":"Collection ` + "`chunks`" + ` already exists!"}}`},
		{"badrequest400", http.StatusBadRequest, `{"status":{"error":"Wrong input: Collection already exists"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			qi := NewQdrantIndex(QdrantOptions{BaseURL: srv.URL, Collection: "chunks"})
			if err := qi.EnsureCollection(context.Background(), 3); err != nil {
				t.Fatalf("already-exists must be treated as success, got %v", err)
			}
		})
	}
}

// TestQdrantEnsureCollectionBadDim asserts dim<=0 errors without any network call.
func TestQdrantEnsureCollectionBadDim(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	qi := NewQdrantIndex(QdrantOptions{BaseURL: srv.URL})
	for _, dim := range []int{0, -1, -100} {
		if err := qi.EnsureCollection(context.Background(), dim); err == nil {
			t.Errorf("dim=%d: want error, got nil", dim)
		}
	}
	if hit {
		t.Fatal("EnsureCollection must not hit the network for dim<=0")
	}
}

// TestQdrantEnsureCollectionServerError confirms a genuine 500 surfaces as a
// wrapped error (distinct from the already-exists case).
func TestQdrantEnsureCollectionServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "qdrant boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	qi := NewQdrantIndex(QdrantOptions{BaseURL: srv.URL})
	if err := qi.EnsureCollection(context.Background(), 3); err == nil || !strings.Contains(err.Error(), "qdrant http 500") {
		t.Fatalf("genuine 500 should surface as wrapped error, got %v", err)
	}
}

// --- helpers ---

type recordedCall struct {
	path, method, query, apiKey string
	body                        map[string]any
}

// filterMatchPresent reports whether the request's filter has a "must" clause
// matching the given payload key.
func filterMatchPresent(body map[string]any, key string) bool {
	filter, ok := body["filter"].(map[string]any)
	if !ok {
		return false
	}
	must, ok := filter["must"].([]any)
	if !ok {
		return false
	}
	for _, m := range must {
		cond, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if cond["key"] == key {
			return true
		}
	}
	return false
}

func mustHaveFilterMatch(t *testing.T, body map[string]any, key string, want any) {
	t.Helper()
	filter, ok := body["filter"].(map[string]any)
	if !ok {
		t.Fatalf("no filter in body: %v", body)
	}
	must, ok := filter["must"].([]any)
	if !ok {
		t.Fatalf("filter.must not a list: %v", filter)
	}
	for _, m := range must {
		cond, _ := m.(map[string]any)
		if cond["key"] != key {
			continue
		}
		matchObj, ok := cond["match"].(map[string]any)
		if !ok {
			t.Fatalf("filter cond for %q has no match: %v", key, cond)
		}
		if !reflect.DeepEqual(matchObj["value"], want) {
			t.Fatalf("filter %q value = %v (%T), want %v (%T)", key, matchObj["value"], matchObj["value"], want, want)
		}
		return
	}
	t.Fatalf("no filter match for key %q in %v", key, must)
}
