package shutdown

import (
	"context"
	"io"
	"log/slog"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewGroup_ContextCancelledOnShutdown(t *testing.T) {
	g := NewGroup(context.Background(), quietLogger())
	if g.Ctx() == nil {
		t.Fatal("expected non-nil context")
	}

	var workerDone atomic.Bool
	g.Go("test-worker", func(ctx context.Context) {
		<-ctx.Done()
		workerDone.Store(true)
	})

	g.Shutdown(context.Background(), 5*time.Second)

	if !workerDone.Load() {
		t.Fatal("worker should have been signalled via context cancellation")
	}
}

func TestGroup_PhaseHookCalled(t *testing.T) {
	g := NewGroup(context.Background(), quietLogger())

	var phases []Phase
	g.WithPhaseHook(func(p Phase) {
		phases = append(phases, p)
	})

	g.Shutdown(context.Background(), 5*time.Second)

	expected := []Phase{PhaseHTTP, PhaseBus, PhaseWorkers, PhaseWait, PhaseOTel, PhaseDB}
	if len(phases) != len(expected) {
		t.Fatalf("expected %d phases, got %d: %v", len(expected), len(phases), phases)
	}
	for i, p := range phases {
		if p != expected[i] {
			t.Fatalf("phase[%d] = %v, want %v", i, p, expected[i])
		}
	}
}

func TestGroup_WorkerFinishesBeforeTimeout(t *testing.T) {
	g := NewGroup(context.Background(), quietLogger())

	g.Go("fast-worker", func(ctx context.Context) {
		<-ctx.Done()
		// Simulate finishing quickly after cancellation
	})

	// Should complete quickly since the worker exits promptly.
	start := time.Now()
	g.Shutdown(context.Background(), 10*time.Second)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("shutdown took %v, expected fast completion", elapsed)
	}
}

func TestGroup_ShutdownTimeout(t *testing.T) {
	g := NewGroup(context.Background(), quietLogger())

	g.Go("slow-worker", func(ctx context.Context) {
		<-ctx.Done()
		// Ignore cancellation and keep running (simulates a stuck worker).
		select {}
	})

	start := time.Now()
	g.Shutdown(context.Background(), 100*time.Millisecond)
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("shutdown should have waited for timeout, took %v", elapsed)
	}
}

func TestGroup_NoGoroutineLeakAfterShutdown(t *testing.T) {
	before := numGoroutines()
	g := NewGroup(context.Background(), quietLogger())

	for i := 0; i < 5; i++ {
		g.Go("leak-test", func(ctx context.Context) {
			<-ctx.Done()
		})
	}

	g.Shutdown(context.Background(), 5*time.Second)

	// Allow goroutines to exit and be cleaned up.
	time.Sleep(50 * time.Millisecond)
	after := numGoroutines()

	// The shutdown group's internal goroutine (waitWithTimeout) may still be
	// finishing; allow a small delta.
	leaked := after - before
	if leaked > 3 {
		t.Errorf("possible goroutine leak: %d goroutines remain (before=%d, after=%d)", leaked, before, after)
	}
}

func TestGroup_MultipleShutdownIsSafe(t *testing.T) {
	g := NewGroup(context.Background(), quietLogger())
	g.Go("worker", func(ctx context.Context) {
		<-ctx.Done()
	})

	// First shutdown.
	g.Shutdown(context.Background(), 5*time.Second)
	// Second shutdown must not panic (cancel on already-cancelled context is safe).
	g.Shutdown(context.Background(), 1*time.Second)
}

func TestGroup_GoStarted(t *testing.T) {
	g := NewGroup(context.Background(), quietLogger())

	var started atomic.Bool
	g.GoStarted("started-worker", func(ctx context.Context, ready chan<- struct{}) {
		started.Store(true)
		close(ready)
		<-ctx.Done()
	})

	time.Sleep(50 * time.Millisecond)
	if !started.Load() {
		t.Fatal("GoStarted worker should have signalled readiness")
	}

	g.Shutdown(context.Background(), 5*time.Second)
}

func TestGroup_GoStarted_PanicBeforeReady(t *testing.T) {
	g := NewGroup(context.Background(), quietLogger())

	done := make(chan struct{})
	go func() {
		g.GoStarted("panic-before-ready", func(ctx context.Context, ready chan<- struct{}) {
			panic("test panic before ready")
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("GoStarted hung after fn panicked before signalling ready")
	}

	g.Shutdown(context.Background(), 5*time.Second)
}

func TestGroup_GoStarted_NeverSignalsCancelled(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	g := NewGroup(parent, quietLogger())

	done := make(chan struct{})
	go func() {
		g.GoStarted("never-signals", func(ctx context.Context, ready chan<- struct{}) {
			<-ctx.Done() // Never closes ready; exits when the context cancels.
		})
		close(done)
	}()

	// Prove the worker is really parked before cancelling.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("GoStarted hung after root context cancellation with no readiness signal")
	}

	g.Shutdown(context.Background(), 5*time.Second)
}

func TestGroup_PanicRecovery(t *testing.T) {
	g := NewGroup(context.Background(), quietLogger())

	g.Go("panicking-worker", func(ctx context.Context) {
		panic("test panic")
	})

	// Give the panic time to propagate.
	time.Sleep(50 * time.Millisecond)
	// Shutdown should not hang despite the panic.
	g.Shutdown(context.Background(), 5*time.Second)
}

func numGoroutines() int {
	return runtime.NumGoroutine()
}
