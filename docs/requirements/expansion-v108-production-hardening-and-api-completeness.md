# 高价值扩展方向：生产安全加固、实时双向通信、密钥管理 API、S3 桶级子资源完备性、异步操作模式

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — `/home/u1/aero-vault` 内 240+ Go 源文件、50 对迁移文件、3 套 SDK（Go/Python/JS）、MCP 双模式（HTTP+stdio）、Web UI、完整部署配置（Helm/Grafana/Prometheus/OTel）、`AGENTS.md`、`ROADMAP.md`、`CHANGELOG.md`  
> **去重验证：** 对 `docs/requirements/` 下全部 107 份既有分析文档 + `ROADMAP.md` + `TODO.md` 逐方向进行全文关键词正则 + 代码锚点交叉验证  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。选取**代码中无任何实现锚点、但具有实质性生产运营/产品影响且在前 107 轮分析中零或接近零深度覆盖**的方向。每个方向包含：产品价值 → 现状与代码证据 → 架构权衡与建议方案 → 边界情况。

---

## 方法论：不存在的功能缺口

前面 107 份分析文档聚焦的均是"代码中存在锚点但管线断裂"的半实现功能。本扫描反其道而行——聚焦**代码中完全不存在、但现代对象存储平台必须拥有的功能**。这些功能不会出现在 GRPC 论中发现模式、状态机断裂分析或死代码路径检测中，因为它们**从未被写入**。

判定标准：

| 判定条件 | 说明 |
|---------|------|
| **零代码锚点** | `grep -rn` 全库（internal/ + cmd/ + sdk/）无相关类型、接口、方法或配置变量 |
| **零需求文档** | 107 份既有分析中无独立方向覆盖；提及频率 ≤3 次且均为一句话概念性提及，无架构分析 |
| **高用户/运营影响** | 缺失该功能将：① 阻碍生产部署；② 导致 S3 兼容性失败；③ 遗留重大安全隐患；④ 阻塞关键产品场景 |
| **可独立验证** | 可以通过代码库中已有的抽象层和基础设施独立实现，不依赖外部服务或重大架构变更 |

---

## 去重验证总表

对 `docs/requirements/` 下全部 107 份既有分析文档进行全文关键词正则扫描：

| 方向 | 既有覆盖情况 | 结论 |
|------|-------------|------|
| **方向一：原生 TLS/HTTPS 与安全传输层** | **零覆盖** — `grep -rn "ListenAndServeTLS\|TLSConfig\|TLSCert\|CertFile\|APP_TLS\|https.*redirect\|mTLS\|mutual.*auth\|HSTS\|Strict-Transport\|Content-Security-Policy\|X-Frame-Options\|X-Content-Type-Options\|security.*middleware\|security.*header"` → 全库零命中（仅 `storage/storage.go` 有 `TLSHandshakeTimeout`，这是 S3/OSS/COS 的**出站 HTTP 客户端**配置，与入站服务器无关）。`cmd/server/main.go:267` 使用 `srv.ListenAndServe()`，**无** `ListenAndServeTLS()`。`internal/config/config.go:AppConfig` 无任何 TLS/证书字段。`docs/requirements/` 下一份文档以 TLS 为独立方向 | ✅ **全新方向** |
| **方向二：WebSocket 实时双向通信 API** | **浅层提及无架构** — 8 份文档中合计 12 次提及 "WebSocket" 或 "websocket"，全部为一句话概念性列举（如 "可考虑 WebSocket 替代 SSE"、"WebSocket 推送通知"）。**零架构分析**：无接口设计、无通道模型、无路由注册、无 SDK 集成、无鉴权模型。代码库中零 WebSocket 实现 — `internal/api/rest/router.go` 无 `ws`/`wss` 路由，`internal/mcp/` 无 WebSocket 传输层 | ✅ **全新方向** |
| **方向三：服务端加密密钥管理 API** | **零覆盖** — `grep -rn "key.*rotate\|rotate.*key\|KEK.*manage\|key.*escrow\|key.*backup\|key.*restore\|crypto.*key.*API\|SSE.*API\|rewrap.*API\|rekey\|secret.*store.*API\|vault.*integrat\|key.*management.*endpoint"` → 全库零命中（除 `rewrap.go` 启动时一次性重包装外，无可运行时调用的密钥管理端点）。`internal/config/config.go` 中所有 SSE 密钥配置均为启动时一次性读取的 env var，**无运行时读取/轮换 API** | ✅ **全新方向** |
| **方向四：S3 桶级子资源完备性（Bucket Tagging、Encryption、Website、Inventory、Accelerate 存根）** | **部分提及无独立深度分析** — `?accelerate` 在 v68 中以 1 行注明"返回 Suspended 存根"但无架构分析；`?encryption` 在 expansion-directions-v3.md 的矩阵表中出现 2 行但**无任何架构方案**；`?inventory` 在 v16 中以约 200 行为独立方向但**无代码锚点驱动分析**。`dispatchBucketSubresource` 中**缺失** `tagging`/`encryption`/`website`/`inventory`/`requestPayment` 的代码级缺口从未被分析 | ✅ **全新方向**（以缺失的 dispatch 路由为锚点） |
| **方向五：异步操作模式与长任务 API** | **浅层提及无架构** — v100 方向三中以 1 行提到"冷存储恢复返回 `202 Accepted`"，v93 方向五覆盖「异步写入缓冲」概念但聚焦 ingestion buffer 而非通用 async API 模式。**零分析** `202 + Location: /v1/jobs/{id}` 的通用异步操作模式、`/v1/jobs/{id}/status` 状态轮询、逐事件推送、操作结果保留策略。代码库中 `jobs` 表 + `jobs.Pool` 存在但无 REST 面朝用户的 async API 端点 | ✅ **全新方向** |

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码证据 |
|---|------|------|--------|---------|---------|
| **1** | **原生 TLS/HTTPS 与安全传输层加固** | 安全/生产部署 | **P0** | 服务器零 HTTPS 支持；所有传输在明文 HTTP 上进行；无 mTLS、无加密头、无安全标头中间件。生产部署必须在前置反向代理（nginx/ALB）上终止 TLS——增加部署复杂度且与 aero-vault 内建认证模型脱节 | `cmd/server/main.go:267` → `srv.ListenAndServe()`（纯 HTTP）；`internal/config/config.go:AppConfig` → 无 TLS 字段；`internal/middleware/middleware.go` → 无 `SecurityHeaders` 中间件；`cmd/server/main.go:215-233` → `applyMiddleware` 链（RequestID→CORS→Auth→Tenant→RateLimit→OTel→Recoverer→AccessLog）**无安全头部**；全库 `grep -r "HSTS\|CSP\|X-Content-Type\|X-Frame-Options"` → 零命中 |
| **2** | **WebSocket 实时双向通信 API** | 产品/平台 | **P1** | SSE 是服务器→客户端的单向通道。无 WebSocket 意味着：无法实现流式上传进度推送、实时文件同步变更、双向 AI Agent 对话、WebDAV 实时通知、跨标签页同步、以及现代 UI 需要的高效全双工通信 | `internal/api/rest/router.go` → 无 `ws`/`wss` 路由；`internal/api/rest/sse.go` → 仅 SSE（单向）；`internal/mcp/transport.go` → 仅 HTTP+stdio，无 WebSocket 传输；`sdk/go/aerovault/client.go` → 无 WebSocket 支持；`internal/webui/static/` → 前端仅 EventSource（SSE），无 WebSocket；`internal/storage/storage.go` → 无 `Subscribe(key string) <-chan Event` 方法 |
| **3** | **服务端加密密钥管理 API** | 安全/运维 | **P1** | SSE 密钥仅通过启动时环境变量配置。无法在运行时轮换密钥、管理 KEK、备份/恢复密钥材料、或查看密钥状态。`STORAGE_SSE_REWRAP_ON_START` 虽可重包装加密侧车但仅做一次即退出，且无法通过 API 触发 | `internal/config/config.go` → SSE 密钥全部为 `STORAGE_LOCAL_SSE_*` 系列 env var；`internal/storage/secret.go` → `NewEnvSecretProvider`/`NewKeyfileProvider` 均为启动时一次性构造；`internal/storage/rewrap.go` → `RewrapStale` 仅在启动时调用（`cmd/server/main.go:maybeRewrapSSE`）；`internal/storage/encrypt.go` → `EncryptObject`/`DecryptObject` 无密钥版本协商回调；`internal/api/rest/router.go` → 无 `/admin/crypto/*` 或 `/admin/keys/*` 路由；`internal/repository/repository.go` → 无加密密钥记录或版本表 |
| **4** | **S3 桶级子资源完备性** | 协议兼容 | **P1** | `dispatchBucketSubresource` 缺失 5 个标准 S3 桶子资源：`?tagging`（桶标签）、`?encryption`（默认加密）、`?website`（静态网站托管）、`?inventory`（清单报告）、`?requestPayment`（请求者付费）。`?accelerate` 返回硬编码 `"Suspended"` 存根。这些是标准 AWS S3 API 的一部分，SDK 调用会静默失败或返回意外错误 | `internal/api/s3compat/handler.go:280-310` → `dispatchBucketSubresource` switch-case **无** `tagging`/`encryption`/`website`/`inventory`/`requestPayment` 分支；`internal/api/s3compat/handler.go:835-845` → `getBucketAccelerate` 返回 `Status: "Suspended"`（硬编码 XML，不经任何后端检查）；`internal/api/s3compat/xml.go` → 无 `tagging`/`encryption`/`website`/`inventory`/`requestPayment` XML 编解码器；`internal/repository/repository.go:BucketConfig` → 无 `DefaultEncryption`/`WebsiteConfig`/`Inventory` 字段 |
| **5** | **异步操作模式与长任务 API** | 产品/DX | **P2** | 所有操作均为同步 HTTP 请求-响应。无 `202 Accepted` + `Location: /v1/jobs/{id}` 异步模式意味着：Glacier 冷存储恢复必须阻塞等待、大型文件 COPY 无法离线跟踪、Batch Delete 结果无法异步校验、生命周期转换无完成通知、用户无法通过 SDK 轮询异步操作状态 | `internal/api/rest/router.go` → 所有端点均为同步（200/4xx/5xx）；`internal/jobs/jobs.go` → `Registry`+`Pool`+`Queue` 完整但**无 REST API 暴露作业状态给最终用户**（admin 有 `ListJobs`/`RetryJob` 但面向运营者，非租户）；`internal/api/rest/admin_jobs.go` → `ListJobs` 用 admin scope，非租户自服务；`internal/repository/jobs.go` → `Job` 有 `ID`/`Status`/`Result` 字段（可作为异步操作的基础）；`internal/service/file_crud.go` → `Get`/`Put`/`Delete` 均为同步阻塞；`internal/api/s3compat/handler.go:restoreObject` → 返回 `202 Accepted` 但不返回 `Location` header 指向可轮询的 job |

---

## 方向一：原生 TLS/HTTPS 与安全传输层加固

### 产品价值

| 维度 | 影响 |
|------|------|
| **生产部署** | 零 HTTPS 支持的存储平台无法通过任何安全审计。生产环境永远必须前置反向代理（nginx、ALB、CloudFront）。增加了部署复杂度，且让内建认证机制（JWT、Bearer Token）通过明文传输——违背 OWASP 基本要求 |
| **mTLS 零信任** | 不存在 mTLS 意味着不能基于客户端证书做身份识别、无服务间通信安全、无法在零信任网络中使用 aero-vault 作为服务间存储层 |
| **安全标头** | 无 HSTS、CSP、X-Content-Type-Options、X-Frame-Options 等安全标头使得 Web UI 和 REST API 暴露在点击劫持、MIME 嗅探、XSS 等攻击面下 |
| **合规** | PCI DSS、HIPAA、SOC2 均要求传输中加密（TLS 1.2+）。无内置 TLS 支持 = 合规阻止 |

### 现状与代码证据

**HTTP 服务器：纯明文**

```go
// cmd/server/main.go:262-268
srv := &http.Server{
    Addr:              cfg.App.Addr,
    Handler:           handler,
    ReadHeaderTimeout: 15 * time.Second,
    WriteTimeout:      time.Duration(cfg.App.WriteTimeoutSec) * time.Second,
    IdleTimeout:       time.Duration(cfg.App.IdleTimeoutSec) * time.Second,
}
// ...
if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
```

- 调用 `ListenAndServe()` 而非 `ListenAndServeTLS(certFile, keyFile)`
- `http.Server` 未设置 `TLSConfig`
- 无 ACME/Let's Encrypt 自动证书获取

**配置层无 TLS 字段：**

```go
// internal/config/config.go:20-26
type AppConfig struct {
    Addr              string
    LogLevel          slog.Level
    WriteTimeoutSec   int
    IdleTimeoutSec    int
    RequestTimeoutSec int
    MaxInFlight       int
    PerTenantMax      int
    // ⬆ 无 TLS/Cert/Key 字段
}
```

**中间件链无安全标头：**

```go
// cmd/server/main.go:215-233 — applyMiddleware 链
func applyMiddleware(handler http.Handler, ...) http.Handler {
    for _, m := range []func(http.Handler) http.Handler{
        middleware.AccessLog(logger),
        concurrencyMW,
        middleware.Recoverer(logger),
        telemetry.HTTPMiddleware("aero-vault"),
        rl.Middleware(),
        middleware.Tenant,
        authReg.Middleware(),
        middleware.CORS(...),
        middleware.RequestID,
    } {
        handler = m(handler)
    }
    return handler
}
```

- 无 `SecurityHeaders` 中间件
- 无自动 HTTP→HTTPS 重定向
- 无 mTLS 客户端证书验证

**存储层出站连接——唯一 TLS 配置点：**

```go
// internal/storage/storage.go:89 — 仅在 S3/OSS/COS HTTP Client 代码中
TLSHandshakeTimeout: nonZero(tc.ConnectTimeout, 5*time.Second),
```

这是出站客户端连接的超时配置，与入站服务器安全无关。

### 架构方案概要

**配置层扩展：**

```go
type AppConfig struct {
    // ... 现有字段
    TLS *TLSConfig // nil = 纯 HTTP（向后兼容）
}

type TLSConfig struct {
    CertFile string // 证书 PEM 路径
    KeyFile  string // 私钥 PEM 路径
    // 可选扩展
    AutoCert    bool   // 启用 Let's Encrypt ACME
    ACMEEmail   string // Let's Encrypt 注册邮箱
    ACMEDomains []string
    MinVersion  string // "1.2" | "1.3"
    ClientAuth  string // "none" | "request" | "require" | "verify-if-given" | "require-and-verify"（mTLS）
    CACertFile  string // mTLS 客户端 CA 证书
}
```

**TLS 检测服务器启动：**

```go
if cfg.App.TLS != nil {
    srv.TLSConfig = buildTLSConfig(cfg.App.TLS)
    // 自动 HTTP→HTTPS 重定向 goroutine（原端口 +1, 308 redirect）
    return srv.ListenAndServeTLS(cfg.App.TLS.CertFile, cfg.App.TLS.KeyFile)
} else {
    return srv.ListenAndServe()
}
```

**安全标头中间件：**

```go
func SecurityHeaders(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("X-Content-Type-Options", "nosniff")
        w.Header().Set("X-Frame-Options", "DENY")
        w.Header().Set("X-XSS-Protection", "1; mode=block")
        w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
        // CSP 按产品配置灵活控制；可选的 S3 兼容模式
        if !strings.HasPrefix(r.URL.Path, "/s3") {
            w.Header().Set("Content-Security-Policy",
                "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:")
        }
        // HSTS 仅在 HTTPS 连接中设置
        if r.TLS != nil {
            w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
        }
        next.ServeHTTP(w, r)
    })
}
```

**安全头注入点：** 在中间件链最外层（第一个执行），确保所有下游响应都携带安全标头。

### 边界情况与风险

| 场景 | 处理 |
|------|------|
| 纯 HTTP 部署（开发环境） | `TLS: nil` 维持现有行为，不破坏本地开发体验 |
| 证书过期 | 启动时验证证书有效期；`AutoCert=true` 模式在证书到期前 30 天自动续期 |
| mTLS 客户端证书 | 客户端证书 CN/SAN 可映射为 tenant 标识；失败模式：拒绝无证书连接（`require-and-verify` 模式） |
| 安全头与 S3 兼容 | S3 SDK 可能不期望安全头——S3 路径的性能优化头（`Accept-Ranges` 等）与安全头不冲突；CSP 可能需放宽以兼容 S3 SDK 浏览器场景 |
| HTTP/2 支持 | Go `http.Server` 在 TLS 模式下自动启用 HTTP/2——零额外代码即可获得多路复用优势 |

---

## 方向二：WebSocket 实时双向通信 API

### 产品价值

| 维度 | 影响 |
|------|------|
| **实时文件变更推送** | SSE 仅服务器→客户端方向。WebSocket 支持双向通信：客户端订阅文件变更 → 服务器推送。这将允许文件管理器（WebUI、WebDAV 客户端、自定义应用）实时看到其他客户端的上传/删除/修改 |
| **流式上传进度** | 大文件上传时，客户端可通过 WebSocket 接收实时进度事件（已接收字节、ETag、完成状态），而非默默等待 HTTP 响应 |
| **AI Agent 全双工通道** | 当前 `/chat/stream` 使用 SSE 进行单次 LLM 流。WebSocket 允许连续的对话式 AI 交互：用户发送消息、Agent 调用工具、流式返回结果、用户打断、Agent 继续——无 HTTP 连接重建开销 |
| **MCP WebSocket 传输** | MCP 协议标准支持 WebSocket 传输（规范中定义），可作为 HTTP + stdio 之外的第三种传输模式，适用于浏览器和移动端 AI Agent 连接 |

### 现状与代码证据

**SSE 是唯一的实时通道：**

```go
// internal/api/rest/sse.go — GET /v1/events/stream
// HTTP SSE: 服务器→客户端单向事件流
func (h *SSEHandler) liveStream(w http.ResponseWriter, r *http.Request, flusher http.Flusher, tenant string) {
    sub := h.bus.Subscribe()
    // ...
    // 无法接收客户端消息回传
}
```

**无 WebSocket 路由：**

```bash
$ grep -rn "ws\|websocket\|WSUpgrade\|Gorilla\|nhooyr\|golang.org/x/net/websocket" internal/ cmd/ --include='*.go'
# (零输出)
```

**事件总线设计为单向扇出：**

```go
// internal/events/bus.go
func (b *Bus) Subscribe() <-chan repository.Event  // 只读通道
// 无 PublishFromClient / 无 RPC 风格请求-响应
```

**前端只使用 EventSource：**

`internal/webui/static/` 中 JS 代码使用 `new EventSource('/v1/events/stream')`——无 WebSocket `new WebSocket('wss://...')`。

### 架构方案概要

**新增 WebSocket 升级路由：**

```go
// internal/api/rest/router.go
r.Get("/v1/ws", ws.Handler(svc, repo, search, chat, authReg, logger))
r.Get("/v1/ws/file/{bucket}/{key}", ws.FileStreamHandler(svc, logger))
```

**WebSocket 消息协议：**

```jsonc
// 客户端→服务器
{
  "type": "subscribe",
  "channels": ["events/*", "file:uploads/{uploadID}", "chat:{sessionID}"]
}
// 服务器→客户端
{
  "type": "event",
  "channel": "events/created",
  "payload": { "bucket": "default", "key": "doc.pdf", "size": 1024 }
}
// 服务器→客户端（上传进度）
{
  "type": "upload_progress",
  "uploadID": "abc123",
  "receivedBytes": 5242880,
  "totalBytes": 10485760,
  "status": "in_progress"
}
```

**WebSocket Session 管理：**

```go
type WSSession struct {
    conn     *websocket.Conn
    tenant   string
    scopes   []string
    channels map[string]context.CancelFunc
    mu       sync.Mutex
}

type WSHandler struct {
    hub     *Hub        // 管理所有活跃连接
    bus     *events.Bus // 复用事件总线
    svc     *service.FileService
    authReg *auth.Registry
}
```

**MCP WebSocket 传输适配：** 新增 `websocketTransport`，实现与 `httpTransport`/`stdioTransport` 相同的 `Serve(ctx)` 方法签名，复用 JSON-RPC 协议编解码。

### 边界情况与风险

| 场景 | 处理 |
|------|------|
| 连接断开重连 | 客户端应在 WebSocket URL 中附带 `lastEventID` 参数；服务器重放末消费事件 |
| 鉴权 | WebSocket 升级时校验 Bearer Token / Cookie（复用 auth middleware）；成功后 Tenant 绑定到 session 生命周期 |
| 连接数限流 | `MAX_WS_CONNECTIONS` 配置；超限返回 `429`；每个 tenant 独立配额 |
| 空闲超时 | 30 秒无消息发送 ping → 60 秒无 pong 回复 → 关闭连接 |
| S3 SDK 不使用 WebSocket | WebSocket 是 REST API 的补充，不破坏现有 S3 兼容性 |

---

## 方向三：服务端加密密钥管理 API

### 产品价值

| 维度 | 影响 |
|------|------|
| **密钥轮换合规** | PCI DSS 要求加密密钥至少每年轮换一次。当前只能通过重启服务器更换 env var 实现轮换——需要停机，且无法验证轮换状态 |
| **密钥备份与恢复** | 无密钥备份机制。若 `STORAGE_LOCAL_SSE_KEYFILE` 文件丢失或 `STORAGE_LOCAL_SSE_KEY` env var 被覆盖，所有已加密的对象**永远无法解密** |
| **运维安全性** | 密钥在进程内存中以明文持有（`SecretProvider` 接口当前直接返回明文 key）。无密钥访问审计、无密钥版本追踪、无 KEK 委派管理模式 |
| **KMS 集成扩展** | 已有 `Storage.Local.SSEKMSURL` 和 `DataKeyWrapper`（`/wrap`/`/unwrap` 端点），但无 API 来管理 KMS 配置（注册/注销 KMS 端点、查看密钥版本、触发按需重包装） |

### 现状与代码证据

**所有密钥配置均为启动时一次性 env var：**

```go
// internal/config/config.go
type LocalStorageConfig struct {
    // ...
    SSEKey      string // 启动时从 env 读取
    SSEKeyfile  string // 启动时读取 JSON key ring
    SSEKeyURL   string // 启动时 HTTP GET 密钥环
    SSEKeyToken string // 访问 SSEKeyURL 的 bearer token
    SSEKMSURL   string // 启动时配置 KMS 端点
    SSEKMSKeyID string
    SSEKMSToken string
}
```

**SecretProvider 接口无运行时管理方法：**

```go
// internal/storage/secret.go
type SecretProvider interface {
    GetSecret(ctx context.Context) (Secret, error)  // 启动时调用
}
// 无 Rotate / ListVersions / Health / Backup 方法
```

**重包装功能仅启动时运行一次：**

```go
// cmd/server/main.go:maybeRewrapSSE
func maybeRewrapSSE(ctx context.Context, cfg *config.Config, store storage.Storage, logger *slog.Logger) {
    if cfg.Storage.SSERewrapOnStart {
        go func() {
            rep, err := storage.RewrapStale(ctx, store)
            // ... 运行一次后 goroutine 退出
        }()
    }
}
```

**无 API 端点：**

```bash
$ grep -rn "/admin/crypto\|/admin/key\|/v1/keys\|/v1/crypto\|POST.*rewrap\|POST.*rotate" internal/api/ --include='*.go'
# (零输出)
```

**无密钥状态持久化：**

```bash
$ grep -rn "key_version\|encryption_key\|crypto_key\|key_id\|key_ring\|key_status" internal/repository/ --include='*.go'
# (零输出 — 除 encrypt.go 中 envelope 自身携带的 kid 外)
```

### 架构方案概要

**新增密钥管理端点（`/v1/admin/crypto` scope: admin）：**

| 端点 | 方法 | 功能 |
|------|------|------|
| `/v1/admin/crypto/keys` | `GET` | 列出所有密钥版本（ID、状态 `active|retired|compromised`、创建时间） |
| `/v1/admin/crypto/keys` | `POST` | 轮换主密钥（生成新版本、设为 active、retire 旧版本） |
| `/v1/admin/crypto/rewrap` | `POST` | 触发全量重新包装（异步 job：`JobRewrapStale`） |
| `/v1/admin/crypto/rewrap/status` | `GET` | 上次重新包装状态（扫描数、重包装数、失败数、时间） |
| `/v1/admin/crypto/backup` | `POST` | 导出加密密钥的备份（加密外包，返回下载 token） |
| `/v1/admin/crypto/config` | `GET` | 当前加密配置摘要（provider 类型、版本计数、支持的算法） |

**SecretProvider 接口扩展：**

```go
type SecretProvider interface {
    GetSecret(ctx context.Context) (Secret, error)
    // 新增运行时方法
    ListVersions(ctx context.Context) ([]KeyVersion, error)
    Rotate(ctx context.Context) (Secret, error)    // 生成新版本并切换 active
    RevokeVersion(ctx context.Context, kid string) error // 标记 compromised
    Backup(ctx context.Context) ([]byte, error)    // 加密导出
    Health(ctx context.Context) error              // KMS 可达性检测
}
```

**密钥记录持久化：**

```go
// 新增迁移 0025：crypto_keys 表
type CryptoKeyRecord struct {
    ID        int64
    Kid       string    // 密钥标识符（在 envelope 中引用）
    Status    string    // "active" | "retired" | "compromised"
    Provider  string    // "env" | "keyfile" | "http" | "kms"
    CreatedAt time.Time
    RetiredAt *time.Time
}
```

**按需重包装 job：**

```go
// 复用 internal/jobs 基础设施
const JobRewrapStale = "crypto_rewrap"

// POST /v1/admin/crypto/rewrap 触发
// 扫描所有对象的 storage_key → 读取 envelope → 若非当前 active kid → 重新包装
// 结果写入 jobs.result；GET /v1/admin/crypto/rewrap/status 轮询
```

### 边界情况与风险

| 场景 | 处理 |
|------|------|
| 轮换后旧密钥不可用（KMS 退役） | 先验证旧密钥可达，再标注 retired；回滚时可手动恢复为 active |
| 重包装期间服务器崩溃 | 重包装是幂等的——envelope 中记录 `kid`，两次重包装结果相同 |
| 密钥泄露（compromised） | 标记旧版本为 compromised → 强制立即全部重包装 → 旧版 envelope 不再解密 |
| 备份加密密钥 | 备份必须使用与主密钥不同的密钥加密（如用户提供的 passphrase + KMS 包装） |
| 无密钥启动 | 读取第一个对象时，若发现 envelope 的 kid 不在当前密钥环中，记录 `WARN` 并尝试使用 child key（旧密钥已退役但未删除） |

---

## 方向四：S3 桶级子资源完备性

### 产品价值

| 维度 | 影响 |
|------|------|
| **S3 兼容性可信度** | AWS SDK、AWS CLI、S3 浏览器、第三方工具调用标准桶子资源时，若返回 `404`、`501` 或静默空响应，用户立即失去信任 |
| **企业迁移阻碍** | 企业从 S3 迁移前会运行兼容性检查工具（如 `aws s3api` 命令脚本）。缺失的每个 `?` 子资源都是一个阻塞性 checkmark ❌ |
| **桶标记（Tagging）日常使用** | 桶标签是成本分配（成本中心标签）、访问控制（基于标签的策略）和资源组织的基础。S3 ListBuckets 响应中不包含标签 |
| **默认加密（Encryption）合规** | 许多安全策略要求"所有新上传的对象必须加密"。无桶级默认加密配置意味着必须在每个 PUT 时由客户端设置 SSE 头 |

### 现状与代码证据

**dispatchBucketSubresource 缺失的 5 个子资源：**

```go
// internal/api/s3compat/handler.go:280-310
func (h *Handler) dispatchBucketSubresource(w http.ResponseWriter, r *http.Request, bucket string, q url.Values) bool {
    switch {
    case q.Has("versioning"):    // ✅
    case q.Has("lifecycle"):     // ✅
    case q.Has("object-lock"):   // ✅
    case q.Has("acl"):           // ✅
    case q.Has("location"):      // ✅
    case q.Has("versions"):      // ✅
    case q.Has("policy"):        // ✅
    case q.Has("logging"):       // ✅（完整 CRUD 但 WriteAccessLog 零调用 — 见 v99/v100）
    case q.Has("notification"):  // ✅（完整 CRUD 但零运行时路由 — 见 v99/v100）
    case q.Has("accelerate"):    // ⚠️ 存根（返回硬编码 "Suspended"）
    // ❌ case q.Has("tagging"):       — 不存在
    // ❌ case q.Has("encryption"):    — 不存在
    // ❌ case q.Has("website"):       — 不存在
    // ❌ case q.Has("inventory"):     — 不存在
    // ❌ case q.Has("requestPayment"):— 不存在
    }
    return false
}
```

**现有子资源的 XML 编解码器：**

```bash
$ grep -n "xml.Name.*Configuration" internal/api/s3compat/xml.go | head -10
56:type copyResult struct { XMLName xml.Name `xml:"CopyObjectResult"` ... }
# 无 tagging/encryption/website/inventory/requestPayment 的 XML 类型
```

**`getBucketAccelerate` 存根代码：**

```go
// internal/api/s3compat/handler.go:835-845
func (h *Handler) getBucketAccelerate(w http.ResponseWriter, r *http.Request, bucket string) {
    _ = bucket  // 完全忽略桶参数！
    writeXML(w, http.StatusOK, accelerateConfig{
        XMLNs: s3Namespace, Status: "Suspended",  // 永远返回 Suspended
    })
}
```

**对象级标签存在但桶级标签缺失：**

```go
// internal/api/s3compat/extra.go — 对象级 ?tagging 已实现
func (h *Handler) getObjectTagging(w, r, bucket, key)  // ✅
func (h *Handler) putObjectTagging(w, r, bucket, key)  // ✅
func (h *Handler) deleteObjectTagging(w, r, bucket, key) // ✅
// 但桶级 ?tagging 不存在
```

### 架构方案概要

按实现复杂度排序的 5 个缺失子资源：

| 子资源 | 实现复杂度 | 依赖 | 复用 |
|--------|-----------|------|------|
| **`?tagging`**（桶标签） | 低（~80 行） | `BucketConfig` 已有 JSON 字段（可复用 `notification_rules` 模式） | 复用 `repository.UpdateTags`（对象标签方法可扩展为支持桶标签） |
| **`?requestPayment`**（请求者付费） | 低（~60 行） | 仅需 `BucketConfig` 新增 `RequesterPays bool` + GET/PUT handler | 复用现有 `GetBucketConfig`/`SetBucketConfig` |
| **`?encryption`**（默认加密） | 中（~120 行） | `BucketConfig.DefaultEncryption`（algorithm + key ID）+ GET/PUT/DELETE handler | 复用现有 SSE 基础设施 |
| **`?website`**（静态网站） | 中（~150 行） | `BucketConfig.WebsiteConfig`（index/error document, routing rules）+ GET/PUT/DELETE + 路由规则评估 | 新建 `WebsiteHandler`（path → key 映射） |
| **`?inventory`**（清单报告） | 高（~300 行） | schema + 定时 job + `?inventory` XML handler（v16 已有初步分析） | 复用 reconcile + jobs 基础设施 |

**加速器（`?accelerate`）修正：**

当前 `getBucketAccelerate` 返回硬编码 `"Suspended"`，应改为：
- 默认返回 `"Suspended"`（功能未启用——标准 S3 行为）
- 当 `S3_ACCELERATE_ENABLED=true` 时返回 `"Enabled"` + `accelerate_endpoint`（CDN/CloudFront 前缀）
- Add `PUT /{bucket}?accelerate` handler（切换加速状态）

### 边界情况与风险

| 场景 | 处理 |
|------|------|
| AWS SDK 调用 `get_bucket_tagging()` 未设置标签 | 返回 `NoSuchTagSet` 错误（与 S3 一致），而非空 200 |
| `?website` 启用后与 REST 路由冲突 | Website 有独立 endpoint（`<bucket>.s3-website-<region>.amazonaws.com`），非路径冲突；可配置为 `<prefix>/<bucket>/` 下的特殊路由 |
| 清单生成频率 | 支持 `Daily`/`Weekly` 频率（S3 标准）；输出为 CSV/ORC/Parquet 到指定目标桶 |
| `?encryption` 与现有 SSE 配置冲突 | 桶级默认加密应用于所有新 PUT；当 PUT 请求也携带 `x-amz-server-side-encryption` 头时，请求级覆盖桶默认（S3 行为） |

---

## 方向五：异步操作模式与长任务 API

### 产品价值

| 维度 | 影响 |
|------|------|
| **Glacier 冷存储恢复** | 冷存储恢复需要数小时到数天。同步等待不可能。需要 `POST ?restore` → `202 Accepted` + `Location: /v1/jobs/{id}` + `GET /v1/jobs/{id}` 轮询 |
| **大文件跨后端 Copy/Move** | 当源与目标在不同存储后端（local→S3 或 S3→Glacier）时，Copy 是长时间操作。需要异步跟踪进度 |
| **批量操作结果校验** | `POST /v1/batch/delete` 当前同步返回完整的每 key 结果。对于 1000+ 对象的批量操作，同步保持 HTTP 连接等待不可扩展 |
| **生命周期转换通知** | 当对象从 STANDARD→GLACIER 转换完成时，用户需要一个完成通知机制（Webhook 或可轮询状态）|
| **SDK 一致性** | 所有现代云 SDK（AWS、GCP、Azure）都支持异步操作模式——`202 + Location` + 轮询是标准模式 |

### 现状与代码证据

**所有端点均为同步阻塞：**

```go
// internal/api/rest/router.go — 所有路由直接返回最终响应
r.Post("/batch/delete", h.BatchDelete)  // 同步，保持连接直到全部完成
r.Post("/multipart/{uploadID}/complete", h.CompleteMultipart) // 同步合并大文件
```

**`?restore` 存根实现：**

```go
// internal/api/s3compat/handler.go:restoreObject
func (h *Handler) restoreObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
    if err := h.svc.RestoreObject(r.Context(), tenant, bucket, key); err != nil {
        writeS3Error(w, r, err)
        return
    }
    w.WriteHeader(http.StatusAccepted)  // ✅ 202
    // ❌ 无 Location header → 客户端无法轮询状态
    fmt.Fprintf(w, `<RestoreObjectResult>...`)
}
```

**Job 基础设施存在但无租户面 API：**

```go
// internal/jobs/jobs.go — 完整的作业队列
type Queue struct { /* ... */ }
type Pool struct { /* ... */ }
type Job struct {
    ID     int64
    Status string // "pending" | "running" | "completed" | "failed"
    Result string
    // ...
}
```

```go
// internal/api/rest/admin_jobs.go — 管理面存在
func (h *AdminHandler) ListJobs(w, r)  // admin scope
func (h *AdminHandler) RetryJob(w, r)  // admin scope
// ❌ 无普通租户可调用的作业状态查询 API
```

**无 `GET /v1/jobs/{id}` 端点：**

```bash
$ grep -rn "\"GET.*jobs\"" internal/api/rest/ --include='*.go'
internal/api/rest/router.go:	r.Get("/admin/jobs", adm.ListJobs)  // 只有 admin
# 无租户面 /v1/jobs/{id}
```

### 架构方案概要

**核心抽象：异步操作创建模式**

```go
// 统一的异步操作创建函数
func writeAccepted(w http.ResponseWriter, jobID int64, apiPrefix string) {
    w.Header().Set("Location", fmt.Sprintf("%s/jobs/%d", apiPrefix, jobID))
    w.Header().Set("Retry-After", "5") // 建议轮询间隔（秒）
    w.WriteHeader(http.StatusAccepted)
    json.NewEncoder(w).Encode(map[string]any{
        "job_id":  jobID,
        "status":  "pending",
        "message": "operation accepted, see Location header for status",
    })
}
```

**新增端点（租户面，scope: read）：**

| 端点 | 方法 | 功能 |
|------|------|------|
| `/v1/jobs/{id}` | `GET` | 查询异步作业状态和结果 |
| `/v1/jobs` | `GET` | 列出当前租户的异步作业历史 |

**异步化的操作：**

| 操作 | 当前行为 | 异步化后 |
|------|---------|---------|
| `?restore`（Glacier 恢复） | 202 无 Location | 202 + `Location: /v1/jobs/{id}` |
| `POST /v1/batch/delete`（1000+ 对象） | 同步持连接 | 202 + 返回 job；DELETE 在后台完成 |
| `POST /v1/batch/upload` | 不存在 | 202 + 每个文件单独状态 |
| 生命周期转换 STANDARD→GLACIER | 同步阻塞（不存在） | 202 + job 跟踪 |
| `POST /v1/admin/crypto/rewrap` | 不存在 | 202 + 重包装 job |

**状态查询响应：**

```jsonc
{
  "id": 42,
  "type": "restore",
  "status": "running",           // pending | running | completed | failed
  "progress": {                  // 类型相关的进度信息
    "objects_scanned": 150,
    "objects_rewrapped": 120,
    "errors": 0
  },
  "result": {                    // status=completed 时有
    "message": "Restore complete",
    "target_bucket": "default",
    "target_key": "archive-2026.tar.gz",
    "expiry": "2026-08-01T00:00:00Z"
  },
  "error": null,                 // status=failed 时有
  "created_at": "2026-07-11T10:00:00Z",
  "completed_at": null
}
```

**Webhook 完成通知：** 异步作业完成时，若该作业创建时带了 `X-Complete-URL` 头或桶通知规则匹配，则 POST 完成事件到该 URL（复用 webhook retry 机制）。

### 边界情况与风险

| 场景 | 处理 |
|------|------|
| 客户端在作业完成前断开 | 作业继续在后台运行；客户端重连后可查询 `/v1/jobs/{id}` 获取当前状态 |
| 作业永远不会完成（deadlock） | `ReapStuckJobs` 机制（已有）标记超时作业为 `failed` |
| 客户端轮询间隔过短 | 规范化轮询：`Retry-After: 5` + 可选客户端背压（429 当轮询频率 > 1 次/秒） |
| 作业结果在客户端轮询前已过期 | 配置 `JOBS_RESULT_RETENTION_HOURS`；过期作业返回 `404` + 提示"结果已过期" |
| 作业 ID 枚举攻击（跨租户） | 所有 `GET /v1/jobs/{id}` 校验租户匹配；非同一租户返回 `404` |
| 大批量 Batch Delete 的进度粒度 | 每 100 个对象报告一次进度（写入 jobs.result）+ 最终汇总 |

---

## 跨方向关联与实施建议

### 关联矩阵

| 方向 | 依赖 | 被依赖 | 共享基础设施 |
|------|------|--------|-------------|
| **TLS/HTTPS（#1）** | 无 | WebSocket（#2）应运行在 WSS 上；所有其他方向 | `middleware.SecurityHeaders` 可复用 |
| **WebSocket（#2）** | 复用 `events.Bus`（已实现）；需要 auth middleware | MCP 可作为 WebSocket 传输 | 共享 tenant/auth 上下文 |
| **密钥管理 API（#3）** | 复用 `storage/secret.go` 接口；需新增 migration | 无 | 复用 `jobs.Pool` 执行重包装；复用 `admin` scope 授权 |
| **S3 子资源完备性（#4）** | 复用 `repository.BucketConfig`（扩展字段） | 无 | 复用 `s3compat/xml.go` 编解码器 |
| **异步操作模式（#5）** | 复用 `internal/jobs` 全部基础设施 | 生命期转换（ROADMAP #9）需要此模式导出；密钥重包装（#3）需要 | `GET /v1/jobs/{id}` 是核心抽象 |

### 建议实施顺序

**第一批（P0 — 安全/生产）：**
1. **TLS/HTTPS（#1）** — 最少的代码改动量（配置层 ~30 行 + 服务器启动 ~10 行 + 安全头中间件 ~40 行），最高的安全影响。无任何数据面代码改动，纯基础设施加固。

**第二批（P1 — 协议/运维）：**
2. **异步操作模式（#5）** — 先实现 `GET /v1/jobs/{id}` 租户面端点（~100 行），然后将 `?restore` 从同步改为异步（~50 行）。
3. **S3 子资源完备性（#4）** — 按低→高复杂度逐个实现：`?tagging` → `?requestPayment` → `?encryption` → `?accelerate` 存根修复 → `?website`。

**第三批（P1 — 平台/安全）：**
4. **密钥管理 API（#3）** — 需要迁移 + 新端点 + SecretProvider 接口扩展。独立于数据面实现，可分批上线。
5. **WebSocket（#2）** — 最大的代码变动（新 package + 路由 + 前端集成 + SDK 扩展）。可在 WebUI 上作为渐进增强引入。

---

## 附录：快速验证列表

### 方向一（TLS/HTTPS）
- [ ] `grep -rn "ListenAndServeTLS\|TLSConfig\|TLSCert" internal/ cmd/` → 确认零输出
- [ ] `grep -rn "Content-Security-Policy\|HSTS\|X-Frame-Options" middleware/` → 确认零输出
- [ ] `curl -k https://localhost:8080/healthz` → 确认连接错误而非正常响应
- [ ] 检查 `internal/config/config.go:AppConfig` 是否含 TLS 字段 → 确认无 TLS 配置

### 方向二（WebSocket）
- [ ] `grep -rn "WebSocket\|Upgrade.*websocket\|Sec-WebSocket" internal/` → 确认零输出
- [ ] `grep -rn "new WebSocket\|new EventSource\|ws:\/\/" webui/` → 确认仅 EventSource，无 WebSocket
- [ ] 检查 `go.mod` 中 `gorilla/websocket` 或 `nhooyr.io/websocket` → 确认无 WebSocket 依赖

### 方向三（密钥管理 API）
- [ ] `grep -rn "/v1/admin/crypto\|/v1/keys\|key.*rotate" internal/api/rest/router.go` → 确认零路由
- [ ] 检查 `internal/storage/secret.go:SecretProvider` 接口 → 确认无 `Rotate`/`ListVersions`/`Health` 方法
- [ ] 检查 `internal/repository/repository.go` → 确认无加密密钥记录相关接口方法

### 方向四（S3 子资源完备性）
- [ ] `aws s3api get-bucket-tagging --bucket test --endpoint-url http://localhost:8080/s3` → 确认 `NoSuchTagSet` 或 `404`（期望 `NoSuchTagSet` 但实际可能不同）
- [ ] `aws s3api get-bucket-encryption --bucket test --endpoint-url http://localhost:8080/s3` → 确认 `501` 或 `404`
- [ ] `aws s3api get-bucket-accelerate --bucket test --endpoint-url http://localhost:8080/s3` → 确认返回 `Suspended`（存根）
- [ ] 检查 `dispatchBucketSubresource` switch-case → 确认漏缺 5 个子资源

### 方向五（异步操作模式）
- [ ] `POST /s3/{bucket}/{key}?restore` → 确认响应头中无 `Location` header
- [ ] `grep -rn "\"GET.*jobs\|tenants.*jobs" internal/api/rest/` → 确认无租户面 jobs 端点
- [ ] `POST /v1/batch/delete` 携带 500+ key → 确认响应时间随 key 数线性增长（而非立即返回 202）
