# 架构分析报告：Aero-Vault 代码锚点验证审阅

## 1. 架构评估

### 1.1 当前架构的优势

从验证报告和 AGENTS.md 可以看出，aero-vault 的核心架构设计具有以下显著优势：

| 优势 | 具体表现 |
|------|---------|
| **刚性契约层** | `FileService` 作为唯一业务控制器，协议适配器（REST/S3/WebDAV/MCP）均为薄层——这是正确的分层决策，防止了业务逻辑泄漏到协议层 |
| **Opt-in 安全默认** | AI/Vector/Webhook/Replication 全部 flag-gated，`nil` 安全的设计让基线路径零心智负担 |
| **DAG 装配清晰** | `config→storage→repo→service→workers→middleware→router` 的启动顺序可追踪、可替换、可 mock |
| **存储 key 不变性** | `storageKey(tenant, bucket, key)` 统一生成，禁止反解析——杜绝了数据覆盖漏洞的根源 |
| **Instrumentation 全覆盖** | 15 OTel instruments + Grafana panels，可观测性优先 |

### 1.2 核心局限性（架构债务）

验证报告暴露了 **五个架构层的系统性缺失**，我将其归类为三类技术债：

#### (A) 资源管控缺失（方向一、三）—— **最危险**

- **版本无限增长**：`BucketConfig.Versioning bool` 是二值开关，缺少 `MaxVersions`/`NoncurrentDays` 等保留策略。这意味着 production 环境中一个活跃 bucket 的版本表会无限膨胀，直到：
  - SQLite 行锁争用（DELETE + INSERT 高频场景）
  - Postgres 表膨胀导致 autovacuum 跟不上
  - 存储成本线性增长
- **配额非原子**：`preflightQuota` 与 `AddTenantUsage` 之间存在 TOCTOU 窗口。虽然 SQLite 单写者模式下风险较低，但 Postgres 多 pod 部署时这是确定性竞态条件。
- **存储层无容量约束**：Storage 接口没有 `Capability()` 或 `DiskFree()` 方法，`LocalStorage.Put` 直接 `os.Create`，当磁盘满时表现为不可预测的 I/O 错误而非优雅的 `ErrQuotaExceeded`。

**根本原因**：架构设计时假设了 "unlimited resources" 的理想环境，没有嵌入资源预算作为一阶概念。

#### (B) AI 管道缺乏防护（方向二）—— **最隐蔽**

- 无 Token 预算：`buildChatPrompt` 直接拼接全部 hits，Agent 消息历史无限追加。给定 `K=20`、chunk size=600，极限上下文约 12K tokens，超过 Mistral 7B (8K)、Phi-3 (4K)、Gemma-2 (8K) 等常见模型的窗口。表现为**静默截断**（LLM 静默丢弃前缀）或**崩溃**（OOM）。
- 无 Relevance Threshold：BM25/Vector 返回的结果即使余弦相似度仅 0.05 也会送入 LLM，稀释上下文质量。
- 无 Budget 保护：虽然有 `AI_TENANT_DAILY_BUDGET_USD`，但仅作用于 Chat，不对 Agent tool calls 做逐步预算检查。

**根本原因**：AI 管道设计时推理层与检索层之间缺少一个 "context window govenor" 中间层——一个负责分配、计量、截断上下文的仲裁者。

#### (C) 运维可观测性缺口（方向四）—— **长期债务**

- `ReindexStale` 无进度追踪、无取消、无暂停。在 embedding 模型变更后需要全量重索引时，千/万级对象的 reindex 操作完全黑盒。`startReindexOnStartup` 甚至只用 `go func()` 一行启动，无幂等保护——多 pod 同时启动时触发重复索引。
- 缺少 `GET /v1/admin/reindex/progress` 等运维 API。这导致生产运维人员必须 grep 日志估算进度。

**根本原因**：Background Job 模式抽象不完整——只定义了触发方式（定时/事件），缺少状态机（pending→running→paused→cancelled→done）和进度契约。

#### (D) S3 兼容性假阳性（方向五）—— **生态债**

- `GetBucketLocation` 返回空 `Location` 字段。主流 S3 SDK（boto3、aws-sdk-go）在创建客户端时会调用此接口，空值可能导致 SDK 行为异常（例如 `aws s3api list-objects` 在某些版本下因 region 校验失败而报错）。`TestBucketLocation` 仅测 200 状态码不验证值，属于**测试盲区**。

### 1.3 架构健康度评分

| 维度 | 评分 | 说明 |
|------|------|------|
| 分层清晰度 | ⭐⭐⭐⭐⭐ | FileService/Storage/Repository 三层职责分明 |
| 扩展性 | ⭐⭐⭐⭐ | 多 Storage backend、多 DB、多 Vector backend 均有工厂 |
| 资源管控 | ⭐⭐ | 无磁盘配额、版本无限、无 token budget |
| 可观测性 | ⭐⭐⭐ | 指标丰富，但 background job 进度为零 |
| S3 兼容性 | ⭐⭐⭐⭐ | 核心路径完备，`GetBucketLocation` 等边缘未覆盖 |
| AI 管道健壮性 | ⭐⭐ | 缺少最重要的上下文窗口防护 |
| 测试覆盖 | ⭐⭐⭐⭐ | unit contract suite 完善，但 `GetBucketLocation` 盲区说明边缘 case 测试不足 |

---

## 2. 扩展方向

基于验证报告暴露的五个盲区，我识别出以下高价值架构扩展方向（按优先级排序）：

### 方向 A：Resource Governor Layer（P0）

**现状**：资源约束散落在各个模块，无统一治理模型。

**推荐方案**：引入 `resource.Governor` 抽象层

```
FileService
  ├── resource.Governor  ← 新增
  │   ├── QuotaChecker      (租户配额)
  │   ├── VersionRetention  (版本保留策略)
  │   ├── DiskCapacity      (存储节点容量)
  │   └── TokenBudget       (AI token 预算)
  │
  ├── Storage
  └── Repository
```

**核心决策点**：

| 选项 | 方案 | 权衡 |
|------|------|------|
| **A1** 同步检查器 | 每次写入前同步调用 Governor 检查 | 延迟增加（+0.1-1ms），实现简单 |
| **A2** 异步预授权 | Token-bucket 风格的预授权窗口 | 降低延迟，但增加 TOCTOU 窗口 |
| **A3** 写前声明式 reserve | PutObject 前 reserve 资源，commit 时 consume | 最安全，但需要 rollback 机制 |

**推荐 A1**作为第一版——aero-vault 的延迟预算可以接受 1ms，实现复杂度最低。

**关键设计原则**：
- `Governor` 以 middleware 参数注入 `FileService`，不侵入 Storage/Repository 接口
- 策略配置统一放在 `BucketConfig` 内（`MaxVersions`, `NoncurrentDays`, `MaxSizeGB` 等）
- `Governor` 的可选性：`nil` governor = 不检查 = 兼容现有行为

### 方向 B：Context Window Governor（P0）

**现状**：chat.go 直接拼接所有检索结果，Agent 循环无限追加。

**推荐方案**：在 `ai` 包内部引入 `contextGovernor` 组件

```
Chat.Answer / Agent.Run
  │
  ├── contextGovernor      ← 新增
  │   ├── TokenCounter        (tiktoken 或 model 专用 tokenizer)
  │   ├── WindowAllocator     (系统提示 → 对话历史 → 检索结果 → 剩余)
  │   ├── SlidingRanker       (按 relevance 裁剪检索结果)
  │   └── BudgetTracker       (逐 token 计量，Agent 中按步扣费)
  │
  └── LLM

Search
  │
  ├── relevance.Filter     ← 新增
  │   └── MinScore(minCosine=0.3, minBM25=5.0) 剔除低质量 chunk
  │
  └── Retrieved Hits
```

**核心配置**：
- `AI_MAX_CONTEXT_TOKENS`：模型上下文窗口上限（默认 4096，需按实际模型调整）
- `AI_MIN_SCORE_VECTOR` / `AI_MIN_SCORE_BM25`：检索结果 relevance 阈值
- `AI_CONTEXT_SYSTEM_TOKENS` / `AI_CONTEXT_HISTORY_TOKENS`：预留给系统提示和历史的比例

**实现注意事项**：
- Token 计数**不可**使用简单字符统计——必须使用模型对应的 tokenizer（tiktoken 对 OpenAI 模型，huggingface tokenizers 对开源模型）
- `contextGovernor` 需要在 `buildChatPrompt` 处介入，而非提前——因为 LLM 返回后才知道实际消耗

### 方向 C：Background Job State Machine（P1）

**现状**：Reindex 匿名 goroutine + 最终一行 log。

**推荐方案**：将 `ReindexStale` 重构为 `Job` 接口的实现

```go
// 新增 internal/job 包
type Job interface {
    ID() string
    Type() JobType
    State() JobState  // pending → running → paused → cancelled → done
    Progress() (completed, total int)
    Run(ctx context.Context) error
    Cancel(ctx context.Context) error
    Pause(ctx context.Context) error
}

type JobRegistry struct {
    jobs sync.Map  // jobID → Job
    mu   sync.Mutex
}
```

**API 暴露**：
- `POST /v1/admin/reindex` → 创建 Job，返回 jobID
- `GET /v1/admin/jobs/{id}` → 查询进度
- `POST /v1/admin/jobs/{id}/cancel`
- `POST /v1/admin/jobs/{id}/pause`

**与现有系统的集成**：
- JobPool（`jobs` 表）用于持久化作业状态——`ReindexStale` 的进度写入 `job_progress` 表
- 集群单例模式：`RECONCILE_CLUSTER_SINGLETON` 通过 `leases` 表防止多 pod 重复 reindex

### 方向 D：S3 兼容性完备化（P2）

**现状**：5 个已识别盲区中最轻量的修复。

**推荐**：将 `S3_REGION` 和 `S3_BUCKET_LOCATION` 加入配置，`GetBucketLocation` 从配置中读取。

```go
type S3CompatConfig struct {
    Prefix         string
    Region         string  // 新增，默认 "us-east-1"
    BucketLocation string  // 新增，默认与 Region 一致
}
```

**扩展机会**：考虑多 region 的 S3 compatible 场景——`BucketConfig` 增加 `region` 字段，允许按 bucket 配置 location。

### 方向 E：Event-Driven Lifecycle Hooks（P2）

**现状**：版本淘汰是方向一的核心风险，`lifecycle.go` 只处理当前版本过期。

**扩展方向**：将 Lifecycle 引擎扩展为通用的事件驱动 hook

```
Object Lifecycle Pipeline:
  object.created  → [Antivirus, Replication, Indexer]
  object.updated  → [Indexer]
  object.deleted  → [Replication, ChunkCleaner]
  object.expired  → [{current expiry, noncurrent expiry, delete_marker expiry}]
                   ↑ 在 Lifecycle 内部：按保留策略分层过期
```

**核心变更**：`lifecycle.go` 增加 `sweepNoncurrent` goroutine，按 `NoncurrentDays` 和 `MaxVersions` 清理非当前版本。

---

## 3. 接口设计建议

### 3.1 关键接口设计原则

**原则一：可选接口优于可选实现**（`nil` 安全 vs. Noop 实现）

```go
// 错误方式：在每个调用点 if governor != nil { ... }
// 正确方式（当前 AGENTS.md I5 标准）：
var defaultGovernor = &noopGovernor{}  // 返回 nil error，无操作

type Governor interface {
    CheckPutObject(ctx, tenant, bucket, key, size) error
    CheckVersionLimit(ctx, tenant, bucket) error
    CheckDiskCapacity(ctx, size) error
}

type noopGovernor struct{}
func (g *noopGovernor) CheckPutObject(...) error    { return nil }
func (g *noopGovernor) CheckVersionLimit(...) error { return nil }
func (g *noopGovernor) CheckDiskCapacity(...) error { return nil }
```

这样 `FileService` 始终有一个有效的 `Governor` 实例，不存在 nil check 遗漏的风险。

**原则二：结构化配置优于参数膨胀**

```go
// 错误方式：BucketConfig 随着功能增加字段膨胀
// 正确方式：使用嵌套配置对象

type BucketConfig struct {
    Versioning   VersioningConfig   // 嵌套
    Lifecycle    LifecycleConfig    // 嵌套
    Lock         LockConfig         // 嵌套
    Quota        QuotaConfig        // 新建
}

type VersioningConfig struct {
    Enabled            bool
    MaxVersions        *int              // nil = 无限（现有行为）
    NoncurrentDays     *int              // nil = 永不过期（现有行为）
}
```

`*int` 设计允许向后兼容——现有 bucket 的 `MaxVersions=nil` 保持原有无限行为。

**原则三：错误分类**——新增 `resource` error 分类，与 `domain` 错误分离

```go
// 新增 internal/errors 包（或 util 但按 AGENTS.md 禁止 utils 包，应放在 internal/domain 下）
var (
    ErrVersionLimitExceeded = &ResourceError{Code: "VersionLimitExceeded", HTTP: 409}
    ErrDiskFull             = &ResourceError{Code: "InsufficientStorage", HTTP: 507}
    ErrContextOverflow      = &ResourceError{Code: "ContextWindowExceeded", HTTP: 400}
    ErrBudgetExceeded       = &ResourceError{Code: "BudgetExceeded", HTTP: 429}
)
```

### 3.2 是否需要新的抽象层

| 新抽象 | 必要性 | 接口规模 | 建议 |
|--------|--------|---------|------|
| `resource.Governor` | **高**——统一资源管控入口 | 3-5 方法 | 新增 |
| `ai.TokenCounter` | **高**——模型适配 | 1 方法 `Count(text) int` | 新增，`ai.Embedder` 平行的第一公民 |
| `job.Job` | **中高**——背景作业标准化 | 6 方法 | 新增，逐步迁移 Reindex/Reconcile |
| `storage.Capability` | **中**——存储后端自治 | 1 方法 `Capabilities() Capability` | 为 Storage 接口增加可选方法 |

**不推荐的抽象**：
- 不引入独立的 `buffer.Api` 或 `pool.ObjectPool`——当前的对象生命周期管理足够健壮
- 不引入 `domain/event` 事件框架——EventBus 已经够用，过度抽象会违反 "Opt-in 安全默认" 原则

### 3.3 向后兼容性策略

| 变更 | 兼容措施 | 废弃路径 |
|------|---------|---------|
| `BucketConfig.Versioning bool → VersioningConfig` | 保留 `Versioning bool` 作为 deprecated alias，读时映射到 `VersioningConfig.Enabled` | 2026 Q4 移除 |
| `preflightQuota` 签名不变 | 内部调用 `governor.CheckPutObject`，不改变对外接口 | N/A |
| `ChatReq` 增加 `MaxContextTokens` | 默认值 = 0 表示使用 server 配置的 `AI_MAX_CONTEXT_TOKENS`，无 break change | N/A |
| S3 `GetBucketLocation` 返回值 | 之前返回空值，现在返回 `S3_REGION` → 这是**修复**不是破坏 | N/A |

---

## 4. 技术选型

### 4.1 是否需要引入新的技术栈

| 组件 | 推荐方案 | 替代方案 | 理由 |
|------|---------|---------|------|
| Token Counter | **tiktoken** (Go binding: `github.com/pkoukk/tiktoken-go`) | huggingface tokenizers | tiktoken 覆盖 OpenAI/Claude 系列模型，Go 生态成熟；与 embedder 使用的 tokenizer 可共用缓存 |
| Progress Storage | **SQLite/Postgres** 已有 `jobs` 表扩展 | Redis Streams / NATS | 保持零外部依赖原则，`jobs` 表增加 `progress` json 列即可 |
| Rate Limiting | 已有 token-bucket | — | 已有实现足够，无需替换 |
| Capacity Query | `os.Statfs` (Linux) for Local；云 SDK API for S3/OSS/COS | — | Platform-specific 封装在 Storage backend 内 |

**不引入**：
- 不引入消息队列（Kafka/RabbitMQ）——EventBus + JobPool 足够支撑当前规模
- 不引入任务编排引擎（Temporal/Cadence）——过度工程，Job state machine 在 Go 内可自实现
- 不引入专门的 AI Gateway——aero-vault 作为独立 AI gateway 的架构设计本身就是去中心化的

### 4.2 第三方依赖评估标准

CI gate（AGENTS.md I6）规定 "新 `go.mod` 依赖需论证"。针对方向一至五的修复，需要评估的依赖：

| 依赖 | 用途 | 评估 |
|------|------|------|
| `github.com/pkoukk/tiktoken-go` | Token 计数 | ✅ 轻度依赖，纯 Go 实现，无 CGO，LICENSE MIT |
| `github.com/prometheus/client_golang` | 已有 | N/A |
| `github.com/mattn/go-sqlite3` | 已有 | N/A |

**结论**：修复五个盲区不需要引入任何新第三方依赖。`tiktoken-go` 是唯一个可能需要引入的，但 token 计数也有纯 Go 硬编码方案（仅支持一个模型）作为 zero-dependency 备选。

### 4.3 自建 vs 采购

| 场景 | 自建理由 | 采购不推荐理由 |
|------|---------|-------------|
| Token counter | 模型 tokenizer 是公开算法，无需外部服务 | 每次调用都请求外部 API 延迟高、成本高 |
| Disk capacity | `os.Statfs` 一行代码 | 无外部服务提供此信息 |
| Quota enforcement | 需要深度集成到 FileService 事务路径 | 外部服务影响写入延迟，增加故障点 |
| Progress tracking | 增量状态存储在 jobs 表即可 | 无合适的 off-the-shelf 替代 |

---

## 5. 实施路线图

### 5.1 优先级排序

```
Sprint N (当前)          Sprint N+1             Sprint N+2             Sprint N+3
────────────────────     ────────────           ────────────           ────────────
Direction 1              Direction 2             Direction 4            Direction 5
Version Bloat            Context Window          Reindex Progress       Bucket Location
                          
Direction 3              ── 并行 ──>             ── 并行 ──>           
FS Quota
```

**排序逻辑**：
- **P0 (Sprint N)**：方向一 + 方向三 = **资源管控**。直接关系到生产稳定性（版本无限增长导致磁盘满、TOCTOU 配额绕过导致超额）。两个方向共享 `resource.Governor` 和 `BucketConfig` 扩展，可以合并为单次架构变更。
- **P1 (Sprint N+1)**：方向二 = **AI 健壮性**。当前 chat/agent 暴露 LLM token overflow 是最快的用户可见的质量问题。
- **P2 (Sprint N+2)**：方向四 = **运维体验**。对业务无直接影响，但长期运维负担。
- **P3 (Sprint N+3)**：方向五 = **生态兼容性**。影响面最小，可灵活安排。

### 5.2 阶段划分

#### 阶段 I：Resource Governor 引入（Sprint N，4-6 天）

| 步骤 | 产出 | 风险 |
|------|------|------|
| 1. 定义 `resource.Governor` 接口 + `noopGovernor` | `internal/resource/governor.go` | 无——纯新增 |
| 2. `BucketConfig` 增加 `VersioningConfig` | 向后兼容的配置结构 | 配置迁移：现有 config 无 `MaxVersions`，需合理默认值 |
| 3. 实现 `VersionRetentionGovernor.CheckVersionLimit` | 写入时检查版本数 | SQL 复杂度：COUNT versions per bucket 在版本量级大时慢 |
| 4. 实现 `QuotaGovernor.CheckPutObject` + 事务内配额扣减 | 原子配额操作 | 事务范围扩展增加锁争用 |
| 5. `FileService` 注入 Governor | 服务层变更 | 需要确保 handler 测试 mock |
| 6. `lifecycle.go` 增加 `sweepNoncurrent` routine | 非当前版本过期 | 与 `sweepExpired` 互斥？需要加锁或协调 |
| 7. Contract tests + integration tests | 配额竞态测试 | 需要并发测试路径 |

**核心风险**：`CheckVersionLimit` 的 SQL 性能。`COUNT(*) WHERE bucket_id = ? AND version IS NOT NULL` 在海量版本切换后可能变慢。缓解措施：
- 在 `bucket_config` 表增加 `version_count` 汇总列（写时递增/递减）
- 或使用 `EXPLAIN` 确保索引覆盖

#### 阶段 II：Context Window Governor（Sprint N+1，3-5 天）

| 步骤 | 产出 | 风险 |
|------|------|------|
| 1. 定义 `TokenCounter` 接口 + tiktoken 实现 | `ai/token.go` | tiktoken-go 可能出现 OOV（out-of-vocabulary），需要 fallback |
| 2. `Config` 增加 `AI_MAX_CONTEXT_TOKENS`/`AI_MIN_SCORE_*` | 配置项 | 默认值设定：4K 对大部分模型太保守，8K 又太激进 |
| 3. 实现 `contextGovernor.buildContext` | 替换 `buildChatPrompt` | 需要精确 token 计量，不同模型不同 |
| 4. 实现 `relevance.Filter` | 检索后过滤 | 低阈值 chunk 的合理默认值 |
| 5. Agent 循环增加 token budget 检查 | 工具循环每一步前检查 | 与 `dailyBudget` 的交互 |
| 6. SSE Chat stream 增加 `event: context` 信息帧 | 报告 token 使用状态 | 前端需要解析新 event type |

**核心风险**：Token 计数的性能。对每次 chat/agent 调用，需要对系统提示 + 对话历史 + K×chunks 做 tokenize。缓解措施：
- 缓存 tokenizer 实例（线程安全）
- 对话历史增量 token 计数（每次追加时累计，避免重复 tokenize）

#### 阶段 III：Background Job 标准化（Sprint N+2，2-3 天）

| 步骤 | 产出 |
|------|------|
| 1. 定义 `job.Job` 接口 + `JobRegistry` | `internal/job/job.go` |
| 2. `ReindexStale` 重构为 `ReindexJob` | `internal/job/reindex_job.go` |
| 3. SQL schema `job_progress` 表 | 迁移文件 |
| 4. REST API `GET /v1/admin/jobs/{id}` + cancel/pause | `rest/admin_jobs.go` |
| 5. 移除 `startReindexOnStartup` 的裸 `go func()` | 统一使用 JobRegistry |

#### 阶段 IV：S3 Location 修复（Sprint N+3，0.5-1 天）

| 步骤 | 产出 |
|------|------|
| 1. `S3CompatConfig` 增加 `Region`/`BucketLocation` | 配置变更 |
| 2. `getBucketLocation` 读取配置 | handler 变更 |
| 3. `TestBucketLocation` 增加值断言 | 测试修复 |

### 5.3 风险点和缓解策略

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| **Version 计数 SQL 瓶颈** | 中 | 高 | 使用汇总列 + 异步同步，或增加 `INSERT...ON CONFLICT` 原子递增 |
| **Token 计数性能** | 中 | 中 | 增量 token 计数、tokenizer 实例池、prompt 长度缓存 |
| **配额原子化引入死锁** | 低 | 高 | 使用 SQLite 的 `BEGIN IMMEDIATE` 避免重试；Postgres 使用 `SELECT...FOR UPDATE` |
| **Governor 配置向后兼容** | 低 | 中 | `*int` 设计 + `Versioning bool` deprecated alias，读时转换 |
| **Reindex 重构破坏现有行为** | 中 | 中 | 旧 `ReindexStale` 保留作为迁移期间的 fallback，双写兼容 |

### 5.4 关键里程碑

| 里程碑 | 时间 | 可验证目标 |
|--------|------|-----------|
| M0: Resource Governor 合并 | Sprint N 结束 | `make check` 全绿 + 新增 contract tests 覆盖配额/版本限制 |
| M1: Context Window 保护上线 | Sprint N+1 结束 | Chat stream 中 context 使用量的 SSE 报告 + 超越 `AI_MAX_CONTEXT_TOKENS` 时的截断行为 |
| M2: Background Job API | Sprint N+2 结束 | `curl localhost:8080/v1/admin/jobs/{id}` 返回进度 |
| M3: S3 Location 修复 | Sprint N+3 结束 | `aws s3api get-bucket-location --endpoint-url http://localhost:8080` 返回有效 region |

---

## 总结

这五个盲区虽然各自独立，但汇聚到了一个共同的架构痛点：**aero-vault 的资源治理模型是隐式的而不是显式的**。

- 版本无限膨胀 = 没有显式的**版本保留契约**
- Chat Context 无保护 = 没有显式的**上下文预算契约**
- Quota TOCTOU = 没有显式的**配额事务契约**
- Reindex 无进度 = 没有显式的**作业状态机契约**
- Bucket Location 空值 = 没有显式的**部署拓扑契约**

五个盲区映射到同一个修复模式：**将隐式假设升级为显式契约**。引入 `resource.Governor` 作为统一的显式资源治理入口，将从根本上改变 aero-vault 的资源模型成熟度——从 "假设无限" 到 "假设有限，显式约束"。

建议从 **方向一** 和 **方向三** 联合启动（Sprint N），因为它们共享 `resource.Governor` 这同一个架构决策。方向二紧随其后（Sprint N+1），作为提高 AI 服务质量的关键防御。
