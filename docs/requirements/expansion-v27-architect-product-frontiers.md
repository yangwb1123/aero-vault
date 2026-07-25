# 高价值扩展方向 v27 — 版本化状态机、预签名约束、凭据审计、标签生命周期与跨桶复制

> **分析范围：** 全代码库深度扫描 — `cmd/server/main.go` + `internal/*`（service、storage、repository、ai、events、auth、api/rest、api/s3compat、api/webdav、mcp、middleware、reconcile、replication、antivirus、jobs、telemetry、config、thumbnail、webui、snapshot、cli）共计 237+ 个 `.go` 源文件，48 对迁移 SQL 文件，3 套 SDK（Go/Python/JS），Web UI，MCP 双模式（HTTP+stdio），WebDAV，完整配置层，`deploy/` 全套 Helm/Grafana/Prometheus/OTel 配置  
> **分析日期：** 2026-07-11  
> **视角：** 资深架构师 / 产品经理  
> **核心原则：** 不编写任何实现代码。每个方向提供：现状 → 代码锚点 → 产品/架构影响 → 边界情况。

---

## 方向总览

| # | 方向 | 类型 | 严重度 | 核心问题 | 既有分析覆盖 |
|---|------|------|--------|---------|-------------|
| **1** | **S3 版本化状态机 — Suspend/Re-enable 与 Null Version 语义** | S3 协议兼容/架构 | **P1** | 当前版本化是简单布尔开关，无法表达 S3 的 Enabled → Suspended → Enabled 三态转换及 Null Version 语义；Suspending 后 PUT 应产生 Null Version 而非覆盖当前版本 | **零覆盖** — 无独立深度分析 |
| **2** | **预签名 URL 上传约束绑定与验证** | 安全/数据完整性 | **P1** | 预签名 PUT URL 生成时不绑定 Content-Type/Content-Length/Caller 身份，上传时不验证任何约束；持有 URL 者可上传任意内容 | **零覆盖** — 无独立深度分析 |
| **3** | **每凭据访问审计轨迹与异常模式监测** | 安全/可观测性 | **P1** | 无法追溯"哪个 API Key / JWT 做了什么操作"；凭据泄漏后无法定量评估损失范围；无调用模式基线与异常检测 | **零覆盖** — 无独立深度分析 |
| **4** | **标签与元数据驱动的生命周期规则** | 产品/成本 | **P2** | 生命周期仅支持按更新天数过期；无法基于对象标签、Content-Type、大小、元数据值等条件触发自动删除或转换 | **零覆盖** — 无独立深度分析 |
| **5** | **跨桶/跨租户对象复制与授权** | 架构/协议 | **P2** | CopyObject 仅在单桶内完成；跨桶复制需手动 GET+PUT（两倍带宽与延迟）；无跨租户复制授权模型 | **零覆盖** — 无独立深度分析 |

---

## 1. 🔴 S3 版本化状态机 — Suspend/Re-enable 与 Null Version 语义

### 现状

当前版本化实现是一个简单的布尔开关，存储在 `buckets.versioning` 列（`internal/repository/repository.go:45`）：

```go
type BucketConfig struct {
    Versioning        bool    // true = "Enabled", false = "not enabled / never configured"
    // ...
}
```

代码锚点：

- `internal/api/s3compat/bucketconfig.go:30-66` — `getBucketVersioning` 在 `Versioning==true` 时返回 `"Enabled"`，否则省略 `Status`（符合 S3 规范）；`putBucketVersioning` 仅检查 `in.Status == "Enabled"`，`"Suspended"` 被映射为 `Versioning=false`（等同于从未启用）。
- `internal/service/file_crud.go:132-163` — `Put` 方法根据 `bcfg.Versioning` 决定是否生成 VersionID。`Versioning==false` → 简单 `UpsertObject`（覆盖）；`Versioning==true` → `InsertObjectVersion`。
- `internal/service/file_multipart.go` — 与上对称：`InitMultipart` 在 `Versioning==true` 时生成唯一 `@v<id>` storage key。
- `internal/repository/sql_buckets.go:270-279` — `SetBucketVersioning(ctx, tenant, bucket, enabled bool)` 只接受二进制值。

**问题：S3 版本化有三态，不是二态。**

S3 版本化的状态机：
```
Unversioned (初始) ──PUT ?versioning Status=Enabled──→ Enabled
Enabled ──PUT ?versioning Status=Suspended──→ Suspended
Suspended ──PUT ?versioning Status=Enabled──→ Enabled
Suspended ──PUT ?versioning Status=Suspended──→ (相同状态, no-op)
```

每个状态的行为差异：

| 操作 | Enabled | Suspended | Unversioned |
|------|---------|-----------|-------------|
| PUT 新对象 | 创建新版本（版本 ID 非空） | 创建 Null Version（覆盖 Null Version） | 覆盖当前对象 |
| DELETE 对象 | 创建删除标记（Delete Marker） | 创建删除标记（Delete Marker） | 删除对象 |
| GET 当前版本 | 返回当前版本（最新） | 返回 Null Version | 返回当前对象 |
| 现有版本 | 所有版本可访问 | 现有版本**保留并可访问** | — |

当前实现中，**Suspending 版本化后 PUT 会直接覆盖当前对象**，之前的版本因为 `UpsertObject` 而被丢失。这与 S3 行为严重不一致——Suspending 的核心语义是"暂停为后续写入创建新版本，但历史版本保持可读"。

### 为什么需要

| 角度 | 影响 |
|------|------|
| **S3 兼容性断裂** | 使用 `aws s3api put-bucket-versioning --versioning-configuration Status=Suspended` 的客户端期望：① 现有版本被保留；② 后续 PUT 具有 `version-id: "null"`；③ GET 返回 Null Version。当前实现静默破坏此契约——现有版本在随后 PUT 时丢失。 |
| **数据安全** | 用户启用版本化→累积多个版本→暂停版本化（期望保留历史）→PUT 新对象→`UpsertObject` 覆盖当前行→既有版本指向孤儿 blob（metadata 被替换）。这是一个不可逆的数据丢失路径。 |
| **Null Version 语义缺失** | S3 的 Null Version 是一个特殊的版本 ID，表示"版本化暂停期间写入的对象"。多个 PUT 在 Suspended 期间会覆盖同一个 Null Version。当前无此概念，Suspended 期间每个 PUT 仍可能产生新行（取决于 `UpsertObject` 是否按 `key`+`tenant`+`bucket` 去重）。 |

### 影响范围

| 层 | 变更 |
|----|------|
| `buckets.versioning` 列 | 从 `BOOLEAN` 改为 `TEXT`（`"Unversioned"`/`"Enabled"`/`"Suspended"`），或新增 `versioning_status` 列（保留原始 `versioning` 的布尔兼容） |
| `BucketConfig.Versioning` | 从 `bool` 改为枚举类型或使用 `VersioningStatus` 字符串常量 |
| `SetBucketVersioning` | 接收 `"Enabled"`/`"Suspended"` 而非布尔值；校验状态转换合法性（Unversioned → Suspended 非法） |
| `InsertObjectVersion` / `UpsertObject` | 新增 `NullVersion` 变体：Suspended 状态下 PUT → 查找或创建 Null Version 行执行 `Upsert` |
| 版本 ID | Null Version 使用特殊版本 ID 常量（如 `"null"`）而非随机 UUID |
| `getBucketVersioning` XML | Suspended 状态返回 `<Status>Suspended</Status>`（当前省略 Status 字段而非显式返回 Suspended） |
| S3 `?versions` 列表 | Null Version 应出现在版本列表中，使用空版本 ID 或 `"null"` |
| Delete Marker | Suspended 状态下 DELETE 仍应创建 Delete Marker（与 Enabled 行为一致） |
| Reconcile/Retention | Null Version 的保留策略应与普通版本一致；版本过期规则应区分 Null Version |

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| Unversioned → Suspended 直跳 | S3 不允许；必须先经过 Enabled。应返回 `MalformedXML` 或 `InvalidBucketState`。 |
| Suspended → PUT → DELETE → 再 PUT | 中间 DELETE 会创建 Delete Marker；后续 PUT 应覆盖 Null Version 而非删除标记。行为须与 S3 一致。 |
| Suspended 状态下启用版本锁 | S3 要求版本化不能为 Suspended 才能启用 Object Lock。应返回错误。 |
| 迁移：现有布尔 `versioning=1` 对应哪个状态 | 应假设 `true` = "Enabled"，`false` = "Unversioned"。允许用户显式 PUT `Suspended`。 |
| `ListObjectVersions` 分页 | VersionIdMarker 须支持 `"null"` 字符串作为 marker。 |

---

## 2. 🔴 预签名 URL 上传约束绑定与验证

### 现状

预签名 URL 当前实现位于 `internal/storage/sign.go` 和 `internal/service/file_features.go`：

```go
// internal/service/file_features.go:130-144
func (s *FileService) PresignGet(ctx context.Context, tenant, bucket, key string, expiry time.Duration) (string, error) {
    obj, err := s.repo.GetObject(ctx, tenant, bucket, key)
    // ...
    return s.store.PresignGet(ctx, obj.StorageKey, expiry)
}

func (s *FileService) PresignPut(ctx context.Context, tenant, bucket, key string, expiry time.Duration) (string, error) {
    // ... validates key ...
    return s.store.PresignPut(ctx, storageKey(tenant, bucket, key), expiry)
}
```

Local 后端中的预签名 URL 实现（`internal/storage/local_read.go`）：

```go
func (s *LocalStorage) PresignGet(ctx context.Context, key string, expiry time.Duration) (string, error) {
    // 使用 HMAC-SHA256 签名生成一次性下载 URL
}

func (s *LocalStorage) PresignPut(ctx context.Context, key string, expiry time.Duration) (string, error) {
    // 使用 HMAC-SHA256 签名生成一次性上传 URL
}
```

S3 后端委托给 AWS SDK 的 `s3.PresignClient`。

**核心问题：预签名 PUT URL 在生成时和上传时之间没有约束一致性检查。**

| 缺失的约束 | 具体攻击场景 |
|-----------|-------------|
| **Content-Type** | Alice 生成预签名 URL 给 Bob 上传 `report.pdf`（Content-Type: application/pdf）。Bob 用同一个 URL 上传 `malware.exe`（Content-Type: application/x-msdownload）。存储记录的 Content-Type 与 Alice 预期不同。 |
| **Content-Length 范围** | 生成 URL 时预期上传 1MB-10MB 的文件。恶意用户上传 100GB 文件撑爆存储。 |
| **Content-MD5** | 生成 URL 时应绑定预期内容的 MD5 哈希。上传时不验证 MD5 将导致静默数据替换。 |
| **调用者身份** | 预签名 URL 泄漏后，任何人都可以用它上传任意内容。无法追溯"谁用了这个 URL"。 |
| **上限次数** | 预签名 URL 可被多次使用（S3 的 v2/v4 签名在有效期内可重复使用），生成者无法控制使用次数。 |
| **存储路径固定** | 当前预签名 URL 绑定了存储路径，但未防止路径遍历攻击（若 `key` 在预签名 URL 后被解析，可能被篡改）。 |

**代码等级证据：**

- `internal/storage/local_read.go:PresignPut` — 返回的 URL 在验证签名后执行 `PUT`，但签名仅验证 `key`+`expiry`，不验证 Content-Type/Content-Length/MD5。
- `internal/storage/sign.go` — HMAC 签名载荷仅包含 `key`、`expiry`、`method`。Content-Type 和 Content-Length 不在签名范围内。
- `internal/api/rest/handler.go:Presign` — REST 端点 `POST /v1/files/<key>/presign` 仅接收 `expiry` 参数，无约束输入。
- `internal/storage/s3.go:PresignPut` — AWS SDK 生成的预签名 URL 默认包含 Content-Type/Content-MD5 等条件，但当前代码未设置任何条件参数。

### 为什么需要

| 角度 | 影响 |
|------|------|
| **安全最低标准** | 预签名 URL 是"谁持有谁使用"的机制。若不绑定 Content-Type，攻击者可劫持 URL 上传不同类型内容（例如用图片上传 URL 上传 HTML 触发 XSS；用文档上传 URL 上传可执行文件）。 |
| **数据完整性** | 生成 URL 时已知预期内容的 MD5。上传时不验证意味着接收方无法保证存储的内容与预期的内容一致。 |
| **成本控制** | 无大小约束的预签名 URL = 无限存储成本攻击面。攻击者上传一个 1TB 文件即可产生计费。 |
| **S3 标准行为** | AWS S3 的预签名 URL 默认支持条件约束：`x-amz-content-sha256`、`Content-Type`、`Content-MD5` 等可作为签名参数。当前实现未利用此能力。 |
| **审计断裂** | 无法回答"哪个预签名 URL 被谁用了多少次"——当前完全无使用记录。 |

### 建议方案

**生成侧：**

```go
type PresignPutConstraints struct {
    ContentType   string // 预期 Content-Type（上传时强制匹配）
    MaxSize       int64  // 最大允许字节（上传时 Content-Length 不得超过此值）
    MinSize       int64  // 最小允许字节
    ContentMD5    string // 预期 body MD5 哈希（若已知）
    AllowedCaller string // 允许调用者的身份标识（key_hash / jwt_sub）
    MaxUses       int    // 最大使用次数（0 = 无限制）
}
```

**验证侧：**

预签名 PUT 请求到达时，验证器检查：
1. Content-Type 匹配（若生成时指定）
2. Content-Length 在 `[MinSize, MaxSize]` 范围内
3. Content-MD5 匹配（若生成时指定）
4. 调用者身份匹配（若生成时指定）
5. 使用次数未超限（若指定 MaxUses）

**签名扩展：** 约束条件加入 HMAC 签名载荷，防止篡改：

```
signature = HMAC-SHA256(secret, key + expiry + method + content-type + max-size + md5)
```

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| 上传时未提供 Content-Type | 若生成时绑定了 Content-Type，缺少此 header 应返回 403；未绑定则上传任意 Content-Type 均可。 |
| 分块上传（Transfer-Encoding: chunked）| `Content-Length` 未知，此时 `MaxSize` 约束无法通过 Header 验证，需在完全接收 body 后比较实际字节数。 |
| 预签名 URL + SSE 加密 | 约束绑定与 SSE-C 密钥需要同时存在于签名中。 |
| 并发使用计数 | MaxUses 需要原子递增计数器（Redis/DB 行锁），性能取决于精度要求。 |
| 与现有预签名 URL 的向后兼容 | 无约束的旧式签名应继续接受（默认无约束）；仅当生成时指定约束才验证。 |

---

## 3. 🔴 每凭据访问审计轨迹与异常模式监测

### 现状

当前系统中，请求经过 Auth 中间件验证后进入 handler，但**验证通过的凭据身份不会被传递到业务逻辑或访问日志中**。

代码锚点：

- `internal/auth/auth.go:Authenticate` — 返回 `AuthInfo{Tenant, Scopes}`，但不包含 `KeyHash`、`KeyLabel`、`JWTSub` 等凭据标识。
- `internal/auth/auth_middleware.go` — 中间件将 `AuthInfo` 注入上下文后，handler 仅通过 `mw.TenantFrom(ctx)` 获取租户信息，不获取凭据 ID。
- `internal/middleware/middleware.go:AccessLog` — 访问日志输出 `method`、`path`、`status`、`duration`、`tenant`，但不输出 `key_hash`、`key_label`、`auth_type`、`jwt_sub`。
- `internal/repository/audit.go` — 审计日志仅记录 admin 操作（`RecordAudit`），不记录常规数据面访问。
- `internal/api/rest/handler.go` — 所有 handler 从 `mw.TenantFrom(ctx)` 获取租户但不获取凭据标识。
- `internal/auth/store.go` — `APIKeyRecord` 包含 `LastUsedAt`、`TokenHash`、`Label`，但从不在认证成功后更新 `LastUsedAt`（`TouchAPIKey` 存在但仅在特定场景调用）。

**缺失的能力清单：**

| 能力 | 现状 | 生产需求 |
|------|------|---------|
| 每次请求记录 key_hash | ❌ | 追溯"哪个 Key 执行了什么操作" |
| 每次请求记录 key_label | ❌ | 人类可读的凭据名称识别 |
| 每次请求记录 auth_type | ❌ | 区分 Bearer JWT / API Key / SigV4 / Anonymous |
| 凭据使用率仪表盘 | ❌ | 识别未使用/过度使用的凭据 |
| 异常检测（新 IP、新设备） | ❌ | 识别凭据泄漏 |
| 凭据级限流 | ❌ | 单个凭据滥用不影响其他用户 |
| `LastUsedAt` 实时更新 | ❌ | 识别"僵尸凭据"以清理 |

### 为什么需要

| 角度 | 影响 |
|------|------|
| **安全事件调查** | 数据泄露发生后，第一问题就是"哪个 API Key 访问了哪些数据？当前无任何追踪能力。 |
| **SOC2/PCI 合规** | 合规审计要求"对系统内每个访问请求进行身份关联"。当前只能回答"请求来自哪个租户"，不能回答"来自哪个凭据"。 |
| **凭据清理** | 无法识别"90 天未使用的 API Key"来执行自动过期/提醒。凭据数量持续增长、暴露面不断扩大。 |
| **凭据级限流** | 一个恶意 API Key 的滥用会触发全局 rate limiter，影响所有合法用户。无法隔离有问题的凭据。 |
| **异常行为检测** | 如果某个 API Key 突然从新 IP 地址大量读取从未访问过的桶，这是潜在泄漏信号。当前无基线无可检测性。 |

### 建议方案

**Phase 1 — 凭据追踪（短期）：**

```
1. AuthInfo 扩展:
   type AuthInfo struct {
       Tenant   string
       Scopes   []string
       KeyHash  string   // 新增: sha256 hex of the API key, or "" for JWT/anonymous
       KeyLabel string   // 新增: human-readable label
       AuthType string   // 新增: "apikey" | "jwt" | "sigv4" | "anonymous"
       JWTSub   string   // 新增: JWT subject claim (when auth_type=jwt)
   }

2. AccessLog middleware 扩展:
   日志格式增加 key_hash / key_label / auth_type 字段

3. TouchAPIKey 在每次认证时调用:
   在 auth_middleware.go 中认证成功后异步更新 LastUsedAt

4. 自动过期检查:
   reconcile 周期扫描 expires_at < now() 的 API Key → 标记为 expired
```

**Phase 2 — 凭据分析（中期）：**

```
1. access_log 表:
   CREATE TABLE access_log (
       id            INTEGER PRIMARY KEY,
       tenant_id     TEXT NOT NULL,
       key_hash      TEXT,       -- 可为空 (anonymous / system)
       key_label     TEXT,
       auth_type     TEXT,
       method        TEXT,
       path          TEXT,
       status        INTEGER,
       bytes         INTEGER,
       latency_ms    INTEGER,
       remote_ip     TEXT,
       user_agent    TEXT,
       request_id    TEXT,
       created_at    TEXT NOT NULL DEFAULT (datetime('now'))
   );

2. 凭据使用报告:
   - Top-N 最活跃凭据
   - 未使用超过 N 天的凭据
   - 每个凭据的访问模式 (常见 IP, 常见路径, 常见方法)

3. 凭据级 rate limiter:
   在现有 token-bucket 基础上增加 per-key_hash 的独立限制
```

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| JWT 无稳定 sub 标识 | 若 JWT 无 sub 声明或 sub 格式不统一，凭据追踪优先基于 API Key；JWT 场景退回到 token_id。 |
| 高并发写入 access_log | 使用异步批量写入或 channel-backed writer，避免认证路径增加延迟。 |
| 预签名 URL 的调用者识别 | 预签名 URL 不关联特定凭据。建议在 access_log 中记录预签名 URL 的唯一 ID（若生成时分配）。 |
| KeyHash 的隐私保护 | KeyHash 是 sha256 哈希，无个人信息风险。但应限制 `access_log` 的访问权限（仅 admin）。 |
| 凭据生命周期事件 | 创建/吊销凭据应写入 audit_log；过期自动清理前应发送通知。 |

---

## 4. 🟠 标签与元数据驱动的生命周期规则

### 现状

当前生命周期系统只支持基于时间的过期删除（`expire_after_days` + `expire_action`），以 `BucketConfig` 字段存储：

```go
// internal/repository/repository.go:41-49
type BucketConfig struct {
    ExpireAfterDays int    // 从 updated_at 起多少天后过期
    ExpireAction    string // "soft_delete" | "hard_delete"
    // ...
}
```

代码锚点：

- `internal/reconcile/lifecycle.go` — `LifecycleJob.sweepExpired` 执行简单查询：`WHERE updated_at < now() - expire_after_days AND deleted_at IS NULL`。无任何标签、Content-Type、大小、前缀过滤条件。
- `internal/api/s3compat/xml.go:215-227` — `lifecycleConfiguration` 的 XML 解析仅支持 `Expiration.Days` 和 `Expiration.Date`，不支持 `Filter`、`Tag`、`And`、`Prefix` 等标准 S3 Lifecycle 过滤字段。
- `internal/api/s3compat/bucketconfig.go` — `putBucketLifecycle` 解析的 XML 直接丢弃了除 `Days` 外的所有字段。
- `internal/repository/sql_buckets.go` — 生命周期持久化仅存储二维 `(days, action)`，无 JSON 规则数组。

**缺失的 S3 Lifecycle 规则类型：**

| S3 规则类型 | 当前状态 | 用途 |
|------------|---------|------|
| `Expiration.Days` | ✅ 部分（无 Filter） | 按天过期 |
| `Expiration.Date` | ❌ 缺失 | 按固定日期过期 |
| `Expiration.ExpiredObjectDeleteMarker` | ❌ 缺失 | 自动清理孤立删除标记 |
| `NoncurrentVersionExpiration.NoncurrentDays` | ❌ 缺失 | 非当前版本过期 |
| `NoncurrentVersionExpiration.NewerNoncurrentVersions` | ❌ 缺失 | 保留最近 N 个非当前版本 |
| `Transition.Days` + `StorageClass` | ❌ 缺失 | 存储类转换 |
| `NoncurrentVersionTransition.NoncurrentDays` | ❌ 缺失 | 非当前版本存储类转换 |
| `AbortIncompleteMultipartUpload.DaysAfterInitiation` | ❌ 缺失 | 自动废弃未完成分片上传 |
| `Filter.Prefix` | ❌ 缺失 | 按前缀过滤 |
| `Filter.Tag` | ❌ 缺失 | 按标签过滤 |
| `Filter.And` | ❌ 缺失 | 多条件组合过滤 |
| `Filter.ObjectSizeGreaterThan` | ❌ 缺失 | 按大小过滤 |
| `Filter.ObjectSizeLessThan` | ❌ 缺失 | 按大小过滤 |

**当前 Tags 和 Metadata 的使用范围：**

- Tags 只能通过 `PUT /v1/files/{key}/tags` 或 S3 `PUT ?tagging` 设置，存储在 `object_tags` 表。
- Metadata 由 `_aero_*` 系统键和用户自定义键组成，存储在 `objects` 表的 `metadata` JSON 列。
- 两者均**不作为生命周期规则的输入**。

### 为什么需要

| 角度 | 影响 |
|------|------|
| **成本精确管理** | "所有带 `env=dev` 标签的对象 7 天后过期"——比"整桶 7 天过期"精确得多。标签驱动规则使自动清理更安全（仅清理标记对象）。 |
| **合规自动化** | "所有 Content-Type=application/pdf 且 retention=90 的对象 90 天后过渡到 GLACIER"——无需用户手动标记或移动。 |
| **存储效率** | "大于 100MB 且最后访问在 30 天前的日志文件转换到 STANDARD_IA"——基于大小的规则可拦截大文件异常存储成本。 |
| **多规则组合** | 一个桶可以有多个生命周期规则，每条规则有不同的 `Filter` + `Action`。当前只能有一个全局过期策略。 |
| **S3 兼容性** | 使用 `aws s3api put-bucket-lifecycle-configuration` 的 SDK/工具无法与当前系统正常工作，所有 `Filter` 参数被静默丢弃。 |

### 建议方案

**规则模型：**

```go
type LifecycleRule struct {
    ID     string `xml:"ID" json:"id"`
    Status string `xml:"Status" json:"status"` // "Enabled" | "Disabled"
    Filter LifecycleFilter `xml:"Filter" json:"filter"`
    Expiration *LifecycleExpiration `xml:"Expiration,omitempty" json:"expiration,omitempty"`
    NoncurrentVersionExpiration *LifecycleNoncurrentExpiration `xml:"NoncurrentVersionExpiration,omitempty"`
    Transitions []LifecycleTransition `xml:"Transition,omitempty" json:"transitions,omitempty"`
    AbortIncompleteMultipartUpload *LifecycleAbortMultipart `xml:"AbortIncompleteMultipartUpload,omitempty"`
}

type LifecycleFilter struct {
    Prefix          string              `xml:"Prefix,omitempty" json:"prefix,omitempty"`
    Tag             *LifecycleTag       `xml:"Tag,omitempty" json:"tag,omitempty"`
    And             *LifecycleAnd       `xml:"And,omitempty" json:"and,omitempty"`
    ObjectSizeGT    *int64              `xml:"ObjectSizeGreaterThan,omitempty" json:"size_gt,omitempty"`
    ObjectSizeLT    *int64              `xml:"ObjectSizeLessThan,omitempty" json:"size_lt,omitempty"`
}
```

**执行引擎：**

`LifecycleJob.sweepExpired` 从单条 SQL 升级为规则引擎：

1. 加载桶的全部 `LifecycleRule[]`
2. 对每条 `Enabled` 规则：
   - 构建 SQL 过滤条件组合（prefix LIKE、tags @>、content_type IN、size BETWEEN 等）
   - 对匹配对象执行指定 `Action`（soft_delete / hard_delete / transition）
3. 每条规则的执行进度独立记录和度量

**SQL 查询引擎扩展：**

```sql
-- 标签过滤：规则对应 "Tag: {Key: env, Value: dev}"
SELECT o.* FROM objects o
JOIN object_tags t ON o.id = t.object_id
WHERE t.key = 'env' AND t.value = 'dev'
  AND o.tenant_id = $1 AND o.bucket = $2

-- 大小过滤：规则对应 "ObjectSizeGreaterThan: 104857600"
SELECT * FROM objects
WHERE size > 100000000
  AND tenant_id = $1 AND bucket = $2

-- 组合过滤（And）
SELECT o.* FROM objects o
JOIN object_tags t ON o.id = t.object_id
WHERE o.key LIKE 'logs/%'
  AND t.key = 'env' AND t.value = 'staging'
  AND o.size < 1000000
```

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| 多条规则匹配同一对象 | 默认"最长保留期优先"规则。多条规则指定不同的 Transition 目标时，按"更冷存储类优先"。 |
| 标签值含特殊字符 | 标签 key 和 value 应正确转义 SQL 参数，避免 SQL 注入。使用参数化查询。 |
| 规则 ID 冲突 | 规则 ID 在桶内唯一；重复 ID 的 PUT 应返回 400。 |
| 大量规则（50+）的性能 | 单桶生命周期规则数量应有上限（S3 限制 1000 条）。每条规则独立执行 SQL 可能产生 N+1 查询——应批量化。 |
| 规则从"无"到"有"的存量适配 | 新规则应能作用于已有对象（`updated_at` 不变）。规则生效时间 = 下一个 reconcile 周期。 |

---

## 5. 🟠 跨桶/跨租户对象复制与授权

### 现状

当前所有的对象操作都限定在 `(tenant, bucket, key)` 三元组内。`CopyObject` 仅在 S3 协议层实现（`internal/api/s3compat/extra.go:33-65`），且通过 GET+PUT 实现：

```go
func (h *Handler) copyObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
    srcBucket := r.Header.Get("x-amz-copy-source")
    // 解析 srcBucket/srcKey
    rc, src, err := h.svc.Get(ctx, tenant, srcBucket, srcKey)  // 读取源
    defer rc.Close()
    obj, err := h.svc.Put(ctx, tenant, bucket, key, rc, src.Size, opts)  // 写入目标
    // ...
}
```

**代码锚点：**

- `internal/service/file.go` — `FileService` 没有 `Copy(source, destination)` 方法。所有复制都是 GET+PUT 在 handler 层实现。
- `internal/storage/storage.go` — `Storage` 接口没有 `Copy(key, dstKey)` 或 `CopyFrom(source, dest)` 方法。没有存储层的服务器端复制原语。
- `internal/service/file_crud.go:Get` + `Put` — 跨桶复制必须：读取完整流 → 写入目标路径。对于大文件（>1GB），两倍带宽 + 两倍 I/O。
- `internal/auth/auth.go` — Authorization scope 按 `(action)` 校验，不区分 source_tenant/dest_tenant。
- `internal/service/file_multipart.go` — 无 `UploadPartCopy` 方法，>5GB 的跨桶复制无法完成（需要 multipart copy）。

**跨桶复制的限制：**

| 场景 | 当前行为 | 期望行为 |
|------|---------|---------|
| 同桶复制 | 通过 GET+PUT 实现 | 服务端 `Copy` 零数据传输（更改 metadata/key） |
| 跨桶复制（同租户） | 通过 GET+PUT 实现 | 存储层 `Copy` 原语（S3 后端直接 CopyObject） |
| 跨租户复制 | **不支持** | 授权后复制，源租户可授予读权限 |
| 跨区域复制 | 通过 ReplicationWorker 实现（异步） | 需要额外的同步复制 API |
| 大对象跨桶复制 | >5GB 失败（无 UploadPartCopy） | 自动降级为 multipart copy |

### 为什么需要

| 角度 | 影响 |
|------|------|
| **性能** | 同一个 S3 后端（或 Local FS）内复制对象时，GET+PUT 需要完整读取→写入。S3 后端 `CopyObject` 是原子元数据操作（零数据移动），速度提升数万倍。 |
| **带宽成本** | 跨桶复制在 S3 后端按对象元数据重写计费（$0.0000/GB），而 GET+PUT 按读取+写入双向计费。对于 PB 级活跃存储，差异巨大。 |
| **跨租户数据共享** | 多租户场景下，"产品团队"需要共享设计素材给"市场团队"。当前只能通过第三方中介或直接 PVC 暴露来完成，有安全隐患。 |
| **UploadPartCopy 兼容** | AWS SDK 自动对 >5GB 的 CopyObject 请求降级为 multipart copy（`UploadPartCopy`）。当前无此实现导致大文件跨桶复制必然失败。 |

### 建议方案

**Phase 1 — 存储层 Copy 原语（核心架构）：**

```go
// internal/storage/storage.go — 新增
Copy(ctx context.Context, srcKey, dstKey string) (ObjectInfo, error)
```

每个后端的实现：

| 后端 | 实现方式 |
|------|---------|
| `LocalStorage` | `os.Link()`（硬链接，零拷贝）或 `io.Copy`（跨文件系统时） |
| `S3Storage` | `CopyObject` API（零数据移动） |
| `OSSStorage` | 阿里云 `CopyObject` API |
| `COSStorage` | 腾讯云 `CopyObject` API |

**Phase 2 — Service 层跨桶复制：**

```go
// internal/service/file_features.go — 新增
CopyOptions struct {
    SourceTenant string
    SourceBucket string
    SourceKey    string
    MetadataDirective string // "COPY" | "REPLACE"
    TaggingDirective   string // "COPY" | "REPLACE"
}
func (s *FileService) Copy(ctx, destTenant, destBucket, destKey string, opts CopyOptions) (Object, error)
```

**Phase 3 — 跨租户复制授权：**

```go
type CrossTenantGrant struct {
    SourceTenant string
    SourceBucket string
    SourcePrefix string    // 可选前缀
    TargetTenant string
    ExpiresAt    time.Time
    CreatedAt    time.Time
    CreatedBy    string
}
```

- 存储在 `cross_tenant_grants` 表
- `Copy` 方法校验 `SourceTenant` → `TargetTenant` 是否有有效授权
- 可选的自动过期（合规数据共享需要限时授权）

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| 跨后端复制（Local → S3） | 存储层 `Copy` 原语仅在**同后端**内生效；跨后端复制仍需要读取→写入流。应在 Service 层自适应判断：同后端 → 调用 `Copy`；异后端 → 回退到 GET+PUT。 |
| 版本化桶的复制 | 跨桶复制时目标桶的版本化设置应决定是否需要新版本 ID；源版本 ID 可以保留在 metadata 中。 |
| 复制 + Object Lock | 目标对象应继承源对象的保留设置（除非 bucket 默认锁覆盖）。跨租户时更严格。 |
| 复制 + SSE | 目标对象应用目标 bucket 的默认加密设置，而非源对象的 KMS key。SSE-C 复制的密钥协商。 |
| 权限校验 | 同租户跨桶 → 需要 source 的读权限 + dest 的写权限。跨租户 → 额外的 `CrossTenantGrant` 授权检查。 |
| UploadPartCopy 的偏移与范围 | S3 支持 `x-amz-copy-source-range` 头指定复制字节范围用于分片。当前无此支持，multipart copy 无法实现。 |

---

## 总结：优先级排序与建议路线

| 优先级 | 方向 | 预估工作量 | ROI | 依赖关系 |
|--------|------|-----------|-----|---------|
| **P1** | #1 版本化状态机 | 中（~500 行 + migration） | 高（S3 兼容性底线，数据安全） | 无 |
| **P1** | #2 预签名 URL 约束 | 低（~300 行） | 极高（安全加固，低成本高回报） | 无 |
| **P1** | #3 凭据审计轨迹 | 中（~500 行 + schema） | 高（合规必选项） | 无 |
| **P2** | #4 标签生命周期 | 大（~1500 行 + schema + 执行引擎重写） | 高（ROI 取决于客户量） | 依赖 #1（版本化生命周期规则） |
| **P2** | #5 跨桶复制 | 大（~2000 行 + 存储层 + service + auth） | 高（企业采纳关键功能） | 依赖 #3（跨租户授权需要凭据审计） |

**建议执行顺序：** #2 → #3 → #1 → #5 → #4。先修复安全缺口（预签名约束 + 凭据审计），再完成 S3 兼容性（版本化状态机），最后基础设施级扩展（跨桶复制 + 标签生命周期）。
