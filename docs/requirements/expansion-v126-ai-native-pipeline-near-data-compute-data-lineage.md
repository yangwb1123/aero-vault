# 高价值扩展方向：AI 原生智能管线断层、近数据计算范式与数据血缘基础设施

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — 237+ Go 源文件、50 对迁移文件、3 套 SDK（Go/Python/JS）、MCP 双模式（HTTP+stdio）、Web UI、Helm Chart、Grafana/Prometheus/OTel 配置。逐包审阅 `internal/` 全部子包、`cmd/server/main.go`、`internal/webui/`、`sdk/`。  
> **核心原则：** 不编写任何代码。选取**代码中存在明确锚点但管线断裂、在前 114 轮分析中零实质性覆盖、且有高产品/架构影响**的方向。  
> **日期：** 2026-07-11  

---

## 去重验证：前 114 轮分析覆盖矩阵摘要

前 114 期已从 50+ 分析视角覆盖约 600 个方向。以下方向已被充分覆盖，**本文不再重复**：

| 领域 | 覆盖期数覆盖方向数 |
|------|-------------------|
| AI/RAG 管线（Embed/Search/Chat/Agent/Rerank/PII/Indexer/Cache/Lineage） | 15+ |
| S3 协议完备性（子资源/Batch/Multipart/ACL/Policy/CORS/Logging/Notification/VirtualHosted/Checksum） | 14+ |
| 存储后端（S3/OSS/COS/KMS/SSE/CircuitBreaker/Multi-Backend Routing/Server-Side Copy） | 12+ |
| 认证授权（JWT/API Key/SigV4/OIDC/SAML/MFA/Policy Engine/Key Isolation/MCP Auth/SSO） | 10+ |
| 多租户（CRUD/Quota/Budget/Audit/Governance/Isolation/Self-Service API） | 10+ |
| 事件/通知/Webhook/SSE/Transport/Routing/Filter/Multi-Destination/CircuitBreaker | 12+ |
| 复制/高可用/集群（CRR/SRR/HA/Active-Active/Federation/Cluster Singleton/Metadata DR） | 10+ |
| 存储分层/生命周期转换/冷热数据/Glacier/Restore/NoncurrentVersion | 8+ |
| 合规/WORM/Legal Hold/Retention/Access Log/MFA Delete/Object Lock Modes | 8+ |
| 可观测性（OTel/Metrics/Grafana/Prometheus/SLO/Tracing/pprof/Cost Attribution） | 6+ |
| 工程质量（并发安全/内存管理/优雅关闭/流式路径/崩溃恢复/合约测试） | 10+ |
| Web UI / Admin Console 生产化/跨租户面板/内容预览 | 7+ |
| 基础设施（TLS/Feature Flag/Config Declarative/Zero-Downtime/Health/Chaos） | 8+ |

---

## 本文五大方向总览

| # | 方向 | 类型 | 核心痛点 | 代码锚点 | 既有覆盖 |
|---|------|------|---------|----------|---------|
| **1** | **🔴 自适应分块策略引擎** —— 从固定窗口到语义感知、动态优化的 Chunking 管线 | AI 质量/架构 | 当前 `Chunker` 是固定滑动窗口（Window/Overlap），不感知文档语义边界、不感知内容类型、不学习用户查询模式；分块质量是 RAG 召回率的根本瓶颈，却完全不可配置 | `internal/ai/chunker.go`（`Chunk` 结构体仅 `Window`/`Overlap`）、`extractor.go:Extract`（返回整个字符串 → chunker 无法利用结构信息）、`indexer.go:handleExtractError`（不可分块类型静默跳过，无降级策略） | **零覆盖** — 全库搜索 `adaptive.*chunk\|semantic.*chunk\|chunk.*boundary\|learn.*chunk\|smart.*chunk\|chunk.*optim` → 命中 0 次 |
| **2** | **🔴 近数据计算与 Serverless Function 触发器** —— 从事件通知到可编程数据变换 | 架构/产品 | 事件总线仅做传递式通知（Webhook/SSE/Replication），无用户可编程的处理逻辑；`NotificationRule.LambdaARN` 标记为 `unused, kept for compat`；所有内容变换（缩略图、格式转换、AI 提取）均为内置硬编码，无第三方扩展路径 | `internal/events/bus.go`（`Publish` 方法仅广播事件，无触发器引擎）、`internal/repository/repository.go:NotificationRule.LambdaARN`（`unused, kept for compat`）、`internal/thumbnail/thumbnail.go`（唯一内置变换器，硬编码 JPEG/PNG）、`internal/jobs/jobs.go`（Job 池支持自定义 handler，但无用户注册机制） | **零覆盖** — 全库搜索 `lambda.*trigger\|function.*compute\|serverless\|near.data.*compute\|user.defined.*function\|wasm.*sandbox\|plugin.*exec` → 仅 v13 方向五 1 次概念提及，无代码分析 |
| **3** | **🟠 多模型路由网关** —— 从单 LLM/Embedder 到租户/场景感知的模型选择层 | AI 架构/成本 | `Embedder`/`LLM`/`Reranker` 各为单一实例，全局共享。不同租户、不同使用场景（摘要 vs 搜索 vs 聊天）、不同成本预算无法路由到不同模型。无 fallback 链、无 A/B 实验框架、无成本感知的模型选择 | `cmd/server/main.go:buildEmbedder`（单一 `ai.Embedder`）、`buildLLM`（单一 `ai.LLM`）、`setupChatAndAgent`（单一 chat+agent）、`internal/ai/embedder.go:Embedder` 接口（无路由/选择器抽象）、`internal/ai/search.go:Search`（内嵌单一 embedder）、`internal/config/config_ai.go:AIConfig`（单一模型配置字段，无 model registry）| **零覆盖** — 全库搜索 `multi.*llm\|llm.*rout\|model.*selector\|model.*gateway\|tenant.*model.*map\|fallback.*chain\|A.B.*test.*model` → 仅 v78 方向四 1 次概念提及，无代码分析 |
| **4** | **🟠 数据血缘与溯源图基础设施** —— 从单对象血缘到完整数据谱系 | 合规/AI 治理 | `GET /v1/lineage/objects/{id}` 返回原始的使用记录（谁查了哪些 chunk），但无变换血缘、无派生关系、无数据溯源图。对于 AI 训练数据溯源、合规审计、数据质量回溯场景完全不足以支撑 | `internal/api/rest/search.go:Lineage`（仅查询 `repo.ListUsageForObject`）、`internal/repository/repository.go:Usage`（仅记录 `Query`/`ChunkIDs`/`ObjectIDs`/`CostMicros` 等使用指标）、`internal/repository/repository.go:Object`（无 `DerivedFrom`、`Transformation`、`Provenance` 字段）| **零覆盖** — 全库搜索 `data.*provenance\|provenance.*graph\|derived.*from\|transform.*lineage\|data.*pedigree\|溯源\|谱系` → 仅 v57 方向二 1 次概念性讨论，无代码分析 |
| **5** | **🟡 近似重复检测与内容指纹引擎** —— 从字节级去重到语义级去重 | 成本/数据质量 | 当前存储去重不存在，相同内容重复上传消耗 N 倍空间。内容级重复检测功能完全缺失——无法识别微小变体文档、近似图片、重写内容。搜索系统会返回 N 条近似相同的结果，降低检索质量 | `internal/storage/storage.go:Storage` 接口（无 `ContentHash`/`PutIfAbsent` 方法）、`internal/service/file_crud.go:Put`（始终写入新 blob，零去重检查）、`internal/repository/repository.go:Object`（无 `ContentHash`/`RefCount` 字段）、`internal/ai/search.go:Query`（返回结果无去重逻辑，近似 chunk 全部返回）| **零覆盖** — 全库搜索 `near.*duplic\|fuzzy.*duplic\|approxim.*duplic\|content.*fingerprint\|simhash\|minhash\|perceptual.*hash\|semantic.*dedup\|近似.*重复\|模糊.*去重` → 命中 0 次 |

---

## 方向一：自适应分块策略引擎 —— 从固定窗口到语义感知、动态优化的 Chunking 管线

### 为什么需要它

分块（Chunking）是 RAG 管线的质量和成本起点。当前 `Chunker` 是一个固定滑动窗口：

```go
// internal/ai/chunker.go
type Chunker struct {
    Window  int `config:"AI_CHUNK_WINDOW"`  // 默认 600 字符
    Overlap int `config:"AI_CHUNK_OVERLAP"` // 默认 80 字符
}

func (c *Chunker) Chunk(text string) []Chunk {
    // 简单 for 循环按固定窗口滑动
    for start := 0; start < len(text); start += step { ... }
}
```

这个设计对所有内容类型（代码、散文、表格、JSON、日志）使用完全相同的分块策略，导致：

- **语义断裂**：一个 600 字符窗口可能在句子中间、代码函数中间、或者 JSON 对象中间断开，破坏语义单位
- **检索噪声**：片段语义不完整导致向量相似度匹配不准确
- **索引膨胀**：固定窗口不考虑内容的"信息密度"——高密度部分（如法律条款）需要更精确的边界，低密度部分（如日志）可以更大
- **无感知**：Chunker 接收的纯文本字符串已丢失原始格式信息（Markdown 标题、代码缩进、表格结构等），无法恢复语义边界

### 产品价值

| 维度 | 影响 |
|------|------|
| **RAG 质量** | 语义分块可提升检索准确率 15-30%（引用 Anthropic、LlamaIndex 实验数据），是搜索质量最直接的杠杆 |
| **索引成本** | 针对内容类型自适应分块可以减少总 chunk 数 20-40%，降低向量存储和嵌入 API 成本 |
| **产品差异化** | 当前所有 RAG 平台都面临同样的分块问题，自适应策略是显著的竞争壁垒 |

### 架构权衡与建议方案

```
当前管线：
Extract(text) → Chunk(text, window=600, overlap=80) → Embed(chunks)

目标管线：
Extract(text, contentType, metadata) 
    → ContentTypeRouter(决定策略)
    → ChunkPipeline {
        [策略 1] SentenceWindow    (散文/文档)
        [策略 2] RecursiveSplit    (代码/JSON/XML)
        [策略 3] SemanticBoundary  (法律/学术/合同)
        [策略 4] TokenLimit        (LLM Context Window 对齐)
      }
    → Embed(chunks)
```

**核心抽象变更：**

```go
// 现有接口（仅一个策略）
type Chunker struct { Window, Overlap int }

// 目标抽象
type ChunkStrategy interface {
    Name() string
    Chunk(text string, opts ChunkOptions) []Chunk
    // opts 可携带 content-type、document structure hints、target chunk size
}

type ChunkPipeline struct {
    strategies map[string]ChunkStrategy // contentType → strategy
    default    ChunkStrategy
}

// 内置策略注册
func NewDefaultPipeline() *ChunkPipeline { ... }
// 1. SentenceWindow: 保留句子边界
// 2. RecursiveCharacter: 递归分割（LangChain 模式）
// 3. SemanticBoundary: 使用 Embedder 检测语义转换点
// 4. TokenLimit: 按 token 数而非字符数分块
```

### 边界情况与风险

| 场景 | 处理 |
|------|------|
| 内容类型无法识别 | 回退到默认策略（当前固定窗口） |
| 语义分块策略耗时长 | 作为可选项，默认使用快速策略（SentenceWindow）|
| 分块大小变化影响嵌入 | 保持同模型同分块策略，不混用 |
| 用户自定义 ChunkStrategy | 通过 `Indexer.WithChunkStrategy` 扩展 |
| 既有对象已使用旧策略分块 | 模型漂移检测已实现（`ReindexStale`），可复用同一机制 |

---

## 方向二：近数据计算与 Serverless Function 触发器 —— 从事件通知到可编程数据变换

### 现状与代码证据

当前事件总线的所有消费端都是内置的、硬编码的：

```go
// cmd/server/main.go — 所有消费者
go avw.Run(ctx, bus.Subscribe())       // Antivirus
go rw.Run(ctx, bus.Subscribe())        // Replication
go wh.Run(ctx, bus.Subscribe())        // Webhook
go indexer.Run(ctx, bus.Subscribe())   // Indexer
```

每个消费者是编译期确定的。没有用户注册的处理函数。对比 AWS S3 + Lambda 模型——用户上传文件时触发任意自定义代码——差距显著。

`NotificationRule` 结构体明确预留了函数计算锚点：

```go
// internal/repository/repository.go
type NotificationRule struct {
    ID        string
    Events    []string
    FilterKey string
    QueueARN  string
    TopicARN  string `json:",omitempty"` // unused, kept for compat
    LambdaARN string `json:",omitempty"` // unused, kept for compat ← 这就是锚点
}
```

此外，`internal/thumbnail/thumbnail.go` 是唯一的"数据处理"函数，且是硬编码的。更多变换场景（PDF 转文本、图片 OCR、格式转换、压缩、加密）要么没有实现，要么需要修改核心代码。

### 产品价值

| 维度 | 影响 |
|------|------|
| **平台扩展性** | 用户无需修改 aero-vault 代码即可注册数据处理管线 |
| **企业集成** | 文件上传 → 自动触发 ETL、合规扫描、格式转换、通知下游 |
| **S3 兼容** | S3 通知模型的 `LambdaFunctionConfiguration` 是标配，缺失导致 SDK 兼容断裂 |
| **差异化** | MCP 工具的 `write_file` + 自定义触发器 = 完整的 data-in → data-out 闭环 |

### 架构权衡与建议方案

```
执行模型选择（按推荐优先级）：

1. [P0] Webhook 扩展 —— 现有 webhook.go 基础设施
   事件 → 桶规则匹配 → 多个 webhook URL（当前仅一个全局 URL）
   - 优点：零新基础设施，复用 webhook retry/backoff
   - 不足：外发 HTTP，非本地执行，延迟高

2. [P1] 内嵌 Wasm 沙箱 —— e.g., Wasmtime 或 wazero
   用户上传 .wasm 文件 → 注册为函数 → 事件触发时调用
   - 优点：本地执行，低延迟，沙箱安全
   - 不足：新增 Go 依赖，Wasm 生态限制（IO、网络访问需 host function）

3. [P1] 侧车容器模型 —— 用户运行 gRPC worker 进程
   aero-vault 调用本地/远程 worker 执行处理
   - 优点：语言无关，能力无限制
   - 不足：运维复杂度增加

建议路径：P0 → P2 → P1，即先解决"多目标 webhook"，再做内嵌执行。
```

**现有基础设施复用：** Job 池（`internal/jobs/jobs.go`）已有 `Registry` + `Retry` + `Dead-Letter` 完整机制，可用于执行用户注册的函数：

```go
// 增量：注册用户函数
jobReg.Register("user:transform:ocr", func(ctx context.Context, job Job) error {
    payload := parsePayload(job.Payload)
    doc, err := svc.Get(ctx, payload.Tenant, "default", payload.Key)
    text := runOCRLib(doc)
    svc.Put(ctx, payload.Tenant, "default", payload.Key+"/ocr.txt", text, ...)
    return nil
})
```

### 边界情况与风险

| 场景 | 处理 |
|------|------|
| 用户函数执行超时 | 复用 Job 超时机制；超过 max_attempts 后 dead-letter |
| 函数内修改同一文件的无限循环 | 增加去重令牌：处理中的对象跳过 trigger |
| 函数资源消耗（CPU/内存）无约束 | Wasm 沙箱有资源限制；侧车模型依赖 Docker/cgroup |
| 函数破坏系统稳定性 | Wasm 沙箱隔离；侧车模型进程隔离 |
| 函数注册管理和版本控制 | 新增 admin API 端点管理函数注册表 |

---

## 方向三：多模型路由网关 —— 从单 LLM/Embedder 到租户/场景感知的模型选择层

### 现状与代码证据

当前 AI 管线是严格的单实例架构：

```go
// cmd/server/main.go
embedder := buildEmbedder(cfg, logger)  // 一个 Embedder
llm := buildLLM(cfg, logger)            // 一个 LLM
reranker := buildReranker(cfg, logger)   // 一个 Reranker

// 全局唯一
search := ai.NewSearch(repo, embedder, logger)
chat := ai.NewChat(search, llm, repo, logger)
```

`AIConfig` 中所有配置字段都是单值的（`ChatModel`, `ChatEndpoint`, `ChatAPIKey` 等）：

```go
// internal/config/config_ai.go
type AIConfig struct {
    ChatProvider string
    ChatEndpoint string
    ChatModel    string
    ChatAPIKey   string
    // ... 同一模型模型只有一个
}
```

这意味着：
- 开发/测试/生产共用同一 LLM 端点
- 免费租户和付费企业租户使用同一模型
- 小 query（"文件路径"）和大 query（"分析这份合同"）消耗同样的模型成本
- 无法在模型间做 A/B 测试
- 一个模型故障 = 全局 AI 故障

### 产品价值

| 维度 | 影响 |
|------|------|
| **成本控制** | 小查询路由到低成本模型（如 GPT-4o-mini），复杂查询路由到高性能模型 |
| **租户分级** | 免费层使用开源模型，企业层使用 GPT-4/Claude 3.5 |
| **可用性** | 主模型故障时自动 fallback 到备用模型 |
| **质量迭代** | 生产和影子模式同时跑不同模型，对比质量 |
| **区域合规** | 不同区域租户路由到本地部署的模型 |

### 架构权衡与建议方案

```go
// 新增抽象：ModelRouter
type ModelRouter struct {
    // 路由策略
    strategies []RoutingRule
    fallback   ModelEndpoint
}

type RoutingRule struct {
    Matcher  func(ctx context.Context, req ModelRequest) bool
    Endpoint ModelEndpoint
}

type ModelEndpoint struct {
    Name      string
    Provider  string // "openai" | "anthropic" | "ollama" | "mock"
    Model     string
    Endpoint  string
    APIKey    string
    Priority  int  // fallback 顺序
    MaxTokens int
    Timeout   time.Duration
}

// 原有接口不变，新增路由层
type RouterLLM struct {
    router  *ModelRouter
    default LLM  // fallback
}

func (r *RouterLLM) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
    endpoint := r.router.Select(ctx, ModelRequest{
        Tenant:   mw.TenantFrom(ctx),
        UseCase:  detectUseCase(req.Messages),
        TokenEst: estimateTokens(req),
    })
    return endpoint.LLM.Chat(ctx, req)
}
```

**路由维度建议（优先级排序）：**

1. **Tenant tier** — `X-Aero-Tenant` + `TenantRecord.Plan` → 不同计划路由不同模型
2. **Use case** — 搜索嵌入 vs 聊天生成 → 可配置不同模型
3. **Query complexity** — token 数/tool call 数评估 → 小查询用小模型
4. **Cost budget** — 日预算剩余量 → 预算不足时自动降级
5. **Latency SLA** — 请求 `X-Latency-SLA` 头 → 低延迟选小模型

### 边界情况与风险

| 场景 | 处理 |
|------|------|
| 所有模型故障 | fallback 到 `mock` 模型或返回 503 |
| 路由规则冲突 | 优先匹配第一条规则（排序 = 优先级）|
| 模型切换导致向量维度变化 | 不同 Embedder 模型不同 dim → 按 model name 隔离索引 |
| 租户路由动态变更 | 无需重新索引，新请求立即使用新路由 |
| 成本追踪 | 路由层记录每个请求使用的模型和 token 量 |

---

## 方向四：数据血缘与溯源图基础设施 —— 从单对象血缘到完整数据谱系

### 现状与代码证据

当前 `GET /v1/lineage/objects/{id}` 的实现极其有限：

```go
// internal/api/rest/search.go
func (h *AIHandler) Lineage(w http.ResponseWriter, r *http.Request) {
    id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
    usage, _ := h.repo.ListUsageForObject(r.Context(), tenant, id, 100)
    writeJSON(w, http.StatusOK, lineageResponse{
        ObjectID:   id,
        UsageCount: len(usage), // 只是"谁查了这个对象"的使用日志
        Usage:      usage,
    })
}
```

返回数据本质上是一个审计日志（谁在什么时候查了哪些 chunk），不是真正意义上的数据血缘。

**缺失的能力：**

1. **变换血缘** — 对象 A（原始 PDF）→ 提取文本 B → 分块 C1..Cn → 嵌入 V1..Vn，无法追踪
2. **派生关系** — 对象 X 从对象 Y 复制/变换而来（`x-amz-copy-source` 不记录派生链）
3. **溯源查询** — "这个嵌入向量是从哪份原始文档生成的？经过了哪些变换？"
4. **影响分析** — "如果删除这个原始文档，哪些派生数据和嵌入需要清理？"
5. **合规溯源** — "这份 AI 回答中引用的内容来自哪个数据源？是否有合规标记？"

`repository.Object` 没有 `DerivedFrom`、`TransformationHistory`、或 `Provenance` 字段。

### 产品价值

| 维度 | 影响 |
|------|------|
| **AI 治理** | EU AI Act 要求训练/推理数据可溯源；客户审计必需 |
| **数据质量** | 发现数据管线中的错误转换源头 |
| **合规** | 识别敏感数据的传播路径（GDPR 删除权需要追踪所有副本）|
| **调试** | RAG 回答引用追溯：chunk → 页面 → 文档 → 来源 |
| **影响分析** | 删除操作前评估影响范围 |

### 架构权衡与建议方案

```go
// 新增 Provenance 数据结构
type ProvenanceEvent struct {
    ID           int64
    SourceID     int64  // 源对象 ID（可为空 — 原始上传）
    TargetID     int64  // 派生对象/Chunk ID
    TargetType   string // "object" | "chunk" | "embedding"
    RelationType string // "extracted_from" | "derived_from" | "copied_from" | "transformed_from"
    TransformOp  string // "extract_text" | "ocr" | "chunk" | "embed" | "compress" | "thumbnail"
    Params       map[string]string // 变换参数（如 chunk window=600）
    Actor        string // "system:indexer" | "user:alice" | "webhook:etl"
    TenantID     string
    CreatedAt    time.Time
}
```

**关键管线接入点：**

| 管线阶段 | 血缘事件 |
|----------|---------|
| 对象 PUT（新上传） | 根节点，`RelationType=original_upload` |
| 对象 COPY（x-amz-copy-source） | `RelationType=copied_from`, `SourceID=源` |
| 文本提取（Extractor） | `TargetType=chunk`? `RelationType=extracted_from` |
| 分块（Chunker） | 批量 `RelationType=chunked_from`, `TargetType=chunk` |
| 嵌入（Embedder） | 批量 `RelationType=embedded_from` |
| AI 查询（Chat/Search） | 现有 `Usage` 表 + `RelationType=ai_queried` |
| 变换（缩略图/格式转换） | `RelationType=transformed_from` |

**查询场景：**

```
# 向前溯源：从 chunk 追溯到原始文档
chunk C1 → extracted_from → object A (原始 PDF)

# 向后影响：删除对象 A 影响什么
object A → (extracted_from) n chunks → (embedded_from) n embeddings
object A → (copied_from) object B → (copied_from) object C

# 使用追溯：这份回答用了哪些数据
response R → ai_queried → chunk C1, C2, C3 → extracted_from → object A, B
```

### 边界情况与风险

| 场景 | 处理 |
|------|------|
| 老对象无血缘记录 | 新系统只记录新增对象；历史数据通过 ReindexStale 补充 |
| 血缘图膨胀 | 设置保留期限（如 90 天）；按 tenant 分区 |
| 删除对象时血缘记录 | 保留血缘（软删除），只标记对象为 deleted |
| 跨租户血缘 | 同一租户内可追踪；跨租户默认禁止（安全策略）|
| 血缘查询性能 | 索引 `(source_id, target_id, relation_type)`；限制深度 |

---

## 方向五：近似重复检测与内容指纹引擎 —— 从字节级去重到语义级去重

### 现状与代码证据

当前系统对内容重复**完全无感知**：

- **存储层**：每次 `Put` 都写入新 blob (`internal/service/file_crud.go:Put`)，零内容哈希检查
- **搜索层**：`ai.Search.Query` 返回的 Hit 列表不包含去重逻辑，近似相同的 chunk 全部返回
- **对象模型**：`repository.Object` 无 `ContentHash`、`Fingerprint`、`RefCount` 字段
- **VM 层**：`storage.Storage` 接口无 `PutIfAbsent`、`ContentHash` 或引用计数方法

这意味着：
1. 同一份 10MB PDF 被 5 个用户上传 → 占用 50MB 空间
2. 同一份报告的小版本修改 → 完全独立的两份副本
3. 搜索 "公司年报" → 返回 20 条近似相同的 chunk 挤满结果页

### 产品价值

| 维度 | 影响 |
|------|------|
| **存储成本** | 企业场景去重率可达 30-70%（代码库、文档库、备份场景）|
| **搜索质量** | 去重后的搜索结果多样性和覆盖率显著提升 |
| **传输效率** | 去重后可实现"变化增量上传"（类似 rsync）|
| **合规** | 检测敏感数据的意外副本传播 |
| **S3 兼容** | S3 无原生去重但生态工具依赖指纹检测 |

### 架构权衡与建议方案

**三层检测架构：**

```
Layer 1: 精确字节级 (SHA256)
  - Put 路径计算 SHA256 → 查 content_hash 索引
  - 命中 → 引用计数 +1，不写新 blob
  - 适用于：完全相同文件、备份、容器镜像

Layer 2: 近似文本级 (SimHash/MinHash)
  - 提取文本后生成 SimHash 指纹（64/128 bit）
  - 海明距离 < 阈值 → 近似重复标记
  - 适用于：文档变体、复制改写、翻译

Layer 3: 语义级 (Embedding Cosine)
  - 复用现有向量索引
  - 新增对象 chunk 的向量与已有库对比 > 0.95 → 语义近重复
  - 适用于：不同来源但内容相同的新闻文章等
```

**代码锚点改造量评估：**

| 组件 | 改动规模 | 说明 |
|------|---------|------|
| `storage/storage.go:Storage` 接口 | 小 | 新增 `ContentHash(key) string` 方法（存储后端实现）|
| `storage/local_write.go:writeObject` | 小 | 写入时计算 SHA256，写入 `content.hash` 侧边文件 |
| `service/file_crud.go:Put` | 中 | 计算 SHA256 → 查 `content_hash_idx` → 直接引用或写入 |
| `repository/sql_objects.go` | 中 | 新增 `content_hash_idx`、`ref_count` 列、引用计数操作 |
| `ai/search.go:Query` | 小 | 返回结果根据 content_hash 做多样性选择（MMR 替代简单 top-k）|
| `internal/reconcile/scrub.go` | 小 | 引用计数归零时物理删除 |

### 边界情况与风险

| 场景 | 处理 |
|------|------|
| 加密对象无法字节级去重 | 加密前计算哈希；若使用 SSE 则每个对象唯一密文无法去重 |
| 引用计数一致性问题 | 引用计数在事务内更新；隔离级别保证 |
| 近似检测 false positive | SimHash 阈值可配置；标记为"疑似"而非自动去重 |
| 版本化桶的去重 | 版本化桶跳过去重（每个版本保持独立）|
| 并发上传同一内容 | `content_hash` 唯一约束阻止重复插入；并发写入分先后 |

---

## 总结：优先级建议实施顺序

| 优先级 | 方向 | 建议原因 | 前置依赖 |
|--------|------|---------|----------|
| **P0** | 自适应分块（方向一） | RAG 质量的根本杠杆，改动集中且低风险 | 无 |
| **P1** | 多模型路由（方向三） | 成本控制和可用性直接影响 SaaS 运营 | 无 |
| **P2** | 数据血缘（方向四） | 合规需求随时间推移越来越紧迫；技术债务积累快 | 方向一的 Chunk ID 稳定 |
| **P2** | 近数据计算（方向二） | 高产品差异化但架构影响大；建议等事件系统稳定 | 多目标 Webhook（未覆盖）|
| **P3** | 近似去重（方向五） | 存储成本优化；企业场景高价值但实现面广 | 无 |
