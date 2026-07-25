# AeroVault 资深架构师/产品经理视角 — 第 85 轮：跨层集成与平台成熟度盲区

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 全部 23+ 子包，三套 SDK，MCP 双模式，Web UI，48+ 对迁移文件，`deploy/` 配置，Makefile，CI gate）  
> **去重验证：** 对 `docs/requirements/` 下全部 84 份既有分析文档逐方向进行 `grep` 正则交叉验证 + 语义比对 + 代码锚点映射  
> **日期：** 2026-07-11  
> **核心原则：** 选取代码中存在具体锚点、可量化影响、且在前 84 轮分析中**零实质性分析**或**仅有表格行级提及**的跨层集成盲区。每个方向包含产品价值说明、代码锚点、影响分析、实施建议。

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 | 既有分析覆盖 |
|---|------|------|--------|---------|---------|-------------|
| **1** | **基于 Content-Type 的后处理管道（Post-Upload Processing Pipeline）** | 产品/架构 | **P1** — 系统上传对象后无法按 Content-Type 自动触发处理流程。图片不会自动生成缩略图、PDF 不会自动提取元数据、视频不会自动截帧。Thumbnail 生成仅存在于请求时路径，索引器仅做 text→chunk→embed。事件总线 + 作业队列 + ChunkSink 模式均已就绪，但缺少可扩展的 `Processor` Pipeline 抽象层 | `internal/thumbnail/thumbnail.go`（仅请求时调用，非事件驱动）；`internal/ai/indexer.go`（最接近的 pipeline 模式但硬编码 text→embed）；`internal/events/bus.go`（事件系统可用作 pipeline 触发器）；`internal/jobs/jobs.go`（`Pool` + `Registry` 可承载 pipeline 步骤）；`internal/ai/sink.go`（`ChunkSink` 接口是 pipeline 扩展点的范本）；`internal/service/file_crud.go:Put`（写入后通过 `s.sink.Publish` 发射 `EventCreated` — 触发点存在但无 processor 消费） | ✅ **零实质性分析**（v9 方向表一行"允许函数写入前修改内容" —— 聚焦 pre-upload hook 而非 post-upload pipeline，**零代码锚点、零架构模式分析、零实施路径**。其余 83 份 doc 零提及） |
| **2** | **存储后端 Circuit Breaker 状态零可观测性（Dead Code）** | 运维/可靠性 | **P1** — Circuit breaker 内部维护完整状态机（`State()`）和统计信息（`Stats()`），但两者**无任何调用方**。Prometheus/OTel 15+ 领域指标中零存储后端健康指标。运维人员无法知道断路器是否已打开、后端是否降级、故障率是否在上升。恢复时间完全不可观测 | `internal/storage/circuitbreaker.go:117-125`（`State()` 和 `Stats()` 方法定义——**全局零引用**）；`internal/telemetry/metrics.go`（15+ 计数器/直方图——**零存储后端指标**）；`internal/storage/factory.go`（`NewCircuitBreaker` 正确包装；但 `State`/`Stats` 从不用）；`cmd/server/main.go:readyzHandler`（仅 DB ping + 一次 Stat——无断路器健康检查）；`deploy/grafana/`（12 面板仪表盘——零存储后端面板） | ✅ **零实质性分析**（v55 方向三以概念方向标识"存储后端可观测性真空"并提到 CB `Stats()` 零指标暴露——但将 CB 可观测性作为"存储可观测性"大方向的一个子点，无 CB 特定代码锚点分析；v63/v65 补充了 `Health()`/`Capacity()` API 分析但均聚焦**接口定义**而非**CB 已有 State()/Stats() 方法无消费方**的 dead code 问题。**本方向首次以"dead code 回归"视角分析 CB 可观测性**） |
| **3** | **AI Provider 统一弹性层缺失（Circuit Breaker + 连接池 + 超时管理）** | 可靠性/架构 | **P1** — 存储后端有完整的 circuit breaker 支持（`storage/circuitbreaker.go`）和可配置的连接超时/读取超时/写入超时（`TimeoutConfig`），但 AI 提供商（Embedder、LLM、Reranker、Remote Extractor）调用外部 HTTP 端点时使用裸 `http.Client`——无熔断、无连接池、无超时管理。LLM 单次调用超时可阻塞所有 `/v1/chat` 请求；Embedder 后端抖动可能导致整个搜索路径退化 | `internal/ai/embedder.go:108`（`HTTPEmbedder` 用 `&http.Client{}` — 零超时、零连接池）；`internal/ai/llm.go:90-96`（`HTTPLLM` 用 `&http.Client{Timeout: 120*time.Second}` — 有超时但无 CB/连接池）；`internal/ai/rerank.go`（`HTTPReranker` 用 `&http.Client{Timeout: 30*time.Second}` — 同）；`internal/ai/extractor_remote.go`（`RemoteExtractor` 用裸 HTTP——同）；`internal/storage/circuitbreaker.go`（可复用模式——storage CB 架构完整但 ai 包零引用）；`internal/storage/factory.go:NewHTTPClient`（timeout 配置工具函数存在但 ai 包未使用）；`cmd/server/main.go:156-160`（`buildEmbedder`/`buildLLM`/`buildReranker` 均无 CB 包装调用） | ✅ **零实质性分析**（v57 方向二以概念方向标识"AI Provider Resilience Gap"并提到 HTTP client 裸露——在方向上以一句"AI 管线零熔断"标识，无跨 AI provider 逐组件（embedder/llm/reranker/extractor）的分类代码锚点分析、无 CB 模式复用的具体路径、无连接池缺失的量化影响。**本方向为首次以逐 provider 代码锚点 + 可复用模式映射的方式分析 AI Provider 弹性缺失**） |
| **4** | **S3 Batch Operations API 面缺失** | 协议合规/产品 | **P2** — REST API 提供了 `POST /v1/batch/delete` 和 `POST /v1/batch/tag`，但 S3 协议无对应的 Batch Operations API（`POST /{bucket}?delete` 已实现，但无 `POST /?batch` 作业框架）。S3 批量操作（Batch Copy、Batch Restore、Batch Invoke Lambda、Batch Put ACL、Batch Put Tagging）是 AWS S3 的标准大对象管理接口，当前完全缺失。无作业跟踪（Job ID、Completion Report、Notification） | `internal/api/rest/router.go:99-100`（REST 侧 `batch/delete` + `batch/tag` — 仅两个操作，无作业框架）；`internal/api/s3compat/handler.go`（S3 侧有 `deleteObjects` batch delete——**仅此一个批量操作**）；`internal/repository/repository.go`（无 `batch_jobs` 表或相关接口）；`internal/repository/jobs.go`（`jobs` 表可复用——但无 batch 作业类型注册）；`internal/events/bus.go`（事件系统可用于 batch completion notification）；`internal/jobs/jobs.go`（`Pool` 可承载 batch job 执行） | ✅ **零实质性分析**（v25/v27/v31/v42 在 S3 兼容性清单中一行列出"S3 Batch Operations"作为目标项，**零独立分析 S3 批量操作 API 的框架、作业跟踪、完成报告、冲突管理**。REST `batch/delete` + `batch/tag` 的现有实现仅被视为两个端点而非 Batch Operations 框架的种子） |
| **5** | **访问治理三系统隔离：审计、配额、限流各自为政** | 平台工程/多租户 | **P2** — 审计日志（`audit_log`）、租户配额（`TenantQuota`）、速率限制（`RateLimiter`）是三个功能完备但完全独立的子系统。没有统一的操作级访问控制层能回答"此操作的发起者是否在配额内、是否被限流、是否需要审计"。每次 GET 操作无审计记录（`audit_log` 仅 admin 操作）、租户配额定检查不覆盖 RateLimiter 的拦截、RateLimiter 不分操作类型（GET/PUT/DELETE/AI 等价）。在多租户 SaaS 场景下，这三个系统的割裂导致违反策略的行为只能事后拼接日志分析，无法事前执行 | `internal/repository/audit.go`（仅 admin 操作：key add/revoke, tenant create/delete/status, quota set——无 `RecordObjectAccess` 方法）；`internal/repository/quota.go`（`TenantQuota` 仅存储字节和对象数，无 rate limit 状态、无审计集成）；`internal/middleware/ratelimit.go`（`RateLimiter` 令牌桶——不读审计策略、不写审计日志、不关联配额状态）；`internal/service/file_crud.go:preflightQuota`（仅在 PUT 路径做字节级检查——GET/HEAD/LIST/DELETE 无配额检查）；`internal/api/rest/handler.go:Get`（`h.svc.Get` — 无审计、无配额检查）；`internal/service/file_crud.go:Get`（直接 `repo.GetObject` — 无事前审计写入） | ✅ **零实质性分析**（v29 方向表一行列出"按区域/请求类型的细分 rate limit"——聚焦限流细化维度，非三系统集成。v14 方向表一行列出"access control / audit / rate limit / quota 整合"概念——**一行概念无代码锚点**。**零分析 audit/quota/ratelimit 三个已实现系统的整合架构、操作级治理层（Access Governance Layer）的设计与实现路径**） |

---

## 方向一：基于 Content-Type 的后处理管道（Post-Upload Processing Pipeline）

### 现状

当前系统上传对象的流程是：

```
REST/S3/WebDAV/MCP → FileService.Put → 存储后端 → 事件总线 → 索引器（仅 text→chunk→embed）
```

处理管道的种子代码已就位但无抽象层：

| 组件 | 状态 | 作用 |
|------|------|------|
| `internal/events/bus.go` | ✅ 已实现 | PUT 后发射 `EventCreated`，可触发 pipeline |
| `internal/jobs/jobs.go` | ✅ 已实现 | `Pool` + `Registry` 可承载异步处理步骤 |
| `internal/ai/sink.go` | ✅ 已实现 | `ChunkSink` 接口可作为 pipeline `Processor` 的范本 |
| `internal/thumbnail/thumbnail.go` | ✅ 已实现 | 图像缩略图生成——但仅 REST 请求时调用，非事件驱动 |
| `internal/ai/indexer.go` | ✅ 已实现 | 最接近 pipeline 模式——但硬编码为 text→chunk→embed |
| `internal/service/file_crud.go:Put` | ✅ 已实现 | `s.sink.Publish` 触发点存在，但无 processor 注册 |

### 缺少什么

**一个 `Pipeline` 抽象层**，允许注册 Content-Type 维度的 `Processor`：

```go
type Processor interface {
    // Name returns a unique identifier for this processor.
    Name() string
    // ContentTypes returns the list of MIME types this processor handles.
    // Use "*" for all types.
    ContentTypes() []string
    // Process is called after an object is uploaded. The processor reads
    // the object content from store, processes it, and writes results
    // (thumbnails, extracted metadata, transcoded versions, etc.) back
    // as new objects or metadata updates.
    Process(ctx context.Context, obj repository.Object, r io.Reader) error
}
```

### 影响分析

| 场景 | 当前 | 加入 Pipeline 后 |
|------|------|-----------------|
| 用户上传 JPEG 头像 | 缩略图仅在 `/thumbnail?w=256` 请求时生成，首次请求延迟 ≥500ms | 上传即自动生成 256px 缩略图，写入 `thumb_256/` 前缀下，首次请求零延迟 |
| 用户上传 PDF 合同 | 索引器提取文本 → 嵌入 → 可搜索；但合同元数据（页数、作者、标题）不提取 | PDF Processor 提取元数据作为对象 Tags，无需专门 API 调用 |
| 用户上传 MP4 视频 | 无任何处理 | Video Processor 截取关键帧生成 JPEG 封面 → 写为 `cover.jpg` 对象 |
| 用户上传 CSV 数据 | 仅做文本嵌入 | CSV Processor 解析列标题、行数、文件编码 → 作为结构化元数据存储 |
| 用户上传 ZIP 压缩包 | 按原始 blob 存储 | Archive Processor 列出内容清单作为 Tags，或提取小文件 |

### 代码锚点分析

| 位置 | 现有能力 | Pipeline 缺口 |
|------|---------|--------------|
| `events.Bus.Subscribe()` | 可接收所有 `EventCreated` 事件 | 无 Processor 注册表——事件被发出后无消费方查询 Content-Type |
| `jobs.Registry` | 可注册按 job type 区分的 handler | 无 Content-Type → job type 的路由映射 |
| `service.file_crud.go:Put` | 写入后 `s.sink.Publish(Event{Type: EventCreated})` | 事件 payload 包含 `Bucket`/`Key`/`ObjectID`/`ContentType` 但不触发后续处理 |
| `thumbnail.Generate()` | 纯函数：`(r, maxW, maxH) → []byte` | 非事件驱动，无 pipeline 集成 |
| `ai.Extractor` | 文本提取，仅用于嵌入管线 | Processor 接口与其正交——可共存 |

### 实施建议

1. **定义 `Processor` 接口**（+ `ProcessorContext`，含 object ref + 临时文件路径）
2. **创建 `Pipeline` 注册表**（`map[string][]Processor`——key 为 Content-Type pattern）
3. **在 EventBus 消费者中注册 Pipeline Runner**（收到 `EventCreated` → 查 Content-Type → 分发到匹配 Processors）
4. **内置至少两个 Processor**：`ThumbnailProcessor`（移出请求时路径）+ `MetadataExtractor`（PDF 元数据）
5. **计数器**：`pipeline_processed_total{processor, content_type, status}`

### 边界情况

| # | 场景 | 预期行为 |
|---|------|---------|
| B1 | Processor 处理失败（下游超时、OOM） | Pipeline Runner 将失败记录到 `jobs` 表，按 `jobs.Pool` 重试策略重试；`Pipeline.Runner` 自身不 panic |
| B2 | Processor 写同一个目标 key（如 thumbnail）与用户后续 PUT 冲突 | 目标 key 进入版本化（若 bucket 启用版本控制），或 Processor 使用独立命名空间 `_processed/{processor}/{key}` |
| B3 | 大文件 Pipeline（如 4K 视频截帧） | Processor 返回 `ErrWillProcessAsync`，Pipeline Runner 将作业移交给 `jobs.Pool`，立即返回 |
| B4 | Pipeline 处理期间的并发更新 | 对象元数据版本化后 Pipeline 通过 `VersionID` 绑定到特定版本，避免处理与覆盖之间的竞态 |
| B5 | Processor 注册顺序依赖 | Pipeline 按注册顺序串行执行（Processor 可声明 `RunAfter`），出现错误中断后续处理器 |

---

## 方向二：存储后端 Circuit Breaker 状态零可观测性（Dead Code）

### 现状

`circuitBreaker` 实现了完整的 <output → 计数值> 可观测性方法，但**无任何消费者**：

```go
// internal/storage/circuitbreaker.go:117-125
func (cb *circuitBreaker) State() CBState { … }  // ← 全局零调用
func (cb *circuitBreaker) Stats() (state CBState, failures, total int) { … }  // ← 全局零调用
```

`telemetry.RegisterStorageGauges` 暴露 `storage_bytes` 和 `storage_objects`，但**不暴露 `storage_backend_state`、`storage_failure_count`、`storage_total_requests`**。`readyz` 端点仅做一次 `Stat("@healthz/probe")`——无断路器健康检查。

### 为什么重要

断路器 `Stats()` 的 `failures/total` 直接反映后端健康状况。无可见性意味着：

- **故障恢复不可观测**：运维不知道断路器何时打开、打开多久、何时自动恢复
- **容量规划盲目**：无法区分"后端正常但负载高"与"后端故障中"
- **SLO 无法跟踪**：存储后端可用性只能通过 HTTP 请求 5xx 间接估计，无法从断路器层面直接度量
- **告警盲区**：aws S3 慢速退化（连续 4 次超时 → 断路器打开）不会被告警，直到用户投诉

### 代码锚点

| 位置 | 当前状态 | 缺口 |
|------|---------|------|
| `storage/circuitbreaker.go:117` | `State()` 返回 `CBClosed/CBOpen/CBHalfOpen` | 无 Prometheus gauge 注册 `storage_backend_state{backend="local|s3|oss|cos"}` |
| `storage/circuitbreaker.go:125` | `Stats()` 返回 `(state, failures, total)` | 无 `storage_backend_requests_total` + `storage_backend_failures_total` counter |
| `telemetry/prometheus.go` | 无存储后端仪表 | 需注册 gauge 跟踪断路器状态 |
| `telemetry/metrics.go` | 15+ 领域指标 | 需加 `StorageBackendHealth` 扩展点 |
| `cmd/server/main.go:readyzHandler` | 仅 `Stat("@healthz/probe")` | 需加 `breaker.State()` 检查——`CBOpen` 时 `/readyz` 应返回 503 |
| `deploy/grafana/` | 12 面板 | 需加存储后端健康面板（断路器状态 + 请求/失败率 + 延迟） |
| `deploy/prometheus/alerts.yml` | 8 告警规则 | 需加 `HighStorageBackendFailureRate` 告警 |

### 影响分析

| 场景 | 当前行为 | 加入 CB 可观测性后 |
|------|---------|-----------------|
| S3 后端因网络抖动间歇性超时 | 断路器阈值达到后打开 → 请求直接返回 `ErrBackendUnavailable`；运维零感知 | `storage_backend_state{backend="s3"} = 2 (open)` → 告警触发 → 运维 5 分钟内响应 |
| 后端故障恢复后断路器自动闭合 | 用户流量自动恢复，运维零感知 | `storage_backend_state → 0 (closed)` → 告警自动恢复，dashboard 可视 |
| 容量规划：决定是否增加 S3 bucket 并发 | 无数据支撑 | `storage_backend_requests_total{backend, status}` 积累数据 → 容量瓶颈可量化 |
| CI 环境 local 后端磁盘满 | `local.Write` 返回 `unexpected EOF` | 断路器打开后 `/readyz` 返回 503——K8s Probe 自动摘除 pod |

### 实施建议

1. **在 `telemetry/metrics.go` 注册 CB state gauge**：`storage_backend_state`（0=closed, 1=half-open, 2=open）——在 `factory.go` 中创建 breaker 时完成注册
2. **在 `telemetry/metrics.go` 注册 request/failure counter**：`storage_backend_requests_total{backend}` + `storage_backend_failures_total{backend}`——从 `recordOutcome` 中递增
3. **扩展 `readyzHandler`**：读取 `breaker.State()`，若 `CBOpen` 则返回 503
4. **Grafana 面板**：新增存储后端健康面板——state gauge + requests/s + failure/s + CB 状态时间线
5. **Prometheus 告警**：`HighStorageBackendFailureRate`（failure_ratio > 0.1 for 5m）

### 边界情况

| # | 场景 | 预期行为 |
|---|------|---------|
| B1 | 断路器已禁用（`Enabled=false`） | State 固定 `CBClosed`，gauge 不产生有意义数据——应在仪表盘隐藏或显示"N/A" |
| B2 | 后端子状态计数器溢出（int64） | 计数器周期 reset（Prometheus 自动处理） |
| B3 | `readyz` 在断路器打开时返回 503 与负载均衡 | K8s `livenessProbe` 仍用 `/healthz`（仅进程存活），`readinessProbe` 用 `/readyz`（含 CB 检查）——断路器打开时从 LB 摘除 |
| B4 | 多后端共存（如 local + s3 双写拆分） | 每个后端独立断路器 → 独立的 `{backend}` 标签 |

---

## 方向三：AI Provider 统一弹性层缺失

### 现状

当前系统有三个核心 AI Provider（Embedder、LLM、Reranker）和一个辅助 Provider（Remote Extractor），它们的 HTTP 客户端实现状态：

| Provider | 文件 | HTTP Client | 超时 | 连接池 | Circuit Breaker | 重试 |
|----------|------|------------|------|--------|----------------|------|
| HTTP Embedder | `ai/embedder.go` | `&http.Client{}` | ❌ 无（永远阻塞） | ❌ 默认连接池 | ❌ | ❌ |
| HTTP LLM | `ai/llm.go` | `&http.Client{Timeout: 120s}` | ✅ 120s 读超时 | ❌ 默认连接池 | ❌ | ❌ |
| HTTP Reranker | `ai/rerank.go` | `&http.Client{Timeout: 30s}` | ✅ 30s 读超时 | ❌ 默认连接池 | ❌ | ❌ |
| Remote Extractor | `ai/extractor_remote.go` | 裸 HTTP client | ❌ 无 | ❌ 默认连接池 | ❌ | ❌ |

作为对比，存储后端：

| Backend | HTTP Client | 超时 | 连接池 | Circuit Breaker | 重试 |
|---------|------------|------|--------|----------------|------|
| S3 | `storage.NewHTTPClient` | ✅ 可配置（5s/30s/30s） | ✅ `http.Transport` 池化 | ✅ CBConfig | ❌（CB 后 fail-fast） |
| OSS | `storage.NewHTTPClient` | ✅ 同 S3 | ✅ | ✅ | ❌ |
| COS | `storage.NewHTTPClient` | ✅ 同 S3 | ✅ | ✅ | ❌ |

这是一个跨内部架构的不对称缺口：存储层有完整的弹性保障，价值更高（单次调用延迟更短、失败代价更大的）AI 层反而零保护。

### 为什么重要

| 场景 | 当前行为 | 后果 |
|------|---------|------|
| Embedding 服务抖动（500ms → 30s 响应） | 每个 embed 请求阻塞当前 goroutine 30s → 请求线程池快速耗尽 → 全局请求排队 | 搜索 + 聊天全链路降级 |
| LLM 提供商 API 返回 429（Rate Limited） | 无 retry-after 处理，请求返回 500，客户端重试加剧雪崩 | 聊天完全不可用 |
| Reranker 端点 DNS 解析失败 | HTTP 客户端默认超时 30s→每次请求阻塞 30s→代理循环全阻塞 | 混合搜索降级 |
| AI Provider 间歇性不可用（<5% 错误率） | 低于 CB 阈值→每次 绝对多数正常但偶发超时→累积 latency SLO 违规 | 影响搜索/聊天 P99 延迟 |

### 代码锚点

| 位置 | 当前状态 | 缺口 |
|------|---------|------|
| `ai/embedder.go:108`（`NewHTTPEmbedder`） | `Client: &http.Client{}` | 无超时、无连接池、无 CB |
| `ai/llm.go:90-96`（`NewHTTPLLM`） | `&http.Client{Timeout: 120 * time.Second}` | 有超时但无 CB、无连接池配置 |
| `ai/rerank.go`（`NewHTTPReranker`） | `&http.Client{Timeout: 30 * time.Second}` | 同 LLM |
| `ai/extractor_remote.go`（`NewRemoteExtractor`） | 透传裸 HTTP | 无任何保护 |
| `storage/circuitbreaker.go` | 完整 CB 实现 | 可复用——`NewCircuitBreaker` 接收 `Storage` 接口，等效的 `ai.CircuitBrokenEmbedder` 需适配 |
| `storage/factory.go:NewHTTPClient` | `TimeoutConfig` 工具函数 | 可供 AI provider 使用——导入即可 |
| `cmd/server/main.go:158-178`（`buildEmbedder`/`buildLLM`/`buildReranker`） | 裸创建 provider | 创建链中无 CB 包装步骤 |

### 推荐方案

1. **为 AI Provider 创建统一的 `HTTPClientProvider`**，复用 `storage.NewHTTPClient` 的 `TimeoutConfig`
2. **在 `ai/` 包内增加 `CircuitBreaker` 包装器**：`CBEmbedder`/`CBLLM`/`CBReranker`——模式完全复用 `storage/circuitbreaker.go`（适配 `ai.Embedder` 接口而非 `storage.Storage`）
3. **配置化阈值**：`AI_PROVIDER_CB_FAILURE_THRESHOLD` / `AI_PROVIDER_CB_RECOVERY_TIMEOUT`
4. **连接池全局共享**：AI Provider 复用统一的 `http.Client` 实例（带连接池），而非每个 provider 创建独立 client
5. **metrics**：`ai_provider_requests_total{provider, status}` + `ai_provider_failures_total` + `ai_provider_cb_state`

### 边界情况

| # | 场景 | 预期行为 |
|---|------|---------|
| B1 | Embedder CB 打开但 LLM CB 正常 | 搜索返回 503（`search enabled: embedder unavailable`）；聊天仍通过缓存结果或纯 LLM 响应（当聊天不依赖搜索时） |
| B2 | 所有 AI Provider 均不可用 | `AI_DEGRADED_MODE` 自动激活（当前是静态启动标志，改为动态切换） |
| B3 | Provider 恢复后 CB 自动关闭 | 按 `storage/circuitbreaker.go` 模式：`half-open` → 一个成功请求 → `closed` |
| B4 | 连接池耗尽（大量并发 AI 请求） | `ai_provider_requests_total` 告警 + 降级：`/v1/search` 先于 `/v1/chat` 降级 |

---

## 方向四：S3 Batch Operations API 面缺失

### 现状

当前系统的批量操作能力：

| 接口 | 状态 | 说明 |
|------|------|------|
| `POST /v1/batch/delete` | ✅ REST | 逐个 Delete，返回 per-key 结果 |
| `POST /v1/batch/tag` | ✅ REST | 逐个 SetTags，返回 per-key 结果 |
| S3 `POST /{bucket}?delete` | ✅ S3 | 批量删除（XML 格式） |
| S3 Batch Operations | ❌ 完全缺失 | 无 Batch Copy、Batch Restore、Batch Invoke、Batch Put ACL、Batch Put Tagging |

S3 Batch Operations 是一个完整的作业框架，不是简单的端点集合：

```xml
<!-- 请求结构：定义作业 -->
<BatchOperationsJob>
  <Id>job-123</Id>
  <Operation>
    <S3PutObjectCopy>
      <TargetResource>arn:aws:s3:::target-bucket</TargetResource>
    </S3PutObjectCopy>
  </Operation>
  <Manifest>
    <Spec>
      <Format>S3InventoryReportCsv_201808</Format>
      <Fields>Bucket,Key,VersionId</Fields>
    </Spec>
    <Location>
      <ObjectArn>arn:aws:s3:::manifest-bucket/manifest.csv</ObjectArn>
      <ETag>abc123</ETag>
    </Location>
  </Manifest>
  <Report>
    <Bucket>arn:aws:s3:::report-bucket</Bucket>
    <Format>Report_CSV_202308</Format>
    <Enabled>true</Enabled>
    <Prefix>batch-reports/</Prefix>
  </Report>
  <Priority>10</Priority>
  <RoleArn>arn:aws:iam::123:role/s3-batch-role</RoleArn>
</BatchOperationsJob>
```

### 为什么重要

| 场景 | 当前方案 | 问题 |
|------|---------|------|
| 将 10 万个对象从桶 A 复制到桶 B | 遍历 List→逐个 Get→Put | 数小时到数天，不可恢复中断 |
| 批量恢复 5000 个 GLACIER 对象 | 不支持（无 GLACIER restore API） | 无法使用冷存储层 |
| 基于清单文件批量删除/标记 | 每次调用限制在 ~1000 key | 百万级清理需多次分页调用 |
| 迁移后校验（对象级 ETag、大小比对） | 不支持 | 无法自动化验证迁移完整性 |

### 代码锚点

| 位置 | 当前能力 | Batch Operations 缺口 |
|------|---------|----------------------|
| `api/rest/router.go:99-100` | `POST /batch/delete`, `POST /batch/tag` | 无作业框架——当前是同步逐个执行 |
| `api/s3compat/handler.go:deleteObjects` | S3 `?delete` 批量删除 | 仅 DELETE，无其他操作类型 |
| `repository/jobs.go` | 通用 `jobs` 表 | 可复用作为 batch job 存储——需 `batch_jobs` 扩展 |
| `jobs/pool.go` | 通用 Job Pool | 可执行 batch job 步骤 |
| `events/bus.go` | 事件系统 | 可用于 batch completion 通知 |
| `service/file_crud.go:Copy` | 单个 Copy | S3 Batch Copy 可复用此路径 |

### 实施建议（最小可行版本）

1. **扩展 `jobs` 表**：增加 `batch_id`、`total_objects`、`completed_objects`、`failed_objects` 列
2. **新增 `POST /v1/admin/batch`**：定义操作类型 + 对象清单（JSON 数组或 reference to 清单文件）
3. **实现最小操作集**：`copy` + `restore` + `delete` + `set_tags`（复用已有 file_service 方法）
4. **作业状态 API**：`GET /v1/admin/batch/{id}` → 进度、失败记录、预计完成时间
5. **完成报告**：batch 完成后将结果写入目标桶（S3 风格 `Report`）或通过 `audit_log` 记录
6. **S3 兼容端点**：`POST /?batch` 接受 S3 XML 格式请求

### 边界情况

| # | 场景 | 预期行为 |
|---|------|---------|
| B1 | 批处理中途部分对象失败 | 继续执行剩余对象；失败对象列表持久化；API 返回失败详情 |
| B2 | 批处理中对象被并发修改 | 一致性模型：batch 开始时快照对象清单 vs 实时操作（建议快照模式——batch 仅操作清单捕获时已存在的对象） |
| B3 | 批处理作业中的重复对象（清单内重复 key） | 去重：同一 key 在 batch 中只处理一次 |
| B4 | 超大批处理（100 万+ 对象） | 通过 `jobs.Pool` 拆分为多个子作业；支持暂停/恢复 |
| B5 | 批处理完成通知 | 通过 EventBus 发射 `batch.completed` 事件 → Webhook |）

---

## 方向五：访问治理三系统隔离——审计、配额、限流各自为政

### 现状

三个生产关键子系统在代码中完全隔离运行：

```
┌─────────────────┐    ┌─────────────────────┐    ┌───────────────────┐
│ audit_log       │    │ TenantQuota         │    │ RateLimiter       │
│ (admin 操作)     │    │ (bytes + objects)   │    │ (per-tenant 桶)    │
├─────────────────┤    ├─────────────────────┤    ├───────────────────┤
│ GET 对象 → 不审计 │    │ GET → 不检查配额     │    │ GET = PUT = DELETE │
│ PUT → 不审计     │    │ PUT → 仅字节级检查    │    │ = 同一租户桶       │
│ LIST → 不审计    │    │ DELETE → 不释放配额   │    │ 无操作类型权重     │
│ DELETE → 不审计   │    │ LIST → 不检查        │    │ 无桶级限流         │
└─────────────────┘    └─────────────────────┘    └───────────────────┘
```

没有一个系统可以回答："tennat `acme` 在 bucket `payments` 上的 GET 操作是否应被允许？"

### 为什么重要

| 场景 | 当前行为 | 应然行为 |
|------|---------|---------|
| 安全事件响应：谁访问了哪个文件 | 仅 `audit_log` 有 admin 操作，对象读取完全不可见 | 每次 GET/HEAD/LIST 有审计记录（可配置采样率） |
| 某租户突发大量 GET 请求拖慢其他租户 | RateLimiter 按桶限流——昂贵的 GET 大文件与廉价 GET 小文件等权重 | 审计层检测到异常读取模式 → RateLimiter 动态调整该租户权重 |
| 租户删除大量对象释放存储 | 配额显示 UsedBytes 不减少（配额是写入时增加，非实时计算） | DELETE 操作审计记录 + 实时配额重算 |
| 合规审计：需要按文件维度展示访问、存储、限流三视图 | 三系统数据无法关联 | Audit + Quota + RateLimit 通过统一 `(tenant, bucket, key, operation)` 维度关联 |

### 代码锚点

| 系统 | 组件 | 当前状态 | 治理缺口 |
|------|------|---------|---------|
| 审计 | `repository/audit.go` | 仅记录 admin 操作：key/tenant/quota 变更 | 无 `RecordObjectAccess` 方法、无 GET/PUT/DELETE/LIST 审计 |
| 审计 | `api/rest/handler.go:Get` | 直接调用 `svc.Get` | 无审计钩子——`h.svc.Get` 之前无 `AuditObjectAccess(ctx, tenant, bucket, key, "GET")` |
| 配额 | `repository/quota.go` | `TenantQuota` 仅 `MaxBytes`/`MaxObjects` | 无 `MaxReadRequests`/`MaxListRequests`/`MonthlyBudget` 维度 |
| 配额 | `service/file_crud.go:preflightQuota` | 仅 PUT 路径、仅字节和对象数 | GET/HEAD/LIST 无配额检查、无配额"预留"语义 |
| 限流 | `middleware/ratelimit.go` | per-tenant 桶，不分操作类型 | 无 GET vs PUT vs DELETE 加权、无桶级限流 |
| 配置 | `config/config.go:RateLimitCfg` | `RPS`/`Burst`/`AIRPS`/`AIBurst` | 无 `PerOperation` 配置、无 `PerBucket` 配置 |

### 推荐方案

1. **对象访问审计（最小步）**：在 `FileService.Get/Head/List/Delete` 路径上添加可配置的审计钩子，通过环境变量 `AUDIT_OBJECT_ACCESS=true` 启用。写入 `object_access_log` 表：`(tenant, bucket, key, operation, caller, protocol, timestamp)`
2. **操作类型加权限流**：扩展 `RateLimiter` 支持加权 token 消耗——PUT=2, DELETE=2, GET=1, LIST=1, AI Search=5, AI Chat=10。通过 `RATE_LIMIT_WEIGHTS` 配置
3. **GET 配额**：在 `TenantQuota` 增加 `MaxReads`/`MaxDownloads`/`MonthlyReadBudget` 字段，在 GET 路径检查
4. **统一治理中间件**：创建 `AccessGovernance` 中间件（可选，基于 `AUDIT_OBJECT_ACCESS` / `ENFORCE_READ_QUOTA` 等开关），在执行操作前依次检查：权限→配额→限流→审计

### 边界情况

| # | 场景 | 预期行为 |
|---|------|---------|
| B1 | 高安全环境需每对象访问审计 | `AUDIT_OBJECT_ACCESS=true` + `AUDIT_SAMPLE_RATE=1.0` → 每次 GET 写审计行。注意写入吞吐——生产者-消费者模式通过事件队列写入 |
| B2 | 审计写入压力过大（100K QPS 的 GET） | 可配置采样率 `AUDIT_SAMPLE_RATE=0.01`（仅 1% 写入）+ 审计日志 buffer 128 后批量 flush |
| B3 | 配额检查与真实用量之间的 TOCTOU | 参考 `preflightQuota` 模式——当前也是 TOCTOU，但对存储级配额可接受（超配 ≤ 单个并发写入量）。改进：乐观锁 CAS 更新 `used_bytes` |
| B4 | 治理中间件增加请求延迟 | 所有治理检查在 `ctx` 中传递结果，避免重复查询。审计异步写（事件队列），配额和限流同步（O(1) 检查） |

---

## 总体收益总结

| # | 方向 | 预估工作量 | 预期收益 | 风险 | 建议顺序 |
|---|------|-----------|---------|------|---------|
| **1** | 基于 Content-Type 的后处理管道 | **M**（Processor 接口 + Pipeline Runner + 2 内置 Processor，约 6 文件，350 行） | 🟢 打开自动化处理的大门；thumbnail 延迟请求时 → 上传即就绪；拓展生态（用户可自定义 Processor） | 低（纯增量，不影响写入路径；Processor 失败不阻断原始 PUT） | **①** |
| **2** | 存储后端 CB 状态可观测性 | **S**（metrics 注册 + readyz 扩展 + 仪表盘面板，约 4 文件，150 行） | 🟢 CB 状态从 blackbox → visible；`/readyz` 响应后端健康；零编码开销利用现有 `State()/Stats()` | 极低（纯增量，从 dead code 中增加消费方） | **③** |
| **3** | AI Provider 统一弹性层 | **M**（CB 适配器 × 3 + 连接池共享 + 配置，约 5 文件，300 行） | 🔴 Embedder/LLM/Reranker 从裸露 → 全保护；P99 延迟尾部切断（open CB → fail-fast）；与 storage CB 统一运维视图 | 中（CB 误触发可能导致 false-positive 降级——阈值需保守多级） | **②** |
| **4** | S3 Batch Operations API 面 | **L**（迁移文件 + 作业框架 + 端点 × 4 + 报告，约 8 文件，600 行） | 🟠 百万级对象管理成为可能；冷存储 restore 管线基础；与 AWS SDK 完全互操作 | 中（批处理中部分失败的处理语义需要谨慎设计） | **④** |
| **5** | 访问治理三系统隔离 | **L**（迁移文件 + 治理中间件 + 审计钩子 × 4 + 配额扩展，约 8 文件，500 行） | 🟠 审计从 admin-only → 全操作；限流从 flat → 操作加权；配额从存储 → 全访问维度。SOC2/HIPAA 合规能力完整性提升 | 高（GET 审计在高吞吐下可能产生大量写——需采样设计；配额重算与真实用量可能有偏差） | **⑤** |

**建议实施顺序：** 方向 2 → 方向 3 → 方向 1 → 方向 4 → 方向 5

- **方向 2**（CB 可观测性）是最小投入、立即可见的运维收益——仅激活 dead code
- **方向 3**（AI Provider 弹性）是生产就绪的关键——在 AI 流量增长前完成保护
- **方向 1**（处理管道）打开生态大门——Processor 接口是平台化的起点
- **方向 4**（S3 Batch）是批量管理的核心——需要方向 1 的 Job Queue 基础设施经验反馈
- **方向 5**（治理集成）影响面最广、最复杂——放在最后，利用前 4 个方向积累的中间件和事件基础设施经验

---

> **生成方法：** 对 `internal/` 下全部 Go 源文件逐包审查，追踪跨三个以上文件的数据流和调用链，识别现有接口/方法未被消费的"死代码模式"（方向二）以及类功能组件间缺省的集成界面（方向一/三/四/五）。每个方向的去重验证基于对 `docs/requirements/` 下全部 84 份既有分析文档的正则全文搜索 + 逐文档确认。
