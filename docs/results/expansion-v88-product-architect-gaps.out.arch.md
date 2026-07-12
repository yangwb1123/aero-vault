# 架构深度分析：AeroVault 扩展方向与系统债务

> **分析对象：** `expansion-v88-product-architect-gaps.out.md`（5 方向）+ `expansion-v104-architect-systemic-gaps.md`（另 5 方向）+ 项目全局架构（`AGENTS.md`）
> **方法：** 交叉审阅 v88、v104 两卷共 10 个方向的代码级发现，结合整个 documentation corpus（137+ 轮分析）做系统性评估
> **立场：** 不直接验证代码行（两卷已验证），而是在架构层面评估方向的价值、优先级、关联性、以及更广泛的系统含义

---

## 1. 架构评估

### 1.1 当前架构的核心优势

AeroVault 的架构在许多方面展现了成熟的设计判断：

| 优势 | 依据 |
|------|------|
| **清晰的六边形架构（Ports & Adapters）** | `FileService` 作为核心控制器，被 REST/S3/WebDAV/MCP 四个协议适配器环绕，隔离了协议逻辑和业务逻辑 |
| **Opt-in 安全默认** | AI、pgvector、Qdrant、复制、集群单例、WebDAV 全部 flag-gated，默认 off；`nil` embedder/llm/reranker 不会破坏核心 CRUD — 这是生产级系统的正确默认值 |
| **可插拔持久化层** | Storage 和 Repository 的接口抽象使得后端可以独立替换（local↔S3↔OSS↔COS；SQLite↔Postgres） |
| **事件驱动的工作流** | EventBus + Jobs + Workers 为异步处理（AV 扫描、复制、Reconcile）提供了可扩展的骨架 |
| **丰富的可观测性** | OTel + Prometheus + 15 个 instruments + Grafana 仪表盘 — 小团队项目中有此基础设施实属罕见 |

### 1.2 架构债务与系统性问题

两卷文档揭示了**三类不同层次的架构债务**：

#### 第一类：显性缺口（可直接修复，低风险）

| 债务 | 影响范围 | 修复难度 |
|------|---------|---------|
| Agent `dispatchTool` 无权限/配额/审计 | Agent 安全基座缺失 | 小 |
| `UploadPart` 无 size 校验 + `uploads` map 无 TTL | 存储效率 + 内存泄漏 | 小 |
| `store.Put` → `repo.UpsertObject` 无补偿事务 | 数据一致性（孤儿 blob） | 中 |
| `Bus.Publish` 不读取 `notification_rules` | 产品功能半实现 | 中 |
| 流式路径 `io.Copy`/`io.ReadAll` 无内存预算 | OOM 风险 | 中 |

#### 第二类：结构性债务（需架构层面调整）

| 债务 | 表现 | 修复难度 |
|------|------|---------|
| `Storage` 接口 `Backend() string` 作为唯一能力标识 | 隐式分支散落在各处，无法组合多后端 | 大 |
| DB 驱动特性不对称（Postgres-only 功能在 SQLite 上无优雅降级） | 运行时静默失败 | 大 |
| 认证凭据无生命周期管理（`expires_at` 被存储但从不检查） | 安全运维债务 | 中 |

#### 第三类：系统性盲区（需多轮迭代的战略方向）

| 盲区 | 核心挑战 |
|------|---------|
| 数据平面缺乏准入控制（字节级内存预算、流式背压） | 本质上是数据平面与控制面的分离 |
| 无 SAGA/补偿事务框架 | 当前"尽力补偿 + Reconcile 兜底"模式随规模扩大不可持续 |
| 多后端组合路由 | 需要能力契约 + 路由策略 + 迁移生命周期三个子系统协同 |

### 1.3 关键架构判断：最值得质疑的设计决策

在所有文档中，**最值得推敲的设计决策**是 `Storage` 接口的 `Backend() string` 模式。它暴露了三个根本性问题：

1. **类型擦除**：Go 接口本可以通过类型断言或策略模式表达行为差异，但选择了最弱的字符串标识
2. **运行时发现**：调用者必须调用、捕获 `"not implemented"` 才知道后端不支持什么——违背了"fail fast"和"make illegal states unrepresentable"原则
3. **阻碍组合**：字符串无法优雅地表达 "AND"（一个后端同时支持 multipart 和 versioning）或 "以最高能力为准" 的组合语义

对比方案：Grafana 的 `backend.QueryDataHandler` 使用 `CheckHealth(ctx, req) (*CheckHealthResult, error)` 和 `QueryData(ctx, req) (*QueryDataResponse, error)` 的接口拆分；Hashicorp 的 `go-plugin` 使用版本化的 capability 协商。AeroVault 当前的设计在这两种成熟模式之间选择了最弱的方案。

---

## 2. 扩展方向

综合 v88 和 v104 两卷共 10 个方向，我筛选出**对架构影响最大**的 5 个扩展方向，按战略价值排序。

### 方向 A（P0）：写入路径补偿事务框架（来自 v88#4 + v104#1）

两个文档从不同角度指向了同一个问题：v88#4 从 `Put`/`Delete` 路径的时序依赖切入；v104#1 从更全面的 SAGA 模式视角覆盖了相同的存储↔元数据双写 gap。

**为什么是 P0：** 
- 这是**数据完整性**问题。孤儿 blob 和幽灵元数据直接影响 RPO/RTO，是企业级存储系统不可接受的设计。
- 当前依赖 Reconcile 的 `sweepOrphanBlobs` 作为兜底——但 v88 已指出这是一个周期性扫描，不是及时的补偿。窗口期内发生的 crash 仍然会产生不可恢复的孤儿。
- 与方向 C（分片上传治理）的交集：`CompleteMultipart` 路径同样存在存储↔元数据的双写 gap，可共享同一补偿框架。

**核心挑战：**
- 补偿操作在并发写入同一 key 时可能误删对方的数据（v88 已标注）。需要**乐观锁**（使用 `storageKey + updated_at` 或 `object_version_id` 作为 ETag）。
- 补偿框架需要在 Service 层注入，但不能侵入每个方法的正常逻辑流程。Go 的 `defer` 模式适合但覆盖有限。
- 分布式环境下的补偿：补偿在另一个节点上执行是否安全？答案：因为是幂等 Delete，所以安全。

**预期架构变更：**

```
FileService 内部新增补偿管理器：

compensator := NewCompensator(logger, metrics)
compensator.Register("put", func(ctx, undoCtx) error {
    return store.Delete(ctx, sk)  // 幂等
})

// 使用方式：
func (s *FileService) Put(ctx, ...) (Object, error) {
    defer compensator.Flush(ctx, "put")  // 所有注册补偿在函数退出时执行
    
    info, err := store.Put(ctx, sk, ...)
    compensator.Register("put", func(ctx) { store.Delete(ctx, sk) })
    
    obj, err := repo.UpsertObject(ctx, ...)
    if err != nil {
        compensator.Force(ctx)  // 立即执行所有已注册补偿
        return Object{}, err
    }
    compensator.Clear("put")  // upsert 成功，清除补偿
}
```

**对现有系统的影响：** 中。新增 `internal/service/compensator.go`；侵入 `Put`、`hardDeleteObject`、`CompleteMultipart` 三个热点路径；不改变接口签名。新增 Prometheus 指标 `write_path_compensations_total`。

### 方向 B（P0）：Agent 工具执行沙箱与治理（来自 v88#1）

**为什么是 P0：** 
- 当前 Agent 是"权力的真空"——继承租户全部权限，无审计、无配额、无范围收缩。在企业部署中这是**安全审计的阻挡项**。
- 实施量级小（v88 评估为小～中），与方向 A 无依赖冲突，可并行开发。
- 方向 A 和 B 都影响"我可以把这个系统交付给客户吗"的判断——前者影响数据完整性，后者影响安全合规。

**为什么不和 MCP 工具治理统一：**
v88 提到 Agent 和 MCP 存在工具代码重复（v75 已覆盖），但**安全模型的统一**比代码统一更重要。Agent 是 LLM 驱动的自动工具循环，MCP 是用户驱动的按需调用——两者的威胁模型不同：
- Agent：威胁在于 prompt injection + 隐蔽数据外泄
- MCP：威胁在于非法协议调用 + 未授权访问

如果强行统一治理模型，可能导致 Agent 的安全策略过松或 MCP 的用户体验过紧。建议在 Service 层共享 `callReadFile`/`callSearch` 等核心方法，但治理层（sandbox/budget/audit）各自独立。

**预期架构变更：**

```go
type AgentSession struct {
    ID        string
    Tenant    string
    Budget    ToolBudget        // 每工具配额
    Scope     ToolScope         // 访问范围收缩
    Audit     *AuditRecorder    // 审计记录器
}

type ToolBudget struct {
    MaxReads     int  // 剩余可读次数
    MaxSearches  int  // 剩余可搜索次数
    MaxListFiles int
    MaxBytesRead int64 // 累计读取字节上限
}

type ToolScope struct {
    AllowedBuckets []string      // 空=全桶允许
    AllowedPrefix  string        // 前缀限制（预留）
    DenyPatterns   []string      // 路径黑名单（glob）
}
```

**对现有系统的影响：** 小。新增 `internal/ai/session.go`；`dispatchTool` 增加 session 参数；复用已有的 `audit_log` 表和 PII Detector。

### 方向 C（P1）：分片上传生命周期治理（来自 v88#3）

**为什么是 P1 而非 P0：** 
方向 C 影响的是**存储效率**而非数据完整性。孤儿 `.multipart/` 目录和 `uploads` 表残行会浪费磁盘，但不会丢失数据。同时现有 `Reconcile` 框架提供了天然的扩展点可以挂接扫描逻辑。

**与方向 A 的关系：** 双重依赖——方向 C 的 `CompleteMultipart` 补偿路径可以复用方向 A 的补偿框架；方向 C 的孤儿上传 GC 可以在 `Reconcile` 中新增一个 `sweepStaleUploads` 阶段，与 `sweepOrphanBlobs` 并列。

**核心挑战：**
- 两层状态同步：`local.uploads` 内存 map + `repository.Upload` 持久表。TTL 需要同时作用于两层（内存 map 通过定时淘汰 goroutine；持久表通过 Reconcile + `expires_at` 列）。
- S3 协议要求最小分片大小 5MB（最后一片除外）——但 AeroVault 也允许 REST API 直接调用分片上传。最小分片应作为 `Storage` 的能力属性（方向 2 的 `Capabilities`），而非硬编码在 local 实现中。

**预期架构变更：**
- `local_multipart.go`：新增 `UploadPart` 前的 size 校验（引用 `cfg.MinPartSize`）
- `uploads` 表：新增 `expires_at` 列（已有 `created_at`，可计算）
- `Reconcile`：新增 `ListExpiredUploads` + `AbortMultipart` 阶段的通用实现
- 新增配置：`MULTIPART_UPLOAD_TTL_HOURS`、`MULTIPART_MIN_PART_SIZE`、`MULTIPART_CONCURRENT_PER_KEY`

### 方向 D（P1）：存储后端能力契约（来自 v88#2）

**为什么不是 P0：** 这是一个**组合性的架构改善**而非功能缺陷。当前单后端模式工作正常，多后端组合是 v87 方向四（StorageClass 分层）的前置基础设施。如果 v87 方向四不列入路线图，方向 D 的独立价值有限。

**核心权衡：**

| 方案 | 描述 | 复杂度 | 价值 |
|------|------|--------|------|
| 方案 A（最小）：`Capabilities()` 方法 | 在现有 `Storage` 接口新增 `Capabilities()`，系统在调用前检查 | 小 | 解决了运行时"not implemented"问题 |
| 方案 B（标准）：能力契约 + 路由层 | 新增 `StorageRouter` 根据 storage class 选择后端 | 中 | 支持组合多后端 |
| 方案 C（完整）：能力契约 + 路由 + 迁移双写 | 方案 B + 后端迁移期间双写 + 后台 transition worker | 大 | 完整多后端生命周期管理 |

**建议：** 只实施方案 A（`Capabilities()`），作为方向 D 的"最小可行路径"。方案 B 和 C 与 v87 方向四（StorageClass 分层）深度绑定，应等待 StorageClass 分层的架构决策后再推进。

**对现有系统的影响：** 小～中。`Storage` 接口新增方法；`Local`/`S3`/`OSS`/`COS` 各实现一个；`FileService` 中增加能力检查的辅助函数。

### 方向 E（P2）：桶通知运行时路由（来自 v104#2）

**这是一个半实现功能的治理修复：** `SetBucketNotifications` CRUD 完整实现（包括 S3 XML 解析和 JSON 持久化），但 `Bus.Publish` 从不读取这些规则。这是极罕见的产品半实现——一般情况下要么不做 CRUD，要么做了 CRUD 就在运行时使用。

**为什么是 P2：** 
- 影响依赖事件路由的生产场景（如"只接收 bucket-A 的 created 事件"）——但不是阻挡项
- 与 Webhook 的重叠：当前 Webhook 是全局单 URL（`EVENTS_WEBHOOK_URL`）。如果只想要某个 bucket 的事件触发 webhook，当前无法做到
- 修复量级中等：只需要在 `Bus.Publish` 中查询 `buckets.notification_rules` 并按规则过滤订阅者

**核心挑战：**
- 通知规则的解析和匹配性能——规则的匹配需要 `regexp` 或 `glob` 模式匹配（`AllowedPrefix`/`DeniedPrefix`），每次 publish 都执行可能成为瓶颈
- 规则变更的实时性——当前规则从 DB 读取后无缓存，每次 publish 都要查询

**建议的架构决策：** 不引入新的事件路由引擎，而是在 `Bus.Publish` 中嵌入一个轻量级 `notifications.MatchRules(tenant, bucket, eventType, key) []Recipient` 函数。规则缓存使用 `sync.Map` + 30s TTL 惰性过期，避免引入 Redis 或 etcd 依赖。

---

## 3. 接口设计建议

### 3.1 三个最关键的接口调整

#### 调整一：`Storage` 接口的能力化

```go
// 当前（问题版本）
type Storage interface {
    Backend() string  // 唯一标识
    // ... 15+ 方法
}

// 建议
type Capabilities struct {
    Features     []Feature           // 支持的特性集
    MaxObjectSize int64              // 0 = 由系统物理限制决定
    MaxParts     int                 // multipart 上限
    MinPartSize  int64               // multipart 最小分片
    SupportedChecksums []string      // ["md5", "sha256"]
}

type Storage interface {
    Capabilities() Capabilities      // 新增：能力查询
    Backend() string                 // 保留：向后兼容
    // ... 其余方法不变
}
```

**原则：** `Capabilities()` 应在构造时确定，运行时不变。这避免了 v88《关键补充》中"构造时检测而非运行时查询"的要求。实现上，每个 backend 的 `New*()` 函数应返回一个缀入了静态 `Capabilities` 的实例。

**向后兼容：** `Backend()` 保留，旧调用方不受影响。新增 `Capabilities()` 是追加而非修改。

#### 调整二：Service 层的补偿事务上下文

```go
// 新增
type Compensator struct {
    logger  *slog.Logger
    metrics telemetry.MetricsProvider
    actions []CompensatingAction
}

type CompensatingAction struct {
    Name string
    Run  func(ctx context.Context) error
}

func NewCompensator(logger *slog.Logger, metrics telemetry.MetricsProvider) *Compensator
func (c *Compensator) Register(name string, fn func(context.Context) error)
func (c *Compensator) Clear(name string)
func (c *Compensator) Execute(ctx context.Context) error  // 执行所有未清除的补偿
```

**接口融合点：** `Compensator` 与 `storage.Storage` 的关系是"补偿器调 Storage"，不是"Storage 知道自己被补偿"。保持了关注点分离。

#### 调整三：Agent 会话治理

```go
// 新增：Agent 治理上下文
type AgentSession struct {
    ID        string
    Tenant    string
    UserID    string          // 来源用户
    Budget    ToolBudget      // 当前剩余配额
    Scope     ToolScope       // 访问范围
    Recorder  AuditWriter     // 审计写接口
}

type ToolBudget struct {
    RemainingReads    int
    RemainingSearches int
    RemainingBytes    int64
}

type ToolScope struct {
    AllowedBuckets []string      // 允许的桶列表；空=全部允许
    Prefix         string        // 路径前缀限制（预留）
}

// AuditWriter 是审计日志的写接口，与现有审计表兼容
type AuditWriter interface {
    Record(ctx context.Context, entry AuditEntry) error
}
```

### 3.2 不需要引入的新抽象层

| 曾经考虑的抽象 | 决策 | 理由 |
|---------------|------|------|
| SAGA 引擎/编排器 | ❌ 不需要 | 当前补偿场景局限在 2-3 个步骤的线性路径，不需要分布式 SAGA 协调器。函数式补偿器足够 |
| 事件路由引擎 | ❌ 不需要 | 桶通知规则匹配是简单的 bucket+event+prefix 三级过滤，不需要路由表。`notifications.MatchRules` 纯函数足够 |
| 内存预算管理器 | ⚠️ 暂时不需要 | 流式路径的内存压力更可能在 admission control（请求级）层面解决，而非字节级预算 |
| 能力注册中心 | ❌ 不需要 | `Capabilities()` 是构造时确定的静态属性，不需要注册中心。多后端路由需要的是路由器而非注册表 |

### 3.3 向后兼容性策略

| 变更 | 兼容策略 |
|------|---------|
| `Storage` 接口新增 `Capabilities()` | 追加方法，所有现有实现需要新增；go `interface` 的演化需要，可编译期检查 |
| `Agent` 结构体新增 `Session` 字段 | 新增可选字段，旧调用方传 `nil` 后降级为当前行为（无治理） |
| `Compensator` 引入 `Put`/`Delete` | 仅在 service 层内部使用，对外接口不变 |
| `notification_rules` 运行时启用 | 读取已有 `buckets.notification_rules` 列，旧记录自动生效 |

---

## 4. 技术选型

### 4.1 以下情况**不需要**引入新依赖

经过两卷文档的交叉验证，AeroVault 面临的架构缺口**均可以在现有基础设施上解决**，不需要引入：

| 曾被考虑的外部依赖 | 否决理由 |
|------------------|---------|
| Redis/etcd（用于上传 TTL、认证缓存、规则缓存） | `Reconcile` 的 SQL 扫描 + `sync.Map` 惰性过期足够；`uploads` 表有 `created_at` |
| Kafka/RabbitMQ（用于事件路由） | 当前 EventBus + 通知规则匹配可以就地增强；Postgres LISTEN/NOTIFY 已经在使用 |
| Terraform/DbMigrations 工具 | 已有 48 对迁移文件 + 启动自动迁移；不需要额外工具链 |
| OpenFGA/Casbin（用于 Agent 范围策略） | Agent 当前的访问范围模型是简单的"允许桶列表"，CaSbin 的策略引擎对于当前阶段过重 |
| Temporal/Cadence（SAGA 工作流） | 补偿场景是 2-3 步线性路径，引入工作流引擎是过度工程 |

**基本判断：** AeroVault 的核心 Go 依赖栈（stdlib + sqlite + pgx + aws-sdk + qdrant-client）已经覆盖了所有方向所需的基础设施。新增外部依赖应经过严格的"必须性"论证。

### 4.2 两个可能需要引入的轻量级依赖

如果方向 D（多后端组合）进入方案 B 或 C 阶段，以下依赖值得评估：

| 依赖 | 用途 | 替代方案 | 评估 |
|------|------|---------|------|
| `hashicorp/go-multierror` | 补偿事务中多个错误合并 | 手写 `errors.Join`（Go 1.20+） | 不需要——`errors.Join` 已内置 |
| `cenkalti/backoff/v4` | 补偿重试（后端 DELETE 失败时重试） | 手写指数退避 | **可能有用**——重复使用现有库即可，非核心依赖 |

### 4.3 自建 vs 采购的决策矩阵

对于方向相关的组件决策：

| 组件 | 选项 | 建议 | 理由 |
|------|------|------|------|
| 补偿框架 | 自建 / 采购（Temporal） | **自建** | Temporal 是分布式工作流引擎，AeroVault 的补偿场景是本地 2-3 步线性路径，引入 Temporal 是过度工程 |
| 策略引擎（Agent） | 自建 / OpenFGA | **自建** | Agent 范围治理的语义空间有限（桶列表 + 前缀），策略引擎的通用模型（Type/Relation/Tuple）对于当前用例而言过于抽象 |
| 通知路由 | 自建 / Kafka | **自建** | 当前事件量级和规则复杂度不需要消息队列；Postgres LISTEN/NOTIFY + 内存规则缓存足够 |
| 模型漂移监控 | 自建 / MLflow | **自建** | 监控需求局限在嵌入覆盖率和重索引进度两个指标，不需要完整的 ML 实验跟踪系统 |

**决策原则总结：** 只用现有基础设施能解决的问题，一律不引入新系统。只有在新方向引入了**根本不同的计算模型**（如流处理、分布式协调）时才考虑外部系统。

---

## 5. 实施路线图

### 5.1 优先级总排序

基于**数据完整性 > 安全 > 存储效率 > 产品完整性 > 运维自动化**的优先级原则：

| 优先级 | 方向 | 代码包 | 预估工作量 | 依赖 |
|--------|------|--------|-----------|------|
| **P0** | 写入路径补偿事务 | `internal/service/` + `internal/reconcile/` | 中（3-5 天） | 无 |
| **P0** | Agent 沙箱治理 | `internal/ai/` | 小（2-3 天） | 无 |
| **P1** | 分片上传生命周期治理 | `internal/storage/` + `internal/service/` + `internal/reconcile/` | 小～中（2-4 天） | 补偿事务框架（可选复用） |
| **P1** | 存储后端能力契约（方案 A） | `internal/storage/` | 小（1-2 天） | 无 |
| **P1** | 流式路径内存压力管理 | `internal/service/` + `internal/api/` | 中（2-3 天） | 无 |
| **P2** | 桶通知运行时路由 | `internal/events/` | 中（2-3 天） | 无 |
| **P2** | DB 驱动特性不对称优雅降级 | `internal/repository/` | 小（1-2 天） | 无（增量修复） |
| **P2** | 认证凭据生命周期管理 | `internal/auth/` | 中（2-3 天） | 无 |
| **P2** | 向量模型漂移检测与修复 | `internal/ai/` + `internal/telemetry/` | 中（3-4 天） | 无 |

### 5.2 阶段划分

```
Phase 1（当前迭代 — 2 周）
├── P0: 写入路径补偿事务框架（Compensator）
│   ├── 创建 compensator.go
│   ├── 注入 Put 路径（store.Put → repo.UpsertObject gap）
│   ├── 注入 hardDelete 路径（store.Delete → repo.HardDeleteObject gap）
│   ├── 注入 CompleteMultipart 路径
│   └── 新增 Prometheus 指标 + warn log 兜底
│
├── P0: Agent 沙箱治理
│   ├── 创建 AgentSession / ToolBudget / ToolScope
│   ├── dispatchTool 注入 session 参数 + 配额校验
│   ├── callReadFile 添加敏感内容检查钩子（复用 PIIDetector）
│   └── 工具调用写入 audit_log
│
└── P1: 分片上传治理（最小版本）
    ├── UploadPart size 校验（minPartSize, maxParts）
    ├── uploads 表 expires_at 列
    └── Reconcile 新增 ListExpiredUploads + auto AbortMultipart

Phase 2（下一迭代 — 2 周）
├── P1: 存储后端能力契约（方案 A 仅 Capabilities()）
│   ├── Storage 接口 + Capabilities 类型定义
│   ├── 四后端实现（local/s3/oss/cos）
│   └── FileService 全局预校验钩子
│
├── P1: 流式路径内存压力管理
│   ├── Put handler 包装 MaxBytesReader
│   ├── Get → MCP 路径添加 MemoryBoundedReader
│   ├── CopyObject 路径添加 spool-to-temp 降级
│   └── GetRange 跳过时使用 seekable 优化而非 io.CopyN(discard)
│
└── P2: 桶通知运行时路由
    ├── notifications.MatchRules 纯函数
    ├── Bus.Publish 嵌入规则匹配
    └── sync.Map + 30s TTL 规则缓存

Phase 3（远期 — 灵活排期）
├── P2: DB 驱动特性不对称降级（增量修复每个已有问题）
├── P2: 认证凭据生命周期管理
├── P2: 向量模型漂移自动检测 + 水印续传
└── 根据 v87 StorageClass 分层决策 → 决定是否扩展方向 D 到方案 B/C
```

### 5.3 关键里程碑

| 里程碑 | 验收条件 | 预计时间 |
|--------|---------|---------|
| **M1：数据完整性基座** | `make check` 包含 `compensator_test.go`；`Put`/`Delete`/`CompleteMultipart` 三条路径均在模拟故障后产生补偿记录 | Phase 1 结束 |
| **M2：Agent 安全可审计** | 渗透测试验证：prompt injection 无法访问未授权 bucket；所有 Agent 工具调用均可在 `audit_log` 中追溯 | Phase 1 结束 |
| **M3：分片上传零孤儿** | 24h 内未完成的上传被自动 abort；`UploadPart` 对 <5MB 的 part 返回 400（最后一片除外） | Phase 1 结束 |
| **M4：能力感知** | `s3.PresignGet` 不可用时系统自动降级到 `Get` + 代理解耦，不再返回 `"not implemented"` | Phase 2 结束 |
| **M5：通知可路由** | `SetBucketNotifications` 配置后，`Bus.Publish` 按规则过滤事件目标；SSE 流只接收匹配规则的事件 | Phase 2 结束 |

### 5.4 风险矩阵

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| **补偿操作在并发写入时误删对方数据** | 中 | 🔴 严重 | 补偿前检查 `updated_at` 或使用 object version ID 作为乐观锁 |
| **Agent 配额在并发请求下计算不准确（race）** | 高 | 🟠 中 | 配额状态关联到 `(tenant, sessionID)`，使用原子操作或 request-scoped budget |
| **分片上传 TTL 到期 + 最后一片同时到达** | 低 | 🟠 中 | `CompleteMultipart` 前检查 `expires_at`；过期返回 400 而非静默接受 |
| **能力契约方案 B/C 的架构依赖（StorageClass 分层优先级）** | 中 | 🟠 中 | 方向 D 的方案 A（仅 Capabilities()）不依赖任何外部决策，可独立交付 |
| **12 个方向同时在多个分支上开发导致合并冲突** | 高 | 🟠 中 | 按阶段分批次，Phase 1 的三个方向代码路径不重叠（`internal/service/` vs `internal/ai/` vs `internal/storage/`） |

### 5.5 建议规避的陷阱

| 陷阱 | 说明 |
|------|------|
| **补偿事务与业务逻辑耦合** | 补偿逻辑不应该侵入 `Put` 的正常 return 路径。补偿器应该是"注册-清除"模式，非"try-catch"模式 |
| **Agent 治理统一到 MCP 安全模型** | 两个威胁模型不同，强行统一会导致 Agent 安全策略过松或 MCP 用户体验过紧 |
| **过早引入组合式多后端路由** | 在方向 D 方案 A 尚未落地、无实际多后端使用场景之前，不要实现 StorageClass 路由层 |
| **通知规则匹配的性能优化过早** | 先实现功能正确，再关心缓存/索引优化。当前阶段事件吞吐量远未到规则匹配成为瓶颈的水平 |

---

## 综合评审总结

AeroVault 的架构处在"单体应用已具备清晰分层、可插拔持久化、事件驱动骨架"的成熟期，正在向"企业级安全、组合式存储后端、自治运维"的方向演进。两卷文档揭示的 10 个方向中，**有 2 个 P0（数据完整性补偿 + Agent 安全治理）、3 个 P1（分片上传、能力契约、流式内存压力）、5 个 P2（通知路由、DB 对称性、凭据生命周期、模型漂移、以及未展开的批量操作框架）**。

最令我关注的不是单个方向的缺失，而是**项目缺乏一个系统的"架构演化方法论"**——当前 137 轮分析都是独立的深度扫描，但没有在架构层面将这些方向组织成依赖图和演化阶段。建议在完成本期 Phase 1（补偿事务 + Agent 治理 + 分片上传）后，暂停新的 expansion 分析，花一次迭代将全部 137 份分析文档归类到 3-5 个"架构支柱"下，生成一份 [ARCHITECTURE_EVOLUTION.md](./ARCHITECTURE_EVOLUTION.md) 路线图文档。这份文档应该回答：

1. 这 137 个方向哪些是**线性依赖**（必须先做 A 再做 B），哪些是**独立**（可并行）？
2. 哪些方向在**下一个大版本发布前必须完成**？（当前 P0）
3. 哪些方向依赖外部依赖的决策（如 StorageClass 分层决定方向 D 的范围）？
4. **停止扩张边界**的标准是什么？——或者说，何时说"这个方向很好但不是现在"？

这个元层面（meta-level）的架构治理，可能比任何一个单独的扩展方向都更有长期价值。
