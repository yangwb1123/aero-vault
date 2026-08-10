package auditgovernance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/repository"
)

type Store interface {
	repository.AuditGovernanceStore
}

// storeProbeTimeout bounds Ready()'s two store probes independently of
// AUDIT_GOVERNANCE_HTTP_TIMEOUT_SECONDS (5s default, relay HTTP bound) and
// the caller's request context: a wedged relay store must not hold /readyz
// beyond ~2s per probe. Mirrors readyzProbeTimeout (cmd/server/http.go).
const storeProbeTimeout = 2 * time.Second

type Runtime struct {
	store          Store
	publisher      *Publisher
	logger         *slog.Logger
	owner          string
	revision       uint64
	bound          map[string]struct{}
	states         map[string]string
	redactor       *redactor
	pollEvery      time.Duration
	batchSize      int
	claimTTL       time.Duration
	httpTimeout    time.Duration
	initialBackoff time.Duration
	maxBackoff     time.Duration
	maxLag         time.Duration
	reconcileBatch int
	retention      time.Duration
	cleanupEvery   time.Duration
	cleanupBatch   int
	nextCleanup    time.Time
	transport      *httpTransportCloser
	onRetry        func(delay time.Duration)
	startOnce      sync.Once
	stopOnce       sync.Once
	stop           chan struct{}
	done           chan struct{}
	// draining is true when this Runtime was built from a drain-mode boot
	// (AUDIT_GOVERNANCE_DRAIN=true + empty manifest): capture is inactive for
	// every tenant by construction, and the WARN boot log + draining/bound-
	// tenants gauges make the transition self-describing.
	draining bool
	// appliedDigest is the desired-manifest digest persisted into
	// audit_governance_control by the apply — the WARN log fingerprint.
	appliedDigest string
	// degradedMu guards the probe-result cache (degraded + backlogAge):
	// recordDegraded writes BOTH fields under a single Lock acquisition so
	// readers can only observe valid (degraded, age) pairs; the getters are
	// zero-I/O RLock reads (the run loop + /readyz keep the cache fresh).
	degradedMu sync.RWMutex
	degraded   bool
	backlogAge time.Duration
}

type httpTransportCloser struct {
	close func()
}

func New(
	cfg config.AuditGovernanceConfig, store Store, logger *slog.Logger,
) (*Runtime, error) {
	if store == nil || !cfg.Enabled {
		return nil, errors.New("audit governance runtime requires enabled config and store")
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid audit governance config: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	redactor, err := newRedactor(cfg.HMACKey)
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(cfg.HTTPTimeoutSeconds) * time.Second
	applyCtx, cancel := context.WithTimeout(context.Background(), timeout)
	appliedDigest, err := applyDesiredBindings(applyCtx, store, cfg, redactor)
	cancel()
	if err != nil {
		return nil, err
	}
	client, transport := noRedirectClient(timeout)
	publisher, err := newPublisher(cfg, client)
	if err != nil {
		return nil, err
	}
	bound := make(map[string]struct{}, len(cfg.Bindings))
	states := make(map[string]string, len(cfg.Bindings))
	for _, binding := range cfg.Bindings {
		bound[binding.TenantID] = struct{}{}
		states[binding.TenantID] = binding.State
	}
	return &Runtime{
		store: store, publisher: publisher, logger: logger, owner: uuid.NewString(), revision: cfg.Revision,
		bound:  bound,
		states: states, redactor: redactor,
		pollEvery: time.Duration(cfg.PollMilliseconds) * time.Millisecond,
		batchSize: cfg.BatchSize, claimTTL: time.Duration(cfg.ClaimTTLSeconds) * time.Second,
		httpTimeout: timeout, initialBackoff: time.Duration(cfg.InitialBackoffSeconds) * time.Second,
		maxBackoff: time.Duration(cfg.MaxBackoffSeconds) * time.Second,
		maxLag:     time.Duration(cfg.MaxLagSeconds) * time.Second, reconcileBatch: cfg.ReconcileBatchSize,
		retention:    time.Duration(cfg.DeliveredRetentionSeconds) * time.Second,
		cleanupEvery: time.Duration(cfg.CleanupIntervalSeconds) * time.Second,
		cleanupBatch: cfg.CleanupBatchSize,
		transport:    &httpTransportCloser{close: transport.CloseIdleConnections},
		stop:         make(chan struct{}), done: make(chan struct{}),
		draining:      cfg.Drain && len(cfg.Bindings) == 0,
		appliedDigest: appliedDigest,
	}, nil
}

func (r *Runtime) Start(parent context.Context) {
	r.startOnce.Do(func() {
		if parent.Err() != nil {
			r.initiateStop()
		}
		go r.run()
		go func() {
			select {
			case <-parent.Done():
				r.initiateStop()
			case <-r.done:
			}
		}()
	})
}

func (r *Runtime) Close() {
	r.initiateStop()
	r.startOnce.Do(func() { close(r.done) })
	select {
	case <-r.done:
	case <-time.After(r.claimTTL + r.httpTimeout):
		r.logger.Error("audit governance relay drain timed out")
	}
	r.transport.close()
}

func (r *Runtime) initiateStop() {
	r.stopOnce.Do(func() { close(r.stop) })
}

func (r *Runtime) Bound(tenant string) bool {
	_, ok := r.bound[normalizedTenant(tenant)]
	return ok
}

// Draining reports whether this Runtime was built from a drain-mode boot
// (AUDIT_GOVERNANCE_DRAIN=true + empty manifest). Zero store I/O; backs the
// audit_governance.draining gauge and the drain-mode WARN boot log.
func (r *Runtime) Draining() bool {
	return r.draining
}

// BoundTenants is the number of applied bindings (zero in drain mode). Zero
// store I/O; backs the audit_governance.bound_tenants gauge — the only
// positive signal distinguishing a drained-but-enabled relay from a healthy
// one (both report degraded=0 / backlog_age=0).
func (r *Runtime) BoundTenants() int {
	return len(r.bound)
}

// AppliedDigest returns the desired-manifest digest persisted into the
// audit_governance_control row by the last apply — the drain boot log
// fingerprint (cross-referenceable against the DB, not secret).
func (r *Runtime) AppliedDigest() string {
	return r.appliedDigest
}

func (r *Runtime) Capture(tenant string) bool {
	return r.states[normalizedTenant(tenant)] == repository.AuditGovernanceBindingActive
}

// PendingBacklogAge returns the age of the oldest pending (non-terminal) fact
// for bound tenants. ok=false means no pending facts. B3-2: the value feeds
// the audit_governance_backlog_age_seconds gauge that drives the degraded
// alert (maxLag×0.5); terminal rows are excluded by the store query, so a
// fully dead-lettered backlog reports zero backlog and never blocks
// readiness. This is the store-querying accessor; BacklogAge() (no args) is
// the zero-I/O cache getter returning the last probe result.
func (r *Runtime) PendingBacklogAge(ctx context.Context) (time.Duration, bool, error) {
	oldest, ok, err := r.store.OldestPendingAuditGovernance(ctx)
	if err != nil {
		return 0, false, err
	}
	if !ok {
		return 0, false, nil
	}
	return time.Since(oldest), true, nil
}

// Degraded reports whether the last readiness probe recorded a degraded
// condition (lag > configured maxLag, or a store probe timeout/cancel — age
// unknown). Zero store I/O: the cache is refreshed by probeAndRecord (the
// run loop, once per poll cycle, and every /readyz request).
func (r *Runtime) Degraded() bool {
	r.degradedMu.RLock()
	defer r.degradedMu.RUnlock()
	return r.degraded
}

// BacklogAge returns the age recorded by the last readiness probe (0 when
// none ran yet, when the store is empty, or when the probe timed out with
// the age unknown). Zero store I/O — the gauge and /readyz read this cache.
func (r *Runtime) BacklogAge() time.Duration {
	r.degradedMu.RLock()
	defer r.degradedMu.RUnlock()
	return r.backlogAge
}

// isProbeCtxError distinguishes probe timeout/cancellation (the wedged-store
// shape — degrades, never fails readiness) from genuine store errors
// (fail-closed readiness failures, unchanged).
func isProbeCtxError(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// recordDegraded writes BOTH cache fields under a single degradedMu.Lock()
// acquisition so readers can only observe valid (degraded, age) pairs:
// (true, 0) timeout/unknown, (true, age>maxLag), (false, age≤maxLag),
// (false, 0).
func (r *Runtime) recordDegraded(degraded bool, age time.Duration) {
	r.degradedMu.Lock()
	r.degraded = degraded
	r.backlogAge = age
	r.degradedMu.Unlock()
}

// probeAndRecord runs Ready()'s two store probes under a shared
// storeProbeTimeout bound (a wedged relay store must not hold /readyz beyond
// ~2s) and records the result into the degraded cache. Probe timeout/cancel
// records degraded with age 0 and returns nil — a wedged store degrades the
// audit-governance readiness contribution, it never 503s the node.
func (r *Runtime) probeAndRecord(ctx context.Context) error {
	probeCtx, cancel := context.WithTimeout(ctx, storeProbeTimeout)
	defer cancel()
	draining, err := r.store.HasPendingDrainingAuditGovernance(probeCtx)
	if err != nil {
		if isProbeCtxError(err) {
			r.logger.Warn("audit governance store probe timed out — degraded", "probe", "drain")
			r.recordDegraded(true, 0)
			return nil
		}
		return errors.New("audit governance drain lookup failed")
	}
	if draining {
		return errors.New("audit governance binding drain is in progress")
	}
	oldest, ok, err := r.store.OldestPendingAuditGovernance(probeCtx)
	if err != nil {
		if isProbeCtxError(err) {
			r.logger.Warn("audit governance store probe timed out — degraded", "probe", "backlog")
			r.recordDegraded(true, 0)
			return nil
		}
		return errors.New("audit governance backlog lookup failed")
	}
	age := time.Duration(0)
	if ok {
		age = time.Since(oldest)
	}
	// B3-2 (D1): a backlog beyond maxLag is a degraded condition, not a
	// readiness failure — /readyz stays 200 (no restart loop) and the
	// backlog-age gauge (alert threshold maxLag×0.5) drives operator
	// attention. Store errors remain fail-closed readiness failures.
	if ok && age > r.maxLag {
		r.logger.Warn("audit governance relay degraded",
			"backlog_age", age.String(), "max_lag", r.maxLag.String())
		r.recordDegraded(true, age)
		return nil
	}
	r.recordDegraded(false, age)
	return nil
}

func (r *Runtime) Ready(ctx context.Context) error {
	return r.probeAndRecord(ctx)
}

func (r *Runtime) run() {
	defer close(r.done)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-timer.C:
		}
		if r.stopping() {
			return
		}
		r.reconcile()
		if r.stopping() {
			return
		}
		r.deliverBatch()
		r.cleanupDelivered()
		if r.stopping() {
			return
		}
		// G3: refresh the degraded cache once per poll cycle so the gauge is
		// fresh (≤ poll interval) independent of /readyz traffic. A genuine
		// store error logs and skips recording — it never stops the loop.
		if err := r.probeAndRecord(context.Background()); err != nil {
			r.logger.Warn("audit governance readiness probe failed", "error", err)
		}
		timer.Reset(r.pollEvery)
	}
}

func applyDesiredBindings(
	ctx context.Context, store Store, cfg config.AuditGovernanceConfig, redactor *redactor,
) (string, error) {
	bindings := make([]repository.AuditGovernanceBindingState, 0, len(cfg.Bindings))
	for _, binding := range cfg.Bindings {
		bindings = append(bindings, repository.AuditGovernanceBindingState{
			TenantID: binding.TenantID, State: binding.State,
		})
	}
	digest := redactor.desiredDigest(cfg)
	err := store.ApplyAuditGovernanceBindings(ctx, cfg.Revision, digest, bindings)
	var backlog *repository.AuditGovernanceUnboundBacklogError
	if errors.As(err, &backlog) {
		refs := make([]string, 0, len(backlog.TenantIDs()))
		for _, tenant := range backlog.TenantIDs() {
			refs = append(refs, redactor.opaqueTenant(tenant))
		}
		return "", fmt.Errorf("audit governance unbound backlog blocks startup: refs=%s",
			strings.Join(refs, ","))
	}
	if err != nil {
		return "", errors.New("audit governance binding state initialization failed")
	}
	return digest, nil
}
