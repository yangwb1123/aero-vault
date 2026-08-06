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
	startOnce      sync.Once
	stopOnce       sync.Once
	stop           chan struct{}
	done           chan struct{}
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
	err = applyDesiredBindings(applyCtx, store, cfg, redactor)
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

func (r *Runtime) Capture(tenant string) bool {
	return r.states[normalizedTenant(tenant)] == repository.AuditGovernanceBindingActive
}

func (r *Runtime) Ready(ctx context.Context) error {
	draining, err := r.store.HasPendingDrainingAuditGovernance(ctx)
	if err != nil {
		return errors.New("audit governance drain lookup failed")
	}
	if draining {
		return errors.New("audit governance binding drain is in progress")
	}
	oldest, ok, err := r.store.OldestPendingAuditGovernance(ctx)
	if err != nil {
		return errors.New("audit governance backlog lookup failed")
	}
	if ok && time.Since(oldest) > r.maxLag {
		return errors.New("audit governance backlog exceeds maximum lag")
	}
	return nil
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
		timer.Reset(r.pollEvery)
	}
}

func applyDesiredBindings(
	ctx context.Context, store Store, cfg config.AuditGovernanceConfig, redactor *redactor,
) error {
	bindings := make([]repository.AuditGovernanceBindingState, 0, len(cfg.Bindings))
	for _, binding := range cfg.Bindings {
		bindings = append(bindings, repository.AuditGovernanceBindingState{
			TenantID: binding.TenantID, State: binding.State,
		})
	}
	err := store.ApplyAuditGovernanceBindings(ctx, cfg.Revision, redactor.desiredDigest(cfg), bindings)
	var backlog *repository.AuditGovernanceUnboundBacklogError
	if errors.As(err, &backlog) {
		refs := make([]string, 0, len(backlog.TenantIDs()))
		for _, tenant := range backlog.TenantIDs() {
			refs = append(refs, redactor.opaqueTenant(tenant))
		}
		return fmt.Errorf("audit governance unbound backlog blocks startup: refs=%s",
			strings.Join(refs, ","))
	}
	if err != nil {
		return errors.New("audit governance binding state initialization failed")
	}
	return nil
}
