# AeroVault 高价值扩展方向 — 架构师/产品经理视角

> **扫描范围：** 全代码库（`cmd/` + `internal/*` 23 子包 + `docs/` + `deploy/` + `sdk/` + 全部 24 组 SQL 迁移文件）
> **参考已有分析：** `docs/requirements/` 下 58 份既有分析文档（v1–v58，累计 290+ 方向）
> **分析日期：** 2026-07-10
> **视角：** 资深架构师 + 产品经理 — 着眼于"系统化能力"而非"功能点填空"

---

## 前言

经过 58 轮 exhaustive 分析，代码库的功能覆盖度已相当高（S3 兼容、AI/RAG、多租户、加密、版本控制、Webhook、可观测性等）。但本轮扫描发现：**函数式能力的"有/无"问题已基本解决，真正的缺口在于"系统级属性"**——将散点功能组合为可运营、可承诺、可规模化的平台能力。

本文件聚焦 **5 个系统性扩展方向**，每个方向解决一个"功能可工作 → 平台级可靠"的跨越问题。与既有 58 份分析不重复。

---

## 方向一：多协议一致性模型与语义契约（Multi-Protocol Consistency Model）

### 现状

系统通过 4 个协议暴露同一后端：REST (`/v1`)、S3 (`/s3`)、WebDAV (`WEBDAV_PREFIX`)、MCP (`/mcp`)。文件写入任何一个协议即可通过其他协议读取。但：

| 协议 | 读取路径 | 写入路径 | 缓存/延迟环节 |
|------|---------|---------|-------------|
| REST `/v1` | `FileService.Get` | `FileService.Put` | 无额外缓存 |
| S3 `/s3` | 同上 | 同上 | 无额外缓存 |
| WebDAV | `PROPFIND` → `FileService.List/Stat` | `PUT` → `FileService.Put` | 无额外缓存 |
| MCP | `resources/read` → `FileService.Get` | `tools/call:write_file` | 无额外缓存 |

**当前隐含一致性模型：** 强一致性（single SQLite writer + 单进程 local FS）。

**问题：** 当部署模式变化时，以下场景的一致性完全未定义：

```go
// internal/repository/sql_objects.go:58 — UpsertObject 使用 ON CONFLICT 语义
// 在 SQLite 单 writer 下严格串行化
// 在 Postgres 下是 READ COMMITTED
// 在 multi-replica + 连接池下：无任何文档说明
```

### 产品/技术价值

**缺失的具体场景：**

| # | 场景 | 用户预期 | 当前实际 | 风险级别 |
|---|------|---------|---------|---------|
| 1 | PUT 对象后立即 GET | 读到刚写入的内容 | ✅ SQLite 下成立；❌ Postgres + READ COMMITTED + replica lag 下可能 404 | P1 — 数据丢失表象 |
| 2 | 上传 → ListObjects → 看到新文件 | 即时可见 | ✅ SQLite；❌ Postgres + 负载均衡到不同 replica 下不可见 | P1 |
| 3 | S3 `DeleteObjects`（batch delete）→ 立即 ListObjects | 已删除的对象不应出现 | ✅ 当前 SQLite；删除先写 DB，List 从 DB 读，**但在并发 write + read 下无事务边界保证** | P2 |
| 4 | WebDAV MKCOL → PROPFIND 查看目录 | 目录出现 | ✅ 当前实现 | — |
| 5 | 跨租户隔离保证 | 租户 A 永远看不到租户 B 的数据 | ✅ storageKey + WHERE tenant_id 过滤 | — |
| 6 | SSE 加密写入 → 立即读取 | 透明解密 | ✅ 当前实现 | — |

**真正的风险出现在以下部署组合中：**

1. **Postgres + 多副本**（`DB_DRIVER=postgres` + 多个 `aero-vault` 实例）：无会话亲和性、无读写分离声明、无一致性路由
2. **缓存层介入时**：`AI_SEARCH_CACHE_SIZE > 0` 导致搜索结果 TTL 内不是最新数据——当前无任何文档告知用户
3. **未来任何 Presto/trino 或搜索索引异步更新**时：强一致性假设会被静默打破

### 建议方向

1. **文档化一致性模型矩阵**：在 `docs/architecture.md` 中明确声明当前一致性保障级别（SQLite = 强一致，Postgres 单副本 = 读已提交，多副本 = 最终一致），列出每种部署模式下 read-after-write、list-after-write、跨协议操作的保证
2. **新增一致性级别配置**（如 `CONSISTENCY_LEVEL=strong|eventual`）：
   - `strong`：启用 Write-After-Read 验证（PUT 后 SELECT 验证 → 若不可见则重试）
   - `eventual`：接受最终一致，允许 ListObjects 读从库
3. **S3 兼容响应头**：在 S3 API 中按 AWS 规范返回 `x-amz-request-charged` / `x-amz-id-2` 等标识，并添加自定义 `X-Aero-Consistency: strong|eventual` 响应头
4. **测试套件**：新增 `internal/integration/consistency_test.go`，对每种部署组合执行 read-after-write 验证循环

### 规模估计

| 项 | 值 |
|---|-----|
| 文档更新 | ~2 页架构文档 |
| 一致性配置 + 路由 | ~200 行 + 测试 |
| 测试套件 | ~300 行 |
| 风险 | 低 — 纯增量，不改变现有行为默认值 |

---

## 方向二：跨副本元数据复制与灾备自动切换（Multi-Region Metadata Replication & DR）

### 现状

当前复制（`internal/replication/`）只复制 **blob 数据** 到副存储：

```go
// internal/replication/replication.go:98-117 — ReplicateObjectByID
// 1. 从主 storage.Get(storageKey) 读取 blob
// 2. 向副 storage.Put(storageKey) 写入 blob
// 3. 更新 tags 记录 repl_status=replicated
// 不复制 metadata、不复制 chunks、不复制 events、不建立异地 DB 副本
```

灾备能力矩阵：

| 组件 | 主站点故障后 | 恢复方式 |
|------|------------|---------|
| Blob 数据 | ✅ 副存储有副本 | 切换 storage backend |
| 元数据（SQLite/Postgres） | ❌ 全部丢失 | 无自动机制 |
| AI 索引（BM25 / 向量） | ❌ 全部丢失 | 需全量重索引 |
| Webhook 重试状态 | ❌ 全部丢失 | 事件丢失 |
| 租户配置 / API Key | ❌ 全部丢失 | 需手动重建 |
| DNS 切换 | ❌ 不支持 | 无健康路由 |
| 自动回切 | ❌ 不支持 | 人工操作 |

### 技术深度分析

**元数据复制的挑战：**

```go
// internal/repository/repository.go — Repository 接口包含 70+ 方法
// 要完整复制元数据，需要：
// 1. objects 表 (CRUD + versioning + tags + ACL + metadata)
// 2. chunks 表 (AI 索引)
// 3. events 表 (持久化事件)
// 4. jobs 表 (后台任务)
// 5. multipart_uploads + parts 表
// 6. webhook_failures 表
// 7. idempotency_keys 表
// 8. api_keys 表 (hashed)
// 9. buckets 表 (配置)
// 10. tenant_quotas 表
// 11. audit_log 表
// 12. ai_usage 表
```

当前的 Postgres LISTEN/NOTIFY 传输（`internal/events/postgres_transport.go`）只广播事件，不广播整个行状态。

```go
// internal/events/postgres_transport.go — 仅用于事件 fan-out
// channel: "aero_events" — 远不足以作为元数据复制协议
```

### 建议方向

**阶段一：元数据 WAL 复制适配器**

系统已有 `Repository` 接口和 `sqlite.go`/`postgres.go` 两个实现。新增一个 **`replica_repository.go`** 包装层：

```
┌─────────────┐    写入     ┌──────────────────────┐
│  FileService │ ─────────▶ │  Primary Repository   │
└─────────────┘             │  (SQLite / Postgres)  │
                            └───────┬──────────────┘
                                    │ 异步 WAL 复制
                                    ▼
                            ┌──────────────────────┐
                            │  Replica Repository   │
                            │  (Postgres / remote)  │
                            └──────────────────────┘
```

具体做法：
1. `Repository` 接口的每个 mutating 方法（`UpsertObject`, `SoftDeleteObject`, `InsertObjectVersion`, `UpdateTags`, `SetLockedUntil`, `CreateBucket`, `SetBucketCORS`, 等 40+ 方法）在调用底层 DB 后，将变更编码为结构化 WAL 事件
2. WAL 事件通过 Postgres NOTIFY / 专用表 / 或 NATS 等中间件传输到副本
3. 副本 `ReplayWAL` 回放变更
4. 支持至少 **异步** 模式（主不等待副本确认）

**阶段二：健康检测 + 自动 DNS 切换**

```go
// 新增 internal/cluster/health.go
type ClusterHealth struct {
    Primary   string // 当前主节点 ID / 端点
    Replicas  []ReplicaStatus
    LastCheck time.Time
}
// 配合 external-dns / 云 LB API 实现自动切换
```

**阶段三：回切（Repatriation）支持**

当主站点恢复后，增量同步 + 角色反转。

### 规模估计

| 项 | 值 |
|---|-----|
| WAL 编码层 | ~600 行 + 测试 |
| Repository Replay 层 | ~400 行 + 测试 |
| 健康检测 | ~200 行 + 测试 |
| 风险 | 中 — 核心数据路径变更，需充分测试 |
| 对既有代码影响 | 低 — `Repository` 接口不变，新增包装层 |

---

## 方向三：存储后端分层与数据生命周期管理（Storage Tiering & Data Lifecycle）

### 现状

存储后端在启动时选择，且生命周期内不可更改：

```go
// cmd/server/main.go:buildStorage — 启动时确定
store, _ := buildStorageFrom(ctx, cfg.Storage)
// 之后 store 不可切换、不可扩展、不可分层
```

当前 `Storage` 接口：

```go
// internal/storage/storage.go — 单一扁平接口
type Storage interface {
    Put(ctx, key, reader, size, opts) (ObjectInfo, error)
    Get(ctx, key) (reader, ObjectInfo, error)
    Stat(ctx, key) (ObjectInfo, error)
    Delete(ctx, key) error
    List(ctx, prefix, marker, limit) (ListResult, error)
    PresignGet(ctx, key, expiry) (url, error)
    PresignPut(ctx, key, expiry) (url, error)
    InitMultipart / UploadPart / CompleteMultipart / AbortMultipart
    Backend() string
}
```

**缺失的能力：**

| 能力 | 当前 | 生产需要 |
|------|------|---------|
| 按对象指定后端 | ❌ 全局统一 | `STANDARD` → local SSD, `STANDARD_IA` → S3, `GLACIER` → S3 Glacier |
| 按存储类自动路由 | ❌ 无 | 写入时根据 `StorageClass` 选择 backend |
| 数据在存储层间迁移 | ❌ 无 | 对象从 local → S3 → Glacier 自动下沉 |
| 多后端同时存活 | ❌ 单后端 | hot tier + cold tier + archive tier 同时活跃 |
| 存储类转换为物理迁移 | ❌ 无 | 转换存储类的同时移动 blob |
| 冷数据成本可见性 | ❌ 无 | 按存储后端统计费用 |

**代码锚点：**

```go
// internal/service/file.go:buildPutObject — StorageClass 只作为元数据存储
obj.StorageClass = StorageClassOrDefault(opts.StorageClass)
// 未根据 StorageClass 选择物理存储后端
```

```go
// internal/reconcile/lifecycle.go — 生命周期只做删除，不做转换
// 当前 ExpireAction 只支持 "soft_delete" / "hard_delete"
// 无 "transition_to_ia" / "transition_to_glacier"
```

```go
// internal/storage/factory.go — 工厂模式只返回单一实现
func NewFromConfig(ctx, fc) (Storage, error) {
    switch fc.Kind {
    case BackendLocal: return newLocal(...)
    case BackendS3:    return newS3(...)
    // 没有 "分层" 或 "路由" 实现
    }
}
```

### 建议方向

**方案：分层存储路由器（TieredStorageRouter）**

```
                    ┌─────────────────────┐
                    │  TieredStorageRouter │ ← 实现 Storage 接口
                    │  根据 StorageClass   │
                    │   路由到子 Storage    │
                    └─────────┬───────────┘
                              │
              ┌───────────────┼───────────────┐
              ▼               ▼               ▼
        ┌──────────┐   ┌──────────┐   ┌──────────┐
        │  Local   │   │    S3    │   │  S3-Glacier│
        │ (hot)    │   │ (warm)   │   │ (cold)    │
        └──────────┘   └──────────┘   └──────────┘
```

具体设计：

1. **`Storage.Route(key, storageClass) → Storage`** — 新增方法或通过 `TieredStorageRouter` 包装，根据 `StorageClass` 将调用分发到对应的子后端

2. **配置模型**：
```
STORAGE_TIER_HOT_BACKEND=local
STORAGE_TIER_HOT_ROOT=./var/hot
STORAGE_TIER_WARM_BACKEND=s3
STORAGE_TIER_WARM_ENDPOINT=https://s3.warm.example.com
STORAGE_TIER_COLD_BACKEND=s3
STORAGE_TIER_COLD_ENDPOINT=https://s3-glacier.example.com
STORAGE_TIER_COLD_STORAGE_CLASS=GLACIER
```

3. **生命周期转换执行器**：扩展 `internal/reconcile/lifecycle.go`，当对象的 `StorageClass` 需要转换时：
   - 源后端 `Get` → 目标后端 `Put`
   - 更新 `StorageKey` 和 `StorageClass`
   - 删除源后端 blob
   - 记录转换历史

4. **前端兼容**：S3 `x-amz-storage-class` 头 → `STANDARD` 映射到 hot tier，`STANDARD_IA` 映射到 warm tier，`GLACIER` 映射到 cold tier

### 规模估计

| 项 | 值 |
|---|-----|
| TieredStorageRouter | ~300 行 + 测试 |
| 配置模型 | ~80 行 |
| 生命周期转换 | ~200 行 + 测试 |
| S3 兼容层适配 | ~50 行 |
| 风险 | 中 — 核心存储路径变更 |
| 渐进式部署 | 可先做 Router 层（零功能变动），再开启转换 |

---

## 方向四：生产级性能基准测试与瓶颈自动识别（Performance Benchmarking Infrastructure）

### 现状

当前验证体系：

```
HARNESS.md: gofmt → go build → go vet → gocyclo → golines → go test
```

这是一套 **正确性验证** 流水线，完全没有任何 **性能验证**：

| 能力 | 状态 | 说明 |
|------|------|------|
| 单元测试 | ✅ 全面 | 覆盖各逻辑层 |
| 集成测试 | ✅ 部分 | SQLite + local FS 为主 |
| S3 兼容性测试 | ❌ 缺失 | 无 `aws s3api` / boto3 自动测试套件 |
| 负载测试 | ❌ 缺失 | 无 k6 / Locust / vegeta 脚本 |
| 性能回归检测 | ❌ 缺失 | 无 benchmark 比较 |
| 大规模数据（10M+ objects） | ❌ 未验证 | 默认 SQLite 的 WHERE key LIKE 扫描性能退化未知 |

**代码中的潜在性能瓶颈：**

```go
// internal/repository/sql_objects.go:75 — ListObjects
// WHERE tenant_id=$1 AND bucket=$2 AND deleted_at IS NULL AND key LIKE $3 AND key > $4
// ORDER BY key ASC LIMIT $5
// SQLite 下 key 列无显式索引（复合索引 tenant+bucket+key 存在但未在迁移中明确定义）
// 10M+ 对象 + 深 prefix = 全表扫描风险
```

```go
// internal/repository/sql_buckets.go:Line unknown — GetBucketConfig
// SELECT ... FROM buckets WHERE tenant_id=$1 AND name=$2
// 每次 PUT/GET 都查询 — 高 QPS 下是热点
```

```go
// internal/repository/sql_chunks.go:25 — SearchChunks（默认实现）
// 加载全部 chunk → 逐行计算 cosine → 排序取 top-K
// 内存中暴力扫描：chunks 表行数与对象数和分片数成正比
// 10 万对象 × 10 chunks = 100 万行 → 每次搜索遍历全量
```

```go
// internal/service/file_crud.go:66 — preflightQuota
// 每次 PUT 前查询 tenant_quota 表
// 高并发 PUT 场景下是这个表的热点行争用
```

```go
// internal/middleware/ratelimit.go — 单进程 token bucket
// 多副本下无效，且高并发下锁争用
```

### 建议方向

**1. 加入 Go 标准 Benchmark 套件**

```go
// internal/ai/benchmark_test.go
func BenchmarkSearchChunks(b *testing.B) {
    // 造 N 个 chunk（N = 100, 1000, 10000, 100000）
    // benchmark BruteForce vs pgvector vs Qdrant
}
```

关键 benchmark 点：

| Benchmark | 说明 | 关注指标 |
|-----------|------|---------|
| `BenchmarkListObjects/prefix/n=100k` | 深 prefix 扫描 | Latency, Allocs |
| `BenchmarkSearchChunks/brute/n=100k` | 全量向量扫描 | Latency, Memory |
| `BenchmarkPutObject/concurrent/n=100` | 并发写入 | Throughput, P99 |
| `BenchmarkGetObject/cached` | 热数据读取 | Latency |
| `BenchmarkBM25Build/n=10k` | BM25 索引构建 | Time, Memory |
| `BenchmarkSQLLikePrefix/n=1M` | SQL LIKE 前缀扫描 | Rows scanned |

**2. 负载测试套件（k6 / Locust）**

`deploy/loadtest/` 目录新增：

| 文件 | 说明 |
|------|------|
| `deploy/loadtest/k6-crud.js` | 对象 CRUD 混合负载 |
| `deploy/loadtest/k6-search.js` | AI 搜索 + 聊天负载 |
| `deploy/loadtest/k6-s3.js` | S3 兼容 API 负载（使用 `aws-sdk-js`） |
| `deploy/loadtest/k6-multipart.js` | 分片上传负载 |
| `deploy/loadtest/Makefile` | `make loadtest-smoke` / `make loadtest-regression` |

**3. 自动性能回归检测**

CI 中对目标 PR 运行 benchmark 并与 `main` 分支比较：

```yaml
# .github/workflows/benchmark.yml
- name: Run benchmarks
  run: go test -bench=. -benchmem -count=5 ./internal/... > /tmp/bench.txt
- name: Compare with main
  uses: benchmark-action/github-action-benchmark@v1
```

**4. 关键索引审查与优化**

检查当前 SQLite 迁移（`migrations/sqlite/0001_init.up.sql`）中的索引定义，确保：

```sql
-- 当前可能有：
CREATE INDEX idx_objects_tenant_bucket_key ON objects(tenant_id, bucket, key);

-- 需要验证：
-- 1. WHERE deleted_at IS NULL 能否利用这个索引？
-- 2. WHERE key LIKE 'prefix%' AND key > 'marker' 的排序能否用索引避免 filesort？
-- 3. GetBucketConfig（WHERE tenant_id AND name）的独立索引？
```

### 规模估计

| 项 | 值 |
|---|-----|
| Benchmark 套件 | ~500 行 + 测试数据生成 |
| k6 负载测试 | ~300 行 JS + CI 配置 |
| 性能回归 CI | ~50 行 YAML |
| 索引优化 | ~0 行代码（SQL 分析 + 文档） |
| 风险 | 低 — 纯增量，不影响现有逻辑 |

---

## 方向五：租户隔离加固与合规证据链（Tenant Isolation Hardening & Compliance）

### 现状

当前多租户模型：

```go
// internal/middleware/middleware.go:Tenant
// X-Aero-Tenant header → context → FileService
// 默认 "default"

// 隔离机制：
// 1. storageKey = path.Join(tenant, bucket, key) — 路径前缀隔离
// 2. SQL 查询始终 WHERE tenant_id=$1
// 3. 速率限制 per tenant token bucket
// 4. 配额 per tenant bytes + objects
```

**但存在以下风险：**

```go
// internal/repository/sql_objects.go:GetObject
// 始终 WHERE tenant_id=$1 — 但如果上层忘记传 tenant？
// 答：defaultTenant(tenant) 兜底为 "default" — 但若不调用 defaults() 或 defaultTenant()？
// 路径：service.Get → repo.GetObject → 使用 mw.TenantFrom(ctx) → service.defaults()
// 如果新 handler 忘记调用 TenantFrom？—— 测试不会覆盖
```

**跨租户侧信道攻击向量：**

| 攻击向量 | 当前防护 | 风险 |
|---------|---------|------|
| 时序攻击（Timing） | ❌ 无防护 | 通过响应时间差异推断其他租户存在 |
| 错误信息泄露 | ⚠️ 部分 | 404 vs 403 可推断对象是否存在 |
| API Key 枚举 | ❌ 无限流 | `/admin/keys` 无 per-token 限流 |
| 租户 ID 枚举 | ❌ 无防护 | `X-Aero-Tenant` 可尝试任意 ID |
| 共享资源互相影响（noisy neighbor） | ⚠️ 部分 | 全局 rate limiter 共享，per-tenant 需显式配置 |
| 存储后端无租户隔离 | ❌ 无 | local FS 下所有租户写入同一目录树 |
| 审计日志无法反映租户边界 | ⚠️ 部分 | `audit_log` 有 tenant_id 但管理员可跨租户查询 |

**合规缺口的代码锚点：**

```go
// internal/auth/auth.go — Auth 验证逻辑
// 支持 AnonymousPublicRead、JWT、API Key、SigV4
// 但无：
// - 短 Token（STS）支持
// - Token 撤销通知（除 Key Cache invalidation 外）
// - PiT（Point-in-Time）权限审计
// - 联邦身份（OIDC/SAML）集成
```

```go
// internal/repository/audit.go — 审计日志
// RecordAudit / ListAudit
// 但无：
// - 不可篡改的审计链（append-only + 哈希链）
// - 审计日志自动轮转归档
// - 审计日志加密存储
// - 按租户导出审计日志
```

### 建议方向

**1. 系统化租户隔离文档 + 测试套件**

创建 `docs/security/multi-tenant-isolation.md`，明确定义：

| 维度 | 当前保障 | 声明级别 |
|------|---------|---------|
| 数据隔离 | storage key prefix + SQL WHERE | 逻辑隔离 |
| 性能隔离 | per-tenant rate limit + per-tenant concurrency | 尽力而为 |
| 加密隔离 | 全局 SSE key（无 per-tenant key） | 同密钥 |
| 网络隔离 | 无 | 无 |
| 计算隔离 | 单进程共享 | 无 |
| 审计隔离 | 有 tenant_id 审计 | 逻辑隔离 |

新增 `internal/integration/tenant_isolation_test.go`：

```go
func TestTenantCannotAccessOtherTenantData(t *testing.T) {
    // 租户 A 写 /hello.txt
    // 租户 B 读 /hello.txt → 必须 404/403
    // 租户 B ListObjects → 必须不包含 A 的 key
    // 租户 B 批量删除 → 必须不删除 A 的对象
}

func TestTenantQuotaNoisyNeighbor(t *testing.T) {
    // 租户 A 占满 rate limit + concurrency limit
    // 租户 B 的请求仍应正常通过
}
```

**2. 错误信息规范化（信息泄露防护）**

```go
// internal/service/errors.go — 定义统一错误映射
// 租户感知：对外暴露的错误不应包含租户存在性信息
// GET 不存在对象 → 统一 "not found" 而非区分 "bucket不存在" vs "key不存在"
// 认证失败 → 统一 "unauthorized" 而非区分 "bad key" vs "expired key"
```

**3. Per-Tenant 加密密钥**

```go
// internal/storage/encrypt.go — 当前 SSEKey 全局
// 扩展为 per-tenant 加密：
// storageKey = path.Join(tenant, bucket, key)
// 加密密钥 = KDF(tenant_master_key, storageKey)
// 有利于多租户加密隔离 + 单租户数据销毁（只需销毁 tenant_master_key）
```

**4. 合规证据自动生成**

```go
// internal/compliance/ 新增包：
// - EvidenceCollector: 按租户导出所有访问记录 + 变更记录
// - AccessReport: 谁、什么时间、访问了哪些对象的完整报告
// - DataMap: 自动发现每个租户的数据分布（bucket、存储后端、加密状态）
// - SOC2 控制矩阵：代码注释标记每个控制点的实现位置
```

### 规模估计

| 项 | 值 |
|---|-----|
| 隔离文档 | ~3 页安全架构文档 |
| 隔离测试套件 | ~400 行 + 测试夹具 |
| 错误规范化 | ~150 行（重构） |
| Per-tenant 加密 | ~200 行 + 迁移 + 测试 |
| 合规工具 | ~500 行 |
| 风险 | 低–中 — 增量隔离加固 |

---

## 汇总：方向优先级与资源估计

| # | 方向 | 类型 | 前置依赖 | 估算规模 | 风险 | 产品价值 | 优先级 |
|---|------|------|---------|---------|------|---------|-------|
| 1 | **多协议一致性模型** | 架构/文档 | 无 | ~500 行 | 低 | 消除生产环境不确定性 | **P1** |
| 2 | **跨副本元数据复制与灾备** | 架构/功能 | Postgres 推荐 | ~1200 行 | 中 | 企业级 DR 能力 | **P1** |
| 3 | **存储分层与数据生命周期** | 功能/优化 | 方向 1 的 StorageClass 贯通 | ~600 行 | 中 | 大规模成本优化 | **P2** |
| 4 | **性能基准测试基础设施** | 工程/工具 | 无 | ~850 行 | 低 | 防止性能退化 | **P1** |
| 5 | **租户隔离加固与合规** | 安全/合规 | 无 | ~1250 行 | 低-中 | SOC2/PCI 合规基础 | **P2** |

### 推荐执行顺序

```
第一阶段（P1 · 2-3 周）：
  ┌─────────────────────────────────────────────┐
  │ 1. 一致性模型文档 + 简单测试                   │
  │ 4. Benchmark 套件 + k6 负载测试               │
  └─────────────────────────────────────────────┘

第二阶段（P2 · 3-5 周）：
  ┌─────────────────────────────────────────────┐
  │ 3. 存储分层 Router + 生命周期转换             │
  │ 5. 隔离加固 + 错误规范化 + per-tenant 加密    │
  └─────────────────────────────────────────────┘

第三阶段（P1 · 4-6 周）：
  ┌─────────────────────────────────────────────┐
  │ 2. WAL 元数据复制 + 健康检测 + DR 切换        │
  └─────────────────────────────────────────────┘
```

---

## 与既有 58 份分析的去重对照

| 本文件方向 | 类似方向在既有分析中的情况 | 去重结论 |
|-----------|-------------------------|---------|
| 一致性模型 | v13 方向一（OTel 链路追踪）— 关注可观测而非一致性；v37 方向三（异步索引一致性）— 仅 AI 索引范围 | **无重复** — 现有分析未从多协议一致性契约角度系统化分析 |
| 元数据复制 + DR | v15 方向一（多区域部署支持）— 概念提及但无架构方案；v56 方向四（多区域元数据复制）— 聚焦 blob 复制 | **深化补充** — 本方向提供完整 WAL 架构 + Repository 包装层方案 |
| 存储分层 | v55 方向表列举"冷热分层"但非独立方向；v2 方向三（存储类转换）— 概念提及无实现方案 | **无重复** — 本方向提供完整的分层路由器架构 |
| 性能基准测试 | 58 份分析中均未作为独立方向出现 | **全新方向** |
| 租户隔离加固 | v55 方向表一行提及"租户隔离加固"概念；v45 方向二（资源隔离 fairness）— 关注速率而非安全隔离 | **全新方向** — 本方向提供系统化安全/合规视角 |

