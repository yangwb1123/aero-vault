# 架构分析报告：aero-vault 服务层完整性、通知路由、DB 对称性、流控与凭据治理

> **分析依据：** `docs/requirements/expansion-v122-service-transaction-integrity-bucket-notifications-db-asymmetry-streaming-memory-credential-lifecycle.md`
>
> **分析方法：** 全代码库深度扫描 + 5 方向代码锚点逐行验证 + 121 轮前序分析去重交叉验证
>
> **日期：** 2026-07-12

---

## 1. 架构评估

### 1.1 当前架构的核心优势

aero-vault 的整体架构展现出几个值得肯定的设计决策：

**清晰的层次边界（Layered Architecture）：**
- `Protocol Adapters → FileService → Storage/Repository` 的分层是教科书级别的整洁。协议适配器（REST/S3/WebDAV/MCP）保持无业务逻辑的薄层，所有核心逻辑集中在 `internal/service`。
- 这种设计使得新增协议（如已存在的 MCP 和 WebDAV）不需要改动核心业务逻辑，这是架构弹性的有力证据。

**事件驱动解耦（Event-Driven Architecture）：**
- `EventBus` 将 CRUD 操作与副作用（Webhook、Replication、Antivirus）解耦，使得这些功能可以独立演进、独立失败、独立扩展。
- JobPool + worker 模式提供了异步重试和持久化保障，这是分布式系统中正确的失败处理模式。

**Opt-in 安全默认（Safe Defaults）：**
- AI、pgvector、Qdrant、events、cluster、retention、WebDAV 全部 flag-gated 默认关闭。`nil` embedder/llm/reranker 不破坏 core CRUD。
- 这种设计使得 CI gate（SQLite + local FS）始终保持轻量、零网络、零外部依赖，加速开发和测试循环。

**可扩展后端模式（Pluggable Backends）：**
- `Storage` 接口 + `factory.go` + `BackendKind` 枚举使得新增后端（local/S3/OSS/COS）只需实现接口并通过 contract test。
- 这是 Go 生态中接口设计的最佳实践——接口小（柯里化最小方法集）、契约强（`storage/contract_test.go`）。

### 1.2 当前架构的局限性

V122 分析揭示的五个方向本质上暴露了以下架构性问题：

**缺乏操作级原子性（Missing Operational Atomicity）：**
- 方向一的核心问题是：多个持久化层（Storage + Repository）之间的操作缺乏整体原子性。这是分布式系统中经典的"双写问题"。
- 当前的回滚策略（Content-MD5 错误时删除 blob）是点状的、不完整的。`hardDeleteObject` 的 5 步线性执行且每步无法回滚，是事务边界不清晰导致的典型技术债务。

**事件路由模型过于简化（Overly Simplified Event Routing）：**
- 方向二的问题是架构层"短接"的典型案例：`Bus.Publish` 的 `broadcast` 模式是一个占位实现，从未完成与 `notification_rules` 的连线。
- `TopicARN`/`LambdaARN` 字段明确注释为 `unused, kept for compat`——这是重构未完成、产品功能实现一半的警示信号。它暴露了 S3 兼容性开发中的一个常见陷阱：XML 解析和持久化占用了全部开发精力，运行时路由被推迟到"下一轮"，然后被遗忘。

**语言级可移植性错觉（Language-Level Portability Illusion）：**
- 方向三揭示了一个细微但重要的问题：`database/sql` 的通用接口 + `$N→?` 的语法可移植性，让开发者误以为 SQLite 和 Postgres 在功能上是可互换的。
- 但语义鸿沟（`SKIP LOCKED`、`LISTEN/NOTIFY`、向量索引、`LIMIT` 在 `DELETE` 中的行为差异）在 Go 的编译模型中无法被静态检测——它们只在运行时以 SQL 错误或静默退化的方式暴露。
- 这是 Go 生态中数据库抽象层的共性挑战，aero-vault 并非特例，但需要正视。

**内存管理策略缺失（Absent Memory Management Strategy）：**
- 方向四揭示了系统在最基础的管理层面——内存——的缺失。`io.CopyN(io.Discard, offset)` 读取并丢弃该读取的字节，是流处理中特别直观的反模式，因为它浪费了 IO 带宽。
- `MaxInFlight`/`PerTenantMax` 只控制并发请求数，不控制字节级吞吐，这意味着一个 1GB 的 Range 请求和 1KB 的 GET 请求在准入控制上被等同对待。

**凭据治理停留于存储层面（Credential Governance at Storage Layer Only）：**
- 方向五反映了一种常见的安全成熟度模型：认证（Authentication）的存储层先于治理（Governance）完成。密钥过期检查已存在，但轮换工作流、使用率追踪、JWT 撤销这些更高阶的安全能力尚未实现。

### 1.3 关键设计决策评估

| 决策 | 评价 | 是否值得重构 |
|------|------|-------------|
| `FileService` 作为唯一核心控制器 | ✅ **正确。** 所有协议必须经过 `FileService`，保证了统一的 ACL、配额、事件发布 | 不需要 |
| `hardDeleteObject` 的 5 步线性执行 | ⚠️ **经验证在多数场景可接受**。原子性缺失是已知 trade-off，当前 Reconcile 兜底 | **需要**增加 write_log 补偿 |
| `Bus.Publish` 广播模式 | ❌ **占位实现。** `notification_rules` 持久化但永不读取，是功能开发的半成品 | **必须**完成路由连线 |
| `sql.go:rebind` 仅处理语法差异 | ⚠️ **合理但不足。** Go 的 `database/sql` 不提供能力声明机制，需自行构建 | **需要** Capability Registry |
| `GetRange` 使用 `io.CopyN(io.Discard)` | ❌ **反模式。** 每后端都支持原生 Range（S3 Range header、local file seek） | **必须**重构 |
| `lookupStore` 检查 `ExpiresAt` | ✅ **正确实现。** 过期检查是 runtime enforcement 的正确位置 | 已有，需补充上游能力 |

### 1.4 技术债务归纳

| 债务类型 | 程度 | 受影响模块 | 建议偿还策略 |
|---------|------|-----------|-------------|
| **功能半成品**（桶通知只存不用） | **严重** | `EventBus` + `NotificationRule` | 方向二 Phase 1 诊断模式 + Phase 2 实际投递 |
| **缺乏操作原子性**（双写 gap） | **中** | `FileService.Put` / `hardDeleteObject` | 方向一 write_log 补偿 |
| **DB 能力隐式假设** | **中** | `main.go` 启动路径 / 配置层 | 方向三 Capability Registry |
| **无界 I/O** | **中** | `range.go` / `mcp/server.go` | 方向四 GetRange + 监控 Reader |
| **凭据治理缺失** | **低-中** | `auth/` + `admin/` | 方向五 Phase 1-3 逐步实施 |

---

## 2. 扩展方向

基于对当前架构的评估，我识别出以下高价值扩展方向。每个方向包含完整的分析，并考虑了与 V122 文档的协同演进。

---

### 方向 A：操作补偿框架（Operational Compensation Framework）

> **基于 V122 方向一，但超越"双写完整性"视角，提出可跨域复用的通用补偿框架**

#### 为什么需要

V122 方向一聚焦 `Put` 路径的双写一致性，但更本质的问题是：**系统缺少一个可跨功能域复用的操作补偿机制**。当前模式的孤例：
- `Put` 路径：仅 Content-MD5 有回滚
- `hardDeleteObject`：5 步不可回滚线性执行
- `Reconcile`：定时扫描孤儿——被动、有窗口期

如果未来新增功能（如复制写入、对象归档、生命周期转换、冷热分层）也采用多步操作模式，每一个新增功能都需要重新实现补偿逻辑——这是无法规模化的。

#### 核心挑战与技术难点

1. **补偿操作的可逆性不对称**：`storage.Put` 可逆（`storage.Delete`），但 `store.Delete`（S3/OSS）不可逆——补偿必须知道哪些操作是可逆的、哪些只能记录 audit trail。
2. **与现有事件系统的集成**：补偿操作（回滚、清理、告警）自身不应产生新的事件循环——需要一种机制来区分"业务事件"和"补偿事件"。
3. **补偿的幂等性**：Reconcile 线程可能多次扫描同一个 stale 日志行，补偿操作必须是幂等的。

#### 预期架构变更

```
┌─────────────────────────────────────────────────────┐
│ Compensation Framework (internal/compensation/)      │
│                                                      │
│ Key Components:                                      │
│ - IntentLog (compensation_intents 表)                │
│ - IntentRecorder (Insert/Update/Complete)            │
│ - Compensator (运行时正向执行 + 失败反向补偿)          │
│ - RecoveryJob (启动时 + Reconcile 定时扫描 stale)     │
│                                                      │
│ Intent 生命周期:                                      │
│ INITIALIZED → [step1 ok] → PARTIAL → [step2 ok]     │
│   → [all ok] → COMPLETED                             │
│   → [any fail] → COMPENSATING → [done] → ROLLED_BACK│
└──────────────────────────────────────────────────────┘
```

不必每个操作都使用补偿框架——它应是一种**显式选择加入**的机制，开发者通过 `compensation.NewIntent(ctx, "object.put")` 来使用。

#### 对现有系统的影响

- **低侵入性**：补偿框架作为可选层存在，不改变现有 CRUD 路径的行为（write_log 插入失败时退化为当前逻辑）。
- **新功能对齐**：未来的 Replication 写入、Archive 操作、Lifecycle 转换可直接复用框架。
- **不影响 CI gate**：补偿框架的集成测试可以使用 SQLite + 本地存储。

---

### 方向 B：事件路由与集成网关（Event Routing & Integration Gateway）

> **基于 V122 方向二，扩展为通用事件集成层**

#### 为什么需要

V122 方向二聚焦桶通知的运行时缺口修复，但更广阔的需求是：**aero-vault 正在成为多协议统一存储平台，需要将存储事件路由到外部系统**（SQS、Kafka、Redis Streams、SNS、Lambda、Webhook、EventBridge）。当前的单 Webhook 模型不够。

一个统一的集成网关可以让：桶通知 → SQS（AWS 兼容）、事件审计 → Kafka、系统告警 → Webhook、变更数据捕获 → PostgreSQL LISTEN/NOTIFY——全部通过同一配置模型管理。

#### 核心挑战与技术难点

1. **投递保证语义差异**：SQS 提供 at-least-once，Webhook 提供 best-effort，Kafka 提供 offset 精确提交——不同目标的投递语义不同，错误处理和重试策略也不同。
2. **出站目标认证管理**：SQS 需要 AWS 凭证，Kafka 需要 SASL/SSL，Webhook 需要 HMAC——认证信息的存储和轮换需要与凭据生命周期管理（方向五）协同。
3. **目标不可达时的背压**：如果 SQS 队列不可达，事件投递 worker 不应阻塞事件总线——需要背压管理和独立 worker pool。

#### 预期架构变更

```
┌───────────────────────────────────────────────────────────┐
│ EventGateway (internal/events/gateway/)                   │
│                                                           │
│ subscriptions表的扩展：                                    │
│ CREATE TABLE event_subscriptions (                        │
│     id TEXT PRIMARY KEY,                                  │
│     tenant TEXT,                                          │
│     bucket TEXT,              -- optional, all if null    │
│     event_patterns TEXT,      -- JSON array of patterns   │
│     target_type TEXT,         -- 'sqs'|'sns'|'lambda'|     │
│                               -- 'webhook'|'kafka'|'amqp'│
│     target_config TEXT,       -- connection specific JSON │
│     status TEXT DEFAULT 'active',                         │
│     created_at TEXT                                       │
│ )                                                         │
│                                                           │
│ Router: pattern match → fan-out → deliver                 │
│ Worker pool: per-target-type, isolated failure domains    │
│ Retry policy: per-topic (queue-full→retry, bad-payload→drop)│
└───────────────────────────────────────────────────────────┘
```

关键设计决策：gateway 独立于 `Bus.Publish` 路径，通过 `EventSubscriber` 接口从 Bus 接收事件，避免阻塞主事件流。

#### 对现有系统的影响

- **向后兼容**：现有 webhook 配置映射到 `target_type='webhook'` 的订阅行；`EVENTS_WEBHOOK_URL` 环境变量自动生成订阅。
- **方向二 Phase 1（诊断模式）** 是集成网关的自然第一步——在不实现投递的前提下验证规则配匹逻辑。
- **DB 驱动不对称的接口**：gateway 自身的状态存储（subscriptions 表 + delivery_log）应使用可移植 SQL，不依赖 Postgres-only 特性。

---

### 方向 C：自适应运行时能力声明（Adaptive Runtime Capability Declaration）

> **基于 V122 方向三，提出整体化的能力发现机制**

#### 为什么需要

V122 方向三的 Capability Registry 解决了"当前 DB 支持什么"的问题，但更普遍的问题是：**系统需要知道"当前运行时环境支持什么"**——不仅仅是数据库驱动，还包括：
- Storage 后端支持原生 Range？（S3 支持，local 支持，部分 OSS 限制）
- 是否有 SSE 加密能力？
- 是否支持多分片上传？
- 事件传输是否可用？
- worker 并发度受什么限制？

当前，这些知识散布在代码中，在启动时以 `if cfg.X != ""` 的方式检查。一个统一的能力声明框架可以让系统在启动时就有一个完整的运行时能力矩阵，决策路径（`if backend supports X then use X else degrade to Y`）可以在一个地方管理。

#### 核心挑战与技术难点

1. **能力探测的时机和成本**：某些能力需要运行时验证（如尝试连接 Qdrant、发送探测请求到 KMS），不能仅从配置推断。探测失败应触发降级而非崩溃。
2. **能力视图的传播**：`FileService` 需要用到的能力（如 `storage.SupportsNativeRange`）需要在服务构造时注入，不应在每次请求时探测。
3. **动态能力变化**：某些能力可能运行时变化（如数据库主从切换后 `SKIP LOCKED` 变得可用或不可用）——框架应支持重新探测的能力变化。

#### 预期架构变更

```go
// internal/capability/registry.go

type Registry struct {
    caps map[Capability]ProbeFunc  // 函数探测而非静态枚举
}

type Capability string

const (
    CapStorageNativeRange   Capability = "storage.native_range"
    CapDBSkipLocked         Capability = "db.skip_locked"
    CapDBEventTransport     Capability = "db.event_transport_postgres"
    CapVectorIndexPgvector  Capability = "vector.pgvector"
    CapVectorIndexQdrant    Capability = "vector.qdrant"
    CapSSEKMS               Capability = "sse.kms_provider"
    CapMultipartUpload      Capability = "storage.multipart"
)

type ProbeFunc func(ctx context.Context) (bool, error)

func (r *Registry) Check(cap Capability) bool { ... }
func (r *Registry) Probe(ctx context.Context) (map[Capability]bool, error) { ... }
```

能力注册在 `main.go` 的启动装配阶段完成：

```go
// main.go
registry := capability.NewRegistry()

// Storage capabilities
registry.Register(capability.CapStorageNativeRange,
    func(ctx context.Context) (bool, error) {
        // 检查 storage backend 是否实现了 GetRange 接口
        _, ok := store.(storage.RangeGetter)
        return ok, nil
    })

// DB capabilities
registry.Register(capability.CapDBEventTransport,
    func(ctx context.Context) (bool, error) {
        return repository.CheckCapability(db.Driver(), repository.CapEventTransport), nil
    })

// Probe all
caps, _ := registry.Probe(ctx)
```

#### 对现有系统的影响

- **与 V122 方向三的 Capability Registry 本质上相同的精神，但升级为通用框架。**
- **向后兼容**：现有配置驱动的 feature gates 优先于能力探测——如果用户在配置中明确禁用某个功能，能力探测不 override 用户意图。
- **测试友好**：`capability.Registry` 可以在测试中 mock：`registry := mockCapabilityRegistry(map[capability.Capability]bool{...})`。

---

### 方向 D：字节级准入控制与流式预算（Byte-Level Admission Control & Streaming Budget）

> **基于 V122 方向四，将平面化的请求并发控制升级为字节级预算管理**

#### 为什么需要

V122 方向四聚焦 `GetRange` 的字节跳过修复和监控 Reader——这解决了"可见的浪费"和"基础的可观测性"。但更深层的需求是：**系统需要一个字节级的准入控制机制**。

当前的 `MaxInFlight` 控制的是**请求数**，不是**字节数**。一个 1KB 的 GET 请求和 1GB 的 GET 请求在准入控制层面被等同对待。一个慢客户端下载一个 10GB 文件，将长时间占用一个 goroutine、一组连接、以及内核页缓存的压力。在极端情况下，50 个并发的 1GB 请求可以将内存压力推到不可控的水平。

这不仅是大对象的问题——在 MCP 路径上，100 个并发 `read_file` 请求（每个最多 4MB）可消耗 400MB 堆内存。在 S3 Copy 路径上，虽然当前已流式处理，但大规模并发 Copy 操作仍可能因为背压传递（慢存储→快储存）导致内存堆积。

#### 核心挑战与技术难点

1. **字节预算的精确性**：Go 的内存模型不提供精确的"当前 I/O 缓冲使用的内存"计数。只能估算（`atomic.Int64` 计数器）而非精确度量。预算作为**软上限**而非硬上限。
2. **语义和性能的权衡**：byte-level admission 需要在 `Reader.Read` 调用的热路径上增加原子操作。对于高频小请求（`io.CopyBuffer` with 32KB buffer），每秒可能数十万次原子操作——需要评估 overhead。
3. **与现有限流的交互**：已经存在 `RATE_LIMIT_RPS`（请求速率限制）和 `MaxInFlight`（并发请求数限制）。Byte-level 限流是第三层——三个限流层的优先级和交互需要明确。

#### 预期架构变更

```
三层限流模型（从外到内）：

Layer 1: 请求速率限流（RATE_LIMIT_RPS）
   作用于：请求入口（middleware）
   用途：防止突发流量
   单位：请求数/秒

Layer 2: 并发请求数限流（MaxInFlight）
   作用于：请求分发路径
   用途：控制 goroutine 数
   单位：并发请求数

Layer 3: 字节级吞吐限流（STREAM_BUDGET_BYTES）
   作用于：Reader.Read() 调用
   用途：控制内存压力
   单位：并发读取字节数
```

实现方案（最小侵入性路径）：

```go
// internal/stream/budget.go

type Budget struct {
    max  atomic.Int64     // 最大并发读取字节数
    used atomic.Int64     // 当前已用的读取字节数
}

// Acquire 向预算申请 size 字节。返回 Release 函数。
// 当预算不足时，阻塞直到有空间释放或 ctx 超时。
func (b *Budget) Acquire(ctx context.Context, size int64) (func(), error) {
    for {
        cur := b.used.Load()
        if cur+size <= b.max.Load() {
            if b.used.CompareAndSwap(cur, cur+size) {
                return func() { b.used.Add(-size) }, nil
            }
        }
        // 等待 100ms 或 ctx.Done
        //   ← 此处需要避免忙循环，使用条件变量或通道
    }
}
```

#### 对现有系统的影响

- **仅影响并发大对象路径**：`FileService.Get` / `FileService.Put` 路径增加 Budget.Acquire/Release 调用，对 1KB 小请求的开销可忽略不计。
- **不影响现有功能测试**：Budget 在测试中可设为 `math.MaxInt64` 以禁用。
- **与 MCP 4MB 截断的交互**：MCP `read_file` 应优先回归到合理的字节预算管理，而非 4MB 硬截断。

---

### 方向 E：凭据联合治理与控制平面（Credential Federated Governance & Control Plane）

> **基于 V122 方向五，提出 API Key + JWT + 预签名 URL 的统一生命周期管理**

#### 为什么需要

V122 方向五分析了 API Key 和 JWT 生命周期，但忽略了第三个重要的凭据类型：**预签名 URL（Pre-signed URL）**。系统已有 `POST /v1/presign` 端点用于生成有时效的访问 URL。这三种凭据类型（API Key、JWT、Pre-signed URL）各自有不同的生命周期管理需求，但它们在"如何创建→如何验证→如何撤销→如何轮换→如何审计"的链路层面是相同的。

一个统一凭据治理框架可以带来以下能力：
- 单一视图查看所有已签发凭据（API Key + JWT token ID + Pre-signed URL token）
- 基于策略的自动轮换（每 90 天强制轮换）
- 按源 IP 或地域限制凭据使用范围
- 凭据使用率评分和安全评分

#### 核心挑战与技术难点

1. **JWT 的无状态本质与撤销需求的对立**：JWT 的 stateless 设计是优势（不需要查 DB），但撤销需要状态（黑名单）。管理这组 trade-off：黑名单的 TTL、缓存的同步、性能影响。
2. **预签名 URL 的离线撤销问题**：预签名 URL 一旦签发，在过期前不可撤销（不引入中心化检查点）。解决方案是引入可选的"签发放映"——每次预签名签发时记日志，提供 `POST /v1/presign/revoke/{token}` 端点来实时使其失效。
3. **与认证中间件的交互**：凭据治理应在认证中间件中发生，不应每个 handler 独立检查。当前认证中间件已经处理了 API Key 和 JWT——治理增强应注入中间件链内而非之外。

#### 预期架构变更

```go
// internal/auth/credential_registry.go

type CredentialType string

const (
    CredAPIKey       CredentialType = "api_key"
    CredJWT          CredentialType = "jwt"
    CredPresignedURL CredentialType = "presigned_url"
)

// Credential represents any issued credential in the system.
type Credential struct {
    ID             string
    Type           CredentialType
    IssuedAt       time.Time
    ExpiresAt      time.Time
    TenantID       string
    Scopes         []string
    SourceIP       string  // 签发源 IP（用于审计）
    LastUsedAt     time.Time
    RequestCount   int64
    RevokedAt      *time.Time
    RevocationNote string
}

// CredentialStore provides a unified view across all credential types.
type CredentialStore interface {
    List(ctx context.Context, filter CredentialFilter) ([]Credential, error)
    Revoke(ctx context.Context, id string, note string) error
}
```

统一治理 API：

```
GET  /v1/admin/credentials → 列所有凭据（支持 filter: type, tenant, status）
POST /v1/admin/credentials/{id}/revoke → 撤销凭据
GET  /v1/admin/credentials/usage?days=30 → 使用率仪表盘
POST /v1/admin/credentials/{id}/rotate → 轮换凭据（仅 API Key）
```

注意：JWT 的"轮换"不适用（JWT 本质上是一次性签发、时效自过期），但 JWT 的撤销需要黑名单机制。

#### 对现有系统的影响

- **新增端点新增功能**，不影响现有 API 路径。
- 需要新增 `credential_events` 表（迁移 0026+ 的一部分）用于记录凭据生命周期事件——这部分与 V122 方向五的 Phase 1（access log）和 Phase 2（JWT 黑名单）共享基础设施。
- **预签名 URL 的离线撤销**是一个突破性变更——当前预签名 URL 签发的是一次性 URL，不可撤销。增加撤销能力意味着预签名验证路径需要增加一次 DB 查询（或 Redis 缓存）。这是性能和安全的 trade-off，应该作为可选功能 flag-gated。

---

## 3. 接口设计建议

### 3.1 关键模块接口设计原则

基于当前架构的分析，我建议以下接口设计原则：

#### 原则一：懒惰抽象（Lazy Abstraction）

不要为"可能"的复用性提前抽象。只有当出现 3 个或以上的调用方时，才提取为接口。

- **✅ 现有做法符合**：`Storage` 接口当前有 4 个实现（local/S3/OSS/COS）——完全合理。
- **⚠️ 需要改进**：`ChunkCleaner` 接口是否真的需要？如果只有一个实现，考虑内联。

#### 原则二：补偿操作作为一等接口（Compensation as First-Class Interface）

```go
// 可补偿操作的接口
type CompensableOperation interface {
    // Execute 执行操作，返回补偿函数和错误。
    // 如果执行失败，补偿函数将为 nil——调用方需要明确处理。
    Execute(ctx context.Context) (compensate func(context.Context) error, err error)
}
```

这个接口设计上的关键在于：**补偿函数是和操作结果一同返回的**，而不是在操作开始前注册。这允许补偿函数利用操作的成功返回值（如 `storageKey`、`versionID`）来决定补偿策略。

#### 原则三：事件消费者接口最小化

```go
type EventConsumer interface {
    // Consume 接收一个事件并决定是否投递到外部目标。
    // 返回 true 表示已经投递或不需要投递；
    // 返回 false 表示需要重试。
    Consume(ctx context.Context, event repository.Event) (delivered bool, err error)
}
```

这个接口的关键设计在于：
- 返回值直接表达"是否需要重试"——而非通过异常控制流。
- 不假设投递是同步的还是异步的——`Consume` 可以选择入队后立即返回。

#### 原则四：能力声明而非能力协商

不要要求调用方去协商能力（"你的 DB 是否支持 SKIP LOCKED？"），而是让系统声明能力，调用方据此适配：

```go
// ❌ 不推荐：每个调用方轮询
if supportsSkipLocked(driver) { ... } else { ... }

// ✅ 推荐：系统声明，调用方使用
cap := registry.Check(capability.CapDBSkipLocked)
if cap.Available() { ... } else { ... }
```

### 3.2 是否需要引入新的抽象层

#### 建议引入

| 抽象层 | 位置 | 理由 | 复杂度 |
|--------|------|------|--------|
| `compensation.Intent` + `compensation.Recoverer` | `internal/compensation/` | 跨域复用，避免每个功能重写补偿逻辑 | 中 |
| `capability.Registry` | `internal/capability/` | 统一能力管理，避免散点 `if` 检查 | 低 |
| `stream.Budget` + `stream.MonitoredReader` | `internal/stream/` | 统一流式 I/O 管理，提升可观测性 | 低 |
| `gateway.Router` + `gateway.Target` | `internal/events/gateway/` | 统一出站事件路由，替代当前单 webhook | 高 |

#### 建议不引入

| 抽象层 | 不引入的理由 |
|--------|-------------|
| `credential.Credential` 统一接口 | API Key/JWT/Pre-signed URL 的验证路径差异过大，强行统一会导致充血模型 |
| `storage.RangeGetter` 额外接口 | 可以将 `GetRange` 融入 `Storage` 现有接口（用零值表示不支持），而非新增接口 |
| `service.Transaction` 事务管理器 | `FileService` 不是事务管理器——补偿日志已经是最小侵入方案，完整的事务管理器会引入过高的复杂度 |

### 3.3 向后兼容性策略

对于 V122 分析中的 5 个方向，向后兼容策略如下：

| 方向 | 变更类型 | 兼容策略 |
|------|---------|---------|
| 方向一：write_log 补偿 | 新增行为（现有路径不变） | **完全兼容。** write_log 插入失败时退化为当前逻辑 |
| 方向二：通知路由 | 新增行为（诊断模式先于投递） | **完全兼容。** 诊断模式不影响现有广播行为 |
| 方向三：Capability Registry | 基础架构（不改变外部行为） | **完全兼容。** 仅在启动时增加探测和日志 |
| 方向四：GetRange | 接口扩展（`Storage` 接口增加新方法） | **二进制兼容。** 现有后端实现只需 `go build` 通过（未实现 `GetRange` 的后端在运行时才报错） |
| 方向五：凭据治理 | 新增端点和管理能力 | **完全兼容。** 所有新增端点以 `/admin/` 前缀新增，现有端点不变 |

---

## 4. 技术选型

### 4.1 是否需要引入新的技术栈或框架

对这些方向的需求进行分析后，结论是：**不需要引入新的语言或框架。** Go 标准库 + 当前已有依赖可以支持全部 5 个方向的实现。

具体分析：

| 方向 | 是否需新依赖 | 说明 |
|------|------------|------|
| 方向一：write_log 补偿 | **否** | 表 + Repository 方法 + 启动钩子——纯应用逻辑 |
| 方向二：通知路由 | **否**（Phase 1） / **是**（Phase 2 需要 AWS SDK） | Phase 2 的 SQS/SNS/Lambda 投递需要引入 `github.com/aws/aws-sdk-go-v2`（可能已有引入检查 go.mod） |
| 方向三：Capability Registry | **否** | 纯应用逻辑 + 现有 `database/sql` |
| 方向四：流控/GetRange | **否** | 接口扩展 + 后端实现——依赖已存在 |
| 方向五：凭据治理 | **否** | 纯应用逻辑——加密和缓存依赖已存在 |

如果方向 B（集成网关）扩展到 **Kafka** 目标，则可能需要 `github.com/segmentio/kafka-go` 或 `github.com/confluentinc/confluent-kafka-go`。但这应作为单独的 Phase，在 SQS/SNS/Lambda 投递实现之后。

### 4.2 第三方依赖评估标准

在当前架构语境下，引入任何新 `go.mod` 依赖应遵循以下评估标准（加固 AGENTS.md 的 I6 规则）：

| 评估维度 | 标准 | 问题模板 |
|---------|------|---------|
| **必要性** | 功能是否可以用标准库在 1 天内实现？ | "标准库 + 100 行代码能否完成 80% 的功能？" |
| **稳定性** | 依赖的发布频率和 semver 策略 | "主版本是否 > 1？API 是否稳定？" |
| **维护性** | 社区活跃度和维护状态 | "最近 release 在 6 个月内？issues 响应是否及时？" |
| **大小** | 依赖的间接依赖树 | "`go mod graph | wc -l` 是否 < 50？" |
| **安全** | 已知 CVE 和供应链风险 | "是否有未修复的安全问题？" |
| **license** | 兼容性 | "是否与当前 license（AGPL 假设）兼容？" |

### 4.3 自建 vs 采购的决策依据

对于这个项目（Go 编写的本地存储平台），自建是主流选择。但某些子功能仍需权衡：

| 功能 | 自建 | 采购/集成 | 建议 |
|------|------|---------|------|
| **通知投递到 SQS** | 实现 Go 的 AWS SDK 调用——~200 行 | 直接使用 AWS SDK 的标准路径 | **自建**——已有 AWS SDK 依赖 |
| **通知投递到 Kafka** | Kafka 客户端 ~500 行 | 使用 `segmentio/kafka-go` | **建议使用三方库**——Kafka 协议复杂，自建易错 |
| **速率限流** | `rate.Limiter` 标准库可用 | — | **自建（已实现）**——标准库支持 |
| **密钥轮换工作流** | 纯应用逻辑 ~400 行 | — | **自建**——领域特定逻辑，无现成可复用 |
| **DB 能力探测** | `SELECT version()` + 静态映射 ~100 行 | — | **自建**——简单逻辑，无需外部库 |
| **预签名 URL 撤销** | 签名验证路径加 DB 检查 ~200 行 | — | **自建**——领域特定逻辑 |

---

## 5. 实施路线图

### 5.1 优先级排序

基于**业务价值、技术影响、依赖关系、实施复杂度**四个维度的综合评估：

| 优先 | 方向 | 阶段 | 业务价值 | 技术影响 | 前置依赖 | 复杂度 | 投入估算 |
|------|------|------|---------|---------|---------|--------|---------|
| **P0** | 方向三：DB Capability Registry | Phase 1 | **低**（用户不可见） | **高**（基础设施修复） | 无 | **低** | ~1 周 |
| **P0** | 方向一：write_log 补偿 | Phase 1 | **高**（数据可靠性） | **中**（核心 CRUD 路径修改） | 迁移 0026 | **中** | ~2-3 周 |
| **P1** | 方向四：Storage.GetRange | Phase 1 | **中**（性能优化） | **中**（Storage 接口扩展） | 无 | **中** | ~2-3 周 |
| **P1** | 方向五：AccessLog + JWT 黑名单 | Phase 1 | **高**（安全审计能力） | **低**（auth 中间件增强） | 无 | **低** | ~1-2 周 |
| **P1** | 方向二：通知诊断模式 | Phase 1 | **中**（运维可见性） | **低**（Bus.Publish 增强） | 无 | **低** | ~1 周 |
| **P2** | 方向五：密钥轮换 API | Phase 2 | **中**（安全治理） | **低**（admin 端点新增） | P0 验证 | **低** | ~1 周 |
| **P2** | 方向一：删除路径补偿 | Phase 2 | **中**（数据一致性） | **中**（hardDeleteObject 修改） | Phase 1 验证 | **中** | ~1-2 周 |
| **P2** | 方向二：SQS/Lambda 投递 | Phase 2 | **高**（产品完整度） | **高**（事件路由架构） | Phase 1 验证 | **高** | ~3-4 周 |
| **P2** | 方向四：字节级预算 | Phase 3 | **中**（运行时稳定性） | **中**（Stream Budget 组件） | Phase 1 验证 | **中** | ~2 周 |
| **P3** | 方向 C：通用补偿框架 | 独立 | **中**（架构提升） | **高**（新抽象层引入） | 方向一落地经验 | **高** | ~3-4 周 |
| **P3** | 方向 E：统一凭据治理 | 独立 | **中**（安全架构） | **中**（凭据存储统一） | 方向五落地经验 | **中** | ~2-3 周 |

### 5.2 阶段划分和里程碑

#### 阶段 1：基础设施加固（4-6 周）

```
Sprint 1-2（2 周）：
  ✅ 方向三 Phase 1：Capability Registry + 启动探测
    - capability.Registry 结构体 + Probe 方法
    - DB 驱动的 cap 映射
    - main.go 中 events/pgvector/pgfts 检查点注入
    - 启动时 WARN 日志输出
    
Sprint 2-4（2-3 周）：
  ✅ 方向一 Phase 1：Put 路径 write_log 补偿
    - 迁移 0026：write_log 表
    - repository.WriteLog methods
    - FileService.Put 路径注入
    - RecoverOrphanWrites 启动钩子
    - Reconcile.sweepOrphans 增强
    
Sprint 4-6（2 周）：
  ✅ 方向四 Phase 1：Storage.GetRange
    - Storage 接口增加 GetRange 方法
    - local/s3/oss/cos 实现
    - FileService.GetRange 使用 GetRange
    - MCP read_file 可配置最大大小
```

**里程碑 1（6 周后）：数据一致性基础修复 + Storage 原生 Range**
- 写路径的有意日志和启动回滚上线
- Storage 原生 Range 支持部署
- 运行时 DB 能力检测和降级告警上线

#### 阶段 2：运行时稳定性与可观测性（4-5 周）

```
Sprint 7-8（2 周）：
  ✅ 方向五 Phase 1：AccessLog + JWT 黑名单
    - AccessLog 记录 key_label/key_hash
    - jwt_blacklist 表 + 验证路径增强
    - ErrKeyExpired 错误类型 + header
    - 过期 API Key 定时清理 job
    
Sprint 8-10（2 周）：
  ✅ 方向二 Phase 1：通知诊断模式
    - Bus.Publish 增强：读取 bucket notification_rules
    - eventMatchesPattern 模式匹配函数
    - notification_match_log 表（可选）
    - GET /v1/admin/notifications/stats
    
Sprint 10-11（1 周）：
  ✅ 方向四 Phase 2：监控 Reader + 读取指标
    - monitoredReader 包装器
    - OTel 流式读取指标
    - 可配置 STREAM_METRIC_INTERVAL_BYTES
```

**里程碑 2（11 周后）：安全审计 + 可观测性基础**
- AccessLog 可追踪请求来源 key
- JWT 撤销能力上线
- 通知规则匹配可观测（诊断模式）
- 流式 I/O 可度量

#### 阶段 3：产品能力补全（4-6 周）

```
Sprint 12-14（2-3 周）：
  ✅ 方向二 Phase 2：SQS/Lambda/Webhook 投递
    - NotificationTarget 接口
    - SQS 投递实现（需 AWS SDK）
    - Lambda 投递实现
    - 投递 worker pool + 重试策略
    - 投递失败日志
    
Sprint 14-16（2-3 周）：
  ✅ 方向五 Phase 2：密钥轮换 + 使用率仪表盘
    - POST /admin/keys/{token}/rotate 端点
    - 轮换宽限期机制
    - POST /v1/admin/jwt/revoke
    - GET /v1/admin/keys/usage
```

**里程碑 3（17 周后）：产品能力补全**
- 桶通知实际投递到 SQS/SNS/Lambda（需要配置 AWS）
- 密钥轮换工作流上线
- 密钥使用率可查询

#### 阶段 4：架构提升（可选，持续投入）

```
持续（P3/P4，与功能开发平行）：
  🔧 方向 C：从 write_log 到通用补偿框架
    - 提取 compensation.Intent 类型
    - 泛化 hardDeleteObject 补偿
    - 为未来操作提供复用模式
  
  🔧 方向 E：从 API Key 治理到统一凭据治理
    - CredentialStore 统一接口
    - 预签名 URL 可撤销
    - 凭据使用评分和安全评分
  
  🔧 方向 D：从 GetRange 到字节级准入控制
    - stream.Budget 全局实例
    - 大对象读取路径注入
    - 字节级 503 背压
```

### 5.3 风险点和缓解策略

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| **write_log 表写入成为新瓶颈**（高吞吐写入路径增加一次 INSERT） | 中 | 中 | write_log INSERT 失败时退化为 no-op（当前行为）；write_log 使用异步批量插入（非关键路径同步等待） |
| **GetRange 接口扩展导致 Storage 后端实现不完整**（开发者忘记实现新接口） | 高 | 中 | 编译时检查：`var _ storage.RangeGetter = (*s3Backend)(nil)`；运行时退化为 Get+CopyN fallback |
| **JWT 黑名单缓存一致性问题**（多副本实例间的黑名单同步延迟） | 中 | 中 | 黑名单使用 TTL 缓存（5s 过期）+ Postgres LISTEN/NOTIFY 推送失效事件；列表 > 提前更新，最终一致性可接受 |
| **通知投递的 worker pool 被慢目标阻塞**（一个 SQS 目标不可达影响所有通知投递） | 中 | 高 | per-target 独立 worker goroutine；每个 goroutine 有独立超时和退避；监控投递延迟，目标不可达超过阈值时报警但不阻塞 |
| **字节级预算导致吞吐下降**（预算误判导致合法请求被降速） | 低 | 高 | 预算仅作为软上限（soft limit），超限时新请求排队等待而非直接拒绝；可配置阈值和超时时间 |
| **迁移 0026 与现有迁移冲突** | 低 | 中 | 迁移文件编号对齐（确认当前最大迁移文件编号）；Postgres 和 SQLite 双文件同步生成；`migrate test` 正向+反向验证 |

### 5.4 与 V122 优先级建议的分歧说明

V122 分析建议的执行顺序为：**方向三 → 方向一+方向四并行 → 方向五+方向二并行**。

我的路线图稍有调整：将**方向五 Phase 1（AccessLog + JWT 黑名单）提升到 Stage 2**，而非 Stage 3。理由是：
1. AccessLog 的 key_label 记录是**零侵入**的代码变更（仅 middleware 层增加一个 log attribute），但提供**高价值**的安全审计能力。
2. JWT 黑名单解决的是**紧急安全需求**——如果当前有用户依赖 JWT 认证且需要凭据吊销能力，这是安全事件驱动的需求，不应等到 Stage 3。

这个调整不影响 Stage 1 的方向三和方向一的核心基础设施工作。

---

## 最终建议

aero-vault 的当前架构基础扎实，上述 5 个方向本质上是**架构从"功能完整"到"生产可靠"的演进路径**。它们不是重写，而是**增量增强**。每阶段的交付物都保持向后兼容，不破坏现有功能。

最重要的架构决策是：**在引入补偿框架和事件网关等新抽象层之前，先用 Phase 1 的最小侵入方案（write_log、诊断模式、Capability Registry）验证方向的价值和复杂性**。当一个方向被证明有价值且有稳定接口后，再提取为通用抽象层（通用补偿框架、事件集成网关）。这是务实的原则——避免过度工程化的同时保持架构演进性。

---

*本分析基于代码库深度扫描 + V122 分析文档 + Mermaid 架构视图的交叉验证完成。核心结论已在方向三（DB Driver Capability Registry 而非 pg_try_advisory_lock）和方向五（expires_at 检查 + audit 日志已实现）等处修正了初始文档的代码锚点误差。*
