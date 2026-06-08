package cluster

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeLease struct {
	grant map[string]bool
	err   error
	calls int
}

func (f *fakeLease) AcquireLease(_ context.Context, name, _ string, _ time.Duration) (bool, error) {
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	return f.grant[name], nil
}

func TestSingleton_DisabledAlwaysRuns(t *testing.T) {
	f := &fakeLease{}
	s := NewSingleton(f, "x", nil)
	ran := false
	s.Guard(context.Background(), time.Minute, func(context.Context) { ran = true })
	if !ran {
		t.Fatal("a disabled singleton should always run")
	}
	if s.Enabled() {
		t.Fatal("should be disabled by default")
	}
	if f.calls != 0 {
		t.Fatalf("disabled singleton must not touch the lease, got %d calls", f.calls)
	}
}

func TestSingleton_EnabledRunsWhenHeld(t *testing.T) {
	f := &fakeLease{grant: map[string]bool{"sweep": true}}
	s := NewSingleton(f, "sweep", nil).Enable("node-A")
	ran := false
	s.Guard(context.Background(), time.Minute, func(context.Context) { ran = true })
	if !ran || f.calls != 1 {
		t.Fatalf("should acquire the lease and run: ran=%v calls=%d", ran, f.calls)
	}
}

func TestSingleton_EnabledSkipsWhenNotHeld(t *testing.T) {
	f := &fakeLease{grant: map[string]bool{"sweep": false}}
	s := NewSingleton(f, "sweep", nil).Enable("node-B")
	ran := false
	s.Guard(context.Background(), time.Minute, func(context.Context) { ran = true })
	if ran {
		t.Fatal("should not run when another replica holds the lease")
	}
}

func TestSingleton_LeaseErrorFailsSafe(t *testing.T) {
	f := &fakeLease{err: errors.New("db down")}
	s := NewSingleton(f, "sweep", nil).Enable("node-C")
	ran := false
	s.Guard(context.Background(), time.Minute, func(context.Context) { ran = true })
	if ran {
		t.Fatal("a lease error must skip the action (fail-safe), not run it")
	}
}
