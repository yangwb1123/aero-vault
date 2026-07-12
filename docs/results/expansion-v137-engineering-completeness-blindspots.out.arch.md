好的，我已经完整阅读了这份 680 行的分析文档。下面从架构师视角进行深度分析。

---

# AeroVault 架构分析与扩展建议

## 1. 架构评估

### 1.1 当前架构的核心优势

| 维度 | 评价 | 依据 |
|------|------|------|
| **分层清晰度** | ★★★★★ | Protocol Adapters → FileService → Storage/Repository 的三层分离非常干净，handler 不包含业务逻辑，service 不感知协议细节 |
| **可扩展性** | ★★★★☆ | Storage backend 通过接口可插拔，Repository 通过 `s.rebind` 抽象 SQL 方言差异，迁移双文件机制保障跨 DB 演进 |
| **事件驱动解耦** | ★★★★☆ | EventBus 将 Indexer、Webhook、Replication 等异步消费者与主路径解耦，核心 CRUD 不依赖 AI 组件 |
| **Opt-in 安全默认** | ★★★★★ | AI、pgvector、Qdrant、WebDAV 全部 flag-gated，`nil` 安全设计确保基线路径无回归 |

### 1.2 架构局限性（按严重程度排序）

**问题 1：存储层与元数据层之间无事务边界（架构级裂痕）**
这是方向五的根本原因。当前架构选择了"存储 blob 先、元数据写后"的朴素顺序，两者之间没有任何分布式事务、Saga 或补偿机制。这不是代码 bug，而是架构层面的**缺乏一致性协调器**。

```
Put 路径:  store.Put → (无事务) → repo.UpsertObject
Delete 路径: store.Delete → (无事务) → repo.HardDeleteObject
```

当 `store.Put` 使用 S3/OSS/COS 远程存储后端时，网络延迟和部分失败放大了这个窗口。这是**核心架构债务**。

**问题 2：EventBus 的不可靠传递被用于关键数据路径（设计决策权衡失衡）**
方向一的根因。`bus.go:100-103` 的 `select { case ch <- e: default: }` 是一个**性能优先于可靠性的显式设计选择**。但对于 Indexer 这样的关键消费者（chunk 清理依赖事件到达），这个选择变成了数据一致性的直接威胁。

```
权衡：非阻塞广播 ≈ 零等待 × 数据可能永久丢失
```
在事件驱动的系统里，"事件丢失后 Indexer 永不知道"意味着系统没有**最终一致性修复路径**。这超出了"可以接受"的权衡范围。

**问题 3：领域模型缺少版本化/乐观锁原语**
方向五中裂痕 B 和 C 的根因。`Object` 结构体没有 `Version` 或 `UpdatedAt` 字段供 CAS 比较。`UpsertObject` 使用朴素的 `INSERT ... ON CONFLICT DO UPDATE`，最后写入者获胜。这对于多租户生产系统是不够的。

**问题 4：Chunk 生命周期是对象生命周期的二等公民**
方向一的根本原因。Chunk 的创建是事件驱动的（Indexer 监听 `object.created`），但 chunk 的删除只在一个地方（`hardDeleteObject`) 被顺带处理。这意味着：
- 软删除 → chunk 残留（依赖事件，事件可能丢）
- 保留期清除 → chunk 残留（代码根本没写）
- 桶级联删除 → chunk 全部孤儿
- Tenant 删除 → chunk 全部孤儿

Chunk 没有被纳入 **正式的对象生命周期状态机**。

**问题 5：协议层验证形同虚设（方向二）**
S3 CompleteMultipartUpload 的 handler 解析了 XML 却将结果送入空结构体被 GC。这不是一个边缘案例，而是**协议合规性的系统性盲区**——缺乏对 S3 协议规范的逐条核对机制。同样的风险可能存在于其他 S3 子资源处理中。

### 1.3 架构债务总结

| 债务类型 | 严重程度 | 估计偿还成本 | 如果不偿还的后果 |
|---------|---------|-------------|----------------|
| 存储层/元数据层无事务协调 | **P0 架构债务** | 中等（Saga 模式约 500 行框架代码） | 并发下的数据丢失/phantom 对象 |
| EventBus 不可靠传递用于关键路径 | **P1 设计债务** | 低（持久化 + 重试机制约 300 行） | 事件丢失导致的数据永久不一致 |
| 领域模型无版本号 | **P1 模型债务** | 低（加一列 + CAS 逻辑约 100 行） | 并发覆盖导致的数据丢失 |
| Chunk 生命周期未建模 | **P2 模型债务** | 中（reconcile 循环约 150 行） | 搜索结果污染、向量存储成本浪费 |
| 协议合规性缺少验证框架 | **P2 流程债务** | 中（属性测试套件 + 协议核对清单） | 类似的协议 bug 会再次出现 |

---

## 2. 扩展方向

### 方向 A：事件驱动可靠性基础架构（Eventing Reliability Foundation）

**优先级：P0** | **为什么需要**

EventBus 不可靠传递是方向一、方向五的共性根因。当前架构中，EventBus 承载了 Webhook（业务关键）、Indexer（数据一致性关键）、Replication（持久性关键）多个角色的消息传递。任何一个消费者丢消息都有不可逆影响。从根本上，需要将 EventBus 升格为有持久化保证的基础设施。

**核心挑战：**
- 非阻塞语义（`select/default`）不能简单改为阻塞，否则会反向阻塞主 CRUD 路径
- 需要在"不阻塞生产者"和"不丢失消息"之间找到平衡
- 不同消费者对可靠性的需求不同（Webhook 需要 exactly-once 语义，Indexer 至少需要 at-least-once）
- 与当前无外部依赖的基线模式（SQLite + local FS）保持兼容

**预期的架构变更：**

```
当前:                          建议:
┌─────────┐                    ┌──────────────────┐
│ EventBus │──(channel)──→│ 消费者          │    │  EventLog      │──(持久化)→│  Dispatcher     │
│ (内存)   │                    │  (内存)           │    │  (outbox 表)    │          │  (ack-based)    │
└─────────┘                    └──────────────────┘    └────────────────┘          └────────────────┘
                                                           │                            │
                                                           │   ┌──────────────────┐   │    ┌──────────────┐
                                                           └──→│  EventStore      │   │    │  Dead Letter  │
                                                               │  (Repository)    │   │    │  Queue        │
                                                               └──────────────────┘   │    └──────────────┘
                                                                                      │    ┌──────────────────┐
                                                                                      └──→│  Consumer Group   │
                                                                                          │  (with retry)     │
                                                                                          └──────────────────┘
```

| 组件 | 职责 | 变更量 |
|------|------|-------|
| `EventStore` interface | 持久化事件到 outbox 表，支持 ACK/重放 | 新接口 + Repository 实现 ~200 行 |
| `Dispatcher` | 从 outbox 拉取事件，分发给消费者，管理 ACK/NACK | 新组件 ~250 行 |
| `ConsumerGroup` | 消费者注册 + 重试策略 + 死信队列 | 新组件 ~150 行 |
| 向后兼容模式 | `STORAGE_BACKEND=local` 时退化为当前内存模式 | ~30 行条件判断 |

**对现有系统的影响：**
- 启动时新增 EventStore 初始化（无额外配置，复用 Repository 的连接池）
- 现有 `EventBus` 接口保留但内部实现切换到 `EventLog → Dispatcher`
- 消费者接口从 `chan Event` 改为 `func(ctx, Event) error`（支持 ACK/NACK）
- 迁移双文件：新增 `events_outbox` 和 `dead_letter` 表

**技术选型建议：**

| 选项 | 优势 | 劣势 | 推荐 |
|------|------|------|------|
| **Postgres outbox + LISTEN/NOTIFY** | 零额外依赖，复用现有连接池，SQL 事务保证 | 需要 Postgres，SQLite 不支持 NOTIFY | ✅ **主推荐** |
| SQLite outbox + 轮询 | 零依赖，保持基线 | 轮询延迟，SQLite 写竞争 | 基线模式 |
| NATS JetStream | 高性能，内置持久化/重试 | 新增外部依赖，违反 AGENTS.md I6 | 仅重度部署推荐 |

---

### 方向 B：对象生命周期统一建模（Unified Object Lifecycle Model）

**优先级：P1** | **为什么需要**

方向一、三、五都指向同一个根因——对象的不同"侧面"（blob、metadata、chunks、versions、locks）没有被统一建模为一个**正式状态机**。当前架构中，这些侧面由不同的组件独立管理，没有同步协调机制。

```
当前隐含状态机（非正式）：
   Active ──softDelete──→ SoftDeleted ──retention──→ (blob deleted, meta deleted, ❌ chunk orphaned)
     │                                                  ↑
     └──hardDelete──→ (blob deleted, meta deleted, chunk deleted) ✅ 唯一正确路径

建议显式状态机：
   Active ──softDelete──→ SoftDeleted ──retentionExpired──→ Purging ──purgeComplete──→ Gone
     │                       │                                  │
     │                       └──restore──→ Active               ├──chunkCleanup
     │                                                          ├──blobCleanup
     └──hardDelete──→ Purging ────────────────────────→         └──metaCleanup
```

**核心挑战：**
- 状态机需要向后兼容（现有数据没有 `state` 字段）
- 需要区分"正在删除中"（`Purging`）和"已删除"（`Gone`）两种状态，以支持中断恢复
- 补偿逻辑（方向五裂痕 D）需要有明确的状态入口
- 需要支持 bucket 级别的生命周期策略（TTL-based state transitions）

**预期的架构变更：**

| 变更 | 说明 | 估算 |
|------|------|------|
| `Object` 增加 `State` 枚举字段 | `active / soft_deleted / purging / gone` | migration + model 变更 ~50 行 |
| 状态转换方法 | `TransitionTo(ctx, fromState, toState) error`（CAS 语义） | service 层 ~80 行 |
| `PurgeOrchestrator` | 协调 chunk/blood/meta 三步清理，支持部分失败恢复 | 新组件 ~200 行 |
| Reconcile 增强 | 发现 `purging` 状态的孤儿，执行补偿 | ~80 行 |
| 向后兼容 | 现有 `deleted_at` 非 null 的行映射为 `soft_deleted` | ~20 行 |

**对现有系统的影响：**
- `FileService.Put` 和 `Delete` 需要增加状态检查（`purging` 状态下拒绝写入）
- `UpsertObject` 需要 CAS 语义（与方向五 Phase 1 合并）
- `RetentionJob` 使用新的状态机路径替代手动 blob/meta 删除
- Web UI 管理面板可以展示对象状态分布（方向三 Phase 1）

---

### 方向 C：协议合规性保障层（Protocol Compliance Gateway）

**优先级：P1** | **为什么需要**

方向二暴露了 S3 兼容协议实现中的一个系统性风险：**协议 handler 的实现正确性完全依赖人工 review**，没有任何自动化验证。aero-vault 宣称兼容 S3，但没有一个 S3 协议合规性测试套件。类似的 bug（解析结果未使用、响应格式错误、子资源缺失）很可能存在于其他 handler 中。

**核心挑战：**
- AWS S3 协议规范是一个 4000+ 页的文档，完整覆盖不现实
- S3 兼容性 SDK（如 `aws-sdk-go`）的行为不是规范——存在版本差异性
- 需要区分"必须兼容"（GetObject/PutObject/DeleteObject 的核心语义）和"可有可无"（S3 Select/对象锁等高级特性）
- 测试需要在零网络（CI gate）环境下可运行

**预期的架构变更：**

| 变更 | 说明 |
|------|------|
| **属性测试框架** | 对核心 S3 操作使用 `testing/quick` 或 `rapid` 库生成随机输入，验证 handler 不会 panic/返回 5xx |
| **请求/响应 schemas** | 为每个 S3 handler 声明请求 XML schema 和响应 XML schema，启动时自动验证 |
| **协议 compliance 矩阵** | 在 `docs/` 中声明支持的 S3 特性列表，每项对应集成测试 |
| **S3 SDK 集成测试** | 使用 AWS Go SDK v2 向本地实例发送请求，验证行为与 AWS 一致 |

**技术挑战：**
- S3 协议的 XML 响应格式中，不同操作使用不同的命名空间和结构，schema 验证不简单
- 部分 S3 特性（如对象锁、legal hold）有复杂的交互逻辑，测试覆盖成本高
- 无法用开源工具（如 `s3tests`）因为那些需要 Ceph RGW 的特定扩展

**对现有系统的影响：**
- 不修改任何生产代码——纯测试/验证层
- `internal/api/s3compat` 的新 handler 必须先通过 schema 验证才能合入
- CI gate 增加 `make test-s3-compat`（可选步骤，非阻塞）

---

### 方向 D：操作可观测性与管理面统一（Observability-First Operations Platform）

**优先级：P2** | **为什么需要**

方向三揭示了 15+ 管理功能仅 CLI/curl 可达的问题。但更深层的问题是：**系统的可观测性数据（metrics、audit log、job status）与管理操作分离在不同的访问路径中**。运营人员无法在一个视图中完成"发现问题→定位根因→执行操作"的闭环。

**核心挑战：**
- 不引入重量级的前端框架（保持当前零依赖的静态 SPA 模式）
- 管理 API 有 scope 权限控制，UI 需要正确传递 token
- 多租户切换需要在所有面板中一致生效
- 大量桶/对象的场景需要虚拟滚动或分页

**预期的架构变更：**

| 变更 | 说明 |
|------|------|
| **API 代理层** | 在 `webui/web.go` 新增 `/ui/api/*` 代理，在浏览器中解决 CORS + token 管理 | ~100 行 Go |
| **管理标签页架构** | 使用 Web Components 或纯函数式 JS 组件，避免框架依赖 | ~400 行 JS |
| **可观测性面板** | 从 Prometheus API（如果配置）直接拉取数据渲染 | ~200 行 JS |
| **WebSocket 状态推送** | 为 Job 状态/审计日志提供实时推送（可选增强） | ~150 行 Go + JS |

**对现有系统的影响：**
- `webui/web.go` 从纯静态文件服务变为带 API 代理的 HTTP handler
- 无后端数据模型变更——只消费现有 admin API
- 权限控制：UI 不做额外校验——依赖 API 返回 403 时展示错误

---

### 方向 E：存储后端一致性 Saga 框架（Storage-Repository Saga Framework）

**优先级：P1** | **为什么需要**

方向五中的 4 种不一致裂痕本质上是同一个架构问题的不同表现：**跨两个持久化层（Store + Repository）的操作缺乏事务协调**。需要一个 saga 框架来封装"存储 blob + 写入 metadata"为可补偿的分布式事务。

**当前朴素模式：**
```
doStoreOperation()           // 可能成功
doRepoOperation()            // 可能失败
// 无失败处理
```

**建议 Saga 模式：**
```
tx := saga.New()
tx.AddStep(
    func() { return store.Put(ctx, ...) },     // 正向操作
    func() { return store.Delete(ctx, ...) },   // 补偿操作
)
tx.AddStep(
    func() { return repo.UpsertObject(ctx, ...) },
    func() { return repo.DeleteObject(ctx, ...) },
)
return tx.Execute(ctx)  // 任一失败 → 逆序执行所有补偿
```

**核心挑战：**
- 补偿操作必须是幂等的（补偿的补偿不能出错）
- 补偿操作自身也可能失败——需要重试极限
- Saga 框架增加代码复杂度，需要清晰的接口设计
- 部分存储后端（如 S3）的 `store.Put` 补偿（`store.Delete`）本身可能因权限/网络失败
- 需要与现有 `FileService` 的方法签名兼容

**预期的架构变更：**

| 变更 | 说明 | 估算 |
|------|------|------|
| `saga` 包 | 轻量级 saga 编排器，无外部依赖 | ~120 行 |
| `CompensableStore` 接口 | `Put → RollbackPut`，`Delete → RollbackDelete` | ~30 行接口定义 |
| `FileService` 使用 saga | Put/Delete/MultipartComplete 等方法改为 saga 模式 | ~150 行变更 |
| 错误报告 | Saga 失败时记录补偿状态到 `saga_log` 表供管理员审查 | ~80 行 |

**对现有系统的影响：**
- `FileService` 的核心方法签名不变——saga 是内部实现细节
- 部分存储后端（如 S3）的补偿操作可能因 bucket 权限失败——需要在文档中说明
- Saga 失败不会导致数据不一致（最坏情况是孤儿blob，ReconcileJob 可清理）
- 与方向 B 的对象状态机结合：`purging` 状态可被 saga 使用

---

## 3. 接口设计建议

### 3.1 核心设计原则

| 原则 | 说明 | 为什么重要 |
|------|------|-----------|
| **补偿优先于预防** | 接口应提供正向操作和对应的补偿操作 | 跨层事务无法用 Postgres 的 2PC，补偿是唯一可靠的恢复路径 |
| **ACK 语义的消费者** | 所有重要事件的消费者接口从 `chan Event` 改为 `func(ctx, Event) (ack bool)` | 区分"处理成功"和"处理失败但需要重试" |
| **CAS 优先于悲观锁** | 所有写操作接口应包含版本号或条件参数 | 分布式系统中悲观锁不可伸缩 |
| **幂等是接口契约的一部分** | 每个写操作接口的文档必须明确幂等性保证 | 重试是分布式系统的默认行为 |
| **控制面与数据面分离** | 管理 API（admin scope）与数据 API 在接口层次上明确分离 | 安全审计、权限模型、版本演进都更容易 |

### 3.2 推荐的接口变更

**Repository 层：**

```go
// 当前（问题：无版本控制）
type Repository interface {
    UpsertObject(ctx, obj) (Object, error)
    HardDeleteObject(ctx, tenant, bucket, key) error
}

// 建议（增加版本化访问）
type Repository interface {
    // 读
    GetObject(ctx, tenant, bucket, key, version?) (Object, error)
    
    // 写带 CAS
    UpsertObject(ctx, obj, expectedVersion?) (Object, error)  // CAS when version != nil
    
    // 删除走状态机
    SoftDeleteObject(ctx, tenant, bucket, key) error          // 状态 → soft_deleted
    HardDeleteObject(ctx, tenant, bucket, key) error          // 状态 → gone（需先 soft）
    
    // 状态机辅助
    ListObjectsByState(ctx, state, before, limit) ([]Object, error)
    TransitionObjectState(ctx, id, fromState, toState) error
}
```

**Eventing 层：**

```go
// 当前（问题：无 ACK、无重试）
type EventBus interface {
    Publish(ctx, Event)
    Subscribe(cap int) chan Event
}

// 建议
type EventBus interface {
    Publish(ctx, Event) error                    // 持久化写入
}

type EventConsumer interface {
    Handle(ctx, Event) (ack bool, err error)     // ACK/NACK + 死信
}

type Dispatcher interface {
    Register(topic string, consumer EventConsumer, opts ConsumerOptions)
    // opts: MaxRetries, BackoffStrategy, DeadLetterQueue
    Start(ctx) error
    Shutdown(ctx) error
}
```

**Storage 层（Compensable）：**

```go
// 新增
type CompensableStorage interface {
    Storage
    DeleteOnFailure(ctx, key string) func() error  // 返回补偿操作
}

// 或者更简洁：saga.Step 直接包装
type SagaStep struct {
    Forward  func(ctx) (result any, err error)
    Compensate func(ctx) error
}
```

### 3.3 向后兼容性策略

| 变更 | 兼容性策略 |
|------|-----------|
| EventBus 接口变更 | 保留旧接口（标记 Deprecated），新消费者使用新接口。过渡期两个接口共存。 |
| Object 增加 Version 字段 | 现有 row 的 version 默认为 0。CAS 检查时若 oldVersion=0 则跳过。迁移后所有新写入使用 version。 |
| Object 增加 State 字段 | 现有 row 的 state 默认为 `active`。`deleted_at` 非 null 的迁移为 `soft_deleted`。 |
| Repository 新方法 | 纯新增，不修改现有方法签名。旧代码不受影响。 |
| Saga 框架 | 纯新增包，`FileService` 增量改造。不改造的方法保持现有行为。 |

---

## 4. 技术选型

### 4.1 需要引入的技术栈评估

| 候选 | 适用方向 | 评估 | 建议 |
|------|---------|------|------|
| **属性测试库（`rapid`/`testing/quick`）** | 方向 C | 零运行时依赖，纯测试增强，与现有 Go 测试框架兼容。`rapid` 比标准库 `testing/quick` 更强大（状态测试支持）。 | ✅ **强烈推荐**（符合 I6：测试依赖） |
| **SQL 查询构建器（`squirrel`/`goqu`）** | 方向 A、E | 减少 SQL 拼接错误，支持不同方言。但当前 `s.rebind` 方案已经够用，新依赖违反 I6。 | ❌ **不推荐**（当前方案充分） |
| **Saga 框架（`go-saga`/自建）** | 方向 E | 现有 Go 系 saga 框架偏重微服务场景（HTTP/gRPC），不适合同进程内的两层补偿。自建轻量级约 120 行。 | ✅ **自建推荐**（无合适第三方） |
| **Web Component 框架（Lit/Alpine.js）** | 方向 D | 当前 UI 零 JS 框架依赖，引入会增加构建步骤和体积。纯函数式 JS（模块模式）已证明满足 4-tab 需求。 | ❌ **不推荐**（保持零依赖） |
| **事件存储（NATS/Kafka）** | 方向 A | 高性能、持久化、重试/死信内置。但引入外部依赖违反软件包原则（I6），且增加部署复杂度。 | ⚠️ **可选**（仅重度部署） |
| **OpenAPI 代码生成（oapi-codegen）** | 方向 C | 从 `openapi.json` 生成 handler 接口和请求/响应类型，消除类型不匹配 bug。但需要 `openapi.json` 持续同步。 | ✅ **推荐**（验证阶段引入） |

### 4.2 自建 vs 采购决策矩阵

| 功能 | 自建成本 | 采购/引入成本 | 决策 |
|------|---------|-------------|------|
| Saga 编排器 | ~120 行（低） | 无合适的 Go 库（go-saga 偏微服务，需改造） | **自建** |
| 事件持久化 | ~300 行（低） | NATS 集群运维成本高 | **自建 outbox 模式** |
| 属性测试 | ~50 行 + 测试（低） | 标准库已有，rapid 为纯测试库 | **引入 rapid** |
| 管理 UI | ~700 行 JS（中） | 无现成方案（需对接 admin API） | **自建** |
| S3 协议 schema 验证 | ~200 行（中） | 无现成 Go 库对应 S3 XML schema | **自建** |

### 4.3 关键技术选择论证

**事件持久化方案选择（方向 A）：**

| 维度 | Postgres outbox + LISTEN/NOTIFY | SQLite outbox + 轮询 | NATS JetStream |
|------|--------------------------------|---------------------|---------------|
| 外部依赖 | Postgres（已有） | 无（基线） | 新增（NATS 集群） |
| 实时性 | 即时（NOTIFY） | 轮询间隔（1-5s） | 即时 |
| 吞吐 | ~1000 msg/s | ~100 msg/s | ~100000 msg/s |
| 持久化保障 | WAL + 同步复制 | WAL | RAFT 复制 |
| 运维复杂度 | 低（已有连接池） | 零 | 高（集群管理） |
| **基线兼容** | ❌（SQLite 模式不可用） | ✅ | ❌ |
| **推荐场景** | Postgres 部署 | local/SQLite 部署 | 企业级大规模部署 |

**结论：** 接口设计为可插拔模式，默认选 Postgres outbox，SQLite 退化为轮询模式。

**版本号字段选型（方向五 Phase 1）：**

| 方案 | 实现成本 | 并发安全 | 冲突处理 |
|------|---------|---------|---------|
| **整数 Version（单调递增）** | ~100 行（+1 列 + 迁移 + CAS 重试） | ✅ UPDATE ... WHERE version = old | 客户端重试 |
| UUID Version | ~120 行（+16 字节列 + 生成 + 比较） | ✅ 同上 | 同 |
| `updated_at` 时间戳比较 | ~50 行（无需新列） | ⚠️ 时钟同步问题 | 精度问题 |
| **推荐：整数 Version** | 简洁、低开销、可读性强、时钟无关 | ✅ | 重试循环 3 次 |

---

## 5. 实施路线图

### 5.1 总体优先级排序

```
P0（立即 — 数据完整性风险）
  ├── 方向二 Phase 1: ETag 交叉验证 (CompleteMultipartUpload)
  └── 方向一 Phase 1: Retention GC + ChunkCleaner

P1（高优先级 — 一致性保障）
  ├── 方向一 Phase 2: 软删除路径同步 ChunkCleaner
  ├── 方向五 Phase 1: Object 版本号 + CAS
  ├── 方向五 Phase 2: 硬删除路径幂等重试
  └── 方向 A: 事件持久化基础架构

P2（产品完整性）
  ├── 方向三 Phase 1: Web UI 只读管理面板
  ├── 方向一 Phase 3: Chunk 与对象的 reconcile 循环
  ├── 方向 C: 协议合规性测试框架
  ├── 方向 E: Saga 框架（可选增强）
  ├── 方向三 Phase 2: Web UI 可写管理操作
  └── 方向四 Phase 1: CSV Select（差异化能力）

P3（长期）
  ├── 方向三 Phase 3: 对象生命周期管理 UI
  ├── 方向四 Phase 2: JSON Select
  ├── 方向 B: 统一对象生命周期状态机
  └── 方向 D: 可观测性管理面板
```

### 5.2 阶段划分

**阶段 1 —"止血"（Sprint 1-2, ~2 周）**

| 目标 | 修复方向二和数据路径中最直接的数据完整性漏洞 |
|------|------------------------------------------|
| 交付物 | ① CompleteMultipartUpload ETag 交叉验证；② RetentionJob 调用 ChunkCleaner；③ 软删除路径同步 ChunkCleaner 兜底 |
| 代码量 | ~90 行（方向二 50 行 + 方向一 30 行 + 10 行） |
| 风险 | 极低——纯新增验证逻辑和调用点，不修改现有行为 |
| 验证 | 现有测试绿 + 新增 3-5 个单元测试用例 |

**阶段 2 —"地基"（Sprint 3-5, ~3 周）**

| 目标 | 解决架构级的一致性裂痕 |
|------|----------------------|
| 交付物 | ① Object 增加 Version 列 + CAS Upsert（方向五 Phase 1）；② 硬删除幂等重试（方向五 Phase 2）；③ 事件持久化 outbox 模式（方向 A） |
| 代码量 | ~450 行（100 + 50 + 300） |
| 风险 | 中——Version 字段是 schema 变更，需要迁移双文件；事件持久化涉及消费者接口变更 |
| 缓解 | 版本号 backward-compat（旧行 version=0 跳过 CAS）；事件接口新旧并存 |

**阶段 3 —"可见"（Sprint 6-7, ~2 周）**

| 目标 | 弥补产品体验盲区 |
|------|---------------|
| 交付物 | ① Web UI 管理标签页（只读监控面板：存储统计、租户列表、Job 状态、审计日志流）；② Chunk 与对象的 reconcile 循环（方向一 Phase 3） |
| 代码量 | ~500 行（300 + 150 + telemetry 指标） |
| 风险 | 低——纯新增功能，不修改现有行为 |
| 验证 | 人工验收 + 单元测试 |

**阶段 4 —"强化"（Sprint 8-10, ~3 周）**

| 目标 | 构建长期质量保障体系 |
|------|-------------------|
| 交付物 | ① 协议合规性测试框架（属性测试 + schema 验证）；② Saga 框架（可选）；③ Web UI 可写操作（桶管理 + API Key 管理） |
| 代码量 | ~550 行（200 + 120 + 230） |
| 风险 | 中——Saga 框架涉及 FileService 核心路径改道 |
| 缓解 | 增量改造：先改 Put 路径验证 Saga 稳定性后再改 Delete |

### 5.3 风险矩阵与缓解策略

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|---------|
| Object Version 列迁移导致锁表（大型部署） | 低 | 高 | 使用 `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` 在线 DDL；Postgres 下无锁；SQLite 下先检查再迁移 |
| 事件持久化 outbox 表增长过快 | 中 | 中 | TTL 自动清理（已 ACK 的事件 24h 后删除）；设置 max rows 告警 |
| Saga 补偿操作也失败 | 中 | 中 | 声明 Saga 补偿是"尽力而为"；最坏情况 blob/metadata 不一致→ReconcileJob 事后清理 |
| Web UI 管理面板暴露未授权操作 | 中 | 高 | UI 不做额外鉴权——依赖 API 返回 403；管理 tab 在非 admin scope 下隐藏（仅前端隐藏，后端兜底） |
| S3 Select 实现与 AWS 行为偏差 | 高 | 中 | 声明为"非标兼容"模式；文档明确列出差异；不承诺 100% AWS 兼容 |
| Phase 2 CAS 冲突导致客户端重试风暴 | 低 | 中 | 重试使用指数退避（base=50ms, max=1s）；3 次重试后返回 `ErrConflict` 让客户端自行处理 |

### 5.4 依赖关系图

```
Phase 1（止血）
  方向二 Phase 1 ─────────────────────────┐
  方向一 Phase 1 ─────────────────────────┤
  方向一 Phase 2 ─────────────────────────┤  （无依赖，可并行）
                                           │
Phase 2（地基）                              │
  方向五 Phase 1 (Version CAS) ────────────┤── 依赖方向一 Phase 1? 否，独立
  方向五 Phase 2 (幂等重试) ────────────────┤── 依赖 Phase 1? 建议放在 Phase 2
  方向 A (事件持久化) ──────────────────────┴── 建议 Phase 2（但不是 Phase 1 的前提）
                                           │
Phase 3（可见）                              │
  方向三 Phase 1 (管理面板) ────────────────┤── 依赖方向 A? 否，独立
  方向一 Phase 3 (reconcile chunk) ────────┤── 最好在 Phase 1 之后
                                           │
Phase 4（强化）                              │
  方向 C (协议合规) ────────────────────────┤── 独立，但建议在所有修复后引入
  方向 E (Saga) ───────────────────────────┤── 依赖方向五 Phase 1（version）
  方向三 Phase 2 (可写操作) ────────────────┤── 依赖方向三 Phase 1
```

**关键路径：** `方向一 Phase 1 → 方向一 Phase 3` 是最短的增值路径。`方向五 Phase 1 → 方向 E` 是架构演进的关键路径。

---

## 总结

这份分析文档揭示了一个有趣的现象：aero-vault 的架构在**宏观分层上是优秀的**（清晰的 adapter/service/storage/repo 分离），但在**微观一致性保障上是薄弱的**（层间无事务、事件无确认、模型无版本）。这 5 个盲区本质上都是同一类问题的不同表现——**"优雅的分层"被"层间协调的缺失"所抵消**。

给出的 5 个扩展方向中，我个人认为**方向 A（事件持久化）** 和 **方向五 Phase 1（版本号 + CAS）** 是最具杠杆效应的投资——它们不直接解决任何一个盲区，但为所有其他方向提供了更坚实的基础。在这两个地基打牢之前，其他修复（如 Saga 框架、Chunk reconcile 循环）都是在沙滩上盖楼。

建议第一阶段集中精力用最少的代码（~90 行）快速止血，第二阶段再做基础架构投资，第三阶段面向用户交付可见价值。
