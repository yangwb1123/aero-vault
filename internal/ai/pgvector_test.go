package ai

import (
	"context"
	"testing"
)

// Compile-time conformance is also asserted in pgvector.go; restate it here so
// the test file fails to build if the contract drifts.
var _ VectorIndex = (*PgVectorIndex)(nil)

func TestOpenPgVectorIndexEmptyDSN(t *testing.T) {
	idx, err := OpenPgVectorIndex(context.Background(), "", PgVectorOptions{})
	if err == nil {
		t.Fatal("expected error for empty dsn, got nil")
	}
	if idx != nil {
		t.Fatalf("expected nil index on error, got %v", idx)
	}
}

func TestVectorLiteral(t *testing.T) {
	cases := []struct {
		in   []float32
		want string
	}{
		{[]float32{1, 2, 3}, "[1,2,3]"},
		{[]float32{}, "[]"},
		{nil, "[]"},
		{[]float32{0.5, -1.25}, "[0.5,-1.25]"},
	}
	for _, c := range cases {
		if got := vectorLiteral(c.in); got != c.want {
			t.Errorf("vectorLiteral(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPgVectorOptionsDefaults(t *testing.T) {
	o := PgVectorOptions{}.withDefaults()
	if o.Table != "chunks" {
		t.Errorf("default Table = %q, want %q", o.Table, "chunks")
	}
	if o.VectorColumn != "embedding_vec" {
		t.Errorf("default VectorColumn = %q, want %q", o.VectorColumn, "embedding_vec")
	}
}
