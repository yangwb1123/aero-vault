# 架构分析报告：Aero-Vault 系统扩展方向评估

---

## 1. 架构评估

### 1.1 当前架构优势

在深入阅读了全部核心代码后，我确认当前架构在以下方面表现出色：

**分层职责清晰。** `main.go` 的装配顺序（`config → storage → repo → service → workers → middleware → router`）严格执行了依赖方向，不存在循环依赖。FileService 作为唯一的业务入口（第 95-100 行），向下屏蔽了 Storage/Repository 的具体实现，向上提供统一的 `*FileService` 句柄给 REST/S3/WebDAV/MCP 四套协议适配器。

**协议适配器厚度可控。** REST handler 不超过 50 行（完全匹配 AGENTS.md 的单函数约束），S3compat handler 也保持相似的薄度。验证发现 `internal/api/rest/handler.go` 确实遵守了"业务逻辑写入 handler"的禁令——所有分支最终调用 `svc.Put/svc.Get`。

**Opt-in 安全默认贯彻始终。** AI pipeline（第 826-830 行 `buildEmbedder`）返回 `nil` 时，`ai.Search`、`ai.Chat`、`ai.Agent` 的 nil 检查贯穿搜索和聊天路径。同样的模式适用于 `pgvector`/`Qdrant`/`webhook`/`replication`。CI gate 路径（SQLite + local FS + 无鉴权 + AI off）是可信的基线。

**扩展点设计良好。** Storage 的 `contract_test.go` 强制每个新 backend 通过合约测试；Repository 的 `interface{…}`（`repository.go` 第 56-120 行）为 SQLite/Postgres 双实现提供了坚实的抽象；EventBus 的订阅/发布模式让 Indexer、Webhook、Replication 做了良好的关注点分离。

### 1.2 架构局限与技术债

| # | 问题 | 位置 | 严重程度 | 评估 |
|---|------|------|---------|------|
| **A1** | **全局共享的 AI 组件无租户隔离** | `main.go:buildEmbedder/buildLLM` 返回单一实例 → 所有租户共享同一个 embedder/llm/reranker | **高** | 当前 embedder 实例仅一个。虽然 embedding 请求可以并发，但当租户 A 的批量索引作业占满 embedder 时，租户 B 的在线搜索会经历相同的尾延迟。`telemetry.RecordSearchLatency` 记录全局值，无法区分"满负载 vs 空闲" |
| **A2** | **MCP/Agent 硬编码 DefaultBucket** | `mcp/server.go:toolWriteFile/toolDeleteFile` 使用 `service.DefaultBucket`；`internal/mcp/server.go` 的 `list_files`、`read_file`、`write_file`、`delete_file` 工具要么硬编码 bucket，要么仅在参数可选时使用 | **中** | 非 `default` bucket 中的对象无法通过 MCP 写入或删除。这个在四套协议间产生了不一致的行为 |
| **A3** | **Indexer 租户遍历缺乏并发控制** | `indexer.go:ReindexStale` 顺序执行 `ListObjectIDsToReindex`；`handle()` 无租户级 backpressure | **中** | 单租户的重新索引洪流会阻塞其他租户的事件处理（所有租户共享同一个 `sub <-chan repository.Event`） |
| **A4** | **搜索缓存无租户感知的键空间** | `result_cache.go` 的 `resultCacheKey` 未隔离租户（仅 `tenant + bucket + query + mode + k` → 不同租户相同查询不会缓存冲突，但未显式声明隔离策略） | **低** | 当前实现使用租户作为 key 的一部分，所以无意中已是租户隔离的。但缺乏注释和测试覆盖这一设计意图 |
| **A5** | **`webhook_failures` 表无限增长** | 失败记录仅标记成功或更新 nextRetryAt，无过期清理策略 | **中** | `webhook_retry_test.go` 验证重试逻辑，但 `reconcile` 未清理已终结的 `webhook_failures` 记录。长期运行可能导致表膨胀 |
| **A6** | **SDK 客户端仅处理明文** | 三套 SDK（Go/JS/Python）均不提供加密能力，`sdk/go/aerovault/client.go:Upload` 逐字上传。`sdk/go/aerovault/sse.go` 仅实现 SSE 解析，不涉及客户端加密 | **中** | 合规场景要求数据在离开客户端前加密 |

### 1.3 关键设计决策评估

| 决策 | 当前实现 | 评估 |
|------|---------|------|
| FileService 作为唯一入口 | ✅ 四套协议适配器均调用 FileService | **合理。** 无协议绕过 storage 的直接路径 |
| 全局 Embedder/LLM 单例 | `main.go:buildEmbedder` 返回单一 `ai.Embedder` | **对小规模合理，但扩展性受限。** 多租户场景下，一个租户的索引操作产生的 embedding 延迟会直接影响其他租户的搜索延迟 |
| 接口优先于结构体 | `ai.Embedder`、`ai.LLM`、`ai.Reranker`、`service.EventSink`、`service.ChunkCleaner` | **合理。** 测试友好（MockLLM/HashEmbedder），扩展友好 |
| 事件驱动索引 | EventBus → Indexer `sub` + `drainBacklog` 轮询 | **合理。** 双路径（实时+轮询吞 backlog）保证了重启安全 |
| 双文件迁移 | 每个迁移有 `{sqlite,postgres}/NNNN_*.{up,down}.sql` | **合理。** I2 不变量强制执行 |
| SDK 客户端无加密 | 明文传输 | **对开源简单用例可接受，对企业合规不可接受。** 方向二正解决此问题 |

---

## 2. 扩展方向

### 方向一：AI 计算资源的租户级隔离

**为什么需要（业务价值/技术价值）：**

多租户 SaaS 场景中，租户 A 的批量索引操作不应造成租户 B 的搜索 P95 延迟从 50ms 飙升到 500ms。当前 `Search.Query` 路径（`search.go:206-250`）的 `s.embedder.Embed(ctx, …)` 和 `s.vindex.SearchVectors(ctx, …)` 不区分调用来源。当没有租户级限流时，一个"吵闹的邻居"租户可以耗尽共享的 AI 计算容量。

业务价值：提供可预测的搜索性能（搜索 SLA 确定性）、增强多租户隔离保证、支持差异化定价层。

**核心挑战和技术难点：**

1. **Embedder 并发控制的建模维度。** 需要决定：per-tenant 排队的公平调度（租户公平共享），还是 per-tenant 令牌桶（租户容量固定），或是 per-tenant 专用 pod（隔离最强但成本最高）。
2. **Per-tenant 限流与全局限流的交互。** 当前 `aiRL`（`middleware.NewRateLimiter(cfg.AI.RateLimitRPS, cfg.AI.RateLimitBurst)`）是全局的。需要引入一个 `TenantRateLimiter` 或 `WeightedRateLimiter` 来叠加 per-tenant 配额。
3. **追溯性。** 已经发生的争抢需要可观测性——`telemetry.RecordSearchLatency` 需要增加 `tenant` 标签维度。

**预期的架构变更：**

```
┌─main.go─────┐
│ aiRL(global) │ → RateLimitMiddleware (AI route group)
└──────────────┘
改为：
┌─main.go────────────────────────────────┐
│ aiRL = NewWeightedRateLimiter(globalRPS,│
│          perTenantRPS, tenantExtractor)  │
└──────────────────────────────────────────┘
```

或者，在 ai 包内部嵌入限流：

```go
type tenantAwareEmbedder struct {
    base  Embedder
    rls   sync.Map  // map[tenantID]*rate.Limiter
    lim   rate.Limit
    burst int
}
```

**对现有系统的影响：**

- 路径变化最小：嵌入一个装饰器（Decorator pattern）在 Embedder 外面即可
- 无需 schema 变更
- 对 `nil` embedder 路径无影响（I5 不变量保持）
- 可观测性增强（新增 `tenant` 标签的 `embed_latency_seconds` 指标）

### 方向二：SDK 客户端加密

**为什么需要（业务价值/技术价值）：**

合规驱动需求。HIPAA、GDPR、SOC2 等框架要求"at-rest"和"in-transit"的加密是*重叠的*——只信任服务器端加密（SSE-C/SSE-S3）意味着密钥传输到在途网络中（尽管是 TLS），且服务器可能有权限访问明文。客户端加密确保数据在离开客户端进程之前就已加密，服务器永无明文访问权。

业务价值：打开合规要求苛刻的行业（医疗、金融、法律）、差异化竞争力。

**核心挑战和技术难点：**

1. **密钥管理。** 客户端加密需要密钥在客户端可用——环境变量、KMS SDK（AWS KMS / GCP Cloud KMS）、Password-based KDF。每种方式的安全属性不同。
2. **可搜索性。** 客户端加密数据在服务端是密文——服务器的 Indexer 无法提取文本，导致 AI 搜索不可用。需要决定是否"先加密再上传"（放弃 AI 搜索）或"先上传明文索引，后上传密文数据"（数据密钥分级）。
3. **SDK 三套的一致性。** Go/JS/Python 需要实现相同的加密方案、相同的信封格式、相同的密钥派生路径。出错（不一致）会导致数据不可恢复。
4. **性能。** 大文件的流式加密（如 `crypto/aes` + `crypto/cipher.StreamWriter`）在 JS 侧需要 Web Crypto API，Python 端需要维护一个兼容的 AES-GCM 流。

**预期的架构变更：**

```
sdk/go/aerovault/
├── client.go        (unchanged)
├── crypto.go        (NEW)  // AEAD envelope, key wrapping, streaming encrypt/decrypt
├── crypto_test.go   (NEW)
├── types.go         (unchanged)
└── sse.go           (unchanged)

sdk/python/aero_vault.py   (add CryptoOptions)
sdk/js/aero-vault.js       (add crypto module using Web Crypto API or SubtleCrypto)
```

**Client API 增量：**

```go
type CryptoOptions struct {
    Key      []byte   // 32-byte AES-256 key
    Wrapping string   // "none" | "kdf:argon2" | "kms:aws"
}

func (c *Client) UploadEncrypted(ctx, key, io.Reader, opts UploadOptions, crypto CryptoOptions) (*Object, error)
// or: transparent encrypt via middleware when CryptoOptions is set in Client
```

**对现有系统的影响：**

- 服务器零变化（不感知加密）
- SDK 文件量增加约 200-300 行/套
- 单元测试需要确定性密钥和固定测试向量
- 需要明确的文档警告："客户端加密与 AI 搜索不兼容"

### 方向三：跨协议对象身份统一

**为什么需要（业务价值/技术价值）：**

当前四套协议（REST/S3/WebDAV/MCP）通过不同的"路径"引用同一个对象：

| 协议 | 对象引用方式 |
|------|------------|
| REST | `/v1/files/{key}`（隐含 `tenant=default, bucket=default`） |
| S3 | `/{bucket}/{key}`（隐含 `tenant=default`） |
| WebDAV | `/{prefix}/{key}`（隐含 `tenant=default, bucket=default`） |
| MCP | `aero-vault://{tenant}/{bucket}/{key}`（资源 URI） |
| Agent | 调用 `svc.Put(ctx, tenant, "default", key, …)`（硬编码 DefaultBucket） |

Agent 的 `toolWriteFile` 和 `toolDeleteFile` 使用 `service.DefaultBucket`，`toolListFiles` 也默认 `"default"`。跨协议的一致对象身份需要 canonical reference。

业务价值：MCP 工具返回的 `aero-vault://tenant/bucket/key` URI 可以被其他协议直接使用、日志中统一的引用格式、审计追踪可串联相同对象的不同访问路径。

**核心挑战和技术难点：**

1. **CanonicalRef 的定义位置。** 放在 `repository` 包（靠近数据模型）还是 `service` 包（靠近业务逻辑）？建议在 `repository` 包定义 `CanonicalRef` 类型，因为它本质上是数据库行的唯一标识。
2. **向后兼容。** REST 路径 `/v1/files/{key}` 已经是事实标准，不能破坏它。CanonicalRef 应作为*额外的*标识符，而非替代。
3. **OpenAPI 规范的一致性。** `rest/openapi.go` 需要在 schema 中添加 `canonical_ref` 字段，并添加从 canonical ref 解析 `{tenant, bucket, key}` 的端点。

**预期的架构变更：**

```go
// repository/repository.go
type CanonicalRef struct {
    Tenant string `json:"tenant"`
    Bucket string `json:"bucket"`
    Key    string `json:"key"`
    // optional: VersionID for versioned objects
    VersionID string `json:"version_id,omitempty"`
}

func (r CanonicalRef) String() string {
    return fmt.Sprintf("aero-vault://%s/%s/%s", r.Tenant, r.Bucket, r.Key)
}

func (r CanonicalRef) Path() string {
    return path.Join(r.Tenant, r.Bucket, r.Key)
}
```

在 `Object` 中添加方法：
```go
func (o Object) CanonicalRef() CanonicalRef {
    return CanonicalRef{Tenant: o.TenantID, Bucket: o.Bucket, Key: o.Key, VersionID: o.VersionID}
}
```

**对现有系统的影响：**

- 新增类型和序列化，现有代码零变更
- `mcp/server.go` 的 `readResource` 解析器已经部分实现了从 URI 反解 `{tenant, bucket, key}`，可以复用 `CanonicalRef.Parse()`
- `Agent` tool 中硬编码的 `DefaultBucket` 需要改为可配置参数
- OpenAPI schema 新增一个字段（非破坏性变更）

### 方向四：搜索一致性 SLA 度量

这与方向一的可观测性相关但关注点不同：方向一关心*资源争抢*，方向四关心*数据一致性的延迟*。

**为什么需要（业务价值/技术价值）：**

当一个对象被写入（PUT）后，多久可以通过搜索查到？这是 RAG 场景的核心 SLA。当前 Indexer 的事件管道是异步的：`FileService.Put` → `EventBus → Indexer.handle → drainBacklog → Embed → InsertChunks → Search`。没有端到端的延迟度量。

业务价值：面向客户的搜索一致性 SLA（"数据写入后 5 秒内可搜索"）、索引管道性能瓶颈的识别。

**核心挑战和技术难点：**

1. **端到端的追踪。** 需要在 `FileService.Put` 处生成一个 `traceID`，通过 EventBus 传播到 Indexer，在 `Indexer.IndexObjectByID` 完成时记录端到端延迟。Go 的 `context.Context` 可以通过 `Event.Payload` 传递，但当前 `Event.Payload` 是 `map[string]string`，不够灵活。
2. **SLA 违约的告警条件。** 需要定义"从 EventCreated 到 Chunks Inserted 的时间 > 阈值"的 Prometheus 告警规则。
3. **事件管道积压的隔离度量。** `drainBacklog` 的 backlog 深度是一个自然指标。

**预期的架构变更：**

```go
// telemetry/metrics.go 新增以下指标
searchConsistencyLatency := prometheus.NewHistogramVec(
    prometheus.HistogramOpts{
        Name: "search_consistency_latency_seconds",
        Help: "End-to-end latency from object write to chunk indexed (write→search).",
        Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60},
    },
    []string{"tenant"},
)
```

EventBus 传播路径增加时间戳嵌入（非侵入式）：

```go
// Event.Payload 增加 "_indexer_start" 时间戳（由 FileService 在 emit 时设置）
// Indexer 在 handle() 结束时计算 delta
```

**对现有系统的影响：**

- 仅需新增指标注册和记录点（约 60 行）
- 无需 schema 变更
- 不影响 I5 不变量（指标采集不影响业务路径）

### 方向五（新增建议）：对象血缘的内链去重

方向五不是来自四个方向之一，而是我基于代码分析提出的架构扩展建议。

**为什么需要：**

当前 `repository.RecordUsage`（`search.go:255`）每次搜索查询都记录 `ChunkIDs` 和 `ObjectIDs` 到 `usage` 表。在频繁搜索的场景下，这个表会迅速膨胀——每次搜索都记录数百个 chunkID 的引用。`ListUsageForObject` 用于 `/v1/lineage/objects/{id}` 端点，返回"谁消费了这个对象的哪些 chunk"。如果 Usage 表是按每次搜索存储的行，则：

1. 存储量 O(queries × hits) — 快速膨胀
2. 查询 `ListUsageForObject` 的性能随 usage 表增长而下降

**核心挑战：**

需要设计 Usage 表的聚合策略——按 `(query_fingerprint, tenant, day)` 聚合，还是仅记录唯一查询模式。

**对现有系统的影响：**

- `repository.go:RecordUsage` 改为批量 upsert（而非逐条 insert）
- `usage` 表增加 `count` 列（合并相同 query + chunk set 的访问）
- Lineage 端点的返回语义需要增加聚合说明

---

## 3. 接口设计建议

### 3.1 关键接口设计原则

**I1 — 区分"稳定接口"与"实验接口"。** `Repository` 接口（`repository.go` 约 60 个方法）已经是系统的核心抽象。新增方法应考虑：是否应该向接口追加（破坏现有实现），还是使用"可选接口"模式（type assertion）。建议：

- **面向客户的 API**（SDK 客户端方法）：永远向后兼容，新增方法而不是修改签名
- **内部 SPI**（Storage/Repository）：允许在次要版本中扩展（Go 接口的扩展需要通过类型断言或新接口）

**I2 — 装饰器优先于中间件。** 对于方向一的租户隔离 embedder，使用嵌入 Embedder 的装饰器（如 `TenantAwareEmbedder`）比在中间件层嵌入限流更灵活，因为：
- 装饰器可以在单元测试中独立验证（`embedder_test.go`）
- 装饰器可以作为第一类依赖注入（`buildEmbedder` 返回时包装）
- 不影响 HTTP 中间件链顺序（I4 不变量保护）

**I3 — 配置的"尾延迟"显式声明。** AI 组件大量依赖外部 HTTP 服务（embedder/LLM/reranker）。当前仅有 `connect_timeout`、`read_timeout`、`write_timeout`。缺乏：
- `ai_embedd_timeout`（区分于 storage timeout）
- `ai_search_timeout`（搜索操作的总超时）
- `ai_rank_timeout`（reranker 超时）

### 3.2 是否需要新的抽象层

| 抽象层 | 需要程度 | 理由 |
|--------|---------|------|
| **AI 资源管理器** `ai.ResourceManager` | **推荐新增** | 统一管理 embedder/LLM/reranker 的配额、限流、重试、降级。替换 `main.go:80-85` 中的裸变量 `search`/`chat`/`agent` |
| **跨协议对象身份解析** `service.RefResolver` | **P2（低优先级）** | 如果方向三实施，则需要统一的反解析入口。但当前可以使用接收 `CanonicalRef` 的 `FileService` 方法重载 |
| **索引管道监控** `telemetry.IndexPipeline` | **可选** | 方向四的度量可以直接嵌入现有 `telemetry/metrics.go`，无需新包 |

### 3.3 向后兼容性策略

- **SDK 客户端方法重载而非修改。** 添加 `UploadEncrypted`（方向二）而不是修改 `Upload`——这让未升级的客户端不受影响
- **配置的 Opt-in 性质保持。** 所有新设置默认为"off"或"0"（无限制），确保现有部署的兼容性
- **OpenAPI 仅增加新字段。** 添加 `canonical_ref` 到响应体是安全的（JSON 消费者应忽略未知字段）
- **Storage 接口只扩展可选方法。** 如果需要新增方法（如 `Storage.Rewrap` 已存在），通过接口断言检测

---

## 4. 技术选型

### 4.1 是否需要引入新技术栈

| 方向 | 建议 | 理由 |
|------|------|------|
| 方向一（AI 租户隔离） | **无需新依赖。** 使用 `golang.org/x/time/rate`（已在 `middleware/ratelimit.go` 中使用） | 现有基础设施足够。优雅降级策略优于队列 |
| 方向二（SDK 加密） | ① Go：stdlib `crypto/aes` + `crypto/cipher`（已有，免费）；② JS：Web Crypto API；③ Python：`cryptography` 包 | stdlib 优先策略（I6），JS/Python 侧不需要额外的 go.mod 依赖 |
| 方向三（CanonicalRef） | **无需新依赖。** 纯 Go `path` + `strings` 操作 |  |
| 方向四（SLA 度量） | **无需新依赖。** Prometheus Histogram 已在 `telemetry/prometheus.go` 中使用 |  |
| 方向五（血缘去重） | **无需新依赖。** 数据库 upsert 语义 |  |

### 4.2 第三方依赖评估标准

当需要引入新依赖时（当前四个方向均不需要），应当使用 AGENTS.md 的 I6 框架：

```
评估检查表：
1. 是否通过 Go stdlib 不可实现？→ 如果可实现则拒绝
2. 依赖的许可证是否兼容（MIT/APLv2/B SD-3）？→ GPL/Affero 拒绝
3. 依赖的总代码行数是否 < 5000？→ 否则不可审计
4. 依赖在 go.sum 中是否引入了 > 5 个传递依赖？→ 拒绝（过度依赖）
5. 是否有 test-only 替代方案？→ 单元测试应仅用 testing 包
```

### 4.3 自建 vs 采购的决策

| 场景 | 决策 | 逻辑 |
|------|------|------|
| 客户端加密方案 | **自建** | 加密原语（AES-GCM、XChaCha20-Poly1305）是标准化的。自建可以完全控制密钥派生路径和安全审计。第三方（如 Tink、Libsodium）是包装，但增加了依赖度 |
| 多租户限流方案 | **自建** | 基于滑动窗口的令牌桶+租户隔离是约 200 行 Go 代码。商业 API 网关（Kong、AWS API GW）可以作为替代，但对 self-hosted 部署不透明 |
| 搜索一致性 SLA 仪表盘 | **复用 Grafana** | 已有 `deploy/grafana/` 仪表盘。新增 panel 即可 |

---

## 5. 实施路线图

### 5.1 优先级排序

| 优先级 | 方向 | 阶段 | 估算工作量 | 说明 |
|--------|------|------|-----------|------|
| **P0** | 方向四 Phase 1（SLA 度量） | Track A（可观测性） | ~60 行 | 零风险，立即填补盲区。从代码锚点到 `telemetry.RecordSearchLatency` 的路径已存在 |
| **P0** | 方向一 Phase 1（可观测性） | Track A（可观测性） | ~80 行 | 添加 `tenant` 标签到现有指标，立即收益 |
| **P1** | 方向三 Phase 1（CanonicalRef 类型定义） | Track B（架构基础） | ~100 行 | 类型定义+序列化/反序列化方法。不影响现有代码 |
| **P1** | 方向二（SDK 加密） | Track C（安全/合规） | ~500 行跨三套 SDK | 实质性工作，但并行独立。MVP 可以先只做 Go SDK |
| **P2** | 方向一 Phase 2（租户级 embedder 限流） | Track A 延续 | ~200 行 | 需要 `TenantAwareEmbedder` 装饰器 + 配置项 |
| **P2** | 方向五（血缘去重） | 新 Track D | ~150 行 | 长期数据规模优化 |
| **P3** | 方向三 Phase 2（Agent/MCP 去除硬编码 DefaultBucket） | Track B 延续 | ~50 行 | MCP 协议层面小改动 |
| **P3** | `webhook_failures` 清理策略 | 延续 | ~40 行 | Reconciler 中新增过期清理 |

### 5.2 阶段划分和里程碑

```
Phase 1（立即 — 第 1-2 周）
├── 方向四 Phase 1: search_consistency_latency_seconds 指标 → 部署 Prometheus 规则
├── 方向一 Phase 1: 现有指标增加 tenant 标签 → Prometheus 告警中 tenant 维度的搜索延迟
└── 方向三 Phase 1: CanonicalRef 类型定义 + 单元测试
    ├── 里程碑 M1（第 2 周末）: 可观测性指标上线，CanonicalRef 类型可用

Phase 2（第 3-5 周）
├── 方向二 Phase 1: Go SDK 客户端加密实现
│   ├── 密钥派生（环境变量 + KDF AES-256）
│   ├── 流式加密/解密
│   ├── 信封格式（AEAD nonce + ciphertext）
│   └── 单元测试 + 固定测试向量
├── 方向一 Phase 2: TenantAwareEmbedder 装饰器
    └── 里程碑 M2（第 5 周末）: Go SDK 加密可用，多租户限流上线

Phase 3（第 6-8 周）
├── 方向二 Phase 2: Python SDK + JS SDK 客户端加密（与 Go SDK 一致）
├── 方向五: Usage 表去重迁移
└── 方向三 Phase 2: MCP/Agent 去除 DefaultBucket 硬编码
    └── 里程碑 M3（第 8 周末）: 三套 SDK 加密一致，部署无忧
```

### 5.3 风险点和缓解策略

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| SDK 加密方案在三套语言间不一致导致数据不可恢复 | 低 | **灾难性** | ① 统一设计文档定义信封格式和密钥派生路径；② 每套 SDK 测试向量一致（同一密钥+同一明文→同一密文）；③ 跨语言集成测试 |
| 租户级限流配置错误导致服务降级 | 中 | 高 | ① 默认 off（opt-in）；② 降级策略：限流过度降级为无限制并告警，而非拒绝服务；③ `DegradedMode` 配置已存在（`cfg.AI.DegradedMode`） |
| 可观测性指标维度过多导致 Prometheus 性能下降 | 低 | 中 | tenant 基数管控：仅活跃租户（last 24h）注册标签，非活跃租户聚合到 `tenant="inactive"` |
| Indexer 事件管道溢出不通知 | 低 | 中 | 方向四指标上线后自动解决——`search_consistency_latency_seconds` > 阈值触发告警 |
| 方向二的加密实现与 SSE-C 混淆 | 低 | 中 | 文档明确区分："客户端加密 = 在客户端加密明文 → 上传密文" vs "SSE-C = 上传明文 → 服务器使用提供的密钥加密存储"。两者可以共存 |

---

## 总结

当前 aero-vault 架构在整洁性、可测试性、和安全默认配置方面做得非常出色。四个分析方向通过代码层面的验证确认了其有效性和实施价值。

**最关键的建议：**
1. 立即实施 Track A（方向一 Phase 1 + 方向四 Phase 1）——零风险、高可见性收益，弥补当前可观测性的最大盲区：搜索一致性延迟
2. 方向二的客户端加密是企业合规的最强差异化手段，但需要充分的跨语言测试覆盖
3. 方向三的 CanonicalRef 是"当下小投入，未来大回报"——100 行定义一个统一的跨协议对象身份，为后续的审计、检索、迁移打下基础
