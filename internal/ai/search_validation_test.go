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
