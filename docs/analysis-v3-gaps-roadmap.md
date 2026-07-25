# 🏗️ AeroVault 深度评估 v3 — 协议生态、运维卓越、平台经济性

> **日期:** 2026-06-30  
> **方法:** 全局代码扫描（236 文件 / ~45K 行），第三轮，聚焦新维度  
> **视角:** 平台工程 + 产品策略 + 开发者体验

---

## 0. 本轮焦点

前两轮分析覆盖了**功能差距**（v1）和**系统韧性**（v2）。本轮聚焦被忽略的**软性维度**——API 协议生态一致性、开发者体验、运维可诊断性、平台经济性（多云成本、用量计量）、以及安全合规纵深。这些方面不直接影响"能否工作"，但决定了**能否在真实企业环境中落地**。

---

## 1. 协议生态与 API Surface 综合分析

AeroVault 有 4 种协议适配器（REST、S3 兼容、WebDAV、MCP），但它们的**能力交集**存在系统性缺口。

### 1.1 协议能力矩阵

| 功能 | REST `/v1` | S3 `/s3` | WebDAV | MCP |
|--------|:------:|:--------:|:--------:|:---:|
| **对象 CRUD** | ✅ | ✅ | ✅ | ✅ |
| **Multipart** | ✅ | ✅ | ❌ | ❌ |
| **版本控制** | ✅ `?version=` | ✅ | ❌ | ❌ |
| **标签管理** | ✅ `/tags` | ✅ | ❌ | ❌ |
| **ACL** | ✅ `/acl` | ✅ | ❌ | ❌ |
| **预签名 URL** | ✅ `/presign` | ✅ | ❌ | ❌ |
| **桶策略** | ✅ `/policy` | ✅ `?policy` | ❌ | ❌ |
| **CORS 管理** | ✅ `/cors` | ✅ | ❌ | ❌ |
| **桶日志配置** | ❌ | ✅ `?logging` | ❌ | ❌ |
| **桶通知配置** | ❌ | ✅ `?notification` | ❌ | ❌ |
| **桶管理 (CRUD)** | ❌ | ✅ `PUT /{bucket}` | ❌ | ❌ |
| **事件流 SSE** | ✅ `/events/stream` | ❌ | ❌ | ❌ |
| **AI 搜索** | ✅ `/search` | ❌ | ❌ | ✅ |
| **AI Chat** | ✅ `/chat` | ❌ | ❌ | ✅ |
| **AI Agent** | ✅ `/agent` | ❌ | ❌ | ❌ |
| **对象血缘** | ✅ `/lineage` | ❌ | ❌ | ❌ |
| **批量删除** | ❌ | ✅ `POST ?delete` | ❌ | ❌ |
| **桶列表** | ❌ | ✅ `GET /` | ✅ PROPFIND | ❌ |
| **管理 API** | ✅ `/admin/*` | ❌ | ❌ | ❌ |
| **健康检查** | ✅ `/healthz` | ❌ | ❌ | ✅ `ping` |
| **OpenAPI spec** | ✅ `/openapi.json` | ❌ | ❌ | ❌ |

**发现：** S3 兼容层是功能最完整的协议（与标准 S3 API 对齐程度高），但 REST API 在桶管理、日志配置、通知等方面有缺口。MCP 协议没有管理能力。WebDAV 没有任何 AI 或高级特性。

### 1.2 协议间一致性问题

| 问题 | 示例 | 影响 |
|--------|--------|--------|
| **错误格式不一致** | REST 返回 JSON `{"error":{"code":"","message":""}}`；S3 返回 XML `<Error><Code></Code></Error>`；WebDAV 返回标准 HTTP | 客户端 SDK 需为每个协议编写独立错误解析 |
| **鉴权模型不同** | REST 用 `Authorization: Bearer` / `X-Api-Key`；S3 用 `SigV4`；WebDAV 用 `X-Aero-Tenant` + 隐式 auth（依赖外层 middleware） | 协议切换时认证上下文不一致 |
| **桶命名空间** | REST 面向默认桶 `default`；S3 按请求路径区分桶；WebDAV 用前缀路径 | 多桶场景下映射混乱 |
| **分页方式不同** | REST 用 `marker` + `limit`；S3 用 `?list-type=2&continuation-token=`；WebDAV 隐式分页 | SDK 需实现 3 种分页模式 |
| **时间格式** | REST 用 RFC3339；S3 用 ISO 8601；WebDAV 用 RFC1123 | 客户端解析负担 |

**代码引用:** `api/rest/util.go` (JSON 错误编码) vs `api/s3compat/xml.go` (XML 错误编码) vs `api/webdav/dav.go` (标准 HTTP 错误码)

---

## 2. 运维可诊断性缺口

### 2.1 可观测性（Observability）深度分析

| 缺口 | 当前状态 | 真实世界影响 |
|--------|-------------|-------------------------|
| **结构化请求日志** | `AccessLog` 输出 7 个字段（method/path/status/bytes/duration/request_id/tenant） | **无法按用户/桶/错误码/延迟 P99 进行切片分析** |
| **无慢查询追踪** | 无数据库查询耗时指标。SQLite/Postgres 的慢查询不可见 | **性能问题诊断如盲人摸象** |
| **无 AI 推理延迟明细** | 搜索+嵌入耗时合并到 `Request` 级别 span | **无法区分"嵌入慢"vs"检索慢"vs"重排序慢"** |
| **无客户端指标** | 无按 SDK 版本、User-Agent、来源 IP 聚合的客户端分布 | **无法识别异常客户端行为** |
| **无依赖健康探测** | 无存储后端（S3/OSS/COS）的延迟/可用性仪表板 | **中断时无法快速定位故障域** |
| **无作业队列延迟细化** | 作业入队/认领/完成的时间差无追踪 | **后台工作线程瓶颈不可见** |
| **无协调器扫描范围报告** | `reconcile` 扫描了哪些键、耗时多久、清理了多少——全静默 | **运维人员不知道是否正确工作** |

**代码引用:** `telemetry/metrics.go` (15 个仪器，全部是 HTTP 级别——无 DB/存储/AI 延迟明细)；`middleware/middleware.go:AccessLog` (仅 7 个字段)；`reconcile/job.go` (无扫描进度指标)

### 2.2 Debuggability（可调试性）

| 缺口 | 问题 |
|--------|-------|
| **无 Pprof 端点** | 生产环境无法热分析 CPU/内存/goroutine/阻塞 |
| **无 /debug/vars** | 无 expvar 运行时指标暴露 |
| **无慢操作追踪** | 无法针对单次请求进行全链路追踪（OpenTelemetry span 存在但未传播 `traceparent` 到存储后端调用） |
| **无错误明细日志** | `classify(err)` 将错误映射到 7 个通用代码，上下文不足 |
| **无 dump 端点** | 无 `/debug/dump` 来快照协程/锁/队列状态 |

**代码引用:** `cmd/server/main.go` 无 pprof 端点注册；`api/rest/handler.go:classify` 通用错误映射

### 2.3 配置管理

| 缺口 | 问题 |
|--------|-------|
| **80+ 环境变量无验证** | 配置仅在 `Validate()` 中做基本检查（backend/driver/rate-limit），未检查 `AI_CHUNK_WINDOW` 是否为正数、`AI_AGENT_MAX_STEPS` 是否在合理范围 |
| **无运行时配置重载** | 所有配置项仅启动时加载。想调整限速 / 日志级别 / degrated mode → 必须重启 |
| **无配置版本化** | 配置变更不可审计，无 `last_applied` / `changed_by` 追踪 |
| **无配置 diff 工具** | 无法比较当前运行配置 vs 预期配置（"漂移检测"） |

**代码引用:** `config/config.go:Validate` (基本验证)；`config/config_ai.go` (6 个整型参数全无条件检查)

---

## 3. 数据生命周期与平台经济性缺口

### 3.1 计量（Metering）与计费（Billing）

| 缺口 | 当前状态 | 为什么重要 |
|--------|-------------|----------------|
| **无按租户用量计费** | `TenantQuota` 追踪 bytes/objects，但 `Usage` 端点仅返回当前值——无历史趋势、无费率、无账单 | 多租户 SaaS 场景的**核心前提** |
| **无请求级别计量** | 无每请求的存储 I/O 计量（PUT/GET bytes、操作次数） | 无法实现按量计费模型 |
| **无存储类分级定价** | `StorageClass` 已持久化但无价格关联 | 冷/热数据分层的货币化基础缺失 |
| **无带宽计量** | 出站流量（GET/下载）未统计 | CDN/带宽计费的前提 |
| **无月度/日度聚合** | AI 费用有 `SumAICostMicros`（仅日度），但存储无 | 无法生成租户账单 |

**代码引用:** `repository/quota.go` (追踪 bytes/objects 但无历史)；`api/rest/management.go:Usage` (返回当前快照)；`repository/ai_usage_cost.go` (仅 AI 有日度费用聚合)

### 3.2 数据导入/导出与迁移

| 缺口 | 影响 |
|--------|--------|
| **无批量导入工具** | `snapshot.Create` 是 tar.gz（仅 local + SQLite 场景）。无 S3 批量导入、无 `aws s3 sync` 等效 | 无法将现有数据迁移到 AeroVault |
| **无 S3 兼容导出** | 无法以标准 S3 API 格式批量导出对象 | 供应商锁定顾虑 |
| **无云间迁移** | 无从 S3→OSS、OSS→COS、COS→local 的内置迁移通道 | 多云策略实现困难 |
| **无格式中立转储** | 快照包含 .db + /objects——不透明格式。无法用标准工具检查 | 归档与合规受限 |

**代码引用:** `snapshot/snapshot.go` (仅 tar.gz)；`replication/replication.go` (异步复制但非"批量迁移")

### 3.3 归档与合规保留

| 缺口 | 问题 |
|--------|-------|
| **无 WORM 合规模式** | `LockedUntil` 基于应用层时间——管理员可调整系统时钟绕过 | 合规审计无法通过 |
| **无法定保留（Legal Hold）** | 无法对特定对象设置无限期保留（超越 bucket 级别的 `ObjectLockSeconds`） | eDiscovery / 诉讼场景不可用 |
| **无事件保留策略** | `events` 表无限增长，无自动清理 | 长期运行后存储膨胀 |
| **无审计日志保留** | `audit_log`——同上的问题 | 无合规控制 |

**代码引用:** `service/file_crud.go:hardDeleteObject` (检查 `LockedUntil` 但基于 `time.Now()`)

---

## 4. 开发者体验（DX）缺口

| 缺口 | 当前状态 | 为什么重要 |
|--------|-------------|----------------|
| **无 OpenAPI 版本文档** | `/openapi.json` 生成静态定义，但无 `x-version`、`deprecated` 标记、变更日志 | API 演化不可追踪 |
| **SDK 方法不对称** | Go SDK 30+ 方法覆盖 REST 完整；Python/JS SDK 只有基础 CRUD（未审计，以 `sdk/` 目录存在但未完整实现） | Go 之外的 SDK 用户体验差 |
| **无错误类型化 SDK** | Go SDK 返回 `*Error` 带 `Code`/`Message` 但无状态码映射；API 变更时静默失败 | 脆弱的客户端错误处理 |
| **无 CLI 自动补全** | CLI 无 shell 补全（bash/zsh/fish） | 操作效率低 |
| **无 SDK 重试逻辑** | Go SDK `do()` 无重试/退避/超时配置 | 客户端需自行实现 |
| **无 REST API 版本前缀** | 路由是 `/v1` 但内部版本是代码的（无 `/v2` 迁移路径）。破坏性变更不可避免 | API 兼容性风险 |
| **无 throttling 信号** | 429 响应无标准 `Retry-After` 头（当前 429 来自 `ConcurrencyLimiter`，但无 `RateLimit-Reset` / `X-RateLimit-*` 标准头） | 客户端限流处理困难 |

**代码引用:** `sdk/go/aerovault/client.go` (30+ 方法)；`sdk/python/`（存在但未经本次审计）；`api/rest/router.go` (`/v1` 硬编码)；`middleware/ratelimit.go` (无标准限流头)

---

## 5. 安全纵深与合规审计

### 5.1 加密与密钥管理

| 缺口 | 当前状态 | 风险 |
|--------|-------------|------|
| **传输加密（TLS）** | 无内置 TLS 终止。`http.ListenAndServe` 无 `*http.Server.TLSConfig` | 默认部署是明文 HTTP |
| **密钥轮换自动化** | `rewrap.go` 是启动时单次运行。无定期轮换调度（例如：每 90 天自动轮换 + rewrap） | 加密最佳实践不合规 |
| **HSM / KMS 集成局限** | `kms.go` 支持 HTTP KMS，但无 AWS KMS / GCP Cloud KMS / Azure Key Vault 原生集成 | 企业合规受限 |
| **加密密钥访问审计** | 密钥使用（加密/解密操作次数、每次使用的 `kid`）无日志 | 合规审计不完整 |
| **客户端加密** | 无客户端-side 加密 SDK（客户端加密后再上传） | 零信任架构不可用 |

**代码引用:** `cmd/server/main.go` (`ListenAndServe` 非 `ListenAndServeTLS`)；`storage/rewrap.go` (启动时单次)；`storage/kms.go` (仅 HTTP KMS，无云原生)

### 5.2 访问控制深度

| 缺口 | 问题 |
|--------|-------|
| **无 RBAC 模型** | `scopes` 是自由格式字符串（`read`, `write`, `admin`）。无角色层级、无权限组合验证 | 细粒度访问控制不可行 |
| **API Key 无过期策略** | Key 创建时带 `ExpiresAt`，但无自动吊销过期 key 的机制 | 凭据蔓延 |
| **无 IP 白名单** | `auth/policy.go` 支持 `IpAddress` 条件（仅 S3 策略）。REST 端点和 API Key 无 IP 绑定 | 凭据泄露后无网络层防御 |
| **无失败登录锁定** | API Key / JWT 认证失败无计数、无延迟、无锁定 | 暴力破解无防护 |
| **无速率限制按租户细化** | `RateLimiter` 按租户执行 token-bucket，但所有租户共享全局 RPS。恶意租户可 DoS 其他租户 | 多租户隔离缺陷 |
| **pre-signed URL 无约束** | URL 可访问任何对象、任何 IP、任何 User-Agent、无审计 | 安全漏洞 |

**代码引用:** `auth/auth.go` (自由格式 scopes)；`auth/store.go` (Key CRUD 但不验证 `ExpiresAt`)；`middleware/ratelimit.go` (全局共享，非每租户独立池)

---

## 6. 🚀 5 个高价值扩展方向（新维度）

---

### 🥇 方向 1：Unified Observability Pipeline — 结构化遥测 + 全链路追踪 + SLO 仪表板

**为什么需要它：**

当前 15 个 OTel 指标全部是 HTTP 请求级别的计数器/直方图。没有存储后端延迟、没有 AI 管线分阶段耗时、没有作业队列深度演变、没有慢查询追踪。这意味着**生产问题诊断只能靠猜测**。

**架构蓝图：**

```
当前: TelemetryMiddleware → 15 counters/histograms (全部 HTTP 级别)

改进: ObservabilityPipeline (新包 internal/telemetry/pipeline)
├── 结构化 AccessLog (JSON lines → 可导入 Loki/Elastic/DataDog):
│   ├── 基础: method, path, status, bytes, duration_ms, tenant, request_id
│   ├── 请求: user_agent, referer, client_ip, protocol
│   ├── 存储: storage_backend, storage_latency_ms, storage_bytes
│   ├── AI: embed_latency_ms, search_latency_ms, llm_latency_ms, tokens, cost_micros
│   └── DB: db_query_count, db_latency_p50/p95/p99, rows_scanned
├── 全链路追踪:
│   ├── Propagation: traceparent → storage.Get / repository.Query → span 上下文
│   ├── Span 类型: http.request, storage.put, db.insert, ai.embed, ai.search, ai.chat
│   └── Exporter: OTLP (gRPC/HTTP) + Jaeger/Zipkin 兼容
├── SLO 仪表板:
│   ├── 可用性: /healthz 成功率, 各协议错误率
│   ├── 延迟: 存储 P50/P95/P99, AI P95 嵌入延迟, Chat P95 首 token
│   ├── 容量: 活跃对象总量, 存储使用率, 作业队列深度, 事件总线丢弃
│   └── 业务: 每租户请求量, AI 费用日度趋势, 热门搜索查询 top-N
└── 健康端点增强:
    ├── /healthz → 存活探针 (当前: 仅返回 ok)
    ├── /readyz → 就绪探针 (当前: 仅 ping DB)
    └── /debug/pprof → CPU/内存/goroutine/block/mutex 热分析
```

**复用资产：** `telemetry/metrics.go`（15 个现有指标可作为基线）、`telemetry/otel.go`（OTel SDK 已配置）、`telemetry/http.go`（HTTP middleware 已注册）、`telemetry/prometheus.go`（Prometheus exporter 已可用）、`middleware/middleware.go:AccessLog`（可通过新增字段扩展）

**预计影响：**

| 指标 | 改进前 | 改进后 |
|----------|------------|--------------|
| 延迟问题定位 | 猜测"是 AI 慢" | 精确到"嵌入 2.3s + 检索 50ms + LLM 4.1s" |
| 存储瓶颈发现 | 无数据 | S3 P95 GET 延迟 1.2s → 优化连接池 |
| 作业队列瓶颈 | 不可见 | 队列堆积可视化 → 扩容 worker |
| 错误率趋势 | 仅日志 | 仪表板按错误码 / 协议 / 租户聚合并告警 |

| 复杂度 | 用户影响 | 代码复用 | 差异化 |
|----------|-------------|-------------|------------|
| 中 | 极高（运维信心） | ~60% | ★★★★★ |

---

### 🥇 方向 2：API Gateway 统一层 — 协议归一化、版本管理、流量治理

**为什么需要它：**

四种协议各自独立实现，错误格式不统一、鉴权方式不同、分页机制各异。面向 SDK/CLI 用户的 API 体验是割裂的。AeroVault 需要一个**内部 API 网关**，将请求归一化为统一的内部表示，再分派给 `FileService`。

**架构蓝图：**

```
当前: HTTP → REST/S3/WebDAV/MCP → 各自 Handler → FileService (各自鉴权+路由+编码)

改进: APIGateway (新包 internal/gateway)
├── RequestNormalizer:
│   ├── 协议检测 (Content-Type/URL 模式)
│   ├── 统一鉴权: SigV4 / Bearer / ApiKey → 统一 Principal 对象
│   ├── 统一租户提取 (X-Aero-Tenant 始终作为来源)
│   ├── 统一错误编码: 内部错误 → REST JSON / S3 XML / WebDAV multistatus
│   └── 统一分页: marker+limit → 适配各协议分页形式
├── VersionRouter:
│   ├── /v1/... → REST v1 路由
│   ├── /v2/... → 新路由（预留）
│   └── 向后兼容: /v1 旧路由 + /v2 新路由共存
├── 流量治理:
│   ├── 请求限流 (每租户独立速率桶, 复用 RateLimiter)
│   ├── 请求大小限制 (Content-Length 硬上限)
│   ├── 超时控制 (按路径配置超时)
│   └── 断路器 (后接方向 1 的 StorageCircuitBreaker)
├── 统一审计:
│   └── 所有写操作 → AuditEntry (当前 admin 操作已审计，但 S3/WebDAV 写入无)
└── 标准限流响应头:
    ├── RateLimit-Limit, RateLimit-Remaining, RateLimit-Reset
    └── Retry-After (当前仅 ConcurrencyLimiter 设置)
```

**复用资产：** `api/rest/router.go`（路由注册可重组）、`auth/auth.go`（统一鉴权层已存在）、`auth/auth_middleware.go`（middleware 链可复用）、`api/s3compat/xml.go`（S3 XML 编码可被网关复用）

**预计影响：**

| 场景 | 改进前 | 改进后 |
|--------|------------|--------------|
| 统一限流头 | 无 `RateLimit-*` → 客户端需自行探测 | `RateLimit-Limit: 100, RateLimit-Remaining: 42` |
| 跨协议审计追踪 | S3/WebDAV 操作无审计 | 所有协议写入审计日志 |
| API 版本迁移路径 | /v1 无法演化为 /v2 | 同时挂载 /v1 旧 + /v2 新 |
| SDK 兼容性 | 4 种错误解析逻辑 | 网关统一 → 1 种错误解析 |

| 复杂度 | 用户影响 | 代码复用 | 差异化 |
|----------|-------------|-------------|------------|
| 中 | 高（SDK 体验） | ~50% | ★★★★☆ |

---

### 🥇 方向 3：Multi-Cloud Cost Optimization Engine — 智能存储调度

**为什么需要它：**

AeroVault 已支持 4 种存储后端（local/S3/OSS/COS），但用户在启动时选择一个后端，**运行时无法动态切换**。真正的多云策略需要：按成本选择存储位置、按访问模式自动迁移、跨云差异定价的透明计量。

**架构蓝图：**

```
当前: → buildStorage() → 一个 storage.Storage 实例 → 所有对象写入同一后端

改进: CostOptimizer (新包 internal/optimizer)
├── StorageSelector:
│   ├── 多 storage.Storage 实例同时注册 (S3_US + OSS_CN + COS_EU)
│   ├── 写入策略: cost_min / latency_min / geo_nearest / custom_label
│   └── 读取策略: 就近读取 + 跨区域回退
├── CostTracker:
│   ├── 各后端单价配置 (PUT $/GB, GET $/GB, Storage $/GB/月)
│   ├── 每对象存储成本累计字段 (优化 `StorageClass` 为记录成本而非仅类)
│   └── 月度成本预测 + 仪表板
├── MigrationScheduler:
│   ├── 基于访问频率的热/冷数据调度 (复用 reconcile 周期)
│   ├── 冷数据迁移到低成本后端 (S3_STANDARD → OSS_ARCHIVE)
│   └── 地理调度: EU 用户数据 → COS_EU
├── StorageTieringPolicy DSL (复用 BucketConfig):
│   ├── rules:
│   │   - after 30d: tier to "s3_ia"
│   │   - size > 100MB: tier to "oss_standard"
│   │   - tag "archive": tier to "cos_archive"
│   └── evaluation: reconcile/lifecycle.go 扩展
└── 出站流量成本追踪:
    └── GET/Presign 出站 → 按目的地区域计量成本
```

**复用资产：** `storage/storage.go`（接口可被 `StorageSelector` 包裹）、`reconcile/lifecycle.go`（周期性扫描可作为迁移触发器）、`repository/sql_objects.go`（`StorageClass` 字段可承载成本信息）、`config/config.go`（存储后端配置已存在）

**预计影响：**

| 场景 | 改进前 | 改进后 |
|--------|------------|--------------|
| 跨云写入 | 一次部署一个后端 | PUT 时按策略选择 S3_US / OSS_CN / COS_EU |
| 冷数据降本 | 无自动迁移 | 30 天未访问 → 自动迁移到 OSS_ARCHIVE（节省 ~70% 存储费）|
| 成本透明 | 无数据 | 仪表板显示每租户每后端月度成本 |

| 复杂度 | 用户影响 | 代码复用 | 差异化 |
|----------|-------------|-------------|------------|
| 高 | 极高（成本节约） | ~40% | ★★★★★ |

---

### 🥇 方向 4：Enterprise Compliance Suite — WORM 2.0 + Legal Hold + Audit Chain

**为什么需要它：**

当前 WORM 实现（`LockedUntil` + `time.Now()`）可被时钟绕过。无法应对法定保留（诉讼/调查期间无限期冻结）。审计日志存于 SQL 表——管理员可篡改。企业合规要求**防篡改审计链 + 加密证明的保留 + 可导出的合规报告**。

**架构蓝图：**

```
当前: LockedUntil (应用层时间) + audit_log (SQL 表, 可篡改)

改进: ComplianceSuite (新包 internal/compliance)
├── WORM 2.0 — 强保留:
│   ├── 时钟漂移检测: 记录 NTP 偏移 + 拒绝明显偏离的锁定请求
│   ├── 法定保留覆盖: LegalHold (独立于 LockedUntil, 可无限期)
│   │   ├── 按对象 / bucket / 租户设置
│   │   ├── 任何硬删除 / 覆盖被阻止
│   │   └── 管理员可查询: `GET /v1/compliance/legal-holds`
│   └── 保留事件日志: 每次保留创建/解除 → 审计事件
├── 审计链:
│   ├── 哈希链: 每条 AuditEntry 包含前一条的 SHA-256 → 防篡改
│   ├── 定期链根发布: 每 24h 将链根哈希写入可信时间戳服务
│   └── 导出: 审计链 → JSON/CSV/PDF 合规报告
├── 事件与日志保留策略:
│   ├── events 表: 配置 TTL，过期事件自动归档到冷对象存储
│   ├── audit_log 表: 同上
│   └── webhook_failures 表: 同上
└── 合规报表生成:
    ├── 每租户: WORM 锁定对象清单 + 法定保留清单 + 审计链校验
    ├── SOC2 报告: 访问控制日志 + 加密密钥轮换记录 + 备份恢复验证
    └── GDPR 报告: 个人数据检索 + 删除行踪
```

**复用资产：** `service/file_crud.go:hardDeleteObject`（已有锁检查——扩展 `LegalHold`）、`repository/audit.go`（追加——改为哈希链）、`repository/repository.go`（`AuditEntry` 字段保留）、`storage/rewrap.go`（可作为链根发布的触发器）

**预计影响：**

| 场景 | 改进前 | 改进后 |
|--------|------------|--------------|
| 法定保留 | 不支持 | 诉讼时一键冻结相关对象 |
| 审计证据 | SQL 表可篡改 | 哈希链可验证 + 链根已发布到时间戳服务 |
| 合规导出 | 无 | SOC2 / GDPR 报告可一键生成 |
| WORM 绕过 | 调整系统时钟即可 | NTP 偏移检测 + 法定保留不可覆盖 |

| 复杂度 | 用户影响 | 代码复用 | 差异化 |
|----------|-------------|-------------|------------|
| 中高 | 极高（合规必经之路） | ~35% | ★★★★★ |

---

### 🥇 方向 5：Developer Platform — Webhook Functions + SDK Generation + Playground

**为什么需要它：**

AeroVault 有事件总线但没有**用户自定义数据处理**。用户不能在"对象创建时"运行自己的 Go/JS 函数（类似 AWS Lambda + S3 触发器）。这意味着 AeroVault 是存储终点，不是平台起点。

**架构蓝图：**

```
当前: EventBus → 内置订阅者 (Indexer, AV, Replication, Webhook)

改进: DeveloperPlatform (新包 internal/functions)
├── FunctionEngine:
│   ├── 用户注册函数: POST /v1/functions {"name":"resize","runtime":"wasm","code":"base64..."}
│   ├── 触发器绑定: POST /v1/functions/{name}/bind {"event":"object.created","filter":"key=images/*"}
│   ├── 运行时: Wasm 沙箱 (使用 wazero 零依赖 WASM 运行时)
│   ├── SDK: 用户函数接收 Event 结构体，返回 Action (pass/modify/delete/drop)
│   └── 计量: 每次调用计费（函数执行时间 + 资源消耗）
├── SDK 代码生成器:
│   ├── OpenAPI → Go / Python / JS / Java SDK 自动生成
│   ├── 发布 CI: 每次 API 变更 → 自动更新 SDK + 发布 GitHub release
│   └── 样例: 使用函数引擎的完整端到端示例
├── Developer Playground (Web UI 增强):
│   ├── API Explorer: 在浏览器中调用任意 REST API + 查看响应
│   ├── SSE 事件监视器: 实时查看事件流 + 按租户/类型过滤
│   ├── 搜索调试器: 查看嵌入向量、BM25 分数、重排序过程
│   └── 函数编辑器: 浏览器中编辑 WASM 函数 + 测试 + 部署
└── 变更日志 / API 版本通知:
    ├── OpenAPI `x-sunset` 标记 → 提前通知 API 废弃
    └── SDK 中的弃用警告
```

**复用资产：** `events/bus.go`（发布/订阅机制）、`events/webhook.go`（外部调用执行基础设施）、`api/rest/openapi.go`（OpenAPI 规范可作为 SDK 生成的源）、`api/rest/router.go`（路由定义列表）、`webui/static/index.html`（已有 SPA——可扩展 playground tab）

**预计影响：**

| 场景 | 改进前 | 改进后 |
|--------|------------|--------------|
| 用户自定义处理 | 需要部署独立服务 + 配置 webhook | 直接在 AeroVault 内注册 WASM 函数 |
| SDK 维护负担 | 手动编写 3 个 SDK | OpenAPI → auto SDK 生成 |
| 开发者 onboarding | 阅读文档 + curl | Playground API Explorer + 搜索调试器 |
| 事件驱动生态 | AeroVault 唯一消费事件 | 用户可以编写自己的事件消费者 |

| 复杂度 | 用户影响 | 代码复用 | 差异化 |
|----------|-------------|-------------|------------|
| 高 | 极高（平台 vs 工具） | ~45% | ★★★★★ |

---

## 7. 三轮分析综合路线图

| 阶段 | 方向（v1） | 方向（v2） | 方向（v3，本轮） |
|-------|--------------|--------------|--------------|
| **Q3** | 存储 Tiering | 写入代理 + 断路器 | 统一可观测性管线 |
| **Q3** | FUSE 挂载 | Saga 编排引擎 | 开发者平台 + 函数引擎 |
| **Q4** | 外部队列事件 | 多模态存储引擎 | API 网关统一层 |
| **Q4** | 对象缓存层 | 自愈存储网格 | 成本优化多云调度 |
| **H1** | 多区域复制 | 搜索联邦 | 合规套件 |

**总体推进建议（三轮联合）：**

```
v0.5: 存储 Tiering + 断路器 + 可观测性管线     (基础设施可靠性)
v0.6: FUSE + API Gateway + 开发者平台           (采用率与体验)
v0.7: 事件队列 + 合规套件 + 成本优化            (企业级平台)
```

---

## 8. 附录：配置项完整审计

| 配置键 | 类型 | 默认值 | 验证 | 是否有边界检查 |
|-------------|--------|---------|----------|------------------|
| `AI_CHUNK_WINDOW` | int | 600 | ❌ | 可为负数或零 |
| `AI_CHUNK_OVERLAP` | int | 80 | ❌ | 可 >= Window → 无意义 |
| `AI_AGENT_MAX_STEPS` | int | 4 | ❌ | 可为 0 / 负数 |
| `AI_EMBED_DIM` | int | 256 | ❌ | 可为 0 或负数 → 嵌入断裂 |
| `RECONCILE_INTERVAL_MINUTES` | int | 0 | ❌ | 可为负数 |
| `RECONCILE_RETENTION_DAYS` | int | 0 | ❌ | 可为负数 |
| `STORAGE_CONNECT_TIMEOUT` | int | 5 | ❌ | 可为 0 → 永远等待 |
| `RATE_LIMIT_RPS` | float | 0 | ✅ | 与 Burst 联合检查 |
| `AI_RATE_LIMIT_RPS` | float | 0 | ✅ | 与 Burst 联合检查 |

> *建议：在 `config/config.go` 中添加一个 `validateAI()` 方法，对 AI 配置项逐一做有理数检查。*

---

> *本文档第三次全局扫描完成，未修改任何代码。基于 `AGENTS.md` 第 0 节：`handler.go`（565 行）超过 500 行限制，建议在 API 网关方向实施时优先拆分。*
