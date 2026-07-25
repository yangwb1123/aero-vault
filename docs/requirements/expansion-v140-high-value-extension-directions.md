# 高价值扩展方向：存储分层、元数据查询、事件工作流、读缓存、PITR

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — `cmd/server/main.go` + `internal/` 全部 30+ 子包、三套 SDK、MCP 双模式、Web UI、50 对迁移文件、`deploy/` 全套配置、`HARNESS.md`、`AGENTS.md`  
> **去重验证：** 对 `docs/requirements/` 下全部既有分析文档逐方向进行关键词 + 语义交叉验证  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。选取**代码中存在明确锚点、有实质性产品/架构影响、且在既有分析中未被深度独立覆盖**的方向。每个方向包含：现状与代码证据 → 产品价值 → 架构权衡 → 边界情况。

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 | 既有分析覆盖 |
|---|------|------|--------|---------|---------|-------------|
| **1** | **存储生命周期分层引擎 (Storage Lifecycle Transitions)** | 成本优化/架构扩展 | **P1** | 对象自创建后永久留存于 `STANDARD` 存储类，无自动降级/归档/删除策略，启用版本化后历史版本无限堆积，存储成本随数据增长线性膨胀 | `internal/reconcile/lifecycle.go`（仅 `ExpireAfterDays` + `soft_delete`/`hard_delete`，零 `Transition`）；`internal/repository/migrations/0021_storage_class.up.sql`（`storage_class` 列存在但写入后永不改变）；`internal/api/s3compat/bucketconfig.go`（解析 lifecycle XML 时丢弃 `Transition`、`NoncurrentVersionTransition`、`NoncurrentVersionExpiration`、`AbortIncompleteMultipartUpload`） | v80–v90 共计 11 份文档以表格/pass 形式列出 "StorageClass transition 缺失" 或 "生命周期深度不够" 但均为 1–3 行提及，**无一篇提供基于 reconcile/lifecycle.go 引擎的完整实施路径分析** |
| **2** | **对象元数据与标签查询引擎 (Object Metadata & Tag Query)** | 产品特性/数据可发现性 | **P1** | 对象存储了 `tags`（`map[string]string`）和 `metadata`（`map[string]string`），但**没有任何查询接口能按标签或元数据筛选对象**。用户只能通过前缀列举批量扫描，在大规模存储中效率极低。S3 的 `ListObjects` 不支持按 tag 过滤，但 AWS 提供了 SELECT-like 的 `s3:ObjectTagging` + `s3:GetObjectTagging` 组合；AeroVault 当前在 AI 语义搜索之外缺少结构化元数据查询能力 | `internal/repository/sql_objects.go`（`ListObjects` 仅 `WHERE bucket=? AND key LIKE ?`，无 `tags` 或 `metadata` 过滤）；`internal/repository/repository.go`（`Object` 结构体有 `Tags map[string]string`、`Metadata map[string]string` 字段）；`internal/repository/sql_tags_acl.go`（`Tags` JSON 列存储但无查询索引）；`internal/api/rest/handler.go:List`（`GET /v1/files` 仅接受 `prefix`/`delimiter`/`limit`/`marker` 参数）；`internal/api/rest/dto.go`（`objectDTO` 序列化 tags/metadata 但检索路径不用） | **全部既有分析文档均未作为独立深度方向覆盖「元数据/标签查询引擎」** |
| **3** | **事件驱动的可配置工作流触发器 (Event-Driven Workflow Destinations)** | 自动化/集成 | **P2** | 事件总线已实现持久化 + 广播，但订阅者**硬编码**为 Webhook（单一 URL）、Antivirus Worker、Replication Worker、Indexer。用户无法自定义触发规则（前缀/后缀/事件类型过滤 + 自定义目标）。AWS S3 Event Notifications 支持 SQS/SNS/Lambda 等多目标路由，这是对象存储与外部系统集成的核心能力 | `internal/events/bus.go`（广播模式——所有订阅者接收全部事件）；`internal/events/webhook.go`（单一 URL，全部事件推送到同一 URL）；`internal/repository/migrations/0024_bucket_notifications.up.sql`（`notification_rules TEXT` 列已存在但仅有 schema 层，`internal/api/rest/handler.go` 虽有 `GetBucketNotifications`/`PutBucketNotifications`/`DeleteBucketNotifications` 端点但**仅持久化规则字符串，无任何规则解析和执行引擎**）；`internal/api/s3compat/handler.go`（同样有 notification 端点但只存取 JSON）；`internal/repository/repository.go`（`BucketConfig.Notifications` 字段存在但无消费者） | v139 对 S3 Event Notifications 的 SQS/SNS/Lambda ARN 目标做了深度分析（目标兼容性路线），**但未覆盖「用户可配置的自定义工作流触发器（HTTP endpoint + 前缀/后缀过滤 + 规则级限流）」的产品化方案** |
| **4** | **分布式读路径扩展：内容缓存层 + 只读副本路由 (Read Scalability: Content Cache & Replica Routing)** | 性能/架构扩展 | **P2** | 当前所有 GET/HEAD 请求直连 `Storage` 后端。存储后端无读缓存、无数据局部性优化、无读副本路由。单后端写扩展（local/S3）成为读吞吐瓶颈。对于频繁访问的热对象，每次请求都走完整的存储后端 I/O，延迟和吞吐均受限 | `internal/storage/storage.go`（`Get` 方法直接返回底层存储的 `io.ReadCloser`）；`internal/storage/local_read.go`（每次 `Get` 执行完整文件系统 `os.Open` + 全量读取）；`internal/middleware/ratelimit.go`（已有请求级别限流但无内容缓存）；`internal/service/file_crud.go:Put`（无写路径缓存预热逻辑）；`internal/service/file.go:FileService`（无缓存层 wrapper）；`internal/storage/factory.go`（无 wrap-with-cache 逻辑） | v84/v86 以 1–2 句提到 "CDN" 或 "edge cache" 概念但**无代码锚点驱动的深度分析** |
| **5** | **分布式部署的一致快照与时间点恢复 (Point-in-Time Recovery for Distributed Deployments)** | 运维/韧性 | **P2** | 当前 `internal/snapshot/snapshot.go` 仅支持 **SQLite + local FS** 场景的 tar.gz 快照。对于生产级 Postgres + S3 部署，没有任何一致的快照/恢复机制。元数据（数据库）和内容（存储后端）的组合快照需要跨系统一致性——这是 AWS S3 + RDS 场景的基本运维需求 | `internal/snapshot/snapshot.go:23`（`Create` 函数注释明确说明 "only sqlite local snapshots are supported"）；`internal/snapshot/snapshot.go:97`（`Restore` 同样只支持 SQLite DSN）；`internal/snapshot/snapshot.go:186`（`dbFileFromDSN` 函数只解析 SQLite `file:` URI，**对 Postgres DSN 直接返回空字符串**）；`internal/repository/sql.go`（Repository 接口的 `Ping` 方法可用于快照健康检查但无快照能力）；`cmd/server/main.go:initInfrastructure`（存储和数据库初始化顺序固定，无快照恢复替换启动路径）；`deploy/helm/aero-vault/templates/`（Helm chart 部署无快照 sidecar 或 init container） | v9 方向四以 1 页分析 "简化版快照"（聚焦当时刚实现的 SQLite 快照功能本身），**后续既有分析从未深入覆盖「分布式部署场景下跨存储+数据库的一致快照」** |

---

## 方向一：存储生命周期分层引擎

### 现状

当前生命周期引擎仅处理**对象过期删除**：

```
reconcile/lifecycle.go
  └─ sweepExpired()
       └─ repo.ListExpired()  // SQL: WHERE deleted_at IS NULL AND expire_at <= now()
            └─ 对每个对象 soft_delete 或 hard_delete
```

存储类（`storage_class`）的完整生命周期：

```sql
-- migration 0021: 对象表增加 storage_class 列，默认 STANDARD
ALTER TABLE objects ADD COLUMN storage_class TEXT NOT NULL DEFAULT 'STANDARD';
```

写入时根据 `x-amz-storage-class` 头或 `DefaultStorageClass` 设置 `storage_class`，但**写入后永不改变**。

S3 生命周期 XML 解析中的缺失（`internal/api/s3compat/bucketconfig.go`）：

```go
// 当前实现只读取 Expiration.Days
if rule.Expiration != nil && rule.Expiration.Days > 0 {
    days = rule.Expiration.Days
}
// Transition、NoncurrentVersionTransition、NoncurrentVersionExpiration、
// AbortIncompleteMultipartUpload 均被静默忽略
```

bucket 配置中只存储了过期策略：

```go
// internal/repository/repository.go:BucketConfig
type BucketConfig struct {
    // ...
    ExpireAfterDays int    // days after creation/version to expire
    ExpireAction    string // "soft_delete" or "hard_delete"
    // 无 TransitionDays map[string]int  (storage_class -> days)
    // 无 NoncurrentVersionTransition
    // 无 NoncurrentVersionExpiration
    // 无 AbortIncompleteMPUDays
}
```

### 产品价值

| 用户画像 | 场景 | 当前体验 | 就绪后 |
|---------|------|---------|--------|
| 成本管理员 | 日志数据 30 天后几乎不被访问但需要保留 1 年 | 全部 STANDARD 存储，1TB 日志月费 ~$23（S3 标准） | 30 天后 → INFREQUENT_ACCESS（$12.5），90 天后 → GLACIER（$4），1 年删除 — 节省 60%+ |
| DevOps 团队 | 构建产物保留 7 天，之后归档以备审计 | 手动脚本扫描 + 迁移，无统一声明式策略 | 一条 lifecycle rule 声明式完成：`transition[0]={days:7, class:GLACIER}, expiration={days:365}` |
| 合规官 | 版本化桶中的非当前版本无限累积 | 每版本都是 STANDARD，无成本控制 | `noncurrent_version_transition={days:30, class:INFREQUENT_ACCESS}` + `noncurrent_version_expiration={days:365}` |
| 平台管理员 | 中断的多分片上传残留永久占用存储 | 无自动清理，需要手动扫描 | `abort_incomplete_multipart_upload={days_after_initiation:7}` |

### 架构权衡

**方案 A：在现有 reconcile 引擎中扩展（推荐）**

```
reconcile/lifecycle.go
  └─ sweep()
       ├─ sweepExpired()           ← 已有
       ├─ sweepTransitions()       ← 新增
       │    └─ repo.ListTransitionDue()
       │         └─ FOR EACH object:
       │              └─ 判断当前 storage_class 和目标 storage_class
       │              └─ 转换成本：标准 → IA：元数据更新（低成本）
       │              └─ 转换成本：IA → GLACIER：存储层重写（高成本）
       └─ sweepNoncurrent()        ← 新增
       │    └─ repo.ListNoncurrentDue()
       │         └─ 过期非当前版本或转换其存储类
       └─ sweepAbortedMultipart()  ← 新增
            └─ repo.ListAbandonedUploads()
                 └─ 调用 store.AbortMultipart
```

| 维度 | 评价 |
|------|------|
| **复杂度** | 中 — 新增 3 个 Repository 查询方法 + LifecycleJob 中新增 3 个 sweep 方法 + Storage 接口需要增加 `TransitionStorageClass(key, targetClass)` 或复用 `Copy`（后端相关） |
| **影响范围** | `internal/reconcile/lifecycle.go`、`internal/repository/sql_objects.go`、`internal/repository/repository.go`、`internal/storage/storage.go`、`internal/api/s3compat/bucketconfig.go`、`internal/api/s3compat/xml.go`、迁移文件 0025 |
| **存储后端要求** | Local: 文件重命名或复制到子目录；S3: `CopyObject` with `x-amz-storage-class`；OSS/COS: 类似 API。**需要 Storage 接口新增 `TransitionStorageClass` 方法或扩展 `Copy` 语义** |
| **冷存储挑战** | GLACIER/DEEP_ARCHIVE 类存储**不可直接读取**（需先 Restore）— 当前 `Get` 路径需要增加 `storage_class == GLACIER → 返回 RestoreInProgress` 的逻辑 |
| **事务性** | `repo.UpdateStorageClass` 和 `store.Transition` 之间无事务。故障产生 metadata-storage_class 与实际后端存储类不一致。可接受——reconcile 重试幂等 |

**方案 B：异步 Job 队列（更解耦）**

将每个过渡/过期操作入队 `jobs` 表，由现有 JobPool 执行。优势：失败重试、速率控制、可观察性。

```
lifecycle sweep → 发现 N 个待过渡对象 → 每个入队 "lifecycle_transition" job
                                      → JobPool 消费 → 执行 transition
```

### 边界情况

| 场景 | 影响 | 处理策略 |
|------|------|---------|
| 对象从 STANDARD → GLACIER 后，用户 GET 请求 | GLACIER 不可直接读取 | 返回 `InvalidObjectState` 错误（S3 语义），或自动触发 Restore+等待 |
| 同一个对象在同一次 sweep 中匹配 transition + expiration | 双重操作或浪费 | transition 优先级高：先过渡，expiration 在下次 sweep 处理 |
| `sweepTransitions` 中 `store.Transition` 成功但 `repo.UpdateStorageClass` 失败 | metadata 与实际存储类不一致 | 使用幂等重试：sweep 时比对 `desired_class != actual_class` 重新 transition |
| 用户手动调用 `CopyObject` 改变 storage_class | 与 lifecycle sweep 并发竞争 | 乐观锁：使用 `updated_at` 或版本号做 CAS |
| `AbortIncompleteMultipartUpload` 清除后，用户尝试 CompleteMultipart | 正常——S3 语义：upload 不存在 | MultipartUpload 行被删除后 CompleteMultipart 返回 NoSuchUpload |
| NoncurrentVersion 的数量无上限 | 存储成本随版本无限增长 | 引入 `MaxNoncurrentVersions` bucket 配置，在 expiration 前先删除最旧版本 |

---

## 方向二：对象元数据与标签查询引擎

### 现状

所有对象都存储了 `tags` 和 `metadata` 两个 `map[string]string` 字段，数据模型完整但查询能力为零：

```sql
-- objects 表结构（简化）
CREATE TABLE objects (
  key      TEXT NOT NULL,
  tags     TEXT NOT NULL DEFAULT '{}',      -- JSON 对象
  metadata TEXT NOT NULL DEFAULT '{}',      -- JSON 对象
  content_type TEXT NOT NULL DEFAULT '',
  size     INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
```

当前唯一的列出方式是前缀匹配：

```go
// internal/repository/sql_objects.go
func (r *repository) ListObjects(ctx context.Context, tenant, bucket, prefix, marker string, limit int) ([]Object, error) {
    rows, err := r.db.QueryContext(ctx, `
        SELECT ... FROM objects
        WHERE tenant=? AND bucket=? AND key LIKE ? AND deleted_at IS NULL
        ORDER BY key LIMIT ?
    `, tenant, bucket, prefix+"%", marker, limit)
}
```

没有任何按 tags/metadata/content_type/size 范围/日期范围的查询能力。

### 产品价值

| 用户画像 | 场景 | 当前体验 | 就绪后 |
|---------|------|---------|--------|
| 数据科学家 | 查找所有 metadata.dataset="training" 的对象 | 需要通过 SDK 遍历所有对象客户端过滤 | `GET /v1/files?metadata.dataset=training` 直接返回匹配结果 |
| 合规官 | 查找所有标记 `classification=PII` 的对象 | 无能力 | `GET /v1/files?tag.classification=PII` 秒级返回 |
| DevOps | 查找上周创建的所有 `content_type=application/json` 的文件 | `ls` 全量 + grep，大桶不可行 | `GET /v1/files?content_type=application/json&created_after=2026-07-04` |
| 审计员 | 查找 `size > 1GB` 的大对象 | 无法用当前 API 实现 | `GET /v1/files?size_min=1073741824` |
| 平台用户 | 查找 `updated_at > 2026-07-01` 且 key 以 `/reports/` 开头的对象 | SQL 直接查库（不安全） | RESTful 查询 API |

### 架构权衡

**方案：扩展 ListObjects 查询能力，支持可选的过滤条件**

```
GET /v1/files?prefix=reports/&tag.dept=finance&metadata.project=audit&content_type=application/pdf&size_min=1024&created_after=2026-07-01&sort_by=created_at&sort_order=desc&limit=50
```

| 维度 | 评价 |
|------|------|
| **复杂度** | 中 — Repository 层新增条件构建器 + Handler 层解析 query 参数 + SQL JSON 查询 |
| **SQLite JSON** | `json_extract(tags, '$.dept') = 'finance'` — SQLite 3.38+ 支持 JSON 函数；需添加索引：`CREATE INDEX idx_objects_tag_dept ON objects(json_extract(tags, '$.dept'))` |
| **Postgres JSONB** | `tags @> '{"dept":"finance"}'` — 原生 JSONB 支持，可建 GIN 索引：`CREATE INDEX idx_objects_tags_gin ON objects USING GIN (tags jsonb_path_ops)` |
| **性能** | 全文索引扫描 + JSON 提取在大表上需加索引；多条件组合走最选择性索引 + 内存过滤 |
| **SQL 注入** | 所有用户输入必须通过参数化查询，禁止拼接 JSON path 字符串 |

**查询参数设计（REST API）：**

| 参数 | 类型 | SQL 映射 | 索引策略 |
|------|------|---------|---------|
| `tag.<k>` | string | `json_extract(tags, '$.k') = ?` | JSON 表达式索引（每 tag key 一个）或 Postgres GIN |
| `metadata.<k>` | string | `json_extract(metadata, '$.k') = ?` | 同上 |
| `content_type` | string | `content_type = ?` | 直接列索引 |
| `size_min` / `size_max` | int64 | `size >= ? AND size <= ?` | `idx_objects_size` B-tree 索引 |
| `created_after` / `created_before` | time | `created_at >= ? AND created_at <= ?` | `idx_objects_created_at` B-tree 索引 |
| `updated_after` / `updated_before` | time | `updated_at >= ? AND updated_at <= ?` | `idx_objects_updated_at` B-tree 索引 |
| `sort_by` | string | `ORDER BY <column> <dir>` | 白名单校验（仅允许 key/size/created_at/updated_at） |
| `sort_order` | `asc`/`desc` | 拼接 ORDER BY | 硬编码校验 |

**S3 兼容性：**

S3 不提供 REST 风格的元数据查询。但提供 S3 Select（`select-object-content`）——在对象级别用 SQL 查询 CSV/JSON/Parquet。本方向聚焦的是**桶级别按元数据筛选对象列表**，这是 S3 能力之外的自有增值特性。也可映射到 MCP tool 暴露：

```json
{
    "name": "query_objects",
    "description": "Search objects by tags, metadata, content type, size, or date range.",
    "inputSchema": {
        "properties": {
            "tag": {"type": "object", "description": "Tag key-value pairs to match"},
            "metadata": {"type": "object", "description": "Metadata key-value pairs to match"},
            "content_type": {"type": "string"},
            "size_min": {"type": "integer"},
            "created_after": {"type": "string"}
        }
    }
}
```

### 边界情况

| 场景 | 影响 | 处理策略 |
|------|------|---------|
| `tag.` 或 `metadata.` 参数中的 key 包含特殊字符（如点号、引号） | JSON path 构建错误/注入 | 严格校验 key 仅允许 `[a-zA-Z0-9_-]`；限制 key 长度 ≤ 128 |
| 无索引的 JSON 字段查询在大表上 | 全表扫描 + JSON 解析，性能灾难 | 查询计划检测：explain 发现全表扫描时记录警告日志；文档建议提前创建常用标签索引 |
| 组合多条件（如 tag.x=a & metadata.y=b & size_min=1GB） | 选择性高的条件走索引，其余内存过滤 | 查询规划器自动选择最优索引；DBA 可通过 `CREATE INDEX` 自定义 |
| 查询返回结果过多 | 响应体过大、DB 内存溢出 | 强制分页：`limit` 上限 1000，使用 `marker` 游标分页 |
| 用户在 `tags` 或 `metadata` 中存储大量数据 | JSON 列膨胀 | 已有 `ErrMetadataTooLarge`（64KB 上限）；tags 加相似限制 |
| Postgres 和 SQLite 的 JSON 函数差异 | 查询构建器需要双实现 | `repository/sql.go` 中已有 `rebind` 模式；新增 `jsonExtract(column, path)` 抽象方法 |

---

## 方向三：事件驱动的可配置工作流触发器

### 现状

事件总线的基础设施完整：

```
Bus.Publish()
  ├── 持久化到 events 表（durable, restart-safe）
  └── 内存广播到所有 subscriber
       ├── Indexer (硬编码)
       ├── Antivirus (硬编码)
       ├── Replication (硬编码)
       ├── Webhook (单一 URL, 全部事件)
       └── (无用户可配置目标)
```

通知规则 schema 已存在但无执行引擎：

```sql
-- migration 0024: 桶增加 notification_rules JSON 列
ALTER TABLE buckets ADD COLUMN notification_rules TEXT NOT NULL DEFAULT '[]';
```

REST API 端点已注册但仅存取未执行：

```go
// internal/api/rest/handler.go
r.Get("/buckets/{bucket}/notification", h.GetBucketNotifications)    // 读取 JSON 字符串
r.Put("/buckets/{bucket}/notification", h.PutBucketNotifications)    // 存储 JSON 字符串
r.Delete("/buckets/{bucket}/notification", h.DeleteBucketNotifications) // 清空
```

这三个端点将传入的 JSON 写入 `BucketConfig.Notifications`，读取时返回存储的 JSON，**没有任何规则解析、匹配、路由或执行逻辑**。

### 产品价值

| 用户画像 | 场景 | 当前体验 | 就绪后 |
|---------|------|---------|--------|
| 数据平台团队 | 新文件上传后自动触发 ETL pipeline | 需轮询 API 或自建消息队列 | 配置 `notification_rule`：事件 `s3:ObjectCreated:*` → HTTP POST 到 ETL 服务 |
| 安全团队 | 敏感文件被删除时实时告警 | 无法实现 | 规则：事件 `s3:ObjectRemoved:*` + prefix `finance/` → 发送到 Slack webhook |
| DevOps | 镜像构建完成后触发 CD 流程 | 需外部协调 | 规则：事件 `s3:ObjectCreated:Put` + suffix `.img` → SQS 队列 |
| 平台管理员 | 限制通知频率防止下游过载 | 无能力 | 规则级速率限制 + 去重窗口（如 60 秒内同 key 只发一次） |

### 架构权衡

**方案：规则引擎作为 EventBus 的通用 subscriber**

```
event flow:

Bus.Publish(event)
  ├── (已有) 硬编码 subscriber
  └── (新增) NotificationRouter
       └── 查询 affected bucket 的 notification_rules
            └── FOR EACH rule:
                 ├── 事件类型匹配（ObjectCreated:* / ObjectRemoved:Put / ...）
                 ├── 前缀/后缀过滤
                 └── 目标分发
                      ├── HTTP(S) endpoint
                      ├── SQS (future)
                      ├── SNS (future)
                      └── Lambda (future)
```

**规则数据结构（兼容 S3 Event Notification 语义）：**

```go
type NotificationRule struct {
    ID          string   `json:"id"`
    Events      []string `json:"events"`        // ["s3:ObjectCreated:*", "s3:ObjectRemoved:Delete"]
    Filter      *Filter  `json:"filter,omitempty"`
    Destination Destination `json:"destination"`
    // 非 S3 标准扩展
    RateLimitRPS int `json:"rate_limit_rps,omitempty"`  // 每规则速率上限
    RetryMax     int `json:"retry_max,omitempty"`       // 失败最大重试次数
}

type Filter struct {
    Prefix string `json:"prefix,omitempty"`
    Suffix string `json:"suffix,omitempty"`
    // 支持 AND 语义：同时满足 prefix 和 suffix 才触发
}

type Destination struct {
    Type   string `json:"type"`             // "http" | "sqs" | "sns" | "lambda"
    URI    string `json:"uri"`              // URL / ARN
    Secret string `json:"secret,omitempty"` // HMAC 签名密钥
}
```

| 维度 | 评价 |
|------|------|
| **复杂度** | 中高 — 规则解析引擎 + 事件过滤 + 多目标分发 + 重试 + 速率限制 |
| **影响范围** | `internal/events/bus.go`（新增 `NotificationRouter` subscriber）、`internal/events/notification.go`（新文件）、`internal/repository/repository.go`（已有 `BucketConfig.Notifications`）、`internal/repository/sql_buckets.go`（反序列化规则） |
| **与已有 Webhook 的关系** | 当 `EVENTS_WEBHOOK_URL` 设置时，当前全局 Webhook 推所有事件到单一 URL。通知规则提供**每桶/每规则粒度的多目标推送**，两者可共存——全局 webhook 捕获系统性事件，规则引擎实现业务集成 |
| **过滤效率** | `Bus.Publish` 路径已是同步广播，通知规则过滤在此路径做无阻塞匹配；大量规则可能增加 Publish 延迟。可通过规则索引预筛选（按 bucket + event type 分组） |
| **重试策略** | 复用已有 `webhook_failures` 表的持久化重试模式 |

### 边界情况

| 场景 | 影响 | 处理策略 |
|------|------|---------|
| 每个桶创建大量通知规则（数百条） | 事件发布 O(n) 规则匹配 | 限制每桶最大规则数（如 10 条）；桶级别规则缓存；超限返回 400 |
| 通知目标地址不可达 | 消息丢失 | 持久化 `notification_failures` 表 + 指数退避重试（复用 `webhook_failures` 模式） |
| 同一个事件匹配多个规则的目标相同 | 重复推送 | 规则级去重窗口；允许重复（应用层幂等） |
| 用户在事件洪峰期创建大量对象 | 通知风暴打垮下游 | 规则级 `RateLimitRPS`；全局通知速率上限；Backpressure（缓冲区满时丢弃） |
| 更新通知规则后正在处理的事件 | 旧规则执行或新规则执行——竞态 | 弱一致性：事件持久化时带当时的规则快照？代价太高。接受短暂不一致（最终一致） |

---

## 方向四：分布式读路径扩展——内容缓存层与只读副本路由

### 现状

当前读请求路径：

```
Client → REST/S3/WebDAV Handler → FileService.Get() → Storage.Get() → (Local FS / S3 / OSS / COS)
                                                                    └─ 每次直连后端，零缓存
```

所有 GET/HEAD 请求直通存储后端，无任何中间缓存层：

```go
// internal/service/file_crud.go
func (s *FileService) Get(ctx context.Context, tenant, bucket, key string, opts GetOptions) (io.ReadCloser, Object, error) {
    // ...
    storageKey := s.storageKey(tenant, bucket, key)
    rc, info, err := s.store.Get(ctx, storageKey)  // ← 每次都调用底层存储
    // ...
}
```

```go
// internal/storage/local_read.go
func (s *LocalStorage) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
    // ...
    f, err := os.Open(path)  // ← 每次都系统调用
    // ...
}
```

没有数据局部性优化、没有热对象缓存、没有读副本路由。对 Postgres 部署而言，连 metadata 查询（Stat/Head）都走主实例。

### 产品价值

| 用户画像 | 场景 | 当前体验 | 就绪后 |
|---------|------|---------|--------|
| 高频 API 用户 | 同一文件每秒被读取 100 次 | 存储后端承受 100 次完整 I/O | 热对象命中内存缓存 → 延迟从 10ms 降至 <1ms |
| 多区域部署 | 跨区域用户读取同一对象 | 所有请求到主区域存储后端 | 只读副本区域本地处理，延迟降低 10-100× |
| 大文件客户端 | 反复下载相同的 ML 模型包（5GB） | 每次都从 S3 全量下载（~200MB/s 带宽消耗） | 边缘缓存命中 → 本地读取 |
| 元数据密集查询 | 大量 HEAD 请求检查文件变更 | 每次 HEAD 都查询 Postgres 主库 | HEAD 请求由缓存层响应（TTL 内）；只读副本分担查询 |

### 架构权衡

**方案：分层的缓存体系——内存缓存 + 可选 CDN + 只读数据库副本**

```
层次一：内存缓存（Storage 接口的装饰器）
  CacheStorage implements Storage
    ├── Put: 写入后端 + 写入缓存（write-through 或 write-around）
    ├── Get: 先查缓存 → 命中返回 → 未命中查后端 → 写入缓存
    ├── Stat/Head: 缓存元数据（ObjectInfo）— TTL 控制
    └── Delete: 删除后端 + 删除缓存

层次二：内容分发（只读副本，可选）
  └── ReadReplicaStorage implements Storage (只读)
       ├── Get/Stat/Head → 路由到副本
       └── Put/Delete → 路由到主后端
       └── 后端间异步元数据同步

层次三：HTTP 响应缓存（可选）
  └── Cache-Control / ETag 支持 → 客户端侧缓存
       └── 已有 conditional request 支持（If-Match/If-None-Match）
       └── 缺少 Cache-Control header 设置
```

| 维度 | 评价 |
|------|------|
| **复杂度** | 中 — Storage 接口装饰器模式 + 配置化启用的缓存层 |
| **影响范围** | `internal/storage/cache.go`（新文件）、`internal/storage/factory.go`（`WrapWithCache`）、`internal/config/config_app.go`（缓存配置项）、`internal/middleware/cache.go`（HTTP 缓存头） |
| **缓存策略** | Write-around PUT：写入穿透到后端，失效缓存 key；Write-through：写入时同步更新缓存（适合小对象）；Read-Through GET：缓存未命中时加载 |
| **缓存失效** | 直接对象删除/覆盖：同步失效缓存 key；过期失效：TTL 控制，`CacheStorage` 装饰器记录 TTL；版本化：同 key 多版本时缓存当前版本，写入新版本时失效 |
| **内存限制** | `CACHE_MAX_BYTES` 配置项；LRU 逐出；大对象（>1MB）跳过缓存 |

**缓存配置（`config/config_app.go`）：**

```go
type CacheConfig struct {
    Enabled     bool   // CACHE_ENABLED
    MaxBytes    int64  // CACHE_MAX_BYTES (0 = unlimited in test/dev)
    MaxObjects  int    // CACHE_MAX_OBJECTS (0 = default 10000)
    MaxObjectBytes int64 // CACHE_MAX_OBJECT_BYTES (>0 跳过更大的对象)
    TTLSeconds  int    // CACHE_TTL_SECONDS (元数据，0 = no expiry)
    Backend     string // CACHE_BACKEND: "memory" | "redis" | ""
}
```

**只读副本路由配置：**

```go
type ReadReplicaConfig struct {
    // 只读副本的 DB DSN（读查询路由到此 DSN）
    ReadDBDSN string
    // 只读存储后端（可选）
    ReadStorage StorageConfig
}
```

### 边界情况

| 场景 | 影响 | 处理策略 |
|------|------|---------|
| 缓存对象在一台 replica 上更新，另一台 replica 的缓存未失效 | 脏读 | 短 TTL（秒级）+ 可选 Redis 集中缓存（全局一致性） |
| 大对象（如 5GB 模型文件）被多副本缓存 | 内存超限 | `MaxObjectBytes` 跳过；Streaming：缓存仅缓冲部分？缓存仅元数据不缓存体 |
| 写入后立即读取（写后读一致性） | 缓存写延迟导致读取旧内容 | Write-through 模式保证一致性（写入完成后再返回，代价是写入延迟增加） |
| 缓存层故障 | 缓存不可用 → 读请求失败 | 缓存层出错时降级到直接读取后端（fallback）；断路器模式 |
| 只读副本滞后 | 读取到旧元数据或旧内容 | 最终一致性可接受时使用；强一致性请求增加 `?consistency=strong` 参数 → 路由到主实例 |

---

## 方向五：分布式部署的一致快照与时间点恢复

### 现状

当前快照工具仅针对 SQLite + local FS 开发场景：

```go
// internal/snapshot/snapshot.go:23 — 明确的范围限制
// Package snapshot packs the database + object storage into a single tar.gz
// for backup/restore. It is intended for SQLite + local-FS development
// instances and small production deployments. For large Postgres+S3 stacks,
// fall back to pg_dump + s3 lifecycle copies.
```

DSN 解析仅支持 SQLite：

```go
// internal/snapshot/snapshot.go:186
func dbFileFromDSN(dsn string) string {
    // 解析 "file:./var/aero.db?..." → "./var/aero.db"
    // 对 Postgres DSN ("postgres://user:pass@host/db") 返回 ""
}
```

对于生产部署（Postgres + S3）没有任何一致快照机制：

| 组件 | 生产部署 | 快照能力 | 问题 |
|------|---------|---------|------|
| 元数据 | Postgres | `pg_dump`（事务一致） | 内容快照需要与之时间点对齐 |
| 对象内容 | S3 | S3 API 批量列举 | 无跨系统一致标记 |
| 配置/密钥 | 环境变量 / Secret | 外部 | 无快照参与 |

### 产品价值

| 用户画像 | 场景 | 当前体验 | 就绪后 |
|---------|------|---------|--------|
| SRE | 升级前创建全量一致快照 | 需手动协调 pg_dump + s3 sync，无法保证时间点一致 | `aero-vault snapshot create --output=s3://backups/snap-2026-07-11.tar.gz` 一键完成 |
| 合规官 | 法律要求保留某时间点的完整数据拷贝 | 手动、不可靠 | 时间点标记 + 导出到独立存储桶 |
| 业务恢复 | 误操作导致数据损坏，需要回滚到昨天 | 没有回滚能力，只能从代码仓库重新构建 | `aero-vault snapshot restore --from=s3://backups/snap-2026-07-10.tar.gz` |
| 开发环境同步 | 需要从生产环境拉取一份数据子集做测试 | 手动步骤多、容易出错 | 快照 + 选择性恢复（指定 prefix/tenant） |

### 架构权衡

**方案：基于对象列表标记的一致快照**

核心思想：记录快照时间点，不复制元数据库和内容存储的全部数据，而是**记录该时间点的对象清单（manifest）**，恢复时根据 manifest 重建。

```
Snapshot 文件格式（tar.gz）:
  manifest.json — 快照时间点、元数据、对象清单
  ├── version: 1
  ├── created_at: 2026-07-11T12:00:00Z
  ├── metadata: {config,tenant_list,key_hashes,...}
  └── objects: [
       {key:"doc/a.pdf", bucket:"default", version_id:"...", etag:"...", storage_class:"STANDARD"},
       ...
     ]
  （不包含实际对象内容，对象通过 etag+storage_key 引用）
```

**快照类型：**

| 类型 | 复杂度 | 恢复能力 | 适用场景 |
|------|--------|---------|---------|
| **Level 0: Manifest-only** | 低 | 仅知道"有哪些对象"，恢复需从原始存储读取 | 备份清单、审计 |
| **Level 1: Metadata + Manifest** | 中 | 可恢复完整元数据至 Postgres + 对象清单 | 数据库灾难恢复（元数据重建） |
| **Level 2: Full (metadata+content)** | 高 | 完整恢复到独立存储 | 完全隔离的 DR |

**实现路径：**

```
Phase 1: 通用快照 CLI（低复杂度）
  CLI: aero-vault snapshot create --manifest-only
  1. 访问 Repository，事务级查询当前所有活动对象（LIMIT 分页）
  2. 生成 manifest.json（对象 key + etag + version_id 清单）
  3. 存储到快照 tar.gz（可选上传到独立 S3 bucket）
  
  CLI: aero-vault snapshot restore --manifest manifest.json
  1. 读取 manifest
  2. FOR EACH object: stat → 验证 etag 匹配 → 不匹配时警告
  3. 输出恢复报告

Phase 2: 元数据快照（中复杂度）
  添加 Repository.Snapshot(ctx) → 事务级导出所有元数据表
  └─ 对 Postgres: 内部执行 pg_dump 或 pg_export（需要超级权限？）
  └─ 对 SQLite: 使用备份 API
  
Phase 3: 内容快照（高复杂度）
  FOR EACH object in manifest:
    └─ Storage.Get() → 写入 tar.gz 或 S3 copy 到备份 bucket
```

| 维度 | 评价 |
|------|------|
| **复杂度** | 中高（Phase 1 低、Phase 2 中、Phase 3 高） |
| **影响范围** | `internal/snapshot/` 重写、`internal/repository/repository.go`（新增 `Snapshot`/`Restore` 方法）、`internal/cli/cli_snapshot.go`（扩展 CLI）、`internal/config/config_app.go`（备份策略配置） |
| **Postgres 一致性问题** | `pg_dump` 需要 `--serializable-deferrable` 事务隔离级别确保一致性；或者使用 `pg_export_snapshot()` 函数获取系统快照 ID |
| **增量快照** | 每次只记录变更（基于 `updated_at` 或 `event log`）——需要版本历史或变更数据捕获（CDC） |

### 边界情况

| 场景 | 影响 | 处理策略 |
|------|------|---------|
| 快照过程中对象被写入/删除 | 快照一致性破坏：manifest 包含的 etag 与恢复时实际 etag 不匹配 | 使用可序列化事务（Postgres）或全局时间戳标记点；manifest-only 场景下标记时间点，恢复时验证 etag |
| 恢复时对象已在目标桶中存在 | 覆盖风险 | 恢复选项：`--skip-existing` / `--overwrite` / `--version`（创建新版本） |
| 大快照创建耗时过长 | 时间点偏差（同一快照中的对象时间不同） | 分片并行快照 + 最终校验；对等业务接受最终一致快照 |
| 快照存储到 S3 本身 | 快照文件的备份？ | 快照的文件可以再次被快照（递归防护：manifest 排除快照文件自身路径） |
| 快照中包含已软删除的对象 | 恢复已删除数据 | `manifest-only` 默认只包含 `deleted_at IS NULL`；可选 `--include-soft-deleted` |

---

## 综合优先级建议

| 方向 | 优先级 | 建议启动时机 | 预计工期 | 交付影响 |
|------|--------|------------|---------|---------|
| **方向一：生命周期分层引擎** | **P1** | 当前 Sprint 后立即启动 | 2–3 周 | 直接降低用户存储成本 40-70%，提升 S3 协议完整度 |
| **方向二：元数据/标签查询引擎** | **P1** | 当前 Sprint 后立即启动 | 1–2 周 | 填补 ListObjects 的功能空白，大幅提升数据可发现性 |
| **方向三：事件驱动工作流触发器** | **P2** | 方向一/二交付后 | 2–3 周 | 打开平台集成生态，通知规则 schema 已就绪（0024 migration） |
| **方向四：读路径缓存扩展** | **P2** | 方向一/二交付后 | 1–2 周 | 高频读取场景延迟降低 90%+，降低后端 I/O 压力 |
| **方向五：分布式一致快照** | **P2** | 方向一/二交付后 | 2–4 周（分 Phase） | 填补生产运维最后一块拼图，降低恢复时间目标（RTO） |

> **风险提示：** 方向三虽然 schema 已存在，但规则引擎的过滤性能和多目标分发架构设计需要谨慎——事件广播路径是同步的，不恰当的规则匹配实现会反向影响核心写入路径延迟。建议规则匹配采用**异步过滤器**模式：事件入 `events` 表后，专用 notification worker 消费并匹配规则，不阻塞主 Publish 路径。
