// Package shutdown provides coordinated goroutine lifecycle management and
// phased graceful shutdown for AeroVault. It ensures that background workers
// complete in-flight work before the process exits, preventing data corruption
// and partial operations.
//
// Usage:
//
//	sg := shutdown.NewGroup(context.Background(), logger)
//	sg.Go("indexer", func(ctx context.Context) {
//	    indexer.Run(ctx, bus.Subscribe())
//	})
//	// ... start more workers ...
//	sg.Shutdown(ctx) // Phased: HTTP → Bus → Workers → OTel → DB
package shutdown

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Phase represents a step in the shutdown sequence.
type Phase int

const (
	PhaseHTTP    Phase = iota + 1 // Stop accepting new requests
	PhaseBus                      // Stop event publishing, drain consumers
	PhaseWorkers                  // Signal workers to finish current work
	PhaseWait                     // Wait for all goroutines (with timeout)
	PhaseOTel                     // Shutdown OpenTelemetry exporter
	PhaseDB                       // Close database connections
)

func (p Phase) String() string {
	switch p {
	case PhaseHTTP:
		return "http"
	case PhaseBus:
		return "bus"
	case PhaseWorkers:
		return "workers"
	case PhaseWait:
		return "wait"
	case PhaseOTel:
		return "otel"
	case PhaseDB:
		return "db"
	default:
		return "unknown"
	}
}

// Group manages goroutine lifecycle with coordinated shutdown.
// Create with NewGroup, register goroutines with Go, and trigger shutdown
// with Shutdown.
type Group struct {
	logger *slog.Logger
	root   context.Context
	cancel context.CancelFunc

	mu    sync.Mutex
	names []string // goroutine names, for logging
	wg    sync.WaitGroup

	// onPhase is called at the start of each shutdown phase. Nil is a no-op.
	onPhase func(Phase)
}

// NewGroup creates a new shutdown Group. The returned context is a child of ctx
// that is cancelled when Shutdown is called. All goroutines registered via Go
// should select on this context or check ctx.Done() to be notified of shutdown.
func NewGroup(ctx context.Context, logger *slog.Logger) *Group {
	ctx, cancel := context.WithCancel(ctx)
	if logger == nil {
		logger = slog.Default()
	}
	return &Group{
		logger: logger,
		root:   ctx,
		cancel: cancel,
	}
}

// WithPhaseHook sets a callback invoked at the start of each shutdown phase.
// Useful for integrating external drain logic (e.g., HTTP server Shutdown,
// Bus Close).
func (g *Group) WithPhaseHook(fn func(Phase)) *Group {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.onPhase = fn
	return g
}

// Ctx returns the group's root context, which is cancelled when Shutdown is
// called. Workers should use this context for cancellation.
func (g *Group) Ctx() context.Context { return g.root }

// Go starts a goroutine that is tracked by the Group. The name is used in
// shutdown logging. The function should return when ctx is cancelled (i.e.,
// when Shutdown is called). If fn returns, the goroutine is considered done.
func (g *Group) Go(name string, fn func(ctx context.Context)) {
	g.mu.Lock()
	g.names = append(g.names, name)
	g.mu.Unlock()
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		defer g.recoverPanic(name)
		fn(g.root)
	}()
}

// GoStarted is like Go but expects fn to signal readiness on ready chan.
// This ensures the goroutine has started before Shutdown can be called.
func (g *Group) GoStarted(name string, fn func(ctx context.Context, ready chan<- struct{})) {
	g.mu.Lock()
	g.names = append(g.names, name)
	g.mu.Unlock()
	ready := make(chan struct{})
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		defer g.recoverPanic(name)
		fn(g.root, ready)
	}()
	<-ready
}

func (g *Group) recoverPanic(name string) {
	if r := recover(); r != nil {
		g.logger.Error("goroutine panicked",
			"name", name,
			"panic", r,
		)
	}
}

// Shutdown performs a phased graceful shutdown:
//
//  1. PhaseHTTP:   Stop accepting new requests (caller should invoke srv.Shutdown)
//  2. PhaseBus:    Stop event publishing, drain consumers
//  3. PhaseWorkers: Cancel root context (signal workers to finish)
//  4. PhaseWait:   Wait up to timeout for all goroutines to complete
//  5. PhaseOTel:   Log that OTel should be shutdown (caller handles)
//  6. PhaseDB:     Log that DB should be closed (caller handles)
//
// If the provided ctx is cancelled before shutdown completes, remaining
// goroutines are abandoned.
func (g *Group) Shutdown(ctx context.Context, timeout time.Duration) {
	g.logger.Info("shutdown: starting phased shutdown", "timeout", timeout)
	g.logPhase(PhaseHTTP)
	g.firePhase(PhaseHTTP)

	g.logPhase(PhaseBus)
	g.firePhase(PhaseBus)

	g.logPhase(PhaseWorkers)
	g.cancel() // Signal all workers via context
	g.firePhase(PhaseWorkers)

	g.logPhase(PhaseWait)
	g.firePhase(PhaseWait)
	g.waitWithTimeout(ctx, timeout)

	g.logPhase(PhaseOTel)
	g.firePhase(PhaseOTel)

	g.logPhase(PhaseDB)
	g.firePhase(PhaseDB)

	g.logger.Info("shutdown: complete")
}

func (g *Group) waitWithTimeout(ctx context.Context, timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		g.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		g.logger.Info("shutdown: all goroutines finished")
	case <-ctx.Done():
		g.logger.Warn("shutdown: context cancelled while waiting for goroutines")
	case <-time.After(timeout):
		g.logger.Warn("shutdown: timeout waiting for goroutines", "timeout", timeout)
	}
}

func (g *Group) logPhase(p Phase) {
	g.logger.Info("shutdown: phase starting", "phase", p.String())
}

func (g *Group) firePhase(p Phase) {
	g.mu.Lock()
	onPhase := g.onPhase
	g.mu.Unlock()
	if onPhase != nil {
		onPhase(p)
	}
}
