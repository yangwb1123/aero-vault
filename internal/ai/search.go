package ai

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/telemetry"
)

// recordSearchDegraded reports degraded hybrid-search fallbacks. It's a
// package var so tests can observe reasons without a metrics reader.
var recordSearchDegraded = telemetry.IncSearchDegraded

// Search is the user-facing semantic-search service. It embeds the query,
// asks the repository for nearest chunks, optionally merges in BM25 results,
// optionally reranks with a cross-encoder, and records an AI usage row.
type Search struct {
	repo              repository.Repository
	embedder          Embedder
	bm25              *BM25
	rerank            Reranker
	vindex            VectorIndex
	lexical           LexicalIndex
	results           *resultCache
	logger            *slog.Logger
	hitAuth           HitAuthorizer
	hitAuthConfigured bool
	boundary          *SearchResourceBoundary
	resources         service.SpaceResourceReader
	resourcesExplicit bool
	canonical         bool // false only for the optional, wholly unconfigured legacy path
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
	return &Search{repo: repo, resources: repo, embedder: emb, vindex: repoVectorIndex{repo: repo}, logger: logger}
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
// identical repeated queries skip embedding and retrieval. Only candidates are
// cached; canonical proof, authorization and reranking run for every subject.
// A capacity<=0 or ttl<=0 leaves caching disabled (no-op). Corpus additions may
// remain absent until expiry; stale or revoked candidates are never delivered.
func (s *Search) WithResultCache(capacity int, ttl time.Duration) *Search {
	s.results = newResultCache(capacity, ttl)
	if s.results != nil {
		s.canonical = true
	}
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
	if r != nil {
		s.canonical = true
	}
	return s
}

// WithHitAuthorizer filters every candidate before it reaches search, chat, or
// agent callers. Cached candidates never retain subject-specific decisions.
func (s *Search) WithHitAuthorizer(authorizer HitAuthorizer) *Search {
	s.hitAuth, s.hitAuthConfigured = authorizer, true
	s.canonical = true
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
	return !nilLike(s.embedder)
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	vecs, err := s.embedder.Embed(ctx, []string{req.Query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("embedder returned no vectors")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	hits, err := s.vindex.SearchVectors(ctx, req.Tenant, req.Bucket, vecs[0], min(req.K*2, 100))
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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

func (s *Search) searchAndMerge(ctx context.Context, req Request, mode string) ([]ranked, error) {
	var vecHits []ranked
	var vecErr error
	if mode == "vector" || mode == "hybrid" {
		vecHits, vecErr = s.searchVector(ctx, req)
		if securityResourceError(vecErr) {
			return nil, vecErr
		}
	}
	var bm25Hits []ranked
	var lexErr error
	if mode == "bm25" || mode == "hybrid" {
		bm25Hits, lexErr = s.searchLexical(ctx, req)
	}
	return s.mergeRetrieved(ctx, req, mode, vecHits, bm25Hits, vecErr, lexErr)
}

func (s *Search) mergeRetrieved(ctx context.Context, req Request, mode string, vecHits, bm25Hits []ranked, vecErr, lexErr error) ([]ranked, error) {
	if err := retrievalSecurityError(vecErr, lexErr); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, cancelledRetrieval(ctx, vecErr, lexErr)
	}
	if err := s.validateRetrieved(ctx, req, vecHits, bm25Hits); err != nil {
		return nil, err
	}
	var merged []ranked
	switch mode {
	case "vector":
		if vecErr != nil {
			return nil, vecErr // pure mode: error stays visible (FR-3)
		}
		merged = vecHits
	case "bm25":
		if lexErr != nil {
			return nil, lexErr // pure mode: error stays visible (FR-3)
		}
		merged = bm25Hits
	case "hybrid":
		merged, vecErr = s.mergeHybrid(ctx, vecHits, bm25Hits, vecErr, lexErr)
		if vecErr != nil {
			return nil, vecErr
		}
	}
	return trimToOverK(merged, req.K*3), nil
}

func (s *Search) mergeHybrid(ctx context.Context, vecHits, bm25Hits []ranked, vecErr, lexErr error) ([]ranked, error) {
	switch {
	case vecErr != nil && lexErr != nil:
		return nil, vecErr // both halves failed: no healthy half to degrade to
	case vecErr != nil:
		reason := degradeReason(vecErr)
		s.warnDegrade(reason, vecErr)
		recordSearchDegraded(ctx, reason)
		return bm25Hits, nil // BM25-only, preserve backend order
	case lexErr != nil:
		s.warnDegrade("lexical", lexErr)
		recordSearchDegraded(ctx, "lexical")
		return vecHits, nil // vector-only, preserve backend order
	default:
		return rrfMerge(vecHits, bm25Hits), nil
	}
}

// warnDegrade logs the hybrid-fallback warn for a failed modality. Message
// substrings are pinned by tests (FR-1): "embed failed" / "vector index
// failed" / "lexical search failed".
func (s *Search) warnDegrade(reason string, err error) {
	switch reason {
	case "embed":
		s.logger.Warn("embed failed; falling back to lexical results", "err", err)
	case "lexical":
		s.logger.Warn("lexical search failed; falling back to vector results", "err", err)
	default:
		s.logger.Warn("vector index failed; falling back to lexical results", "err", err)
	}
}

// degradeReason classifies a vector-half failure for the degraded counter.
// Anchored on searchVector's stable wrapper prefixes ("embed query:" /
// "search chunks:"); drift falls back to "vector" and is caught JOINTLY by
// D-AC-6 (literal + emission-side pins) and AC-1 (end-to-end
// reasons==["embed"]): classifier drift → D-AC-6 red; wrapper drift → AC-1
// red; both changed → pins move with the change (correct procedure).
func degradeReason(err error) string {
	switch {
	case err == nil:
		return ""
	case strings.HasPrefix(err.Error(), "embed query:"):
		return "embed"
	default:
		return "vector" // "search chunks:" or any prefix drift
	}
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
