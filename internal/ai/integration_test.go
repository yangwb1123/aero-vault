package ai

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

const testTenant = "default"
const testBucket = "default"

type testEnv struct {
	repo  repository.Repository
	store storage.Storage
	svc   *service.FileService
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatal(err)
	}
	svc := service.NewFileService(store, repo, nil)
	return &testEnv{repo: repo, store: store, svc: svc}
}

// putObject writes an object through the service (auto-creates the bucket).
func (e *testEnv) putObject(t *testing.T, key, contentType, body string) repository.Object {
	t.Helper()
	obj, err := e.svc.Put(context.Background(), testTenant, testBucket, key,
		strings.NewReader(body), int64(len(body)), service.PutOptions{ContentType: contentType})
	if err != nil {
		t.Fatalf("put %q: %v", key, err)
	}
	return obj
}

// seedChunks inserts chunks for an object with HashEmbedder embeddings so that
// vector search has something to match.
func (e *testEnv) seedChunks(t *testing.T, obj repository.Object, emb Embedder, contents ...string) {
	t.Helper()
	vecs, err := emb.Embed(context.Background(), contents)
	if err != nil {
		t.Fatalf("embed seed chunks: %v", err)
	}
	chunks := make([]repository.Chunk, 0, len(contents))
	for i, c := range contents {
		chunks = append(chunks, repository.Chunk{
			ObjectID:   obj.ID,
			TenantID:   obj.TenantID,
			Bucket:     obj.Bucket,
			ObjectKey:  obj.Key,
			Seq:        i,
			Content:    c,
			Embedding:  vecs[i],
			Dim:        len(vecs[i]),
			EmbedModel: emb.Name(),
		})
	}
	if err := e.repo.InsertChunks(context.Background(), chunks); err != nil {
		t.Fatalf("insert chunks: %v", err)
	}
}

// --- BM25 BuildFromRepo + Search ---

func TestBM25BuildAndSearch(t *testing.T) {
	env := newTestEnv(t)
	emb := NewHashEmbedder(64)

	o1 := env.putObject(t, "doc1.txt", "text/plain", "about cats")
	o2 := env.putObject(t, "doc2.txt", "text/plain", "about dogs")
	env.seedChunks(t, o1, emb, "the cat sat on the mat", "feline companions are quiet")
	env.seedChunks(t, o2, emb, "the dog ran in the park", "canine friends love walks")

	b := NewBM25()
	if err := b.BuildFromRepo(context.Background(), env.repo, testTenant); err != nil {
		t.Fatalf("build: %v", err)
	}

	hits := b.Search("cat feline", "", 10)
	if len(hits) == 0 {
		t.Fatal("expected BM25 hits for 'cat feline'")
	}
	// Top hit should belong to the cat object.
	if hits[0].Doc.objectID != o1.ID {
		t.Fatalf("expected top hit from cat object %d, got object %d", o1.ID, hits[0].Doc.objectID)
	}
	// Scores must be in descending order.
	for i := 1; i < len(hits); i++ {
		if hits[i].Score > hits[i-1].Score {
			t.Fatalf("hits not sorted desc at %d: %f > %f", i, hits[i].Score, hits[i-1].Score)
		}
	}
}

func TestBM25SearchEmptyAndBucketFilter(t *testing.T) {
	env := newTestEnv(t)
	emb := NewHashEmbedder(64)
	o1 := env.putObject(t, "d.txt", "text/plain", "data")
	env.seedChunks(t, o1, emb, "alpha beta gamma")

	b := NewBM25()
	if err := b.BuildFromRepo(context.Background(), env.repo, testTenant); err != nil {
		t.Fatalf("build: %v", err)
	}
	// Empty query -> no hits.
	if got := b.Search("", "", 10); got != nil {
		t.Fatalf("empty query should return nil, got %v", got)
	}
	// limit<=0 -> no hits.
	if got := b.Search("alpha", "", 0); got != nil {
		t.Fatalf("limit<=0 should return nil, got %v", got)
	}
	// Non-existent bucket filter -> no hits.
	if got := b.Search("alpha", "no-such-bucket", 10); len(got) != 0 {
		t.Fatalf("bucket filter should exclude all, got %d hits", len(got))
	}
	// Matching bucket -> hit.
	if got := b.Search("alpha", testBucket, 10); len(got) == 0 {
		t.Fatal("matching bucket should return a hit")
	}
}

func TestBM25EmptyIndex(t *testing.T) {
	b := NewBM25()
	if got := b.Search("anything", "", 10); got != nil {
		t.Fatalf("empty index should return nil, got %v", got)
	}
}

// --- Search service ---

func TestSearchVectorMode(t *testing.T) {
	env := newTestEnv(t)
	emb := NewHashEmbedder(128)
	o := env.putObject(t, "kb.txt", "text/plain", "knowledge")
	env.seedChunks(t, o, emb,
		"reciprocal rank fusion combines result lists",
		"the weather is sunny today with clear skies",
	)

	s := NewSearch(env.repo, emb, nil)
	hits, err := s.Query(context.Background(), Request{
		Tenant: testTenant, Query: "rank fusion result lists", K: 5, Mode: "vector",
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one vector hit")
	}
	// The fusion chunk should outrank the weather chunk for this query.
	if !strings.Contains(hits[0].Chunk, "fusion") {
		t.Fatalf("expected fusion chunk first, got %q", hits[0].Chunk)
	}
	if hits[0].ObjectKey != "kb.txt" || hits[0].EmbedModel != emb.Name() {
		t.Fatalf("hit metadata mismatch: %+v", hits[0])
	}
}

func TestSearchEmptyQueryError(t *testing.T) {
	env := newTestEnv(t)
	s := NewSearch(env.repo, NewHashEmbedder(64), nil)
	if _, err := s.Query(context.Background(), Request{Tenant: testTenant, Query: ""}); err == nil {
		t.Fatal("empty query should error")
	}
}

func TestSearchBM25ModeRequiresIndex(t *testing.T) {
	env := newTestEnv(t)
	s := NewSearch(env.repo, NewHashEmbedder(64), nil) // no WithBM25
	_, err := s.Query(context.Background(), Request{Tenant: testTenant, Query: "x", Mode: "bm25"})
	if err == nil || !strings.Contains(err.Error(), "bm25 not enabled") {
		t.Fatalf("want bm25-not-enabled error, got %v", err)
	}
}

func TestSearchHybridMode(t *testing.T) {
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
	s := NewSearch(env.repo, emb, nil).WithBM25(b)
	hits, err := s.Query(context.Background(), Request{
		Tenant: testTenant, Query: "raft consensus protocol", K: 5, Mode: "hybrid",
	})
	if err != nil {
		t.Fatalf("hybrid query: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hybrid hits")
	}
	if !strings.Contains(hits[0].Chunk, "raft") {
		t.Fatalf("expected raft chunk to rank first in hybrid, got %q", hits[0].Chunk)
	}
}

func TestSearchWithReranker(t *testing.T) {
	env := newTestEnv(t)
	emb := NewHashEmbedder(128)
	o := env.putObject(t, "r.txt", "text/plain", "rr")
	env.seedChunks(t, o, emb,
		"golang concurrency goroutines channels",
		"python list comprehensions",
		"rust ownership and borrowing",
	)
	s := NewSearch(env.repo, emb, nil).WithReranker(HeuristicReranker{})
	hits, err := s.Query(context.Background(), Request{
		Tenant: testTenant, Query: "golang goroutines channels", K: 2, Mode: "vector",
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(hits) == 0 || len(hits) > 2 {
		t.Fatalf("rerank topK=2: got %d hits", len(hits))
	}
	if !strings.Contains(hits[0].Chunk, "golang") {
		t.Fatalf("reranker should surface golang chunk, got %q", hits[0].Chunk)
	}
}

func TestSearchRecordsUsage(t *testing.T) {
	env := newTestEnv(t)
	emb := NewHashEmbedder(64)
	o := env.putObject(t, "u.txt", "text/plain", "usage")
	env.seedChunks(t, o, emb, "audit lineage tracking record")

	s := NewSearch(env.repo, emb, nil)
	hits, err := s.Query(context.Background(), Request{
		Tenant: testTenant, Query: "audit lineage record", K: 5, Mode: "vector", Caller: "test:search",
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected a hit so usage is recorded")
	}
	usage, err := env.repo.ListUsageForObject(context.Background(), testTenant, o.ID, 10)
	if err != nil {
		t.Fatalf("list usage: %v", err)
	}
	if len(usage) == 0 {
		t.Fatal("expected a recorded usage row referencing the object")
	}
	if usage[0].Caller != "test:search" {
		t.Fatalf("usage caller: want test:search, got %q", usage[0].Caller)
	}
}

// --- Chat ---

func TestChatAnswer(t *testing.T) {
	env := newTestEnv(t)
	emb := NewHashEmbedder(128)
	o := env.putObject(t, "c.txt", "text/plain", "chat kb")
	env.seedChunks(t, o, emb, "the capital of france is paris", "unrelated trivia about cheese")

	s := NewSearch(env.repo, emb, nil)
	chat := NewChat(s, MockLLM{}, env.repo, nil)
	resp, err := chat.Answer(context.Background(), ChatReq{
		Tenant: testTenant, Query: "capital of france paris", K: 5, Mode: "vector",
	})
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if resp.Model != "mock" {
		t.Fatalf("model: want mock, got %q", resp.Model)
	}
	if !strings.Contains(resp.Answer, "[mock-llm]") {
		t.Fatalf("expected mock answer, got %q", resp.Answer)
	}
	// MockLLM echoes the last user message (the query).
	if !strings.Contains(resp.Answer, "capital of france paris") {
		t.Fatalf("mock answer should echo the query, got %q", resp.Answer)
	}
	if len(resp.Citations) == 0 {
		t.Fatal("expected citations from retrieval")
	}
}

func TestChatAnswerNilLLMAndEmptyQuery(t *testing.T) {
	env := newTestEnv(t)
	s := NewSearch(env.repo, NewHashEmbedder(64), nil)

	noLLM := NewChat(s, nil, env.repo, nil)
	if _, err := noLLM.Answer(context.Background(), ChatReq{Tenant: testTenant, Query: "x"}); err == nil ||
		!strings.Contains(err.Error(), "no LLM configured") {
		t.Fatalf("nil LLM should error, got %v", err)
	}

	chat := NewChat(s, MockLLM{}, env.repo, nil)
	if _, err := chat.Answer(context.Background(), ChatReq{Tenant: testTenant, Query: ""}); err == nil {
		t.Fatal("empty query should error")
	}
}

func TestChatAnswerStream(t *testing.T) {
	env := newTestEnv(t)
	emb := NewHashEmbedder(128)
	o := env.putObject(t, "cs.txt", "text/plain", "stream kb")
	env.seedChunks(t, o, emb, "streaming tokens arrive incrementally over sse")

	s := NewSearch(env.repo, emb, nil)
	chat := NewChat(s, MockLLM{}, env.repo, nil)

	var streamed strings.Builder
	resp, err := chat.AnswerStream(context.Background(), ChatReq{
		Tenant: testTenant, Query: "streaming tokens sse", K: 5, Mode: "vector",
	}, func(chunk string) {
		streamed.WriteString(chunk)
	})
	if err != nil {
		t.Fatalf("answer stream: %v", err)
	}
	if resp.Answer == "" {
		t.Fatal("expected non-empty streamed answer")
	}
	// MockLLM.ChatStream emits the content word-by-word; the concatenation
	// should contain the same text (modulo trailing spaces).
	if !strings.Contains(strings.TrimSpace(streamed.String()), "mock-llm") {
		t.Fatalf("streamed chunks should reconstruct the mock answer, got %q", streamed.String())
	}
}

// --- Agent ---

// scriptedLLM returns canned responses in sequence, letting us drive the agent
// tool loop deterministically. It satisfies the LLM interface.
type scriptedLLM struct {
	responses []ChatResponse
	calls     int
	lastReq   ChatRequest
}

func (s *scriptedLLM) Name() string { return "scripted" }
func (s *scriptedLLM) Chat(_ context.Context, req ChatRequest) (ChatResponse, error) {
	s.lastReq = req
	if s.calls >= len(s.responses) {
		// Default terminal response.
		return ChatResponse{Content: "done", Model: "scripted"}, nil
	}
	r := s.responses[s.calls]
	s.calls++
	return r, nil
}
func (s *scriptedLLM) ChatStream(ctx context.Context, req ChatRequest, onChunk func(string)) (ChatResponse, error) {
	return s.Chat(ctx, req)
}

func toolCall(name, args string) ToolCall {
	return ToolCall{
		ID:       "tc",
		Type:     "function",
		Function: map[string]any{"name": name, "arguments": args},
	}
}

func TestAgentRunNoToolCall(t *testing.T) {
	env := newTestEnv(t)
	s := NewSearch(env.repo, NewHashEmbedder(64), nil)
	agent := NewAgent(env.svc, s, MockLLM{}, env.repo, nil)
	if agent.MaxSteps != 4 {
		t.Fatalf("default MaxSteps want 4, got %d", agent.MaxSteps)
	}
	resp, err := agent.Run(context.Background(), AgentReq{Tenant: testTenant, Query: "just answer directly"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// MockLLM never returns tool calls, so the agent answers on step 0.
	if !strings.Contains(resp.Answer, "[mock-llm]") {
		t.Fatalf("expected mock direct answer, got %q", resp.Answer)
	}
	if len(resp.Steps) != 0 {
		t.Fatalf("expected no tool steps, got %d", len(resp.Steps))
	}
}

func TestAgentRunListFilesTool(t *testing.T) {
	env := newTestEnv(t)
	env.putObject(t, "alpha.txt", "text/plain", "content a")
	env.putObject(t, "beta.txt", "text/plain", "content beta longer")

	s := NewSearch(env.repo, NewHashEmbedder(64), nil)
	llm := &scriptedLLM{responses: []ChatResponse{
		// Step 0: call list_files.
		{Model: "scripted", ToolCalls: []ToolCall{toolCall("list_files", `{"limit":10}`)}},
		// Step 1: final answer (no tool calls).
		{Model: "scripted", Content: "I listed the files."},
	}}
	agent := NewAgent(env.svc, s, llm, env.repo, nil)
	resp, err := agent.Run(context.Background(), AgentReq{Tenant: testTenant, Query: "what files exist?"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(resp.Steps) != 1 {
		t.Fatalf("expected 1 tool step, got %d", len(resp.Steps))
	}
	if resp.Steps[0].Tool != "list_files" {
		t.Fatalf("step tool: %q", resp.Steps[0].Tool)
	}
	if !strings.Contains(resp.Steps[0].Result, "alpha.txt") || !strings.Contains(resp.Steps[0].Result, "beta.txt") {
		t.Fatalf("list_files result missing keys: %q", resp.Steps[0].Result)
	}
	if resp.Answer != "I listed the files." {
		t.Fatalf("final answer: %q", resp.Answer)
	}
}

func TestAgentRunReadFileTool(t *testing.T) {
	env := newTestEnv(t)
	env.putObject(t, "readme.txt", "text/plain", "hello from the readme body")

	s := NewSearch(env.repo, NewHashEmbedder(64), nil)
	llm := &scriptedLLM{responses: []ChatResponse{
		{Model: "scripted", ToolCalls: []ToolCall{toolCall("read_file", `{"key":"readme.txt"}`)}},
		{Model: "scripted", Content: "read the file"},
	}}
	agent := NewAgent(env.svc, s, llm, env.repo, nil)
	resp, err := agent.Run(context.Background(), AgentReq{Tenant: testTenant, Query: "read readme"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(resp.Steps) != 1 || resp.Steps[0].Tool != "read_file" {
		t.Fatalf("expected one read_file step, got %+v", resp.Steps)
	}
	if !strings.Contains(resp.Steps[0].Result, "hello from the readme body") {
		t.Fatalf("read_file result mismatch: %q", resp.Steps[0].Result)
	}
}

func TestAgentRunReadFileMissingKey(t *testing.T) {
	env := newTestEnv(t)
	s := NewSearch(env.repo, NewHashEmbedder(64), nil)
	llm := &scriptedLLM{responses: []ChatResponse{
		{Model: "scripted", ToolCalls: []ToolCall{toolCall("read_file", `{}`)}},
		{Model: "scripted", Content: "ok"},
	}}
	agent := NewAgent(env.svc, s, llm, env.repo, nil)
	resp, err := agent.Run(context.Background(), AgentReq{Tenant: testTenant, Query: "read"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(resp.Steps[0].Result, "key required") {
		t.Fatalf("expected 'key required' error result, got %q", resp.Steps[0].Result)
	}
}

func TestAgentRunSearchTool(t *testing.T) {
	env := newTestEnv(t)
	emb := NewHashEmbedder(128)
	o := env.putObject(t, "s.txt", "text/plain", "searchable")
	env.seedChunks(t, o, emb, "kubernetes orchestrates containers across nodes")

	// Build a BM25 index because the agent's search uses Mode:"hybrid".
	b := NewBM25()
	if err := b.BuildFromRepo(context.Background(), env.repo, testTenant); err != nil {
		t.Fatalf("build bm25: %v", err)
	}
	s := NewSearch(env.repo, emb, nil).WithBM25(b)

	llm := &scriptedLLM{responses: []ChatResponse{
		{Model: "scripted", ToolCalls: []ToolCall{toolCall("search", `{"query":"kubernetes containers","k":3}`)}},
		{Model: "scripted", Content: "searched"},
	}}
	agent := NewAgent(env.svc, s, llm, env.repo, nil)
	resp, err := agent.Run(context.Background(), AgentReq{Tenant: testTenant, Query: "find k8s info"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(resp.Steps) != 1 || resp.Steps[0].Tool != "search" {
		t.Fatalf("expected one search step, got %+v", resp.Steps)
	}
	if !strings.Contains(resp.Steps[0].Result, "kubernetes") {
		t.Fatalf("search result should contain matched chunk, got %q", resp.Steps[0].Result)
	}
}

func TestAgentRunUnknownTool(t *testing.T) {
	env := newTestEnv(t)
	s := NewSearch(env.repo, NewHashEmbedder(64), nil)
	llm := &scriptedLLM{responses: []ChatResponse{
		{Model: "scripted", ToolCalls: []ToolCall{toolCall("frobnicate", `{}`)}},
		{Model: "scripted", Content: "ok"},
	}}
	agent := NewAgent(env.svc, s, llm, env.repo, nil)
	resp, err := agent.Run(context.Background(), AgentReq{Tenant: testTenant, Query: "x"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(resp.Steps[0].Result, "unknown tool frobnicate") {
		t.Fatalf("expected unknown-tool result, got %q", resp.Steps[0].Result)
	}
}

func TestAgentRunStepBudgetExhausted(t *testing.T) {
	env := newTestEnv(t)
	env.putObject(t, "loop.txt", "text/plain", "x")
	s := NewSearch(env.repo, NewHashEmbedder(64), nil)
	// Always return a tool call -> never terminates naturally -> forced final.
	llm := &scriptedLLM{responses: []ChatResponse{
		{Model: "scripted", ToolCalls: []ToolCall{toolCall("list_files", `{}`)}},
		{Model: "scripted", ToolCalls: []ToolCall{toolCall("list_files", `{}`)}},
		{Model: "scripted", ToolCalls: []ToolCall{toolCall("list_files", `{}`)}},
		{Model: "scripted", ToolCalls: []ToolCall{toolCall("list_files", `{}`)}},
		// 5th call is the forced final answer (no tool calls in scripted default).
		{Model: "scripted", Content: "forced final"},
	}}
	agent := NewAgent(env.svc, s, llm, env.repo, nil)
	resp, err := agent.Run(context.Background(), AgentReq{Tenant: testTenant, Query: "loop"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// MaxSteps=4 tool cycles, each producing one step.
	if len(resp.Steps) != 4 {
		t.Fatalf("expected 4 steps before forced answer, got %d", len(resp.Steps))
	}
	if resp.Answer != "forced final" {
		t.Fatalf("expected forced final answer, got %q", resp.Answer)
	}
}

func TestAgentNilLLMAndEmptyQuery(t *testing.T) {
	env := newTestEnv(t)
	s := NewSearch(env.repo, NewHashEmbedder(64), nil)
	noLLM := NewAgent(env.svc, s, nil, env.repo, nil)
	if _, err := noLLM.Run(context.Background(), AgentReq{Tenant: testTenant, Query: "x"}); err == nil ||
		!strings.Contains(err.Error(), "no LLM configured") {
		t.Fatalf("nil LLM should error, got %v", err)
	}
	agent := NewAgent(env.svc, s, MockLLM{}, env.repo, nil)
	if _, err := agent.Run(context.Background(), AgentReq{Tenant: testTenant, Query: ""}); err == nil {
		t.Fatal("empty query should error")
	}
}

// --- Indexer integration ---

func TestIndexerIndexObjectByID(t *testing.T) {
	env := newTestEnv(t)
	emb := NewHashEmbedder(128)
	// Long enough body to produce multiple chunks (window 600, overlap 80).
	body := strings.Repeat("the quick brown fox jumps over the lazy dog. ", 40)
	obj := env.putObject(t, "big.txt", "text/plain", body)

	ix := NewIndexer(env.repo, env.store, NewDefaultExtractor(), NewChunker(), emb, nil)
	if err := ix.IndexObjectByID(context.Background(), obj.ID); err != nil {
		t.Fatalf("index: %v", err)
	}

	chunks, err := env.repo.ListChunksForObject(context.Background(), obj.ID)
	if err != nil {
		t.Fatalf("list chunks: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for long body, got %d", len(chunks))
	}
	for i, c := range chunks {
		if c.EmbedModel != emb.Name() {
			t.Fatalf("chunk %d embed model: want %q, got %q", i, emb.Name(), c.EmbedModel)
		}
		if c.Dim != emb.Dimensions() || len(c.Embedding) != emb.Dimensions() {
			t.Fatalf("chunk %d dim mismatch: dim=%d len=%d want %d", i, c.Dim, len(c.Embedding), emb.Dimensions())
		}
		if c.Seq != i {
			t.Fatalf("chunk seq out of order: want %d got %d", i, c.Seq)
		}
	}
}

func TestIndexerIdempotent(t *testing.T) {
	env := newTestEnv(t)
	emb := NewHashEmbedder(64)
	obj := env.putObject(t, "idem.txt", "text/plain", "stable content for idempotency test")
	ix := NewIndexer(env.repo, env.store, NewDefaultExtractor(), NewChunker(), emb, nil)

	if err := ix.IndexObjectByID(context.Background(), obj.ID); err != nil {
		t.Fatalf("index 1: %v", err)
	}
	first, _ := env.repo.ListChunksForObject(context.Background(), obj.ID)
	// Re-index: should replace, not duplicate.
	if err := ix.IndexObjectByID(context.Background(), obj.ID); err != nil {
		t.Fatalf("index 2: %v", err)
	}
	second, _ := env.repo.ListChunksForObject(context.Background(), obj.ID)
	if len(first) != len(second) {
		t.Fatalf("re-index changed chunk count: %d -> %d", len(first), len(second))
	}
}

func TestIndexerUnsupportedContentIsNoOp(t *testing.T) {
	env := newTestEnv(t)
	emb := NewHashEmbedder(64)
	obj := env.putObject(t, "image.bin", "application/pdf", "%PDF binary-ish bytes")
	ix := NewIndexer(env.repo, env.store, NewDefaultExtractor(), NewChunker(), emb, nil)

	// Unsupported content type -> no error, no chunks.
	if err := ix.IndexObjectByID(context.Background(), obj.ID); err != nil {
		t.Fatalf("index unsupported: %v", err)
	}
	chunks, _ := env.repo.ListChunksForObject(context.Background(), obj.ID)
	if len(chunks) != 0 {
		t.Fatalf("unsupported content should produce no chunks, got %d", len(chunks))
	}
}

func TestIndexerDeleteObjectChunks(t *testing.T) {
	env := newTestEnv(t)
	emb := NewHashEmbedder(64)
	obj := env.putObject(t, "del.txt", "text/plain", "delete me content")
	ix := NewIndexer(env.repo, env.store, NewDefaultExtractor(), NewChunker(), emb, nil)
	if err := ix.IndexObjectByID(context.Background(), obj.ID); err != nil {
		t.Fatalf("index: %v", err)
	}
	if c, _ := env.repo.ListChunksForObject(context.Background(), obj.ID); len(c) == 0 {
		t.Fatal("expected chunks before delete")
	}
	if err := ix.DeleteObjectChunks(context.Background(), obj.ID); err != nil {
		t.Fatalf("delete chunks: %v", err)
	}
	if c, _ := env.repo.ListChunksForObject(context.Background(), obj.ID); len(c) != 0 {
		t.Fatalf("expected no chunks after delete, got %d", len(c))
	}
}

func TestIndexerWithPIIScanWritesTags(t *testing.T) {
	env := newTestEnv(t)
	emb := NewHashEmbedder(64)
	obj := env.putObject(t, "pii.txt", "text/plain", "reach me at alice@example.com or 123-45-6789")
	ix := NewIndexer(env.repo, env.store, NewDefaultExtractor(), NewChunker(), emb, nil).
		WithPII(NewPIIDetector(), false)

	if err := ix.IndexObjectByID(context.Background(), obj.ID); err != nil {
		t.Fatalf("index: %v", err)
	}
	reloaded, err := env.repo.GetObjectByID(context.Background(), obj.ID)
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	scan := reloaded.Tags["pii_scan"]
	if scan == "" {
		t.Fatalf("expected pii_scan tag, tags=%v", reloaded.Tags)
	}
	if !strings.Contains(scan, "email=1") {
		t.Fatalf("pii_scan should record email=1, got %q", scan)
	}
}

func TestIndexerWithPIIRedactsChunks(t *testing.T) {
	env := newTestEnv(t)
	emb := NewHashEmbedder(64)
	obj := env.putObject(t, "redact.txt", "text/plain", "email secret@corp.com appears here")
	ix := NewIndexer(env.repo, env.store, NewDefaultExtractor(), NewChunker(), emb, nil).
		WithPII(NewPIIDetector(), true) // redact=true

	if err := ix.IndexObjectByID(context.Background(), obj.ID); err != nil {
		t.Fatalf("index: %v", err)
	}
	chunks, _ := env.repo.ListChunksForObject(context.Background(), obj.ID)
	if len(chunks) == 0 {
		t.Fatal("expected chunks")
	}
	for _, c := range chunks {
		if strings.Contains(c.Content, "secret@corp.com") {
			t.Fatalf("redaction failed; chunk still has the email: %q", c.Content)
		}
	}
}

// recordingEnqueuer captures enqueued jobs to verify the event->job bridge.
type recordingEnqueuer struct{ jobs []repository.Job }

func (r *recordingEnqueuer) Enqueue(_ context.Context, j repository.Job) (int64, bool, error) {
	r.jobs = append(r.jobs, j)
	return int64(len(r.jobs)), false, nil
}

func TestIndexerRunBridgesEventsToQueue(t *testing.T) {
	env := newTestEnv(t)
	obj := env.putObject(t, "evt.txt", "text/plain", "event bridged content")

	enq := &recordingEnqueuer{}
	ix := NewIndexer(env.repo, env.store, NewDefaultExtractor(), NewChunker(), NewHashEmbedder(64), nil).
		WithQueue(enq)

	// Feed a created event through a closed channel after one send so Run exits.
	sub := make(chan repository.Event, 1)
	id := obj.ID
	sub <- repository.Event{ID: 9999, TenantID: testTenant, Type: repository.EventCreated, ObjectID: &id}
	close(sub)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ix.Run(ctx, sub) // returns when sub is closed

	if len(enq.jobs) == 0 {
		t.Fatal("expected an enqueued index_object job")
	}
	found := false
	for _, j := range enq.jobs {
		if j.Type == JobIndexObject {
			found = true
			gotID, err := DecodeObjectID(j.Payload)
			if err != nil || gotID != obj.ID {
				t.Fatalf("job payload object id: got %d (err %v), want %d", gotID, err, obj.ID)
			}
		}
	}
	if !found {
		t.Fatalf("no index_object job enqueued; jobs=%+v", enq.jobs)
	}
	// No chunks should be written inline when a queue is configured.
	if c, _ := env.repo.ListChunksForObject(context.Background(), obj.ID); len(c) != 0 {
		t.Fatalf("queue mode should not index inline, got %d chunks", len(c))
	}
}

func TestIndexerRunInlineProcessesEvent(t *testing.T) {
	env := newTestEnv(t)
	obj := env.putObject(t, "inline.txt", "text/plain", "inline processed content here")

	ix := NewIndexer(env.repo, env.store, NewDefaultExtractor(), NewChunker(), NewHashEmbedder(64), nil)
	// No queue -> inline indexing on event.

	sub := make(chan repository.Event, 1)
	id := obj.ID
	sub <- repository.Event{ID: 1, TenantID: testTenant, Type: repository.EventCreated, ObjectID: &id}
	close(sub)
	ix.Run(context.Background(), sub)

	chunks, _ := env.repo.ListChunksForObject(context.Background(), obj.ID)
	if len(chunks) == 0 {
		t.Fatal("inline indexer should have written chunks")
	}
}

// sanity: ensure io import used (read_file uses io in agent.go path indirectly).
var _ = io.Discard
