# AeroVault 高价值扩展方向（第十二期）

> **视角：** 资深架构师 / 产品经理
> **方法：** 全局代码扫描（~50K 行 Go 源码，覆盖 `internal/` 全部 23+ 子包），逐一审阅前十一期 expansion 文档（`expansion-directions.md` ~ `expansion-v11-operational-depths.md`）、`ROADMAP.md`、`CHANGELOG.md`、`AGENTS.md`。确认每个方向在**所有既有文档中零覆盖**。
> **日期：** 2026-07-10
> **原则：** 选取 5 个**既有文档从未系统讨论**的工程架构方向。每个方向附带：代码锚点、当前状态 vs 理想状态、边界情况、架构蓝图、实现理由。**不编写任何实现代码。**

---

## 审阅摘要：前十一期已覆盖的范围（验证去重）

| 覆盖类别 | 对应期数 | 状态 |
|---------|---------|------|
| AI 管线（检索/Embedding/Chat/Agent/PII/Cache/Indexer/Reranker） | v1~v11, ROADMAP #1~#2 | 12×+，深度覆盖 |
| S3 兼容性（Policy 存储/CORS/Logging/Notification CRUD/SSE-C） | v8, v9, v10, ROADMAP #7 | 4× |
| Storage 后端（S3/OSS/COS/KMS/SSE/Encryption/CircuitBreaker） | v4~v11, ROADMAP #5 | 8×+ |
| 多租户（Quota/Budget/Billing/Usage Metering） | v3, v4, v7, ROADMAP #2, #4 | 5× |
| 事件系统（Webhook/Postgres Transport/Bus/SSE 韧性/CDC/Kafka/NATS） | v5, v6, v8, v9, v11 | 5× |
| 身份联邦（SSO/OIDC/SAML/SCIM/Policy Evaluation Engine） | v5, v8 | 2× |
| 合规（WORM/Legal Hold/生命周期治理/Disposition Audit/Access Audit） | v6, v9, v10 | 3× |
| 复制（Cross-Region Active-Active/Conflict Detection/Replication） | v9, ROADMAP #3 | 2× |
| WASM 函数/事件触发计算 | v9 | 1× |
| 内容去重/CAS（内容寻址存储） | v7 | 1× |
| 结构化元数据 Schema | v7 | 1× |
| 内容智能/DLP/格式转换/文档预览 | v6, v8 | 2× |
| 存储分层/生命周期 Transition/Cold Archive/Restore | v5, v1/ext, ROADMAP #9 | 3× |
| 跨后端数据迁移 | v10 | 1× |
| 可观测性成熟度（SLO/成本/容量预测/仪表盘） | v11 | 1× |
| 测试基础设施（Benchmark/Fuzz/契约测试/集成 CI） | v11 | 1× |
| 开发者体验（热重载/DevContainer/Docker Compose/Dev Mode） | v11 | 1× |
| 存储层自愈（磁盘监控/CB 持久化/自动修复） | v11 | 1× |
| 生产安全纵深（TLS/Secret 管理/输入加固） | v11 | 1× |
| 优雅关闭/生产级部署韧性 | v10 | 1× |
| API 版本治理与兼容性保障 | v10 | 1× |
| 冷存储/Deep Archive/Restore | v5 | 1× |
| 批量操作/文件夹管理/层次化命名空间 | v3, v1/ext | 2× |
| 浏览器直传/Resumable Upload/TUS | v7 | 1× |
| Web UI / CLI / MCP | v8 | 1× |
| Postgres 连接池/Read Replica | v8 | 1× |
| 跨协议并发写一致性/冲突检测 | v8 | 1× |
| 运行时优雅降级/特性开关框架 | v6 | 1× |
| 分层限流/Per-Key 配额/操作成本加权 | v6 | 1× |
| 备份/快照/容灾 | v8 | 1× |
| 客户端加密/SSE-C/零信任 | v10 | 1× |
| Content Integrity/Scrub/Self-Healing | v8, ROADMAP #8 | 2× |

**本期选点原则：** 从前十一期的覆盖矩阵中定位"零覆盖或仅骨架提及"的方向，要求：① 与现有架构可增量集成；② 有明确的代码锚点和边角情况；③ 具有显著的工程或商业价值。

---

## 本期方向总览

| # | 方向 | 类型 | 影响 | 核心代码锚点 | 既有覆盖 |
|---|------|------|------|-------------|---------|
| 1 | **多存储后端智能路由与 Placement Engine** | 架构/成本 | 🔴 统一多后端命名空间的缺失拼图 | `internal/storage/factory.go`, `internal/repository/repository.go:Object.Backend`, `internal/service/file_crud.go:Put` | **零覆盖** |
| 2 | **分布式 Tracing 成熟度：跨组件 Span 覆盖与 Debug 平台** | 可观测/调试 | 🔴 OTEL 框架就绪但应用层 0 spans | `internal/telemetry/otel.go`, `internal/telemetry/http.go` | **零覆盖** |
| 3 | **自定义对象 Schema 与基于 Schema 的校验/检索** | 功能/差异化 | 🟠 从"盲存"到"结构化管理" | `internal/service/file.go:validateMetadata`, `internal/repository/repository.go:Object.Metadata` | **零覆盖** |
| 4 | **多租户成本内部分摊（Showback）与消费异常检测** | 运维/成本 | 🟠 内部成本归属与预算风控基础设施 | `internal/repository/quota.go`, `internal/ai/cost.go`, `internal/telemetry/metrics.go` | **零覆盖** |
| 5 | **Webhook 事件目录与 Payload 转换管线** | 集成/生态 | 🟠 从"固定格式 POST"到"生态集成枢纽" | `internal/events/webhook.go`, `internal/events/bus.go`, `internal/repository/webhook_failures.go` | **零覆盖** |

---

## 1. 多存储后端智能路由与 Placement Engine

### 当前状态

**单后端，无路由。** 当前 `Storage` 接口假设你只有一个后端实例。

```go
// internal/storage/storage.go:102
type Storage interface {
    Put(ctx, key, r, size, opts) (ObjectInfo, error)
    Get(ctx, key) (io.ReadCloser, ObjectInfo, error)
    // ...
}

// internal/storage/factory.go:45-58
// 启动时选择一个后端（local / s3 / oss / cos），运行期间不可变更
case BackendLocal:
    store, _ = NewLocal(cfg.Local)
case BackendS3:
    store, _ = NewS3(ctx, bc)
// ...
```

**有趣的事实：** `Object` 结构体已经有了 `Backend string` 字段，并且 `objects` 表也有一列 `backend`（查看 migration 0001_init.up.sql）。但是——所有对象始终指向同一个后端，无法利用这个字段做路由。

```go
// internal/repository/repository.go:21-45
type Object struct {
    Backend      string   // 总是 "local" 或 "s3"，不会变化
    StorageKey   string   // 在单后端中唯一
    // ...
}
```

**当前单后端的局限性：**

| 场景 | 需要 | 当前能力 |
|------|------|---------|
| 成本优化：热数据放 NVMe，温数据放 HDD，冷数据放 S3 | 多后端同时在线 + 按策略路由 | ❌ 只有一个后端 |
| 合规：敏感数据放 EU 区域 S3，普通数据放本地 | 按标签/元数据选择后端 | ❌ |
| 冗余：同时写入 local + S3 做实时双写 | write-through 到多个后端 | ❌ |
| 性能：小文件放本地 SSD，大文件放远程 S3 | 按大小阈值路由 | ❌ |
| 供应商锁定规避：同时使用 2 个云厂商 | 基于成本/延迟的智能路由 | ❌ |

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/storage/factory.go:45-58` | `NewFromConfig` 返回 `Storage`（单后端） | 无 `MultiBackendStore` |
| `internal/storage/storage.go:102` | `Storage` 接口代表一个后端 | 无组合/路由抽象 |
| `internal/service/file_crud.go:Put` | `s.store.Put(ctx, key, ...)` — 单 store | 无选择后端的逻辑 |
| `internal/service/file_crud.go:serveObjectContent` | `s.store.Get(ctx, obj.StorageKey)` | 无后端感知的读取路由 |
| `internal/repository/repository.go:Object.Backend` | 字段存在但总是同一值 | 从未用于读取决策 |
| `internal/service/file.go:FileService` | 持有 `store storage.Storage`（单） | 需改为 `stores map[string]storage.Storage` |
| `internal/service/service_test.go` | 基础测试 | 无多后端测试 |
| `internal/storage/contract_test.go` | 单后端合同测试 | 无多后端路由合同测试 |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **后端 A 已死但对象在后端 A** | 某后端不可达，其对象无法读取 | 错误传播到 handler | 如果有副本在后端 B，降级读取；否则返回 503 + 后端名 |
| **策略说放 A 但 A 已满** | 放置策略选择 local 但磁盘 100% | 硬错误 | 降级到次优后端 + 记录 `placement_fallback_total` 指标 |
| **PUT 到多后端部分失败** | 双写到 S3 成功但 local 失败 | 部分写入，无一致性保证 | 事务-like: 至少写入一个 → 记录 partial 状态 → reconcile 修复 |
| **对象跨后端移动后并发读** | 迁移中对象被读取 | 可能读到旧副本或空 | 读路由用 `StorageKey` + `Backend` 定位；迁移过程保持两者有效 |
| **后端策略变更后历史对象** | 策略从 S3 → OSS，但历史对象仍在 S3 | 留在原地，永不迁移 | 策略变更触发异步 rebalance job（复用 v10#3 迁移框架） |
| **跨后端版本一致性** | 双写时对象在第 3 次版本后后端 A 写失败 | 双写状态追踪复杂 | 用 `backend` 字段记录"权威后端"，其他为镜像；失败时标记待修复 |
| **后端点位成本不固定** | S3 价格变动，需要重新评估最优放置 | 硬编码策略 | 成本配置驱动 + 定时 rebalance job 重新评估 |

### 架构蓝图

```
┌─ 多后端注册与路由 ───────────────────────────────────────────│
│ 新增: internal/storage/multibackend.go                          │
│                                                                  │
│ type BackendConfig struct {                                      │
│     Name    string         // "local-ssd", "s3-eu-west", "oss-cn" │
│     Store   storage.Storage                                      │
│     Weight  float64        // 路由权重（随机选择时使用）           │
│     Default bool           // 无匹配规则时的默认后端              │
│     Priority int           // 回退优先级（数字越低越优先）         │
│ }                                                                 │
│                                                                  │
│ type PlacementRule struct {                                      │
│     Name        string                                           │
│     Target      string         // 目标后端名                      │
│     Conditions  []PlacementCondition                             │
│ }                                                                 │
│                                                                  │
│ type PlacementCondition struct {                                 │
│     Field  string // "size" | "content_type" | "metadata.x" |    │
│                   // "bucket" | "prefix" | "tag"                 │
│     Op     string // "lt" | "gt" | "eq" | "prefix" | "regex"    │
│     Value  string                                                │
│ }                                                                 │
│                                                                  │
│ type MultiBackendStore struct {                                  │
│     backends map[string]BackendConfig                            │
│     rules    []PlacementRule                                     │
│     fallback string        // 兜底后端名                          │
│     picker   PlacementPicker // 策略引擎                         │
│ }                                                                 │
│                                                                  │
│ 放置流程:                                                         │
│   Put(key, r, size, opts) →                                      │
│     1. 顺序匹配 PlacementRule（首匹配优先）                       │
│     2. 选中目标后端 → store.Put(ctx, key, r, size, opts)         │
│     3. 记录 Object.Backend = selectedBackend                     │
│     4. 可选：异步双写到次优后端（replication mode）              │
│                                                                  │
│ 读取流程:                                                         │
│   Get(ctx, obj) →                                                │
│     1. 从 obj.Backend 定位后端                                    │
│     2. 如果该后端不可达 → 按 Priority 回退到其他后端              │
│     3. 如果无任何后端可读 → 返回 503 FallbackExhausted           │
│                                                                  │
│ 配置格式（环境变量）:                                              │
│   STORAGE_BACKENDS='[                                             │
│     {"name":"local-ssd","kind":"local","root":"./var/ssd",       │
│      "weight":1.0},                                              │
│     {"name":"s3-eu","kind":"s3","bucket":"prod-eu",              │
│      "weight":0.5}                                               │
│   ]'                                                              │
│   STORAGE_PLACEMENT_RULES='[                                       │
│     {"name":"small-to-local","target":"local-ssd",                │
│      "conditions":[{"field":"size","op":"lt","value":"1048576"}]},│
│     {"name":"media-to-s3","target":"s3-eu",                       │
│      "conditions":[{"field":"content_type","op":"prefix",         │
│                     "value":"video/"}]}                            │
│   ]'                                                              │
└────────────────────────────────────────────────────────────────┘

┌─ Read-Through Write-Through 策略 ─────────────────────────────│
│ Write-Through（双写）:                                            │
│   每次 PUT → 写入主后端 + 异步写入 1 个或多个副本后端               │
│   主后端由 PlacementRule 决定                                      │
│   副本后端由 REPLICA_BACKENDS 配置决定                             │
│   Object 元数据记录 primary + replicas（JSON 在 storage_key 旁）   │
│   读失败时按 (primary → replica1 → replica2) 顺序回退              │
│                                                                  │
│ Read-Through（缓存）:                                             │
│   如果 local-ssd 作为 S3 的缓存层：                                │
│     Get → 先查 local-ssd → miss → 从 S3 读取 → 缓存到 local      │
│     类似一般的 cache-aside 模式                                    │
│   使用 LRU 或 LFU 驱逐策略：                                       │
│     热点对象保留在 SSD 层                                          │
│     冷对象仅存在 S3 后端                                           │
│                                                                  │
│ 复制模式:                                                          │
│   1. async: 主后端成功立即返回，副本异步写入                        │
│   2. sync: 所有后端确认后才返回（强一致但慢）                       │
│   3. quorum: W > N/2 写入成功即返回（R > N/2 读取）                │
└────────────────────────────────────────────────────────────────┘

┌─ rebalance job ───────────────────────────────────────────────│
│ 复用 v10#3 迁移框架 + v9#3 CDC 流：                               │
│                                                                  │
│ 触发条件:                                                         │
│   1. 放置策略变更（新规则生效）                                     │
│   2. 后端成本变更（rebalance cost evaluation）                     │
│   3. 后端健康事件（某后端降级，转移其所负责的对象）                 │
│   4. 定时 rebalance（每月一次，重新评估所有对象的最优放置）          │
│                                                                  │
│ 执行流程:                                                         │
│   与现有迁移框架对齐，但目标是"按策略重新评估"，而非"全量迁移"。      │
│   1. 扫描策略规则变化                                              │
│   2. 对每个对象，重新匹配 PlacementRule                            │
│   3. 如果新的目标后端 != 当前后端：                                 │
│      执行对象级别迁移                                              │
│   4. 更新 Object.Backend                                           │
└────────────────────────────────────────────────────────────────┘

**为什么现在做：** `Object.Backend` 字段已经存在，`objects` 表已有 `backend` 列。多后端智能路由是"单后端"到"统一多后端命名空间"的进化——这是存储中间件的核心价值。当前架构已经为多后端铺好了元数据基础，只缺路由引擎。商业上，这是多云/混合云存储差异化竞争的关键能力。

| 影响面 | 工作量估计 |
|--------|-----------|
| `MultiBackendStore` 路由引擎 | 中 |
| 配置系统（多后端 + PlacementRule） | 低-中 |
| 读取回退逻辑 | 低 |
| Write-Through 双写 | 中 |
| Read-Through 缓存层 | 中 |
| rebalance job | 中（复用迁移框架） |
| 多后端 contract tests | 中 |

---

## 2. 分布式 Tracing 成熟度：跨组件 Span 覆盖与 Debug 平台

### 当前状态

**OTel 框架已完整就绪，但应用层 Spans 为零。**

```go
// internal/telemetry/otel.go
// OTel SDK 已完全配置：TracerProvider + MeterProvider + Propagator
// 支持 OTLP over HTTP 导出

// internal/telemetry/http.go
// 唯一创建 spans 的地方：
tracer := otel.Tracer("aero-vault/http")
ctx, span := tracer.Start(r.Context(), r.Method+" "+r.URL.Path, ...)
defer span.End()
```

**全代码库 spans 覆盖调查：**

```
$ grep -rn "tracer\|\.Start(" --include="*.go" | grep -v _test.go | grep -v "\.Start("
internal/telemetry/http.go      → ✓ HTTP 中间件（1 个 span/请求）
internal/telemetry/otel.go      → ✓ 框架初始化
internal/ai/*.go                → ✗ 无 spans
internal/service/*.go           → ✗ 无 spans
internal/storage/*.go           → ✗ 无 spans
internal/repository/*.go        → ✗ 无 spans
internal/events/*.go            → ✗ 无 spans
internal/jobs/*.go              → ✗ 无 spans
internal/reconcile/*.go         → ✗ 无 spans
internal/mcp/*.go               → ✗ 无 spans
internal/auth/*.go              → ✗ 无 spans
internal/antivirus/*.go         → ✗ 无 spans
internal/replication/*.go       → ✗ 无 spans
```

**缺失 Tracing 的影响：**

| 调试场景 | 当前能力 | 应该能力 |
|---------|---------|---------|
| 搜索请求慢：是 Embedding 慢还是 DB 检索慢？ | 只有 HTTP 总延迟 | 分解为：embed → search → rerank，每段独立 span |
| S3 后端上传慢：是网络延迟还是 circuit breaker 等待？ | 只看 HTTP PUT 延迟 | 分解为：store.Put → S3 SDK → TCP 连接 |
| SSE 事件延迟高：是 bus 分发慢还是 webhook HTTP POST 慢？ | 无指标 | 分解为：event → bus.deliver → webhook.POST |
| 文件读取慢：是 storage.Get 还是 SQL 查询慢？ | 合并延迟 | 分解为：repo.GetObject → store.Get → 响应 |
| 跨实例调试：请求经过了 LB → app → Postgres → S3 | 无 trace 关联 | trace ID 贯穿所有组件 |
| Job 执行慢：是作业等待队列还是执行本身？ | 无区分 | 分解为：job.wait → job.execute (含子 spans) |

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/telemetry/otel.go` | OTel SDK 完整: TracerProvider + MeterProvider | 只配置了，无人使用 API |
| `internal/telemetry/http.go` | 创建 HTTP span + 属性 | 只覆盖入口，不覆盖内部 |
| `internal/service/file_crud.go:Put` | 20+ 行关键逻辑 | 无 trace context 传播 |
| `internal/ai/search.go:Query` | 多层调用：embed → retrieve → rerank | 无子 span 分解 |
| `internal/storage/s3.go:Put` | 网络调用 S3 API | 无 span 记录延迟/错误 |
| `internal/repository/sql_objects.go:UpsertObject` | SQL INSERT/UPDATE | 无 DB span |
| `internal/jobs/jobs.go:processJob` | 执行 background job | 无 span（丢失作业追踪）|
| `internal/events/webhook.go:send` | HTTP POST webhook | 无 span |
| `internal/auth/sigv4.go:Sign` | SigV4 签名计算 | 无 span |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **Span 采样与成本** | 每秒 10000 请求，全量采样 → 采集器崩溃 | OTel SDK 默认全量采样 | 支持 head-based 采样（概率/速率），配置 `OTEL_TRACES_SAMPLER` |
| **Span 上下文传播到异步 Job** | 请求 A 触发 indexer job，10s 后执行 | 丢失与请求 A 的 trace 关联 | Job payload 中序列化 trace 上下文，启动时重建 parent span |
| **N+1 查询 spans** | ListObjects 循环 N 次 DB 查询 | N 个独立 span，无法关联 | 用 `WithLink` 或嵌套 span 表示循环 |
| **大文件流式读取 span 超长** | 10GB 文件 GET 持续 5 分钟 | span 跨度 5 分钟 | 拆分为多个子 span：read(0-16KB), read(16-32KB), ... |
| **跨服务 trace 传播** | LB → app → Postgres → S3 | trace ID 通过 HTTP headers 传播，但到 Postgres/S3 断链 | DB 调用设置 `db.statement` span 属性 + S3 SDK 集成 |
| **trace 过多导致 OOM** | 异常流量产生百万 trace | OTel SDK buffer 占满，进程 OOM | 配置 `MaxExportBatchSize` + 背压丢弃 |
| **无 trace 时额外开销** | TracerProvider 是 no-op | 每次 `tracer.Start()` 无开销 | 但建议在 no-op 时跳过 span 创建以消除函数调用开销 |

### 架构蓝图

```
┌─ Span 覆盖计划 ──────────────────────────────────────────────│
│ Phase 1（核心路径，3-5 天）:                                      │
│                                                                  │
│ 1. FileService 层                                                │
│    internal/service/file_crud.go:                                │
│      Put(ctx, ...) → span "file.put"                             │
│        子 span: "quota.check" → "repo.ensureBucket" →            │
│                 "store.Put" → "repo.UpsertObject"                │
│      Get/serveObjectContent → span "file.get"                    │
│        子 span: "repo.GetObject" → "store.Get"                   │
│                                                                  │
│ 2. AI 管线                                                       │
│    internal/ai/search.go:Query → span "ai.search"                │
│      子 span: "embed.query" → "index.search" →                  │
│               (optional) "rerank"                                │
│    internal/ai/chat.go:Answer → span "ai.chat"                   │
│      子 span: "ai.search" → "llm.complete"                      │
│                                                                  │
│ 3. 存储后端                                                       │
│    internal/storage/local.go:Put/Get → span "storage.put/get"   │
│      属性: size, backend_type, disk_latency_ms                   │
│    internal/storage/s3.go:Put → span "s3.put"                    │
│      属性: bucket, region, http_status_code                      │
│                                                                  │
│ Phase 2（完备路径，1-2 周）:                                      │
│   4. Repository: DB 查询 spans ("db.query", "db.upsert")        │
│   5. Events/Webhook: event 生产/消费 spans                       │
│   6. Jobs: job 入队/执行/完成 spans                              │
│   7. Reconcile: 定时任务执行 spans                               │
│   8. Auth: 鉴权/签名校验 spans                                   │
│                                                                  │
│ 统一模式:                                                         │
│   每个包获取 tracer：                                              │
│     var tracer = otel.Tracer("aero-vault/service")               │
│   添加辅助函数：                                                  │
│     func startSpan(ctx, name string, attrs ...) (context, span)  │
│   错误记录：                                                       │
│     span.RecordError(err)                                         │
│     span.SetStatus(codes.Error, err.Error())                      │
└────────────────────────────────────────────────────────────────┘

┌─ 异步 Job → Trace 传播 ───────────────────────────────────────│
│ 当前：Job 入队时 ctx 中的 trace 上下文丢失                          │
│                                                                  │
│ 方案：序列化 trace context 到 Job payload 的 `_trace` 字段          │
│   // 入队时：                                                     │
│   tc := trace.SpanContextFromContext(ctx)                        │
│   if tc.IsValid() {                                              │
│       carrier := propagation.MapCarrier{}                        │
│       otel.GetTextMapPropagator().Inject(ctx, carrier)           │
│       job.Payload["_trace"] = carrier                            │
│   }                                                              │
│                                                                  │
│   // 出队执行时：                                                 │
│   if carrier, ok := job.Payload["_trace"]; ok {                  │
│       ctx = otel.GetTextMapPropagator().Extract(ctx, carrier)    │
│   }                                                              │
│   ctx, span := tracer.Start(ctx, "job."+job.Type)               │
└────────────────────────────────────────────────────────────────┘

┌─ Debug Dashboard ─────────────────────────────────────────────│
│ 基于 Jaeger/Tempo 的 trace 查询：                                 │
│   - 按 service (aero-vault) + operation 查询                     │
│   - 按 tenant 标签筛选（span 中添加 tenant 属性）                 │
│   - 按 duration > 1s 过滤慢 trace                                │
│   - 按 HTTP status >= 500 筛选错误 trace                         │
│                                                                  │
│ 关键仪表盘面板:                                                    │
│   1. Trace 吞吐量（span/秒）                                      │
│   2. P50/P95/P99 span 持续时间（按 span name 分组）               │
│   3. 错误 span 率                                                 │
│   4. 最慢的 10 个 trace（按持续时间排序）                           │
│   5. Trace 采样率 vs 实际导出率                                   │
└────────────────────────────────────────────────────────────────┘

**为什么现在做：** OTel 的 `go.opentelemetry.io/otel` 已经是 `go.mod` 中的顶极依赖——基础设施成本已经支付。但当前只有 HTTP 入口有 spans，意味着追踪调优、慢请求定位、分布式调试的能力完全未解锁。在一个有 AI 管线（embed → search → rerank → LLM）、多后端存储、事件总线的系统中，没有 spans 几乎无法诊断性能问题。这个方向属于"高杠杆、低风险"——每个 span 的增量成本是 3-5 行代码，但边际价值是持续的调试效率提升。

| 影响面 | 工作量估计 |
|--------|-----------|
| FileService spans（Phase 1） | 低（~30 行） |
| AI 管线 spans（Phase 1） | 低（~30 行） |
| Storage backend spans（Phase 1） | 低（~30 行） |
| 异步 Job trace 传播（Phase 2） | 中 |
| Repository spans（Phase 2） | 低 |
| Events/Webhook spans（Phase 2） | 低 |
| Debug dashboard（Grafana/Tempo） | 低 |

---

## 3. 自定义对象 Schema 与基于 Schema 的校验/检索

### 当前状态

**Metadata 是自由格式的 `map[string]string`，完全没有 Schema 约束。**

```go
// internal/repository/repository.go
type Object struct {
    Metadata     map[string]string   // 自由格式，任何 key/value
    // ...
}

// 用户 PUT 时可以传入任何 metadata：
// X-Aero-Meta-Color: red
// X-Aero-Meta-Project: my-project
// X-Aero-Meta-Priority: high
// 没有校验，没有类型，没有必填约束，没有默认值
```

**当前 metadata 处理路径：**

```
PUT /v1/files/doc.txt (with X-Aero-Meta-* headers)
  → handler.go:writeMetadataHeaders() — 解析 headers 为 map
  → service/file.go:validateMetadata() — 只检查 key 命名规则
  → repository/sql_objects.go — 序列化为 JSON 存入 metadata 列
  → 存入后不再有结构验证
```

**metadata 的产出路径：**

```
GET /v1/files/doc.txt
  → handler.go — 反序列化 metadata JSON 到 map
  → 作为 X-Aero-Meta-* 响应头返回
```

**当前缺失的能力：**

| 能力 | 需要程度 | 当前 |
|------|---------|------|
| 定义对象的自定义类型（如 "Document"、"Image"、"Video"） | 🔴 企业内容管理基础 | ❌ |
| 必填字段校验（如 Document 必须有关键字 "author"） | 🔴 数据完整性 | ❌ |
| 字段类型约束（如 "year" 必须是整数 1900-2099） | 🟠 输入验证 | ❌ |
| 字段默认值（如 "status" 默认为 "draft"） | 🟠 降级输入复杂度 | ❌ |
| 基于 metadata 字段的搜索（"所有 author=张三 的文档"） | 🔴 元数据检索核心 | ❌（只能全文搜内容） |
| 基于 metadata 的 ACL/Policy（"只有 author 可以删除"） | 🟠 细粒度权限 | ❌ |
| 自动 UI 表单生成（根据 schema 生成上传表单） | 🟠 用户体验 | ❌ |

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/service/file.go:validateMetadata` | 只检查 key 命名规则（禁止 `_aero` 前缀等） | 无 schema 校验 |
| `internal/repository/repository.go:Object.Metadata` | `map[string]string` | 无法表达类型信息 |
| `internal/repository/sql_objects.go:UpsertObject` | metadata 序列化为 JSON 字符串 | 无 schema 验证步骤 |
| `internal/api/rest/handler.go:writeMetadataHeaders` | 只解析 headers 为 map | 无 schema 映射 |
| `internal/api/rest/search.go` | ai.Search.Query 只搜索 content/chunks | 无 metadata 字段搜索 |
| `internal/ai/search.go:Request` | `Query` (string) + `K` (int) | 无 `MetadataFilter` 字段 |
| `internal/repository/sql_chunks.go` | chunks 表存的是文本片段 + 向量 | 无 metadata 列的倒排索引 |
| `internal/service/file_features.go:ListObjects` | 仅 prefix + marker + limit 分页 | 无 metadata 过滤条件 |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **Schema 版本迁移** | 已有 1 万个对象用的是 schema v1，现在定义 v2 新增必填字段 `rating` | 老对象无 `rating` | 老对象默认值 = null 或 schema 定义的 default；迁移工具逐步补充 |
| **Schema 兼容性** | 新增字段 vs 修改已有字段类型 | schema 变更不向后兼容 | schema 版本号 + 兼容性检查（不允许非兼容变更） |
| **交叉类型查询** | 搜 `type=Document AND metadata.author=张三` | 无能力 | schema-aware 查询引擎 |
| **元数据值超长** | 用户传入 10MB 的 metadata 值 | 不限制，DB 压力 | 单值 max 1KB，总 metadata max 64KB |
| **Schema 与 S3 兼容性** | S3 PUT 的 x-amz-meta-* headers 如何映射到 schema 字段 | S3 headers 不感知 schema | S3 协议中 schema 字段通过 `x-amz-meta-schema-{field}` 传递 |
| **Schema 继承** | 创建一个 "ConfidentialDocument" 继承 "Document" | 无概念 | schema 支持 `extends` 语义 |
| **Schema 变更后的搜索一致性** | 修改 schema 后索引未重建，按新字段搜索返回空 | 无提示 | schema 变更触发 reindex（类似 AI_REINDEX）或写时验证 |

### 架构蓝图

```
┌─ Schema 定义 ─────────────────────────────────────────────────│
│ 新增 internal/schema/ 包                                         │
│                                                                  │
│ type Schema struct {                                             │
│     ID          string       // uuid                             │
│     Name        string       // "Document"                       │
│     Version     int          // 1, 2, 3                          │
│     TenantID    string       // 所属租户                          │
│     Bucket      string       // "" = 所有桶; 或限定到指定桶       │
│     Fields      []FieldDef                                       │
│     Description string                                            │
│     CreatedAt   time.Time                                         │
│     UpdatedAt   time.Time                                         │
│ }                                                                 │
│                                                                  │
│ type FieldDef struct {                                            │
│     Name        string     // "author"                           │
│     Type        FieldType  // string, int64, float64, bool,       │
│                            // datetime, enum, string_array        │
│     Required    bool                                               │
│     Default     *string    // nil = 无默认值                       │
│     MinLength   *int                                               │
│     MaxLength   *int                                               │
│     MinValue    *float64                                           │
│     MaxValue    *float64                                           │
│     Enum        []string   // 枚举允许值                           │
│     Pattern     *string    // 正则校验                            │
│     Description string                                             │
│ }                                                                 │
│                                                                  │
│ Schema 存储（复用现有 Repository 模式）:                          │
│   新增表 object_schemas:                                          │
│     id, tenant_id, bucket, name, version, fields (JSON),          │
│     active (bool), created_at, updated_at                         │
│                                                                  │
│ Schema 管理 API:                                                   │
│   POST   /v1/admin/schemas               → 创建 schema            │
│   GET    /v1/admin/schemas               → 列出 schema            │
│   GET    /v1/admin/schemas/{id}          → schema 详情            │
│   POST   /v1/admin/schemas/{id}/version  → 创建新版本             │
│   DELETE /v1/admin/schemas/{id}          → 删除（标记废弃）        │
└────────────────────────────────────────────────────────────────┘

┌─ Schema 验证（写入时） ────────────────────────────────────────│
│ 在 FileService.Put 中新增步骤：                                   │
│                                                                  │
│ 1. 根据 tenant+bucket 查找绑定的 schema                          │
│ 2. 如果绑定：                                                     │
│    a. 检查必填字段是否存在                                         │
│    b. 检查字段类型（string/int/bool/datetime/date）               │
│    c. 检查取值约束（min/max/pattern/enum）                       │
│    d. 填充默认值                                                   │
│    e. 如果任何校验失败 → 返回 422 Unprocessable Entity             │
│ 3. 如果未绑定：自由格式 metadata（向后兼容）                       │
│                                                                  │
│ 性能考虑:                                                          │
│   Schema 缓存（内存，TTL 60s）避免每次 PUT 查 DB                  │
│   只有 schema 绑定的桶才进入校验路径，其他桶零开销                  │
└────────────────────────────────────────────────────────────────┘

┌─ Schema-aware 检索（读取时） ──────────────────────────────────│
│ 扩展 search.Request 新增 MetadataFilter 字段：                     │
│                                                                  │
│ type MetadataFilter struct {                                      │
│     Field    string                                               │
│     Operator string    // "eq", "neq", "gt", "gte", "lt", "lte",  │
│                        // "in", "exists", "prefix"                │
│     Value    string                                               │
│ }                                                                 │
│                                                                  │
│ 检索管线:                                                          │
│   1. 全文检索（现有 BM25/Vector 不变）                             │
│   2. metadata 字段精确匹配（metadata 列 JSON 查询）               │
│   3. 如果 schema 定义了 datetime 类型，支持范围查询                │
│   4. 结果取交集（AND 语义）                                        │
│   5. SQLite 用 JSON_EXTRACT，Postgres 用 jsonb_path_query         │
│                                                                  │
│ 索引优化:                                                          │
│   高频 metadata 字段可以投影到独立列（如 metadata_author）         │
│   或使用 generated column + index                                  │
└────────────────────────────────────────────────────────────────┘

**为什么现在做：** 这是一个"当前是空白但竞争对手都有"的功能。AWS S3 有 tags（有限的结构），Google Cloud Storage 有 labels，但都不支持 schema 定义+校验。这实际上是 AeroVault 从"对象存储"进化为"智能内容平台"的分水岭功能。Schema 定义能力对企业内容管理（ECM）、数字资产管理（DAM）、文档管理系统是核心竞争力。并且项目已有 tags/metadata 基础设施——schema 层是自然的向前演进。

| 影响面 | 工作量估计 |
|--------|-----------|
| Schema 定义 + 存储（表 + CRUD） | 中 |
| PUT 时 schema 校验 | 中 |
| Schema 管理 API | 中 |
| Metadata 字段搜索 | 中 |
| 缓存（schema + metadata index） | 低 |
| Schema 版本迁移 | 中 |
| UI 表单生成 | 低（基于 schema JSON） |

---

## 4. 多租户成本内部分摊（Showback）与消费异常检测

### 当前状态

**跨租户成本可见性有限，无法做内部成本归属。**

当前租户级别追踪的能力：

| 维度 | 当前实现 | 状态 |
|------|---------|------|
| 存储用量（bytes, objects） | `TenantQuota.UsedBytes` / `UsedObjects` | ✅ 基础 |
| AI 费用（micros） | `ai_usage_cost` 表，per-query 记录 | ✅ 基础 |
| AI 日预算 | `AI_TENANT_DAILY_BUDGET_USD` | ✅ 
| 全局 RPS 限流 | `RATE_LIMIT_RPS` per tenant | ✅ |
| 所有租户的聚合视图 | `ListTenantQuotas()` | ✅ |

**完全缺失的 Showback 能力：**

| 能力 | 商业价值 | 当前 |
|------|---------|------|
| 按 bucket/prefix/tag 的成本归因 | 知道"哪个项目/部门/团队花多少钱" | ❌ |
| 成本趋势（7d/30d 趋势） | 发现异常增长 | ❌ |
| 成本预测（基于历史增长曲线） | 预算规划 | ❌ |
| 每日/每周/每月成本报告 | 内部账单 | ❌ |
| 成本异常检测（突然异常增长） | 防止预算失控 | ❌ |
| 按存储类的成本分摊 | 精确区分 hot/cold 存储成本 | ❌ |
| 跨租户成本对比 | 识别资源消耗大户 | ❌ |
| 成本阈值多层次告警（50%/75%/90%/100%） | 预算风控 | ❌（只有 100% 日预算阻断） |

### 为什么 Showback 不同于 Billing

v7#3 讨论的是 Billing（计费）——向外部客户收费。这是 SaaS 商业化的功能。

Showback（成本内部分摊）是不同的概念——在一个企业内部，IT 部门的成本按照实际使用量分摊到各个业务线/团队/项目。没有 Showback：
- 业务线不了解自己的存储成本
- IT 部门无法驱动"成本意识"（谁会为"便宜"操心？反正不是自己付钱）
- 成本异常需要财务周期结束才能发现

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/repository/quota.go` | `GetTenantQuota` / `SetTenantQuota` — 只存储总用量 | 无按 bucket/prefix/tag 的细分用量 |
| `internal/repository/sql_events.go` | 事件表记录对象变更 | 可用来计算每个对象的操作（但当前不汇总） |
| `internal/ai/cost.go` | `RecordUsage` — AI 调用计费 per tenant | 不记录对应的 bucket/key/query |
| `internal/telemetry/metrics.go` | 15 domain 指标 | 无 cost_by_tag/bucket 指标 |
| `internal/service/file_crud.go:Put` | 写入时更新 Quota.UsedBytes | 只累加到总计数 |
| `internal/repository/tenants.go` | 租户 CRUD | 无成本归因字段 |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **Tag 变动后成本归属变更** | 对象从 tag `project:A` 重新标记为 `project:B` | 成本仍归属 A | 按 tag 变动时间点做 prorated 分摊 |
| **存储成本每天波动** | 某天写入 1TB → 成本飙升 → 第二天删除 | 只有总量，无时间维度 | 时间序列成本曲线（按小时/天聚合） |
| **搜索消耗不计入成本** | 每个搜索请求消耗 CPU + 内存，但无计量 | 搜索免费 → 激励滥用 | 搜索引擎成本系数（按请求耗时加权） |
| **跨租户成本对比时数据倾斜** | 一个租户有 100 万个对象，另一个有 100 个 | 聚合查询跨租户倾斜 | 用 dashboard 模板变量或 separate top-10 视图 |
| **成本报告延迟** | 成本数据需要 T+1 才能出报告 | 无 SLA | 准实时（< 5 分钟延迟） |
| **免费层的成本补贴** | 免费租户的实际成本 > 免费配额 | 统一计量 | 出免费租户的"隐性成本"报告 |
| **标签层次结构** | `cost-center:eng` 包含子标签 `project:search` | 无层次 | 标签层次支持聚合查询 |

### 架构蓝图

```
┌─ 成本归因数据模型 ─────────────────────────────────────────────│
│ 新增表 cost_allocations:                                          │
│   id            bigserial                                         │
│   tenant_id     text NOT NULL                                     │
│   date          date NOT NULL        // 按天分区                  │
│   category      text NOT NULL        // "storage"|"ai"|"egress"   │
│   bucket        text                                             │
│   key_prefix    text                 // 按前缀归类                │
│   tags          jsonb                // 对象标签，用于成本归因     │
│   bytes_hours   bigint               // 存储类：byte * hour       │
│   requests      int                  // 操作次数                  │
│   ai_tokens     int                  // AI 消耗（入+出）          │
│   cost_micros   bigint               // 估算成本（微元）          │
│   PRIMARY KEY (tenant_id, date, category, bucket, key_prefix)     │
│                                                                  │
│ 成本归因 pipeline:                                                 │
│   写入时（PUT/DELETE）:                                           │
│     更新 TenantQuota.UsedBytes（已有）                            │
│     同时写 cost_allocations 行（bucket + key prefix + tags）      │
│                                                                  │
│   读取时（GET/HEAD）:                                             │
│     cost_allocations.requests++（异步，影响性能时可抽样）          │
│                                                                  │
│   AI 调用时:                                                      │
│     现有 ai_usage_cost 行 → 同步写 cost_allocations               │
│                                                                  │
│   每日汇总 Job（凌晨 2 点）:                                       │
│     聚合前一天的 cost_allocations 行                               │
│     生成 per-tenant 每日报告 + 检测异常 + 预测趋势                │
└────────────────────────────────────────────────────────────────┘

┌─ 成本异常检测 ─────────────────────────────────────────────────│
│ 基于聚合后的每日成本数据：                                          │
│                                                                  │
│ 检测规则:                                                          │
│   1. 日环比 > 50% → 标记为 `cost_spike`                           │
│   2. 7 天移动平均 × 1.5 阈值 → 标记为 `cost_trend_up`            │
│   3. 同类型租户对比 > 3σ → 标记为 `cost_outlier`                  │
│   4. 某 bucket/tag 成本占比突变 > 20% → 标记为 `cost_shift`       │
│                                                                  │
│ 告警通道:                                                          │
│   - 日志（`warn` 级别）                                            │
│   - 新增指标 `cost_anomaly{tenant, type}`                         │
│   - 可选：Webhook 通知（复用现有 webhook 框架，新增 event type）   │
│                                                                  │
│ 仪表盘面板:                                                        │
│   - 每租户成本趋势（7d/30d 面积图）                                │
│   - Top-10 成本标签（饼图）                                        │
│   - 异常事件时间线                                                 │
│   - 成本预测 vs 实际（虚线对比）                                    │
└────────────────────────────────────────────────────────────────┘

┌─ 成本报告 API ─────────────────────────────────────────────────│
│ 管理端点（admin scope）:                                           │
│   GET /v1/admin/cost/daily?from=&to=&tenant=                        │
│     → [{date, storage_cost, ai_cost, egress_cost, total}]          │
│   GET /v1/admin/cost/by-tag?from=&to=&tenant=                      │
│     → [{tag_key, tag_value, storage_bytes, ai_tokens, cost}]       │
│   GET /v1/admin/cost/by-bucket?from=&to=&tenant=                   │
│     → [{bucket, storage_bytes, requests, cost}]                    │
│   GET /v1/admin/cost/anomalies?from=&to=                           │
│     → [{tenant, type, severity, detected_at, detail_url}]          │
│   GET /v1/admin/cost/forecast?tenant=&horizon_days=30              │
│     → [{date, predicted_cost, confidence_lower, confidence_upper}] │
└────────────────────────────────────────────────────────────────┘

**为什么现在做：** 多租户系统上线后最常被问到的问题是"哪个部门/项目花了多少钱？"和"为什么这个月的成本比上月高 30%？"。没有 Showback，IT 运维无法做成本归因和预算风控——这是从"技术项目"到"企业服务"的必经之路。当前 quota + ai_usage_cost 已经提供了基础数据，成本归属（按 bucket/tag 维度）是"最后一公里"。

| 影响面 | 工作量估计 |
|--------|-----------|
| `cost_allocations` 表 + 模型 | 低-中 |
| 写入时成本归因（PUT/DELETE） | 中 |
| AI 调用成本归因 | 低 |
| 每日聚合 Job | 中 |
| 异常检测规则引擎 | 中 |
| 成本报告 API | 中 |
| Grafana 仪表盘 | 中 |

---

## 5. Webhook 事件目录与 Payload 转换管线

### 当前状态

**单一目标 URL，固定 payload 格式，无转换能力。**

```go
// internal/events/webhook.go
// 事件 → 序列化为 JSON → POST 到单一 webhook URL

// 当前 webhook 配置：
// EVENTS_WEBHOOK_URL=https://hooks.example.com/events
// EVENTS_WEBHOOK_SECRET=...
```

**当前 webhook 能力全景：**

| 维度 | 当前 |
|------|------|
| 目标数量 | 1（全局 webhook URL） |
| 事件过滤 | 无（所有事件都发送） |
| Payload 格式 | 固定的 JSON 格式（`repository.Event` 序列化） |
| Payload 转换 | 无 |
| 签名 | HMAC-SHA256（现有） |
| 重试 | 指数退避（现有） |
| 失败持久化 | `webhook_failures` 表（现有） |
| Dashboard | 无 |
| Event Schema | 无（从代码反推） |

**对比企业级 Webhook 平台（如 GitHub、Slack、Stripe）的差距：**

| 功能 | GitHub Webhooks | AeroVault Webhooks |
|------|----------------|-------------------|
| 按事件类型订阅 | ✅ `push`, `pull_request`, `issues` 等 | ❌ 全部或无 |
| 按内容过滤 | ✅ `?issue.label=bug` | ❌ |
| Payload 版本 | ✅ `X-GitHub-Event` + schema 版本 | ❌ |
| 重试可视化 | ✅ Delivery log | ❌ |
| 手动重试 | ✅ 在 UI 上点重试 | ✅ `POST /admin/jobs/{id}/retry` |
| 事件 Schema | ✅ 完整的 JSON schema 文档 | ❌ 只能读代码 |
| 测试模式 | ✅ 可发送测试 payload | ❌ |
| 多个端点 | ✅ 支持多个 webhook | ❌ 只支持一个 |
| Payload 转换 | ✅ 可选 `application/vnd.github+json` | ❌ |
| 限流通知 | ✅ 返回 `X-RateLimit-*` headers | ❌ |

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/events/webhook.go:send` | 固定的 JSON POST | 无 payload 转换管道 |
| `internal/events/bus.go:Subscribe` | 订阅者接收完整 Event 结构 | 无事件过滤机制 |
| `internal/events/bus.go:broadcast` | 所有事件发给所有订阅者 | 无按 type/bucket 的路由 |
| `internal/repository/webhook_failures.go` | `webhook_failures` 表存储失败记录 | 无成功率/SLA 指标 |
| `internal/config/config_app.go:WebhookURL` | 单一的 webhook URL 配置 | 不支持多目标 |
| `internal/config/config_app.go:WebhookSecret` | 单一的签名密钥 | 不支持 per-target 密钥 |
| `internal/api/rest/router.go` | SSE 端点 | webhook 管理端点缺失 |
| `internal/api/rest/admin.go` | 仅有 `ListWebhookFailures` | 无 webhook CRUD |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **Webhook 目标死循环** | 目标 URL 是 AeroVault 自身（事故） | HTTP POST 到自身 → 又产生事件 → 无限循环 | 检测回环（`X-Aero-Webhook: true` header）→ 拒绝 |
| **Payload 大小超限** | 事件对象包含 10MB metadata | POST 10MB body → 目标拒绝 | 截断 payload 发 `{"truncated": true, "event": "..."}` |
| **多目标一致性** | 两个 webhook 目标，一个成功一个失败 | 无影响 | 各自独立追踪，失败不影响其他目标 |
| **事件风暴** | 批量删除 10000 个对象 → 10000 个事件 → webhook 目标被打爆 | 所有事件排队发送 | 事件批处理（batch 100 个事件为一个 POST）|
| **Schema 版本不匹配** | webhook 目标期望 v2 payload，发送了 v1 | 解析失败 | 通过 `X-Aero-Webhook-Version` header 协商 |
| **Webhook 目标降级** | 目标返回 503，持续 30 分钟 | 指数退避重试 3 次 → 写入 `webhook_failures` | 检测持续失败 → 自动暂停该目标 → 恢复后重试 |
| **测试 webhook** | 管理员想测试 webhook 配置是否有效 | 必须手动触发一个真实事件 | `POST /v1/admin/webhooks/{id}/test` → 发送测试事件 |

### 架构蓝图

```
┌─ 事件目录（Event Catalog） ──────────────────────────────────│
│ 新增 internal/events/catalog.go                                  │
│                                                                  │
│ type EventSchema struct {                                        │
│     Type        string   // "object.created"                     │
│     Version     int      // 1                                    │
│     Description string                                           │
│     Schema      string   // JSON Schema 定义                      │
│     Sample      string   // 示例 payload                          │
│     Deprecated  bool                                              │
│     Since       string   // "0.1.0" — 引入版本                    │
│ }                                                                 │
│                                                                  │
│ 注册所有事件类型：                                                  │
│   object.created / object.deleted / object.updated                │
│   bucket.created / bucket.deleted                                 │
│   ai.search / ai.chat / ai.agent                                  │
│   job.completed / job.failed                                     │
│   tenant.quota_exceeded / tenant.budget_exceeded                 │
│   storage.corruption_detected / storage.backend_degraded         │
│                                                                  │
│ 目录端点：                                                         │
│   GET /v1/admin/webhooks/events → 列出所有事件类型 + schema      │
│   GET /v1/admin/webhooks/events/{type} → 特定事件 schema + 示例  │
└────────────────────────────────────────────────────────────────┘

┌─ 多 Webhook 目标管理 ────────────────────────────────────────│
│ 新增表 webhook_targets:                                          │
│   id            uuid                                             │
│   tenant_id     text                                             │
│   name          text           // "slack-alerts"                  │
│   url           text           // 目标 URL                        │
│   secret        text           // HMAC 密钥                       │
│   event_types   jsonb          // ["object.created", ...]        │
│   filters       jsonb          // {bucket: "prod", ...}           │
│   retry_config  jsonb          // {max_attempts:3, backoff:"exponential"} │
│   active        bool                                              │
│   created_at    timestamptz                                       │
│   updated_at    timestamptz                                       │
│                                                                  │
│ 管理 API:                                                         │
│   POST   /v1/admin/webhooks/targets               → 创建目标      │
│   GET    /v1/admin/webhooks/targets               → 列出目标      │
│   GET    /v1/admin/webhooks/targets/{id}          → 目标详情      │
│   PUT    /v1/admin/webhooks/targets/{id}          → 更新目标      │
│   DELETE /v1/admin/webhooks/targets/{id}          → 删除目标      │
│   POST   /v1/admin/webhooks/targets/{id}/test     → 发送测试事件   │
│   POST   /v1/admin/webhooks/targets/{id}/retry    → 重试失败事件   │
│   GET    /v1/admin/webhooks/targets/{id}/deliveries → 投递历史     │
└────────────────────────────────────────────────────────────────┘

┌─ Payload 转换管线 ────────────────────────────────────────────│
│ 当前：固定 JSON → 目标 URL                                        │
│ 目标：事件 → 转换管线（可选链）→ 目标 URL                          │
│                                                                  │
│ type WebhookPipeline struct {                                     │
│     TargetID string                                                │
│     Transforms []TransformStep    // 转换步骤链                   │
│ }                                                                 │
│                                                                  │
│ type TransformStep struct {                                       │
│     Type    string // "filter_fields" | "rename_fields" |          │
│                    // "template" | "jq" | "add_header"             │
│     Config  map[string]any                                        │
│ }                                                                 │
│                                                                  │
│ 内置转换器:                                                        │
│   1. filter_fields: 选择包含/排除的字段                             │
│      Config: { "include": ["event_type", "object.key", "timestamp"] } │
│   2. rename_fields: 字段重命名                                     │
│      Config: { "object.key": "path", "event_type": "action" }     │
│   3. template: Go text/template 渲染（输出自定义格式）              │
│      Config: { "template": "{{.EventType}}: {{.Object.Key}}" }    │
│   4. add_header: 自定义请求头                                      │
│      Config: { "X-My-App-Version": "1.0" }                        │
│                                                                  │
│ 管道执行顺序:                                                      │
│   1. 事件过滤（event type + condition filter）                     │
│   2. Payload 转换（按配置链顺序执行）                                │
│   3. HTTP 签名（HMAC-SHA256，已有）                                │
│   4. HTTP POST（已有）                                             │
└────────────────────────────────────────────────────────────────┘

┌─ Webhook 可观测性 ────────────────────────────────────────────│
│ 新增指标:                                                          │
│   webhook.deliveries_total{target, status}  // "success"|"failed" │
│   webhook.delivery_duration_ms{target}       // P50/P95/P99       │
│   webhook.batch_size{target}                // 事件批大小          │
│   webhook.paused{target}                    // 0/1 gauge           │
│                                                                  │
│ 新增仪表盘面板:                                                    │
│   1. Webhook 投递成功率（每个 target 的 24h 成功率）                │
│   2. Webhook 延迟 P95（每个 target）                               │
│   3. Webhook 事件吞吐量（event/秒）                                │
│   4. 待重试事件数（积压量）                                         │
│                                                                  │
│ 自动暂停:                                                          │
│   持续失败（如 10 次连续失败）→ 自动暂停该 target → 标记为 paused  │
│   管理员手动恢复或定期（24h）自动重试一次                             │
└────────────────────────────────────────────────────────────────┘

**为什么现在做：** Webhook 是系统与外部集成的命脉。当前单一 URL + 固定格式的 webhook 在非生产环境中够用，但在企业集成场景中（Slack 通知、PagerDuty 告警、Jira 创建 ticket、自定义数据处理管线），每个下游系统需要不同的 payload 格式和过滤条件。Webhook 事件目录 + 转换管线使系统从"有一个 webhook"进化为"企业集成枢纽"——而这只需要在现有 webhook 框架上叠加管理层和转换层。

| 影响面 | 工作量估计 |
|--------|-----------|
| Event Catalog + schema 定义 | 低 |
| 多目标管理（表 + API） | 中 |
| Payload 转换管线（filter/template） | 中 |
| 事件过滤（type + condition） | 低 |
| Webhook 自动暂停 | 低 |
| Webhook 可观测性指标 | 低 |
| 管理 UI（WebUI 集成） | 中 |

---

## 汇总

| # | 方向 | 业务价值 | 技术风险 | 工作量估计 | 代码锚点数量 | 优先级 |
|---|------|---------|---------|-----------|------------|--------|
| 1 | 多后端智能路由与 Placement Engine | 🔴 多云/混合云差异化竞争力 | 中 | 中-高 | 6 | P1 |
| 2 | 分布式 Tracing 成熟度 | 🟠 生产调试效率倍增 | 低 | 低-中 | 8 | P0（高杠杆） |
| 3 | 自定义对象 Schema 与校验/检索 | 🟠 从"对象存储"到"智能内容平台" | 中 | 中 | 7 | P2 |
| 4 | 成本内部分摊（Showback）与异常检测 | 🟠 企业成本可见性/预算风控 | 低 | 中 | 5 | P1 |
| 5 | Webhook 事件目录与 Payload 转换管线 | 🟠 企业集成生态 | 低 | 中 | 6 | P2 |

**推荐启动顺序：** Direction #2（Tracing）→ Direction #1（多后端路由）→ Direction #4（Showback）→ Direction #5（Webhook 平台）→ Direction #3（Schema 系统）

理由：Tracing 是"零成本启动"（OTel 已就绪，只需添加 spans），可在 1 周内完成，立即带来调试效率的提升。多后端路由是架构层面的演进，值得优先投入。Showback 是客户驱动需求（多租户上线后自然产生的需求）。Webhook 平台和 Schema 系统是向下游演进的功能，可以晚一些。

> **与 ROADMAP 的关系：** 本期方向与前十一期形成补充。ROADMAP 关注于"从单实例到生产级系统"的演进，本期的 #1 填补了"多后端统一命名空间"的空白，#2 填补了"可观测性最后一公里"的空白，#3 填补了"元数据管理从自由格式到结构化"的空白，#4 填补了"成本归因的粒度"的空白，#5 填补了"集成枢纽能力"的空白。
