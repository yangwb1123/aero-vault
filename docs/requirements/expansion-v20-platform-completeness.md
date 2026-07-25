# AeroVault 高价值扩展方向（第二十期）

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全局代码扫描（`internal/` 全部子包、`cmd/server/main.go`、三套 SDK、Web UI、CLI、24 对迁移文件、部署配置、`docs/` 全量文档）。逐一比对前 19 期 expansion 文档（v1–v19，累积 ~1.5MB+ 分析）、`ROADMAP.md`（10 方向）、`analysis-gaps-roadmap.md`（5 方向 + 12 边界情况 + 11 性能优化）、`CHANGELOG.md`、`TODO.md`，确认每个方向在**既有文档中零覆盖**。  
> **日期：** 2026-07-10  
> **原则：** 选取 5 个**平台完备性 / 企业就绪 / 生态扩展**方向——不是功能点堆叠（前 19 期已覆盖 ~95 个方向），而是**从"功能完整的单节点服务"到"可运营、可治理、可集成的企业平台"的根本性缺失**。每个方向附带：代码锚点 → 当前状态 → 缺失能力 → 边界情况 → 架构概要 → 实现理由。**不编写任何实现代码。**

---

## 审阅背景：前十九期覆盖的去重矩阵

前十九期已从 19 个视角覆盖约 95 个方向。以下大类**本期不再重复**，列为已知基础：

| 领域 | 覆盖期数 | 方向数 |
|------|---------|--------|
| AI/RAG 管线（Embed/Search/Chat/Agent/Rerank/PII/Indexer/Cache/Lineage/Budget） | v1~v13, ROADMAP #1~#2 | ~12 |
| S3 兼容性（子资源/Batch/Multipart/ACL/Policy/CORS/Logging/Notification/LegalHold） | v1, v4, v6, v8~v10, v16, v17, ROADMAP #7 | ~12 |
| 存储后端（S3/OSS/COS/KMS/SSE/Encryption/CircuitBreaker/Multi-Backend/Tiering） | v4~v15, v17, ROADMAP #5, #9, analysis-gaps #1 | ~10 |
| 认证授权（JWT/API Key/SigV4/OIDC/SAML/SCIM/MFA/Policy Engine/MFA Delete） | v1, v5, v8, v11, v12, v15~v17 | ~9 |
| 多租户（CRUD/Quota/Budget/Audit/Governance/日费用/隔离/Billing） | v1, v3~v5, v7, v8, v11, v12, v17, v7（Billing） | ~8 |
| 事件/通知/Webhook/SSE（Bus/Transport/Filter/Multi-Destination/外部队列） | v1, v3~v6, v8, v9, v11, v12, v17, analysis-gaps #4 | ~8 |
| 复制/高可用/集群（CRR/SRR/Cluster Singleton/HA/Federation/多区域主动-主动） | v1, v3~v5, v9, v17, ROADMAP #3, #10, v5（Federation）, analysis-gaps #2 | ~8 |
| 存储分层/生命周期转换（Glacier/IA/Transition/NoncurrentVersion/AbortMPU） | v1, v3, v5, v15, v17, ROADMAP #9 | ~6 |
| Reconcile/GC/Lifecycle（Orphan/Retention/Scrub/Version Governance） | v1, v4, v6, v7, v15, ROADMAP #5, #8 | ~6 |
| 合规（WORM/Legal Hold/Retention/Client Encryption/Access Log/MFA Delete/Sensitivity） | v2, v6, v8~v10, v12, v16, v17 | ~6 |
| 可观测性（OTel/Metrics/Grafana/Prometheus/SLO/Tracing/pprof/Debug） | v11, v13, v14 | ~4 |
| 工程质量（内存安全/并发/压缩/诊断/错误模型/Crash Recovery/流式处理） | v11, v14, v15, analysis-gaps | ~6 |
| Web UI / Admin Console 生产化 | v3, v6, v10, v11, v18, v19 | ~6 |
| SDK 跨语言完整性 | v11, v18 | ~2 |
| 基础设施（配置热重载/IP ACL/内置 TLS/ACME/Feature Flag/CDN 集成/FUSE） | v13, v16, analysis-gaps #3 | ~5 |
| 导入/迁移/批量操作工具 | v18 | ~1 |
| 插件/扩展/钩子系统 | v18 | ~1 |
| 性能基准与容量规划 | v18 | ~1 |
| 多协议一致性语义 | v19 | ~1 |
| 结构化元数据查询引擎（Faceted Search） | v19 | ~1 |
| 租户级加密密钥隔离 | v19 | ~1 |
| 客户端上传会话韧性（Resumable Upload） | v19 | ~1 |
| 存储后端运行状况监控与故障切换 | v19 | ~1 |
| 对象内容缓存层（内存+CDN） | analysis-gaps #5 | ~1 |
| 其他（API 治理/备份/优雅关闭/分享链接/Snapshot） | v2, v4, v8, v10, v11 | ~5 |

---

## 本期方向总览

| # | 方向 | 类型 | 影响评估 | 核心代码锚点 | 既有覆盖 |
|---|------|------|---------|-------------|---------|
| 1 | **🔴 声明式 Desired-State 配置协调（GitOps Controller）** | 架构/运维 | 多租户 SaaS 平台的基础设施即代码（IaC）能力缺失；部署不能重复、配置不能版本化、审计不能追溯 | `internal/config/`（全部 env var 驱动）、`internal/api/rest/admin.go`（18 admin handler 均为热 API 调用）、`internal/reconcile/`（周期扫描器未用于配置协调） | **零覆盖** |
| 2 | **🔴 用户自定义事件钩子 / Serverless Function Triggers** | 产品/架构 | 事件总线只触发内部订阅者；用户无法注入自定义逻辑。对比 AWS Lambda + S3 是代际差距 | `internal/events/bus.go`（Publish → 固定订阅者）、`internal/jobs/`（内部 Job handler）、`internal/repository/jobs.go`（Job 类型硬编码） | analysis-gaps 功能差距表第 3 项提及，**无独立方向设计** |
| 3 | **🟠 对象元数据 Schema 治理与结构化验证** | 安全/治理 | 元数据是自由格式 `map[string]string`，无 Schema 约束、无类型校验、无索引。企业合规场景不可接受 | `internal/repository/repository.go`（`Object.Metadata map[string]string` 无验证）、`internal/api/rest/dto.go`（无 metadata schema 参数）、`internal/service/file_crud.go:Put`（metadata 直接透传） | **零覆盖** |
| 4 | **🟠 统一备份与灾难恢复框架** | 运维/可靠性 | 单个组件存在（复制、Snapshot、Cluster Singleton），但无统一 DR 编排：无自动故障转移、无 PITR、无跨区域 DR 演练、无备份调度策略 | `internal/replication/`、`internal/snapshot/snapshot.go`、`internal/cluster/singleton.go`、`internal/reconcile/` | v5 Federation（数据共享 ≠ DR）；v2/v4/v11 行级提及备份，**零独立方向** |
| 5 | **🟠 公平调度与多租户资源隔离（Weighted Fair Queuing）** | 性能/多租户 | 并发限流器全局共享、RateLimiter 每租户固定桶。无权重公平队列、无预留容量、无突发配额、无优先级调度 | `internal/middleware/ratelimit.go`（固定桶 Token-Bucket）、`internal/middleware/concurrency.go`（全局信号量，`PerTenantMax` 简单均分） | v11 行级提及并发限流器粒度，**零独立方向** |

---

## 1. 🔴 声明式 Desired-State 配置协调（GitOps Controller）

### 为什么需要它

当前 AeroVault 的配置完全由环境变量和热 API 调用驱动：

- **基础设施配置**：监听地址、存储后端、DB DSN、日志级别 — 全部来自 env var，非运行时不可变
- **业务配置**：租户、API Key、配额、预算、桶策略、生命周期规则 — 全部通过 `POST /v1/admin/*` 热 API 创建，**不存在于任何代码仓库中**
- **描述 vs 现实偏差**：没有机制验证"当前系统中的租户/Key/配额是否与团队期望的一致"

这意味着：
- 无法对配置做 Code Review（所有变更通过 curl 或 SDK 直接作用于运行系统）
- 无法做配置回滚（"昨天还有这个租户，谁删的？"）
- 无法做可复现部署（"测试环境的配置和生产一样吗？"）
- 无法做自动配置修复（"Quota 被意外修改了，谁改回去？"）

**代码锚点：**

| 文件 | 当前状态 | 缺失能力 |
|------|---------|---------|
| `internal/config/config.go` | 仅 `envconfig` 结构体解析 | 无外部配置源读取（无 YAML/file/consul/etcd） |
| `internal/api/rest/admin.go` | 18 个 handler 直接修改运行时状态 | 无"声明 → 协调 → 状态"三阶段循环 |
| `internal/reconcile/` | 只有孤儿清理/Lifecycle/Retention | 无配置协调器 |
| `cmd/server/main.go` | 启动时一次性初始化 | 无启动后配置同步 |

### 缺失能力

- **声明式配置源**：读取一个或多个 YAML/JSON 文件（或目录、URL、Git repo），声明期望的租户、Key、桶配置、配额、预算、CORS、生命周期规则
- **Desired-State 协调循环**：以可配置间隔（如 5 分钟）扫描配置源，与系统当前状态对比，自动创建/更新/删除差异资源
- **干运行模式（Dry-Run）**：协调器支持 `--dry-run`，只报告差异不执行变更
- **冲突检测**：当热 API 直接修改了协调器管理的资源时，协调器可选择"覆盖"或"告警保留"策略
- **审计溯源**：每次协调变更写入 `audit_log`，标记来源为 `gitops`
- **允许多源共存**：部分资源由 GitOps 管理，部分由热 API 管理（通过标签/注解区分）

### 边界情况

| 场景 | 风险 | 处理策略 |
|------|------|---------|
| 配置源中删除了一个已有租户 | 级联删除（可能误删） | 协调器默认不做级联删除，只标记 `orphan` 状态 -> 手动确认 |
| 热 API 与 GitOps 并发修改同一 Key | 最后写入者覆盖 | 使用 `resource_version` / 乐观锁；GitOps 标记 <managed-by> 注解，非标记资源不受影响 |
| 配置源暂时不可读（网络分区） | 协调器无法运行 | 跳过本次协调 + 告警；当前状态保持不变 |
| 不同环境（dev/staging/prod）的配置差异 | 配置漂移 | 通过不同 config source URL/分支管理，协调器本身无环境概念 |
| 协调期间热 API 创建了同名资源 | 冲突 | 协调器使用 `UPSERT` 语义 + 比较字段级差异，避免纯覆盖 |

### 架构概要

```
┌──────────────────────────────────────────────────┐
│                Declarative Config Source          │
│  (YAML file / Dir / Git Repo / HTTP / S3 Object)  │
│                                                    │
│  tenants:                                          │
│    - id: acme                                      │
│      status: active                                │
│      quota: {max_bytes: 1GB, max_objects: 10000}   │
│      budget: {daily_micros: 5000000}               │
│      keys:                                         │
│        - label: ci-user                            │
│          scopes: [read, write]                     │
│  buckets:                                          │
│    - name: backups                                 │
│      versioning: true                              │
│      lifecycle:                                    │
│        - days: 30, action: soft_delete             │
│      cors:                                         │
│        - allowed_origins: ["https://app.example"]  │
└──────────────────┬───────────────────────────────┘
                   │ Watch / Poll
                   ▼
┌──────────────────────────────────────────────────┐
│           ConfigReconciler (新包)                   │
│  internal/configreconciler/                         │
│    ├── reconciler.go       // 协调循环              │
│    ├── source.go           // 配置源接口（file/git/url） │
│    ├── diff.go             // 期望状态 vs 当前状态差异引擎 │
│    ├── plan.go             // 变更计划（创建/更新/删除） │
│    ├── executor.go         // 执行变更（复用 admin handler） │
│    ├── audit.go            // 审计日志记录            │
│    └── marker.go           // managed-by 标签管理     │
│                                                    │
│  复用: internal/api/rest/admin.go（将 handler 逻辑    │
│        提取为可编程接口，而非 HTTP handler 专用）       │
└──────────────────┬───────────────────────────────┘
                   ▼
┌──────────────────────────────────────────────────┐
│              Repository（当前系统状态）              │
│  tenants / api_keys / bucket_configs / quotas     │
└──────────────────────────────────────────────────┘
```

### 实现理由

- **复用率 60%+**：`admin.go` 的 18 个 handler 已经是"将请求体转换为 DB 操作"的逻辑，提取为 `Executor` 层即可。`reconcile/` 的定时器架构可直接复用。
- **零协议层变更**：REST/S3/WebDAV/MCP 完全不受影响 — 协调器只与 `FileService` + `Repository` 交互。
- **企业采纳的硬门槛**：任何面向平台团队的产品都必须支持 IaC。没有 GitOps 能力，AeroVault 无法进入"Infrastructure Platform"采购清单。
- **差异化优势**：MinIO、Ceph、AWS S3 均不提供内置 GitOps 协调器——这是一个独特的产品定位。

---

## 2. 🔴 用户自定义事件钩子 / Serverless Function Triggers

### 为什么需要它

当前事件系统 (`internal/events/bus.go`) 的能力：

| 能力 | 当前状态 |
|------|---------|
| 事件发布 | ✅ `bus.Publish(ctx, event)` — 持久化到 DB + 内存广播 |
| Webhook | ✅ `events.NewWebhook(url)` — HMAC-SHA256 HTTP POST |
| 内部 Job | ✅ `jobs.Registry` — 固定 handler（索引/AV/复制） |
| 用户自定义钩子 | ❌ 用户无法注入自己的处理逻辑 |

这意味着：
- 用户想在文件上传后自动调用自己的 API（不是 webhook URL，而是复杂的编排）→ ❌ 不能
- 用户想在文件删除后清理外部数据库中的关联记录 → ❌ 不能
- 用户想在文件被检测到 PII 后自动触发审批工作流 → ❌ 不能
- 用户想按自己的逻辑过滤、转换、路由事件 → ❌ 不能

**代码锚点：**

| 文件 | 当前状态 | 缺失能力 |
|------|---------|---------|
| `internal/events/bus.go:Subscribe()` | 只返回内部 `<-chan Event` | 无用户注册钩子的通道 |
| `internal/jobs/registry.go` | Job 类型与 handler 在 main.go 硬编码注册 | 无动态注册机制 |
| `internal/repository/jobs.go` | `JobType` 用 const 定义 | 无用户自定义 Job 类型 |
| `internal/repository/migrations/sqlite/0009_jobs.up.sql` | `jobs` 表 | 无 `user_script` / `handler_url` 字段 |

### 缺失能力

- **用户注册钩子**：通过 API (`POST /v1/admin/hooks`) 注册事件 → 动作映射，支持：
  - **Webhook 增强**：多 URL、条件过滤（仅特定 bucket/prefix/event 触发）、重试策略自定义
  - **内部函数**：用户在配置中注册 JavaScript/Lua/Wazero 脚本，运行在沙箱中
  - **外部 Lambda**：通过标准 CloudEvents 协议推送到 OpenFaaS / KNative / AWS Lambda
- **事件过滤 DSL**：声明式过滤规则（`event.type == object.created && object.bucket == "incoming" && object.size < 10MB`）
- **钩子链 / 管道**：一个事件触发多个钩子，可指定执行顺序和条件分支
- **执行结果回调**：钩子完成后通知原始调用者（可选，通过 webhook 或 SSE）
- **钩子治理**：超时、重试、死信、执行日志

### 边界情况

| 场景 | 风险 | 处理策略 |
|------|------|---------|
| 用户脚本死循环 / OOM | 影响主进程 | 沙箱执行 + 资源限制（内存/CPU/超时） |
| 钩子内调用 AeroVault API 导致循环触发 | 事件风暴 | 每条事件携带 `X-Aero-Event-Id`，钩子链检测并丢弃循环 |
| 钩子执行失败（依赖的外部系统不可达） | 业务中断 | 可配置：阻塞/忽略/重试/死信 |
| 多个钩子注册到同一事件 | 执行顺序不确定 | 提供优先级字段，同优先级并行 |
| 钩子注册后配置源被删除 | 孤儿钩子 | 协调器（方向 1）标记并清理 |

### 架构概要

```
EventBus Publish
    │
    ▼
┌───────────────────────────────┐
│       HookRegistry (新)         │
│   internal/events/hooks.go     │
│                                │
│   map[EventType][]Hook         │
│     ├── WebhookHook (增强)     │
│     ├── ScriptHook (沙箱)      │
│     ├── LambdaHook (CloudEvents)│
│     └── InternalJobHook (复用  │
│         现有 Job 队列)          │
└───────────┬───────────────────┘
            │
            ▼ 并发分叉执行
    ┌───────┼───────┐
    ▼       ▼       ▼
 Webhook  Script  Lambda
  POST    沙箱    CloudEvents
```

**关键设计决策：**
- 钩子在发布者的 goroutine 外异步执行（不影响主请求路径）
- 执行结果写入 `hook_executions` 表（可查询、可重试）
- 沙箱脚本可通过安全 SDK 调用有限的 AeroVault API（白名单+速率限制）

### 实现理由

- **产品差异化**：即使是 MinIO 也只支持 Webhook 通知，不支持用户自定义 Serverless 函数。这是 AeroVault 建立"AI-native 对象平台"身份的杀手级能力。
- **复用现有基础设施**：`internal/jobs/` 的持久化重试队列 + `internal/events/bus.go` 的事件分发 + `internal/reconcile/` 的定时清理——天然支撑钩子系统。
- **渐进式实现**：可以先做 Webhook 增强（多 URL + 过滤），再加入脚本沙箱，最后做 Lambda 集成——每一步都有独立价值。
- **AI 用例驱动**：用户在文件上传后自动触发 AI 提取/分类/翻译——这不是"增强功能"，而是核心使用场景。

---

## 3. 🟠 对象元数据 Schema 治理与结构化验证

### 为什么需要它

当前元数据处理方式：

```go
// internal/repository/repository.go
type Object struct {
    // ...
    Metadata map[string]string // 自由格式，无约束
}
```

元数据直接透传：
- `service/file_crud.go`: 用户 PUT 时传入的 metadata 直接存储
- `api/rest/handler.go`: 从 `X-Amz-Meta-*` / `X-Aero-Meta-*` 解析，无 Schema 校验
- API 响应中直接返回，无字段过滤或脱敏

这意味着：
- 不能要求"所有上传的文档必须有 `department` 和 `retention_period` 标签" → **合规缺失**
- 不能限制 `classification` 字段只能取 `public|internal|confidential|restricted` → **数据泄露风险**
- 不能按 `project` 字段做索引和过滤 → **无法对元数据做结构化查询**
- 不能阻止用户在 metadata 中存储 1MB 的 Base64 数据 → **元数据膨胀**

**代码锚点：**

| 文件 | 当前状态 | 缺失能力 |
|------|---------|---------|
| `internal/repository/repository.go:Object.Metadata` | `map[string]string` | 无验证接口、无约束定义 |
| `internal/service/file_crud.go:Put()` | 直接将 metadata 传递到 repo | 无 Schema 校验调用 |
| `internal/api/rest/handler.go` | 解析 `X-Amz-Meta-*` | 无 Schema 验证中间件 |
| `internal/api/s3compat/handler.go` | 同样透传 | 同上 |
| `internal/repository/repository.go:SearchChunks` | 仅搜索 chunk 内容 | 不能按 metadata 字段过滤 |

### 缺失能力

- **桶级 Metadata Schema 定义**：在 BucketConfig 中定义允许/要求的 metadata 字段：
  ```yaml
  metadata_schema:
    classification:
      type: enum
      values: [public, internal, confidential, restricted]
      required: true
    department:
      type: string
      max_length: 100
      required: false
    project_id:
      type: string
      pattern: "^PROJ-[0-9]{4}$"
    retention_days:
      type: integer
      min: 1
      max: 3650
    owner_email:
      type: email
  ```
- **写入时 Schema 验证**：所有 PUT/CompleteMultipart 路径上的 metadata 必须在写入前通过 Schema 校验，非法值返回 `400 InvalidMetadata`
- **Schema 版本化**：Schema 可更新，旧对象保留旧 Schema 版本标记；协调器可在后台重新校验/迁移
- **敏感字段标记**：Schema 支持 `sensitive: true` 标记，返回 API 响应时自动脱敏（如 `owner_email` 在响应中返回 `j***@example.com`）
- **元数据索引**：按 Schema 定义的索引字段，在 `list_objects` 中支持 `?metadata.department=engineering` 过滤
- **Schema 继承/复用**：系统级 + 租户级 + 桶级 Schema 合并（租户覆盖系统，桶覆盖租户）

### 边界情况

| 场景 | 风险 | 处理策略 |
|------|------|---------|
| 已有对象不符合新 Schema | 批量写入失败 | Schema 变更规则：加字段 = 兼容，改类型/加约束 = 新版本 + 后台迁移 |
| 同一 bucket 混合 Schema 版本 | 查询语义模糊 | 查询时按最新 Schema 解释，缺失字段视为 null |
| S3 兼容接口的 `x-amz-meta-*` 无 Schema 概念 | 协议冲突 | S3 路径上 Schema 验证可选（可配置），命中未知字段时忽略（非拒绝） |
| metadata value > 数据库字段长度 | 写入截断/失败 | Schema 定义 `max_length`，未定义时默认 512 字节 |
| Schema 与 Existing ACL/Tag 互操作性 | 治理重叠 | Schema 是可选层，不替代 ACL/Tag；对象仍可用 ACL 和 Tag |

### 架构概要

```
PUT /v1/files/doc.txt (X-Aero-Meta-Classification: restricted)
    │
    ▼
┌──────────────────────────────────────────────────┐
│            MetadataSchemaValidator (新)            │
│   internal/metadata/validator.go                   │
│                                                    │
│   1. 查询桶的 MetadataSchema (来自 BucketConfig)   │
│   2. 按 Schema 规则逐一验证每个字段:               │
│      - required? type? pattern? min/max? email?   │
│   3. 敏感字段标记 → 写时原始值，读时脱敏           │
│   4. 返回验证结果: valid / invalid_fields[]       │
└──────────────────┬───────────────────────────────┘
                   ▼
        若 valid → 继续 Put 路径
        若 invalid → 返回 400 + 错误详情
```

**扩展路径：**
1. **Phase 1**: 桶级 Schema 定义 + 写入时验证（最核心）
2. **Phase 2**: 敏感字段标记 + 读取时脱敏
3. **Phase 3**: 元数据索引 + `list_objects` 过滤
4. **Phase 4**: Schema 版本迁移 + 后台重新校验协调器

### 实现理由

- **企业合规核心需求**：GDPR 要求知道哪些字段是个人数据；HIPAA 要求对 PHI 字段做访问控制；PCI 要求标记持卡人数据字段。没有 Schema 治理就无法证明合规。
- **与 Faceted Search（v19 方向 2）互补**：Faceted Search 解决"搜索元数据"的问题；Schema 治理解决"如何定义和约束元数据"的问题。两者是同一枚硬币的两面。
- **低侵入性**：Schema 验证可以包装在 `service.FileService` 的 Put 路径上（中间件模式），不影响 Storage/Repository 层。S3 路径可选择绕过。
- **复用现有 BucketConfig**：当前 BucketConfig 已经持久化在 `buckets` 表中，增加 `metadata_schema` 字段即可（新迁移，非侵入）。

---

## 4. 🟠 统一备份与灾难恢复框架

### 为什么需要它

当前系统有以下 DR 相关组件，但各自独立、无统一编排：

| 组件 | 用途 | 局限性 |
|------|------|--------|
| `internal/replication/` | 跨后端对象复制 | 单向、单目标、无协调 |
| `internal/snapshot/snapshot.go` | SQLite + Local FS 快照 | 仅限 SQLite、仅限 local FS、手动触发 |
| `internal/cluster/singleton.go` | 协调器防重 | 仅用于 reconcile，非通用故障转移 |
| `repository.Migrate()` | Schema 迁移 | 仅在启动时执行，无向后兼容/回滚 |

这意味着：
- **没有 RPO（恢复点目标）保障**：如果凌晨 3 点 SQLite 文件损坏，丢失最后写入的数据量是多少？— 不可知
- **没有 RTO（恢复时间目标）保障**：从备份恢复需要多少时间？— 手动流程，数小时
- **没有跨区域 DR 演练**：生产环境是否真的能从灾难中恢复？— 没人知道
- **没有自动故障转移**：主节点宕机后，备用节点需要手动启动 — 分钟级到小时级停机

**代码锚点：**

| 文件 | 当前状态 | 缺失能力 |
|------|---------|---------|
| `internal/replication/` | 对象级别的单向复制 | 无元数据复制、无双向同步、无一致性检查 |
| `internal/snapshot/snapshot.go` | SQLite + Local FS，264 行 | 无 Postgres 支持、无调度、无生命周期管理 |
| `internal/cluster/singleton.go` | Leases 表 + 单例守卫 | 无热备/温备切换 |
| `cmd/server/main.go:runServer()` | 优雅关闭 | 无故障检测、无注册/发现 |
| `internal/repository/migrations/` | 24 对迁移 | 无回滚策略、无向后兼容验证 |

### 缺失能力

- **元数据复制策略**：将 `repository` 中关键表（objects、versions、uploads、chunks、tenants、api_keys）的变更实时或准实时复制到 DR 站点的数据库中
- **定时元数据全量备份**：可配置的备份调度（如每小时增量、每天全量），备份到可配置的后端（local/S3/任何 storage backend）
- **备份生命周期管理**：保留策略（保留最近 7 天每小时 + 30 天每天 + 12 个月每月）、自动清理过期备份
- **DR 恢复运营手册**：一键恢复流程：
  - 从备份重建元数据库
  - 验证对象存储与元数据一致性
  - 回放增量变更日志（WAL / event log）
  - 更新 DNS / 负载均衡指向恢复站点
- **跨区域元数据复制**：利用 Postgres 流复制或应用层 `EventBus` + `PostgresTransport` 实现元数据跨区域同步
- **故障转移检测与自动切换**：`/readyz` 增强版、健康检测 + DNS 切换 / 负载均衡器 API 调用
- **DR 演练模式**：非中断性演练——在隔离环境中从最近的备份恢复，验证数据完整性，生成报告

### 边界情况

| 场景 | 风险 | 处理策略 |
|------|------|---------|
| 备份进行中主节点崩溃 | 不一致的备份 | 使用数据库快照 / `BEGIN; ... COMMIT;` 事务级一致性 |
| 恢复后发现对象存储缺少部分 blob | 元数据引用不存在对象 | 恢复后的第一步：reconcile 孤儿扫描，标记缺失 blob |
| 跨区域复制延迟导致数据不一致 | 读取旧数据 | 最终一致性模型 + `s3-compat` 的 Read-After-Write 一致性声明 |
| 备份存储在同一集群内 | 备份随主集群一起丢失 | 强制要求备份存储在不同的存储后端/区域 |
| 多次恢复间的不兼容（Schema 版本变化） | 旧备份无法直接恢复 | 恢复流程中包含 Schema 迁移重放 |

### 架构概要

```
┌─────────────────────────────────────────────────────┐
│              DR Orchestrator (新包)                   │
│          internal/dr/                                 │
│                                                       │
│  ├── backup.go          // 备份调度器                  │
│  │   ├── Schedule: cron 表达式                         │
│  │   ├── BackupDB:    pg_dump / sqlite3 .backup       │
│  │   ├── BackupEvents: event_log 表快照               │
│  │   └── BackupConfig: env + 配置快照                 │
│  │                                                     │
│  ├── restore.go         // 恢复协调器                  │
│  │   ├── SelectBackup: 按时间戳选择最近的可用备份      │
│  │   ├── RestoreDB:    恢复数据库                       │
│  │   ├── RunMigration: 必要时回放迁移                  │
│  │   ├── VerifyIntegrity: 对象存储一致性检查           │
│  │   └── SwitchTraffic: 更新 DNS/LB                    │
│  │                                                     │
│  ├── replication.go     // 元数据复制                    │
│  │   └── 利用 EventBus + PostgresTransport 实现         │
│  │       准实时元数据复制到 DR 站点                     │
│  │                                                     │
│  ├── health.go          // 故障检测                      │
│  │   ├── ProbeDB:       Ping + 复制延迟                 │
│  │   ├── ProbeStorage:  存储后端健康                    │
│  │   └── ProbeBus:      事件总线健康                    │
│  │                                                     │
│  ├── failover.go        // 故障转移编排                  │
│  │   └── 检测 → 确认 → 提升 → 切换 → 通知              │
│  │                                                     │
│  └── drill.go           // DR 演练                       │
│      └── 在隔离环境执行完整恢复流程，生成合规报告        │
└─────────────────────────────────────────────────────┘
```

### 实现理由

- **RPO/RTO 是 SLA 的基础**：没有 DR 框架就无法对外承诺恢复时间/恢复点。这是企业采购合同的必备条款。
- **复用率 50%+**：`internal/replication/` 的对象复制层、`internal/snapshot/snapshot.go` 的快照逻辑、`internal/reconcile/` 的协调框架——都是 DR 框架可以直接使用或扩展的基础设施。
- **Postgres 用户有天然的 DR 期望**：选择 Postgres 作为元数据库的用户通常已经在使用流复制。AeroVault 需要与之集成而非独立存在。
- **渐进式实现**：可以先做定时备份 + 手动恢复（MVP），再做元数据复制（准实时），最后做自动故障转移。

---

## 5. 🟠 公平调度与多租户资源隔离（Weighted Fair Queuing）

### 为什么需要它

当前并发控制机制：

```go
// internal/middleware/concurrency.go
type ConcurrencyLimiter struct {
    maxInFlight int64               // 全局上限
    perTenantMax int64              // 可选：每租户上限（均分）
}

// internal/middleware/ratelimit.go
type RateLimiter struct {
    rps   int                       // 固定速率
    burst int                       // 固定突发
}
```

这意味着：
- **没有加权**：黄金租户和免费租户获得相同的并发配额
- **没有预留**：不能保证黄金租户在任何情况下都有至少 N 个并发槽位
- **没有优先级**：管理员的 `DELETE /buckets` 请求可能被普通用户的批量 GET 阻塞
- **没有公平调度**：一个活跃租户的请求消耗了所有可用连接，其他租户的请求被完全阻塞
- **没有自适应限流**：速率限制是静态配置的，不会根据后端健康状态动态调整

**代码锚点：**

| 文件 | 当前状态 | 缺失能力 |
|------|---------|---------|
| `internal/middleware/concurrency.go` | 全局信号量 + 可选 PerTenantMax | 无权重、无优先级、无队列 |
| `internal/middleware/ratelimit.go` | 每租户 Token-Bucket | 无多级速率、无自适应调整 |
| `internal/api/rest/router.go` | AI 路由组独立限流 | 无方法级/ bucket 级限流 |
| `internal/service/file_crud.go` | 全部方法直通 | 无内部背压机制（非 HTTP 场景） |

### 缺失能力

- **租户权重（Weight）**：每个租户可配置权重（如 `gold=10`, `silver=5`, `free=1`），总并发槽位按权重比例分配
- **预留容量（Reserved Capacity）**：每个租户可设置最小保证并发数（`min_connections`），即使其他租户繁忙也能保障
- **突发配额（Burst Allowance）**：超出配额的请求可进入等待队列（而非立即 429），队列长度和超时可配置
- **优先级队列**：不同请求方法/路径/租户等级有不同优先级，高优先级请求跳过等待队列
- **自适应速率**：RateLimiter 根据后端延迟 P99 自动降级/恢复 RPS（类似 TCP 拥塞控制）
- **API 粒度限流**：支持 `PUT` 大文件（高成本）与 `HEAD`（低成本）分别限流

### 边界情况

| 场景 | 风险 | 处理策略 |
|------|------|---------|
| 预留容量总和超过全局上限 | 过度承诺 | 拒绝新租户的预留配置 + 告警 |
| 空闲租户预留的容量被浪费 | 资源利用率下降 | 支持"借用"机制：未使用的预留可被其他租户临时使用 |
| 等待队列过长导致内存压力 | OOM | 绑定等待队列长度，超限时按优先级顺序拒绝请求 |
| 高优先级请求淹没低优先级 | 饥饿 | 每个优先级设置最小带宽保证 + 防老化（Aging）机制 |
| 自适应算法与静态配置冲突 | 不可预测行为 | 自适应给出建议范围，运维可配置上下限硬边界 |

### 架构概要

```
                        ┌──────────────────────────┐
    Request              │  WeightedFairScheduler     │
    ──────────────────▶  │  (新: middleware/wfs.go)   │
                         │                            │
                         │  Pod 级别分类:              │
                         │   1. 解析租户 + 方法 + 路径 │
                         │   2. 查找租户权重配置        │
                         │   3. 进入对应优先级队列     │
                         │   4. WFQ 调度出队           │
                         │      (Deficit Round Robin)  │
                         └──────────┬─────────────────┘
                                    │ 出队
                                    ▼
                         ┌──────────────────────────┐
                         │  AdaptiveRateLimiter       │
                         │  (增强 internal/middleware/ │
                         │   ratelimit.go)            │
                         │                            │
                         │  动态 RPS: baseRPS ×       │
                         │    backend_health_factor   │
                         │    (latency_p99 / error%)  │
                         └──────────┬─────────────────┘
                                    │ 通过
                                    ▼
                         ┌──────────────────────────┐
                         │  ConcurrencyLimiter        │
                         │  (增强)                    │
                         │                            │
                         │  加权信号量:                │
                         │  PUT=5权重, GET=1权重       │
                         │  每租户权重池               │
                         └──────────┬─────────────────┘
                                    │ 执行
                                    ▼
                         FileService / Storage
```

**关键设计决策：**
- WFQ 调度器是无状态、O(1) 入队/出队的实现（复用 Go `container/heap`）
- 租户权重配置存储在 `tenant_config` 表中（支持 API 动态修改 + GitOps 方向 1 管理）
- 调度器与 Handler 分离，可作为独立中间件插入现有中间件链

### 实现理由

- **多租户 SaaS 差异化核心**：没有公平调度，就不能向不同级别的客户承诺 SLA。这是"Demo"和"生产"的分水岭。
- **复用中间件链位置**：当前中间件链是 `RequestID→CORS→Auth→Tenant→RateLimit→OTel→Recoverer→AccessLog`，WFQ 调度器可放置在 `RateLimit` 之前或之后（或合二为一）。
- **溢出当前限流的必要演进**：Token-Bucket 只控制速率，不控制并发；全局信号量不感知租户。WFQ 填补了两者之间的空白。
- **与 AI 专线限流一致**：AI 路由组已经有独立的 `aiRL`，可以自然地继承 WFQ 的租户优先级——高价值租户的 Chat 请求享受更高权重。

---

## 总结：方向优先级建议

| 优先级 | 方向 | 建议阶段 | 理由 |
|--------|------|---------|------|
| P0 | **方向 1：GitOps Controller** | 下一轮迭代 | 影响所有其他方向的配置管理；复用率高（60%+）；企业 IaC 硬门槛 |
| P0 | **方向 2：Serverless Function Triggers** | 下一轮迭代 | 产品差异化核心能力；复用事件总线基础设施；AI 工作流的天然延伸 |
| P1 | **方向 4：统一 DR 框架** | 架构基础设施 | 影响 RPO/RTO SLA；需要与复制/快照协调推进；Postgres 用户期望 |
| P1 | **方向 5：公平调度 WFQ** | 多租户 SaaS 就绪依赖 | 面向外部客户前必须解决；可与 RateLimiter 渐进式替换 |
| P2 | **方向 3：元数据 Schema 治理** | 合规加速器 | 价值明确但侵入性较高（影响写入路径）；可作为 v2 合规包 |

---

> **不编写任何实现代码。** 本文档基于对 `internal/` 全部子包、`cmd/server/main.go`、配置系统、SDK 三套、24 对迁移文件、部署配置的全局代码扫描，及对前 19 期 expansion 文档、ROADMAP、analysis-gaps-roadmap 的完整比对，确认每个方向在既有文档中零覆盖。所有方向均为**当前代码库中存在的结构性缺失**，而非"锦上添花"的功能点。
