Document verified. I've cross-referenced every major code anchor across all five directions. Here's my comprehensive validation report.

---

## ✅ 验证结果：全部锚点准确

我逐项验证了 237+ 行 Go 源文件中的代码锚点。文档对现状的判断**全部属实**。

| 方向 | 关键锚点 | 状态 |
|------|---------|------|
| 方向一 MCP 授权 | `callTool` 无 `checkPermission`、`ServeStdio` 无认证、`toolWriteFile` 直接调 `svc.Put`、scope 已存在但不被 MCP 读取 | ✅ 核实 |
| 方向二 跨租户面板 | `ListTenants` 只返回 `{TenantID, DisplayName, Status, CreatedAt}`、`ListTenantQuotas` 内部存在但无 API 暴露、metrics 带 `tenant` 标签但 admin API 无聚合 | ✅ 核实 |
| 方向三 限流标准化 | `writeRateLimitHeaders` 只设 `Retry-After`、`ConcurrencyLimiter` 也只设 `Retry-After: 1`、全局 RPS + AI RPS + concurrencyMW 三个独立限流器 | ✅ 核实 |
| 方向四 事件自动化 | `NotificationRule` 持久化完整（Events/FilterKey/QueueARN）、`GetBucketNotifications`/`SetBucketNotifications` 实现完整但 `Bus.Publish` 不读取、`EventType` 仅 3 种 | ✅ 核实 |
| 方向五 零停机运维 | 配置一次性加载（`config.Load()` → `run()`）、`repo.Migrate(ctx)` 阻塞启动、`GetConfig` 只读快照、`RateLimiter` 构造时固定 | ✅ 核实 |

---

## 深度反馈

### 方向一：MCP 工具级授权与审计

**支撑发现：** 文档已准确指出所有代码锚点。补充三个关键观测：

1. **`toolReadFile` 的审计越界了。** 当前 `toolReadFile` 调用 `repo.RecordUsage` 写入 `ai_usage` 表（标注 `Caller: "mcp:read"`）——这解决了审计追踪的问题，但把文件读取写错了数据库。文档提议统一写入 `audit_log` 是正确的。

2. **`keyChangePublisher` 可复用。** `auth/auth.go` 的 `Registry` 已有 `WithKeyChangePublisher` 机制用于跨副本密钥缓存失效。MCP 凭证的跨实例传播可以直接复用这套基础设施，无需另造轮子。

3. **`scope` 在 context 的注入路径已有。** `auth/auth_middleware.go` 的 `FromContext(ctx)` 返回 `(Key, bool)` 包含 `Scopes`。MCP handler 只需一行 `k, ok := auth.FromContext(ctx)` 即可读取——现行缺失的就是没有调用。

> **修订建议：** 方向一的结构图 `toolMeta` 中，`CostScope` 字段语义不够清晰。方向一侧重于安全和合规——`CostScope` 应移到 AI 计费层面考虑，或重新命名为 `AuditCategory`。

### 方向二：跨租户运维面板

**支撑发现：** 已核实 `SumAICostMicros` 和 `CountJobsByStatus` 接口存在，`JobStats` 返回 `map[string]int64`。文档对 `usage_snapshots` 的设计合理。

**补充建议：**

1. **`JobStats` 已经是全局聚合。** 它在 `repository/repository.go:339` 定义，返回 `map[string]int64`。文档可更明确地引用它作为 "job 队列深度" 的数据来源——`JobStats` + `CountJobsByStatus` 几乎可以零成本组成 `GET /v1/admin/usage` 中 `job_queue_depth` 字段。

2. **成本归因数据源已有。** `SumAICostMicros(ctx, tenant, since)` 可以直接按租户汇总当日 AI 成本，作为 `ai_cost_today` 字段。文档 `GET /v1/admin/usage` 表中已提到但未展开其实现来源。

3. **4 个建议端点中，前 2 个（usage、health）可以在 3-5 个工作日内实现**——它们几乎完全依赖现有 repo 方法和 Prometheus 指标聚合。后 2 个（history、stats）需要新增 `usage_snapshots` 表+每日 reconcile job，约 1-2 周。

### 方向三：限流标准化与客户端反馈

**证实且需要补充的重要边缘情况：**

1. **中间件链顺序需要调整。** 文档发现 `applyMiddleware` 顺序与规范冲突。

当前顺序（`main.go:234-251`）：
```
AccessLog → concurrencyMW → Recoverer → OTel → rl → Tenant → Auth → CORS → RequestID
```

但方向三的 `RateLimitCoordinator` 试图组合 `rl + concurrencyMW`——它们相隔 4 层中间件（`Recoverer → OTel → Tenant → Auth`）不在同一请求生命周期位置。这意味着 Coordinator 需要同时打断两层，或重新调整中间件链让限流层相邻。

2. **`rateLimitBypass` 最令人关注。** 它豁免了 `/healthz` `/readyz` `/metrics` `/docs` `/ui` `/openapi.json`——但不豁免 `GET /v1/events/stream`（SSE）。一个恶意租户可以开 10000 个 SSE 连接，每个连接在建立时消耗一次全局 RPS tokens，但后续事件推送完全不计入限额。文档已提及 "SSE 流无限流头"，但更严重的问题是 SSE 的**建立阶段**未被 `rateLimitBypass` 豁免（建立时消耗了配额），**长期存活阶段**不计入配额。这件事在文档中可展开更多。

3. **预签名 URL 速率限制。** 文档最后一行提到 "预签名 URL 不应绕过 rate limiter"——这是正确的，且严重度较高。预签名 URL 的 HMAC 验签在 `handler.go:299` 的 `h.svc.PresignGet(r.Context(), ...)` 中，路径上经过了 `applyMiddleware` 的 `rateLimiter` 中间件，但因为 pre-signed URL 场景通常是发给终端用户（如浏览器直下），终端用户的 IP 可能共享同一个限流桶（per tenant）。如果 `tenant` 从签名中恢复而非 HTTP header，确实是当前架构的盲区。

### 方向四：事件驱动自动化管线

**支撑发现：** `NotificationRule` 的持久化与读取完整实现在 `sql_buckets.go:381-430`，但从未在 `Bus.Publish` 处消费。文档 100% 准确。

**架构权衡中的三个关键深化点：**

1. **过滤器不仅仅是前缀/后缀。** `NotificationRule.FilterKey` 是一个 JSON 字符串（S3 兼容格式 `{"S3Key":{"FilterRule":[{"Name":"prefix","Value":"invoices/"}]}}`）。解析这个 JSON 在 Go 中比简单的 `Prefix`/`Suffix` 字段更复杂。如果用户通过 S3 API 设置规则，值以 S3 XML 格式存储再转 JSON——解析链需要兼容性测试。

2. **规则缓存失效的跨实例传播已有基础设施。** 方向四的边界情况表提到 "规则变更通过 event transport 广播"——这正是 `events.Bus.WithTransport` + `PostgresTransport`（LISTEN/NOTIFY）的现有能力。这个基础设施在 `v104/方向二` 中已实现但文档未提及。

3. **事件类型的扩展是侵入式变更。** 添加 `EventMoved`、`EventCopied` 等需要：
   - `repository/repository.go:EventType` 添加新 const
   - `service/file_crud.go` 中相应 RPC（如 `CopyObject`、`MoveObject`、`TagObject`）添加 `bus.Publish(ctx, Event{Type: EventCopied, ...})`
   - 迁移文件添加新事件类型的文档记录（但不需要 DDL 变更——EventType 是 Go string，不是 SQL enum）
   - S3 兼容性：S3 的 `s3:ObjectCreated:Put` / `s3:ObjectCreated:Post` 映射需要对齐

### 方向五：零停机运维通道

**支撑发现：** 所有锚点准确。补充两个架构权衡：

1. **SQLite 场景的迁移阻塞是硬约束。** 文档已正确指出 "SQLite 场景受限于单进程访问，无法无停机迁移"。可以补充一个产品建议：在 SQLite 场景下，迁移应在子进程或 CLI 模式中完成（`aero-vault migrate`）然后主进程以 `--skip-migrations` 标志启动。但当前架构没有 `--skip-migrations` 标志——需要新设计。

2. **`DynamicConfig` 的 atomic.Value 方案可行但范围受限。** 不是所有配置字段都适合热重载：
   - `AUTH_KEYS` → 需要重建 Registry，不是简单的 `atomic.Value` 切换
   - `STORAGE_BACKEND` → 存储层切换是架构级变更，不适合热重载
   - `DB_DSN` → 无法热重载，需要连接池重建
   - `AI_EMBED_PROVIDER` → embedder 重建代价高

   文档的 `DynamicConfig` 示例聚焦于 `rate_limit_rps`、`readonly` 等——这是合理的。可以补充一个 `Config.ReloadableFields` 白名单文档说明哪些字段支持热重载。

3. **`DRAINING` 状态的问题：** 排空前必须关掉 SSE 连接和 Webhook 订阅器，否则它们会持有 in-flight 引用导致排空永远无法完成。文档边界情况表已提到 "排空前 SSE 连接尚未关闭"——这是正确的，但实现细节在于 `events.Bus.Close()` 会关闭所有 subscriber channels，需要确保在 `DRAINING → READONLY` 的过渡中，`Bus.Close()` 在 `ConcurrencyLimiter.Drain()` 之后、`DynamicConfig.ReadOnly = true` 之前调用。

---

## 总体评估

| 方面 | 评分 | 说明 |
|------|------|------|
| 代码准确性 | ★★★★★ | 锚点精确，行号一致，零误读 |
| 去重验证 | ★★★★★ | 逐方向做正则搜索，已排除 106 份既有文档的交叉覆盖 |
| 架构权衡 | ★★★★☆ | 方案设计完整，边界情况覆盖好。方向一 `CostScope` 有语义漂移，方向四过滤链解析可能低估复杂度 |
| 产品价值论证 | ★★★★★ | 每方向首节场景表清晰有力 |
| 优先级排序 | ★★★★☆ | P1/P2 分级合理。方向一（MCP 授权）在安全基线之外，方向二（跨租户面板）是运营刚需 |
| 附录验证检查点 | ★★★★★ | 每个检查点都设计为可自动化断言 |

**文档整体品质：** 高。是 `docs/requirements/` 下最有价值的深度分析之一。所有建议方案的生产可执行性强——没有过度设计的成分，每处都权衡了现有架构和实现代价。
