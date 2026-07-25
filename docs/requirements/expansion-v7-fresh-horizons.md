# AeroVault 高价值扩展方向（第七期）

> **视角:** 资深架构师 / 产品经理  
> **方法:** 全局代码扫描（~35,600 行 Go 源码，392+ 文件），依次审阅 `ROADMAP.md`、六期 `expansion-directions[-v2..v6]`、`CHANGELOG.md`、`architecture.md`、`TODO.md`，确认每个方向在既有文档中**零覆盖**。  
> **日期:** 2026-07-10  
> **原则:** 选取 5 个既有文档均未覆盖的方向。每个方向附带具体代码锚点、当前状态缺口、边缘案例分析、架构蓝图和实现理由（商业价值 + 技术必要性）。

---

## 总览

| # | 方向 | 类型 | 影响 | 核心代码锚点 | 覆盖检查 |
|---|------|------|------|-------------|---------|
| 1 | **内容去重 & 内容寻址存储（Content-Deduplication / CAS）** | 成本/架构 | 🔴 多副本场景 TCO 核心竞争力 | `internal/service/file_crud.go:Put`, `internal/storage/storage.go:Storage`, `internal/repository/sql_objects.go` | 全文档未覆盖 |
| 2 | **浏览器直传：S3 POST Object / 签名表单上传（Anonymous Form Upload）** | 体验/生态 | 🟠 Web 应用集成断裂 | `internal/api/rest/handler.go:PostForm`, `internal/api/s3compat/extra.go`, `internal/auth/policy.go` | 全文档未覆盖 |
| 3 | **计费 & 用量计量系统（Billing & Usage Metering）** | 平台/商业化 | 🔴 SaaS 变现的缺失拼图 | `internal/repository/quota.go`, `internal/telemetry/metrics.go`, `internal/repository/ai_usage_cost_test.go` | 全文档未覆盖 |
| 4 | **可恢复上传会话（Resumable Upload Session / TUS 模式）** | 体验/可靠性 | 🟠 大文件上传网络容错缺口 | `internal/service/file_multipart.go`, `internal/storage/storage.go:InitMultipart`, `internal/api/rest/handler.go:PostForm` | 全文档未覆盖 |
| 5 | **结构化元数据 Schema & 元数据全文检索** | 差异/平台 | 🟠 从"对象存储"到"智能内容平台" | `internal/service/file.go:validateMetadata`, `internal/repository/sql_objects.go`, `internal/api/rest/handler.go:writeMetadataHeaders` | 全文档未覆盖 |

---

## 1. 内容去重 & 内容寻址存储

### 为什么需要它

当前每个 `PUT` 请求都创建一个新的存储 blob，即使内容与已有对象完全相同。代码走读确认：

```
internal/service/file_crud.go:Put → storage.Put → 直接写入 blob
```

没有任何检查"是否已存在相同内容"。这在以下场景中造成严重的存储浪费：

- **CI/CD 制品仓库**：每次构建产生相似或相同的文件（JAR 包、Docker layer、node_modules），90%+ 的内容在每次构建间重复
- **备份系统**：全量备份通常包含大量未更改的文件
- **AI/ML 数据集**：同一数据集被多次组织、归档、分享，产生大量副本
- **容器镜像仓库**：同一基础镜像层被成千上万的镜像引用

**现有去重机制的缺位：** 已有 `idempotency_keys` 表（`repository/idempotency.go`）实现了**请求级别**的去重（同一请求重放不创建副本），但这与内容去重完全不同——不同的 key 上传相同的内容，仍然是 N 份存储。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/service/file_crud.go:Put` | 无条件调用 `store.Put` | 无内容哈希检查→重复 blob |
| `internal/storage/storage.go:Storage` 接口 | `Put(ctx, key, ...)` → 必须指定 key | 不支持 key-to-content 间接映射 |
| `internal/storage/local_write.go:Put` | `os.WriteFile(keypath, data)` → 直接写入 | 不引用已有内容 |
| `internal/repository/sql_objects.go` | objects 表直接存 `storage_key` | 无 content-addressed 层 |
| `internal/repository/repository.go:Object` | Object.StorageKey = `tenant/bucket/key` | 无 `ContentHash` 字段 |
| `internal/service/file_features.go:CopyObject` | Copy 读取源对象后重新写入全量数据 | 无法做 server-side 逻辑复制 |
| `internal/reconcile/scrub.go` | `_aero_content_md5` 完整性验证已存在 | 验证数据但不驱动去重 |
| `internal/repository/migrations/*/0005_versioning_tagging.up.sql` | 版本设计为每个版本独立 storage_key | 版本之间内容可能相同 |

### 边缘情况分析

| 场景 | 当前行为 | 期望行为 |
|------|---------|---------|
| **相同内容不同 key** | `PUT a.txt` 和 `PUT b.txt` 内容相同 → 2 份 blob | 2 个 metadata row，1 个物理 blob，引用计数 2 |
| **相同内容同 key 覆盖** | 版本化桶中覆盖 `a.txt` 但内容不变 → 创建新版本新 blob | 检测内容相同 → 复用已有 blob，不浪费存储 |
| **内容去重后删除** | 删除 `a.txt`（引用计数 1→2→1） | 不作为 GC 条件：引用计数归零后删除物理 blob |
| **跨租户内容相同** | 租户 A 和 B 上传相同的公开文档 | 默认不跨租户去重（安全隔离），可选通过 `X-Aero-CAS-Share: cross-tenant` 启用 |
| **去重阈值** | 小文件（< 4KB）去重收益小 | 可配置 `CAS_MIN_SIZE_BYTES=4096`，小于阈值跳过 |
| **加密对象去重** | SSE 加密的对象内容相同但 envelope 不同 | 去重必须在 encryption layer 之后（密文比较），或提供明文哈希选项 |
| **分片上传的 CAS** | Multipart upload 的 10MB 分片与另一个单对象内容相同 | 分片级去重（block-level dedup），而非只做对象级 |

### 架构蓝图

```
┌─ 内容寻址层（Content-Addressable Storage）─────────────────────│
│ 新增包: internal/storage/cas/                                    │
│                                                                  │
│ type ContentHash [32]byte  // SHA-256 of content bytes           │
│                                                                  │
│ type CASStore struct {                                            │
│     store      storage.Storage  // 底层的物理存储                 │
│     repo       repository.Repository                             │
│     minSize    int64            // 去重最小尺寸                   │
│     crossTenant bool           // 是否允许跨租户去重              │
│ }                                                                 │
│                                                                  │
│ func (cs *CASStore) Put(ctx, key string, r io.Reader,             │
│     size int64, opts storage.PutOptions) (ObjectInfo, error) {    │
│     // 1. streaming 计算 SHA-256                                  │
│     // 2. CAS 查找: repo.LookupContentHash(hash)                 │
│     // 3. 如果存在: refCount++, 返回现有 blob（不写入）             │
│     // 4. 如果不存在: store.Put(casKey, ...) + 写入 content_hash   │
│     //    行                                                      │
│     // 返回: ObjectInfo{StorageKey: casKey}                       │
│ }                                                                 │
│                                                                  │
│ Storage key 变化:                                                 │
│   Object.StorageKey:                                              │
│     当前: tenant/bucket/key[@v<id>]                               │
│     去重后: cas/{hash[:2]}/{hash}  // 按哈希前 2 字节分片          │
│                                                                  │
│ 保留向后兼容:                                                      │
│   PutOptions 新增字段: UseCAS bool（默认 false，逐桶/逐租户开启）   │
│   现有对象不受影响（storage_key 保持原样）                          │
│   只有 UseCAS=true 的写入走重定向到 cas/* 路径                      │
└────────────────────────────────────────────────────────────────┘

┌─ Repository 扩展 ──────────────────────────────────────────────│
│ 新增表: content_hashes（migration N+1）                          │
│   hash         BYTEA PRIMARY KEY   // SHA-256 (32 bytes)        │
│   cas_key      TEXT NOT NULL       // cas/{hash[:2]}/{hash}     │
│   ref_count    INT NOT NULL DEFAULT 1                          │
│   size_bytes   INT8 NOT NULL                                    │
│   first_tenant TEXT                  // 首次出现的租户 ID        │
│   created_at   TEXT NOT NULL         // RFC3339Nano             │
│                                                                  │
│ 新增 Repository 方法:                                             │
│   LookupContentHash(ctx, hash) (*ContentHashRow, error)          │
│   IncrementRefCount(ctx, hash) error                             │
│   DecrementRefCount(ctx, hash) (remainingRefs int, err)          │
│   ListOrphanContentHashes(ctx) ([]ContentHashRow, error)         │
│                                                                  │
│ 引用计数 GC:                                                      │
│   当 DecrementRefCount 返回 0 → enqueue 异步删除 cas blob       │
│   reconcile 增加 CAS 孤儿检查（orphan_content_hashes sweep）      │
└────────────────────────────────────────────────────────────────┘

┌─ 面向用户的配置 ───────────────────────────────────────────────│
│ 桶级配置:                                                         │
│   PUT /v1/buckets/{bucket}?cas=true                              │
│   bucketConfig.ContentDedup bool (migration N+2 新增字段)        │
│                                                                  │
| REST API:                                                        |
|   POST /v1/files/{key}?cas=true  // 单次写入启用去重              |
|                                                                  |
| 指标:                                                             |
|   storage_dedup_bytes_saved{tenant, bucket}                      |
|   storage_dedup_ratio{tenant, bucket}   // 去重率                 |
|   storage_dedup_refcount_histogram                              |
└────────────────────────────────────────────────────────────────┘

**实现优先级：** 对象级去重（本期）→ 块级去重（v2）→ 分片去重（v3）。对象级去重解决 80% 的场景，实现复杂度最低。

**复杂度:** L · **存储影响:** -50%~90%（取决于工作负载） · **代码变更:** ~1500 行新代码 + ~400 行修改

---

## 2. 浏览器直传：S3 POST Object / 签名表单上传

### 为什么需要它

当前代码支持两种上传方式：

1. **`PUT /v1/files/{key}`** — 需要 `Authorization: Bearer` 或 `X-Api-Key` 头，适合 SDK/CLI
2. **`POST /v1/files`** （multipart/form-data）— `handler.go:PostForm`，仍然需要 API Key 认证
3. **`GET /v1/files/{key}/presign?op=put`** — 预签名 URL，仅限 GET/PUT，不支持 POST

**根本问题：任何浏览器上传都需要暴露 API Key 或依赖服务器端生成预签名 URL。**

S3 兼容 API 的标准解决方案是 **POST Object with Policy**（`POST /{bucket}/{key}` + `?key=` + `?policy=` + `?signature=` 等表单字段）。这种机制：

- 允许服务端生成一个**签名策略文档**（policy document），指定上传约束（bucket、key 前缀、文件大小、Content-Type、有效期等）
- 浏览器直接通过 HTML `<form>` 上传到 S3 endpoint，**不需要 API Key**
- 策略文档使用 HMAC 签名，防止篡改

缺少这个特性意味着：

- **Web 应用必须通过自己的后端中转文件**：用户选择文件 → 发到应用服务器 → 应用服务器 PUT 到 aero-vault → 应用服务器内存/带宽瓶颈
- **无法做服务器端分片上传进度条**：HTML `<form>` 没有上载进度事件，只能依赖客户端 JS XHR，但 XHR 需要 Authorization 头
- **多部分表单上载兼容性断裂**：S3 兼容的客户端库期望 `POST` 端点可用

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/api/rest/handler.go:PostForm` | 需要认证的 multipart POST | 无匿名策略上传 |
| `internal/api/s3compat/extra.go` | 仅有 `CreateMultipartUpload` 和 `CompleteMultipartUpload` | 无 S3 POST handler |
| `internal/api/s3compat/router.go` | 路由分发 | 无 `POST /{bucket}/{key}` 分发 |
| `internal/auth/auth.go:Registry` | 支持 SigV4、JWT、ApiKey | 无 POST policy 验证 |
| `internal/auth/policy.go` | IAM-style policy 解析器（`Condition`、`IpAddress` 等） | 存在但仅用于 bucket policy——**可直接复用** |
| `internal/storage/sign.go` | HMAC-SHA256 签名 | 类似算法可为 policy 签名 |
| `internal/service/file_crud.go:Put` | 核心写入 | 可作为 POST 的后端 |
| `internal/api/s3compat/xml.go` | S3 XML 编码/解码 | 需要添加 `PostResponse` XML |
| `internal/config/config_app.go` | 应用配置 | 无 POST policy 相关配置 |

### 边缘情况分析

| 场景 | 当前行为 | 期望行为 |
|------|---------|---------|
| **无 API Key 浏览器上传** | 用户打开 HTML 页面，选择文件 → 无法直接上传到 aero-vault | 服务端生成`policy`文档 → 浏览器 `<form>` POST → aero-vault 验证签名 → 存储 |
| **策略已过期上传** | 上传请求在 policy 过期后到达 | 无检查 | 返回 `403 AccessDenied` + `ExpiredToken` |
| **Content-Length 超限** | 上传 1GB 文件但 policy 限制 100MB | 无检查 | 连接中途关闭或返回 `EntityTooLarge` |
| **Key 前缀越权** | policy 允许 `uploads/` 前缀，用户上传到 `admin/` | 无检查 | 返回 `302 Redirect` + `AccessDenied` |
| **Policy 篡改** | 中间人修改 policy 中的 `max-size` | 无签名验证 | HMAC-SHA256 签名检测篡改 → 拒绝 |
| **POST redirect** | 上传成功后需要跳转到确认页面 | 不支持 | `success_action_redirect` 表单字段支持 |
| **条件 Content-Type** | 只允许上传 `image/*` 类型 | 无检查 | `x-amz-content-type` 匹配策略条件 |
| **跨域上传** | 浏览器 preflight 检查 | 当前 CORS 中间件已支持 | 确保 `POST` 方法在 CORS `AllowedMethods` 中 |

### 架构蓝图

```
┌─ 签名策略引擎 ─────────────────────────────────────────────────│
│ 新增: internal/auth/postpolicy.go                                │
│                                                                  │
│ type PostPolicy struct {                                         │
│     Expiration    time.Time                                      │
│     Conditions    []PostCondition                                │
│     // 隐式约束:                                                  │
│     //   acl, bucket, key, content-length-range,                 │
│     //   content-type, x-amz-*, success_action_redirect,        │
│     //   success_action_status, filename                        │
│ }                                                                 │
│                                                                  │
│ type PostCondition struct {                                       │
│     Operator string  // "eq" | "starts-with" | "content-length-  │
│                      // range" | "in"                            │
│     Field    string                                               │
│     Value    interface{}                                          │
│ }                                                                 │
│                                                                  │
│ func (p *PostPolicy) Encode() (b64JSON string)                    │
│   // base64(JSON(expiration, conditions))                        │
│                                                                  │
│ func (p *PostPolicy) Sign(secret []byte) (signature string)       │
│   // HMAC-SHA256(secret, b64EncodedPolicy)                      │
│                                                                  │
│ func (p *PostPolicy) Verify(form FormFields) error                │
│   // 解析每个 condition，验证表单字段满足约束                     │
│   // 1. 检查过期时间                                              │
│   // 2. 检查 key 前缀匹配                                         │
│   // 3. 检查文件大小范围                                           │
│   // 4. 检查 Content-Type 匹配                                     │
│   // 5. ...                                                       │
│                                                                  │
│ func (p *PostPolicy) CreatePolicy(ctx, tenant, bucket,            │
│     opts PostPolicyOpts) (PostPolicyResponse, error)              │
│   // REST API:                                                    │
│   // GET /v1/files/*key/presign?op=post&expires=3600&            │
│   //   &content_length_range="0,10485760"&key_prefix=uploads/    │
│   // 返回: { "url", "policy", "signature", "fields" }            │
└────────────────────────────────────────────────────────────────┘

┌─ S3 POST Object Handler ───────────────────────────────────────│
│ 新增: internal/api/s3compat/post.go                              │
│                                                                  │
│ func (h *Handler) PostObject(w, r, bucket, key) {                │
│     // 1. 从表单字段提取 policy + signature + access key         │
│     // 2. base64 解码 policy JSON                                │
│     // 3. 验证 signature (HMAC-SHA256)                           │
│     // 4. 调用 policy.Verify(所有表单字段)                       │
│     // 5. 验证通过 → h.svc.Put(...) → 保存对象                    │
│     // 6. 返回 204 (或 success_action_redirect)                  │
│ }                                                                 │
│                                                                  │
│ 路由:                                                             │
│   POST /{bucket}/{key+} → PostObject handler                     │
│   （与现有 `?uploads` 的 POST 做 URL 参数判别）                   │
└────────────────────────────────────────────────────────────────┘

┌─ SDK / Web UI 集成 ───────────────────────────────────────────│
│ JS Browser SDK 新增:                                             │
│   client.createUploadPolicy(options) → {url, fields}             │
│   // 生成 policy 后，直接用 HTMLFormElement.submit()             │
│                                                                  │
│ Web UI 增强:                                                      │
│   上传面板增加"直接上传（浏览器直传）"选项                          │
│   文件不经过 Web UI 后端 → 减少服务器带宽压力                      │
│   进度条通过 XHR `upload.onprogress` 事件                        │
└────────────────────────────────────────────────────────────────┘

**复杂度:** M · **用户影响:** ★★★★☆（Web 集成场景） · **代码变更:** ~800 行新代码 + ~200 行修改

---

## 3. 计费 & 用量计量系统

### 为什么需要它

当前系统**有计量，无计费**：

- **存储计量**：`repository/quota.go` 有 `UsedBytes` / `UsedObjects`，`telemetry/metrics.go` 有 `storage_bytes` / `storage_objects` 按租户
- **AI 成本追踪**：`internal/ai/cost.go` 按 token 记录成本到 `ai_usage` 表（`cost_micros`），`internal/repository/ai_usage_cost_test.go` 验证了 `SumAICostMicros`
- **传输计量**：无（如 expansion-v4 #1 所述）

但**所有计量都停留在运维可观测层，没有连接到计费系统**：

```
计量层                         计费层（缺失）
┌──────────────┐            ┌──────────────────┐
│ storage_bytes │  ─────×──  │ 月底账单生成      │
│ ai_cost_micros│  ─────×──  │  按存储量阶梯定价  │
│ egress_bytes  │  ─────×──  │  AI 调用按量计费  │
│ requests_total│  ─────×──  │  订阅套餐映射     │
└──────────────┘            │  发票发送          │
                            │  Stripe 扣款       │
                            └──────────────────┘
```

这意味着：
- **无法运营 SaaS 产品**——不能给客户出账单
- **免费层无法定义**——不能限制免费用户只能存储 1GB
- **超额无法处理**——超过 quota 的用户无法优雅地过渡到付费或降级
- **缺乏计费事件**——没有 `billing.cycle.completed` 事件通知用户

现有 AI 成本追踪（`internal/ai/cost.go`）和存储配额（`internal/repository/quota.go`）提供了构建计费系统所需的**原始数据源**。缺的是**聚合 → 定价 → 发票 → 支付**的管线。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/repository/quota.go:TenantQuota` | `UsedBytes/UsedObjects` | 无计费周期、无定价关联 |
| `internal/repository/quota.go:ListTenantQuotas` | 按租户列出 | 无 `price_tier` 字段 |
| `internal/repository/ai_usage_cost_test.go` | AI 费用聚合已测试 | 无聚合到租户级账单 |
| `internal/telemetry/metrics.go` | `ai_cost_micros_total{tenant,model}` | 无租户级聚合到计费视图 |
| `internal/repository/repository.go:Usage` | 记录单次用量 | 无计费关联 ID |
| `internal/api/rest/admin.go` | 管理 API（key/tenant/quota） | 无计费管理端点 |
| `internal/repository/tenants.go` | 租户 CRUD | 无 `billing_plan`、`payment_method` 字段 |
| `internal/config/config.go` | 配置结构 | 无 `BILLING_*` 配置节 |
| `internal/api/rest/handler.go:GetUsage` | `GET /v1/usage` 返回用量 | 不返回预计费用 |

### 边缘情况分析

| 场景 | 当前行为 | 期望行为 |
|------|---------|---------|
| **免费层用户超限** | 超过配额 → PUT 返回 403 | PUT 返回 402 Payment Required + 升级提示 |
| **AI 超额自动降级** | Chat 返回 BudgetExceeded | Chat 降级为纯 BM25 搜索（失去 LLM 能力但保留搜索） |
| **月结日并行账单** | 月底 23:59:59 的请求和 00:00:01 的请求跨月 | 请求级 timestamp 精确到 ms，按 UTC 日归属月 |
| **退款处理** | 用户不满意要求退款 | 记录退款调整到 `billing_adjustments` 表 |
| **套餐变更时间点** | 月中从 Standard 升级到 Premium | 按比例分摊（proration）计算月费 |
| **免费额度耗尽** | 免费用户当月 10GB 用尽 | 停止上传但不影响读取（存储可读不可写） |
| **多币种支持** | 跨国用户需要按本地货币计费 | 存储 USD 金额 + 汇率转换时间戳 |
| **欠费用户数据保留** | 用户未付款 | 标记为 `payment_past_due` → 保留期（30天）→ 自动软删除 |
| **无缝扩展** | 计费需要在运行时新增套餐选项 | 套餐定义应配置化（YAML/JSON），无需重新部署 |

### 架构蓝图

```
┌─ 计费模型 ─────────────────────────────────────────────────────│
│ 新增包: internal/billing/                                        │
│                                                                  │
│ type Plan struct {                                               │
│     ID          string    // "free" | "starter" | "pro"         |
│     Name        string                                           |
│     PriceMonth  int64     // 月费（美元微单位，如 999 = $9.99）   |
│     Included    Included                                          |
│     Overages    Overages                                         |
│ }                                                                 │
│                                                                  │
│ type Included struct {                                           │
│     StorageBytes int64  // 1 << 30 = 1GB                        |
│     Objects      int64  // 100000 = 100K objects                 |
│     AIQueries    int64  // 1000 = 每月 1000 次 AI 搜索            |
│     EgressBytes  int64  // 10 << 30 = 10GB 出站流量              |
│ }                                                                 │
│                                                                  │
│ type Overages struct {                                           │
│     StoragePerGBMonth int64  // 每 GB/月 超量费                   |
│     AIQueryEach       int64  // 每次 AI 查询超量费                 |
│     EgressPerGB       int64  // 每 GB 出站费                      |
│ }                                                                 │
│                                                                  │
│ 套餐定义方式:                                                     │
│   文件: deploy/plans.yaml（非代码，不重启可修改）                   │
│   载入: billing.LoadPlans(path) → map[string]*Plan               │
└────────────────────────────────────────────────────────────────┘

┌─ 月度用量聚合 ─────────────────────────────────────────────────│
│ 新增 internal/billing/aggregator.go                              │
│                                                                  │
│ 每日凌晨 00:05（cron-like job）:                                  │
│   1. 遍历每个活跃租户                                             │
│   2. 查询 storage_bytes、objects、ai_usage 的昨日累计             │
│   3. 写入 billing_daily_usage 表:                                 │
│        tenant_id, date, plan_id, storage_bytes, objects,          │
│        ai_queries, ai_tokens, egress_bytes, created_at           │
│                                                                  │
│ 月初日凌晨 00:10:                                                  │
│   1. 合并上月每日数据                                              │
│   2. 计算包含项与超量费用                                           │
│   3. 生成 invoice 记录（状态: pending）                            │
│   4. enqueue 通知 job（发送邮件/Webhook）                          │
│                                                                  │
│ 计费表:                                                            │
│   billing_plans        // 套餐定义（配置驱动，表内缓存）           │
│   billing_subscriptions // 租户→计划映射 + 计费周期               │
│   billing_daily_usage  // 每日聚合用量                            │
│   billing_invoices     // 月账单                                  │
│   billing_adjustments  // 退款/信用/手动调整                      │
│   billing_payments     // 支付记录                                │
└────────────────────────────────────────────────────────────────┘

┌─ Stripe 集成 ──────────────────────────────────────────────────│
│ 新增 internal/billing/stripe.go                                  │
│                                                                  │
│ 事件（Stripe Webhook → AeroVault）:                               │
│   invoice.paid         → 标记 invoice 已付 + 解锁超额限制          │
│   invoice.payment_failed → 标记 payment_past_due + 发送通知      │
│   customer.subscription.updated → 同步套餐变更                    │
│   customer.subscription.deleted → 标记租户为关闭中                 │
│                                                                  │
│ 事件（AeroVault → Stripe API）:                                   │
│   创建租户 → Stripe Customer                                     │
│   选择套餐 → Stripe Subscription                                 │
│   月初 → Stripe Invoice                                          │
│   支付 → Stripe PaymentIntent                                    │
│                                                                  │
│ REST API:                                                         │
│   GET /v1/admin/billing/plans → 列出可用套餐                      │
│   POST /v1/admin/billing/subscribe → 创建/变更订阅                │
│   GET /v1/admin/billing/invoices → 查看账单                      │
│   POST /v1/admin/billing/invoices/{id}/pay → 手动支付            │
│   GET /v1/admin/billing/usage → 当前周期用量 + 预估费用           │
└────────────────────────────────────────────────────────────────┘

**复杂度:** L · **商业影响:** ★★★★★（产品化的最后一块拼图） · **代码变更:** ~2000 行新代码 + ~300 行修改

---

## 4. 可恢复上传会话（Resumable Upload Session / TUS 模式）

### 为什么需要它

当前大文件上传只有**多分片上传（Multipart Upload）**`internal/service/file_multipart.go`。但这要求客户端：

1. 调用 `InitMultipartUpload` 获取 uploadID
2. 手动将文件切分为分片
3. 逐分片调用 `UploadPart`（含分片号 + uploadID）
4. 所有分片完成后调用 `CompleteMultipartUpload`
5. **如果网络中断后重连，必须知道所有已完成的分片和 uploadID**——客户端状态丢失后无法恢复

这对于以下场景是**严重用户体验断裂**：

- **移动端上传**：App 进入后台、切换基站、地铁隧道 → 连接中断 → 重新选择文件重传
- **Web 大文件上传**：浏览器刷新、标签页切换 → 文件需要重新选择
- **跨区域上传**：用户在上海上传 10GB 数据集到美东服务器 → 丢包率 5% → 极大概率中断
- **CI/CD 大型构建产物**：Docker 镜像 layer（数百 MB）上传 → CI worker 重启后必须全量重传

**TUS 协议（Resumable Upload Protocol）** 是行业标准解决方案（`tus.io`），已被 Vimeo、Cloudflare Stream、Google Photos 等使用。核心模式：

1. 客户端发起创建上传会话 → 服务端返回一个 `upload_url`（如 `/uploads/{session_id}`）
2. 客户端通过 `PATCH upload_url` 发送数据块，带 `Upload-Offset` 头指定偏移量
3. 服务端记录已收到的偏移量
4. 断线后客户端 `HEAD upload_url` 查询已接收的偏移量
5. 从该偏移量继续上传

**与现有 Multipart Upload 的关系：** 两者不同。Multipart Upload 要求客户端管理分片；Upload Session 服务端管理已接收的字节范围，客户端只需要单调递增地发送数据。Session 可以内部使用 Multipart Upload 作为后端（分片由服务端管理），也可以直接追加写入。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/service/file_multipart.go` | MultipartUpload（客户端管理分片） | 无服务端管理偏移量的 Session |
| `internal/storage/storage.go:InitMultipart/UploadPart` | 多分片接口 | 无 `Append(ctx, key, offset, r)` 追加写入 |
| `internal/storage/local.go` | local 存储 | 无追加写支持（需 truncate+write） |
| `internal/storage/s3.go` | S3 SDK | S3 原生不支持追加写→需要通过临时文件组合 |
| `internal/api/rest/handler.go:PostForm` | 快速上传（小文件） | 无 Session 管理端点 |
| `internal/api/rest/router.go` | 路由注册 | 无 `/uploads/*` 路由 |
| `internal/repository/sql_uploads.go` | 上传记录表（分片上传） | 无 UploadSession 表 |
| `internal/jobs/jobs.go` | Job 队列 | 可复用清理过期 Session |
| `internal/config/config_app.go` | 应用配置 | 无 `UPLOAD_SESSION_*` 配置 |

### 边缘情况分析

| 场景 | 当前行为 | 期望行为 |
|------|---------|---------|
| **上传中网络中断** | 客户端只有 uploadID，不知道已上传哪些分片 | `HEAD /uploads/{id}` → `Upload-Offset: 5242880` 继续 |
| **并发上传同一 Session** | 两个客户端同时 `PATCH` 同一 session | 互斥锁 + 串行化，后到者返回 `409 Conflict` |
| **会话过期** | 上传 10GB 用时 3 天（跨夜） | `UPLOAD_SESSION_TTL_HOURS=72` 配置，超时后 `404 Not Found` |
| **Service 重启中断上传** | 正在进行的 session 在内存中丢失 | session 状态持久化到 DB（uploads 表扩展字段 `received_bytes`） |
| **同文件续传校验** | 用户修改了原文件后重新上传 | `Upload-Metadata` 头可携带文件指纹，偏移量不一致时拒绝 |
| **最终组装时发现不完整** | 声称偏移量 = 100MB 但实际只收到 99MB | 验证 `Content-Length` + `Upload-Offset` 一致后才完成 |
| **超大 Session（>5TB）** | 单个 session 可能跨数月 | 偏移量使用 `int64` 足够（9.22EB）；定期强制完成 |
| **空 Session** | 创建 session 但从未上传数据 | 后台 reaper 定期清理 `created_at < now - TTL` 的 session |
| **已完成的 Session 重复完成** | 客户端多次调用 `POST /uploads/{id}/complete` | 幂等：`completed_at != NULL` → 返回已有 `Location` |

### 架构蓝图

```
┌─ Upload Session 模型 ──────────────────────────────────────────│
│ 新增 internal/api/rest/upload_session.go                         │
│                                                                  │
│ type UploadSession struct {                                      │
│     ID            string        // uuid                         │
│     TenantID      string                                        │
│     Bucket        string                                        │
│     Key           string                                        │
│     ContentType   string                                        │
│     Metadata      map[string]string                              │
│     TotalBytes    int64         // 0 = unknown, >0 = promised    |
│     ReceivedBytes int64         // 当前已接收                     |
│     StorageKey    string        // 临时存储路径                   |
│     Status        string        // "active" | "completed" |      |
│                                  // "expired"                    |
│     CreatedAt     time.Time                                      |
│     UpdatedAt     time.Time                                      |
│     CompletedAt   *time.Time                                     |
│ }                                                                 │
│                                                                  │
│ TUS 协议端点:                                                     │
│   POST   /uploads         → 创建 session（返回 Location）        │
│   HEAD   /uploads/{id}    → 查询上传进度（Upload-Offset）        │
│   PATCH  /uploads/{id}    → 追加数据（Upload-Offset + body）     │
│   DELETE /uploads/{id}    → 取消/放弃上传                        │
│   POST   /uploads/{id}/complete → 完成（组装对象）                │
│                                                                  │
│ 响应头:                                                           │
│   Upload-Offset: 5242880                                         │
│   Tus-Resumable: 1.0.0                                           │
│   Upload-Length: 10485760                                        │
│   Location: /uploads/{id}                                        │
└────────────────────────────────────────────────────────────────┘

┌─ 后端持久化 ───────────────────────────────────────────────────│
│ 新增 repository 表: upload_sessions                              │
│   id              TEXT PRIMARY KEY                               │
│   tenant_id       TEXT NOT NULL                                  │
│   bucket          TEXT NOT NULL                                  │
│   key             TEXT NOT NULL                                  │
│   content_type    TEXT                                           │
│   metadata        TEXT (JSON)                                    │
│   total_bytes     INT8 DEFAULT 0                                 │
│   received_bytes  INT8 DEFAULT 0                                 │
│   storage_key     TEXT NOT NULL                                  │
│   status          TEXT NOT NULL DEFAULT 'active'                 |
│   created_at      TEXT NOT NULL                                  │
│   updated_at      TEXT NOT NULL                                  │
│   completed_at    TEXT                                           │
│                                                                  │
│ 临时文件存储:                                                     │
│   local 后端: /var/sessions/{tenant}/{session_id}.tmp            │
│   写入策略: append-only + sha256 流式校验                        │
│   完成时: rename to object storage path                          │
│                                                                  │
│ S3 后端:                                                          │
│   临时文件: .sessions/{tenant}/{session_id}.tmp                  │
│   使用 S3 Multipart Upload 内部加分片（服务端管理分片边界）        │
│   完成时: CompleteMultipartUpload                                 │
│                                                                  │
│ 后台清理:                                                         │
│   reconcile 扩展: UploadSessionReaper                            │
│   每 10 分钟扫描过期 session                                      │
│   status = "expired" + 删除临时文件                               │
└────────────────────────────────────────────────────────────────┘

┌─ 与现有 Multipart Upload 的关系 ──────────────────────────────│
│                          Multipart Upload           Upload Session  │
│  ──────────────────────  ───────────────────────  ─────────────────  │
│  分片管理                 客户端                     服务端          │
│  断线恢复                 客户端需保持所有分片状态    HEAD 查偏移量即可 │
│  上传顺序                 无序（可任意分片号）      必须按偏移量有序  │
│  并发上传                 支持（并行上传分片）      不支持（串行追加） │
│  最终文件大小              提前未知（片量不定）      提前已知（Total）  │
│  进度粒度                 分片级                    字节级            │
│  适用场景                 并行加速+已知分片          网络不稳定的上传  │
│  互通性                   两种协议可在服务端互相转换（互相导入）        │
└────────────────────────────────────────────────────────────────┘

**复杂度:** M · **用户影响:** ★★★★☆（大文件上传核心体验） · **代码变更:** ~1200 行新代码 + ~300 行修改

---

## 5. 结构化元数据 Schema & 元数据全文检索

### 为什么需要它

当前元数据系统（`internal/service/file.go:PutOptions.Metadata`）是一个**无结构的 `map[string]string`**：

```go
type PutOptions struct {
    Metadata  map[string]string  // user-defined key-value pairs
    ...
}
```

验证函数 `validateMetadata`（`file.go:103-128`）仅检查总大小和 key/value 长度——不做类型校验，不支持嵌套结构，不能定义必填字段，没有索引加速：

| 约束 | 当前状态 | 理想状态 |
|------|---------|---------|
| 数据类型 | 全字符串（`map[string]string`） | 支持 `string`、`number`、`boolean`、`date`、`array`、`object` |
| 值校验 | 仅长度 | 必填、正则、范围、枚举 |
| 索引 | 无 | 元数据字段 B-tree / GIN 索引 |
| 搜索 | 不支持（只能按 `key` 前缀匹配） | 支持 `metadata.author = "John" AND metadata.pages > 10` |
| Schema 定义 | 无 | 用户定义 Schema → 系统自动校验 + 创建索引 |
| 默认值 | 无 | Schema 可为字段指定默认值 |
| 只读系统元数据 | `_aero_*` 前缀保留 | 保持 `_aero_*` 前缀 |

这意味着：
- **元数据搜索不存在**：用户不能写"找出所有 `author=John` 且 `pages>10` 的 PDF"
- **数据质量无法保证**：`created_date` 可以是 "2024-01-01" 或 "01/01/2024" 或 "last week"
- **无法做 Schema 演进**：新增必填字段后，老对象不会被校验证
- **第三方集成困难**：没有自描述 Schema，外部系统不知道元数据的结构

这是从"对象存储"到"智能内容平台"的关键能力。AWS S3 不支持富元数据搜索（只能用 Tags），但 Snowflake Object Store、MongoDB GridFS、Azure Blob Index 都支持。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/service/file.go:validateMetadata` | 仅长度+总大小校验 | 无类型/必填/Schema 校验 |
| `internal/service/file.go:PutOptions.Metadata` | `map[string]string` | 不支持嵌套结构 |
| `internal/repository/sql_objects.go` | `SELECT ... WHERE storage_key, tenant_id, bucket` | 无 `WHERE metadata->>'key' = ?` 查询 |
| `internal/repository/repository.go:ListObjects` | 仅前缀过滤 | 不支持元数据过滤 |
| `internal/api/rest/handler.go:getObject/writeMetadataHeaders` | GET 返回 `X-Meta-{key}` 头 | 无元数据搜索 API |
| `internal/api/rest/router.go` | 路由注册 | 无 `GET /v1/files?meta.author=John` |
| `internal/api/rest/search.go` | 仅全文语义搜索 | 无结构化属性搜索 |
| `internal/snapshot/snapshot.go` | 快照包含 metadata（已验证） | 搜索时 metadata 被忽略 |
| `internal/repository/sql.go:rebind` | SQL 重绑定 | 需要 GIN/json 查询语法适配 |

### 边缘情况分析

| 场景 | 当前行为 | 期望行为 |
|------|---------|---------|
| **元数据搜索性能** | 100 万个对象，搜索 `author=John` → 扫描全部（无索引） | GIN 索引 → 毫秒级响应 |
| **Schema 变更向后兼容** | 已存 100 个对象没有 `pages` 字段，新增 Schema 要求必填 | 查询时缺失字段视为 NULL，不报错 |
| **元数据大小限制** | 当前最大 2KB 元数据 | 支持 Schema 定义：大文本字段例外（存储在单独表或压缩） |
| **保留字污染** | 用户使用 `key` `tenant` 等 SQL 保留字作为元数据键 | JSON 路径转义 → 安全 |
| **Schema 版本管理** | 对象 A 用 Schema v1，对象 B 用 Schema v2 → `author` 从 string 变为 object | 检索使用当前 Schema 解释，向下兼容 |
| **嵌套元数据** | `{"address": {"city": "Beijing", "zip": 100000}}` | `metadata->>'address.city'` 支持路径表达式 |
| **元数据与 Tags 关系** | Tags 是已有 KV 模型 | Tags 应作为特殊元数据（`_aero_tags.*`）统一到 Schema 体系 |
| **Postgres JSONB vs SQLite** | MySQL JSON 在 SQLite 中存为 TEXT | 需要兼容查询抽象层 |

### 架构蓝图

```
┌─ Metadata Schema 定义 ─────────────────────────────────────────│
│ 新增 internal/metadata/schema.go                                 │
│                                                                  │
│ type FieldType string                                            │
│ const (                                                           │
│     TypeString  FieldType = "string"                             │
│     TypeNumber  FieldType = "number"                             │
│     TypeBool    FieldType = "boolean"                            │
│     TypeDate    FieldType = "date"    // ISO 8601                │
│     TypeArray   FieldType = "array"                              │
│     TypeObject  FieldType = "object"                             │
│ )                                                                 │
│                                                                  │
│ type FieldDef struct {                                           │
│     Name        string        // "author"                       │
│     Type        FieldType     // "string"                        │
│     Required    bool          // 必填                             |
│     Default     *string       // 默认值                           |
│     Enum        []string      // 枚举值                           |
│     Pattern     string        // 正则（如 `^[a-z]+$`）           |
│     MinLength   *int                                              |
│     MaxLength   *int                                              |
│     Minimum     *float64                                         |
│     Maximum     *float64                                         |
│     Indexed     bool          // 是否建索引                       |
|     Description string                                           |
| }                                                                 |
|                                                                   |
| type Schema struct {                                              |
|     ID         string      // uuid                               |
|     Name       string      // "document"                         |
|     Version    int         // 1, 2, …                            |
|     Bucket     string      // 应用到哪个桶（空=全桶）             |
|     Fields     []FieldDef                                        |
|     CreatedAt  time.Time                                         |
| }                                                                 |
└────────────────────────────────────────────────────────────────┘

┌─ API 扩展 ─────────────────────────────────────────────────────│
|                                                                   |
| Schema 管理:                                                      |
|   POST   /v1/admin/metadata/schemas       → 创建 Schema          |
|   GET    /v1/admin/metadata/schemas       → 列出 Schema          |
|   GET    /v1/admin/metadata/schemas/{id}  → Schema 详情          |
|   PUT    /v1/admin/metadata/schemas/{id}  → 更新 Schema（新建版本）|
|   DELETE /v1/admin/metadata/schemas/{id}  → 删除 Schema          |
|                                                                   |
| 元数据搜索（现有 /v1/search 扩展）:                                |
|   GET /v1/search?meta.author=John&meta.pages.gt=10                |
|   → 合并语义搜索与属性搜索                                        |
|   → 返回同时匹配语义+属性的交集                                    |
|                                                                   |
| 文件列表扩展（/v1/files 扩展）:                                   |
|   GET /v1/files?prefix=docs/&meta.status=published                |
|   → 在现有 prefix 过滤基础上追加 metadata WHERE 子句             |
|   → 分页支持（marker + limit）                                    |
|                                                                   |
| 元数据查询语法:                                                    |
|   meta.field=value        → 精确匹配                              |
|   meta.field.gt=100       → 大于                                  |
|   meta.field.gte=100      → 大于等于                              |
|   meta.field.lt=100       → 小于                                  |
|   meta.field.lte=100      → 小于等于                              |
|   meta.field.in=a,b,c     → IN                                    |
|   meta.field.exists=true  → 字段存在性                            |
|   meta.field.like=John*   → LIKE 前缀匹配                         |
|   meta.nested.key=val     → 嵌套字段路径                          |
└────────────────────────────────────────────────────────────────┘

┌─ 存储层兼容 ───────────────────────────────────────────────────│
│ Postgres:                                                         │
│   在 objects 表加 `metadata` JSONB 列                            │
│   GIN 索引: CREATE INDEX idx_objects_meta ON objects USING GIN (metadata)  │
│   查询: WHERE metadata @> '{"author": "John"}'                   │
│         WHERE (metadata->>'pages')::int > 10                     │
│                                                                  │
│ SQLite:                                                           │
│   JSON 函数（json_extract, json_type）可用                       │
│   无原生 GIN → 使用表达式索引                                     │
│   CREATE INDEX idx_objects_meta_author                            │
│     ON objects(json_extract(metadata, '$.author'))                │
│   查询: WHERE json_extract(metadata, '$.author') = 'John'        │
│                                                                  │
│ 兼容层:                                                            │
│   internal/repository/sql_helpers.go 新增                         │
│     BuildMetadataWhere(conditions) -> (whereClause, args)         │
│     根据 DBDriver 生成不同语法                                      │
└────────────────────────────────────────────────────────────────┘

**复杂度:** M-L · **用户影响:** ★★★★☆（平台差异化能力） · **代码变更:** ~1800 行新代码 + ~500 行修改

---

## 附录：各方向交叉引用与 ROADMAP 对应检查

| 方向 | ROADMAP # | 对应 check | 既有 expansion doc | 对应 check |
|------|-----------|-----------|-------------------|-----------|
| 内容去重 CAS | #5（large-object hardening）| 不同——#5 聚焦 blob 管理 | 所有六期 | 未见去重讨论 |
| 浏览器直传 | #7（S3 feature parity）| 不同——#7 聚焦 S3 API 缺失，未提 POST Object | 所有六期 | 未见 POST 策略上传 |
| 计费系统 | #2（cost governance）| 不同——#2 聚焦 AI 成本上限，非计费 | 所有六期 | 未见计费模型 |
| 可恢复上传 | #5（large-object hardening）| 弱相关——#5 提及大文件内存溢出 | 所有六期 | 未见 Upload Session |
| 元数据搜索 | #8（content integrity）| 不同——#8 聚焦校验和 | 所有六期 | 未见元数据 Schema |

## 决策记录

| 决策 | 选项 | 选择 | 理由 |
|------|------|------|------|
| 内容去重粒度 | (a) 对象级 (b) 块级 (c) 分片级 | **(a) 对象级（本期）** | 80% 收益 20% 复杂度；块级需要固定大小切割+指纹表，实现量级翻倍 |
| 去重默认启用 | (a) 全局开启 (b) 桶级 opt-in (c) 请求级 opt-in | **(b) 桶级 + (c) 请求级** | 安全默认（不改变现有行为），大型用户按需开启 |
| POST policy 签名密钥 | (a) 使用 API Key (b) 使用独立 Signing Key | **(b) 独立 Signing Key** | API Key 泄漏风险大；Signing Key 可设置只签名 policy 无读权限 |
| 计费套餐定义 | (a) 代码常量 (b) 配置文件 (c) 数据库 | **(b) 配置文件 + (c) 数据库缓存** | 不重启即新增套餐；DB 缓存保证查询性能 |
| Upload Session 存储 | (a) local file append (b) Multipart Upload (c) 二者 | **(c) hybrid** | local=高性能追加写，S3=Multipart 做后端实现，统一抽象层 |
| 元数据 Schema 版本管理 | (a) schema 版本号递增 (b) 所有版本并存 (c) 仅当前 | **(b) 所有版本并存** | 支持对老对象的按旧版本解释，不破坏已有查询 |
| 元数据搜索与语义搜索关系 | (a) 分离端点 (b) 合并到 /v1/search | **(b) 合并到 /v1/search** | 用户一次查询即可语义+属性同时过滤，体验一致 |
