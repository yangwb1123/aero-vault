package ai

import (
	"context"
	"math"
	"regexp"
	"strings"
	"sync"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// BM25 is an in-memory BM25 index over chunks. It is seeded once from the
// repository (BuildFromRepo) and then maintained incrementally as the indexer
// fans out per-object chunk changes through the ChunkSink seam — no periodic
// full-corpus rebuild.
//
// In production, swap this for an inverted index in Postgres (tsvector) or
// Meilisearch/Tantivy via an HTTP adapter.
type BM25 struct {
	k1, b float64

	mu       sync.RWMutex
	docs     map[int64]bm25Doc // chunkID -> doc
	df       map[string]int    // term -> doc frequency
	objDocs  map[int64][]int64 // objectID -> chunk IDs it owns
	avgLen   float64
	totalDoc int
	totalLen int // running sum of doc lengths, so avgLen stays O(1) to update
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
	return &BM25{k1: 1.5, b: 0.75, docs: map[int64]bm25Doc{}, df: map[string]int{}, objDocs: map[int64][]int64{}}
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
// given tenant. It iterates all buckets the tenant owns and paginates through
// all objects — not just the first 1000 in the "default" bucket. Call this
// on startup to warm the index from persisted data; subsequent writes are
// kept current via the ChunkSink (UpsertObjectChunks / DeleteObjectChunks).
func (b *BM25) BuildFromRepo(ctx context.Context, repo repository.Repository, tenant string) error {
	buckets, err := repo.ListBuckets(ctx, tenant)
	if err != nil {
		return err
	}
	// Collect all chunks before taking the write lock so the index is
	// unavailable for as short a window as possible.
	const pageSize = 500
	var all []repository.Chunk
	for _, bucket := range buckets {
		marker := ""
		for {
			page, err := repo.ListObjects(ctx, tenant, bucket, "", marker, pageSize)
			if err != nil {
				break
			}
			for _, o := range page.Objects {
				chunks, err := repo.ListChunksForObject(ctx, o.ID)
				if err != nil {
					continue
				}
				all = append(all, chunks...)
			}
			if !page.HasMore {
				break
			}
			marker = page.NextMarker
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.docs = make(map[int64]bm25Doc, len(all))
	b.df = make(map[string]int)
	b.objDocs = make(map[int64][]int64)
	b.totalDoc = 0
	b.totalLen = 0
	for _, c := range all {
		b.insertDocLocked(c)
	}
	b.recomputeAvgLenLocked()
	return nil
}

// insertDocLocked indexes one chunk as a doc, updating df, totalDoc, totalLen
// and the objectID->chunkIDs aux map. Caller holds b.mu.Lock and is responsible
// for calling recomputeAvgLenLocked afterwards.
func (b *BM25) insertDocLocked(c repository.Chunk) {
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
	b.objDocs[c.ObjectID] = append(b.objDocs[c.ObjectID], c.ID)
	b.totalDoc++
	b.totalLen += len(toks)
}

// removeObjectLocked drops every doc the aux map records for objectID,
// decrementing df per unique term (deleting the key at zero) and subtracting
// each doc's length from the running total. Caller holds b.mu.Lock and is
// responsible for calling recomputeAvgLenLocked afterwards.
func (b *BM25) removeObjectLocked(objectID int64) {
	for _, id := range b.objDocs[objectID] {
		d, ok := b.docs[id]
		if !ok {
			continue
		}
		for t := range d.tokens {
			if b.df[t] <= 1 {
				delete(b.df, t)
			} else {
				b.df[t]--
			}
		}
		b.totalLen -= d.length
		b.totalDoc--
		delete(b.docs, id)
	}
	delete(b.objDocs, objectID)
}

func (b *BM25) recomputeAvgLenLocked() {
	if b.totalDoc > 0 {
		b.avgLen = float64(b.totalLen) / float64(b.totalDoc)
	} else {
		b.avgLen = 0
	}
}

// UpsertObjectChunks replaces everything the index holds for objectID with
// exactly the given chunks (an empty slice clears the object). It implements
// ai.ChunkSink and is safe for concurrent use.
func (b *BM25) UpsertObjectChunks(_ context.Context, objectID int64, chunks []repository.Chunk) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.removeObjectLocked(objectID)
	for _, c := range chunks {
		b.insertDocLocked(c)
	}
	b.recomputeAvgLenLocked()
	return nil
}

// DeleteObjectChunks removes everything the index holds for objectID. It
// implements ai.ChunkSink and is safe for concurrent use.
func (b *BM25) DeleteObjectChunks(_ context.Context, objectID int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.removeObjectLocked(objectID)
	b.recomputeAvgLenLocked()
	return nil
}

var _ ChunkSink = (*BM25)(nil)

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
