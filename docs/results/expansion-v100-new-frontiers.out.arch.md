现在我已全面审查了实际代码库。以下是我的架构分析。

---

# 架构分析：Aero-Vault 对象存储

## 1. 架构评估

### 1.1 当前架构的优势

**清晰的洋葱层架构。** 代码库实现了严格的层级分离：

```
协议适配器（薄层）→ FileService（核心编排）→ Storage（后端抽象）+ Repository（元数据）
```

这种分离证明了其设计质量：`internal/storage/storage.go` 中的 `Storage` 接口定义了 12 个方法（Put/Get/Stat/Delete/List/PresignGet/PresignPut/Multipart CRUD + Backend），且实现（local/S3/OSS/COS）均可插拔。`FileService` 是**唯一的业务逻辑入口点**，所有四个协议适配器（REST、S3 兼容、WebDAV、MCP）都接入它，没有绕过行为。

**生产就绪的可观测性。** 服务嵌入了 15 个 OpenTelemetry 指标、具有可配置超时的结构化 `slog` 日志记录、以及一个带自定义 statusWriter（支持 `http.Flusher`，因此 SSE 可工作）的请求级访问日志中间件。Prometheus `/metrics` 路径带有 Grafana 仪表板模板，属于专业级别。

**优雅的可选功能模式。** AI/RAG 流水线、pgvector/Qdrant 集成、事件传输、复制和 WebDAV——所有这些都默认关闭（`nil` 嵌入器/LLM/重排序器不会破坏核心 CRUD）。这遵循了 I5 不变量（选择加入安全默认值），并允许以最小配置进行 CI 测试。

**加密信封设计考虑了密钥轮换。** `envelopeEncrypter` 将每个对象的数据密钥封装在由主密钥 KEK 包裹的 AES-256-GCM 下，记录 `kid` 以便在读取时解析。启动时的 Rewrap 逻辑（`maybeRewrapSSE`）可以在不接触对象 Blob 的情况下将现有信封迁移到新的主密钥——这是对象存储加密的专业级设计。

**作业系统具有回退/重试/占用人回收。** `jobs.Pool` 具有指数回退（±25% 抖动）、紧急作业回收（`ReapStuckJobs`）以及对已崩溃处理器的 panic 恢复。队列支持每个作业的可选 `MaxDepth` 背压和 `DedupeKey`。

### 1.2 架构债务与技术债

**写入原子性缺口（P0 层面）。** `writePutObject` 中 repo 写入失败会在存储中留下孤立的 blob——`s.logger.Error` 和 `fmt.Errorf` 返回错误，但 blob 没有被清理。加密路径中还有第二个缺口：`local.WriteObject` 首先写入临时文件，但如果 `writeMeta` 随后失败，则会通过 `os.Remove(path)` 回滚。`emit()` 调用可能失败，但仅在 warn 日志级别记录。

**加密路径缓冲整个对象。** 因为 AES-256-GCM 需要整个密文来验证其认证标签，所以 `encryptReader` 和 `decryptReader` 都会对整个对象调用 `io.ReadAll`。对于大于几百 MB 的对象，这意味着 2× 内存使用量（一进一出），在非加密路径上则没有问题（`os.Open` → 流式）。文件中的注释正确地指出了这一点，但缺乏迁移到 AES-CTR + HMAC 分块的时间表。

**每租户隔离不够细粒度。** 事件总线是全局的——一个嘈杂的租户可以通过 backpressure 丢弃其他租户的事件（`bus.dropped` 是全局计数器）。作业系统的 `ClaimJob` 使用 `ORDER BY priority DESC, id ASC`，并且已有 `priority` 字段，因此每租户加权循环可以不用 schema 迁移就实现。并发限制器通过 `PerTenantConcurrencyLimiter` 具有每租户跟踪，但事件总线和作业池则没有。

**由于 opt-in 默认值导致缺少重试层和缓存。** Circuit breaker 是 opt-in（`Enabled: false`），并且没有 Storage 调用的重试包装器。S3 后端具有超时，但没有内置重试——`factory.go` 显示可以以类似的包装器模式添加重试层。没有元数据缓存（每次 `Stat` 都访问 repo），没有对象体缓存（result_cache 仅限于搜索）。

**中间件链组装顺序与文档不同。** 实际的执行顺序是：

```
AccessLog → ConcurrencyLimiter → Recoverer → OTel → RateLimit → Tenant → Auth → CORS → RequestID → Handler
```

Review 文档声称它是 RequestID → CORS → Auth → Tenant → RateLimit → OTel → Recoverer → AccessLog（这实际上是外层在内部包装的相反顺序，但 ConcurrencyLimiter 缺失了）。更重要的是，`Rest.NewRouter` 在其子路由上应用了自己的 `mw.Auth`，导致双重身份验证——功能上幂等，但增加了延迟。

**校验零碎分散。** 没有集中的请求验证层——每个 handler 都解析自己的参数，`FileService.validateKey` 在业务逻辑层面而非入口层面运行。`openapi.json` 通过 `OpenAPISpecHandler()` 暴露，但从未用于运行时验证。

---

## 2. 扩展方向

### 方向 1：写入路径崩溃恢复与写入前日志（P0）

**业务价值：** 防止数据丢失和不一致。在 Storage Put 成功但 Repository 写入失败的情况下，blob 变成孤儿，占用空间但不被索引。在配额后端暂不可用时，配额计数会发生偏移。

**核心挑战：**

- 引入写入前日志（WAL）步骤意味着每个写入操作都需要两次额外的持久化操作（journal insert + journal delete）。在 SQLite 中，如果使用单个事务，日志行会在提交时自动清理，但在 Postgres 中，需要显式的 GC。
- 现有的 emit-after-repo-write 模式必须保持不变或迁移到 Journal-then-emit。

**提议的架构变更：**

```
当前：Storage.Put → Repo.Write → emit
提议：journal.insert → Storage.Put → Repo.Write → journal.delete → emit
```

应将 `file_crud.go:writePutObject` 修改为将 `emit` 包装在 at-least-once 语义中，其中已持久化事件的 ID 记录在 journal 中。一个后台回收器会清理已完成的 journal 条目。

**对现有系统的影响：**

- 存储接口无需更改。
- FileService 需要一个新的 `Journal` 依赖项（一个具有两个方法的接口：`Insert(ctx, WriteJournal) error` 和 `Delete(ctx, id) error`）。
- `POST /v1/files` 和 `PUT /v1/files` 延迟会因一次额外的预写而增加（在 NVMe 上可忽略不计）。
- 所有 Storage 后端都能从中受益，因为修复位于 FileService 层，而非 Storage 层。

**备选方案：**

| 方案 | 权衡 |
|------|--------|
| **两阶段提交**：在 Storage/Repository 之间使用 Prepare/Commit | 需要对 Storage 接口进行重大更改，对 S3 等远程后端有状态 |
| **后台回收器**：定期扫描孤立的 blob 并与 repo 行进行协调 | 已经以 `reconcile.New` 的形式存在；`DeleteOrphanBlobs` 已经处理此问题，但时间窗口为分钟级而不是秒级 |
| **写入前日志（推荐）**：短生命周期 journal 表 + 回收器 | 最简单的实现；新增 1 个接口，在 FileService 中新增约 40 行 |

---

### 方向 2：每租户 QoS 与后台作业隔离（P1）

**业务价值：** 在多租户部署中，一个租户的索引作业洪流不应饿死另一个租户。事件总线上的 backpressure 丢弃应以租户为粒度进行计数和限制。

**核心挑战：**

- 事件总线使用通道广播——要为每个租户创建一个通道，需要总线在添加新租户时懒加载创建通道，并在租户删除时清理通道。
- 作业系统的 `ClaimJob` 已经具有 `priority` 字段——通过租户加权循环比创建每租户 SQL 查询更简单，且侵入性更小。

**提议的架构变更：**

1. **事件总线**：从单个全局通道切片 `[]chan Event` 迁移到 `map[string][]chan Event`（按租户键控）。广播变为：获取事件租户，将其发送到该租户的通道，以及一个全局"所有租户"通道（用于系统事件）。向 `Bus.Subscribe` 添加一个可选的 `tenants ...string` 过滤器。
2. **作业池**：添加一个 `weightedFairScheduler` 包装器，位于 `ClaimJob` 之上，轮询每个租户的待处理作业计数，并选择 `(priority, pendingCount)` 最高的租户。无需 schema 变更——`priority` 字段和 `ORDER BY priority DESC, tenant_id, id ASC` 就足够了。
3. **并发限制器**：已存在全局 + 每租户限制（`PerTenantConcurrencyLimiter`），但需要推广到后台作业处理。

**对现有系统的影响：**

- 事件总线接口不变（`Subscribe()` 返回 `<-chan Event`），但内部结构变为 `map[string][]chan`。
- 作业池接口不变；调度策略是内部设计更改。
- 租户通道的生命周期管理：需要一种方法来通知总线有关新租户/已删除租户的信息。
- 对于单租户部署应零开销（`Bus` 可以优化为单个全局通道）。

---

### 方向 3：存储重试层与断路器细化（P1→P0）

**业务价值：** 临时性后端故障（S3 限流、网络闪断）目前会直接作为 5xx 错误冒泡。重试层可以将 5xx 错误转换为偶发的延迟峰值。断路器需要按操作进行细化，因为共享断路器会在打开时中断读取和写入。

**核心挑战：**

- 现有的断路器使用单一计数器，且对所有 Storage 操作共享——GET 失败会错误地限制 PUT。
- 重试必须幂等：Storage.Put 如果部分写入会发生什么？使用预先生成的 key + `PutOptions`，并且本地和 S3 后端的 `Put` 在失败时要么完全写入，要么失败（没有部分状态），因此重试是安全的。
- 需要特别考虑超时：30 秒的读取超时对于大文件来说太短了。

**提议的架构变更：**

1. **将断路器拆分为按操作类型：** `circuitbreaker.go` 中的 `CBConfig` 获得 `PerOp bool` 标志，该标志按读/写/列表创建单独的计数器。`CircuitBreaker` 获得一个 `operation` 字段，用于在失败时进行键控。
2. **添加一个 `RetryingStorage` 包装器：** 遵循现有的断路器包装模式——`NewRetryingStorage(wrap Storage, cfg RetryConfig)` 返回一个 `Storage`。
3. **在 `NewFromConfig` 中默认启用重试：** 与 opt-in 断路器不同，重试应默认启用，使用保守的默认值（3 次尝试，指数回退 100 毫秒基准，总共最长 5 秒）。

**对现有系统的影响：**

- 存储接口不变。新的包装器与断路器处于同一级别。
- `config.go` 获得 `Retry{MaxAttempts, BaseBackoff, MaxBackoff}` 块。
- `main.go:buildStorageFrom` 获得一个 if 块：`if retry enabled { store = NewRetryingStore(store, cfg) }`。
- 延迟在重试时增加，但可以绑定（`MaxBackoff` 提供上限）。

---

### 方向 4：元数据缓存与读取路径可靠性（P2）

**业务价值：** 对热对象的 `Stat` 调用（HEAD 请求）目前每次都要查询 repository SQLite/Postgres。添加一个带有短 TTL（1-2 秒）的直写缓存可将读取延迟减少约 1-5 毫秒，并将可观测性后端上的负载减少约 10-20 倍。ETag 验证增加了读取时的信任，但代价是 CPU 开销。

**核心挑战：**

- 写入后一致性：缓存必须在存储 Put 后立即失效。直写模式消除了陈旧读取（在 Put 成功后在缓存中设置），但写入失败会留下陈旧的缓存条目。
- 缓存驱逐策略：简单的 TTL 可能足够（1-2 秒），因为大多数对象很少被修改。
- ETag 验证在 `file_crud.go` 中已经作为 `ReadVerificationConfig` 存在——只是默认关闭。

**提议的架构变更：**

1. **添加一个 `CachingRepository` 包装器：** 位于实际 Repository 周围的装饰器，用 `sync.Map` + TTL 缓存 `GetObject` 调用。
2. **失效挂钩：** FileService 的 `writePutObject`、`hardDeleteObject` 和 `softDeleteObject` 调用 `cache.Evict(tenant, bucket, key)`。
3. **默认启用 ETag 验证：** 对于 <64MB 的对象，使用完整内容验证；对于大型对象，使用采样。

**对现有系统的影响：**

- Repository 接口不变。包装器位于 `repository` 包中，或作为 `internal/cache` 包。
- 对象读取路径获得可配置的缓存层。`config.go` 获得 `Cache{ObjectMetadataTTLSeconds}`。
- 对于大部分冷工作负载，此功能价值不大，可以选择关闭。

---

### 方向 5：API 治理层（P2）

**业务价值：** 集中的请求验证、版本协商和操作级审计。目前，每个 handler 自行解析请求参数，REST 路由硬编码为 `/v1`，且审计日志仅涵盖 admin 操作。

**核心挑战：**

- 在 Go 的强类型系统中引入声明式验证需要代码生成或与运行时类型信息的妥协。`openapi.json` 可以作为单一真实来源，但构建一个对照 OpenAPI spec 验证请求的中间件成本很高。
- 操作级审计将所有 CRUD 操作记录到审计表中——在写入路径上增加约 1 毫秒，并在数据库中产生存储开销。

**提议的架构变更：**

1. **OpenAPI 验证中间件：** 从现有的 `openapi.json` 加载，对照路径/方法 + 参数模式验证每个请求，拒绝格式错误的输入并返回 422，这样 handler 就不需要样板解析代码。
2. **CRUD 审计：** 一个 `AuditMiddleware` 包装了 `FileService` 的所有公共方法，记录每次调用及其持续时间。与现有的 admin 审计不同（在 handler 层运行），这个中间件在业务逻辑层运行，覆盖所有协议适配器。
3. **版本协商标头：** 添加对 `Accept-Version` 标头的支持，映射到内部 API 版本，而无需更改路由路径。

**对现有系统的影响：**

- OpenAPI 验证是有开销的——每个请求的 JSON 解析 + 模式查找。在 Golang 中，一个已编译的正则表达式或模式匹配器可以保持低开销（每个请求约 1-10 微秒）。
- CRUD 审计产生存储和 I/O 开销——对于每个写入/读取操作，写入审计日志行。应使用缓冲异步写入（带 backpressure 的通道）。
- 协议适配器不变。审计位于 FileService 中，自动覆盖所有入口点。

---

## 3. 接口设计建议

### 3.1 设计原则

1. **装饰器模式优于侵入式 API 更改。** 代码库已经证明这是正确的：断路器是一个包装器，`factory.go:NewFromConfig` 将装饰器链接在一起。任何新功能（重试、缓存、审计）都应遵循此模式。
2. **接口应小而专注。** `Storage` 接口有 12 个方法——处于合理大小的上限。任何新方法（例如 `Rewrite`、`Copy`）都应该被讨论，但如果单个后端无法高效实现，则倾向于使用包装器（例如 `CopyingStorage`）。
3. **默认值应该安全。** 重试应默认启用。断路器应默认关闭（保持 opt-in）。缓存应默认启用，使用 1 秒 TTL。
4. **审计应在服务层注入，而不是在 handler 层。** 当前的 admin 审计在 handler 层运行——只有 REST admin 操作被审计。`FileService` 应该有一个可选的 `Auditor` 依赖项，记录每次公共方法调用。

### 3.2 新抽象层

应该引入以下包装器接口，它们都使用 `Storage` 接口：

```go
// 遵循现有的 CircuitBreaker 模式
type StorageWrapper func(Storage) Storage

// 工厂配置使用一个包装器切片
wrappers := []StorageWrapper{
    NewRetryingStore(cfg.Retry),      // 内部重试
    NewCachingStore(cfg.Cache),       // 对象缓存
    NewCircuitBreaker(cfg.CB),        // 断路器（opt-in）
}
```

这保持了 `Storage` 接口的纯净，并允许以声明方式组合。

### 3.3 向后兼容性

所有提议的更改都遵循这些规则：

- **零配置破坏性变更：** 任何带有 `true` 默认值的标志都不得改变现有行为。例如，重试应默认为 **1 次尝试**（无重试），从而与当前行为匹配。用户选择加入 3 次尝试。
- **日志记录路径不变：** 当前 warn/error 日志消息的格式保持不变。新日志使用不同的键（`retry_attempt`、`cache_hit`）前缀。
- **指标向后兼容：** 现有指标不变。新指标使用不同的前缀（`storage_retry_*`、`cache_*`）。

---

## 4. 技术选型

### 4.1 核心依赖原则

该项目目前的 Go 依赖清单合理——`chi`（路由）、`uuid`（ID）、`sqlite`（通过 CGo 或纯 Go 驱动）、`pgx`（Postgres）。新依赖需要按照 I6（标准库优先）进行论证。

| 需求 | 自建 | 第三方 | 理由 |
|-------|---------|-----------|---------|
| OpenAPI 验证 | **自建**（约 200 行解析器 + 类型映射） | `kin-openapi` / `go-swagger` | 自建保持零依赖；OpenAPI 验证是一个薄层（匹配路径 + 检查必需字段 + 类型强制），不验证复杂模式 |
| 内存缓存 | **自建**（`sync.Map` + TTL） | `freecache` / `bigcache` | 现有代码已经通过 `ai.NewCachingEmbedder` 使用 `sync.Map` 进行缓存；对象元数据缓存很小（每个条目约 200 字节），即使有 100 万个对象也只需约 200MB |
| 重试逻辑 | **自建**（约 100 行） | cenkalti/backoff | 重试逻辑简短（指数回退 + jitter），不需要外部依赖 |
| AES-CTR + HMAC 分块加密 | **自建**（替换 `encrypt.go` 中的 2 个函数） | 无 | Go 的 `crypto/aes`、`crypto/cipher`（CTR 模式）、`crypto/hmac` 和 `crypto/sha256` 都是标准库 |

### 4.2 Storage 后端决策矩阵

| 后端 | 适合 | 不适合 |
|-------|---------|------------|
| **Local FS**（默认 ★） | 开发、单节点、CI、边缘部署 | 多节点（没有共享 FS）、>10TB、无备份 |
| **S3** | 生产多节点、>10TB、地理冗余 | 最小延迟（增加约 2-5 毫秒网络延迟）、离线部署 |
| **OSS/COS** | 阿里云/腾讯云部署 | AWS 或本地部署 |

所有提议的更改都与后端无关，在 Storage 接口和之上的包装器层运行。

### 4.3 自建 vs 采购决策

| 决策 | 自建 | 采购 |
|------|---------|---------|
| **写入前日志** | ✅ 自建（约 150 行） | N/A |
| **每租户事件隔离** | ✅ 自建（在 `events/bus.go` 中更改约 50 行） | N/A |
| **重试包装器** | ✅ 自建（约 100 行） | N/A |
| **元数据缓存** | ✅ 自建（约 120 行） | N/A |
| **OpenAPI 验证** | ✅ 自建（约 200 行） | 外部库增加了一个 Go 依赖和一个 OpenAPI 解析步骤 |

在所有情况下，自建是更好的选择，因为代码量小，且保持依赖清单较少。

---

## 5. 实施路线图

### 阶段划分

| 阶段 | 时间 | 方向 | 关键交付物 |
|-------|------|-----------|----------------|
| **Phase 0** | 1-2 天 | 方向 3（重试层） | `RetryingStorage` 包装器、默认启用、可配置回退 |
| **Phase 1** | 2-3 天 | 方向 1（WAL） | `WriteJournal` 接口、journal 表迁移、FileService 集成、回收器 |
| **Phase 2** | 3-5 天 | 方向 2（每租户 QoS） | 每租户事件总线通道、作业池加权调度、每租户并发限制 |
| **Phase 3** | 3-5 天 | 方向 4（缓存）+ 方向 5（治理） | `CachingRepository` 包装器、OpenAPI 验证中间件、CRUD 审计 |

### 详细时间线

**Phase 0 — 存储重试层（P0, 1-2 天）**

1. 在 `internal/storage/retry.go` 中实现 `RetryingStorage` 包装器
2. 将其添加到 `factory.go:NewFromConfig` 包装器链
3. 配置：`STORAGE_RETRY_MAX_ATTEMPTS=3`、`STORAGE_RETRY_BASE_BACKOFF_MS=100`、`STORAGE_RETRY_MAX_BACKOFF_MS=5000`
4. 通过注入故障的模拟 Storage 进行测试

**Phase 1 — 写入原子性（P0, 2-3 天）**

1. 编写迁移：向 `jobs` 表添加 `write_journal` 类型（或使用新模式——复用现有模式更简单，但需要干净的语义）
2. 在 `repository` 中实现 `InsertWriteJournal` / `DeleteWriteJournal` / `ReapWriteJournals`
3. 修改 `file_crud.go:writePutObject`：
   - 在 Storage.Put 之前：`journalID = repo.InsertWriteJournal(ctx, obj)`
   - 在 repo 写入之后：`repo.DeleteWriteJournal(ctx, journalID)`（非阻塞——失败由回收器处理）
4. 实现 `WriteJournalReaper`（类似于 `jobs.Pool.reaper`）——回收早于 `journalTTL` 的已清理 journal 条目和孤立的 journal 条目

**Phase 2 — 每租户隔离（P1, 3-5 天）**

1. 将事件总线从 `[]chan Event` 重构为 `map[string][]chan Event`
2. 添加 `Bus.SubscribeFiltered(tenants ...string)`——使用 `*` 表示所有租户（系统组件）
3. 惰性创建租户通道——当第一个匹配该租户的订阅者注册时创建，当最后一个订阅者注销时删除
4. 实现作业池 `WeightedFairScheduler`——轮询待处理作业计数，选择 `(priority, pendingCount)` 最高的租户
5. 为总线添加每租户 `dropped` 计数器（`map[string]*atomic.Int64`）

**Phase 3 — 缓存 + API 治理（P2, 3-5 天）**

1. 使用 `sync.Map` + TTL 实现 `CachingRepository` 包装器
2. 从 `file_crud.go:writePutObject`/`Delete`/`Upsert` 添加失效调用
3. 实现 OpenAPI 验证中间件：加载 `openapi.json`，编译路径模式，对照 spec 验证每个请求
4. 将 CRUD 审计添加为 `FileService` 中的一个可注入 `Auditor` 接口——记录每次公共方法调用

### 风险与缓解

| 风险 | 概率 | 影响 | 缓解 |
|------|----------|--------|------------|
| WAL 增加了写入延迟 | 中 | 低 | journal.insert 是一次 SQL 写入（<1ms 通常在 SQLite 中）；在 NVMe 上，<0.1ms |
| 每租户事件通道导致内存膨胀 | 低 | 低 | 每个租户通道有 64 个缓冲槽；即使有 10,000 个租户，也约为 10MB |
| OpenAPI 验证成为运行时瓶颈 | 低 | 中 | 在启动时预编译模式；解析是零分配的，并且是 O(path_depth) |
| CRUD 审计日志填满数据库 | 中 | 高 | 使用缓冲通道 + 批量插入（每 100ms 一次或每 1000 条事件一次）；通过 `AUDIT_RETENTION_DAYS` 设置保留期 |
| 重试层掩盖真正的故障 | 低 | 高 | 在 3 次尝试失败后记录详细的错误日志（`attempts=3, first_error=..., last_error=...`） |
| 重试与断路器相互作用 | 低 | 中 | 执行顺序：重试 → 断路器（如果断路器打开，重试不会浪费尝试） |

### 关键检查节点

每个阶段后，运行 `make check`（`gofmt`、`go vet`、`go test ./...`）。对于方向 2，添加针对 `make test-integration` 的 Postgres 集成测试。对于方向 1，使用模拟故障测试写入原子性的测试。
