# 高价值扩展方向：MCP 工具授权缺口、跨租户运维面板、限流标准化与客户端反馈、事件驱动自动化管线、零停机运维通道

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — 237+ Go 源文件，50 对迁移文件，3 套 SDK（Go/Python/JS），MCP 双模式（HTTP+stdio），Web UI，完整部署配置（Helm/Grafana/Prometheus/OTel），`AGENTS.md`，`HARNESS.md`  
> **去重验证：** 对 `docs/requirements/` 下全部 106 份既有分析文档逐方向进行全文关键词正则 + 代码锚点交叉验证  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。选取**代码中存在明确锚点、有实质性生产运营/产品影响、且在 106 轮既有分析中未被独立深度覆盖**的方向。每个方向包含：产品价值 → 现状与代码证据 → 架构权衡与建议方案 → 边界情况。

---

## 去重验证总表

| 方向 | 既往覆盖情况 | 结论 |
|------|-------------|------|
| **方向一：MCP 工具级授权与审计缺口** | v67 方向三全覆盖 MCP「连接生命周期管控」（per-tool 限流、空闲超时、连接数限制、stdio auth），但**零覆盖 per-tool 授权模型**——哪个 tenant/用户可调用 `write_file` vs 仅 `read_file`？`write_file`/`delete_file` 在 HTTP 模式下继承 auth middleware（但只验证"是否有有效凭证"，不检查"是否有写入权限"），stdio 模式下完全无认证。**正则搜索 `MCP.*scope\|MCP.*role\|MCP.*permission\|MCP.*authoriz.*tool\|tool.*level.*auth\|tool.*write.*protect\|MCP.*rbac\|MCP.*acl`** → 0 独立深度覆盖 | ✅ **全新方向** |
| **方向二：跨租户运维面板缺失——从单租户指标到全局运营视图** | v92 方向一覆盖「Distributed tracing」聚焦请求追踪链路；v106 方向三覆盖「storage cost attribution」聚焦成本归因；v82 方向四覆盖「Tenant CRUD」聚焦租户生命周期管理。但**零分析跨租户聚合运维视图**——超级管理员需要一个面板同时看到所有租户的健康状态、存储用量趋势、错误率热力图、AI 预算消耗、Job 队列深度。**正则搜索 `cross.tenant.*dashboard\|super.admin.*view\|multi.tenant.*dashboard\|aggregate.*tenant.*metric\|global.*admin.*observ\|tenant.*overview.*page\|tenant.*health.*dashboard\|all.*tenant.*status\|租户.*概览\|跨租户.*面板`** → 0 独立深度覆盖 | ✅ **全新方向** |
| **方向三：限流标准化与客户端反馈缺失** | v104 方向四限流协调有单行提及但聚焦「全局与 AI RPS 不同步」；v23 方向三在 3 行中提到「no Retry-After header」和「no standard rate limit headers」。但**零独立架构分析**标准化 rate limit headers、多限流器协调、客户端退避协议。**正则搜索 `Retry-After.*rate\|X-RateLimit\|rate.*limit.*standard.*header\|rate.limit.*response.*header\|rate.*limit.*client.*feedback\|rate.*limit.*coordina\|rate.*limit.*protocol\|429.*header.*规范`** → 仅 v23 表格 3 行提及，**零架构分析** | ✅ **全新方向** |
| **方向四：事件驱动自动化管线——从静默事件到用户可编程规则引擎** | v104 方向二全覆盖「桶通知运行时缺口——持久化但不执行」但聚焦**补齐桶通知到 Webhook 的路由**（单层路由）；v89 方向三 covered「跨协议运营治理」聚焦速率限制；v94 方向四聚焦「事件溯源与不可变日志」。但**零分析构建用户可编程的 event→action 规则引擎**——当事件 X 发生且满足条件 Y 时，执行动作 Z（调用 webhook、复制对象、删除对象、发送通知、启动工作流）。**正则搜索 `event.*action.*engine\|event.*rule.*engine\|event.*driven.*auto.*platform\|if.*this.*then.*bucket\|event.*handler.*user.defined\|event.*processor.*pipeline\|event.*workflow.*engine\|可编程.*事件\|事件.*规则.*引擎\|自动化.*管线`** → 0 独立深度覆盖 | ✅ **全新方向** |
| **方向五：零停机运维通道——维护模式、排空与在线迁移基础设施** | v105 方向五全覆盖「优雅关闭 in-flight 排空」聚焦**进程级别关闭顺序**，但**零覆盖运维生命周期**——读只维护模式、计划内排空与回填、蓝绿配置切换、Zero-downtime schema migration、API 兼容性通告。**正则搜索 `read.only.*mode\|maintenance.*mode\|drain.*mode\|blue.green.*deploy\|zero.downtime.*schema\|maintenance.*window\|planned.*downtime\|schedule.*migration\|online.*schema.*migrat\|滚动.*升级\|蓝绿.*部署\|维护.*模式`** → 0 独立深度覆盖 | ✅ **全新方向** |

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 |
|---|------|------|--------|---------|---------|
| **1** | **MCP 工具级授权与审计：从"有凭证即可操作"到细粒度工具权限** | 安全/合规 | **P1** | MCP `write_file`/`delete_file` 无 per-tool 授权；stdin 模式完全无认证；HTTP 模式仅验证"有有效凭证"不区分读写权限；MCP 调用无操作审计追踪 | `internal/mcp/server.go:80-120`（`dispatch` 到 `callTool` 无任何 `checkPermission` 调用）；`internal/mcp/server.go:175-212`（`toolWriteFile`/`toolDeleteFile` 直接调用 `svc.Put`/`svc.Delete` 无前置权限检查）；`internal/mcp/transport.go:20-60`（`ServeStdio` 无认证逻辑）；`cmd/server/main.go:195`（`r.Method("POST", "/mcp", mcp.HTTPHandler(...))` 依赖全局 auth middleware 但无 MCP 特定授权）；`internal/auth/auth.go:ScopeAdmin`/`ScopeRead`/`ScopeWrite`（scope 定义存在但 MCP 路径不校验）；`internal/auth/auth_middleware.go:30-50`（中间件设置 `scope` 到 context，MCP handler 不读取）；`internal/mcp/server.go:243-250`（`toolReadFile` 调用 `repo.RecordUsage` 记录审计，但 `toolWriteFile`/`toolDeleteFile` 无任何审计） |
| **2** | **跨租户运维面板与全局运营视图** | 运维/可观测性 | **P1** | 管理 API 均为 per-tenant 视角；无超级管理员全局视图查看所有租户的运行状态、存储趋势、错误率、AI 预算消耗；问题排查需要在不同租户之间手动切换 | `internal/api/rest/admin.go:392-420`（`GetConfig` — 返回配置快照但无租户聚合数据）；`internal/api/rest/admin.go:264-280`（`ListTenants` — 返回租户列表但无用量/健康信息）；`internal/repository/sql_buckets.go:BucketStats`（per-bucket 统计）；`internal/repository/quota.go:ListTenantQuotas`（唯一返回所有租户配额+用量的方法——但 admin API 未暴露聚合端点）；`internal/telemetry/metrics.go:15`（`ai_requests_total{tenant}`、`ai_cost_micros_total{tenant}`、`storage_bytes{tenant}`——所有维度按 tenant 标签，Prometheus 可聚合但 admin API 无聚合查询端点）；`internal/repository/repository.go:ListTenants`（仅返回 TenantRecord 列表，无聚合统计）；`internal/repository/jobs.go:JobStats`（返回全局 job 状态统计——唯一全局聚合方法） |
| **3** | **限流标准化与客户端反馈协议** | 可靠性/DX | **P2** | 429 响应不带标准限流头域；客户端无法智能退避；全局 RPS、AI RPS、并发限制器三者独立不协调；无 `X-RateLimit-Limit`/`X-RateLimit-Remaining`/`X-RateLimit-Reset` | `internal/middleware/ratelimit.go:90-95`（`writeRateLimitHeaders` — 仅设 `Retry-After`，无 `X-RateLimit-*` 系列头）；`internal/middleware/ratelimit.go:125-140`（`Middleware` — 拒绝时仅返回 http.Error + Retry-After）；`internal/middleware/middleware.go:80-120`（`ConcurrencyLimiter` — 拒绝时设 `Retry-After: 1`，无标准限流头）；`internal/middleware/middleware.go:160-190`（`PerTenantConcurrencyLimiter` — 同样只设 Retry-After）；`cmd/server/main.go:215-218`（全局 rl + aiRL 两个独立 limiter，无协调逻辑）；`internal/api/rest/sse.go:45-60`（SSE 流无限流头——大量 SSE 连接可耗尽资源）；`internal/repository/jobs.go:Enqueue`（`JOBS_MAX_DEPTH` 超限返回 `ErrQueueFull`——但无 HTTP 标准化错误响应） |
| **4** | **事件驱动自动化管线——从静默事件到用户可编程规则引擎** | 产品/功能 | **P2** | S3 桶通知规则持久化但不执行（migration 0024）；事件总线有完整的生产-消费能力，但用户无法定义"当对象在桶 X 创建且前缀匹配 Y 时执行动作 Z"的自动化规则；Webhook 仅支持单全局 URL | `internal/repository/sql_buckets.go:381-415`（`GetBucketNotifications` + `SetBucketNotifications` + `DeleteBucketNotifications` — 规则持久化完整）；`internal/repository/repository.go:NotificationRule`（`Events []string` + `FilterKey string` + `QueueARN string` — 事件/过滤/目标字段齐全但 `QueueARN` 标注 `"webhook URL or queue ARN"` ——未在运行时解析执行）；`internal/events/bus.go:90-100`（`Publish` — broadcast 给所有 subscriber，零规则路由）；`internal/events/webhook.go`（单 URL 全局 webhook，不读取桶通知规则）；`internal/jobs/jobs.go:Registry` + `Queue`（job 注册+队列机制完整，可作为规则引擎的执行后端）；`internal/repository/repository.go:EventType`（仅 3 种 `created`/`deleted`/`accessed`，后续扩展需要新增 `moved`/`tagged`/`copied` 等事件类型） |
| **5** | **零停机运维通道——维护模式、排空与在线迁移基础设施** | 运维/架构 | **P2** | 配置变更需重启；schema migration 启动时阻塞（单节点 SQLite 场景）；无读只维护模式；无计划内排空与回填；无向后兼容的迁移路径 | `cmd/server/main.go:37-42`（`cfg, err := config.Load()` — 启动一次性加载，运行时不可变）；`cmd/server/main.go:84-86`（`repo.Migrate(ctx)` — 启动时同步迁移，阻塞监听）；`internal/repository/sqlite.go`（SQLite WAL 模式支持并发读但迁移仍阻塞）；`internal/repository/migrations/{sqlite,postgres}/`（50 对迁移文件——单向版本，无回滚自动化）；`cmd/server/main.go:247-268`（`runServer` — `srv.Shutdown(shutdownCtx)` — 关闭但不排空 in-flight 写入）；`internal/middleware/middleware.go:ConcurrencyLimiter`（无维护模式——无法优雅拒绝新请求等待排空后进行维护）；`internal/config/config.go:Validate`（配置在启动时验证一次——无运行时配置重载点）；`internal/api/rest/admin.go:GetConfig`（配置读快照——但无可写配置端点） |

---

## 方向一：MCP 工具级授权与审计缺口

### 产品价值

MCP 是 aero-vault 的 AI Agent 集成核心通道——Claude Desktop、VS Code、JetBrains、以及自定义 Agent 框架都通过 MCP 与系统交互。当前 MCP 暴露了 6 个工具，包括具有破坏能力的 `write_file` 和 `delete_file`，以及消耗真实成本的 `search` 和 `chat`。

| 场景 | 当前行为 | 期望行为 |
|------|---------|---------|
| **只读 Agent**：给分析师使用的 Claude Desktop 仅需查询文件 | Agent 可以调用 `delete_file` 误删数据 | `write_file`/`delete_file` 在只读角色下被拒绝 |
| **CI/CD Agent**：部署流水线中的 Agent 需写入日志文件但不能删除已有文件 | Agent 有完整写+删能力 | 仅允许 `write_file`，拒绝 `delete_file` |
| **终端用户 MCP**：通过 HTTP MCP 访问自己租户的文件 | 通过 auth middleware 后拥有完整 MCP 能力 | 工具调用受 scope（read/write/admin）约束 |
| **SSH 共享主机 stdio MCP**：`aero-vault mcp` 子进程 | 任何能启动该子进程的调用者都有完全文件访问权限 | stdio 模式应验证短生命凭证或降级为只读 |
| **安全审计**：需要知道"谁通过 MCP 删除了哪个文件" | MCP 操作不在 audit_log 中 | 所有 MCP 工具调用写入 audit_log |

### 现状

```go
// internal/mcp/server.go:80-120 — dispatch 到 callTool
func (s *Server) callTool(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
    var p toolCallParams
    if err := json.Unmarshal(raw, &p); err != nil {
        return nil, &rpcError{Code: -32602, Message: "invalid params"}
    }
    switch p.Name {
    case "write_file":
        return s.toolWriteFile(ctx, p.Arguments)   // ← 无权限检查
    case "delete_file":
        return s.toolDeleteFile(ctx, p.Arguments)   // ← 无权限检查
    case "search":
        return s.toolSearch(ctx, p.Arguments)       // ← 无权限检查
    // ...
    }
}
```

**关键缺口：**

1. **无 per-tool scope 校验：** `auth/auth.go` 定义了 `ScopeRead = "read"`, `ScopeWrite = "write"`, `ScopeAdmin = "admin"`，`auth/auth_middleware.go` 将这些 scope 注入 context——但 MCP handler 不读取。

2. **无审计日志：** `toolReadFile` 调用了 `repo.RecordUsage`（审计到 `ai_usage` 表），但 `toolWriteFile` 和 `toolDeleteFile` 完全不记录审计。它们调用 `svc.Put` 和 `svc.Delete`，这些方法会 emit `EventCreated`/`EventDeleted` 事件，但这些事件记录在 `object_events` 表而非 `audit_log`，且不包含调用者身份信息。

3. **stdio 模式无认证：** `ServeStdio` 直接从 stdin 读取 JSON-RPC 消息，无握手认证过程。任何能启动 `aero-vault mcp` 进程的本地用户都拥有完整数据访问权限。

```go
// internal/mcp/transport.go:20-60 — stdio transport
func ServeStdio(ctx context.Context, s *Server, in io.Reader, out io.Writer) error {
    scanner := bufio.NewScanner(in)
    for scanner.Scan() {
        line := scanner.Bytes()
        resp := s.Handle(ctx, line)   // ← 无认证，直接处理
        // ...
    }
}
```

### 架构权衡

**方案：scope-based per-tool authorization + audit trail**

```go
// 工具元数据定义
type toolMeta struct {
    Name        string
    Requires    []string // required scopes, e.g. ["write"]
    Audit       bool     // whether to record in audit_log
    CostScope   string   // "storage" | "ai" | "admin"
}

var toolRegistry = map[string]toolMeta{
    "list_files":  {Name: "list_files", Requires: []string{"read"}, Audit: false},
    "read_file":   {Name: "read_file", Requires: []string{"read"}, Audit: true},
    "search":      {Name: "search", Requires: []string{"read"}, Audit: true},
    "write_file":  {Name: "write_file", Requires: []string{"write"}, Audit: true},
    "delete_file": {Name: "delete_file", Requires: []string{"write"}, Audit: true},
    "chat":        {Name: "chat", Requires: []string{"read"}, Audit: true},
}
```

在 `callTool` 入口插入授权检查：

```go
func (s *Server) callTool(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
    // ...
    meta, ok := toolRegistry[p.Name]
    if !ok {
        return nil, &rpcError{Code: -32601, Message: "unknown tool: " + p.Name}
    }
    // 检查调用者 scope 是否满足工具要求
    if !s.hasScope(ctx, meta.Requires) {
        return nil, &rpcError{Code: -32000, Message: "insufficient permissions"}
    }
    // 记录审计日志
    if meta.Audit {
        s.recordToolAudit(ctx, p.Name, p.Arguments)
    }
    // dispatch...
}
```

**stdio 模式安全增强：**

```go
// stdio 初始化时要求客户端发送认证凭证
{
    "jsonrpc": "2.0",
    "method": "initialize",
    "params": {
        "credentials": "mcp_sk_xxxx..."  // short-lived token
    }
}
```

未提供凭证或凭证无效时，降级为匿名只读模式（仅 `list_files`/`read_file` 可用）。

| 组件 | 改动路径 |
|------|---------|
| `toolMeta` 注册表 | `internal/mcp/server.go` 新增；建立工具→scope→audit 映射 |
| `hasScope` 方法 | 从 context 读取 auth scope（现有 `auth_middleware` 已注入 `scope`） |
| `recordToolAudit` | 调用 `repo.RecordAudit` 写入 `audit_log` 表 |
| stdio 凭证握手 | `ServeStdio` 在 `initialize` 方法中处理凭证；`transport.go` |
| `Server.WithCredentials` | 新增方法，为 stdio 模式配置凭证验证器 |
| 配置 | `config.go` 新增 `MCPAllowAnonymousRead`（默认 true）|

### 边界情况

| 场景 | 行为 |
|------|------|
| HTTP MCP 客户端有 "read" scope，调用 `write_file` | 返回 `-32000 insufficient permissions` error |
| stdio 无凭证 | 降级为只读模式；只暴露 `list_files`/`read_file` |
| 跨租户 MCP：tenant A 的凭证调用 tenant B 的文件 | MCP server 的 `tenantFor` 从 context 获取 tenant，由 auth middleware 保障 |
| `write_file` 创建的文件在 AI 索引中立即可见？ | 当前设计：`svc.Put` → `emit(created)` → Indexer 异步索引。MCP 应以事件驱动，不等待 |
| 审计日志爆炸（高频 `read_file` 调用） | 可配置采样率；`toolMeta` 中 `AuditRate: 0.1` |
| stdio 凭证泄露 | 凭证应为短生命 token（与 `POST /v1/admin/jwt` 共用 JWT 签发机制）；过期自动失效 |

---

## 方向二：跨租户运维面板与全局运营视图

### 产品价值

作为多租户平台，运维人员需要同时管理所有租户。当前系统提供的是"单租户显微镜"而非"全局广角镜"。

| 运维场景 | 当前体验 | 期望体验 |
|---------|---------|---------|
| **容量规划** | 逐租户调用 `GET /v1/usage`，手动汇总 | 一个 API 返回所有租户的 `{storage_bytes, storage_objects, ai_cost, job_depth, request_rate}` |
| **异常检测** | 一个租户的存储使用量突增需要跨租户对比才能发现 | 全局看板显示各租户的存储增长趋势，红色标记异常增长 |
| **问题隔离** | 需要知道"哪个租户消耗了 80% 的 AI 预算" | 租户级 AI 使用量排行榜，按 cost/request/token 排序 |
| **集群健康** | `/healthz` 返回全局 200/503，不知道哪些子系统异常 | 按租户展示存储后端延迟、错误率、job 队列积压 |
| **成本归因** | 无法回答"上周哪些租户的存储增长最快" | 租户存储增长趋势（d/d, w/w, m/m）图表 |

### 现状

```go
// internal/api/rest/admin.go:264-280 — ListTenants
func (h *AdminHandler) ListTenants(w http.ResponseWriter, r *http.Request) {
    recs, err := h.repo.ListTenants(r.Context())
    // 返回: [{TenantID, DisplayName, Status, CreatedAt}]
    // 不含任何用量/健康信息
}
```

```go
// internal/repository/quota.go — ListTenantQuotas
// SELECT tenant_id, max_bytes, used_bytes, max_objects, used_objects, ...
// FROM tenant_quotas
// 这是唯一返回所有租户用量聚合的方法——但 admin API 未暴露该端点
```

```go
// internal/telemetry/metrics.go — 所有计数器带 tenant 标签
ai_requests_total{tenant="acme", model="gpt-4o-mini"}
ai_cost_micros_total{tenant="acme"}
storage_bytes{tenant="acme"}
// Prometheus 可以聚合这些数据，但无管理 API 查询
```

**关键缺口：**

| 能力 | 当前 | 需要 |
|------|------|------|
| 所有租户的存储用量总览 | 仅每个租户 `GET /v1/usage` | `GET /v1/admin/usage` 返回所有租户 |
| 租户存储增长趋势 | 无（Prometheus 有但查询门槛高） | API 返回 7d/30d 趋势 |
| 租户健康状态 | 无 | `GET /v1/admin/health` 按租户展示 |
| 租户排序/过滤 | ListTenants 无分页、无排序 | 支持 `?sort=bytes` `?limit=20` `?status=active` |
| 租户活跃度 | 无 | 最近 24h API 调用次数、存储增量 |

### 架构权衡

**建议新增 admin 聚合端点：**

| 端点 | 返回值 | 实现来源 |
|------|--------|---------|
| `GET /v1/admin/usage` | `[{tenant, storage_bytes, storage_objects, ai_cost_today, job_queue_depth}]` | `repo.ListTenantQuotas` + `repo.SumAICostMicros` + `repo.CountJobsByStatus` |
| `GET /v1/admin/health` | `[{tenant, storage_latency_p50, error_rate_5m, indexer_lag}]` | 聚合现有 telemetry 数据 |
| `GET /v1/admin/usage/history?days=7` | `[{tenant, date, bytes, objects, requests}]` | 新增 `usage_history` 表 + 日快照 job |
| `GET /v1/admin/stats` | `{total_tenants, total_objects, total_bytes, total_ai_cost, active_jobs}` | 聚合 repo 查询 |

**存储用量历史趋势：** 新增一个轻量 daily snapshot 表，由 reconcile 循环每日写入一次：

```sql
CREATE TABLE usage_snapshots (
    tenant_id TEXT NOT NULL,
    snapshot_date TEXT NOT NULL,  -- "2026-07-11"
    bytes INT64 NOT NULL,
    objects INT64 NOT NULL,
    ai_cost_micros INT64 NOT NULL DEFAULT 0,
    requests_24h INT64 NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_id, snapshot_date)
);
```

这个表通过 reconcile 的每日周期填充，提供历史趋势查询而不依赖 Prometheus。

### 边界情况

| 场景 | 行为 |
|------|------|
| 1000+ 租户的大规模部署 | `admin/usage` 支持分页（`?limit=100&offset=0`）和排序（`?sort=bytes_desc`）|
| 新租户无历史数据 | `usage_snapshots` 从租户创建日才开始有数据；之前返回 0 |
| 多 AZ/多 region 部署 | 每个示例独立的 admin 端点；全局聚合由外部监控系统完成 |
| 数据新鲜度 | `usage_snapshots` 是每日快照，非实时；实时数据仍需 Prometheus |

---

## 方向三：限流标准化与客户端反馈缺失

### 产品价值

当客户端被限流时，缺少标准化响应头使得客户端无法实施智能退避策略。每次 429 都是一个黑盒——客户端不知道自己的配额、剩余配额、以及何时可以重试。

| 场景 | 当前行为 | 期望行为 |
|------|---------|---------|
| **智能退避** | 客户端收到 429，Retry-After 是 1s | 客户端收到 `X-RateLimit-Reset: 1689070800` + `Retry-After: 30` 后精准等待 |
| **配额感知** | 客户端直到被限流才发现配额不足 | `X-RateLimit-Remaining: 5` 让客户端在接近配额时降速 |
| **多限流器诊断** | 客户端收到 429 但不知道是全局 RPS、AI RPS 还是并发限制 | `X-RateLimit-Scope: global\|ai\|concurrency` 标识来源 |
| **SDK 集成** | SDK 无法标准化处理限流重试 | SDK 解析标准头后自动退避+重试 |

### 现状

```go
// internal/middleware/ratelimit.go:90-95 — 当前限流头实现
func (rl *RateLimiter) writeRateLimitHeaders(w http.ResponseWriter, wait time.Duration) {
    w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
    // ❌ 无 X-RateLimit-Limit
    // ❌ 无 X-RateLimit-Remaining
    // ❌ 无 X-RateLimit-Reset
}
```

**三个限流器各自独立，互不感知：**

```go
// cmd/server/main.go:215-218
rl := middleware.NewRateLimiter(cfg.RateLimit.RPS, cfg.RateLimit.Burst)   // 全局
aiRL := middleware.NewRateLimiter(cfg.RateLimit.AIRPS, cfg.RateLimit.AIBurst) // AI 专用
// concurrencyMW 在 applyMiddleware 中也独立运作
```

当客户端同时被多个限流器命中时：

1. 先遇到全局 RPS → 返回 429（Retry-After: 1）
2. 如果通过全局 RPS → 遇到 AI RPS → 返回 429
3. 如果通过所有 RPS → 遇到并发限制器 → 返回 429

客户端收到三次 429，每次原因不同，但无法区分。

**标准限流头（IETF Draft）：**

```
RateLimit-Limit: 100
RateLimit-Remaining: 42
RateLimit-Reset: 30
```

### 架构权衡

**建议实现 IETF `RateLimit-*` 头：**

```go
type RateLimitHeaders struct {
    Limit     float64  // 配额上限（如 100 RPS）
    Remaining float64  // 当前剩余配额
    Reset     int64    // 配额重置的 Unix 时间戳
    Scope     string   // "global" | "ai" | "concurrency"
}

func (rl *RateLimiter) setRateLimitHeaders(w http.ResponseWriter, tenant string) {
    limit, remaining, reset := rl.bucketInfo(tenant)
    w.Header().Set("RateLimit-Limit", strconv.Itoa(int(limit)))
    w.Header().Set("RateLimit-Remaining", strconv.Itoa(int(remaining)))
    w.Header().Set("RateLimit-Reset", strconv.FormatInt(reset, 10))
}
```

**多限流器协调：**

将三个限流器组合为统一检查点，在一次请求中依次检查所有限流器，返回最严格的 `Retry-After`：

```go
type RateLimitCoordinator struct {
    global      *RateLimiter
    ai          *RateLimiter
    concurrency *ConcurrencyLimiter
}

func (c *RateLimitCoordinator) Check(ctx context.Context, path string) error {
    scope := c.resolveScope(path)
    // 对所有匹配的限流器进行一次检查
    // 返回组合后的等待时间 + 对应的 Scope 标识
}
```

**影响范围：**

| 组件 | 改动 |
|------|------|
| `internal/middleware/ratelimit.go` | `Allow` 返回配额信息；新增 `setRateLimitHeaders` 设置标准头 |
| `internal/middleware/middleware.go` | `ConcurrencyLimiter` 同样设标准头；标识 scope 为 "concurrency" |
| `internal/middleware/ratelimit.go` 或新文件 | `RateLimitCoordinator` 结构体组合三个限流器 |
| `cmd/server/main.go` | 将 `applyMiddleware` 中的独立限流器改为 `Coordinator` |
| `sdk/go/aerovault/client.go` | SDK 自动解析标准头，实现退避重试 |
| `sdk/python/aero_vault.py` | 同上 |
| `sdk/js/aero-vault.js` | 同上 |

### 边界情况

| 场景 | 行为 |
|------|------|
| 同时触发全局 RPS + AI RPS | 返回最大 Retry-After；`RateLimit-Scope: global, ai` |
| 并发限制器在 rate limiter 之前触发 | ConcurrencyLimiter 需等待 rate limiter 检查后——当前中间件链顺序 `rl → concurrencyMW` 需要调整或让 Coordinator 统一检查 |
| SSE 长连接是否计入限流？ | SSE 连接应在建立时消耗一次配额，后续事件推送不计入（已有机制的 `rateLimitBypass` 不覆盖 SSE——需调整）|
| 预签名 URL 的限流 | 预签名 URL 绕过 auth middleware，但不应该绕过 rate limiter——`PresignGet`/`PresignPut` 返回的 URL 应在签名验证后附加限流 |
| 限流头信息泄露 | `RateLimit-Remaining` 可能泄漏业务量——可通过配置关闭（默认启用）|

---

## 方向四：事件驱动自动化管线——从静默事件到用户可编程规则引擎

### 产品价值

事件总线（`internal/events/bus.go`）是连接存储操作与异步响应的中枢神经系统，但当前它只有内部消费者。用户无法利用它来构建自动化工作流。

| 用户场景 | 期望 | 当前能力 |
|---------|------|---------|
| **文件上传后自动归档** | 在桶 A 中创建对象后自动复制到桶 B | 需手动调用 | 
| **恶意文件自动隔离** | 对象标记为 `av:infected` 后自动移到隔离桶 | 仅支持 `AV_QUARANTINE` 标志——隔离到同一 storage key |
| **新文件推送 Slack 通知** | 文件名含 "invoice" 时调用 Webhook | 仅全局 webhook，无法按条件路由 |
| **定期清理临时文件** | 在 `tmp/` 前缀下创建的对象 7 天后自动删除 | 仅桶级生命周期，不支持前缀级规则 |
| **多级审批工作流** | 文件标记为 `status: draft` 时发送审批请求，批准后移动到 `published/` | 完全不可能 |

### 现状

**持久化层存在但未执行：**

```go
// internal/repository/repository.go:NotificationRule
type NotificationRule struct {
    ID        string   `json:"Id"`
    Events    []string `json:"Events"`    // ["s3:ObjectCreated:*", "s3:ObjectRemoved:*"]
    FilterKey string   `json:"FilterKey,omitempty"` // {"S3Key":{"FilterRule":[{"Name":"prefix","Value":"invoices/"}]}}
    QueueARN  string   `json:"QueueArn,omitempty"`  // webhook URL
    TopicARN  string   `json:"TopicArn,omitempty"`  // "unused, kept for compat"
    LambdaARN string   `json:"LambdaFunctionArn"`   // "unused, kept for compat"
}
```

REST API 和 S3 API 都可以 CRUD 这些规则，但它们被**持久化但不执行**——`Bus.Publish` 广播所有事件给所有订阅者，从不读取规则的过滤条件。

**事件驱动自动化的构建块已存在：**

| 构建块 | 代码 | 当前用途 |
|--------|------|---------|
| 事件持久化 | `events.Bus.Publish` → `repo.InsertEvent` | 生命周期记录 + SSE 推送 |
| 事件过滤 | 无 | 需要从规则表读取过滤条件 |
| Webhook 投递 | `events.Webhook` | 单 URL 全局 webhook |
| Job 队列 | `jobs.Queue` + `jobs.Pool` | Indexer、Antivirus、Replication |
| 命令/通道 | `PostgresTransport` cross-instance | 跨实例事件传播 |

**用户可编程规则引擎的缺失：**

```
当前:
  事件 → Bus.Publish → broadcast → [Indexer, Antivirus, Replication, Webhook]
  ↑ 所有事件"硬连线"到固定消费者

期望:
  事件 → Bus.Publish → 规则引擎 → 匹配 NotificationRule → 投递到对应目标
  ↑ 规则可通过 API/SDK 由用户编程
```

### 架构权衡

**方案：事件规则引擎分层架构**

```
┌─────────────────────────────────────┐
│  Rules API                           │
│  GET/POST/PUT/DELETE /v1/rules      │ ← 用户可编程
│  (复用现有的 NotificationRule 模型)  │
├─────────────────────────────────────┤
│  Event Router                        │
│  从 Bus.Publish 接收事件              │
│  → 加载所有租户的有效规则（缓存）    │
│  → eventMatchesPattern + filterKey  │
│  → 为每个匹配的规则分发到对应目标    │
├──────────────────┬──────────────────┤
│  Webhook HTTP    │  Job Queue       │
│  (现有 webhook)  │  (现有 jobs)     │
│                  │  → 复制/删除/    │
│                  │    移动/转换对象  │
└──────────────────┴──────────────────┘
```

**扩展事件类型：**

```go
// 当前仅 3 种
const (
    EventCreated  EventType = "created"
    EventDeleted  EventType = "deleted"
    EventAccessed EventType = "accessed"
)

// 扩展后支持
const (
    EventMoved    EventType = "moved"
    EventCopied   EventType = "copied"
    EventTagged   EventType = "tagged"
    EventLocked   EventType = "locked"
    EventRestored EventType = "restored"
    EventScanned  EventType = "scanned"   // 病毒扫描完成
    EventExpired  EventType = "expired"   // 生命周期过期
)
```

**规则引擎核心数据结构：**

```go
type EventRule struct {
    ID          string   // 用户指定 ID
    TenantID    string
    Bucket      string   // "" = 所有桶
    Events      []string // ["created", "deleted", "tagged"]
    Prefix      string   // 可选：key 前缀过滤
    Suffix      string   // 可选：key 后缀过滤
    Action      ActionConfig
    Enabled     bool
    CreatedAt   time.Time
}

type ActionConfig struct {
    Type string // "webhook" | "copy" | "move" | "delete" | "tag" | "notify"

    // webhook 类型
    WebhookURL string
    WebhookSecret string

    // copy/move 类型
    TargetBucket string
    TargetPrefix string

    // tag 类型
    Tags map[string]string

    // notify 类型
    Channel string // "sse" | "email" (future)
}
```

### 边界情况

| 场景 | 行为 |
|------|------|
| 规则匹配时目标桶不存在 | 创建目标桶或返回错误并在 `webhook_failures` 中重试 |
| 递归事件：复制对象触发新的 created 事件 | 规则引擎需防止无限循环——每个事件携带 `origin_event_id`，若动作产生的事件与来源规则匹配则跳过 |
| 大规模规则：1000+ 规则 | 规则缓存 + 分组按前缀过滤，避免每次事件全部遍历 |
| 规则编辑一致性：修改运行时规则 | 规则变更通过 event transport 广播到所有 replica 更新缓存 |
| 规则执行顺序 | 按规则 `priority` 或 `created_at` 顺序执行；支持 `stopOnMatch` |
| 幂等性：网络故障导致动作重复执行 | 使用 `Idempotency-Key`（基于 event_id 派生）做幂等检查 |

---

## 方向五：零停机运维通道——维护模式、排空与在线迁移基础设施

### 产品价值

当前系统不支持任何形式的计划内运维窗口，所有的配置变更和迁移都以"停止服务→变更→重启"的粗暴方式完成。

| 运维场景 | 当前 | 期望 |
|---------|------|------|
| **配置变更**（如调整 rate limit 参数） | 修改 `.env` → 重启二进制 → 断连所有连接 | `POST /v1/admin/config` → 实时生效 |
| **Schema 迁移** | 启动时 `repo.Migrate` → 阻塞监听，无法并行提供服务 | 后台迁移，旧 schema 读旧版本，新 schema 写新版本 |
| **计划内维护** | 无法优雅通知客户端"5 分钟后维护" | `POST /v1/admin/maintenance?mode=drain` → 排空后进入只读 → 维护 → 恢复 |
| **存储后端切换** | 迁移需要导出→停服→导入 | `POST /v1/admin/migrate/backend` → 后台逐步迁移 |
| **蓝绿部署** | 新版本配置或代码无法与旧版本共存验证 | `/readyz` 区分新旧版本，滚动更新时负载均衡器自动切换 |

### 现状

```go
// cmd/server/main.go:37-42 — 配置一次性加载
cfg, err := config.Load()   // 运行时不再读取

// cmd/server/main.go:84-86 — 迁移阻塞启动
if err := repo.Migrate(ctx); err != nil {
    // 迁移失败→启动失败→服务不可用
}

// 无维护模式端点
// 无排空端点
// 无读只切换 API
// 无配置热重载
```

**代码锚点：**

| 文件 | 相关代码 | 缺口 |
|------|---------|------|
| `internal/config/config.go` | `func Load() (*Config, error)` | 无运行时重载点；每个配置字段需声明 `Reloadable: bool` |
| `cmd/server/main.go:84-86` | `repo.Migrate(ctx)` | 阻塞迁移，无法后台执行（Postgres 场景不阻塞，SQLite 阻塞）|
| `internal/middleware/middleware.go:ConcurrencyLimiter` | `sem chan struct{}` | 无 `Drain()` 方法——无法阻止新请求入队 + 等待活跃请求完成 |
| `internal/api/rest/admin.go` | `GetConfig`（只读快照） | 无可写配置端点 |
| `internal/service/file_crud.go:Put` | 所有写入方法 | 无 `ErrReadOnlyMode` 返回 |
| `internal/events/bus.go:Subscribe` | 订阅返回 channel | 无维护模式信号——subscript 无法得知"即将关闭，停止发送" |
| `internal/middleware/ratelimit.go:NewRateLimiter` | RPS 在构造时固定 | 运行时调整 RPS 仅能重建 limiter——需 `SetRPS(newRPS)` 方法 |

### 架构权衡

**1. 运行时配置通道：**

为每个可热重载的配置字段注入 `atomic.Value` 包装：

```go
// 可重载配置字段用 atomic 封装
type DynamicConfig struct {
    LogLevel     atomic.Value // slog.Level
    RateLimitRPS atomic.Value // float64
    RateLimitBurst atomic.Value // float64
    ReadOnly     atomic.Value // bool
    MaintenanceMode atomic.Value // string: "" | "draining" | "readonly"
}
```

新增 admin 端点：

| 端点 | 方法 | 效果 |
|------|------|------|
| `POST /v1/admin/config` | `{"rate_limit_rps": 200}` | 热更新可重载字段 |
| `POST /v1/admin/maintenance` | `{"mode": "readonly"}` | 进入只读维护模式 |
| `POST /v1/admin/maintenance` | `{"mode": "draining"}` | 开始排空，完成后进入只读 |
| `POST /v1/admin/maintenance` | `{"mode": ""}` | 退出维护模式 |
| `GET /v1/admin/status` | `{"mode": "draining", "draining_progress": 0.7}` | 查询当前状态 |

**2. 维护模式状态机：**

```
NORMAL ──→ DRAINING ──→ READONLY ──→ NORMAL
  ↑           │                        ↑
  └───────────┘                        │
  (cancel drain)                       │
       └───────────────────────────────┘
       (maintenance complete)
```

| 状态 | 行为 | 实现 |
|------|------|------|
| `NORMAL` | 正常服务 | — |
| `DRAINING` | 停止接受新写入请求；允许当前 in-flight 写入完成；新请求返回 503 + `Retry-After: 5` | `ConcurrencyLimiter.Drain()` 阻止新请求，等待活跃请求数归零 |
| `READONLY` | GET/HEAD/LIST/SEARCH 正常；PUT/POST/DELETE 返回 `ErrReadOnlyMode` | `FileService` 每个写入方法前检查 `DynamicConfig.ReadOnly` |
| 恢复 | 从 `READONLY` 回到 `NORMAL`，重新允许写入 | `DynamicConfig.ReadOnly = false`，`ConcurrencyLimiter.Resume()` |

**3. Zero-downtime Schema Migration（Postgres 场景）：**

SQLite 场景受限于单进程访问，无法无停机迁移。Postgres 场景可通过：

```go
// 1. 读旧版本
// 2. 后台迁移：在新列/表中开始写入
// 3. 双写期：同时写入新旧结构
// 4. 切换期：old→new，原子切换
// 5. 清理期：删除旧结构
```

**4. 指标与可观测性：**

```go
// 新增运维指标
aero_maintenance_mode{state="readonly"}  // 1 或 0
aero_draining_active  // 1 或 0
aero_draining_requests_inflight  // 当前 in-flight 请求数
aero_migration_active{type="schema"|"backend"}  // 迁移进行中
```

### 边界情况

| 场景 | 行为 |
|------|------|
| DRAINING 状态超时（in-flight 请求不结束） | 可配置最大排空等待时间，超时后强制进入 READONLY（剩余 in-flight 被终止）|
| READONLY 模式下收到写入请求 | 返回 HTTP 503 + 响应体 `{"error": {"code": "ReadOnlyMode", "message": "System is in read-only maintenance mode"}}` |
| 排空前 SSE 连接尚未关闭 | SSE handler 应在 DRAINING 状态下发送 `event: shutdown\n` 后关闭连接 |
| Schema 迁移期间出现兼容性问题 | 迁移应支持 `--dry-run` 模式预检查；应支持回滚（`migrate down`）|
| 多副本集群中的维护模式 | 通过 PostgresTransport 广播维护模式信号；所有副本同时进入/退出 |

---

## 附录：验证检查点

### 方向一（MCP 工具授权）
- [ ] `internal/mcp/server.go` 的 `callTool` 是否有 `checkPermission` 调用？否（当前零权限检查）
- [ ] `toolWriteFile` 执行前是否读取 context 中的 scope？否
- [ ] stdio 模式下是否有任何认证机制？否（`ServeStdio` 无认证）
- [ ] MCP 操作是否在 `audit_log` 表中留下记录？否（仅 `toolReadFile` 写入 `ai_usage`）

### 方向二（跨租户运维面板）
- [ ] 是否有一个 API 端点返回所有租户的 `{tenant, used_bytes, used_objects}`？否（只有 `ListTenantQuotas` 内部方法，无 API 暴露）
- [ ] 是否有一个 API 端点返回所有租户的健康状态？否
- [ ] 是否有存储用量历史趋势 API？否
- [ ] Prometheus 中能否按 tenant 聚合指标？能——但需要从 admin API 获取

### 方向三（限流标准化）
- [ ] 429 响应是否包含 `X-RateLimit-Limit` 头？否
- [ ] 429 响应是否包含 `X-RateLimit-Remaining` 头？否
- [ ] `ConcurrencyLimiter` 拒绝请求时是否设置标准限流头？否（仅 `Retry-After: 1`）
- [ ] 三个限流器（global RPS, AI RPS, concurrency）是否协调返回最严格限制？否

### 方向四（事件自动化规则引擎）
- [ ] `Bus.Publish` 是否读取桶的 `notification_rules` 配置？否
- [ ] 用户能否通过 API 定义"当文件在 `invoices/` 前缀下创建时调用 Webhook A"的规则？可以定义规则（持久化），但不能执行（运行时未消费）
- [ ] 事件类型是否支持 `moved`、`tagged`、`copied` 等？否（仅 `created`、`deleted`、`accessed`）

### 方向五（零停机运维）
- [ ] 是否有运行时配置写入端点？否（`GetConfig` 只读）
- [ ] 是否支持只读维护模式？否
- [ ] 是否支持排空模式？否
- [ ] `RateLimiter` 支持运行时更改 RPS？否（构造时固定）
- [ ] Schema 迁移在 Postgres 场景下是否无停机？是（Postgres 支持 online DDL）——但 aero-vault 启动时仍阻塞等待迁移完成
