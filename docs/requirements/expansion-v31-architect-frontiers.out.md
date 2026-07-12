现在我已经充分验证了代码库的实际状态。以下是基于实际代码的 **架构审阅**：

---

## 架构审阅：v31 分析的验证与修正

### ✅ 方向 1 — Data Provenance：结论完全正确

**现状属实：** 代码中零 `provenance`、`derived_from`、`parent_id` 相关实现。

**补充发现：** 现有基础设施实际上已经为 Provenance 准备了许多组件：

| 已有组件 | 文件位置 | 可映射到 Provenance |
|---------|---------|-------------------|
| `Object.VersionID` + `@v<id>` storage key | `file_crud.go:Put` | 版本链 → `supersedes` 边 |
| `ai_usage` 表 (RecordUsage) | `sql_chunks.go:115` | 对象 ↔ AI 请求的引用关系 |
| `events` 表 (EventCreated/Deleted/Accessed) | `sql_events.go` | 事件时间线 |
| `audit_log` 表 | `audit.go` | 管理操作的审计轨迹 |
| `Event.Payload` 字典 | `file.go:emit` | 已含 `backend` `etag` `content_type` |

**建议调整：** 在 P0 中，可以**零新表**实现最小可行的 Provenance：在 `Object.Metadata` 中添加 `_provenance_source`、`_provenance_agent`、`_provenance_derived_from` 等系统 metadata key。这避免了 schema 迁移，且与现有系统 metadata 机制一致。P1 再引入 `provenance_records` 和 `provenance_edges` 表。

---

### ⚠️ 方向 2 — CDC/Event Streaming：正确，但路径 A 需要修正

**现状属实：** 无 Kafka/Pulsar/Kinesis 适配器。事件仅通过 in-process channel + Postgres LISTEN/NOTIFY 传播。

**对路径 A 的重要修正：**

> 分析建议 "SSE / Webhook → Kafka Connect Source Connector"

这个方案有一个**严重的可靠性缺陷**：SSE 和 Webhook 都是"推送模型"，缺乏 Kafka Connect 所需的 offset 管理能力。Kafka Connect Source Connector 的标准做法是：

- **JDBC Source Connector** — 基于 `SELECT ... WHERE id > $last_offset` 轮询
- **Debezium** — 基于 WAL/CDC 日志的流式消费

**推荐修正方案：**

```
┌─ aero-vault ──────────────┐     ┌─ Kafka Connect ────┐     ┌─ Kafka ──┐
│                            │     │                     │     │           │
│  events 表 (持久化)        │────▶│ JDBC Source          │────▶│ aero-    │
│  (repository.Event)        │     │ Connector            │     │ events   │
│                            │     │ (SELECT FROM events   │     │           │
│                            │     │  WHERE id > $offset) │     │           │
└────────────────────────────┘     └─────────────────────┘     └───────────┘
```

**理由：**
1. `events` 表已持久化、有序、有自增 ID — 是 JDBC poll 的理想源
2. Kafka Connect JDBC Source Connector 是标准组件，零自定义代码
3. 支持 exactly-once offset 管理
4. SSE 的有状态连接在连接断开后不可靠重放

**原方案的 SSE/Webhook 路径适用于"轻量集成"场景**（不需要 Kafka 基础设施的小型部署），但不适合作为推荐的企业集成路径。

---

### 🔴 方向 3 — Serverless Object Triggers：部分不准确

**关键发现：** S3-compatible 的 Notification Configuration API 已经**完整实现**：

| 组件 | 状态 | 代码位置 |
|------|------|---------|
| `GET /?notification` XML 返回 | ✅ 完整 | `handler.go:780` |
| `PUT /?notification` XML 解析 | ✅ 完整 | `handler.go:809` |
| `DELETE /?notification` | ✅ 完整 | `handler.go:841` |
| `lambdaConfig` / `topicConfig` / `queueConfig` XML 结构 | ✅ 完整 | `xml.go:395-425` |
| `NotificationRule` 数据模型 (LambdaARN/TopicARN/QueueARN) | ✅ 完整 | `repository.go:58` |
| 持久化到 bucket 配置 JSON | ✅ 完整 | `sql_buckets.go:142` |
| REST API 等效端点 | ✅ 完整 | `rest/handler.go:552` |
| **运行时事件分发到配置的目标** | ❌ **缺失** | 注释明确标注 `unused, kept for compat` |

**修正论断：** 这个方向不应该被描述为 "完全未覆盖"，而应该是 **"API 平面 100% 就绪，运行时引擎 0% 就绪"**。这使得实施复杂度显著降低——不需要重新设计 API 合约。

**建议的修正架构：**

```
现有（已完成）                 缺失（需要实现）
┌─────────────────────┐     ┌─────────────────────────────┐
│ S3 API layer         │     │  Notification Dispatch       │
│  GET/PUT/DELETE      │────▶│  Engine                      │
│  ?notification       │     │                              │
│                      │     │  EventBus.Subscribe()        │
│ NotificationRule     │     │  → match rules by event type │
│ (persisted)          │     │  → dispatch via:             │
│                      │     │     LambdaARN  → ???         │
│  EventBus.Publish()  │     │     TopicARN   → SNS SDK     │
│  (当对象创建时)       │     │     QueueARN   → SQS SDK     │
└─────────────────────┘     └─────────────────────────────┘
```

**同时注意：** 分析中提到的 `LambdaARN string` 已在代码中标记为 `unused`，但 SNS Topic 和 SQS Queue 的支持实际上更有企业价值——因为 AWS Lambda 的替代方案（自定义容器/gRPC）在自托管环境中更适用。

---

### ✅ 方向 4 — Predictive Tiering：结论正确，但现有 AI 数据比分析描述的更丰富

**现状确认：** `reconcile/lifecycle.go` 只有 `ExpireAfterDays` 规则式生命周期。

**补充发现 — 已经可用的训练数据远超分析所述：**

- `ai_usage` 表记录了每次 Chat/Search 的 `object_ids`、`caller`、`query`、`total_tokens`、`cost_micros`
- `events` 表记录了所有 `object.created/deleted/accessed` 事件
- AccessLog middleware 记录了每次 HTTP 请求的 tenant/method/key/status/latency
- `Object.Metadata` 已经是 JSON 字典，可以附加任意系统标签（如 `_last_access_at`、`_access_count_30d`）

**建议：** P0 可以极低成本实现启发式预测：
1. 在 FileService 的 `Get`/`Head` 路径中更新 `Object.Metadata._last_access_at`
2. LifecycleJob 检查 `_last_access_at` + `_access_count` 而不是固定天数
3. 这**不需要 ML 模型**，仅基于现有访问行为的自适应阈值

---

### ✅ 方向 5 — Multi-Model Query Engine：结论正确，但嵌入式 SQL 方案需重新评估

**现状确认：** 无 SQL 查询能力。元数据查询走 REST `/v1/files?prefix=&marker=`，语义搜索走 POST `/v1/search`，无统一入口。

**对方案的审阅意见：**

> 分析推荐 "嵌入式 SQL 引擎（基于 expr 或 go-sal）"

这有**三个未被讨论的权衡**：

| 权衡 | 嵌入式方案 | 替代方案（Trino/Presto connector） |
|------|-----------|-----------------------------------|
| **表达能力** | 只能 SELECT + 虚拟表，无法跨节点分布式 JOIN | 完整 ANSI SQL + 跨数据源 JOIN |
| **大规模查询** | 全表扫描在单进程内执行，大结果集 OOM | 分布式 worker 执行，内存可扩展 |
| **维护成本** | 自己维护 SQL 解析器、优化器、执行器 | 外部成熟项目，社区驱动 |

**推荐修正：** 采用**两阶段方案**：
- **P0（嵌入式轻量方案）：** 使用 `expr` 或 Go 的 SQL 解析器 + 虚拟表，只支持 `SELECT ... WHERE ... LIMIT` 子集（分析查询）。限制扫描行数（上限 10,000）。
- **P1（企业级方案）：** 实现 **Trino/Kyuubi connector**（与分析中说的 "不推荐" 相反）。这是一个独立项目（`aerovault-trino-connector`），零依赖嵌入 aero-vault 核心，但提供完整的 SQL 能力。

**为什么 P1 应该选择 Trino 而不是嵌入式：** 分析中提到的 "100+ MB JVM 依赖" 对于 connector 来说不成立——Trino 本身是独立 JVM 进程，connector 是它的一个插件（一个 JAR）。aero-vault 核心代码不加载 JVM。

---

## 总结评级

| 方向 | 分析质量 | 主要修正 |
|------|---------|---------|
| 1. Data Provenance | ✅ 高质量 | 建议利用已有的 `Object.Metadata` 实现 P0 最小方案 |
| 2. CDC / Event Streaming | ✅ 好 | 路径 A 应改用 JDBC Source Connector（非 SSE/Webhook） |
| 3. Serverless Triggers | ⚠️ 有缺陷 | "完全未覆盖" 不准确 — Notification API 层已完成；需补运行时分发引擎 |
| 4. Predictive Tiering | ✅ 好 | 建议 P0 从启发式方案开始（利用 `_last_access_at` 而非 ML） |
| 5. Multi-Model Query | ⚠️ 方案偏轻 | 应提供两阶段路径：P0 嵌入式 + P1 Trino Connector（大型查询） |

整体分析质量高，去重逻辑严谨，5 个方向确实在前 30 期分析中未被实质性覆盖。以上修正主要是基于实际代码状态的补充和调整。
