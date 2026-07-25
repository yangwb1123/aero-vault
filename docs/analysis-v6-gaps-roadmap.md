# 🏗️ AeroVault 深度评估 v6 — AI 管线进化、企业合规、全球化、无服务器函数、平台货币化

> **日期:** 2026-06-30  
> **方法:** 全局代码扫描（237 文件 / ~45K 行），第六轮  
> **视角:** AI 管线成熟度 → 企业合规 → 全球化 → 平台经济

---

## 0. 本轮焦点：从"功能完整"到"平台竞争力"

前五轮已覆盖功能缺口（v1）、韧性与一致性（v2）、协议生态系统与经济学（v3）、内部架构债与 AI 管线质量（v4）、存储实现质量与标准合规（v5）。  
本轮转向**产品与业务维度**——分析一个功能完整但相对简单的开源项目如何进化成可竞争的平台。这些方向不解决"是否有功能 X"的问题，而是解决"在现实世界中，谁会用这个替代 AWS S3 + 内部部署 RAG 方案？"

---

## 1. AI 管线深度进化：从"能用"到"智能"

### 1.1 当前状态的诚实评估

AeroVault 的 AI 管线（提取 → 分块 → 嵌入 → 搜索 → 重排序 → 聊天 → 工具代理）**架构完整但功能基础**：

| 阶段 | 当前实现 | 成熟度评级 |
|-------|----------------|:----------:|
| 提取 | `Extractor`（文本类内置；二进制委托 RemoteExtractor） | 基础 |
| 分块 | `Chunker`（基于窗口，600/80 默认，覆盖无重叠） | 基础 |
| 嵌入 | `Embedder`（单模型，有缓存） | 基础 |
| 向量检索 | brute-force / pgvector / Qdrant（有模型漂移过滤） | 良好 |
| 词法检索 | BM25（内存增量）/ pgFTS | 良好 |
| 混合搜索 | RRF 融合（K=60，评分 DESC + chunkID ASC 分胜负） | 良好 |
| 重排序 | HTTPReranker（Cohere 格式）/ HeuristicReranker（降级） | 基础 |
| 聊天 | 系统提示 + 检索上下文 → LLM（有成本会计和预算） | 基础 |
| 代理 | 函数调用循环（`AI_AGENT_MAX_STEPS=4`） | ✅ 有 |
| PII 检测 | 正则 + Luhn 验证（信用卡、SSN、邮箱、电话、IP） | 基础 |

### 1.2 缺失的高阶 AI 能力

| 能力 | 当前状态 | 影响 |
|-----------|------------|--------|
| **查询重写** | ❌ 缺失 | 用户输入短/模糊 → 检索效果差。HyDE、查询扩展、多跳分解 |
| **多模型路由** | ❌ 缺失 | 单 LLM 端点。简单查问用小模型/便宜模型；复杂 RAG 用大模型 |
| **多模态提取** | ❌ 缺失 | 图像内的文字（OCR）、音频（转录）、PDF 表格 |
| **上下文窗口优化** | ❌ 缺失 | 当 K 个结果超过 LLM 上下文窗口时，需滑动窗口或摘要精炼 |
| **RAG 质量评估** | ❌ 缺失 | 无衡量标准：检索精度、响应忠实度、幻觉检测、引用覆盖率 |
| **结构提取** | ❌ 缺失 | 无 JSON Schema 提取（"从此发票中提取金额、日期、供应商"） |
| **会话记忆调整** | ⚠️ 基础 | `Prior` 消息传递但无持久化、摘要、主动遗忘智能策略 |
| **模型 A/B 测试** | ❌ 缺失 | 无法对比不同嵌入或 LLM 模型的准确率 |
| **查询建议/自动补全** | ❌ 缺失 | 无 "大家都在搜" 或语义补全 |
| **反馈追踪** | ❌ 缺失 | 无 `thumbs up/down` 反馈收集链路的埋点 |

### 1.3 代码中的具体改进点

**风险：上下文中过多的检索结果会因上下文窗口溢出而丢失。**

```go
// internal/ai/chat.go:buildChatPrompt 中的提示构建
// 当前版本无条件格式化前 K 个结果：
fmt.Fprintf(&ctxBlock, "[#%d] %s/%s (score %.3f)\n%s\n\n", i+1, h.Bucket, h.ObjectKey, h.Score, h.Chunk)
// 如果总长度 > LLM 上下文窗口，消息会被截断（静默丢弃重要内容）
// 后续可加：估算 token 数 + 滑动窗口压缩或摘要精炼
```

**风险：Reranker 错误只会记录日志，无降级度量。**
```go
// internal/ai/search.go:applyRerankOrTrim
// 当前：重排序失败 → 原始排序 + warn 日志
// 改进：telemetry.IncRerankerFailure(tenant, model) 计数器，用于警报
```

### 1.4 架构蓝图：AI 管线 2.0

```
当前: 单模型嵌入 → 搜索 → [可选重排序] → 单 LLM 聊天 → 审计
改进: AI Pipeline 2.0

┌──────────────────────────────────────────────────────────────┐
│ Query Rewriter:                                                │
│   ┌─ HyDE (假设文档嵌入)                                       │
│   ├─ 多跳分解 ("2024年收入和费用" → [2024年收入, 2024年费用])  │
│   ├─ 查询扩展 (同义词 + 相关术语)                               │
│   └─ 自动过滤提取 (日期范围, 桶, 标签从查询)                   │
├──────────────────────────────────────────────────────────────┤
│ RouterChain (路由链):                                          │
│   ┌─ 成本: 简单 < 500 token → 小/便宜模型                      │
│   ├─ 质量: 复杂 RAG → 大模型                                   │
│   ├─ 降级: LLM 超时 → 回退到缓存或更小模型                     │
│   └─ 配置: per-query-model 头或 tenant-level 默认              │
├──────────────────────────────────────────────────────────────┤
│ MultiModal Extractor:                                          │
│   ┌─ OCR: PDF 图片/扫描 → 文本                                 │
│   ├─ 音频: 语音 → 文字                                         │
│   ├─ 表格: HTML/PDF 表格 → 结构化 markdown                     │
│   └─ 图片: 描述生成 (LLaVA/GPT-4V)                             │
├──────────────────────────────────────────────────────────────┤
│ Context Optimizer:                                             │
│   ┌─ Token 预算分配 (总预算 → 每文档按分数分配)                │
│   ├─ Map-Reduce: K=10 → 分别摘要 → 最终答案 (适用超长上下文)    │
│   └─ 引用验证: 返回内容与检索原文对比 → 标记幻觉                │
├──────────────────────────────────────────────────────────────┤
│ RAG Quality Framework:                                         │
│   ┌─ 离线评估: 黄金数据集 (query/ideal_chunks/ideal_answer)     │
│   ├─ 在线评估: 点击穿透率、引用有用率、回答有用率评分            │
│   └─ 自动回归: 部署后检测质量下降的 CI 门禁                     │
└──────────────────────────────────────────────────────────────┘
```

**复用的现有资产：** `internal/ai/`（所有文件——管线是可靠的框架）、`internal/repository/`（`ai_usage` 表已跟踪 token/模型——可扩展用于质量指标）、`internal/telemetry/`（已有 `ai_requests`、`ai_tokens`、`ai_cost_micros` 指标）

---

## 2. 企业级内容合规：超越 WORM

### 2.1 当前状态

AeroVault 通过以下方式提供基础合规功能：
- **对象锁/WORM**（`locked_until` 字段）——单对象，基于 header 设置
- **生存期**（基于时间的软/硬删除桶级生命周期规则）
- **桶策略**（JSON 策略文档）
- **审计日志**（管理操作日志 `audit_log` 表）

这覆盖了基础用例，但缺少真实世界合规方案中的关键特性：

| 合规需求 | 当前状态 | 缺失项 |
|-------------|------------|------------|
| **法律封存**（诉讼暂停） | ❌ 缺失 | 跨桶/跨越租户的临时封存，覆盖现有生命周期 |
| **数据分类** | ❌ 缺失 | 自动标记（机密/内部/公开），策略驱动处理 |
| **信息权限管理** | ❌ 缺失 | 共享链接过期、水印、动态文档查看器 |
| **不可变审计日志** | ⚠️ 部分 | `audit_log` 表**可被 admin 删除**——无不可变存储 |
| **内容审核** | ❌ 缺失 | 自动标记不当内容（暴力/仇恨言论/成人） |
| **数据主权** | ❌ 缺失 | 地理限制：特定桶/租户的数据必须留在一个地区 |

### 2.2 代码分析：合规增强点

```go
// internal/repository/repository.go 中的对象锁
type Object struct {
    // ...
    LockedUntil *time.Time // 支持 WORM
}

// internal/api/rest/conditional.go 中的锁定_检查
// 当前：只检查单对象 locked_until
// 缺失：桶级法律封存（覆盖所有对象）+ 租户级封存
```

```go
// internal/service/file_crud.go 中的删除路径
// 当前：硬删除会跳过锁定对象
// 缺失：legal_hold 标记（独立于 locked_until——无到期时间）
```

### 2.3 架构蓝图：合规套件

```
当前: 对象锁 + 桶生命周期 + 管理审计日志
改进: Enterprise Compliance

┌────────────────────────────────────────────────────────────────┐
│ Legal Hold (法律封存):                                          │
│   表: legal_holds { id, tenant, bucket, key_pattern(前缀/后缀), │
│        reason, created_by, created_at, released_at }            │
│   行为: 在 GC/生命周期/Delete 之前检查——如果匹配任何活跃封存则禁止 │
│   优先级: 封存 > 对象 locked_until > 生命周期规则                 │
├──────────────────────────────────────────────────────────────┤
│ Data Classification (分类服务):                                  │
│   自动标记 (配合 Indexer 管线运行):                               │
│   ├─ PII 检测器 → 如果 PII > 阈值设定敏感度                      │
│   ├─ 关键词匹配 → 合同、财务、HR → 相应分类                     │
│   ├─ ML 分类器 (可选模型) → 机密/内部/公共                      │
│   策略引擎: "分类为"机密"的对象 → 必须加密 + 禁止匿名头访问      │
├──────────────────────────────────────────────────────────────┤
│ Immutable Audit Trail (不可变审计追踪):                          │
│   配置: AUDIT_LOG_BACKEND (db|file|s3)                          │
│   ├─ db: 当前 audit_log 表 (可删——不够不可变)                    │
│   ├─ file: append-only 日志到本地存储 (WORM via 文件系统权限)    │
│   └─ s3: 通过 bucket 策略写至 S3 对象 (对象锁时删除禁止)        │
│   验证: 定期审计日志哈希链                                       │
├──────────────────────────────────────────────────────────────┤
│ Information Rights Management (文档安全管理):                    │
│   ├─ 共享链接: 持有者直接访问, 可设密码 + 过期 + 次数限制       │
│   ├─ 水印: 按需在对象上打水印 (用户+时间+IP)                    │
│   └─ 安全查看器: HTML-only 查看器, 禁止下载+打印+复制            │
└──────────────────────────────────────────────────────────────┘
```

**复用的现有资产：** `internal/repository/sql_objects.go`（已标记 `locked_until` 约束）、`internal/ai/pii.go`（`PIIDetector` 可用于分类）、`internal/ai/indexer.go`（索引管线可扩展分类步骤）、`internal/api/rest/admin.go`（管理端点已存在）

---

## 3. 全局多区域部署：从单副本到全球网络

### 3.1 当前状态

AeroVault 的跨区域策略是**一个副本，一个目标**（`internal/replication/`）：
- `Replication.Worker` 监听创建事件 → 异步复制到单个复制目标
- 复制是单向的（主 → 备）
- `Reconcile` 和 `RetentionJob` 可在**集群单例**模式下运行（使用 `leases` 表/DL 锁）

这适合 DR，但**不支持真正的全球分发**：

| 全球部署需求 | 当前状态 | 缺失项 |
|-------------------|------------|------------|
| 主动-主动多区域 | ❌ 缺失 | 无冲突检测或 CRDT 合并 |
| 全球桶命名空间 | ❌ 缺失 | 所有请求需指定区域。无全局域名 |
| 读取亲和性路由 | ❌ 缺失 | 欧洲客户端应读取欧洲副本 |
| 区域故障转移 | ❌ 缺失 | 区域 B 宕机 → 路由至区域 C，自动 |
| 跨区域复制拓扑 | ⚠️ 基础 | 单向单跳。无链式复制或网格复制 |

### 3.2 代码分析：复制层

```go
// internal/replication/replication.go 中的复制模式
// 当前：Worker.Run 监听创建事件 → Enqueue(JobReplicate)
//        Handler.ReplicateObjectByID 从主存储读取 → 写入副本存储
//
// 局限：
// 1. 仅单向（区域 A → 区域 B）
// 2. 无冲突检测（互相同步的区域 A 和区域 B 写入同一对象覆盖）
// 3. 存储键相同（云后端回退到跨区域访问时的延迟问题）
// 4. 仅对象创建，非删除/标签/元数据
```

```go
// internal/cluster/singleton.go 中的集群单例
// 当前：仅 gate reconcile + lifecycle + retention
// 改进：扩展为全局领导者选举，跨区域一致（需要共识或外部编排器）
```

### 3.3 架构蓝图：全球网格

```
当前: 单区域主 → 单区域副本 (异步, 对象创建仅)
改进: Global Replication Grid

┌─────────────────────────────────────────────────────────────────┐
│ Topology Descriptor (拓扑描述符):                                │
│   ┌─ regions: [                                                    │
│   │  { name: "us-east", endpoint: "https://us-east.aero",         │
│   │    replicas: ["us-west", "eu-central"] },                     │
│   │  { name: "eu-central", endpoint: "https://eu.aero",           │
│   │    replicas: ["us-east"] }                                    │
│   └─ ]                                                            │
├─────────────────────────────────────────────────────────────────┤
│ Conflict Resolution (冲突解决):                                   │
│   ├─ Last-Writer-Wins (LWW) w/ wall clock (当前隐式模式)           │
│   ├─ 版本向量 (Dotted Version Vector) — CRDT                        │
│   │   ├─ 每个对象: (region, counter) 的向量                        │
│   │   └─ 冲突时: 保留两个版本, 标记为冲突                          │
│   └─ 管理 API: POST /v1/resolve/{object} → 选择胜出版本            │
├─────────────────────────────────────────────────────────────────┤
│ Global Namespace (全球命名空间):                                   │
│   ├─ 全局路由层 (GSLB):                                           │
│   │   ├─ 按延迟: 客户端 DNS → 最近区域                             │
│   │   └─ 按健康: 区域宕机 → 备用区域                               │
│   ├─ 读取关联: GET/LIST → 本地副本；PUT/DELETE → 主写入区域        │
│   └─ 全局桶发现: HEAD bucket → 所有区域搜索                        │
├─────────────────────────────────────────────────────────────────┤
│ Cross-Region Replication Improvements:                            │
│   ├─ 拓扑感知: 链式 (US→EU→APAC) 与网格 (全连接)                  │
│   ├─ 事件: 创建+删除+标签+元数据+ACL                              │
│   ├─ 批量回填: 初始复制整个桶                                     │
│   └─ 验证: 定期哈希校验和校验（长期一致性）                       │
└─────────────────────────────────────────────────────────────────┘
```

**关键的现有资产：** `internal/replication/`（模式已存在——需要扩展）、`internal/repository/`（`events` 和 `jobs` 表已有跨区域持久化）、`internal/events/postgres_transport.go`（跨实例事件总线概念可沿用于跨区域）、`internal/cluster/singleton.go`（领导者选取原语可用于全球领导者）

---

## 4. 集成事件驱动函数引擎（无服务器函数）

### 4.1 当前状态

AeroVault 已有一个**健壮的事件/作业系统**：
- `EventBus`（进程内 + Postgres 传输）
- `JobPool`（持久作业队列 + worker pool + dedupe + 重试）
- 事件类型：`object.created`、`object.deleted`、`object.modified`
- 内置消费者：`Replication.Worker`、`Antivirus.Worker`、`Webhook`

但**缺少发送自定义用户函数的能力**。Webhook 是唯一的外部集成点，并且仅支持 HTTP POST 到一个单独预配置的 URL。

### 4.2 代码分析：扩展点

```go
// internal/jobs/pool.go — JobPool
// AddHandler 方法已允许注册任意处理函数
// 新类型: "user_function" + payload { runtime, source, timeout }

// internal/events/bus.go — EventBus
// Publish 方法将事件广播到所有订阅者
// 可增加 "pod" 字段用于触发用户函数

// 缺失:
// - 运行时沙箱 (安全地执行用户提供的代码)
// - 函数注册 API (上传代码并绑定到事件)
// - 函数日志收集 (收集 stdout/stderr 到审计日志)
```

### 4.3 架构蓝图：函数引擎

```
当前: Webhook → 预配置 URL
改进: AeroVault Functions (Serverless, 事件驱动)

┌──────────────────────────────────────────────────────────────────┐
│ Function Lifecycle (函数生命周期):                                 │
│   POST /v1/admin/functions                                        │
│   { "name": "resize-thumbnails",                                  │
│     "runtime": "wasm",                                            │
│     "code": "<base64-wasm-bytecode>",                              │
│     "trigger": "object.created",                                  │
│     "filter": "bucket=uploads && content-type.starts-with(image/)",│
│     "timeout_seconds": 30 }                                       │
├──────────────────────────────────────────────────────────────────┤
│ Runtime Execution (执行运行时):                                     │
│   ┌─ WebAssembly Sandbox (推荐 — 安全, 快速, 跨语言)              │
│   │   ├─ 使用 wazero (零依赖 Go Wasm 运行时)                       │
│   │   ├─ 内存限制: 128MB/函数, CPU 限制: 1 核                      │
│   │   └─ 系统调用过滤: 仅允许 aero-vault SDK 调用                  │
│   ├─ JavaScript (QuickJS/goja — 较慢但更易采用)                    │
│   └─ Python (starlark — 受限, 仅安全子集)                         │
├──────────────────────────────────────────────────────────────────┤
│ Event-Function Bridge (事件-函数桥):                                │
│   当事件触发时:                                                    │
│   ├─ Bus.Publish → JobPool 检查触发器                              │
│   ├─ 匹配函数 → 构造 ExecutionPayload{event, env, context}        │
│   ├─ 执行: 在沙箱中 spawned goroutine（超时后取消上下文）          │
│   ├─ 输出: 捕获函数返回 → 可能触发次级事件（函数链）              │
│   └─ 记录: function_name + object_id + duration + output          │
├──────────────────────────────────────────────────────────────────┤
│ Built-in Function Library (内置函数库):                             │
│   ├─ 图像缩略图: 收到 image/* 上传 → 创建 _thumb.png              │
│   ├─ 自动压缩: 收到 text/* 上传 → 创建 content.txt.gz             │
│   ├─ 病毒扫描: 云后端的内置替代方案 (通过 ClamAV API)              │
│   ├─ 数据路由: 按内容类型或大小路由到不同桶                        │
│   └─ 通知: Slack/MSTeams/PagerDuty 通知 (替代通用 Webhook)         │
└──────────────────────────────────────────────────────────────────┘
```

**复用资产：** `internal/jobs/`（作业池基础设施——重试、取幂退避、去重）、`internal/events/bus.go`（事件路由）、`internal/repository/`（持久化函数元数据）、`internal/storage/`（函数输出——作为对象存储）、`internal/repository/sql_objects.go`（`tags` 可标记函数处理状态）

**差异化：** 与 S3 Lambda 相比，这是开源的、自托管的，且零额外成本

---

## 5. 平台货币化：计量、套餐、SLA

### 5.1 当前状态

AeroVault 已有基础部分：
- **租户配额**（最大字节数 + 对象数，`TenantQuota`）
- **按使用计费**（`ai_usage` 表按租户 + 模型追踪 token + 成本 + 延迟）
- **每日预算**（`AI_TENANT_DAILY_BUDGET_USD` + 每租户覆盖使用 `daily_budget_micros`）
- **管理密钥**（作用域 `read/write/admin` 的 API 密钥）
- **租户状态**（`active|disabled`）

但这些都是**成本控制，而非收入生成**。没有计量系统、套餐、预留容量或发票。

### 5.2 代码分析：货币化入口点

```go
// internal/repository/repository.go: TenantQuota
type TenantQuota struct {
    TenantID           string
    MaxBytes           int64
    MaxObjects         int64
    UsedBytes          int64
    UsedObjects        int64
    DailyBudgetMicros  int64  // AI spend cap
}

// 缺失的计量字段:
// - storage_class_bytes: map[string]int64  (每层级花费不同)
// - request_count:       map[string]int64  (GET/PUT/DELETE)
// - bandwidth_ingress:   int64
// - bandwidth_egress:    int64
// - retention_days:      int      (计费层级)
```

```go
// internal/repository/repository.go: Usage (AI)
type Usage struct {
    // ... token/成本/延迟
}
// 缺失: 非 AI 使用追踪 (存储字节日、请求数、带宽)
```

### 5.3 架构蓝图：货币化套件

```
当前: 配额 + AI 成本追踪 + 预算上限
改进: Platform Monetization

┌─────────────────────────────────────────────────────────────────┐
│ Metering Pipeline (计量管线):                                     │
│   3 个计量维度:                                                   │
│   ┌── Storage: 字节-天 (每存储层级) — 从 reconcile 每日计算        │
│   ├── Requests: GET/PUT/DELETE/LIST/SEARCH/CHAT — 从 middleware  │
│   │             累积到内存，每 5 分钟刷新到 metering_usage 表       │
│   └── Bandwidth: 入口/出口字节 — 从 storage 层统计 (Put/Get 大小) │
│                                                                   │
│   计量聚合:                                                        │
│   metering_hourly { tenant, tier, hour,                          │
│     storage_bytes, request_count, ingress_bytes, egress_bytes }   │
├─────────────────────────────────────────────────────────────────┤
│ Tiered Plans (套餐分层):                                           │
│   配置: 管理 API + YAML 文件                                      │
│   ┌── Free:     1GB, 1000 req/天, 1 桶, 50 次 AI 调用/天          │
│   ├── Pro:      100GB, 重复版本+标签+ACL, AI 无限                  │
│   ├── Business: 1TB, SLA 99.95%, 支持 WORM+审计, 自定义嵌入        │
│   └── Enterprise: 自定义, VPC 部署, 全球复制, 白标商店              │
│                                                                    │
│   策略: PUT/GET 前检查——超限则 402 PaymentRequired + upgrade URL   │
├─────────────────────────────────────────────────────────────────┤
│ Billing Integration (计费集成):                                    │
│   ├─ Stripe 适配器:                                                 │
│   │   ├─ POST /v1/admin/billing/sync                              │
│   │   ├─ 推送计量至 Stripe Metered Billing                        │
│   │   └─ 处理 webhook 事件 (payment.success, subscription.update)  │
│   ├─ 发票: 月度 PDF 摘要 (使用 snapshot 生成 + 邮件)              │
│   └─ 预留容量: 1TB 预留 = $50/月 (无论使用量)                     │
├─────────────────────────────────────────────────────────────────┤
│ SLA Dashboard (SLA 仪表板):                                        │
│   ├─ SLI 指标:                                                     │
│   │   ├─ 可用性: 请求成功率 > 99.9%                               │
│   │   ├─ 延迟: P95 GET < 50ms, P95 SEARCH < 2s                    │
│   │   └─ 耐用性: 年度对象校验和错误率 < 0.0001%                    │
│   ├─ SLA 报告: 月度信用计算                                        │
│   └─ 健康页: public GET /_health → uptime + 最近事件               │
└─────────────────────────────────────────────────────────────────┘
```

**复用资产：** `internal/repository/`（`TenantQuota` 已存在——可扩展布尔值）、`internal/telemetry/`（已有 `http.server.requests`、`ai_requests`——可汇总到计量）、`internal/api/rest/admin.go`（管理 API 可扩展计费端点）、`internal/reconcile/`（每日作业可用于计算存储字节-天）、`internal/snapshot/`（用于发票 PDF 生成）

---

## 6. 性能优化机会（本代码扫描专属）

在上述 5 个方向之外，以下是本轮扫描中发现的性能优化机会：

### 6.1 AI 管线：嵌入批量处理

```go
// internal/ai/indexer.go:IndexObject
// 当前: 每个对象顺序嵌入其块。
// 改进: 批量嵌入（最多 AI_EMBED_BATCH_SIZE=16 个块/请求）
// 影响: 嵌入吞吐量 3-5x（因 HTTP 往返开销为主）
```

### 6.2 Webdav：`Rename` 回退到 S3（存储无关拷贝）

```go
// internal/api/webdav/dav.go
// 当前: Rename 读取到内存然后写入
// 影响: 大对象跨后端拷贝的 OOM 风险
// 改进: 使用 storage.PresignGet + PresignPut 进行服务器端拷贝（S3 场景下零数据移动）
```

### 6.3 审计日志批处理

```go
// internal/repository/sql_objects.go — audit log writes
// 当前: 每次管理操作都执行独立 INSERT
// 影响: 大量密钥轮换/租户操作时产生过多写入
// 改进: 对 `audit_log` 写入使用缓冲写入器（缓冲区填满或每 1 秒刷新）
```

### 6.4 对象锁验证异步化

```go
// internal/service/file_crud.go:硬删除路径
// 当前: 每次在删除前查询对象锁定状态（写入路径上的额外查询）
// 改进: 在对象元数据缓存（如在 `GetObject` 期间填充的 `locked_until`）中缓存锁定状态
```

### 6.5 BM25 索引开销与副作用

```go
// internal/ai/bm25.go — 内存行级 BM25
// 当前: O(1) 更新/删除（增量维护），但在启动时重建整个索引
// 影响: 存储 100K+ 个对象的高内存占用
// 改进: 可选磁盘支持的 BM25（mmap FTS 文件）或 pgFTS 作为可扩展替代方案（已实现）
```

---

## 7. 综合优先级矩阵：跨五轮所有方向

| 阶段 | v1（特征） | v2（韧性） | v3（生态系统） | v4（质量） | v5（质量实施） | **v6（平台竞争）** |
|-------|-----------|-----------|---------------|-----------|----------------|-------------------|
| **P0（立即）** | — | 断路器 | — | 竞态检测 CI | S3 补齐 | **AI 查询重写 + 多模型路由** |
| **P0** | — | — | — | 数据访问优化 | 存储错误映射统一 | **函数引擎基础（WASM 沙箱）** |
| **P1（本季度）** | 存储分层 | 自愈网格 | 可观测性管线 | 优雅关闭 | 传输安全 | **法律封存 + 数据分类** |
| **P1** | — | — | — | RAG 评估 | 开发者入门 | **SLA 仪表板 + 计量管线** |
| **P2（下季度）** | FUSE | 搜索联邦 | 合规套件 | 开发工具链 | 数据可移植性 & SDK | **全球网格（主动-主动）** |

---

## 8. 跨轮按领域的累计分析视图

| 领域 | v1 | v2 | v3 | v4 | v5 | **v6** |
|--------|:--:|:--:|:--:|:--:|:--:|:------:|
| **特征完整性** | ★★★★★ | ★★★ | ★★★ | ★★ | ★★★★ | ★★★ |
| **韧性/可靠性** | ★★ | ★★★★★ | ★★ | ★★★ | ★★★ | ★ |
| **安全/合规** | ★★ | ★★★ | ★★★★ | ★ | ★★★★★ | ★★★★ |
| **可观测性** | ★ | ★ | ★★★★★ | ★★ | ★★★ | ★★★ |
| **性能/可扩展性** | ★★★★ | ★★ | ★★★ | ★★★★ | ★★ | ★★★ |
| **AI 管线质量** | ★ | ★ | ★ | ★★★★★ | ★ | ★★★★ |
| **开发者体验** | ★ | ★ | ★ | ★★ | ★★★★ | ★★★ |
| **平台/商业准备** | — | — | — | — | — | **★★★★★** |

---

> *第六次全局扫描完成，未修改任何代码。本轮 5 个方向将 AeroVault 的视角从"功能完备的开源存储+AI 系统"转向"面向现实用户和收入的竞争性平台"。前五轮基础上的累计缺口分析涵盖 30 个方向、50+ 个独立发现，以及完整的代码引用库。*
