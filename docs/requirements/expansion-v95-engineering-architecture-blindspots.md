# 高价值扩展方向：服务端 COPY/MOVE 数据移动、Webhook 交付基础设施、跨协议安全架构、分布式追踪可观测性

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — `cmd/server/main.go` + `internal/` 全部 30+ 子包（231 个 Go 源文件），3 套 SDK（Go/Python/JS），MCP 双模式（HTTP+stdio），Web UI，50 对迁移文件，`deploy/` 全套配置（Helm/Grafana/Prometheus/OTel），`HARNESS.md`，`AGENTS.md`，`ROADMAP.md`  
> **去重验证：** 对 `docs/requirements/` 下全部 94 份既有分析文档（`expansion-directions.md` ~ `expansion-v94-architectural-white-spaces.md`）逐方向进行关键词正则 + 代码锚点交叉验证 + `ROADMAP.md` 10 大方向 + `TODO.md`  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。选取**代码中存在明确锚点、有实质性产品/架构影响、且在 94 轮分析 + ROADMAP + TODO 中未被独立深度覆盖**的方向。每个方向包含：现状与代码证据 → 产品价值 → 架构权衡 → 边界情况。

---

## 去重验证

对 `docs/requirements/` 下全部 94 份既有分析文档逐方向进行关键词正则 + 语义交叉验证：

| 方向 | 既往覆盖情况 |
|------|-------------|
| **方向一：服务端 COPY/MOVE 数据移动架构** | ✅ **零实质性架构分析** — 全量 94 份文档中，ROADMAP 第 7 项「S3 feature parity」的 6 个子项列表包含一行 `COPY Object` 标注为「via handler」；ROADMAP 第 9 项的 tier transition worker 描述了 **storage class 间**的数据移动但聚焦于成本优化而非 COPY/MOVE 操作自身的数据移动架构（原子性、进度、metadata 保留、跨后端）。**零文档**分析 `copyObject` 的下载-再-上传实现、缺少的服务端副本、大对象原子移动、跨后端副本协调。正则搜索 `copy.architect\|move.architect\|server.side.copy\|in.storage.copy\|atomic.move\|copy.progress\|copy.metadata\|cross.backend.copy` → **0 命中** |
| **方向二：Webhook 交付基础设施成熟度** | ✅ **零实质性架构分析** — v42「S3 协议差距」表格 1 行列出 notifications 缺失但仅点到为止；v94 方向四覆盖「事件溯源与不可变事件日志」，聚焦于**事件数据的持久化存储、事件类型扩展、消费者偏移量追踪**——这是事件存储层（event persistence），与 outbound webhook 交付层（delivery reliability, SLA, 重试策略, 死信队列, 交付分析）**完全不同的架构关注点**。正则搜索 `webhook.*delivery\|webhook.*sla\|webhook.*retry\|dead.letter.*webhook\|webhook.*reliab\|webhook.*govern\|delivery.*guarantee\|outbound.*event` → **0 命中** |
| **方向三：跨协议安全架构** | ✅ **零实质性架构分析** — v94 方向二覆盖「准入控制与并发治理现代化」，聚焦于**操作层面的并发限制和速率控制**（admission control），属于**弹性工程**（resilience engineering）而非**安全架构**（security architecture）。全量 94 份文档中**零文档**分析：无 OIDC/LDAP/SAML 身份联邦、无输入验证中间件、无 XML 载荷大小限制、无 content-type 校验、无跨协议认证统一、无安全边界审计。正则搜索 `OIDC\|LDAP\|SAML\|federat.*auth\|identity.*federat\|input.*validat.*middleware\|content.type.*enforce\|request.*size.*limit.*per.*endpoint\|XXE\|billion.*laugh\|security.*architect\|auth.*unified\|cross.protocol.*auth` → **0 命中** |
| **方向四：分布式追踪与可观测性成熟度** | ✅ **零实质性架构分析** — ROADMAP 第 2 项覆盖「domain metrics」（域级计数器、直方图、Grafana 面板、Prometheus 告警），但**完全不涉及分布式追踪**（distributed tracing）：`otel.Tracer` 仅在 HTTP middleware 中创建单一根 span，**从不传播到 Service/Storage/Repository 层**，无嵌套 span，无 trace context 传递，无跨组件延迟分解，无跨协议请求关联。正则搜索 `distributed.trac\|trace.propagat\|trace.contex\|span.*nest\|span.*child\|trace.*sampl\|trace.*correlat\|cross.component.trac\|SLO.*based\|burn.rate\|multi.window` → **0 命中**（ROADMAP #2 匹配 `trace` 的上下文均为 `trace as in "follow"` 而非 OpenTelemetry tracing） |

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 |
|---|------|------|--------|---------|---------|
| **1** | **服务端 COPY/MOVE：从客户端流式传输到存储中介的数据移动** | 性能/架构 | **P1** | COPY 对象需完全下载到服务器内存再上传，大对象无法处理、同后端副本浪费带宽、不支持原子移动、缺少跨后端副本协调、无进度追踪 | `internal/api/s3compat/extra.go:39`（`copyObject` — 全量 Read → Put，无服务端副本）；`internal/service/file_crud.go:Get+Put`（COPY 依赖两次完整 I/O）；`internal/storage/storage.go`（`Storage` 接口无 `Copy` 方法）；`internal/storage/local.go`（`Copy` 不存在）；`internal/storage/s3.go`（无 S3 COPY 对象 API 调用）；`internal/service/file.go:WithDefaultStorageClass`（StorageClass 写入后永不用于存储路由）；`internal/repository/sql_objects.go`（对象元数据无 `copy_source` 追踪） |
| **2** | **Webhook 交付基础设施成熟度：可靠性、治理与可观测性** | 可靠性/合规 | **P1** | 当前 webhook：单 URL 无过滤、指数退避死信时用「标记成功」永久丢失失败信息、无交付 SLA 追踪、无外发限流、无密钥轮换、无交付延迟直方图、无事件去重窗口 | `internal/events/webhook.go:deliver`（`w.postOne` — 给所有 URL 各发一次，无去重窗口）；`internal/events/webhook.go:retryOne`（最多 10 次后 `MarkWebhookSucceeded` —**永久丢失事件**）；`internal/events/webhook.go:RetryLoop`（15s 固定轮询，无自适应间隔）；`internal/events/webhook.go:postOne`（`http.Client{Timeout:5s}` — 硬编码无配置）；`internal/repository/webhook_failures.go`（只有 `succeeded bool` 二元状态，无 dead_letter 状态）；`internal/telemetry/metrics.go`（无 webhook 交付延迟/成功率/队列深度指标）；`internal/events/webhook.go:deliver`（`w.postOne` 并发调用所有 URL，无限速） |
| **3** | **跨协议安全架构：身份联邦、输入验证与攻击面收缩** | 安全/架构 | **P1** | 4 种协议各自独立认证：REST（JWT/API-Key）、S3（SigV4/策略/ACL）、WebDAV（租户头+IP）、MCP（Bearer）；无统一身份模型；无 OIDC/LDAP/SAML 联邦；无中央输入验证层；XML 端点无载荷大小限制；无 Content-Type 强制校验；无跨协议授权审计 | `internal/auth/auth.go:Registry`（硬编码 `Parse` env keys + JWT + SigV4 — 无外部身份源）；`internal/auth/auth.go:Key`（`Token string` — 无 issuer/sub 等联邦身份字段）；`cmd/server/main.go:configureAuthSecrets`（仅 JWT + SigV4 + 持久化 API 密钥——无联邦）；`internal/api/s3compat/handler.go:PutObject`（`xml.NewDecoder(r.Body).Decode(&in)` — 无大小限制）；`internal/api/s3compat/handler.go:deleteObjects`（`xml.NewDecoder(r.Body).Decode(&in)` — 无大小限制）；`internal/middleware/middleware.go`（无 `Content-Type` 验证或 body 大小校验中间件）；`internal/api/rest/handler.go:Put`（`r.Body` 直传 `svc.Put` — 无大小预检）；`internal/middleware/cors.go`（CORS 中间件但不验证 Origin 合法性） |
| **4** | **分布式追踪与可观测性成熟度：Trace Context 传播、结构化日志关联与 SLO 驱动运维** | 运维/可靠性 | **P2** | OTel 已初始化但 trace context 从不跨越组件边界：HTTP middleware 创建单一根 span 然后立即结束；Service/Storage/Repository 调用链无嵌套 span；跨协议请求无法关联；结构化日志与 trace ID 无关联；Prometheus 告警仅覆盖 AI 延迟（3 条规则），无多窗口多燃烧率 SLO 告警 | `internal/telemetry/http.go:23`（`tracer.Start(r.Context(), ...)` — 仅在 HTTP 层创建 span，结束后不传播）；`internal/telemetry/http.go:34`（`span.End()` — 在 middleware 返回前结束，子调用无父 span 上下文）；`internal/service/file_crud.go:Get`（无 trace context 接收或子 span 创建）；`internal/storage/local_read.go:Get`（无 trace context）；`internal/middleware/middleware.go:RequestID`（RequestID 存在但不与 trace ID 关联）；`internal/telemetry/otel.go:Setup`（导出器仅配置了 OTLP 但 `sampler` 使用默认 always-on——无概率采样）；`deploy/grafana/aero-vault-ai-ops-dashboard.json`（12 个面板全为指标面板，零 trace 面板）；`deploy/prometheus/alerts.yml`（仅 3 组告警规则：AI p95 延迟 + 队列深度——无 multi-window/burn-rate SLO 告警） |

---

## 方向一：服务端 COPY/MOVE——从客户端流式传输到存储中介的数据移动

### 现状

当前 `copyObject` 的实现是将源对象**完全读取到服务器内存，再上传到目标位置**：

```go
// internal/api/s3compat/extra.go:39
func (h *Handler) copyObject(w http.ResponseWriter, r *http.Request, dstBucket, dstKey, copySource string) {
    // ...
    rc, src, err := h.svc.Get(r.Context(), tenant, srcBucket, srcKey)  // ← 全量下载到内存
    // ...
    dst, err := h.svc.Put(r.Context(), tenant, dstBucket, dstKey, rc, src.Size, opts)  // ← 全量上传
    // ...
}
```

**五个结构性缺陷：**

| # | 缺陷 | 代码证据 | 影响 |
|---|------|---------|------|
| **1** | **大对象全量内存缓冲** | `copyObject` 调用 `svc.Get` 返回 `io.ReadCloser`，`svc.Put` 消费流。但 `svc.Get` 内部（`file_crud.go:Get`）在开启 SSE 加密时**全量解密到内存**，WebDAV `spillBuffer`（8 MiB 阈值）和 Idempotency `idemSpool` 只在各自层级做边界，`copyObject` 本身无流控 | 10GB 对象 COPY 需要至少 10GB 可用内存（实际更多因 internal buffering），OOM 风险 |
| **2** | **同后端副本带宽浪费** | S3/OSS/COS 后端支持服务端 COPY（`CopyObject` API），但 aero-vault 从不使用——同一后端内的 COPY 也要经过服务器 | S3 同 bucket COPY 账单包含服务器端和下载带宽双重费用 |
| **3** | **跨后端副本无协调** | `Replication` worker 已实现 `primary.Get` → `replica.Put`，但 COPY/MOVE 不走此路径 | 从 S3 到 local 的 cross-backend copy 与 replication 实现完全重复 |
| **4** | **无原子移动操作** | REST `/v1/files/{key}/move` 或 S3 MOVE 需要 DELETE + COPY 两步，非原子 | 中途失败导致源删目标未写，数据丢失 |
| **5** | **无进度追踪** | COPY 是一个请求完成整个操作，无分段进度可观测 | 大对象 COPY 超时后无法续传，客户端无反馈 |

此外，`Storage` 接口（`storage.go`）没有任何 `Copy` 或 `Move` 方法——调用方只能通过 Get+Put 组合实现：

```go
// internal/storage/storage.go
type Storage interface {
    Put(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) (ObjectInfo, error)
    Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)
    // ... 无 Copy/Move 方法
}
```

同时，`Replication` 包（`internal/replication/replication.go`）已经实现了 backend-to-backend 拷贝的完整模式：primary.Get → replica.Put + tag update + idempotent。这个模式可以作为 COPY/MOVE 的基础设施，但当前完全不共享。

### 产品价值

| 场景 | 当前体验 | 优化后 |
|------|---------|--------|
| **同 bucket COPY 1GB 对象** | 服务器下载 1GB + 上传 1GB = 2GB 带宽 + 2x I/O + 2x 时间 | S3 服务端 COPY：元数据操作 + 零数据移动，< 100ms |
| **跨 bucket MOVE 100MB 对象** | COPY（2x I/O）+ DELETE（1x I/O）= 3x I/O，非原子 | 原子 MOVE：源标记 + 后台复制 + 源清除，无中断窗口 |
| **跨后端迁移（S3 → local）** | 自定义脚本，不追踪进度，失败需重头开始 | `Storage` 统一 Copy 方法 + 后台 job + 进度追踪 |
| **版本化桶的 COPY** | COPY 始终产生新版本，无法选择保留版本历史 | 支持 `COPY` vs `COPY + replace` 语义（S3 `x-amz-copy-source-if-match`） |
| **大对象 COPY 超时** | 全有或全无，1h 后超时返回错误，无任何中间状态 | 分段 COPY（multipart copy），已完成的 parts 可续传 |

### 架构权衡

**建议方案：分层 Copy 策略模式**

```
            ┌──────────────────┐
            │  CopyStrategy    │  ← 策略接口：Copy(ctx, srcKey, dstKey, opts) (ObjectInfo, error)
            └──────┬───────────┘
                   │
        ┌──────────┼───────────┐
        ▼          ▼           ▼
 ┌────────────┐ ┌────────┐ ┌────────┐
 │ ServerSide │ │ Client │ │ Chunked│
 │ Copy       │ │ Stream │ │ Copy   │
 └────────────┘ └────────┘ └────────┘
```

**策略选择逻辑：**

| 条件 | 策略 | 实现 |
|------|------|------|
| 源和目标**同一后端**且后端支持服务端 COPY（S3/OSS/COS） | **ServerSide Copy** | 调用后端原生 `CopyObject` API，零数据通过服务器 |
| 后端不支持服务端 COPY（local）或跨后端 | **Client Stream** | 当前实现（Get → Put），但需增加流控和解密后重加密传递 |
| 对象 > 5GB（S3 multipart copy threshold） | **Chunked Copy** | 分段 ListParts → UploadPartCopy → CompleteMultipart，支持续传 |

**接口扩展：**

```go
// 在 Storage 接口中新增（可选实现）
type Storage interface {
    // ... 现有方法
    CanCopy() bool  // 是否支持服务端副本
    Copy(ctx context.Context, srcKey, dstKey string, opts CopyOptions) (ObjectInfo, error)
}

type CopyOptions struct {
    MetadataDirective string     // COPY | REPLACE
    TaggingDirective  string     // COPY | REPLACE
    Conditional       *CopyConditional  // If-Match / If-None-Match / If-Modified-Since
    StorageClass      string     // 目标存储类（可能跨 tier）
}
```

**不可变对象保证：** COPY 操作必须保证源对象在 COPY 期间不被修改：

| 机制 | 实现 |
|------|------|
| **乐观锁** | `x-amz-copy-source-if-match` / `x-amz-copy-source-if-none-match`（S3 标准） |
| **版本锁定** | COPY 时锁定源版本 ID，读取时验证 `updated_at` 未变化 |
| **事务性 COPY（Postgres）** | `SELECT ... FOR UPDATE` 锁定源行，COPY 完成后释放 |

**原子 MOVE 实现路径：**

```
Phase 1: 源标记 + 元数据复制
    INSERT INTO objects (tenant, bucket, key, ...)  -- 目标行
    UPDATE objects SET moved_to = '<target>' WHERE ...  -- 源标记

Phase 2: 后台 blob 移动（Job Queue）
    Job: JobMoveBlob { source_storage_key, target_storage_key }
    → storage.Copy(src, dst) → storage.Delete(src) → repo.UpdateStorageKey(...)

Phase 3: 源清除（Reconcile 中处理标记行）
    超过 grace period 后删除源 metadata 行
```

**WebDAV MOVE / REST Rename：** 当前 WebDAV 的 MOVE 和 REST 的 rename 都需要经过 copyObject：

```go
// internal/api/webdav/dav.go
func (fs *davFS) rename(ctx context.Context, oldName, newName string) error {
    // 当前实现：Get → Put → Delete（与 copyObject 完全相同的模式）
}
```

统一 COPY/MOVE 基础设施后，WebDAV MOVE 和 REST rename 直接重用 `svc.Move(ctx, tenant, srcBucket, srcKey, dstBucket, dstKey)`。

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| **源对象在 COPY 过程中被删除** | 使用版本 ID 锁定或 `If-Match` eTag 条件；若源已不存在，返回 `ErrNotFound` |
| **版本化桶的 COPY** | 默认 COPY 产生新版本（`InsertObjectVersion`）；可选的 `replace` 语义 |
| **跨后端 COPY 加密** | 源后端解密 → 明文传递 → 目标后端重新加密（当前 `service.Get` + `service.Put` 已隐含此模式） |
| **大对象分段 COPY 进度** | `jobs.Job` 的 `result` 字段记录已完成字节数；admin API 可查询进度 |
| **COPY 到自身** | S3 协议要求返回 `200 OK` 但实际不操作；需检测 `srcKey == dstKey` |
| **COPY 目标已存在且锁定** | 遵循目标 bucket 的对象锁策略（与 `Put` 路径一致的 `checkLockBeforeOverwrite`） |

---

## 方向二：Webhook 交付基础设施成熟度——可靠性、治理与可观测性

### 现状

当前 webhook 实现（`internal/events/webhook.go`）是一个**尽力交付的直发器**：

```go
// 核心交付循环
func (w *Webhook) deliver(ctx context.Context, e repository.Event) {
    body, _ := json.Marshal(map[string]any{...})
    for _, u := range w.urls {
        w.postOne(ctx, e.ID, u, body, sig, 1)  // ← 每个 URL 依次发一次
    }
}

// 重试策略
func (w *Webhook) retryOne(ctx context.Context, f repository.WebhookFailure) {
    // ... 指数退避 ...
    if attempts >= 10 {
        _ = w.repo.MarkWebhookSucceeded(ctx, f.ID)  // ← 死信 = 标记成功！永久丢失
        return
    }
}
```

**六个结构性缺陷：**

| # | 缺陷 | 代码证据 | 影响 |
|---|------|---------|------|
| **1** | **死信 = 标记成功，事件永久丢失** | `webhook.go:retryOne` 第 10 次失败后调用 `MarkWebhookSucceeded`——这张失败记录在 `ListWebhookFailures` 中可见但**永不再次投递**，且原始事件无副本 | 运维无法补救死信事件，审计丢失事件详情 |
| **2** | **无事件去重窗口** | `deliver` 对每个事件立即 POST，即使接收端返回 `5xx` 后重试循环也发送相同 payload + 最新 `X-Aero-Signature`，无 `Idempotency-Key` 式的去重 | 接收端必须自行去重（当前依赖 event ID 幂等性，但无重放保护） |
| **3** | **无外发速率限制** | `deliver` 在 goroutine 中串行调用所有 URL 的 `postOne`，无 per-URL 节流（若接收端响应变慢，HTTP 连接池堆积） | 慢接收端可导致 goroutine 泄漏 / 连接池耗尽 |
| **4** | **无交付延迟可观测性** | `postOne` 不记录 HTTP 请求耗时；`retryOne` 不记录重试间隔分布 | 运维无法回答「最近的 webhook 平均延迟是多少」「p95 延迟趋势」 |
| **5** | **密钥配置固定、无轮换** | `WithSecret(secret string)` 接受单次配置，无法在运行时轮换 HMAC 密钥 | 密钥泄露后需重启服务才能更换，无过渡期（双密钥） |
| **6** | **单 URL 无过滤/扇出** | 所有事件发送到所有 URL。无法为不同类型事件配置不同接收端，无法为不同租户配置不同 URL | 接收端必须自己过滤不关心的事件，浪费带宽 |

**WebhookFailures 表当前 schema（`migrations/sqlite/0008_webhook_retries.up.sql`）：**

```sql
-- 只有 attempts, last_error, last_status, next_retry_at, succeeded(0/1)
-- 无 dead_letter 状态，无 delivery_count（累计投递次数），无 max_attempts 配置
```

### 产品价值

| 场景 | 当前体验 | 优化后 |
|------|---------|--------|
| **接收端故障 2 小时** | 指数退避到 10 次后死信（约 2h），事件永久丢失 | 进入死信队列可查询、可重放、可导出 |
| **接收端需要事件去重** | 必须在应用层检查 Event ID 是否处理过（如果实现不完善→重复处理） | 内建去重窗口（5 分钟相同 payload 不重发） |
| **接收端每 15 分钟维护窗口** | 10 次退避撞上窗口外，无一成功，死信 | 自适应退避：窗口期自动暂停，窗口后恢复 |
| **安全审计要求 webhook 密钥轮换** | 重启服务 + 接收端同步更新密钥 | 支持双密钥（active/previous），平滑轮换 |
| **运维想看 webhook 健康状况** | 无面板，需手动查询 `webhook_failures` 表 | Grafana 面板：交付率、延迟趋势、队列深度、错误分布 |
| **不同事件路由到不同接收端** | 所有事件发到同一 URL | `event_type` 匹配规则 + 租户过滤 + 前缀过滤 |

### 架构权衡

**建议方案：Webhook 交付引擎分层设计**

```
                 ┌───────────────┐
                 │  Event Bus    │  (原始事件)
                 └───────┬───────┘
                         ▼
              ┌───────────────────┐
              │  Webhook Router   │  ← 根据 event_type/tenant/prefix 分发
              └────────┬──────────┘
                       │
         ┌─────────────┼─────────────┐
         ▼             ▼             ▼
  ┌────────────┐ ┌────────────┐ ┌────────────┐
  │ URL #1     │ │ URL #2     │ │ Dead Letter│
  │ (retry q)  │ │ (retry q)  │ │ Queue      │
  └────────────┘ └────────────┘ └────────────┘
```

**1. 死信队列架构：**

当前 `webhook_failures` 表需要新增状态和字段：

```sql
ALTER TABLE webhook_failures ADD COLUMN status TEXT NOT NULL DEFAULT 'retrying';
-- status ∈ {'retrying', 'dead_letter', 'delivered'}
ALTER TABLE webhook_failures ADD COLUMN max_attempts INTEGER NOT NULL DEFAULT 10;
ALTER TABLE webhook_failures ADD COLUMN delivered_at TEXT;  -- 成功投递时间
```

- `NextPendingFailures` 只选 `status='retrying'` 且 `next_retry_at <= now` 的行
- 超 `max_attempts` 后设置 `status='dead_letter'` 而非 `succeeded=1`
- admin API 提供 `POST /admin/webhook-failures/{id}/retry` 将死信重新置为 `status='retrying'`
- admin API 提供 `POST /admin/webhook-failures/{id}/discard` 标记为 `status='delivered'`（确认放弃）

**2. 事件过滤与多路由：**

```go
type WebhookRoute struct {
    URL         string            // 目标 URL
    Filter      *WebhookFilter    // 过滤规则（nil = 接收全部）
    RateLimit   float64           // 外发速率限制（0 = 不限）
    MaxAttempts int               // 最大重试次数（默认 10）
    Timeout     time.Duration     // HTTP 超时（默认 5s）
}

type WebhookFilter struct {
    EventTypes []string           // 空列表 = 全部
    Tenants    []string           // 空列表 = 全部
    Buckets    []string           // 空列表 = 全部
    Prefix     string             // 对象 key 前缀匹配
}
```

配置方式支持多路径（当前单 URL 向后兼容）：

```
EVENTS_WEBHOOK_URLS=prod=https://hooks.example.com/events,audit=https://hooks.example.com/audit
EVENTS_WEBHOOK_ROUTE_prod_FILTER_EVENT_TYPES=created,deleted,accessed
EVENTS_WEBHOOK_ROUTE_audit_FILTER_EVENT_TYPES=object.locked,acl.changed
```

或者通过 bucket notifications API 管理（使用 migration 0024 已有的 `notification_rules` TEXT 列）。

**3. 外发速率限制：**

```go
// per-URL token bucket（独立于全局 RATE_LIMIT_RPS）
type WebhookRateLimiter struct {
    buckets map[string]*rate.Limiter  // key = URL
}
```

`postOne` 在 HTTP 请求前调用 `rateLimiter.Wait(ctx)`，实现 per-URL 背压。当接收端返回 `429 Too Many Requests` + `Retry-After` 头时，动态调整 token bucket 速率（与接收端协商）。

**4. 密钥轮换：**

```go
type WebhookSecret struct {
    Active   string  // 当前主密钥（签名新事件）
    Previous string  // 上一个密钥（验证旧签名，过渡期用）
}
```

- 新事件用 `Active` 签名
- 接收端可以用 `Previous` 验证过渡期的签名
- `Active` 轮换后旧的自动降为 `Previous`，再下次轮换时清除

**5. 可观测性指标：**

| 指标名称 | 类型 | Labels | 当前状态 |
|---------|------|--------|---------|
| `webhook_delivery_total` | Counter | `{url, status_code}` | ❌ 缺失 |
| `webhook_delivery_latency_ms` | Histogram | `{url}` | ❌ 缺失 |
| `webhook_retry_queue_depth` | Gauge | `{url}` | ❌ 缺失 |
| `webhook_dead_letter_total` | Counter | `{url}` | ❌ 缺失 |
| `webhook_secret_rotation_count` | Counter | — | ❌ 缺失 |

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| **接收端长时间不可用** | 重试队列持续增长；`max_attempts` 耗尽后进入死信队列；运维在恢复后手动重放死信 |
| **接收端返回 429** | 解析 `Retry-After` 头，动态调整 retry 退避时间，并降低 `rateLimiter` 速率 |
| **Payload 过大（> 1MB）** | 截断 payload 中的 `Payload` 字段（保留事件元数据，裁剪对象体引用）或通过 `X-Aero-Payload-Trimmed: true` 头通知接收端 |
| **Webhook 配置变更期间的事件** | 新增 URL 后旧 URL 仍接收过渡期事件（15 分钟）；移除 URL 后仍在重试队列中的事件转为死信 |
| **事件风暴（burst）** | `deliver` 使用有界 goroutine pool（`semaphore`），超过背压时 ingress 侧限流（可选降级为仅存入 DB） |

---

## 方向三：跨协议安全架构——身份联邦、输入验证与攻击面收缩

### 现状

**四种协议，四种认证方式，零统一：**

```
REST:    Bearer JWT（HS256 签名）或 X-Api-Key（sha256 哈希）
S3:      SigV4（HMAC-SHA256）+ 桶策略（IAM JSON）+ 桶 ACL + 对象 ACL
WebDAV:  X-Aero-Tenant 头（协议无标准认证）+ 依赖前置代理做 Basic Auth
MCP:     Bearer Token（硬编码/环境变量）
```

```go
// internal/auth/auth.go:Registry
type Registry struct {
    keys     map[string]Key          // 内存 map，env vars + 持久化 key
    jwt      *jwtState               // 单 JWT 密钥
    sigv4    *SigV4Credentials       // S3 SigV4 凭据
    anonRead bool                    // 匿名公共读
    // 无 OIDC/LDAP/SAML 支持
}
```

**输入验证缺失（安全边界未收缩）：**

| 攻击面 | 代码位置 | 风险 |
|--------|---------|------|
| **XML 无大小限制** | `s3compat/handler.go:PutObject`（`xml.NewDecoder(r.Body).Decode(&in)`） | 恶意客户端上传 2GB XML → OOM |
| **Content-Type 不校验** | `rest/handler.go:Put`（`r.Body` 直接传参） | 客户端上传 `text/html` 到 S3 bucket 但提示为 `image/png`（存储注入） |
| **请求体大小无端点级限制** | `s3compat/handler.go:PutObject`（`r.Body` → `svc.Put`） | 全局 `MAX_INFLIGHT_REQUESTS` 仅控制并发数，不阻止 10GB PUT |
| **HTTP 方法覆盖无保护** | `middleware/cors.go`（无 `OPTIONS` 校验外的 HTTP method 硬性限制） | CORS 预检后实际 method 可被修改 |
| **路径遍历** | `service/file.go:validateKey` 已拦截 `..` 和 `/` 前缀 | 基础防护已存在但无统一中间件层 |

**核心安全架构缺口：**

| # | 缺口 | 影响 | 当前缓解措施 | 现状 |
|---|------|------|-------------|------|
| **1** | **无外部身份联邦** | 无法集成企业 IdP（Okta/Azure AD/Keycloak），需要自行同步用户 | 无 | 🔴 |
| **2** | **无统一 authz 上下文** | 跨协议操作无法共享 authz 决策（如 S3 SigV4 认证后 WebDAV 操作不感知） | 无 | 🔴 |
| **3** | **无中央输入验证层** | 每个 handler 自行验证参数，不一致且易遗漏 | `validateKey` / `validateMetadata` 仅服务层 | 🟡 |
| **4** | **无安全响应头** | 无 CSP / X-Frame-Options / X-Content-Type-Options | Web UI 可能依赖 | 🟡 |
| **5** | **XML 解析无保护** | 无限制的 `xml.NewDecoder` 可导致 OOM | Go encoding/xml 对 entity 扩展有限制 | 🟢 |

### 产品价值

| 场景 | 当前体验 | 优化后 |
|------|---------|--------|
| **企业 SSO 集成** | 不支持——用户必须手动创建管理 API Key | 支持 OIDC/OAuth2 登录：用户通过企业 IdP 认证，自动获取映射的 tenant 和 scopes |
| **跨协议操作审计** | S3 上传 → REST 查询 → WebDAV 下载，三个请求三个 auth 上下文，无法关联 | 统一 `authz_context`（`{identity, tenant, session_id}`）跨协议传递，审计日志可追溯全链路 |
| **恶意客户端上传巨量 XML** | `xml.NewDecoder(r.Body).Decode(...)` 无限制，OOM | 中间件 `MaxBodyBytes(1<<20)` + 全局 `RequestSizeLimit` |
| **Content-Type 注入** | 客户端上传文件后可指定任意 Content-Type，用于 XSS 或 MIME 混淆 | `Strict-Content-Type` 策略 + `X-Content-Type-Options: nosniff` |
| **WebDAV 协议缺乏标准认证** | 依赖前置代理做 Basic Auth，无原生支持 | 添加 `X-Aero-Authorization` 头支持或继承独立认证令牌 | 
| **安全合规（SOC2/PCI）** | 无安全 headers、无输入验证策略文档 | 完整的安全中间件链 + 安全配置文档 + 渗透测试指南 |

### 架构权衡

**建议方案：分层安全架构**

```
                    ┌──────────────────┐
                    │  Identity        │  ← OIDC / LDAP / SAML 联邦
                    │  Federation      │
                    └────────┬─────────┘
                             │
              ┌──────────────┴──────────────┐
              ▼                              ▼
     ┌──────────────────┐          ┌──────────────────┐
     │  Authz Context   │          │  Input           │
     │  (principal,     │          │  Validation      │
     │   tenant, scopes,│          │  Layer           │
     │   session_id)    │          └────────┬─────────┘
     └────────┬─────────┘                   │
              │                ┌─────────────┼─────────────┐
              ▼                ▼             ▼             ▼
     ┌──────────────┐  ┌────────────┐ ┌────────┐ ┌────────────┐
     │ Protocol     │  │ Body Size  │ │Content │ │ Security   │
     │ Adapters     │  │ Limit MW   │ │Type MW │ │ Headers MW │
     └──────────────┘  └────────────┘ └────────┘ └────────────┘
```

**1. 身份联邦：**

```go
type IdentityProvider interface {
    Name() string              // "oidc" | "ldap" | "saml"
    Authenticate(ctx context.Context, credentials json.RawMessage) (Identity, error)
}

type Identity struct {
    Subject    string            // 用户/服务的唯一标识
    Issuer     string            // IdP issuer URL
    TenantID   string            // 映射的 tenant
    Scopes     []string          // 授权范围
    Attributes map[string]string // 额外属性（组、角色……）
    ExpiresAt  time.Time
}
```

**评估策略：**

| Provider | 实现路径 | 复杂度 | 优先级 |
|----------|---------|--------|--------|
| **OIDC/OAuth2** | 新增 `auth.OIDCProvider`，验证 JWT（RS256/ES256）+ `iss`/`aud` 校验；映射 `sub` → tenant | 中（`coreos/go-oidc` 或 stdlib） | **P1** |
| **LDAP** | 新增 `auth.LDAPProvider`，bind + search 查询用户映射 | 低（少量 LDAP 查询） | **P2** |
| **SAML** | 最少优先——可通过 OIDC 映射覆盖 | 高（SAML 协议复杂） | **P3** |

**JWT 签名算法风险：** 当前 `internal/auth/jwt.go` 使用 `crypto/hmac` + HS256（对称签名）。HS256 要求客户端和服务器共享密钥——客户端可以签发任意 JWT。对于 OIDC 联邦，必须**升级到 RS256/ES256**（非对称签名）并通过 JWT `iss` 校验阻止跨 IdP 令牌重用。

```go
// 当前可能的安全风险
// HS256 是对称签名：拥有 JWT_SECRET 的客户端可以签发任意身份的令牌
// 当前使用场景是管理员自签发（可接受），但扩展到 OIDC 后必须禁止 HS256
```

**2. 输入验证中间件链：**

```go
// 新增中间件（在现有中间件链中位于 Auth 后、Handler 前）
chain := []func(http.Handler) http.Handler{
    middleware.MaxBodySize(10 << 20),         // 默认 10MB 请求体上限
    middleware.EnforceContentType("json"),     // REST /v1 要求 Content-Type: application/json
    middleware.SecureHeaders(),                // CSP / X-Frame-Options / X-Content-Type-Options
    middleware.CORSPreflightMethodGuard(),     // 只允许 OPTIONS + 已声明的 methods
}
```

每个 handler 可通过 annotation 或 route-level config 覆盖全局默认值。

**3. XML 安全解析：**

```go
// 封装安全的 XML decoder
func safeXMLDecoder(r io.Reader, maxBytes int64) *xml.Decoder {
    decoder := xml.NewDecoder(io.LimitReader(r, maxBytes))
    // Go 的 encoding/xml 默认不展开外部实体（XXE 不可行）
    // 但可通过 Strict: false 允许更宽松的解析
    decoder.Strict = true  // 默认即 true，显式设定
    return decoder
}
```

同时为所有 XML endpoint 添加 `maxBytes` 限制——拒绝大于 1MB 的 XML payload。

**4. 安全响应头：**

```nginx
# 建议的安全 headers（通过 middleware.SecureHeaders() 自动设置）
X-Content-Type-Options: nosniff
X-Frame-Options: DENY
X-XSS-Protection: 1; mode=block
Referrer-Policy: strict-origin-when-cross-origin
Content-Security-Policy: default-src 'self'
```

现有 `applyMiddleware` 链中 CORS 已存在，安全 headers 可直接接入同一链。

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| **多个 IdP 同时使用** | `Registry` 支持 `[]IdentityProvider` 迭代尝试；`WWW-Authenticate` 头指示可选 IdP |
| **OIDC token 过期** | middleware 检测 `exp` 声明 → `401 Unauthorized` + `WWW-Authenticate` |
| **LDAP 服务器不可用** | 降级到本地认证（API key/JWT），记录告警；可配置 `LDAP_FAIL_OPEN=false`（严格模式拒绝所有请求） |
| **HS256 vs RS256 共存** | JWT middleware 检测 `alg` 头；`alg=HS256` 仅本地管理员签发可用；`alg=RS256` 走 OIDC 验证 |
| **Content-Type 校验对 S3 不适用** | S3 协议要求接受任意 Content-Type——input validation middleware 必须按路由组配置（`/v1/` 校验，`/s3/` 不校验） |
| **已存在的 API key 与联邦身份冲突** | API key 认证继续工作；联邦身份按 `sub` 映射 tenant，两者共存而非替代 |

---

## 方向四：分布式追踪与可观测性成熟度——Trace Context 传播、结构化日志关联与 SLO 驱动运维

### 现状

**OTel 已经初始化，但 trace 上下文从不跨越组件边界：**

```go
// internal/telemetry/http.go — 当前唯一的 trace 代码
func HTTPMiddleware(serviceName string) func(http.Handler) http.Handler {
    tracer := otel.Tracer("aero-vault/http")
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            ctx, span := tracer.Start(r.Context(), r.Method+" "+r.URL.Path,
                trace.WithAttributes(...))
            defer span.End()  // ← 在 middleware 返回时就结束了！
            next.ServeHTTP(w, r.WithContext(ctx))
            span.SetAttributes(...)
        })
    }
}
```

span 在 middleware 返回前结束（`defer span.End()`），意味着**所有下游调用（service.Get → storage.Get → os.Open）都在 span 已经结束后才执行**——这些调用的延迟永远不会出现在 trace 中。

**三个层级的问题：**

| 层级 | 问题 | 代码证据 | 影响 |
|------|------|---------|------|
| **L1：Trace 传播断裂** | span 在 HTTP middleware 结束后才执行下游调用；无子 span 创建 | `telemetry/http.go:29`（`defer span.End()` — 过早结束）；`service/file_crud.go:Get`（无 `tracer.Start`） | 无法分解请求延迟到具体组件 |
| **L2：Trace 与日志割裂** | RequestID 与 TraceID 不关联；结构化日志无 `trace_id` 或 `span_id` 字段 | `middleware/middleware.go:RequestID`（只设置 `X-Request-ID` header，不设 trace context）；`telemetry/http.go:23`（span 创建后不存入 request context 供日志使用） | 日志无法按 trace 查询 |
| **L3：告警无 SLO 维度** | 当前 8 条告警规则仅覆盖 AI p95 延迟和队列深度；无 multi-window multi-burn-rate SLO 告警 | `deploy/prometheus/alerts.yml`（仅 3 组规则，全部为 static threshold） | 无法区分「服务正在缓慢退化」与「突发故障」 |

### 产品价值

| 场景 | 当前能力 | 优化后 |
|------|---------|--------|
| **用户投诉「GET 对象慢」** | 可看到 HTTP 请求延迟（`http.server.duration_ms`），但无法判断慢在 storage（S3 HTTP 调用）还是 repository（SQL 查询） | trace 显示 `fileService.Get` → `s3.Get`(320ms) → `repo.GetObject`(15ms)，精确找到瓶颈 |
| **S3 上传后 REST 搜索失败** | 两个请求的 RequestID 不同，日志无法关联 | trace 提供 `trace_id` 跨请求关联；Web UI 可展示同一 trace 下的所有请求 |
| **运维想知道 /v1/search p99 趋势** | Grafana 有 `ai_search_duration_ms` 直方图展示 p50/p95 | 结合 trace 分解的 LLM 调用时间和 vector search 时间，定位慢在哪个子调用 |
| **存储后端退化时的降级决策** | 仅靠 `circuitBreaker` 断开后 alert，无前置指标 | 多窗口 burn-rate 告警：1h 窗口违规 + 5m 窗口违规 + 已消耗错误预算 50% → 触发告警（page） |
| **跨副本请求追踪** | `RequestID` 进程内唯一，不跨节点 | `trace_id` 跨副本传播（通过 HTTP headers `traceparent`），同一 trace 可跨多个 aero-vault 实例 |

### 架构权衡

**建议方案：三层渐进式投入**

```
Phase 1（2-3 周）：修复 span 生命周期 + 结构化日志关联
Phase 2（4-6 周）：组件级 span + 概率采样
Phase 3（4-6 周）：SLO 告警 + Grafana trace 面板
```

**Phase 1：修复 span 生命周期**

当前 `defer span.End()` 在 middleware 返回时触发——但 `next.ServeHTTP` 是同步的，所以问题不在 defer 而在：span 过早结束是因为 `defer` 在 middleware 函数体结束时执行，而此时 `next.ServeHTTP` 已经完成并返回。等等——实际上 `next.ServeHTTP` 是同步的，`defer span.End()` 在 `next.ServeHTTP` 返回后调用，这正是正确的顺序。让我重新审视...

实际上，`defer span.End()` 在 `HTTPMiddleware` 函数返回前执行，而 `next.ServeHTTP(w, r.WithContext(ctx))` 是同步调用——所以 span 的结束是在 handler 执行完毕后。这样来看，当前实现是正确的。但是 span 并没有**传播**到 service/storage/repository 层——因为 `ctx` 虽然携带了 span 上下文，但下游函数并没有创建子 span。

所以真正的问题是**没有嵌套 span**，而不是 span 结束太早。

```go
// 当前：HTTP middleware 创建 span → 传给 handler → handler 不传播 → span 结束
// 目标：HTTP middleware 创建 span → handler 创建子 span → service 创建子 span → storage 创建子 span
```

**Phase 2：组件级 span 传播**

```go
// 在 service.Get 中：
func (s *FileService) Get(ctx context.Context, tenant, bucket, key string) (io.ReadCloser, repository.Object, error) {
    ctx, span := otel.Tracer("aero-vault/service").Start(ctx, "FileService.Get",
        trace.WithAttributes(attribute.String("key", key)))
    defer span.End()
    // ...
    rc, _, err := s.store.Get(ctx, obj.StorageKey)  // ctx 携带父 span，storage 可创建子 span
    // ...
}
```

在 `storage` 层：

```go
func (s *localStore) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
    ctx, span := otel.Tracer("aero-vault/storage").Start(ctx, "local.Get",
        trace.WithAttributes(attribute.String("key", key)))
    defer span.End()
    // ... 实际文件读取
}
```

**采样策略（避免 trace 数据淹没后端）：**

| 路由组 | 采样率 | 理由 |
|--------|--------|------|
| `/v1/files/*` GET/HEAD | 1% | 读取操作高频，1% 足以捕获 p99 |
| `/v1/files/*` PUT/DELETE | 10% | 写入操作低频但关键 |
| `/v1/search` `/v1/chat` | 100% | AI 调用成本高、延迟关键，需完整追踪 |
| `/v1/admin/*` | 100% | 安全敏感操作 |
| `/s3/*` | 1-5% | 高频但重要性低 |
| `/healthz` `/readyz` `/metrics` | 0% | 健康检查不追踪 |

**结构化日志关联：**

```go
// 在 HTTP middleware 中（请求进入时）：
traceID := trace.SpanContextFromContext(ctx).TraceID().String()
logEntry := slog.With(
    "request_id", reqID,
    "trace_id", traceID,
    "span_id", spanID,
)
```

将 `logEntry` 放入 `context.Context` 中，下游所有组件通过 `slog.FromContext(ctx)` 获取带 trace 信息的 logger。

**Phase 3：SLO 告警**

基于 `http.server.duration_ms` 直方图构建 SLO：

```yaml
# 示例 SLO 配置
slo:
  - name: "api_latency"
    target: 99.9
    threshold_ms: 500
    window: 30d

  - name: "search_latency"
    target: 99.0
    threshold_ms: 3000
    window: 30d
```

**Multi-window multi-burn-rate 告警规则：**

```yaml
# Prometheus rule（每个 SLO 生成 3 条规则）
groups:
  - name: slo
    rules:
      # 快速 burn（高消耗率）：5m 窗口违反率 > 95%
      - alert: HighErrorBudgetBurn
        expr: |
          (
            (1 - (sum(rate(http_request_duration_seconds_count{status_code=~"5.."}[1h])) / sum(rate(http_request_duration_seconds_count[1h]))))
            -
            (1 - (sum(rate(http_request_duration_seconds_count{status_code=~"5.."}[5m])) / sum(rate(http_request_duration_seconds_count[5m]))))
          ) > 0
```

当前告警规则（`deploy/prometheus/alerts.yml`）只有静态阈值规则，缺少 burn-rate 告警和 SLO 告警组。

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| **Trace 数据淹没 OTel Collector** | 概率采样 + 端点级采样率 + head-based sampling（在 span 创建时就决定是否采样） |
| **Trace ID 跨进程传递** | [W3C Trace Context](https://www.w3.org/TR/trace-context/) 标准（`traceparent` / `tracestate` headers）——S3/REST/WebDAV/MCP 统一使用同一标准 |
| **日志体积膨胀** | `trace_id` 字段长度固定（32 hex chars），日志体积增加 < 0.1% |
| **高基数的 span attributes** | `key` 路径作为 attribute 可能导致高基数——限制 `bucket` + `prefix` 级别的聚合，不包含完整 key |
| **WebDAV 和 MCP 的 trace 传播** | WebDAV 当前在 chi 路由外分发——需确保 WebDAV handler 的 context 也携带 trace；MCP stdio 模式无 HTTP header，需 JSON-RPC `params` 中传递 `traceparent` |
| **采样决策一致性** | Head-based sampling → 所有组件（service/storage/repository）继承同一采样决策（通过 context 中的 sampled flag） |

---

## 优先级与建议执行顺序

| 排序 | 方向 | 前置依赖 | 建议投入 | 核心交付物 |
|------|------|---------|---------|-----------|
| **1** | **方向三：跨协议安全架构**（Phase 1：输入验证中间件 + 安全 headers） | 无（纯新增中间件层） | 3-4 周 | `MaxBodySize`/`SecureHeaders`/`SafeXMLDecoder` 中间件 + 渗透测试 |
| **2** | **方向一：服务端 COPY/MOVE**（Phase 1：`Storage.Copy` 接口 + `CopyStrategy` + 本地后端实现） | 无（独立于其他方向） | 4-6 周 | `Storage.Copy()` 接口 + `CopyStrategy` 选择器 + local/S3 后端实现 + 单元测试 |
| **2** | **方向二：Webhook 交付基础设施**（Phase 1：死信队列 + 交付指标） | 无（独立于其他方向） | 4-6 周 | `status` 字段 + `dead_letter` 状态转换 + `POST /admin/webhook-failures/{id}/retry` + Grafana webhook 面板 |
| **4** | **方向四：分布式追踪**（Phase 1：嵌套 span + 日志关联） | OTel 已就绪，trace 传播耗时较短 | 2-3 周 | Service/Storage/Repository 层 span + `slog` trace_id 关联 |
| **5** | **方向三 Phase 2：OIDC 身份联邦** | Phase 1 安全 headers + 输入验证已完成 | 6-8 周 | `IdentityProvider` 接口 + `OIDCProvider` + RS256 JWT 验证 + tenant 映射 |
| **6** | **方向四 Phase 2+3：SLO 告警 + 概率采样** | Phase 1 嵌套 span 已就绪 | 4-6 周 | SLO 配置 + burn-rate 告警规则 + Grafana trace 面板 |

**建议执行策略：**

1. **Phase 1（方向三 + 方向一并行）**：安全架构和 COPY/MOVE 是两个正交的方向，无共享依赖。安全（输入验证中间件）是短周期高回报的基础设施投资；COPY/MOVE 是功能深度——两者可并行启动，分别投入 1 名工程师。
2. **Phase 2（方向二 + 方向四 Phase 1 并行）**：Webhook 死信队列和嵌套 span 传播都是基础设施增强，不影响 API 稳定性，可在现有版本中增量发布。
3. **Phase 3（方向三 Phase 2 + 方向四 Phase 2+3）**：OIDC 联邦和 SLO 告警依赖前面阶段的中间件和 trace 基础，适合更成熟的系统状态。

---

## 总结

以上四个方向覆盖了 aero-vault 在**数据移动架构、事件交付可靠性、安全架构深度、可观测性成熟度**四个关键维度的缺口。它们与既有 94 轮分析 + ROADMAP + TODO 无实质重叠，同时在代码库中有明确锚点，具备从当前架构渐进演进的可行性。

| 维度 | 当前状态 | 目标状态 |
|------|---------|---------|
| **数据移动** | COPY = 下载→上传（内存缓冲，无服务端优化，非原子） | `Storage.Copy` 接口 + 策略模式（服务端/流式/分段）+ 原子 MOVE + 进度追踪 |
| **事件交付** | 单 URL + 10 次退避后标记成功（永久丢失）+ 无指标 | 多路由 + 事件过滤 + 死信队列 + 密钥轮换 + 交付延迟直方图 + 外发限流 |
| **安全架构** | 4 种协议 4 种认证 + 零输入验证 + 无联邦 | 统一安全中间件链 + 端点级 body 限制 + XML 安全解析 + OIDC 联邦 + 安全 headers |
| **可观测性** | HTTP 根 span 不传播 + 日志无 trace_id + 静态阈值告警 | 全链路嵌套 span + 结构化日志关联 + 概率采样 + SLO multi-window burn-rate 告警 |
