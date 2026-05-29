package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/aero-vault/aero-vault/internal/ai"
	"github.com/aero-vault/aero-vault/internal/antivirus"
	"github.com/aero-vault/aero-vault/internal/api/rest"
	"github.com/aero-vault/aero-vault/internal/api/s3compat"
	dav "github.com/aero-vault/aero-vault/internal/api/webdav"
	"github.com/aero-vault/aero-vault/internal/auth"
	"github.com/aero-vault/aero-vault/internal/cli"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/events"
	"github.com/aero-vault/aero-vault/internal/jobs"
	"github.com/aero-vault/aero-vault/internal/mcp"
	"github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/reconcile"
	"github.com/aero-vault/aero-vault/internal/replication"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
	"github.com/aero-vault/aero-vault/internal/telemetry"
	"github.com/aero-vault/aero-vault/internal/webui"
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

	shutdownOtel, err := telemetry.Setup(ctx, "aero-vault", logger)
	if err != nil {
		logger.Warn("otel setup failed; continuing without", "err", err)
		shutdownOtel = func(context.Context) error { return nil }
	}

	store, err := buildStorage(ctx, cfg)
	if err != nil {
		return fmt.Errorf("init storage: %w", err)
	}
	logger.Info("storage ready", "backend", store.Backend(), "sse", cfg.Storage.Local.SSEKey != "")

	repo, err := repository.Open(ctx, cfg.DB.Driver, cfg.DB.DSN)
	if err != nil {
		return fmt.Errorf("open repository: %w", err)
	}
	defer repo.Close()
	if err := repo.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	bus := events.New(repo, logger)
	// Optional cross-instance event transport (multi-replica). Opt-in; requires
	// Postgres. NOT exercised by CI — default ("") keeps the in-process bus.
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

	embedder := buildEmbedder(cfg, logger)
	if cfg.AI.EmbedCacheSize > 0 {
		embedder = ai.NewCachingEmbedder(embedder, cfg.AI.EmbedCacheSize)
	}
	llm := buildLLM(cfg, logger)
	reranker := buildReranker(cfg, logger)

	svc := service.NewFileService(store, repo, logger).WithEventSink(bus)

	// Shared background job pool, used by the indexer and the antivirus worker.
	// Handlers register below; the pool starts once everything is wired.
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
		search = ai.NewSearch(repo, embedder, logger)
		// Optional pgvector ANN backend (opt-in; requires Postgres + vector
		// extension). NOT exercised by CI — default keeps brute-force retrieval.
		if cfg.AI.VectorBackend == "pgvector" && cfg.AI.VectorDSN != "" {
			if vi, err := ai.OpenPgVectorIndex(ctx, cfg.AI.VectorDSN, ai.PgVectorOptions{}); err != nil {
				logger.Warn("pgvector index disabled (open failed); using brute-force", "err", err)
			} else {
				search.WithVectorIndex(vi)
				logger.Info("pgvector vector index enabled (requires Postgres + vector ext; unverified in CI)")
			}
		}
		// Optional Postgres FTS lexical backend (opt-in; reuses AI_VECTOR_DSN).
		if cfg.AI.LexicalBackend == "pgfts" && cfg.AI.VectorDSN != "" {
			if li, err := ai.OpenPgFTSIndex(ctx, cfg.AI.VectorDSN, ai.PgFTSOptions{}); err != nil {
				logger.Warn("pgfts lexical index disabled (open failed); using in-process BM25", "err", err)
			} else {
				search.WithLexicalIndex(li)
				logger.Info("pgfts lexical index enabled (requires Postgres; unverified in CI)")
			}
		}
		// Optional hot-result cache (opt-in; default OFF). TTL bounds staleness.
		if cfg.AI.SearchCacheSize > 0 {
			search.WithResultCache(cfg.AI.SearchCacheSize, time.Duration(cfg.AI.SearchCacheTTLSeconds)*time.Second)
		}
		if cfg.AI.HybridSearch {
			bm := ai.NewBM25()
			search.WithBM25(bm)
			go func() {
				_ = bm.BuildFromRepo(ctx, repo, "default")
				t := time.NewTicker(30 * time.Second)
				defer t.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-t.C:
						_ = bm.BuildFromRepo(ctx, repo, "default")
					}
				}
			}()
		}
		if reranker != nil {
			search.WithReranker(reranker)
			logger.Info("reranker enabled", "model", reranker.Name())
		}
		if llm != nil {
			chat = ai.NewChat(search, llm, repo, logger).
				WithPricing(cfg.AI.ChatCostPromptPer1K, cfg.AI.ChatCostCompletionPer1K).
				WithBudget(cfg.AI.TenantDailyBudgetUSD)
			agent = ai.NewAgent(svc, search, llm, repo, logger)
			logger.Info("rag chat + agent enabled", "llm", llm.Name())
		}
		// Indexer can be wrapped by the remote extractor if configured.
		extractor := ai.Extractor(ai.NewDefaultExtractor())
		if cfg.AI.ExtractorEndpoint != "" {
			extractor = ai.NewRemoteExtractor(cfg.AI.ExtractorEndpoint, cfg.AI.ExtractorAPIKey, extractor)
			logger.Info("remote extractor enabled", "endpoint", cfg.AI.ExtractorEndpoint)
		}
		indexer := ai.NewIndexer(repo, store, extractor, ai.NewChunker(), embedder, logger)
		if cfg.AI.PIIScan {
			indexer.WithPII(ai.NewPIIDetector(), cfg.AI.PIIRedact)
			logger.Info("pii scan enabled", "redact", cfg.AI.PIIRedact)
		}

		// The indexer becomes an event→job bridge; the shared pool runs the heavy
		// extract/embed work with durable retry. Without a pool it stays inline.
		if jobReg != nil {
			jobReg.Register(ai.JobIndexObject, func(ctx context.Context, job repository.Job) error {
				id, err := ai.DecodeObjectID(job.Payload)
				if err != nil {
					return err
				}
				return indexer.IndexObjectByID(ctx, id)
			})
			jobReg.Register(ai.JobDeleteChunks, func(ctx context.Context, job repository.Job) error {
				id, err := ai.DecodeObjectID(job.Payload)
				if err != nil {
					return err
				}
				return indexer.DeleteObjectChunks(ctx, id)
			})
			indexer.WithQueue(jobQueue)
		}

		go indexer.Run(ctx, bus.Subscribe())
		logger.Info("indexer started", "embedder", embedder.Name(), "dim", embedder.Dimensions(), "hybrid", cfg.AI.HybridSearch)
		if cfg.AI.ReindexStaleOnStart {
			go func() {
				if n, err := indexer.ReindexStale(ctx, "default", 1000); err != nil {
					logger.Warn("reindex-stale-on-start failed", "err", err)
				} else if n > 0 {
					logger.Info("reindex-stale-on-start complete", "reindexed", n)
				}
			}()
		}
	}

	// Antivirus scanning (independent of AI; requires the job pool).
	if cfg.Antivirus.Enabled && jobReg != nil {
		scanner := buildScanner(cfg, logger)
		avw := antivirus.NewWorker(repo, store, scanner, jobQueue, cfg.Antivirus.Quarantine, logger)
		jobReg.Register(antivirus.JobScan, func(ctx context.Context, job repository.Job) error {
			id, err := antivirus.DecodeObjectID(job.Payload)
			if err != nil {
				return err
			}
			return avw.ScanObjectByID(ctx, id)
		})
		go avw.Run(ctx, bus.Subscribe())
		logger.Info("antivirus enabled", "scanner", scanner.Name(), "quarantine", cfg.Antivirus.Quarantine)
	}

	// Cross-region replication to a secondary backend (independent of AI;
	// requires the job pool).
	if cfg.Replication.Enabled && jobReg != nil {
		replica, err := buildStorageFrom(ctx, cfg.Replication.Storage)
		if err != nil {
			return fmt.Errorf("build replica storage: %w", err)
		}
		rw := replication.NewWorker(repo, store, replica, jobQueue, logger)
		jobReg.Register(replication.JobReplicate, func(ctx context.Context, job repository.Job) error {
			id, err := replication.DecodeObjectID(job.Payload)
			if err != nil {
				return err
			}
			return rw.ReplicateObjectByID(ctx, id)
		})
		go rw.Run(ctx, bus.Subscribe())
		logger.Info("replication enabled", "replica_backend", replica.Backend())
	}

	// Start the shared job pool once all handlers are registered.
	if jobReg != nil {
		go jobs.NewPool(repo, jobReg, cfg.Jobs.Workers, logger).Run(ctx)
		logger.Info("job pool started", "workers", cfg.Jobs.Workers)
	}

	if cfg.Events.WebhookURL != "" {
		wh := events.NewWebhook(cfg.Events.WebhookURL, logger).
			WithSecret(cfg.Events.WebhookSecret).
			WithRetryStore(repo)
		go wh.Run(ctx, bus.Subscribe())
		go wh.RetryLoop(ctx)
		logger.Info("event webhook enabled", "url", cfg.Events.WebhookURL, "signed", cfg.Events.WebhookSecret != "")
	}

	if cfg.Reconcile.IntervalMinutes > 0 {
		j := reconcile.New(repo, store, time.Duration(cfg.Reconcile.IntervalMinutes)*time.Minute,
			cfg.Reconcile.DeleteOrphanBlobs,
			time.Duration(cfg.Reconcile.OrphanGraceMinutes)*time.Minute,
			cfg.Reconcile.Tenants, logger)
		lf := reconcile.NewLifecycle(repo, store, time.Duration(cfg.Reconcile.IntervalMinutes)*time.Minute, logger)
		var rg *reconcile.RetentionJob
		if cfg.Reconcile.RetentionDays > 0 || cfg.Reconcile.IdempotencyTTLHours > 0 {
			rg = reconcile.NewRetention(repo, store, time.Duration(cfg.Reconcile.IntervalMinutes)*time.Minute,
				time.Duration(cfg.Reconcile.RetentionDays)*24*time.Hour, logger)
			if cfg.Reconcile.IdempotencyTTLHours > 0 {
				rg.WithIdempotencyTTL(time.Duration(cfg.Reconcile.IdempotencyTTLHours) * time.Hour)
			}
		}
		if cfg.Reconcile.ClusterSingleton {
			instanceID := uuid.NewString()
			j.WithClusterSingleton(instanceID)
			lf.WithClusterSingleton(instanceID)
			if rg != nil {
				rg.WithClusterSingleton(instanceID)
			}
		}
		go j.Run(ctx)
		go lf.Run(ctx)
		if rg != nil {
			go rg.Run(ctx)
		}
		logger.Info("reconcile + lifecycle jobs started",
			"interval_minutes", cfg.Reconcile.IntervalMinutes,
			"delete_orphan_blobs", cfg.Reconcile.DeleteOrphanBlobs,
			"orphan_grace_minutes", cfg.Reconcile.OrphanGraceMinutes,
			"tenants", cfg.Reconcile.Tenants,
			"cluster_singleton", cfg.Reconcile.ClusterSingleton,
			"retention_days", cfg.Reconcile.RetentionDays)
	}

	authReg, err := auth.Parse(cfg.Auth.Keys)
	if err != nil {
		return fmt.Errorf("parse auth keys: %w", err)
	}
	if cfg.Auth.JWTSecret != "" {
		authReg.WithJWT(cfg.Auth.JWTSecret)
	}
	if cfg.Auth.AnonymousPublicRead {
		authReg.WithAnonymousPublicRead(true)
		logger.Info("anonymous public-read enabled (ACL-gated object GET/HEAD)")
	}
	if sv, err := auth.ParseSigV4Credentials(cfg.Auth.SigV4Credentials); err != nil {
		return fmt.Errorf("parse sigv4 credentials: %w", err)
	} else if sv != nil {
		authReg.WithSigV4(sv)
		logger.Info("s3 sigv4 verification enabled")
	}
	if cfg.Auth.PersistKeys {
		authReg.WithStore(apiKeyStore{repo: repo})
		logger.Info("persistent API keys enabled (hashed, repo-backed)")
		if cfg.Auth.KeyCacheTTLSeconds > 0 {
			authReg.WithKeyCache(time.Duration(cfg.Auth.KeyCacheTTLSeconds)*time.Second, 4096)
			logger.Info("persisted API-key lookup cache enabled", "ttl_seconds", cfg.Auth.KeyCacheTTLSeconds)
		}
	}
	if authReg.Enabled() {
		logger.Info("auth enabled", "api_keys", len(authReg.ListKeys(ctx)), "jwt", cfg.Auth.JWTSecret != "")
	}

	rl := middleware.NewRateLimiter(cfg.RateLimit.RPS, cfg.RateLimit.Burst)

	// Prometheus exporter (optional second metric reader)
	var promHandler http.Handler
	if cfg.Telemetry.PrometheusEnabled {
		h, err := telemetry.EnablePrometheus()
		if err != nil {
			logger.Warn("prometheus exporter failed", "err", err)
		}
		promHandler = h
		logger.Info("prometheus /metrics enabled")
	}

	// Observable domain gauges (read on each scrape): pending job depth and
	// per-tenant storage usage. Registered after the meter provider is installed.
	telemetry.RegisterQueueDepthGauge(func(ctx context.Context) int64 {
		n, _ := repo.CountJobsByStatus(ctx, "pending")
		return int64(n)
	})
	telemetry.RegisterStorageGauges(func(ctx context.Context) []telemetry.TenantStorage {
		qs, _ := repo.ListTenantQuotas(ctx)
		out := make([]telemetry.TenantStorage, 0, len(qs))
		for _, q := range qs {
			out = append(out, telemetry.TenantStorage{Tenant: q.TenantID, Bytes: q.UsedBytes, Objects: q.UsedObjects})
		}
		return out
	})

	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	r.Get("/readyz", func(w http.ResponseWriter, req *http.Request) {
		if err := repo.Ping(req.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	if promHandler != nil {
		r.Method(http.MethodGet, "/metrics", promHandler)
	}
	r.Get("/openapi.json", rest.OpenAPISpecHandler())
	r.Get("/docs", rest.SwaggerUIHandler())

	r.Mount("/v1", rest.NewRouter(svc, repo, search, chat, agent, bus, authReg, logger))
	if cfg.S3Compat.Prefix != "" {
		r.Mount(cfg.S3Compat.Prefix, s3compat.NewRouter(svc, logger))
	}
	mcpServer := mcp.NewServer(svc, repo, search, "default", logger)
	r.Method(http.MethodPost, "/mcp", mcp.HTTPHandler(mcpServer))

	if cfg.WebUI.Enabled {
		r.Mount("/ui", webui.Handler())
	}

	// WebDAV outside chi so PROPFIND/MKCOL work.
	var davH http.Handler
	if cfg.WebDAV.Prefix != "" {
		davH = dav.Handler(cfg.WebDAV.Prefix, svc, logger)
	}

	dispatcher := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if davH != nil && cfg.WebDAV.Prefix != "" {
			p := req.URL.Path
			if p == cfg.WebDAV.Prefix || strings.HasPrefix(p, cfg.WebDAV.Prefix+"/") {
				davH.ServeHTTP(w, req)
				return
			}
		}
		r.ServeHTTP(w, req)
	})
	// Middleware order (request flow, outermost first):
	//   RequestID → CORS → Auth → Tenant → RateLimit → OTel → Recoverer → AccessLog
	// Auth must run before Tenant — the API-key path pins the tenant header,
	// and Tenant middleware reads that header into the request context.
	var finalHandler http.Handler = dispatcher
	for _, m := range []func(http.Handler) http.Handler{
		middleware.AccessLog(logger), // innermost
		middleware.Recoverer(logger),
		telemetry.HTTPMiddleware("aero-vault"),
		rl.Middleware(),
		middleware.Tenant,
		authReg.Middleware(),
		middleware.CORS(middleware.CORSConfig{
			AllowedOrigins: cfg.CORS.AllowedOrigins,
			AllowedHeaders: cfg.CORS.AllowedHeaders,
			AllowedMethods: cfg.CORS.AllowedMethods,
			ExposeHeaders:  []string{"ETag", "X-Request-ID", "X-Version-Id"},
		}),
		middleware.RequestID, // outermost
	} {
		finalHandler = m(finalHandler)
	}

	srv := &http.Server{
		Addr:              cfg.App.Addr,
		Handler:           finalHandler,
		ReadHeaderTimeout: 15 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.App.Addr,
			"auth", authReg.Enabled(), "webdav", cfg.WebDAV.Prefix,
			"ui", cfg.WebUI.Enabled, "prom", cfg.Telemetry.PrometheusEnabled,
			"chat", chat != nil, "search", search != nil)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown requested")
	case err := <-errCh:
		if err != nil {
			return err
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	bus.Close()
	_ = shutdownOtel(shutdownCtx)
	return nil
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
	svc := service.NewFileService(store, repo, logger)
	embedder := buildEmbedder(cfg, logger)
	var search *ai.Search
	if embedder != nil {
		search = ai.NewSearch(repo, embedder, logger)
	}
	server := mcp.NewServer(svc, repo, search, "default", logger)
	logger.Info("mcp stdio server starting")
	return mcp.ServeStdio(ctx, server, os.Stdin, os.Stdout)
}

func buildStorage(ctx context.Context, cfg *config.Config) (storage.Storage, error) {
	return buildStorageFrom(ctx, cfg.Storage)
}

func buildStorageFrom(ctx context.Context, sc config.StorageConfig) (storage.Storage, error) {
	fc := storage.FactoryConfig{Kind: storage.BackendKind(sc.Backend)}
	switch fc.Kind {
	case storage.BackendLocal:
		fc.Local = storage.LocalConfig{
			Root:      sc.Local.Root,
			PublicURL: sc.Local.PublicURL,
			SignKey:   sc.Local.SignKey,
			SSEKey:    sc.Local.SSEKey,
		}
	case storage.BackendS3:
		fc.S3 = storage.S3Config{
			Endpoint:       sc.S3.Endpoint,
			Region:         sc.S3.Region,
			Bucket:         sc.S3.Bucket,
			AccessKey:      sc.S3.AccessKey,
			SecretKey:      sc.S3.SecretKey,
			ForcePathStyle: sc.S3.ForcePathStyle,
		}
	case storage.BackendOSS:
		fc.OSS = storage.OSSConfig{
			Endpoint:  sc.OSS.Endpoint,
			Bucket:    sc.OSS.Bucket,
			AccessKey: sc.OSS.AccessKey,
			SecretKey: sc.OSS.SecretKey,
		}
	case storage.BackendCOS:
		fc.COS = storage.COSConfig{
			BucketURL: sc.COS.BucketURL,
			SecretID:  sc.COS.SecretID,
			SecretKey: sc.COS.SecretKey,
		}
	}
	return storage.NewFromConfig(ctx, fc)
}

func buildEmbedder(cfg *config.Config, logger *slog.Logger) ai.Embedder {
	if !cfg.AI.Enabled {
		return nil
	}
	if cfg.AI.Provider == "http" && cfg.AI.Endpoint != "" {
		logger.Info("embedder: http", "endpoint", cfg.AI.Endpoint, "model", cfg.AI.Model)
		return ai.NewHTTPEmbedder(cfg.AI.Endpoint, cfg.AI.Model, cfg.AI.APIKey, cfg.AI.Dim)
	}
	logger.Info("embedder: hash (built-in)", "dim", cfg.AI.Dim)
	return ai.NewHashEmbedder(cfg.AI.Dim)
}

func buildScanner(cfg *config.Config, logger *slog.Logger) antivirus.Scanner {
	if cfg.Antivirus.Provider == "http" && cfg.Antivirus.Endpoint != "" {
		logger.Info("antivirus: http scanner", "endpoint", cfg.Antivirus.Endpoint)
		return antivirus.NewHTTPScanner(cfg.Antivirus.Endpoint, cfg.Antivirus.APIKey)
	}
	logger.Info("antivirus: built-in signature scanner")
	return antivirus.NewSignatureScanner(nil)
}

func buildLLM(cfg *config.Config, logger *slog.Logger) ai.LLM {
	if !cfg.AI.Enabled {
		return nil
	}
	if cfg.AI.ChatProvider == "http" && cfg.AI.ChatEndpoint != "" {
		logger.Info("llm: http", "endpoint", cfg.AI.ChatEndpoint, "model", cfg.AI.ChatModel)
		return ai.NewHTTPLLM(cfg.AI.ChatEndpoint, cfg.AI.ChatModel, cfg.AI.ChatAPIKey)
	}
	if cfg.AI.ChatProvider == "mock" {
		logger.Info("llm: mock (echo)")
		return ai.MockLLM{}
	}
	return nil
}

func buildReranker(cfg *config.Config, logger *slog.Logger) ai.Reranker {
	if cfg.AI.RerankProvider == "http" && cfg.AI.RerankEndpoint != "" {
		logger.Info("reranker: http", "endpoint", cfg.AI.RerankEndpoint, "model", cfg.AI.RerankModel)
		return ai.NewHTTPReranker(cfg.AI.RerankEndpoint, cfg.AI.RerankModel, cfg.AI.RerankAPIKey)
	}
	if cfg.AI.RerankProvider == "heuristic" {
		logger.Info("reranker: heuristic (dep-free fallback)")
		return ai.HeuristicReranker{}
	}
	return nil
}

// apiKeyStore adapts the repository to auth.PersistentStore, keeping the auth
// package decoupled from the repository types. Wired only when AUTH_PERSIST_KEYS
// is set.
type apiKeyStore struct{ repo repository.Repository }

func (s apiKeyStore) PutAPIKey(ctx context.Context, k auth.PersistedKey) error {
	return s.repo.PutAPIKey(ctx, repository.APIKeyRecord{
		TokenHash: k.TokenHash, TenantID: k.TenantID, Scopes: k.Scopes,
		Label: k.Label, CreatedAt: k.CreatedAt, ExpiresAt: k.ExpiresAt, LastUsedAt: k.LastUsedAt,
	})
}

func (s apiKeyStore) GetAPIKeyByHash(ctx context.Context, hash string) (auth.PersistedKey, bool, error) {
	r, found, err := s.repo.GetAPIKeyByHash(ctx, hash)
	if err != nil || !found {
		return auth.PersistedKey{}, found, err
	}
	return auth.PersistedKey{
		TokenHash: r.TokenHash, TenantID: r.TenantID, Scopes: r.Scopes,
		Label: r.Label, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, LastUsedAt: r.LastUsedAt,
	}, true, nil
}

func (s apiKeyStore) DeleteAPIKeyByHash(ctx context.Context, hash string) (bool, error) {
	return s.repo.DeleteAPIKeyByHash(ctx, hash)
}

func (s apiKeyStore) ListAPIKeys(ctx context.Context, tenant string) ([]auth.PersistedKey, error) {
	recs, err := s.repo.ListAPIKeys(ctx, tenant)
	if err != nil {
		return nil, err
	}
	out := make([]auth.PersistedKey, 0, len(recs))
	for _, r := range recs {
		out = append(out, auth.PersistedKey{
			TokenHash: r.TokenHash, TenantID: r.TenantID, Scopes: r.Scopes,
			Label: r.Label, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, LastUsedAt: r.LastUsedAt,
		})
	}
	return out, nil
}

func (s apiKeyStore) TouchAPIKey(ctx context.Context, hash, when string) error {
	return s.repo.TouchAPIKey(ctx, hash, when)
}
