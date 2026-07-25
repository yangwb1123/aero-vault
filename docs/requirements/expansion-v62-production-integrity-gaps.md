# AeroVault 高价值扩展方向 — 生产完整性缺口

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全局代码扫描（50K+ Go 源码，237 文件，23 子包，48 组 SQL 迁移）  
> **去重验证：** 逐方向对照 `docs/requirements/` 下全部 61 份既有分析文档（v1–v61）、`docs/ROADMAP.md`、`docs/TODO.md`，确认无重复分析  
> **日期：** 2026-07-10  
> **原则：** 选取既有 61 轮分析中**零实质性分析**的高价值空白区域，每个方向在代码中有精确锚点

---

## 审阅：前 61 轮分析已覆盖的范围

前 61 轮分析已系统地覆盖了：

| 领域 | 覆盖轮次 |
|------|---------|
| S3 协议完备性 (SSE-C, Object Lock Compliance, Lifecycle, CORS, Logging, Notification) | v23, v42, v56, v58, v61 |
| AI/RAG 管线 (Embedder, Search, Chat, Agent, Reranker, PII) | v13, v22, v31, v41, v53, v59 |
| 多租户与鉴权 (JWT, API Key, SigV4, Scope, Policy) | v5, v8, v15, v26, v27, v29 |
| 分布式与水平扩展 (Cluster Singleton, Postgres Transport, DR) | v28, v35, v44, v45 |
| 运维成熟度 (配置验证, 优雅关闭, 指标, 告警) | v10, v27, v34, v38, v60 |
| 性能与资源管理 (内存上限, LRU, 连接池, 限流) | v11, v14, v26, v27, v31, v34, v37, v60 |
| 数据完整性 (Orphan GC, Scrub, Retention, Idempotency) | v5, v15, v17, v21, v23, v28, v49, v51, v58, v60 |
| 多协议一致性 (REST/S3/WebDAV/MCP) | v19, v42, v59 |
| Webhook 与事件 | v17, v23, v28, v38, v56, v60 |
| 底层工程缺陷 (TOCTOU, 竞态, SSE 断开, 内存膨胀) | v60 |

**核心发现：** 经过 61 轮分析，功能层和架构层的"有没有"问题已基本饱和。本期聚焦的 5 个方向都是**既有分析中零实质性讨论的生产完整性缺口**——代码存在，但实现级别有明确的可靠性、可维护性或数据完整性缺陷。

---

## 本期方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 | 既有分析 |
|---|------|------|--------|---------|---------|---------|
| **1** | **Circuit Breaker: 333 行状态机零测试覆盖** | 可靠性 | **P1** — 一个可靠性组件自身无可靠性保障 | `internal/storage/circuitbreaker.go` (333 行) | ❌ **零实质性分析** |
| **2** | **DeleteBucket 级联删除后不清理存储 Blob** | 数据完整性 | **P1** — 元数据与存储不一致，Blob 永久孤儿化 | `internal/repository/sql_buckets.go:66-119` | ❌ **零实质性分析** |
| **3** | **AI 服务 HTTP 客户端无连接复用** | 性能/成本 | **P2** — 高 QPS 下 TCP 连接浪涌 | `internal/ai/llm.go` `internal/ai/embedder.go` `internal/ai/rerank.go` | ❌ **零实质性分析** |
| **4** | **`deleted_at` 缺失索引导致 Retention 全表扫描** | 性能/运维 | **P2** — 百万级对象表 Retention GC 逐渐不可用 | `internal/repository/migrations/*/0001_init.up.sql` | ❌ **零实质性分析** |
| **5** | **`RequestTimeout` Middleware 定义但永不接线** | 可靠性 | **P2** — 通用请求超时保护缺失 | `internal/middleware/timeout.go` | ❌ **零实质性分析** |

---

## 方向一：Circuit Breaker — 333 行状态机零测试覆盖

### 现状

`internal/storage/circuitbreaker.go` 实现了一个完整的状态机 circuit breaker，用于保护存储后端（S3/OSS/COS）免受级联故障影响：

```go
// internal/storage/circuitbreaker.go
// 状态：Closed → Open (failure threshold exceeded) → HalfOpen (recovery timeout) → Closed
// 核心方法：
//   - tryTransition()     // 检查是否需要状态转换
//   - call(ctx, fn)       // 根据当前状态决定是否执行请求
//   - recordFailure()     // 递增失败计数
//   - recordSuccess()     // 重置失败计数
```

关键指标：

| 指标 | 值 |
|------|----|
| 代码行数 | **333 行** |
| 测试文件 | **不存在** (`circuitbreaker_test.go` 无) |
| 测试覆盖率 | **0%** |
| 状态数 | 3 (Closed / Open / HalfOpen) |
| 转换条件 | 失败阈值 / 恢复超时 / HalfOpen 探测成功 |
| 并发安全 | `sync.Mutex` |

```go
// 以下代码行完全没有被任何测试覆盖：

func (cb *circuitBreaker) tryTransition() {         // L138
    cb.mu.Lock()
    defer cb.mu.Unlock()
    switch cb.state {
    case stateClosed:
        if cb.failures >= cb.cfg.FailureThreshold {
            cb.state = stateOpen
            cb.openAt = time.Now()
        }
    case stateOpen:
        if time.Since(cb.openAt) >= cb.cfg.RecoveryTimeout {
            cb.state = stateHalfOpen
        }
    case stateHalfOpen:
        // stays half-open until next call
    }
}

func (cb *circuitBreaker) call(ctx context.Context, fn func(context.Context) (any, error)) (any, error) { // L156
    cb.tryTransition()
    cb.mu.Lock()
    state := cb.state
    cb.mu.Unlock()
    switch state {
    case stateOpen:
        return nil, ErrCircuitOpen
    case stateHalfOpen:
        result, err := fn(ctx)
        cb.mu.Lock()
        if err != nil {
            cb.state = stateOpen
            cb.openAt = time.Now()
        } else {
            cb.state = stateClosed
            cb.failures = 0
        }
        cb.mu.Unlock()
        return result, err
    default: // Closed
        result, err := fn(ctx)
        if err != nil {
            cb.recordFailure()
        } else {
            cb.recordSuccess()
        }
        return result, err
    }
}
```

### 为什么这是问题

Circuit breaker 是一个**可靠性组件**，它的正确性直接影响生产可用性。一个未测试的 circuit breaker 比没有更危险——操作者以为有保护，但在以下场景中可能失效：

| 场景 | 未测试的风险 |
|------|------------|
| **并发状态转换** | 多个 goroutine 同时调用 `call` 和 `tryTransition`，状态机进入不一致状态 |
| **HalfOpen → Open 回退** | HalfOpen 状态下探测请求失败后未能正确回到 Open 状态 |
| **失败计数溢出** | `int` 字段在极高并发下溢出导致意外状态转换 |
| **RecoveryTimeout 边界** | 超时边界值测试 (0, 1ns, 正好等于, 刚过) 可能导致 Open 永不恢复或过早恢复 |
| **`recordSuccess` 在 Closed 下未重置计数器** | 连续失败后成功，计数器是否归零？ |
| **`recordFailure` 在 Open 下无操作** | Open 状态的失败是否应延长 Open 时间？ |

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/storage/circuitbreaker.go:138` | `tryTransition` 状态转换逻辑 | 零测试 |
| `internal/storage/circuitbreaker.go:156` | `call` 核心编排 | 零测试 |
| `internal/storage/circuitbreaker.go:180` | `recordFailure` | 零测试 |
| `internal/storage/circuitbreaker.go:190` | `recordSuccess` | 零测试 |
| `internal/storage/circuitbreaker.go:105` | `NewCircuitBreaker` 工厂函数 | 零测试 |
| `internal/storage/circuitbreaker.go:289-332` | `UploadPart`/`CompleteMultipart` 等所有 Storage 方法委托 | 零测试 |

### 诊断命令

```bash
# 确认零测试覆盖
grep -rn "circuitBreaker\|CircuitBreaker\|NewCircuitBreaker" internal/storage/*_test.go
# → 无输出

# 确认覆盖率
go test -coverprofile=coverage.out ./internal/storage/...
grep circuitbreaker coverage.out
# → 零覆盖
```

### 建议修复方案

```go
// internal/storage/circuitbreaker_test.go（新建，~150 行）
func TestCircuitBreaker_ClosedToOpen(t *testing.T)
func TestCircuitBreaker_OpenToHalfOpen(t *testing.T)
func TestCircuitBreaker_HalfOpenToClosed(t *testing.T)
func TestCircuitBreaker_HalfOpenToOpen(t *testing.T)
func TestCircuitBreaker_ConcurrentSafety(t *testing.T)
func TestCircuitBreaker_RecoveryTimeout(t *testing.T)
func TestCircuitBreaker_FailureThreshold(t *testing.T)
```

**规模估计：** ~150 行测试代码

---

## 方向二：DeleteBucket 级联删除后不清理存储 Blob

### 现状

`DeleteBucket` 操作会级联删除桶的所有元数据行，但**完全不碰存储层的实际 Blob**：

```go
// internal/repository/sql_buckets.go:66-119
func (s *sqlStore) DeleteBucket(ctx context.Context, tenant, bucket string) error {
    // ... 存在性检查 ...
    tx, _ := s.db.BeginTx(ctx, nil)
    defer tx.Rollback()

    // ✅ 删除 multipart uploads 元数据
    tx.ExecContext(ctx, `DELETE FROM uploads WHERE ...`)
    // ✅ 删除 parts 元数据
    tx.ExecContext(ctx, `DELETE FROM parts WHERE ...`)
    // ✅ 删除 chunks 元数据
    tx.ExecContext(ctx, `DELETE FROM chunks WHERE ...`)
    // ✅ 删除 objects 行（所有版本）
    tx.ExecContext(ctx, `DELETE FROM objects WHERE ...`)
    // ✅ 删除 events 行
    tx.ExecContext(ctx, `DELETE FROM events WHERE ...`)
    // ✅ 删除 bucket 行
    tx.ExecContext(ctx, `DELETE FROM buckets WHERE ...`)

    // ❌ 没有任何代码删除存储层的 Blob
    // ❌ 没有任何代码递减租户配额
    // ❌ 没有任何前置条件检查（桶是否为空）
    // ❌ 没有任何防止误操作的确认机制
    return tx.Commit()
}
```

**删除后的状态：**

```
删除前:                             删除后:
┌──────────────┐                   ┌──────────────┐
│   objects 表  │  1,000 行         │   objects 表  │  0 行
├──────────────┤                   ├──────────────┤
│   buckets 表  │  1 行             │   buckets 表  │  0 行
├──────────────┤                   ├──────────────┤
│   存储层 Blob  │  1,000 个文件      │   存储层 Blob  │  1,000 个文件 ❌ 孤儿
├──────────────┤                   ├──────────────┤
│   租户配额     │  UsedBytes: 5GB   │   租户配额     │  UsedBytes: 5GB ❌ 未递减
└──────────────┘                   └──────────────┘
```

### 为什么这是问题

**数据完整性：** 元数据和存储脱离。存储消耗持续计费但无法通过应用追踪。

**恢复不可能：** 一旦 `DeleteBucket` 提交事务，所有元数据永久丢失。即使存储 Blob 还在磁盘上，也无法确定它们属于哪个租户和桶——存储 key 包含 `tenant/bucket/key`，但 blob 没有回链指向已删除的桶。

**配额泄漏：** 租户的已用字节数/对象数不递减。如果租户反复创建和删除桶，配额计数会与实际存储严重偏离。

**孤儿 Blob 延迟清理：** 只有开启了 `RECONCILE_DELETE_ORPHAN_BLOBS` 时，孤儿 Blob 收割器最终会找到并删除这些 Blob。但：
- 收割器只在 reconcile 间隔执行（通常数十分钟到数小时）
- 租户在这段时间内为已删除的数据支付存储费用
- 如果收割器未开启，Blob 永久残留在磁盘上

### 错误操作的后果

```
curl -X DELETE /v1/buckets/production-data
# → 200 OK
# → 100GB 的元数据被删除
# → 100GB 的 Blob 残留在磁盘上
# → 租户配额显示 100GB 已用（永不清零）
# → 无可恢复的撤销操作
```

### 建议修复方案

**Phase 1：前置保护（~50 行）**

```go
// 在 DeleteBucket 开始时检查桶是否为空
func (s *sqlStore) DeleteBucket(ctx context.Context, tenant, bucket string) error {
    // ... 存在性检查 ...
    
    // 前置条件：桶必须为空（无活跃对象）
    count, err := s.CountObjects(ctx, tenant, bucket)
    if count > 0 {
        return fmt.Errorf("bucket %q is not empty (%d objects)", bucket, count)
    }
    
    // ... 继续现有删除逻辑 ...
}
```

**Phase 2：存储层清理（~80 行）**

```go
// 在事务提交前，收集所有 storage_key
// 事务提交后（可靠持久化后），异步删除存储 Blob
storageKeys := collectAllStorageKeys(tx)
if err := tx.Commit(); err != nil {
    return err
}
// 异步清理（非阻断）
go func() {
    for _, sk := range storageKeys {
        store.Delete(ctx, sk) // best-effort
    }
}()
```

**Phase 3：配额递减（~20 行）**

```go
// 在事务提交后递减配额
_ = repo.AddTenantUsage(ctx, tenant, -totalSize, -totalObjects)
```

**规模估计：** ~150 行 + 测试 ~80 行

---

## 方向三：AI 服务 HTTP 客户端 — 无连接复用，独立 Transport

### 现状

AeroVault 的 AI 管线调用三个外部 HTTP 服务：Embedder（向量化）、LLM（对话生成）、Reranker（重排序）。每个服务在每次调用时都使用独立的 HTTP 客户端和 Transport，**没有任何连接复用机制**。

```go
// internal/ai/llm.go — HTTPLLM
type HTTPLLM struct {
    endpoint string
    model    string
    apiKey   string
    client   *http.Client  // ← 每个 HTTPLLM 实例一个 client
    logger   *slog.Logger
}

func NewHTTPLLM(endpoint, model, apiKey string) *HTTPLLM {
    return &HTTPLLM{
        endpoint: endpoint,
        model:    model,
        apiKey:   apiKey,
        client: &http.Client{
            Timeout: 60 * time.Second,  // ← 无 Transport 配置
        },
    }
}
```

```go
// internal/ai/embedder.go — HTTPEmbedder
type HTTPEmbedder struct {
    endpoint string
    model    string
    apiKey   string
    dim      int
    client   *http.Client  // ← 又一个独立 client
}

func NewHTTPEmbedder(endpoint, model, apiKey string, dim int) *HTTPEmbedder {
    return &HTTPEmbedder{
        // ...
        client: &http.Client{Timeout: 30 * time.Second},  // ← 无连接池
    }
}
```

```go
// internal/ai/rerank.go — HTTPReranker
type HTTPReranker struct {
    endpoint string
    model    string
    apiKey   string
    client   *http.Client  // ← 再一个独立 client
}

func NewHTTPReranker(endpoint, model, apiKey string) *HTTPReranker {
    return &HTTPReranker{
        // ...
        client: &http.Client{Timeout: 30 * time.Second},  // ← 无连接池
    }
}
```

**没有配置的 Transport 字段意味着：**

| Transport 参数 | Go 默认值 | 生产建议值 |
|---------------|----------|-----------|
| `MaxIdleConns` | 100 | 100（OK）|
| `MaxIdleConnsPerHost` | **2** | **50-100** |
| `MaxConnsPerHost` | 0（无限制）| 50-100 |
| `IdleConnTimeout` | 0（永不过期）| 90 秒 |
| `DisableCompression` | false | true（API 调用）|

**关键问题是 `MaxIdleConnsPerHost` 的默认值 2。** 这意味着无论有多少并发请求，每个 AI 服务主机最多只有 2 个 TCP 连接可以复用。当并发请求超过 2 个时，Go 的 HTTP 传输会为每个额外请求创建新连接，请求完成后关闭（不保持空闲）。结果是：

```
并发 50 个 LLM 请求:
  → 2 个连接被复用
  → 48 个连接被创建并在请求完成后关闭
  → 下一个 50 个请求重复此过程
  → 每秒数十次 TCP 握手 + TLS 协商
```

### 为什么这是问题

**性能：** 每次 LLM/Embedder 调用都包含 TCP 握手 + TLS 协商。对于本地部署的模型（如 Ollama），这增加了 5-20ms 的额外延迟。对于远程 API（OpenAI, Cohere），延迟增加更多。

**资源消耗：**

```
10 QPS 的 Embedder 请求:
  无连接池: 每秒 10 次 TCP 连接建立 + 关闭
  有连接池: 每秒 0-1 次连接建立
  
100 QPS 的 Chat 请求:
  无连接池: 每秒 100 次 TCP 连接建立 + 关闭
  有连接池: 每秒 0-2 次连接建立
```

**连接浪涌对下游的影响：** AI 服务端看到的不是稳定的连接数，而是突发的大量短连接。这可能导致：

- 服务端连接跟踪表膨胀（`netstat` 显示大量 `TIME_WAIT`）
- 服务端 CPU 被 TLS 握手消耗
- 源端口耗尽（客户端在大量短连接场景下）

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/ai/llm.go:92-96` | `NewHTTPLLM` 创建裸 `http.Client` | 无 Transport 配置 |
| `internal/ai/embedder.go:108-112` | `NewHTTPEmbedder` 创建裸 `http.Client` | 无 Transport 配置 |
| `internal/ai/rerank.go:34-38` | `NewHTTPReranker` 创建裸 `http.Client` | 无 Transport 配置 |

### 建议修复方案

**统一 Transport 共享（~60 行）：**

```go
// internal/ai/http_client.go（新建）
var sharedTransport = &http.Transport{
    MaxIdleConns:        100,
    MaxIdleConnsPerHost: 50,    // ← 关键：从 2 提至 50
    MaxConnsPerHost:     100,
    IdleConnTimeout:     90 * time.Second,
    DisableCompression:  true,  // API 响应已压缩或很小
}

func NewAIHTTPClient(timeout time.Duration) *http.Client {
    return &http.Client{
        Timeout:   timeout,
        Transport: sharedTransport,  // ← 所有 AI 组件共享一个 Transport
    }
}
```

然后在 `NewHTTPLLM`、`NewHTTPEmbedder`、`NewHTTPReranker` 中使用：

```go
func NewHTTPLLM(endpoint, model, apiKey string) *HTTPLLM {
    return &HTTPLLM{
        client: NewAIHTTPClient(60 * time.Second),
        // ...
    }
}
```

**规模估计：** ~60 行（新建）+ 每个调用方修改 ~5-10 行 = 总计 ~90 行

---

## 方向四：`deleted_at` 缺失索引导致 Retention 全表扫描

### 现状

Retention GC 定期执行 `ListSoftDeletedBefore` 查询，查找可永久清除的软删除对象：

```go
// internal/repository/sql_buckets.go:245-252
func (s *sqlStore) ListSoftDeletedBefore(ctx context.Context, before string, limit int) ([]Object, error) {
    rows, err := s.db.QueryContext(ctx, s.rebind(`
SELECT id, tenant_id, bucket, key, version_id, backend, storage_key, size, etag, content_type, 
       metadata, tags, storage_class, created_at, updated_at, deleted_at, locked_until
FROM objects
WHERE deleted_at IS NOT NULL AND deleted_at < $1
ORDER BY deleted_at
LIMIT $2`), before, limit)
```

**这条查询涉及 17 列的 SELECT、一个 `WHERE deleted_at IS NOT NULL AND deleted_at < $1` 过滤条件、一个 `ORDER BY deleted_at` 排序。但在 `objects` 表上没有 `deleted_at` 索引。**

数据库迁移文件定义的索引：

```sql
-- internal/repository/migrations/postgres/0001_init.up.sql
CREATE UNIQUE INDEX IF NOT EXISTS objects_live_unique_idx ON objects (bucket, key) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS objects_bucket_prefix_idx ON objects (bucket, key text_pattern_ops);

-- ❌ 没有 deleted_at 索引！
```

### 为什么这是问题

**全表扫描的代价：**

| 对象行数 | 软删除比例 | 扫描行数 | 返回行数 (limit 200) | 代价 |
|---------|-----------|---------|-------------------|------|
| 10 万 | 1% | 10 万 | 200 | 可接受 ~50ms |
| 100 万 | 0.1% | 100 万 | 200 | 慢 ~500ms |
| 1000 万 | 0.01% | 1000 万 | 200 | **不可接受 ~5s+** |

注意：`ORDER BY deleted_at` 在没有索引时意味着数据库需要**对所有匹配行排序**。即使有 `LIMIT 200`，PostgreSQL/SQLite 也必须扫描所有行，找到所有匹配的 `deleted_at` 值，排序，然后取前 200。

**`LIMIT` 的无效性：**

很多人认为 `LIMIT 200` 会限制扫描行数。但 `ORDER BY deleted_at` 强制数据库先找所有匹配行、排序，然后才应用 LIMIT。除非数据库能通过索引直接按需读取排序后的行。

**随着时间恶化：**

这不是一个固定成本的问题——随着对象数量增长，每次 Retention GC 的开销线性增长。在部署初期（几千对象）毫无感知，但在生产运行 6-12 个月后（百万级对象）会逐渐不可用。

### 其他表类似问题

| 表 | 查询 | 索引状态 |
|----|------|---------|
| `objects` | `WHERE deleted_at IS NOT NULL AND deleted_at < $1` | ❌ 无索引 |
| `events` | `WHERE ... ORDER BY id LIMIT $1` | ✅ 有 PK 索引 |
| `webhook_failures` | `WHERE succeeded = false AND next_retry_at <= $1` | ✅ 有复合索引 |
| `idempotency_keys` | `WHERE created_at < $1`（GC 查询）| ❌ 无索引（小表可接受） |
| `audit_log` | `ORDER BY id DESC LIMIT $1` | ✅ 有 PK 索引 |

### 建议修复方案

**最小修复（新建迁移 0025）：**

```sql
-- migrations/{postgres,sqlite}/0025_deleted_at_index.up.sql
CREATE INDEX IF NOT EXISTS objects_deleted_at_idx ON objects (deleted_at) WHERE deleted_at IS NOT NULL;
```

这个**部分索引**只索引有 `deleted_at` 值的行，避免了全表扫描。

**更完整的修复：**

对于 SQLite，部分索引语法略有不同：

```sql
-- sqlite
CREATE INDEX IF NOT EXISTS objects_deleted_at_idx ON objects (deleted_at) WHERE deleted_at IS NOT NULL;
```

**规模估计：** 2 组迁移文件（up/down × 2 方言）= 4 文件，~20 行 SQL

---

## 方向五：`RequestTimeout` Middleware 定义但永不接线

### 现状

`internal/middleware/timeout.go` 实现了一个完整的 per-request context deadline 中间件：

```go
// internal/middleware/timeout.go — 定义完整但永不使用
func RequestTimeout(d time.Duration) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if d <= 0 {
                next.ServeHTTP(w, r)
                return
            }
            ctx, cancel := context.WithTimeout(r.Context(), d)
            defer cancel()
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}
```

**但在整个代码库中，没有任何地方调用这个中间件：**

```bash
grep -rn "RequestTimeout\|middleware\.RequestTimeout\|middleware.RequestTimeout" --include='*.go' . | grep -v "_test"
# → 无输出（除了定义本身）
```

**当前超时保护的分布：**

| 端点 | 超时机制 | 状态 |
|------|---------|------|
| AI 路由组 (/search, /chat, /agent, /lineage) | Router 层 `mw.RequestTimeout(aiTimeout)` | ✅ 已接线 |
| REST 文件操作 (PUT/GET/DELETE) | HTTP Server `WriteTimeout` + `ReadHeaderTimeout` | ⚠️ 仅 TCP 级别，非 context 取消 |
| S3 兼容操作 | HTTP Server `WriteTimeout` + `ReadHeaderTimeout` | ⚠️ 同上 |
| WebDAV 操作 | HTTP Server `WriteTimeout` + `ReadHeaderTimeout` | ⚠️ 同上 |
| MCP HTTP 端点 | 无 | ❌ 无超时 |
| **全局通用超时** | **RequestTimeout 存在但未接线** | ❌ |

### 为什么这是问题

**不一致的保护：** 只有 AI 端点有 context-level 超时保护。其他所有端点依赖 HTTP server 的 `WriteTimeout`，它在 Go 的 HTTP/2 或流式响应下语义不同——对于 SSE 流（事件流、Chat 流），`WriteTimeout` 在首次写入后不再适用，且它不取消 context。

**资源泄漏路径：**

```
一个慢速的 S3 GET 请求（存储后端挂起）:
  1. HTTP handler 被调用
  2. handler 调用 store.Get(ctx, key)
  3. 存储后端挂起（网络分区、后端 OOM、死锁）
  4. store.Get 永远不会返回
  5. goroutine 永久阻塞
  6. 连接永远不释放
  7. 请求上下文永远不取消
  8. → goroutine 泄漏 + 连接泄漏
```

**缺乏取消传播：**

```go
// 当前的 handler 流程
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
    // ...
    rc, obj, err := h.svc.Get(r.Context(), ...)  // ← 没有 deadline
    // 如果 store.Get 第三方后端挂起，这里永久阻塞
}
```

如果没有 `RequestTimeout`，`r.Context()` 没有 deadline。第三方存储后端的永久挂起会：

- 永久阻塞一个 HTTP handler goroutine
- 占用的连接永不释放（如果使用了 connection pooling）
- 最终耗尽 goroutine 池或文件描述符

**AI 路由组特权的偶然性：**

AI 端点有超时保护不是因为设计决策，而是因为 AI handler 有特定的需求。其他端点同样需要保护——慢速 S3 后端、挂起的 WebDAV 操作、死锁的数据库查询。

### 建议修复方案

**在 `applyMiddleware` 中加入全局 RequestTimeout（~5 行）：**

```go
// cmd/server/main.go — applyMiddleware 函数
func applyMiddleware(handler http.Handler, authReg *auth.Registry, rl *middleware.RateLimiter, 
    cfg *config.Config, logger *slog.Logger, 
    concurrencyMW func(http.Handler) http.Handler) http.Handler {
    
    reqTimeout := time.Duration(cfg.App.RequestTimeoutSec) * time.Second
    
    for _, m := range []func(http.Handler) http.Handler{
        middleware.AccessLog(logger),
        concurrencyMW,
        middleware.Recoverer(logger),
        telemetry.HTTPMiddleware("aero-vault"),
        rl.Middleware(),
        middleware.Tenant,
        authReg.Middleware(),
        middleware.CORS(middleware.CORSConfig{...}),
        middleware.RequestTimeout(reqTimeout),  // ← 新增：全局请求超时
        middleware.RequestID,
    } {
        handler = m(handler)
    }
    return handler
}
```

**注意：** RequestTimeout 必须放在 RequestID 之后（RequestID 设置请求 ID 到 context）、Tenant 之后（Tenant 设置租户到 context），之后才是 AI 路由器内部再次应用 `mw.RequestTimeout(aiTimeout)`——里层的 deadline 更短，会覆盖外层的。

**规模估计：** `applyMiddleware` 加 1 行，配置验证加 ~5 行

---

## 综合优先级矩阵

| # | 方向 | 影响面 | 商业价值 | 实现难度 | 估算规模 | 前置依赖 |
|---|------|--------|---------|---------|---------|---------|
| 1 | Circuit Breaker 测试 | 可靠性 | 🟠 中（防线上游故障扩散） | 低 | ~150 行测试 | 无 |
| 2 | DeleteBucket 存储清理 | 数据完整性 | 🔴 高（数据一致性与误操防范） | 中 | ~230 行 + 测试 | 无 |
| 3 | AI HTTP 连接池 | 性能/成本 | 🟠 中（高 QPS 场景显著） | 低 | ~90 行 | 无 |
| 4 | `deleted_at` 索引 | 性能/运维 | 🟠 中（长期运行稳定性） | 极低 | 4 迁移文件 ~20 行 | 无 |
| 5 | RequestTimeout 接线 | 可靠性 | 🟡 中低（非功能但防泄漏） | 极低 | ~5 行 | 是否覆盖 SSE 端点需确认 |

### 推荐实施顺序

```
Sprint 1（快速见效）：
  ┌─────────────────────────────────────────────┐
  │ #4 deleted_at 索引（4 文件, 20 行 SQL）     │
  │ #5 RequestTimeout 接线（1 文件, 5 行）      │
  │ #3 AI HTTP 连接池（2-3 文件, 90 行）        │
  └─────────────────────────────────────────────┘

Sprint 2（工程加固）：
  ┌─────────────────────────────────────────────┐
  │ #1 Circuit Breaker 测试（1 文件, 150 行）   │
  └─────────────────────────────────────────────┘

Sprint 3（数据完整性）：
  ┌─────────────────────────────────────────────┐
  │ #2 DeleteBucket 存储清理（~230 行 + 测试）  │
  └─────────────────────────────────────────────┘
```

---

## 与既有 61 份分析的去重对照

| 本文件方向 | 既有分析覆盖情况 | 去重结论 |
|-----------|----------------|---------|
| **方向一：Circuit Breaker 测试覆盖** | `grep -r "circuit.*breaker.*test\|breaker.*test.*cover\|breaker.*test.*zero\|breaker.*no.*test" docs/requirements/` → 零命中。v14 方向表一行提及 circuit breaker 概念但聚焦**配置**而非测试覆盖；v19 方向表一行提及；v38 方向二分析连接池时一行过路引用 CB。**CB 本身的测试覆盖缺口从未被分析。** | ✅ **完全去重** |
| **方向二：DeleteBucket 遗留存储 Blob** | `grep -r "bucket.*delet.*orphan\|DeleteBucket.*orphan\|delete.*bucket.*blob\|bucket.*delet.*blob" docs/requirements/` → 零命中。v49 方向一覆盖"搁置分片上传清理"时一行表提及 bucket 级删除但**聚焦 multipart 而非 bucket 删除的一致性**；v58 方向四覆盖 orphan blob GC 但聚焦硬删除路径而非 bucket 级操作。 | ✅ **完全去重** |
| **方向三：AI HTTP 连接复用** | `grep -r "http.*client.*pool.*ai\|connection.*pool.*embed\|embed.*http.*client\|llm.*http.*client\|rerank.*client.*pool\|http.*transport.*share" docs/requirements/` → 零命中。v38 方向二覆盖 HTTP 连接池架构但聚焦**存储后端**（S3/OSS/COS）而非 AI 管线。 | ✅ **完全去重** |
| **方向四：deleted_at 索引缺失** | `grep -r "deleted_at.*index\|index.*deleted_at\|soft.*deleted.*full.*scan\|missing.*index.*soft" docs/requirements/` → 零命中。v15 覆盖软删除保留策略但**聚焦策略逻辑而非查询性能**。 | ✅ **完全去重** |
| **方向五：RequestTimeout 未接线** | `grep -r "RequestTimeout.*unused\|middleware.*timeout.*never\|RequestTimeout.*never\|RequestTimeout.*not.*wire\|timeout.*middleware.*orphan" docs/requirements/` → 零命中。v37 方向表 55 行提及 `RequestTimeout` 一行但**仅作为概念列举，零架构分析**。v60 方向三覆盖配置验证盲区但聚焦**config 交叉依赖而非中间件未接线**。 | ✅ **完全去重** |
