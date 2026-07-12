以下是对验证报告的架构级深度分析。

---

# 架构分析报告：aero-vault 五方向深化设计

## 1. 架构评估

### 1.1 当前架构的显性优势

从验证报告中可以归纳出 aero-vault 的架构设计有几个突出优点：

**分层清晰，职责单一。** Protocol Adapters → FileService → Storage/Repository/EventBus 的三层模型在实战中站住了。方向一验证了 MCP adapter 自始至终未绕过 FileService；方向四验证了 NotificationRule 的持久化层与事件发布层的解耦虽有空隙（持久化后未被消费），但从分层角度，这恰恰是**接口契约正确但实现不完整**——好过"实现完整但耦合混乱"。架构债在边界而非核心。

**扩展点设计务实。** `storage.Storage` 接口、`repository.Repository` 接口、`events.Bus`、`config.Load()` 的注入点都是一致的。验证报告逐层确认了这些接口的合同完整性。方向三的 `rateLimitBypass` 虽然没有标准化，但它证明了限流器在设计时确实考虑了某些路径需要跳过——缺口在实现粒度而非设计意图。

**持久化层与业务层解耦彻底。** 方向四验证了 `NotificationRule` 的读写完整但业务层不消费，说明两层之间有清晰的数据边界。这是好事——迁移、回滚、测试都可以独立进行。

### 1.2 当前架构的隐性债务

以下债务是验证报告中暴露但未直接命名的：

**债务一：审计日志分裂。** 目前存在三个审计路径：`audit_log` 表（admin 操作）、`ai_usage` 表（AI 调用）、MCP `RecordUsage`（文件读取）。三个表各自为政，查询全局审计轨迹需要 UNION 或外部 join。这是标准的**半结构化审计反模式**——当审计需求从"记录谁做了什么"变成"做全局安全审计"时，碎片化会大幅提高查询成本。

> **根本原因：** 三个功能模块各自引入时没有统一审计抽象层，各自采用了当时最"顺手"的持久化方式。

**债务二：事件类型集是闭集。** 当前只有 3 种 `EventType`。方向四指出扩展事件类型需要跨 4 层代码修改。这暴露了一个设计问题：**事件类型的定义没有插件化扩展点。** 与 S3 兼容的场景不同——S3 有数十种事件类型（`s3:ObjectCreated:*` 等路径表达式），当前的硬编码 set 无法优雅扩展。

> **根本原因：** 事件总线在初始设计时被视为"内部消息通道"而非"外部集成层"，因此事件 schema 没有考虑第三方订阅者视角的标准化。

**债务三：中间件链的声明与执行不一致。** 验证报告指出 `applyMiddleware` 调用链顺序（方向三）与 `AGENTS.md` 定义的固定顺序（I4）不一致。这是一个**文档-代码漂移**案例。中间件链是安全基石，代码顺序与文档规定的顺序不一致可能导致安全回归测试漏掉中间件依赖关系。

> **严重程度：** 当前没有因此导致安全漏洞（验证报告没有指出实际功能错误），但它是每次重构的风险源——如果有人调整中间件顺序而不更新文档，安全模型可能被静默侵蚀。

**债务四：限流器碎片化。** 三个独立限流器（全局 RPS、AI 组 RPS、ConcurrencyLimiter）各有各的配置、各有各的 token bucket、各有各的响应头格式。这不是"职责分离"，而是**配置耦合**——运维人员需要知道三个参数之间的交互关系才能正确调优。

> **耦合表现：** `RATE_LIMIT_RPS` 与 `CONCURRENCY_LIMIT` 之间没有数学关系表达。如果 `RATE_LIMIT_RPS=100` 而 `CONCURRENCY_LIMIT=50`，实际吞吐上限是 min(100,50) = 50 RPS，但没有任何文档或告警告知运维人员这个关系。

### 1.3 关键设计决策评估

| 决策 | 合理性 | 评估 |
|------|--------|------|
| Default：SQLite + local FS + 零网络 | ✅ 合理 | CI gate 确定性最高，方向五的零停机挑战是必须接受的成本 |
| Protocol Adapters 不挂中间件链 | ✅ 合理 | 验证了 handler 测试可独立，这是正确的架构选择 |
| Opt-in 安全默认（AI/event/cluster 默认 off） | ✅ 合理 | 降低入门门槛，生产环境通过配置逐步开启 |
| 事件类型为 Go const 而非可注册 | ⚠️ 可接受 | 当前规模（3 种）下够用；扩展到 10+ 时建议改为注册模式 |
| 配置一次性加载 | ❌ 受限 | 方向五确认了这是零停机的主要障碍；但产品早期阶段可接受 |
| 审计日志三表分裂 | ❌ 债务 | 当前规模影响不大，但每新增一个审计点都会加剧碎片 |

---

## 2. 扩展方向

基于验证报告的五方向分析，我提炼出更高维度的架构扩展方向。

### 方向 A：统一审计与计量子系统（P1）

**为什么需要：** 当前分裂的审计（`audit_log` + `ai_usage` + MCP `RecordUsage`）在方向一发布后（MCP 工具权限+审计）会进一步加剧。当安全合规要求"提供某用户在某时段的所有操作"时，三表 join 的查询将不可维护。

**核心挑战：**
- 三个表的 schema 不同（`audit_log` 有 `actor,action,resource,detail`；`ai_usage` 有 `tenant_id,total_tokens,cost_micros`；MCP 有 `caller,path`）
- 无法聚合回溯已有数据（历史数据不能迁移）
- 写入路径不同（HTTP handler、MCP handler、background worker），需要一个统一的接入点

**预期架构变更：**

```
当前:                 目标:
audit_log (admin)     audit.Events (统一入口)
ai_usage (AI)          ├─ sink → audit_log (结构化)
MCP RecordUsage        ├─ sink → metrics (OTel counter)
                      └─ sink → cost_meter (预算控制)
```

- 新增 `internal/audit` 包，定义 `Event` struct（`Actor, Action, Resource, Payload, TenantID, Timestamp` 等通用字段）
- 提供 `audit.Recorder` 接口，FileService 和 MCP handler 均通过该接口记录事件
- `RecordUsage` 不再直写 `ai_usage`，而是通过 `audit.Recorder` 发送 `Event{AIAction}`，由接收器写入 `ai_usage` 表
- 提供多种 sink：`TableSink`（写 `audit_log`）、`MetricSink`（OTel counter）、`BudgetSink`（日费用计算）

**现有系统影响：**
- `service/file_crud.go` 中的直接表写入需替换为接口调用——这是 moderate 规模重构
- 新增 `internal/audit` 包后，需要迁移 mcp 的 `RecordUsage`、admin API 的 `audit_log` 写入
- 历史数据保留在原表不变，审计查询改为视图或应用层 union（或者不回溯）

### 方向 B：可插拔事件类型与路由子系统（P1-P2）

**为什么需要：** 方向四的事件自动化管线扩展遇到事件类型硬编码的瓶颈。S3 兼容的事件类型（`s3:ObjectCreated:Put`, `s3:ObjectCreated:Post`, `s3:ObjectRemoved:Delete`, `s3:ObjectTagging:*` 等）是一个路径层次结构，当前的平面枚举无法表达。

**核心挑战：**
- Go 的 const 枚举无法动态注册
- S3 事件类型支持通配符（`*`），当前的 `==` 匹配逻辑不支持
- 事件 schema 需要向前兼容——已部署的 NotificationRule 在事件类型名变更后不失效

**预期架构变更：**

```
当前:                 目标:
EventType const       events.Registry
  └─ switch/case        ├─ Register("s3:ObjectCreated:Put")
                        ├─ Match("s3:ObjectCreated:*") → true
                        └─ List() → []EventType
```

- `internal/events` 包新增 `TypeRegistry`，支持字符串注册和通配符匹配（类似 `path.Match`）
- `NotificationRule` 的 `Events` 字段存储字符串而非 const——迁移不需要 DDL
- 添加新事件类型只需在初始化阶段 `events.Registry.Register("s3:ObjectCreated:Put")`，无需修改中央枚举
- `Bus.Publish` 处的匹配逻辑从 `==` 改为 `TypeRegistry.Match`，使通配符订阅生效

**现有系统影响：**
- `EventType` 的现有 const 保持向后兼容——注册阶段自动注册所有现有 const
- 已持久化的 `NotificationRule.Events` 是字符串数组，无需迁移
- 影响面较小——主要是 `internal/events/bus.go` 的匹配逻辑和 `main.go` 的初始化

### 方向 C：运维通道与热重载框架（P2）

**为什么需要：** 方向五确认了配置热重载是零停机的基础需求。但不仅仅是 `rate_limit_rps`——还包括 `AUTH_KEYS`、`AI_MODEL_*`、`LOG_LEVEL` 等。需要一个标准化的热重载框架，而不是对每个字段手写 atomic.Value。

**核心挑战：**
- 不是所有配置都适合热重载（`DB_DSN`、`STORAGE_BACKEND` 有架构级影响）
- 热重载与集群复制需要协调（一台节点的配置变更需要广播到其他节点）
- 配置变更的事务性：一个变更涉及多个字段时，要么全部生效要么全部回滚
- 需要版本化配置变更历史以支持审计回滚

**预期架构变更：**

```
config.HotReload       Config (reloadable subset)
  ├─ Set(key, value)     ├─ rate_limit_rps
  ├─ OnChange(cb)        ├─ readonly
  ├─ Version() int64     ├─ log_level
  └─ Snapshot() Config   ├─ auth_keys (需 rebuild Registry)
                         └─ ai_embed_provider (需 rebuild embedder)
```

- 新增 `config.HotReload` 结构体，使用 `atomic.Value` 存储快照指针
- 每个可热重载字段对应一个变更回调列表；`Set` 操作触发所有回调
- `AUTH_KEYS` 变更→回调重建 `Registry`；`AI_EMBED_PROVIDER` 变更→回调重建 `Embedder`
- 不可热重载字段（`DB_DSN`、`STORAGE_BACKEND`）明确列入黑名单，配置变更返回 `ErrNotHotReloadable`
- `RateLimiter` 不再构造时固定 RPS，改为定期从 `config.HotReload` 读取（或监听 `OnChange`）

**现有系统影响：**
- `config.Load()` 当前返回 `Config` 结构体指针——需要将该结构体拆分为 `StaticConfig` + `DynamicConfig`
- `RateLimiter`、`AuthMiddleware`、`Logger` 等组件需要接入变更回调
- 配置变更 API（`PATCH /v1/admin/config`）新增——复用当前 admin 路由

### 方向 D：多协议数据面一致性保障（P2-P3）

**为什么需要：** 当前 REST/S3/WebDAV/MCP 四个协议适配器各自为政。它们都调 FileService，但校验逻辑、错误映射、响应格式各有差异。方向一的 MCP 授权问题只是冰山一角——更深层的问题是：**每个协议对同一个"创建文件"操作的理解是否一致？**

S3 有 bucket owner 的概念，REST 有多租户，WebDAV 有文件锁定，MCP 有工具参数——FileService 作为核心控制器要处理所有这些协议的语义映射。当前架构没有防呆机制来防止一个协议的 edge case 泄漏到另一个协议。

**核心挑战：**
- 协议适配器之间的语义映射没有形式化定义
- S3 的条件请求（If-Match/If-None-Match）在 MCP 中没有对应参数
- WebDAV 的 LOCK/UNLOCK 在 FileService 中没有接口
- 每个协议的错误类型不同，但 FileService 返回统一的 `service.Error`

**预期架构变更：**

```
FileService (核心)
  ├─ semantic mapping (声明式)
  ├─ protocol negotiation
  └─ response normalization

非新增抽象层，而是在现有 FileService 接口上定义:
  - 每个方法的 ProtocolContext (来源协议、请求特征)
  - FileService 根据 ProtocolContext 调整行为 (如 S3 模式启用 ETag 严格校验)
```

- 在 `service/file_crud.go` 的每个方法参数中嵌入 `ProtocolInfo`（如 `source: "s3"`, `features: []Conditional`）
- FileService 根据 `ProtocolInfo` 决定是否绕过某些校验（如 S3 的 bucket ACL 检查 vs REST 的 tenant scope 检查）
- 新增 `service/protocol.go` 定义协议元数据结构和默认行为

**现有系统影响：**
- 当前 FileService 方法参数已较多（`PutObject` 有 ~8 个参数），增加 `ProtocolInfo` 需要 go-style 的 option pattern
- 不需要改 storage 或 repository 层——只影响 service 层
- 协议测试（S3 合约测试、REST handler 测试）需要补充 `ProtocolInfo` 参数

### 方向 E：集群模式与有状态编排（P3）

**为什么需要：** 当前架构以单节点 SQLite + local FS 为默认基线。方向五的零停机分析暴露了 SQLite 场景硬约束。Postgres/pgvector 场景已有集群能力（方向四的 PostgresTransport 用于跨实例广播），但距离真正的集群运维（滚动升级、灰度发布、故障转移）还有距离。

**核心挑战：**
- 存储层一致性（S3 后端适合集群，local FS 不适合）
- 索引重建协调（向量索引在某个节点重建时，其他节点的检索请求如何路由）
- 会话亲和性（S3 multipart upload 需要在同一节点完成）

**预期架构变更：**
```
                    ┌─ Gateway (无状态)
                    │   ├─ rate limiter (全局共享)
                    │   ├─ auth (共享配置)
Client ───────────▶  └─ routing (根据 tenant hash)
                          │
                    ┌─────┴─────┐
                    ▼           ▼
                Node-1       Node-2
                (S3 backend) (S3 backend)
                └─ JobPool   └─ JobPool
                (advisory lock on Postgres:cluster singleton)
```

- 引入无状态 Gateway 节点作为流量入口（复用当前 router 逻辑，去掉 storage/repo）
- 工作节点保持当前架构，加上集群协调（租约、心跳、排空）
- 新增 `internal/cluster` 包处理节点注册、心跳、排空协议

**现有系统影响：**
- 这是最大的架构变更——当前代码假设单节点
- 需要引入 Service Discovery 机制（简单的配置文件或 etcd/consul）
- S3 后端自然可扩展；local FS 场景保持单节点（不参与集群）

---

## 3. 接口设计原则与抽象层建议

### 3.1 核心接口设计原则

**原则一：审计接口应与业务接口解耦。** 当前 `RecordUsage` 直接嵌入业务逻辑。应改为：业务逻辑调用 `audit.Recorder` 的接口返回后不阻塞；审计失败不影响业务路径（异步 fail-open）。

```go
// 推荐设计
type Recorder interface {
    Record(ctx context.Context, event Event) error
}
// 如果返回 error，业务方选择：
// - 忽略（fail-open，推荐）
// - 记录到内部 buffer 后重试（fail-safe）
// - 直接返回给调用方（fail-close，仅在安全等级极高时使用）
```

**原则二：事件类型使用注册机制而非枚举。** 扩展方向 B 已详述。

**原则三：配置变更使用观察者模式。** 当前组件在构造时读取配置一次。应改为所有需要运行时变更的组件实现 `ConfigWatcher` 接口：

```go
type ConfigWatcher interface {
    Watch(config *HotReload) // 注册变更回调，不要返回 error
}
```

`main.go` 的装配顺序中，所有组件在创建后调用一次 `Watch` 注册回调。`config.Set()` 调用后各组件自动重配置。

**原则四：限流器分层接口化。** 当前的三个限流器应统一到一个接口下：

```go
type Limiter interface {
    Allow(ctx context.Context, key string) (bool, RateLimitInfo)
}

type RateLimitInfo struct {
    Remaining int
    ResetAt   time.Time
    RetryAfter time.Duration
}
```

这样方向三中 `writeRateLimitHeaders` 的响应头格式可以统一。`ConcurrencyLimiter` 也实现同一接口——客户端看到的 `RateLimit-*` 头格式一致。

### 3.2 是否需要新的抽象层

**需要新增：** `internal/audit` 包。理由：三个审计路径已经存在，统一接入可以解决方向一的审计碎片问题和方向二的成本归因需求。

**不需要新增：** "协议适配器统一抽象层"。理由：当前四个协议的差异太大（S3 是 RESTful 对象语义、MCP 是 RPC、WebDAV 是文件系统、REST 是 JSON API），强行抽象会陷入 leaky abstraction 陷阱。保持现状，但通过 `ProtocolInfo` 在 FileService 内部做差异化处理更务实。

**可以考虑但不紧急：** "事件路由抽象层"。当方向四的事件类型扩展到 10+ 后，可以考虑引入 `internal/events/router.go` 负责将事件分发到匹配的 handler（类似 HTTP router）。

### 3.3 向后兼容性策略

| 变更类型 | 兼容策略 | 过渡期 |
|---------|---------|--------|
| 审计统一（三表合并查询） | 新增 `audit.Events` 视图（应用层 UNION），不迁移历史数据 | 永久兼容 |
| 事件类型注册 | 启动时自动注册所有现有 const，新代码使用 `Register` | 向后兼容 |
| 配置热重载 | `Config` 结构体新增 `DynamicConfig` 内嵌字段；旧配置 YAML 解析兼容 | 至少 2 个里程碑 |
| 限流器接口化 | 新接口 `Limiter` 实现时，`ConcurrencyLimiter` 保留旧 API（deprecated）+ 新 API | 1 个里程碑后删除旧 API |
| `ProtocolInfo` 参数 | Option pattern：默认值 `ProtocolInfo{Source: "rest"}`，不修改旧调用方 | 向后兼容 |

---

## 4. 技术选型建议

### 4.1 不需要引入新的技术栈

基于验证报告的分析，**五个方向的实现都不需要引入新的外部技术栈。** 理由：

- 审计统一：纯 Go 接口设计 + 现有 SQLite/Postgres 表
- 事件类型注册：纯 Go map + `path.Match`，零依赖
- 配置热重载：`sync/atomic`（已有）+ `sync.Map`，零依赖
- 限流器接口化：纯 Go 接口重构
- 集群模式：Postgres `LISTEN/NOTIFY`（已有）、文件锁（已有）

这是好信号——现有架构的技术选型是合理的，五个方向的债务都是**设计债而非技术栈债**。

### 4.2 唯一应该评估的新依赖

如果方向 E（集群模式）进入实施阶段，**唯一应该评估的新依赖是服务发现机制**：

| 方案 | 适用场景 | 理由 |
|------|---------|------|
| 静态配置文件 | 固定节点数（2-8 节点） | 零运维复杂度，适合中等规模部署 |
| DNS SRV 记录 | 节点数频繁变化 | 无外部依赖，但要配合 DNS TTL |
| etcd / consul | 大规模集群（16+ 节点） | 运维复杂度上升，但提供租约和 watch 能力 |

**建议：** 从静态配置文件起步，预留 `internal/cluster/discovery` 接口。如果未来有集群需求增长，再引入 etcd/consul。

### 4.3 第三方依赖的评估标准

对于所有未来可能引入的依赖，建议使用以下检查清单（来源：验证报告中的 I6 原则——stdlib 优先）：

| 标准 | 权重 | 说明 |
|------|------|------|
| 是否 stdlib 可替代？ | 否决项 | 如果 Go 标准库有等效方案，优先使用（如 `net/http` 替代 gorilla/mux） |
| 是否渗透到业务核心？ | 高 | 依赖渗透到 domain 层后难以替换（如日志库、错误库） |
| 是否在接口边界引入？ | 可接受 | 在适配器层引入（如身份认证客户端、kafka producer）容易替换 |
| 是否影响测试确定性？ | 高 | 需要 mock 或 Docker 的依赖应限制在集成测试层 |
| CI gate 是否需要？ | 中 | CI gate（`go build ./...` 通过）不应要求外部服务 |

### 4.4 自建 vs 采购决策

aero-vault 的当前定位是独立二进制文件，可自托管。这决定了：

| 场景 | 决策 | 依据 |
|------|------|------|
| 审计 | 自建 ✓ | OSS 审计方案（如 OpenPolicyAgent）与当前架构耦合度太低 |
| 统计面板 | 自建 ✓ | 方向二的 admin API 外部依赖少，Prometheus 已就位 |
| 事件路由 | 自建 ✓ | 场景简单（~10 种事件类型），不足以引入 Kafka/NATS |
| 集群协调 | 轻量方案 ⇢ 渐进升级 | 从 Postgres 租约开始，需要时升级到 etcd |

**不采购。** 五个方向都是产品核心能力（基础设施可观测性、安全合规、事件集成），不是差异化竞争点也不是 commodity——它们应该在产品内部深度集成，而不是绑定到外部 SaaS 或闭源组件。

---

## 5. 实施路线图

### 5.1 优先级排序框架

```
优先级 = 安全影响 × 依赖数（逆向） × 架构债深度 × 产品价值

P0: 安全影响高 + 已产生架构债 + 依赖少
P1: 产品价值高 + 已经投入实现 + 依赖可控
P2: 架构完善度 + 可延后但不该无限期拖欠
P3: 未来趋势 + 当前无法论证必要性
```

### 5.2 各方向优先级（重新校准版）

基于方向一 `CostScope` 语义偏移的观察，我在原始五方向的基础上做了优先级调整：

| 排名 | 方向 | 优先级 | 关键理由 |
|------|------|--------|---------|
| 1 | **方向一中的审计统一**（从 MCP 授权拆出） | P0 | 安全基线；方向一若不改，MCP 新工具每次重复审计缺陷 |
| 2 | **方向三限流标准化** | P0 | 中间件链顺序不一致是安全风险；SSE 限流盲区是中危 |
| 3 | **方向二跨租户面板**（仅 admin API 部分） | P1 | 产品刚需（运维人员）；实现代价低（3-5 天） |
| 4 | **方向四事件驱动**（仅事件类型扩展） | P1 | S3 兼容必须；当前 3 种事件类型严重不足 |
| 5 | **方向一 MCP 授权+审计日志** | P1 | 安全基线重启；需要审计统一作为前提条件 |
| 6 | **方向五零停机**（配置热重载） | P2 | 非业务刚需但架构债持续增长 |
| 7 | **方向五跨租户面板历史数据**（usage_snapshots 表） | P2 | 依赖新增大表 + reconcile job |
| 8 | **方向四 S3 事件类型完整映射** | P2 | 功能完善，非 MVP |
| 9 | **方向 D 多协议数据面一致性** | P2 | 深层架构完善度 |
| 10 | **方向 E 集群模式** | P3 | 当前无业务场景必要性 |

### 5.3 阶段划分

**阶段一（Milestone 1）——安全基线加固，预计 2-3 周**

目标：消除方向一（MCP 授权盲区）+ 方向三（限流盲区）+ 中间件链对齐

- [ ] 修正 `applyMiddleware` 调用顺序与 `AGENTS.md` I4 保持一致
- [ ] `toolWriteFile` 添加 `checkPermission` 调用；`toolReadFile` 写入 `audit_log` 而非 `ai_usage`
- [ ] SSE 建立阶段与存活阶段的限流全覆盖
- [ ] 预签名 URL 的限流路径确认与修复
- [ ] `writeRateLimitHeaders` 标准化响应头格式



**阶段二（Milestone 2）——统一审计 + 跨租户面板，预计 3-4 周**

目标：消除审计碎片，交付运维面板，为 MCP 授权审计做准备

- [ ] `internal/audit` 包设计，定义 `Recorder` 接口和 `Event` 结构
- [ ] `audit_log` 表 + `ai_usage` 表 + MCP `RecordUsage` 统一入口
- [ ] `GET /v1/admin/usage` 端点（`SumAICostMicros` + `JobStats` + metrics 聚合）
- [ ] `GET /v1/admin/health` 端点（跨租户状态聚合）

```
决策点：两个端点，是否有更全的 admin API schema 设计？

选项 A：一次性设计完整的 admin API v2（包括 Phase 3 的 usage_snapshots）
  - 好处：接口一次性定型，客户端绑定一次
  - 风险：过度设计——usage_snapshots 的实现方案可能在开发中变化
  
选项 B：增量式添加端点（先 usage + health，后 history + stats）
  - 好处：低风险，快速交付
  - 风险：可能 v1 到 v2 的接口不兼容

→ 推荐 B。运维面向的是内部用户（开发者/运维人员），API 不兼容的摩擦小于过度设计的浪费。
```



**阶段三（Milestone 3）——MCP 完整授权 + 事件扩展，预计 3-4 周**

目标：方向一闭环（MCP 工具完全受控）+ 方向四事件类型扩展

- [ ] 基于 `internal/audit`，将 MCP `toolReadFile`、`toolWriteFile` 的审计迁移到 `audit_log`
- [ ] MCP handler 添加 `scope` 校验（`auth.FromContext(ctx)`）
- [ ] MCP 工具注册时通过 `audit.AuditCategory`（替代模糊的 `CostScope`）描述操作类型
- [ ] `events.Registry` 注册机制 + 通配符匹配
- [ ] 新增 3-5 个事件类型（`EventMoved`、`EventCopied`、`EventTagged`）
- [ ] `Bus.Publish` 处开始消费 `NotificationRule`



**阶段四（Milestone 4）——配置热重载，预计 2-3 周**

目标：方向五的 HotReload 框架

- [ ] `config.DynamicConfig` 定义 + `HotReload` 框架
- [ ] `ReloadableFields` 白名单文档
- [ ] `RateLimiter` 接入 `HotReload`，不再构造时固定 RPS
- [ ] `AUTH_KEYS` 热重载（重建 `Registry`）
- [ ] `PATCH /v1/admin/config` 端点（admin scope）
- [ ] 中间件链支持排空（`ConcurrencyLimiter.Drain()` + SSE 关闭）



**阶段五（Milestone 5）——集群化基础 + S3 事件完整映射，预计 3-5 周**

目标：方向 B + 方向 E 的轻量集群

- [ ] `internal/cluster` 包（静态配置发现 + 心跳）
- [ ] 排空协议（`DRAINING → READONLY → OFFLINE`）
- [ ] Gateway 节点（无状态路由，复用当前中间件链）
- [ ] S3 事件类型完整映射（`s3:ObjectCreated:*` 等）
- [ ] 事件导出到外部总线（SNS/SQS 桥接）——如果你需要与 AWS EventBridge 集成

### 5.4 风险点与缓解策略

| 风险 | 影响面 | 概率 | 缓解 |
|------|--------|------|------|
| 审计统一引入性能回归（每业务操作多一次接口调用） | 方向一 & 二 | 中 | ① `Recorder.Record` 接口声明为非阻塞建议（设计原则一）；② 添加 `telemetry.IncAuditDrop` 计数监控审计丢失率 |
| 中间件链顺序调整导致 handler 测试大面积失败 | 方向三 | 中 | ① 调整前后运行全量 `go test`；② 检查每个 `httptest.NewRecorder()` 测试是否显式构建中间件链（当前约束规定 handler 不自挂链——如果测试遵守了该约束，则不受影响） |
| 事件类型注册与旧规则兼容性 | 方向四 | 低 | 已持久化的 `Events` 字段是字符串数组 → 新注册机制默认包含现有 const；**唯一风险**：如果某规则持久化了 `"EventCreated"` 而未来改名为 `"EventCreated:Put"`，现有规则失效。→ 注册机制同时接受新旧名称（forward compatibility mapping） |
| 配置热重载导致集群节点配置不一致 | 方向五 | 中 | 所有配置变更通过 `PATCH /v1/admin/config` 执行 → 该请求触发 `events.Bus.Publish(ConfigChange)` → 其他节点通过 `PostgresTransport` 收到通知并重载。**单节点手动 `env` 重载不做跨实例传播** |
| SSE 排空导致活跃用户断连 | 方向五 | 高 | 排空前发送 `event: shutdown` SSE 帧，客户端识别后 3 秒内自动重连到其他节点（如果集群模式）。单节点场景：排空前发送 `Retry-After` 响应头，客户端 5 秒后重试。 |

---

## 6. 核心决策追踪（ADC）

以下是对本分析中涉及的关键架构决策的索引：

| 决策编号 | 决策内容 | 章节 | 状态 |
|---------|---------|------|------|
| ADC-001 | 审计统一为 `internal/audit.Recorder` 接口 | 2A, 3.1 | 提议 |
| ADC-002 | 事件类型从 const enum 改为注册机制 | 2B, 3.1 | 提议 |
| ADC-003 | 不引入新的外部技术栈 | 4.1 | 确认 |
| ADC-004 | 集群发现从静态配置起步 | 4.2 | 提议 |
| ADC-005 | admin API 增量式设计（先 usage + health） | 5.3 | 提议 |
| ADC-006 | 审计 sink 采用 fail-open 异步模式 | 3.1, 5.4 | 提议 |
| ADC-007 | 不引入协议适配器统一抽象层 | 3.2 | 确认 |
| ADC-008 | 限流器统一为 `Limiter` 接口 | 3.1 | 提议 |

---

**总结：** 验证报告确认了五个方向的代码锚点准确。本分析在此基础上提出两个架构债修正（审计分裂、限流器碎片化）、五个扩展方向（审计统一、事件可插拔、配置热重载、多协议一致性、集群模式）、以及路由优先级的重新校准。建议以 M1（安全基线）和 M2（审计统一+运维面板）作为接下来 6 周的实现目标。
