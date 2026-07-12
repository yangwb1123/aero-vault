现在我对代码库有了全面的了解。以下是我的架构分析。

---

# 架构分析：aero-vault

## 1. 架构评估

### 优势

aero-vault 展示了在文件存储系统中罕见的**架构清晰度**。它做对了几个关键的选择：

**正确的核心抽象。** `FileService` 作为所有协议适配器的单一入口，是当架构决策正确时表现如何的教科书式案例。协议适配器（REST、S3、WebDAV、MCP）是真正精简的——每个适配器都简单地将其协议语义映射到 `FileService` 的方法，没有业务逻辑渗入处理程序层。这意味着通过 S3 写入的对象立即通过 REST 可见，并且所有安全策略（配额、版本控制、对象锁）在一个地方强制执行。

**存储和仓库的正确接口。** `storage.Storage` 接口简洁（~10 个方法），且提供者无关。多后端（local、S3、OSS、COS）通过一个工厂函数注入。同样，`Repository` 接口在 SQLite 和 Postgres 之间提供了清晰的抽象。双层抽象（字节存储 + 元数据仓库）是管理文件系统元数据复杂性的正确方式。

**以租户为第一设计要素。** 存储键模式 `tenant/bucket/key` 简洁且防碰撞。每个存储后端自然支持多租户，无需支持租户感知的配置。X-Aero-Tenant 头传播干净，且作用域密钥的固定防止了租户越界。

**事件驱动架构设计良好。** 事件总线 + 订阅者模式是解耦异步处理（索引、杀毒、复制、Webhook）的正确方式。作业队列增加了有持久重试能力的持久性。索引器在纯事件模式（`JOBS_WORKERS=0`）和基于作业的持久模式之间的可选回退是一个实用的设计选择。

**Opt-in 安全默认设计。** 每个 AI 功能、pgvector、Qdrant、事件、复制、WebDAV 都需要明确配置启用。核心 CRUD 路径在任何选择的功能之前完全可测试。这不仅仅是谨慎——当 AI 提供商故障时，它防止了整个系统的级联故障。

### 局限性

**BM25 索引缺乏持久性是架构级别的技术债务。** 不是"我们可以稍后添加 checkpoint 实用工具"的问题。它暴露了系统关于搜索索引生命周期的更深层未解决的架构问题：
- 在启动时持有写锁（`BuildFromRepo` 调用 `b.mu.Lock()`）期间，搜索查询降级为空结果，无论混合搜索模式是否启用
- 索引按租户逐个构建（没有并行化），对于拥有许多对象的大型租户，启动时间会相应增长
- 优雅关闭时没有触发 checkpoint，因此每次重启都会丢失 BuilderFromRepo 启动后的所有增量更改
- 当前模式是"全量重建或根本不重建"——没有检查点 -> 增量恢复 -> 事件回放的路径

**优雅关闭序列只是一个 HTTP 关闭调用。** 这是一个单一实例系统，其关闭序列仅是 `srv.Shutdown(ctx)` → `bus.Close()` → `shutdownOtel()`。没有：
- 正在进行的请求追踪（进行中的请求被粗暴地终止）
- 分阶段关闭（负载均衡排空 → 正在进行的请求 → SSE 信号 → 工作者耗尽 → 检查点 → 资源清理）
- 管理 shutdown 端点（用于编排平台如 Kubernetes 的 `POST /v1/admin/shutdown`）
- 通知 SSE 客户端即将关闭的信号机制

**标签搜索分页在架构上是不正确的。** 在分页响应上应用客户端过滤意味着 `ListObjectsByTag` 无法跨页面正确迭代，且无法一致地报告 `HasMore`。这不仅仅是代码中的错误——这是仓库接口中的**抽象泄漏**：分页不能由调用方在客户端合成，因为状态（光标）是面向存储的，并且分页边界可能任意对齐。

**多部分版本键分歧是一个数据完整性 bug。** 存储键在 `InitMultipart` 时使用版本 1 创建，但 `InsertObjectVersion`（在 `CompleteMultipart` 期间调用）生成版本 2。结果是一个幽灵 blob 和一个带有错误 storage_key 引用的对象行。

### 架构债务（按优先级排列）

| 债务 | 严重程度 | 分类 |
|------|----------|----------|
| BM25 在重启时丢失全部状态 | P1 | 数据持久性 |
| 优雅关闭缺乏正在进行的请求追踪 | P1 | 运维可靠性 |
| `ListObjectsByTag` 分页在架构上损坏 | P1 | 正确性 |
| 多部分版本键分歧 | P0 | 数据完整性 |
| WebDAV LOCK + Object Lock 不一致 | P2 | 合规性 |
| CheckLockBeforeOverwrite 被多部分绕过 | P0 | 合规性 + 数据完整性 |
| 事件总线总是在进程内（无跨实例抽象） | P2 | 可扩展性 |
| AI 限流是每实例的，不是分布式的 | P2 | 多实例正确性 |
| 双 SQL 迁移文件（sqlite + postgres）的维护负担 | P3 | 开发体验 |
| 没有形式化的 API 版本策略（`/v1` 是隐式的） | P3 | API 治理 |

---

## 2. 扩展方向

### 方向 1：持久化统一搜索索引

**为什么需要：** 当前的三层搜索（内存 BM25、暴力向量扫描、可选的 pgvector/Qdrant）是功能正确但运维脆弱的。BM25 在进程内且是易失性的。暴力向量扫描的延迟与存储大小成线性关系。pgvector 和 Qdrant 的集成感觉像事后添加（它们的适配器独立运行，没有混合融合支持）。生产部署需要一个单一的、持久的、可水平扩展的搜索索引，统一处理和向量检索。

**核心挑战：**
- 索引同步：当搜索索引是外部后端（Meilisearch/Tantivy）时，必须确保持久写入的一致性——"至少一次"可能导致重复，"恰好一次"需要幂等性
- 模式演进：搜索模式（字段、标记器、向量维度）必须与应用程序代码一起版本化，且不同的版本可能同时运行
- 跨租户隔离：搜索索引需要支持租户过滤，要么通过每个租户的索引，要么通过文档级租户标签

**预期的架构变更：**
- 用实现持久搜索后端的 `PersistentIndex` 接口替换 `ChunkSink` 接口（当前由 `BM25` 和 `QdrantIndex` 实现）

```go
// 当前定义（精简）：
type ChunkSink interface {
    UpsertObjectChunks(ctx, objectID, chunks) error
    DeleteObjectChunks(ctx, objectID) error
}

// 新定义：
type SearchIndex interface {
    ChunkSink
    Search(ctx, query, opts) ([]Hit, error)
    Refresh() error              // 强制提交/刷新新写入
    Close() error                // 优雅关闭，刷新缓冲区
    Stats() (docCount, ...)      // 健康检查
}
```

- `ai.Search` 将持有一个或多个 `SearchIndex` 实现，用于混合融合（BM25 + 向量）
- 在启动时验证搜索索引健康状态，如果索引不可用则降级为错误而非静默返回空结果
- 迁移路径：新的 `SearchIndex` 实现包装在启动时使用 `BuildFromRepo` 种子数据的现有 BM25

**对现有系统的影响：** 低。`Search` 结构已抽象了 `ChunkSink`。主要变化是使 BM25 持久化并统一向量后端抽象。一旦新接口就位，当前的内存 BM25 可以保留为 `MemoryBM25` 实现，用于开发/测试。

**建议的实现方法（外部）：** Meilisearch 提供了最直接的优势——它具有原生的 BM25 + 向量搜索（作为混合搜索），易于运行、操作简单，并提供内置的容错能力。Tantivy（通过 `tantivy-go`）提供了更快的性能但更小的社区支持。Elasticsearch 对于文件管理用例来说过于重量级。

---

### 方向 2：分阶段优雅关闭与运维现代化

**为什么需要：** 在 Kubernetes 环境中，Pod 终止信号通常有严格的截止时间（`terminationGracePeriodSeconds`，典型值为 30–60 秒）。当前 15 秒的关闭超时时间，没有正在进行的请求追踪，意味着：
- 用户的活跃 HTTP 请求（尤其是大的对象 GET/PUT）在 15 秒后突然断开
- SSE 客户端在没有通知的情况下断开连接
- 作业工作者提前终止，留下未完成的作业
- BM25 增量放弃（索引数据丢失）
- Lease 释放（用于集群单例）可能过早释放，导致另一个实例在下个周期获取 lease

**核心挑战：**
- 正在进行的请求追踪：`http.Server` 的 `RegisterOnShutdown` 有助于但不追踪活跃的 http.Handler 执行。需要 `sync.WaitGroup` 或原子计数器模式
- 分阶段超时：每个阶段（排空 → 正在进行的请求 → SSE → 工作者 → 检查点 → 资源）应限制自己的时间预算，且总预算不得超过编排平台的截止时间
- SSE 通知：当前 SSE 端点使用 `r.Context().Done()`；主动关闭需要副作用通道或每个 SSE 客户端的中断广播

**预期的架构变更：**

```
// 新：带有命名阶段的 ShutdownManager
type ShutdownManager struct {
    ctx           context.Context    // 带有 maxGracePeriod 超时的父上下文
    inflight      *InflightTracker   // 正在进行的 HTTP 请求的 WaitGroup
    sseBroadcast  chan struct{}      // 关闭信号的广播通道
    bus           *events.Bus
    bm25          *ai.BM25          // 如果混合搜索启用
    jobPool       *jobs.Pool
    // ...
}

// 正在进行的请求的装饰器
func InflightMiddleware(tracker *InflightTracker) func(http.Handler) http.Handler
```

添加：
- `internal/server/shutdown.go` —— ShutdownManager，具有分阶段超时和依赖顺序
- 中件链中的 `InflightMiddleware`（在 Recoverer 之后立即添加，以便每个请求都被追踪）
- SSE `event: shutdown` —— 当阶段 3 触发时发送
- 管理端点 `POST /v1/admin/shutdown` —— 启动具有可选 `?reason=` 的关闭序列
- Kubernetes preStop 钩子在 `deploy/kubernetes/` 清单中的文档

**对现有系统的影响：** 中到低。`runServer` 函数被重构为使用 ShutdownManager，但现存的 HTTP 路由和业务逻辑无需更改。当前关闭序列被替换为分阶段版本。如果当前未设置，GracePeriod 配置将更新默认值为 30 秒。

---

### 方向 3：变更数据捕获（CDC）总线抽象

**为什么需要：** 事件总线已经发布对象创建/删除/更新事件。但目前：
- 消费者是进程内的（订阅通道），没有跨实例分发
- Webhook 系统是单目标的（一个 URL，没有路由）
- 没有事件重放机制用于外部消费者在断线后追赶
- 杀毒、复制和 Webhook 是唯一的外部消费者；没有基础设施用于新的外部集成（审计、通知、数据管道）

形式化的 CDC 层解锁了生态系统的集成能力——实时搜索索引更新、分析管道、外部缓存失效、多云同步。

**核心挑战：**
- 至少一次交付：事件总线是进程内的且会丢弃滞后的消费者；持久 CDC 需要确认和偏移量管理
- 回溯兼容性：现有的事件消费模式不能破坏——订阅者继续工作，新的 CDC 消费者在它们旁边
- 每个租户过滤：CDC 消费者可能需要按租户过滤；当前的全局事件通道没有分区

**预期的架构变更：**
- 新的可选 `EventTransport` 接口（Postgres LISTEN/NOTIFY 已经存在，但抽象成接口）

```go
type EventTransport interface {
    Publish(ctx, event) error
    Subscribe(ctx, handler) error
    Close() error
}
```

- 可配置的 CDC 输出：可插拔的后端（Postgres 逻辑复制输出插件、NATS JetStream、Redis Streams、Kafka）
- 现有的事件消费者（索引器、杀毒、复制、Webhook）转换为带有可配置确认语义的 `CDCProcessor`
- Webhook 成为 CDC 处理器的一个实例，而不是特殊的单例

**对现有系统的影响：** 低到中等。事件总线代码（`events/bus.go`）被重构，但所有现存的 `bus.Subscribe()` 消费者继续工作。Webhook 后台重构以使用 CDC 处理器抽象。引入 `EventTransport` 接口是新增的内容，没有破坏性更改。

---

### 方向 4：分层存储引擎

**为什么需要：** 文件存储部署通常跨越多个性能/成本层级。对象可能首先存储在本地 NVMe 上以便快速访问，在 N 天不活跃后透明地迁移到 S3，最后在归档到 Glacier 之前再迁至 S3 标准存储。目前，所有对象都位于单个 `storage.Storage` 后端。

**核心挑战：**
- 数据迁移一致性：在不中断并发读取的情况下在层级之间移动对象需要小心协调。复制 + 交换方法可能会暂时显示两个副本，而移动方法可能会留下一个窗口，在此期间读取失败
- 层级策略：策略是全局的、按桶的、还是按对象的？存储类元数据已经存在于 `Object.StorageClass` 中，但它是一个带有向后端映射语义建议的字符串，目前没有强制执行
- 混合后端：同一个对象可能跨两个后端拆分（例如，热数据在本地，冷归档在 S3），需要合并读取路径

**预期的架构变更：**
- `storage.Storage` 获得一个可选的 `MoveTo` 方法（或者通过复制 + 删除组成）
- `storage.TierManager` 根据策略调度迁移，通过作业队列执行
- 对象 `StorageClass` 枚举变得更加结构化（`STANDARD`、`STANDARD_IA`、`GLACIER`、`DEEP_ARCHIVE`）
- 生命周期强制执行（`ExpireAfterDays` + `ExpireAction`）可以调用层级迁移而不是仅软删除

**对现有系统的影响：** 高。当前 `storage.Storage` 接口是单一后端的——它隐含地假设一个对象在一个地方。添加层级意味着要么使用组合存储包装器（一个位于多个 `Storage` 实例之上的聚合器），要么更改 `FileService` 以考虑后端选择。`Object.StorageClass` 字段已经是一个前哨字段——添加层级只是赋予它语义。

---

### 方向 5：运行时插件系统

**为什么需要：** 当前扩展点（存储后端、仓库、AI 嵌入器、聊天 LLM、重排序器、杀毒扫描器）都是通过编译时接口和配置变量驱动的。第三方扩展被迫分叉代码库。Go 1.25 的 `plugin` 包（或更好的，通过 gRPC 的子进程）允许独立的扩展生命周期。

**核心挑战：**
- 版本兼容性：Go 插件与调用主机的精确版本编译；任何依赖不匹配都会导致运行时加载失败
- 进程隔离：子进程方法（gRPC/HTTP）提供隔离，但引入部署复杂性——用户必须运行另一个二进制文件
- 生命周期：插件可能崩溃、挂起或消耗过多资源；宿主需要看门狗

**预期的架构变更：**
- 插件注册表：`internal/plugin/registry.go`
- 每个可插拔接口的 gRPC 适配器模式（例如，`storage.PluginAdapter` 将 gRPC 调用委托给子进程）
- 生命周期配置：`PLUGIN_<name>_ENDPOINT` 或者 `PLUGIN_<name>_COMMAND` 用于进程外插件
- 新的文档部分：插件开发指南

**对现有系统的影响：** 低。所有相关接口已经存在，且可以用 gRPC 包装器替换。对于大部分用户来说，编译时接口仍然是默认的。

---

## 3. 接口设计建议

### 优秀的内容

**`storage.Storage` 接口**简洁、提供者无关，并且包含正确的原语（CRUD + 列表 + 预签名 + 多部分）。它是有意保持小型的，这是正确的设计选择。对于分层存储，可以考虑添加一个可选的 `MoveTo(dst Storage, key string) error`。

**`FileService` 作为核心控制器**继续存在，但其方法签名应该形式化正在进行请求的上下文传播。当前，上下文从处理程序流经 `FileService` → 存储/仓库。添加 `InflightTracker` 到上下文链中不会破坏任何东西。

### 应该改变的内容

**`Repository` 接口增长得太大了。** 查看代码——它处理对象、桶、标签、ACL、上传、事件、作业、租户、API 密钥、配置、lease 和预算。这 ~20 个方法混合了不相关领域的关注点。建议的方法：

| 选项 | 描述 | 权衡 |
|--------|-------------|-------|
| A. 保持大接口，但在实现之间共享通用 SQL 代码 | 当前状态。简单但不扩展 | 随着时间的推移，接口变得更宽；新的实现（例如，MySQL）需要更多样板 |
| B. 按领域拆分：`ObjectRepository`、`BucketRepository`、`EventRepository`、`JobRepository`、`AdminRepository` | 更清晰的关注点分离 | 事务变得棘手——跨多个仓库边界的一个对象创建 + 事件发布 |
| C. 命令/查询分离（CQS）——为读使用 `ObjectStore`，为元数据使用 `MetadataStore` | 类似于选项 B，但允许不同的实现（对象用 Redis，元数据用 Postgres） | 对于简单的部署来说过度设计 |

**推荐：选项 C** 但作为可选模式——将主 `Repository` 接口保留做事务一致性，但将其拆分为逻辑接口以从调用者角度考虑：`ObjectReader`、`ObjectWriter`、`BucketManager`、`AdminStore`。`Repository` 组合所有这些。

**`ChunkSink` 接口需要 evolve 为 `SearchIndex`。** 当前 `UpsertObjectChunks` + `DeleteObjectChunks` 仅写入；没有搜索方法。使其成为一个完整的搜索抽象可以解锁搜索后端的可插拔性。

**事件桥需要跨实例抽象。** `Bus.Subscribe()` 返回一个带有不可配置缓冲区的通道——适合进程内消费者，但不适合跨实例分发。引入可选的 `EventTransport` 接口：

```go
type EventTransport interface {
    Publish(ctx, event) error
    Subscribe(ctx, func(event)) error
    Close() error
}
```

进程内总线成为默认的 `MemoryTransport`；`PostgresTransport` 已经存在；`NATSTransport` 和 `RedisTransport` 成为可选实现。

### 保持向后兼容性

首先添加新接口，然后重构现有消费者。具体来说：
1. 添加 `ChunkSink` → `SearchIndex` 演进，带有一个包装现有 `ChunkSink` 的适配器
2. 添加 `EventTransport` 作为可选层；`Bus` 持续使用 `MemoryTransport` 作为默认
3. `Repository` 进入接口拆分时在文件内保留现有的接口定义（调用者可以通过嵌入向下转型）
4. 使用 `internal/compat/` 包为旧接口路径提供过渡桥接，带有弃用警告

---

## 4. 技术选型

### 需要评估的当前技术栈

| 层 | 目前 | 评估 |
|----|-------------|--------|
| HTTP 路由 | `chi` | 优秀。轻量、可组合、大量采用。继续使用。 |
| SQL | `modernc.org/sqlite` + `jackc/pgx` | 都高质量且适合用途。维护双迁移文件是一个负担，但对于支持两个目标来说是必要的。可以考虑迁移工具如 `golang-migrate` 或 `pressly/goose` 以统一加载。 |
| 事件总线 | 进程内 + Postgres LISTEN/NOTIFY | 进程内的对于单实例来说很好。对于多实例需要跨实例。NATS JetStream 或 Redis Streams 将比 Kafka（过重）更合适。 |
| BM25 | 自定义进程内 | 需要持久性。见方向 1 的讨论。 |
| 向量搜索 | 内存 + pgvector + Qdrant | 可接受，但 pgvector 和 Qdrant 适配器是独立的，且没有统一的处理程序。如果 Qdrant 是可访问的，它应该优先于 pgvector 用于纯向量工作负载。 |
| 监控 | OTel + Prometheus | 优秀的选择。继续使用。 |
| SSE | 自定义（从事件总线拉取） | 正确设计。在关闭期间缺少 `event: shutdown` 信号是一个低级修复。 |

### 建议的新引入

**用于运维的可选依赖：**

| 建议 | 理由 | 替代方案 |
|----------|---------|--------------|
| **NATS JetStream** 用于跨实例事件总线 | 轻量、持久、有确认、内置去重。比 Kafka 简单得多，适合文件事件吞吐量。 | Redis Streams（更简单但持久性更差）；Kafka（过重但生态更广泛） |
| **Meilisearch** 用于统一搜索索引 | 原生的 BM25 + 向量搜索，高度可用，类似 Elasticsearch 但更简单许多。搜索索引的单一系统。 | Tantivy（更快但 Go 绑定更少）；Elasticsearch（功能丰富但运维复杂） |
| **Redis** 用于分布式限流 + 缓存 | 现有系统的分布式限流和高速缓存（对象标签、API 密钥、AI 结果）的单一系统。可选的——当前模式可以保留。 | 无（当前模式可工作但缺乏跨实例协调） |

**自建的选择：**

| 决策 | 立场 |
|------|--------|
| **ShutdownManager**（选项 A：自建） | 自建。Go 标准库提供了构建此所有构建块（`sync.WaitGroup`、`context`、`http.Server.RegisterOnShutdown`）。外部依赖不值得为这个特定组件付出。 |
| **持久搜索索引**（选项 B：采购） | 采购。Meilisearch 或类似产品。自定义构建的搜索索引在运维上昂贵，易于出现与 Lucene/Tantivy 已经解决类似的错误，且需要持续的维护。如果 Go 绑定可用，Tantivy 是唯一可行的自建替代方案。 |
| **CDC 事件总线**（选项 C：自建） | 自建插件架构（使用 gRPC 的子进程适配器）。现有的接口已经存在；添加 gRPC 适配器包将进程外扩展连接到主代码库。子进程比 Go 的 `plugin` 包更可取（运行时兼容性问题）。 |
| **分页后标签过滤**（选项 D：修复，不要替换） | 用循环分页（服务层）修复当前的实现。不要迁移到不同的数据库（MySQL/MongoDB 有原生的标签过滤）——对于一对已知的 bug，迁移的代价太高。 |

### 依赖评估标准

在添加新的 `go.mod` 依赖之前，按照以下标准评估：

1. **许可证兼容性**：必须是 MIT、Apache 2.0、BSD 或 ISC。避免 AGPL。
2. **活跃维护**：在最近 12 个月内提交，且有证明的发布历史。
3. **Go API 稳定性**：使用 `go1` 兼容性承诺的包（不是实验性的 `x/` 包，除非有充分的理由）。
4. **可切出性**：如果依赖失败或消失，可以将接口替换为另一个实现。
5. **大小和构建时间**：优先选择无 CGO 的依赖（`modernc.org/sqlite` 是个例外，但特意选择是为了消除 CGO）。
6. **测试隔离**：集成测试必须在 Docker 容器上可运行，但在 `go test ./...` 中不是必需的。

---

## 5. 实施路线图

### 阶段 0：关键修复（P0 — 2-3 周）

这些是数据完整性和合规性问题，必须在任何新功能之前解决。

| 项目 | 估计 | 风险 | 缓解措施 |
|--------|----------|------|-------------|
| **多部分版本键分歧**：在 `buildObjectFromUpload` 中修复 `VersionID` 传播 | 2 天 | 低。限制在 `file_multipart.go`。现有的迁移 `0018_upload_storage_key` 可能用于添加 `version_id` 列，或者更简单的：在完成时生成新的版本 ID 并重写 storage_key。 | 在 CI 中增加多部分 + 版本化的集成测试案例。 |
| **多部分锁绕过**：让 `CompleteMultipart` 在非版本化桶上调用 `checkLockBeforeOverwrite` | 1 天 | 低。单线更改。 | 用锁案例扩展现有的多部分测试。 |
| **标签搜索分页**：在服务层实现循环分页，使用正确的 NextMarker | 3 天 | 低。零模式变更。核心更改在 `sql_objects.go:ListObjectsByTag` 和 `file_features.go`。 | 添加边缘案例的测试：跨分页边界的标签、空标签、部分匹配。 |

### 阶段 1：核心稳健性（P1 — 4-6 周）

| 项目 | 估计 | 风险 | 缓解措施 |
|--------|----------|------|-------------|
| **BM25 checkpoint/restore**：将 BM25 检查点到 `_bm25_checkpoint` 表，在启动时加载，然后增量事件回放 | 2 周 | 中等。需要一个新的仓库方法，谨慎地在 checkout 加载期间处理读锁降级。 | 在启动时开始使用全量 `BuildFromRepo` 作为基线，但在上面叠加 checkpoint 恢复以支持增量重新索引。 |
| **WebDAV 写入路径锁检查**：在 `davFS.OpenFile` 的写入路径中添加 `checkLockBeforeOverwrite` 和 legal hold 检查 | 1 周 | 低。全在 `dav.go` 中。 | 为 WebDAV + 对象锁场景添加集成测试。 |
| **优雅关闭（第 1 部分：正在进行的请求追踪）**：添加 `InflightTracker` 中间件和分阶段关闭 | 2 周 | 低到中等。`shutdownContext` 模式已有；添加正在进行的请求计数、排空阶段和 SSE 通知。 | 使用在关闭期间创建和终止大量请求的测试来测试。 |
| **BM25 搜索索引接口**：将 `ChunkSink` 演变为 `SearchIndex`；将当前的 BM25 包装为 `MemoryBM25` | 1 周 | 低。纯重构，无行为变化。 | 现有的测试应该不变。 |

### 阶段 2：可扩展性基础（P2 — 8-12 周）

| 项目 | 估计 | 风险 | 缓解措施 |
|--------|----------|-------------|------|
| **优雅关闭（第 2 部分：阶段顺序 + 管理端点）**：实现完整的 7 阶段关闭 | 2 周 | 低。基于阶段 1 的工作构建。 | 暂存测试环境中的详细集成测试。 |
| **事件总线跨实例抽象**：`EventTransport` 接口 + NATS JetStream 实现 | 3 周 | 中等。需要 NATS 操作知识。JetStream 消费者的持久订阅语义需要仔细设计。 | 保留进程内总线作为默认以避免引入 NATS 依赖。 |
| **并行化 BuilderFromRepo**：使用工作者池跨桶和租户并行化索引构建 | 1 周 | 低。`BuildFromRepo` 已分页浏览对象；并行化是每种桶/租户组合的 goroutine。 | 在启动时不要超过 `GOMAXPROCS` goroutines。 |
| **分布式限流**：可选 Redis 后端用于限流跨实例协调 | 2 周 | 低到中等。现有的权限桶是每实例的；Redis 滑动窗口使其成为跨实例的。 | 保留每实例权限桶作为默认；Redis 是可选的。 |

### 阶段 3：高级（P3 — 12-20 周）

| 项目 | 估计 | 风险 | 缓解措施 |
|--------|----------|-------------|------|
| **持久搜索索引（Meilisearch 适配器）**：实现 `SearchIndex` 的 Meilisearch 后端 | 4 周 | 中等。需要新的依赖；与 BM25 的混合融合需要 RRF 协调。 | 保留现有的 BM25 + 向量后端作为默认；Meilisearch 是可选的。 |
| **分层存储引擎**：`TierManager` 带有策略驱动迁移 | 6 周 | 高。核心架构变更。需要在存储抽象中引入 `MoveTo` 并处理跨层级的一致性。 | 为可测试性扩展 `storage.contract_test.go`。先在单个本地后端上原型，再添加远程。 |
| **CDC 事件流（Kafka 适配器）**：将事件总线暴露为 Kafka 主题 | 4 周 | 中等到高。Kafka 模式注册表和竞争消费者语义使这个变得复杂。 | 限制在 Kafka 已到位的数据中心部署；为更简单的用例保留 NATS 选项。 |

### 总体路线图可视化

```
Q3 2026               Q4 2026                Q1 2027
│                     │                      │
├ P0 Fixes (3w)      │                      │
├ BM25 checkpoint     ├ EventTransport       │
├ WebDAV lock check   ├ Distributed rl       │
├ Graceful shutdown 1 ├ Parallel BuildFromRepo│
├ BM25 → SearchIndex ├ Graceful shutdown 2  │
│                     ├─── Phase 2 ────      │
├─── Phase 1 ───      │                     ├ Meilisearch adapter
│                     │                      ├ Tiered storage
│                     │                      ├ CDC Kafka adapter
│                     │                      ├─── Phase 3 ──
```

### 风险与缓解措施

| 风险 | 可能性 | 影响 | 缓解措施 |
|------|----------|--------|-------------|
| BM25 checkpoint 与正在进行的搜索冲突（索引构建期间的读锁饥饿） | 中等 | 高 | 在 checkpoint 序列化期间使用 `sync.RWMutex` 锁降级。如果 checkpoint 耗时过长，降级为快照 + 增量重放方法。 |
| 多实例事件分发引入至少一次的交付复杂性 | 中等 | 中等到高 | 使用 NATS JetStream 的恰好一次语义，或在应用程序级别提供幂等消费者。 |
| 分层存储需要一个原子移动操作，这在对象存储中不存在 | 高 | 高 | 对跨层级移动采用复制 + 删除 + 重试方法。对于原子可见性，可以在存储键中引入一个版本标记。 |
| Go 插件 API 在 1.25 中仍然不适合生产使用 | 低 | 高 | 总是使用子进程（gRPC/HTTP）模式用于插件。如果 Go 的 `plugin` 包在不兼容时继续存在问题，将其当作内部工具放弃。 |
| 关闭阶段违反依赖顺序（例如，检查点发生在工作线程死亡之前） | 低 | 中等 | 每个阶段的超时独立；阶段按拓扑顺序执行。使用阶段之间的通道进行通信，而不是使用全局超时。 |

---

## 总结

aero-vault 代码库展示了一个深思熟虑的、可能耗费了大量努力的架构。验证发现验证的五个问题中没有一个是随机的代码错误——它们都是**未完成的架构决策**的自然结果：

- **BM25** 从一开始就打算成为内存索引，从未升级为持久性
- **优雅关闭** 从未从"T 秒的简单关闭"演进为"分阶段排空"
- **标签搜索分页** 忽略了 HTTP API 的抽象边界，应用了延迟过滤
- **多部分版本键** 从未统一过存储键和元数据 ID 的生成
- **WebDAV 锁** 完全从不同的安全域（Object Lock/WORM）构建，从未桥接

好消息是：**架构本身是正确的**。每个问题都可以通过演变现有抽象而不是重新设计它们来修复。P0/P1 修复是直接了当的，且关键路径上的零技术债务。系统在更广泛的生产部署中维护良好的核心设计值得投入阶段 2 和 3 的工作。
