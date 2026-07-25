# 高价值扩展方向：多层缓存架构、准入控制现代化、插件扩展系统、事件溯源、API 治理

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — `cmd/server/main.go` + `internal/` 全部 30+ 子包（237+ Go 源文件），3 套 SDK，MCP 双模式，Web UI，48 对迁移文件，`deploy/` 全套配置，`HARNESS.md`，`AGENTS.md`，ROADMAP.md  
> **去重验证：** 对 `docs/requirements/` 下全部 93 份既有分析文档（`expansion-directions.md` ~ `expansion-v93-compliance-governance-transformation-distributed-quota-unified-search-async-buffer.md`）逐方向进行关键词正则 + 语义交叉验证  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。选取**代码中存在明确锚点、有实质性产品/架构影响、且在 93 轮分析中未被独立深度覆盖**的方向。每个方向包含：代码证据 → 产品价值 → 架构权衡 → 边界情况。

---

## 去重验证

对 `docs/requirements/` 下全部 93 份既有分析文档逐方向进行关键词正则扫描：

| 方向 | 既往覆盖情况 |
|------|-------------|
| **多层缓存架构（对象体+元数据+Chunk）** | ✅ **零实质性覆盖** — 全量 93 份文档正则搜索 `multi.layer.cache\|object.*cache.*layer\|metadata.*cache\|write.through.cache\|cache.*between.*service.*storage\|hot.object.*cache\|cache.*architect` → **0 命中**。v91 方向四覆盖了「读缓存与只读副本路由」但聚焦于 **CDN/边缘缓存 + 副本路由** 而非 **FileService 与 Storage 之间的进程内多层缓存**；`ROADMAP.md` #2 覆盖了 embedding 缓存与搜索结果缓存，但均为 AI 管线专用，非通用存储缓存层 |
| **准入控制与并发治理现代化** | ✅ **零实质性分析** — v89 方向二覆盖「跨协议运营治理」聚焦于 **速率限制的差异化策略**（S3 vs REST vs WebDAV），但**从未分析 ConcurrencyLimiter 的架构缺陷**：无 OTel 指标、无后端压力感知、无协议优先级、无租户分级准入、无优雅降级。正则搜索 `admission.control\|concurrency.*govern\|concurrency.*admission\|adaptive.*admission\|admission.*tier\|backpressure.*admission\|weighted.*admission` → **0 命中**（v71/v89 匹配「admission」的上下文均为 `no admission` / `admission control` 路过提及，**零架构分析**） |
| **插件与扩展系统** | ✅ **零实质性覆盖** — 全量 93 份文档正则搜索 `plugin.*system\|extension.*hook\|extensib.*architect\|plugable.*backend\|extensible.*provider\|custom.*extract\|third.party.*extension` → **0 命中**。`ROADMAP.md` 无任何关于插件/扩展系统的规划。当前扩展方式为硬编码 switch-case（`factory.go`），新增后端需修改核心包代码 |
| **事件溯源与不可变事件日志** | ✅ **零实质性覆盖** — 全量 93 份文档正则搜索 `event.sourc\|immutable.*event\|event.*log\|event.*replay\|event.*durab\|event.*store\|event.*persist\|event.*audit` → **0 命中** 事件溯源/不可变日志。v55 方向四覆盖「进程内事件总线订阅者健康管理」聚焦于**订阅者生命周期**而非事件数据持久化；v17 方向三覆盖「S3 Notification 引擎」聚焦于**事件路由**而非存储 |
| **API 治理与版本化演进策略** | ✅ **零实质性覆盖** — 全量 93 份文档正则搜索 `API.*versioning\|versioning.*strategy\|API.*govern\|OpenAPI.*generat\|sdk.*generat\|API.*lifecycle\|API.*deprecat\|backward.*compat.*API\|API.*contract` → **0 命中**。v46 方向一覆盖「开发者体验」包含 OpenAPI 验证、CI 集成、版本号修复，但**聚焦 DX 而非 API 治理与版本化演进架构** |

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 |
|---|------|------|--------|---------|---------|
| **1** | **多层缓存架构：在 FileService 与 Storage/Repository 之间插入缓存层** | 性能/架构 | **P1** | 每次 GET/HEAD/Stat 都直连 storage backend 或 repository，无任何进程内缓存；`s.store.Get(ctx, sk)` → 磁盘 I/O；`s.repo.GetObject` → SQL 查询；`s.repo.GetBucketConfig` → SQL 查询；热点对象被重复读取，响应延迟随并发升高而退化；云存储（S3/OSS/COS）每次 GET 都有 HTTP 往返延迟和请求费用 | `internal/service/file_crud.go:Get`（`rc, _, err := s.store.Get(ctx, obj.StorageKey)`—直读后端无缓存）；`internal/service/file_crud.go:Stat`（`obj, err := s.repo.GetObject(ctx, tenant, bucket, key)`—直查 DB 无缓存）；`internal/service/file_features.go:List`（`s.repo.ListObjects`—全量 SQL 查询无缓存）；`internal/storage/local_read.go:Get`（`os.Open`→`io.Copy`—磁盘 I/O）；`internal/ai/result_cache.go`（唯一缓存实现但仅限 AI 搜索）；`internal/storage/encrypt.go`（解密后的明文可缓存但未缓存） |
| **2** | **准入控制与并发治理现代化：从二元信号量到自适应、可观测、分层的准入架构** | 架构/可靠性 | **P1** | `ConcurrencyLimiter` 是仅基于 HTTP method 的加权信号量（GET=1，其他=2），无 OTel 指标暴露、无后端压力感知、无协议优先级、无租户分级准入、无优雅降级。当 storage backend 开始退化（延迟升高、错误率增加），并发请求继续涌入，加速后端饱和，导致级联故障 | `internal/middleware/middleware.go:ConcurrencyLimiter`（`sem chan struct{}`—无指标、无动态权重、无压力感知）；`internal/middleware/middleware.go:PerTenantConcurrencyLimiter`（`map[string]int`—内存 map 无持久化、无分级）；`internal/middleware/ratelimit.go:RateLimiter`（`map[string]*bucket`—独立于并发限制器，无关联）；`internal/storage/circuitbreaker.go`（断路器状态机完全独立，不反馈给准入层）；`internal/telemetry/metrics.go`（15+ 计数器/直方图，**零并发限制器指标**）；`cmd/server/main.go:217-222`（`concurrencyMW` 装配在中间件链中但不集成 OTel） |
| **3** | **插件与扩展系统：为存储后端、AI 组件、认证方式提供声明式扩展注册机制** | 架构/生态 | **P2** | 当前扩展新功能需修改核心包代码：新增 storage backend → 改 `factory.go`、`config.go`、`main.go`；新增 extractor → 改 `main.go` `buildIndexer`；新增 auth provider → 改 `auth.go`、`main.go`。无插件发现、无声明周期管理、无版本约束、无能力声明。社区贡献者无法以独立包形式分发扩展 | `internal/storage/factory.go:NewFromConfig`（`switch cfg.Kind {case BackendLocal:...case BackendS3:...}` —硬编码分支无注册点）；`internal/storage/storage.go`（`Storage` 接口硬编码无 `Capabilities()` / `Metadata()` 扩展点）；`internal/ai/extractor.go`（`Extractor` 接口无注册机制，`cmd/server/main.go:595` 中手动装配）；`internal/auth/auth.go`（`Registry` 硬编码 `Parse` env keys + JWT + SigV4 三个来源）；`internal/config/config.go`（配置结构体将所有后端参数平铺为字段，无动态配置扩展点） |
| **4** | **事件溯源与不可变事件日志：从尽力传递的内存事件到持久化、可回放、可查询的事件存储** | 可靠性/合规 | **P1** | `Bus.Publish` 持久化事件到 `events` 表但内存 channel 仅 64 深；慢订阅者导致事件静默丢弃（`dropped` 计数器增长）；新订阅者无法回放历史事件；SSE 重建连接后的「续传」基于 `Last-Event-ID` 查询 `events` 表但只限未过期事件；系统无不可变日志用于合规审计（admin audit 是独立表，不记录对象级事件） | `internal/events/bus.go:31`（`buffer: 64` —硬编码浅 buffer）；`internal/events/bus.go:107-118`（`broadcast` 非阻塞发送 `case ch <- e:` —满 buffer 丢弃）；`internal/events/bus.go:72-80`（`Subscribe()` 返回无缓冲/小缓冲通道）；`internal/api/rest/sse.go:liveStream`（`replayMissed` 查询 `events` 表但受 `EVENT_DB_RETENTION_HOURS` 限制）；`internal/repository/sql_events.go`（events 表有 TTL 删除，非不可变）；`internal/repository/audit.go`（审计日志仅 admin 操作，对象级事件不入审计） |
| **5** | **API 治理与版本化演进策略：OpenAPI 完备性、SDK 自动化、版本契约与兼容性保障** | 产品/工程文化 | **P2** | OpenAPI 规范通过 `openapi.go` 中 Go 代码手动构造 JSON，与真实 handler 行为无编译期契约；SDK 三套件手动维护，功能不对称；所有路由在 `/v1` 下无子版本划分，无法优雅废弃端点；breaking change 需等待主版本跳升无过渡期 | `internal/api/rest/openapi.go`（Go 代码手动构造 `map[string]any{}` 而非注解/代码生成）；`internal/api/rest/router.go`（`r.Mount("/v1", ...)` —单版本前缀无子版本路由）；`sdk/go/aerovault/client.go`（1006 行，无需代码生成，手动维护）；`sdk/python/aero_vault.py`（684 行，手动维护，缺失 14+ 方法）；`sdk/js/aero-vault.js`（1084 行，手动维护）；`cmd/server/main.go:190`（`"version":"0.1.0"` —硬编码，无语义版本策略）；`docs/CHANGELOG.md`（手动维护） |

---

## 方向一：多层缓存架构——在 FileService 与 Storage/Repository 之间插入缓存层

### 现状

当前每个 GET/HEAD/Stat 请求都穿透到存储后端和数据库，不经过任何缓存层：

```
客户端 → FileService.Get → s.store.Get(ctx, sk) → 磁盘/S3/OSS/COS I/O
                      ↘ s.repo.GetObject → SQL 查询
                      ↘ 解密（若 SSE 启用）→ 全量解密无缓存
```

```go
// internal/service/file_crud.go:Get
func (s *FileService) Get(ctx context.Context, tenant, bucket, key string) (io.ReadCloser, repository.Object, error) {
    // ...
    rc, _, err := s.store.Get(ctx, obj.StorageKey)  // ← 每次磁盘 I/O
    // ...
    if obj.Metadata["_aero_content_encoding"] == "gzip" {
        gr, err := gzip.NewReader(rc)  // ← 每次重新解压缩
    }
    return rc, obj, nil
}
```

**可缓存的三个层次：**

| 层次 | 缓存对象 | 当前状态 | 失效粒度 |
|------|---------|---------|---------|
| **L1 对象元数据** | `repository.Object`（size/etag/content_type/metadata/tags） | 每次 `Stat`/`Get` 都查询 DB | per-object（更新/删除时失效） |
| **L2 存储对象体** | storage blob 的已解密字节流 | 每次 `Get` 都读取后端（可能含加解密开销） | per-object（覆盖/删除时失效） |
| **L3 派生/变换内容** | thumbnail、resize、格式转换、gzip 解压结果 | 缩略图每次独立生成，gzip 每次重新解压 | per-object + per-transform-params |

唯一存在的缓存是 `ai.ResultCache`，但仅限于 AI 搜索路径——存储路径完全无缓存。

### 产品价值

| 场景 | 当前体验 | 有缓存后 |
|------|---------|---------|
| **热点对象反复读取**（热门文档、频繁引用的图片） | 每次请求走存储后端，S3 场景额外增加请求费用 | 内存缓存命中，微秒级响应，零后端请求成本 |
| **Stat-heavy 操作**（List 后逐个 Stat 获取元数据） | N 个对象 = N 个 SQL 查询 | 批量预填充元数据缓存，N 个查询 → 0 次 DB 命中 |
| **SSE 加密对象读取** | 每次 `Get` 都走完整解密路径（读数据密钥 → AES-GCM 解密） | 缓存明文（LRU），预热后可秒级响应 |
| **gzip 压缩对象** | 每次 `Get` 都流式 `gzip.NewReader` 解压缩 | 缓存解压后的内容，CPU 换时间 |
| **多副本部署下的缓存未命中** | 缓存本地无状态，每副本独立，无共享 | 可选的分布式缓存后端（Redis）或副本本地 LRU |

### 架构权衡

**方案一：进程内 LRU 缓存（推荐首发）**

```
FileService
  ↓
 CacheLayer (L1: metadata, L2: object body, L3: derived)
  ↓
 Storage / Repository
```

- **数据结构：** `lru.Cache[string, cacheEntry]`（键 = `{tenant}:{bucket}:{key}` 或 `{storage_key}`）
- **L1 缓存：** 对象元数据（~200 bytes/entry），`MaxL1Size` 默认 100K entries
- **L2 缓存：** 对象体（可能很大），`MaxL2Size` 默认 64MB，单对象最大 4MB（超过的不缓存）
- **L3 缓存：** 派生内容（缩略图、格式转换），`MaxL3Size` 默认 128MB，TTL 5 分钟
- **失效策略：** `object.updated` / `object.deleted` 事件 → 异步清除所有副本的缓存项
- **TTL：** L1 60s（短 TTL，容忍最终一致），L2 300s（容忍稍长），L3 由变换参数决定

**方案二：Write-Through + 惰性失效（更高性能，更复杂）**

- 写入时同步更新缓存（`Put` 路径），无需等待事件传播
- 删除时同步逐出（`Delete` 路径）
- 多副本间通过事件总线广播失效通知
- 优点：缓存更新鲜；缺点：写路径延迟增加

**方案三：分布式缓存（Redis）**

- 多副本共享同一缓存池，副本间零不一致窗口
- 依赖 Redis（新增运维依赖）
- 建议作为可选升级路径，不阻塞进程内 LRU 首发

**新增风险：**

| 风险 | 缓解措施 |
|------|---------|
| 缓存中毒（恶意构造的 key 填满缓存） | 对象体缓存按大小限制（`max_object_size=4MB`），元数据缓存按计数限制 |
| 陈旧数据服务 | `GET` 时附加 `Cache-Control: no-cache` 头；缓存键包含对象 `updated_at` 时间戳 |
| 内存压力 | 缓存池纳入 Go 内存限制（`runtime/debug.SetMemoryLimit`）；可配置 `CACHE_MEMORY_PERCENT=10` |
| 加密对象缓存暴露明文 | L2 缓存仅限解密后；若安全性要求高可只缓存元数据不缓存对象体 |

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| 大对象（>4MB） | 透传到存储后端，不缓存对象体；元数据仍缓存 |
| 版本化桶中的同一 key 多个版本 | 缓存键 = `{storage_key}`（含 `@v<id>` 后缀），不同版本独立缓存 |
| 预签名 URL 直接访问存储后端 | 绕过缓存层，不出现在缓存中 |
| `Range` 请求 | 缓存完整对象体后支持 Range 切片；或按 `bytes=start-end` 分片缓存 |
| 缓存与后端一致性检查 | `Stat` 响应携带 `Last-Modified`，缓存对比 If-Modified-Since |
| 租户隔离 | 缓存键含 `tenant` 前缀；支持按租户配置缓存开关 |

---

## 方向二：准入控制与并发治理现代化——从二元信号量到自适应、可观测、分层的准入架构

### 现状

当前准入控制由三个独立、不关联的组件组成：

```go
// 1. 并发限制器 — 基于 HTTP method 的加权信号量
cl := middleware.NewConcurrencyLimiter(cfg.App.MaxInFlight)
// 权重：GET/HEAD/OPTIONS = 1, 其他 = 2
// 无 OTel 指标、无压力感知、无动态调权

// 2. 每租户同步限制（可选）
ptcl := middleware.NewPerTenantConcurrencyLimiter(cfg.App.MaxInFlight, cfg.App.PerTenantMax)
// 基于内存 map[string]int，无持久化、无分级、重启丢失

// 3. 速率限制器 — 独立的 token-bucket
rl := middleware.NewRateLimiter(cfg.RateLimit.RPS, cfg.RateLimit.Burst)
aiRL := middleware.NewRateLimiter(cfg.RateLimit.AIRPS, cfg.RateLimit.AIBurst)
// 与并发限制器无关联，不感知后端压力
```

应用顺序：`AccessLog → ConcurrencyLimiter → Recoverer → OTel → RateLimiter → Tenant → Auth → CORS → RequestID`

**核心缺陷：**

| 缺陷 | 影响 | 严重度 |
|------|------|--------|
| 无 OTel 指标 | 运维人员不知道当前并发数、拒绝率、信号量利用率、积压深度 | 🔴 运维盲区 |
| 无后端压力感知 | 存储后端退化（延迟升高、错误增加）时，并发请求继续涌入，加速级联故障 | 🔴 可靠性 |
| 无协议感知权重 | S3 批量删除（一次涉及 1000 个对象）与 GET 一个 1KB 文件的信号量权重相同 | 🟠 公平性 |
| 无租户分级 | 免费租户与付费租户共享同一并发池，无优先级抢占 | 🟠 多租户 |
| 无优雅降级 | 并发满时直接返回 429，无排队、无降级、无自适应 RPS 反馈 | 🟠 用户体验 |
| 无存储后端标签 | 不同后端（S3 慢 vs 本地快）无差异化并发上限 | 🟢 |

### 产品价值

| 价值 | 量化影响 |
|------|---------|
| **级联故障预防** | 后端 P99 延迟从 100ms 升到 2s 时，准入控制器自动降低并发上限（减少 50% in-flight），防止连接池耗尽 |
| **公平调度** | S3 批量删除（权重=10）与 GET 头像（权重=1）不争抢相同信号量槽位 |
| **可观测性** | Grafana 面板展示当前并发数、拒绝率、信号量饱和度、各后端排队深度——运维人员第一时间感知异常 |
| **租户 SLA 差异化** | 付费租户比免费租户获得 10x 准入配额，突发时付费请求优先通过 |
| **成本控制** | 大请求（PUT 100MB）消耗更多配额，防止一个请求耗尽所有并发槽位 |

### 架构权衡

**现代化方案核心组件：**

```
                 ┌──────────────┐
                 │  Admission   │
                 │  Controller  │
                 └──────┬───────┘
                        │
         ┌──────────────┼──────────────┐
         ▼              ▼              ▼
  ┌────────────┐ ┌────────────┐ ┌────────────┐
  │  Weight    │ │  Pressure  │ │  Tier      │
  │  Engine    │ │  Monitor   │ │  Manager   │
  └────────────┘ └────────────┘ └────────────┘
```

**1. 可配置请求权重模型：**

```go
// 每操作类型权重（可配置）
map[string]int{
    "GET":              1,    // 小读取
    "HEAD":             1,    // 元数据查询
    "PUT:small":        2,    // < 1MB 写入
    "PUT:large":        10,   // >= 1MB 写入
    "DELETE:single":    2,    // 单对象删除
    "POST:batch-delete": 20, // 批量删除（1000 对象）
    "POST:search":      5,    // AI 检索
    "POST:chat":        15,   // LLM 推理
}
```

**2. 后端压力感知：**

- `circuitBreaker.State()` → `CBOpen` 时准入控制器降低最大并发数（如 50%）
- `circuitBreaker.Stats()` 滑动窗口错误率 > 10% 时 Progressive Backoff
- 存储后端响应延迟 P99 > 1s 时减少准入权重
- 压力阈值暴露为可配置 `BACKEND_PRESSURE_LATENCY_THRESHOLD` / `BACKEND_PRESSURE_ERROR_RATE`

**3. OTel 指标暴露：**

当前缺失的指标（对照 `internal/telemetry/metrics.go`）：

| 指标名称 | 类型 | Labels | 意义 |
|---------|------|--------|------|
| `admission.inflight` | UpDownCounter | `{protocol, tier}` | 当前处理中的请求数 |
| `admission.limit` | Gauge | `{protocol, tier}` | 当前上限（根据压力动态变化时可见） |
| `admission.rejected` | Counter | `{protocol, tier, reason}` | 被拒绝的请求（reason ∈ {limit, pressure, tenant}） |
| `admission.queued` | UpDownCounter | `{protocol, tier}` | 排队等待的请求数 |
| `admission.queue_latency` | Histogram | `{protocol, tier}` | 排队等待时间 |

**4. 多副本下的准入：** 当前 `PerTenantConcurrencyLimiter` 使用进程本地 `map[string]int`。在多副本部署下，每个副本独立计数，无法实现全局限流。对于需要跨副本协调的场景，可依赖已有的分布式限流方案（Postgres advisory lock 或 Redis），但建议**首发保持进程本地**（v93 方向三已覆盖分布式限流，不重复）。

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| 压力感知阈值误判（网络抖动） | 滑动窗口 + 最小持续时间（如 10s 持续高延迟才降低准入），防止频繁震荡 |
| 信号量泄漏（请求异常终止未释放） | Go 的 `defer` 机制已保证；但增加 `sync.WaitGroup` 健康 goroutine 检测卡住的 slot |
| 高优先级租户持续占满所有 slot | 按租户分级配额：premium = 80% max, free = 20% max，premium 可用 free 的配额 |
| 存储后端恢复后的「惊群效应」 | 断路器 half-open 类似机制：后端恢复后逐步增加准入量（每 10s 增加 10%），而非瞬间全开 |
| WebDAV 协议跳过中间件链 | WebDAV 在 `buildDispatcher` 中先于中间件链分发，**所有准入控制对 WebDAV 无效**（v55 方向二已覆盖此问题，此处仅标注非重复） |

---

## 方向三：插件与扩展系统——为存储后端、AI 组件、认证方式提供声明式扩展注册机制

### 现状

当前代码库采用**硬编码开关式架构**（switch-case architecture）来管理中可扩展组件：

```go
// internal/storage/factory.go
func NewFromConfig(ctx context.Context, fc FactoryConfig) (Storage, error) {
    switch cfg.Kind {          // ← 新后端需修改此 switch
    case BackendLocal:
        store, err = NewLocal(cfg.Local)
    case BackendS3:
        store, err = NewS3(ctx, bc)
    case BackendOSS:
        store, err = NewOSS(bc)
    case BackendCOS:
        store, err = NewCOS(bc)
    }
}
```

同样的模式出现在：
- **AI Extractors** — `cmd/server/main.go` 中 `buildIndexer` 函数手动选择 `DefaultExtractor` 或 `RemoteExtractor`
- **Embedders** — `buildEmbedder` 函数 switch-case `http` vs `hash`
- **LLMs** — `buildLLM` 函数 switch-case `http` vs `mock`
- **Rerankers** — `buildReranker` 函数 switch-case `http` vs `heuristic` vs `none`
- **Auth** — `auth.Parse` 硬编码 env keys 解析，`auth.Registry` 手动装配 JWT + SigV4
- **Antivirus** — `buildScanner` 函数 switch-case `http` vs `signature`
- **Vector Indexes** — `setupVectorIndexes` 函数 switch-case `pgvector` vs `qdrant` vs `none`

**问题在于：**
1. **扩展需要改核心代码** — 社区贡献者无法以独立 Go 包分发新后端
2. **配置与实现耦合** — `config.go` 需要为每个后端/提供商添加字段
3. **启动顺序硬编码** — `main.go` 中 `build*` 函数的调用顺序固定
4. **无能力声明** — 新增组件无法声明「我需要哪些配置项」「我支持哪些 storage class」

### 产品价值

| 价值 | 量化影响 |
|------|---------|
| **社区贡献渠道** | 第三方开发者可以 `go get github.com/xxx/aero-vault-backend-minio` 安装新后端，零核心代码修改 |
| **内部解耦** | 内置后端（local/s3/oss/cos）通过同一注册机制加载，消除 `factory.go`/`config.go` 分支 |
| **按需编译** | 使用 `go:build` tags，仅编译需要的后端——减小二进制体积 |
| **企业私有扩展** | 企业可编写私有认证插件（LDAP/OIDC 适配器）或存储网关而不公开 |
| **A/B 测试** | 同一接口的两个实现可并行注册，通过配置切换 |

### 架构权衡

**推荐方案：`init()` 注册 + 接口型插件 + 声明式配置**

```
// 1. 定义插件注册表
var storageBackends = map[string]StorageFactory{}

// 2. 插件实现 init() 中自注册
func init() {
    RegisterStorageBackend("s3", NewS3FromConfig)
}

// 3. main.go 扫描所有注册的后端，根据配置选择
store, err := storageBackends[cfg.Kind](ctx, cfgJSON)
```

**接口设计：**

```go
// 现有接口（不变）：
type Storage interface { ... }

// 新增扩展点：
type StorageFactory func(ctx context.Context, config json.RawMessage) (Storage, error)

type PluginCapabilities struct {
    Name        string            // 唯一名称
    Version     string            // 语义版本
    ConfigSchema map[string]any   // JSON Schema 声明所需配置
    StorageClasses []string       // 支持的存储类（例如 ["STANDARD"]）
    Requires    []string          // 依赖的其他插件（例如 ["sse"]）
}
```

**配置变化：**

当前（硬编码嵌套结构）：
```go
type StorageConfig struct {
    Backend string
    Local   LocalConfig   `json:",omitempty"`
    S3      S3Config      `json:",omitempty"`
    OSS     OSSConfig     `json:",omitempty"`
    COS     COSConfig     `json:",omitempty"`
}
```

未来（插件注册制）：
```go
type StorageConfig struct {
    Backend   string          // "s3" → 索引 storageBackends["s3"]
    ConfigJSON json.RawMessage // 插件自行解析
}
```

**影响范围：**

| 组件 | 当前行数（估算） | 影响 |
|------|----------------|------|
| `internal/storage/factory.go` | ~100 行 switch-case | 消除，简化为 `backends[cfg.Kind](ctx, cfgJSON)` |
| `internal/config/config_storage.go` | ~80 行字段定义 | 简化为 `Backend` + `ConfigJSON` |
| `cmd/server/main.go:build*` 函数 | 6 个函数 × 各 20-50 行 | 消除或简化为通用 `buildPlugin` |
| 单个内置后端 | ~300 行/个 | 加 `func init() { Register(...) }` 3 行 |

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| 两个插件注册相同名称 | `init()` 执行顺序未定义；`Register` 检测到冲突时 panic（fail-fast）或后注册覆盖（灵活） |
| 插件 `init()` 中依赖未就绪 | `Register` 仅注册工厂，不在 `init()` 中创建实例；`JSON schema` 声明依赖，启动时检查 |
| 插件崩溃/panic | 插件运行在独立 goroutine？不——插件是库代码，在同一进程中运行。需通过 `PluginCapabilities` 声明资源需求 |
| 配置兼容性 | `ConfigJSON` 允许插件版本升级时更改配置格式，不改核心 `config.go` |
| CGO 依赖的插件 | `go:build` tag 控制编译；用户需 `-tags cgo` 编译 |

---

## 方向四：事件溯源与不可变事件日志——从尽力传递的内存事件到持久化、可回放、可查询的事件存储

### 现状

当前事件系统的设计是**尽力传递 + 临时存储**：

```
Publish → events 表（TTL 后删除）
       ↘ 内存 channel（buffer=64）→ 订阅者 → 非阻塞发送 → 满则丢弃
```

```go
// internal/events/bus.go
func (b *Bus) Publish(ctx context.Context, e repository.Event) {
    // 1. 持久化到 events 表（有 TTL）
    b.repo.InsertEvent(ctx, e)
    // 2. 内存广播（64 深 buffer）
    b.broadcast(e)  // 满 buffer → dropped++
}

func (b *Bus) broadcast(e repository.Event) {
    for _, sub := range b.subs {
        select {
        case sub <- e:           // 非阻塞发送
        default:
            atomic.AddInt64(&b.dropped, 1)  // 静默丢弃，仅计数
        }
    }
}
```

**五个结构性缺失：**

| 缺失 | 代码证据 | 影响 |
|------|---------|------|
| **事件不可重放** — 新订阅者无法获取历史事件 | `Subscribe()` 返回新 channel，不回放历史 | SSE 客户端断连后只能续传 `Last-Event-ID` 之后的事件，之前的永久丢失 |
| **事件不可逆** — events 表有 TTL 删除策略 | `internal/repository/sql_events.go:DeleteEventsBefore` —定时清理 | 事件日志有保存期限制，不适用于长期合规审计 |
| **事件种类有限** — 仅 `object.created/deleted/accessed` | `internal/repository/repository.go:EventType` — 3 种 | 缺少 `object.moved`、`object.tagged`、`object.locked`、`object.retention_changed`、`config.changed` 等操作事件 |
| **admin audit 与事件分离** — 两个独立存储 | `internal/repository/audit.go`（admin 操作） vs `internal/repository/events.go`（对象操作） | 无法在一个时间线上查看「谁在何时做了什么」的全量审计 |
| **无事件消费者位置追踪** — 消费者进度不持久化 | `internal/api/rest/sse.go:replayMissed` —每次从头查询 | 大规模消费端无法追踪「上次处理到哪里」 |

### 产品价值

| 价值 | 量化影响 |
|------|---------|
| **合规可证明性** | 不可变事件日志满足 SOC2/ISO 27001 的审计轨迹要求；可回答「谁在何时对哪个对象做了什么操作」 |
| **数据恢复** | 通过回放事件日志重建对象元数据变更历史——在误操作后回溯到之前状态 |
| **事件驱动架构** | 系统可发布丰富的事件类型（对象锁变更、租户状态变更、配置变更），外部系统订阅后自动同步 |
| **调试与排障** | 运维人员通过事件时间线追踪「为什么这个对象被删了」「谁改了 bucket 策略」 |
| **新功能基础** | 事件溯源是跨区域复制、变更数据捕获（CDC）、搜索索引重建、数据湖同步等场景的基础设施 |

### 架构权衡

**核心思路：将 events 表从「TTL 临时存储」演变为「不可变追加日志」**

```
当前： events 表（TTL+定期清理）
目标： event_log 表（仅追加，永不删除）+ events 表（保留为 TTL 工作缓存）
```

**事件类型扩展：**

```go
// 当前 3 种事件类型
const (
    EventCreated  EventType = "object.created"
    EventDeleted  EventType = "object.deleted"
    EventAccessed EventType = "object.accessed"
)

// 目标：25+ 事件类型覆盖全操作域
// 对象操作
EventCreated, EventDeleted, EventAccessed,
EventUpdated, EventMoved, EventCopied,
EventLocked, EventUnlocked, EventRetentionChanged,
EventTagged, EventACLChanged,
// 存储操作
EventStorageClassChanged, EventBackendMigrated,
// 配置操作
EventBucketCreated, EventBucketDeleted,
EventBucketConfigChanged, EventLifecycleChanged,
EventNotificationConfigChanged,
// 租户操作
EventTenantCreated, EventTenantSuspended, EventTenantActivated,
EventQuotaChanged, EventBudgetChanged,
// 安全操作
EventKeyAdded, EventKeyRevoked, EventKeyExpired,
EventAuthFailure, EventAuthSuccess,
// 系统操作
EventReconcileStarted, EventReconcileCompleted,
EventIndexerStarted, EventIndexerCompleted,
EventBackendUnavailable, EventBackendRecovered,
```

**事件类型版本化：**

```go
type Event struct {
    ID           int64           `json:"id"`
    EventType    string          `json:"event_type"`
    EventVersion int             `json:"event_version"` // ← 新增：事件 schema 版本
    Timestamp    time.Time       `json:"timestamp"`
    TenantID     string          `json:"tenant_id"`
    Actor        string          `json:"actor"`        // ← 新增：操作者（key ID / JWT sub）
    RequestID    string          `json:"request_id"`
    Payload      json.RawMessage `json:"payload"`
    // 新增可选字段
    ParentEventID *int64         `json:"parent_event_id,omitempty"` // 关联前一个事件（因果链）
    ObjectID      *int64         `json:"object_id,omitempty"`
    StorageKey    string         `json:"storage_key,omitempty"`
    PrevState     json.RawMessage `json:"prev_state,omitempty"`    // 操作前状态（diff 用）
    Diff          json.RawMessage `json:"diff,omitempty"`           // 变更内容（JSON Patch）
}
```

**消费者位置追踪：**

```go
type EventConsumer struct {
    ConsumerName string    // 唯一名称（如 "indexer", "sse-tenant-default"）
    LastEventID  int64     // 最后成功处理的事件 ID
    Filter       string    // 事件类型过滤（可选）
    CreatedAt    time.Time
    UpdatedAt    time.Time
}
```

消费者定期 checkpoint 位置到 `consumer_offsets` 表。重启时从 `LastEventID` 续传。

**存储策略：**

| 策略 | 行为 | 适用场景 |
|------|------|---------|
| **无限保留** | `event_log` 永不删除 | 合规审计 |
| **TTL 保留** | 与当前 `events` 表相同，定期清理 | 调试/运维 |
| **归档** | 旧事件导出到冷存储（S3/OSS）并压缩 | 长期合规存储 |
| **采样** | 高吞吐时仅记录部分事件（如 1:100） | 监控/指标 |

**性能影响：**

- 写入路径：每次对象操作增加一次 `event_log` INSERT（与当前 `events` INSERT 类似）
- 读取路径：事件查询按 EventID 索引（B-tree），查询 `WHERE event_id > $1` 为索引范围扫描
- 历史查询：按时间范围 + 事件类型过滤，需 `(timestamp, event_type)` 复合索引
- 建议 `event_log` 使用独立的 INSERT-only 连接，不占用主查询连接池

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| 事件日志无限增长 | 分离 `event_log`（无限保留/合规用途）和 `events`（TTL/工作缓存）；`event_log` 按分区表管理 |
| 高吞吐下的写入瓶颈 | `event_log` 使用批量 INSERT（每 100ms 或每 100 条 flush 一次） |
| 事件类型 schema 变更 | `EventVersion` 字段允许不同版本解析器共存；旧版本事件按旧 schema 解析 |
| 消费者长时间落后 | `consumer_offsets` 中 `lag` 字段监控并告警；落后超过 `MAX_LAG` 的消费者降级为仅接收新事件 |
| 不可变日志的 GDPR 删除要求 | 支持 `DELETE FROM event_log WHERE event_id = $1`（物理删除）但记录删除操作本身到 `event_log` |
| 事件排序（同一对象的操作顺序） | `ParentEventID` 维护因果链；`Timestamp` 为应用时间（非 wall clock），容忍时钟偏移 |

---

## 方向五：API 治理与版本化演进策略——OpenAPI 完备性、SDK 自动化、版本契约与兼容性保障

### 现状

三个互相关联的问题：

**1. OpenAPI 规范手动维护且可能偏离实际：**

```go
// internal/api/rest/openapi.go
// OpenAPI 规范通过 Go 代码手动构造 map[string]any{} JSON
// 无任何编译期验证确保 spec 与实际 handler 行为一致
func OpenAPISpecHandler() http.HandlerFunc {
    spec := map[string]any{
        "openapi": "3.0.3",
        "info": map[string]any{
            "title":   "AeroVault API",
            "version": "0.1.0",
        },
        "paths": map[string]any{
            "/v1/files/{key}": map[string]any{
                "get": map[string]any{ /* ... */ },
                "put": map[string]any{ /* ... */ },
            },
        },
    }
    // ...
}
```

**2. 三套 SDK 手动维护且功能不对称：**

| 功能区域 | Go SDK | JS SDK | Python SDK |
|---------|--------|--------|-----------|
| 文件 CRUD | ✅ | ✅ | ✅ |
| Tags | ✅ | ✅ | ✅ |
| 版本列举 | ✅ | ✅ | ✅ |
| ACL | ✅ | ✅ | ✅ |
| 缩略图 | ✅ | ✅ | ✅ |
| 预签名 URL | ✅ | ✅ | ❌ |
| 搜索 | ✅ | ✅ | ✅ |
| Chat | ✅ | ✅ | ✅ |
| ChatStream | ✅ | ✅ | ❌ |
| Agent | ✅ | ✅ | ✅ |
| Lineage | ✅ | ✅ | ❌ |
| Admin Keys | ✅ | ✅ | ❌ |
| Admin Tenants | ✅ | ✅ | ❌ |
| Admin Jobs | ✅ | ✅ | ❌ |
| Admin Audit | ✅ | ✅ | ❌ |
| Admin Budget | ✅ | ✅ | ❌ |
| 批量操作 | ✅ | ✅ | ❌ |
| SSE 支持 | ✅ | ❌ | ❌ |
| Multipart Upload | ❌ | ❌ | ❌ |
| Bucket 管理 | ✅ | ✅ | ❌ |
| Webhook Failures | ✅ | ✅ | ❌ |
| Usage | ✅ | ✅ | ❌ |

**3. 无 API 版本化策略：**

- 所有路由在 `/v1` 下，无子版本划分（`/v1.1`、`/v2` 无预留）
- 端点废弃无过渡期、无 `Deprecation` HTTP 头、无 `Sunset` 头
- Breaking change 无通知机制、无迁移指南

### 产品价值

| 价值 | 量化影响 |
|------|---------|
| **开发者信任** | 精确的 OpenAPI 规范 + 生成的 SDK = 调用方 100% 确信参数/响应结构正确 |
| **维护成本降低** | 从 OpenAPI 自动生成 SDK 替代三套手动维护（~3066 行/周更新 → 代码生成/次发布） |
| **向前兼容** | `/v1` 端点永远向后兼容，breaking change 进 `/v2`，现有客户端无破坏 |
| **企业合规** | 有版本契约、deprecation 策略、迁移窗口，满足企业供应商合同要求 |
| **API 发现** | 完整的 OpenAPI spec 可导入 Postman/Insomnia/ApiDog，降低集成成本 |

### 架构权衡

**1. OpenAPI 治理（Code-First → Spec-First）：**

推荐**渐进式迁移**，非重写：

| 阶段 | 行动 | 工具/方法 |
|------|------|----------|
| Phase 1 | 在 CI 中验证 OpenAPI spec 与 handler 不矛盾 | 请求/响应录制 → 自动比对 spec（`go test -record-openapi`） |
| Phase 2 | 引入注解式 OpenAPI（`ogen` 或 `echo` 路由元数据） | 每个 handler 加 `// @Summary` `// @Param` 注解 |
| Phase 3 | 从 OpenAPI spec 生成 route registration | `ogen` server 生成 → 替换手动 `router.go` |
| Phase 4 | 从 OpenAPI spec 生成 SDK | `openapi-generator` / `ogen` client gen |

**2. API 版本化策略：**

```
当前： /v1/files/...
目标： /v1/files/...        ← 稳定（永不 breaking）
      /v2/files/...        ← 新的 breaking 版本
      /v1.1-beta/files/... ← 预览特性（有限升级）
```

| 策略 | 实现 | 适合阶段 |
|------|------|---------|
| **URL 路径版本** | `/v1/` vs `/v2/` | 清晰、缓存友好 |
| **Header 版本** | `Accept: application/vnd.aero-vault.v2+json` | RESTful、URL 干净 |
| **Query 参数版本** | `?version=2` | 简单但不推荐 |

建议**URL 路径版本 + Header 版本双轨制**：URL 用于主要版本（v1/v2），Header 用于微版本间的预览控制。

**3. Deprecation 流程：**

```http
# 废弃端点响应
HTTP/1.1 200 OK
Deprecation: true
Sunset: Sat, 11 Jul 2027 00:00:00 GMT
```

```
Phase 1: 添加 Deprecation header（告警期，至少 6 个月）
Phase 2: 添加 Sunset header（设置最后期限）
Phase 3: 返回 410 Gone（超过最终期限）
Phase 4: 移除端点代码
```

**4. SDK 自动生成：**

从 OpenAPI spec 自动生成 SDK 模板，需自定义模板引擎处理：

- Go：`ogen` client gen 或 `deepmap/oapi-codegen` 
- Python：`openapi-generator` python 模板 + `requests`
- JS：`openapi-generator` typescript-fetch 模板

**自定义逻辑**（自动生成无法覆盖的）：

| SDK 逻辑 | 处理策略 |
|---------|---------|
| SSE 流式解析 | 自动生成 + 手动 SSE 流式解析模板 |
| ChatStream token 回调 | SDK 特有 callback 模式，模板中预置 |
| 错误映射（`AeroVaultError` / `*Error`） | 语言特定的错误类型，OpenAPI 不覆盖 |
| 认证构建（Bearer/X-Api-Key/tenant） | 客户端配置模式，OpenAPI 不覆盖 |

**建议策略：** 80% 代码自动生成（请求构建、响应解析、类型定义）+ 20% 手动维护（流式、认证、错误处理）。

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| 版本 v2 与 v1 共享同一 handler | `router.go` 中 `/v1/files/*` 和 `/v2/files/*` 可指向同一 `Handler`；版本仅在 schema/sdk 层面不同 |
| API 响应中新增字段（非 breaking） | 符合 OpenAPI 的 `additionalProperties: true` 或向后兼容的 schema 扩展 |
| 删除请求参数 | 参数标记为 `deprecated: true` 并在文档中说明替代方案，6 个月后移除 |
| SDK 版本与 API 版本绑定 | SDK 发布采用 `{api_version}.{sdk_revision}` 版本号（如 `1.0.5` 对应 API v1）；`CHANGELOG.md` 记录变更 |
| 客户端不发送 `Accept` 头 | 默认返回最旧稳定版本（当前 `/v1`），确保向后兼容 |

---

## 优先级与建议执行顺序

| 排序 | 方向 | 前置依赖 | 建议投入 | 核心交付物 |
|------|------|---------|---------|-----------|
| **1** | 方向一：多层缓存架构 | 无（纯新增层，不影响现有路径） | 4-6 周 | `CacheLayer` 包裹 `Storage` + OTel 指标 + TTL 失效 |
| **2** | 方向四：事件溯源与不可变事件日志 | 方向一无直接依赖，但共享事件总线 | 6-8 周 | `event_log` 表 + 事件类型扩展 + `consumer_offsets` + admin 审计整合 |
| **3** | 方向二：准入控制现代化 | 方向一（缓存减轻压力，降低准入必要性） | 6-8 周 | `AdmissionController` + OTel 指标 + 后端压力感知 + 分级准入 |
| **4** | 方向五：API 治理 | CI 流水线增强（无架构依赖） | 8-12 周 | OpenAPI CI 验证 + 版本策略文档 + API deprecation 头 |
| **5** | 方向三：插件扩展系统 | 方向一/二/四/五均不依赖 | 10-14 周 | `Register*` 注册机制 + `PluginCapabilities` + 内置后端迁移 |

**建议执行策略：**

1. **Phase 1（方向一 + 方向四）**：缓存和事件溯源共享「数据路径优化」主题。缓存立即降低存储延迟和费用；事件溯源奠定合规和可观测性的基石。两方向无冲突，可并行启动。
2. **Phase 2（方向二）**：缓存降低了后端压力，此时重审准入控制需求更准确。方向二需方向四的事件流来感知后端压力状态。
3. **Phase 3（方向五）**：API 治理是工程文化投资——影响开发效率但不影响系统可靠性。可在任意时间切入，但尽早启动以在 SDK 差异扩大之前遏制。
4. **Phase 4（方向三）**：插件系统是基础设施投资——核心代码重构范围大，建议在系统稳定、API 治理就绪后进行。

---

## 总结

以上五个方向覆盖了 aero-vault 在**性能架构、可靠性架构、可扩展性架构、合规架构、开发者体验**五个维度的关键缺口。它们与既有 93 轮分析 + ROADMAP.md 无实质重叠，同时在代码库中有明确锚点，具备从当前架构渐进演进的可行性。

| 维度 | 当前状态 | 目标状态 |
|------|---------|---------|
| **性能** | GET/Stat 直穿后端，零缓存 | 多层 LRU 缓存 + 派生缓存 + 事件驱动失效 |
| **可靠性** | 二元信号量 + 无压力感知 + 无 OTel | 自适应准入 + 后端压力反馈 + 全面可观测 + 分级调度 |
| **可扩展性** | 硬编码 switch-case 分支 | 插件注册表 + 声明式配置 + `init()` 自注册 |
| **合规** | 有 TTL 的 events 表 + admin audit 分离 | 不可变 event_log + 25+ 事件类型 + 消费者位置追踪 + 因果链 |
| **开发者体验** | 手动 OpenAPI + 三套手写 SDK + 无版本策略 | OpenAPI CI 验证 + SDK 代码生成 + 版本化 API + deprecation 流程 |
