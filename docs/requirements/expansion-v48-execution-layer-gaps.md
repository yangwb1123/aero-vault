# AeroVault 高价值扩展方向 v48 — 执行层行为完整性缺口

> **分析范围：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 全部 237 个 `.go` 文件，~50K 行代码，48 对迁移文件，三套 SDK，`deploy/*`，`docs/*`，`Makefile`）
>
> **分析视角：** 资深架构师 / 产品经理 — 聚焦此前 **47 期 expansion 分析（累计 250+ 方向，~550,000+ 字分析文本）+ `docs/ROADMAP.md`（10 大方向）+ `docs/CHANGELOG.md` + `docs/TODO.md` + `docs/adr/DECISIONS.md` + `extensions*.md`** 中从未实质性触及的执行层行为完整性缺口
>
> **分析日期：** 2026-07-10
>
> **去重验证：** 对 `docs/requirements/` 下全部 47 份既有分析文档（v1–v47）+ `ROADMAP.md` + `CHANGELOG.md` + `TODO.md` + `adr/DECISIONS.md` + `extensions*.md` 进行穷尽式关键词 `grep` 验证。每个方向在既有文档中 **零实质性独立架构分析**（表格一行过路引用、举例提及、单一子点均不构成实质性分析）。

---

## 前言

此前 47 期 expansion 分析覆盖了 250+ 方向，从 AI/RAG 管线到 S3 协议实现纵深、从存储后端到认证授权、从多租户到合规、从可观测性到工程基础设施、从产品成熟度到开发者体验、从生产就绪度到系统性交叉架构缺口。最新三期（v45 交叉架构缺口、v46 产品成熟度、v47 生产就绪度）已触及了大量此前遗漏的连接层和产品层问题。

然而，经过对代码库的最后一轮穷举扫描，以下 **5 个方向** 依然未被任何一期作为独立架构方向分析。它们的共同特征是：**不涉及"新功能"的添加，而是表现为"功能表面已经构建完成，但行为断点处运行时实际执行缺失"——用户可以通过 API 配置功能，后台却没有对应的执行流水线，形成"配置落空"的执行层断裂。**

```
功能维度（前 42 期）：      ❌ 不支持 → ✅ 已实现
执行层维度（v42–v44）：     ✅ 有 CRUD → ✅ 运行时行为完整  
交叉架构维度（v45–v47）：   ✅ 各功能独立正确 → ⚠️ 功能交叉面一致
执行行为完整性（本期 v48）： ✅ API 可配置 → ❌ 后台无对应执行流水线
```

这 5 个方向的共同模式是：

```
用户通过 S3 API 配置 → 配置持久化到 DB → API 返回 200 OK
    ↓                           ↓                     ↓
  ✅ REST handler            ✅ repository           ✅ S3-compat XML
    ↓                           ↓                     ↓
  ❌ 后台 worker 却从未读取这些配置并执行对应操作
```

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心矛盾 | 锚定代码 | 47 期覆盖 |
|---|------|------|--------|---------|---------|-----------|
| 1 | **Per-Bucket 通知规则执行引擎（配置落空一号）** | 兼容性/完整性 | **P1** — 迁移 0024 已创建通知规则 schema，S3 API 完整实现（get/put/delete 全部工作），但没有任何后台 worker 读取这些规则并递送事件；用户在 S3 API 上配置了通知，却永远收不到任何事件 | `internal/repository/migrations/sqlite/0024_bucket_notifications.up.sql`（schema ✅）；`internal/api/s3compat/handler.go:809-841`（putBucketNotifications ✅）；`internal/service/file_features.go:320-340`（SetBucketNotifications ✅）；**⚠️ 没有任何 worker 读取 `NotificationRule` 并执行递送** | ❌ **零覆盖**（`extensions.md` 60 行提及，但非编号 expansion 文档，且聚焦功能设计而非执行层断裂分析） |
| 2 | **S3 Server Access Logging 交付运行时（配置落空二号）** | 兼容性/合规 | **P1** — 迁移 0023 已创建 logging 配置 schema，S3 API 完整实现（get/put/delete 全部工作），但没有任何后台 worker 读取 logging 配置、生成访问日志对象并投递到目标 bucket | `internal/repository/migrations/sqlite/0023_bucket_logging.up.sql`（schema ✅）；`internal/api/s3compat/handler.go:722-767`（get/put/deleteBucketLogging ✅）；`internal/service/file_features.go:285-315`（Get/Set/DeleteBucketLogging ✅）；**⚠️ 没有 Access Log Writer worker** | ❌ **零覆盖**（v3 早期文档 80 行概念性方向，非执行层断裂分析） |
| 3 | **Multipart Upload 并发一致性与冲突模型** | 可靠性/数据完整性 | **P1** — 当前 multipart 上传无任何并发保护：两个客户端可为同一 key 同时发起上传、可上传相同 part number、可同时调用 CompleteMultipart 产生竞态、版本化 bucket 中可在完成前重复分配 version ID | `internal/service/file_multipart.go`（InitMultipart/UploadPart/CompleteMultipart 无并发锁）；`internal/api/s3compat/handler.go`（uploadPart/CompleteMultipart 无冲突检测）；`internal/storage/local_multipart.go`（本地存储无并发保护） | ❌ **零覆盖**（v7 方向表 1 行提及"resumable upload"概念，与 multipart 并发一致性完全无关） |
| 4 | **DeleteBucket 存储 Blob 遗孤与存储空间回收** | 可靠性/资源管理 | **P2** — `DeleteBucket` 级联删除数据库中的对象/分块/上传/事件记录，但**从不删除底层存储 blob**；删除 bucket 后，local 文件系统和 S3/OSS/COS 上残留大量孤立 blob，只能通过低效的 reconcile GC 周期清理 | `internal/repository/sql_buckets.go:69-120`（`DeleteBucket` 仅 SQL DELETE，无 `s.store.Delete`）；`internal/reconcile/job.go`（Reconcile GC 靠扫描全部存储 key 来发现孤儿，粒度粗、延迟高） | ❌ **零覆盖** |
| 5 | **结构化系统健康与运行状态 API** | 运维/可观测性 | **P2** — 当前 `/healthz` 仅检查 DB ping + 存储后端 stat（最简存活探测），/info 返回硬编码版本号；无聚合全部组件（存储后端、后台 worker、SSE 加密、索引器、复制、DB 连接池、集群单例、AI 管线）的结构化 JSON 健康端点 | `cmd/server/main.go:185-199`（`readyzHandler` 仅 DB + Storage）；`/info` 返回 `{"service":"aero-vault","version":"0.1.0"}`；**无 `/v1/admin/health` 或 `/debug/health` 端点** | ⚠️ v47 方向四覆盖后台 worker 健康（/debug/workers），但非全系统健康聚合；v39 方向表 1 行提及"健康端点"，**零实质性架构分析** |

---

## 方向一：Per-Bucket 通知规则执行引擎（配置落空一号）

### 现状

当前系统有一套完整的 S3 兼容通知规则配置管道：

```sql
-- 迁移 0024：buckets 表增加 notification_rules 列
ALTER TABLE buckets ADD COLUMN notification_rules TEXT NOT NULL DEFAULT '[]';
```

S3 API 完整实现：

```go
// internal/api/s3compat/handler.go:809 — putBucketNotifications
func (h *Handler) putBucketNotifications(w http.ResponseWriter, r *http.Request, bucket string) {
    var rules []repository.NotificationRule
    // 解析 S3 XML → rules
    // ...
    _ = h.svc.SetBucketNotifications(r.Context(), tenant, bucket, rules)
    w.WriteHeader(http.StatusOK)
}

// GET /s3/{bucket}?notification → 返回配置的规则
// PUT /s3/{bucket}?notification → 设置规则
// DELETE /s3/{bucket}?notification → 清除规则
```

Repository 方法完整实现：

```go
// internal/service/file_features.go:320-340
func (s *FileService) GetBucketNotifications(...)
func (s *FileService) SetBucketNotifications(...)
func (s *FileService) DeleteBucketNotifications(...)
```

**然而，没有任何运行时组件读取这些规则：**

```
数据流（当前）：
  S3 API (PUT ?notification) 
    → s3compat/handler.go:putBucketNotifications
    → service.SetBucketNotifications
    → repo.SetBucketNotifications (写入 DB)
    → 200 OK
    → [执行结束，事件永不到达]

数据流（期望）：
  EventBus (object.created / object.deleted / ...)
    → NotificationRouter (新 worker)
       → 读取 bucket 的 NotificationRules
       → 按规则过滤 (event type, filter key)
       → 递送到配置的目标 (webhook URL / SNS / SQS / 多 endpoint)
```

| 组件 | 状态 | 行数 |
|------|------|------|
| 通知规则 DB schema | ✅ 迁移 0024 | 2 行 DDL |
| `GetBucketNotifications` | ✅ repository + service + handler | ~30 行 |
| `SetBucketNotifications` | ✅ repository + service + handler | ~40 行 |
| `DeleteBucketNotifications` | ✅ repository + service + handler | ~20 行 |
| **通知路由引擎（新）** | ❌ **不存在** | **~300 行估计** |
| **目标适配器（多 webhook / SNS / SQS）** | ❌ **不存在** | **~200 行估计** |

### 为什么需要

1. **"配置了但不工作"比"不支持"更糟糕。** 用户通过 S3 API（或 AWS SDK）配置了通知规则，API 返回 `200 OK`。他们此后会一直等待事件到达。当他们发现事件永远不会到达时，会认为系统有 bug 且不可信赖。这是比"不支持该 API"严重得多的问题——后者用户能立刻感知并寻找替代方案。

2. **S3 兼容性的核心要求。** AWS S3 的通知功能是事件驱动架构的基石——用户依赖它触发 CI/CD、数据同步、Lambda 函数。AeroVault 的 `extensions.md` 在非常早期的阶段就识别了这个需求，但至今 40+ 轮迭代后仍未实现执行引擎。

3. **当前全局 webhook 的局限。** 全局 `EVENTS_WEBHOOK_URL` 只能将全量事件投递到一个 URL，无法按 bucket 过滤、无需区分事件类型、不支持多目标。对于多租户场景，不同租户的不同 bucket 需要不同的事件路由。

4. **增量工程成本低。** 复用 `EventBus`（`internal/events/bus.go`）的事件流、`webhook_failures` 表的重试逻辑、现有的 `events` 持久化。只需要一个路由层 + 目标适配器。

### 缺失的能力

1. **`NotificationRouter` 工作 goroutine：**

   ```go
   type NotificationRouter struct {
       repo     repository.Repository
       bus      *events.Bus
       sinks    map[string]NotificationSink // "webhook" → WebhookSink, "sns" → SNSSink
       rules    map[string][]repository.NotificationRule // tenant:bucket → rules
       logger   *slog.Logger
   }
   
   func (nr *NotificationRouter) Run(ctx context.Context, sub <-chan repository.Event) {
       // 1. 监听事件
       // 2. 加载相关 bucket 的通知规则（带缓存）
       // 3. 匹配事件类型 + FilterKey
       // 4. 分发到配置的目标
   }
   ```

2. **通知目标适配器接口：**

   ```go
   type NotificationSink interface {
       Name() string
       Deliver(ctx context.Context, rule repository.NotificationRule, event repository.Event) error
   }
   ```

   初始实现应至少支持：
   - **`WebhookSink`** — 复用 `events.Webhook` 的重试逻辑，将事件 POST 到规则中配置的 URL
   - **`MultiWebhookSink`** — 支持规则配置多个 webhook URL（而非全局仅一个）

3. **规则缓存与失效：** 缓存 `tenant:bucket` → `[]NotificationRule` 的映射，当规则变更时通过 `EventBus` 发布规则失效事件。

4. **事件类型过滤：** 规则中的 `Events` 字段（如 `"s3:ObjectCreated:*"`, `"s3:ObjectRemoved:*"`）应在路由层匹配，不匹配的事件直接跳过。

5. **FilterKey 前缀/后缀过滤：** 规则中的 `FilterKey` 字段用于过滤特定前缀/后缀的对象，减少不必要的事件投递。

### 执行层断裂影响

| 场景 | 当前行为 | 期望行为 |
|------|---------|---------|
| 用户配置 PUT ?notification 设置规则 | API 返回 200，规则持久化 | API 返回 200，规则持久化 ✅ |
| 用户创建新对象 | 全局 webhook 触发（如果配置）或静默 | 匹配 bucket 通知规则，投递到配置的目标 |
| 用户删除对象 | 事件写入 events 表 | 匹配规则，投递事件 |
| 用户查询通知配置 | GET ?notification 返回配置 | ✅ 正常工作 |

---

## 方向二：S3 Server Access Logging 交付运行时（配置落空二号）

### 现状

与通知规则完全相同的模式——配置管道完整，运行时执行缺失：

```sql
-- 迁移 0023：buckets 表增加 logging_target 和 logging_prefix 列
ALTER TABLE buckets ADD COLUMN logging_target TEXT NOT NULL DEFAULT '';
ALTER TABLE buckets ADD COLUMN logging_prefix TEXT NOT NULL DEFAULT '';
```

S3 API 完整实现：

```go
// internal/api/s3compat/handler.go:722-767
func (h *Handler) getBucketLogging(...)    // GET ?logging → XML 返回配置
func (h *Handler) putBucketLogging(...)    // PUT ?logging → 保存配置
func (h *Handler) deleteBucketLogging(...) // DELETE ?logging → 清除配置
```

配置通过 `service.SetBucketLogging` → `repo.SetBucketLogging` 持久化到 `buckets` 表。

**运行时执行缺失：**

```
S3 API PUT ?logging (TargetBucket: "logs", TargetPrefix: "access-log/")
  → 200 OK
  → 配置保存在 buckets.logging_target + buckets.logging_prefix
  → [执行结束，永远不会生成任何访问日志对象]
```

### 为什么需要

1. **S3 Server Access Logs 是安全审计的基础。** SOC2、PCI DSS、ISO 27001 等合规框架要求保留详细的访问日志。用户期望启用桶级日志后，能够定期收到格式化的访问日志文件，用于审计分析。

2. **"配置了但不工作"的产品体验问题（同方向一）。** 用户通过 S3 API 配置了日志目标，API 返回成功，但没有日志文件产生——这是产品信任度的直接伤害。

3. **当前 access log middleware 的缺口。** `middleware.AccessLog`（`internal/middleware/middleware.go:74-87`）记录每个请求的访问日志到 `slog` 输出，但没有按 bucket 分发到不同目标、没有标准 S3 访问日志格式、没有写入对象存储。

4. **区分审计日志与访问日志。** 现有 `audit_log` 表记录 admin 操作。S3 Server Access Logs 是另一类——记录所有 GET/HEAD/PUT/DELETE 等数据操作，用于容量规划、使用分析和合规审计。

### 缺失的能力

1. **`AccessLogWriter` 工作 goroutine：**

   ```go
   type AccessLogWriter struct {
       repo       repository.Repository
       svc        *service.FileService
       logger     *slog.Logger
       buffer     map[string][]accessLogEntry // tenant:bucket → buffer
       flushInterval time.Duration
   }
   
   // accessLogEntry 的结构兼容 S3 Server Access Log 格式
   type accessLogEntry struct {
       Bucket      string
       Time        time.Time
       RemoteIP    string
       Requester   string
       RequestID   string
       Operation   string  // REST.GET.OBJECT, REST.PUT.OBJECT, etc.
       Key         string
       HTTPStatus  int
       BytesSent   int64
       ObjectSize  int64
       TotalTime   time.Duration
       TurnAround  time.Duration
       Referer     string
       UserAgent   string
       VersionID   string
       // ... 更多字段
   }
   ```

2. **Access Log 生成路径：** 有两种可行的实现策略：

   | 策略 | 优点 | 缺点 |
   |------|------|------|
   | **在线写入**：每个请求处理完后同步/异步写入日志 buffer，定期 flush 到目标 bucket 的日志对象 | 实时性好；日志粒度精确 | 增加每个请求的 IO 开销 |
   | **离线聚合**：`AccessLog` middleware 的日志输出到文件中继，后台 worker 定期解析、聚合、上传到目标 bucket | 不增加请求路径开销；可批处理压缩 | 实时性差；文件管理复杂 |

   推荐 **混合策略**：在线写入内部 log buffer + 每分钟 flush 到目标 bucket（S3 标准日志格式的 tab-separated 文本）。

3. **标准 S3 Access Log 格式兼容：** 生成与 AWS S3 Server Access Log 兼容的 [Tab 分隔格式](https://docs.aws.amazon.com/AmazonS3/latest/userguide/LogFormat.html)：

   ```
   #Version: 1.0
   #Fields: date time x-edge-location sc-bytes c-ip cs-method cs(Host) cs-uri-stem sc-status cs(Referer) cs(User-Agent) cs-uri-query cs(Cookie) x-edge-result-type x-edge-request-id x-host-header cs-protocol cs-bytes time-taken forwarded-for ssl-protocol ssl-cipher x-edge-response-result-type cs-protocol-version fle-status fle-encrypted c-port time-to-first-byte x-edge-detailed-result-type sc-content-type sc-content-len sc-range-start sc-range-end
   ```

4. **日志对象的生命周期管理：** 日志对象自身应遵循生命周期策略（如 30 天后自动删除），或支持配置日志对象的过期策略。

5. **日志 Buffer 的持久化保护：** 如果进程在 flush 前崩溃，未写入的日志不应丢失。可配合 WAL 或临时文件实现 at-least-once 投递。

### 执行层断裂影响

| 场景 | 当前行为 | 期望行为 |
|------|---------|---------|
| 用户配置日志目标 bucket | ✅ API 200 OK | ✅ API 200 OK |
| 用户执行 GET/PUT/DELETE 操作 | → 仅写入 slog | → 日志写入目标 bucket 的 `TargetPrefix/YYYY-MM-DD-HH-MM-<UUID>.log.gz` |
| 审计员检查访问日志 | 无标准日志文件 | 在目标 bucket 中可找到标准格式的 S3 Server Access Log 文件 |
| 日志文件过大 | 不适用 | 按时间（每 15 分钟）或大小（每 100MB）自动轮转 |

---

## 方向三：Multipart Upload 并发一致性与冲突模型

### 现状

当前 multipart 上传实现（`internal/service/file_multipart.go`）是纯粹的无状态模型——每个操作直接代理到存储后端的对应方法，**没有任何并发控制或冲突检测**：

```go
// internal/service/file_multipart.go

func (s *FileService) InitMultipart(ctx context.Context, tenant, bucket, key string, opts PutOptions) (repository.Upload, error) {
    // ✅ 校验 tenant/bucket/key
    // ✅ 检查版本化 → 预生成 versionID 写入 storage_key
    // ✅ 调用 s.store.InitMultipart
    // ✅ 调用 s.repo.CreateUpload
    // ❌ 不检查是否已存在该 key 的进行中 multipart upload
    return u, nil
}

func (s *FileService) UploadPart(ctx context.Context, uploadID string, partNumber int32, r io.Reader, size int64) (repository.PartRecord, error) {
    // ✅ 查询 upload → 获取 storage_key
    // ✅ 调用 s.store.UploadPart
    // ✅ 调用 s.repo.RecordPart
    // ❌ 不检查 partNumber 是否已被同 upload 的其他 part 占用（最后写入者获胜）
    // ❌ 不检查 upload 是否已被 abort 或 complete（经典 TOCTOU）
    return rec, nil
}

func (s *FileService) CompleteMultipart(ctx context.Context, uploadID string) (repository.Object, error) {
    // ✅ 查询 upload
    // ✅ ListParts → 验证 parts 非空
    // ✅ preflightMultipartQuota
    // ✅ checkMultipartLock
    // ✅ s.store.CompleteMultipart (存储后端合并)
    // ✅ saveMultipartObject → 写入元数据
    // ✅ DeleteUpload
    // ❌ 无并发保护：两个 goroutine 可同时调用 CompleteMultipart 并两次保存对象
    // ❌ 无版本化 bucket 中 version ID 的唯一性保护（两次 Complete 可能生成相同 version_id？
    //    不，version_id 在 InitMultipart 时生成，两次 Complete 会用同一个 storage_key，
    //    第二次 Complete 会覆盖第一次的 blob，但数据库里会 InsertObjectVersion 两次...）
    return saved, nil
}
```

**具体竞态条件分析：**

| 场景 | 代码路径 | 竞争后果 |
|------|---------|---------|
| **同一 key 并发 InitMultipart** | 两个客户端同时调用 InitMultipart("bucket", "key") | 两个 upload 都成功，storage 后端上两个独立 multipart session 共存，部分可以交叉引用 |
| **同一 upload 并发 UploadPart(partNumber=1)** | 两个 goroutine 对同一 upload 调用 UploadPart(1) | part 记录被覆盖（最后写入者获胜），但底层存储有两个 blob 存在且合并时只会用一个 |
| **CompleteMultipart 并发调用** | 两个 goroutine 对同一 upload 调用 CompleteMultipart | 在非版本化 bucket：UpsertObject 两次，第二次覆盖第一次 ✅ 但存储后端合并了两遍可能产生重复 blob；在版本化 bucket：InsertObjectVersion 两次，产生两个版本引用同一 storage_key → **数据损坏** |
| **CompleteMultipart + AbortMultipart 并发** | 一个 goroutine 正在合并 parts，另一个 abort | CompleteMultipart 可能在 abort 后读取已删除的 parts，产生错误 |
| **UploadPart + CompleteMultipart 并发** | 一个 goroutine 上传 part(N)，同时另一个 complete | 可能 complete 时缺少 part(N) → 虽然 ListParts 在 Complete 中调用，但 UploadPart 写入尚未 commit |

**版本化 bucket 中的特定问题：**

```go
// InitMultipart 中为版本化 bucket 预分配 storage_key
sk := storageKey(tenant, bucket, key)
if bcfg.Versioning {
    sk = sk + "@v" + repository.NewVersionID()
}
```

如果两个 goroutine 几乎同时在同一个版本化 bucket 中为同一 key 调用 InitMultipart：
- 两个 upload 都有独立的 upload ID
- 两个 upload 的 `StorageKey` 不同（因为 `NewVersionID()` 生成唯一 ID）
- 当其中一个 CompleteMultipart 完成后，第二个 Complete 在 `InsertObjectVersion` 时创建第二个版本
- **这是符合预期的行为** ✅（版本化 bucket 中每个版本独立）

但问题在 CompleteMultipart 的版本化路径：

```go
func (s *FileService) saveMultipartObject(ctx context.Context, obj repository.Object, bcfg repository.BucketConfig) (repository.Object, error) {
    if bcfg.Versioning {
        saved, err = s.repo.InsertObjectVersion(ctx, obj)
    } else {
        saved, err = s.repo.UpsertObject(ctx, obj)
    }
}
```

在非版本化 bucket 中，如果两次 CompleteMultipart 几乎同时发生：
1. 两个 Complete 都读取了同一个 upload 的 parts（假设 upload 还未被删除）
2. 都能顺利通过 `s.store.CompleteMultipart`（存储后端可能处理幂等）
3. 都调用 `UpsertObject` — 第二次写入覆盖第一次
4. 两个 Complete 都成功返回，但客户端看到的可能不是最终持久化的版本
5. **数据库里只有最终写入的那个版本，但底层存储可能有两个合并后的 blob**

### 为什么需要

1. **数据完整性风险。** 并发 CompleteMultipart 调用在版本化和非版本化 bucket 中的行为不一致。在版本化 bucket 中，两个 CompleteMultipart 可能创建两个版本引用同一个 storage_key 的 blob——这是数据损坏。

2. **AWS S3 的 multipart API 是幂等的。** AWS S3 的 `CompleteMultipartUpload` 在并发调用时保证至少一个成功，且不会产生重复版本。当前实现完全未考虑幂等性。

3. **上传中断恢复的信任基础。** 用户依赖 multipart 上传大文件。如果并发场景中存在竞态条件，用户在网络不佳时重试上传可能与原始上传产生冲突，导致数据不一致。

4. **Orphan blob 积累。** 并发 abort + complete 可能留下存储后端的孤儿 blob（存储在 S3 的 orphan part），产生存储成本浪费。

### 缺失的能力

1. **InitMultipart 的 key 级并发检测：** 在同一 key 上有进行中的 multipart upload 时，是否允许第二个 InitMultipart？有以下选择：

   | 策略 | 行为 | 适用场景 |
   |------|------|---------|
   | **允许共存**（当前行为） | 同一 key 可以有多个进行中的 upload | AWS S3 兼容行为 |
   | **拒绝** | 返回 `409 Conflict` | 需要串行化上传的场景（不推荐） |

   AWS S3 允许共存。所以当前行为可以保留，**但需要文档化**。

2. **CompleteMultipart 的幂等性与防重入：**

   ```go
   func (s *FileService) CompleteMultipart(ctx context.Context, uploadID string) (repository.Object, error) {
       // 1. 原子地标记 upload 为 "completing" 状态
       //    使用 UPDATE uploads SET status='completing' WHERE id=$1 AND status='in_progress'
       //    如果影响行数为 0，说明已有其他 Complete 进行中或已 complete
       // 2. 执行合并操作
       // 3. 写入对象元数据
       // 4. 标记 upload 为 "completed"
   }
   ```

3. **UploadPart 的 TOCTOU 保护：** UploadPart 前检查 upload 状态是否仍为 `in_progress`，防止在 abort 后继续上传。

4. **非版本化 bucket 中 CompleteMultipart 的 Upsert 冲突处理：** 使用乐观锁（比较 `updated_at` 或版本号）防止两次 Complete 意外覆盖。

5. **Part 冲突检测：** 当 UploadPart(partNumber=1) 被调用两次时，可以选择：
   - **拒绝重复**（返回 `409 Conflict`）— 严格模式
   - **最后写入者获胜**（当前行为）— 宽松模式，但需文档化
   - 推荐：存储后端（如 local）应覆盖旧 part，行为等价于最后写入者获胜

6. **上传状态迁移图：** 显式文档化 upload 状态机：

   ```
   in_progress → completing → completed
       ↓                          ↑
       ↓                          ↑
       ↓ (abort)                  ↑
       ↓                          ↑
   aborted ───────────────────────┘ (已完成上传基础上调用 abort → 忽略)
   ```

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **并发 CompleteMultipart（非版本化 bucket）** | 使用原子状态标记防重入。首个进入的 Complete 将 upload 标记为 `completing`，第二个 Complete 看到 `completing` 或 `completed` 后返回上一个 Complete 的结果或报错 |
| **并发 CompleteMultipart（版本化 bucket）** | 同样防重入。额外保护：验证 storage_key 尚未被任何 object 版本使用 |
| **UploadPart 在 Complete 进行中到来** | 此时 upload 状态为 `completing`，UploadPart 应返回 `409 Conflict` |
| **AbortMultipart 与 CompleteMultipart 并发** | 两者都需要原子地获取 upload 状态。Abort 只能取消 `in_progress` 状态的 upload |
| **Part 列表一致性** | CompleteMultipart 在读取 ListParts 时应当在一个事务中完成，防止 UploadPart 和 CompleteMultipart 的读取不一致 |
| **Underyling 存储后端的幂等性** | S3 CompleteMultipartUpload 是幂等的（多次调用返回相同结果）；local 存储也需要实现类似保证 |

---

## 方向四：DeleteBucket 存储 Blob 遗孤与存储空间回收

### 现状

当前 `DeleteBucket` 实现的完整路径：

```go
// internal/repository/sql_buckets.go:69-120
func (s *sqlStore) DeleteBucket(ctx context.Context, tenant, bucket string) error {
    // 1. 校验 bucket 存在
    // 2. 事务内：
    //    a. DELETE FROM uploads WHERE tenant=? AND bucket=?
    //    b. DELETE FROM parts WHERE upload_id NOT IN (SELECT id FROM uploads)
    //    c. DELETE FROM chunks WHERE object_id IN (SELECT id FROM objects WHERE ...)
    //    d. DELETE FROM objects WHERE tenant=? AND bucket=?
    //    e. DELETE FROM events WHERE tenant=? AND bucket=?
    //    f. DELETE FROM buckets WHERE tenant=? AND name=?
    // 3. Commit
    
    // ❌ 没有任何 s.store.Delete 调用
    // ❌ 没有任何存储 blob 回收
}
```

**Blob 遗孤范围：**

| 存储内容 | DB 记录 | 存储 blob | DB 删除后状态 |
|---------|---------|-----------|-------------|
| 正常对象 | `objects` 行 ✅ 删除 | `var/objects/...` blob ❌ 保留 | 孤立的 blob |
| 版本化对象（旧版本） | `objects` 行 ✅ 删除 | `...@v<id>` blob ❌ 保留 | 孤立的 blob |
| Multipart 上传 parts | `uploads` + `parts` ✅ 删除 | parts 目录 / S3 parts ❌ 保留 | 孤立的 parts |
| SSE 加密对象 | `objects` 行 ✅ 删除 | 加密 blob + .meta.json ❌ 保留 | 孤立的加密 blob |
| 软删除对象 | `objects` 行（deleted_at 非空） ✅ 删除 | blob ❌ 保留 | 孤立的 blob |

当前只能依赖 `Reconcile` GC（`internal/reconcile/job.go`）来清理这些孤儿。但 Reconcile GC 的工作方式是：

```go
// reconcile/job.go — 扫描存储后端，对比 DB
// 1. 遍历存储后端的所有 key
// 2. 检查每个 key 在 DB 中是否存在对应记录
// 3. 如果 DB 中不存在，删除 blob
```

这个方式的局限：
- **扫描效率低**：需要遍历整个存储命名空间（对于 S3，`ListObjectsV2` 可能返回数千页）
- **延迟高**：默认 reconcilation 间隔是分钟级（`RECONCILE_INTERVAL_MINUTES`），在两次 GC 之间 blob 一直占用空间
- **租户级粒度**：如果只有单个 bucket 被删除，GC 扫描的是整个租户的存储前缀

### 为什么需要

1. **存储泄漏。** 用户删除 bucket 以为数据已被完全清除，但实际上存储占用并未释放。在云存储后端（S3、OSS、COS）上，这意味着持续产生存储费用。在 local 后端上，这意味着磁盘空间被无用数据占用。

2. **合规风险。** `DeleteBucket` 被调用时用户期望**所有数据被彻底清除**。如果 blob 仍然存在于存储后端，在合规审计场景下这可能是个问题（特别是涉及 GDPR "被遗忘权"的场景）。

3. **与现有功能的交互。** 如果用户删除了 bucket 然后重新创建同名 bucket，新对象可能与旧 blob 的 storage key 冲突（取决于 storageKey 算法）。虽然 `storageKey` 包含 `tenant/bucket/key`，但如果 key 相同... 需要分析。

4. **"软删除桶"恢复场景。** 一个相关的产品能力是：能否实现"桶回收站"？当前 `DeleteBucket` 是即时永久的。但在生产环境中，误删除 bucket 需要恢复能力。

### 缺失的能力

1. **`DeleteBucket` 的存储 blob 级联删除：**

   ```go
   func (s *sqlStore) DeleteBucket(ctx context.Context, tenant, bucket string) error {
       // [现有 DB 清理逻辑]
       
       // 新增：异步或同步删除存储 blob
       // 方案 A：同步删除（简单但阻塞）
       objects, _ := s.ListObjects(ctx, tenant, bucket, "", "", 0)
       for _, obj := range objects {
           s.store.Delete(ctx, obj.StorageKey)
       }
       
       // 方案 B：异步删除（通过事件 + 后台 worker）
       // 发布 "bucket.deleted" 事件
       // 后台 BlobCleaner worker 消费并清理
   }
   ```

2. **遗留 blob 的异步清理 worker（`BlobCleaner`）：**

   ```go
   type BlobCleaner struct {
       repo  repository.Repository
       store storage.Storage
   }
   
   func (bc *BlobCleaner) Run(ctx context.Context, sub <-chan repository.Event) {
       for e := range sub {
           if e.Type == "bucket.deleted" {
               bc.cleanBucketBlobs(ctx, e.TenantID, e.Bucket)
           }
       }
   }
   ```

3. **桶回收站（可选增强）：** 软删除 bucket 而非硬删除，保留 N 天，支持恢复：

   ```
   POST /v1/admin/tenants/{t}/undelete-bucket/{bucket}
   ```

   这在 `extensions.md` 中提及过但从未实现。

4. **存储使用确认：** 删除 bucket 后，提供 API 查询该 bucket 的存储是否已被完全清理。

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **删除 100 万个对象的 bucket** | 同步调用 `store.Delete` 100 万次不可接受。必须采用异步批量删除 + 进度报告 |
| **底层 blob 已被手动删除** | `store.Delete` 对不存在的 key 应返回 nil（已实现 ✅） |
| **S3 batch delete 优化** | 对于 S3 后端，可使用 `DeleteObjects` API 一次删除最多 1000 个对象 |
| **SSE 加密对象的 blob** | 同样删除即可，但需注意 .meta.json 文件在 local 后端中的清理 |
| **并发删除** | 桶删除期间不应接受新的 PUT 操作（可在代码层面加桶级锁） |

---

## 方向五：结构化系统健康与运行状态 API

### 现状

当前系统有三个与"健康"相关的端点：

```go
// 1. /healthz — 最简存活检查
r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
    w.WriteHeader(http.StatusOK)
    _, _ = w.Write([]byte(`{"ok":true}`))
})

// 2. /readyz — DB + Storage 存活检查
func readyzHandler(repo repository.Repository, store storage.Storage) http.HandlerFunc {
    return func(w http.ResponseWriter, req *http.Request) {
        if err := repo.Ping(req.Context()); err != nil {
            http.Error(w, "db: "+err.Error(), http.StatusServiceUnavailable)
            return
        }
        if _, err := store.Stat(req.Context(), "@healthz/probe"); err != nil && !errors.Is(err, storage.ErrNotFound) {
            http.Error(w, "storage: "+err.Error(), http.StatusServiceUnavailable)
            return
        }
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte(`{"ok":true}`))
    }
}

// 3. /info — 硬编码版本号
r.Get("/info", func(w http.ResponseWriter, _ *http.Request) {
    _, _ = w.Write([]byte(`{"service":"aero-vault","version":"0.1.0"}` + "\n"))
})
```

**现状总结：**

| 维度 | `/healthz` | `/readyz` | `/info` |
|------|-----------|-----------|--------|
| 返回格式 | 固定 JSON | 纯文本或 JSON | 固定 JSON |
| DB 连接 | ❌ 不检查 | ✅ Ping | ❌ |
| Storage 后端 | ❌ 不检查 | ✅ Stat 探针 | ❌ |
| 后台 worker 健康 | ❌ | ❌ | ❌ |
| SSE 加密状态 | ❌ | ❌ | ❌ |
| 索引器状态 | ❌ | ❌ | ❌ |
| 复制状态 | ❌ | ❌ | ❌ |
| DB 连接池统计 | ❌ | ❌ | ❌ |
| 集群单例状态 | ❌ | ❌ | ❌ |
| AI 管线状态 | ❌ | ❌ | ❌ |
| 多存储后端健康（S3/OSS/COS） | ❌ | ❌ | ❌ |
| 版本号动态注入 | ❌（硬编码） | ❌ | ❌（硬编码） |
| 启动时间 / 运行时长 | ❌ | ❌ | ❌ |
| goroutine 数量 / 内存使用 | ❌ | ❌ | ❌ |

### 为什么需要

1. **生产运维的核心工具。** 任何一个生产系统都需要一个聚合的健康端点，供负载均衡器、Kubernetes probe、监控系统（PagerDuty、Datadog、Grafana）统一查询。当前只检查 DB + Storage，运维在后台 worker 静默宕机后无法第一时间感知。

2. **Kubernetes probe 升级。** 当前 `/readyz` 返回 `200` 只要 DB 和存储后端存活。但一个索引器死锁的 Pod 不应接收流量。把后台 worker 健康纳入 readyz 判断，让 K8s 自动摘除异常 Pod。

3. **运维排障的第一站。** 当用户报告"搜索没有结果"时，运维人员首先需要知道"索引器是否在运行？AI 嵌入管线是否正常？复制是否滞后？"。一个结构化的健康 API 可以在 1 秒内回答这些问题。

4. **与 v47 方向四的互补关系。** v47 方向四聚焦于**后台 worker 的健康模型**（健康注册、自治愈、/debug/workers 端点）。而本方向关注的是**全系统健康视图**——不仅包含 worker，还包含存储后端、DB 连接池、SSE 加密、集群状态等所有组件的结构化输出。两者互补：/debug/workers 是内部诊断端点，/v1/admin/health 是运维可观测性端点。

### 缺失的能力

1. **结构化健康 API：`GET /v1/admin/health`**

   ```json
   {
     "status": "degraded",
     "version": "0.1.0-123abc",
     "uptime_seconds": 86400,
     "components": {
       "database": {
         "status": "healthy",
         "driver": "sqlite",
         "ping_ms": 2,
         "open_connections": 3,
         "in_use": 1,
         "max_open": 10
       },
       "storage_primary": {
         "status": "healthy",
         "backend": "local",
         "root": "./var/objects",
         "probe_ms": 5,
         "sse_enabled": true
       },
       "sse_encryption": {
         "status": "healthy",
         "key_count": 3,
         "current_key_id": "key-20260701",
         "next_rotation": "2026-08-01T00:00:00Z"
       },
       "indexer": {
         "status": "healthy",
         "processed": 15234,
         "lag_seconds": 2.3,
         "last_event": "2026-07-10T12:34:56Z"
       },
       "replication": {
         "status": "degraded",
         "replica_backend": "s3",
         "replica_bucket": "aero-replica",
         "pending_jobs": 5,
         "last_success": "2026-07-10T12:30:00Z",
         "last_error": "connection timeout"
       },
       "webhook": {
         "status": "healthy",
         "urls": ["https://hooks.example.com/events"],
         "retry_queue": 0,
         "dead_letters": 2
       },
       "job_pool": {
         "status": "healthy",
         "workers": 4,
         "pending": 12,
         "running": 2,
         "failed_last_hour": 0
       },
       "rate_limiter": {
         "status": "healthy",
         "active_buckets": 25
       },
       "ai_pipeline": {
         "status": "healthy",
         "embedder": "hash",
         "llm": "http",
         "reranker": "none",
         "search_cache_entries": 128,
         "daily_budget_used_usd": 0.05
       },
       "cluster": {
         "status": "healthy",
         "singleton_holder": "node-1",
         "singleton_lease_remaining": "45s",
         "listen_notify": "enabled"
       }
     }
   }
   ```

2. **组件健康注册模式：** 各组件在启动时注册到健康注册中心：

   ```go
   type HealthRegistry struct {
       mu         sync.RWMutex
       reporters  map[string]HealthReporter
   }
   
   type HealthReporter interface {
       Name() string
       Health(ctx context.Context) ComponentHealth
   }
   
   type ComponentHealth struct {
       Status   ComponentStatus // healthy | degraded | unhealthy
       Details  map[string]any
       LastOK   time.Time
       Error    string
   }
   ```

3. **健康聚合逻辑：** 各组件健康 → 系统健康映射规则：

   | 组件状态分布 | 系统状态 |
   |------------|---------|
   | 所有组件 healthy | `healthy` |
   | 任意组件 degraded，无 unhealthy | `degraded` |
   | 任意关键组件 (DB, Storage) unhealthy | `unhealthy` |
   | 非关键组件 unhealthy | `degraded` |

4. **`/readyz` 集成：** 当系统健康为 `unhealthy` 或关键组件 `degraded` 时，`/readyz` 返回 `503`。

5. **健康历史：** 保留最近 N 次健康检查的变更记录，用于诊断"何时开始出现问题"（可选，最小化实现可先不记录历史）。

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **健康检查自身引入性能开销** | 各组件健康报告缓存 10 秒（`sync.Map` + TTL），不穿透到后端 |
| **健康信息泄露内部拓扑** | `/v1/admin/health` 要求 admin scope，与 audit_log 联动 |
| **组件依赖关系** | 如果 DB 不可用，大部分组件健康无法获取——应返回缓存的最后一次已知健康状态 |
| **健康检查并发控制** | `HealthRegistry.reporters` 使用 RWMutex，读多写少 |
| **启动中的组件** | 组件在完全初始化前应报告 `starting` 状态，/readyz 在启动期间返回 `503` |

---

## 综合优先级与建议实施顺序

| 优先级 | 方向 | 影响面 | 前置依赖 | 涉及改动量 | 建议开始时间 |
|--------|------|--------|---------|-----------|------------|
| **P1** | Per-Bucket 通知规则执行引擎 | 兼容性/完整性——S3 API 接受配置但永不执行，用户体验灾难 | 无 | 新 `internal/events/notify.go` + `main.go` 注册（~300 行） | **立即** |
| **P1** | Multipart Upload 并发一致性与冲突模型 | 可靠性/数据完整性——可能的数据损坏 bug | 无 | `internal/service/file_multipart.go` 并发保护（+100 行） | **当前 Sprint** |
| **P1** | S3 Server Access Logging 交付运行时 | 兼容性/合规——配置落空，SOC2/PCI 准入 | 无 | 新 `internal/logging/` 包（~400 行） | **当前 Sprint** |
| **P2** | DeleteBucket 存储 Blob 遗孤与空间回收 | 可靠性/资源管理——存储泄漏 | 方向三的并发保护可能相关 | `internal/repository/sql_buckets.go` + 新 worker（~200 行） | **下一 Sprint** |
| **P2** | 结构化系统健康与运行状态 API | 运维/可观测性——生产运维核心工具 | `internal/worker/`（v47 方向四）完成后可复用健康注册机制 | 新 `internal/health/` 包 + `router.go` 注册（~400 行） | **下下 Sprint** |

### 建议的 Sprint 计划

```
Sprint N（立即）:
  ├── 方向三：修复 CompleteMultipart 并发竞态（原子状态标记防重入）— ~20 行，数据完整性红线
  └── 方向一：创建 NotificationRouter 作为独立 goroutine，支持多 webhook 目标 — ~300 行

Sprint N+1:
  ├── 方向二：创建 AccessLogWriter + 日志格式定义 + 定期 flush 到目标 bucket — ~400 行
  ├── 方向一：规则缓存 + FilterKey 过滤 + 事件类型匹配 — ~150 行
  └── 方向三：UploadPart TOCTOU 保护 + Part 冲突检测文档化 — ~50 行

Sprint N+2:
  ├── 方向四：DeleteBucket 异步 BlobCleaner worker — ~200 行
  ├── 方向四：桶回收站（软删除 + 恢复 API）— ~200 行（可选）
  └── 方向五：HealthRegistry + 组件注册模式 + /v1/admin/health 端点 — ~300 行

Sprint N+3+:
  ├── 方向五：所有组件注册健康报告器（indexer, replication, webhook, job pool, SSE, AI）— ~200 行
  ├── 方向五：/readyz 集成组件健康 + 健康历史追踪 — ~100 行
  └── 方向五：Grafana 面板展示系统健康（从 health API 拉取）— ~50 行配置
```

### 与既有 47 期分析的去重关系

| 方向 | 既有覆盖 | 本分析的新贡献 |
|------|---------|-------------|
| **Per-Bucket 通知规则执行引擎** | ⚠️ `extensions.md` 60 行提及（非编号 expansion 文档） | 首次定位为"配置落空"执行层断裂：schema/API 完整 → 运行时零执行；提供完整的路由引擎架构 + 目标适配器模式 + 规则缓存设计 |
| **S3 Server Access Logging 交付运行时** | ⚠️ v3 早期文档 80 行概念性方向（非执行层断裂分析） | 首次定位为"配置落空"执行层断裂；提供在线写入 + 定期 flush 的混合架构 + S3 标准格式兼容 + 日志生命周期管理 |
| **Multipart Upload 并发一致性与冲突模型** | ❌ **零覆盖**（v7 方向表 1 行提及"resumable upload"概念，完全不同的主题） | 首次全面分析 6 种竞态条件 + 数据损坏路径 + 版本化 bucket 的特殊问题 + 状态机设计 + 幂等 CompleteMultipart 方案 |
| **DeleteBucket 存储 Blob 遗孤与空间回收** | ❌ **零覆盖** | 首次识别 DeleteBucket 只删除 DB 不删 blob 的存储泄漏路径；分析 Reconcile GC 的局限性；提供异步 BlobCleaner 设计方案 |
| **结构化系统健康与运行状态 API** | ⚠️ v47 方向四覆盖后台 worker 健康（/debug/workers），v39 方向表 1 行提及"健康端点" | 首次提供全系统健康聚合架构：12+ 组件的结构化健康 + 状态推导规则 + K8s probe 集成 + 健康注册模式 |
