---

# 1. 架构评估

## 1.1 当前架构的优势

**分层隔离做得非常好。** 从 `s3compat/handler.go` → `service/FileService` → `storage.Storage` + `repository.Repository` 的三层分割清晰且严格。每个协议适配器（S3、REST、WebDAV、MCP）都只做协议翻译，不涉及业务逻辑。这个设计被文档中 5 个方向的代码锚点一致验证。

**事件驱动的工作流核心设计正确。** 通过 `EventBus` 解耦 AV 扫描、Webhook、复制等横向关注点。`LifecycleJob` 以 worker 模式运行并配合集群单例锁，这是成熟的选择。

**配置驱动的 opt-in 安全默认。** AI、pgvector、Qdrant、事件、WebDAV 等全部默认关闭。这种设计让基线 CI 路径（SQLite + local FS + 零鉴权）保持极轻量和可测试。

**代码锚点验证结论：** 文档中全部代码锚点通过了当前 `git HEAD`（`90add1a`）的实体验证，无一偏差。文档自述的"一处细微偏差"（`CommonPrefixes` 未定义而非未填充）也已确认属实。

## 1.2 关键架构局限性

### 协议语义断层（最严重的架构债务）

当前 S3 兼容层尚处在"功能骨架"阶段。5 个方向中有 3 个属于协议层面的语义断层：

```
ListObjectsV2 读取了 prefix / continuation-token / max-keys / tag-key / tag-value
              → 但忽略了 delimiter（S3 文件夹浏览的核心参数）
              
PutObject 读取了 Content-Type / Metadata / Content-MD5 / StorageClass / x-amz-acl
          → 但忽略了 x-amz-server-side-encryption / x-amz-server-side-encryption-aws-kms-key-id
          
CopyObject 读取了 x-amz-copy-source / x-amz-metadata-directive
           → 但忽略了 x-amz-copy-source-range
           → 且没有 UploadPartCopy 路由
```

**这不是功能缺失，而是契约断裂。** AWS SDK 和 S3 兼容客户端默认发送这些参数，系统静默忽略它们会产生安全幻觉（SSE）和无法解释的失败（大对象复制）。

### 状态机不完整

`storage_class` 字段在对象创建时正确写入，但缺少状态转换的执行器。`LifecycleJob` 只处理过期删除，没有分层转换。这意味着——用 S3 术语说——系统支持 `STANDARD`、`STANDARD_IA`、`GLACIER` 的**声明**但不支持**转换**。对象的 `storage_class` 一旦写入，终身不变。

```
objects.storage_class → 写入即冻结，永不变化
bucket_config → 无 Transition 数组，只有标量的 ExpireAfterDays
lifecycle.go → sweepExpired() 只扫过期删除
```

### 安全模型断裂

对象锁的实现路径中，`locked_until` 阻止了任何模式下的删除，但不区分 GOVERNANCE（可绕过）和 COMPLIANCE（绝对不可绕过）。这导致：

- 管理员无法在紧急情况下通过 `BypassGovernanceRetention` 解锁 GOVERNANCE 对象——这是合规流程中断
- COMPLIANCE 对象的安全强度被降级到 GOVERNANCE 水平——这是合规幻觉

## 1.3 关键设计决策回顾

| 决策 | 正确性 | 注释 |
|------|--------|------|
| `storage.key` 为 opaque 字符串，不做反解析（I3） | ✅ | 避免了 prefix/delimiter 对存储层的侵入 |
| BucketConfig 存储为单独的表而非 JSON blob | ✅ | 支持 SQL 查询和迁移 |
| Middleware 链固定顺序，handler 不自挂链（I4） | ✅ | 隔离测试可行，关注点分离 |
| `PutOptions` 为扁平结构而非 builder 模式 | ⚠️ | 当前字段少时可接受，但 SSE/range/copy-source 加入后会膨胀 |
| Lifecycle 规则存储为标量字段而非数组 | ❌ | 无法扩展 Transition，需要 schema 变更 |
| Legal Hold 存为 metadata（`_aero_legal_hold`）而非结构化字段 | ❌ | 不可查询，不可审计，S3 协议不兼容 |

## 1.4 架构债务汇总

| 债务类型 | 位置 | 严重度 | 修复代价 |
|---------|------|--------|---------|
| `ListPage` 无 `CommonPrefixes` 字段 | repository + s3compat | 中 | 低（~150 行） |
| `PutOptions` 无 SSE 字段 | service | 中 | 低（~100 行） |
| `objects` 表无 `sse_algorithm`/`retention_mode`/`legal_hold` 列 | repository (schema) | 高 | 中（3 个 migration） |
| `BucketConfig` 为标量而非结构化的 lifecycle 数组 | repository | 高 | 中（schema + 接口变更） |
| `copyObject` 全量流式复制无 range 支持 | s3compat | 高 | 中（~200 行） |

---

# 2. 扩展方向

## 2.1 高价值方向一：S3 协议语义完整性层（P0）

**为什么需要：** 所有 S3 客户端（CLI、SDK、工具）都依赖 `delimiter`、`UploadPartCopy`、`SSE` 请求头。没有它们，AeroVault 不能算"兼容 S3"，而只是一个"长得很像 S3 的存储 API"。对于任何考虑迁移的 AWS 用户，这 3 个缺口是阻挡采用的硬性门槛。

**核心挑战：**

```
delimiter 分页:  应用层分组 + marker 分页的交叉
                 跨页边界: 一个 CommonPrefix 下有超过 maxKeys 个对象时
                 需在第一页就返回该 CommonPrefix + 从中断处继续

UploadPartCopy:  x-amz-copy-source-range 的字节范围解析
                 与版本化源对象的组合 (?versionId)
                 加密源对象的解密 → 重新加密路径

SSE 头传递:      声明式(echo) vs 实际加密
                 SSE-KMS 不支持时返回 400 vs 静默降级
```

**预期架构变更：**

```
Repository 层:
  ListObjects(ctx, tenant, bucket, prefix, marker, limit) → 新增 delimiter 参数
  ListPage 增加 CommonPrefixes []string 字段
  SQL 不变（应用层分组），但 pagination 需要调整

Service 层:
  PutOptions 增加: SSEAlgorithm / SSEKMSKeyID / SSECustomerKey / SSECustomerKeyMD5
  CopyObject 增加: SourceRange / SourceVersionID 参数

S3 Compat 层:
  handler.go → listObjectsV2 读取 delimiter→传递→填充 CommonPrefixes
  handler.go → PutObject 读取 SSE 头→传递→响应回显
  handler.go → UploadPartCopy 路由 (partNumber+uploadId+x-amz-copy-source)
```

**对现有系统的影响：** 低。Service 接口是增量的（新字段），Repository 接口是兼容的（参数有默认值）。Handler 层的变更不涉及中间件链。

## 2.2 高价值方向二：存储分层状态机（P1）

**为什么需要：** 这是 S3 最核心的成本优化功能。没有自动分层转换，所有对象永久占据热存储，运营成本（OPEX）逐年线性增长。对于备份、日志、归档类租户，自动降级到 GLACIER 可节省 70-90% 的存储成本。

**核心挑战：**

```
状态机设计:
  STANDARD ──[30天后]──→ STANDARD_IA ──[90天后]──→ GLACIER
                                       ──[365天后]──→ Expired (删除)
  
  每个规则独立运行，对象可能同时满足多个 Transition
  按 Days 排序，每次只执行最近的一个

跨后端迁移:   local → S3 → S3 IA → S3 Glacier
              每个迁移需要: 读取 → 写入新后端 → 更新 storage_key → 延迟删除旧 blob

幂等性:       转换作业中途崩溃后重启，不应重复迁移
              需要记录转换进度 (conversion_id + 状态)
```

**预期架构变更：**

```
Repository:
  BucketConfig 增加 LifecycleRules JSON 字段（或独立表）
    []LifecycleRule{
      {ID, Status, Transitions: []Transition{Days, StorageClass}, Expiration}
    }
  ListEligibleForTransition(ctx, tenant, bucket, rule) 方法
  UpdateObjectStorageClass(ctx, id, newClass, newStorageKey) 方法

Reconcile:
  TransitionJob 新增（与 LifecycleJob 并列运行或合并）
  单例锁、慢启动、分批（每轮 200 个对象）

Storage:
  可能需要 Storage.Copy(ctx, srcKey, dstKey) 用于同后端快速转换
  跨后端则复用现有 Get + Put 管线
```

**对现有系统的影响：** 中。`BucketConfig` 的 schema 变更需要 migration + 向后兼容。TransitionJob 需要单体或集群锁。要求 `LifecycleRule` 的实现处理与方向五（对象锁）的交互——锁定的对象不能转换。

## 2.3 高价值方向三：异步操作框架统一化（P1→P0）

**为什么需要：** 当前复制、生命周期、Webhook、AV 扫描各走不同的调度机制（EventBus、JobPool、Ticker）。这种异构的异步框架增加了运营复杂度，并且在跨 `EventBus` 和 `JobPool` 两个系统之间缺乏统一的可见性。

**核心挑战：**

```
现状碎片:
  Replication  → 通过 EventBus 触发，阻塞式
  Lifecycle    → Ticker + 单例锁
  Antivirus    → EventBus 触发
  Webhook      → EventBus 触发 + webhook_failures 表重试
  Reconcile    → Ticker + 单例锁

需要统一的:
  作业定义、调度策略（立即/延迟/周期）、重试策略、死信队列、
  进度跟踪、操作取消、可观测性指标
```

**预期架构变更：**

```
统一 Scheduler:
  Job {
    ID, Type, Payload, State (pending/running/completed/failed),
    Schedule (immediate/cron/delay), MaxRetries, BackoffStrategy
  }
  
  统一重试: 所有 EventBus 触发的工作走 JobPool
  统一可观测: 所有 worker 共享 metrics (job_queue_depth, job_duration, job_retries)
```

**对现有系统的影响：** 高。这涉及重构已经运行良好的 EventBus 模式。建议作为 P1/P2 迭代优化而非重写。可以先给 JobPool 增加 cron 调度能力，逐步将 Ticker 式 worker 迁移过来。

## 2.4 高价值方向四：存储后端路由器（P2）

**为什么需要：** 当前 `storage.Storage` 是一个抽象层，每个后端单独实现（local / s3 / oss / cos）。但缺少一个**路由层**，无法基于规则（storage class、tenant、bucket、对象大小）自动选择后端。存储分层（方向四）要求对象能在后端之间迁移，路由层是前置条件。

**核心挑战：**

```
路由规则:
  storage_class = STANDARD     → backend = "local-nvme"
  storage_class = STANDARD_IA  → backend = "local-hdd"  
  storage_class = GLACIER      → backend = "s3:glacier-bucket"
  tenant = "backup"            → backend = "s3:backup-bucket"
  
迁移管线:  路由层应提供 CopyObject(fromKey, toBackend) 的跨后端操作
```

**预期架构变更：**

```
StorageRouter:
  Put(ctx, key, r, size, opts) → 根据 opts.StorageClass 选择后端
  Get(ctx, key) → 根据 key 查找对象所在后端（需查 metadata）
  Copy(ctx, srcKey, dstBackend) → 跨后端复制
  Relocate(ctx, key, targetClass) → 触发异步迁移

BackendRegistry:
  名称 → BackendKind + Config 的映射
  启动时初始化，运行时不可变
```

**对现有系统的影响：** 中到高。当前 `FileService` 持有单个 `storage.Storage`；路由层会增加一个间接层。如果迁移是异步的，还需要状态跟踪（迁移中、已迁移）。

## 2.5 高价值方向五：事件驱动架构增强（P2）

**为什么需要：** 当前事件系统仅支持 `object.created` / `object.deleted` / `object.accessed` 三个事件类型和 Webhook 一个消费者。缺少：事件过滤（按 bucket/prefix/type）、事件重放、事件持久化窗口、S3 Event Notifications 兼容格式（`Records[]` JSON）。

**核心挑战：**

```
S3 Event 格式兼容:
  {Records: [{eventName, eventTime, s3: {bucket, object, size, etag}}]}
  
事件持久化:  当前只有 webhook_failures 持久化失败事件
             没有通用的事件日志用于审计和重放

事件过滤:    S3 支持按 prefix/suffix 过滤事件通知
             当前 EventBus 无过滤机制
```

**预期架构变更：**

```
EventBus 增强:
  Subscribe(filter EventFilter, handler EventHandler)
  Publish(event Event) → 匹配过滤器 → 持久化 → 扇出

EventStore:
  持久化事件到 events 表（TTL 可配置）
  支持重放（ReplayEvents(from, to, filter)）

Notification:
  兼容 S3 NotificationConfiguration
  BucketConfig 增加 NotificationRules 数组
```

**对现有系统的影响：** 中。EventBus 接口变更会影响现有消费者（Replication、Antivirus、Webhook）。建议保持现有 `EventBus` 接口不变，增加一个 `FilteredEventBus` 装饰器。

---

# 3. 接口设计建议

## 3.1 关键模块接口设计原则

### 原则一：对 Repository 层只做增量加法，不做破坏性变更

当前 `Repository` 接口的方法签名（如 `ListObjects(ctx, tenant, bucket, prefix, marker, limit)`）被多方调用（s3compat handler、REST handler、CLI、MCP 工具）。任何签名变更都会产生连锁编译错误。

**建议**：对新参数使用选项结构体模式：

```go
// 不变（保持向后兼容）
ListObjects(ctx, tenant, bucket, prefix, marker, limit) (ListPage, error)

// 新增带 delimiter 的变体（或使用可选的 ListObjectsOpts）
type ListObjectsOpts struct {
    Prefix    string
    Marker    string
    Limit     int
    Delimiter string
}
ListObjectsWithOpts(ctx, tenant, bucket, opts) (ListPage, error)
```

这样可以不破坏现有调用者，同时新增功能。

### 原则二：PutOptions 应逐步转移到 Builder 模式

当前 `PutOptions` 是扁平结构体，传入方式为值类型：

```go
service.PutOptions{
    ContentType: "...",
    Metadata:    meta,
    StorageClass: "...",
    // 每个新字段都在这里添加
}
```

随着 SSE、Range、CopySource、ContentRange 等字段加入，建议演进为：

```go
// 阶段一：增量添加字段（当前）
// 阶段二：增加 functional options（拆分为 SetSSE、SetSource 等）
// 阶段三（远期/可选）：Builder 模式
```

不要在阶段一就引入 Builder——对 Go 来说过度设计，且破坏与 `PutOptions` 现有调用者的兼容性。

### 原则三：BucketConfig 的存储模型需要重构

当前存储设计是**扁平的标量结构**：

```
bucket_config (
  object_lock_seconds INT,
  expire_after_days   INT,
  expire_action       TEXT
)
```

目标设计应该是**嵌套的结构化存储**：

```
bucket_config (
  lifecycle_rules   JSON,  -- [{transition: [{days, class}], expiration: {days}}]
  object_lock       JSON,  -- {mode: "GOVERNANCE"|"COMPLIANCE", days: int, enabled: bool}
  notifications     JSON,  -- [{id, topic, events[], filter: {prefix, suffix}}]
  cors_rules        JSON,  -- existing
  logging           JSON,  -- existing
  policy            TEXT   -- existing
)
```

**迁移策略：** 不对现有列做原地修改。新增一个 `bucket_config_ext` 表（或 `config_json TEXT` 列），用 JSON 存储结构化配置。现有代码继续读写标量字段，新代码读写 JSON 字段。通过 `COALESCE(ext.config_json, '{}')` 向下兼容。

```
阶段一: 增加 config_json TEXT NOT NULL DEFAULT '{}' 列
        后台迁移现有配置到 JSON
阶段二: 新功能（Transition、Notification）只读写 JSON
        逐步废弃标量字段
阶段三: 完全切换到 JSON（远期 schema 优化）
```

## 3.2 新抽象层评估

### 需要新增：`TransitionEngine`

存储分层转换引入了一个新的领域概念，不应直接放在 `LifecycleJob` 中。应为它新建一个接口：

```
TransitionEngine:
  Evaluate(ctx, tenant, bucket, rule) → []EligibleObject  -- 找出到期对象
  Apply(ctx, obj, targetClass) → error                     -- 转换单个对象
  Rollback(ctx, obj, originalClass) → error                -- 回滚（部分故障时）
```

`LifecycleJob` 则拆分为两个 worker：`ExpirationSweeper`（保留现有职责）+ `TransitionApplier`（新增），在 `reconcile` 包中并列运行。

### 不需要新增：`StorageRouter`

当前 `FileService` 直接持有 `storage.Storage`，这个模式运行良好。存储分层对后端的切换不应通过"路由层"来实现，而应通过 `FileService` 的现有 `Get`/`Put` 管线加上 `Object.StorageKey` 更新。

```
FileService.TransitionObject(ctx, obj, targetClass):
  1. 从旧存储 key 读取对象 (store.Get)
  2. 选择目标后端 (根据 targetClass 查配置)
  3. 写入目标后端 (targetStore.Put)
  4. 更新 repo 中的 storage_class + storage_key
  5. 异步删除旧存储 blob
```

这样避免了引入新的架构抽象层，保持 `storage.Storage` 接口不变。

## 3.3 向后兼容性策略

| 变更类型 | 策略 | 示例 |
|---------|------|------|
| Repository 接口新增方法 | 接口不变，通过 Opts 结构体扩展 | `ListObjectsWithOpts` |
| Schema 变更 | ALTER TABLE ADD COLUMN，NOT NULL 提供默认值 | `sse_algorithm TEXT DEFAULT ''` |
| XML 响应格式 | 新增字段默认 omitempty，不影响现有解析 | `CommonPrefixes` 为空时不输出 |
| 请求头解析 | 新请求头静默不存在时不改变行为 | `x-amz-server-side-encryption` 缺失时与当前一致 |
| PutOptions 字段新增 | 零值 = 当前行为 | `SSEAlgorithm = ""` 时不加密 |
| BucketConfig 重构 | JSON 列 + 标量列双写 → 逐步迁移 | 读时 JSON 优先，回退到标量 |

---

# 4. 技术选型

## 4.1 是否引入新技术栈

**结论：不建议引入新的依赖。** 以上 5 个方向全部可用当前技术栈（Go 标准库 + SQLite/Postgres + 现有 storage 后端）实现，理由如下：

### 不需要引入的技术

| 技术 | 被考虑用于 | 为什么不引入 |
|------|----------|------------|
| 消息队列（NATS/RabbitMQ） | 异步操作框架统一 | 当前 EventBus + JobPool 组合已经足够。引入 MQ 会增加运维复杂度和部署依赖 |
| 分布式调度器（Quartz/Cadence） | 生命周期转换调度 | JobPool 已提供 `jobs` 表轮询，只需增加 cron 触发即可 |
| 对象存储 gateway（MinIO Gateway） | 解决协议兼容 | 这是对 AeroVault 的替代，不是扩展 |
| Protocol Buffers / gRPC | 内部接口 | 当前纯 Go 接口调用无需序列化切换。可能在后端之间的跨网络复制时有用，但不是当前瓶颈 |

### 可在未来评估的技术

| 技术 | 评估时机 | 解决什么问题 |
|------|---------|------------|
| 分布式文件锁（etcd / ZooKeeper） | Postgres 变为默认且集群规模 > 5 节点 | 替代 Postgres 的 `GET_LOCK()` 用于集群单例 |
| Apache Parquet + Columnar 存储 | 审计日志 / 事件持久化超过 10TB | 分析型查询的性能优化 |
| WAL-based CDC（Debezium / wal2json） | 事件驱动需要精准一次语义 | 当前 EventBus 可能丢事件 |

## 4.2 第三方依赖评估标准

如果未来确实需要新依赖，应当用以下标准评估：

| 维度 | 要求 | 否决条件 |
|------|------|---------|
| License | Apache 2.0 / MIT / BSD | AGPL / SSPL / 自定义 license |
| 纯 Go | 无 CGo / C 依赖（SQLite 除外） | 需要 C 编译器或系统库 |
| 依赖规模 | 引入后 go.mod 新增 ≤5 个 transitive 依赖 | 超过 20 个新增依赖 |
| 维护状态 | GitHub stars > 1000, 最近提交 < 6 个月 | 已归档或无活动 1 年以上 |
| API 稳定性 | Go 1.x compatibility promise | 主要版本 < 1.0 或 breaking changes 频繁 |
| 与现有架构拟合 | 可作为现有接口的一个实现而非改变整体模式 | 需要引入 goroutine 池管理、生命周期钩子等侵入式设计 |

## 4.3 自建 vs 集成的决策

| 功能 | 建议 | 理由 |
|------|------|------|
| SSE 加密（方向三） | **自建**（已有 `encrypt.go`） | 已有的 AES-256-GCM 信封加密实现成熟。仅为加密集成就引入 Hashicorp Vault 或 AWS KMS SDK 是大炮打蚊子。KMS 集成可作为 P3 可选 |
| 存储分层转换（方向四） | **自建** | 这是 AeroVault 的核心差异化功能。S3 Lifecycle 是参考实现而非黑盒——写一个 TransitionJob 比适配第三方调度器更可控 |
| 对象锁合规（方向五） | **自建** | 纯架构变更（权限评估 + schema 字段），无外部依赖 |
| 异步操作框架统一（方向三扩展） | **自建** | 在 JobPool 基础上增加 cron 调度，不需要引入 Quartz / cron 库。Go 的 `time.Ticker` + `jobs` 表就够用 |

**唯一值得集成的外部技术：** 如果方向四的跨后端迁移需要异步通知（如 S3 到 Glacier 的 restore 可能需数小时），可考虑与 AWS S3 Batch Operations 集成，但那是 P3 远期考虑。

---

# 5. 实施路线图

## 5.1 优先级排序

```
  P0 ────────────────────────────────────────────────────── P2
  (用户可见的协议断裂)             (架构优化)             (安全合规)
  
  ┌─────────────────────────────────────────────────────────┐
  │  P0: ListObjects Delimiter    │                         │
  │  P0: UploadPartCopy           │                         │
  │  P1: SSE 请求头 (方案A+B)     │                         │
  │       ├─ 方向三依赖于方向二    │                         │
  │       └─ 方向二加密对象copy    │                         │
  │                               │                         │
  │             P1: 存储分层转换   │  P2: 对象锁合规模式    │
  │                  (P1成本优化)  │      (P2合规流程)      │
  └─────────────────────────────────────────────────────────┘
```

**依赖链：**
- 方向二（UploadPartCopy）不阻塞方向三（SSE），但**加密对象的 part copy** 依赖 SSE 头解析 → 建议方向二的实现兼容"SSE 头不存在时按明文处理"（当前行为不变）
- 方向四（存储分层）的转换引擎应**复用**方向二的跨后端复制能力
- 方向五（对象锁）应先于方向四实现，因为合规对象的转换有约束

## 5.2 阶段划分与里程碑

### 阶段一：S3 协议修复（2-3 周）

| 工作项 | 工期 | 产出 | 验证方式 |
|--------|------|------|---------|
| ListObjects Delimiter | 5 天 | 应用层分组 + CommonPrefixes XML | `aws s3 ls s3://bucket/prefix/` 返回目录结构 |
| UploadPartCopy | 5 天 | 流式 range copy + multipart 组合 | >5GB 对象的跨键复制成功 |
| SSE 请求头（方案 A：声明兼容） | 3 天 | 读取+回显+忽略 | boto3 put_object 响应包含 x-amz-server-side-encryption |
| 阶段性集成测试 | 2 天 | aws-cli / boto3 / aws-sdk-go 三轮测试 | 3 方向全部通过 |

**里程碑 M1：** `hack/s3-compat-test.sh` 通过（含 delimiter + uploadPartCopy + SSE echo）

### 阶段二：SSE 透明加密桥接 + 对象锁合规（2-3 周）

| 工作项 | 工期 | 产出 | 验证方式 |
|--------|------|------|---------|
| SSE 方案 B（透明桥接本地加密） | 5 天 | SSE-S3 触发本地 AES-256-GCM；响应回显 `AES256` | 设置 `STORAGE_SSE_KEY` 后 SSE 对象实际加密存储 |
| `objects` 表增加 SSE 列（migration） | 1 天 | `sse_algorithm TEXT` 列 | 历史对象迁移正确 |
| `retention_mode` + `legal_hold` 结构化 | 3 天 | `objects` 表加 2 列；`_aero_legal_hold` 迁移 | 查询 `legal_hold=true` 的对象 |
| GOVERNANCE/COMPLIANCE 区分 | 3 天 | `hardDeleteObject` 分支逻辑 + BypassGovernance 权限评估 | 测试 GOVERNANCE 可绕过、COMPLIANCE 不可绕过 |
| Legal Hold 独立端点 | 2 天 | `GET/PUT /{key}?legal-hold` | S3 SDK legal_hold 方法通过 |

**里程碑 M2：** AWS S3 合规性测试套件通过 Object Lock 场景

### 阶段三：存储分层转换（3-4 周）

| 工作项 | 工期 | 产出 | 验证方式 |
|--------|------|------|---------|
| BucketConfig 增加 lifecycle_rules JSON 列 | 2 天 | 迁移文件 + Repository 读写方法 | PUT `?lifecycle` 接受 Transition 规则 |
| TransitionEngine 实现 | 10 天 | Evaluator + Applier + 幂等性 + 事务 | 30 天后 STANDARD→STANDARD_IA 转换 |
| TransitionJob worker | 3 天 | 集群单例 + 分批 + OTel 指标 | 遥测显示 `transition_total{from,to}` |
| 跨后端迁移集成（复用方向二能力） | 5 天 | local→S3 的分层转换 | 对象物理移动到目标后端 |

**里程碑 M3：** 生命周期规则支持完整的 Transition + Expiration，运行数据在 Grafana 仪表盘可见

### 阶段四（远期 P2）：架构统一与优化

| 工作项 | 优先级 | 依赖 |
|--------|--------|------|
| 异步操作框架统一（JobPool + cron） | P2 | 阶段一/二/三全部完成 |
| 事件驱动架构增强（S3 compatible events） | P2 | 异步框架统一 |
| 存储后端路由器 | P2 | 分层转换稳定运行后 |
| SSE-KMS 集成 | P3 | SSE 方案 B 运行后评估需求 |

## 5.3 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| **方向一跨页边界错误** — CommonPrefix 在分页边界被截断，用户看到不完整目录 | 中 | 高（数据不一致） | 应用层分组时使用 `LIMIT+N` 缓冲区（N=1 倍 maxKeys）；增量第一轮就增加 `CommonPrefixes` 的单元测试覆盖所有分页边界场景 |
| **UploadPartCopy 并发写入冲突** — 多个 part 同时写同一个 uploadId 被不同的后端节点处理 | 低 | 中 | Multipart upload 在 Service 层是同步的（`svc.Put` 是阻塞方法），不会出现分布式冲突 |
| **存储分层转换中 Crash** — TransitionJob 在"已读旧 blob 但未写入新 blob"之间崩溃导致数据丢失 | 中 | 高 | 转换使用**三阶段事务**：(1) 读取旧 blob → (2) 写入新 blob（带 temp key）→ (3) 原子更新 `storage_key` + `storage_class` + 删除旧 blob。崩溃时 temp key 可被 GC 清理 |
| **迁移文件冲突** — 方向三/四/五各自引入 migration，顺序变更导致冲突 | 中 | 中 | 统一在 `migrations/` 下分配编号区间（SOAK test 环境预演合并）；避免在多个方向中修改同一张表的同一列 |
| **SSE 方案 B 对已有对象的影响** — 已有对象未加密，PUT 更新时自动加密，协议层不感知 | 低 | 中 | 这是设计行为——S3 的 SSE 是"写在对象上的"，每次 PUT 独立生效。无需迁移已有对象 |
| **BucketConfig JSON 与标量字段的同步不一致** — 双写时一处更新另一处遗漏 | 中 | 中 | 阶段性过渡中使用应用层同步：`SetBucketConfig` 同时更新 JSON 和标量。验证脚本定期扫描不一致性 |

## 5.4 测试策略

| 测试类型 | 覆盖方向 | 工具/方法 |
|---------|---------|----------|
| 协议兼容性测试 | 1, 2, 3 | `hack/s3-compat-test.sh`（使用 aws-cli + boto3）；`s3tests` 社区套件 |
| 单元测试（~85% 覆盖率目标） | 全部 | Go `testing` + `httptest`；AI mock 零网络 |
| 分页跨边界测试 | 1 | 10000+ 对象的 prefix，每页 1000，验证每页 CommonPrefixes 完整 |
| 大对象复制测试 | 2 | 5GB+ 对象的 multipart copy（可用 `/dev/zero` 生成） |
| 加密审计测试 | 3 | `STORAGE_SSE_KEY` 启用后，验证存储 blob 为密文 |
| 分层转换集成测试 | 4 | SQLite + local FS，规则 30 天 → STANDARD_IA，手动设置 `created_at` 并触发 `sweep` |
| 合规锁定测试 | 5 | GOVERNANCE 绕过成功 / COMPLIANCE 绕过失败；Legal Hold 设置/清除 |
| 迁移测试 | 3, 4, 5 | `migrations/testdata/` 中放置已知状态 db，验证升降级脚本正确 |

---

# 总结

AeroVault 的当前架构基础非常扎实——三层隔离、事件驱动、opt-in 安全默认——这使得 5 个扩展方向都可以用**增量变更**实现，不需要架构重写。

最紧急的架构债务是 S3 协议语义断层（P0 的两个方向 + P1 的一个方向），它们触及了协议兼容性的核心契约。**建议优先方向一和方向二并行开发**，方向三紧随其后（因为加密对象的 part copy 需要 SSE 头支持）。

存储分层（P1）和对象锁合规（P2）是成本优化和合规能力的关键区分项，但它们的架构变更集中在 `BucketConfig` 和 worker 层的扩展，与协议修复的代码路径**几乎无交叉**，可以并行推进。

**架构层面最大的单一改进**是将 `BucketConfig` 从扁平标量迁移到 JSON 结构化存储。这会对方向四和方向五同时受益，应优先完成这个 schema 变更（作为阶段二的奠基工作）。
