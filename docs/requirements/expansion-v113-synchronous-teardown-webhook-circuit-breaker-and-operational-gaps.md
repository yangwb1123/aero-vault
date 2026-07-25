# 高价值扩展方向：同步资源拆除、Webhook 熔断、分片上传治理、缓存语义缺失与存储合约盲区

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — 250+ Go 源文件，完整装配链路 `cmd/server/main.go`，全部子包 `internal/`（storage/repository/service/api/ai/auth/middleware/events/jobs/reconcile/replication/mcp/cli/webui），3 套 SDK（Go/Python/JS），MCP 双模式（HTTP+stdio），WebDAV，Web UI，50 对迁移文件，`deploy/` 全套 Helm/Grafana/Prometheus 配置  
> **去重验证：** 对 `docs/requirements/` 下全部 112 份既有分析文档 + `deep-production-gaps-v1.md` 进行逐方向关键词正则 + 代码锚点交叉验证。  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。每个方向必须包含：**代码中存在的具体实现锚点**、**可量化的生产/产品影响**、**架构权衡与边界情况**。

---

## 方法论：从"功能的骨架"到"运行的断层"

AeroVault 的功能矩阵已经非常完整。前 112 轮分析覆盖了绝大多数功能缺口、协议完备性、AI 管线、存储深度和运维治理。

本文聚焦的是一类尚未被系统覆盖的缺口：**功能的骨架存在，但运行时行为在边缘条件下产生静默断裂、资源泄漏或运维盲区。**

| 缺口类型 | 判定标准 | 本文方向 |
|----------|---------|----------|
| **同步路径资源泄漏** | 请求处理路径执行了无界 I/O 的同步操作，阻塞 HTTP 连接直到操作完成，无异步降级或超时边界 | 方向一（同步拆除） |
| **熔断缺失导致级联重试** | 海量事件的并行重试在目标端点故障时不仅无帮助，反而加剧故障 | 方向二（Webhook 熔断） |
| **协议合约静默绕过** | S3 规范要求的上传约束（最小分片大小、分片幂等性）在服务端无校验，客户端可绕过 | 方向三（分片上传治理） |
| **缓存语义缺失** | HTTP 响应中缺失缓存控制头，客户端和 CDN 无法正确缓存对象 | 方向四（缓存语义） |
| **合约测试覆盖不均** | 存储后端合约测试仅覆盖核心 CRUD，多分片上传和预签名路径在云后端的合规性未经自动化验证 | 方向五（合约盲区） |

### 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 关键代码锚点 |
|---|------|------|--------|---------|-------------|
| **1** | **同步资源拆除阻塞 HTTP 请求路径** —— hardDelete、配额回滚、ChunkCleaner 在请求 goroutine 内同步执行，云后端 I/O 延迟直接转化为 HTTP 延迟，无异步逃生方案 | 可靠性/性能 | **P0** | `hardDeleteObject` 同步调用 `store.Delete`（云后端耗时可达秒级）+ `chunkCleaner.DeleteObjectChunks` + `repo.HardDeleteObject` + `AddTenantUsage` 回滚，全部在请求 goroutine 中串行执行。50GB 对象的删除在 S3 后端上可阻塞请求 >3 秒。`DeleteFolder` 更极端：先加载全部 key 进内存，再逐 key 调用 softDelete → 万级操作在一个 HTTP 请求中完成 | `internal/service/file_crud.go:250-275`（`hardDeleteObject` — 同步调用 `store.Delete` + `chunkCleaner.DeleteObjectChunks` + `repo.HardDeleteObject` + `AddTenantUsage` + `emit` 全部串行）；`internal/api/rest/handler.go:745-780`（`DeleteFolder` — 加载全部 key 进 `allKeys` 切片，再逐 key `BatchDelete`）；`internal/service/file_multipart.go:181-186`（`AbortMultipart` — 同步调用 `store.AbortMultipart`）；`internal/storage/s3.go:120-135`（S3 `Delete` — AWS SDK HTTP 调用，单次 200-500ms，重试后更久）；`internal/storage/circuitbreaker.go`（熔断器存在但仅为存储后端提供全局 fail-fast，不做请求级异步拆除） |
| **2** | **Webhook 重试系统缺乏每目标端点的熔断器与速率限制** —— 所有 Webhook 目标共享一个全局重试池，无每 URL 失败追踪或熔断，一个故障端点可消耗所有重试容量 | 可靠性 | **P1** | 当前 Webhook 重试系统的 `MarkWebhookSucceeded` 在 10 次失败后调用，将永久死信与成功混淆。但重试本身无每目标速率限制：当 1000 个事件同时触发，1000 个 HTTP POST 并行发向同一个故障端点。无熔断机制 — 故障端点会在每次事件时被重试，不会因持续失败而被暂停 | `internal/events/webhook.go:100-130`（`Run` — `NextPendingFailures` → `sendWithRetry` — 每事件独立重试，无目标分组速率限制）；`internal/events/webhook.go:150-172`（`sendWithRetry` — 指数退避仅作用于单次事件的多次尝试，不阻塞其他事件向同一 URL 的重试）；`internal/events/webhook.go:182-195`（`MarkWebhookSucceeded` — 10 次失败后标记成功以停止重试，但此期间已有大量并行请求发出）；`internal/repository/webhook_failures.go:12-30`（`WebhookFailure` — 结构体无 `retry_count` 或 `backoff_until` 字段）；`internal/events/webhook_test.go`（测试仅验证单事件重试逻辑，不验证多事件并行向同一 URL 的场景） |
| **3** | **S3 分段上传分片大小与并发治理缺口** —— 最小分片大小未校验、分片号可重复覆盖、无过期上传清理策略 | 协议合规/资源保护 | **P1** | S3 规范要求除最后一片外，每分片 ≥ 5MB。当前 `UploadPart` 不校验 `partNumber` 范围外的数值合法性，也不校验分片大小。两个并发请求对同一 `uploadID+partNumber` 上传不同内容时，后写入的分片覆盖前一个，无冲突检测。废弃的分段上传在存储后端不清理，累积泄漏资源 | `internal/service/file_multipart.go:48-62`（`UploadPart` — 无 `partSize` 校验，无 `partNumber` 去重校验）；`internal/api/s3compat/extra.go:62-72`（`uploadPart` — 仅校验 `1 ≤ partNumber ≤ 10000`，不校验分片最小大小）；`internal/storage/local_multipart.go`（本地存储不分片—直接拼接，无分片大小概念）；`internal/storage/s3.go:170-195`（S3 `UploadPart` — 直接透传 SDK，SDK 默认值 5MB 但服务端不校验）；`internal/repository/sql_uploads.go`（`CreateUpload` / `RecordPart` — 无 `parts_expected` 或 `parts_received` 校验逻辑）；`internal/reconcile`（Reconcile 生命周期不扫描废弃上传 — 无 `ListExpiredUploads` 或 `AbandonedUploadCleanup` 任务）；`internal/service/file_multipart.go:10-20`（`InitMultipart` — `preflightQuota` 检查配额但不检查 `maxParts`，用户可创建 <5MB 的非法分片序列） |
| **4** | **HTTP 响应缓存控制头缺失** —— 已存储对象的 GET 响应中无 Cache-Control、Expires、ETag 强验证器集成，CDN 无法有效缓存 | 性能/带宽成本 | **P2** | 当前 `writeObjectHeaders`/`handleRangeOrFull` 设置 Content-Type、Content-Length、ETag、Last-Modified、Accept-Ranges，但不设置任何缓存策略头。S3 标准响应包含 `Cache-Control: public, max-age=31536000` 等缓存头。无 `Vary: Accept-Encoding` 头，导致 CDN 在压缩场景下缓存错误版本。客户端浏览器无法利用本地缓存，每次请求都回源 | `internal/api/s3compat/handler.go:625-660`（`writeObjectHeaders` — 设置 Content-Type/ETag/Last-Modified/Accept-Ranges/Content-Encoding/Content-Disposition，**无 Cache-Control、Expires、Vary**）；`internal/api/rest/handler.go:470-500`（`handleRangeOrFull` — 同上，无缓存头）；`internal/api/s3compat/handler.go:235-260`（`GetObject` — S3 GET 未读取 `x-amz-object-attributes` 或响应缓存指令）；`internal/middleware/middleware.go`（中间件链 — CORS 中间件在 `RequestID` → `CORS` → `Auth` → `Tenant` → `RateLimit` → `OTel` → `Recoverer` → `AccessLog` 序列中，无全局缓存头注入）；`internal/config/config.go`（配置层 — `S3CompatConfig` 无 `CacheControl` 或 `DefaultCachePolicy` 字段）；`internal/repository/repository.go:Object`（Object 元数据 — 无 `CacheControl` 或 `Expires` 持久化字段） |
| **5** | **存储后端合约测试覆盖盲区** —— `contract_test.go` 仅覆盖核心 CRUD（Put/Get/Delete/Stat/List），多分片上传（Init/Upload/Complete/Abort）和预签名（PresignGet/PresignPut）在云后端上无合约验证 | 可靠性/测试 | **P2** | `storage.Storage` 接口定义了 12 个方法，但 `contract_test.go` 只测试了 5 个核心 CRUD 方法。OSS 和 COS 后端的多分片行为在 CI 中从不被测试。预签名的 URL 格式、过期行为在不同后端间可能不一致。CI gate 仅运行 SQLite + local FS 路径，OSS/COS 后端的行为差异只在生产环境暴露 | `internal/storage/contract_test.go`（合约测试 — 仅覆盖 `Put` → `Get` → `Stat` → `Delete` → `List` 循环，**无多分片、无预签名**）；`internal/storage/storage.go`（`Storage` 接口 — 12 个方法，约定 "Implementations must be safe for concurrent use" 但合约不测试并发行为）；`internal/storage/s3.go`（S3 后端的 `InitMultipart` 使用 `CreateMultipartUpload` API — 不测试 `AbortMultipart` 后分片是否清理）；`internal/storage/local.go`（local 后端的 `PresignGet` — 返回本地路径签名，非 HTTP URL，与 OSS/COS 行为不同）；`internal/storage/oss.go`（OSS 后端 — 无独立测试文件，仅 `oss_cos_test.go` 共享）；`internal/storage/cos.go`（COS 后端 — 同上）；`Makefile`（`make test` — 仅跑 local FS；`make test-integration` — 仅 Qdrant/Postgres，不涉及存储后端合约） |

---

## 方向一：同步资源拆除阻塞 HTTP 请求路径

### 产品/运营影响

| 维度 | 影响 |
|------|------|
| **HTTP 延迟膨胀** | 一个 `DELETE /v1/files/doc.pdf` 请求在有版本控制的桶上、S3 后端中，串行执行：锁检查 → `store.Delete`（S3 API 200-800ms）→ `chunkCleaner.DeleteObjectChunks`（BM25+vector 索引更新 50-200ms）→ `repo.HardDeleteObject`（50-100ms）→ `AddTenantUsage`（20-50ms）→ `emit`（20-50ms）。总计 350-1200ms 的请求处理时间中，用户可感知的"删除逻辑"只需要最后 100ms。其余都是副作用的同步等待 |
| **HTTP 连接占满** | 当大量删除并发（例如 `DeleteFolder` + 10000 个对象），单一 HTTP 连接被持有数分钟，占用 `MaxInFlight` 限流器槽位，阻塞其他用户的正常请求 |
| **`DeleteFolder` 内存爆炸** | `allKeys` 切片在 100 万对象时占用约 80-100MB 内存，加上对象元数据序列化，单请求可消耗 >500MB 堆内存 |
| **优雅关闭期删除操作被硬杀** | 15s 的 `srv.Shutdown` 超时无法等待正在执行中的 `store.Delete("s3://bucket/50GB-object")`，连接被硬中断，但后端删除仍在进行（fire-and-forget）——调用方收到 503 但删除最终完成，造成调用方认知不一致 |
| **监控盲区** | 同步拆除的耗时计入请求延迟，Operators 无法区分"请求处理慢"和"后端清理慢" |

### 现状与代码证据

**证据 1：`hardDeleteObject` —— 完全同步的拆除流水线**

```go
// internal/service/file_crud.go:250-275
func (s *FileService) hardDeleteObject(ctx context.Context, obj repository.Object, tenant, bucket, key string) error {
    // ① 锁检查（内存操作，< 1μs）✅ 合理
    if obj.LockedUntil != nil && obj.LockedUntil.After(time.Now()) {
        return ErrLocked
    }
    if obj.Metadata["_aero_legal_hold"] == "ON" {
        return ErrLocked
    }
    // ② 同步 chunk 清理（BM25 + Vector 索引，50-200ms）
    if s.chunkCleaner != nil {
        if err := s.chunkCleaner.DeleteObjectChunks(ctx, obj.ID); err != nil {
            s.logger.Warn("chunk cleanup on hard delete failed", ...)
        }
    }
    // ③ 同步存储删除（S3 API 调用，200-800ms）
    if err := s.store.Delete(ctx, obj.StorageKey); err != nil {
        return fmt.Errorf("storage delete: %w", err)
    }
    // ④ 同步数据库删除（50-100ms）
    if err := s.repo.HardDeleteObject(ctx, tenant, bucket, key); err != nil {
        return err
    }
    // ⑤ 配额回滚（best-effort，20-50ms）
    if _, qErr := s.repo.AddTenantUsage(ctx, tenant, -obj.Size, -1); qErr != nil {
        s.logger.Warn("quota decrement on hard delete failed", ...)
    }
    // ⑥ 事件发布（20-50ms）
    s.emit(ctx, obj, repository.EventDeleted)
    return nil
}
```

**证据 2：`DeleteFolder` —— 加载全部 key 进内存后再逐元素删除**

```go
// internal/api/rest/handler.go:745-780
func (h *Handler) DeleteFolder(w http.ResponseWriter, r *http.Request) {
    // ...
    allKeys := []string{}           // ❌ 无界增长
    var marker string
    for {
        page, err := h.svc.List(ctx, tenant, DefaultBucket, folderPath, marker, 1000)
        // ...
        for _, obj := range page.Objects {
            allKeys = append(allKeys, obj.Key)
        }
        if !page.HasMore { break }
        marker = page.NextMarker
    }
    results := h.svc.BatchDelete(ctx, tenant, DefaultBucket, allKeys)
    // ❌ allKeys 在逐元素删除期间持续占用内存
}
```

**证据 3：`AbortMultipart` —— 同步存储后端调用**

```go
// internal/service/file_multipart.go:181-186
func (s *FileService) AbortMultipart(ctx context.Context, uploadID string) error {
    u, err := s.repo.GetUpload(ctx, uploadID)
    // ...
    _ = s.store.AbortMultipart(ctx, sk, u.BackendUID)  // ❌ 同步云 API 调用
    return s.repo.DeleteUpload(ctx, uploadID)
}
```

**证据 4：S3 `Delete` API 在云后端上的延迟特征**

```go
// internal/storage/s3.go:120-135
// S3 DeleteObject API 单次调用延迟：
//   - AWS S3 us-east-1: ~50-200ms
//   - 跨区域（复制目标）: ~200-800ms
//   - 重试（限速/超时）: +500-2000ms
// 平均 300ms，P99 > 2s
```

**证据 5：存储层熔断器不参与请求级决策**

```go
// internal/storage/circuitbreaker.go
// CBState 只用于拒绝新的存储操作，但不提供"排入后台队列"的异步逃生方案
// 调用方（FileService）在熔断器打开时收到 ErrBackendUnavailable，只能向上返回 500
```

### 架构权衡

**方案 A：异步拆除队列（推荐）**

为每个需要异步拆除的操作定义 Job 类型（`delete_object`、`delete_bucket`、`delete_upload`），由统一的 JobPool 消费：

| 步骤 | 当前（同步） | 建议（异步） |
|------|------------|------------|
| 用户请求 | `DELETE /v1/files/key` → 200ms 后收到响应 | `DELETE /v1/files/key` → 立即返回 202 Accepted + `job_id` |
| 存储删除 | 请求 goroutine 内 `store.Delete` | Job worker 执行 `store.Delete` |
| Chunk 清理 | 请求 goroutine 内 `chunkCleaner` | Job worker 执行（在存储删除之前或之后，取决于语义） |
| 配额回滚 | 请求后同步 | Job worker 执行 |
| 事件通知 | `emit → event.created` | Job worker 执行 |
| 失败处理 | 客户端收到 500，部分操作已完成（部分失败） | Job 重试机制保证最终一致性 |

**边界情况：**
- **锁检查发生在请求时还是 Job 执行时？** 请求时做初步检查（当前锁定状态），Job 执行时再次检查（可能已被其他操作修改）。两阶段检查防 TOCTOU。
- **重复删除请求：** 幂等性 key 在请求层去重，确保同一 key 不生成两个 job。
- **`DeleteFolder` 拆分为多个 Job：** 每页 1000 个 key 生成一个 `delete_batch` job，避免大内存占用。
- **依赖顺序：** 先删 chunk → 再删 storage → 再删 repo row → 再减配额。如 storage 删失败，repo 不删（幂等重试）。
- **回滚策略：** 若 storage 删成功但 repo 删失败，通过 reconcile 清理孤儿（已有机制）。

**方案 B：轻量级 Go 协程池 + WaitGroup**

对每次拆除启动一个后台 goroutine，用 `errgroup` 收集结果。请求在 store.Delete 后即返回，后续清理后台执行。

**权衡：** 无持久化保证（goroutine 崩溃后任务丢失）；无重试机制；无跨副本协调。

---

## 方向二：Webhook 重试系统缺乏每目标端点的熔断器与速率限制

### 产品/运营影响

| 维度 | 影响 |
|------|------|
| **级联故障风险** | 当 Webhook 目标开始返回 5xx，所有事件处理器同时向故障端点发起重试。1000 个并发 `object.created` 事件 → 1000 个并发出站 HTTP POST → 目标被压垮 → 重试加剧 → 系统各 Webhook 整体延迟增加 |
| **无故障隔离** | 共享的 `NextPendingFailures` 轮询队列不区分目标 URL——一个故障端点的事件可以堵塞整个重试管道，健康目标的事件被迫等待 |
| **运维可观测性缺口** | 无法回答："给定 webhook URL，过去 5 分钟有多少投递成功/失败？平均延迟多少？当前是否被熔断？" |
| **成本失控** | 故障端点的无限制重试消耗出站带宽和第三方 API 调用次数（对计费 API 尤其危险） |

### 现状与代码证据

**证据 1：`sendWithRetry` 没有目标级速率控制**

```go
// internal/events/webhook.go:150-172
func (w *Webhook) sendWithRetry(ctx context.Context, key string, attempt int) error {
    // 单次事件的指数退避（0.5s, 1s, 2s, 4s, ...）
    // 但不同事件向相同 URL 的退避是独立的，互不感知
    // 100 个并发事件 → 100 个独立的退避序列 → 全部在前 0.5s 内发出
}
```

**证据 2：`NextPendingFailures` 返回全局未区分的事件**

```go
// internal/repository/webhook_failures.go
func (s *sqlStore) NextPendingFailures(ctx context.Context, limit int) ([]WebhookFailure, error) {
    // SELECT * FROM webhook_failures WHERE next_retry_at <= now() ORDER BY next_retry_at LIMIT $1
    // ❌ 不区分目标 URL，不按 URL 分组限流
}
```

**证据 3：`Webhook` 结构体无每目标状态追踪**

```go
// internal/events/webhook.go:60-80
type Webhook struct {
    repo           repository.Repository
    logger         *slog.Logger
    url            string   // 单一全局 webhook URL
    maxRetries     int
    // ❌ 无 map[string]*targetState（每 URL 失败计数、熔断状态、速率限制器）
}
```

**证据 4：全局单一 Webhook URL 已经限制了多目标扩展**

`EVENTS_WEBHOOK_URL` 是一个环境变量，指向单一 URL。如果要支持 S3 风格的多个通知目标（多个 SQS、SNS、Lambda），当前架构需要扩展到多 URL。在扩展之前，熔断和限流是必要前提。

### 架构权衡

**方案 A：每目标熔断器 + 令牌桶速率限制器**

| 组件 | 实现 |
|------|------|
| **目标状态追踪** | `targetState{ url, consecutiveFailures, lastFailureTime, breakerState, rateLimiter *rate.Limiter }`，全局 `sync.Map` 或带 GC 的 LRU |
| **熔断器阈值** | 连续 5 次失败 → 打开（暂停 30s）；半开后单次成功 → 关闭 |
| **速率限制** | 每目标每秒最多 N 次（可配置），基于 `rate.Limiter`（已有 `middleware/ratelimit.go` 可复用） |
| **死信区分** | `WebhookFailure` 增加 `status` 字段（`pending`/`succeeded`/`dead_letter`/`circuit_breaked`），替代当前 `succeeded bool` 的二值语义 |

**边界情况：**
- **URL 变更：** 当 `EVENTS_WEBHOOK_URL` 更新时重置熔断器（新 URL 从健康状态开始）。
- **部分失败：** 若目标偶尔成功（80% 成功率），熔断器应在半开状态（成功率 > 阈值 → 关闭；< 阈值 → 继续打开）。
- **背压传播：** 当 Webhook 目标熔断打开时，`bus.Publish` 应返回非致命错误，让事件生产者（FileService）知晓投递延迟——当前 `emit` 吞没所有错误。
- **清理：** 长时间不活跃的目标状态应从内存中清理（GC 周期 > 1h 不活跃的 target）。

**方案 B：全局重试队列分区（与方案 A 互补）**

按 URL hash 将 `webhook_failures` 表分区，每个 URL 有独立的重试 goroutine（`for { nextPendingByURL(url) → process }`），天然隔离故障。

---

## 方向三：S3 分段上传分片大小与并发治理缺口

### 产品/运营影响

| 维度 | 影响 |
|------|------|
| **资源耗尽向量** | 客户端可创建数百万个 <5MB 的分片（甚至 1KB 分片），`RecordPart` 在数据库中累积大量行，存储后端保留大量临时分片数据。S3 的标准行为是拒绝 <5MB 的分片（最后一片除外） |
| **数据完整性问题** | 两个并发请求上传同一 `uploadID+partNumber` 时静默覆盖。`CompleteMultipart` 使用 `ListParts(order by part_number ASC)` 拉取当前数据库中的分片列表——若分片 A 被分片 B 覆盖，`CompleteMultipart` 会包含分片 B 的内容但分片 A 的请求已返回成功，客户端认为分片 A 已上传成功 |
| **废弃上传泄漏** | 已初始化的多分片上传若从未提交或中止（客户端崩溃），存储后端的分片数据永远不被清理。当前 Reconcile 组件的 `LifecycleJob` 不扫描废弃的上传记录 |
| **缺少 UploadPartCopy** | 大于 5GB 的跨键复制只能通过 multipart + UploadPartCopy 完成，但后者未实现（详见方向二覆盖，此处聚焦上传治理） |

### 现状与代码证据

**证据 1：`UploadPart` 不检验分片大小**

```go
// internal/service/file_multipart.go:48-62
func (s *FileService) UploadPart(ctx context.Context, uploadID string, partNumber int32, r io.Reader, size int64) (repository.PartRecord, error) {
    u, err := s.repo.GetUpload(ctx, uploadID)
    // ...
    // ❌ 无 size 校验：size=1 也通过，S3 要求 ≥ 5MB（最后一片除外）
    // ❌ 无 partNumber 去重校验：两次上传相同 partNumber 静默覆盖
    part, err := s.store.UploadPart(ctx, sk, u.BackendUID, partNumber, r, size)
    // ...
    // ❌ 无 preflightCheck（配额检查在 CompleteMultipart 才执行，非 UploadPart 时）
}
```

**证据 2：S3 handler 的最小分片校验已休眠**

```go
// internal/api/s3compat/extra.go:62-72
func (h *Handler) uploadPart(w http.ResponseWriter, r *http.Request, uploadID string, partNumber int) {
    if partNumber < 1 || partNumber > 10000 {
        writeS3Error(w, r, fmt.Errorf("%w: partNumber must be between 1 and 10000", ...))
        return
    }
    // ✅ partNumber 范围校验存在
    // ❌ 但 size < 5MB（非最后一片）不校验
    // ❌ r.ContentLength = -1（chunked upload）时完全无大小概念
}
```

**证据 3：Reconcile 不扫描废弃上传**

```go
// internal/reconcile/lifecycle.go:16-90
func (j *LifecycleJob) sweepExpired(ctx context.Context) {
    // 仅处理 lifecycle expire_after_days → soft_delete/hard_delete
    // ❌ 无 ListUploadsOlderThan(ctx, time.Hour*24*7) 调用
    // ❌ 无 CleanupOrphanParts(ctx, before) 扫描
}
```

**证据 4：`CompleteMultipart` 的 parts 数据竞争窗口**

```go
// internal/service/file_multipart.go:117-145
func (s *FileService) CompleteMultipart(ctx context.Context, uploadID string) (repository.Object, error) {
    u, err := s.repo.GetUpload(ctx, uploadID)
    // ...
    parts, err := s.repo.ListParts(ctx, uploadID)
    // ↑ 从 DB 读取当前分片列表（可能被并发 UploadPart 修改）
    // ↓ 发送给 storage backend 合并
    storageParts, total := buildPartList(parts)
    info, err := s.store.CompleteMultipart(ctx, sk, u.BackendUID, storageParts)
    // ❌ 在 ListParts 和 CompleteMultipart 之间的时间窗口，可能有新的 UploadPart 写入
    //    但 storage backend 的合并操作使用自己在 CompleteMultipart 时组装的分片
    //    → 数据库中的分片列表与存储后端实际装配的内容不一致
}
```

### 架构权衡

**方案 A：UploadPart 时按 S3 规范校验**

| 校验点 | 当前行为 | 建议 |
|--------|---------|------|
| `size < 5MB` 且非最后一片 | 通过 | 拒绝，返回 `EntityTooSmall` |
| `partNumber` 已存在 | 静默覆盖 | 拒绝或按 S3 行为覆盖（但记录审计日志） |
| 上传持续时间 > 7 天 | 允许 | 后端自动中止过期上传（`AbortMultipart`） |

**边界情况：**
- **最后一片判定：** `UploadPart` 时不知道是否最后一片（只有 `CompleteMultipart` 才知道哪些分片是最终的）。方案：在 `UploadPart` 时不校验最后一片逻辑；在 `CompleteMultipart` 时校验所有分片（除最后一个外）是否 ≥ 5MB。
- **chunked transfer encoding：** 当 `Content-Length` 为 -1 时无分片大小信息。方案：要求 multipart upload 必须有 `Content-Length` 头，或流式读取直到知道实际大小再验证。
- **并发覆盖 vs S3 行为：** AWS S3 允许多次上传同一 partNumber（后覆盖前），最后上传的分片在 Complete 时生效。当前实现已符合此行为，但需要文档化（幂等性保证）。
- **并发 Complete：** 多次 `CompleteMultipart` 对同一 uploadID 当前会创建多个对象（版本化桶生成多余版本）。需要幂等性保护。

**方案 B：废弃上传 Reconcile 任务**

```go
// 新增 Reconcile 任务：CleanupOrphanUploads
// 1. SELECT id FROM uploads WHERE created_at < now() - 7d
// 2. 对每个过期上传调用 AbortMultipart（存储后端清理 + 删除 upload 行）
// 3. 每周期运行一次（RECONCILE_INTERVAL_MINUTES）
```

---

## 方向四：HTTP 响应缓存控制头缺失

### 产品/运营影响

| 维度 | 影响 |
|------|------|
| **CDN 缓存效率下降** | 无 `Cache-Control` 头时 CDN 降级为启发式缓存（通常仅缓存 304 响应）。对于公开可读的静态资源（图片、CSS/JS、文档），每次请求都回源，增加源站负载和出口带宽成本 |
| **浏览器缓存利用率低** | 无 `Cache-Control: max-age` 或 `Expires` 头时，浏览器每次请求都发送 `If-None-Match`，即使对象未修改也需要服务端返回 304 |
| **无 `Vary` 头导致缓存错误** | 当服务端返回 `Content-Encoding: gzip` 和 `Accept-Encoding` 协商时，缺少 `Vary: Accept-Encoding` 头导致 CDN 向不支持 gzip 的客户端返回 gzip 压缩的内容 |
| **S3 协议兼容缺口** | AWS S3 标准行为是在 PUT 时记录 `x-amz-object-cache-control`，并在 GET 时原样返回。当前系统忽略该头 |

### 现状与代码证据

**证据 1：`writeObjectHeaders` 不写缓存头**

```go
// internal/api/s3compat/handler.go:625-660
func writeObjectHeaders(w http.ResponseWriter, contentType string, size int64, etag, lastModified, storageClass string, meta map[string]string) {
    if contentType != "" {
        w.Header().Set("Content-Type", contentType)
    }
    if size > 0 {
        w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
    }
    w.Header().Set("ETag", `"`+etag+`"`)
    w.Header().Set("Last-Modified", lastModified)
    w.Header().Set("Accept-Ranges", "bytes")
    if storageClass != "" && storageClass != service.DefaultStorageClass {
        w.Header().Set("x-amz-storage-class", storageClass)
    }
    if v, ok := meta["_aero_content_disposition"]; ok && v != "" {
        w.Header().Set("Content-Disposition", v)
    }
    if v, ok := meta["_aero_content_encoding"]; ok && v != "" {
        w.Header().Set("Content-Encoding", v)
    }
    // ❌ 无 Cache-Control
    // ❌ 无 Expires
    // ❌ 无 Vary
    // ❌ 无 x-amz-tag-count
}
```

**证据 2：S3 客户端期望的缓存头**

`aws s3 cp s3://bucket/doc.pdf .` 会继承服务端返回的 `Cache-Control` 头。主流 S3 SDK（aws-sdk-go v2 的 `s3.GetObjectInput` 和 `PutObjectInput` 都支持 `CacheControl` 字段。当前忽略意味着 S3-compat 层的语义断裂。

**证据 3：`x-amz-object-cache-control` 请求头被忽略**

```go
// internal/api/s3compat/handler.go:104-108
// PutObject 解析的请求头：x-amz-copy-source/tagging/uploadId/acl/restore/Content-Type/Content-MD5/x-amz-storage-class/x-amz-acl/x-amz-object-lock-legal-hold
// ❌ 没有 x-amz-object-cache-control 或 Cache-Control 请求头
```

**证据 4：`ValidateMetadata` 不禁止 `cache-control` 元数据**

```go
// internal/service/file.go:105-120
func validateMetadata(meta map[string]string) error {
    for k, v := range meta {
        if strings.HasPrefix(k, "_aero_") {
            continue  // 跳过系统键
        }
        // cache-control 不是系统键前缀，但应被识别为特殊元数据
    }
}
```

### 架构权衡

**方案 A：PUT 时存储缓存策略 + GET 时原样回放**

| 组件 | 实现 |
|------|------|
| **PUT 路径** | 在 `PutObject` / `Put` handler 解析 `Cache-Control` 和 `Expires` 请求头，存入对象元数据（如 `_aero_cache_control` 系统键） |
| **GET 路径** | `writeObjectHeaders` 读取 `_aero_cache_control` 并设置 `Cache-Control` 响应头 |
| **默认策略** | 可配置默认 `Cache-Control`（环境变量 `S3_DEFAULT_CACHE_CONTROL`），对未设置缓存策略的对象应用此默认值 |

**边界情况：**
- **私有对象不应被 CDN 缓存：** 对于非公开 ACL 的对象，即使有 `Cache-Control: public` 也应忽略或覆盖为 `private`。
- **Range 请求：** `Cache-Control: no-cache` 应自动追加到 206 Partial Content 响应（因不同 Range 返回不同内容）。
- **浏览器兼容：** `Cache-Control: immutable` 对不可变版本化对象（`doc.pdf?v=abc123`）可启用一年缓存。
- **与 SSE 加密冲突：** SSE 加密的对象不应被 CDN 缓存（因不同请求者可能使用不同密钥）。建议检测到 SSE-KMS/SSE-C 时自动覆盖为 `Cache-Control: private`。

---

## 方向五：存储后端合约测试覆盖盲区

### 产品/运营影响

| 维度 | 影响 |
|------|------|
| **多分片上传在云后端行为不确定** | `InitMultipart` → `UploadPart` → `CompleteMultipart` → `Get` 的端到端流程在 S3/OSS/COS 上各有不同的实现路径。无合约测试确保：分片顺序正确性、最小分片数量的边缘情况、中止后分片清理 |
| **预签名 URL 跨后端不一致** | Local 后端的 `PresignGet` 返回本地签名路径（`/presign/{key}?expires=...&hmac=...`），而 S3 后端返回 AWS 预签名 URL（`https://s3.amazonaws.com/bucket/key?X-Amz-Algorithm=...`）。合约测试应验证：URL 格式、有效期行为、使用预签名 URL 的实际 GET 操作 |
| **OSS/COS 后端从未在 CI 中验证** | Local 和 S3 后端在 CI 中有测试。OSS 和 COS 后端的偏离行为只在生产环境暴露。`NewOSS`/`NewCOS` 的配置参数错误在部署时才被发现 |
| **并发安全未验证** | `Storage` 接口约定 "Implementations must be safe for concurrent use"，但合约测试不验证并发 Put/Get 同一 key 的行为 |

### 现状与代码证据

**证据 1：`contract_test.go` 仅覆盖核心 5 个方法**

```go
// internal/storage/contract_test.go
func RunContractTests(t *testing.T, s Storage) {
    t.Run("Put-and-Get", ...)
    t.Run("Stat", ...)
    t.Run("Delete", ...)
    t.Run("List", ...)
    t.Run("Overwrite", ...)
    // ❌ 无 Multipart
    // ❌ 无 Presign
    // ❌ 无 Concurrent access
}
```

Storage 接口有 12 个方法，合约测试仅覆盖其中 5 个：

| 方法 | 合约测试 | 实现偏离风险 |
|------|---------|-------------|
| `Put` | ✅ | — |
| `Get` | ✅ | — |
| `Stat` | ✅ | — |
| `Delete` | ✅ | — |
| `List` | ✅ | — |
| `PresignGet` | ❌ | Local 返回路径签名 vs S3 返回 AWS URL |
| `PresignPut` | ❌ | 同上 |
| `InitMultipart` | ❌ | S3 需要 CreateMultipartUpload API，local 仅创建临时目录 |
| `UploadPart` | ❌ | S3 需要 UploadPart API，local 写临时文件 |
| `CompleteMultipart` | ❌ | S3 需要 CompleteMultipartUpload，local 拼接文件 |
| `AbortMultipart` | ❌ | S3 需要 AbortMultipartUpload，local 清理临时文件 |
| `Backend` | ❌ | 纯字符串，无偏离风险 |

**证据 2：`local_test.go` 有多分片测试但 OSS/COS 无**

```go
// internal/storage/local_test.go
func TestLocalMultipart(t *testing.T) { ... }  // ✅ local 有
func TestLocalPresign(t *testing.T) { ... }     // ✅ local 有

// internal/storage/oss_cos_test.go
func TestOSSBasics(t *testing.T) { ... }        // ❌ 仅有可选测试，无 CI 集成
func TestCOSBasics(t *testing.T) { ... }         // ❌ 同上
```

**证据 3：`PresignGet` 返回格式因后端而异**

```go
// internal/storage/local.go
func (s *LocalStorage) PresignGet(ctx context.Context, key string, expiry time.Duration) (string, error) {
    // 返回类似 /presign/default/default/doc.pdf?expires=1234567890&hmac=abc123
    // 使用的是本地签名，不是 HTTP URL
}

// internal/storage/s3.go
func (s *S3Storage) PresignGet(ctx context.Context, key string, expiry time.Duration) (string, error) {
    // 使用 s3.PresignClient.PresignGetObject
    // 返回 https://s3.us-east-1.amazonaws.com/bucket/key?X-Amz-Algorithm=...
    // 是完整的可访问 HTTP URL
}
```

### 架构权衡

**方案：扩展 `RunContractTests` 覆盖全部 12 个方法**

| 新增测试组 | 验证内容 | 边界情况 |
|-----------|---------|---------|
| `TestMultipartLifecycle` | Init → UploadPart(3 parts) → CompleteMultipart | 最小 part 数（1-part）、多 part（10000-part 理论值但限制为 10-part 以避免慢测试） |
| `TestUploadPartOverwrite` | 两次 UploadPart 同一 partNumber | 后写入覆盖前一个（S3 语义）；CompleteMultipart 最终合并最后写入的内容 |
| `TestAbortMultipart` | Init → UploadPart → Abort → ListParts 验证 | Abort 后 ListParts 返回空；已上传的分片被后端清理 |
| `TestPresignGet` | PresignGet → 使用 URL 的 HTTP GET | URL 有效期内的可访问性；过期 URL 的 403/404 |
| `TestPresignPut` | PresignPut → 使用 URL 的 HTTP PUT → Get 验证 | PUT 成功后 Get 返回正确内容 |
| `TestConcurrentAccess` | 10 个 goroutine 同时 Put/Get/Delete 同一 key | 无 panic、无数据损坏（不保证顺序） |
| `TestZeroByteObject` | Put("", size=0) → Get → Stat | 空内容的正确处理 |

**CI 集成：**
- `make test` 对 local 后端运行全部合约测试（含新增测试）
- `make test-integration` 需要 S3/MinIO 凭据时运行 S3 合约测试
- OSS/COS 的合约测试文档化运行指南（需要对应云资源），不在 CI gate 中

**权衡：**
- 多分片测试涉及多个 API 调用，测试时间从 <1s 增加到 3-5s（local 后端）。可接受。
- S3/MinIO 集成测试需要 Docker 或网络凭据，维护成本增加。
- OSS/COS 测试需要真实的云资源，仅手工运行。

---

## 总结

| 方向 | 核心价值 | 推荐实施顺序 | 预估工作量 |
|------|---------|------------|-----------|
| **方向一**：同步拆除异步化 | 防止请求处理路径被无界 I/O 阻塞，提升 HTTP 并发容量和宕机恢复能力 | **P0 — 热修复** | 中等（Job 类型定义 + async 拆除 handler + `DeleteFolder` 分批处理） |
| **方向二**：Webhook 熔断 | 防止故障级联扩散，保护终端目标不被重试淹没，提升系统可靠性 | **P0 — 热修复** | 小（`targetState` + 令牌桶 + 死信状态字段） |
| **方向三**：分片上传治理 | 防止资源耗尽攻击，提升多分片上传数据完整性，清理废弃空间 | **P1 — 下一迭代** | 中等（5MB 校验 + 废弃上传 Reconcile + `UploadPartCopy` 初步实现） |
| **方向四**：缓存控制头 | 降低带宽成本，提升 CDN/浏览器缓存效率，补齐 S3 协议语义 | **P2 — 产品增强** | 小（元数据存储 + 响应头回放 + 默认策略） |
| **方向五**：合约测试覆盖 | 防止云后端部署后才发现行为差异，提升新后端实现的安全性 | **P2 — 基础设施** | 中等（7 个新测试组 + S3/MinIO CI 集成） |

> **实施建议：** 方向一和方向二可同时进行（不冲突），方向三可并行开始分析废弃上传的 SQL 查询。方向四和方向五可并行开展但需不同技能组合（HTTP 协议 vs 测试基础设施）。
