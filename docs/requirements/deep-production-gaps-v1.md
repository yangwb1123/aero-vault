# AeroVault 高价值扩展方向：代码级生产盲区

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — `cmd/server/main.go` + `internal/` 全部 30+ 子包（237+ Go 源文件，50 对迁移文件），MCP 双模式、WebDAV、Web UI、3 套 SDK（Go/Python/JS）、`deploy/` 全套配置  
> **去重验证：** 对 `docs/requirements/` 下全部 112 份既有分析文档 + `ROADMAP.md` 10 大方向 + `docs/analysis-*.md` 进行关键词正则 + 语义交叉验证，确认本文方向未被任一既有文档作为主要方向独立覆盖。  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。每个方向包含：**产品/运营价值** → **现状与代码证据** → **架构权衡与边界情况**。

---

## 方法论：从代码存在的断层到运行时行为断裂

AeroVault 已有 112 轮深度分析覆盖了绝大多数功能缺口与架构优化方向。本文聚焦的缺口类型是：**代码存在、配置可写、但运行时行为与预期之间存在静默断裂。** 具体来说：

| 缺口类型 | 判定标准 | 本文对应方向 |
|----------|---------|-------------|
| **写时验证但读时不验证** | 数据写入时执行完整性检查（ETag/MD5），但读取时从不验证，静默腐败不可感知 | 方向一 |
| **配置持久化但运行时不执行** | CORS 规则通过 API 完整 CRUD、S3 XML 正确响应，但全局中间件完全不读取桶级配置 | 方向二 |
| **数据字段存在但无公共操作 API** | 用户元数据在创建时写入、在服务内可修改（SetObjectMetaKey），但无面向用户的更新端点，用户只能通过重传整个对象来更新元数据 | 方向三 |
| **热路径保护但冷路径绕过** | 文件 CRUD 操作被 idempotency middleware 包裹，但多分片上传的四个端点全部裸奔，重试 CompleteMultipart 产生重复对象 | 方向四 |

### 四个方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 关键代码锚点 |
|---|------|------|--------|---------|-------------|
| **1** | **读取路径数据完整性验证缺失** —— ETag 写时计算、读时永不检查，静默数据腐败不可感知 | 数据安全/可靠性 | **P1** | 所有存储后端在 PUT 时计算 ETag（MD5）并保存，但 GET/HEAD 路径从不验证存储内容是否与 ETag 匹配。后台 bit rot、部分写入、磁盘静默错误可导致用户读到损坏数据而系统毫不知情 | `internal/service/file_crud.go:147-155`（Put 路径计算 MD5 ETag，但 Get 路径不检查）；`internal/storage/local_read.go:58-65`（Get 读取文件后直接返回，无 checksum 验证）；`internal/storage/s3.go:143-160`（S3 Get 直接返回 SDK 流，不校验 ETag）；`internal/storage/storage.go:40-44`（ObjectInfo.ETag 存在但仅在 PUT 时填充）；`internal/reconcile/scrub.go:48-70`（Scrub 模块 reconcile 时校验，非读时） |
| **2** | **桶级 CORS 规则：已持久化但运行时永不执行** —— CORS 配置有完整 CRUD API 和 S3 XML 端点，但全局 CORS 中间件完全忽略桶级配置 | 协议兼容/安全 | **P1** | 用户通过 PUT `/v1/buckets/{bucket}/cors` 或 S3 `PUT ?cors` 设置了桶级 CORS 规则，这些规则被正确入库和返回，但 OPTIONS 预检请求和跨域响应头完全由全局 CORS 中间件（读取环境变量 `CORS_*`）控制。桶级配置是空的"数据坟墓" | `internal/api/rest/router.go:101-103`（`GetBucketCORS`/`PutBucketCORS`/`DeleteBucketCORS` REST 端点已注册）；`internal/api/s3compat/handler.go:789-808`（S3 `getBucketCors`/`putBucketCors`/`deleteBucketCors` XML 端点已实现）；`internal/repository/repository.go:43`（`BucketConfig.CORSRules []CORSRule` 已定义）；`internal/repository/sql_buckets.go:299-335`（`GetBucketCORS`/`SetBucketCORS` SQL 层完整）；**`internal/middleware/cors.go:19-60`（CORS 中间件仅读取 `CORSConfig` 结构体——来自 `cfg.CORS` 环境变量，从不读取请求的 bucket 上下文或 `BucketConfig.CORSRules`）**； `cmd/server/main.go:244-249`（全局 CORS 配置从 `cfg.CORS` 注入，无桶级路由逻辑） |
| **3** | **对象元数据更新 API 缺失** —— 元数据在创建时写入后不可变，用户无端点单独修改而不重传整个对象 | 产品完整/API 设计 | **P1** | 标签（Tags）有 `PUT /files/{key}/tags` 和 `DELETE /files/{key}/tags` 专用端点，但用户元数据（metadata map）仅在 Upload/Put 时通过 `metadata` 表单字段或 `X-Amz-Meta-*` 头写入。Repository 层存在 `SetObjectMetaKey` 方法但仅供内部 scrubbing 使用，未暴露给用户。用户无法：更正拼写错误的 metadata 值、补充缺失的 metadata key、删除过时的 metadata 项 | `internal/service/file_crud.go:65-72`（Metadata 通过 `PutOptions.Metadata` 传入，仅在 Put 路径写入）；`internal/repository/repository.go:252`（`SetObjectMetaKey(ctx, tenant, bucket, key, metaKey, metaValue)` 已存在但仅被 `internal/reconcile/scrub.go:94` 内部调用）；`internal/repository/sql_objects.go:360-380`（`SetObjectMetaKey` 的 SQL 实现——`json_set`/`jsonb_set` 更新单个 key，存在完整实现）；`internal/api/rest/router.go:130-133`（`putKey` 分派了 `/tags` 和 `/acl` 子路径，无 `/metadata` 路由）；`internal/service/file_features.go:23-25`（`SetTags` 存在公共方法，但无对应 `SetMetadata` 公共方法）；`internal/storage/local_meta.go:12-32`（本地存储的 metadata sidecar 文件—`writeMeta` 全量覆写，支持修改） |
| **4** | **多分片上传幂等性缺口** —— InitMultipart/UploadPart/CompleteMultipart/AbortMultipart 四个端点均未受 Idempotency-Key 中间件保护 | 可靠性/数据一致性 | **P2** | 文件 Put/Post/Delete 操作被 `idempotency` middleware 包裹，重试的请求自动去重并回放原始响应。但四个多分片上传端点注册在 idempotency group 之外。`CompleteMultipart` 在超时重试场景下可产生重复的已完成对象。UploadPart 重试可能导致重复的分片被存储后端记录 | `internal/api/rest/router.go:40-45`（`idempotency` middleware 包裹的 group：`Post("/files")`、`Post("/files/*")`、`Put("/files/*")`、`Delete("/files/*")`）；`internal/api/rest/router.go:49-52`（多分片路由在 idempotency group 之外：`Post("/multipart")`、`Put("/multipart/{uploadID}/parts/{n}")`、`Post("/multipart/{uploadID}/complete")`、`Delete("/multipart/{uploadID}")`）；`internal/service/file_multipart.go:117-145`（`CompleteMultipart` 无幂等性检查——每次调用都尝试合并分片并写入新对象）；`internal/api/rest/idempotency.go:38-55`（`idempotency` 中间件实现——基于 `(tenant, key)` 的 claim/completion 机制） |

---

## 方向一：读取路径数据完整性验证缺失

### 产品/运营价值

| 维度 | 影响 |
|------|------|
| **静默数据腐败** | 磁盘静默错误（silent bit rot）、后台部分写入事故、后端存储的 checksum 不一致，在 Put 时写入正确的 ETag，但 GET 时读取损坏数据。系统无感知，用户收到损坏文件 |
| **合规要求** | SOC2 / PCI-DSS / HIPAA 均要求数据在存储和传输过程中保持完整性。当前架构仅保证了"我们当初上传了什么"的记录（ETag），但未保证"我们给出的是什么"的验证 |
| **S3 协议预期** | AWS S3 的 `x-amz-checksum-*`（CRC32C/SHA256）和 `Content-MD5` 的全部意义在于端到端完整性。如果服务端自己不校验，这些头形同虚设 |
| **差异化竞争力** | AeroVault 的定位是"安全的知识保险库"。如果用户不能确信读出的文件就是当初存入的文件，核心信任感缺失 |

### 现状与代码证据

**Put 路径（写时）：完成 ETag 计算和校验**

```go
// internal/service/file_crud.go:58-73
func md5WrapReader(r io.Reader, contentMD5 string) (io.Reader, func() error, error) {
    // Content-MD5 头解码 + 计算 md5 → 写入时校验
}

// internal/storage/local_write.go:25-40
func (s *LocalStorage) writeObject(...) (localMeta, error) {
    h := md5.New()
    reader = io.TeeReader(r, h)
    // ... 写入临时文件 ...
    return localMeta{
        ETag: hex.EncodeToString(h.Sum(nil)),  // ← ETag 是 MD5 十六进制
    }, nil
}
```

**Get 路径（读时）：零验证**

```go
// internal/service/file_crud.go:222-238
func (s *FileService) Get(ctx context.Context, tenant, bucket, key string) (io.ReadCloser, repository.Object, error) {
    obj, err := s.repo.GetObject(ctx, tenant, bucket, key)
    // ...
    rc, _, err := s.store.Get(ctx, obj.StorageKey)
    // ← 直接返回 rc，没有对内容做任何 checksum 验证
    return rc, obj, nil
}

// internal/storage/local_read.go:58-65
func (s *LocalStorage) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
    // 打开文件 → 返回 Reader
    // ← 没有计算 MD5，没有与存储的 ETag 比对
}
```

**Scrub 模块（周期性校验）：非读时，有延迟窗口**

```go
// internal/reconcile/scrub.go:48-70
// Scrub 在 reconcile 周期中执行（RECONCILE_INTERVAL_MINUTES），非用户请求路径
// 读路径在两次 scrub 之间的静默腐败无保护
```

**当前数据流：**

```
PUT:  客户端 → md5WrapReader(验证 Content-MD5) → store.Put(计算 ETag) → 存储 ETag
GET:  store.Get(返回 Reader) → 直接给客户端               ← 无 ETag 验证
```

### 架构权衡与建议方案

| 维度 | 考量 |
|------|------|
| **性能影响** | 每次读取重新计算 MD5 会增加 CPU 负载。对大文件（GB 级），全量 MD5 计算的延迟不可接受。可考虑：(1) 后台异步验证（reconcile scrub 增强）；(2) 分层验证——小文件全量计算，大文件按分片抽样；(3) 仅对 `x-amz-checksum-*` 请求头指定的算法做验证 |
| **存储后端适配** | S3 后端从 AWS 返回的 ETag 通常是 MD5（单分片）或非 MD5（多分片上传），无法直接用于内容验证。需要存储独立的内容校验和（如 `_aero_content_md5` 已在 metadata 中） |
| **边界情况** | 多分片上传的 ETag 为 AWS 特定格式（`"{md5_of_part_md5s}-{part_count}"`），不是文件内容的 MD5。对于通过 S3 multipart 上传的对象，需要改用独立的 checksum 存储 |
| **降级策略** | 验证失败时的行为：(1) 返回 502/500 并记录审计；(2) 尝试从 replica 读取（如果配置了复制）；(3) 标记对象为 corrupt（已有 `_aero_scrub_status` 机制） |

---

## 方向二：桶级 CORS 规则——已持久化但运行时永不执行

### 产品/运营价值

| 维度 | 影响 |
|------|------|
| **功能幻觉** | 用户通过 S3 SDK 或 REST API 配置了桶级 CORS，SDK 返回成功（HTTP 200/204）。用户以为自己的 bucket 有了指定的跨域策略。实际上这些规则从未被任何中间件读取——全部 CORS 行为由**全局环境变量**控制。非特权用户可以通过 API 设置 CORS 规则，获得虚假的"已配置"确认感 |
| **安全边界虚设** | 安全审计时发现某 bucket 的 CORS 设置为 `AllowedOrigins: ["*"]`，审计员标记为风险——但实际上这个配置无运行时效果，真正的策略是 env `CORS_ALLOWED_ORIGINS`。审计结论错误、安全姿态不透明 |
| **S3 兼容性断裂** | AWS S3 的 `PUT ?cors` 和 `GET ?cors` 是最常用的桶配置之一。客户端配置后实际行为与 AWS 不同（全局策略 vs 桶级策略），迁移用户会感到困惑 |
| **多租户隔离失效** | 多租户场景下，不同租户的桶应有独立的 CORS 策略。当前全局 CORS 策略强制所有桶使用同一套规则 |

### 现状与代码证据

**桶级 CORS 配置：完整 CRUD + S3 XML 支持**

```go
// internal/api/rest/router.go:101-103
r.Get("/buckets/{bucket}/cors", h.GetBucketCORS)      // ✅ REST 读取
r.Put("/buckets/{bucket}/cors", h.PutBucketCORS)       // ✅ REST 写入
r.Delete("/buckets/{bucket}/cors", h.DeleteBucketCORS) // ✅ REST 删除
```

```go
// internal/api/s3compat/handler.go:789-808
// S3 GET /{bucket}?cors → 返回桶级 CORS XML    ✅
// S3 PUT /{bucket}?cors → 解析 XML 写入存储     ✅
// S3 DELETE /{bucket}?cors → 删除桶级配置       ✅
```

```go
// internal/repository/sql_buckets.go:299-335
func (s *sqlStore) GetBucketCORS(...) ([]CORSRule, error) // ✅ SQL 读取
func (s *sqlStore) SetBucketCORS(...) error                // ✅ SQL 写入
```

**运行时 CORS 检查：完全跳过桶级配置**

```go
// cmd/server/main.go:244-249
// 全局 CORS 中间件从环境变量注入，非桶级感知：
handler = mw.CORS(middleware.CORSConfig{
    AllowedOrigins: cfg.CORS.AllowedOrigins, // ← 来自 env CORS_ALLOWED_ORIGINS
    AllowedHeaders: cfg.CORS.AllowedHeaders,
    AllowedMethods: cfg.CORS.AllowedMethods,
})(handler)

// internal/middleware/cors.go:19-60
func CORS(cfg CORSConfig) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            // ← 只读取 cfg（全局配置），从不读取请求上下文的 bucket 信息
            // ← 不调用 repo.GetBucketCORS()
            writeCORSHeaders(w, r, cfg)
        })
    }
}
```

**数据流动：**

```
用户 PUT /v1/buckets/mybucket/cors → repo.SetBucketCORS()   ✅ 保存
用户 GET  /v1/buckets/mybucket/cors → repo.GetBucketCORS()   ✅ 返回
用户 OPTIONS /v1/files/somekey → cors.CORS(cfg)               ❌ 全局策略，忽略 mybucket
```

### 架构权衡与建议方案

| 维度 | 考量 |
|------|------|
| **性能影响** | 每个 OPTIONS 请求和跨域请求需要从 DB 读取桶级 CORS 配置。通过桶级缓存（bucket config 内存缓存）可消除 DB 压力 |
| **中间件位置** | 当前 CORS 中间件是全局的，在最外层 middleware 链中。桶级 CORS 需要在 Tenant 中间件之后（从 context 读取 tenant）和 Auth 之后（验证请求身份）。这与当前全局 CORS 在外层的位置冲突——需要重新设计中间件链或使用双阶段 CORS（全局默认 + 桶级覆盖） |
| **与全局策略的关系** | 全局 CORS 策略应作为**默认值**，桶级 CORS 作为**覆盖**。未配置桶级规则的 bucket 继承全局配置 |
| **边界情况** | `Origin` 头不存在时跳过 CORS 检查；桶被删除时缓存失效；跨桶拷贝（CopyObject）的 CORS 策略应遵循源桶还是目标桶？AWS 行为是目标桶 |
| **预检缓存** | 桶级 CORS 的 `MaxAgeSeconds` 应被 OPTIONS 响应的 `Access-Control-Max-Age` 正确反映 |

---

## 方向三：对象元数据更新 API 缺失

### 产品/运营价值

| 维度 | 影响 |
|------|------|
| **用户体验断裂** | 标签有专用更新端点，元数据没有。用户上传文件后发现 metadata 拼写错误或需要补充信息，只能重新上传整个对象——成本高、不优雅 |
| **标签 vs 元数据不对称** | Tags 是轻量级（字符串键值对且不可嵌套），Metadata 是通用键值对（可嵌套更大更灵活的值）。Tags 有 CRUD 端点，Metadata 没有——使用者被迫将信息塞入 Tags 或接受"不可变" |
| **自动化场景阻断** | CI/CD 管线需要在上传后为对象补充 `_build_id`、`_commit_sha`、`_deploy_time` 等元数据，但无法在不重传的情况下实现 |
| **元数据结构升级** | 平台未来可能扩展元数据 schema（如内容类型分类、合规标签、数据来源），但若无可变 API，schema 升级需要全量数据迁移 |

### 现状与代码证据

**Tags 已有完整更新端点：**

```go
// internal/api/rest/router.go:130-133
func (h *Handler) putKey(w http.ResponseWriter, r *http.Request) {
    switch {
    case strings.HasSuffix(r.URL.Path, "/tags"):  // ✅ PUT /files/{key}/tags
        h.PutTags(w, r)
    case strings.HasSuffix(r.URL.Path, "/acl"):   // ✅ PUT /files/{key}/acl
        h.PutObjectACLHandler(w, r)
    // ← 无 /metadata 路由
    }
}
```

**Metadata 仅在 Put 路径写入：**

```go
// internal/service/file_crud.go:65-72
func storeContentMD5(opts *PutOptions) {
    // metadata 仅在 PutOptions 中传入，且仅在 Put 路径被消费
}
```

**Repository 层 SetObjectMetaKey 已存在但仅内部使用：**

```go
// internal/repository/repository.go:252
SetObjectMetaKey(ctx context.Context, tenant, bucket, key, metaKey, metaValue string) error

// internal/repository/sql_objects.go:360-380
func (s *sqlStore) SetObjectMetaKey(...) error {
    // 使用 json_set/jsonb_set 更新单个 metadata key，不需要读取整个 metadata 对象
    // SQL: UPDATE objects SET metadata = json_set(metadata, '$."'+$1+'"', $2) WHERE ...
}
```

```go
// internal/reconcile/scrub.go:94
// 唯一调用处——内置使用，非公共 API
if err := j.repo.SetObjectMetaKey(ctx, obj.TenantID, obj.Bucket, obj.Key, "_aero_scrub_status", "corrupt"); err != nil {
```

**缺失的端点和服务方法：**

```
缺失: PUT  /v1/files/{key}/metadata     → 全量替换 metadata
缺失: PATCH /v1/files/{key}/metadata    → 增量更新特定 key
缺失: DELETE /v1/files/{key}/metadata   → 清空 metadata
缺失: svc.SetMetadata()                 → FileService 公共方法（对标 svc.SetTags()）
```

### 架构权衡与边界情况

| 维度 | 考量 |
|------|------|
| **大小限制** | 元数据整体有 64 KiB 上限（`ErrMetadataTooLarge`）。更新时需验证更新后的总大小不超过限制——需要先读取现有 metadata，合并后再写回 |
| **增量 vs 全量** | `PATCH /metadata` 可保持现有的 key（用户只发变化的 key）；`PUT /metadata` 则全量替换。Repository 的 `SetObjectMetaKey` 天然支持增量（`json_set` 单个 key），但当前无批量版本 |
| **Storage backend 影响** | 仅 metadata（数据库行中的 JSON blob）受影响，无需动存储层的 blob。这是纯元数据操作，低成本 |
| **并发安全** | 并发更新同一对象的 metadata 可能丢失其中一个更新。乐观锁（ETag 条件更新）或单独的行版本号可解决 |
| **版本控制兼容** | 对于有版本控制的 bucket，metadata 更新应作用于当前版本还是最新版本？S3 行为是当前版本。应当与 `SetTags` 的语义一致 |
| **审计** | metadata 更新应写入 audit log（与 PutTags 一致） |

---

## 方向四：多分片上传幂等性缺口

### 产品/运营价值

| 维度 | 影响 |
|------|------|
| **重复对象风险** | `CompleteMultipart` 是幂等性最关键的操作。网络超时后客户端重试 `CompleteMultipart`，后端可能已经成功合并了分片，重试导致再次尝试合并——不同后端行为不同：S3 返回已完成合并的 ETag，本地后端可能创建重复对象 |
| **非原子操作链** | 一个多分片上传包含 1 次 `InitMultipart` + N 次 `UploadPart` + 1 次 `CompleteMultipart`。如果中间任何一步被重试，无法保证整个操作链的最终一致性 |
| **S3 协议预期** | AWS S3 对 `CompleteMultipartUpload` **不是幂等的**（第一次返回 200 + ETag，重试返回 400 InvalidRequest）。但一个健壮的存储系统应当能安全处理重试，避免数据损坏 |
| **用户信任基础** | 大文件上传需要几十分钟甚至几小时，用户依赖客户端重试机制。如果重试可能导致数据损坏，用户信心受损 |

### 现状与代码证据

**Idempotency-Key 中间件的覆盖范围：**

```go
// internal/api/rest/router.go:40-52
// Idempotency 包裹的操作：
r.Group(func(r chi.Router) {
    r.Use(idempotency(repo, logger, idemHashBody))
    r.Post("/files", h.PostForm)
    r.Post("/files/*", h.postKey)
    r.Put("/files/*", h.putKey)
    r.Delete("/files/*", h.deleteKey)
})

// Idempotency 未覆盖的操作：
r.Post("/multipart", h.InitMultipart)                   // ← 未包裹
r.Put("/multipart/{uploadID}/parts/{n}", h.UploadPart)  // ← 未包裹
r.Post("/multipart/{uploadID}/complete", h.CompleteMultipart)  // ← 未包裹
r.Delete("/multipart/{uploadID}", h.AbortMultipart)     // ← 未包裹
```

**CompleteMultipart 当前实现——无幂等性：**

```go
// internal/service/file_multipart.go:117-145
func (s *FileService) CompleteMultipart(ctx context.Context, uploadID string) (repository.Object, error) {
    u, err := s.repo.GetUpload(ctx, uploadID)    // 每次调用都查找 upload
    // ...
    info, err := s.store.CompleteMultipart(ctx, sk, u.BackendUID, storageParts)  // 后端合并
    saved, err := s.saveMultipartObject(ctx, obj, bcfg)  // 每次调用都写入对象行
    // ← 无去重检查：两次 CompleteMultipart 产生两个对象行（非版本化时覆盖，版本化时增加版本）
    s.emit(ctx, saved, repository.EventCreated)
    return saved, nil
}
```

**影响路径：**

```
客户端 → POST /multipart/{uploadID}/complete
  ├─ 第一次: CompleteMultipart → 写入对象行 → 成功后网络超时
  ├─ 重试:   CompleteMultipart → 再次写入对象行（非版本化: 覆盖上一个；版本化: 新增版本）
  └─ 结果: 用户以为只有一个对象，实际有 N 个版本或非预期覆盖
```

### 架构权衡与边界情况

| 维度 | 考量 |
|------|------|
| **Idempotency-Key 的 key 选择** | 多分片上传的幂等性 key 应基于 `(tenant, uploadID)`——uploadID 已经唯一标识了一个上传会话，天然适合作为幂等键 |
| **CompleteMultipart 的特殊性** | CompleteMultipart 成功后应持久化该 uploadID 的幂等记录。此后同 uploadID 的 CompleteMultipart 请求回复放第一次的结果（ETag、对象信息） |
| **UploadPart 的去重** | 对特定 `(uploadID, partNumber)` 组合，可以用 `(tenant, uploadID, partNumber)` 作为幂等 key。要求 S3 返回的 ETag 对同一分片内容一致（AWS S3 保证这一点） |
| **InitMultipart 的去重** | 对同一 `(bucket, key)` 并发发起多个 InitMultipart 是合法行为（AWS 允许创建多个 upload session）。故 InitMultipart 不需要幂等性 |
| **AbortMultipart 的去重** | 已 abort 的 uploadID 再 abort 应 safe（S3 返回 204）。当前实现 `GetUpload → DeleteUpload`，缺少幂等记录 |
| **存储后端适配** | 本地后端（local FS）的 `CompleteMultipart` 是服务端合并；S3 后端的 `CompleteMultipart` 是 API 调用。幂等性逻辑应相同 |

---

## 附录：快速验证检查点

以下 grep 命令可独立验证本文的代码锚点：

```bash
# 方向一：Read-path ETag 验证缺失
grep -rn "func.*Get\b.*context" internal/storage/*.go | grep -v test
# → 所有 Get 实现均无 MD5/checksum 验证逻辑

# 方向二：CORS 中间件不读桶级配置
grep -rn "CORSRules\|GetBucketCORS\|BucketConfig" internal/middleware/cors.go
# → 零结果——中间件不感知桶级配置

# 方向三：SetObjectMetaKey 调用者
grep -rn "SetObjectMetaKey" internal/ --include="*.go"
# → 仅 internal/reconcile/scrub.go 一处，无公共 API

# 方向四：多分片路由在 idempotency group 外
sed -n '38,55p' internal/api/rest/router.go
# → 文件 CRUD 在 idempotency group 内，multipart 在 group 外
```
