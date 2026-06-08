package ai

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/telemetry"
)

// Search is the user-facing semantic-search service. It embeds the query,
// asks the repository for nearest chunks, optionally merges in BM25 results,
// optionally reranks with a cross-encoder, and records an AI usage row.
type Search struct {
	repo     repository.Repository
	embedder Embedder
	bm25     *BM25
	rerank   Reranker
	vindex   VectorIndex
	lexical  LexicalIndex
	results  *resultCache
	logger   *slog.Logger
}

func NewSearch(repo repository.Repository, emb Embedder, logger *slog.Logger) *Search {
	if logger == nil {
		logger = slog.Default()
	}
	// Default to the brute-force repository index; WithVectorIndex swaps in a
	// scalable backend (pgvector/Qdrant) without changing this service.
	return &Search{repo: repo, embedder: emb, vindex: repoVectorIndex{repo: repo}, logger: logger}
}

// WithVectorIndex overrides the default brute-force retrieval backend (e.g. a
// pgvector or Qdrant adapter) for scalable nearest-neighbour search.
func (s *Search) WithVectorIndex(vi VectorIndex) *Search {
	if vi != nil {
		s.vindex = vi
	}
	return s
}

// WithLexicalIndex overrides the default in-process BM25 keyword search with a
// shared/scalable backend (e.g. Postgres FTS). When set, it serves the lexical
// half of bm25/hybrid queries instead of the in-memory index.
func (s *Search) WithLexicalIndex(li LexicalIndex) *Search {
	s.lexical = li
	return s
}

// WithResultCache enables an opt-in, bounded, TTL'd hot-result cache so that
// identical repeated queries skip the embed + retrieval + rerank work. A
// capacity<=0 or ttl<=0 leaves caching disabled (no-op). Results can go stale as
// the corpus changes; the TTL bounds that staleness, which is why this is
// opt-in with a short default TTL.
func (s *Search) WithResultCache(capacity int, ttl time.Duration) *Search {
	s.results = newResultCache(capacity, ttl)
	return s
}

// WithBM25 enables hybrid search (vector + BM25 with reciprocal rank fusion).
func (s *Search) WithBM25(b *BM25) *Search {
	s.bm25 = b
	return s
}

// WithReranker installs a cross-encoder reranker (called after retrieval).
func (s *Search) WithReranker(r Reranker) *Search {
	s.rerank = r
	return s
}

// Request is the parsed /v1/search input.
type Request struct {
	Tenant string
	Bucket string // optional; empty = all buckets in tenant
	Query  string
	K      int
	Mode   string // "vector" (default) | "bm25" | "hybrid"
	Caller string // "rest:search" / "mcp:search" / etc.
	ReqID  string
}

// Hit is a search result: a chunk plus its score, ready for JSON.
type Hit struct {
	Score      float32 `json:"score"`
	Chunk      string  `json:"chunk"`
	ChunkID    int64   `json:"chunk_id"`
	ObjectID   int64   `json:"object_id"`
	Bucket     string  `json:"bucket"`
	ObjectKey  string  `json:"object_key"`
	Seq        int     `json:"seq"`
	EmbedModel string  `json:"embed_model"`
}

func (s *Search) Query(ctx context.Context, req Request) ([]Hit, error) {
	if req.Query == "" {
		return nil, fmt.Errorf("query required")
	}
	if req.K <= 0 || req.K > 100 {
		req.K = 10
	}
	if req.Caller == "" {
		req.Caller = "rest:search"
	}
	mode := req.Mode
	if mode == "" {
		mode = "vector"
	}
	if mode != "vector" && mode != "bm25" && mode != "hybrid" {
		return nil, fmt.Errorf("invalid search mode %q: want vector, bm25, or hybrid", mode)
	}
	if (mode == "bm25" || mode == "hybrid") && s.bm25 == nil && s.lexical == nil {
		return nil, fmt.Errorf("bm25 not enabled; set search mode to vector or build the BM25 index")
	}
	if (mode == "vector" || mode == "hybrid") && s.embedder == nil {
		return nil, fmt.Errorf("vector search unavailable: no embedder configured")
	}
	// Keep the request's normalized fields (K/Mode defaults applied) on req so
	// the cache key reflects exactly what the retrieval path will use.
	req.Mode = mode

	// Hot-result cache: identical normalized queries short-circuit the embed +
	// retrieval + rerank work. Returns a copy so callers can't mutate the entry.
	var cacheKey string
	if s.results != nil {
		cacheKey = resultCacheKey(req)
		if hits, ok := s.results.get(cacheKey); ok {
			return hits, nil
		}
	}

	// Collect ranked lists per modality. Time from here (a confirmed cache miss)
	// so the histogram reflects real retrieval+rerank cost, not cache hits.
	start := time.Now()
	type ranked struct {
		chunkID int64
		score   float32
		chunk   repository.Chunk
	}
	var vecHits []ranked
	if mode == "vector" || mode == "hybrid" {
		vecs, err := s.embedder.Embed(ctx, []string{req.Query})
		if err != nil {
			return nil, fmt.Errorf("embed query: %w", err)
		}
		if len(vecs) == 0 {
			return nil, fmt.Errorf("embedder returned no vectors")
		}
		hits, err := s.vindex.SearchVectors(ctx, req.Tenant, req.Bucket, vecs[0], req.K*2)
		if err != nil {
			return nil, fmt.Errorf("search chunks: %w", err)
		}
		queryModel := s.embedder.Name()
		for _, h := range hits {
			// Embedding-model drift guard: a chunk embedded by a *different*
			// model must never be vector-compared against this query, even when
			// the dimensions happen to match — the cosine score would be
			// meaningless. (Brute-force SearchChunks only filters on dimension.)
			if queryModel != "" && h.Chunk.EmbedModel != "" && h.Chunk.EmbedModel != queryModel {
				continue
			}
			vecHits = append(vecHits, ranked{chunkID: h.Chunk.ID, score: h.Score, chunk: h.Chunk})
		}
	}
	var bm25Hits []ranked
	if mode == "bm25" || mode == "hybrid" {
		if s.lexical != nil {
			// Shared/scalable lexical backend (e.g. Postgres FTS).
			hits, err := s.lexical.SearchLexical(ctx, req.Tenant, req.Bucket, req.Query, req.K*2)
			if err != nil {
				return nil, fmt.Errorf("lexical search: %w", err)
			}
			for _, h := range hits {
				bm25Hits = append(bm25Hits, ranked{chunkID: h.Chunk.ID, score: h.Score, chunk: h.Chunk})
			}
		} else {
			raw := s.bm25.Search(req.Query, req.Bucket, req.K*2)
			for _, h := range raw {
				ch, _ := s.repo.GetObjectByID(ctx, h.Doc.objectID)
				bm25Hits = append(bm25Hits, ranked{
					chunkID: h.ChunkID,
					score:   float32(h.Score),
					chunk: repository.Chunk{
						ID: h.ChunkID, ObjectID: h.Doc.objectID, TenantID: h.Doc.tenant, Bucket: h.Doc.bucket,
						ObjectKey: h.Doc.objectKey, Seq: h.Doc.seq, Content: h.Doc.content, EmbedModel: "bm25",
					},
				})
				_ = ch
			}
		}
	}

	var merged []ranked
	switch mode {
	case "vector":
		merged = vecHits
	case "bm25":
		merged = bm25Hits
	case "hybrid":
		// Reciprocal Rank Fusion: score(d) = sum 1/(k+rank_i(d)). k=60 standard.
		const rrfK = 60.0
		acc := map[int64]float64{}
		seen := map[int64]repository.Chunk{}
		for i, h := range vecHits {
			acc[h.chunkID] += 1.0 / (rrfK + float64(i+1))
			seen[h.chunkID] = h.chunk
		}
		for i, h := range bm25Hits {
			acc[h.chunkID] += 1.0 / (rrfK + float64(i+1))
			if _, ok := seen[h.chunkID]; !ok {
				seen[h.chunkID] = h.chunk
			}
		}
		for id, s := range acc {
			merged = append(merged, ranked{chunkID: id, score: float32(s), chunk: seen[id]})
		}
		// sort desc
		for i := 1; i < len(merged); i++ {
			j := i
			for j > 0 && merged[j].score > merged[j-1].score {
				merged[j], merged[j-1] = merged[j-1], merged[j]
				j--
			}
		}
	}

	// Over-retrieve for the reranker; trim to K after rerank.
	overK := req.K * 3
	if overK > len(merged) {
		overK = len(merged)
	}
	if overK > 0 && len(merged) > overK {
		merged = merged[:overK]
	}
	out := make([]Hit, 0, len(merged))
	chunkIDs := make([]int64, 0, len(merged))
	objSeen := map[int64]struct{}{}
	objIDs := make([]int64, 0, len(merged))
	for _, h := range merged {
		out = append(out, Hit{
			Score:      h.score,
			Chunk:      h.chunk.Content,
			ChunkID:    h.chunkID,
			ObjectID:   h.chunk.ObjectID,
			Bucket:     h.chunk.Bucket,
			ObjectKey:  h.chunk.ObjectKey,
			Seq:        h.chunk.Seq,
			EmbedModel: h.chunk.EmbedModel,
		})
	}

	// Apply reranker if configured; otherwise just take top-K from the merged list.
	if s.rerank != nil && len(out) > 0 {
		if reranked, err := s.rerank.Rerank(ctx, req.Query, out, req.K); err == nil {
			out = reranked
		} else {
			s.logger.Warn("rerank failed; using raw order", "err", err)
			if len(out) > req.K {
				out = out[:req.K]
			}
		}
	} else if len(out) > req.K {
		out = out[:req.K]
	}

	for _, h := range out {
		chunkIDs = append(chunkIDs, h.ChunkID)
		if _, ok := objSeen[h.ObjectID]; !ok {
			objSeen[h.ObjectID] = struct{}{}
			objIDs = append(objIDs, h.ObjectID)
		}
	}

	if err := s.repo.RecordUsage(ctx, repository.Usage{
		TenantID: req.Tenant, Caller: req.Caller, Query: req.Query,
		ChunkIDs: chunkIDs, ObjectIDs: objIDs, RequestID: req.ReqID,
	}); err != nil {
		s.logger.Warn("audit usage failed", "err", err)
	}
	telemetry.RecordSearchLatency(ctx, mode, float64(time.Since(start).Milliseconds()))
	if s.results != nil {
		s.results.put(cacheKey, out)
	}
	return out, nil
}
