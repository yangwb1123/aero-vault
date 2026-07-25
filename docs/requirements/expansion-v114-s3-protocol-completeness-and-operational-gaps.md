# 高价值扩展方向：S3 协议完备性、数据完整性校验与运营治理盲区

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — 280+ Go 源文件，完整装配链路 `cmd/server/main.go`，全部子包 `internal/`（storage/repository/service/api/ai/auth/middleware/events/jobs/reconcile/replication/mcp/cli/webui），3 套 SDK（Go/Python/JS），MCP 双模式（HTTP+stdio），WebDAV，Web UI，24 对迁移文件，`deploy/` 全套 Helm/Grafana/Prometheus 配置  
> **去重验证：** 对 `docs/requirements/` 下全部既有分析文档进行逐方向关键词 + 代码锚点交叉验证  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。每个方向必须包含：**代码中存在的具体实现锚点**、**可量化的生产/产品影响**、**架构权衡与边界情况**。

---

## 方法论：从"API 骨架"到"协议真实性"

AeroVault 的功能矩阵已经非常完整，覆盖了对象 CRUD、多协议适配（REST/S3/WebDAV/MCP）、AI/RAG 管线、事件驱动治理和运维观测等核心领域。前 112+ 轮分析覆盖了绝大多数功能缺口、性能问题和架构盲区。

本文聚焦的是一类尚未被系统覆盖的缺口：**API 的骨架（接口定义、路由注册、配置存储）存在，但协议语义在运行时是空心的——响应行为不匹配标准、客户端兼容性断裂、运营资源泄漏无人治理。**

| 缺口类型 | 判定标准 | 本文方向 |
|----------|---------|----------|
| **协议兼容性断裂** | S3 标准客户端因缺失路由模式或 API 端点而完全无法工作 | Directions 1, 4 |
| **数据完整性盲区** | 对象存储全链路（写入→持久化→读取）中缺少端到端校验和验证 | Direction 2 |
| **合规审计缺失** | 配置层存储了审计/合规配置，但实现层是空操作（no-op） | Direction 3 |
| **资源泄漏无人治理** | 用户操作创建了后端资源，但系统无任何回收机制 | Direction 5 |

### 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 关键代码锚点 |
|---|------|------|--------|---------|-------------|
| **1** | **Virtual Hosted-Style S3 请求路由** —— 仅支持 Path-Style（`/s3/bucket/key`），现代 S3 SDK 默认使用 Virtual Hosted-Style（`bucket.s3.amazonaws.com/key`），完全无实现 | 协议兼容性 | **P0** | AWS CLI v2、boto3、aws-sdk-go-v2 默认使用 Virtual Hosted-Style，与 AeroVault 的 Path-Style 模式不兼容。用户必须手动配置 `--endpoint-url` + `force_path_style`，且部分 SDK（如 JS SDK v3）的某些操作在 Path-Style 下行为异常 | `internal/api/s3compat/router.go:1-28`（`NewRouter` — 仅 `/{bucket}` 和 `/{bucket}/*` 路径模式，无虚拟主机头解析）；`internal/api/s3compat/handler.go:1-15`（`Handler` — 无 `Host` 头解析逻辑）；`cmd/server/main.go:202-204`（`buildRouter` — `r.Mount(cfg.S3Compat.Prefix, …)` 固定前缀 `/s3`，无虚拟主机路由）；`internal/config/config.go`（`S3CompatConfig` — 仅 `Prefix` 字段，无 `VirtualHostedDomain` 或 `UseVirtualHostedStyle`）；`internal/middleware/middleware.go:1-30`（中间件链 — tenant 解析通过 Header `X-Aero-Tenant`，不解析 S3 虚拟主机中的 tenant/bucket）|
| **2** | **S3 Flexible Checksum API** —— 仅支持 `Content-MD5` 和 `x-amz-checksum-md5`，无 CRC32/CRC32C/SHA1/SHA256，无读取验证 | 数据完整性/协议补全 | **P1** | 2023 年起 AWS S3 默认使用 CRC32 作为校验和算法，要求服务端校验 `x-amz-checksum-crc32` / `x-amz-checksum-crc32c` / `x-amz-checksum-sha1` / `x-amz-checksum-sha256`。当前仅支持 MD5，缺失完整算法套件和读取路径上的校验和验证。这影响使用 aws-sdk-go-v2 的客户端（SDK 默认启用校验和）以及需要端到端数据完整性保证的企业场景 | `internal/api/s3compat/handler.go:680-700`（`writeS3ObjectMeta` — 仅输出 `x-amz-checksum-md5`，无其他 checksum 算法）；`internal/api/s3compat/handler.go:686`（注释 `"sets x-amz-checksum-md5 when"` — 明确只做了 MD5）；`internal/api/s3compat/handler.go:465-486`（`PutObject` — 仅读 `Content-MD5` header，不读 `x-amz-checksum-*` 系列）；`internal/storage/storage.go:1-80`（`Storage` 接口 — `Put` 和 `Get` 参数/返回值均无 checksum 字段）；`internal/service/file_crud.go:63-80`（`md5WrapReader` — 仅 MD5 验证，无 CRC32 等）；`internal/repository/repository.go:Object`（Object 结构体 — 无 `ChecksumAlgorithm` / `ChecksumValue` 持久化字段）|
| **3** | **Server Access Log 交付管线** —— `WriteAccessLog` 是空操作，日志配置持久化但永无日志产出 | 合规审计 | **P1** | `BucketConfig.LoggingTarget` 和 `LoggingPrefix` 完整持久化，REST 和 S3 API 均暴露了 Get/Put/Delete BucketLogging 端点。但 `repository.WriteAccessLog` 的实现体为空（`return nil`），意味着没有一行访问日志被写出。对于 SOC2/PCI-DSS/ISO27001 合规场景，这直接导致审计失败 | `internal/repository/sql_buckets.go:368-376`（`WriteAccessLog` — 完整签名接收 6 个参数，函数体仅 6 行 `_ = …`，`return nil`）；`internal/repository/repository.go:274`（`Repository` 接口 — `WriteAccessLog` 声明为接口方法，但所有存储实现都继承自 sqlStore 的 no-op 版本）；`internal/repository/sql_buckets.go:351-364`（`SetBucketLogging` / `DeleteBucketLogging` / `GetBucketLogging` — 完整 CRUD 实现，配置 CRUD 完好但消费端断裂）；`internal/api/rest/handler.go:301-330`（`GetBucketLogging` / `PutBucketLogging` / `DeleteBucketLogging` — REST 端点完整）；`internal/api/s3compat/handler.go:296-310`（`dispatchBucketSubresource` 中的 `logging` 子资源分发）；`internal/middleware/middleware.go:85-100`（`AccessLog` 中间件 — 记录的是 HTTP 请求日志到 stdout，不是 S3 Server Access Logs）|
| **4** | **S3 Multi-Object Delete（POST ?delete）** —— 标准 S3 批量删除操作未实现，`aws s3 rm --recursive` 等工具命令断裂 | 协议兼容性 | **P1** | S3 规范要求 `POST /{bucket}?delete` 接受 XML body（`<Delete><Object><Key>...</Key></Object></Delete>`），返回 `200 <DeleteResult>`。当前 S3 兼容层无此路由/处理。这导致 `aws s3 rm s3://bucket/prefix/ --recursive`、`aws s3 sync --delete` 等工具操作失败——这些工具内部使用 Multi-Object Delete 代替逐对象 DELETE | `internal/api/s3compat/router.go:1-28`（`NewRouter` — 无 `POST /{bucket}?delete` 路由）；`internal/api/s3compat/handler.go:1-50`（`Handler` 方法集 — 无 multi-object delete handler）；`internal/api/s3compat/xml.go`（XML 序列化层 — 无 `<Delete>` / `<DeleteResult>` XML 类型定义）；`internal/service/file_features.go:126-143`（`BatchDelete` — REST API 存在 batch delete 逻辑，但 S3 compat 层未接入）；`internal/api/rest/handler.go:380-410`（`BatchDelete` — REST 端点完好，S3 层可以复用同一 service 方法）|
| **5** | **废弃分段上传（Abandoned Multipart Upload）生命周期治理** —— 用户发起但未完成的分段上传在存储后端永久残留，系统无清理机制 | 资源泄漏/成本 | **P2** | 当用户调用 `InitMultipartUpload` 后因网络闪断、客户端崩溃或主动放弃而不调用 `CompleteMultipartUpload` 或 `AbortMultipartUpload` 时，已上传的分片在存储后端永久留存。当前 Reconcile/Retention 任务不扫描废弃上传。在 S3 后端上这些分片按标准存储计费；在 Local 后端上占用磁盘。对于高并发上传场景，月累积量可达 TB 级 | `internal/storage/storage.go:1-80`（`Storage` 接口 — 无 `ListMultipartByAge` 或 `ListActiveMultipartUploads` 方法）；`internal/storage/s3.go:170-195`（`InitMultipart` — S3 CreateMultipartUpload，但不记录到本地索引）；`internal/storage/local.go:1-50`（Local 后端 — multipart 信息仅存在于内存 Map 中，重启即丢失，更无法被外部扫描）；`internal/reconcile/job.go:30-70`（`New` — 创建 reconcile job 时无废弃上传清理选项）；`internal/reconcile/lifecycle.go:1-30`（`LifecycleJob` — 仅处理过期对象生命周期，不处理上传生命周期）；`internal/reconcile/retention.go:1-35`（`RetentionJob` — 仅处理软删除保留和幂等性 TTL，不处理上传）；`internal/repository/sql_uploads.go:1-30`（`sqlStore` — 上传记录表含 `created_at` 字段，可支持按时间查询但无公开查询接口）；`internal/api/s3compat/handler.go:190-210`（`` (UploadPart 和 CompleteMultipart 逻辑中无超时检查)）|

---

## 方向一：Virtual Hosted-Style S3 请求路由

### 产品/运营影响

| 维度 | 影响 |
|------|------|
| **兼容性断裂** | AWS CLI v2 默认使用 Virtual Hosted-Style。用户必须手动追加 `--endpoint-url https://aero-vault.example.com/s3` + `--cli-connect-timeout` + 配置 `force_path_style = true` 才能使用。这增加了 3 步额外配置，是用户 onboarding 的第一道摩擦 |
| **SDK 兼容性缺口** | aws-sdk-go-v2、boto3、aws-sdk-js-v3 的 `S3Client` 默认使用 Virtual Hosted-Style。JavaScript SDK v3 的某些功能（如 `@aws-sdk/lib-storage` 的 `Upload` 类）在 Path-Style 下对某些 endpoint 行为异常。Go SDK 的新版本也开始废弃对 Path-Style 的优化 |
| **Host 头浪费** | 当前完全不解析 `Host` 头，但 `Host` 头携带了完整的目标信息。CORS 预检请求 `Origin` 头也未被用于自动协议协商 |
| **CDN/反向代理兼容性** | 部署在 CDN（CloudFront/CloudFlare）后时，Path-Style 被重写的概率高于 Virtual Hosted-Style，导致签名验证失败 |

### 现状与代码证据

**证据 1：S3 路由仅解析 URL 路径，不解析 Host 头**

```go
// internal/api/s3compat/router.go:14-27
func NewRouter(svc *service.FileService, logger *slog.Logger) chi.Router {
    h := NewHandler(svc, logger)
    r := chi.NewRouter()

    // Bucket-only paths: with or without trailing slash.
    r.HandleFunc("/{bucket}", h.BucketDispatch)
    r.HandleFunc("/{bucket}/", h.BucketDispatch)

    // Object verbs.
    r.Put("/{bucket}/*", h.PutObject)
    r.Get("/{bucket}/*", h.GetObject)
    r.Head("/{bucket}/*", h.HeadObject)
    r.Delete("/{bucket}/*", h.DeleteObject)
    r.Post("/{bucket}/*", h.PostObject)
    return r
}
```

所有 bucket 提取均通过 `chi.URLParam(r, "bucket")` 从 URL 路径获取。无 `r.Host` 解析分支。

**证据 2：Handler 中无虚拟主机逻辑**

```go
// internal/api/s3compat/handler.go:87-92
func (h *Handler) PutObject(w http.ResponseWriter, r *http.Request) {
    bucket := chi.URLParam(r, "bucket")
    key := keyFromURL(r)
    ...
```

Handler 中的 `bucket` 和 `key` 提取全部依赖 URL 路径参数。全局搜索 `r.Host` 在 `internal/api/s3compat/` 下零出现。

**证据 3：配置层无虚拟主机域名配置**

```go
// internal/config/config.go:50-53
type S3CompatConfig struct {
    Prefix string // 固定值 /s3
}
```

无 `Domain`、`VirtualHostedDomain` 或 `UseVirtualHostedStyle` 字段。装配时直接 `r.Mount(cfg.S3Compat.Prefix, ...)`。

### 边界情况

| 场景 | 问题 |
|------|------|
| `Host: bucket.aero-vault.internal` | 完全不可路由。bucket 信息丢失 |
| `Host: bucket.tenant.aero-vault.internal` | 多租户 + 虚拟主机场景无支持。当前 tenant 通过 `X-Aero-Tenant` 头传递 |
| `Host: storage.googleapis.com/bucket` | GCS 兼容模式不可用 |
| 混合部署（同一端口同时服务 path-style + virtual hosted-style） | 需协商逻辑，当前路由被 path-style 完全占据 |

### 架构权衡

| 方案 | 优势 | 代价 |
|------|------|------|
| 在 `dispatcher` / 全局中间件中解析 Host → 注入 Header | 不改动现有路由，后向兼容 | 增加中间件复杂度；Host 域名模式需配置化 |
| 在 s3compat 路由层做双模式分发 | 路由自包含，不侵入全局 | router 逻辑翻倍 |
| 统一改为 Virtual Hosted-Style，废弃 Path-Style | 最简洁 | 破坏已有客户端；与配置声明的 `/s3` 前缀冲突 |

**推荐权衡：** 在 `buildDispatcher` 或 `applyMiddleware` 层增加一个 `s3VirtualHostResolver` 中间件，将 Virtual Hosted-Style 请求转换为 Path-Style 的内部表示（注入 `X-Aero-Virtual-Bucket` 头），再由 s3compat router 读取该头覆盖 URL 参数。这样零改动 s3compat handler 代码，后向兼容，且可配置化。

### 投入产出评估

| 指标 | 估算 |
|------|------|
| 涉及文件数 | ~5（config + dispatcher/middleware + s3compat router + handler 微调） |
| 核心代码变更 | ~150-250 行 |
| 测试覆盖 | Host 头解析单元测试 + S3 客户端 e2e 测试（minio-client、awscli） |
| 用户影响 | 所有使用默认配置的 S3 SDK 客户端 instant-compatible |

---

## 方向二：S3 Flexible Checksum API

### 产品/运营影响

| 维度 | 影响 |
|------|------|
| **数据完整性风险** | 当前仅依赖 TLS 传输层加密保证数据完整性。存储层无独立校验和。TLS 终止于反向代理后，内部网络传输的数据无完整性保护。磁盘静默损坏不可检测 |
| **SDK 兼容性** | aws-sdk-go-v2 从 v1.18.0+ 开始默认启用 `RequestChecksumCalculation.WHEN_REQUIRED`，会在 PUT/GET 时发送 `x-amz-checksum-crc32` 请求服务端校验。当前服务端忽略这些头，SDK 的校验和验证断裂 |
| **合规需求** | SOC2 / HIPAA / PCI-DSS 要求"数据在静态和传输中都有完整性验证"。当前 TLS 覆盖传输，但静态验证缺失。用户无法在读取时校验对象完整性 |
| **企业采购门槛** | 金融/医疗行业的 RFP（Request for Proposal）中，"end-to-end checksum verification" 是必选功能 |

### 现状与代码证据

**证据 1：写入路径只校验 MD5**

```go
// internal/service/file_crud.go:63-80
func md5WrapReader(r io.Reader, expectedMD5 string) (io.Reader, func() error, error) {
    if expectedMD5 == "" {
        return r, func() error { return nil }, nil
    }
    hasher := md5.New()
    tee := io.TeeReader(r, hasher)
    return tee, func() error {
        actual := base64.StdEncoding.EncodeToString(hasher.Sum(nil))
        if actual != expectedMD5 {
            return ErrBadDigest
        }
        return nil
    }, nil
}
```

仅 `Content-MD5`。无 CRC32/CRC32C/SHA1/SHA256 分支。

**证据 2：读取路径无校验和输出**

```go
// internal/api/s3compat/handler.go:680-700
func writeS3ObjectMeta(w http.ResponseWriter, meta map[string]string) {
    for k, v := range meta {
        switch {
        case strings.EqualFold(k, "_aero_legal_hold"):
            w.Header().Set("x-amz-object-lock-legal-hold", v)
        case strings.HasPrefix(k, "_aero_"):
            continue
        default:
            w.Header().Set("x-amz-meta-"+k, v)
        }
    }
}
```

无 `x-amz-checksum-*` 响应头输出。

**证据 3：Storage 接口无 checksum 契约**

```go
// internal/storage/storage.go:20-50（简化）
type PutOptions struct {
    ContentType string
    Metadata    map[string]string
}
type ObjectInfo struct {
    Key          string
    ETag         string
    Size         int64
    LastModified time.Time
}
```

无 `ChecksumAlgorithm` / `ChecksumValue` 字段。存储后端不感知校验和算法。

**证据 4：Repository Object 模型无 checksum 持久化**

```go
// internal/repository/repository.go:30-55
type Object struct {
    ID           int64
    TenantID     string
    Bucket       string
    Key          string
    VersionID    string
    Backend      string
    StorageKey   string
    Size         int64
    ETag         string
    ContentType  string
    Metadata     map[string]string
    Tags         map[string]string
    StorageClass string
    ...
}
```

无 `ChecksumCRC32` / `ChecksumCRC32C` / `ChecksumSHA1` / `ChecksumSHA256` 字段。

### 边界情况

| 场景 | 问题 |
|------|------|
| 客户端同时发送多个校验和头（`x-amz-checksum-crc32` + `x-amz-checksum-sha256`） | 无并行校验和验证逻辑，只取第一个或全部忽略 |
| 读取时请求 `x-amz-checksum-crc32` 但存储时只持久化了 MD5 | 需要按需实时计算或回退到已持久化的算法 |
| 分段上传场景下校验和如何聚合 | S3 规范对 multipart 的校验和定义了特定的聚合方式（各段 CRC32 异或） |
| 从 S3 Storage Backend 读取时如何透传 AWS S3 的校验和返回给用户 | 当前 Get 操作丢弃了 AWS SDK 返回的 checksum 信息 |

### 架构权衡

| 方案 | 优势 | 代价 |
|------|------|------|
| 仅存储层计算并持久化所有校验和算法（CRC32/CRC32C/SHA1/SHA256） | 完全透明，所有读取即时响应 | 写入性能和存储放大：4 种算法 × 8-32 字节/对象 = 每次写入额外 4 次 CPU 计算 |
| 存储客户端指定的算法，不做额外计算 | 最小性能开销 | 读取时若请求算法不同于存储算法，需实时重算 |
| 分层策略：CRC32C 默认（S3 推荐），仅在客户端指定时追加 | 平衡性能和兼容性 | 算法协商逻辑复杂 |

**推荐权衡：** 默认仅计算 CRC32C（AWS S3 的新默认算法，硬件加速友好），在 `PutOptions` 中增加 `ChecksumAlgorithms []string` 指定额外算法。读取时优先返回存储的算法；若请求算法未存储，实时流式计算后返回（仅首读有延迟成本）。

### 投入产出评估

| 指标 | 估算 |
|------|------|
| 涉及文件数 | ~10（storage 接口+3个后端、service 层、s3compat handler、repository 模型+迁移、配置） |
| 核心代码变更 | ~400-600 行 |
| 数据结构迁移 | 需新增 migration 双文件（sqlite + postgres）为 objects 表加 checksum 列 |
| 测试覆盖 | checksum 算法单元测试 + 存储后端合约测试 + SDK 兼容性 e2e |
| 用户影响 | 所有使用 aws-sdk-go-v2 默认配置的客户端自动获得端到端完整性保证 |

---

## 方向三：Server Access Log 交付管线

### 产品/运营影响

| 维度 | 影响 |
|------|------|
| **合规审计失败** | SOC2 要求"记录所有系统访问事件，包括对象读取、写入、删除"。当前没有任何访问日志被持久化输出。审计员可证明配置存在但配置永远为空 |
| **安全事件溯源断裂** | 当发生数据泄露时，无法回答"谁在什么时间读取了哪个对象"。仅有的 HTTP access log（stdout）在容器重启后丢失，且不包含对象级信息 |
| **计费对账盲区** | 无法基于访问日志进行按量计费/用量分析。当前仅维护总用量（`used_bytes`、`used_objects`），粒度不够 |
| **SLA 违规举证不能** | 客户投诉性能时，无独立日志证明请求延迟分布 |

### 现状与代码证据

**证据 1：WriteAccessLog 是空操作**

```go
// internal/repository/sql_buckets.go:368-376
func (s *sqlStore) WriteAccessLog(ctx context.Context, tenant, sourceBucket, method, key, status, latencyMs, userAgent string) error {
    _ = tenant
    _ = sourceBucket
    _ = method
    _ = key
    _ = status
    _ = latencyMs
    _ = userAgent
    return nil
}
```

函数签名完整，6 个参数全部 discard。注释区声明"creates a log entry object in the target bucket"，但从未创建。

**证据 2：配置 CRUD 完整但消费断裂**

```go
// internal/repository/repository.go:50-53
type BucketConfig struct {
    ...
    LoggingTarget     string // target bucket for access logs; "" = disabled
    LoggingPrefix     string // key prefix for log objects
    ...
}
```

`LoggingTarget` 和 `LoggingPrefix` 完整持久化。`SetBucketLogging` / `GetBucketLogging` / `DeleteBucketLogging` 完备。但消费端 `WriteAccessLog` 空转。

**证据 3：HTTP AccessLog 中间件不是 Server Access Log**

```go
// internal/middleware/middleware.go:85-100
func AccessLog(logger *slog.Logger) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
            start := time.Now()
            next.ServeHTTP(sw, r)
            logger.Info("access",
                "method", r.Method,
                "path", r.URL.Path,
                "status", sw.status,
                "duration", time.Since(start).String(),
                "tenant", TenantFrom(r.Context()),
            )
        })
    }
}
```

输出到 JSON stdout，非结构化、不持久、无对象级信息。容器重启即丢。

### 边界情况

| 场景 | 问题 |
|------|------|
| 日志目标 bucket 就是源 bucket（循环写入） | 访问日志自身产生写操作 → 递归日志写入 → 无限循环 |
| 日志写入失败时的错误处理策略 | 异步 best-effort（不影响用户请求）vs 同步阻塞（可以接受丢失？） |
| 日志格式标准化 | S3 Server Access Log 有标准格式（`<bucket> <time> <ip> <user> <key> <method> <status> <bytes> ...`），是否严格遵从？ |
| 高吞吐场景下的日志写入频率 | 每请求写一次 log object 会产生巨大的小对象数（TPS 10000 → 每天 8.64 亿条日志对象） |
| 日志的日志（元日志） | 是否需要让用户配置日志的生命周期策略 |

### 架构权衡

| 方案 | 优势 | 代价 |
|------|------|------|
| **实时写入**：每请求同步 WriteObject 到日志目标 bucket | 实时可查 | 写入延迟放大 2×；日志写入失败可能阻塞主请求 |
| **异步缓冲批量写入**：内存 buffer + 定时 flush（类似 S3 的真实行为） | 低延迟开销，可批量合批 | 宕机时丢失缓冲区内日志（≤ flush interval） |
| **分离日志存储**：不写入对象存储，写入专用日志后端（ELK/Loki/S3 单独 bucket） | 不影响主存储性能，日志可独立配置生命周期 | 增加系统组件依赖 |

**推荐权衡：** 异步缓冲 + 批量写入到一个专门的日志 bucket（非源 bucket），每 60 秒或 10000 条 flush 一次。日志格式遵循 S3 Server Access Log 规范子集。日志 bucket 本身不做日志（通过配置限制）。WriteAccessLog 的存储端实现在单独的日志 worker goroutine 中执行，不阻塞请求路径。

### 投入产出评估

| 指标 | 估算 |
|------|------|
| 涉及文件数 | ~8（日志 format、日志 worker、repository 实现、middleware 埋点、配置） |
| 核心代码变更 | ~300-400 行 |
| 数据库变更 | 无（日志存入对象存储而非数据库） |
| 测试覆盖 | 日志格式测试 + buffer flush 测试 + 高吞吐不丢失测试 |
| 用户影响 | 合规审计通过；安全事件溯源可行；客户投诉时有日志可查 |

---

## 方向四：S3 Multi-Object Delete（POST ?delete）

### 产品/运营影响

| 维度 | 影响 |
|------|------|
| **工具链断裂** | `aws s3 rm s3://bucket/prefix/ --recursive` 内部对每 1000 个 key 发起一次 `POST ?delete`，在 AeroVault 上返回 `405 Method Not Allowed`。用户被迫使用 `aero-vault cli rm` 或逐个 `aws s3api delete-object` |
| **运维效率损失** | 批量删除 10000 个对象：当前 S3 层需要 10000 次 DELETE 请求（10 RPS × 1000s = 16.7 分钟）。Multi-Object Delete 只需 10 次请求（1000 keys/请求）× 网络往返 ≈ 10 秒。效率差 100 倍 |
| **同步操作副作用** | REST API 的 BatchDelete 是同步串行操作（循环内逐 key `softDelete`）。对于 1000 个对象的删除，一个 HTTP 请求持有连接可达数秒到分钟，消耗 `MaxInFlight` 槽位 |

### 现状与代码证据

**证据 1：S3 路由中无 `?delete` 分发**

```go
// internal/api/s3compat/router.go:14-27
r.Put("/{bucket}/*", h.PutObject)     // 有
r.Get("/{bucket}/*", h.GetObject)      // 有
r.Head("/{bucket}/*", h.HeadObject)    // 有
r.Delete("/{bucket}/*", h.DeleteObject) // 有
r.Post("/{bucket}/*", h.PostObject)    // 有（仅 multipart）
```

无 `POST /{bucket}?delete` 路由。

**证据 2：BucketDispatch 中无 delete 子资源路由**

```go
// internal/api/s3compat/handler.go:~280-320
func (h *Handler) dispatchBucketSubresource(w http.ResponseWriter, r *http.Request, bucket string, q url.Values) bool {
    switch {
    case q.Has("versioning"):
        ...
    case q.Has("lifecycle"):
        ...
    case q.Has("object-lock"):
        ...
    // 无 q.Has("delete") 分支
```

**证据 3：REST BatchDelete 存在但无法通过 S3 访问**

```go
// internal/service/file_features.go:126-143
func (s *FileService) BatchDelete(ctx context.Context, tenant, bucket string, keys []string) []BatchDeleteResult {
    results := make([]BatchDeleteResult, 0, len(keys))
    for _, key := range keys {
        r := BatchDeleteResult{Key: key}
        if err := s.Delete(ctx, tenant, bucket, key, false); err != nil {
            r.Error = err.Error()
        }
        results = append(results, r)
    }
    return results
}
```

批量删除逻辑已实现，但通过 REST API 访问（`POST /v1/batch/delete`），S3 兼容层无法调用。

**证据 4：无 XML 序列化类型**

```go
// internal/api/s3compat/xml.go - 搜索 "Delete" 和 "DeleteResult"
// 无 <Delete> / <DeleteResult> / <Deleted> / <Error> XML 类型定义
```

### 边界情况

| 场景 | 问题 |
|------|------|
| 单请求包含超过 1000 个 key | S3 规范限制 1000 keys/请求。超出需返回 `MalformedXML` |
| 删除包含不存在的 key | S3 返回 `<Deleted><Key>xxx</Key></Deleted>` 而不是错误。静默成功 |
| 版本化桶中的批量删除 | S3 支持 `?delete` 的 `<Object><Key>xxx</Key><VersionId>vid</VersionId></Object>` |
| `Quiet=true` 模式 | 在 Quiet 模式下只返回删除失败的条目，不返回成功条目 |
| 权限检查 | 需要对 batch 中的每个 key 进行权限检查（在 AeroVault 的 auth 模式下，tenant/bucket 级别足够吗？） |

### 架构权衡

| 方案 | 优势 | 代价 |
|------|------|------|
| 在 `PostObject` 中加入 `?delete` 分支 | 复用已有路由，最小变更 | PostObject 逻辑变得更复杂 |
| 新增 `BucketDispatch` 的 `?delete` 分支 | 语义清晰，与 S3 规范对齐 | POST 路由要特别处理 multipart vs delete 的区分 |
| 拆出独立 handler | 最清晰 | 多一个文件，多一些导入 |

**推荐权衡：** 在 `dispatchBucketSubresource` 中新增 `q.Has("delete")` 分支，指向新 handler `h.multiObjectDelete`。该 handler 解析 XML body → 调用 `svc.BatchDelete` → 序列化 `DeleteResult` XML。复用已有的 service 层 `BatchDelete` 方法。

### 投入产出评估

| 指标 | 估算 |
|------|------|
| 涉及文件数 | ~4（s3compat handler + xml + router + 测试） |
| 核心代码变更 | ~150-200 行 |
| 数据库变更 | 无 |
| 测试覆盖 | XML 序列化/反序列化测试 + `aws s3 rm --recursive` e2e |
| 用户影响 | `aws s3 rm --recursive`、`aws s3 sync --delete`、`aws s3api delete-objects` 即时可用 |

---

## 方向五：废弃分段上传（Abandoned Multipart Upload）生命周期治理

### 产品/运营影响

| 维度 | 影响 |
|------|------|
| **存储成本泄漏** | 每个 InitMultipartUpload 在 S3 后端创建了一个上传会话，已上传的分片按标准存储计费。假设每日 10000 次 Init，5% 废弃率（500 次），每次上传平均 100MB 分片 → 50GB/天 → 1.5TB/月的无效存储 |
| **存储后端资源占用** | Local 后端上，废弃分片占用磁盘空间但不可见（storage key 不进入 objects 表）。只有 GC 目录扫描才能发现 |
| **用户无感知** | 调用 InitMultipartUpload 后客户端闪断，用户不知道需要调用 AbortMultipart。对象上传失败但费用照常产生。S3 控制台有"管理上传"面板可查看和清理，AeroVault 无 |
| **运维盲区** | 无废弃上传的监控指标。Ops 无法知道"当前有多少活跃上传""有多少已超过 24 小时未完成" |

### 现状与代码证据

**证据 1：Reconcile/Retention 不扫描上传**

```go
// internal/reconcile/job.go:30-70
func New(repo repository.Repository, store storage.Storage, interval time.Duration,
    deleteOrphanBlobs bool, gracePeriod time.Duration, tenants []string, logger *slog.Logger) *Job {
    return &Job{
        repo:             repo,
        store:            store,
        interval:         interval,
        deleteOrphanBlobs: deleteOrphanBlobs,
        gracePeriod:      gracePeriod,
        tenants:          tenants,
        logger:           logger,
    }
}
```

参数组中无 `abandonedUploadCleanup` 或 `maxUploadAge` 字段。

```go
// internal/reconcile/retention.go:23-35
type RetentionJob struct {
    repo           repository.Repository
    store          storage.Storage
    interval       time.Duration
    retention      time.Duration   // 软删除保留期
    idempotencyTTL time.Duration   // 幂等性记录过期
    instanceID     string
    clusterSingleton bool
}
```

无 `uploadTTL` 字段。

**证据 2：Storage 接口无废弃上传扫描方法**

```go
// internal/storage/storage.go:30-80（Storage 接口方法集）
type Storage interface {
    Put(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) (ObjectInfo, error)
    Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)
    Stat(ctx context.Context, key string) (ObjectInfo, error)
    Delete(ctx context.Context, key string) error
    List(ctx context.Context, prefix, marker string) (ListResult, error)
    InitMultipart(ctx context.Context, key string, opts PutOptions) (MultipartInit, error)
    UploadPart(ctx context.Context, key, uploadID string, partNumber int32, r io.Reader, size int64) (MultipartPart, error)
    CompleteMultipart(ctx context.Context, key, uploadID string, parts []MultipartPart) (ObjectInfo, error)
    AbortMultipart(ctx context.Context, key, uploadID string) error
    PresignGet(ctx context.Context, key string, expiry time.Duration) (string, error)
    PresignPut(ctx context.Context, key string, expiry time.Duration) (string, error)
    Backend() string
}
```

无 `ListMultipartUploads(ctx, prefix string) ([]MultipartUpload, error)` 或 `ListMultipartByAge(ctx, before time.Time) ([]MultipartUpload, error)`。

**证据 3：Local 后端 multipart 信息仅存内存，重启即丢失**

```go
// internal/storage/local_multipart.go:1-30
// Local 后端的 multipart 使用 map[string]*multipartUpload 存储
// key 的构造是 uploadID。此 map 仅存于进程内存中
```

服务器重启后，Local 后端无法列出或清理之前创建但未完成的 multipart 上传。DB 中上载记录存在，但存储后端的上传会话已丢失。

**证据 4：数据库中有 created_at 但无查询接口**

```go
// internal/repository/sql_uploads.go:20-40
// Upload 结构体有 CreatedAt 字段
// 但 Repository 接口公开的方法中无 ListUploadsByAge 或 CountActiveUploads
```

### 边界情况

| 场景 | 问题 |
|------|------|
| 用户主动调用 AbortMultipart | 当前工作正常。但若用户不调用，无人清理 |
| 服务器重启后遗留的 Active uploads | Local 后端：storage 层会话丢失，DB 中 upload 记录成为孤儿。需通过 DB 记录手动 abort |
| S3 后端的上传生命周期 | S3 提供 `ListMultipartUploads` API 和 `AbortMultipartUpload` 可批量清理，但未利用 |
| 超时策略配置 | 不同场景需要不同超时（大文件上传可能需要 7 天，小文件 1 小时）。是否需要 per-bucket 配置？ |
| 清理时的并发安全 | 用户主动 CompleteMultipart 与 Reconcile 清理同时发生 → 已完成的 upload 碎片被误 abort |

### 架构权衡

| 方案 | 优势 | 代价 |
|------|------|------|
| **新增 Reconcile 子任务**：在 RetentionJob 中增加 `uploadTTL` 配置，定时扫描 `created_at < now - TTL` 的 upload 记录，逐条调用 `AbortMultipart` + 删除 DB 记录 | 最小的架构变动，复用已有 Reconcile 框架 | 需要 Storage 接口新增扫描方法或依赖 DB 记录中的 storage key |
| **Storage 接口新增 `ListActiveMultipartUploads`**：各后端实现自己的废弃上传扫描，S3 调用 `ListMultipartUploads`，Local 扫描 DB | 后端感知自身状态 | 接口变动影响所有后端实现 |
| **纯 Repository 侧清理**：不依赖 Storage 的扫描能力，仅通过 DB 中的 upload 记录 + `created_at` 做超时判断，调用 `svc.AbortMultipart` | 不修改 Storage 接口，zero-touch 后端 | 对 S3 后端而言，DB 记录和 S3 上传会话可能不一致 |

**推荐权衡：** 在 RetentionJob 中新增 `uploadTTL` 配置 + `CleanupAbandonedUploads` 方法。Reconcile 循环中调用 `repo.ListUploadsOlderThan(ctx, ttl)` 获取超时上传列表，对每个调用 `svc.AbortMultipart(ctx, ...)`。S3 后端额外通过 `ListMultipartUploads` API 做交叉验证。新增 `abandoned_upload_total` 和 `abandoned_upload_cleaned_total` 监控指标。

### 投入产出评估

| 指标 | 估算 |
|------|------|
| 涉及文件数 | ~6（reconcile/retention.go + repository + storage interface + s3/local backend + config） |
| 核心代码变更 | ~200-300 行 |
| 数据库变更 | 无（已有 created_at 字段） |
| 测试覆盖 | 上传超时扫描 + abort + 并发安全测试 + 监控指标测试 |
| 用户影响 | 每月可能节省 TB 级别的无效存储成本；运维人员可通过 Prometheus 监控废弃上传量 |

---

## 总体路线图建议

```
P0 (紧迫)          P1 (重要)           P2 (值得做)
─────────────────────────────────────────────────────
Virtual Hosted     Flexible Checksum   废弃上传清理
Style S3                              ─────────
───────────────    ───────────────     RetentionJob
用户入口兼容性     数据完整性保证      扩展
                   Server Access Log  
                   ───────────────    
                   合规审计            
                   Multi-Object Delete 
                   ───────────────    
                   运维效率            
```

### 实施顺序推理

| 顺序 | 方向 | 理由 |
|------|------|------|
| **1** | Virtual Hosted-Style S3 | 这是 on-boarding 的第一道门槛。没有它，新用户必须阅读文档了解 `force_path_style` 配置，产生认知摩擦。投入最小（~200 行），影响最大（全 SDK 兼容） |
| **2** | Multi-Object Delete | 实现与 direction 1 正交但用户感知同样强烈。"`aws s3 rm` 不能用"是非常直观的断裂。与 direction 1 可并行开发 |
| **3** | Server Access Log | 合规需求。在企业场景中，合规是采购的前提条件而非锦上添花。与方向 2 可并行 |
| **4** | Flexible Checksum | 企业采购门槛。可与方向 3 并行，但实现成本略高（涉及 storage 接口改动、DB migration） |
| **5** | 废弃上传清理 | 成本优化。对中小企业用户感知不强烈，但对高吞吐业务场景是必须的。可安排在后续迭代 |
