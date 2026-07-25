# 高价值扩展方向：死代码路径、治理模型缺口与不完全管线补齐

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — 237+ Go 源文件、3 套 SDK（Go/Python/JS）、MCP 双模式（HTTP+stdio）、Web UI、Helm Chart、Grafana/Prometheus/OTel 配置  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。选取**代码中存在明确实现锚点但实际断线、半实现或仅存骨架**的方向——即代码"看起来存在"但用户实际使用时功能不完整或静默空转。  
> **前置阅读建议：** `docs/ROADMAP.md`（10 大官方方向）、`docs/requirements/expansion-v98-truly-high-value-directions.md`（多模态 AI 等 5 方向）

---

## 方法论：从代码锚点到产品缺口

本次分析不以"理想对象存储应该有什么功能"出发，而是以**代码库中已存在的实现片段**为锚点，追踪它们是否形成完整的功能闭环。当一个功能的 API 层、持久化层、配置层都存在，但**运行时管线断裂**——即配置被保存但永不执行、数据被采集但永不消费、接口被暴露但下层为空——则该方向被标记为高价值补齐目标。

### 本扫描的五大方向

| # | 方向 | 性质 | 核心发现 |
|---|------|------|----------|
| **1** | **桶级事件通知引擎**——从"配置即保存"到"配置即执行" | 功能完整性 | S3 `?notification` 接口完整解析并持久化规则 XML，但事件总线完全不读取这些规则；全球仅有单 URL 的 Webhook 目标在运行；`TopicARN`/`LambdaARN` 字段标记为 `unused, kept for compat` |
| **2** | **服务端访问日志**——从"死代码接口"到"可审计日志流" | 合规/运维 | `repository.WriteAccessLog` 方法有完整 SQL 实现，S3 `?logging` 端点完整配置 CRUD，但**没有任何 handler、middleware 或业务逻辑调用它**。这是全库最明显的死代码路径。 |
| **3** | **Object Lock 完整治理模型**——从"单个时间戳"到"S3 合规锁" | S3 合规 | 当前仅 `LockedUntil` 时间戳，无 `GOVERNANCE`/`COMPLIANCE` 模式区分；Legal Hold 存为 `_aero_legal_hold` 元数据标志；`x-amz-bypass-governance-retention` 头不被识别；无 `PUT ?retention` 或 `PUT ?legal-hold` 端点 |
| **4** | **对象生命周期状态机**——从"二元过期"到"完整存储类转换" | 成本/架构 | 生命周期仅支持 `soft_delete` / `hard_delete`；无 `STANDARD→STANDARD_IA→GLACIER→DEEP_ARCHIVE→DELETE` 状态机；`?restore` 端点是软删除恢复而非冷存储恢复；`storage_class` 字段已存在但永不转换 |
| **5** | **桶策略条件引擎扩展**——从 `aws:SourceIp` 到完整 IAM 条件 | 安全/多租户 | `internal/auth/policy.go` 仅在 `checkBucketPolicy` 中 `Eval`，仅支持 `IpAddress`/`NotIpAddress` 条件和 `aws:SourceIp` 键；无资源级授权、无 `NotPrincipal`/`NotAction`/`NotResource`、无策略变量、无 `aws:CurrentTime`/`aws:SecureTransport` 等条件键；策略仅在 S3 handler 层评估，REST API 层不执行 |

---

## 方向一：桶级事件通知引擎——从"配置即保存"到"配置即执行"

### 现状与代码证据

S3 兼容层完整实现了 `GET/PUT/DELETE /{bucket}?notification` 接口：

```go
// internal/api/s3compat/handler.go:809-833
// dispatchBucketNotifications 根据 HTTP method 分发到
// getBucketNotifications / putBucketNotifications / deleteBucketNotifications
//
// putBucketNotifications 完整解析 S3 通知 XML 为 repository.NotificationRule：
//   - TopicConfiguration → TopicARN 字段
//   - QueueConfiguration → QueueARN 字段
//   - LambdaFunctionConfiguration → LambdaARN 字段
//   - Filter → S3Key prefix/suffix 规则
//
// Service 层完整透传：
//   service.FileService.SetBucketNotifications(ctx, tenant, bucket, rules)
//
// 持久化层完整：
//   repository.sql_buckets.GetBucketNotifications / SetBucketNotifications / DeleteBucketNotifications
//   → 规则写入 buckets.notification_rules JSON 列
```

**但是，事件发布端完全忽略这些规则：**

```go
// internal/events/bus.go:90-100 — Publish 方法
func (b *Bus) Publish(ctx context.Context, e repository.Event) {
    // ... 持久化到 events 表
    b.broadcast(e)  // ← 直接广播给所有 subscriber，零规则检查
    // ... 如有 transport，也直接发布
}
```

所有消费者（indexer、antivirus worker、replication worker、webhook、SSE stream）都接收**所有事件**：

```go
// cmd/server/main.go:
go avw.Run(ctx, bus.Subscribe())       // Antivirus — 全量事件
go rw.Run(ctx, bus.Subscribe())        // Replication — 全量事件
go wh.Run(ctx, bus.Subscribe())        // Webhook — 单 URL，全量事件
go indexer.Run(ctx, bus.Subscribe())   // Indexer — 全量事件
```

`internal/api/rest/sse.go` 的 SSE 端点同样消费所有事件，无 bucket 过滤。

`internal/repository/repository.go:51-58` 的文档直接声明：

```go
type NotificationRule struct {
    // ...
    QueueARN  string `json:"QueueArn,omitempty"` // webhook URL or queue ARN
    TopicARN  string `json:"TopicArn,omitempty"` // unused, kept for compat
    LambdaARN string `json:"LambdaFunctionArn"`  // unused, kept for compat
}
```

**结论：通知规则配了等于没配。** S3 兼容用户从 AWS SDK/CLI 调用 `put-bucket-notification-configuration` 后，配置被静默保存但永不执行。

### 产品价值

| 维度 | 影响 |
|------|------|
| **企业集成** | 对象存储的事件驱动集成是数据管线的基础。没有工作通知路由，无法实现：新文件→触发数据处理、文件删除→触发下游清理、合规事件→触发告警 |
| **S3 兼容可信度** | 用户配置通知后感知为空操作，对平台信任损失极大 |
| **代码复用** | `events` 包、`webhook.go` 的 retry 机制、`jobs` 包均可复用；仅需新增路由层 |

### 典型场景

1. **数据管线触发**：CSV 文件上传到 `data/ingest/` 前缀时 → 自动触发 ETL 回调
2. **合规监控**：删除事件发生时 → 发送告警到安全团队 Slack Webhook
3. **跨区域复制选择**：仅特定前缀/标签的对象参与复制，而不是全桶复制

### 架构方案概要

```
┌──────────────┐  持久化事件    ┌───────────────┐
│   EventBus   │ ──────────────→│  events 表     │
│   .Publish() │               └───────────────┘
└──────┬───────┘
       │ 广播事件
       ▼
┌─────────────────────────────────────────┐
│         Notification Router (新增)       │
│   - 从 DB 加载每个 tenant/bucket 的规则  │
│   - 匹配 event.type、key prefix/suffix  │
│   - 按规则分发到不同 endpoint            │
└──────┬──────────┬──────────┬────────────┘
       │          │          │
       ▼          ▼          ▼
   Webhook A   Webhook B   Job Queue
   (e.g.      (e.g.       (e.g.
    Slack)     Lambda)     SQS style)
```

**关键设计点：**
- 规则缓存 + TTL（避免每次事件查 DB）
- per-rule 重试策略（复用 `webhook.go` 的 durable retry 模式）
- prefix/suffix 过滤（S3 的 `S3Key.Filter` 规则）
- 目标去重（同一事件不重复发送给同一 URL）
- 新消费者也可以基于规则订阅（例如 SSE 流的 bucket 过滤）

### 边界情况与风险

| 场景 | 处理 |
|------|------|
| 规则不存在/被删除 | 静默跳过，不阻塞事件流 |
| 目标 endpoint 不可达 | 记录 `webhook_failures`，调用方获得 `202 Accepted` 但事件可能在 retry 窗口后丢失 |
| 同一个事件匹配多条规则 | 每条规则独立发送 |
| 高频事件冲击目标 | 复用 rate limiter 或加入 adaptive backoff |
| 事件顺序保证 | 无顺序保证（同 S3 行为） |

---

## 方向二：服务端访问日志——从"死代码接口"到"可审计日志流"

### 现状与代码证据

该功能是代码库中最明显的**断线功能**。以下是完整代码链追踪：

**配置层完整：**
```go
// internal/repository/repository.go:274
WriteAccessLog(ctx context.Context, tenant, sourceBucket, method, key, status, latencyMs, userAgent string) error
```

**SQL 实现完整：**
```go
// internal/repository/sql_buckets.go:368-370
func (s *sqlStore) WriteAccessLog(ctx context.Context, tenant, sourceBucket, method, key, status, latencyMs, userAgent string) error {
    // 完整 SQL 插入到目标桶的日志对象
}
```

**Service 层完整：**
```go
// internal/service/file_features.go:
GetBucketLogging(ctx, tenant, bucket)   → 返回 LoggingConfig
SetBucketLogging(ctx, tenant, bucket, targetBucket, targetPrefix) → CRUD
DeleteBucketLogging(ctx, tenant, bucket) → CRUD
```

**REST API 层完整：**
```go
// internal/api/rest/router.go:
r.Get("/buckets/{bucket}/logging", h.GetBucketLogging)
r.Put("/buckets/{bucket}/logging", h.PutBucketLogging)
r.Delete("/buckets/{bucket}/logging", h.DeleteBucketLogging)
```

**S3 兼容层完整：**
```go
// internal/api/s3compat/handler.go:
getBucketLogging → XML 响应
putBucketLogging → 解析 XML，调用 SetBucketLogging
deleteBucketLogging → 清空配置
```

**迁移文件完成：**
```sql
-- internal/repository/migrations/sqlite/0023_bucket_logging.up.sql
-- repositories/migrations/postgres/0023_bucket_logging.up.sql
```

**但是，`WriteAccessLog` 的调用点数为零：**
```bash
$ grep -rn "WriteAccessLog" internal/ --include='*.go'
internal/repository/sql_buckets.go:368  // 只有定义
internal/repository/repository.go:274    // 只有接口声明
```

没有任何 handler、middleware、interceptor 或 goroutine 调用它。**配置了 logging 的桶，没有一条访问日志被写入。**

同时，现有的 middleware `AccessLog`（`internal/middleware/middleware.go:85`）仅输出到 `slog`：

```go
func AccessLog(logger *slog.Logger) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            // ...
            logger.Info("access", ...)  // ← 仅结构化日志，不入 DB
        })
    }
}
```

### 产品价值

| 维度 | 影响 |
|------|------|
| **合规强制** | SOC2、PCI DSS、HIPAA、FedRAMP 均要求对象级访问记录。缺失该功能是合规硬阻断。 |
| **安全审计** | 无法追踪"谁在什么时候访问了哪个对象"，安全事件调查不可行 |
| **运维诊断** | 无法分析访问模式、热点对象、异常流量 |
| **计费对账** | 无法按访问量计费（例如每 GB GET 次数） |

### 典型场景

1. **SOC2 合规**：需要记录过去 12 个月的所有对象访问，支持审计查询
2. **安全事件响应**：检测到数据泄露后，追溯特定对象被哪些 IP、在什么时间被读取
3. **使用分析**：分析最常访问的对象、访问时间分布、客户端 UA 分布

### 架构方案概要

```
HTTP Request
    │
    ▼
middleware.AccessLog ─→ slog (保留现有)
    │
    └─→ AccessLogWriter (新增)
            │
            ├─→ 同步：WriteAccessLog(ctx, ...) → 目标桶日志对象
            │    - 异步写缓冲（batch 写入减少写入放大）
            │    - 日志对象格式：S3 标准日志格式
            │    - 按桶分目标（LoggingConfig.Target）
            │
            └─→ 异步：写入本地缓冲区 → 定时 flush → 目标桶
                 - 内存缓冲区避免每个请求一次 DB 写
                 - 可配置：batch size、flush interval
```

**关键设计点：**
- 日志写入目标桶本身也是对象 PUT——需要避免递归（写日志到目标桶时触发日志写）
- 批量写入：1000 条或 5 秒 flush 一次，减少写入放大
- 日志格式：S3 标准格式（`<bucket> <key> <time> <ip> <user> <request_id> <operation> <status> <bytes> <latency> <ua>`）
- 可选：日志写入 job queue 异步处理，不阻塞请求路径

### 边界情况与风险

| 场景 | 处理 |
|------|------|
| 目标桶与原桶相同 | 必须跳过日志写入的日志写入（递归死循环检测） |
| 目标桶不存在 | 降级为 `slog.Warn`，不阻塞请求；尝试周期性重创建 |
| 高 QPS 下的写入压力 | 批量写入 + 内存缓冲 + 可丢弃（best-effort）或保证送达（job queue） |
| 日志对象生命周期 | 日志对象自身应遵守 Lifecycle 规则自动过期（S3 标准做法） |

---

## 方向三：Object Lock 完整治理模型——从"单个时间戳"到"S3 合规锁"

### 现状与代码证据

当前 Object Lock 的实现极为骨架化：

**唯一的 lockdown 机制：**
```go
// internal/repository/repository.go:68
type Object struct {
    LockedUntil *time.Time // present when Object Lock is active
}
```

**锁定检查（写覆盖/硬删除前）：**
```go
// internal/service/file_crud.go:87-95
func (s *FileService) checkLockBeforeOverwrite(...) {
    if cur.LockedUntil != nil && cur.LockedUntil.After(time.Now()) {
        return ErrLocked  // 统一错误，不分模式
    }
}

// internal/service/file_crud.go:294-302
func (s *FileService) hardDeleteObject(...) {
    if obj.LockedUntil != nil && obj.LockedUntil.After(time.Now()) {
        return ErrLocked
    }
    if obj.Metadata["_aero_legal_hold"] == "ON" {
        return ErrLocked  // Legal Hold 存为元数据
    }
}
```

**Legal Hold 以元数据形式存储（漏洞重重）：**
```go
// internal/api/s3compat/handler.go:93-98
if lh := r.Header.Get("x-amz-object-lock-legal-hold"); lh == "ON" || lh == "on" {
    meta["_aero_legal_hold"] = "ON" // ← 用户元数据可以被随意覆写/删除
}
```

**桶级默认锁配置：**
```go
// internal/repository/repository.go:44
type BucketConfig struct {
    ObjectLockSeconds int  // 默认 retention 秒数；仅秒数，无模式
}
```

**缺失的 S3 Object Lock 核心功能：**

| 功能 | S3 标准 | 当前状态 |
|------|---------|---------|
| Retention Mode | `GOVERNANCE` / `COMPLIANCE` 二选一 | ❌ 无模式 |
| `PUT /{key}?retention` | 设置 `x-amz-object-lock-retain-until-date` + mode | ❌ 无端点 |
| `PUT /{key}?legal-hold` | 切换 `x-amz-object-lock-legal-hold: ON/OFF` | ❌ 无端点（靠 PUT 头部间接设置） |
| `x-amz-bypass-governance-retention` | 拥有 `s3:BypassGovernanceRetention` 可覆写 GOVERNANCE 锁 | ❌ 无识别 |
| Legal Hold DB 列 | 独立列，非元数据 | ❌ 元数据存储（用户可覆盖） |
| 锁定对象不可覆盖 metadata/tags | S3 行为 | ❌ 无保护 |

### 产品价值

| 维度 | 影响 |
|------|------|
| **合规强制** | SEC 17a-4(f)、FINRA、CFTC 要求 WORM 存储；没有 GOVERNANCE/COMPLIANCE 区分无法通过审计 |
| **数据保护** | Legal Hold 存为元数据可被任何 API 调用覆写，是伪保护 |
| **S3 兼容可信度** | 配置了 Object Lock 的桶在关键操作（绕锁删除）时不报错或行为与 S3 不同，破坏信任 |

### 典型场景

1. **金融合规（SEC 17a-4）**：电子邮件归档必须使用 COMPLIANCE 模式锁定——没有任何人（包括管理员和根用户）可以在保留期内删除或修改对象
2. **电子发现（eDiscovery）**：法律诉讼期间对相关文档设置 Legal Hold——独立于保留期，直到诉讼结束才释放
3. **治理覆盖**：GOVERNANCE 模式下，拥有特殊权限的管理员可以在保留期结束前调整（用于误锁定纠正）

### 架构方案

**新增/修改的数据模型：**

```go
type RetentionMode string
const (
    RetentionGovernance  RetentionMode = "GOVERNANCE"
    RetentionCompliance  RetentionMode = "COMPLIANCE"
)

type Object struct {
    // ... 现有字段
    LockedUntil    *time.Time     // 保留到期时间
    RetentionMode  RetentionMode  // 新增：锁定模式
    LegalHold      bool           // 新增：独立布尔列（替代元数据 hack）
}

type BucketConfig struct {
    // ...
    ObjectLockEnabled bool         // 新增：桶级 Object Lock 开关
    RetentionMode    RetentionMode // 新增：桶默认模式
    RetentionDays    int           // 新增：桶默认保留天数
}
```

**需要的新端点：**
- `PUT /{bucket}/{key}?retention` — 设置保留模式 + 日期
- `PUT /{bucket}/{key}?legal-hold` — 设置 Legal Hold ON/OFF
- `PUT /{bucket}?object-lock` — 已存在但需扩展支持启用开关 + 模式

**需要识别的 Header：**
- `x-amz-object-lock-retain-until-date` — 保留到期日
- `x-amz-object-lock-mode` — `GOVERNANCE` | `COMPLIANCE`
- `x-amz-bypass-governance-retention: true` — 绕过 GOVERNANCE 锁

### 边界情况与风险

| 场景 | 处理 |
|------|------|
| COMPLIANCE 锁不可绕过 | 即使 admin 调用硬删除也必须拒绝；只有锁到期才能删除 |
| GOVERNANCE 锁 + bypass header | 有 `s3:BypassGovernanceRetention` 权限的用户可以绕过 |
| Legal Hold + Retention 同时生效 | 两者任一锁定，对象不可写/删；Legal Hold 独立于 Retention 到期 |
| 已有 Object Lock 的桶启用版本控制 | Object Lock 要求版本控制已启用（S3 规则）；迁移时需要验证 |
| 桶默认锁与对象级锁冲突 | 对象级锁设置覆盖桶默认（S3 行为） |

---

## 方向四：对象生命周期状态机——从"二元过期"到"完整存储类转换"

### 现状与代码证据

当前生命周期仅支持简单的过期删除：

```go
// internal/repository/repository.go:44
type BucketConfig struct {
    ExpireAfterDays int    // 从 updated_at 起 N 天后执行
    ExpireAction    string // "soft_delete" | "hard_delete"（仅两个选项）
}
```

**生命周期执行器：**
```go
// internal/reconcile/lifecycle.go
// NewLifecycle 创建一个周期扫描 expired 对象的 worker
// 它查询 ListExpired（条件：updated_at + ExpireAfterDays），然后执行对应 action
```

**storage_class 字段已存在但不用于转换：**
```go
// internal/repository/repository.go:67
type Object struct {
    StorageClass string // e.g. STANDARD, STANDARD_IA, GLACIER; "" = STANDARD
}
```

**REST handler 已暴露生命周期 CRUD：**
```go
// internal/api/rest/router.go:
r.Put("/buckets/{bucket}/lifecycle", adm.PutBucketLifecycle)
r.Get("/buckets/{bucket}/lifecycle", h.GetBucketLifecycle)
```

**S3 兼容的 `?lifecycle` 端点解析标准 XML：**
```go
// internal/api/s3compat/bucketconfig.go
// putBucketLifecycle 解析 LifecycleConfiguration XML 中的 Expiration Days
```

**缺失的核心能力：**

| S3 Lifecycle 能力 | 当前状态 |
|--------------------|---------|
| `Transition`（STANDARD → STANDARD_IA → ...） | ❌ 无转换动作 |
| `NoncurrentVersionExpiration` | ❌ 非当前版本过期 |
| `NoncurrentVersionTransition` | ❌ 非当前版本转换 |
| `Expiration.DeleteMarker` | ❌ 无删除标记概念 |
| `AbortIncompleteMultipartUpload` | ❌ 超时未完成分片上传清理 |
| `RestoreObject` 冷存储恢复 | ❌ 当前 `?restore` 做的是软删除恢复 |

### 产品价值

| 维度 | 影响 |
|------|------|
| **成本优化 #1** | 存储分层是最直接的成本控制手段。标准→低频→归档的成本差可达 10–50× |
| **SaaS 竞品差异化** | 不支持分层的对象存储无法与 S3、Backblaze B2、Wasabi 等竞争企业级客户 |
| **自动化运维** | 无需人工干预的数据老化策略 |

### 典型场景

1. **日志归档**：最近 30 天的日志在 `STANDARD`，30–90 天在 `STANDARD_IA`，90 天后转为 `GLACIER`，1 年后删除
2. **媒体存储**：热内容在 NVMe 本地存储，冷内容在 S3 Standard，归档在 Glacier Deep Archive
3. **上传清理**：超过 7 天未完成的 multipart upload 自动 abort

### 架构方案

**扩展的生命周期规则模型：**

```go
type LifecycleRule struct {
    ID          string
    Status      string // "Enabled" | "Disabled"
    Filter      LifecycleFilter // prefix + tag 过滤
    
    // 转换动作（可多个，按时间升序排列）
    Transitions []Transition
    
    // 过期动作
    Expiration  *Expiration
    
    // 非当前版本
    NoncurrentVersionTransition  []NoncurrentVersionTransition
    NoncurrentVersionExpiration  *NoncurrentVersionExpiration
    
    // 分片上传中止
    AbortIncompleteMultipartUpload *AbortIncompleteMultipartUpload
}

type Transition struct {
    Days         int    // 对象 age
    StorageClass string // STANDARD_IA | ONEZONE_IA | GLACIER | DEEP_ARCHIVE
}

type NoncurrentVersionTransition struct {
    NoncurrentDays int
    StorageClass   string
}
```

**StorageClass 感知的后端抽象：**
`storage.Storage` 接口需要扩展以声明支持的 StorageClass 及其成本特征。本地 FS 仅支持 `STANDARD`，S3 后端可以完整映射到 S3 StorageClass。

**Transition Worker：**
一个新的 job handler `JobTransitionObject`，执行：
1. 从源 backend `Get` 对象
2. `Put` 到目标 StorageClass 的后端
3. 验证 checksum
4. 更新 `storage_key` + `storage_class`
5. 删除源 blob（带 grace period 回滚窗口）

**Restore API：**
`POST /v1/files/{key}/restore` 或 `POST /{bucket}/{key}?restore` 发起从 GLACIER 恢复到 STANDARD 的操作：
- 返回 `202 Accepted` + job ID
- Transition Worker 异步完成复制
- 后续 GET 从 STANDARD 返回（带 `x-amz-restore` header）

### 边界情况与风险

| 场景 | 处理 |
|------|------|
| 转换期间 GET | 从源 StorageClass 服务，转换完成后切换到新位置 |
| 转换失败 | 重试 N 次后记录失败，保留源 blob；提供 admin retry 接口 |
| 多版本对象转换 | 每个版本独立转换，成本显著 |
| GLACIER 对象不可直接 GET | GET 必须返回 `x-amz-restore: ongoing-request="true"` 或直接响应 `InvalidObjectState` |

---

## 方向五：桶策略条件引擎扩展——从 `aws:SourceIp` 到完整 IAM 条件

### 现状与代码证据

当前桶策略实现高度骨架化：

```go
// internal/auth/policy.go
type Policy struct {
    Version   string
    Statement []Statement
}

type Statement struct {
    Effect    string
    Principal map[string]interface{}
    Action    []string
    Resource  []string
    Condition map[string]map[string][]string // 仅支持 [operator][key]values
}
```

**仅支持的 operator：** `IpAddress`、`NotIpAddress`（各只有一个 key：`aws:SourceIp`）

```go
// internal/auth/policy.go:136-148
func (s *Statement) matchesConditions(sourceIP string) bool {
    if len(s.Condition) == 0 { return true }
    for operator, conditions := range s.Condition {
        for key, values := range conditions {
            switch {
            case operator == "IpAddress" && key == "aws:SourceIp":
                // ... 唯一支持的组合
            case operator == "NotIpAddress" && key == "aws:SourceIp":
                // ... 唯一支持的另外组合
            }
        }
    }
    return true
}
```

**S3 handler 调用策略检查：**
```go
// internal/api/s3compat/handler.go:65-78
func (h *Handler) checkBucketPolicy(...) bool {
    p, err := auth.ParsePolicy(cfg.Policy)
    // ...
    if !auth.Allowed(p, action, host) {
        writeS3Error(w, r, service.ErrForbidden)
        return false
    }
    return true
}
```

**应用范围有限：** `checkBucketPolicy` 只在 S3 兼容 handler 中被调用（`PutObject`、`GetObject`、`DeleteObject`、`BucketDispatch`），REST API 层**完全不调用**桶策略。

**缺失的核心元素：**

| IAM 条件元素 | 支持情况 |
|-------------|---------|
| `aws:SourceIp` | ✅ 完整支持 |
| `aws:Referer` | ❌ |
| `aws:SourceVpc` | ❌ |
| `aws:SourceVpce` | ❌ |
| `aws:SecureTransport` | ❌ |
| `aws:CurrentTime` | ❌ |
| `aws:EpochTime` | ❌ |
| `aws:UserAgent` | ❌ |
| `aws:RequestTag` | ❌ |
| `aws:ResourceTag` | ❌ |
| `NotPrincipal` | ❌ |
| `NotAction` | ❌ |
| `NotResource` | ❌ |
| 资源级 ARN（`bucket/prefix/*`） | ❌ |
| 策略变量 `${aws:username}` | ❌ |

### 产品价值

| 维度 | 影响 |
|------|------|
| **企业安全** | 没有基于条件的访问控制无法实现：仅内部 VPC 可访问、仅 HTTPS 可访问、仅特定时间段可访问 |
| **多租户隔离** | 跨账户访问（Cross-account access）需要资源级授权策略 |
| **S3 兼容** | IAM 策略是 S3 的主要授权机制；当前实现过于基础无法迁移真实 S3 策略 |

### 典型场景

1. **VPC 隔离**：`"Condition": {"StringEquals": {"aws:SourceVpc": "vpc-12345"}}` — 仅特定 VPC 内的请求可访问桶
2. **HTTPS 强制**：`"Condition": {"Bool": {"aws:SecureTransport": "false"}}` — 拒绝所有 HTTP 请求，仅允许 HTTPS
3. **时间窗口**：`"Condition": {"DateGreaterThan": {"aws:CurrentTime": "2026-01-01T00:00:00Z"}}` — 仅在某时间后可用
4. **Referer 防盗链**：`"Condition": {"StringLike": {"aws:Referer": "https://mydomain.com/*"}}` — 仅特定网站可引用

### 架构方案

**条件引擎扩展（`internal/auth/policy.go`）：**

```go
// 新增条件类型枚举
type ConditionOperator int
const (
    CondStringEquals     ConditionOperator = iota
    CondStringNotEquals
    CondStringEqualsIgnoreCase
    CondStringLike
    CondStringNotLike
    CondBool
    CondDateGreaterThan
    CondDateGreaterThanEquals
    CondDateLessThan
    CondDateLessThanEquals
    CondIpAddress
    CondNotIpAddress
    CondArnEquals
    CondArnLike
    CondNull
)

// 条件上下文：从请求中提取的所有可评估键值对
type ConditionContext struct {
    SourceIp        string
    Referer         string
    SecureTransport bool
    CurrentTime     time.Time
    UserAgent       string
    SourceVpc       string
    SourceVpce      string
    // ...可根据需要扩展
}

// 条件评估接口
func evaluateCondition(op ConditionOperator, key string, values []string, ctx ConditionContext) bool
```

**条件键支持矩阵（初始实现建议）：**

| 条件键 | 类型 | 实现难度 |
|--------|------|---------|
| `aws:SourceIp` | IpAddress | ✅ 已有 |
| `aws:Referer` | StringLike | 低（HTTP Referer 头） |
| `aws:SecureTransport` | Bool | 低（`r.TLS != nil`） |
| `aws:CurrentTime` | DateGreaterThan/LessThan | 低（`time.Now()`） |
| `aws:UserAgent` | StringLike | 低（User-Agent 头） |
| `aws:SourceVpc` | StringEquals | 中（需从请求上下文获取） |
| `aws:SourceVpce` | ArnEquals | 中（需从请求上下文获取） |

**REST API 集成：**
将桶策略评估扩展到 REST API 层，放入 `internal/api/rest/handler.go` 的公共中间件或关键操作入口点，确保无论通过 S3 还是 REST 协议，桶策略均一致执行。

### 边界情况与风险

| 场景 | 处理 |
|------|------|
| 策略语法错误 | 记录 warn 并跳过策略（当前行为一致）；不应拒绝服务 |
| 无 `aws:SourceIp` 的请求 | 条件键不存在时返回 false（拒绝访问）— 安全优先 |
| Deny 优先级 | 即使 Allow 匹配，存在任何匹配的 Deny 即拒绝（已实现） |
| 匿名请求的策略评估 | 匿名 `Principal: "*"` 的策略应与匿名公读配置协同 |

---

## 跨方向关联与实施建议

以上五个方向在实现层面有天然关联，建议**按两个阶段**推进：

### 第一阶段：补齐断线管线（方向一、二）

| 方向 | 预估工作量 | 依赖 |
|------|-----------|------|
| **方向一：通知引擎** | 较大 | 需新建 `NotificationRouter` 组件；复用 webhook retry 机制 |
| **方向二：访问日志** | 中等 | 需在 middleware 层新增日志写入点；复用 batch 写入模式 |

**阶段一依赖：** 均依赖 `events` 包/`jobs` 包的现有基础设施，无外部依赖。

### 第二阶段：补齐治理模型（方向三、四、五）

| 方向 | 预估工作量 | 依赖 |
|------|-----------|------|
| **方向三：Object Lock** | 较大 | 需 schema 变更（`retention_mode`、`legal_hold` 列）、新增端点、修改锁定检查逻辑 |
| **方向四：生命周期状态机** | 大 | 需新的 `StorageClass` 后端映射、`Transition` worker、`RestoreObject` 完整实现 |
| **方向五：策略引擎** | 中等 | 需新增条件评估函数 + 条件上下文 + REST API 集成 |

**阶段二关联：** Object Lock（方向三）的 GOVERNANCE 模式（`BypassGovernanceRetention`）本质上是桶策略的一个特殊权限——策略引擎（方向五）需要支持 `s3:BypassGovernanceRetention` Action。生命周期状态机（方向四）的 Transition 和 Restore 事件应通过通知引擎（方向一）发出通知。访问日志（方向二）应记录 Object Lock bypass 操作和 Transition 事件。

---

## 附录：快速验证列表

以下检查点可用于在实施前验证当前状态的准确性：

### 方向一（通知引擎）
- [ ] `SetBucketNotifications` 后，通过 `GetBucketNotifications` 确认规则已持久化
- [ ] 事件发生后，webhook 是否收到该事件（无论规则配置如何）→ 确认断线存在
- [ ] 删除所有 notification rules 后，webhook 是否仍收到事件？→ 验证全局 webhook 独立于 per-bucket 规则

### 方向二（访问日志）
- [ ] 调用 `GET /v1/buckets/{b}/logging` 是否返回配置？→ 确认配置 CRUD 正常
- [ ] 配置 target bucket 后，访问对象 → target bucket 中是否有日志对象？→ 确认断线
- [ ] `middleware.go:AccessLog` 输出的 `slog` 行是否包含所有审计所需字段？（request_id、tenant、key、method、status、latency）

### 方向三（Object Lock）
- [ ] PUT 带 `x-amz-object-lock-legal-hold: ON` → 确认 metadata 中有 `_aero_legal_hold: ON`
- [ ] PUT 带相同 key 覆盖上述对象 → 是否仍含有 `_aero_legal_hold`？→ 验证 metadata 可被覆盖
- [ ] 通过 S3 SDK 调用 `put_object_legal_hold()` → 确认 404 或 501

### 方向四（生命周期）
- [ ] PUT 带 `x-amz-storage-class: GLACIER` → 确认 `storage_class` 字段
- [ ] GET 返回的 `x-amz-storage-class` header → 确认回显
- [ ] `?restore` 对非软删除对象的调用 → 确认当前行为

### 方向五（策略引擎）
- [ ] 设置包含 `"aws:SourceIp": ["1.2.3.4/32"]` 的桶策略 → 仅该 IP 可访问
- [ ] 设置包含 `"aws:Referer"` 条件的策略 → 确认 Referer 条件被静默忽略
- [ ] 通过 REST API（非 S3）访问有策略的桶 → 确认策略不生效
