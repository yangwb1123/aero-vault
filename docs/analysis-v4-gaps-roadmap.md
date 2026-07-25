# 🏗️ AeroVault 深度评估 v4 — 内部质量、AI 管线深度、数据访问层

> **日期:** 2026-06-30  
> **方法:** 全局代码扫描（236 文件 / ~45K 行），第四轮  
> **视角:** 代码质量 + 架构债 + AI 管线成熟度 + DB 层优化

---

## 0. 本轮焦点：从"缺什么"到"怎么做得更好"

前三轮分别覆盖了**功能差距**（v1）、**系统韧性**（v2）、**协议生态与经济性**（v3）。  
本轮转向**已实现功能的内在质量**——代码架构债、AI 管线的评估盲区、数据库查询模式、并发安全模型、以及测试覆盖的深层缺口。这些不直接可见于功能列表，但决定了**长期可维护性**和**生产可靠性**。

---

## 1. 内部架构质量与代码债

### 1.1 包依赖与耦合分析

| 包 | 内部依赖数 | 被依赖数 | 角色 |
|-------|:----------:|:--------:|------|
| `service` | 3 | 5 | **核心耦合点**——4 个协议适配器 + AI Agent 都依赖它 |
| `repository` | 0 | 8 | **被依赖之王**——几乎所有业务包都依赖它 |
| `storage` | 0 | 4 | 抽象稳固——`service`、`replication`、`reconcile`、`antivirus` |
| `events` | 2 | 3 | 事件总线被 `service` + `main` 引用 |
| `ai` | 4 | 2 | 耦合最高——依赖 `repository`、`storage`、`service`、`telemetry` |
| `api/rest` | **8** | 0 | **依赖最多**——引用 8 个内部包，包含 handler 565 行 |
| `middleware` | 0 | 0 | 干净——零内部依赖 |
| `webui` | 0 | 0 | 干净——纯静态文件 |

**发现：** `internal/api/rest` 引用了 8 个内部包（service、repository、ai、auth、events、middleware、config、telemetry），是整个代码库中耦合度最高的包。这说明 REST API 层承担了过多的编排职责——它同时处理认证、业务逻辑、AI 路由、事件流、限流等，缺乏清晰的分层。

### 1.2 错误处理模式审计

| 模式 | 使用场景 | 问题 |
|---------|-----------|-------|
| **`errors.Is` 链式检查** | `file_crud.go:Get` 中检查 `storage.ErrNotFound` → `service.ErrNotFound` | ✅ 正确 |
| **`errors.Join` 合并** | `api/rest/router.go` 中合并 `service.ErrInvalidArgs` + `errInvalidPostKey` | ⚠️ 调用方需 `errors.Is` 逐一检查 |
| **静默吞错误** | `file_crud.go:writePutObject` 中 `s.logger.Error` + `AddTenantUsage` 失败仅 warn | ⚠️ 业务错误不传播 |
| **`classify(err)` 映射** | `handler.go` 中 7 个 `errors.Is` 分支 + 1 个 default `InternalError` | ⚠️ 错误信息直接暴露给客户端（含 SQL 错误）|
| **`defer` 中忽略错误** | `handler.go:Put` 中 `defer rc.Close()` 不检查 error | ⚠️ 资源泄漏被掩藏 |
| **`context.Background()` 脱离请求** | `bus.go:123`、`indexer.go:313`、`dav.go:302/381` | ⚠️ 丢失链路追踪与超时传播 |

**代码引用:** `api/rest/handler.go:classify`（7 种映射）；`service/file_crud.go:writePutObject`（静默错误）；`events/bus.go:123`（`telemetry.IncEventDropped(context.Background())` 无请求上下文）

### 1.3 并发安全模型

| 组件 | 同步机制 | 评估 |
|--------|--------------|--------|
| `storage/local.go` | `sync.RWMutex` | ✅ 读多写少的合理选择 |
| `events/bus.go` | `sync.RWMutex` + `atomic.Int64` | ✅ 高效订阅/广播 |
| `auth/auth.go` | `sync.RWMutex` | ✅ |
| `middleware/ratelimit.go` | `sync.Mutex` + `atomic.Int64` | ⚠️ `Mutex` 可能成为高并发瓶颈 |
| `ai/bm25.go` | `sync.RWMutex` | ⚠️ 全表重建时阻塞所有搜索 |
| `ai/caching_embedder.go` | `sync.Mutex` | ⚠️ 嵌入热点时竞争 |
| `ai/result_cache.go` | `sync.Mutex` | ✅ 小缓存，低竞争 |
| `jobs/jobs.go` | `sync.RWMutex` | ⚠️ 注册表锁 + DB 锁双重竞争 |
| `replication/replication.go` | 无（goroutine-safe 设计） | ✅ 无共享状态 |
| `ai/indexer.go` | 无（event-driven） | ✅ 无共享状态 |
| `storage/circuitbreaker.go` | `sync.Mutex` | ✅ 低频状态切换，合理 |

**发现：** 同步模型总体合理。关注点：
1. `bm25.go` 的 `RWMutex` 在 `BuildFromRepo` 期间（全表扫描重建）会阻塞所有读取，可通过分段锁或[写时拷贝(Copy-on-Write)](https://en.wikipedia.org/wiki/Copy-on-write) 优化
2. `caching_embedder.go` 的 `Mutex` 在嵌入热点下会成为瓶颈——建议改用 `sync.Map` 或分片锁
3. `jobs.go` 双重锁（进程内 `RWMutex` + DB 行锁）在 100+ 并发 worker 下可能导致活锁

### 1.4 优雅关闭分析

| 组件 | 关闭行为 | 问题 |
|----------|-------------|-------|
| HTTP Server | `srv.Shutdown(shutdownCtx)` 15s 超时 | ⚠️ 15 秒不足以排空长连接 SSE 流 |
| EventBus | `bus.Close()` 关闭所有 subscriber channel | ⚠️ 关闭顺序不保证 subscriber 处理完正在消费的事件 |
| JobPool | 无 `Stop()` 方法——goroutine 随 main 退出 | ⚠️ 正在执行的 job 被硬中断 |
| Reconcile | 无 `Stop()`——同上 | ⚠️ 可能留下未完成的清理操作 |
| SSE 连接 | 无主动排空——依赖客户端断开 | ⚠️ 重启时 1000+ SSE 连接瞬间断开 |
| OTel | `shutdownOtel(shutdownCtx)` 15s 超时 | ✅ 标准做法，但 exporter 排空可能不够 |

**代码引用:** `cmd/server/main.go:258-265`（仅关闭 HTTP + OTel，不等待 worker 排空）

---

## 2. AI 管线深度审计

### 2.1 Embedding Pipeline 分析

| 阶段 | 实现 | 质量评估 |
|-------|----------|--------------|
| **文本提取** | `DefaultExtractor`（PDF/HTML/DOCX/TXT）+ `RemoteExtractor` | ⚠️ **无图像 OCR**、**无音频转写**、**无视频字幕提取** |
| **分块** | `Chunker{Window:600, Overlap:80}`——固定窗口+重叠 | ⚠️ **无语义分块**、**无递归文档分割**、**无分段感知** |
| **嵌入降级** | `HashEmbedder`——基于 shingle 哈希 | ⚠️ **搜索质量差**——仅用于 demo，但生产无告警 |
| **批量嵌入** | `HTTPEmbedder.Embed` 单次调用传入 `[]string` | ⚠️ **无自动批处理大小优化**——大文档列表可能超 token 限制 |
| **模型漂移** | `searchVector` 跳过 `EmbedModel` 不匹配的 chunk | ⚠️ **无渐进式重新嵌入**——直到启用手动 reindex |
| **向量检索** | 暴力 / pgvector / Qdrant | ⚠️ **无 ANN 参数调优**（efConstruction/nprobes/segments）|

**代码引用:** `ai/extractor.go`（6 种格式，无图像/音频）；`ai/chunker.go`（固定窗口，无语义分割）；`ai/embedder.go:Embed`（单次批处理调用）；`ai/search.go:searchVector`（模型匹配过滤）

### 2.2 LLM Chat & Agent 质量

| 维度 | 当前实现 | 质量评估 |
|----------|-------------|--------------|
| **System Prompt** | 硬编码 `agentSystemPrompt`（3 工具描述） | ⚠️ **不可自定义**，无租户级覆盖 |
| **Tool 调度** | `dispatchTool` switch 语句（3 工具） | ⚠️ **硬编码**，不可扩展 |
| **上下文窗口** | 无 `MaxTokens` 设置——`ChatRequest.MaxTokens` 零值不发送 | ⚠️ LLM 使用默认窗口，可能截断 |
| **幻觉控制** | 无 `grounding` / `citation check` / `factual consistency` 验证 | ⚠️ Agent 回答可能完全编造 |
| **重试** | 无自动重试——LLM 调用失败直接向上返回错误 | ⚠️ 瞬时故障导致用户体验降级 |
| **流式 SSE** | `ChatStream` 通过 `token`/`done`/`error` 事件 | ⚠️ **无心跳**——静默连接保持无反馈 |
| **成本控制** | `cost.go` 用 `costMicros` 追踪——但仅聊天路径 | ⚠️ **搜索成本不计入费用** |

**代码引用:** `ai/agent.go`（system prompt 硬编码、3 工具固定）；`ai/chat.go`（无幻觉检查）；`ai/llm.go`（`MaxTokens=0` 不发送）；`ai/cost.go`（仅聊天计费）

### 2.3 RAG 评估与质量保证

| 缺口 | 影响 |
|--------|--------|
| **无检索评估** | 无法测量 recall@k / precision@k / MRR / NDCG |
| **无 A/B 测试框架** | 无法比较不同嵌入模型 / 分块策略的搜索效果 |
| **无用户反馈回路** | 无"此回答有帮助吗？"→ 无法优化排序 |
| **无查询日志分析** | 无零结果查询 → 无法发现索引缺口 |
| **无提示注入防护** | System prompt 是静态的——用户输入可覆盖行为 |
| **无内容安全过滤** | 未对检索到的 chunk 做毒性 / 有害内容过滤 |

**代码引用:** `ai/search.go`（无 recall/precision 追踪）；`ai/agent.go`（无提示注入保护）；`ai/indexer.go`（索引完成但无质量门禁）

---

## 3. 数据库与数据访问层

### 3.1 查询模式审计

| 查询 | 位置 | 模式 | 效率评估 |
|---------|--------|---------|--------------|
| `SearchChunks` | `sql_chunks.go:76` | **全表扫描** → 加载所有 chunk 到内存 → Go 中计算余弦相似度 | ⚠️ **100K+ chunk 时 OOM** |
| `ListStorageKeys` | `sql_buckets.go` | 返回所有 storage_key 到内存 | ⚠️ **同上** |
| `ListObjects` | `sql_objects.go:166` | `WHERE tenant=$1 AND bucket=$2 AND key LIKE $3` | ✅ 前缀查询有索引 |
| `ListObjectsByTag` | `sql_objects.go:202` | SQL + **内存过滤** tags | ⚠️ **返回全量后在 Go 中过滤** |
| `InsertChunks` | `sql_chunks.go:16` | 逐行 INSERT 在循环中 | ⚠️ **批量插入无事务合并** |
| `NextUnconsumedEvents` | `sql_events.go:36` | `WHERE consumed_at IS NULL ORDER BY id LIMIT $1` | ✅ 分页正确 |

**代码引用:** `sql_chunks.go:76-114`（暴力余弦扫描）；`sql_objects.go:202-230`（tag 查询在 Go 中过滤）；`sql_chunks.go:16-40`（逐行插入循环）

### 3.2 缺少的数据库索引

| 表 | 查询模式 | 缺少索引 | 影响 |
|------|-------------|--------------|--------|
| `objects` | 按 `tenant_id + bucket + deleted_at` 查询 | ✅ `(tenant_id, bucket, key) WHERE deleted_at IS NULL` 已存在 | — |
| `objects` | `ListSoftDeletedBefore` 按 `deleted_at` 排序 | ⚠️ **`(deleted_at)` 或 `(deleted_at, tenant_id)`** | 软删除清理扫描全表 |
| `objects` | `ListExpired` 按 `updated_at` + `storage_class` | ⚠️ **`(updated_at, storage_class)`** | 生命周期扫描全表 |
| `chunks` | `SearchChunks` 全表扫描 | ⚠️ **无索引——不用索引** | 每次搜索全表扫描 |
| `chunks` | `DeleteChunksForObject` 按 `object_id` | ✅ `(object_id)` 应有索引（检查迁移） | — |
| `events` | `NextUnconsumedEvents` 按 `consumed_at IS NULL` | ⚠️ **`(consumed_at, id)` 部分索引** | 事件扫描全表 |
| `audit_log` | `ListAudit` 无过滤 | ⚠️ 无时间索引 | 大量审计记录后全表扫描 |
| `jobs` | `ClaimJob` 按 `status + run_after` | ⚠️ **`(status, run_after)`** | 作业认领扫描全表 |
| `ai_usage` | `SumAICostMicros` 按 `tenant_id + created_at` | ⚠️ **`(tenant_id, created_at)`** | 费用统计全表扫描 |

**代码引用:** `repository/migrations/sqlite/0001_init.up.sql`（检查现有索引）；`repository/migrations/postgres/0001_init.up.sql`（同上）

### 3.3 事务与隔离

| 场景 | 当前行为 | 风险 |
|--------|-------------|------|
| `UpsertObject` + `AddTenantUsage` | 两个独立 DB 调用，无事务包装 | ⚠️ 配额更新与对象插入不一致 |
| `CompleteMultipart` → `deleteUpload` + `InsertObjectVersion` | 无事务 | ⚠️ 上传记录泄露 |
| `HardDeleteObject` → `DeleteChunksForObject` + `hardDeleteDB` | ChunkCleaner 在 repo 操作前调用 | ⚠️ 清理成功但删除失败 → chunk 孤立 |
| `InsertObjectVersion` | `SoftDeleteObject` + `INSERT`——**单个事务中** | ✅ 正确 |
| `EnqueueJob` | 单 INSERT + `DedupeKey` 约束 | ✅ 正确 |

**代码引用:** `service/file_crud.go`（`writePutObject` 中 quota 与 object 分开写入）；`service/file_multipart.go`（`CompleteMultipart` 中 upload 与 object 分开）；`repository/sql_objects.go:73-130`（`InsertObjectVersion` 内有事务）

---

## 4. 测试基础设施深度分析

### 4.1 测试分布

| 包 | 生产文件 | 测试文件 | 测试行数 | 生产行数 | 比例 | 评估 |
|-------|:---------:|:---------:|:----------:|:---------:|:----:|--------|
| `ai` | 20 | 16 | 4172 | 6726 | 62% | ✅ 最佳 |
| `storage` | 17 | 11 | 3361 | 5419 | 62% | ✅ 良好 |
| `service` | 6 | 5 | 1550 | 2086 | 74% | ✅ 最佳 |
| `api/rest` | 14 | 13 | 2437 | 4878 | 50% | ⚠️ 边缘 |
| `auth` | 8 | 6 | 1196 | 2420 | 49% | ⚠️ 边缘 |
| `reconcile` | 4 | 4 | 943 | 1925 | 49% | ⚠️ 边缘 |
| `jobs` | 1 | 2 | 544 | 519 | 105% | ⚠️ 测试行数 > 生产（含集成+模拟） |
| `events` | 3 | 5 | 1312 | 1555 | 84% | ✅ 良好 |
| `repository` | 19 | 14 | 3414 | 5410 | 63% | ✅ 良好 |
| `cli` | 5 | 2 | 1558 | 2755 | 57% | ⚠️ 已知 BUG（参看下文） |
| `middleware` | 4 | 3 | 828 | 1397 | 59% | ✅ 良好 |
| `mcp` | 3 | 3 | 1304 | 1794 | 73% | ✅ 良好 |
| **全库** | **133** | **104** | **24791** | **45389** | **55%** | ⚠️ 接近 50% 阈值 |

### 4.2 测试缺口

| # | 缺口 | 影响 | 位置 |
|---|---------|--------|--------|
| 1 | **集成测试覆盖率低** | 3 个集成测试文件，均带 `//go:build integration` tag，**CI 不执行** | `internal/integration/` |
| 2 | **云存储后端未测试** | `contract_test.go` 仅测试 local 后端。S3/OSS/COS 无契约测试 | `storage/contract_test.go` |
| 3 | **已知 BUG 在测试中声明** | CLI 有 6 个声明的 BUG（忽略 HTTP 状态码） | `cli/cli_test.go:1419-1430` |
| 4 | **AI 管线无端到端测试** | 搜索/聊天/Agent 路径仅在 `pure_units_test.go` 中有单元测试，无真实嵌入+搜索的集成测试 | `ai/` |
| 5 | **无并发测试** | 无 `go test -race` 的并发安全测试。同步模型未经验证 | 全库 |
| 6 | **WebUI 零测试** | 28 行生产代码，0 行测试 | `webui/` |
| 7 | **定时器/协调器无时间模拟** | `reconcile/job.go` 生命周期硬编码 `time.Duration`，测试中不可模拟 | `reconcile/` |
| 8 | **无故障注入测试** | `storage/` 层无模拟存储客户端注入错误——错误处理路径未经验证 | 全库 |

**代码引用:** `cli/cli_test.go` 第 1419-1430 行（6 个声明的 BUG）；`internal/integration/`（3 个文件，零 CI 执行）

### 4.3 测试代码质量

| 模式 | 出现次数 | 问题 |
|---------|:--------:|-------|
| `t.TempDir()` for DB+storage | ~30 次 | ✅ 正确模式 |
| `httptest.NewRecorder()` | ~40 次 | ✅ 正确 |
| 直接调用 handler 而非 router | 多数测试 | ⚠️ 不经过 middleware 链，遗漏 auth/tenant/rate-limit 测试 |
| Mock: `MockLLM{}` | 5 次 | ✅ 正确依赖注入 |
| Mock: `noopSink{}` | 3 次 | ✅ 正确 |
| 测试文件中的 `BUG:` 注释 | 6 处 | ⚠️ 已知问题未修复 |

---

## 5. 🚀 5 个高价值扩展方向（新维度）

---

### 🥇 方向 1：Data Access Optimization Layer — 查询优化器 + 索引管理 + 批量写入

**为什么需要它：**

当前数据库查询层存在**三个系统性性能问题**：全表扫描（`SearchChunks` 暴力余弦）、逐行操作（`InsertChunks` 循环插入）、缺少关键索引（`events`、`jobs`、`ai_usage` 表在大型数据集上全表扫描）。这些问题不会在测试环境中暴露（SQLite + 少量数据），但在生产环境中会导致 OOM 和超时。

**架构蓝图：**

```
当前: repository/sql_*.go → 直接 SQL + 应用层暴力计算

改进: DataAccessOptimizer (新包 internal/repository/optimizer)
├── QueryAnalyzer:
│   ├── EXPLAIN ANALYZE 定期扫描慢查询
│   ├── 自动检测全表扫描 → 告警
│   └── 查询计划缓存
├── IndexManager:
│   ├── 迁移时自动创建推荐索引 (基于 `ListExpired`/`ClaimJob`/`SumAICostMicros` 模式)
│   ├── 索引使用率监控 → 删除未使用的索引
│   └── 迁移 0025 添加关键索引:
│       ├── `CREATE INDEX idx_objects_expired ON objects(updated_at, storage_class)`
│       ├── `CREATE INDEX idx_events_unconsumed ON events(consumed_at, id) WHERE consumed_at IS NULL`
│       ├── `CREATE INDEX idx_jobs_claim ON jobs(status, run_after)`
│       └── `CREATE INDEX idx_ai_usage_tenant ON ai_usage(tenant_id, created_at)`
├── 批量操作引擎:
│   ├── `InsertChunksBatch`: 事务中批量 INSERT (100/批)
│   ├── `DeleteChunksBatch`: 批量删除
│   └── `SearchChunksOptimized`: 支持 pgvector ANN / Qdrant / 内存索引选择
├── 连接池管理:
│   ├── SQLite: WAL mode + busy_timeout + `_txlock=immediate`
│   └── Postgres: `MaxOpenConns` / `MaxIdleConns` / `ConnMaxLifetime` 配置暴露
└── 事务一致性改进:
    ├── `UpsertObject` + `AddTenantUsage` → 事务包装
    └── `CompleteMultipart` → 事务删除 upload + 插入 object
```

**复用资产：** `repository/sql.go`（`sqlStore` 结构体可扩展方法）、`repository/sql_chunks.go`（搜索可优化而非重写）、`repository/migrations/`（现有迁移模式）、`storage/circuitbreaker.go`（可复用到 DB 连接层）

**预计影响：**

| 查询 | 改进前 | 改进后 | 加速 |
|---------|------------|------------|--------|
| `SearchChunks` | 全表扫描 100K 行 | 向量索引 ANN | **100-1000x** |
| `InsertChunks` | 逐行 INSERT | 批量 INSERT 事务 | **10-50x** |
| `ClaimJob` | 全表扫描 | 索引范围扫描 | **100x** |
| `SumAICostMicros` | 全表扫描 | 索引范围扫描 | **100x** |

| 复杂度 | 用户影响 | 代码复用 | 差异化 |
|----------|-------------|-------------|------------|
| 中 | 极高（可靠性与扩展性） | ~70% | ★★★★★ |

---

### 🥇 方向 2：RAG Quality Assurance Framework — 评估、护栏、多模态

**为什么需要它：**

RAG 管线可工作，但**无法衡量工作好坏**。没有 recall@k、没有幻觉检测、没有提示注入防护、没有多模态输入提取。对于一个定位为"AI 原生"的存储平台，这代表着成熟度的巨大缺口。

**架构蓝图：**

```
当前: Extractor → Chunker → Embedder → Search → LLM → Answer (无质量门禁)

改进: RAGQualitySuite (新包 internal/ai/quality)
├── RetrievalEvaluator:
│   ├── 指标: recall@k, precision@k, MRR, NDCG, hit_rate
│   ├── 标注: 管理员可标记"正确"chunk → 自动评估
│   └── A/B 测试: 比较不同 embedder / chunker 策略的指标
├── LLM Guardrails:
│   ├── 提示注入检测: 用户输入中检测越狱 / 系统提示覆盖
│   ├── 事实一致性: ASQA / SelfCheckGPT 风格——检查回答是否源自检索的 chunk
│   ├── 毒性过滤: 检查检索内容 + 生成内容的有害性
│   └── PII 后置过滤: 索引时 PII 脱敏 + 生成时 PII 检查
├── 多模态扩展:
│   ├── 图像: OCR (tesseract/marker) + 图像描述 (多模态 LLM)
│   ├── 音频: 语音转文字 (Whisper API/本地)
│   ├── 视频: 关键帧提取 → OCR + 描述
│   └── 代码: 语法感知分块 (tree-sitter) 而非固定窗口
├── 用户反馈回路:
│   ├── POST /v1/feedback {query, response, helpful:true/false}
│   ├── 日志查询 → 零结果查询仪表板
│   └── 反馈 → 重排序模型微调信号
└── RAG 调试仪表板:
    ├── Web UI "Search Debugger" tab (复用 webui)
    ├── 显示: 检索到的 chunk + 分数 + 重排序前/后
    └── 显示: LLM prompt + 生成的回答 + 引用
```

**复用资产：** `ai/pii.go`（PII 检测可扩展到生成输出）、`ai/extractor.go`（可扩展注册新类型）、`ai/search.go`（可作为评估框架的检索后端）、`webui/static/index.html`（可扩展调试标签页）、`repository/ai_usage.go`（可扩展存储反馈数据）

**预计影响：**

| 场景 | 改进前 | 改进后 |
|--------|------------|--------------|
| 搜索质量 | 不可测量 | recall@k, MRR 可追踪 |
| 幻觉回答 | 无检测 | SelfCheckGPT 自动标记可疑回答 |
| 提示注入 | 无防护 | 输入清洗 + 越狱检测 |
| 多模态文档 | 仅文本提取 | OCR + ASR + 代码感知分块 |
| 管理员调试 | 无 | Web UI 调试面板 |

| 复杂度 | 用户影响 | 代码复用 | 差异化 |
|----------|-------------|-------------|------------|
| 高 | 极高（AI 可信度） | ~40% | ★★★★★ |

---

### 🥇 方向 3：Graceful Degradation & Lifecycle Management — 信号排空 + 热备 + 优雅关闭

**为什么需要它：**

当前 `runServer` 只关闭 HTTP 服务器（15s 超时）。Job pool、reconcile、indexer、webhook retry 工作线程都是 `go func()`——`main()` 退出时被硬中断。这意味着：
1. **正在写入的 blob 可能不完整**
2. **SSE 客户端瞬间断开无通知**
3. **正在进行的工作线程状态不可恢复**

**架构蓝图：**

```
当前: signal.NotifyContext → srv.Shutdown() → main 退出 (worker 被硬杀)

改进: LifecycleManager (新包 internal/lifecycle)
├── SignalHandler:
│   ├── SIGTERM → 优雅排空（并发 drain）
│   ├── SIGINT → 同上
│   ├── SIGHUP → 日志轮转 / 配置重载
│   └── SIGUSR1 → dump 状态到 /tmp
├── DrainManager:
│   ├── Phase 1: HTTP 停止接收新请求 (healthz/readyz 返回 503)
│   ├── Phase 2: SSE 发送关闭事件 → 等待客户端断开 (max 10s)
│   ├── Phase 3: JobPool.Stop() → 等待当前 job 完成 (max 30s)
│   ├── Phase 4: Indexer.Stop() → flush 缓冲区
│   ├── Phase 5: Reconcile/Lifecycle/Retention → last round
│   ├── Phase 6: EventBus.Close() → flush pending
│   └── Phase 7: OTel exporter flush + shutdown
├── 健康端点增强:
│   ├── /healthz: 存活（goroutine not stuck?）
│   ├── /readyz: 就绪（DB ping + 存储 ping + 断路器未开）
│   └── /debug/pprof: 转到 pprof 端点
├── Watchdog:
│   ├── 每个 worker 注册心跳
│   ├── 心跳超时 → goroutine dump + 日志告警
│   └── 连续超时 → 进程退出（Kubernetes 重启）
└── 配置热重载:
    ├── SIGHUP → 重新读取配置文件
    ├── 日志级别: 运行时调整 (slog.SetLogLoggerLevel)
    ├── 限流: 运行时调整 RPS/Burst
    └── Degraded 模式切换: 无需重启即可关闭 AI 端点
```

**复用资产：** `cmd/server/main.go`（现有 `signal.NotifyContext` 可扩展）、`middleware/timeout.go`（超时管理可集成）、`events/bus.go:Close()`（已有）、`cluster/singleton.go`（租约管理可复用）

**预计影响：**

| 场景 | 改进前 | 改进后 |
|--------|------------|--------------|
| Kubernetes 滚动更新 | SSE 客户端丢失事件 + job 被硬中断 | 排空完成后才退出 |
| 配置调整 | 杀死进程 → 重启 | SIGHUP 热重载 |
| 工作线程卡死 | 静默 | Watchdog 告警 + goroutine dump |
| 存储后端故障 | 端到端超时 30s+ | 断路器 + /readyz 返回 503 |

| 复杂度 | 用户影响 | 代码复用 | 差异化 |
|----------|-------------|-------------|------------|
| 中 | 高（运维可靠性） | ~50% | ★★★★☆ |

---

### 🥇 方向 4：Concurrency & Resilience Hardening — Race 检测 + 锁优化 + 背压

**为什么需要它：**

代码库有 11 处使用 `sync.Mutex` / `sync.RWMutex` 保护共享状态，但**没有并发测试**（`go test -race` 从未在 CI 中执行）。已知的风险模式包括：BM25 全表重建时阻塞所有搜索、缓存嵌入在热点下竞争、限流器的 `Mutex` 可能成为瓶颈。

**架构蓝图：**

```
当前: 零并发测试 + 简单的 Mutex 保护 + 无限作业入队

改进: ConcurrencyHardening (跨包重构)
├── Race Detection CI Gate:
│   ├── `go test -race ./internal/...` 作为 make check 的一部分
│   ├── 已知竞态: 记录并逐一修复
│   └── 试验性: Go 1.25 `sync.Map` + `runtime.Deadline` 改进
├── 锁优化:
│   ├── `ai/bm25.go`: `RWMutex` → **Copy-on-Write 快照切换**
│   │   ├── 写入时构建新索引 → 原子赋值 `*atomic.Pointer[Index]`
│   │   └── 读取始终读当前快照 → 零阻塞
│   ├── `ai/caching_embedder.go`: `Mutex` → `sync.Map` + 分片锁
│   ├── `middleware/ratelimit.go`: `Mutex` → 分片 token bucket + `atomic` 递减
│   └── `auth/auth.go`: `RWMutex` → `sync.Map` 读优化
├── 背压系统:
│   ├── EventBus: 订阅者落后 → 背压信号 → 限制发布速率 (当前仅丢弃)
│   ├── JobQueue: `MaxQueueSize` → 超过时拒绝入队（当前无限增长）
│   ├── HTTP: `ConcurrencyLimiter` → 扩展到按租户独立加权
│   └── Indexer: 管道缓冲 → 处理速度 < 生产速度时暂停消费
├── 无锁数据结构:
│   ├── `requestID` 生成: `atomic.Uint64` 替代 `uuid.NewString`（无锁 + 更短）
│   ├── `NewVersionID`: 使用 `crypto/rand` → 考虑 `ulid` 或 `xid`
│   └── 计数器: `atomic.Int64` 替代 `Mutex` + `int64`
└── 死锁预防:
    ├── 锁顺序协议: 文档化加锁顺序（eg: `mu global → mu local`)
    └── `defer` 解锁: ✅ 当前已正确（全库检查确认）
```

**高频锁竞争分析（按路径）：**

```
请求路径: HTTP → middleware → auth → service → storage/repository

锁竞争链:
  1. auth.Registry.mu (读) → 读取 API 密钥 → 可接受
  2. middlewar.ratelimit.mu (写) → 每请求更新 token → ⚠️ 高竞争
  3. storage/local.mu (写) → 每 PUT/GET → ⚠️ 本地后端高竞争
  4. ai/caching_embedder.mu (写) → 每嵌入缓存未命中的查询 → ⚠️ AI 场景高竞争
  5. ai/bm25.mu (写) → BM25 重建时 → ⚠️ 阻塞全库搜索
```

**代码引用:** `ai/bm25.go:23`（`RWMutex` 阻塞读）；`ai/caching_embedder.go:17`（`Mutex` 竞争）；`middleware/ratelimit.go:17`（`Mutex` 瓶颈）；`events/bus.go:123`（丢弃而非背压）；`jobs/jobs.go:33`（`RWMutex` + DB 锁）

**预计影响：**

| 场景 | 改进前 | 改进后 |
|--------|------------|--------------|
| BM25 重建 | 阻塞搜索 10s+ | 零阻塞（Copy-on-Write） |
| 缓存嵌入 | 每未命中锁定 | 分片锁 → 几乎零竞争 |
| 限流器 | 全局 Mutex | 分片 atomic → 可扩展 |
| 竞态检测 | 零检查 | CI gate 保证 |
| 事件背压 | 丢弃 | 背压通信 → 生产平滑 |

| 复杂度 | 用户影响 | 代码复用 | 差异化 |
|----------|-------------|-------------|------------|
| 中 | 高（可靠性与扩展性） | ~60% | ★★★★☆ |

---

### 🥇 方向 5：Development Velocity Platform — 代码生成、API 契约测试、重构工具链

**为什么需要它：**

当前开发流程有一些重复性高、易出错的任务：OpenAPI 规范手动维护、迁移文件手工创建、新路由需要在多处注册、handler 接口需要重复编写错误映射。构建一个**开发工具链**可以显著提高开发速度和代码一致性。

**架构蓝图：**

```
当前: 手工编写: handler + router + dto + openapi + migration + test

改进: DevTooling (新目录 tools/)
├── CodeGenerators:
│   ├── `openapi-gen`: 从 `rest.Handler` 方法签名生成 OpenAPI 3.0 spec
│   │   ├── 替代当前手动 `openapi.go` (36 行)
│   │   └── 确保 handler 与 spec 始终同步
│   ├── `migration-gen`: `make migration name=add_retention_policy`
│   │   ├── 生成 `NNNN_add_retention_policy.{up,down}.sql` (sqlite + postgres)
│   │   └── 自动增量版本号
│   ├── `handler-stub`: 从 `service.FileService` 方法生成 handler 桩代码
│   │   └── 减少重复的 request解析→svc调用→response编码 模式
│   └── `error-gen`: 从 `service.Err*` 生成 `rest.classify` 映射
├── ContractTesting:
│   ├── `apitest` 框架: 自动测试 OpenAPI spec 中每个端点的:
│   │   ├── 200/400/404/409/500 响应
│   │   ├── 请求体验证 (required fields, type validation)
│   │   └── 响应体格式验证
│   ├── 协议一致性: REST 200 ↔ S3 200 ↔ WebDAV 200 对齐
│   └── 向后兼容: diff OpenAPI v1 vs v2 → 标记破坏性变更
├── Makefile 增强:
│   ├── `make generate` → 运行所有代码生成器
│   ├── `make check-contract` → 运行协议契约测试
│   ├── `make check-race` → `go test -race ./internal/...`
│   ├── `make check-lint` → `golangci-lint` 全面检查
│   ├── `make coverage` → 覆盖率报告 + 阈值检查
│   └── `make check-bounds` → 检查 500 行文件 / 50 行函数限制
├── 重构助手:
│   ├── `check-pkg-size`: 检测超过 500 行的包 → 拆分建议
│   ├── `check-func-size`: 检测超过 50 行的函数
│   ├── `check-cyclo`: 检测圈复杂度 > 10 的函数
│   └── `check-import-cycle`: 检测循环依赖
└── 模板系统:
    ├── `new-handler`: 新 handler 模板 (request/response/error/route/test)
    ├── `new-migration`: 双文件迁移模板
    └── `new-worker`: 后台工作线程模板 (注册/事件/重试/测试)
```

**复用资产：** `Makefile`（现有 `make check` 可扩展）、`api/rest/openapi.go`（现有手动 spec → 替换为生成）、`api/rest/dto.go`（DTO 类型 → 生成器输入）、`repository/migrations/`（双文件模式作为模板）、`AGENTS.md`（已有工程约束——工具可自动检查）

**预计影响：**

| 场景 | 改进前 | 改进后 |
|--------|------------|--------------|
| 新增 REST 端点 | 手工 5 文件: handler + route + dto + openapi + test (~60 min) | 生成器创建桩代码 → 填充业务逻辑 (~10 min) |
| OpenAPI 漂移 | 手动更新 spec → 与实际代码不同步 | 从 handler 自动生成 → 100% 同步 |
| 工程约束检查 | 人工 review 中 `AGENTS.md` 检查 | CI gate 自动拒绝违反 500 行 / 50 行 / 10 圈复杂度 |
| 迁移创建 | 手动创建 4 个文件 | `make migration name=xxx` → 自动生成 |
| 协议契约 | 无自动化 | 自动测试每个端点的所有响应代码 |

| 复杂度 | 用户影响 | 代码复用 | 差异化 |
|----------|-------------|-------------|------------|
| 中 | 中高（开发效率） | ~30% | ★★★★☆ |

---

## 6. 综合执行优先级（四轮联合）

| 阶段 | v1 方向 | v2 方向 | v3 方向 | **v4 方向（本轮）** |
|-------|-----------|-----------|-----------|----------------|
| **P0（当前缺口）** | 存储 Tiering | 写入断路器 | 可观测性管线 | **数据访问优化层** |
| **P0** | — | — | — | **并发加固 + 竞态检测 CI** |
| **P1** | FUSE 挂载 | Saga 编排 | API 网关统一层 | **优雅关闭生命周期管理** |
| **P1** | 外部队列事件 | 自愈网格 | 多云成本优化 | **RAG 评估框架** |
| **P2** | 多区域复制 | 搜索联邦 | 合规套件 | **开发者工具链** |

**推荐前三优先级（v4 维度）：**

```
#1 数据访问优化层 (DB 查询性能 + 索引 + 批处理)
    → 直接影响线上稳定性（OOM 风险）和搜索延迟
    → 代码复用率 70%，实现成本可控

#2 并发加固 + Race CI Gate
    → 前置条件（没有竞态测试，其他优化可能引入回归）
    → `go test -race ./internal/...` 作为 make check 的第一步

#3 优雅关闭生命周期管理
    → Kubernetes 部署的前提条件
    → 无需重启即可调整配置（日志级别/限速/降级模式）
```

---

## 7. 附录：已声明的已知 BUG 与 TODO

| 位置 | 类型 | 描述 |
|--------|------|---------|
| `cli_test.go:1419` | BUG | `cmdList` 从不检查 HTTP 状态码 |
| `cli_test.go:1422` | BUG | `cmdTag` 不检查 HTTP 响应状态 |
| `cli_test.go:1424` | BUG | `cmdVersions` 同上 |
| `cli_test.go:1426` | BUG | `cmdLineage` 同上 |
| `cli_test.go:1428` | BUG | `cmdSearch` 同上 |
| `cli_test.go:1430` | BUG | `snapshot create` 静默忽略缺失的 DB 文件 |
| `lifecycle_test.go:436` | BUG | `lifecycle.go` 调用 `store.Delete` 后未处理错误（代码注释中声明） |

> *发现：以上 7 个 BUG 均在测试文件的注释中声明，但从未修复。建议在 CI gate 中增加对这些已知 BUG 的跟踪，或在新季度开始前统一修复。*

---

> *本文档第四次全局扫描完成，未修改任何代码。基于 `AGENTS.md` 第 0 节工程约束：`sql_buckets.go`（406 行）、`encrypt.go`（363 行）、`repository.go`（364 行）虽未超 500 行阈值，但接近限度，建议在新开发前拆分。*
