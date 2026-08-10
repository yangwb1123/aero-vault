package ai

// Degraded hybrid-search read-path tests (D1 drill — spec
// docs/requirements/d1-drill-ai-read-path-degrade-v1.md, design v2 §7).
//
// Coverage-subset rationale (design R12): F6 (rerank failure after degrade)
// and F7 (usage-record failure) exercise unchanged code paths already covered
// by TestSearchWithReranker and the recordUsage warn path; F9 (counter
// registration failure) is the exact IncIndexerSkip mirror — no new tests
// here for unchanged code.
//
// Drift-pin attribution (design R12): degradeReason's literal strings are
// pinned by D-AC-6 (TestDegradeReason_Classification) and the end-to-end
// reasons==["embed"] semantics by AC-1 (TestSearchHybrid_DegradesToBM25On
// EmbedderFailure). The two pins are joint, not interchangeable: wrapper
// drift in searchVector reds AC-1, classifier drift reds D-AC-6, and the
// emission-side anchor inside D-AC-6 keeps the pin local.
//
// These tests MUST NOT use t.Parallel: internal/ai has zero parallel tests
// (package-wide -race safety for the recordSearchDegraded seam swap).

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

var errSentinelEmbed = errors.New("sentinel: embed provider down")
var errSentinelLex = errors.New("sentinel: lexical backend down")

// failingEmbedder is a configured-but-failing embedder: Embed always returns
// err without touching ctx (so a Background test ctx stays alive and the
// lexical half can complete), while Dimensions/Name delegate to inner so the
// Name() equals the seed chunks' EmbedModel (matchesEmbedModel filter).
type failingEmbedder struct {
	inner Embedder
	err   error
}

func (f *failingEmbedder) Embed(_ context.Context, _ []string) ([][]float32, error) {
	return nil, f.err
}
func (f *failingEmbedder) Dimensions() int { return f.inner.Dimensions() }
func (f *failingEmbedder) Name() string    { return f.inner.Name() }

// failingLexical is a configured-but-failing lexical backend (pgFTS analog).
type failingLexical struct{ err error }

func (f *failingLexical) SearchLexical(_ context.Context, _, _, _ string, _ int) ([]repository.SearchHit, error) {
	return nil, f.err
}

// failingVIndex is a configured-but-failing vector backend (pgvector/Qdrant
// analog).
type failingVIndex struct{ err error }

func (f *failingVIndex) SearchVectors(_ context.Context, _, _ string, _ []float32, _ int) ([]repository.SearchHit, error) {
	return nil, f.err
}

// captureHandler collects slog records so tests can assert warn messages.
type captureHandler struct{ records *[]slog.Record }

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	*h.records = append(*h.records, r)
	return nil
}
func (h *captureHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(_ string) slog.Handler      { return h }

// warnMessages returns the messages of all captured records at the given level.
func warnMessages(records []slog.Record, level slog.Level) []string {
	var out []string
	for _, r := range records {
		if r.Level == level {
			out = append(out, r.Message)
		}
	}
	return out
}

// swapSeam redirects recordSearchDegraded and returns the recorded reasons.
func swapSeam(t *testing.T) *[]string {
	t.Helper()
	orig := recordSearchDegraded
	var reasons []string
	recordSearchDegraded = func(_ context.Context, r string) { reasons = append(reasons, r) }
	t.Cleanup(func() { recordSearchDegraded = orig })
	return &reasons
}

// hybridEnv seeds one object with two chunks ("raft consensus" + baking) and a
// built BM25 index — the shared fixture for all degrade tests.
func hybridEnv(t *testing.T) (*testEnv, *HashEmbedder, *BM25) {
	t.Helper()
	env := newTestEnv(t)
	emb := NewHashEmbedder(128)
	o := env.putObject(t, "h.txt", "text/plain", "hybrid")
	env.seedChunks(t, o, emb,
		"distributed systems consensus raft protocol",
		"baking sourdough bread at home",
	)
	b := NewBM25()
	if err := b.BuildFromRepo(context.Background(), env.repo, testTenant); err != nil {
		t.Fatalf("build bm25: %v", err)
	}
	return env, emb, b
}

// AC-1: hybrid + failing embedder → BM25-only hits + warn "embed failed" +
// exactly one recordSearchDegraded("embed"); degraded result set equals the
// pure-bm25 result set (lexical ordering untouched).
func TestSearchHybrid_DegradesToBM25OnEmbedderFailure(t *testing.T) {
	env, emb, b := hybridEnv(t)
	reasons := swapSeam(t)
	var records []slog.Record
	capture := &captureHandler{records: &records}

	s := NewSearch(env.repo, &failingEmbedder{inner: emb, err: context.DeadlineExceeded},
		slog.New(capture)).WithBM25(b)

	hits, err := s.Query(context.Background(), Request{
		Tenant: testTenant, Query: "raft consensus", K: 5, Mode: "hybrid",
	})
	if err != nil {
		t.Fatalf("hybrid query with failing embedder: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected BM25-only hits after embedder failure")
	}
	if !strings.Contains(hits[0].Chunk, "raft") {
		t.Fatalf("expected raft chunk first in degraded hybrid, got %q", hits[0].Chunk)
	}
	msgs := warnMessages(records, slog.LevelWarn)
	if len(msgs) == 0 || !strings.Contains(msgs[0], "embed failed") {
		t.Fatalf("expected warn containing %q, got %v", "embed failed", msgs)
	}
	if len(*reasons) != 1 || (*reasons)[0] != "embed" {
		t.Fatalf("recordSearchDegraded reasons = %v, want [embed]", *reasons)
	}

	// Control: pure bm25 mode on the same fixture must yield the same hit set
	// (degraded BM25-only == real BM25-only, ordering and content).
	ctrl, err := s.Query(context.Background(), Request{
		Tenant: testTenant, Query: "raft consensus", K: 5, Mode: "bm25",
	})
	if err != nil {
		t.Fatalf("bm25 control query: %v", err)
	}
	if len(ctrl) != len(hits) {
		t.Fatalf("degraded hits (%d) differ from pure bm25 hits (%d)", len(hits), len(ctrl))
	}
	for i := range hits {
		if hits[i].Chunk != ctrl[i].Chunk || hits[i].ChunkID != ctrl[i].ChunkID {
			t.Fatalf("degraded hit %d (%q) != bm25 hit %d (%q): lexical ordering changed",
				i, hits[i].Chunk, i, ctrl[i].Chunk)
		}
	}
}

// AC-1 symmetric (FR-1 second clause): healthy embedder + failing lexical
// backend → vector-only hits + warn "lexical search failed" + exactly one
// recordSearchDegraded("lexical").
func TestSearchHybrid_DegradesToVectorOnLexicalFailure(t *testing.T) {
	env, emb, b := hybridEnv(t)
	reasons := swapSeam(t)
	var records []slog.Record
	capture := &captureHandler{records: &records}

	s := NewSearch(env.repo, emb, slog.New(capture)).WithBM25(b).
		WithLexicalIndex(&failingLexical{err: context.DeadlineExceeded})

	hits, err := s.Query(context.Background(), Request{
		Tenant: testTenant, Query: "raft consensus", K: 5, Mode: "hybrid",
	})
	if err != nil {
		t.Fatalf("hybrid query with failing lexical backend: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected vector-only hits after lexical failure")
	}
	msgs := warnMessages(records, slog.LevelWarn)
	if len(msgs) == 0 || !strings.Contains(msgs[0], "lexical search failed") {
		t.Fatalf("expected warn containing %q, got %v", "lexical search failed", msgs)
	}
	if len(*reasons) != 1 || (*reasons)[0] != "lexical" {
		t.Fatalf("recordSearchDegraded reasons = %v, want [lexical]", *reasons)
	}
}

// AC-3: pure modes keep errors visible — no degrade, no counting.
func TestSearchVectorMode_SurfacesEmbedderError(t *testing.T) {
	env, emb, b := hybridEnv(t)
	reasons := swapSeam(t)

	s := NewSearch(env.repo, &failingEmbedder{inner: emb, err: context.DeadlineExceeded}, nil).WithBM25(b)
	_, err := s.Query(context.Background(), Request{
		Tenant: testTenant, Query: "raft consensus", K: 5, Mode: "vector",
	})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pure vector mode must surface embedder error, got %v", err)
	}
	if len(*reasons) != 0 {
		t.Fatalf("pure vector mode must not count degrade, got reasons %v", *reasons)
	}

	// bm25 mode + failing lexical backend: same visibility rule.
	s2 := NewSearch(env.repo, emb, nil).WithBM25(b).
		WithLexicalIndex(&failingLexical{err: context.DeadlineExceeded})
	_, err = s2.Query(context.Background(), Request{
		Tenant: testTenant, Query: "raft consensus", K: 5, Mode: "bm25",
	})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pure bm25 mode must surface lexical error, got %v", err)
	}
	if len(*reasons) != 0 {
		t.Fatalf("pure bm25 mode must not count degrade, got reasons %v", *reasons)
	}
}

// D-AC-6: classifier literal pins + emission-side anchor (R8). Literal drift
// reds this test; wrapper-prefix drift in searchVector reds AC-1; the
// emission anchor keeps the pin local instead of relying on AC-1's
// end-to-end assertion.
func TestDegradeReason_Classification(t *testing.T) {
	if got := degradeReason(errors.New("embed query: boom")); got != "embed" {
		t.Fatalf("degradeReason(embed query:) = %q, want embed", got)
	}
	if got := degradeReason(errors.New("search chunks: boom")); got != "vector" {
		t.Fatalf("degradeReason(search chunks:) = %q, want vector", got)
	}
	if got := degradeReason(errors.New("anything else")); got != "vector" {
		t.Fatalf("degradeReason(anything else) = %q, want vector (drift fallback)", got)
	}
	if got := degradeReason(nil); got != "" {
		t.Fatalf("degradeReason(nil) = %q, want empty", got)
	}
	// Known conservative mislabel (search.go "embedder returned no vectors" is
	// prefixless): embed-stage failure labels as vector — pinned, not drift.
	if got := degradeReason(errors.New("embedder returned no vectors")); got != "vector" {
		t.Fatalf("degradeReason(embedder returned no vectors) = %q, want vector", got)
	}

	// Emission-side anchor: a real failing-embedder searchVector error must
	// carry the "embed query:" wrapper that AC-1's reasons==["embed"] depends on.
	env, emb, _ := hybridEnv(t)
	s := NewSearch(env.repo, &failingEmbedder{inner: emb, err: context.DeadlineExceeded}, nil)
	_, err := s.searchVector(context.Background(), Request{Tenant: testTenant, Query: "x", K: 5})
	if err == nil || !strings.Contains(err.Error(), "embed query:") {
		t.Fatalf("searchVector failing-embedder error must contain %q, got %v", "embed query:", err)
	}
}

// D-AC-7: both halves fail → deterministic first error (vector half priority,
// R3), no counting.
func TestSearchHybrid_BothHalvesFail_SurfacesError(t *testing.T) {
	env, emb, b := hybridEnv(t)
	reasons := swapSeam(t)

	s := NewSearch(env.repo, &failingEmbedder{inner: emb, err: errSentinelEmbed}, nil).WithBM25(b).
		WithLexicalIndex(&failingLexical{err: errSentinelLex})

	_, err := s.Query(context.Background(), Request{
		Tenant: testTenant, Query: "raft consensus", K: 5, Mode: "hybrid",
	})
	if err == nil || !errors.Is(err, errSentinelEmbed) {
		t.Fatalf("both-fail must surface the vector-half error (deterministic), got %v", err)
	}
	if !strings.Contains(err.Error(), "embed query:") {
		t.Fatalf("both-fail error must keep the vector-half phase wrap, got %v", err)
	}
	if len(*reasons) != 0 {
		t.Fatalf("both-fail must not count degrade (error is visible), got reasons %v", *reasons)
	}
}

// D-AC-8: deadline ctx → never degrade. (a) F4: failing half's wrapped error
// (phase preserved, errors.Is holds). (b) F11: both halves healthy but the
// deadline fired before the decision → bare ctx.Err().
func TestSearchHybrid_DeadlineCtx_NoDegrade(t *testing.T) {
	// (a) F4 — cancelled ctx + failing embedder: deadline preempts degrade,
	// the 500 body keeps "embed query:" and errors.Is(err, DeadlineExceeded).
	env, emb, b := hybridEnv(t)
	reasons := swapSeam(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s := NewSearch(env.repo, &failingEmbedder{inner: emb, err: context.DeadlineExceeded}, nil).WithBM25(b)
	_, err := s.Query(ctx, Request{Tenant: testTenant, Query: "raft consensus", K: 5, Mode: "hybrid"})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled ctx + failing embedder: want DeadlineExceeded, got %v", err)
	}
	if !strings.Contains(err.Error(), "embed query:") {
		t.Fatalf("F4 error must keep the phase wrap, got %v", err)
	}
	if len(*reasons) != 0 {
		t.Fatalf("deadline path must not count degrade, got reasons %v", *reasons)
	}

	// (b) F11 — cancelled ctx + both halves healthy (HashEmbedder ignores ctx,
	// fakeVectorIndex ignores ctx, in-memory BM25 ignores ctx): both succeed,
	// then the deadline fires in the decision window → bare ctx.Err().
	reasons2 := swapSeam(t)
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	fake := &fakeVectorIndex{hits: []repository.SearchHit{{
		Score: 0.9,
		Chunk: repository.Chunk{ID: 1, ObjectID: 1, Bucket: testBucket, ObjectKey: "h.txt", Seq: 0, Content: "raft", EmbedModel: emb.Name()},
	}}}
	s2 := NewSearch(env.repo, emb, nil).WithBM25(b).WithVectorIndex(fake)
	_, err = s2.Query(ctx2, Request{Tenant: testTenant, Query: "raft consensus", K: 5, Mode: "hybrid"})
	if err == nil || err != ctx2.Err() {
		t.Fatalf("F11 race window: want bare ctx.Err() (%v), got %v", ctx2.Err(), err)
	}
	if len(*reasons2) != 0 {
		t.Fatalf("F11 must not count degrade, got reasons %v", *reasons2)
	}
}

// D-AC-9 (R11): module-level end-to-end pin for the vector-half failure path
// (F2) — AC-2 is REST-level and cannot observe the seam; D-AC-6 only pins the
// classifier literals.
func TestSearchHybrid_DegradesToBM25OnVectorIndexFailure(t *testing.T) {
	env, emb, b := hybridEnv(t)
	reasons := swapSeam(t)
	var records []slog.Record
	capture := &captureHandler{records: &records}

	s := NewSearch(env.repo, emb, slog.New(capture)).WithBM25(b).
		WithVectorIndex(&failingVIndex{err: context.DeadlineExceeded})

	hits, err := s.Query(context.Background(), Request{
		Tenant: testTenant, Query: "raft consensus", K: 5, Mode: "hybrid",
	})
	if err != nil {
		t.Fatalf("hybrid query with failing vector backend: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected BM25-only hits after vector backend failure")
	}
	if !strings.Contains(hits[0].Chunk, "raft") {
		t.Fatalf("expected raft chunk first, got %q", hits[0].Chunk)
	}
	msgs := warnMessages(records, slog.LevelWarn)
	if len(msgs) == 0 || !strings.Contains(msgs[0], "vector index failed") {
		t.Fatalf("expected warn containing %q, got %v", "vector index failed", msgs)
	}
	if len(*reasons) != 1 || (*reasons)[0] != "vector" {
		t.Fatalf("recordSearchDegraded reasons = %v, want [vector]", *reasons)
	}
}
