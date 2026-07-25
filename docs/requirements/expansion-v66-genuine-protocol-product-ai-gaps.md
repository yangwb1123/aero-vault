# AeroVault 高价值扩展方向 — 协议完备性、产品缺口与 AI 原生边界

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全局代码扫描（~50K 行 Go 源码，65 份既有分析去重验证）  
> **去重策略：** 逐方向全文 grep 检查 `docs/requirements/` 下全部 65 份既有分析文档，仅选取**零实质性架构分析**的方向  
> **日期：** 2026-07-10  

---

## 审阅：前 65 轮分析覆盖边界

前 65 轮分析已系统性覆盖的核心领域包括：

- **存储生态**：Azure Blob/GCP 后端、存储健康管理、桶级默认加密、SSE-C 客户密钥、连接池与优雅降级（v10, v65）
- **S3 协议**：桶策略引擎、通知 XML 格式、CORS、Logging、StorageClass 分层、Lifecycle Transition、CopyObject、Batch Operations 框架（v8, v23, v25, v34, v42, v56, v57, v58）
- **AI/RAG 管线**：全链路文本提取/分块/嵌入/检索/生成/Agent（v13, v22, v31, v41, v53, v59, v60, v61, v63）
- **多租户与鉴权**：JWT、API Key、SigV4、Scope、Policy、ACL、mTLS、审计日志（v5, v8, v15, v26, v27, v29, v32, v55, v64）
- **事件通知**：SQS/SNS/Lambda 传输、webhook 过滤路由、payload 模板化（v64, v65）
- **对象锁/WORM**：Legal Hold、Retention 到期、锁模式治理（expansion-directions, v16, v23, v25, v30）
- **分布式与水平扩展**：集群单例、复制多目标、冲突处理（v28, v35, v44, v45, v55, v57, v65）
- **数据完整性**：孤儿 GC、Scrub、Retention、幂等性、崩溃安全（v5, v15, v17, v21, v23, v28, v49, v51）
- **内容去重与 CAS**：不可变存储、内容寻址模式（v25 方向四）
- **运维生产硬化**：指标、告警、追踪、备份恢复（v10, v27, v34, v38, v39, v46, v47, v60）

**核心发现：** 经过 65 轮分析，功能层面的"有没有"已高度饱和，但仍有几个**交叉领域缺口**和**协议语义完备性缺口**未被触及。本期聚焦的 5 个方向均处于现有功能矩阵的空白区域。

---

## 本期方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 | 既有分析覆盖 |
|---|------|------|--------|---------|---------|-------------|
| **1** | **S3 版本化删除标记与 Version-ID 感知删除** | 协议完备性/S3 兼容 | **P0 — 行为 bug** | 版本化桶中 DELETE 直接删除 blob 而非创建 DeleteMarker；无法按 versionId 删除；GET 到 DeleteMarker 应返回 404+header | `internal/api/s3compat/handler.go:247-260`（DeleteObject 无视版本化，总是 hard delete）；`internal/api/s3compat/bucketconfig.go:205-209`（注释明确"无 DeleteMarker 实体"） | ❌ 仅 v15/v42/v51/v61 各 1 行过路表格提及"缺少 DeleteMarker header"，**零架构设计与实现分析** |
| **2** | **Token 门控匿名上传门户** | 产品功能/鉴权 | **P1 — 新场景** | 外部用户需要上传文件到指定路径时，当前必须持有完整 API Key 或 JWT；缺少轻量级、短时效、仅写权限的上传令牌 | `internal/auth/auth.go`（无 per-scope 临时 token 机制）；`internal/auth/auth_middleware.go`（Bearer 解析无 extract token 路径）；`internal/middleware/middleware.go`（Tenant 上下文无匿名写入标记） | ❌ **零实质性分析**（grep 全文 65 份文档，`anonymous.*upload` / `public.*upload` / `upload.*portal` / `token.*gate` / `file.*drop` / `scoped.*upload` → 0 命中） |
| **3** | **S3 Object Lock 保留模式（Governance / Compliance）与 Bypass 语义** | 协议完备性/合规 | **P1 — 合规缺口** | 当前 `locked_until` 单一时间戳无法区分 Governance（可绕过）和 Compliance（不可绕过）模式；缺少 `s3:BypassGovernanceRetention` IAM Action 校验 | `internal/service/file_crud.go:295-302`（`hardDeleteObject` 仅检查 `LockedUntil`+`_aero_legal_hold`）；`internal/repository/repository.go:21-38`（`Object` 无 `RetentionMode` 字段）；`internal/auth/policy.go:37-62`（无 `BypassGovernanceRetention` action） | ❌ v16 方向五提及"锁模式治理"概念但聚焦 S3 子资源 API 完整性；**Governance/Compliance 模式区分与 Bypass 语义的架构设计从未被实质性分析** |
| **4** | **S3 Multipart 预签名 URL（InitMultipart / UploadPart / Complete / Abort）** | 协议完备性 | **P2 — 大文件上传断裂** | Storage 接口仅暴露 `PresignGet` / `PresignPut`；浏览器分片上传必须走服务端中转，无法实现客户端直传 S3 | `internal/storage/storage.go:121-125`（仅 `PresignGet` + `PresignPut`）；`internal/storage/local.go`、`s3.go`、`oss.go`、`cos.go`（无任何 presign-multipart 方法）；`internal/api/rest/handler.go:45-50`（`PostForm` 服务端转收，不支持客户端直传） | ❌ **零实质性分析**（grep 全量 docs，`presign.*multipart` / `upload.*part.*presign` / `UploadPart.*URL` → 0 命中） |
| **5** | **多模态 AI 索引管线（图像/PDF 文字识别 / 音频转录 / 视频理解）** | AI 管线扩展 | **P2 — AI-native 定位缺口** | 当前 AI 管线仅支持文本提取→分块→嵌入；图像、PDF 扫描件、音频、视频等非文本内容被静默跳过；与"AI-native 文件平台"定位严重不符 | `internal/ai/extractor.go:32-90`（仅支持 `.txt`, `.md`, `.json`, `.csv`, `.html`, `.xml`, `.yaml` 等纯文本格式）；`internal/ai/indexer.go:90-105`（`indexObject` 中 `text == ""` 走 `IncIndexerSkip(unsupported)` 跳过）；`internal/ai/chunker.go`（仅处理字符串文本，无图像/音频/视频分支） | ❌ **零实质性分析**（grep 全量 65 份文档，`multimodal` / `multi.modal` / `audio` / `video` / `OCR` / `speech` / `transcri` → 仅 v65 的 `extractor_remote.go` 注释行路过提及"Whisper / PaddleOCR"作为远端提取器示例，**零架构设计**） |

---

## 方向一：S3 版本化删除标记与 Version-ID 感知删除

### 当前状态

S3 兼容层实现了**创建版本**的正确语义（PUT 到已版本化桶创建新版本行），但 DELETE 语义完全不符合 S3 标准：

```go
// internal/api/s3compat/handler.go:247-260
func (h *Handler) DeleteObject(w http.ResponseWriter, r *http.Request) {
    // ...
    // ❌ 无视桶是否已启用版本化，始终 hard delete
    if err := h.svc.Delete(r.Context(), ..., bucket, key, true); err != nil {
        // ...
    }
    // ❌ 响应头缺少 x-amz-delete-marker: true
    w.WriteHeader(http.StatusNoContent)
    // ❌ 无 ?versionId 查询参数处理
}
```

内部模型明确注释无 DeleteMarker 概念：

```go
// internal/api/s3compat/bucketconfig.go:205-209
// deleted_at on the prior row and has no distinct delete-marker entity, so every
// stored version row is reported as a <Version>; the newest ... carries IsLatest=true.
//                     ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
```

S3 标准行为对比：

| 操作 | AWS S3 行为 | AeroVault 当前行为 |
|------|------------|-------------------|
| DELETE 版本化桶对象 | 创建 DeleteMarker（新版本行，`IsLatest=true`），原版本保留 | 实际删除 storage blob + 软删除或硬删除行 |
| DELETE `?versionId=<id>` | 永久删除指定版本（不可撤回） | ❌ 不支持（忽略 versionId 参数） |
| GET 到 DeleteMarker | 返回 404 + `x-amz-delete-marker: true` | ❌ 返回对象内容（如果对象未硬删）或 404（如果已硬删）|
| GET `?versionId=<DeleteMarkerID>` | 返回 405 Method Not Allowed | ❌ 返回对象内容 |
| HEAD 到 DeleteMarker | 返回 404 + `x-amz-delete-marker: true` | 同上 |
| `?versions` 列表 | DeleteMarker 出现在 `<DeleteMarker>` 元素而非 `<Version>` 元素 | ❌ 所有版本行都作为 `<Version>` 返回 |
| `x-amz-version-id` 响应头 | 始终返回（新对象/版本/DeleteMarker）| ✅ 部分实现（GET/HEAD）|
| `x-amz-delete-marker` 响应头 | DELETE 返回 `true` | ❌ 缺失 |

### 代码锚点

| 文件 | 影响 |
|------|------|
| `internal/api/s3compat/handler.go:247-260` | `DeleteObject` 需要改写：版本化桶→创建 DeleteMarker；`?versionId`→永久删除特定版本 |
| `internal/api/s3compat/bucketconfig.go:205-260` | `listObjectVersions` 需要区分 DeleteMarker 和 Version 元素 |
| `internal/api/s3compat/handler.go:115-165` | `GetObject` 需要检查 DeleteMarker → 404 + `x-amz-delete-marker: true` |
| `internal/api/s3compat/handler.go:195-230` | `HeadObject` 同上 |
| `internal/api/s3compat/xml.go:248-290` | `listVersionsResult` 需要新增 `<DeleteMarker>` 元素 |
| `internal/service/file.go` | 新增 `DeleteToMarker` / `DeleteVersionByID` / `IsDeleteMarker` 方法 |
| `internal/service/file_crud.go:287-340` | `hardDeleteObject` / `softDeleteObject` 需要保留版本化语义 |
| `internal/repository/repository.go:21-38` | `Object` 结构体新增 `IsDeleteMarker bool` 字段 |
| `internal/repository/sql_objects.go` | 插入 DeleteMarker 行；按 versionId 查询/删除 |
| `internal/repository/migrations/{sqlite,postgres}/0025_delete_marker.up.sql` | 新增 migration：`ALTER TABLE objects ADD COLUMN is_delete_marker INTEGER DEFAULT 0` |
| `internal/reconcile/retention.go` | DeleteMarker 的 GC 规则：版本化桶中不应 GC 被 DeleteMarker 覆盖的版本 |

### 为什么需要

1. **S3 协议兼容性阻断级缺陷。** 版本控制是 S3 最核心的功能之一，而 DeleteObject 在版本化桶中的行为是 S3 协议的基本契约。当前实现会让 AWS SDK 和 S3 客户端的行为完全不可预测——想"删除"一个对象的结果是数据永久丢失。这不是功能缺失，而是行为错误。

2. **数据安全风险。** 使用版本控制的目的是防止误删和覆盖。当前实现下，DELETE 操作**实际删除了数据**——版本控制的根本目的被破坏了。

3. **合规场景不可用。** 需要版本控制来满足合规要求的场景（如 SEC 17a-4 电子记录保留），当前实现无法通过审计。

### 架构建议

```go
// Object 模型扩展
type Object struct {
    // ... 现有字段
    IsDeleteMarker bool       // 新增：是否为 DeleteMarker（版本化桶专用）
    RetentionMode  string     // "" | "GOVERNANCE" | "COMPLIANCE"（见方向三）
}

// DeleteObject 新语义
func (h *Handler) DeleteObject(w, r, bucket, key) {
    if vid := r.URL.Query().Get("versionId"); vid != "" {
        // DELETE ?versionId=<id> — 永久删除特定版本
        // 1. 校验 retention/legal-hold
        // 2. 硬删除 storage blob + 行
        // 3. 返回 204
        return
    }
    // 检查版本化
    bcfg := h.svc.GetBucketConfig(ctx, tenant, bucket)
    if bcfg.Versioning {
        // 创建 DeleteMarker：插入 is_delete_marker=true 的新行
        // 不删除任何 storage blob
        // 响应 x-amz-delete-marker: true
        return
    }
    // 非版本化桶：软删除（保持现有行为）
    h.svc.Delete(ctx, tenant, bucket, key, false)
}

// GetObject 新检查
func (h *Handler) GetObject(w, r, bucket, key) {
    if vid := r.URL.Query().Get("versionId"); vid != "" {
        obj := repo.GetObjectVersion(tenant, bucket, key, vid)
        if obj.IsDeleteMarker {
            // 405 Method Not Allowed
            w.Header().Set("x-amz-delete-marker", "true")
            w.WriteHeader(http.StatusMethodNotAllowed)
            return
        }
    } else {
        // 获取当前最新版本
        obj := repo.GetObject(tenant, bucket, key)
        if obj.IsDeleteMarker {
            // 404 + x-amz-delete-marker: true
            w.Header().Set("x-amz-delete-marker", "true")
            w.WriteHeader(http.StatusNotFound)
            return
        }
    }
}
```

### 边界情况

| Edge Case | 当前行为 | 目标行为 |
|-----------|---------|---------|
| 无版本化桶 × DELETE | 硬删除 | 保持不变（软/硬取决于参数） |
| 版本化桶 × DELETE × 对象不存在 | 404 | 创建 DeleteMarker（S3 行为） |
| 版本化桶 × DELETE 已经 DeleteMarked 的对象 | 404 | 创建新的 DeleteMarker |
| 版本化桶 × DELETE `?versionId` 指向 DeleteMarker | 不支持 | 返回 400 `InvalidArgument` |
| 版本化桶 × GET 活动对象 → DELETE → GET | 404（已硬删）| 404 + `x-amz-delete-marker: true` |
| DeleteMarker 的 GC | 无 | DeleteMarker 应在 `ExpireAfterDays` 后自动清理 |
| 版本化桶的 ListObjects（非 `?versions`）| ✅ 正常列出 | 不应列出 DeleteMarker（已符合） |

---

## 方向二：Token 门控匿名上传门户

### 当前状态

当前鉴权系统只支持两种模式：

1. **完整认证**（API Key / JWT / SigV4）：对 `/v1/files/*` 和 `/s3/*` 的所有操作都需要 `read`/`write`/`admin` scope
2. **匿名公开读**（`AUTH_ANONYMOUS_PUBLIC_READ=true`）：允许未认证的 GET/HEAD 请求，但**写操作从未对匿名用户开放**

```go
// internal/auth/auth.go
// 仅支持 Bearer JWT、X-Api-Key、SigV4
// 无"匿名写"概念

// internal/middleware/middleware.go
// Auth middleware 要么通过（有有效凭证），要么拒绝（401）
// 无"部分匿名"路径——无法授予匿名用户仅写特定前缀的权限
```

**缺失的使用场景：**

| 场景 | 描述 | 当前替代方案 |
|------|------|------------|
| HR 文件收集 | 外部候选人上传简历到 `applications/{candidate_id}/` | ❌ 必须注册 API Key |
| 客户文档上传 | 客户上传合同/发票到 `uploads/{customer_id}/` | ❌ 必须集成 SDK |
| 匿名反馈 | 用户上传截图/日志到 `feedback/` | ❌ 无法实现 |
| 协作共享 | 将文件暂存区共享给外部合作者写入 | ❌ 必须创建完整用户 |

### 代码锚点

| 文件 | 影响 |
|------|------|
| `internal/auth/auth.go` | 新增 `TokenUpload` 类型（短时效、单前缀、仅写权限）|
| `internal/auth/auth_middleware.go` | 新增 Upload Token 验证路径（Bearer token 可映射为匿名上传会话）|
| `internal/middleware/middleware.go` | 新增 `AnonymousWriter` 租户上下文标记 |
| `internal/api/rest/handler.go:45-100` | PUT/POST 路径增加匿名上传允许检查 |
| `internal/api/rest/router.go` | 新增 `POST /v1/upload-token` 生成上传令牌（需 admin scope）|
| `internal/config/config.go` | 新增 `UPLOAD_TOKEN_MAX_SIZE` / `UPLOAD_TOKEN_TTL` 配置 |
| `internal/repository/repository.go` | 新增 `UploadToken` 模型（hash, tenant, bucket, prefix, max_bytes, expires_at）|
| `internal/repository/migrations/{sqlite,postgres}/0025_upload_tokens.up.sql` | 新增 `upload_tokens` 表 |

### 为什么需要

1. **产品需求直接。** 这是被用户问得最多的功能之一——"如何让外部用户上传文件？"没有答案就是产品硬伤。

2. **零信任场景的核心组件。** 即使在同一个组织内，有时也需要提供一个"扔文件进来"的入口，而不暴露完整的 API 访问权限。

3. **差异化竞争优势。** AWS S3 原生没有简单的"可分享上传链接"功能（需要 PreSigned URL + 手动管理），AeroVault 可以内置这个能力。

### 架构建议

```http
# 管理员创建上传令牌
POST /v1/admin/upload-tokens
Authorization: Bearer <admin-key>
{
  "path": "uploads/{tenant_id}/",     # 允许上传的前缀
  "max_files": 10,                     # 最大文件数
  "max_size_bytes": 104857600,         # 每个文件最大 100MB
  "expires_in": "24h"                  # 24 小时后过期
}
→ 201
{
  "token": "avup_b1a3f5...",
  "url": "https://aero.example.com/v1/upload/b1a3f5",
  "expires_at": "2026-07-11T10:00:00Z"
}

# 匿名用户上传
PUT https://aero.example.com/v1/upload/b1a3f5/resume.pdf
Content-Type: application/pdf

<binary>
→ 201
{ "key": "uploads/{tenant_id}/resume.pdf", "size": 12345 }
```

**令牌属性：**

```
UploadToken {
    TokenHash    string     // sha256(token)
    TenantID     string     // 归属租户
    Bucket       string     // 目标桶
    PathPrefix   string     // 允许上传的 key 前缀
    MaxFiles     int        // 最多文件数（0=无限）
    MaxSizeBytes int64      // 单文件大小上限
    CurrentFiles int        // 已上传文件数
    CurrentBytes int64      // 已上传总字节
    CreatedAt    time.Time
    ExpiresAt    time.Time
    LastUsedAt   *time.Time
}
```

### 边界情况

| Edge Case | 行为 |
|-----------|------|
| 令牌过期 | 返回 401 `TokenExpired` |
| 超过最大文件数 | 返回 403 `UploadQuotaExceeded` |
| 文件超过大小上限 | 返回 413 `PayloadTooLarge` |
| 路径不匹配前缀 | 返回 403 `PathNotAllowed` |
| 令牌被撤销 | 管理端可删除令牌行，后续请求返回 401 |
| 并发上传计数 | 使用原子增减（`UPDATE upload_tokens SET current_files = current_files + 1 WHERE ...`）|
| 续期 | 管理端可 POST 延长过期时间 |

---

## 方向三：S3 Object Lock 保留模式（Governance / Compliance）与 Bypass 语义

### 当前状态

当前对象锁实现使用单一的 `locked_until` 时间戳：

```go
// internal/service/file_crud.go:295-305
func (s *FileService) hardDeleteObject(ctx, obj, tenant, bucket, key) error {
    if obj.LockedUntil != nil && obj.LockedUntil.After(time.Now()) {
        return fmt.Errorf("%w: hard delete blocked until %s", ErrLocked, ...)
    }
    if obj.Metadata["_aero_legal_hold"] == "ON" {
        return fmt.Errorf("%w: object is under legal hold", ErrLocked)
    }
    // ... 真正删除
}
```

**问题：** S3 Object Lock 定义了两种保留模式，每种有完全不同的绕过语义：

| 模式 | 描述 | 能否绕过 | 绕过条件 |
|------|------|---------|---------|
| `GOVERNANCE` | 治理模式——保留期内阻止修改/删除，但可由授权用户绕过 | ✅ 可绕过 | 用户持有 `s3:BypassGovernanceRetention` 权限且请求携带 `x-amz-bypass-governance-retention: true` |
| `COMPLIANCE` | 合规模式——保留期内绝对不可修改/删除，**没有任何用户可绕过** | ❌ 不可绕过 | 无 |
| Legal Hold | 法律封存——独立于保留期，无限期阻止删除 | ✅ 可移除 | 用户持有 `s3:PutObjectLegalHold` 权限可开关 |

当前实现将 `locked_until` 当作 COMPLIANCE 模式处理（不可绕过）——但这既不是正确的 Governance 行为（缺少 bypass 路径），也不是完整的 Compliance 实现（缺少 `RetentionMode` 字段）。

### 代码锚点

| 文件 | 影响 |
|------|------|
| `internal/repository/repository.go:21-38` | `Object` 新增 `RetentionMode string`（"" / "GOVERNANCE" / "COMPLIANCE"）|
| `internal/repository/sql_objects.go` | 新增 `retention_mode` 列；`SetRetention` 方法增加 mode 参数 |
| `internal/service/file.go` | 新增 `LockObjectWithMode` / `BypassRetention` 方法 |
| `internal/service/file_crud.go:295-305` | `hardDeleteObject` 增加 Governance 模式检查路径 |
| `internal/auth/policy.go:37-62` | 新增 `BypassGovernanceRetention` IAM Action |
| `internal/api/s3compat/handler.go:880-900` | 新增 `putObjectRetention` / `getObjectRetention` handler |
| `internal/api/s3compat/xml.go` | 新增 `retentionConfiguration` XML 类型（`<RetentionPeriod>` + `<Mode>`）|
| `internal/api/s3compat/handler.go:247-260` | `DeleteObject` 增加 Bypass header 校验 |
| `internal/repository/migrations/{sqlite,postgres}/0025_retention_mode.up.sql` | `ALTER TABLE objects ADD COLUMN retention_mode TEXT DEFAULT ''` |

### 为什么需要

1. **S3 协议合规性。** S3 Object Lock 的核心契约就是两种模式的区分。没有这个区分，对象锁在实际合规场景中无法使用——要么所有锁都可以绕过（不安全），要么所有锁都不能绕过（运维故障）。

2. **企业合规落地。** 金融机构受 SEC 17a-4 约束时 MUST 使用 COMPLIANCE 模式。内部数据治理场景则常用 GOVERNANCE 模式以便管理员可以按需清理。没有模式选择就无法适配不同合规要求。

3. **可用性。** 当前所有锁都是 COMPLIANCE 模式（不可绕过）。如果某天配置错误导致一个文件被错误锁定，没有任何恢复路径。GOVERNANCE 模式至少给了授权管理员一个逃生舱。

### 架构建议

```go
// 对象模型扩展
type Object struct {
    LockedUntil   *time.Time
    LegalHold     bool
    RetentionMode string // "" | "GOVERNANCE" | "COMPLIANCE"
}

// Retention 设置
func (h *Handler) putObjectRetention(w, r, bucket, key) {
    var cfg retentionConfiguration
    xml.NewDecoder(r.Body).Decode(&cfg)
    
    mode := cfg.Mode // "GOVERNANCE" | "COMPLIANCE"
    until := parseRetainUntil(cfg)
    
    h.svc.SetObjectRetention(ctx, tenant, bucket, key, mode, until)
    w.WriteHeader(http.StatusOK)
}

// 删除检查
func (s *FileService) hardDeleteObject(ctx, obj, tenant, bucket, key) error {
    if obj.LegalHold {
        return ErrLocked // LegalHold 无法绕过
    }
    if obj.LockedUntil != nil && obj.LockedUntil.After(time.Now()) {
        if obj.RetentionMode == "GOVERNANCE" {
            // 检查请求是否携带 bypass 头
            if bypass := ctx.Value("x-amz-bypass-governance-retention"); bypass == "true" {
                // 检查调用者是否有 s3:BypassGovernanceRetention 权限
                if hasBypassPermission(ctx) {
                    return nil // 允许绕过
                }
                return ErrAccessDenied
            }
            return ErrLocked
        }
        // COMPLIANCE: 不可绕过
        return ErrLocked
    }
    // ... 真正删除
}

// IAM Action 扩展
var s3Actions = map[string]string{
    "s3:GetObject":              "s3:GetObject",
    "s3:PutObject":              "s3:PutObject",
    "s3:DeleteObject":           "s3:DeleteObject",
    "s3:BypassGovernanceRetention": "s3:BypassGovernanceRetention", // 新增
    "s3:PutObjectRetention":     "s3:PutObjectRetention",           // 新增
    "s3:PutObjectLegalHold":     "s3:PutObjectLegalHold",           // 新增
}
```

### 边界情况

| Edge Case | 行为 |
|-----------|------|
| 已锁定对象设置更高保留期 | 允许（只能延长不能缩短）|
| 已锁定对象设置更低保留期 | 拒绝（S3 规范要求）|
| COMPLIANCE 模式下尝试绕过 | 拒绝（即使有 Bypass 权限）|
| GOVERNANCE 模式 + 无 bypass 头 | 拒绝 |
| GOVERNANCE 模式 + bypass 头 + 无权限 | 拒绝（403 AccessDenied）|
| 锁定期间 PUT 覆盖 | 拒绝（当前已实现，需确认）|
| 锁定期间删除 `?versionId` | 拒绝（只有 Governance + bypass 可绕过）|

---

## 方向四：S3 Multipart 预签名 URL

### 当前状态

Storage 接口仅定义了整体对象上传/下载的预签名：

```go
// internal/storage/storage.go:121-125
type Storage interface {
    PresignGet(ctx context.Context, key string, expiry time.Duration) (string, error)
    PresignPut(ctx context.Context, key string, expiry time.Duration) (string, error)
    // ❌ 没有 PresignInitMultipart / PresignUploadPart / PresignCompleteMultipart / PresignAbortMultipart
}
```

REST API 提供 `POST /v1/files/{key}/presign` 但同样只支持整体 PUT：

```go
// internal/api/rest/handler.go:300-330
func (h *Handler) Presign(w, r) {
    // 只支持 presign GET / PUT
}
```

### 缺失的场景

| 场景 | 描述 | 当前状态 |
|------|------|---------|
| 浏览器大文件上传 | >100MB 文件通过浏览器上传 | ❌ 必须经过服务端中转（`PostForm` 将整个文件读入内存）|
| 客户端直传 S3 | 移动端/Web 端直接上传到 S3 后端 | ❌ 无法生成 UploadPart URL |
| 分片上传进度条 | 前端显示每片上传进度 | ❌ 无法分片客户端直传 |
| 离线上传 | 生成有效期为 7 天的 upload part URL | ❌ 不支持 |

### 代码锚点

| 文件 | 影响 |
|------|------|
| `internal/storage/storage.go:121-125` | Storage 接口新增 `PresignInitMultipart` / `PresignUploadPart` / `PresignCompleteMultipart` / `PresignAbortMultipart` |
| `internal/storage/local.go` | local 后端实现：通过 SignKey 签名 multipart 操作参数 |
| `internal/storage/s3.go` | S3 后端：使用 AWS SDK `PresignClient` 的 `PresignUploadPart` 等 |
| `internal/storage/oss.go` | OSS 后端：使用阿里云 SDK 的签名方法 |
| `internal/storage/cos.go` | COS 后端：使用腾讯云 SDK 的预签名 |
| `internal/api/rest/handler.go:300-330` | 新增 `presignMultipart` / `presignUploadPart` / `presignComplete` / `presignAbort` |
| `internal/api/rest/router.go` | 新增路由：`POST /v1/multipart/presign-init` `POST /v1/multipart/presign-part/{uploadID}/{partNumber}` `POST /v1/multipart/presign-complete/{uploadID}` `POST /v1/multipart/presign-abort/{uploadID}` |
| `internal/service/file_multipart.go` | 新增 `PresignInitMultipart` / `PresignUploadPart` 等方法 |
| `internal/auth/policy.go` | 新增 `s3:UploadPart` 权限映射 |

### 为什么需要

1. **浏览器上传场景断裂。** 当前前端只能通过 `PostForm` 上传——这在浏览器中意味着整个文件被读取到服务器内存。对于 >100MB 的文件，这既不安全也不可行。

2. **S3 协议一致性。** AWS SDK 的 multipart upload 支持通过 presigned URL 上传单个 part。不支持此功能意味着标准 S3 客户端无法用 presigned URL 上传大文件到 AeroVault。

3. **离线/异步工作流。** 预签名 part URL 可以设置较长有效期，支持断点续传场景。当前服务端中转架构无法支持。

### 架构建议

```http
# 1. 初始化分片上传并获取各分片预签名 URL
POST /v1/multipart/presign-init
{ "key": "large-file.zip", "part_count": 5, "expiry": "1h" }
→ 201
{
  "upload_id": "abc123",
  "parts": [
    { "number": 1, "url": "https://.../part=1&uploadId=abc123&sig=...", "expires_at": "..." },
    { "number": 2, "url": "https://.../part=2&uploadId=abc123&sig=...", ... },
    ...
  ]
}

# 2. 客户端直接 PUT 到预签名 URL（不经过 AeroVault 服务器）
PUT https://.../part=1&uploadId=abc123&sig=...
Content-Range: bytes 0-9999999/50000000
<binary part 1>

→ 200
{ "etag": "etag1", "part_number": 1 }

# 3. 完成分片上传（传递所有 etag）
POST /v1/multipart/presign-complete/abc123
{
  "parts": [
    { "number": 1, "etag": "etag1" },
    { "number": 2, "etag": "etag2" },
    ...
  ]
}
→ 200
{ "key": "large-file.zip", "size": 50000000, "etag": "..." }
```

### 边界情况

| Edge Case | 行为 |
|-----------|------|
| 部分 URL 已过期 | 返回各 part 单独过期时间，前端可重新请求过期 part |
| 客户端未上传所有 part 就 complete | 失败——S3 协议要求所有 part 必须在 complete 前上传 |
| 预签名 URL 上传不匹配的 part number | 存储后端应验证 part number 与签名一致 |
| 上传超出 Content-Range 的 payload | 截断或拒绝（取决于后端）|
| 预签名 abort | 使用 presign-abort URL 可随时取消 multipart 上传 |
| 非 local 后端的中转路径 | 对于 local 后端，presign 通过 HMAC 签名参数实现；对于 S3/OSS/COS，通过 SDK 的 PresignClient 生成 |

---

## 方向五：多模态 AI 索引管线

### 当前状态

当前 AI 管线仅支持纯文本文件：

```go
// internal/ai/extractor.go:32-90
// textExtensions 是 supportedExt() 使用的集合
var textExtensions = map[string]bool{
    ".txt": true, ".md": true, ".json": true, ".csv": true,
    ".html": true, ".xml": true, ".yaml": true, ".yml": true,
    ".log":  true, ".sh": true, ".go": true, ".py": true,
    ".js":   true, ".ts": true, ".java": true, ".c": true,
    ".h":    true, ".cpp": true, ".hpp": true,
}
```

非文本文件被静默跳过：

```go
// internal/ai/indexer.go:90-105
if text == "" {
    telemetry.IncIndexerSkip(ctx, "unsupported")
    return nil // 跳过
}
```

**完全缺失的 AI 索引能力：**

| 内容类型 | 当前状态 | 影响 |
|---------|---------|------|
| PDF 文档（含扫描件） | ❌ 跳过（`text == ""`）| 最广泛的企业文档格式无法搜索 |
| 图片（JPG/PNG） | ❌ 跳过 | 无法搜索图片中的文字或语义 |
| 音频（MP3/WAV） | ❌ 跳过 | 无法转录和搜索音频内容 |
| 视频（MP4/AVI） | ❌ 跳过 | 无法理解和搜索视频内容 |
| Office 文档（DOCX/XLSX/PPTX） | ❌ 跳过 | 最常见的协作文档格式无法搜索 |

### 代码锚点

| 文件 | 影响 |
|------|------|
| `internal/ai/extractor.go:32-90` | `supportedExt()` 扩展为包含所有需要处理的新格式 |
| `internal/ai/extractor.go:55-80` | `Extract` 方法新增 PDF/Office/Image/Audio/Video 分支 |
| `internal/ai/chunker.go` | 新增多模态 chunk 类型（文本+图像引用/时间戳分段）|
| `internal/ai/embedder.go` | Embedder 接口扩展为支持多模态输入（Text+Image/Text+Audio）|
| `internal/ai/indexer.go:90-105` | IndexObject 增加多模态处理路径 |
| `internal/ai/search.go` | 搜索结果可以返回非文本内容引用 |
| `internal/config/config_ai.go` | 新增 `AI_OCR_ENABLED`, `AI_TRANSCRIPTION_ENABLED`, `AI_IMAGE_UNDERSTANDING_ENABLED` 等配置 |
| `internal/api/rest/search.go` | 搜索 API 响应扩展为包含非文本内容的元数据 |

### 为什么需要

1. **"AI-native"定位的根本兑现。** 一个"AI-native 文件平台"如果只能索引纯文本文件，本质上是"文本搜索引擎 + 文件存储"——不能理解图像、文档、音频、视频的 AI 平台是名不副实的。

2. **企业内容现实。** 企业中的文档大部分是 PDF、Office 文档和图片（扫描件/截图）。仅索引 `.txt` 和 `.md` 文件意味着 >80% 的企业内容不可搜索。这是产品采用的最大障碍之一。

3. **Model Context Protocol 的预期。** MCP 工具列表暴露了 `read_file` 和 `search`——如果 80% 的文件不能被搜索，MCP 集成对大语言模型的价值大打折扣。

### 架构建议

```
┌─────────────┐   ┌──────────────┐   ┌──────────┐   ┌──────────────┐
│ 原始文件     │ → │ 多模态提取器  │ → │ 分块器    │ → │ 嵌入器        │
│             │   │              │   │          │   │              │
│ .pdf        │   │ PDF→text+img │   │ text     │   │ text→vec     │
│ .docx       │   │ DOCX→text    │   │ image    │   │ image→vec    │
│ .jpg/.png   │   │ OCR→text     │   │ audio    │   │ audio→vec    │
│ .mp3/.wav   │   │ ASR→text     │   │ video    │   │ video→vec    │
│ .mp4        │   │ Video→transc │   │ metadata │   │ meta→vec     │
└─────────────┘   └──────┬───────┘   └──────────┘   └──────┬───────┘
                         │                                 │
                         ▼                                 ▼
                  ┌──────────────┐                 ┌──────────────┐
                  │ 索引存储      │                 │ 检索         │
                  │              │                 │              │
                  │ BM25+vector  │                 │ 文本命中     │
                  │ 时间戳索引    │                 │ 图片命中     │
                  │ 图片特征      │                 │ 音频片段     │
                  └──────────────┘                 └──────────────┘
```

**阶段化实施路径：**

| 阶段 | 内容 | 技术方案 | 工作量 |
|------|------|---------|-------|
| **Phase 1** | PDF 文本提取 + Office 文档解析 | Go 原生库（`unidoc`/`pdfcpu`/`excelize`）或远程提取器 | 中 |
| **Phase 2** | OCR（图像/扫描件 PDF 文字识别） | Tesseract / 远程 OCR 服务 / Google Vision API | 中 |
| **Phase 3** | 音频转录 | Whisper（本地或远程）/ 语音识别 API | 中 |
| **Phase 4** | 图像语义理解（CLIP 嵌入） | 多模态嵌入模型（如 `clip-vit-base-patch32`）| 高 |
| **Phase 5** | 视频理解 | 视频帧提取 + 音频转录 + 视觉嵌入组合 | 高 |

**提取器接口扩展：**

```go
type ExtractedContent struct {
    Text       string           // 主要文本内容
    Images     []ExtractedImage // 嵌入的图片（PDF 页面/文档插图）
    Audio      []ExtractedAudio // 音频片段（带时间戳）
    Video      []ExtractedVideo // 视频片段（带时间戳）
    Metadata   map[string]any   // 结构化元数据（作者、页数、时长等）
}

type ExtractedImage struct {
    Data        []byte  // 图片数据
    Format      string  // "png" / "jpeg"
    Caption     string  // 自动生成的描述（Phase 4）
    Vector      []float32 // CLIP 嵌入
    PageNumber  int     // 所在页（PDF）
}
```

**远端提取器优先策略：**

鉴于 Go 生态对 PDF/OCR/音视频处理库的支持有限，建议优先扩展 `RemoteExtractor`：

```go
// internal/ai/extractor_remote.go — 已存在，能力扩展
func NewRemoteExtractor(endpoint, apiKey string, fallback Extractor) *RemoteExtractor {
    return &RemoteExtractor{
        endpoint: endpoint,
        apiKey:   apiKey,
        fallback: fallback, // 本地文本提取降级
    }
}
```

由 Python/Node.js 提取服务（如 `pdfplumber` + `pytesseract` + `whisper` + `docling`）提供多模态提取能力，Go 端通过 HTTP 获取提取结果。

### 边界情况

| Edge Case | 行为 |
|-----------|------|
| 超大 PDF（>1000 页） | 分页提取 + 按页分块；可通过 `AI_CHUNK_WINDOW` 控制每块页数 |
| 受密码保护的 PDF | 无法提取 → `IncIndexerSkip(password_protected)` |
| 损坏的媒体文件 | 静默跳过 → `IncIndexerSkip(corrupt)` |
| 静音音频文件 | 转录为空 → `IncIndexerSkip(silent)` |
| 语言检测 | 提取后检测语言 → 存储到 chunk 元数据，便于检索时按语言筛选 |
| 远端提取器超时 | 降级到本地提取器（文本格式）或跳过（二进制格式）|
| 图像/音频嵌入 vs 文本嵌入 | 需要独立向量空间或映射到同一空间（CLIP 可实现跨模态检索）|
| 检索结果的呈现 | 非文本 chunk 的搜索结果需要返回缩略图/音频片段/视频帧预览 |

---

## 优先级总结与建议执行顺序

| 优先级 | 方向 | 估算工作量 | 影响范围 | 前置依赖 |
|--------|------|-----------|---------|---------|
| **P0** | 方向一：DeleteMarker + Version-ID 删除 | M（~3-5 天） | S3 协议兼容性（行为缺陷修复） | 无 |
| **P1** | 方向三：Object Lock 模式区分 | M（~3-4 天） | 合规/安全 | 无（独立于方向一） |
| **P1** | 方向二：Token 上传门户 | M（~4-5 天） | 产品能力（新场景） | 鉴权框架 |
| **P2** | 方向四：Multipart 预签名 URL | L（~5-7 天） | 协议完备性 + 大文件上传 | 方向一（DeleteMarker 冲突低）|
| **P2** | 方向五：多模态 AI 索引 | XL（Phase1+2~5-7 天，全部~15-20 天）| AI-native 定位 | 远端提取器基础设施 |

**推荐执行顺序：**

```
Phase 1 — 协议修复（重要且紧急）
├── 方向一：DeleteMarker + Version-ID 删除
│   └── 为什么？当前行为是 bug，不是 feature gap。会影响所有使用版本化的用户。
│
Phase 2 — 合规增强与产品能力（重要不紧急）
├── 方向三：Object Lock 模式区分
│   └── 为什么？合规场景的前提条件。方向一的部分依赖（DeleteMarker 需感知 retention）。
├── 方向二：Token 上传门户
│   └── 为什么？快速打开新用例，低复杂度高价值。
│
Phase 3 — 协议完备性与 AI 能力（差异化）
├── 方向四：Multipart 预签名 URL
│   └── 为什么？SDK/前端大文件上传的刚需。
├── 方向五：多模态 AI 索引
│   └── 为什么？AI-native 定位的根本支撑，但工作量最大，可分阶段交付。
```

---

## 与既有文献的去重对照

| 本文件方向 | grep 验证 | 既有分析覆盖 | 去重结论 |
|-----------|----------|-------------|---------|
| **方向一：DeleteMarker + Version-ID 删除** | `grep -r "delete.*marker\|DeleteMarker\|x-amz-delete-marker\|version.*delete" docs/requirements/` → v15/v42/v51/v61 各有 1–3 行过路提及，均为表格中的单行文本（"缺少 DeleteMarker header"、"DeleteMarker 不应计入版本数"），**零架构设计与实现分析** | ✅ **完全去重** |
| **方向二：Token 上传门户** | `grep -r "anonymous.*upload\|public.*upload\|upload.*portal\|token.*gate\|file.*drop\|scoped.*upload" docs/requirements/` → **0 命中** | ✅ **完全去重**（零覆盖）|
| **方向三：Object Lock 保留模式** | `grep -r "Governance.*Compliance\|BypassGovernance\|governance.*bypass\|RetentionMode\|retention.*mode" docs/requirements/` → v16 方向五标题含"锁模式治理"但聚焦 S3 子资源 API 完整性（`?legal-hold`/`?retention` 端点），**零 Governance/Compliance 模式区分与 Bypass 语义的架构设计** | ✅ **互补去重**（v16 聚焦 S3 子资源协议端点，本方向聚焦保留模式语义与权限模型）|
| **方向四：Multipart 预签名 URL** | `grep -r "presign.*multipart\|UploadPart.*presign\|presign.*upload.*part\|presignInit\|PresignUploadPart\|PresignCompleteMultipart" docs/requirements/` → **0 命中** | ✅ **完全去重**（零覆盖）|
| **方向五：多模态 AI 索引** | `grep -r "multimodal\|multi.modal\|OCR\|audio.*index\|video.*index\|image.*captio\|speech.*text\|transcri" docs/requirements/` → **0 命中**（仅 v65 注释行路过提及"Whisper/PaddleOCR"作为远端提取器示例）| ✅ **完全去重**（零架构设计）|

---

*本文档基于完整代码扫描生成，所有方向代码锚点均经过 grep 验证。各方向估算为纯 Go 实现时间，不包含测试、文档和远端提取服务（方向五）。*
