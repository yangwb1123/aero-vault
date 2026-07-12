# AeroVault 架构深度分析报告

> **分析对象：** `expansion-v138-architecture-blindspots-and-extension-directions.md`  
> **视角：** 资深架构师  
> **核心方法：** 全代码库模式识别 → 跨域结构性映射 → 隔离度评估 → 演进路径设计  
> **原则：** 不编写具体代码，聚焦架构权衡与决策

---

## 一、架构评估：优势、局限性与技术债务

### 1.1 现有架构的优势

当前 AeroVault 在架构层面有若干经过验证的正确设计决策，值得首先肯定：

| 决策 | 设计合理性 | 证据 |
|------|-----------|------|
| **协议适配器模式** | 正确。四套协议（REST/S3/WebDAV/MCP）各自为独立薄层，共享 `FileService` 核心，避免了每个协议实现一套完整业务逻辑的灾难性重复。这不仅减少了代码量，也**强制了单点行为变更** | 所有 CRUD 操作最终汇聚到 `internal/service/file_*.go` |
| **事件驱动的工作者分离** | 正确。EventBus 将写入路径（CRUD）与读取/处理路径（Indexer/AV/Replication/Webhook）解耦，使写入延迟不依赖后续处理的完成程度 | `internal/events/bus.go` + `internal/workers/` |
| **Opt-in 默认关闭策略** | 正确。AI、pgvector、Qdrant、集群单例、WebDAV 等能力默认关闭，使得 CI gate（SQLite + local FS + 无鉴权）始终是确定性基线 | `AGENTS.md` I5 规则 + 全部 flag-gated |
| **分层中间件链** | 治理边界清晰。`RequestID → CORS → Auth → Tenant → RateLimit → OTel → Recoverer → AccessLog` 的顺序固定且不可变，handler 不自挂中间件 | `I4` 不变量 + `main.go:applyMiddleware` |
| **Storage + Repository 双层抽象** | 存储与元数据分离。允许独立演进（local ↔ S3 ↔ OSS ↔ COS），Repository 只描述元数据模式，Storage 只处理字节流 | `internal/storage/` + `internal/repository/` |

### 1.2 核心局限性（结构性盲区）

v138 文档精准识别了 5 个盲区，但在我进一步细读代码模式后，这 5 个盲区还暴露了更深层的结构性局限：

#### 1.2.1 "配置完备但执行缺失"是架构级模式，不是孤立 bug

这是本报告**最重要的架构洞察**。v138 方向三正确识别了 4 个实例（通知/日志/存储类/LegalHold），但这个模式在代码库中**更深层的投射**包括：

- **Bucket Lifecycle 规则：** S3 API 可以 PUT/GET lifecycle 配置，但只有 `Expiration.Days` 被 `Reconcile` 消费——`Transition`, `NoncurrentVersionExpiration`, `AbortIncompleteMultipartUpload` 等策略字段被解析存储却零执行
- **Bucket Policy / ACL：** S3 API 存储了 Policy JSON 和 ACL XML，但 `FileService` 的鉴权路径完全依赖自己的中间件体系，不读取 bucket policy 做访问决策
- **CORS Configuration：** S3 API 写入 CORS 规则到 DB，但 REST API 的 CORS 行为由 `middleware/cors.go` 静态配置决定，与 bucket 级 CORS 规则完全无关

**这意味着什么？** —— 系统在 S3 协议层打造了一个完整的"配置面"，但这个配置面与"执行面"之间存在**系统性断连**。用户通过 S3 API 设置的所有规则，**系统承诺了存储但未承诺执行**。这是 S3 兼容性中的最严重缺陷：用户信任 `PUT /bucket?notification → 200 OK` = "配置成功"，但系统行为是"配置被保存了，仅此而已"。

**根本原因**不是编码疏忽，而是**架构层面没有为"策略执行引擎"预留位置**。`main.go` 的装配顺序（`config → storage → repo → service → workers → middleware → router`）中没有"策略引擎"或"规则引擎"这样的组件。

#### 1.2.2 协议适配器的隔离度不等

虽然四套协议共享 `FileService` 是正确设计，但各适配器的**治理隔离度**存在严重不均衡：

| 适配器 | 中间件覆盖 | 鉴权一致性 | 速率限制 | 备注 |
|--------|-----------|-----------|---------|------|
| REST | ✅ 全链 | ✅ 通过 chi middleware | ✅ 通过 chi middleware | 治理最完善 |
| S3 | ✅ 全链 | ✅ SigV4 + chi middleware | ✅ 通过 chi middleware | 治理完善 |
| WebDAV | ❌ **完全绕过** | ❌ 独立鉴权路径 | ❌ 无 rate limit | **架构后门** |
| MCP HTTP | ✅ 全链 | ✅ 通过 chi middleware | ✅ 通过 chi middleware | 正常 |
| MCP stdio | ❌ **无中间件** | ❌ 进程内直接调用 | ❌ 无限制 | **隧道效应** |

WebDAV 绕过中间件链不是疏忽，而是**架构决策遗留**：`buildDispatcher` 在设计上将 WebDAV 作为"替代路由入口"，与主 chi router 并列。这个决策在当时可能为了快速集成 `xwebdav.Handler`，但导致了 WebDAV 在治理层面成为"二等公民"——速率限制、鉴权标准路径、审计日志等均不适用。

#### 1.2.3 后台工作者缺少生命周期契约

当前系统有 5 类后台工作者（Indexer、AV Scanner、Replication、Reconcile/GC、Webhook retry），但 `internal/workers/` 目录下不存在统一的 Worker 抽象。每个工作者通过 `go func() { for { select { case <-ctx.Done(): return; ... } } }()` 的模式运行，但：

- 无标准化的 `Start(ctx) / Stop(ctx) error` 接口
- 无统一的生命周期状态（Running/Draining/Stopped）
- 无 in-flight 任务计数
- 无向主控报告健康状况的机制

这与方向一的"排空窗口"问题直接相关：`main.go` 无法在关闭前确定所有后台工作者已完成。

### 1.3 架构债务与技术债

| 债务类型 | 严重程度 | 具体表现 | 影响 |
|---------|---------|---------|------|
| **架构债务** | 🔴 高 | WebDAV 绕过中间件链 | 安全治理空洞；QoS 不可控 |
| **架构债务** | 🔴 高 | 策略-动作鸿沟（4 子系统） | S3 兼容性承诺与行为不一致，用户信任损伤 |
| **设计债务** | 🟡 中 | 后台工作者无统一生命周期接口 | 优雅关闭不可靠；健康检测不真实 |
| **设计债务** | 🟡 中 | SSE/ChatStream 无客户端断开检测 | goroutine 泄漏（方向五已识别） |
| **实现债务** | 🟢 低 | MCP stdio 无 ReadDeadline | 僵尸进程风险 |
| **实现债务** | 🟢 低 | EventBus 慢订阅者无保护 | 背压导致事件丢失 |

---

## 二、扩展方向：三个高价值的架构演进路径

基于 v138 的 5 个方向 + 我的独立分析，我提出 **3 个优先级的架构扩展方向**，每个对应一个重大的治理或演进维度：

### 方向 A（优先级 P0）：策略引擎层 —— 弥合配置-执行鸿沟

#### 为什么需要

这是**S3 兼容性最致命的产品缺陷**。用户在 S3 API 上配置了通知规则、访问日志、存储类、Lifecycle 策略、Legal Hold、Bucket Policy——但系统仅存储这些配置而不执行。在 SaaS 场景中，这意味着：

- 用户选择 AeroVault 替代 AWS S3 进行迁移 → 配置了事件通知后 Lambda 不触发 → **迁移项目失败**
- 合规审计要求 Legal Hold 阻止所有访问路径 → GET 仍返回数据 → **合规事件**
- 客户配置 STANDARD_IA 期望降低成本 → 账单无变化 → **信任崩塌**

这已经不是"功能缺失"而是**契约违背**——HTTP 200 OK 在 S3 协议语义中意味着"配置已生效"，而非"配置已存储"。

#### 核心挑战和技术难点

| 挑战 | 难度 | 说明 |
|------|------|------|
| **策略模型统一** | ★★★ | 通知规则、访问日志、存储类转换、Legal Hold、Bucket Policy——五个策略域有不同的数据模型、生命周期和失败语义。统一到一个策略引擎中需要分层抽象而非平铺实现 |
| **执行时机** | ★★★ | 同步执行（Legal Hold GET 拦截）vs 异步执行（通知投递）vs 定时执行（存储类转换）——不同策略需要不同的执行触发器 |
| **顺序与冲突** | ★★★ | 同文件上 Legal Hold + Object Lock + Retention 同时设置时，优先级和冲突解决策略是什么？——当前无模型 |
| **性能保证** | ★★ | 通知引擎不得成为请求热路径的瓶颈——需要 worker pool + 背压机制 |

#### 预期架构变更

```
当前：
  S3 API → 存储到 DB ───────────────────────────  [无人读取]

变更后：
  S3 API → 存储到 DB ───→ Policy Engine ───→ Notification Dispatcher
                                │                → AccessLog Writer
                                │                → Lifecycle Scheduler
                                │                → Legal Hold Enforcer
                                │                → Bucket Policy Evaluator
                          [统一的策略评估与执行层]
```

具体组件：
- `internal/policy/` 新包，包含：
  - `Engine`：策略注册、匹配、执行调度
  - `Rule`：统一的策略规则模型（可扩展策略域）
  - `Action`：执行接口（`type Action func(ctx context.Context, event PolicyEvent) error`）
- `internal/notification/` 取代或扩展 `internal/events/bus.go` 中的通知路由部分
- `internal/accesslog/` 封装现有的 `WriteAccessLog` 为可调用的 Action

#### 对现有系统的影响

| 影响 | 程度 | 说明 |
|------|------|------|
| 现有接口 | 🟢 无破坏性 | `internal/repository/sql_buckets.go` 中存储配置的方法不变，仅在读取侧新增消费者 |
| 现有事件模型 | 🟡 需要调整 | `EventBus.Publish` 可能需要扩展为携带 `bucket` 上下文，以便策略引擎按桶匹配规则 |
| FileService | 🟢 无影响 | 策略引擎作为新组件，与 FileService 并列引用而非修改其内部 |
| 部署 | 🟢 无影响 | 新组件可选启用（`POLICY_ENGINE_ENABLED`），默认关闭以保持 CI gate 独立 |

### 方向 B（优先级 P1）：协议治理统一层 —— QoS 与安全编排

#### 为什么需要

当前 WebDAV 绕过中间件链是**安全治理的架构后门**。MCP stdio 模式下无任何治理。随着多协议用户增加（S3 批量同步 + REST API 查询 + WebDAV 交互 + MCP AI 工具调用同时存在），缺乏协议感知的速率限制导致：

- 一个低优先级的批量操作可能饿死高优先级的交互式请求
- 管理员无法在负载情况下保护管理 API
- MCP stdio 模式下没有任何监管约束

这不是"优化性能"而是**平台可运营性的基础能力**——没有协议感知的 QoS，系统就无法在多租户多协议场景下提供可预测的服务质量。

#### 核心挑战和技术难点

| 挑战 | 难度 | 说明 |
|------|------|------|
| **MCP stdio 的治理可达性** | ★★★★ | stdio 模式运行在用户进程中，完全脱离 HTTP 上下文的约束。如何在不引入 HTTP 依赖的前提下对 stdio 会话施加速率限制和鉴权？——需要在 MCP 传输层植入治理中间件 |
| **跨协议优先级仲裁** | ★★★ | S3 批量 GET 的 "low priority" 在 FileService 层面如何体现？——FileService 当前无请求优先级概念。需要为每个请求分配一个 priority 元数据，并让 FileService 据此调整资源分配（如限制并发、控制缓冲区） |
| **租户级配额的全局一致性** | ★★★ | 租户配额应在 HTTP 中间件层 + MCP 传输层 + FileService 层三层同时生效。如何保证三个层面感知同一个配额状态？——需要共享的配额状态存储（例如 Redis 或 Repository） |

#### 预期架构变更

```
当前：
  WebDAV ──→ dispatch(绕过chi) ──→ FileService
  MCP stdio ──→ FileService(无治理)

变更后：
  所有协议 ──→ Protocol Governance Layer ──→ FileService
                  ├─ Rate Limiter (protocol-aware, path-based)
                  ├─ Tenant Quota Enforcer
                  ├─ Priority Scheduler
                  └─ AccessLog Writer (统一记录)
```

具体变更：
- `internal/middleware/` 中的 `RateLimiter` 重构为支持多协议匹配规则
- `buildDispatcher` 重构：WebDAV handler 注册为 chi route 而不是 dispatch-level 截获
- `internal/mcp/` 传输层增加治理中间件（`middlewareChain` for stdio transport）
- `FileService` 增加请求优先级标记（通过 context 传递）

#### 对现有系统的影响

| 影响 | 程度 | 说明 |
|------|------|------|
| `buildDispatcher` | 🟡 需重构 | WebDAV 路由方式从 dispatch-level 截获改为 chi route 注册——需充分测试确保路由行为一致 |
| MCP stdio | 🟡 需扩展 | stdio transport 需要支持治理中间件链——这是新增接口，不影响现有 MCP 工具注册 |
| FileService 接口 | 🟢 新增安全 | 请求优先级可通过 context 传递，不破坏现有方法签名 |
| 现有 route 路径 | 🟢 无损 | REST、S3、MCP HTTP 路由路径不变 |

### 方向 C（优先级 P1）：后台工作者生命周期管理框架

#### 为什么需要

当前优雅关闭路径中，`main.go` 对所有后台工作者执行的是"盲 shutdown"——发信号后等待 15 秒，不管任务是否完成。这在生产环境中有三个风险：

1. **部分完成任务**：Indexer 写入了一半 chunk，Replication 复制了一半对象——留下脏状态
2. **资源泄漏**：goroutine 未正常退出导致 `sync.Pool`、`*sql.DB` 连接、`*os.File` 句柄泄漏
3. **启动风暴**：重启后所有工作者同时开始工作，可能压垮外部依赖（LLM endpoint、目标存储）

这组问题映射到更一般的架构需求：**系统需要一个统一的后台任务生命周期管理框架**。这是一个可复用的基础设施层，不仅仅服务于优雅关闭。

#### 核心挑战和技术难点

| 挑战 | 难度 | 说明 |
|------|------|------|
| **取消语义的统一** | ★★★ | `context.Context` 被取消后，Indexer 应回滚未提交写入，Replication 应记录断点，AV Scanner 应标记检查状态——每个工作者对"取消"的行为定义不同。框架不能假设所有工作者对 cancel 的响应一致 |
| **排空顺序的确定** | ★★ | Replication 应排空后再关闭 Indexer（避免新增对象无索引），Bus 应在所有工作者排空后关闭——依赖序的声明比实现更难 |
| **任务断点与恢复** | ★★★★ | 若 Indexer 处理大文件到一半被关闭，重启后如何恢复？——需要确立"最小可恢复单元"的概念（按文件粒度？按 chunk 粒度？） |
| **工作者健康告警** | ★★ | 工作者 stuck（goroutine 不响应 cancel）时如何检测与告警？——需要 watchdog goroutine 配合 `select` 超时 |

#### 预期架构变更

```
当前：
  type Config struct { ... }      // 每个工作者持有自己的配置
  go worker.Run(ctx)              // 无统一生命周期

变更后：
  type Worker interface {
      Name() string
      Run(ctx context.Context) error      // 主循环
      Drain(ctx context.Context) error    // 排空 in-flight 任务
      Status() WorkerStatus               // Running/Draining/Idle/Stuck
  }

  type WorkerManager struct {     // 生命周期管理器
      workers []Worker
      wg      sync.WaitGroup
      state   atomic.Value       // Running / Draining / Stopped
  }
  // Start all → Wait ready → Signal drain → Wait drain → Close
```

具体变更：
- `internal/worker/` 新包（注意：现有 `internal/workers/` 可能需调整）
  - `Worker` 接口定义
  - `WorkerManager` 编排组件
  - `WorkerStatus` 状态枚举
- 现有工作者逐一适配 `Worker` 接口

#### 对现有系统的影响

| 影响 | 程度 | 说明 |
|------|------|------|
| 现有工作者 | 🟡 需适配 | Indexer/AV/Replication/Reconcile 需实现 Run+Drain+Status 方法——这是增量改动，不破坏现有逻辑 |
| EventBus | 🟢 无影响 | Bus 作为工作者之一参与生命周期管理，但不改变其事件模型 |
| main.go | 🟡 重构 `runServer` | 关闭路径从 `srv.Shutdown → bus.Close` 变为 `srv.Shutdown → manager.DrainAll → manager.CloseAll` |
| 不影响 CI gate | 🟢 安全 | 工作者生命周期管理是纯运维改进，不影响 CRUD 路径的测试 |

---

## 三、接口设计建议

### 3.1 Strategy-Executor 模式替代当前"存储即执行"假设

**问题诊断：** 当前代码库中，策略配置（`NotificationRule`、`LoggingConfig`、`StorageClass`）被存储后即假定为"已完成"——系统从未在读取侧建立"执行循环"。这本质上是**Store-and-Silence** 模式而非 **Store-and-Act** 模式。

**建议引入的接口模式：**

```
// Strategy: 可被持久化的策略定义
type Strategy interface {
    Type() string       // "notification" | "access_log" | "lifecycle" | ...
    Match(event any) bool  // 判断事件是否匹配此策略
    Priority() int
}

// Executor: 策略匹配后的动作执行器
type Executor interface {
    Execute(ctx context.Context, event Event) error
    // 同步或异步执行，由 Executor 自身决定
}

// Engine: 策略注册、匹配与执行调度
type Engine interface {
    RegisterStrategy(strategy Strategy) error
    RegisterExecutor(executor Executor) error
    // 注册策略域，例如 notification_strategy → notification_executor
    Evaluate(ctx context.Context, event Event) error
    // Evaluate 对所有匹配策略调用对应 Executor
}
```

**为什么需要这个接口层而不直接调用？**

- **可扩展性**：新的策略域（如未来可能的 Bucket Policy 鉴权）只需新增 Strategy + Executor 实现，无需修改 Engine
- **可测试性**：Strategy 和 Executor 各自可独立单元测试，Engine 可 mock 执行
- **失败隔离**：一个 Executor 的失败不阻断其他 Executor 的执行

### 3.2 Protocol-Governed Transport 接口

**问题诊断：** 当前治理（RateLimiter/Auth/Tenant）依赖 HTTP 中间件链，MCP stdio 完全绕过。接口不在传输层而是在协议层。

**建议引入 Transport 抽象：**

```
// Transport 是协议无关的请求-响应载体
type Transport interface {
    // 治理中间件——在每个 Transport 上统一植入
    Use(middleware func(Handler) Handler)

    // 路由注册
    Handle(path string, handler Handler)

    // 启动/停止
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
}

// Handler 是携带治理上下文的请求处理函数
type Handler func(ctx context.Context, req Request) Response

// Request/Response 是协议无关的消息结构
type Request struct {
    Protocol string          // "s3"/"rest"/"webdav"/"mcp"
    Tenant   string
    Path     string
    Method   string
    Priority PriorityClass
    // ...
}
```

**设计要点：**
- HTTP/HTTPS Transport 是一个具体实现（包装 chi router）
- stdio Transport 是另一个实现（包装 `bufio.Scanner` + `os.Stdout`）
- 治理中间件（RateLimiter、Auth、TenantExtractor）写一次，在所有 Transport 上复用
- **向后兼容**：现有 `internal/api/rest/`, `internal/api/s3compat/`, `internal/mcp/` 中的 handler 签名不变，由 Transport 负责将通用治理上下文转换为 handler 需要的参数

### 3.3 Worker Lifecycle 接口

**问题诊断：** 当前工作者无统一生命周期接口。

```
// ManagedWorker 是受生命周期管理器管控的工作者
type ManagedWorker interface {
    // 唯一标识，用于日志和指标
    Name() string

    // Run 是工作者主循环。当 ctx 被取消时，Run 应开始排空并返回。
    // 返回 nil 表示正常退出（排空完成）。
    // 返回 error 表示异常退出（需要告警）。
    Run(ctx context.Context) error

    // Drain 提示工作者停止接受新任务并排空 in-flight 任务。
    // contextualTimeoout 参数字段提示工作者期望的排空时长。
    // 返回的新 context 用于监听排空完成。
    Drain(ctx context.Context) (<-chan struct{}, error)

    // Status 返回工作者当前状态
    Status() WorkerStatus

    // Readiness 返回工作者是否就绪（可作为 /readyz 的输入）
    Readiness() ReadinessResult
}
```

**向后兼容设计：**
- 可选的接口实现（`if w, ok := worker.(ManagedWorker); ok { manager.Register(w) }`）
- 未实现 `ManagedWorker` 的现有工作者继续使用 goroutine + `ctx.Done()` 的现有模式——`WorkerManager` 对它们执行标准 `context.WithTimeout` 关闭

---

## 四、技术选型建议

### 4.1 是否需要引入新的技术栈？

经过对 v138 识别的 5 个方向以及我上文提出的 3 个扩展方向的分析，**当前阶段不建议引入新的技术栈**——Go 标准库 + 现有依赖足以实现所有方向的前两步。

以下是各方向对新技术依赖的评估：

| 方向 | 是否需要新技术 | 理由 |
|------|--------------|------|
| 策略引擎 (A) | ❌ 不需要 | 策略评估纯 Go 实现即可，规则匹配使用正则/前缀树足以应对；通知路由使用 HTTP Client（已存在） |
| 协议治理 (B) | ❌ 不需要 | 速率限制 token-bucket（已实现）；租户配额在 repository 层扩展字段即可 |
| 工作者生命周期 (C) | ❌ 不需要 | `sync.WaitGroup` + `context.Context` + `atomic.Value` 已够用 |
| 成本归因 (D) | ❌ 不需要 | 计数器 + DB 表 + HTTP API，纯 Go |
| 连接韧性 (E) | ❌ 不需要 | `context.Context` + `select` + `bufio.ReadDeadline` 纯 stdlib |

**但如果进入长期演进阶段（3 个月+），以下场景可能需要新依赖：**

| 场景 | 候选技术 | 评估标准 |
|------|---------|---------|
| 配置热加载的触发器 | `fsnotify`（或 Go 1.25+ 的 `os.FS` watcher） | 文件系统通知是纯 stdlib 无法做到的能力 |
| 统一配额存储 | Redis（可选） | 如果配额需要在多副本间高并发同步读写且需要 TTL，Redis 优于 SQLite 行锁。**但 CI gate 不可依赖 Redis**——需提供 fallback 到 SQLite 的实现 |
| 策略规则的复杂匹配 | 自建 DSL（expr/bexpr 风格） | 如果策略过滤条件（如 FilterKey 的正则匹配、标签条件）复杂度增加，可能需要轻量表达式引擎 |
| 统一成本仪表盘 | Grafana（已有）+ 自定义面板 | 成本数据进入 Prometheus → Grafana 仪表盘，已有基础设施 |

**决策依据：** 每当考虑引入新依赖，必须满足：
1. **CI gate 不依赖**：`make test` 不能依赖 Docker 或外部服务
2. **Go 标准库无法合理实现**：纯 Go 实现成本 > 依赖引入成本
3. **至少两个不同的用户场景受益**：单一场景不 justify 新依赖

### 4.2 自建 vs 采购的建议

| 能力 | 建议 | 理由 |
|------|------|------|
| **策略引擎** | ✅ 自建 | 这是 AeroVault 的核心差异化能力——S3 兼容策略执行。没有现成的 Go 库能理解 `NotificationRule` + `LifecycleRule` + `BucketPolicy` 的 S3 语义 |
| **MCP stdio 治理** | ✅ 自建 | 在 MCP 传输层植入治理中间件是一个很薄的包装层（约 200 行）。无现成库可用，且引入外部 MCP 框架可能造成协议兼容性问题 |
| **成本归因/计费** | ✅ 自建（核心逻辑）/ 🟡 可集成 Stripe（支付） | 成本模型（存储定价 × 用量）是业务核心逻辑，不可外购；但支付网关可集成 Stripe |
| **实时配置热加载** | ✅ 自建 | Go 的 `atomic.Value` + `sync.RWMutex` 足以实现配置的热切换 |

### 4.3 接口标准的演进策略

当前阶段不必引入 Protobuf/gRPC 或专门的规则引擎。但在长期演进中，两个点的标准化值得关注：

1. **策略模型的序列化格式**：当前 `NotificationRule` 存储为 JSON（`buckets.notification_rules` JSON 字段）。如果策略引擎扩展到 5+ 个策略域，建议：
   - 短期：保持 JSON 字段，每个策略域独立字段
   - 长期：引入 Protobuf 或 FlatBuffers 作为策略模型的序列化格式，以获得强类型 + schema 演进的确定性

2. **MCP 传输治理**：如果 stdio 治理中间件模式被验证有效，可考虑将治理规范回馈到 MCP 开放式标准（定义 `mcp:governance` 扩展帧）——但这不应纳入当前路线图。

---

## 五、实施路线图

### 5.1 整体路线图

```
Phase 1 (2-3 weeks) ─── 快速闭环，解决最严重的架构缺陷
  ├── P0: Legal Hold GET 拦截                    (~10 行, 1 天)
  ├── P0: 访问日志 middleware 调用 WriteAccessLog (~50 行, 1 天)
  ├── P1: WebDAV 归入 chi 中间件链               (~30 行, 1 天)
  ├── P1: SSE + ChatStream 客户端断开检测         (~20 行, 1 天)
  ├── P1: MCP stdio ReadDeadline                  (~15 行, 0.5 天)
  └── P1: sync.WaitGroup 后台 goroutine 追踪     (~50 行, 2 天)

Phase 2 (4-6 weeks) ─── 结构化治理与排空框架
  ├── P0: 策略引擎骨架 (Engine 接口 + 通知路由)
  ├── P1: WorkerManager 生命周期框架
  ├── P1: path-based rate limit 规则
  ├── P1: EventBus 慢订阅者保护
  ├── P1: Multipart 上传 TTL + GC
  └── P2: AI 管线全链路成本记录

Phase 3 (2-3 months) ─── 平台级能力
  ├── P0: 完整通知引擎 (SQS/SNS/Lambda 适配器)
  ├── P1: 租户级请求配额
  ├── P1: 配置热加载框架 (Reload 接口 + /debug/reload)
  ├── P2: 存储成本每日快照 + 后端定价映射
  ├── P2: MCP stdio 治理中间件
  └── P2: 就绪探针深度语义化 (/readyz?full=1)

Phase 4 (3-6 months) ─── 企业级完备
  ├── P0: Bucket Policy 鉴权引擎
  ├── P1: 阶段式关闭状态机
  ├── P1: 请求优先级队列
  ├── P2: 统一成本 API + 租户账单导出
  ├── P2: ChatStream 断线续传
  └── P2: 配置热加载全组件覆盖
```

### 5.2 每阶段的优先级矩阵

| 阶段 | 方向覆盖 | 风险 | 缓解策略 |
|------|---------|------|---------|
| **Phase 1** | 方向三局部 + 方向一局部 + 方向二局部 + 方向五局部 | 🟢 低——改动行数极小，均为已验证模式的增量修复 | 每项独立 PR，独立 `make check` 验证 |
| **Phase 2** | 方向三核心 + 方向一核心 + 方向五核心 + 方向四开端 | 🟡 中——策略引擎骨架是新增组件，需要良好的接口设计 | 先完成接口定义文档再编码；策略引擎默认关闭，开启后不影响现有 CRUD 路径 |
| **Phase 3** | 方向一深入 + 方向二深入 + 方向四深入 | 🟡 中——配置热加载需要组件全量适配 Reload 接口 | 增量改造：先实现 rate_limiter.Reload + log_level.Reload，再扩展到其他组件 |
| **Phase 4** | 方向三完整 + 方向一完整 + 方向四完整 | 🔴 高——Bucket Policy 涉及鉴权路径重构，影响所有请求 | 使用 Feature Flag 隔离新鉴权路径；与旧路径并存至少一个发布周期 |

### 5.3 关键风险与缓解策略

#### 风险 1：策略引擎成为"第二个配置面"

**症状：** 策略引擎新增后，用户配置了通知规则 → 引擎存储了规则映射 → **通知仍然不送达**（因为 dispatcher 还没实现）。

**缓解：**
- 在 Phase 2 开始实现策略引擎时，**第一个实现必须是端到端可运行的完整链路**（如通知路由到全局 webhook），不要先实现 Configuration CRUD 再实现 Execution
- 新增策略域必须在合并前通过集成测试验证端到端行为

#### 风险 2：WebDAV 路由重构导致生产回归

**症状：** 将 WebDAV 从 `buildDispatcher` 移入 chi route 后，某些路径的鉴权/路由行为不一致。

**缓解：**
- 重构前编写 WebDAV 路径的完整集成测试覆盖（`curl` 或 `httptest` 模拟所有已支持的 WebDAV 方法：PROPFIND/MKCOL/PUT/GET/DELETE）
- 分两步部署：第一步将 WebDAV handler 同时注册在 dispatch 和 chi route 中（使用 feature flag 切换）；第二步稳定后移除 dispatch 路径

#### 风险 3：工作者生命周期框架引入死锁

**症状：** Worker.Drain() 等待工作者排空，但工作者正在等待某个被 Drain 阻塞的资源（如 EventBus 的 broadcast），形成循环依赖死锁。

**缓解：**
- 在 `WorkerManager.DrainAll` 中实现**阶段式排空**：先关闭入站（停止接受新事件）→ 再排空 in-flight → 最后关闭资源
- 为每个 Drain 操作设置硬超时，超时后执行强制 `ctx.Done()` + 记录告警
- 排空顺序必须是：业务工作者（Indexer/Replication/AV）→ Bus（停止传播新事件）→ 存储和 Repository（关闭连接）

#### 风险 4：成本归因数据方案过度设计

**症状：** 花了 3 周设计"统一成本模型+多重定价版本+历史摊销"，但用户只需要"每月给租户出个账单"。

**缓解：**
- **MVP 定义**：Phase 2 的成本归因仅完成 AI 全链路成本记录 + 存储字节数按存储类分组 + 一个 `GET /v1/admin/tenants/{t}/usage` 返回基础用量数据（无财务金额换算）
- **定价配置**：从静态 YAML/JSON 文件读取（`cost-pricing.yaml`），不引入数据库版本管理——迭代中再升级
- **MVP 验证**：拿出月度账单给运营负责人确认"这是我们需要的数据吗？"

---

## 六、总结：架构决策要点

### 必须立即决策的（本周内）

| 决策 | 选项 | 建议 |
|------|------|------|
| WebDAV 路由方式 | (a) 保留 dispatch 截获 + 增加治理包装 (b) 移入 chi route | **(b)**——这是长期正确的架构选择，Phase 1 闭环 |
| 策略引擎独立包 vs 嵌入现有结构 | (a) `internal/policy/` 新包 (b) 在 `internal/events/` 中扩展 | **(a)——策略引擎是跨维度的基础架构，不应隐藏在 events 子包中 |
| 后台工作者生命周期管理 | (a) `internal/worker/` 新接口 (b) 在 `main.go` 中用 map + WaitGroup 管理 | **(b) 先做简单的，Phase 2 在需要时再抽象**——`main.go` 级别的管理足够覆盖 Phase 1 需求 |

### 短期需验证的

| 验证项 | 方法 | 时限 |
|--------|------|------|
| 策略引擎接口设计是否足够通用来覆盖 5 个策略域 | 写 3 个不同的 Strategy 实现的伪代码（notification/lifecycle/legalhold） | Phase 2 开始前 |
| WebDAV 路由重构后所有现有 WebDAV 路径的响应是否一致 | 集成测试覆盖 10+ 核心 WebDAV 路径 | Phase 1 内 |
| Worker Drain 接口是否能被所有工作者无冲突地实现 | 对 Indexer 和 Replication 两个最复杂的工作者编写 Drain 实现原型 | Phase 1 结束前 |

### 长期需持续关注

| 关注点 | 为什么 |
|--------|--------|
| **策略模型的演进复杂度** | 5 个策略域可能增长到 10+（Bucket Policy / CORS / Object Lock / Retention / ...），需要确保策略引擎的可扩展性 |
| **MCP stdio 治理的统一性** | 随着 MCP 使用增多，stdio 治理缺口会越来越突出——需要从"绕过治理"走向"治理归一" |
| **成本归因的数据准确性** | 一旦账单发给客户，数据准确性就失去了迭代缓冲期——需要从一开始就做对（幂等性、对账机制、审计追踪） |
| **编排复杂度** | 策略引擎 + WorkerManager + 协议治理 + 成本归因，四个扩展同时引入后系统的认知负载会增加——需要良好的文档和清晰的组件边界定义 |

---

*本分析基于 `expansion-v138-architecture-blindspots-and-extension-directions.md` 第 89 轮扫描成果及对全部 24+ 子包的独立审查。分析过程中未编写任何代码。*
