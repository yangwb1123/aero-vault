# 架构分析报告：aero-vault 项目当前状态与演进方向

> 分析基于以下材料：`cmd/server/main.go`、`internal/events/bus.go`、`internal/shutdown/group.go`、`internal/mcp/server.go`、`Makefile`、`docs/architecture.md`、`AGENTS.md`

---

## 1. 架构评估

### 1.1 核心优势

| 维度 | 评级 | 说明 |
|------|------|------|
| **分层清晰度** | ★★★★★ | 协议 → 中间件 → Service → Storage/Repository 的分层边界极其清晰，无跨层依赖 |
| **抽象稳定性** | ★★★★★ | `storage.Storage` 和 `repository.Repository` 接口稳定，已有 4 个 Storage 实现、2 个 Repository 实现 |
| **测试隔离性** | ★★★★☆ | AI mock (`MockLLM`, `HashEmbedder`)、fake repo、httptest 模式成熟；Race 全绿 |
| **安全默认** | ★★★★★ | AI/pgvector/Qdrant/events 全部 opt-in；nil 安全；MCP 跨租户漏洞已修复 |
| **可观测性** | ★★★★☆ | 15 OTel instruments + Prometheus + Grafana，覆盖全链路 |

### 1.2 架构债务与技术债

#### 🔴 严重违反 AGENTS.md 工程约束

| 违反项 | 具体问题 | 位置 |
|--------|---------|------|
| 单文件 ≤ 500 行 | **`main.go` 约 680 行**（超限 36%） | `cmd/server/main.go` |
| 单函数 ≤ 50 行 | **`run()` 约 130 行**（超限 160%） | `cmd/server/main.go:40-170` |
| 单函数 ≤ 50 行 | **`buildAIComponents()` 约 55 行**（超限 10%） | `cmd/server/main.go:320-375` |
| 单函数 ≤ 50 行 | **`buildBackgroundWorkers()` 约 65 行**（超限 30%） | `cmd/server/main.go:390-455` |

> **影响**：AGENTS.md 规定「违反者将在 HARNESS.md 定义的自动检查中被拒绝」，但当前 `make check` 未包含文件行数/函数行数检查。这是合规流程的隐患。

#### 🟡 Goroutine 生命周期管理缺失

`main.go` 中 6 个 `go func()` 启动的 goroutine **完全没有被 `ShutdownGroup` 管理**：

```go
// 问题代码模式 (main.go 中重复出现)
idxSub, _ := bus.Subcribe()   // cancel func 被丢弃 ← 无法在 shutdown 时取消订阅
go indexer.Run(ctx, idxSub)    // goroutine 不可追踪 ← 无法等待完成
// 同上模式用于: avw.Run, rw.Run, wh.Run, reconciler*3, Pool.Run, PostgresTransport.Run, SSE-rewrap
```

这是 12 个不追踪的 goroutine。虽然 `context.WithCancel` 最终会通过 `signal.NotifyContext` 触发取消，但：
1. 无法知道它们何时真正退出
2. 无法设置超时强制退出
3. 事件总线订阅泄露（`bus.Subscribe()` 的 cancel func 被丢弃）

#### 🟡 启动装配顺序未形式化

AGENTS.md 定义了「`config → storage → repo → service → workers → middleware → router`」的装配顺序，但：
- 这个顺序只存在于 `run()` 函数的**代码布局**中，没有**类型系统或结构体**保证
- 装配逻辑分散在 8 个辅助函数中（`buildStorage`, `buildRouter`, `buildAIComponents` 等）
- 没有 `App` 或 `Server` 结构体来封装完整生命周期

#### 🟢 已修复或可接受

- MCP 跨租户漏洞已修复 ✅
- Race 检测已加入 CI ✅
- `ShutdownGroup` 包已创建但**尚未集成到 main.go**（这是下轮 Sprint 的自然任务）

---

## 2. 扩展方向

### 方向 1：Server 结构体封装与 main.go 重构 ★★★★☆ P0

**为什么需要**：main.go 严重超限（680 行 vs 500 行阈值），违反 AGENTS.md 工程约束。更严重的是，启动代码和关闭代码无法复用（例如 `runMCP()` 复制了 `run()` 的 40% 逻辑），且单元测试无法启动一个"最小化 server"。

**核心挑战**：
- 如何在不破坏现有启动流程的前提下提取 `Server` 或 `App` 结构体
- 条件依赖（AI 可选、pgvector 可选、WebDAV 可选）需要组合模式或 Builder 模式
- 与 `ShutdownGroup` 的集成时序

**预期架构变更**：

```go
// 新 cmd/server/main.go (~80 行)
func run() error {
    cfg, logger := loadConfigAndLogger()
    app := NewApp(cfg, logger)
    return app.Run(ctx)
}

// internal/app/app.go (~250 行)
type App struct {
    cfg    *config.Config
    logger *slog.Logger
    sg     *shutdown.Group

    store storage.Storage
    repo  repository.Repository
    bus   *events.Bus
    svc   *service.FileService
    // ... 所有可选组件
}

func (a *App) Run(ctx context.Context) error {
    a.initInfrastructure(ctx)
    a.initServices()
    a.initAI(ctx)
    a.initWorkers(ctx)
    a.initRouter()
    return a.serve(ctx)
}
```

**对现有系统的影响**：
- ✅ 零侵入：现有 `build*` 函数可内联到 `App` 方法中
- ✅ `runMCP()` 可复用 `App` 的 60% 初始化流程
- ✅ 测试可以 `NewApp(cfg, logger)` + `app.Start(ctx)` + `app.Shutdown(ctx)`

---

### 方向 2：Goroutine 注册制与生命周期管理器集成 ★★★★☆ P1

**为什么需要**：当前 12+ 个不追踪的 goroutine 是生产环境稳定性的隐患。`ShutdownGroup` 包已就绪，但未集成到 main.go。

**核心挑战**：
- 已有的 goroutine 启动分布在 4 个函数中（`buildAIComponents`, `buildBackgroundWorkers`, `initInfrastructure`, `runServer`），需要统一注册点
- 每个 worker 的"就绪信号"需要显式化（`GoStarted` 模式）
- 事件总线订阅的取消函数需要与 worker 生命周期绑定

**预期的架构变更**：

```go
// 统一注册模式
app.sg.Go("indexer", func(ctx context.Context) {
    ch, cancel := app.bus.Subscribe()
    defer cancel()           // 确保 goroutine 退出时取消订阅
    app.indexer.Run(ctx, ch) // Run 返回时 goroutine 结束
})

app.sg.Go("reconcile", func(ctx context.Context) {
    app.reconciler.Run(ctx)  // Run 内部 select ctx.Done()
})

// 需要就绪信号的 worker
app.sg.GoStarted("http", func(ctx context.Context, ready chan<- struct{}) {
    close(ready) // 标记 HTTP server 已启动
    app.srv.ListenAndServe() // 阻塞直到 Shutdown
})
```

**对现有系统的影响**：
- 中型变更：需要为每个 worker 的 `Run()` 方法确认它是否响应 `ctx.Done()`
- ✅ `ShutdownGroup` 已有 `PhaseWorkers` 阶段调 `g.cancel()`，所有基于 `ctx` 的 worker 将自动停止
- 风险点：某些 worker 可能使用自己的 context（例如 `reconcile.New` 的内部定时器），需要验证

---

### 方向 3：Feature Interaction Contract Tests（方向 D3）★★★★☆ P1

**为什么需要**：系统中存在大量可选组件的 feature 交互（AI + events + replication + reconcile + webhook），这些交互的**正确性**当前只能通过集成测试覆盖，而集成测试需要 Docker（不在 CI gate 内）。

**核心挑战**：
- 需要定义「交互契约」：给定事件 E + 组件组合 C，预期行为 B
- 测试需要 mock 而非真实基础设施
- 契约测试必须能在 SQLite + local FS 下运行（CI gate 要求）

**预期架构变更**：

```go
// internal/integration/contract_test.go (无 build tag，在 CI gate 内运行)
func TestIndexerEventSequence(t *testing.T) {
    // Arrange: repo(SQLite) + store(local) + bus + indexer(无网络)
    // Act: PUT object → wait for indexer
    // Assert: chunks in repo
}

func TestReplicationEventDriven(t *testing.T) {
    // Arrange: source store + replica store(mock) + bus + replication worker
    // Act: DELETE object
    // Assert: replica received delete
}

func TestWebhookRetryDurable(t *testing.T) {
    // Arrange: bus + webhook + failing endpoint(mock HTTP server)
    // Act: Publish event → webhook fails
    // Assert: webhook_failures row created, retry loop picks it up
}
```

**对现有系统的影响**：
- 纯新增测试代码（~300 行），零生产代码变更
- ✅ 无风险，可独立推进

---

### 方向 4：Control/Data Plane 分离（方向 D4）★★★☆☆ P2

**为什么需要**：当前所有配置变更（auth keys、tenant quotas、AI 预算）都需要重启进程。对于生产部署，这意味着一扇滚动更新窗口。

**核心挑战**：
- 哪些配置支持热更新？auth keys（动态）、rate limits（动态）、log level（动态）vs storage backend（静态）、DB DSN（静态）
- 需要线程安全的配置快照切换（`atomic.Pointer[Config]`）
- 热更新通知机制（信号？HTTP endpoint？etcd watch？）

**预期的架构变更**：

```go
// internal/config/live.go
type LiveConfig struct {
    atomic.Pointer[LiveSnapshot]
}
type LiveSnapshot struct {
    RateLimit    RateLimitConfig
    AuthKeys     []KeyConfig
    AIBudgets    map[string]float64
    LogLevel     slog.Level
}

// 使用方式（非新 config 包，而是嵌入现有 Config 的可变部分）
type Config struct {
    // ... 所有不可变启动时配置
    Live *config.LiveConfig // 可热更新的子集
}
```

**对现有系统的影响**：
- 需要审计每个配置项的可变性需求
- 中间件（auth、ratelimit）需要支持运行时刷新
- 与现有 `config.Load()` 流程集成

---

### 方向 5：租户级资源隔离 ★★★☆☆ P2

**为什么需要**：当前租户隔离仅在元数据层面（tenant 字段隔离），但资源（goroutine、内存、并发请求）在所有租户间共享。一个恶意或高流量租户可以 DoS 其他租户。

**核心挑战**：
- 请求级隔离：per-tenant goroutine limiter（已部分实现 `PerTenantConcurrencyLimiter`）
- 内存级隔离：per-tenant writer buffer pool
- 存储级隔离：per-tenant I/O 限流
- 事件级隔离：per-tenant event queue

**预期架构变更**：

```go
// 扩展 existing middleware.PerTenantConcurrencyLimiter
type PerTenantLimiter struct {
    maxConcurrent int
    perTenant     int
    semaphores    sync.Map // map[string]chan struct{}  per-tenant
}

// 新增 middleware.TenantRateLimiter (per-tenant token bucket)
type TenantRateLimiter struct {
    global  *RateLimiter
    buckets sync.Map // map[string]*rate.Limiter
}
```

**对现有系统的影响**：
- 中型变更：新增中间件 + 配置项
- 不影响现有请求路径（新增中间件插入链中）
- 存储层需要额外设计（per-tenant I/O 优先级队列）

---

## 3. 接口设计建议

### 3.1 关键接口设计原则

| 原则 | 当前状态 | 建议 |
|------|---------|------|
| **接口尽量小** | ✅ `Storage` (10 methods)、`Repository` (稳定) | 保持，避免接口膨胀 |
| **接口隔离** | ⚠️ `service.ChunkCleaner` 只有 1 个方法，好 | 但 `ai.Indexer` 同时实现 `ChunkCleaner` 和内部接口 — 考虑拆分为 `Indexer` 和 `Deleter` |
| **返回类型稳定** | ⚠️ `Search.Query` 返回 `[]Hit`，但 Hit 的字段被多个调用方依赖 | 考虑将 `Hit` 改为接口 + 结构体组合，避免字段扩散 |
| **无循环依赖** | ✅ 当前目录结构无循环依赖 | 保持 lint 检查 |

### 3.2 是否需要新的抽象层

#### 建议引入：`internal/app` 包

当前缺失的关键抽象是一个代表**完整服务器实例**的类型。论证：

```
问题：当前谁拥有生命周期？
├─ run() 函数 → 不被任何结构体封装 → 无法复用、无法测试
├─ runMCP() 函数 → 复制了 40% 的初始化逻辑 → 知识重复
├─ 6个辅助 build* 函数 → 调用者负责管理返回值 → 条件逻辑散布
└─ 结论：需要 App 结构体

App 的职责：
├─ 拥有所有组件的生命周期（initialize → start → shutdown）
├─ 管理条件依赖图（AI Required → Embedder, LLM; AI Optional → Reranker）
├─ 与 ShutdownGroup 直接绑定
└─ 提供 Start/Shutdown 方法供测试和主流程共用
```

#### 不建议引入：新的依赖注入框架

当前的手工装配（`run()` 中显式调用 `build*` 函数）虽然有冗余，但有两大优点：
1. 显式可见性 — 所有依赖在编译时明确
2. 无反射/代码生成 — 保持构建速度和调试友好性

**选项 A（推荐）**：提取 `App` 结构体 + 手工装配
- 优点：简单、显式、零依赖
- 缺点：`App` 的字段可能较多（~15 个），但可通过 Option 模式或 Builder 模式缓解

**选项 B**：Wire 或类似 DI 框架
- 优点：减少样板代码
- 缺点：代码生成增加调试复杂性；隐性依赖不如显式接口清晰

**决策**：选 A。当依赖图规模翻倍时才考虑 B。

### 3.3 向后兼容性

| 变更类型 | 兼容策略 |
|---------|---------|
| 新增 `internal/app` 包 | ✅ 不改变任何现有 public API；main.go 可逐步迁移 |
| `bus.Subscribe()` 返回 cancel func | ✅ 已兼容（旧调用方忽略返回值，新调用方使用它） |
| ShutdownGroup 集成 | ✅ 不改变现有 worker 接口，只改变启动/关闭管理方式 |
| Control Plane API | ⚠️ 新增/conf HTTP endpoint，不影响现有 REST 路由 |

---

## 4. 技术选型分析

### 4.1 当前技术栈评估

| 组件 | 当前选择 | 评价 |
|------|---------|------|
| HTTP 路由 | `chi/v5` | ✅ 轻量、稳定，适合当前规模 |
| SQL | `modernc.org/sqlite` + `pgx/v5` | ✅ SQLite 纯 Go 无 CGO，CI 友好 |
| Storage | 自定义接口 | ✅ 4 个实现，已验证 |
| OTel | `go.opentelemetry.io` | ✅ 行业标准 |
| AI Embedding | 自定义接口 + HTTP 后端 | ⚠️ 无内置 embedding 能力，依赖外部服务，这是合理的设计 |
| 序列化 | stdlib `encoding/json` | ✅ 合规 |

### 4.2 不需要引入的新依赖

| 候选 | 排除理由 |
|------|---------|
| Wire (DI) | 当前手工装配足够清晰，依赖图~15 个节点 |
| Uber Fx | 过度抽象，破坏显式性 |
| protobuf/gRPC | JSON-RPC for MCP + REST for API 已经覆盖使用场景 |
| Redis | 当前无分布式缓存需求；进程内缓存（`CachingEmbedder`、Search 结果缓存）已满足 |
| ORM (GORM/Ent) | 自定义 SQL + migration 提供更细粒度控制，符合 I6（stdlib 优先） |

### 4.3 值得关注但不必立即引入

| 候选 | 理由 | 引入时机 |
|------|------|---------|
| `errgroup` (golang.org/x/sync) | 当前手动 sync.WaitGroup，但 `ShutdownGroup` 已封装；`errgroup` 可提供 first-error-wins 语义 | 控制平面 API 需要 atomic 多组件健康检查时 |
| `singleflight` | 防止并发重计算（如 BM25 构建、索引重建） | 高并发场景出现重复计算问题时 |
| 分布式配置（etcd/consul） | 控制/数据面分离需要 | P2 热更新场景到来时 |
| K8s operator | 自动部署/扩缩容 | 多 replica 部署成为主流需求时 |

### 4.4 自建 vs 采购决策记录

| 需求 | 决策 | 理由 |
|------|------|------|
| 事件总线 | 自建 | 项目内仅有 ~5 种事件类型，业务逻辑耦合轻；Postgres LISTEN/NOTIFY 提供跨实例广播 |
| 任务队列 | 自建（DB 轮询） | 任务类型少（~5 种），无秒级延迟要求；DB 写入即持久化，无需独立 MQ |
| AI Embedding | 外部 HTTP | 保持单二进制无 GPU 依赖 |
| 鉴权 | 自建 | 支持的 3 种模式（Key/JWT/SigV4）均为标准算法，无外包必要 |

---

## 5. 实施路线图

### 5.1 优先级排序

| 优先级 | 事项 | 工作量 | 风险 |
|--------|------|--------|------|
| **P0** | 遵守工程约束：main.go 重构为 App 结构体 | ~200 行提取 + ~80 行新文件 | 低 — 纯代码提取 |
| **P1** | ShutdownGroup 集成到 main.go | ~100 行变更 | 中 — 需要验证每个 worker 的 ctx 响应 |
| **P1** | Feature interaction contract tests (D3) | ~300 行新测试 | 低 — 纯新增测试 |
| **P1** | 丢弃的 bus cancel func 修复 | ~10 行 | 低 |
| **P2** | Goroutine leak 测试 | ~200 行新测试 | 低 |
| **P2** | 控制/数据面分离 | ~500 行 + 配置重构 | 高 — 需要热更新安全机制 |
| **P2** | 租户级资源隔离 | ~400 行 + 新中间件 | 中 — 存储层改造复杂 |

### 5.2 阶段划分

```
阶段 1 (当前 Sprint) ─── 工程合规 + 稳定性基础
├─ Refactor main.go → App 结构体                           [P0]
│  └─ 拆分 run() ~130 行 → App.Run() + App.Start() + App.Shutdown()
├─ ShutdownGroup 集成                                      [P1]
│  └─ 12+ 个 goroutine 全部注册到 sg.Go()
├─ 废弃的 bus cancel func 修复                              [P1]
│  └─ idxSub, cancel := bus.Subscribe(); defer cancel()
└─ make check 补充行数/圈复杂度检查                          [P0]
   └─ 添加 complexity-lines 规则到 HARNESS.md

阶段 2 (下个 Sprint) ─── 测试韧性 + 质量门禁
├─ Feature interaction contract tests (D3)                 [P1]
│  └─ test: Indexer+Event, Replication+Event, Webhook+Retry
├─ Goroutine leak test                                     [P2]
│  └─ test: worker Run+Shutdown 不泄漏 goroutine
├─ runMCP() 复用 App 基础设施                               [P1]
│  └─ runMCP() → app.WithMCPServer().Run()
└─ 测试覆盖率 ≥ 50% (当前? → 80% 目标)                      [P1]

阶段 3 (未来 Sprint) ─── 生产就绪
├─ Control/Data Plane 分离                                  [P2]
│  └─ 热更新 auth keys、rate limits、log level
├─ 租户级资源隔离                                           [P2]
│  └─ per-tenant rate limiter + concurrency limiter
├─ Graceful shutdown E2E 测试                                [P2]
│  └─ test: SIGTERM → in-flight 请求完成
└─ 分布式追踪 (已有 OTel 但未启用)                            [P2]
```

### 5.3 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| Worker 不响应 ctx.Done() | 中 | 高 — 关闭 hang | 逐个审查 worker Run() 的 ctx 使用；添加 shutdown 超时；ShutdownGroup 已有 timeout 机制 |
| main.go 重构导致回归 | 低 | 高 — 服务不可用 | 分步重构：先提取 App 结构体内联现有代码，再逐步拆分；每次 `make check` 全绿 |
| Contract test 依赖真实基础设施 | 中 | 中 — 测试不在 CI gate 内 | 强制所有 contract test 使用模拟/SQLite/LocalFS，禁止 build tag |
| 热更新导致数据竞争 | 中 | 高 — 配置不一致 | 使用 `atomic.Pointer` 原子切换快照；每个配置快照不可变；单独 race test |
| 租户限流误伤合法流量 | 低 | 中 | 默认关闭 per-tenant limiter；先告警模式再生效模式 |

### 5.4 关键里程碑

```
M0: main.go < 500 行, run() < 50 行, make check 合规            [本周]
M1: ShutdownGroup 集成完成, race CI 全绿                          [2 周]
M2: 12+ contract tests 通过                                     [3 周]
M3: runMCP() 与 run() 共享 App 基础设施                           [4 周]
M4: 热更新 API /conf 上线                                       [6 周]
M5: 全量 P2 功能完成, 测试覆盖率 ≥ 80%                            [8 周]
```

---

## 总结

当前 aero-vault 的架构基础非常扎实：分层清晰、接口稳定、测试隔离性好。最迫切的问题不是引入新功能，而是 **遵守自身设定的工程约束**（main.go 行数超限、run() 函数超限）和 **补全生命周期管理的最后一公里**（ShutdownGroup 集成 + goroutine 注册制）。

建议优先做阶段 1 的工程合规工作（重构 main.go + 集成 ShutdownGroup），这是后续所有改进的基础。阶段 2 的 contract test 可以在阶段 1 的代码提取完成后并行推进，因为测试层对生产代码的依赖是最小的。
