package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Reranker is the post-retrieval cross-encoder. We over-retrieve from the
// vector / BM25 index (e.g. 3×k) then ask a stronger model to score each
// (query, chunk) pair and keep the top-k.
type Reranker interface {
	Rerank(ctx context.Context, query string, hits []Hit, topK int) ([]Hit, error)
	Name() string
}

// HTTPReranker calls a JSON service compatible with the Cohere / Voyage /
// bge-reranker-v2 wire shape:
//
//	POST {endpoint}/rerank
//	{ "model":"…", "query":"…", "documents":["..."], "top_n":k }
//	-> { "results": [{ "index": int, "relevance_score": float }, ...] }
type HTTPReranker struct {
	Endpoint string
	Model    string
	APIKey   string
	Client   *http.Client
}

func NewHTTPReranker(endpoint, model, apiKey string) *HTTPReranker {
	if endpoint == "" {
		return nil
	}
	return &HTTPReranker{
		Endpoint: strings.TrimRight(endpoint, "/"),
		Model:    model,
		APIKey:   apiKey,
		Client:   &http.Client{Timeout: 30 * time.Second},
	}
}

func (r *HTTPReranker) Name() string { return r.Model }

type rerankReq struct {
	Model     string   `json:"model,omitempty"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n"`
}

type rerankResp struct {
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
	} `json:"results"`
}

func (r *HTTPReranker) buildRerankPayload(query string, hits []Hit, topK int) ([]byte, error) {
	docs := make([]string, len(hits))
	for i, h := range hits {
		docs[i] = h.Chunk
	}
	return json.Marshal(rerankReq{Model: r.Model, Query: query, Documents: docs, TopN: topK})
}

func (r *HTTPReranker) parseRerankResponse(body []byte, hits []Hit, topK int) ([]Hit, error) {
	var out rerankResp
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	if len(out.Results) == 0 {
		return hits[:topK], nil
	}
	final := make([]Hit, 0, len(out.Results))
	for _, res := range out.Results {
		if res.Index < 0 || res.Index >= len(hits) {
			continue
		}
		h := hits[res.Index]
		h.Score = float32(res.RelevanceScore)
		final = append(final, h)
	}
	if len(final) > topK {
		final = final[:topK]
	}
	return final, nil
}

func (r *HTTPReranker) Rerank(ctx context.Context, query string, hits []Hit, topK int) ([]Hit, error) {
	if len(hits) == 0 {
		return nil, nil
	}
	if topK <= 0 || topK > len(hits) {
		topK = len(hits)
	}
	body, err := r.buildRerankPayload(query, hits, topK)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.Endpoint+"/rerank", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.APIKey)
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("reranker http %d: %s", resp.StatusCode, string(raw))
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return r.parseRerankResponse(respBody, hits, topK)
}

// HeuristicReranker is a dep-free fallback that boosts shorter chunks and
// chunks with more query-term overlap. Useful for offline tests; quality is
// noticeably below a real cross-encoder.
type HeuristicReranker struct{}

func (HeuristicReranker) Name() string { return "heuristic" }
func (HeuristicReranker) Rerank(_ context.Context, query string, hits []Hit, topK int) ([]Hit, error) {
	terms := tokenize(query)
	scored := make([]Hit, len(hits))
	copy(scored, hits)
	for i := range scored {
		ch := strings.ToLower(scored[i].Chunk)
		var hits int
		for _, t := range terms {
			if strings.Contains(ch, t) {
				hits++
			}
		}
		penalty := float32(len(ch)) / 4000.0
		scored[i].Score = float32(hits) - penalty + scored[i].Score*0.1
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
	if topK > 0 && len(scored) > topK {
		scored = scored[:topK]
	}
	return scored, nil
}
