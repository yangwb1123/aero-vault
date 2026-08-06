package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

type Store interface {
	repository.BillingStore
	AcquireLease(ctx context.Context, name, holder string, ttl time.Duration) (bool, error)
}

type Runtime struct {
	store        Store
	bindings     map[string]*Client
	logger       *slog.Logger
	owner        string
	projectEvery time.Duration
	pollEvery    time.Duration
	batchSize    int
	claimTTL     time.Duration
	httpTimeout  time.Duration
	transport    *http.Transport
	startOnce    sync.Once
	closeOnce    sync.Once
	lifecycleMu  sync.Mutex
	closed       bool
	cancel       context.CancelFunc
	wait         sync.WaitGroup
}

func New(cfg config.BillingConfig, store Store, logger *slog.Logger) (*Runtime, error) {
	if store == nil || !cfg.Enabled {
		return nil, errors.New("billing runtime requires enabled config and store")
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid billing runtime config: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	transport := newHTTPTransport()
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   time.Duration(cfg.HTTPTimeoutSeconds) * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	bindings := make(map[string]*Client, len(cfg.Bindings))
	for _, binding := range cfg.Bindings {
		tokens := newTokenSource(cfg.TokenURL, binding.ClientID, binding.ClientSecret, httpClient)
		bindings[binding.TenantID] = newClient(cfg.BaseURL, httpClient, tokens)
	}
	return &Runtime{
		store: store, bindings: bindings, logger: logger, owner: uuid.NewString(),
		projectEvery: time.Duration(cfg.ProjectionIntervalSec) * time.Second,
		pollEvery:    time.Duration(cfg.OutboxPollMillis) * time.Millisecond,
		batchSize:    cfg.OutboxBatchSize,
		claimTTL:     time.Duration(cfg.ClaimTTLSeconds) * time.Second,
		httpTimeout:  time.Duration(cfg.HTTPTimeoutSeconds) * time.Second,
		transport:    transport,
	}, nil
}

func (r *Runtime) Close() {
	r.closeOnce.Do(func() {
		r.lifecycleMu.Lock()
		r.closed = true
		cancel := r.cancel
		r.lifecycleMu.Unlock()
		if cancel != nil {
			cancel()
		}
		r.wait.Wait()
		r.transport.CloseIdleConnections()
	})
}

func (r *Runtime) Start(parent context.Context) {
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	if r.closed {
		return
	}
	r.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(parent)
		r.cancel = cancel
		r.wait.Add(1)
		go func() {
			defer r.wait.Done()
			r.run(ctx)
		}()
	})
}

func (r *Runtime) run(ctx context.Context) {
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		r.runProjector(ctx)
	}()
	go func() {
		defer workers.Done()
		r.runOutbox(ctx)
	}()
	workers.Wait()
}

func newHTTPTransport() *http.Transport {
	if transport, ok := http.DefaultTransport.(*http.Transport); ok {
		return transport.Clone()
	}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

func (r *Runtime) Ready(ctx context.Context) error {
	for tenant := range r.bindings {
		if _, ok, err := r.store.GetBillingProjection(ctx, tenant); err != nil {
			return errors.New("billing projection lookup failed")
		} else if !ok {
			return fmt.Errorf("%w: tenant %q", service.ErrEntitlementUnavailable, tenant)
		}
	}
	return nil
}

func (r *Runtime) CheckQuota(
	ctx context.Context, tenant string, current repository.TenantQuota,
	deltaBytes, deltaObjects int64,
) error {
	if _, ok := r.bindings[tenant]; !ok {
		return fmt.Errorf("%w: tenant is not server-bound", service.ErrEntitlementUnavailable)
	}
	projection, ok, err := r.store.GetBillingProjection(ctx, tenant)
	if err != nil {
		return fmt.Errorf("%w: projection lookup failed", service.ErrEntitlementUnavailable)
	}
	if !ok {
		return fmt.Errorf("%w: projection not initialized", service.ErrEntitlementUnavailable)
	}
	if deltaBytes <= 0 && deltaObjects <= 0 {
		return nil
	}
	if !projectionEffective(projection, time.Now().UTC()) {
		return fmt.Errorf("%w: subscription is inactive", service.ErrQuotaExceeded)
	}
	if exceedsBillingLimit(projection.Bytes, current.UsedBytes, deltaBytes) ||
		exceedsBillingLimit(projection.Objects, current.UsedObjects, deltaObjects) {
		return fmt.Errorf("%w: subscription hard limit", service.ErrQuotaExceeded)
	}
	return nil
}

func (r *Runtime) Apply(
	ctx context.Context, mutation service.UsageMutation,
) (repository.TenantQuota, error) {
	if _, ok := r.bindings[mutation.TenantID]; !ok {
		return repository.TenantQuota{}, fmt.Errorf("%w: tenant is not server-bound", service.ErrEntitlementUnavailable)
	}
	quota, _, err := r.store.ApplyBillingUsage(ctx, repository.BillingUsageMutation{
		OperationID: uuid.NewString(), TenantID: mutation.TenantID, Kind: mutation.Kind,
		DeltaBytes: mutation.DeltaBytes, DeltaObjects: mutation.DeltaObjects,
		OccurredAt: mutation.OccurredAt,
	})
	return quota, err
}

func projectionEffective(projection repository.BillingProjection, now time.Time) bool {
	if !projection.Active || now.Before(projection.EffectiveAt) {
		return false
	}
	return projection.ExpiresAt.IsZero() || now.Before(projection.ExpiresAt)
}

func exceedsBillingLimit(limit repository.BillingLimit, used, delta int64) bool {
	return delta > 0 && !limit.Unlimited && (used > limit.Hard || delta > limit.Hard-used)
}

var _ service.UsageAccountant = (*Runtime)(nil)
