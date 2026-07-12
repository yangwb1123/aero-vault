# AeroVault — 架构分析报告

> **分析范围：** 全代码库（`internal/*` 23+ 子包、三套 SDK、MCP、Web UI、48 对迁移文件）
> **分析视角：** 资深架构师
> **分析日期：** 2026-07-12
> **输入文档：** `docs/requirements/expansion-v80-systemic-production-gaps.md`

---

## 1. 架构评估

### 1.1 优势

当前架构的核心理念（**thin protocol adapters → fat service → pluggable storage/repo**）是一个经过充分验证的六边形架构（Ports & Adapters）变体，在 Go 生态中属于成熟模式。其优势突出：

| 优势 | 具体体现 | 架构价值 |
|------|---------|---------|
| **单二进制部署** | 全部功能（REST/S3/WebDAV/MCP + AI + workers + admin）打包为一个 binary | 零外部依赖部署，适配容器化/K8s/边缘 |
| **协议无关服务层** | REST/S3/WebDAV/MCP 共享 `FileService`，业务规则集中在 700 行 | 任意协议的 bug 修复同时惠及全部入口 |
| **Storage 抽象合理** | 7 方法接口（Put/Get/Stat/Delete/List/PresignGet/PresignPut）+ multipart 扩展 | 本地 FS/S3/OSS/COS 可互换，可同时用于主存储和复制目标 |
| **Repository 分离干净** | 元数据与 blob 分离，SQLite/Postgres 双实现共享 SQL core | 开发用 SQLite 零配置，生产用 Postgres 可扩展 |
| **事件持久化先行** | `Publish` 先写 DB 再广播，`object_events` 表始终有一份持久化副本 | 即使广播丢失，数据未永久丢失（恢复可能） |
| **多租户内置** | `X-Aero-Tenant` header + `storageKey(tenant, bucket, key)` 前缀 + 行级 scope | 代码中不必额外感知租户 |
| **Opt-in 安全默认** | AI/Event/Cluster/WebDAV 全部功能开关控制，默认关闭 | 最小攻击面 |

### 1.2 局限性

| 局限性 | 影响面 | 根本原因 |
|--------|-------|---------|
| **EventBus 广播不可靠** | Replication → 跨区数据丢失；Antivirus → 扫描跳过；Webhook → 合规通知丢失 | `select { case ch <- e: default: }` 设计哲学"宁可丢不可慢"对关键消费者不适用 |
| **StorageClass 是死数据** | 存储成本无法自动优化，管理员手动迁移或任凭成本增长 | `StorageClass` 字段持久化在元数据层，但存储层从不读取它驱动行为变化 |
| **Access Log 幻象 API** | S3 `?logging` API 返回 200 OK，但零日志产生，形成审计幻觉 | 管道的配置端（解析 + 持久化）已完整实现，但写入端（middleware → logger worker → 目标桶）从未构建 |
| **SDK 功能碎片化** | 对象级高级功能（Lock/Restore/Batch/Folder/CORS/Notification）三套 SDK 全部缺失 | API 演进时没有强制 SDK 同步更新的机制 |
| **Content Hash 基础设施闲置** | 重复内容写入 N 个独立 blob，存储成本线性增长 | `ETag`（MD5 hex）和 `Content-MD5` 校验已就位，但不驱动去重决策 |
| **启动装配线性** | `main.go` 单文件 700+ 行，wire 顺序固定，难以单元测试启动逻辑 | 缺少启动装配抽象（如 `fx` / `wire` 或自行封装 `ServerBuilder`） |
| **集群能力基础薄弱** | 仅有 lease-based singleton 用于 Reconcile 互斥；无服务发现、无 gRPC、无分布式协调 | 当前设计为单实例优化，高可用需要追加基础设施依赖 |

### 1.3 关键设计决策评估

| 决策 | 选择 | 评价 |
|------|------|------|
| 事件持久化+广播双写 | 先写 DB，再非阻塞广播 | **合理。** 但广播侧应该区分可靠/尽力投递两种模式 |
| SIGTERM 优雅关闭 | 通过 `signal.Notify` 触发生命周期钩子 | **合理。** 需验证 workers 有足够 drain 时间（当前 webhook/antivirus 无 drain 逻辑） |
| 存储 key 前缀 = `tenant/bucket/key` | `path.Join(tenant, bucket, key)` | **合理。** 但限制了租户间 blob 共享（去重场景需要额外的 content-hash → storage_key 映射） |
| 跨实例事件传输 | Postgres LISTEN/NOTIFY（WithTransport） | **合理。** 但 NOTIFY 有 8K 载荷限制且不持久——大 payload 会静默截断 |
| SDK 手工维护 | Go/Python/JS 各一套独立实现 | **当前可接受。** 长期需要 OpenAPI → codegen 或至少声明式 API 规范 |
| WebDAV 独立于 chi | 在 chi 外单独分发 | **正确决定。** WebDAV 需要 PROPFIND/MKCOL 等非标准 HTTP 方法，chi 不支持 |

### 1.4 架构债务汇总

| 债务项 | 严重程度 | 修复成本估测 | 触发条件 |
|--------|---------|------------|---------|
| EventBus 静默丢弃 | **Critical** | ~300 行改 `bus.go` + ~200 行死信表 + API | 任意高吞吐写入场景 |
| Access Log 管道缺失 | High | ~400 行 middleware + async logger | 合规审计要求 |
| Lifecycle 无 transition | High | ~500 行 transition action handler | 存储成本账单 |
| SDK 功能缺口（对象级） | Medium | ~200 行 × 3 SDK | 用户需要 Lock/Batch/Folder |
| Content Dedup 缺失 | Medium | ~400 行 content_hashes 表 + 读写路径 | 高重复内容工作负载 |
| main.go 启动线性化 | Low | 封装 `ServerBuilder` 或 `fx` | 启动逻辑复杂度增长 |

---

## 2. 扩展方向

### 方向 A：事件驱动架构成熟化 — 从尽力投递到至少一次 + 背压 + 可观测

**这是文档方向三的升华版本。** 当前 EventBus 的核心问题不是"少了一个 `SubscribeCritical`"，而是**架构层面缺少事件可靠性分级的统一模型**。

#### 为什么需要

- 当前所有订阅者使用同一个 `Subscribe()` API，返回同一个 `chan repository.Event`，具有相同的 64 缓冲区
- `broadcast` 方法中 `select/default` 对所有订阅者一视同仁地丢弃
- 订阅者无法向 EventBus 表达"我的消息比另一个订阅者更重要"
- 这违反了康威定律的反面：两个完全不同可靠性要求的使用场景（Replication vs. SSE 实时推送）被塞进同一个抽象

#### 核心挑战

| 挑战 | 描述 |
|------|------|
| **可靠性分级模型** | 如何设计 `SubscribeCritical` / `SubscribeBestEffort` 使得调用者明确表达可靠性需求 |
| **背压传导** | 关键订阅者积压时，背压应传导到 `Publish` 调用者，还是只阻塞该订阅者的 channel？传导到业务层怎么表达？（返回 `ErrBusOverloaded`？返回 `503`？） |
| **死信 ≠ 恢复** | 死信表保障了"不丢记录"但保障不了"不丢投递"。回放 API 需要与业务协作者 state 对齐（比如 Replication 已经收到了后来的事件，回放旧事件会乱序） |
| **跨实例一致性** | WithTransport 模式下，一个实例丢弃了事件，另一个实例可能已送达。死信在共享 DB 中需要实例标记 |

#### 预期的架构变更

```
当前：
EventBus.Publish → InsertEvent → broadcast() → {ch <- e: default: dropped++}

改造后：
EventBus.Publish → InsertEvent → broadcast(
    for each subscriber:
        if subscriber.reliable:
            ch <- e  // 阻塞式，背压传导
        else:
            select {case ch <- e: default: → write_dead_letter(e, subscriber)}
)

SubscribeCritical(name string, ch chan<- Event) → 注册可靠订阅者
SubscribeBestEffort(name string, ch chan<- Event, dlq bool) → 注册尽力投递（可选死信）
```

#### 对现有系统的影响

- **向下兼容：** 现有 `Subscribe()` 语义变为 `SubscribeBestEffort(ch, dlq=false)`，所有现有消费者零改动
- **`Publish` 签名不变**，调用者不需感知
- **背压传导新增**：`Publish` 在关键订阅者全部阻塞时可选择阻塞（用 context 超时控制）
- SSE endpoint（`/v1/events`）是典型的尽力投递消费者，不必改造

---

### 方向 B：存储层多维化 — `StorageClass` 从元数据到行为驱动的跃迁

**这是文档方向一的架构级重构。** 核心洞见：当前 `StorageClass` 在 `Object` 结构体中是字符串字段，存储后端接口 `Storage` 对存储类一无所知——这意味着任何跨类操作（transition）必须在 service 层自行实现 copy-to-tier/rename 逻辑，不同后端各自实现。

#### 为什么需要

当前 `Storage.Put(key, r, size, opts)` 无 `StorageClass` 参数。这意味着：

1. 即使 `FileService` 想写 `STANDARD_IA` 对象到 S3，它无法通过接口表达
2. 不同存储后端处理存储类的方式截然不同：
   - S3：CopyObject 加 `x-amz-storage-class` header
   - Local FS：不同 tier 子目录 + 不同 replication factor（如果部署了分布式 FS）
   - OSS/COS：类似 S3 但 API 不同
3. 当前 `LifecycleJob.handleExpiredObject` 只能删除，不能 transition，因为它没有从 `storage.Storage` 获取"把对象从 tier A 移到 tier B"的能力

#### 核心挑战

| 挑战 | 描述 |
|------|------|
| **Storage 接口扩展** | 如何设计 `Move(key, targetClass) error` 或 `Copy(key, targetKey, targetClass)` — 需要同时保障操作的原子性或幂等性 |
| **Tier 语义不一致** | Local FS 没有 S3 的 STANDARD_IA/GLACIER 概念——怎么映射？Local 可以定义 `./var/objects/{hot,warm,cold}/` 三个子目录，但"cold"没有类似 GLACIER 的恢复延迟语义 |
| **存储 key 变更** | Transition 可能改变存储 key（如 S3 跨 bucket transition）。`objects.storage_key` 需要更新，并且旧 key 在确认新 key 可读后删除 |
| **Transition 与 Lock 冲突** | Object Lock 启用的对象不能 transition 到不支持 lock 的 tier（如 GLACIER） |
| **成本优化策略** | 谁来决定 transition 的时机？策略是 bucket 级别的 Lifecycle 规则，还是系统级别的智能分层（如 S3 Intelligent-Tiering） |

#### 预期架构变更

```
// 新的 Storage 扩展接口
type TieredStorage interface {
    Storage
    Move(ctx, key, targetClass string) error
    SupportedStorageClasses() []string
}

// 或作为 Storage 接口的 method，所有后端实现（local 返回 nil 或降级）
interface Storage {
    // 现有方法...
    
    // 新方法：可选实现，返回 ErrNotSupported 表示不支持
    MoveToTier(ctx, fromKey, toKey, targetClass string) error
}
```

#### 对现有系统的影响

- **Storage 接口新增 method** — 所有后端实现需编译通过（`local` 返回 `storage.ErrNotSupported`）
- **`LifecycleJob` 新增 transition action handler** — 与现有 expire handler 并列
- **`objects` 表需要 `transition_after_days` 字段**（或复用 `expire_after_days` + action 枚举扩展）
- **S3 XML 解析需要支持 `Transition` 元素** — 当前 `xml.go:203-231` 定义了结构体但 handler 不解析
- **迁移文件新增**：`0025_object_transition.up.sql`

---

### 方向 C：API / SDK 治理体系 — 协议规范、自动生成、向前兼容

**文档方向四的架构级重述。** 核心问题不是"补哪几个方法"，而是**缺少 API 规范驱动的多方契约**。

#### 为什么需要

| 现状问题 | 影响 |
|---------|------|
| Go SDK 手工维护 43 个方法，Python/JS 各一套独立实现 | 三份代码各自维护，新增 API 端点需要 PR 三个文件 |
| 对象级高级功能（Lock/Restore/Batch/Folder/CORS/Notification）三套 SDK 全部缺失 | 文档声称"Python/JS 管理面断裂"但实际 admin 方法已齐全；真正缺失的是这些高级对象功能 |
| REST API 无全局版本化策略 | `/v1/files` 路径固定，新增字段或废弃端点无迁移路径 |
| SDK 无 HTTP 请求/响应拦截器 | 无法统一添加 tracing header、logging、retry 逻辑 |
| 错误模型不统一 | REST 返回 JSON error envelope，S3 返回 XML error，SDK 各自包装为不同异常类型 |

#### 核心挑战

| 挑战 | 描述 |
|------|------|
| **OpenAPI 全面性** | 当前 `/openapi.json` 从代码生成，但覆盖率可能不完整。需要确保每一端点 + 请求/响应 schema + 错误码都被捕获 |
| **Codegen 可行性** | OpenAPI → client codegen（`openapi-generator` 或 `oapi-codegen`）可生成桩代码，但设计良好的 Go 客户端（context-first, typed errors）需要模板定制 |
| **向后兼容策略** | API 演化采用 additive-only 模式（不破坏现有字段），必要时用 `x-deprecated` header + sunset header |
| **SDK 质量保障** | codegen 生成的桩代码需要手动封装（重试、分页、流式、错误处理）。哪些层自动生成、哪些手工打磨需要明确划分 |

#### 预期架构变更

```
1.  OpenAPI spec → source of truth
    ├── REST API 全部端点（含 admin、AI、batch）
    ├── 请求/响应 body JSON Schema
    ├── 错误模型统一（code + message + request_id）
    └── 每次 API 变更先更新 spec → 再实现 handler → 再更新 SDK

2.  三套 SDK 共享 API 契约
    ├── Go：oapi-codegen 生成 transport 层，手工封装业务便利方法
    ├── Python：openapi-generator 生成 Transport + 数据类，手工封装流式/retry
    └── JS：openapi-generator 生成 fetch-based transport，手工封装迭代器

3.  新增 API checklist
    每个 REST handler 的 PR 必须包含：
    - openapi.json 更新
    - Go SDK 对应方法
    - Python SDK 对应方法
    - JS SDK 对应方法
```

#### 对现有系统的影响

- **短期零影响** — 手工 SDK 继续运行
- **长期** — 从手工维护过渡到半自动生成，需要预留 2-3 个 sprint 的平稳过渡期
- **测试策略** — 各 SDK 应运行同一个集成测试 suite（可共用 Go 的 contract test 思路）

---

### 方向 D：多活集群架构准备 — 从单实例走向高可用

**当前架构假设单实例运行。** 除了 Reconcile 的 lease-based singleton 和 EventBus 的 WithTransport（Postgres LISTEN/NOTIFY）外，没有任何分布式协作机制。

#### 为什么需要

| 场景 | 当前 | 集群后 |
|------|------|-------|
| 实例崩溃 | 服务中断，直到重启 | 负载均衡器将请求转发到健康实例 |
| 滚动升级 | 人工确认流量排空 | Replica A 升级期间 Replica B 接管 |
| 存储高可用 | 依赖后端自身（S3 本身 HA） | 元数据层 Postgres HA 独立 |
| 横向扩展读取 | 单实例瓶颈 | 读取副本分担 GET/List/Search |
| 零停机部署 | 不可行 | 蓝绿部署 |

#### 核心挑战

| 挑战 | 描述 |
|------|------|
| **服务发现** | 实例如何发现彼此？需要 DNS-based（headless service）还是 registry-based（Consul/etcd）？ |
| **分布式事件总线** | Postgres LISTEN/NOTIFY 上限 8K 载荷 + 不持久。生产级集群需要替代方案（NATS / Redis PubSub / pg_notify + 轮询回退） |
| **write-once-read-all** | JobPool 需要在所有实例间协调——每个 job 只被消费一次（当前 singleton lease + local worker 模式） |
| **数据一致性** | `Publish` 先写 DB 再广播。实例 A 写 DB 成功但广播失败，实例 B 应能从 DB 轮询到未送达的事件 |
| **优雅排空** | SIGTERM 时需等待进行中的 PUT/GET 完成、JobPool worker 完成当前任务、EventBus subscriber 排空 |

#### 预期架构变更

```
1.  EventBus 多实例层
    └── LocalBus（当前模式）→ HybridBus（local broadcast + DB polling fallback）

2.  JobPool 分布式锁
    └── Job Registry + Postgres advisory lock（类似 Reconcile singleton 模式）

3.  Health API 增强
    └── /readyz 检查：DB 连接、Storage 可访问、各 worker 健康

4.  优雅关闭契约
    └── http.Server.Shutdown → JobPool.Drain → EventBus.Close → storage.Close
```

#### 对现有系统的影响

- **低影响起点** — EventBus 的 WithTransport + Reconcile 的 ClusterSingleton 已为集群模式预留接口
- 主要改造在 `main.go` 启动流程和优雅关闭流程
- `JOBS_WORKERS` 需支持跨实例协调（用 `jobs` 表的 `status` 列 + `claimed_by` + `claimed_at` + 超时重分配）

---

### 方向 E：可观测性成熟度 — 从存在到可用

当前 telemetry 层满足"有 OpenTelemetry 和 Prometheus 指标"，但离"运维人员能快速定位问题"还有差距。

#### 为什么需要

| 维度 | 现状 | 目标 |
|------|------|------|
| **结构化日志** | 仅有 `AccessLog` 每请求一行 + `slog` 散布各处，日志行无统一 schema | 统一 JSON 日志 schema，每个日志行含 `request_id`, `tenant`, `trace_id`, `span_id`, `duration_ms` |
| **分布式追踪** | OTel installed 但无手动 instrumentation（SPE/latency 路径无 span） | 各关键路径（storage.Put → event.Publish → job.Run）有完整 span hierarchy |
| **SLO 指标** | 仅有 raw counters/histograms | `{storage,search,chat}_latency_p99` + `error_budget` burn rate |
| **告警规则** | Prometheus 告警规则存在但无 runbook | 每个告警附带 runbook 链接、缓解步骤、playbook |
| **健康/就绪检查** | `/healthz` 返回 200，`/readyz` 返回 200 | 区分 liveness（进程存活）和 readiness（DB 可连接、Storage 可访问、队列深度正常） |

#### 核心挑战

| 挑战 | 描述 |
|------|------|
| **手动 instrumentation** | Go 的 OTel API 使用 `otel.Tracer("...").Start(ctx, "spanName")`，需要在 15+ 关键路径插入 span。工作量大但机械 |
| **日志采样** | 高吞吐时（如 CI 流水线批量上传），全量日志成本高。需实现 head-based 采样（错误请求全采样，成功请求按 1:100 采样） |
| **SLO 定义** | AI Search latency、Storage Get latency、EventBus 投递延迟——哪些是用户可见的关键指标？ |
| **日志持久化** | AccessLog 写 stderr，由容器运行时收集。但 bucket-level access log 需要持久化到目标桶（方向二的基础） |

#### 预期架构变更

```
1.  统一日志 schema
    {
        "timestamp": "RFC3339Nano",
        "level": "info|warn|error",
        "message": "...",
        "request_id": "...",
        "trace_id": "...",
        "tenant": "...",
        "duration_ms": 123,
        "error": "..."  // 仅 error level
    }

2.  Span 覆盖关键路径
    └── FileService.Put → storage.Put → repo.UpsertObject → events.Publish
    └── FileService.Get → storage.Get → repo.GetObject
    └── Search.Query → embedder.Embed → repo.SearchVector → reranker.Rerank

3.  /readyz 多维健康检查
    ├── repo.Ping() → DB 可连接
    ├── store.Stat(system-key) → Storage 可访问
    ├── bus.Health() → 订阅者积压深度
    └── pool.Health() → Job worker 数
```

#### 对现有系统的影响

- **纯新增** — 不影响现有业务路径
- 新增 dashboard 和 runbook 但不修改任何现有代码
- `AccessLog` 中间件可扩展为同时写 stderr + 发送到 bucket logging pipeline

---

## 3. 接口设计建议

### 3.1 关键接口设计原则

| 原则 | 说明 | 案例 |
|------|------|------|
| **最小接口** | 每个 interface 只暴露调用方真正需要的方法 | `Storage` 接口 7 方法，multipart 单独成接口，而非全部合并到主接口 |
| **接口隔离（ISP）** | 调用方不应依赖它不使用的方法 | `TieredStorage` 可选接口，而非给 `Storage` 加 `MoveToTier` |
| **错误可区分** | 使用 sentinel error（`var ErrNotFound`）而非 magic string | `storage.ErrNotFound` / `repository.ErrNotFound` |
| **Context 优先** | 每个 I/O 方法第一个参数是 `context.Context` | 全部 service/repo/storage 方法 |
| **零值可用** | `nil` 字段不应导致 panic | `ai.MockLLM{}` / `HashEmbedder` — AI 关闭时无 nil check 散布 |
| **配置化，非硬编码** | 行为变更通过 config/开关控制 | `STORAGE_DEDUP_ENABLED` / `AI_INDEX_ENABLED` |

### 3.2 是否需要新抽象层

| 抽象 | 是否需要 | 理由 |
|------|---------|------|
| **ServerBuilder** | **建议引入** | 当前 `main.go` 700+ 行顺序 wire 逻辑，封装为 `ServerBuilder` 可单元测试、可替换组件、可生成多个 server 变体（如 embed-only 模式） |
| **EventBus 可靠性分级** | **建议引入** | 文档方向三的核心方案——`SubscriberCritical` / `SubscriberBestEffort` |
| **SDK Transport 层** | **中期引入** | OpenAPI codegen 自动生成 HTTP transport + 数据类，手工封装业务便利方法 |
| **Storage 类感知层** | **中期引入** | `Storage.ClassAwareStorage` 包装器，在 service 层与存储后端之间插入 class-aware 行为（重试、metrics、tier 映射） |
| **Backpressure 通道层** | **视方向 A 方案而定** | 如果背压传导到 HTTP handler，需要在 middleware 层与 service 层之间加入 rate-limiting / circuit-breaker 通道 |

### 3.3 向后兼容性策略

| 策略 | 适用场景 | 实施方式 |
|------|---------|---------|
| **Additive-Only API** | REST/S3 API 演化 | 添加新端点或新字段，不在同一版本内删除或重命名现有字段。版本号变化时才清理 deprecated 字段 |
| **Deprecation Header** | 废弃现有端点 | 响应头 `Sunset: Sat, 12 Jul 2027 00:00:00 GMT` + `Deprecation: true` |
| **零值默认语义** | 配置项新增 | 新配置项默认值 = 旧行为（如 `STORAGE_DEDUP_ENABLED=false`） |
| **事件 schema 演化** | EventBus event 类型扩展 | `Event.Data` 使用 `map[string]any`，新增字段时消费者忽略未知字段 |
| **SDK 版本兼容** | SDK 误用不匹配 server | SDK 声明 `MinServerVersion`，HTTP header `X-Aero-Version` 对比，不匹配时友好错误 |

---

## 4. 技术选型

### 4.1 当前技术栈评估

| 层级 | 当前技术 | 评价 |
|------|---------|------|
| 语言 | Go 1.25 | ✅ 适合 I/O 密集型文件服务，协程模型高效 |
| HTTP 路由 | chi v5 | ✅ 轻量标准库兼容，支持 middleware 链 |
| 数据库 | SQLite (modernc.org/sqlite) / Postgres (pgx) | ✅ SQLite 零配置开发，Postgres 生产级 |
| 存储 | local FS / S3 / OSS / COS | ✅ 抽象合理，覆盖主流云 |
| AI Embedding | hash / OpenAI-compatible HTTP | ✅ hash embedder 零依赖可用于 CI |
| LLM Chat | OpenAI-compatible HTTP | ✅ 标准 API |
| 事件 | In-memory pub/sub + Postgres LISTEN/NOTIFY | ⚠️ 见方向 A 分析 |
| 序列化 | encoding/json | ✅ 标准库，无外部依赖 |
| 指标 | OpenTelemetry SDK + Prometheus | ✅ 生态标准 |
| 异步作业 | 自建 jobs 表 + worker pool | ✅ 轻量够用，Postgres 行锁防重入 |

### 4.2 是否需要新技术

| 方向 | 建议技术 | 候选方案 | 决策 |
|------|---------|---------|------|
| **方向 A — 跨实例事件** | NATS (nats-io/nats.go) | Redis PubSub / RabbitMQ / Kafka | **NATS 更适合轻量级事件广播**（~1μs 延迟，无持久化负担——事件已持久化到 DB）。Redis PubSub 无持久化但有 eviction 问题。Kafka 太重（需要 ZK/KRaft） |
| **方向 C — SDK Codegen** | oapi-codegen (Go) / openapi-generator (Python/JS) | 手工维护 / 自建 codegen | **oapi-codegen** 深度集成 Go 生态（`net/http` handler generation），Python/JS 用 openapi-generator |
| **方向 D — 集群配置** | K8s headless service + DNS SRV | Consul / etcd / Kubernetes API | **K8s 环境下用 headless service** — 零额外运维。非 K8s 环境用 Consul |
| **方向 E — 日志聚合** | OpenTelemetry Collector + Loki/Elasticsearch | 直接写 S3 / stderr + Fluentd | **OpenTelemetry Collector** — 与现有 OTel 一致，可同时处理 traces + metrics + logs |

### 4.3 自建 vs 引入的判断框架

| 判断维度 | 自建 | 引入第三方 |
|---------|------|-----------|
| **核心差异化能力** | Storage 抽象、FileService 业务规则、多租户模型 | 绝对自建 |
| **通用基础设施** | EventBus 跨实例传输、Job 协调 | 引入 NATS (轻量) |
| **可替代/插拔能力** | CLI、Web UI | 自建—产品体验不可替代 |
| **运维复杂度考量** | `go.mod` 新增依赖必须论证（I6 规则） | 仅引入 zero-dependency 或 stdlib-compatible 库 |
| **版本兼容风险** | 每个新依赖可能引入 breaking change | 用 interface 包装 + integration test 隔离 |

### 4.4 具体技术建议

```
┌──────────────────────────────────────────────────────┐
│ 方向 A (EventBus 可靠)                                │
│   ├── SubscribeCritical → 在现有 Bus 上改造，无新依赖   │
│   ├── 死信表 → 复用现有 repository 模式，无新依赖        │
│   └── 跨实例广播 → 可选 NATS 封装在 WithTransport 后    │
│                                                       │
│ 方向 B (Storage Tier)                                  │
│   ├── Storage.MoveToTier → 接口扩展，无新依赖           │
│   └── S3 CopyObject with x-amz-storage-class → 已有 SDK │
│                                                       │
│ 方向 C (SDK 治理)                                      │
│   ├── oapi-codegen → go generate 集成，dev 依赖        │
│   ├── openapi-generator → CI pipeline，运行时无依赖     │
│   └── API 变更 checklist → 流程改进，无技术依赖         │
│                                                       │
│ 方向 D (集群 HA)                                       │
│   ├── 优雅关闭 → 标准库 http.Server.Shutdown           │
│   ├── K8s headless service → 配置，无代码依赖           │
│   └── 分布式 JobPool → Postgres advisory lock，已有依赖  │
│                                                       │
│ 方向 E (可观测性)                                      │
│   ├── OTel manual instrumentation → 已有 SDK，无新依赖  │
│   └── /readyz 多维检查 → 标准库，无新依赖               │
└──────────────────────────────────────────────────────┘
```

---

## 5. 实施路线图

### 5.1 优先级矩阵

| 方向 | 优先级 | 业务价值 | 技术风险 | 依赖 | 推荐分批 |
|------|--------|---------|---------|------|---------|
| **A (EventBus 可靠)** | **P1** | 数据完整性——丢失不可逆 | 中 — 改 `bus.go` 核心逻辑 | 无 | **Sprint 1** |
| **B (Storage Tier)** | **P1** | 存储成本降低 63-92% | 中 — 接口扩展 + S3 兼容 | 方向 A 不阻塞 | **Sprint 2-3** |
| **C (SDK 治理)** | **P2** | 开发者体验——长期 ROI | 低 — 纯新增 | 无 | **Sprint 1-持续** |
| **D (集群 HA)** | **P2** | 生产可靠性——滚动升级 | 高 — 分布式一致性 | 方向 A 的 EventBus 集群模式 | **Sprint 4-5** |
| **E (可观测性)** | **P2** | 运维效率——排障能力 | 低 — 纯新增 | 方向 B 的日志管道 | **Sprint 2-4** |

### 5.2 阶段划分

```
Sprint 1 (P0-P1, ~2 周)
├── 方向 A: SubscribeCritical + 死信表 + 死信回放 API
│   ├── bus.go: SubscribeCritical(name, ch) 阻塞式投递
│   ├── dead_letter_events 表：migrations/0026
│   ├── Admin API: GET /admin/events/dead-letter + POST .../replay/{id}
│   └── 指标: eventbus_dropped_total, eventbus_dead_letter_total
│
├── 方向 C: SDK 治理启动
│   ├── openapi.json 审计：所有端点覆盖率检查
│   ├── 三套 SDK 缺失的对象级功能补齐（Lock/Restore/Batch/Folder/CORS/Notification）
│   └── API 变更 checklist 文档化
│
└── 快速修复: AccessLog 写 stderr 增加 JSON 格式 + request_id + tenant

Sprint 2-3 (P1, ~4 周)
├── 方向 B: StorageClass transition 引擎
│   ├── Storage 接口扩展: MoveToTier (后端可选实现)
│   ├── s3: CopyObject with x-amz-storage-class
│   ├── local: 子目录 rename + meta 更新
│   ├── LifecycleJob: transition_to_standard_ia / transition_to_glacier action
│   ├── S3 XML: Transition 元素解析
│   ├── 迁移文件: 0025_object_transition
│   └── 边界情况处理表（Lock冲突、crash恢复、expire优先）
│
├── 方向 E: 可观测性
│   ├── 10+ 关键路径 OTel span 注入
│   ├── /readyz 多维健康检查
│   └── SLO 指标仪表面板追加
│
└── 方向二: AccessLog pipeline
    ├── AccessLog middleware → channel → async logger worker
    ├── logger worker: 批量 JSON 写入目标桶
    ├── 循环引用检测 + PII scrub
    └── 内置日志对象生命周期（30 天过期）

Sprint 4-5 (P2, ~3 周)
├── 方向 D: 集群 HA 准备
│   ├── ServerBuilder 重构 main.go
│   ├── 优雅关闭全路径覆盖
│   ├── JobPool 分布式协调（jobs.claimed_by + timeout）
│   └── 可选: NATS transport for EventBus
│
└── 方向五: Content Dedup
    ├── content_hashes 表 + 迁移文件
    ├── Put 路径: 计算 SHA256 → 查找 → ref_count++
    ├── Delete 路径: ref_count-- → ref_count==0 → blob 删除
    ├── STORAGE_DEDUP_ENABLED 配置开关
    └── 并发安全: INSERT ... ON CONFLICT DO NOTHING
```

### 5.3 风险点与缓解策略

| 风险 | 影响方向 | 概率 | 严重度 | 缓解策略 |
|------|---------|------|-------|---------|
| EventBus 背压传导阻塞业务路径 | A | 中 | 高 | 用 context timeout 兜底；阻塞超时后降级为丢弃+死信 |
| S3 CopyObject 跨 region 或跨 account 权限 | B | 中 | 中 | transition 失败日志 + 下次 sweep 重试；不阻塞 expire 路径 |
| SDK codegen 生成代码难以手工加工 | C | 高 | 低 | 保留手工封装层；codegen 仅生成 transport + DTO |
| 集群模式下事件乱序 | D | 中 | 高 | 事件消费者需幂等；Replication/Webhook 本身已有重试/去重机制 |
| Content Dedup 与 SSE 加密冲突 | 五 | 低 | 中 | 加密对象密文不同 → 自然不命中；文档明确说明"加密对象不去重" |
| Lifecycle transition 中 crash → 旧 key 已删新 key 未写 | B | 低 | 高 | 三步提交法：INSERT new_key → UPDATE storage_key → DELETE old_key；`reconcile` 定期检测孤儿 |

### 5.4 实施建议

> **方向四（SDK）的事实修正：** 验证发现文档声称"Python SDK 仅 4 个 admin 方法、JS SDK 0 个"与代码不符——**Python 和 JS SDK 均已实现全部 14 个 admin 方法**。真正缺失的是对象级高级功能（Lock/Restore/BatchDelete/BatchTag/Folder/BucketCORS/BucketNotification/BucketStats/ListBucketVersions），且**三套 SDK 全部缺失**，不限于 Python/JS。实施时应优先补齐这些对象级功能，而非 admin 方法。

**推荐启动顺序（基于风险/价值比）：**

```
1st → 方向 A SubscribeCritical + 死信表（~2d，修复最严重数据完整性漏洞）
2nd → 方向 C SDK 对象级功能补齐（可增量，不影响核心路径）
3rd → 方向 B Lifecycle Transition 引擎（存储成本 ROI 最高）
4th → 方向 E 可观测性 + 方向二 AccessLog（运维成熟度）
5th → 方向五 Content Dedup（功能开关关闭，低风险）
6th → 方向 D 集群 HA（依赖前面方向的 EventBus/JobPool 改造）
```

---

## 补充：验证发现

在分析过程中，我对输入文档进行了独立代码核查。以下是与文档声明的关键偏差：

| 文档声明 | 核查结果 | 影响 |
|---------|---------|------|
| **方向四：Python SDK 仅 4 个 admin 方法** | ❌ Python SDK 有 **全部 14 个** admin 方法（`add_key` ~ `set_budget` 完整） | 方向四的优先驱动力需要重新定义——真实缺口是**对象级高级功能**（Lock/Restore/Batch/Folder/CORS/Notification），且三套 SDK 全部缺失 |
| **方向四：JS SDK 0 个 admin 方法** | ❌ JS SDK 也有 **全部 14 个** admin 方法（`addKey` ~ `setBudget` 完整） | 同上 |
| **方向三：去重声明需微调** | ✅ 建议调整措辞但本质判断正确 | 已在上述分析中采纳 |
| **方向一/二/五** | ✅ 全部精确 | 无偏差 |

此偏差不影响五个方向的总体架构分析的有效性，但在实施优先级上应重新评估——方向四的修复成本和影响面比文档描述的小。
