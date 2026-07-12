以下是对 `expansion-v139-high-value-expansion-directions-v9.md` 的架构分析。

---

# 架构分析报告：AeroVault 高价值扩展方向（第九期）

## 1. 架构评估

### 1.1 当前架构优势

| 优势 | 说明 |
|------|------|
| **干净的分层结构** | Protocol Adapters → FileService → Storage/Repository 的三角模型清晰，Adapter 层薄且可替换 |
| **Opt-in 安全默认** | AI/pgvector/events/cluster 全部 flag-gated，`nil` 安全设计确保 core CRUD 不受扩展影响 |
| **Event Bus 中心辐射** | 事件总线作为扩展中枢，已有 webhook/indexer/AV/replication 四个消费者，扩展新消费者只需实现 `Subscribe` |
| **SigV4 自实现** | 内部 SigV4 引擎避免了 AWS SDK 依赖膨胀，为 SQS/SNS/Lambda 投递创造了零新依赖条件 |
| **迁移双文件制** | SQLite/Postgres 独立迁移路径，schema 变更可追溯可回滚 |

### 1.2 当前架构局限性

以下局限性有一大部分已被本期文档覆盖，我在此基础上补充系统级的观察：

**① Consumer 隔离缺失（架构债，影响 CDC 方向）**
`NextUnconsumedEvents` 被 indexer、webhook、AV、SSE 共享同一张 unconsumed 标记——这是**架构层面的消费组泄漏**。一个消费组的进度标记会静默干扰另一个：

```go
// 当前: 所有消费者共享 unconsumed 标记
// 问题: indexer 标记 consumed=1 后，SSE replayMissed 就丢失事件
// 影响: 无法建立任何有可靠性保证的外部事件流
```

这本质上是一个**游标管理缺失**，本文 CDC 方向正确地识别了它。但作为架构评估，我指出：这不仅是"新功能缺口"，更是**现有功能的可靠性债**——SSE 的 `replayMissed` 功能在多个内部消费者并存时出现概率性丢事件，属于可复现的 bug。

**② 复制方向单一，无环路防护（架构债，影响 Active-Active 方向）**
当前 `ReplicateObjectByID` 是单目标 push，无 `origin_region` 字段。即使实现多目标，复制事件触发目标 region 再次复制就会产生无限循环。这是现有代码的**架构不完整性**——任何多 region 扩展都必须同时解决防循环，无法仅靠增量实现。

**③ 合规信息分散在元数据字段（设计债，影响合规方向）**
`LockedUntil` + `_aero_legal_hold` metadata key + lifecycle rules 分散在 Object 结构体、元数据表、和 lifecycle 配置中。没有统一的保留关联表（retention bindings），导致"对象 A 因什么原因被保留到何时"无法从单一位置回答。合规审计需要的正是一个统一的查询点。

**④ 依赖管理策略待明确**
当前 go.mod 仅有标准库 + 少量必要依赖（SQLite driver、chi router、OTel）。但扩张到 SQS/SNS/Lambda（需 SigV4 签名，已有但需验证对 AWS service 的兼容性）、WASM（需 wazero）时，需要一个明确的依赖准入策略。文档提出的"选项 B"（复用内部 SigV4）是正确的选择，但 WASM 运行时 wazero 是必须引入的新依赖。

### 1.3 关键设计决策合理性

| 决策 | 合理性评估 | 建议 |
|------|-----------|------|
| `DeliveryTarget` 接口 | ✅ 正确。与 event bus 的 `Subscriber` 模式一致，所有投递目标适配同一接口 | 建议接口方法增加 `DeliverBatch(ctx, []Event, rule)` 以支持批量压缩 |
| CDC 游标基于数据库 | ✅ 正确。零新依赖，与现有 tenant/auth 模型集成。但需注意：events 表是 append-only 事务日志，保留策略需要独立实现，不可与业务数据共用相同保留周期 | 建议 events 表使用独立 TTL 配置 `CDC_EVENTS_RETENTION_DAYS` |
| WASM 使用 wazero | ✅ 正确。纯 Go、零 CGO、安全沙箱。没有引入 containerd/Docker 的开销和复杂性 | 需注意 WASM 的 I/O 限制可能使部分函数场景受阻，应提供可选的"原生函数"接口作为 fallback |
| 冲突检测先 LWW 再 Version Vector | ⚠️ 慎用 LWW 作为默认。在 Active-Active 场景下，LWW 是最危险的策略——它静默丢弃数据 | 建议默认实现 Version Vector，LWW 作为兼容选项标记为 deprecated |
| S3 Event Notifications 复用内部 SigV4 | ✅ 正确。已有 SigV4 引擎，无需 AWS SDK | 需验证现有 sigv4.go 对 SQS/SNS/Lambda service 名称和 region 参数的支持 |

### 1.4 架构债务与技术债

| 债务类型 | 位置 | 影响 | 修复时机 |
|---------|------|------|---------|
| **消费组隔离缺失** | `sql_events.go:NextUnconsumedEvents` | SSE replayMissed 概率性丢事件 | 建议随 CDC 方向（方向 #2）一并修复 |
| **复制方向单一 + 无源标识** | `replication.go` | Active-Active 的前提未满足 | 必须在方向 #4 之前或之中解决 |
| **合规信息碎片化** | Object 结构体 + metadata 表 | 合规审计不可查询 | 建议方向 #5 的第一步就是迁移 |
| **Search Cache 范围不明确** | `AI_SEARCH_CACHE_SIZE` | 未知缓存粒度（全查询 vs embedding 级） | 修复成本低，但需先明确 |
| **hardDeleteObject 缺少 legal_hold 检查** | `file_crud.go` | 法律保全对象可能被绕过 | 高优先级安全债务，建议先于合规方向修复 |

---

## 2. 扩展方向

本文档提出的 5 个方向，我按架构视角重新组织和深化分析。

### 方向 A：CDC 事件流与消费组隔离（基于本文 #2，扩展 #1）

**为什么修改推荐排序？** 本文排序 #1 是 S3 Event Notifications。但从架构层面，我认为**CDC 流（#2）应提前到 P0，且在 S3 通知之前实现**。理由：

1. CDC 流直接修复现有 SSE `replayMissed` 的可靠性 bug（架构债务）
2. 消费组隔离是所有事件消费者的基础设施——包括 S3 通知投递本身
3. 成本极低，复用 events 表，收益极高

**核心挑战：**

| 挑战 | 复杂度 | 解法 |
|------|--------|------|
| 从共享 unconsumed 迁移到独立 cursor | 中 | 反向兼容：新增 `consumer_cursors` 表，旧 consumers 仍可读 unconsumed，但新 API 返回 `deprecated` 警告 |
| SSE 连接游标管理 | 低 | `Last-Event-ID` header + 服务端保活游标（cursor TTL 30min + keepalive 刷新） |
| 事件保留策略 | 低 | 新增 config `EVENTS_RETENTION_DAYS`（默认 7），reconcile worker 清理 |
| 背压机制 | 中 | events 表本身不产生内存背压——消费者自己的速度决定了 cursor 位置。生产者写入 events 表仅受数据库限制 |

**预期架构变更：**

```
新增：consumer_cursors 表（迁移 0025）
修改：NextUnconsumedEvents → 每个消费者传入 consumer_name
新增：GET /v1/events?after=&limit= 端点
新增：POST/GET/PUT /v1/events/cursors/{name} 端点
修改：SSE replayMissed → 使用 Last-Event-ID cursor
修改：indexer/webhook/AV 内部消费者 → 使用独立 cursor
```

**对现有系统的影响：** 零。内部消费者迁移前，旧路径继续工作。迁移是逐消费者进行的，可并行。

### 方向 B：S3 Event Notifications 执行引擎（基于本文 #1）

**业务价值：** 这是 S3 兼容性最显眼的"死数据"问题——`notification_rules` 表里的数据只能存储不能执行。对于使用 S3 事件驱动架构的用户，这是第一个会注意到的功能断裂。

**技术难点：**

| 难点 | 分析 |
|------|------|
| **AWS API 签名** | SQS/SNS/Lambda 使用 AWS SigV4，但 service 参数不同（`sqs`/`sns`/`lambda`）。需验证现有 sigv4.go 支持 service-specific 签名。如果不支持，需要在签名器中增加 `Service` 参数 |
| **批量压缩** | 高吞吐场景下，每秒 1000 个 PUT 事件不应导致 1000 次独立的 SQS/Lambda 调用。建议实现批量窗口（batch window = 100ms 或 batch size = 100） |
| **跨账户投递** | SQS/Lambda 在不同 AWS 账户需要 `AssumeRole`，这是一个从"无状态签名"到"STS 会话管理"的能力跨越 |
| **死信策略** | 投递失败（队列不可达、权限不足、限流）需要有指数退避 + 最终死信 |

**预期架构变更：**

```
新增包：internal/events/delivery/
  - delivery.go: DeliveryTarget 接口 + Dispatcher 实现
  - queue.go:    SQS SendMessage（复用 SigV4）
  - topic.go:    SNS Publish（复用 SigV4）
  - lambda.go:   Lambda Invoke（复用 SigV4）
  - batch.go:    批量压缩 + 窗口聚合
修改：events/bus.go 增加 NotificationDispatcher 订阅者
新增：metrics notification_delivery_* 指标
```

**对现有系统的影响：** NotificationRule CRUD 不动，事件总线增加一个订阅者。这是典型的新增不修改。

### 方向 C：多区域 Active-Active 复制与冲突检测（基于本文 #3）

**为什么是 P2（而非本文推荐的 P4）：** 业务连续性 + 数据安全这两个诉求将主动式 multi-region 从"nice to have"推向"must have"。LWW 式的静默覆盖在 replicated 环境下的用户信任损害极大。如果选择支持多区域部署，冲突检测**不可绕过**。

**核心技术难点：**

| 难点 | 复杂度 | 分析 |
|------|--------|------|
| **无限复制循环** | 高 | 事件必须携带 `origin_region` + `replica_of`，消费者检查并跳过非自身 region 的事件和复制事件。这是必须解决的**基础前提** |
| **Version Vector 设计** | 高 | 每个对象维护 `map[region]version`，写入时递增自己的版本。冲突定义：A:3 vs A:2+1（不可比）。需要序列化格式、存储位置（metadata？tags？独立列？） |
| **严格一致性读** | 高 | Active-Active 通常意味着最终一致性。如果需要 Read-After-Write 一致性，需要请求路由到 primary region 或使用 quorum read |
| **删除标记防风暴** | 中 | 软删除标记跨 region 传播时，必须有 `source_region` + `delete_marker_origin`，反向复制跳过 |
| **跨区域版本 ID 唯一性** | 低 | 当前 version_id 可能冲突。改为 `<region_prefix>/<local_uuid>` 格式即可 |

**预期架构变更：**

```
新增：ReplicationRule 配置模型（支持多目标、双向、过滤）
新增：Object 字段 ReplicaVersions map[string]string
新增：Event 字段 OriginRegion + ReplicaOf
新增包：internal/replication/conflict/
  - detector.go: VersionVector 检测 + 冲突标记
  - resolver.go: 管理 API（列出冲突 + 解决冲突）
修改：replication.go Worker → 多目标路由 + 防循环
修改：JobPool → 支持多 region 队列
新增：metrics replication_conflict_total, replication_lag_ms
```

**不建议的选项：** CRDT（冲突自由数据类型）。虽然自动合并在学术上优美，但与 S3 的对象语义（整个对象替换）不兼容。S3 的对象是原子的 blob 替换，不是 map merge。

### 方向 D：合规治理框架（基于本文 #5）

**业务价值：** 金融（SEC 17a-4）、医疗（HIPAA）、GDPR、PCI-DSS 的合规审计是存储产品进入受监管市场的 Gating Item。当前的 `LockedUntil` + `_aero_legal_hold` 不足以通过任何正规审计。

**设计权衡：**

| 选项 | 优点 | 缺点 |
|------|------|------|
| **选项 A：增量扩展现有字段**（`LockedUntil` + `legal_hold` + lifecycle） | 改动最小，向前兼容 | 合规信息仍然分散，无统一的"保留原因"审计链 |
| **选项 B：独立合规层**（`internal/compliance/`包 + `retention_bindings` 表） | 统一的合规视图：保留原因、授权人、法律案号、处置证书 | 迁移成本：需要将现有 `LockedUntil` 对象反向填充到 bindings 表 |
| **推荐：选项 B，逐步迁移** | 架构清晰，可审计 | 需处理大量历史对象 |

**核心难点：**

| 难点 | 解法 |
|------|------|
| **处置审批链** | 不是简单的"到期就删"，而是多级审批。审批人角色、升级策略（超时→上级）、拒绝→延长保留期。这是业务流程而非单纯技术 |
| **处置证书不可变性** | 证书生成后必须 append-only，即使对象已被删除。`_aero_disposal_certificates` 表标记为不可 DELETE/UPDATE（应用层 + DB trigger 双保险） |
| **法律保全覆盖保留** | Legal hold 必须独立于 retention——即使 `LockedUntil` 已到期，legal hold 未解除就不允许删除。`hardDeleteObject` 路径必须检查 legal_holds 表 |
| **保留策略变更的向前兼容** | 桶级保留策略从 7 年改为 3 年——已写入的对象应保持原保留期还是缩短？从合规角度：**保留期只增不减**。策略变更仅影响新写入 |

**对现有系统的影响：**

- `LockedUntil` 字段继续存在，作为 "quick lock" 模式（不写合规原因）的简化路径
- 新合规路径写入 `retention_bindings` 和 `legal_holds` 表
- `hardDeleteObject` 同时检查 `LockedUntil` + `legal_holds` + `retention_bindings.status`
- 不需要修改现有 `LockedUntil` 的写入路径

### 方向 E：WASM 沙箱化事件触发器（基于本文 #4）

**这是从"对象存储"到"计算平台"的差异化特性。** 文档正确地识别了它在架构上的跳跃性——不是增量改进，而是平台身份转变。

**与外部 Lambda 的取舍：**

| 维度 | 内置 WASM | 外部 AWS Lambda |
|------|----------|----------------|
| 延迟 | < 1ms（进程内） | 50-200ms（网络） |
| 隔离性 | 进程级+沙箱 | 独立函数实例 |
| 网络需求 | 无（离线可用） | 必须 AWS 可达 |
| 语言支持 | 任何 WASM 编译语言 | 主流语言 |
| I/O 能力 | 受限（须显式 enable） | 完整 AWS 生态 |
| 运维负担 | 内置（无额外服务） | 需管理 AWS 账户+权限 |

**建议策略：** 不二选一，而是双层：
- **WASM 层**：低延迟、离线可用、安全沙箱化，用于轻量预处理（校验、转换、标记）
- **外部 Lambda（S3 标准）**：当 `LambdaFunctionArn` 不为空时，通过 `lambdaDelivery` 调用 AWS Lambda——这是 S3 标准通知目标的一部分，与本文 #1 方向共享投递引擎

**技术难点：**

| 难点 | 复杂度 | 分析 |
|------|--------|------|
| 同步 Hook 集成 | 高 | 在 FileService 的 PUT 路径中加入 pre-upload hook：完成验证后才写入存储。这涉及核心路径修改，需要 feature flag 保护 |
| 内存隔离 | 高 | wazero 提供实例级内存隔离，但需确保函数不能通过 Go 的反射/unsafe 逃逸 |
| 超时控制 | 中 | context.WithTimeout 包装每个函数调用，超时后 kill 实例 |
| 版本管理 | 中 | 函数更新时，正在执行的旧版本调用应允许完成（drain），然后新流量使用新版本 |
| 多租户隔离 | 高 | WASM 函数的 `ctx` 必须注入租户身份，函数只能操作自身租户的资源 |

**预期架构变更：**

```
新增包：internal/functions/
  - model.go:      Function, TriggerDef 结构体
  - runtime.go:    WASM 运行时（wazero 封装）
  - sandbox.go:    安全限制（内存/时间/网络）
  - registry.go:   函数注册 + 版本管理
新增：functions 表 + 函数管理 API
新增：EventBus 的 WASM 消费者
新增：FileService 的 PreWriteHook 接口（可选）
新增：metrics function_* 指标
```

---

## 3. 接口设计建议

### 3.1 DeliveryTarget 接口（方向 A + B）

```go
// 建议接口
type DeliveryTarget interface {
    // Name 返回目标类型标识用于指标和日志
    Name() string
    
    // Deliver 投递单个事件
    Deliver(ctx context.Context, event Event, rule NotificationRule) error
    
    // DeliverBatch 批量投递（可选实现，用于压缩聚合）
    DeliverBatch(ctx context.Context, events []Event, rule NotificationRule) error
    
    // Validate 验证目标配置是否可达（用于管理 API 的预检查）
    Validate(ctx context.Context, rule NotificationRule) error
}
```

**设计原则：**
- `Deliver` 是必选，`DeliverBatch` 是可选（通过 type assertion 检测是否支持批量）
- `Validate` 允许管理 API 在保存规则前测试目标可达性
- 错误分类：可重试（限流/超时）vs 不可重试（规则配置错误/权限不足）

### 3.2 CDC Cursor API（方向 A）

```go
// 消费者游标管理
type CursorManager interface {
    // GetCursor 获取消费者进度
    GetCursor(ctx context.Context, consumerName, topic string) (int64, error)
    
    // AdvanceCursor 推进消费者进度
    AdvanceCursor(ctx context.Context, consumerName, topic string, cursor int64) error
    
    // RegisterConsumer 注册新消费者（初始化 cursor = max_id）
    RegisterConsumer(ctx context.Context, consumerName, topic string) error
}
```

**REST API 设计：**

```
GET /v1/events?after={id}&limit={n}&tenant={t}
  → { events: [...], next_cursor: N, has_more: bool }

POST /v1/events/cursors
  → {"consumer": "etl-pipeline", "topic": "object-events"}

GET /v1/events/cursors/{consumer}
  → {"consumer": "...", "cursor": 1000}

PUT /v1/events/cursors/{consumer}
  → {"cursor": 2000}  # 推进游标
```

### 3.3 冲突检测接口（方向 C）

```go
type ConflictDetector interface {
    // Check 检查写入是否存在冲突，返回冲突信息
    Check(ctx context.Context, object Object, writeVersion Vector) (*Conflict, error)
    
    // Mode 返回当前检测模式
    Mode() ConflictMode // LWW | VersionVector
}

type Conflict struct {
    ObjectID     int64
    Key          string
    LocalVersion Vector // map[region]version
    RemoteVersion Vector
    DetectedAt   time.Time
    Status       ConflictStatus // Open | Resolved
}
```

### 3.4 合规层接口（方向 D）

```go
type RetentionManager interface {
    // ApplyRetention 应用保留策略到对象
    ApplyRetention(ctx context.Context, objectID int64, policy RetentionPolicy, reason string) error
    
    // CheckRetention 检查对象是否可以被删除（返回阻止删除的原因列表）
    CheckRetention(ctx context.Context, objectID int64) ([]RetentionBlock, error)
    
    // AddLegalHold 施加法律保全
    AddLegalHold(ctx context.Context, objectID int64, caseID, appliedBy, reason string) error
    
    // RemoveLegalHold 解除法律保全
    RemoveLegalHold(ctx context.Context, holdID string, removedBy string) error
}
```

**关键设计原则：** `CheckRetention` 返回的 `[]RetentionBlock` 是**阻止原因列表**而非布尔值。这使得删除路径可以明确告诉调用方"为什么不能删"（法律保全未解除 / 保留期未到 / 处置审批中）。

### 3.5 向后兼容性原则

| 变更类型 | 策略 |
|---------|------|
| 新增接口方法 | 使用可选的 interface 扩展（如 `DeliverBatch` 通过 type assertion 检测） |
| 新增 API 端点 | 不影响现有端点，OpenAPI 新增 path |
| 新增事件类型 | `compliance.*` 事件，不会与现有 `object.*` 事件冲突 |
| 内部消费者迁移 | 旧 `NextUnconsumedEvents` 继续可用，标记为 deprecated，日志告警 |
| Schema 变更 | 仅 ADD COLUMN（SQLite 限制少，Postgres ADD COLUMN 安全） |
| 配置变更 | 新配置项设置合理默认值（如 `EVENTS_RETENTION_DAYS=7`） |

---

## 4. 技术选型

### 4.1 新依赖评估

| 依赖 | 用途 | 方案评估 | 结论 |
|------|------|---------|------|
| `github.com/aws/aws-sdk-go-v2` | SQS/SNS/Lambda API 调用 | 选项 A（完整 SDK）vs 选项 B（自建 SigV4） | **选项 B**：复用现有 sigv4.go，零新依赖。需扩展 sigv4.go 支持 `Service` 参数 |
| `github.com/tetratelabs/wazero` | WASM 运行时 | 唯一纯 Go WASM 运行时，~200KB，零 CGO | **必须**：WASM 方向的硬依赖 |
| Kafka/NATS 客户端 | CDC 事件总线 | 未来考虑 | **当前不引入**：基于数据库的 CDC 已足够，Kafka 作为未来升级路径保留接口 |

### 4.2 第三方依赖评估标准

```
准入标准（组织级）：
1. 许可证兼容性（Apache 2.0 / MIT / BSD 首选，AGPL 禁止）
2. 零 CGO 要求（保持纯 Go 构建）
3. 活跃维护（过去 6 个月有提交）
4. 没有间接引入重型依赖（如 Docker/containerd）
5. 二进制体积增量可接受（wazero ~200KB ✅）
```

### 4.3 自建 vs 采购决策

| 能力 | 决策 | 理由 |
|------|------|------|
| SQS/SNS/Lambda 投递 | **自建**（复用 SigV4） | 每个投递目标 ~150 行，无第三方 API 差异风险 |
| CDC 事件流 | **自建**（复用 events 表） | 零新依赖，events 表已有基础设施 |
| 冲突检测 | **自建** | Version Vector 是标准算法，无成熟 Go 库 |
| WASM 运行时 | **采购**（wazero） | wazero 是行业标准选择，自建 WASM 运行时不现实 |
| 合规处置证书 | **自建** | HMAC 签名 + 时间戳是标准密码学操作，无需额外依赖 |

### 4.4 存储后端扩展性

对于多区域复制（方向 C），每个 region 需要一个 Storage 坐标。当前 `Storage` 接口缺少"从坐标创建"的能力：

```go
// 建议新增
type Storage interface {
    // ...现有接口...
    
    // WithConfig 从存储坐标创建新的 Storage 实例
    WithConfig(config json.RawMessage) (Storage, error)
}
```

这使得 `replication.go` 可以根据 `ReplicationRule.TargetRegions` 动态创建目标 Storage 实例。

---

## 5. 实施路线图

### 5.1 优先级排序

```
P0（当前 Sprint / 开发周 1-3）：
  CDC 事件流 + 消费组隔离（方向 #2）
  理由：修复现有 SSE 可靠性 bug，为所有事件消费者建立基础设施

P1（开发周 4-8）：
  S3 Event Notifications 执行引擎（方向 #1）
  合规治理框架基础（方向 #5 的 retention_bindings + legal_holds）
  理由：S3 兼容性最后拼图 + 合规市场准入

P2（开发周 9-16）：
  多区域 Active-Active 复制与冲突检测（方向 #3）
  理由：工程复杂度高，需要 CDC + 事件投递就绪后才能有效测试

P3（中期路线图，3-6 个月）：
  WASM 沙箱化事件触发器（方向 #4）
  理由：差异化特性，工程投入高，建议先行打桩
```

### 5.2 阶段划分和里程碑

**阶段 1：事件基础设施增强（P0，2-3 周）**

```
Week 1: consumer_cursors 表 + CRUD + 分页事件查询 API
Week 2: 内部消费者迁移（indexer → cursor, webhook → cursor, AV → cursor）
Week 3: SSE replayMissed 修复 + CDC API 文档 + 集成测试
  ✅ Milestone: CDC 流可作为外部 ETL 管线的可靠数据源
```

**阶段 2：通知投递 + 合规骨骼（P1，4-5 周）**

```
Week 4-5: DeliveryTarget 接口 + queueDelivery + topicDelivery + lambdaDelivery
Week 6:   规则过滤引擎（Events[] 匹配 + Filter 评估）+ 批量压缩
Week 7:   retention_bindings 表 + legal_holds 表 + 保留策略引擎
Week 8:   合规管理 API + hardDeleteObject 扩展 + 处置证书生成
  ✅ Milestone: S3 事件驱动架构完整 + 金融级合规框架可用
```

**阶段 3：多区域复制（P2，8 周）**

```
Week 9-10:  ReplicationRule 多目标配置 + 路由引擎
Week 11-12: Version Vector 冲突检测 + 冲突标记
Week 13-14: 防循环（origin_region + replica_of）+ 复制事件过滤
Week 15-16: 冲突管理 API + 多 region 集成测试（Docker Compose）
  ✅ Milestone: 双区域 Active-Active 可演示 + 冲突可检测可解决
```

**阶段 4：WASM 触发器打桩（P3，2 周先行 + 后续迭代）**

```
先行打桩（2 周）：
  functions 表 + 管理 API CRUD（未连接 event bus）
  WASM 运行时 wazero 集成（可执行 Hello World）
  Function 测试执行端点 POST /v1/admin/functions/{id}/test
后续迭代：
  Event bus 订阅者连接
  FileService PreWriteHook 集成（可选）
  安全沙箱加固
```

### 5.3 风险点和缓解策略

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| CDC 游标迁移导致内部消费者丢事件 | 低 | 高 | 双写期：新旧 cursor 同时维护，逐消费者迁移，每个迁移后观察 24h |
| SQS/SNS SigV4 签名不兼容 | 中 | 中 | 方向 #1 实现前先编写 SigV4 针对 SQS 的集成测试（用真实 AWS 或 localstack） |
| Active-Active 测试环境复杂 | 高 | 中 | 使用 Docker Compose 模拟多 region（不同端口 + 不同 volume），不需要真实跨区域网络 |
| WASM 函数执行导致核心路径崩溃 | 中 | 高 | 始终使用 goroutine + recover，特征标志（`FUNCTIONS_ENABLED`）控制，默认关闭 |
| SQLite 在高吞吐 CDC 下成为瓶颈 | 低 | 中 | CDC 方向明确指出 SQLite 适用于低吞吐。产品文档应注明：高吞吐场景推荐 Postgres |
| 合规处置审批业务流程复杂化 | 中 | 中 | 处置审批作为可选阶段，不审批直接处置是默认行为。审批链仅在 `RequiresApproval=true` 时激活 |
| 多租户 WASM 函数的数据泄露 | 低 | 高 | wazero 实例级隔离 + 调用时注入租户上下文 + 函数只能通过显式 API 访问数据 |

### 5.4 推荐的依赖引入顺序

```
1. 无新依赖 → CDC 流（events 表复用）
2. 无新依赖 → SQS/SNS/Lambda（复用 SigV4，仅扩展 sigv4.go）
3. 无新依赖 → 合规框架（复用现有字段 + 新增表）
4. wazero (200KB) → WASM 运行时
5. 无新依赖 → Version Vector 冲突检测
```

---

## 总结

本文档 v139 识别了 5 个高质量扩展方向，全部是既有的代码扫描 + 差异分析产物，没有"为了凑数"的方向。从架构视角，我建议的**排序调整**集中在：

1. **CDC 流提前到 P0**：它不仅是新功能，更是修复现有 SSE 事件流的架构债务
2. **合规框架与通知投递并行**：合规是进入受监管市场的硬门槛，通知是 S3 兼容性的最后拼图，两者互补
3. **Active-Active 的冲突检测不可跳过**：LWW 的静默数据丢失在 Active-Active 场景是不接受的

文档中"代码锚点+当前状态+边界情况"的结构化为每个方向的工程评估提供了扎实的基础。建议工程团队按"阶段 1-2-3-4"的路线图推进，每个阶段结束后验证 `make check` + 集成测试通过，再进入下一阶段。
