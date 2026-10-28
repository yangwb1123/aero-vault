package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/ai"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/events"
	"github.com/aero-vault/aero-vault/internal/jobs"
)

// spaceStartup owns the prepared handles through preflight, backend
// initialization and actual starts. It never calls the legacy eager AI builder.
// Indexer/BM25's retained exact-identity support is not runtime Space admission.
type spaceStartup struct {
	cfg         *config.Config
	logger      *slog.Logger
	infra       *preparedInfrastructure
	assembly    *spaceAssembly
	registry    *jobs.Registry
	queue       *jobs.Queue
	search      *ai.Search
	chat        *ai.Chat
	agent       *ai.Agent
	embedder    ai.Embedder
	bm25        *ai.BM25
	qdrant      *ai.QdrantIndex
	indexer     *ai.Indexer
	pgvector    *ai.PgVectorIndex
	pgfts       *ai.PgFTSIndex
	workers     *backgroundWorkers
	transport   *events.PostgresTransport
	initialized bool
	started     bool
}

func prepareSpaceStartup(cfg *config.Config, logger *slog.Logger, infra *preparedInfrastructure, a *spaceAssembly) (*spaceStartup, error) {
	if cfg == nil || infra == nil || a == nil || a.svc == nil {
		return nil, spaceStartupFault(errors.New("missing startup composition"))
	}
	if err := infra.readers.validate(); err != nil {
		return nil, err
	}
	p := &spaceStartup{cfg: cfg, logger: logger, infra: infra, assembly: a}
	if cfg.Jobs.Workers > 0 {
		p.registry = jobs.NewRegistry()
		p.queue = jobs.NewQueue(a.repo)
		if cfg.Jobs.MaxDepth > 0 {
			p.queue.WithMaxDepth(cfg.Jobs.MaxDepth)
		}
	}
	p.prepareAI()
	var err error
	p.workers, err = prepareBackgroundWorkers(cfg, logger, a.repo, infra.store, infra.bus,
		p.registry, p.queue, a.svc, infra.readers)
	if err != nil {
		return nil, err
	}
	p.transport = preparePostgresTransport(cfg, infra.readers)
	return p, p.validate()
}

func (p *spaceStartup) prepareAI() {
	p.embedder = buildEmbedder(p.cfg, p.logger)
	if p.embedder == nil {
		return
	}
	if p.cfg.AI.EmbedCacheSize > 0 {
		p.embedder = ai.NewCachingEmbedder(p.embedder, p.cfg.AI.EmbedCacheSize)
	}
	p.search = ai.NewSearch(p.assembly.repo, p.embedder, p.logger)
	p.infra.readers.wireSearch(p.search, p.assembly.svc)
	if p.cfg.AI.HybridSearch {
		p.bm25 = ai.NewBM25()
		p.search.WithBM25(p.bm25)
	}
	if p.cfg.AI.VectorBackend == "qdrant" && p.cfg.AI.VectorURL != "" {
		p.qdrant = ai.NewQdrantIndex(ai.QdrantOptions{
			BaseURL: p.cfg.AI.VectorURL, APIKey: p.cfg.AI.VectorAPIKey, Collection: p.cfg.AI.VectorCollection,
		})
		p.search.WithVectorIndex(p.qdrant)
	}
	if p.cfg.AI.SearchCacheSize > 0 {
		p.search.WithResultCache(p.cfg.AI.SearchCacheSize, time.Duration(p.cfg.AI.SearchCacheTTLSeconds)*time.Second)
	}
	if reranker := buildReranker(p.cfg, p.logger); reranker != nil {
		p.search.WithReranker(reranker)
		p.logger.Info("reranker enabled", "model", reranker.Name())
	}
	p.chat, p.agent = setupChatAndAgent(p.cfg, p.assembly.svc, p.search,
		buildLLM(p.cfg, p.logger), p.assembly.repo, p.logger)
	p.prepareIndexer()
}

func (p *spaceStartup) prepareIndexer() {
	extractor := ai.Extractor(ai.NewDefaultExtractor())
	if p.cfg.AI.ExtractorEndpoint != "" {
		extractor = ai.NewRemoteExtractor(p.cfg.AI.ExtractorEndpoint, p.cfg.AI.ExtractorAPIKey, extractor)
		p.logger.Info("remote extractor enabled", "endpoint", p.cfg.AI.ExtractorEndpoint)
	}
	p.indexer = ai.NewIndexer(p.assembly.repo, p.infra.store, extractor, &ai.Chunker{
		Window: p.cfg.AI.ChunkWindow, Overlap: p.cfg.AI.ChunkOverlap,
	}, p.embedder, p.logger).WithObjectTagger(p.assembly.svc)
	if p.bm25 != nil {
		p.indexer.WithChunkSink(p.bm25)
	}
	if p.qdrant != nil {
		p.indexer.WithChunkSink(p.qdrant)
	}
	if p.cfg.AI.PIIScan {
		p.indexer.WithPII(ai.NewPIIDetector(), p.cfg.AI.PIIRedact)
		p.logger.Info("pii scan enabled", "redact", p.cfg.AI.PIIRedact)
	}
	p.assembly.svc.WithChunkCleaner(p.indexer)
	registerIndexerJobs(p.registry, p.queue, p.indexer)
}

// This is capability/configuration validation only. No probes, policy reads,
// backend opens, subscriptions, warmup, polling or readiness checks occur.
func (p *spaceStartup) validate() error {
	if p == nil || p.cfg == nil || p.infra == nil || p.assembly == nil {
		return spaceStartupFault(errors.New("missing startup plan"))
	}
	if err := p.infra.readers.validate(); err != nil {
		return err
	}
	if err := p.assembly.svc.ValidateSpaceBoundary(); err != nil {
		return err
	}
	if err := p.validateAI(); err != nil {
		return err
	}
	return p.workers.validate()
}

func (p *spaceStartup) validateAI() error {
	if !p.cfg.AI.Enabled {
		return nil
	}
	if err := p.search.ValidateResourceBoundary(); err != nil {
		return err
	}
	if p.indexer == nil {
		return spaceStartupFault(errors.New("missing prepared indexer"))
	}
	return p.indexer.ValidateChunkSinks()
}

func (p *spaceStartup) initialize(ctx context.Context) error {
	if err := p.validate(); err != nil {
		return err
	}
	if p.initialized || p.started {
		return spaceStartupFault(errors.New("startup already initialized"))
	}
	if ctx == nil {
		return spaceStartupFault(errors.New("missing startup context"))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.openAIBackends(ctx)
	if err := p.workers.initialize(ctx); err != nil {
		return err
	}
	if err := p.validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.initialized = true
	return nil
}

// Preserve optional-backend fallback semantics, but only after every enabled
// consumer has passed preflight. None of these opens starts protected workers.
func (p *spaceStartup) openAIBackends(ctx context.Context) {
	if p.search == nil {
		return
	}
	if p.cfg.AI.VectorBackend == "pgvector" && p.cfg.AI.VectorDSN != "" {
		vi, err := ai.OpenPgVectorIndex(ctx, p.cfg.AI.VectorDSN, ai.PgVectorOptions{})
		if err != nil {
			p.logger.Warn("pgvector index disabled (open failed); using brute-force", "err", err)
		} else {
			p.pgvector = vi
			p.search.WithVectorIndex(vi)
			p.logger.Info("pgvector vector index enabled (requires Postgres + vector ext; unverified in CI)")
		}
	}
	if p.cfg.AI.LexicalBackend == "pgfts" && p.cfg.AI.VectorDSN != "" {
		li, err := ai.OpenPgFTSIndex(ctx, p.cfg.AI.VectorDSN, ai.PgFTSOptions{})
		if err != nil {
			p.logger.Warn("pgfts lexical index disabled (open failed); using in-process BM25", "err", err)
		} else {
			p.pgfts = li
			p.search.WithLexicalIndex(li)
			p.logger.Info("pgfts lexical index enabled (requires Postgres; unverified in CI)")
		}
	}
	if p.qdrant != nil {
		if err := p.qdrant.EnsureCollection(ctx, p.embedder.Dimensions()); err != nil {
			p.logger.Warn("qdrant ensure collection failed (continuing)", "err", err)
		}
		p.logger.Info("qdrant vector index enabled (external store; unverified in CI)", "collection", p.cfg.AI.VectorCollection)
	}
}

func (p *spaceStartup) validateInitialized() error {
	if err := p.validate(); err != nil {
		return err
	}
	if !p.initialized || p.started {
		return spaceStartupFault(errors.New("startup not initialized or already started"))
	}
	return p.workers.validateInitialized()
}

func (p *spaceStartup) start(ctx context.Context) error {
	if err := p.validateInitialized(); err != nil {
		return err
	}
	if ctx == nil {
		return spaceStartupFault(errors.New("missing startup context"))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.infra.start(ctx, p.cfg, p.logger)
	p.assembly.startRuntimes(ctx)
	if err := p.workers.start(ctx); err != nil {
		return err
	}
	startPostgresTransport(ctx, p.transport, p.infra.bus, p.logger)
	p.startAI(ctx)
	p.started = true
	return nil
}

func (p *spaceStartup) startAI(ctx context.Context) {
	if p.indexer == nil {
		return
	}
	if p.bm25 != nil {
		go func() {
			for _, tenant := range aiStartupTenants(p.cfg) {
				if err := p.bm25.BuildFromRepo(ctx, p.assembly.repo, tenant); err != nil {
					p.logger.Warn("bm25 warmup failed", "tenant", tenant, "err", err)
				}
			}
		}()
	}
	sub, _ := p.infra.bus.Subscribe()
	systemCtx := access.SystemContext(ctx, "*")
	go p.indexer.Run(systemCtx, sub)
	p.logger.Info("indexer started", "embedder", p.embedder.Name(), "dim", p.embedder.Dimensions(), "hybrid", p.cfg.AI.HybridSearch)
	startReindexOnStartup(systemCtx, p.cfg, p.indexer, p.logger)
}

func (p *spaceStartup) requireStarted() error {
	if p == nil || !p.started || !p.initialized {
		return spaceStartupFault(errors.New("server startup has not completed"))
	}
	return nil
}

func (p *spaceStartup) close() {
	p.assembly.close()
	if p.pgfts != nil {
		_ = p.pgfts.Close()
	}
	if p.pgvector != nil {
		_ = p.pgvector.Close()
	}
	p.infra.bus.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = p.infra.shutdown(ctx)
	_ = p.infra.raw.Close()
}
