# 架构分析报告：死代码路径、治理模型缺口与不完全管线补齐

> **分析范围：** 基于全库深度验证结果及 `docs/requirements/expansion-v99-dead-code-paths-and-governance-gaps.md`  
> **分析视角：** 系统架构、接口设计、技术选型、实施路线  
> **前置阅读：** `docs/architecture.md`, `docs/configuration.md`, `AGENTS.md`

---

## 1. 架构评估

### 1.1 当前架构的核心理念与优势

当前系统采用了一个**高度解耦但未充分利用的分层架构**，其设计决策在方向上是正确的：

| 架构决策 | 优势 | 实例 |
|---------|------|------|
| **薄协议层 + 单一 FileService** | 跨协议功能一致性；S3/REST/WebDAV/MCP 共享同一业务逻辑 | 所有协议都通过 `FileService.GetObject()` 读对象 |
| **Storage/Repository 双重抽象** | 存储后端和后端 DB 可独立替换；local/S3/OSS/COS 共享同一接口契约 | `factory.go` 在启动时构造后端 |
| **EventBus 广播 + 可插拔 Transport** | 进程内消费者解耦于事件生产者；`WithTransport` 支持跨实例分发 | Antivirus、Replication、Indexer 均通过 `Subscribe()` 接入 |
| **Middleware 链顺序固定** | 鉴权 → 租户 → 限流 → 遥测，顺序明确且可审计 | 请求处理路径可预测 |
| **Opt-in 安全默认** | AI/Postgres/Qdrant/WebDAV 等均为 flag-gated，默认不开启 | baseline 测试路径零网络零 Docker |

### 1.2 核心架构债务

尽管分层清晰，验证结果揭示了**三类技术债**：

#### 第一类：管线断线（功能存在但无执行者）

这是最直接的技术债。代码库中含有"看起来完整"的功能路径，但实际上配置被持久化后永不执行：

```
          配置层             持久化层           执行层
方向一:  PUT ?notification → JSON 列        → [断线] Bus.Publish 零规则检查
方向二:  PUT ?logging      → LoggingConfig  → [断线] WriteAccessLog 无调用者（且是空存根）
```

**性质：** 这不仅是 bug，而是对用户信任的系统性损害——用户在 S3 SDK 中调用了 `put_bucket_notification_configuration()`，获得了 `200 OK`，认为配置已生效，但实际行为毫无变化。在没有日志或警报的情况下，这种断裂可能在生产环境中运行数月而不被察觉。

**成本分析：** 每次断线功能被"准实现"，后续开发者会在其上继续构建——例如为通知规则添加新字段、扩展 schema——从而持续增加维护成本而不产生任何用户价值。

#### 第二类：伪治理模型（接口存在但无强制力）

方向三（Object Lock）和方向四（生命周期）的问题是**保护机制可被轻易绕过**：

```
方向三: Legal Hold = 元数据字段 `_aero_legal_hold` → 覆盖式 PUT 可清除它
方向四: StorageClass 字段存在 → 但永不自动转换
```

**性质：** 比管线断线更危险——用户配置了"对象锁"并认为数据受保护，但实际上任何 API 调用都可以覆盖该元数据（覆盖式 PUT 不保留原元数据，见 `file_crud.go:371` 仅检查硬删除）。这与安全中的"安慰剂效应"类似：让用户产生虚假的安全感。

#### 第三类：评估范围不对称（策略不适用于所有入口）

方向五的桶策略仅在 S3 handler 层执行，REST API 层不评估：

```
S3 协议: PutObject → checkBucketPolicy() ✅
REST API: PUT /v1/files/... → [无策略检查] ❌
```

**性质：** 这是一个典型的"协议后门"。攻击者发现 S3 访问受策略保护后，可切换到 REST API 绕过同一策略。异步策略（策略在 S3 和 REST 间不一致）比无策略更危险，因为它产生安全假象。

### 1.3 架构债务根源分析

| 根本原因 | 表现 | 影响范围 |
|---------|------|---------|
| **功能优先于管线完整** | 先实现接口解析/持久化，后接执行器 | 方向一、二 |
| **最小实现满足测试"存在"** | S3 兼容测试仅验证解析/响应，不验证执行 | 方向一、二、四 |
| **元数据模型作为"快速字段"** | 复杂治理概念降级为字符串字段 | 方向三（Legal Hold = meta["_aero_legal_hold"]） |
| **协议隔离处理** | S3 和 REST 各自为政，共享业务逻辑但共享不完整 | 方向五（策略仅在 S3 层评估） |

---

## 2. 高价值扩展方向

### 方向 A：事件路由引擎（优先级 P0）

**从"广播总线"到"可过滤事件分发器"**

#### 为什么需要

当前的 `Bus.Publish()` 是一种**简单的 pub-sub 广播模式**：所有 subscriber 收到所有事件。这在系统规模较小时是合理的——antivirus, indexer, replication, webhook 每个都"自过滤"自己关心的事件。但缺陷在于：

1. **S3 通知规则**（方向一的核心发现）——用户通过 S3 SDK 配置了 `Filter`（prefix/suffix），但配置被保存后无人读取。这是用户可见的断线行为。
2. **SSE 流**无法按 bucket 过滤——SSE endpoint 当前只过滤 `tenantID`，所有 bucket 的事件混在一起。
3. **无法条件分发**——全局 webhook URL 收到所有事件，无法将"删除事件"发给 Slack、"上传事件"发给 ETL 函数。

#### 核心挑战与技术难点

| 挑战 | 难度 | 说明 |
|------|------|------|
| **规则热加载** | 中 | 每次事件查 DB 读通知规则不可接受（写入放大）。需要进程内缓存 + TTL + 变更通知 |
| **匹配性能** | 低-中 | S3 通知规则的匹配模式相对简单（prefix 字符串前缀、suffix 字符串后缀），非正则 |
| **多规则冲突** | 低 | 同一事件匹配多条规则时全部独立触发（同 S3 行为） |
| **事件保序** | 视需求而定 | S3 Event Notifications 不保证顺序；但特定场景（如审计）可能需要 per-bucket 顺序 |

#### 架构变更建议

```
当前:
  Bus.Publish() → 广播给所有 subscriber（索引器、AV、复制器、webhook）
                → webhook 使用单 URL

建议:
  Bus.Publish() → EventRouter (新增组件)
                    │
                    ├── 从缓存加载通知规则
                    ├── 匹配 event.Type × event.Key (prefix/suffix filter)
                    ├── 分发到匹配的 endpoint
                    │     ├── Webhook A (Slack: 仅 delete 事件)
                    │     ├── Webhook B (ETL: 仅 data/ingest/ 前缀的上传事件)
                    │     └── Job Queue (Lambda 模拟)
                    │
                    └── 同时广播给内部 subscriber（但携带 bucket 过滤）
                          ├── Indexer: 仅 .txt/.md/.pdf 文件
                          └── SSE stream: 仅用户所在 tenant 内对应 bucket 的事件
```

**关键组件设计：**

```go
// 新增组件：事件路由器
type EventRouter struct {
    ruleCache   *RuleCache       // 进程内缓存，TTL 5 分钟
    dispatchers map[string]Dispatcher // endpoint 类型 → 分发器
}

type RuleCache struct {
    mu     sync.RWMutex
    byKey  map[TenantBucket][]repository.NotificationRule
    lastFetch time.Time
    ttl    time.Duration
}

type Dispatcher interface {
    Dispatch(ctx context.Context, event repository.Event, rule repository.NotificationRule) error
}

// 内置分发器
type WebhookDispatcher  // 复用现有的 webhook.go 重试/持久化机制
type SSEDispatcher      // 新增：将事件推送到指定 tenant 的 SSE 连接
type JobDispatcher      // 新增：写入 jobs 表，由 JobPool 异步处理
```

**对现有系统的影响：**

| 变更点 | 影响范围 | 程度 |
|--------|---------|------|
| `Bus.Publish` 新增路由逻辑 | `internal/events/bus.go` | 中等（非破坏性，默认无规则时退化为广播） |
| 现有 subscriber 选择性订阅 | `cmd/server/main.go` | 推荐：迁移 subscriber 从全量接收改为按需订阅 |
| SSE 流添加 bucket 参数 | `internal/api/rest/sse.go` | 低 |

**兼容性策略：**
- 无通知规则的桶：EventRouter 退化为当前广播行为，零影响
- 有通知规则的桶：广播 + 规则分发并行，确保现有 subscriber 仍收到事件（直到它们迁移）
- 旧 subscriber 代码无需改动——可以继续使用 `Subscribe()`，EventRouter 仅在广播之外增加规则分发路径

---

### 方向 B：访问日志管线补齐 + 批量日志架构（优先级 P0）

**从"空存根"到"可配置的审计日志流"**

#### 为什么需要

`WriteAccessLog` 是目前全库最明显的"断线"——接口完整、SQL 完整、配置 CRUD 完整、迁移文件就绪，但空存根。这意味着：

1. **合规硬伤**：SOC2 / HIPAA / PCI DSS 均要求"记录所有操作"。当前日志仅存在于 `slog`（可能在容器重启后丢失），未持久化到 DB 或目标桶
2. **审计调查不可行**：无法回答"谁在什么时候读了什么文件"——是安全事件响应的硬阻断
3. **S3 兼容度**：`logging` 配置被保存但无日志产生，用户从 AWS Console 检查会看到一个配置"有效"但目标桶空空的桶

#### 核心挑战与技术难点

| 挑战 | 难度 | 说明 |
|------|------|------|
| **写入放大（Write Amplification）** | 高 | 每个 GET/PUT 请求产生一条日志。对于频繁访问的热桶，日志写入量可能达到数据写入量的 50-100% |
| **递归写入** | 中 | 日志写入目标桶时，该写入本身也触发 AccessLog → 日志写入 → 日志写入 →... 必须检测并跳过 |
| **API 层面的日志上下文** | 中 | 需要在 middleware 中捕获请求相关的完整上下文（tenant、bucket、key、method、status、latency、user_agent、request_id、source_ip） |
| **日志对象生命周期管理** | 低 | 日志本身也应受生命周期规则管理以避免无限累积 |

#### 架构变更建议

```
当前:
  middleware.AccessLog → slog.Info("http", ...) [仅输出到结构化日志]
  WriteAccessLog()     → return nil            [空存根]

建议:
  HTTP Request
    │
    ▼
  middleware.AccessLog (重构)
    │
    ├─→ slog (保留，用于运维调试)
    │
    └─→ AccessLogBuffer (新增组件)
            │
            ├─ 每个请求追加一条 AccessLogEntry
            ├─ 每 N 条记录或每 T 秒 flush
            │
            ▼
        AccessLogWriter
            │
            ├─ 查询 log_target_config 获取目标桶配置
            ├─ 格式化日志行（S3 标准服务器日志格式）
            ├─ 写入目标桶（通过 storage.Storage 接口）
            └─ 跳过递归（目标桶 = 当前请求的桶时跳过）
```

**写入放大缓解策略（关键设计决策）：**

| 策略 | 收益 | 代价 | 推荐度 |
|------|------|------|--------|
| **批量写入缓冲区** | 1000 条缓冲后一次写入，减少 1000× 写入次数 | ~5 秒延迟；进程崩溃可能丢失未 flush 的日志 | ⭐⭐⭐ 首选 |
| **异步写入 + 可丢弃** | 日志不阻塞用户请求，高负载下跳过部分日志 | 日志不完整（但比不存在好） | ⭐⭐ 可接受 |
| **Job Queue 写入** | 事件驱动，重启安全 | 每个日志条目一个 DB 事件 → 写入放大更严重 | ❌ 不推荐 |
| **本地文件 + 外挂日志代理** | 写入放大最小；Filebeat/Fluentd 自动采集 | 需要外部日志管线；跨实例日志聚合复杂 | ⭐ 可选 |

**对现有系统的影响：**

| 变更点 | 影响范围 | 程度 |
|--------|---------|------|
| 重构 `middleware.AccessLog` | `internal/middleware/middleware.go` | 中等——不改变签名，新增逻辑 |
| `WriteAccessLog` 替换为批量 API | `repository.WriteAccessLog` 迁移为新签名或替换层 | 中等 |
| 新增 `AccessLogBuffer` | `internal/service/` 或新增 `internal/audit/` | 新组件 |
| 日志格式定义 | 需要标准化日志行格式 | 低 |

---

### 方向 C：存储类状态机 + 分层后端（优先级 P1）

**从"二元过期"到"对象生命周期管理"**

#### 为什么需要

当前生命周期仅支持 `soft_delete` / `hard_delete`——这是最粗粒度的数据管理。对于对象存储产品来说，**存储分层是最大成本优化手段**：

| 存储类 | 相对成本 | 场景 | 
|--------|---------|------|
| STANDARD | 1× | 热数据，频繁访问 |
| STANDARD_IA | ~0.5× | 低频访问但需快速恢复 |
| GLACIER | ~0.1× | 归档数据，可接受数小时恢复 |
| DEEP_ARCHIVE | ~0.025× | 合规归档，极少访问 |

**不考虑分层的原因通常是"我们只需要支持 local FS"**。但代码库中已经：
- 有 `StorageClass` 字段在 `Object` 结构体中
- 有四个 Storage 后端（local、S3、OSS、COS）
- 有 `replication` 包可以将对象从一个后端复制到另一个

这意味着分层转换的后端基础设施**已经存在**，缺失的是：
1. 生命规则中的 `Transition` 解析
2. 调度对象从后端 A 复制到后端 B 并更新元数据的 worker
3. 多 StorageClass 感知的存储抽象

#### 核心挑战与技术难点

| 挑战 | 难度 | 说明 |
|------|------|------|
| **后端间对象转移** | 高 | Transition 本质上是跨后端复制 + 删除 + 元数据更新。`storage.Storage` 接口无 `Copy` 方法，当前的 `replication` 包走的是"读→写"路径，对超大型对象（>5GB）效率低 |
| **StorageClass 与后端映射** | 中 | 需要定义：哪些 Storage Backend 支持哪些 StorageClass？local FS 通常只支持 STANDARD，OCI Object Storage 支持所有类，且定价模型不同 |
| **GLACIER 对象的访问模式** | 中 | GLACIER 中的对象必须先 `Restore`（异步复制回 STANDARD），这需要新增一个 Restore 状态跟踪表 |
| **成本精确性** | 低 | 自建对象存储时"存储类"的主要价值是元数据标记——实际成本在于后端选择而非虚拟类名 |

#### 架构变更建议

```
当前抽象:
  storage.Storage → local / s3 / oss / cos
                    (每个后端仅有一个隐式的"存储类")

建议扩展:
  storage.Storage (保持不变)
    │
    → storage.ClassAwareStorage (可选接口)
    │    │
    │    ├─ backend.StorageClass() → []string // 该后端支持的存储类
    │    └─ backend.DefaultClass() → string
    │
    → storage.TransitionPlanner (新增接口)
         │
         ├─ PlanTransition(obj *repository.Object, rules []LifecycleRule) → TransitionAction
         └─ ExecuteTransition(ctx, action) → error
```

**状态机设计：**

```
           ┌─────────────────────────────────────────────────────────┐
           │                  Lifecycle Rule Engine                   │
           │  (独立 worker，可配置间隔，类似 reconcile/lifecycle.go) │
           └──────────────┬──────────────────────────────────────────┘
                          │ 扫描 ListExpired → 匹配规则
                          ▼
    ┌────────────────────────────────────────────┐
    │            TransitionPlanner                │
    │  根据对象的 StorageClass + age + 规则列表   │
    │  决定下一步：STANDARD → IA → GLACIER → DEL  │
    └──────────────┬──────────────────────────────┘
                   │
                   ▼
        ┌─────────────────────┐
        │ TransitionExecutor  │
        │                     │
        │  1. 源后端 Get      │
        │  2. 目标后端 Put    │
        │  3. Checksum 校验   │
        │  4. 更新 Object     │
        │    (storage_key,    │
        │     storage_class)  │
        │  5. 源后端 Delete   │
        └─────────────────────┘
```

**对现有系统的影响：**

| 变更点 | 影响范围 | 程度 |
|--------|---------|------|
| `LifecycleRule` 结构体扩展 | `internal/repository/repository.go` | 中等——新增字段，不破坏现有 |
| 生命周期迁移文件解析 | `internal/api/s3compat/bucketconfig.go` | 中等——需完整解析 Transition XML |
| 新增 `TransitionExecutor` | `internal/reconcile/` | 新组件 |
| `Object.StorageClass` 现在有实际意义 | `internal/service/` | 低——已有字段 |
| `file_crud.go` 的 GET/PUT 路径需感知 StorageClass | 影响 GET 响应头（`x-amz-storage-class`） | 低 |

**兼容性策略：**
- 现有桶的生命周期规则（只有 `Expiration`）无需修改
- 现有对象（`StorageClass = ""`）被视为 `STANDARD`
- Transition 执行器默认不启用（通过配置开关或规则存在才激活）
- 非 STANDARD 对象的 GET 行为：GLACIER 对象直接 GET 返回 `InvalidObjectState`（符合 S3 行为）

---

### 方向 D：Object Lock 合规模型（优先级 P1）

**从"时间戳锁"到"符合 SEC 17a-4 的 WORM 存储"**

#### 为什么需要

当前 Object Lock 是"名义上的"——它有一个 `LockedUntil` 时间戳，但：

1. **Legal Hold 是伪保护**：以元数据 `_aero_legal_hold` 存储，可被覆盖式 PUT 清除
2. **无 GOVERNANCE/COMPLIANCE 模式区分**：审计师无法确认锁的可绕性
3. **无 bypass-governance-retention 识别**：`GOVERNANCE` 锁本应可被特权用户绕过，但当前此机制不存在

这意味着：一个声称"支持 Object Lock"的系统，实际上**不能用于任何受监管的合规场景**。

#### 核心挑战与技术难点

| 挑战 | 难度 | 说明 |
|------|------|------|
| **COMPLIANCE 模式的严格性** | 高 | COMPLIANCE 模式下，任何人都不能删除/覆盖——包括 root/admin、DBA（直接操作 DB）、Storage 后端操作者。这需要：API 层拒绝 + Repository 层拒绝 + Storage 层不执行删除 |
| **版本控制依赖** | 中 | S3 要求 Object Lock 桶必须启用版本控制。当前代码库检查此约束了吗？可能需要新增校验 |
| **Legal Hold 与 Retention 交互** | 中 | 两者独立生效：Legal Hold 开启 → 不可删除（即使 Retention 已到期）；Retention 有效 → 不可删除（即使无 Legal Hold） |
| **Bypass 权限模型** | 中 | 需要将 `s3:BypassGovernanceRetention` 映射到 Auth 系统的权限检查——桶策略需要支持此 Action |

#### 架构变更建议

```
当前数据模型:
  Object.LockedUntil   → *time.Time  (单一字段)
  Object.Metadata["_aero_legal_hold"]  → 元数据标记（可被覆盖）

建议数据模型:
  Object.LockedUntil   → *time.Time            (保留，但增加模式)
  Object.RetentionMode → RetentionMode          (新增: "GOVERNANCE" | "COMPLIANCE" | "")
  Object.LegalHold     → bool                   (新增独立列)

  BucketConfig.ObjectLockEnabled → bool         (新增桶级开关)
  BucketConfig.RetentionMode    → RetentionMode (新增桶默认模式)
  BucketConfig.RetentionDays    → int           (新增桶默认保留天数)
```

**锁定检查分层（防御纵深）：**

```
API 层 (internal/api/s3compat/handler.go + internal/api/rest/handler.go):
  - PUT / DELETE 前检查 Object Lock 状态
  - COMPLIANCE 锁 — 直接拒绝
  - GOVERNANCE 锁 + 无 bypass header — 直接拒绝
  - GOVERNANCE 锁 + bypass header + bypass 权限 — 允许

Service 层 (internal/service/file_crud.go):
  - checkLockBeforeOverwrite / hardDeleteObject
  - 作为 API 层的后盾（防止新增 API handler 遗漏检查）
  - 独立于协议

Repository 层 (internal/repository/):
  - 硬删除前二次锁检查（防止 SSH/console 直接调用 repo 绕过）
```

---

### 方向 E：策略引擎增强 + 跨协议统一授权（优先级 P2）

**从"仅 S3 × 仅 IP 条件"到"跨协议策略授权"**

#### 为什么需要

当前桶策略引擎有两个根本性局限：

1. **仅支持 `IpAddress`/`NotIpAddress` 条件**——无法满足企业级安全需求（VPC 隔离、HTTPS 强制、时间窗口、Referer 防盗链）
2. **仅在 S3 handler 层执行**——REST API、WebDAV、MCP 均不评估桶策略

这是一个**明显的安全后门**：即使 S3 请求受策略控制，同等的 REST API 请求不受策略控制。

#### 核心挑战与技术难点

| 挑战 | 难度 | 说明 |
|------|------|------|
| **条件上下文提取** | 中 | 每个请求需要在 middleware 层从 HTTP request 提取完整条件上下文（source IP, referer, TLS state, time, user agent, source VPC...）并注入到 context |
| **策略变量替换** | 中 | `${aws:username}` 等变量需要在授权时动态替换，当前已支持 tenant 提取——可复用 |
| **跨协议一致性** | 高 | 需要在所有协议入口（S3、REST、WebDAV、MCP）执行统一的授权检查点。目前没有这样一个"授权网关" |
| **Deny 优先级** | 低 | 已实现：任何 Deny 匹配即拒绝 |

#### 架构变更建议

```
当前授权流:
  S3 handler → checkBucketPolicy() → auth.Allowed()
  REST handler → 无策略检查
  WebDAV → 无策略检查
  MCP → 无策略检查

建议授权流（授权网关模式）:
  Middleware 层新增: PolicyCheckMiddleware
    │
    ├─ 从 context 提取 tenant、bucket、action
    ├─ 加载桶策略（缓存）
    ├─ 提取请求条件上下文（source IP、TLS、time、referer、user agent）
    ├─ 调用 auth.Allowed() 扩展版
    │
    ├─ Deny → 403 Forbidden
    └─ Allow → 进入 FileService

  所有协议共享此 middleware，确保一致授权。
```

**条件引擎扩展：**

```go
// 新增：条件评估上下文（从请求中提取）
type ConditionContext struct {
    SourceIp        string
    Referer         string
    SecureTransport bool        // r.TLS != nil
    CurrentTime     time.Time
    UserAgent       string
    SourceVpc       string      // 需从 X-Forwarded-For / 元数据获取
    SourceVpce      string
}

// 新增：条件评估函数族
type ConditionFunc func(key string, values []string, ctx ConditionContext) bool

var builtinConditions = map[string]ConditionFunc{
    "IpAddress":           evaluateIPAddress,
    "NotIpAddress":        evaluateNotIPAddress,
    "StringEquals":        evaluateStringEquals,
    "StringNotEquals":     evaluateStringNotEquals,
    "StringLike":          evaluateStringLike,
    "Bool":                evaluateBool,
    "DateGreaterThan":     evaluateDateGT,
    "DateLessThan":        evaluateDateLT,
    "ArnEquals":           evaluateArnEquals,
    "Null":                evaluateNull,
}
```

---

## 3. 接口设计建议

### 3.1 核心抽象层评估

当前两个核心接口（`storage.Storage` 和 `repository.Repository`）设计良好：

| 方面 | 评价 | 建议 |
|------|------|------|
| `Storage` 接口简洁性 | ✅ 恰到好处的方法集 | 保持当前大小——不要因分层转换而添加 `Copy/ Move` 等高耦合方法 |
| `Storage` 后端可扩展性 | ✅ 新增后端只需实现 8 方法 | 考虑为 Transition 添加**可选的** `CopyFrom` 方法（避免大对象走读→写的路径） |
| `Repository` 接口大小 | ⚠️ 已偏大（~50 方法） | 考虑按领域拆分为子接口：`ObjectRepository`、`BucketConfigRepository`、`EventRepository` |
| `EventSink` 接口 | ✅ 恰当地抽取为 `service.EventSink` | 保持 |

### 3.2 需要新增/改进的抽象

#### Event Filter（事件过滤抽象）

方向 A 需要一种灵活的事件过滤机制。建议设计为**可组合的过滤链**而非复杂的 DSL：

```go
type EventFilter interface {
    Match(e repository.Event) bool
}

// 内置过滤器
type TypeFilter []string    // 只匹配指定类型
type PrefixFilter string    // 只匹配指定 key 前缀
type SuffixFilter string    // 只匹配指定 key 后缀
type TagFilter map[string]string // 只匹配指定标签
type AndFilter []EventFilter     // 所有子过滤器必须通过
type OrFilter  []EventFilter     // 任一子过滤器通过
```

**设计理由：** 避免引入事件过滤 DSL（如类似 AWS EventBridge 的 JSON 规则语言），保持轻量。S3 通知规则本身的表达能力有限（prefix/suffix + Tags），上述组合足以覆盖。

#### LogSink（日志写入抽象）

方向 B 需要将日志写入与具体存储后端解耦：

```go
type LogSink interface {
    Write(ctx context.Context, entries []AccessLogEntry) error
}

// 内置实现
type BucketLogSink  // 写入目标桶（通过 Storage.Storage）
type DBLogSink      // 写入 access_logs 表（用于快速审计查询）
type StdoutLogSink  // 写入 stdout（兼顾容器日志）
```

#### TransitionPlanner / TransitionExecutor（分层转换抽象）

方向 C 的核心抽象：

```go
type TransitionPlanner interface {
    // 给定一组生命周期规则和一个对象，返回下一个要执行的动作
    Plan(ctx context.Context, obj *repository.Object, rules []LifecycleRule) (*TransitionAction, error)
}

type TransitionAction struct {
    SourceClass string   // 当前 StorageClass
    TargetClass string   // 目标 StorageClass
    Action      string   // "transition" | "expire"
    DueAt       time.Time // 应该执行的时间
}

type TransitionExecutor interface {
    // 执行一个转换动作（跨后端复制）
    Execute(ctx context.Context, obj *repository.Object, action *TransitionAction) error
}
```

### 3.3 跨协议授权网关

方向 E 的核心问题是**授权评估位置**。建议引入统一的**授权中间件**而非在每个 handler 中调用：

```go
// 新增中间件：策略授权
func PolicyEvaluator(repo repository.Repository) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            // 1. 从 context 提取 tenant、bucket、action
            // 2. 加载桶策略（缓存）
            // 3. 构建条件上下文（IP、TLS、time...）
            // 4. Eval：Deny → 403
            // 5. Allow → next
        })
    }
}
```

**关键决策：** 授权中间件放在 Auth middleware 之后、handler 之前。这将其集成到现有的 middleware 链中（`RequestID → CORS → Auth → Tenant → [新增] PolicyAuthorization → RateLimit → ...`），无需修改任何 handler 代码。

### 3.4 向后兼容性设计原则

| 场景 | 原则 | 实例 |
|------|------|------|
| 新增接口方法 | 用可选接口（type assertion）而非修改现有接口 | `type ClassAwareStorage interface { StorageClass() []string }` |
| 数据模型扩展 | 新字段在 DB 中设默认值或 `COALESCE` | `retention_mode VARCHAR DEFAULT ''` |
| 配置新增 | 环境变量默认 opt-out（false / empty） | `EVENT_ROUTING_ENABLED=false` |
| 旧协议兼容 | S3 XML 响应保持标准格式 | 新增字段不影响旧客户端解析 |

---

## 4. 技术选型

### 4.1 当前技术栈适用性评估

| 当前栈 | 适用性 | 评价 |
|--------|--------|------|
| **Go 1.25 + Stdlib HTTP** | ✅ 完全适合 | 并发模型适合 IO-bound 的服务、编译型性能好、部署简单 |
| **modernc.org/sqlite (纯 Go)** | ✅ 适合单机/轻量场景 | 无 CGO 依赖、零配置 |
| **pgx/v5 (Postgres)** | ✅ 适合集群场景 | 成熟、高性能 |
| **chi router** | ⚠️ 可满足当前需求 | 功能足够但非必要依赖 |
| **OpenTelemetry** | ✅ 行业标准 | 监控可观测性 |

### 4.2 是否需要引入新技术栈

| 方向 | 是否需要新技术 | 建议选型 | 理由 |
|------|--------------|---------|------|
| A: 事件路由 | **否**——现有基础设施足够 | 不引入新依赖 | `events` 包和 `jobs` 包可复用；规则匹配逻辑用标准库足以 |
| B: 访问日志 | **否**——利用现有存储管线 | 不引入新依赖 | 日志写入目标桶通过现有 `storage.Storage` 接口；批量缓冲区用标准 `sync.Pool` |
| C: 分层转换 | **可能需要**——大对象复制 | 推荐：引入 `io.CopyN` + `storage.CopyFrom` 可选接口 | 若不做零拷贝复制优化，当前 reader/writer 模式即可；极端情况（10GB+）考虑分片并行复制 |
| D: Object Lock | **否**——纯业务逻辑 | 不引入新依赖 | 所有改动是数据模型 + 验证逻辑 |
| E: 策略引擎 | **否**——纯逻辑扩展 | 不引入新依赖 | 条件评估函数无外部依赖；策略解析已有 |

> **结论：五个方向均无需引入新的第三方框架或存储系统。** 所有扩展都可以在当前技术栈内完成。

### 4.3 自建 vs 依赖选择的决策框架

对于事件路由和访问日志等方向，可能会想到"是否使用消息队列替代自建路由"。以下是决策框架：

| 场景 | 自建路由（当前方案） | 引入消息队列（RabbitMQ/Kafka） |
|------|----------------------|-------------------------------|
| **系统复杂度** | 低——在现有 Bus 上叠加路由层 | 高——引入新中间件、运维复杂度增加 |
| **部署复杂度** | 不变——单二进制部署 | 新组件：MQ 集群部署、网络配置 |
| **持久化/重试** | 复用现有 `jobs` 表 + `webhook_failures` 表 | MQ 自带死信队列和重试 |
| **吞吐量上限** | 当前 `EVENTS_SUB_BUFFER=64` 够用 | Kafka 支撑百万级/秒 |
| **跨实例分发** | 当前已有 `WithTransport`（Postgres LISTEN/NOTIFY） | Kafka 分区原生支持 |
| **选择** | **推荐：当前自建** | 仅当需要跨数据中心事件分发时考虑 |

---

## 5. 实施路线图

### 5.1 优先级排序

```
                   高影响
                    │
                    │   方向A (事件路由)     方向B (访问日志)
                    │      P0                    P0
                    │   用户可见断线          合规硬阻断
                    │   快速修复：S3 SDK     快速验证：日志配置后
                    │   调用静默失败          目标桶无日志产生
                    │
                    │
                    │   方向D (Object Lock)   方向C (分层转换)
                    │      P1                    P1
                    │   治理缺口              成本优化
                    │   中等复杂度            高复杂度
                    │   依赖 schema 变更      依赖 worker 架构
                    │
                    │   方向E (策略引擎)
                    │      P2
                    │   安全后门
                    │   但需跨协议集成
                    │
                    └───────────────────────────────────→ 高复杂度
```

### 5.2 阶段划分

#### 阶段一：断线补齐（P0 · 预估 2-3 周）

| 里程碑 | 方向 | 交付物 | 检查条件 |
|--------|------|--------|---------|
| M1.1 | A | `EventRouter` 组件 + 通知规则热加载缓存 | S3 SDK `put_bucket_notification` → `Filter` 生效 |
| M1.2 | A | Webhook 分发器和 SSE 过滤 | 两个桶不同规则 → 事件只转发给匹配的目标 |
| M1.3 | B | `AccessLogBuffer` 批量日志写入 | 配置日志目标桶 → 访问对象 → 目标桶出现日志对象 |
| M1.4 | B | 递归检测 + `make check` | 写目标桶不触发日志写入（检测 + 跳过） |
| M1.5 | A+B | 集成测试 | `make test` 包含通知路由和日志写入测试 |

**风险点：**
- 事件路由的缓存一致性（规则变更后是否需要等待 TTL？→ 初始方案 5 分钟 TTL + 启动时强制加载）
- 日志写入对请求延迟的影响（批量缓冲缓解；第一个 flush 可见时延 ~5s）

#### 阶段二：治理补齐（P1 · 预估 4-6 周）

| 里程碑 | 方向 | 交付物 | 检查条件 |
|--------|------|--------|---------|
| M2.1 | D | Schema 迁移（`retention_mode`, `legal_hold` 列） | 升级不丢失数据；降级脚本 |
| M2.2 | D | `PUT ?retention` / `PUT ?legal-hold` 端点 | S3 SDK `put_object_retention` 成功 |
| M2.3 | D | COMPLIANCE 锁不可绕过测试 | hard delete 返回 403 |
| M2.4 | C | `Transition` 规则解析 + 工人框架 | 生命周期 XML 中的 Transition 被正确解析 |
| M2.5 | C | `TransitionExecutor` 跨后端复制 | 对象从 STANDARD → GLACIER（后端迁移） |

**风险点：**
- **数据迁移安全（方向 D）**：Schema 变更不允许导致已有数据丢失。迁移脚本需 100% 向下兼容（新列可为 NULL）
- **COMPLIANCE 锁的严格性（方向 D）**：需要同时在 API 层、Service 层、Repository 层执行检查。单一层遗漏等于安全漏洞
- **转换期间的对象访问（方向 C）**：过渡窗口中（源后端已删除但目标后端未完成），GET 请求应降级等待或返回 `InProgress`

#### 阶段三：安全补齐（P2 · 预估 2-3 周）

| 里程碑 | 方向 | 交付物 | 检查条件 |
|--------|------|--------|---------|
| M3.1 | E | 条件引擎扩展（StringEquals, Bool, Date, Arn 条件） | 策略包含 `aws:SecureTransport` → HTTP 请求被拒绝 |
| M3.2 | E | 授权中间件集成到 middleware 链 | S3 和 REST 对同一桶策略产生一致授权结果 |
| M3.3 | E | ConditionContext 完整提取 | 所有条件键（Referer, UserAgent, SourceVpc 等）在请求中可访问 |

**风险点：**
- 授权中间件对所有请求的性能影响（策略评估涉及 JSON 解析 + 条件匹配，建议缓存策略解析结果）
- REST API 现有 handler 中可能有"不需要鉴权的路径"（如 `/healthz`），需要排除列表

### 5.3 长期架构演进路径

```
Phase 1 ─→ Phase 2 ─→ Phase 3 ─→ Future
(断线补齐)  (治理补齐)  (安全补齐)  (高级)

  方向A         方向D         方向E      多集群事件路由
  方向B         方向C                   跨区域复制策略
                                       日志数据分析仪表盘
                                       WORM 审计证书报告
                                       存储类成本分析
```

**未来值得考虑的独立方向（不在当前 5 方向内）：**

1. **多集群事件网格**——当系统部署为多集群时，事件路由不仅限于单实例
2. **S3 Object Lambda**——在 GET 请求返回对象前执行自定义转换（图像缩放、数据脱敏）
3. **批量作业编排**——Lifecycle + Replication + Notification 的组合可以构建"数据管线"（Data Pipeline）

### 5.4 风险登记表

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| 方向 A 事件路由性能瓶颈（高 TPS 场景） | 低-中 | 中 | 规则缓存 + 无规则退化为广播；压力测试做基准 |
| 方向 B 日志写入耗尽磁盘 IO（高频桶） | 中 | 高 | 批量缓冲区 + 可丢弃模式 + metrics 监控 `log_write_dropped_total` |
| 方向 C 跨后端对象转换失败导致数据丢失 | 低 | 严重 | 先复制后删除（两步提交）；转换完成后才删除源 blob；失败重试 + 手动恢复 |
| 方向 D COMPLIANCE 锁被绕过 | 极低 | 灾难 | 三层防御（API → Service → Repo）；审计日志记录锁定绕过尝试 |
| 方向 E 策略条件引擎引入性能退化 | 中 | 中 | 策略评估可超时中断；缓存策略 AST |

---

## 总结

五个方向反映出同一个结构性问题的不同侧面：**当前系统在"功能面积"（Feature Surface）上扩展很快（API 层、持久化层均已覆盖），但是执行管线（Runtime Pathway）没有同步完成。** 结果是代码库中存在大量"看起来"完整的路径，但实际上并不产生用户价值。

建议的实施策略是"**先修补断线，再补齐模型，最后统一安全**"（P0 → P1 → P2），五个方向均无需引入新技术栈，所有扩展都可以在当前 Go + SQLite/Postgres + 分层架构的框架内完成。
