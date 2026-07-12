现在我已掌握两份文档的完整内容。以下是我作为资深架构师的系统性分析。

---

# AeroVault 架构分析：五个系统性纵深缺口的架构审视

## 1. 架构评估

### 1.1 当前架构的显著优势

**分层边界清晰。** 从 `AGENTS.md` 描述和代码库结构来看，AeroVault 采用了典型的六边形架构（Ports & Adapters）——协议适配器层（REST/S3/WebDAV/MCP）都是"薄层"，业务逻辑集中在 `internal/service` 的 `FileService`，持久化抽象为 `Storage` + `Repository` 两层。这一决策是正确的：它使得协议扩展（如未来添加 gRPC 或 GraphQL）的边际成本极低。

**事件驱动骨架合理。** `EventBus` → `Worker` + `Webhook` + `Indexer` 的扇出模式与大多数企业级存储系统的设计范式一致。将 Indexer、Replicator、Antivirus 等异步处理解耦到事件总线，避免了 FileService 的同步路径膨胀。

**Opt-in 安全默认。** AI、pgvector、Qdrant、集群模式、事件等所有"高级"功能均默认为关闭。这一决策对于 CI gate 的稳定性至关重要——`go test ./...` 必须零网络、零 Docker、零外部依赖，从而确保每次提交的基本路径不被破坏。

**单文件/单函数约束。** 500 行文件上限、50 行函数上限、圈复杂度 ≤ 10 的硬性规则，从架构治理角度是极佳的策略。它强制了模块化，防止了 `utils/` 包和 God 类型等反模式的出现。

### 1.2 局限性：三个核心架构债务

**债务一：单体进程的隐式假设。**

尽管系统已有 Postgres 支持、集群单例等功能，但**大量架构决策仍然基于单进程假设**。最明显的证据就是方向三指出的纯内存令牌桶限流器。更隐蔽的问题是：

- EventBus 的 `broadcast()` 使用 Go channel 做进程内扇出——这在多副本部署中意味着每个副本都有自己独立的事件订阅者集合，**不存在跨副本的事件去重或顺序保证**。
- `ChunkCleaner.DeleteObjectChunks` 在硬删除路径同步执行——如果对象是多副本共享后端存储，此操作存在竞态。
- 配置热重载未实现——意味着所有分布式场景的配置变更都需要滚动重启。

这一债务不是 Bug，而是**架构阶段的取舍**。从 MVP 到多副本生产系统，必须正视并系统性地偿还。

**债务二：可观测性的"有而无用"缺口。**

系统已有 OTel + Prometheus 指标 + Grafana 仪表盘，覆盖了 15 个 instrument。**但核心业务路径的关键决策点没有任何可见性指标**：

- 限流器：剩余令牌、违反次数、等待请求数 → 全无。
- 事件总线：丢弃事件（`sendOrDrop` 失败）→ 全无。方向二已确认 `events_dropped_total` 计数器完全缺失。
- Webhook 分发：并发数、重试队列深度、风暴检测 → 全无。
- ChunkCleaner 和 GC 执行：每次执行的耗时、清理对象数、跳过原因 → 全无。

这不是"将来再添加"的问题——**当一个系统没有关键决策点的指标时，生产事故的诊断过程就是纯粹的猜测**。此债务的优先级应高于任何新功能。

**债务三：Tags 作为"存储即死"的元数据。**

方向一精准地定位了这一断层。Tags 的存储和 CRUD 已完整实现——这意味着有人为此投入了工程时间。**但是没有任何消费者使用 Tags 来做任何决策。** 这在架构层面意味着：

- Tags 的存储成本（`objects` 表 JSON 列的索引、序列化/反序列化开销）是**纯浪费**。
- 依赖 Tags 的上层策略（Lifecycle、Auth、Replication）被预留了扩展点，但从未验证这些扩展点的设计是否合理。
- **这是最危险的架构债务**——不完全的功能扩展点比没有扩展点更危险，因为它给未来开发者以"这个能力已经存在"的虚假安全感。

### 1.3 关键设计决策评估

| 决策 | 评价 | 风险 |
|------|------|------|
| FileService 作为唯一业务控制器 | ✅ 正确 | 协议适配器可能绕过 → 审计日志完整性需额外验证 |
| SQLite 作为默认 + Postgres 可选 | ✅ 合理 | 但迁移同步（方向二中的 I2）增加了维护成本 |
| 内存 BBolt 作为限流器状态 | ⚠️ 阶段合理 | 方向三已论证，多副本场景必须重构 |
| 事件总线 channel 作为默认传输 | ⚠️ 单进程合理 | 多副本下需替换为 Postgres LISTEN/NOTIFY 或 NATS |
| AI 管线作为可选组件 | ✅ 正确 | `nil` 安全确保非 AI 路径零影响 |
| 迁移文件双文件策略 | ✅ 严谨 | 但增加了每次 schema 变更的认知负载 |

---

## 2. 扩展方向

以下是我基于上述分析识别的高价值架构扩展方向，与文档中的 5 个方向互补而非替代。

### 2.1 方向 A：多副本协调层（Multi-Replica Coordination Layer）

**为什么需要：**
方向二（事件风暴）和方向三（分布式限流）的诊断都指向同一个根因：**系统缺乏跨副本的状态协调基础设施**。没有这一层，任何"全局"语义（全局限流、全局事件顺序、全局去重）都无法实现。在多副本部署成为标配的市场环境下，这是必须偿还的核心架构债务。

**核心挑战：**
1. **强一致 vs. 最终一致的选择。** 限流器需要强一致（错误的计数导致超额放行是安全漏洞），事件去重可以接受最终一致（偶尔重复事件不是灾难）。同一协调层需要同时支持两种语义。
2. **依赖引入的权衡。** Redis 是最自然的方案，但引入 Redis 意味着运维复杂度跃升。Postgres advisory lock + 本地缓存是零额外依赖的中间方案，但性能上限远低于 Redis。

**预期的架构变更：**
```
当前：                                未来：
┌─────────────────────┐              ┌─────────────────────────┐
│ middleware/          │              │ coordination/           │
│   ratelimit.go       │              │   coordinator.go        │ ← 接口
│   (纯内存令牌桶)     │              │   ratelimit.go          │ ← 重写为客户端
└─────────────────────┘              │   dedup.go              │ ← 新
                                      │   leader.go             │ ← 集群单例封装
                                      │   membership.go         │ ← 可选
                                      ├─────────────────────────┤
                                      │ coordination/redis/     │ ← Redis 实现
                                      │ coordination/postgres/  │ ← PG 实现
                                      │ coordination/local/     │ ← 单进程兼容
                                      └─────────────────────────┘
```

**对现有系统的影响：**
- `middleware/ratelimit.go` 需重新设计为接口驱动的客户端，而非直接的内嵌实现。
- 所有使用当前限流器的 middleware 链无需改动（面向接口不变）。
- 新增的环境变量 `COORDINATION_BACKEND={local|postgres|redis}`。
- **影响范围小，但设计需深思熟虑。** 定义错误的接口将导致后续每个分布式特性都需要修改协调层接口。

### 2.2 方向 B：可观测性基础层增强（Observability Foundation Enhancement）

**为什么需要：**
1.2 节已经论证了当前"有而无用"的可观测性缺口。**这不是增量改进，而是生产就绪的前提条件。** 没有关键决策点的指标，SRE 团队没有能力诊断生产事故，也没有能力做出容量规划和 SLO 承诺。

**核心挑战：**
1. **指标泛滥。** 在不加选择地为每个操作添加指标时，指标基数会爆炸。需要设计一套指标分级体系（RED 方法：Rate/Errors/Duration 覆盖所有请求路径，USE 方法：Utilization/Saturation/Errors 覆盖所有资源）。
2. **结构化日志的缺失。** 当前系统的日志结构未知——如果只是 `log.Printf` 级别的非结构化日志，则在多副本环境中无法有效聚合和查询。

**预期的架构变更：**
```
internal/telemetry/
  ├── metrics.go           ← 已有（15 instruments）
  ├── metrics_extended.go  ← 新增：限流器、事件总线、Webhook、GC 指标
  ├── logging.go           ← 新增：结构化日志接口（slog 或 zerolog）
  ├── tracing.go           ← 新增：OpenTelemetry tracing 集成
  └── slo/                 ← 新增：SLO 计数器 + 燃烧率告警
      ├── definition.go
      └── alerting.go
```

**对现有系统的影响：**
- 现有 `log.Printf` 或 `log.Println` 调用需要逐步迁移到结构化日志——这是纯粹的机械性工作，约 2 天可完成全局替换。
- 新指标添加不应改动现有 instrument——加法变更，零破坏。
- SLO 框架是新增组件，不影响现有代码。

### 2.3 方向 C：策略引擎抽象层（Policy Engine Abstraction）

**为什么需要：**
方向一（Tag 治理）识别了 Tag 需要被生命周期、访问策略、复制规则、合规策略等多种消费者使用。但更深层的问题是：**这些策略的评估模型各不相同**——Lifecycle 是时间触发的时间表达式评估，访问策略是请求时的决策评估，合规策略是异步的持续评估。没有统一的策略引擎抽象层，Tag 驱动的治理将不得不在三个不同的子系统中重复实现策略评估逻辑。

**核心挑战：**
1. **策略评估时机差异。** 访问策略需要在请求路径上同步评估（≤1ms），生命周期策略可以异步评估（可以容忍秒级延迟），合规策略的评估可以更慢。统一引擎必须支持不同的延迟要求。
2. **策略语言设计。** 是用现有的 IAM/JWT-style policy（JSON 结构），还是引入更表达力更强的语言（如 Rego/OPA）？前者保持一致但表达力有限，后者引入新的依赖和学习成本。

**选项权衡：**

| 选项 | 优势 | 劣势 | 推荐场景 |
|------|------|------|---------|
| **A. 扩展现有 PolicyDocument** | 零新依赖，一致性强 | JSON 结构复杂场景下可读性差，条件组合能力弱 | 初期 MVP，方向一的 P0 需求 |
| **B. 引入 OPA/Rego** | 表达力强，已有企业级验证(Cisco, Netflix) | 200KB+ 二进制膨胀，新的运维复杂度 | 多租户策略复杂的企业部署 |
| **C. 自建 DSL** | 控制力最强，可深度集成 | 高风险（设计错误不可逆），不推荐 | ❌ 不推荐 |

**预期的架构变更（推荐选项 A → 渐进演进到选项 B）：**
```
internal/policy/
  ├── engine.go           ← 新增：策略评估引擎接口
  ├── model.go            ← 新增：统一策略模型（从 auth/policy.go 提取）
  ├── lifecycle.go        ← 新增：Lifecycle 规则评估实现
  ├── access.go           ← 新增：访问策略评估实现（包装现有 auth/policy.go）
  ├── replication.go      ← 新增：复制规则评估实现
  └── compliance.go       ← 新增：合规策略评估实现
```

**对现有系统的影响：**
- 需要从 `internal/auth/policy.go` 提取 `PolicyDocument` 到新包——涉及跨包引用重构。
- 现有 `auth` 包的 import 路径不变，但核心模型迁移到 `policy` 后需更新引用。
- 工程量中等（约 3-5 天），但设计阶段需要 1-2 天的专项讨论。

### 2.4 方向 D：存储分层与冷热数据生命周期（Storage Tiering & ILM）

**为什么需要：**
这是方向一（Tag 治理引擎）最自然的"首付"消费者，也是最直接可创造业务价值的方向。企业存储部署中，**超过 80% 的数据在 30 天内不再被访问**。将这些冷数据自动迁移到廉价后端（如 S3 Standard-IA、Glacier、甚至本地 HDD）可以显著降低 TCO。

**核心挑战：**
1. **存储后端间的数据迁移必须在生产流量下进行。** 热副本用户正在读取时，不能因为迁移到冷存储导致请求中断。需要支持"读取时重新提升"（on-access promotion）模式。
2. **迁移的事务性。** 对象从 Local → S3 Standard-IA 的迁移过程涉及：读源 → 写目标 → 更新元数据 → 删源。如果中途崩溃，可能导致对象丢失或重复。

**预期的架构变更：**
```
internal/storage/
  ├── tiering/
  │   ├── manager.go       ← 新增：分层管理器
  │   ├── rule.go          ← 新增：分层规则定义
  │   ├── prompter.go      ← 新增：on-access promotion
  │   └── migration.go     ← 新增：迁移执行引擎 + checkpoint

internal/reconcile/
  └── lifecycle.go         ← 扩展：增加 StorageTier 转换规则
```

**对现有系统的影响：**
- `Object` 元数据模型可能需要增加 `storage_tier` 字段。
- `FileService.Get` 路径需要主-被迁移——如果对象在冷存储层，是否透明地提升（promote）回热存储层？
- `config.go` 需要新的配置块。

### 2.5 方向 E：协议层扩展框架（Protocol Extension Framework）

**为什么需要：**
当前系统已有 REST、S3、WebDAV、MCP 四种协议适配器。从架构演进趋势来看，未来可能增加的协议包括：GraphQL 网关（用于 Admin Console 的复杂查询）、gRPC（用于内部服务间通信的高性能 RPC）、以及可能的自定义企业协议。每次新增协议都需要从零编写 handler、路由、认证适配——这种"重复造轮子"的模式不可持续。

**核心挑战：**
1. **认证兼容性。** 每个协议有自己独特的认证方式（S3 → SigV4、REST → Bearer JWT、WebDAV → Basic Auth）。协议扩展框架不能强制统一认证，但需要提供认证链的组合能力。
2. **错误模型映射。** 每个协议的错误模型不同（S3 → XML 错误响应、REST → JSON、gRPC → status codes）。框架需要自动做错误翻译。

**预期的架构变更：**
```
internal/proto/
  ├── registry.go          ← 新增：协议注册表
  ├── handler.go           ← 新增：通用 handler 接口
  ├── auth.go              ← 新增：认证链组合器
  ├── error.go             ← 新增：错误翻译器
  ├── rest/                ← 移入：现有 REST 适配器
  ├── s3compat/            ← 移入：现有 S3 适配器
  ├── webdav/              ← 移入：现有 WebDAV 适配器
  └── mcp/                 ← 移入：现有 MCP 适配器
```

**对现有系统的影响：**
- **此方向风险最高**——涉及大规模代码重组，且对现有功能的零破坏性要求极高。
- 建议推迟到系统韧性（方向二、三、五）就位后再启动。
- 前期可通过 `internal/api/` 的轻量抽象替代。

---

## 3. 接口设计建议

### 3.1 关键模块的接口设计原则

**原则一：面向失败设计（Design for Failure）。**

当前系统对错误的设计是"向调用方返回错误"。在多副本、分布式场景中，错误不是例外而是常态。所有接口设计应考虑以下问题：

```
/* ❌ 当前设计（假设成功）*/
func (s *FileService) Get(ctx, tenant, key) (*Object, error)

/* ✅ 面向失败设计 */
type GetResult struct {
    Object *Object
    Error  error
    Stale  bool  // 从副本读取的过时数据？
    Tier   Tier  // 从冷存储提升的？
}

func (s *FileService) Get(ctx, tenant, key, opts GetOptions) (*GetResult, error)
```

**原则二：上下文驱动配置（Context-Driven Configuration）。**

当前系统依赖全局公开配置变量（`config.AI_INDEX_ENABLED`、`config.STORAGE_BACKEND` 等）。在新特性量增长后，这种模式不可持续：

```
/* ❌ 当前设计 */
if config.AI_INDEX_ENABLED { ... }

/* ✅ 上下文驱动 */
type FeatureFlags context.Context
ctx = WithFeature(ctx, "ai_index", true)
if HasFeature(ctx, "ai_index") { ... }
```

这对于多租户场景特别重要——不同租户可能启用不同的功能集。

**原则三：分离同步与异步接口。** 当前 `FileService` 的同步 CRUD 操作和异步的 EventBus 发布耦合在同一事务中。在分布式场景下，这会导致：
- 同步路径的延迟受异步后端的健康状况影响。
- 异步操作的失败导致同步操作回滚。

建议：`FileService` 不应直接调用 `EventBus.Publish`；应返回"待发布事件"，由 middleware/协调层决定何时发布（同步发布或最终投递）。

### 3.2 是否需要引入新的抽象层

**需要引入三个抽象层：**

1. **协调层（Coordination Layer）** —— 在上文方向 A 中已详细论证。这是多副本部署的前提条件，优先级最高。

2. **策略引擎层（Policy Engine）** —— 在上文方向 C 中已论证。当前 `auth/policy.go` 中的策略实现与 auth 包的耦合过紧，限制了它被 Lifecycle、Replication 等其他子系统重用。

3. **指标注册层（Metric Registry）** —— 当前 `internal/telemetry/metrics.go` 的 15 个 instrument 散落在不同文件中。建议引入一个集中指标注册表，让每个子系统声明自己需要的指标，注册表负责创建和管理生命周期。这可以防止指标基数失控：

```go
// 指标注册表设计
type MetricRegistry struct {
    // 子系统在 init() 或构造时注册
}

// 每个子系统：
type EventBusMetrics struct {
    Published   *prometheus.CounterVec
    Dropped     *prometheus.CounterVec
    Latency     *prometheus.HistogramVec
}
var Metrics = telemetry.Register[EventBusMetrics]("events")
```

### 3.3 向后兼容性策略

对于五个方向中的所有变更，向后兼容性应该是非协商性的硬性约束：

| 变更类型 | 兼容策略 | 过渡期 |
|---------|---------|--------|
| 配置项新增 | 默认值 = 当前行为（opt-in） | 无期限 |
| 接口变更 | 旧接口标记 `@Deprecated`，新接口并行存在 | 至少 2 个 minor 版本 |
| 存储格式变更 | 迁移以双步走（先写新格式，旧格式读取器兼容） | 1 个 major 版本 |
| 行为变更（如限流器默认开启） | Feature flag 控制 + 公告 | 1 个 minor 版本 |
| 依赖新增（如 Redis） | 可选的 backend 实现，不影响默认 SQLite-only 路径 | 无期限 |

对于方向三（分布式限流）：`COORDINATION_BACKEND=local` 必须是默认值——即使是多副本部署，如果用户未配置 Redis/Postgres 协调层，系统应退化为当前的单实例行为（包括其已知缺陷），而非强制用户必须配置额外基础设施。

---

## 4. 技术选型

### 4.1 是否需要引入新的技术栈

基于五个方向的评估，**最低限度需要引入：**

| 方向 | 必要新增 | 选型推荐 | 理由 |
|------|---------|---------|------|
| 方向一（Tag 治理） | 无 | — | 纯 Go 实现，无需外部依赖 |
| 方向二（风暴防护） | Redis（可选） | Redis 6+ `EVALSHA` 做分布式滑动窗口 | 用于跨副本的事件去重和速率限制；可用 Postgres 作为降级方案 |
| 方向三（分布式限流） | Redis（可选） | 同上 | 和方向二可共享 Redis 实例 |
| 方向四（静态网站） | 无 | — | 纯 Go 实现 |
| 方向五（混沌工程） | `testcontainers-go`（测试用） | 已有 | 仅用于集成测试，非生产依赖 |

**关键决策：Redis 的引入。**

| 选项 | 优势 | 劣势 | 推荐 |
|------|------|------|------|
| **A. 引入 Redis** | 性能最高（μs 级），生态成熟，社区运维经验丰富 | 运维复杂度增加(持久化/HA/网络)，二进制依赖 | 推荐用于生产环境 |
| **B. 只用 Postgres** | 零额外依赖，运维简单 | 性能上限低（ms 级），高并发下 PG 连接池压力大 | 推荐作为默认降级方案 |
| **C. 自建 Raft 协调** | 控制力最强，无需外部依赖 | 工程量极大（~6 个月），bug 风险高 | ❌ 不推荐 |

**推荐方案：** `local`（单进程/开发环境） → `postgres`（小规模生产/零额外依赖） → `redis`（大规模生产）三档可选。`redis` 作为可选的性能优化，不改变系统在无 Redis 下的正确性（仅降低性能）。

### 4.2 第三方依赖评估标准

对于所有新依赖，应强制执行以下评估模板：

```
1. 必要性：是否必须？能否纯 Go 实现？
2. 许可：MIT/Apache2/BSD ✅ | GPL/AGPL ❌
3. 依赖树大小：go mod graph 总依赖数 ≤ 20
4. 社区活跃度：GitHub stars ≥ 1k，最近更新 ≤ 6 个月
5. 安全审计：是否已有已知 CVE？修复流程？
6. 过载模式：依赖不可达时，系统的降级行为？
```

对于 Redis 客户端，当前 Go 生态有两个选项：

| 库 | Stars | 许可 | 依赖树 | 推荐场景 |
|---|-------|------|--------|---------|
| `go-redis/redis` (v9) | 20k+ | BSD-2 | ~5 个 | **推荐**。生态最大，文档最全，支持 Redis 集群和哨兵 |
| `redis/rueidis` | 2k+ | Apache-2 | ~3 个 | 高性能备选，支持 RESP3，但社区较小 |

**推荐 `go-redis/redis`** 用于第一版实现，后续可抽象为 `RateLimitBackend` 接口以支持替换。

### 4.3 自建 vs 采购的决策依据

对于 AeroVault 当前的发展阶段（开源项目/商业产品早期），**几乎所有的方向都不适合采购**：

| 方向 | 自建理由 | 是否有可行的现成方案 |
|------|---------|-------------------|
| Tag 治理引擎 | 完全绑定于 AeroVault 的内部数据模型 | ❌ 无 |
| 风暴防护 | 必须深度集成到 EventBus 内部 | 部分可借鉴 Sentinel（但 Java 生态） |
| 分布式限流 | 算法简单（令牌桶/滑动窗口），自建成本低 | ✅ Redis module (Redis Stack) 可加速 |
| 静态网站托管 | 纯路由逻辑，零外部依赖 | ❌ 无（与 S3 协议的绑定性太强） |
| 混沌工程 | 测试框架已有，只需增加测试用例 | ✅ Chaos Mesh、Litmus 但太重 |

**唯一可考虑集成的现成方案：** 混沌工程方向可以考虑与 `chaos-mesh`（CNCF 项目）集成，通过注入网络延迟/分区来验证 AeroVault 的韧性模式。但即使集成，AeroVault 内部的 `FaultInjector` 接口仍需自建。

---

## 5. 实施路线图

### 5.1 优先级排序

基于"零风险先做、高价值先交付、高风险先验证"的原则：

```
P0（必须，2 周内）：
  ├── 方向二子集：events_dropped_total 计数器 + webhook_concurrency 仪表盘
  ├── 全系统关键决策点指标审计（限流器、事件总线、GC）
  └── 结构化日志迁移

P1（重要，6 周内）：
  ├── 方向二（风暴防护）：事件去重窗 + 每订阅者限流 + Webhook 并发控制
  ├── 方向三（分布式限流）：local 增强 + Postgres 后端
  └── 协调层接口设计（coordination/ 包）

P2（高价值，12 周内）：
  ├── 方向一（Tag 治理引擎）：Tag 条件评估引擎 + Lifecycle Filter by Tag
  ├── 方向三（Redis 后端）
  └── 方向五（混沌测试套件 + FaultInjector 接口）

P3（独立特性，按需）：
  ├── 方向四（静态网站托管）
  ├── 策略引擎抽象层（方向 C）
  └── 存储分层（方向 D）
```

### 5.2 阶段划分和里程碑

**Phase 0 — 可观测性急救（第 1-2 周）**

*重点：在零风险前提下解决当前可观测性缺口的最危险部分。*

| 里程碑 | 交付物 | 验证标准 |
|--------|--------|---------|
| M0.1 | `events_dropped_total` + `webhook_concurrency` 指标上线 | `/metrics` 可查询到新指标 |
| M0.2 | 限流器指标（`rate_limit_violations_total`、`rate_limit_tokens_remaining`） | 压测中可观察到令牌消耗 |
| M0.3 | 结构化日志迁移完成 | `grep -r "log\.Print"` 输出为空 |

**Phase 1 — 协调层奠基（第 3-6 周）**

*重点：设计并实现跨副本协调层，为所有分布式特性提供基础。*

| 里程碑 | 交付物 | 验证标准 |
|--------|--------|---------|
| M1.1 | `coordination/` 包接口设计评审 | ADR 文档签署 |
| M1.2 | `local` 后端（现有行为封装） | 所有现有测试通过 |
| M1.3 | `postgres` 后端（限流器 + 事件去重） | 集成测试验证 3 副本下限流有效 |
| M1.4 | `middleware/ratelimit.go` 重写为 Coordination 客户端 | 原有单副本行为不变 |

**Phase 2 — 风暴防护 + 分布式限流（第 7-10 周）**

*重点：事件系统安全加固 + 全局速率限制。可与 Phase 1 并行。*

| 里程碑 | 交付物 | 验证标准 |
|--------|--------|---------|
| M2.1 | 事件去重窗（可配置窗口时间） | 5 秒内 10 次同事件 → 1 次投递 |
| M2.2 | Webhook 并发控制（信号量模式） | 并发 webhook 数不超过 `MAX_CONCURRENT_WEBHOOK` |
| M2.3 | 级联检测（TraceId 跳数方案） | 超过 N 跳后自动熔断 |
| M2.4 | Redis 协调后端 | 基准测试：local vs. postgres vs. redis 吞吐对比 |

**Phase 3 — Tag 治理引擎（第 11-15 周）**

*重点：让 Tags 从"存储即死"变为"标记即策略"。*

| 里程碑 | 交付物 | 验证标准 |
|--------|--------|---------|
| M3.1 | `internal/tagengine/` 条件评估 | `{key=value AND (key2=value2 OR key3=value3)}` 正确评估 |
| M3.2 | Lifecycle Filter by Tag | 含 tag `expire=yes` 的对象在过期后自动删除 |
| M3.3 | Access Policy Condition by Tag | `Condition: StringEqualsIfExists("s3:ExistingObjectTag/owner", "alice")` 生效 |
| M3.4 | Tag 变更事件 + 引擎重评估 | 移除 tag → 相关策略重新评估 |

**Phase 4 — 混沌工程 + 韧性验证（第 16-18 周）**

*重点：验证 Phase 1-3 的所有韧性模式在生产故障下是否按预期工作。*

| 里程碑 | 交付物 | 验证标准 |
|--------|--------|---------|
| M4.1 | `FaultInjector` 接口 + `FaultyStorage` 包装器 | 集成测试可注入延迟/错误 |
| M4.2 | 6 个混沌测试用例 | `make chaos-test` 全部通过 |
| M4.3 | 韧性验证 Runbook | `docs/chaos/` 下 4+ 实验文档 |

**Phase 5 — 静态网站托管（独立排期）**

*重点：独立特性，可与 Phase 3/4 并行。*

| 里程碑 | 交付物 | 验证标准 |
|--------|--------|---------|
| M5.1 | `BucketConfig.Website` + S3 `?website` 子资源 | `aws s3api put-bucket-website` 成功 |
| M5.2 | 请求重定向引擎 | `GET /` → `index.html`；`GET /nonexistent` → `error.html` |
| M5.3 | SPA 路由兼容（404 → index.html 重写） | React/Vue 路由在浏览器中正常工作 |

### 5.3 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| **Coordination 接口过度设计** | 中 | 中 | 遵循 YAGNI：当前只设计限流 + 去重两个能力，不超前设计完整协调层 |
| **Redis 引入导致默认路径退化** | 低 | 高 | 强制 `local` 为默认后端；所有 Redis 功能仅当显式配置 `COORDINATION_BACKEND=redis` 时激活 |
| **Tag 治理引擎的竞态条件** | 中 | 中 | 提供两种评估模式：Snapshot（策略评估时快照 Tags，安全但可能过时）vs. ReadCommitted（评估时实时读取，一致但性能差） |
| **混沌测试的假阳性** | 高 | 低 | 所有混沌测试断言有超时窗口（非即时断言）；`chaos` build tag 不作为 CI gate |
| **方向四（静态网站）安全漏洞** | 中 | 高 | 默认禁止公开读；仅当显式配置 `WEBSITE_PUBLIC_READ=true` 时激活；所有异常情况返回 404 而非信息泄露的错误信息 |
| **跨方向依赖阻塞** | 中 | 中 | 每个方向定义后向兼容的中间产物；例如限流器方向可以先完成协调层接口和 local 后端，即使 Redis 后端延迟也不阻塞其他方向 |

### 5.4 不建议优先做的事情

1. **方向 C（策略引擎抽象层）过早引入。** 在 Tag 治理引擎只有 1-2 个消费者（Lifecycle）之前，策略引擎的抽象只增加复杂度没有价值。等到有 3+ 个消费者时再考虑提取。

2. **方向 D（存储分层）。** 这是企业级特性，但当前系统甚至还没有稳定的多副本部署方案（Phase 1 解决）。过早引入存储分层将增加巨大的测试和验证成本。建议延期到 Phase 1-2 完成后。

3. **gRPC/GraphQL 等新协议。** 四种协议适配器已经覆盖了当前的市场需求。在方向 E（协议扩展框架）就位之前，新增协议将增加维护负担。

4. **方向五的运行时混沌注入端点。** `POST /v1/admin/chaos/inject` 虽然炫酷，但其安全风险（即使有认证）和实现成本远高于纯测试套件的收益。建议仅实现 `FaultInjector` 接口 + 测试套件，不实现运行时端点。等到混沌工程成为日常运维流程（至少 Phase 4 完成后）再考虑。

---

## 总结

AeroVault 已经拥有一个架构边界清晰、扩展性良好的核心系统。五个系统性纵深缺口定位准确，其中**方向二（风暴防护）和方向三（分布式限流）触及了系统从单机走向分布式的核心架构债务——缺乏跨副本协调层**。这一债务的偿还，应该优先于任何新特性（包括 Tag 治理引擎和静态网站托管）。

我的核心建议可以总结为三句话：

1. **先在可观测性上止血。** 花 2 周时间补齐关键决策点的指标——这不需要设计评审，不需要跨团队协调，纯收益、零风险。

2. **用 Coordination 接口统一分布式语义。** 不要为限流器和事件去重分别设计不同的分布式方案。`coordination/` 包的接口设计应该是 Phase 1 最重要的交付物——它的设计质量将影响未来 2-3 年的所有分布式特性。

3. **Tag 治理引擎的架构价值大于业务价值。** 从架构视角看，Tag 治理不是"给用户一个好用的功能"——它是**验证策略引擎抽象层是否合理的试金石**。如果 Tag 治理引擎必须迁就三个不同子系统（Lifecycle、Auth、Replication）的既有设计，无法建立统一的策略评估模型——这意味着系统的抽象层已经不够了。这是我建议在 Phase 3 重点关注的核心命题。
