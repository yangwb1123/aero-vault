# AeroVault 高价值扩展方向（第四期）

> **视角:** 资深架构师 / 产品经理  
> **方法:** 全局代码扫描（129 非测试 Go 文件 / ~23K 行源码）+ 深度分析所有层  
> **日期:** 2026-07-10  
> **原则:** 选取 **ROADMAP + 八轮 analysis-v[1-8] + 前三期 expansion-directions 均未覆盖** 的方向。每个方向附带具体代码位置、当前状态缺口和实现理由。

---

## 总览

| # | 方向 | 类型 | 影响 | 当前状态 | 前置 |
|---|------|------|------|---------|------|
| 1 | **Egress Governance & Multi-DC Traffic Management** | 成本/架构 | 🔴 企业多租户硬性需求 | 零实现；仅有单向 RPS 限流 | Middleware RateLimiter |
| 2 | **Comprehensive Lifecycle: Version Cleanup, Multipart GC & Delete Marker Mgmt** | 成本/运维 | 🔴 存储无限增长风险 | 仅 `ExpireAfterDays` 删除；无版本/分片/删除标记管理 | reconcile.LifecycleJob |
| 3 | **API Governance & Client SDK Maturity** | 平台/生态 | 🟠 产品化最后一步 | 单版本 `/v1`、无弃用框架、标准化 pagination/rate-limit 头缺失 | OpenAPI |
| 4 | **Active-Active Multi-Region Replication & Geo-Distribution** | 架构/可用性 | 🔴 跨区域生产必备 | 仅单向异步复制；无读路由、冲突解决、数据主权 | replication.Worker |
| 5 | **Storage Cost & Usage Analytics Engine** | 成本/智能 | 🟠 从存储进化为数据资产管理器 | 零实现；只有基础 `BucketStats` 和 `StorageClassCounts` | Telemetry |

---

## 1. Egress Governance & Multi-DC Traffic Management

### 为什么需要它

当前代码库的流量管控止步于**请求级别**：`RATE_LIMIT_RPS`（每租户每秒请求数）和 `ConcurrencyLimiter`（全局/每租户并发数）。但多租户对象存储的核心成本与公平性问题来自**带宽**而非请求数：

- **一个租户下载 10TB 可以打满服务器出口带宽，导致所有其他租户的服务质量下降**——当前代码对此毫无防护。
- **没有每租户带宽配额**（bytes/sec 或每日/每月总出口字节数），意味着无法做带宽计费或公平调度。
- **没有 CDN 集成**：企业用户需要将存储作为 CDN 的 origin（CloudFront / Cloudflare / Fastly），需要 origin-pull 身份验证、签名 URL 集成、以及 CORS 配置的自动同步。
- **没有多区域读路由**：如果部署跨多个数据中心，用户应自动被路由到延迟最低的区域读取。
- **没有 Requester Pays 模型**：对于共享数据集（如公开科学数据），带宽成本应由请求方承担而非存储所有者。

这是多租户对象存储产品化的**准入级缺口**。AWS S3 有完善的数据传输定价 + CloudFront 集成 + 请求者付费模式。缺少这些，企业无法将其用作 CDN origin 或共享数据平台。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/middleware/ratelimit.go` | 每租户 RPS token-bucket | 无带宽（bytes/sec）限流 |
| `internal/middleware/middleware.go:ConcurrencyLimiter` | 全局+每租户并发限制 | 无出口字节计量 |
| `internal/service/file_crud.go:Get` | 流式读取 | 不计量/限制出口带宽 |
| `internal/service/file_features.go:SetBucketCORS` | 桶级 CORS 配置 | 仅存储，不对外暴露为 CDN 配置 |
| `internal/api/rest/handler.go:Get` / `serveObjectContent` | 全量 GET 响应 | 无带宽计量中间件 |
| `internal/api/s3compat/handler.go:GetObject` | S3 GET | 无带宽计量 |
| `internal/telemetry/metrics.go` | 请求级 metrics | 无 `egress_bytes_total{tenant}` |
| `internal/config/config.go` | 配置结构体 | 无带宽配置节 |
| `internal/repository/repository.go:TenantQuota` | 字节/对象配额 | 无 `egress_bytes_budget` 字段 |

### 架构蓝图

```
┌─ Egress Meter & Limiter ───────────────────────────────────────│
│ 新 middleware: EgressLimiter                                    │
│   职责:                                                          │
│     1. 每次 GET/HEAD 响应写入时，记录写入字节数到租户计量器         │
│     2. 当租户超过带宽配额时，延迟或拒绝新读取请求                   │
│     3. 带宽配额重置周期：日/月（可配置）                          │
│                                                                │
│ 计量方案（Token-bucket 变体）：                                    │
│   每个租户一个带宽桶：                                             │
│     - refill_rate: N bytes/sec（持续带宽上限）                    │
│     - burst: N bytes（瞬时突发上限）                              │
│   每次响应写 N bytes → 从桶中扣除 N bytes                         │
│   桶空 → 响应返回 429 + Retry-After                              │
│                                                                │
│ 配置:                                                            │
│   RATE_LIMIT_EGRESS_BYTES_PER_SEC=10485760  # 10 MB/s per tenant │
│   RATE_LIMIT_EGRESS_BURST=52428800          # 50 MB burst        │
│   RATE_LIMIT_EGRESS_DAILY_BYTES=107374182400 # 100 GB/day cap    │
└────────────────────────────────────────────────────────────────┘

┌─ CDN Origin Integration ───────────────────────────────────────│
│ 模式: aero-vault 作为 CDN 的 origin                             │
│                                                                │
│ 问题: CDN origin-pull 需要:                                      │
│   1. 无认证的 GET（或预签名 URL）                                   │
│   2. 正确的 Cache-Control 响应头                                   │
│   3. ETag/Last-Modified 的条件请求                                  │
│   4. 高吞吐量的 Range 请求（分片缓存）                               │
│                                                                │
│ 解决方案:                                                       │
│   - 新增 "CDN mode" 配置（STORAGE_CDN_MODE=true）                │
│     允许预定义的 CIDR/IP 范围免认证 GET（CDN 回源 IP）             │
│   - 自动设置 Cache-Control 响应头（基于桶配置或对象元数据）         │
│   - 优化 Range 请求路径（避免随机读取时的 IO.CopyN 浪费）           │
│   - CDN 鉴权: 预签名 URL + CDN 回源 IP 白名单                     │
│                                                                │
│ REST API 扩展:                                                  │
│   PUT /v1/buckets/{bucket}/cdn                                  │
│     { "enabled": true, "cache_control": "public, max-age=3600", │
│       "allowed_cidrs": ["10.0.0.0/8"],                          │
│       "signed_url_required": false }                            │
│                                                                │
│   GET /v1/buckets/{bucket}/cdn → 返回当前 CDN 配置               │
└────────────────────────────────────────────────────────────────┘

┌─ Multi-Region Read Routing ────────────────────────────────────│
│ 场景: 部署在 us-east-1 + eu-west-1 + ap-southeast-1              │
│       用户从欧洲请求 → 自动路由到 eu-west-1 读取                   │
│                                                                │
│ 方案: 轻量级全局读路由层                                           │
│   type RegionRouter struct {                                    │
│       regions []Region                                          │
│       strategy   string // "latency" | "geo" | "round-robin"    │
│       geoDB      *GeoIP  // 可选 GeoIP 数据库                    │
│   }                                                             │
│                                                                │
│   type Region struct {                                          │
│       Name    string                                            │
│       BaseURL string   // 本区域 aero-vault API endpoint        │
│       Egress  int64    // 当前区域出口余量（bytes）               │
│       Healthy bool                                              │
│   }                                                             │
│                                                                │
│ 请求流程:                                                        │
│   GET /v1/files/doc.pdf                                         │
│   → ReadRouter 检查请求来源 IP                                   │
│   → 按策略选择最佳区域                                            │
│   → 302 重定向到该区域的 endpoint（或透明代理转发）                 │
│   → 响应包含 X-Aero-Region: eu-west-1 头                         │
│                                                                │
│ 配置:                                                            │
│   STORAGE_REGIONS=us-east-1=https://us.aero.dev,                │
│                   eu-west-1=https://eu.aero.dev                 │
│   STORAGE_REGION_ROUTER=geo      # geo | latency | proxy        │
└────────────────────────────────────────────────────────────────┘

┌─ Requester Pays Model ─────────────────────────────────────────│
│ 场景: 公开数据集（如科学数据），带宽费由请求方承担                   │
│                                                                │
│ 桶级配置:                                                       │
│   PUT /v1/buckets/{bucket}/requester-pays                       │
│     { "enabled": true }                                         │
│                                                                │
│ 请求流程:                                                        │
│   1. 客户端发现 403（BucketRequesterPays）                       │
│   2. 客户端在请求头中添加 x-amz-request-payer: requester         │
│   3. 服务端识别请求方身份，标记 egress 计入请求方租户              │
│   4. 如果请求方也是 aero-vault 租户，直接从其带宽配额扣除           │
│   5. 如果请求方非租户，要求预付费或使用预签名 URL                  │
│                                                                │
│ S3 兼容:                                                        │
│   ?x-amz-request-payer=requester 在 GET/HEAD/POST 中            │
│   响应头: x-amz-request-charged: requester                      │
└────────────────────────────────────────────────────────────────┘

**边界情况：**
- 带宽计量精确到字节，但 HTTP 响应头、chunked encoding 开销不计入（只计对象 body bytes）
- Range 请求只计量实际发送的字节数，不计量整个对象大小
- 并发大下载时，带宽桶空 → 429 但不应断开已有连接（只拒绝新请求）
- CDN origin 白名单 IP 的 GET 应 bypass 带宽计量（CDN 已承担出口）
- 跨区域复制（replication.go）的出口流量不应计入租户带宽配额（系统开销）
- Requester Pays 模式下，匿名请求不能绕过：即使公开读也必须声明 `x-amz-request-payer`

**复杂度:** L-M · **用户影响:** ★★★★★（多租户公平性） · **代码变更:** ~1200 行新代码 + ~400 行修改

---

## 2. Comprehensive Lifecycle: Version Cleanup, Multipart GC & Delete Marker Management

### 为什么需要它

当前生命周期实现（`internal/reconcile/lifecycle.go`）只做一件事：**到期删除**——根据 `ExpireAfterDays` 软删除或硬删除对象。但 S3 生命周期规范定义了一个更丰富的能力集，这些能力的缺失直接导致**存储成本不可控**：

- **版本化桶无限增长**：打开 versioning 后，每次 PUT 创建新版本，旧版本永远不删除。一个每天更新 1000 次的文件，一年产生 365K 个版本。如果没有版本清理，存储增长是版本化前的 N 倍。
- **分片上传碎片堆积**：`InitMultipart` 创建上传会话，但 `AbortMultipart` 可能永远不会被客户端调用（连接中断、客户端 crash）。这些分片在 S3/OSS/COS 后端永久占用存储，且**没有清理机制**。当前 `reconcile` 清扫的是 `objects` 引用的 key，但 `abandoned uploads` 的存储 key 不在 objects 表中。
- **删除标记堆积**：版本化桶中，`DELETE` 创建删除标记（delete marker）而非真正删除。这些标记本身不占存储，但在 listing 中持续出现，干扰结果。S3 生命周期支持 `ExpiredObjectDeleteMarker` 清理。
- **缺少对象的生命周期规则可视化**：用户无法看到"这个桶的生命周期规则将如何处理我的对象"。

代码库中 `Object.StorageClass`、`BucketConfig.ExpireAfterDays`/`ExpireAction` 结构已经存在，`reconcile` 框架（`LifecycleJob`/`RetentionJob`）足够健壮——但只有**删除动作**实现了，存储成本相关的其余生命周期动作全是空位。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/reconcile/lifecycle.go` | `ListExpired` + `store.Delete` | 无版本清理、分片GC、删除标记 |
| `internal/reconcile/retention.go` | 软删除清除 | 与 lifecycle 分离，无版本维度 |
| `internal/repository/repository.go:ListExpired` | 查 `ExpireAfterDays` 过期行 | 无版本过滤 |
| `internal/service/file_multipart.go:InitMultipart` | 创建上传记录 | 无上传会话超时/TTL |
| `internal/service/file_multipart.go:UploadPart` | 记录分片 | 无 orphan 清理 |
| `internal/repository/repository.go:ListUploads` | 列出上传 | 无过期上传清除 |
| `internal/repository/sql_buckets.go:SetBucketLifecycle` | 存储生命周期规则 | 仅 expire，无版本/分片规则 |
| `internal/api/s3compat/bucketconfig.go` | S3 生命周期 XML 解析 | 只解析了 expiration |
| `internal/repository/repository.go:BucketConfig` | `ExpireAfterDays` + `ExpireAction` | 无 `NoncurrentVersionExpiration`、`AbortIncompleteMultipartUpload` |

### 架构蓝图

```
┌─ 生命周期类型扩展 ─────────────────────────────────────────────│
│ 当前: ExpireAfterDays + ExpireAction                             │
│ 扩展后:                                                          │
│                                                                │
│ BucketConfig 新增字段:                                           │
│   NoncurrentVersionExpiration *NoncurrentVersionRule            │
│   AbortIncompleteMultipartUpload *AbortMPURule                  │
│   ExpiredObjectDeleteMarker     *ExpiredDelMarkerRule           │
│   TransitionRules               []TransitionRule (见 v3 #1)     │
│                                                                │
│ type NoncurrentVersionRule struct {                              │
│     NoncurrentDays int  // 非当前版本保留天数                    │
│     NewerVersions   int  // 保留最近 N 个版本（0 = 禁用）         │
│ }                                                               │
│                                                                │
│ type AbortMPURule struct {                                       │
│     DaysAfterInitiation int  // 初始化后 X 天未完成则中止        │
│ }                                                               │
│                                                                │
│ type ExpiredDelMarkerRule struct {                               │
│     ExpiredObjectDeleteMarker bool  // 自动清理过期删除标记      │
│ }                                                               │
│                                                                │
│ type LifecycleRule struct {                                      │
│     ID            string                                        │
│     FilterPrefix  string                                        │
│     FilterTags    map[string]string                             │
│     Status        string   // Enabled | Disabled                │
│     Expiration    *Expiration                                   │
│     Noncurrent    *NoncurrentVersionRule                        │
│     AbortMPU      *AbortMPURule                                 │
│     Transitions   []Transition                                   │
│ }                                                               │
└────────────────────────────────────────────────────────────────┘

┌─ Non-Current Version Expiration ───────────────────────────────│
│ 为什么需要:                                                      │
│   版本化桶中，每次覆盖写入会保留旧版本。如果不清理，版本会无限积累。  │
│   用户说"我打开了版本来防误删"，结果 1TB 数据变成了 10TB 版本。     │
│                                                                │
│ 实现（扩展 reconcile/lifecycle.go）:                              │
│   新方法: sweepNoncurrentVersions()                              │
│     1. 查询所有非当前版本（当前版本 = updated_at 最大）           │
│     2. 对每个非当前版本，检查是否满足 NoncurrentDays              │
│     3. 如果满足 → hard delete（删除存储 blob + 行）              │
│     4. 如果配置了 NewerVersions，保留最新的 N 个非当前版本         │
│     5. 跳过 locked_until 未过期的对象                             │
│                                                                │
│ SQL:                                                             │
│   SELECT * FROM objects                                         │
│   WHERE tenant_id=$1 AND bucket=$2 AND key=$3 AND deleted_at IS NOT NULL  │
│   AND updated_at < $cutoff                                       │
│   ORDER BY updated_at DESC                                       │
│   OFFSET $newer_versions                                         │
│   -- 保留最新的 N 个版本，删除更早的                               │
└────────────────────────────────────────────────────────────────┘

┌─ Incomplete Multipart Upload GC ───────────────────────────────│
│ 为什么需要:                                                      │
│   客户端 crash 后 UploadPart 上传的分片永远留在后端。              │
│   当前 aero-vault 没有清理机制——这些碎片只在 uploads 表中有记录。  │
│   即使 AbortMultipart API 存在，如果调用端无法触发，分片就是垃圾。  │
│                                                                │
│ 场景: 一个 100TB 的分片上传中断后未中止 → 永久占用 100TB 存储。    │
│                                                                │
│ 实现（新 reconcile worker 或扩展现有）:                           │
│   sweepAbandonedUploads()                                        │
│     1. 查询所有 uploads 表中的记录                                 │
│     2. 检查 CreatedAt > DaysAfterInitiation                     │
│     3. 对过期上传 → 自动调用 AbortMultipart                      │
│     4. 日志记录每次清理（用于审计）                                │
│     5. 指标: multipart_uploads_aborted_total{reason: "gc"}       │
│                                                                │
│ 配置:                                                            │
│   LIFECYCLE_MPU_DAYS=7  # 7 天未完成 → 自动中止                   │
│   # S3 默认是 7 天，AWS S3 Lifecycle 允许设置                     │
└────────────────────────────────────────────────────────────────┘

┌─ Expired Delete Marker Cleanup ────────────────────────────────│
│ 为什么需要:                                                      │
│   版本化桶中，DELETE 操作在所有版本被删除后留下删除标记。            │
│   这些标记不占存储，但产生以下问题：                                │
│   - listing 中仍然出现被"删除"的对象（结果是空的，但 key 还在）     │
│   - 当所有非当前版本都被生命周期删除后，删除标记仍然存在            │
│   - 用户无法再次上传同名文件（因为删除标记阻挡）                   │
│                                                                │
│ S3 解决方案: ExpiredObjectDeleteMarker 生命周期规则               │
│   在对象的非当前版本全部过期后，自动删除该对象的删除标记。           │
│                                                                │
│ 实现:                                                             │
│   在 sweep 中一步判断：                                           │
│     当前版本 = deleted_at IS NOT NULL（删除标记）                  │
│     且没有任何非当前版本（所有版本都已过期）                       │
│     → 硬删除该行（删除标记行）                                     │
└────────────────────────────────────────────────────────────────┘

┌─ Lifecycle Simulation API ─────────────────────────────────────│
│ 产品需求: 用户希望"预览"生命周期规则的效果                          │
│                                                                │
│ POST /v1/buckets/{bucket}/lifecycle/simulate                    │
│   { "rules": [...], "scope": "prefix:logs/" }                   │
│   → 返回预计影响：                                                │
│     {                                                            │
│       "matched_objects": 12345,                                  │
│       "matched_bytes": 107374182400,                             │
│       "deleted": { "objects": 1234, "bytes": 12345678 },        │
│       "transitioned": { "to_ia": {"objects": 5000, "bytes": ...},│
│                        "to_glacier": {"objects": 5000, ...} },   │
│       "version_cleanup": {"objects": 50000, "bytes_saved": ...}, │
│       "mpu_aborted": 23,                                         │
│     }                                                            │
│                                                                │
│ 实现: 只读分析 + 统计查询（不执行任何删除/转换）                   │
└────────────────────────────────────────────────────────────────┘

┌─ 迁移需求 ─────────────────────────────────────────────────────│
│ 双驱动迁移 0025:                                                 │
│   新增表: lifecycle_rules (独立于 bucket_config)                  │
│   - id, tenant_id, bucket, filter_prefix, filter_tags_json,      │
│     status, rule_type, days, newer_versions, action/target_class  │
└────────────────────────────────────────────────────────────────┘

**边界情况：**
- 版本清理和分片 GC 必须尊重 `RECONCILE_CLUSTER_SINGLETON`（已实现）
- 正在进行的分片上传不应被 GC 清理（判断标准：CreatedAt + DaysAfterInitiation）
- 版本清理时，如果对象有法律封存或对象锁，跳过（复用 `LockedUntil`/`legal_hold` 检查）
- 删除标记清理后，同 key 的新 PUT 应正常创建新版本（当前行被删，不会冲突）
- 模拟 API 不应执行任何写入操作（纯只读）
- GC 清理的分片上传应在审计日志中记录（复用 `audit_log` 表）

**复杂度:** M · **用户影响:** ★★★★★（存储成本核心） · **代码变更:** ~1000 行新代码 + ~300 行修改

---

## 3. API Governance & Client SDK Maturity

### 为什么需要它

当前 API 表面已经覆盖了大量功能——REST `/v1` + S3 兼容 + WebDAV + MCP + CLI + 多语言 SDK（Python/JS/Go）。但从产品化角度看，API 层面缺少**面向长期维护和客户契约**的基础设施：

- **单版本号，无版本协商**：所有请求走 `/v1/`，没有 `Accept-Version` 头支持。一旦 `/v1` 需要 breaking change，要么新建 `/v2` 并维护两套 handler，要么强制所有用户升级。
- **无弃用框架**：没有 `Sunset` / `Deprecation` 响应头标准（RFC 8594）。API 变更时用户无法自动感知。
- **无标准化分页**：`ListObjects` 在 REST 和 S3 路由中使用不同的分页模型（`marker`/`continuation-token`/`nextMarker`/`NextContinuationToken`）。没有统一的分页规范。
- **无 Rate Limit 响应头**：429 返回但不告知用户限额和重置时间（`X-RateLimit-Limit` / `X-RateLimit-Remaining` / `X-RateLimit-Reset`）。用户只能凭经验猜测限流策略。
- **无标准化错误响应体**：REST 路由返回 JSON `{"error": {"code": "...", "message": "..."}}`，但 S3 路由返回 XML 错误。SDK 必须处理两种格式。
- **SDK 依赖手动维护**：`sdk/python/`、`sdk/javascript/`、`sdk/go/` 的 API 方法是手动编写的。每次新增 route 需要手动同步三个 SDK。

这些问题不会在开发阶段暴露，但在客户落地时成为重要阻力和维护负担。尤其是当 API 发生向后不兼容变更时——这是 SaaS 产品的常态。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/api/rest/router.go` | `/v1` 路由注册 | 无版本协商 |
| `internal/api/rest/handler.go` | JSON 响应 | 无 `Deprecation`/`Sunset` 头 |
| `internal/api/rest/dto.go` | 响应 DTO | 无标准化分页对象 |
| `internal/api/rest/search.go` | AI 查询响应 | 无 RateLimit 响应头 |
| `internal/middleware/ratelimit.go` | RateLimiter 中间件 | 不写 `X-RateLimit-*` 头 |
| `internal/middleware/middleware.go` | 中间件链 | 无 deprecation 中间件 |
| `internal/api/s3compat/xml.go` | S3 XML 编解码 | 与 REST 完全不同的错误格式 |
| `internal/api/rest/openapi.json` | OpenAPI 3.0 规范 | 定义了的 API 但未用于 SDK 生成 |
| `sdk/python/aero_vault/` | 手动维护 SDK | 非生成式 |
| `sdk/javascript/` | 手动维护 SDK | 非生成式 |
| `sdk/go/` | 手动维护 SDK | 非生成式 |

### 架构蓝图

```
┌─ API Version Negotiation ──────────────────────────────────────│
│ 目标: 支持向后兼容的版本演进路径                                    │
│                                                                │
│ 方案 1（推荐）: Accept-Version 头                                  │
│   请求: GET /v1/files/doc.pdf                                   │
│          Accept-Version: 1.2                                     │
│   响应: X-Aero-API-Version: 1.2                                  │
│                                                                │
│   当版本低于当前版本时 → 通过兼容层（映射到新 handler）              │
│   当版本高于当前版本时 → 400 UnsupportedVersion                   │
│   当版本缺失时 → 默认最低兼容版本                                  │
│                                                                │
│ 方案 2（备选）: URL 路径版本                                      │
│   /v2/files/doc.pdf                                             │
│   资源消耗较大（需要两套路由）                                      │
│                                                                │
│ 实现路径:                                                         │
│   middleware 层:                                                 │
│     func APIVersion(defaultVer string, handlers map[string]      │
│       http.Handler) func(http.Handler) http.Handler              │
│     解析 Accept-Version → 映射到 handler                         │
└────────────────────────────────────────────────────────────────┘

┌─ Deprecation Framework ────────────────────────────────────────│
│ RFC 8594: Sunset and Deprecation HTTP headers                   │
│                                                                │
│ 中间件: DeprecationMiddleware                                    │
│   配置:                                                          │
│     deprecations := map[string]DeprecationConfig{                │
│       "/v1/files": {                                            │
│         Sunset: "2027-07-10T00:00:00Z",   // 完全移除日期        │
│         MigrationURL: "/docs/migration-v1-v2",                  │
│         DeprecatedSince: "2026-07-10",                          │
│       },                                                         │
│     }                                                            │
│                                                                │
│ 对匹配路径的每个响应自动添加头:                                    │
│   Deprecation: true                                              │
│   Sunset: Sat, 10 Jul 2027 00:00:00 GMT                          │
│   Link: </docs/migration-v1-v2>; rel="deprecation"               │
│                                                                  │
│ 实现:                                                             │
│   从中间件读取路由模式 → 匹配请求路径 → 写入响应头                  │
│   不影响未弃用的 API 路径                                          │
└────────────────────────────────────────────────────────────────┘

┌─ Standardized Pagination ──────────────────────────────────────│
│ 当前状态:                                                         │
│   REST List: GET /v1/files?prefix=...&marker=...&limit=100      │
│   S3 ListV2: token=base64(continuation-token)                    │
│   S3 ListV1: marker=key                                          │
│   每种返回格式不同                                               │
│                                                                │
│ 标准化方案:                                                       │
│   统一 cursor-based pagination:                                   │
│   请求: GET /v1/files?cursor=xxx&limit=100                       │
│   响应: {                                                        │
│     "data": [...],                                               │
│     "pagination": {                                              │
│       "next_cursor": "xxx",      // 下一页（null = 无更多）      │
│       "limit": 100,              // 请求的页面大小                │
│       "total": 1234              // 总匹配数（可选，成本高）     │
│     }                                                            │
│   }                                                              │
│                                                                │
│   S3 兼容层保持现有格式不变（S3 标准格式不可变）                   │
│   但内部统一为 cursor 模型，在 response 层转换                     │
│                                                                │
│ 迁移:                                                             │
│   添加 `cursor` 查询参数（替代 `marker`）                         │
│   旧 `marker` 参数保持向后兼容                                    │
└────────────────────────────────────────────────────────────────┘

┌─ Rate Limit Standard Headers ──────────────────────────────────│
│ 目标: 让客户端能够自适应限流策略                                     │
│                                                                │
│ 当前: 429 响应只有 Retry-After 头                                  │
│ 扩展后: 所有响应包含：                                             │
│   X-RateLimit-Limit: 1000        # 每窗口上限                    │
│   X-RateLimit-Remaining: 234     # 当前窗口剩余                  │
│   X-RateLimit-Reset: 1594814400  # 窗口重置时间（Unix epoch）    │
│                                                                │
│ 扩展中间件: ratelimit.go                                          │
│   写入这些头到 ResponseWriter（在请求处理完后写入）                 │
│   区分全局 RPS / AI RPS / Egress bandwidth 三个维度的限流头       │
│                                                                │
│ 可选:                                                            │
│   X-RateLimit-Resource: "ai"     # 哪个维度的限流                │
│   X-RateLimit-Policy: "1000/1s"  # 限流策略描述                  │
└────────────────────────────────────────────────────────────────┘

┌─ OpenAPI-Driven SDK Generation ────────────────────────────────│
│ 当前: 三套 SDK 手动编写，容易与 API 实际行为漂移                    │
│                                                                │
│ 目标: OpenAPI 规范作为单一事实来源，SDK 基于 openapi.json 生成     │
│                                                                │
│ 工具链:                                                          │
│   openapi-generator-cli (Java)                                    │
│   或 oapi-codegen (Go-native)                                    │
│   或 fern (multi-language)                                       │
│                                                                │
│ 实施步骤:                                                         │
│   1. 完善 openapi.json（确保覆盖所有路由 + schema）               │
│   2. Makefile 集成代码生成:                                       │
│      make sdk-python: openapi-generator -g python                │
│      make sdk-javascript: openapi-generator -g javascript        │
│      make sdk-go: oapi-codegen -package sdk                      │
│   3. 消除手动 SDK 文件（逐步替换）                                 │
│   4. CI 检查 openapi.json 是否与路由注册一致                      │
│                                                                │
│ 注意: 生成的 SDK 可能有 80% 可用性；剩下的 20%（如 WebSocket/SSE │
│ 流式客户端、签名方法）需要手动包装                                  │
└────────────────────────────────────────────────────────────────┘

┌─ Unified Error Format ─────────────────────────────────────────│
│ 当前: REST → JSON error 格式                                     │
│       S3 → XML error 格式                                        │
│       MCP → JSON-RPC error 格式                                  │
│       WebDAV → XML error 格式（DAV:error）                        │
│                                                                │
│ 目标: 内部统一错误码 + 外部按协议转换                               │
│                                                                │
│ type APIError struct {                                           │
│     HTTPStatus int                                               │
│     Code       string   // 机器可读: "NotFound", "QuotaExceeded" │
│     Message    string   // 人类可读                                │
│     Details    any      // 可选: 结构化错误详情                    │
│     RequestID  string                                            │
│ }                                                                │
│                                                                │
│ 转换函数:                                                         │
│   toJSONError(err)  → {"error":{"code":"...","message":"..."}}   │
│   toS3XMLError(err) → <Error><Code>...</Code>...</Error>        │
│   toMCPError(err)   → {"jsonrpc":"2.0","error":{"code":...,     │
│                        "message":"..."}}                         │
│   toWebDAVError(err) → <d:error>...</d:error>                    │
│                                                                │
│ 好处:                                                            │
│   - 所有协议共享相同的错误定义                                     │
│   - 新的协议适配器自动获得正确的错误格式                             │
│   - SDK 可以基于错误 Code 做逻辑分支                               │
└────────────────────────────────────────────────────────────────┘

**边界情况：**
- 向后兼容：添加 `Accept-Version` 头支持时，无版本头的请求必须默认使用最新兼容版本
- 弃用头应仅由中间件添加，handler 不需感知——通过路由模式匹配注入
- OpenAPI 生成的 SDK 需要 post-processing（添加认证逻辑、重试、流式支持）
- 统一错误码需要映射现有的 `service.Err*` 常量到 API 错误码
- 分页 `total` 字段在大数据集上成本高（COUNT 扫描），默认不返回，用 `X-Total-Count` 头可选

**复杂度:** M · **用户影响:** ★★★★☆（API 消费者 & 维护者） · **代码变更:** ~1000 行新代码 + ~500 行修改

---

## 4. Active-Active Multi-Region Replication & Geo-Distribution

### 为什么需要它

当前复制实现（`internal/replication/replication.go`）是**单向、单目标、异步、最终一致**的。这是灾难恢复（DR）的最低要求，但离真正的**多区域活跃拓扑**还有距离：

- **无读路由**：如果部署在 us-east-1 和 eu-west-1，欧洲用户只能从 us-east-1 读取（高延迟）。没有任何机制将读取请求路由到最近区域。
- **无冲突解决**：当两个区域同时写入同一个 key 时，复制工人会互相覆盖。没有 last-writer-wins 的明确策略或版本冲突检测。
- **无配置复制**：桶级配置（versioning、lifecycle、CORS、ACL、policy）不复制。在一个区域创建的桶在另一个区域不可见（除非手动创建）。
- **无数据主权控制**：有些数据必须留在特定地区（GDPR、中国数据安全法）。当前没有机制将对象"钉"在某个区域，或跨区域移动时检查合规约束。
- **无复制拓扑灵活性**：当前代码硬编码了一个 primary → replica 的配置。企业需要 one-to-many（单源到多区域）、many-to-one（多区域汇聚到一个 DR 中心）、以及 mesh 复制（所有区域互相同步）。
- **无复制状态可见性**：用户无法查询"这个对象是否已经被复制到 eu-west-1？复制延迟是多少？"
- **复制流量无治理**：跨区域复制消耗的出口带宽没有计量和管理，可能导致意外云账单。

ROADMAP #3（水平扩展）和 #10（元数据 HA/DR）分别讨论了单区域内多副本和 DB 层 HA。本方向解决的是**跨区域应用层复制 + 读写拓扑**。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/replication/replication.go` | 1 个 primary → 1 个 replica | 无多目标、无双向 |
| `internal/replication/replication.go:Worker.Run` | 只处理 `EventCreated` | 无 deleted/accessed 事件复制 |
| `internal/repository/repository.go:Object` | 无区域字段 | 无 `Region`、`ReplicaStatus` |
| `internal/service/file_features.go:SetBucketLifecycle` | 桶配置管理 | 不复制到其他区域 |
| `internal/jobs/jobs.go` | 作业队列 | 可用于复制调度但未用于跨区域协调 |
| `internal/cluster/singleton.go` | 集群单例 | 仅用于 reconcile，未用于复制选举 |
| `internal/config/config.go` | 配置结构体 | 无多区域复制拓扑 |

### 架构蓝图

```
┌─ Replication Topology Model ───────────────────────────────────│
│ type ReplicationRule struct {                                   │
│     ID             string                                       │
│     Priority       int     // 规则优先级，用于冲突解决           │
│     Destinations   []ReplicaTarget                              │
│     FilterPrefix   string                                       │
│     FilterTags     map[string]string                            │
│     ReplicateDeletes bool   // 默认 true                       │
│     SyncConfig     bool    // 是否复制桶配置                     │
│     ConflictResolution string // "lww" | "version-vector"      │
│ }                                                               │
│                                                                │
│ type ReplicaTarget struct {                                     │
│     Region   string  // "eu-west-1"                            │
│     Endpoint string  // "https://eu.aero.dev"                  │
│     Credentials auth.Credentials  // 目标区域的认证凭证         │
│     BandwidthMax int64  // 复制流量上限（bytes/sec）            │
│ }                                                               │
│                                                                │
│ 配置:                                                            │
│   REPLICATION_RULES='[                                          │
│     {"id":"to-europe", "destinations":[{                        │
│       "region":"eu-west-1",                                    │
│       "endpoint":"https://eu.aero.dev",                        │
│     }], "filter_prefix":"public/"}                              │
│   ]'                                                             │
└────────────────────────────────────────────────────────────────┘

┌─ Cross-Region Read Routing ────────────────────────────────────│
│ 模式: 基于延迟/地理位置的透明读路由                                │
│                                                                │
│ 架构:                                                            │
│   每个区域部署独立的 aero-vault 实例                              │
│   入口层: 全局负载均衡器（DNS-based Anycast / HTTP redirect）    │
│   读路由策略:                                                     │
│     1. DNS: 根据请求来源 IP 解析到最近区域的 A 记录               │
│     2. HTTP 302: 发送到入口区域后，计算最优区域 → 重定向          │
│     3. 代理: 入口区域透明转发到目标区域（低延迟选项）               │
│                                                                │
│ 实现:                                                             │
│   新包: internal/georoute/                                       │
│     type Router struct {                                         │
│       regions []RegionInfo                                       │
│       geoDB   *maxmind.Reader  // GeoIP2                         │
│     }                                                            │
│     func (r *Router) NearestRegion(ip string) string              │
│                                                                │
│   中间件:                                                         │
│     GeoRoute(router) func(http.Handler) http.Handler              │
│       对 GET/HEAD 请求评估并添加 X-Aero-Region 头                 │
│       如果当前区域不是最优 → 返回 302 + Location                  │
└────────────────────────────────────────────────────────────────┘

┌─ Conflict Resolution ──────────────────────────────────────────│
│ 场景: us-east-1 和 eu-west-1 同时 PUT /doc.pdf                  │
│       两个区域的复制工人都会收到对方的事件                         │
│       如果各自覆盖写入 → 数据丢失                                 │
│                                                                │
│ 策略 LWW（Last-Writer-Wins，默认）:                                │
│   每个版本携带时间戳 + 区域 ID                                     │
│   复制时比较时间戳：较新的写入胜出                                  │
│   如果时间戳相同，区域ID小的胜出（确定性）                          │
│   使用: version vector / Lamport clock                            │
│                                                                │
│ 实现:                                                             │
│   Object 扩展字段:                                                │
│     ReplicaVersion string  // Lamport timestamp+region           │
│     ReplicaRegion string   // originating region                 │
│                                                                │
│   复制 worker 修改:                                               │
│     收到复制事件 → 比较 ReplicaVersion                           │
│     如果本地版本更新的时间较晚 → 跳过（已覆盖）                   │
│     如果本地版本较早 → 写入新版本                                │
└────────────────────────────────────────────────────────────────┘

┌─ Configuration Replication ────────────────────────────────────│
│ 场景: 管理员在 us-east-1 创建 bucket "data" 并设置 versioning    │
│       eu-west-1 看不到这个 bucket，复制来的对象无法写入            │
│                                                                │
│ 方案: 桶配置变更事件同步到目标区域                                  │
│                                                                │
│ 事件类型扩展:                                                     │
│   repository.EventType: "config_created" | "config_updated"     │
│   新增的事件类型在 bucket config 变更时发布                        │
│   复制 worker 订阅这些事件 → 在目标区域执行同等的配置 API 调用     │
│                                                                │
│ 复制的配置:                                                       │
│   - Bucket 创建/删除                                             │
│   - Versioning 开关                                              │
│   - Lifecycle 规则                                               │
│   - CORS 配置                                                    │
│   - ACL / Policy                                                 │
│   - Object Lock 设置                                             │
│   - Notification 规则                                             │
│   - Logging 配置                                                 │
│   - Encryption 策略（当 v3 #2 实现后）                             │
│                                                                │
│ 注意: 配置复制是单向的（从主区域到从区域），避免循环复制             │
└────────────────────────────────────────────────────────────────┘

┌─ Data Sovereignty & Geo-Fencing ───────────────────────────────│
│ 场景: 医疗数据必须留在美国境内                                    │
│       欧盟用户的数据不能离开 EU                                   │
│                                                                │
│ 方案:                                                            │
│   桶级/对象级 region-pin 标记                                     │
│     x-aero-allowed-regions: us-east-1, us-west-2             │
│     x-aero-forbidden-regions: eu-*                          │
│                                                                │
│   复制 worker 检查:                                               │
│     如果对象标记了 allowed-regions                                │
│     并且目标区域不在 allowed 列表中 → 跳过该对象的复制             │
│                                                                │
│   实现:                                                           │
│   对象元数据扩展: _aero_allowed_regions, _aero_forbidden_regions  │
│   桶级默认配置: AllowedRegions / ForbiddenRegions                │
│   复制 worker 在复制前检查 geo 策略                               │
└────────────────────────────────────────────────────────────────┘

┌─ Replication Observability ────────────────────────────────────│
│ 新的 Prometheus 指标:                                             │
│   replication_events_total{target_region, event_type, status}   │
│   replication_lag_bytes{target_region}                          │
│   replication_lag_duration_ms{target_region}                    │
│   replication_egress_bytes_total{target_region}                  │
│   replication_queue_depth                                       │
│   replication_conflicts_total{resolution}                        │
│                                                                │
│ Replication Status API:                                          │
│   GET /v1/replication/status                                    │
│     → { "rules": [...],                                         │
│         "targets": { "eu-west-1": {"lag_ms": 1234,              │
│             "pending": 0, "errors": 0, "egress_gb": 123 } } }  │
│                                                                │
│   GET /v1/files/{key}/replication-status                        │
│     → { "key": "doc.pdf",                                       │
│         "regions": { "eu-west-1": { "status": "replicated",     │
│             "replicated_at": "2026-07-10T12:00:00Z" } } }       │
└────────────────────────────────────────────────────────────────┘

**边界情况：**
- 复制循环检测：如果区域 A 复制到区域 B，区域 B 又复制回区域 A → 无限循环。需要 ReplicaRegion 标记阻止循环。
- 跨区域复制时，密码/凭据的安全传输：目标区域之间使用 TLS + API 密钥认证
- 网络分区恢复：区域 A 和区域 B 断开 1 小时后恢复，需要 catch-up 复制（批量扫描并比较落后区域）
- 对象锁跨区域：在一个区域锁定的对象，不应在另一个区域可写
- 复制流量带宽治理：一段跨区域链路的带宽有限时，复制作业应排队等待而不是打爆链路

**复杂度:** L（多区域协调） · **用户影响:** ★★★★★（全球化部署） · **代码变更:** ~1500 行新代码 + ~400 行修改

---

## 5. Storage Cost & Usage Analytics Engine

### 为什么需要它

存储在今天的代码库中是一个"黑盒"——你能看到总字节数（`BucketStats`）和每存储类的对象数（`StorageClassCounts`），但看不到：

- **哪些数据是热的，哪些是冷的？** ——没有访问模式分析。不知道哪些对象在过去 30 天从未被读取，适合降冷或删除。
- **哪些数据是重复的？** ——没有内容去重检测。同一个文件被上传多次，除了版本机制保留的不同版本，也可能有完全相同的副本。
- **版本开销有多大？** ——版本化桶中，历史版本可能占存储的 70%+，但你无从得知。
- **存储成本预测？** ——如果把前缀 `logs/` 从 STANDARD 降到 STANDARD_IA，每月省多少钱？当前没有任何"what-if"分析工具。
- **费用归因？** ——每个租户/桶/前缀的存储成本是多少？没有成本仪表盘。

这是从"对象**存储**"进化为"对象**数据管理平台**"的关键一步。AWS S3 提供 Storage Lens（全局存储分析器）、Cost Allocation Tags、S3 Analytics（分析存储类建议）——这些都是企业用户的日常工具。

当前代码库已经收集了足够的数据（`LastAccessed`、`Object.UpdatedAt`、`metadata`、`tags`、`Backend`），但完全未利用为分析能力。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/repository/repository.go:BucketStats` | 桶级总大小+对象数 | 无存储类分布、无访问统计 |
| `internal/repository/repository.go:StorageClassCounts` | 按存储类计数 | 无字节级分布 |
| `internal/repository/sql_objects.go` | 对象 SQL 查询 | 无分析查询（按标签、前缀、时间范围聚合） |
| `internal/service/file_features.go:List` | 有分页列出 | 无分析聚合 |
| `internal/service/file_crud.go:Get` | 读取时 emit EventAccessed | 无访问计数/最后访问时间记录 |
| `internal/telemetry/metrics.go` | 运行时指标 | 无存储成本/使用分析指标 |
| `internal/repository/sql_helpers.go` | SQL 工具 | 无聚合分析函数 |
| `internal/api/rest/admin.go` | 管理 API | 无分析端点 |

### 架构蓝图

```
┌─ Access Pattern Heatmap ───────────────────────────────────────│
│ 目标: 识别数据的热度分布                                           │
│                                                                │
│ 实现路径:                                                         │
│   1. 在 Object 中添加 LastAccessedAt 字段（或复用 EventAccessed） │
│   2. 每次 GET/HEAD 更新 LastAccessedAt                           │
│   3. 分析查询按时间范围分组                                       │
│                                                                │
│ API:                                                             │
│   GET /v1/admin/analytics/access?tenant=acme&bucket=default      │
│     &from=2026-01-01&to=2026-07-10&granularity=monthly           │
│     → {                                                          │
│         "buckets": [                                             │
│           {"bucket": "logs", "access_count": 12345,              │
│            "total_bytes": 1073741824, "cold_bytes": 536870912},   │
│           ...                                                     │
│         ],                                                        │
│         "access_timeline": [                                      │
│           {"date": "2026-06","gets": 1234,"head": 567},           │
│           ...                                                     │
│         ]                                                         │
│       }                                                           │
│                                                                │
│   GET /v1/admin/analytics/idle-objects?days=90                   │
│     → 过去 90 天未被访问的对象列表                                │
│       { "objects": [                                             │
│         {"key": "archive/2020/q1.csv", "size": 1073741824,       │
│          "last_accessed": "2025-12-01", "storage_cost_monthly":  │
│          "$0.023"}, ...                                          │
│       ], "total_size": 107374182400, "monthly_cost": "$2.30" }   │
└────────────────────────────────────────────────────────────────┘

┌─ Duplicate Detection ──────────────────────────────────────────│
│ 目标: 发现存储中相同内容的对象                                      │
│                                                                │
│ 实现路径:                                                         │
│   1. 在 Object 中添加 ContentHash (SHA-256) 字段                 │
│   2. PUT 时计算并存储 content hash                                │
│   3. 分析查询按 content hash 分组                                 │
│   （注意：不实现全局去重（这会破坏版本和锁定语义），仅检测报告）     │
│                                                                │
│ API:                                                             │
│   GET /v1/admin/analytics/duplicates?tenant=acme                 │
│     → { "groups": [                                              │
│         {"hash": "abc123...", "objects": 3, "total_wasted":      │
│          1048576, "keys": ["backup/a.iso", "backup/a_副本.iso",  │
│           "shared/archive.iso"]}, ...                           │
│       ], "total_wasted_bytes": 53687091200 }                     │
│                                                                │
│ 迁移: 新增 content_hash 列（VARCHAR(64)），逐步回填存量数据        │
└────────────────────────────────────────────────────────────────┘

┌─ Version Overhead Analysis ────────────────────────────────────│
│ 目标: 量化版本化存储的开销                                         │
│                                                                │
│ API:                                                             │
│   GET /v1/admin/analytics/version-overhead?tenant=acme           │
│     → {                                                          │
│         "total_current_bytes": 107374182400,                     │
│         "total_version_bytes": 322122547200,                      │
│         "version_ratio": 3.0,   // 版本存储是当前的 3 倍         │
│         "buckets": [                                              │
│           {"bucket": "data", "current": 10737418240,             │
│            "versions": 53687091200, "ratio": 5.0,                │
│            "oldest_version": "2024-01-01"},                      │
│           ...                                                     │
│         ],                                                        │
│         "recommendation": "Set NoncurrentVersionExpiration to    │
│            retain only last 3 versions → save ~80% version cost" │
│       }                                                           │
└────────────────────────────────────────────────────────────────┘

┌─ Cost Projection & What-If Analysis ───────────────────────────│
│ 目标: 帮助用户理解存储成本并优化                                     │
│                                                                │
│ 成本模型（基于假设单价，可配置）:                                    │
│   STANDARD:      $0.023/GB/月                                    │
│   STANDARD_IA:   $0.0125/GB/月                                   │
│   GLACIER:       $0.004/GB/月                                    │
│   DEEP_ARCHIVE:  $0.001/GB/月                                    │
│                                                                │
│ API:                                                             │
│   POST /v1/admin/analytics/cost-projection                       │
│     { "what_if": [                                               │
│         {"action": "transition", "prefix": "logs/2024/*",       │
│          "target_class": "STANDARD_IA"},                         │
│         {"action": "transition", "prefix": "archive/**",        │
│          "target_class": "GLACIER"},                             │
│         {"action": "expire_versions", "bucket": "data",          │
│          "keep_last": 3},                                        │
│       ]}                                                          │
│     → {                                                          │
│         "current_monthly_cost": 1250.00,                         │
│         "projected_monthly_cost": 423.50,                        │
│         "monthly_savings": 826.50,                              │
│         "savings_pct": 66.1,                                     │
│         "payback_period_days": 0,  // no upfront cost           │
│         "details": [                                             │
│           {"action": "logs/2024/* → STANDARD_IA",                │
│            "saving": 450.00, "objects_affected": 50000},         │
│           {"action": "archive/** → GLACIER",                     │
│            "saving": 350.00, "objects_affected": 120000},         │
│           {"action": "data version cleanup (keep 3)",            │
│            "saving": 26.50, "objects_affected": 25000},          │
│         ]                                                         │
│       }                                                           │
└────────────────────────────────────────────────────────────────┘

┌─ Recommendation Engine ────────────────────────────────────────│
│ 目标: 自动建议可执行的优化方案                                       │
│                                                                │
│ 规则引擎（基于分析查询的输出触发建议）:                               │
│   - "prefix 'logs/2024/' has 50000 objects with no access in   │
│      90 days → move to STANDARD_IA, save ~$450/month"           │
│   - "bucket 'data' has version ratio 5:1 → set noncurrent      │
│      version expiration to 30 days, ~$150/month savings"        │
│   - "object 'backup/a.iso' appears 3 times with identical       │
│      content hash → remove duplicates, ~$2/month savings"        │
│   - "prefix 'tmp/' has 5000 objects older than 30 days →        │
│      consider lifecycle expiration, ~$5/month savings"           │
│   - "bucket 'logs' has 230 incomplete multipart uploads         │
│      occupying 1.2TB → enable AbortIncompleteMultipartUpload"   │
│                                                                │
│ API:                                                             │
│   GET /v1/admin/analytics/recommendations?tenant=acme            │
│     → { "recommendations": [                                     │
│         {"id": "r1", "type": "transition",                      │
│          "priority": "high",                                     │
│          "prefix": "logs/2024/",                                │
│          "current_class": "STANDARD",                           │
│          "recommended_class": "STANDARD_IA",                    │
│          "monthly_savings": 450.00,                             │
│          "rationale": "No access in 90 days for 50000 objects"},│
│         ...                                                      │
│       ]}                                                         │
└────────────────────────────────────────────────────────────────┘

┌─ 迁移需求 ─────────────────────────────────────────────────────│
│ 双驱动迁移 0025:                                                 │
│   ALTER TABLE objects ADD COLUMN last_accessed_at TEXT;          │
│   ALTER TABLE objects ADD COLUMN content_sha256 TEXT;            │
│   CREATE INDEX idx_objects_last_accessed ON objects(             │
│     tenant_id, last_accessed_at);                                │
│   CREATE INDEX idx_objects_content_hash ON objects(              │
│     content_sha256) WHERE content_sha256 != '';                  │
└────────────────────────────────────────────────────────────────┘

**边界情况：**
- `LastAccessedAt` 更新不能阻塞 GET 请求——应异步更新（event-driven 或 queue）
- `ContentHash` 仅在 PUT 时计算，不重新扫描存量对象（可选后处理扫描）
- 分析 API 是只读的，永远不触发写入操作
- 大租户的分析查询可能很慢——设置默认时间范围限制 + 超时
- 建议引擎的规则应是可配置的（是否启用某规则、阈值参数）
- 成本估算基于配置的单位价格——每个租户/部署可能有不同定价模型
- 大量小型对象的分析（如百万级）需流式分页，防止 OOM

**复杂度:** M · **用户影响:** ★★★★☆（成本优化） · **代码变更:** ~1400 行新代码 + ~300 行修改

---

## 附录：排除但有价值的较小改进

| 问题 | 位置 | 说明 | 建议 |
|------|------|------|------|
| **DeleteBucket 不检查非空桶** | `internal/service/file_features.go:DeleteBucket` | 直接删除桶行，不检查是否有对象 | 删除前应要求桶为空（`BucketStats` > 0 则拒绝），或引入 `--force` 标志 |
| **EventAccessed 仅用于审计，不被消费** | `internal/service/file_crud.go:Get` | emits `EventAccessed` 但 indexer 忽略它（no-op） | 可被 analytics 消费用于访问模式分析（方向 #5 前置）|
| **分片上传的存储 key 与 versionID 不一致** | `internal/service/file_multipart.go:InitMultipart` | versioned 桶中，`@v<id>` 在 `InitMultipart` 时分配但在 `CompleteMultipart` 时可能不一致 | 确保 `upload.StorageKey` 中的 versionID 与最终 `InsertObjectVersion` 的 versionID 一致 |
| **多租户 CLI 缺少租户切换** | `internal/cli/cli.go` | CLI 命令通过 env `AERO_TENANT` 读取租户，但无 `--tenant` 参数 | 支持 `--tenant` 标志 | 
| **匿名公共读缺少 IP 级限流** | `internal/auth/auth.go:WithAnonymousPublicRead` | 匿名用户可以通过公读 ACL 无限下载 | 匿名访问应应用独立的、更严格的 RPS 限制（与认证用户分开） |
| **Bucket 名缺少 DNS 兼容校验** | `internal/service/file.go:validateKey` | 校验了 key 但未校验 bucket 名 | bucket 名应满足 S3 命名规则（小写、无下划线、3-63 字符） |
| **OpenAPI spec 缺少许多端点** | `internal/api/rest/openapi.json` | 定义的 API 小于实际实现 | 同步 openapi.json 与实际路由注册 |

---

> **总结：** 以上 5 个方向均未被 ROADMAP、八轮 analysis-v[1-8] 和三期 expansion-directions 覆盖。它们覆盖了**多租户公平性（出口治理）、存储成本失控（全面生命周期）、API 产品化（治理与 SDK）、全球化部署（多区域复制）、以及数据资产管理（成本分析）** 五个关键维度。建议实施顺序：#2（存储成本，最急切）→ #1（多租户公平性）→ #5（成本可见性）→ #3（API 产品化，长期持续改进）→ #4（全球化部署，架构级投入）。

