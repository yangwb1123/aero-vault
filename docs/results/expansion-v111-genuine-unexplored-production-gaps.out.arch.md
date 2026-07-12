以下是我的架构分析。

---

# 架构分析：认证盲区、AI 持久性、内容预览与运营治理

## 一份针对现有 124 号分析文档的独立架构审查

---

## 1. 架构评估

### 1.1 当前架构的优势

**端口适配器分离经过实战检验。** 该项目拥有一个整洁的六边形架构，其中 `FileService` 是核心控制器，协议适配器（REST、S3、MCP、WebDAV）在其周围保持轻薄。我在代码验证中确认了以下几点：

- 中间件链 `applyMiddleware` 实际包裹了**整个** `buildRouter` 处理程序 —— 包括 `buildDispatcher`，它处理 WebDAV 分发。这意味着**在代码层面，WebDAV 和 MCP 都确实经过了 auth 中间件**。然而，如下所述，这里存在一个更微妙的协议级不匹配问题。
- 存储和后端的可插拔接口（`storage.Storage`、`repository.Repository`）定义明确，并通过合约性测试强制执行
- 默认的 SQLite + local FS 基线使得单元测试真正实现零网络/零 Docker，这符合 AGENTS.md 中的 I5/I6 约束

**事件驱动架构成熟。** 事件总线将对象变更事件传递给多个消费者（webhook、防病毒、复制、索引器），采用持久化重试和退避策略。这在没有完整消息队列基础设施的情况下是务实的选择。

**通过 Opt-in 实现安全默认。** AI 索引、pgvector、Qdrant、事件、集群、保留策略 —— 一切都通过标志控制，默认关闭。这使得基线启动路径简单且可预测。

### 1.2 架构债务与局限性

**债务 1：协议适配器的认证处理存在不对称性。** 让我提供一个比文档更准确的表述，该文档声称 MCP/WebDAV "完全绕过" auth 中间件。经过代码验证，**认证中间件确实处理了所有传入请求**，包括 MCP 和 WebDAV（它们包裹在 `applyMiddleware` 链的后面）。但根本问题是：

| 协议 | 认证工作机制 | 实际故障点 |
|------|-------------|-----------|
| REST | Bearer / ApiKey 头 → `authenticateBearer` | 正常工作 |
| S3 | SigV4 签名 → `authenticateSigV4` | 正常工作 |
| MCP HTTP | 中间件查找 Bearer/ApiKey 头；MCP 客户端通常**不发送这些认证头** | 客户端-中间件不匹配：中间件会尝试认证，但 MCP 客户端（以及 Claude Desktop）通常期望通过 JSON-RPC 请求体传递凭证（除非在传输级别进行配置） |
| MCP stdio | 无 HTTP；因此在设计上永远不会发送认证头 | 从根本上无法通过 HTTP 承载认证信息 |
| WebDAV | 中间件查找 Bearer/ApiKey；macOS Finder / Windows Explorer 使用 **Basic Auth**，当前中间件不支持 | Basic Auth 提取失败 → `extractToken` 返回空 → 导致 401 |

因此，问题不在于中间件被绕过，而在于**认证凭据分派层**不理解 MCP 和 WebDAV 客户端的凭据承载能力。这是一个协议适配器设计的缺口，而不是中间件的缺口。

**债务 2：BM25 索引缺少生命周期管理。** `BM25` 结构体没有 `Save`/`Load`/`Version` 方法。启动时的 `BuildFromRepo` 在规模上是 O（对象 × 分块），并且与 `AddDocument` 的并发事件处理之间存在竞态条件（`BuildFromRepo` 持有写锁，阻塞索引器）。这是架构架构性债务 —— 该类型应当支持其自身的序列化生命周期。

**债务 3：Web UI 是 API 调试器，而非产品。** 282 行的单页应用仅通过 `JSON.stringify` 显示响应。图像、PDF、Markdown 和媒体文件的端点已存在（thumbnail、`GET /v1/files/{key}`，Content-Type 已保留），但前端没有任何内容类型感知的渲染逻辑。

**债务 4：Webhook 状态模型违反了有限状态机约定。** `succeeded bool` 对于三种状态有五种含义 —— 相关代码注释明确承认了这种语义污染。2026 年，没有理由不添加一个简单的 `status TEXT` 列。

**债务 5：Auth Scope 模型未考虑十租户级管理。** 仅有三种 scope（`read`、`write`、`admin`），没有 `self-service` 或 `tenant-admin` 的概念。这使得平台模型呈现二元化：要么你是管理员（可以管理所有内容），要么你不是（只能读写对象，不能创建密钥或查看配额）。

### 1.3 关键设计决策的评估

| 决策 | 评估 | 建议 |
|------|------|------|
| `buildDispatcher` 在 chi 外部处理 WebDAV | 设计合理：WebDAV 的路径结构（`/webdav/{path}`）与 chi 的路由模型不匹配。但凭据分派有缺口 | 在 dispatch 层添加 Basic Auth → Bearer 转换 |
| BM25 在进程内存中构建 | 对于 MVP 可以理解，但缺少持久化出口是一种债务 | 在 `BM25` 上添加 `Save/Load/LoadOrBuild` 接口，作为正式的 SPI |
| `succeeded bool` 用于死信 | 有意识的权衡以避免模式迁移，但一年多的生产经验表明应该已经修复了 | 简单的迁移：添加 `status TEXT`，弃用 `succeeded`，没有无法修复的问题 |
| Auth 中间件在 chi 之外的统一处理 | 强大的安全模型 —— 一道大门。但凭据提取是协议相关的 | 添加一个 `CredentialExtractor` 接口，根据协议分发到不同的提取策略 |
| Web UI 作为零依赖的独立 HTML 页面 | 对于 MVP 部署来说可以理解，但在产品上不可接受 | 添加前端构建步骤或 CDN 分发模型；即使无框架，也需要处理程序来管理内容类型感知的渲染 |

---

## 2. 扩展方向

除了原始文件中的 5 个方向外，我还确定了 5 个高价值架构扩展方向，这些方向要么未被覆盖，要么需要更深入的处理。

### 方向 A：统一凭据提取层（对方向 1 的深入处理）

**为何需要：** 原始文件正确地将方向 1 识别为安全风险，但低估了解决方案。这不仅仅是"移动 MCP/WebDAV 到中间件链中"（它们已经在链中），而是每个协议在凭据承载能力上存在根本差异：

- REST：Bearer 令牌 / API Key 头（已处理）
- S3：SigV4 签名（已处理）
- WebDAV：Basic Auth（未处理）
- MCP HTTP：JSON-RPC 体内部或 HTTP 头（取决于客户端能力）
- MCP stdio：Unix socket 凭证或环境变量（根本无 HTTP）

**核心挑战与技术难点：**
- 多个凭据来源具有不同的安全性：Basic Auth 将凭据以明文形式发送（仅 HTTPS 可接受），Bearer 更安全，SigV4 避免传输密钥
- 部分协议（WebDAV）的客户端无法更改其凭据行为
- 在协议级凭据（HTTP 头）和业务级凭据（JSON-RPC 参数）之间建立映射

**预期的架构变更：**

```
type CredentialExtractor interface {
    Extract(r *http.Request) (token string, source string, ok bool)
}

// 注册表针对每个协议
extractors := map[string]CredentialExtractor{
    "bearer":   &BearerExtractor{},
    "api_key":  &APIKeyExtractor{},
    "basic":    &BasicAuthExtractor{},
    "sigv4":    &SigV4Extractor{reg.sigv4},
    "mcp_body": &MCPBodyExtractor{}, // 从 JSON-RPC 体中提取
}
```

**对现有系统的影响：**
- 影响范围小：仅影响 `auth_middleware.go` 中的 `extractToken`。将其替换为可配置的提取器链
- 向后兼容：默认行为保持不变（仅检查 Bearer/ApiKey）
- `basic` 提取器需要 HTTPS 守护：如果是非 TLS，则记录警告

### 方向 B：搜索索引生命周期框架（对方向 2 的深入处理）

**为何需要：** BM25 在内存中，向量搜索有三种后端（内存暴力搜索、pgvector、Qdrant），RAG 聊天使用 LLM —— 但每个组件都自行管理其生命周期。需要有一个**形式化的搜索索引生命周期接口**：

| 阶段 | 当前 BM25 | 当前向量 | 理想统一方式 |
|------|-----------|---------|------------|
| 构建 | `BuildFromRepo`（阻塞） | 通过索引器逐个分块构建 | `Build(ctx) error` |
| 加载 | 无 | 无（重启后向量索引在 DB 中保留） | `Load(ctx) error` / `LoadOrBuild(ctx) error` |
| 保存 | 无 | 受向量 DB 支持 | `Save(ctx) error`（对于内存后端可选） |
| 版本控制 | 无 | 无（模型漂移在 `search.go` 中处理） | `Version() string` + 迁移逻辑 |
| 健康检查 | 无 | 无 | `Healthy(ctx) bool` |

**核心挑战与技术难点：**
- `termFreq` 的序列化大小：对于大型文档集可达 100-500MB，需要高效的编码（gob 或 protobuf）和增量 checkpoint 策略
- 版本迁移：如果 BM25 算法发生变化（`k1`、`b` 参数或 tokenization 规则），旧状态必须可重建
- 竞态条件：`BuildFromRepo` 锁定写锁并阻塞 `AddDocument`。基于 checkpoint 的增量构建更加健壮

**预期的架构变更：**

```go
// 在 ai 包中
type IndexLifecycle interface {
    Build(ctx context.Context, repo repository.Repository, tenant string) error
    Save(ctx context.Context, sink repository.BM25StateSink, tenant string) error
    Load(ctx context.Context, source repository.BM25StateSource, tenant string) (bool, error)
    Version() uint64
}
```

**对现有系统的影响：**
- `BM25` 结构体扩展接口，不影响客户端代码
- 需要一个新的迁移，但在方向 2 中已经预估
- 主启动流程从 `BuildFromRepo` → 切换到 `LoadOrBuild`

### 方向 C：事件模式注册表 + SQL 化死信队列（方向 4 的完整架构）

**为何需要：** 事件是架构的支柱 —— webhook、防病毒、复制、索引器都依赖它们。但目前没有：
- 形式化的事件模式（生产者/消费者就载荷结构达成一致的方式）
- 事件版本控制（当载荷变更时，旧消费者不会崩溃的方式）
- 作为一等概念的死信队列（具有手动重试、到期、告警功能）
- 跨消费者的事件追踪（"这个对象创建事件是否已被防病毒、复制和索引器处理？"）

**核心挑战与技术难点：**
- 模式演进：Go 结构体是隐式模式。需要像 `event.ObjectCreatedV1` 这样的显式事件类型
- 消费者的死信隔离：一个目标失败不应影响其他目标

**预期的架构变更：**

```go
type EventSchema struct {
    Type    string `json:"type"`    // "object.created.v1"
    Version int    `json:"version"`
    Payload json.RawMessage `json:"payload"`
}

type DeadLetterEntry struct {
    EventID    int64     `json:"event_id"`
    Consumer   string    `json:"consumer"`   // "webhook", "av", "replication"
    Target     string    `json:"target"`      // URL for webhook, "av" for AV
    Status     DLStatus  `json:"status"`      // pending, delivered, dead_lettered, retrying
    LastError  string    `json:"last_error"`
    RetryCount int       `json:"retry_count"`
    CreatedAt  time.Time `json:"created_at"`
}
```

**对现有系统的影响：**
- 方向 4 已经处理了 `webhook_failures` 表的死信语义。此扩展将其泛化为一个框架
- 将 webhook 作为死信模型的第一个消费者，为防病毒和复制故障铺平道路
- 需要新的迁移来创建 `event_schema_registry` 和 `dead_letter_log` 表

### 方向 D：内容处理管线（方向 3 的架构完整版本）

**为何需要：** 当前，内容处理是零散的：
- 缩略图生成存在于 `internal/thumbnail/` 但未被调用
- PII 检测存在于 `internal/ai/pii.go` 但仅由索引器调用
- 病毒扫描存在于 `workers/` 但仅由事件驱动
- 预览（方向 3 的范围）不存在

A **形式化的内容处理管线** 统一了所有这些处理过程：

```
对象创建 → 内容类型分派 → [缩略图生成, 文本提取, PII 扫描, 病毒扫描, 预览生成]
                                    ↓
                              结果合并 → 持久化 → 通知
```

**核心挑战与技术难点：**
- 管线有序性：病毒扫描必须在其他处理之前进行；PII 扫描可以在文本提取之后进行
- 失败语义：扫描失败不应阻止对象存储（非致命），但元数据应反映不完整状态
- 幂等性：如果管线因崩溃而中断，重启后应从中断处继续
- 资源限制：图像缩放和 PDF 文本提取是 CPU 密集型操作，需要资源治理

**预期的架构变更：**

```go
type ProcessingPipeline struct {
    Steps []ProcessingStep // 按顺序配置
}

type ProcessingStep interface {
    Name() string
    Process(ctx context.Context, obj *service.Object, reader io.Reader) (result ProcessingResult, err error)
    IsBlocking() bool // false：失败时记录但不阻断对象创建
}
```

**对现有系统的影响：**
- 影响大：需要一个新包 `internal/pipeline` 和启动时配置
- 向后兼容：默认的管线为空（保留当前行为）
- 现有处理程序（thumbnail、PII、antivirus）各自成为一个步骤

### 方向 E：形式化的租户生命周期管理器（超越方向 5）

**为何需要：** 方向 5 解决了租户自助 API 的问题，但忽略了更大的图景：租户具有生命周期。一个租户被创建（预配置状态），激活（可操作），可能被暂停（由于预算/违规），可能被归档（数据保留）或删除（硬删除）。这些状态转换具有不同的含义和清理需求。

**核心挑战与技术难点：**
- 状态转换的原子性：暂停一个租户需要中止正在进行的复用，关闭活动 WebSocket 连接，并标记所有待处理作业
- 数据保留合规性：归档必须保留数据但使其不可访问；删除必须遵守保留策略
- 过渡期间自动化的清理任务：暂停后 30 天 → 发送警告电子邮件；暂停后 90 天 → 归档

**预期的架构变更：**

```go
type TenantStatus string
const (
    TenantProvisioning TenantStatus = "provisioning" // 创建后立即生效
    TenantActive       TenantStatus = "active"
    TenantSuspended    TenantStatus = "suspended"    // 预算/违规
    TenantArchived     TenantStatus = "archived"     // 数据保留，不可访问
    TenantDeleted      TenantStatus = "deleted"      // 硬删除
)

type TenantLifecycleManager struct {
    transitions map[TenantStatus][]TenantStatus
    hooks       map[TenantStatus][]func(ctx, tenant) error
}
```

**对现有系统的影响：**
- 影响中等：扩展 `TenantRecord` 并添加生命周期管理器
- 向后兼容：现有租户默认状态为 `active`
- 与现有管理员 API 配合：状态变更自动触发清理任务

---

## 3. 接口设计建议

### 3.1 关键接口原则

**原则 1：一致的生命周期管理。** 每个具有进程外状态的组件（BM25、搜索索引、AI 模型、速率限制器）应当实现一个标准生命周期接口：

```go
type Lifecycle interface {
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
    Healthy(ctx context.Context) bool
}
```

目前，没有组件实现这一点。启动是 `main.go` 中的命令性代码。

**原则 2：将凭据提取与凭据验证分离。** `auth.Registry.Middleware()` 目前同时执行这两项操作。应将其拆分为 `CredentialExtractor`（提取令牌方式）和 `Authenticator`（验证令牌方式），在 `Middleware` 中组合：

```go
type CredentialExtractor interface {
    Extract(r *http.Request) (token string, ok bool)
}

type Authenticator interface {
    Authenticate(ctx context.Context, token string) (Identity, error)
}

// 中间件组合多个提取器，然后调用验证器
```

这使得 WebDAV（Basic Auth）可以拥有自己的提取器，MCP（JSON-RPC 体）可以拥有自己的提取器，而无需更改验证逻辑。

**原则 3：内容类型感知的响应媒体类型。** REST API 返回原始字节流并设置 Content-Type。预览层应当实现标准的内容协商模式：

```go
type ContentRenderer interface {
    Render(ctx context.Context, obj *Object, rc io.Reader, params RenderParams) (RenderResult, error)
    SupportedTypes() []string // ["image/*", "text/markdown", "application/pdf"]
}
```

### 3.2 新抽象层

**需要：（a）一个协议注册表**，将协议前缀映射到认证要求，使在 `buildRouter` 中以声明方式配置，而不是命令式：

```go
type ProtocolAdapter struct {
    Prefix   string
    Handler  http.Handler
    Auth     AuthConfig  // required, optional, anonymous, sigv4
    Extractors []CredentialExtractor  // 协议特定的提取器
}
```

**需要：（b）一个搜索索引提供者抽象。** 当前，BM25、pgvector 和 Qdrant 有不同的接口。应有一个 `SearchIndex` 接口，统一 BM25（全词）和向量搜索：

```go
type SearchIndex interface {
    Search(ctx context.Context, query Query) (Results, error)
    Indexer() ChunkSink  // 从索引器接收更新的方式
    Lifecycle            // Start/Stop/Healthy
}
```

### 3.3 向后兼容性

| 变更 | 兼容性策略 |
|------|-----------|
| 新增 CredentialExtractor | 默认提取器列表保留现有行为（Bearer + ApiKey） |
| 新增 `status` 列到 webhook_failures | 添加 `status TEXT` 的迁移；`succeeded bool` 保留旧行为，优先使用 `status` |
| 新增 SearchIndex 接口 | BM25 实现接口；客户端代码从 `*BM25` 切换到 `SearchIndex` |
| 新增租户状态机 | 添加 `status TEXT NOT NULL DEFAULT 'active'` 的迁移；所有现有租户按原样继续工作 |
| MCP 中的 auth 要求 | 通过 `MCP_AUTH_ENABLED` 标志门控，默认关闭，提供迁移窗口 |

---

## 4. 技术选型

### 4.1 新依赖的评估

| 需求 | 候选方案 | 评估 |
|------|---------|------|
| BM25 序列化格式 | **gob**（stdlib）、protobuf、JSON | **gob** 符合 stdlib 优先（I6），对结构体变更优雅。Protobuf 对于跨语言持久化或跨网络同步更为合适。JSON 对于稀疏的 `map[string]map[int]int` 过于冗长。**推荐：** gob |
| Markdown → HTML 渲染 | **goldmark**（Go 原生）、blackfriday | goldmark 是 Go 生态系统中事实上的标准；使用 CommonMark。运行时轻量，零 CGo。**推荐：** goldmark |
| PDF 预览 | **pdf.js**（前端）、poppler（后端） | pdf.js 在前端运行，可嵌入 Web UI，无需后端处理。但如果图像需要后端 PDF → 图像转换，poppler 是更好的选择。**推荐：** Web UI 使用 pdf.js，后端预览使用 poppler |
| 语法高亮 | **Chroma**（Go 原生）、highlight.js（前端） | Chroma 是 Go 原生（Hugo 使用），可用于 MCP 和后端预览。对于 Web UI，highlight.js 在客户端运行。**推荐：** MCP 使用 Chroma，Web UI 使用 highlight.js |
| 速率限制 | 当前：令牌桶（自建） | 自建适合用例。如果需要更复杂的策略（滑动窗口、租户分片），可以考虑 **golang.org/x/time/rate**（已通过 telemetry 导入）。**推荐：** 继续自建；仅在分层 QoS 需要时才引入外部依赖 |
| 配置热加载 | **koanf**、viper、fsnotify | stdlib 优先原则（I6）建议最小化配置重载。对于 Kubernetes 部署，配置重载通常通过滚动更新完成。**推荐：** 不引入；使用环境变量 + 重启 |

### 4.2 自建 vs 采购

| 领域 | 自建 | 采购 | 建议 |
|------|------|------|------|
| BM25 持久化 | `gob` 序列化≤500 行代码 | 引入 Meilisearch/Tantivy | **自建。** 轻量级，符合架构，对于 ≤100K 文档可扩展 |
| PDF 预览 | 使用 `pdf.js` 客户端（免费） | PdfTron 等商业 SDK | **自建。** pdf.js 是免费的，被广泛使用，且足够好 |
| 租户自助 UI | 向现有 Web UI 添加标签页（~300 行） | 集成商业门户 | **自建。** 范围定义明确且轻量级 |
| 跨复制实例的事件同步 | Postgres LISTEN/NOTIFY（免费，已有支持） | NATS / Kafka | **继续使用 Postgres。** 架构已经与 Postgres 配合 LISTEN/NOTIFY 用于密钥缓存失效；扩展用于事件同步是直接的。当事件吞吐量超过 1000/秒时迁移到 Kafka |
| 死信队列 Web UI | 向现有 Web UI 添加标签页 | 商业监控工具 | **自建。** 所需的数据（状态、错误、重试计数）已经存在于 DB 中 |

### 4.3 技术栈决策

| 决策 | 选项 | 推荐 | 理由 |
|------|------|------|------|
| 前端框架 | 无框架 vs 轻量（Alpine/Svelte/Vue） | **保持无框架或 Alpine.js。** 当前 282 行的页面不需要框架。Alpine 如果 Web UI 增长至超过 500 行，将是合适的提升 |
| 后端预览引擎 | 单例 vs 工作者池 | **工作者池。** 预览生成（尤其是 PDF/图像）是 CPU 密集型任务。通过有界工作池与主请求处理程序异步处理 |
| 缓存层 | 内存 vs Redis | **内存。** 对于 ≤10K 租户和 ≤100MB BM25 状态，进程内缓存就足够了。如果需要跨 Pod 缓存失效，引入 Redis |

---

## 5. 实施路线图

### 5.1 优先级评估

我保留了原始文档的顺序但提供了更精细的分解：

| 轮次 | 范围 | 工作量 | 风险 | 价值 |
|------|------|--------|------|------|
| **P0** | 方向 1 基础版（协议认证凭据提取器） | 2 天 | 低 | **关键安全修复。** Basic Auth 提取器 ≤50 行代码。MCP Bearer 头标准化同样简单 |
| **P0** | 方向 1 MCP stdio auth（Unix 凭证/环境变量） | 1 天 | 低 | 安全修复的完整性 |
| **P1** | 方向 4（webhook 死信语义） | 3 天 | 低 | 运营数据质量；简单的迁移 + 状态枚举 |
| **P1** | 方向 2 BM25 持久化（方案 C：文件序列化） | 3 天 | 中 | 对于生产部署至关重要；序列化大小偶尔会导致 OOM |
| **P2** | 方向 4 DLQ Web UI | 2 天 | 低 | 运营可见性；为 Web UI 添加管理标签页 |
| **P2** | 方向 3 短期（Web UI 内容类型感知预览） | 5 天 | 中 | 产品可用性，非技术用户的入门门槛较低 |
| **P2** | 方向 2 BM25 增量（方案 B：changelog 表） | 5 天 | 中 | 减少大型部署的启动时间 |
| **P3** | 方向 5 租户自助 API | 7 天 | 中 | 平台化；需要 scope 迁移 |
| **P3** | 方向 3 长期（MCP 预览工具） | 5 天 | 中 | AI 客户端的代理能力 |
| **P3** | 方向 C：事件模式注册表 | 5 天 | 中 | 面向未来；需要在方向 4 之后 |
| **P3** | 方向 A：统一凭据提取层 | 3 天 | 低 | 重构现有负责；依赖于方向 1 |
| **P3** | 方向 E：形式化的租户生命周期 | 7 天 | 高 | 影响大；需要与其他变更协调 |
| **P3** | 方向 D：内容处理管线 | 10 天 | 高 | 最大的工作量；最好在方向 2 和 3 之后进行 |

### 5.2 阶段和里程碑

**第一阶段：安全与数据基础（2 周）**

```
里程碑 1：协议认证覆盖
- Basic Auth 提取器用于 WebDAV（auth_middleware.go 中的 20 行）
- MCP HTTP：标准化 Bearer 头，更新客户端文档
- MCP stdio：AERO_TENANT 环境变量用于租户选择（没有 HTTP 的 HTTP auth 是不可解决的）
- 验证 curl 测试：

    curl -X PROPFIND http://localhost:8080/webdav/ -u "token:"  
    # → 当注册表启用时返回 401，之前返回 200

里程碑 2：Webhook 语义
- 迁移 0025_webhook_failures_status 添加 status TEXT
- 将 retryOne 中的 MarkWebhookSucceeded 替换为 UpdateWebhookStatus(id, "dead_lettered")
- 添加 ?dead_lettered 过滤到管理 API
- 为 webhook_dead_lettered_total 添加 Prometheus 计数器
```

**第二阶段：AI 生产就绪（2 周）**

```
里程碑 3：BM25 持久化
- BM25.Save/Load 使用 gob 编码
- 启动流程：Load() → 成功 → 使用，失败 → BuildFromRepo() → Save()
- 版本控制：嵌入版本号，在算法变更时触发全量重建
- 竞态条件修复：BuildFromRepo 中的构建时阻止 UpsertObjectChunks

里程碑 4：搜索索引生命周期
- 提取 SearchIndex 接口
- BM25 实现 SearchIndex + Lifecycle
- 基于 changelog 的增量 BM25 构建（如果文件序列化不够快）
```

**第三阶段：产品可用性（3 周）**

```
里程碑 5：Web UI 预览
- 基于 content-type 的条件渲染（image → <img>，markdown → goldmark + sanitize，pdf → pdf.js）
- 列表和搜索结果中的缩略图注入
- 大文件优雅降级（>10MB → "文件过大，无法预览"）
- HTML sanitization（bluemonday 或 DOMPurify）用于 text/html

里程碑 6：MCP 预览工具
- 新增 preview_file(key) 工具，返回结构化预览
- 图像：base64 + 尺寸
- Markdown：渲染后的 HTML（已清理）
- 代码：语言检测 + 语法高亮
- PDF：文本提取 + 页数
```

**第四阶段：平台化（3 周）**

```
里程碑 7：租户自助 API
- 新增 self-service scope
- /v1/me/* 路由组（密钥、配额、webhook、审计）
- Web UI 中的设置标签页
- scope 限制：self-service 密钥不能创建 admin 或 self-service 密钥

里程碑 8：事件基础设施
- 事件模式注册表（object.created.v1、object.deleted.v1）
- 死信日志（消费者不可知，不仅是 webhook）
- 在 Web UI 中添加 DLQ 仪表盘
```

### 5.3 风险与缓解

| 风险 | 可能性 | 影响 | 缓解 |
|------|--------|------|------|
| BM25 持久化损坏数据（版本不匹配） | 中 | 高 | Save() 时包含版本号，Load() 时校验，版本不匹配时回退到 BuildFromRepo |
| WebDAV Basic Auth 在 macOS Finder 中无法正常工作 | 中 | 中 | HTTPS + Basic Auth 是要求；在启用文档中明确记录 |
| MCP 客户端（Claude Desktop）不发送 Bearer 头 | 高 | 高 | 在 stdio 模式中记录 AERO_API_KEY env var；HTTP 模式支持 MCP 传输级别的认证扩展（`_meta.authorization`） |
| 租户自助 API 被非特权密钥访问 | 中 | 高 | self-service scope 必须与 API 密钥创建松耦合：self-service 密钥永远不能创建具有自身 scope 的密钥 |
| Web UI 预览中的 XSS | 中 | 高 | HTML sanitization 是强制性的（bluemonday/DOMPurify）；富文本类型（text/html、text/markdown）必须清理 |
| 内容预览管线的内存使用 | 中 | 中 | 预览大小限制：10MB 用于文本/代码，5MB 用于图像，3MB 用于 PDF。超出时优雅降级 |
| 并发 BM25 保存 + 事件写入导致死锁 | 低 | 高 | Save 实现必须使用 TryLock 或专用协程；`BuildFromRepo` 应该在持有锁之前收集 |

### 5.4 快速验证的合理调整

原始文档的验证 curl 命令很有用，但应对方向 1 进行修正：

```bash
# 方向 1 — MCP 确实经过了 auth 中间件
# 关键测试：当 AUTH_KEYS 已配置且请求中无 Auth 头
curl -v -X POST http://localhost:8080/mcp \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' \
  -H "Content-Type: application/json"
# → 如果 AUTH_KEYS 已配置且无 Bearer 头，预期返回 401
# → 当前，extractToken 返回 ""，导致 401（当注册表已启用时）

# 方向 1 — WebDAV 确实经过了 auth 中间件
# 关键测试：当 AUTH_KEYS 已配置，使用 curl 的 Basic Auth
curl -v -X PROPFIND http://localhost:8080/webdav/ \
  -u "any:thing"
# → 当前行为：extractToken 不理解 Basic Auth 头，返回 ""，导致 401
# → 修复后：BasicAuthExtractor 提取并验证
```

方向 1 的真实问题是**凭据提取不匹配**，而非中间件绕过。当前，`isBypassPath` 不包含 `/mcp` 或 `/webdav/`，因此 auth 中间件确实处理了它们。但 `extractToken` 不理解 Basic Auth（WebDAV）或 MCP 传输级别的凭据。修复方案是向 auth 中间件添加额外的 `CredentialExtractor` 实现，而非更改路由注册。

---

## 总结

原始分析文档在识别这 5 个方向上做得非常出色，并为每个方向提供了可靠的代码级别证据。我的架构审查证实了它们的有效性，但指出了关于方向 1 的根本原因的一个关键修正：这不是中间件绕过，而是**凭据提取不匹配**。WebDAV 和 MCP 请求确实经过了 auth 中间件，但 `extractToken` 不理解它们的凭据格式，导致不可用的 401（而非不安全的 passthrough）。

在架构扩展方向中，我建议 **方向 A（统一凭据提取层）** 作为最高价值的基础设施投资，因为它解决了方向 1 的根本原因，并为协议认证的未来增长做好了准备。**方向 C（事件模式注册表）** 是最高价值的平台投资，将零散的事件消费者统一到一致的基础设施下。

建议的实施顺序本质上是正确的（安全 → 数据质量 → AI 运营 → 平台 → 产品），但方向 1 的实际工作量被高估了 —— Basic Auth 提取器是一个 ≤50 行的变更，可在几个小时内完成，而非 2-3 天。
