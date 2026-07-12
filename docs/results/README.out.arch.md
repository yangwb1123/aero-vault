# 架构分析：aero-vault 扩展方向深度评估

基于对代码库的实际阅读（`internal/service`、`internal/storage`、`internal/events`、`internal/reconcile`、`internal/repository`）和提供的分析文档，以下从架构师视角给出系统分析。

---

## 1. 架构评估

### 优势：当前设计的精华

**1.1 分层接口的正交性**

`storage.Storage` 与 `repository.Repository` 是两个高度正交的抽象。前者管理字节的物理存放下标，后者管理元数据的逻辑视图。这种分离使得：

- 任意 Storage backend 可搭配任意 Repository backend（local+S3, S3+Postgres 等）
- `FileService` 不感知底层实现，仅依赖接口
- **验证：** `cmd/server/main.go:162-163` 中 `StorageClassCounts` 的读取直接透传 Telemetry 仪表盘，不需要 storage 参与

这是一个被大多数同类系统（MinIO、SeaweedFS）忽视的设计点——它们倾向于将元数据和存储绑定在同一后端内。aero-vault 的解耦是其最大技术护城河。

**1.2 事件总线的持久化与去耦**

`internal/events/bus.go` 的 `Publish` 先写 DB 再广播的设计是生产级正确的：

```go
func (b *Bus) Publish(ctx context.Context, e repository.Event) {
    id, err := b.repo.InsertEvent(ctx, e) // 先持久化
    if err != nil { return }
    e.ID = id
    b.broadcast(e) // 再广播
    // transport 可选
}
```

这意味着：
- 崩溃时事件不丢失（DB 是源）
- in-process subscriber 可以落后而不会失序（channel buffer + drop，但 DB 仍可回放）
- 跨实例传输（Postgres LISTEN/NOTIFY）通过 `Deliver` 与 `Publish` 分离，防止通知风暴

**1.3 服务层（FileService）作为唯一服务入口**

所有协议适配器（REST、S3、WebDAV、MCP）都调用同一个 `FileService`。这意味着：
- 业务规则（quota、版本控制、WORM）只在一个地方实现
- 跨协议一致性天然存在——一个对象通过 S3 PUT 写入，立即可以通过 REST GET 读取
- 审计/事件发射点集中，不会漏掉

这正确避免了大多数存储系统常见的「每个协议各自实现一套 CRUD」的架构债务。

**1.4 SSE 加密的分层实现**

`internal/storage/encrypt.go` 的 Envelope Encryption 实现（AES-GCM with Key Encapsulation）是在 `LocalStorage` 的 `Get`/`Put` 路径中透明 wrap/unwrap 的。这正确遵循了「加密是存储层关心的事，不是业务层」的原则。

### 局限性与架构债务

**L1. `Repository` 接口过大（Interface Bloat）**

`internal/repository/repository.go` 中的 `Repository` 接口定义了约 **70 个方法**，覆盖了对象 CRUD、Bucket 配置、版本控制、Tags、ACL、Event、Chunk、Job、Quota、API Key、Tenant、Audit、Lease 等所有子领域。这是一个明显的 **God Interface** 反模式：

| 问题 | 表现 | 后果 |
|------|------|------|
| 测试 Mock 膨胀 | 每个 mock 实现必须 stub 全部 70 个方法，哪怕只测 2 个 | 测试维护成本指数级增长 |
| 接口稳定度低 | 任何新特性都往接口加一个方法，无法版本化 | 改动影响力大，回归风险高 |
| 实现耦合 | SQLite 和 Postgres 必须同时跟进接口变化 | 双实现维护负担 |

**修复建议（不立即做，但列入技术债务跟踪）：**

```
Repository (facade interface, ~10 core methods)
  ├── ObjectStore { Get, Put, Delete, List, ... }
  ├── BucketConfig { CreateBucket, GetBucketConfig, SetLifecycle, ... }
  ├── EventStore { InsertEvent, NextUnconsumed, ... }
  ├── JobStore { Enqueue, Claim, Complete, ... }
  ├── AIConfig { InsertChunks, SearchChunks, ... }
  └── AdminStore { PutAPIKey, UpsertTenant, RecordAudit, ... }
```

但这需要一次大规模重构，且当前 SQL 实现是单文件 `sql_objects.go`，拆分接口的同时也要拆分 SQL 文件。**建议在引入 Event WAL 时一并做**——因为 WAL 的实现天然需要一个独立的 `EventStore` 接口，可以作为拆分的突破口。

**L2. 生命周期管理的局限性**

当前 `internal/reconcile/lifecycle.go` 只支持 `ExpireAfterDays`（软删除/硬删除），不支持**存储类转换**（如 `STANDARD → STANDARD_IA → GLACIER`）。但 `storage_class` 字段早已存在于 `Object` 结构体和 `objects` 表中了——这属于：**字段就位但逻辑未实现**的架构欠账。

当前 flow：

```
BucketConfig.ExpireAfterDays → LifecycleJob.sweep → SoftDelete / HardDelete
```

缺少的路径：

```
BucketConfig.Transitions[{Days, TargetClass}] → lifecycle_job → update metadata (no blob move for STANDARD→STANDARD_IA)
                                                                   → move blob to .archive/ (for GLACIER on local)
                                                                   → restore from archive (for GLACIER→STANDARD restore)
```

**L3. 存储类语义在 local 后端的模糊性**

这是文档中正确指出的关键问题。`Object.StorageClass` 在 local 后端当前只是一个**元数据标签**，没有物理行为差异。这与 S3 的语义断裂——在 S3 上 `STANDARD_IA` 意味着不同的冗余/访问层级，在 local 上这些概念没有直接映射。

**L4. AI Pipeline 与 Core CRUD 的生命周期耦合**

当前索引操作是通过 EventBus → JobPool 异步触发的。这本身是正确的选择。但问题在于：

- AI 索引的元数据（chunks、embeddings）存储在 `repository` 的同一 SQL 数据库中，而不是独立的索引存储
- 这意味着 repository 数据库的 schema 变更会影响 AI 索引的可用性
- 并且 repository 的 `SearchChunks` 实现在 `sql_objects.go` 中，与对象元数据混在一起

当索引规模增长到数亿 chunk 时，混在同一 SQLite/Postgres 中的设计会成为性能瓶颈。

---

## 2. 扩展方向

基于文档和代码库的实际情况，列出 5 个高价值扩展方向，按优先级排列。

### 方向 1：存储类生命周期自动转换（P1）

| 维度 | 内容 |
|------|------|
| **为什么需要** | 这是 S3 兼容的核心功能。许多 S3 用户依赖 Lifecycle Policy 来自动降冷/归档以节省成本。当前只支持 expire（删除），缺少 transition（转换）。且代码中 `storage_class` 字段已存在，实现了 Transition 即可完整对齐 S3 Lifecycle API。 |
| **核心挑战** | (1) **local 后端 GLACIER 语义定义**：local 没有不同存储介质，GLACIER 必须定义为「元数据标记 + blob 移动到归档目录 + 禁止直接 GET」；(2) **Restore 流程**：GLACIER 对象需要一个临时的 `restored_copy` 和 `restore_expiry`；(3) **并发限制**：transition 扫描的 LIST 大小和频率需要控制，否则 REPLACE INTO 风暴；(4) **成本可见性**：用户需要知道当前各存储类的分布。 |
| **预期的架构变更** | - `BucketConfig` 增加 `Transitions []StorageTransition{Days, TargetClass}` 字段<br>- `LifecycleJob` 扩展检查 transition 规则，非仅 expire<br>- 新增 `reconcile/restore.go` 处理 GLACIER 恢复（临时副本、过期清理）<br>- `FileService` 增加 `RestoreObject` 方法（对 GLACIER 对象发起恢复）<br>- 在 `main.go` 装配时，`LifecycleJob` 接受 Transition 规则列表<br>- `local` 后端的 `Get` 检查 `GLACIER` 标记，拒绝直接读取（返回 `ErrInvalidStorageClass`） |
| **对现有系统的影响** | 低——大部分代码是新增而不是修改。`service.go` 的 `Get` 路径需要加一个 storage class 检查，但这是可控的。`Object.StorageClass` 已存在，`BucketConfig` 的 `ExpireAfterDays` 已经提供了生命周期配置机制，扩展为 transitions 很自然。 |

**关键设计决策点：**

```
选项 A: LifecycleJob 内嵌 transition + expire 逻辑
  └─ 优点：单一职责；扫描一次 objects 表完成所有检查
  └─ 缺点：LifecycleJob 膨胀

选项 B: 拆为 LifecycleJob (expire) + TransitionJob (transition) 两个独立 worker
  └─ 优点：清晰分离，各自可独立 scale
  └─ 缺点：两次 ListExpired/ListTransitionReady 扫描，DB 压力翻倍
```

**推荐：** 选项 A（过渡期），因为当前 `reconcile/lifecycle.go` 每次只 LIMIT 200 条，扫描开销在可接受范围。未来扫描量 > 10w 时再拆。

### 方向 2：元数据查询引擎（P1，与方向 3 有交叉）

| 维度 | 内容 |
|------|------|
| **为什么需要** | 当前元数据查询能力薄弱：`ListObjects` 只支持 prefix + marker 分页，不支持按 size、storage class、tags、自定义 metadata 过滤。对于大规模存储（百万级对象），缺乏高效的元数据查询引擎导致用户必须全量扫描或依赖外部索引工具。 |
| **核心挑战** | (1) **查询模型的复杂性**：S3 的 Select/ListObjectsV2 只提供 prefix+folder 分页，但 REST API 和用户期望的是 SQL-like 过滤（`WHERE size > 1MB AND storage_class='GLACIER'`）；(2) **索引维护**：在 objects 表上建多字段索引（tenant, bucket, storage_class, size, created_at）会显著增加写入开销，尤其在 Postgres 上；(3) **与 WAL 的一致性**：无 WAL 时只能走 EventBus 异步索引，最大 ~1s 延迟。但元数据查询对一致性要求较高（用户刚写入一个对象，立即查不到？） |
| **预期的架构变更** | - 在 `repository` 中增加 `ListObjectsWithFilter` 方法，支持动态 WHERE 条件<br>- 新增 `internal/query` 包（或并入 `internal/ai`），提供元数据查询接口<br>- REST 路由 `POST /v1/objects/filter` 或 `GET /v1/objects?...` 支持多字段过滤<br>- 可选的元数据索引表：`objects_idx`（物化索引 vs DB 原生索引的选择）<br>- 添加 `QueryCache`（与 AI 的 `resultCache` 共享 `ristretto` 实例） |
| **对现有系统的影响** | 中——需要扩展 Repository 接口（增加方法），影响所有实现。但可以向 `sql_objects.go` 新增方法，扩展有限。 |

**关键设计决策点：**

```
选项 A: PostgreSQL 原生 JSONB 索引 + GIN
  └─ 适用于 metadata 过滤
  └─ 但 SQLite 不支持 GIN 索引，需要在 SQLite 上做 client-side 过滤

选项 B: 独立索引表 objects_idx(tenant, bucket, key, storage_class, size, ...)
  └─ 定宽字段，可统一建 B-tree index
  └─ 写入需同步更新 objects 和 objects_idx 两张表（事务包裹）

选项 C: EventBus 驱动的物化索引（异步）
  └─ 无事务开销，适合大型写入负载
  └─ 但一致性模型为最终一致（~1s 延迟），用户写入后立即查询可能不命中
```

**推荐：** 选项 B 为 baseline (sync, ACID)，选项 C 作为可选的降级路径（针对高写入吞吐场景），通过配置切换 `METADATA_INDEX_ASYNC=true`。同时遵循文档建议：MVP 先走 EventBus 异步路径，不阻塞 WAL。

### 方向 3：Event WAL 实现（P1，方向 2 的依赖项）

| 维度 | 内容 |
|------|------|
| **为什么需要** | 当前 EventBus 虽然持久化事件到 DB，但 `Publish` 是单点操作——没有一个有序的、可回放的事件日志（Write-Ahead Log）。WAL 是实现一致索引重建、跨区复制、CDC 流的基础。没有 WAL，任何「基于事件重建状态」的操作都可能丢事件。 |
| **核心挑战** | (1) **与现有 EventBus 的共存**：当前 EventBus 已承担事件持久化和广播职责。WAL 是底层更强的抽象——EventBus 可以建立在 WAL 之上，但反向不行；(2) **有序性保证**：WAL 需要在跨复制的场景下保证全局有序（至少同一 tenant 内）。(3) **日志压缩**：WAL 如果无限增长，需要 snapshot/compaction 机制。 |
| **预期的架构变更** | - 新增 `internal/wal` 包，提供 append-only 日志接口<br>- `WALEntry{TenantID, Seq, EventType, Payload, Timestamp}` 结构<br>- 实现：文件 WAL（本地）或 DB-backed WAL（Postgres `pg_logical` 或自定义 `wal_entries` 表）<br>- `EventBus` 重构为 WAL 的上层：订阅 WAL 流，而不是自己 insert+broadcast<br>- 新增 WAL 游标管理：哪个 consumer 读到哪条 seq<br>- 元数据索引引擎从 WAL 游标重建索引 |
| **对现有系统的影响** | **高**——这是基础设施级变更。`EventBus.Publish` 需要从「先 insert 再 broadcast」改为「先写 WAL，WAL 消费器广播」。所有事件 consumer（indexer、webhook、replication）都需要适配 WAL 格式。 |

**建议实施路径（分两阶段）：**

```
阶段 1（立即，不影响现有架构）：
  - 在 events 包内部添加 WAL 表（sqlite/postgres 迁移）
  - EventBus.Publish 先写 WAL 表再写 events 表
  - 现有 consumer 继续从 events 表消费，不感知 WAL

阶段 2（独立里程碑）：
  - consumer 迁移到直接从 WAL 读取
  - events 表降级为 WAL 的物化视图
  - 引入 WAL 游标管理
  - 元数据索引从 WAL 游标重建
```

### 方向 4：缓存层（P2，与 SSE-C 交互是关键边界条件）

| 维度 | 内容 |
|------|------|
| **为什么需要** | 当前所有 GET 请求都透传到后端存储。对于高频访问的同一对象，重复的存储读取是浪费。缓存层可以显著降低延迟和后端负载。 |
| **核心挑战** | (1) **SSE-C 降级**（文档正确指出）：SSE-C 对象每请求携带不同密钥，缓存键必须包含 key hash → 无法跨请求共享缓存 → L1/L2 缓存必须对 SSE-C 对象穿透；(2) **缓存失效**：对象更新/删除后，缓存必须立即失效（或容忍短 TTL 的不一致）；(3) **大对象**：缓存大对象（>10MB）导致内存压力和驱逐延迟，需要 streaming passthrough 策略；(4) **L1（内存） vs L2（本地磁盘）** 的两级策略设计。 |
| **预期的架构变更** | - 新增 `internal/storage/caching.go`，实现 `Storage` 接口的包装器（Decorator 模式）<br>- `CachingStorage` 包装底层 `Storage`，添加 L1（ristretto）和 L2（local disk cache）<br>- 对 SSE-C 对象透传（检测 `opts.Headers["x-amz-server-side-encryption-customer-algorithm"]`）<br>- 对大小超过 `CACHE_MAX_OBJECT_SIZE` 的对象透传<br>- 失效策略：写操作（Put/Delete）清除对应 key 的缓存条目<br>- 配置项：`STORAGE_CACHE_SIZE`（L1 MB）、`STORAGE_CACHE_TTL`、`STORAGE_CACHE_MAX_OBJECT_SIZE`、`STORAGE_CACHE_L2_PATH` |
| **对现有系统的影响** | 低——只需要在 `main.go` 的 `buildStorageFrom` 路径中包一层包装器。`Storage` 接口不变。所有协议自动受益。 |

**架构设计：**

```
                ┌──────────────────────────────────────────────┐
                │                FileService                    │
                └──────────────────┬───────────────────────────┘
                                   │
                ┌──────────────────▼───────────────────────────┐
                │           CachingStorage (decorator)          │
                │                                               │
                │   Get(key):                                   │
                │     if SSE-C → passthrough                     │
                │     if key in L1 → return L1                   │
                │     if key in L2 → promote to L1, return       │
                │     backend.Get → store L1+L2 → return         │
                │                                               │
                │   Put(key): → backend.Put → invalidate L1+L2  │
                │   Delete(key): → backend.Delete → invalidate   │
                └──────────────────┬───────────────────────────┘
                                   │
                ┌──────────────────▼───────────────────────────┐
                │         Storage (local/s3/oss/cos)            │
                └──────────────────────────────────────────────┘
```

### 方向 5：跨协议操作一致性（文档中未覆盖的交叉点）

| 维度 | 内容 |
|------|------|
| **为什么需要** | 当前 REST ACL、S3 ACL、WebDAV 锁是三套独立的机制：(1) REST ACL 写入 `acl` 元数据字段；(2) S3 ACL 使用单独的 `acl` 子资源 XML 格式；(3) WebDAV 锁用 DAV:lockdiscovery XML。三者之间没有同步——通过 REST API 设置了 ACL，不会阻止 WebDAV 在未授权的情况下读取文件。对于多协议存取的场景，这是严重的安全不一致。 |
| **核心挑战** | (1) **语义映射**：S3 ACL（READ/ WRITE/ FULL_CONTROL） vs WebDAV 锁（exclusive/shared, depth） vs REST ACL（r/ w/ rw/ admin）没有直接的一对一映射；(2) **锁的时效性**：WebDAV 锁有时间限制，S3/WORM 锁是永久的（直到释放），两者不能混用；(3) **协议限定的语义**：WebDAV `LOCK` 是文件操作锁（排他写入），而 S3 对象锁是合规锁（WORM），用途不同。 |
| **预期的架构变更** | - 新增 `internal/lock` 包，提供统一的锁抽象：`Lock{Type, Tenant, Bucket, Key, Owner, Expiry, Depth}`<br>- 为 REST ACL 与 S3 ACL 建立等价映射表（`acl.go` 在两个协议间翻译）<br>- WebDAV `LOCK`/`UNLOCK` 走统一锁层，不再是 WebDAV handler 的本地状态<br>- 通过 EventBus 发布锁事件（`lock.acquired` / `lock.released` / `lock.conflict`） |
| **对现有系统的影响** | 中——需要新增包，修改 REST ACL handler、S3 ACL handler、WebDAV handler。但 `FileService` 的核心 CRUD 通路不变——锁检查只作用于 write/delete 路径的附加检查点。 |

---

## 3. 接口设计建议

### 3.1 关键原则

**P1. Storage 接口不应扩展**

当前 `Storage` 接口只有 ~13 个方法，小而精。不要因为「缓存」或「存储类转换」而往里面加 `CopyObject`、`RestoreObject` 等方法。相反，用装饰器或独立服务实现：

```
✅ CachingStorage: Storage { get/put/delete + cache logic }
✅ CopyService: func(ctx, srcKey, dstKey) error → 依赖 Storage 的 Get+Put
✅ RestoreService: func(ctx, key, days) → 触发后端 restore 并更新 metadata
```

**P2. Repository 接口应拆分（逐步）**

如前所述（L1），Repository 的 70 个方法是债务。**新增功能不应继续往 Repository 接口加方法**。相反，新功能应定义自己的子接口：

```go
// 错误做法
type Repository interface {
    // ... existing 70 methods ...
    ListObjectsWithFilter(ctx, filter) ([]Object, error) // 第71个
}

// 正确做法
type ObjectQueryStore interface {
    ListWithFilter(ctx, filter) ([]Object, error)
}
```

这样新的 `MetadataQueryEngine` 只依赖 `ObjectQueryStore`，不关心 API Key、Tenant、Job 等无关方法。

**P3. 缓存抽象不应污染 Storage 接口**

缓存是跨切面关注点，不应在 Storage 接口中暴露。使用 **Decorator 模式**：

```go
// 不修改 Storage 接口
type CachingStorage struct {
    inner Storage
    cache *ristretto.Cache
}

func (c *CachingStorage) Get(ctx, key) (io.ReadCloser, ObjectInfo, error) {
    // cache logic here, transparent to callers
}
```

### 3.2 是否需要新的抽象层

| 新抽象 | 需要性 | 理由 |
|--------|--------|------|
| `wal.WAL` | **需要** | WAL 是基础设施级抽象。EventBus、CDC、索引重建都依赖它 |
| `query.ObjectQueryStore` | **需要** | 隔离元数据查询逻辑，避免 Repository 继续膨胀 |
| `lock.LockManager` | **可选** | 仅在实现跨协议锁一致时需要。如果先不做这个方向，可以跳过 |
| `cache.CachingStorage` | **推荐暂不抽象** | 装饰器模式已经足够，不一定要独立包。放 storage 包内即可 |

### 3.3 向后兼容性

所有扩展必须遵守以下兼容性契约：

1. **Storage 接口不变** — 装饰器和新服务都基于现有接口
2. **Repository 接口不删不减现有方法** — 新增方法另起接口
3. **配置项 opt-in** — 新功能默认关闭（`ENABLE_LIFECYCLE_TRANSITIONS=false`）
4. **WAL 引入不破坏现有 EventBus consumer** — 两阶段迁移（见方向 3 实施路径）
5. **对象存储 key 格式不变** — `path.Join(tenant, bucket, key)` 是 I3 不变量

---

## 4. 技术选型

### 4.1 需要评估的新依赖

| 层次 | 候选 | 适用场景 | 评估标准 |
|------|------|---------|---------|
| **缓存** | `dgraph-io/ristretto` | L1 内存缓存（Sharded LRU + TTL） | 已用于 AI ResultCache，无额外引入成本 |
| **WAL** | `hybridgroup/go-wal` / 自建 | 文件级 WAL | 规模小（<1M events/天）可自建；规模大建议引入成熟库 |
| **查询** | `postgres` GIN index + JSONB | 元数据过滤 + 权限策略 | SQLite 受限，需在 sqlite 端做 client-side 过滤 |
| **锁** | `go.etcd.io/etcd/client/v3` | 分布式锁（取代现有 DB lease） | 当前 DB lease (`cluster.Singleton`) 已够用，不需要引入 etcd |

### 4.2 严格的自建原则

当前代码库遵循「stdlib 优先」原则（I6）。评估新依赖的标准：

```
【必须满足】每个新 go.mod 依赖必须论证：
  1. 为什么现有代码（或自建方案）无法满足
  2. 该依赖的许可证兼容性（Apache 2.0 / MIT / BSD）
  3. 该库在 Go 社区的成熟度（star > 1k, 最近更新 < 1年）
  4. 引入后对二进制体积的影响

【优先自建的场景】
  - 核心业务逻辑（如 WAL 在低吞吐时自建最可控）
  - 依赖涉及加密/密钥管理（如 KMS 集成）
  - 依赖的 API 风格与现有设计不一致（如引入 gRPC 只为了一个调用）
```

**具体建议：**

| 新功能 | 推荐方案 | 理由 |
|--------|---------|------|
| L1 缓存 | `dgraph-io/ristretto`（已用） | 无需重造轮子，且已在 AI `result_cache.go` 中验证 |
| L2 磁盘缓存 | 自建，基于 `os.File` + `internal/cache/diskcache.go` | 简单 key-value 磁盘缓存，不需要引入 bolt/bbolt |
| WAL | 低吞吐自建（文件 + protobuf 序列化） | 核心基础设施，依赖外部库风险高 |
| 元数据查询 | Postgres 原生索引 + SQLite client-side fallback | 不引入新依赖，充分利用各 DB 能力 |

### 4.3 自建 vs 集成决策矩阵

| 决策 | 选项 | 适用场景 |
|------|------|---------|
| 存储类 transition 定义 | 代码中硬编码 `STANDARD, STANDARD_IA, GLACIER, DEEP_ARCHIVE` | 简单、可预测，不需要动态加载 |
| 存储类 transition 规则引擎 | 配置驱动（`BUCKET_LIFECYCLE_JSON`） | 支持在 BucketConfig 中设置自定义 transition 规则 |
| 对象 restore | 服务层 + 临时元数据标记 | 不需要引入 cold storage 的 gateway service |
| URL 预签名 | 已有 HMAC 签名 `sign.go` | 已经够用，不需要 AWS SDK v4 替换 |

---

## 5. 实施路线图

### 优先级定义

```
P0: 阻塞性缺陷或安全漏洞
P1: 核心功能扩展，对业务价值有直接贡献
P2: 性能优化/架构整治，可延后但不做将累积债务
P3: 探索性/非关键功能，可搁置等待需求确认
```

### 阶段划分

```
            Q3 2026                      Q4 2026                       Q1 2027
┌─────────────────────┬──────────────────────────────┬──────────────────────────────┐
│  Phase 1 (6wks)     │  Phase 2 (8wks)              │  Phase 3 (8wks)              │
│                     │                              │                              │
│  P1: 存储类转换     │  P1: WAL (阶段1)              │  P1: WAL (阶段2)              │
│  P1: 元数据查询 MVP │  P2: 缓存层                     │  P1: 元数据查询增强            │
│  P2: 接口拆分起步    │  P1: 元数据索引引擎            │  P2: 跨协议锁一致性           │
│                     │                              │                              │
│  ┌──────────────┐   │  ┌──────────────┐            │  ┌──────────────┐            │
│  │  里程碑 M1   │   │  │  里程碑 M2   │            │  │  里程碑 M3   │            │
│  │  Storage     │   │  │  WAL 表引入  │            │  │  WAL consumer│            │
│  │  Class       │   │  │  Caching     │            │  │  迁移完成    │            │
│  │  Transition  │   │  │  decorator   │            │  │  跨协议锁    │            │
│  └──────────────┘   │  └──────────────┘            │  └──────────────┘            │
└─────────────────────┴──────────────────────────────┴──────────────────────────────┘
```

### Phase 1 详细任务

**任务 1.1 — 存储类转换（4 周）**

```
Week 1: BucketConfig schema 扩展
  - 迁移：objects 表 storage_class 字段已存在，无需改
  - 迁移：bucket_configs 表增加 transitions JSON 列
  - 更新 BucketConfig 结构体，添加 Transitions 字段
  - 更新 Get/SetBucketLifecycle 接口（生命周期可含 transitions + expire）

Week 2: LifecycleJob 扩展
  - LifecycleJob.sweep 增加 transition 检查分支
  - 对 GLACIER 目标：执行 metadata update + blob move（local 后端）
  - 对 STANDARD_IA 目标：仅改 metadata（无 blob move）
  - 测试：metadata-only transition + blob-move transition

Week 3: Restore 流程
  - Object 增加 RestoreStatus、RestoreExpiry 字段（或 metadata hack）
  - FileService 增加 RestoreObject 方法
  - local 后端 GLACIER 对象 Get 路径返回 ErrInvalidStorageClass
  - REST/S3 API 增加 POST /restore 端点

Week 4: 集成与边界
  - 测试覆盖：bucket transition → 等待 → 对象自动转换
  - 测试覆盖：GLACIER restore → 等待 expiry → 临时副本自动清理
  - 测试覆盖：transition 与 object lock 的交互（locked 对象不能被 transition/expire）
```

**任务 1.2 — 元数据查询 MVP（2 周，与 1.1 并行）**

```
Week 3: Repository 扩展
  - 定义 ObjectQueryStore 接口（独立于 Repository 主接口）
  - 在 sql_objects.go 实现 ListObjectsWithFilter
  - 支持过滤条件：storage_class, size range, tag key/value, created_at range
  - SQLite 实现 client-side 过滤（SQL 只做 prefix + type，go 层过滤）
  - Postgres 实现原生 SQL WHERE（利用 GIN index for metadata/tags）

Week 4: REST API + 缓存
  - 新增 GET /v1/objects?filter=... 路由
  - 查询结果缓存（与 ristretto resultCache 共享）
  - OpenAPI 文档更新
  - 测试覆盖：各过滤条件组合
```

**任务 1.3 — 接口拆分起步（可并行，不阻塞 1.1/1.2）**

```
  - 定义 ObjectStore、BucketStore、EventStore、ChunkStore 四个子接口
  - Repository 主接口保留，但内部实现委托给子接口
  - ObjectQueryStore（1.2 定义）作为第一个独立于 Repository 主接口的扩展
  - 目标是：新扩展不再往 Repository 加方法
```

### Phase 2 详细任务

**任务 2.1 — WAL 阶段 1（4 周）**

```
Week 1: WAL 表 + 数据模型
  - 迁移：wal_entries 表 (seq, tenant_id, event_type, event_payload, created_at)
  - WALEntry 结构体（protobuf 或 JSON 序列化）
  - WAL 写入接口 (Append) 在 events/bus.go 中作为 Publish 的前置步骤

Week 2: EventBus 适配
  - EventBus.Publish 先写 WAL，再写 events 表（两阶段提交）
  - 不阻塞现有 consumer（它们继续从 events 表读）
  - 添加 WAL 游标管理：每个 consumer 在 consumer_cursors 表中记录 last_seq

Week 3: Consumer 双路径支持（从 WAL 或 events 表消费）
  - indexer 可选从 WAL 消费（新增选项 AI_INDEXER_WAL_CURSOR）
  - 其他 consumer 保持 events 表消费

Week 4: 回放工具 + 监控
  - 新增 aero-vault cli wal replay --cursor=<seq> 工具
  - WAL 深度监控（wal_depth gauge metrics）
  - 测试覆盖：WAL 写入、回放、consumer 游标管理
```

**任务 2.2 — 缓存层（4 周，与 2.1 并行）**

```
Week 1: CachingStorage 装饰器
  - 实现 L1 缓存（ristretto）
  - 实现 SSE-C 降级检测（headers 检查）
  - 实现大对象透传（> CACHE_MAX_OBJECT_SIZE）

Week 2: L2 磁盘缓存
  - 本地磁盘缓存：缓存 key 的 hash 作为文件名
  - 独立的 cache_dir（STORAGE_CACHE_L2_PATH）
  - L2 填充策略：Get 后端读取后异步写入 L2
  - L2 读取后 promoted 到 L1

Week 3: 失效策略
  - Put/Delete 时 invalidate L1 + 删除 L2 文件
  - TTL 过期驱逐（L1 + L2）
  - L2 磁盘空间使用量控制（LRU 淘汰 + max_size 限制）

Week 4: 集成测试
  - 验证缓存在 Get/GetObject 路径的命中率
  - 验证 SSE-C 对象绕过缓存的正确性
  - 验证 Put/Delete 后缓存正确失效
```

### 风险点与缓解策略

| 编号 | 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|------|---------|
| R1 | WAL 写入成为新瓶颈（双写 WAL + events 表） | 中 | 高 | Phase 1 先用两阶段，但 WAL 写是本地 append（接近零成本）；events 表写保持现样；Phase 2 再优化 |
| R2 | GLACIER 恢复过多导致磁盘膨胀 | 低 | 中 | restore 副本设 TTL（默认 1 天），并在 reconcile 扫描中清理过期副本 |
| R3 | 缓存失效延迟导致用户读到旧数据 | 中 | 中 | 写入路径同步 invalidate（Put/Delete 完成时立即清缓存）；以短 TTL（~30s）兜底 |
| R4 | 元数据查询在 SQLite 上性能差 | 高 | 中 | 文档明确声明：SQLite 用于开发和单机部署；生产使用 Postgres。SQLite 的 filter 测试标记 `//go:build !sqlite` |
| R5 | 跨协议锁实现复杂度超预算 | 中 | 高 | 决策：Phase 3 才启动跨协议锁，先做可行性评估。如果复杂度超预期，降级为「只统一 REST ACL + S3 ACL，WebDAV 锁暂不同步」 |
| R6 | Storage class transition 与 dedup 块级引用冲突 | 中 | 高 | 文档正确指出：块级 dedup 的对象不能独立降冷。如果 dedup 在路线图中，transition 规则需要检查引用计数——在 transition 前做 `StorageKeyReferenced` 查询 + 检查是否被不同 storage class 的对象引用 |

### 检查清单（每个任务提交前）

依据 `AGENTS.md` 的 CI Gate，每个提交前必须通过：

```bash
make check   # gofmt + go build + go vet + go test
```

以及额外新增的专门检查：

| 新规则 | 检查方式 |
|--------|---------|
| 迁移双文件 | `git diff --name-only -- 'internal/repository/migrations/*'` 必须同时有 `.up.sql` + `.down.sql` |
| 存储类 transition 可逆 | 所有下行迁移有对应上行（`down.sql` 恢复原始 schema） |
| SSE-C 缓存穿透验证 | `caching_storage_test.go` 中的 `TestCachingStoragePassesThroughSSEC` |
| WAL consumer 游标正确性 | `bus_test.go` 中的 `TestWALConsumerCursor` |

---

## 总结

| 维度 | 评估 |
|------|------|
| 当前架构质量 | **高**：分层清晰，抽象正交，解耦程度在同类系统中处于第一梯队 |
| 最大架构债务 | Repository 70 方法 God Interface；AI 索引与对象元数据共库；local 后端存储类无物理语义 |
| 最高价值扩展（文档覆盖） | **存储类生命周期转换**（代码已半就绪，只差 LifecycleJob 扩展）+ **元数据查询引擎**（用户刚需） |
| 最高价值扩展（文档未覆盖） | **跨协议一致性**（REST ACL / S3 ACL / WebDAV lock 三套独立机制的安全隐患） |
| 最需谨慎的交叉点 | SSE-C 缓存穿透（已正确识别）+ 块级 dedup × storage class transition（新识别） |
| 优先级建议 | P1：存储类转换 + 元数据查询 MVP；P2：缓存 + WAL 阶段 1；P3：跨协议锁 + WAL 阶段 2 |

**最终建议：** 立即从 Phase 1 的存储类转换开始——这是工程价值最高、代码就绪度最高的扩展。`internal/reconcile/lifecycle.go` 已是现成的切入点，只需扩展其 sweep 逻辑以支持 transition 而非仅 expire，并添加 Restore 路径。
