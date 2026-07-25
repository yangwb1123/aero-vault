# AeroVault 高价值扩展方向（第九期）

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全局代码扫描（~46.7K 行 Go 源码，覆盖 internal/ 全部 23 个子包），逐一审阅 `ROADMAP.md`、八轮 `analysis-v[1-8]-gaps-roadmap.md`、七期 `expansion-directions[-v2..v8]`、`CHANGELOG.md`、`TODO.md`、`HARNESS.md` 及全部 Migration SQL（0001–0024）。确认每个方向在**所有既有文档中零覆盖或仅骨架提及**。  
> **日期：** 2026-07-10  
> **原则：** 选取 5 个**既有文档从未系统讨论**的工程架构方向。每个方向附带：代码锚点、当前状态 vs 理想状态、边界情况、架构蓝图、实现理由。**不编写任何实现代码。**

---

## 审阅摘要

八期 expansion 文档与 ROADMAP 已覆盖极广的范围。执行"差异分析"后，以下类别被高频覆盖（*不再选入本期*）：

| 类别 | 覆盖期数 | 状态 |
|------|---------|------|
| S3 兼容性（Policy/CORS/Logging/Notification CRUD） | v8, ROADMAP #7 | 已实现存储+CRUD，部分未接入执行 |
| 内容去重/CAS | v7 #1 | 未实现 |
| 浏览器直传 / S3 POST | v7 #2 | 未实现 |
| 计费/用量 | v7 #3 | 未实现 |
| Resumable Upload / TUS | v7 #4 | 未实现 |
| 结构化元数据 Schema | v7 #5 | 未实现 |
| SSE 韧性/事件过滤 | v6 #1 | 未实现 |
| WORM 跨协议一致 | v6 #2 | 部分实现，MOVE 路径有缺口 |
| 内容智能/格式转换 | v6 #3 | 未实现 |
| 优雅降级/特性开关 | v6 #4 | `DegradedMode` 是存根 |
| 分层限流 | v6 #5 | 未实现 |
| Postgres 连接池/Read Replica | v8 #2 | 未实现 |
| 生产级备份/DR | v8 #3 | 未实现 |
| DLP 框架 | v8 #4 | 未实现 |
| 跨协议并发一致性 | v8 #5 | 未实现 |
| 内容完整性/校验和 | ROADMAP #8 | 未实现 |
| 存储分层/生命周期 | ROADMAP #9 | 部分实现（class 字段存在，无 transition worker） |
| Metadata HA/DR | ROADMAP #10 | 未实现 |

---

## 本期方向总览

| # | 方向 | 类型 | 影响 | 核心代码锚点 | 既有文档覆盖 |
|---|------|------|------|-------------|-------------|
| 1 | **S3 Event Notifications 执行引擎：SQS/SNS/Lambda 投递** | 兼容/生态 | 🔴 既有骨架但完全不执行 | `internal/repository/repository.go:58-64`, `internal/api/s3compat/handler.go:786-810`, `internal/events/webhook.go` | 0 覆盖 |
| 2 | **对象变更数据捕获（CDC）流：可回放的有序变更日志** | 平台/集成 | 🔴 外部 ETL/分析管线断裂 | `internal/repository/sql_events.go`, `internal/api/rest/sse.go`, `internal/events/bus.go` | 0 覆盖 |
| 3 | **多区域 Active-Active 复制与冲突检测框架** | 架构/可靠性 | 🔴 单区域=业务连续性天花板 | `internal/replication/replication.go`, `internal/jobs/jobs.go`, `internal/storage/storage.go` | 0 覆盖 |
| 4 | **沙箱化事件触发器：用户自定义函数（WASM）** | 差异/平台 | 🟠 从"存储"到"计算平台" | `internal/repository/repository.go:64`（`LambdaARN` 存根）, `internal/events/bus.go` | 0 覆盖 |
| 5 | **对象生命周期治理与合规框架：保留调度、法律保全、处置证书** | 合规/治理 | 🟠 金融/医疗/政务市场准入 | `internal/reconcile/retention.go`, `internal/service/file_crud.go:hardDeleteObject`, `internal/repository/repository.go:Object` | 0 覆盖 |

---

## 1. S3 Event Notifications 执行引擎：SQS/SNS/Lambda 投递

### 当前状态

**骨架存在，完全空转。** Notification Rules（migration 0024）的 CRUD 已经完整实现——存储、获取、删除、S3/XML 序列化全部就绪。但 `QueueARN`、`TopicARN`、`LambdaARN` 三个目标字段被代码注释明确标记为 `"unused, kept for compat"`。

**既有（但空转）：**
```go
// internal/repository/repository.go:57-64
type NotificationRule struct {
    ID        string   `json:"Id"`
    Events    []string `json:"Events"`
    Filter    *Filter  `json:"Filter,omitempty"`
    QueueARN  string   `json:"QueueArn,omitempty"` // unused, kept for compat
    TopicARN  string   `json:"TopicArn,omitempty"` // unused, kept for compat
    LambdaARN string   `json:"LambdaFunctionArn,omitempty"`  // unused, kept for compat
}
```

**实际的事件投递管线（仅 Webhook）：**
```
event bus → internal/events/webhook.go → HTTP POST (仅一种) → 单一 webhook URL
```

**缺失的投递通道（S3 规范应有）：**
```
event bus → SQS queue (AWS API/SDK)
event bus → SNS topic (HTTP/HTTPS subscription)
event bus → Lambda function (AWS Invoke API)
event bus → custom HTTP endpoint (Webhook)
                           ↑ 已实现，但也只有这个
```

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/repository/repository.go:58-64` | `NotificationRule` 定义：`QueueARN`/`TopicARN`/`LambdaARN` | 显式注释 `unused, kept for compat` |
| `internal/repository/repository.go:54` | `BucketConfig.NotificationRules` 字段 | 仅存储，无投递者消费 |
| `internal/repository/repository.go:275-277` | `GetBucketNotifications` / `SetBucketNotifications` / `DeleteBucketNotifications` | 完整的 CRUD |
| `internal/api/s3compat/handler.go:786-810` | XML 序列化/反序列化 | 正确但 wire 未连接 |
| `internal/api/s3compat/handler.go:292` | `case q.Has("notification"):` 路由分发 | 正确处理 GET/PUT/DELETE |
| `internal/events/webhook.go` | `Webhook` 事件消费者（HTTP POST） | 唯一的投递实现 |
| `internal/events/bus.go` | `Bus.Subscribe()` 广播机制 | 可复用（增加 SQS/SNS/Lambda 消费者） |
| `internal/events/bus.go:113-119` | `broadcast` 多播 | 每个消费者独立 goroutine |
| `internal/config/config_app.go:38-39` | `WebhookURL` / `WebhookSecret` 配置 | 无 SQS/SNS/Lambda 配置 |
| `internal/repository/sql_buckets.go:123` | `notification_rules` 从数据集读取 | 正确 |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **SQS 队列不可达** | SQS 队列被删除/权限不足 | 无消费者，通知静默消失 | 重试策略（指数退避）+ 死信队列 + 失败指标 |
| **Lambda 冷启动延迟** | 函数首次调用（~1s+） | 无消费者，事件丢失 | 异步调用 + 不阻塞 event bus |
| **SNS 端点限流** | HTTP/HTTPS 订阅返回 429 | 无重试 | 退避重试（与 webhook 复用基础设施） |
| **过滤器语义** | `Filter` 的 `S3Key` 规则（`prefix`/`suffix`） | 存储但不执行 | 消费者端 Evaluate filter 再投递 |
| **批量事件压缩** | 每秒 1000 个 PUT 事件→1000 次 Lambda 调用 | 可能超过 Lambda 并发限制 | 批量聚合（`S3:TestEvent` + 合并窗口） |
| **跨账户投递** | SQS/Lambda 在另一个 AWS 账户 | 需要跨账户 IAM | 配置层支持角色扮演（`AssumeRole`） |
| **事件类型匹配** | `s3:ObjectCreated:*`, `s3:ObjectRemoved:*` | 正则匹配已就绪（`strings.HasPrefix` 在 XML 序列化中） | 投递前按规则 Events 过滤 |
| **多规则单事件** | 一个事件匹配 3 条规则→3 次投递 | 无去重 | 每条规则独立投递，允许重复 |

### 架构蓝图

```
┌─ 通知投递引擎 ────────────────────────────────────────────────│
│ 新增包: internal/events/delivery/                                │
│                                                                  │
│ type DeliveryTarget interface {                                  │
│     Deliver(ctx context.Context, event Event, rule NotificationRule) error │
│     Name() string                                                │
│ }                                                                 │
│                                                                  │
│ 实现:                                                             │
│   webhookDelivery  — 重用现有的 HTTP POST 逻辑（已就绪）          │
│   queueDelivery    — AWS SQS SendMessage API（新增）              │
│     → 需要 AWS SDK 依赖或最小化 HTTP API 调用                     │
│   topicDelivery    — SNS Publish API（HTTP/HTTPS 订阅）          │
│     → AWS SNS API 调用或用 HTTP POST 模拟                        │
│   lambdaDelivery   — Lambda Invoke API（新增）                   │
│     → AWS Lambda API 调用                                        │
│                                                                  │
│ 规则评估:                                                         │
│   新的订阅者: events.NewNotificationDispatcher(repo, targets)    │
│     订阅 bus 的所有事件                                           │
│     每个事件:                                                    │
│       1. repo.GetBucketNotifications(tenant, bucket)             │
│       2. 遍历规则: 匹配 Events[] → 检查 Filter → 按目标类型分发   │
│       3. 异步投递（goroutine per target + per-rule）             │
│                                                                  │
│ 指标:                                                             │
│   notification_delivery_total{target_type, bucket, status}       │
│   notification_delivery_duration_ms{target_type}                 │
│   notification_delivery_retries_total{target_type}               │
│                                                                  │
│ 配置:                                                             │
│   无全局配置——每个 bucket 的规则独立定义                           │
│   （复用现有的 notification_rules 配置结构，不新增字段）            │
└────────────────────────────────────────────────────────────────┘

┌─ 依赖管理 ─────────────────────────────────────────────────────│
│ AWS SDK 策略:                                                     │
│   选项 A: 添加 `github.com/aws/aws-sdk-go-v2` 为可选依赖            │
│     优点: 完整签名 + 错误处理                                     │
│     缺点: 依赖膨胀（~5MB）                                        │
│   选项 B: 手动构造 HTTP 签名请求（SigV4）                          │
│     优点: 零新依赖                                               │
│     缺点: 需要维护内部 SigV4 签名                                 │
│     现状: internal/auth/sigv4.go 已有完整的 SigV4 实现              │
│     结论: 选项 B 可行——复用 SigV4 为 SQS/SNS/Lambda 签名          │
│                                                                  │
│ 自制签名示例（基于现有 sigv4.go 的能力）:                           │
│   service="sqs" / region=cfg.AWSRegion / body=jsonPayload        │
│   Authorization: AWS4-HMAC-SHA256 ...                            │
│   用内部 Signer 计算签名，无需 AWS SDK                              │
└────────────────────────────────────────────────────────────────┘

**为什么现在做：** 这是 S3 兼容性最显眼的**骨架半成品**——通知规则的 CRUD 全面就绪但投递通道全部指向 `unused`。对于使用 S3 事件驱动架构（ETL pipeline、实时处理、异步工作流）的用户来说，这是第一个会注意到的缺口。实现成本低（SQS/SNS/Lambda 的投递逻辑各约 150 行，复用现有 SigV4 引擎），但生态价值极高。

| 影响面 | 工作量估计 |
|--------|-----------|
| DeliveryTarget 接口 + dispatcher | 中 |
| queueDelivery（SQS/SigV4） | 中 |
| topicDelivery（SNS/SigV4） | 中 |
| lambdaDelivery（Lambda/SigV4） | 中 |
| 规则过滤（Events/Filter 匹配） | 低 |
| 测试 + 集成测试 | 高 |

---

## 2. 对象变更数据捕获（CDC）流：可回放的有序变更日志

### 当前状态

**事件持久化存在，但无有序、可回放的变更日志暴露给外部消费者。** 当前事件系统：

```
object mutation → event bus → 
   ├─ SSE (live only, 无可靠回放)
   ├─ webhook (push, 无批量回溯)
   ├─ Postgres transport (跨实例, 内部用)
   └─ indexer/AV/replication (内部消费者, 消费后标记 event.consumed)
```

**关键缺口：**

| 能力 | 当前状态 |
|------|---------|
| 有序持久化 | events 表有自增 ID，但外部不可查询 |
| 可回放 | `NextUnconsumedEvents` 被内部消费者标记 consumed |
| 外部消费 | 无公开的尾部-和-偏移量 API |
| 批量回溯 | 无 `GET /v1/events?after=ID&limit=1000` 端点 |
| Schema 演进兼容 | 无 Avro/Protobuf schema registry |
| 多消费者组隔离 | 所有消费者共享 unconsumed 标记 |

**代码证据：**

```go
// internal/repository/sql_events.go
func (s *sqlStore) NextUnconsumedEvents(ctx context.Context, tenant string) ([]Event, error) {
    // 被所有内部消费者共享——一个消费者标记 consumed，其他消费者丢失
    rows, err := s.db.QueryContext(ctx, s.rebind(
        `SELECT ... FROM events WHERE consumed=0 AND tenant_id=$1 ORDER BY id LIMIT 100`), tenant)
```

每个新 SSE 连接调用 `NextUnconsumedEvents`——但 unconsumed 标记可能已被 indexer 或 webhook 清除。这是**消费组隔离缺失**。

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/repository/sql_events.go` | 事件追加 + 标记 consumed + 查询 unconsumed | 无独立的 CDC 游标表 |
| `internal/repository/sql_events.go:InsertEvent` | `INSERT INTO events` 自增 ID | 但无消费者游标（cursor）表 |
| `internal/api/rest/sse.go:replayMissed` | 使用 `NextUnconsumedEvents` | 回放不可靠（被其他消费者标记） |
| `internal/api/rest/sse.go` | SSE 端点 `GET /v1/events/stream` | 无批量事件导出 API |
| `internal/events/bus.go` | 内存广播 | 无持久化游标 |
| `internal/repository/repository.go:Event` | `ID int64`, `Type`, `TenantID`, `Bucket`, `Key` | 无 schema version、无 partition key |
| `internal/api/rest/router.go` | 路由表 | 无 `/v1/events?after=&limit=` 路由 |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **消费者断线后回溯** | CDC 消费者下线 6 小时后重连 | 无机制告知消费者从哪里继续 | `GET /v1/events/cursor/{name}?after=1000` |
| **事件积压** | 生产者持续产生事件，消费者处理慢 | 无背压（bus broadcast 使用 select+default 丢事件） | 持久化积压，允许消费者按自己的速度消费 |
| **多消费者独立进度** | 两个 CDC 消费者读取同一事件流 | 共享 unconsumed 标记→互相干扰 | 每个消费者独立 cursor（`consumer_name + topic`） |
| **历史事件批量导出** | 数据迁移需要导出全部事件 | 只支持 `NextUnconsumedEvents(100)` 限制 | 支持 `?after=0&limit=10000` 的分页遍历 |
| **事件 Schema 变更** | v1 事件有 `bucket` 字段，v2 事件加 `region` | 所有事件同表同结构 | 事件 JSON 字段支持 schema 版本号 |
| **事件分区** | 按 tenant 分区的事件流 | events 表 tenant 字段被索引 | 支持按 tenant 过滤的 CDC cursor |

### 架构蓝图

```
┌─ CDC 消费者游标 ──────────────────────────────────────────────│
│ 迁移 N+1: consumer_cursors 表                                  │
│   consumer_name TEXT NOT NULL    // "etl-pipeline"              │
│   topic         TEXT NOT NULL    // "object-events"             │
│   cursor_id     INT8 NOT NULL    // 已消费的最大 event.ID      │
│   updated_at    TEXT NOT NULL    // RFC3339Nano                 │
│   metadata      TEXT             // JSON: 消费者自定义状态      │
│   PRIMARY KEY (consumer_name, topic)                           │
│                                                                  │
│ API 端点:                                                        │
│   GET /v1/events?after={event_id}&limit={max}&tenant={t}       │
│     → SELECT * FROM events WHERE id > ? AND tenant_id = ?       │
│       ORDER BY id LIMIT ?                                      │
│     → 返回: { events: [...], next_cursor: 10000, has_more: true }│
│                                                                  │
│   POST /v1/events/cursors                                       │
│     {"name": "etl-pipeline", "topic": "object-events"}          │
│     → INSERT OR IGNORE consumer_cursors                         │
│     → 返回: { cursor: 0 }                                       │
│                                                                  │
│   GET /v1/events/cursors/{name}                                 │
│     → 返回游标当前位置                                          │
│                                                                  │
│   PUT /v1/events/cursors/{name}                                 │
│     {"cursor": 10000}  → 推进游标（提交消费进度）               │
│                                                                  │
│ 消费者 API:                                                      │
│   外部 ETL 管线:                                                  │
│     1. POST /v1/events/cursors → 注册消费者                      │
│     2. GET /v1/events?after={cursor}&limit=1000                  │
│     3. 处理事件                                                  │
│     4. PUT /v1/events/cursors/{name} → 推进游标                  │
│     5. 循环 → 步骤 2                                             │
└────────────────────────────────────────────────────────────────┘

┌─ 系统消费者迁移 ──────────────────────────────────────────────│
│ 当前: NextUnconsumedEvents                    → 所有消费者共享    │
│ 迁移后: 每个消费者（indexer, webhook, av）持独立 cursor         │
│                                                                  │
│ 内部消费者的迁移（以 indexer 为例）:                               │
│   1. 启动时: cursor = repo.GetCursor("indexer", "object-events") │
│   2. 轮询: SELECT FROM events WHERE id > cursor ORDER BY id LIMIT │
│   3. 处理完后: repo.AdvanceCursor("indexer", lastEventID)        │
│   4. 互不干扰: indexer cursor ≠ webhook cursor ≠ AV cursor       │
│                                                                  │
│ 这解决了一个长期问题: SSE 连接的 replayMissed 不可靠               │
│   SSE 客户端现在持有一个临时 cursor（连接期间有效）                │
│   断线重连: 从 Last-Event-ID 继续（不再是 NextUnconsumedEvents）  │
└────────────────────────────────────────────────────────────────┘

┌─ 与 Kafka/NATS 的关系 ────────────────────────────────────────│
│ 此方向不要求引入 Kafka 或消息队列。它提供一个基于数据库的 CDC 流： │
│                                                                  │
│ 优势:                                                            │
│   - 零额外依赖（只依赖现有事件表）                               │
│   - 与现有 Auth/Tenant 模型集成                                  │
│   - 每条事件受事件表的事务保护                                   │
│                                                                  │
│ 限制:                                                            │
│   - 事件保留：需要可配置的 CDC 保留期（如 7 天 / 100 万条）       │
│   - 高吞吐：单表可能成为瓶颈（解决方案：events 表按 tenant 分区） │
│   - Postgres 可处理每秒数千条插入，SQLite 适合低吞吐场景          │
│                                                                  │
│ 未来方向: 当 CDC 流量超过单表能力时，可接入 Kafka 作为事件总线    │
│          保留 CDC API 接口，后端可替换                             │
└────────────────────────────────────────────────────────────────┘

**为什么现在做：** 事件持久化的基础设施已经完整（events 表、自增 ID、按 tenant 索引）。缺的只是一个外部可见的、消费者隔离的、可回放的 CDC API。没有它，外部 ETL 系统（数据湖、分析引擎、备份系统）无法可靠地消费变更流。在 AI 平台场景下，CDC 是"数据管道"的基础设施前提交付。

| 影响面 | 工作量估计 |
|--------|-----------|
| consumer_cursors 迁移 + CRUD | 低 |
| 分页事件查询 API | 低 |
| CDC 端点 + OpenAPI | 中 |
| 内部消费者迁移到独立游标 | 中 |
| 事件保留/GC（CDC 表） | 低 |
| SSE replayMissed 修复 | 低 |

---

## 3. 多区域 Active-Active 复制与冲突检测框架

### 当前状态

**复制是单向、一对一的。** `internal/replication/replication.go` 的 `ReplicateObject` 将对象从一个存储后端复制到另一个。没有多区域、没有双向、没有冲突检测。

```go
// internal/replication/replication.go:97
func (w *Worker) ReplicateObjectByID(ctx context.Context, objectID int64) error {
    // 1. 从主存储读取
    // 2. 写入目标存储（单目标）
    // 3. 标记 replicated tag
    // 没有反向复制，没有冲突检测
}
```

**现实世界的复制需求（S3CR 规范 + 企业生产）：**

| 模式 | 现状 |
|------|------|
| 单目标复制 | ✅ 支持（当前唯一模式） |
| 多目标复制 | ❌ 一个对象复制到多个 region |
| 双向复制 | ❌ Region A ↔ Region B |
| 冲突检测 | ❌ LWW（最后写入）导致静默数据丢失 |
| 带条件复制 | ❌ 按标签/前缀/storage class 过滤 |
| 复制指标 | ✅ 基础 replication_total |
| 复制事件 | ❌ 复制的对象不触发目标 region 的事件 |
| 删除标记复制 | ❌ 软删除不跨 region 同步 |
| 复制时间 SLA | ❌ 无延迟指标 |

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/replication/replication.go` | `ReplicateObjectByID` 单目标复制 | 无多目标路由、无双向 |
| `internal/replication/replication.go` | `replication_timestamp` tag | 无 vector clock / 版本向量 |
| `internal/config/config_app.go:52-54` | `ReplicationCfg` 只有一个 `Storage` 目标 | 只能配一个副本 |
| `internal/storage/storage.go` | `Storage` 接口 | 无 `ReplicateTo(ctx, targets)` 批量 |
| `internal/jobs/jobs.go` | JobQueue | 可复用但需支持多 job provider |
| `internal/events/bus.go` | 事件广播 | 复制消费者只订阅 `object.created/deleted` |
| `internal/repository/repository.go:Object` | Object 有 `VersionID` | 无 `ReplicaVersions map[region]version_id` |
| `internal/reconcile/lifecycle.go` | 生命周期 | 无跨区域生命周期协调 |
| `internal/telemetry/metrics.go` | `replication_total` | 无 per-region 延迟/吞吐/冲突指标 |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **双向环形复制** | Region A→B, B→C, C→A | 不支持多目标 | 无限转圈→需要检测并丢弃自己同步的事件 |
| **并发写入冲突** | Region A 和 Region B 同时写入同一 key | 各存各的，最终 LWW 覆盖 | 检测冲突→标记为`CONFLICT`→保留两个版本 |
| **网络分区恢复** | Region A 和 Region B 之间的链路中断 30 分钟 | 中断期间的所有变更丢失 | 中断恢复后批量同步+冲突检测 |
| **复制风暴** | 删除一个对象，复制到所有 region，每个 region 再删除触发反向复制 | 可能产生无限删除循环 | 删除标记携带 source_region，反向复制跳过自身 |
| **严格一致性读** | 用户在 Region A 写入后立即在 Region B 读取 | 可能读到旧版本（无一致性路由） | 可选 Read-After-Write 一致性（路由到主 region） |
| **跨区域版本 ID 冲突** | Region A 和 Region B 各自独立分配 version_id | 同一 key 两个 version_id 冲突 | 版本 ID 包含 region 前缀（`<region>/<local_id>`） |
| **复制风暴 + 删除标记** | 对象被软删除后复制到副本 | 支持删除标记的跨区域传播 | 删除标记必须有 `origin_region` 字段，防止循环 |

### 架构蓝图

```
┌─ 复制配置扩展 ────────────────────────────────────────────────│
│ 复制规则:                                                        │
│   type ReplicationRule struct {                                  │
│       ID              string    // 规则 ID                       │
│       Priority        int       // 优先级（小优先）              │
│       TargetRegions   []string  // 目标 region 列表              │
│       FilterPrefix    string    // 可选前缀过滤                  │
│       FilterTags      map[string]string // 可选标签过滤           │
│       DeleteMarker    bool      // 是否同步删除标记              │
│       SyncDirection   string    // "push" | "push-pull"         │
│       Metrics         bool      // 是否收集复制指标              │
│   }                                                              │
│                                                                  │
│ 新的配置模型:                                                     │
│   配置: YAML/MongoDB 风格的复制规则列表                           │
│   每个 region 配置自己的 Storage 后端                              │
│   启动时读取 `REPLICATION_RULES` 环境变量或配置文件               │
│                                                                  │
│ 复制 Worker 扩展:                                                 │
│   不再只有一个目标。一个 replication 事件遍历所有匹配规则：         │
│     1. 检查 FilterPrefix/FilterTags                              │
│     2. 对每个 TargetRegion:                                      │
│        a. 跳过 origin 自身                                       │
│        b. Push: 使用目标的存储配置写入                            │
│        c. 跳过已经由该 region 同步过来的事件（检查 event log）    │
└────────────────────────────────────────────────────────────────┘

┌─ 冲突检测框架 ────────────────────────────────────────────────│
│ type ConflictDetector struct {                                  │
│     mode string  // "lww" | "version" | "crdt"                  │
│ }                                                                │
│                                                                  │
│ 模式选项:                                                        │
│   LWW（Last Writer Wins）:                                       │
│     默认模式，兼容当前行为                                       │
│     冲突时按 timestamp（X-Aero-Timestamp）决定                   │
│     回退：按 region 优先级排序                                   │
│                                                                  │
│   Version Vector:                                                │
│     每个对象携带 `_aero_version_vector: {A:3, B:2}`             │
│     写入时递增自己的版本号                                        │
│     复制时合并 version vector                                     │
│     检测到冲突（A:3 vs A:2+1 不可比）→ 保留两个版本              │
│     对象状态标记为 `CONFLICT`                                     │
│     管理 API 提供冲突解决端点：                                   │
│       GET /v1/admin/conflicts/{tenant}/{bucket}/{key}            │
│       POST /v1/admin/conflicts/resolve → 选择保留版本             │
│                                                                  │
│   CRDT（推荐复杂场景）:                                           │
│     使用 RGA/OR-Set 等 CRDT                                      │
│     对 map[string]string metadata 可使用 Observed-Remove Map     │
│     对 Tags 可使用 OR-Set                                        │
│     优点: 自动合并，无冲突                                       │
│     缺点: 实现复杂，不兼容 S3 语义                               │
└────────────────────────────────────────────────────────────────┘

┌─ 复制事件防循环 ──────────────────────────────────────────────│
│ 关键问题: 复制触发的事件在目标 region 再次触发复制→无限循环        │
│                                                                  │
│ 解决方案: 事件携带复制来源信息                                  │
│   Event 新增字段:                                                 │
│     OriginRegion string  // 事件产生的 region                    │
│     ReplicaOf   string  // 如果是复制事件，指向源事件 ID         │
│                                                                  │
│ 规则:                                                             │
│   每个 replication worker 消费事件时：                            │
│     如果 event.OriginRegion == localRegion → 跳过（自己的事件）  │
│     如果 event.ReplicaOf != "" → 跳过（复制的事件不再次复制）    │
│                                                                  │
│ 最终: 每个对象在每个 region 只保留一份                           │
│   写入在 Region A → 复制到 Region B → 停止                       │
│   写入在 Region B → 复制到 Region A → 停止                       │
└────────────────────────────────────────────────────────────────┘

**为什么现在做：** 当前的单区域部署架构是**业务连续性硬天花板**。任何依赖存储的产品一旦扩展到多区域需求（跨洲用户、合规数据驻留、异地容灾）就必须有原生的 active-active 支持。现在的复制代码提供了基础但不足以支撑真实的多区域场景。冲突检测更是主动数据保护——LWW 的静默覆盖在 replicated 环境下是最令人恐惧的用户体验问题。

| 影响面 | 工作量估计 |
|--------|-----------|
| 复制规则配置 + 路由引擎 | 中 |
| 多目标存储坐标（Storage 坐标映射） | 中 |
| 冲突检测 Version Vector 实现 | 高 |
| 防循环源 region 标识 | 低 |
| 冲突管理 API | 中 |
| 测试（多 region 模拟） | 高 |

---

## 4. 沙箱化事件触发器：用户自定义函数（WASM）

### 当前状态

**事件触发能力存在（webhook + event bus），但不可编程。** 用户无法定义在特定事件发生时执行的业务逻辑：

| 能力 | AWS S3 + Lambda | 当前 AeroVault |
|------|----------------|----------------|
| 事件触发函数 | ✅ Lambda 函数 | ❌ 无 |
| 自定义预处理 | ✅ 上传时转换 | ❌ 无 |
| 实时内容路由 | ✅ 按条件分发 | ❌ 无 |
| 复杂事件处理 | ✅ Step Functions | ❌ 无 |
| 触发延迟 | ms 级 | ❌ 无 |

**骨架存在：** `repository.go:64` 的 `LambdaARN` 字段表明代码库的设计意图是支持 Lambda 式触发器，但注释明确写为 `"unused, kept for compat"`。

**为什么插这里而非外部 Lambda：** 

| 原因 | 说明 |
|------|------|
| **网络隔离** | 用户部署可能在隔离网络环境中（内网、私有云、离线），无法访问外部 Lambda 服务 |
| **延迟** | 外部调用增加 50-200ms 延迟。内嵌 WASM 函数调用 < 1ms |
| **成本** | 无额外 AWS Lambda 费用（每次调用 × 请求量） |
| **零依赖** | 一个二进制直接运行，不依赖外部计算服务 |
| **S3 规范** | S3 的 `LambdaFunctionArn` 是标准通知目标，但执行引擎可以内嵌 |

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/repository/repository.go:64` | `LambdaARN` 字段 | 注释 `unused, kept for compat` |
| `internal/events/bus.go` | 事件广播到所有订阅者 | 无"执行用户函数"的订阅者 |
| `internal/events/webhook.go` | Webhook 消费者（HTTP POST） | 可复用其模式实现 WASM 消费者 |
| `internal/service/file_crud.go:Put` | 核心写入路径 | 无预处理/后处理钩子 |
| `internal/jobs/jobs.go` | Job 队列 | 可执行异步函数 |
| `internal/api/rest/admin.go` | 管理 API | 无函数 CRUD 端点 |
| `internal/mcp/server.go` | MCP 工具 | 无函数管理工具 |
| `go.mod` | 依赖清单 | 无 WASM 运行时依赖 |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **函数运行时崩溃** | WASM 函数 panic 或 OOM | 无隔离 | 进程级隔离（每个函数单独 sandbox）+ 超时后 kill |
| **无限循环** | 函数触发后再次触发同一事件 | 产生事件风暴 | 限制递归深度（max 3）+ 循环检测（事件 origin 追溯） |
| **函数超时** | 函数运行超过 30 秒 | 阻塞事件处理管线 | 默认超时 5s，可配置 |
| **状态泄漏** | 函数访问全局变量 | 数据泄露风险 | 每次调用新实例（无状态沙箱） |
| **磁盘写入** | 函数写入临时文件 | 磁盘耗尽 | 配额限制（`MAX_FUNCTION_TMPFS` = 10 MB） |
| **代码升级** | 用户上传新版本函数 | 正在运行的函数中断 | 版本化函数 + 优雅迁移（先 drain 旧版本再切换） |
| **多租户隔离** | 租户 A 的函数访问租户 B 的数据 | 越权数据访问 | 函数调用时注入租户上下文，只允许访问自身 bucket |
| **函数更新失败** | 新版本函数语法错误 | 所有事件丢失 | 回滚到上一个稳定版本 + 告警 |

### 架构蓝图

```
┌─ WASM 函数运行时 ─────────────────────────────────────────────│
│ 新增包: internal/functions/                                     │
│                                                                  │
│ 依赖: wazero（纯 Go WASM 运行时，零 CGO，~ 200KB）               │
│       → go.mod 添加 github.com/tetratelabs/wazero                │
│                                                                  │
│ type Function struct {                                           │
│     ID        string       // uuid                               │
│     Name      string       // "resize-images"                    │
│     Tenant    string       // 所属租户                           │
│     Code      []byte       // WASM 字节码（用户上传）             │
│     Trigger   TriggerDef   // 触发配置                           │
│     Timeout   time.Duration // 超时（默认 5s）                   │
│     MemoryKB  int          // 内存限制（默认 64MB）              │
│     EnvVars   map[string]string                                  │
│     Version   int                                                │
│     Active    bool                                               │
│     CreatedAt time.Time                                          │
│ }                                                                 │
│                                                                  │
│ type TriggerDef struct {                                         │
│     EventTypes []string    // ["object.created", "object.deleted"] │
│     Bucket     string      // "" = 所有                          │
│     Prefix     string      // "images/"                          │
│     Filter     *EventFilter                                      │
│ }                                                                 │
│                                                                  │
│ 运行时:                                                           │
│   每个函数调用:                                                   │
│     1. 克隆模块实例（非共享全局状态）                              │
│     2. 注入 Event JSON + 租户上下文到 WASM 内存                   │
│     3. 调用 `handle(event)` 导出函数                              │
│     4. 函数返回 result（可选 Action: {Action: "tag", Tags: {...}}）│
│     5. 根据 result 执行后续（打标、隔离、拒绝写入—函数决定）        │
│     6. 超时或 panic → 记录失败 + 跳过不影响事件                    │
│                                                                  │
│ 安全限制:                                                         │
│   - 无网络访问（除非显式 enable）                                 │
│   - 无文件系统访问（只读 proc 信息）                              │
│   - 没有 Go runtime 逃逸                                        │
│   - 内存上限（超限→实例被杀）                                     │
│   - CPU 时间片限制                                               │
└────────────────────────────────────────────────────────────────┘

┌─ 管理 API ────────────────────────────────────────────────────│
│                                                                  │
│   POST   /v1/admin/functions         → 创建函数（上传 WASM 字节码） │
│   GET    /v1/admin/functions         → 列出函数                   │
│   GET    /v1/admin/functions/{id}    → 函数详情                   │
│   PUT    /v1/admin/functions/{id}    → 更新函数代码（新版本）      │
│   DELETE /v1/admin/functions/{id}    → 删除函数                   │
│   POST   /v1/admin/functions/{id}/activate  → 启用               │
│   POST   /v1/admin/functions/{id}/deactivate → 停用              │
│   POST   /v1/admin/functions/{id}/test → 测试执行（带模拟事件）    │
│   GET    /v1/admin/functions/{id}/logs   → 执行日志               │
│                                                                  │
│ 指标:                                                             │
│   function_invocations_total{function_id, status}                │
│   function_duration_ms{function_id}                              │
│   function_memory_kb{function_id}                                │
│   function_errors_total{function_id, error_type}                 │
└────────────────────────────────────────────────────────────────┘

┌─ 同步 vs 异步 ────────────────────────────────────────────────│
│                                                                  │
│ 同步模式（文件写入前）：                                          │
│   适用场景: 内容验证、格式校验、安全扫描、自动转换                  │
│   PUT 请求 → 执行函数 → 函数返回 deny/allow → 继续/拒绝          │
│   注意: 同步模式增加请求延迟（~5ms for WASM vs ~100ms for HTTP）│
│   可配置同步策略: "允许函数写入前修改内容" (pre-upload hook)      │
│                                                                  │
│ 异步模式（文件写入后）：                                          │
│   适用场景: 压缩、转码、标记、元数据提取                            │
│   通过 Job 池异步执行，不阻塞请求                                  │
│   可用 webhook 消费者框架触发                                     │
└────────────────────────────────────────────────────────────────┘

**为什么现在做：** 这是从"对象存储"到"计算平台"的核心差异化特性。WASM 提供了零依赖、安全沙箱化的代码执行能力（纯 Go 运行时，无 Docker/containerd 依赖）。事件驱动计算是 S3 Lambda 模式的灵魂——没有它，AeroVault 的 S3 兼容性停留在"存储"层面，无法进入"计算平台"的竞争维度。同时，已有 `LambdaARN` 骨架字段表明设计方向的一致性。

| 影响面 | 工作量估计 |
|--------|-----------|
| `internal/functions/` 包 + wazero 集成 | 中 |
| 管理 API（函数 CRUD） | 中 |
| 事件触发器消费者 | 中 |
| 安全沙箱（内存/时间/网络限制） | 高 |
| Sync hook 集成到 FileService | 中 |
| 测试（WASM 编译 + 执行 + 隔离） | 高 |

---

## 5. 对象生命周期治理与合规框架：保留调度、法律保全、处置证书

### 当前状态

**合规能力当前处于"点状实现"阶段：**

| 合规能力 | 当前状态 |
|---------|---------|
| 对象锁（Retention） | ✅ `LockedUntil` 字段 + `checkLockBeforeOverwrite` |
| Legal Hold | ✅ `_aero_legal_hold` 元数据标记 |
| WORM 桶 | ⚠️ 桶级 `ObjectLockSeconds` 存在但无"不可逆启用"语义 |
| 到期删除 | ✅ Lifecycle `expire_after_days` 规则 |
| 软删除保留 | ✅ `RetentionJob`（RECONCILE_RETENTION_DAYS） |
| 合规模式（GOVERNANCE/COMPLIANCE） | ❌ 无模式区分，所有锁都一样 |
| 法律保全（Litigation Hold） | ❌ 覆盖 Retention，不能被覆盖或到期 |
| 保留调度（Event-Based Retention） | ❌ 只能按时间，不能按事件触发 |
| 处置审批（Disposition Approval） | ❌ 到期后自动删除，无审批环节 |
| 处置证书（Certificate of Destruction） | ❌ 无删除证明文档 |
| 自动分类→策略匹配 | ❌ 无分类驱动的保留规则 |

**核心缺口：** 当前系统锁定对象的方式是"多少秒后解锁"——但企业合规要求的是"谁、什么、何时、证明"：

```
当前: Object.LockedUntil = "2027-01-01T00:00:00Z"
企业要求:
  - 保留原因: "SEC_regulation_17a-4"
  - 保留授权人: "compliance-officer@company.com"
  - 法律保全案号: "LIT-2026-0042"
  - 处置审批链: [{"approver": "manager", "at": "..."}, {"approver": "legal", "at": "..."}]
  - 处置证书: {"destroyed_at": "...", "method": "crypto-shred", "witness": "auditor"}
```

### 代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/repository/repository.go:Object` | `LockedUntil *time.Time` | 无保留原因、法律保全案号、保留分类 |
| `internal/service/file_crud.go:hardDeleteObject` | 检查 `LockedUntil` | 不检查法律保全状态（legal hold 通过 metadata 检查）|
| `internal/reconcile/retention.go:RetentionJob` | 按天清除过期软删除 | 无处置审批链、无证书生成 |
| `internal/reconcile/lifecycle.go:handleExpiredObject` | 按 lifecycle 规则删除 | 无保留策略检查前的处置审批步骤 |
| `internal/api/rest/admin.go` | 管理 API | 无保留策略管理端点 |
| `internal/api/s3compat/bucketconfig.go` | `ObjectLockSeconds` 桶配置 | 无 `ObjectLockMode`（GOVERNANCE/COMPLIANCE）|
| `internal/repository/migrations/sqlite/0005_versioning_tagging.up.sql` | 版本化表 | 无 `retention_reason`、`legal_case_id` 字段 |
| `internal/service/file_features.go:LockObject` | 设置 `LockedUntil` | 不写入保留元数据链 |

### 边界情况暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **法律保全覆盖保留** | 对象设置了 90 天保留 + 法律保全 | 90 天后锁释放但法律保全未解除 | 法律保全必须阻止任何删除，不受 `LockedUntil` 影响 |
| **多次法律保全** | 同一个对象被两个独立的诉讼保全 | 只能有一个 legal hold 标记 | 支持多个法律保全案号 + 各自独立解除 |
| **保留分类继承** | 桶指定"金融文档保留 7 年" | 不对——每个对象手动设置 | 桶级默认保留策略 + 自动应用于新对象 |
| **处置审批超时** | 审批请求发送给 manager 7 天未回应 | 对象过期但无法删除 | 审批超时 → 升级到上级审批人 + 告警 |
| **处置证书真实性** | 合规审计需要删除证明 | 无证书 | 生成 signed 处置证书（HMAC-SHA256 签名时间戳） |
| **监管机构暂缓处置** | 监管机构要求暂停所有已就绪的处置 | 无暂停机制 | 全局处置暂缓开关 + 排除特定 case ID |
| **保留策略变更后向前兼容** | 桶保留规则从 7 年改为 3 年 | 已保存的对象保留期不变还是变？ | 策略变更不影响已应用保留的对象；新策略仅适用于新写入 |
| **跨桶保留引用** | 对象被两个桶的保留策略引用 | 无引用计数 | 对象保留策略使用决定最长的保留期（MAX 聚合） |

### 架构蓝图

```
┌─ 保留策略模型 ────────────────────────────────────────────────│
│ 新增 internal/compliance/ 包                                    │
│                                                                  │
│ type RetentionPolicy struct {                                   │
│     ID           string                                         │
│     Name         string    // "SEC_17a-4"                       │
│     Description  string                                         │
│     Scope        string    // "bucket:*" | "bucket:financial"   │
│     Duration     Duration  // 保留时长                           │
│     Action       string    // "archive" | "destroy" | "freeze"  │
│     Mode         string    // "GOVERNANCE" | "COMPLIANCE"       │
│     RequiresApproval bool   // 到期需要审批                      │
│     ApprovalChain []string // 审批人角色列表                     │
│     LegalHold    bool     // 是否强制法律保全                    │
│     CreatedAt    time.Time                                      │
│ }                                                                │
│                                                                  │
│ type RetentionBinding struct {                                   │
│     PolicyID     string     // 关联的策略                        │
│     ObjectID     int64                                          │
│     AppliedBy    string     // "system" | "user:{id}"           │
│     AppliedAt    time.Time                                      │
│     Reason       string     // 保留原因                          │
│     CaseID       string     // 法律案号（可选）                  │
│     ExpiresAt    time.Time   // 期望到期时间                     │
│     Status       string     // "active" | "pending_disposal"    │
│                             //  "disposed"                      │
│     DisposedBy   *string    // 处置执行人                       │
│     DisposedAt   *time.Time // 处置时间戳                        │
│     DisposalCert *string    // 处置证书哈希                      │
│ }                                                                │
└────────────────────────────────────────────────────────────────┘

┌─ 处置工作流 ──────────────────────────────────────────────────│
│ 阶段 1: 保留期满检测                                             │
│   每日 reconcile 扫描:                                           │
│     SELECT FROM retention_bindings WHERE                          │
│       expires_at < now() AND status = 'active'                   │
│   结果: 标记为 `pending_disposal`                                 │
│                                                                  │
│ 阶段 2（如需要审批）: 处置审批                                    │
│   向审批链第一人发送通知                                          │
│   每个审批人审批后→下一位                                         │
│   所有审批通过→进入阶段 3                                         │
│   审批拒绝→更新保留期（延长）                                     │
│   审批超时→升级到上一级                                           │
│                                                                  │
│ 阶段 3: 安全处置                                                 │
│   1. 生成处置证书: JSON {                                        │
│        object_id, key, storage_key, size, etag,                  │
│        retention_policy_id, retained_from, retained_until,        │
│        destroyed_at, method: "crypto-shred",                     │
│        witness: "system", certificate_hash: "sha256:..."         │
│      }                                                           │
│   2. 对处置证书签名：HMAC-SHA256(system_secret, cert_json)       │
│   3. 将处置证书存储为不可变记录（_aero_disposal_certificates 表） │
│   4. 真正删除对象 / Crypto-shred（若 SSE 加密：丢弃密钥）         │
│   5. 更新 retention_binding: status=disposed                     │
│                                                                  │
│ 阶段 4: 审计                                                     │
│   处置证书对合规审计员可查询：                                     │
│     GET /v1/admin/compliance/certificates?from=...&to=...        │
│   证书不可删除（仅追加）。                                        │
└────────────────────────────────────────────────────────────────┘

┌─ 法律保全（Litigation Hold）───────────────────────────────────│
│ 法律保全独立于保留策略:                                           │
│   - 可以施加在任何对象上（不论保留期）                           │
│   - 覆盖保留期：即使保留期已到，法律保全阻止处置                   │
│   - 支持多个法律保全（多个诉讼可能针对同一对象）                   │
│   - 每个法律保全有独立 case_id + 施加人 + 日期 + 预期解除日期     │
│                                                                  │
│ 管理 API:                                                         │
│   POST   /v1/admin/compliance/legal-holds                        │
│     {"object_ids": [...], "case_id": "LIT-2026-0042",            │
│      "applied_by": "legal@co.com", "reason": "patent dispute"}   │
│                                                                  │
│   DELETE /v1/admin/compliance/legal-holds/{id}                   │
│     （需要 case_id + 授权人验证）                                  │
│                                                                  │
│   GET /v1/admin/compliance/legal-holds?object_id=X              │
│     → 列出某个对象上的所有法律保全                                │
│                                                                  │
│ 删除保护:                                                         │
│   hardDeleteObject 路径新增：                                    │
│     如果 legal_holds 非空 → 即使 LockedUntil 已到期也拒绝删除    │
└────────────────────────────────────────────────────────────────┘

┌─ 与现有事件的集成 ────────────────────────────────────────────│
│ 合规事件也是事件:                                                │
│   event: compliance.retention_applied                            │
│   event: compliance.retention_expiring                           │
│   event: compliance.disposition_pending                          │
│   event: compliance.disposition_completed                        │
│   event: compliance.legal_hold_applied                           │
│   event: compliance.legal_hold_removed                           │
│                                                                  │
│ 这些事件可以触发:                                                │
│   1. 通知 retention 负责人                                       │
│   2. 触发 webhook 到外部 GRC 系统                                │
│   3. 记录到 audit_log                                           │
│   4. 通过 SSE 推送到合规仪表盘                                    │
└────────────────────────────────────────────────────────────────┘

**为什么现在做：** 对象锁与 WORM 的点状实现只能满足基础的"防止删除"需求，但企业合规（金融监管 SEC 17a-4、医疗 HIPAA、GDPR、PCI-DSS）要求的是完整的**保留治理生命周期**。合规是存储产品进入受监管市场的准入门槛。当前的合法持有（`_aero_legal_hold` 只是一个 metadata key）和保留（`LockedUntil` 一个时间戳）不足以通过任何正规合规审计。更重要的是，框架是增量构建的——现有 `LockedUntil`、`legal_hold`、`lifecycle`、`RetentionJob` 都是这个框架的天然组成构件，集成成本低。

| 影响面 | 工作量估计 |
|--------|-----------|
| RetentionBinding 模型 + 迁移 | 中 |
| 保留策略引擎 + 管理 API | 中 |
| 处置工作流 + 证书生成 | 高 |
| 法律保全 + 管理 API | 中 |
| 现有 LockedUntil/legal_hold 统一 | 低 |
| 集成审计 + 事件总线 | 低 |

---

## 优先级矩阵

| # | 方向 | 业务价值 | 工程成本 | 既有资产复用 | 推荐排序 |
|---|------|---------|---------|-------------|---------|
| 1 | **S3 Event Notifications（SQS/SNS/Lambda 投递）** | ★★★★★（兼容性最后拼图） | ★★（复用 SigV4 + bus + webhook 架构） | `NotificationRule` CRUD、SigV4、event bus | **1** |
| 2 | **对象 CDC 流** | ★★★★（外部 ETL/分析需求） | ★（复用 events 表 + ID 序列） | `events` 表、自增 ID、tenant 索引 | **2** |
| 3 | **生命周期治理与合规** | ★★★★（合规市场准入） | ★★★★（构件多但集成度低） | `LockedUntil`、`legal_hold`、`RetentionJob`、`audit_log`、`lifecycle` | **3** |
| 4 | **Active-Active 多区域复制** | ★★★★（业务连续性） | ★★★★★（复制 + 冲突检测 + 防循环） | `replication.go` 基础复制、`JobPool` | **4** |
| 5 | **WASM 沙箱化事件触发器** | ★★★★★（差异化竞争） | ★★★★（wazero 集成 + 安全沙箱） | `LambdaARN` 骨架、event bus、job queue | **5** |

**排序逻辑：**
- **#1 通知投递**：CRUD 已经完成，只差执行引擎——这是"最后一公里"的问题。每多一个版本不实现，notification_rules 列就多一个版本的死数据。
- **#2 CDC 流**：成本极低（复用已有 events 表），但生态价值极高——解锁外部 ETL、数据湖、分析集成。
- **#3 合规治理**：现有点状合规能力的自然延伸。差异在"从点状→系统化"，但核心构件已存在。
- **#4 多区域复制**：工程复杂度最高，但方向明确——单区域架构的硬天花板。
- **#5 WASM 触发器**：最具差异化价值但也是工程投入最高。建议作为**中期 roadmap 的标志性能力**，先行打桩（`LambdaARN` 字段+函数管理 CRUD），再实现运行时。

---

*分析基于 commit: `cae49d9` | 代码行数 ~46,659 (Go) + SDK/UI/Infra | 扫描范围: 全部 internal/ 子包、migrations、config、deploy、docs | 排除: 全部 prior expansion 文档（v1–v8）已覆盖方向*
