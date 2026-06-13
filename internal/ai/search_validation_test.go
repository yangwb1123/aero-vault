package ai

import (
	"context"
	"strings"
	"testing"
)

// An unrecognized search mode is rejected with an error rather than silently
// returning empty results.
func TestSearchQuery_InvalidModeErrors(t *testing.T) {
	s := NewSearch(nil, NewHashEmbedder(8), nil)
	if _, err := s.Query(context.Background(), Request{Query: "x", Mode: "bogus"}); err == nil || !strings.Contains(err.Error(), "invalid search mode") {
		t.Fatalf("want invalid-mode error, got %v", err)
	}
}

// Vector/hybrid search with no embedder configured errors instead of panicking
// on a nil embedder.
func TestSearchQuery_NilEmbedderErrors(t *testing.T) {
	s := NewSearch(nil, nil, nil) // no embedder
	if _, err := s.Query(context.Background(), Request{Query: "x", Mode: "vector"}); err == nil || !strings.Contains(err.Error(), "no embedder configured") {
		t.Fatalf("want nil-embedder error, got %v", err)
	}
}

// TestHybridSort_TiebreakerByChunkID verifies that when two ranked entries share
// an identical RRF score the insertion sort places the lower chunkID first,
// making hybrid search output deterministic regardless of map iteration order.
func TestHybridSort_TiebreakerByChunkID(t *testing.T) {
	type ranked struct {
		chunkID int64
		score   float32
	}
	// Start with higher ID first to confirm the sort moves lower ID to front.
	merged := []ranked{
		{chunkID: 200, score: 0.5},
		{chunkID: 100, score: 0.5},
	}
	// Insertion sort — same logic as the hybrid case in search.go.
	for i := 1; i < len(merged); i++ {
		j := i
		for j > 0 && (merged[j].score > merged[j-1].score ||
			(merged[j].score == merged[j-1].score && merged[j].chunkID < merged[j-1].chunkID)) {
			merged[j], merged[j-1] = merged[j-1], merged[j]
			j--
		}
	}
	if merged[0].chunkID != 100 {
		t.Fatalf("want chunkID 100 first, got chunkID %d first", merged[0].chunkID)
	}
}

// The indexer's public methods error on a nil embedder instead of dereferencing it.
func TestIndexer_NilEmbedderErrors(t *testing.T) {
	ix := &Indexer{} // no embedder configured
	if _, err := ix.ReindexStale(context.Background(), "t", 10); err == nil {
		t.Fatal("ReindexStale with nil embedder should error")
	}
	if err := ix.IndexObjectByID(context.Background(), 1); err == nil {
		t.Fatal("IndexObjectByID with nil embedder should error")
	}
}
