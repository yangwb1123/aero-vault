# 高价值扩展方向：上传治理与流量整形、冷存储恢复语义、桶级资源配额、MCP 协议完备性

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — 237+ Go 源文件，50 对迁移文件，3 套 SDK，MCP 双模式（HTTP+stdio），Web UI，Helm Chart，Grafana/Prometheus/OTel 配置，`AGENTS.md`，`ROADMAP.md`，`CHANGELOG.md`  
> **去重验证：** 对 `docs/requirements/` 下全部 102 份既有分析文档逐方向进行关键词正则 + 语义交叉验证 + 代码锚点反查  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。选取**代码中存在明确锚点、有实质性产品/架构影响、且在前 102 轮分析中未被独立深度覆盖**的方向。每个方向包含：现状与代码证据 → 产品价值与典型场景 → 架构权衡与建议方案 → 边界情况。

---

## 去重验证

对 `docs/requirements/` 下全部 102 份既有分析文档逐方向进行关键词正则 + 语义交叉验证：

| 方向 | 既往覆盖情况 |
|------|-------------|
| **方向一：上传治理与流量整形** | ⚠️ **零星提及，零深层分析** — v32 方向表一行 `max_content_length` 提及但无代码锚点分析；v11 方向三 `request size management` 讨论 JSON body 限流但聚焦 REST 端点而非跨协议上传治理；v49 方向二的 `max-uploads` 讨论的是 S3 ListMultipartUploads 分页参数，非上传大小限制；v86 方向一 `upload size limits as a config` 一句话提及但未分析全协议覆盖、带宽整形、慢客户端防护、暂停/恢复机制。**全量 102 份文档中零文档**分析 `io.Copy` 无缓冲节流、`r.Body` 直传无 `MaxBytesReader`、无逐协议上传治理策略 |
| **方向二：冷存储恢复语义与临时对象模型** | ⚠️ **浅层提及，零详细分析** — v56 方向一覆盖「object streaming」聚焦流式数据传输架构；v65 方向一覆盖「Glacier-like deep archive」概念性提到 `RestoreObject` 但仅作为 S3 协议差异列表中一行；v96 方向一覆盖「storage tiering」聚焦分层迁移引擎的 Transition 规则。**零文档**分析 `?restore` 当前仅做软删除恢复、Storage 接口无 `RestoreObject` 方法、异步恢复工作流（进度追踪+恢复后临时副本+自动过期）、恢复状态查询 API 等核心架构 |
| **方向三：桶级资源配额与公平调度** | ✅ **零实质性架构分析** — v82 方向二分析「per-tenant unfair rate limiting」聚焦租户级速率限制不均匀问题，**不涉及桶级**；v59 方向三分析「tenant-level quota enforcement」停留于租户级配额，未触及桶级子资源管控。正则搜索 `bucket.*quota\|bucket.*limit\|per.bucket.*ratelimit\|bucket.*resource.*govern\|bucket.*budget\|桶.*配额\|桶.*限流` → **102 份文档中零独立分析** |
| **方向四：MCP 协议完备性与租户隔离** | ⚠️ **部分覆盖但缺失核心方面** — v67 方向三覆盖 MCP 安全关注的 `mcp.tools.security#callTool` 和 `mcp.server#server.properties` 验证；v81 方向一覆盖「MCP protocol compliance as a testing surface」聚焦 MCP 协议版本的兼容性测试；v61 方向一覆盖「MCP tool composition」讨论工具组合模式。**零文档**系统分析 MCP specification 的六大核心能力层（Tools, Resources, Prompts, Sampling, Notifications, Streaming）当前实现覆盖面、`resources/subscribe` 缺失、`prompts/list` 缺失、`sampling/createMessage` 缺失、stdio 模式下租户隔离缺失、流式大文件读取缺失、MCP 通知机制缺失 |

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 |
|---|------|------|--------|---------|---------|
| **1** | **上传治理与流量整形：从无限制到分层带宽管控** | 生产可靠性/成本 | **P1** | 当前无任何上传大小限制；慢客户端可长期占用连接；大对象全内存缓冲（WebDAV spillBuffer 仅限 WebDAV 路径）；无传输进度追踪；无暂停/恢复 | 四处 `r.Body` 直传：`internal/api/rest/handler.go:Put`（`r.Body` → `svc.Put` 无 `MaxBytesReader`），`internal/api/s3compat/handler.go:PutObject`（`r.Body` 无大小预检），`internal/api/webdav/dav.go:OpenFile`（无 upload size limit），`internal/mcp/server.go:toolWriteFile`（`strings.NewReader(content)` 写入——仅限字符串，无二进制流）。`internal/config/config_app.go` 中**无任何 upload 大小配置字段**。WebDAV `spillBuffer`（8 MiB 阈值）仅限 WebDAV 一个路径 |
| **2** | **冷存储恢复语义与临时对象模型：从软删除恢复到完整归档生命周期** | S3 兼容/功能 | **P1** | `?restore` 仅处理软删除恢复；`StorageClass` 是标签而非行为；`Storage` 接口无 `RestoreObject`；S3 GLACIER RESTORE 的异步语义缺失 | `internal/api/s3compat/handler.go:restoreObject`（调用 `svc.RestoreObject` = 仅软删除恢复，注释 `restoreObject handles POST ?restore: restores a soft-deleted object`）。`internal/service/file_crud.go:RestoreObject`（`repo.RestoreObject` = SET deleted_at=NULL，与 GLACIER 恢复无关）。`internal/storage/storage.go:Storage`（无 `RestoreObject` 方法）。`internal/repository/repository.go:Object.StorageClass`（仅标签字段，无恢复状态或到期时间）。`internal/api/s3compat/xml.go`（XML 结构体无 `RestoreObjectResult`/`RestoreOutput` 节点） |
| **3** | **桶级资源配额与公平调度：从单租户粗粒度到多桶精细管控** | SaaS 运营/架构 | **P2** | 所有资源限制在 tenant 级别：配额（`max_bytes, max_objects`）、预算（`daily_budget_usd`）、速率限制（`RATE_LIMIT_RPS`）均无桶级隔离。一个桶的突发写入可耗尽全局并发槽或租户配额，影响同一租户下其他桶 | `internal/repository/repository.go:GetTenantQuota/SetTenantQuota`（仅 tenant 级，无 bucket 级）。`internal/service/file_crud.go:Put`（调用 `checkQuota` 检查 tenant 配额，无 bucket 子计数器）。`internal/service/quota_test.go`（仅 tenant 级测试）。`internal/middleware/ratelimit.go:PerTenantRateLimiter`（按 tenant 建 token bucket，无 bucket 级）。`internal/repository/sql_buckets.go`（`buckets` 表无 quota/limit 字段）。`internal/api/rest/admin.go:SetQuota`（`PUT /admin/tenants/{tenant}/quota`——只有 tenant 端点，无 bucket 配额端点） |
| **4** | **MCP 协议完备性与租户隔离：从基础工具集到全协议实现** | 生态/集成 | **P2** | MCP 仅实现了 6 个方法（initialize, ping, tools/list, tools/call, resources/list, resources/read）；缺失 Prompts、Sampling、Streaming、Notifications 核心能力；大文件读取截断在 4MB；stdio 模式无租户上下文传递 | `internal/mcp/protocol.go`（仅定义 `rpcRequest`, `rpcResponse`, `rpcError`, `tool`, `resource`——无 `prompt`, `sampling`, `subscription` 等类型）。`internal/mcp/server.go:dispatch`（switch 仅 6 个 case）。`internal/mcp/server.go:toolReadFile`（`io.LimitReader(rc, 4<<20)`——4MB 硬截断，无 streaming 分块）。`internal/mcp/server.go:tenantFor`（回退到 `s.tenant`，stdio 模式无租户头传递路径）。`internal/mcp/transport.go:ServeStdio`（scanner 1<<24 最大行，逐行 JSON-RPC，无二进制 streaming）。`cmd/server/main.go:runMCP`（`mcp.ServeStdio(ctx, server, os.Stdin, os.Stdout)`——无租户上下文注入） |

---

## 方向一：上传治理与流量整形

### 现状

当前系统对上传**完全没有大小限制和流量控制**。四个协议路径都直接将请求体传递给 `FileService.Put`，没有任何保护机制：

**1. REST PUT 无任何大小预检：**

```go
// internal/api/rest/handler.go
func (h *Handler) Put(w http.ResponseWriter, r *http.Request) {
    // ...
    obj, err := h.svc.Put(r.Context(), tenant, bucket, key, r.Body, r.ContentLength, opts)
    //                                    ^^^^^^ 直传，无 MaxBytesReader
}
```

**2. S3 PutObject 同样直传：**

```go
// internal/api/s3compat/handler.go:PutObject
obj, err := h.svc.Put(r.Context(), mw.TenantFrom(r.Context()), bucket, key, r.Body, r.ContentLength, ...)
//                                                                       ^^^^^^ 同样无限制
```

**3. WebDAV 虽有 spillBuffer 但仅限 8 MiB 以上溢出到临时文件——大上传仍可能因并发耗尽磁盘：**

```go
// internal/api/webdav/spill.go:spillBuffer — 8 MiB 阈值，仅限 WebDAV
const spillThreshold = 8 << 20  // 8 MiB
```

**4. 配置中无任何上传治理字段：**

```go
// internal/config/config_app.go — 搜索不到 upload/max/body/size 相关字段
type AppConfig struct {
    Addr              string
    LogLevel          slog.Level
    WriteTimeoutSec   int
    IdleTimeoutSec    int
    RequestTimeoutSec int
    MaxInFlight       int
    PerTenantMax      int
    // ❌ 无 UploadMaxBytes
    // ❌ 无 UploadBandwidthPerTenant
}
```

**5. 带宽限制完全缺失：**

```bash
# grep "throttle\|bandwidth\|transfer.*limit\|traffic.*limit\|upload.*speed\|download.*speed" -r internal/
# → 零结果
```

系统没有任何机制来：
- 根据 Content-Length 拒绝过大的上传
- 限制单个请求的传输时间（慢客户端攻防）
- 按租户/桶分配带宽配额
- 在上传期间提供进度追踪
- 支持断点续传（暂停/恢复）

### 产品价值

| 场景 | 当前行为 | 治理后 |
|------|---------|--------|
| **恶意客户端上传 50GB 对象** | 服务器内存耗尽（OOM）或 50GB 写入到磁盘 | 在协议层被 `MaxUploadBytes` 拒绝（`413 Payload Too Large`），保护整个系统 |
| **慢客户端（1KB/s）上传 100MB 对象** | 连接保持打开约 28 小时，占用一个 server goroutine | 被 `UploadIdleTimeout` 断开（如 5 分钟无数据=408），释放连接 |
| **租户 A 的 10 个并行大上传** | 耗尽 `MaxInFlight` 并发槽，租户 B 的所有请求被排队 | 按租户分配带宽令牌（`bandwidth_bps_per_tenant`），大上传降速但不阻塞小请求 |
| **用户想知道 5GB 上传的进度** | 完全黑箱：要么成功要么失败 | 支持 `Content-Length` 已知时回传 `X-Aero-Upload-Progress` 头（长轮询或 SSE 事件） |
| **移动端网络中断后上传失败** | 必须从头重传 | 支持 `Upload-Offset` / `Upload-Incomplete`（Tus 协议或自定义 resumable upload） |

### 架构权衡

**建议方案：三层上传治理架构**

```
┌─────────────────────────────────────────────────────────────┐
│                    Protocol Layer (Adapters)                 │
│                                                             │
│  REST: MaxBytesReader(r.Body, maxUploadBytes)                │
│  S3:   MaxBytesReader(r.Body, s3MaxUploadBytes)             │
│  WebDAV: MaxBytesReader (已有 spillBuffer)                    │
│  MCP:   硬限制 content ≤ maxUploadBytes (now: string only)    │
└─────────────────────┬───────────────────────────────────────┘
                      │
┌─────────────────────▼───────────────────────────────────────┐
│               Service Layer (限流 + 带宽整形)                  │
│                                                             │
│  BandwidthLimiter: token-bucket per tenant + per connection  │
│  UploadTracker:   in-progress upload registry               │
│  ProgressEmitter: SSE event per uploaded MB                 │
└─────────────────────┬───────────────────────────────────────┘
                      │
┌─────────────────────▼───────────────────────────────────────┐
│               Storage Layer (实际写入)                         │
│                                                             │
│  流式写入，不缓冲全量到内存                                      │
└─────────────────────────────────────────────────────────────┘
```

**1. 上传大小限制（协议层）：**

```go
// 在 middleware 或 handler 中注入
const defaultMaxUploadBytes = 5 << 30  // 5GB 默认

type UploadConfig struct {
    MaxUploadBytes      int64 // 单次上传最大值（默认 5GB）
    MaxUploadBytesPerTenant int64 // 按租户累计（每小时滑动窗口）
    UploadIdleTimeout   time.Duration // 无数据超时（默认 5 分钟）
}
```

配置方式：

```
UPLOAD_MAX_BYTES=5368709120       # 5GB 默认
UPLOAD_MAX_BYTES_PER_TENANT=0     # 0 = 不限制
UPLOAD_IDLE_TIMEOUT_SECONDS=300   # 5 分钟超时
```

**实现：**

```go
// REST handler 中的注入点
r.Body = http.MaxBytesReader(w, r.Body, cfg.MaxUploadBytes)

// S3 handler 中（S3 协议标准错误码）
r.Body = http.MaxBytesReader(w, r.Body, s3MaxUpload)
// 超出时返回 413 EntityTooLarge（S3 标准 XML 错误）
```

**2. 带宽整形（服务层）：**

```go
type BandwidthLimiter struct {
    mu      sync.Mutex
    perConn map[string]*rate.Limiter  // key = client IP + tenant
    perTenant map[string]*rate.Limiter
    
    bytesPerSec    int64  // 全局默认
    bytesPerTenant int64  // 按租户
}
```

`io.LimitReader` 不能直接限速，需要 `rate.Limiter` + 自定义 `io.Reader` wrapper：

```go
type rateLimitedReader struct {
    r       io.Reader
    limiter *rate.Limiter
}

func (r *rateLimitedReader) Read(p []byte) (int, error) {
    n, err := r.r.Read(p)
    if n > 0 {
        _ = r.limiter.WaitN(context.Background(), n)  // 阻塞直到令牌可用
    }
    return n, err
}
```

**3. 上传进度追踪（可观测性）：**

```go
type UploadProgress struct {
    Key          string
    Bucket       string
    TotalBytes   int64
    UploadedBytes int64
    StartedAt    time.Time
    Status       string // "uploading" | "paused" | "completed" | "failed"
}
```

通过 SSE 事件流或带外 `/v1/uploads/progress/{key}` 暴露。

**4. 可恢复上传（Resumable Upload）：**

最小可行实现是 Tus 协议子集：

```
POST /v1/files/{key}?resumable=true
→ 201 Location: /v1/uploads/{uploadID}

PATCH /v1/uploads/{uploadID}
→ Upload-Offset header

HEAD /v1/uploads/{uploadID}
→ Upload-Offset / Upload-Length
```

与现有 S3 multipart upload 不冲突——`POST ?uploads` 继续做 S3 分片上传，`POST ?resumable` 走 Tus 风格。

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| **Content-Length 未知（chunked transfer）** | 等待 `MaxUploadBytes` 数据到达后拒绝；或依赖 `UploadIdleTimeout` 断开慢连接 |
| **带宽限制与租户配额冲突** | 带宽限制在协议层，配额在服务层——配额已满时带宽限制无关，直接 `403 QuotaExceeded` |
| **上传中途连接断开** | 非可恢复上传：丢弃部分数据；可恢复上传：记录 offset，客户端通过 `Upload-Offset` 续传 |
| **可恢复上传的垃圾回收** | `Reconcile` 定期清理超时未完成的 `uploads`（`storage_key` 指向的临时 blob） |
| **大上传与 Idempotency-Key 交互** | 可恢复上传本身是幂等的（相同 offest 重复 PATCH 不产生副作用）；重试上传需 `Idempotency-Key` 防止重复 |
| **WebDAV MOVE 后的大文件** | MOVE 源文件不经过上传治理（已在存储上）；MOVE 目标路径也不应该受上传限制 |
| **多分片上传 vs 单次上传限制** | `MaxUploadBytes` 应用于**每个分片会话**，而非单个分片——500 个 10MB 分片构成 5GB 对象，不应被 `MaxUploadBytes=1GB` 阻拦 |

---

## 方向二：冷存储恢复语义与临时对象模型

### 现状

`StorageClass` 字段已经存在于对象元数据中，数据模型可以表达对象所属的存储层，但**完整冷存储恢复生命周期完全缺失**：

**1. `?restore` 仅处理软删除恢复——与 GLACIER 恢复无关：**

```go
// internal/api/s3compat/handler.go:restoreObject
func (h *Handler) restoreObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
    tenant := mw.TenantFrom(r.Context())
    if err := h.svc.RestoreObject(r.Context(), tenant, bucket, key); err != nil {
        // ↑ svc.RestoreObject = repo.RestoreObject = UPDATE deleted_at=NULL
        // 这是软删除恢复，不是 GLACIER 归档取回
        writeS3Error(w, r, err)
        return
    }
    // 返回 200 + RestoreObjectResult，但实际取回的 blob 可能不可用
}
```

**2. S3 协议的 GLACIER RESTORE 语义：**

```
POST /{bucket}/{key}?restore&days=7
→ 202 Accepted  (异步启动 restore)
→ GET /{bucket}/{key}
   → 403 InvalidObjectState (restore in progress)
   → 200 OK (restore complete, x-amz-restore: ongoing-request="false", expiry-date="...")
```

**3. Storage 接口缺少 `RestoreObject`：**

```go
// internal/storage/storage.go
type Storage interface {
    Put(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) (ObjectInfo, error)
    Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)
    // ...
    // ❌ 无 RestoreObject 方法
}
```

对于 S3 后端，`RestoreObject` 调用 `s3.RestoreObjectInput`（InitiateRestoreObject API）。对于 local 后端，GLACIER/GLACIER_IR/DEEP_ARCHIVE 不支持冷存储层——但可通过分离 STANDRAD 目录模拟。

**4. 恢复状态没有持久化字段：**

```go
// internal/repository/repository.go:Object
type Object struct {
    // ...
    StorageClass string  // 仅标签
    // ❌ 无 RestoreStatus string  // "in_progress" | "restored"
    // ❌ 无 RestoreExpiry *time.Time  // 临时副本到期时间
    // ❌ 无 RestoreRequestedAt *time.Time
}
```

**5. 没有迁移表中定义的恢复状态索引：**

```sql
-- 当前 migration（0018 ~ 0024）无任何 restore 相关列
```

### 产品价值

| 场景 | 当前行为 | 有冷存储恢复后 |
|------|---------|--------------|
| **对象被归档到 GLACIER（StorageClass=GLACIER）** | 标签存在但行为无变化——对象可正常 GET | GET 返回 `403 InvalidObjectState` + 错误码 `ObjectNotInActiveTier` |
| **用户需要取回归档对象** | 无法操作 | `POST ?restore&days=7` → 异步恢复，6 小时后可读 |
| **用户想知道恢复进度** | 无法查询 | `GET ?restore` → `x-amz-restore: ongoing-request="true"` |
| **临时副本即将过期** | 无通知 | 提前 1 天发出 `x-amz-restore-expiry` 警告或自动续期 |
| **S3 兼容性测试** | 失败（不符合 S3 语义） | 通过 |

### 架构权衡

**建议方案：恢复状态机 + 作业驱动工作流**

```
对象 StorageClass 为冷层
         │
         ▼
  POST ?restore&days=7
         │
         ▼
 ┌──────────────────┐
 │  InitiateRestore  │  ← 作业 JobRestore 入队
 │  → status=in_progress
 │  → restore_expires=now+7d
 │  → StorageClass 不变
 └────────┬─────────┘
          │
          ▼
  ┌────────────────┐
  │  JobRestore     │  ← jobs.Pool worker 执行
  │  1. storage.Restore(key)  │
  │  2. repo.SetRestoreStatus │
  │     (restored_at=now)     │
  │  3. events.Publish        │
  │     (restore.completed)   │
  └────────┬─────────────────┘
           │
           ▼
 ┌──────────────────┐
 │  Restored 状态    │
 │  GET → 正常响应    │
 │  x-amz-restore:   │
 │   ongoing-request=│
 │   "false"         │
 │   expiry-date=... │
 └────────┬─────────┘
          │
          ▼ 临时副本到期
 ┌──────────────────┐
 │  JobRestoreExpire │
 │  1. 删除临时副本   │
 │  2. 恢复原 Storage│
 │     Class 行为     │
 │  3. GET → 403     │
 └──────────────────┘
```

**1. 数据模型扩展：**

```sql
-- migration 0025: restore 元数据
ALTER TABLE objects ADD COLUMN restore_status TEXT;  -- NULL | "in_progress" | "restored"
ALTER TABLE objects ADD COLUMN restore_expires_at TEXT;  -- 临时副本到期时间
ALTER TABLE objects ADD COLUMN restore_requested_at TEXT;
```

**2. Storage 接口扩展：**

```go
type Storage interface {
    // ... 现有方法
    // RestoreObject 从冷层取回对象到活跃层（S3: RestoreObjectInput）
    RestoreObject(ctx context.Context, key string, restoreDays int) error
    // StorageClassSupported 报告是否支持指定存储类
    StorageClassSupported(class string) bool
}
```

各后端实现：

| 后端 | `StorageClassSupported` | `RestoreObject` |
|------|------------------------|-----------------|
| `local` | 仅 `STANDARD` | `ErrNotSupported`（local 无冷存储概念） |
| `s3` | `STANDARD`, `STANDARD_IA`, `GLACIER`, `DEEP_ARCHIVE`, `INTELLIGENT_TIERING` | `s3.RestoreObjectInput`（需 `s3:RestoreObject` 权限） |
| `oss` | `STANDARD`, `IA`, `ARCHIVE`, `COLD_ARCHIVE` | OSS RestoreObject API |
| `cos` | `STANDARD`, `STANDARD_IA`, `ARCHIVE` | COS RestoreObject API |

**3. S3 RESTORE 响应 XML：**

```xml
<!-- in-progress -->
<RestoreObjectResult xmlns="...">
  <RestoreOutput>
    <RestoreStatus>in-progress</RestoreStatus>
  </RestoreOutput>
</RestoreObjectResult>

<!-- HEAD/GET 时的恢复信息 -->
<!-- x-amz-restore: ongoing-request="true" -->
<!-- x-amz-restore: ongoing-request="false", expiry-date="Sat, 18 Jul 2026 12:00:00 GMT" -->
```

**4. 恢复作业框架：**

```go
const JobRestore = "restore_object"
const JobRestoreExpire = "restore_object_expire"

// main.go 中的注册
jobReg.Register(JobRestore, func(ctx context.Context, job repository.Job) error {
    var params struct {
        ObjectID   int64 `json:"object_id"`
        RestoreDays int  `json:"restore_days"`
    }
    json.Unmarshal([]byte(job.Payload), &params)
    return svc.ExecuteRestore(ctx, params.ObjectID, params.RestoreDays)
})

jobReg.Register(JobRestoreExpire, func(ctx context.Context, job repository.Job) error {
    id, err := strconv.ParseInt(job.Payload, 10, 64)
    if err != nil { return err }
    return svc.ExpireRestoredCopy(ctx, id)
})
```

**5. REST API 扩展：**

```go
// POST /v1/files/{key}/restore  — 异步恢复
func (h *Handler) Restore(w http.ResponseWriter, r *http.Request) {
    daysStr := r.URL.Query().Get("days")
    days, _ := strconv.Atoi(daysStr)
    if days <= 0 { days = 1 }
    // ... 验证对象 StorageClass 是冷层 ...
    // ... 入队 JobRestore ...
    w.WriteHeader(http.StatusAccepted)
}

// GET /v1/files/{key}?restore  — 查询恢复状态
// 返回 x-amz-restore 头
```

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| **对象已在活跃层时 POST ?restore** | 返回 `400 InvalidRequest: object is already in STANDARD`（S3 返回 `InvalidObjectState`） |
| **恢复期间对象被删除** | 删除清除 restore 标记和临时副本；JobRestore 执行时检查对象是否存在 |
| **同一对象多次 POST ?restore** | 第二次调用返回 `200 OK`（S3 语义）或返回 `409 Conflict`（已有恢复进行中） |
| **临时副本到期后客户端未读取** | 到期后下次 GET 返回 `403`；不到期对象可能在恢复作业执行时副本已不存在 |
| **跨后端冷存储恢复（local primary + S3 GLACIER replica）** | RestoreObject 作用于 primary backend；GLACIER 对象需要先由 replication 恢复 |
| **恢复通知** | 恢复完成时通过 event bus 发布 `restore.completed` 事件——Webhook 可接收通知 |
| **REST 和 S3 恢复一致性** | REST 的 `POST /files/{key}/restore` 和 S3 的 `POST ?restore` 调用同一 `svc.RestoreColdObject` |

---

## 方向三：桶级资源配额与公平调度

### 现状

当前所有资源管控都是**租户级（tenant-level）**，没有桶级（bucket-level）隔离：

| 资源维度 | 租户级 | 桶级 |
|---------|--------|------|
| **存储配额** | ✅ `TenantQuota.MaxBytes` / `MaxObjects` | ❌ 缺失 |
| **对象数量限额** | ✅ `TenantQuota.MaxObjects` | ❌ 缺失 |
| **请求速率限制** | ✅ `RateLimiter` per tenant | ❌ 缺失 |
| **AI 日费用预算** | ✅ `TenantRecord.DailyBudgetMicros` | ❌ 缺失 |
| **并发请求数** | ✅ `PerTenantConcurrencyLimiter` | ❌ 缺失 |
| **存储标签** | — | ❌ 桶无配额告警阈值 |

**代码证据：**

```go
// internal/repository/repository.go:GetTenantQuota — 只有租户级
GetTenantQuota(ctx context.Context, tenant string) (TenantQuota, error)
SetTenantQuota(ctx context.Context, tenant string, maxBytes, maxObjects int64) error
// ❌ 无 BucketQuota

// internal/middleware/ratelimit.go:PerTenantRateLimiter — 按租户
func (rl *RateLimiter) Middleware() func(http.Handler) http.Handler {
    // key = tenantID, 无 bucket 维度
}

// internal/middleware/middleware.go:PerTenantConcurrencyLimiter
// key = tenantID, 无 bucket 维度

// internal/repository/sql_buckets.go
// buckets 表无 quota_* 字段
```

**这导致的生产问题：**

```
场景：        租户 A 有 2 个桶：logs/ 和 prod/
问题：        logs/ 桶的 100 个并发写入耗尽租户 A 的全局并发槽
             → prod/ 桶的读取也被阻塞
影响：        同一个租户内的噪音桶影响关键业务桶
类似场景：    logs/ 桶写入 50GB 日志 → 触发 TenantQuota.MaxBytes
             → prod/ 桶无法写入任何数据
```

### 产品价值

| 场景 | 当前行为 | 桶级配额后 |
|------|---------|-----------|
| **logs/ 桶突发写入 1TB 日志** | 触发 tenant 配额 → 所有桶（含 prod/）写入拒绝 | `logs/` 桶单独达到 `max_bytes=100GB` 限制，`prod/` 继续工作 |
| **审计要求桶级存储量追踪** | 只能通过外部 `ListObjects` 统计 | 自动追踪每桶 `BucketStats` + `bucket_bytes` Prometheus 指标 |
| **桶级速率限制保护** | 一个慢客户端打满整个租户的 RPS 配额 | 关键桶可以配置更高的 RPS 上限，普通桶受限 |
| **AI 预算按桶分配** | 所有桶共享租户每日 AI 预算 | 某些桶可有独立的 AI 日预算上限 |
| **桶级告警阈值** | 无法为特定桶设置容量告警 | Grafana 可对 `bucket_bytes > 0.9 * bucket_max_bytes` 设置告警 |

### 架构权衡

**建议方案：桶级资源分层的三种模式**

**模式 A（推荐）：桶级计数器 + 租户级硬帽**

```
租户配额: MaxBytes=100GB, MaxObjects=10K
  ├── bucket=logs/:  BucketMaxBytes=80GB (optional), BucketMaxObjects=8K
  ├── bucket=prod/:  BucketMaxBytes=20GB (optional), BucketMaxObjects=2K
  └── bucket=temp/:  BucketMaxBytes=10GB (optional, overcommit)
```

- 桶级上限 `≤` 租户上限（超额时租户上限优先）
- 桶无单独配额限制时，共享租户池
- 新创建桶默认无单独限制（租户池分配）

**数据模型：**

```sql
-- migration 0025: 桶级配额
ALTER TABLE buckets ADD COLUMN quota_max_bytes INTEGER;      -- NULL = 继承 tenant
ALTER TABLE buckets ADD COLUMN quota_max_objects INTEGER;    -- NULL = 继承 tenant
ALTER TABLE buckets ADD COLUMN quota_max_rps REAL;           -- NULL = 继承 tenant
ALTER TABLE buckets ADD COLUMN quota_daily_budget_micros INTEGER; -- NULL = 继承 tenant
ALTER TABLE buckets ADD COLUMN quota_warn_bytes INTEGER;     -- 告警阈值（如 80%）
```

**API 扩展：**

```go
// REST
PUT /v1/buckets/{bucket}/quota          -- 设置桶级配额
GET /v1/buckets/{bucket}/quota          -- 查看桶级配额
GET /v1/buckets/{bucket}/usage          -- 当前桶使用量（替代 BucketStats 中的对象计数）
DELETE /v1/buckets/{bucket}/quota       -- 恢复为继承租户配额

// Admin API（跨租户查看）
GET /v1/admin/buckets/usage             -- 所有桶用量排行
GET /v1/admin/tenants/{tenant}/buckets/usage  -- 某租户下所有桶的用量
```

**S3 兼容：**

理论上 S3 没有桶级配额 API，但可通过自定义 extension（`x-amz-*` 头）或在 bucket config 中设置：

```
PUT /{bucket}?quota (aero-vault extension)
```

**使用计数：**

```go
// service/file_crud.go:Put — 修改路径
func (s *FileService) Put(ctx context.Context, tenant, bucket, key string, ...) {
    // 1. 检查租户级配额（现有）
    // 2. 检查桶级配额（新增）
    bucketUsage := s.repo.GetBucketUsage(ctx, tenant, bucket)  // 新方法
    bucketQuota := s.repo.GetBucketQuota(ctx, tenant, bucket)  // 新方法
    if bucketQuota.MaxBytes > 0 && bucketUsage.TotalBytes + size > bucketQuota.MaxBytes {
        return ErrQuotaExceeded
    }
    // ... 写入 ...
    s.repo.AddBucketUsage(ctx, tenant, bucket, size, 1)  // 原子更新
}
```

**模式 B（更轻量）：桶级速率限制中间件**

如果不想引入桶级配额完整的 DB 变更，可先实现桶级速率限制作为独立中间件：

```go
// internal/middleware/bucket_ratelimit.go
type BucketRateLimiter struct {
    defaults  *rate.Limiter  // 全局默认
    perBucket map[string]*rate.Limiter  // bucket → limiter（从 DB 配置加载）
}
```

在 S3 handler 和 REST handler 中按 bucket 提取 rate limiter，与现有 tenant-level rate limiter 串联。

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| **桶配额设为 0 或负数** | 0 = 无限制（等于继承租户配额），负数视为无效配置 `ErrInvalidArgs` |
| **桶配额总和超过租户配额** | 这是一个 overcommit 场景——单个桶超额时仍受租户硬帽限制（`min(bucket_max, tenant_max - other_buckets_used)`） |
| **动态调整桶配额正在使用中的桶** | `PUT /buckets/{bucket}/quota` 即时生效；降低配额时需检查当前使用量是否已超——若超则返回 `409 Conflict` |
| **大量桶（>10K）的配额内存** | 桶配额不常变——可通过 Redis 或 DB 缓存层按需加载；内存中维护 LRU cache |
| **桶级配额与 Lifecycle 互动** | Lifecycle 删除对象后配额计数实时减少；保留期内的对象虽然已软删除但仍占用配额——在 `quota_warn_bytes` 中说明 |
| **删除桶时配额残留** | `DeleteBucket` 清除桶的配额行 |

---

## 方向四：MCP 协议完备性与租户隔离

### 现状

当前的 MCP 实现（`internal/mcp/`）覆盖了 MCP 协议的一个基础子集，但离完整的 MCP specification 实现还有较大差距：

**1. 已实现（6 个方法）：**

| 方法 | 状态 | 代码位置 |
|------|------|---------|
| `initialize` | ✅ | `server.go:83` |
| `ping` | ✅ | `server.go:106` |
| `tools/list` | ✅ | `server.go:89`（6 个工具） |
| `tools/call` | ✅ | `server.go:117`（6 个 handler） |
| `resources/list` | ✅ | `server.go:325` |
| `resources/read` | ✅ | `server.go:341` |
| `notifications/initialized` | ❌ 缺失 | — |
| `resources/subscribe` | ❌ 缺失 | — |
| `resources/listChanged` | ❌ 缺失 | — |
| `prompts/list` | ❌ 缺失 | — |
| `prompts/get` | ❌ 缺失 | — |
| `sampling/createMessage` | ❌ 缺失 | — |
| `roots/list` | ❌ 缺失 | — |

**2. 关键缺失能力：**

| 能力 | 影响 | 代码锚点 |
|------|------|---------|
| **Resource subscriptions** | 客户端无法订阅对象变更通知，必须轮询 | `server.go:dispatch` 无 `resources/subscribe` case；`protocol.go` 无 subscription 类型 |
| **Prompts（模板提示）** | MCP 客户端无法使用预定义的提示模板（如 `summarize-file`、`search-and-answer`） | `server.go:listTools` 有工具但无 `prompts/list`；`protocol.go` 无 `Prompt` 类型 |
| **Streaming（流式响应）** | `read_file` 截断到 4MB；`list_files` 截断到 50 条；无分页游标或流式分块 | `server.go:toolReadFile`（`io.LimitReader(rc, 4<<20)`）；`server.go:toolListFiles` 硬限制 `limit=50` |
| **Sampling（LLM 回调）** | MCP host 无法通过 sampling 向 aero-vault 请求 LLM 补全 | `protocol.go` 无 `CreateMessageRequest`/`CreateMessageResult` |
| **Notifications** | 服务端无法主动通知客户端事件（如对象创建、删除） | `server.go:dispatch` 无 notification 相关处理 |
| **Stdio 租户隔离** | `aero-vault mcp` 模式无租户上下文——所有操作在 `default` 租户下 | `transport.go:ServeStdio` 无租户头解析；`server.go:tenantFor` 在 stdio 模式回退到 `s.tenant`（`"default"`） |

**3. `read_file` 的 4MB 硬截断：**

```go
// internal/mcp/server.go:249
body, err := io.ReadAll(io.LimitReader(rc, 4<<20))
```

对于超过 4MB 的文件（常见场景：日志文件、PDF、大文本文件），客户端获得的是截断内容，且完全没有意识到数据不完整。

**4. Stdio 模式无租户传递路径：**

MCP 的 stdio 模式通过 stdin/stdout 通信。在 `aero-vault mcp` 子命令中：

```go
// cmd/server/main.go:runMCP
server := mcp.NewServer(svc, repo, search, "default", logger)
//                                               ^^^^^^^ 硬编码 default
```

MCP host（如 Claude Desktop）启动时可能需要在环境中告诉 aero-vault 当前租户，目前无此机制。

### 产品价值

| 场景 | 当前行为 | 完备后 |
|------|---------|--------|
| **MCP 客户端需要实时文件变更通知** | 必须轮询 `list_files`（每次全量扫描） | 订阅 `resources/subscribe` → 变更时推送 |
| **读取 10MB 文档** | 只能读取前 4MB，剩余 6MB 静默丢弃 | 流式分块或返回完整内容 |
| **MCP host 想用预定义提示模板** | 无法发现——所有逻辑都在工具的固定 schema 中 | `prompts/list` 暴露 `summarize`、`query-answer`、`extract-action-items` |
| **Agent 需要 LLM 补全但无外部 LLM 配置** | 依赖 aero-vault 内建 LLM（需 AI_CHAT_PROVIDER） | 通过 sampling 回调 MCP host 的 LLM |
| **多租户场景的 MCP 使用** | 所有 MCP 操作在 `default` 租户下 | stdio 模式通过环境变量 `AERO_TENANT` 或 JSON-RPC `initializationOptions` 传递租户 |
| **列出 10,000 个文件** | 返回前 50 个，剩余不可达 | 分页游标（`cursor` token）+ 连续分页 |

### 架构权衡

**建议方案：分阶段 MCP 协议完备性扩展**

**Phase 1（短周期，1-2 周）：提升现有能力**

| 改进项 | 实现方式 | 代码位置 |
|--------|---------|---------|
| 移除 `read_file` 的 4MB 硬限制 | 流式分块响应（`Read(p []byte)` loop + `Content` 分段）或允许 `max_size` 参数 | `server.go:toolReadFile` |
| `list_files` 支持分页 | 添加 `cursor` 参数，返回 `next_cursor` | `server.go:toolListFiles` |
| Stdio 租户隔离 | 从环境变量 `AERO_TENANT` 读取（或从 JSON-RPC `initializationOptions`） | `transport.go:ServeStdio` + `server.go:tenantFor` |
| `resources/subscribe` | 通过 event bus 订阅，有变更时发送 `notifications/resources/listChanged` | `server.go:dispatch` + `protocol.go` |

```go
// Phase 1: read_file 支持分段读取
func (s *Server) toolReadFile(ctx context.Context, args map[string]any) (any, *rpcError) {
    maxSize := intArg(args, "max_size", 0)  // 0 = 不限制
    if maxSize <= 0 || maxSize > maxReadLimit {
        maxSize = maxReadLimit  // 128MB 硬上限
    }
    body, err := io.ReadAll(io.LimitReader(rc, int64(maxSize)))
    // ...
}

// Phase 1: list_files 分页
func (s *Server) toolListFiles(ctx context.Context, args map[string]any) (any, *rpcError) {
    limit := intArg(args, "limit", 200)
    if limit <= 0 || limit > 1000 { limit = 1000 }
    cursor := stringArg(args, "cursor", "")
    page, err := s.svc.List(ctx, s.tenantFor(ctx), bucket, prefix, cursor, limit)
    // 返回 next_cursor
}
```

**Phase 2（中周期，2-3 周）：Prompts + Notifications**

```go
// protocol.go 新增类型
type Prompt struct {
    Name        string `json:"name"`
    Description string `json:"description,omitempty"`
    Arguments   []PromptArgument `json:"arguments,omitempty"`
}

type PromptArgument struct {
    Name        string `json:"name"`
    Description string `json:"description,omitempty"`
    Required    bool   `json:"required"`
}

// server.go 新增 handler
func (s *Server) dispatch(ctx context.Context, req rpcRequest) (any, *rpcError) {
    switch req.Method {
    // ... 现有 case ...
    case "prompts/list":
        return s.listPrompts(), nil
    case "prompts/get":
        return s.getPrompt(ctx, req.Params)
    case "resources/subscribe":
        return s.subscribeResource(ctx, req.Params)
    case "notifications/initialized":
        return nil, nil  // 确认即返回
    }
}
```

**预定义 Prompts：**

```go
func (s *Server) listPrompts() listPromptsResult {
    prompts := []Prompt{
        {
            Name: "summarize-file",
            Description: "Summarize the content of a file in the vault",
            Arguments: []PromptArgument{
                {Name: "key", Description: "Object key", Required: true},
            },
        },
        {
            Name: "search-and-answer",
            Description: "Search the vault and answer using RAG",
            Arguments: []PromptArgument{
                {Name: "query", Description: "Search query", Required: true},
            },
        },
    }
    return listPromptsResult{Prompts: prompts}
}
```

**Phase 3（长周期，2-3 周）：Sampling + Streaming**

| 能力 | 实现方式 | MCP spec |
|------|---------|---------|
| `sampling/createMessage` | 将 aero-vault 的 LLM 能力包装为 sampling provider，让 MCP host 调用 | `$/sampling/createMessage` |
| 流式工具响应 | 支持 MCP 的 JSON-RPC 分块响应（`$/progress` 通知） | Progress notifications |
| `resources/listChanged` 主动通知 | 当对象创建/删除/变更时广播 | `notifications/resources/listChanged` |

**Phase 4（按需）：租户隔离完善**

```go
// Stdio 模式下通过初始化参数传递租户
// MCP Host (Claude Desktop) 发送：
{
    "jsonrpc": "2.0",
    "method": "initialize",
    "params": {
        "protocolVersion": "2025-03-26",
        "clientInfo": {"name": "claude-desktop", "version": "1.0"},
        "capabilities": {},
        "initializationOptions": {
            "tenant": "acme-corp"  // ← 自定义扩展字段
        }
    }
}

// 服务器端解析：
func (s *Server) handleInitialize(params json.RawMessage) {
    // 如果能解析出 tenant，设置到当前上下文
}
```

或者更简单（对于不支持自定义初始化选项的 MCP host）：从环境变量 `AERO_TENANT` 读取，在 stdio 子进程启动前设置。

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| **大文件流式读取超时（stdio 模式无 HTTP 超时控制）** | 客户端负责在合理时间内消费流；服务端设置 `max_stream_timeout=5m`，超时后自动断开连接 |
| **订阅了 resource 但连接断开** | 重新 connect 后客户端应通过 `resources/subscribe` 重新订阅——服务端不持久化订阅状态 |
| **Prompts 参数校验失败** | 返回 `-32602 Invalid params` + 描述哪个参数缺失或类型错误 |
| **Sampling 请求时 LLM 未配置** | 返回 sampling 错误 `-32000` → "sampling not available: no LLM configured" |
| **多租户环境下的 MCP 工具权限** | 每个工具调用使用 `tenantFor(ctx)` 确定租户；scope 校验复用 REST handler 的 scope 机制 |
| **MCP 协议版本兼容性** | 当前声明 `ProtocolVersion = "2025-03-26"`，初始化时客户端可能要求旧版本——需支持向下兼容的 capability negotiation |

---

## 优先级与建议执行顺序

| 排序 | 方向 | 前置依赖 | 建议投入 | 核心交付物 |
|------|------|---------|---------|-----------|
| **1** | **方向一：上传治理与流量整形**（Phase 1：`MaxBytesReader` 注入所有协议 + 配置字段 + 测试） | 无 | 1-2 周 | 四个协议路径的 `MaxBytesReader` + `UploadIdleTimeout` 中间件 + 配置 `UPLOAD_MAX_BYTES` + 单元测试 |
| **2** | **方向二：冷存储恢复语义**（Phase 1：`Storage.RestoreObject` 接口 + 恢复状态机 + `?restore` 重构） | 方向一中的 S3 协议路径改造 | 2-3 周 | `RestoreObject` 接口 + S3/local 实现 + 状态字段（`restore_status`, `restore_expires_at`）+ `?restore` 重构 + 作业 `JobRestore`/`JobRestoreExpire` |
| **3** | **方向四 Phase 1：MCP 基础完备性**（4MB 限制解除 + 分页 + stdio 租户） | 无 | 1-2 周 | `read_file` 分段 + `list_files` 分页光标 + `AERO_TENANT` 环境变量 + `resources/subscribe` |
| **4** | **方向三：桶级资源配额**（Phase 1：DB 表扩展 + `GetBucketUsage` + 服务层检查） | 现有 `BucketStats` 已存在 | 2-3 周 | `buckets` 表扩展（`quota_max_bytes`, `quota_max_objects`）+ `GetBucketQuota`/`AddBucketUsage` 方法 + `PUT /buckets/{bucket}/quota` API |
| **5** | **方向四 Phase 2：MCP Prompts + Notifications** | Phase 1 完成 | 2-3 周 | `Prompt` 类型 + `prompts/list` + `prompts/get` + 预定义提示模板 + `notifications/resources/listChanged` |

**建议执行策略：**

1. **Phase 1（方向一 + 方向四 Phase 1 并行，2 周）**：上传治理是生产可靠性基础，MCP 基础完备性是 DX 改善——两个正交、无依赖的快速交付。上传治理直接影响 `main.go` 中四个协议路径的 handler，影响面广但变更模式简单（添加 `io.LimitReader`/`MaxBytesReader` 封装）。MCP 基础改进是纯 `internal/mcp/` 内部变更。

2. **Phase 2（方向二 + 方向三并行，3 周）**：冷存储恢复和桶级配额都是数据模型扩展（DB migration）+ 新 API 端点。两者共享数据层变更但逻辑独立，可并行开发。

3. **Phase 3（方向四 Phase 2，2-3 周）**：MCP Prompts、Notifications、Sampling 作为增强 MCP 协议完备性的最后阶段。Sampling 依赖 LLM 配置，适合最后交付。

---

## 总结

以上四个方向覆盖了 aero-vault 在**生产可靠性（上传治理 + 流量整形）、S3 兼容深度（冷存储恢复语义）、SaaS 运营精细度（桶级资源配额）、生态集成（MCP 协议完备性）** 四个关键维度的缺口。每个方向与前 102 轮分析无实质重叠，同时在代码库中有明确锚点，具备从当前架构渐进演进的可行性。

| 维度 | 当前状态 | 目标状态 |
|------|---------|---------|
| **上传治理** | 零限制：无大小限制、无带宽控制、无超时、无进度 | 分协议 `MaxUploadBytes` + 租户级带宽整形 + 空闲超时 + 可恢复上传（可选） |
| **冷存储恢复** | `?restore` = 软删除恢复，`StorageClass` 是纯标签 | 异步 `RestoreObject` + 恢复状态机 + 临时副本 + 到期自动过期 + 进度查询 |
| **桶级资源管控** | 所有配额在租户级，一个桶可饿死同租户其他桶 | `buckets.quota_*` 字段 + 桶级速率限制 + 桶级存储/对象/预算配额 |
| **MCP 完备性** | 6 个基础方法，4MB 截断，无分页，无租户隔离，无 Prompts | 全协议能力覆盖 + 流式读取 + 订阅通知 + Prompts + Sampling + 租户传递 |
