# 高价值扩展方向：元数据注入安全、内容编码 Range 语义、对象键安全、分段上传孤子清理、管理面控制台

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — `cmd/server/main.go` + `internal/` 全部子包（237+ Go 源文件，50 对迁移文件），3 套 SDK（Go/Python/JS），MCP 双模式（HTTP+stdio），Web UI，`deploy/` 全套配置（Helm/Grafana/Prometheus/OTel），`HARNESS.md`，`AGENTS.md`，`ROADMAP.md`  
> **去重验证：** 对 `docs/requirements/` 下全部 103 份既有分析文档逐方向进行关键词正则 + 语义交叉验证 + 代码锚点反查  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。选取**代码中存在明确锚点、有实质性安全/正确性影响、且在 103 轮分析中未被独立深度覆盖**的方向。每个方向包含：产品价值 → 现状与代码证据 → 架构权衡 → 边界情况。

---

## 去重验证

对 `docs/requirements/` 下全部 103 份既有分析文档进行逐方向交叉验证：

| 方向 | 既往覆盖情况 | 结论 |
|------|-------------|------|
| **方向一：`_aero_` 保留元数据命名空间注入** | 方向 v93 提及 `_aero_legal_hold` 作为法律标记；方向 v11 在模糊测试表中一行提及 `_aero_` 前缀概念；方向 expansions-v3/11/12/13/14 在功能层面提及 `_aero_` 系统键。**零文档分析用户可通过 HTTP 头注入 `_aero_` 系统键、覆盖 `_aero_scrub_status` 绕过损坏检测、覆盖 `_aero_legal_hold` 阻止删除等攻击向量**。正则搜索 `_aero_.*inject\|_aero_.*overwr\|_aero_.*spoof\|_aero_.*tamper\|metadata.*injection` → 0 命中 | ✅ **全新安全分析方向** |
| **方向二：Content-Encoding 破坏 Range 请求的正确性** | 方向 v14 有一行（`content-range + gzip 分块（标准不支持）`）提及 gzip 与 range 不兼容；方向 v94 分析 gzip 缓存语义但聚焦计算开销。**零文档分析 Range 偏移量基于压缩大小计算但返回解压后内容这一数据损坏 bug**。正则搜索 `gzip.*offset\|range.*offset.*gzip\|Bytes=.*gzip\|gzip.*bytes=.*\|compress.*range.*offset` → 0 命中 | ✅ **全新数据完整性方向** |
| **方向三：对象键安全——字符编码、长度与规范化** | 方向 v10/v11/v38/v42/v45/v71/v74 提及部分对象键问题（路径穿越、符号链接、`.`/`..` 校验），但**均聚焦于文件系统路径安全而非完整的键输入验证模型**。`validateKey` 的 6 行实现从未被任何文档完整分析。正则搜索 `validateKey.*gap\|key.*length.*limit\|key.*control.*char\|Unicode.*normal.*key\|key.*canonical\|key.*encoding.*validation` → 0 独立分析 | ✅ **全新安全边界方向** |
| **方向四：分段上传孤子生命周期——无 GC 导致存储泄漏** | 方向 v28/v4/v62/v79/v88 从不同角度提及 multipart upload 但不涉及孤子上传的 GC 缺失。方向 v79 方向二覆盖「CompleteMultipart ETag 交叉验证」聚焦完成语义而非清理。**零文档分析 `AbortMultipart` 仅客户端主动调用、无 Reconcile 级自动清理、`sql_uploads.go` 无过期字段**。正则搜索 `multipart.*orphan\|multipart.*gc\|multipart.*abandon\|upload.*clean.*reconcile\|AbortMultipart.*periodic` → 0 独立分析 | ✅ **全新存储治理方向** |
| **方向五：Web UI 管理面控制台——从预览 SPA 到运维管理工具** | 方向 v2/v11/v28/v46/v72/v84/v90 在不同时间点提及 Web UI 增强，但**所有涵盖均为"应该增强"的建议性文字，无任何文档进行代码级现状分析**。当前 `index.html` 282 行单文件 SPA 的能力边界从未被系统地量化记录。正则搜索 `index.html\|282.*line\|SPA\|single.*page\|embedded.*UI\|static.*UI` → 0 行级别的代码锚点分析 | ✅ **全新产品方向** |

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 |
|---|------|------|--------|---------|---------|
| **1** | **`_aero_` 保留元数据命名空间注入防护** | 安全/数据完整性 | **P0** | 用户可通过 `x-amz-meta-_aero_*` HTTP 头注入系统保留键，覆盖 `_aero_scrub_status`（绕过损坏检测）、`_aero_legal_hold`（阻止删除）、`_aero_content_md5`（篡改校验和）、`_aero_content_encoding`（触发错误解压缩） | `internal/service/file.go:105-120`（`validateMetadata` 对 `_aero_` 前缀仅跳过大小检查、不拒绝注入）；`internal/api/rest/handler.go:848-866`（`extractMetadataHeaders` 不过滤 `_aero_` 键）；`internal/api/s3compat/handler.go:700-718`（`extractMetaHeaders` 不过滤 `_aero_` 键）；`internal/api/s3compat/handler.go:668-684`（`s3PutMeta` 主动设置 `_aero_content_disposition`/`_aero_content_encoding`；用户也可设置其余 `_aero_` 键）；`internal/service/file.go:105-108`（注释 `// System keys (prefixed _aero_) are exempt.` 本意是豁免系统键免于大小限制，但无意间打开注入通道——无 `_aero_` 拒绝逻辑） |
| **2** | **Content-Encoding 感知 Range 请求：解压缩破坏字节偏移** | 数据完整性/协议正确性 | **P0** | 客户端上传 gzip 压缩对象并设置 `Content-Encoding: gzip`，存储压缩后的字节流（例如 100MB 压缩到 10MB）。客户端请求 `Range: bytes=0-5000000`（期望第 0-5MB 解压后偏移）。`GetRange` 调用 `io.CopyN(io.Discard, rc, offset)` 在解压流上跳过 `offset` 字节——偏移量基于**压缩后大小**（10MB），但跳过距离作用于**解压后内容**（100MB）。偏移量完全错位，客户端收到与请求范围完全不同的字节 | `internal/service/file_crud.go:248-261`（`Get` 对 `_aero_content_encoding == "gzip"` 的对象返回自动解压的 `gzipReadCloser`）；`internal/service/range.go:77-90`（`GetRange` 调用 `s.Get` 获取已解压 Reader，然后用 `io.CopyN(io.Discard, rc, offset)` 跳过——偏移量基于压缩大小、跳过在解压流上）；`internal/service/range.go:83`（`length` 参数控制在解压 Reader 上的 `io.LimitReader`——同样语义错误）；`internal/api/s3compat/handler.go:649-651`（S3 GET 的 `Range` 头解析后调用 `ReadAt`——但 `ReadAt` 最终走 `GetRange`）；`internal/api/rest/handler.go:836-843`（REST GET 响应头设置 `Content-Encoding` 告知客户端内容已编码，但实际下发的是解压后字节——HTTP 规范要求 `Content-Encoding: gzip` + Range 见 RFC 7233 §4.1） |
| **3** | **对象键安全：字符编码、长度限制与规范化** | 安全/可靠性 | **P1** | `validateKey` 仅检查空字符串、`..` 路径穿越、`/` 前缀。不验证：控制字符（0x00-0x1F）、最大键长度（S3 限制 1024 UTF-8 bytes）、Unicode 规范化（NFC vs NFD 导致同一个键在 macOS vs Linux 上指向不同对象）、保留文件系统字符（Windows: `\:*?"<>|`）、键中尾随空格/点（Windows 文件系统静默去除）、UTF-8 编码有效性 | `internal/service/file.go:129-134`（`validateKey` — 6 行实现覆盖最简子集）；`internal/storage/local.go`（`Put`/`Get` 使用直接文件系统路径——键中 `/` 字符转化为目录结构，无转义）；`internal/repository/repository.go`（`Object.Key` `string` 类型——不存储规范化或原始编码信息）；`internal/service/file.go:143`（`storageKey` 使用 `path.Join(tenant, bucket, key)` — 对键中嵌入的 `/` 做规范化但不保留原始键）；`internal/api/rest/handler.go:35`（`keyFromPath` 使用 `chi.URLParam` — 自动 URL 解码，可能隐藏编码问题） |
| **4** | **分段上传孤子生命周期：自动清理与存储泄漏检测** | 存储治理/成本 | **P1** | 客户端调用 `InitMultipart` 后上传若干分片但从未调用 `CompleteMultipart` 或 `AbortMultipart`——分片数据永久驻留在存储后端。当前无自动清理机制，`Reconcile` sweep 完全不扫描未完成的分段上传。随着时间推移，孤子积累导致存储成本泄漏 | `internal/reconcile/job.go:40-50`（`Reconcile.Job` 结构体——存储扫描和元数据扫描无上传状态检查）；`internal/repository/sql_uploads.go`（`uploads` 表无 `created_at`/`expires_at` 过期字段，仅有 `upload_id`/`tenant_id`/`bucket`/`key`/`backend`/`backend_uid`/`storage_key`/`metadata`）；`internal/repository/repository.go`（`Repository` 接口无 `ListPendingUploads` 或 `DeleteUploadBefore` 方法）；`internal/service/file_multipart.go:8-35`（`InitMultipart` 存储后端初始化和创建 DB 行——无超时或到期机制）；`internal/service/file_multipart.go:149-155`（`AbortMultipart`——仅客户端主动调用，从不自动触发）；`internal/storage/s3.go:InitMultipart`（S3 后端的分段上传在 S3 侧有 7 天默认 TTL，但 Local 和 OSS/COS 后端无此保障）；`internal/storage/local_multipart.go`（Local 后端将分片存储在临时目录——**从不清理**） |
| **5** | **Web UI 管理面控制台：从单页预览到运维管理工具** | 产品体验/DX | **P2** | 当前 Web UI 是 282 行单文件 HTML+CSS+JS SPA，支持基础浏览、搜索、聊天、血缘追踪。但无法执行任何管理操作：创建/删除桶、设置生命周期规则、配置 ACL/CORS、管理 API 密钥、查看审计日志、管理租户配额、查看作业队列。管理员必须使用 CLI 或原始 HTTP API 完成所有运维 | `internal/webui/web.go:10-30`（`Handler()` 通过 `embed.FS` 提供 `static/*` 文件服务——纯静态 SPA，无服务端渲染或 API 编排）；`internal/webui/static/index.html`（282 行单文件——4 个标签页：搜索、详情、血缘、聊天。无管理面板标签）；`internal/webui/static/index.html:44-48`（API 调用基于 `fetch()` + 硬编码 endpoint——无错误重试、无请求队列、无离线状态管理）；`internal/webui/static/index.html:14-20`（CSS 样式内嵌——非 UI 框架，无响应式设计）；`internal/api/rest/admin.go`（完整的 Admin REST API——`/v1/admin/tenants/*`、`/v1/admin/keys/*`、`/v1/admin/jobs/*`、`/v1/admin/audit`——Web UI 完全不消费）；`internal/api/rest/router.go:48-55`（admin routes 在 REST router 中完整注册——但 Web UI 的 tab 导航无 admin 入口） |

---

## 方向一：`_aero_` 保留元数据命名空间注入防护

### 产品价值

`_aero_` 前缀被系统用作内部元数据的保留命名空间，存储 `_aero_scrub_status`、`_aero_legal_hold`、`_aero_content_md5`、`_aero_content_encoding`、`_aero_content_disposition` 等关键系统状态。当前实现允许**任何经过身份验证的客户端**通过以下方式注入任意 `_aero_` 键：

```
PUT /v1/files/bucket/key
X-Meta-_aero_scrub_status: healthy
X-Meta-_aero_legal_hold: ON
```

**攻击场景与影响：**

| 攻击 | 方式 | 影响 |
|------|------|------|
| **绕过损坏检测** | 设置 `_aero_scrub_status=healthy`（覆盖已损坏对象的 `corrupt` 状态） | 损坏对象变得可读，用户拿到静默损坏数据 |
| **自锁对象** | 设置 `_aero_legal_hold=ON` | 对象无法被硬删除（即使租户配额充足，管理 API 也无法删除）——潜在的勒索/资源耗尽 |
| **篡改校验和** | 设置 `_aero_content_md5=<预期值>` | Scrub 完整性校验使用此 MD5 值，掩盖真实数据损坏 |
| **触发错误解压缩** | 对二进制对象设置 `_aero_content_encoding=gzip` | GET 时尝试解压缩非 gzip 数据，流式读取失败（方向二） |
| **元数据长度耗尽** | 注入大量 `_aero_` 键（因它们被豁免大小限制） | 使 `MaxMetadataSize` 保护形同虚设，耗尽存储 |

### 现状

**1. `validateMetadata` 对 `_aero_` 键只豁免不拒绝：**

```go
// internal/service/file.go:105-120
func validateMetadata(meta map[string]string) error {
    if len(meta) == 0 {
        return nil
    }
    var total int
    for k, v := range meta {
        if strings.HasPrefix(k, "_aero_") {
            continue  // ← 跳过大小检查，但也不拒绝！用户可以注入任意 _aero_ 键
        }
        // size checks for non-_aero_ keys...
    }
    return nil  // ← _aero_ 键永远通过验证
}
```

注释说明 `// System keys (prefixed _aero_) are exempt.` 含义是豁免系统键免于大小限制——但实现上完全接受任意 `_aero_` 键值对，没有任何白名单或拒绝机制。

**2. REST handler 无过滤地提取元数据：**

```go
// internal/api/rest/handler.go:848-866
func extractMetadataHeaders(h http.Header) map[string]string {
    out := map[string]string{}
    for k, v := range h {
        // ...
        case strings.HasPrefix(lower, "x-amz-meta-"):
            out[strings.TrimPrefix(lower, "x-amz-meta-")] = v[0]
        case strings.HasPrefix(lower, "x-meta-"):
            out[strings.TrimPrefix(lower, "x-meta-")] = v[0]
        // 无 _aero_ 过滤！
    }
    return out
}
```

**3. S3 handler 同样无过滤：**

```go
// internal/api/s3compat/handler.go:700-718
func extractMetaHeaders(h http.Header) map[string]string {
    out := map[string]string{}
    for k, v := range h {
        if strings.HasPrefix(lower, "x-amz-meta-") {
            out[strings.TrimPrefix(lower, "x-amz-meta-")] = v[0]
            // 无 _aero_ 过滤！
        }
    }
    return out
}
```

**4. 在 GET/HEAD 响应中 `_aero_` 键被过滤（输出过滤存在但输入过滤缺失）：**

```go
// internal/api/s3compat/handler.go:686-690
func writeS3ObjectMeta(w http.ResponseWriter, meta map[string]string) {
    for k, v := range meta {
        if strings.HasPrefix(k, "_aero_") {
            continue  // ← 输出时过滤，但注入已成功
        }
        w.Header().Set("x-amz-meta-"+k, v)
    }
}
```

### 架构权衡

**建议方案：输入过滤 + 白名单 + 审计**

**方案 A（推荐）：在元数据提取入口处过滤 `_aero_` 键**

```
PUT 请求 → extractMetadataHeaders / extractMetaHeaders → 过滤 _aero_ 前缀
                                                        ↓
                                                validateMetadata → 拒绝 _aero_ 键
                                                        ↓
                                                service.Put → 写入存储
```

**两个过滤点，防御深度：**

1. **协议层过滤**（`extractMetadataHeaders` / `extractMetaHeaders`）：
   ```go
   // 在提取循环中直接拒绝 _aero_ 键
   key := strings.TrimPrefix(lower, "x-amz-meta-")
   if strings.HasPrefix(key, "_aero_") {
       continue  // 静默丢弃系统保留键
       // 或日志告警（当发现客户端尝试注入时）
   }
   ```

2. **服务层拒绝**（`validateMetadata`）——作为第二道防线：
   ```go
   func validateMetadata(meta map[string]string) error {
       for k := range meta {
           if strings.HasPrefix(k, "_aero_") {
               return fmt.Errorf("%w: reserved key prefix _aero_", ErrInvalidArgs)
           }
       }
       // ...现有大小检查...
   }
   ```

**方案 B（更严格）：维护显式白名单**

```go
var allowedSystemKeys = map[string]bool{
    "_aero_content_disposition": true,
    "_aero_content_encoding":    true,
    "_aero_content_md5":         true,   // 仅服务端写入
    "_aero_legal_hold":          true,   // 仅通过 x-amz-object-lock-legal-hold 设置
    "_aero_scrub_status":        true,   // 仅 Scrub 写入
}
```

在 PUT 路径上拒绝所有不在白名单中的 `_aero_` 键，同时阻止用户写入只能由服务端内部写入的键（如 `_aero_scrub_status`、`_aero_content_md5`）。

**方案 C（最终阶段）：元数据版本与来源追踪**

```go
type MetadataEntry struct {
    Key       string    `json:"k"`
    Value     string    `json:"v"`
    Source    string    `json:"s"` // "user" | "system" | "header"
    UpdatedAt time.Time `json:"t"`
}
```

分离用户元数据和系统元数据存储到不同字段/列，从架构上消除命名空间冲突。这是一个较大的重构但最彻底。

**推荐路径：**

| Phase | 内容 | 危险键覆盖 | 改动量 |
|-------|------|-----------|--------|
| **P0（热修复）** | 在 `extractMetadataHeaders` / `extractMetaHeaders` / `validateMetadata` 三个函数中过滤 `_aero_` 输入 | 100% | 每个函数 2-3 行 |
| **P1** | `PUT /v1/admin/metadata/schema` API + 白名单校验 | 100% | ~200 行 |
| **P2** | 用户元数据与系统元数据列分离（DB migration） | 100% | migration + 重构 |

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| **合法系统操作需要写入 `_aero_` 键** | 内部服务代码（Scrub、Legal Hold、MD5 存储）通过 `repo.SetObjectMetaKey` 直接写入，不经过用户输入的 `validateMetadata`——不影响 |
| **S3 `x-amz-object-lock-legal-hold: ON` 流** | S3 handler 在调用 `service.Put` 之前独立将 legal hold 写入 meta（`handler.go:96-99`）——在协议层过滤之后、服务层调用之前注入，需要确保 filter 之后系统路径仍可设置 `_aero_legal_hold`。解决方案：在 `extractMetaHeaders` 过滤后，`s3PutMeta` 仍可添加系统键——服务层 `validateMetadata` 需要区分"用户注入"和"系统添加"。更简洁：不在 `validateMetadata` 拒绝所有 `_aero_` 键，而是在协议层过滤后让系统路径保留注入能力 |
| **现有对象已有用户注入的 `_aero_` 键** | 热修复前的注入已持久化。修复后需提供迁移脚本或 Reconcile 任务扫描并清理这些键 |
| **审计跟踪** | 检测到 `_aero_` 注入尝试时记录 audit 日志（`action: metadata_injection_attempted`, `detail: {"key": "_aero_scrub_status", "value": "healthy"}`） |
| **S3 SDK 自动发送 `x-amz-meta-*` 头** | AWS SDK 客户端可能自动将用户添加的元数据以 `x-amz-meta-` 前缀发送。如果用户在 SDK 中设置了 `_aero_` 开头的元数据键，过滤后静默丢弃——需在响应中告知（如 `x-amz-meta-ignored-keys` 头） |

---

## 方向二：Content-Encoding 感知 Range 请求

### 产品价值

HTTP Range 请求是大型对象存储的核心能力——支持视频 seek、PDF 分页加载、断点续传、CDN 缓存。当前系统对 gzip 压缩对象的 Range 请求返回**语义错误的字节范围**，因为偏移量基于压缩大小计算但正文已被解压。

| 场景 | 当前行为 | 期望行为 |
|------|---------|---------|
| **10MB gzip 压缩对象（未压缩 100MB）请求 `Range: bytes=0-5000000`** | 服务端从压缩流读 0-5MB 位置，解压后返回约 50MB 数据（实际偏移严重偏移） | 要么返回 206（解压后 0-5MB 位置），要么返回 200（不支持 Range）并说明原因 |
| **视频 seek 到 50% 位置** | 偏移量严重错误，客户端收到错误帧 | 精确 seek |
| **CDN 回源并发请求不同的 Range** | 不同客户端收到重叠/错位的内容 | 每个 Range 请求独立正确 |
| **客户端使用 `--continue-at` 断点续传** | 续传起始点与实际存储偏移不匹配，文件损坏 | 续传位置精确 |

### 现状

**1. `Get` 方法自动解压 gzip 对象：**

```go
// internal/service/file_crud.go:248-261
func (s *FileService) Get(ctx context.Context, tenant, bucket, key string) (io.ReadCloser, repository.Object, error) {
    // ...获取存储 reader...
    if obj.Metadata["_aero_content_encoding"] == "gzip" {
        gr, err := gzip.NewReader(rc)  // ← 每次自动解压
        // ...
        rc = &gzipReadCloser{gr, rc}  // ← 替换为解压 Reader
    }
    return rc, obj, nil
}
```

**2. `GetRange` 在已解压的流上计算偏移：**

```go
// internal/service/range.go:77-90
func (s *FileService) GetRange(ctx context.Context, tenant, bucket, key string, offset, length int64) (io.ReadCloser, repository.Object, error) {
    rc, obj, err := s.Get(ctx, tenant, bucket, key)  // ← 获取自动解压后的流
    // ...
    if offset > 0 {
        if _, err := io.CopyN(io.Discard, rc, offset); err != nil {  // ← offset 基于压缩大小
            // ...
        }
    }
    if length >= 0 {
        return &limitReadCloser{r: io.LimitReader(rc, length), c: rc}, obj, nil  // ← length 基于压缩大小
    }
    return rc, obj, nil
}
```

**3. 问题根因：**

```
储层:          [--- 压缩数据 10MB ---]  (storage_key 下的 blob)
Range 请求:    bytes=0-5242880        (客户端期望 0-5MB 解压后偏移)
                      ↓
服务端 Get:    gzip.NewReader(rc)     (解压为 100MB)
                      ↓
GetRange:      io.CopyN(Discard, rc, 5242880)  (跳过 5MB 解压数据)
              io.LimitReader(rc, 5242881)      (读取到 10MB+ 解压数据)
                      ↓
返回:          [--- 约 5MB 解压数据 ---]
              BUT 客户端预期偏移 0-5MB = 前 5MB 解压数据
              ACTUAL 前 5MB 压缩数据 = 约 50MB 解压数据
              → 客户端实际收到的是 50-55MB 区域的内容
```

**4. 响应头与内容不一致：**

```go
// internal/api/rest/handler.go:836-843
func writeContentResponseHeaders(w http.ResponseWriter, meta map[string]string) {
    if v, ok := meta["_aero_content_encoding"]; ok && v != "" {
        w.Header().Set("Content-Encoding", v)  // ← 告诉客户端 Content-Encoding: gzip
        // 但实际下发的是解压后字节！
    }
}
```

HTTP 规范 RFC 7233 §4.1 要求：当响应包含 `Content-Encoding: gzip` 时，Range 计算的编码层在传输编码之前。但在此实现中，`Content-Encoding` 头说明内容需解压，而实际下发的是已解压的裸字节——响应与 header 矛盾。

### 架构权衡

**建议方案：Content-Encoding 感知的 Range 处理**

**方案 A（推荐）：遇到 gzip 压缩对象 + Range 请求时降级为 200（完整响应）**

```go
func (s *FileService) GetRange(ctx context.Context, tenant, bucket, key string, offset, length int64) (io.ReadCloser, repository.Object, error) {
    rc, obj, err := s.Get(ctx, tenant, bucket, key)
    // ...
    // 解压后流不支持字节精确定位
    if isDecompressedStream(rc) {
        // 返回完整对象 + 响应头提示 Range 不受支持
        return rc, obj, nil  // caller 检测到无 Range 支持后返回 200
    }
    // ...
}
```

而协议层（REST/S3 handler）检测 gzip + Range 组合后：
- 返回 `200 OK` 完整响应而不是 `206 Partial Content`
- 可选：添加 `Warning: 299 - "Range not supported for gzip-encoded objects"` 头

**方案 B（中投入）：存储时保留双视图**

对象写入时存储两份：
1. 压缩流（现有逻辑）
2. 解压后大小（元数据字段 `_aero_decompressed_size`）

GET 时根据客户端是否发送 `Accept-Encoding: gzip` 决定：
- 有 `Accept-Encoding: gzip`：返回压缩流，`Content-Encoding: gzip`，支持正确 Range
- 无 `Accept-Encoding: gzip`：返回解压流，无 `Content-Encoding`，支持正确 Range

```go
type PutOptions struct {
    // ...现有字段...
    CompressedSize     int64  // 压缩后的大小（当客户端自动发送时记录）
    DecompressedSize   int64  // 解压后的大小（上传元数据记录）
}
```

**方案 C（高投入）：不做自动解压，仅透传**

移除 `Get` 中的 gzip 自动解压逻辑。由客户端自行决定是否解压（通过 `Accept-Encoding` 协商），类似标准 S3 行为。这是最符合 HTTP 语义的做法，但会破坏依赖自动解压的现有调用方。

**推荐路径：**

| Phase | 内容 | 影响 |
|-------|------|------|
| **P0（热修复）** | 当检测到 gzip 对象 + Range 请求时降级为 200（2-3 行判断） | 消除数据损坏，代价是 Range 降级 |
| **P1** | PUT 时记录解压后大小（`_aero_decompressed_size`）+ GET 时支持 `Accept-Encoding` 协商 | 恢复 Range 功能，保持向后兼容 |
| **P2** | 存储带宽优化：客户端请求未压缩范围时，在服务端解压后切分而非流式跳过 | 大文件 seek 性能提升 |

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| **客户端发送 `Accept-Encoding: gzip` 且对象是 gzip 压缩的** | 返回原始压缩流（不解压），`Content-Encoding: gzip`，支持正确 Range |
| **客户端无 `Accept-Encoding` 且对象是 gzip 压缩的** | P0：降级 200；P1：解压后返回无编码裸数据 |
| **zstd / deflate / br 等其他编码** | 统一处理框架，不仅限于 gzip |
| **分块上传的 gzip 对象** | 分块合并后整个文件是 gzip 流——不影响 Range 处理（最终合并的文件仍为 gzip 格式） |
| **ETag 与解压后内容的匹配** | 现有 ETag 基于压缩流计算；如果返回解压内容，ETag 应返回压缩流的值以保持一致性，或引入第二个 ETag_Decompressed |

---

## 方向三：对象键安全：字符编码、长度限制与规范化

### 产品价值

对象键（Object Key）是对象存储的身份标识——所有操作（PUT/GET/DELETE/LIST）都基于键。键验证的薄弱面导致：

| 风险 | 后果 |
|------|------|
| **控制字符注入**（0x00-0x1F） | 文件系统路径异常、日志解析注入、协议解析异常 |
| **超长键**（>1024 UTF-8 bytes） | 本地文件系统路径超限（`ENAMETOOLONG`，通常 255 bytes/component），数据库索引性能下降 |
| **Unicode 等价攻击** | 通过 NFC/NFD 编码差异绕过权限检查（如 `Café.txt` NFC vs `Cafe\u0301.txt` NFD 在不同系统解析为不同键） |
| **尾随空格/点**（Windows 兼容性） | S3 客户端在 Windows 上会自动去除尾随空格和点，导致键指向不一致 |
| **UTF-8 编码无效**（如 0xFE 0xFF） | 数据库索引失效、JSON 序列化失败、HTTP 路径解析异常 |
| **斜杠规范化**（双斜杠 `/a//b` vs `/a/b`） | 一个对象有两个可访问的键，权限绕过 |

### 现状

**1. `validateKey` 仅检查 3 件事：**

```go
// internal/service/file.go:129-134
func validateKey(key string) error {
    if key == "" {
        return fmt.Errorf("%w: empty key", ErrInvalidArgs)
    }
    if strings.Contains(key, "..") || strings.HasPrefix(key, "/") {
        return fmt.Errorf("%w: illegal key %q", ErrInvalidArgs, key)
    }
    return nil
}
```

不验证：长度、无效字符、编码规范性、Unicode 均衡。

**2. 存储层直接使用键作为文件路径：**

```go
// internal/service/file.go:143
func storageKey(tenant, bucket, key string) string {
    if !strings.HasPrefix(key, "/") {
        return path.Join(tenant, bucket, key)  // ← 直接拼接到文件系统路径
    }
    return path.Join(tenant, bucket, strings.TrimPrefix(key, "/"))
}
```

`path.Join` 清理双斜杠和 `.`/`..`，但不保证：
- 结果路径不超出文件系统最大路径长度（通常 `PATH_MAX` = 4096，或 ext4 单组件 255 bytes）
- 键中不含 `\0`（字符串在 Go 中允许但 POSIX 文件系统拒绝）

**3. `keyFromPath` 通过 URL 自动解码：**

```go
// internal/api/rest/handler.go:35
func keyFromPath(r *http.Request) string {
    return chi.URLParam(r, "*")
}
```

`chi.URLParam` 在路由匹配时自动 URL 解码——编码的 `%00`（空字节）变成 Go 字符串中的 `\x00`，可绕过 `validateKey` 的检查。

**4. S3 侧直接通过 `chi.URLParam` 获取键：**

```go
// internal/api/s3compat/handler.go:660-662
func keyFromURL(r *http.Request) string {
    k := chi.URLParam(r, "*")
    return strings.TrimPrefix(k, "/")
}
```

### 架构权衡

**建议方案：三层键验证模型**

```
┌──────────────────────────────────────┐
│  Layer 1: HTTP 级                    │
│  拒绝控制字符、验证 URL 编码正确性    │
├──────────────────────────────────────┤
│  Layer 2: Service 级 (validateKey)   │
│  长度限制、Unicode 规范化、字符白名单  │
├──────────────────────────────────────┤
│  Layer 3: Storage 级                 │
│  storageKey 对键做确定性转义           │
└──────────────────────────────────────┘
```

**验证规格（参考 S3 标准）：**

```go
const (
    MaxKeyLength        = 1024   // UTF-8 bytes, S3 规范
    MaxKeySegmentLength = 255    // 文件系统单组件限制
    MaxURLLength         = 2048  // 常见 HTTP 代理限制
    
    // 控制字符和不可见字符
    charBlacklist = "\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0A\x0B\x0C\x0D\x0E\x0F" +
                    "\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1A\x1B\x1C\x1D\x1E\x1F" +
                    "\x7F\xFE\xFF"
)
```

**推荐验证顺序：**

```go
func validateKey(key string) error {
    if key == "" {
        return errEmptyKey
    }
    if len(key) > MaxKeyLength {
        return errKeyTooLong
    }
    if strings.ContainsAny(key, charBlacklist) {
        return errInvalidChar
    }
    if !utf8.ValidString(key) {
        return errInvalidUTF8
    }
    // NFC 规范化检查（不对键做自动 NFD→NFC 转换——那会改变键本身）
    if !norm.NFC.IsNormalString(key) {
        // 可选择自动规范化或拒绝
        return errNotNormalized
    }
    // 检查各路径段长度
    for _, seg := range strings.Split(key, "/") {
        if len(seg) > MaxKeySegmentLength {
            return errSegmentTooLong
        }
    }
    // 检查尾随空格/点（安全地在各段上）
    for _, seg := range strings.Split(key, "/") {
        trimmed := strings.TrimRight(seg, ". ")
        if trimmed != seg {
            return errTrailingChar
        }
    }
    // 保留现有检查
    if strings.Contains(key, "..") || strings.HasPrefix(key, "/") {
        return errIllegalKey
    }
    return nil
}
```

**Unicode 规范化处理策略：**

| 策略 | 优点 | 缺点 |
|------|------|------|
| **存储时不规范化，比较时 NFC 化** | 保持用户输入的原始键 | 查询时需规范化，索引中有 NFC/NFD 混合键 |
| **存储前强制 NFC 化** | 键一致，索引有效 | 改变用户上传的键（向后兼容性问题） |
| **拒绝非 NFC 键** | 明确拒绝，用户修正后重试 | 需要更新 SDK/文档 |

推荐策略（与 S3 一致）：**存储时不转换，LIST 比较/索引时做 NFC 相等性检查**。检测到非 NFC 键时记录告警但不拒绝。

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| **现有键中存在控制字符** | 热修复后拒绝新此类键的写入；对现有键提供 Reconcile 扫描和重命名（或删除）工具 |
| **Windows 客户端通过 WebDAV 写入尾随空格键** | WebDAV handler 端增加额外检测（WebDAV 多用于 Windows） |
| **S3 SDK 自动对键进行 URL 编码** | AWS SDK 会编码键中的特殊字符，但 Go 的 `chi.URLParam` 自动解码——服务端看到的是解码后的原始键，不影响验证 |
| **键含 `/` 前缀（`/a/b` vs `a/b`）** | 当前剥离前导 `/`——但如果客户端连续发送 `//a//b`，`path.Clean` 会规范化但 `validateKey` 未检查双斜杠 |
| **LIST 返回的键顺序** | 规范化后应注意 LIST 的排序不受键本身编码影响 |

---

## 方向四：分段上传孤子生命周期：自动清理与存储泄漏检测

### 产品价值

分段上传（Multipart Upload）是大型对象传输的核心机制。当客户端启动分段上传后从未完成或取消，分片数据在存储后端永久存留。对于 S3 后端，AWS 自动在 7 天后清理孤子；但对于 Local、OSS、COS 后端，**分片永不自动清理**。

| 场景 | 存储泄漏量 | 恢复方式 |
|------|-----------|---------|
| **客户端崩溃（上传 10GB 文件到 50% 时断开）** | ~5GB | 当前：手动 AbortMultipart API（需知道 uploadID） |
| **SDK 错误未调用 CompleteMultipart** | 全部分片大小 | 当前：无法恢复 |
| **恶意客户端反复 InitMultipart 但不完成** | 任意量（DOSS 攻击） | 当前：无上限保护 |
| **Web UI 拖拽上传未实现分片** | 单次上传对象 | 当前：无分片上传执行按钮 |
| **Local 后端测试环境积累** | 持续增长 | 当前：需人工清理 `.multipart/` 目录 |

**预计成本影响：** 在活跃使用的系统中，孤子上传可占用总存储的 **5-15%**，且随客户端故障率线性增长。

### 现状

**1. `uploads` 表无到期/过期字段：**

```sql
-- internal/repository/sql_uploads.go — 当前 schema
CREATE TABLE uploads (
    id              TEXT PRIMARY KEY,   -- upload_id
    tenant_id       TEXT NOT NULL,
    bucket          TEXT NOT NULL,
    key             TEXT NOT NULL,
    backend         TEXT NOT NULL,
    backend_uid     TEXT NOT NULL,
    storage_key     TEXT NOT NULL DEFAULT '',
    metadata        TEXT,
    created_at      TEXT               -- ← 存在不被利用
    -- ❌ 无 expires_at
    -- ❌ 无 last_activity_at
    -- ❌ 无 status（completed / aborted / abandoned）
);
```

**2. `Reconcile` 完全不扫描分段上传：**

```go
// internal/reconcile/job.go — sweep 方法
func (j *Job) sweep(ctx context.Context) {
    // 仅扫描:
    j.sweepOrphanRows(ctx, t)   // 元数据行 vs 存储对象
    j.sweepOrphanBlobs(ctx, t)  // 存储对象 vs 元数据行
    j.scrubAll(ctx, t, j.scrub) // 数据完整性校验
    // ❌ 无 sweepOrphanUploads
}
```

**3. Local 后端存储分片在临时目录且永不清理：**

```go
// internal/storage/local_multipart.go — 分片存储在
// <root>/.multipart/<uploadID>/<partNumber>
// 从未有过自动清理路径
```

S3 后端通过 AWS 侧 7 天 TTL 提供了后端级保护，但 Local、OSS、COS 无此保障。

**4. 无孤儿上传的管理 API：**

```go
// internal/api/rest/admin_jobs.go — 现有管理 API
// ListJobs, RetryJob, ListWebhookFailures
// ❌ 无 ListAbandonedUploads
// ❌ 无 AbortAllUploads
// ❌ 无 AbortUploadByID
```

### 架构权衡

**建议方案：三层防护**

**1. 数据模型扩展：**

```sql
-- migration: uploads 表增加自动过期支持
ALTER TABLE uploads ADD COLUMN expires_at TEXT;      -- 自动到期时间
ALTER TABLE uploads ADD COLUMN last_activity_at TEXT; -- 最后上传分片时间
ALTER TABLE uploads ADD COLUMN status TEXT DEFAULT 'active';  -- active | completed | aborted | expired
```

**2. `InitMultipart` 设置到期时间：**

```go
const DefaultUploadExpiry = 24 * time.Hour  // 默认 24 小时未完成则过期

func (s *FileService) InitMultipart(ctx context.Context, tenant, bucket, key string, opts PutOptions) (repository.Upload, error) {
    // ...现有逻辑...
    u := repository.Upload{
        // ...
        CreatedAt:       time.Now(),
        ExpiresAt:       time.Now().Add(DefaultUploadExpiry),
        LastActivityAt:  time.Now(),
        Status:          "active",
    }
    // ...
}
```

**3. `Reconcile` 新增上传清扫器：**

```go
type UploadCleaner struct {
    repo              repository.Repository
    store             storage.Storage
    maxUploadAge      time.Duration  // 上传最长允许时间（默认 24h）
    maxUploadIdleTime time.Duration  // 无活动时间（默认 2h）
    batchSize         int            // 每批扫描数（默认 100）
}

func (uc *UploadCleaner) sweepExpired(ctx context.Context) (aborted int, errors int) {
    // 查询过期上传：expires_at < now 或 last_activity_at < now - maxUploadIdleTime
    expired, err := uc.repo.ListExpiredUploads(ctx, uc.batchSize)
    for _, u := range expired {
        // 1. 调用存储后端 AbortMultipart（清理后端分片）
        _ = uc.store.AbortMultipart(ctx, u.StorageKey, u.BackendUID)
        // 2. 标记 upload 状态为 expired
        _ = uc.repo.MarkUploadExpired(ctx, u.ID)
        // 3. 记录 OTel 指标
        telemetry.IncAbandonedUploads(context.Background(), u.TenantID)
    }
}
```

**4. 管理 API：**

```go
// GET /v1/admin/uploads — 列出所有上传（按租户/桶过滤）
// DELETE /v1/admin/uploads/{uploadID} — 手动终止上传
// POST /v1/admin/uploads/expire — 触发立即清扫所有过期上传
```

**5. 事件通知：**

当上传因过期而被中止时，发布 `upload.expired` 事件，可集成 webhook 通知。

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| **上传进行中（客户端正在上传分片）但接近到期** | 扩展 `UploadPart` 更新 `last_activity_at`——只要客户端持续上传分片，LRU 时间戳后延 |
| **到期阈值设置太短导致大型分片上传被误杀** | 对于正在上传且 `last_activity_at` 新鲜的上传，忽略 `expires_at`——仅检查 LRU。推荐：`defaultUploadIdleTime = 2h`（2 小时无活动视为放弃） |
| **同一对象多次 InitMultipart** | S3 允许多个并行分片上传同一对象（各有不同的 uploadID）。每个独立生命周期 |
| **集群环境下 Reconcile 清扫冲突** | 已由 `RECONCILE_CLUSTER_SINGLETON` 保护——仅 leader 执行清扫 |
| **清除后客户端仍尝试 CompleteMultipart** | `CompleteMultipart` 检查 upload 状态——如为 `expired` 返回 `ErrUploadNotFound` |
| **S3 后端已有 7 天 TTL vs 本地策略冲突** | 后端 TTL 是最低保障，服务端策略应更严格（默认 24h） |

---

## 方向五：Web UI 管理面控制台

### 产品价值

当前 Web UI（`/ui`）是一个 282 行单页 SPA，提供基本的对象浏览、搜索、聊天和血缘追踪。在项目早期这是极好的开发工具和演示界面。但作为一个面向运维和管理员的产品，缺少管理能力导致开发者始终需要返回终端。

**竞品对比：**

| 能力 | 当前 Web UI | MinIO Console | 期望状态 |
|------|------------|---------------|---------|
| 桶管理（创建/删除/配置） | ❌ | ✅ | ✅ |
| 生命周期规则配置 | ❌ | ✅ | ✅ |
| 对象 ACL / Policy | ❌ | ✅ | ✅ |
| API 密钥管理 | ❌ | ✅ | ✅ |
| 租户管理 | ❌ | ❌（MinIO 无多租户） | ✅ |
| 审计日志查看 | ❌ | ❌ | ✅ |
| 作业队列状态 | ❌ | ❌ | ✅ |
| 存储用量仪表盘 | ✅ 基础计数 | ✅ 图表 | ✅ 图表 |
| 可恢复上传 | ❌ | ❌ | ✅ |
| 对象预览（文本/图像） | ❌ | ✅ 文本/图像/JSON | ✅ |
| 暗色模式 | ✅ | ✅ | ✅ |
| 响应式设计 | ❌ | ✅ | ✅ |

### 现状

**1. 单文件 282 行 SPA——纯静态：**

```html
<!-- internal/webui/static/index.html — 整体结构 -->
<header>
  <h1>aero-vault</h1>
  <label>tenant <input id="tenant" type="text" value="default"></label>
  <label>api key <input id="apikey" type="text" placeholder="(optional)"></label>
  <button onclick="refresh()">refresh</button>
</header>
<main>
  <aside>    <!-- 文件列表 + 上传表单 -->
    <div class="group"><h3>upload</h3>...</div>
    <div class="group"><h3>objects</h3>...</div>
  </aside>
  <section>  <!-- 4 个标签页 -->
    <div class="tabs">
      <div class="tab active" onclick="showTab('search')">Search</div>
      <div class="tab" onclick="showTab('detail')">Detail</div>
      <div class="tab" onclick="showTab('lineage')">Lineage</div>
      <div class="tab" onclick="showTab('chat')">Chat</div>
    </div>
    <!-- 每个标签页的内容区域 -->
  </section>
</main>
```

**2. 当前 Web UI 的 API 调用方式（无 SDK 封装）：**

```javascript
// 所有 API 调用硬编码 fetch 调用
function api(path, opts) {
    const h = {'X-Aero-Tenant': document.getElementById('tenant').value};
    if (apikey) h['Authorization'] = 'Bearer ' + apikey;
    return fetch((endpoint||'') + path, {...opts, headers: {...h, ...opts?.headers}});
}
// 每个 tab 的渲染函数各自调用 api()，无统一状态管理
```

**3. Web UI 完全不消费 admin API：**

```go
// internal/api/rest/router.go — 已注册但 UI 未消费的端点
r.Put("/v1/admin/tenants/{tenant}/quota",          h.SetQuota)
r.Get("/v1/admin/tenants",                         h.ListTenants)
r.Post("/v1/admin/tenants",                        h.CreateTenant)
r.Delete("/v1/admin/tenants/{tenant}",             h.DeleteTenant)
r.Put("/v1/admin/tenants/{tenant}/status",         h.SetTenantStatus)
r.Put("/v1/admin/tenants/{tenant}/budget",         h.SetTenantBudget)
r.Post("/v1/admin/keys",                           h.AddKey)
r.Get("/v1/admin/keys",                            h.ListKeys)
r.Delete("/v1/admin/keys/{keyID}",                 h.RevokeKey)
r.Post("/v1/admin/jwt",                            h.IssueJWT)
r.Get("/v1/admin/jobs",                            h.ListJobs)
r.Post("/v1/admin/jobs/{id}/retry",                h.RetryJob)
r.Get("/v1/admin/audit",                           h.ListAudit)
```

**4. 当前 Web UI 无显式版本管理：**

```go
// internal/webui/web.go — 无版本信息
//go:embed static/*
var staticFS embed.FS  // 无编译时间戳或无内容哈希
```

浏览器可能缓存旧的 `index.html`，导致用户看到过期 UI 而不自知。

### 架构权衡

**建议方案：渐进式 Web UI 增强**

**Phase 1（短周期，1-2 周）：当前 SPA 免重构优化**

| 改进 | 文件 | 影响 |
|------|------|------|
| 添加 Admin 标签页（空白占位 + 链接到 CLI 文档） | `index.html` + 10 行 JS | 用户知道管理功能存在 |
| 对象详情显示元数据（Tags、Content-Type、存储类） | `index.html` + 修改 `showDetail` | 对象信息透明 |
| 对象预览（文本内容通过 fetch GET + 语法高亮） | `index.html` + 30 行 JS | 可直接预览文本文件 |
| Bucket 选择器（当前硬编码 `default`） | `index.html` + API 调用 `GET /v1/buckets` | 多桶支持 |
| 错误提示（当前静默失败） | `index.html` + 简单通知组件 | 用户可见的错误反馈 |

这些改进均不引入新的前端依赖，保持单文件 SPA 结构，改动量 < 200 行。

**Phase 2（中周期，2-3 周）：引入轻量前端框架**

| 框架评估 | 优点 | 缺点 |
|---------|------|------|
| **Vanilla JS + Web Components** | 零依赖，与当前 embed 架构一致 | 缺少路由/状态管理 |
| **Alpine.js** | 15KB，声明式，可嵌入单 HTML | 学习曲线 |
| **htmx** | 后端驱动，最小化 JS | 需要后端点变更 |
| **Vue/React + Vite build** | 生态丰富 | 增加构建步骤，需要 Node.js |

推荐方案：**Vanilla JS + Lit-html（~8KB）** 或 **Alpine.js（~15KB）**，保持 `embed.FS` 单二进制交付模式。

**Phase 3（长周期，2-3 周）：完整的 Admin Dashboard**

```
/ 页面布局:
├── 侧边栏导航
│   ├── 📁 对象浏览 (Object Browser)    ← 当前 Search Tab
│   ├── 📊 仪表盘 (Dashboard)           ← 新：存储/请求/AI 用量图表
│   ├── 🪣 桶管理 (Buckets)              ← 新：创建/删除/配置
│   ├── 🔑 密钥管理 (API Keys)           ← 新：列出/添加/撤销
│   ├── 👥 租户管理 (Tenants)            ← 新：CRUD + 配额/预算
│   ├── 📋 审计日志 (Audit Log)          ← 新：查看/筛选/导出
│   ├── ⚙️ 作业 (Jobs)                  ← 新：状态/重试
│   ├── 💬 聊天 (Chat)                  ← 当前 Chat Tab
│   └── 🧬 血缘 (Lineage)              ← 当前 Lineage Tab
└── 主内容区
```

所有 admin API 已有（Phase 0 无需后端变更），Phase 3 的前端构建引入 `webpack` 或 `esbuild` 打包步骤，生成 minified bundle 后 `embed`。

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| **浏览器缓存过期的 UI** | 在 `index.html` URL 后附加内容 hash（如 `/ui/index.abc123.html`）或设置 `Cache-Control: no-cache` |
| **Web UI 在移动端** | Phase 1 维持桌面优先；Phase 3 引入媒体查询响应式布局 |
| **无 JS 环境** | 系统设计为 API-first，Web UI 是增强层；**不要求** 无 JS 回退 |
| **嵌入到 iframe 的 XSS 风险** | 服务端设置 `Content-Security-Policy: frame-ancestors 'self'` |
| **长期运行后的内存泄漏** | SPA 中订阅 SSE 事件流后不再取消——在 `beforeunload` 中清理 EventSource |

---

## 优先级与建议执行顺序

| 排序 | 方向 | 前置依赖 | 建议投入 | 核心交付物 |
|------|------|---------|---------|-----------|
| **1** | **P0 热修复集合（方向一 + 方向二 P0 + 方向三 P0）** | 无 | 2-3 天 | `_aero_` 注入过滤（3 个函数）、gzip+Range 降级 200（`GetRange` 2 行判断）、`validateKey` 控制字符+长度检查（~15 行） |
| **2** | **方向四：分段上传孤子清理** | `uploads` 表有 `created_at`（已有） | 1-2 周 | `ListExpiredUploads` + `UploadCleaner.sweepExpired` + Reconcile 集成 + `expires_at`/`last_activity_at` 字段 |
| **3** | **方向三完整：对象键规范化体系** | P0 热修复完成 | 1-2 周 | Unicode NFC 检测 + 分段长度检查 + 存储层转义 + 向后兼容数据迁移 |
| **4** | **方向一完整 + 方向二完整** | P0 热修复完成 | 2-3 周 | 元数据白名单（方案 B）+ `Accept-Encoding` 协商 + 解压后大小记录 |
| **5** | **方向五：Web UI 管理控制台** | 无后端变更依赖 | 3-4 周 | Admin 标签页（Phase 1）+ 对象预览 + Bucket 选择器 → Phase 2 轻量框架 → Phase 3 完整管理面板 |

**建议执行策略：**

1. **Phase 0（紧急热修复，2-3 天）**：方向一、二、三的 P0 修复。这些是安全漏洞和正确性 bug，应优先于任何新功能。三个修复都只需 2-15 行代码修改，测试验证即可上线。

2. **Phase 1（方向四 + 方向三完整，2-3 周）**：分段上传孤子清理是存储成本泄漏的修复，对象键规范化是安全基础。两个方向共享数据模型变更（字段添加），可并行。

3. **Phase 2（方向一/二完整 + 方向五 Phase 1，3-4 周）**：元数据白名单和 Accept-Encoding 协商是对 P0 修复的深化。Web UI 增强并行推进，利用已有的完整 admin API。

---

## 总结

以上五个方向覆盖了 aero-vault 在**安全（`_aero_` 注入）、数据完整性（Content-Encoding Range）、输入验证（对象键规范）、存储治理（分段上传孤立）、产品体验（Web UI 管理面）** 五个维度的关键缺口。与前 103 轮分析无实质重叠，且在代码库中有明确、可操作的锚点。

| 维度 | 当前状态 | 目标状态 |
|------|---------|---------|
| **元数据安全** | `_aero_` 前缀可被任意用户覆盖 | 系统保留键不可被外部注入；用户元数据与系统元数据隔离 |
| **Range 正确性** | gzip 对象 + Range 请求返回错误偏移 | 降级 200 或 Accept-Encoding 协商，零数据损坏 |
| **对象键安全** | 6 行 `validateKey`，无字符/长度/Unicode 检查 | 三层验证：控制字符拒绝 + 长度限制 + Unicode 规范性 |
| **存储泄漏** | 分段上传从未自动清理 | Reconcile 清扫 + 到期策略 + 管理 API |
| **管理体验** | 282 行 SPA，无管理能力 | 完整控制台：桶/密钥/租户/审计/作业管理 |
