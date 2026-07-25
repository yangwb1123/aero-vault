# AeroVault 架构盲区与扩展方向（第 89 轮）

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — `cmd/server/main.go` + `internal/` 全部 24+ 子包（约 320 个 Go 源文件），3 套 SDK，MCP 双模式，Web UI，48 对迁移文件，`deploy/` 全套配置，`HARNESS.md`，`AGENTS.md`，ROADMAP.md，CHANGELOG.md  
> **去重验证：** 对 `docs/requirements/` 下全部 88 份既有分析文档逐方向进行关键词正则 + 语义交叉验证  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。选取**代码中存在明确锚点、有实质性产品/架构影响、且在 88 轮分析中零覆盖或仅路过提及**的方向。每个方向包含：现象与代码证据 → 产品价值 → 架构权衡 → 边界情况。

---

## 背景：跨 88 轮分析的去重矩阵

前 88 轮已覆盖约 100+ 方向的深度架构分析。以下领域已被深度覆盖，**本期不再重复**：

| 领域 | 覆盖期数 |
|------|---------|
| AI/RAG 管线（Embed/Search/Chat/Agent/Rerank/PII/Indexer/Cache/Lineage/Drift） | v1~v13, v21, v30, v61, v66, v69, v71, v82, v88 |
| S3 协议完备性（子资源/Batch/Multipart/ACL/Policy/CORS/Logging/Notification/Select/Inventory/LegalHold） | v1, v4, v6, v8~v10, v16, v17, v19, v27, v31, v42, v63, v77, v83, v87 |
| 存储后端（S3/OSS/COS/KMS/SSE/CircuitBreaker/Multi-Backend/Capabilities Contract） | v4~v15, v46, v77, v87, v88 |
| 认证授权（JWT/API Key/SigV4/OIDC/SAML/SCIM/MFA/Policy Engine/ACL/Bucket Policy） | v1, v5, v8, v11, v12, v15~v17, v19 |
| 多租户（CRUD/Quota/Budget/Audit/Governance/BYOK/Budget-enforcement） | v1, v3~v5, v7, v8, v11, v12, v17, v19, v64, v88 |
| 事件/通知/Webhook/SSE/Bus/Transport/Fanout/Dead-Letter/Replay | v1, v3~v6, v8, v9, v11, v12, v17, v38, v44, v49, v55, v56, v60, v69, v87 |
| 复制/高可用/集群（CRR/SRR/HA/Active-Active/Federation/Cluster Singleton） | v1, v3~v5, v9, v17, v38, v54, v72 |
| 存储分层/生命周期转换/冷热数据（Glacier/IA/Transition/NoncurrentVersion/AbortMPU） | v1, v3, v5, v15, v17, v40, v46, v87 |
| Reconcile/GC/Lifecycle/Orphan/Retention/Scrub/Write-Path Compensation | v1, v4, v6, v7, v15, v48, v55, v86, v88 |
| 合规（WORM/Legal Hold/Retention/Governance Mode/Compliance Mode/Access Log/Client Encryption） | v2, v6, v8~v10, v12, v16, v17, v77 |
| 可观测性（OTel/Metrics/Grafana/Prometheus/SLO/Tracing/pprof/Trace Continuity） | v11, v13, v14, v38, v87 |
| 分片上传（Multipart Lifecycle/Orphan/Sizing/Concurrency/ETag/Server-Side Copy） | v48, v56, v76, v77, v79, v88 |
| 工程质量（并发安全/TOCTOU/内存限制/零拷贝/配置验证/Crash Recovery） | v11, v14, v15, v60, v74, v80, v88 |
| Agent/MCP 安全（沙箱/治理/作用域收缩/Prompt Injection/Session Isolation） | v61, v75, v88 |
| 多协议一致性 & 跨协议集成测试 | v19（深度分析） |
| Web UI / Admin Console / DX | v3, v6, v10, v11, v18, v46 |
| SDK 跨语言完整性 | v11, v18, v75 |
| 性能基准与容量规划 | v11, v12, v13, v16~v20, v22, v23（浅层提及，无深度架构） |
| 导入/迁移/批量操作工具 | v18 |
| Billing / Chargeback / 成本归因 | v7（shallow, 19 行停留于成本账户层面）, v11, v12, v34, v39, v50, v52, v60, v69, v84（各 1-6 行浅层引用） |
| 运维韧性（Graceful Shutdown / Live Reload / Zero-Downtime Upgrade） | v6（1 行注释提及）, v46（1 行表格引用） |
| 跨协议 QoS / 速率限制 / 资源隔离 | **零覆盖** |

---

## 本期方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 | 既有分析覆盖 |
|---|------|------|--------|---------|---------|-------------|
| **1** | **运维韧性成熟度：优雅关闭、配置热加载与在服务生命周期管理** | 运维/架构 | **P1** — 生产环境必需能力完全缺失：无连接排空、无配置热加载、无零停机升级路径、无有状态的健康检测 | `cmd/server/main.go:258`（`runServer`——单一 `signal.NotifyContext`，无排空逻辑）；`internal/middleware/middleware.go`（无 drain hook）；`internal/config/config.go`（全量 env-only，无 reload 入口）；`internal/repository/sql.go:35`（Migrate 启动时执行——阻塞 startup）；`internal/storage/local.go`（无 `io.Closer` 实现） | ✅ **零实质性架构分析**（v6 方向表注释行提及"关闭通知（graceful shutdown）"——焦点为 Bus.Close 而非 HTTP 排空；v46 表格一行引用 hot reload——焦点为开发工作流而非运维韧性） |
| **2** | **跨协议运营治理：统一速率限制、租户配额与服务质量编排** | 架构/平台 | **P2** — 四套协议共享一个 FileService 但无协议感知的 QoS 策略：S3 批量同步可挤占交互式 WebDAV 用户；AI 速率限制独立但存储/管理 API 无差异化保护 | `internal/middleware/ratelimit.go`（`RateLimiter`——单一 token-bucket，无协议路由匹配）；`internal/middleware/middleware.go:ConcurrencyLimiter`（全局 semaphore——无协议优先级）；`cmd/server/main.go:212-215`（`aiRL` 仅挂载到 AI 路由组——存储/管理 API 无独立限流）；`internal/service/file.go`（FileService——无协议来源标记，无请求优先级） | ✅ **零覆盖**（全量 88 份文档正则搜索 `protocol.*QoS\|per.protocol.*rate\|protocol.*limit\|protocol.*thrott\|protocol.*govern\|protocol.*priori` → **0 命中**。v7/v11 覆盖了全局 rate limit 概念但**焦点为绝对值而非差异化策略**） |
| **3** | **配置完整性但不执行：策略-动作物化鸿沟（Policy-Action Materialization Gap）** | 架构/合规 | **P1** — S3 API 接受并存储了通知规则、访问日志配置、存储类、LegalHold 等策略，但系统从未读取这些配置来驱动对应动作。这是贯穿全代码库的结构性模式：**配置是完整的，执行引擎是缺失的** | `internal/repository/sql_buckets.go:370-380`（`WriteAccessLog`——实现完整但**全代码库零调用方**）；`internal/events/bus.go:51-67`（`Publish`——不读取 bucket 的 `NotificationRules`，不按规则路由事件）；`internal/api/s3compat/handler.go:809-834`（`putBucketNotifications`——接收 XML、解析 rule、调用 `SetBucketNotifications`——但**无任何消费者读取 rules**）；`internal/service/file_crud.go:buildPutObject`（`StorageClass`——写入 metadata 但永不触发存储层转换）；`internal/service/file_crud.go:checkCorrupt`（检查 `_aero_scrub_status`——但 `_aero_legal_hold` 只在硬删除时校验，不在 GET 路径校验） | ✅ **零实质性跨域架构分析**（v17 方向三分析了 notification engine 的 dispatch 骨架设计（~650 行估算），但**焦点在单一功能: S3 通知的 SQS/SNS/Lambda 路由**，**从未将"配置完备但执行缺失"作为跨 4 个 S3 子系统的结构性架构模式分析**；v40 方向四一行概念性提及"StorageClass→后端映射"；v77 方向二覆盖 LegalHold 读取未过滤。**本方向首次揭示该模式在通知/日志/存储类/合规四域的完整投射**） |
| **4** | **统一成本归因与租户经济模型（Unified Cost Attribution & Tenant Economics Framework）** | 产品/运营 | **P2** — 当前仅跟踪 AI API 调用成本（`ai_usage` 表的 `CostMicros`），但存储、网络、请求、计算成本完全不可见。多租户 SaaS 场景下无法回答"每个租户实际花费多少" | `internal/repository/repository.go:261-270`（`Usage` 结构体——仅包含 `Model`/`PromptTokens`/`CompletionTokens`/`LatencyMs`/`CostMicros`——纯 AI 维度）；`internal/repository/sql_objects.go`（`UpsertObject`——记录 size 但不关联后端定价）；`internal/service/file_crud.go:AddTenantUsage`（仅 bytes+objects 计数，零成本换算）；`internal/service/file_features.go:Usage`（返回 `TenantQuota`——仅有 bytes/objects 用量，零财务维度）；`internal/telemetry/metrics.go`（`StorageBytesGauge`/`StorageObjectsGauge`——聚合但不分存储类、不标注成本） | ✅ **浅层提及无架构**（v7 有 19 行索引匹配但内容仅覆盖"日预算"与"成本账户"概念——**非成本归因架构**；v34/v39/v50/v52/v60/v69/v84 各 1-6 行浅层引用——**无一涉及成本模型设计、存储后端定价映射、租户级损益计算**；v12"智能平台"方向 3 行概念性提及 fairness 与 cost-aware scheduling——**零代码锚点与架构设计**） |
| **5** | **协议级连接与流式传输韧性（Protocol-Level Connection & Streaming Resilience）** | 可靠性/UX | **P2** — 长连接（SSE 事件流、MCP stdio、ChatStream、S3 multipart 上传）缺乏客户端断连检测、心跳保活、断点续传、优雅关闭通知。网络不稳定环境下用户体验不可预测 | `internal/api/rest/sse.go:Stream`（`w.(http.Flusher)`——无 `r.Context().Done()` 监听 → DB 轮询 goroutine 泄漏）；`internal/api/rest/search.go:ChatStream`（SSE 写入循环——无 `WriteDeadline` 刷新、无客户端断开感知）；`internal/mcp/transport.go:30-50`（stdio `*bufio.Scanner`——无心跳、无超时断开）；`internal/storage/local_multipart.go:30-40`（multipart 上传——无上传会话超时、无断点续传）；`internal/events/bus.go:Subscribe`（返回 `<-chan repository.Event`——订阅者关闭后 channel 未 drains，`broadcast` 阻塞高吞吐路径） | ✅ **零实质性架构分析**（v39 方向一覆盖 SSE subscriber channel 泄漏——**聚焦 goroutine 泄漏而非连接韧性**；v44 方向五覆盖 SSE replay 完备性——**聚焦重连后事件续传而非连接健康检测**；v60 方向二提及 ChatStream 断开检测——**约 30 行概念分析，零架构设计**；v48 方向二提及 multipart 超时——**聚焦孤儿清理而非上传会话状态保持**） |

---

## 方向一：运维韧性成熟度 — 优雅关闭、配置热加载与在服务生命周期管理

### 现状

当前 `runServer` 的关闭路径：

```go
// cmd/server/main.go:258（简写，实际行号以 main.go 为准）
func runServer(ctx context.Context, handler http.Handler, cfg *config.Config, ...) error {
    srv := &http.Server{Addr: cfg.App.Addr, Handler: handler, ...}
    go func() {
        srv.ListenAndServe()
    }()
    select {
    case <-ctx.Done():   // SIGINT/SIGTERM → 立即触发
    case <-errCh:
    }
    shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
    defer cancel()
    srv.Shutdown(shutdownCtx)  // 仅等待 in-flight HTTP 请求
    bus.Close()
    _ = shutdownOtel(shutdownCtx)
}
```

存在 5 个关键缺口：

| 缺口 | 当前行为 | 生产影响 |
|------|---------|---------|
| **无排空窗口** | `signal.NotifyContext` 收到信号后**立即调用 `srv.Shutdown`**，无等待新请求停止的 grace period | 负载均衡器尚未将实例从 pool 移除，新请求仍被路由进来但被拒绝 |
| **后台工作者不排空** | `bus.Close()` 关闭 event bus，但 `ai.Indexer.Run`、`antivirus.Worker.Run`、`replication.Worker.Run`、`reconcile.*` 等后台 goroutine 只依赖 `ctx.Done()`，无确定性的排空+等待 | 正在执行的索引/扫描/复制任务被随机终止，可能留下部分完成状态 |
| **存储后端不关闭** | `storage.Storage` 接口无 `Close() error`；`LocalStorage` 的 `*os.File` 在进程退出时由 OS 关闭但无 flush 保证 | 内存 buffered I/O 可能丢失最后几 KB 数据 |
| **无配置热加载** | `config.Load()` 仅在 `main()` 开头调用一次，所有组件持有启动时的配置快照 | 修改 `APP_LOG_LEVEL`、`RATE_LIMIT_RPS`、`STORAGE_DEFAULT_CLASS` 等参数必须重启进程（中断所有连接） |
| **就绪探针无语义深度** | `/readyz` 检查 `repo.Ping()` + `store.Stat("@healthz/probe")`——仅检查 DB 连接和存储基本可用性 | 迁移进行中、索引器落后、存储后端高延迟——readiness 仍返回 200，负载均衡器继续路由流量 |

### 产品价值

| 用户画像 | 场景 | 当前体验 | 就绪后 |
|---------|------|---------|--------|
| 平台 SRE | 滚动更新部署 3 副本集群 | 每实例关闭时丢弃 in-flight 写入 | 负载均衡器先摘除 → 等待排空 → 确认后台任务完成 → 关闭 |
| 运维工程师 | 生产环境需要降级日志级别排查问题 | 修改 `APP_LOG_LEVEL=debug` → 必须重启 bin → 断连所有 WebSocket/MCP 客户端 | `curl -XPOST /debug/log-level?level=debug` 实时生效 |
| 云原生团队 | Kubernetes `preStop` hook + `terminationGracePeriodSeconds` | 只有 15 秒硬超时关闭 HTTP | 可配置的 `drain_timeout`，按组件阶段式关闭 |
| 平台负责人 | 希望在不重启集群的前提下更换 LLM 模型 | 必须修改 env + 滚动重启 | 热切换 LLM 模型：`PUT /debug/ai/model` + 渐进式 warm-up |

### 架构权衡

| 方案 | 复杂度 | 影响范围 | 建议 |
|------|--------|---------|------|
| `srv.RegisterOnShutdown` + `sync.WaitGroup` 跟踪 in-flight 后台任务 | 低 | 仅 `main.go` | ✅ **第一步**：增加 goroutine 追踪与确定性等待 |
| 健康检测扩展：`/readyz?full=1` 包含所有依赖语义状态 | 低 | 仅 `main.go` + `readyzHandler` | ✅ **第一步**：返回组件级详细状态（json） |
| 配置热加载：`/debug/reload` 端点触发 `config.Load()` + 组件 `Reload(*Config) error` 接口 | 中高 | 每个组件需实现 Reload——增量改造 | ✅ **第二步**：优先实现 `rate_limiter.Reload`、`log_level.Reload`、`ai_embedder.Reload` |
| 阶段式关闭：`PRE_STOP` → `DRAIN` → `FLUSH` → `CLOSE` 状态机 | 高 | `main.go` + 所有组件需实现 GracefulShutdown 接口 | ❌ **第三步**：超出当前阶段需求，可后续分批引入 |

### 边界情况

| 场景 | 影响 | 处理策略 |
|------|------|---------|
| `srv.Shutdown` 超时后仍有 in-flight 请求 | 请求被硬断，客户端收到连接重置 | 超时后 `srv.Close()` 立刻关闭监听器，**已处理的请求应已完成**；剩余后台工作者 `context.Background()` 不应被 Cancel |
| 配置热加载中修改的值使组件初始化失败（如 LLM endpoint 不可达） | 新配置 applied 失败 → 系统处于不一致状态 | 先验证（dry-run）再 apply；失败时保持旧配置 + 日志告警 |
| 索引器正在处理大文件时收到关闭信号 | 索引任务执行到一半，DB 无标记 | Indexer 应实现 `context.Context` 感知：`select case <-ctx.Done()` 回滚未提交的 chunk 写入 |
| 多副本同时收到 SIGTERM | 所有实例同时关闭 → 无服务可用 | 依赖 Kubernetes `preStop` + `terminationGracePeriodSeconds` 的交错配置，而非应用层协调 |

---

## 方向二：跨协议运营治理 — 统一速率限制、租户配额与服务质量编排

### 现状

当前系统的四套协议（REST、S3、WebDAV、MCP）共享同一个 `FileService` 核心，但速率限制和资源隔离是**协议不可知**的：

```
            ┌──────────────────────────────────────┐
REST ────── │   Global RateLimiter (token-bucket)    │
S3  ────── │   ↓ 作用于所有入站请求                  │
WebDAV ── │   ConcurrencyLimiter (semaphore)         │
MCP  ──── │   ↓ GET=1, PUT/POST/DELETE=2            │
            └──────────────────────────────────────┘
                              ↓
                      ┌──────────────┐
                      │  FileService  │
                      └──────────────┘
```

| 缺失维度 | 当前状态 | 结果 |
|---------|---------|------|
| **协议感知** | `RateLimiter` 对所有协议一视同仁 | S3 批量操作（`BatchDelete`, `ListObjectsV2`）与 REST 交互式请求竞争同一个 token bucket |
| **操作类型感知** | 所有 HTTP method 使用同一 bucket | `GET` 轻量查询与 `PUT` 大文件上传竞争 |
| **租户请求预算** | 仅存储字节/对象配额；无 API 调用配额 | 一个租户可通过大量小请求耗尽全局 RPS，影响其他租户 |
| **优先级** | 无请求优先级机制 | 管理 API（`/v1/admin/*`）与用户请求同等对待，管理流量可能在负载下被饿死 |
| **速率限制绕过** | WebDAV 使用 `xwebdav.Handler` 独立于 chi router，绕过 chi 中间件链 | WebDAV 请求不受 `aiRL` 影响——但**也不受全局 `rl.Middleware()` 影响**（因为在 `buildDispatcher` 中先于 chi router 分发） |
| **MCP 无限制** | MCP HTTP 端点通过 chi 路由，但 stdio 模式完全绕过 HTTP 中间件链 | stdio MCP 无速率限制、无并发控制 |

从 `buildDispatcher` 的实现可以清晰看到 WebDAV 是如何绕过中间件的：

```go
// cmd/server/main.go（buildDispatcher）
func buildDispatcher(r *chi.Mux, davH http.Handler, cfg *config.Config) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
        if davH != nil && cfg.WebDAV.Prefix != "" {
            p := req.URL.Path
            if p == cfg.WebDAV.Prefix || strings.HasPrefix(p, cfg.WebDAV.Prefix+"/") {
                davH.ServeHTTP(w, req)   // ← 绕过 chi 路由 → 绕过中间件链中的 rl.Middleware()
                return
            }
        }
        r.ServeHTTP(w, req)
    })
}
```

中间件链的顺序是：
```go
// main.go:applyMiddleware
AccessLog → Concurrency → Recoverer → OTel → RateLimiter(lobal) → Tenant → Auth → CORS → RequestID
```

WebDAV 请求在 `buildDispatcher` 中直接被 `davH.ServeHTTP` 截获——**完全不经过中间件链**（包括 `RateLimiter`、`Auth`、`Tenant` 等）。这是一个设计上的「后门」：WebDAV 请求没有全局速率限制、不通过标准鉴权路径。

### 产品价值

| 用户画像 | 场景 | 当前问题 | 治理后 |
|---------|------|---------|--------|
| SaaS 运营 | 上午 10 点批量数据同步（S3）开始，交互式 WebDAV 用户延迟飙升 | S3 批量 GET/PUT 与 WebDAV 交互式操作争抢全局 RPS | S3 同步操作分配 `priority=LOW` 的 rate limit；WebDAV 交互操作分配 `priority=HIGH` |
| 平台管理员 | 一个租户因 bug 发起大量请求，拖慢全局 | 全局 rate limit 达到阈值后所有租户被限流 | 每个租户的 `requests/second` 独立限制，违规租户被限流但不影响其他租户 |
| 企业客户 | 内部工具链使用 MCP stdio 模式频繁调用 search | MCP stdio 完全无速率限制，可在一秒内发起数百次 `search` 和 `read_file`（每个都计费） | MCP 会话绑定到租户的 `tokens/minute` 预算和 `requests/minute` 配额 |
| 安全团队 | 发现异常 IP 正在爬取对象 | 只能通过全局 RPS 间接限制，影响合法用户 | 按 IP/租户/协议三元组独立限流，违规源精准阻断 |

### 架构权衡

| 方案 | 复杂度 | 影响范围 | 建议 |
|------|--------|---------|------|
| 协议感知 rate limiter：`RateLimiter` 增加协议路由匹配 | 中低 | `internal/middleware/ratelimit.go` + `main.go` | ✅ **第一步**：`RateLimit{Path: "/s3/*", RPS: 1000, Burst: 200}` 支持 path-based 规则 |
| WebDAV 归入 chi router：将 dav handler 作为 chi route handler 而非 dispatch 级别截获 | 中 | `main.go:buildDispatcher` + `buildRouter`——让 WebDAV 经过标准中间件链 | ✅ **第一步**：修复 WebDAV 绕过中间件的设计缺陷 |
| 租户级请求配额：`PUT /v1/admin/tenants/{t}/request-quota` | 中 | `internal/repository/tenants.go` + `internal/middleware/ratelimit.go` | ✅ **第二步**：复用已有 tenant/quota schema，增加 `rps_quota` / `burst_quota` 字段 |
| 请求优先级队列：为管理 API 单独分配 slot，高优先级请求可从低优先级预支 | 高 | 需要 `priorityQueue` 或独立 `RateLimiter` 实例 | ❌ **第三步**：当前阶段可先通过多实例 RateLimiter 实现粗粒度优先级 |

### 边界情况

| 场景 | 影响 | 处理策略 |
|------|------|---------|
| MCP stdio 和 HTTP 同属一个进程，用户同时打开两个 MCP 会话 | 两个会话共享同一个 FileService 实例，无隔离 | 每个 MCP 连接绑定 `(session_id, tenant)`，按租户分配独立 quota bucket |
| 协议感知限流下，`PUT /s3/bucket/key`（S3 路径）与 `PUT /v1/files/key`（REST 路径）速率不同 | 用户可能迁回不受限的协议 | 策略一致性：FileService 层记录协议来源，统一在服务层仲裁而非仅 HTTP 中间件 |
| 速率限制配置错误导致管理 API 被限流 | 管理员无法解除限流困境（先有鸡还是先有蛋） | 管理 API 路由组使用独立的 `adminRL`，不与其他请求共享配额；支持从 `localhost`（环回地址）的请求豁免 |

---

## 方向三：配置完整性但不执行 — 策略-动作物化鸿沟

### 现象与范围

这是贯穿全代码库的结构性架构模式：S3 协议接受并存储了多种策略配置，但系统**从不读取这些配置来驱动对应动作**。代码中存在**配置的完整 CRUD 路径**，但**零执行引擎**。

| 子系统 | 配置存储 | 执行状态 | 代码锚点 |
|--------|---------|---------|---------|
| **桶通知** | ✅ `NotificationRule` 通过 S3 API 写入，持久化为 `buckets.notification_rules` JSON | ❌ `events.Bus.Publish` 不读取通知规则，不按规则路由到 SQS/SNS/Lambda/Webhook | `internal/repository/sql_buckets.go:410`（`SetBucketNotifications` 写入 JSON）；`internal/events/bus.go:51-67`（`Publish` 不查询 `GetBucketNotifications`）；`internal/api/s3compat/handler.go:815-834`（S3 XML 解析 + 存储 + 响应——路由引擎零行代码） |
| **访问日志** | ✅ `LoggingConfig`（`logging_target`, `logging_prefix`）通过 S3 API 写入并存储 | ❌ `WriteAccessLog` 在 `sql_buckets.go:370` 有完整实现（INSERT 到目标桶），但**全代码库零调用方**——没有 middleware/handler/service 在任何请求路径上调用它 | `internal/repository/sql_buckets.go:370-380`（`WriteAccessLog` 实现——INSERT 合成 key 到目标桶）；`internal/api/s3compat/handler.go:PutObject`（不调用 `WriteAccessLog`）；`internal/middleware/middleware.go:AccessLog`（记录 slog 但不生成 S3 访问日志） |
| **存储类** | ✅ `Object.StorageClass` 在 Put 时记录、在 S3 响应中返回 `x-amz-storage-class` | ❌ 没有任何组件根据 `StorageClass` 切换存储后端、移动数据或调整副本数 | `internal/service/file_crud.go:buildPutObject`（`StorageClass: StorageClassOrDefault(opts.StorageClass)`——仅元数据存储）；`internal/reconcile/lifecycle.go`（`ExpireAction` 仅 `soft_delete`/`hard_delete`——无 `transition_to_ia`/`transition_to_glacier`） |
| **Legal Hold** | ✅ `_aero_legal_hold` metadata key 在 Put 时通过 `x-amz-object-lock-legal-hold` 设置 | ❌ 仅在 `hardDeleteObject` 中校验——GET/HEAD 路径不检查，WebDAV 读取不检查 | `internal/service/file_crud.go:288-290`（`hardDeleteObject` 检查 `_aero_legal_hold == "ON"`）；`internal/service/file_crud.go:Get`（不检查 legal hold）；`internal/service/file_crud.go:Stat`（不检查 legal hold） |
| **Scrub 状态** | ✅ `_aero_scrub_status` metadata key 由 Scrub 作业写入 | ✅ **唯一例外**：`file_crud.go:checkCorrupt` 在 Get/Stat/Delete 中读取该 key 并返回 `ErrObjectCorrupt` | `internal/service/file_crud.go:237-240`（`checkCorrupt`）；`internal/reconcile/scrub.go`（Scrub 作业正确写入该 key） |

### 最典型案例：桶通知的完整生命周期

```
S3 Client → PUT /bucket?notification (XML)
  ↓
s3compat/handler.go: parseNotificationConfig → 存储到 DB `notification_rules`
  ↓
s3compat/handler.go: 返回 200 OK 给客户端
  ↓
  【 配 置 完 整 】
  ↓
EventBus.Publish(object.created) → repo.InsertEvent + broadcast
  ↓
  【 无 人 读 取 notification_rules 】
  ↓
规则中的 webhook/SQS/Lambda → ❌ 永不送达
```

`NotificationRule` 结构体类型定义：

```go
// internal/repository/repository.go:57
type NotificationRule struct {
    ID        string   `json:"Id"`
    Events    []string `json:"Events"`
    FilterKey string   `json:"FilterKey,omitempty"`
    QueueARN  string   `json:"QueueArn,omitempty"`     // webhook URL or queue ARN
    TopicARN  string   `json:"TopicArn,omitempty"`     // unused
    LambdaARN string   `json:"LambdaFunctionArn"`      // unused
}
```

`TopicARN` 和 `LambdaARN` 的注释明确标注 "unused"——设计者已知这是一个存根。

### 产品价值

| 用户画像 | 场景 | 当前行为 | 就绪后 |
|---------|------|---------|--------|
| 数据工程师 | 配置 S3 事件通知，期望新文件上传后 Lambda 函数自动处理 | 存储成功，事件写入 DB，但 Lambda 从不被调用 | 事件到达 → `Bus.Publish` 读取 bucket 通知规则 → 匹配 FilterKey → POST 到配置的 Lambda/SQS/webhook |
| 安全审计 | 启用 S3 服务器访问日志，期望所有 GET/PUT 记录到目标桶 | 配置成功（200 OK），但无日志写入 | 每个请求通过 middleware → `WriteAccessLog` → 写入目标桶的日志前缀 |
| 存储管理员 | 设置对象为 `STANDARD_IA` 存储类以降低成本 | 元数据正确记录，账单不变，数据在后端不变 | 生命周期作业读取 `StorageClass` → 根据规则将对象迁移到 S3-IA / OSS-AA 等后端 |
| 合规人员 | 对涉密文件设置 Legal Hold | 硬删除被阻止，但 GET 照常返回文件内容 | GET 路径检查 `_aero_legal_hold` → 返回 `423 Locked` 或跳过内容，仅返回元数据 |

### 架构权衡

| 方案 | 复杂度 | 建议 |
|------|--------|------|
| **通知引擎**：`Bus.Publish` 查询 `GetBucketNotifications`，匹配 EventType+FilterKey，路由到已注册 `NotificationDestination` 适配器 | 中（~500 行） | ✅ **最高优先级**——这是最大的"配置完成但零执行"缺口 |
| **访问日志中间件**：`AccessLog` middleware 检查目标桶的 `LoggingConfig`，每请求调用 `WriteAccessLog` | 低（~50 行，复用现有实现） | ✅ **可快速闭环**——`WriteAccessLog` 的实现已存在，仅是未被调用 |
| **StorageClass Transition**：`LifecycleJob` 增加 `ExpireAction=transition` + 后端路由选择器 | 高（需要多后端路由基础设施） | ❌ **依赖方向二（v87/v88 已有设计）+ 本方向四** |
| **Legal Hold GET 拦截**：`file_crud.go:Get` 增加 `_aero_legal_hold` 检查 | 低（~10 行） | ✅ **低挂果实**——实现一天内可完成 |

### 边界情况

| 场景 | 影响 | 处理策略 |
|------|------|---------|
| 通知引擎失败（目标不可达、认证失败） | 事件投递失败 | 记录失败到 `webhook_failures` 表重用已有重试机制；增加 `event_notifications_delivery_failures_total` 指标 |
| 大量事件同时到达导致通知引擎成为瓶颈 | 通知分发延迟累加 | 通知引擎使用 worker pool（复用 `jobs.Pool`），与索引/复制/扫描作业共享工作者 |
| 访问日志写入目标桶本身产生更多访问日志事件 | 无限循环 | 访问日志路径不触发 `WriteAccessLog`（跳过目标桶的自我记录） |
| 存储类转换中对象被读取 | 用户可能读取到正在移动的对象 | 转换使用 copy→verify→delete 模式，转换期间 GET 从原始位置服务 |
| Legal Hold 与对象锁同时设置 | 两个独立的锁定机制 | Legal Hold 优先于时间锁：Legal Hold 解除前即使 `locked_until` 已过期也不允许写入/删除 |

---

## 方向四：统一成本归因与租户经济模型

### 现状

当前系统的成本追踪仅在 AI API 层面：

```
ai_usage 表：Model, PromptTokens, CompletionTokens, TotalTokens, LatencyMs, CostMicros
```

这是唯一具有成本维度（`CostMicros`）的数据点。以下成本维度**完全不可见**：

| 成本维度 | 追踪状态 | 当前信息源 | 缺失部分 |
|---------|---------|-----------|---------|
| **AI 推理成本** | ✅ LLM token 级成本（`ai_usage.cost_micros`） | `chat.go:WithPricing` 配置的 `promptUSDPer1K` / `completionUSDPer1K` | **嵌入成本**（`ai_embed` 调用无成本记录）；**重排成本**（`rerank` 调用无成本记录）；**提取成本**（调用外部 `RemoteExtractor` 无记录） |
| **存储成本** | ❌ 仅 bytes 计数 | `TenantQuota.UsedBytes` | 无后端定价映射（`local=0.02$/GB/月`？`s3=0.023$/GB/月`？`oss=0.019$/GB/月`？）；无存储类定价差异；无副本成本（SSE 加密？复制？） |
| **网络成本** | ❌ 完全未追踪 | 无 | 出站流量（GET/download）成本；跨区复制流量成本；与 `REPLICATION_ENABLED` 相关 |
| **请求成本** | ❌ 完全未追踪 | 无 | PUT/GET/DELETE 每请求成本；LIST 扫描成本；Multipart 每部分成本 |
| **作业成本** | ❌ 完全未追踪 | 无 | 索引作业的嵌入计算成本；AV 扫描作业的计算成本；Reconcile 扫描的 DB 成本 |

`TenantQuota` 结构体：

```go
// internal/repository/tenants.go（示意）
type TenantQuota struct {
    TenantID         string
    UsedBytes        int64    // 存储用量
    MaxBytes         int64    // 存储配额
    UsedObjects      int64    // 对象数
    MaxObjects       int64    // 对象配额
    DailyBudgetMicros int64   // AI 日预算（美元微单位）
}
```

没有任何字段表达：存储成本、网络成本、请求成本、累计支出。

### 产品价值

| 用户画像 | 场景 | 当前瓶颈 | 就绪后 |
|---------|------|---------|--------|
| SaaS 运营负责人 | 需要按租户出具月度账单 | 只能给出 "存储用量 X GB"，无法折算金额 | 账单包含：存储费 + API 请求费 + AI 推理费 + 网络出站费 |
| 产品经理 | 决定免费层和付费层的存储/AI 限额 | 只能靠存储字节数估算，AI 成本靠数据库 `CostMicros` 汇总 | 每个租户可查看 `GET /v1/admin/tenants/{t}/cost?period=2026-06` |
| 平台 SRE | 发现整体成本飙升，需要定位到租户或操作类型 | 只有 `ai_cost_micros_total` 指标，存储/网络成本盲区 | 仪表盘：`top 10 租户 by total_cost`，`top 5 操作 by request_cost` |
| 企业客户 | 需要理解"为什么我的账单比预期高" | 系统无法解释 | 自助成本分析：存储成本由 `STANDARD` / `STANDARD_IA` 分摊明细 |
| 财务团队 | 希望在用户超出预算时自动限制 | 仅 AI 有 `DailyBudgetMicros`，存储超配额只阻止新写入 | 统一预算仪表盘 + 跨维度的自动限制策略 |

### 架构权衡

| 方案 | 复杂度 | 影响范围 | 建议 |
|------|--------|---------|------|
| **扩展 `ai_usage` 表**：增加 `embed_cost_micros`, `rerank_cost_micros`, `extract_cost_micros` | 低 | 仅 `internal/ai/*.go` + 迁移 0014 | ✅ **第一步**：补全 AI 管线全链路成本（嵌入/重排/提取当前零成本记录） |
| **新增 `storage_usage_cost` 表**：每日快照每个租户各存储类别的字节数 × 定价 | 中 | 新增表 + 新 reconcile job + `config_storage.go` 增加定价配置 | ✅ **第二步**：`StorageClassCost{Standard: 0.023, IA: 0.0125, Glacier: 0.004}` + 每日统计作业 |
| **新增 `request_usage` 表**记录每操作类型的请求量 | 中高 | 新增表 + middleware instrumentation 按 tenant+operation+protocol 计数 | ✅ **第三步**：轻量级——仅 `CounterVec` + 每日 flush 到 DB |
| **统一成本 API**：`GET /v1/admin/tenants/{t}/cost-summary` | 高 | 聚合三张成本表 + 按时间范围筛选 | ❌ **第四步**：前三个步骤完成后作为展现层聚合 |

### 边界情况

| 场景 | 影响 | 处理策略 |
|------|------|---------|
| 存储后端定价变化（如 AWS S3 降价） | 历史成本数据变得不准确 | `storage_pricing` 应带版本或生效日期；成本报告标注 "based on pricing as of YYYY-MM-DD" |
| 租户 A 的数据被租户 B 的请求读取（共享桶场景） | 网络成本归因模糊 | 出站流量按读取方租户计费，存储按拥有方租户计费 |
| 免费层租户产生大量请求 | 请求成本累积在免费层租户但无法收费 | 免费层 `request_quota` 阻止超额，超量返回 `402 Payment Required` |
| 硬件折旧（Local Storage 后端的磁盘成本） | 无外部 API 计算硬件成本 | Local 后端成本按 `$/GB/月` 配置，与云后端同等模型处理——由运维配置而非自动计算 |
| AI 成本记录在 `ai_usage` 中但租户被软删除后 | 历史数据不可查询 | 成本记录保留在 `ai_usage` 表（不级联删除），审计/账单仍可访问 |

---

## 方向五：协议级连接与流式传输韧性

### 现状

系统有多个维持长连接的组件，但每个都有连接韧性缺口：

| 连接类型 | 组件 | 当前状态 | 代码证据 |
|---------|------|---------|---------|
| **SSE 事件流** | `GET /v1/events/stream` | 客户端断开后，`Stream` 中的 DB 轮询 goroutine 仍运行，`lastID` 持续泵出事件到已关闭的 `http.Flusher`——goroutine 泄漏 + 无用 DB 查询 | `internal/api/rest/sse.go:30-60`（`Stream` 写入循环——无 `r.Context().Done()` 信号量监听；`for` 循环 `select` 中仅 `ticker.C` 和 `sub` 两个 case——无客户端断连感知） |
| **ChatStream SSE** | `POST /v1/chat/stream` | SSE headers 发送后服务器写入 `flusher.Flush()`；客户端断开时 `r.Context()` 已取消但写入循环不检查 Context——写入可能 panic（向关闭连接写） | `internal/api/rest/search.go:ChatStream`（写入循环——`flusher.Flush()` 可 panic 于已关闭连接；Go 1.25+ 的 `Write` 返回 `io.EOF`，但循环不检查 `ctx.Done()`） |
| **MCP stdio** | `aero-vault mcp` | 使用 `*bufio.Scanner` 逐行读取 stdin，无 `SetReadDeadline`、无客户端断开检测——如果子进程断开，Scanner 阻塞直到父进程 kill | `internal/mcp/transport.go:30-50`（`Scan()` 循环——无 context 感知、无超时）；`man.go:runMCP`（无 `r.Context()` 注入到 Server） |
| **MCP HTTP** | `POST /mcp` | 通过 chi router 路由——标准 HTTP 请求/响应，但 JSON-RPC 会话状态需要在多次请求间保持，当前无会话存储 | `cmd/server/main.go:187`（`r.Method(http.MethodPost, "/mcp", mcp.HTTPHandler(mcpServer))`——无 `http.Hijacker` 或 WebSocket 升级） |
| **Multipart 上传** | `POST /multipart + PUT /multipart/{id}/parts/{n}` | 上传会话无超时，上传部分无 TTL，AbortMultipart 后的部分残留需靠外部 GC | `internal/storage/local_multipart.go:30-40`（`InitMultipart` 创建目录——不记录超时时间）；`internal/reconcile/job.go`（不扫描过期 multipart 上传） |
| **EventBus 订阅者** | `bus.Subscribe()` | 订阅者停止读取 channel 后，`broadcast` 向满 channel 写入会阻塞 `Publish`（阻塞所有后续事件写入和请求完成） | `internal/events/bus.go:63-70`（`broadcast`—`select {case ch <- e: default: b.dropped.Add(1)}`——默认行为是 drop，但前提是 channel 是 buffered 且非阻塞写入——若 `len(ch) == cap(ch)` 而 `cap(ch)` 默认仅 64，backpressure 可快速阻塞生产者） |

### 产品价值

| 用户画像 | 场景 | 当前体验 | 就绪后 |
|---------|------|---------|--------|
| 前端开发者 | 浏览器通过 SSE EventSource 订阅事件流，用户切换页面 | 旧页面未正确关闭 EventSource，goroutine 泄漏 + 无用 DB 查询持续运行 | 客户端断开 5 秒内 SSE goroutine 退出，资源完全释放 |
| AI 集成商 | 通过 ChatStream 流式获取大模型回答，网络短暂抖动 | SSE 流中断后无法续接，用户必须重发整个请求 | ChatStream 支持 `Range: bytes`（等效于已接收令牌）或 Last-Event-ID 续传 |
| MCP 客户端 | Claude Desktop 通过 stdio 模式连接，子进程被 OOM Kill | 子进程 Kill 后，父进程 `bufio.Scanner` 阻塞在 `Scan()`，无超时退出 | stdio 传输增加 ReadDeadline + 健康探测，子进程死后 30 秒内自动退出 |
| 备份工具 | 通过 S3 multipart 上传大文件，上传到一半网络断开 | 已上传的部分在 `AbortMultipart` 或超时前持续占用存储空间 | 上传会话 TTL 可配置，超时后自动 GC；支持 S3 `ListMultipartUploads` + `UploadPartCopy` 续传 |
| SRE | EventBus 的一个订阅者处理慢（如 webhook 延迟） | 慢订阅者导致 `broadcast` 进入 `default`（drop），事件丢失 | 慢订阅者被自动断开（替换为新 channel），事件通过缓冲池防丢失 |

### 架构权衡

| 方案 | 复杂度 | 影响范围 | 建议 |
|------|--------|---------|------|
| **SSE 客户端断开检测**：`r.Context().Done()` 作为 `select` 的 case | 低（~10 行/端点） | `internal/api/rest/sse.go` + `internal/api/rest/search.go` | ✅ **第一步**——修复已知 goroutine 泄漏 |
| **MCP stdio ReadDeadline**：`conn.SetReadDeadline(time.Now().Add(idleTimeout))` | 低（~15 行） | `internal/mcp/transport.go` | ✅ **第一步**——防止僵尸进程 |
| **EventBus 慢订阅者保护**：替换满 channel 为新的带缓冲 channel + 告警 | 中低（~30 行） | `internal/events/bus.go:Subscribe` + `broadcast` | ✅ **第二步**——事件可靠性基础 |
| **Multipart 上传 TTL 与 GC**：`InitMultipart` 记录 `ttl`，reconcile 作业扫描过期上传并 Abort | 中（~100 行） | `internal/storage/local_multipart.go` + `internal/reconcile/job.go` | ✅ **第二步**——存储效率基础 |
| **ChatStream 断线续传**：缓存已发送 token，客户端使用 `X-Last-Event-ID` 续接 | 高（缓存策略 + SSE 改造） | `internal/api/rest/search.go` + 新 `streamBuffer` | ❌ **第三步**——超出当前阶段，可在 WebSocket 升级时一并引入 |

### 边界情况

| 场景 | 影响 | 处理策略 |
|------|------|---------|
| SSE 客户端断连后 `r.Context()` 已取消，但 `http.Flusher` 仍在写入 | `Flush()` 在已关闭连接上 panic | 对 `Flush()` 的调用应包装在 `recover()` 或检查 `r.Context().Err()` 中 |
| MCP stdio 模式下 `bufio.Scanner` 的默认最大行 64KB——大 JSON-RPC 消息被截断 | MCP 工具返回大数据时消息被静默截断，JSON 解析失败 | 设置 `scanner.Buffer(make([]byte, 0, 4<<20), 4<<20)` 适配 MCP 大消息 |
| 多个慢订阅者同时导致 `broadcast` 对所有 subscriber 打 default | 所有非慢订阅者也丢失事件 | 慢订阅者独立处理：慢的 channel 记录告警并替换为新 channel（防止"一颗老鼠屎坏了一锅汤"） |
| Client 断开后 `r.Context()` 立即 Cancel，但 DB 轮询查询可能正好在执行中 | DB 查询消耗资源但结果被丢弃 | 查询前检查 `ctx.Err()`，查询中使用 `QueryContext(ctx, ...)`——`database/sql` 会在 context cancel 时取消查询 |
| ChatStream 断开后已产生的 token 不可重放 | 用户断开后重连无法获取之前已生成的 token，需要从头调用 LLM | 最低成本方案：客户端缓存已收到 token，断连时重发请求并利用 `Idempotency-Key` + `X-Last-Event-ID` 跳过已生成部分 |

---

## 总结：按优先级排序的行动建议

| 优先级 | 方向 | 快速闭环（1-2 天） | 中期架构（1-2 周） | 长期演进（1-3 月） |
|--------|------|-------------------|-------------------|-------------------|
| **P1** | 方向三：策略-动作鸿沟 | Legal Hold GET 拦截 + 访问日志 middleware 调用 `WriteAccessLog` | 通知引擎骨架（路由到全局 webhook） | 完整的通知引擎（SQS/SNS/Lambda 适配器） |
| **P1** | 方向一：运维韧性 | `sync.WaitGroup` 追踪后台 goroutine + `DRAINING` 状态 + `/healthz?full=1` | 组件 `GracefulShutdown` 接口 + `config.Reload()` 框架 | 配置热加载全组件覆盖 + 阶段式关闭状态机 |
| **P2** | 方向二：跨协议 QoS | WebDAV 归入 chi 中间件链 + path-based rate limit 规则 | 租户级请求配额（`rps_quota` 字段） | 请求优先级队列 + MCP stdio 速率控制 |
| **P2** | 方向五：连接韧性 | SSE `r.Context()` 监听修复 + MCP stdio ReadDeadline | EventBus 慢订阅者保护 + multipart TTL/GC | ChatStream 断线续传 + SSE 心跳 |
| **P2** | 方向四：成本归因 | AI 管线全链路成本（embed/rerank/extract cost_micros） | 存储成本每日快照 + 后端定价映射配置 | 统一成本 API + 租户账单导出 |

---

> **文档生成方法：** 逐文件扫描 `cmd/server/main.go` + `internal/` 全部 24+ 子包，识别 5 类架构缺口：① 运维生命周期（关闭/重载/健康）；② 运营治理（QoS/优先级/隔离）；③ 策略执行鸿沟（配置完整但零执行）；④ 成本模型（归因/分摊/计费）；⑤ 连接韧性（断连/续传/泄漏）。每类缺口在对 88 份既有 expansion 文档的穷尽式关键词验证后撰写。
