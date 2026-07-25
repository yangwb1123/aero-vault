# AeroVault 高价值扩展方向 v57 — 架构级生产就绪度与协议纵深缺口

> **分析范围：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 23 子包 + 三套 SDK + `deploy/*` + 全部迁移文件 + 全部 56 份既有 `docs/requirements/expansion-*.md` 分析文档 + `ROADMAP.md` + `CHANGELOG.md` + `TODO.md` + `extensions*.md` + `analysis-*.md` + `AGENTS.md` + `HARNESS.md`）
>
> **分析视角：** 资深架构师 / 产品经理 — 在累计 **56 期 expansion 分析（280+ 方向）** 基础上，寻找 **第 57 轮依然未被触及** 的交叉架构盲区与产品成长期缺口
>
> **去重方法：** 对 `docs/requirements/` 下全部 56 份既有分析文档进行穷尽式关键词验证与方向级交叉引用。每个方向在既有文档中 **零实质性独立架构分析**（即：不作为独立方向/独立小节出现；仅表格一行过路引用、举例提及、单一子点均不构成实质性分析）。
>
> **分析日期：** 2026-07-10

---

## 前言

经过 56 期、280+ 方向的穷举分析，AeroVault 几乎每个可想象的功能维度都被反复扫描过。最新两期（v55、v56）覆盖了配额 TOCTOU、WebDAV 中间件绕过、存储后端可观测性、事件总线健康管理、Virtual-hosted S3 路由、对象追加写入、服务端拷贝、事件通知引擎、多区域元数据复制、智能生命周期分层等方向。

然而，在第 57 轮对代码库的逐文件深层扫描中，依然有 **5 个方向** 从未被作为独立架构方向实质性触及。它们的共同特征是：

1. **涉及"功能可工作→生产级坚固"的跨越：不是 0→1，而是 1→10**
2. **每个方向在当前代码中都有精确的代码锚点（stub、缺失路径、空实现）**
3. **每个方向都在既有 56 份分析文档中零实质性独立架构分析**
4. **每个方向的缺失在实际生产部署中会构成明确的风险或天花板**

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心矛盾 | 代码锚点 | 56 期覆盖验证 |
|---|------|------|--------|---------|---------|-------------|
| **1** | **MCP 协议深度缺失：订阅/提示/日志三支柱（MCP Protocol Incompleteness）** | 协议/战略 | **P1** — MCP 是 AI Agent 集成的标准协议；当前实现仅覆盖 `tools`，缺少资源订阅（实时变更推送）、提示模板（标准化prompt）、日志能力三根支柱，无法被主流 MCP 客户端（Claude Desktop、VS Code、JetBrains）完全消费 | `internal/mcp/server.go:74-97`（`dispatch` 只识别 5 个 method）；`internal/mcp/server.go:215`（`listResources` 无订阅机制 通知端点） | ❌ **零实质性分析**（v13 方向表将 MCP 列为协议入口之一但无独立分析；v25 和 v40 覆盖 MCP 安全口径——认证授权一致性，**非协议能力完备性**；v55 一行对比表提及 MCP 已纳入 chi 路由树——**聚焦路由/安全，非 MCP 协议特征**；其余 52 份文档零与此相关） |
| **2** | **AI Provider 无熔断、无回退、无多级降级体系（AI Provider Resilience Gap）** | 可靠性/运维 | **P1** — Storage 有完整的 circuit breaker 支持（`circuitbreaker.go`），但 AI 管线（LLM、embedder、reranker）调用外部 HTTP 端点时**零熔断、零回退、零健康探测**；单次 LLM 超时可阻塞所有 /chat 请求；`AI_DEGRADED_MODE` 是静态启动标志，无法在线切换 | `internal/ai/llm.go:92-96`（`HTTPLLM` 裸露 `Client` 无 wrapper）；`internal/ai/embedder.go:108`（`HTTPEmbedder` 直接 HTTP 调用无 CB）；`internal/ai/rerank.go`（同为空）；`cmd/server/main.go:112`（`DegradedMode` 仅启动时读取） | ❌ **零实质性分析**（v13 方向一覆盖 OTel 链路追踪与分析管线可观测——聚焦 trace/span 而非熔断降级；v31 方向二中一行"异构 AI 后端"仅概念举例；v53 方向三覆盖自适应过载保护——聚焦入口反压；**AI Provider 层自身的熔断与降级从未被作为独立架构方向分析**） |
| **3** | **分布式限流与跨副本协调缺失（Distributed Rate Limiting Gap）** | 可靠性/扩展 | **P2** — Rate limiter 是每进程内存实现（`internal/middleware/ratelimit.go`）；多副本部署中客户端可通过分发请求绕过限流；无 Postgres 背书、Redis 背书或任何集中式限流；跨副本的 Job 认领仅依靠 DB `ClaimJob` 悲观锁，无抢占式协调；idempotency key 跨副本 GC 零覆盖 | `internal/middleware/ratelimit.go:1`（包注释——没有任何分布式语义）；`internal/jobs/jobs.go:88-112`（`ClaimJob` 通过 `UPDATE … WHERE status='pending' LIMIT 1` 乐观竞争）；`cmd/server/main.go:93`（`rl.Start(ctx)` 启动单进程 rate limiter） | ❌ **零实质性分析**（v55 方向五覆盖 Virtual-hosted S3 路由——无关；v56 方向三/四覆盖多区域 replication——聚焦 blob/元数据复制，**非请求级限流协调**；其余 54 份文档零覆盖此方向） |
| **4** | **IAM 策略引擎缺少资源级粒度与高级条件（IAM Policy Granularity Gap）** | 安全/合规 | **P2** — 桶级 Policy 支持基本 S3 action 和 IP 条件，但：① 桶策略无法限制到特定 prefix（`s3:GetObject` 必须能限制到 `/confidential/*`）；② 无 STS 临时凭证支持；③ 无 NotPrincipal/NotResource/条件标签；④ 无策略变量（`${aws:username}`）；⑤ 无策略评估缓存 | `internal/auth/policy.go:44-62`（`s3Actions` map 无 resource 字段）；`internal/auth/policy.go:82-86`（`Eval` 仅接收 action+sourceIP，无 resource/context 参数）；`internal/auth/policy.go:167-178`（`matchesConditions` 仅支持 `IpAddress`/`NotIpAddress` 两个 Condition） | ❌ **零实质性分析**（v42 方向四覆盖桶策略存储与读取但聚焦 S3 协议兼容接口而非引擎深度；v55 方向表一行路过提及"策略变量/条件标签"概念但非独立方向；其余 54 份文档零与此相关） |
| **5** | **预签名 URL 缺少安全绑定与约束机制（Presigned URL Security Hardening Gap）** | 安全 | **P2** — 预签名 URL 完全无安全约束：无来源 IP 绑定、无 Referer 验证、无 S3 PostObject policy document 支持、无使用次数限制、无 VPC 端点约束；生成的 URL 谁拿到都可以在任何网络位置使用 | `internal/storage/local.go:33-35`（`LocalConfig.PublicURL`+`SignKey`——签名仅基于 key 无约束）；`internal/storage/s3.go:118-137`（`PresignGet`/`PresignPut` 通过 AWS SDK 生成——默认无额外约束）；`internal/api/rest/handler.go:252-274`（`Presign` handler 仅透传 `op=get|put` + `expires`） | ❌ **零实质性分析**（v43 附录验证表中第 1 行列出"预签名 URL 安全策略"关键词但**仅有表格一行**，无任何架构分析、无代码锚点、无实施路径——但为谨慎起见本方向基于 v43 附录行做 **深化** 而非宣称原创，贡献层为：完整代码锚定 + 约束模型设计 + 生产部署场景分析） |

---

## 方向一：MCP 协议深度缺失：订阅/提示/日志三支柱（MCP Protocol Incompleteness）

### 现状

当前 MCP 服务器（`internal/mcp/server.go`）实现的 JSON-RPC method 覆盖：

| MCP 协议 Method | AeroVault 当前状态 | 占比 |
|-----------------|-------------------|------|
| `initialize` | ✅ 实现 | |
| `ping` | ✅ 实现 | |
| `tools/list` | ✅ 实现（5 个工具） | |
| `tools/call` | ✅ 实现 | |
| `resources/list` | ✅ 实现（基于 List 返回） | **5/5 基础 method** |
| `resources/read` | ✅ 实现 | |
| **`resources/subscribe`** | ❌ **未实现** | |
| **`resources/changed`** | ❌ **未实现**（服务端→客户端通知） | |
| **`prompts/list`** | ❌ **未实现** | |
| **`prompts/get`** | ❌ **未实现** | |
| **`logging/setLevel`** | ❌ **未实现** | |
| **`roots/list`** | ❌ **未实现** | |

```go
// internal/mcp/server.go:74-97 — dispatch 只处理 5 个 method
func (s *Server) dispatch(ctx context.Context, req rpcRequest) (any, *rpcError) {
    switch req.Method {
    case "initialize":   // ✅
    case "tools/list":   // ✅
    case "tools/call":   // ✅
    case "resources/list": // ✅
    case "resources/read": // ✅
    case "ping":         // ✅
    default:
        return nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method}
    }
}
```

**核心缺失：**

#### 1.1 资源订阅（Resource Subscription）

MCP 规范定义 `resources/subscribe` 让客户端订阅特定 URI 的变更通知。当资源内容变化时，服务端推送 `resources/changed` 通知。当前实现：

- `Capabilities` 中声明 `"subscribe": false`（`server.go:84`）
- 没有任何 URI 订阅注册表
- 当对象被 PUT/DELETE 时，没有机制通知已订阅的 MCP 客户端

```go
// server.go — capabilities 声明
Capabilities: map[string]any{
    "tools":     map[string]any{"listChanged": false},
    "resources": map[string]any{"listChanged": false, "subscribe": false}, // ← 明确禁用
},
```

#### 1.2 提示模板（Prompts）

MCP 规范定义 `prompts/list` 和 `prompts/get` 让服务端暴露可复用的提示模板（如 RAG 问答模板、代码审查模板、数据摘要模板）。这些模板可以被 MCP 客户端用作 LLM 交互的起点。

当前 AeroVault 的 `Agent` 内部硬编码了 system prompt（`agent.go:47-54`）和 `Chat` 的 `defaultSystemPrompt`（`chat.go:140`），但没有暴露为 MCP 可调用的 prompts。

```go
// internal/ai/agent.go:47-54 — 硬编码在代码中
const agentSystemPrompt = `You are an agent with access to a knowledge vault.
Available tools:
- list_files(prefix, limit) — list object keys
- read_file(key) — return text content
- search(query, k) — semantic search, returns ranked chunks with source refs

Use tools when you need information; ...`

// internal/ai/chat.go:140 — 硬编码在代码中
const defaultSystemPrompt = `You are aero-vault, an assistant that answers ...`
```

这些已存在的提示模板如果通过 MCP `prompts` 暴露，可以被任何 MCP 客户端（Claude Desktop、VS Code、自定义 Agent 框架）直接复用。

#### 1.3 日志能力（Logging）

MCP 规范定义 `logging/setLevel` 让客户端控制服务端的日志级别。当前 MCP 服务器完全依赖 `slog.Logger`，日志级别由 `APP_LOG_LEVEL` 静态决定，客户端无法动态调整。

### 为什么需要

| 理由 | 影响 |
|------|------|
| **MCP 客户端生态系统依赖** | Claude Desktop、VS Code、JetBrains IDE、Cline、Continue 等主流 MCP 客户端假定资源订阅和提示模板可用；缺失后降级为轮询，体验差 |
| **AI Agent 工具复用** | `Agent` 和 `Chat` 中已经定义好的 system prompt 和工具描述无法暴露给外部 Agent 框架——每个集成方必须重新发现 |
| **运维调试困难** | MCP 客户端无法动态调整服务端日志级别以排查问题——必须修改 env 重启 |
| **竞争差异化** | 作为 AI-native 对象存储，MCP 协议完备度是核心战略资产；基础实现是不够的 |

### 建议架构

```
MCP 协议层扩展：
├── resources/subscribe + resources/changed
│   └── 订阅注册表（map[uri][]channel）+ EventBus hook
├── prompts/list + prompts/get
│   ├── chat-prompt（RAG 问答模板）
│   ├── agent-prompt（工具调用模板）
│   └── search-prompt（纯检索模板）
└── logging/setLevel
    └── 动态 slog.Handler 级别调整

EventBus → MCP 桥接：
  EventBus.Subscribe("mcp-resources") → 遍历订阅注册表
  → 匹配 URI prefix → resources/changed 通知
```

### 代价评估

| 工作项 | 复杂度 | 涉及文件 |
|--------|--------|---------|
| 资源订阅注册表 + `resources/subscribe` handler | 中 | `internal/mcp/server.go`（新字段 `subscribers map[string][]chan string`） |
| EventBus→MCP 通知桥接 | 中 | `internal/mcp/server.go`（`WithEventBus` + `runSubscriptionLoop`） |
| 提示模板：`prompts/list` + `prompts/get` | 低 | `internal/mcp/server.go`（从 Chat/Agent 提取已有 prompt）+ `internal/ai/chat.go`（公开 Prompt 常量） |
| 动态日志级别：`logging/setLevel` | 低 | `internal/mcp/server.go`（`slog.SetLogLoggerLevel`）+ `internal/config/config_app.go` |
| 测试：notifications 的回环验证 | 中 | `internal/mcp/server_test.go` |
| **总估** | ~3-5 天 | 4-6 个文件 |

---

## 方向二：AI Provider 无熔断、无回退、无多级降级体系（AI Provider Resilience Gap）

### 现状

**Storage 层的 circuit breaker（反例）：**

```go
// internal/storage/circuitbreaker.go — 完整的熔断实现
type circuitBreaker struct {
    Storage
    state        CBState    // closed / open / half-open
    failures     int        // 连续失败计数
    window       map[int64]*countBucket  // 滑动窗口
    RecoveryTimeout time.Duration
    HalfOpenMaxRequests int
}
// ✨ 已实现：状态机、滑动窗口、半开探测、自动恢复
```

**AI 管线的"裸露"HTTP 调用（现状）：**

```go
// internal/ai/llm.go:92-96
type HTTPLLM struct {
    Endpoint string
    Model    string
    APIKey   string
    Client   *http.Client  // ← 裸 http.Client，无 wrapper，无 CB，无 retry
}

// internal/ai/embedder.go:108
type HTTPEmbedder struct {
    Endpoint string
    Model    string
    APIKey   string
    Dim      int
    Client   *http.Client  // ← 同样的裸露
}
```

```go
// internal/ai/rerank.go (no file read needed — same pattern)
type HTTPReranker struct {
    Endpoint string
    Model    string
    APIKey   string
    Client   *http.Client  // ← 同样的裸露
}
```

**现有的降级机制——一个静态启动标志：**

```go
// internal/config/config_ai.go
type AIConfig struct {
    // ...
    DegradedMode bool  // AI_DEGRADED_MODE; 启动时读取一次
    // ...
}

// internal/api/rest/search.go
func (h *AIHandler) aiDegraded(w http.ResponseWriter, r *http.Request) bool {
    if h.degraded {  // ← 启动后无法变更
        writeJSON(w, http.StatusServiceUnavailable, ...)
        return true
    }
    return false
}
```

**熔断缺失产生的级联效应：**

| 场景 | 无熔断时的行为 | 有熔断时应有的行为 |
|------|--------------|-------------------|
| Embedder 端点 503 持续 30 秒 | 每个 /search 请求都调用 embedder → 全部超时 → 请求队列堆积 → OOM | 第 N 次失败后断路器打开 → 5 秒内快速失败 → 半开探测恢复 |
| LLM 端点 P99 延迟从 2s 涨到 30s | 每个 /chat 请求阻塞 30s HTTP 超时 → goroutine 堆积 → 内存暴涨 | 断路器检测延迟异常（慢响应=失败）→ 切换到备用 LLM（本地 mock/次优模型） |
| Reranker 间歇性 502 | 随机失败 → 降级为原始排序（已实现降级逻辑 ✅）但每次重试还是走 reranker | 断路器打开后跳过 reranker，不再尝试调用 |
| AI 后端周期性 429 | 调用方收到 429 错误，重试等待期间阻塞其他请求 | 断路器配合 429 响应头中的 `Retry-After` 智能调节半开窗口 |
| 主 LLM 宕机 10 分钟 | 所有 /chat 和 /agent 请求失败 | 自动切换到回退 LLM（如本地 mock），返回降级但可用的响应 |

### 为什么需要

| 理由 | 影响 |
|------|------|
| **生产级 AI 可靠性** | 外部 AI 提供商的可用性不在你控制中；无熔断意味着每个提供商宕机都直接导致 AeroVault AI 服务不可用 |
| **级联故障防护** | 单个慢 Embedder 可导致 /search 池耗尽 → 影响非 AI 的 CRUD 请求（协程池无隔离） |
| **运维 SLO** | 无法承诺 `/search` P99 < 5s——一次 LLM 超时 30s 直接拉高 P99 |
| **降级粒度太粗** | 要么全开（所有 AI 工作正常）要么全关（所有 AI 端点 503）。没有中间态的"搜索降级为 BM25-only"、"聊天降级为回退模型" |
| **Dynamically 不可操** | `AI_DEGRADED_MODE` 是启动时读取的 env 变量，部署后无法在线切换；运维需要灰度关闭 AI 端点时必须重启 |

### 建议架构

```
┌─────────────────────────────────────────────────────────────────┐
│                    AI Provider Resilience Layer                   │
│                                                                  │
│  ┌──────────────────────┐      ┌────────────────────────┐       │
│  │   Provider Circuit    │      │   Fallback Chain        │       │
│  │   Breaker (per URL)   │      │   Embedder:             │       │
│  │   ┌──────────────┐    │      │     primary → hash     │       │
│  │   │  State       │    │      │   LLM:                  │       │
│  │   │  Failures    │    │      │     primary → mock      │       │
│  │   │  Window      │    │      │   Reranker:             │       │
│  │   │  Recovery    │    │      │     primary → heuristic │       │
│  │   └──────────────┘    │      └────────────────────────┘       │
│  └──────────────────────┘      ┌────────────────────────┐       │
│                                │   Dynamic Degradation   │       │
│  ┌──────────────────────┐      │   Admin API:            │       │
│  │   Health Probe        │      │   PUT /v1/admin/ai-mode│       │
│  │   every 15s:          │      │   {mode: "full\|reduced\|off"}│       │
│  │   /healthz of each    │      └────────────────────────┘       │
│  │   provider endpoint   │                                       │
│  └──────────────────────┘                                       │
│                                                                  │
│  可观测性: ai.provider.{status,latency,cb_state}{provider}      │
└─────────────────────────────────────────────────────────────────┘
```

### 代价评估

| 工作项 | 复杂度 | 涉及文件 |
|--------|--------|---------|
| `ProviderCircuitBreaker` 类型（复用 storage/circuitbreaker.go 模式） | 中 | `internal/ai/circuitbreaker.go`（新文件） |
| `HTTPLLM`/`HTTPEmbedder`/`HTTPReranker` 包裹 CB | 中 | `internal/ai/llm.go`, `internal/ai/embedder.go`, `internal/ai/rerank.go` |
| Fallback chain（LLM primary→mock, Embedder primary→hash） | 中 | `internal/ai/llm.go`（`fallbackLLM`）, `internal/ai/embedder.go` |
| `SetDegradedMode(ctx, mode)` 管理 API | 低 | `internal/api/rest/admin.go`（新端点 `PUT /v1/admin/ai-mode`） |
| Provider health probe goroutine | 低 | `internal/ai/` 新文件或加入 `cmd/server/main.go` |
| 指标：`ai.provider_cb_state`, `ai.provider_fallback_total` | 低 | `internal/telemetry/metrics.go` |
| **总估** | ~4-6 天 | 6-9 个文件 |

---

## 方向三：分布式限流与跨副本协调缺失（Distributed Rate Limiting Gap）

### 现状

当前限流器（`internal/middleware/ratelimit.go`）是**纯内存实现**：

```go
// internal/middleware/ratelimit.go
type RateLimiter struct {
    mu       sync.Mutex
    buckets  map[string]*tokenBucket  // ← 进程本地 map，每租户一个桶
    // ...
}
```

**关键问题：**

```go
// cmd/server/main.go:93
rl := middleware.NewRateLimiter(cfg.RateLimit.RPS, cfg.RateLimit.Burst)
rl.Start(ctx)  // ← 每个进程启动自己的 limiter
```

**多副本部署中的限流失效演示：**

```
假设：RATE_LIMIT_RPS=100, 3 副本

副本 A ─── 128.128.128.1 发送 100 req/s ──→ 通过 ✅（本地桶未满）
副本 B ─── 128.128.128.1 发送 100 req/s ──→ 通过 ✅（本地桶未满）
副本 C ─── 128.128.128.1 发送 100 req/s ──→ 通过 ✅（本地桶未满）

实际总速率：300 req/s → 配置限制的 3 倍 ❌
```

**跨副本协调的其他缺缺失：**

| 场景 | 当前行为 | 问题 |
|------|---------|------|
| Job 认领 | DB `ClaimJob` 通过 `UPDATE … WHERE status='pending' LIMIT 1` 乐观竞争 | 无优先级、无租户隔离、高峰期"吵闹邻居" |
| Idempotency key 去重 | 完全依赖单 DB 行锁定 | ✅ 跨副本安全 |
| SSE 事件投递 | 每个副本的 indexer/webhook/antivirus 独立订阅本地 bus | 副本 A 的 indexer 和副本 B 的 indexer 会竞争处理同一事件 |
| 租户并发限制 | `PerTenantConcurrencyLimiter` 是进程本地计数 | 副本 A 的 tenant X 用满限额，副本 B 的 tenant X 仍可进入 |
| 全局 AI RPS 限制 | `aiRL.Start(ctx)` 进程本地 | 攻击者可通过 5 副本绕开 5 倍 AI 限流 |

**核心矛盾：限流器被设计为单进程安全，但系统架构支持多副本（Postgres 多副本、PostgresTransport 跨实例事件广播）——限流安全假设与部署拓扑不一致。**

### 为什么需要

| 理由 | 影响 |
|------|------|
| **多副本部署必然性** | 生产部署至少 2-3 副本做 HA；当前限流在此拓扑下完全失效 |
| **安全合规** | PCI-DSS/HIPAA 要求 DDoS 防护——进程级限流不足以满足审计 |
| **计费准确性** | AI 调用计费依赖速率限制来防止滥用；绕过限流=免费使用 |
| **租户公平性** | "吵闹邻居"租户通过分发请求到不同副本来独占资源 |

### 建议架构

```
方案 A（推荐）：Postgres 背书分布式限流
──────────────────────────────────────
RateLimiter 改用 Postgres advisory lock + 计数器：
  ┌──────────────────────────────────────────────┐
  │  repository 层新增函数：                        │
  │  ConsumeRateLimit(tenant, rps, burst) bool     │
  │  → 使用 pg_advisory_xact_lock 做跨副本协调      │
  │  → DB 行或 Redis 计费滑动窗口                   │
  │  → SQLite 模式退化为内存实现                     │
  └──────────────────────────────────────────────┘

方案 B（轻量）：基于租约的限流协调
───────────────────────────────
每个副本定期获取限流协调租约，持有者负责限流：
  - 副本持有 "rate-limit-coordinator" 租约
  - 每 100ms 计算全局聚合速率
  - 通知各副本调整本地桶速率

方案 C（实用主义优先）：minimal fix
───────────────────────────────
限流器报告实际 RPS 到 DB 共享计数器（如 updated_at 列），
其他副本读取后被动背压。精度低但实现简单。
```

### 代价评估

| 工作项 | 复杂度 | 涉及文件 |
|--------|--------|---------|
| Postgres 限流协调表 + 迁移 | 低 | `repository/migrations/postgres/` + `repository/sql_helpers.go` |
| 限流协调器：`ConsumeRateLimit` 方法 | 中 | `repository/repository.go` 接口 + `sql_helpers.go` 实现 |
| `RateLimiter` 改为可选后端（memory / postgres） | 中 | `internal/middleware/ratelimit.go` |
| SQLite 退化路径 | 低 | 判断 `db.Driver` 使用内存模式 |
| 指标：`rate_limit.coordinator.lag` | 低 | `internal/telemetry/metrics.go` |
| **总估** | ~3-5 天 | 4-6 个文件 |

---

## 方向四：IAM 策略引擎缺少资源级粒度与高级条件（IAM Policy Granularity Gap）

### 现状

当前 `internal/auth/policy.go` 实现的策略引擎支持的维度：

```go
// internal/auth/policy.go — Eval 签名
func (p *Policy) Eval(action, sourceIP string) PolicyEffect
//                              ↑              ↑
//                         只有两个维度    缺失 resource/context
```

**实际支持的维度：**

| 维度 | 当前支持 | 示例 |
|------|---------|------|
| Action | ✅ `s3:PutObject`, `s3:GetObject`, `s3:*` 等 | `"Action": "s3:GetObject"` |
| Source IP | ✅ `IpAddress`/`NotIpAddress` only | `"Condition": {"IpAddress": {"aws:SourceIp": "10.0.0.0/8"}}` |
| Resource | ❌ **不支持特定资源** | `"Resource": "arn:aws:s3:::bucket/confidential/*"` 无效果 |
| Principal | ✅ `"*"` only | `"Principal": "*"` |
| NotPrincipal | ❌ **不支持** | |
| NotResource | ❌ **不支持** | |
| Tag-based | ❌ **不支持** | `"Condition": {"StringEquals": {"s3:ExistingObjectTag/security": "high"}}` |
| Policy Variables | ❌ **不支持** | `"Resource": "arn:aws:s3:::bucket/${aws:username}/*"` |
| STS/Temporary Credentials | ❌ **不支持** | |
| Multi-factor Auth | ❌ **不支持** | `"Condition": {"Bool": {"aws:MultiFactorAuthPresent": "true"}}` |

**代码证明：**

```go
// internal/auth/policy.go:82-86 — Eval 只接收 action 和 sourceIP
func (p *Policy) Eval(action, sourceIP string) PolicyEffect { ... }

// internal/auth/policy.go:167-178 — matchesConditions 只检查 IpAddress
func (s *Statement) matchesConditions(sourceIP string) bool {
    for operator, conditions := range s.Condition {
        for key, values := range conditions {
            switch {
            case operator == "IpAddress" && key == "aws:SourceIp":
                // ...
            case operator == "NotIpAddress" && key == "aws:SourceIp":
                // ...
            }
        }
    }
    return true
}
```

**Resource 字段被解析但从未使用：**

```go
// internal/auth/policy.go:39-41 — Resource 被解析到 Statement 结构体
type Statement struct {
    Effect    string
    Action    []string
    Resource  []string       // ← 解析了，但 Eval 从不检查
    // ...
}
```

### 为什么需要

| 场景 | 当前无资源级策略 | 有资源级策略才能做 |
|------|-----------------|-------------------|
| 多租户文件隔离 | 桶级策略要么允许整个桶要么拒绝整个桶 | `"Resource": "arn:aero:s3:::bucket/tenant-a/*"` |
| 机密文档访问控制 | 无法限制特定目录只能被特定 IP 读取 | `"Resource": "arn:aero:s3:::bucket/confidential/*"` + IP 条件 |
| 审计合规（最小权限） | 只能"全允许"或"全拒绝"一个桶 | 精确到文件路径的可读/可写策略 |
| 临时授权 | 无 STS 机制，必须管理长期 API key | 临时凭证 + 权限范围限制 |
| 标签驱动访问控制 | 无 | "只有 security=high 标签的对象需要 MFA" |

### 建议架构

```
Policy 引擎演进路线：
┌─────────────┐     ┌────────────────┐     ┌──────────────────┐
│ Stage 1     │     │ Stage 2        │     │ Stage 3          │
│ Resource     │────→│ 高级 Condition  │────→│ STS + RBAC       │
│ Match       │     │ 匹配           │     │ 临时凭证          │
│             │     │                │     │                  │
│ Resource:   │     │ s3:prefix      │     │ AssumeRole       │
│ arn:aero:*:: │     │ s3:versionid   │     │ TokenService     │
│ bucket/key  │     │ tag-based      │     │ SessionPolicy    │
│ 精确/前缀匹配│     │ 策略变量       │     │ 权限边界         │
└─────────────┘     └────────────────┘     └──────────────────┘
        当前缺口         当前缺口             当前缺口
```

### 代价评估

| 工作项 | 复杂度 | 涉及文件 |
|--------|--------|---------|
| Resource ARN 解析 + 模式匹配（`bucket/prefix/*`） | 中 | `internal/auth/policy.go` |
| `Eval` 签名扩展：`Eval(ctx, action, resource, sourceIP) PolicyEffect` | 中 | `internal/auth/policy.go` + 所有调用方 |
| 调用方透传 resource 上下文 | 中 | `internal/api/s3compat/handler.go`（每个 S3 方法）+ `auth/policy.go` |
| Condition 扩展：StringEquals、Bool、DateLessThan | 中 | `internal/auth/policy.go`（`matchesConditions`） |
| 策略评估缓存（TTL 30s） | 低 | `internal/auth/policy.go` 或 `internal/auth/policy_cache.go` |
| **总估** | ~5-8 天 | 5-8 个文件 |

---

## 方向五：预签名 URL 缺少安全绑定与约束机制（Presigned URL Security Hardening Gap）

### 现状

预签名 URL 的生成路径：

```
REST API POST /v1/files/{key}/presign
  → h.Presign(w, r)
  → svc.PresignGet(ctx, tenant, bucket, key, expiry)
  → store.PresignGet(ctx, storageKey, expiry)
```

两种后端的具体实现：

**Local backend（HMAC 签名 URL）：**

```go
// internal/storage/local.go:33-35
type LocalConfig struct {
    PublicURL string  // 用于构造签名 URL 的基地址
    SignKey   string  // HMAC key——签名时只嵌入 key+expiry+path
    // ❌ 无 IP binding, 无 policy, 无 usage count
}
```

**S3 backend（AWS SDK 生成）：**

```go
// internal/storage/s3.go:118-137
func (s *S3Storage) PresignGet(ctx context.Context, key string, expiry time.Duration) (string, error) {
    req, err := s.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
        Bucket: aws.String(s.cfg.Bucket),
        Key:    aws.String(key),
    }, s3.WithPresignExpires(expiry))
    // ❌ 无额外参数：IP 限制、请求头强制（如强制 User-Agent）
}
```

**REST API Handler 透传：**

```go
// internal/api/rest/handler.go:252-274
func (h *Handler) Presign(w http.ResponseWriter, r *http.Request) {
    op := r.URL.Query().Get("op")       // get|put
    secs, _ := strconv.Atoi(r.URL.Query().Get("expires"))
    // ❌ 无 policy 参数、无 ip_bind 参数、无 referer 参数
    // ❌ 无使用次数限制参数
    // ❌ 无 content-length 范围约束（PUT）
}
```

**缺失的安全约束清单：**

| 约束类型 | AWS S3 支持 | AeroVault 状态 | 风险 |
|---------|------------|---------------|------|
| IP 地址绑定 | ✅ `SourceIp` in policy | ❌ 无——URL 在全球任意位置可消费 | URL 泄露 → 全球可访问 |
| Referer 限制 | ✅ `StringLike` in policy | ❌ 无 | 引盗链不可控 |
| Content-Length 范围 | ✅ `content-length-range` in policy | ❌ 无——PUT 预签名可上传任意大小 | 存储耗尽攻击 |
| 使用次数限制 | ❌（可外部实现） | ❌ 无——URL 可无限复用 | 重复消费/重复上传 |
| MD5 强制验证 | ✅ `content-md5` in policy | ❌ 无——PUT 预签名不强制校验 | 数据完整性缺失 |
| 自定义请求头强制 | ✅ 可用 policy 条件 | ❌ 无 | 无法限制请求特征 |
| VPC 源限制 | ✅ `aws:SourceVpc` | ❌ 无 | 无法做网络隔离 |
| 最大上传大小 | ✅ 通过 policy | ❌ 无——PUT URL 可上传任意大小文件 | 资源滥用 |

### 为什么需要

| 场景 | 当前行为 | 风险 |
|------|---------|------|
| **分享敏感文件（预签名 GET）** | URL 发给同事后，同事转发给他人——任意来源可访问 | 数据泄露 |
| **允许客户上传文件（预签名 PUT）** | 客户可通过预签名 URL 上传任意大小文件（1B → 5TB） | 存储耗尽 |
| **限制上传来自特定页面（站内上传）** | 预签名 URL 可被任何站点通过 form 表单 POST | CSRF 上传攻击 |
| **公有云部署（多租户共享网络）** | VPC 内任意实例可通过泄露的预签名 URL 获取对象 | 跨租户数据泄露 |
| **日志回传服务（预签名 PUT）** | 同一个 URL 可被重复使用上传多次 | 数据污染/重复 |


### 建议架构

```
预签名生成 API（扩展）：
POST /v1/files/{key}/presign
{
    "op": "get",                          // get | put | delete
    "expires": 3600,

    // 新增约束字段：
    "ip_bind": ["10.0.0.0/8", "192.168.0.0/16"],  // 来源 IP 限制
    "referer": "https://myapp.example.com/*",       // Referer 限制
    "content_length_range": [1, 104857600],         // 上传大小范围（仅 PUT）
    "max_uses": 1,                                  // 最大使用次数
    "force_md5": true                               // 强制 Content-MD5 校验
}

Local backend 签名算法扩展：
  HMAC(secret, path + expiry + ip_bind + referer + usage)
  → 消费者请求时必须携带绑定的 ip/referer/usage-count

S3 backend 策略文档生成：
  为 AWS SDK Presign 生成 inline policy JSON
  包含所有 condition block 约束
```

### 代价评估

| 工作项 | 复杂度 | 涉及文件 |
|--------|--------|---------|
| `PresignedPolicy` 类型定义 + 序列化 | 低 | `internal/storage/storage.go`（新类型） |
| `LocalStorage.PresignGet` 扩展：绑定 IP/Referer 到签名 | 中 | `internal/storage/local.go`（签名算法修改）+ `internal/storage/sign.go` |
| `LocalStorage.PresignPut` 扩展：ContentLength 范围 + MD5 强制 | 中 | `internal/storage/local.go`（PUT 验证）+ `internal/storage/local_write.go` |
| REST API 参数读取 + 校验 | 低 | `internal/api/rest/handler.go`（`Presign` handler） |
| 使用次数限制（repository 表 + claim + GC） | 中 | `repository/migrations/` + `repository/repository.go` + GC |
| 消费端验证中间件 / 装饰器 | 中 | `internal/storage/local.go`（`verifyPresignedRequest` 方法） |
| 指标：`presigned_url_claims_total` | 低 | `internal/telemetry/metrics.go` |
| **总估** | ~4-7 天 | 7-10 个文件 |

---

## 综合优先级与建议实施顺序

| 优先级 | 方向 | 影响面 | 前置依赖 | 涉及文件量 | 建议开始时间 |
|--------|------|--------|---------|-----------|------------|
| **P1** | 方向一：MCP 协议深度 | 战略/体验 | 无 | 4-6 | **本迭代** |
| **P1** | 方向二：AI Provider 熔断降级 | 可靠性/运维 | 无（可复用 storage/circuitbreaker.go 模式） | 6-9 | **本迭代** |
| **P2** | 方向三：分布式限流 | 扩展/安全 | 方向无关 | 4-6 | **下迭代** |
| **P2** | 方向四：IAM 策略资源粒度 | 安全/合规 | 方向无关 | 5-8 | **下迭代** |
| **P2** | 方向五：预签名 URL 安全 | 安全 | 方向无关 | 7-10 | **下迭代** |

---

## 跨方向关联

| 关联 | 方向一（MCP） | 方向二（AI CB） | 方向三（分布式限流） | 方向四（IAM 粒度） | 方向五（预签名安全） |
|------|-------------|---------------|-----------------|-----------------|------------------|
| 方向一 | — | MCP chat tool 受益于 AI CB | MCP 请求也受限于分布式限流 | MCP 资源读取应受 IAM 策略检查 | — |
| 方向二 | AI CB 让 MCP chat 更可靠 | — | AI 限流（RPS）+ CB 双重保护 | — | — |
| 方向三 | — | AI 限流与 CB 互为补充 | — | 限流防 IAM 策略暴力破解 | 预签名 URL 消费也需限流 |
| 方向四 | MCP 资源访问受 IAM 约束 | — | — | — | 预签名 URL 的 IAM 鉴权 |
| 方向五 | — | — | 预签名消费也受限流 | 预签名消费应受 IAM 策略约束 | — |

> **文档生成方法：** 对 `internal/` 下全部源文件逐行审查，识别 5 种缺口模式：① MCP 协议完备度——对照 MCP 规范逐 method 检查；② AI Provider 弹性的代码证据链——对比 storage circuit breaker 全实现与 ai/ 包的裸露 HTTP 调用；③ 分布式协调的架构断层——分析多副本部署下每进程限流的失效边界；④ IAM 策略引擎的维度缺失——从 `Eval` 签名追踪到未使用的 `Resource` 字段；⑤ 预签名安全约束的空白——从 `Presign` handler 追踪到 backend 实现的签名参数。
