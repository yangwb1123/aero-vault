# AeroVault 高价值扩展方向 — 未被触及的系统纵深盲区

> **分析范围：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 全部 ~230+ `.go` 文件 + 48 对迁移文件 + `sdk/*` 三套客户端 + `deploy/*` + `docs/` 全部文档）
> **分析视角：** 资深架构师 / 产品经理 — 聚焦此前 **39 期 expansion 分析（累计 200+ 方向、~300,000+ 字分析文本）+ `docs/ROADMAP.md`（10 大方向）+ `docs/adr/` + `docs/CHANGELOG.md` + `docs/TODO.md`** 中从未实质性触及的系统盲区
> **分析日期：** 2026-07-10
> **去重方法：** 逐方向对 `docs/requirements/` 下全部 39 期既有分析（v1–v39） + `docs/ROADMAP.md` + `docs/CHANGELOG.md` + `docs/TODO.md` 进行完整关键词检索验证，确保每个方向在既有文档中 **零实质性覆盖**。

---

## 背景：前 39 期覆盖了什么

前 39 期 expansion 文档加上 ROADMAP 的 10 大方向，已覆盖以下领域（括号内为方向数）：

| 领域 | 覆盖方向数 | 代表议题 |
|------|-----------|---------|
| AI/RAG 管线（提取/分块/嵌入/搜索/重排/Chat/Agent/PII） | ~30 | 增量 BM25、向量漂移、搜索缓存、远程提取器、提示注入防御 |
| S3 兼容协议（子资源/ACL/Policy/CORS/通知/LegalHold/Select） | ~22 | 服务端拷贝、UploadPartCopy、通知过滤、S3 Select、清单报表 |
| 存储后端（Local/S3/OSS/COS/KMS/SSE/熔断器/分层/迁移） | ~24 | 在线迁移、CAS 存储、SSE 轮换、压缩、KMS 集成 |
| 认证授权（JWT/API Key/SigV4/OIDC/SAML/SCIM/MFA/策略） | ~24 | Key 缓存、跨副本失效、JWT issuer pinning、前缀级权限 |
| 多租户（CRUD/配额/预算/审计/日费用/物理隔离/资源隔离） | ~22 | 租户级存储隔离、加权公平调度、计算资源隔离、Noisy Neighbor |
| 事件/通知/Webhook（总线/传输/过滤/多通道/死信/安全） | ~11 | 事件过滤、多通道分发、Payload 转换、死信队列 |
| 复制/HA/集群（CRR/SRR/单例/Federation/多活/读写分离） | ~12 | 跨区复制规则、CQRS 模式、读取扩展、联邦 |
| Reconcile/GC/Lifecycle（孤儿/保留/Scrub/转换/版本/批量） | ~10 | 分片上传统计、搁置分片 GC、版本修剪、批量操作框架 |
| 合规（WORM/Legal Hold/保留/访问日志/客户端加密/对象锁模式） | ~9 | 治理+合规模式、不可变存储、对象访问轨迹 |
| 可观测性（OTel/Metrics/Grafana/Prometheus/SLO/Tracing/pprof） | ~10 | 分布式追踪、SLO 仪表盘、调试平台、跨组件 span |
| 工程质量（内存安全/流式/并发/压缩/错误模型/测试） | ~8 | 大对象流式加密、SpillBuffer、响应压缩 |
| Web UI / Admin Console | ~6 | 管理控制台、Admin UI 生产化 |
| SDK / CLI 完整性 | ~5 | SDK 开发者体验、导入/迁移工具 |
| 基础设施（TLS/ACME/配置热重载/IP ACL/Feature Flag/Helm） | ~7 | 配置热重载、Helm chart、CDN 集成 |
| 其他（GraphQL/gRPC/批量操作/数据湖/分享链接/成本分配） | ~10 | gRPC 网关、GraphQL 接口、数据目录、成本分配 |

**然而，以下 5 个方向在前 39 期 + ROADMAP 中完全没有被实质触及。** 它们不是"新功能"而是"系统性纵深缺口"——在现有架构中，它们的存在与否决定了 AeroVault 能否从一个功能完备的单机存储系统，升级为企业级的多租户/多区域/高信任平台。

---

## 本期方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 |
|---|------|------|--------|---------|---------|
| 1 | **Tag 驱动的治理引擎** | 特性/合规 | **P1（高）** — Tags 已存储但完全"惰性"；不能驱动生命周期、访问策略、合规操作，导致与 AWS S3 tag-based 治理体系脱节 | `repository.Object.Tags`（已存）→ 无引擎消费 |
| 2 | **通知风暴与事件级联安全保护** | 可靠性 | **P1（高）** — 事件→webhook→写→事件的反馈回路无检测/熔断；重试风暴可导致级联故障 | `events/webhook.go` `events/bus.go` |
| 3 | **分布式/协调式速率限制** | 架构/安全性 | **P2（高）** — 当前限流器为每实例内存令牌桶，多副本部署中用户可以轮换实例突破总限额 | `middleware/ratelimit.go` |
| 4 | **S3 兼容静态网站托管** | 特性/采纳 | **P2（中）** — S3 最受欢迎的特色功能完全缺失；阻碍以 AeroVault 作为前端托管的采纳场景 | `api/s3compat/` 无对应 handler |
| 5 | **混沌工程与韧性验证框架** | 可靠性/运维 | **P2（中）** — 系统有熔断器、集群单例、lease 等韧性模式，但没有任何框架验证它们在故障下实际生效 | `storage/circuitbreaker.go` `cluster/singleton.go` |

---

## 方向一：Tag 驱动的治理引擎（Tag-Driven Governance Engine）

### 现状

Tag 系统已完整实现：

- **存储：** `repository.Object.Tags`（`map[string]string`）——持久化于 `objects` 表的 `tags` 列（JSON）
- **CRUD：** `PUT /v1/files/{key}/tags`、`GET /v1/files/{key}/tags`、`DELETE /v1/files/{key}/tags`、`POST /v1/batch/tag`（批量设置）
- **S3 兼容：** `PUT ?tagging`、`GET ?tagging`、`DELETE ?tagging`（`s3compat/handler.go`）
- **对象列表过滤：** REST `/v1/files` 支持 `?tag=key=value` 参数（`handler.go:List`）

**然而，Tags 完全是"存储即死"——没有任何引擎消费 Tags 来驱动业务逻辑：**

| 能力 | 现状 | AWS S3 对标 |
|------|------|-----------|
| Tag 驱动的生命周期转换 | ❌ 不能根据 tag 决定对象何时降冷/删除 | ✅ S3 Lifecycle `Filter.Tag` |
| Tag 驱动的访问策略 | ❌ 不能根据 tag 允许/拒绝访问 | ✅ S3 IAM `Condition.{StringEquals, ...}` |
| Tag 驱动的合规动作 | ❌ 不能根据 tag 自动加锁/保留 | ✅ S3 Object Lock + Tag |
| Tag 驱动的复制规则 | ❌ 不能根据 tag 选择复制范围 | ✅ S3 CRR Filter by tag |
| Tag 驱动的成本分配 | ❌ 无法按 tag 聚合存储/请求成本 | ✅ AWS Cost Allocation Tags |
| Tag 驱动的索引优先级 | ❌ 不能控制哪些对象的 AI 索引优先级 | ❌（自研优势） |

### 为什么需要

1. **企业治理的基石：** 在实际企业存储场景中，Tags 的价值不在于存储，而在于"标记即策略"。没有 tag 驱动的治理引擎，Tags 只是元数据包袱而非治理工具。
2. **AWS S3 适配关键缺口：** 大量从 S3 迁移的工作负载依赖 tag-based lifecycle 和 tag-based IAM 策略。缺此功能意味着无法承接被管数据/合规性要求的工作负载。
3. **内部架构杠杆：** 该引擎将解锁 `LifecycleJob`（`reconcile/lifecycle.go`）的 tag-based 转换规则、`auth/policy.go` 的 tag-based 访问条件、`replication/replication.go` 的 tag-based 过滤——所有需要的"消费端"基础设施已存在，只缺"供给侧"的引擎。

### 缺失的能力

1. **Tag 条件评估引擎（`internal/tagengine/`）** — 提供 `MatchTags(object Tags, condition TagCondition) bool` 函数，支持 `key=value`、`key exists`、`key!=value`、`AND/OR` 组合。可复用 S3 兼容层的 XML 过滤表达式解析。
2. **Lifecycle Filter by Tag** — 扩展 `BucketConfig.LifecycleRule` 增加 `Filter` 字段（当前 `ExpireAfterDays` 是全桶的）。`LifecycleJob.sweep()` 在删除前检查 `MatchTags`。
3. **Access Policy Condition by Tag** — 扩展 `internal/auth/policy.go` 的 `PolicyDocument.Statement.Condition` 支持 `StringEqualsIfExists("s3:ExistingObjectTag/<key>", "<value>")`。
4. **Batch Tag Action 框架** — 现有 `POST /v1/batch/tag` 只是设置 tags。还需 `POST /v1/batch/tag/action`，根据 tag 条件执行批量操作（删除、移动、锁定、修改存储类）。
5. **Tag 变更事件** — 当对象 tag 被修改时，发布 `object.tags.updated` 事件，触发治理引擎重新评估所有依赖于该 tag 的策略。

### 边界情况与注意事项

- **Tag 变更的治理一致性：** 如果一条 lifecycle 规则基于 `tag=expire=yes` 删除对象，用户在最后一天移除该 tag——对象应不再被匹配（安全活锁？风险是：用户可滥用 tag 移除来规避 lifecycle）。需考虑"tag 变更审计"和"策略评估时刻"（快照 vs. 实时）。
- **批量操作的 ACID：** 跨 10,000 个对象的 tag-based 操作中途失败应部分回滚还是记录进度？建议：每个对象独立执行 + 失败记录到 `jobs` 表 + 可重试。
- **Tag 数量限制：** 当前无每对象 tag 数量限制。S3 限制 10 个/对象。应增加 `MaxObjectTags` 配置项。
- **性能：** Tag-based 过滤 `SELECT * FROM objects WHERE tags->>'key' = 'value'` 在 SQLite/Postgres 上需要 GIN/GIN-like 索引。`tags` 列的 JSON 索引应作为迁移可选添加。

---

## 方向二：通知风暴与事件级联安全保护（Notification Storm & Cascade Safety）

### 现状

事件系统架构如下：

```
User PUT → FileService → EventBus.Publish → 
  → Webhook (postOne + persistFailure / RetryLoop)
  → Indexer (subscribe)
  → Replication Worker (subscribe)
  → SSE streams (subscribe)
  → Antivirus Worker (subscribe)
```

当前保护措施：
- Webhook 失败的持久化重试（指数退避）
- Job dedupe key 防止同事件入队多次
- Postgres transport 的 `origin` 字段防止跨副本回环（`bus.go:87`）

**但是，不存在任何针对事件风暴本身（即大量事件在短时间内被生成和传播）的防护措施：**

| 风险场景 | 当前行为 | 后果 |
|---------|---------|------|
| 批量上传 10 万个文件 | 10 万条 `object.created` 事件瞬间涌入总线 → 所有订阅者同时收到 | Webhook 并发打满、Indexer 队列堆积、SSE channel 满 → 丢弃 |
| Webhook 触发写回（Webhook → 外部系统 → 写回 AeroVault） | 写回生成新的事件 → 再次触发同一 Webhook | 正反馈循环 — "notification storm" |
| RetryLoop 突发恢复 | 积累的 10,000 条失败 webhook 同时开始重试 | 目标系统被压倒（retry storm） |
| 网络分区恢复后的累积事件 | Postgres LISTEN/NOTIFY 积压大量事件，一次性 deliver | SSE 客户端瞬间收到数千事件，浏览器卡死 |

### 为什么需要

1. **生产环境中最常见的事故模式：** 事件级联/callback storm 是分布式系统中最难以诊断的生产事故类型之一（经典案例：AWS S3 的 "eventual consistency" 风暴、GitHub 的 webhook 风暴）。AeroVault 的事件系统目前没有一道防线。
2. **多租户环境中更致命：** 一个租户的异常行为（批量上传 + webhook 回写 + 重复触发）可以拖垮整个系统的 event bus，影响所有租户。
3. **当前基础设施完全未设防：** `events/bus.go` 的 `broadcast()` 是纯并发 `for-range sendOrDrop`——无速率控制、无背压、无熔断。`webhook.go` 的 `deliver()` 无并发限流（当前同时对所有 URL 发请求）。

### 缺失的能力

1. **每订阅者速率限制** — `Bus.Subscribe()` 返回的 channel 应可选关联 `RateLimiter`，在 `broadcast()` 中跳过超过 RPS 阈值的订阅者。对 SSE 客户端：慢消费者自动断开（`context deadline`）。
2. **Webhook 并发控制** — `webhook.Run()` 应对所有 URL 总和施加 `MAX_CONCURRENT_WEBHOOK` 限流（信号量模式），防止重试风暴和目标系统过载。
3. **事件去重窗（Event Dedup Window）** — 对同一 `(event_type, object_id)` 在 N 秒内生成的重复事件，总线应合并/丢弃。例如：5 秒内对同一对象的 3 次修改只生成最后一次事件。
4. **级联检测（Cascade Detection）** — 如果 Webhook A 调用触发了对 AeroVault 的写操作，而该写操作又生成事件试图调用 Webhook A——需要检测此回路并在 N 次迭代后熔断（类似 Agent 的 `MaxSteps`，但针对事件传播圈数）。
5. **事件速率仪表盘** — `telemetry/metrics.go` 新增 `events_published_total{type}`、`events_dropped_total{reason}`（bus buffer 满/dedup/rate-limit）、`webhook_concurrency`、`webhook_storm_detected_total`。

### 边界情况与注意事项

- **去重窗的副作用：** 如果 5 秒去重窗合并了两次 "对象已更新" 事件，依赖每次更新做增量同步的下游将丢失中间状态。需提供不同的去重策略（保留最新 vs. 保留首次 vs. 累计变更）。
- **级联检测的复杂性：** 无法完美检测 Webhook 回路（外部系统可能使用不同的身份/路径写回）。合理简化：限制同一 Webhook URL 在 N 秒内触发的写操作深度 ≤ K。
- **SLA 影响：** 添加速率控制可能影响合法的事件驱动工作流。"硬限速"应在配置中可调，默认保守。
- **与 notification rules 的关系：** 当前桶级 `NotificationRule` 已存储但无投递引擎。本方向推荐先完成基础风暴防护（总线级），再基于规则引擎做 per-rule 限流。

---

## 方向三：分布式/协调式速率限制（Distributed Rate Limiting）

### 现状

当前限流器实现：

```go
// internal/middleware/ratelimit.go
type RateLimiter struct {
    tenants sync.Map // map[string]*tokenBucket (per-tenant)
}
```

- **算法：** 每租户独立令牌桶（`RATE_LIMIT_RPS` / `RATE_LIMIT_BURST` + AI 独立的 `AI_RATE_LIMIT_RPS`）
- **粒度：** 每个 Go 进程独立维护
- **存储：** 纯内存，不可共享

**在多副本部署中的根本缺陷：**

```
                ┌──────────────┐
User ──► LB ──► │ Replica A    │  限流器: 100 RPS
               │ (内存令牌桶)  │
               └──────────────┘
               ┌──────────────┐
          ──►  │ Replica B    │  限流器: 100 RPS
               │ (内存令牌桶)  │
               └──────────────┘
               ┌──────────────┐
          ──►  │ Replica C    │  限流器: 100 RPS
               │ (内存令牌桶)  │
               └──────────────┘

用户实际可达总速率: 300 RPS（3×配置值）
```

### 为什么需要

1. **多副本部署流量放大：** 从方向 #3（ROADMAP: Horizontal scale-out & HA）开始，系统已支持 Postgres 集群单例、共享索引、持久化密钥等跨副本协作。但限流器是"最后一块未跨副本的组件"——任何多副本部署实质上没有全局速率限制。
2. **安全合规硬性要求：** 安全审计中常见的"DDoS 防护有效性验证"要求证明系统的速率限制在 N 个副本下仍然有效。当前无法证明。
3. **AI 成本控制失效：** `AI_RATE_LIMIT_RPS` 在 3 副本下变成 3× 实际速率，绕过日费用预算的防护意图。

### 缺失的能力

1. **后端选型** — 引入 `RateLimitBackend` 抽象，支持三种模式：
   - `local`（当前行为，默认）——内存令牌桶，零依赖
   - `postgres` —— 基于 `SELECT ... FOR UPDATE` 或 Postgres  advisory lock 的分布式计数器。慢但零额外依赖
   - `redis` —— 基于 Redis `EVALSHA`（Lua 脚本实现的滑动窗口/令牌桶）。高性能分布式方案
2. **逐租户配置** — 当前限流器对所有租户应用相同的 RPS/Burst。应支持 `PUT /v1/admin/tenants/{tenant}/ratelimit` 覆盖全局默认值（对高级/付费租户开放更高限额）。
3. **自适应限流（Adaptive Throttling）** — 当存储后端延迟升高或错误率上升时，限流器自动降低有效 RPS（类似 Google SRE 的 client-side throttling 或 Netflix 的 concurrency-limits 算法）。这比固定 RPS 更鲁棒。
4. **限流器指标** — 当前无可见的限流器状态（每个租户的剩余令牌、等待请求数、违反次数）。新增 `rate_limit_violations_total{tenant,route_group}`、`rate_limit_tokens_remaining{tenant}`。

### 边界情况与注意事项

- **Postgres 后端的性能：** 每次请求都 `SELECT ... FOR UPDATE` 会极其昂贵。推荐：Postgres 模式仅作为无需 Redis 的降级方案，且使用本地缓存 + 定期同步（每 100ms 从 DB 拉取当前窗口计数）。Redis 模式才是生产方案。
- **时钟偏差：** 滑动窗口算法依赖 server 时钟。多副本间的时钟偏差（即使 NTP 同步）可能导致窗口边缘不一致。令牌桶算法（不依赖精确时间戳）比滑动窗口更宽容。
- **Graceful degradation：** 当 Redis/Postgres 不可达时，限流器应降级为 local 模式（允许轻微超限，拒绝服务降级比完全开放好），而非熔断拒绝所有请求。
- **与并发限制器的关系：** 速率限制（RPS）和并发限制（in-flight count）是正交的。`MAX_INFLIGHT_REQUESTS` 已实现；速率限制聚焦于每时间窗口的请求频率。两者应共存并独立配置。

---

## 方向四：S3 兼容静态网站托管（Static Website Hosting）

### 现状

静态网站托管是 AWS S3 最受欢迎的"非存储"特性之一。AeroVault 当前支持完整的 S3 对象 CRUD 协议（`s3compat/`），但**没有任何静态网站托管能力**：

| 能力 | AWS S3 | AeroVault |
|------|--------|-----------|
| `GET /` → index document | ✅ `IndexDocument.Suffix` | ❌ 返回 404 或目录列表 |
| `GET /nonexistent` → error document | ✅ `ErrorDocument.Key` | ❌ 返回原始 JSON 错误 |
| 自定义重定向规则 | ✅ `RoutingRules` | ❌ 无 |
| `?website` 子资源 | ✅ `GET/PUT ?website` | ❌ 无 |
| CORS 集成 | ✅ per-bucket CORS | ✅ 全局 CORS（已有） |

### 为什么需要

1. **S3 兼容性标志性缺口：** 静态网站托管是开发者评估 S3 替代品时最常检查的特性之一。缺失此功能会让前端开发者直接放弃评估。
2. **低增量成本：** AeroVault 已有完整的基础设施——FileService、chi 路由、per-bucket config（buckets 表已有 `cors_rules` 列可扩展）、范围请求（Range/conditional）、缩略图。静态网站的核心理念只是"把 GET 请求路由到指定 key 作为默认文档"。
3. **拓展应用场景：** 支持静态网站托管后，AeroVault 可以用于托管 SPAs（Vue/React/Angular 构建产物）、API 文档站点、静态博客等——大幅拓展"存储+托管"的场景。

### 缺失的能力

1. **Bucket 网站配置** — 扩展 `BucketConfig` 增加 `Website` 字段：
   ```go
   type WebsiteConfig struct {
       Enabled       bool            `json:"enabled"`
       IndexDocument string          `json:"index_document"` // 默认 "index.html"
       ErrorDocument string          `json:"error_document"` // 可选
       RoutingRules  []RoutingRule   `json:"routing_rules,omitempty"`
   }
   ```
2. **S3 兼容的子资源** — `GET /s3/{bucket}?website`、`PUT /s3/{bucket}?website`、`DELETE /s3/{bucket}?website`，返回 S3 标准 XML 格式。
3. **请求重定向引擎** — 在 `s3compat/handler.go` 的 `BucketDispatch` 中，如果 bucket 启用了 Website 模式：
   - `GET /` → 返回 `IndexDocument` 的内容
   - `GET /some/path/` → 尝试 `GET /some/path/` + `IndexDocument`（S3 行为：`/some/path/` → `/some/path/index.html`）
   - `GET /nonexistent` → 返回 `ErrorDocument` 的内容（HTTP 200，因为 error document 可能包含美化错误页）
   - `RoutingRules` 按优先级匹配（key prefix + HTTP code 条件 → 替换/重定向）
4. **REST API** — `GET /v1/buckets/{bucket}/website`、`PUT /v1/buckets/{bucket}/website`、`DELETE /v1/buckets/{bucket}/website`
5. **公共读覆盖** — 网站托管要求所有对象公开可读（即使 bucket ACL 是私有的）。需要一个显式的 `public-read` 标志或与 `AUTH_ANONYMOUS_PUBLIC_READ` 集成。

### 边界情况与注意事项

- **Index document 中的相对路径：** SPA 路由（如 `/app/users`）在没有服务器端路由支持时，需要将所有路径重写到 `index.html`。`RoutingRules` 中应内置 `Condition: HttpErrorCodeReturnedEquals: 404` → `ReplaceKeyPrefixWith: ""` + `Redirect: ReplaceKeyWith: index.html` 的惯用模式。
- **安全风险：** 启用网站托管的 bucket 本质上将所有对象暴露为可访问 URL。必须确保：
  - 不暴露本不应公开的 object（如备份文件 .zip）
  - 可通过 `WebsiteConfig.RoutingRules` 禁止特定前缀
  - 有 `robots.txt` 自动生成或代理选项
- **Content-Type 推断：** 对于无扩展名的路径（如 `/about`），S3 的网站模式默认返回 `text/html`。AeroVault 需要一个 MIME 映射表或允许用户配置。
- **跨桶重定向：** S3 的 `RoutingRule.Redirect` 支持重定向到另一个 bucket 或外部 URL。需实现 HTTP 301/302 响应。
- **性能：** 静态网站托管通常意味着高 QPS 小对象请求。`MAX_INFLIGHT_REQUESTS` 和 `RATE_LIMIT_RPS` 需要相应的调整建议。

---

## 方向五：混沌工程与韧性验证框架（Chaos Engineering & Resilience Verification）

### 现状

AeroVault 已有以下韧性模式：

| 模式 | 组件 | 说明 |
|------|------|------|
| 熔断器（Circuit Breaker） | `storage/circuitbreaker.go` | 对 S3/OSS/COS 后端调用包裹熔断器 |
| 集群单例（Singleton） | `cluster/singleton.go` | Lease 机制确保后台任务只在一个副本运行 |
| 超时控制 | `middleware/timeout.go` | AI 路由组可配置请求超时 |
| 并发限制器 | `middleware/middleware.go` | `MaxInFlight` 全局 + `PerTenantMax` 可选 |
| 死信 + 重试 | `jobs/jobs.go` | Job `max_attempts` 达到后标记 `failed` |
| 降级模式 | `config` `AI_DEGRADED_MODE` | AI 不可用时 `/search` 返回降级响应 |

**但是，完全没有验证这些韧性模式在实际故障场景中是否按预期工作的框架：**

| 问题 | 影响 |
|------|------|
| 熔断器从未被实际触发过 | 代码逻辑可能有 bug，但在 CI/集成测试中从未被验证 |
| 集群单例的 lease 断裂后能否顺利接替？ | 未在"副本突然死亡"场景测试过 |
| Job 池的 `max_attempts` 触发后数据一致性如何？ | 未验证部分完成的 job 是否有副作用 |
| 熔断器打开后，FileService 的错误传播链路是否正确？ | 未集成测试从 S3 错误 → 熔断器打开 → HTTP 503 的全链路 |

### 为什么需要

1. **韧性模式的"可信度缺口"：** 代码中存在熔断器、单例、重试等模式不等于系统在故障发生时真的安全。没有混沌实验验证，这些模式只是"理论上存在的护栏"。生产事故中，未被验证的韧性模式往往变成"虚假的安全感"。
2. **SRE 运维的基础设施：** 对于达到生产规模的系统，混沌工程不是可选项——它是 SRE 团队建立"系统行为可预测性"信心的必经之路。AeroVault 的 ROADMAP 方向 #6 "Production resilience" 已经列入了熔断器需求，但未包含验证这些熔断器的手段。
3. **低增量成本：** 利用已有的框架（`httptest`、`testcontainers`、`integration_test.go` 的 build tags、`HARNESS.md` 的 `make check` 流程），混沌测试可以增量添加到现有的测试框架中，无需额外的基础设施依赖。

### 缺失的能力

1. **故障注入接口（Fault Injection Interface）** — 在关键边界定义可开关的故障注入点：
   - `storage.Storage` —— `ReadDelay(duration)`、`WriteError(probability)`、`StatTimeout`
   - `repository.Repository` —— `QueryTimeout`、`ConnectionDrop`
   - `events.Bus` —— `DeliverDelay`
   - `ai.Embedder` / `ai.LLM` —— `Latency(duration)`、`ErrorRate(probability)`
   
   实现方式：`FaultyStorage` 包装器实现 `storage.Storage`，接受 `FaultConfig`，在 CI 行为测试中启用。

2. **混沌测试套件（Chaos Test Suite）** — 用 `//go:build chaos` 标签隔离的集成测试：
   - `TestCircuitBreakerOpensOnHighErrorRate`：配置 FaultyStorage 返回 50% 错误 → 验证 5 次后熔断器打开 → 后续请求直接返回 `ErrBackendUnavailable`
   - `TestCircuitBreakerHalfOpenRecovery`：熔断器打开后停掉故障注入 → 验证经过 `RecoveryTimeout` 后熔断器进入 half-open → 成功请求后关闭
   - `TestSingletonLeaseTakeover`：启动两个集群单例 → 杀死第一个 → 验证第二个在 lease 过期后接替 → 验证无重复执行
   - `TestJobDeadLetterOnMaxAttempts`：注册一个永远失败的 job handler → 入队 → 验证 `max_attempts` 后 status = `failed`
   - `TestBusDroppedEventsOnFullChannel`：用小 buffer 订阅 → 发布超过 buffer 数量的事件 → 验证 `events_dropped_total` 增加

3. **运行时混沌注入端点** — 可选（生产环境需认证保护）的 REST API：
   - `POST /v1/admin/chaos/inject`：`{"target": "s3", "fault": "latency", "duration": "30s", "params": {"delay_ms": 5000}}`
   - `POST /v1/admin/chaos/reset`：清除所有活跃故障注入
   
   这允许 SRE 在 staging 环境甚至生产环境（金丝雀）执行"按需混沌实验"。

4. **韧性验证 Runbook** — `docs/chaos/` 下的一组文档化的混沌实验，每个包含：前提条件 → 注入故障 → 预期系统行为 → 观测指标 → 回滚步骤。例如：
   - 实验 1：S3 后端完全不可用 2 分钟
   - 实验 2：Postgres 连接池耗尽
   - 实验 3：AI Embedder 延迟飙升到 10 秒
   - 实验 4：同时杀死一个副本

### 边界情况与注意事项

- **生产安全：** 运行时故障注入 API 必须绑定 admin scope、有操作审计日志（`audit_log` 表已在方向 #4 中实现）、需要二次确认（`X-Chaos-Confirm: yes` 头）。仅限 staging 环境默认启用，生产需显式配置 `CHAOS_ENABLED=true`。
- **Cleanup 保证：** 每个混沌实验必须保证故障注入在 `duration` 后自动清理（即使控制平面崩溃）。使用 `context.WithTimeout` + `defer` 模式。
- **测试环境隔离：** `chaos` build tag 的测试不应纳入 `make check` 的常规 CI 流程（太慢、不可预测）。应作为独立的 `make chaos-test` 目标，定时（nightly）或在发布前手动触发。
- **误报风险：** 混沌测试可能因测试环境资源争用而假阳性失败。所有断言应有明确的超时和重试机制（预期系统在 30 秒内恢复，而非立即）。

---

## 附录：方向间的关联与建议优先级

### 依赖关系

```
方向四（Static Website）
  └─ 基础依赖：S3 协议已有（🟢 Ready）

方向一（Tag Governance）
  ├─ 消费端：LifecycleJob（已有）→ 新增 Filter by Tag
  ├─ 消费端：auth/policy.go（已有）→ 新增 Condition by Tag
  └─ 消费端：replication（已有）→ 新增 Filter by Tag

方向二（Storm Protection）
  ├─ 依赖：events/bus.go 已有 → 新增速率 + 级联防护
  ├─ 依赖：webhook.go 已有 → 新增并发控制
  └─ 增强：方向三（Distributed Rate Limiting）的 Redis 模式可复用

方向三（Distributed Rate Limiting）
  └─ 增强：方向二的每订阅者限流可复用此基础设施

方向五（Chaos Engineering）
  └─ 最强验证目标：方向二（Storm Protection）+ 方向三（DRL）的韧性
```

### 建议实施顺序

| 阶段 | 方向 | 理由 |
|------|------|------|
| Phase 1 | **方向二（风暴防护）— 基础层** | 最低成本、最高即时效用；保护现有的事件/Webhook 系统不受最常见生产事故影响 |
| Phase 2 | **方向三（分布式限流）** | 多副本部署的前提条件；与方向二的每订阅者限流共享基础设施 |
| Phase 3 | **方向一（Tag 治理引擎）** | 业务价值最高但工程量最大；需与现有的 Lifecycle/Auth/Replication 三个子系统协同变更 |
| Phase 4 | **方向五（混沌工程）** | 在 Phase 1–3 的韧性模式就位后再验证它们——先有护栏，再测护栏是否牢固 |
| Phase 5 | **方向四（静态网站托管）** | 独立特性、独立价值——可随时开始，不影响其他方向的排期 |

---

*本文不包含任何实现代码。每个方向均已从前 39 期 analysis 文档 + ROADMAP + TODO + CHANGELOG + ADR 中做了完整关键词去重验证，确保视角新颖。*
