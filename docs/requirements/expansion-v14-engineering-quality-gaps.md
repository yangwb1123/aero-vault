# AeroVault 高价值扩展方向（第十四期）

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全局代码扫描（237 Go 源码文件，约 50K 行），逐包审阅 `internal/` 全部 23+ 子包 + `cmd/server/main.go` + `Makefile` + `Dockerfile` + SDK 三层 + 所有迁移文件 + 配置系统。逐一比对前十三期 expansion 文档（`expansion-directions.md` ~ `expansion-v13-frontier-horizons.md`）+ `extensions.md` + `ROADMAP.md` 共 ~750KB 已有分析，确保每个方向在**既有文档中零覆盖或仅行级提及**。  
> **日期：** 2026-07-10  
> **原则：** 选取 5 个**既有 13 期文档从未系统性设计**的**工程质量缺口**方向——不是"新功能"，而是"不改就会在生产环境出事"的硬伤。每个方向附带：代码锚点 → 当前状态 → 风险等级 → 边界情况 → 架构概要 → 实现理由。**不编写任何实现代码。**

---

## 审阅背景：前十三期已覆盖的去重矩阵

前十三期已从 13 个视角覆盖了约 65 个方向，涵盖 AI 管线、S3 兼容性、多租户、事件系统、身份联邦、合规、复制、WASM、CAS、CDN、内容智能、存储分层、可观测性、测试基础设施、开发体验、安全纵深、优雅关闭、API 治理等。**本期不重复任何上述方向。**

**本期选点原则：** 选取**工程质量 / 架构债务 / 生产隐患**方向——它们不是功能需求，而是不改就会在特定场景下导致数据丢失、服务中断、性能灾难的硬性问题。每个方向都在代码中有明确的"定时炸弹"锚点。

---

## 本期方向总览

| # | 方向 | 类型 | 风险等级 | 核心代码锚点 | 既有覆盖 |
|---|------|------|---------|-------------|---------|
| 1 | **🛑 内存安全：加密 I/O 路径无限制全缓冲** | 性能/正确性 | **严重 (Critical)** — 大文件加密路径 OOM | `storage/encrypt.go:encryptReader` `storage/encrypt.go:decryptReader` `storage/local_write.go:writeObject` | 仅 `extensions.md` 单行提及，**13 期 expansion 零覆盖** |
| 2 | **🟠 写并发安全：无乐观锁、最后写入者胜出** | 数据一致性 | **高 (High)** — 并发 PUT 丢数据 | `service/file_crud.go:Put` `service/file_crud.go:PutOverwrite` `repository/sql_objects.go:UpsertObject` | **零覆盖**（v4 跨区复制冲突、v7 CAS 去重均为不同问题） |
| 3 | **🟠 带宽浪费：无传输层压缩** | 性能/成本 | **中 (Medium)** — 带宽成本 5-10x 浪费 | `api/rest/handler.go:serveFile` `middleware/middleware.go` `service/file_crud.go:serveObjectContent` | v13 一个单元格 "⚠️" 提及，非独立方向 |
| 4 | **🟠 可观测性缺口：零运行时诊断端点** | 运维 | **高 (High)** — 生产问题无法现场诊断 | `cmd/server/main.go`（路由注册） `middleware/middleware.go`（healthz/readyz） | v11 开发者体验清单单一子项，非独立方向 |
| 5 | **🟡 多协议错误模型碎片化** | 架构 | **中 (Medium)** — SDK 维护成本高，排障困难 | `api/rest/handler.go:classify` `api/s3compat/errors.go` `api/webdav/dav.go` `mcp/protocol.go` | v4 小节提及但非独立方向，零后续跟进 |

---

## 1. 🛑 内存安全：加密 I/O 路径无限制全缓冲

### 风险等级：严重 (Critical)

> 当前加密路径对任意大小的对象执行 `io.ReadAll`——整个文件加载到内存后开始处理。对于 SSE 加密的对象（`STORAGE_LOCAL_SSE_KEY` 或 `STORAGE_LOCAL_SSE_KEYFILE` 启用时），**PUT 和 GET 两个方向都存在这个隐患**。代码自身的注释承认了这一点。

### 当前状态

```go
// internal/storage/encrypt.go:305-310
// io.Reader wrappers for encrypt/decrypt-on-the-fly. Because GCM requires the
// whole ciphertext to verify the tag, we buffer through []byte here. This is
// fine for objects up to ~hundreds of MB; for streaming SSE on huge files,
// swap in AES-CTR + HMAC chunked.
func encryptReader(r io.Reader, enc *envelopeEncrypter) (io.Reader, string, error) {
    plain, err := io.ReadAll(r)        // ← 整个文件进内存！
    if err != nil {
        return nil, "", err
    }
    ct, env, err := enc.encrypt(plain)
    if err != nil {
        return nil, "", err
    }
    return bytes.NewReader(ct), nil, env
}

func decryptReader(r io.Reader, envelope string, enc *envelopeEncrypter) (io.ReadCloser, error) {
    ct, err := io.ReadAll(r)            // ← 同样全缓冲
    if err != nil {
        return nil, err
    }
    pt, err := enc.decrypt(ct, envelope)
    if err != nil {
        return nil, err
    }
    return io.NopCloser(bytes.NewReader(pt)), nil
}
```

**调用链分析：**

```
PUT /v1/files/large-backup.zip (500MB, SSE enabled)
  → REST handler → FileService.Put()
  → store.Put() → local.writeObject()
  → s.enc != nil → io.ReadAll(reader) → allocate 500MB → encrypt → write
```

**同一时间 3 个并发大文件 PUT → 1.5GB 瞬时内存分配**。没有 `sync.Pool` 复用缓冲区，没有内存压力回退策略，没有分块处理。

### 代码锚点

| 位置 | 现状 | 问题 |
|------|------|------|
| `internal/storage/encrypt.go:306` | `encryptReader` → `io.ReadAll(r)` | 全缓冲加密，O(object size) 内存 |
| `internal/storage/encrypt.go:316` | `decryptReader` → `io.ReadAll(r)` | 全缓冲解密，O(object size) 内存 |
| `internal/storage/local_write.go:49` | `writeObject` → `io.ReadAll(reader)`（加密分支） | 同上 |
| `internal/storage/local_write.go:60` | `writeObject` → `io.Copy(tmp, reader)`（非加密分支） | ✅ 流式，没问题 |
| 全仓 | `sync.Pool` 使用量 | **零** —— 无一处使用对象池复用缓冲区 |
| `internal/storage/circuitbreaker.go` | 电路断路器 | 只防后端故障，不防内存问题 |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 后果 |
|-----------|------|---------|------|
| **500MB 数据库备份上传** | 用户上传加密的 PostgreSQL dump | `io.ReadAll` 分配 500MB+ | 瞬时内存峰值 TODO→GC 压力→延迟抖动 |
| **2GB 科学数据集** | 加密上传 2GB NetCDF 文件 | `io.ReadAll` 尝试分配 2GB+ | **OOM kill**（容器内存限制 < 2.5GB） |
| **10 个并发 100MB 文件** | 批量上传 10 个加密 PDF | 每个都 `io.ReadAll` | 1GB+ 瞬时使用→GC 停顿→服务退化 |
| **解密路径同样危险** | GET 加密的大文件 | `io.ReadAll` 加密 blob | 同上，OOM 风险 |
| **GCM 认证 Tag 限制** | 整个密文必须接收完才能验证 tag | 无法流式解密返回给客户端 | 用户必须等待整个文件解密完成才能开始下载 |

### 架构概要

```
Phase 1 — 分块加密框架（替换全缓冲）：
┌─────────────────────────────────────────────────────────────┐
│ type chunkedEncrypter struct {                              │
│     chunkSize  int    // 默认 64MB（可配置）                 │
│     enc        *envelopeEncrypter                           │
│     pool       *sync.Pool   // []byte 缓冲池               │
│ }                                                           │
│                                                             │
│ func (c *chunkedEncrypter) Encrypt(r io.Reader) io.ReadCloser { │
│     // 从池中取 64MB 缓冲区                                 │
│     // 读入一个 chunk                                       │
│     // encrypt chunk (AES-256-GCM on each chunk)           │
│     // 写 chunk header (size + iv + tag)                    │
│     // 归还缓冲区到 pool                                    │
│     // 重复直到 io.EOF                                      │
│ }                                                           │
│                                                             │
│ func (c *chunkedEncrypter) Decrypt(r io.Reader) io.ReadCloser { │
│     // 读 chunk header                                      │
│     // 从池中取缓冲区                                       │
│     // 读密文                                                │
│     // decrypt + verify tag                                 │
│     // 返回明文 chunk                                        │
│     // 归还缓冲区                                           │
│ }                                                           │
└─────────────────────────────────────────────────────────────┘

Phase 2 — 全仓内存安全加固：
┌─────────────────────────────────────────────────────────────┐
│ 1. io.Copy 调用链审计：所有 io.Copy/write 操作检查缓冲策略   │
│ 2. sync.Pool 引入：所有 []byte 分配走池                     │
│ 3. 最大分配限制：单请求 > MAX_MEMORY_PER_REQUEST 时返回 413 │
│ 4. 内存压力信号：runtime.GC 触发时主动降级为非加密路径       │
│    （如果可用）或拒绝新请求                                  │
│ 5. 指标：object.io_readall_bytes_total (当无法避免时记录)    │
└─────────────────────────────────────────────────────────────┘
```

**影响面：**
| 组件 | 影响 | 工作量估计 |
|------|------|-----------|
| 分块加密/解密（替代全缓冲） | `storage/encrypt.go` 重写 | 高（~300 行） |
| 缓冲池引入 | 全 `internal/storage/` | 中 |
| 大对象拒绝策略 | `service/file_crud.go:Put` + config | 低 |
| 兼容性：旧加密格式读取 | `decryptReader` 保留 + 自动检测格式 | 低 |
| 指标 | `telemetry/metrics.go` | 低 |

### 为什么现在做

这不是"有一天可能会优化"的问题——这是一个**有明确 OOM 风险的生产隐患**。代码自身注释说"fine for objects up to ~hundreds of MB"——说明作者知道极限在哪里。对于任何涉及大文件（视频、数据库备份、科学数据、容器镜像）的部署场景，当前的加密路径在对象大小超过可用内存时会直接崩溃。分块加密是成熟的工程模式（AWS S3 SSE-C、GCS CSEK 都使用分块），且当前的 envelope 格式（`alg`/`kek`/`iv`）可以向后兼容——旧格式用 `decryptReader` 全缓冲读取，新格式用 chunked 读取。不改的话，这个问题只会在生产环境最不合时宜的时刻暴露。

---

## 2. 🟠 写并发安全：无乐观锁、最后写入者胜出

### 风险等级：高 (High)

> 当前系统对同一 key 的并发 PUT 没有任何写入顺序保证或冲突检测。两个请求同时 PUT `doc.pdf`，第二个完成的请求静默覆盖第一个——且没有警告。元数据操作（Tag/ACL）同样存在此问题。

### 当前状态

```go
// internal/service/file_crud.go:Put
// 检查对象是否存在：
//   - 不存在 → InsertObject (CREATE)
//   - 存在 → InsertObjectVersion (版本化桶) 或 UpsertObject → ON CONFLICT DO UPDATE
// 没有 ETag 验证，没有 If-Match 检查，没有版本计数器

// internal/repository/sql_objects.go:UpsertObject
// SQL: INSERT ... ON CONFLICT (tenant_id, bucket, key, deleted_at) DO UPDATE SET ...
// 乐观锁？不存在的——最后一条 SQL 语句胜出
```

**同一个 key 上的并发 PUT 序列：**

```
时间线:
T1: 客户端 A PUT doc.pdf (ETag: "abc")          → 服务端开始处理
T2: 客户端 B PUT doc.pdf (ETag: "def")          → 服务端开始处理
T3: 服务端完成 B 的 PUT → DB row 更新为 "def"
T4: 服务端完成 A 的 PUT → DB row 更新为 "abc"   ← 静默覆盖了 B 的写入！
   → B 收到了 200 OK，认为自己写成功了
   → 实际上数据已被 A 覆盖
```

**这个问题的现实影响：**

| 场景 | 当前 | 应该 |
|------|------|------|
| 两个 CI pipeline 同时上传构建产物 | 静默覆盖 | 第二个请求收到 412 Precondition Failed |
| 用户编辑文档后保存，同时备份脚本覆盖 | 静默覆盖，用户丢失编辑 | 412，用户被告知冲突 |
| 并发更新 ACL（PUT /acl） | 最后写入者胜出，权限配置丢失 | CAS：指定当前 ETag 才能更新 |
| 并发更新 Tag | 相同问题 | 合并 Tag 或 CAS |

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/service/file_crud.go:Put` / `PutOverwrite` | 不检查 If-Match | 无条件写入乐观锁 |
| `internal/service/file_crud.go:SetTags` | Load → merge → save | 非原子操作，竞态下丢标签 |
| `internal/service/file_crud.go:SetACL` | Load → modify → save | 同上 |
| `internal/service/acl.go` | ACL 操作 | 无版本化 ACL 更新 |
| `internal/api/rest/handler.go:handlePut` | 解析 If-Match 用于 GET | If-Match 在写入路径不生效 |
| `internal/api/rest/conditional.go` | 条件请求用于 GET | 写入路径无条件语义 |
| `internal/repository/sql_objects.go:UpsertObject` | ON CONFLICT DO UPDATE | 无 WHERE etag = $N 条件 |
| `internal/repository/sql_tags_acl.go:UpsertTags` | 同上 | 无版本化 |

### 边界情况暴露

| Edge Case | 场景 | 后果 |
|-----------|------|------|
| **乐观锁误判** | 客户端上传时指定 If-Match，但对象是全新的（无 ETag） | 应该允许 PUT（全新资源没有版本约束） |
| **版本化桶的乐观锁** | 版本化桶中每个 PUT 创建新版本，ETag 是版本标识 | 乐观锁作用于"当前版本"而非整个对象 |
| **Multipart Upload 完成时的冲突** | 两个 CompleteMultipart 同时完成 | 第二个完成的覆盖第一个→部分上传丢失 |
| **复制冲突** | 跨区域复制中两个区域同时修改同一 key | 当前无检测→静默分裂 |
| **Tag 合并 vs 替换** | 并发 SetTags(A) 和 SetTags(B)，一个想增加、一个想删除 | 原子操作应保证一个有序序列 |

### 架构概要

```
Phase 1 — ETag 条件写入（最小改动，最大收益）：
┌─────────────────────────────────────────────────────────────┐
│ PUT /v1/files/doc.pdf                                       │
│ If-Match: "abc123"  ← 客户端指定预期的当前 ETag             │
│ If-None-Match: "*"   ← 仅在对象不存在时创建（已支持）       │
│                                                             │
│ FileService.Put():                                          │
│   1. 读当前对象（GET metadata）                             │
│   2. 如果 If-Match 设了：                                    │
│      - 对象不存在且 If-Match 非 "*" → 412                  │
│      - 对象 ETag != If-Match → 412 Precondition Failed     │
│      - 匹配 → 继续 PUT                                      │
│   3. DB写入时：WHERE etag = $expected_etag                  │
│      - RowsAffected == 0 → 412（并发冲突）                  │
│      - RowsAffected == 1 → 200 OK                           │
│                                                             │
│ 对版本化桶：                                                 │
│   If-Match 作用于"当前版本"的 ETag                           │
│   新版本不受当前版本 ETag 约束                               │
└─────────────────────────────────────────────────────────────┘

Phase 2 — CAS 元数据操作（Tag/ACL 原子性）：
┌─────────────────────────────────────────────────────────────┐
│ PUT /v1/files/doc.pdf/tags                                  │
│ If-Match: "abc123"  ← 基于对象版本号的乐观锁               │
│                                                             │
│ 内部：BEGIN TX → SELECT current_tags + etag                 │
│       → merge(客户端提供, 当前)                              │
│       → UPDATE tags WHERE object_etag = expected            │
│       → RowsAffected == 0 → ROLLBACK + 412                 │
│       → COMMIT                                              │
│                                                             │
│ 简化方案（Phase 2a）：对象级锁                              │
│   对每个 objectID 维护一个 channel 作为 mutex               │
│   同一对象的 Tag/ACL 操作串行化                              │
│   锁粒度：per-object（非 per-key，避免死锁）                │
└─────────────────────────────────────────────────────────────┘
```

**影响面：**
| 组件 | 影响 | 工作量估计 |
|------|------|-----------|
| If-Match 解析增强（PUT 路径） | `api/rest/handler.go` + `service/file_crud.go` | 低 |
| 版本身份的 ETag 验证 | `service/file_crud.go:Put` + `PutOverwrite` | 低 |
| PostgreSQL/SQLite 条件 UPDATE (`WHERE etag=`) | `repository/sql_objects.go` | 低 |
| 412 响应 + 当前 ETag 返回 | `api/rest/handler.go` | 低 |
| S3 兼容：`x-amz-copy-source-if-match` 用于 CopyObject | `api/s3compat/extra.go` | 中 |
| Tag/ACL CAS 操作 | `service/file_features.go` + `acl.go` | 中 |
| 指标：`object.optimistic_lock_conflicts_total` | `telemetry/metrics.go` | 低 |

### 为什么现在做

并发写覆盖不是一个理论问题——在任何有 CI/CD pipeline、多用户协作、或自动化脚本写入的场景中都会发生。当前系统在 GET 路径支持 `If-Match`/`If-None-Match`（conditional.go），说明 ETag 基础设施已经存在——只是没有扩展到写入路径。**将条件语义扩展到 PUT 是填补一个明知道路但未完工的半截功能**。不改的话，用户会在这个问题上浪费数小时调试"为什么我的文件/标签/ACL 被静默覆盖了"。此外 v11 的跨区复制方向要求冲突检测——乐观锁是冲突检测的前置条件。

---

## 3. 🟠 带宽浪费：无传输层压缩

### 风险等级：中 (Medium)

> 所有 HTTP 响应都以原始数据流传输，不做 `Accept-Encoding` 协商。对于文本内容（JSON、HTML、日志、代码、Markdown），压缩比可达 5-10x。当前对 `gzip` 的处理仅针对**已存储对象的内容编码元数据**（上传时客户端已压缩），而非服务端按需压缩。

### 当前状态

```go
// internal/api/rest/handler.go:serveFile
// 响应头设置：
//   Content-Type, Content-Length, ETag, Last-Modified, Cache-Control
// 未设置：
//   Content-Encoding: gzip  ← 缺失！

// 仅有的是对象自身携带的 _aero_content_encoding
// service/file_crud.go:251-254
if obj.Metadata["_aero_content_encoding"] == "gzip" {
    // 自动解压 gzip 编码的对象（不重新压缩）
}
```

**当前数据传输路径：**

```
用户 GET /v1/search → {"results": [... 2MB JSON ...]}
  → 2MB 原始 JSON 传输 → 带宽消耗 2MB
  → 如果 gzip 压缩 → 约 200KB（~10x 压缩）
```

**没有压缩的影响面：**

| 场景 | 数据量 | 未压缩 | gzip 压缩 | 节约 |
|------|--------|--------|-----------|------|
| 搜索返回 1000 条结果 | 1.5MB JSON | 1.5MB | ~120KB | 92% |
| Chat 长回答 | 50KB text | 50KB | ~8KB | 84% |
| ListObjects 返回 10K 项 | 3MB XML | 3MB | ~250KB | 92% |
| 大文本文件下载 | 100MB log | 100MB | ~5MB | 95% |
| OpenAPI 文档 | 120KB JSON | 120KB | ~15KB | 87% |

**Web UI 静态资源 `index.html` 同样没有压缩。**

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/middleware/middleware.go` | 中间件链 | 无压缩中间件 |
| `internal/api/rest/handler.go:writeJSON` | 写入 JSON 响应 | 无 gzip writer 封装 |
| `internal/api/rest/handler.go:serveFile` | 流式返回文件 | 无 Accept-Encoding 协商 |
| `internal/api/s3compat/handler.go:getObject` | S3 GET | 无 gzip |
| `internal/api/s3compat/handler.go:listObjectsV2` | XML 列表响应 | XML 尤其适合压缩 |
| `internal/middleware/middleware.go:readyzHandler` | 健康检查 | 无压缩（不需要） |
| `internal/webui/web.go` | Web UI 静态文件服务 | 无压缩 |

### 边界情况暴露

| Edge Case | 场景 | 行为 |
|-----------|------|------|
| **已压缩的内容重复压缩** | 客户端上传已 gzip 的对象（`Content-Encoding: gzip`） | 不应二次压缩→检测存储元数据跳过 |
| **小响应压缩开销大于收益** | 200 字节的 404 响应 | 应该跳过压缩（`Content-Length < 1024`） |
| **SSE 流式响应** | Chat Streaming 每帧几 token | 流式压缩（chunked transfer encoding + gzip frame）|
| **Range 请求** | GET Range: bytes=100-200 的小范围 | 压缩整个文件再返回 range 浪费→跳过压缩 |
| **图片/视频等已压缩内容** | JPEG/PNG/MP4 等二进制格式 | 检测 Content-Type，跳过二进制格式压缩 |
| **CDN 回源请求** | CDN 回源源站 | 如果源站压缩了，CDN 可能缓存压缩版本→需要 `Vary: Accept-Encoding` |

### 架构概要

```
Phase 1 — 压缩中间件（无侵入，可配置）：
┌─────────────────────────────────────────────────────────────┐
│ middleware/compress.go                                       │
│                                                             │
│ type CompressionConfig struct {                              │
│     MinSize      int      // 低于此大小不压缩，默认 1024B   │
│     Level        int      // gzip 压缩等级，默认 -1 (default)│
│     SkipTypes    []string // 不压缩的 Content-Type 前缀      │
│     SkipPaths    []string // 不压缩的 URL 路径前缀           │
│ }                                                             │
│                                                             │
│ 只读路径（GET/HEAD/SEARCH）压缩，写入路径（PUT/POST）不解压   │
│                                                             │
│ 实现策略：                                                    │
│   Option A: 中间件（透明压缩所有响应）                         │
│     - 检查 Accept-Encoding: gzip                              │
│     - 响应 Content-Type 不在跳过列表                          │
│     - 响应 Content-Length > MinSize                           │
│     - 非 SSE（SSE 需要用 chunked 可能冲突）                   │
│     - wrap ResponseWriter with gzip.Writer                   │
│     - set Content-Encoding: gzip + Vary: Accept-Encoding     │
│                                                             │
│   Option B: 逐 handler 选择性压缩（精确控制）                  │
│     - search handler → JSON 响应体积大，压缩收益高            │
│     - list handler → XML/JSON 列表响应，同上                  │
│     - file GET → 存储对象，根据 Content-Type 决策             │
│     - thumbail → 已经是压缩图片，跳过                        │
└─────────────────────────────────────────────────────────────┘

Phase 2 — 流式压缩（SSE + 大文件）：
┌─────────────────────────────────────────────────────────────┐
│ SSE 流式 Chat：                                              │
│   ChatStream 发送 text/event-stream                          │
│   NOT compatible with gzip wrapper                          │
│   → 使用 chunks 级别压缩或保持不压缩                         │
│                                                             │
│ 大文件 GET：                                                  │
│   Range 请求 → 压缩整个文件后再返回 range 浪费               │
│   → 对 Range 请求跳过压缩                                    │
│   → 或使用 content-range + gzip 分块（标准不支持）           │
│   → 简单方案：仅压缩非 Range 的完整文件 GET                  │
└─────────────────────────────────────────────────────────────┘

指标：
  http.response.compressed_bytes_total{content_type}
  http.response.uncompressed_bytes_total{content_type}
  http.response.compression_ratio{content_type}  // histogram
```

**影响面：**
| 组件 | 影响 | 工作量估计 |
|------|------|-----------|
| 压缩中间件（Option A） | `internal/middleware/compress.go` | 低（~100 行） |
| 配置（压缩等级/最小大小/跳过列表） | `internal/config/config_app.go` | 低 |
| S3 handler 压缩 | `internal/api/s3compat/handler.go` | 低 |
| SSE 压缩策略 | `internal/api/rest/search.go` | 中 |
| 指标 | `internal/telemetry/metrics.go` | 低 |
| Dockerfile 安装 brotli（可选） | 无（stdlib gzip 零依赖） | — |

### 为什么现在做

带宽 = 云成本。对文本密集型工作负载（搜索、日志、代码、文档），gzip 压缩可实现 5-10x 的带宽节省。这不是一个"不错的优化"——而是直接影响运营成本的工程决策。标准库 `compress/gzip` 零依赖引入（已用于 snapshot.go），中间件模式无侵入。`go-chi` 生态有现成的 `chi.middleware.Compress`（当前未使用）。对于 SaaS 部署，带宽通常是前三大成本之一。

---

## 4. 🟠 可观测性缺口：零运行时诊断端点

### 风险等级：高 (High)

> 生产服务器没有任何运行时诊断端点。当发生内存泄漏、goroutine 泄漏、CPU 热点、死锁等问题时，运维人员没有任何内置工具可以现场诊断——必须重启服务、附加外部 profiler、或者等着 OOM kill 后看日志。当前 `/healthz` 和 `/readyz` 只做简单的存活性检查，不提供任何运行时自省能力。

### 当前状态

```go
// cmd/server/main.go — 当前注册的路由
// /healthz  → fmt.Fprintln(w, "ok")
// /readyz   → db.Ping + storage.Stat
// /metrics  → promhttp.Handler()
//
// 缺失：
// /debug/pprof/        ← 无
// /debug/pprof/heap    ← 无
// /debug/pprof/goroutine ← 无
// /debug/pprof/profile ← 无 (CPU profiling)
// /debug/pprof/trace   ← 无 (execution trace)
```

**生产 incident 中缺少诊断工具的影响：**

| 症状 | 根因诊断手段 | 当前能力 |
|------|-------------|---------|
| 内存持续增长（疑似泄漏） | `curl /debug/pprof/heap` → 对比两个时间点的 heap profile | ❌ 无法现场获取 |
| Goroutine 数量暴涨 | `curl /debug/pprof/goroutine` → 查看所有 goroutine 栈 | ❌ 只能看到日志中的 goroutine 数 |
| CPU 使用率飙升 | `curl /debug/pprof/profile?seconds=30` → CPU profile | ❌ 需要外部工具附加 |
| 请求延迟突然变高 | `curl /debug/pprof/trace?seconds=5` → 执行追踪 | ❌ 无法诊断 |
| 死锁或活锁 | `curl /debug/pprof/goroutine?debug=2` → 加锁等待的 goroutine | ❌ 只能通过日志推断 |
| 对象分配热点 | `curl /debug/pprof/allocs` → 分配 profile | ❌ 无法定位 |

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `cmd/server/main.go:router` | chi router 注册 | 无 `pprof` 路由 |
| `middleware/middleware.go:healthzHandler` | `"ok"` | 不暴露运行时状态 |
| `middleware/middleware.go:readyzHandler` | DB + storage ping | 不暴露 goroutine 信息 |
| `telemetry/otel.go:Setup` | OTel SDK 初始化 | OTel 不提供 pprof 端点 |
| `telemetry/prometheus.go` | Prometheus 指标 | 指标是聚合的，无 goroutine 级可见性 |

### 边界情况暴露

| Edge Case | 场景 | 当前 | 应该 |
|-----------|------|------|------|
| **生产环境启用 pprof 的安全风险** | `/debug/pprof/` 公开可能泄露代码信息 | 不暴露 | 仅监听 localhost 或内部端口，或要求 admin scope |
| **CPU profiling 的性能影响** | `profile?seconds=30` 期间采样暂停所有 goroutine 约 1-2% | 完全不支持 | 安全使用说明：仅在诊断期间启用 |
| **Heap profile 文件膨胀** | `debug=2` 输出完整 goroutine 栈可能很大（>100MB） | 不支持 | 默认 debug=1（无函数参数，轻量） |
| **多实例环境中定位问题实例** | 10 个副本中只有 1 个有泄漏 | 无法区分 | `/debug/pprof/` 返回 server_id 标签 |
| **pprof 端点被 DDoS 攻击** | 攻击者频繁请求 CPU profile（消耗服务器性能） | 不受影响（无端点） | `pprof` 中间件加上限流 |

### 架构概要

```
Phase 1 — pprof 端点注册（最小改动）：
┌─────────────────────────────────────────────────────────────┐
│ 在 cmd/server/main.go 中：                                    │
│                                                             │
│ import _ "net/http/pprof"   // 注册 /debug/pprof/ 处理器    │
│                                                             │
│ // 将 pprof 挂载到 admin 子router（需要 admin scope 认证）    │
│ adminRouter.Handle("/debug/pprof/*", http.DefaultServeMux)  │
│                                                             │
│ // 或：监听独立的内部端口（推荐生产）                         │
│ go func() {                                                   │
│     log.Println(http.ListenAndServe(":6060", nil))           │
│ }()                                                           │
│                                                             │
│ 配置选项：                                                    │
│   APP_PPROF_ADDR=:6060  // 空 = 禁用，:6060 = 内部端口      │
│   APP_PPROF_MODE=disabled|internal|admin  // 访问控制        │
└─────────────────────────────────────────────────────────────┘

Phase 2 — /debug/vars 运行时状态（扩展诊断）：
┌─────────────────────────────────────────────────────────────┐
│ Goroutine 计数 / CPU 数 / 内存统计：                          │
│   GET /debug/vars → JSON                                     │
│   {                                                           │
│     "goroutines": 42,                                        │
│     "memory": {                                              │
│       "alloc_mb": 128.5,                                     │
│       "heap_inuse_mb": 95.2,                                 │
│       "gc_cycles": 1240,                                     │
│       "gc_pause_ms_p99": 3.2                                 │
│     },                                                       │
│     "build_info": {                                          │
│       "go_version": "go1.25",                                │
│       "commit": "abc123",                                    │
│       "build_time": "2026-07-10T00:00:00Z"                   │
│     },                                                       │
│     "uptime_seconds": 3600,                                  │
│     "open_files": 128,                                       │
│     "groutines_blocked": 0                                   │
│   }                                                           │
│                                                             │
│ 新增指标：                                                    │
│   runtime.goroutines{server_id}                               │
│   runtime.gc_pause_ms{quantile}                               │
│   runtime.memory_alloc_bytes{type}                            │
└─────────────────────────────────────────────────────────────┘
```

**影响面：**
| 组件 | 影响 | 工作量估计 |
|------|------|-----------|
| pprof 端点注册 + 配置 | `cmd/server/main.go` + `config/config_app.go` | **极低**（~15 行） |
| pprof 安全控制（admin scope 或内部端口） | `cmd/server/main.go` | 低 |
| `/debug/vars` 运行时状态 | `middleware/middleware.go` | 中 |
| 运行时指标 | `telemetry/metrics.go` | 低 |

### 为什么现在做

`/debug/pprof/` 的注册是 Go 生态中最被低估的最佳实践——引入 `net/http/pprof` 只需要一行 import，但对生产排障的价值不可估量。当前系统有 `/healthz` 和 `/readyz`，说明作者有运维意识，但缺少了最关键的一环：**问题发生时的现场取证能力**。不改的话，每次生产事件都需要"加 pprof import 重新部署 → 复现 → 诊断"的循环，将 MTTR 延长数小时到数天。对于任何运行在 production 中的 Go 服务，pprof 端点不是"可选增强"，而是"安全基线"。

---

## 5. 🟡 多协议错误模型碎片化

### 风险等级：中 (Medium)

> 系统通过四个协议暴露：REST（JSON）、S3（XML）、WebDAV（自定义 XML）、MCP（JSON-RPC）。每个协议对同一个服务端错误有不同的呈现方式。三个 SDK（Go/JS/Python）各自实现了错误解析逻辑。错误码只有 8 个且没有对外的 catalog。跨协议排障需要理解三套错误格式。

### 当前状态

```go
// REST — JSON 格式 (api/rest/handler.go:368-398)
func classify(err error) (string, string, int) {
    // 8 个 error code 的 switch:
    //   QuotaExceeded, NotFound, NoSuchUpload, InvalidArgument,
    //   InvalidRange, PreconditionFailed, AccessDenied, ObjectCorrupt
    // + default: InternalError
    // 输出: {"error":{"code":"NotFound","message":"object not found","request_id":"..."}}
}

// S3 — XML 格式 (api/s3compat/errors.go)
// 输出: <Error><Code>NotFound</Code><Message>...</Message>...<RequestID>...</RequestID></Error>
// 有自己的 error code 到 S3 Error Code 映射

// WebDAV — HTTP status + 自定义 XML (api/webdav/dav.go)
// 使用 WebDAV 标准错误码 (404 Not Found, 403 Forbidden, 507 Insufficient Storage)

// MCP — JSON-RPC 格式 (mcp/protocol.go)
// 输出: {"jsonrpc":"2.0","error":{"code":-32603,"message":"...",},"id":1}
```

**当前错误映射的碎片化：**

```
服务端错误 err                   → REST JSON  code          → S3 XML code
service.ErrNotFound              → "NotFound"              → "NoSuchKey"
service.ErrQuotaExceeded         → "QuotaExceeded"         → "QuotaExceeded" (自定义)
service.ErrForbidden             → "AccessDenied"          → "AccessDenied"
service.ErrInvalidArgs           → "InvalidArgument"       → "InvalidArgument"
service.ErrPreconditionFailed    → "PreconditionFailed"    → "PreconditionFailed"
service.ErrRangeNotSatisfiable   → "InvalidRange"          → "InvalidRange"
service.ErrObjectCorrupt         → "ObjectCorrupt"         → "ObjectCorrupt" (自定义)
service.ErrUploadNotFound        → "NoSuchUpload"          → "NoSuchUpload"
repository.ErrNotFound           → "NotFound"              → "InternalError" (误映射!)
```

**注意最后一行：** `repository.ErrNotFound` 被 S3 映射为 "InternalError"（因为 S3 的 classify 中没有处理它），同时 REST handler 中的 `classify` 会将其映射为 "NotFound" — 同一个错误在不同协议下返回不同含义。这是一个实际的 bug。

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/api/rest/handler.go:368-398` | `classify()` — 8 个错误码 | 无对外 catalog，无标准化 detail 字段 |
| `internal/api/s3compat/errors.go` | S3 XML 错误生成 | 与 REST 的 classify 有差异（repos.ErrNotFound 映射不同） |
| `internal/api/webdav/dav.go` | WebDAV HTTP 状态码 | 无结构化错误体 |
| `internal/mcp/protocol.go` | JSON-RPC 错误 | 自定义 code 映射 |
| `sdk/go/aerovault/client.go` | Go SDK 错误解析 | 需处理 REST JSON + 可能的非 JSON 响应 |
| `sdk/python/aero_vault.py` | Python SDK | 同上，两层错误解析 |
| `sdk/js/aero-vault.js` | JS SDK | 同上 |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **S3 handler 未处理 repo.ErrNotFound** | REST 返回 404 NotFound，S3 返回 500 InternalError | 协议不一致的 bug | 统一错误映射 |
| **SDK 收到非 JSON 响应** | REST 返回了非 JSON 格式错误（如 Go HTTP 错误） | SDK panic / 解析失败 | SDK 按 Content-Type 分路解析 |
| **重试决策困难** | 调用方需要知道"这个错误是否可重试？" | 无标准指示 | Error 响应包含 `retryable: true/false` |
| **多语言错误消息** | 用户期望中文错误而非英文 | 固定英文 | `Accept-Language` 协商 |
| **错误详情中有敏感信息** | 内部 SQL 错误或栈信息泄露到错误消息 | `InternalError` 会返回 `err.Error()` | 生产环境 sanitize error messages |

### 架构概要

```
Phase 1 — 统一错误定义（最小改动）：
┌─────────────────────────────────────────────────────────────┐
│ 新增: internal/api/errors.go  — 统一的错误目录              │
│                                                             │
│ type ErrorCode struct {                                      │
│     Code        string   // 机器可读的短字符串              │
│     HTTPStatus  int                                         │
│     S3Code      string   // S3 兼容的 XML Error Code        │
│     Retryable   bool     // 客户端是否可以安全重试          │
│     Message     string   // 默认人类可读消息                 │
│ }                                                             │
│                                                             │
│ var ErrorCatalog = map[error]*ErrorCode{                      │
│     service.ErrNotFound: {                                   │
│         Code: "NotFound",                                    │
│         HTTPStatus: 404,                                     │
│         S3Code: "NoSuchKey",                                 │
│         Retryable: false,                                    │
│     },                                                        │
│     service.ErrQuotaExceeded: {                              │
│         Code: "QuotaExceeded",                               │
│         HTTPStatus: 507,                                     │
│         S3Code: "QuotaExceeded",                             │
│         Retryable: true,   // 等待后重试可能成功             │
│     },                                                        │
│     // ... ~15~20 个错误码                                    │
│ }                                                             │
│                                                             │
│ type ErrorResponse struct {                                   │
│     Code       string            `json:"code"`               │
│     Message    string            `json:"message"`            │
│     RequestID  string            `json:"request_id,omitempty"`│
│     Retryable  bool              `json:"retryable"`          │
│     Details    map[string]any    `json:"details,omitempty"`   │
│ }                                                             │
│                                                             │
│ 规则：                                                        │
│   所有协议使用同一 ErrorCatalog 定义                           │
│   REST → JSON ErrorResponse                                   │
│   S3   → XML 映射（Code→S3Code, Message保持）                 │
│   MCP  → JSON-RPC error object（code 从 ErrorCode 映射）     │
│   WebDAV → HTTP status + XML（最小）                          │
└─────────────────────────────────────────────────────────────┘

Phase 2 — SDK 统一错误处理：
┌─────────────────────────────────────────────────────────────┐
│ 所有 SDK 实现统一的 Error 类/接口：                          │
│                                                             │
│ Go SDK:                                                      │
│   type APIError struct {                                     │
│       Code       string                                     │
│       Message    string                                     │
│       RequestID  string                                     │
│       Retryable  bool                                       │
│       StatusCode int                                        │
│       Details    map[string]any                              │
│   }                                                          │
│   func (e *APIError) Error() string { /* ... */ }           │
│   func (e *APIError) IsRetryable() bool { /* ... */ }       │
│                                                             │
│ SDK 的 HTTP 调用统一解析：                                    │
│   resp, err := http.Do(req)                                  │
│   if err != nil { return nil, err }                          │
│   if resp.StatusCode >= 400 {                                │
│       return nil, parseErrorResponse(resp)                   │
│   }                                                          │
│   // normal response handling                                │
└─────────────────────────────────────────────────────────────┘

指标：
  http.error_responses_total{code, protocol}
```

**影响面：**
| 组件 | 影响 | 工作量估计 |
|------|------|-----------|
| `internal/api/errors.go` 统一错误目录 | 新增文件 | 低 |
| REST `classify` → 使用 ErrorCatalog | `api/rest/handler.go` | 低 |
| S3 XML 错误 → 使用 ErrorCatalog | `api/s3compat/errors.go` | 低 |
| MCP 错误 → 使用 ErrorCatalog | `mcp/server.go` | 低 |
| WebDAV 错误增强 | `api/webdav/dav.go` | 低 |
| 修复 S3 repos.ErrNotFound 误映射 | `api/s3compat/errors.go` | **极低**（1 行） |
| SDK 统一错误解析 | 三个 SDK | 中 |
| 错误码文档化（OpenAPI + 独立文档） | `api/openapi.json` + `docs/` | 低 |

### 为什么现在做

错误模型碎片化看起来像一个"整洁性问题"，但它有着实在的负面影响：① `repository.ErrNotFound` 在 S3 下被错误映射为 500，这是一个 bug（影响 S3 SDK 使用的客户端）；② SDK 开发者需要处理两套（REST + S3）错误格式；③ 调用方无法通过标准字段判断"这个错误是否应该重试"；④ 随着新功能增加错误类型（如方向 1 的 `ContentBlocked`、方向 2 的 `PreconditionFailed`），碎片化的错误映射会越来越难以维护。统一错误目录是"一次投入，持续受益"的架构改进——未来每增加一个错误类型，只需在 ErrorCatalog 中添加一行。

---

## 跨方向协同关系

```
方向 1 (内存安全)      ← 方向 3 (压缩): 分块加密 + 流式压缩可以共享缓冲区池
                      ← 方向 4 (诊断): OOM 时 pprof heap profile 提供根因证据

方向 2 (写并发安全)    ← 方向 5 (错误模型): 412 PreconditionFailed 应纳入 ErrorCatalog
                      ← 方向 4 (诊断): 乐观锁冲突计数器→Prometheus→告警

方向 3 (压缩)          ← 方向 5 (错误模型): Vary: Accept-Encoding 头应记录在规范中
                      ← 方向 4 (诊断): 压缩比指标在 /debug/vars 中可查看

方向 4 (诊断)          ← 方向 5 (错误模型): ErrorCatalog 支持 /debug/errors 端点返回所有错误码
                      ← 方向 1, 2, 3: pprof 可用于诊断这些方向的性能问题

方向 5 (错误模型)      ← 方向 2 (写并发): 新增乐观锁错误码需加入 ErrorCatalog
                      ← 方向 3 (压缩): 压缩中间件应跳过 SST 流式错误帧
```

**建议实施顺序：**

| 阶段 | 方向 | 理由 |
|------|------|------|
| **🔥 立即** | 方向 4 Phase 1（注册 pprof） | 3 行代码，立即获得生产排障能力 |
| **🔥 立即** | 方向 5 Phase 1（修复 repos.ErrNotFound S3 映射 bug） | 1 行代码，修复协议不一致 bug |
| **Phase 1** | 方向 5 → 统一 ErrorCatalog | 为所有后续方向提供一致的错误基础 |
| **Phase 2** | 方向 1 → 分块加密框架（替换全缓冲） | 消除 OOM 风险，需要仔细设计兼容性 |
| **Phase 3** | 方向 2 → ETag 条件写入 | 需要 ErrorCatalog 的 412 支持 |
| **Phase 4** | 方向 3 → 压缩中间件 | 需要确认与分块加密的交互正确 |

---

> *第十四期全局扫描完成，未修改任何代码。本轮 5 个方向聚焦于"工程质量缺口"视角——不考虑新功能，而是识别当前代码中不改就会在生产环境出问题的硬伤。每个方向都有明确的代码锚点、具体的崩溃场景、和逐步可行的修复蓝图。*
