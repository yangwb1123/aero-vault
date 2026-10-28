package ai

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

type scriptedChatAuth struct {
	calls    int
	denyCall int
	revoked  bool
}

func (a *scriptedChatAuth) CanReadObject(context.Context, string, string, string) error {
	a.calls++
	if a.revoked || (a.denyCall > 0 && a.calls >= a.denyCall) {
		return service.ErrForbidden
	}
	return nil
}

type hookLLM struct {
	chatCalls   int
	streamCalls int
	afterChat   func()
	stream      func(func(string))
}

func (l *hookLLM) Name() string { return "hook" }
func (l *hookLLM) Chat(context.Context, ChatRequest) (ChatResponse, error) {
	l.chatCalls++
	if l.afterChat != nil {
		l.afterChat()
	}
	return ChatResponse{Content: "hook answer", Model: "hook", Usage: map[string]int{"prompt_tokens": 2, "completion_tokens": 3}}, nil
}
func (l *hookLLM) ChatStream(_ context.Context, _ ChatRequest, onChunk func(string)) (ChatResponse, error) {
	l.streamCalls++
	if l.stream != nil {
		l.stream(onChunk)
	}
	return ChatResponse{Content: "hook stream", Model: "hook", Usage: map[string]int{"prompt_tokens": 2, "completion_tokens": 3}}, nil
}

func chatResourceEnv(t *testing.T) (*testEnv, *Search, *scriptedChatAuth, repository.Object) {
	t.Helper()
	env := newTestEnv(t)
	emb := NewHashEmbedder(128)
	obj := env.putObject(t, "chat.txt", "text/plain", "chat")
	env.seedChunks(t, obj, emb, "the capital of france is paris")
	auth := &scriptedChatAuth{}
	reader := newSearchBoundaryReader(testTenant, testBucket)
	search := NewSearch(env.repo, emb, nil).
		WithResourceBoundary(reader, reader).
		WithHitAuthorizer(auth)
	return env, search, auth, obj
}

func hasModelUsage(t *testing.T, env *testEnv, obj repository.Object) bool {
	t.Helper()
	usages, err := env.repo.ListUsageForObject(context.Background(), testTenant, obj.ID, 20)
	if err != nil {
		t.Fatalf("list usage: %v", err)
	}
	for _, usage := range usages {
		if usage.Model == "hook" {
			return true
		}
	}
	return false
}

func TestChatHealthyCitationsAndUsage(t *testing.T) {
	env, search, _, obj := chatResourceEnv(t)
	chat := NewChat(search, &hookLLM{}, env.repo, nil)
	resp, err := chat.Answer(context.Background(), ChatReq{Tenant: testTenant, Query: "capital paris", K: 2, Mode: "vector"})
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if len(resp.Citations) == 0 || resp.Model != "hook" {
		t.Fatalf("healthy response = %+v", resp)
	}
	if !hasModelUsage(t, env, obj) {
		t.Fatal("healthy answer did not record model usage")
	}
}

func TestChatPreLLMRevocationStopsModel(t *testing.T) {
	env, search, auth, obj := chatResourceEnv(t)
	auth.denyCall = 2
	llm := &hookLLM{}
	chat := NewChat(search, llm, env.repo, nil)
	_, err := chat.Answer(context.Background(), ChatReq{Tenant: testTenant, Query: "capital paris", K: 2, Mode: "vector"})
	if !errors.Is(err, ErrChatResourceRevoked) {
		t.Fatalf("pre-LLM revocation = %v, want ErrChatResourceRevoked", err)
	}
	if llm.chatCalls != 0 {
		t.Fatalf("model called %d times after revocation", llm.chatCalls)
	}
	if hasModelUsage(t, env, obj) {
		t.Fatal("revoked answer recorded model usage")
	}
}

func TestChatFinalOutputRevocationStopsUsage(t *testing.T) {
	env, search, auth, obj := chatResourceEnv(t)
	llm := &hookLLM{afterChat: func() { auth.revoked = true }}
	chat := NewChat(search, llm, env.repo, nil)
	_, err := chat.Answer(context.Background(), ChatReq{Tenant: testTenant, Query: "capital paris", K: 2, Mode: "vector"})
	if !errors.Is(err, ErrChatResourceRevoked) {
		t.Fatalf("final revocation = %v, want ErrChatResourceRevoked", err)
	}
	if hasModelUsage(t, env, obj) {
		t.Fatal("final revocation recorded model usage")
	}
}

func TestChatStreamStopsUnsafeCallbacks(t *testing.T) {
	env, search, auth, obj := chatResourceEnv(t)
	llm := &hookLLM{stream: func(onChunk func(string)) {
		onChunk("safe ")
		auth.revoked = true
		onChunk("unsafe")
	}}
	chat := NewChat(search, llm, env.repo, nil)
	var streamed strings.Builder
	_, err := chat.AnswerStream(context.Background(), ChatReq{Tenant: testTenant, Query: "capital paris", K: 2, Mode: "vector"},
		func(chunk string) { streamed.WriteString(chunk) })
	if !errors.Is(err, ErrChatResourceRevoked) {
		t.Fatalf("stream revocation = %v, want ErrChatResourceRevoked", err)
	}
	if streamed.String() != "safe " {
		t.Fatalf("streamed %q, want only the pre-revocation callback", streamed.String())
	}
	if hasModelUsage(t, env, obj) {
		t.Fatal("failed stream recorded model usage")
	}
}

type guardedTestLLM struct {
	MockLLM
	chat   func(context.Context, ChatRequest) (ChatResponse, error)
	stream func(context.Context, ChatRequest, func(string)) (ChatResponse, error)
}

func (l *guardedTestLLM) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	return l.chat(ctx, req)
}
func (l *guardedTestLLM) ChatStream(ctx context.Context, req ChatRequest, onChunk func(string)) (ChatResponse, error) {
	return l.stream(ctx, req, onChunk)
}

type chatBudgetCountingRepo struct {
	repository.Repository
	quotaCalls int
}

func (r *chatBudgetCountingRepo) GetTenantQuota(context.Context, string) (repository.TenantQuota, error) {
	r.quotaCalls++
	return repository.TenantQuota{}, nil
}

func TestChatPreCancelStopsBudgetAndModelWork(t *testing.T) {
	for _, stream := range []bool{false, true} {
		env, search, _, _ := chatResourceEnv(t)
		repo := &chatBudgetCountingRepo{Repository: env.repo}
		llm := &hookLLM{}
		chat := NewChat(search, llm, repo, nil).WithPerTenantBudgets()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		req := ChatReq{Tenant: testTenant, Query: "capital paris", K: 2, Mode: "vector"}
		var err error
		if stream {
			_, err = chat.AnswerStream(ctx, req, func(string) { t.Error("cancelled callback") })
		} else {
			_, err = chat.Answer(ctx, req)
		}
		if err != ctx.Err() || repo.quotaCalls != 0 || llm.chatCalls != 0 || llm.streamCalls != 0 {
			t.Fatalf("pre-cancel stream=%v err=%v quota=%d chat=%d stream=%d", stream, err, repo.quotaCalls, llm.chatCalls, llm.streamCalls)
		}
	}
}

func TestChatModelErrorCannotHideCancellation(t *testing.T) {
	env, search, _, obj := chatResourceEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := errors.New("noncooperative provider")
	llm := &guardedTestLLM{chat: func(context.Context, ChatRequest) (ChatResponse, error) {
		cancel()
		return ChatResponse{}, provider
	}}
	_, err := NewChat(search, llm, env.repo, nil).Answer(ctx, ChatReq{Tenant: testTenant, Query: "capital paris", K: 2})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, provider) {
		t.Fatalf("model error hid cancellation/cause: %v", err)
	}
	if hasModelUsage(t, env, obj) {
		t.Fatal("cancelled model recorded usage")
	}
}

func TestChatStreamStickyFaultCancelsChildAndSuppressesCallbacks(t *testing.T) {
	env, search, _, obj := chatResourceEnv(t)
	auth := &mutableHitAuthorizer{allow: map[string]bool{obj.Key: true}}
	search.WithHitAuthorizer(auth)
	cause := errors.New("authoritative reader unavailable")
	fault := errors.Join(access.ErrResourceBoundaryFault, service.ErrNotFound, access.ErrDenied, cause)
	llm := &guardedTestLLM{stream: func(ctx context.Context, _ ChatRequest, callback func(string)) (ChatResponse, error) {
		callback("safe")
		auth.err = fault
		callback("blocked")
		if ctx.Err() != context.Canceled {
			t.Errorf("guard did not cancel child context: %v", ctx.Err())
		}
		if !errors.Is(context.Cause(ctx), cause) {
			t.Errorf("model child lost guard cause: %v", context.Cause(ctx))
		}
		auth.err = nil
		callback("later")
		return ChatResponse{Content: "unsafe final", Model: "hook"}, errors.New("later model error")
	}}
	var text strings.Builder
	resp, err := NewChat(search, llm, env.repo, nil).AnswerStream(context.Background(),
		ChatReq{Tenant: testTenant, Query: "capital paris", K: 2}, func(s string) { text.WriteString(s) })
	if !errors.Is(err, cause) || !IsSearchResourceBoundaryFault(err) || resp.Answer != "" || text.String() != "safe" {
		t.Fatalf("sticky stream resp=%+v err=%v text=%q", resp, err, text.String())
	}
	if hasModelUsage(t, env, obj) {
		t.Fatal("failed guarded stream recorded model usage")
	}
}

func TestChatAnswerGuardFailureCancelsOnlyModelChild(t *testing.T) {
	env, search, auth, obj := chatResourceEnv(t)
	parent := context.Background()
	var modelCtx context.Context
	llm := &guardedTestLLM{chat: func(ctx context.Context, _ ChatRequest) (ChatResponse, error) {
		modelCtx = ctx
		auth.revoked = true
		return ChatResponse{Content: "revoked answer", Model: "hook"}, nil
	}}
	resp, err := NewChat(search, llm, env.repo, nil).Answer(parent, ChatReq{Tenant: testTenant, Query: "capital paris", K: 2})
	if !errors.Is(err, ErrChatResourceRevoked) || resp.Answer != "" {
		t.Fatalf("guard failure resp=%+v err=%v", resp, err)
	}
	if modelCtx.Err() != context.Canceled || parent.Err() != nil {
		t.Fatalf("model child=%v parent=%v, want only child cancelled", modelCtx.Err(), parent.Err())
	}
	if !errors.Is(context.Cause(modelCtx), ErrChatResourceRevoked) {
		t.Fatalf("model child lost revocation cause: %v", context.Cause(modelCtx))
	}
	if hasModelUsage(t, env, obj) {
		t.Fatal("revoked model answer recorded usage")
	}
}

func TestChatStreamConcurrentCallbacksAreSerialized(t *testing.T) {
	env, search, _, obj := chatResourceEnv(t)
	llm := &guardedTestLLM{stream: func(_ context.Context, _ ChatRequest, callback func(string)) (ChatResponse, error) {
		var callbacks sync.WaitGroup
		for i := 0; i < 8; i++ {
			callbacks.Go(func() { callback("piece ") })
		}
		callbacks.Wait()
		return ChatResponse{Content: strings.Repeat("piece ", 8), Model: "hook"}, nil
	}}
	var text strings.Builder
	resp, err := NewChat(search, llm, env.repo, nil).AnswerStream(context.Background(),
		ChatReq{Tenant: testTenant, Query: "capital paris", K: 2}, func(chunk string) { text.WriteString(chunk) })
	if err != nil || text.String() != strings.Repeat("piece ", 8) || resp.Answer != text.String() {
		t.Fatalf("serialized callbacks resp=%+v text=%q err=%v", resp, text.String(), err)
	}
	if !hasModelUsage(t, env, obj) {
		t.Fatal("healthy concurrent stream did not record model usage")
	}
}
