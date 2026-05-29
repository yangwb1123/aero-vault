package ai

import (
	"context"
	"math"
	"regexp"
	"strings"
	"sync"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// BM25 is an in-memory BM25 index over chunks. It is rebuilt on demand from
// the repository. For an MVP-grade search, this is fine: the corpus is small
// enough that a periodic rebuild is cheap.
//
// In production, swap this for an inverted index in Postgres (tsvector) or
// Meilisearch/Tantivy via an HTTP adapter.
type BM25 struct {
	k1, b float64

	mu       sync.RWMutex
	docs     map[int64]bm25Doc // chunkID -> doc
	df       map[string]int    // term -> doc frequency
	avgLen   float64
	totalDoc int
}

type bm25Doc struct {
	tenant    string
	bucket    string
	objectKey string
	objectID  int64
	seq       int
	content   string
	length    int
	tokens    map[string]int // term -> tf
}

func NewBM25() *BM25 {
	return &BM25{k1: 1.5, b: 0.75, docs: map[int64]bm25Doc{}, df: map[string]int{}}
}

var tokenRe = regexp.MustCompile(`[\p{L}\p{N}]+`)

func tokenize(text string) []string {
	matches := tokenRe.FindAllString(strings.ToLower(text), -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) >= 2 {
			out = append(out, m)
		}
	}
	return out
}

// BuildFromRepo refreshes the index from every chunk in the repository for the
// given tenant. Call this on startup and periodically (or after a write
// burst).
func (b *BM25) BuildFromRepo(ctx context.Context, repo repository.Repository, tenant string) error {
	// Load all chunks via SearchChunks with a zero-length query vector trick is
	// not viable; we read object lists then chunks per object.
	page, err := repo.ListObjects(ctx, tenant, "default", "", "", 1000)
	if err != nil {
		return err
	}
	var all []repository.Chunk
	for _, o := range page.Objects {
		chunks, err := repo.ListChunksForObject(ctx, o.ID)
		if err != nil {
			continue
		}
		all = append(all, chunks...)
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.docs = make(map[int64]bm25Doc, len(all))
	b.df = make(map[string]int)
	totalLen := 0
	for _, c := range all {
		toks := tokenize(c.Content)
		tf := make(map[string]int, len(toks))
		for _, t := range toks {
			tf[t]++
		}
		for t := range tf {
			b.df[t]++
		}
		b.docs[c.ID] = bm25Doc{
			tenant: c.TenantID, bucket: c.Bucket, objectKey: c.ObjectKey,
			objectID: c.ObjectID, seq: c.Seq, content: c.Content, length: len(toks), tokens: tf,
		}
		totalLen += len(toks)
	}
	b.totalDoc = len(b.docs)
	if b.totalDoc > 0 {
		b.avgLen = float64(totalLen) / float64(b.totalDoc)
	}
	return nil
}

type bm25Hit struct {
	ChunkID int64
	Score   float64
	Doc     bm25Doc
}

// Search scores every doc against the query terms.
func (b *BM25) Search(query string, bucket string, limit int) []bm25Hit {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.totalDoc == 0 || limit <= 0 {
		return nil
	}
	terms := tokenize(query)
	if len(terms) == 0 {
		return nil
	}
	var out []bm25Hit
	for id, d := range b.docs {
		if bucket != "" && d.bucket != bucket {
			continue
		}
		var score float64
		for _, t := range terms {
			tf := d.tokens[t]
			if tf == 0 {
				continue
			}
			df := b.df[t]
			if df == 0 {
				continue
			}
			idf := math.Log(1 + (float64(b.totalDoc)-float64(df)+0.5)/(float64(df)+0.5))
			norm := float64(tf) * (b.k1 + 1) / (float64(tf) + b.k1*(1-b.b+b.b*float64(d.length)/b.avgLen))
			score += idf * norm
		}
		if score > 0 {
			out = append(out, bm25Hit{ChunkID: id, Score: score, Doc: d})
		}
	}
	// Top-N sort
	sortHitsDesc(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func sortHitsDesc(h []bm25Hit) {
	for i := 1; i < len(h); i++ {
		j := i
		for j > 0 && h[j].Score > h[j-1].Score {
			h[j], h[j-1] = h[j-1], h[j]
			j--
		}
	}
}
