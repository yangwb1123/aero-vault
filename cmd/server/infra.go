package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/events"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/spaces"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/aero-vault/aero-vault/internal/telemetry"
)

type preparedInfrastructure struct {
	store    storage.Storage
	raw      repository.Repository
	readers  spaceReaders
	bus      *events.Bus
	shutdown func(context.Context) error
}

// The base store and migration are prerequisites for capturing authoritative
// ports. No transport, rewrap, telemetry exporter or worker starts here.
func prepareInfrastructure(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*preparedInfrastructure, error) {
	store, err := buildStorage(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("init storage: %w", err)
	}
	logger.Info("storage ready", "backend", store.Backend(), "sse", cfg.Storage.Local.SSEKey != "" || cfg.Storage.Local.SSEKeyfile != "")
	repo, err := repository.Open(ctx, cfg.DB.Driver, cfg.DB.DSN)
	if err != nil {
		return nil, fmt.Errorf("open repository: %w", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		repo.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	readers, err := captureSpaceReaders(repo)
	if err != nil {
		repo.Close()
		return nil, err
	}
	bus := events.NewWithBuffer(repo, logger, cfg.Events.SubBufferSize)
	readers.wireBus(bus)
	return &preparedInfrastructure{store: store, raw: repo, readers: readers, bus: bus,
		shutdown: func(context.Context) error { return nil }}, nil
}

// Retained construction signature; callers needing starts use the guarded
// per-instance startup plan, not an eager infrastructure builder.
func initInfrastructure(ctx context.Context, cfg *config.Config, logger *slog.Logger) (storage.Storage, repository.Repository, *events.Bus, func(context.Context) error, error) {
	infra, err := prepareInfrastructure(ctx, cfg, logger)
	if err != nil {
		return nil, nil, nil, func(context.Context) error { return nil }, err
	}
	return infra.store, infra.raw, infra.bus, infra.shutdown, nil
}

func (i *preparedInfrastructure) start(ctx context.Context, cfg *config.Config, logger *slog.Logger) {
	shutdown, err := telemetry.Setup(ctx, "aero-vault", logger, cfg.Telemetry.PrometheusEnabled)
	if err != nil {
		logger.Warn("otel setup failed; continuing without", "err", err)
	} else {
		i.shutdown = shutdown
	}
	maybeRewrapSSE(ctx, cfg, i.store, logger)
}

func maybeRewrapSSE(ctx context.Context, cfg *config.Config, store storage.Storage, logger *slog.Logger) {
	if cfg.Storage.SSERewrapOnStart {
		go func() {
			rep, err := storage.RewrapStale(ctx, store)
			if err != nil {
				logger.Warn("sse rewrap-on-start failed", "err", err)
				return
			}
			if rep.Failed > 0 {
				logger.Warn("sse rewrap-on-start: some objects could not be re-wrapped",
					"scanned", rep.Scanned, "rewrapped", rep.Rewrapped, "failed", rep.Failed)
			} else {
				logger.Info("sse rewrap-on-start complete", "scanned", rep.Scanned, "rewrapped", rep.Rewrapped)
			}
		}()
	}
}

func preparePostgresTransport(cfg *config.Config, readers spaceReaders) *events.PostgresTransport {
	if cfg.Events.Transport != "postgres" || cfg.Events.TransportDSN == "" {
		return nil
	}
	return events.NewPostgresTransport(cfg.Events.TransportDSN, "").
		WithResourceBoundary(events.NewResourceBoundary(readers.router.Binding))
}

func startPostgresTransport(ctx context.Context, pt *events.PostgresTransport, bus *events.Bus, logger *slog.Logger) {
	if pt == nil {
		return
	}
	bus.WithTransport(pt.Publish)
	go func() {
		if err := pt.Run(ctx, func(e repository.Event) { bus.DeliverContext(ctx, e) }); err != nil {
			logger.Warn("postgres event transport stopped", "err", err)
		}
	}()
	logger.Info("postgres event transport enabled (requires Postgres; unverified in CI)")
}

// Relay construction performs no claims, backlog queries or egress. The
// kill-switch still precedes sink construction; config.Load validates all
// numeric/L2 configuration independently, including when the relay is off.
func prepareEventOutboxRelay(cfg *config.Config, logger *slog.Logger, repo repository.Repository, binding spaces.BindingReader) (*events.EventOutboxRelay, error) {
	if !cfg.EventOutbox.Enabled {
		return nil, nil
	}
	opts := events.EventOutboxRelayOptions{
		PollInterval: time.Duration(cfg.EventOutbox.PollMilliseconds) * time.Millisecond,
		BatchSize:    cfg.EventOutbox.BatchSize, ClaimTTL: time.Duration(cfg.EventOutbox.ClaimTTLSeconds) * time.Second,
		HTTPTimeout:     time.Duration(cfg.EventOutbox.HTTPTimeoutSeconds) * time.Second,
		MaxAttempts:     cfg.EventOutbox.MaxAttempts,
		DeliveredRetain: time.Duration(cfg.EventOutbox.DeliveredRetentionHours) * time.Hour,
		FailedRetain:    time.Duration(cfg.EventOutbox.FailedRetentionHours) * time.Hour,
	}
	sink, err := auditSinkForEventOutbox(cfg, opts.HTTPTimeout, logger)
	if err != nil {
		return nil, err
	}
	opts.AuditSink = sink
	if sink != nil {
		endpoint, bindings := auditSinkBearerConfig(cfg)
		logger.Info("event outbox relay L2 audit sink enabled", "endpoint", endpoint,
			"bindings", len(bindings), "kind", auditSinkKind(cfg))
	}
	variant := "none"
	if sink != nil {
		variant = "bearer"
	}
	logger.Info("event outbox relay audit sink", "kind", auditSinkKind(cfg), "variant", variant)
	return events.NewEventOutboxRelay(repo, logger, opts).WithResourceBoundary(binding), nil
}

func runPreparedEventOutboxRelay(ctx context.Context, cfg *config.Config, logger *slog.Logger, repo repository.Repository, relay *events.EventOutboxRelay) {
	if !cfg.EventOutbox.Enabled {
		logger.Info("event outbox relay disabled", "backlog", eventOutboxBacklog(ctx, repo), "audit_sink_kind", auditSinkKind(cfg))
		return
	}
	logger.Info("event outbox relay started", "audit_sink_kind", auditSinkKind(cfg),
		"poll_ms", cfg.EventOutbox.PollMilliseconds, "batch", cfg.EventOutbox.BatchSize,
		"claim_ttl_s", cfg.EventOutbox.ClaimTTLSeconds, "http_timeout_s", cfg.EventOutbox.HTTPTimeoutSeconds,
		"max_attempts", cfg.EventOutbox.MaxAttempts,
		"delivered_retain_h", cfg.EventOutbox.DeliveredRetentionHours, "failed_retain_h", cfg.EventOutbox.FailedRetentionHours,
		"backlog", eventOutboxBacklog(ctx, repo))
	go relay.Run(ctx)
}

func startEventOutboxRelay(ctx context.Context, cfg *config.Config, logger *slog.Logger, repo repository.Repository) error {
	if !cfg.EventOutbox.Enabled {
		runPreparedEventOutboxRelay(ctx, cfg, logger, repo, nil)
		return nil
	}
	readers, err := captureSpaceReaders(repo)
	if err != nil {
		return err
	}
	relay, err := prepareEventOutboxRelay(cfg, logger, repo, readers.router.Binding)
	if err != nil {
		return err
	}
	runPreparedEventOutboxRelay(ctx, cfg, logger, repo, relay)
	return nil
}

func auditSinkKind(cfg *config.Config) string {
	kind := strings.ToUpper(strings.TrimSpace(cfg.AuditSink.Kind))
	if kind != "" {
		return kind
	}
	if cfg.AuditSink.Legacy.Enabled || cfg.AuditGovernance.Enabled ||
		cfg.AuditSink.Endpoint != "" || cfg.AuditSink.BindingsFile != "" || len(cfg.AuditSink.Bindings) > 0 ||
		cfg.AuditSinkL2.Endpoint != "" || cfg.AuditSinkL2.BindingsFile != "" || len(cfg.AuditSinkL2.Bindings) > 0 {
		return config.AuditSinkKindL2
	}
	return config.AuditSinkKindL0
}

func auditSinkBearerConfig(cfg *config.Config) (string, []config.AuditSinkL2Binding) {
	if cfg.AuditSink.Endpoint != "" || cfg.AuditSink.BindingsFile != "" || len(cfg.AuditSink.Bindings) > 0 {
		return cfg.AuditSink.Endpoint, cfg.AuditSink.Bindings
	}
	endpoint := cfg.AuditSinkL2.Endpoint
	bindings := cfg.AuditSinkL2.Bindings
	if endpoint == "" {
		endpoint = cfg.AuditSink.Endpoint
		bindings = cfg.AuditSink.Bindings
	}
	return endpoint, bindings
}

func auditSinkForEventOutbox(cfg *config.Config, timeout time.Duration, logger *slog.Logger) (events.AuditSink, error) {
	if auditSinkKind(cfg) != config.AuditSinkKindL2 || cfg.AuditSink.Legacy.Enabled || cfg.AuditGovernance.Enabled {
		return nil, nil
	}
	endpoint, bindings := auditSinkBearerConfig(cfg)
	if endpoint == "" {
		return nil, nil
	}
	sink, err := events.NewAuditSinkL2(endpoint, auditSinkL2Bindings(bindings), events.NewAuditSinkL2Client(timeout), logger)
	if err != nil {
		return nil, fmt.Errorf("build audit sink L2: %w", err)
	}
	return sink, nil
}

// Diagnostic only: nil/error is unknown, never an admission or readiness fact.
func eventOutboxBacklog(ctx context.Context, repo repository.Repository) any {
	if repo == nil {
		return "unknown"
	}
	n, err := repo.CountEventOutbox(ctx)
	if err != nil {
		return "unknown"
	}
	return n
}

func auditSinkL2Bindings(bindings []config.AuditSinkL2Binding) map[string]string {
	if len(bindings) == 0 {
		return nil
	}
	out := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		out[binding.Tenant] = binding.Token
	}
	return out
}
