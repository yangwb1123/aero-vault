# 扩展方向分析 — 架构师视角的 5 个高价值缺口

> **分析日期：** 2026-07-10  
> **分析范围：** 全代码库扫描（cmd/server + internal/* + sdk/* + deploy/*）  
> **视角：** 资深架构师 / 产品经理 — 聚焦「阻挡真实生产落地的硬缺口」  
> **约束：** 不写代码，只识别方向、论证必要性、评估影响范围

---

## 背景

aero-vault 已覆盖对象存储的核心 CRUD、S3 兼容协议、多协议适配（REST / S3 / WebDAV / MCP）、AI/RAG 管道（提取→分块→嵌入→检索→生成→Agent）、租户/配额/审计、事件/Webhook、SSE 加密、跨区复制、缩略图、预签名 URL、条件请求等大量功能。代码库工程质量较高（接口隔离、测试覆盖率、CI gate、迁移框架、OTel 可观测性均已就位）。

但逐层扫描后，我发现 5 个**对生产化落地构成直接阻碍**的空缺。它们不是「锦上添花」的特性，而是在企业级部署中迟早会触到的硬天花板。以下按优先级排序。

---

## 方向一：合规对象锁（WORM — Governance + Compliance Mode）

### 现状

当前 `LockedUntil` 字段支持简单的**基于时间的保留**，在 bucket 级别配置 `ObjectLockSeconds`。但：

- **没有治理模式（Governance Mode）与合规模式（Compliance Mode）的区分**。S3 的 Object Lock 核心在于：Governance 模式下，拥有 `s3:BypassGovernanceRetention` 权限的用户可以提前释放锁；Compliance 模式下锁是绝对的，连 root 都无法覆盖。
- **没有法律搁置（Legal Hold）**。Legal Hold 是一个独立的、不限期的 ON/OFF 标志，与保留期限正交。法律/合规部门需要这个能力来标记诉讼相关的文档。
- **bucket 级别的默认锁配置不能阻止已被锁对象的 overwrite**。当前 `checkLockBeforeOverwrite` 只检查非版本化 bucket，版本化 bucket 下不会检查 — 这意味着版本化 bucket 中的用户可以通过 PUT 创建新版本「覆盖」一个 WORM 锁定的对象，而 WORM 的合规语义要求禁止任何修改。
- **锁的豁免角色 / Bypass 权限没有实现**。`s3:PutObjectRetention` / `s3:BypassGovernanceRetention` 等 IAM 语义完全缺失。

### 为什么需要

| 原因 | 影响 |
|------|------|
| **合规护栏** | SEC Rule 17a-4 / FINRA / HIPAA 等监管法规明确要求 WORM 存储。没有合规模式，金融、医疗、政府客户无法采购。 |
| **法律风险** | 诉讼中需要 Legal Hold 冻结相关文档。当前架构无法提供此保障，且已存储的数据可能因后续 PUT 被「合规绕过」。 |
| **竞争对等** | MinIO、Ceph RGW 均支持完整的 S3 Object Lock 语义。这个缺口是竞品对比中最容易被挑出来的弱点。 |

### 影响范围

| 层 | 变更量级 |
|----|---------|
| Repository | 新增 `legal_hold` 字段 + `governance_mode` 标志；现有 `locked_until` 扩展为保留期 + 模式 |
| Service | `checkLockBeforeOverwrite` 需在版本化路径下也生效；新增 `PutObjectRetention` / `PutObjectLegalHold` / `BypassGovernanceRetention` 逻辑 |
| Auth | Scope 中新增 `governance-bypass` 权限 |
| REST / S3 | 新增 `?retention` / `?legal-hold` 子资源路由 |
| Reconcile | 生命周期删除必须跳过 WORM 锁定的对象（当前 `handleExpiredObject` 已检查 LockedUntil，但缺少 Legal Hold 检查） |

---

## 方向二：事件通知体系（S3-兼容 Event Notifications）

### 现状

`EventBus` + `Webhook` 架构已经奠定了事件驱动的基础：
- 任何 `object.created` / `object.deleted` / `object.accessed` 事件被持久化到 `events` 表
- 单个全局 `EVENTS_WEBHOOK_URL` 可配置一个 webhook 端点
- 失败投递有指数退避重试（最多 10 次，`webhook_failures` 表）
- Postgres `LISTEN/NOTIFY` 可选地跨实例广播

### 缺口

1. **没有按 bucket 的独立通知配置**。虽然 `bucket_notifications` 迁移（0024）和 REST 路由已存在，但通知规则仅作为 JSON 存储，**没有任何路由逻辑**——事件的投递目的地、筛选条件、重试策略完全不按规则执行。所有事件仍然只发往全局 `EVENTS_WEBHOOK_URL`。
2. **没有事件类型过滤**。用户无法配置「只接收 `.pdf` 文件的 `s3:ObjectCreated:*` 事件，忽略其他」。
3. **没有多目标路由**。不能将同一 bucket 的事件同时发往多个 webhook / 队列，也无法按事件类型分发到不同终端。
4. **没有 Lambda / SQS / SNS 风格的集成点**。`NotificationRule` 结构体虽然包含 `QueueARN` / `TopicARN` / `LambdaARN` 字段，但实现为空壳。

### 为什么需要

| 原因 | 影响 |
|------|------|
| **事件驱动架构的基础设施** | 没有细粒度通知，aero-vault 无法成为数据管线的可靠源。数据工程团队需要按前缀/后缀/事件类型触发下游处理。 |
| **S3 兼容性断裂** | S3 的 `PutBucketNotificationConfiguration` 是最广泛使用的 API 之一。用户迁移到 aero-vault 后发现通知不工作，这是一个 blocker。 |
| **当前通知架构是死胡同** | `bucket_notifications` 只存不用，Webhook 却是全局的。这两个概念冲突——用户通过 REST API 设置了通知规则但不产生任何效果。 |

### 建议架构

```
Bucket CRUD → EventBus → NotificationRouter
                              │
                    ┌─────────┼──────────┐
                    │         │          │
               rules[0]  rules[1]  rules[2]
               filter:    filter:   filter:
               .pdf,put   .jpg,*    *,delete
                    │         │          │
               Webhook A   Webhook B  Lambda
                    │                   │
               retry+DLQ          SQS/SNS
```

### 影响范围

| 层 | 变更量级 |
|----|---------|
| Events | 新增 `NotificationRouter`，实现规则匹配 + 多目标投递 + 按规则重试策略；当前 `Webhook` 降级为全局回退 |
| Service | 无需变更（事件已完整发布） |
| S3 | `/s3/{bucket}?notification` 路由需要合规响应 |
| Repository | 现有 `notification_rules` 字段已就位；可能需要 `notification_failures` 表支持按规则追踪 |

---

## 方向三：存储分层与跨后端生命周期转换（Storage Tiering & Lifecycle Transition）

### 现状

- `StorageClass` 字段在对象元数据中完整留存（`STANDARD` / `STANDARD_IA` / `GLACIER`），OTel Metrics 中也有 `storage.class_objects` gauge 按 class 统计对象数。
- `LifecycleJob`（`reconcile/lifecycle.go`）只支持 `ExpireAfterDays`（到期软删或硬删），**不支持从 STANDARD → STANDARD_IA → GLACIER 的逐步降冷**。
- 所有后端（local / S3 / OSS / COS）都不感知 storage class；`Put` 时即使传入 `x-amz-storage-class` 也仅做元数据记录，不会选择不同的存储介质或 tier。

### 为什么需要

1. **成本是不可回避的生产问题**。用户存储文件，90% 在 30 天后不再被访问。没有自动降冷策略，热数据存储成本会线性膨胀。S3 兼容协议的用户预期 `LifecycleConfiguration` 中的 `Transition` 规则应当生效。
2. **现有 storage class 元数据是无效的噪声** — 既然标注了 `GLACIER` 但实际数据仍在本地 NVMe 上，这个字段对账单、容量规划、数据保护策略都没有实际意义，反而会误导运维人员。
3. **多云存储分层是差异化竞争点**：可以做到「热数据 → 本地 / S3 Standard，冷数据 → S3 Glacier / OSS Archive / COS Archive」的透明自动转换。aero-vault 的 `Storage` 接口设计本身就适合这一扩展——因为 `local` / `s3` / `oss` / `cos` 是不同的 `Backend`，一个对象的热副本和冷副本可以存在于不同的后端实现中。
4. **当前 `replication` 的单向 DR 复制与分层有天然重叠**：分层本质上也是一种「重定向复制 + 删除源」。如果先实现好分层框架，部分 replication 逻辑可以复用。

### 架构设想

```
┌─ Service ───────────────────────┐
│ LifecycleTransitionAgent        │ → 扫描过期对象 → 读取 Transition 规则
│   ├─ TierRule{From,To,AfterDays}│ → 在同一个 Storage 接口上操作
│   └─ MoveObject(ctx, obj, cls)  │ → 读取源 → 写入目标 class 对应的后端 → 删除源
└──────┬──────────────────────────┘
       │
┌──────▼──────────────────────────┐
│ Storage MultiTierAdapter        │ 包装多个 Storage 后端，按 class 分发
│   ├─ standards[0..N]           │ read/write/delete 按 Tenant+Class 路由
│   └─ Transition(ctx, key, cls) │ 内部执行数据搬迁
└─────────────────────────────────┘
```

### 影响范围

| 层 | 变更量级 |
|----|---------|
| Storage | 新增 `Transition` 方法或 `MultiTierAdapter` 包装器；每个 `Backend` 需声明支持的 class 列表 |
| Service | `Put` 时按 storage class 路由到正确的后端；`Get`/`Delete` 也需要多后端感知 |
| Reconcile | `LifecycleJob` 扩展为支持 `Transition` 规则 + `Expiration` 规则 |
| Config | 新增 `STORAGE_CLASS_*` 配置项映射 class → 后端实例 |
| Repository | BucketConfig 中 `expire_action` 扩展为 `transition` action；`lifecycle_rules` 表可能需要结构化存储（不再只是简单的 `days+action`） |

---

## 方向四：索引驱动的大规模列表（Index-Based Pagination）与对象枚举性能

### 现状

当前所有列表操作使用 `LIKE key%` + `key > marker ORDER BY key ASC LIMIT N` 的模式（`sql_objects.go`），这是一个典型的**偏移量分页的变种**，其核心问题：

1. **前缀搜索性能随数据量退化**。`LIKE prefix%` + `ORDER BY key` 在 SQLite/Postgres 上需要全表扫描或模糊索引扫描。当单 bucket 对象超过 100 万时，`LIKE` 操作无法有效利用 `(tenant_id, bucket, key)` 组合索引的顺序性——因为 `ORDER BY key` 和 `deleted_at IS NULL` 条件共同限制了索引选择的自由度。
2. **`ListObjectsByTag` 在客户端过滤**。当前实现：全量 `ListObjects` → 遍历 Go 代码过滤 tags。这完全不可扩展——如果你有 100 万个对象，`?tag=Type=Report` 每次都要读出全部 100 万行再在内存中过滤。
3. **`ListDeletedObjects` 同样使用 `LIKE`**。`deleted_at IS NOT NULL` 加上 `LIKE key%` 的联合条件几乎无法命中索引。
4. **`Buckets/{bucket}/versions` 的 `ListBucketVersions` 方法只返回最新版本**。REST handler 中调用 `svc.List()` 而非 `svc.ListVersions()`，真正要查看版本历史的用户得不到完整结果。

### 为什么需要

| 原因 | 影响 |
|------|------|
| **对象存储的核心接口是 List** | 用户（尤其是通过 S3 API 的程序）大量依赖 `ListObjectsV2` 做同步、备份、审计。当 bucket 达到 10M 对象时，`List` 的超时/高延迟是 SLA 中断。 |
| **Tags 过滤是硬需求** | 当前 `ListObjectsByTag` 的实现是 O(N)，在中等规模（10 万对象）下已经不可用。缺少 `(tenant_id, bucket, tag_key, tag_value)` 的 GIN / JSONB 索引是明确的性能缺陷。 |
| **准入栅栏** | 在 AGENTS.md 中工程约束明确要求测试覆盖率 ≥50%、单文件 ≤500 行等，但没有性能约束。上述问题是用户能直接感受到的「慢」。 |

### 建议改进

**近期（低风险，高回报）：**
- `ListObjectsByTag` 改为 SQL 级过滤：Postgres 上用 `metadata @> '{"tag_key":"tag_value"}'` 或 JSONB 索引，SQLite 上用 `json_extract`。
- `ListObjects` 增加 `(tenant_id, bucket, deleted_at, key)` 复合覆盖索引，让 `deleted_at IS NULL + ORDER BY key` 条件走索引。

**远期：**
- 实现指关节分页（keyset pagination）：用 `WHERE key > $marker` 替代 `LIKE + >` — 当前实现已经是 `key > marker`，但前缀过滤仍然使用 `LIKE`。对大前缀 `prefix%`，可改为 `WHERE key >= prefix AND key < prefix_exclusive_end`。
- 对版本化 bucket 的 `ListObjectVersions` 实现真正的翻页版本枚举。
- 引入物化视图或汇总表，缓存 bucket 的对象计数、存储总量、按类分布等统计信息（当前 `BucketStats` 也是实时扫描的）。

---

## 方向五：事件总线背压治理与订阅者可靠性

### 现状

`EventBus` 的 `broadcast` 方法在订阅者 channel 满时**静默丢弃事件**：

```go
select {
case ch <- e:
default:
    // subscriber backpressure: drop, the DB has it
    b.dropped.Add(1)
    telemetry.IncEventDropped(context.Background())
}
```

这个设计假设「DB 中永远有副本」——这个假设在局部成立，但有以下漏洞：

1. **Indexer 丢失事件后只能靠轮询补救**。`Indexer.Run` 中有一个 `pollEvery` 定时器周期执行 `drainBacklog`。但如果丢弃持续发生且 backlog 积压速度超过轮询消费速度，索引会持续落后于写入。对于需要近实时搜索的场景（上传后几秒内可检索），这是不可接受的。
2. **Webhook 投递依赖同一通道**。Webhook 从 `bus.Subscribe()` 获取事件，如果背压丢事件，这些事件永远不会被投递到 webhook URL——即使 DB 中有副本，`Webhook.Run` 也不做 catch-up poll，它只处理来自 bus 的实时流。
3. **没有背压信号向上传播**。EventBus 的 `Publish` 方法不返回投递成功率。调用者（Service 层）不知道事件是否被可靠地广播。这意味着**用户在 PUT 一个对象后，无法得知其后续的索引/通知/复制是否已触发**。

### 为什么需要

| 原因 | 影响 |
|------|------|
| **可靠性幻觉** | 当前架构给用户的印象是事件驱动可靠（有 DB 持久化、有重试），但实际运行时背压导致的静默丢弃会破坏索引完整性和通知送达率。 |
| **难以调试** | `events.dropped` 指标存在但运维人员没有可操作的补救措施——即使发现丢弃率很高，也无法「重播已丢弃事件」到某个特定订阅者。 |
| **索引延迟 SLA 不可控** | 事件驱动管线的核心指标是「从写入到可搜索的时间」。当前的轮询补偿机制缺乏可预测性。 |

### 建议改进方向

1. **引入按订阅者的可回溯队列**：每个订阅者（Indexer / Webhook / Replication / Antivirus）如果 channel 满，不应该丢弃事件，而是将自己的消费位点记录到 `subscriber_cursors` 表中。重新上线后从位点开始 catch-up。
2. **`Publish` 返回成功率摘要**：在不破坏「事件不能阻塞用户请求」原则的前提下，至少可以通过日志/指标暴露有多少订阅者未能及时接收。
3. **Webhook 增加 catch-up 机制**：在 `Webhook.Run` 中增加一个初始的 `drainBacklog` 阶段（类似 Indexer 的做法），从事件表中拉取未投递的事件。当前 Webhook 只做实时流，这对于重启后追赶已错过的事件是致命的。
4. **实现事件重放 API**：`POST /v1/events/replay?after=<timestamp>&type=created` 允许运维人员手动重放特定时间窗口的事件，用于修复索引/通知状态。

### 影响范围

| 层 | 变更量级 |
|----|---------|
| Events | 核心 bus + subscriber cursor 表 + Publish 返回摘要 |
| Indexer | 可能迁移到 subscriber cursor 方式替代轮询 |
| Webhook | 增加 catch-up poll + 可重放 API |
| Repository | 新增 `subscriber_cursors` 表 / `EventsByTimeRange` 查询 |

---

## 总结：5 个方向的优先级排序

| 优先级 | 方向 | 为什么在此位置 |
|--------|------|---------------|
| **P0** | 方向一：合规对象锁 | 法规合规是采购硬门槛。没有它，金融/医疗/政府用户根本不进入 POC。 |
| **P0** | 方向二：事件通知体系 | 当前 `bucket_notifications` 是半成品。用户配置了通知规则但不生效，这是功能欺骗级的问题。 |
| **P1** | 方向三：存储分层/生命周期转换 | 成本优化是用户长期留存的核心驱动力。缺少这个，用户随着数据增长必然迁移到原生 S3 的智能分层。 |
| **P1** | 方向四：大规模列表性能 | 不会在早期阻挡采用，但会在规模增长到 100 万对象时突然爆发，且此时迁移成本最高。建议在达到此规模前提前架设。 |
| **P2** | 方向五：事件总线背压治理 | 当前轮询补偿机制在每日数万对象级别可用，但每日百万级时静默丢弃会变成可靠性和可观测性的主要痛点。建议在事件吞吐量增长前投入。 |

---

## 附：本次扫描中发现的边缘问题（非独立方向，但值得记录）

1. **`ListBucketVersions` REST handler 只返回最新版本**：`handler.go` 中调用 `svc.List()` 而非 `svc.ListVersions()`。这会让用户误以为版本不存在。
2. **BM25 索引全在内存中**：`ai.BM25.BuildFromRepo` 扫描所有 chunk 构建倒排索引。对于百万级 corpus，内存消耗不可控。`pgfts` 后端存在但未被默认激活。
3. **`StorageClassCounts` 只统计 `default` tenant**：`metrics.go` 的 `RegisterStorageClassGauge` 硬编码了 `"default"` tenant，多租户部署下存储类分布仪表盘不可用。
4. **Replication 没有 gzip/压缩选项**：跨区复制直接流式传输原始数据，没有带宽优化。对于大文件跨区域复制，网络成本可能超过存储成本。
5. **没有统一的限流可观测性**：`RateLimiter` 被拒绝请求没有返回 `Retry-After` header 让客户端做智能退避。
6. **`preflightQuota` 在 size=0 时行为不一致**：`checkBytesQuota` 在 `size==0` 时检查 `UsedBytes >= MaxBytes`（仅拒绝「已超」），而在 `size>0` 时检查 `UsedBytes+size > MaxBytes`（预测未来）。前者允许刚好达到阈值后继续写空对象，后者则拒绝——两者语义不一致。
7. **缺少 OTel 日志与 trace 的关联**：当前只有 metrics 被 instrumented，`logging` 和 `tracing` 通道没有被利用。在生产环境中，tracing 是定位慢请求最直接的工具。

---

*本文由代码库全局扫描生成，仅用于指导和讨论，不包含任何代码实现。*
