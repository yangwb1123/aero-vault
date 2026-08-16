package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/ai"
	"github.com/aero-vault/aero-vault/internal/auditgovernance"
	"github.com/aero-vault/aero-vault/internal/cli"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/events"
	"github.com/aero-vault/aero-vault/internal/jobs"
	"github.com/aero-vault/aero-vault/internal/mcp"
	"github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/server"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/thumbnail"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "mcp":
			if err := runMCP(); err != nil {
				fmt.Fprintf(os.Stderr, "mcp: %v\n", err)
				os.Exit(1)
			}
			return
		case "cli":
			os.Exit(cli.Run(os.Args[2:]))
		}
	}
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.App.LogLevel}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, repo, bus, shutdownOtel, err := initInfrastructure(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer repo.Close()
	accessManager, err := buildAccessManager(cfg, repo)
	if err != nil {
		return fmt.Errorf("configure enterprise access: %w", err)
	}
	billingRuntime, err := buildBillingRuntime(cfg, repo, logger)
	if err != nil {
		return err
	}
	auditRuntime, err := buildAuditGovernanceRuntime(cfg, repo, logger)
	if err != nil {
		return err
	}
	if billingRuntime != nil {
		defer billingRuntime.Close()
		billingRuntime.Start(ctx)
		repo = wrapBillingRepository(repo, billingRuntime)
	}
	if auditRuntime != nil {
		defer auditRuntime.Close()
		auditRuntime.Start(ctx)
		repo = auditgovernance.WrapRepository(repo, auditRuntime)
		bus.WithRepository(repo)
	}

	embedder := buildEmbedder(cfg, logger)
	if cfg.AI.EmbedCacheSize > 0 {
		embedder = ai.NewCachingEmbedder(embedder, cfg.AI.EmbedCacheSize)
	}
	llm := buildLLM(cfg, logger)
	reranker := buildReranker(cfg, logger)

	svc := service.NewFileService(store, repo, logger).
		WithEventSink(bus).
		WithAuthorizer(accessManager).
		WithDeleteFailOpen(!cfg.Access.DeleteFailClosed).
		WithTenantStatusEnforcement().
		WithReadVerification(service.ReadVerificationConfig{
			Enabled: cfg.Storage.VerifyOnRead,
			MaxSize: cfg.Storage.VerifyMaxSize,
			Sample:  cfg.Storage.VerifySample,
		})
	if billingRuntime != nil {
		svc.WithUsageAccountant(billingRuntime)
	}
	if cfg.Storage.VerifyOnRead {
		logger.Info("read-path verification enabled",
			"max_size", cfg.Storage.VerifyMaxSize,
			"sample", cfg.Storage.VerifySample)
	}

	corsProvider := middleware.NewBucketCORSProvider(repo, 60*time.Second)
	defer corsProvider.Close()

	var (
		jobReg   *jobs.Registry
		jobQueue *jobs.Queue
	)
	if cfg.Jobs.Workers > 0 {
		jobReg = jobs.NewRegistry()
		jobQueue = jobs.NewQueue(repo)
		if cfg.Jobs.MaxDepth > 0 {
			jobQueue.WithMaxDepth(cfg.Jobs.MaxDepth)
		}
	}

	var (
		search *ai.Search
		chat   *ai.Chat
		agent  *ai.Agent
	)
	if embedder != nil {
		search, chat, agent = buildAIComponents(ctx, cfg, logger, repo, store, bus, svc, embedder, llm, reranker, jobReg, jobQueue)
	}

	if err := buildBackgroundWorkers(ctx, cfg, logger, repo, store, bus, jobReg, jobQueue, svc); err != nil {
		return err
	}

	// Single construction point for the server-side thumbnail cache (REQ-2.1):
	// the same instance is served by the REST handler (WithThumbnailCache inside
	// buildRouter) and physically purged by the TTL sweep timer driver below.
	thumbCache := thumbnail.NewCache(cfg.App.ThumbnailCacheBytes, time.Duration(cfg.App.ThumbnailCacheTTL)*time.Second)
	if cfg.Reconcile.IntervalMinutes > 0 {
		startThumbnailCacheSweep(ctx, thumbCache, time.Duration(cfg.Reconcile.IntervalMinutes)*time.Minute, logger)
	}

	authReg := buildAuthRegistry(ctx, cfg, logger, repo)
	oidcHandler, err := buildOIDCHandler(cfg)
	if err != nil {
		return fmt.Errorf("configure OIDC: %w", err)
	}

	rl := middleware.NewRateLimiter(cfg.RateLimit.RPS, cfg.RateLimit.Burst)
	aiRL := middleware.NewRateLimiter(cfg.RateLimit.AIRPS, cfg.RateLimit.AIBurst)
	adminRL := middleware.NewRateLimiter(cfg.RateLimit.AdminRPS, cfg.RateLimit.AdminBurst)
	rl.Start(ctx)
	aiRL.Start(ctx)
	adminRL.Start(ctx)

	promHandler := buildPrometheus(cfg, logger)
	registerGauges(repo, auditRuntime)

	aiTimeout := time.Duration(cfg.App.RequestTimeoutSec) * time.Second
	dispatcher := buildRouter(svc, repo, svc.Storage(), search, chat, agent, bus, authReg, accessManager, oidcHandler, promHandler, thumbCache, cfg, aiTimeout, aiRL, adminRL, logger, corsProvider, runtimeReadiness(billingRuntime, auditRuntime))
	cl := middleware.NewConcurrencyLimiter(cfg.App.MaxInFlight)
	var concurrencyMW func(http.Handler) http.Handler
	if cfg.App.PerTenantMax > 0 {
		ptcl := middleware.NewPerTenantConcurrencyLimiter(cfg.App.MaxInFlight, cfg.App.PerTenantMax)
		concurrencyMW = ptcl.Middleware()
	} else {
		concurrencyMW = cl.Middleware()
	}
	finalHandler := server.ApplyMiddleware(dispatcher, repo, authReg, rl, cfg, logger, concurrencyMW, corsProvider)

	return runServer(ctx, finalHandler, cfg, logger, bus, shutdownOtel)
}

func runMCP() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.App.LogLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := buildStorage(ctx, cfg)
	if err != nil {
		return err
	}
	repo, err := repository.Open(ctx, cfg.DB.Driver, cfg.DB.DSN)
	if err != nil {
		return err
	}
	defer repo.Close()
	if err := repo.Migrate(ctx); err != nil {
		return err
	}
	accessManager, err := buildAccessManager(cfg, repo)
	if err != nil {
		return fmt.Errorf("configure enterprise access: %w", err)
	}
	billingRuntime, err := buildBillingRuntime(cfg, repo, logger)
	if err != nil {
		return err
	}
	auditRuntime, err := buildAuditGovernanceRuntime(cfg, repo, logger)
	if err != nil {
		return err
	}
	if billingRuntime != nil {
		defer billingRuntime.Close()
		billingRuntime.Start(ctx)
		repo = wrapBillingRepository(repo, billingRuntime)
	}
	if auditRuntime != nil {
		defer auditRuntime.Close()
		auditRuntime.Start(ctx)
		repo = auditgovernance.WrapRepository(repo, auditRuntime)
	}
	ctx = access.SystemContext(ctx, "default")
	bus := events.NewWithBuffer(repo, logger, cfg.Events.SubBufferSize)
	defer bus.Close()
	svc := service.NewFileService(store, repo, logger).WithAuthorizer(accessManager).WithEventSink(bus)
	if !cfg.Access.DeleteFailClosed {
		svc.WithDeleteFailOpen(true)
	}
	if billingRuntime != nil {
		svc.WithUsageAccountant(billingRuntime)
	}
	embedder := buildEmbedder(cfg, logger)
	llm := buildLLM(cfg, logger)
	var search *ai.Search
	if embedder != nil {
		search = ai.NewSearch(repo, embedder, logger)
		if accessManager != nil {
			search.WithHitAuthorizer(svc)
		}
		if reranker := buildReranker(cfg, logger); reranker != nil {
			search.WithReranker(reranker)
		}
	}
	var chat *ai.Chat
	if search != nil && llm != nil {
		chat = ai.NewChat(search, llm, repo, logger).
			WithPricing(cfg.AI.ChatCostPromptPer1K, cfg.AI.ChatCostCompletionPer1K).
			WithBudget(cfg.AI.TenantDailyBudgetUSD)
		if cfg.AI.PerTenantBudgets {
			chat.WithPerTenantBudgets()
		}
	}
	server := mcp.NewServer(svc, repo, search, "default", logger)
	if chat != nil {
		server.WithChat(chat)
	}
	logger.Info("mcp stdio server starting")
	return mcp.ServeStdio(ctx, server, os.Stdin, os.Stdout)
}

// Compile-time interface satisfaction checks.
var _ service.ChunkCleaner = (*ai.Indexer)(nil)
