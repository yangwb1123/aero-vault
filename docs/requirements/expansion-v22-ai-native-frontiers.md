# AeroVault 高价值扩展方向 v22 — AI-Native 平台的前沿纵深

> **分析范围：** 全代码库一次扫描（`cmd/server/`、`internal/*` 全部子包、`sdk/*`、`deploy/*`、`docs/*`、迁移文件 24 对）  
> **分析日期：** 2026-07-10  
> **视角：** 资深架构师 / 产品经理 — 关注「AI-Native 文件平台的代际差异化能力」  
> **方法：** 逐一比对前 21 期 expansion 文档（v1–v21，累积 ~1.8MB+ 分析）、`ROADMAP.md`、`CHANGELOG.md`、`TODO.md`，确认每个方向在当前代码和既有文档中**零覆盖或仅行级提及**。  
> **原则：** 不编写任何实现代码。

---

## 审阅：前 21 期覆盖的去重结论

前 21 期 expansion 文档累计从约 21 个视角覆盖了 **~100+ 个方向**。以下大类已全面覆盖，本期不再重复：

| 领域 | 覆盖期数 | 典型方向数 |
|------|---------|-----------|
| 对象存储 CRUD / 多协议适配 / S3 兼容子资源 | v1~v21 贯穿 | ~12 |
| AI 管线（Extract/Chunk/Embed/Search/BM25/Hybrid/Rerank/Chat/Agent/PII/Cache） | v1~v13, v21, ROADMAP #1, #2 | ~14 |
| 存储后端（Local/S3/OSS/COS/KMS/SSE/CircuitBreaker/Tiering） | v4~v17, v21, ROADMAP #5, #9 | ~10 |
| 多租户（CRUD/Quota/Budget/Audit/Billing/Isolation） | v1~v19 贯穿 | ~8 |
| 认证授权（API Key/JWT/SigV4/OIDC/SAML/SCIM/Policy Engine） | v1~v20 | ~9 |
| 事件/通知/Webhook/SSE 总线 | v1~v21 | ~8 |
| 复制/HA/集群/Federation | v1~v21, ROADMAP #3, #10 | ~8 |
| Reconcile/GC/Lifecycle/Orphan/Retention | v1~v21, ROADMAP #5, #8 | ~8 |
| 合规（WORM/Legal Hold/Noncurrent Expiry/Access Log/Retention） | v2~v21 | ~6 |
| 可观测性（OTel/Metrics/Grafana/Prometheus/SLO/Tracing） | v11~v21 | ~5 |
| 工程质量（内存安全/并发/压缩/错误模型/流式/大对象） | v11~v15, analysis-gaps | ~6 |
| Web UI/Admin Console | v3~v20 | ~4 |
| SDK/CLI 完整性 | v11~v20 | ~3 |
| 基础设施（CDN/FUSE/TLS/ACME/Feature Flag/IP ACL/配置热重载） | v13~v20 | ~5 |
| 导入/迁移/批量操作 | v18~v20 | ~2 |
| 插件/扩展/钩子系统 | v18, v20 | ~2 |
| GitOps/声明式配置 | v20 | ~1 |
| Metadata Schema 治理 | v20 | ~1 |
| 加权公平队列/资源隔离 | v20 | ~1 |
| 分片上传 GC | v21 | ~1 |
| 版本生命周期/合规保留 | v21 | ~1 |
| 多活跨区复制/故障切换 | v21 | ~1 |
| 搜索缓存/Embedding 缓存/结果缓存 | v11~v13 | ~3 |
| PII 检测/脱敏 | v1~v6, v21 | ~2 |

**本期不再重复的边界情况**（前 21 期已覆盖）：
- S3 accelerate stub → v21 已记录
- ListByTag 客户端过滤 → v21 已记录
- Webhook 目标无速率限制 → v21 已记录
- AI Agent 无会话上下文 → v21 已记录
- StorageClass 默认值冲突 → v21 已记录
- Presign URL 无方法约束 → v21 已记录
- StorageClassGauge 只采样 default → v21 已记录
- 与 versioning 冲突的 Retention GC → v21 详细分析
- 向量维度漂移 → ROADMAP #5 + v8
- Event bus 64-deep buffer → ROADMAP #3
- 大对象内存缓冲 → ROADMAP #5

---

## 本期方向总览

| # | 方向 | 类型 | 影响 | 核心代码锚点 | 既有覆盖 |
|---|------|------|------|-------------|---------|
| 1 | **🔴 AI Content Enrichment & Write-Back Pipeline** | 产品/架构 | 将 AI 管线从「只读索引」升级为「读写转换引擎」— 这是 AI-Native 平台与普通对象存储 + RAG 的分水岭 | `internal/ai/indexer.go`（只读）、`internal/service/file_crud.go:Put`（无转换钩子）、`internal/events/bus.go`（事件已就绪但无转换消费者） | **零覆盖** |
| 2 | **🔴 Vector & Embedding Platform API** | 产品/架构 | 将内部 embedding/vector 基础设施暴露为外部可消费的 API — 外部应用与文件共享同一向量空间，形成平台网络效应 | `internal/ai/embedder.go`（内部调用）、`internal/ai/search.go`（内部检索）、`internal/api/rest/search.go`（只接受文件检索） | **零覆盖** |
| 3 | **🟠 Event-Driven User Functions (aero-vault Lambda)** | 产品/架构 | 用户注册的短生命周期函数在文件事件上触发执行 — 从「事件通知」跨越到「事件驱动自动化」 | `internal/events/bus.go`（Publish 点就绪）、`internal/jobs/`（job 框架可复用）、`internal/api/rest/admin_jobs.go`（admin 操作已就绪） | v18 插件系统行级提及、v20 方向二行级提及，**无独立设计** |
| 4 | **🟠 Multi-Modal Content Understanding** | AI/产品 | 文本之外的图像、音频、视频理解能力 — AI-Native 平台不应只能「读字」 | `internal/ai/extractor.go`（纯文本接口）、`internal/ai/chunker.go`（文本分块）、`internal/ai/search.go`（纯文本检索） | v11 一行提及「image/CLIP」，**非独立方向** |
| 5 | **🟠 Collaborative File Workspace & Sharing** | 产品/体验 | 文件分享、注释、审批、实时协作 — 从「存储后端」到「协作平台」的体验跃升 | `internal/webui/static/index.html`（单页 SPA，无协作功能）、`internal/service/file.go`（无分享/锁/通知模型）、`internal/api/rest/handler.go`（无分享端点） | **零覆盖** |

---

## 1. 🔴 AI Content Enrichment & Write-Back Pipeline

### 现状

当前 AI 管线是**只读的**：

```
File Upload → Event → Extractor(文本提取) → Chunker(分块) → Embedder(向量化) → Index(写入索引) → Search(读取)
```

三个关键事实揭示了管线的能力边界：

1. **indexer.go** (`internal/ai/indexer.go`) — `IndexObject` 从存储读取对象 → 提取文本 → 分块 → 嵌入 → 写入 chunks 表。**从不向存储写回任何东西**。提取的文本、生成的摘要、检测到的实体、分类标签全部「随风而逝」（只存在于分块中用于检索）。

2. **FileService.Put** (`internal/service/file_crud.go`) — 写入路径没有任何「上传后转换钩子」：文件落地后触发 Event，但 Event 的消费者（indexer、antivirus、replication）全部是只读分析的。

3. **Metadata 系统** (`repository.Object.Metadata map[string]string`) — 支持用户自定义 metadata，但没有任何 AI 自动填充的 metadata 字段。`_aero_content_md5` 是手动计算的，`_aero_legal_hold` 是手动设置的，没有任何 AI 驱动的 metadata 自动生成。

### 为什么需要它

1. **AI-Native 平台的本质差异**：普通的对象存储 + RAG 与 AI-Native 平台的差别在于：前者只能检索文件内容，后者能**理解、转换、增强**文件内容并将结果持久化。如果上传一份 PDF 合同，当前系统只能让你通过 RAG 问「合同里写了什么」。而一个 AI-Native 平台应该：
   - 自动生成摘要并写为 `_aero_summary` metadata
   - 自动检测文档语言并写入 `_aero_language` tag
   - 自动提取关键实体（人名、日期、金额）并写为结构化 metadata
   - 自动分类（发票/合同/报告/邮件）并写入 `_aero_category` tag
   - 支持用户定义转换规则：「所有上传到 `/invoices/` 的文件，提取金额、日期、供应商并写入 metadata」

2. **当前基础设施已预备**：事件总线（`events.Bus`）在每个对象变更时 `Publish`，Indexer 已经是一个事件消费者。新增一个 `EnrichmentWorker` 订阅同一总线，在 indexer 之后（或之前）运行 AI 转换，将结果写回 metadata/派生对象。工程复用度极高。

3. **复用现有组件**：
   - `ai.Extractor` 已提取纯文本 → 可直接输入给 LLM 做摘要/分类
   - `ai.LLM` (`internal/ai/llm.go`) 已配置可用 → 零额外依赖做摘要/实体提取
   - `service.FileService.SetTags` / `repo.SetObjectMetaKey` 已存在 → 写回成本极低
   - `ChunkCleaner` 模式已验证 → 新增 `EnrichmentHook` 类似模式

### 边界情况

| 场景 | 风险 | 防护 |
|------|------|------|
| 转换 LLM 调用失败（超时/限流） | 文件已上传但未富化 | 后台重试（复用 job 队列），不影响用户响应路径 |
| 转换结果写入 metadata 时冲突 | 用户同时 PUT 新 metadata | 使用 `SetObjectMetaKey` 原子更新，AI 前缀 `_aero_` 保留给系统使用 |
| 同一文件被多次触发转换 | 重复生成摘要/标签 | 幂等 key 基于 `objectID + enrichmentType` |
| 大文件转换超时 | LLM context window 溢出 | 分块处理（复用 chunker），逐块摘要后合并 |
| 用户自定义转换规则 | 语法错误/无限循环 | 沙箱执行（WebAssembly 或 JS 引擎）+ step budget |

### 影响范围

| 层 | 变动 |
|---|---|
| `internal/ai/` | 新增 `enrichment.go`：`EnrichmentWorker` 订阅事件 → 调用 LLM → 写回 metadata |
| `internal/service/file.go` | `FileService` 新增 `WithEnrichmentHooks(hooks ...EnrichmentHook)` |
| `internal/repository/sql_objects.go` | `SetObjectMetaKey` 已存在（粒度单键更新） |
| `internal/events/bus.go` | 复用现有事件通道，无需改动 |
| `internal/config/config_ai.go` | 新增 `AI_ENRICHMENT_ENABLED` / `AI_ENRICHMENT_TYPES` |
| `internal/api/rest/handler.go` | 可能新增 `POST /v1/files/*/enrich` 手动触发 |

### 市场差异化

这是 AWS S3 + Bedrock 无法直接提供的体验（需要在 S3 和 Bedrock 之间手动搭建管道）。aero-vault 可以实现「上传即完成」——文件落地后自动完成摘要、分类、标签、实体提取，搜索结果可以直接按这些 AI 标签过滤。

---

## 2. 🔴 Vector & Embedding Platform API

### 现状

当前 embedding/vector 能力完全被封装在平台内部：

```
Flow: 文件上传 → indexer(embed) → search(内部检索) → chat/agent(内部问答)
```

- `ai.Embedder` (`internal/ai/embedder.go`) — `EmbedTexts(ctx, texts) ([][]float32, error)` 接口只有 Indexer 和 Search 内部调用。没有任何 REST API 或 SDK 方法暴露给外部应用。
- `ai.Search` (`internal/ai/search.go`) — Search 只能检索**文件分块**的向量。外部应用无法将自己的文本/数据向量化后与文件向量在同一空间检索。
- `ai.VectorIndex` (`internal/ai/vectorindex.go`) — 接口定义良好 (`SearchVectors(ctx, vec, k) ([]Hit, error)`)，但是**私有的**：没有 `POST /v1/vectors/search` 端点。

### 为什么需要它

1. **向量搜索是独立于文件存储的通用能力**。用户需要的不只是「在文件里搜索」，还需要：
   - 将应用内部的文档（CRM 记录、工单、代码片段）向量化并与文件一起检索
   - 在外部应用中使用 aero-vault 的 embedding 模型（无需自己部署）
   - 将 aero-vault 作为「统一向量知识库」——对内管理文件向量，对外提供检索 API

2. **平台网络效应**：外部应用通过 Embedding API 依赖 aero-vault 的向量空间 → 更深度绑定 → 用户迁移成本提高。这是从「文件存储」到「知识基础设施」的跳跃。

3. **技术复用度极高**：
   - `ai.HTTPEmbedder` 已能与 OpenAI/Ollama 等兼容
   - `ai.QdrantIndex` / `ai.PgVectorIndex` / 默认 `repoVectorIndex` 都已就绪
   - `ai.Search` 的 `WithVectorIndex` / `WithLexicalIndex` 设计就是为可拔出
   - 只需新增 REST API 端点将现有能力暴露出去

### 边界情况

| 场景 | 风险 | 防护 |
|------|------|------|
| 外部应用嵌入 100 万条记录 | 存储/检索性能下降 | 租户级容量配额（复用现有 quota 机制）；租户级向量集合隔离 |
| 外部向量与文件向量维度不一致 | 无法在同一空间检索 | 统一使用配置的 `AI_EMBED_DIM` 维度；不同模型分别索引 |
| 外部嵌入的文本包含 PII | PII 泄漏 | 可选 PII 扫描（复用 `pii.go`）对外部嵌入也执行 |
| 外部应用删除向量但保留文件 | 引用断裂 | 外部向量的 `source` 标记为 `external`，不与文件生命周期绑定 |
| 租户 A 检索到租户 B 的向量 | 数据隔离泄露 | 所有向量按 tenant 命名空间隔离（Qdrant 用不同 collection/prefix，pgvector 用 tenant 列过滤） |

### API 提案

```
# 嵌入 API（返回向量）
POST /v1/embed
{ "texts": ["hello", "world"] }
→ { "embeddings": [[0.1, 0.2, ...], ...], "model": "hash-256", "dim": 256 }

# 向量写入（外部应用注册自己的向量）
POST /v1/vectors
{ "vectors": [{"id": "doc-1", "vector": [...], "payload": {"text": "hello", "source": "crm"}}] }
→ { "indexed": 1 }

# 混合检索（文件 + 外部）
POST /v1/vectors/search
{ "query": "hello", "k": 10, "include_external": true }
→ { "hits": [...], "model": "hash-256" }

# 向量删除
DELETE /v1/vectors/{id}
```

### 影响范围

| 层 | 变动 |
|---|---|
| `internal/api/rest/router.go` | 新增 `/v1/embed`, `/v1/vectors/*` 路由组 |
| `internal/api/rest/handler.go` | 新增 `EmbedHandler`, `VectorWriteHandler`, `VectorSearchHandler` |
| `internal/ai/embedder.go` | 新增 `EmbedText` 导出版本（移除 context 外依赖） |
| `internal/ai/search.go` | `Search` 新增 `SearchVectorsExternal` 方法（不依赖 ObjectID） |
| `internal/repository/sql_chunks.go` | 可能需要新增 `external_vectors` 表或扩展 chunks 表加 `source` 列 |
| `sdk/go/aerovault/client.go` | 新增 `Embed`, `IndexVector`, `SearchVectors` 方法 |
| `internal/config/config_ai.go` | 新增 `AI_VECTOR_API_ENABLED` |

### 市场差异化

现有竞品：Pinecone（纯向量 DB）、Weaviate（向量+对象混合）、Milvus（纯向量）。但没有任何一个与**文件存储 + 自动索引**深度集成。用户需要在 Pinecone 里自己管理向量、在 S3 里存文件、在 Lambda 里搭管道。aero-vault 可以把这一切合并为一个平台。

---

## 3. 🟠 Event-Driven User Functions (aero-vault Lambda)

### 现状

事件系统的消费者当前**全部是内部硬编码的**：

```
events.Bus.Subscribe() →
    indexer (ai)  |  antivirus (av)  |  replication (repl)  |  webhook (http post)  |  sse stream
```

- `events/bus.go` 的 `Subscribe()` 返回 `chan Event`，订阅者列表在 `main.go` 中硬编码。
- 用户只能通过 Webhook 接收事件通知到外部 URL，**无法在 aero-vault 进程内执行自定义逻辑**。
- `internal/jobs/` 的 `Registry` (`jobs.NewRegistry()`) 可以注册自定义 job handler，但 `Registry` 只由 `main.go` 内部使用，用户无法扩展。

### 为什么需要它

1. **Webhook 是不够的**。事件通知到外部系统后，用户还需要搭建接收服务、处理数据、回调 aero-vault。这增加了延迟、成本、运维复杂度。如果用户只是想「上传文件后自动生成缩略图并打标签」，他需要：
   - 配置 Webhook → 部署一个接受 POST 的服务器 → 调用 `/v1/files/x/thumbnail` → 调用 `PUT /v1/files/x/tags`。这至少需要半天的工作。
   - 对比 aero-vault Lambda：上传文件 → 自动触发函数 → 在进程内完成所有操作。零网络延迟、零运维。

2. **复用现有基础设施**：
   - `internal/jobs/jobs.go` — `Registry` 设计就是可扩展的，只是当前被 `main.go` 独占
   - `internal/jobs/queue.go` — Job 队列支持重试、超时、死信
   - `internal/api/rest/admin_jobs.go` — Admin 已经可以 `ListJobs` / `RetryJob`
   - 只需要新增「用户注册函数」的 API + 函数执行沙箱

3. **渐进式实现可行**：第一版可以只支持 WebAssembly 沙箱（安全、跨语言、可限制资源）或嵌入 JS 引擎（Go 有 `goja` 等纯 Go JS 引擎，零 CGO）。

### 边界情况

| 场景 | 风险 | 防护 |
|------|------|------|
| 用户函数死循环 | 占用 goroutine 无限期 | 强制超时（`context.WithTimeout`）+ CPU 使用量上限 |
| 用户函数调用 `os.Exit` | 杀死整个进程 | WebAssembly 沙箱拦截；或 JS 引擎捕获 panic |
| 用户函数递归触发自身（文件上传→函数→写文件→事件→触发自身） | 无限循环 | 检测递归深度 + 事件来源标记（函数触发的事件标记 `_trigger=function:{id}`） |
| 用户函数访问其他租户数据 | 安全漏洞 | 函数执行时 context 注入 tenant，`FileService` 强制 tenant 隔离 |
| 函数执行耗尽了所有 worker | 阻塞系统 job | 为系统 job 保留专用 worker 池（`MIN_SYSTEM_WORKERS`） |
| Wasm 函数无法访问文件系统 | 功能受限 | 通过 host function（`fd_write` 的 WASI 等效）提供受控的 FileService API |

### 架构概要

```
┌─ 新增包: internal/functions/ ─────────────────────────────────────┐
│                                                                   │
│ type Function struct {                                            │
│     ID        string    // 用户创建时分配                           │
│     TenantID  string                                              │
│     Trigger   TriggerSpec  // { event_type, bucket_filter, ... }   │
│     Runtime   string      // "wasm" | "js"                        │
│     Code      []byte      // 编译后的 wasm 或 JS 源码               │
│     Timeout   time.Duration                                       │
│     MaxMemory int64       // bytes                                 │
│     Env       map[string]string                                    │
│ }                                                                 │
│                                                                   │
│ type Runtime interface {                                          │
│     Run(ctx, code, input) (output, error)                         │
│ }                                                                 │
│                                                                   │
│ // Worker 订阅事件总线，匹配 Trigger，执行函数                       │
│ func NewWorker(bus, repo, svc) *Worker                             │
│ func (w *Worker) Run(ctx)                                          │
└───────────────────────────────────────────────────────────────────┘
```

### 影响范围

| 层 | 变动 |
|---|---|
| `internal/functions/` | 新建包：`function.go`（数据模型）+ `worker.go`（事件订阅者）+ `runtime_wasm.go` / `runtime_js.go` |
| `internal/events/bus.go` | 事件 payload 可能需要携带更多上下文（`StorageKey`、`ContentType`） |
| `internal/jobs/jobs.go` | `Registry` 开放给用户函数注册 |
| `internal/api/rest/admin.go` / `router.go` | 新增 `POST /v1/admin/functions`、`GET /v1/admin/functions`、`DELETE /v1/admin/functions/{id}` |
| `internal/repository/` | 可能需要 `functions` 表持久化函数代码和配置 |
| `internal/config/` | 新增 `FUNCTIONS_ENABLED`、`FUNCTIONS_MAX_TIMEOUT`、`FUNCTIONS_MAX_MEMORY` |
| `main.go` | 启动时初始化函数 Worker 并注册到事件总线 |

### 市场差异化

AWS Lambda + S3 是业界标准模式，但需要用户在 AWS 生态内操作。aero-vault Lambda 提供**自包含的事件驱动计算**——不需要管理额外的计算服务、IAM 角色、VPC 配置。对于中小团队和边缘部署场景（IoT、边缘计算、私有化部署），这是一个显著的简化。

---

## 4. 🟠 Multi-Modal Content Understanding

### 现状

AI 管线从提取到检索全部是**纯文本的**：

- `ai.Extractor` (`internal/ai/extractor.go`) — 内置 extractor 使用 `text` 包检测编码 + 按行提取；`RemoteExtractor` 发送字节流到外部端点。两者都假设输入是可转换为文本的。
- `ai.Chunker` (`internal/ai/chunker.go`) — `Chunk(text string) []Chunk` 只接受字符串。
- `ai.Search` — `Search(ctx, req)` 检索的对象是 `Chunk`（只有 `Text` 字段），没有图像、音频、视频的检索路径。
- **没有图像嵌入**：没有 CLIP 或类似模型生成图像向量。
- **没有音频/视频处理**：没有 Whisper 转录、没有视频帧提取、没有语音搜索。
- 当前的 `thumbnail` (`internal/thumbnail/thumbnail.go`) 功能仅支持**请求时**生成缩略图（`GET /thumbnail?w=&h=`），不参与索引管线。

### 为什么需要它

1. **「AI-Native 文件平台」不能只处理文本**。现代企业文件中：
   - PDF 中包含扫描图片（需要 OCR，当前提取器无法从图片 PDF 中提取文本）
   - 产品目录、设计图纸、医疗影像以图片为主
   - 会议录音、语音备忘录是音频文件
   - 监控视频、培训视频是视频文件
   - 代码文件需要结构化的代码理解（AST 感知分块）而不是按行分割

2. **多模态是竞品差异化**。MinIO、SeaweedFS 等对象存储完全不碰 AI。AWS S3 + Bedrock 可以搭多模态但极其复杂（S3 Event → Lambda → Bedrock → 写回 S3）。aero-vault 可以将多模态理解内建为**一键开启**的能力。

3. **复用现有抽象层**：
   - `ai.Extractor` 接口已定义 → 新增 `ImageExtractor`（OCR）、`AudioExtractor`（Whisper）、`VideoExtractor`（帧提取+OCR）
   - `ChunkSink` 接口已定义 → 新增 `ImageChunkSink`（CLIP 向量存入 Qdrant）
   - `ai.VectorIndex.SearchVectors` 已支持跨模态搜索（向量维度一致即可）
   - **多模态融合搜索**：用户搜「cat」→ 同时检索文本片段包含「猫」的文档 **和** 包含猫的图像 → 混合返回

### 边界情况

| 场景 | 风险 | 防护 |
|------|------|------|
| OCR 处理大 PDF（500+ 页） | 内存 OOM | 流式处理（逐页提取）+ 超时 + 大小限制 |
| 音频/视频文件过大 | 存储和处理时间过长 | 分片处理 + 配置最大处理时长 |
| 图像向量与文本向量维度不一致 | 无法跨模态检索 | 所有模态使用同一 `AI_EMBED_DIM` 维度；不同模型映射到统一空间 |
| OCR 质量低导致乱码 | 搜索结果偏移 | 索引时记录 `extraction_confidence`，搜索时可选按置信度过滤 |
| 多模态索引失败后重试 | 重复消耗 API 费用 | 幂等 key + 索引状态追踪（`index_status` 表） |
| Whisper 转录的语言检测不准 | 搜索无法匹配 | 保留原始语言 + 翻译为索引语言（可选项） |

### 影响范围

| 层 | 变动 |
|---|---|
| `internal/ai/extractor.go` | `Extractor` 接口扩展为 `Extract(ctx, r io.Reader, contentType string) (string, error)` 以支持 content-type 驱动的分流 |
| `internal/ai/extractor_image.go` | 新增：OCR（Tesseract CGo 或外部 OCR API） |
| `internal/ai/extractor_audio.go` | 新增：Whisper 集成（通过 HTTP API 调用或嵌入模型） |
| `internal/ai/extractor_video.go` | 新增：ffmpeg 帧提取 → OCR 每帧 |
| `internal/ai/chunker.go` | 可能新增 `ImageChunker`（保留图像分块的空间关系） |
| `internal/ai/embedder.go` | `Embedder` 接口可能需要 `EmbedImage` 方法（返回与文本统一维度的向量） |
| `internal/ai/search.go` | `Search` 融合文本 + 图像结果 (RRF 或加权融合) |
| `internal/ai/indexer.go` | Indexer 根据 content-type 选择 extractor 管线 |
| `internal/thumbnail/thumbnail.go` | 复用缩略图生成逻辑到索引管线 |
| `internal/config/config_ai.go` | 新增 `AI_MULTIMODAL_ENABLED` / `AI_OCR_PROVIDER` / `AI_WHISPER_ENDPOINT` |

### 市场差异化

当前开源 RAG 方案（LangChain + Chroma + Tesseract + Whisper）需要用户自行搭建 5-6 个独立服务，配置复杂的 pipeline。aero-vault 可以实现：上传图像/音频/视频 → 自动 OCR/转录 → 向量化 → 多模态统一检索。**一个二进制文件完成这一切**。

---

## 5. 🟠 Collaborative File Workspace & Sharing

### 现状

aero-vault 当前是一个**纯粹的存储后端**，没有任何协作功能：

- 文件分享：不存在。没有分享链接、没有过期访问、没有密码保护、没有下载限制。
  - `internal/service/file.go:PresignGet` 生成下载 URL，但无权限控制（任何知道 URL 的人都可以下载）、无过期时间以外的约束、无追踪。
  - S3 兼容的 `GET ?presigned-url` 没有用户友好的分享 UI。
- 文件注释：不存在。没有文件级别的评论、标注、讨论线程。
  - `repository.Object` 没有 `comments`、`annotations` 相关字段。
  - `internal/webui/static/index.html` 是只读浏览 + 上传，无协作 UI。
- 审批工作流：不存在。没有「请审核这个文件」的流程。
- 实时协作：不存在。没有 WebSocket 连接、没有光标同步、没有冲突解决。
  - 当前只有一个 SSE 端点 (`/v1/events/stream`) 用于单向推送事件。

### 为什么需要它

1. **纯对象存储的竞争是红海**（S3、MinIO、SeaweedFS、Ceph）。如果 aero-vault 只是一个「带 RAG 的 S3」，它的差异化是有限的，且容易被竞品复制（MinIO + 外部 RAG 管道）。

2. **协作是文件平台的自然演进**。Google Drive、Dropbox、Box 的成功证明了用户愿意为协作付费。aero-vault 的 AI 能力+协作功能=独特的价值主张：
   - 分享文件时自动附带 AI 摘要（接收者无需打开就知道内容）
   - 在文件评论中可 @AI 提问（「这份合同第 3 条的金额是多少？」）
   - 审批流程中 AI 自动检查合规性

3. **技术可行性高**：
   - `PresignGet` 已存在 → 扩展为带权限约束的分享链接（密码、次数限制、过期时间、IP 白名单）
   - `Idempotency-Key` 已实现 → 可以为分享链接生成唯一 ID
   - `internal/events/bus.go` → 文件被分享/评论时发布事件
   - Web UI (`index.html`) → 可扩展为包含分享管理、评论面板

### 边界情况

| 场景 | 风险 | 防护 |
|------|------|------|
| 分享链接被暴力枚举 | 数据泄漏 | 分享 ID 使用 UUIDv4（128 位随机）；速率限制分享链接验证 |
| 分享链接转发给未授权人员 | 非预期访问 | 可选的收件人邮箱白名单 + 邮件验证码 |
| 分享的文件在分享后被删除 | 404 | 返回「文件已被删除」页面而非 404；接收者得到通知 |
| 评论中包含 PII | 隐私泄漏 | 评论内容可选 PII 扫描（复用 `pii.go`） |
| 多用户同时编辑同一文件 | 冲突覆盖 | 乐观锁（`If-Match`/ETag）+ 最后写入者胜出 + 版本历史（已实现） |
| 分享链接追踪 | 谁访问了分享链接 | 访问日志（复用 `audit_log` 表或新增 `share_access_log` 表） |

### 最小可行功能集

```
Phase 1 — 文件分享（2 周）
  - POST /v1/shares { "key": "doc.pdf", "expires": "2026-08-01", "password": "secret" }
    → { "share_id": "uuid", "url": "https://.../s/uuid", "expires": "..." }
  - GET /s/{share_id}?password=secret → 下载文件
  - DELETE /v1/shares/{id} → 撤销分享
  - Web UI：「分享」按钮 → 生成链接 → 复制到剪贴板

Phase 2 — 文件评论与审批（4 周）
  - POST /v1/files/*/comments { "text": "请审核第3段" }
    → { "comment_id": "...", "created_at": "..." }
  - GET /v1/files/*/comments → 评论列表
  - POST /v1/files/*/approval-requests { "assignee": "user@co", "note": "..." }
    → { "request_id": "...", "status": "pending" }
  - Web UI：评论面板 + 审批请求状态

Phase 3 — 实时协作（6 周，可选）
  - WebSocket /v1/ws → 文件变更实时推送
  - 协作光标（Operational Transform / CRDT 简化版）
```

### 影响范围

| 层 | 变动 |
|---|---|
| `internal/service/file.go` | 新增 `ShareService` 或扩展 `FileService` 加分享/评论方法 |
| `internal/api/rest/router.go` | 新增 `/v1/shares`、`/s/{share_id}`、`/v1/files/*/comments` 路由 |
| `internal/api/rest/handler.go` | 新增分享/评论 handler |
| `internal/repository/` | 新增 `shares` 表 + `comments` 表 |
| `internal/webui/static/index.html` | 扩展 UI：分享对话框、评论面板、审批请求 |
| `internal/reconcile/retention.go` | 过期分享链接 GC（复用 RetentionJob 框架） |
| `internal/middleware/ratelimit.go` | 分享链接验证端点应用独立速率限制（防暴力破解） |
| `internal/events/bus.go` | 新增事件类型 `share.created` / `comment.added` |

### 市场差异化

Google Drive / Dropbox 提供了协作但没有 AI。Notion / Coda 提供了 AI 但不是文件存储。aero-vault 可以占据**「AI 增强的文件协作」**这个空白位置：上传 PDF → 自动提取关键信息 → 分享给同事 → 同事可以直接在评论中问 AI 关于文件的问题。这是 Google Drive + Gemini 正在做的方向，但 aero-vault 在私有化部署和 API 可编程性上有天然优势。

---

## 总结：优先级与工程估算

| 优先级 | 方向 | 核心价值 | 建议时机 | 工程估算 |
|--------|------|---------|---------|---------|
| **P0** | AI Content Enrichment & Write-Back | 从「检索平台」升级为「智能处理平台」 | 下一轮 Sprint | 3-4 周（核心）+ 2-3 周（用户自定义规则） |
| **P0** | Vector & Embedding Platform API | 打开外部生态集成，形成网络效应 | 与方向 1 并行 | 2-3 周（核心 API）+ 2 周（SDK 更新） |
| **P1** | Multi-Modal Content Understanding | 差异化引擎，触及非文本内容场景 | 方向 1+2 之后 | 4-6 周（OCR）+ 4-6 周（音频/视频） |
| **P1** | Event-Driven User Functions | 从基础设施升级为可编程平台 | 方向 3 可独立排期 | 4-5 周（WASM runtime）+ 2 周（管理 API + UI） |
| **P2** | Collaborative Workspace & Sharing | 用户体验跃升，打开协作市场 | 里程碑型项目 | Phase 1：2 周，Phase 2：4 周，Phase 3：6 周 |

**战略建议**：

前两个方向（AI Content Enrichment + Vector Platform API）是**同一条主线的两面**：让 AI 管线从「内部只读」变成「开放读写」。它们是 aero-vault 从「带 RAG 的对象存储」进化为「AI-Native 内容智能平台」的关键一跃。建议作为下一阶段的**核心主题**集中投入。

Multi-Modal 和 User Functions 是**支撑性能力**：前者扩展 AI 管线能处理的内容类型，后者让平台变得可编程。可以根据资源情况分阶段推进。

Collaborative Workspace 是一个**独立的产品方向**，适合在核心平台能力稳固后作为一个里程碑来推进，不必与前三者争抢工程资源。

---

## 附录：全局边界情况扫描补充

除了上述五个独立方向外，代码扫描过程中发现以下散布在各处的边界疏忽，虽然不足以成为独立方向，但对生产稳定性有实际影响：

| 问题 | 位置 | 影响 |
|------|------|------|
| **ETag 是 MD5 但未校验** | `storage/local.go` 生成 ETag 为 MD5，`file_crud.go` 校验 `Content-MD5` 但未存储 ETag 到 metadata 供后续校验 | 无法实现端到端完整性验证 |
| **`/_/` 前缀无保留** | `storageKey(tenant, bucket, key)` 不保留 `/` 前缀的 key，S3 路径风格 `/bucket/key` 被转为 `tenant/bucket/key` | 可能影响以 `/` 开头的 S3 兼容客户端 |
| **Job 重试没有指数退避上限** | `jobs.go` 的 `max_attempts` 控制重试次数但无最大退避间隔限制 | 退避时间可能增长到数天（取决于实现） |
| **ListObjects 翻页使用 marker offset** | `sql_objects.go:ListObjects` 的 LIMIT/OFFSET 翻页在大偏移量时性能退化 | 建议改为 keyset pagination（WHERE id > last_id LIMIT N） |
| **WebUI 的 API key 明文在 URL 参数中** | `index.html` 提交表单时 API key 以查询参数传递 | 日志/浏览器历史会记录 API key；建议改为 Header |
| **无优雅关闭时事件丢失量化** | `bus.Close()` 后未发布的事件数量不可观测 | 新增 `events_lost_total` 计数器 |
| **MCP 工具列表硬编码** | `mcp/server.go:listTools` 的 switch 分支与 `dispatch` 分支未共享注册表 | 添加新工具需要修改两处 |
| **软删除对象恢复时不触发事件** | `RestoreObject` 成功后未 `Publish` 事件 | 订阅者（indexer、antivirus）感知不到恢复 |
| **Bucket logging 配置无实际写入** | `SetBucketLogging` 已实现但没有任何代码实际写入 access log | 配置存在但无效果（v21 已指出） |
| **Tenant 删除未级联清除数据** | `DeleteTenant` 仅删除 tenant 行，不清理 objects/chunks/events | 手动清理残留数据的运维负担 |
