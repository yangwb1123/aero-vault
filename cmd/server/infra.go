package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/events"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/aero-vault/aero-vault/internal/telemetry"
)

func initInfrastructure(ctx context.Context, cfg *config.Config, logger *slog.Logger) (storage.Storage, repository.Repository, *events.Bus, func(context.Context) error, error) {
	shutdownOtel, err := telemetry.Setup(ctx, "aero-vault", logger, cfg.Telemetry.PrometheusEnabled)
	if err != nil {
		logger.Warn("otel setup failed; continuing without", "err", err)
		shutdownOtel = func(context.Context) error { return nil }
	}

	store, err := buildStorage(ctx, cfg)
	if err != nil {
		return nil, nil, nil, shutdownOtel, fmt.Errorf("init storage: %w", err)
	}
	logger.Info("storage ready", "backend", store.Backend(), "sse", cfg.Storage.Local.SSEKey != "" || cfg.Storage.Local.SSEKeyfile != "")

	maybeRewrapSSE(ctx, cfg, store, logger)

	repo, err := repository.Open(ctx, cfg.DB.Driver, cfg.DB.DSN)
	if err != nil {
		return nil, nil, nil, shutdownOtel, fmt.Errorf("open repository: %w", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		repo.Close()
		return nil, nil, nil, shutdownOtel, fmt.Errorf("migrate: %w", err)
	}

	bus := events.NewWithBuffer(repo, logger, cfg.Events.SubBufferSize)
	setupPostgresTransport(ctx, cfg, bus, logger)
	return store, repo, bus, shutdownOtel, nil
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

func setupPostgresTransport(ctx context.Context, cfg *config.Config, bus *events.Bus, logger *slog.Logger) {
	if cfg.Events.Transport == "postgres" && cfg.Events.TransportDSN != "" {
		pt := events.NewPostgresTransport(cfg.Events.TransportDSN, "")
		bus.WithTransport(pt.Publish)
		go func() {
			if err := pt.Run(ctx, bus.Deliver); err != nil {
				logger.Warn("postgres event transport stopped", "err", err)
			}
		}()
		logger.Info("postgres event transport enabled (requires Postgres; unverified in CI)")
	}
}
