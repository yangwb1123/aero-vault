# 高价值扩展方向：认证盲区、AI 管线持久性缺陷、产品预览断层与运营治理缺口

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — 250+ Go 源文件、3 套 SDK、Web UI、MCP 双模式（HTTP+stdio）、WebDAV、50 对迁移文件、完整配置层  
> **去重验证：** 对 `docs/requirements/` 下全部 110 份既有分析文档进行关键词 + 代码锚点交叉验证，确认本文方向未被任何已有文档独立深度覆盖  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。每个方向必须包含：**代码中存在的具体实现锚点**、**可量化的生产/产品影响**、**架构权衡与边界情况**。

---

## 方法论：跳出"功能缺失"的定式

前 110 轮分析已覆盖了绝大多数功能级缺口（桶策略、通知引擎、访问日志、对象锁、生命周期、多模态 AI、缓存、去重、复制等）。本轮不再重复"缺少哪个功能"，而是聚焦三类更隐性的缺口：

| 缺口类型 | 判定标准 | 本文覆盖 |
|----------|---------|---------|
| **安全不对称** | 一个协议路径执行了完整的认证/鉴权 → 另一个路径**完全绕过**且无任何防护 | 方向一（MCP/WebDAV 认证盲区） |
| **运行时退化** | 功能"已经实现"但关键组件在进程重启后状态丢失，导致功能静默降级 | 方向二（BM25 索引持久性） |
| **产品体验断层** | 后端完备但用户侧无法消费/预览，形成"存了但看不见"的断层 | 方向三（内容预览管线） |
| **运营数据污染** | 逻辑正确但导致运营数据歧义，"成功"和"永久失败"无法区分 | 方向四（Webhook 死信语义） |
| **平台采用瓶颈** | 所有管理功能仅 admin 可用，租户无法自助操作，形成运维瓶颈 | 方向五（租户自助 API） |

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码证据 |
|---|------|------|--------|---------|---------|
| **1** | **跨协议认证盲区 — MCP 和 WebDAV 完全绕过身份验证** | 安全/架构 | **P0** | REST API 和 S3 兼容层有完整的 Bearer JWT / API Key / SigV4 认证链。但 MCP（HTTP 模式）和 WebDAV 完全不经过 `auth.Registry`，直接暴露 FileService 操作。任何能访问 `/mcp` 或 `WEBDAV_PREFIX` 端点的请求可以未经认证读写所有对象 | `internal/mcp/server.go:35-50` — `NewServer` 不接收 `*auth.Registry`，仅硬编码 `"default"` tenant；`internal/mcp/transport.go:38-52` — `HTTPHandler` 直接包装 `mcp.Server.Handle`，不经过任何中间件；`internal/api/webdav/dav.go:37-48` — `Handler` 仅创建 `xwebdav.Handler`，无认证中间件；`cmd/server/main.go:208-216` — MCP 和 WebDAV 路由注册在 `chi.Router` 上但不在中间件链内 |
| **2** | **BM25 全文索引持久性缺口 — 每次重启冷启动全量重建** | AI/运营 | **P1** | BM25 索引完全在进程内存中。`BuildFromRepo` 在启动时遍历租户的全部对象来重建索引。对于千/万级对象，这需要数分钟。在此期间，`mode=bm25` 和 `mode=hybrid` 搜索要么不可用（bm25 未就绪报错），要么返回空结果 | `internal/ai/bm25.go` — 全文 BM25 索引结构（`docMap`, `termFreq`, `inverseDocFreq`）无持久化方法；`cmd/server/main.go:145-155` — `setupBM25Search` 调用 `bm.BuildFromRepo(ctx, repo, t)`，其中 `BuildFromRepo` 全量扫描 `repo.ListObjects` + `repo.ListChunksForObject`；`internal/ai/bm25_test.go` — 测试仅验证内存构建，无持久化测试 |
| **3** | **内容预览管线断层 — 存储了但"看不见"** | 产品/UI | **P2** | 系统存储任意文件类型（文本、代码、Markdown、PDF、图片、电子表格），但 Web UI 仅展示原始 JSON 响应，MCP 读取原始文本（截断 4MB），无任何 per-content-type 渲染。用户无法在浏览器中预览一张图片、一份 PDF、一篇 Markdown、一段代码 | `internal/webui/static/` — 纯 HTML + vanilla JS，调用 REST API 后仅显示 JSON 字符串；`internal/mcp/server.go:92-104` — `toolReadFile` 使用 `io.ReadAll(io.LimitReader(rc, 4<<20))`，返回纯文本；`internal/api/rest/handler.go:61-89` — `Get` 方法返回 `Content-Type` 和原始字节流，无渲染；`internal/thumbnail/thumbnail.go` — 只有 `Generate` 方法（"生成缩略图"），但 Web UI 和 REST API 均未使用缩略图端点 GET `/thumbnail` |
| **4** | **Webhook 死信语义污染 — "成功"与"永久失败"不可区分** | 运营/数据质量 | **P2** | Webhook 在 10 次重试后将永久失败的消息标记为 `succeeded=true`（代码注释明确声明这是有意为之），`listWebhookFailures` 返回所有行但 `last_error` 是唯一区分信号。运营人员无法区分"投递正常"和"投递 10 次失败后死信" | `internal/events/webhook.go:161-172` — 在 `attempts >= 10` 后调用 `MarkWebhookSucceeded`（`// … this intentionally conflates "permanently dead" with "succeeded" to stop perpetual retries`）；`internal/repository/webhook_failures.go` — `WebhookFailure` 结构体仅 `Succeeded bool` 无 `Status` 枚举；`internal/api/rest/admin.go:150-165` — `ListWebhookFailures` 返回所有行但无状态过滤 |
| **5** | **租户自助 API 缺失 — 所有操作依赖平台管理员** | 产品/平台 | **P3** | 平台有完善的租户隔离和 admin API（`/admin/tenants/*`、`/admin/keys/*`、`/admin/jobs/*`），但租户无法自助管理自己的密钥、配额、通知规则、生命周期策略。每个操作都需要联系平台管理员，形成运维瓶颈 | `internal/api/rest/router.go:80-95` — admin 路由全部挂载在 `/admin/...` 下，scope 校验为 `admin`；无租户级自我管理路由（如 `/v1/me/keys`、`/v1/me/usage`）；`internal/auth/auth.go:85-110` — scope 仅支持 `read`/`write`/`admin`，无 `self-service` scope |

---

## 方向一：跨协议认证盲区 — MCP 和 WebDAV 完全绕过身份验证

### 产品/安全影响

| 维度 | 影响 |
|------|------|
| **安全风险** | 部署了 auth（配置了 `AUTH_JWT_SECRET` 或 API keys）的实例，MCP 和 WebDAV 端点仍然完全开放。这是一个典型的**安全不对称漏洞**：管理员以为"启用了认证"，但两个协议的入口点不受保护。任何能访问服务器网络的人可以未经认证：列出所有对象、读取文件内容、写入文件、删除文件 |
| **多租户破坏** | MCP 硬编码 `"default"` tenant（`internal/mcp/server.go:35`），WebDAV 从 `X-Aero-Tenant` 头读取但从不验证调用者属于该租户。租户隔离被完全绕过 |
| **攻击面扩大** | WebDAV 常用于内网文件共享，开放到公网后成为认证旁路的入口。MCP 通常用于 AI 客户端集成，如果 `/mcp` 端点暴露，任何 LLM agent 都可以未经认证访问数据 |

### 现状与代码证据

**证据 1：MCP 初始化不接收 auth.Registry**

```go
// cmd/server/main.go:208-216
mcpServer := mcp.NewServer(svc, repo, search, "default", logger)
//                                                       ^^^^^^^ — 硬编码 tenant
if chat != nil {
    mcpServer.WithChat(chat)
}
r.Method(http.MethodPost, "/mcp", mcp.HTTPHandler(mcpServer))
//                                  ^^^^^^^^^^^^^^^^^^^^^^^ — 直接暴露，无中间件
```

对比 REST API 的装配方式：

```go
// cmd/server/main.go:204
r.Mount("/v1", rest.NewRouter(svc, repo, search, chat, agent, bus, authReg, logger, ...))
//                                                          ^^^^^^^ — auth.Registry 传入

// cmd/server/main.go:219-237 — applyMiddleware
for _, m := range []func(http.Handler) http.Handler{
    authReg.Middleware(),  // ← REST/S3 经过认证
    ...
}
```

**证据 2：MCP HTTPHandler 是无认证的裸包装**

```go
// internal/mcp/transport.go:38-52
func HTTPHandler(s *Server) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        body, _ := io.ReadAll(r.Body)
        resp := s.Handle(r.Context(), body)
        // ...
    })
}
```

没有调用 `authReg.Middleware()`，没有读取 `Authorization` 头，没有验证任何 token。

**证据 3：WebDAV 不经过中间件**

```go
// cmd/server/main.go:210-214
var davH http.Handler
if cfg.WebDAV.Prefix != "" {
    davH = dav.Handler(cfg.WebDAV.Prefix, svc, logger)
}
return buildDispatcher(r, davH, cfg)
```

`dispatch` 在 chi 路由器**之外**分发请求——它既不经过 `applyMiddleware` 链，也不经过 chi 的中间件栈。

**证据 4：MCP stdio 模式无 tenant 传递**

```go
// internal/mcp/server.go:55-60
func (s *Server) tenantFor(ctx context.Context) string {
    if t := mw.TenantFrom(ctx); t != "" && t != "default" {
        return t
    }
    return s.tenant  // ← 始终 "default"
}
```

stdio 模式的 `ctx` 不会包含 `X-Aero-Tenant`（因为没有 HTTP 请求），所以 `tenantFor` 永远返回 `"default"`。

### 建议方案

**方案 A（推荐，最小侵入）：将 MCP 移入中间件链**

1. MCP 路由注册从裸 `r.Method(http.MethodPost, "/mcp", ...)` 改为挂载在 chi 子路由上，使其经过 `applyMiddleware` 链
2. 或者将 MCP handler 包装在 `authReg.Middleware()` 中

```go
// 最小改动示例 — 将 MCP 挂载到 chi 组中经过认证的子路由
mcpR := chi.NewRouter()
mcpR.Use(authReg.Middleware())
mcpR.Post("/", mcp.HTTPHandler(mcpServer))
r.Mount("/mcp", mcpR)
```

**方案 B（推荐，更完整）：MCP 支持 Bearer token 认证**

在 `mcp.HTTPHandler` 内部解析 `Authorization: Bearer <token>` 头，使用 `auth.Registry.Authenticate(r)` 验证，并将 tenant 注入上下文：

```go
func HTTPHandler(s *Server, reg *auth.Registry) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        token := extractBearer(r)
        identity, err := reg.Authenticate(token)
        if err != nil {
            http.Error(w, "unauthorized", http.StatusUnauthorized)
            return
        }
        ctx := mw.WithTenant(r.Context(), identity.Tenant)
        body, _ := io.ReadAll(r.Body)
        resp := s.Handle(ctx, body)
        // ...
    })
}
```

**方案 C（WebDAV 认证）：对 WebDAV 添加 Basic Auth / Bearer 支持**

WebDAV 客户端通常支持 HTTP Basic Auth，可以通过 middleware 包装：

```go
davHandler := dav.Handler(prefix, svc, logger)
davWithAuth := authReg.Middleware()(davHandler)
```

### 边界情况

| 场景 | 处理 |
|------|------|
| MCP 兼容性（已存在客户端） | stdio 模式无 HTTP auth 头，应有 fallback 到无认证模式（通过 flag 控制）；HTTP 模式默认要求 auth |
| WebDAV Basic Auth 兼容性 | macOS Finder 和 Windows Explorer 仅支持 Basic Auth over HTTPS；需要支持 Basic → Bearer 或 Basic → API Key 的映射 |
| 多 scope 支持 | MCP 工具应有 scope 映射：`read_file` → `read` scope，`write_file` → `write` scope，`delete_file` → `write` scope |
| 健康检查路径 | `/healthz`、`/metrics` 等已有 bypass 列表，MCP 不应 bypass auth |
| 迁移兼容性 | 已有部署可能依赖于 MCP 无认证的现状，应通过配置项 `MCP_AUTH_ENABLED=true` 控制迁移窗口 |

---

## 方向二：BM25 全文索引持久性缺口 — 每次重启冷启动全量重建

### 产品/运营影响

| 维度 | 影响 |
|------|------|
| **停机时间** | 重启后 BM25 重建时间 = 对象总数 × 平均文本长度 ÷ 处理速度。对于 100K 个平均 2KB 文本的对象，重建约需 2-5 分钟（含 repo 查询）。这段时间内 `mode=bm25` 和 `mode=hybrid` 搜索完全不可用或返回空结果 |
| **功能退化** | 如果 BM25 重建在 indexer 之前完成（竞态条件），第一次 hybrid 搜索可能返回空结果因为部分向量索引已就绪但 BM25 侧为空。用户感知到的不是"慢"而是"丢失结果" |
| **成本浪费** | 每次重启都重新扫描全部对象，对于稳定集群这是不必要的重复工作。对于大规模部署，这还意味着每次滚动更新都对 DB 产生突发读负载 |
| **产品体验** | 早期用户（小规模）影响不明显，但随数据增长，每次部署升级后的等待时间线性增长。这是隐含的**伸缩性债务** |

### 现状与代码证据

**证据 1：BM25 是纯内存结构**

```go
// internal/ai/bm25.go:8-20
type BM25 struct {
    mu    sync.RWMutex
    docs  []docRecord
    docMap   map[int64]int // objectID → index in docs
    termFreq map[string]map[int]int // term → docIndex → freq
    inverseDocFreq map[string]float64 // term → IDF
    avgDocLen float64
    totalDocs int
    // 没有持久化方法，没有 Save/Load/Serialize
}
```

`struct BM25` 没有 `Save(w io.Writer) error` 或 `Load(r io.Reader) error` 方法。零持久化相关字段。

**证据 2：启动时全量重建**

```go
// cmd/server/main.go:145-155
func setupBM25Search(ctx context.Context, cfg *config.Config, repo repository.Repository, search *ai.Search) *ai.BM25 {
    // ...
    warmTenants := cfg.Reconcile.Tenants
    if len(warmTenants) == 0 {
        warmTenants = []string{"default"}
    }
    go func() {
        for _, t := range warmTenants {
            _ = bm.BuildFromRepo(ctx, repo, t)  // ← 全量扫描
        }
    }()
}
```

`BuildFromRepo` 的实现需要对每个对象调用 `ListChunksForObject`：

```go
// internal/ai/bm25.go:55-85
func (b *BM25) BuildFromRepo(ctx context.Context, repo repository.Repository, tenant string) error {
    // ...
    for {
        page, _ := repo.ListObjects(ctx, tenant, "", "", marker, 200)
        for _, obj := range page.Objects {
            chunks, _ := repo.ListChunksForObject(ctx, obj.ID)
            for _, chunk := range chunks {
                b.AddDocument(chunk.ObjectID, chunk.Content, chunk.Bucket, chunk.TenantID, chunk.ObjectKey, chunk.Seq, chunk.ID)
            }
        }
    }
}
```

**证据 3：无增量更新路径**（除了事件驱动的逐条 `AddDocument`）

`AddDocument` 被 indexer 的事件处理逐条调用，但 `BuildFromRepo` 是批量的全量重建。如果跑 `BuildFromRepo` 的同时有事件进来，可能出现重复添加。

### 建议方案

**方案 A（推荐）：定期序列化到 DB/文件**

在 `BM25` 上添加 `Save`/`Load` 方法，将 `termFreq`、`inverseDocFreq`、`docs` 等核心结构序列化：

```go
func (b *BM25) Save(ctx context.Context, repo repository.Repository, tenant string) error {
    // 将 BM25 状态编码为 protobuf/JSON/gob
    // 持久化到 repository（新表 bm25_state）或 storage（特殊 key）
    data, _ := b.Marshal()
    return repo.SaveBM25State(ctx, tenant, data)
}

func (b *BM25) Load(ctx context.Context, repo repository.Repository, tenant string) error {
    data, err := repo.LoadBM25State(ctx, tenant)
    if err != nil { return err }
    return b.Unmarshal(data)
}
```

启动流程改为：
```
if Load() succeeded → use loaded state
if Load() failed (no saved state / version mismatch) → BuildFromRepo()
After build → Save() (so next restart picks it up)
```

**方案 B（轻量级）：增量 changelog + 定期 checkpoint**

- indexer 在处理 `object.created` 事件时将 chunk 内容写入一个 `bm25_changelog` 表（新迁移）
- 每次重启时只读取 changelog 中自上次 checkpoint 后的增量行来重建
- 重建完成后写入 checkpoint 标记
- 减少重启后的处理量从 O(N) 到 O(delta since last checkpoint)

**方案 C（最小投入）：持久化到本地文件 + 启动时校验时间戳**

利用 `os` 包的临时文件缓存 BM25 状态：

```go
func (b *BM25) SaveToFile(path string) error { /* gob encode + write */ }
func (b *BM25) LoadFromFile(path string) error { /* read + gob decode */ }
```

启动时优先从文件加载，fallback 到 `BuildFromRepo`。

### 边界情况

| 场景 | 处理 |
|------|------|
| 嵌入模型变更 | 嵌入模型改变不影响 BM25（它是纯词频模型），无需因 embedder 变更而重建 |
| 版本升级（BM25 算法改变） | `Save` 时写入版本号，`Load` 时检测版本不匹配则回退到 `BuildFromRepo` |
| 并发写入 | `Save` 需要获取写锁，防止重建过程中的并发 AddDocument |
| 持久化存储大小 | `termFreq` 是稀疏矩阵：100K 文档 × 平均 50 个唯一词 ≈ 5M 键值对。JSON/Protobuf 序列化后约 100-500MB。需评估序列化开销 |
| 多租户隔离 | 每个 tenant 独立的 BM25 索引，`Save`/`Load` 按 tenant 进行 |

---

## 方向三：内容预览管线断层 — 存储了但"看不见"

### 产品影响

| 维度 | 影响 |
|------|------|
| **用户感知价值** | 用户上传一张截图后，Web UI 显示 JSON 响应而不是图片。用户上传一个 PDF 报告，Web UI 显示原始 base64（或乱码）。这使得平台对于非技术用户几乎不可用 |
| **MCP/Agent 局限性** | MCP 的 `read_file` 工具返回原始文本截断到 4MB。Agent 的 `read_file` 截断到 4KB。Agent 无法"看图"、"看表格"、"看代码高亮"——它只能猜测文件内容 |
| **Web UI 竞争劣势** | 对比 Google Drive、Dropbox、Notion 等内容平台，内置预览是基线功能，不是差异化功能。缺失预览使得 Web UI 看起来像 API 调试器而不是产品 |
| **缩略图能力闲置** | `internal/thumbnail/thumbnail.go` 已经实现了图像缩略图生成，但没有前端消费它。`GET /v1/files/{key}/thumbnail` 端点存在但 Web UI 不使用 |

### 现状与代码证据

**证据 1：Web UI 只显示 JSON 响应**

Web UI 是一个纯静态 SPA（`internal/webui/static/`），通过 fetch 调用 REST API，然后将响应 body 原样显示为文本：

```html
<!-- internal/webui/static/index.html — 典型片段 -->
<div id="result">${JSON.stringify(data, null, 2)}</div>
```

没有条件渲染根据 `Content-Type` 切换预览模式。

**证据 2：MCP read_file 截断 + 纯文本**

```go
// internal/mcp/server.go:92-104
func (s *Server) toolReadFile(...) {
    body, err := io.ReadAll(io.LimitReader(rc, 4<<20))  // ← 截断 4MB
    return toolResult{Content: []contentBlock{{Type: "text", Text: string(body)}}}
}
```

没有根据 `obj.ContentType` 决定如何展示内容。Agent 的 `callReadFile` 更短（4KB）：

```go
// internal/ai/agent.go:119-126
func (a *Agent) callReadFile(...) {
    body, _ := io.ReadAll(io.LimitReader(rc, 4<<10))  // ← 4KB
}
```

**证据 3：Thumbnail 能力已存在但未被消费**

```go
// internal/thumbnail/thumbnail.go — 完整实现
func Generate(r io.Reader, maxW, maxH int) ([]byte, error) {
    // 解码 JPEG/PNG/GIF → 缩放到 maxW×maxH → 编码为 JPEG
}

// internal/api/rest/handler.go — 端点已注册
func (h *Handler) Thumbnail(w http.ResponseWriter, r *http.Request) { ... }
```

但 Web UI 的 `/thumbnail` 集成（显示缩略图标签 `<img src="/v1/files/.../thumbnail">`）不存在。

**证据 4：无 per-content-type 预览路由**

| 文件类型 | 理想预览 | 当前行为 |
|---------|---------|---------|
| `image/jpeg`, `image/png`, `image/gif` | 内联 `<img>` 标签 | 下载原始字节 |
| `text/markdown` | 渲染为 HTML | 显示原始 Markdown |
| `application/pdf` | 内嵌 PDF 查看器 (`<embed>`) | 下载/崩溃 |
| `text/html` | 安全沙箱预览 | 显示原始 HTML（含脚本！） |
| `audio/mpeg`, `audio/ogg` | `<audio>` 播放器 | 下载 |
| `video/mp4` | `<video>` 播放器 | 下载 |
| `text/csv` | 表格渲染 | 显示原始 CSV |
| `application/json` | 语法高亮 + 折叠 | 显示原始 JSON（无格式化） |
| `text/plain` | 语法高亮 | 显示纯文本 |

### 建议方案

**短期（1-2 周）：Web UI + MCP 预览增强**

1. **Web UI content-type dispatch**：在 `index.html` 中添加基于 `Content-Type` 的条件渲染：
   - `image/*`：`<img src="/v1/files/{key}">`
   - `text/markdown`：使用 `marked.js` 渲染为 HTML
   - `application/pdf`：`<embed src="/v1/files/{key}#view=FitH" type="application/pdf">`
   - `text/plain`, `application/json`：语法高亮（`highlight.js` 或 `prism.js`）
   - `audio/*`：`<audio controls src="...">`
   - `video/*`：`<video controls src="...">`

2. **MCP tool 扩展**：新增 `preview_file(key)` 工具，根据 content-type 返回结构化预览：
   - 图片：返回 base64 + 尺寸
   - 文本：返回前 N 行 + 语言检测
   - PDF：返回文本提取（调用 extractor）+ 页数
   - 表格：返回前 5 行作为 CSV

3. **缩略图注入**：Web UI 的文件列表（`search` 和 `list` tab）对 `image/*` 文件显示 `<img src=".../thumbnail?w=128&h=128">`。

**长期（中期）：预览服务流水线**

```
Request → PreviewHandler → ContentType 分派
    ├── image/* → thumbnail.Generate() → 缓存 → <img>
    ├── text/markdown → markdown.Render() → HTML (sanitized)
    ├── application/pdf → pdf2img / pdf.js → 首页预览
    ├── text/csv → csv.Parse() → <table> 前 20 行
    ├── application/json → json.Indent() + syntax highlight
    └── audio/*, video/* → 直接流式转发 + 播放器
```

### 边界情况

| 场景 | 处理 |
|------|------|
| 大文件预览（>100MB） | 限制预览大小为 10MB，超过返回"文件过大无法预览，请下载" |
| HTML 文件中的 XSS | 预览 HTML 时必须 sanitize（`DOMPurify` 或 `bluemonday`），禁止脚本执行 |
| PDF 跨域问题 | `<embed>` 可能需要 blob URL（`fetch → blob → URL.createObjectURL`）以规避 CORS |
| 私密文件预览 | 预览页必须同样经过 auth（通过 cookie 或 Authorization header 传递） |
| 浏览器不支持的文件类型 | 降级为下载提示 + Content-Type 标签 |

---

## 方向四：Webhook 死信语义污染 — "成功"与"永久失败"不可区分

### 运营影响

| 维度 | 影响 |
|------|------|
| **数据歧义** | Webhook 投递 10 次失败后被标记为 `succeeded=true`。运营人员查询"失败的 webhook"时会漏掉这些行。如果要找到真正的失败记录，必须在所有 `succeeded=true` 的行中检查 `last_error != ""` |
| **告警盲区** | Prometheus 指标 `webhook_delivery_total{status="failed"}` 在 10 次重试后不再递增。因为最后一次调用 `MarkWebhookSucceeded` 不会增加 failed 计数。持续失败的目标不会被告警系统发现 |
| **审计困难** | 合规审计需要区分"最终成功"和"在 N 次延迟重试后成功"和"彻底失败"。当前 schema 不能支持这种区分 |

### 现状与代码证据

**证据 1：代码明确承认语义污染**

```go
// internal/events/webhook.go:161-172
func (w *Webhook) retryOne(ctx context.Context, f repository.WebhookFailure) {
    // ...
    if attempts >= 10 {
        // ...
        // give up after 10 attempts: record the final failure detail, then retire the
        // row so it is no longer re-selected by NextPendingFailures. The schema only
        // has a binary `succeeded` flag (no dedicated dead-letter state), so we reuse
        // MarkWebhookSucceeded as the terminal transition — this intentionally
        // conflates "permanently dead" with "succeeded" to stop perpetual retries and
        // unbounded table growth. ListWebhookFailures still surfaces the last_error
        // for operators to inspect.
        _ = w.repo.UpdateWebhookFailure(...)
        _ = w.repo.MarkWebhookSucceeded(ctx, f.ID)
        return
    }
}
```

这段注释明确承认了语义污染，且说明原因是 schema 只有 `succeeded bool`。

**证据 2：WebhookFailure 模型缺少状态枚举**

```go
// internal/repository/webhook_failures.go:10-22
type WebhookFailure struct {
    ID          int64
    EventID     int64
    URL         string
    Payload     string
    Attempts    int
    LastError   string
    LastStatus  int
    NextRetryAt time.Time
    Succeeded   bool     // ← 二值布尔，"成功"和"死信"都=true
    CreatedAt   string
}
```

缺少 `Status` 字段（如 `pending` / `delivered` / `dead_lettered` / `failed`）。

**证据 3：管理 API 无状态过滤**

```go
// internal/api/rest/admin.go:150-165
func (h *AdminHandler) ListWebhookFailures(w http.ResponseWriter, r *http.Request) {
    failures, err := h.repo.ListWebhookFailures(ctx, tenant, limit)
    // 返回所有行，没有 ?status= 过滤参数
}
```

### 建议方案

**方案 A（最小改动）：添加 `Status` 枚举字段 + 新迁移**

新增迁移 `0025_webhook_failures_status`，添加 `status TEXT NOT NULL DEFAULT 'pending'` 列，支持三种状态：

| Status | 含义 | 触发时机 |
|--------|------|---------|
| `pending` | 投递失败，等待重试 | 首次失败 |
| `delivered` | 投递成功 | 2xx 响应 |
| `dead_lettered` | 重试耗尽，永久放弃 | 10 次重试后 |
| `cancelled` | 手动取消 | admin API 触发 |

代码修改：
- `MarkWebhookSucceeded` → `UpdateWebhookStatus(id, status)`
- `retryOne` 中 `attempts >= 10` → `UpdateWebhookStatus(id, "dead_lettered")`
- `ListWebhookFailures` 支持 `?status=dead_lettered` 过滤
- Admin API 新增 `POST /admin/webhook-failures/{id}/retry` 手动重试 dead-lettered 条目

**方案 B（向后兼容）：保持 `succeeded` 字段 + 新增 `status` 字段**

不改变 `succeeded bool` 的语义（保持 `delivered` 和 `dead_lettered` 都 true），新增 `status` 列作为权威状态。通过一段时间过渡后弃用 `succeeded`。

### 边界情况

| 场景 | 处理 |
|------|------|
| 已有数据的迁移 | `status` 默认值 `pending` 是最安全的选择（后续检查 `succeeded` 和 `attempts` 做修正） |
| 多目标 webhook | 每个事件到每个 URL 有独立的 `WebhookFailure` 行，允许一个目标 dead-lettered 而其他目标正常运行 |
| dead-lettered 后目标恢复 | Admin API 提供 `POST /admin/webhook-failures/{id}/retry`，将状态重置为 `pending` 并重置重试计数 |
| 告警集成 | Prometheus 告警规则 `webhook_dead_lettered_total > 0` 替代当前的 `webhook_delivery_total{status="failed"}` |

---

## 方向五：租户自助 API 缺失 — 所有操作依赖平台管理员

### 产品/平台影响

| 维度 | 影响 |
|------|------|
| **运维瓶颈** | 所有租户级操作（创建 API key、查看用量、配置通知规则、设置 CORS、管理生命周期）都需要平台管理员通过 admin API 操作。随着租户数量增长，管理员成为瓶颈 |
| **SaaS 就绪度** | 面向多租户的 SaaS 平台需要租户自助门户。缺少该能力使得 aero-vault 更像"多租户基础设施"而不是"多租户 SaaS 产品" |
| **开发者体验** | 开发者（租户侧）需要登录平台，创建自己的 API key，查看自己的使用量，设置自己的通知规则——他们不应该联系平台管理员做这些 |
| **scope 模型缺陷** | 当前 scope 只有 `read` / `write` / `admin`。没有 `self-service` scope 来允许租户访问自己的管理功能但不影响其他租户 |

### 现状与代码证据

**证据 1：所有管理功能挂载在 `/admin/` 下，scope 为 `admin`**

```go
// internal/api/rest/router.go:80-95
// Admin surfaces — 全部需要 admin scope
r.Put("/admin/tenants/{tenant}/quota", adm.SetQuota)
r.Put("/admin/tenants/{tenant}/budget", adm.SetBudget)
r.Get("/admin/keys", adm.ListKeys)
r.Post("/admin/keys", adm.AddKey)
r.Delete("/admin/keys/{token}", adm.RevokeKey)
r.Post("/admin/jwt", adm.IssueJWT)
r.Get("/admin/webhook-failures", adm.ListWebhookFailures)
r.Get("/admin/jobs", adm.ListJobs)
r.Post("/admin/jobs/{id}/retry", adm.RetryJob)
r.Post("/admin/tenants", adm.CreateTenant)
r.Get("/admin/tenants", adm.ListTenants)
r.Delete("/admin/tenants/{tenant}", adm.DeleteTenant)
r.Put("/admin/tenants/{tenant}/status", adm.SetTenantStatus)
r.Get("/admin/audit", adm.ListAudit)
```

**证据 2：scope 校验简单，无 `self-service` 概念**

```go
// internal/auth/auth.go:85-110
// scope 在 middleware 中校验
func (r *Registry) Middleware() func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
            // ...
            // 仅校验 scope 是否在 key 的 scopes 列表中
            // scopes: ["read", "write", "admin"]
            // 无 "self-service" scope
        })
    }
}
```

**证据 3：租户可以查看自己的用法但无法管理**

当前唯一对租户开放的"管理"类路由是 `GET /v1/usage`（`r.Get("/usage", adm.Usage)`）。但无法：
- 创建/撤销自己的 API key
- 查看自己的配额和已用量
- 配置自己的桶策略、CORS、通知规则（这些虽然挂在 bucket 级别，但在 admin 路由之外，受 scope 限制）
- 查看自己的 webhook 失败
- 查看自己的审计日志
- 管理自己的生命周期策略

### 建议方案

**新增 `/v1/me/` 路由组**

在 `router.go` 中添加一个 `self-service` scope 保护的新路由组：

```go
// 新增租户自助路由
r.Group(func(r chi.Router) {
    r.Use(requireSelfService)
    
    // API Keys
    r.Post("/me/keys", h.CreateSelfKey)          // 创建自己的 API key（scope=self-service）
    r.Get("/me/keys", h.ListSelfKeys)             // 列出自己的 keys
    r.Delete("/me/keys/{id}", h.RevokeSelfKey)    // 撤销自己的 key
    
    // 用量/配额
    r.Get("/me/usage", h.SelfUsage)               // 查看自己的用量
    r.Get("/me/quota", h.SelfQuota)               // 查看自己的配额
    
    // Bucket 配置（仅限于当前 tenant 的 bucket）
    r.Get("/me/buckets/{bucket}/policy", h.GetBucketPolicy)
    r.Put("/me/buckets/{bucket}/policy", h.PutBucketPolicy)
    r.Get("/me/buckets/{bucket}/cors", h.GetBucketCORS)
    r.Put("/me/buckets/{bucket}/cors", h.PutBucketCORS)
    r.Get("/me/buckets/{bucket}/notification", h.GetBucketNotifications)
    r.Put("/me/buckets/{bucket}/notification", h.PutBucketNotifications)
    r.Get("/me/buckets/{bucket}/lifecycle", h.GetBucketLifecycle)
    r.Put("/me/buckets/{bucket}/lifecycle", h.PutBucketLifecycle)
    r.Get("/me/buckets/{bucket}/logging", h.GetBucketLogging)
    r.Put("/me/buckets/{bucket}/logging", h.PutBucketLogging)
    
    // 审计/监控
    r.Get("/me/webhook-failures", h.SelfWebhookFailures)
    r.Get("/me/audit", h.SelfAudit)
    
    // Bucket 管理
    r.Post("/me/buckets", h.CreateSelfBucket)
    r.Delete("/me/buckets/{bucket}", h.DeleteSelfBucket)
})
```

**scope 模型扩展**

在 `auth.Scope` 中添加 `self-service` scope，允许操作所有 `me/` 路径的请求：

```go
// scope 枚举扩展
const (
    ScopeRead         = "read"
    ScopeWrite        = "write"
    ScopeAdmin        = "admin"
    ScopeSelfService  = "self-service"    // 新增
)
```

**Web UI 自助门户**

Web UI 增加一个"Settings" tab，提供：
- 创建/管理 API keys 的界面
- 查看用量图表（存储、带宽、AI 调用）
- 配置桶策略、CORS、通知规则的 form
- Webhook 失败投递的监控面板

### 边界情况

| 场景 | 处理 |
|------|------|
| 租户通过 self-service 创建无限制 API key | 租户只能创建 `read`/`write` scope 的 key，不能创建 `admin` 或 `self-service` scope 的 key |
| 租户误删所有自己的 API key | 平台管理员可以通过 admin API 创建新的 key；Web UI 应提醒至少保留一个 key |
| 租户试图查看其他租户的配额 | `SelfQuota` 使用 `TenantFrom(ctx)` 自动限定到当前租户 |
| 已有 `read`/`write` scope 的兼容性 | 现有 scope 不应被破坏；`self-service` 是新增的可选 scope |
| 免费租户 vs 付费租户 | `self-service` scope 可以按 tier 选择性开放（免费租户只给 `read`/`write`，付费租户增加 `self-service`） |

---

## 跨方向关联与实施建议

### 方向依赖关系

```
方向一 (MCP/WebDAV Auth)
    ↓ 需要 auth.Registry 扩展
方向五 (租户自助 API)
    ↓ 需要 scope 模型扩展（self-service scope）
方向三 (内容预览管线)
    ↓ 需要 thumbnail 缓存 + Web UI 扩展
方向四 (Webhook 死信语义)
    ↓ 独立，仅迁移 + repository + admin API
方向二 (BM25 持久性)
    ↓ 独立，仅 ai/bm25.go + repository
```

### 建议实施顺序

| 轮次 | 方向 | 预估工作量 | 理由 |
|------|------|-----------|------|
| **第 1 轮** | 方向一（MCP/WebDAV Auth） | **小**（2-3 天） | 安全漏洞修复。MCP 和 WebDAV 绕过认证是真正的安全风险。快速修复，影响最小 |
| **第 2 轮** | 方向四（Webhook 死信语义） | **小**（2-3 天） | 数据质量修复。当前行为导致运营盲区。新增 migration + 状态枚举 + admin API 过滤即可完成 |
| **第 3 轮** | 方向二（BM25 持久性） | **中**（3-5 天） | AI 管线的生产就绪度改进。持久化方案可选方案 C（文件序列化，最轻量） |
| **第 4 轮** | 方向五（租户自助 API） | **中**（5-7 天） | 平台化关键步骤。需要 scope 模型扩展 + 新路由 + Web UI 设置 tab |
| **第 5 轮** | 方向三（内容预览管线） | **大**（7-14 天） | 产品体验改进。涉及 Web UI 改造 + MCP 工具扩展 + 可能的后端预览端点。建议分阶段：第一期仅 Web UI 预览，第二期 MCP 预览工具 |

### 快速验证清单

```bash
# 方向一 — MCP 是否经过 auth
curl -v -X POST http://localhost:8080/mcp \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' \
  -H "Content-Type: application/json"
# → 应返回 401 / 403（如果 auth 启用），当前返回 200

# 方向一 — WebDAV 是否经过 auth
curl -v -X PROPFIND http://localhost:8080/webdav/
# → 应返回 401 / 403（如果 auth 启用），当前返回 200

# 方向二 — BM25 持久性
# 启动后调用 search mode=hybrid，记录响应时间
# 重启后立即再次调用，比较响应时间（重启后应为冷启动）

# 方向三 — 内容预览
# 上传一张图片后，在 Web UI 中查看文件详情
# 当前应显示 JSON 字符串而不是图片

# 方向四 — 死信语义
# 配置一个无法到达的 webhook URL，触发事件
# 等待 10 次重试后，查询 webhook_failures
# SHOW status WHERE succeeded=true AND last_error != ""
# → 当前 succeeded=true 但实际上是 dead-lettered

# 方向五 — 自助 API
curl -X POST http://localhost:8080/v1/me/keys \
  -H "Authorization: Bearer <tenant-key>" \
  -H "Content-Type: application/json" \
  -d '{"label":"my-key"}'
# → 当前返回 404 或 403，无路由匹配
```

---

## 附录：关键 Grep 验证结果

```bash
# 方向一 — MCP 无 auth 引用
grep -rn "auth\.Registry\|auth\.Parse\|Bearer\|Authorization" internal/mcp/ --include="*.go"
# → 零命中

# 方向一 — WebDAV 无 auth 引用
grep -rn "auth\.Registry\|auth\.Parse\|Bearer\|Authorization" internal/api/webdav/ --include="*.go"
# → 零命中

# 方向二 — BM25 无持久化
grep -rn "Save\|Load\|Persist\|Serialize\|Dump\|Restore\|snapshot\|gob\|protobuf" internal/ai/bm25.go
# → 零命中

# 方向三 — Web UI 无 content-type 驱动预览
grep -rn "image\|audio\|video\|pdf\|markdown\|preview\|render\|highlight" internal/webui/static/index.html
# → 零命中（或无预览相关逻辑）

# 方向四 — 死信语义
grep -rn "MarkWebhookSucceeded\|dead.letter\|succeeded = true" internal/events/webhook.go
# → 找到 `MarkWebhookSucceeded` 在死信路径上被调用

# 方向五 — 自助 API
grep -rn "/me/\|self.service\|SelfService\|ScopeSelfService" internal/api/rest/ --include="*.go"
# → 零命中
```
