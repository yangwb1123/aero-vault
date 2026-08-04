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
	hitAuth  HitAuthorizer
}

type HitAuthorizer interface {
	CanReadObject(context.Context, string, string, string) error
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

// WithHitAuthorizer filters every candidate before it reaches search, chat, or
// agent callers. Result caching is bypassed because decisions are subject-specific.
func (s *Search) WithHitAuthorizer(authorizer HitAuthorizer) *Search {
	s.hitAuth = authorizer
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

type ranked struct {
	chunkID int64
	score   float32
	chunk   repository.Chunk
}

func (s *Search) bm25Available() bool {
	return s.bm25 != nil || s.lexical != nil
}

func (s *Search) embedderAvailable() bool {
	return s.embedder != nil
}

func (r *Request) validateMode(mode string, s *Search) error {
	switch mode {
	case "vector":
		if !s.embedderAvailable() {
			return fmt.Errorf("vector search unavailable: no embedder configured")
		}
	case "bm25":
		if !s.bm25Available() {
			return fmt.Errorf("bm25 not enabled; set search mode to vector or build the BM25 index")
		}
	case "hybrid":
		if !s.embedderAvailable() {
			return fmt.Errorf("vector search unavailable: no embedder configured")
		}
		if !s.bm25Available() {
			return fmt.Errorf("bm25 not enabled; set search mode to vector or build the BM25 index")
		}
	default:
		return fmt.Errorf("invalid search mode %q: want vector, bm25, or hybrid", mode)
	}
	return nil
}

func (r *Request) validate(s *Search) error {
	if r.Query == "" {
		return fmt.Errorf("query required")
	}
	if r.K <= 0 || r.K > 100 {
		r.K = 10
	}
	if r.Caller == "" {
		r.Caller = "rest:search"
	}
	mode := r.Mode
	if mode == "" {
		mode = "vector"
	}
	if err := r.validateMode(mode, s); err != nil {
		return err
	}
	r.Mode = mode
	return nil
}

func (s *Search) searchVector(ctx context.Context, req Request) ([]ranked, error) {
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
	var vecHits []ranked
	for _, h := range hits {
		if !matchesEmbedModel(queryModel, h.Chunk.EmbedModel) {
			continue
		}
		vecHits = append(vecHits, ranked{chunkID: h.Chunk.ID, score: h.Score, chunk: h.Chunk})
	}
	return vecHits, nil
}

func (s *Search) searchLexical(ctx context.Context, req Request) ([]ranked, error) {
	var bm25Hits []ranked
	queryModel := ""
	if s.embedder != nil {
		queryModel = s.embedder.Name()
	}
	if s.lexical != nil {
		hits, err := s.lexical.SearchLexical(ctx, req.Tenant, req.Bucket, req.Query, req.K*2)
		if err != nil {
			return nil, fmt.Errorf("lexical search: %w", err)
		}
		for _, h := range hits {
			if !matchesEmbedModel(queryModel, h.Chunk.EmbedModel) {
				continue
			}
			bm25Hits = append(bm25Hits, ranked{chunkID: h.Chunk.ID, score: h.Score, chunk: h.Chunk})
		}
	} else {
		raw := s.bm25.Search(req.Tenant, req.Query, req.Bucket, req.K*2)
		for _, h := range raw {
			if !matchesEmbedModel(queryModel, h.Doc.embedModel) {
				continue
			}
			bm25Hits = append(bm25Hits, ranked{
				chunkID: h.ChunkID,
				score:   float32(h.Score),
				chunk: repository.Chunk{
					ID: h.ChunkID, ObjectID: h.Doc.objectID, TenantID: h.Doc.tenant, Bucket: h.Doc.bucket,
					ObjectKey: h.Doc.objectKey, Seq: h.Doc.seq, Content: h.Doc.content,
					EmbedModel: h.Doc.embedModel,
				},
			})
		}
	}
	return bm25Hits, nil
}

func matchesEmbedModel(current, chunk string) bool {
	return current == "" || chunk == current
}

func rrfMerge(vecHits, bm25Hits []ranked) []ranked {
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
	merged := make([]ranked, 0, len(acc))
	for id, s := range acc {
		merged = append(merged, ranked{chunkID: id, score: float32(s), chunk: seen[id]})
	}
	for i := 1; i < len(merged); i++ {
		j := i
		for j > 0 && (merged[j].score > merged[j-1].score ||
			(merged[j].score == merged[j-1].score && merged[j].chunkID < merged[j-1].chunkID)) {
			merged[j], merged[j-1] = merged[j-1], merged[j]
			j--
		}
	}
	return merged
}

func trimToOverK(merged []ranked, overK int) []ranked {
	if overK <= 0 || len(merged) <= overK {
		return merged
	}
	return merged[:overK]
}

func hitsFromRanked(merged []ranked) []Hit {
	out := make([]Hit, 0, len(merged))
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
	return out
}

func (s *Search) applyRerankOrTrim(ctx context.Context, query string, out []Hit, k int) []Hit {
	if s.rerank != nil && len(out) > 0 {
		reranked, err := s.rerank.Rerank(ctx, query, out, k)
		if err == nil {
			return reranked
		}
		s.logger.Warn("rerank failed; using raw order", "err", err)
		if len(out) > k {
			return out[:k]
		}
		return out
	}
	if len(out) > k {
		return out[:k]
	}
	return out
}

func (s *Search) searchAndMerge(ctx context.Context, req Request, mode string) ([]ranked, error) {
	var vecHits []ranked
	if mode == "vector" || mode == "hybrid" {
		var err error
		vecHits, err = s.searchVector(ctx, req)
		if err != nil {
			return nil, err
		}
	}
	var bm25Hits []ranked
	if mode == "bm25" || mode == "hybrid" {
		var err error
		bm25Hits, err = s.searchLexical(ctx, req)
		if err != nil {
			return nil, err
		}
	}
	var merged []ranked
	switch mode {
	case "vector":
		merged = vecHits
	case "bm25":
		merged = bm25Hits
	case "hybrid":
		merged = rrfMerge(vecHits, bm25Hits)
	}
	merged = trimToOverK(merged, req.K*3)
	return merged, nil
}

func (s *Search) Query(ctx context.Context, req Request) ([]Hit, error) {
	if err := req.validate(s); err != nil {
		return nil, err
	}
	mode := req.Mode

	// Hot-result cache: identical normalized queries short-circuit the embed +
	// retrieval + rerank work. Returns a copy so callers can't mutate the entry.
	var cacheKey string
	if s.results != nil && s.hitAuth == nil {
		cacheKey = resultCacheKey(req)
		if hits, ok := s.results.get(cacheKey); ok {
			s.recordUsage(ctx, req, hits)
			return hits, nil
		}
	}

	// Collect ranked lists per modality. Time from here (a confirmed cache miss)
	// so the histogram reflects real retrieval+rerank cost, not cache hits.
	start := time.Now()
	merged, err := s.searchAndMerge(ctx, req, mode)
	if err != nil {
		return nil, err
	}
	out := hitsFromRanked(merged)
	out, err = s.filterAuthorizedHits(ctx, req, out)
	if err != nil {
		return nil, err
	}

	out = s.applyRerankOrTrim(ctx, req.Query, out, req.K)

	s.recordUsage(ctx, req, out)
	telemetry.RecordSearchLatency(ctx, mode, float64(time.Since(start).Milliseconds()))
	if s.results != nil && s.hitAuth == nil {
		s.results.put(cacheKey, out)
	}
	return out, nil
}

func (s *Search) filterAuthorizedHits(ctx context.Context, req Request, hits []Hit) ([]Hit, error) {
	if s.hitAuth == nil {
		return hits, nil
	}
	out := make([]Hit, 0, len(hits))
	for _, hit := range hits {
		if err := s.hitAuth.CanReadObject(ctx, req.Tenant, hit.Bucket, hit.ObjectKey); err != nil {
			continue
		}
		out = append(out, hit)
	}
	return out, nil
}

func (s *Search) recordUsage(ctx context.Context, req Request, hits []Hit) {
	chunkIDs := make([]int64, 0, len(hits))
	objSeen := map[int64]struct{}{}
	objIDs := make([]int64, 0, len(hits))
	for _, h := range hits {
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
}
