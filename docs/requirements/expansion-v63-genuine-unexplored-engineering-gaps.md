# AeroVault 高价值扩展方向 — 真实未被探索的工程缺口

> **视角：** 资深架构师 / 产品经理
> **方法：** 全局代码扫描（Go 源码 ~46K 行，24 子包，48 组 SQL 迁移，62 份既有分析）
> **去重验证：** 逐方向对照 `docs/requirements/` 下全部 62 份既有分析文档（v1–v62）、`docs/ROADMAP.md`、`docs/TODO.md`，**逐方向执行 grep 验证**，确认无实质性分析覆盖
> **日期：** 2026-07-10
> **原则：** 选取 62 轮分析中**零实质性分析**的高价值空白区域，每个方向在代码中有精确锚点

---

## 审阅：前 62 轮分析已覆盖的范围

前 62 轮分析已系统地覆盖了以下领域（仅列出大类）：

| 领域 | 覆盖轮次 |
|------|---------|
| S3 协议完备性 (SSE-C, Object Lock, Lifecycle, CORS, Logging, Notification, Policy, Batch Delete) | v23, v42, v56, v58, v61, v62 |
| AI/RAG 管线 (Embedder, Search, Chat, Agent, Reranker, PII, Cost, Budget, Cache) | v13, v22, v31, v41, v53, v59, v60, v61 |
| 多租户与鉴权 (JWT, API Key, SigV4, Scope, Policy, ACL, mTLS) | v5, v8, v15, v26, v27, v29, v32, v55 |
| 分布式与水平扩展 (Cluster Singleton, Postgres Transport, DR, 限流协调) | v28, v35, v44, v45, v55, v57 |
| 运维成熟度 (配置验证、优雅关闭、指标、告警、健康检查) | v10, v27, v34, v38, v39, v47, v60 |
| 性能与资源管理 (内存上限, LRU, 连接池, 限流, 零拷贝, HTTP/2) | v11, v14, v26, v27, v31, v34, v37, v38, v60 |
| 数据完整性 (Orphan GC, Scrub, Retention, Idempotency, 崩溃安全) | v5, v15, v17, v21, v23, v28, v49, v51, v58, v60, v61 |
| 多协议一致性 (REST/S3/WebDAV/MCP) | v19, v42, v59, v60 |
| Webhook 与事件 (死信, 重试, SSE, 背压) | v17, v23, v28, v38, v39, v44, v56, v60 |
| 加密 (SSE envelope 版本化, 密钥轮换, KMS) | v24, v44, v45, v49 |
| FUSE/POSIX 网关, 链上监管, S3 Select, 批量操作 | v32, v33, v36 |
| 存储后端连接池, Context 传播, 优雅关闭 | v38 |
| 底层工程缺陷 (TOCTOU, 竞态, SSE 断开, 内存膨胀, 配置交叉依赖) | v55, v60, v62 |

**核心发现：** 经过 62 轮分析，功能层和架构层的"有没有"问题已高度饱和。本期聚焦的 5 个方向在 62 轮分析中**零实质性架构分析**（均为 `grep` 验证结果）。

---

## 本期方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 | 62 轮覆盖 |
|---|------|------|--------|---------|---------|-----------|
| **1** | **内容感知增量重索引：索引器每次全量读取对象内容** | 性能/成本 | **P1** — 大对象（>10MB）的每次索引事件都重新读取全量内容、重新提取、重新分块、重新嵌入；内容未变时完全浪费 | `internal/ai/indexer.go` (`IndexObjectByID` 无 skip-if-unchanged)；`internal/ai/extractor.go` (Extract 总是从零开始) | ❌ **零分析** |
| **2** | **非版本化桶的并发写入安全：PUT/PUT 竞态与 TOCTOU 窗口** | 数据完整性 | **P1** — 非版本化桶中两个并发 PUT 到同一 key 无任何锁定或 compare-and-swap；存储写入与元数据提交之间存在竞态窗口 | `internal/service/file_crud.go:41-88` (`Put` 无乐观锁)；`internal/repository/sql_objects.go` (`UpsertObject` 无条件覆盖) | ❌ **零分析** |
| **3** | **存储后端健康与容量可编程 API 缺失** | 运维/可靠性 | **P2** — 无法通过 API 查询存储后端健康状态、剩余容量、首选存储类；本地磁盘写满时返回不可预测的 I/O 错误而非 507 | `internal/storage/storage.go` (Storage 接口无 `Health()`/`Capacity()` 方法)；`internal/storage/local.go` (无磁盘空间检查) | ⚠️ v55 方向三提出存储后端观测性度量但聚焦**指标收集**而非**可编程 API** |
| **4** | **桶策略 JSON 解析无缓存：每次请求重复解析** | 性能 | **P2** — 开启了桶策略的 S3 bucket，每个请求都完整 JSON 解析策略文档再 Evaluate；未命中缓存且 Policy 较大时每次增加 ~50–200µs 延迟 | `internal/api/s3compat/handler.go:93-99` (`checkBucketPolicy` 每次调用 `auth.ParsePolicy`)；`internal/auth/policy.go:82-86` (`Eval` 方法) | ⚠️ v57 方向四表格中一行提及「策略评估缓存」概念但**零架构分析** |
| **5** | **Web UI 与 REST API 的 Admin 能力鸿沟：管理控制面需独立实现** | 产品/UX | **P2** — 当前 Web UI 仅面向最终用户（文件浏览/搜索/聊天）；完整的 Admin 操作（租户管理、Key 管理、配额、审计、Job 管理）只能通过 CLI 或直接 API 调用完成 | `internal/webui/static/index.html` (仅文件操作 UI)；`internal/api/rest/admin.go` (Admin API 无 Web UI) | ⚠️ v46 方向二分析 Web UI 生产硬化，v30/v41 分析管理控制台概念但聚焦**全新构建**而非**渐进式 Admin 面板扩展** |

---

## 方向一：内容感知增量重索引

### 现状

当前索引器 `IndexObjectByID` 在收到 `object.created` 事件后，无论对象内容是否变化，都执行全量重索引：

```go
// internal/ai/indexer.go:251-320 — IndexObjectByID 简化流程
func (ix *Indexer) IndexObjectByID(ctx context.Context, objectID int64) error {
    // Step 1: 从存储读取完整对象内容
    obj, err := ix.repo.GetObjectByID(ctx, objectID)
    rc, _, err := ix.store.Get(ctx, obj.StorageKey)  // ← 全量读取！大对象可能 >100MB
    body, err := io.ReadAll(rc)                        // ← 全量加载到内存！
    
    // Step 2: 文本提取（远程调用或本地解析）
    text, err := ix.extractor.Extract(ctx, obj.Key, obj.ContentType, body, obj.Size)
    
    // Step 3: 分块
    chunks := ix.chunker.Chunk(text)
    
    // Step 4: 嵌入（远程 API 调用）
    embeddings, err := ix.embedder.Embed(ctx, chunkTexts)
    
    // Step 5: 输出到 sink（BM25 / Qdrant / pgvector）
    for _, chunk := range chunks {
        ix.sink.WriteChunk(ctx, ...)
    }
    // ← 无任何方式跳过未变更内容
}
```

**关键缺陷：不存在任何增量/跳过机制。**

当前 `IndexObjectByID` 的调用场景包括：

| 触发场景 | 内容是否变更 | 当前浪费 |
|---------|------------|---------|
| `object.created`（新对象） | ✅ 新内容 | 无浪费 |
| `object.created`（版本覆盖） | ✅ 新内容 | 无浪费 |
| `AI_REINDEX_STALE_ON_START` | ❌ 可能未变 | **全量浪费** |
| `reconcile` 修复后重索引 | ❌ 可能未变 | **全量浪费** |
| 对象 `metadata`/`tags` 更新触发的重索引 | ❌ 仅元数据变化 | **全量浪费** |

### 为什么这是问题

**性能/成本：**

| 对象大小 | 索引操作 | 无增量机制 | 有增量机制 | 节省 |
|---------|---------|-----------|-----------|------|
| 1 KB | 重索引 | ~50ms + 1 次 Embed API | ~1µs (检查 hash) | 接近 100% |
| 1 MB | 重索引 | ~500ms + ~10 次 Embed API | ~1µs | 接近 100% |
| 100 MB PDF | 重索引 | ~30s + ~1000 次 Embed API + Remote Extract | ~1µs | 接近 100% |

**嵌入成本：** Embed API 调用是按 token 付费的（对于远程 provider）。一个 100MB 的文本文件可能对应数百万 token。如果内容未变，每次重索引都是真金白银的浪费。

**远程提取成本：** `RemoteExtractor` 将整个对象发送到外部提取服务。对于经常触发重索引的大型文档（如频繁标记的对象），这是显著的带宽和 API 消耗。

**资源瓶颈：** `io.ReadAll(rc)` 将整个对象加载到内存中。对于大对象（>100MB），每次重索引都产生内存峰值。多对象并发重索引可能导致 OOM。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/ai/indexer.go:251-320` | `IndexObjectByID` 总是全量处理 | 无 skip-if-unchanged 逻辑 |
| `internal/ai/indexer.go:131-172` | `Run` 处理事件循环 | 无事件去重（重复事件导致重复索引） |
| `internal/ai/extractor.go` | `Extract` 总是从字节流提取 | 无法接受"内容未变"的提示 |
| `internal/ai/chunker.go` | `Chunk` 总是分块 | 无缓存/reuse 逻辑 |
| `internal/repository/repository.go` (Object 结构体) | 有 `ETag` 字段但索引器不使用 | ETag 可用于 "if-none-match" 跳过 |
| `internal/storage/storage.go` | `Stat` 返回 `ObjectInfo{ETag:...}` | 索引器未利用 Stat 做变更检测 |

### 增量检测策略

```go
// 策略 A: ETag 比较（轻量，无额外存储）
// Step 0: 仅检查 ETag 是否变化
obj, _ := ix.repo.GetObjectByID(ctx, objectID)
if obj.ETag == storedETag {  // ← 需要存储上次索引的 ETag
    return nil  // 跳过：内容未变
}

// 策略 B: Content-Hash 比较（精确，需要计算 hash）
// 计算流式 SHA-256，与之前存储的 hash 比较
// 适合首次索引时的去重（多版本内容相同）

// 策略 C: 对象最后修改时间（最简单）
if obj.UpdatedAt == lastIndexedAt {
    return nil
}
```

**最少修改路径（策略 A）：**

```go
// 1. 在 Chunk 表或 Object 表增加 last_indexed_etag 字段（迁移 0026）
// 2. IndexObjectByID 开始时检查：
//    obj, _ := ix.repo.GetObjectByID(ctx, objectID)
//    lastIdx, _ := ix.repo.GetLastIndexedETag(ctx, objectID)
//    if obj.ETag == lastIdx { return nil }
// 3. 成功索引后更新 last_indexed_etag
```

**规模估计：** ~120 行（迁移 + repository 方法 + 索引器逻辑）+ ~80 行测试

---

## 方向二：非版本化桶的并发写入安全

### 现状

非版本化桶中的 `Put` 操作执行以下序列（简化）：

```go
// internal/service/file_crud.go:41-88
func (s *FileService) Put(ctx context.Context, tenant, bucket, key string, ...) (repository.Object, error) {
    // ... 配额检查、锁检查 ...
    
    // Step 1: 写入存储后端（网络 I/O，~10-500ms）
    info, err := s.store.Put(ctx, sk, reader, size, ...)
    
    // Step 2: 写入元数据（数据库 I/O，~1-10ms）
    saved, err := s.repo.UpsertObject(ctx, obj)  // ← 无条件覆盖
    
    // ...
}
```

**竞态窗口：**

```
时间线:
  PUT-A (goroutine 1)          PUT-B (goroutine 2)
  │                             │
  ├─ store.Put(sk, dataA)       │
  │                             ├─ store.Put(sk, dataB)
  │         ← dataA 写入完成    │
  │         ← dataB 覆盖 dataA  │
  ├─ repo.UpsertObject(objA)    │
  │                             ├─ repo.UpsertObject(objB)
  │         ← objA 写入 DB      │
  │                             ← objB 覆盖 objA
  │                             │
  └─ ✅ 返回 objA               └─ ✅ 返回 objB
     → DB 指向 objB（最后写入者胜出）
     → 存储上 dataA 已被 dataB 覆盖 → ✅ 一致

但另一种交错:
  PUT-A (goroutine 1)          PUT-B (goroutine 2)
  │                             │
  ├─ store.Put(sk, dataA)       │
  ├─ repo.UpsertObject(objA)    │
  │         ← objA 写入 DB      │
  │                             ├─ store.Put(sk, dataB)
  │         ← dataB 覆盖 dataA  │
  │                             ├─ repo.UpsertObject(objB)
  │         ← objB 覆盖 objA    │
  │                             │
  └─ ✅ 返回 objA               └─ ✅ 返回 objB
     → DB 指向 objB（√）
     → 存储上是 dataB（√）
     → ✅ 一致（巧合：B 的 Put 在 A 的 repo 写入之后）

更糟的交错:
  PUT-A (goroutine 1)          PUT-B (goroutine 2)
  │                             │
  ├─ store.Put(sk, dataA)       │
  │                             ├─ store.Put(sk, dataB)
  │         ← dataA 写入完成    │
  │         ← dataB 覆盖 dataA  │
  │                             ├─ repo.UpsertObject(objB)
  │                             │   ← objB 写入 DB（B 的 ETag）
  │         ← repo.UpsertObject(objA) 失败（DB 连接超时？）→ 返回 error
  │                             │
  └─ ❌ Put 返回 error          └─ ✅ 返回 objB
     → 客户端重试 PUT-A
     → DB 指向 objB（√）
     → 存储上是 dataB → PUT-A 重试后也指向 dataB（√）
     → 但 PUT-A 原始 dataA 已丢失

更多问题:
1. store.Put 成功但 repo.UpsertObject 失败 → 孤儿 blob
2. 两个 PUT 同时 store.Put → 存储后端的最后写入语义可能导致数据A→B→A 的交错
3. 没有"预期 ETag"比较 → 无法实现 CAS（Compare-And-Swap）写入
```

### 为什么这是问题

**数据完整性：** 并发写入没有 CAS 语义，客户端无法实现"仅在未被他人修改时写入"的安全更新。对于配置管理、协作编辑、CI/CD 状态更新等场景，这是关键缺失。

**S3 兼容性：** AWS S3 的 PutObject 在非版本化桶中没有原子 compare-and-swap（用户通过 If-Match/If-None-Match 实现条件写入）。但 AeroVault 的 S3 handler 没有将 `If-Match`/`If-None-Match` 请求头映射到条件写入路径。

**丢失更新：** 没有乐观锁的 "last write wins" 语义意味着：如果客户端 A 读取对象、客户端 B 覆盖对象、客户端 A 写回，客户端 B 的修改被静默覆盖。虽然这是 S3 的 expected behavior，但**没有任何机制可以检测或预防**。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/service/file_crud.go:41-88` | `Put` 无乐观锁 | 无条件覆盖 |
| `internal/repository/sql_objects.go` | `UpsertObject` 无条件 `INSERT OR REPLACE`/`ON CONFLICT DO UPDATE` | 不支持 `WHERE ... AND updated_at = $expected` |
| `internal/api/rest/handler.go:44-46` | Write preconditions (If-Match/If-None-Match) 已有 check 但**仅限 REST** | S3 handler 未实现 If-Match/If-None-Match 写路径 |
| `internal/api/s3compat/handler.go` | PutObject 不读取 If-Match/If-None-Match | 无条件写入 |
| `internal/storage/storage.go` | `Put` 接口总是覆盖 | 无 CAS 写原语 |

### 建议修复方向

**Phase 1：REST API 的 If-Match/If-None-Match 写条件 → S3 handler 对齐**

```go
// internal/api/s3compat/handler.go:PutObject 新增
if etag := r.Header.Get("If-Match"); etag != "" {
    cur, err := h.svc.Stat(ctx, tenant, bucket, key)
    if err != nil { writeS3Error(...); return }
    if `"`+cur.ETag+`"` != etag {
        writeS3Error(w, r, service.ErrPreconditionFailed)
        return
    }
}
```

**Phase 2：FileService.Put 支持条件写入（Conditional Put）**

```go
type PutOptions struct {
    // ... 现有字段 ...
    ExpectedETag string  // If-Nonempty-Match: only succeed if current ETag matches
}
```

**Phase 3：UpsertObject 支持 `WHERE updated_at = $expected`**

```go
// 在 SQL 层面：UPDATE objects SET ... WHERE tenant=$1 AND bucket=$2 AND key=$3 AND updated_at=$4
// 返回 RowsAffected=0 → 并发修改检测
```

**规模估计：** ~200 行 + 测试

---

## 方向三：存储后端健康与容量可编程 API

### 现状

`storage.Storage` 接口定义了对象操作的完整契约，但**没有健康检查或容量查询方法**：

```go
// internal/storage/storage.go — 当前 Storage 接口
type Storage interface {
    Put(...)
    Get(...)
    Stat(...)
    Delete(...)
    List(...)
    PresignGet(...)
    PresignPut(...)
    InitMultipart(...)
    UploadPart(...)
    CompleteMultipart(...)
    AbortMultipart(...)
    Backend() string
    
    // ❌ 没有以下方法：
    // Health(ctx) (HealthStatus, error)
    // Capacity(ctx) (CapacityInfo, error)
    // PreferredStorageClass() string
}
```

**当前系统的存储健康感知：**

```go
// cmd/server/main.go:88-95 — /readyz 处理函数
func readyzHandler(repo repository.Repository, store storage.Storage) http.HandlerFunc {
    return func(w http.ResponseWriter, req *http.Request) {
        if err := repo.Ping(req.Context()); err != nil { ... }
        // 存储健康探测：Stat 一个不存在 key
        if _, err := store.Stat(req.Context(), "@healthz/probe"); err != nil && !errors.Is(err, storage.ErrNotFound) {
            http.Error(w, "storage: "+err.Error(), http.StatusServiceUnavailable)
        }
        // ❌ 没有容量检查
        // ❌ 没有后端特定健康指标返回
        // ❌ 不能区分"后端慢"和"后端死"
    }
}
```

**存储写入路径无空间预检：**

```go
// internal/service/file_crud.go:41-88 — Put 写入路径
func (s *FileService) Put(...) {
    // ... 配额检查 ...
    // ❌ 没有存储层容量检查
    // 当本地磁盘写满时: os.WriteFile → syscall.ENOSPC → 500 Internal Error
    // 期望: 507 Insufficient Storage
}
```

### 为什么需要

**存储容量耗尽时优雅降级：**

| 场景 | 当前行为 | 期望行为 |
|------|---------|---------|
| 本地磁盘使用率 95% | `os.WriteFile` → `ENOSPC` → HTTP 500 | PUT 前检查 → HTTP 507 Insufficient Storage |
| S3 bucket 已达配额 | S3 API error → HTTP 500 | 提前预检 → 拒绝写入 + 清晰错误码 |
| OSS Bucket 流控 | 随机超时 → HTTP 500 | 通过健康检查判断 → 503 Service Unavailable |

**客户端决策：**

```
无健康 API:                         有健康 API:
客户端: PUT /v1/files/big.iso       客户端: GET /v1/health/storage
服务端: ... 磁盘满 ...              服务端: {local: {status: "degraded", 
服务端: 500 Internal Error                   free_bytes: 52428800}}
客户端: ???                       客户端: 切换到另一个 AeroVault 实例
  
S3 客户端: PUT /s3/bucket/key       客户端: GET /v1/health/storage
服务端: 无感知到 S3 bucket 接近配额  服务端: {s3: {status: "healthy", 
服务端: PUT → S3 返回 403 QuotaExceeded      quota_used_percent: 92}}
服务端: 500 (未映射错误)            客户端: 延迟大文件写入
```

**运维与自动化：**

```
无 API:
  - 运维手动 `df -h` 查看磁盘
  - 无法通过监控系统自动获取全局存储健康
  
有 API:
  - Prometheus 通过 /metrics 获取
  - 编排系统 (K8s, Nomad) 通过 /readyz 获取
  - 租户通过 /v1/health/storage 获取
  - 管理员 Dashboard 实时显示
```

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/storage/storage.go` | Storage 接口无 Health/Capacity 方法 | 无契约 |
| `internal/storage/local.go` | Local 存储实现 | 可读取 `syscall.Statfs_t` 获取磁盘信息 |
| `internal/storage/s3.go` | S3 存储实现 | 可调用 `HeadBucket` 或 `ListObjects` 探测 |
| `internal/storage/oss.go` | OSS 存储实现 | 可调用 `GetBucketStat` |
| `internal/storage/cos.go` | COS 存储实现 | 可调用 `HeadBucket` |
| `internal/storage/factory.go` | 工厂函数 | 无健康参数传递 |
| `cmd/server/main.go:88-95` | `/readyz` 存储健康探测 | 仅有 Stat 探测；无容量报告 |

### Storage 接口扩展

```go
type HealthStatus struct {
    Status    string // "healthy" | "degraded" | "unavailable"
    LatencyMs int64  // 探测延迟
    Error     string // 状态描述（degraded/unavailable 时）
}

type CapacityInfo struct {
    TotalBytes     int64   // 总容量（0 = 不可知）
    UsedBytes      int64   // 已用容量
    FreeBytes      int64   // 剩余容量
    UsedPercent    float64 // 使用百分比
    ObjectCount    int64   // 对象数（0 = 不可知）
}

type Storage interface {
    // ... 现有方法 ...
    
    // Health returns a lightweight health check result.
    // The implementation should be cheap (< 100ms).
    Health(ctx context.Context) (HealthStatus, error)
    
    // Capacity returns the backend's storage capacity metrics.
    // Returns zero-valued CapacityInfo when the backend cannot
    // provide capacity information.
    Capacity(ctx context.Context) (CapacityInfo, error)
}
```

**规模估计：** ~150 行（接口 + 各后端实现）+ ~80 行测试 + ~30 行 API 暴露

---

## 方向四：桶策略 JSON 解析无缓存

### 现状

每个 S3 请求如果目标 bucket 配置了策略，都需要重新解析策略 JSON：

```go
// internal/api/s3compat/handler.go:93-99
func (h *Handler) checkBucketPolicy(w http.ResponseWriter, r *http.Request, bucket, action string) bool {
    cfg, err := h.svc.GetBucketConfig(r.Context(), mw.TenantFrom(r.Context()), bucket)
    if err != nil || cfg.Policy == "" {
        return true  // 无策略 → 放行
    }
    p, err := auth.ParsePolicy(cfg.Policy)  // ← 每次请求都 JSON 解析！
    if err != nil {
        return true
    }
    // ...
    if !auth.Allowed(p, action, host) {
        writeS3Error(w, r, service.ErrForbidden)
        return false
    }
    return true
}
```

**解析流程：**

```
请求 → GetBucketConfig（DB 查询）→ 取 Policy JSON 字符串
     → json.Unmarshal → parseStatements → build Condition map
     → Eval 遍历所有 statement
```

`ParsePolicy` 的内部实现：

```go
// internal/auth/policy.go:44-62
func ParsePolicy(doc string) (*Policy, error) {
    var p struct {
        Version   string           `json:"Version"`
        Statement []policyStatement `json:"Statement"`
    }
    if err := json.Unmarshal([]byte(doc), &p); err != nil { // ← 每次 JSON 解析
        return nil, err
    }
    statements := make([]Statement, 0, len(p.Statement))
    for _, s := range p.Statement {
        // 遍历解析 principle/action/resource/condition...
    }
    return &Policy{Version: p.Version, Statements: statements}, nil
}
```

**每条请求的额外开销：**

| Policy 大小 | 解析时间 | 额外延迟占比 |
|------------|---------|------------|
| 空（无 Policy） | 0 | 0%（仅 DB 查询） |
| 小（3 条 statement） | ~10µs | 可忽略 |
| 中（10 条 statement） | ~50µs | 低 |
| 大（50 条 statement + 复杂 condition） | ~200µs | 中（对延迟敏感场景显著） |
| 极大（数百条 + 嵌套 condition） | ~1-2ms | 高 |

**更严重的问题：** 没有缓存的场景下，**同一个 bucket 上每秒数千次请求每次重新解析完全相同的 JSON 字符串**。

### 为什么需要

**性能：** 策略解析是纯 CPU 操作，没有 I/O 等待。在 CPU-bound 场景下，重复解析是无意义的浪费。

**可观测性：** 没有缓存命中率指标，无法量化"策略解析占用了多少 CPU 时间"。

**热路径：** `checkBucketPolicy` 在**每个 S3 PUT/GET/HEAD/DELETE/POST 请求**上都调用。它是 S3 协议处理的热路径。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/api/s3compat/handler.go:93-99` | `checkBucketPolicy` 每次调用 `ParsePolicy` | 无缓存 |
| `internal/auth/policy.go:44-62` | `ParsePolicy` JSON 解析 + Statement 构建 | 无结果缓存 |
| `internal/auth/policy.go:167-178` | `Eval` 遍历所有 statement 执行 condition check | 无预编译优化（如 statement 按 action/ip 索引） |

### 缓存设计

```go
// 方案 A: 简单 TTL 缓存（~40 行）
type policyCache struct {
    mu    sync.RWMutex
    cache map[string]*cacheEntry  // bucketKey → parsed Policy
}

type cacheEntry struct {
    policy    *Policy
    expiresAt time.Time
}

func (pc *policyCache) GetOrParse(bucketKey, policyJSON string) (*Policy, error) {
    pc.mu.RLock()
    e, ok := pc.cache[bucketKey]
    pc.mu.RUnlock()
    if ok && time.Now().Before(e.expiresAt) {
        return e.policy, nil
    }
    p, err := ParsePolicy(policyJSON)
    if err != nil {
        return nil, err
    }
    pc.mu.Lock()
    pc.cache[bucketKey] = &cacheEntry{policy: p, expiresAt: time.Now().Add(30*time.Second)}
    pc.mu.Unlock()
    return p, nil
}

// 方案 B: 使用 bucket config 的 updated_at 做条件缓存（更精确，~60 行）
// 将缓存与 bucket configuration 的版本关联
// 当 BucketConfig.UpdatedAt 变化时自动失效
```

**规模估计：** ~60 行（缓存实现 + 集成）+ ~40 行测试

---

## 方向五：Web UI Admin 控制面板缺失

### 现状

当前 Web UI 是一个面向最终用户的单页应用，包含 4 个标签页：

| 标签页 | 功能 | 认证要求 |
|--------|------|---------|
| 语义搜索 (search) | 搜索、浏览文件 | 无（通过 header） |
| 对象详情 (detail) | 查看对象元数据、标签、版本 | 无 |
| 血缘 (lineage) | 查看 AI 消费历史 | 无 |
| 聊天 (chat) | RAG 对话 | 无 |

**完全缺失的管理功能：**

| 管理功能 | REST API | CLI 支持 | Web UI |
|---------|---------|---------|--------|
| 租户管理（创建/删除/状态/配额/预算） | ✅ | ✅ | ❌ |
| API Key 管理（添加/列出/吊销） | ✅ | ✅ | ❌ |
| JWT 签发 | ✅ | ❌（仅 API） | ❌ |
| Job 管理（列出/重试） | ✅ | ✅ | ❌ |
| 审计日志查看 | ✅ | ✅ | ❌ |
| 桶策略管理 | ✅ | ❌ | ❌ |
| 桶生命周期配置 | ✅ | ❌ | ❌ |
| Webhook 失败重试 | ✅ | ❌ | ❌ |
| SSE 实时事件流 | ✅ | ❌ | ❌ |
| 存储后端健康面板 | ❌ | ❌ | ❌ |

**管理者当前的工作流：**

```bash
# 运营商查看审计日志
aero-vault cli admin audit list --limit 50
# → 查看结果 JSON
# → 无法排序、过滤、搜索

# 运营商创建租户
aero-vault cli admin tenants create acme-corp --display-name "Acme Corp"
aero-vault cli admin tenants quota acme-corp 1073741824 10000

# 运营商检查失败 job
aero-vault cli admin jobs list --status failed
aero-vault cli admin jobs retry <id>

# 以上全部没有 Web UI → 运维人员必须学习 CLI → 增加采用门槛
```

### 为什么需要

**角色分离：**

```
开发者/终端用户                    运维/管理员
    │                                │
    ├─ Web UI (/ui)                  ├─ CLI (aero-vault cli admin ...)
    │   · 搜索文件                   │   · 租户管理
    │   · 上传/下载                  │   · Key 管理
    │   · 聊天                       │   · 审计查看
    │   · 查看详情                   │   · Job 管理
    │                                │
    └─ 无管理入口                    └─ 无 Web UI
```

现代 SaaS 平台期望**运维管理也通过 Web 界面完成**，CLI 作为补充而非主要工具。

**采用门槛：** 潜在用户 `make run` 后打开 `http://localhost:8080/ui` 看到搜索界面，但找不到任何管理功能。他们需要阅读文档、安装 CLI、学习命令——这个过程比看到完整的 Admin 面板要慢得多。

**紧急操作：** 当管理员在手机上收到告警时，通过 CLI 排查比打开浏览器慢，但**当身边只有浏览器时（PC/平板/手机），CLI 不可用**。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/webui/static/index.html` | 282 行 SPA，仅文件操作 | 无任何 Admin 功能 |
| `internal/api/rest/admin.go:1-419` | 完整的 Admin REST API | 有 API 但无 UI 绑定 |
| `internal/api/rest/router.go` | REST router 注册 Admin 路由 | UI 未消费这些路由 |
| `internal/cli/cli_admin.go:1-419` | CLI admin 子命令 | 功能与 UI 重复但无法通过 UI 访问 |

### 建议方向

**渐进式扩展：在现有 Web UI 侧边栏增加 "Admin" 标签页**

```html
<div class="tabs">
  <div class="tab" onclick="switchTab('search')">search</div>
  <div class="tab" onclick="switchTab('detail')">detail</div>
  <div class="tab" onclick="switchTab('lineage')">lineage</div>
  <div class="tab" onclick="switchTab('chat')">chat</div>
  <div class="tab" onclick="switchTab('admin')">admin</div>  <!-- ← 新增 -->
</div>
```

**Admin 标签页支持的初始功能（按优先级）：**

1. **租户列表 + 创建/删除**（复用 `GET /v1/admin/tenants` + `POST /v1/admin/tenants`）
2. **租户配额/预算编辑**（复用 `PUT .../quota` + `PUT .../budget`）
3. **API Key 列表 + 吊销**（复用 `GET /v1/admin/keys` + `DELETE /v1/admin/keys/{hash}`）
4. **审计日志浏览**（复用 `GET /v1/admin/audit`）
5. **Job 列表 + 重试**（复用 `GET /v1/admin/jobs` + `POST /v1/admin/jobs/{id}/retry`）

**认证：** Admin 操作通过 `X-Aero-Tenant` header 中的操作员 key 鉴权。Web UI 可在 Admin 标签页要求用户额外输入操作员 API Key。

**规模估计：** 在现有 `index.html` 中新增 ~300 行 HTML+JS（渐进式，不引入构建步骤）

---

## 综合优先级矩阵

| # | 方向 | 影响面 | 商业价值 | 实现难度 | 估算规模 | 前置依赖 |
|---|------|--------|---------|---------|---------|---------|
| 1 | 内容感知增量重索引 | 性能/成本 | 🔴 高（Embed API 成本 + 大对象重索引开销） | 低 | ~200 行 + 测试 | 迁移 0026 |
| 2 | 非版本化桶并发写入安全 | 数据完整性 | 🔴 高（并发场景数据一致性） | 中 | ~200 行 + 测试 | 无 |
| 3 | 存储后端健康与容量 API | 运维/可靠性 | 🟠 中（生产运维刚需，错误处理改进） | 中低 | ~260 行 + 测试 | 无 |
| 4 | 桶策略解析缓存 | 性能 | 🟡 中低（仅在策略密集型场景显著） | 极低 | ~100 行 + 测试 | 无 |
| 5 | Web UI Admin 控制面板 | 产品/UX | 🟠 中（降低运维采用门槛） | 中低 | ~300 行 HTML+JS | 无 |

### 推荐实施顺序

```
Sprint 1（快速见效）：
  ┌─────────────────────────────────────────────┐
  │ #4 桶策略缓存（~100 行）                     │
  │ #1 增量重索引（~200 行）                     │
  └─────────────────────────────────────────────┘

Sprint 2（工程加固）：
  ┌─────────────────────────────────────────────┐
  │ #3 健康与容量 API（~260 行）                 │
  │ #2 并发写入安全（~200 行）                   │
  └─────────────────────────────────────────────┘

Sprint 3（产品体验）：
  ┌─────────────────────────────────────────────┐
  │ #5 Web UI Admin 面板（~300 行）              │
  └─────────────────────────────────────────────┘
```

---

## 与既有 62 份分析的去重对照

| 本文件方向 | grep 验证命令 | 既有分析覆盖情况 | 去重结论 |
|-----------|-------------|----------------|---------|
| **方向一：内容感知增量重索引** | `grep -r "incremental.*index\|index.*skip\|skip.*reindex\|content.*hash.*index\|etag.*skip\|reindex.*skip\|index.*unchanged\|index.*change.*detect" docs/requirements/` → **0 命中**。v7 覆盖 CAS 内容去重存储（Content-Addressable Storage）但聚焦**块级去重**而非**索引跳过**；v4 覆盖 Content-Hash 但不涉及增量索引 | ✅ **完全去重** |
| **方向二：非版本化桶并发写入安全** | `grep -r "concurrent.*put\|put.*concurrent\|concurrent.*write.*same.*key\|put.*race\|write.*race.*non.*version\|concurrent.*overwrite.*safety\|cas.*put\|compare.*swap.*object\|conditional.*write.*s3\|if-match.*put\|if-none-match.*put" docs/requirements/` → **0 命中**。v55 方向一覆盖配额预检 TOCTOU（Quota Pre-check TOCTOU Race）但聚焦**配额检查**而非**写入路径并发安全**；v60 方向一覆盖 PerTenantConcurrencyLimiter TOCTOU 但聚焦**限流器** | ✅ **完全去重** |
| **方向三：存储后端健康与容量 API** | `grep -r "storage.*health.*api\|capacity.*api\|Health.*Storage.*method\|Capacity.*Storage.*method\|storage.*health.*programmatic\|storage.*api.*health" docs/requirements/` → **0 命中**。v55 方向三覆盖"存储后端可观测性真空"但聚焦**metrics 和告警**（`storage_health` gauge、`storage_capacity_bytes` gauge），**非可编程 API** | ✅ **与 v55 互补**（v55 聚焦指标，本期聚焦 API） |
| **方向四：桶策略解析缓存** | `grep -r "policy.*cache\|ParsePolicy.*cache\|policy.*parse.*cache\|policy.*eval.*cache\|bucket.*policy.*cache" docs/requirements/` → **0 命中**。v57 方向四表格中**仅一行**列出"策略评估缓存（TTL 30s）"作为 IAM 策略深度方向的一个实施子项，**无任何架构分析、无代码锚点、无性能度量** | ✅ **去重**（v57 仅概念提及，零分析） |
| **方向五：Web UI Admin 面板** | `grep -r "admin.*web.*ui\|admin.*console.*web\|web.*admin.*panel\|web.*ui.*admin.*control\|admin.*dashboard.*web" docs/requirements/` → **0 命中**。v46 方向二覆盖"Web UI 生产硬化"（错误处理、加载态、XSS、移动适配）但聚焦**现有 UI 的质量提升**而非**Admin 功能扩展**。v30 方向三覆盖"管理控制台"但聚焦**全新独立 Admin UI** 而非**在现有 UI 中增量添加 Admin 标签页** | ✅ **去重**（v46 聚焦质量，v30 聚焦全新构建，本期聚焦渐进式 Admin 标签页扩展） |
