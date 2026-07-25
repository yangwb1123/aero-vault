# 高价值扩展方向分析 v34 — 生产硬化与平台纵深缺口

> **分析范围：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 全部 237 个 `.go` 文件 + `sdk/*` + `deploy/*` + `docs/*` + 48 对迁移文件）
> **分析日期：** 2026-07-10
> **视角：** 资深架构师 / 产品经理 — 聚焦「此前 33 期分析（累计 ~170+ 方向、22,000+ 行分析文本）已覆盖几乎所有功能方向后的**生产硬化与平台纵深**缺口」
> **去重方法：** 逐领域对比 `docs/requirements/` 下 **33 期既有分析（v1–v33）** + `docs/ROADMAP.md`（10 方向，全部实现） + `docs/adr/DECISIONS.md` + `docs/CHANGELOG.md` + `docs/TODO.md`（全部完成）。每个方向在既有文档中 **无实质性独立架构分析**（矩阵表格中的一行提及或过路引用不构成实质性分析）。
> **原则：** 不编写任何实现代码。每个方向附带：现状 → 代码锚点 → 缺失能力 → 为什么需要 → 架构概要 → 边界情况。

---

## 背景：前 33 期已完成覆盖的去重矩阵

前 33 期 expansion 文档覆盖了 **约 170+ 个方向**，ROADMAP 10 个方向全部实现，TODO 清单全部完成。以下领域已深度覆盖，本期不再重复：

| 领域 | 覆盖期数 | 方向数示例 |
|------|---------|-----------|
| AI/RAG 管线（Embed/Search/Chat/Agent/Indexer/Rerank/PII/缓存/预算/模型路由/语义缓存/漂移检测） | v1~v33 | ~26 |
| S3 兼容协议（子资源/ACL/Policy/CORS/通知/LegalHold/COPY/Batch/Multipart/SSE-C/ListObjectsV2） | v1~v33 | ~18 |
| 存储后端（Local/S3/OSS/COS/KMS/SSE/熔断器/分层/压缩/多后端/块级去重/CAS/SSE 轮换/迁移） | v1~v33 | ~20 |
| 认证授权（JWT/API Key/SigV4/OIDC/SAML/SCIM/MFA/前缀级策略/FIPS/Policy Engine/mTLS/客户端证书） | v1~v33 | ~20 |
| 多租户（CRUD/配额/预算/审计/日费用/物理隔离/FGA/IaC/Admin Console/Terraform/计费/声明式协调） | v1~v33 | ~18 |
| 事件/通知/Webhook（总线/传输/过滤/多通道/死信/背压/CDC/Kafka/Lambda/Postgres NOTIFY/事件重放） | v1~v33 | ~16 |
| 复制/HA/集群（CRR/SRR/单例/Federation/多活/CQRS/故障转移/Geo-Distributed/Conflict Resolution） | v1~v33 | ~15 |
| Reconcile/GC/Lifecycle（孤儿/保留/Scrub/版本/上传GC/Noncurrent/存储类转换/标签规则） | v1~v33 | ~15 |
| 合规（WORM/Legal Hold/保留/访问日志/客户端加密/对象锁模式/数据驻留/Geo-Fencing/SOC2/监管链） | v1~v33 | ~15 |
| 可观测性（OTel/Metrics/Grafana/Prometheus/SLO/Distributed Tracing/pprof/告警/Debug/Profiling） | v1~v33 | ~14 |
| 工程质量（内存安全/流式加密/并发/压缩/错误模型/测试/性能/多协议一致性/代码质量） | v1~v33 | ~15 |
| Web UI / Admin Console / MCP / CLI 完整性 | v1~v33 | ~12 |
| SDK 跨语言（Go/Python/JS）完整性 | v1~v33 | ~8 |
| 基础设施（TLS/ACME/配置热重载/IP ACL/Feature Flag/Helm/CDN/Data Provenance/熔断器/优雅关闭） | v1~v33 | ~12 |
| 存储分层/生命周期/预测性分层/批量操作框架/导入迁移/备份DR | v1~v33 | ~12 |
| 其他（SQL查询/多模型查询/事件驱动计算/Serverless/内容告警/写入优化/层次命名空间/FUSE网关） | v1~v33 | ~14 |

### 本期 5 个方向的去重验证

| # | 方向 | `grep` 验证结果 | 既有覆盖判定 |
|---|------|----------------|------------|
| 1 | **S3 Select & Server-Side Object Analytics** | `grep -rn "select.*object\|s3.*select\|object.*analytics\|csv.*query\|parquet.*query\|server.side.*query" docs/requirements/` → 仅在 v27/v31/v32 的过路表格中存在矩阵行，**零实质性架构分析** | ❌ 未独立分析 |
| 2 | **Adaptive Backpressure & Dynamic Congestion Control** | `grep -rn "adaptive.*concurr\|adaptive.*rate\|dynamic.*throttle\|congestion.*control\|backpressure.*framework\|AdaptiveRateLimiter" docs/requirements/` → 仅 v20 有一行表格提及 | ❌ 未独立分析 |
| 3 | **AI Quality Assurance & RAG Evaluation Framework** | `grep -rn "rag.*evalu\|rag.*metric\|retrieval.*quality\|answer.*correct\|golden.*dataset\|precision.*recall\|eval.*framework\|qa.*test\|retrieval.*eval" docs/requirements/` → v5/v21/v23 有提及但聚焦于 RAG Pipeline 质量（chunking/rerank 策略），**非系统化的评估框架分析** | ⚠️ 部分覆盖但角度不同 |
| 4 | **Performance Benchmarking & Regression Detection** | `grep -rn "benchmark.*suite\|perf.*regression\|regression.*detect\|benchmark.*pipeline\|benchmark.*infra\|benchmark.*framework\|capacity.*model\|linpack\|perf.*gate" docs/requirements/` → v18 覆盖了 CLI benchmark 工具，但**非持续性的 CI 性能回归检测框架** | ⚠️ 部分覆盖但深度不足 |
| 5 | **Dynamic Tenant Provisioning & Self-Service Onboarding** | `grep -rn "self.*service.*onboard\|tenant.*onboard\|tenant.*provision\|self.*serv.*portal\|sign.*up\|registration\|invitation\|billing.*portal\|trial\|freemium" docs/requirements/` → **零命中** | ❌ 完全未覆盖 |

---

## 本期方向总览

| # | 方向 | 类型 | 优先级 | 代码锚点 | 核心痛点 |
|---|------|------|--------|---------|---------|
| 1 | **🟠 S3 Select & Server-Side Object Analytics** | S3 协议/功能 | **P1** — S3 生态兼容性的最后重大缺口 | `internal/api/s3compat/` 无 Select handler；`internal/service/file.go` 无流式 SQL 执行能力 | 用户无法对存储在系统中的结构化数据执行 SQL 查询，必须下载全部数据后离线处理 |
| 2 | **🔴 Adaptive Backpressure & Dynamic Congestion Control** | 可靠性/架构 | **P0** — 生产系统防止级联失效的底线 | `internal/middleware/ratelimit.go`（静态 token-bucket）；`internal/middleware/middleware.go`（静态并发限制）；无动态调节 | 后端抖动时静态限流无法适应：要么限得太严浪费容量，要么限得太松导致级联超时 |
| 3 | **🟡 AI Quality Assurance & RAG Evaluation Framework** | AI/质量 | **P2** — 生产 RAG 系统的可信度保障 | `internal/ai/` 全链路无质量评估点；`internal/telemetry/metrics.go` 只有延迟/成本指标 | 无法量化检索质量（Precision@K、Recall@K）和生成质量（Answer Correctness），模型调优靠感觉 |
| 4 | **🟡 Performance Benchmarking & CI Regression Detection** | 工程质量/运维 | **P1** — 性能退化的早期发现能力 | `Makefile` 无 `benchmark` target；`internal/` 无 `*_benchmark_test.go`；CI 中无性能门禁 | 修改代码后无法自动检测性能退化，性能问题直到生产环境才暴露 |
| 5 | **🟠 Dynamic Tenant Provisioning & Self-Service Portal** | 多租户/平台 | **P2** — 多租户 SaaS 平台的基础设施 | `internal/api/rest/admin.go` 有 admin CRUD 但无自助注册、无租户邀请、无流量分级 | 每个新租户都需要运维人员手动创建 API Key + 配置配额，无法自助上线 |

---

## 方向一：🟠 S3 Select & Server-Side Object Analytics（服务端数据查询）

### 现状

当前系统支持 S3 兼容协议的对象级操作（CRUD、Multipart、Tagging、ACL、版本管理），但**没有服务端数据查询能力**。

S3 Select 是 AWS S3 在 2018 年推出的核心功能：**用户发送 SQL 表达式到服务端，服务端在存储层直接过滤/聚合/投影 CSV、JSON、Parquet 数据，只返回结果集**，而非整个对象。

```http
POST /s3/{bucket}/{key}?select&select-type=2 HTTP/1.1
x-amz-s3-select-type: 2
Content-Type: application/xml

<SelectObjectContentRequest>
  <Expression>SELECT s.year, s.event FROM S3Object s WHERE s.country = 'China'</Expression>
  <ExpressionType>SQL</ExpressionType>
  <InputSerialization><CSV><FileHeaderInfo>USE</FileHeaderInfo></CSV></InputSerialization>
  <OutputSerialization><JSON></JSON></OutputSerialization>
</SelectObjectContentRequest>
```

**当前完全不具备此能力。** 用户要查询存储在 aero-vault 中的 CSV 文件，必须：
1. `GET /v1/files/{key}` 下载整个文件（可能数 GB）
2. 在客户端解析并过滤
3. 带宽和内存瓶颈明显

### 代码锚点

| 位置 | 当前状态 |
|------|---------|
| `internal/api/s3compat/handler.go` | 无 `Select` handler，仅有子资源路由，`?select` 未被解析 |
| `internal/api/s3compat/router.go` | 无 `POST ...?select` 路由注册 |
| `internal/api/s3compat/xml.go` | 无 `SelectObjectContentRequest/Response` XML 编解码 |
| `internal/service/file.go` | 无 `SelectObject` 方法 |
| `internal/storage/storage.go` | `Storage` 接口无 Select 方法（Select 在中间层实现，不需要存储感知） |
| `go.mod` | 无 SQL 解析库依赖（stdlib `database/sql` 或第三方 `expr`/`goyacc`） |

### 为什么需要

**1. S3 Select 是 S3 协议生态的最后重大缺口之一。**

AWS S3 的 S3 Select 功能被广泛用于：
- **数据湖 ETL 预处理**：在加载到分析系统之前过滤/聚合数据
- **边缘/移动场景**：只下载所需字段而非整个文件
- **审计/合规查询**：从日志文件中提取特定事件
- **成本优化**：减少数据传输量和客户端计算资源

没有 S3 Select，使用 S3 协议接入的用户无法利用大量已有的工具和脚本生态。

**2. 工程实现是可行的纯 Go 方案。**

- CSV/JSON 解析：Go 标准库 `encoding/csv` + `encoding/json`
- SQL 解析：`expr` 或 `goyacc` 生成简单 SQL parser（SELECT ... FROM S3Object WHERE ...）
- 流式处理：逐行读取 → 过滤/投影 → 流式输出（不需要加载整个对象到内存）
- Parquet 支持：可选（`parquet-go` 库），支持列式读取

**3. 差异化价值。**

与 MinIO（纯对象存储）、Ceph（RADOS）不同，aero-vault 已经有 AI 管线。Select + AI 意味着：用户可以在存储层直接做 **数据过滤 → 语义搜索 → RAG 问答** 的端到端链路，而不需要经过外部 ETL。

### 架构概要

```
┌─────────────────────────────────────────────────────────────┐
│ S3 Select 架构                                                │
├─────────────────────────────────────────────────────────────┤
│                                                               │
│  POST /s3/{bucket}/{key}?select&select-type=2                 │
│    │                                                          │
│    ▼                                                          │
│  s3compat.SelectHandler                                       │
│    │  ├─ 解析 XML 请求 (InputSerialization + Expression)      │
│    │  ├─ 鉴权 (SigV4 + Bucket Policy)                         │
│    │  ├─ 获取对象元数据 + Content-Type 验证                   │
│    │  └─ 委托给 internal/select.Engine                        │
│    │                                                          │
│    ▼                                                          │
│  internal/select/engine.go                                    │
│    │                                                          │
│    ├─ 1. 解析 SQL 表达式 (自定义 SQL Parser)                  │
│    │     SELECT fields FROM S3Object [WHERE condition]        │
│    │     [LIMIT N] [AGGREGATE functions]                      │
│    │                                                          │
│    ├─ 2. 打开存储读取流 (storage.Get) + 输入格式解析          │
│    │     ├─ CSV: encoding/csv (支持 header/skip/quote/escape) │
│    │     ├─ JSON: encoding/json (支持 Lines 与 Document 模式) │
│    │     └─ Parquet: parquet-go (列式投影读取, Phase 2)       │
│    │                                                          │
│    ├─ 3. 逐行流式处理                                         │
│    │     ├─ 对每行/记录: 评估 WHERE 条件                      │
│    │     ├─ 匹配: 投影 SELECT 字段到输出缓冲区                │
│    │     └─ 不匹配: 丢弃                                     │
│    │                                                          │
│    ├─ 4. 聚合计算 (Phase 2)                                   │
│    │     ├─ COUNT, SUM, AVG, MIN, MAX                         │
│    │     └─ GROUP BY 分组聚合                                 │
│    │                                                          │
│    └─ 5. 流式编码输出 (OutputSerialization)                   │
│          ├─ CSV / JSON / Parquet                              │
│          └─ 通过 HTTP chunked transfer 逐步发送               │
│                                                               │
└─────────────────────────────────────────────────────────────┘
```

**SQL 表达式的实现选择：**

| 方案 | 复杂度 | 能力 | 建议 |
|------|--------|------|------|
| 手写递归下降 parser | 3-5 天 | SELECT ... WHERE ... LIMIT 完整支持 | ✅ Phase 1 — 最灵活，无外部依赖 |
| `expr` (expr-lang/expr) | 2 天 | 表达式引擎 + 自定义函数 | ⚠️ 仅支持 WHERE 子句过滤，无法做 SELECT 投影别名 |
| `sqlparser` (vitess/vitess) | 2 天 | 完整 SQL parser，重量级依赖 | ⚠️ 依赖过大（~300KB），但 SQL 语义最标准 |
| Go `database/sql` 虚拟查询 | 不现实 | 需要注册虚拟 SQLite table | ❌ 架构层面不可行 |

**建议 Phase 1 用手写 parser + `encoding/csv` + `encoding/json`，Phase 2 补充 Parquet 和聚合。**

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| 对象不存在 | 返回 `NoSuchKey` S3 错误 |
| 对象格式与 InputSerialization 不匹配 | 返回 `ParseError`，指出第一行/第一条记录的错误位置 |
| 超大文件（数 TB）的查询 | 全程流式处理，不加载全部到内存；支持 `Range` header 限制扫描范围 |
| SELECT * 无 WHERE | 返回所有行/记录（等同于全量读取，但经过输出格式转换） |
| WHERE 条件扫描所有行但无匹配 | 返回空结果集（不报错） |
| SQL 注入 | 不执行任意 SQL — 表达式严格限定为 `column op value` 模式，不支持子查询、JOIN、UNION |
| 空值处理 | CSV 空字段 → NULL；JSON 缺少字段 → NULL；WHERE 中 NULL 比较返回 false |
| 聚合查询（COUNT/SUM） | 需要扫描所有匹配行后再返回结果（不能流式返回第一条记录前就输出聚合值） |
| 嵌套 JSON 访问 | 支持点号路径：`s.contacts[0].name` — 需要实现路径解析 |
| CSV 自动检测分隔符/引号 | 从 InputSerialization 读取；默认 comma + double-quote |
| 查询被打断（客户端断开） | 中断读流，释放存储连接；通过 `req.Context().Done()` 检测 |

### 代码影响范围

| 模块 | 影响 |
|------|------|
| **新增** `internal/select/parser.go` | SQL 表达式解析器（递归下降） |
| **新增** `internal/select/engine.go` | 查询执行引擎（行流式处理 + 过滤 + 投影） |
| **新增** `internal/select/formats.go` | CSV/JSON/Parquet 输入格式适配器 |
| **新增** `internal/select/aggregate.go` | 聚合计算（COUNT/SUM/AVG/MIN/MAX, Phase 2） |
| **修改** `internal/api/s3compat/handler.go` | 新增 `handleSelect` 方法，路由 `?select&select-type=2` |
| **修改** `internal/api/s3compat/xml.go` | `SelectObjectContentRequest` / `SelectObjectContentResponse` XML 编解码 |
| **修改** `internal/api/s3compat/router.go` | 注册 `POST /s3/{bucket}/{key}` + `?select` 条件路由 |
| **修改** `internal/api/s3compat/errors.go` | 新增 S3 Select 错误类型（`ParseError`、`MissingRequestBodyError` 等） |
| **新增** `internal/service/select.go` | `SelectObject(ctx, tenant, bucket, key, expr, ...) → RowIterator` |
| **新增** `internal/select/parser_test.go` | SQL 表达式解析测试（WHERE 子句边界情况） |
| **新增** `internal/select/engine_test.go` | CSV/JSON 查询的集成测试 |
| **新增** `internal/api/s3compat/select_test.go` | S3 Select 协议级测试 |

---

## 方向二：🔴 Adaptive Backpressure & Dynamic Congestion Control（自适应背压与动态拥塞控制）

### 现状

当前系统的限流和并发控制全部使用**静态配置**：

```go
// internal/middleware/ratelimit.go
// 固定速率 token-bucket，配置后永不变化
rl := middleware.NewRateLimiter(cfg.RateLimit.RPS, cfg.RateLimit.Burst)
aiRL := middleware.NewRateLimiter(cfg.RateLimit.AIRPS, cfg.RateLimit.AIBurst)

// internal/middleware/middleware.go
// ConcurrencyLimiter — 静态容量，基于加权信号量
cl := middleware.NewConcurrencyLimiter(cfg.App.MaxInFlight)
```

| 组件 | 当前机制 | 静态设置的问题 |
|------|---------|--------------|
| 全局 RateLimiter | 固定 RPS × 租户 | 后端抖动时不会自动降速 |
| AI RateLimiter | 固定 AI RPS × 租户 | LLM 变慢时不会自适应降低速率 |
| ConcurrencyLimiter | 固定最大并发数 | 无法根据后端延迟动态调整 |
| PerTenantLimiter | 固定每租户并发上限 | 健康租户不能借用空闲租户的容量 |
| Circuit Breaker (storage) | 固定失败阈值 + 恢复超时 | 不会根据后端恢复速度调整半开探测间隔 |

**后果：** 在后端（存储/LLM/DB）变慢时，客户端请求仍然全速涌入→请求排队→超时→重试→**级联失效**。这是分布式系统中最常见的生产事故模式。

### 代码锚点

| 位置 | 当前能力 | 缺口 |
|------|---------|------|
| `internal/middleware/ratelimit.go` | `RateLimiter` 静态 token-bucket | 无法动态调整 `rate` 和 `burst`；无后端健康感知 |
| `internal/middleware/middleware.go` | `ConcurrencyLimiter` 固定信号量 | 无信号量动态调整；无自适应限流 |
| `internal/middleware/middleware.go` | `PerTenantConcurrencyLimiter` 固定每租户上限 | 无借用机制；闲置租户的容量不能动态分配给繁忙租户 |
| `internal/storage/circuitbreaker.go` | `CircuitBreaker` 固定阈值 | 无法根据后端延迟 P99 动态调整熔断阈值 |
| `internal/jobs/jobs.go` | `Pool` 固定 worker 数 | 无自适应 worker pool 大小 |
| `internal/ai/llm.go` | AI 调用无超时/限流自适应 | 无法根据 LLM 响应时间动态调整 AI RPS |

### 为什么需要

**1. 静态限流在真实生产环境中总是失败。**

生产环境的真实特征：**负载不是恒定的，后端的健康也不是恒定的。** 静态限流在面对以下场景时要么过保护（浪费容量），要么保护不足（级联失效）：

| 场景 | 静态限流行为 | 应该行为 |
|------|-------------|---------|
| S3 后端逐出频繁（限流） | 客户端继续全速发出请求 → 大量 503 | 检测到后端返回 `SlowDown` → 自动降低当前租户速率 |
| LLM 从 500ms 变为 5000ms | AI RateLimiter 继续允许全速 → 请求堆积 | 检测 P99 延迟上升 → 降低 `AI_RATE_LIMIT_RPS` |
| 一个租户的突发流量 | 抢占所有全局信号量 → 其他租户饿死 | 借用空闲租户的容量，同时限制突发不超过安全阈值 |
| DB 写入变慢（行锁竞争） | 并发 PUT 不变 → 连接池耗尽 | 检测 DB 查询延迟 → 降低写入并发 |
| 新的副本上线 | 限流配置不变 → 未充分利用新容量 | 根据活跃副本数自动提高全局并发上限 |

**2. 自适应拥塞控制是业界成熟的标准模式。**

| 系统 | 机制 |
|------|------|
| TCP | 拥塞窗口（Cubic/BBR）— 动态调整发送速率 |
| HTTP/2 | 流控窗口（基于接收方通告） |
| gRPC | 自适应背压（基于客户端/服务器延迟） |
| Netflix Hystrix | 动态熔断阈值（基于滚动窗口错误率） |
| AWS (client) | 指数退避 + 抖动（基于服务端返回的限流信号） |
| Google BBR | 基于带宽和 RTT 的拥塞控制（非丢包触发） |

**3. 工程成本适中，ROI 极高。**

当前基础设施已经提供：
- OTel 指标采集（延迟、错误率、速率）
- Prometheus 告警评估
- Token-bucket 的 `rate` 和 `burst` 字段（当前是 `float64`，可以运行时调整）
- Circuit breaker 的状态机

自适应层只需要：**观测 → 决策 → 执行** 三个组件。

### 架构概要

```
Adaptive Congestion Controller
================================

┌────────────────────────────────────────────────────────────┐
│ Control Loop（控制循环，每秒运行一次）                          │
│                                                              │
│  1. OBSERVE（观测）:                                           │
│     ├─ backend_latency_p99{backend=s3}                        │
│     ├─ backend_error_rate{backend=s3}                         │
│     ├─ queue_depth{queue=s3_writes}                           │
│     ├─ inflight_requests{tenant=acme}                         │
│     ├─ llm_latency_p50{model=gpt-4}                           │
│     └─ db_query_latency_p99{query=UpsertObject}              │
│                                                               │
│  2. DECIDE（决策 — AIMD 算法）:                                  │
│                                                               │
│     for each backend/tenant:                                   │
│       if error_rate > threshold:                              │
│         rate *= 0.5        // multiplicative decrease         │
│         cooldown = 30s     // 冷静期                          │
│       else if latency_p99 > target × 1.5:                     │
│         rate *= 0.8                                           │
│         cooldown = 15s                                        │
│       else if cooldown == 0 && no recent throttling:          │
│         rate *= 1.05       // additive increase               │
│         rate = min(rate, maxRate)                             │
│                                                               │
│     Tenants borrowing:                                         │
│       if tenant A is idle && tenant B is bursting:             │
│         allow B to borrow up to borrow_max from idle pool     │
│                                                               │
│  3. ACT（执行）:                                                │
│     ├─ Update RateLimiter.rate (atomic store)                 │
│     ├─ Update ConcurrencyLimiter.capacity (resize channel)    │
│     ├─ Update CircuitBreaker thresholds                       │
│     └─ Update JobPool worker count                            │
│                                                               │
└────────────────────────────────────────────────────────────┘
```

**关键接口：**

```go
// 可调节限流器 — 所有限流/并发组件实现此接口
type RateAdjuster interface {
    SetRate(rate float64)     // 更新速率（原子操作）
    SetBurst(burst int)       // 更新突发上限
    CurrentRate() float64     // 当前速率
}

// 观察指标—由控制循环收集
type ControllerMetrics struct {
    BackendLatencyP99   map[string]float64
    BackendErrorRate    map[string]float64
    TenantInflight      map[string]int
    DBQueryLatencyP99   map[string]float64
    LLMLatencyP50       map[string]float64
    JobQueueDepth       map[string]int
}

// 控制循环—每秒评估并调整
type CongestionController struct {
    adjusters []RateAdjuster
    metrics   func(context.Context) ControllerMetrics
    config    ControllerConfig  // 阈值、增减系数、冷却时间
}

func (cc *CongestionController) Run(ctx context.Context) {
    ticker := time.NewTicker(1 * time.Second)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            m := cc.metrics(ctx)
            adjustments := cc.decide(m)
            cc.apply(adjustments)
        }
    }
}
```

**配置项：**

```go
type ControllerConfig struct {
    // 后端错误率阈值（超过则降速）
    ErrorRateThreshold    float64  // 默认 0.05（5%）
    // 后端延迟目标（超过 target × LatencyMargin 则降速）
    LatencyTarget         time.Duration // 默认 100ms
    LatencyMargin         float64       // 默认 1.5（超过目标 50% 触发）
    // AIMD 参数
    DecreaseFactor        float64  // 默认 0.5（错误时砍半）
    IncreaseFactor        float64  // 默认 1.05（健康时缓慢增加）
    DecreaseCooldown      time.Duration // 默认 30s（降速后至少维持 30s）
    // 借用量（租户间借用）
    BorrowRatio           float64  // 默认 0.3（最多借用空闲容量的 30%）
    // 最大/最小速率限制
    MinRate               float64  // 防止降到零（默认 1 RPS）
    MaxRate               float64  // 物理上限（不配置则无上限）
}
```

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| 后端恢复后速率回升太慢 | 增加 `IncreaseFactor` 或实现快速恢复模式（检测到错误率归零后加倍增速） |
| 多个后端同时抖动（如 S3 + DB） | 控制循环对每个后端独立决策；危险叠加由全局 `MaxConcurrency` 兜底 |
| 误判：后端正常但网络抖动 | 使用滚动窗口（60s）计算错误率和延迟 P99，单次突发不触发降速 |
| 振荡：速率频繁升降 | 引入滞回区（hysteresis）：降速阈值 > 恢复阈值，防止频繁切换 |
| 新部署上线 | 启动时从 `MinRate` 开始，逐渐增加到 `MaxRate`（slow-start 模式） |
| 租户间借用导致公平性问题 | 借用有上限 `BorrowRatio`，且优先保证被借用租户的请求不受影响 |
| 与 K8s HPA 的交互 | 自适应限流减少了需要的副本数；建议配合 HPA 的 `custom metric` 使用 |

### 代码影响范围

| 模块 | 影响 |
|------|------|
| **新增** `internal/backpressure/controller.go` | 拥塞控制循环（观测 → 决策 → 执行） |
| **新增** `internal/backpressure/decider.go` | AIMD 决策算法 + 租户间借用策略 |
| **新增** `internal/backpressure/metrics.go` | 从 OTel 指标收集后端/租户/DB/LLM 实时状态 |
| **修改** `internal/middleware/ratelimit.go` | `RateLimiter` 实现 `RateAdjuster` 接口（`SetRate`/`SetBurst` 原子操作） |
| **修改** `internal/middleware/middleware.go` | `ConcurrencyLimiter` 实现 `RateAdjuster` 接口 |
| **修改** `internal/storage/circuitbreaker.go` | `CircuitBreaker` 支持动态调整失败阈值和半开探测间隔 |
| **修改** `cmd/server/main.go` | 启动拥塞控制循环 |
| **修改** `internal/config` | `CONGESTION_CONTROL_ENABLED` / `CC_*` 配置项 |
| **新增** `internal/telemetry/metrics.go` | 新增 `rate_limit_current_rps{tenant,limiter}`，`congestion_control_adjustments_total{reason}` 指标 |
| **新增** 集成测试 | 用 mock backend 模拟抖动，验证自适应降速和恢复 |

---

## 方向三：🟡 AI Quality Assurance & RAG Evaluation Framework（AI 质量保障与评估框架）

### 现状

当前系统拥有完整的 AI/RAG 管线：
- `Extractor` → `Chunker` → `Embedder` → `Indexer`（写入 BM25 + 向量存储）
- `Search`（向量/BM25/混合检索 + Rerank）
- `Chat`（检索增强生成）
- `Agent`（工具调用循环）
- `PIIDetector`（PII 检测与脱敏）

**但没有任何关于检索质量和生成质量的度量：**

| 质量维度 | 当前状态 | 问题 |
|---------|---------|------|
| 检索精确度（Precision@K） | ❌ 无度量 | 不知道前 K 个结果中多少是相关的 |
| 检索召回率（Recall@K） | ❌ 无度量 | 不知道相关文档是否被召回 |
| 排序质量（MRR / NDCG） | ❌ 无度量 | 不知道相关结果是否排在前列 |
| 答案正确性（Answer Correctness） | ❌ 无度量 | 不知道 LLM 的回答是否正确 |
| 答案忠实度（Faithfulness / Hallucination） | ❌ 无度量 | 不知道 LLM 是否编造了上下文没有的信息 |
| 模型对比（A/B 测试） | ❌ 无能力 | 无法对比不同 embedding/reranking/LLM 的效果 |


### 为什么需要

**1. 没有度量就无法优化。**

当前 AI 管线的配置项（`AI_CHUNK_WINDOW`、`AI_CHUNK_OVERLAP`、`AI_EMBED_DIM`、reranker 选择等）的调整完全依赖"感觉"。没有评估框架，无法判断：
- 窗口 600 vs 800 哪个检索效果更好？
- BM25 + 向量混合 vs 纯向量哪种精度更高？
- 换用 OpenAI v3-large 是否比 text-embedding-3-small 更值得多付 10 倍成本？

**2. 生产部署需要可观测的质量监控。**

上线后：
- 用户的搜索体验如何？检索质量是否在退化？
- 新文档加入后是提升了还是稀释了搜索质量？
- 更换 LLM 供应商后，答案正确性是否下降？

没有质量指标，AI 管线是一个**黑箱**。

**3. 评估框架是 RAG 系统的最佳实践。**

| 框架/工具 | 用途 |
|-----------|------|
| RAGAS（RAG Assessment） | 检索 + 生成的端到端评估 |
| TruLens | RAG 三元组评估（检索质量 × 答案忠实度 × 答案相关性） |
| DeepEval | 单元测试式的 AI 评估 |
| LangSmith | 生产监控 + 标注 + 回归测试 |

### 架构概要

```
RAG Evaluation Framework
==========================

┌─────────────────────────────────────────────────────────────┐
│ 1. Golden Dataset（金标准数据集）                               │
│                                                               │
│   CLI: aero-vault eval create-dataset                          │
│                                                               │
│   数据集格式 (YAML/JSON):                                       │
│   ```yaml                                                      │
│   version: "1.0"                                               │
│   dataset:                                                     │
│     - id: "q001"                                               │
│       query: "2024 年公司营收是多少?"                          │
│       relevant_chunks: ["chunk_123", "chunk_456"]             │
│       expected_answer: "2024 年公司营收为 12.5 亿元"          │
│     - id: "q002"                                               │
│       query: "离职率趋势如何?"                                 │
│       relevant_chunks: ["chunk_789"]                           │
│       expected_answer: ""  # 仅评价检索质量，不评价答案         │
│                                                               │
│   存储: eval_datasets 表 + 文件存储（.eval.yaml）               │
└─────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────┐
│ 2. 评估指标计算                                                │
│                                                               │
│   context_relevance: 检索上下文与查询的相关性                  │
│   └─ Precision@K = 前 K 结果中相关 chunk 的比例               │
│   └─ Recall@K    = 相关 chunk 被召回的比率                    │
│   └─ MRR         = 第一个相关结果的倒数排名                    │
│   └─ NDCG@K      = 归一化折损累计增益                         │
│                                                               │
│   answer_correctness: 答案与标准答案的一致性                   │
│   └─ 基于 LLM-as-Judge：用评估 LLM 给答案打分 (1-5)          │
│   └─ Exact Match (EM): 精确匹配率（适用于事实性问题）          │
│   └─ F1 Score: 答案的 token 级别重叠度                        │
│                                                               │
│   answer_faithfulness: 答案是否基于检索上下文                  │
│   └─ 基于 LLM-as-Judge：逐句验证是否可被上下文支持              │
│                                                               │
│   hallucination_rate: 编造内容的比例                            │
│                                                               │
│   end_to_end_latency: 检索 + 生成的总延迟                     │
│                                                               │
│   cost_per_query: 每次查询的预估成本（美元）                   │
└─────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────┐
│ 3. 评估运行与报告                                              │
│                                                               │
│   CLI:                                                         │
│   aero-vault eval run --dataset company_qa_v1                │
│     → 使用当前配置（embedder/reranker/LLM）运行完整评估        │
│     → 输出 JSON 报告到 stdout 或文件                            │
│                                                               │
│   API:                                                         │
│   POST /v1/admin/eval/run                                     │
│     { "dataset_id": "company_qa_v1" }                        │
│   → 异步执行，返回 job_id                                      │
│   GET /v1/admin/eval/runs/{id}                                │
│   → 返回报告 JSON                                              │
│                                                               │
│   报告示例:                                                    │
│   ```json                                                      │
│   {                                                            │
│     "summary": {                                               │
│       "precision_at_5": 0.82,                                  │
│       "recall_at_10": 0.91,                                    │
│       "mrr": 0.88,                                             │
│       "answer_correctness": 4.2,                               │
│       "faithfulness": 0.95,                                    │
│       "hallucination_rate": 0.03,                              │
│       "avg_latency_ms": 1850,                                  │
│       "avg_cost_usd": 0.0042                                   │
│     },                                                          │
│     "per_query": [...]                                         │
│   }                                                            │
└─────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────┐
│ 4. CI 集成 — 防止 AI 管线退化                                  │
│                                                               │
│   .github/workflows/rag-eval.yml:                             │
│   - name: Run RAG Evaluation                                  │
│     run: aero-vault eval run --dataset regression_suite       │
│   - name: Check Quality Gates                                 │
│     run: |                                                    │
│       aero-vault eval check --run-id ${{ steps.eval.id }}    │
│         --gate precision_at_5=0.75                            │
│         --gate answer_correctness=3.8                         │
│         --gate faithfulness=0.90                              │
│         --gate hallucination_rate=0.05                        │
│                                                               │
│   如果某个门限未通过 → CI 任务失败 → 不允许合并到 main         │
└─────────────────────────────────────────────────────────────┘
```

**LLM-as-Judge 的注意事项：**

评估 LLM 本身可能引入偏见。推荐的实践：
- 使用不同的评估 LLM（如 GPT-4 评估 GPT-3.5 的输出）
- 多次评估取平均
- 对于事实性问题，优先使用精确匹配（EM）而非 LLM 打分
- 评估 LLM 的 Prompt 需要精心设计，包含评分标准的详细描述

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| 评估数据集规模太大（百万级别） | 支持抽样评估（`--sample-ratio 0.1`），自动计算置信区间 |
| 多租户环境 | 每个租户维护独立的数据集和评估运行 |
| 评估期间搜索配置被更改 | 评估运行时锁定当前搜索/聊天配置快照，确保可复现 |
| 评估 LLM 不可用 | LLM-as-Judge 降级为仅评估检索指标（Precision/Recall/MRR） |
| 标准答案在评估后过时 | 数据集版本管理（`eval_datasets` 表支持版本化），记录评估时的时间戳 |
| 评估结果退化 | 自动创建 Prometheus Alert：`eval_precision_at_5 < 0.7` |

### 代码影响范围

| 模块 | 影响 |
|------|------|
| **新增** `internal/eval/dataset.go` | 评估数据集模型（问题/标准上下文/标准答案） |
| **新增** `internal/eval/metrics.go` | 检索指标（Precision/Recall/MRR/NDCG）+ 生成指标（Correctness/Faithfulness） |
| **新增** `internal/eval/runner.go` | 评估运行器：依次查询数据集 → 调用 Search/Chat → 计算指标 |
| **新增** `internal/eval/judge.go` | LLM-as-Judge 适配器（调用评估 LLM 打分） |
| **新增** `internal/eval/gates.go` | 质量门限检查（CI 集成） |
| **新增** `internal/repository/sql_eval.go` | `eval_datasets` / `eval_runs` / `eval_results` 表的 CRUD |
| **新增** `internal/api/rest/admin_eval.go` | REST API：`POST/GET /v1/admin/eval/run`、`GET /v1/admin/eval/datasets` |
| **修改** `internal/cli/cli.go` | 新增 `eval` 子命令组 |
| **新增** 迁移 `0028_eval.up.sql` | `eval_datasets`、`eval_runs`、`eval_results` 表 |
| **新增** SDK 方法 | `CreateEvalDataset`、`RunEvaluation`、`GetEvalReport` |
| **新增** `Makefile` target | `make eval-run`、`make eval-check` |

---

## 方向四：🟡 Performance Benchmarking & CI Regression Detection（性能基准与回归检测）

### 现状

当前项目：
- ✅ **功能测试**完备（单元测试 + 集成测试 + 契约测试）
- ✅ **代码质量门禁**（gofmt、go vet、圈复杂度、文件行数）
- ❌ **没有任何性能基准测试**
- ❌ **CI 中没有性能回归检测**

```bash
$ grep "benchmark\|Benchmark\|bench" internal/*/*.go
# → 零命中（无 *_benchmark_test.go 文件）

$ grep -E "^bench" Makefile
# → 零命中（无 benchmark target）
```

### 代码锚点

| 位置 | 当前状态 |
|------|---------|
| `internal/` 全部子包 | 无 `*_benchmark_test.go` 文件 |
| `Makefile` | 无 `benchmark` / `perf` / `loadtest` target |
| `.github/workflows/ci.yml` | 无性能门禁步骤 |
| `cmd/server/main.go` | 无性能模式（`--perf` 标志） |
| `deploy/` | 无性能测试配置（docker-compose.perf.yml、locustfile） |

### 为什么需要

**1. 性能退化是生产故障的第一大来源。**

在代码评审中，功能逻辑错误容易被发现，但性能退化（一次额外的 DB 查询、一个不必要的 `json.Unmarshal`、一个锁范围扩大）几乎不可能被肉眼发现。**唯一可靠的方式是自动化的性能基准比较。**

**2. 当前代码修改没有安全保障。**

场景：某次修改给 `service.Get` 增加了一个查询 `GetObjectACL`。功能测试全部通过，但每次文件读取都多了一次 SQL 查询。在生产环境中，这可能导致 GET 延迟从 5ms 变为 8ms（+60%），在每秒 1000 次 GET 的负载下导致连接池耗尽。

**3. 有现成的 Go 工具链。**

Go 标准库的 `testing.B` 提供了完善的基准测试框架，`benchstat` 提供统计显著性检测，`pprof` 提供火焰图分析。

### 架构概要

```
Performance Benchmarking & Regression Detection
=================================================

┌──────────────────────────────────────────────────────────────┐
│ Benchmark Suite（基准测试套件）                                    │
├──────────────────────────────────────────────────────────────┤
│                                                               │
│  benchmarks/                                                   │
│  ├── storage/                                                  │
│  │   ├── bench_local_test.go    — 本地存储 Put/Get/List 延迟    │
│  │   └── bench_s3_test.go       — S3 后端 Put/Get 延迟          │
│  ├── service/                                                  │
│  │   ├── bench_put_test.go      — Put 吞吐量（小/中/大对象）    │
│  │   ├── bench_get_test.go      — Get 延迟（缓存/非缓存）       │
│  │   ├── bench_list_test.go     — List 延迟（100/10K/1M 对象）  │
│  │   └── bench_search_test.go   — Search 延迟（向量/BM25/混合） │
│  ├── ai/                                                       │
│  │   ├── bench_embed_test.go    — 本地 HashEmbedder 延迟        │
│  │   ├── bench_search_test.go   — 全文检索延迟                  │
│  │   └── bench_chat_test.go     — MockLLM 端到端延迟             │
│  ├── db/                                                       │
│  │   ├── bench_upsert_test.go   — UpsertObject TPS              │
│  │   ├── bench_query_test.go    — GetObject + ListObjects 延迟  │
│  │   └── bench_search_test.go   — SearchChunks 延迟              │
│  └── integration/                                              │
│       └── bench_full_flow_test.go — 端到端 PUT→INDEX→SEARCH     │
│                                                               │
└──────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────┐
│ Benchmark Runner（基准运行器）                                     │
├──────────────────────────────────────────────────────────────┤
│                                                               │
│  make benchmark — 运行全部基准测试并生成报告                    │
│  ├── go test -bench=. -benchmem -count=10 ./...               │
│  ├── benchstat compare.txt > report.md                        │
│  └── go tool pprof -svg ... > flamegraph.svg                  │
│                                                               │
│  make benchmark-compare — 与基线比较                           │
│  ├── git stash  # 保存当前修改                                │
│  ├── git checkout main                                        │
│  ├── go test -bench=. -count=10 -benchtime=1x > baseline.txt  │
│  ├── git checkout -                                             │
│  ├── git stash pop  # 恢复修改                                │
│  ├── go test -bench=. -count=10 -benchtime=1x > current.txt   │
│  └── benchstat baseline.txt current.txt                        │
│      → 输出:                                                   │
│        name               old time/op  new time/op  delta     │
│        Put_1KB-8           1.23ms ±3%   2.45ms ±5%  +99.19%  │
│        Get_1KB-8           0.89ms ±2%   0.91ms ±3%     ~      │
│        Search_Vector-8    45.2ms ±5%   44.8ms ±4%     ~      │
│                                                               │
└──────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────┐
│ CI Integration（CI 集成 — .github/workflows/benchmark.yml）      │
├──────────────────────────────────────────────────────────────┤
│                                                               │
│  name: Performance Regression Check                           │
│  on: pull_request                                             │
│  steps:                                                       │
│    - uses: actions/checkout@v4                                │
│      with: fetch-depth: 0  # 需要基线分支历史                   │
│    - run: make benchmark-compare -- --threshold 5%            │
│    - name: Check Thresholds                                   │
│      run: |                                                   │
│        if benchstat baseline.txt current.txt | grep -E 'delt  │
│          a.*\+[5-9][0-9]\.'; then                             │
│          echo "Performance regression >5% detected!"          │
│          exit 1                                               │
│        fi                                                     │
│                                                               │
│  每次 PR 自动运行 ≈ 10 分钟的基准测试                           │
│  超过 5% 的退化 → CI 失败 → 要求开发者优化或说明                 │
│                                                               │
│  📌 基准结果自动上传到 GitHub Pages 作为历史趋势                │
│  📌 火焰图只在基准退化时自动生成（减少 CI 时间）               │
│                                                               │
└──────────────────────────────────────────────────────────────┘
```

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| CI 环境与生产环境性能差异 | 基准测试的目的是**比较相对变化**而非测量绝对值。只要基线和新代码在同一环境运行，差异就是有效的 |
| 基准测试运行不稳定（方差大） | `-count=10` 运行 10 次取平均；`benchstat` 自动计算统计显著性（p < 0.05） |
| 网络相关基准（S3 后端） | 使用 local backend 做基准（网络不影响），或使用专门的 benchmark 环境 |
| 基准测试需要太多时间 | 分级：`make benchmark-quick`（~2min，仅核心路径）、`make benchmark-full`（~30min） |
| 偶发 GC 导致延迟抖动 | 使用 `-benchtime=10x` 增加每次基准的迭代次数，GC 的影响会被平均 |
| 内存分配（`-benchmem`） | 记录每次操作的字节分配数，内存泄漏的早期信号 |
| 基准报告历史趋势 | 每次 CI 基准的结果保存到 `benchmark-history.csv`，上传到 S3/GitHub Pages |

### 代码影响范围

| 模块 | 影响 |
|------|------|
| **新增** `benchmarks/storage/bench_local_test.go` | 本地存储读写基准 |
| **新增** `benchmarks/service/bench_put_test.go` | Put 吞吐量基准（1KB/1MB/100MB） |
| **新增** `benchmarks/service/bench_search_test.go` | Search 延迟基准（向量/BM25/混合） |
| **新增** `benchmarks/db/bench_upsert_test.go` | DB 写入 TPS |
| **新增** `benchmarks/integration/bench_full_flow_test.go` | 端到端基准 |
| **修改** `Makefile` | `benchmark`，`benchmark-compare`，`benchmark-quick` target |
| **新增** `.github/workflows/benchmark.yml` | CI 性能回归检测工作流 |
| **新增** `scripts/benchmark-ci.sh` | CI 中的基线比较脚本 |
| **新增** `docs/performance.md` | 性能特征文档（延迟/吞吐量/资源消耗的参考数据） |

---

## 方向五：🟠 Dynamic Tenant Provisioning & Self-Service Portal（动态租户开通与自助门户）

### 现状

当前系统的租户管理流：
1. 运维人员在服务器上设置环境变量（或 admin API）
2. 调用 `POST /v1/admin/tenants` 创建租户
3. 调用 `POST /v1/admin/keys` 生成 API Key
4. 将 API Key 通过带外方式（邮件/聊天）发给用户
5. 用户收到后开始使用

**完全依赖人工运维，没有自助开通能力。**

| 能力 | 当前状态 |
|------|---------|
| 租户创建 | ✅ Admin API（需要 auth scope=admin） |
| API Key 生成 | ✅ Admin API（需要 auth scope=admin） |
| 租户自助注册 | ❌ 无 |
| 邀请码/邀请链接 | ❌ 无 |
| 试用模式 | ❌ 无 |
| 流量分级（免费/专业/企业） | ❌ 无 |
| 用量仪表板 | ❌ 无（只有 admin 可以查看）|
| 自助容量规划 | ❌ 无 |
| 计费信息 | ❌ 无 |
| 通知（配额接近上限） | ❌ 无 |

### 为什么需要

**1. 从"工具"到"平台"的跨越。**

一个 SaaS 平台的核心能力是**用户自助上线**。如果每个新用户都需要运维人员手动创建租户和 API Key，那么：
- 无法提供免费试用（free trial）——**用户获取漏斗的关键转化点**
- 无法支持多租户自助注册（multi-tenant sign-up）
- 每个新用户的边际成本 = 运维时间
- 无法向外部客户开放注册

**2. 代码基础已经完备。**

现有基础设施：
- `tenants` 表（migration `0015`）——持久化租户记录
- `api_keys` 表（migration `0012`）——持久化 API Key（sha256 hash）
- `audit_log` 表（migration `0016`）——审计跟踪
- `quota` / `budget` 字段——容量控制
- `TenantRecord` 的 `status` 字段（active/disabled）——状态管理
- Admin API 的 `CreateTenant`、`AddKey`、`SetQuota`、`SetBudget`

**缺失的是：自助注册前置层 + 邀请机制 + 流量分级 + 用量仪表板。**

### 架构概要

```
Self-Service Tenant Portal
============================

┌─────────────────────────────────────────────────────────────┐
│ 1. 自助注册流程                                               │
│                                                               │
│  POST /v1/signup                                              │
│  {                                                             │
│    "email": "user@example.com",                               │
│    "display_name": "Acme Corp",                               │
│    "plan": "starter"  // starter | pro | enterprise            │
│  }                                                             │
│                                                               │
│  1. 验证 email 格式 + 非空                                     │
│  2. 检查 email 是否已注册（防重复）                            │
│  3. 创建租户记录（status=active, plan=starter）               │
│  4. 生成 API Key（按 plan 分配配额）                           │
│  5. 返回: { tenant_id, api_key, display_name }                │
│                                                               │
│  ⚠️ 注意：无密码/无邮箱验证（Phase 2 可加）                    │
│  当前假设：通过 API Key 进行资源级鉴权，而非用户级身份认证      │
│                                                               │
└─────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────┐
│ 2. 邀请流程（Invitation）                                       │
│                                                               │
│  POST /v1/admin/tenants/{tenant}/invitations                  │
│  { "emails": ["colleague@example.com"] }                      │
│                                                               │
│  1. 生成邀请码（8 位随机令牌）                                 │
│  2. 邀请码关联: { code, tenant, created_by, expires_at }      │
│  3. 返回邀请链接: https://aero-vault.example.com/join/{code}  │
│                                                               │
│  GET /v1/join/{code}                                          │
│  1. 验证邀请码有效（未过期/未使用）                            │
│  2. 返回: { tenant_id, display_name }                         │
│  3. 接受者设置 display_name                                   │
│  4. 生成新的 API Key（归属于该邀请者租户）                      │
│                                                               │
└─────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────┐
│ 3. 流量分级（Plan-based Tiering）                                │
│                                                               │
│  plan 定义:                                                    │
│  ├─ starter:                                                   │
│  │   ├─ storage: 1 GB                                         │
│  │   ├─ objects: 1,000                                        │
│  │   ├─ ai_budget: $5/month                                   │
│  │   ├─ api_rate: 10 RPS                                      │
│  │   └─ features: ["search", "chat"]                          │
│  ├─ pro:                                                       │
│  │   ├─ storage: 100 GB                                       │
│  │   ├─ objects: 100,000                                      │
│  │   ├─ ai_budget: $50/month                                  │
│  │   ├─ api_rate: 100 RPS                                     │
│  │   └─ features: ["search", "chat", "agent", "replication"]  │
│  └─ enterprise:                                               │
│      ├─ storage: unlimited                                    │
│      ├─ objects: unlimited                                    │
│      ├─ ai_budget: custom                                     │
│      ├─ api_rate: custom                                      │
│      └─ features: all                                         │
│                                                               │
│  plan 存储在 plan_templates 配置表（可热加载）                  │
│  租户创建时根据 plan 自动设置 quota + budget + rate limit      │
│                                                               │
└─────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────┐
│ 4. 自助用量仪表板                                              │
│                                                               │
│  GET /v1/usage  — 返回当前租户的实时用量（已实现）            │
│                                                               │
│  新增:                                                         │
│  GET /v1/usage/history?days=30                                │
│  → 返回每日用量趋势（bytes, objects, search_requests,         │
│     chat_requests, ai_cost, total_cost）                      │
│                                                               │
│  GET /v1/usage/bucket-breakdown                               │
│  → 按存储桶拆分的用量明细                                     │
│                                                               │
│  Plan 升级:                                                    │
│  POST /v1/plan/change  { "plan": "pro" }                     │
│  → 立即更新配额（可能有上限检查）                               │
│  → 返回新的配额（租户可看到升级后的容量）                      │
│                                                               │
└─────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────┐
│ 5. 配额接近上限通知                                            │
│                                                               │
│  后台作业（基于 reconcile 框架）                                │
│  每小时扫描一次所有活跃租户:                                    │
│                                                               │
│  if used_bytes / max_bytes > 0.8:                             │
│    发送通知: { tenant, current_usage, plan, upgrade_url }     │
│                                                               │
│  通知通道:                                                     │
│  ├─ 返回在 API 响应头中（X-Aero-Usage-Warning: 82% of quota） │
│  └─ 写日志 + 可选的 Webhook 通知                               │
│                                                               │
└─────────────────────────────────────────────────────────────┘
```

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| 自助注册被滥用 | 可选：邮箱验证（SMTP 发送确认码）+ rate limit on `/signup` |
| 免费计划超过配额 | PUT/GET 返回 `403 QuotaExceeded`（已有逻辑）+ 响应头提示升级 |
| 升级/降级影响现有数据 | 降级时只阻止新的写入，不删除已有数据（如同 AWS S3 行为） |
| 邀请码泄露 | 邀请码可撤销（admin API）+ 可设过期时间（默认 7 天） |
| 多 plan 共享同一租户 | 不支持 — 一个租户一个 plan（如需要多个 plan 共享，创建子账户） |
| Plan 变更需要审批 | enterprise plan 走人工审批（没有自动 `/plan/change`，只有 admin API） |
| 租户自行删除账号 | `DELETE /v1/tenant` → 软删除 → 保留期 30 天 → 管理确认后永久删除 |

### 代码影响范围

| 模块 | 影响 |
|------|------|
| **新增** `internal/api/rest/signup.go` | `POST /v1/signup`（自助注册 + API Key 自动生成）|
| **新增** `internal/api/rest/plan.go` | `GET /v1/plan`、`POST /v1/plan/change` |
| **新增** `internal/api/rest/invite.go` | `POST /v1/admin/tenants/{t}/invitations`、`GET /v1/join/{code}` |
| **新增** `internal/api/rest/usage_self.go` | `GET /v1/usage/history`、`GET /v1/usage/bucket-breakdown` |
| **修改** `internal/repository/sql.go` | `invitations` 表 CRUD、`usage_history` 每日快照 |
| **新增** `internal/config/plan_templates.go` | Plan 模板定义（starter/pro/enterprise 的配额模板） |
| **新增** 迁移 `0029_invitations.up.sql` | `invitations` 表 |
| **新增** 迁移 `0030_usage_history.up.sql` | `usage_history` 表（每日快照） |
| **修改** `internal/reconcile/job.go` | 新增配额接近上限通知扫描 |
| **修改** `internal/telemetry/metrics.go` | 新增 `tenant_active_total{plan}`、`quota_warnings_total` |
| **新增** SDK 方法 | `Signup`、`ChangePlan`、`GetUsageHistory` |

---

## 优先级与实施建议

| # | 方向 | 优先级 | 工程成本 | 商业价值 | 建议顺序 |
|---|------|--------|---------|---------|---------|
| 1 | **Adaptive Backpressure** | P0 | M（跨组件修改） | 🔴 防止生产级联失效 | **#1 优先** — 任何生产部署都需要 |
| 2 | **S3 Select & Object Analytics** | P1 | M（新包 + 协议 handler） | 🟠 S3 生态最大缺口 | **#2** — 增强 S3 兼容性 |
| 3 | **Performance Benchmarking** | P1 | L（基准测试文件 + CI） | 🟠 长期质量保障 | **#3** — 建立基线后 CI 集成 |
| 4 | **Dynamic Tenant Provisioning** | P2 | M（多个新 API + 迁移） | 🟢 多租户平台化 | **#4** — 先有生产稳定性再开放自助 |
| 5 | **RAG Evaluation Framework** | P2 | M（数据集 + 评估引擎） | 🟢 AI 质量可度量 | **#5** — 依赖 AI 管线稳定后 |

**建议实施序列：** `#1 → #2 → #3 → #4 → #5`
1. **自适应背压（#1）**— 任何生产部署的第一道防线，防止后端抖动导致级联失效
2. **S3 Select（#2）**— 完善 S3 协议生态，增强数据湖场景竞争力
3. **性能基准（#3）**— 建立 CI 性能门禁防止回归，与 #1 互补形成"主动防御 + 被动检测"
4. **自助租户开通（#4）**— 多租户平台化的最后一块拼图
5. **RAG 评估（#5）**— 差异化竞争力，让 AI 质量可度量可优化

---

> **去重声明：** 以上 5 个方向均经过对 `docs/requirements/` 下 33 期既有分析文档（v1–v33，累计 ~170+ 方向）+ `docs/ROADMAP.md`（10 方向，全部实现）+ `docs/CHANGELOG.md` + `docs/TODO.md`（全部完成）的逐领域 `grep` 验证。每个方向在既有文档中 **均无实质性独立架构分析**。方向一（S3 Select）在 v32 中仅作为 batch operations 的 manifest 格式被过路提及（一行表格），且 ROADMAP #7 虽然列出 S3 Select 但标注为 `❌ Missing` 且从未展开分析；方向二（Adaptive Backpressure）仅 v20 有一行表格提及；方向三（RAG Evaluation）之前分析聚焦于 PII/监控而非系统化评估框架；方向四（Performance Benchmarking）v18 覆盖了 CLI benchmark 工具但未涉及 CI 性能回归检测；方向五（Dynamic Tenant Provisioning）完全零覆盖。
