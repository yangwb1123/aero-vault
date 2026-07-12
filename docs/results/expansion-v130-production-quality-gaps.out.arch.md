# 架构审查与扩展设计：aero-vault 生产质量夯实

---

## 1. 架构评估

### 1.1 当前架构定位评估

aero-vault 当前处于 **"功能完整性 → 生产可靠性"的转折点**。这是技术债务的自然增长曲线中最微妙的阶段——功能覆盖率高意味着代码路径的**交互复杂度**远超代码行数所暗示的。v38 分析团队正确地识别了这一本质。

从架构图来看，当前架构的**本质优势**是清晰的层分离和事件驱动内核：协议适配器是薄壳，FileService 承上启下，Storage/Repository 无状态互换。这为后续韧性改造提供了良好的基础——如果一个架构是"大泥球"，那韧性改造的成本会高出一个数量级。

### 1.2 关键设计决策评估

| 决策 | 评估 | 依据 |
|------|------|------|
| FileService 作为唯一业务入口禁止协议层直连 Storage | 🟢 **正确** | 单一网关便于审计、事件发布、ACL 校验；代价是吞吐路径多一次函数调用，可忽略 |
| EventBus 作为异步工作流中枢 | 🟢 **正确但未成熟** | 抽象正确，但缺少背压、排空、重放机制，目前是"裸 channel"形态 |
| Opt-in 安全默认（AI/pgvector/Qdrant 全关闭） | 🟢 **正确** | 基线路径零依赖是运维底线，与 CI gate 策略一致 |
| Middleware 链固定且 handler 不自挂 | 🟢 **正确** | 增强了可测试性和审计确定性，但收益被缺少 context 传播一致性部分抵消 |
| 错误模型扁平（`AeroError` 单一结构） | 🟡 **需演进** | 适合 v1-v38 的"快速识别错误位置"阶段，但 retryable 静态化阻碍了协议感知的错误处理 |
| SQL 占位符不可复用（`$N` → `rebind`） | 🔴 **持续产生隐形成本** | 每次写 SQL 都要小心避免参数错误，缺乏编译期保证 |

### 1.3 架构债务与技术债清单

**即刻威胁（Production Critical）：**

1. **`secret.go` HTTP provider 连接池缺失 → FD 泄漏**（方向二的核心发现）
   - 每个请求创建新 `*http.Client`，transport 的 idle conn 在 goroutine 生命周期之后仍在存活
   - 高并发下 `too many open files` 硬失败，重启前不可恢复
   - **严重性：** 在高负载下必然触发，不依赖低概率竞态

2. **`EventBus.Publish` 与 `Bus.Close` 的竞态**（方向三的补充发现）
   - `Publish` 检查 closed flag 后在写入 channel 前被 `Close` 中断 → `send on closed channel` panic
   - 这是 shutdown 窗口中的低概率高影响风险，重启时间越长概率越高

3. **otelhttp handler 自动注入 vs 手动 propagator 不一致**（方向一的补充发现）
   - WebDAV goroutine 可能跳过 `otelhttp.NewHandler`，导致 trace 断裂
   - 分布式追踪的价值在这种"部分追踪"情况下降低 80%

**演进债务（Growth Impediment）：**

4. **`AeroError.Retryable` 作为静态字段**（方向四的核心发现）
   - 同一错误（如 `NoSuchUpload`）对 S3 客户端是永久错误，对内部 repl worker 可重试
   - 静态字段迫使调用方在错误生成时做出**过早的判断**

5. **`utils/` 包存在倾向**（违反 AGENTS.md 约束）
   - 从分析报告中可见错误构造、KMS 等多种功能共用"内部工具"模式
   - 违反"禁止 `utils/` `common/` `helper/` 包，必须按领域分散"的工程约束

6. **测试覆盖率 61.1% 但无变异测试**
   - 高覆盖率低活性的测试在重构时产生大量误报，实际上**降低了重构意愿**
   - 这放了一个错误信号给管理层：覆盖率高=质量好

### 1.4 架构健康度评分

| 维度 | 评分 | 说明 |
|------|------|------|
| 层分离程度 | 🟢 8/10 | 协议-服务-存储三层清晰，但 AI pipeline 与 FileService 耦合略高 |
| 错误域覆盖 | 🟡 5/10 | 有统一错误结构但缺少协议感知的 retryable 判定 |
| 可观测性 | 🟢 7/10 | OTel 指标丰富，但 context 断裂导致 trace 质量不均 |
| 韧性模式 | 🔴 3/10 | 无断路器、无 bulkhead、无超时传播、无优雅排空 |
| 测试有效性 | 🟡 5/10 | 覆盖率高但变异测试未建立，无法区分有效覆盖与死覆盖 |
| 配置灵活性 | 🟡 4/10 | 仅静态配置，无热加载 / 无需重启变更 |

**综合：5.3/10 — 功能丰富但生产韧性不足。** 接下来的版本应聚焦非功能属性的提升，而非新增功能。

---

## 2. 高价值架构扩展方向

在 v38 分析团队的 5 个方向基础上，我提出 5 个**互补的架构扩展方向**，按维度排列而非优先级。

### 方向 α：依赖网关层（Resiliency Gateway）

**为什么需要：**
- 当前所有外部依赖调用（AI providers、S3 backends、KMS、webhook targets）缺乏韧性模式
- 一个慢响应 LLM 的热点可以**饿死同一 transport 池中的 S3 请求**（方向二的 transport pool 分片解决了饥饿的不同类型，但同一组内仍有热点风险）
- 缺少断路器意味着底层故障会传播到上层协议适配器，污染客户端错误统计

**核心挑战：**
| 挑战 | 难度 | 说明 |
|------|------|------|
| 断路器状态持久化 | 🔴 高 | 进程重启后半开状态丢失，可能导致重启后的"冷启动雪崩" |
| Bulkhead 与现有限流的边界 | 🟡 中 | `AI_RATE_LIMIT_RPS` 按租户限流，bulkhead 按依赖组限并发，两者需要组合而非替代 |
| 超时传播的嵌套衰减 | 🟡 中 | 每层 retry 的超时需要递减（连接超时 500ms → 请求超时 3s → 调用超时 10s），避免上层超时重试堆积 |
| 协议适配器的无感接入 | 🟢 低 | 通过 `http.RoundTripper` 包装和 `storage.Backend` 包装层实现，对业务代码透明 |

**预期的架构变更：**

```
当前：
ai.Embedder → http.Client{} (raw)
webhook → http.Client{} (raw)
storage/s3  → http.Client{} (raw)

目标：
ai.Embedder → resiliency.Client{name:"ai", breaker, bulkhead} → http.Client{transport from pool}
webhook     → resiliency.Client{name:"webhook", breaker, retry} → http.Client{transport from pool}
storage/s3  → resiliency.Client{name:"storage-s3", breaker, bulkhead} → http.Client{transport from pool}
```

引入 `internal/resiliency` 包，提供：
- `Client` 包装结构（嵌入 `*http.Client` 或自定义接口）
- `CircuitBreaker` 组件（支持半开状态）
- `Bulkhead` 组件（信号量风格的并发控制）
- `RetryPolicy` 组件（指数退避 + jitter）
- `ResiliencyConfig` 全局配置映射

**对现有系统的影响：**
- 对业务代码的影响极低（通过 transport 层包装和 storage factory 单点注入）
- 新增依赖：`gobreaker`（成熟稳定）或自建（约 300 行关键逻辑）
- 新增 OTel 指标：`resiliency_breaker_state{name,state}`、`resiliency_bulkhead_wait{name}`、`resiliency_retry_attempts{name}`
- 完全向后兼容（无韧性配置时退化到原始 `http.Client`）

---

### 方向 β：动态配置引擎（Live Configuration Engine）

**为什么需要：**
- 当前系统**每次配置变更都需要重启进程**——在生产环境中，重启窗口、预热损失、连接池重建都是事故风险
- 故障响应中，"调大 rate limit / 关闭某个 AI provider / 增加日志级别" 是典型的前几分钟操作，不能等到下一轮部署
- 多租户场景下，不同租户可能需要不同的配置覆盖（例如租户 A 的 AI 日预算在月中调整）

**核心挑战：**
| 挑战 | 难度 | 说明 |
|------|------|------|
| 配置变更的原子性和验证 | 🔴 高 | 分步应用配置导致系统处于"部分更新"状态，需要类似两阶段提交的 `Validate → Apply → Commit` |
| 并发安全的配置读取 | 🟡 中 | `sync.RWMutex` 保护的配置映射，读取路径 `RLock` 不应在 hot path 上成为瓶颈 |
| 配置源的优先级和合并 | 🟡 中 | 命令行标志 > 环境变量 > 配置文件 > DB 存储 > 默认值，且支持租户级覆盖 |
| 配置变更的审计与回滚 | 🟡 中 | 每次变更记录到 `audit_log`，并保留 N 个历史版本支持快速回退 |

**预期的架构变更：**

```
internal/config/
├── static.go          // 现有静态配置（启动时加载，不变）
├── live.go            // 可热加载配置接口
├── store/
│   ├── file.go        // 文件监听（fsnotify / 定时扫描）
│   ├── db.go          // DB 存储配置覆盖（按租户分层）
│   └── memory.go      // 运行时覆盖（用于测试和紧急操作）
├── validator.go       // 配置变更验证（类型检查、范围检查、交叉引用检查）
└── watcher.go         // 变更通知（hook 回调 / channel 广播）
```

关键接口设计：

```
type Provider interface {
    Get(key string) (Value, error)
    Watch(ctx context.Context) <-chan ChangeEvent
    Set(key string, value string, meta *Meta) error
    Validate(changes map[string]string) error
}
```

**对现有系统的影响：**
- 引入新包，不修改现有 `config.go` 的结构
- 现有静态配置作为"初始值" + "最终回退"
- 每个功能模块通过 `live.Watch(prefix)` 订阅自己的配置变更
- 初期只需迁移 3-5 个关键配置作为试点（rate limits、AI budgets、log levels）
- 完全向后兼容（不设动态配置时行为不变）

---

### 方向 γ：操作溯源与事务化 Saga

**为什么需要：**
- 当前 `FileService` 对 `Storage` 和 `Repository` 的操作**不是原子性的**——存储写成功但元数据写失败导致系统处于不一致状态
- 跨区复制、AV 扫描、Webhook 等异步操作缺乏"补偿动作"的定义
- EventBus 的"至少一次"语义（JobPool 的 `running → pending` 重置）缺少异常恢复的精确性

**核心挑战：**
| 挑战 | 难度 | 说明 |
|------|------|------|
| Saga 协调器的可靠性 | 🔴 高 | 由 `jobs` 表驱动的异步 saga 需要保证补偿动作最终执行，且自身不能成为单点 |
| 补偿动作的正确性 | 🔴 高 | "undo put"不是简单的 `delete`（需要考虑版本控制、WORM、权限、审计链），每个操作都需要对偶的补偿 |
| 与现有 EventBus 的重叠 | 🟡 中 | 部分事件（`object.created`）既是 saga 步骤又是外部通知，需要区分"内部 saga 状态" vs "外部事件" |
| 性能影响 | 🟡 中 | 每次操作写入 `operations` 表，增加约 200μs 的延迟（SQLite，~10ms for Postgres） |

**预期架构变更：**

```
internal/saga/
├── orchestrator.go      // Saga 执行引擎
├── step.go              // 原子步骤 + 补偿函数
├── registry.go          // 操作类型 → Saga 步骤映射
├── store.go             // Saga 状态持久化（复用 jobs 表模式）
└── reconciler.go        // 后台恢复卡住的地步（幂等重试）
```

FileService 的主要变更：所有"跨存储-仓库调用"改为 saga 封装。例如 `PutObject` 的简化 saga：

```
Step 1: Generate ObjectID + storageKey        → Comp: 无
Step 2: Write to Storage (blob)               → Comp: Delete blob (with version check)
Step 3: Write metadata to Repository          → Comp: Delete metadata row
Step 4: Publish object.created event          → Comp: 无（下游自行处理冲销）
```

如果 Step 3 失败，orchestrator 执行 Comp for Step 2（delete blob），然后标记 saga 为 `failed`。

**对现有系统的影响：**
- **这是最大规模的改造**，不应放在初期
- 建议先对"跨存储-仓库"的关键路径（PUT/DELETE/Copy）做 saga 层
- 通过接口默认实现保持测试友好（`saga.NoopOrchestrator`）
- 必须在 AGENTS.md 中**新增硬性不变量**："所有跨存储-仓库写入必须经过 saga 封装"

---

### 方向 δ：背压感知的异步工作流（Backpressure-Aware Async Pipeline）

**为什么需要：**
- 当前 EventBus 和 Worker 使用**无界 channel 或轮询表**，缺少生产系统中对输入速率 > 处理能力的感知
- Indexer 处理大文件时（chunk 量增加）可能阻塞内存，而 JobPool 的轮询模式意味着"能拉就做"，不做负载感知
- 下游（Qdrant、AI embedder）的慢响应会堆积在 channel 中，表现为"完成上传但检索永远搜不到"

**核心挑战：**
| 挑战 | 难度 | 说明 |
|------|------|------|
| 背压信号的传播路径 | 🔴 高 | 应该拒绝新上传（上游进入）还是拒绝事件（中游排队）？不同协议适配器的背压语义不同（S3 应接受后返回 503，MCP 应拒绝工具调用） |
| 有界队列的容量策略 | 🟡 中 | 固定大小 vs 动态调整 vs 基于租户配额；队列长度的合理阈值取决于绑定资源 |
| 积压可视性 | 🟢 低 | 增加 OTel gauge `eventbus_queue_depth{priority}` 和 `worker_backlog{name}` |
| 优雅降级路径 | 🟡 中 | 背压时哪些操作可以降级：索引延迟/跳过、webhook 丢弃、缩略图跳过 |

**预期架构变更：**

```
当前：
EventBus.Publish(event) → channel (unbuffered or buffered) → subscriber

目标：
EventBus.Publish(event, opts ...PublishOption) → 
  queue(subscriber backpressure check) → 
  accept (返回 ack) / reject (返回 error with Retry-After hint)
```

新增 `Queue` 抽象：

```
type Queue interface {
    Enqueue(ctx context.Context, item interface{}, priority int) error
    Dequeue(ctx context.Context) (item interface{}, ack func(), nack func(), error)
    Len() int
    Cap() int
}
```

EventBus 支持**优先级队列**（对象索引 > 缩略图 > webhook 通知），并在背压时选择性丢弃低优先级的非关键事件。

**对现有系统的影响：**
- 核心变更在 `internal/events` 包，替换 channel 为 `Queue`
- 背压信号通过 context cancellation 传播（上游的 `FileService.PutObject` 在 `ctx.Err()` 非 nil 时返回 503）
- Worker 的轮询改为基于 `Dequeue` 阻塞调用（减少 spin-loop 的 CPU 浪费）
- **关键兼容性考量：** 当前 subscriber 是同步回调（`Handle(ctx, event)`），改为异步 queue 后需要 adapter 层使现有 subscriber 无需改动

---

### 方向 ε：多协议错误域统一（Unified Error Domain across Protocols）

**为什么需要：**
- 当前 `AeroError` 服务于**所有协议**（REST JSON、S3 XML、WebDAV XML、MCP JSON-RPC），但每个协议的错误序列化路径是独立实现的
- S3 的错误码（`NoSuchKey`）与 REST 的错误码（`NotFound`）语义相同但命名不同，缺少映射
- 方向四的 `Classify(ctx, err, protocol)` 是最优解，但需要一个围绕它的**协议错误翻译层**去做自动转码

**核心挑战：**
| 挑战 | 难度 | 说明 |
|------|------|------|
| S3 错误 XML 中字段的精确映射 | 🟡 中 | S3 要求 `Code`、`Message`、`Resource`、`RequestId`、`HostId`，REST 只需 `error` + `code` + `message` |
| 条件请求错误的 HTTP 状态码语义 | 🟡 中 | `If-Match` 失败的 412 在 REST/S3/WebDAV 中都有不同要求（S3 用 412 vs 304 vs 400） |
| 不引入新的抽象层导致层泛滥 | 🟡 中 | 已经有 `AeroError`、协议 handler 内部错误、外部库错误，不能再增加一层"error wrapper" |
| 测试中的错误断言可读性 | 🟢 低 | 方向四解决了部分匹配问题，但还需要协议 adapter 的测试 fixture |

**预期架构变更：**

```
internal/errors/
├── domain.go            // AeroError 定义（+ Retryable(ctx) 方法替代字段）
├── classify.go          // Classify(ctx, err, ProtocolKind) → *AeroError
├── translate/
│   ├── rest.go          // AeroError → REST JSON response body
│   ├── s3.go            // AeroError → S3 XML error response
│   ├── webdav.go        // AeroError → WebDAV multistatus / error XML
│   └── mcp.go           // AeroError → JSON-RPC Error object
├── matcher.go           // testutil 部分匹配器
└── codes.go             // 统一错误码定义（所有协议共用）
```

关键设计点：
- **`Classify` 不是 switch-case 巨函数**，而是注册表模式：`errors.RegisterClassifier(protocol, classifier)`
- `ProtocolKind` 是一个 `string` 类型，现有协议在启动时注册自己的 classifier
- `translate` 包中的函数是**有状态的**（需要 `RequestID`、`HostID` 等全局元数据），通过翻译器结构体注入

**对现有系统的影响：**
- `AeroError` 结构体增加 `Retryable(ctx)` 方法但保留 `Retryable` 字段（过渡期双重支持）
- 每个协议 handler 中的错误处理路径改为单一的 `translate.Translate(ctx, err, protocol)` 调用
- 测试中可以用 `errors.MatchError(t, err, &AeroError{Code: "NotFound"}, IgnoreExtraFields())`
- 向后兼容阶段（v39-v40）：同时支持新旧两种错误路径

---

## 3. 接口设计建议

### 3.1 关键模块接口设计原则

**原则一：面向契约测试，而非面向实现测试**

当前 `storage` 包有 `contract_test.go`，这是正确的做法。所有新接口都需要对应的 contract test suite：

| 新接口 | 契约验证内容 |
|--------|-------------|
| `resiliency.Client` | 断路器打开时返回 `ErrCircuitOpen`、关闭时透传请求、半开态允许探测 |
| `saga.Orchestrator` | 补偿动作无论成功或失败都更新 saga state、幂等重放不重复补偿 |
| `config.live.Provider` | 配置变更触发 `Watch` channel 事件、验证失败不应用变更、并发读取返回最新值 |
| `events.Queue` | 背压时 `Enqueue` 返回 `ErrBackpressure`、`Dequeue` 阻塞直至有元素或 ctx 取消 |

**原则二：返回错误统一使用 `errors.AeroError`，禁止返回原始 `error`**

所有公开 API 方法签名中的 `error` 返回值必须是 `*errors.AeroError`（或实现了 `AeroError` 接口的类型）。这一约束通过接口而非类型断言来保证：

```go
type AeroError interface {
    error
    Code() string
    Message() string
    Retryable(ctx context.Context) bool
    Kind() errors.Kind
    WithMeta(key string, val interface{}) AeroError
}
```

这与 Go 的惯用 `error` 接口兼容，同时保证调用方可以从 `errors.As` 中获得结构化信息。

**原则三：抽象层数量控制 ≤ 3 层（handler → service → resource），避免过度分层**

当前系统 `handler → FileService → Storage/Repository` 是 3 层，这是合理的。新增的 `resiliency` 层应嵌入 `Storage` 接口的实现中，而非作为独立层存在。类比：`Storage` 接口的 `s3` backend 内部使用 `resiliency.Client`，但对外接口不变。

### 3.2 是否需要引入新的抽象层

| 抽象层 | 决策 | 理由 |
|--------|------|------|
| `resiliency.Client` | ✅ 引入 | 作为 `http.Client` 的面向领域包装，不暴露底层韧性逻辑给业务代码 |
| `saga.Orchestrator` | ✅ 引入 | 作为 `FileService` 的内部编排器（非公开包），通过接口注入以支持测试 mock |
| `config.live.Provider` | ✅ 引入 | 新包，现有 `config.go` 不变，通过 listener 模式扩展 |
| `events.Queue` | ✅ 引入 | 替换 EventBus 内部 channel，接口定义在 `events` 包内 |
| 额外的"error wrapper"抽象 | ❌ 不引入 | `AeroError` 已足够，不增加 `AppError` / `DomainError` 等同类抽象 |
| 额外的"repository abstraction" | ❌ 不引入 | 当前 `repository.Repository` 接口粒度合理，不必拆分为读-写分离等微接口 |
| 额外的"protocol router"抽象 | ❌ 不引入 | 当前 `chi` 路由 + 各协议 adapter 的独立分发模式合适，不需要统一路由抽象 |

### 3.3 向后兼容性策略

| 变更类型 | 兼容策略 | 过渡周期 |
|---------|---------|---------|
| `AeroError.Retryable` 字段 → 方法 | 过渡期双重实现（字段+方法），日志警告字段调用 | 2 个版本 |
| EventBus 同步 channel → 异步 Queue | 通过 event adapter 包装现有 subscriber，不修改 subscriber 签名 | 无破坏性变更 |
| 配置键值变动 | 弃用旧键后保留 4 个版本的别名映射 | 4 个版本 |
| 新 `errors/translate` 包引入 | 协议 handler 逐一迁移，允许新旧路径共存 | 每个协议 1 个版本 |
| Saga 层引入 | 默认使用 `saga.NoopOrchestrator`，仅启用时产生差异化行为 | 永久默认 noop |

**关键决策：** 所有破坏性变更（如 `AeroError` 字段移除）**必须**在 `CHANGELOG.md` 中用 `BREAKING` 前缀标记，并在 CI gate 中加入 `git grep -n "\.Retryable " -- '*.go'` 检查，在移除前一个版本打印 deprecation 警告。

---

## 4. 技术选型

### 4.1 新技术引入决策矩阵

| 能力域 | 推荐方案 | 备选方案 | 决策逻辑 |
|--------|---------|---------|---------|
| 断路器 | **`sony/gobreaker`** (v3.1k ⭐, 稳定) | 自建（~300 行） | 社区方案成熟且简单，接口适合依赖注入；自建成本在测试上不划算 |
| Bulkhead | **自建**（信号量模式, ~100 行） | `hagen1778/gobulkhead` | 过于简单不值得引入新依赖；Go 标准库 `chan struct{}` 信号量即可 |
| 配置热加载 | **自建**（文件监听 + DB 存储） | `spf13/viper` + `fsnotify` | Viper 的 Watch 在 Linux 上的 inotify 限制（目录级监听），且与已有 config 结构深度绑定。更轻量的自建方案适合当前复杂度 |
| 变异测试 | **`go-mutesting`** (v0.5k ⭐, 标准) | `go-mutator` | go-mutesting 与 Go 标准工具链集成更好，支持增量模式 |
| 分布式 Tracing | 现有 OTel（无需新依赖） | Jaeger/Zipkin SDK | OTel 已是标准，协议适配即可 |
| 错误处理增强 | **`go-multierror`** (由 HashiCorp) | 自建错误收集 | 在 `Classify` 的复合错误场景中有实用价值（多个校验错误合并），但需评估是否值得引入 |

### 4.2 自建 vs 引入依赖的决策边界

| 条件 | 自建 | 引入依赖 |
|------|------|---------|
| 逻辑行数 ≤ 200 | ✅ | ❌ |
| 对内核稳定性有特殊要求（如 "半开状态持久化"） | ❌（测试成本高） | ✅ |
| 需要深度定制以满足领域特性 | ✅ | ❌（fork 成本更高） |
| 安全审计需要追踪源码（无上游变动风险） | ✅ | ❌（CVE 追踪成本） |
| 社区方案已被广泛验证（>1000 ⭐, >1yr stable） | ❌ | ✅ |
| 接口简单（≤3 methods） | ✅ | ❌ |
| 需与现有 OTel/Metrics 深度集成 | ✅（定制 +1 策略） | ❌（扩展性可能不足） |

**关于 `gobreaker`：** 虽然是第三方依赖，但它的接口简单（3 methods）、状态机明确、与 `http.RoundTripper` 集成自然。且当前系统的韧性缺口已经达到"等不了自建"的紧急程度。**每新增一个外部 HTTP 调用（AI provider、KMS、webhook target）但不加断路器就是在积累风险**——依赖引入的边际成本（~200 行包装代码）远低于自建全功能断路器的测试成本（~800 行测试）。

### 4.3 技术债务处置建议

| 债务项 | 处置建议 | 时间 |
|-------|---------|------|
| `utils/` 包存在 | 逐步解体为领域包（`errors/`、`resiliency/`、`config/live/`），每次重构挪一个功能 | 持续（每个版本 1 个功能） |
| SQL 占位符约束 | 引入 `sqlcheck` 作为 pre-commit hook（检测 `$N` 复用），长期考虑代码生成 | v39（pre-commit）→ v41+（代码生成） |
| `context.Background()` 在 handler 路径中使用 | 先发布 lint rule（`errcheck` + 自定义检查），逐个修复 | v39 规则定义 → v40 修复 |
| 连接池缺失的 HTTP calls | 通过方向 β（config）+ 方向 α（resiliency）一次性解决 | v39 |

---

## 5. 实施路线图

### 5.1 优先级排序

| 优先级 | 方向 | ROI 评估 | 实施影响 | 依赖 |
|--------|------|---------|---------|------|
| **P0** | 方向① Context Propagation + 方向③ Graceful Shutdown | **最佳 ROI**：修复成本低（定向 lint + 少数 goroutine 改造 + Bus RWMutex），直接降低 MTTR 50%+ | 低 | 无 |
| **P0** | 方向② HTTP Connection Pooling + 方向 α 断路器（最小子集） | **止损**：立即消除 FD 泄漏风险 | 低-中 | 无 |
| **P1** | 方向④ Structured Error Domain（`Retryable` 方法化 + `Classify` 注册表） | **架构基础**：为所有后续韧性改造提供统一的错误判定基础设施 | 中 | P0 完成 |
| **P1** | 风险 A（shutdown ordering deadlock） | **防崩溃**：排空阶段的死锁可能导致 shutdown hang（分钟级） | 低 | P0 的 ① + ③ |
| **P1** | 风险 B（AeroError test matcher） | **提效**：消除测试中的 "只测 code 不测 message" 坏味道 | 低 | 可独立 |
| **P2** | 方向 α 完整实现（断路器 + bulkhead + retry）+ 方向 β（动态配置） | **韧性跃升**：系统对外部故障的隔离能力提升 100x | 高 | P1 的 ④ |
| **P2** | 方向 ε（多协议错误域统一） | **一致性**：消除各协议错误响应之间的语义漂移 | 中 | P1 的 ④ |
| **P2** | 方向⑤ 测试基础设施 + 变异测试 | **质量保障**：确保重构安全，但回报有滞后性 | 中 | 无 |
| **P3** | 方向 γ（事务化 Saga） | **一致性**：解决存储-仓库不一致但影响大于收益的阶段暂缓 | 高 | P2 全部 |
| **P3** | 方向 δ（背压感知） | **吞吐稳定性**：重要但当前无急迫信号 | 中高 | P2 的 α |
| **P3** | 方向 β 的功能完善（多层次配置、回滚机制） | **运维提效** | 中 | P2 的 β 试点 |

### 5.2 阶段划分

**Phase 1：基础韧性（v39）— 4-6 周**

目标：消除即刻生产威胁，建立错误基础设施。

```
Week 1-2: 方向① + 方向③（最低修复）
  - contextlint 规则发布
  - 关键 goroutine（Indexer、repl worker、webhook sender）的 context 传递修复
  - EventBus RWMutex 保护
  - shutdown ordering 文档化 + 检查

Week 3-4: 方向② + 方向④ 的 Retryable 方法化
  - secret.go 的 HTTP provider 按方向二建议修复（transport pool per group）
  - AeroError.Retryable 改为方法（双重实现过渡期）
  - Classify 注册表框架 + 默认 classifier

Week 5-6: 风险 B + 方向⑤ 的变异测试引入
  - errors.Matcher 接口 + testify helper
  - go-mutesting CI 集成（作为 `make check` 的可选阶段）
  - 修复第一批变异测试发现的 "死覆盖"（预计 15-25 处）
```

**里程碑（v40 发布前）：**
- [ ] `make check` 包含 contextlint 检查，零失败
- [ ] 新增 `resiliency_test.go` 覆盖 EventBus 关闭竞态
- [ ] 方向② 的 transport pool 部署后无 FD 泄漏事件
- [ ] 变异检测覆盖率 ≥ 50% 的有效覆盖确认率

**Phase 2：韧性模式启用（v40）— 6-8 周**

目标：断路器 + bulkhead + 动态配置试点部署。

```
Week 1-2: 方向 α 的断路器包装
  - gobreaker 集成 + resiliency.Client 包装
  - AI provider、KMS、webhook 包装
  - OTel 指标集成

Week 3-4: Bulkhead + RetryPolicy
  - 信号量 bulkhead 实现
  - 指数退避 + jitter 的 retry 策略
  - 配置联动（断路器阈值、bulkhead 容量、retry 次数）

Week 5-6: 方向 β 动态配置试点
  - 文件监听 provider（config/live/file.go）
  - 3 个试点配置：rate limits、AI budgets、断路器状态手动强制

Week 7-8: 方向 ε 多协议错误域统一
  - 统一 error codes 定义
  - REST/S3 error 翻译器
  - 向后兼容过渡
```

**里程碑（v41 发布前）：**
- [ ] 所有外部 HTTP 调用经过 `resiliency.Client`
- [ ] 容量测试验证 bulkhead 在 AI provider 慢响应时保护 S3 请求
- [ ] 动态配置可热更新 rate limit 和 AI budget
- [ ] S3 和 REST 的错误响应使用统一的 `errors/translate`

**Phase 3：高级韧性（v41+）— 8-12 周**

目标：异步工作流韧性、动态配置完善、可选事务化 Saga。

```
v41: 方向 δ 背压感知
  - 有界 Queue 替换 EventBus channel
  - Indexer 和 Worker 的负载感知
  - 优先级事件队列

v42: 方向 γ Saga 试点（PUT/Delete 关键路径）
  - saga.Orchestrator 实现
  - FileService.PutObject 和 DeleteObject 封装
  - Reconciler 实现

v43: 方向 β 完善 + 运维工具
  - 多层次配置覆盖
  - 配置回滚 UI
  - 配置变更审计
  - 剩余 `utils/` 包解体
```

### 5.3 风险矩阵与缓解策略

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| Phase 1 中 context 改造导致原有 trace 断裂 | 中 | 高 | 在每个 goroutine 改造前先写集成测试捕获 trace parent；使用 `httptest` + `oteltest` 验证 propagation |
| 断路器在新部署后恢复流量时冷启动雪崩 | 中 | 高 | 断路器初始状态不设 `Closed` 而是 `HalfOpen`（允许首次探测后进入正常）；`gobreaker` 支持自定义 `NewState` |
| 动态配置错误导致全局中断 | 低 | 极高 | 双层验证（语法验证 + 沙箱应用）；自动回滚（心跳超时 → 前一个已知安全配置） |
| 变异测试产生的假阳性削弱团队信心 | 中 | 中 | 初始阶段使用"突变 kill ratio"而非"覆盖率"作为指标；允许 list of false-positive mutations（注释标记 `// live`） |
| Saga 层引入的性能衰退在 SQLite 路径上不可接受 | 低-中 | 高 | Saga 默认 noop，仅在显式启用时激活；性能基准测试在 CI 中守护 |
| 多版本过渡期的接口兼容性代码增长 | 中 | 低 | 每个过渡期版本结束后立即清理 deprecated code，作为版本发布的标准 checklist 项目 |

### 5.4 监控与验证指标

新增以下 OTel 指标以衡量 Phase 1-3 的实施效果：

| 指标 | 类型 | 期望趋势 | 所属阶段 |
|------|------|---------|---------|
| `aero_eventbus_publish_blocked{reason}` | Counter | → 0（方向③修复） | Phase 1 |
| `aero_resiliency_breaker_open{name}` | Gauge | 应根据故障正常 toggle | Phase 2 |
| `aero_resiliency_bulkhead_wait_time{name}` | Histogram | P99 < 5ms（无竞争时 < 10μs） | Phase 2 |
| `aero_saga_commit_duration` | Histogram | PUT 路径增加 < 5ms（SQLite）| Phase 3 |
| `aero_saga_compensation_executed` | Counter | 应极低（< 总操作数的 0.1%）| Phase 3 |
| `aero_eventbus_queue_depth{priority}` | Gauge | 不应超过配置的 queue cap 的 80% | Phase 3 |
| `aero_context_background_calls{file}` | Counter | → 0（方向① lint） | Phase 1 |
| `aero_mutation_test_kill_ratio` | Gauge | 持续 > 60% | Phase 1+ |

---

## 总结

从架构层面看，aero-vault 当前处于**功能密度已到天花板而生产韧性仍是负债**的阶段。v38 分析团队的 5 个方向击中了这个过渡的核心要害，我补充了 5 个扩展方向，**关键区别在于**：

| 维度 | v38 方向（分析团队的） | 本文扩展（补充审查的） |
|------|---------------------|---------------------|
| 焦点 | 修复已知问题 | 引入新的架构韧性模式 |
| 范围 | 在现有抽象层内修补 | 引入 1-2 个新抽象层 |
| 风险 | 低（不影响外部接口） | 中高（需向后兼容） |
| 回报 | 立竿见影（MTTR、FD 泄漏） | 长期（架构韧性跃升） |

**建议的执行顺序** 是先解决 v38 的全部 5 个方向（Phase 1），再推进扩展方向 α、β、ε（Phase 2），最后根据运维信号决定是否需要 δ 和 γ（Phase 3）。Phase 1 的 ROI 最高、风险最低、收益最可见，是"去说服团队做这件事"的切入点。

一个可供团队吸纳的核心治理建议：**每次发布周期（sprint）的最后一个版本不再加新功能，只做非功能改造。** 这来自一个经验观察——功能密度高的系统一旦停止韧性投入，运维成本会指数增长。aero-vault 已经在这个拐点上了。
