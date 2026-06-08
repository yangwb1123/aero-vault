// Package cluster provides small primitives for coordinating work across
// replicas. Singleton is a lease-backed leader election that gates a periodic
// action so it runs on only one instance at a time.
package cluster

import (
	"context"
	"log/slog"
	"time"
)

// LeaseStore is the slice of the repository a Singleton needs: an atomic,
// expiring lease keyed by name. repository.Repository satisfies it.
type LeaseStore interface {
	AcquireLease(ctx context.Context, name, holder string, ttl time.Duration) (bool, error)
}

// Singleton gates an action so it runs on only one cluster replica at a time, via
// a repository lease (renew-own / take-over-on-expiry). It is disabled by default
// — a pass-through where every replica runs — and enabled per instance with
// Enable. This is the shared leader-election primitive behind the reconcile,
// lifecycle and retention sweeps, and is reusable by any future singleton task.
type Singleton struct {
	repo    LeaseStore
	lease   string
	holder  string
	enabled bool
	logger  *slog.Logger
}

// NewSingleton returns a disabled Singleton guarding the named lease.
func NewSingleton(repo LeaseStore, lease string, logger *slog.Logger) *Singleton {
	if logger == nil {
		logger = slog.Default()
	}
	return &Singleton{repo: repo, lease: lease, logger: logger}
}

// Enable turns on single-replica gating, identifying this replica as holder.
func (s *Singleton) Enable(holder string) *Singleton {
	s.enabled = true
	s.holder = holder
	return s
}

// Enabled reports whether single-replica gating is on.
func (s *Singleton) Enabled() bool { return s.enabled }

// Guard runs fn iff gating is disabled, or this replica holds (or acquires) the
// lease for this round. ttl should be ~2× the action's interval so a dead
// holder's lease frees and another replica takes over after ~2 rounds. A lease
// error is logged and fn is skipped — fail-safe: better to skip a destructive
// sweep than to run it twice.
func (s *Singleton) Guard(ctx context.Context, ttl time.Duration, fn func(context.Context)) {
	if s.enabled {
		held, err := s.repo.AcquireLease(ctx, s.lease, s.holder, ttl)
		if err != nil {
			s.logger.Warn("singleton: acquire lease", "lease", s.lease, "err", err)
			return
		}
		if !held {
			return
		}
	}
	fn(ctx)
}
