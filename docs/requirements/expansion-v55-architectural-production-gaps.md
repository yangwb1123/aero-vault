# AeroVault 高价值扩展方向 v55 — 架构级生产就绪度与协议纵深缺口

> **分析范围：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 全部 23 个子包，~55K `.go` + 三套 SDK + `deploy/*` + 24 对迁移文件 + 全部 54 份既有 `docs/requirements/expansion-*.md` 分析文档 + `ROADMAP.md` + `CHANGELOG.md` + `TODO.md` + `docs/architecture.md` + `docs/configuration.md` + `AGENTS.md` + `HARNESS.md`）
>
> **分析视角：** 资深架构师 / 产品经理 — 在累计 **54 期 expansion 分析（270+ 方向，~850,000+ 字分析文本）** 基础上，寻找 **54 轮穷举后依然未被触及** 的架构级生产就绪度缺口
>
> **去重方法：** 对 `docs/requirements/` 下全部 54 份既有分析文档 + `ROADMAP.md` + `CHANGELOG.md` + `TODO.md` + `extensions*.md` + `analysis-*.md` 进行穷尽式关键词验证与方向级交叉引用。每个方向在既有文档中 **零实质性独立架构分析**（即：不作为独立方向/独立小节出现；仅表格一行过路引用、举例提及、单一子点均不构成实质性分析）。
>
> **分析日期：** 2026-07-10

---

## 前言

经 54 期、270+ 方向的穷举分析，AeroVault 从功能实现广度、AI/RAG 管线、S3 协议纵深、存储引擎、认证安全、多租户、事件系统、操作完整性、产品成熟度、运维就绪度、系统工程质量、跨协议一致性等维度已被反复扫描。几乎每个可想象的功能方向都被触及。

然而，在第 55 轮对代码库的逐文件深层扫描中，依然有 **5 个方向** 从未被作为独立架构方向实质性触及。它们的共同特征是：

1. **不是"加新端点"，而是"已有架构中缺失的关键安全/可靠/运维保障环节"**
2. **涉及组件间交互边界的盲区——非功能本身，而是功能之间的连接层**
3. **每个方向在当前代码库中都有明确、可定位的代码证据锚点**
4. **每一条在 v1–v54 中零实质性独立架构分析**

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心矛盾 | 代码锚点 | 54 期覆盖验证 |
|---|------|------|--------|---------|---------|-------------|
| **1** | **配额预检原子性缺失与 TOCTOU 竞态（Quota Enforcement TOCTOU Race）** | 数据完整性/可靠性 | **P1** — `preflightQuota` 在写入前检查配额，但 `AddTenantUsage` 在写入后调用；并发写入可在检查与提交之间消耗同片余额，导致配额超额 | `internal/service/file_crud.go:48-53`（`preflightQuota` 检查）；`internal/service/file_crud.go:155`（`AddTenantUsage` 提交） | ❌ **零覆盖**（v27 方向二一行路过提及 ENOSPC 的 TOCTOU 但聚焦磁盘空间，非租户配额；其余 53 份文档零命中 "quota+race/TOCTOU"） |
| **2** | **WebDAV 协议绕过全链路中间件防护（WebDAV Middleware Chain Bypass）** | 安全/运维 | **P1** — WebDAV Handler 在 `buidDispatcher` 中先于 chi 路由树分发，完全跳过 auth、rate limiting、concurrency limiter、CORS、OTel、access log、request ID 七层中间件 | `cmd/server/main.go:164-175`（`buildDispatcher` 优先路由 WebDAV）；`internal/middleware/middleware.go`（中间件链） | ❌ **零实质性分析**（v40 方向表两行分别提及 WebDAV 无 Bearer token 通道和认证适配器概念——聚焦 auth 本身，**非全链路中间件绕过分析**；v44 方向表一行路过提及 WebDAV 不参与 idempotency——**与安全绕过无关**；其余 52 份文档零与此相关） |
| **3** | **存储后端可观测性真空：Circuit Breaker 状态与后端健康指标裸露（Storage Backend Observability Vacuum）** | 运维/可靠性 | **P1** — `circuitBreaker` 内维护完整的状态机（open/closed/half-open）和滑动窗口统计（`Stats()` 方法），但**零指标暴露**到 Prometheus / OTel；全局 15+ domain metrics 无一覆盖存储后端延迟、健康状态、剩余容量 | `internal/storage/circuitbreaker.go:77-84`（`Stats()` 返回 state/failures/total 但无注册）；`internal/storage/circuitbreaker.go:83`（`tryTransition` 内部调度但零 instrumentation）；`internal/telemetry/metrics.go`（15 组计数器/直方图，**零存储后端指标**） | ❌ **零实质性分析**（v11 方向三的指标规划表格中一行路过列出 `storage.backend_latency_ms{backend}` 概念——**仅一行表格占位，无架构分析、无实施路径、无代码锚点引用**；其余 53 份文档零覆盖此方向） |
| **4** | **进程内事件总线订阅者健康管理缺失（In-Process Event Bus Subscriber Health Management）** | 可靠性/运维 | **P2** — `Bus.Subscribe` 创建无限数量的匿名 channel，无注册/注销机制、无订阅者健康检测、无慢消费告警、无积压深度监控；慢订阅者事件静默丢弃（仅 `dropped` 原子计数器累计），运维人员无法识别哪个订阅者异常 | `internal/events/bus.go:72-80`（`Subscribe` 仅 append channel 到 slice）；`internal/events/bus.go:107-118`（`broadcast` 非阻塞发送，满 buffer 丢弃）；`internal/events/bus.go:31`（`dropped` 无标签计数器——无法按订阅者分类） | ❌ **零覆盖**（v44 方向五覆盖 SSE replay 回放分页与 webhook 死信审计——聚焦**外部事件消费端**的交付保障；v11 方向表一行列出 `event.subscriber_depth` gauge 概念但**零架构分析**。进程内订阅者健康管理——订阅者注册/注销/监控/告警——从未被分析） |
| **5** | **S3 虚拟主机风格请求路由缺失（Virtual-Hosted Style S3 Request Routing Gap）** | S3 协议兼容性/采纳 | **P2** — 当前 S3 协议仅支持 `path-style`（`/s3/{bucket}/{key}`），不支持 `virtual-hosted`（`http://{bucket}.{host}/{key}`）；主流 S3 SDK（aws-sdk-js v3, boto3）默认使用 virtual-hosted 风格，导致开箱即用失败 | `internal/api/s3compat/router.go`（仅注册 `/{bucket}/*` 路径风格路由）；`cmd/server/main.go:129`（`r.Mount(cfg.S3Compat.Prefix, s3compat.NewRouter(...))`——单一路径挂载）；`S3_COMPAT_PREFIX=/s3`（env 默认注册 path-style 前缀） | ❌ **零覆盖**（54 份文档中无任何一篇以独立方向分析 virtual-hosted style 支持；v42 方向三覆盖 S3 子资源完备性但聚焦 REST 端点而非请求路由风格） |

---

## 方向一：配额预检原子性缺失与 TOCTOU 竞态（Quota Enforcement TOCTOU Race）

### 现状

当前配额检查与扣减流程为：

```go
// internal/service/file_crud.go — Put 方法简化流程
func (s *FileService) Put(ctx context.Context, ...) (repository.Object, error) {
    // 1. 预检配额（检查时点 T1）
    if err := s.preflightQuota(ctx, tenant, size, 1); err != nil {
        return repository.Object{}, err      // ← 超额时拒绝
    }

    // 2. 写入存储后端（耗时操作，云后端可达 100ms-5s）
    info, err := s.store.Put(ctx, sk, reader, size, ...)

    // 3. 写入元数据仓库
    saved, err := s.repo.UpsertObject(ctx, obj)

    // 4. 扣减配额（提交时点 T2）
    if _, qErr := s.repo.AddTenantUsage(ctx, obj.TenantID, saved.Size, 1); qErr != nil {
        s.logger.Warn("quota usage increment failed", ...)  // ← 仅 warn，不阻断
    }
}
```

**竞态窗口分析：**

```
时间线：
T1: preflightQuota 检查: UsedBytes=900MB, MaxBytes=1GB → 通过（还有 100MB）
T1': 并发请求 B 的 preflightQuota: UsedBytes=900MB, MaxBytes=1GB → 通过（还有 100MB）
T2: 请求 A 写入 80MB → AddTenantUsage(80MB) → UsedBytes=980MB
T2': 并发请求 B 写入 80MB → AddTenantUsage(80MB) → UsedBytes=1,060MB ← ⚠️ 超额 60MB!
```

| 维度 | 当前行为 | 理想行为 |
|------|---------|---------|
| 配额检查 | 乐观预检（read-then-write，不带锁） | 原子预留（预留 N bytes，超时自动释放） |
| 超用处理 | 无——`AddTenantUsage` 后不校验是否超额 | 超额时拒绝新写入 / 触发告警 |
| 扣减失败 | 仅 warn log，不阻断请求 | 重试 / 事务回滚 |
| 并发保护 | 无——所有写入共享预检-提交窗口 | Repository 层行锁 / 乐观锁（version vector） |

**影响规模：**

| 场景 | 超额风险 | 影响面 |
|------|---------|--------|
| 多个小文件并发上传（~100KB） | 低—窗口小，数额小 | 轻微超额 |
| 少量大文件并发上传（~100MB） | **高**—单次超额可达百 MB 级 | 显著超额 |
| 多租户密集写入（>10 并发/租户） | **极高**—配额限制形同虚设 | 收费用收不到、资源耗尽 |
| 配合 quota warming（智能预读） | 超额在预热期间爆发 | 管理面失控 |

### 为什么需要

| 理由 | 影响 |
|------|------|
| **配额承诺违反** | 向用户承诺 1GB 限额，实际允许 1.2GB+ 使用——SLA 失信 |
| **资源争抢** | 单租户超额可耗尽共享存储，导致同一存储后端上的"安静的邻居"租户 OOM/ENOSPC |
| **计费失真** | 按量计费模式下，超额部分无法向租户收费（或需承担坏账） |
| **审计要求** | SOC2/HIPAA 要求资源消耗的可审计性——TOCTOU 使审计记录不可靠 |
| **S3 协议预期** | AWS S3 Bucket 配额一旦达到即返回 403 AccessDenied，不允许多写 |

### 建议架构

```
┌─────────────────────────────────────────────────────────────────┐
│                   原子配额管理系统                              │
│                                                                 │
│  ┌──────────────────────┐    ┌──────────────────────────┐      │
│  │  配额预留层           │    │  预留回收 GC              │      │
│  │  ReserveBytes(t,n,d) │    │  expireStaleReservations │      │
│  │  → reservation_id    │◄───│  (超时未提交 → 自动回收)  │      │
│  │  → TTL: 30s          │    └──────────────────────────┘      │
│  └────────┬─────────────┘                                      │
│           │                                                     │
│  ┌────────▼─────────────┐    ┌──────────────────────────┐      │
│  │  两阶段提交            │    │  超额补偿引擎              │      │
│  │  preflight+reserve   │    │  commit失败 → revert     │      │
│  │  write → commit      │    │  commit超额 → alert      │      │
│  │  (reservation_id)    │    │  revert超额 → 终止新写入  │      │
│  └──────────────────────┘    └──────────────────────────┘      │
│                                                                 │
│  Repository 层新增：reservations 表 + TTL + 并发控制            │
└─────────────────────────────────────────────────────────────────┘
```

### 代价评估

| 工作项 | 复杂度 | 涉及文件 |
|--------|--------|---------|
| `reservations` 表 + 迁移 | 低（新增表 + 2 条 SQL） | `repository/migrations/{sqlite,postgres}/NNNN_*` |
| `ReserveBytes` / `ReleaseReservation` | 中（Repository 接口新增 2 方法） | `repository/repository.go`, `sql_helpers.go` |
| TOCTOU 窗口消除（preflight 改造） | 中（整合预留 → 写入 → 提交流水线） | `service/file_crud.go`, `service/file_multipart.go` |
| 预留超时 GC | 低（加入 Reconcile 或独立 ticker） | `reconcile/` 或 `service/` |
| 超额告警 metric | 低（`quota.overage_bytes` gauge） | `telemetry/metrics.go` |

---

## 方向二：WebDAV 协议绕过全链路中间件防护（WebDAV Middleware Chain Bypass）

### 现状

`cmd/server/main.go` 中的请求分发逻辑：

```go
// line 164-175
func buildDispatcher(r *chi.Mux, davH http.Handler, cfg *config.Config) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
        if davH != nil && cfg.WebDAV.Prefix != "" {
            p := req.URL.Path
            if p == cfg.WebDAV.Prefix || strings.HasPrefix(p, cfg.WebDAV.Prefix+"/") {
                davH.ServeHTTP(w, req)  // ← WebDAV 先于 chi 路由处理！
                return
            }
        }
        r.ServeHTTP(w, req)  // ← chi 路由（含全部中间件）
    })
}
```

中间件链在 `applyMiddleware` 中包裹 chi router：

```go
// line 194-209
func applyMiddleware(...) http.Handler {
    for _, m := range []func(http.Handler) http.Handler{
        middleware.AccessLog(logger),   // 访问日志
        concurrencyMW,                  // 并发限制
        middleware.Recoverer(logger),   // panic 恢复
        telemetry.HTTPMiddleware(...),  // OTel 指标
        rl.Middleware(),                // 速率限制
        middleware.Tenant,              // 租户提取
        authReg.Middleware(),           // 认证鉴权
        middleware.CORS(...),           // 跨域
        middleware.RequestID,           // 请求 ID
    } {
        handler = m(handler)  // ← 中间件链仅包裹 chi router
    }
    return handler
}
```

**WebDAV 请求路径完全不经过以下中间件：**

```
buildDispatcher (入口)
  ├── WebDAV 请求 → davH.ServeHTTP → ❌ AccessLog ❌ ConcurrencyLimit ❌ Recoverer
  │                                      ❌ OTel ❌ RateLimit ❌ Tenant ❌ Auth
  │                                      ❌ CORS ❌ RequestID
  └── 其他请求 → applyMiddleware(chi router) → ✅ 全量中间件
```

**各协议防护对比：**

| 防护维度 | REST `/v1` | S3 `/s3` | WebDAV | MCP `/mcp` |
|---------|------------|----------|--------|-------------|
| Auth 认证 | ✅ `authReg.Middleware()` | ✅ | ❌ **完全跳过** | ✅（chi 路由内） |
| Rate Limit | ✅ `rl.Middleware()` | ✅ 全局 | ❌ **完全跳过** | ✅ |
| Concurrency Limit | ✅ `concurrencyMW` | ✅ | ❌ **完全跳过** | ✅ |
| CORS | ✅ `middleware.CORS` | ✅ | ❌ **完全跳过** | ❌（验证） |
| OTel HTTP Metrics | ✅ `telemetry.HTTPMiddleware` | ✅ | ❌ **完全跳过** | ✅ |
| Access Log | ✅ `middleware.AccessLog` | ✅ | ❌ **完全跳过** | ✅ |
| Request ID | ✅ `middleware.RequestID` | ✅ | ❌ **完全跳过** | ✅ |
| Tenant Header | ✅ `middleware.Tenant` | ✅ | ✅（`davFS.tenant` 手动读取） | ✅ |
| Recoverer | ✅ `middleware.Recoverer` | ✅ | ✅（`xwebdav.Handler` 内置 recover） | ✅ |

### 影响分析

| 场景 | 风险等级 | 具体影响 |
|------|---------|---------|
| **未认证访问** | 🔴 **严重** — WebDAV 无 auth 检查，任何能访问网络的用户可通过 WebDAV 读取/写入所有对象（取决于 `X-Aero-Tenant` header 的控制粒度） | WebDAV 完全无认证：无 API key 验证、无 JWT 验证、无 SigV4 验证、无匿名公读策略控制 |
| **无速率限制** | 🟠 **中** — 攻击者可通过 WebDAV 发起大量并发请求耗尽服务器资源，绕过全局 RPS 限制 | 全局 `RATE_LIMIT_RPS` 对 WebDAV 完全无效 |
| **无并发限制** | 🟠 **中** — `MAX_INFLIGHT_REQUESTS` 和 `PER_TENANT_CONCURRENCY_MAX` 对 WebDAV 无效，单租户可耗尽连接池 | WebDAV PUT 大文件不受并发限制 |
| **无 OTel 指标** | 🟡 **低（运维）** — WebDAV 请求对运维人员完全透明：无请求计数、无延迟指标、无 4xx/5xx 错误计数 | 无法回答"WebDAV 有多少请求、多慢、多少错误" |
| **无访问日志** | 🟡 **低（审计）** — WebDAV 操作在日志中完全不可见，无法审计谁通过 WebDAV 做了什么 | 合规审计漏洞 |
| **无 CORS** | 🟢 **低（WebDAV 客户端通常非浏览器）** — 浏览器中的 WebDAV JS 客户端无法跨域使用 | 影响面有限 |

### 建议架构

```
方案 A（推荐）：将 WebDAV 纳入 chi 路由树
────────────────────────────────────────
改动：不再用 buildDispatcher 前置分发，而是把 WebDAV handler 挂到 chi 路由树上
r.Handle(cfg.WebDAV.Prefix+"/*", davH)
这样 WebDAV 自动继承全部中间件。

但 WebDAV 目前使用 x/net/webdav.Handler，它要求：
  - 自身处理 OPTIONS 请求（与 CORS middleware 的 OPTIONS 处理冲突）
  - 自身读取 Tenant header（与 middleware.Tenant 冗余）
解决方案：在 WebDAV handler 外包裹一个适配层，从 context 读取 Tenant 而非从 header 直接读取。

方案 B（轻量修补）：为 WebDAV 添加最小安全层
────────────────────────────────────
不改路由结构，在 davFS 方法中注入 auth 检查：
  func (f *davFS) tenant(ctx context.Context) string {
      // ✅ 来自 middleware.Tenant（方案 A 依赖）
      // 或 ❌ 直接从 header 读取（当前实现）
  }
在 davFS 每个公开方法入口调用 authReg.Authenticate(w, r)
```

### 代价评估

| 工作项 | 复杂度 | 涉及文件 |
|--------|--------|---------|
| WebDAV 路由迁移到 chi 树 | 中 | `cmd/server/main.go`（移除 `buildDispatcher`，改为 r.Handle） |
| `davFS.tenant` 改为读取 context | 低 | `internal/api/webdav/dav.go`（`tenant()` 方法）+ 测试 |
| CORS middleware 兼容 WebDAV OPTIONS | 低 | `internal/middleware/cors.go`（OPTIONS 短路非 CORS 请求） |
| MCP 路由纳入安全链 | 低 | `cmd/server/main.go`（`r.Method("/mcp")` 而非 `r.Handle` + 外层） |

---

## 方向三：存储后端可观测性真空（Storage Backend Observability Vacuum）

### 现状

当前 `telemetry/metrics.go` 定义了 15+ 组域指标，涵盖 AI、Reconcile、Jobs、Idempotency、Event、Indexer、Scrub、Webhook——**零存储后端指标**。

```go
var (
    // ✅ AI 管线：6 组指标
    mAIRequests, mAITokens, mAICostMicros
    mAIEmbedRequests, mAIEmbedTokens
    mAISearchLatency, mAIEmbedLatency

    // ✅ 后台任务：5 组指标
    mReconcileOrphanBlobs, mReconcileDeleted
    mJobsCompleted, mJobsFailed, mJobsRetried

    // ✅ 其它域：5 组指标
    mIdempotencyReplays, mEventsDropped
    mIndexerSkip, mScrubTotal, mWebhookRetries

    // ❌ 存储后端：0 组指标
)
```

`circuitBreaker` 内部维护完整状态机但零暴露：

```go
// internal/storage/circuitbreaker.go:77-84
func (cb *circuitBreaker) Stats() (state CBState, failures, total int) {
    cb.mu.Lock()
    defer cb.mu.Unlock()
    state = cb.state
    // ... 滑动窗口聚合
    return state, failures, total  // ← 返回了但无人消费
}
```

`FileService` 零方法级指标：

```go
// internal/service/file.go — FileService 结构体
type FileService struct {
    store        storage.Storage
    repo         repository.Repository
    logger       *slog.Logger
    sink         EventSink
    chunkCleaner ChunkCleaner
    // ❌ 无 metrics
    // ❌ 无 latency recorder
    // ❌ 无 operation counter
}
```

**缺失的关键指标：**

| 指标域 | 需要什么 | 用法 |
|--------|---------|------|
| **存储操作延迟** | `storage.put_duration_ms{backend}`, `storage.get_duration_ms{backend}`, `storage.delete_duration_ms{backend}` | 发现后端性能退化、区分 fast/slow 后端 |
| **存储错误率** | `storage.errors_total{backend, error_type}` | 区分 404（正常）vs 500（后端故障） |
| **Circuit Breaker 状态** | `storage.circuit_breaker_state{backend}` 0/1/2 gauge | 自动发现后端不可用 |
| **Circuit Breaker 事件** | `storage.circuit_breaker_transitions_total{backend, from, to}` | 追踪断路器打开/关闭频次 |
| **存储容量** | `storage.capacity_bytes{backend}`, `storage.used_bytes{backend}`, `storage.usable_bytes{backend}` | 容量规划、磁盘满预警 |
| **存储健康** | `storage.health{backend}` 0/1 gauge（stat 探测） | 聚合健康面板 |
| **FileService 操作计数** | `file_service.requests_total{operation, protocol}` | 按操作排名的请求分布 |
| **FileService 操作延迟** | `file_service.duration_ms{operation, protocol}` | 按协议的 Get/Put/Delete 延迟 |

### 为什么需要

| 理由 | 影响 |
|------|------|
| **故障定位慢** | S3 后端慢 → 管理员只能看到 HTTP 500，无法区分是 S3 超时还是应用层瓶颈 |
| **容量规划盲区** | 无法回答"以当前增长率多久会填满磁盘"——只能人工 df -h |
| **断路器无告警** | `circuitBreaker` 打开后所有请求直接 503，但运维无告警触发器可配 |
| **性能基线缺失** | 无法回答"local FS vs S3 在 PUT 1MB 对象上的 P50/P95 差异" |
| **SLA 报告不可编** | 无法生成"存储后端可用性 99.9%"的报告 |

### 建议架构

```
FileService 指标仪表化：
  方法入口 → recordLatency("Put") → defer recordLatency("Put")
  方法返回 → recordCount("Put", err)

  实现方式：在 FileService 上增加一个可选的 metrics 字段
  type FileService struct {
      ...
      metrics *StorageMetrics  // nil = no-op
  }

Circuit Breaker 指标暴露：
  - 新增 OTel gauge: circuit_breaker_state{backend}
  - 新增 OTel counter: circuit_breaker_transitions{backend}

Storage 后端健康探针：
  - 可配置的 periodic stat probe（每 30s stat @health/probe）
  - 暴露 storage_up{backend} 0/1 gauge

所有指标通过 OTel Prometheus exporter 在 /metrics 暴露
```

### 代价评估

| 工作项 | 复杂度 | 涉及文件 |
|--------|--------|---------|
| `FileService` 添加 metrics 字段 + 仪表化 | 中 | `internal/service/file.go`, `internal/service/file_crud.go` |
| `circuitBreaker` 暴露 OTel 指标 | 低 | `internal/storage/circuitbreaker.go` + `internal/telemetry/metrics.go` |
| 存储后端健康探针 | 低 | 新文件或加入 reconciler |
| 容量 gauge 注册（disk usage） | 低 | `internal/telemetry/metrics.go`（`RegisterStorageGauges` 扩展） |
| Grafana dashboard 面板扩展 | 低 | `deploy/grafana/` |

---

## 方向四：进程内事件总线订阅者健康管理缺失（In-Process Event Bus Subscriber Health Management）

### 现状

`events.Bus` 当前实现：

```go
// internal/events/bus.go
type Bus struct {
    repo      repository.Repository
    logger    *slog.Logger
    subBuffer int

    mu        sync.RWMutex
    subs      []chan repository.Event  // ← 匿名 channel 切片，无订阅者标识
    transport func(...) error
    dropped   atomic.Int64             // ← 全局计数器，无订阅者维度
}
```

订阅机制：

```go
func (b *Bus) Subscribe() <-chan repository.Event {
    ch := make(chan repository.Event, b.subBuffer)
    b.mu.Lock()
    b.subs = append(b.subs, ch)  // ← 只追加，无上限、无标识、无需注销
    b.mu.Unlock()
    return ch
}
```

广播机制：

```go
func (b *Bus) broadcast(e repository.Event) {
    b.mu.RLock()
    defer b.mu.RUnlock()
    for _, ch := range b.subs {
        select {
        case ch <- e:               // ← 缓冲区够 → 投递
        default:                    // ← 缓冲区满 → 静默丢弃
            b.dropped.Add(1)       // ← 全局计数器，不知道谁在丢
            telemetry.IncEventDropped(context.Background())
        }
    }
}
```

**已知的 5 个订阅者：**

| 订阅者 | 启动位置 | 缓冲区 | 当前消费速率 | 丢弃风险 |
|--------|---------|--------|-------------|---------|
| `Indexer.Run` | `main.go:198` | 64 (defaultSubBuffer) | 中等（embed+store） | 🟡 批量嵌入时可能暂堵 |
| `Antivirus.Worker.Run` | `main.go:217` | 64 | 高（扫描快） | 🟢 低 |
| `Replication.Worker.Run` | `main.go:230` | 64 | 低（网络 I/O） | 🟠 后端网络延迟高时积压 |
| `Webhook.Run` | `main.go:244` | 64 | 中等（HTTP POST） | 🟠 目标端点慢时积压 |
| `SSE Stream`（每个浏览器） | `sse.go:64` | 64 | 无限（客户端连接数） | 🔴 页面打开多 + 不消费 = 快速满 buffer |

### 为什么需要

| 场景 | 当前行为 | 问题 |
|------|---------|------|
| Webhook 目标临时宕机 5 分钟 | channel buffer 64 条 → 约第 65 条事件开始丢弃 | **事件丢失**——webhook 重试队列需要原始事件，但已丢弃的无法重试 |
| 10 个浏览器打开 SSE 页面未消费 | 10 channels × 64 buffer = 640 条可丢弃 | 约第 641 条起每个事件触发 10 次丢弃——`dropped` 计数器暴涨但 `EventCreated` 索引器可能也丢 |
| 索引器处理大文件（30s embeddings） | 64 buffer → 索引器处理完前约 64 个新事件被丢弃 | **对象可能永远不会被索引**——事件丢了，除非全量 reindex |
| 运维值班查询"为什么事件堆积" | 无——没有任何度量能回答 | 只能逐个检查 worker goroutine 状态，靠 slog log 推理 |

### 建议架构

```
┌─────────────────────────────────────────────────────────────────┐
│               事件总线订阅者健康管理系统                          │
│                                                                 │
│  ┌──────────────────┐    ┌─────────────────────────┐           │
│  │  订阅者注册表      │    │  健康检查器               │           │
│  │  name -> channel  │    │  every 30s:              │           │
│  │  buffer_size      │───►│  - 检查 channel depth    │           │
│  │  drop_count       │    │  - 检查缓冲饱和度 %      │           │
│  │  created_at       │    │  - 发出告警阈值 80%      │           │
│  └──────────────────┘    └──────────┬──────────────┘           │
│                                     │                           │
│  ┌──────────────────┐    ┌──────────▼──────────────┐           │
│  │  背压策略          │    │  每个订阅者的独立指标     │           │
│  │  慢订阅者：        │    │  event.subscriber_depth │           │
│  │  - 暂停（暂停投递）  │    │  event.subscriber_cap  │           │
│  │  - 降速（每 N 条 1）│───►│  event.subscriber_drops│           │
│  │  - 断开（close ch） │    │  event.subscriber_lag  │           │
│  └──────────────────┘    └─────────────────────────┘           │
│                                                                 │
│  Subscribe(name string, cap int) Subscriber                     │
│  Subscriber.C() <-chan Event                                     │
│  Subscriber.Close()                                              │
│  Subscriber.Stats() {Depth, Cap, Drops, CreatedAt}              │
└─────────────────────────────────────────────────────────────────┘
```

### 代价评估

| 工作项 | 复杂度 | 涉及文件 |
|--------|--------|---------|
| `Subscriber` 类型定义（name + channel + depth gauge） | 低 | `internal/events/subscriber.go`（新文件） |
| `Bus.Subscribe` 改为 `Subscribe(name string, size int) *Subscriber` | 中 | `internal/events/bus.go` |
| `broadcast` 改为关联 subscriber 的 channel | 中 | `internal/events/bus.go`（`broadcast` 方法，`dropped` 按订阅者计数） |
| 注册 subscribers OTel 指标 | 低 | `internal/telemetry/metrics.go` |
| 订阅者健康检测 goroutine（告警高 drop 率） | 低 | `internal/events/bus.go` 或独立文件 |
| 调用方适配（Indexer/Antivirus/Replication/Webhook/SSE 改用命名 Subscribe） | 中 | `cmd/server/main.go`, `internal/api/rest/sse.go` |

---

## 方向五：S3 虚拟主机风格请求路由缺失（Virtual-Hosted Style S3 Request Routing Gap）

### 现状

当前 S3 兼容协议仅支持 **path-style** 请求：

```
# Path-style（当前已支持）
PUT /s3/{bucket}/{key}
GET /s3/{bucket}/{key}
```

AWS S3 同时支持两种请求风格，且主流 SDK **默认使用 virtual-hosted style**：

```
# Virtual-hosted style（当前不支持）
PUT http://{bucket}.{host}:{port}/{key}
GET http://{bucket}.{host}:{port}/{key}
```

**当前路由注册：**

```go
// internal/api/s3compat/router.go
func NewRouter(svc *service.FileService, logger *slog.Logger) chi.Router {
    r := chi.NewRouter()
    // 仅支持 path-style，注册在 chi 子路由上
    // ...
}

// main.go — 注册挂载点
r.Mount(cfg.S3Compat.Prefix, s3compat.NewRouter(svc, logger))
// 例如：/s3/{bucket}/{key}
```

**不支持 virtual-hosted 的具体影响：**

| SDK/工具 | 默认请求风格 | AeroVault 是否兼容 |
|----------|------------|-------------------|
| AWS CLI (`aws s3 cp`) | Virtual-hosted（新版本） | ⚠️ 需 `--endpoint-url` + `--no-sign-request` + 特殊配置 |
| boto3 (Python) | Virtual-hosted（`s3 = boto3.client('s3')`） | ❌ 默认配置不可用 |
| aws-sdk-js v3 | Virtual-hosted | ❌ 默认配置不可用 |
| MinIO Client (`mc`) | Path-style | ✅ |
| rclone | Virtual-hosted | ❌ 默认配置不可用 |
| Cyberduck | Path-style 可选 | ✅（需配置） |

### 为什么需要

| 理由 | 影响 |
|------|------|
| **开箱即用体验** | 用户按照标准 S3 SDK 文档创建客户端 → 请求默认发到 `{bucket}.s3.amazonaws.com` → 连接 refused。需阅读 AeroVault 专属文档，学习 path-style 配置 |
| **SDK 集成门槛** | Python/JS/Go SDK 示例中默认的 virtual-hosted 不工作，每份 SDK 文档都需要额外的 `endpoint_url`/`force_path_style` 配置说明 |
| **桶名 DNS 兼容性** | Virtual-hosted style 隐含了桶名必须是合法 DNS 标签（小写字母数字连字符）的约束——path-style 无此约束，允许桶名包含大写字母、下划线等非法 DNS 字符 |
| **SSL 证书体验** | 生产部署中，`*.example.com` 通配符证书可覆盖 `{bucket}.example.com` 的 virtual-hosted 请求；path-style 需要额外路由配置 |

### 建议架构

```
方案 A：基于 Host header 的请求分发
──────────────────────────────────
在 buildDispatcher 或 chi 路由前加入 Host header 分析：

func virtualHostedBucket(host, prefix string) (bucket string, ok bool) {
    // 从 host 提取子域名作为 bucket 名
    // host = "mybucket.example.com:8080"
    // 已知 base domain = "example.com"
    // → bucket = "mybucket", ok = true
}

buildDispatcher 扩展：
  if virtualHosted := ...; virtualHosted != "" {
      // 重写 URL.Path = "/s3/{bucket}/{key}"
      // 交给 s3compat router 处理
  }

方案 B：配置式 virtual-hosted 映射
──────────────────────────────────
S3_VIRTUAL_HOSTED_DOMAINS=example.com,localhost
请求 Host header 匹配时 → 提取子域名作为 bucket
```

### 代价评估

| 工作项 | 复杂度 | 涉及文件 |
|--------|--------|---------|
| Virtual-hosted bucket 提取逻辑 | 低 | `internal/api/s3compat/router.go` 或新文件 |
| Host header 分析 + URL 重写 | 低 | `cmd/server/main.go`（`buildDispatcher`） |
| 配置项 `S3_VIRTUAL_HOSTED_DOMAINS` | 低 | `internal/config/config.go`, `internal/config/config_app.go` |
| SSL/TLS 兼容性验证 | 低 | `deploy/demo/`——仅需要文档说明 |
| S3 SDK 兼容性测试 | 中 | 端到端测试：用 boto3/aws-sdk-js 默认配置连接 |

---

## 综合优先级与建议实施顺序

| 优先级 | 方向 | 影响面 | 前置依赖 | 涉及文件量 | 建议开始时间 |
|--------|------|--------|---------|-----------|------------|
| **P1** | 方向二：WebDAV 中间件绕过 | 安全/运维 | 无 | 3–5 个文件 | **立即** |
| **P1** | 方向一：配额 TOCTOU 竞态 | 数据完整性 | 无 | 5–8 个文件 | **立即** |
| **P1** | 方向三：存储后端可观测性 | 运维/可靠性 | 无 | 4–6 个文件 | **本迭代** |
| **P2** | 方向四：事件订阅者健康管理 | 可靠性 | 方向三的 OTel 基础 | 5–7 个文件 | **下迭代** |
| **P2** | 方向五：Virtual-hosted S3 | S3 兼容性 | 无 | 3–5 个文件 | **下迭代** |

---

## 与既有文档的交叉引用确认

| 方向 | 看似重复但实际不同的既有方向 | 区别说明 |
|------|--------------------------|---------|
| **方向一** | v27 方向二「运行时健全性」中一行 "ENOSPC 的 TOCTOU" 提及 | v27 聚焦磁盘空间耗尽（ENOSPC）的 TOCTOU，本方向聚焦**租户配额超额**——是完全不同的资源维度（逻辑配额 vs 物理磁盘）和不同的 data plane 影响 |
| **方向二** | v40 方向表两行「WebDAV 专有认证适配器」「WebDAV 无 Bearer token 通道」 | v40 仅覆盖**认证通道**的有无，本方向揭示 WebDAV **完全跳过 auth/rate limit/concurrency/CORS/OTel/access log/request ID 七层中间件**——范围和严重性远超 v40 |
| **方向三** | v11 方向三指标表格中一行 `storage.backend_latency_ms{backend}` | v11 的 3 行无架构分析、无代码锚点、无实施路径；本方向给出完整代码证据锚链（circuitBreaker 内部状态、Stats() 裸露、metrics.go 零覆盖）和分步实施方案 |
| **方向四** | v44 方向五「SSE Replay 完备性与 Webhook 死信审计」 | v44 聚焦**外部事件消费端**（SSE 客户端断线重连、webhook 死信状态字段）；本方向聚焦**进程内事件总线核心**——订阅者注册/注销/健康检测/背压策略，是被 v44 完全绕过的不同架构层 |
| **方向五** | v42 方向一「S3 对象 lock mode」/ 方向三「S3 子资源完备性」/ 方向四「events 通知执行引擎」 | v42 覆盖 S3 操作语义完备性（子资源 REST API 的有无），本方向覆盖**请求路由风格**——virtual-hosted vs path-style，是完全不同的协议层维度 |

---

> **文档生成方法：** 对 `internal/` 下全部 100+ 源文件逐行审查，识别 4 种缺口模式：① 安全边界缺失（方向二）、② 并发安全模型漏洞（方向一）、③ 可观测性盲区（方向三）、④ 可靠性保障断点（方向四）、⑤ 协议兼容性差距（方向五）。每种模式在代码中有精确锚点，在 54 份既有文档中零实质性分析。
