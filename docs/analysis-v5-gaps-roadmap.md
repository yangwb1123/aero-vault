# 🏗️ AeroVault 深度评估 v5 — 存储实现质量、标准合规、开发者入门

> **日期:** 2026-06-30  
> **方法:** 全局代码扫描（237 文件 / ~45K 行），第五轮  
> **视角:** 存储后端质量审计 + 协议合规性 + 安全性 + 文档与开发者体验

---

## 0. 本轮焦点：从"架构设计"到"实现质量"

前四轮覆盖了功能差距（v1）、韧性（v2）、协议经济性（v3）、内部架构债（v4）。  
本轮转向**已实现的代码在真实环境中的表现质量**——4 个存储后端的健壮性、协议规范的遵循程度、安全纵深防御、以及进入门槛（文档、示例、可部署性）。

---

## 1. 存储后端实现质量审计

### 1.1 横向对比：4 个存储后端的实现成熟度

| 维度 | local (`local_*.go`) | s3 (`s3.go`) | oss (`oss.go`) | cos (`cos.go`) |
|--------|:--------------------:|:------------:|:--------------:|:--------------:|
| **代码行数** | ~500 | ~294 | ~218 | ~226 |
| **依赖 SDK** | 无 | `aws-sdk-go-v2` | `aliyun-oss-go-sdk` | `cos-go-sdk-v5` |
| **错误处理** | 模式化 `os.IsNotExist` | 裸 SDK 错误 | 裸 SDK 错误 | 裸 SDK 错误 |
| **超时控制** | 无（本地 I/O） | HTTP 客户端超时可配置 | HTTP 客户端超时可配置 | HTTP 客户端超时可配置 |
| **重试** | 无 | 无 | 无 | 无 |
| **预签名 URL** | HMAC 签名 | SDK `PresignClient` | SDK `SignURL` | SDK `GetObjectURL` |
| **Content-Type 处理** | 传递 | 传递 | 传递 | 传递 |
| **User-Metadata 处理** | JSON 行列存储 | AWS `Metadata` 映射 | OSS `Meta()` 选项 | COS `x-cos-meta-*` 头 |
| **SSE** | AES-256-GCM 信封 | ❌ 未实现 | ❌ 未实现 | ❌ 未实现 |
| **Multipart** | 文件系统分片 | SDK `UploadPart` | SDK `UploadPart` | SDK `UploadPart` |
| **List** | 递归目录读取 | SDK `ListObjectsV2` | SDK `ListObjects` | SDK `ListObjects` |
| **一致性模型** | 无分布式问题 | 最终一致性 | 最终一致性 | 最终一致性 |
| **大文件流式** | 临时文件 + 重命名 | SDK 流式 | 可能全部缓冲 | SDK 流式 |

**发现 1：SSE 加密仅 local 后端支持。** S3、OSS、COS 即使配置了 `STORAGE_SSE_KEY`，也不会进行加密。`encrypt.go` 中的 `envelopeEncrypter` 和 `rewrap.go` 均只与 `LocalStorage` 集成。对于云后端，用户需要依赖 Provider 的内置 SSE（S3 SSE-S3/SSE-KMS、OSS 服务端加密等），但 AeroVault 没有传递方式。

**发现 2：云后端的错误处理过于原始。** S3、OSS、COS 的后端方法直接将 SDK 返回的错误返回给 `FileService`。这些错误包含 SDK 类型信息和内部细节（`RequestError`、`NoSuchKey`、`oss.ServiceError`），泄露给用户。`classify(err)` 中缺少对这些 SDK 错误类型的映射。

**发现 3：云后端缺少一致性感知。** S3/OSS/COS 写入后立即读取（`GetObject`）可能找不到刚写入的对象（最终一致性）。`FileService` 的 `Put` 写入后立即 `emit` 事件，此时 storage 尚未保证全局可见。

### 1.2 存储后端错误处理对比

| 场景 | local | s3 | oss | cos |
|--------|-------|-----|-----|------|
| **对象不存在** | `os.ErrNotExist` → `storage.ErrNotFound` | `*s3types.NoSuchKey` → 原始 | `oss.NotFound` → 原始 | `*cos.ErrorResponse` → 原始 |
| **访问被拒** | 文件系统权限错误 | `*s3types.AccessDenied` | `oss.ServiceError` (403) | 无特殊处理 |
| **网络超时** | N/A | HTTP 超时错误 | HTTP 超时错误 | HTTP 超时错误 |
| **限流** | N/A | `*s3types.InsufficientCapacity` | `oss.ServiceError` (503) | 无特殊处理 |
| **Bucket 不存在** | N/A | SDK 创建 bucket？❌ | SDK 创建 bucket？❌ | SDK 创建 bucket？❌ |

**代码引用:** `storage/s3.go`（所有方法直接返回 SDK 错误）；`storage/oss.go`（同理）；`storage/cos.go`（同理）；`storage/local_read.go:Get`（`os.ErrNotExist` → `ErrNotFound`）

### 1.3 Pre-signed URL 实现对比

| 维度 | local | s3 | oss | cos |
|--------|-------|-----|------|------|
| **机制** | HMAC-SHA256 签名路径+到期 | SDK `PresignClient` | SDK `SignURL` | SDK `GetObjectURL` |
| **过期时间** | 服务端验证 | 委托 S3 | 委托 OSS | 委托 COS |
| **IP 绑定** | ❌ 无 | ❌ 无 | ❌ 无 | ❌ 无 |
| **撤销** | ❌ 无 | ❌ 无 | ❌ 无 | ❌ 无 |
| **审计** | ❌ 无 | ❌ 无 | ❌ 无 | ❌ 无 |
| **多操作(GET+PUT)** | 仅单一操作 | 仅单一操作 | 仅单一操作 | 仅单一操作 |

**发现：** 预签名 URL 的实现存在统一的安全薄弱点：**无绑定、无撤销、无审计**。一旦签名泄漏，攻击者可任意访问。建议至少添加 `X-Forwarded-For` IP 记录和可选的 IP 约束。

---

## 2. 协议合规性与标准遵循

### 2.1 S3 兼容性矩阵（剩余缺口）

根据 `ROADMAP.md` 和代码扫描，以下 S3 API 仍缺失：

| S3 API 端点 | 状态 | 工作量评估 | 要求频率 |
|-------------|:----:|:----------:|:--------:|
| `GET/PUT ?policy` | ❌ 缺失 | ~100 行 | ⭐⭐⭐⭐⭐ |
| `GET/PUT ?notification` | ❌ 缺失 | ~150 行 | ⭐⭐⭐⭐ |
| `GET/PUT ?logging` | ❌ 缺失 | ~200 行 | ⭐⭐⭐⭐ |
| `GET/PUT ?cors` (per-bucket) | ❌ 缺失 | ~100 行 | ⭐⭐⭐ |
| `POST ?select` (S3 Select) | ❌ 缺失 | ~300 行 | ⭐⭐ |
| `POST ?restore` (Glacier restore) | ❌ 缺失 | ~150 行 | ⭐⭐ |
| `PUT ?accelerate` (Transfer Acceleration) | ❌ 缺失 | ~80 行 | ⭐ |
| `GET/PUT ?website` (Static hosting) | ❌ 缺失 | ~200 行 | ⭐ |
| `GET/PUT ?object-lock` (bucket-level) | ❌ 缺失 | ~80 行 | ⭐⭐⭐ |
| `GET/PUT ?tagging` (bucket-level) | ❌ 缺失 | ~50 行 | ⭐⭐⭐ |
| `ListObjectsV2` `?tag-key` | ❌ 缺失 | ~50 行 | ⭐⭐⭐ |
| `ListObjectVersions` (S3 兼容响应) | ⚠️ 有 REST API，S3 XML 响应？ | 检查中 | ⭐⭐⭐ |

**严重程度：** `?policy` 是最严重的缺口——它是 AWS IAM 集成的核心端点。没有它，使用 S3 IAM 策略工具链的用户无法迁移。

### 2.2 WebDAV 合规性

| 维度 | 状态 | 发现 |
|--------|:----:|--------|
| **PROPFIND** | ✅ 实现 | 虚拟目录折叠 + 分页支持 |
| **MKCOL** | ❌ 空操作 | `Mkdir` 返回 nil——不实际创建目录 |
| **GET** | ✅ 实现 | Content-Type 预置 + spillBuffer 流式 |
| **PUT** | ✅ 实现 | 通过 spillBuffer 上传 |
| **DELETE** | ✅ 实现 | 调用 `svc.Delete` hard=true |
| **MOVE (Rename)** | ✅ 实现 | copy-then-delete，带回滚 |
| **LOCK / UNLOCK** | ✅ 内置 | `xwebdav.NewMemLS()` 内存锁系统 |
| **PROPPATCH** | ❌ 未测试 | 默认 `xwebdav.Handler` 处理但未验证 |
| **COPY** | ❌ 未实现 | `xwebdav.Handler` 不处理跨文件系统复制——需自定义 |
| **版本控制** | ❌ | 无 WebDAV 版本控制扩展（DeltaV） |
| **ACL** | ❌ | 无 WebDAV ACL（RFC 3744） |

**发现：** WebDAV 的 `MKCOL` 当前是空操作——不创建空目录标记。AeroVault 的 "目录" 完全是虚拟的（基于前缀扫描），这意味着 WebDAV 客户端创建目录时没有报错，但目录不会持久化。

### 2.3 MCP 协议完整性

| 维度 | 状态 | 发现 |
|--------|:----:|--------|
| **Protocol Version** | `2025-03-26` | ✅ 采用最新版 |
| **`initialize`** | ✅ 正确 | 报告名称+版本+能力 |
| **`tools/list`** | ✅ 动态 | 搜索/聊天启用时追加 |
| **`tools/call`** | ✅ 分派 | 6 个工具 |
| **`resources/list`** | ✅ 列出对象 | 限制 200 条 |
| **`resources/read`** | ✅ 读取对象 | 限制 4MB |
| **`ping`** | ✅ 实现 | |
| **`notifications/**`** | ❌ 未实现 | 无 `notifications/initialized` |
| **`resources/subscribe`** | ❌ 声明不支持 | 能力中 `subscribe: false` |
| **`tools/listChanged`** | ❌ 声明不支持 | 能力中 `listChanged: false` |
| **日志记录** | ❌ 未实现 | 无 `logging/setLevel` |

---

## 3. 安全纵深：威胁建模视角

### 3.1 攻击面分析

| 攻击面 | 暴露面 | 风险评级 | 现有防护 |
|-------------|-----------|:-------:|----------------|
| **预签名 URL 泄露** | 日志、网络嗅探、客户端泄露 | 🔴 **高** | 无（无 IP 绑定、无 Single-Use、无审计） |
| **API Key 暴力破解** | `/v1/*` 认证端点 | 🔴 **高** | 无失败计数、无速率限制锁定 |
| **SSRF via Webhook** | `EVENTS_WEBHOOK_URL` → 内部网络 POST | 🔴 **高** | 无 URL 域名/IP 验证 |
| **路径遍历** | `PUT /v1/files/../../etc/passwd` | 🟡 **中** | `validateKey` 拒绝 `..`，但存储键用 `path.Join` |
| **SSRF via Remote Extractor** | `AI_EXTRACTOR_ENDPOINT` → 任意 HTTP POST | 🟡 **中** | 无 URL 验证 |
| **SSRF via Embedder/LLM** | `AI_EMBED_ENDPOINT` / `AI_CHAT_ENDPOINT` | 🟡 **中** | 无 URL 验证 |
| **无 TLS** | `http.ListenAndServe` — 明文传输 | 🟡 **中** | 部署文档建议反向代理终止 TLS |
| **Session 无状态（JWT）** | JWT 签发后无法撤销（无黑名单） | 🟡 **中** | `AUTH_JWT_SECRET` 签发后无法吊销 |
| **SSE 数据泄露** | 认证后 SSE 连接 → 任意租户事件 | 🟡 **中** | 已在 `liveStream` 中按 `e.TenantID != tenant` 过滤 |
| **Env 泄露** | 80+ 配置项包含明文密钥 | 🟡 **中** | Helm chart 支持 Secret |

### 3.2 纵深防御缺口

| 防御层 | 当前状态 | 改进方向 |
|----------|-------------|----------------|
| **传输安全** | 无内置 TLS | 支持 `ListenAndServeTLS` 或自动 Let's Encrypt |
| **输入验证** | `validateKey` 拒绝 `..` 和 `/` 前缀 | 扩展文件名编码（`%2e%2e`）、Unicode normalize |
| **认证** | Bearer token / API Key / JWT / SigV4 | JWT 黑名单、Key 过期自动吊销、IP 锁定 |
| **授权** | Scopes (`read/write/admin`) + ACL | 细粒度 Resource-based 策略（现有 Policy JSON 但仅 S3） |
| **速率限制** | 全局 token-bucket + AI 独立 | 按端点细化、按租户独立池 |
| **请求验证** | 无请求体大小限制 | `Content-Length` 硬上限（当前依赖 Go 默认） |
| **错误输出** | 错误消息含内部细节 | 生产环境剥离实现细节错误 |
| **审计** | Admin 操作已审计 | 扩展到所有对象写操作（S3/WebDAV） |

### 3.3 已知 CVE 模式检查

| 模式 | 位置 | 风险评估 |
|---------|--------|:-------:|
| **Path traversal** | `storage/local.go:objectPath` 使用 `filepath.Clean` + `filepath.Rel` + 前缀检查 | ✅ 正确处理 |
| **Path traversal** | `service/file.go:validateKey` 拒绝 `..` 和 `/` 前缀 | ✅ 正确处理 |
| **Log injection** | `middleware/middleware.go:AccessLog` 直接将请求路径传入日志 | ⚠️ 建议转义换行符 |
| **SQL Injection** | `sql.go` 使用参数化查询（`$N`/`?`） | ✅ 正确处理 |
| **XXE (XML External Entity)** | `s3compat/xml.go` 没有明确禁用 XML 外部实体 | ⚠️ `encoding/xml` 默认不解压 XXE——安全 |
| **SSRF (Server-Side Request Forgery)** | `events/webhook.go` 无 URL 验证 | 🔴 应验证 URL Scheme + Host 黑名单 |

---

## 4. 开发者体验与文档质量

### 4.1 文档覆盖审计

| 文档 | 路径 | 评估 |
|-------------|------|--------|
| **README.md** | `/README.md` | ✅ 全面——功能矩阵 + 快速开始 + 配置表 |
| **Architecture** | `/docs/architecture.md` | ✅ 包含架构图 + 分层说明 |
| **Configuration** | `/docs/configuration.md` | ✅ 80+ 配置项逐条解释 |
| **API Reference** | `/docs/api.md` | ⚠️ 内容全面但冗长（506 行），缺少快速查找索引 |
| **Deployment** | `/docs/deployment.md` | ✅ Docker + Helm + docker-compose |
| **OpenAPI Spec** | `internal/api/rest/openapi.json` | ✅ 完整 JSON，可在 Swagger UI 渲染 |
| **ROADMAP** | `/docs/ROADMAP.md` | ✅ 10 个方向——非常详细 |
| **CHANGELOG** | `/docs/CHANGELOG.md` | ✅ 存在 |
| **SDK README** | `sdk/go/README.md` | ✅ Go SDK 文档 |
| **Python SDK** | `sdk/python/README.md` | ❌ 仅 README——SDK 代码未审计 |
| **JS SDK** | `sdk/js/README.md` | ❌ 同上 |
| **Swagger UI** | `GET /docs` | ✅ 内嵌 Swagger UI 服务 |
| **BR / 运维文档** | 不明确存在 | ❌ 备份恢复、监控设置、扩容指南缺失 |
| **安全文档** | 不明确存在 | ❌ 安全配置、威胁模型、审计日志解释缺失 |
| **贡献指南** | 不明确存在 | ❌ 如何贡献、本地开发设置、测试指南 |

### 4.2 入门体验

| 步骤 | 当前用户体验 | 评估 |
|--------|----------------|--------|
| **1. 克隆** | `git clone` | ✅ 无问题 |
| **2. 构建** | `go build ./cmd/server` | ✅ Go 1.25，25+ 依赖，`go mod tidy` |
| **3. 配置** | 复制 `.env.example`，编辑 | ✅ 但 80+ 变量令人望而生畏 |
| **4. 运行** | `./aero-vault` | ✅ 无配置即可启动 |
| **5. 创建数据** | `curl -X PUT ...` 或 CLI | ✅ CLI `upload` 命令 |
| **6. 搜索** | 需要配置 `AI_INDEX_ENABLED=true` | ⚠️ 需要 embedding provider 或默认 `hash` |
| **7. AI Chat** | 需要 `AI_CHAT_PROVIDER=http` + endpoint | ⚠️ 开箱无法体验 RAG |
| **8. 第一个集成** | Go SDK / Python / JS | ⚠️ JS/Python SDK 缺少完整 API 覆盖 |

**发现：** 新手从 git clone 到完成首次 RAG 搜索的步骤较多（6-7 步），主要障碍是 AI 配置。建议增加 `docker-compose.demo.yml`，一键启动 AeroVault + Ollama（嵌入+LLM）+ Postgres + Qdrant 的完整 AI 体验。

### 4.3 `openapi.json` 与实际 API 的一致性审计

| 端点 | OpenAPI 中声明 | 实际实现 | 差异 |
|----------|:--------------:|:--------:|:----:|
| `GET /v1/files/{key}` | ✅ 文档化 | `GET /v1/files/*` | 路径格式不同（通配符 vs 路径参数） |
| `PUT /v1/files/{key}` | ✅ 文档化 | `PUT /v1/files/*` | 同上 |
| `DELETE /v1/files/{key}` | ✅ 文档化 | `DELETE /v1/files/*` | 同上 |
| `POST /v1/chat` | ✅ 文档化 | ✅ | — |
| `POST /v1/chat/stream` | ✅ 文档化 | ✅ | — |
| `POST /v1/agent` | ✅ 文档化 | ✅ | — |
| `GET /v1/lineage/objects/{id}` | ✅ 文档化 | ✅ | — |
| `GET /v1/events/stream` | ✅ 文档化 | ✅ | — |
| `PUT /v1/admin/tenants/{tenant}/budget` | ✅ 文档化 | ✅ | — |
| `GET /v1/buckets/{bucket}/logging` | ❌ 未文档化 | ✅ 实现 | 文档缺口 |
| `GET /v1/buckets/{bucket}/notifications` | ❌ 未文档化 | ✅ 实现 | 文档缺口 |
| `DELETE /v1/buckets/{bucket}/cors` | ❌ 未文档化 | ✅ 实现 | 文档缺口 |

**发现：** OpenAPI 规范在桶管理端点上（`logging`、`notifications`、`cors` 删除）存在缺口。这些端点已实现但未在 OpenAPI 中声明。

---

## 5. 网络与传输层

### 5.1 HTTP 服务器配置

| 参数 | 当前值 | 评估 |
|----------|-------------|--------|
| **Addr** | `:8080`（默认） | ✅ 可配置 |
| **ReadHeaderTimeout** | 15s | ✅ 防止 Slowloris |
| **WriteTimeout** | `APP_WRITE_TIMEOUT`（默认 60s） | ⚠️ 大文件上传/下载可能超时 |
| **IdleTimeout** | `APP_IDLE_TIMEOUT`（默认 120s） | ✅ 合理 |
| **TLS** | 无 | ❌ 需反向代理 |
| **H2C** | 无 | ❌ HTTP/2 不支持 |
| **MaxHeaderBytes** | 默认 1MB | ⚠️ Go 默认值可被大 Header 攻击 |
| **请求体大小限制** | 无 | ❌ 无硬限制——依赖 storage 后端 |
| **WebSocket** | 无 | ❌ 不支持 Upgrade |
| **Unix Socket** | 无 | ❌ 生产部署需要 |

**代码引用:** `cmd/server/main.go:runServer`（`ReadHeaderTimeout: 15s`, `WriteTimeout`, `IdleTimeout` 从配置读取）

### 5.2 gRPC 与流式协议

| 协议 | 支持 | 建议 |
|--------|:---:|---------|
| **gRPC** | ❌ | 高性能内部服务间通信的首选 |
| **HTTP/2 streaming** | ⚠️ SSE 使用 | REST API 支持 SSE 但非原生流式传输 |
| **WebSocket** | ❌ | 实时双向通信的缺失协议 |
| **Unix Domain Socket** | ❌ | 本地高性能通信 |

---

## 6. 🚀 5 个高价值扩展方向（新维度）

---

### 🥇 方向 1：Storage Backend Hardening — 统一错误映射 + SSE 跨后端 + 一致性感知重试

**为什么需要它：**

目前的 4 个存储后端在错误处理、加密支持、一致性处理上存在显著的不一致。本地后端有完整的错误映射（`os.ErrNotExist` → `storage.ErrNotFound`），但三个云后端都返回原始 SDK 错误。SSE 加密只对本地有效。最终一致性场景下的写入后读取无保护。

**架构蓝图：**

```
当前:
  local: os.ErrNotExist → ErrNotFound ✅   SSE ✅
  s3:    *s3types.NoSuchKey → 原始错误 ❌  SSE ❌
  oss:   oss.NotFound → 原始错误 ❌       SSE ❌
  cos:   *cos.ErrorResponse → 原始错误 ❌  SSE ❌

改进: StorageHardening (跨 storage/ 包重构)
├── UnifiedErrorMapper:
│   ├── local: 已有正确映射
│   ├── s3:   wrapSDKError(err) → s3types.NoSuchKey → ErrNotFound, s3types.AccessDenied → ErrForbidden
│   ├── oss:  wrapSDKError(err) → oss.NotFound → ErrNotFound, oss.ServiceError(403) → ErrForbidden
│   └── cos:  wrapSDKError(err) → cos.ErrorResponse(404) → ErrNotFound
├── Cross-Backend SSE:
│   ├── 定义 `SSESupport` 接口: Encrypt(ctx, key, plaintext) → (ciphertext, error)
│   ├── local: 当前 envelopeEncrypter (保持)
│   ├── s3:   委托 AWS SSE-S3 / SSE-KMS (x-amz-server-side-encryption 头)
│   ├── oss:   委托 OSS 服务器端加密 (x-oss-server-side-encryption 头)
│   └── cos:   委托 COS 服务器端加密 (x-cos-server-side-encryption 头)
├── ConsistencyAwareRetry:
│   ├── 配置: `STORAGE_CONSISTENCY_WAIT` (默认 0, 仅最终一致性场景使用)
│   ├── 写入后 Get/Stat 的可选等待+重试
│   └── 仅用于一致性关键路径（如版本创建后的立即读取）
└── StorageContractTest 扩展:
    ├── 将 `contract_test.go` 参数化 → 可针对每个后端运行
    └── 测试: 写入/读取/删除/列出/预签名/错误映射
```

**复用资产：** `storage/encrypt.go`（信封加密逻辑可被 SSE 接口引用）、`storage/storage.go`（`ErrNotFound` 等错误常量和 `ObjectInfo`）、`storage/contract_test.go`（测试框架可扩展）

**预计影响：**

| 场景 | 改进前 | 改进后 |
|--------|------------|--------------|
| S3 对象不存在返回 500 | SDK 原始错误 → `InternalError` | `NoSuchKey` → `ErrNotFound` → `404 NotFound` |
| 最终一致性写入后读 | 可能返回 404 | 可选自动重试（可配置关闭）|
| 云后端加密 | 不支持 | 遵循云原生 SSE（S3 SSE-S3/KMS）|
| 后端契约测试 | 仅 local | 4 个后端均测试，CI 可配置 |

| 复杂度 | 用户影响 | 代码复用 | 差异化 |
|----------|-------------|-------------|------------|
| 中 | 高（稳定性） | ~55% | ★★★★★ |

---

### 🥇 方向 2：S3 Compatibility Completeness — 补全前 5 个缺失端点

**为什么需要它：**

S3 兼容性是 AeroVault 最重要的集成表面。当前缺失的 `?policy`、`?logging`、`?notification` 是企业用户选型时的硬门槛。补全这些端点的工作量很小（每端点 50-200 行），但影响巨大——允许现有的 S3 工具链和 SDK 无缝迁移。

**架构蓝图：**

```
当前: api/s3compat/router.go 挂载 12 个端点 → 缺失 ~10 个标准 S3 端点

改进: S3Completeness 增量实现 (按优先级排序)

P0 — Bucket Policy (?policy) ~100 行:
├── 复用 rest/handler.go 的 GetBucketPolicy / PutBucketPolicy
├── api/s3compat/bucketconfig.go: handlePolicy() → GET / PUT ?policy
└── XML 响应: <Policy>...</Policy> (与 S3 一致)

P1 — Bucket Notifications (?notification) ~150 行:
├── 复用 rest/handler.go 的 GetBucketNotifications / SetBucketNotifications
├── api/s3compat/extra.go: handleNotification() → GET / PUT ?notification
└── XML 序列化: <NotificationConfiguration><TopicConfiguration>...</>

P2 — Server Access Logging (?logging) ~200 行:
├── 复用 rest/handler.go 的 GetBucketLogging / SetBucketLogging
├── api/s3compat/extra.go: handleLogging() → GET / PUT ?logging
└── XML 响应: <BucketLoggingStatus><LoggingEnabled>...</>

P3 — Bucket CORS (?cors) ~100 行:
├── 复用 rest/ 的 BucketCORS 处理器
├── api/s3compat/extra.go: handleCORS() → GET / PUT / DELETE ?cors
└── XML 序列化: <CORSConfiguration><CORSRule>...</>

P4 — Tag-based Listing (?tag-key on ListObjectsV2) ~50 行:
├── 扩展 ListObjectsV2 处理 tag-key/tag-value 参数
├── 调用 repo.ListObjectsByTag (已有)
└── 在 XML 响应中添加 tag-count
```

**复用资产：** `api/rest/handler.go`（所有桶管理端点均已实现）、`api/s3compat/xml.go`（XML 编解码器）、`api/s3compat/router.go`（路由注册模式）、`api/s3compat/bucketconfig.go`（现有桶配置处理）、`api/s3compat/extra.go`（扩展端点位置）

**预计影响：**

| 场景 | 改进前 | 改进后 |
|--------|------------|--------------|
| `aws s3api get-bucket-policy` | `403` 或 `NotImplemented` | 正常工作 |
| `aws s3api put-bucket-notification` | `403` 或错误 | 映射到 EventBus/Webhook |
| S3 日志审计 | 必须使用 REST API | 标准 S3 日志配置 |
| 从 S3 迁移 | 需要策略适配 | 直接使用现有工具链 |

| 复杂度 | 用户影响 | 代码复用 | 差异化 |
|----------|-------------|-------------|------------|
| 低 | 高（迁移兼容性） | ~80% | ★★★★★ |

---

### 🥇 方向 3：Transport Security & Network Hardening — TLS + H2C + 请求体限制 + SSRF 防护

**为什么需要它：**

AeroVault 默认运行在明文 HTTP 上。没有传输加密、没有 HTTP/2 支持、没有请求体大小限制、没有出站连接验证（Webhook/Extractor/LLM 端点无 SSRF 防护）。对于一个存储系统来说，这是从"可用"到"可部署到任何受监管环境"的最大障碍。

**架构蓝图：**

```
当前: http.ListenAndServe → 明文 HTTP / 无限制 / 无 SSRF 防护

改进: TransportSecurity (cmd/server/main.go + internal/transport)
├── TLS Termination:
│   ├── 可选: `SERVER_TLS_CERT` / `SERVER_TLS_KEY` 文件路径
│   ├── 可选: `SERVER_TLS_AUTO=true` → Let's Encrypt Auto (ACME)
│   ├── `http.ListenAndServe` → `http.ListenAndServeTLS`
│   └── 配置: `SERVER_TLS_MIN_VERSION` (默认 1.3)
├── HTTP/2 (H2C + H2):
│   ├── TLS 启用后自动升级到 HTTP/2
│   ├── 无 TLS 可选 H2C (`SERVER_H2C=true`——内部转发用)
│   └── SSE 通过 HTTP/2 多路复用改善连接管理
├── 请求体大小限制:
│   ├── `MAX_REQUEST_BODY_BYTES` (默认 0 = 无限制)
│   ├── 在 middleware 层增强: `http.MaxBytesReader`
│   ├── 大型 multipart 上传需要单独设置或排除
│   └── 返回 `413 Request Entity Too Large` + JSON/XML 错误
├── SSRF 防护:
│   ├── 出站 HTTP 客户端验证:
│   │   ├── 拒绝私有 IP 范围 (127.0.0.0/8, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16)
│   │   ├── 拒绝 URL 中内嵌的凭证
│   │   └── 资源: `ALLOWED_OUTBOUND_HOSTS` 白名单
│   ├── Webhook 调用: 验证 URL
│   ├── Extractor 调用: 验证 URL
│   └── LLM/Embedder 调用: 验证 URL (可选)
├── 安全响应头:
│   ├── `Strict-Transport-Security` (HSTS)
│   ├── `X-Content-Type-Options: nosniff`
│   ├── `X-Frame-Options: DENY`
│   └── `Referrer-Policy: no-referrer`
└── 速率限制响应头标准化:
    ├── `RateLimit-Limit`, `RateLimit-Remaining`, `RateLimit-Reset`
    └── `429` 始终包含 `Retry-After`
```

**复用资产：** `cmd/server/main.go:runServer`（现有 HTTP 服务器创建——可替换为 TLS）、`middleware/`（现有中间件可添加安全头）、`config/config_app.go`（可扩展 TLS/限制配置）、`storage/NewHTTPClient`（可扩展 SSRF 防护）

**预计影响：**

| 场景 | 改进前 | 改进后 |
|--------|------------|--------------|
| 传输加密 | 依赖反向代理 | 内置 TLS + 可选自动 Let's Encrypt |
| 请求体攻击 | 无上限 | `MAX_REQUEST_BODY_BYTES` 防护 |
| SSRF 攻击 | Webhook/Extractor 可达内网 | 拒绝私有 IP 范围 + 白名单 |
| 安全合规 | 不符合安全基线 | HSTS + 安全头 + TLS 1.3 |

| 复杂度 | 用户影响 | 代码复用 | 差异化 |
|----------|-------------|-------------|------------|
| 中 | 极高（安全基础） | ~40% | ★★★★★ |

---

### 🥇 方向 4：Developer Onboarding Experience — Demo Mode + Playground + Tutorial

**为什么需要它：**

AeroVault 是目前功能最丰富的开源存储+AI 平台之一，但入门体验存在摩擦：新手无法"开箱即搜索"。没有嵌入、LLM 和向量存储的 demo 配置，完整 RAG 体验需要 3 个外部服务。需要一条免配置、零依赖的路径，让新用户在 5 分钟内体验整个平台。

**架构蓝图：**

```
当前: .env.example → 配置编辑 → ./aero-vault → curl (仅基础 CRUD, AI 需外部服务)

改进: DeveloperExperience (跨包 + 文档)
├── Demo Mode (零配置 AI):
│   ├── `AERO_DEMO_MODE=true` (新环境变量)
│   │   ├── 内置嵌入: HashEmbedder + 内存暴力搜索 (现有——直接启用)
│   │   ├── 内置 LLM: MockLLM + 种子响应 (现有——直接启用)
│   │   ├── 种子数据: 启动时自动索引 `docs/demo-content/` 目录
│   │   │   ├── 包含 ~20 个示例文档（MD/PDF/TXT）
│   │   │   └── 标记为 demo 租户
│   │   ├── Web UI 自动打开: `http://localhost:8080/ui`
│   │   └── 消息: "🟢 AeroVault Demo Mode — search, chat, and explore!"
│   └── 禁用 demo 模式: AERO_DEMO_MODE=false (默认)
├── Interactive Playground (Web UI 增强):
│   ├── 新标签页: "API Explorer"
│   │   ├── 选择端点 + 填写参数 → 发送请求 → 显示响应
│   │   ├── 请求历史 + 收藏
│   │   └── 自动生成 `curl` / Python / JS 代码片段
│   ├── 新标签页: "Search Debugger"
│   │   ├── 输入查询 → 显示检索到的 chunk + 余弦分数 + 重排序前后
│   │   ├── 切换 mode: vector / bm25 / hybrid
│   │   └── 显示嵌入向量（截断）
│   └── 新标签页: "System Status"
│       ├── 存储后端状态 + 断路器状态
│       ├── DB 连接状态 + 迁移版本
│       └── AI 管线状态 (嵌入/LLM/重排序器 是否可用)
├── 交互式教程:
│   ├── Playground 中的引导向导
│   ├── "Step 1: Upload a file" → 自动执行 `POST /v1/files/hello.txt`
│   ├── "Step 2: Search" → `POST /v1/search {query: "hello"}`
│   ├── "Step 3: Chat" → `POST /v1/chat {query: "What did I upload?"}`
│   └── "Step 4: Try the Agent" → `POST /v1/agent {query: "..."}`
├── docker-compose.demo.yml (增强):
│   ├── 服务: aero-vault + Ollama + Qdrant + Postgres
│   ├── 环境: `AI_INDEX_ENABLED=true`, `AI_CHAT_PROVIDER=http`, `AI_VECTOR_BACKEND=qdrant`
│   └── 预置: 种子文档 + 自动索引
└── 快速启动模板:
    ├── GitHub Codespaces 一键启动
    ├── `make demo` → 构建+启动+种子数据
    └── 5 分钟视频教程链接（README 中）
```

**复用资产：** `webui/static/index.html`（SPA 框架——可扩展标签页）、`internal/ai/embedder.go`（`HashEmbedder` 已有）、`internal/ai/llm.go`（`MockLLM` 已有）、`docs/`（示例文档可提取为种子）、`deploy/docker-compose.demo.yml`（已有——可增强）

**预计影响：**

| 场景 | 改进前 | 改进后 |
|--------|------------|--------------|
| 首次搜索耗时 | ~20 分钟（配置嵌入+LLM+向量存储） | ~1 分钟（`AERO_DEMO_MODE=true`） |
| Web UI 功能 | 搜索+详情+血缘+聊天 | + API Explorer + 搜索调试器 + 系统状态 |
| 新用户转化 | 理解配置障碍后放弃 | 5 分钟完成首次 RAG 对话 |
| 社区贡献 | 需要搭建开发环境 | Codespaces 一键启动 + 引导教程 |

| 复杂度 | 用户影响 | 代码复用 | 差异化 |
|----------|-------------|-------------|------------|
| 中 | 极高（采用率） | ~60% | ★★★★★ |

---

### 🥇 方向 5：Data Portability & Standards Compliance — 规范化导出 + SDK 生成 + 互操作测试套件

**为什么需要它：**

当前与外界的数据交换仅限于 REST JSON 格式和 S3 兼容的 XML。缺乏标准化的数据导出格式、自动生成的 SDK（除了 Go）、协议互操作性测试。对于要求数据主权和供应商中立的客户来说，这是核心需求。

**架构蓝图：**

```
当前: REST JSON + S3 XML + WebDAV XML + MCP JSON-RPC — 4 种协议无互操作测试

改进: DataPortability (新包 internal/portability)
├── 标准化导出格式:
│   ├── Parquet 导出 (使用 `github.com/xitongsys/parquet-go` 或 Go 标准库)
│   │   ├── schema: object_id, tenant, bucket, key, size, etag, storage_class, created, updated, metadata, tags
│   │   └── 支持 SQL 查询 + 数据分析工具
│   ├── NDJSON 导出 (每行一个 JSON 对象——可使用标准流式处理)
│   │   ├── 与 `jq` / Python Pandas / Go 兼容
│   │   └── 支持管道: `GET /v1/export/objects | jq '.size' | awk '{s+=$1} END {print s}'`
│   └── 对象内容批量导出 tarball:
│       ├── PUT /v1/export/batch → 后台 job → 下载 tar.gz（复用 snapshot）
│       └── 格式: {objects/{key}, metadata/{key}.json}
├── 自动 SDK 生成:
│   ├── 从 OpenAPI spec 生成 Go/Python/JS SDK
│   │   ├── 工具: `openapi-generator` 或 `oapi-codegen`
│   │   ├── CI: 每次 OpenAPI 变更自动重新生成 + 发布 PR
│   │   └── 目标: 3 个 SDK 保持 API 100% 同步
│   ├── SDK 功能矩阵:
│   │   ├── Go: 30 方法 (✅ 完整)
│   │   ├── Python: 部分 (❌ 未审计)
│   │   └── JS: 部分 (❌ 未审计)
│   └── SDK 测试: 对 mock server 运行合约测试
├── 协议互操作测试套件:
│   ├── 1 个请求 → 4 个协议验证:
│   │   └── PUT file → REST 201, S3 200, WebDAV 201(Created), MCP write_file
│   │   └── GET file → REST 200, S3 200, WebDAV 200, MCP resources/read
│   ├── S3 兼容性测试:
│   │   ├── 使用 `aws-s3-test` 或自定义测试套件
│   │   ├── 验证每个 S3 端点的正确行为
│   │   └── CI 中运行
│   └── WebDAV 兼容性测试:
│       ├── 使用 cadaver / litmus 测试套件
│       └── CI 中运行
└── 开放格式支持:
    ├── 图片: Exif/XMP 元数据保留 (当前 thumbnail 使用标准库正确处理)
    ├── 文档: 自定义元数据字段的标准化 (当前使用 _aero_* 前缀——良好实践)
    └── JSON Schema: 可选的对象元数据验证 (用户定义)
```

**复用资产：** `api/rest/openapi.json`（SDK 生成的源）、`snapshot/snapshot.go`（导出框架）、`sdk/go/aerovault/client.go`（Go SDK 参考实现）、`internal/repository/sql_objects.go`（对象查询可复用）、`api/s3compat/handler_test.go`（HTTP 测试模式可复用）

**预计影响：**

| 场景 | 改进前 | 改进后 |
|--------|------------|--------------|
| 数据导出 | 仅 tar.gz (local+sqlite 限定) | Parquet + NDJSON + 标准 tar |
| SDK 同步 | 手动维护，Go 完整但 Python/JS 缺 | OpenAPI 自动生成，3 语言 100% 同步 |
| S3 互操作 | 无验证 | CI 中 aws-s3-test 验证 |
| 供应商锁定 | 高风险（.db + /objects 闭格式） | 标准格式导出 | 

| 复杂度 | 用户影响 | 代码复用 | 差异化 |
|----------|-------------|-------------|------------|
| 中高 | 高（数据主权） | ~50% | ★★★★☆ |

---

## 7. 综合优先级矩阵（五轮联合）

| 阶段 | v1 方向 | v2 方向 | v3 方向 | v4 方向 | **v5 方向（本轮）** |
|-------|-----------|-----------|-----------|-----------|----------------|
| **P0（立即可做）** | — | 写入断路器 | — | 竞态检测 CI | **S3 补齐 policy/logging/notification** |
| **P0** | — | — | — | 数据访问优化 | **存储后端错误映射统一** |
| **P1（本季度）** | 存储 Tiering | — | 可观测性管线 | 优雅关闭 | **传输安全 (TLS + SSRF 防护)** |
| **P1** | — | 自愈网格 | — | RAG 评估框架 | **开发者入门体验** |
| **P2（下季度）** | FUSE 挂载 | 搜索联邦 | 合规套件 | 开发工具链 | **数据可移植性 & SDK 自动生成** |

---

## 8. 附录：代码库关键元数据快照

| 度量 | 值 |
|--------|-------|
| **Go 源文件** | 237（含本轮新增 1 个文件 `analysis-v5`）。生产 133，测试 104 |
| **总代码行** | ~45,389 |
| **内部包** | 23 |
| **外部依赖 (go.mod)** | ~25 个 |
| **迁移** | 24 对（48 SQL 文件）|
| **Env 变量** | 80+ |
| **接口** | 14 个（Storage、Repository、LLM、Embedder、Extractor、Reranker、ChunkSink、VectorIndex、LexicalIndex、EventSink、ChunkCleaner、SecretProvider、DataKeyWrapper、LeaseStore）|
| **存储后端** | 4（local/S3/OSS/COS）|
| **测试覆盖率** | ~55%（行级）、3 个集成测试（均不在 CI 中执行）|
| **已知 BUG（测试中声明）** | 7 处 |
| **文档文件** | 14 个（md + yml + json + sql）|
| **部署配置** | Docker + Helm + docker-compose + Prometheus + Grafana |

---

> *本文档第五次全局扫描完成，未修改任何代码。新增发现：`circuitbreaker.go` 已在 v2 发布前实现并集成（333 行，完全功能化）；存储后端 SSE 仅覆盖 local；已知 BUG 从上次 7 处变为 7 处仍未修复；OpenAPI 与实际 API 存在 3 处文档缺口。*
