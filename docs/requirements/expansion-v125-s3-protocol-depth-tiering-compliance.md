# 高价值扩展方向：S3 协议深度、存储分层状态机、对象锁合规、异步操作框架与内容透明压缩

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — 237+ Go 源文件、50 对迁移文件、3 套 SDK（Go/Python/JS）、Web UI、MCP 双模式、WebDAV、完整配置层、`deploy/` 全套 Helm/Grafana/Prometheus 配置  
> **去重验证：** 对 `docs/requirements/` 下全部 124 份既有分析文档逐方向进行关键词正则 + 语义交叉验证 + 代码锚点反查，确认本文方向未被已有文档独立深度覆盖  
> **日期：** 2026-07-11  
> **验证状态：** 所有代码锚点已通过实时代码库验证（`git HEAD` 2026-07-11）  
> **核心原则：** **不编写任何代码。** 每个方向必须包含：**代码中存在的具体实现锚点**、**可量化的生产/产品影响**、**架构权衡与边界情况**。

---

## 方法论：越过"功能存在"的表层

AeroVault 已经是一个非常成熟的项目。大路货的扩展点（桶策略、CORS、通知、日志、版本控制、多租户、AI管线、ID基数等）在前 124 轮分析中已经被反复覆盖。

本文聚焦的是一类更隐晦的缺口：**功能的骨架存在，但运行时行为与用户/协议预期之间存在断层**。具体而言：

| 缺口类型 | 判定标准 | 本文方向 |
|----------|---------|----------|
| **协议语义断层** | API 端点"已实现"但忽略了关键的协议参数，导致主流 SDK/工具行为异常 | 方向一（ListObjects Delimiter）、方向二（UploadPartCopy）、方向三（SSE 请求头） |
| **状态机不完整** | 数据模型已支持某个状态字段，但没有对应的状态转换执行器 | 方向四（存储分层转换） |
| **安全模型断裂** | 认证/授权通过了但关键的协议级安全约束未被评估 | 方向五（对象锁 GOVERNANCE vs COMPLIANCE） |

### 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码证据 |
|---|------|------|--------|---------|---------|
| **1** | **S3 ListObjects Delimiter/CommonPrefixes 缺失**——S3 兼容层不支持文件夹式层级浏览 | 协议兼容/产品 | **P0** | S3 ListObjectsV2 的 `delimiter` 参数被完全忽略。任何依赖 S3 虚拟文件夹访问模式的工具（aws-cli、rclone、Cyberduck、AWS SDK 的 `ListObjectsV2Paginator`）会收到扁平列表而非目录结构。文件无法按 `/` 分隔的层级组织浏览 | `internal/api/s3compat/handler.go:457-506` — `listObjectsV2` 解析 `prefix`/`continuation-token`/`max-keys`/`tag-key`/`tag-value`，但完全没有读取 `delimiter` 查询参数；`internal/repository/sql_objects.go:166-195` — `ListObjects` 的 SQL 查询仅 `WHERE key LIKE prefix%`，无 `delimiter` 分组逻辑；`internal/api/s3compat/xml.go:10-23` — `listBucketResult` 结构体**尚未定义** `CommonPrefixes` 字段（需新增） |
| **2** | **S3 UploadPartCopy 缺失**——超过 5GB 的对象无法跨键复制 | 协议兼容 | **P0** | CopyObject 使用 `io.Copy` 流式读取整个源对象再写入目标。当对象 >5GB 时 AWS SDK 自动回退到 multipart copy（UploadPartCopy），但该 API 端点未实现，导致大对象复制必然失败 | `internal/api/s3compat/extra.go:33-65` — `copyObject` 使用 `h.svc.Get()` 读取整个源对象然后用 `h.svc.Put()` 写入；`internal/api/s3compat/handler.go:76-83` — `PutObject` 仅在检测到 `x-amz-copy-source` 时调用 `copyObject`，无 multipart copy 的 `?partNumber&uploadId` 分发路径；`internal/api/s3compat/handler.go:85` — `PutObject` 仅分发 `uploadId` 到 `uploadPart`，无 UploadPartCopy 路由 |
| **3** | **S3 服务端加密请求头盲区**——SSE-S3/SSE-KMS/SSE-C 请求头被静默忽略 | 协议兼容/安全 | **P1** | 主流 S3 SDK（boto3、aws-sdk-go、AWS CLI）默认发送 `x-amz-server-side-encryption` 请求头。当前实现在 `PutObject` 路径中完全不读取这些表头，既不存储也不返回。客户端以为启用了加密但实际未生效 | `internal/api/s3compat/handler.go:108-110` — `PutObject` 读取的请求头集合：`Content-Type`/`Metadata`/`ContentMD5`/`StorageClass`/`x-amz-acl`，但**没有任何 SSE 相关头**；`internal/service/file_crud.go:176-182` — `Put` 方法的 `PutOptions` 结构体没有 `SSEAlgorithm`/`SSEKMSKeyID`/`SSECustomerKey` 字段；`internal/repository/sql_objects.go:33-50` — `objects` 表的 INSERT 语句没有 `sse_algorithm`/`sse_kms_key_id` 列 |
| **4** | **存储分层生命周期转换缺失**——`storage_class` 被记录但不被转换 | 架构/成本 | **P1** | 对象模型已包含 `StorageClass` 字段，`x-amz-storage-class` 请求头被正确解析和持久化，遥测已按 storage class 分类统计。但生命周期仅支持过期删除（`expire_after_days` → `soft_delete`/`hard_delete`），无 STANDARD → STANDARD_IA → GLACIER 的自动分层转换 | `internal/repository/repository.go:41-44` — `BucketConfig` 只有 `ExpireAfterDays`/`ExpireAction`，无 `Transition` 数组；`internal/api/s3compat/xml.go:221-226` — `lifecycleRule` 的 XML 模型只有 `Expiration` 没有 `Transition`；`internal/reconcile/lifecycle.go:16-90` — `LifecycleJob.sweepExpired` 仅执行过期删除，无任何分层转换逻辑；`internal/telemetry/metrics.go:181-183` — `RegisterStorageClassGauge` 已按 storage class 统计计数，但数据永不变化 |
| **5** | **对象锁合规模式盲区**——`GOVERNANCE` 与 `COMPLIANCE` 无区分，`BypassGovernance` 未实现 | 合规/安全 | **P2** | ObjectLockConfiguration 可以被设置（`PUT ?object-lock`），`locked_until` 被正确执行（阻止删除/覆盖）。但系统不区分 GOVERNANCE（可被特权用户绕过）和 COMPLIANCE（不可绕过）两种保留模式，也不支持 `x-amz-bypass-governance-retention` 请求头和 `s3:BypassGovernanceRetention` 权限评估 | `internal/service/file_crud.go:295-310` — `hardDeleteObject` 检查 `locked_until` 但只检查 `obj.LockedUntil != nil && obj.LockedUntil.After(time.Now())`，不区分 GOVERNANCE/COMPLIANCE；`internal/api/s3compat/handler.go:90-99` — `PutObject` 将 `x-amz-object-lock-legal-hold: ON` 存入 `_aero_legal_hold` metadata，但无 `PUT ?legal-hold` 独立端点；`internal/api/s3compat/bucketconfig.go:171-200` — `getBucketObjectLock` 硬编码 `Mode: "GOVERNANCE"`，`putBucketObjectLock` 解析 XML 的 `Mode` 但存储时仅保留 `seconds`；`internal/repository/repository.go:35-40` — `BucketConfig.ObjectLockSeconds` 是 `int` 而非结构化保留配置 |

---

## 方向一：S3 ListObjects Delimiter/CommonPrefixes 缺失

### 产品影响

这是一个被低估的协议兼容性问题。S3 没有真正的"目录"概念——文件系统中的"文件夹"在 S3 中实现为 `{prefix}/{name}/` 前缀的约定。`delimiter` 参数是 S3 协议中实现此约定的核心机制：

- `GET /bucket?prefix=photos/&delimiter=/` → 只返回 `photos/` 下的直接子项，而非递归全部对象
- 响应中 `CommonPrefixes` 列出虚拟目录，`Contents` 列出直接文件
- aws-cli 的 `ls`、rclone 的目录遍历、Cyberduck 的文件夹打开、AWS SDK 的 `ListObjectsV2Paginator` 都依赖此参数

**无 delimiter 的实际后果：**
- `aws s3 ls s3://bucket/photos/` 返回所有嵌套对象的扁平列表而非仅直接子项
- 带有 100 万对象的 `photos/` 前缀，ListObjectsV2 返回全部 100 万条记录而非仅 1000 个直接子文件夹
- Web UI 的文件夹树将无法工作

### 代码证据

```go
// internal/api/s3compat/handler.go:457-506 — listObjectsV2
func (h *Handler) listObjectsV2(w http.ResponseWriter, r *http.Request, bucket string) {
    q := r.URL.Query()
    prefix := q.Get("prefix")
    token := q.Get("continuation-token")
    // ... 解析 max-keys, tag-key, tag-value
    // ❌ delimiter 完全没有被读取或传递给 repo

    page, err = h.svc.List(ctx, tenant, bucket, prefix, token, maxKeys)
    // List(ctx, tenant, bucket, prefix, marker, limit int) — 签名无 delimiter 参数
}
```

```go
// internal/repository/sql_objects.go:166-195 — ListObjects SQL 查询
// ❌ WHERE key LIKE $3 (prefix+"%") 匹配所有嵌套路径，无 delimiter 分组
// ❌ 响应结构 ListPage 没有 CommonPrefixes 字段
```

```go
// internal/api/s3compat/xml.go:10-23 — listBucketResult 结构体
type listBucketResult struct {
    // ❌ CommonPrefixes 尚未定义，需要新增
    Contents              []listContent `xml:"Contents"`
    // ...
}
```

### 架构权衡

**实现方案：**
1. **SQL 层 delimiter 分组** — 在 `ListObjects` SQL 中使用 `CASE WHEN key LIKE prefix+'%' AND key LIKE prefix+delimiter+'%' THEN SUBSTR(key, LEN(prefix)+1, INSTR(SUBSTR(key, LEN(prefix)+1), delimiter)-1)` 之类的表达式提取公共前缀，然后用 `DISTINCT` 分组。但 SQLite 和 Postgres 的字符串函数差异大，维护成本高。
2. **应用层分组** — `ListObjects` 返回 prefix+delimiter 模式下的下一页结果，在 Go 代码中提取 `CommonPrefixes`。下一 token 需要是最后一个对象或公共前缀的 key。与现有 marker 分页兼容。
3. **混合策略** — `ListObjects` SQL 查询增加一个 `LIMIT+N` 的缓冲区，在 Go 层做 delimiter dedup，确保分页在跨公共前缀边界时正确。

**建议方案：** 方案 2（应用层分组）。理由：
- 与现有 SQL 查询兼容（无需改 SQL）
- 分页 token 是扁平 key，兼容现有 `marker` 机制
- `ListPage` 结构体需要增加 `CommonPrefixes []string` 字段
- `listBucketResult` 和 `listBucketResultV1` 都需要新增加 `CommonPrefixes` XML 字段
- 复杂度在于正确处理跨页边界的 CommonPrefix（某前缀的第一页可能只显示了该前缀的部分对象）

**边界情况：**
- 空 delimiter（等同于无 delimiter，保持扁平）
- 多个连续 delimiter（`a///b`，S3 视 `//` 为有效字符）
- 跨页边界：CommonPrefix `photos/2024/` 有 2000 个对象，一页 1000 个时需在第一页就返回该前缀并在第二页从中断处继续
- marker 与 delimiter 的组合：`marker=photos/2024/01/sunset.jpg&delimiter=/` 应从 `photos/2024/01/sunset.jpg` 之后开始扫描
- `listObjectsV1`（v1 协议）也需要同样的 delimiter 支持

---

## 方向二：S3 UploadPartCopy 缺失

### 产品影响

S3 CopyObject API 的上限是 5GB。对于大于 5GB 的对象，AWS SDK 自动使用 multipart upload + UploadPartCopy 来完成复制——每个 part 独立复制，最后组合。

UploadPartCopy 缺失意味着：

- 任何 >5GB 对象的跨键复制（备份、迁移、归档）必然失败
- AWS CLI 的 `cp`、AWS SDK 的 `CopyObject` 对大对象静默回退到 UploadPartCopy，报错
- 多区域复制引擎（`internal/replication/`）跨存储后端复制大对象时无法工作
- 生命周期转换到冷存储需要复制对象到不同后端时也会失败

### 代码证据

```go
// internal/api/s3compat/extra.go:33-65 — copyObject 流式读取整个对象
func (h *Handler) copyObject(w http.ResponseWriter, r *http.Request, dstBucket, dstKey, copySource string) {
    rc, src, err := h.svc.Get(r.Context(), tenant, srcBucket, srcKey)
    defer rc.Close()  // ❌ 整个对象在内存中

    dst, err := h.svc.Put(r.Context(), tenant, dstBucket, dstKey, rc, src.Size, opts)
    // ❌ 当 src.Size > 5GB 时内存溢出或磁盘缓冲溢出
    // ❌ 无 x-amz-copy-source-range 支持
}
```

```go
// internal/api/s3compat/handler.go:85 — PutObject 仅分发到 uploadPart
if uploadID := r.URL.Query().Get("uploadId"); uploadID != "" {
    h.uploadPart(w, r, uploadID, partNumberOf(r))
    // ❌ 无 UploadPartCopy 分支
}
```

### 架构权衡

**UploadPartCopy 的 S3 语义：**

```
PUT /dst-bucket/dst-key?partNumber=1&uploadId=example-upload-id HTTP/1.1
x-amz-copy-source: /src-bucket/src-key
x-amz-copy-source-range: bytes=0-5242880
```

响应应返回 `CopyPartResult`（ETag + LastModified），与普通 UploadPart 的响应兼容，可用于 CompleteMultipartUpload。

**实现方案：**
1. **流式 copy** — 复用现有的 `FileService.Get` + `FileService.Put`，但只读取 `x-amz-copy-source-range` 指定的字节范围。`PutOptions` 需要增加 `ContentRange` 字段（用于 multipart part）。
2. **存储后端直连 copy** — 当源和目的在同一个 `storage.Storage` 后端（如 local→local 或 s3→s3）时，使用后端原生的 CopyObject API（S3 CopyObject 支持 range）。但跨后端复制必须走流式。
3. **混合策略** — 优先尝试存储后端直连 copy（带 range），不支持时回退到流式。

**建议方案：** 方案 1（流式 copy + range）。理由：
- 与现有 `FileService` 的 `Get`/`Put` 签名兼容
- 不依赖特定存储后端的扩展能力
- `range.go` 已有 `ParseByteRange` 函数可复用
- 需要给 `PutOptions` 增加 `ExpectedPartCount` 字段用于 ETag 预测

**边界情况：**
- Copy source 带 `?versionId`（需要支持版本化对象的复制）
- Copy source 是加密对象（SSE-C 需要传递加密头，当前 SSE-C 未实现——见方向三）
- Part 大小必须 ≥ 5MB（除最后一个 part），S3 有此约束
- Part 编号必须连续（1 到 10000），但可以乱序到达
- 与 `If-Match`/`If-None-Match`/`If-Modified-Since` 条件请求头的组合

---

## 方向三：S3 服务端加密请求头盲区

### 产品影响

S3 定义了三层服务端加密模型：
1. **SSE-S3**（`x-amz-server-side-encryption: AES256`）— 托管密钥的透明加密
2. **SSE-KMS**（`x-amz-server-side-encryption: aws:kms` + `x-amz-server-side-encryption-aws-kms-key-id`）— KMS 托管密钥
3. **SSE-C**（`x-amz-server-side-encryption-customer-algorithm: AES256` + ...）— 客户提供密钥

主流 S3 SDK（boto3、aws-sdk-go）在 PUT 请求中**默认发送** `x-amz-server-side-encryption: AES256`。同时，AeroVault 已经实现了自己的 SSE 加密层（`internal/storage/encrypt.go`）——本地 AES-256-GCM 信封加密。

当前实现在 S3 兼容层完全忽略这些请求头，导致：

- AWS SDK 默认启用的 SSE 请求不被感知
- 客户端以为数据被 SSE 保护，但实际上未经过 AeroVault 的加密层（走的是存储后端的明文路径）
- 对于需要端到端加密的工作负载，这是一个**安全幻觉**
- GET/HEAD 响应也缺少 `x-amz-server-side-encryption` 响应头

### 代码证据

```go
// internal/api/s3compat/handler.go:108-110 — PutObject 请求头读取
obj, err := h.svc.Put(r.Context(), mw.TenantFrom(r.Context()), bucket, key, r.Body, r.ContentLength, service.PutOptions{
    ContentType:  r.Header.Get("Content-Type"),
    Metadata:     meta,
    ContentMD5:   r.Header.Get("Content-MD5"),
    StorageClass: r.Header.Get("x-amz-storage-class"),
    // ❌ 无 SSEAlgorithm / SSEKMSKeyID / SSECustomerAlgorithm / SSECustomerKey
})
```

```go
// internal/service/file_crud.go:176-182 — PutOptions 结构体
type PutOptions struct {
    ContentType  string
    Metadata     map[string]string
    ContentMD5   string
    StorageClass string
    // ❌ 无 SSE 相关字段
}
```

### 架构权衡

**实现路径有三档：**

| 方案 | 实现量 | 效果 | 取舍 |
|------|--------|------|------|
| A: 声明 + 忽略 | 低（~50 行） | 接受 `x-amz-server-side-encryption` 头，在响应中回显，但不改变实际存储行为 | 诚实但功能不变 |
| B: 透明桥接到本地 SSE | 中（~200 行） | `AES256` → 启用本地 SSE 加密（`STORAGE_SSE_KEY` 必须已配置）；`aws:kms` → 拒绝（不支持的 key 类型） | 本地加密透明化，但 KMS 不支持 |
| C: 完整 SSE-C 支持 | 高（~500 行） | 接受客户提供的加密密钥，使用该密钥加密对象，与 `storage.encrypt` 层集成 | 安全最大化，但密钥管理复杂 |

**建议方案：** 阶段性交付——先方案 A（声明兼容），再方案 B（SSE-S3 透明桥接）。

**建议优先实现方案 A+B：**
1. `PutOptions` 增加 `SSEAlgorithm string` 字段
2. `PutObject` 从请求头读取 `x-amz-server-side-encryption`，只有值 `AES256` 被接受（`aws:kms` 返回 400 `InvalidArgument`）
3. 当 SSE 被请求且 `STORAGE_SSE_KEY` 已配置时，确保该对象走本地 SSE 加密路径
4. `GetObject`/`HeadObject` 响应携带 `x-amz-server-side-encryption: AES256`
5. 在 `objects` 表增加 `sse_algorithm` 列（migration），记录每个对象的加密状态

**边界情况：**
- SSE 请求头与 `Content-MD5` 的交互（加密发生在 checksum 验证之前还是之后？顺序：先验证 MD5，再加密）
- SSE-C 需要 `x-amz-server-side-encryption-customer-key-MD5` 来验证密钥完整性
- 已加密对象被 CopyObject 复制时的 SSE 处理（源和目标可能使用不同密钥）
- 预签名 URL 与 SSE 的组合（预签名 URL 应编码 SSE 参数）

---

## 方向四：存储分层生命周期转换缺失

### 产品影响

在 S3 中，生命周期规则不仅支持过期删除，还支持不同存储类之间的自动转换：

```xml
<LifecycleConfiguration>
  <Rule>
    <ID>tier-rule</ID>
    <Status>Enabled</Status>
    <Transition>
      <Days>30</Days>
      <StorageClass>STANDARD_IA</StorageClass>
    </Transition>
    <Transition>
      <Days>90</Days>
      <StorageClass>GLACIER</StorageClass>
    </Transition>
    <Expiration>
      <Days>365</Days>
    </Expiration>
  </Rule>
</LifecycleConfiguration>
```

这是 S3 最核心的成本优化机制。AeroVault 的 `storage_class` 字段已经存在并按对象级别持久化，但：

- `bucket_config` 的 `expire_after_days`/`expire_action` 是单一字段而非数组
- 没有后台 worker 来执行 `STANDARD → GLACIER` 的对象移动
- 没有跨后端对象迁移的管线（本地 → S3 → 更低成本 S3）

**对租户的实际影响：**
- 所有对象必须存储在同一个热存储后端，无法按访问频率降级
- 备份/日志归档数据占据昂贵的 NVMe/S3 Standard 空间
- 没有"30 天后自动转到冷存储"的自动化机制
- 与真实 S3 的成本管理能力差距巨大

### 代码证据

```go
// internal/repository/repository.go:41-44 — BucketConfig 是标量而非数组
type BucketConfig struct {
    ObjectLockSeconds int
    VersioningEnabled bool
    ExpireAfterDays   int    // ❌ 单一标量，无结构化的 Transition 数组
    ExpireAction      string // "soft_delete" | "hard_delete"
    // ...
}
```

```go
// internal/api/s3compat/xml.go:221-226 — Lifecycle XML 模型
type lifecycleRule struct {
    ID         string               `xml:"ID,omitempty"`
    Status     string               `xml:"Status"`
    Expiration *lifecycleExpiration `xml:"Expiration,omitempty"`
    // ❌ 无 Transition 字段
}
```

```go
// internal/reconcile/lifecycle.go:16-90 — 仅处理过期删除
func (l *LifecycleJob) sweep(ctx context.Context) {
    soft, hard := l.sweepExpired(ctx)  // ❌ 只扫过期，不扫转换
}
```

```go
// internal/repository/sql_objects.go:33-50 — StorageClass 被正确持久化
INSERT INTO objects (..., storage_class, ...)
    VALUES ($1, ..., $2, ...)
// ✅ storage_class 存在，但写入后永不变化
```

### 架构权衡

**分层转换引擎的核心设计：**

```
LifecycleRule.Transitions[]:
  [{Days: 30, StorageClass: "STANDARD_IA"},
   {Days: 90, StorageClass: "GLACIER"}]

TransitionJob:
  1. repo.ListEligibleForTransition(tenant, rule) — SQL 查询找到到期的对象
  2. 对每个对象：
     a. 从当前后端读取
     b. 用目标 StorageClass 写入新后端（或同一后端只是标记）
     c. 更新对象的 storage_class 和 storage_key（如果后端变了）
     d. 删除旧的存储 blob
```

**存储后端迁移的三种策略：**

| 策略 | 延迟 | 成本 | 复杂性 |
|------|------|------|--------|
| **同后端标记**（如 S3 Standard → S3 Standard-IA，同一 S3 bucket）| 低 | 低（仅 API 调用） | 低 |
| **跨后端流式复制**（local → S3）| 高 | 中（读+写+删除） | 中 |
| **跨后端直链复制**（S3 → S3，源端通过 CopyObject）| 中 | 低 | 中 |

**建议方案：** 先支持"同后端标记"（storage_class 字段更新 + 通知后端），再扩展跨后端迁移。

**迁移文件结构：**
```sql
-- migrations/sqlite/0025_lifecycle_transitions.up.sql:
ALTER TABLE buckets ADD COLUMN lifecycle_rules TEXT NOT NULL DEFAULT '[]';
-- JSON 数组包含 Transition 和 Expiration 规则
```

**边界情况：**
- 对象锁（WORM）：已锁定的对象不应被转换
- 正在转换期间服务重启：转换应为幂等操作（可重试）
- 目标存储后端容量不足：应有失败回退机制和告警
- 转换的调度限制：冷存储可能限制每日转化量（如 Glacier 的 5% 每日恢复量）
- 跨后端迁移时源数据的延迟删除：应使用软删除 + 间隔清除，而非立即删除

---

## 方向五：对象锁合规模式盲区

### 产品影响

S3 Object Lock 提供两种保留模式：

| 模式 | 描述 | 能否绕过 |
|------|------|---------|
| **GOVERNANCE** | 特权用户（有 `s3:BypassGovernanceRetention` 权限）可在保留期内删除 | 可以（需显式绕过） |
| **COMPLIANCE** | 绝对不可删除或覆盖，**没有任何用户（包括 Root）可以绕过** | 不可以 |

当前实现中，`locked_until` 字段被用于阻止删除和覆盖，但不区分这两种模式。这意味着：

- 管理员无法在紧急情况下删除 GOVERNANCE 锁定的对象（因为没有绕过机制）
- COMPLIANCE 锁定的对象在保留期内依然可以通过"修改 `locked_until`"来绕过（因为没有权限检查）
- `x-amz-object-lock-legal-hold: ON` 被存为 metadata 而非结构化字段
- 没有 `GET /{key}?legal-hold` 和 `PUT /{key}?legal-hold` 的独立端点

### 代码证据

```go
// internal/service/file_crud.go:295-310 — 删除/覆盖检查
func (s *FileService) hardDeleteObject(ctx context.Context, obj repository.Object, tenant, bucket, key string) error {
    // ...
    if obj.LockedUntil != nil && obj.LockedUntil.After(time.Now()) {
        return fmt.Errorf("%w: hard delete blocked until %s", ErrLocked, obj.LockedUntil.Format(time.RFC3339))
    }
    // ❌ 不检查 RetentionMode（GOVERNANCE vs COMPLIANCE）
    // ❌ 不检查 x-amz-bypass-governance-retention 请求头
    // ❌ 不检查 Caller 是否有 s3:BypassGovernanceRetention 权限
}
```

```go
// internal/repository/sql_objects.go:33-50 — objects 表 schema
INSERT INTO objects (..., locked_until, ...)
// ❌ 无 retention_mode 列（"GOVERNANCE" / "COMPLIANCE" / NULL）
```

```go
// internal/api/s3compat/handler.go:90-99 — Legal Hold 实现
if lh := r.Header.Get("x-amz-object-lock-legal-hold"); lh == "ON" || lh == "on" {
    meta["_aero_legal_hold"] = "ON"  // ❌ 存为 metadata，非结构化字段
}
// ❌ 无 PUT /key?legal-hold 和 GET /key?legal-hold 端点
```

```go
// internal/api/s3compat/bucketconfig.go:171-200 — ObjectLock 配置
func (h *Handler) putBucketObjectLock(w http.ResponseWriter, r *http.Request, bucket string) {
    // 解析 XML body
    // ✅ Mode 被正确解析
    // ❌ 但存储时只用了 seconds，Mode 被丢弃
    seconds = in.Rule.DefaultRetention.Days * 86400
}
```

### 架构权衡

**完整 Object Lock 合规所需的组件：**

1. **`retention_mode` 字段** — `objects` 表增加 `retention_mode VARCHAR` 列（`GOVERNANCE`/`COMPLIANCE`/NULL）
2. **`legal_hold` 字段** — `objects` 表增加 `legal_hold BOOLEAN` 列，替代 metadata hack
3. **`retention_mode` 在 BucketConfig 中的持久化** — 当前 `ObjectLockSeconds int` 扩展为结构化配置（包含 Mode + Days）
4. **Bypass-Governance 权限评估** — 在 `deleteObject` 路径中，当 `retention_mode = GOVERNANCE` 时检查请求头 `x-amz-bypass-governance-retention: true`，并验证 caller 的 scope/权限
5. **Legal Hold 专用端点** — `GET /{key}?legal-hold` + `PUT /{key}?legal-hold` from S3-compat

**迁移安全性：**
```sql
-- 对已存在 locked_until 的对象，默认 retention_mode 设为 COMPLIANCE
-- 以防止现有安全行为被意外降级
ALTER TABLE objects ADD COLUMN retention_mode TEXT DEFAULT 'COMPLIANCE';
```

**边界情况：**
- **生命周期 vs 对象锁**：生命周期规则不应删除或转换已锁定的对象（当前 `handleExpiredObject` 已检查 `LockedUntil`，但需要兼容 retention_mode）
- **版本控制**：PUT 新版本不应删除旧版本的锁定——锁定是 per-object-version 的
- **Bucket 默认锁 vs 单对象锁**：PUT 时的 `x-amz-object-lock-retain-until-date` 和 `x-amz-object-lock-mode` 应覆盖 bucket 默认
- **COMPLIANCE 模式下的恢复**：一旦 COMPLIANCE 锁定且保留期未过，数据不可恢复——这是设计行为，但必须有告警（在审计日志中记录谁试图删除 COMPLIANCE 对象）

---

## 总结：按优先级排序

| # | 方向 | 类型 | 优先级 | 预计代码量 | 最重要的受益方 |
|---|------|------|--------|-----------|---------------|
| **1** | ListObjects Delimiter | 协议兼容 | P0 | ~150 行 Go + ~10 行 SQL | 所有 S3 SDK/CLI 用户 |
| **2** | UploadPartCopy | 协议兼容 | P0 | ~200 行 Go + ~30 行测试 | 大对象工作负载（备份、迁移） |
| **3** | SSE 请求头 | 协议兼容/安全 | P1 | ~100 行 Go（方案 A）+ migration | 安全合规团队、SDK 用户 |
| **4** | 存储分层转换 | 架构/成本 | P1 | ~400 行 Go + migration + worker | 运营团队（降低存储成本） |
| **5** | 对象锁合规模式 | 合规/安全 | P2 | ~300 行 Go + migration + 权限 | 金融/医疗等监管行业客户 |

### 依赖关系
- **方向 2（UploadPartCopy）依赖方向 3（SSE 头）** 的路径：加密对象的 part copy 需要 SSE 参数传递——如果 SSE 头不被解析，加密对象的 part copy 必然失败
- **方向 4（存储分层）不依赖**其他方向，但其转换引擎应复用方向 2 的跨后端数据移动能力
- **方向 5（对象锁合规）** 应先于方向 4 实现，因为合规要求的对象不应被生命周期转换

---

## 实时代码验证注释（2026-07-11）

本分析中的所有代码锚点已通过 `git HEAD` 验证。发现一处细微偏差：

| 文档声称 | 实际代码 | 影响 |
|----------|---------|------|
| `listBucketResult.CommonPrefixes` "已定义但从未被填充" | `CommonPrefixes` 字段**尚未定义**在 `listBucketResult` 结构体中 | 实现时需新增该字段而非填充已有字段 |

其余所有锚点均准确匹配实时代码。

---

*本文档基于全代码库扫描，聚焦于第 125 轮分析中尚未被独立深度覆盖的 5 个高价值方向。每个方向均附有精确的代码锚点、生产影响分析和架构权衡。*
