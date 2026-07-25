# 高价值扩展方向分析 v21 — 从架构完整性看下一波增长点

> **分析范围：** 全代码库一次扫描（cmd/server + internal/* + sdk/* + deploy/*）  
> **分析日期：** 2026-07-10  
> **视角：** 架构师/产品经理 — 关注「缺失了哪些会阻挡实际生产落地」的功能与边界  
> **原则：** 不写代码，只识别方向、论证必要性、评估影响范围

---

## 概述

当前 aero-vault 已经覆盖了对象存储的核心 CRUD、S3 兼容协议、多协议适配（REST/S3/WebDAV/MCP）、AI/RAG 管道、租户/配额/审计、事件/Webhook、SSE 加密、副本复制等大量功能，工程成熟度较高。  

但在逐层扫描后，可以发现几个**对生产化落地构成直接阻碍**的空缺——它们不是「锦上添花」的特性，而是在企业级部署中迟早会触到的硬天花板。以下选取 5 个方向，每个方向都附带「为什么没有它就会出问题」的论证。

---

## 方向一：存储分层与生命周期转换（Storage Tiering & Lifecycle Transition）

### 现状

- `StorageClass` 字段在对象元数据中完整留存（`STANDARD` / `STANDARD_IA` / `GLACIER`），Metrics 中也有 `storage.class_objects` gauge 按 class 统计对象数。
- Lifecycle 策略只支持 `ExpireAfterDays`（到期删除），**不支持从 STANDARD → STANDARD_IA → GLACIER 的逐步降冷**。
- 所有后端（local / S3 / OSS / COS）都不感知 storage class；`Put` 时即使传入 `x-amz-storage-class` 也仅做元数据记录，不会选择不同的存储后端或 tier。

### 为什么需要

1. **成本是不可回避的生产问题**。用户存储文件，90% 在 30 天后不再被访问。没有自动降冷策略，热数据存储成本会线性膨胀，S3 兼容协议的用户预期 `LifecycleConfiguration` 中的 `Transition` 规则应当生效。
2. **现有存储类元数据是无效的噪声** — 既然标注了 `GLACIER` 但实际数据仍在本地 SSD 上，这个字段对账单、容量规划、数据保护策略都没有实际意义，反而会误导运维人员。
3. **多云存储分层**是差异化的竞争点：可以做到「热数据 → 本地 NVMe / S3 Standard，冷数据 → S3 Glacier / OSS Archive / COS Archive」的透明转换，而目前完全没有这个架构。

### 影响范围

| 层 | 变动 |
|---|---|
| `storage.Storage` 接口 | 新增 `SetTier(key, class)` 或生命周期方法的签名 |
| `storage.LocalStorage` | 需要支持按 storage class 分离目录/介质 |
| `internal/reconcile/lifecycle.go` | 从只做「到期删除」扩展为「扫描 → 转换 → 记录新 tier」 |
| `internal/repository/sql_objects.go` | 需要支持按 class + 最后访问时间批量查询 |
| `internal/config` | 新增 tier-to-backend 映射配置 |

### 边界情况

- 转换中的对象正在被读取时应等待完成或提供降级读
- `GLACIER` 对象需要先 `restore` 才能读取（当前 restore 只处理软删除恢复）
- 跨后端转换需要 re-encrypt（如果 SSE key 不同桶或区域级）

---

## 方向二：搁置分片上传垃圾回收（Orphaned Multipart Upload GC）

### 现状

- `InitMultipart` 在存储层和 repo 层都创建记录。
- 如果客户端在 `CompleteMultipart` 或 `AbortMultipart` 之前崩溃/断开，**上传记录和部分数据会永久滞留**。
- 当前没有任何后台机制来扫描和清理搁置的分片上传（超出 TTL 的 upload 记录 + 存储层的部分 blob）。
- S3 协议中，`ListMultipartUploads` 返回的结果会包含这些僵尸上传——客户端长期看到自己的「死」上传，体验很差。

### 为什么需要

1. **资源泄漏是确定性的**。每次客户端断连，磁盘/对象存储上就留下一个永远不删除的分片。在 CI/CD 频繁上传大文件的场景下，周级泄漏量可达 GB 级。
2. **S3 兼容的硬要求** — AWS S3 会自动清理超过 7 天未 complete 的上传。用户迁移工具（aws s3 cp / rclone）依赖这个行为来避免泄漏。
3. **已经在 Reconcile/Retention 框架内**，只需要新增一个扫描器，复用现有的集群单例 + ticker 机制，工程成本低但收益明确。

### 影响范围

| 层 | 变动 |
|---|---|
| `internal/reconcile/` | 新增 `uploadGC` 子模块，复用 `interval` + `cluster singletion` 模式 |
| `internal/repository` | 新增 `ListExpiredUploads(before time.Time)` + `DeleteUploadCascade(id)` |
| `internal/storage` | 新增 `CleanupParts(uploadID)` 接口方法（S3 后端已有；local 需要实现） |
| `internal/config` | 新增 `UPLOAD_GC_TTL_HOURS` / `UPLOAD_GC_INTERVAL` |

### 边界情况

- 一个 upload 的最后一次 part 刚上传完成但尚未 complete——不能简单按「超过 N 天无活动」来判定，需要结合 `part_updated_at` 或 `created_at`。
- 跨存储后端的 upload 记录不匹配（存储层已 abort 但 repo 还在）— 需要 reconcile 模式双向校验。
- S3 分片上传的 `uploadId` 在存储层（S3 / OSS）和 repo 层是两套 ID 体系，需要映射清理。

---

## 方向三：访问日志投递与通知调度引擎（Access Log Delivery & Notification Dispatch）

### 现状

- Bucket 级别的 logging 配置已完整（`BucketConfig.LoggingTarget` / `LoggingPrefix`），repo 接口也有 `WriteAccessLog(ctx, tenant, sourceBucket, method, key, status, latencyMs, userAgent)` 方法签名。
- Notification 规则已支持 CRUD（`SetBucketNotifications` / `GetBucketNotifications`），数据模型 `NotificationRule` 可配置 `QueueARN` / `TopicARN` / `LambdaARN` + `Events` 过滤器。
- **但两者都没有实际的投递/调度引擎**：
  - `WriteAccessLog` 从未被任何 middleware 或 handler 调用。
  - `SetBucketNotifications` 存储了规则后，没有任何消费者匹配事件并投递到目标。
- 事件总线（`events.Bus`）在对象变更时 `Publish`，但 notification 引擎并未接入到该总线上。

### 为什么需要

1. **合规性要求**：SOC2 / HIPAA / 金融监管普遍要求保留对象级访问日志。当前虽然能在审计日志（`audit_log` 表）中看到部分 admin 操作，但普通读写操作完全没有记录。这是合规审计的红线缺失。
2. **事件驱动架构的断点**：Notification 规则是 S3 兼容协议的重要差异化功能——用户在 S3 上设置 `s3:ObjectCreated:*` 通知到 SQS 是常见的 pipeline 模式。当前只能通过全局 Webhook 来做，做不到 per-bucket 细粒度路由。
3. **logging + notification 共享同一数据管道**：两者都可以基于事件总线的事件来驱动。新增一个 `notification.Worker` 订阅 bus，匹配规则后投递，同时 `accesslog.Worker` 写入 logging 目标桶。工程复用度高。

### 影响范围

| 层 | 变动 |
|---|---|
| `internal/events/bus.go` | 已有事件类型（`created` / `deleted` / `accessed`），notification 可以消费现成事件 |
| `internal/api/rest/handler.go` + `internal/api/s3compat/handler.go` | 所有 GET/HEAD/PUT/DELETE 入口需要调用 `WriteAccessLog`（或通过 middleware 自动记录） |
| `internal/notifications/` | 新建包：订阅事件 → 匹配 `NotificationRule` → 按 `QueueARN` 投递（HTTP/SQS proxy） |
| `internal/accesslog/` | 新建包：从事件流或 middleware 层接收记录 → 写入 logging 目标桶 |
| `internal/repository/sql_objects.go` | `WriteAccessLog` 需要实际写入 `access_logs` 表或直接存储到目标桶 |
| Migration | 新增 `access_logs` 表或设计日志对象命名方案 |

### 边界情况

- **自身递归**：写 access log 到目标桶时又会触发 `EventCreated` → 通知引擎必须能识别并跳过 logging 桶自身的写入事件（通过 `sourceBucket != targetBucket` 或事件来源标记）。
- **日志对象膨胀**：高吞吐场景下每秒可能产生数千条日志行。需要设计缓冲 + 批量写 + 自动轮转（按小时/天分桶）。
- **通知投递失败**：需要类似 webhook 的重试与死信机制，复用现有的 `webhook_failures` 模式。

---

## 方向四：多活跨区复制与故障切换（Active-Active Replication & Failover）

### 现状

- 已有的 `replication.Worker` 实现了**单向**异步复制：primary → replica，事件驱动通过 job 队列。
- 复制是**尽力而为**的：如果 replica 存储不可用，job 重试若干次后标记失败，没有降级读或自动 failover。
- 复制成功后只在 tags 中标记 `repl_status=replicated`，**没有一致性校验**（对象内容、大小、etag 是否匹配）。
- **没有反向同步或双向同步机制**，因此不能实现 active-active 多活。
- 读请求始终指向 primary，即使 primary 不可用也不会切到 replica。

### 为什么需要

1. **高可用架构的基石**。当前设计存在单点故障：primary 存储一旦不可用（S3 region 故障、OSS 服务中断），整个系统读不可用，写更不可用。生产部署必须支持 `primary → replica` 故障切换（至少 manual/promote）。
2. **数据耐久性需要可验证**。当前 replication 写完后只打了个 tag，没有校验 replica 上的对象是否完整。对于法规要求的数据冗余验证（如 SEC Rule 17a-4），这是不可接受的。
3. **异地容灾部署的必选项**。如果 primary 在中国（OSS），replica 在美西（S3），用户需要一个可观测的健康状态和明确的切换流程。
4. **当前基础设施已预备**：`replication` 包已存在、`ReplicationCfg` 已定义、job 队列已就绪。缺失的只是故障检测、切换仲裁和一键 promote 流程。

### 影响范围

| 层 | 变动 |
|---|---|
| `internal/replication/replication.go` | 新增一致性校验（对比 etag + size）；新增反向同步路径 |
| `internal/cluster/` | 新增集群健康检测器（ping primary/replica）；新增 Leader 选举状态机（谁做 active） |
| `internal/api/rest/admin.go` / `router.go` | 新增 `POST /admin/replication/promote`、`GET /admin/replication/status` |
| `internal/config` | 新增 `REPLICATION_MODE`（async-sync / active-active）、`REPLICATION_HEALTH_CHECK_INTERVAL` |
| `internal/service/file_crud.go` | 写入路径可能需要 multi-ack（等待 N 个副本确认） |
| Migration | 新增 `replication_status` 表跟踪复制进度 |

### 边界情况

- **裂脑（split-brain）**：primary 和 replica 之间的网络分区导致两个 region 都开始接受写入。需要 fencing 机制（lease + 第三方仲裁）。
- **复制延迟下的读取一致性**：用户刚写入 primary 就从 replica 读，可能读到旧数据。需要 `Read-After-Write` 一致性标记或「读 primary unless degraded」策略。
- **SSE 密钥不在 replica 上**：如果使用本地 SSE key 加密，replica 必须能获取到相同的 key ring 才能解密数据。

---

## 方向五：对象版本生命周期管理与合规保留（Non-Current Version Expiry & Compliance Hold）

### 现状

- 版本控制（versioning）已完整实现：`InsertObjectVersion` + `GetObjectVersion` + `ListObjectVersions`。
- 对象锁（WORM）已实现：`LockedUntil` 字段 + `SetLockedUntil` + 覆盖/删除时的锁检查。
- **Legal Hold** 通过元数据 `_aero_legal_hold: ON` 手工跟踪，不是一等公民：
  - 无法批量查询处于 legal hold 的对象。
  - 没有 API 来 put/get legal hold（只有通过 S3 `x-amz-object-lock-legal-hold` header 间接支持）。
- **没有「非当前版本过期」策略**：S3 生命周期有 `NoncurrentVersionExpiration` 和 `NoncurrentVersionTransition`，但当前只有针对当前版本的 `ExpireAfterDays`。
- 软删除保留（`RetentionDays`）只能基于 `deleted_at` 清除软删除行——但 versioning 开启时，旧版本是用 `deleted_at` 标记的，会被 Retention GC 错误清除。

### 为什么需要

1. **合规保留是 S3 兼容的必选项**。医疗（HIPAA）、金融（SEC 17a-4）、司法（eDiscovery）场景都需要 legal hold + 不可擦除的版本链。当前 legal hold 只是一个 metadata tag——任何有写权限的人都可以删除这个 tag 然后删除对象，等于没有 legal hold。
2. **版本爆炸是真实的成本问题**。如果每次写入都产生一个新版本（versioning enabled），而没有任何机制来自动清除 N 天前的旧版本，存储成本会随时间线性增长。用户会在 3 个月后发现存储费用翻了三倍。
3. **当前 Retention GC 与 Versioning 冲突**：`purgeSoftDeleted` 依据 `deleted_at IS NOT NULL` 来清除——但在 versioning 模式下，旧版本就是用 `deleted_at` 标记的。这意味着开启 versioning 的 bucket，旧版本可能会被 Retention GC 错误地物理删除。这是一个数据安全 bug。

### 影响范围

| 层 | 变动 |
|---|---|
| `internal/repository/sql_objects.go` | 新增表 `legal_holds`（object_id + tenant + hold_reason + created_at）；`HardDeleteObject` 必须检查 legal hold（不只看 metadata tag） |
| `internal/repository/sql_buckets.go` | `BucketConfig` 新增 `NoncurrentDays` / `NoncurrentCount` 字段 |
| `internal/reconcile/lifecycle.go` | 新增非当前版本扫描 → 过期删除逻辑；保留旧版本不受 versioning bucket 影响 |
| `internal/service/file_features.go` | 新增 `PutLegalHold` / `GetLegalHold` 方法 |
| `internal/api/rest/router.go` | 新增 `/v1/files/*/legal-hold` 端点 |
| `internal/api/s3compat/handler.go` | 完善 `x-amz-object-lock-legal-hold` 的 get/put 支持（当前只处理 put header） |
| `internal/reconcile/retention.go` | 修复：在 versioning bucket 中，不清除标记为旧版本的行（`deleted_at IS NOT NULL AND version_id IS NOT NULL` 不是可清除的） |

### 边界情况

- **Legal hold 覆盖删除**：如果对象有 legal hold，即使 WORM 到期也不应允许删除。当前锁检查只检查 `LockedUntil`，不检查 legal hold。
- **版本链中的 legal hold 独立性**：单个对象的某个版本有 legal hold，其他版本不应受影响。
- **批量 legal hold 操作**：eDiscovery 场景需要对大量对象同时加 hold，当前只能逐个 PUT。
- **Lifecycle 与 legal hold 的交互**：有 legal hold 的版本即使已过期也不应被 lifecycle 删除。

---

## 补充：全局边界情况扫描

在扫描过程中发现了一些散布在各层的边界疏忽，虽然不是独立的扩展方向，但对生产稳定性有影响：

| 问题 | 位置 | 影响 |
|------|------|------|
| **S3 accelerate 返回 stub** | `s3compat/handler.go:getBucketAccelerate` | SDK 调用 `GetBucketAccelerate` 会拿到 `Suspended`，可能触发客户端 fallback 逻辑异常 |
| **ListByTag 客户端过滤** | `sql_objects.go:ListObjectsByTag` | 标签过滤在 Go 层进行，不是 SQL 级过滤。大桶场景性能差（先拉到 1000 条再过滤） |
| **Webhook 目标无速率限制** | `events/webhook.go` | 如果 webhook URL 响应慢，`RetryLoop` 可能批量重试压垮下游 |
| **AI Agent 无会话上下文** | `ai/agent.go` | Agent 每次调用都是全新对话，无法跨步骤保持上下文（`messages` 会在 step budget 耗尽后丢弃） |
| **StorageClass 默认值冲突** | 多处 `service.DefaultStorageClass` vs `STANDARD` | 个别地方硬编码 `"STANDARD"`，与 `WithDefaultStorageClass` 的覆盖不统一 |
| **Presign URL 无方法约束** | `rest/handler.go:Presign` | `POST /presign?op=get` 生成下载 URL，但无法限制只能 GET（URL 可以用于 HEAD/PUT 等） |
| **`StorageClassGauge` 只采样 `default` 租户** | `main.go:registerGauges` | 多租户场景下 storage class 统计只采样 default 租户，其他租户不可见 |

---

## 总结：优先级建议

| 优先级 | 方向 | 建议时机 | 工程预估 |
|--------|------|---------|---------|
| **P0** | 版本生命周期 + 合规保留修复 | 下一轮 Sprint | 2-3 周 |
| **P0** | 搁置分片上传 GC | 下一轮 Sprint | 1 周 |
| **P1** | 访问日志 + 通知调度 | 下下轮 Sprint | 3-4 周 |
| **P1** | 存储分层转换 | 下下轮 Sprint | 3-4 周（简单 tier）~ 6-8 周（跨后端 tier） |
| **P2** | 多活跨区复制 | 里程碑型项目 | 6-8 周 |

**P0 的判定标准**：如果不修复，会在用户达到一定规模后（百 GB 级别存储 / 数十并发客户端）造成数据丢失或合规违规。这些是先于新功能必须加固的防线。
