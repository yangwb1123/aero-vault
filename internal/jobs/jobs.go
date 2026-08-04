// Package jobs is a small, durable background job queue backed by the
// repository's `jobs` table. A Registry maps job types to Handlers; a Pool runs
// a fixed set of workers that claim runnable jobs, execute the matching
// handler, and on failure reschedule them with exponential backoff until
// MaxAttempts is exhausted. A reaper requeues jobs orphaned by a crashed
// worker.
//
// It is deliberately transport-agnostic: enqueue from anywhere (event bridge,
// HTTP handler, cron) and any process running a Pool with the right handlers
// registered will execute the work.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"runtime/debug"
	"sync"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/telemetry"
)

// Handler executes one job. Returning an error triggers a retry with backoff
// until the job's MaxAttempts is reached, after which it is marked failed.
type Handler func(ctx context.Context, job repository.Job) error

// Registry maps job types to handlers. Safe for concurrent use.
type Registry struct {
	mu       sync.RWMutex
	handlers map[string]Handler
}

func NewRegistry() *Registry { return &Registry{handlers: map[string]Handler{}} }

// Register binds a handler to a job type. Registering twice overwrites.
func (r *Registry) Register(jobType string, h Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[jobType] = h
}

func (r *Registry) lookup(jobType string) (Handler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.handlers[jobType]
	return h, ok
}

// Types lists the registered job types.
func (r *Registry) Types() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.handlers))
	for t := range r.handlers {
		out = append(out, t)
	}
	return out
}

// ErrQueueFull is returned by Enqueue when the pending-job count has reached the
// configured depth cap (backpressure). Callers may surface it as 429.
var ErrQueueFull = errors.New("job queue full")

// Queue is the enqueue side — a thin, dependency-light wrapper so callers don't
// need the whole repository surface.
type Queue struct {
	mu       sync.Mutex
	repo     repository.Repository
	maxDepth int // 0 = unbounded
}

func NewQueue(repo repository.Repository) *Queue { return &Queue{repo: repo} }

// WithMaxDepth caps the number of pending jobs; Enqueue returns ErrQueueFull
// once the cap is reached. Zero (default) is unbounded.
func (q *Queue) WithMaxDepth(n int) *Queue {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.maxDepth = n
	return q
}

// Enqueue adds a job. Returns (id, deduped). When the job carried a DedupeKey
// matching a live job, deduped is true and id is the existing job. When a depth
// cap is set and the pending backlog has reached it, returns ErrQueueFull.
func (q *Queue) Enqueue(ctx context.Context, j repository.Job) (int64, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.maxDepth > 0 {
		n, err := q.repo.CountJobsByStatus(ctx, "pending")
		if err != nil {
			return 0, false, fmt.Errorf("count pending jobs: %w", err)
		}
		if n >= q.maxDepth {
			return 0, false, ErrQueueFull
		}
	}
	return q.repo.EnqueueJob(ctx, j)
}

// Pool executes jobs with a fixed number of workers.
type Pool struct {
	repo   repository.Repository
	reg    *Registry
	logger *slog.Logger

	workers     int
	pollEvery   time.Duration
	baseBackoff time.Duration
	maxBackoff  time.Duration
	reapEvery   time.Duration
	reapAfter   time.Duration
}

// NewPool builds a pool. workers<=0 defaults to 4.
func NewPool(repo repository.Repository, reg *Registry, workers int, logger *slog.Logger) *Pool {
	if logger == nil {
		logger = slog.Default()
	}
	if workers <= 0 {
		workers = 4
	}
	return &Pool{
		repo:        repo,
		reg:         reg,
		logger:      logger,
		workers:     workers,
		pollEvery:   time.Second,
		baseBackoff: time.Second,
		maxBackoff:  5 * time.Minute,
		reapEvery:   time.Minute,
		reapAfter:   10 * time.Minute,
	}
}

// Run starts the workers and a reaper, blocking until ctx is canceled.
func (p *Pool) Run(ctx context.Context) {
	p.logger.Info("job pool starting", "workers", p.workers, "types", p.reg.Types())
	var wg sync.WaitGroup
	for i := 0; i < p.workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			p.worker(ctx, fmt.Sprintf("w%d", id))
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.reaper(ctx)
	}()
	wg.Wait()
}

func (p *Pool) worker(ctx context.Context, name string) {
	// Stagger initial polls so workers don't all wake together.
	idle := time.NewTimer(time.Duration(rand.Int63n(int64(p.pollEvery) + 1)))
	defer idle.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-idle.C:
		}
		// Drain greedily while there's work, then back off to polling.
		for {
			worked, err := p.runOne(ctx, name)
			if err != nil && ctx.Err() == nil {
				p.logger.Warn("job claim failed", "worker", name, "err", err)
			}
			if !worked || ctx.Err() != nil {
				break
			}
		}
		idle.Reset(p.pollEvery + time.Duration(rand.Int63n(int64(p.pollEvery)+1)))
	}
}

// runOne claims and executes a single job. worked=false means the queue was
// empty (or the claim was lost to another worker).
func (p *Pool) runOne(ctx context.Context, worker string) (worked bool, err error) {
	job, ok, err := p.repo.ClaimJob(ctx, worker)
	if err != nil || !ok {
		return false, err
	}
	h, ok := p.reg.lookup(job.Type)
	if !ok {
		p.logger.Error("no handler for job type", "type", job.Type, "id", job.ID)
		_ = p.repo.FailJob(ctx, job.ID, "no handler registered for type "+job.Type)
		telemetry.IncJobFailed(ctx, job.Type)
		return true, nil
	}
	runErr := p.execute(ctx, h, job)
	if runErr == nil {
		if e := p.repo.CompleteJob(ctx, job.ID, ""); e != nil {
			p.logger.Warn("complete job", "id", job.ID, "err", e)
		}
		p.logger.Info("job done", "id", job.ID, "type", job.Type, "attempts", job.Attempts)
		telemetry.IncJobCompleted(ctx, job.Type)
		return true, nil
	}
	// Failed: retry with backoff or give up.
	if job.Attempts >= job.MaxAttempts {
		p.logger.Error("job failed permanently", "id", job.ID, "type", job.Type, "attempts", job.Attempts, "err", runErr)
		_ = p.repo.FailJob(ctx, job.ID, runErr.Error())
		telemetry.IncJobFailed(ctx, job.Type)
		return true, nil
	}
	delay := p.backoff(job.Attempts)
	p.logger.Warn("job failed, retrying", "id", job.ID, "type", job.Type, "attempt", job.Attempts, "retry_in", delay.String(), "err", runErr)
	_ = p.repo.RetryJob(ctx, job.ID, runErr.Error(), time.Now().Add(delay))
	telemetry.IncJobRetried(ctx, job.Type)
	return true, nil
}

// execute runs the handler with panic recovery so one bad job can't take down
// a worker goroutine.
func (p *Pool) execute(ctx context.Context, h Handler, job repository.Job) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
		}
	}()
	return h(ctx, job)
}

func (p *Pool) reaper(ctx context.Context) {
	t := time.NewTicker(p.reapEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := p.repo.ReapStuckJobs(ctx, p.reapAfter)
			if err != nil {
				p.logger.Warn("reap stuck jobs", "err", err)
				continue
			}
			if n > 0 {
				p.logger.Info("reaped stuck jobs", "requeued", n)
			}
		}
	}
}

// backoff returns an exponential delay (baseBackoff * 2^(attempt-1)) with ±25%
// jitter, capped at maxBackoff. attempt is 1-based (first failure = attempt 1).
func (p *Pool) backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := p.baseBackoff
	for i := 1; i < attempt && d < p.maxBackoff; i++ {
		d *= 2
	}
	if d > p.maxBackoff {
		d = p.maxBackoff
	}
	jitter := time.Duration(rand.Int63n(int64(d)/2+1)) - d/4
	return d + jitter
}
