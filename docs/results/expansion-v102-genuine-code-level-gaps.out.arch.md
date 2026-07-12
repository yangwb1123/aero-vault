# 架构分析报告：aero-vault 高价值扩展方向

## 1. 架构评估

### 1.1 当前架构的优势

aero-vault 的整体架构展现了几个值得肯定的设计决策：

| 优势 | 代码证据 | 评价 |
|------|---------|------|
| **事件总线与消费者分离** | `events.Bus.Publish` 将事件持久化到 DB + 广播给本地 subscriber；消费者（复制/AV/索引器）通过 `jobs.Pool` 异步处理 | 正确的 CDC 模式，DB 落盘保证 durability，channel 保证低延迟 |
| **存储与元数据分离** | `storage.Storage` 接口 + `repository.Repository` 接口，FileService 组合两者 | 标准的双写模式，backend 可替换；contract test 保障一致性 |
| **Opt-in 安全默认** | AI/pgvector/Qdrant/events/WebDAV 全部 flag-gated，默认 off | 符合最小暴露原则，基线路径零外部依赖 |
| **JobPool 去中心化设计** | `jobs.Pool` 通过 `jobs.Registry` 解耦 job type 与 handler，任何进程都可注册并执行 | 易于扩展新 worker 类型，支持多实例消费 |
| **接口粒度合理** | `FileService` 作为核心控制器统一所有协议入口，handler 层保持薄层 | 协议差异被限制在 handler 层，核心逻辑可测试 |

### 1.2 关键设计缺陷

但这些优势背后存在**五个结构性技术债**，与需求文档中的五个方向一一对应：

#### 债一：复制——半成品灾备（P0）

`replication.go:78` 只处理 `EventCreated`，且 dedupe key `"replicate:<ObjectID>"` 对非版本化桶的覆盖写会错误去重。这导致：

- 副本上无法反映删除操作（对象永久残留）
- 副本上无法反映更新操作（永远保留初版）

**根本原因**：复制被设计为"创建时异步备份"而非"实时精确副本"。设计文档中缺少对复制语义的完整定义：什么是"复制"？是"灾备基础"还是"归档"？

#### 债二：SSE 事件流——易失性与消费者混同（P1）

`bus.go:101` 的 `broadcast` 方法在 channel 满时静默丢弃事件；`sse.go:44` 的 `replayMissed` 使用 `NextUnconsumedEvents`（全局 unconsumed 视角）而非客户端专属游标。这导致：

- 客户端短暂阻塞即永久丢事件（有 metric 但无补偿）
- 重连时不精确回放（可能错过已被其他消费者处理的事件）

**根本原因**：SSE 被设计为"尽力而为"的实时通知，而非"可靠投递"的事件流。`bus.Subscribe()` 的消费者模型将所有 subscriber 等同视之——没有区分"重要消费者（复制/Webhook）"和"可丢消费者（SSE）"。

#### 债三：DELETE 语义随协议漂移（P1）

`s3compat/handler.go:258` 传 `hard=true`，`rest/handler.go:310-320` 传 `hard=false`。`service.Delete` 的 `hard bool` 参数将策略决策推给了每个调用者。WebDAV 的 `RENAME` 在 REST/S3/MCP 中完全不存在。

**根本原因**：协议差异通过参数传播而非通过抽象消除。`hard bool` 本质是策略（policy）而非选项（option）——策略应由 bucket 配置决定，而非由每个 handler 在调用现场决策。

#### 债四：JobPool 错误处理扁平化（P2）

`jobs.go:runOne` 将所有 handler 返回的 error 统一视为"重试或死信"——不区分 `ErrNotFound`（无害的删除竞赛）和真正的网络故障。当创建后立即删除的对象被消费者处理时，`GetObjectByID` 返回的 `ErrNotFound` 被等同为需要重试的故障。

**根本原因**：Job handler 的错误语义未被分类。`jobs.Pool` 没有定义"可重试错误"与"不可重试错误"的契约，导致所有错误进入同一重试路径。

#### 债五：缺少中心化策略层

`hard bool`（DELETE 语义）、`SubBufferSize`（SSE 缓冲深度）、`DedupeKey` 格式（复制去重策略）、`RENAME/MOVE` 支持（协议能力矩阵）——这些分散的决策点缺少一个统一的**策略配置层**。每个新功能都需要修改 3-4 个分散位置。

### 1.3 架构债务总结

```
当前架构状态
━━━━━━━━━━━━━
Service Layer (FileService)
    ↓ 3 event types
EventBus (64 deep channel, drop on full)
    ↓ to subscribers (all treated equally)
    ├── Replication (only EventCreated)
    ├── Webhook (all types)
    ├── SSE (per-conn subscriber, global unconsumed replay)
    ├── Antivirus (via JobPool)
    └── Indexer (via JobPool)
        ↓
JobPool (all errors → retry → dead letter)
```

关键缺失：
1. **无"精确副本"语义**的复制契约
2. **无客户端专属游标**的 SSE 订阅模型
3. **无策略抽象**的协议差异处理
4. **无错误分类**的 Job 执行契约

---

## 2. 扩展方向

### 2.1 复制全生命周期（P0 — 立即执行）

**为什么需要**：

"复制"是对象存储的灾备基石。当前实现中，副本与主站之间的差异会随时间单调增长：

```
时间 →    主站                   副本
t0        PUT doc.txt            PUT doc.txt ✅
t1        PUT doc.txt (更新)      空 (dedupe key 阻挡) ❌
t2        DELETE doc.txt         副本残留 doc.txt ❌
t3        存储空间 10GB           存储空间 20GB (残留) ❌
```

灾备切换时副本包含大量过期数据，**切换即灾难**。

**核心挑战和技术难点**：

| 挑战 | 难度 | 说明 |
|------|------|------|
| 事件顺序保证 | 🟠 中 | 并发写时，EventCreated(seq=1) 和 EventDeleted(seq=2) 可能乱序到达副本 worker |
| 幂等删除 | 🟢 低 | `storage.Delete` 已幂等（`ErrNotFound` 静默忽略） |
| 版本化桶删除 | 🟠 中 | 删除最新版本 vs 删除指定版本——副本需知道具体版本号 |
| Lifecycle 过期删除缺口 | 🔴 高 | `lifecycle.go` 直接调 `store.Delete` + `repo.HardDeleteObject`，**不发 EventDeleted**——复制 worker 不知道对象已被 lifecycle 删除 |
| 初始全量同步（新建复制目标） | 🔴 高 | 需要可恢复的批处理遍历 + 断点续传；500+ 行 |

**预期的架构变更**：

```
replication.Worker.Run 当前：
    EventCreated → enqueue JobReplicate
    EventDeleted → continue (skip)

变更后：
    EventCreated → enqueue JobReplicate (dedupe key 含 version_id)
    EventDeleted → enqueue JobDelete (新 job type)
    lifecycle 触发删除 → 也发 EventDeleted
```

需要三处改动：
1. `replication.go:78` — 增加 `EventDeleted` 分支
2. `replication.go:85` — dedupe key 加入 `versionID` 或 `updatedAt` 时间戳
3. `internal/reconcile/lifecycle.go` — lifecycle 删除补发 `EventDeleted`

**对现有系统的影响**：

- **最低影响**：只修改 `replication.go` + `lifecycle.go`，不涉及接口变更
- **向后兼容**：新增 `JobDelete` job type，旧版本 worker 遇到未知 job type 会记 `FailJob`——但 `replication.Worker` 和 `jobs.Pool` 在同一进程中，升级后自动生效
- **风险点**：lifecycle 补发 EventDeleted 会产生额外事件——现有的 SSE 和 webhook 消费者也会收到，需确认他们的处理逻辑（当前这些消费者应已处理 EventDeleted，只是复制没处理）

### 2.2 跨协议语义收敛（P1 — 第二优先）

**为什么需要**：

四协议架构是 aero-vault 的核心差异化优势。但当相同操作在不同协议上产生不同效果时，这个优势变成了用户的困惑源。DELETE 是最严重的不一致：

| 协议 | 用户预期 | 实际行为 | 惊讶程度 |
|------|---------|---------|---------|
| S3 DELETE | 永久删除（S3 标准语义） | ✅ 硬删除 | 🟢 符合预期 |
| REST DELETE | 删除对象 | ❌ 软删除（保留版本） | 🟡 "删了但存储没释放" |
| Finder (WebDAV) | 移入废纸篓 | ❌ 软删除 | 🟢 符合 Finder 用户预期 |
| MCP delete_file | 与 REST 一致 | ❌ 依赖 REST 后端 | 🟡 不透明 |

**核心挑战和技术难点**：

| 挑战 | 难度 | 说明 |
|------|------|------|
| 桶级别策略化 DELETE | 🟢 低 | 在 `BucketConfig` 中增加 `DeleteMode` 字段（`soft`/`hard`/`versioned`） |
| 向后兼容性 | 🟠 中 | 现有 S3 用户依赖硬删除行为——改变默认值会破坏现有工作流 |
| RENAME 原子性 | 🔴 高 | WebDAV 的 `Rename` 是 Get→Put→Delete——非原子，中间失败导致数据丢失 |
| RENAME 与版本控制 | 🟠 中 | rename 后是否携带版本历史？10 个版本的旧 key → 新 key 下也应 10 个版本 |
| 条件请求统一 | 🟠 中 | REST(`conditional.go`) 和 S3(`s3compat/conditional.go`) 用两套独立实现验证条件请求，行为可能漂移 |

**预期的架构变更**：

```
当前状态：
    hard bool 参数在每次调用时由 handler 决定
    BucketConfig 中无 DeleteMode

目标状态：
    BucketConfig.DeleteMode ∈ {soft, hard, versioned}
    S3 DELETE → 查 BucketConfig.DeleteMode
    REST DELETE → 查 BucketConfig.DeleteMode
    两端统一
    
    新增 REST endpoint: POST /v1/files/*/rename
    新增 MCP tool: rename_file
    条件请求统一到 service 层: CheckPreconditions()
```

**对现有系统的影响**：

- DELETE 语义统一：如果默认改为 bucket 配置驱动，现有用户（BucketConfig 无 DeleteMode）应保持原行为（S3=hard, REST=soft）——通过配置默认值隐式决定
- RENAME：新增端点不影响现有 API。WebDAV 已有实现可复用
- 条件请求统一：重构后两个协议 handler 调同一个 `service.CheckPreconditions`，减少维护负担。需线性验证 REST 和 S3 的条件语义等价

**选项对比**：

| 选项 | 工作量 | 风险 | 评价 |
|------|--------|------|------|
| A. 统一 DELETE 为 hard（S3 标准） | 小 | 高 | REST 用户丢失软删除保护，向后不兼容 |
| B. 统一 DELETE 为 soft（REST 标准） | 小 | 高 | S3 用户不符合 AWS 标准 |
| C. BucketConfig.DeleteMode（推荐） | 中 | 低 | 每个 bucket 可配置，默认值保持旧行为 ✅ |

### 2.3 SSE 事件流韧性（P1 — 第三优先）

**为什么需要**：

SSE 是 Web UI 的实时更新通道和 Agent 的异步通知机制。当前实现有三个根本性缺陷：

1. **64 深缓冲溢出丢事件**：`bus.go:30` 的 `defaultSubBuffer = 64` 太小——一个 SSE 连接在 GC pause 或网络抖动时，64 个事件瞬间填满
2. **重连回放不精确**：`sse.go:44` 用 `NextUnconsumedEvents` 返回**全局** unconsumed 事件而不是该客户端未收到的事件
3. **SDK 无退避重连**：服务器重启时所有客户端同时重连形成 thundering herd

**核心挑战和技术难点**：

| 挑战 | 难度 | 说明 |
|------|------|------|
| 客户端专属游标 | 🟢 低 | 新增 `sse_subscriptions` 表 + `last_event_id` 字段；重连时 SELECT WHERE id > last_id |
| SSE 缓冲深度 | 🟢 低 | `bus.Subscribe()` 已接受 SubBufferSize 配置参数（`NewWithBuffer`），但 SSE handler 未传递它 |
| SDK 指数退避 | 🟢 低 | 每个 SDK ~20 行，加入随机 jitter |
| 消费者分级 | 🟠 中 | 将 subscriber 分为"重要"(webhook/replication — 阻塞发送) 和"可丢"(SSE — 当前行为)。需要 `SubscribeImportant()` 与 `Subscribe()` 两种订阅方法 |
| 历史回放长度限制 | 🟢 低 | `replayMissed` 的 limit=200 可配置化 |

**预期的架构变更**：

```
当前 SSE 重连路径：
    Last-Event-ID: N
    → repo.NextUnconsumedEvents(200)  [全局 unconsumed]
    → 过滤 e.ID > N
    → 发送

改进后 SSE 重连路径：
    Last-Event-ID: N
    → repo.NextEventsAfter(tenant, lastEventID, limit)  [该 tenant 所有事件]
    → 发送
```

**对现有系统的影响**：

- **最低影响**：新增 `sse_subscriptions` 表 + 迁移文件；`sse.go` 修改 replay 逻辑
- **向后兼容**：重连时如果 `Last-Event-ID` = 0，回退到当前行为（recent unconsumed）
- **存储增长**：`sse_subscriptions` 表每条连接一条记录，连接断开后 cleanup

### 2.4 Job 错误语义分类与时序缺口修补（P2 — 第四优先）

**为什么需要**：

创建后快速删除导致的 `ErrNotFound` 是事件驱动系统经典的时序竞争问题。当前 JobPool 对所有 error 同等重试，导致死信队列被"无害故障"污染——运维人员需要手动区分真正的错误和这种时序噪声。

**核心挑战和技术难点**：

| 挑战 | 难度 | 说明 |
|------|------|------|
| 不可重试错误契约 | 🟢 低 | 定义 `ErrSkip` sentinel error，JobPool 判断：如果 handler 返回 `ErrSkip` → Complete 而非 Retry |
| 删除事件吞没创建事件 | 🟠 中 | 事件总线上如果队列中已有同一 ObjectID 的未处理 `JobReplicate`，收到 `EventDeleted` 时从队列中移除待处理的 Job |
| 软删除 vs 硬删除 | 🟢 低 | 软删除保留历史，`GetObjectByID` 能找到行（`deleted_at` 非空）；只有硬删除（删除行）才是真正的 `ErrNotFound` |

**预期的架构变更**：

```
当前：
    handler(ctx, job) → error
    job.Pool.runOne: if error → Retry or Fail

改进后：
    type JobResult int
    const (
        Success     JobResult = iota
        Skip                  // 无害错误，Complete
        Retryable             // 临时故障，Retry
        Fatal                 // 永久故障，Fail
    )
    
    或更简单的方案：
    var ErrSkip = errors.New("skip: not an error")
    handler 返回 ErrSkip → Pool 直接 CompleteJob
```

**对现有系统的影响**：

- **最低影响**：只修改 `jobs.go:runOne` + 三个 handler（replication/AV/indexer）增加 `ErrSkip` 判断
- **向后兼容**：所有现有 handler 不返回 `ErrSkip`，行为不变

### 2.5 CLI 运维成熟度（P2 — 第五优先）

**为什么需要**：

CLI 是 DevOps 工程师的日常工具。六个文档化的 BUG 和 `--json` 缺失严重限制了 CLI 在 CI 管道中的使用。修复成本极低但用户体验提升显著。

**核心挑战**：

| 挑战 | 难度 | 说明 |
|------|------|------|
| HTTP 状态码检查 | 🟢 低 | 每个 handler 加 `if resp.StatusCode >= 400` 分支 |
| `--json` 输出 | 🟢 低 | 检测 `--json` flag，输出 JSON 格式（`repository.Object` 已有 JSON tags） |
| 退出码规范化 | 🟢 低 | `ExitOK=0, ExitError=1, ExitNotFound=2` 等 |
| 分页聚合 | 🟠 中 | `cmdList` 当前只请求一页（limit=50），`--json` 模式下应自动翻页 |

**对现有系统的影响**：

- **无影响**：CLI 修改不影响服务端。`--json` 标志是新增行为，不影响文本输出
- **向后兼容**：不指定 `--json` 时输出格式不变

---

## 3. 接口设计建议

### 3.1 核心原则

| 原则 | 说明 |
|------|------|
| **策略与实现分离** | `hard bool` 不应由 handler 决策，应由 BucketConfig 或租户策略配置决定。增加 `BucketConfig.DeleteMode` |
| **错误语义分类** | Job handler 不应只返回 `error`，应返回 `(error, skipable bool)` 或使用哨兵 error 区分可重试与不可重试错误 |
| **消费者身份化** | SSE 连接的 subscriber 不应匿名——需要 `SubscribeWithID(id string)` 以支持客户端专属游标 |
| **事件契约明确化** | `EventDeleted` 的 payload 应携带 `hard bool` 字段，使消费者（SSE/replication/webhook）知道是软删除还是硬删除 |

### 3.2 是否需要新的抽象层

#### 建议一：Job Handler 返回值语义化

当前 `jobs.Handler` 签名：

```go
type Handler func(ctx context.Context, job repository.Job) error
```

建议改为：

```go
// Option A — 哨兵 error
var ErrSkip = errors.New("skip job (not an error)")
var ErrFatal = errors.New("permanent failure, do not retry")

// Option B — 结果类型（更显式）
type JobResult int
const (
    Success    JobResult = iota
    Skip                // 完成任务但不视为成功（无害跳过）
    Retryable           // 临时故障，需重试
    Fatal               // 永久故障，直接死信
)
type HandlerResult struct {
    Err     error
    Result  JobResult
}
type Handler func(ctx context.Context, job repository.Job) HandlerResult
```

**推荐 Option A**：哨兵 error 方式侵入性更小，现有 handler 无需改动。`jobs.go:runOne` 中加：

```go
if errors.Is(runErr, ErrSkip) {
    _ = p.repo.CompleteJob(ctx, job.ID, runErr.Error())
    return true, nil
}
```

#### 建议二：SSE 消费者游标持久化

新增 `sse_subscriptions` 表（或复用 `object_events` 的 last_read_id 机制）：

```
sse_subscriptions:
    id          TEXT PRIMARY KEY  (uuid or client-provided)
    tenant_id   TEXT NOT NULL
    last_event_id BIGINT NOT NULL DEFAULT 0
    created_at  TIMESTAMP NOT NULL
    updated_at  TIMESTAMP NOT NULL
```

`SSEHandler.Stream` 接受 `X-SSE-Subscription-Id` header（或自动生成 UUID）。重连时用 subscription_id 查找 `last_event_id` 而非解析 `Last-Event-ID` header。

#### 建议三：复制策略配置化

当前复制策略硬编码在 `replication.go`：

```go
if e.Type != repository.EventCreated || e.ObjectID == nil {
    continue
}
```

建议在 Bucket 级别定义复制规则：

```go
type ReplicationRule struct {
    ID             string
    Status         string  // "Enabled" | "Disabled"
    Destination    string  // 目标 backend 标识
    EventTypes     []string // ["created", "deleted"] — 可配置
    PrefixFilter   string  // 可选前缀过滤
}
```

但这属于**远期扩展**。当前阶段直接在 worker 中订阅 `EventDeleted` 即可——P0 修复不应等待策略层完善。

### 3.3 向后兼容性策略

| 变更 | 兼容策略 |
|------|---------|
| DELETE 语义统一 | 未设置 `BucketConfig.DeleteMode` 时保持原行为（S3=hard, REST=soft） |
| Job handler 返回值语义化 | Option A（哨兵 error）完全向后兼容：现有 handler 不返回 `ErrSkip`/`ErrFatal`，行为不变 |
| SSE 消费者游标 | `Last-Event-ID` header 仍受支持；新 subscription_id 方式并行存在 |
| 新 job type `JobDelete` | 旧版本 worker 遇到未知 job type 会记 `FailJob`——新版本无需考虑；单进程升级无此问题 |

---

## 4. 技术选型

### 4.1 是否需要新依赖

| 方向 | 需求 | 建议 |
|------|------|------|
| 复制完整性 | 无新依赖 | 纯代码改动，复用 `storage.Storage` 接口 |
| SSE 持久游标 | 无新依赖 | `sse_subscriptions` 表 + SQL 查询 |
| 跨协议语义 | 无新依赖 | `BucketConfig` 扩展字段 |
| CLI 成熟度 | 无新依赖 | 标准库 JSON 编码 |
| Job 错误分类 | 无新依赖 | 哨兵 error 模式 |

**结论**：**五个方向均不需要引入新的外部依赖**。这是"架构重构"而非"技术栈替换"——前提是现有抽象层足够（确实足够）。

### 4.2 评估框架

如果未来需要引入新依赖，建议使用以下评估标准：

| 维度 | 问题 | 门槛 |
|------|------|------|
| **必要性** | 该依赖解决了什么标准库/现有代码无法解决的问题？ | 必须明确论证 |
| **成熟度** | 是否有活跃维护？Go/Python/JS 社区认可度？ | GitHub Stars > 1K + 近 6 月有 release |
| **许可证** | 是否与项目（Apache 2.0？）兼容？ | GPL 类排除 |
| **依赖深度** | 自身依赖数量？| 传递依赖 ≤ 10 |
| **ABI 稳定性** | Major version 是否承诺向后兼容？ | v1+ |
| **审计可行性** | 代码行数？核心文件数？| 核心 ≤ 5 文件，≤ 2K 行 |

### 4.3 自建 vs 采购的决策矩阵

| 功能 | 自建成本 | 采购/集成成本 | 建议 |
|------|---------|-------------|------|
| 复制完整性 | ~300 行 | N/A（核心特性） | ✅ **自建** |
| SSE 持久游标 | ~200 行 | N/A（核心特性） | ✅ **自建** |
| SDK 指数退避 | ~60 行（三套 SDK） | 使用 `fetch-retry` / `backoff` 库 | ✅ **自建**（简单到不值得引入依赖） |
| 全量同步（初始复制） | ~500 行 | 使用 `rsync` 模式？ | ✅ **自建**（与 aero-vault 存储耦合） |

**结论**：五个方向均适合自建。不引入新依赖。

---

## 5. 实施路线图

### 5.1 优先级排序与阶段划分

```
时间线
━━━━━━━
Q3 2026 (Phase 1: Foundations)          Q4 2026 (Phase 2: Reliability)    Q1 2027 (Phase 3: Maturity)
───────────────────────────────        ─────────────────────────────     ─────────────────────────────
P0: 复制完整性                          P1: SSE 流韧性                    P2: CLI 成熟度
  ├─ EventDeleted 复制                    ├─ SSE 专属游标表                  ├─ HTTP 状态码修复
  ├─ Dedupe key 修复                      ├─ 缓冲深度可配置化                ├─ --json 标志
  └─ Lifecycle 补发事件                    ├─ SDK 指数退避                   ├─ 退出码规范化
                                          └─ 消费者分级（远期）              └─ 分页聚合
P1: DELETE 语义统一                      P2: 时序缺口修补
  ├─ BucketConfig.DeleteMode               ├─ ErrSkip 哨兵 error
  ├─ S3 适配新策略模式                      ├─ 三个 handler 适配
  └─ REST 适配新策略模式                    └─ 删除事件吞没创建事件（远期）
  
P2: 时序缺口修补（quick win）
  └─ ErrSkip 哨兵 error
```

### 5.2 详细里程碑

#### Phase 1a: 复制修复（P0 — 1-2 周）

| 步骤 | 文件 | 工作量 |
|------|------|--------|
| 1. replication.Worker.Run 增加 EventDeleted 分支 | `replication.go:78` | ~30 行 |
| 2. 新增 `JobDelete` 常量 + `DeleteFromReplica` handler | `replication.go`（新方法） | ~80 行 |
| 3. Dedupe key 加入 `updated_at` 时间戳 | `replication.go:85` | ~10 行 |
| 4. Lifecycle 补发 EventDeleted | `reconcile/lifecycle.go` | ~20 行 |
| 5. 测试：幂等删除、乱序事件、版本化桶 | `replication_test.go` | ~100 行 |
| 6. `make check` 全绿 | — | — |

**风险**：lifecycle 补发 EventDeleted 可能被 webhook/SSE 消费者重复处理——需要测试确认当前消费者对重复 EventDeleted 的容忍度。

#### Phase 1b: DELETE 语义统一（P1 — 1 周）

| 步骤 | 文件 | 工作量 |
|------|------|--------|
| 1. BucketConfig 增加 `DeleteMode` 字段 | `repository/repository.go` | ~5 行 |
| 2. 迁移：新增 `delete_mode` 列（双文件） | `migrations/{sqlite,postgres}/` | ~20 行 |
| 3. S3 handler 删除时查 BucketConfig | `s3compat/handler.go:258` | ~10 行 |
| 4. REST handler 删除时查 BucketConfig | `rest/handler.go:310-320` | ~10 行 |
| 5. 向后兼容默认值 | 初始化逻辑 | ~10 行 |

**风险**：默认值策略（S3=hard, REST=soft）需要确认是否维持——如果改为统一的 BucketConfig 驱动，现有用户的迁移路径必须明确

#### Phase 2a: 时序缺口修补（P2 quick win — 3 天）

| 步骤 | 文件 | 工作量 |
|------|------|--------|
| 1. 在 `repository` 或 `jobs` 包中定义 `ErrSkip` | `jobs/errors.go` | ~5 行 |
| 2. `jobs.go:runOne` 增加 `ErrSkip` 判断 | `jobs/jobs.go` | ~5 行 |
| 3. 复制 handler 返回 `ErrSkip` 当 `ErrNotFound` | `replication.go:107` | ~5 行 |
| 4. AV handler 返回 `ErrSkip` 当 `ErrNotFound` | `antivirus/worker.go:41` | ~5 行 |
| 5. 索引器 handler 返回 `ErrSkip` 当 `ErrNotFound` | `ai/indexer.go` | ~5 行 |

#### Phase 2b: SSE 流韧性（P1 — 2 周）

| 步骤 | 文件 | 工作量 |
|------|------|--------|
| 1. 新增 `sse_subscriptions` 表 + 迁移 | `repository/` + `migrations/` | ~30 行 |
| 2. Repository 新增 `NextEventsAfter` 方法 | `repository/repository.go` | ~30 行 |
| 3. SSE handler 重连逻辑改用 subscription_id | `rest/sse.go:44` | ~50 行 |
| 4. `replayMissed` 限 200 → 配置化 | `rest/sse.go:44` | ~10 行 |
| 5. SDK 指数退避 + 连接超时检测 | 三套 SDK | ~60 行 |
| 6. SubBufferSize 从 Bus 传递到 handler | `rest/sse.go:69` | ~10 行 |

**风险**：sse_subscriptions 表在低活跃连接上的积累——需要 cleanup 策略（TTL + Reaper）

#### Phase 3: CLI 成熟度（P2 — 1 周）

| 步骤 | 文件 | 工作量 |
|------|------|--------|
| 1. 修复 5 个 HTTP 状态码 BUG | `cli_crud.go`, `cli_search.go` 等 | ~30 行 |
| 2. 修复 cmdSnapshot stat error swallowing | `cli_snapshot.go` | ~5 行 |
| 3. 添加 `--json` 全局标志 | `cli.go` + 各 handler | ~80 行 |
| 4. 退出码规范化 | `cli.go:Run` | ~20 行 |
| 5. 分页聚合（`cmdList --json` 自动翻页） | `cli_crud.go` | ~30 行 |

### 5.3 风险矩阵与缓解策略

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| **复制事件顺序**：并发创建+删除导致乱序 | 🟠 中 | 🟠 中 | 在复制 handler 中检查 `updated_at`：如果对象的 `updated_at` 晚于事件的 `created_at`，跳过该事件（已有更晚的操作） |
| **Lifecycle 补发事件重复处理** | 🟢 低 | 🟡 低 | 复制 handler 已幂等；Webhook/SSE 的 EventDeleted 处理逻辑需确认 |
| **SDK 退避参数与后端匹配** | 🟢 低 | 🟢 低 | SDK 退避最大间隔 30s，服务器重启窗口通常 < 10s——不会产生不必要的 long poll |
| **SSE 订阅表膨胀** | 🟡 低 | 🟡 低 | 新增 Reaper 清理超过 24h 未更新的 subscription 行 |
| **DELETE 默认值变更破坏现有用户** | 🟠 中 | 🔴 高 | **明确原则**：BucketConfig 无 DeleteMode 时各协议保持原行为。仅新 bucket 或用户显式配置后行为统一 |
| **CLI --json 输出格式争议** | 🟢 低 | 🟢 低 | 复用 `repository.Object` 已有的 JSON tags，保持与 REST API 响应格式一致 |
| **多方向并行修改导致冲突** | 🟡 中 | 🟡 中 | Phase 1a/1b/2a 修改的文件无重叠（replication.go / repository.go / jobs.go）；Phase 2b 修改 sse.go，与 1a/1b 无关 |

### 5.4 不做这些方向的代价量化

| 方向 | 6 个月后 | 12 个月后 |
|------|---------|----------|
| 复制完整性 | 副本膨胀 2x（积累已删除对象）；灾备切换时存储超卖 50% | 副本膨胀 3-10x；灾备切换时间因数据量过大致使 RTO 超限 |
| SSE 流韧性 | Web UI 用户每周错过 5-10 次事件通知；events_dropped_total 日均 200+ | 运维建立"SSE 不可靠"的心理预期，放弃对实时特性的依赖 |
| DELETE 语义不一致 | 用户论坛 3-5 个 "为什么 DELETE 在两个地方行为不同？" 帖子 | 产品 reviews 列为"平台完整性"扣分项 |
| CLI BUG | CI 管道中 2-3 次静默失败 debug 事件（每次 2-4 小时） | CI 工程师将 aero-vault CLI 列为"不可靠工具" |
| 时序缺口 | 死信队列中 15% 是 ErrNotFound 噪声；1-2 次误告警 | 运维团队建立"检查死信队列中的 ErrNotFound 并手动清除"的 SOP |

---

## 总结：执行清单

```
优先级  方向                       工作量   依赖关系
──────────────────────────────────────────────────────
P0      复制 EventDeleted + dedupe  ~140行   无
P0      Lifecycle 补发 EventDeleted ~20行    上游（依赖 lifecycle 模块）
P1      DELETE 语义统一              ~55行    无     
P1      SSE 持久游标                ~130行   无（迁移文件 + 代码）
P1      SDK 退避重连                ~60行    与 SSE 游标无关
P2      ErrSkip 哨兵 error          ~25行    无
P2      CLI Bug 修复                ~165行   无
```

**关键洞察**：五个方向中四个（除 CLI 外）都涉及同一个核心问题——**事件驱动系统的可靠性契约不完整**。复制只订阅了部分事件、SSE 缺乏客户端游标、JobPool 不区分错误类型、时序窗口无保护——这些是同一个问题在不同层的不同表现。

建议在处理具体方向之前，先建立**事件契约文档**：

```markdown
# 事件契约

EventCreated: payload = {object_id, tenant, bucket, key, version_id, versioning_enabled}
  - 保证：新对象已写入存储 + 元数据已持久化
  - 消费者：复制(必须)、索引器(必须)、防病毒(必须)、Webhook(可选)、SSE(可选)

EventDeleted: payload = {object_id, tenant, bucket, key, version_id, hard: bool}
  - 保证：对象已标记删除（hard=true 时存储已清理，hard=false 时元数据标记 deleted_at）
  - 消费者：复制(必须)、Webhook(可选)、SSE(可选)
  - 注意：hard=false 时 GetObjectByID 仍可找到行（deleted_at 非空）
          hard=true 时 GetObjectByID 返回 ErrNotFound

EventAccessed: payload = {object_id, tenant, bucket, key}
  - 保证：对象已被读取
  - 消费者：无（当前无必要消费者，仅 SSR 使用）
```

这个契约文档明确了每个消费者**必须处理**哪些事件、以及**在对象被删除后 `GetObjectByID` 的行为**——解决了方向五中的 ErrNotFound 歧义问题。
