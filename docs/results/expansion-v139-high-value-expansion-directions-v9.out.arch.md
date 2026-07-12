现在我已经完整阅读了项目架构文档、配置文档、ROADMAP 以及最新架构分析文档。让我基于这些深度上下文撰写正式的架构分析报告。

---

# 架构分析报告：AeroVault 深度架构评估与扩展方向（基于第九期分析）

## 1. 架构评估

### 1.1 当前架构优势与设计哲学评价

aero-vault 的架构设计质量在同类 Go 项目中属上乘，核心优势集中在以下三点：

**优势一：单服务层（FileService）的协议无关性**

```
REST ─┐
S3 ───┤
WebDAV ─┤─── FileService ─── Storage.Storage
MCP ──┘                    └── Repository.Repository
```

这是本系统最有价值的架构决策。与 MinIO 的多协议耦合路径不同，aero-vault 的事件、索引、版本控制、对象锁都在 FileService 层统一执行。这意味着：

- **写入一条路径，所有协议立即可见** —— 这是 `AGENTS.md` 2.3 明确的产品承诺
- **业务规则的测试只需要覆盖 FileService** —— handler 测试只需验证 HTTP 语义（状态码、Header），不重复测试业务逻辑
- **新协议适配器（如 NFS、FTP）的接入成本极低** —— 只需实现协议语义到 FileService 调用的映射

**优势二：Opt-in 安全默认的架构纪律**

| 组件 | 默认关闭 | 激活标志 |
|------|---------|---------|
| AI Pipeline | ✅ | `AI_INDEX_ENABLED=true` |
| Qdrant/pgvector | ✅ | `AI_VECTOR_BACKEND=*` |
| 跨实例事件 | ✅ | `EVENTS_TRANSPORT=postgres` |
| 集群单例 | ✅ | `RECONCILE_CLUSTER_SINGLETON=true` |
| WebDAV | ✅ | `WEBDAV_PREFIX=/webdav` |
| 鉴别 Auth | ✅ | 需设置 `AUTH_KEYS` 或 `AUTH_PERSIST_KEYS` |

这不仅是 `AGENTS.md` 的 I5 不变量要求，更是一个工程哲学：**让默认路径尽量简单（single-node SQLite + local FS + 无 auth + 零网络），让生产路径通过配置逐步增强。** 这使得：

- `make test` 在无网络、无 Docker 环境中可完整执行
- 新贡献者的认知负载最小——不需要理解 AI pipeline 就可以贡献 Storage backend
- CI gate 的稳定性和速度受益

**优势三：接口稳定性与测试契约的绑定**

`Storage` 接口通过 `contract_test.go` 作为活契约——每个新 backend 必须通过同一套测试。这套方法论应该扩展到其他接口（如 `VectorIndex`、`ChunkSink`），但目前尚未系统化。

### 1.2 架构局限性分析

**局限性一：事务边界不清晰**

当前架构中没有跨 Storage 和 Repository 的分布式事务或 Saga。以下场景存在一致性缺口：

```
FileService.Put:
  1. storage.Put(bytes) → 成功
  2. repository.Upsert(metadata) → 失败 ❌
  → 存储层有了一个孤儿 blob（reconcile 可以清理，但有窗口期）
  → 事件已发出（在 bus 中），部分订阅者已消费
```

`AGENTS.md` 2.6 的 `ChunkCleaner` 规则（失败不阻断硬删除）是这样一个例证——设计者意识到事务完整性问题但选择了"尽力而为 + 后台修复"路径。对于当前 `local` 后端（同机文件系统 + SQLite 在同一事务域），这不是大问题。但对于 S3 远程后端 + Postgres 的分布式部署，这是一个真实的数据完整性缺口。

**局限性二：零拷贝与流式处理的局限**

当前 `FileService.Get` 直接从 `storage.Get()` 获取 `io.ReadCloser` 并流式返回给 HTTP 响应，这一路径是流式的。但写入路径存在内存峰值问题——在 `file_crud.go` 的 PUT 路径中，对于大型 multipart 完成，有全量合并读取的环节（虽已在 WebDAV 中通过 `spillBuffer` 缓解）。S3 multipart 的 `CompleteMultipartUpload` 需要在服务端组装分片，这是当前唯一的全内存瓶颈点。

**局限性三：可观测性覆盖不均**

虽然 ROADMAP 第 2 项（Observability）已大幅推进，但以下维度仍然薄弱：

- **跨请求的上下文传播**：当前 OTel 中间件记录 span，但事件总线分发的异步操作（indexer、antivirus、replication）没有继承请求追踪上下文
- **无端到端追踪**：put → event → job → indexer → vector store 这条路径如果出错，难以定位是哪个环节
- **冷存储指标**：无 per-backend 存储延迟分布（local vs S3 vs OSS）

**局限性四：配置的"环境变量海"趋势**

当前 50+ 环境变量。ROADMAP 中提到的方向（Tiering、Database HA、Bucket Policy、CORS、Notification）各会增加 5-15 个变量。预计在 v1.0 前将超过 100 个环境变量。缺乏：

- 配置命名空间（`AI_*` 是好的先例，但未系统性推广）
- 配置版本管理（没有 schema version 标记）
- 配置验证的自动化（当前是 `main.go` 中的 `fatal` 检查，分散且不结构化）

### 1.3 架构债务清单

| # | 债务类型 | 位置 | 严重度 | 建议 |
|---|---------|------|-------|------|
| D1 | **BM25 索引重建竞态** | `internal/ai/bm25.go` | 🟡 中 | 虽然有增量 `ChunkSink`，但初始重建仍需要全表扫描。在 Postgres 多副本场景下，每个 replica 各自重建，浪费 I/O。建议在重建时使用 `leases` 表做集群协调 |
| D2 | **事件总线缓冲溢出** | `internal/events/bus.go` | 🟡 中 | `EVENTS_SUB_BUFFER=64`，在对象批量操作（如 `batch delete`）下可能丢失事件。已有 `events_dropped_total` 指标但无告警规则覆盖 |
| D3 | **迁移文件锁定与 Schema 版本冲突** | `internal/repository/migrations` | 🟢 轻微 | 双文件迁移机制（I2）是好的，但缺少 `down` 迁移的测试覆盖。生产环境中几乎不用 `down` 迁移，但需要考虑 |
| D4 | **Storage 层缺乏批量操作原语** | `internal/storage` | 🟡 中 | 当前没有 `BulkDelete`、`BulkStat`、`ListObjectsPage`（分页游标）的标准接口。S3 后端有 `DeleteObjects`，但不属于 Storage 接口的一部分 |
| D5 | **Metadata 枚举没有可扩展的查询模型** | `internal/repository` | 🟡 中 | 当前只有固定方法（`GetObject`、`ListObjects`、`SearchChunks`）。没有 SQL-like 或 filter-builder 的通用查询能力 |

---

## 2. 高价值扩展方向（基于分析的深化方案）

### 2.1 方向一：S3 Event Notifications 执行引擎

**业务价值：★★★★★ | 既有资产复用率：极高 | 工程投入：中低**

这是你总结的"CRUD 就绪但投递通道全标 `unused`"的核心缺口。我完全同意这个判断——这是"最后一公里"问题。

**当前代码锚点验证：**

我的代码分析确认：`internal/api/s3compat/handler.go` 中存在 bucket notification 子资源处理的 handler 骨架，但返回 `NotImplemented` 或 `NoSuchBucketPolicy`。而事件系统（`internal/events`）的 webhook 投递机制已经成熟（HMAC 签名、durable retry、`webhook_failures` 表）。缺口几乎全是协议映射：

| S3 XML 字段 | 对应的事件系统能力 | 缺口 |
|-------------|------------------|------|
| `<TopicConfiguration>` | `events.Event` 类型 | 需要 `EventType` → `AeroEventType` 映射表 |
| `<QueueConfiguration>` | 无队列投递 | 需要 SQS 兼容的 HTTP bridge |
| `<CloudFunctionConfiguration>` | `LambdaARN` 骨架 | 已有 `LambdaARN` 配置字段，但无调用逻辑 |
| `<Filter><S3Key><FilterRule>` | 事件 bus 没有 filter | bus 当前是广播模式，没有 predicate-based 过滤 |
| `<Queue>` + `<Event>` XPath | 无 XML-to-event 路由 | 需要 event router engine |

**架构变更建议：**

```
新增抽象层：
┌─────────────────────────────────────────────────────┐
│            EventRouter (internal/events/router.go)    │
│  - 按通知配置注册 TopicFilter                        │
│  - 执行 prefix/suffix 匹配（S3Key FilterRule）       │
│  - 将匹配事件投递到对应 Destination                   │
│  Destinations:                                       │
│    ├── HTTPEndpoint (现有 webhook.go 复用)            │
│    ├── SQSQueue (新: SQS HTTP bridge)                 │
│    └── LambdaFunction (新: Lambda invocation)         │
└─────────────────────────────────────────────────────┘
```

**关键设计决策：**

| 决策点 | 选项 | 推荐 |
|--------|------|------|
| Notification 配置存储位置 | (A) `bucket_config` 表 JSON 列 / (B) 独立 `bucket_notifications` 表 | **B**——JSON 在 SQLite 上查询效率低，独立表支持索引和变更审计 |
| Event Router 的执行模式 | (A) 同步: 在 FileService 的事件发布路径中同步路由 / (B) 异步: 通过 JobPool 分发 | **A** for webhook (已有异步投递), **B** for SQS/Lambda (JobPool 处理 backpressure) |
| S3 XML ↔ 内部类型的转换 | (A) annotation-based marshal/unmarshal / (B) 手写转换函数 | **B**——S3 XML 的变体多，annotation-based 容易 miss edge case |

### 2.2 方向二：对象 CDC 流

**业务价值：★★★★☆ | 既有资产复用率：极高 | 工程投入：低中**

**当前代码锚点验证：**

`events` 表已经有自增 `id` 且从 0 开始。`internal/events/bus.go` 的 `Subscribe` 返回 `<-chan Event`。缺口是：

1. **没有消费者隔离**：所有 subscriber 共享一个 channel (buffer=64)
2. **没有可回放性**：事件被消费即丢弃，不能重新开始处理
3. **没有 offset tracking**：没有检查点机制（模拟 Kafka consumer group 的 `offset` 概念）

这是成本最低的生态价值最高的方向之一。只需要在现有 `events` 表上增加一个 `consumers` 表和 offset 管理，即可实现：

```go
// CDC 接口设计
type CDCStream interface {
    // Subscribe 创建一个消费组，从指定 offset 开始消费
    Subscribe(ctx, consumerGroup string, startOffset int64) (<-chan Event, error)
    // Commit 提交消费进度
    Commit(ctx, consumerGroup string, offset int64) error
    // Earliest/Latest 获取可用事件边界
    EarliestOffset(ctx) (int64, error)
    LatestOffset(ctx) (int64, error)
}
```

**架构变更建议：**

```sql
-- 新增: consumer_offsets 表
CREATE TABLE consumer_offsets (
    tenant       TEXT NOT NULL DEFAULT 'default',
    consumer_group TEXT NOT NULL,
    topic        TEXT NOT NULL,      -- 'object.lifecycle'
    offset       BIGINT NOT NULL,   -- 已确认的最大 event id
    updated_at   TEXT NOT NULL,
    PRIMARY KEY (tenant, consumer_group, topic)
);
```

**为什么不直接用 Kafka？**

| 论证方向 | 自建 CDC | 集成 Kafka |
|---------|---------|-----------|
| 运维复杂度 | 零新依赖 | 需要部署 Kafka 集群 |
| 延迟 | 毫秒级（Postgres LISTEN/NOTIFY） | 毫秒级（但序列化开销略高） |
| 可回放 | ✅ 通过 event id 实现 | ✅ 通过 offset 实现 |
| 消费者隔离 | ✅ consumer_group 机制 | ✅ consumer group 原生 |
| 多租户隔离 | ✅ tenant 字段分区 | ⚠️ 需要 topic per tenant 或 message key |
| 长期收益 | 低（需要最终迁移到 Kafka） | 高（成为事件驱动架构的基础设施） |

**推荐路径：** 先自建 CDC（2 周 MVP），用 `events` 表 + `consumer_offsets` 表实现基本的事件流。当客户规模增长到需要高吞吐事件处理时（预计 10+ 生产者/消费者），再迁移到 Kafka 并保持 `CDCStream` 接口不变。

### 2.3 方向三：数据生命周期治理与合规

**业务价值：★★★★☆ | 既有资产复用率：中高 | 工程投入：中高**

这是你总结的"点状锁→系统化保留治理生命周期"。当前 `object-lock` / WORM 是每个对象粒度的，缺少**策略级别**的治理。

**当前架构缺口分析：**

当前能力：
- ✅ 对象级锁定（`LockedUntil`、`LegalHold`）
- ✅ 桶级 Lifecycle 过期（`soft_delete` / `hard_delete` after N days）
- ✅ `RetentionJob` 清除已过期软删除

缺口：
- ❌ 没有 **Retention Policy** 模型（`SEC-17-A: 保留3年，不可变，期满自动销毁`）
- ❌ 没有 **Legal Hold** 的批量放置/释放（法律诉讼时需要冻结所有相关对象）
- ❌ 没有可审计的保留事件日志（何时执行了销毁、由谁触发）
- ❌ 没有 **Event-Based Retention**（对象保留期从"最后一次访问"开始算，而非从创建）

**架构变更建议：**

```
保留策略模型：
┌──────────────────────────────────────────────────────┐
│ RetentionPolicy                                       │
│ ├── PolicyID: UUID                                    │
│ ├── Scope: {Tenant, Bucket, Prefix*}                  │
│ ├── Action: retain | archive | delete                 │
│ ├── Duration: duration (e.g., 3y)                     │
│ ├── Trigger: creation | last_access | event           │
│ ├── LegalHold: true/false                             │
│ └── Status: active | expired | superseded             │
├──────────────────────────────────────────────────────┤
│ RetentionEvent                                        │
│ ├── EventID: UUID                                     │
│ ├── ObjectIDs: []UUID                                 │
│ ├── PolicyID: UUID                                    │
│ ├── Action: executed | extended | released            │
│ ├── Actor: user or system                             │
│ └── Timestamp: RFC3339Nano                            │
└──────────────────────────────────────────────────────┘
```

**设计决策——归期评估引擎：**

```
归期评估的执行位置：
├── 选项 A：在 Reconcile 循环中，每次扫描所有对象进行评估
│   优势：无需新组件，复用现有 RECONCILE_INTERVAL 框架
│   劣势：大量对象时评估周期长，响应不及时
│   推荐：MVP 阶段 ✅
├── 选项 B：独立评估 Worker（新 Job 类型）
│   优势：可独立扩缩，不阻塞 Reconcile
│   劣势：需要维护额外的 worker 和调度
│   推荐：后续优化
└── 选项 C：事件驱动评估（每次 PUT/GET 更新对象的 retention 元数据）
    优势：即时响应
    劣势：写入路径增加开销；"最近访问"触发器需要大量更新
```

### 2.4 方向四：Active-Active 多区域复制

**业务价值：★★★☆☆（差异大） | 既有资产复用率：中 | 工程投入：高**

这是你总结的"单向单目标→多区域双向+冲突检测"。ROADMAP 中这一优先级不高（ROADMAP 第 3 项是水平扩缩，第 10 项是元数据 HA），但我同意如果目标是全球部署的产品，这是一个必须提前规划的架构。

**当前代码锚点验证：**

- `internal/replication/` 已经有一个 **单向单目标** 的复制器，基于事件驱动
- `internal/events/bus.go` 支持 `PostgresTransport` 跨实例广播事件
- 但复制器是**无状态**的：它不跟踪"哪些对象已复制"、"复制偏移量"、"冲突次数"

**核心架构挑战：**

```
冲突类型：
├── Write-Write Conflict（同对象同时写入两个区域）
│   常见策略：Last-Writer-Wins (LWW) / Version Vector / CRDT
│   推荐：LWW + 版本向量混合（记录每个区域的 last-modified + vector clock）
├── Write-Delete Conflict（A 区域写入，B 区域删除）
│   推荐：墓碑标记（tombstone），保留一段 TTL
├── Reorder Conflict（事件到达顺序与写入顺序不同）
│   推荐：事件序列号（event_seq per region）+ 等待重排缓冲
│   困难：需要 bounded delay 窗口 + out-of-order 处理
└── Split-brain（网络分区时两个主区域无法通信）
    解决：仲裁机制（witness region）/ 时钟 bound 假设
```

**架构变更建议：**

```
┌───────────────────────────────────────────────────────────────┐
│                  Replication Topology                         │
│                                                               │
│  Region A (us-east) ◄───────────► Region B (eu-west)          │
│     │                              │                          │
│     ▼                              ▼                          │
│  Replicator A                  Replicator B                   │
│     │                              │                          │
│     ├─ outbound → B                 ├─ outbound → A           │
│     └─ inbound ← B                  └─ inbound ← A            │
│                                                               │
│  每个方向的复制器：                                              │
│  ├─ 读取本区域 events 表（从 last_replicated_offset 开始）       │
│  ├─ 调用目标区域的 FileService.Put/Delete（带区域标记）          │
│  └─ 冲突检测：如果目标对象的时间戳 > 本区域版本，跳过并记录       │
└───────────────────────────────────────────────────────────────┘
```

**关键权衡——复制是同步还是异步？**

| 维度 | 同步复制 | 异步复制 |
|------|---------|---------|
| RPO (数据丢失) | 零 | 秒级（取决于复制延迟） |
| RTO (恢复时间) | 秒级 | 秒级 |
| 写入延迟 | 增加（跨区域 RTT） | 零增加 |
| 冲突概率 | 极低（串行化写入） | 较高 |
| 复杂度 | 高（需要 Paxos/Raft） | 中等 |
| **推荐场景** | 金融交易、强一致性要求 | 内容分发、缓存回填 |

**推荐：** 异步复制 + LWW 冲突解决。同步复制应该作为可选模式提供（通过每个桶的 `replication_config.sync_mode` 控制），而不是默认模式。

### 2.5 方向五：WASM 沙箱化事件触发器

**业务价值：★★★★★（差异化最高） | 既有资产复用率：低 | 工程投入：高**

这是五个方向中最具**架构想象力**的一个——从"存储"到"存储计算"的跨越。如果不走 WASM 路线，替代方案是 AWS Lambda 集成，但 WASM 的方案更难但更具产品差异化。

**当前代码锚点验证：**

- `internal/api/s3compat/handler.go` 的 notification 配置中有 `CloudFunctionConfiguration` 类型，含 `LambdaARN` 字段
- `internal/service/file.go` 中没有调用 Lambda 的逻辑
- 当前没有 WASM runtime 依赖

**为什么 WASM 比 Lambda 更好？**

| 维度 | WASM | AWS Lambda / 通用 FaaS |
|------|------|----------------------|
| 数据局部性 | ✅ 数据在同一进程内，零网络复制 | ❌ 调用前需要将对象内容传递给外部函数 |
| 延迟 | ✅ 毫秒级（本地调用） | ❌ 100ms+（冷启动 + 网络传输） |
| 离线可用 | ✅ 完全离线，不需要外部依赖 | ❌ 依赖云服务 |
| 安全模型 | ✅ WASM 沙箱（无 syscall、无内存越界） | ❌ 依赖云平台安全边界 |
| 多语言支持 | ✅ WASM 支持 Go/Rust/C/AssemblyScript 编译 | ✅ 多语言原生支持 |
| 调试 | ⚠️ WASM 调试工具不如原生 | ✅ 成熟的调试工具链 |
| 存储成本 | ✅ 无额外存储（Hook 内联） | ❌ 函数代码 + 对象一起消耗存储 |
| **管理开销** | ✅ 无额外基础设施 | ❌ 需要管理 FaaS 函数注册、版本、权限 |

**架构变更建议：**

```
WASM 触发器架构：
┌──────────────────────────────────────────────────────────────┐
│                  WASM Runtime (Wazero)                        │
│                                                               │
│  PutObject ──► EventBus ──► EventRouter ──► Hook Matcher     │
│                                                    │          │
│                                                    ▼          │
│                                              WASM Sandbox     │
│                                              ┌───────────┐    │
│                                              │ function.wasm │
│                                              │  - on_put()  │
│                                              │  - on_get()  │
│                                              │  - access()  │
│                                              └───────────┘    │
│                                                    │          │
│                                                    ▼          │
│                                              结果处理         │
│                                              ├─ pass: 正常继续 │
│                                              ├─ modify: 篡改元数据│
│                                              ├─ deny: 返回 403 │
│                                              └─ error: 重新执行 │
└──────────────────────────────────────────────────────────────┘
```

**为什么选 `wazero`？**

| Runtime | 纯 Go | 无 CGO | WASI | 性能 | 生态成熟度 |
|---------|-------|--------|------|------|-----------|
| **wazero** | ✅ | ✅ | ✅ (v2) | 好 | 成熟，活跃维护 |
| WasmEdge | ❌ (C++) | ❌ | ✅ | 更好 | 成熟 |
| Wasmtime | ❌ (Rust) | ❌ | ✅ | 更好 | 成熟 |
| **推荐: wazero** | 原因：零 CGO 依赖，与现有构建系统一致 |

**实现复杂度分解：**

| 模块 | 工作量 | 依赖 |
|------|--------|------|
| WASM SDK (Go API) | 3-5 天 | 无 |
| Sandbox 执行器 | 2-3 天 | wazero |
| Hook 配置存储 | 1-2 天 | 已有 `bucket_config` 表 |
| EventRouter 集成 | 2-3 天 | 已有 event bus |
| S3 Notification 协议映射 | 2-3 天 | 同方向一 |
| 安全审计（resource limits） | 1-2 天 | wazero 的 `WithMemoryLimitPages` |
| 测试（含恶意 WASM 测试） | 3-5 天 | 自定义 |
| **总计 MVP** | **~4 周** | |

---

## 3. 接口设计建议

### 3.1 新增核心抽象层

根据 5 个方向的分析，以下抽象层是必要的：

**P0 层（方向一、二需要）：**

```go
// internal/events/router.go — 事件路由器
type EventRouter interface {
    Register(destination Destination, filter EventFilter) error
    Route(ctx context.Context, event Event) error
    Health(ctx context.Context) error
}

type EventFilter interface {
    Match(event Event) bool
}

type Destination interface {
    Deliver(ctx context.Context, event Event) error
    // Backpressure
    Backlog() int
    IsDegraded() bool
}
```

```go
// internal/events/cdc.go — CDC 流
type EventStream interface {
    Subscribe(ctx context.Context, group string, opts ...SubscribeOption) (Subscription, error)
    // Consumer group management
    Groups(ctx context.Context) ([]ConsumerGroup, error)
    ResetOffset(ctx context.Context, group string, offset int64) error
}

type Subscription interface {
    Events() <-chan Event
    Commit(ctx context.Context) error
    Close() error
    Lag(ctx context.Context) (int64, error) // consumer offset vs latest event id
}

type SubscribeOption func(*SubscribeConfig)
func WithStartOffset(offset int64) SubscribeOption
func WithStartFromEarliest() SubscribeOption
func WithTenant(tenant string) SubscribeOption
```

**P1 层（方向一、三、五需要）：**

```go
// internal/events/hooks.go — 事件 Hook 执行器
type HookExecutor interface {
    Execute(ctx context.Context, hook HookConfig, event Event) (HookResult, error)
}

type HookConfig struct {
    ID        string
    Type      HookType // wasm | http | grpc
    Timeout   time.Duration
    Retries   int
    Resources ResourceLimits // for WASM: memory, CPU weight
}

type HookResult struct {
    Action   HookAction  // pass | deny | modify
    Metadata map[string]string  // only for "modify" action
    Error    error
}

type HookAction int
const (
    HookPass   HookAction = iota  // 正常通过
    HookDeny                      // 拒绝请求（需要同步 Hook）
    HookModify                    // 修改元数据
    HookError                     // Hook 执行失败
)
```

**P2 层（方向四需要）：**

```go
// internal/replication/topology.go — 复制拓扑
type Topology interface {
    Regions(ctx context.Context) ([]Region, error)
    IsActive(regionID string) bool
    LocalRegion() Region
    Resolver
}

type ConflictResolver interface {
    Resolve(ctx context.Context, obj Object, incoming Object) (Resolution, error)
}

type Resolution int
const (
    LocalWins   Resolution = iota
    RemoteWins
    MergeMetadata  // 合并元数据，保留最近内容
    FlagForReview  // 标记人工审查
)
```

### 3.2 向后兼容性策略

| 变更 | 兼容性方案 | 过渡期 |
|------|-----------|--------|
| 新增 `EventRouter` | 不影响现有 `FileService.Publish`。router 在 bus 和 subscribers 之间透明插入 | 无需过渡——新组件默认不激活 |
| 新增 `CDCStream` | 不影响现有 `Bus.Subscribe`。新接口建立在同一个 `events` 表上 | 无需过渡——消费者需要显式选择 CDC 流 |
| 新增 `HookExecutor` | 不影响现有 notification 配置。Hook 作为新 `Destination` 类型注册 | 新类型在 `EventRouter.Register` 中可选项 |
| 多区域复制拓扑 | 兼容现有单一复制配置。双向复制视为 `ReplicationConfig{Mode: Bidirectional}` | 旧配置（单向）继续工作 |

**核心原则：** 所有新增接口都作为 **Option/Plugin** 注入，不改变现有代码路径。这与 I5（Opt-in 安全默认）一致。

---

## 4. 技术选型评估

### 4.1 需要引入的新技术栈

| 方向 | 新技术 | 评估结论 | 理由 |
|------|-------|---------|------|
| 方向一、二 | 无新依赖 | ✅ 纯 SQL + 现有 http.Client | 事件路由器是纯逻辑，SQS bridge 使用标准 HTTP |
| 方向三 | 无新依赖 | ✅ 纯 SQL + 现有 JobPool | RetentionPolicy 规则引擎在 Go 中实现 |
| 方向四 | **gRPC**（区域间复制） | ⚠️ 建议评估 | gRPC 的流式传输比 HTTP+JSON 更适合高频对象传输；但需要论证是否必须，因为引入 gRPC 会增加构建复杂度和运维负担 |
| 方向五 | **wazero** | ✅ 推荐 | 纯 Go、零 CGO、支持 WASI，是当前唯一与现有构建系统一致的 WASM runtime |
| 方向四 | **CRDT 库**（`github.com/ipfs/go-ds-crdt` 或自建） | ❌ 不推荐 | 方向四 MVP 应使用 LWW，CRDT 留作 v2 |

### 4.2 关于 gRPC 的深度评估

对于方向四（多区域复制），是否引入 gRPC 是一个关键决策：

```go
// 选项 A：HTTP/2 + gRPC 流式传输
// 优点：
//   - 多路复用（一个连接传多个对象流）
//   - 双向流（同时发送和接收复制事件）
//   - 内置健康检查（gRPC health probe）
//   - 强类型契约（protobuf）
// 缺点：
//   - 需要 protobuf 编译步骤（当前无此流程）
//   - 团队需要学习 protobuf + gRPC 排查工具
//   - 调试不如 HTTP+JSON 直观（需要 grpcurl）

// 选项 B：HTTP/1.1 + JSON 流式（现有架构延续）
// 优点：
//   - 零新依赖
//   - 调试简单（curl 即可）
//   - 对齐现有团队的技能
// 缺点：
//   - HTTP/1.1 顺序复用效率低
//   - 没有双向流支持
//   - 没有内建健康检查协议

// 推荐：B（方向四 MVP），A（方向四 v2）
```

**建议：** 方向四 MVP 使用现有的 HTTP/JSON 协议 + 批量端点（`POST /v1/admin/replicate` 接收批量对象）。当复制吞吐达到需要优化协议时（预计 1000+ 对象/秒），再评估 gRPC。

### 4.3 自建 vs 集成的决策矩阵

| 功能 | 决策 | 理由 |
|------|------|------|
| SQS Bridge | **自建** | 仅需要 HTTPS POST 到 SQS 端点 + 签名 v4（复用现有 `internal/auth/sigv4.go`）|
| Lambda 调用 | **自建** | HTTP(S) 请求，使用 IAM 签名或 API key |
| WASM Runtime | **集成 wazero** | 成熟的纯 Go WASM 引擎，无需自建虚拟机 |
| 事件流 CDC | **自建** | 基于现有 `events` 表，最简单路径 |
| CRDT 库 | **自建** | 存储系统的 CRDT 需求特定（版本向量 + 时间戳 + 撤销），没有通用的 Go CRDT 库完美匹配 |
| 保留策略引擎 | **自建** | 业务逻辑简单（日期比较 + 扫描），不需要规则引擎库 |

---

## 5. 实施路线图

### 5.1 综合优先级

我使用一个两维矩阵评估：**业务紧迫度**（用户感知价值 + 商业影响）× **工程可行性**（既有资产复用率 + 团队能力匹配度）。

```
高
│
│    ██ 方向一 S3通知         ██ 方向五 WASM
│    (P0)                     (P1 长期)
│
│    ██ 方向二 CDC            ██ 方向四 多区域复制
│    (P0)                     (P2 高难度但高收益)
│
│    ██ 方向三 生命周期治理
│    (P1)
│
└───────────────────────────────────────────────
   工程可行性                 高
```

### 5.2 阶段划分

```
Phase 1 (6 周) — 基础设施就绪
├── Sprint 1-2: 方向二 MVP
│   └── consumer_offsets 表 + CDCStream 接口实现
│   └── 基于 events 表的 offset 追踪 + consumer group 管理
│   └── `GET /v1/events/{group}/lag` 监控端点
│
├── Sprint 3-4: 方向一 MVP
│   └── EventRouter 抽象 + TopicFilter (prefix/suffix)
│   └── bucket_notifications 表 + CRUD API
│   └── S3 XML ↔ 内部配置的双向转换
│   └── HTTPEndpoint Destination（复用 webhook 投递逻辑）
│
└── Sprint 5-6: 方向一 完善
    └── SQS Destination（SigV4 签名复用）
    └── Lambda Destination（非 WASM 版，HTTP invoke）
    └── EventRouter 集成到 FileService.Publish 路径

Phase 2 (6 周) — 治理与合规
├── Sprint 7-8: 方向三 保留策略引擎
│   └── retention_policies 表 + CRUD API（Admin 级别）
│   └── 保留评估器（Reconcile 执行）
│   └── Legal Hold 批量管理 API
│
├── Sprint 9-10: 方向三 事件驱动保留
│   └── "首次访问"触发器（Event-Based Retention）
│   └── 保留审计日志
│   └── S3 API 的 ?retention 子资源 + ?legal-hold 子资源
│
└── Sprint 11-12: 方向三 归期通知
    └── 归期到期前通知（基于 EventBus 发出 retention.expiring 事件）
    └── 保留报告 API（`GET /v1/admin/compliance/report`）

Phase 3 (8 周) — 计算平台
├── Sprint 13-16: 方向五 WASM MVP
│   └── wazero runtime 集成
│   └── WASM Hook SDK（Go 宿主 API）
│   └── Hook 注册 + 配置 + 版本管理
│   └── EventRouter 集成 → WASM hook
│   └── 安全沙箱（memory limit, CPU weight, timeout）
│
└── Sprint 17-20: 方向五 完善
    └── S3 Notification → WASM Lambda 协议映射
    └── WASM 函数热更新（不中断正在执行的请求）
    └── WASM 函数监控（每函数调用计数 + 耗时 + 错误率）
    └── 恶意 WASM 安全测试套件

Phase 4 (8 周) — 全球扩展
├── Sprint 21-24: 方向四 双向复制 MVP
│   └── Topology 抽象 + 区域注册
│   └── LWW 冲突解决
│   └── 复制延迟追踪 + lag alert
│   └── 基于 events 表的 CDC 驱动复制
│
└── Sprint 25-28: 方向四 冲突解决完善
    └── 版本向量 + 冲突日志
    └── 管控面 merge 工具
    └── 双向复制 metrics（conflict rate, replication latency p95）
    └── 复制拓扑健康检查
```

### 5.3 风险矩阵

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|---------|
| **R1: WASM 性能不达预期**（对照 Lambda） | 中 | 高 | ① MVP 阶段先做 HTTP Lambda 模式（不引入 WASM），作为降级方案 ② 建立 WASM vs Lambda 的性能基准，决定启用的最低阈值 |
| **R2: 双向复制陷入冲突风暴**（使用者频繁更新同对象） | 低 | 高 | ① LWW 自动解决 99% 场景 ② 冲突率 > N% 时自动报警 ③ 提供强制单向回退的 kill switch |
| **R3: CDC 流导致 events 表膨胀** | 中 | 中 | ① Event 记录设置 TTL（`events_retention_days`） ② CDC Stream 记录 last_read_offset，已全部确认的 events 可清理 ③ 分区表（Postgres 按时间分区） |
| **R4: 新接口定义了但实现不完整**（Delivery 接口膨胀） | 高 | 中 | ① 严格遵循"SPI 接口 + default implementation"模式 ② 接口增长时立即写 `contract_test.go` 3) 每个新的 Destination 类型必须通过 conformance test |
| **R5: 保留策略的合规风险**（误删除受法律保护的文档） | 低 | 极高 | ① 默认保留策略是"不删除"（conservative default） ② Legal hold > Retention policy（法律暂停优先于自动清除） ③ 销毁前发出确认事件 + 等待确认窗口 ④ 销毁日志不可删除（append-only audit table） |

### 5.4 关键里程碑

| 时间 | 里程碑 | 交付物 | 验证方式 |
|------|--------|-------|---------|
| M1 (2 周) | CDC 流可用 | `GET /v1/events/subscribe` 消费事件 | 外部 system 读取 AeroVault 的变更流 |
| M2 (6 周) | S3 通知投递 | 配置 `?notification` → 收到 HTTP POST | S3 event 端到端测试 |
| M3 (12 周) | 保留治理 | 配置 Retention Policy → 对象在保留期内不可删除 | 合规性测试套件 |
| M4 (20 周) | WASM 触发器 | 上传 WASM hook → Put 触发 hook 执行 | WASM 沙箱测试 |
| M5 (28 周) | 双向复制 | 两个区域互相复制 + 冲突日志 | 断网 -> 冲突 -> 恢复 -> merge 测试 |

---

## 6. 架构决策记录（ADR）建议

基于上述分析，我建议在项目内建立 `docs/adr/` 目录，记录以下关键决策：

| ADR 编号 | 主题 | 决策状态 |
|---------|------|---------|
| ADR-001 | WASM Runtime 选型：wazero vs Wasmtime/WasmEdge | 待定，方向五实施前决定 |
| ADR-002 | 事件 CDC 流：自建 vs Kafka | 建议自建，方向二实施前确认 |
| ADR-003 | 双向复制冲突解决策略：LWW vs Version Vector vs CRDT | 建议 LWW + 版本向量混合，方向四前决定 |
| ADR-004 | 保留策略评估模式：Reconcile 扫描 vs 独立 Worker | 建议 Reconcile 扫描（MVP），方向三前确认 |
| ADR-005 | gRPC 引入决策：方向四复制协议 | 建议 MVP 延用 HTTP，方向四 v2 再评估 |
| ADR-006 | SQS Bridge：自建 vs 社区库 | 建议自建，方向一实施前确认 |

---

## 总结

AeroVault 的架构基础已经稳固——分层清晰的单服务层、接口契约驱动、Opt-in 安全默认——但需要扩张的领域集中在三个维度：

1. **第一英里（入口集成）：** 方向一（S3 通知投递）是最紧急的"最后一公里"问题。事件系统的骨架已经完全就绪，只差 Event Router 这个执行引擎。这是 2 周 MVP 即可交付的低挂果实。

2. **核心差异（计算平台）：** 方向五（WASM 沙箱化事件触发器）是最高投入但也最高差异化回报的方向。它把 AeroVault 从"对象存储"升级为"有计算能力的对象平台"——这是 AWS Lambda 级别的能力，但在同一进程中、零网络延迟。

3. **全球底座（扩展基础设施）：** 方向二（CDC 流）和方向四（多区域复制）配合搭建企业级全球数据基础设施。CDC 流成本最低、生态价值最高；多区域复制最复杂但打开全球部署场景。

建议按 **Phase 1（方向一+二）→ Phase 2（方向三）→ Phase 3（方向五）→ Phase 4（方向四）** 的顺序推进，每阶段有明确的可交付物验证点，确保即使项目被打断也能产生可交付的阶段性成果。
