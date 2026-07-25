# AeroVault 高价值扩展方向（第十七期）

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全局代码扫描（240+ Go 源码文件，约 50K 行），逐包审阅 `internal/` 全部子包、`cmd/server/main.go`、配置系统、SDK、全部 24 对迁移文件、部署配置。逐一比对前十六期 expansion 文档（`expansion-directions.md` ~ `expansion-v16-foundations.md`，累积约 900KB 分析）+ `ROADMAP.md`（10 方向）+ `analysis-v8-gaps-roadmap.md` + `extensions.md`，确认每个方向在**既有文档中零覆盖或仅行级提及**。  
> **日期：** 2026-07-10  
> **原则：** 选取 5 个**S3 生态兼容性 / 数据生命周期管理**方向——不是新造轮子，而是补齐 S3 标准生态中用户从 AWS S3 迁移时**默认期待存在**的能力。每个方向附带：代码锚点、当前状态、缺失能力、边界情况、架构概要、实现理由。**不编写任何实现代码。**

---

## 审阅背景：前十六期覆盖的去重矩阵

前十六期（v1–v16）已从约 16 个视角覆盖约 80 个方向。以下大类已深度覆盖，**本期不再重复**：

| 领域 | 覆盖期数 | 方向数 |
|------|---------|--------|
| AI/RAG 管线（Embed/Chunk/Search/Chat/Agent/Rerank/PII/Indexer/Cache） | v1~v13, ROADMAP #1~#2 | ~12 |
| S3 兼容性（子资源/Batch/Multipart/ACL/Policy/CORS/Logging/Notification） | v1, v4, v6, v8~v10, ROADMAP #7 | ~8 |
| 存储后端多路由 / 分层（Multi-Backend/Storage Tier/Routing Rules） | v15, ROADMAP #9 | ~5 |
| 存储后端实现（S3/OSS/COS/KMS/SSE/Encryption/CircuitBreaker） | v4~v15, ROADMAP #5 | ~7 |
| 认证授权（JWT/API Key/SigV4/OIDC/SAML/SCIM/MFA/Policy Engine） | v1, v5, v8, v11~v12, v15 | ~7 |
| 多租户（CRUD/Quota/Budget/Audit/Isolation/Governance） | v1, v3~v5, v7~v8, v11~v12 | ~6 |
| 事件/通知/Webhook/SSE（Bus/Transport/Retry/Postgres LISTEN/NOTIFY） | v1, v3~v6, v8~v9, v11~v12 | ~7 |
| 复制/高可用（Async/Multi-Region/Cluster/Active-Active/Federation） | v1, v3~v5, v9, ROADMAP #3, #10 | ~6 |
| Reconcile/GC/Lifecycle（Orphan/Retention/Scrub/Idempotency GC） | v1, v4, v6~v7, ROADMAP #5, #8 | ~5 |
| 合规（WORM/Legal Hold/Retention/Disposition/Client-Side Encryption） | v2, v6, v8~v10, v12 | ~5 |
| 内容智能（DLP/分类/格式转换/预览/CAS/元数据 Schema） | v6~v8, v12 | ~4 |
| 可观测性（OTel/Metrics/Grafana/Prometheus/SLO/Tracing/Debug） | v11, v13, v14 | ~4 |
| 工程质量（内存安全/并发/压缩/诊断/错误模型/测试） | v11, v14 | ~6 |
| Web UI / Admin UI（管理/仪表板/搜索体验/Admin CRUD） | v3, v6, v10~v11 | ~4 |
| 基础设施基线（配置热重载/IP 访问控制/内置 TLS） | v16 | ~3 |
| S3 子功能（Bucket Inventory/对象级 Legal Hold API/Retention API） | v16 | ~2 |
| 其他（API 治理/备份/迁移/优雅关闭/CDN/分享链接/Federation） | v2, v4, v8, v10~v11, v13 | ~6 |

> **本期选点原则：** 选取**S3 标准生态中市场默认期待、但当前完全缺失或仅存骨架**的 5 个方向。它们不是"锦上添花"——在用户从 AWS S3 / MinIO / Ceph 迁移评估中，这些是"有就有、没有就回不去"的关键决策项。

---

## 本期方向总览

| # | 方向 | 类型 | 当前状态 | 既有覆盖 |
|---|------|------|---------|---------|
| 1 | **🔴 对象生命周期转换引擎（Object Lifecycle Transition）** | 数据生命周期 | 仅支持过期删除，无存储类转换/版本过期/Multipart 清理 | v1 小节 + ROADMAP #9 方向性提及，**零独立设计** |
| 2 | **🔴 通知事件过滤与多通道分发（Notification Filter & Multi-Destination）** | 事件系统 | 配置骨架存在，运行时无过滤/无多通道 | v1 行级提及 "hook S3 events to external systems"，**零独立设计** |
| 3 | **🟠 桶级复制规则（Bucket Replication CRR/SRR）** | 数据保护 | 全局全量复制，无桶级/前缀级/标签级规则 | v1 全局复制 + ROADMAP #3 方向性提及，**零独立设计** |
| 4 | **🟠 操作级多因素认证（MFA Delete / MFA Protection）** | 安全 | 完全缺失 | 零覆盖 |
| 5 | **🟠 服务端访问日志运行时引擎（Server Access Logging）** | 合规/审计 | 配置骨架（迁移0023+API路由+Repo方法）存在，**运行时日志生产完全缺失** | v8 行级提及，**零独立设计** |

---

## 1. 🔴 对象生命周期转换引擎

### 为什么需要它

S3 Lifecycle 规则支持四种动作类型，当前 AeroVault 仅实现了其中一种：

| S3 Lifecycle 动作 | 当前状态 | 缺失 |
|-------------------|---------|------|
| **Expiration** — 对象过期删除 | ✅ `ExpireAfterDays` + `soft_delete/hard_delete` | 仅 hard/soft delete，无 `ExpiredObjectDeleteMarker` |
| **Transition** — 存储类转换 | ❌ **完全缺失** | 无 STANDARD→STANDARD_IA→GLACIER→DEEP_ARCHIVE 转换 |
| **NoncurrentVersionExpiration** — 非当前版本过期 | ❌ **完全缺失** | 版本化桶中旧版本无限堆积 |
| **NoncurrentVersionTransition** — 非当前版本转换 | ❌ **完全缺失** | 旧版本无法降冷 |
| **AbortIncompleteMultipartUpload** — 未完成分片清理 | ❌ **完全缺失** | 中断的分片上传泄露存储 |
| **ExpiredObjectDeleteMarker** — 过期删除标记清理 | ❌ **完全缺失** | 删除标记残留 |

**为什么这个缺失是致命的：**

1. **成本失控** — 没有自动转换，所有数据永远留在成本最高的存储层。对于需要保留但很少访问的日志/备份/合规数据，用户只能手动管理或选择离开。
2. **版本爆炸** — 版本化桶中每次 PUT 创建新版本，旧版本没有自动过期机制。一个活跃桶数月内可以产生数百万个版本行。
3. **分片泄露** — 中断的分片上传在 S3 标准实现中会被 lifecycle 规则清理（默认 7 天），当前版本没有任何防御机制。
4. **S3 API 断档** — `PUT /{bucket}?lifecycle` 已实现，但返回的规则仅包含 `Expiration.Days`，不包含 `Transition`/`NoncurrentVersionExpiration`/`AbortIncompleteMultipartUpload`。从 AWS S3 导入的 lifecycle XML 被静默截断或返回错误。

### 当前状态

```go
// internal/repository/repository.go:BucketConfig
type BucketConfig struct {
    // ...
    ExpireAfterDays int    // 仅支持过期天数
    ExpireAction    string // "soft_delete" | "hard_delete"
    // 没有 TransitionRules, NoncurrentVersionRules, AbortIncompleteUploads
}
```

| 代码位置 | 当前行为 | 缺失 |
|---------|---------|------|
| `internal/repository/repository.go:BucketConfig` | `ExpireAfterDays` + `ExpireAction` | 无 `TransitionRules []TransitionRule`、`NoncurrentVersionRules`、`AbortIncompleteMPUDays` |
| `internal/repository/sql_buckets.go:GetBucketConfig/SetBucketLifecycle` | 读写 `expire_after_days` + `expire_action` | 无 transition/noncurrent/abort 字段 |
| `internal/repository/migrations/0007_lifecycle.up.sql` | Schema 含 `lifecycle_rules TEXT`？ | 需确认是否仅存 `expire_after_days` |
| `internal/reconcile/lifecycle.go` | 扫描过期对象 → hard/soft delete | 无 transition handler、noncurrent version handler、abort multipart handler |
| `internal/api/s3compat/bucketconfig.go:putBucketLifecycle` | 解析 XML，仅取第一个 `Expiration.Days` | 丢弃 `Transition`、`NoncurrentVersionExpiration`、`AbortIncompleteMultipartUpload` |
| `internal/service/file_features.go:SetBucketLifecycle` | 仅接收 days+action | 无 transition/version/abort 参数 |
| `internal/reconcile/` | Run() 内生命周期循环 | 无 transition 调度 |

### 边界情况暴露

| Edge Case | 场景 | 后果 |
|-----------|------|------|
| **存储类不存在** | Transition 规则指向 `GLACIER` 但后端未配置 `cold` 存储 | 转换失败 → 对象停留在原层 → 静默不对齐 |
| **转换后访问** | 对象从 STANDARD 转换到 GLACIER 后 GET 访问 | 需要恢复（restore）流程，当前无 `?restore` 支持 |
| **版本+生命周期交集** | 桶同时开启版本控制和 lifecycle transition | 当前版本→热层，旧版本→冷层，需要版本感知的 route rules |
| **规则冲突** | 同一对象的 expiration 和 transition 在同一天触发 | 需要 S3 语义：expiration 优先于 transition |
| **超大桶** | 1000 万对象的转换需要分批进行 | 单次 reconcile 循环可能超时/内存溢出 |
| **转换成本** | 每天 100 万对象从 STANDARD→STANDARD_IA（按扫描量计费） | 需要 throttling 和成本告警 |

### 架构方向

```
┌──────────────────────────────────────────────────────────────────┐
│                 Lifecycle Transition Engine                       │
│                                                                  │
│  Schema 扩展（迁移 0025）：                                        │
│    buckets 表新增字段（JSON, 替代旧的 expire_after_days/action）： │
│      lifecycle_rules TEXT DEFAULT '[]'                           │
│    JSON 结构：                                                    │
│      [{                                                            │
│        "id": "transition-old-logs",                                │
│        "status": "Enabled",                                        │
│        "filter": {"prefix": "logs/", "tags": {"type":"archive"}}, │
│        "transitions": [{                                          │
│          "days": 30,                                               │
│          "storage_class": "STANDARD_IA"                            │
│        }, {                                                       │
│          "days": 90,                                               │
│          "storage_class": "GLACIER"                                │
│        }],                                                         │
│        "expiration": {"days": 365},                                │
│        "noncurrent_version_transition": {                          │
│          "noncurrent_days": 30,                                    │
│          "storage_class": "GLACIER"                                │
│        },                                                          │
│        "noncurrent_version_expiration": {"noncurrent_days": 90},   │
│        "abort_incomplete_multipart_upload": {"days_after_init": 7} │
│      }]                                                            │
│                                                                   │
│  迁移策略：                                                         │
│    • 首次部署自动将旧的 expire_after_days 迁移到 lifecycle_rules    │
│    • 读取时兼容旧字段（ReadLifecycleRules 函数有两个 fallback 路径）│
│                                                                   │
│  执行引擎（internal/reconcile/lifecycle.go 扩展）：                 │
│    每个 reconcile 周期：                                            │
│      1. 加载所有启用了 lifecycle_rules 的桶                         │
│      2. 对每个规则按类型处理：                                       │
│         a. Transition → 筛选对象 → 调用 BackendRouter.Select()     │
│                          → 复制到目标后端 → 更新 Object.Backend     │
│                          → 删除源 blob                              │
│         b. Expiration → 现有逻辑（hard/soft_delete）                │
│         c. NoncurrentVersionExpiration → ListVersions → 删除旧版本  │
│         d. AbortIncompleteMPU → ListUploads → AbortMultipartUpload  │
│      3. 分批 + 限速（每次循环处理 N 个对象，避免 DB 锁死）           │
│                                                                   │
│  与多后端路由的关系（expansion v15）：                               │
│    Lifecycle Transition 是**多后端路由的驱动者**：                   │
│    • BackendRouter 定义"有哪些后端"                                 │
│    • Lifecycle Rule 定义"什么数据何时去哪层"                        │
│    • 两者共同实现"数据生命周期自动化"                                │
└──────────────────────────────────────────────────────────────────┘
```

### 实现理由

**商业价值：** 数据生命周期管理是对象存储的核心价值主张之一。没有 transition 能力，所有数据永远在最高成本层存储，导致 TCO 竞争力严重不足。多数 S3 迁移候选的第一批问题就是"你们的 lifecycle 支持 transition 吗？"

**技术必要性：** 当前存储系统缺乏自动化的版本清理和分片清理机制，长期运行必然导致存储膨胀。加上版本化桶后，这个缺口会迅速从"可接受的 GC 延迟"变为"不可接受的存储成本失控"。

**代码复杂度：** 中到高。需要：① schema 迁移 + 下兼容（~100 行）；② lifecycle rule 解析 + 领域模型（~150 行）；③ 执行引擎，按规则类型分别实现（~400 行）；④ S3 API lifecycle XML 全面解析（~150 行）；⑤ 与 BackendRouter 集成（~50 行）。总体 ~850 行。

---

## 2. 🔴 通知事件过滤与多通道分发

### 为什么需要它

当前 AeroVault 的事件通知模型是一个**单一、全局、未过滤的 firehose**：

```
对象变更 → EventBus → Webhook(POST to EVENTS_WEBHOOK_URL)
                     → SSE(所有事件推送到所有连接)
                     → Indexer(所有对象都索引)
                     → Antivirus(所有对象都扫描)
                     → Replication(所有对象都复制)
```

所有订阅者收到**相同的事件流**，没有任何过滤机制。

而 AWS S3 Event Notifications 支持：

| 能力 | S3 标准 | AeroVault 当前 |
|------|---------|---------------|
| **事件类型过滤** | `s3:ObjectCreated:*`, `s3:ObjectCreated:Put`, `s3:ObjectRemoved:*` 等 | ✅ 已有 `EventType` 枚举（created/deleted/accessed），但**未在调度层使用** |
| **前缀/后缀过滤** | `FilterKey` 规则 | ❌ 配置骨架存在（`NotificationRule.FilterKey`）但**运行时未评估** |
| **标签过滤** | `s3:ObjectTag` 条件 | ❌ 完全缺失 |
| **多目标** | 同时发送到多个 SQS Queue / SNS Topic / Lambda | ❌ 仅有 `EVENTS_WEBHOOK_URL` |
| **批量通知** | 聚合 5 分钟内的事件 | ❌ 每次事件单独发送 |
| **重试策略** | 每目标独立重试 | ⚠️ 全局 webhook 有重试 |

### 当前状态

```go
// internal/repository/repository.go:NotificationRule
type NotificationRule struct {
    ID        string   `json:"Id"`
    Events    []string `json:"Events"`
    FilterKey string   `json:"FilterKey,omitempty"`
    QueueARN  string   `json:"QueueArn,omitempty"`           // "webhook URL or queue ARN"
    TopicARN  string   `json:"TopicArn,omitempty"`           // unused, kept for compat
    LambdaARN string   `json:"LambdaFunctionArn,omitempty"`  // unused, kept for compat
}
```

| 代码位置 | 当前行为 | 缺失 |
|---------|---------|------|
| `internal/repository/repository.go:NotificationRule` | 结构体定义完整（Events/FilterKey/QueueARN） | 缺 `FilterSuffix`、`FilterTags`、`BatchWindowSeconds`、`DestinationType` |
| `internal/repository/sql_buckets.go:SetBucketNotifications` | 存储 JSON 到 `notification_rules` | ✅ 存储正常工作 |
| `internal/api/s3compat/handler.go:BucketDispatch` | 无 `?notification` 路由 | ❌ S3 API 未注册 |
| `internal/events/webhook.go` | `Run()` 订阅所有事件 → POST | ❌ 无规则评估、无过滤、无多目标 |
| `internal/api/rest/sse.go:Stream` | 推送全部事件 | ❌ 无客户端过滤 |
| `internal/events/bus.go:Subscribe()` | 返回全量 chan | ❌ 订阅时不带过滤条件 |

### 边界情况暴露

| Edge Case | 场景 | 后果 |
|-----------|------|------|
| **规则变更时处理中事件** | 修改通知规则时有一条正在发送的事件 | 事件可能按旧规则/新规则发送，需要最终一致性保障 |
| **规则无匹配** | 事件类型和前缀均不匹配任何规则 | 该事件不应发送到任何目标（当前：全部发送） |
| **目标不可达** | SQS/SNS 端点在区域故障中 | 需要与 webhook 相同的 durable retry 机制 |
| **事件风暴** | 批量上传 10 万个文件，每个触发一个通知 | 10 万条消息瞬间涌入，需要 batch + throttling |
| **循环通知** | 通知目标是一个 Lambda，Lambda 写回同一个桶 | 无限触发循环；需要 `x-amz-event-loop-detection` 或深度限制 |
| **IAM 鉴权** | 向 SQS 发送消息需要 AWS 凭证 | 当前无跨服务凭证管理 |

### 架构方向

```
┌──────────────────────────────────────────────────────────────────┐
│              Notification Dispatch Engine                        │
│                                                                  │
│  事件源: EventBus.Publish (所有 FileService 变更事件)              │
│                                                                  │
│  调度器（新 goroutine，替代 webhook.Run 的原始订阅方式）：          │
│    ┌─────────────────────────────┐                               │
│    │  NotificationDispatcher     │                               │
│    │  ┌───────────────────────┐  │                               │
│    │  │ 1. 收到事件            │  │                               │
│    │  │ 2. 查询桶的            │  │                               │
│    │  │    notification_rules │  │                               │
│    │  │ 3. 逐规则评估:         │  │                               │
│    │  │    - EventType 匹配    │  │                               │
│    │  │    - FilterKey 匹配    │  │                               │
│    │  │    - FilterSuffix 匹配 │  │                               │
│    │  │ 4. 匹配→发送到目标      │  │                               │
│    │  └───────────────────────┘  │                               │
│    └─────────────────────────────┘                               │
│              │                                                   │
│     ┌────────┼────────┬────────┬────────┐                        │
│     ▼        ▼        ▼        ▼        ▼                        │
│  ┌──────┐ ┌──────┐ ┌──────┐ ┌──────┐ ┌──────┐                  │
│  │Webhook│ │SQS   │ │SNS   │ │Lambda│ │SSE   │                  │
│  │HTTP   │ │Send  │ │Pub   │ │Invoke│ │Filter│                  │
│  │POST   │ │Msg   │ │      │ │      │ │Push  │                  │
│  └──────┘ └──────┘ └──────┘ └──────┘ └──────┘                  │
│                                                                  │
│  接口抽象:                                                        │
│    type NotificationDestination interface {                       │
│        Send(ctx, event) error                                     │
│        Type() string  // "webhook" | "sqs" | "sns" | "lambda"    │
│    }                                                              │
│                                                                  │
│  规则引擎:                                                        │
│    type FilterRule struct {                                       │
│        Events    []string     // "s3:ObjectCreated:*"             │
│        FilterKey *string      // prefix                           │
│        FilterSuffix *string   // suffix                           │
│        FilterTags  map[string]string                              │
│    }                                                              │
│                                                                  │
│  S3 API 兼容:                                                     │
│    GET /{bucket}?notification → <NotificationConfiguration>      │
│    PUT /{bucket}?notification → 接收 <NotificationConfiguration> │
└──────────────────────────────────────────────────────────────────┘
```

### 实现理由

**商业价值：** 企业级对象存储的通知系统是其"事件驱动集成"的核心。没有多目标/过滤的通知，用户无法构建基于 S3 事件的标准工作流（实时 ETL、增量备份、CDN 刷新、Lambda 处理）。这是迁移评估中仅次于 S3 API 兼容性的第二大类需求。

**技术必要性：** 当前将所有事件发送到单一 webhook 的模式对于多租户场景是灾难性的——租户 A 的事件流中混入租户 B 的事件，每个租户收到与自己无关的流量。更严重的是，当前系统不支持通知过滤意味着 Indexer/Antivirus/Replication 都在处理全局事件流，随着租户增多，无用工作比例急剧上升。

**代码复杂度：** 中。需要：① NotificationRule schema 扩展（~50 行）；② FilterEngine（~150 行）；③ Dispatcher goroutine（~200 行）；④ S3 `?notification` handler（~150 行）；⑤ Destination adapter 接口 + webhook adapter 重构（~100 行）。注意：SQS/SNS/Lambda 适配器可后续实现，本期仅完成 dispatcher 骨架 + webhook 重构 + S3 API。总体 ~650 行。

---

## 3. 🟠 桶级复制规则（S3 CRR/SRR）

### 为什么需要它

当前 AeroVault 的复制（Replication）是一个**全局级、全量、不可配置**的开关：

```
REPLICATION_ENABLED=true
→ 所有 object.created / object.deleted 事件 → JobReplicate → 复制到目标后端
```

而 AWS S3 的复制（Cross-Region Replication / Same-Region Replication）是**桶级、规则驱动、高度可配置**的：

| 能力 | S3 CRR/SRR | AeroVault 当前 |
|------|-----------|---------------|
| **作用范围** | 按桶启用 | ❌ 全局（所有桶） |
| **前缀过滤** | `Filter.Prefix` | ❌ 无过滤 |
| **标签过滤** | `Filter.Tag` | ❌ 无过滤 |
| **目标桶** | 每条规则指定不同目标桶/区域 | ❌ 单目标后端 |
| **存储类覆盖** | 复制到目标时设置存储类 | ❌ 不可配置 |
| **复制指标** | `ReplicationLatency`、`PendingBytes` | ❌ 无 |
| **复制时间控制** | `ReplicationTimeControl`（15 分钟 SLA） | ❌ 无 |
| **删除标记复制** | `DeleteMarkerReplication` | ❌ 软删除/硬删除事件均可触发复制 |
| **元数据复制** | 可选择复制或替换 metadata | ❌ 当前复制原始 blob → metadata 丢失 |

### 当前状态

```go
// internal/config/config.go:ReplicationCfg
type ReplicationCfg struct {
    Enabled bool
    Storage StorageConfig  // 单目标后端
}
```

| 代码位置 | 当前行为 | 缺失 |
|---------|---------|------|
| `internal/config/config_app.go:ReplicationCfg` | 全局 enable/disable + 单目标 | 无每条规则独立的目标/过滤/配置 |
| `internal/replication/replication.go` | 订阅所有事件 → 解码 object ID → Get → Put 到目标后端 | 无规则评估、无前缀/标签过滤、无 metrics |
| `internal/service/file.go:emit` | 所有 mutating 操作发布事件 | 事件无"是否应被复制"标记 |
| `internal/repository/repository.go` | 无复制规则存储 | 无 `ReplicationRule` 类型或表 |
| `internal/api/s3compat/bucketconfig.go` | 无 `?replication` handler | ❌ S3 API 缺失 |
| `internal/reconcile/` | 无复制延迟监控 | ❌ 无复制健康检查 |

### 边界情况暴露

| Edge Case | 场景 | 后果 |
|-----------|------|------|
| **规则冲突** | 对象同时匹配两条复制规则 | 需要优先级：首次匹配或最精确匹配 |
| **循环复制** | 桶 A 复制到桶 B，桶 B 复制回桶 A | 无限循环 → 存储爆炸 |
| **复制中对象更新** | 对象正在复制时被覆盖 | 旧版本复制到目标后被新版本覆盖，但事件顺序可能错乱 |
| **版本化目标** | 源桶和目标桶都开启版本化 | 目标应保留每个复制版本的 version ID（或记录映射） |
| **复制失败** | 目标后端故障 | 重试队列增长 → 需要复制延迟告警 |
| **存量数据复制** | 现有数据需要一次性的批量复制到新目标 | 当前仅实时事件触发，无存量回填能力 |
| **删除事件复制** | 源桶删除对象后，目标是否同步删除 | 需要明确策略：`DeleteMarkerReplication: Enabled` 或 `Disabled` |

### 架构方向

```
┌──────────────────────────────────────────────────────────────────┐
│              Bucket Replication Rules Engine                     │
│                                                                  │
│  Schema 扩展（迁移 0026）：                                        │
│    CREATE TABLE bucket_replication (                              │
│        id TEXT PRIMARY KEY,                                       │
│        tenant_id TEXT NOT NULL,                                   │
│        bucket TEXT NOT NULL,                                      │
│        role_arn TEXT NOT NULL DEFAULT '',                         │
│        destination_bucket TEXT NOT NULL,                          │
│        destination_storage_class TEXT DEFAULT '',                 │
│        prefix TEXT DEFAULT '',                                    │
│        tags TEXT DEFAULT '{}',   -- JSON map                      │
│        status TEXT DEFAULT 'Enabled',                             │
│        delete_marker_replication TEXT DEFAULT 'Disabled',         │
│        metrics_enabled BOOLEAN DEFAULT false,                     │
│        created_at TEXT NOT NULL                                   │
│    )                                                              │
│                                                                  │
│  规则模型:                                                        │
│    type ReplicationRule struct {                                  │
│        ID             string                                      │
│        Status         string     // Enabled | Disabled            │
│        Prefix         string     // filter by prefix              │
│        Tags           map[string]string // filter by tags          │
│        DestBucket     string     // logical bucket name           │
│        DestStorage    string     // 目标后端 backend kind         │
│        DestClass      string     // 目标存储类                     │
│        DeleteMarker   bool       // 是否同步删除标记               │
│        MetricsEnabled bool       // 报告复制指标                   │
│    }                                                              │
│                                                                  │
│  复制 Worker 改造（internal/replication/replication.go）：         │
│    当前:                                                          │
│      Run(ctx) → Subscribe() → 所有事件 → ReplicateObjectByID    │
│    新:                                                            │
│      Run(ctx) → Subscribe() → 对每个事件:                         │
│        1. 解析事件 → 获取 tenant/bucket/key/type                  │
│        2. 查询该桶的全部 replication rules                      │
│        3. 逐规则评估: prefix 匹配? tags 匹配?                    │
│        4. 匹配 → 对每个匹配规则分派复制任务                       │
│        5. 复制任务包含: 目标后端标识符、存储类覆盖                  │
│                                                                  │
│  S3 API 兼容:                                                    │
│    GET /{bucket}?replication → <ReplicationConfiguration>       │
│    PUT /{bucket}?replication → 接收 <ReplicationConfiguration>  │
│    DELETE /{bucket}?replication → 删除所有复制规则               │
│                                                                  │
│  复制指标:                                                       │
│    replication_pending_objects{rule_id}  gauge                   │
│    replication_pending_bytes{rule_id}     gauge                  │
│    replication_latency_seconds{rule_id}   histogram              │
│    replication_failed_total{rule_id}      counter                │
│                                                                  │
│  存量回填:                                                        │
│    POST /v1/admin/replication/backfill                           │
│      { "rule_id": "xxx", "max_objects": 10000 }                 │
│    → JobPool 任务：扫描存量对象 + 匹配规则 + 入队列复制            │
└──────────────────────────────────────────────────────────────────┘
```

### 实现理由

**商业价值：** 桶级复制是 S3 跨区域灾备的基石。当前全局全量复制的模式在以下场景中完全不可用：① 只有部分数据需要跨区域保护（如生产数据复制、日志不复制）；② 不同数据需要复制到不同目标（如亚洲数据复制到东京、欧美数据复制到法兰克福）；③ 需要按标签筛选复制（如 `compliance=true` 的对象才复制）。

**技术必要性：** 全局强制复制意味着无论用户是否需要，每一笔写操作都在等待复制 worker 的排队和处理。这既增加了不必要的负载，又让真正需要复制的关键数据与噪声数据在同一个队列中竞争资源。

**代码复杂度：** 中。需要：① 复制规则表 + CRUD（~150 行）；② Worker 改造为规则评估模式（~250 行）；③ S3 `?replication` handler（~200 行）；④ 存量回填 job（~100 行）；⑤ 复制指标（~50 行）。总体 ~750 行。

---

## 4. 🟠 操作级多因素认证（MFA Delete）

### 为什么需要它

AWS S3 支持对以下敏感操作要求 MFA（Multi-Factor Authentication）：

| 操作 | S3 MFA 保护 | 当前状态 |
|------|------------|---------|
| **Permanent delete (versioned bucket)** | `x-amz-mfa` header 要求 | ❌ 完全缺失 |
| **Disable versioning** | 要求 MFA | ❌ 完全缺失 |
| **Change bucket lifecycle** | 要求 MFA（可选） | ❌ 完全缺失 |
| **Suspend MFA Delete** | 要求 MFA | ❌ 完全缺失 |
| **Bypass Governance Retention** | 要求 MFA（可选） | ❌ 完全缺失 |

**为什么这个缺失是致命的：**

1. **一把钥匙毁所有** — 如果 API Key 泄露，攻击者可以永久删除所有数据（包括版本化桶中的所有版本），没有任何二次验证。在合规审计中这是一个典型的"单一故障点"。
2. **社会工程防护** — 即使通过 RBAC/权限管理控制了"谁可以删除"，如果开发者的本地环境被入侵，攻击者依然可以用开发者的 key 执行破坏操作。MFA 提供了"即使 key 被偷，攻击者也无法完成敏感操作"的安全层。
3. **合规硬需求** — SOC2、ISO 27001、PCI DSS、HIPAA 等标准要求对数据销毁操作有"强认证"控制。无 MFA 的存储系统在合规评审中会被直接标记为重大缺陷。

### 当前状态

```go
// internal/auth/auth.go:Registry
type Registry struct {
    // 无 MFA 相关字段或方法
}
```

| 代码位置 | 当前行为 | 缺失 |
|---------|---------|------|
| `internal/auth/auth.go:Registry` | API Key / JWT / SigV4 验证 | 无 MFA Provider / MFA Token 验证 |
| `internal/auth/auth_middleware.go` | 提取 Bearer / X-Api-Key | 不读取 `x-amz-mfa` header |
| `internal/auth/policy.go` | IAM 策略引擎支持 `aws:MultiFactorAuthPresent` 条件 | 条件存在但在运行时**从未赋值** |
| `internal/service/file_crud.go:Delete` | 硬删除检查 LegalHold/LockedUntil | 无 MFA 检查 |
| `internal/service/file_features.go:SetBucketVersioning` | 直接切换 | 无 MFA 验证 |
| `internal/api/s3compat/handler.go:deleteObject` | 直接调用 Delete | 无 MFA header 检查 |
| `internal/config/config.go` | 无 MFA 配置 | 无 `AUTH_MFA_*` 配置项 |

### 边界情况暴露

| Edge Case | 场景 | 后果 |
|-----------|------|------|
| **MFA 令牌验证** | 如何验证 TOTP/HOTP 令牌？ | 需要集成 TOTP（Time-based One-Time Password）验证器 |
| **MFA 缓存** | 每次请求都要求 MFA 太繁重 | 需要 `MFA_SESSION_TIMEOUT`（如 15 分钟内同一会话免 MFA） |
| **MFA 与 SigV4** | SigV4 签名中如何携带 MFA 令牌？ | 需要支持 `x-amz-mfa` header 作为 SigV4 扩展 |
| **MFA 令牌同步** | 多副本部署中 MFA 会话状态 | 需要共享会话状态或容忍最终一致性 |
| **MFA 设备管理** | 用户如何注册/更换 MFA 设备？ | 需要 `/v1/admin/mfa/devices` API |
| **MFA 恢复码** | 用户丢失 MFA 设备 | 需要一次性恢复码（类似 Google Auth 的 backup codes） |
| **不带 MFA 的操作拒绝** | 未提供 MFA 的硬删除请求 | 应返回 `403 AccessDenied`，附带指示需要 MFA 的提示 |

### 架构方向

```
┌──────────────────────────────────────────────────────────────────┐
│                 MFA Authentication System                        │
│                                                                  │
│  组件概览:                                                       │
│    ┌──────────┐     ┌─────────────┐     ┌────────────────────┐  │
│    │ TOTP     │     │ MFA         │     │ Policy Condition   │  │
│    │ Provider │━━━━▶│ Middleware   │━━━━▶│ Engine             │  │
│    │ (OTP)    │     │ (Auth后)    │     │ (检查 MFA 条件)    │  │
│    └──────────┘     └─────────────┘     └────────────────────┘  │
│                            │                                     │
│                            ▼                                     │
│                     ┌──────────────┐                             │
│                     │ MFA Session  │                             │
│                     │ Cache (TTL)  │                             │
│                     └──────────────┘                             │
│                                                                  │
│  配置:                                                           │
│    AUTH_MFA_ENABLED=true                                         │
│    AUTH_MFA_ISSUER=aero-vault  (TOTP URI issuer)                 │
│    AUTH_MFA_SESSION_TTL=900     (秒, 默认 15 分钟)               │
│                                                                  │
│  API:                                                            │
│    POST /v1/admin/mfa/enroll                                    │
│      → 返回 TOTP URI + QR code URL + backup codes (10 个)       │
│    POST /v1/admin/mfa/verify                                    │
│      { "token": "123456" } → 验证并激活                          │
│    POST /v1/admin/mfa/disable                                   │
│      { "token": "123456" } → 关闭 MFA (需要有效 token)          │
│    POST /v1/admin/mfa/recovery                                  │
│      { "code": "XXXX-XXXX" } → 使用恢复码登录 (消耗一个)        │
│                                                                  │
│  S3 x-amz-mfa header 格式:                                       │
│    x-amz-mfa: "arn:aws:iam::123456:mfa/user 123456"             │
│    → 格式: "<device> <token>"                                    │
│    → AeroVault 格式: "<tenant>/<user> <6-digit-totp>"           │
│                                                                  │
│  强制策略:                                                       │
│    桶级 `MfaDelete: Enabled` 配置 (BucketConfig 新增字段)       │
│      → 该桶上的 HardDelete + DisableVersioning 要求 MFA         │
│    对象级 Governance Retention 绕过要求 MFA                      │
│                                                                  │
│  会话管理:                                                       │
│    MFA 验证成功后, 生成 session token (JWT, 15分钟有效期)        │
│    后续请求携带 `X-Aero-MFA-Session` header 免重复验证           │
│    session token 绑定到请求者的 access key                       │
└──────────────────────────────────────────────────────────────────┘
```

### 实现理由

**商业价值：** MFA Delete 是 S3 安全基线的标志性功能之一。在金融、政务、医疗行业的合规评审中，"是否支持删除操作的 MFA 保护"是一个标准问题。不支持即被淘汰。

**技术必要性：** 当前安全模型依赖"单一凭证"——API Key 泄露等于完全失控。MFA 提供了纵深防御：即使 Key 泄露，攻击者仍然无法永久删除数据。对于版本化桶，一次硬删除可能抹去所有版本历史（当前实现中 `HardDeleteObject` 硬删除所有 blob）。

**代码复杂度：** 中。需要：① TOTP 验证器（可使用 `github.com/pquerna/otp`，或纯 Go 实现）（~100 行）；② MFA 中间件 + header 解析（~100 行）；③ MFA 会话缓存（~80 行）；④ MFA 管理 API（enroll/verify/disable/recovery）（~200 行）；⑤ 桶级 MfaDelete 配置 + S3 API（~100 行）；⑥ Service 层删除/MFA 检查集成（~80 行）。总体 ~660 行。

---

## 5. 🟠 服务端访问日志运行时引擎

### 为什么需要它

S3 服务端访问日志（Server Access Logs）是 S3 的标准审计功能：每个对桶的请求（GET/HEAD/PUT/DELETE/LIST）都会生成一条日志记录，输出到指定目标桶的另一前缀下。

**当前状态：配置骨架存在，运行时完全缺失。**

```
✅ 迁移 0023: bucket_logging → schema 准备好了
✅ Repository: GetBucketLogging / SetBucketLogging / DeleteBucketLogging
✅ Service: GetBucketLogging / SetBucketLogging / DeleteBucketLogging
✅ REST API: GET/PUT/DELETE /v1/buckets/{bucket}/logging
✅ S3 API handler: getBucketLogging / putBucketLogging / deleteBucketLogging

❌ 但是没有一个人在实际请求路径上调用 WriteAccessLog!
```

这意味着：
- 客户可以通过 API 配置桶的访问日志目标（"日志往这个桶写"）
- **但实际没有任何日志被写入**
- 合规审计要求"必须记录每一次对象访问"，当前完全无法满足
- 无法回答"谁在什么时间访问了什么对象"这个最基本的问题

### 当前状态

```go
// internal/repository/repository.go
type Repository interface {
    // ...
    WriteAccessLog(ctx context.Context, tenant, sourceBucket, method, key, status, latencyMs, userAgent string) error
    // ...
}
```

| 代码位置 | 当前行为 | 缺失 |
|---------|---------|------|
| `internal/repository/sql_buckets.go:WriteAccessLog` | 💡 方法实现了（INSERT INTO access_logs） | 但**无人调用** |
| `internal/repository/migrations/0023_bucket_logging.up.sql` | 创建了 `bucket_logging` 配置表 | 但**没有 `access_logs` 表**来存日志行 |
| `internal/middleware/middleware.go:AccessLog` | HTTP 层面的访问日志（标准格式） | 非 S3 格式，不写入配置的目标桶 |
| `internal/api/s3compat/handler.go` | 每个 handler 处理请求后返回 | 没有调用 `WriteAccessLog` |
| `internal/service/file_crud.go:Get/Put/Delete` | 仅 emit event | 不记录访问日志 |
| `internal/reconcile/` | 无访问日志清理 | 长期积累的日志行需要清理策略 |

### 边界情况暴露

| Edge Case | 场景 | 后果 |
|-----------|------|------|
| **日志目标桶也是日志源** | 日志桶本身开启了 access logging | 无限递归 → 存储爆炸 |
| **高 QPS 桶** | 每秒 10K 请求，每个请求写一条日志 | DB INSERT 瓶颈；需要批量写或异步写 |
| **日志一致性** | 异步写入 → 访问请求返回后日志才落盘 | 宕机丢失最后几秒的日志 |
| **日志格式** | S3 访问日志有标准格式（`79 fields`） | 需要对齐格式以便现有日志分析工具（ELK、Athena）消费 |
| **日志清理** | 日志累积数 TB | 需要 lifecycle 规则管理日志自身的保留周期 |
| **日志目标不存在** | 配置的日志目标桶已被删除 | WriteAccessLog 静默失败或返回错误 |

### 架构方向

```
┌──────────────────────────────────────────────────────────────────┐
│              Server Access Log Engine                            │
│                                                                  │
│  架构变更:                                                        │
│    当前:                                                         │
│      S3 Handler → FileService → 响应                              │
│      中间没有任何日志记录点                                       │
│                                                                  │
│    新:                                                            │
│      ┌────────────────────────────────────────────────┐          │
│      │  AccessLogProducer (中间件)                    │          │
│      │  位置: S3 route group 内（/s3/*）              │          │
│      │  职责: 在请求完成后记录日志                       │          │
│      │  数据: method, key, status, latency,           │          │
│      │         user-agent, request-id, tenant         │          │
│      └──────────────┬─────────────────────────────────┘          │
│                     ▼                                             │
│      ┌────────────────────────────────────────────────┐          │
│      │  AccessLogWriter (新的后台 Worker)              │          │
│      │  职责:                                          │          │
│      │    1. 从内存 buffer 批量读取日志 entry            │          │
│      │    2. 按目标桶分组                               │          │
│      │    3. 每 N 秒或每 M 条 flush 一次                │          │
│      │    4. 写入目标桶: <targetPrefix>/<YYYY>-<MM>-    │          │
│      │       <DD>-<HH>-<MM>-<UUID>.log                 │          │
│      │    5. 标准格式: space-delimited 79 字段         │          │
│      └────────────────────────────────────────────────┘          │
│                                                                  │
│  数据库 vs 文件:                                                 │
│    选择: 写入目标桶（作为对象）而非数据库                         │
│    理由:                                                         │
│      • S3 兼容：日志是标准对象，可被任何 S3 客户端读取            │
│      • 不影响 DB 性能：高 QPS 桶的日志不冲击元数据 DB            │
│      • 日志本身就是审计证据：作为对象存储满足监管要求              │
│      • 已有存储后端可用：local/S3/OSS/COS                         │
│                                                                  │
│  日志格式（S3 Server Access Log 兼容子集）：                      │
│    # 字段 (space-delimited, URL-encoded):                        │
│    bucket_owner bucket time remote_ip requester                  │
│    request_id operation key request_uri                          │
│    http_status error_code bytes_sent object_size                 │
│    total_time turn_around_time referer user_agent                │
│    version_id host_signature                                     │
│                                                                  │
│  Batch 写入策略:                                                  │
│    entry 进入 ring buffer（无锁）                                 │
│    每 60 秒或每 10000 条 flush 一次（谁先到谁触发）               │
│    flush: 按 (source_bucket, target_bucket) 分组                  │
│      → 构造日志对象 → storage.Put → 完成                         │
│    失败重试: 放回 buffer 前部（最多重试 3 次，否则丢日志）        │
│                                                                  │
│  防止循环:                                                        │
│    如果请求的目标桶本身开启了 access logging                       │
│    且请求的源桶等于日志的目标桶 → 该请求不产生 access log         │
│    （配置时校验 + 运行时检测双重防护）                             │
│                                                                  │
│  第一步（MVP）：                                                  │
│    1. 创建 access_logs 表（或直接写入 target bucket）             │
│    2. 在 S3 handler 的 route group 内添加 AccessLogProducer      │
│    3. 实现简单的同步写入（先走 DB，后续改异步文件写入）           │
│    4. S3 XML handler 返回 `?logging` 完整配置                    │
└──────────────────────────────────────────────────────────────────┘
```

### 实现理由

**商业价值：** 访问日志是合规审计的基础要求。金融监管（SOX、FINRA）、数据隐私（GDPR、CCPA）、医疗（HIPAA）等场景下，"谁在什么时间访问了什么数据"是必须回答的问题。没有访问日志功能的对象存储在这些行业中不可使用。

**技术必要性：** 当前配置 API 已经完整实现（迁移 0023、Repository、Service、REST、S3），但运行时路径完全缺失，这是一个**半成品状态**——用户配置了日志但没有任何日志产生，这会误导用户认为审计功能已正常工作，造成隐蔽的合规风险。

**代码复杂度：** 低到中。① AccessLogProducer 中间件（~80 行）；② AccessLogWriter 后台 Worker（~200 行）；③ S3 日志格式格式化（~100 行）；④ S3 API 的 `?logging` handler 已有（仅需微调）；⑤ 防止循环逻辑（~40 行）。总体 ~420 行。

---

## 总结：实施优先级

| 方向 | 影响 | 复杂度 | 风险 | 为什么优先 |
|------|------|--------|------|-----------|
| 1. 🔴 Lifecycle Transition | 客户留存/TCO | 中-高 | 低（新增功能，不触及核心路径） | 迁移评估第一问 |
| 2. 🔴 Notification Filter | 集成生态 | 中 | 中（重构 webhook Run() 路径） | 事件驱动工作流基础 |
| 3. 🟠 Bucket Replication | 灾备/合规 | 中 | 中（改造现有 Worker） | 多区域部署前提 |
| 4. 🟠 MFA Delete | 安全基线 | 中 | 低（新增独立组件） | 合规评审刚需 |
| 5. 🟠 Access Logging | 合规审计 | 低-中 | 低（补全已有骨架） | 修复半成品，低投入高回报 |

**建议实施阶段：**

| Phase | 方向 | 理由 |
|-------|------|------|
| **Phase 1**（最短路径） | #5 Access Logging | 代码骨架已存在，最低成本补齐，立即可用 |
| **Phase 1**（最短路径） | #4 MFA Delete | 独立组件，不影响现有路径，安全收益明确 |
| **Phase 2**（核心能力） | #1 Lifecycle Transition | 最大客户影响，但与多后端路由（v15）有依赖关系 |
| **Phase 2**（核心能力） | #2 Notification Filter | 重构 webhook 路径，需要充分测试 |
| **Phase 3**（高级能力） | #3 Bucket Replication | 依赖 #2 的规则评估模式，可复用相同架构模式 |

> **核心建议：** 先以最低成本补齐 #5（访问日志）和 #4（MFA Delete），在 1-2 周内交付两个"有就有、没有就没有"的合规功能。然后集中精力在 #1（Lifecycle Transition）上做深度设计——这是最影响用户 TCO 感知的能力，也是与多后端路由（v15）形成完整"数据生命周期自动化"故事的关键拼图。
