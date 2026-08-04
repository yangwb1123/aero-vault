package main

import (
	"context"
	"log/slog"

	"github.com/aero-vault/aero-vault/internal/access"
	"time"

	"github.com/aero-vault/aero-vault/internal/ai"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/events"
	"github.com/aero-vault/aero-vault/internal/jobs"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

func buildAIComponents(ctx context.Context, cfg *config.Config, logger *slog.Logger, repo repository.Repository, store storage.Storage, bus *events.Bus, svc *service.FileService, embedder ai.Embedder, llm ai.LLM, reranker ai.Reranker, jobReg *jobs.Registry, jobQueue *jobs.Queue) (*ai.Search, *ai.Chat, *ai.Agent) {
	search := ai.NewSearch(repo, embedder, logger)
	if cfg.Access.Enabled {
		search.WithHitAuthorizer(svc)
	}
	qdrantIndex := setupVectorIndexes(ctx, cfg, search, embedder, logger)
	setupLexicalCache(ctx, cfg, search, logger)
	bm := setupBM25Search(ctx, cfg, repo, search)
	if reranker != nil {
		search.WithReranker(reranker)
		logger.Info("reranker enabled", "model", reranker.Name())
	}
	chat, agent := setupChatAndAgent(cfg, svc, search, llm, repo, logger)
	buildIndexer(ctx, cfg, logger, repo, store, svc, embedder, bm, qdrantIndex, jobReg, jobQueue, bus)
	return search, chat, agent
}

func setupVectorIndexes(ctx context.Context, cfg *config.Config, search *ai.Search, embedder ai.Embedder, logger *slog.Logger) *ai.QdrantIndex {
	var qdrantIndex *ai.QdrantIndex
	if cfg.AI.VectorBackend == "pgvector" && cfg.AI.VectorDSN != "" {
		if vi, err := ai.OpenPgVectorIndex(ctx, cfg.AI.VectorDSN, ai.PgVectorOptions{}); err != nil {
			logger.Warn("pgvector index disabled (open failed); using brute-force", "err", err)
		} else {
			search.WithVectorIndex(vi)
			logger.Info("pgvector vector index enabled (requires Postgres + vector ext; unverified in CI)")
		}
	}
	if cfg.AI.VectorBackend == "qdrant" && cfg.AI.VectorURL != "" {
		qdrantIndex = ai.NewQdrantIndex(ai.QdrantOptions{
			BaseURL: cfg.AI.VectorURL, APIKey: cfg.AI.VectorAPIKey, Collection: cfg.AI.VectorCollection,
		})
		search.WithVectorIndex(qdrantIndex)
		if err := qdrantIndex.EnsureCollection(ctx, embedder.Dimensions()); err != nil {
			logger.Warn("qdrant ensure collection failed (continuing)", "err", err)
		}
		logger.Info("qdrant vector index enabled (external store; unverified in CI)", "collection", cfg.AI.VectorCollection)
	}
	return qdrantIndex
}

func setupLexicalCache(ctx context.Context, cfg *config.Config, search *ai.Search, logger *slog.Logger) {
	if cfg.AI.LexicalBackend == "pgfts" && cfg.AI.VectorDSN != "" {
		if li, err := ai.OpenPgFTSIndex(ctx, cfg.AI.VectorDSN, ai.PgFTSOptions{}); err != nil {
			logger.Warn("pgfts lexical index disabled (open failed); using in-process BM25", "err", err)
		} else {
			search.WithLexicalIndex(li)
			logger.Info("pgfts lexical index enabled (requires Postgres; unverified in CI)")
		}
	}
	if cfg.AI.SearchCacheSize > 0 {
		search.WithResultCache(cfg.AI.SearchCacheSize, time.Duration(cfg.AI.SearchCacheTTLSeconds)*time.Second)
	}
}

func setupBM25Search(ctx context.Context, cfg *config.Config, repo repository.Repository, search *ai.Search) *ai.BM25 {
	var bm *ai.BM25
	if cfg.AI.HybridSearch {
		bm = ai.NewBM25()
		search.WithBM25(bm)
		warmTenants := aiStartupTenants(cfg)
		go func() {
			for _, t := range warmTenants {
				_ = bm.BuildFromRepo(ctx, repo, t)
			}
		}()
	}
	return bm
}

func setupChatAndAgent(cfg *config.Config, svc *service.FileService, search *ai.Search, llm ai.LLM, repo repository.Repository, logger *slog.Logger) (*ai.Chat, *ai.Agent) {
	var chat *ai.Chat
	var agent *ai.Agent
	if llm != nil {
		chat = ai.NewChat(search, llm, repo, logger).
			WithPricing(cfg.AI.ChatCostPromptPer1K, cfg.AI.ChatCostCompletionPer1K).
			WithBudget(cfg.AI.TenantDailyBudgetUSD)
		if cfg.AI.PerTenantBudgets {
			chat.WithPerTenantBudgets()
		}
		agent = ai.NewAgent(svc, search, llm, repo, logger)
		agent.MaxSteps = cfg.AI.AgentMaxSteps
		logger.Info("rag chat + agent enabled", "llm", llm.Name())
	}
	return chat, agent
}

func buildIndexer(ctx context.Context, cfg *config.Config, logger *slog.Logger, repo repository.Repository, store storage.Storage, svc *service.FileService, embedder ai.Embedder, bm *ai.BM25, qdrantIndex *ai.QdrantIndex, jobReg *jobs.Registry, jobQueue *jobs.Queue, bus *events.Bus) {
	extractor := ai.Extractor(ai.NewDefaultExtractor())
	if cfg.AI.ExtractorEndpoint != "" {
		extractor = ai.NewRemoteExtractor(cfg.AI.ExtractorEndpoint, cfg.AI.ExtractorAPIKey, extractor)
		logger.Info("remote extractor enabled", "endpoint", cfg.AI.ExtractorEndpoint)
	}
	indexer := ai.NewIndexer(repo, store, extractor, &ai.Chunker{
		Window: cfg.AI.ChunkWindow, Overlap: cfg.AI.ChunkOverlap,
	}, embedder, logger).WithObjectTagger(svc)
	if bm != nil {
		indexer.WithChunkSink(bm)
	}
	if qdrantIndex != nil {
		indexer.WithChunkSink(qdrantIndex)
	}
	if cfg.AI.PIIScan {
		indexer.WithPII(ai.NewPIIDetector(), cfg.AI.PIIRedact)
		logger.Info("pii scan enabled", "redact", cfg.AI.PIIRedact)
	}
	svc.WithChunkCleaner(indexer)
	registerIndexerJobs(jobReg, jobQueue, indexer)
	idxSub, _ := bus.Subscribe()
	systemCtx := access.SystemContext(ctx, "*")
	go indexer.Run(systemCtx, idxSub)

	logger.Info("indexer started", "embedder", embedder.Name(), "dim", embedder.Dimensions(), "hybrid", cfg.AI.HybridSearch)
	startReindexOnStartup(systemCtx, cfg, indexer, logger)
}

func registerIndexerJobs(jobReg *jobs.Registry, jobQueue *jobs.Queue, indexer *ai.Indexer) {
	if jobReg == nil {
		return
	}
	jobReg.Register(ai.JobIndexObject, func(ctx context.Context, job repository.Job) error {
		id, err := ai.DecodeObjectID(job.Payload)
		if err != nil {
			return err
		}
		return indexer.IndexObjectByID(access.SystemContext(ctx, job.TenantID), id)
	})
	jobReg.Register(ai.JobDeleteChunks, func(ctx context.Context, job repository.Job) error {
		id, err := ai.DecodeObjectID(job.Payload)
		if err != nil {
			return err
		}
		return indexer.DeleteObjectChunks(access.SystemContext(ctx, job.TenantID), id)
	})
	indexer.WithQueue(jobQueue)
}

func startReindexOnStartup(ctx context.Context, cfg *config.Config, indexer *ai.Indexer, logger *slog.Logger) {
	if cfg.AI.ReindexStaleOnStart {
		go func() {
			total := 0
			for _, tenant := range aiStartupTenants(cfg) {
				n, err := indexer.ReindexStale(ctx, tenant, 1000)
				if err != nil {
					logger.Warn("reindex-stale-on-start failed", "tenant", tenant, "err", err)
					continue
				}
				total += n
			}
			if total > 0 {
				logger.Info("reindex-stale-on-start complete", "reindexed", total)
			}
		}()
	}
}

func aiStartupTenants(cfg *config.Config) []string {
	if len(cfg.Reconcile.Tenants) == 0 {
		return []string{"default"}
	}
	seen := make(map[string]struct{}, len(cfg.Reconcile.Tenants))
	tenants := make([]string, 0, len(cfg.Reconcile.Tenants))
	for _, tenant := range cfg.Reconcile.Tenants {
		if tenant == "" {
			continue
		}
		if _, ok := seen[tenant]; ok {
			continue
		}
		seen[tenant] = struct{}{}
		tenants = append(tenants, tenant)
	}
	if len(tenants) == 0 {
		return []string{"default"}
	}
	return tenants
}
