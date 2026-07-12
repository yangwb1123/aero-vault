好的，我已完整阅读原文，并核实了代码锚点的准确性。以下是我的架构审阅意见。

---

# 架构审阅：高价值扩展方向

**范围：** 对本文 5 个方向的 Go 代码锚点验证、架构深度补充、边界条件修正以及实施风险分析。

---

## 1. 代码锚点准确性验证

逐一核对文中引用的代码行与代码库真实内容：

| 方向 | 锚点验证 | 准确性 | 备注 |
|------|---------|--------|------|
| **方向一** | `handler.go:247-260` DeleteObject → 始终 `hard delete`，无视版本化 | ✅ 精确 | 确认为行为 bug |
| **方向一** | `bucketconfig.go:205-209` 注释声明"无 DeleteMarker 实体" | ✅ 精确 | 与代码匹配 |
| **方向一** | `GetObject` 不在 404 路径检查 DeleteMarker | ✅ 确认 | 当前无任何 DeleteMarker 引用 |
| **方向三** | `file_crud.go:295-302` `hardDeleteObject` 仅检查 `LockedUntil` | ✅ 精确 | 不检查任何 `retention_mode` |
| **方向三** | `Object` 结构体无 `RetentionMode` 字段 | ✅ 精确 | `repository.go:21-38` 确认 |
| **方向三** | `policy.go` 无 `BypassGovernanceRetention` | ✅ 精确 | 无此 action 常量 |
| **方向四** | `storage.go:121-125` 仅 `PresignGet` + `PresignPut` | ✅ 精确 | 接口定义已确认 |
| **方向四** | 所有后端无 `Presign*Multipart` | ✅ 精确 | `local.go` / `s3.go` 已确认 |
| **方向五** | `extractor.go:32-90` 只支持纯文本 | ✅ 精确 | `DefaultExtractor` 确为白名单 |
| **方向五** | `indexer.go:90-105` `text==""` → `IncIndexerSkip(unsupported)` | ✅ 精确 | 与代码一致 |
| **方向五** | `RemoteExtractor` 已存在 | ✅ 精确 | 已检查 `extractor_remote.go` |

**结论：** 所有代码锚点经过 grep + read 验证，定位准确。无虚标锚点。

---

## 2. 方向一：DeleteMarker — 架构深潜微调

### 2.1 迁移号冲突

文中所有 5 个方向均提议 `0025_*` 迁移。当前最新为 `0024_bucket_notifications`。需协调——**建议方向一独占 0025**，其余方向依次递增：

```
方向一 → 0025_delete_marker.{up,down}.sql
方向三 → 0026_retention_mode.{up,down}.sql  （若独立于方向一）
方向二 → 0027_upload_tokens.{up,down}.sql
方向四 → 0028_multipart_presign.{up,down}.sql（或无迁移——仅接口扩展）
方向五 → 无需迁移（远端提取器无 schema 变更）
```

### 2.2 核心架构决策：DeleteMarker 存储方式

文档建议在 `objects` 表加 `is_delete_marker` 列。这可行但有一个**重要的架构权衡**：

| 方案 | 优点 | 缺点 |
|------|------|------|
| **A：单一 `objects` 表 + `is_delete_marker` 列** | 无需新表；版本迭代清晰；现有查询最小改动 | DeleteMarker 行与版本行共享列（`size=0`, `etag=""`, `storage_key=""`）；索引可被稀释 |
| **B：独立 `delete_markers` 表** | 列模型纯净；可独立 GC；`?versions` 查询通过 UNION 分别返回 `<DeleteMarker>` 和 `<Version>` | 增加全系统查询复杂度；`ListObjectVersions` 需 JOIN/UNION；审计日志需跨表 |

**我的建议：采用方案 A（`is_delete_marker bool` + `storage_key=""`）**，理由：
- `ListObjectVersions` 当前逻辑是 `ORDER BY updated_at DESC` 扫描版本行——如果 DeleteMarker 在同一个表，可以用 `WHERE` 条件区分 `<Version>` 和 `<DeleteMarker>`
- 不需要额外的 UNION，性能开销最小
- 但也意味着**方向一和方向三共享同一张 `objects` 表**——建议合并迁移：

**合并建议：** 方向一和方向三在同一个迁移中完成 `objects` 表变更：

```sql
-- 0025_object_versioning.up.sql
ALTER TABLE objects ADD COLUMN is_delete_marker INTEGER DEFAULT 0;
ALTER TABLE objects ADD COLUMN retention_mode TEXT DEFAULT '';  -- "" | "GOVERNANCE" | "COMPLIANCE"
CREATE INDEX objects_delete_marker_idx ON objects (tenant_id, bucket, key, is_delete_marker, updated_at DESC);
```

### 2.3 关键边缘情况补充

| 缺失的边缘情况 | 影响 |
|--------------|------|
| **并发 PUT + DELETE 竞争条件** | 如果 PUT 和 DELETE 在同一微妙到达——非版本化桶中当前行为是最后操作者胜。版本化桶中 DELETE 创建 DeleteMarker + PUT 创建新版本——都需要事务性保证。需要确认 `InsertObjectVersion` 和 `CreateDeleteMarker` 是否在同一事务中 |
| **DeleteMarker 的生命周期到期** | S3 允许在 Lifecycle 规则中设置 `ExpiredObjectDeleteMarker`。当前 Lifecycle 实现（`reconcile/retention.go`）需要扩展 |
| **`?versions` 列表的 `IsLatest` 标记** | 如果当前最新行是 DeleteMarker，则 `IsLatest=true` 的 `<DeleteMarker>` 元素应标记为 `IsLatest`。S3 协议要求在 `<DeleteMarker>` 元素中也包含 `<IsLatest>` |

### 2.4 事务性建议

当前 `DeleteObject` → `svc.Delete(ctx, tenant, bucket, key, true)` 调用了 `hardDeleteObject`。对于版本化桶的 DeleteMarker **不应删除 storage blob**——这是当前行为 bug 的核心。建议重构为：

```go
func (h *Handler) DeleteObject(w, r, bucket, key) {
    // ...
    if vid := r.URL.Query().Get("versionId"); vid != "" {
        // DELETE ?versionId=<id> — 永久删除特定版本
        h.svc.DeleteVersion(ctx, tenant, bucket, key, vid) 
        return
    }
    bcfg := h.svc.GetBucketConfig(ctx, tenant, bucket)
    if bcfg.Versioning {
        // 创建 DeleteMarker — 不删除 blob
        marker, err := h.svc.CreateDeleteMarker(ctx, tenant, bucket, key)
        w.Header().Set("x-amz-version-id", marker.VersionID)
        w.Header().Set("x-amz-delete-marker", "true")
        w.WriteHeader(http.StatusNoContent)
        return
    }
    // 非版本化桶：现有行为
    h.svc.Delete(ctx, tenant, bucket, key, true)
}
```

注意：**S3 协议要求对版本化桶中不存在的对象发送 DELETE，仍要创建 DeleteMarker**（即不能返回 404）。这是 AWS 的刻意设计——文档的边界情况已正确捕捉。

---

## 3. 方向二：Token 上传门户 — 架构深潜

### 3.1 鉴权集成路径

文档提议通过 `X-Aero-Tenant` + 上传令牌来控制匿名上传。关键的架构问题是：**令牌如何映射到租户和 scope？**

当前鉴权中间件链是：

```
Auth Middleware → authenticateBearer → scope check
```

对于上传令牌，需要一个新的中间件路径：

```go
func (r *Registry) authenticateUploadToken(w http.ResponseWriter, req *http.Request) (*http.Request, bool) {
    token := extractToken(req) // 从 URL path 或 Authorization header 中提取
    t, err := r.repo.GetUploadToken(ctx, sha256(token))
    if err != nil || t.ExpiresAt.Before(time.Now()) {
        forbidden(w, "invalid or expired upload token")
        return nil, false
    }
    // 更新计数器
    r.repo.IncrementUploadTokenUsage(ctx, t.ID)
    // 注入租户 + 写入权限
    req.Header.Set("X-Aero-Tenant", t.TenantID)
    req.Header.Set("X-Upload-Token-ID", t.TokenHash)
    // 创建一个仅写、路径限制的匿名身份
    ctx := context.WithValue(req.Context(), ctxKeyUploadToken, t)
    return req.WithContext(ctx), true
}
```

**注意：** 这需要 `Registry` 持有 `repository.Repository` 引用——当前 `Registry` 是纯内存的，只持有 key map。这是有意的架构权衡——鉴权层不应该依赖数据库。建议：

> **方案：** 将上传令牌验证放在**业务层**（`middleware` 包或单独的 `uploadtoken` 中间件），而非 `auth` 包。在 Middleware 链中，Auth 先拒绝未认证请求，然后再由 UploadToken 中间件提供降级路径。

这样 Auth 层保持纯内存无数据库依赖，上传令牌逻辑在业务中间件层。

### 3.2 令牌路径安全

文档正确指出了路径前缀匹配。但缺少一个重要细节：**路径遍历防护**。如果令牌允许 `uploads/customer1/`，用户必须不能上传到 `uploads/customer2/../malicious/`。需要额外的 `path.Clean` 校验：

```go
func validateUploadPath(allowedPrefix, actualPath string) bool {
    clean := path.Clean("/" + actualPath) // 标准化
    allowed := path.Clean("/" + allowedPrefix)
    return strings.HasPrefix(clean, allowed)
}
```

### 3.3 配额边界条件

文档没有讨论：**上传令牌的用量 vs 租户配额的关系。** 如果租户设置了租户级配额（`max_bytes`），上传令牌的累积用量也应受租户配额的限制。建议：

```
每次匿名上传成功后，检查：
  if current_tenant_usage + upload_size > tenant_quota {
      return 403 "TenantQuotaExceeded"
  }
```

---

## 4. 方向三：Object Lock 模式区分 — 关键修正

### 4.1 对当前代码中 "GOVERNANCE" 的误读

文档正确指出当前 `hardDeleteObject` **完全忽略保留模式**——但需要注意代码库中**已经**在 bucket 配置层序列化了 `GOVERNANCE`：

```go
// bucketconfig.go:183
out.Rule = &objectLockRule{DefaultRetention: objectLockRetention{Mode: "GOVERNANCE", Days: days}}
```

这意味着：**S3 `GET /bucket?object-lock` 响应已经返回 `Mode="GOVERNANCE"`**。但实际**对象锁的 DELETE 阻拦行为**将所有锁定视为 COMPLIANCE 模式（不可绕过）。

这是一个 **bug 在 enforcement 层，不在 API/XML 层**。文档的修复方向（`hardDeleteObject` 增加模式感知）是正确的。

### 4.2 接口设计建议

当前 `FileService` 的 `SetBucketObjectLock` 只接受 `seconds`（int）。要支持模式，需要扩展：

```go
// 当前
func (s *FileService) SetBucketObjectLock(ctx, tenant, bucket string, seconds int) error

// 建议
func (s *FileService) SetBucketObjectLock(ctx, tenant, bucket string, mode string, seconds int) error
```

但更好的设计是**不在 bucket 级存储保留模式**（S3 的 bucket 级配置只有 `DefaultRetention.Mode`，每个对象的 `PUT ?retention` 可以指定不同模式）。AeroVault 只需要在**对象级**存储 `retention_mode`：

```go
type Object struct {
    LockedUntil   *time.Time
    RetentionMode string // "" | "GOVERNANCE" | "COMPLIANCE" — 由 PUT ?retention 设置，而非 bucket 级默认
}
```

bucket 级的 `DefaultRetention.Mode` 仅用于初始化，**不持久化到 `objects.retention_mode`**——对象锁创建时会自己写 mode。

### 4.3 法律封存（Legal Hold）的归并

当前代码中 Legal Hold 是通过 metadata `_aero_legal_hold` 实现的，不是结构体字段。文档建议新增 `LegalHold bool` 字段——但当前已有功能通过 `obj.Metadata["_aero_legal_hold"] == "ON"` 工作。**建议保持 metadata 方式以最小化迁移影响**，或统一提升为结构体字段。

---

## 5. 方向四：Multipart 预签名 URL — 架构深潜

### 5.1 当前 Storage 接口已有多分片方法

Storage 接口已有 `InitMultipart`, `UploadPart`, `CompleteMultipart`, `AbortMultipart`。这意味着**后端已经实现了多分片操作**——只是没有预签名变体。

对于 **local 后端**的预签名分片上传，需要一种新的本地签名机制。当前 `local.PresignGet`/`PresignPut` 使用 HMAC 签名 `key:method:expiry`。对于分片上传，签名需要包含 `uploadID` 和 `partNumber`：

```go
// local.go
func (s *LocalStorage) PresignUploadPart(ctx, key, uploadID string, partNumber int32, expiry time.Duration) (string, error) {
    if s.SignKey == "" {
        return "", ErrPresignDisabled
    }
    data := fmt.Sprintf("uploadPart:%s:%s:%d:%d", key, uploadID, partNumber, expiry.Unix())
    sig := hmacSHA256(s.SignKey, data)
    return fmt.Sprintf("%s/%s/parts/%d?uploadId=%s&expires=%d&sig=%s", s.PublicURL, key, partNumber, uploadID, expiry.Unix(), sig), nil
}
```

### 5.2 核心问题：预签名分片上传播的原子性

文档的方案有一个**重要的缺省**：预签名分片上传播结束后，客户端必须调 `complete` 来合成最终对象。但**谁来追踪已上传的 part 信息？**

S3 做法：服务端追踪每个 UploadPart 的 etag（在 `UploadPart` 初始化时存储 etag），客户端只在 complete 时提交 part list。但预签名场景中，**服务端没有收到 UploadPart 请求**（客户端直传到 S3），因此服务端不知道哪些 part 已上传以及对应的 etag。

有两种方案：

| 方案 | 描述 | 复杂度 |
|------|------|--------|
| **A：服务端代理追踪** | 客户端在上传完成 part 后，把 etag 发给服务端记录 | 中——需要 `POST /v1/multipart/record-part/{uploadID}` |
| **B：客户端在 complete 时一次性提交所有 part etag** | 类似 S3 CompleteMultipartUpload 行为——客户端在 presign-complete 时提交 `[{number, etag}]` | 低——但服务端需要在最终合成时验证每个 part 对应的 etag |

**建议：方案 B**（与 AWS S3 完全一致）。`PresignCompleteMultipart` 要求客户端提交完整的 `{partNumber, etag}[]` 列表。服务端验证每个 etag 与实际上传的 part 匹配。

### 5.3 S3 后端的预签名委派

对于 S3 后端，可以直接使用 AWS SDK 的 `PresignClient`：

```go
func (s *S3Storage) PresignUploadPart(ctx, key, uploadID string, partNumber int32, expiry time.Duration) (string, error) {
    input := &s3.UploadPartInput{
        Bucket:     aws.String(s.bucket),
        Key:        aws.String(key),
        UploadId:   aws.String(uploadID),
        PartNumber: aws.Int32(partNumber),
    }
    req, _ := s.s3Client.UploadPartRequest(input)
    presignedReq, err := req.Presign(expiry)
    if err != nil {
        return "", err
    }
    return presignedReq.URL, nil
}
```

---

## 6. 方向五：多模态 AI 索引 — 关键风险与技术债务

### 6.1 `RemoteExtractor` 已为多模态铺路

文档正确识别了 `RemoteExtractor` 的存在。当前实现：

```go
func (e *RemoteExtractor) Extract(ctx, contentType, reader) {
    if isTextType(contentType) { return e.Fallback.Extract(...) } // 本地同步
    return e.extractRemote(...) // HTTP 请求远端
}
```

这已经是**按 content-type 分流**的架构。对于多模态扩展，只需要在 `isTextType` 之外增加更多的路由分支：

```go
func (e *MultiModalExtractor) Extract(ctx, contentType, reader) (ExtractedContent, error) {
    switch {
    case isTextType(contentType):
        text, _ := e.textExtractor.Extract(ctx, contentType, reader)
        return ExtractedContent{Text: text}, nil
    case isPDF(contentType):
        return e.extractPDF(ctx, reader)
    case isImage(contentType):
        return e.extractImage(ctx, contentType, reader)
    case isAudio(contentType):
        return e.extractAudio(ctx, contentType, reader)
    case isVideo(contentType):
        return e.extractVideo(ctx, contentType, reader)
    }
}
```

### 6.2 关键架构缺口：Extractor 不返回 ExtractedContent

当前 `Extractor` 接口只返回 `(string, error)`——即纯文本。要实现多模态，需要**修改提取器接口**：

```go
// 当前
type Extractor interface {
    Extract(ctx context.Context, contentType string, r io.Reader) (string, error)
}

// 建议
type ExtractorResult struct {
    Text    string            // 主要文本内容
    Chunks  []ExtractedChunk   // 可选的多模态子块
    Meta    map[string]any    // 结构化元数据
}

type ExtractedChunk struct {
    Type        string      // "text" | "image" | "audio" | "video"
    Text        string      // 对于非文本块，可能是描述/字幕
    Data        []byte      // 原始数据（可选，用于视觉嵌入）
    Timestamp   *time.Duration // 音频/视频的时间偏移
    PageNumber  int         // PDF 页码
}

type Extractor interface {
    Extract(ctx context.Context, contentType string, r io.Reader) (ExtractorResult, error)
}
```

**这是接口不兼容变更**——影响所有现有提取器实现（`DefaultExtractor`, `RemoteExtractor`, 单元测试 mock）。**建议在 Phase 1 之前先重构提取器接口。** 或者在 Phase 1 中先保持 `string` 接口，通过 `RemoteExtractor` 外部处理多模态。

### 6.3 阶段性实施风险

| 阶段 | 风险 | 缓解措施 |
|------|------|---------|
| Phase 1（PDF+Office） | Go 原生 PDF 库缺位——`pdfcpu` 只处理操作层，文本提取弱；`unidoc` 是商业库 | 优先走 `RemoteExtractor`（Python `pdfplumber` + `python-docx`） |
| Phase 2（OCR） | Tesseract CGo 绑定复杂——CGo 构建时间和交叉编译问题 | 远端提取器（Python `pytesseract`） |
| Phase 3（音频） | Whisper 模型推理需要 GPU 或较慢的 CPU | 远端提取器（`faster-whisper`），支持 GPU 加速 |
| Phase 4（图像语义） | CLIP 嵌入与文本嵌入需要同一向量空间 | 设置 `AI_EMBED_PROVIDER` 为多模态嵌入模型 |
| Phase 5（视频） | 帧提取需要 ffmpeg 绑定 | 远端服务 + 帧采样策略 |

**核心建议：所有非文本提取全走 `RemoteExtractor` 路径**。Go 端零 CGo 依赖，多模态能力完全由 Python/Node.js 提取服务提供。这在文档中已经多处提及（Whisper / PaddleOCR），应升格为主架构决策而非备选路径。

---

## 7. 交叉方向依赖与冲突

| 依赖对 | 冲突 | 影响 |
|--------|------|------|
| **方向一 ↔ 方向三** | 共享 `objects` 表 | 应当合并迁移到 `objects` 表 |
| **方向一 ↔ 方向四** | 无直接冲突 | 可独立并行 |
| **方向一 ↔ 已有版本化逻辑** | `Put` 在版本化桶已正确创建新版本行 | DeleteMarker 实现后，`Put` 到已 DeleteMarked 对象不覆盖 DeleteMarker——而是创建一个新版本（正确行为） |
| **方向二 ↔ 方向一** | 上传令牌匿名写 + 版本化桶的 DeleteMarker | 匿名上传不允许删除（`scope: write-only`），因此方向二的匿名令牌与版本化语义无冲突 |
| **方向三 ↔ 方向一** | DeleteMarker 在锁定对象上 | 保留期内对象不能创建 DeleteMarker——`hardDeleteObject` 的检查同样应适用于 CreateDeleteMarker |
| **方向四 ↔ 已有 Multipart** | 无冲突——只是扩展接口 | 存储后端已实现 Multipart 操作 |
| **方向五 ↔ 已有 AI 管线** | 提取器接口变更风险 | 建议保留向后兼容的 `Extract(ctx, ct, r) (string, error)` 或通过 `MultiModalExtractor` 装饰器 |

---

## 8. 完整文件变更清单汇总（基于代码验证）

| 方向 | 文件 | 变更类型 |
|------|------|---------|
| **方向一** | `internal/repository/repository.go` | `Object.IsDeleteMarker bool` 字段 |
| | `internal/repository/migrations/{sqlite,postgres}/0025_object_versioning.up.sql` | 新迁移：`is_delete_marker` + `retention_mode` |
| | `internal/service/file.go` | 新增 `CreateDeleteMarker`, `DeleteVersionByID`, `IsDeleteMarker` |
| | `internal/service/file_crud.go` | `Delete` 重构以支持版本化 |
| | `internal/api/s3compat/handler.go` | `DeleteObject` + `GetObject` + `HeadObject` 重构 |
| | `internal/api/s3compat/xml.go` | `listVersionsResult` 增加 `<DeleteMarker>` 元素 |
| | `internal/api/s3compat/bucketconfig.go` | 更新 `listObjectVersions` 区分版本/标记 |
| | `internal/reconcile/retention.go` | 增加 DeleteMarker 到期 GC |
| **方向二** | `internal/middleware/middleware.go` | 新增 UploadToken 中间件 |
| | `internal/api/rest/router.go` | 新增 `POST /v1/admin/upload-tokens` |
| | `internal/api/rest/handler.go` | 新增匿名上传处理 |
| | `internal/repository/repository.go` | 新增 `UploadToken` 模型 + CRUD |
| | `internal/repository/migrations/0027_upload_tokens.up.sql` | 新迁移 |
| | `internal/config/config.go` | 新增 `UPLOAD_TOKEN_*` 配置 |
| **方向三** | `internal/repository/repository.go` | `Object.RetentionMode string` 字段 |
| | （复用方向一 0025 迁移） | 含 `retention_mode` 列 |
| | `internal/service/file_crud.go` | `hardDeleteObject` 增加模式感知 |
| | `internal/service/file.go` | 新增 `SetObjectRetention` / `BypassRetention` |
| | `internal/api/s3compat/handler.go` | 新增 `putObjectRetention` / `getObjectRetention` |
| | `internal/api/s3compat/xml.go` | 新增 `retentionConfiguration` XML 类型 |
| | `internal/auth/policy.go` | 新增 `BypassGovernanceRetention` action |
| **方向四** | `internal/storage/storage.go` | Storage 接口新增 4 个 Presign 方法 |
| | `internal/storage/local.go` | Local HMAC 签名实现 |
| | `internal/storage/s3.go` | S3 AWS SDK PresignClient 实现 |
| | `internal/storage/oss.go`, `cos.go` | 各云 SDK 实现 |
| | `internal/api/rest/handler.go` | 新增 multipart presign 路由 |
| | `internal/service/file_multipart.go` | 新增 `Presign*` 方法 |
| **方向五** | `internal/ai/extractor.go` | 接口扩展（可选）或新增 `MultiModalExtractor` |
| | `internal/ai/extractor_remote.go` | 扩展远端协议支持结构化结果 |
| | `internal/ai/indexer.go` | 多模态处理路径 |
| | `internal/ai/search.go` | 非文本命中结果处理 |
| | `internal/ai/chunker.go` | 多模态分块 |
| | `internal/config/config_ai.go` | `AI_*_ENABLED` 配置 |

---

## 9. 总体评价

| 维度 | 评分 | 说明 |
|------|------|------|
| **代码锚点准确性** | ★★★★★ | 全部经过 grep + read 验证，无虚标 |
| **去重完整性** | ★★★★★ | 65 份文档的 grep 验证充分，方向二和四为零覆盖，方向一和五为真白区 |
| **架构深度** | ★★★★☆ | 五个方向的核心架构都有清晰的方案。方向四缺少 part etag 追踪讨论；方向五缺少提取器接口兼容性变更分析 |
| **边缘情况覆盖** | ★★★★☆ | 方向一的表格非常完整。方向三缺少 Legal Hold metadata vs struct 字段的权衡讨论 |
| **实施可行性** | ★★★★☆ | 方向五的分阶段计划合理。方向一和四的存储后端适配讨论了正确性。方向二的鉴权集成细节略有不足 |
| **优先级合理性** | ★★★★★ | P0→P2 排序正确。Phase1→3 的执行顺序合理 |

**总体：一份高质量的架构分析文档。** 代码锚点夯实、去重验证严谨、方向选择切中产品缺口。上述补充建议主要围绕：交叉依赖协调、提取器接口兼容性、预签名 etag 追踪、方向二与鉴权框架的集成方式等技术细节。

如需要我可以就任何方向撰写详细的实现 task.md 或开始实施代码。
