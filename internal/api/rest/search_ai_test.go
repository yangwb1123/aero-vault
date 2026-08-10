package rest

// AC-2 (spec d1-drill-ai-read-path-degrade-v1.md §4): the REST seam must
// return 200-with-hits (never 5xx) when one hybrid modality fails, and keep
// the 500 for pure modes. This is the package's first AI-handler test
// (design E9: zero NewAIHandler hits existed in any *rest test).

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aero-vault/aero-vault/internal/ai"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// failingVIndex is a configured-but-failing vector backend (pgvector/Qdrant
// analog) for the REST seam test.
type failingVIndex struct{ err error }

func (f *failingVIndex) SearchVectors(_ context.Context, _, _ string, _ []float32, _ int) ([]repository.SearchHit, error) {
	return nil, f.err
}

// searchAIEnv assembles a /v1/search router over a seeded repo with a failing
// vector backend: hybrid degrades to BM25-only, pure vector stays 500.
func searchAIEnv(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "ai.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { repo.Close() })
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	svc := service.NewFileService(store, repo, nil)
	obj, err := svc.Put(context.Background(), "default", "default", "h.txt",
		strings.NewReader("hybrid"), 6, service.PutOptions{ContentType: "text/plain"})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	emb := ai.NewHashEmbedder(128)
	vecs, err := emb.Embed(context.Background(),
		[]string{"distributed systems consensus raft protocol", "baking sourdough bread at home"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	chunks := make([]repository.Chunk, 0, len(vecs))
	for i, c := range []string{"distributed systems consensus raft protocol", "baking sourdough bread at home"} {
		chunks = append(chunks, repository.Chunk{
			ObjectID: obj.ID, TenantID: obj.TenantID, Bucket: obj.Bucket, ObjectKey: obj.Key,
			Seq: i, Content: c, Embedding: vecs[i], Dim: len(vecs[i]), EmbedModel: emb.Name(),
		})
	}
	if err := repo.InsertChunks(context.Background(), chunks); err != nil {
		t.Fatalf("insert chunks: %v", err)
	}
	b := ai.NewBM25()
	if err := b.BuildFromRepo(context.Background(), repo, "default"); err != nil {
		t.Fatalf("build bm25: %v", err)
	}
	s := ai.NewSearch(repo, emb, slog.Default()).WithBM25(b).
		WithVectorIndex(&failingVIndex{err: context.DeadlineExceeded})
	aih := NewAIHandler(repo, s, nil, nil, slog.Default(), false)

	r := chi.NewRouter()
	r.Use(mw.Tenant, mw.Auth)
	r.Post("/v1/search", aih.Search)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

func TestSearchREST_HybridDegradesOnVectorBackendError(t *testing.T) {
	srv := searchAIEnv(t)

	// Hybrid + failing vector backend → 200 with BM25-only hits (never 5xx).
	resp, body := req(t, http.MethodPost, srv.URL+"/v1/search",
		[]byte(`{"query":"raft consensus","mode":"hybrid","k":5}`), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("hybrid degrade: status=%d body=%s, want 200", resp.StatusCode, body)
	}
	var out struct {
		Query string   `json:"query"`
		Hits  []ai.Hit `json:"hits"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode response %q: %v", body, err)
	}
	if len(out.Hits) == 0 {
		t.Fatal("degraded hybrid response must contain BM25 hits")
	}
	if !strings.Contains(out.Hits[0].Chunk, "raft") {
		t.Fatalf("expected raft chunk first in degraded hybrid, got %q", out.Hits[0].Chunk)
	}

	// Negative control: pure vector mode on the same router/fixture keeps the
	// 500 (FR-3 visibility pinned at the seam).
	resp, body = req(t, http.MethodPost, srv.URL+"/v1/search",
		[]byte(`{"query":"raft consensus","mode":"vector","k":5}`), nil)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("pure vector mode: status=%d body=%s, want 500", resp.StatusCode, body)
	}
	var errBody errorBody
	if err := json.Unmarshal(body, &errBody); err != nil {
		t.Fatalf("decode error body %q: %v", body, err)
	}
	if errBody.Error.Code != "InternalError" {
		t.Fatalf("pure vector mode error code = %q, want InternalError", errBody.Error.Code)
	}
}
