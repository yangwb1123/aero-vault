# 高价值扩展方向分析

> **扫描范围：** 全局代码库（237 个 Go 源文件，52 个 SQL 迁移文件，SDK 三件套，Web UI，CLI）
> **扫描日期：** 2026-07-10
> **视角：** 资深架构师 / 产品经理
> **特点：** 基于实际代码扫描发现的、现有 57 份分析文档中未充分覆盖的盲区

---

## 总览

aero-vault 已经是一个功能极其丰富的系统：四协议接入、多后端存储、多租户、
完整的 RAG 管线、事件驱动架构、可观测性体系。以下五个方向不是"锦上添花"
—— 它们是实际投入生产后必然会撞上的天花板。

---

## 方向一：对象追加写入与流式上传语义（Append & Streaming Write）

### 现状

当前所有写操作都是**全量替换**（PUT / 分片上传最终合并为一个完整对象）。
FileService 中没有任何 `Append`、`Truncate` 或流式写入接口。

```go
// file_crud.go — 唯一的写入入口
func (s *FileService) Put(ctx context.Context, tenant, bucket, key string,
    r io.Reader, size int64, opts PutOptions) (repository.Object, error)
```

- Storage 接口同样只定义了 `Put`、没有 `Append`
- 不存在部分写 / 范围写的概念
- 分片上传（Multipart）是上传完成前对客户端的分段，不是服务端意义上的追加

### 为什么需要

| 场景 | 现状方案 | 痛点 |
|------|---------|------|
| 日志采集（实时写追加） | 每个日志周期创建一个新文件 | 文件爆炸、查询需跨大量小文件 |
| 传感器 / IoT 时序数据 | 全量替换整个对象 | 写入带宽浪费 100 倍以上 |
| 实况转录 / 字幕生成 | 逐段 PUT 不同 key | 下游消费者无法流式读取 |
| 数据库 WAL 归档 | 频繁小 PUT | 产生海量版本、存储利用率极低 |
| 大文件尾部增量更新（CSV 追加行） | 必须全量重新上传 | 大文件场景完全不可用 |

### 建议方案

1. **Storage 层新增 `Append(key, r io.Reader) (ObjectInfo, error)`** — 支持后端本地文件追加写入、S3 多分片追加或预先分配的文件追加
2. **FileService 暴露 `AppendObject(ctx, tenant, bucket, key, r, size)`** — 实现原子追加语义、正确更新 object size 和 etag
3. **S3 Compat 支持 `x-amz-append:` 扩展头或兼容 S3 Object Lambda 模式**
4. **索引器增量处理** — Append 后仅重新索引追加部分，而非全量重新索引

### 边界条件

- 追加对象上不能启用服务端加密（改写加密边界）
- 追加与版本控制的交互：每次 Append 是否生成新版本？
- 追加对象上是否允许对象锁 / WORM？
- 大对象（>5GB）不能追加前已存在的限制
- 并发追加的可见性语义（read-your-writes）

---

## 方向二：服务端拷贝、原子重命名与跨桶操作（CopyObject / Rename / Move）

### 现状

S3 compat 实现了 `copyObject`，但本质是**客户端读写**：

```go
// internal/api/s3compat/extra.go
func (h *Handler) copyObject(w http.ResponseWriter, r *http.Request, ...) {
    rc, obj, err := h.svc.Get(ctx, ...)   // 完整读出
    _, err = h.svc.Put(ctx, ..., rc, obj.Size, ...)  // 完整写入
}
```

- 没有服务端直接拷贝（storage backend 内部复制）
- 没有原子重命名 / 跨桶移动
- 没有批量拷贝语义
- CLI 也没有 `mv` / `cp` 命令

### 为什么需要

| 场景 | 现状方案 | 痛点 |
|------|---------|------|
| 用户整理文件（重命名 / 移动） | GET + PUT + DELETE 三步骤 | 非原子、耗时、可能丢数据 |
| S3 兼容性：CopyObject 是 AWS SDK 常用操作 | 勉强可工作 | 大对象超时、无 server-side copy、ETag 不一致 |
| 数据分层 / 归档（转储到冷存储桶） | 无内置方案 | 需要外部编排 |
| ETL 管线中转数据 | 无原子 Move | 两步操作间有窗口期 |

### 建议方案

1. **Storage 接口新增 `Copy(ctx, srcKey, dstKey) error`** — 后端原生拷贝（local FS 是 cp，S3 是 CopyObject API，OSS/COS 同类）
2. **FileService 新增 `CopyObject` 和 `MoveObject`** — Move = Copy + Delete，原子性由 repository 事务保证
3. **REST API 新增 `POST /v1/copy` 和 `POST /v1/move`**
4. **S3 compat 的 copyObject 改为调用服务端 Copy，而不是 GET+PUT**
5. **CLI 新增 `aero-vault cp` 和 `aero-vault mv`**
6. **副本间跨区域 Copy** — 结合 replication worker 实现跨存储后端拷贝

### 边界条件

- 跨桶 Move 时权限检查（源桶可读、目标桶可写）
- 版本控制桶中 Move 是否保留版本历史
- 对象锁下的 Copy/Move 语义
- 大对象（>5GB）的 Copy 需要 multipart copy
- Copy 时 metadata 的处理（保留还是替换）

---

## 方向三：事件通知分发引擎（Event Notification Dispatch）

### 现状

代码里已经有了**完整的通知规则配置管线**：

- 数据库 schema：`buckets` 表有 `notification_rules` 列（JSON，存储 SQS ARN / Topic ARN / Lambda ARN）
- Repository 接口：`GetBucketNotifications` / `SetBucketNotifications` / `DeleteBucketNotifications`
- FileService：透传 repository 方法
- REST & S3 API：端到端可读写 `GET/PUT/DELETE /v1/buckets/{bucket}/notification` 和 `?notification`

然而——**没有任何代码实际读取这些规则并分发事件**。通知规则的真正使命——当对象创建/删除时自动推送消息到 SQS / SNS / Lambda——是完全缺失的。

另外，现有 EventBus 存在以下限制：

- **无死信队列**：超出重试上限的 webhook 仅在表中标记为 `dead-lettered`，没有进一步投递到 DLQ
- **无事件过滤**：订阅者只能收到所有事件，不能按 bucket/prefix/suffix/tag 过滤
- **无时间旅行**：不能从历史某个时间点重放事件

### 为什么需要

| 场景 | 现状 | 痛点 |
|------|------|------|
| 对象创建后自动触发数据处理（DMS / ETL） | 无 | 必须外部轮询或额外编写 Webhook |
| 与 AWS S3 Event Notifications 兼容 | Schema 兼容 | 实际不工作，迁移用户会质疑 |
| 跨集群事件同步 | 仅有 Postgres LISTEN/NOTIFY | 仅限于 PG 集群内 |
| 失败事件的审计与手动重试 | Dead-letter 仅存于 `webhook_failures` 表 | 无法路由到 SQS DLQ 等标准 DLQ |
| 细粒度事件订阅：只接收特定 prefix 的对象创建事件 | 全量广播 | 下游系统需自行过滤 |

### 建议方案

1. **通知引擎注册到 EventBus** — `bus.Subscribe()` 后检查每条事件的 bucket notification rules，匹配后调用 SQS / SNS / Lambda（通过 HTTP/SDK）
2. **SQS 适配器** — 使用 AWS SDK 或兼容 endpoint（如 SQS-compatible message queue）发送消息
3. **SNS / Lambda 适配器** — HTTP 调用或标准 AWS SDK
4. **前缀/后缀/标签过滤** — 在 `notificationConfiguration.FilterKey` 基础上实现实际过滤逻辑
5. **死信队列支持** — `webhook_failures` 表扩展 `dlq_*` 字段，超重试上限后投递到配置的 DLQ
6. **事件重放 API** — `POST /v1/events/replay?from=2026-01-01&filter=prefix:logs/`
7. **可观测性增强** — `event_notifications_sent_total{target, status}` / `event_dlq_routed_total`

### 边界条件

- SQS/SNS/Lambda 跨云厂商兼容性（AWS / 阿里云 MNS / 腾讯 CMQ）
- 过滤规则变更后已排队事件的处理
- 通知目标不可达时的退避策略与幂等性
- 大规模事件场景（每秒数千次对象变更）下的通知吞吐

---

## 方向四：多区域元数据复制与全局命名空间

### 现状

当前有**存储层 blob 复制**（`internal/replication`）：

```go
// replication.NewWorker — 复制 blob 到另一个 Storage backend
// 触发条件：object.created / object.deleted 事件
```

但**元数据没有跨区域复制**：

- 每个 aero-vault 实例有自己独立的 DB（SQLite / Postgres）
- 不存在跨区域/跨集群的一致全局名字空间
- 不存在多活（active-active）架构
- 不存在区域级故障切换（failover）
- 不存在全局 bucket / 对象 ID 的唯一性保证
- 复制 worker 不支持方向选择（单向 / 双向）、冲突解决策略（last-writer-wins / version vector）

### 为什么需要

| 场景 | 现状 | 痛点 |
|------|------|------|
| 全球分布式团队访问同一个 bucket | 各自独立的区域实例 | 数据不一致，无法跨区域共享 |
| 主区域故障时无缝切换 | 单点部署 | 停机时间 = 灾难 |
| 数据就近访问降低延迟 | 单区域 | 跨洋延迟不可接受 |
| 合规：数据必须留在特定司法管辖区 | 无 | 无法实现数据驻留 |
| SaaS 服务提供全球多区域 | 无多区域架构 | 无法扩展到全球 |

### 建议方案

1. **元数据变更日志（Change Data Capture）** — 每个对象的 CRUD 操作写入 `metadata_change_log` 表，作为跨区域复制的源
2. **全局协调服务（可选）** — 轻量级的全局 ID 分配 / 冲突检测服务（或使用 CRDT）
3. **复制通道** — 基于 Postgres 逻辑复制 / Kafka / 内置 gRPC 流
4. **租户级数据驻留策略** — 每个 tenant 可配置 primary region + allowed replica regions
5. **读本地写全局（Read Local, Write Global）** — 写入转发到 primary region，读取从本地 replica 服务
6. **全局 Bucket 名称唯一性** — 跨区域名称注册 / 分配服务

### 边界条件

- 最终一致性 vs 强一致性：读写分离下的 Read-After-Write 一致性保证
- 区域间网络延迟对写入延迟的影响
- 冲突解决方案：last-writer-wins（默认）、version vector、应用层自定义 CRDT
- 区域故障切换触发条件（自动 vs 手动）和回切（failback）
- 复制带宽成本：跨区域数据传输费用
- 元数据复制与存储 blob 复制的协调（确保两者原子性）

---

## 方向五：智能生命周期管理与存储分层（Intelligent Tiering）

### 现状

当前生命周期规则（`internal/reconcile/lifecycle.go` + `repository.SetBucketLifecycle`）极其有限：

- 仅两个动作：`soft_delete` / `hard_delete`
- 仅一个条件：`ExpireAfterDays`（基于 `updated_at`）
- 无存储分层：所有对象统一 `STANDARD`
- 无非当前版本过期策略
- 无未完成分片上传自动中止
- 无对象大小过滤条件
- 无标签级生命周期规则

相关代码规模：

```go
// internal/reconcile/lifecycle.go — 约 150 行，核心逻辑少于 50 行
func (j *Lifecycle) applyLifecycle(ctx, bucket, cfg) // 直接删，没有分层
```

此外，`StorageClass` 只是一个字符串字段（`repository.Object.StorageClass`），
没有任何逻辑驱动对象在不同 class 间迁移。S3 兼容端点虽然能读/写 `x-amz-storage-class`，
但该字段仅在 SQL 中存储，不产生任何实际行为。

### 为什么需要

| 场景 | 现状 | 痛点 |
|------|------|------|
| 冷数据自动降级（30 天后 → STANDARD_IA，90 天后 → GLACIER） | 无 | 存储成本线性增长，无竞争优势 |
| 保留 N 个非当前版本后自动清理 | 无 | 版本控制启用后无限膨胀 |
| 30 天未完成的分片上传自动中止 | 无 | 泄漏存储，需要手动清理 |
| 按对象标签分类处理（`retention=forever` 不被删除） | 无 | 所有对象一视同仁 |
| AWS S3 Lifecycle 策略兼容 | 仅兼容了 `Expiration`（不完整）| 迁移用户无法直接复用现有策略 |

### 建议方案

1. **存储分层引擎** — 新增 `LifecycleTransition` 规则（Days, StorageClass），`reconcile` 模块在分层窗口到达时执行 `storage.Copy` 到目标后端并更新 `StorageClass`
2. **支持的 Action 扩展** — `transition_to_ia` / `transition_to_glacier` / `transition_to_deep_archive` / `expire`（已有 `soft_delete` / `hard_delete`）
3. **非当前版本管理** — `NoncurrentVersionExpiration`（保留 N 个新版本后删除旧版本）、`NoncurrentVersionTransition`
4. **分片上传中止规则** — `AbortIncompleteMultipartUpload`（超过 N 天后自动 abort）
5. **标签 / 大小过滤** — 生命周期规则可附加 `Filter: {Prefix, Tag, ObjectSizeGreaterThan/LessThan}`
6. **StorageClass 实际作用于读取** — `Get` 时根据 Class 选择正确 backend 读取（尤其是 GLACIER 需要先 restore）
7. **CLI + SDK 支持** — 查看/设置存储类、发起 restore 操作
8. **可观测性** — `lifecycle_transitions_total{from_class, to_class}`、`lifecycle_cost_savings_usd`

### 边界条件

- GLACIER 对象的最小存储周期（90 天）及提前删除罚款计算
- GLACIER 对象的取回（restore）时间窗口与费用
- 分层与版本控制的交互：当前版本 vs 非当前版本的分层策略不同
- 分层与对象锁 / WORM 的交互：锁定中的对象不能分层
- 分层引擎的频繁读写对后端存储的 API 费用影响
- 跨后端分层（如 local → S3）的带宽成本和延迟

---

## 附录：现有代码库其他值得关注的边界缺口

### A. 日志写入 stub
`internal/repository/sql_buckets.go` 中 `WriteAccessLog` 是目前系统里最清晰的 stub：

```go
func (s *sqlStore) WriteAccessLog(ctx context.Context, tenant, sourceBucket, method, key, status, latencyMs, userAgent string) error {
    _ = tenant; _ = sourceBucket; _ = method; _ = key; _ = status; _ = latencyMs; _ = userAgent
    return nil  // ← 所有参数被丢弃，什么都不做
}
```

可以配置存储、Schema 完整，但写日志的逻辑没有实现。S3 兼容性中的 Server Access Logging 是一个合规要求。

### B. 冷启动优化
AI 管线在启动时做大量 heavy lifting：BM25 全量构建（`setupBM25Search` 中 `BuildFromRepo`）、
ReindexStaleOnStart、pgvector/Qdrant 集合创建。大租户场景下启动时间可能达到数分钟。
没有延迟初始化、渐进式构建或预热机制。

### C. 存储层对象大小硬限制检查
`service.Put` 中没有对单对象大小做上限检查（S3 标准限制为 5TB）。
`validateMetadata` 有元数据大小限制，但对象体本身无检查。大对象写入可能填满磁盘或使
`store.Put` 超时。

### D. 权限模型缺少子资源粒度
Bucket policy（`auth/policy.go`）支持 S3 action（`s3:PutObject`, `s3:GetObject`），
但没有区分子资源操作（`s3:PutObjectTagging` vs `s3:PutObjectAcl` vs
`s3:PutObjectVersioning`）。所有写操作都被映射到 `s3:PutObject`，无法实现细粒度 IAM。
