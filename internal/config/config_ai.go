package config

type AIConfig struct {
	Enabled        bool
	Provider       string
	Endpoint       string
	Model          string
	APIKey         string
	Dim            int
	HybridSearch   bool
	EmbedCacheSize int // >0 wraps the embedder in a bounded in-memory cache

	SearchCacheSize       int  // >0 enables a bounded, TTL'd hot-result cache for identical repeated queries
	SearchCacheTTLSeconds int  // TTL bounding staleness of cached search results
	ReindexStaleOnStart   bool // re-index objects whose chunks use a different embed model (after embedder change)

	// Vector retrieval backend. "" = brute-force (default); "pgvector" = ANN via
	// Postgres pgvector; "qdrant" = an external Qdrant vector store. Each is
	// opt-in; "" keeps the brute-force repository scan.
	VectorBackend string
	VectorDSN     string
	// Qdrant external vector store (used when VectorBackend == "qdrant"). The
	// adapter implements both the read (VectorIndex) and write (ChunkSink) seams.
	VectorURL        string
	VectorAPIKey     string
	VectorCollection string
	// Lexical retrieval backend. "" = in-process BM25 (default); "pgfts" =
	// Postgres full-text search. Reuses VectorDSN for its connection; opt-in.
	LexicalBackend string

	ExtractorEndpoint string
	ExtractorAPIKey   string

	ChatProvider string // "http" | "mock" | ""
	ChatEndpoint string
	ChatModel    string
	ChatAPIKey   string

	// Estimated cost accounting (USD per 1000 tokens; 0 = don't price).
	ChatCostPromptPer1K     float64
	ChatCostCompletionPer1K float64
	// Per-tenant daily AI spend cap (USD; 0 = unlimited). Enforced at the chat seam.
	TenantDailyBudgetUSD float64
	// PerTenantBudgets lets each tenant override TenantDailyBudgetUSD via its
	// stored quota row (set through the admin budget endpoint).
	PerTenantBudgets bool

	RerankProvider string // "http" | "heuristic" | ""
	RerankEndpoint string
	RerankModel    string
	RerankAPIKey   string

	PIIScan   bool
	PIIRedact bool

	AgentMaxSteps int
	ChunkWindow   int
	ChunkOverlap  int
	// DegradedMode forces all AI endpoints to return 503 immediately without
	// attempting any provider call. Useful for draining traffic during outages.
	DegradedMode bool
}
