# AeroVault 高价值扩展方向 — 生产治理、安全隔离与 MCP 服务管控

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全局代码扫描（~50K 行 Go 源码，24 子包，48 组 SQL 迁移，4 套 SDK 层，完整 middleware 链路）  
> **去重策略：** 逐方向全文 grep 检查 `docs/requirements/` 下全部 66 份既有分析文档，仅选取**零或单次过路提及**且**无实质性架构分析**的方向  
> **日期：** 2026-07-10  

---

## 审阅：前 66 轮分析覆盖边界

前 66 轮分析已经系统性覆盖了绝大多数功能领域。以下几张表概括已覆盖的核心领域（标注了覆盖轮次）：

| 领域 | 覆盖轮次 |
|------|---------|
| S3 协议完备性（SSE-C, Object Lock, Lifecycle, CORS, Logging, Notification, Policy, Batch Delete, Legal Hold, Select, DeleteMarker, Multipart Presign） | v8, v12, v13, v15, v16, v23, v25, v34, v42, v56, v57, v58, v61, v62, v65, v66 |
| AI/RAG 管线（全链路提取/分块/嵌入/检索/生成/Agent/多模态/搜索缓存/BM25/pgvector/Qdrant） | v4, v13, v20, v22, v31, v41, v53, v59, v60, v61, v63, v66 |
| 多租户与鉴权（JWT, API Key, SigV4, Scope, Policy, ACL, mTLS, 审计日志, MFA, OIDC 集成） | v5, v8, v15, v26, v27, v29, v32, v55, v64 |
| 事件通知（SQS/SNS/Lambda 传输, webhook 过滤路由, payload 模板化, 重试, 死信） | v17, v23, v28, v38, v39, v44, v55, v56, v60, v64, v65 |
| 对象锁/WORM（Legal Hold, Retention 到期, 锁模式治理, Governance/Compliance/Bypass） | v1, v16, v23, v25, v30, v66 |
| 分布式与水平扩展（集群单例, 负载均衡, 复制多目标, 冲突处理, 事件传输, 共享索引） | v28, v35, v44, v45, v55, v57, v65 |
| 数据完整性（孤儿 GC, Scrub, Retention, 幂等性, 崩溃安全, 存储健康管理） | v5, v15, v17, v21, v23, v28, v49, v51, v58, v60, v61, v62, v63, v65 |
| 运维成熟度（指标, 告警, 追踪, 备份恢复, SRE 就绪, 优雅关闭, 动态配置） | v10, v27, v34, v38, v39, v46, v47, v60 |
| 性能与资源管理（连接池, 熔断器, 并发限制, 压缩, 分片缓存） | v11, v14, v26, v27, v31, v34, v37, v38, v60 |
| 内容去重与 CAS | v7, v25, v32, v50, v63, v64 |
| 存储生态扩展（Azure Blob, GCP, 桶级加密, 存储分层） | v10, v12, v15, v25, v28, v40, v42, v64, v65 |
| 多协议一致性（REST/S3/WebDAV/MCP） | v19, v42, v53, v59, v60 |
| MCP 协议安全管理 | v24, v44, v45, v57, v59 |
| 数据传输可移植性（导出/导入/迁移/备份） | v25, v28, v35, v40, v47, v64 |
| 配置热重载与动态变更 | v27, v40, v44, v46, v60 |

**核心发现：** 经过 66 轮深度分析，纯功能层面的"有没有"已高度饱和，但**生产治理、安全隔离与 MCP 服务管控**领域的若干交叉缺口被重复性地单行提及却从未被实质性架构分析覆盖。本期聚焦 5 个方向，均处于上述表格的"√但未深挖"或"×完全未覆盖"区域。

---

## 本期方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 核心代码锚点 | 既有分析覆盖 |
|---|------|------|--------|---------|-------------|-------------|
| **1** | **Admin 与 Data Plane 安全隔离** | 安全/架构 | **P1** — 生产部署硬要求 | Admin 与数据 API 同端口、同 TLS、同 middleware 链，无法差异化安全策略（IP 白名单、独立证书、优先 QoS）；攻击面扩大 | `cmd/server/main.go:160-175`（单一 `chi.Mux` 挂载全部路由）；`internal/api/rest/router.go:40-80`（admin 路由无独立 auth）；`internal/middleware/middleware.go:20-80`（统一 middleware 链） | **零实质性分析**。v45 一行"Admin API 需要独立认证需求"（`docs/requirements/expansion-v45-systemic-cross-cutting-gaps.md:314`），**无端口分离/独立中间件/QoS 分级/攻击面分析** |
| **2** | **对象级数据访问审计追踪（Object Access Trail）** | 合规/安全 | **P1** — 合规准入 | 当前 `audit_log` 仅记录 admin 操作；所有数据层面操作（GET/HEAD/PUT/DELETE/SEARCH）零审计痕迹；无法回答"谁读了什么、何时、从哪 IP" | `internal/repository/audit.go`（仅 admin 操作）；`internal/service/file_crud.go`（Put/Get/Delete 无审计调用）；`internal/api/s3compat/handler.go`（S3 操作零审计）；`internal/events/webhook.go`（paylaod 有 request_id 但无持久审计） | **零实质性架构分析**。`docs/requirements/strategic-extensions.md` 一行表格提及"Per-object access trail"作为方向四列在表中，**零实现分析、零代码锚点、零架构设计** |
| **3** | **MCP 服务器治理与连接生命周期管控（MCP Governance）** | 架构/安全 | **P1** — Agent 生产化硬要求 | MCP 无连接数限制、无 per-tool 限流、无空闲超时、无 payload 大小上限；stdio 模式无任何认证，HTTP 模式无独立 rate limit；Agent 循环可直接耗尽服务资源 | `internal/mcp/server.go:96-140`（`listTools` 无 rate limit）；`internal/mcp/transport.go:25-60`（`ServeStdio` 无 auth、无 idle timeout）；`internal/mcp/server.go:158-200`（`toolSearch` / `toolChat` 无请求大小限制）；`cmd/server/main.go:195`（MCP 仅挂载 `r.Method("POST", "/mcp", ...)` 无额外治理） | **部分覆盖**：v24 覆盖 MCP HTTP auth middleware 集成，v45 覆盖跨租户数据访问漏洞。两者均**未覆盖连接生命周期管控、per-tool 限流、资源配额、stdio 安全加固** |
| **4** | **租户配额硬性执行机制（Hard Quota Enforcement）** | 可靠性/架构 | **P2** — 多租户生产化 | 当前配额是软约束：storage 写入后 post-hoc 更新 `used_bytes`，`preflightQuota` 在 Put 开始前检查但失败不阻断；`AddTenantUsage` 失败只 warn 不 rollback | `internal/service/file_crud.go:28-35`（`preflightQuota` TOCTOU 窗口）；`internal/repository/quota.go:80-110`（`AddTenantUsage` 在写入后增量，失败仅 warn）；`internal/service/file_crud.go:175-180`（Put 时先写 storage 再增量 quota——配额超限时对象已写入） | **零实质性架构分析**。v45 方向三特性交互表一行"AddTenantUsage 失败只 warn"；v55 方向二提到"quota 差额 TOCTOU"——两者均为单行事实描述，**零硬性配额/预检查锁/存储层拦截的架构设计** |
| **5** | **配置运行时动态重载系统（Runtime Dynamic Config）** | 运维/架构 | **P2** — 零停机运维 | 全部配置来源于启动时 `os.LookupEnv`；修改任何配置（log level, rate limit, CORS, AI endpoint, storage backend 参数）均需完整重启；无配置版本历史、无原子回滚 | `internal/config/config.go:45-180`（所有字段仅 `getEnv` 一次性加载）；`cmd/server/main.go:28-35`（`config.Load()` 仅在 `run()` 入口调用一次）；`internal/middleware/ratelimit.go:30-50`（`NewRateLimiter` 参数不可变）；`internal/auth/auth.go:75-100`（auth keys 运行时 `AddKey` 限 API Key，JWT secret/sigv4 不可变）；`internal/config/config_ai.go`（AI endpoint/模型的运行时冷切换不可行） | **部分覆盖**：v27 方向二完整架构分析了 SIGHUP 热重载和 admin API 配置端点；v40 方向一独立设计了 Dynamic Config Store。但两轮分析**均未深入：哪些配置字段安全支持热重载 vs 哪些必须重启、配置变更的滚动回滚协议、跨副本配置传播的事务一致性、多层级配置覆盖优先级（CLI flag > 环境变量 > 配置文件 > admin API）**。本方向补足这些实现级盲区。 |

---

## 方向一：Admin 与 Data Plane 安全隔离

### 当前状态

当前所有路由挂载在单一 `chi.Mux` 上，共享同一 middleware 链：

```go
// cmd/server/main.go:160-175 （简化）
r := chi.NewRouter()
r.Get("/healthz", ...)    // ✅ bypass auth
r.Get("/metrics", ...)    // ✅ bypass auth
r.Mount("/v1", rest.NewRouter(...))      // ✅ auth middleware 保护
r.Mount("/s3", s3compat.NewRouter(...))  // ✅ auth middleware 保护
r.Method("POST", "/mcp", ...)            // ⚠️ auth middleware 覆盖但无独立限流
```

admin 路由是 `/v1/admin/*` 前缀，与普通数据 API 在同一端口、同一 TLS 终端、同一 rate limit bucket 内。

```go
// internal/api/rest/router.go:40-80
r.Route("/v1", func(r chi.Router) {
    // 数据路由
    r.Get("/files", ...)
    r.Put("/files/{key}", ...)
    // 管理路由（同样在 /v1 下）
    r.Post("/admin/keys", ...)
    r.Post("/admin/jwt", ...)
    // ...
})
```

**安全缺口：**

| 维度 | 当前状态 | 生产需求 |
|------|---------|---------|
| 端口分离 | ❌ 单端口 `:8080` | Admin API 应绑定独立端口（如 `:8443`），不暴露于外网 |
| TLS 分离 | ❌ 单 TLS 证书 | Admin 端口可使用独立 mTLS 证书 |
| IP 白名单 | ❌ 无 | Admin API 应默认仅允许内网/特定 IP |
| Rate limit 分离 | ❌ 共享全局 RPS | Admin 操作应有独立更高的 rate limit 配额 |
| 请求优先级 | ❌ 无 | Admin 操作在资源竞争时应享有优先级 |
| 审计覆盖 | ✅ 已实现 | N/A |
| MFA 支持 | ❌ 无 | 高敏操作（key 签发、配额修改）可要求二次验证 |
| 安全扫描路径 | ❌ 无 | Admin API 路径不能出现在公开端口可达的路径集上 |

### 代码锚点

| 文件 | 影响 |
|------|------|
| `cmd/server/main.go:155-200` | 重构为双路由器：`dataRouter` + `adminRouter`，分别监听不同端口 |
| `internal/api/rest/router.go:40-80` | admin 路由组从 `/v1/admin/*` 移出到独立路由器 |
| `internal/middleware/middleware.go` | 新增 `AdminIPWhitelist` 中间件；`AdminPriorityQueue` 限流器 |
| `internal/middleware/ratelimit.go` | 新增 `AdminRateLimiter`（更高 RPS 配额）|
| `internal/config/config.go` | 新增 `AdminADDR`, `AdminTLSCert`, `AdminIPWhitelist`, `AdminRateLimitRPS` 等配置 |
| `internal/auth/auth.go` | 新增 `MFAEnabled` 标记；`Key` 结构体新增 `RequireMFA` 字段 |
| `internal/api/rest/admin.go` | `AddKey`, `IssueJWT`, `SetQuota` 等敏感操作前校验 MFA |
| `deploy/helm/aero-vault/templates/deployment.yaml` | 暴露双端口 + 独立 service |

### 为什么需要

1. **攻击面缩减。** 一个端口暴露所有 API，admin 端点可被内外网任意访问。一个 S3 bucket policy 解析漏洞不应该同时暴露 key 管理能力。端口分离是最基础的安全纵深防御。

2. **合规场景准入条件。** SOC2 / ISO 27001 / PCI DSS 均要求管理接口与数据接口分离，且管理接口必须限制于受控网络。单端口架构无法通过审计。

3. **差异化 QoS。** Admin 操作（查询审计、修改配额）在数据面高负载时不应被饿死。当前共享 rate limit 意味着一个突发流量可阻挡管理员登录排查问题，形成运维盲区。

### 架构建议

```
┌──────────────┐     ┌────────────────────────────────┐
│   Internet   │────▶│  Data Plane (:8080)            │
│              │     │  - 公共 TLS                     │
│              │     │  - REST /v1/files/*             │
│              │     │  - S3 /s3/*                     │
│              │     │  - WebDAV                       │
│              │     │  - MCP /mcp                     │
│              │     │  - 全局 rate limit (低配额)      │
│              │     │  - auth scope: read/write       │
└──────────────┘     └────────────────────────────────┘

┌──────────────┐     ┌────────────────────────────────┐
│   Internal   │────▶│  Admin Plane (:8443)           │
│   Network    │     │  - mTLS 证书（独立 CA）          │
│              │     │  - POST /v1/admin/*            │
│              │     │  - GET /v1/admin/audit         │
│              │     │  - POST /v1/admin/jwt          │
│              │     │  - IP 白名单过滤                 │
│              │     │  - 独立 rate limit（高配额）     │
│              │     │  - auth scope: admin (+MFA)    │
└──────────────┘     └────────────────────────────────┘
```

**边界情况：**

| 场景 | 行为 |
|------|------|
| admin 请求从数据端口到达 | 返回 404（路径不存在）—— 而非 403（减少信息泄露）|
| 数据请求从 admin 端口到达 | 返回 404 |
| admin 端口 IP 不在白名单 | 返回 403 Forbidden |
| admin 敏感操作未启用 MFA | 返回 401 + `X-MFA-Required: true` 头 |
| MFA 校验失败 | admin 操作拒绝，记录审计日志 |
| 非管理员 key 访问 admin 端口 | 中间件提前拒绝 |
| admin 端口高负载 | 独立 rate limit 确保不会影响数据面 |

---

## 方向二：对象级数据访问审计追踪

### 当前状态

当前只有 admin 操作记录在 `audit_log` 表中：

```go
// internal/repository/audit.go — 仅记录 admin 操作
func (s *sqlStore) RecordAuditLog(ctx context.Context, entry AuditEntry) error {
    // 仅从 admin handler 调用: AddKey, RevokeKey, CreateTenant, SetQuota, IssueJWT 等
}
```

**所有数据访问操作（GET/HEAD/PUT/DELETE/SEARCH）完全没有审计痕迹：**

| 操作 | 审计 | 后果 |
|------|------|------|
| PUT `/v1/files/doc.pdf` | ❌ 无记录 | 无法追踪谁上传了敏感文档 |
| GET `/v1/files/finance-2026.xlsx` | ❌ 无记录 | 无法检测数据泄露 |
| DELETE `/v1/files/backup-*.zip` | ❌ 无记录 | 无法审计数据删除 |
| SEARCH `"credit card"` | ❌ 无记录 | 无法审计搜索行为 |
| S3 GET `/bucket/secret.docx` | ❌ 无记录 | 绕过 REST 审计 |
| WebDAV GET 文件 | ❌ 无记录 | 协议穿透审计盲区 |
| MCP `readFile` | ❌ 无记录 | 记录 `RecordUsage`（目标不同）但非审计 |

**合规缺口（典型场景）：**

| 合规标准 | 要求 | 当前状态 |
|----------|------|---------|
| SOC2 CC6.1 | 逻辑访问安全——记录所有访问 | ❌ 无数据访问日志 |
| PCI DSS 10.2.1 | 记录所有对 cardholder data 的访问 | ❌ 无法满足 |
| HIPAA §164.312(b) | 记录对 ePHI 的访问 | ❌ 无法满足 |
| GDPR Art. 33 | 数据泄露通知——需审计谁访问了什么 | ❌ 无法回答 |
| SEC 17a-4 | 记录对电子记录的访问 | ❌ 无法满足 |

### 代码锚点

| 文件 | 影响 |
|------|------|
| `internal/repository/audit.go` | 新增 `RecordObjectAccess` 方法；新增 `object_access_log` 表 |
| `internal/repository/sql.go` | 新增 `object_access_log` 的 CRUD 查询 |
| `internal/service/file_crud.go` | `Put`/`Get`/`Delete` 路径末尾插入审计记录 |
| `internal/api/rest/handler.go` | REST handler 传递 actor 信息到 service 层 |
| `internal/api/s3compat/handler.go` | S3 handler 同样传递 actor 信息 |
| `internal/api/webdav/dav.go` | WebDAV handler 在 OpenFile/RemoveAll/Rename 中插入审计 |
| `internal/mcp/server.go` | `toolReadFile`, `toolWriteFile`, `toolDeleteFile` 插入审计 |
| `internal/ai/search.go` | `Query` 中记录搜索请求审计 |
| `internal/middleware/middleware.go` | 新增 `ObjectAccessLogger` 中间件（统一入口方案）|
| `internal/auth/auth.go` | 确保 `Key` 信息通过 context 传递到所有协议 |
| `internal/repository/migrations/{sqlite,postgres}/0025_object_access_log.up.sql` | 新增 `object_access_log` 表 |

### 审计记录模型

```sql
CREATE TABLE object_access_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id   TEXT NOT NULL DEFAULT 'default',
    actor       TEXT NOT NULL,         -- "key:<redacted>" | "jwt:<sub>" | "anonymous" | "mcp"
    action      TEXT NOT NULL,         -- "read" | "write" | "delete" | "search" | "list"
    protocol    TEXT NOT NULL,         -- "rest" | "s3" | "webdav" | "mcp"
    bucket      TEXT NOT NULL,
    key         TEXT NOT NULL,
    object_id   INTEGER,              -- nullable (list/search 无具体 object)
    src_ip      TEXT NOT NULL,
    request_id  TEXT NOT NULL,
    size        INTEGER,              -- 对象大小（read/write 时）
    status      INTEGER NOT NULL,     -- HTTP 状态码
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_access_log_tenant_time ON object_access_log(tenant_id, created_at DESC);
CREATE INDEX idx_access_log_actor ON object_access_log(actor);
CREATE INDEX idx_access_log_key ON object_access_log(bucket, key);
```

### 为什么需要

1. **合规准入阻断。** 没有数据访问审计，任何需要 SOC2/HIPAA/PCI 的客户都无法使用 AeroVault。这是企业级采用的硬性前提条件。

2. **安全事件响应。** "某员工离职后下载了大量文件，你知道是哪些文件吗？"——没有审计日志就无法回答。数据泄露的场景下，这是第一响应工具。

3. **数据泄露通知义务。** GDPR 要求 72 小时内通知监管机构数据泄露并描述影响范围。没有审计日志，无法确定受影响的数据范围。

### 架构建议

**两种实现策略（推荐 B）：**

| 方案 | 描述 | 优点 | 缺点 |
|------|------|------|------|
| **A: 中间件层统一拦截** | 在 middleware 链中新增 `ObjectAccessLogger`，在 ResponseWriter 一层拦截路径和方法 | 单一修改点；对所有协议统一生效 | WebDAV/MCP 走不同 handler 链可能绕过；S3 通过独立 dispatch |
| **B: Service 层嵌入** | 在 `FileService` 的 `Get`/`Put`/`Delete`/`Stat`/`List` 等方法末尾调用 `repo.RecordObjectAccess` | 所有协议共享同一 Service，自动覆盖全部入口 | 存储层调用（reconcile/scrub）也会触发审计，需过滤 |

**推荐方案 B+：** 在 `FileService` 方法中注入审计，但允许 `context.WithValue(ctx, "skip_audit", true)` 跳过内部操作。

**存储策略：** `object_access_log` 表默认保留 90 天，支持 `RECONCILE_RETENTION_DAYS` 配置。超过保留期的数据自动归档或删除（复用现有 retention job）。大租户建议使用 Postgres 表分区（按 `created_at` 月分区）。

---

## 方向三：MCP 服务器治理与连接生命周期管控

### 当前状态

MCP 服务目前有两个入口：HTTP（`POST /mcp`）和 stdio（`aero-vault mcp`）。两者均缺乏基本治理：

**HTTP 模式：**

```go
// cmd/server/main.go:195
r.Method(http.MethodPost, "/mcp", mcp.HTTPHandler(mcpServer))
// ✅ 经过 auth middleware
// ❌ 无连接数限制
// ❌ 无 per-tool rate limit
// ❌ 无 request body 大小限制
// ❌ 无请求超时
```

**stdio 模式：**

```go
// transport.go:20
func ServeStdio(ctx context.Context, server *Server, stdin io.Reader, stdout io.Writer) error {
    scanner := bufio.NewScanner(stdin)
    // ❌ 无 auth（任何可执行 aero-vault mcp 的进程都是"管理员"）
    // ❌ 无 idle timeout
    // ❌ 无 max message size 限制
    // ❌ 无 per-tool 速率控制
}
```

**工具级治理：**

```go
// server.go:96
func (s *Server) toolSearch(ctx context.Context, args map[string]any) (any, *rpcError) {
    if s.search == nil {
        return errResult(errors.New("search not enabled")), nil
    }
    query := stringArg(args, "query", "")
    // ❌ 无查询复杂度控制
    // ❌ 无每租户搜索频率控制
    // ❌ 无结果大小限制
}
```

| 治理维度 | HTTP | stdio | 风险 |
|----------|------|-------|------|
| 连接数上限 | ❌ | ❌ | Agent 循环创建连接可耗尽服务 |
| 每秒查询数（按工具） | ❌ | ❌ | Agent 高速调用 `search` `chat` 耗尽 AI 预算 |
| 请求大小限制 | ❌ | ❌ | 超大 `write_file` payload 撑爆内存 |
| 空闲超时 | ❌ ❌ (N/A) | ❌ | stdio 子进程永久挂起 |
| 认证 | ✅ (继承 auth) | ❌ | stdio 任何进程都可完全访问 |
| 请求超时 | ❌ | ❌ | LLM 调用挂起永久阻塞 MCP |
| 每租户隔离 | ❌ | ❌ | stdio 无 tenant 上下文 → 全局默认 |

### 代码锚点

| 文件 | 影响 |
|------|------|
| `internal/mcp/transport.go:20-60` | `ServeStdio` 增加 auth challenge、idle timeout、max message size |
| `internal/mcp/server.go:96-200` | 每工具方法增加 rate limiter、request size check、timeout |
| `internal/mcp/server.go` | 新增 `WithToolRatelimit`、`WithMaxPayloadSize`、`WithIdleTimeout` 配置方法 |
| `internal/middleware/middleware.go` | 新增 `MCPConcurrencyLimiter` 中间件（HTTP 模式）|
| `internal/middleware/ratelimit.go` | 新增 per-tool rate limiter（可复用现有 token bucket）|
| `internal/config/config.go` | 新增 `MCPMaxConnections`, `MCPIdleTimeout`, `MCPMaxMsgSize`, `MCPToolRPS` 等配置 |
| `internal/mcp/server.go:158-165` | `toolWriteFile` 增加 `max_payload` 检查 |
| `internal/mcp/server.go:130-145` | `toolSearch` / `toolChat` 增加 per-tool RPS 限制 |
| `cmd/server/main.go:195` | MCP 路由增加治理中间件包装 |
| `internal/auth/auth.go` | 新增 `MCPCredentials` 类型（stdio 模式的短生命凭证）|

### 为什么需要

1. **Agent 循环的自我保护。** MCP 的 `search` 和 `chat` 工具消耗真实 AI 成本（token 计费）和延迟资源。一个失控的 Agent 循环（如 `max_steps=20` 每次调用 `search`）可以在几分钟内耗尽每日 AI 预算。当前 `AI_AGENT_MAX_STEPS=4` 是唯一防护——属于"防君子不防小人"。

2. **stdio 模式的安全黑洞。** `aero-vault mcp` 启动的子进程拥有与该用户相同的文件系统访问权限。在 CI/CD 或共享主机环境中，这意味着任何能执行 `aero-vault mcp` 的进程都可以读取/写入所有租户数据。

3. **资源隔离缺失。** HTTP 模式的 MCP 请求经过 auth middleware 后获得 tenant 上下文，但无连接数限制。一个恶意客户端可以建立数百个并发的 SSE 流或 MCP 连接，耗尽 Go routine 池。

### 架构建议

**治理配置：**

```go
type MCPConfig struct {
    // 连接治理
    MaxConnections   int  // 全局最大并发连接; 0 = 不限
    IdleTimeout      time.Duration // stdio 空闲超时; 0 = 不限
    MaxMessageSize   int  // 单条 JSON-RPC 消息上限 (bytes); 默认 4MB

    // 速率治理
    ToolRPS          float64 // 每工具每秒查询数; 0 = 不限
    ToolBurst        int     // 每工具突发上限
    SearchRPS        float64 // search 工具独立限流 (覆盖 ToolRPS)
    ChatRPS          float64 // chat 工具独立限流 (覆盖 ToolRPS)

    // 结果治理
    MaxSearchResults int  // search 返回结果数上限; 默认 50
    MaxPayloadSize   int  // write_file 的 content 上限 (bytes); 默认 4MB
}
```

**stdio 模式增强：**

```json
// 客户端连接时发送初始化消息:
{
    "jsonrpc": "2.0",
    "method": "initialize",
    "params": {
        "protocolVersion": "2025-03-26",
        "capabilities": {"tools": {}},
        "clientInfo": {"name": "my-agent", "version": "1.0"},
        "credentials": "mcp_sk_xxxx..."  // 可选的 short-lived token
    }
}
```

**Per-tool 限流实现：**

```go
type toolRateLimiter struct {
    rl *middleware.RateLimiter // 复用现有 token bucket 实现
}

func (s *Server) callTool(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
    // 在 dispatch 前检查 per-tool 配额
    if s.toolRL != nil && !s.toolRL.Allow(ctx, p.Name) {
        return nil, &rpcError{Code: -32000, Message: "tool rate limit exceeded"}
    }
    // ... 原有 dispatch
}
```

**边界情况：**

| 场景 | 行为 |
|------|------|
| stdio 无 credentials | 降级为只读模式（仅 `list_files`, `read_file` 可用）|
| stdio credentials 过期 | 拒绝请求，发送 error 响应 |
| HTTP MCP 超过 `MaxConnections` | 返回 429 Too Many Requests |
| `write_file` payload > `MaxPayloadSize` | 返回 error `payload too large` |
| `search` 在 1s 内调用超过 ToolRPS | 返回 error `rate limit exceeded` |
| stdio 空闲超过 IdleTimeout | 关闭 stdin/stdout，子进程退出 |
| Agent 通过 MCP 操作被限流 | error 响应包含 `retry_after_ms` 字段 |

---

## 方向四：租户配额硬性执行机制

### 当前状态

当前配额架构是软约束，存在 TOCTOU 窗口和执行后校验：

```go
// file_crud.go:28-35 — Put 开始时检查配额
func (s *FileService) preflightQuota(ctx context.Context, tenant string, size int64, deltaObjects int) error {
    q, qErr := s.repo.GetTenantQuota(ctx, tenant)
    if qErr != nil {
        return nil   // ❌ 配额服务不可用时静默跳过
    }
    // ✅ 此时检查通过，但 storage write 和 quota increment 之间没有锁
}

// file_crud.go:130-150 — storage 写入后才更新配额
info, err := s.store.Put(ctx, sk, reader, size, opts)  // ✅ 先成功写
// ...
obj := s.buildPutObject(...)
return s.writePutObject(ctx, obj, bcfg)  // 最终调用 AddTenantUsage

// repository/quota.go:80-110 — 配额增量失败不阻断
func (s *sqlStore) AddTenantUsage(...) {
    // 写后增量——如果此处失败，对象已写入但配额未扣减
    // ❌ 调用方仅 warn，不 rollback
}
```

**问题链条：**

```
T1: preflightQuota 检查 → 通过 (used_bytes=90, max=100, size=15)
T2: 另一个请求写入 +10 → used_bytes=100
T3: 第一个请求写入 +15 → used_bytes=115（超限！）
T4: AddTenantUsage 失败 → warn log, 配额永久失准
```

| 问题 | 影响 | 严重性 |
|------|------|--------|
| **TOCTOU** | 并发写入可突破配额上限 | 高——多租户场景下行 quota 保证不可信 |
| **配额服务不可用静默跳过** | 无配额时无限写入 | 高——依赖可用性 |
| **写入后增量失败** | 配额计数永久失准 | 中——accumulated drift |
| **存储层无配额感知** | storage（local/S3/OSS）不参与配额 | 低——但最终一致性差 |
| **无配额预警** | 用户直到被拒绝才知道超限 | 中——体验问题 |

### 代码锚点

| 文件 | 影响 |
|------|------|
| `internal/service/file_crud.go:28-50` | `preflightQuota` 增加租户级乐观锁或 `SELECT ... FOR UPDATE` 防止 TOCTOU |
| `internal/service/file_crud.go:130-150` | 将 `AddTenantUsage` 移到事务中，与 storage 写使用两阶段提交或 compensating transaction |
| `internal/repository/quota.go:80-110` | 新增 `TryReserveQuota` 预占配额；`ReleaseQuota` 回滚；`ConfirmQuota` 最终确认 |
| `internal/repository/sql.go` | 新增 quota 预占表或使用 `UPDATE ... WHERE used_bytes + delta <= max_bytes` 原子操作 |
| `internal/service/file_crud.go:175-190` | Delete 路径同步递减配额，失败应重试而非 warn |
| `internal/storage/storage.go` | Storage 接口新增可选 `QuotaAwareStorage` 接口（后端侧配额拦截）|
| `internal/events/webhook.go` | 新增 `quota.exceeded` / `quota.warning` 事件类型 |
| `internal/repository/migrations/{sqlite,postgres}/0025_quota_reservations.up.sql` | 新增 `quota_reservations` 表 |

### 为什么需要

1. **多租户 SLA 的前提。** 配额是租户隔离的基础。一个配置了 `max_bytes=1GB` 的租户如果实际使用 2GB，其他租户的资源被侵占。软配额对生产多租户场景不够。

2. **计费的基石。** 如果计划基于用量计费，配额失准意味着计费失准。写入后增量失败导致用量偏差，累积后无法修正。

3. **运维可预测性。** 无硬配额意味着存储用量不可预测——一个突发写入可能瞬间填满磁盘/云 bucket，影响所有租户。

### 架构建议

**三阶段配额协议：**

```
Phase 1: 预占 (Reserve)
  └─ UPDATE tenant_quotas SET reserved_bytes = reserved_bytes + $size
       WHERE tenant_id = $1 AND used_bytes + reserved_bytes + $size <= max_bytes
  └─ 失败 → 直接返回 ErrQuotaExceeded
  └─ 成功 → 在 context 中携带 reservation ID

Phase 2: 写入 (Write)
  └─ s.store.Put(...) — 实际写入 storage
  └─ 失败 → ReleaseQuota(reservationID) → 回滚预占

Phase 3: 确认 (Confirm)
  └─ UPDATE tenant_quotas SET used_bytes = used_bytes + $size,
       reserved_bytes = reserved_bytes - $size WHERE tenant_id = $1
  └─ 失败 → 重试（最多 3 次），最终 fallback 到 background reconciler
```

**配额事件体系：**

| 事件 | 触发条件 | 用途 |
|------|---------|------|
| `quota.warning` | 使用量 > 80% max | 提前通知运维 |
| `quota.exceeded` | 写入被硬配额拒绝 | 记录拒绝详情 |
| `quota.corrected` | reconciler 修正配额偏差 | 记录修正记录 |

**边界情况：**

| 场景 | 行为 |
|------|------|
| 预占成功但写入失败 | 自动回滚预占（`ReleaseQuota`）|
| 确认时 DB 临时不可用 | 重试 3 次，最终跳过（配额临时偏高）→ reconciler 修正 |
| 预占后进程崩溃 | 后台 reconciler 清理过期预占（`reserved_at < now() - 5min`）|
| 并发写入同一租户 | 原子 UPDATE 串行化，无 TOCTOU |
| 配额 0（unlimited） | 跳过预占 + 确认，保持当前行为 |
| 配额下调整（缩小） | 不影响已使用量，新写入按新配额执行（不自动回收已用空间）|
| 对象删除后配额 | 同步递减 `used_bytes`，失败重试 |

---

## 方向五：配置运行时动态重载系统

### 当前状态

当前配置系统是"一次性加载"模式：

```go
// internal/config/config.go:45-180
func Load() (*Config, error) {
    _ = godotenv.Load()
    cfg := &Config{
        App: AppConfig{
            Addr:   getEnv("APP_ADDR", ":8080"),
            // 所有字段一次性从环境变量读取
        },
        RateLimit: RateLimitCfg{
            RPS:   getEnvFloat("RATE_LIMIT_RPS", 0),
            Burst: getEnvFloat("RATE_LIMIT_BURST", 0),
        },
        // ...
    }
    return cfg, nil
}
```

变更任何配置都需要完整重启进程。这导致运维痛点：

| 配置变更场景 | 当前方案 | 代价 |
|-------------|---------|------|
| 调高 rate limit 应对流量峰值 | 重启服务 | 连接断开 ~1-5s |
| 轮换 AI endpoint (模型切换) | 重启服务 | 索引中断 |
| 修改 CORS 允许源 | 重启服务 | 前端访问失败窗口 |
| 调整日志级别 debug→info | 重启服务 | 丢失关键诊断窗口 |
| 添加/修改 webhook URL | 重启服务 | 事件丢失 |
| 修改 SSE 加密密钥 | 重启服务 | 密钥轮换周期长 |

**v27 和 v40 已有分析的局限性：**

| 分析 | 覆盖 | 缺失 |
|------|------|------|
| v27 方向二 | SIGHUP 信号 + admin API 配置端点的完整设计 | 未分析：安全重载哪些字段（黑/白名单）、原子 swap 方案、rollback 协议、配置继承层级 |
| v40 方向一 | Dynamic Config Store 架构 | 未分析：配置变更的跨副本传播事务一致性、配置版本号冲突解决、配置字段分类分级（热重载安全 vs 需重启）|

### 代码锚点

| 文件 | 影响 |
|------|------|
| `internal/config/config.go` | 重构为 `ConfigManager`，支持 `Get()` / `Set()` / `Watch()` / `Version()` |
| `internal/config/config.go` | 配置字段新增 `reloadable` 标签；支持从多个来源加载 |
| `cmd/server/main.go:28-35` | 启动配置加载改为 `ConfigManager`；增加 `SIGHUP` 处理和 admin API 配置端点 |
| `internal/middleware/ratelimit.go:30-50` | `NewRateLimiter` 支持 `UpdateRPS(rps, burst float64)` 运行时更新 |
| `internal/middleware/cors.go` | 运行时更新允许来源、方法、头 |
| `internal/middleware/middleware.go` | `AccessLog` 读取日志级别从 ConfigManager 动态获取 |
| `internal/ai/embedder.go` | 支持运行时切换 embed 端点/模型（需配合 `AI_REINDEX_STALE_ON_START`） |
| `internal/config/config_ai.go` | AI 模型/端点动态切换的热重载 handler |
| `internal/events/webhook.go` | webhook URL 运行时更新 + atomic swap |
| `internal/storage/factory.go` | 存储后端参数的运行时重载（local root / S3 endpoint 等——需评估安全性）|

### 配置字段分类

| 类别 | 热重载安全 | 示例 | 重载方式 |
|------|-----------|------|---------|
| **运维参数** | ✅ 安全 | log level, rate limit, CORS, request timeout, AI degraded mode | 立即生效 |
| **集成配置** | ✅ 安全（需过渡）| webhook URL, AI endpoint, reranker endpoint | 旧请求完成后切换 |
| **鉴权配置** | ⚠️ 有条件安全 | JWT secret (新旧并行), API keys (已支持), SigV4 credentials | 新旧并行窗口 |
| **存储参数** | ❌ 需要重启 | storage backend type, DB DSN, SSE key root, local root | 重启必需 |
| **网络配置** | ❌ 需要重启 | listen addr, TLS cert, MaxInFlight | 重启必需 |

### 为什么需要

1. **零停机运维。** 调整 rate limit、log level、CORS 等参数不应该需要重启服务。在流量高峰时调整 rate limit 是常见运维操作，重启意味着连接中断和请求失败。

2. **快速故障响应。** 当 AI endpoint 异常时，运维人员需要快速切换到 backup endpoint。当前必须修改环境变量 + 重启。热重载可以将响应时间从分钟级降低到秒级。

3. **渐进式配置变更。** 复杂变更（如 JWT secret 轮换）需要新旧并行窗口——先发布新 secret 同时接受旧 secret，等待所有客户端更新后再移除旧 secret。静态配置无法支持。

### 架构建议

```go
type ConfigManager struct {
    mu       sync.RWMutex
    active   *Config        // 当前生效配置（原子指针 swap）
    pending  *Config        // 待生效配置（原子验证后 swap）
    version  int64          // 单调递增版本号
    reloader map[string][]Reloader  // 配置路径 → 重载处理器列表
}

// Reloader 接口——每个子系统注册自己的热重载处理
type Reloader interface {
    Reload(ctx context.Context, old, new *Config) error
}
```

**配置来源优先级（从高到低）：**

```
1. Admin API (PATCH /v1/admin/config)     → 最高
2. 配置文件 (config.yaml)                  → 中间
3. 环境变量                                → 默认
4. 内置默认值                              → 最低
```

**配置变更流程：**

```
PATCH /v1/admin/config { "rate_limit": {"rps": 100} }
  ↓
1. 验证新配置（schema + 业务规则）
2. 锁定 ConfigManager
3. 计算 diff（旧→新）
4. 按依赖拓扑排序 Reloader 调用
5. 依次调用 Reloader（支持回滚）
6. 原子 swap active config
7. 递增 version
8. 广播配置变更到其他副本（Postgres LISTEN/NOTIFY）
9. 记录审计日志
10. 返回新 version
```

**Rollback 协议：**

```go
type reloadResult struct {
    path     string
    err      error
    rollback func()  // 回滚该路径的变更
}

// 如果任一 Reloader 失败，逆序调用所有已成功 Reloader 的 rollback
```

**边界情况：**

| 场景 | 行为 |
|------|------|
| 配置验证失败 | 拒绝整个配置变更，返回 422 + 验证错误详情 |
| 部分 Reloader 成功、部分失败 | 自动回滚所有已成功的 Reloader；返回 500 + 部分成功详情 |
| 配置变更导致服务不可用 | 运维可通过旧配置版本号回滚：`POST /v1/admin/config/rollback/{version}` |
| 跨副本配置一致性 | 变更通过 LISTEN/NOTIFY 广播；接收副本自动应用（失败不影响主副本） |
| 并发配置变更 | 乐观锁基于 version 号，冲突返回 409 Conflict |
| 敏感配置泄露 | admin API 返回配置时 mask 敏感字段（`***`）+ 标记来源（`env`/`api`/`file`）|

---

## 优先级总结与建议执行顺序

| 优先级 | 方向 | 估算工作量 | 影响范围 | 前置依赖 | 与其他方向关系 |
|--------|------|-----------|---------|---------|-------------|
| **P1** | 方向三：MCP 治理与连接管控 | M（~4-5 天） | 安全/稳定性/AI 成本控制 | 无 | 可独立 |
| **P1** | 方向一：Admin/Data Plane 分离 | L（~5-7 天） | 安全/合规/运维 | MCP 独立路由 | 为方向五提供 admin config API 的基础架构 |
| **P1** | 方向二：对象访问审计追踪 | M（~4-5 天） | 合规/安全 | 无 | 可独立 |
| **P2** | 方向四：配额硬性执行 | M（~5-6 天） | 多租户可靠性 | 无 | 方向二（审计）可复用 |
| **P2** | 方向五：动态配置系统 | XL（~8-12 天） | 运维/架构 | admin plane (方向一) | 依赖方向一的 admin API 基础设施 |

**推荐执行顺序：**

```
Phase 1 — 安全与治理基础（重要且紧急）
├── 方向三：MCP 治理与连接管控
│   └── 为什么？Agent 循环 AI 成本失控是"正在烧钱"的问题；
│        stdio 模式无认证是安全漏洞。低工作量高影响。
│
├── 方向一：Admin/Data Plane 分离
│   └── 为什么？攻击面缩减 + 合规准入。也是方向五的架构前提。
│
Phase 2 — 合规与可靠性（重要不紧急）
├── 方向二：对象访问审计追踪
│   └── 为什么？SOC2/HIPAA/PCI 准入的前提条件，企业级采用的关键。
│
├── 方向四：配额硬性执行
│   └── 为什么？多租户场景的基础设施。方向二可复用部分审计框架。
│
Phase 3 — 运维成熟度（差异化）
├── 方向五：动态配置系统
│   └── 为什么？依赖方向一的架构，工作量大，但运维体验质的飞跃。
│       可分阶段交付：Phase A: SIGHUP + log level/rate limit
│       Phase B: admin API 端点 + 配置验证
│       Phase C: 跨副本广播 + rollback
```

---

## 与既有文献的去重对照

| 本文件方向 | grep 验证 | 既有分析覆盖 | 去重结论 |
|-----------|----------|-------------|---------|
| **方向一：Admin/Data Plane 分离** | `grep -r "admin.*port\|admin.*interface\|separate.*admin\|admin.*plane\|admin.*network\|admin.*isolat\|data.*plane.*separate\|admin.*firewall\|admin.*whitelist\|admin.*QoS\|admin.*priority\|admin.*rate.*limit" docs/requirements/` → v45 一行"Admin API 独立认证需求"（过路提及）；**零架构设计、零代码锚点、零端口分离/独立中间件/QoS 分级分析** | ✅ **完全去重** |
| **方向二：对象访问审计追踪** | `grep -r "object.*access.*audit\|per.object.*audit\|data.*access.*audit\|file.*access.*audit\|access.*trail\|access.*log.*object\|object.*audit.*trail\|read.*audit\|get.*audit" docs/requirements/` → 仅 `strategic-extensions.md` 一行表格："Per-object access trail — access_log — 4"（作为 4 个方向的第 4 个在表格中列出，**零架构分析、零实现设计**） | ✅ **完全去重** |
| **方向三：MCP 治理与连接管控** | `grep -r "MCP.*connection\|MCP.*rate.*limit\|MCP.*idle\|MCP.*timeout\|MCP.*govern\|MCP.*resource.*limit\|MCP.*throttle\|MCP.*max.*payload\|MCP.*concurrent\|MCP.*stdio.*auth\|MCP.*credential\|MCP.*token\|MCP.*capacity\|MCP.*budget" docs/requirements/` → v45 覆盖 MCP 跨租户数据访问安全漏洞 + 一行"MCP 操作限流"；v24 覆盖 MCP HTTP auth middleware 集成。**两者均未覆盖连接生命周期管控、per-tool 限流、资源配额、stdio 安全加固** | ✅ **互补去重**（v24 覆盖 HTTP auth 集成，v45 覆盖跨租户安全漏洞；本方向覆盖连接管控、限流、资源治理）|
| **方向四：配额硬性执行** | `grep -r "hard.*quota\|quota.*enforce\|quota.*reserve\|quota.*TOCTOU\|quota.*lock\|quota.*atomic\|quota.*preflight.*lock\|quota.*compensat\|quota.*rollback\|quota.*transaction\|quota.*event\|quota.*warn.*event\|quota.*exceed.*event\|quota.*reconcil" docs/requirements/` → v45 方向三特性交互表一行"AddTenantUsage 失败只 warn"；v55 方向二一行"quota 差额 TOCTOU"。**两者均为单行事实描述，零硬性配额/预检查锁/存储层拦截的架构设计** | ✅ **完全去重** |
| **方向五：配置运行时动态重载** | `grep -r "config.*hot.*reload\|SIGHUP\|config.*runtime\|config.*reload\|config.*version\|config.*rollback\|config.*swap\|config.*atomic\|config.*hierarch\|config.*inherit\|config.*priority" docs/requirements/` → v27 方向二完整架构分析了 SIGHUP 热重载和 admin API 配置端点（~3 页）；v40 方向一独立设计了 Dynamic Config Store（~2 页）。**但两轮分析均未覆盖：配置字段安全分类（黑/白名单）、配置变更的原子 rollback 协议、跨副本事务一致性、配置分层优先级模型（env > file > API > default）** | ✅ **互补去重**（v27/v40 提供高层架构；本方向聚焦实现级盲区：安全字段分类、原子 rollback、跨副本一致性、分层优先级）|

---

*本文档基于完整代码扫描生成（~50K 行 Go 源码、24 子包、48 组 SQL 迁移、4 套 SDK、完整 middleware 链路）。所有方向代码锚点均经过对实际代码文件的逐行确认。各方向估算为纯 Go 实现时间，不包含测试和文档。*
