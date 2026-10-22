package ai

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

type qdrantPointStore struct {
	mu         sync.Mutex
	points     map[int64]map[string]any
	calls      []recordedCall
	deleteHits int
	putHits    int
}

func newQdrantPointStore() *qdrantPointStore {
	return &qdrantPointStore{points: map[int64]map[string]any{}}
}

func (s *qdrantPointStore) seedPoint(id int64, payload map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.points[id] = payload
}

func (s *qdrantPointStore) pointIDs() map[int64]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[int64]bool, len(s.points))
	for id := range s.points {
		out[id] = true
	}
	return out
}

func (s *qdrantPointStore) callCounts() (deletes, puts int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteHits, s.putHits
}

func (s *qdrantPointStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, recordedCall{path: r.URL.Path, method: r.Method, query: r.URL.RawQuery, body: body})
	switch {
	case r.URL.Path == "/collections/chunks/points/delete":
		s.deleteHits++
		s.deleteMatching(body)
	case r.URL.Path == "/collections/chunks/points" && r.Method == http.MethodPut:
		s.putHits++
		s.storePoints(body)
	}
	_, _ = w.Write([]byte(`{"result":{"status":"completed"}}`))
}

func (s *qdrantPointStore) deleteMatching(body map[string]any) {
	filter, _ := body["filter"].(map[string]any)
	must, _ := filter["must"].([]any)
	for id, payload := range s.points {
		if qdrantPayloadMatches(payload, must) {
			delete(s.points, id)
		}
	}
}

func qdrantPayloadMatches(payload map[string]any, must []any) bool {
	for _, raw := range must {
		cond, _ := raw.(map[string]any)
		match, _ := cond["match"].(map[string]any)
		key, _ := cond["key"].(string)
		if key == "" || payload[key] != match["value"] {
			return false
		}
	}
	return len(must) > 0
}

func (s *qdrantPointStore) storePoints(body map[string]any) {
	points, _ := body["points"].([]any)
	for _, raw := range points {
		point, _ := raw.(map[string]any)
		id, _ := point["id"].(float64)
		payload, _ := point["payload"].(map[string]any)
		s.points[int64(id)] = payload
	}
}

func TestQdrantScopedDeletePreservesOtherTenantAndLegacyPoints(t *testing.T) {
	ctx := context.Background()
	store := newQdrantPointStore()
	store.seedPoint(100, map[string]any{
		"tenant_id": "alpha", "bucket": "docs", "object_id": float64(7), "object_key": "a.txt", "content": "A",
	})
	store.seedPoint(200, map[string]any{
		"tenant_id": "beta", "bucket": "docs", "object_id": float64(7), "object_key": "a.txt", "content": "B",
	})
	store.seedPoint(300, map[string]any{"object_id": float64(7), "content": "legacy"})
	srv := httptest.NewServer(store)
	defer srv.Close()

	qi := NewQdrantIndex(QdrantOptions{BaseURL: srv.URL, Collection: "chunks"})
	ref := qdrantTestRef("alpha", "docs", "a.txt", 7)
	if err := qi.DeleteScopedObjectChunks(ctx, ref); err != nil {
		t.Fatalf("scoped delete: %v", err)
	}
	ids := store.pointIDs()
	if ids[100] || !ids[200] || !ids[300] {
		t.Fatalf("scoped delete must remove A and preserve B/legacy, got %v", ids)
	}

	chunks := []repository.Chunk{
		{ID: 101, ObjectID: 7, TenantID: "alpha", Bucket: "docs", ObjectKey: "a.txt", Seq: 0, Content: "new", Embedding: []float32{1, 0}, Dim: 2},
		{ID: 102, ObjectID: 7, TenantID: "alpha", Bucket: "docs", ObjectKey: "a.txt", Seq: 1, Content: "no embed"},
	}
	if err := qi.UpsertScopedObjectChunks(ctx, ref, chunks); err != nil {
		t.Fatalf("scoped upsert: %v", err)
	}
	ids = store.pointIDs()
	if ids[100] || !ids[101] || ids[102] || !ids[200] || !ids[300] {
		t.Fatalf("scoped upsert state: %v", ids)
	}
	deletes, puts := store.callCounts()
	if deletes != 2 || puts != 1 {
		t.Fatalf("calls: deletes=%d puts=%d", deletes, puts)
	}
}

func TestQdrantScopedValidationZeroHTTP(t *testing.T) {
	ref := qdrantTestRef("alpha", "docs", "a.txt", 7)
	valid := repository.Chunk{
		ID: 1, ObjectID: 7, TenantID: "alpha", Bucket: "docs", ObjectKey: "a.txt",
		Seq: 0, Content: "ok", Embedding: []float32{1, 2}, Dim: 2,
	}
	with := func(mutate func(*repository.Chunk)) []repository.Chunk {
		c := valid
		mutate(&c)
		return []repository.Chunk{c}
	}
	dup := []repository.Chunk{valid, valid}
	nan := valid
	nan.Embedding = []float32{float32(math.NaN()), 0}
	inf := valid
	inf.Embedding = []float32{float32(math.Inf(1)), 0}
	badDim := valid
	badDim.Dim = 3
	badLen := valid
	badLen.Embedding = []float32{1}
	mixedDim := valid
	mixedDim.ID = 2
	mixedDim.Embedding = []float32{1, 0, 0}
	mixedDim.Dim = 3

	cases := []struct {
		name   string
		ref    access.ChunkRef
		chunks []repository.Chunk
	}{
		{"invalid-ref", access.ChunkRef{}, []repository.Chunk{valid}},
		{"tenant", ref, with(func(c *repository.Chunk) { c.TenantID = "beta" })},
		{"bucket", ref, with(func(c *repository.Chunk) { c.Bucket = "other" })},
		{"key", ref, with(func(c *repository.Chunk) { c.ObjectKey = "other.txt" })},
		{"object", ref, with(func(c *repository.Chunk) { c.ObjectID = 8 })},
		{"zero-id", ref, with(func(c *repository.Chunk) { c.ID = 0 })},
		{"duplicate", ref, dup},
		{"bad-dim", ref, []repository.Chunk{badDim}},
		{"bad-length", ref, []repository.Chunk{badLen}},
		{"nan", ref, []repository.Chunk{nan}},
		{"inf", ref, []repository.Chunk{inf}},
		{"mixed-dimensions", ref, []repository.Chunk{valid, mixedDim}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()
			qi := NewQdrantIndex(QdrantOptions{BaseURL: srv.URL})
			if err := qi.UpsertScopedObjectChunks(context.Background(), tc.ref, tc.chunks); err == nil {
				t.Fatal("invalid input must be refused")
			}
			if hits != 0 {
				t.Fatalf("invalid input issued %d HTTP calls", hits)
			}
		})
	}

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	defer srv.Close()
	qi := NewQdrantIndex(QdrantOptions{BaseURL: srv.URL})
	if err := qi.DeleteScopedObjectChunks(context.Background(), access.ChunkRef{}); err == nil {
		t.Fatal("invalid delete ref must be refused")
	}
	if hits != 0 {
		t.Fatalf("invalid delete issued %d HTTP calls", hits)
	}
}

func TestQdrantScopedEmptyUpsertOnlyDeletes(t *testing.T) {
	store := newQdrantPointStore()
	store.seedPoint(1, map[string]any{
		"tenant_id": "alpha", "bucket": "docs", "object_id": float64(7), "object_key": "a.txt",
	})
	store.seedPoint(2, map[string]any{
		"tenant_id": "beta", "bucket": "docs", "object_id": float64(7), "object_key": "a.txt",
	})
	srv := httptest.NewServer(store)
	defer srv.Close()
	qi := NewQdrantIndex(QdrantOptions{BaseURL: srv.URL, Collection: "chunks"})
	if err := qi.UpsertScopedObjectChunks(context.Background(), qdrantTestRef("alpha", "docs", "a.txt", 7), nil); err != nil {
		t.Fatalf("empty scoped upsert: %v", err)
	}
	ids := store.pointIDs()
	if ids[1] || !ids[2] {
		t.Fatalf("empty upsert must clear only its ref, got %v", ids)
	}
	deletes, puts := store.callCounts()
	if deletes != 1 || puts != 0 {
		t.Fatalf("empty upsert calls: deletes=%d puts=%d", deletes, puts)
	}
}

func TestQdrantLegacyWritesRequireScopeWithoutHTTP(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	defer srv.Close()
	qi := NewQdrantIndex(QdrantOptions{BaseURL: srv.URL})
	chunks := []repository.Chunk{{
		ID: 1, ObjectID: 7, TenantID: "alpha", Bucket: "docs", ObjectKey: "a.txt",
		Embedding: []float32{1}, Dim: 1,
	}}
	if err := qi.UpsertObjectChunks(context.Background(), 7, chunks); err == nil || !strings.Contains(err.Error(), "scoped") {
		t.Fatalf("legacy upsert must require scope: %v", err)
	}
	if err := qi.DeleteObjectChunks(context.Background(), 7); err == nil || !strings.Contains(err.Error(), "scoped") {
		t.Fatalf("legacy delete must require scope: %v", err)
	}
	if hits != 0 {
		t.Fatalf("legacy scope errors must not issue HTTP, got %d calls", hits)
	}
}

func TestQdrantScopedDeleteFailurePreventsPut(t *testing.T) {
	puts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/collections/chunks/points/delete" {
			http.Error(w, "delete boom", http.StatusInternalServerError)
			return
		}
		if r.Method == http.MethodPut {
			puts++
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	qi := NewQdrantIndex(QdrantOptions{BaseURL: srv.URL, Collection: "chunks"})
	chunks := []repository.Chunk{{
		ID: 1, ObjectID: 7, TenantID: "alpha", Bucket: "docs", ObjectKey: "a.txt",
		Embedding: []float32{1}, Dim: 1,
	}}
	err := qi.UpsertScopedObjectChunks(context.Background(), qdrantTestRef("alpha", "docs", "a.txt", 7), chunks)
	if err == nil || !strings.Contains(err.Error(), "qdrant http 500") {
		t.Fatalf("delete failure: %v", err)
	}
	if puts != 0 {
		t.Fatalf("delete failure must not reach PUT, puts=%d", puts)
	}
}
