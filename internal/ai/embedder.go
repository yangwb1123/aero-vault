package ai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// Embedder produces a fixed-size float32 vector for each input text.
// Implementations must return len(texts) vectors, each of length Dimensions().
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Dimensions() int
	Name() string
}

// HashEmbedder is a deterministic, dependency-free fallback. It hashes each
// 5-rune shingle with SHA-256 and bins counts into Dim buckets, then L2-
// normalizes. Quality is poor compared to a real model, but it gives a working
// /search demo in environments without an embedding provider.
type HashEmbedder struct{ Dim int }

func NewHashEmbedder(dim int) *HashEmbedder {
	if dim <= 0 {
		dim = 256
	}
	return &HashEmbedder{Dim: dim}
}

func (h *HashEmbedder) Dimensions() int { return h.Dim }
func (h *HashEmbedder) Name() string    { return fmt.Sprintf("hash-%d", h.Dim) }

func (h *HashEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = h.encode(t)
	}
	return out, nil
}

func (h *HashEmbedder) encode(text string) []float32 {
	vec := make([]float32, h.Dim)
	runes := []rune(strings.ToLower(strings.TrimSpace(text)))
	const k = 5
	if len(runes) < k {
		runes = append(runes, []rune(strings.Repeat(" ", k-len(runes)))...)
	}
	for i := 0; i+k <= len(runes); i++ {
		shingle := string(runes[i : i+k])
		sum := sha256.Sum256([]byte(shingle))
		bucket := int(binary.LittleEndian.Uint32(sum[:4])) % h.Dim
		if bucket < 0 {
			bucket += h.Dim
		}
		vec[bucket]++
	}
	// L2-normalize so cosine == dot product.
	var s float64
	for _, x := range vec {
		s += float64(x) * float64(x)
	}
	norm := float32(math.Sqrt(s))
	if norm > 0 {
		for i := range vec {
			vec[i] /= norm
		}
	}
	return vec
}

// HTTPEmbedder calls an OpenAI-compatible /v1/embeddings endpoint. Works with
// OpenAI, Voyage, Ollama, vLLM, LocalAI, etc. — anything that speaks the
// embeddings shape:
//
//	POST {endpoint}/v1/embeddings
//	{ "model": "...", "input": ["..."] }
//	-> { "data": [{"embedding": [...]}, ...] }
type HTTPEmbedder struct {
	Endpoint string // e.g. "https://api.openai.com" or "http://localhost:11434"
	Model    string // e.g. "text-embedding-3-small"
	APIKey   string // optional, sent as Authorization: Bearer
	Dim      int    // declared dimensions of the model
	Client   *http.Client
}

func NewHTTPEmbedder(endpoint, model, apiKey string, dim int) *HTTPEmbedder {
	return &HTTPEmbedder{
		Endpoint: strings.TrimRight(endpoint, "/"),
		Model:    model,
		APIKey:   apiKey,
		Dim:      dim,
		Client:   &http.Client{Timeout: 30 * time.Second},
	}
}

func (e *HTTPEmbedder) Dimensions() int { return e.Dim }
func (e *HTTPEmbedder) Name() string    { return e.Model }

type embedReq struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResp struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

func (e *HTTPEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, _ := json.Marshal(embedReq{Model: e.Model, Input: texts})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.Endpoint+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.APIKey)
	}
	resp, err := e.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embedder http %d: %s", resp.StatusCode, string(b))
	}
	var er embedResp
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, err
	}
	if len(er.Data) != len(texts) {
		return nil, fmt.Errorf("embedder returned %d vectors for %d texts", len(er.Data), len(texts))
	}
	out := make([][]float32, len(er.Data))
	for i, d := range er.Data {
		out[i] = d.Embedding
	}
	if len(out) > 0 && e.Dim == 0 {
		e.Dim = len(out[0])
	}
	return out, nil
}
