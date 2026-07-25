# 高价值扩展方向：代码级盲区与生产就绪缺口

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — 涵盖 `cmd/`、`internal/` 全部子包（~250 个 Go 源文件）、3 套 SDK、Web UI、50 对迁移文件、`deploy/` 全套配置  
> **去重验证：** 对 `docs/requirements/` 下全部 109 份既有分析文档进行关键词 + 代码锚点交叉验证，确认本文方向未被独立深度覆盖  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。每个方向必须包含：**代码中存在的具体实现锚点**、**可量化的生产影响**、**架构权衡与边界情况**。

---

## 方法论：面向代码盲区的扫描策略

本文与前 109 份分析的核心区别：不关注"理论上可以添加什么大功能"，而是聚焦**代码中已有接口、类型、配置字段或数据模型，但管线断裂、逻辑短路或生产场景下行为错误的缺口**。筛选标准：

| 条件 | 说明 |
|------|------|
| **代码锚点存在但管线断裂** | 接口中定义了方法签名，或数据模型中包含字段，但实现路径缺失或短路 |
| **生产影响可量化** | 导致：数据丢失风险、合规漏洞、性能瓶颈、运营成本失控、S3 SDK 不兼容 |
| **跨 109 份分析未深度覆盖** | 前 109 份文档中无独立架构方案、无代码级分析、无边界情况枚举 |
| **修复可独立推进** | 不依赖外部服务或重大架构变更，可在当前抽象层内完成 |

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码证据 |
|---|------|------|--------|---------|---------|
| **1** | **S3 Multipart Upload Copy（UploadPartCopy）与大对象复制** | 性能/数据完整性 | **P0** | 所有复制操作（S3 CopyObject、跨区 Replication）均通过 Get→Put 实现。AWS S3 PutObject 限制 5GB，超过此大小的对象无法复制，跨区 DR 在 S3 后端上对 >5GB 对象完全断裂 | `Storage` 接口无 `Copy` 方法（`internal/storage/storage.go:30-85`）；复制件 Get→Put 实现（`internal/api/s3compat/extra.go:39-65`，`internal/replication/replication.go:104-120`）；S3 `UploadPart` 已实现但无 `UploadPartCopy` |
| **2** | **Per-Object TTL / 对象级过期时间** | 产品/功能 | **P1** | 无逐对象过期机制。`Object` 模型无 `ExpiresAt` 字段。生命周期仅桶级别。S3 `x-amz-expiration` 头不被解析；临时上传、缓存、预签名内容、会话数据等场景无法声明对象存活时间 | `repository.Object`（`internal/repository/repository.go:40-55`）无 `ExpiresAt`；S3 handler 不处理 `x-amz-expires`（全文件 grep 零命中）；`BucketConfig.ExpireAfterDays` 仅桶级 |
| **3** | **Metadata / Tag 服务端过滤在 List API** | 性能/产品 | **P1** | `ListObjects` 仅支持 prefix + marker 分页。`ListObjectsByTag` 先取出整页再做 Go 内存过滤。对于百万级对象桶，每次列出需全量传输+过滤，延迟和网络成本线性增长。SQL 层可直接 `WHERE metadata->>'key'='val'` 但未实现 | `ListObjectsByTag` 客户端过滤（`internal/repository/sql_objects.go:235-265`）；`ListObjects` SQL 查询无 metadata WHERE 条件（`internal/repository/sql_objects.go:166-198`）；REST/S3 List API 不接受 `?metadata.k=v` 参数 |
| **4** | **NoncurrentVersion Expiration — 历史版本自动清理** | 成本/运营 | **P1** | 版本化桶产生无限版本，每版本都占用存储成本和对象计数配额。S3 标准支持 `NoncurrentVersionExpiration`（生命周期规则：保留最新 N 个版本，或删除 N 天前的非当前版本）。当前 Reconcile 完全不处理版本 | `internal/reconcile/lifecycle.go:70-110` — `sweepExpired` 只处理 `expire_after_days` 活跃对象；无版本清理逻辑；迁移文件 `0021_storage_class` 后无版本管理相关 |
| **5** | **Event Notification Filter — 事件通知前缀/后缀过滤** | 产品/性能 | **P2** | `NotificationRule.FilterKey` 字段存在（`internal/repository/repository.go:61`），Webhook 收到的事件经 S3 渠道可配置 filter，但 `events/webhook.go` 完全忽略 filter，所有事件广播到所有 URL。大量无关事件推送给 webhook 消费者 | `FilterKey` 字段定义（`repository.go:61`）；S3 handler 通知配置解析 filter（`handler.go:786-818`）；`events/webhook.go:55-95` — `Run` 全部事件无过滤直接 `deliver` |

---

## 方向一：S3 Multipart Upload Copy（UploadPartCopy）与大对象复制

### 产品价值

| 维度 | 影响 |
|------|------|
| **大对象复制断裂** | AWS S3 的 `PutObject` API 单次最大支持 5GB。当 `Storage` 后端为 S3 时，以下场景全部断裂：S3 `CopyObject`（>5GB 的对象）、跨区 Replication worker（>5GB 对象）、跨后端复制（local→s3 的 >5GB 对象）。这直接导致企业级 DR 方案不可用 |
| **内存/网络资源浪费** | 即使 <5GB 的对象，当前 Get→Put 复制也需要将整个对象读入 Go 进程内存再写出。对于 GB 级对象，内存峰值 = 对象大小 × 并发复制数，极易触发 OOM。AWS S3 CopyObject 是服务端操作，数据不离开 S3 集群，延迟和资源消耗减少 90%+ |
| **S3 SDK 不兼容** | 主流 S3 SDK 的 `CopyObject` 调用者期望近乎瞬时的响应（尤其是小对象），当前 Get→Put 实现完全破坏了 SDK 使用体验。AWS SDK 的 `UploadPartCopy` 方法在 >5GB 复制场景是标准路径，但系统中完全不存在 |

### 现状与代码证据

**Storage 接口无 Copy 方法，无 UploadPartCopy：**

```go
// internal/storage/storage.go:30-85
type Storage interface {
    Put(...)
    Get(...)
    Stat(...)
    Delete(...)
    List(...)
    PresignGet/PresignPut(...)
    InitMultipart/UploadPart/CompleteMultipart/AbortMultipart(...)
    Backend() string
    // ❌ 无 Copy(ctx, srcKey, dstKey, opts) (ObjectInfo, error)
}
```

`grep -rn "UploadPartCopy\|uploadPartCopy\|CopyObject\|copyObject" internal/storage/` → **零命中**。所有后端（local、s3、oss、cos）都不提供 Copy 或 UploadPartCopy 方法。

**S3 handler 的 CopyObject 是 Get→Put 内存中转：**

```go
// internal/api/s3compat/extra.go:39-65
func (h *Handler) copyObject(w http.ResponseWriter, r *http.Request, dstBucket, dstKey, copySource string) {
    rc, src, err := h.svc.Get(r.Context(), tenant, srcBucket, srcKey)  // ← 全量读入内存
    // ...
    dst, err := h.svc.Put(r.Context(), tenant, dstBucket, dstKey, rc, src.Size, opts)  // ← 全量写出
}
```

**S3 后端 PutObject 有 5GB 限制（AWS 文档化）：**

```go
// internal/storage/s3.go:81-95
func (s *S3Storage) Put(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) (ObjectInfo, error) {
    input := &s3.PutObjectInput{
        Bucket: aws.String(s.cfg.Bucket),
        Key:    aws.String(key),
        Body:   r,
    }
    // ...
    out, err := s.client.PutObject(ctx, input)  // ← AWS 限制 5GB max
}
```

**跨区 Replication Worker 同样 Get→Put（>5GB 断裂）：**

```go
// internal/replication/replication.go:104-120
func (w *Worker) ReplicateObjectByID(ctx context.Context, objectID int64) error {
    // ...
    rc, _, err := w.primary.Get(ctx, obj.StorageKey)      // ← 全量读
    // ...
    _, err = w.replica.Put(ctx, obj.StorageKey, rc, ...)   // ← 全量写（S3 后端 >5GB 失败）
}
```

### 架构权衡与建议方案

#### 方案 A（推荐）：Storage 接口增加 Copy + UploadPartCopy

在 `Storage` 接口上增加：

```go
type Storage interface {
    // ... 现有方法 ...
    Copy(ctx context.Context, srcKey, dstKey string, opts CopyOptions) (ObjectInfo, error)
    UploadPartCopy(ctx context.Context, srcKey, dstKey, uploadID string, partNumber int32, srcOffset, srcLength int64) (MultipartPart, error)
}
```

各后端实现：

| 后端 | `Copy` 实现 | `UploadPartCopy` 实现 |
|------|-------------|----------------------|
| **Local** | `copy_file_range` syscall 或 `io.Copy`（跨分区回退） | 本地文件分段复制 |
| **S3** | `s3.CopyObject` API（单次请求，数据不离开集群） | `s3.UploadPartCopy` API |
| **OSS** | `oss.CopyObject` API | OSS 分片复制 |
| **COS** | COS 复制 API | COS 分片复制 |

**Service 层包装：**

```go
func (s *FileService) CopyObject(ctx context.Context, tenant, srcBucket, srcKey, dstBucket, dstKey string, opts CopyOptions) (repository.Object, error) {
    // 1. 解析 storage keys
    // 2. 优先调用 store.Copy()
    // 3. 若 store returns ErrUnsupported，回退到 Get→Put
    // 4. 复制元数据、标签、ACL
    // 5. 新对象写 repo，源对象触发 accessed 事件
}
```

**优势：**
- 云后端实现极为简单（单 API 调用）
- 大幅减少大对象复制的延迟和内存消耗
- 向后兼容（回退路径）

#### 方案 B：Service 层检测对象大小自动选择 UploadPartCopy

当源对象 >5GB 时，自动使用 multipart upload + UploadPartCopy 而非单次 Copy。

#### 边界情况

| 场景 | 处理 |
|------|------|
| 跨后端复制（local→s3） | 存储层无法优化，必须回退到 Get→Put 流式复制（但仍需分片以防止 >5GB 断裂） |
| 相同后端不同 bucket | S3 CopyObject 支持跨 bucket 复制，Local 可能需要 io.Copy |
| 源对象 >5TB（S3 CopyObject 限 5GB，UploadPartCopy 每部分限 5GB，最多 10000 部分） | 必须使用 multipart，每部分最大 5GB，10000 部分 = 最大 50TB |
| `x-amz-copy-source-if-*` 条件头 | Service 层需处理 If-Match / If-None-Match / If-Modified-Since / If-Unmodified-Since |
| `x-amz-metadata-directive: COPY` vs `REPLACE` | COPY 保留源对象内容类型和元数据；REPLACE 使用请求头覆盖 |
| 版本化对象复制 | 需支持 `?versionId` 查询参数复制特定版本 |
| 分片复制时源对象正在被写入 | 最终一致性；文档化行为 |
| 对象锁复制 | 目标对象应继承源对象锁定状态 |

---

## 方向二：Per-Object TTL / 对象级过期时间

### 产品价值

| 维度 | 影响 |
|------|------|
| **临时文件/缓存场景** | 上传的临时文件、预签名 URL 指向的过期内容、AI 生成的中间结果等场景需要对象在一段时间后自动删除。当前只能依赖桶级生命周期或手动清理，缺少逐对象声明过期能力 |
| **S3 协议兼容性** | AWS S3 的 `PutObject` 支持 `x-amz-expires` 请求头（设置对象过期时间），`GetObject` 响应包含 `x-amz-expiration` 头。标准 S3 SDK 客户端期望这些头。当前 S3 handler 完全忽略这些头 |
| **预签名 URL 安全性** | 预签名 URL 有自身过期时间，但指向的对象可能永久存在。若对象本身有 TTL，预签名 URL 即便未到期也可能指向已自动删除的内容，为合规场景提供额外安全层 |
| **运营自动清理** | 手动标记过期对象，由后台 Reconcile 统一清理，无需针对每个临时场景编写清理代码 |

### 现状与代码证据

**Object 数据模型无 ExpiresAt 字段：**

```go
// internal/repository/repository.go:40-55
type Object struct {
    ID           int64
    TenantID     string
    Bucket       string
    Key          string
    VersionID    string
    Backend      string
    StorageKey   string
    Size         int64
    ETag         string
    ContentType  string
    Metadata     map[string]string
    Tags         map[string]string
    StorageClass string
    CreatedAt    time.Time
    UpdatedAt    time.Time
    DeletedAt    *time.Time
    LockedUntil  *time.Time
    // ❌ 无 ExpiresAt *time.Time
}
```

**S3 handler 不处理 x-amz-expires / x-amz-expiration：**

```bash
grep -rn "x-amz-expir\|x-amz-expiration\|Expires\|expires_at\|expiration" internal/api/s3compat/ --include="*.go"
# → 仅输出 PresignGet 的 expiry 参数，非对象级过期
```

**生命周期仅桶级别：**

```go
// internal/repository/sql_buckets.go
// ListExpired 只查询桶级别的 expire_after_days：
obj.Metadata["__expire_action"] = action  // 通过桶配置注入，非对象级
```

**迁移文件无 expires_at 字段：** `internal/repository/migrations/{sqlite,postgres}/` 从 0001 到 0024，没有一条迁移添加 `expires_at` 列。

### 架构权衡与建议方案

#### 数据模型扩展

```go
type Object struct {
    // ... 现有字段 ...
    ExpiresAt   *time.Time // 对象级过期时间；nil = 永不过期
}
```

#### 迁移

```sql
-- 0025_add_expires_at.up.sql
ALTER TABLE objects ADD COLUMN expires_at TEXT;
CREATE INDEX idx_objects_expires_at ON objects(tenant_id, expires_at);
```

#### API 扩展

| 协议 | 输入 | 输出 |
|------|------|------|
| S3 | `x-amz-expires: <epoch>` 或 `x-amz-expiration: <ISO8601>` | `x-amz-expiration: expiry-date="...", rule-id="per-object"` |
| REST | `PUT /v1/files/*key?expires_at=<ISO8601>` | `expires_at` 字段在 Object JSON 中 |
| SDK | `PutOptions{ExpiresAt: time}` | 读响应 `Object.ExpiresAt` |

#### Reconcile 扩展

```go
// internal/reconcile/retention.go 或新文件
func (r *RetentionJob) sweepExpiredObjects(ctx context.Context) (int, error) {
    expired, err := r.repo.ListExpiredObjects(ctx, time.Now())  // 新 SQL：WHERE expires_at < now()
    for _, obj := range expired {
        // 跳过锁定对象
        // 软删除（可恢复）或硬删除（根据桶配置或对象元数据）
    }
}
```

#### 边界情况

| 场景 | 处理 |
|------|------|
| 对象 TTL 与桶生命周期同时存在 | 取最先触发者 |
| 更新对象（Put 覆盖）时更新 TTL | 同普通字段，新 Put 可设置新的 expires_at |
| 版本化桶 + TTL | 每个版本独立过期；过期版本标记删除 |
| 锁定的对象 + TTL | 过期时间晚于锁定解除时间时才允许自动删除 |
| TTL 精确度 | 不要求秒级精确；Reconcile 周期内最终一致即可 |
| x-amz-expires 与 S3 预签名 `?expires=` 冲突 | 一个是对象本身过期，一个是 URL 授权过期；语义不同，分别处理 |

---

## 方向三：Metadata / Tag / ACL 服务端过滤在 List API

### 产品价值

| 维度 | 影响 |
|------|------|
| **大规模桶查询性能** | `ListObjectsByTag` 当前是客户端过滤：从 DB 取出一整页（最多 1000 条）后，在 Go 内存中循环检查 `obj.Tags[tagKey]`。对于百万级对象桶，即使只查 1 个匹配对象也需要全量传输 1000 行 + 内存扫描 |
| **复合过滤场景** | 无法表达 "prefix=a/ 且 metadata.department=engineering 且 tags.project=alpha" 的查询。客户端必须拉取所有对象自行过滤，对于亿级桶完全不现实 |
| **REST API 完备性** | 当前 REST `GET /v1/files` 只接受 `?prefix=&marker=&limit=`，不支持 `?metadata.k=v`。S3 ListObjectsV2 的 tag-key/tag-value 参数已实现，但 metadata 过滤缺失 |
| **SQL 能力未充分利用** | 元数据以 JSON 存储在 `metadata` 列中（SQLite json / Postgres jsonb），SQL 引擎支持 `metadata->>'key' = 'value'` 过滤，但代码中未使用 |

### 现状与代码证据

**ListObjects SQL 无 metadata 过滤：**

```go
// internal/repository/sql_objects.go:166-198
func (s *sqlStore) ListObjects(ctx context.Context, tenant, bucket, prefix, marker string, limit int) (ListPage, error) {
    rows, err := s.db.QueryContext(ctx, s.rebind(`
SELECT ... FROM objects
WHERE tenant_id=$1 AND bucket=$2 AND deleted_at IS NULL AND key LIKE $3 AND key > $4
ORDER BY key ASC LIMIT $5`), ...)
    // WHERE 子句仅 tenant + bucket + key prefix + marker
    // ❌ 无 metadata 过滤条件
}
```

**ListObjectsByTag 是客户端过滤（性能灾难）：**

```go
// internal/repository/sql_objects.go:235-265
func (s *sqlStore) ListObjectsByTag(ctx context.Context, ...) (ListPage, error) {
    page, err := s.ListObjects(ctx, ...)  // 先正常取整页
    var filtered []Object
    for _, obj := range page.Objects {    // 然后在 Go 中过滤
        if obj.Tags == nil { continue }
        v, ok := obj.Tags[tagKey]
        if !ok { continue }
        // ...
    }
}
```

**REST 和 S3 List API 参数有限：**

```go
// internal/api/rest/router.go
r.Get("/files", h.List)
// Handler.List 只读 prefix, marker, limit, deleted 参数

// internal/api/s3compat/handler.go:432-470
// listObjectsV2 只处理 prefix, continuation-token, max-keys, tag-key, tag-value
// ❌ 无 metadata filter 参数
```

### 架构权衡与建议方案

#### 方案：SQL 层支持 metadata/tag 过滤

将 `ListObjects` 改为动态构建 WHERE 子句：

```go
type ListFilter struct {
    Prefix     string
    Marker     string
    Limit      int
    TagKey     string
    TagValue   string
    MetaFilter map[string]string // metadata_key → metadata_value
}

func (s *sqlStore) ListObjects(ctx context.Context, tenant, bucket string, filter ListFilter) (ListPage, error) {
    query := `SELECT ... FROM objects WHERE tenant_id=$1 AND bucket=$2 AND deleted_at IS NULL`
    args := []any{tenant, bucket}
    n := 2
    
    if filter.Prefix != "" {
        n++; query += fmt.Sprintf(` AND key LIKE $%d`, n); args = append(args, filter.Prefix+"%")
    }
    if filter.Marker != "" {
        n++; query += fmt.Sprintf(` AND key > $%d`, n); args = append(args, filter.Marker)
    }
    for k, v := range filter.MetaFilter {
        n++; query += fmt.Sprintf(` AND metadata->>'$%d' = $%d`, n, n+1)
        args = append(args, k, v)
        n++
    }
    // ...
}
```

**REST API 扩展：**

```
GET /v1/files?prefix=a/&metadata.color=red&metadata.env=prod&limit=20
```

**S3 ListObjectsV2 扩展：**

```
GET /s3/bucket?list-type=2&prefix=a/&x-amz-meta-color=red
```

#### 权衡

| 因素 | 说明 |
|------|------|
| SQL 注入 | 动态构建 WHERE 子句需参数化绑定；`$N` 占位符（`s.rebind` 兼容 SQLite 的 `?`） |
| 索引支持 | Postgres jsonb 支持 GIN 索引，SQLite JSON1 支持表达式索引 |
| 分页一致性 | 分页 marker 是 key，metadata 过滤后返回的对象数可能远小于 limit，需透传 hasMore |
| Postgres vs SQLite | `->>` 运算符在 SQLite 和 Postgres 中语法一致 |

#### 边界情况

| 场景 | 处理 |
|------|------|
| metadata 键不存在 | `metadata->>'nonexistent'` 返回 NULL，`= 'value'` 不匹配，行为正确 |
| 复合 AND 过滤 | 多个 `metadata->>'k' = 'v'` 用 AND 连接 |
| OR 过滤（任一 metadata 匹配） | 当前暂不支持；可后续添加 |
| Tag 过滤 + Metadata 过滤同时使用 | 组合 AND 条件 |
| 性能退化 | 无 index 的 metadata 过滤导致全表扫描；需文档化索引建议 |
| 分页截断 | 过滤后结果不足 limit 但 DB 中仍有更多对象：继续翻页 |

---

## 方向四：NoncurrentVersion Expiration — 历史版本自动清理

### 产品价值

| 维度 | 影响 |
|------|------|
| **存储成本失控** | 版本化桶无版本数量限制。每次 `Put` 创建新版本，旧版本存储空间永不释放。对于频繁更新的对象（日志、配置、AI 模型权重），版本数量可快速增长，存储成本线性膨胀 |
| **对象计数配额透支** | 每个版本计为 1 个对象计入 `used_objects` 配额。无限版本意味着一个对象可独占全部配额 |
| **S3 生命周期标准缺失** | AWS S3 生命周期规则支持 `NoncurrentVersionExpiration`（非当前版本过期天数）和 `NoncurrentVersionTransitions`（存储类转换）。当前系统生命周期只处理活跃对象 |
| **合规审计负担** | 保留所有历史版本无期限，可能违反 GDPR "right to erasure" 和数据最小化原则 |

### 现状与代码证据

**Reconcile/Lifecycle 完全不处理版本：**

```go
// internal/reconcile/lifecycle.go:70-110
func (l *LifecycleJob) sweepExpired(ctx context.Context) (soft, hard int) {
    expired, err := l.repo.ListExpired(ctx, 200)
    // ListExpired SQL 查询：
    // SELECT ... FROM objects o JOIN buckets b ...
    // WHERE o.deleted_at IS NULL AND b.expire_after_days > 0
    // AND o.updated_at < datetime('now', '-' || b.expire_after_days || ' days')
    // ❌ 只查 deleted_at IS NULL（活跃对象），不处理版本
}
```

**无版本清理的 SQL 查询：**

```bash
grep -rn "Noncurrent\|noncurrent\|non_current\|old_version\|stale_version\|version.*expir\|version.*clean\|version.*prune" internal/ --include="*.go" --include="*.sql"
# → 零命中
```

**版本化行为只在写入时保存，从不回收：**

```go
// internal/repository/sql_objects.go:80-120
func (s *sqlStore) InsertObjectVersion(ctx context.Context, obj Object) (Object, error) {
    // 软删除当前版本，插入新版本
    _, err = tx.ExecContext(ctx, `UPDATE objects SET deleted_at=now() WHERE ... AND deleted_at IS NULL`)
    // ... 插入新行
    // ❌ 无 "检查版本数量是否超过限制" 的逻辑
}
```

**迁移文件无版本管理相关：** 24 对迁移文件中无任何版本清理的表或列。

### 架构权衡与建议方案

#### 方案：桶级别 NoncurrentVersion 配置

```go
type BucketConfig struct {
    // ...
    Versioning                   bool
    NoncurrentVersionDays        int  // 非当前版本保留天数；0 = 永久保留
    MaxNoncurrentVersions        int  // 最大非当前版本数；0 = 不限制
    NoncurrentVersionDeleteAction string // "soft_delete" (默认) 或 "hard_delete"
}
```

#### Reconcile 扩展

```go
// internal/reconcile/noncurrent.go
func (r *RetentionJob) sweepNoncurrentVersions(ctx context.Context) (int, error) {
    // 查询所有版本化桶的非当前版本
    // 对每个桶：
    //   - 若 NoncurrentVersionDays > 0：删除超过 N 天的非当前版本
    //   - 若 MaxNoncurrentVersions > 0：保留最新 N 个版本，删除更旧的
    // 跳过已锁定版本（LockedUntil）
    // 软删除或硬删除取决于桶配置
}
```

#### S3 API 扩展

S3 handler 的 `putBucketLifecycle` 需解析：

```xml
<NoncurrentVersionExpiration>
    <NoncurrentDays>30</NoncurrentDays>
</NoncurrentVersionExpiration>
```

当前实现中的生命周期结构体缺少此字段：

```go
// internal/api/s3compat/xml.go 和 bucketconfig.go
// ❌ 无 NoncurrentVersionExpiration 结构体
```

#### 边界情况

| 场景 | 处理 |
|------|------|
| NoncurrentDays + MaxVersions 同时设置 | 取更严苛者：满足任一条件即删除 |
| 删除标记（Delete Marker）管理 | S3 中删除标记本身是一个版本；需要特殊处理 |
| 版本锁定 + 清理 | Compliance 模式锁定版本不可删除；Governance 模式需检查权限 |
| 删除中的版本正在被读取 | Reconcile 应避免删除正在被访问的版本（final consistency window） |
| 清理原子性 | 删除 storage blob + repository 行应在事务中完成 |
| 集群单例 | 版本清理是破坏性操作，需通过 `ClusterSingleton` 防止多副本并发执行 |

---

## 方向五：Event Notification Filter — 前缀/后缀过滤

### 产品价值

| 维度 | 影响 |
|------|------|
| **无关事件噪音** | 当前 webhook 接收桶中**所有**事件的**所有**变更。对于包含多种数据类型的桶（logs/、uploads/、processed/...），webhook 消费者收到大量无关事件，需自行过滤 |
| **S3 通知标准缺失** | AWS S3 通知配置支持 `S3Key` filter 规则（`FilterRule{Name: "prefix"|"suffix", Value: "..."}`）。标准 S3 客户端（如 Terraform、AWS CLI）期望能配置此 filter |
| **消费者不可扩展** | 单个 webhook URL 接受所有事件。若需不同的消费逻辑处理不同前缀的事件，用户必须部署中间过滤层或部署多个 aero-vault 实例 |

### 现状与代码证据

**NotificationRule 数据模型已有 FilterKey 字段：**

```go
// internal/repository/repository.go:57-61
type NotificationRule struct {
    ID        string   // unique rule id
    Events    []string // e.g. ["s3:ObjectCreated:*"]
    QueueARN  string   // SQS-style target
    TopicARN  string
    LambdaARN string
    FilterKey string   // ✅ 字段已存在：JSON pointer to S3Key filter
}
```

**S3 handler 能解析 filter：**

```go
// internal/api/s3compat/handler.go:786-818
func filterFromKey(key string) *filter {
    if key == "" { return nil }
    return &filter{S3Key: filterRule{Name: "prefix", Value: filterVal{Value: key}}}
}

func filterKey(f *filter) string {
    if f == nil { return "" }
    return f.S3Key.Value.Value
}
```

**但 Webhook 实现完全忽略 filter：**

```go
// internal/events/webhook.go:55-95
func (w *Webhook) Run(ctx context.Context, sub <-chan repository.Event) {
    for {
        select {
        case <-ctx.Done(): return
        case e, ok := <-sub:
            if !ok { return }
            w.deliver(ctx, e)  // ← 对所有事件无差别分发
        }
    }
}
```

**Webhook 无过滤逻辑：** `grep -rn "FilterKey\|filter\|NotificationRule" internal/events/webhook.go` → 零命中。

### 架构权衡与建议方案

#### 方案：Webhook 订阅时支持 FilterKey

```go
type Webhook struct {
    urls   []webhookTarget // 每个 URL 可携带独立的过滤规则
    // ...
}

type webhookTarget struct {
    URL       string
    FilterKey string // "" = 全部事件
    secret    []byte
}

func NewWebhook(urls string, rules []NotificationRule, logger *slog.Logger) *Webhook {
    // 从 NotificationRule 解析 filter 并绑定到对应的 URL
}
```

**过滤逻辑：**

```go
func (w *Webhook) shouldDeliver(e repository.Event, target webhookTarget) bool {
    if target.FilterKey == "" {
        return true // 无过滤 = 全部转发
    }
    // FilterKey 格式：prefix:logs/ 或 suffix:.jpg
    parts := strings.SplitN(target.FilterKey, ":", 2)
    if len(parts) != 2 {
        return true
    }
    switch parts[0] {
    case "prefix":
        return strings.HasPrefix(e.Key, parts[1])
    case "suffix":
        return strings.HasSuffix(e.Key, parts[1])
    }
    return true
}
```

#### 配置方式

```yaml
# 当前：EVENTS_WEBHOOK_URL=https://hook.example.com/events
# 扩展后（环境变量可沿用但不支持过滤）：
# 通过 REST API PUT /v1/buckets/{bucket}/notification 配置
```

#### 边界情况

| 场景 | 处理 |
|------|------|
| 多个 filter 规则（同一 URL 多个前缀） | 任一匹配即转发（OR 语义） |
| 前缀过滤 + 后缀过滤同时 | AND 语义：同时满足前缀和后缀才转发 |
| 事件 key 为 ""（桶级别事件） | 空 key 默认不过滤（透传） |
| filter 更新 | 需重启 webhook 或热加载订阅；可通过 `NotificationRule` 变更事件触发重载 |
| 向后兼容 | 无 filter 的配置 = 转发所有事件（与当前行为相同） |

---

## 附录：全表对比 — 本文方向 vs 前 109 份分析

| 方向 | 前 109 份中提及 | 前分析覆盖程度 | 本文新增 |
|------|----------------|---------------|---------|
| 1. UploadPartCopy / Storage.Copy | v109 方向一"零拷贝复制"提及 `Storage.Copy` 方案，但**未涉及 `UploadPartCopy` 及 >5GB 断裂** | 高层面讨论，缺少 S3 5GB 限制的断裂分析和实现路径 | 首次指出 S3 PutObject 5GB 限制导致的硬断裂；给出了 UploadPartCopy 方案和边界场景枚举 |
| 2. Per-Object TTL | v11 方向四"对象级软 TTL"讨论过概念，但**无代码级证据** | 抽象讨论，未验证代码 | 首次定位 Object 模型无 `ExpiresAt`、S3 handler 忽略 `x-amz-expires`、Reconcile 无对象级过期处理的完整证据链 |
| 3. Metadata 服务端过滤 | **未被任何前分析深度覆盖** | 零 | 全新方向 |
| 4. NoncurrentVersion Expiration | v95 方向三"版本管理"提及版本保留策略，但**未分析 Reconcile/Lifecycle 代码** | 概念提及，无代码锚点 | 首次定位 `sweepExpired` 不处理版本、SQL 查询只有 `deleted_at IS NULL`、版本化桶无清理逻辑 |
| 5. Event Notification Filter | v93 方向五"事件驱动扩展能力"提及通知过滤，但**未分析 webhook.go 代码** | 产品级讨论，无代码证据 | 首次定位 `FilterKey` 字段存在但 webhook 完全忽略、S3 handler 能解析 filter 但事件通道断裂 |

