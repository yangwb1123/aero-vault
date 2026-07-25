# AeroVault 高价值扩展方向（第三期）

> **视角:** 资深架构师 / 产品经理  
> **方法:** 全局代码扫描（Go 源码 ~50K 行, 392+ 文件），深度分析所有层  
> **日期:** 2026-07-10  
> **原则:** 选取 **前两期 expansion-directions 均未覆盖** 的方向。每个方向附带具体代码位置、当前状态缺口和实现理由。

---

## 总览

| # | 方向 | 类型 | 影响 | 当前状态 | 前置 |
|---|------|------|------|---------|------|
| 1 | **分层存储与生命周期转换（STANDARD → STANDARD_IA → GLACIER → DEEP_ARCHIVE）** | 成本/架构 | 💰 存储成本降低 70-90% | `StorageClass` 字段存在但无实际转换逻辑；`ExpireAction` 仅支持删除 | 第一期 Lifecycle |
| 2 | **默认加密策略 + 无感 KMS 集成** | 安全/合规 | 🛡️ 全局加密准入 | SSE 仅 opt-in per-object；无桶级默认加密策略；KMS 配置复杂 | — |
| 3 | **S3 事件通知投递管线（S3 Event Notifications）** | 平台能力 | 🟠 生态集成刚需 | `NotificationRule` 存储结构已完成，但投递实现为空桩 | — |
| 4 | **Server Access Logs — 全量请求审计日志管线** | 合规/可观测 | 🛑 SOC2/PCI 准入 | `LoggingConfig` / `WriteAccessLog` 存在但实现为空 | 桶配置 |
| 5 | **企业级 Web Admin Dashboard + 实时运维面板** | 用户体验 | 🟠 运维效率刚需 | 当前 Web UI 仅限 4-tab 开发者工具；无管理/监控面板 | — |

---

## 1. 分层存储与生命周期转换

### 为什么需要它

当前代码库中 `repository.Object` 拥有 `StorageClass` 字段（`STANDARD`、`STANDARD_IA`、`GLACIER` 等），`BucketConfig` 支持 `ExpireAfterDays` + `ExpireAction`，但 **生命周期只有"到期删除"这一种动作**。没有：

- **无存储类转换**：对象不能从 `STANDARD` → `STANDARD_IA` → `GLACIER` 逐层降冷
- **无冷存储后端**：GLACIER/DEEP_ARCHIVE 需要不同的存储后端（S3 Glacier API、本地磁带/光盘），当前 `Storage` 接口不支持
- **无需检索流程**：GLACIER 对象需要 restore 操作才能读取，当前 `Get` 路径没有 restore 检查
- **无存储类分析**：没有工具帮助用户分析哪些对象适合降冷（最后访问时间、访问频率）

对企业而言，这直接意味着 **存储成本不可控**：热数据（STANDARD）的成本是冷数据（GLACIER）的 10-50 倍。对于以"存储"为核心价值的产品，缺少分层存储是严重的成本架构缺陷。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/repository/repository.go:Object.StorageClass` | 字段存在，枚举 `STANDARD` / `STANDARD_IA` / `GLACIER` | 字段永不转换 |
| `internal/repository/repository.go:BucketConfig.ExpireAfterDays` | 到期天数 | 只有 delete action，无 transition action |
| `internal/repository/repository.go:BucketConfig.ExpireAction` | `"soft_delete"` / `"hard_delete"` | 无 `"transition"` |
| `internal/reconcile/lifecycle.go` | 生命周期 worker | 仅处理过期删除，不处理转换 |
| `internal/storage/storage.go:Storage` | 统一 Storage 接口 | 无 Restore / ColdStorageClass 概念 |
| `internal/service/file_crud.go:Get` | 直接读取 | 不检查对象是否在冷存储中需要 restore |
| `internal/service/file_features.go:SetBucketLifecycle` | 设置生命周期 | 不支持 transition 类型 |
| `internal/api/rest/admin.go:PutBucketLifecycle` | 管理 API | 不支持 transition 配置 |

### 架构蓝图

```
┌─ Storage Class Model ─────────────────────────────────────────┐
│ const (                                                       │
│     StorageClassStandard    = "STANDARD"                       │
│     StorageClassStandardIA  = "STANDARD_IA"     // 低频        │
│     StorageClassOneZoneIA   = "STANDARD_ONEZONE_IA"           │
│     StorageClassGlacier     = "GLACIER"          // 归档        │
│     StorageClassDeepArchive = "DEEP_ARCHIVE"     // 深度归档    │
│ )                                                              │
│                                                                │
│ LifecycleRule (扩展 BucketConfig):                              │
│   ID            string                                         │
│   FilterPrefix  string                                         │
│   FilterTags    map[string]string                              │
│   Transitions   []Transition   // 按时间顺序: age → target     │
│   Expiration    *Expiration    // 最终删除                     │
│                                                                │
│ Transition {                                                   │
│   Days         int          // 对象创建后天数                   │
│   StorageClass string       // 目标存储类                      │
│ }                                                              │
│                                                                │
│ Expiration {                                                   │
│   Days        int                                              │
│   Action      string         // soft_delete | hard_delete      │
│ }                                                              │
└────────────────────────────────────────────────────────────────┘

┌─ Cold Storage Backend ─────────────────────────────────────────│
│ 新增 Storage 接口方法:                                         │
│   Restore(ctx, key, days) (RestoreInfo, error)                  │
│     → 发起冷存储检索请求，返回预计完成时间                        │
│   RestoreStatus(ctx, key) (string, error)                       │
│     → 查询 restore 状态: "pending" | "in-progress" | "done"    │
│                                                                │
│ GLACIER 存储后端实现:                                            │
│   storage/glacier.go → 包装 S3 Glacier API                     │
│   只写不读：Put 写入 Glacier，Get 前必须 Restore                  │
│   本地模拟: storage/local_glacier.go → 延迟加载 + 定时解冻       │
│                                                                │
│ 对象元数据扩展:                                                  │
│   RestoreStatus     string    // "" | "restoring" | "restored"  │
│   RestoreExpiresAt *time.Time // restore 到期时间               │
│   ArchiveStatus     string    // "archived" | "restoring" |     │
│                               // "restored"                     │
└────────────────────────────────────────────────────────────────┘

┌─ Lifecycle Transition Worker ──────────────────────────────────│
│ 在 reconcile/lifecycle.go 中扩展:                              │
│   Run(ctx) 循环:                                               │
│     1. 列出所有启用了 transition 规则的 bucket                    │
│     2. 对于每条规则:                                            │
│        a. 查询符合条件的对象 (created_at + storage_class 匹配)    │
│        b. 对符合 transition 条件的对象:                         │
│           - 更新 storage_class (DB)                              │
│           - 如果后端不支持冷存储 → 迁移对象到新后端                │
│           - 如果后端支持 → 标记为可转换                           │
│     3. 对于符合 expire 条件的对象: 继续现有删除逻辑                │
│                                                                │
│ 迁移表: lifecycle_rules (独立于 bucket_config)                   │
│   CREATE TABLE lifecycle_rules (                                │
│     id TEXT PRIMARY KEY,                                        │
│     tenant_id TEXT NOT NULL,                                    │
│     bucket TEXT NOT NULL,                                       │
│     filter_prefix TEXT DEFAULT '',                              │
│     rule_type TEXT NOT NULL,  -- "transition" | "expiration"   │
│     days INTEGER NOT NULL,                                      │
│     target_class TEXT DEFAULT '',  -- for transition            │
│     expire_action TEXT DEFAULT 'soft_delete',  -- for expiration│
│     created_at TEXT NOT NULL                                     │
│   );                                                            │
└────────────────────────────────────────────────────────────────┘

┌─ Restore API Surface ──────────────────────────────────────────│
│ POST /v1/files/{key}/restore?days=7                             │
│   → 从 GLACIER/DEEP_ARCHIVE 恢复到 STANDARD 临时副本             │
│   → 返回估计完成时间 + restore id                                │
│                                                                │
│ GET  /v1/files/{key}/restore-status                             │
│   → 返回 { status, restore_expires_at }                         │
│                                                                │
│ GET  /v1/files/{key} (on archived object)                       │
│   → 返回 403 + "object is archived; call restore first"         │
│   → 如果 restore 有效 → 正常返回                                 │
│                                                                │
│ S3-compat 路由:                                                 │
│   POST /s3/{bucket}/{key}?restore { days: 7 }                  │
│   GET  /s3/{bucket}/{key} (归档对象返回 403 ArchiveAccess)       │
└────────────────────────────────────────────────────────────────┘

┌─ 存储成本分析工具（可选 v2）───────────────────────────────────│
│ GET /v1/admin/storage-analyzer/{tenant}                         │
│   返回: {                                                        │
│     total_size: N,                                              │
│     by_storage_class: {STANDARD: N, STANDARD_IA: M, ...},       │
│     savings_estimate: {                                          │
│       move_ia_to_glacier: {size: N, monthly_saving: $X},        │
│       move_cold_to_deep: ...                                    │
│     },                                                           │
│     recommendations: [                                          │
│       "prefix 'logs/2024/' → GLACIER ($YY/month saving)"        │
│     ]                                                           │
│   }                                                              │
└────────────────────────────────────────────────────────────────┘

```

**边界情况：**
- Glacier 对象 restore 有临时副本有效期，过期后必须重新 restore
- 生命周期规则不能同时 transition 和 expire 同一批对象（冲突解决：transition 优先，expire 在 transition 后进行）
- 已经处于 GLACIER 的对象不能被 transition 到 STANDARD_IA（只能升冷到 STANDARD 或 expire）
- 桶级默认存储类配置（`STORAGE_DEFAULT_CLASS`）已存在但仅应用于新 PUT，不应用于现有对象
- Transition 对象时保留版本 ID：只有当前版本转换，历史版本不变
- **回滚风险**：transition 操作的幂等性和可逆性需明确——STANDARD→GLACIER 可逆（restore），但耗时；GLACIER→DEEP_ARCHIVE 不可逆

**复杂度:** L（冷存储后端 + 转换引擎） · **用户影响:** ★★★★★（成本核心） · **代码变更:** ~1500 行新代码 + ~500 行修改

---

## 2. 默认加密策略 + 无感 KMS 集成

### 为什么需要它

当前加密实现（`internal/storage/encrypt.go`）是 **opt-in 模式**：只有单个 `STORAGE_LOCAL_SSE_KEY` 或 keyfile/KMS URL 配置，加密作用于整个 local 后端。缺少：

- **无桶级默认加密策略**：S3 支持在桶上设置 `x-amz-default-encryption`，使得所有 PUT 请求自动加密，无需客户端指定。当前实现中，用户必须显式设置 `STORAGE_LOCAL_SSE_KEY` 才能启用加密，且作用于全局。
- **无加密策略 API**：用户无法通过 API 查看/设置桶的默认加密配置
- **无 KMS 密钥轮换策略**：`rewrap.go` 支持启动时重新包装，但没有**定期自动轮换**机制
- **无密钥别名管理**：KMS 密钥 ID 是硬配置，不能通过别名/标签引用密钥
- **无缝用户感知**：加密对象在 GET 时自动解密（已有），但用户在 PUT/HEAD/GET 时无法感知或确认加密状态

这对以下场景是准入级需求：
- **金融/医疗**：要求所有静态数据必须加密（即使客户端忘记设置）
- **S3 兼容性**：AWS S3 的 `x-amz-server-side-encryption` 头在 PUT 时可选但不设置时可能失败（如果桶有默认加密）
- **密钥合规**：PCI-DSS、SOC2 要求定期轮换加密密钥

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/storage/encrypt.go` | AES-256-GCM envelope 加密 | 无桶级策略、无感知 |
| `internal/storage/rewrap.go` | 启动时单次重包装 | 无定期自动轮换 |
| `internal/storage/kms.go` | HTTP KMS client | 无密钥别名、无轮换策略 |
| `internal/storage/local_write.go` | 写入时条件加密 | 无桶级默认策略检查 |
| `internal/repository/repository.go:BucketConfig` | 桶配置结构体 | 无 `DefaultEncryption` 字段 |
| `internal/repository/sql_buckets.go` | 桶配置持久化 | 无双驱动迁移 |
| `internal/service/file_crud.go:Put` | PUT 流程 | 不读取桶级加密策略 |
| `internal/api/rest/router.go` | REST 路由 | 无加密策略管理端点 |
| `internal/api/rest/handler.go` | Handler | 不返回 `x-amz-server-side-encryption` 响应头 |
| `internal/api/s3compat/handler.go` | S3 handler | PUT 时不检查 `x-amz-server-side-encryption` 请求头 |
| `internal/config/config_storage.go` | 存储配置 | 只有全局 SSEKey，无桶级默认 |

### 架构蓝图

```
┌─ Default Encryption Policy Model ──────────────────────────────│
│ BucketConfig 扩展:                                              │
│   DefaultEncryption *EncryptionConfig                           │
│                                                                │
│ type EncryptionConfig struct {                                 │
│     SSEAlgorithm string // "AES256" | "aws:kms" | "SM4"        │
│     KMSKeyID     string // KMS 密钥 ID（alias/arn/id）           │
│     KMSContext   map[string]string  // 加密上下文               │
│     AppliedAt    string // "put" | "bucket-policy"              │
│ }                                                              │
│                                                                │
│ 优先级（从低到高）:                                              │
│   1. 全局默认（STORAGE_LOCAL_SSE_KEY）→ 兜底                      │
│   2. 桶级默认策略（PUT API 中检查） → 桶配置                      │
│   3. 请求级头（x-amz-server-side-encryption）→ 最高优先级        │
│   4. 如果请求指定 SSE-C（客户提供密钥）→ 覆盖所有                 │
└────────────────────────────────────────────────────────────────┘

┌─ API Surface ──────────────────────────────────────────────────│
│ PUT /v1/buckets/{bucket}/encryption                             │
│   {                                                             │
│     "sse_algorithm": "AES256",                                  │
│     "kms_key_id": ""  // AES256 不需要                           │
│   }                                                             │
│   → 设置桶的默认加密策略                                          │
│   → 响应: {"sse_algorithm": "AES256"}                           │
│                                                                │
│ GET /v1/buckets/{bucket}/encryption                             │
│   → 返回当前桶的加密策略                                          │
│                                                                │
│ DELETE /v1/buckets/{bucket}/encryption                          │
│   → 删除桶级默认加密策略，回到全局默认                              │
│                                                                │
│ S3-compat 路由:                                                 │
│   PUT /s3/{bucket}?encryption                                   │
│     <ServerSideEncryptionConfiguration>                         │
│       <Rule><ApplyServerSideEncryptionByDefault>                │
│         <SSEAlgorithm>AES256</SSEAlgorithm>                     │
│       </ApplyServerSideEncryptionByDefault></Rule>              │
│     </ServerSideEncryptionConfiguration>                        │
│                                                                │
│   GET /s3/{bucket}?encryption                                   │
│     → 返回上述 XML                                              │
│                                                                │
│   PUT /s3/{bucket}/{key} (with x-amz-server-side-encryption)    │
│     → 存储算法 + 在响应头中回显                                   │
└────────────────────────────────────────────────────────────────┘

┌─ KMS Auto-Rotation ────────────────────────────────────────────│
│ 当前: RewrapStale 扫描旧密钥版本的重包装（启动时）。                │
│ 扩展:                                                           │
│   KMS 定期轮换 worker（运行在 reconcile 循环中）:                 │
│     interval := cfg.Storage.SSEKeyRotationHours                  │
│     1. 列出所有 SSE 加密的对象（metadata._aero_sse_key_id != ""）│
│     2. 检查当前密钥 ID 是否是最新 primary key                     │
│     3. 如果不是 → 重新包装（复用 rewrapper.RewrapStale 逻辑）      │
│     4. 进度指标: sse_key_rotate_total                            │
│                                                                │
│ 配置:                                                            │
│   STORAGE_SSE_KEY_ROTATION_HOURS=24  # 0 = 禁用自动轮换           │
│   STORAGE_SSE_PRIMARY_KEY_ID="alias/aero-vault-primary"          │
└────────────────────────────────────────────────────────────────┘

┌─ 客户端感知 ───────────────────────────────────────────────────│
│ PUT 响应头:                                                     │
│   x-amz-server-side-encryption: AES256                          │
│   x-amz-server-side-encryption-aws-kms-key-id: ... (仅 KMS)      │
│                                                                │
│ GET/HEAD 响应头:                                                │
│   x-amz-server-side-encryption: AES256                          │
│   (如果对象是 SSE 加密的)                                         │
│                                                                │
│ 对象元数据扩展:                                                  │
│   _aero_sse_algorithm: "AES256" | "aws:kms"                     │
│   _aero_sse_key_id: "..."  # KMS 密钥 ID                        │
│   _aero_sse_key_version: "v2"  # 当前 envelope 密钥版本          │
└────────────────────────────────────────────────────────────────┘

┌─ 迁移需求 ─────────────────────────────────────────────────────│
│ 双驱动迁移 0025:                                                 │
│   ALTER TABLE buckets ADD COLUMN default_encryption TEXT;        │
│   # JSON: {"sse_algorithm":"AES256","kms_key_id":""}             │
└────────────────────────────────────────────────────────────────┘
```

**边界情况：**
- SSE-C（客户提供密钥）和 SSE-KMS（服务端 KMS）的互斥：如果一个对象是 SSE-C 加密的，桶级默认 SSE-KMS 策略不应覆盖
- 桶级策略变更**不影响**现有对象——只影响新的 PUT 请求
- KMS key 被删除后的灾难恢复：当前对象的所有 envelope 使用 `key_id` 加密，如果 key 被删除则对象变为不可读——需要在 `rewrap.go` 中增加 key 有效性检测
- 跨区域 KMS：如果 KMS 密钥在不同区域，对象复制时需要特殊处理（`replication/replication.go` 需传递加密上下文）
- 桶级加密策略和生命周期转换（方向 #1）的交互：对象从 STANDARD 转换到 GLACIER 时，加密策略必须保持一致
- 性能影响：每次 PUT 检查桶级策略有额外开销，需缓存策略到内存（复用 `auth/key_cache.go` 模式）

**复杂度:** M · **用户影响:** ★★★★☆（合规安全） · **代码变更:** ~1000 行新代码 + ~400 行修改

---

## 3. S3 事件通知投递管线（S3 Event Notifications）

### 为什么需要它

代码库中 `BucketConfig.NotificationRules` 结构体和仓库接口 `SetBucketNotifications` / `GetBucketNotifications` / `DeleteBucketNotifications` **已经完整实现**，但实际的**事件投递引擎不存在**。

当前状态：
- `repository/repository.go` 定义了 `NotificationRule` 结构体（`Events`、`FilterKey`、`QueueARN` 等字段）
- `service/file_features.go` 实现了 `SetBucketNotifications` / `GetBucketNotifications` / `DeleteBucketNotifications`
- `internal/repository/sql_buckets.go` 实现了这些方法的 SQL 持久化
- `internal/repository/migrations/postgres/0024_bucket_notifications.up.sql` 已经创建了 `bucket_notifications` 表
- **但没有任何代码实际读取 `NotificationRule` 并投递事件到配置的 QueueARN 端点**

这意味着：
- 用户可以通过 API 配置通知规则（返回 200 OK）
- 规则存储在数据库中
- 但事件永远不会被投递
- 这是一个**存根功能**——看起来实现了但实际不工作

S3 Event Notifications 是企业集成的核心需求。典型场景：
- 对象创建时触发 Lambda 函数处理
- 对象删除时通过 SQS 通知下游系统
- 通过 SNS 发送事件到运维团队

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/repository/repository.go:NotificationRule` | 结构体完整 | 有字段但无投递者使用 |
| `internal/repository/repository.go:SetBucketNotifications` | 持久化存在 | 只存不投 |
| `internal/repository/repository.go:GetBucketNotifications` | 查询存在 | 无人读取来投递 |
| `internal/repository/sql_buckets.go` | SQL 实现 | 读写正确但无消费者 |
| `internal/service/file_features.go:SetBucketNotifications` | 服务层方法 | 调用 repo 存储，不触发投递 |
| `internal/service/file_features.go:GetBucketNotifications` | 服务层方法 | 仅返回存储的规则 |
| `internal/events/bus.go` | 事件总线 | 只支持 webhook（全局 URL）和内部 worker |
| `internal/webhook/webhook.go` | Webhook worker | 全局 webhook，不支持按桶规则过滤投递 |
| `internal/migrations/postgres/0024_bucket_notifications.up.sql` | 迁移已存在 | 表已创建但无消费者 |

### 架构蓝图

```
┌─ Notification Engine ──────────────────────────────────────────│
│ 新包: internal/notifications/                                   │
│                                                                │
│ type Engine struct {                                           │
│     repo    repository.Repository                              │
│     bus     *events.Bus                                        │
│     clients map[string]*http.Client  // per-endpoint pool       │
│     logger  *slog.Logger                                        │
│ }                                                               │
│                                                                │
│ 启动: 监听 bus.Subscribe()                                      │
│   对每个 event:                                                  │
│     1. 查询桶的所有 NotificationRule                            │
│     2. 对每条规则:                                               │
│        a. 检查 EventTypes 匹配                                   │
│        b. 检查 FilterKey 前缀匹配（如果配置了）                  │
│        c. 构造 S3-风格通知消息 JSON                              │
│        d. 根据 QueueARN 选择投递方式                              │
│                                                                │
│ 投递方式:                                                       │
│   - HTTP(S) POST（通用 webhook） → 复用 events/webhook.go 逻辑   │
│   - SQS（AWS 简单队列服务）→ 通过 AWS SDK                         │
│   - SNS（AWS 通知服务）→ 通过 AWS SDK                             │
│   - Lambda（AWS 函数触发）→ 通过 AWS SDK                         │
│                                                                │
│ 本实现顺序:                                                      │
│   v1: HTTP(S) POST webhook（复用现有 retry infrastructure）      │
│   v2: SQS（需要 AWS SDK 依赖）                                    │
│   v3: SNS / Lambda                                                │
└────────────────────────────────────────────────────────────────┘

┌─ S3-Event Compatible JSON Payload ─────────────────────────────│
│ {                                                               │
│   "Records": [{                                                 │
│     "eventVersion": "2.1",                                      │
│     "eventSource": "aero-vault:s3",                             │
│     "awsRegion": "us-east-1",                                   │
│     "eventName": "ObjectCreated:Put",                           │
│     "s3": {                                                     │
│       "s3SchemaVersion": "1.0",                                 │
│       "bucket": {                                               │
│         "name": "default",                                      │
│         "arn": "arn:aws:s3:::default"                           │
│       },                                                        │
│       "object": {                                               │
│         "key": "path/to/file.pdf",                              │
│         "size": 12345,                                          │
│         "eTag": "abc123",                                       │
│         "versionId": "v_xxxx"                                   │
│       }                                                         │
│     },                                                          │
│     "requestId": "uuid-xxx",                                    │
│     "eventTime": "2026-07-10T12:00:00Z"                         │
│   }]                                                             │
│ }                                                                │
└────────────────────────────────────────────────────────────────┘

┌─ QueueARN 解析 ───────────────────────────────────────────────│
│ 支持格式:                                                       │
│   # HTTP/S webhook                                              │
│   arn:aws:webhook:::https://hooks.example.com/events           │
│                                                                │
│   # SQS 队列                                                    │
│   arn:aws:sqs:us-east-1:123456789012:my-queue                  │
│                                                                │
│   # SNS 主题                                                    │
│   arn:aws:sns:us-east-1:123456789012:my-topic                  │
│                                                                │
│   # Lambda 函数                                                 │
│   arn:aws:lambda:us-east-1:123456789012:function:my-function   │
│                                                                │
│ 解析器:                                                         │
│   type ARN struct {                                             │
│     Partition string  // "aws"                                  │
│     Service   string  // "sqs" | "sns" | "lambda" | "webhook"  │
│     Region    string                                            │
│     Account   string                                            │
│     Resource  string                                            │
│   }                                                              │
└────────────────────────────────────────────────────────────────┘

┌─ Filters & Prefix Matching ────────────────────────────────────│
│ 增强现有 NotificationRule.FilterKey:                             │
│   "" → 匹配所有事件                                              │
│   "images/" → 只匹配前缀匹配的对象的事件                           │
│   "*.jpg" → 后缀匹配（或同时支持 prefix + suffix）                │
│                                                                │
│ 规则评估:                                                        │
│   func matchFilter(rule NotificationRule, event Event) bool {    │
│     // 1. Event type 匹配                                       │
│     if !contains(rule.Events, eventName(event)) { return false } │
│     // 2. Prefix/suffix 过滤                                    │
│     if rule.FilterKey != "" && !prefixMatch(event.Key, rule.FilterKey) { return false } │
│     return true                                                  │
│   }                                                              │
└────────────────────────────────────────────────────────────────┘

┌─ 与现有 Webhook 的关系 ────────────────────────────────────────│
│ 全局 Webhook（EVENTS_WEBHOOK_URL）: 所有事件的统一投递地址        │
│ 桶级别 NotificationRules: 按桶/前缀过滤的精细投递                  │
│ 两者共存:                                                        │
│   - 全局 webhook 由 events/webhook.go 处理（已实现）               │
│   - 桶级通知由新 notifications.Engine 处理（本方向）               │
│   - 事件不会重复投递：各自独立评估过滤器                            │
└────────────────────────────────────────────────────────────────┘

┌─ Metrics ──────────────────────────────────────────────────────│
│ notification_matched_total{rule_id, event_type}                 │
│ notification_delivered_total{target, status}                   │
│ notification_latency_ms{target}                                │
│ notification_retry_total{rule_id}                              │
└────────────────────────────────────────────────────────────────┘
```

**边界情况：**
- 事件投递**去重**：同一事件在同一秒内多次触发的幂等性（使用事件 `ID` + `CreatedAt` 做 dedup）
- 投递失败重试：复用 `webhook_failures` 表（指数退避）
- 批量事件合并：对于大量创建事件（如批量上传），支持合并到一个通知中（最大 100 条/通知）
- 投递速率限制：每个目标端点独立限流（10 rps），防止下游过载
- 非阻塞：通知投递失败不应影响对象操作（与现有 EventBus 设计一致）
- 通知规则的动态更新：添加/删除规则后，Engine 应能热加载（无需重启）

**复杂度:** L-M · **用户影响:** ★★★★☆（生态集成） · **代码变更:** ~1200 行新代码 + ~100 行修改

---

## 4. Server Access Logs — 全量请求审计日志管线

### 为什么需要它

当前代码库中 `BucketConfig.LoggingConfig` 结构体和仓库接口 `SetBucketLogging` / `GetBucketLogging` / `DeleteBucketLogging` / `WriteAccessLog` **已经完整实现**，与方向 #3 类似——结构定义和持久化存在，但**实际日志写入管线不存在**。

当前状态：
- `repository/repository.go` 定义了 `LoggingConfig`（`Enabled`、`Target`、`Prefix`）
- `repository/repository.go` 定义了 `WriteAccessLog(ctx, tenant, sourceBucket, method, key, status, latencyMs, userAgent)`
- `service/file_features.go` 实现了 `GetBucketLogging` / `SetBucketLogging` / `DeleteBucketLogging`
- `middleware/middleware.go:AccessLog` 将每个 HTTP 请求记录到 **slog**（服务器日志），但**不写入桶访问日志**
- **没有任何地方调用 `WriteAccessLog`**

这意味着：
- 缺少 S3 Server Access Logs 等价物
- 用户无法审计谁在何时访问了哪些对象
- SOC2/PCI-DSS 合规要求（记录所有数据访问）无法满足
- 运维无法分析请求模式（带宽分布、热点、错误率）

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/repository/repository.go:LoggingConfig` | 结构体完整 | 有字段但开启后不产生日志 |
| `internal/repository/repository.go:WriteAccessLog` | 接口方法定义 | 实现为空（SQLite/Postgres 中跳过） |
| `internal/repository/sql_buckets.go` | 桶配置持久化 | `LoggingTarget`/`LoggingPrefix` 存储正确但不生效 |
| `internal/service/file_features.go:SetBucketLogging` | 服务层方法 | 仅存储配置 |
| `internal/middleware/middleware.go:AccessLog` | 服务器访问日志 | 只打到 slog，不写入桶日志 |
| `internal/api/rest/handler.go:classify` | 所有错误路径 | 不记录访问日志 |
| `internal/repository/migrations/postgres/0023_bucket_logging.up.sql` | 迁移存在 | 表 `bucket_logging` 已创建但无消费者 |

### 架构蓝图

```
┌─ Access Log Pipeline ──────────────────────────────────────────│
│ 数据流:                                                         │
│   HTTP Handler → middleware.AccessLog（slog）                    │
│   HTTP Handler → accessLogMiddleware（新）→ WriteAccessLog       │
│                                                                │
│ 新 middleware:                                                  │
│   func AccessLogWriter(repo repository.Repository) func(http.Handler) http.Handler│
│   在每个请求完成后调用:                                           │
│     repo.WriteAccessLog(ctx, tenant, bucket, method,            │
│       key, statusCode, latencyMs, userAgent)                    │
│                                                                │
│ 写入目标（可配置）:                                               │
│   1. 写入目标桶（日志对象本身存储在 aero-vault 中）               │
│      每个 bucket 可以有单独的 logging target bucket              │
│      格式: {prefix}{YYYY-MM-DD-HH-MM}/_{random-uuid}.log       │
│   2. 写入 DB 表（access_logs）——适合分析查询                      │
│   3. 写入外部系统（S3、syslog、Kafka）——可选                      │
│                                                                │
│ 推荐实现: 写入目标桶（复用现有 storage API）                       │
│   优点: 不增加 DB 负担、利用存储层持久性、与 S3 模型一致           │
└────────────────────────────────────────────────────────────────┘

┌─ Access Log Entry Format ──────────────────────────────────────│
│ 兼容 S3 Server Access Log 格式（Tab-separated）:                 │
│                                                                │
│ # Bucket  Owner  Time  RemoteIP  Requester  Operation           │
│ # Key  Request-URI  HTTPStatus  ErrorCode  BytesSent            │
│ # ObjectSize  TotalTime  TurnAroundTime  Referer  UserAgent     │
│ # VersionID  HostID  SigV  CipherSuite  AuthType                │
│ # Endpoint  TLSVersion                                          │
│                                                                │
│ 简化版（v1，JSON lines，每行一个请求）:                         │
│ {                                                               │
│   "bucket": "default",                                          │
│   "key": "reports/q3.pdf",                                      │
│   "method": "GET",                                              │
│   "status": 200,                                                │
│   "bytes": 12345,                                               │
│   "latency_ms": 42,                                             │
│   "remote_ip": "10.0.0.1",                                      │
│   "tenant": "acme-corp",                                        │
│   "request_id": "abc-123",                                      │
│   "user_agent": "aws-sdk-go/2.0.0",                             │
│   "timestamp": "2026-07-10T12:00:00.000Z",                      │
│   "operation": "REST.GET.OBJECT",                                │
│   "referer": "",                                                │
│   "version_id": "",                                             │
│   "request_uri": "/v1/files/reports/q3.pdf"                     │
│ }                                                                │
└────────────────────────────────────────────────────────────────┘

┌─ Log Delivery ─────────────────────────────────────────────────│
│ 新的 reconcile worker 或独立 worker:                             │
│                                                                │
│ accessLogWorker:                                                │
│   1. 批次收集 access log entries（内存 buffer 或 DB 临时表）      │
│   2. 按 {target_bucket}/{prefix}/{YYYY-MM-DD-HH}/ 分区          │
│   3. 攒批写入（每分钟一次或每 10000 条一次）                      │
│   4. 写入后清理临时存储（如果使用 DB）                             │
│                                                                │
│ 配置:                                                            │
│   ACCESS_LOG_BUFFER_SIZE=10000     # 攒批大小                    │
│   ACCESS_LOG_FLUSH_INTERVAL=60     # 刷新间隔（秒）              │
│   ACCESS_LOG_TARGET=sqlite         # "sqlite" | "postgres" |     │
│                                    # "storage-bucket"           │
└────────────────────────────────────────────────────────────────┘

┌─ Access Log Query API（可选）────────────────────────────────────│
│ GET /v1/admin/access-logs?tenant=acme&bucket=default            │
│   &from=2026-07-01&to=2026-07-10                                │
│   &prefix=reports/&status=4xx&limit=100                         │
│   → 返回筛选后的访问日志条目                                      │
│                                                                │
│ GET /v1/admin/access-logs/stats                                  │
│   → {                                                            │
│       "total_requests": 1234567,                                 │
│       "bytes_sent": 99999999,                                    │
│       "error_rate_pct": 0.12,                                    │
│       "top_paths": ["/v1/files/...", "/s3/...", ...],            │
│       "slowest_ops": [...]                                      │
│     }                                                            │
└────────────────────────────────────────────────────────────────┘

┌─ 日志管理 ─────────────────────────────────────────────────────│
│ 生命周期绑定（复用方向 #1 的生命周期规则）:                        │
│   访问日志桶也可设置生命周期策略：自动删除 90 天前的日志            │
│   配置: ACCESS_LOG_RETENTION_DAYS=90                             │
│                                                                │
│ 日志安全:                                                        │
│   日志中不应包含请求 body                                       │
│   如果启用了 SSE，日志条目中加密字段应标记但隐藏密钥               │
│   日志桶本身应独立加密                                            │
└────────────────────────────────────────────────────────────────┘
```

**边界情况：**
- 日志写入不得阻塞请求响应（异步写入）：使用带缓冲的 channel + 后台 worker
- 如果日志桶不可写（如存储故障），降级为丢弃日志而非阻塞主请求——记录一条警告日志即可
- 日志条目可能很大（长 key、长 UA）→ 每个字段应有长度限制（key 1024、UA 256）
- 日志本身的存储计费：不应计入租户的配额（纳入系统开销）
- 大规模部署（100K rps）下日志量极大——必须支持 target=storage-bucket 方式，避免压垮 DB
- 日志查询 API 应考虑性能（分页、索引、时间范围限制）

**复杂度:** M · **用户影响:** ★★★★☆（合规运维） · **代码变更:** ~800 行新代码 + ~400 行修改

---

## 5. 企业级 Web Admin Dashboard + 实时运维面板

### 为什么需要它

当前 Web UI（`internal/webui/static/index.html`）是一个 4-tab 开发者工具：
1. **Semantic Search** — 搜索/向量/混合检索界面
2. **Object Detail** — 选中对象的元数据显示
3. **Lineage** — 对象血缘查看
4. **Chat** — RAG 聊天界面

缺少的是企业管理员/运维人员日常工作中**最需要的管理面板**：

- **无对象浏览器**：不能按列排序、不能多选操作、不能按标签筛选
- **无用户/租户管理**：无法在 UI 中管理租户、API 密钥、配额
- **无实时监控**：看不到当前 QPS、延迟、错误率
- **无存储概览**：看不到总用量、存储类分布、桶大小排名
- **无作业监控**：看不到后台作业状态、失败率
- **无法批量操作**：不能在 UI 中批量删除、批量打标签
- **无审计日志查看**：不能在 UI 中查看安全审计日志
- **无配置管理**：不能在 UI 中查看/更改运行时配置

把所有这些功能藏在 REST API 后面，要求运维人员用 curl/CLI 操作，是产品化的主要障碍。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/webui/web.go` | 嵌入 static/ 文件系统 | 只有 4-tab 开发者工具 |
| `internal/webui/static/index.html` | ~280 行 vanilla JS | 功能极度有限 |
| `internal/api/rest/admin.go` | 完整的 admin API（租户/密钥/配额/作业/审计） | 无 UI 消费 |
| `internal/api/rest/handler.go` | 对象管理 API | 无 UI 消费 |
| `internal/telemetry/prometheus.go` | Prometheus /metrics | 无 in-UI 可视化 |
| `internal/jobs/jobs.go` | 作业池 | 无 UI 监控 |
| `internal/repository/repository.go:ListAudit` | 审计日志查询 | 无 UI |
| `internal/repository/repository.go:ListJobs` | 作业列表 | 无 UI |

### 架构蓝图

```
┌─ UI 框架选型 ──────────────────────────────────────────────────│
│ 当前方案: 纯静态 embed + vanilla JS（零外部依赖，完美）             │
│ 延续方案: 保持 embed + vanilla JS 风格                            │
│          不引入 React/Vue 等框架（不增加构建依赖）                  │
│          使用 lit-html 或 htmx 风格的轻量模板（可选）              │
│          或继续纯手工 DOM 操作（当前做法）                         │
│                                                                │
│ 推荐: 继续 vanilla JS + 模块化拆分（多个 HTML 页面或 SPA 路由）    │
│ 拆分方案:                                                        │
│   static/index.html → 入口 + 导航栏                               │
│   static/dashboard.html 或 #dashboard → 仪表盘面板                │
│   static/objects.html    或 #objects → 对象浏览器                  │
│   static/admin.html      或 #admin → 管理面板                     │
│   static/audit.html      或 #audit → 审计日志                     │
│   static/jobs.html       或 #jobs → 作业监控                      │
└────────────────────────────────────────────────────────────────┘

┌─ 仪表盘面板 (#dashboard) ──────────────────────────────────────│
│ ┌───────────────┬───────────────┬───────────────┬───────────────┐│
│ │  存储总用量     │  对象总数      │  活跃租户数    │  今日 AI 调用    ││
│ │  1.2 TB / 5 TB│  3,456,789    │  12           │  1,234          ││
│ └───────────────┴───────────────┴───────────────┴───────────────┘│
│ ┌───────────────────────────────────────────────────────────────┐│
│ │ 实时 QPS 折线图（过去 1 小时）                                  ││
│ │   ┌──────┬──────┬──────┬──────┬──────┬──────┬──────┐         ││
│ │   │ ██   │ ███  │ ██   │ ████ │ ██   │ ███  │ ██   │         ││
│ │   │ 120  │ 145  │ 110  │ 200  │ 130  │ 155  │ 100  │         ││
│ │   └──────┴──────┴──────┴──────┴──────┴──────┴──────┘         ││
│ └───────────────────────────────────────────────────────────────┘│
│ ┌───────────────┬───────────────┬───────────────────────────────┐│
│ │ 存储类分布      │  Top 5 桶      │  最近错误（10 条）             ││
│ │ STANDARD: 60% │  default: 500GB│  404 GET /v1/files/x ....    ││
│ │ STANDARD_IA:  │  logs: 300GB   │  500 PUT /s3/bucket/k ...   ││
│ │ 25%           │  backups: 200GB│  ...                        ││
│ │ GLACIER: 15%  │  ...          │                              ││
│ └───────────────┴───────────────┴───────────────────────────────┘│
└────────────────────────────────────────────────────────────────┘

┌─ 对象浏览器 (#objects) ─────────────────────────────────────────│
│ 增强当前文件列表:                                                │
│   ├── 表格模式（列：Key | Size | Type | Modified | Tags | ACL）  │
│   ├── 可排序（点击列头）                                         │
│   ├── 可多选（checkbox）                                         │
│   ├── 批量操作：删除、打标签、改 ACL、下载                        │
│   ├── 筛选器：prefix + tag key=value + min_size + max_size       │
│   ├── 拖拽上传反馈（当前已支持）                                  │
│   └── 版本浏览：切换版本查看历史                                  │
└────────────────────────────────────────────────────────────────┘

┌─ 管理面板 (#admin) ─────────────────────────────────────────────│
│  ├── 租户管理：列出/创建/启用/禁用/删除租户                       │
│  ├── 配额管理：设置/修改租户的 max_bytes + max_objects           │
│  ├── AI 预算：查看/设置每日 AI 预算                               │
│  ├── API 密钥：列出/创建/撤销密钥                                │
│  ├── 桶配置：查看/修改桶的 versioning/lock/lifecycle/CORS/logging│
│  ├── 存储类分布：按桶/前缀展示存储类分布                          │
│  └── 运行时配置：查看非敏感配置项                                 │
│                                                                │
│ 注意: 所有管理操作调用已有 REST API                               │
│   /v1/admin/tenants, /v1/admin/keys, /v1/admin/...              │
└────────────────────────────────────────────────────────────────┘

┌─ 审计日志查看 (#audit) ─────────────────────────────────────────│
│  ├── 表格：Time | Actor | Action | Target | Detail              │
│  ├── 筛选：按 actor/action/target/time-range                     │
│  ├── 导出：CSV 下载                                              │
│  └── 详情：点击查看完整 JSON                                     │
│                                                                │
│ API: GET /v1/admin/audit?limit=100（已有）                       │
└────────────────────────────────────────────────────────────────┘

┌─ 作业监控 (#jobs) ──────────────────────────────────────────────│
│  ├── 作业队列状态：pending | running | failed | completed         │
│  ├── 实时计数 + 趋势                                            │
│  ├── 失败作业列表 + 重试按钮                                     │
│  ├── 批量作业进度（配合第一期方向 #1）                            │
│  └── 每个 worker 的吞吐量                                       │
│                                                                │
│ API: GET /v1/admin/jobs?status=failed&limit=50（已有）           │
│      POST /v1/admin/jobs/{id}/retry（已有）                       │
└────────────────────────────────────────────────────────────────┘

┌─ 集成方式 ──────────────────────────────────────────────────────│
│ 保持现有 embed 方式:                                             │
│   //go:embed static/dashboard.html static/admin.html ...        │
│   static/js/*.js → 模块化 JS                                    │
│   static/css/*.css → 样式分离                                    │
│                                                                │
│ 路由（当前 web.go 可扩展）:                                      │
│   /ui/                     → 入口 + 导航                        │
│   /ui/dashboard            → 仪表盘                              │
│   /ui/objects              → 对象浏览器                          │
│   /ui/admin                → 管理面板（需要 admin scope）          │
│   /ui/audit                → 审计日志                            │
│   /ui/jobs                 → 作业监控                            │
└────────────────────────────────────────────────────────────────┘

┌─ 实时数据更新 ──────────────────────────────────────────────────│
│ 使用 EventSource（SSE）从 /v1/events/stream 获取实时事件：        │
│   新对象创建 → 更新计数                                         │
│   新作业状态变更 → 更新作业面板                                   │
│   错误事件 → 实时错误流                                          │
│                                                                │
│ 周期性轮询（作为 fallback）：                                     │
│   GET /v1/admin/stats （新 API, 聚合仪表盘数据）                   │
│   → 缓存 10s，减少请求频率                                       │
└────────────────────────────────────────────────────────────────┘
```

**边界情况：**
- Admin UI 需要认证（admin scope），但 `/ui` 目前可能不受 auth 保护——要么不加 auth 墙（危险），要么添加 auth 检查
- 批量操作在 UI 中的确认机制：删除前确认弹窗，可撤销操作提供软删除选项
- 性能数据轮询频率：不能高于 10 秒/次，避免压垮 API
- 大租户（百万对象）的页面加载：列表必须分页（当前 1000 限制），搜索建议自动补全
- 静态资源缓存：JS/CSS 内容哈希 + 强缓存头（304 Not Modified）
- 移动端适配：关键操作（列表/上传/搜索）在移动设备上可用

**复杂度:** M-L（多页面 + JS 逻辑） · **用户影响:** ★★★★★（日常运维） · **代码变更:** ~2000 行 HTML/JS/CSS + ~200 行 Go（新 API）

---

## 附录：被排除但值得关注的较重要改进

| 问题 | 位置 | 说明 | 建议优先级 |
|------|------|------|-----------|
| **桶删除不检查对象存在** | `internal/service/file_features.go:DeleteBucket` | 调用 `repo.DeleteBucket` 直接删除桶行，不检查是否有对象残留 | 🔴 高：会导致对象孤立在存储中 |
| **批量操作缺乏事务/SAGA** | `internal/service/file_features.go:BatchDelete` | 批量操作逐对象执行，中途失败不清理已完成的 | 🟠 中：幂等性问题，但可接受 |
| **大目录删除内存溢出** | `internal/api/rest/handler.go:DeleteFolder` | `DeleteFolder` 将所有 key 加载到 memory 中 | 🔴 高：大目录会 OOM |
| **EventBus 无持久化重试** | `internal/events/bus.go:broadcast` | 事件投递给 subscriber 时 channel full → 丢弃 | 🟠 中：DB 有副本，但非关键事件可能丢失 |
| **缺少健康的 Storage 健康检查** | `cmd/server/main.go:readyzHandler` | 只检查 `Stat("@healthz/probe")`，不探测写入路径 | 🟠 中：无法区分只读/降级 |
| **No SSE 链路追踪** | `internal/storage/encrypt.go` | 解密失败无结构化错误上下文 | 🟡 低：增加 debug 可观测性 |
| **CLI 不支持管理操作** | `internal/cli/*.go` | CLI 只有对象 CRUD + search + snapshot | 🟡 低：管理操作只能用 API |
| **Web UI 无拖拽上传文件夹支持** | `internal/webui/static/index.html` | 只支持单文件上传 | 🟠 中：用户体验改进 |
| **预签名 URL 无多协议支持** | `internal/storage/sign.go` | 只有 local 后端的 HMAC 签名 | 🟠 中：S3/OSS/COS 预签名未实现 |
| **README/文档落后于代码** | `README.md` | 缺少 S3 Event Notifications / Access Log / 桶策略等的文档 | 🟠 中：新用户上手困难 |

---

> **总结：** 以上 5 个方向均未被前两期 expansion-directions 覆盖。它们覆盖了**成本架构**（分层存储）、**安全合规**（默认加密策略）、**生态集成**（S3 事件通知）、**运维合规**（访问日志管线）、**产品体验**（企业级 Web UI）五个关键维度。
>
> **建议实施顺序：** #5（企业 UI，产品化基础，可并行） → #3（通知投递，补全存根功能，低成本高价值） → #2（默认加密，安全准入） → #4（访问日志，合规驱动） → #1（分层存储，架构改造最大）。#5 和 #3 可以**最快交付**且**零架构风险**；#1 需要最多的设计和测试投入，建议放在最后。
