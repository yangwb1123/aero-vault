# 架构分析：AeroVault 运行时盲区与高价值扩展方向

## 目录

1. [架构评估](#1-架构评估)
2. [高价值扩展方向](#2-高价值扩展方向)
3. [接口设计建议](#3-接口设计建议)
4. [技术选型](#4-技术选型)
5. [实施路线图](#5-实施路线图)

---

## 1. 架构评估

### 1.1 当前架构的优势

| 维度 | 评价 | 依据 |
|------|------|------|
| **分层清晰度** | 优秀 | Protocol Adapters → FileService → Storage/Repository/EventBus 三层边界明确，符合整洁架构 |
| **可扩展性** | 良好 | 存储后端、数据库、向量索引均通过接口抽象 + factory 模式注入，新增后端只需实现接口 + 注册工厂 |
| **测试友好** | 良好 | 分层允许 mock 下层依赖（如 `ai.MockLLM`、`HashEmbedder`），SQLite + local FS 作为 CI 基线零网络依赖 |
| **默认安全** | 合理 | AI/pgvector/Qdrant/WebDAV 均 opt-in gated，默认关闭；`nil` embedder/llm 不会破坏 CRUD 路径 |
| **单体优势** | 充分利用 | 15+ 后台 worker 在进程内共享 EventBus，避免了分布式事件总线的延迟和一致性开销 |

### 1.2 关键设计决策评估

**已做对的决策：**

| # | 决策 | 评价 |
|---|------|------|
| D1 | FileService 作为唯一业务层入口，禁止协议层直连 Storage | 正确——鉴权、版本控制、事件发布集中在同一层，防止逻辑碎片化 |
| D2 | Opt-in 安全默认（AI/高级功能默认关闭） | 正确——CI gate 运行在最小依赖路径上，降低回归风险 |
| D3 | SQLite + local FS 作为默认基线 | 正确——零外部依赖的本地开发体验降低了上手门槛 |
| D4 | Storage/Repository 双抽象 + factory 注入 | 正确——切换成本低，contract test suite 确保了语义一致性 |

**有争议但可接受的决策：**

| # | 决策 | 潜在问题 | 权衡 |
|---|------|---------|------|
| D5 | Middleware 链不挂在 handler test 中 | 隔离测试无 tenant/auth 上下文 | 简化单元测试，但集成测试需额外装配 |
| D6 | 事件总线为 in-process channel 切片 | 无分布式、无持久化、subscriber 无生命周期管理 | 单体场景下序列化成本最低；分布式场景需接入外部消息队列（可后续替换） |
| D7 | 协程以 `go func()` 直接启动，无 supervisor | 任意 worker panic 直接崩溃进程或静默退出 | 结构简单；但在生产环境中不合理 |

### 1.3 架构债务与技术债

从 v39 分析文档映射到代码库，识别出以下债务：

#### 🏗️ 架构债务（需要结构性重构）

| 债务 | 位置 | 严重程度 | 修复周期 |
|------|------|---------|---------|
| **Subscriber 生命周期管理缺失** | `bus.go` 中 `subs []chan` 永久增长 | **严重** — 生产运行数小时后必然出现内存泄漏 | 1-2 天 |
| **Worker 健康基础设施缺失** | 15+ goroutine 零健康可见性，零重启机制 | **严重** — 系统可进入"僵尸"状态（HTTP 200 但功能退化） | 1-2 周 |
| **全局资源无租户隔离** | ConcurrencyLimiter、JobQueue、DB 连接池均为全局共享 | **高** — Noisy neighbor 可使整个集群不可用 | 2-4 周 |
| **/metrics 端点未认证** | 认证旁路白名单包含 `/metrics` | **高** — 多租户场景下为信息泄露通道 | 1-2 天 |

#### 🔧 技术债（需要局部改进）

| 债务 | 位置 | 严重程度 |
|------|------|---------|
| 后台 goroutine 无 panic recovery | 除 Job Pool 外均无 `recover()` | 中 — panic = 进程崩溃 |
| SSE 连接无上限控制 | 无 `SSE_MAX_CONNECTIONS` | 中 — 恶意客户端可耗尽内存 |
| Job Queue 纯 FIFO | `ORDER BY created_at` 无租户/优先级感知 | 中 — 多租户场景公平性不足 |
| Worker 启动模式与 main.go 耦合 | `main.go` 直接 `go func()` 启动，不可测试 | 低 — 影响可测性 |

#### 债务分类建议

```
       严重性
        ↑
  严重   │  Worker 健康    Subscriber 泄漏
        │
  高     │  /metrics 未认证  无租户隔离
        │
  中     │  Job FIFO    SSE 无上限
        │
  低     │  main.go 耦合
        └─────────────────────────→ 修复成本
             低     中     高
```

---

## 2. 高价值扩展方向

> 从 v39 的 5 个方向出发，但以**架构重组**而非"打补丁"的视角提出扩展方向。每个方向包含：动机 → 核心挑战 → 架构变更 → 影响评估。

---

### 方向 A：运行时底层设施（Runtime Infrastructure Layer）

**v39 映射：** 方向 1（SSE 泄漏）+ 方向 2（Worker 健康）

#### 为什么需要

当前系统将**进程内基础设施**（goroutine 管理、事件总线、健康探测）视为"简单实现细节"，以 `go func()` 和全局切片直接编码。在开发/单租户场景下这足够工作，但在生产中暴露三个致命缺陷：

1. **资源泄漏静默积累** — SSE subscriber channel 泄漏 2 小时后可导致 1GB+ 内存浪费
2. **功能退化无声** — Indexer panic 退出后 `/healthz` 仍返回 200
3. **不可测试** — `main.go` 中直接启动的 goroutine 无法通过单元测试验证生命周期

#### 核心挑战

| 挑战 | 难度 | 说明 |
|------|------|------|
| 与现有 `go func()` 启动模式的兼容 | 中 | 需要在不破坏现有代码的前提下引入新抽象 |
| 重启策略的参数化 | 中 | 不同 worker 的重启策略不同（indexer 应重启，sse-rewrap 应 fatal） |
| 健康指标定义标准化 | 中 | 每个 worker 需定义"健康"的含义（lastSuccess？queueDepth？） |

#### 架构变更

**现状：**
```
main.go:
  go indexer.Run(ctx)
  go reconciler.Run(ctx)
  // 15+ 类似的 go func()
  // 无 registry，无健康，无 recover
```

**目标：**
```
type Worker interface {
    Name() string
    Run(ctx context.Context) error
    Health() WorkerHealth
}

type Supervisor struct {
    workers []Worker
}

// 启动所有 worker
sv := supervisor.New(supervisor.Config{
    RestartStrategy:    exponentialBackoff(1s, 60s),
    MaxRestartsPerMin:  3,
    StatusRegistry:     healthRegistry,
    MetricRegisterer:   prometheus.DefaultRegisterer,
})
sv.Add(&IndexerWorker{...})
sv.Add(&ReconcileWorker{...})
sv.Start(ctx)
```

**关键设计决策：**

| 决策点 | 选项 A（推荐） | 选项 B | 权衡 |
|--------|---------------|--------|------|
| Worker 接口粒度 | 每个 goroutine 一个 Worker 实现 | 按功能域分组（IndexerWorker 内含多个内部 goroutine） | A: 颗粒度细，每个 worker 独立健康/重启；B: 实现更少但重启粒度粗 |
| 健康状态聚合 | `/readyz` 聚合所有 worker，关键 worker 死亡返回 503 | 每个 worker 独立端点 `/readyz/{name}` | A: 与 K8s readiness probe 兼容；B: 更灵活但对 LB 不友好 |
| Worker 注册时机 | 编译时注入（main.go 显式创建） | 运行时 discovery（反射/注册表） | A: 显式，可控；B: 灵活但过度 |

**推荐：** 选项 A（编译时显式注入），因为 15 个 worker 数量可控，显式注入的可读性超过反射的灵活性。

#### 对现有系统的影响

- **低风险：** 引入抽象层时保持 `indexer.Run(ctx)` 签名不变，仅包装调用者
- **无侵入：** 健康状态通过指标暴露，不改变现有 worker 的内部逻辑
- **分阶段：** Phase 1 仅加包装（supervisor + registry），Phase 2 加重启/告警

---

### 方向 B：多租户资源隔离契约（Multi-Tenant Resource Isolation Contract）

**v39 映射：** 方向 3（计算资源隔离）+ 方向 5（Job Queue 隔离）

#### 为什么需要

现有隔离机制（数据行隔离 + 速率限制）仅在**请求入口**和**数据存储**层面隔离租户，忽略了**计算资源**这个最稀缺的共享资源。在一个 SaaS 存储服务中，计算资源隔离缺失可能导致：

- 一个租户的批量导入任务**占满所有 DB 连接**，阻塞其他租户的读取
- 一个租户的索引作业**填满 Job Queue**，延迟其他租户的复制（导致数据安全风险）
- 一个租户的大量文件上传**耗尽 goroutine 池**，使其他租户的所有请求 429

#### 核心挑战

| 挑战 | 难度 | 说明 |
|------|------|------|
| 资源限额的测算 | 高 | "一个租户应该允许多少并发"没有标准答案——取决于租户等级、集群大小 |
| Per-tenant DB 连接池 | 高 | Postgres 原生的连接池不支持 per-tenant 拆分，需要用连接池包装器 |
| 内存预算的精确跟踪 | 高 | Go GC 使得准确跟踪 per-tenant 存活内存困难，只能用近似方法 |
| 配置复杂度 | 中 | 10+ 可配置限额项增加了运营复杂度，需要合理的默认值 |

#### 架构变更

**引入资源配额层（Resource Quota Layer）：**

```
                     ┌─────────────────────┐
                     │   FileService       │
                     └──────┬──────────────┘
                            │ 每次资源操作前检查配额
                            ▼
                ┌─────────────────────────┐
                │   ResourceQuotaGate     │ ◄── 新抽象层
                │                         │
                │  ┌───────────────────┐  │
                │  │ Handler Concurrency│  │ per-tenant weight
                │  │ DB Connections     │  │ per-tenant pool
                │  │ Job Queue Depth    │  │ per-tenant cap
                │  │ Memory Budget      │  │ per-tenant counter
                │  │ Embed API Concur.  │  │ per-tenant semaphore
                │  └───────────────────┘  │
                └─────────────────────────┘
                            │
              ┌─────────────┼─────────────┐
              ▼             ▼             ▼
         Storage        Repository      JobQueue
```

**核心接口设计：**

```go
// 每个资源维度的配额定义
type ResourceQuota struct {
    Name           string
    HardLimit      int64
    SoftLimit      int64   // 超过时 warn，超过 HardLimit 时 reject
}

// 租户资源消费跟踪
type TenantResourceState struct {
    TenantID        string
    Concurrency     int64
    PendingJobs     int64
    MemoryBytes     int64
    DBConns         int64
}

// 配额门卫接口
type QuotaGate interface {
    // Acquire 尝试获取资源配额，返回释放函数
    Acquire(ctx context.Context, tenantID string, resource string) (release func(), error)
    // Status 返回给定租户的当前资源使用状态
    Status(tenantID string) TenantResourceState
}
```

#### 配置设计策略

```
# 推荐使用 YAML 配置而非环境变量——多维度配额需要结构化表达

tenant_defaults:
  max_concurrent_requests: 20     # 全局 handler goroutine 软限
  max_pending_jobs: 1000          # Job queue per-tenant 上限
  max_db_connections: 5           # Postgres 模式 per-tenant 连接池
  max_memory_bytes: 500000000     # 500MB per-tenant 内存预算
  max_embed_concurrency: 4        # 嵌入 API 并发调用

tenants:
  "enterprise-corp":
    max_concurrent_requests: 100  # 企业级租户更高配额
    max_pending_jobs: 5000
```

#### 对现有系统的影响

| 影响面 | 程度 | 缓解 |
|--------|------|------|
| 新增概念 | 中 — 需要运维人员理解配额语义 | 合理的默认值（default 不感知） |
| 性能开销 | 低 — 每个资源操作前执行 atomic counter 操作 | ns 级开销 |
| 向后兼容 | 高 — 不配置 QuotaGate 时回退到现有全局模式 | `QUOTA_ENABLED=false` 默认 |
| 测试适配 | 中 — 现有测试在无配额模式下运行不变 | QuotaGate 通过 nil check 跳过 |

---

### 方向 C：可观测性信息分级访问控制（Observability Access Control）

**v39 映射：** 方向 4（/metrics 未认证）

#### 为什么需要

`/metrics` 端点暴露了所有租户的请求量、存储用量、AI 花费——在多租户场景中，这些是**商业敏感数据**。从架构视角看，这暴露了一个更深层问题：

> **系统的可观测性通道（metrics/traces/logs）与业务数据通道共享同一个认证边界。**

这意味着：如果用户能访问 API，他们就能访问所有运营数据。在多租户架构中，运营数据应该被隔离到**管理平面**（control plane），而非暴露在**数据平面**（data plane）。

#### 核心挑战

| 挑战 | 难度 | 说明 |
|------|------|------|
| Prometheus 指标过滤 | 高 — Prometheus 客户端库的设计假设"全部收集或全不收集" | 需要在 gather 层面或暴露层面过滤 |
| 聚合计算 | 中 — 跨租户聚合（如 `sum(storage_bytes)`）在过滤后不准确 | 聚合只在 admin scope 下可用 |
| 向后兼容 | 低 — `METRICS_AUTH_REQUIRED=false` 保持当前行为 | 默认保持兼容 |

#### 架构变更

**引入指标分级暴露策略：**

```
/metrics 端点 → MetricsMiddleware
  │
  ├─ Scope=anonymous
  │   └─ 仅暴露：go_*, process_*, http_server_requests_total (无tenant标签)
  │       └─ 用于：负载均衡器健康检查、基础设施监控
  │
  ├─ Scope=read (tenant特定的key)
  │   └─ 暴露：该租户的 tenant 标签指标 + 无标签基础设施指标
  │       └─ 用于：租户自服务监控（需要此功能开启）
  │
  └─ Scope=admin
      └─ 暴露：全量指标（所有 tenant 标签）
          └─ 用于：运维人员、Prometheus Operator
```

**技术方案比较：**

| 方案 | 优点 | 缺点 | 推荐 |
|------|------|------|------|
| **多 Registry**：每个 scope 一个 prometheus.Registry，admin 注册全部，tenant 注册子集 | 彻底隔离，无泄露风险 | 需维护多个 registry，租户级别需动态创建 | ✅ 适合大规模 |
| **Gather 后过滤**：单 registry，`/metrics` handler 中根据 scope 过滤 label | 部署简单，单 registry | 过滤逻辑复杂，有泄露风险（忘记过滤某个 metric） | ⚠️ 过渡方案 |
| **label 加密**：tenant label 使用 HMAC 加密，仅 admin key 可解密 | 保护数据但保持 Prometheus 兼容 | 复杂，需要 Prometheus 端解密 | ❌ 过度设计 |

**建议方案：** 短期用 Gather 后过滤（方向 4 的快速修复思路），中长期迁移到多 Registry 方案。

---

### 方向 D：Job Queue 公平调度（Fair Scheduling for Job Queue）

**v39 映射：** 方向 5

#### 为什么需要

从 FIFO 到公平调度的升级，是 AeroVault 从"单租户工具"演进为"多租户平台"的标志性架构变更。详见 v39 方向 5 分析。

#### 核心挑战

高于方向 5 所述——因为公平调度不是简单的 `ORDER BY priority`，而需要解决：

1. **优先级反转**：低优先级作业持有锁导致高优先级作业等待
2. **饥饿预防**：确保所有租户最终都能获得执行
3. **突发处理**：短时大量入队不应导致长时间不公平

#### 架构变更

**引入工作窃取调度器（Work-Stealing Scheduler）：**

```
当前架构：
  ┌──────────────────────────┐
  │  Single Queue (FIFO)     │ ← 所有租户、所有类型的作业共享
  │  [A1, A2, ..., B1, B2]   │
  └──────────┬───────────────┘
             │ ClaimJob: ORDER BY created_at
             ▼
          Worker Pool (N goroutines)

目标架构：
  ┌──────────────────────────────────────────┐
  │  Priority-Queue per Tenant (WF²Q)       │
  │                                          │
  │  Tenant A: [replicate, replicate, index] │
  │  Tenant B: [scan, index]                 │
  │  Tenant C: [] (idle)                     │
  └──────────┬───────────────────────────────┘
             │ ClaimJob: 
             │   1. 按租户轮转（fair queuing）
             │   2. 同租户内按优先级
             │   3. per-tenant 并发上限检查
             ▼
          Worker Pool (N goroutines)
```

**推荐算法：Weighted Fair Queuing (WF²Q)**

```
每个租户有虚拟时钟，按权重推进：
- tenant A 权重 10（企业级），tenant B 权重 1（基础级）
- 每处理 1 个 tenant B 的作业，处理 10 个 tenant A 的作业
- 但 tenant B 仍在调度中，不存在饥饿
```

**与 v39 方向 5 的差异：**

| 维度 | v39 建议 | 本扩展建议 | 理由 |
|------|---------|-----------|------|
| 调度粒度 | 全局 FIFO + per-tenant cap | WF²Q + per-tenant cap | 纯 cap 在边界情况下仍有饥饿（cap 满了才停） |
| 优先级 | 作业类型优先级 | 类型优先级 + 租户权重 | 租户权重可映射到 SLA 等级 |
| 并发控制 | per-tenant worker reservation | per-tenant claim quota + 工作窃取 | 预留 worker 浪费资源，工作窃取更高效 |

---

### 方向 E：进程内资源管理抽象（In-Process Resource Manager）

**v39 映射：** 方向 1-5 的**基础设施层**统一抽象

#### 为什么需要

v39 分析揭示了 5 个不同的运行时盲区，但它们有一个共同的根因：

> **系统缺少一个统一的进程内资源管理抽象层。**

具体来说：
- SSE Subscriber 泄漏 → 缺少 `Resource生命周期管理`
- Worker 健康不可见 → 缺少 `Component健康注册`
- 租户计算隔离缺失 → 缺少 `Resource配额门卫`
- /metrics 泄露 → 缺少 `Observability访问控制`
- Job Queue 饥饿 → 缺少 `Scheduler公平性`

这些不应该是 5 个独立的修复，而应该是一个统一的基础设施层的 5 个方面。

#### 核心挑战

| 挑战 | 难度 | 说明 |
|------|------|------|
| 抽象边界 | 高 — 抽象太薄无价值，太厚过度设计 | 需要精确地只覆盖"运行时管理" |
| 与现有代码的集成 | 中 — 渐进式引入，不要求一次性重构 | 适配器模式包装现有组件 |
| 性能开销 | 低 — 管理层面应保持在纳秒-微秒级 | 使用原子操作 + lock-free 结构 |

#### 架构变更

**引入 `runtime` 包（内部基础设施层）：**

```go
package runtime

// Component 是所有可管理组件的统一接口
type Component interface {
    Name() string
    Run(ctx context.Context) error
}

// Resource 是需要在进程内管理的资源
// 可以是 goroutine、DB 连接、内存预算等
type Resource[T any] struct {
    ID       string
    Value    T
    Lifetime context.Context
    OnRelease func()
}

// ResourceManager 管理组件的生命周期和资源配额
type ResourceManager struct {
    // 组件管理
    supervisor   *Supervisor
    healthRegistry *HealthRegistry
    
    // 资源管理
    quotas       *QuotaManager
    scheduler    *FairScheduler
    
    // 可观测性
    metricsAuth  *MetricsAccessControl
}
```

**这 5 个方向统一到一个抽象层下：**

```
ResourceManager
├── Component Management (方向1+2)
│   ├── Supervisor — goroutine 生命周期 + 重启
│   ├── HealthRegistry — /readyz feed
│   └── Subscribe/Unsubscribe — SSE 等 subscriber 管理
│
├── Resource Quota (方向3+5)
│   ├── QuotaGate — per-tenant 资源限额
│   └── FairScheduler — Job Queue 公平调度
│
└── Observability Control (方向4)
    └── MetricsAccess — 分级指标暴露
```

#### 对现有系统的影响

| 影响面 | 评估 | 说明 |
|--------|------|------|
| 迁移成本 | 中 — 现有代码需要逐步适配 | 适配器模式：现有组件实现 `Component` 接口即可接入 |
| 代码可读性 | 提高 — 集中化的生命周期管理 vs 分散的 `go func()` | 运维人员从 `/debug` 端点即可看到所有组件状态 |
| 测试性 | 显著提高 — Component 接口可在测试中替换/mock | 无需在集成测试中真正启动 background worker |
| 风险 | 低 — 渐进式引入，每个组件独立迁移 | 先迁移 1-2 个组件（如 SSE），验证后推广 |

---

## 3. 接口设计建议

### 3.1 关键接口设计原则

针对 v39 分析的 5 个方向，提炼以下接口设计原则：

#### 原则 1：生命周期接口化（方向 1+2 的修复基础）

所有具有明确生命周期的组件（worker、subscriber、connection）应实现统一的生命周期接口：

```go
// 核心原则：创建 → 运行 → 健康 → 销毁 都有明确的接口
type Lifecycle interface {
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
    Health() HealthStatus
}
```

**为什么是接口而不是 struct：**
- `Lifecycle` 是对行为的描述，而非对状态的描述
- `go func()` 启动的 goroutine 包装后也符合此接口
- 测试时可以用 `NoopLifecycle` 替换

#### 原则 2：订阅者生命周期由订阅者自身管理（方向 1 的精确修复）

```go
// 反模式（当前 bus.go）：
sub := bus.Subscribe()       // 通道被追加到切片
// ...                             // 但从未移除

// 正模式：
sub := bus.Subscribe()       // 返回 *Subscription
defer sub.Close()            // Close 从 b.subs 移除并 close channel
```

**`Subscription` 对象的设计：**
- `context.Context` + `Close()` 双重退出路径（Context 用于外部取消，Close 用于显式释放）
- `Done()` channel 通知监听方（类似 `context.Context.Done()`）
- `fmt.Stringer` 用于日志/指标（打印 subscriber ID + 创建时间）

#### 原则 3：资源作用域（Scope）显式化（方向 3+4 的基础）

```go
// 所有需要隔离的操作都应该接收 scope 参数
type Scope struct {
    TenantID string
    // 未来可扩展：UserID, APIKeyID, SessionID
}

// ResourceManager 方法签名：
func (rm *ResourceManager) Acquire(ctx context.Context, scope Scope, resource string) error
func (rm *ResourceManager) Release(scope Scope, resource string)
```

**Scope 的传递方式：**
- 不要隐式从 `context.Context` 提取（测试中容易遗漏）
- 通过接口参数显式传递，文档化"此操作受 Scope 约束"
- Context 中的 tenant ID 用于默认值，显式 Scope 用于覆盖

#### 原则 4：健康信号标准化（方向 2 的修复基础）

```go
type HealthStatus struct {
    Name           string
    Alive          bool
    LastSuccess    time.Time
    Error          string      // 非空时说明最近一次失败原因
    Degraded       bool        // true = 功能正常但性能下降
}
```

**标准化好处：**
- `/readyz` 输出结构一致（JSON 而非自由文本）
- 告警规则可以统一为 `worker_alive{name=~".+"} == 0`
- 每个 worker 的健康实现可交换

### 3.2 是否需要新的抽象层

**需要，但必须精确定义边界：**

| 抽象层 | 包含 | 不包含 | 优先级 |
|--------|------|--------|--------|
| `internal/runtime` | 生命周期管理、健康注册、Supervisor | 业务逻辑、协议处理 | **P0** — 方向 1+2 的基础 |
| `internal/quota` | 资源配额门卫、公平调度 | 速率限制（已有 `middleware.RateLimiter`） | **P1** — 方向 3+5 的基础 |
| `internal/metricsauth` | 指标暴露的认证/过滤 | 指标收集本身（已有 `telemetry`） | **P1** — 方向 4 的基础 |

**哪个先引入：** `internal/runtime` 优先，因为它覆盖了方向 1（SSE 泄漏）和方向 2（Worker 健康），而这 2 个是 P1 严重级别。

### 3.3 向后兼容策略

| 变更 | 兼容策略 | 回退路径 |
|------|---------|---------|
| `Bus.Subscribe()` 改为返回 `*Subscription` | 原返回 `<-chan Event` 不变，`Subscription` 包装 channel | 通过构建标签 `//go:build legacy_bus` 保留旧接口 |
| Worker 启动改为 `Supervisor` | `main.go` 中 `go indexer.Run(ctx)` 改为 `supervisor.Add(indexer).Start()` | 旧代码仍编译，新代码可选使用 |
| `/metrics` 增加认证 | `METRICS_AUTH_REQUIRED=false` 保持当前行为 | 环境变量切换，零代码变更 |
| QuotaGate 引入 | `QUOTA_ENABLED=false` 跳过配额检查 | 零代码变更，配置开关 |

**核心原则：** 所有架构变更必须有**配置开关**使得旧行为 100% 保留。增量采用，不要大爆炸式迁移。

---

## 4. 技术选型

### 4.1 自建 vs 引入第三方

| 领域 | 自建 | 第三方 | 推荐 | 理由 |
|------|------|--------|------|------|
| **Supervisor/Goroutine 管理** | ✅ 自建 | ❌ 不推荐 | **自建** | 接口只有 3 个方法（Start/Stop/Health），Go 原生 goroutine 管理极简，不需要库 |
| **健康注册表** | ✅ 自建 | ❌ 不推荐 | **自建** | 本质是 `sync.Map[string]*HealthStatus` + `/readyz` JSON handler，~100 行代码 |
| **公平调度（Job Queue）** | ✅ 自建 | ⚠️ 可借鉴 | **自建** | WF²Q 算法实现简单（1 个 struct + 1 个 goroutine），但需要与现有 `ClaimJob` SQL 集成 |
| **Per-tenant DB 连接池** | ❌ 不推荐 | ✅ 自研或用库 | **待定** | Go `database/sql` 原生不支持 per-tenant pool，需要包装层。pgxpool 作为候选 |
| **指标过滤** | ⚠️ 可自建 | ✅ Prometheus client | **Prometheus client 扩展** | 使用 `promhttp.HandlerOpts` 的 `Gatherer` 包装，不引入新依赖 |
| **分布式协调** | ❌ 不推荐 | ✅ etcd/Redis | **视情况** | 当前需求在单体范围内，分布式租户隔离是后期扩展 |

### 4.2 第三方依赖评估标准

对于必须引入的依赖，评估矩阵：

| 标准 | 权重 | 说明 |
|------|------|------|
| **License 兼容性** | ❌ 阻断 | 必须是 OSI 认证的开源许可证，排除 AGPL |
| **Go 版本兼容** | ⚠️ 必须 | 必须支持 Go 1.25（项目当前版本） |
| **依赖深度** | 高 | 引入的 transitive deps 不超过 5 个，避免依赖膨胀 |
| **API 稳定性** | 高 | 必须是 v1.x 且 breaking change 频率 < 1年/次 |
| **社区活跃度** | 中 | 最近 6 个月有提交，issue 响应 < 2 周 |
| **Go 标准库替代** | ⚠️ 否决 | 如果标准库能完成（`sync`、`context`、`database/sql`），不引入第三方 |

### 4.3 当前代码库无需引入的新技术栈

根据 v39 分析，**明确不需要**引入：

| 技术 | 被提出的原因 | 为什么不需要 | 替代方案 |
|------|------------|------------|---------|
| Redis | 用于 SSE 跨进程广播 | 单体进程内不需要，未来分布式场景再引入 | `chan` + `sync.RWMutex` |
| etcd | 用于租户配额协调 | 单体不需要分布式协调 | `sync.Map` + 配置热加载 |
| RabbitMQ/Kafka | 用于 Job Queue 持久化 | 当前 Job Queue 基于 DB 表（已持久化），FIFO→WF²Q 是算法变更而非存储变更 | 修改 `ClaimJob` SQL + 内存调度器 |
| gRPC | 用于内部服务通信 | 单体仅有一个进程 | 函数调用 |
| envoy/linkerd | 用于租户级流量控制 | Application-level 的租户隔离更适合在代码中实现 | `middleware.ConcurrencyLimiter` 的 per-tenant variant |

### 4.4 需要评估的技术

| 技术 | 评估领域 | 决策时间点 |
|------|---------|-----------|
| `pgxpool` 包装实现 per-tenant 连接池 | 方向 3 实施时 | Postgres 模式下需要时 |
| `prometheus/client_golang` 的 `Registry` 多实例 | 方向 4 实施时 | 过滤方案采用多 Registry 时 |
| `hashicorp/go-memdb` 用于内存中的租户配额状态 | 方向 3 实施时 | 替代 `sync.Map`，如果状态查询复杂度超过 O(1) |

---

## 5. 实施路线图

### 5.1 优先级矩阵

基于 v39 分析的**严重性 × 修复成本 × 依赖关系**综合评估：

```
      修复成本
       ↓
  低  ┌────────────┬────────────┐
      │ 🔥 方向1   │ 方向4      │
      │ SSE泄漏    │ Metrics    │
      │ (1-2天)    │ (1-2天)    │
      ├────────────┼────────────┤
      │ 方向5(JOB) │ 🔧 方向2   │
      │ per-tenant │ Worker     │
      │ cap(3-5天) │ 健康(1-2周)│
      ├────────────┼────────────┤
      │            │ 🔧 方向3   │
      │            │ 资源隔离    │
      │            │ (2-4周)    │
  高  └────────────┴────────────┘
       低          高
          ← 严重性 →
```

### 5.2 阶段划分

#### 🔥 Phase 0：立即修复（Week 1）— 止血

| 任务 | 工作量 | 产出 | 风险 |
|------|--------|------|------|
| **P0a: `Bus.Unsubscribe(ch)` + SSE handler 调用** | 1 天 | 消除 channel 泄漏 | 低 — 接口添加，不影响现有调用方 |
| **P0b: `/metrics` 分级暴露（配置开关默认关）** | 1-2 天 | `METRICS_AUTH_REQUIRED` 配置 + 过滤中间件 | 低 — 配置默认关闭，向后兼容 |
| **P0c: SSE_MAX_CONNECTIONS + sse_connections_active 指标** | 0.5 天 | 连接上限保护 | 低 — 新增环境变量 + 计数器 |

**Phase 0 完成标志：** 运行 24 小时后，SSE 连接断开后 `sse_connections_active` 正确归零，`/metrics` 无未认证泄露。

#### 🟠 Phase 1：短期基础设施（Week 2-3）— 奠定基础

| 任务 | 工作量 | 产出 | 风险 |
|------|--------|------|------|
| **P1a: `internal/runtime` 包 + Supervisor** | 3-5 天 | Worker 生命周期管理、panic recovery、重启策略 | 中 — 需确保不破坏现有 goroutine 启动模式 |
| **P1b: HealthRegistry + /readyz 增强** | 2-3 天 | 每个 worker 注册健康状态，`/readyz` 返回聚合状态 | 低 — 新端点，不修改现有 `/healthz` |
| **P1c: Worker 指标暴露** | 1-2 天 | `worker_up`、`worker_restarts_total`、`worker_last_success` | 低 — 新增 Prometheus metric |

**Phase 1 完成标志：** `curl /readyz` 返回每个 worker 的健康状态 JSON；手动 `panic` 一个 worker 后，指标显示重启计数器 +1。

#### 🟡 Phase 2：租户隔离（Week 4-6）— 公平性建设

| 任务 | 工作量 | 产出 | 风险 |
|------|--------|------|------|
| **P2a: Job Queue per-tenant cap** | 2-3 天 | 入队时检查 per-tenant pending 上限 | 低 — 纯 SQL 过滤 + 配置项 |
| **P2b: Job Queue 优先级** | 3-4 天 | `ClaimJob ORDER BY priority, created_at` | 中 — 需要处理现有 pending 作业的迁移 |
| **P2c: Per-tenant ConcurrencyLimiter** | 2-3 天 | 在全局限流之上叠加 per-tenant 权重的并发控制 | 中 — 需处理 tenant 上下文传递 |

**Phase 2 完成标志：** 一个租户的批量导入不会影响其他租户的作业延迟；per-tenant 并发超限的请求返回 429 而非影响全局。

#### 🔵 Phase 3：高级特性（Week 7-10）— 成熟度提升

| 任务 | 工作量 | 产出 | 风险 |
|------|--------|------|------|
| **P3a: Job Queue WF²Q 调度** | 3-5 天 | 租户加权公平调度 | 中 — 算法调试需要场景覆盖 |
| **P3b: Per-tenant 资源配置** | 3-5 天 | YAML 配置 + 运行时动态调整 | 中 — 配置热加载需测试 |
| **P3c: 多 Registry 指标隔离** | 2-3 天 | 替代 Phase 0 的 Gather 后过滤方案 | 低 — 内部重构，对外接口不变 |

### 5.3 里程碑

```
Week 1
  M0: 🔥 SSE 泄漏修复 + /metrics 认证
  └─ 验证：24h 稳定运行，指标无泄漏

Week 3
  M1: 🟠 Worker 生命周期管理上线
  └─ 验证：/readyz 反映 worker 真实健康状态

Week 6
  M2: 🟡 租户隔离 Phase 1（Job cap + Concurrency）
  └─ 验证：单租户压力测试不影响其他租户

Week 10
  M3: 🔵 高级调度 + 配置化资源隔离
  └─ 验证：WF²Q 下多租户作业延迟分布符合预期权重
```

### 5.4 风险矩阵与缓解策略

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| `Bus.Unsubscribe` 导致 broadcast 时并发修改切片 | 中 | 高 — 竞争条件导致 panic 或 deadlock | 使用 `sync.RWMutex`：broadcast 用 RLock，Unsubscribe 用 Lock |
| Supervisor panic recovery 掩盖真正的 bug | 中 | 中 — 无限的 restart 掩盖了根本原因 | 限制重启频率（1 次/秒 → 指数退避至 60s），每次重启输出完整 stack trace |
| Per-tenant cap 配置不当导致合法用户被限 | 高 | 中 — 影响用户体验 | 默认 cap 设置为"远大于正常使用"（如 1000），SoftLimit + HardLimit 双层控制 |
| Job Queue WF²Q 增加 SQL 复杂度导致查询变慢 | 低 | 中 — 调度延迟增加 | `ClaimJob` 使用索引覆盖 `(tenant_id, priority, created_at)`；profile 后优化 |
| `/metrics` 过滤增加请求延迟 | 低 | 低 — 过滤是 O(n) 遍历 | 多 Registry 方案在注册时就完成分类，抓取时零过滤开销 |

### 5.5 依赖图——实施顺序的约束

```
Phase 0: 方向1 + 方向4
    │ (独立，无依赖)
    ▼
Phase 1: 方向2 (Worker 健康)
    │ (需要方向4的 metrics 基础设施暴露 worker 指标)
    ▼
Phase 2: 方向5 (Job Queue 隔离)  +  方向3 部分 (Concurrency)
    │ (需要方向2的 worker 健康基础设施监控 worker 重启)
    │ (需要方向1的 lifecycle 接口管理 subscription)
    ▼
Phase 3: 方向3 完整 (资源隔离)  +  方向5 WF²Q
    │ (需要 Phase 2 的 per-tenant cap 和优先级作为基础)
    │ (需要方向2来监控隔离效果)
```

**结论：** v39 建议的顺序（1→4→2→5→3）是合理的，但需要补充 Phase 1 和 Phase 2 之间的依赖关系——方向 5 的 job 隔离需要方向 2 的 worker 健康来监控 job worker 的健康。同时方向 3 的并发控制依赖方向 1 的 lifecycle 接口（SSE 连接也是一种"资源"）。

---

## 总结

v39 的 5 个方向揭示了 AeroVault **一个核心架构缺口**：缺少统一的进程内运行时管理抽象层。5 个方向不是 5 个独立的 bug fix，而是同一个架构问题的 5 个不同侧面。

**核心建议：**

1. **立即止血（Phase 0）**：修复 SSE 泄漏 + /metrics 认证，作为独立的安全补丁先行
2. **短期基建（Phase 1）**：引入 `internal/runtime` 包，统一生命周期管理、健康注册、Supervisor
3. **中期公平（Phase 2）**：在基础设施之上构建租户级资源隔离和公平调度
4. **长期成熟（Phase 3）**：高级调度算法和完全可配置的租户资源管理

**一个关键设计决策贯穿所有阶段：** "是否引入统一的 `ResourceManager` 抽象层"。

- **选 `是`**：架构更整洁，扩展性更好，但 Phase 0 的直接修复会变成临时方案，Phase 1 需要落地
- **选 `否`**：5 个方向各自独立修复，更快交付，但架构碎片化，后续每增加一个"运行时管理"需求都需要重复实现

**我的推荐：** 选 `是`。引入 `internal/runtime` 包作为 Phase 1 的核心产物，方向 1 和方向 2 作为这层抽象的 killer app。这是 AeroVault 从"功能完整的单体"演进为"生产就绪的平台"的关键架构投资。
