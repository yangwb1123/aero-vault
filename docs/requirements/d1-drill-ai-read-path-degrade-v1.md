# 方向：D1 drill —— AI 读路径在 embedder/vector 超时时降级而非硬 500（hybrid 回退 BM25-only）

> **模块：** `internal/ai`（REST 缝验收测试落在 `internal/api/rest`）· **来源分析：** `docs/auto/analyses/internal-ai-99180452.json` · **日期：** 2026-08-08
> **评分：** 价值 9 / 风险降低 8 / 工作量 4 / 置信度 9
> **验证基准：** 工作树 = HEAD `15763e2`（B3-2 Ready decoupling 已落地——本方向即把同一 D1 原则从 readyz 延伸到 AI 读路径）。本文所有引用均对照该基准逐行验证（§2.2）；未运行测试，行号为验证时实测。
> **范围声明：** 本文是**增量行为规格**——降级行为本身尚未实现（方向文 verified_claim 已确认），全部 FR 为新契约；不触碰任何配置键、中间件链、迁移或响应 schema。

---

## 1. 问题陈述

`internal/ai` 是产品的主要读路径，却违反 B3-2/D1 原则（"读路径超时降级而非硬失败"）：**任一模态失败即中止整条查询**。

1. **`searchAndMerge` 对第一个模态错误硬失败**：hybrid 查询中，embed 或向量检索失败时 `return nil, err`（`internal/ai/search.go:309`、`:317`），**直接丢弃健康一侧的 BM25 结果**——即使词法侧完好，调用方也拿不到任何命中。
2. **REST 缝把一切 AI 错误映射为 500 `InternalError`**（`internal/api/rest/search.go:78` Search、`:174` Chat、`:215` Agent；ChatStream 发 SSE `event:error code:InternalError`）——`Search` 或 `Chat` 下任何检索失败都变成客户端可见的总失败。
3. **`mw.RequestTimeout` 注入的上下文截止时间放大该问题**：AI 路由组挂 `mw.RequestTimeout(aiTimeout)`（`internal/api/rest/router.go:253`，`aiTimeout` 来自 `REQUEST_TIMEOUT_SECONDS`，`cmd/server/main.go:156`）；embedder 是 30 秒客户端超时（`internal/ai/embedder.go:106`），一旦 embed 阶段吃满请求截止时间，整条 hybrid 查询 500。
4. **模块内降级先例已存在但未延伸到 embed/retrieve 步**：`applyRerankOrTrim` 在 reranker 失败时 warn + 原始排序（`internal/ai/search.go:291`）；readyz 缝的 B3-2 先例是 backlog 超限 → **200 + gauge 信号，绝不 503**（`cmd/server/readyz_drill_test.go:6,:224`，HEAD 已落地）。embed/retrieve 步没有任何同类保护。

**本方向要求：** hybrid 模式下，任一模态（vector 半 / lexical 半）失败 → **降级为健康模态的结果继续管线**（warn 日志 + 新遥测计数器），只有 hybrid 有另一模态可回退；纯 `vector`/`bm25` 模式错误原样上抛（失败保持可见）。

### 触发场景（真实工作流）

1. embedder 提供方 30s 超时/5xx/连接拒绝 → `POST /v1/search {"mode":"hybrid"}` → 今天 500 `InternalError`；要求：200 + BM25 命中 + warn + 计数。
2. 向量后端（pgvector/Qdrant）抖动、`SearchVectors` 报错 → hybrid 今天整体 500，词法侧完好却被丢弃；要求：200 + BM25 命中。
3. `REQUEST_TIMEOUT_SECONDS` 截止时间在 embed 阶段触发（`context.DeadlineExceeded` 沿 `Embed` 上抛）→ 今天 500；要求：上下文仍存活时降级为 BM25-only 200。

---

## 2. 现状与代码证据（已逐条验证）

### 2.1 关键路径速览

```
POST /v1/search (aih.Search, rest/search.go:40-74)
  └─ mw.RequestTimeout(aiTimeout) 上下文截止时间（router.go:253；main.go:156）
  └─ Search.Query (search.go:333)
       ├─ validate (mode 默认 "vector"；hybrid 要求 embedder+BM25 均在)
       ├─ searchAndMerge (search.go:303)   ← 硬失败点 :309/:317
       │    ├─ searchVector (search.go:168)  ← embed :171 / vindex :178 错误上抛
       │    └─ searchLexical (search.go:191) ← lexical :200 / BM25 内存索引
       ├─ filterAuthorizedHits → applyRerankOrTrim（降级先例 :291）→ recordUsage
       └─ 任一错误 → 500 InternalError（rest/search.go:78）
```

### 2.2 方向文证据验证表

| # | 方向文引用 | 验证结果 |
|---|-----------|---------|
| E1 | `search.go:300-311` — searchAndMerge 首个模态失败硬失败 | ✅ 实际 `:303-331`（漂移 ~3 行）。`:309`（vector 半）、`:317`（lexical 半）均为 `return nil, err`——`nil,err` 返回直接丢弃另一模态已收集的结果（hybrid 下二者是分步收集的） |
| E2 | `search.go:159-172` — searchVector | ✅ 实际 `:168-189`（漂移 ~9 行）。`:171` `embed query: %w`、`:178` `search chunks: %w`，两处错误直接上抛，无降级路径 |
| E3 | `search.go:284-296` — applyRerankOrTrim 降级先例 | ✅ 实际 `:285-301`。`:291` `s.logger.Warn("rerank failed; using raw order", "err", err)` + 原始排序截断——**模块内既有降级范式**（本方向的 warn 风格与之对齐） |
| E4 | `rest/search.go` — Search/Chat → 500 InternalError | ✅ `:78`（Search）、`:174`（Chat）、`:215`（Agent）`writeJSON(w, http.StatusInternalServerError, …Code:"InternalError")`；ChatStream `:214-216` SSE error 帧。AI 路由组 `router.go:250-260`，`mw.RequestTimeout(aiTimeout)` 于 `:253`；`aiTimeout = REQUEST_TIMEOUT_SECONDS`（`main.go:156`） |
| E5 | `middleware/timeout.go:14-26` | ✅ 全文 26 行；`RequestTimeout` 用 `context.WithTimeout` 注入截止时间，注释明示 *"all AI pipeline calls honour context cancellation"* |
| E6 | `embedder.go:106` — 30s 客户端超时 | ✅ 精确 `:106`：`Client: &http.Client{Timeout: 30 * time.Second}`（`rerank.go:44` 同值）。embedder 卡死时由该超时或请求截止时间先触发 |
| E7 | `telemetry/metrics.go:276` — IncIndexerSkip 模式 | ✅ 实际 `:276-281`；计数器声明 `:31`、注册 `:77`（`Int64Counter("indexer.skip_total")`）、`Add` 带 `reason` 属性 `:281`——本方向新计数器的直接镜像 |
| E8 | `rerank.go` — Reranker 失败 → 原始顺序 + warn | ✅ `HTTPReranker.Rerank` 传输错误 `:90-96`、HTTP ≥300 `:100-103` 均返回 error；消费方 `applyRerankOrTrim` 降级（E3）。**已实现**（AGENTS.md §4 "Reranker 失败 → 降级为原始排序 + warn；不向调用方报错"） |
| E9 | Chat 经同一 `search.Query`（修复自动传导） | ✅ `chat.go:132` `c.search.Query(...)`；`:138` `retrieval: %w` 包装 → Chat 500（E4）。`Agent` 工具 search 用 hybrid（AGENTS.md §2.3）同路径 |
| E10 | 测试基建（fixture/缝先例） | ✅ `integration_test.go:26-64` `newTestEnv`（SQLite+local FS）、`:198-222` `TestSearchHybridMode`（`NewBM25()+BuildFromRepo` 先例）；`vectorindex_test.go:12-20` `fakeVectorIndex`；`result_cache_test.go:13-25` `countingHashEmbedder`（包装 embedder 先例）；`embedder.go:21` `recordEmbedUsage` 包变量缝 + `http_clients_test.go:69-73` 替换断言模式；`telemetry/metrics_test.go:42-55` `scrapeValue` 行精确解析 + `:57-91` `TestAuditGovernanceMetrics_SurfaceInScrape`；REST 缝测试模式 `mw.Tenant`/`mw.Auth`（`router.go:229`、`admin_files_delete_test.go:77-92`） |

**补充验证（方向文未引用、规格依赖的事实）：**
- `Request.Mode` 空值默认 `"vector"`（`search.go:157-160`）；hybrid 校验要求 embedder 与 BM25 均可用（`:136-143`）——降级路径的前提是**已配置但失败**的 embedder（nil embedder 仍被 validate 拒绝，行为不变）。
- `searchLexical` 用 `s.embedder.Name()` 做 `matchesEmbedModel` 过滤（`:195-198`）——失败 embedder 的 `Name()` 不调用 `Embed`，不产生新错误；**测试 fixture 中失败 embedder 的 `Name()` 必须等于种子 chunk 的 `EmbedModel`**，否则 BM25 命中会被全部过滤（AC-1 已内建）。
- `Query` 错误路径：`searchAndMerge` 错误 → `return nil, err`（`:355`），无部分结果通道——降级决策必须落在 `searchAndMerge` 内（两半结果齐备处），`searchVector`/`searchLexical` 不知 mode，不能自行降级。
- 全仓 grep：`internal/api/rest` 无任何 AI handler 测试（`NewAIHandler`/`/v1/search` 在 `*_test.go` 零命中）——AC-2 是新建缝测试，非扩展。
- readyz B3-2 先例：`readyz_drill_test.go:224` 断言 `status=%d want 200 (degraded, never 503)`——"降级而非失败"已在 cmd-server 侧落地（HEAD `15763e2` 提交信息同源）。

### 2.3 缺口分析（方向 acceptance vs 现状）

| # | 缺口 | 现状证据 | 后果 |
|---|------|---------|------|
| G1 | **hybrid 模态失败无降级** | `searchAndMerge` `:309/:317` 硬失败；`searchVector` `:171/:178` 错误上抛后无消费方兜底 | embedder/vector 故障 → `/v1/search` 总失败（500），词法侧结果被丢弃；违反 B3-2/D1 |
| G2 | **无降级遥测** | `metrics.go` 无 `ai.search.degraded` 类计数器（grep 确认；`ai.*` 域仅有 requests/tokens/cost_micros/embed_requests/embed_tokens/search_duration_ms/embed_duration_ms） | 降级不可观测——即便行为修复，运维无法区分"正常 BM25-only"与"降级 BM25-only" |
| G3 | **无降级行为测试** | 全仓无 embedder 失败 × hybrid 的测试；REST 缝零 AI handler 测试（§2.2 补充验证） | 行为无钉死，回归风险高（本方向 acceptance 的三条测试全部为新建） |

---

## 3. 需求规格

### FR-1：hybrid 模态级降级（核心契约）

`Search.Query`（`search.go:333`）在 `mode=="hybrid"` 下，任一模态失败**不得**中止查询：

- **vector 半失败**（`Embed` 或 `SearchVectors` 返回错误）→ 以 lexical 半结果继续管线（**BM25-only**）：调用 `s.logger.Warn`（消息须含精确子串 `"embed failed"` 或 `"vector index failed"`，镜像 `:291` 的 `"rerank failed; using raw order"` 风格）并递增降级计数器（FR-2，`reason="embed"` 或 `"vector"`）。
- **lexical 半失败**（`searchLexical` 返回错误，即 pgFTS 后端故障）→ 以 vector 半结果继续（**vector-only**）：warn（子串 `"lexical search failed"`）+ 计数（`reason="lexical"`）。与上一子句是同一分支结构的对称情形——实现不得只覆盖 vector 半。
- **降级结果走既有管线，无新响应字段**：`filterAuthorizedHits` → `applyRerankOrTrim`（reranker 自身失败按既有降级 `:291`）→ `recordUsage` → `telemetry.RecordSearchLatency(ctx, "hybrid", …)` → 结果缓存。`searchResponse` schema 不变（`dto.go:100-103`）。
- **降级决策在 `searchAndMerge`（`:303`）内**：两半分别收集结果与错误，hybrid 下某半失败时用健康半的 `[]ranked` 走 `trimToOverK`（`:329`）的替代路径（仅健康半，不融合；`rrfMerge` 只在两半都成功时调用）；`searchVector`/`searchLexical` 本身不改（错误原样上抛，由合并处决策）。
- **边界（保持可见性）**：
  - 两半都失败 → 仍返回错误（`return nil, err`，第一个错误即可）——无健康结果可降级，错误必须可见。
  - 请求 ctx 已 done（截止时间已触发）→ 返回 `ctx.Err()`，**不降级**——词法半同样会失败，降级仅当健康半能在同一 ctx 上完成。AC-1 的 mock embedder 返回 `context.DeadlineExceeded` 但**不取消**传入 ctx（测试 ctx 为 `context.Background()`），这是降级得以成立的判别条件。
  - 纯 `vector`/`bm25` 模式不降级（FR-3）。

### FR-2：降级遥测计数器

- 新计数器 `ai.search.degraded`（Prometheus 导出名 **`ai_search_degraded_total`**），属性 `reason ∈ {"embed","vector","lexical"}`；**仅在实际降级时 +1**（健康半成功返回结果）——两半皆败的错误路径不计数（错误本身已可见）。
- 形状完全镜像 `IncIndexerSkip`（`metrics.go:279-281`）：`func IncSearchDegraded(ctx context.Context, reason string)`，`initDomain` 内注册（`:77` 区）`Int64Counter("ai.search.degraded")`，字段声明（`:31` 区）。
- `search.go` 增加包变量缝 `var recordSearchDegraded = telemetry.IncSearchDegraded`（镜像 `embedder.go:21` 的 `recordEmbedUsage` 注释模式），使 `internal/ai` 单测可断言计数而不必起 Prometheus。

### FR-3：纯模式错误保持可见（回归钉）

- `mode=="vector"`：embed/vindex 失败 → 错误原样上抛（REST 500 不变）。
- `mode=="bm25"`：lexical 失败 → 错误原样上抛。
- 全仓不新增任何"错误 → 200"的静默映射；只有 hybrid 因存在另一模态而降级。`aiDegraded` 503 kill-switch（`rest/search.go:37-47`，`AI_DEGRADED_MODE` 全局开关）与本次 per-query 降级**互不干扰、均不改动**。

### 非功能约束

- `make check` 全绿（gofmt/go build/go vet/go test，AGENTS.md §0）；新增/修改文件 ≤ 500 行（改动集中在 `search.go` 尾部 + `metrics.go` 三处 + 测试文件，预计 <100 行生产代码）。
- 纯 stdlib，无新 `go.mod` 依赖（I6）；单测仅 `testing`，无断言框架。
- **不触碰**中间件链（I4，`timeout.go` 不改）、无配置键、无迁移、无 `openapi.json` 改动（响应 schema 不变）。

---

## 4. 验收标准（可测试；逐条映射方向 acceptance）

> 方向 acceptance 三条原样保留并细化：① 单元降级测试 ② REST 缝 200-with-hits ③ 纯 vector 回归钉；遥测计数器新增 scrape 面钉（AC-4，仓库既有 `TestAuditGovernanceMetrics_SurfaceInScrape` 模式）。
> 测试基建（已验证，§2.2 E10）：`newTestEnv`（SQLite + local FS）；`NewBM25()+BuildFromRepo`（`TestSearchHybridMode` 先例）；`fakeVectorIndex`（`vectorindex_test.go:12-20`）；`countingHashEmbedder` 包装模式；`recordEmbedUsage` 包变量缝替换模式（`http_clients_test.go:69-73`）；`scrapeValue` 行精确解析（`metrics_test.go:42-55`）。

### AC-1（方向 acceptance ①）单元测试：hybrid + 失败 embedder → BM25 命中 + warn + 计数

```go
// internal/ai/degrade_test.go（新建；包内测试，复用 testEnv）
type failingEmbedder struct{ inner Embedder }
// Embed 返回 nil, context.DeadlineExceeded（不取消传入 ctx）；
// Dimensions()/Name() 委托 inner —— Name() 必须等于种子 chunk 的 EmbedModel，
// 否则 searchLexical 的 matchesEmbedModel（search.go:195-198）过滤掉全部 BM25 命中。

func TestSearchHybrid_DegradesToBM25OnEmbedderFailure(t *testing.T) {
	env := newTestEnv(t)                              // integration_test.go:26
	emb := NewHashEmbedder(128)
	o := env.putObject(t, "h.txt", "text/plain", "hybrid")
	env.seedChunks(t, o, emb, "raft consensus protocol", "baking sourdough")
	b := NewBM25()
	if err := b.BuildFromRepo(context.Background(), env.repo, testTenant); err != nil { t.Fatal(err) }

	// 遥测缝替换（http_clients_test.go:69-73 模式）
	orig := recordSearchDegraded
	defer func() { recordSearchDegraded = orig }()
	var reasons []string
	recordSearchDegraded = func(_ context.Context, r string) { reasons = append(reasons, r) }

	// warn 捕获 logger（自定义 slog.Handler：Enabled 恒 true，Handle 收集 records）
	var warns []slog.Record
	capture := &captureHandler{records: &warns}
	s := NewSearch(env.repo, &failingEmbedder{inner: emb}, slog.New(capture)).WithBM25(b)

	hits, err := s.Query(context.Background(), Request{
		Tenant: testTenant, Query: "raft consensus", K: 5, Mode: "hybrid"})
	// 断言：
	//  1) err == nil；len(hits) > 0 且 hits 中含 "raft" chunk（BM25-only 结果）
	//  2) warns 中至少一条 Level==slog.LevelWarn 且 Message 含子串 "embed failed"
	//  3) reasons == ["embed"]（恰一次）
	//  4) 对照组：同一 fixture 下 Mode:"bm25" → 命中集合与降级结果一致
	//     （降级结果 == 纯 BM25 结果，证明没有悄悄改变词法排序）
	//  5) 对称子用例（FR-1 第二子句，同一分支）：健康 embedder + WithLexicalIndex
	//     (&failingLexical{}) [SearchLexical 返回 context.DeadlineExceeded] →
	//     err == nil、hits 非空（vector-only）、warn 含 "lexical search failed"、
	//     reasons == ["lexical"]
}
```

### AC-2（方向 acceptance ②）REST 缝测试：200-with-hits（非 5xx）

```go
// internal/api/rest/search_ai_test.go（新建；grep 证实当前零 AI handler 测试）
func TestSearchREST_HybridDegradesOnVectorBackendError(t *testing.T) {
	// 装配：repository.Open("sqlite", "file:…") + Migrate（integration_test.go 同款）；
	// 经 service.Put 落对象 + repo.InsertChunks 种子 chunk（EmbedModel == embedder.Name()）；
	// b := NewBM25(); b.BuildFromRepo(ctx, repo, tenant)
	// s := NewSearch(repo, emb, slog.Default()).WithBM25(b).
	//     WithVectorIndex(&failingVIndex{})   // SearchVectors 返回 context.DeadlineExceeded
	// aih := NewAIHandler(repo, s, nil, nil, slog.Default(), false /*degraded*/)
	// 路由：r := chi.NewRouter(); r.Use(mw.Tenant, mw.Auth); r.Post("/v1/search", aih.Search)
	//       （AGENTS.md 测试模式 "需 tenant/auth 上下文 → mw.Tenant(mw.Auth(h))"；
	//       reg==nil 时 mw.Auth 为透传，router.go:229 同款）

	// POST /v1/search {"query":"raft consensus","mode":"hybrid","k":5}
	// 断言：code == 200（绝非 5xx）；解析 searchResponse（dto.go:100-103）：
	//   resp.Hits 非空且首条含 "raft"（BM25-only 命中经缝完整返回）

	// 负控制（同 router、同 fixture）：{"mode":"vector"} → code == 500 且
	//   body error.code == "InternalError"（FR-3 可见性在缝上钉死）
}
```

### AC-3（方向 acceptance ③）回归钉：纯 vector 模式错误仍可见

```go
// internal/ai/degrade_test.go（与 AC-1 同文件）
func TestSearchVectorMode_SurfacesEmbedderError(t *testing.T) {
	// 同 AC-1 fixture；Mode:"vector" + failingEmbedder
	// 断言：err != nil（errors.Is(err, context.DeadlineExceeded)）；
	//       recordSearchDegraded 未被调用（reasons 为空 —— 错误路径不计数，FR-2）
	// Mode:"bm25" + failingLexical 同理：err != nil 且不计数
}
```

### AC-4 遥测面钉：新计数器出现在 /metrics scrape

```go
// internal/telemetry/metrics_test.go 扩展（TestAuditGovernanceMetrics_SurfaceInScrape 模式 :57-91）
func TestSearchDegradedMetrics_SurfaceInScrape(t *testing.T) {
	if sharedPromHandler == nil { t.Skip(...) }
	IncSearchDegraded(ctx, "embed")
	IncSearchDegraded(ctx, "vector")
	IncSearchDegraded(ctx, "lexical")
	// scrape（TestMain 单一 EnablePrometheus，main_test.go）：
	//   scrapeValue(body, "ai_search_degraded_total") 按 reason 标签逐行精确断言：
	//   ai_search_degraded_total{reason="embed"} == 1，vector/lexical 同理
}
```

### AC-5 既有行为不回归

- `go test ./internal/ai/ ./internal/api/rest/ ./internal/telemetry/` 全绿；`make check` 全绿。
- 既有 hybrid/rerank/cache/validation 测试全部通过（`TestSearchHybridMode`、`TestSearchWithReranker`、`TestSearchResultCache_*`、`search_validation_test.go` 等）。
- 纯 `vector`/`bm25` 模式、`aiDegraded` 503、预算 402、SSE 错误帧形状均不变。

---

## 5. 范围边界（明确不做）

| 不做 | 理由 |
|------|------|
| `/chat`、`/chat/stream`、`/agent` 语义改动 | 三者经共享 `search.Query`（chat.go:132）自动受益于降级；检索整体失败时仍 500（不变）。LLM/预算/SSE 帧均不动 |
| 新超时/重试/上下文分离 | 方向只要求降级。embedder 30s 客户端超时（embedder.go:106）不改；降级半用**同一 ctx**——ctx 已 done 则错误上抛（可见性优先，FR-1 边界） |
| 响应体降级标记（新字段/header） | `searchResponse` schema 与 `openapi.json` 不变（dto.go:100-103）——降级可观测性由 warn + 计数器承载，与 rerank 降级先例（无响应标记）一致 |
| 结果缓存语义 | 降级结果可入缓存（`resultCacheKey` 不变）——"结果可陈旧"是既有属性（`WithResultCache` 文档明示），本方向不改 |
| 纯 `vector`/`bm25` 模式降级 | 无另一模态可回退；错误上抛（AC-3 钉死） |
| `aiDegraded` 503 kill-switch（`AI_DEGRADED_MODE`） | 显式全局开关（rest/search.go:37-47），独立于 per-query 降级；不改 |
| 既有遥测计数器/metrics 名、`ai.search.duration_ms` 语义 | 只新增一个计数器；`RecordSearchLatency(mode=hybrid)` 在降级路径照常记录（FR-1） |
| Indexer/usage/lineage/agent 审计面 | 方向 2/3（ai_usage 幂等、e2e 并发）是独立方向；`recordUsage` 随降级结果照常执行（行为不变） |
| 新配置键、迁移、中间件链（I4） | 本方向零配置、零迁移、零中间件改动 |

---

## 6. 实现指引（供验收后落地，非本规格交付物）

- **`internal/ai/search.go`**（预计 ~25 行净改动）：重构 `searchAndMerge`（`:303-331`）为"两半分别收集（结果, 错误）"；hybrid 下任一半失败 → `s.logger.Warn("embed failed; falling back to lexical results", "err", err)`（vector 半）/ 对称消息（lexical 半）+ `recordSearchDegraded(ctx, reason)`，以健康半的 `[]ranked` 走 `trimToOverK`；两半皆败 → 返回第一个错误。`searchVector`/`searchLexical` 不改。包变量缝 `var recordSearchDegraded = telemetry.IncSearchDegraded` 置于文件顶部（镜像 `embedder.go:21`）。
- **`internal/telemetry/metrics.go`**（3 处，镜像 `:31/:77/:279-281`）：声明 `mSearchDegraded metric.Int64Counter` → `initDomain` 注册 `Int64Counter("ai.search.degraded")` → `IncSearchDegraded(ctx, reason string)`（`Add` 带 `reason` 属性）。
- **测试**：`internal/ai/degrade_test.go`（AC-1/AC-3，含 `failingEmbedder`/`failingLexical`/`captureHandler` fixture）；`internal/api/rest/search_ai_test.go`（AC-2）；`internal/telemetry/metrics_test.go` 扩展（AC-4）。
- 无迁移、无配置键、无 `openapi.json`/路由/中间件改动；`make check` 验证。
