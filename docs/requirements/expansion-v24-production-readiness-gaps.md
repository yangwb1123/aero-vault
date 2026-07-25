# 高价值扩展方向分析 v24 — 生产就绪盲区与平台化缺口

> **分析范围：** 全代码库扫描（`cmd/server`、`internal/*` 共 237 个 `.go` 文件、`sdk/*`、`deploy/*`、`docs/*`、迁移文件 48 对）
> **分析日期：** 2026-07-10
> **视角：** 资深架构师 / 产品经理 — 聚焦「从功能完整到生产就绪的跨越」
> **去重方法：** 逐一比对前 23 期 expansion 文档、`ROADMAP.md`（10 方向）、`CHANGELOG.md`、`TODO.md`、`docs/requirements/extensions.md`，确认每个方向在既有文档中**零覆盖或仅有表层提及**。
> **原则：** 不编写任何实现代码。

---

## 审阅：前 23 期分析的覆盖范围

前 23 期 expansion 文档（v1–v23）累计覆盖了约 **120+ 个方向**，覆盖领域包括：

| 领域 | 已覆盖方向数 | 典型示例 |
|------|------------|---------|
| 对象存储 CRUD / 多协议适配 | ~14 | S3 子资源完整、WebDAV、MCP 工具集 |
| AI 管线（Extract/Chunk/Embed/Search/BM25/Hybrid/Rerank/Chat/Agent） | ~16 | 增量 BM25、向量模型漂移、搜索缓存 |
| 存储后端（Local/S3/OSS/COS/KMS/SSE/Tiering） | ~12 | 多后端、SSE 加密/轮换、电路熔断器 |
| 多租户（CRUD/Quota/Budget/Audit） | ~8 | 租户管理、预算强制、审计日志 |
| 认证授权（API Key/JWT/SigV4/Policy） | ~10 | 持久化 Key、跨副本失效、JWT issuer |
| 事件/通知/Webhook/SSE | ~9 | 多副本事件桥、通知规则 CRUD |
| 复制/HA/集群/Federation | ~8 | 集群单例、跨区复制、pgvector |
| Reconcile / GC / Lifecycle | ~8 | 孤儿 blob、软删除保留、Scrub |
| 合规（WORM/Legal Hold/Retention） | ~6 | 合规锁、版本生命周期、Legal Hold |
| 可观测性（OTel/Metrics/Grafana/Prometheus） | ~6 | 14 个域仪器、Grafana 12 面板 |
| SDK 完整性 | ~3 | 跨语言 API 方法覆盖 |
| Web UI / Admin Console | ~4 | UI 生产化、管理控制台 |
| 工程质量（内存安全/流式/并发） | ~7 | 大对象 spillBuffer、BM25 并发 |
| 基础设施（TLS/CDN/FUSE/ACME） | ~5 | 证书自动续期、CDN 集成 |
| 导入/迁移/批量操作 | ~2 | 从 S3 导入工具 |

**本期辨别的 5 个方向已有分析均未覆盖或仅行级提及**，且不属于前列领域的子方向。

---

## 本期方向总览

| # | 方向 | 类型 | 影响 | 既有覆盖 |
|---|------|------|------|---------|
| 1 | **🔴 MCP 安全模型与生产加固** | 安全/架构 | AI Agent 接入的安全护栏缺失；当前 MCP 零认证、零授权、零审计 | **零覆盖** |
| 2 | **🔴 SDK 开发者体验与生产模式** | 采纳/集成 | 开发者选择 SDK 的首要因素是 DX，不是功能数；当前 SDK 缺乏重试、流式、错误映射等生产模式 | v18 仅覆盖 API 方法缺口，**未涉及 DX 模式** |
| 3 | **🟠 管理控制台（Admin Web Console）** | 运维/产品 | 18+ admin REST 端点无 GUI 暴露；非技术运维人员无法完成日常操作 | v18 两行提及，**无深入分析** |
| 4 | **🟠 结构化错误协议（统一错误编目）** | API 工程 | 四套协议各自为政的错误格式导致 SDK 复杂性翻倍；缺少可机器解析的错误码与重试语义 | v4/v14/v19 表层提及，**无完整设计** |
| 5 | **🟠 API 版本化策略与弃用生命周期** | API 工程 | 平台演进必须的版本协商/废弃机制完全缺失；长期积压的运维债务 | **零覆盖** |

---

## 1. 🔴 MCP 安全模型与生产加固

### 现状

MCP 协议是 aero-vault 与 AI Agent 交互的核心桥梁，当前有两类接入方式：

**HTTP 模式**（`POST /mcp`，`cmd/server/main.go:112`）：
```go
r.Method(http.MethodPost, "/mcp", mcp.HTTPHandler(mcpServer))
```
MCP handler 直接在 chi 路由器上注册，**完全跳过了 middleware 链** — 而 middleware 链包含 Auth、Tenant、RateLimiter、OTel、AccessLog。这意味着：
- 任何 MCP HTTP 请求都不需要 API Key、JWT 或任何凭证
- `X-Aero-Tenant` header 不会被解析（`TenantFrom(ctx)` 返回空字符串，fallback 到 `"default"`）
- 速率限制不生效
- 请求不经过 OTel 中间件，无法追踪
- 访问日志不记录

**Stdio 模式**（`aero-vault mcp`，`cmd/server/main.go:56`）：
```go
func runMCP() error {
    // ...直接打开存储和 repo
    svc := service.NewFileService(store, repo, logger)
    server := mcp.NewServer(svc, repo, search, "default", logger)
    // tenant 硬编码为 "default"
    return mcp.ServeStdio(ctx, server, os.Stdin, os.Stdout)
}
```
- tenant 硬编码为 `"default"`，多租户环境无法使用
- API Key / JWT 不会从 stdio 通道获取
- 工具调用没有任何权限检查（`list_files` / `read_file` / `write_file` / `delete_file` / `search` / `chat`）

**代码证据**（`internal/mcp/server.go`）：
```go
func (s *Server) tenantFor(ctx context.Context) string {
    if t := mw.TenantFrom(ctx); t != "" && t != "default" {
        return t
    }
    return s.tenant // 始终返回 "default"
}
```
这个退避逻辑在 HTTP 模式下仅当中间件未设置 tenant 时才生效 — 而 MCP handler **根本没有中间件**。

工具列表中的 `write_file` 和 `delete_file` 对任何能连接到 MCP 端点的客户端都完全开放：
```go
// internal/mcp/server.go:92–116
{Name: "write_file", Description: "Write text content to an object key."},
{Name: "delete_file", Description: "Soft-delete an object by key."},
```

### 为什么需要

| 原因 | 影响 |
|------|------|
| **AI Agent 集成是核心用例** | MCP 是 AI 客户端（Claude Desktop、Cline 等）的主要接入协议。随着 AI Agent 采用率增长，MCP 的暴露面就是产品的安全暴露面。 |
| **零认证 = 零安全** | 当前部署中，任何知道 `/mcp` URL 的人都可以列举、读取、写入、删除所有对象。在暴露于公网的部署中，这是灾难性的。 |
| **多租户集成不可用** | Stdio 模式硬编码 `"default"` tenant，SaaS 部署中的 MCP 客户端无法指定租户——单个 `aero-vault mcp` 进程只能服务一个租户。 |
| **无审计溯源** | MCP 操作不经过 AccessLog、不记录 AuditEntry、不经过 OTel，运维人员无法回答"谁通过 MCP 删除了这个文件？"。 |

### 建议方向

1. **HTTP 模式接入 middleware 链**：将 MCP handler 移入经 auth+tenant+ratelimit+CORS+OTel+accesslog 处理的路径。使 MCP 请求获得与其他协议同等的安全护栏。
2. **MCP 会话层认证**：在 `initialize` 握手阶段接受 `X-Api-Key` 或 `Authorization: Bearer`，将凭证与 MCP 会话绑定。
3. **按工具授权**：限制特定 scope 的 API Key 只能调用特定工具（例如 `read` 范围只能调用 `list_files` / `read_file` / `search`，不能调用 `write_file` / `delete_file`）。
4. **MCP 访问审计**：记录 MCP 工具调用的 audit 行（复用 `audit_log` 表），包含工具名称、参数摘要、调用者身份。
5. **Stdio 模式多租户**：允许 stdio 模式通过命令行参数或环境变量指定 tenant，或通过协议扩展传递 tenant 上下文。

### 影响评估

| 层 | 影响 |
|----|------|
| `internal/mcp/server.go` | 从无状态变为带会话/凭证状态的 handler；新增 `WithAuth` 配置 |
| `internal/auth/` | 新增 scope → MCP tool 映射；新增 MCP 专用的 token 验证路径 |
| `cmd/server/main.go` | MCP handler 注册位置从直接挂 chi 改为挂到 middleware 之后 |
| `internal/repository/` | 可能新增 `mcp_tool_call` 审计子类型 |

---

## 2. 🔴 SDK 开发者体验与生产模式

### 现状

当前三套 SDK（Go / Python / JavaScript）已完成基本 CRUD + 18 个 admin 方法的覆盖，达到 **API 方法级的功能完整性**（v18 已详细分析）。但在**开发者体验（Developer Experience）** 和**生产模式**层面存在显著空白：

**缺失的生产模式：**

| 模式 | Go SDK | Python SDK | JS SDK | 问题 |
|------|--------|------------|--------|------|
| **自动重试/退避** | ❌ | ❌ | ❌ | 429/503 错误需要开发者自行实现重试逻辑 |
| **多分片上传助手** | ❌ | ❌ | ❌ | 大文件分片上传需手动调用 4 个步骤（init/part/complete/abort） |
| **SSE 事件流式监听** | partial | ❌ | ❌ | Go SDK 有 `sse.go`（低级别），无高级事件监听器 |
| **错误类型映射** | ❌ | ❌ | ❌ | 所有错误返回 `error` / 异常，无结构化错误码到 SDK 类型的映射 |
| **文件下载/上传流式** | partial | ❌ | ❌ | `Get` 返回 `io.ReadCloser`（正确），但无进度回调、断点续传 |
| **TypeScript 类型定义** | N/A | N/A | partial | `.d.ts` 存在但类型不全，缺乏泛型，枚举缺失 |
| **集成测试套件** | ❌ | ❌ | ❌ | 只有 stub-based 单元测试，无真实服务器集成测试 |
| **README 完整示例** | partial | partial | partial | 每个 SDK 有 quickstart，但缺少多场景可运行示例 |
| **API 兼容性检查** | ❌ | ❌ | ❌ | 无 SDK ↔ 服务器版本兼容性验证 |

**代码证据**（`sdk/go/aerovault/client.go`）：
```go
// ~1000 行文件，仅有一个 `do` 方法处理所有 HTTP 调用：
func (c *Client) do(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
    req, _ := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
    // ... 无重试逻辑，无退避，无 429/503 处理
    return c.httpClient.Do(req)
}
```

**JavaScript SDK**（`sdk/js/aero-vault.js`）缺少模块化导出：
```javascript
// 全局导出所有函数到一个对象，无 ES module / CJS / TypeScript 类型
window.AeroVault = { upload, get, search, /* ... */ };
```

### 为什么需要

| 原因 | 影响 |
|------|------|
| **开发者选择 SDK 的首要因素是 DX** | 根据 Stripe / Twilio / AWS 的经验，SDK 的开发者体验（错误消息、文档、重试、类型安全）直接影响 API 的采用率。一个重试逻辑缺失的 SDK 会让开发者在第一个 429 后就放弃。 |
| **生产模式缺失 = 生产不可用** | 没有重试/退避的 SDK 在面对短暂网络故障或限流时直接暴露出原始错误给最终用户。没有多分片上传助手的 SDK 让大文件上传成为不可能的挑战。 |
| **SDK 是产品的门面** | 大多数开发者不会直接调用 REST API——他们通过 SDK 形成对产品的第一印象。当前 SDK 的 README 示例过于简单，无法展示平台的完整能力。 |
| **与竞品的全方位差距** | AWS SDK 提供完整的重试策略、多分片上传管理器、事件监听器、凭证链、区域路由等生产模式。aero-vault SDK 在这些方面是空白。 |

### 建议方向

1. **跨语言重试/退避层**：在所有三个 SDK 中实现指数退避 + jitter 的重试机制，自动处理 429（限流）和 503（不可用），可通过选项配置白名单/黑名单状态码。
2. **多分片上传管理器**：提供高级 `MultipartUpload(ctx, key, reader, size)` 方法，内部自动分片、并发上传、超时重试、失败时 abort。支持并行度、分片大小、进度回调。
3. **SSE 事件监听器**：提供类型化的事件监听 API（`client.Events().Subscribe(handler)`），自动重连，断线恢复。
4. **结构化错误类型**：为每个可预测的服务器错误（`NotFound` / `QuotaExceeded` / `BudgetExceeded` / `RateLimited` / `ObjectCorrupt` / `Locked`）定义独立错误类型/类，附带 `RetryAfter`、`RequestID`、`Code` 等属性。
5. **SDK 集成测试套件**：为每个 SDK 编写一套集成测试，以 Docker Compose 中的 aero-vault 实例为目标运行，覆盖 CRUD、搜索、多分片上传、错误处理等。作为 CI 的一部分。
6. **TypeScript 类型生成**：从 OpenAPI spec 自动生成完整的 TypeScript 类型定义，确保类型与 API 同步。

---

## 3. 🟠 管理控制台（Admin Web Console）

### 现状

当前 Web UI（`internal/webui/static/index.html`，282 行）是一个简洁的 SPA 文件浏览器，包含 4 个 Tab：

| Tab | 功能 | 状态 |
|-----|------|------|
| Search | 语义搜索 + BM25/Vector/Hybrid | ✅ 可用 |
| Detail | 选中对象的 metadata JSON | ✅ 可用 |
| Lineage | AI 使用线路图（object ID 维度） | ✅ 可用 |
| Chat | RAG 聊天（SSE 流式渲染） | ✅ 可用 |

**但没有任何管理功能。** 全部 18+ admin REST 端点（`/v1/admin/*`）无一可通过 GUI 操作：

| Admin 端点 | 功能 | UI 暴露 |
|------------|------|---------|
| `GET/POST/DELETE /v1/admin/tenants` | 租户 CRUD | ❌ |
| `PUT /v1/admin/tenants/{tenant}/status` | 启用/停用租户 | ❌ |
| `PUT /v1/admin/tenants/{tenant}/quota` | 设置配额 | ❌ |
| `PUT /v1/admin/tenants/{tenant}/budget` | 设置 AI 预算 | ❌ |
| `POST /v1/admin/keys` | 创建 API Key | ❌ |
| `GET /v1/admin/keys` | 列举 API Key | ❌ |
| `DELETE /v1/admin/keys/{hash}` | 吊销 API Key | ❌ |
| `POST /v1/admin/jwt` | 签发 JWT | ❌ |
| `GET /v1/admin/jobs` | 查看后台作业 | ❌ |
| `POST /v1/admin/jobs/{id}/retry` | 重试失败作业 | ❌ |
| `GET /v1/admin/webhook-failures` | Webhook 失败列表 | ❌ |
| `GET /v1/admin/audit` | 审计日志 | ❌ |
| `PUT /v1/admin/tenants/{tenant}/quota` | 设置配额 | ❌ |
| `GET /v1/info` | 服务信息 | ❌ |
| `GET /healthz` / `/readyz` | 健康检查 | ❌ |

**代码证据**（`internal/webui/static/index.html`）：
```javascript
// UI 只有 fetch() 调用到 /v1/files/* 和 /v1/search、/v1/chat、/v1/lineage
// 无任何 /v1/admin/* 调用
```

### 为什么需要

| 原因 | 影响 |
|------|------|
| **非技术运维人员无法管理** | 几乎所有运维操作（添加租户、吊销 Key、查看配额、重试失败作业）都需要 curl 或 SDK 调用。没有 GUI 意味着团队中必须有开发人员在场才能完成日常管理。 |
| **管理效率低下** | 列举租户/Key 只能用 CLI/REST。无法直观地看到哪个租户接近配额上限、哪个作业频繁失败。 |
| **故障排除耗时** | Webhook 失败、作业失败、审计日志只能通过 REST API 查看。每次排障都需要打开终端 + curl。 |
| **与竞品的巨大体验差距** | MinIO Console、Ceph Dashboard 都提供了功能完备的 Web 管理界面。aero-vault 在运维体验上存在明显短板。 |

### 建议方向

基于现有的 4-Tab SPA 架构扩展：

1. **管理侧边栏或独立 Admin Tab**：新增 `Admin` Tab，包含：
   - **租户管理**：列举、创建、停用租户；查看配额使用量；设置 AI 预算
   - **API Key 管理**：列举、创建、吊销 Key；显示 Key 的最后使用时间和过期时间
   - **作业队列**：查看 pending/running/failed 作业；重试失败作业
   - **Webhook 失败**：查看投递失败记录；手动触发重试
   - **审计日志**：按时间范围、操作者、操作类型浏览审计事件
   - **健康状态**：显示 `/healthz` 和 `/readyz` 结果、存储后端状态、DB 状态

2. **复用现有基础设施**：UI 侧不需要新的 API——18+ admin 端点已完整实现。只需要在 HTML/JS 中调用它们并渲染结果。

3. **角色感知**：UI 检测当前 token 的 scope（`admin` / `read` / `write`），仅显示相应操作入口。

---

## 4. 🟠 结构化错误协议（统一错误编目）

### 现状

aero-vault 通过四个协议暴露 API，每个协议使用不同的错误格式：

| 协议 | 错误格式 | 示例 |
|------|---------|------|
| **REST** `/v1` | Ad-hoc JSON `{"error":{"code":"...","message":"..."}}` | `{"error":{"code":"NotFound","message":"object not found"}}` |
| **S3** `/s3` | AWS XML `<?xml...><Error><Code>NoSuchKey</Code>...` | `<Error><Code>NoSuchKey</Code><Message>...</Message></Error>` |
| **MCP** `/mcp` | JSON-RPC error `{"error":{"code":-32603,"message":"..."}}` | `{"jsonrpc":"2.0","error":{"code":-32601,"message":"method not found"},"id":1}` |
| **WebDAV** `/webdav` | HTTP status + DAV error XML | `207 Multi-Status` + DAV XML body |

**当前问题清单：**

1. **无统一错误码编目**：REST 错误的 `"code"` 是内联字符串（`"NotFound"`、`"InvalidArgs"`、`"QuotaExceeded"` 等），没有中心化的错误码注册表。不同 handler 可能对相同条件返回不同的 code string。

2. **S3 错误码映射不完整**：`internal/api/s3compat/errors.go` 维护了一部分 REST error → S3 XML Code 的映射，但缺少覆盖全部错误的系统性映射。某些 REST handler 抛出的错误通过 S3 协议返回时可能映射为 `InternalError` 而不是准确的 S3 错误码。

3. **错误缺少重试语义**：调用方无法从错误响应中判断：
   - 这个错误是临时的（可重试）还是永久的（不可重试）
   - 如果可重试，建议等待多长时间
   - 错误是客户端的（4xx）还是服务端的（5xx）

4. **错误中缺少关联上下文**：错误响应很少包含 `request_id`、`request_id` 或 `trace_id`，导致日志关联困难。

5. **SDK 错误处理困难**：因为错误格式不统一、错误码不规范、缺少结构化字段，每个 SDK 必须用字符串匹配来解析错误——脆弱且不可靠。

**代码证据**（`internal/api/rest/util.go`）：
```go
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
    // 通过 errors.Is 链判断错误类型，每个分支手动设置 status + code
    switch {
    case errors.Is(err, service.ErrNotFound):
        writeErr(w, http.StatusNotFound, "NotFound", err.Error())
    case errors.Is(err, service.ErrInvalidArgs):
        writeErr(w, http.StatusBadRequest, "InvalidArgs", err.Error())
    case errors.Is(err, service.ErrQuotaExceeded):
        writeErr(w, http.StatusInsufficientStorage, "QuotaExceeded", err.Error())
    // ...
    }
}
```

这个 `switch` 模式在每个协议层重复实现（`util.go`、`s3compat/errors.go`、`mcp/server.go`），错误判定的逻辑是分散的。

### 为什么需要

| 原因 | 影响 |
|------|------|
| **开发者体验的基石** | 错误信息是开发者与 API 交互时看到的第一手反馈。不规范的错误响应直接影响对平台质量的判断。 |
| **SDK 可靠性前提** | 没有结构化的错误码和重试语义，SDK 无法实现可靠的自动重试、错误分类、用户友好提示。|
| **排障效率** | 缺少 `request_id` 关联的错误响应让运维人员无法将用户报告的错误与日志条目关联起来。 |
| **平台演进基础** | 规范化的错误编目是 API 版本化、向后兼容性保证、OpenAPI spec 完整性的前提条件。 |

### 建议方向

1. **统一错误注册表**：创建 `internal/errors/catalog.go`，定义一个中心化的错误码枚举（`ErrNotFound`、`ErrQuotaExceeded`、`ErrBudgetExceeded`、`ErrRateLimited`、`ErrLocked`、`ErrObjectCorrupt` 等），每个错误码携带：
   - HTTP 状态码
   - REST JSON code string
   - S3 XML Error Code
   - MCP JSON-RPC code
   - 是否可重试（`retryable bool`）
   - 错误类别（`client` / `server` / `auth` / `quota`）

2. **错误响应包含标准字段**：所有协议的错误响应应包含：
   - `request_id`（已有 `X-Request-ID` header）
   - `code`（统一的机器可读错误码）
   - `message`（人类可读描述）
   - `retry_after`（可选，429/503 时建议的秒数）
   - `detail`（可选，附加的上下文信息）

3. **从错误注册表自动生成**：
   - 各协议层的错误响应函数（`writeRESTError`、`writeS3Error`、`writeMCPError`）从统一注册表派生
   - SDK 的错误类型/类自动生成
   - OpenAPI spec 中的错误响应 schema 自动生成

### 影响范围

| 层 | 影响 |
|----|------|
| `internal/errors/` | 新建包，包含错误注册表 + 各协议写错误函数 |
| `internal/api/rest/util.go` | 从 `switch` 模式迁移到注册表查找 |
| `internal/api/s3compat/errors.go` | 从手工 `switch` 迁移到注册表映射 |
| `internal/mcp/server.go` | 新增 `writeMCPError` 统一入口 |
| `sdk/go/aerovault/` | 自动生成 `errors.go` 类型 |
| `sdk/python/aero_vault.py` | 自动生成错误类 |
| `sdk/js/aero-vault.js` | 自动生成错误类 |

---

## 5. 🟠 API 版本化策略与弃用生命周期

### 现状

aero-vault 的所有 HTTP API 都位于 `/v1/` 路径下。没有版本协商机制，没有废弃策略，没有向后兼容性文档。

**关键证据：**

1. **无版本协商**：不支持 `Accept: application/vnd.aero-vault.v2+json` 或 URL 路径版本号（`/v2/`）。新的 API 变更直接修改 `/v1/` 路径上的行为。

2. **无废弃/弃用机制**：
   - 响应中无 `Sunset` header（RFC 8594）
   - 响应中无 `Deprecation` header（即将废弃的端点没有标志）
   - OpenAPI spec 中没有 `deprecated` 标记

3. **无向后兼容性承诺**：没有文档说明 `/v1/` 的稳定性承诺。用户在集成时无法判断哪些行为是稳定的、哪些可能变更。

4. **header 演进无协调**：
   - `X-Aero-Tenant` header 是 core API 的一部分但未在 OpenAPI 中反映
   - `Idempotency-Key` header 新增后没有版本化标记
   - 未来任何 header 行为变更都会影响所有 `/v1/` 客户

**代码证据**（`internal/api/rest/router.go`）：
```go
func NewRouter(...) chi.Router {
    r := chi.NewRouter()
    // 所有路由都在 /v1 下，无任何版本区分
    r.Route("/v1", func(r chi.Router) {
        r.Put("/files/*", h.Put)
        r.Get("/files/*", h.Get)
        // ...
    })
}
```

对比：AWS S3 API 从 2006 年发布以来，核心对象操作（GET/PUT/DELETE）的语义几乎没有变化，因为向后兼容性是 S3 的核心理念。

### 为什么需要

| 原因 | 影响 |
|------|------|
| **平台演进的基础设施** | 没有版本化策略，任何 API 变更要么是向后兼容的（缩手缩脚），要么是破坏性的（迁移痛苦）。这是 API 平台演进的必答题。 |
| **生态系统的信任信号** | SDK 发布、合作伙伴集成、客户合同——所有都依赖于对 API 稳定性的信任。没有版本化策略破坏了这种信任。 |
| **长期运维债务积累** | 随着时间推移，无法废弃旧端点意味着代码库不断膨胀。没有 Sunset 机制，必须永远维护所有端点。 |
| **与优秀 API 实践的差距** | Stripe、GitHub、AWS 都有完善的版本化策略。这是判断一个平台是否"成熟"的关键信号。 |

### 建议方向

1. **版本化策略声明**：在 `docs/api.md` 和相关文档中明确：
   - `/v1/` 的稳定性级别（stable / beta / alpha）
   - 变更通知渠道（changelog、邮件列表）
   - 废弃时间线（至少 N 个月提前通知）

2. **废弃响应头**：当端点被标记为废弃时，响应包含：
   - `Deprecation: true`
   - `Sunset: Sat, 01 Nov 2027 00:00:00 GMT`
   - `Link: </v2/files/{key}>; rel="successor-version"`
   
   实现方式：在路由注册时指定 `deprecatedSince` 和 `sunsetDate`，middleware 自动注入上述 header。

3. **版本迁移中间件**：实现一个中间件，允许 `/v1/` 请求的响应中指出可用的新版本 `Warning: 299 - "This endpoint will be removed; use /v2/..."`。

4. **OpenAPI spec 版本化管理**：
   - 为每个 `/vN/` 版本维护独立的 OpenAPI spec（或使用标签区分）
   - spec 中包含 `deprecated` 标记
   - SDK 从对应版本的 spec 生成

5. **版本兼容性测试**：在 CI 中用新版本的服务器运行旧版本 SDK 的测试，确保 `/v1/` 不破坏老客户。

### 影响范围

| 层 | 影响 |
|----|------|
| `internal/api/rest/router.go` | 路由注册时附加版本元数据 |
| `internal/middleware/` | 新增 `Sunset` / `Deprecation` header 注入中间件 |
| `docs/api.md` | 版本化策略文档 |
| `internal/api/rest/openapi.go` | OpenAPI spec 版本化 + deprecation 标记 |
| CI | 新增版本兼容性测试流水线 |

---

## 总结：5 个方向的优先级排序

| 优先级 | 方向 | 为什么在此位置 |
|--------|------|---------------|
| **P0** | **#1 MCP 安全模型与生产加固** | 安全漏洞：零认证的 MCP 端点暴露全部 CRUD 能力。随着 AI Agent 采用率上升，暴露面持续扩大。即时风险。 |
| **P0** | **#2 SDK 开发者体验与生产模式** | 采纳门槛：第一个 429 没有重试、第一个大文件没有分片上传助手——这些是开发者放弃的拐点。直接影响用户留存。 |
| **P1** | **#3 管理控制台** | 运维效率：非技术运维人员无法完成日常操作。18+ admin 端点无 GUI 暴露。影响团队自助服务能力。 |
| **P1** | **#4 结构化错误协议** | 工程基础：四套协议的错误格式不一致导致 SDK 复杂度翻倍。影响所有客户端集成的可靠性。 |
| **P2** | **#5 API 版本化策略** | 长期债务：不会立即阻止采用，但随着平台演进和 SDK 发布，版本化缺失会成为越来越严重的运维障碍。建议在发布第一个非向后兼容变更前建立机制。 |

---

## 附：本次扫描中发现的边缘问题

1. **MCP write_file 无大小限制**：`toolWriteFile` 将整个 content 读入内存后 PUT，无大小限制。大内容写入可能导致 OOM。
2. **MCP resources/list 无分页**：`listResources` 调用 `svc.List` 时 limit=200，在对象数超过 200 时结果截断且无 `hasMore` 指示。
3. **Web UI 的 tenant 选择器不持久化到后端**：UI 只在 localStorage 中保存 tenant 值，后端请求头依赖于 HTML 中的输入框。如果页面刷新，选中的 tenant 可能丢失。
4. **Web UI 无认证状态管理**：API Key 输入框不保存到 localStorage，刷新后需要重新输入。
5. **SDK 的 SSE 模块仅 Go 有**：`sdk/go/aerovault/sse.go`（事件流订阅），Python 和 JS SDK 缺失 SSE 事件监听能力。
6. **SDK 缺少 WebDAV 支持**：WebDAV 协议完全通过 SDK 不可访问，而 `rclone` 等工具直接使用 WebDAV，无需 SDK。
7. **MCP 的资源 URI 格式未在文档中说明**：`aero-vault://{tenant}/{bucket}/{key}` 格式仅在代码中定义，对 MCP 客户端不透明。
8. **无统一的 `User-Agent` header 策略**：SDK 请求的 `User-Agent` 没有标准化（Go SDK 有 `aero-vault-go/0.4.0`，Python 和 JS 可能缺失或不同）。

---

*本文由代码库全局扫描生成，与现存 23 期 expansion 文档逐条去重，仅用于指导和讨论，不包含任何代码实现。*
