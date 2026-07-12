# AeroVault 架构分析报告：基于 5 个工程完整性盲区

## 1. 架构评估

### 1.1 当前架构的核心优势

从文档中 5 个盲区的分布与深度分析可以看出，aero-vault 的架构在以下方面表现优秀：

**明确的分层边界。** `protocol → service → storage/repository` 的三层架构是合理的。证据在于：所有 5 个盲区都能精确定位到某一层的边界裂痕（如 retention 绕过 `ChunkCleaner` 属于 service 层调用遗漏；ETag 验证跳过属于 protocol 层解析遗失），这说明层的职责定义是清晰的——问题出在**层间契约的执行不完整**，而非层本身的划分错误。

**依赖注入模式的正确运用。** `FileService.WithChunkCleaner` 的可插拔设计、`main.go` 中通过参数装配不同 backend，为测试（mock chunkCleaner）和扩展（新增 storage backend 只需实现接口）提供了良好的基础。方向一的 Phase 1 建议（将 `ChunkCleaner` 注入 `RetentionJob`）正是这个模式的自然延伸。

**事件驱动架构的正确选择。** EventBus 模式使得 Indexer、Webhook、Replication 等关注点解耦。方向一的路径分析表明：事件驱动是正确的设计选择——问题是**可靠性保证不足**（非阻塞 `select-default` 丢弃），而非模式本身的问题。

### 1.2 关键架构债务

文档 5 个盲区揭示了三项深层的架构债务：

#### 债务 A：存储与元数据的二阶段提交缺失（Cross-cutting, P1-P2, 涉及方向一/五）

这是最根本的架构债务。当前系统在两个无事务协调器的资源上执行两阶段写入：

```
存储后端（local/S3/OSS/COS）    ← 无全局事务
Repository（SQLite/Postgres）    ← 有本地事务
```

`store.Put` 成功 → `UpsertObject` 失败的窗口，以及 `store.Delete` 成功 → `HardDeleteObject` 失败的窗口，本质上是**跨资源的两阶段提交**问题。当前既无 prepare/commit 模式，也无 saga 补偿模式，而是采用了"尽力而为"的顺序执行。

**评估：** 在单节点部署（SQLite + local FS）下，此问题可以通过 SQLite 本身的事务+文件系统 fsync 顺序部分缓解，但在 Postgres + S3 等异构后端下是没有保证的。这不是一个 bug，而是一个**架构级决策**——是否接受最终一致性，以及如果接受，补偿机制是否到位。

**建议判断：** 对于 P0 级别的数据保障（对象存储的核心承诺是"你存进去什么，取出什么"），当前设计是无法接受的。ReconcileJob 虽然是事后补偿机制，但它不覆盖所有路径（方向一的 chunk 孤儿就是其缺失的证明）。

#### 债务 B：事件可靠性保证的"脆皮抽象"

`bus.go:100-103` 中 `select-default` 丢弃事件是一个典型的"架构短路"——在低层做了高层不该默许的妥协。这导致：

| 影响 | 说明 |
|------|------|
| 事件订阅者必须能跟上发布者 | 否则静默丢事件 |
| 没有 backpressure 信号 | 发布者不知道订阅者落后 |
| 没有持久化 | 重启丢失全部未消费事件 |
| 没有重试 | 丢就是丢 |

**评估：** 对于事件总线来说，至少有四种可靠性模型，当前选择了最脆弱的一种（at-most-once 非持久化）。考虑到 aero-vault 的事件驱动面已经扩展到 Indexer、Webhook、Replication、Antivirus 等多个关键路径，这个基础设施的脆弱性会随着事件驱动面的扩大而线性放大。

#### 债务 C：Reconcile/补偿能力不完整

系统有 `ReconcileJob`（清理孤儿 blob、版本保留）、`RetentionJob`（清除软删除）、`ChunkCleaner`（清理 AI chunk），但：

- 没有统一的事务补偿框架
- 每个补偿路径独立实现，覆盖范围不完整
- 跨资源的不一致只能靠"定时扫描"来事后修复

**评估：** 对于存储系统来说，事后补偿（Reconcile）和事前预防（事务/锁）应该**两者兼备**。当前在两个方向上都有缺口。

### 1.3 关键设计决策的合理性回顾

针对 5 个盲区反映的决策点：

| 决策 | 合理性 | 建议 |
|------|--------|------|
| `store.Put` 在 `UpsertObject` 之前 | ❌ 错误。这个顺序意味着 blob 可以没有元数据，违反"元数据为主"的契约。正确的方向应该是先保留元数据（在 DB 事务中标记 pending），再写 blob，再确认 | 改为 `INSERT pending → store.Put → UPDATE confirmed` 的三阶段模式（或 saga） |
| EventBus 非阻塞传递 | ⚠️ 可以接受，但需要建立**死信通道**。丢弃事件后应有 DLQ 或周期性 reconcile 来补偿 | 增加 secondary consume-from-DB 路径 |
| `completeMultipartUpload` 解析丢弃 | ❌ 设计错误。协议层的 XML 解析应该产生活跃数据，而非死代码 | 修复方向二（且这个修复的代码行数极少，≈50 行） |
| Retention GC 绕过 ChunkCleaner | ❌ 调用遗漏。属于 service 层契约执行不完整 | 方向一的 Phase 1 修复 |
| Web UI 无管理面板 | ✅ 合理的阶段性决策——先有功能后有 UI。但现在功能完备度已超过 UI 覆盖度，需要补齐 | 方向三的推进 |
| S3 Select 不实现 | ✅ 合理的下限决策。这个功能不是 S3 兼容的核心，且实现复杂度相对较高 | 方向四保持 P3 优先级 |
| 无乐观锁/CAS | ⚠️ 在当前并发模型下可以工作，但在多副本、多节点下会出问题。建议在引入集群模式前加入 | 方向五的 Phase 1 |

---

## 2. 扩展方向

### 方向 A（P0）：引入跨资源事务补偿框架

**为什么需要：**

方向五的 Put/Delete 事务裂痕（blob ↔ metadata 不一致）和方向一的 chunk 清理遗漏其实指向同一个根本问题：**系统没有统一的机制来保证跨存储/数据库/索引三个资源的数据一致性**。随着未来可能再加入缓存、CDN 预热、消息通知等新资源，这个问题只会恶化。

**核心挑战：**

1. **异构资源无法全局事务化。** local FS 没有 prepare, S3 没有 2PC, Qdrant 不支持分布式事务。不能用传统数据库的 `BEGIN/COMMIT` 来解决
2. **补偿操作的幂等性必须严格保证。** `DeleteObjectChunks` 重复调用不能报错，`store.Delete` 对已删除 blob 必须返回 nil
3. **补偿的截止条件。** 如果补偿本身失败，系统应该重试到何时？何时放弃并标记为 manual intervention required？

**预期架构变更：**

```
当前：
  store.Put → UpsertObject
  └── "尽力而为"顺序

变更后：
  store.Put        → 主操作（有重试）
  ↓ 同时记录 intent
  UpsertObject     → 依赖操作
  ↓ 如果失败
  store.Delete     → 补偿操作
  ↓ 如果补偿也失败
  Intent 表标记失败 → ReconcileJob 兜底
```

引入 `write_ahead_intents` 表或类似 Outbox 模式：

```
┌─────────────────────────────────────────────┐
│                Intent Table                  │
├────────────┬──────────┬─────────────────────┤
│ intent_id  │ status   │ compensating_action  │
│ uuid-1     │ pending  │ store.Delete(key)    │
│ uuid-1     │ done     │ —                    │
│ uuid-2     │ failed   │ store.Delete(key)    │ ← Reconcile picks up
└────────────┴──────────┴─────────────────────┘
```

**对现有系统的影响：**

- 新增 ~200 行的 intent 表/管理逻辑
- `FileService.Put` `FileService.Delete` 的重构（方向五已有详细路径）
- ReconcileJob 增加 intent 补偿的通用 handler
- 这是**侵入性最小**的改进——不影响 protocol 层，不影响存储具体实现

### 方向 B（P1）：事件系统的可靠性升级

**为什么需要：**

方向一的分析显示，EventBus 的静默丢弃已经造成了实际的数据一致性问题（chunk 残留）。而当前事件系统的消费端包括 Indexer、Webhook、Replication、Antivirus——每一个都是对数据完整性和业务正确性至关重要的路径。在 at-most-once 语义下，系统的核心业务路径（如 retention GC）需要绕过事件系统来做冷备份补偿，说明事件系统已经失去了信任。

**核心挑战：**

1. **at-least-once 要求幂等消费。** 当前 Indexer 的 `handle` 是否幂等的？看起来是的（删除已删除的 chunk 是安全的，索引已索引的对象会覆盖），但需要审计
2. **性能与可靠性的权衡。** 如果事件必须持久化到 DB 后才能广播，延迟会增加（每次事件写入 DB 的额外延迟）
3. **重试逻辑的边界。** 一个事件重试多少次？间隔策略是线性退避还是指数退避？最终放弃后的死信处理？
4. **事件格式的向前兼容。** 增加可靠性的同时不能破坏已有的事件类型和订阅者

**预期架构变更：**

```
当前：
  Publish → broadcast (select-default)
  └── at-most-once, 无持久化

方案 A (轻量)：
  Publish → channel + goroutine → broadcast
  └── channel 有 buffer，满了阻塞而非丢弃
  └── 发布者可以选择 timeout

方案 B (重量)：
  Publish → INSERT events_outbox → background consumer → broadcast
  └── at-least-once, 重启不丢失
  └── 消费完后标记 consumed
```

**对现有系统的影响：**

- 方案 A：仅修改 `bus.go`（~30 行），事件类型不变，订阅者无变化
- 方案 B：新增 `events_outbox` 表（migration）+ 后台消费 goroutine（~200 行），事件类型不变

**建议：** 方案 A 作为 Phase 1（修复丢弃问题），方案 B 作为 Phase 2（持久化保证）。方向一的 Phase 2（软删除同步清理 chunk）可以先堵住事件丢失的副作用，为事件系统升级争取时间。

### 方向 C（P1）：数据完整性契约层

**为什么需要：**

方向二的 ETag 验证遗漏、方向五的并发裂痕，本质上都是**系统缺少一个统一的"数据完整性契约"层**。当前这个契约分散在各层中：

| 检查点 | 所在层 | 当前状态 |
|--------|--------|---------|
| UploadPart ETag 存储 | storage layer | ✅ 正确存储 |
| CompleteMultipart 交叉验证 | protocol → service | ❌ 跳过了 |
| Put 的 MD5 校验 | service layer (verifyMD5) | ✅ 正确执行 |
| 并发 Put 的 CAS | service/repository | ❌ 缺失 |
| 硬删除的幂等性 | service layer | ❌ 缺失 |
| Chunk ↔ Object 存在性 | reconciliation | ❌ 缺失 |

**核心挑战：**

1. **契约层应该放在哪？** service 层是目前最自然的位置（因为它位于 protocol 和 storage/repo 之间），但有些检查需要在 protocol 层做（如 ETag 验证需要在解析客户端请求后立即进行），有些需要在 repository 层做（如 CAS 乐观锁）
2. **避免重复检查影响性能。** 同一个数据在多个层级都校验会增加不必要的开销。需要明确每个检查点的**唯一所有者**
3. **错误响应的标准化。** 当前 S3 返回 S3 XML 错误，REST 返回 JSON 错误，再增加新的校验失败应该复用现有的错误码体系

**预期架构变更：**

不需要新增抽象层。而是在现有层次中增加**显式的校验点**：

```
protocol layer  →  解析+验证  →  ETag 交叉验证（方向二修复）
service layer   →  业务校验   →  MD5, CAS, 存在性检查
repository layer → 乐观锁     →  version 字段 CAS
storage layer   →  存储校验   →  checksum 存储与返回
```

每个校验点输出标准化的错误类型（`ErrIntegrityViolation`, `ErrConflict`, `ErrPartMismatch`），各层可以统一处理。

**对现有系统的影响：**

- repository 层增加 `version` 列（migration，~30 行 SQL 改动）
- service 层 Put/Delete 增加 CAS 重试（方向五 Phase 1，~100 行）
- protocol 层 S3 handler 增加 ETag 验证（方向二 Phase 1，~50 行）
- 每个变更独立、增量、可测试

### 方向 D（P2）：统一的运维管理平面

**为什么需要：**

方向三揭示了 Web UI 与管理能力之间的鸿沟——系统有完整的 admin REST API，但 Graphic UI 完全不消费。这导致：
- 运维人员需要同时掌握 CLI 和 Web UI
- 系统的高级功能（桶 CORS 配置、桶策略、通知、生命周期）无法在 UI 中发现
- 多租户运营缺少可视化 dashboard

**核心挑战：**

1. **UI 开发资源 vs 后端开发资源。** 当前项目主要面向后端/SDK 用户。投入 UI 开发的边际收益需要量化
2. **前端栈选择。** 当前 `index.html` 是裸 JS+Fetch。如果要扩展为管理面板，是否引入 React/Vue/Svelte 等框架？这会带来构建工具的复杂度
3. **OpenAPI 一致性问题。** 后端有 `openapi.json`，但 UI 目前不通过 OpenAPI 生成的客户端消费 API。如果直接 fetch，API 变更时需要同步修改 UI

**预期架构变更（最少侵入方案）：**

```
当前：
  Web UI (static HTML)    ───→ REST API (独立)
  CLI                     ───→ REST API (独立)

建议：
  Web UI (static HTML)    ───→ API Proxy (可选) ───→ REST API
                                └── 解决 CORS、auth token 注入
  同时：在后端增加 /v1/config 或 /v1/discovery endpoint
       暴露 UI 可以消费的"可用特性"清单
```

**对现有系统的影响：**

- 后端：无侵入（REST API 已存在）。只需增加一个 `GET /v1/ui/config` endpoint（~20 行）返回租户/桶列表等引导信息
- 前端：新增若干 HTML/JS 面板（~1000 行，分 3 个 phase）。不需要新的前端工具链——保持纯 JS 可以确保构建系统不变
- 测试：E2E 测试需要浏览器（Playwright/Cypress）→ 这个在 CI gate 外

### 方向 E（P2-P3）：协议层的"最后一公里"增强

**为什么需要：**

方向四的 S3 Select 缺失和方向二的 ETag 验证遗漏都属于"协议层实现不完整"的问题。aero-vault 的 S3 兼容性覆盖了大部分常用操作，但在一些关键路径上存在缺口。如果项目要作为 S3 替代品被更广泛使用，这些缺口需要补齐。

**核心技术挑战：**

S3 Select 的实现难点不在于 S3 协议层（XML 解析是简单的），而在于：
- SQL 表达式解析和执行（需要一个轻量 SQL 引擎）
- 流式 CSV/JSON/Parquet 解析（不能一次性加载到内存）
- 输出的事件格式（S3 Select 使用自定义 event stream 格式，需要在 ResponseWriter 上实现 chunked encoding）

**预期架构变更：**

```
S3 Select 架构：
  POST /{bucket}/{key}?select
  │
  ▼
  handler.SelectObjectContent(xml decode)
  │
  ▼
  sql.Parser(SQL expression)     ← 新增
  │                              ← 使用 expr 或 gval 等轻量库
  ▼
  csv.Reader(input serialization) ← 复用标准库或增强
  │
  ▼
  projection+filter(在 Go 中执行) ← 无额外依赖
  │
  ▼
  eventStream.Encoder(w)          ← 新增 S3 Select 事件格式
```

**对现有系统的影响：**

- 后端新增 `internal/s3select/` 或类似包（~450 行，分 2 个 phase）
- 无现有功能的回归风险——S3 Select 是完全新增的 endpoint
- 新增的依赖（如 `expr` 或 `gval` 库）需要评估

---

## 3. 接口设计建议

### 3.1 关键接口设计原则

基于 5 个盲区的分析，我建议以下设计原则：

**原则 1：失败传播要完整，不可静默吞没**

方向一的 `bus.go:100-103` 的 `select-default` 是一个典型反例。任何接口如果承诺"传递"，就应该在无法传递时通知调用方。这适用于：

```go
// 当前（Bad）：静默丢
func (b *Bus) Publish(e Event) {
    select {
    case ch <- e:
    default:
        b.dropped.Add(1)  // 调用方不知道
    }
}

// 建议（Good）：返回错误
func (b *Bus) Publish(e Event) error {
    select {
    case ch <- e:
        return nil
    default:
        return ErrBusFull  // 调用方可以选择阻塞、重试、或使用 fallback
    }
}
```

**原则 2：调用方提供的意图必须被消费**

方向二的 `completeMultipartUpload` 中，客户端提交的 `<Part>` 列表被解析后丢弃是最明显的违反。更一般的原则是：**任何从请求中解析出的数据，要么用于驱动逻辑，要么在日志中明确记录为"已忽略"**。代码中不应存在"解析后不用"的变量——这应该作为 lint 规则。

**原则 3：补偿操作必须与主操作处于同一抽象层**

方向一的 `purgeSoftDeleted` 在 storage 层删除了 blob，在 repository 层删除了 metadata，但跳过了 service 层的 chunk cleanup。这是因为 `RetentionJob` 在 `internal/reconcile/` 包中，直接使用了 `store` 和 `repo` 接口，绕过了 `FileService`。这违反了"Dependency Inversion Principle"——高层次的操作（回收清理）依赖了低层次的存储和数据库细节。

**建议：** `ReconcileJob` 和 `RetentionJob` 应该依赖一个更高层的接口：

```go
// 统一数据生命周期接口
type DataLifecycleManager interface {
    // 硬删除对象及其所有关联数据
    HardDeleteObject(ctx context.Context, tenant, bucket, key string) error
    // 清理所有残留数据（孤儿 blob、chunk 等）
    CleanupOrphans(ctx context.Context) error
}
```

这样 `RetentionJob` 不再需要知道 `ChunkCleaner` 的存在——它只需要调用 `HardDeleteObject`，由实现者（即 `FileService`）来处理所有关联数据的清理。

### 3.2 是否需要新的抽象层

| 方向 | 需要新抽象 | 理由 |
|------|-----------|------|
| 数据一致性 | **是 — Intent/Outbox 模式** | 跨资源的事务补偿需要统一的管理机制。当前每个操作自己处理补偿（如 `Put` 的 MD5 失败后 `store.Delete`），不一致且不完整 |
| 事件可靠性 | **否** — 增强现有 `Bus` 接口 | 不需要新增抽象。只需在 `Publish` 方法中增加错误返回（或阻塞/超时选项），以及增加持久化 Outbox consumer |
| 数据完整性 | **否** — 增强现有 `Object` 结构 | 增加 `Version` 字段即可支持 CAS，现有的 Repository 接口方法签名微调 |
| 运维管理 | **否** — 复用现有 REST API | Web UI 直接消费现有 API 即可，不需要新增抽象层 |
| S3 Select | **是 — 新增 `s3select` 包** | 这是全新的功能领域（SQL 解析+流式过滤），有独立的输入/输出格式和错误类型 |

### 3.3 向后兼容性策略

| 变更类型 | 策略 | 示例 |
|----------|------|------|
| **新增字段**（Object.Version） | 默认值 0，旧请求走旧路径 | `UpsertObject` 不检查 version 如果为 0（无 CAS） |
| **加强验证**（ETag 交叉验证） | 渐进式推出：先 warn log，后 reject | Phase 1 在 ETag 不匹配时 warn log + 仍继续；Phase 2 改为 reject |
| **新增接口**（Bus.Publish error） | 新增方法，不修改现有签名 | `PublishBlocking` 和 `PublishOrDrop` 共存 |
| **SQL migration** | 所有 migration 必须提供 UP 和 DOWN 脚本 | `0006_add_object_version.{up,down}.sql` — 方向五的 version 列 |
| **新增功能**（S3 Select） | 全新路由，不修改现有 handler | `r.Post("/{bucket}/*", h.SelectObjectContent)` 不冲突 |

---

## 4. 技术选型

### 4.1 是否需要引入新的技术栈

从 5 个盲区的修复出发，评估：

| 方向 | 推荐方案 | 是否需要新依赖 | 评估 |
|------|---------|---------------|------|
| 方向一（chunk 清理） | 仅修改现有代码路径 | **否** | 纯逻辑改动，无新依赖 |
| 方向二（ETag 验证） | 解析 XML 到结构体 + 验证 | **否** | `encoding/xml` 已引入 |
| 方向三（Web UI） | 纯 JS + Fetch API | **否** | 不引入前端框架，保持构建系统简洁 |
| 方向四（S3 Select） | SQL 解析 + CSV 流式处理 | **可选** — `expr` 或 `gval` | 评估见下文 |
| 方向五（并发一致性） | 乐观锁 + 重试 | **否** | 纯逻辑改动 + SQL migration |

**结论：** 5 个方向的修复中，4 个不需要任何新依赖。方向四的 S3 Select 是唯一可能引入新依赖的。

### 4.2 方向四的依赖评估

如果实现 S3 Select 的 SQL 解析，有以下选项：

| 方案 | 说明 | 优点 | 缺点 |
|------|------|------|------|
| **A: 手动解析** | 用 Go 标准库的 `text/scanner` 或手写 parser | 零依赖；精确控制支持能力；安全（无 SQL 注入绕 parser 的可能） | 开发量较大（~150 行 parser）；只支持有限的 SQL 子集 |
| **B: `expr` 库** (`expr-lang/expr`) | 类 Go 表达式的求值引擎 | 成熟的语法树；支持 `WHERE col > 100` 等条件；已有 4.4k stars；零 CGO | 不支持完整的 SQL SELECT（需自行实现 PROJECTION）；不是标准 SQL |
| **C: `gval` 库** (`PaesslerAG/gval`) | 通用表达式求值引擎 | 类似 expr，但更灵活 | 同样不支持完整 SQL；社区较小 |
| **D: SQLite in-memory** | 将 CSV 加载到内存 SQLite 表，执行标准 SQL | 支持完整 SQL（GROUP BY, ORDER BY, JOIN）；B 树引擎成熟 | 内存占用（大对象）；需要 CGO（或纯 Go SQLite 如 `modernc.org/sqlite`）；大文件无法流式 |

**个人建议：方案 B（`expr`）+ 手写 PROJECTION + CSV 流式处理**。理由：

1. S3 Select 的使用场景主要是**列过滤 + 行过滤**，不需要 JOIN、GROUP BY 等。`expr` 完全可以覆盖 `WHERE` 子句
2. `SELECT col1, col2` 的投影可以用手写的简单 parser（或 `strings.Split`）解析
3. 流式读取 CSV 不依赖任何外部库（`encoding/csv` 是标准库）
4. 安全的表达式求值——`expr` 不支持任意代码执行

如果团队不想引入新依赖，方案 A（手动解析）也是可行的——S3 Select 只需要支持 `SELECT ... WHERE ... LIMIT ...` 三个子句，解析复杂度有限。

### 4.3 自建 vs 采购/采用的决策依据

从 5 个盲区的修复来看，不涉及"采购"决策——所有修复都是纯工程改动。但方向三（Web UI）涉及一个重要的自建决策：

| 方向 | 自建 | 采用 | 建议 |
|------|------|------|------|
| **Web UI 管理面板** | 纯 JS 手写 | 引入 React/Vue/Svelte | **自建纯 JS**。原因：项目已有 `index.html` 是纯 JS 手写；引入前端框架会带来构建工具链（webpack/vite）、npm 依赖、CI 新步骤。当前只增加几个面板页面，不涉及复杂的状态管理或路由 |
| **S3 Select SQL 解析** | 手写 parser | 使用 expr/gval | **建议采用 expr**。原因：SQL 条件表达式的解析看起来简单，但 edge case 多（括号嵌套、引号字符串、`NULL` 处理、类型转换）；成熟的表达式引擎在这些 edge case 上已经经过验证 |
| **事件持久化** | 使用现有 DB（SQLite/Postgres）作为事件 outbox | 引入 Kafka/RabbitMQ/nats | **自建 DB Outbox**。原因：项目不需要 Kafka 的吞吐量；现有的 Repository 层已有 DB 连接；增加一个 `events_outbox` 表比引入新中间件简单两个数量级 |

---

## 5. 实施路线图

### 5.1 优先级矩阵

基于影响范围 × 修复成本的二维评估：

```
影响范围（y 轴，向上越大）
  ↑
  │  方向一(chunk)  ●  方向二(ETag)
  │  P1                P1
  │
  │  方向五(并发) ●    方向三(UI)
  │  P2                P2
  │
  │                    方向四(Select)
  │                    P3
  └──────────────────────────────→ 修复成本（x 轴，向右越大）

注：方向一虽然涉及三个 phase，但 Phase 1 修复（~30 行）是低成本的
    方向二虽然影响严重，但修复成本也非常低（~50 行）
```

**最终优先级：**

| 优先级 | 方向 | 理由 |
|--------|------|------|
| **P0** | 方向一 + 方向二 | 影响数据完整性/搜索质量，修复成本低（80 行 total），**应立即执行** |
| **P1** | 方向一 Phase 3 + 方向五 | 方向五的并发一致性影响数据安全，但修复成本高于方向二。方向一 Phase 3 的 reconcile loop 可以系统性地解决所有 chunk 孤儿问题 |
| **P2** | 方向三 | 产品体验提升，不直接影响数据安全性，但影响运维效率和产品完整性 |
| **P3** | 方向四 | 纯新增功能，不修复现有缺陷 |

### 5.2 阶段划分

#### Phase 0（Hotfix — 立即执行，预计 1-2 天）

| 任务 | 文件 | 代码行估计 | 产出 |
|------|------|-----------|------|
| 方向二 Phase 1：ETag 交叉验证 | `extra.go` | ~50 行 | CompleteMultipartUpload 拒绝 ETag 不匹配的请求 |
| 方向一 Phase 1：RetentionJob 调用 ChunkCleaner | `retention.go` | ~30 行 | 保留期清除不再遗留 orphan chunk |
| 方向一 Phase 2：软删除同步清理 chunk | `file_crud.go` | ~10 行 | 软删除路径增加 ChunkCleaner 幂等调用 |

**这个阶段后，数据完整性的最严重缺口已被堵住。** 方向一的三条 path 中，Path A（软删除）和 Path B（retention GC）的缺口被堵住；方向二（ETag 静默跳过）被修复。

**风险：** 低。这些修改是增量性的、幂等的、且都有版本控制的 migration 支持（或不需要 migration）。

#### Phase 1（短期优化 — 预计 1 周）

| 任务 | 文件 | 代码行估计 | 产出 |
|------|------|-----------|------|
| 方向五 Phase 1：乐观锁 | `sql_objects.go`, migration | ~100 行 | Put/Delete 路径使用 CAS，并发冲突可检测 |
| 方向五 Phase 2：硬删除重试 | `file_crud.go` | ~50 行 | HardDeleteObject 失败时自动重试 |
| 方向一 Phase 3：ReconcileChunksJob | `reconcile/` 新文件 | ~150 行 | 周期性扫描 chunk 孤儿并清理 |
| EventBus 方案 A（阻塞替代丢弃） | `bus.go` | ~30 行 | 事件不再静默丢失 |

**这个阶段后，并发一致性的最恶化路径已被保护。** EventBus 不再丢弃事件（改为阻塞或 caller 控制超时）。Chunk 孤儿有了定期清理机制。

**风险：** 中。乐观锁的引入可能暴露之前未被发现的并发 bug（比如某些代码路径假设了"最后写入者获胜"）。建议在测试环境中启用后观察。

#### Phase 2（中期增强 — 预计 2-3 周）

| 任务 | 文件 | 代码行估计 | 产出 |
|------|------|-----------|------|
| 方向三 Phase 1：管理标签页（只读） | `index.html`, 新 JS | ~300 行 | 存储统计、租户列表、Job 队列状态、审计日志 |
| 方向三 Phase 2：桶管理+Key 管理 | `index.html`, 新 JS | ~400 行 | 桶创建/删除/配置、API Key 管理 |
| 方向三 Phase 3：对象管理增强 | `index.html`, 新 JS | ~300 行 | 文件上传/下载/删除/版本浏览/标签编辑 |
| EventBus 方案 B（持久化 outbox） | `bus.go`, migration | ~200 行 | at-least-once 事件保证 |

**这个阶段后，产品的管理能力在 UI 和 API 两端对齐。** 事件系统获得持久化保障。

**风险：** 中低。UI 开发是独立的，不涉及后端架构变更。EventBus 持久化的 outbox 模式使用现有的数据库，不引入新依赖。

#### Phase 3（长期增强 — 预计 2-3 周）

| 任务 | 文件 | 代码行估计 | 产出 |
|------|------|-----------|------|
| 方向四 Phase 1：CSV Select | `s3select/` 新包 | ~300 行 | 基本的 CSV 列投影+行过滤 |
| 方向四 Phase 2：JSON Support | `s3select/` 扩展 | ~150 行 | JSON Lines/文档模式的 Select |
| Intent/Outbox 统一补偿框架 | `internal/compensator/` 新包 | ~200 行 | 统一的跨资源事务补偿 |

**这个阶段后，系统在 S3 协议完备性上达到更高水平。** 统一的补偿框架为未来增加更多资源类型提供保障。

**风险：** 中。S3 Select 是全新的功能，QA 需要覆盖 CSV/JSON 的多种格式变体和支持的 SQL 子集。

### 5.3 关键里程碑

| Milestone | 完成标准 | 时间 | 交付物 |
|-----------|---------|------|--------|
| **M0: 数据完整性修复** | 方向一 Phase 1&2 + 方向二 完成 | Day 2 | 3 个 PR，约 90 行代码改动。CI 通过 |
| **M1: 并发安全加固** | 方向五 Phase 1&2 + EventBus 方案 A 完成 | Week 1 | 4 个 PR，约 180 行代码改动。CI 通过 |
| **M2: 搜索索引完整性** | 方向一 Phase 3（ReconcileChunksJob）完成 | Week 1.5 | 1 个 PR，~150 行。新增 `index_chunk_coverage_ratio` 指标 |
| **M3: 管理 UI 完成** | 方向三 Phase 1&2&3 完成 | Week 4 | 多个 PR，~1000 行 HTML/JS。人工 QA |
| **M4: 事件系统持久化** | EventBus 方案 B 完成 | Week 4.5 | 1 个 PR，~200 行 + migration |
| **M5: S3 Select MVP** | 方向四 Phase 1 完成 | Week 7 | 1 个 PR，~300 行。支持 CSV Select |
| **M6: 补偿框架** | Intent/Outbox 框架完成 | Week 8 | 1 个 PR，~200 行。Put/Delete 迁移到新框架 |

### 5.4 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| **乐观锁引入新的并发 bug** | 中 | 高 — 可能导致数据竞争或死锁 | ① 先在非版本化桶上启用，观察 1 周 ② 增加 `go test -race` 在 CI 中 ③ 配置 Feature Flag 可以回退到旧行为 |
| **EventBus 阻塞导致发布者延迟** | 中 | 中 — 影响吞吐量 | ① 方案 A 中提供 `PublishTimeout` 和 `PublishBlocking` 两种选择 ② 增加 blocking 事件的 metric，监控阻塞率 ③ 持久化 outbox（方案 B）分两步走，先读后写 |
| **方向四 SQL 解析的表达式注入** | 低 | 高 — 安全风险 | ① 使用 `expr` 的安全模式（禁止函数调用） ② 白名单支持的函数 ③ 限制表达式复杂度（最大深度 10） |
| **方向三 UI 改动过多** | 中 | 中 — 维护负担 | ① 严格保持纯 JS，不引入前端框架 ② 每个 phase 独立上线 ③ 维护 `openapi.json` 与 UI 的同步 |
| **方向一 ReconcileChunksJob 误删活跃 chunk** | 低 | 高 — 搜索功能受损 | ① 先以 dry-run 模式运行 1 周期 ② 每次操作写入 audit log ③ 支持手动回滚（从 backup 恢复 chunk） |
| **Phase 0 修改的回归** | 低 | 中 — 核心路径受影响 | ① Phase 0 的每个 PR 必须增加对应测试 ② Multipart ETag 测试必须覆盖 ETag 匹配/不匹配两种情况 ③ ChunkCleaner 幂等性测试 |

### 5.5 依赖关系图

```
Phase 0 (Data Integrity Fixes)
  ├── 方向二（ETag）← 无依赖
  └── 方向一 Phase 1&2（ChunkCleaner）← 无依赖

Phase 1 (Concurrency Safety)
  ├── 方向五 Phase 1（CAS）← 需 migration
  ├── 方向五 Phase 2（Retry）← 依赖方向五 Phase 1
  ├── 方向一 Phase 3（ReconcileChunks）← 依赖方向一 Phase 1&2
  └── EventBus 方案 A ← 无依赖

Phase 2 (Management UI + Event Persistence)
  ├── 方向三 Phase 1（只读 UI）← 依赖方向一 Phase 1&2（搜索结果不再包含删对象）
  ├── 方向三 Phase 2（可写 UI）← 依赖方向三 Phase 1
  ├── 方向三 Phase 3（对象管理）← 依赖方向三 Phase 2
  └── EventBus 方案 B ← 依赖 EventBus 方案 A

Phase 3 (Advanced Features)
  ├── 方向四 Phase 1（CSV Select）← 无依赖
  ├── 方向四 Phase 2（JSON Select）← 依赖方向四 Phase 1
  └── 补偿框架 ← 依赖方向五 Phase 1&2 + 方向一 Phase 3
```

---

## 总结

从架构角度看，这 5 个盲区反映的不是"系统设计错了"，而是**系统在扩张过程中，已有的架构契约没有随着新功能的加入而同步强化**。具体来说：

1. **EventBus 的不可靠设计**（方向一）是在系统只有少数订阅者时的合理简化，但现在有 4+ 个订阅者，且每个都影响数据完整性，可靠性需要升级

2. **缺少乐观锁**（方向五）在单线程/低并发场景下不是问题，但文件存储天然是多客户端访问的资源，需要 CAS

3. **协议层的解析遗漏**（方向二）是代码质量问题，暴露了 code review 中对"客户端输入必须被消费"原则的松懈

4. **管理 UI 缺失**（方向三）是产品阶段的合理决策——先有功能后有 UI——但现在到了补齐的时候

5. **S3 Select 缺失**（方向四）是功能范围的选择——不实现是对的（P3 优先级），但如果目标是完整 S3 兼容，最终需要补齐

**最重要的建议（按对话顺序）：**

**首先修复 Phase 0 的 90 行。** 方向二的 ETag 验证和方向一的 ChunkCleaner 调用的修复成本极低，但影响极高。应该今天就开始。

**其次解决设计层面的两个根本问题：** 事件系统可靠性（方向一辅助）和跨资源一致性（方向五）。这两个问题未来只会随着系统 feature 的增多而恶化。

**最后才是功能增强：** 管理 UI（方向三）和 S3 Select（方向四）。这些是重要的产品能力，但不是数据安全/完整性层面的紧迫问题。
