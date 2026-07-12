现在我已掌握完整背景。以下是架构分析。

---

# AeroVault 架构分析与扩展建议

## 1. 架构评估

### 1.1 当前架构的优势

AeroVault 的架构有多个设计亮点，值得在扩展中保留：

**清晰的层叠分层 + 单一服务骨干。** 协议适配器（REST/S3/WebDAV/MCP）均为薄层，全部汇聚到 `FileService` 这一核心控制器。这一模式使得跨协议一致性成为固有属性——无论通过何种协议写入的对象，对其他协议立即可见。与大多数对象的存储系统（如 MinIO 中 S3 原生逻辑与 REST 管理 API 分离）不同，AeroVault 的设计避免了"另一个 API 漏洞"。

**严格排序的中间件职责链。** 从 `RequestID` → `CORS` → `Auth` → `Tenant` → `RateLimit` → `OTel` → `Recoverer` → `AccessLog` 的顺序并非随意设定——每个中间件要么**建立**协程上下文（RequestID、Tenant），要么**保护**下游（Auth、RateLimit），要么**观察**（OTel、AccessLog）。这种不可变的有序性是安全正确的基础，必须通过架构手段强制执行，而非人工纪律。

**Opt-in 安全默认。** AI/pgvector/Qdrant 复制、集群模式、WebDAV 均为标记控制且默认关闭。`nil` embedder/llm/reranker 不会破坏核心 CRUD。这一"最小基线"原则是 CI gate 能够仅依赖 SQLite + 本地文件系统且零网络即可运行的根本原因——也是将新贡献者入职成本保持在较低水平的关键。

**存储 key 方案（tenant/bucket/key → path.Join）。** 单一前缀布局使得一个后端桶可为无限租户和逻辑桶提供服务。它有意避免反解析（GC 需精确 key 匹配），并且不可遍历（空 key、`/` 前缀、`..` 均在服务层拒绝）。这是一个经过深思熟虑的不变量（I3），绝不可破坏。

**事件驱动 worker 异步。** `EventBus` + `JobQueue` 模式的组合，使得 `FileService` 能够异步触发索引、扫描、复制和 webhook，而不会阻塞写路径。`JobQueue` 还提供了重试语义和持久化保障——这是生产级 worker 的基础。

### 1.2 局限性（架构债务）

**中间件链正在达到临界复杂度。** 当前 8 个中间件。文档 1 建议在此链中插入 EgressLimiter（`RateLimit` 之后），文档 3 建议添加 APIVersion 和 Deprecation 中间件，文档 4 建议添加 GeoRoute 中间件。若全部实施，中间件链将膨胀至 12+ 个组件——已接近维护负担超过收益的临界点。此时应进行中间件**分组**或**子路由**，而非进一步线性扩展。

**协议异构错误格式是深层债务。** REST → JSON，S3 → XML，MCP → JSON-RPC，WebDAV → XML。每个协议适配器实现自己的错误转换。当添加新错误码或字段时，所有四层均需修改。统一错误类型（文档 3 中提出的 `APIError`）是必需的最起码措施。

**没有任何层实现熔断器模式。** 外部依赖（AI 提供商、复制目标、webhook 端点）可能在降级模式下运行。当前代码要么重试直到超时，要么静默跳过——无熔断、无半开探测、无快速失败。对生产部署而言，这是操作风险。

**无限制的租户资源隔离。** 文档 1 正确识别了出口带宽问题，但更根本的缺失在于：一个无限循环的租户可以耗尽文件描述符（并发 GET 无上限）、填满磁盘（无存储配额硬上限）、或充斥作业队列。当前 `TenantQuota` 检查对象数量和字节数——但均在写路径上。没有读取路径上的资源治理。

**分析是完全盲区。** 尽管代码库已收集足够的数据（`LastAccessed`、`Object.UpdatedAt`、元数据、标签、`Backend`、已定义的 `EventAccessed`），但完全未将其用于任何分析能力。管理员无法回答最基本的问题："哪些数据是冷的？""版本成本是多少？""我可以节省多少钱？"

---

## 2. 高价值扩展方向

文档 1-5 覆盖了关键的缺失功能。以下是我认为按架构重要性排序的下一个层次的方向——超越功能缺口，关注系统结构。

### 方向 A（第零步）：中间件执行器模式——从线性链到插件化管道

**为什么需要。** 当前 8 个中间件按照 `cmd/server/main.go` 中的硬编码顺序连接。添加一个新中间件需要：（1）理解正确的插入位置，（2）手动编辑 main.go，（3）理解其相对于前后中间件的隐式依赖。没有中间件之间的"契约"——例如，Auth 在 Tenant 之前运行，因为 Auth 设置 Tenant——但这一关系未在代码中得到表达。

**核心挑战。** 提取一个由 `Phase` 和 `Priority` 约束的声明性中间件执行器：

```
type MiddlewarePhase int
const (
    PhaseIdentity   MiddlewarePhase = iota // RequestID
    PhaseSecurity                          // CORS, Auth
    PhaseTenancy                           // Tenant
    PhaseRateControl                       // RateLimit, EgressLimit
    PhaseObservability                     // OTel, AccessLog
    PhaseRecovery                          // Recoverer
)
type MiddlewareDef struct {
    Handler  func(http.Handler) http.Handler
    Phase    MiddlewarePhase
    Priority int      // 阶段内顺序
    Depends  []string // 可选：依赖性声明
}
```

**预期的架构变更。**
- 新包 `internal/middleware/executor.go`，通过 `opt-in` 注册构建链
- 在启动时进行拓扑排序以验证无循环依赖
- 按阶段分组进行单元测试——而非整个链的 `NewRouter` 集成测试

**对现有系统的影响。** 仅重构 `main.go`；`handler` 函数不变。向后兼容。

**复杂度:** S · **影响:** ★★★☆☆（内部质量） · **代码变更:** ~200 行新代码 + main.go 重构

---

### 方向 B：从被动存储到主动数据生命周期——存储类转换、过渡计划、成本归因

文档 2 涵盖了版本清理和分片上传 GC。这是生命周期的一部分。而文档 2 未覆盖的部分——**存储类转换**——才是真正的成本杠杆所在。

**为什么需要。** 在 AWS S3 中，生命周期转换（STANDARD → STANDARD_IA → GLACIER → DEEP_ARCHIVE）是节费的核心驱动力。AeroVault 的当前代码定义了 `StorageClass` 枚举，实现了 `STANDARD` / `STANDARD_IA` / `GLACIER` / `DEEP_ARCHIVE`，以及 `pending_transition` / `transition_ready_at` 字段——但从未真正**执行**转换。`reconcile/lifecycle.go` 仅删除对象，而不移动它们。

**核心挑战。** 转换意味着将字节从一个物理位置（hot SSD）移动到另一个物理位置（cold HDD 或远程归档）。这需要：

- 衡量存储层的相对成本与延迟特征
- 一个协调循环，扫描 `transition_ready_at` 到期的对象，并调用 `storage.Copy` 或 `storage.Move`
- 转换对象版本的语义（flat vs preserved）
- 计量层面：重新计算租户配额（降冷后，已用字节可能减少——具体取决于分层定价）
- `StorageClass` 值需要映射到后端特定的桶/前缀配置（例如 S3 的 `Transition` 规则 vs 本地文件系统的目录移动）

**预期的架构变更。**
- 在 `BucketConfig` 中添加 `TransitionRules []TransitionRule`（文档 2 的蓝图已有定义）
- 将 `storage.Storage` 扩展 `Move(ctx, srcKey, dstKey, StorageClass) (ObjectInfo, error)` 方法——此处存储类的语义由后端定义
- `reconcile/transition.go` 中的新协调 job 处理到期转换
- 实现 `TransitionRule` 中的 `Date` 和 `Days` 条件
- 架构抉择：`Move` 应在存储层（原子重命名，如本地文件系统或 S3 CopyObject）完成，还是通过重新读取并重新写入完成？

**对现有系统的影响。** 中等。需要对所有四个 `Storage` 后端进行 `Move` 更改。`TransitionRules` 需要与现有 `BucketConfig` 并存。迁移工作。

**科技选型权衡。** 对于 S3/OSS/COS 后端，`Move` 是 `CopyObject + DeleteObject`——由 SDK 原生支持。对于本地文件系统，需要一个原子重命名，后端内已有支持（文件移动）。关键设计决策：`Move` 应该在 `Storage` 接口中，还是应该作为 JobQueue 中独立于存储层的协调过程来实现？**建议：** 在 `Storage` 中新增 `Move`——它让每个后端以自己的方式实现高效转换（S3 的 CopyObject、本地文件系统的 rename(2)），而非使用低效的 read+write 循环。

**复杂度:** L-M · **影响:** ★★★★★（成本可控性） · **代码变更:** ~800 行

---

### 方向 C：连接熔断器与背压——韧性基础设施

该文件涉及速率限制和带宽治理，但缺乏**韧性模式**。在生产部署中，外部依赖以不可预测的方式失效。当前代码在重试时阻塞，在失败时跳过，但从未"熔断"——即当依赖项处于降级状态时，主动且快速拒绝请求。

**为什么需要。** 考虑以下场景：

1. AI 嵌入提供商（Ollama/OpenAI）开始返回 429 或 503。索引器重试 3 次，每次阻塞写入路径 30 秒。
2. 复制目标区域网络中断。复制 worker 重试直到超时，积压了队列。
3. 一个坏 actor 租户发送了大量并发请求——`ConcurrencyLimiter` 限制只适用于线程数，而非连接数。后端在 accept 队列中积压。

**架构蓝图建议。** 采用 `google-sre` 风格的熔断器：

```
type CircuitBreaker struct {
    state         int32  // closed | open | half-open
    failures      int64
    threshold     int64      // 在open前的连续失败数
    cooldown      time.Duration  // 在进入half-open前的等待时间
    lastFailure   time.Time
    halfOpenMax   int64      // half-open中允许的最大请求数
}
```

- 嵌入调用、复制发送、webhook 投递和外部提取应都通过熔断器包装
- 熔断器状态应暴露在 `/metrics`（`circuit_breaker_state{name="embedder"}`）和 `/healthz` 中
- 熔断器**必须**在租户层面或全局层面？**建议：** 外部依赖用全局，带 `per-tenant` 故障计数器（防止一个坏租户熔断全局）

**对现有系统的影响。** 低加。仅在外部调用点引入包装。不改变核心 CRUD。

**复杂度:** S-M · **影响:** ★★★★☆（生产稳定性） · **代码变更:** ~400 行 + 跨外部调用的包装集成

---

### 方向 D：元数据索引策略——从 SQL 扫描到可扩展查询

**为什么需要。** 当前，`ListObjects`、`Search` 以及所有管理分析查询均通过同一 `Repository` 进行 SQL 扫描。在 1 亿对象下，`SELECT COUNT(*)`、带前缀过滤的列表、按标签聚合以及时间范围扫描将变得缓慢。当前没有辅助索引策略：没有物化视图、没有只读副本、也没有 CQRS（命令查询职责分离）。

**核心挑战。**

- `objects` 表是写入主表。分析查询（文档 5 中的 `LastAccessedAt` 聚合）在该表上运行，与写入 CRUD 竞争。
- 前缀列表本质上是指向性扫描——`WHERE key LIKE 'prefix/%'` 如果没有覆盖索引，需要全表扫描。当前 schema 上未生产 `(tenant_id, bucket, key)` 索引。
- 标签（`tag` 表中的键值对）需要对标签进行过滤查询——这在高基数下会变得代价高昂。

**架构蓝图建议。** 按优先级顺序：

1. **低优先级：** 添加复合索引（`(tenant, bucket, key)`、`(tenant, last_accessed_at)`、`(tenant, content_sha256)`）
2. **中等优先级：** 引入核心 `list` 和 `aggregate` 操作的**覆盖索引**——将 `size`、`storage_class`、`updated_at` 作为包含列，以避免回表查询
3. **高优先级：** 对于 Postgres 后端，实现**分区**——按 `tenant_id` 或按 `updated_at` 时间范围分区
4. **长远（P2）：** `CQRS` 模式——一个专门的**读模型**，将对象元数据去规范化为分析优化的结构（宽表 + 物化聚合），通过 `EventBus` 事件——`object.created`、`object.deleted`、`object.accessed`——异步更新

**对现有系统的影响。** 低加。仅变更 schema + repository 查询。不引入新依赖。

**复杂度:** M · **影响:** ★★★☆☆（性能） · **代码变更:** ~300 行 SQL + 也许加新索引

---

### 方向 E：租户层级资源治理——超越配额到预算控制、自动伸缩和公平调度

**为什么需要。** 当前 `TenantQuota` 有 `max_bytes` 和 `max_objects`——在写入路径上检查的硬上限。但没有以下能力：

- 存储预算（每月 $X）：当存储成本超出预算范围时，触发告警/节流
- 出口预算：与文档 1 的 EgressLimiter 挂钩，但增加了计费层面（$X/月出口配额）
- 请求预算：每个租户一天内可以执行多少次 API 调用（独立于 RPS，后者是速率限制而非总量限制）
- 分层服务：`free`、`pro`、`enterprise` 层级映射到不同的配额、速率限制和存储类访问权限

**架构蓝图建议。** 扩展 `TenantRecord`：

```go
type TenantRecord struct {
    ID        string
    Name      string
    Tier      string          // "free" | "pro" | "enterprise"
    Status    TenantStatus

    // 存储配额（当前有）
    MaxBytes  int64
    MaxObjects int64

    // 新增：预算
    MonthlyBudgetCents     int64    // 每月最大成本（美分）
    EgressMonthlyBudgetCents int64

    // 新增：分层限制
    AllowedStorageClasses  []string  // nil = 全部允许
    AllowedFeatures        []string  // ["versioning", "object-lock", "replication", "cdn"]

    // 新增：告警阈值（占总预算的百分比触发 webhook/通知）
    WarnThresholdPct  int   // 默认 80
    CriticalThresholdPct int // 默认 95
}
```

**实现路径。**

1. 将 `TenantRecord` 扩展上述字段（迁移 0026）
2. 添加 `internal/service/tenant_budget.go` — 预算检查 + 消耗跟踪（通过事件）
3. 在写路径（存储费用）和读路径（出口费用）中挂钩预算检查
4. 添加 `GET /v1/admin/tenants/{id}/usage` — 当前周期累计使用情况
5. 可选：添加预算告警 webhook（当租户达到预算的 80%/95% 时）

**对现有系统的影响。** 中等。需要新的迁移、`TenantRecord` 变更、以及 `FileService` 中的挂钩。**预算检查绝不能阻塞写路径**——建议使用基于事件的异步消耗跟踪，在单独协程中检查预算阈值。

**复杂度:** M · **影响:** ★★★★☆（多租户经济模型） · **代码变更:** ~600 行

---

## 3. 接口设计原则

### 3.1 对当前接口契约的观察

**`storage.Storage` 接口（当前 11 个方法）** 是最成熟的抽象。它是精简的、后端不可知的，并且有合约测试。扩展时需谨慎：向其添加 `Move` 是合理的（如方向 B），但添加 `BulkDelete`、`BulkCopy` 或 `Backup` 将违反其专注于单对象操作的定位。对于批量操作，使用 JobQueue 中的协调循环。

**`repository.Repository` 接口（~40 个方法）** 正在增长。它覆盖对象 CRUD、版本管理、标签、ACL、配额、配置、事件、作业、AI 表和现在的分析查询。它应该被分解：

- `ObjectRepository` — 对象/版本 CRUD + 列表
- `ConfigRepository` — 桶配置、生命周期规则、复制规则
- `AnalyticsRepository` — 聚合查询、成本预测、标签统计
- `JobRepository` — 作业、webhook 失败
- `AIChunkRepository` — chunk、embedding、usage 行

**何时拆分。** 不是在本次冲刺中，而是当 `repository.go` 超过 300 行或方法数量超过 50 个时——根据 AGENTS.md 约束。

### 3.2 新抽象层：配置存储与运行时配置

当前，所有桶级配置（versioning、lifecycle、CORS、ACL）都在 `BucketConfig` 中作为仓库中的结构化字段存储。这随着生命周期规则（方向 2）和 CDN 配置（方向 1）的扩展而变得难以管理。

**建议。**

```go
type ConfigStore interface {
    GetBucketConfig(ctx, tenant, bucket, key string) (string, error)
    SetBucketConfig(ctx, tenant, bucket, key string, value string) error
    DeleteBucketConfig(ctx, tenant, bucket, key string) error
    ListBucketConfig(ctx, tenant, bucket) (map[string]string, error)
}
```

- 将特定于配置的 `BucketConfig` 字段迁移到键值对中
- 保持 `BucketConfig` 作为冷启动缓存，而在 `ConfigStore` 中持久化
- 允许在运行时添加配置属性而无需 schema 迁移（例如 `bucket:cdn.enabled`、`bucket:lifecycle.rule_0.filter_prefix`）

### 3.3 向后兼容性策略

| 变更类型 | 策略 | 示例 |
|----------|--------|---------|
| 新 API 端点 | 安全；在现有路由旁添加 | `POST /v1/admin/analytics/cost-projection` |
| 新请求头 | 安全；旧客户端忽略未知头 | `Accept-Version`, `X-Aero-Region` |
| 新响应头 | 安全；旧客户端忽略未知头 | `X-RateLimit-*`, `Deprecation`, `Sunset` |
| 新查询参数 | 安全；旧客户端不发送新参数 | `?cursor=`（与 `?marker=` 并列） |
| 响应添加新字段 | 安全；JSON 解析器忽略未知字段 | 在搜索命中中添加 `citations` |
| 移除响应字段 | 破坏性；必须通过弃用流程 | 在移除前 6 个月添加 `Deprecation` 头 |
| 更改请求体 schema | 破坏性；需要版本协商 | 从 `/v1` 迁移到 `/v2` |
| 更改现有路由的 HTTP 方法 | 破坏性；需要版本协商 | `DELETE /v1/buckets/{b}` → `POST /v1/buckets/{b}/delete` |

弃用中间件（文档 3）在技术上很简单，但**策略层面**很困难：谁决定何时弃用端点？如何通知客户端？建议增加一个 `DeprecationPolicy` 配置映射：

```go
type DeprecationPolicy struct {
    MinNoticeDays    int           // 弃用和移除之间的最短天数
    DefaultSunset    time.Duration // 弃用后假设的默认寿命
    OverrideHeaders  bool          // 即使没有配置的弃用也添加 Sunset 头
}
```

---

## 4. 技术选型

### 4.1 需要评估的候选新依赖

| 依赖 | 用途 | 评估标准 |
|------|---------|----------|
| **maxmind/geoip2** | 地理路由（方向 4）中的 GeoIP 查找 | 许可证（CC BY-SA 4.0 用于 GeoLite2 免费数据库）、内存占用（~50MB 用于完整 GeoIP2 City）、更新频率 |
| **openapi-generator / oapi-codegen** | SDK 生成（方向 3） | 生成的 SDK 质量、Go/JS/Python 中的定制钩子支持、对非 standard HTTP（SSE）的处理 |
| **eclipse/paho.mqtt.golang** | 跨区域事件广播（替代 HTTP 拉取） | 延迟、持久化、与现有 EventBus 的集成复杂度；可能过度 |
| **segmentio/ksuid** | 全局排序的唯一 ID（用于版本、复制） | 比 UUIDv4 更具可排序性，比 Snowflake 更少依赖协调 |
| **hashicorp/go-memdb** | 内存中分析读模型（方向 D 的 CQRS） | PostgreSQL 后端的可行性（备选：PostgreSQL 物化视图） |

### 4.2 自建 vs 购买决策

| 能力 | 自建代价 | 购买/集成 | 建议 |
|------|----------|----------|----------|
| **GeoIP 路由** | 低（~200 行，maxmind 库） | N/A | 自建 — 这是少量封装代码 |
| **SDK 生成** | 高（维护 3 个手动 SDK） | openapi-generator 或 fern | 购买 — 采用 openapi-generator（成熟、多语言），定制 ~20% 的包装器 |
| **成本分析仪表盘** | 高（前端 + 后端聚合 + 存储） | Grafana + Prometheus / Datadog | 混合 — 在 `GET /v1/admin/analytics/*` 中暴露原始数据端点；让用户将 Grafana 指向 `/metrics` 和 API |
| **跨区域消息总线** | 高（持久化、排序、恰好一次） | Kafka / NATS / RabbitMQ | 等待 — 在到达跨区域复杂度的拐点前，当前 HTTP 拉取 + JobQueue 可行 |
| **AI 嵌入提供商** | 极高（从零训练模型） | OpenAI API / Ollama / Cohere | 购买 — 当前抽象（`Embedder` 接口）已经正确；不需要原生嵌入 |

### 4.3 技术风险矩阵

| 风险 | 可能性 | 影响 | 缓解 |
|------|----------|--------|----------|
| SQLite 在 1M+ 对象下写入争用 | 中 | 高 | 文档建议 PostgreSQL；在超过文档 5 的分析查询中，SQLite 将退化。添加 `DB_DRIVER=postgres` 作为扩展的标准建议 |
| 在分析查询中 `LastAccessedAt` 更新与当前 `GET` 路径争用 | 高 | 中 | 异步更新：`EventAccessed` → EventBus → 分析消费者批量更新 `LastAccessedAt`（每 N 次访问或每 T 秒刷新一次） |
| 生命周期模拟 API 读取大量数据（百万级对象） | 中 | 中 | 添加 `limit` + `timeout` 查询参数；在 Postgres 中使用 `EXPLAIN` 验证查询计划 |
| 跨区域复制的网络分区导致无限重试 | 中 | 高 | 添加熔断器（方向 C）+ `max_retention_duration`（复制 worker 中最大的 backoff） |
| `Move` 到 `storage.Storage` 破坏了后端合约测试 | 低 | 高 | `Move` 必须对每个后端分别实现；合约测试必须验证 `Move` 在新旧 key 上的正确性 |

---

## 5. 实施路线图

### 总结的 5 个方向的优先级

方向文档提出的顺序是：#2 → #5 → #1 → #3 → #4。我认同将**成本控制**放在首位的基本思路，但我会重新排列为**成本可见性 → 治理 → 效率 → 平台 → 全球化**：

| 优先级 | 方向 | 理由 |
|----------|----------|------------|
| **P0** | #5 分析引擎 + #2 生命周期 | 在优化之前，必须可见。分析引擎（#5）回答了"我的成本构成是什么？"；生命周期（#2）基于这些数据执行优化。它们共同构成了**成本反馈闭环**。 |
| **P1** | #1 出口治理 | 多租户公平性是一项准入要求。没有它，企业中第一个下载大数据集的租户就会影响到所有其他人。 |
| **P1** | #3 API 治理（在 SDK 生成前进行错误统一和弃用） | 在 API 表面硬化之前，错误格式和标准化分页应先修复。OpenAPI → SDK 生成应在 API 表面稳定后再进行。 |
| **P2** | #3 API 治理（SDK 生成 + 版本协商） | 对开发者体验重要，但在基本成本控制和公平性之后进行。 |
| **P2** | #4 多区域复制 | 最大投入和最复杂。仅当有明确的跨区域部署需求时才启动。 |

### 阶段划分

#### 阶段 1：成本可见 + 生命周期控制（8-10 周）

**里程碑 M1**（第 4 周）：
- `objects` 表的 `migration 0025`：添加 `last_accessed_at`、`content_sha256`、索引
- `EventAccessed` 消费：已定义但未使用——新分析消费者从 EventBus 读取并批量更新 `last_accessed_at`（批处理大小为 N，刷新间隔为 T）
- `GET /v1/admin/analytics/access`：按租户/桶/前缀的访问模式热力图
- `GET /v1/admin/analytics/idle-objects`：`N` 天未访问的对象

**里程碑 M2**（第 8 周）：
- `migration 0026`：将 `lifecycle_rules` 拆分为独立表
- 实现 `NoncurrentVersionExpiration`、`AbortIncompleteMultipartUpload`、`ExpiredObjectDeleteMarker`
- 新的 reconcile sweeper：`sweepNoncurrentVersions`、`sweepAbandonedUploads`
- `GET /v1/admin/analytics/version-overhead`：版本比率的可见性

**里程碑 M3**（第 10 周）：
- `POST /v1/buckets/{bucket}/lifecycle/simulate`：只读规则预览
- `GET /v1/admin/analytics/recommendations`：基于规则的优化建议
- 文档 5 的 `cost-projection` what-if 分析

**风险。** 对 1M+ 个对象的大型 `last_accessed_at` 更新可能导致争用。缓解：批处理更新 + 异步刷新（每 1000 次访问或每 60 秒刷新一次）。`cost-projection` 查询对于大范围可能超时。缓解：添加 `max_objects` 限制和 `timeout` 参数。

#### 阶段 2：出口治理 + API 卫生（6-8 周）

**里程碑 M4**（第 4 周）：
- `EgressLimiter` 中间件：基于租户 token-bucket 的带宽计量
- `X-RateLimit-*` 响应头（Limit、Remaining、Reset）用于全局和 AI RPS
- 指标 `egress_bytes_total{tenant}` 导出到 Prometheus

**里程碑 M5**（第 8 周）：
- `APIError` 统一错误类型 + 协议转换器（JSON/XML/JSON-RPC/WebDAV）
- 在 `Service` 层重构 `Err*` 常量为 `APIError` 值
- 弃用中间件（RFC 8594 `Sunset` + `Deprecation` 头）
- 标准化分页：`cursor` 参数 + `pagination` 响应格式（`marker` 保持向后兼容）

**风险。** `EgressLimiter` 增加了每个 `GET`/`HEAD` 响应的开销。衡量 `ResponseWriter` 包装器的性能影响。缓解：流式写入期间的批量 token 扣除（每 64KB 计数一次，而非每次 `Write` 调用计数一次）。

#### 阶段 3：平台 + 全球化（10-14 周，取决于需求）

**里程碑 M6**（依赖先行）：
- OpenAPI 规范完整性：覆盖所有路由 + schema
- SDK 生成管道：`make sdk-python`、`make sdk-javascript`、`make sdk-go`
- 将手动 SDK 文件替换为生成代码 + 包装器

**里程碑 M7**（设计先行）：
- 复制拓扑模型：单对多规则 + 过滤
- `Move` 到 `storage.Storage` 实现
- 冲突解决：LWW 和 Lamport 时钟
- 配置复制：桶配置变更 → 跨区域事件广播
- 复制可观测性指标和 API

**风险。** 多区域复制是最复杂的变更，文档 4 完全承认这一点。最大的未知：在跨越两个区域的复制 worker 中，冲突解决将如何表现？建议在启动前进行 `make test-integration-replication` 与 Docker-compose 双区域设置。

### 风险总结与缓解

| 风险 | 范围 | 缓解 |
|------|---------|----------|
| 异步 `LastAccessedAt` 更新延迟导致分析过时 | 阶段 1 | 接受最终一致性；对实时场景使用 `EventAccessed`（SSE）；对低成本近实时使用批处理刷新 |
| `EgressLimiter` 的并发大下载导致 `sync.Mutex` 争用 | 阶段 2 | 每个租户使用 `atomic.Int64` 而非 `sync.Mutex` 进行 token 计量；使用无锁 token-bucket 实现 |
| 来自 CDN origin 的 `Range` 请求路径低效（随机 IO + `io.CopyN` 浪费） | 阶段 2 | 这是一个已知问题（文档 1 已标记）；优化 `Range` 请求处理以支持顺序读取 |
| SDK 生成覆盖率缺口（SSE、流式、身份验证包装器） | 阶段 3 | 为所有 SDK 中的这些缺口建立手动包装器；在 `Makefile` 中为包装器添加测试 |
| 复制拓扑循环（A→B→A）的数据覆盖 | 阶段 3 | 在每个事件中添加 `ReplicaRegion` 标记；检查目标区域不等于来源区域 |

---

## 结论

AeroVault 的架构基础稳固——分层架构、强不变量以及 Opt-in 设计为扩展提供了坚实的基础。五个文档方向正确识别了生产缺口：**成本可见性、生命周期控制、出口治理、API 产品化和多区域复制**。

我最高优先级的建议是：

1. **将分析（#5）和生命周期（#2）合并为一个阶段**——它们在"测量 → 优化 → 验证"周期中互为前提。
2. **不要触及中间件链直到完成第一阶段**——EgressLimiter（#1）将是对中间件执行器的第一次真正压力测试。届时考虑将其分解为按阶段分组的插件化管道（方向 A）。
3. **接受最终一致性用于分析**——在关键写入路径上同步更新 `LastAccessedAt` 将不必要地增加延迟。使用批处理异步更新是正确的权衡。
4. **设计错误统一作为 API 治理的第一步**——在硬化 OpenAPI 规范（#3）之前，统一错误格式减少了一整类 SDK 兼容性问题。
5. **跨区域复制（#4）是最大投入，从架构上保留到最后**——但设计分析/生命周期/出口系统时，要考虑跨区域维度（每个区域独立计量、每个区域独立生命周期扫描、跨区域复制事件）。
