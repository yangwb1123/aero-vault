package ai

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

type hookAgentLLM struct {
	responses []ChatResponse
	calls     int
	afterCall func(int)
}

func (l *hookAgentLLM) Name() string { return "hook-agent" }
func (l *hookAgentLLM) Chat(_ context.Context, _ ChatRequest) (ChatResponse, error) {
	l.calls++
	if l.afterCall != nil {
		l.afterCall(l.calls)
	}
	index := l.calls - 1
	if index >= len(l.responses) {
		return ChatResponse{Content: "done", Model: "hook-agent"}, nil
	}
	return l.responses[index], nil
}
func (l *hookAgentLLM) ChatStream(ctx context.Context, req ChatRequest, _ func(string)) (ChatResponse, error) {
	return l.Chat(ctx, req)
}

func TestAgentAccumulatedHistoryStopsNextModelCall(t *testing.T) {
	env, emb, obj, _ := seedBoundarySearchEnv(t)
	reader := newSearchBoundaryReader(testTenant, testBucket)
	search := NewSearch(env.repo, emb, nil).WithResourceBoundary(reader, reader)
	llm := &hookAgentLLM{
		responses: []ChatResponse{
			{Model: "hook-agent", ToolCalls: []ToolCall{toolCall("list_files", `{"limit":10}`)}},
		},
		afterCall: func(call int) {
			if call == 1 {
				reader.setBound()
			}
		},
	}
	agent := NewAgent(env.svc, search, llm, env.repo, nil)
	_, err := agent.Run(context.Background(), AgentReq{Tenant: testTenant, Query: "list"})
	if !IsSearchResourceBoundaryDenied(err) {
		t.Fatalf("accumulated list ref = %v, want explicit boundary denial", err)
	}
	if llm.calls != 1 {
		t.Fatalf("model calls = %d, want 1", llm.calls)
	}
	_ = obj
}

func TestAgentReadHistoryRevocationBlocksFinalOutput(t *testing.T) {
	env, emb, obj, _ := seedBoundarySearchEnv(t)
	reader := newSearchBoundaryReader(testTenant, testBucket)
	search := NewSearch(env.repo, emb, nil).WithResourceBoundary(reader, reader)
	llm := &hookAgentLLM{
		responses: []ChatResponse{
			{Model: "hook-agent", ToolCalls: []ToolCall{toolCall("read_file", `{"key":"boundary.txt"}`)}},
			{Model: "hook-agent", Content: "final answer"},
		},
		afterCall: func(call int) {
			if call == 2 {
				_ = env.repo.SoftDeleteObject(context.Background(), testTenant, testBucket, obj.Key)
			}
		},
	}
	agent := NewAgent(env.svc, search, llm, env.repo, nil)
	resp, err := agent.Run(context.Background(), AgentReq{Tenant: testTenant, Query: "read"})
	if err == nil || resp.Answer != "" {
		t.Fatalf("revoked read history returned answer=%q err=%v", resp.Answer, err)
	}
	if !errors.Is(err, repository.ErrNotFound) {
		t.Logf("final guard error: %v", err)
	}
}

func TestAgentForcedFinalRevalidatesSearchRefs(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	emb := NewHashEmbedder(128)
	obj := env.putObject(t, "agent-search.txt", "text/plain", "agent")
	env.seedChunks(t, obj, emb, "kubernetes orchestrates containers")
	bm := NewBM25()
	if err := bm.BuildFromRepo(ctx, env.repo, testTenant); err != nil {
		t.Fatalf("build bm25: %v", err)
	}
	reader := newSearchBoundaryReader(testTenant, testBucket)
	search := NewSearch(env.repo, emb, nil).WithResourceBoundary(reader, reader).WithBM25(bm)
	llm := &hookAgentLLM{
		responses: []ChatResponse{
			{Model: "hook-agent", ToolCalls: []ToolCall{toolCall("search", `{"query":"kubernetes containers","k":3}`)}},
		},
		afterCall: func(call int) {
			if call == 2 {
				_ = env.repo.SoftDeleteObject(context.Background(), testTenant, testBucket, obj.Key)
			}
		},
	}
	agent := NewAgent(env.svc, search, llm, env.repo, nil)
	agent.MaxSteps = 1
	resp, err := agent.Run(ctx, AgentReq{Tenant: testTenant, Query: "find containers"})
	if err == nil || resp.Answer != "" {
		t.Fatalf("forced final after revocation returned answer=%q err=%v", resp.Answer, err)
	}
}

func TestAgentEmptyListKeepsBucketScope(t *testing.T) {
	env := newTestEnv(t)
	if _, err := env.svc.Put(context.Background(), testTenant, testBucket, "seed.txt",
		strings.NewReader("seed"), 4, service.PutOptions{}); err != nil {
		t.Fatalf("seed bucket: %v", err)
	}
	if err := env.repo.HardDeleteObject(context.Background(), testTenant, testBucket, "seed.txt"); err != nil {
		t.Fatalf("clear seed object: %v", err)
	}
	reader := newSearchBoundaryReader(testTenant, testBucket)
	search := NewSearch(env.repo, NewHashEmbedder(64), nil).WithResourceBoundary(reader, reader)
	llm := &hookAgentLLM{
		responses: []ChatResponse{
			{Model: "hook-agent", ToolCalls: []ToolCall{toolCall("list_files", `{"limit":10}`)}},
		},
		afterCall: func(call int) {
			if call == 1 {
				reader.setBound()
			}
		},
	}
	agent := NewAgent(env.svc, search, llm, env.repo, nil)
	_, err := agent.Run(context.Background(), AgentReq{Tenant: testTenant, Query: "list"})
	if !IsSearchResourceBoundaryDenied(err) {
		t.Fatalf("empty list bucket scope = %v, want boundary denial", err)
	}
}

func TestAgentRetainsEmptySearchScopeBeforeForcedFinal(t *testing.T) {
	env, emb, _, _ := seedBoundarySearchEnv(t)
	reader := newSearchBoundaryReader(testTenant, testBucket)
	reader.afterCoverage = func() {
		if reader.coverageCalls == 2 {
			reader.setCoverageBound()
		}
	}
	search := NewSearch(env.repo, emb, nil).WithResourceBoundary(reader, reader).
		WithVectorIndex(&fakeVectorIndex{}).WithBM25(NewBM25())
	llm := &hookAgentLLM{responses: []ChatResponse{{ToolCalls: []ToolCall{toolCall("search", `{"query":"absent"}`)}}}}
	agent := NewAgent(env.svc, search, llm, env.repo, nil)
	agent.MaxSteps = 1
	resp, err := agent.Run(context.Background(), AgentReq{Tenant: testTenant, Query: "find absent"})
	if !IsSearchResourceBoundaryDenied(err) || llm.calls != 1 || resp.Answer != "" {
		t.Fatalf("empty scope forgotten: resp=%+v err=%v model_calls=%d", resp, err, llm.calls)
	}
}

func TestAgentDoesNotOverwriteHistoricalChunkObservation(t *testing.T) {
	env, emb, obj, rows := seedBoundarySearchEnv(t)
	repo := &canonicalFaultRepo{Repository: env.repo, rows: append([]repository.Chunk(nil), rows...), override: true}
	search := boundarySearch(t, repo, emb)
	guard := newAgentResourceGuard(env.svc, search, repo, testTenant)
	old := hitsFromRanked([]ranked{{chunkID: rows[0].ID, chunk: rows[0]}})
	guard.addHits(old)
	repo.rows[0].Content = "new canonical content with the same chunk ID"
	current := hitsFromRanked([]ranked{{chunkID: rows[0].ID, chunk: repo.rows[0]}})
	guard.addHits(current)
	if err := guard.validate(context.Background()); !errors.Is(err, ErrCanonicalHit) {
		t.Fatalf("old prompt content forgotten for object %d: %v", obj.ID, err)
	}
}

func TestAgentDoesNotOverwriteHistoricalObjectObservation(t *testing.T) {
	env, emb, obj, _ := seedBoundarySearchEnv(t)
	guard := newAgentResourceGuard(env.svc, boundarySearch(t, env.repo, emb), env.repo, testTenant)
	old := obj
	old.Key = "previous-observed-key.txt"
	guard.addObject(old)
	guard.addObject(obj)
	if err := guard.validate(context.Background()); err == nil {
		t.Fatal("a later same-ID observation erased the earlier resource scope")
	}
}

func TestAgentDoesNotSynthesizeListRowScope(t *testing.T) {
	env, emb, obj, _ := seedBoundarySearchEnv(t)
	guard := newAgentResourceGuard(env.svc, boundarySearch(t, env.repo, emb), env.repo, testTenant)
	foreign := obj
	foreign.TenantID, foreign.Bucket = "other", "private"
	guard.addList(testTenant, testBucket, repository.ListPage{Objects: []repository.Object{foreign}})
	if err := guard.validate(context.Background()); err == nil {
		t.Fatal("list observation scope was reconstructed from request instead of the actual row")
	}
}

func TestAgentBoundaryToolDenialNeverBecomesModelText(t *testing.T) {
	env, emb, _, _ := seedBoundarySearchEnv(t)
	reader := newSearchBoundaryReader(testTenant, testBucket)
	reader.setBound()
	env.svc.WithSpaceBoundary(service.NewSpaceBoundary(service.SpaceBoundaryReaders{Binding: reader, Coverage: reader, Resources: env.repo}))
	llm := &hookAgentLLM{responses: []ChatResponse{{ToolCalls: []ToolCall{toolCall("list_files", `{}`)}}}}
	agent := NewAgent(env.svc, NewSearch(env.repo, emb, nil), llm, env.repo, nil)
	resp, err := agent.Run(context.Background(), AgentReq{Tenant: testTenant, Query: "list"})
	if !IsSearchResourceBoundaryDenied(err) || llm.calls != 1 || resp.Answer != "" {
		t.Fatalf("boundary denial became model tool text: resp=%+v err=%v calls=%d", resp, err, llm.calls)
	}
}

func TestAgentOnlyOrdinaryToolErrorsAreText(t *testing.T) {
	for _, err := range []error{
		errors.Join(service.ErrForbidden, access.ErrSpaceBound, access.ErrDenied),
		errors.Join(service.ErrForbidden, service.ErrTenantDisabled),
		errors.Join(service.ErrForbidden, spaces.ErrTenantUnavailable),
		errors.Join(service.ErrNotFound, access.ErrResourceBoundaryFault),
		errors.Join(service.ErrNotFound, ErrCanonicalHit),
		errors.Join(service.ErrForbidden, context.Canceled),
	} {
		if ordinaryAgentToolError(err) {
			t.Errorf("security/cancellation error treated as ordinary tool text: %v", err)
		}
	}
	if !ordinaryAgentToolError(service.ErrNotFound) || !ordinaryAgentToolError(service.ErrInvalidArgs) {
		t.Fatal("ordinary tool-error compatibility lost")
	}
}

func TestAgentSameIDOverwriteCannotForgetEarlierReadBody(t *testing.T) {
	env, emb, obj, _ := seedBoundarySearchEnv(t)
	search := boundarySearch(t, env.repo, emb)
	llm := &hookAgentLLM{responses: []ChatResponse{
		{Content: "first assistant observation", ToolCalls: []ToolCall{toolCall("read_file", `{"key":"boundary.txt"}`)}},
		{Content: "second assistant observation", ToolCalls: []ToolCall{toolCall("read_file", `{"key":"boundary.txt"}`)}},
	}}
	llm.afterCall = func(call int) {
		if call == 2 {
			replacement := env.putObject(t, obj.Key, "text/plain", "replacement body")
			if replacement.ID != obj.ID {
				t.Fatalf("same-ID overwrite fixture changed identity: %d/%d", obj.ID, replacement.ID)
			}
		}
	}
	agent := NewAgent(env.svc, search, llm, env.repo, nil)
	agent.MaxSteps = 2
	resp, err := agent.Run(context.Background(), AgentReq{Tenant: testTenant, Query: "read twice"})
	if !errors.Is(err, ErrAgentResourceChanged) || llm.calls != 2 || resp.Answer != "" {
		t.Fatalf("earlier read body forgotten: resp=%+v err=%v calls=%d", resp, err, llm.calls)
	}
}

func TestAgentToolProviderFaultPreservesCauseWithoutAnotherModelCall(t *testing.T) {
	env, emb, _, _ := seedBoundarySearchEnv(t)
	reader := newSearchBoundaryReader(testTenant, testBucket)
	cause := errors.New("provider unavailable")
	reader.bindingErr = errors.Join(cause, service.ErrNotFound, access.ErrDenied)
	env.svc.WithSpaceBoundary(service.NewSpaceBoundary(service.SpaceBoundaryReaders{Binding: reader, Coverage: reader, Resources: env.repo}))
	llm := &hookAgentLLM{responses: []ChatResponse{{ToolCalls: []ToolCall{toolCall("list_files", `{}`)}}}}
	agent := NewAgent(env.svc, NewSearch(env.repo, emb, nil), llm, env.repo, nil)
	resp, err := agent.Run(context.Background(), AgentReq{Tenant: testTenant, Query: "list"})
	if !IsSearchResourceBoundaryFault(err) || !errors.Is(err, cause) || llm.calls != 1 || resp.Answer != "" {
		t.Fatalf("tool provider fault escaped: resp=%+v err=%v calls=%d", resp, err, llm.calls)
	}
}

func TestAgentAssistantHistoryRetainsContentAndToolCalls(t *testing.T) {
	message := assistantToolMessage(ChatResponse{Content: "retained assistant observation",
		ToolCalls: []ToolCall{toolCall("read_file", `{"key":"boundary.txt"}`)}})
	if !strings.Contains(message.Content, "retained assistant observation") || !strings.Contains(message.Content, "read_file") {
		t.Fatalf("assistant history was overwritten: %+v", message)
	}
}
