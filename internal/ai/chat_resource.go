package ai

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/telemetry"
)

// ErrChatResourceRevoked marks a prompt whose canonical resource set changed
// or whose authorization was withdrawn after retrieval. It is intentionally a
// hard error: the prompt may already contain the revoked content.
var ErrChatResourceRevoked = errors.New("chat resource authorization changed")

// chatResourceGuard re-observes the exact retrieved hits before each model
// call, before each streamed callback, and before final citations/usage are
// emitted. A fault or ordinary revocation cancels the child model context and
// suppresses later callbacks; it never becomes a healthy empty answer.
type chatResourceGuard struct {
	search  *Search
	req     ChatReq
	hits    []Hit
	mu      sync.Mutex // serializes callbacks with final delivery and the first fault
	invalid error
	closed  bool
	cancel  context.CancelCauseFunc
}

func newChatResourceGuard(search *Search, req ChatReq, hits []Hit) *chatResourceGuard {
	return &chatResourceGuard{search: search, req: req, hits: cloneHits(hits)}
}

func (g *chatResourceGuard) refresh(ctx context.Context) ([]Hit, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.refreshLocked(ctx)
}

func (g *chatResourceGuard) refreshLocked(ctx context.Context) ([]Hit, error) {
	if g.invalid != nil {
		return nil, g.invalid
	}
	if err := searchContextError(ctx); err != nil {
		g.failLocked(err)
		return nil, err
	}
	if g.search == nil {
		return cloneHits(g.hits), nil
	}
	hits, denied, err := g.search.revalidateHits(ctx, Request{
		Tenant: g.req.Tenant, Bucket: g.req.Bucket,
	}, g.hits)
	if err != nil {
		g.failLocked(err)
		return nil, err
	}
	if denied {
		g.failLocked(ErrChatResourceRevoked)
		return nil, ErrChatResourceRevoked
	}
	return hits, nil
}

func (g *chatResourceGuard) failLocked(err error) {
	if g.invalid == nil {
		g.invalid = err
	}
	if g.cancel != nil {
		g.cancel(g.invalid)
	}
}

func (g *chatResourceGuard) modelContext(ctx context.Context) (context.Context, context.CancelFunc) {
	child, cancel := context.WithCancelCause(ctx)
	g.mu.Lock()
	g.cancel = cancel
	g.mu.Unlock()
	return child, func() { cancel(nil) }
}

func (g *chatResourceGuard) wrap(ctx context.Context, onChunk func(string)) func(string) {
	return func(chunk string) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.closed || g.invalid != nil {
			return
		}
		if _, err := g.refreshLocked(ctx); err != nil {
			return
		}
		if onChunk != nil {
			onChunk(chunk)
		}
		if err := ctx.Err(); err != nil {
			g.failLocked(err)
		}
	}
}

// finish closes callback delivery even when a noncooperative provider invokes
// a callback after returning. The first observed guard failure wins over later
// provider errors and the child cancellation caused by that failure.
func (g *chatResourceGuard) finish(ctx context.Context, modelErr error) ([]Hit, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	if g.invalid != nil {
		return nil, g.invalid
	}
	if modelErr != nil {
		g.failLocked(errors.Join(modelErr, ctx.Err()))
		return nil, g.invalid
	}
	return g.refreshLocked(ctx)
}

func (c *Chat) recordChatUsage(
	ctx context.Context,
	req ChatReq,
	resp ChatResponse,
	hits []Hit,
	latency time.Duration,
	stream bool,
) {
	chunkIDs := make([]int64, 0, len(hits))
	objIDs := make([]int64, 0, len(hits))
	seen := map[int64]struct{}{}
	for _, hit := range hits {
		chunkIDs = append(chunkIDs, hit.ChunkID)
		if _, ok := seen[hit.ObjectID]; !ok {
			seen[hit.ObjectID] = struct{}{}
			objIDs = append(objIDs, hit.ObjectID)
		}
	}
	pt, ct, tt := tokensFromUsage(resp.Usage)
	cost := costMicros(pt, ct, c.promptMicrosPer1K, c.completionMicrosPer1K)
	if err := c.repo.RecordUsage(ctx, repository.Usage{
		TenantID: req.Tenant, Caller: req.Caller, Query: req.Query,
		ChunkIDs: chunkIDs, ObjectIDs: objIDs, RequestID: req.ReqID,
		Model: resp.Model, PromptTokens: pt, CompletionTokens: ct, TotalTokens: tt,
		LatencyMs: latency.Milliseconds(), CostMicros: cost,
	}); err != nil {
		if stream {
			c.logger.Warn("audit chat-stream usage", "err", err)
		} else {
			c.logger.Warn("audit chat usage", "err", err)
		}
	}
	telemetry.RecordAIUsage(ctx, req.Tenant, resp.Model, pt, ct, cost)
}
