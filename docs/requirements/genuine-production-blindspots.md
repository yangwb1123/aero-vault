# 高价值扩展方向：异步管线追踪断裂、元数据灾难恢复、数据面审计、写时存储类路由

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — `cmd/server/main.go` + `internal/` 全部子包（237+ Go 源文件，50 对迁移文件），3 套 SDK（Go/Python/JS），MCP 双模式（HTTP+stdio），Web UI，`deploy/` 全套配置（Helm/Grafana/Prometheus/OTel），`HARNESS.md`，`AGENTS.md`，`ROADMAP.md`  
> **去重验证：** 对 `docs/requirements/` 下全部 105 份既有分析文档逐方向进行关键词正则 + 语义交叉验证 + 代码锚点反查  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。选取**代码中存在明确实现锚点、有实质性生产运营影响、且在 105 轮分析中未被独立深度覆盖**的方向。每个方向包含：产品价值 → 现状与代码证据 → 架构权衡与边界情况。

---

## 方法论

本次分析基于三个筛选条件：

1. **代码中存在具体锚点**（配置字段、接口签名、数据模型字段、已暴露的 API 端点），而非纯概念构想。
2. **断裂发生在运行时的管线中**——功能看起来"存在"（字段被持久化、接口被定义、事件被生产），但没有人消费或执行。
3. **断裂在生产运营场景下有可量化的影响**——数据不可恢复、合规风险、运维盲区、成本泄漏。

### 本扫描的四大方向

| # | 方向 | 性质 | 核心发现 |
|---|------|------|---------|
| **1** | **异步管线请求追踪断裂**——事件总线已传播 RequestID，但所有异步 Worker 静默丢弃它 | 运维盲区/可观测性 | `repository.Event.RequestID` 字段被 `Publish` 填充，但 Indexer、Replication Worker、Antivirus Worker、Reconcile 全部不提取也不传递这个值。用户无法追踪"我的上传是否完成了索引/复制？" |
| **2** | **元数据灾难恢复缺失**——数据库丢失即永久丢失所有元数据，存储后端中的 Blob 无法恢复其元数据上下文 | 灾难恢复/数据安全 | 所有对象元数据（版本信息、标签、ACL、生命周期配置）仅存在 DB 中。无 `storage.Storage.List` 扫描重建元数据的机制。版本化存储的 `@v<id>` 后缀无法反向解析出版本归属 |
| **3** | **数据面访问审计缺失**——谁在什么时候读取了哪个文件，系统无记录 | 合规/安全 | `audit_log` 仅覆盖管理操作；`object_events` 仅记录生命周期事件；GET/HEAD 路径不会产生任何持久化的"谁访问了什么"记录。GDPR 的数据主体访问请求（DSAR）无法满足 |
| **4** | **写时存储类路由缺失**——`StorageClass` 字段被持久化但从未用于决定哪个后端存储该对象 | 成本/架构 | `repository.Object.StorageClass` 和 `service.DefaultStorageClass` 存在但仅做元数据记录；所有对象进入同一个 `storage.Storage` 后端，无法实现热数据在本地、冷数据在 S3 的分层路由 |

---

## 方向一：异步管线请求追踪断裂

### 现状与代码证据

系统已有 `RequestID` 基础设施：

```go
// internal/middleware/middleware.go — RequestID middleware
// 为每个 HTTP 请求分配一个唯一 RequestID，存入 context 和响应头 X-Request-ID
```

该 ID 被正确传播到事件系统：

```go
// internal/service/file.go:223-235 — emit 方法
func (s *FileService) emit(ctx context.Context, o repository.Object, t repository.EventType) {
    e := repository.Event{
        RequestID: middleware.RequestIDFrom(ctx),  // ← RequestID 被放入事件
        // ...
    }
    s.sink.Publish(ctx, e)
}
```

事件结构定义了 `RequestID` 字段：

```go
// internal/repository/repository.go:192-200
type Event struct {
    RequestID string  // ← 存在
    // ...
}
```

**但是，消费事件的异步 Worker 全部忽略这个 RequestID：**

**Indexer（`internal/ai/indexer.go`）：**

```go
func (ix *Indexer) handle(ctx context.Context, e repository.Event) {
    // e.RequestID 被静默无视
    switch e.Type {
    case repository.EventCreated:
        ix.dispatch(ctx, JobIndexObject, e.TenantID, *e.ObjectID,
            func() error { return ix.IndexObjectByID(ctx, *e.ObjectID) })
    }
}
```

`IndexObjectByID` 签名是 `func (ix *Indexer) IndexObjectByID(ctx context.Context, objectID int64) error`——`ctx` 是传入的 main context，不包含 RequestID，日志行无法关联回触发请求。

**Replication Worker（`internal/replication/replication.go`）：**

```go
func (w *Worker) Run(ctx context.Context, sub <-chan repository.Event) {
    for e := range sub {
        // e.RequestID 从未被使用
        w.ReplicateObjectByID(ctx, id)  // ctx 中无 RequestID
    }
}
```

**Antivirus Worker（`internal/antivirus/worker.go`）：**

```go
func (w *Worker) Run(ctx context.Context, sub <-chan repository.Event) {
    for e := range sub {
        // e.RequestID 从未被使用
        w.ScanObjectByID(ctx, id)  // ctx 中无 RequestID
    }
}
```

**Webhook（唯一例外——`internal/events/webhook.go`）：**

```go
func (w *Webhook) Run(...) {
    payload := map[string]any{
        "request_id": e.RequestID,  // ← 唯一一个使用 RequestID 的消费者
    }
}
```

### 为什么需要

| 场景 | 影响 |
|------|------|
| **用户上传后搜索不到** | 用户 PUT 一个文件后调用 `/v1/search` 没有结果。运维无法判断是索引尚未完成还是索引失败——因为无法将上传请求与索引操作关联 |
| **复制延迟排查** | 复制 Worker 报告失败，但不知道是哪个用户请求触发的复制、无法回传错误给原始请求 |
| **审计归因** | 审计日志只记录"索引器处理了对象 42"——但无法追溯到触发该索引的用户请求 |
| **SLA 衡量** | 无法测量端到端延迟（从 PUT 到异步操作完成），因为两端没有共享标识符 |

### 架构权衡

**核心问题：** `ctx` 以 `context.Context` 形式在异步边界上传播，但 Event 到达 Worker 时使用的是 `Run` 传入的 main context，而非携带原始请求上下文。

**建议方案链路：**

```
HTTP Request (RequestID = "abc-123")
  → FileService.Put()
    → emit(e{RequestID: "abc-123"})
      → Bus.Publish() 持久化事件
        → Worker 收到 e (RequestID 在事件中，不在 ctx 中)
          → 提取 e.RequestID 注入子 ctx
            → IndexObjectByID(ctxWithRequestID, objID)
              → 日志包含 "request_id=abc-123"
```

具体改动：

1. 在每个 Worker 的 `Run` 循环中，消费事件后提取 `e.RequestID` 并注入到 `context.WithValue(ctx, requestIDKey, e.RequestID)`。
2. 将 RequestID 向下传递到所有子调用（job handler、repository call、日志）。
3. 在 Worker 的日志行中统一输出 `request_id` 字段。

**边界情况：**

- **Backlog 事件（重启后重放）：** 从 `NextUnconsumedEvents` 拉取的事件中 `RequestID` 可能在原始请求完成数小时后已无意义。但携带它仍有助于关联——可额外增加 `event_id` 和 `created_at` 日志字段。
- **Job Queue 路径：** 当 Indexer 将任务委托给 job pool（`Queue.Enqueue`）时，`Job` 结构体需要扩展以携带 `RequestID`，使得最终执行时仍可追溯。
- **telemetry 关联：** 如果后续引入 OpenTelemetry tracing（v95 方向四），`RequestID` 应该与 `trace_id` 做映射，使得你能通过 RequestID 找到 trace，也能通过 trace 找到 RequestID。

---

## 方向二：元数据灾难恢复缺失

### 现状与代码证据

当前系统架构中，**元数据与 Blob 分离存储**：

```
存储后端 (Storage)          ← 对象内容
  └─ local / s3 / oss / cos
  └─ 存储 key = path.Join(tenant, bucket, key) [+ @v<versionID>]

元数据库 (Repository)       ← 对象元数据
  └─ SQLite / Postgres
  └─ objects 表: 版本、标签、ACL、存储类、锁、所有者
  └─ buckets 表: 配置、生命周期、CORS、通知规则
  └─ uploads 表: 分段上传状态
```

元数据可以从存储 Blob 中部分重建，但**当前没有任何工具或流程**实现这一点。

核心代码证据：

```go
// internal/repository/sql_objects.go — 所有元数据查询依赖 objects 表
func (s *sqlStore) GetObject(ctx context.Context, tenant, bucket, key string) (Object, error) {
    // SELECT ... FROM objects WHERE tenant=$1 AND bucket=$2 AND key=$3 AND deleted_at IS NULL
    // 如果 objects 表不存在或损坏，这条语句不可恢复
}

// internal/repository/repository.go:67-82 — Object 结构体
type Object struct {
    ID           int64              // ← DB 自增 ID，存储无法重建
    VersionID    string             // ← 仅存 DB 中，storageKey 前缀带 @v<id>
    StorageKey   string             // ← 这是存储中 blob 的路径
    Tags         map[string]string  // ← 仅存 DB 中
    LockedUntil  *time.Time         // ← 仅存 DB 中
    StorageClass string             // ← 仅存 DB 中
    // ...
}
```

存储 key 的构造规则：

```go
// internal/service/file.go:148-153
func storageKey(tenant, bucket, key string) string {
    return path.Join(tenant, bucket, key)
}

// versioned: storageKey(tenant, bucket, key) + "@v" + versionID
```

这意味着：如果你知道存储 key 的构造规则，**可以从存储后端列出所有 blob 并推测其元数据**。但：

1. **版本化对象：** `@v<versionID>` 后缀包含 versionID，但无法判断哪个版本是"当前版本"（最新）。
2. **标签、ACL、锁定状态：** 完全不存在于 blob 内容或路径中，仅存 DB。
3. **上传状态：** `uploads` 表记录分片上传的中间状态，无法从存储后端推断。
4. **删除标记：** 软删除对象在存储中仍有 blob，但 DB 标记为 `deleted_at IS NOT NULL`——重建时无法区分"软删除"和"活跃"。
5. **空目录标记：** 文件夹标记对象（`application/x-directory`）与普通文件在存储层无区别。

### 为什么需要

| 场景 | 影响 |
|------|------|
| **SQLite 文件损坏** | 单文件部署场合，SQLite 文件因磁盘故障或异常关机损坏——所有对象"消失"，但 `./var/objects/` 目录下所有 blob 完好无损 |
| **Postgres 灾难性丢失** | Postgres 实例被误删除或需要跨区域迁移——元数据不可能在业务可接受的时间内从备份恢复（如果备份存在的话） |
| **跨环境迁移** | 从 staging 复制到 production——需要有从存储 blob 重建元数据的能力，而不是依赖 DB dump |
| **存储完整性验证** | 安全审计需要验证 DB 中的元数据是否与存储中的 blob 一致——当前无交叉验证工具 |

### 架构权衡

**建议方案分级：**

| 层级 | 方案 | 复杂度 | 重建精度 |
|------|------|--------|---------|
| **L1** | `aero-vault recover metadata --scan` CLI 工具，遍历存储后端 `List` 结果，推测性地重建 objects 表 | 低 | 仅重建活跃非版本化对象的基本字段（key/size/etag/content_type） |
| **L2** | L1 + 读取对象的 `_aero_content_md5` 元数据（如果存储后端支持对象元数据，如 S3 的 user-metadata），回填校验和信息 | 中 | 增加 Content-MD5 恢复 |
| **L3** | 元数据级联冗余：在 storage key 旁边写一个侧车 `.meta.json` 文件，包含 tags/ACL/lock/version 信息；DB 丢失时从侧车重建 | 高 | 全部功能恢复 |

**关键设计决策：**

- **只读重建 vs 恢复服务：** L1/L2 重建后的 objects 表可作为"只读快照"——对象可 GET 但不可写入（因为重建的 ID 可能与原 ID 不同，影响引用完整性）。L3 可以做到完全恢复。
- **版本化处理：** 重建时的核心问题——遍历 `@v<id>` 后缀的 blob，但无法判断哪个是"最新版"。可策略选择"时间戳最晚的为最新版"或"全部重建为独立对象"。
- **`uploads` 表重建不可行：** 分片上传的中间状态（已上传哪些分片）不存在于存储后端，重建不可行。建议仅重建 `objects` 和 `buckets` 表。

**边界情况：**

- **并发重建与写入冲突：** 重建过程中如果有新 PUT 请求写入 DB，重建将丢失这些对象或产生主键冲突。重建应当停服或使用"INSERT OR IGNORE"策略。
- **存储后端不保证 List 完整：** S3 `ListObjectsV2` 在大量对象下需要多次调用，OSS/COS 同理。重建工具应当实现完整的分页遍历并合理限速。
- **加密影响：** 如果 SSE 加密启用，重建后的对象无法解密——因为加密密钥也可能在 DB 中（或依赖于 keyfile）。需要在重建时正确处理加密上下文。
- **Object Lock 无法恢复：** `locked_until` 时间戳不存在于存储侧，也无法从任何存储元数据中推断。重建后的对象不会携带锁状态——这是一个安全权衡：宁可让锁丢失（对象可被删除）也不应该让锁永久锁死对象。

---

## 方向三：数据面访问审计缺失

### 现状与代码证据

系统已有两层审计能力，但都**不覆盖数据面读操作**：

**1. 管理面审计日志（`audit_log` 表）：**

```go
// internal/api/rest/admin.go — admin 端点自动写 audit_log
// POST /v1/admin/tenants         → audit_log 行
// PUT /v1/admin/tenants/{t}/quota → audit_log 行
// POST /v1/admin/keys             → audit_log 行
// 但 GET /v1/files/*              → 无 audit_log 行
```

**2. 事件系统（`object_events` 表）：**

```go
// internal/service/file.go:emit
// 只有 created / deleted / accessed 类型
// accessed 事件在 GET 路径发出，但:
//   - Indexer 将 accessed 事件标记为 no-op（internal/ai/indexer.go:167）
//   - accessed 事件没有记录"谁"（身份信息）——只有 RequestID
//   - accessed 事件不是持久化审计目标，而是用于实时 SSE 流
```

核心缺口代码锚点：

```go
// internal/service/file_crud.go:235-243 — Get 路径
func (s *FileService) Get(ctx context.Context, tenant, bucket, key string) (io.ReadCloser, Object, error) {
    // ...
    rc, _, err := s.store.Get(ctx, obj.StorageKey)
    // ...
    s.emit(ctx, obj, repository.EventAccessed)  // ← 没有记录请求者身份
    return rc, obj, nil
}
```

`emit` 方法只记录 RequestID——不是哪个用户/API key：

```go
// internal/service/file.go:223-235
func (s *FileService) emit(ctx context.Context, o Object, t EventType) {
    e := Event{
        RequestID: middleware.RequestIDFrom(ctx),  // 唯一身份标识
        // 没有 tenant 身份之外的用户信息
    }
}
```

当前 `event:accessed` 事件的消费者：

```go
// internal/ai/indexer.go:165-167
case repository.EventAccessed:
    // no-op (used only for audit)
```

注释说"for audit"但实际 no-op——accessed 事件既不持久化处理也不触发任何审计逻辑。

**协议层的 GET 路径同样不生成持久审计记录：**

- REST `GET /v1/files/*` → `h.Get` → `svc.Get` → `emit(accessed)` → Event 持久的表但无身份信息
- S3 `GET /{bucket}/{key}` → `s3compat handler` → `svc.Get` → 同上
- WebDAV `GET` → `dav.Handler` → `svc.Get` → 同上
- MCP `read_file` → `toolReadFile` → `svc.Get` → `emit(accessed)` → 同上

### 为什么需要

| 合规框架 | 要求 | 当前能力 |
|---------|------|---------|
| **GDPR** 第 15 条——数据主体访问权 | 数据控制者应提供"谁在何时访问了数据主体的个人数据"的记录 | ❌ 仅知道数据被访问过（accessed 事件），但不知道"谁" |
| **SOC 2**——CC6.1 逻辑访问安全 | 应记录对敏感系统的访问，包括读取操作 | ❌ 仅记录写入和删除，不记录读取 |
| **HIPAA**——164.312(b) 审计控制 | 必须记录对受保护健康信息的访问 | ❌ 无法满足 |
| **企业数据泄露调查** | 安全团队需要回答"这个文件在泄露时间窗口内被哪些 IP/用户访问过" | ❌ 无法回答 |

### 架构权衡

**核心问题：** `event:accessed` 事件缺少请求者身份信息（用户/API key 标识、IP 地址、认证方式），并且不被任何持久化消费者处理。

**建议方案：**

1. **在 Event 中增加身份字段：**

```go
type Event struct {
    // ... 现有字段
    RequestID    string
    // 新增:
    RequesterID  string // API key 哈希、JWT sub 或 "anonymous"
    RequesterIP  string // 请求来源 IP
    AuthMethod   string // "jwt" | "apikey" | "sigv4" | "anonymous"
}
```

2. **在 `emit` 调用时从 context 提取身份信息：**

```go
func (s *FileService) emit(ctx context.Context, o Object, t EventType) {
    e := Event{
        RequestID:    middleware.RequestIDFrom(ctx),
        RequesterID:  middleware.RequesterFrom(ctx),    // 新 middleware 提取
        RequesterIP:  middleware.ClientIPFrom(ctx),      // 新 middleware 提取
        AuthMethod:   middleware.AuthMethodFrom(ctx),    // 新 middleware 提取
    }
}
```

3. **引入 `event:read` 持久化消费者：**

当前 `accessed` 事件是 no-op。增加一个轻量消费者：

```go
// 新增: AccessAuditWorker
type AccessAuditWorker struct {
    repo repository.Repository
}

func (w *AccessAuditWorker) Run(ctx context.Context, sub <-chan repository.Event) {
    for e := range sub {
        if e.Type != repository.EventAccessed {
            continue
        }
        w.repo.InsertAccessLog(ctx, AccessLogEntry{
            EventID:       e.ID,
            TenantID:      e.TenantID,
            Bucket:        e.Bucket,
            Key:           e.Key,
            ObjectID:      e.ObjectID,
            RequesterID:   e.RequesterID,
            RequesterIP:   e.RequesterIP,
            AuthMethod:    e.AuthMethod,
            AccessedAt:    time.Now(),
        })
    }
}
```

注意：这一步不需要新建 migration——可在现有 `object_events` 表上增强，或新建轻量的 `access_log` 表。

**边界情况：**

- **批量操作放大：** `BatchDelete` 或 `ListObjects`（分页列出 1000 个对象）可能产生 1000 个 accessed 事件。需要聚合或采样——"用户 X 在时间 T 请求了 prefix `foo/` 的列表" 作为一个日志条目，而不是每个对象一行。
- **S3 预签名 URL 访问：** 预签名 URL 的 GET 请求通过 SigV4 验证，但预签名 URL 的有效负载中没有"用户身份"——只有 signed header 中的 AccessKey。当预签名 URL 被分享给第三方时，实际访问者身份不可知。日志应记录"AccessKey X 生成的预签名 URL 在时间 T 被使用"。
- **匿名公读：** 当 `AnonymousPublicRead` 启用时，GET 请求不经过 Auth。日志应记录 `requester: "anonymous"` 和来源 IP。
- **日志保留策略：** 访问日志增长速度远快于管理审计日志（GET 远多于 PUT/DELETE）。需要可配置的保留策略——"保留 90 天后归档/删除"，避免磁盘被审计数据填满。

---

## 方向四：写时存储类路由缺失

### 现状与代码证据

系统中存在完整的存储类基础设施，但从未用于存储后端选择。

**模型层已经定义了 StorageClass：**

```go
// internal/repository/repository.go:79
type Object struct {
    StorageClass string  // e.g. STANDARD, STANDARD_IA, GLACIER; "" = STANDARD
}
```

**配置层持久化默认存储类：**

```go
// internal/service/file.go:52-57
var DefaultStorageClass = "STANDARD"

func WithDefaultStorageClass(sc string) {
    if sc != "" {
        DefaultStorageClass = sc
    }
}

func StorageClassOrDefault(sc string) string {
    if sc == "" {
        return DefaultStorageClass
    }
    return sc
}
```

**协议层解析存储类 Headers：**

```go
// REST handler
obj, err := h.svc.Put(..., service.PutOptions{
    StorageClass: r.Header.Get("x-amz-storage-class"),  // 从请求头解析
})

// S3 Compat handler（internal/api/s3compat/handler.go）
// x-amz-storage-class header 被完整解析和透传
```

**PUT 路径将 storage_class 持久化到 objects 表：**

```go
// internal/service/file_crud.go:178-195
func (s *FileService) buildPutObject(...) repository.Object {
    obj := repository.Object{
        StorageClass: StorageClassOrDefault(opts.StorageClass),  // 被记录
    }
}

// internal/repository/sql_objects.go — UpsertObject
// INSERT INTO objects (..., storage_class, ...) VALUES (..., $12, ...)
```

**存储后端工厂已支持多个后端类型：**

```go
// internal/storage/factory.go
// 支持: local, s3, oss, cos
```

**但是，`storage_class` 的值从未影响使用哪个后端实例：**

```go
// internal/service/file.go:60-63
type FileService struct {
    store  storage.Storage  // ← 单后端，无论存储类是什么
    repo   repository.Repository
}
```

```go
// cmd/server/main.go:113-115
func buildStorage(ctx context.Context, cfg *config.Config) (storage.Storage, error) {
    return buildStorageFrom(ctx, cfg.Storage)  // 单一后端构造
}
```

换言之：

```
PUT /v1/files/doc.md  x-amz-storage-class: STANDARD
  → store.Put(ctx, key, ...)    // 后端: local /var/objects

PUT /v1/files/archive.log  x-amz-storage-class: GLACIER
  → store.Put(ctx, key, ...)    // 同一个后端: local /var/objects
```

`GLACIER` 被善意地记录在 DB 中，但对象内容仍然存储在本地磁盘，从未被移到 S3 Glacier。

### 为什么需要

| 场景 | 当前状态 | 目标状态 |
|------|---------|---------|
| **日志文件分层**：最近 7 天日志频繁查询（STANDARD → 本地 NVMe），30 天后低频查询（STANDARD_IA → S3 Standard），90 天后归档（GLACIER → S3 Glacier Deep Archive） | 所有日志在本地，即使 90 天前的仍占用高性能存储 | 根据存储类自动路由到成本最优的后端 |
| **媒体文件**：热内容（最近发布的视频）在本地 SSD，冷内容（2 年前的视频）在 S3 Standard | 全部在本地，SSD 快速填满 | 写时根据 header 选择后端 |
| **合规归档**：财务记录必须写入选定区域的 S3，且必须使用特定加密密钥 | 无法实现按对象选择存储位置 | StorageClass 映射到后端 + 加密策略 |

### 架构权衡

**核心限制：** `FileService.store` 是单一 `storage.Storage` 接口实例。要让存储类影响后端选择，有两种路径：

**方案 A：写时分发路由（推荐首期实现）**

```
FileService
  └─ storageRouter (新抽象)
       ├─ STANDARD    → localStore (NVMe)
       ├─ STANDARD_IA → s3Store (S3 Standard)
       └─ GLACIER     → s3Store (S3 Glacier, 使用 S3 StorageClass)
```

```go
type storageRouter struct {
    backends map[string]storage.Storage
    defaultBackend string  // "STANDARD"
}

func (r *storageRouter) Put(ctx context.Context, key string, ..., opts PutOptions, storageClass string) (ObjectInfo, error) {
    backend := r.backends[storageClass]
    if backend == nil {
        backend = r.backends[r.defaultBackend]
    }
    return backend.Put(ctx, key, ..., opts)
}
```

**方案 B：后端能力契约（v92 方向五的前置——需要先实现）**

每个后端声明支持的 storage class 列表和成本特征，路由根据契约智能选择。

```go
type StorageCapability struct {
    SupportedClasses []string  // ["STANDARD", "STANDARD_IA"]
    Location         string    // "us-east-1"
    Encryption       bool
    MaxObjectSize    int64
    MinStorageDays   int       // 最低存储天数（GLACIER 最少 90 天）
}
```

**推荐：首期实现方案 A（简单写时分发），二期引入方案 B（能力契约路由）。**

**关键设计决策：**

1. **映射规则在哪里定义？** 配置层最合适——让运维声明"GLACIER → s3-backend"的映射关系。
2. **同一后端多存储类支持？** S3 后端在上传时设置 `x-amz-storage-class` header 即可——不需要多个后端实例。
3. **读操作的路由？** GET 时，FileService 需要根据对象的 `StorageKey` + 记录的 backend 信息选择正确的后端读取。当前 `repository.Object.Backend` 字段可以在写入时记录后端标识。

**边界情况：**

- **存储类变更（已经写入的对象）：** 对象写入后存储类不应更改（S3 语义——`CopyObject` 可以改存储类）。当前设计不支持对象存储类变更后的后端切换，需要引入 CopyObject + DeleteObject 事务（即"迁移"）。
- **GLACIER 对象的读取延迟：** S3 Glacier 对象读取需要 1-12 小时的恢复时间。写入 GLACIER 后立即 GET 应当返回错误（`ObjectNotInStorage` 或 `RestoreInProgress`），而非静默从另一个后端返回其他内容。需要引入恢复状态跟踪——与当前软删除恢复流程正交，不可复用。
- **List 操作跨后端：** `ListObjects` 需要聚合多个后端的对象列表。可以仅在元数据库层面聚合（所有后端写入同一个 DB），因此 List 仍是单 DB 查询。
- **后端不可用降级：** 当 GLACIER 后端（S3）不可用时，新的 GLACIER 写入应当失败并告知用户"存储类后端不可用"，而非静默降级为 STANDARD 写入——那会导致合规归档场景下数据存错位置。

---

## 附录：快速验证检查点

### 方向一（异步管线追踪）
- [ ] 触发一个对象创建后，检查 `object_events` 表——`request_id` 字段是否非空？
- [ ] 检查 Indexer 日志——是否包含 `request_id` 字段？
- [ ] 检查 Replication Worker 日志——是否包含 `request_id` 字段？
- [ ] 启动后，`NextUnconsumedEvents` 返回的历史事件是否有 `request_id`？

### 方向二（元数据灾难恢复）
- [ ] 关闭数据库，重启服务——是否能从存储中恢复？
- [ ] `storage.Storage.List(prefix="", marker="", limit=1000)` 是否返回所有 blob？是——但只有 key，没有 tag/version/ACL/lock
- [ ] 版本化对象的 `@v<id>` 后缀能否反向解析为 version_id 和 is_latest？否
- [ ] 软删除对象的 blob 与活跃对象的 blob 在存储层能否区分？否

### 方向三（数据面审计）
- [ ] 执行 `GET /v1/files/some-file` 后检查 `object_events` 表——type=`accessed` 的行是否包含请求者身份信息？
- [ ] `audit_log` 表是否有 GET 操作的记录？否（仅 admin 操作）
- [ ] 检查 `EventAccessed` 类型的消费者——是否存在非 no-op 的处理逻辑？

### 方向四（写时存储类路由）
- [ ] PUT 一个文件并设置 `x-amz-storage-class: STANDARD_IA`——对象的 `storage_class` 字段是否为 `STANDARD_IA`？是
- [ ] 但该对象的 blob 是否与 STANDARD 对象的 blob 存储在同一个后端？是
- [ ] 如果后端是 local——`./var/objects` 下能否找到该 blob？能
- [ ] 如果后端是 S3——该对象是否使用了 S3 的 STANDARD_IA 存储类？否（S3 SDK 需要额外设置 StorageClass 参数）
