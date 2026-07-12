现在我对当前代码基线与 v89 分析之间的关系已经有了清晰的认识。以下是我的全面架构评估。

---

# 架构分析：v89 运营治理盲区 — v144+ 基线状态核查

## 1. 架构评估

### 1.1 现状评估（v89 vs v144+）

根据代码库扫描结果，v89 识别的 5 个方向中，自分析以来系统取得的进展有限：

| 方向 | v89 诊断后的状态 | 当前状态 (v144+) | 评估 |
|------|------------------|------------------|------|
| **1. 运维韧性** | 优雅关闭完全不存在 | ✅ `internal/shutdown/group.go` 实现了分阶段关闭（HTTP → Bus → Workers → OTel → DB），具备 `sync.WaitGroup` 追踪和超时保护 | **已解决 — 大幅改进**。v89 描述的 `signal.NotifyContext` 问题现已被 `Group.Shutdown` 的分阶段协调所替代 |
| **1b. 配置热加载** | 缺失 | ❌ 代码库中零匹配 "Reload"、"hot reload"、"live reload" | **仍为空白** — 调整日志级别、速率限制或 AI 模型仍然需要进程重启 |
| **2. 跨协议 QoS** | WebDAV 绕过 chi 中间件链；无协议感知速率限制；无租户请求配额 | ❌ `buildDispatcher` 仍直接在 chi 路由外部派发 WebDAV 请求 (main.go:185-190)；`RateLimiter` 仍是全局 token-bucket；`ConcurrencyLimiter` 仍是全局信号量 | **未解决** — WebDAV 仍然是一个设计"后门" |
| **3. 策略-动作鸿沟** | 跨 4 个子系统的完整配置但零执行 | ❌ `WriteAccessLog` 有完整实现 (sql_buckets.go:370) 但**仍无调用方**（仅 `repository.go:290` 有接口定义和 SQL 实现 — 无 middleware 或 handler 调用它）；通知规则存储但事件总线从不查询它们；Legal Hold 仅在 `hardDeleteObject` (file_crud.go:371) 中检查，不在 GET 或 WebDAV 读取路径中检查；`StorageClass` 作为元数据保存但未触发存储迁移 | **根本性未解决** — 这仍然是系统中最具影响力的结构性缺陷。系统接受 S3 API 配置，存储这些配置，但部署从未强制执行这些配置 |
| **4. 成本归因** | 仅 AI（Chat）成本追踪；无存储/网络/请求成本 | ❌ 成本追踪仍局限于 AI Chat 推理（`ai.cost_micros` via `cost.go:costMicros` 和 `chat.go`）。AI 管线中的嵌入/重排序/提取完全未追踪。租户无存储成本核算。`TenantQuota` 无财务相关字段 | **未解决** — 缺乏成本核算意味着多租户 SaaS 运营在财务上是在盲操作 |
| **5. 连接韧性** | SSE goroutine 泄漏、MCP stdio 无超时、EventBus 慢订阅者问题 | ✅ SSE `sse.go:74` 现在在 select 中有 `r.Context().Done()` 监听；**但** MCP stdio 仍使用 `bufio.Scanner` 无 `ReadDeadline`（根据 v143 确认）；EventBus 仍丢弃满缓冲消息 | **部分解决** — SSE 已修复；MCP stdio 和 multipart TTL/GC 仍未解决 |

### 1.2 架构优势

系统架构的哪些方面使这些盲点成为可能（而非例外）：

1. **拥有强抽象的正确设计，但缺少执行/反馈循环**。`Storage`、`Repository`、`EventBus` 抽象层是清晰的，但缺少一个跨层策略引擎来桥接"存储配置"和"根据配置运行"。

2. **开箱即用的安全默认**（AGENTS.md 中的 I5）是有纪律的，但也意味着通知、访问日志、存储类转换等可选功能仍然是完全的存根。

3. **S3 协议适配器的完整性**是值得称道的 — 解析 XML、验证请求、存储配置 — 但这恰恰是问题所在：适配器太完整了，它如实响应用户，却不执行他们所要求的内容。

### 1.3 架构债务

| 债务 | 位置 | 原因 |
|------|------|------|
| **WebDAV 绕过中间件** | `main.go:buildDispatcher` (185-190) | 一个阻止 WebDAV 接收速率限制、认证、租户解析、遥测的技术债务。重构风险：小（将 dav 处理程序移入 chi 路由），但影响到整个请求生命周期 |
| **WriteAccessLog 悬空代码** | `repository/sql_buckets.go:370` + `repository.go:290` | 从未调用的代码 — 维护负担，虚假的覆盖率统计，以及"它应该在工作"的认知错位 |
| **StorageClass 不作为** | `file_crud.go:245` (`StorageClass` 元数据存储) | 对象存储关键属性已保存但从未使用 — 当用户询问"为什么我的标准文件没有迁移到 IA"时，这是个产品问题 |
| **EventBus 丢弃语义** | `bus.go` (broadcast 默认丢弃) | 事件可靠性协议缺失。在数据完整性场景（如保留期后清除映射的 AI chunk）中，丢弃事件会静默导致系统处于不一致状态 |

---

## 2. 扩展方向

### 方向 A：策略-动作引擎（方向 3 的延续 — P0/P1）

**为什么需要**：这是系统中最昂贵的结构性缺口。用户在配置 S3 通知、访问日志、存储类、合法性保留 — 系统接受这些并返回 200 OK — 然后无所作为。对于每个缺失的执行者：
- **通知**：Lambda/Webhook 未触发 → 数据管道断裂
- **访问日志**：审计空白 → 合规风险
- **存储类**：成本超支 → 用户为其声称的层级付费，但获得了全部热存储
- **合法性保留**：合规违规 → 对象在应受限时仍可读取

**核心挑战**：需要一个新的组件——一个`PolicyExecutor`（或等效组件）——它：
1. 收到事件通知时查询存储的策略（通知规则、访问日志配置、生命周期规则）
2. 根据配置路由事件（通知 → 适配器、访问日志 → 写入目标桶、生命周期 → 转换时间表）
3. 处理失败（重试、死信队列、指标）

**架构变更**：
```
EventBus → PolicyExecutor → ─ NotificationDispatcher (SQS/SNS/Lambda/webhook adapters)
                            ├─ AccessLogWriter (调用已存在的 WriteAccessLog)
                            ├─ LifecycleScheduler (为 StorageClass 转换排队作业)
                            └─ LegalHoldEnforcer (在所有读取路径中检查)
```

相反，通知引擎可以替代为非 Worker 节点上的一个进程，而是一个在 `Bus.Publish` 中同步（含 timeout）或异步（通过 JobPool）调用的策略匹配函数。

**对现有系统的影响**：最小。`WriteAccessLog` 已实现。`NotificationRule` 结构体已定义。`LegalHold` 元数据已存储。这纯粹是连接现有片段。

---

### 方向 B：运营治理平面（方向 1+2 合并 — P1）

**为什么需要**：两个盲点共享同一个根因 — 没有部署时机制来保护系统免受自身影响：
- 没有配置热加载 → 运维对生产行为的每次更改都是破坏性的
- 没有协议感知速率限制 → S3 批量操作会压垮交互式 WebDAV/MCP 用户
- 没有租户请求配额 → 一个跑偏的租户可以消耗全局容量
- WebDAV 绕过中间件 → 整个 WebDAV 路径基本上不受治理

**核心挑战**：热加载需要一个组件可以原子更新的共享、线程安全的配置。速率限制需要每个租户、每个协议的 token bucket 实例（保持 O(1) 空间复杂度）。

**架构变更**：
```
ConfigStore (加锁的 atomic.Value)
  ├── rate_limiter.Reload(cfg.RateLimit)
  ├── log_level.Reload(cfg.LogLevel)
  ├── ai_embedder.Reload(cfg.AI)
  └── 等等。

RateLimiter
  ├── global: token-bucket（当前）
  ├── per-protocol: 针对 /s3、/v1、/mcp、/webdav 的路由模式匹配
  └── per-tenant: 数据库中的 rps_quota/burst_quota 字段
```

**对现有系统的影响**：中等。`shutdown.Group` 架构显示了团队可以做到这一点。WebDAV 重构（移至 chi 内部）是一个小风险。每个组件的 `Reload` 接口需要单独实现，但可以从 `rate_limiter` 和 `log_level` 自下而上进行。

---

### 方向 C：统一成本核算 & 租户经济学（方向 4 — P2）

**为什么需要**：就多租户 SaaS 而言，该系统目前在经济上是盲目的。你可以回答"这个租户用了多少 AI token"，但无法回答"这个租户的成本是多少"——而后者才是实际计费中的重要问题。

**核心挑战**：成本核算需要：
1. 定价配置（每个存储后端的每 GB 成本、每个请求成本、每个 AI 调用的嵌入/重排序/提取成本）
2. 使用数据收集（需要对现有 CRUD 路径进行插桩以记录请求并进行分类）
3. 汇总（每日快照作业以将分布式计数转换为持久性成本记录）

**架构变更**：
```
新存储成本表：storage_cost_snapshots (tenant, storage_class, bytes, effective_price_per_gb, snapshot_date, cost_usd_micros)
新请求成本表：request_usage (tenant, operation, protocol, count, date)
扩展 ai_usage：增加 embed_cost_micros, rerank_cost_micros, extract_cost_micros
成本 API：GET /v1/admin/tenants/{t}/cost-summary?period=monthly
```

**对现有系统的影响**：低至中。可以增量进行，从 AI 管线扩展开始（嵌入成本是最简单的 win）到存储层，再到请求级插桩。

---

### 方向 D：数据面完整性 — 删除路径 & Chunk 一致性（来自 v144 方向 1 — P1）

**为什么需要**：v144 对此的分析很精确。保留清除绕过 `ChunkCleaner`，EventBus 丢弃删除事件。这意味着被删除对象的 AI chunk 会在搜索索引中永远残留——这是搜索质量、成本（查询浪费）和合规方面的数据一致性问题。

**核心挑战**：修复保留路径中的 `purgeSoftDeleted` 以调用 `ChunkCleaner`，并修复 EventBus 以不丢失删除事件（缓冲队列或至少死信重试）。

**对现有系统的影响**：极小（保留清除 +20 行，EventBus 中的 `enqueue` 重试逻辑 +50 行）。根本不应该等待。

---

### 方向 E：连接治理 — MCP stdio & Multipart 韧性（方向 5 的延续 — P2）

**为什么需要**：MCP stdio 没有 `ReadDeadline` 意味着一个失效的子进程会无限期挂起父进程。Multipart 上传没有 TTL 意味着中止的上传会无限期地消耗存储空间。这些是运营稳定性问题，在 Cloude Desktop（用于 MCP）或备份工作负载（用于 multipart）的生产部署中会咬人。

**核心挑战**：MCP stdio 修复很小（在 `transport.go` 中设置 `SetReadDeadline`）。Multipart TTL 需要一个新的 reconcile 作业来扫描 `InitMultipart` 记录并与 `AbortMultipart` 协调——约 50-80 行代码。

**对现有系统的影响**：最小 — 独立于核心架构变更。

---

## 3. 接口设计建议

### 3.1 新抽象：`PolicyEngine`

```go
// 在 internal/service/ 或新的 internal/policy/ 中
type PolicyEngine interface {
    // OnEvent 针对给定事件评估并执行存储的策略。
    // 由 EventBus.Publish 在事件对象上调用。
    OnEvent(ctx context.Context, event Event) error
}
```

**设计原则**：
- **单一职责**：引擎评估策略并路由到执行器。它不关心存储后端或数据库细节。
- **可插拔执行器**：通知、访问日志、生命周期、合法性保留各自实现一个 `PolicyExecutor` 接口。
- **失败隔离**：一个执行器失败不会影响其他执行器。所有失败都记录指标但不会破坏主请求路径。
- **无新类型**：重用现有的 `NotificationRule`、`LoggingConfig`、`Object.StorageClass` 类型。

**为什么现在需要这个**：没有这个，方向 3 的每个子问题都需要单独修复。有了策略引擎，我们就有了可扩展的原语来桥接"存储配置"和"执行动作"。

### 3.2 热加载模式：`Reloader`

```go
type Reloader interface {
    Reload(ctx context.Context, cfg *config.Config) error
}
```

所有持有可变状态的组件都应实现这个。配置持有者（rate_limiter、log_level、ai_embedder）首先实现。这应该是一个扩展接口，而不是核心 `Component` 必需接口。

**向后兼容性**：`Reload` 返回错误但不改变内部状态 = 安全更新。实现始终应用变更前进行 dry-run 验证。

### 3.3 WebDAV 重构

当前设计：
```go
// buildDispatcher 在主处理程序返回前分发 WebDAV — 绕过中间件
func buildDispatcher(r *chi.Mux, davH http.Handler, cfg *config.Config) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
        if davH != nil && cfg.WebDAV.Prefix != "" {
            p := req.URL.Path
            if p == cfg.WebDAV.Prefix || strings.HasPrefix(p, cfg.WebDAV.Prefix+"/") {
                davH.ServeHTTP(w, req) // ← 绕过中间件！
                return
            }
        }
        r.ServeHTTP(w, req)
    })
}
```

重构后：
```go
// 将 WebDAV 处理程序作为 chi 路由注册，而非独立的 HTTP 处理程序
r.Group(func(r chi.Router) {
    r.Use(middlewareChain) // 自动继承标准中间件
    r.Handle(cfg.WebDAV.Prefix+"/", davH)
    r.Handle(cfg.WebDAV.Prefix, davH)
})
```

**为什么这是正确的**：chi 路由组自然继承 `RequestID` → `CORS` → `Auth` → `Tenant` → `RateLimit` → `OTel` → `Recoverer` → `AccessLog` 链。WebDAV 不是一种不同的协议——它是一种不同的数据格式，通过 HTTP 运行。它对速率限制、认证和遥测的需求与 REST/S3 端点相同。

### 3.4 EventBus 可靠性契约

当前：
```go
select {
case ch <- e:
default:
    b.dropped.Add(1) // 静默丢弃
}
```

建议：
```go
select {
case ch <- e:
default:
    // 慢订阅者保护：用一个新通道静默替换满通道
    // 旧通道被关闭，事件通过缓冲池保持（至少一次语义）
    b.replaceSlowSubscriber(ch)
    b.dropped.Add(1)
}
```

至少一次交付需要一个带缓冲的排队层和一个确认机制。对于 v1，将 `default: drop` 替换为 `default: enqueue to per-subscriber buffer + async retry`。失败和重试记录在 `webhook_failures` 表中。

---

## 4. 技术选型

### 4.1 通知路由：不需要新框架

**选项 A：内部 Worker Pool（建议）**。使用现有的 `JobPool`（`jobs` 表）来路由通知。匹配通知规则 → 将 `deliver_notification` 作业入队 → Worker 从队列中取出并调用适配器。复用现有的重试/死信机制。

> **适合我们**，因为作业基础设施已经存在（用于 AV、复制、协调）。添加通知路由类型只需要一个新作业类型常量和一个处理程序注册。

**选项 B：外部消息队列（如 RabbitMQ、NATS）**。将事件发布到外部 Broker，由外部 Worker 消费。

> **不适合现在**：导致对生产部署依赖外部基础设施。处于早期阶段的 SaaS 产品应尽可能长时间地保持单体架构。

### 4.2 成本定价：配置驱动，非内置

每个存储后端应该有 YAML/JSON 中的可配置定价：
```yaml
# deploy/pricing/default.yaml
storage:
  local: { per_gb_per_month_usd: 0.02 }
  s3:    { per_gb_per_month_usd: 0.023, per_gb_transfer_out: 0.09 }
  oss:   { per_gb_per_month_usd: 0.019 }
ai:
  embed:   { per_1k_tokens_usd: 0.0001 }
  llm:     { per_1k_prompt_usd: 0.002, per_1k_completion_usd: 0.006 }
  rerank:  { per_1k_docs_usd: 0.001 }
```

**是否应该有第三方依赖**：也许是一个小数算术库。Go 的 float64 不足以用于美元金额（舍入误差）。但我们可以使用 `int64` 美分/微美元（该代码库已经是这种情况）——无需新依赖。

### 4.3 配置热加载：自建 vs 采购

**自建**是这里显而易见的选择：
- 配置已经是 Go 结构体（`config.Config`）
- 需要变更的组件很少（rate_limiter、log_level、ai_embedder）
- 端点很简单：`POST /debug/reload {"component": "rate_limiter"}` 或 `PUT /debug/log-level?level=debug`
- 泛型框架（Viper、envconfig）会增加间接层，但收益仅略高于 20 行 `atomic.Value` 模式

**不要做什么**：不要使用 Kubernetes ConfigMap 热加载（卷挂载 inotify）。它将我们的关闭模型与 K8s 绑定，并对不运行 K8s 的开发/CI 环境进行负优化。

### 4.4 MCP stdio ReadDeadline：对现有依赖的零变更

这不需要任何新依赖。`internal/mcp/transport.go` 中的修正：
```go
// 替换
scanner := bufio.NewScanner(conn)
// 为
conn.SetReadDeadline(time.Now().Add(idleTimeout))
scanner := bufio.NewScanner(conn)
```

每次扫描后重置超时：
```go
for scanner.Scan() {
    conn.SetReadDeadline(time.Now().Add(idleTimeout))
    // ...
}
```

---

## 5. 实施路线图

### 优先级排序

| 优先级 | 方向 | 理由 |
|--------|------|------|
| **P0** | 方向 D：删除路径完整性（保留路径中调用 ChunkCleaner + EventBus 不丢弃删除事件） | 这是现实中的数据损坏。删除的对象在搜索中仍然可见。它很小（~70 行），阻塞了生产就绪性 |
| **P1** | 方向 A：策略-动作引擎 — 首要目标：通知规则 + 访问日志 | 最大价值与工作量比。`WriteAccessLog` 已经实现——只需调用它。通知规则的 CRUD 路径已完成——只需连接事件总线。这部分上线意味着 4 个结构性缺口中有 2 个立即关闭 |
| **P1** | 方向 A：策略-动作引擎 — 次要目标：Legal Hold GET 拦截 | ~10 行代码。合规阻止程序。应该是"一个周五下午"的实现 |
| **P1** | 方向 B：运营治理 — WebDAV 修复 + 速率限制策略 | WebDAV 绕过中间件是一个架构危险信号。修复速率限制以支持基于路径的规则是实用主义的下一步 |
| **P2** | 方向 B：运营治理 — 热加载 | 与当前功能无关，但在按请求改动前对生产运营至关重要，以最小化中断 |
| **P2** | 方向 C：统一成本核算 — AI 全链路扩展 | 低难度（仅在现有嵌入/重排序/提取路径中增加 cost_micros），为后续成本工作奠定基础 |
| **P2** | 方向 E：连接治理 — MCP stdio + Multipart TTL | 低难度，高可靠性影响 |
| **P3** | 方向 B：运营治理 — 租户级请求配额 | 需要数据库模式变更 + 新端点。真正的努力 |
| **P3** | 方向 A：策略-动作引擎 — StorageClass 转换 + 生命周期 | 需要多后端路由基础设施。依赖于方向 2（v87/v88） |
| **P3** | 方向 C：统一成本核算 — 存储成本快照 + 统一 API | 有赖于定价模型定义和每日汇总作业 |

### 第一阶段（P0 — 1 周）

| 里程碑 | 交付物 |
|---------|---------|
| **M0.1** | `purgeSoftDeleted` 在保留期间硬删除后调用 `ChunkCleaner.DeleteObjectChunks` (retention.go) |
| **M0.2** | EventBus 为删除事件（至少缓冲重试）使用非丢弃策略，或 Indexer 增加并行的基于协调的兜底（reconcile 作业来检测并清理孤儿 chunk） |

### 第二阶段（P1 — 2 周）

| 里程碑 | 交付物 |
|---------|---------|
| **M1.1** | `AccessLog` middleware 或 `FileService.Put`/`Get`/`Delete` hook 调用 `WriteAccessLog`。用于避免循环的自我记录旁路逻辑 |
| **M1.2** | `EventBus.Publish` 查询 `GetBucketNotifications`，匹配事件类型。初始适配器：webhook（复用现有的 `EVENTS_WEBHOOK_URL` 模式） |
| **M1.3** | Legal Hold 检查添加到 `FileService.Get` 和 `FileService.Stat`。返回 `423 Locked` |
| **M1.4** | WebDAV 从 `buildDispatcher` 移至 chi 路由组。验证所有中间件（Auth、RateLimit、Tenant、OTel、AccessLog）均适用于 WebDAV 请求 |

### 第三阶段（P2 — 4 周）

| 里程碑 | 交付物 |
|---------|---------|
| **M2.1** | 热加载框架：`POST /debug/reload` 端点 + `Reloader` 接口。至少 `log_level` + `rate_limiter` |
| **M2.2** | AI 全链路成本扩展：`ai_usage` 中的 `embed_cost_micros`、`rerank_cost_micros`、`extract_cost_micros` + 迁移 |
| **M2.3** | MCP stdio `ReadDeadline` 超时 + 健康探测 |
| **M2.4** | Multipart 上传 TTL：`InitMultipart` 记录超时时间 + reconcile 作业扫描并中止过期上传 |

### 第四阶段（P3 — 长期）

| 里程碑 | 交付物 |
|---------|---------|
| **M3.1** | 通知引擎：SQS/SNS/Lambda 适配器 + 死信重试 |
| **M3.2** | 租户级请求配额（`rps_quota`/`burst_quota` 数据库字段 + 速率限制实现） |
| **M3.3** | 存储成本核算：每日快照作业 + 后端定价配置 + `cost-summary` API |
| **M3.4** | StorageClass 生命周期转换（storage tiering + 跨后端路由） |

### 风险与缓解

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| 通知引擎使 `Bus.Publish` 变慢 | 中 | 高 — 请求路径延迟增加 | 异步处理：将通知路由卸载到 JobPool。`Bus.Publish` 仅存储事件 + 入队通知作业。JobPool worker 执行路由 |
| 热加载使系统处于不一致状态 | 低 | 高 | 始终在提交时 dry-run 验证新配置。失败时保持旧配置。操作者可见的审计日志 |
| WebDAV 重构破坏 WebDAV 请求 | 低 | 中 | `buildDispatcher` 周围的全面集成测试（WebDAV PROPFIND、MKCOL、PUT、GET、DELETE 在重构前后验证） |
| 成本核算数据使数据库膨胀 | 中 | 低 | 成本快照是纯聚合（每日一行，非每请求）。可接受的行增长。旧聚合的保留策略（7 年？） |
| 解决策略-动作鸿沟的工程工作被视为"非功能性"并推迟 | 高 | 高 | **这就是需要优先处理的原因。** 用户配置了通知规则。它们不工作。这本身就是非功能性，并且用户会将其视为功能错误。AGENTS.md 强制要求"所有业务逻辑必须可测试"——通知规则具有可测试的业务逻辑，但完全没有 |

---

## 总体建议

1. **立即行动（P0）**：修复删除路径（v144 方向 1 保留 + 事件丢弃）。这是数据完整性问题，不是功能增量。AGENTS.md 中的 I1 先例适用于此。

2. **下一个冲刺（P1）**：策略-动作引擎（方向 A）。投入一周时间专门连接 `WriteAccessLog`、`EventBus.Publish → NotificationRules` 和 Legal Hold GET 检查。这三个都已完成 70-90%——只是最后一步缺失。

3. **规划下一阶段（P2）**：WebDAV 中间件修复（向后兼容 — WebDAV 行为不变，只是治理范围扩大到包括它）。随后进行热加载和连接治理（MCP stdio、Multipart TTL）。

4. **长期（P3）**：统一成本核算和 StorageClass 转换。这些都是真正的产品差异化因素，但需要在核心执行引擎就位后才具备基础。

**核心洞察**：无法通过单次修复来填补策略-动作鸿沟。这是一个结构性模式，需要一个新的中间件——一个`PolicyEngine`——来桥接"配置存储"和"动作执行"。一旦这个引擎就位，通知、访问日志、生命周期和 LegalHold 都可以同时闭合。没有这个引擎，你就是在打地鼠。
