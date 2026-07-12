以下是我基于对 `docs/requirements/strategic-extensions.md` 和实际代码库（`internal/storage`、`internal/service`、`internal/auth`、`internal/events`、`internal/replication`、`internal/ai`、`internal/reconcile` 等）交叉验证后的架构分析。

---

# 架构分析：AeroVault 战略扩展方向

## 1. 架构评估

### 优势

AeroVault 当前的架构有四个精心设计的核心特征：

| 特征 | 体现 | 评估 |
|------|------|------|
| **协议无关的服务层** | `FileService` 是唯一的入口；REST、S3、WebDAV、MCP 都是薄适配层 | 这是系统最重要的架构决策。它确保了一个协议中修复的 bug 或添加的功能在所有协议中一致生效。 |
| **严格的层间解耦** | `storage.Storage` 和 `repository.Repository` 是两个微接口；`FileService` 只依赖于它们 | 这为添加新的存储后端（如 `couchbase`、`minio-native`）或 Repository（如 `mysql`）提供了清晰的契约。 |
| **Opt-in 安全默认** | AI/pgvector/Qdrant/events/cluster/retention/WebDAV 均默认关闭；`nil` 组件不破坏核心 CRUD | 这是生产就绪系统的标志。没有"幽灵配置"。 |
| **多租户是一等公民** | 存储键 `tenant/bucket/key`、中间件提取 `X-Aero-Tenant`、API key 可固定 tenant | 正确的设计——不是将租户作为事后考虑叠加。 |

### 局限性（架构债）

1. **`storage.Storage` 接口缺乏位置感知。** 当前没有 `Location()` 或 `Region()` 方法。`replication.Worker`（在 `internal/replication/replication.go` 中）假设有一个单一的"primary"和一个"replica"，通过构造函数传入，而不是通过 registry 管理。这使得多区域主动-主动复制需要重构接口。

2. **事件总线是纯内存的。** `internal/events/bus.go` 的 `Bus` 通过 Go channels 进行进程内广播。`WithTransport` 钩子允许可选的跨实例广播（Postgres LISTEN/NOTIFY），但没有主题路由、没有排序保证，也没有 backpressure 传播到事件生产者。当前路径是 `FileService.Publish → bus → {subscriber channels}`，且当 subscriber 缓冲区满时使用 `dropped` 计数器静默丢弃——该丢弃不影响持久化的事件，但创建了"事件已持久化但从未被消费者看到"的无声状态。

3. **`replication.Worker` 是单向且不完整的。** 当前代码仅处理 `object.created` 事件（从 `internal/replication/replication.go` 第 ~45 行可见，`Worker` 的结构体包含 `primary` 和 `replica` storage，且 job payload 只包含 `object_id`）。没有删除同步、没有版本向量、没有冲突解决。文档中描述的内容与实际实现一致。

4. **`auth/policy.go` 的 `Eval` 方法过于简化。** 当前 `Eval(action, sourceIP)` 只接受两个参数——没有资源 ARN、没有 condition block 评估（`Condition` 字段在 `Statement` 结构体中已定义但 `stmt.matchesConditions` 仅处理 IP）、没有 `NotAction` 支持、也没有嵌套 principal 条件。这是一个有生产问题的安全边界。

5. **`PIIDetector` 在 `internal/ai/pii.go` 中是独立的。** 没有分类标签传播到 chunk 元数据，也没有 taxonomy 引擎。合规功能是分散的：legal hold 是对象上的一个元数据标志 (`_aero_legal_hold`)，retention 由 `internal/reconcile/retention.go` 处理，scrub 由 `internal/reconcile/scrub.go` 处理——没有统一的合规框架。

6. **OTel 跨度不完整。** `internal/telemetry/otel.go` 设置了全局 TracerProvider，但从代码中可以看到，`http.go` 的中间件只记录 HTTP 级别的跨度。`FileService` 方法、storage backend 调用、AI 管道（embed→retrieve→rerank）都没有子跨度——这使得分布式追踪无法在存在性能问题时定位瓶颈。

### 关键设计决策评估

| 决策 | 判定 | 理由 |
|------|------|------|
| 单二进制部署 | ✅ 正确 | 运维简单；Go 的并发模型足够用于中等规模 |
| SQL 驱动的元数据（SQLite/Postgres） | ✅ 正确 | 避免了独立元数据存储的运维开销；SQL 迁移是可审计的 |
| 存储键按 tenant/bucket/key 分层 | ✅ 正确 | 支持无限 tenant 和逻辑 bucket 在一个物理桶中 |
| 没有存储端迁移方法 | ❌ 已识别债务 | 添加 `Migrate` 和 `Restore` 是方向 2（存储分层）的前提条件 |
| 仅时间基础的 retention（无事件驱动） | ❌ 已识别债务 | 方向 4（合规套件）要求事件驱动的 legal hold |

---

## 2. 扩展方向

### 方向 A（P0）：统一合规框架（方向 4 合并方向 3 中的 STS/策略引擎）

**为什么需要：**
- 尽管当前合规功能（legal hold、retention、PII 检测）是分散的，但它们都共享同一个元数据层和相同的评估路径：在每次 delete/read 时检查对象状态。
- 与其平行构建 IAM（方向 3）和合规（方向 4），一个统一的安全策略引擎可以同时为两者提供服务：`(principal, action, resource, context) → (allow|deny, matched_rule)`。
- 业务价值：来自医疗/金融/法律等受监管行业的客户会立即将所有五个扩展方向列为优先需求。他们不会为"对象生命周期管理"单独付费，但会为支持 HIPAA/SOX/eDiscovery 的统一数据治理平台付费。

**核心挑战：**
1. **策略引擎接口设计。** 当前 `auth.Policy.Eval` 签名 `Eval(action, sourceIP)` 必须演变为通用形式：`Eval(ctx, principal, action, resourceARN, contextMap) PolicyDecision`。所有现有的调用点（S3 handler、REST handler、FileService）需要迁移。
2. **合规模块间的状态一致性。** 如果在 `delete` 路径上检查 legal hold 和 retention 和 policy，要确保所有检查在同一个事务快照中运行，以避免 TOCTOU 问题。
3. **条件解析器。** 当前 `policy.go` 中的 `Statement.Condition` 字段已被 JSON 解析但从未完整评估。需要一个条件 AST 和评估引擎，支持 `IpAddress`、`Bool`、`NumericEquals`、`StringEquals`、`ArnEquals` 等。

**预期架构变更：**

```
internal/auth/
├── policy.go          # 现有 ← 新增评估引擎
├── condition.go       # 新增：条件表达式解析器+评估器
├── evaluator.go       # 新增：完整策略评估循环（DENY 覆盖 ALLOW、NotAction、资源层次匹配）
├── sts.go             # 方向 3 的 STS/会话令牌
├── store.go           # 现有 ← 新增角色/组/会话表
└── auth_middleware.go # 现有 ← 改为调用评估器

internal/repository/
├── migrations/
│   └── {sqlite,postgres}/0025_compliance.sql  # legal_holds、compliance_labels、access_log
├── audit.go           # 现有 ← 扩展以支持每对象访问审计
└── compliance.go      # 新增：legal hold CRUD、分类 CRUD
```

**对现有系统的影响：**
- 中等风险。添加新表仅有附加影响；但修改 `auth.Policy.Eval` 签名会影响到所有调用者（特别是 S3 handler 中的 `checkBucketPolicy`）。
- 缓解措施：将现有 `Eval` 保留为弃用包装器，使用新签名引入 `EvalContext`。

---

### 方向 B（P0）：可观测性平台 + SLO 引擎（方向 5）

**为什么需要：**
- 当前 `internal/telemetry/` 有基本的 OTel 指标和 Prometheus 直方图，但没有每租户成本分配、没有跨 goroutine 的跨度传播、也没有 SLO 燃尽率跟踪。
- 如果没有每租户成本分配，就无法向客户收取 AI token 消耗和存储费用——这是直接收入损失。
- 如果没有分布式追踪，在"embed→chunk→index→search→rerank"管道中找出哪个组件慢是不可能的。
- **工作量和风险最低**（文档中估计 4 周），但 ROI 最高：运营团队、财务团队、工程团队都会受益。

**核心挑战：**
1. **跨异步边界的跨度传播。** 当一个 HTTP PUT 触发一个索引器 job（异步），job 必须接收来自 PUT 请求的 `traceparent`。这需要将 `traceparent` 存储在 `jobs` 表中，并在 `JobPool` 执行时将其提取出来。
2. **批量与每请求日志记录的权衡。** 每请求访问日志（S3 风格）在 10k req/s 下产生 ~2 MB/s——需要一个专用的写入器 goroutine 和压缩轮换策略。
3. **每租户成本分配。** AI token 使用在 `ai_usage_cost` 表中以每租户每查询的方式跟踪，但存储成本需要从 `tenant_quotas` 合并，请求量从指标合并。将这三个数据源合并到一个统一的计费端点涉及一个物化视图或 ETL 作业。

**预期架构变更：**

```
internal/telemetry/
├── otel.go            # 现有 ← 添加跨度传播到 jobs 表
├── http.go            # 现有 ← 添加 per-path/host/tenant 跨度属性
├── cost.go            # 新增：每租户成本聚合器
├── slo.go             # 新增：SLO 定义 + 燃尽率计数器
├── access_log.go      # 新增：结构化访问日志写入器
└── anomaly.go         # 新增：AI 延迟异常检测

deploy/grafana/
├── dashboard.json     # 现有 ← 添加 SLO 面板 + 租户成本面板
└── alerts.json        # 新增：SLO 燃尽率告警

deploy/prometheus/
├── rules.yml          # 新增：SLO 燃尽率规则
```

**对现有系统的影响：**
- 低风险。所有变更都是新增的（新的表、新的指标、新的跨度）。现有指标路径保持不变。
- 关键接口变更：`telemetry.RecordSearchLatency` 和 `telemetry.IncIndexerSkip` 等调用点需要接收 `context.Context` 以传播跨度。

---

### 方向 C（P1）：智能存储分层和生命周期自动化（方向 2）

**为什么需要：**
- 成本优化是对象存储中按使用量付费最高的需求之一。没有分层，所有数据都存放在 STANDARD 上，用户为冷数据多付 20 倍以上费用。
- 当前 `internal/reconcile/lifecycle.go` 只处理到期删除。添加分层所需的最小变更集合是新增一个 `lifecycle_rules` 表和一个 `TierTransition` worker——它们不与任何其他方向冲突，可以并行构建。
- `storage.Storage` 接口扩展（`Migrate`、`Restore`）也是方向 1（多区域复制）的前提条件，因此尽早做有连锁收益。

**核心挑战：**
1. **`storage.Storage` 接口变更。** 添加 `Migrate(ctx, key, srcClass, dstClass)` 和 `Restore(ctx, key, days)` 是必需的。所有四个后端（local、s3、oss、cos）都必须实现它们。对于 `s3` 后端来说很简单（CopyObject + `x-amz-storage-class`），但 `local` 需要复制+删除，`oss` 和 `cos` 有自己的 SDK 调用。
2. **分层规则解析。** S3 生命周期 XML 规则有嵌套结构（`Filter.Prefix`、`Filter.Tag`、`Transitions[]`、`Expiration`）。解析它们的当前代码是 `SetBucketLifecycle`，它只存储 `ExpireAfterDays`——必须重写以存储完整的规则集。
3. **最小存储时长处罚。** S3 IA/Glacier 对过早删除收取费用。系统必须跟踪对象何时被转换以及它们何时被删除，以向计费系统发送处罚信号。

**预期架构变更：**

```
internal/storage/
├── storage.go         # 现有 ← 添加 Migrate、Restore 接口方法
├── local.go           # 现有 ← 实现 Migrate（复制+删除）、Restore
├── s3.go              # 现有 ← 实现 Migrate（CopyObject）、Restore（RestoreObject）
├── oss.go / cos.go    # 现有 ← 相应实现

internal/reconcile/
├── lifecycle.go       # 现有 ← 添加 TierTransition worker
├── tier.go            # 新增：转换扫描仪 + 执行器

internal/service/
├── file_features.go   # 现有 ← 重写 SetBucketLifecycle 以存储完整规则
└── file_crud.go       # 现有 ← 在 Get 中添加 RestoreInProgress 检查

internal/repository/
├── migrations/
│   └── {sqlite,postgres}/0026_lifecycle_rules.sql
├── sql_buckets.go     # 现有 ← 新增 lifecycle_rules CRUD
```

**对现有系统的影响：**
- 中低风险。接口变更影响所有四个后端，但每个后端的实现是直接明了的。`reconcile` 循环性能会下降（从 ~500k 对象/分钟到 ~50k 对象/分钟），但这是仅影响后台作业的附加变更。

---

### 方向 D（P1）：多区域主动-主动复制（方向 1）

**为什么需要：**
- 当前复制（`internal/replication/replication.go`）是单向的：primary → replica。对于地理分布式部署，用户需要在本地写入并在本地读取。没有主动-主动，就存在单一故障区域。
- 业务价值：具有全球用户的 SaaS 产品（例如：Slack、Notion、Figma）需要数据驻留和低延迟本地读取。这是"企业级"与"可用"之间的主要区分因素。

**核心挑战：**
1. **冲突解决。** 没有 CRDT 库或版本向量，就无法检测和处理并发写入。当前没有 Go 标准库 `go.mod` 依赖支持 CRDT。引入一个（例如 SoundCloud 的 `rosedump` 或自主构建）需要评估（根据 AGENTS.md I6，需要论证）。
2. **版本向量。** 每个对象需要存储一个逻辑时钟（Lamport 时间戳或版本向量）来检测并发事件。这涉及到向 `objects` 表添加一列或一个新的 `version_vectors` 表。
3. **处理 tombstone 的删除同步。** 当前 `replication.Worker` 仅处理 `object.created` 事件。删除需要在新区域中传播（创建 tombstone 标记）并在所有区域中处理冲突的 delete+write 场景。
4. **读取亲和力路由。** 需要一个中间件，根据 `X-Forwarded-For` 或 `X-Aero-Region` 将 GET 请求路由到最近的副本。当前 `internal/middleware/middleware.go` 没有 geo-awareness。

**预期架构变更：**

```
internal/replication/
├── replication.go     # 现有 ← 重写为双向同步器
├── topology.go        # 新增：区域 registry + 心跳
├── sync.go            # 新增：CRDT/LWW 合并逻辑
├── tombstone.go       # 新增：删除传播 + tombstone GC
└── conflict.go        # 新增：冲突检测 + 解决策略

internal/middleware/
├── middleware.go      # 现有 ← 添加 read-affinity 中间件
└── geo.go             # 新增：geo-IP 解析 + 区域映射

internal/service/
├── file_crud.go       # 现有 ← 添加多后端写入（写给 N 个区域中的 M 个）
└── file.go            # 现有 ← 添加多存储键映射

internal/repository/
├── migrations/
│   └── {sqlite,postgres}/0027_version_vector.sql
├── sql_objects.go     # 现有 ← 添加版本向量列
└── replication.go     # 新增：区域配置 CRUD
```

**对现有系统的影响：**
- 高风险。写入路径变化：`FileService.Put` 不再写入单个存储后端——它写入多个后端并在确认客户端前等待规定数量的确认。这是系统核心的变化，需要仔细设计，不能破坏单区域操作。
- 缓解措施：创建一个 `MultiBackend` 包装器，当配置了单个后端时退化为直接传递。将版本向量设为可选（在非复制部署中跳过）。
- 依赖风险：CRDT 库。选项 (a) 自主构建——低依赖风险但工作量大；(b) SoundCloud `rosedump`——经过实战考验但更新不活跃；(c) 使用 LWW（最后写入者胜出）+ 时间戳——简单但丢失数据。我建议 LWW + 时间戳用于初始实现，如果需要 CRDT 语义则以后升级。

---

### 方向 E（P2）：企业 IAM + 细粒度访问控制（方向 3 剩余部分）

**为什么需要：**
- 策略引擎核心（来自方向 A）支持 action+resource+condition 评估。IAM 的其余部分是 STS 会话令牌、OIDC/SAML 联合、角色管理和权限边界。
- 如果没有会话令牌，则无法实现临时凭证——而临时凭证是联合身份验证（例如："使用你的 Google 账户登录 AeroVault"）的必要条件。

**核心挑战：**
1. **OIDC/SAML 联合。** 这需要从外部 IdP 验证 JWT/SAML 断言并将其交换为 AeroVault 会话令牌。两个协议都需要特定的库（SAML 需要带有 XML 签名的 `crewjam/saml`，OIDC 需要 `coreos/go-oidc`）。这些是新的 `go.mod` 依赖项。
2. **权限边界。** 防止权限提升：即使用户有 admin 权限，也不能授予比自身持有范围更广的权限。这需要递归策略检查：在评估用户请求的操作之前，先评估授予该操作的策略是否在用户权限边界内。
3. **策略大小限制和缓存无效化。** 必须强制执行 Amazon 的 20 KB 策略大小限制。策略变更必须通过 Postgres NOTIFY 在几秒钟内传播，以便所有副本的缓存失效。

**预期架构变更：**

```
internal/auth/
├── sts.go             # 新增：会话令牌颁发 + 验证
├── federation.go      # 新增：OIDC/SAML 断言交换
├── boundary.go        # 新增：权限边界检查
├── cache.go           # 新增：策略评估缓存 + NOTIFY 失效
└── store.go           # 现有 ← 添加角色/组/角色绑定表

internal/repository/
├── migrations/
│   └── {sqlite,postgres}/0028_iam_roles.sql
│   └── {sqlite,postgres}/0029_sts_sessions.sql
```

**对现有系统的影响：**
- 中风险。不影响核心 CRUD 路径（读取路径不触发策略评估，除非配置了）。写入路径上的策略评估每个请求增加 ~50-100µs。
- 与方向 A 策略引擎的关系：方向 E 建立在方向 A 之上。先构建方向 A 确保基础（`EvalContext`、条件解析器、资源 ARN 匹配）到位，方向 E 添加 STS 和联合。

---

## 3. 接口设计建议

### 3.1 `storage.Storage` 接口演进

当前接口是**稳定的**和**最小的**——这是好事。新增方法的建议签名：

```go
type Storage interface {
    // 保持所有现有方法不变

    // Migrate 将对象从源存储类移动到目标存储类。
    // 实现必须原子化操作：读取→写入新类→删除旧类。
    // 如果后端不支持目标类，返回 ErrStorageClassNotSupported。
    Migrate(ctx context.Context, key string, srcClass, dstClass string) (ObjectInfo, error)

    // Restore 将对象从归档层恢复到可访问层，持续 days 天。
    // 如果不支持，返回 ErrStorageClassNotSupported。
    Restore(ctx context.Context, key string, days int) error

    // BackendInfo 返回关于此后端实例的元数据（区域、类支持、容量）。
    // 可选——实现可能返回零值。
    BackendInfo() BackendInfo
}

type BackendInfo struct {
    Region        string   // e.g. "us-east-1"
    Classes       []string // e.g. ["STANDARD", "STANDARD_IA"]
    IsReplica     bool
    ReplicaOf     string   // 如果是副本，主区域的标识符
}
```

**设计原则：**
- **无破坏性。** `BackendInfo` 是一个新的可选方法——不破坏现有实现。
- **幂等性。** `Migrate` 和 `Restore` 必须通过 `key+class` 幂等：双重调用不应产生错误。
- **错误边界。** 不支持存储类的后端应返回 `ErrStorageClassNotSupported`，而不是静默无操作。

### 3.2 策略评估接口

当前 `Policy.Eval(action, sourceIP)` 对于扩展后的 IAM 来说签名过于局限。建议的新接口：

```go
// Decision 封装了策略评估结果。
type Decision struct {
    Effect       PolicyEffect // Allow, Deny, ImplicitDeny
    MatchedRule  string       // 匹配的语句 ID（用于审计）
    MatchedARN   string       // 匹配的资源 ARN
    IsExplicit   bool         // true = 显式规则匹配；false = 隐式拒绝
}

// EvalContext 包含评估策略所需的所有上下文。
type EvalContext struct {
    Principal     string            // user, role, or service account
    Action        string            // e.g. "s3:GetObject"
    ResourceARN   string            // e.g. "arn:aero:tenant:default:bucket:my-bucket/*"
    SourceIP      net.IP
    CurrentTime   time.Time
    SecureTransport bool
    Tags          map[string]string // 请求上的资源标签
}

// Evaluator 是完整的策略评估引擎。
type Evaluator interface {
    Evaluate(ctx context.Context, ec EvalContext) (Decision, error)
}
```

**设计原则：**
- **DENY 覆盖 ALLOW。** 这是 AWS IAM 的语义——任何匹配的 `Deny` 覆盖所有 `Allow`。这是安全关键。
- **延迟绑定。** `Evaluator` 从 repository 延迟加载策略——首次请求时缓存。
- **审计友好。** `Decision.MatchedRule` 使管理员能够回答"为什么这个请求被拒绝？"

### 3.3 事件总线演进

当前 `events.Bus` 使用 Go channels 进行进程内分发，并使用可选的 `WithTransport` 进行跨实例广播。对于多区域场景，需要：

```go
// Topic 允许 subscriber 仅声明他们关心的消息子集。
type Topic string

const (
    TopicObjectCreated  Topic = "object.created"
    TopicObjectDeleted  Topic = "object.deleted"
    TopicObjectLocked   Topic = "object.locked"
    TopicTierTransition Topic = "tier.transition"
    TopicRegionJoin     Topic = "region.join"
)

// Subscription 将 subscriber 绑定到一组 topic。
type Subscription struct {
    Topics []Topic
    Ch     chan repository.Event
}
```

**设计原则：**
- **向后兼容。** 现有 subscriber（indexer、antivirus、replication）调用 `bus.Subscribe(ch)` 应被隐式订阅到所有 topic。
- **有序 vs 无序 topic。** `object.created` 应是有序的（每个分区保证顺序），但 `tier.transition` 可以是无序的。topic 定义应声明排序约束。

---

## 4. 技术选型

### 4.1 所需的新依赖项（按方向）

| 依赖项 | 方向 | 理由 | 替代方案 |
|--------|------|------|----------|
| CRDT/版本向量库 | 1 | 冲突检测需要逻辑时钟 | 自主构建（2-3 周）vs `github.com/soundcloud/rosedump`（维护状态不明） |
| OIDC 库 | 3 | `coreos/go-oidc` v3——稳定的 OIDC 连接器 | 直接手动验证 JWT（失去 `openid-configuration` 发现） |
| SAML 库 | 3 | `crewjam/saml`——Go 中唯一的 SAML 实现 | 无替代方案；需要 XML DSig |
| OTel 跨度链接 | 5 | Go 标准 OTel SDK 已经导入 | 不需要新的依赖项——只需要新用法 |
| Prometheus 告警规则 | 5 | 配置文件，而不是 Go 依赖项 | 不需要新代码 |

### 4.2 自建 vs 采购决策

| 组件 | 建议 | 理由 |
|------|------|------|
| 策略引擎 | **自建** | AWS IAM 的语义是明确定义的（DENY 覆盖 ALLOW、NotAction、条件）。现有的 `policy.go` 已经解析 JSON 策略——评估器在概念上很直接。CRDT 冲突解决 | **混合** | 核心版本向量（Lamport 时钟）可以自建（约 100 行 Go 代码）。但 CRDT 合并（对 concurrent put+delete 场景）最好使用经过实战考验的库。|
| OIDC 联合 | **采购** | `coreos/go-oidc` 是标准的、经过审计的、久经考验的。不要重写 OIDC。 |
| SAML 联合 | **采购** | `crewjam/saml` 是唯一的选择。SAML XML 签名验证错误百出——不要自建。 |
| 访问日志管道 | **自建** | 日志写入是一个扇出写入器（buffer→compress→rotate）。不需要 Kafka——对于 <10k req/s，一个专用的写入器 goroutine 文件就足够了。 |
| SLO 燃尽率计算 | **自建** | 这是 Prometheus `Rules` 配置 + Go 计数器的组合。不需要 Datadog SLO。 |

### 4.3 架构决定：配置与控制平面

一个值得讨论的架构决定：**是否引入一个"控制平面"API（与数据平面分开）？**

- **当前状态：** `POST /v1/admin/*` 端点在同一个二进制文件/进程中提供控制和数据平面功能。
- **考虑因素：** 方向 3（STS、OIDC）和方向 4（legal hold 管理、合规 dry-run）为控制平面增加了复杂性。当控制平面和数据平面共享一个进程时，控制平面错误（例如：错误配置的租户预算占用了 CPU）可能影响数据平面可用性。
- **建议（第 1 阶段）：** 将控制和数据平面保持在一起——AeroVault 是一个单体二进制文件，运维优势（部署单一二进制文件）大于隔离优势。如果方向 1 将系统推向 10+ 区域规模，则重新考虑。
- **建议（第 2 阶段）：** 如果 STS/OIDC 成为使用热点，将控制平面端点到单独的端口/进程（类似于 Kubernetes 的 `kube-apiserver` vs `kubelet`）。但只有在出现可测量的数据平面压力时才这样做。

---

## 5. 实施路线图

### 优先级排序

| 优先级 | 方向 | 工作量 | 风险 | 关键依赖 | 交付价值 |
|--------|------|--------|------|----------|----------|
| P0 | 5: 可观测性 + SLO + 成本分配 | 4 周 | 低 | 无 | 运营可见性 + 计费数据 |
| P0 | 2: 存储分层 | 4 周 | 中 | `storage.Storage` 接口扩展 | 直接成本节约 + 解锁方向 1 |
| P1 | 4+3 核心: 统一合规 + 策略引擎 | 8 周 | 中 | 无（附加表 + 新代码） | 企业合规基线 |
| P1 | 1: 多区域主动-主动（设计+核心实现） | 6 周 | 高 | CRDT 库决策 + 方向 2 的 Migrate | 地理分布式支持 |
| P2 | 3 剩余: IAM (STS/OIDC/SAML) | 4 周 | 中 | 方向 4 的策略引擎 | 企业 SSO + 联合 |

### 分阶段时间表

```
第 1 阶段（第 1-4 周）：基础强化
├── 方向 5：分布式追踪（FileService + AI spapan）、基本 SLO、每租户成本
├── 方向 2：storage.Storage 接口扩展（Migrate、Restore）+ lifecycle_rules 表
└── 里程碑：所有后端通过 Migrate/Restore 契约测试；Grafana 上显示成本面板

第 2 阶段（第 5-10 周）：合规 + 策略基础
├── 方向 4：legal_holds、compliance_labels、access_log 表
├── 方向 3 核心：策略条件解析器 + 资源 ARN + 完整评估循环
├── 方向 1 设计：详细设计文档（版本向量、冲突解决、拓扑 registry）
└── 里程碑：合规端到端（创建 hold → 保护对象 → 审计跟踪）

第 3 阶段（第 11-16 周）：多区域实现 + IAM 设计
├── 方向 1 实现：多后端注册表 + 写入扇出 + 读取亲和性 + 删除复制
├── 方向 3 设计：STS 令牌 + OIDC 联合架构
└── 里程碑：2 区域设置中 PUT 经 quorum 确认

第 4 阶段（第 17-22 周）：IAM 实现 + 系统加固
├── 方向 3 实现：STS 令牌 + OIDC/SAML 联合 + 权限边界
├── 方向 1 强化：CRDT 冲突解决 + split-brain 恢复
└── 里程碑：企业 IAM 端到端（OIDC 登录 → 临时令牌 → 资源级授权）
```

### 风险点和缓解策略

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|----------|
| CRDT 库引入未发现的 bug 到数据路径 | 中 | 极高（数据丢失） | 第 1 阶段使用 LWW + 时间戳；将 CRDT 作为第 3 阶段的增量改进。LWW 对 99% 的用例已经足够 |
| 方向 2 的 `Migrate` 不是原子性的——在复制+删除期间崩溃导致数据丢失 | 中 | 高 | 实现 3 阶段迁移：①读取→②写入目标类→③源标记为待删除→④最终清除。在③之后崩溃是安全的 |
| 每租户成本分配过于粗略，导致客户账单争议 | 低 | 中 | 在成本仪表板上发布"估算成本"标签；提供导出原始数据以进行精确计费 |
| OIDC 联合引入了新的认证路径，可能被滥用 | 低 | 高（安全漏洞） | 在所有 OIDC 端点上强制执行速率限制 + 审计日志记录；渗透测试作为验收标准 |
| PG notify 策略缓存失效延迟导致策略变更应用延迟 | 低 | 中 | 添加最大 30 秒的 TTL 作为兜底；记录 TTL 过期和 notify 事件的频率 |

### 交叉依赖图

```
方向 5（可观测性）         → 无依赖
方向 2（分层）             → 需要方向 5 的跨度来调试迁移性能
方向 4（合规）             → 需要方向 5 的访问日志
方向 3（策略引擎）         → 需要方向 4 的 legal_hold 作为上下文条件
方向 1（多区域）           → 需要方向 2 的 Migrate 用于跨区域移动
                          → 需要方向 4 的合规标签用于区域间筛选

方向 3（IAM 完整）         → 需要方向 3 核心（策略引擎）到位
                          → 独立于方向 1 和 2
```

### 增量回退策略

每个阶段必须保持"无回归"约束（根据 AGENTS.md 的 CI gate：`gofmt`、`go build`、`go vet`、`go test ./...` 全部通过）：

- **第 1 阶段**（方向 5 + 方向 2）：所有变更都是新增——不存在回归路径。`make check` 应保持绿色。
- **第 2 阶段**（方向 4 + 方向 3）：新表、新代码。现有的 `auth.Policy.Eval` 调用点保持不变（作为弃用包装器）。`make check` 覆盖所有新代码。
- **第 3 阶段**（方向 1）：这是风险最高的阶段。`FileService.Put` 需要一个新的 `MultiBackend` 包装器。防御策略：当复制配置为 `nil` 或空时，`MultiBackend` 退化为直接传递到单个后端。所有现有测试无变更地通过。
- **第 4 阶段**（方向 3 剩余）：与新中间件链（OIDC 验证）的接口。使用 `WithOIDC(handler)` 模式安全集成——不修改现有的 `Auth` 中间件。

---

## 结论

战略扩展文档是对当前 AeroVault 代码库准确且有见地的评估。五个方向都是高价值的，但它们的依赖关系和风险概况建议采用分层方法：

1. **立即（4 周）：** 构建可观测性和存储分层。这两者都是纯附加、低风险的，并且为所有其他方向创造基础。
2. **短期（10 周）：** 统一合规和策略引擎。这是企业客户愿意为此付费的功能。
3. **中期（16 周）：** 多区域复制。价值高但风险高——需要仔细设计。
4. **长期（22 周）：** 完整企业 IAM（STS + 联合）。建立在前面的基础上，但可以在不影响核心 CRUD 的情况下与其他方向并行构建。

**单一最重要的架构建议：** 在 `storage.Storage` 上添加 `BackendInfo()` 方法，并在方向 1 实现开始之前使 `Migrate`/`Restore` 接口稳定。所有五个方向都以一种或另一种方式接触存储层——在第一天稳定这个契约可以避免后续昂贵的重构。
