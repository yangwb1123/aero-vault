// Package ai: Qdrant-backed VectorIndex + ChunkSink adapter.
//
// QdrantIndex offloads retrieval to a dedicated Qdrant vector store for very
// large deployments (ROADMAP #1). It is the second consumer of the two AI seams:
// it implements the READ seam (VectorIndex.SearchVectors) and the WRITE seam
// (ChunkSink.UpsertObjectChunks / DeleteObjectChunks), so chunks written by the
// indexer flow into Qdrant and queries flow back out — with NO change to Search
// or the indexer. It speaks Qdrant's REST API directly over net/http +
// encoding/json (stdlib only; no client library), exactly like HTTPEmbedder and
// HTTPLLM.
//
// CAVEATS:
//
//   - OPT-IN: nothing wires it in by default. An operator selects it via
//     AI_VECTOR_BACKEND=qdrant; the brute-force repository scan stays the
//     default and is byte-for-byte unchanged.
//   - Collection lifecycle is the operator's responsibility: create the
//     collection (with the right vector size + distance) out of band, e.g.
//     PUT /collections/aero_chunks {"vectors":{"size":<dim>,"distance":"Cosine"}}.
//     The adapter does not create it (the embedding dimension is not known here).
//   - UNVERIFIED in CI: there is no Qdrant in the test harness, so the live
//     round-trip against a real server is NOT exercised. Correctness here is
//     pinned by the httptest contract tests in qdrant_test.go (request paths,
//     methods, the api-key header, JSON request bodies, response decoding, and
//     error handling); runtime behaviour against a live Qdrant is unverified.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// var _ asserts QdrantIndex satisfies BOTH seams.
var (
	_ VectorIndex = (*QdrantIndex)(nil)
	_ ChunkSink   = (*QdrantIndex)(nil)
)

const defaultQdrantCollection = "aero_chunks"

// QdrantOptions configures a QdrantIndex.
type QdrantOptions struct {
	// BaseURL is the Qdrant REST root, e.g. "http://localhost:6333". A trailing
	// slash is trimmed (like HTTPEmbedder.Endpoint).
	BaseURL string
	// APIKey, when non-empty, is sent as the "api-key" header (Qdrant's auth header).
	APIKey string
	// Collection holds the chunk points. Default: "aero_chunks".
	Collection string
	// Client is optional; defaults to a client with a sane timeout.
	Client *http.Client
}

// QdrantIndex is a VectorIndex + ChunkSink backed by a Qdrant collection. It
// holds only immutable config plus an *http.Client (itself safe for concurrent
// use), so it carries no mutable shared state and needs no mutex.
type QdrantIndex struct {
	baseURL    string
	apiKey     string
	collection string
	client     *http.Client
}

// NewQdrantIndex builds a QdrantIndex. It is lazy: no network call is made here,
// so a misconfigured URL cannot crash a server that never indexes.
func NewQdrantIndex(opts QdrantOptions) *QdrantIndex {
	collection := opts.Collection
	if collection == "" {
		collection = defaultQdrantCollection
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &QdrantIndex{
		baseURL:    strings.TrimRight(opts.BaseURL, "/"),
		apiKey:     opts.APIKey,
		collection: collection,
		client:     client,
	}
}

// qdrantFilter is Qdrant's filter shape: a list of `must` field-match conditions.
type qdrantFilter struct {
	Must []qdrantMatch `json:"must"`
}

type qdrantMatch struct {
	Key   string         `json:"key"`
	Match qdrantMatchVal `json:"match"`
}

type qdrantMatchVal struct {
	Value any `json:"value"`
}

// scopeFilter builds the tenant (and optional bucket) match filter the other
// adapters enforce: tenant is mandatory; bucket is added only when non-empty.
func scopeFilter(tenant, bucket string) qdrantFilter {
	f := qdrantFilter{Must: []qdrantMatch{{Key: "tenant_id", Match: qdrantMatchVal{Value: tenant}}}}
	if bucket != "" {
		f.Must = append(f.Must, qdrantMatch{Key: "bucket", Match: qdrantMatchVal{Value: bucket}})
	}
	return f
}

type qdrantSearchReq struct {
	Vector      []float32    `json:"vector"`
	Limit       int          `json:"limit"`
	WithPayload bool         `json:"with_payload"`
	Filter      qdrantFilter `json:"filter"`
}

// chunkPayload is the chunk's fields stored on a point and read back on search.
type chunkPayload struct {
	TenantID   string `json:"tenant_id"`
	Bucket     string `json:"bucket"`
	ObjectID   int64  `json:"object_id"`
	ObjectKey  string `json:"object_key"`
	Seq        int    `json:"seq"`
	Content    string `json:"content"`
	Dim        int    `json:"dim"`
	EmbedModel string `json:"embed_model"`
}

type qdrantSearchResp struct {
	Result []struct {
		ID      int64        `json:"id"`
		Score   float32      `json:"score"`
		Payload chunkPayload `json:"payload"`
	} `json:"result"`
}

// SearchVectors runs a Qdrant nearest-neighbour search scoped to the tenant (and
// optional bucket) and maps the points back to repository SearchHits. Limit is
// clamped like PgVectorIndex (<=0 or too large -> default 10).
func (q *QdrantIndex) SearchVectors(ctx context.Context, tenant, bucket string, query []float32, limit int) ([]repository.SearchHit, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	reqBody := qdrantSearchReq{
		Vector:      query,
		Limit:       limit,
		WithPayload: true,
		Filter:      scopeFilter(tenant, bucket),
	}
	var resp qdrantSearchResp
	if err := q.do(ctx, http.MethodPost, "/points/search", "", reqBody, &resp); err != nil {
		return nil, err
	}
	hits := make([]repository.SearchHit, 0, len(resp.Result))
	for _, p := range resp.Result {
		hits = append(hits, repository.SearchHit{
			Chunk: repository.Chunk{
				ID:         p.ID,
				TenantID:   p.Payload.TenantID,
				Bucket:     p.Payload.Bucket,
				ObjectID:   p.Payload.ObjectID,
				ObjectKey:  p.Payload.ObjectKey,
				Seq:        p.Payload.Seq,
				Content:    p.Payload.Content,
				Dim:        p.Payload.Dim,
				EmbedModel: p.Payload.EmbedModel,
			},
			Score: p.Score,
		})
	}
	return hits, nil
}

type qdrantPoint struct {
	ID      int64        `json:"id"`
	Vector  []float32    `json:"vector"`
	Payload chunkPayload `json:"payload"`
}

type qdrantUpsertReq struct {
	Points []qdrantPoint `json:"points"`
}

type qdrantDeleteReq struct {
	Filter qdrantFilter `json:"filter"`
}

// UpsertObjectChunks replaces everything Qdrant holds for this object with
// exactly these chunks. Because a re-index can change the chunk-ID set, it first
// deletes all existing points for object_id (so stale points can't linger), then
// upserts the new points (id = chunk.ID, vector = chunk.Embedding, payload = the
// chunk fields). Chunks with empty embeddings are skipped. The upsert uses
// ?wait=true so the write is durable before returning (the durable indexer job
// retries on error).
func (q *QdrantIndex) UpsertObjectChunks(ctx context.Context, objectID int64, chunks []repository.Chunk) error {
	if err := q.DeleteObjectChunks(ctx, objectID); err != nil {
		return err
	}
	points := make([]qdrantPoint, 0, len(chunks))
	for _, c := range chunks {
		if len(c.Embedding) == 0 {
			continue
		}
		points = append(points, qdrantPoint{
			ID:     c.ID,
			Vector: c.Embedding,
			Payload: chunkPayload{
				TenantID:   c.TenantID,
				Bucket:     c.Bucket,
				ObjectID:   c.ObjectID,
				ObjectKey:  c.ObjectKey,
				Seq:        c.Seq,
				Content:    c.Content,
				Dim:        c.Dim,
				EmbedModel: c.EmbedModel,
			},
		})
	}
	if len(points) == 0 {
		// Nothing to write; the delete above already purged stale points.
		return nil
	}
	return q.do(ctx, http.MethodPut, "/points", "wait=true", qdrantUpsertReq{Points: points}, nil)
}

// DeleteObjectChunks removes every point for this object via a delete-by-filter
// on object_id.
func (q *QdrantIndex) DeleteObjectChunks(ctx context.Context, objectID int64) error {
	body := qdrantDeleteReq{Filter: qdrantFilter{
		Must: []qdrantMatch{{Key: "object_id", Match: qdrantMatchVal{Value: objectID}}},
	}}
	return q.do(ctx, http.MethodPost, "/points/delete", "wait=true", body, nil)
}

// do performs one Qdrant REST call: it encodes reqBody as JSON, sets the
// Content-Type and (when configured) api-key headers, propagates ctx, treats any
// non-2xx as a wrapped error (with a bounded error body), and decodes the
// response into out when out != nil.
func (q *QdrantIndex) do(ctx context.Context, method, subPath, rawQuery string, reqBody, out any) error {
	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("qdrant: encode request: %w", err)
	}
	url := q.baseURL + "/collections/" + q.collection + subPath
	if rawQuery != "" {
		url += "?" + rawQuery
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("qdrant: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if q.apiKey != "" {
		req.Header.Set("api-key", q.apiKey)
	}
	resp, err := q.client.Do(req)
	if err != nil {
		return fmt.Errorf("qdrant: %s %s: %w", method, subPath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("qdrant http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("qdrant: decode response: %w", err)
	}
	return nil
}
