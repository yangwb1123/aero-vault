# AeroVault 高价值扩展方向（第五期）

> **视角:** 资深架构师 / 产品经理  
> **方法:** 全局代码扫描（Go 源码 ~45K 行），综合 ROADMAP + 八轮 analysis-v[1-8] + 四期 expansion-directions 评估后，选取**仍未覆盖**的方向  
> **日期:** 2026-07-10  
> **原则:** 每个方向附带具体代码位置、当前状态缺口、架构蓝图和实现理由（Why Now）。不编写任何实现代码。

---

## 总览

| # | 方向 | 类型 | 影响 | 代码锚点 | 覆盖检查 |
|---|------|------|------|---------|---------|
| 1 | **Federated Identity & SSO（OIDC/OAuth2/SAML/SCIM）** | 安全/生态 | 🔴 企业采购准入 | `internal/auth/*` | 全部未覆盖 |
| 2 | **Cold Storage / Deep Archive Tier & Async Restore** | 成本/功能 | 🔴 生产级存储必备 | `internal/service/*`, `internal/storage/storage.go` | ROADMAP #9 仅泛提 |
| 3 | **Enterprise Event Streaming (Kafka / NATS / RabbitMQ)** | 集成/生态 | 🟠 事件驱动架构断裂 | `internal/events/*` | 全部未覆盖 |
| 4 | **AI Pipeline Quality & Search Observability Framework** | AI/可观测 | 🟠 从"有 RAG"到"可信 RAG" | `internal/ai/*` | 全部未覆盖 |
| 5 | **Multi-Cluster Federation & Global Namespace** | 架构/规模 | 🔴 水平扩展天花板 | `internal/replication/*`, `internal/cluster/*` | expansion-v4 #4 方向不同 |

---

## 1. Federated Identity & SSO（OIDC / OAuth2 / SAML / SCIM）

### 为什么需要它

当前认证系统（`internal/auth/`）支持 API Key（sha256 哈希）、HS256 JWT、AWS SigV4 三种模式。这足以支撑 MVP 和开发环境，但**企业采购技术评审的必经关卡**是 SSO 集成：

- 客户运行 Okta / Azure AD / Keycloak / Auth0，**无法接受为 AeroVault 单独管理一套 API Key 和 JWT Secret**。
- **无 RBAC/ABAC 模型**：当前 scope 只有 `read/write/admin` 三个级别，企业需要 `{department}.{project}.{role}` 粒度的细权限。
- **无 SCIM 用户/组同步**：人员离职后密钥无法自动撤销，形成账号生命周期管理的安全漏洞。
- **无 OAuth2 授权码流**：SDK/CLI 用户需要弹浏览器登录而不是复制粘贴 token。

这是**产品化与"玩具"的分水岭**。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/auth/auth.go` | `Registry` 支持 Key + JWT + SigV4 | 无 OIDC/OAuth2 provider |
| `internal/auth/auth_middleware.go` | 中间件从 Header 提取凭据 | 无 Authorization Code 流重定向 |
| `internal/auth/jwt.go` | HS256 签发/验证 | 无 RS256/ES256、无 JWKS 端点、无 `kid` 轮换 |
| `internal/auth/store.go` | `PersistentStore` + `KeyCache` | 无用户/组模型 |
| `internal/api/rest/admin.go` | 管理 API Key CRUD | 无 SCIM `/Users` `/Groups` 端点 |
| `internal/auth/policy.go` | IAM-style policy 解析 | 无 ABAC 属性引擎 |
| `internal/config/config_auth.go` | `AuthConfig` | 无 OIDC 发现 URL / ClientID / ClientSecret |

### Edge Cases 暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **Token 过期竞态** | 用户浏览器发并发请求，access_token 在中间某请求过期 | 第一个成功，其余 401 | 透传 401 + `WWW-Authenticate` header |
| **OIDC 发现端点超时** | IdP 故障（Azure AD 限流） | 服务启动失败或挂起 | 降级到缓存 + 返回 401 而非 crash |
| **JWKS 密钥轮换** | IdP 发布新 key，旧 key 标记为 `kid` 过期 | 需要硬重启服务 | 自动轮换：后台 goroutine 定期刷新 JWKS |
| **SCIM 增量同步** | 用户被移出 AD 组 | 直到管理员手动删除 key 才生效 | webhook 接收 SCIM PATCH → 自动撤销 |
| **多 IdP 共存** | 收购合并后两个 IdP 并存 | 不支持 | 每条 token 的 `iss` 映射到对应 IdP 的 JWKS |
| **SAML 跨域登录** | Cloud 版需要 SP-initiated SSO | 不支持 | REST API `/v1/auth/saml/acs` |
| **授权码 replay** | OAuth2 code 被中间人截获 | 无防护 | PKCE (S256) + 一次性 code + 短 TTL |
| **OAuth2 scope 膨胀** | 同一 tenant 的 admin 和 dev 需要不同 scope | 只有全局三个 scope | scope 模版 + 租户级 scope 映射 |

### 架构蓝图

```
┌─ 新增: internal/auth/oidc.go ──────────────────────────────────┐
│ type OIDCProvider struct {                                      │
│     Issuer     string    // https://accounts.google.com         │
│     ClientID   string                                           │
│     JWKSURI    string    // 从 .well-known/openid-configuration  │
│     jwksCache  *JWKSCache                                       │
│     alg        string    // RS256 / ES256                       │
│ }                                                               │
│ func (p *OIDCProvider) Verify(ctx, token) (*Claims, error)      │
│                                                                  │
│ JWKSCache 实现:                                                  │
│   - 启动时加载 JWKS                                             │
│   - 后台定期刷新（支持 304 Not Modified）                        │
│   - 线程安全，读写锁                                              │
└────────────────────────────────────────────────────────────────┘

┌─ 新增: internal/auth/scim.go ──────────────────────────────────┐
│ 实现 SCIM 2.0 服务端端点:                                        │
│   GET/POST /Users                                                │
│   GET/PUT/PATCH/DELETE /Users/{id}                              │
│   GET/POST /Groups                                               │
│   GET/PUT/PATCH/DELETE /Groups/{id}                             │
│ /Schemas, /ResourceTypes, /ServiceProviderConfigs                │
│                                                                  │
│ 事件集成:                                                         │
│   SCIM PATCH /Users/{id} deactivate → EventBus → Key revocation  │
│   SCIM PATCH /Groups/{id}/members → EventBus → Rebuild ACL cache │
└────────────────────────────────────────────────────────────────┘

┌─ Auth 中间件扩展 ──────────────────────────────────────────────┐
│ auth.Registry.Middleware() 新增模式:                             │
│   1. 尝试提取 Bearer token                                      │
│   2. 按 iss 匹配已注册的 OIDCProvider                            │
│   3. 验证 JWT (kid → JWKS, exp, iss, aud)                       │
│   4. 从 token claims 中提取 tenant/scope                          │
│   5. 刷新 rate-limiter 的 tenant 计数器                          │
│   6. 写入 context: tenant, user_id, scopes                      │
│   7. 缓存已验证 token（可选，可配置 TTL）                         │
│                                                                  │
│ 降级策略:                                                         │
│   - JWKS 刷新失败 → 使用本地缓存（不阻断已有 token）               │
│   - IdP 完全不可达 → 降级到 API Key + JWT（遗留兼容）              │
│   - `Authentication: Bearer <token>` 和 `X-Api-Key: <key>` 并存  │
└────────────────────────────────────────────────────────────────┘

┌─ RBAC/ABAC 引擎 ──────────────────────────────────────────────┐
│ 当前: scope = "read" | "write" | "admin"                        │
│ 目标:                                                           │
│   内置策略文档路径: internal/auth/policy.go (已有 IAM JSON 解析)  │
│   扩展为:                                                        │
│   {                                                             │
│     "effect": "Allow",                                          │
│     "actions": ["s3:GetObject", "ai:Search"],                   │
│     "resources": ["arn:aero:tenant-42:bucket-prod/*"],          │
│     "conditions": {                                             │
│       "IpAddress": {"aws:SourceIp": "10.0.0.0/8"},             │
│       "StringEquals": {"aero:department": "engineering"}        │
│     }                                                           │
│   }                                                             │
│   条件引擎: 评估 request context 中的属性                          │
└────────────────────────────────────────────────────────────────┘

### 为什么现在做

已有 `auth.Registry` + `PersistentStore` + `KeyCache` 的基础设施让 OIDC 集成有干净的落地位置。SCIM 可以利用 `internal/repository/tenants.go` + `internal/repository/apikeys.go` 的存储层。这是**企业销售流程的 G2 里程碑**。

---

## 2. Cold Storage / Deep Archive Tier & Async Restore

### 为什么需要它

当前 `StorageClass` 字段（`STANDARD` / `STANDARD_IA` / `GLACIER`）**仅作为元数据存储**（`internal/service/file.go:StorageClassOrDefault`），没有任何实质性分层行为：

- GLACIER 和 DEEP_ARCHIVE 类对象必须**不可直接被读取**——读取前需要通过 `RestoreObject` 发起异步恢复，等待数小时，返回临时副本。
- 没有**自动分层策略**：对象从创建开始没有生命周期规则来将它从 STANDARD → STANDARD_IA → GLACIER → DEEP_ARCHIVE。
- 没有**恢复队列**：一批对象的恢复请求需要排队 + 进度通知。
- 没有**恢复计费计量**：恢复出站流量需要计入 egress 配额。

这是 AWS S3 存储产品中最核心的成本优化手段。缺少它，AeroVault 在大数据场景下的 TCO 竞争力严重不足。

`ROADMAP.md` 方向 #9 泛提了 "Storage tiering & intelligent lifecycle"，但未深入分析异步恢复模型和冷存储的具体实现挑战。这是本方向的区分点。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/storage/storage.go:Storage` 接口 | 无 `RestoreObject` 方法 | 存储后端不支持冷存储恢复 |
| `internal/storage/local.go` | 所有对象均为即时可读 | 无冷对象标记 / 暂存区 |
| `internal/storage/s3.go` | 直接调用 S3 GET | 未处理 `InvalidObjectState`（对象在 GLACIER 中） |
| `internal/service/file_crud.go` | `Get` 直接流式读取 | 无 GLACIER 拦截 + 返回 `RestoreInProgress` |
| `internal/service/file.go:StorageClassOrDefault` | 仅作为默认值 | 无分层调度逻辑 |
| `internal/repository/repository.go:BucketConfig` | 只有 `ExpireAfterDays` | 无 `TransitionDays` / `TransitionClass` |
| `internal/reconcile/lifecycle.go` | 仅处理过期删除 | 无分层 transition |
| `internal/jobs/jobs.go` | 通用 job 队列 | 无恢复 job 类型 |
| `internal/api/s3compat/handler.go` | S3 GET 处理 | 无 `x-amz-restore` 头响应 |
| `internal/api/rest/handler.go` | GET 直接流式输出 | 无 GLACIER 错误码 |

### Edge Cases 暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **恢复中并发读取** | 请求 GLACIER 对象，恢复进行中（20% 完成） | 返回 `StorageError` 或 HTTP 500 | 返回 `200` + `x-amz-restore: ongoing-request="true"` header |
| **恢复过期** | 临时副本 24h 后自动删除，用户仍持有 URL | 返回 403 | 返回 `InvalidObjectState` + 提示重新恢复 |
| **批量恢复** | 恢复 10 万个 GLACIER 对象的项目 | 手动逐个调用无法接受 | `POST /v1/batch/restore` + 异步 job |
| **恢复队列积压** | 大量恢复请求超过后端读取带宽 | 全部并发执行导致 OOM | 每个租户独立恢复队列 + 速率限制 |
| **分层回退** | 对象从 GLACIER → STANDARD（需要重新写入） | 不支持 | `RestoreObject` 参数 `Days=0` → 永久恢复（相当于迁移） |
| **存储类校验** | PUT STANDARD 到仅支持 GLACIER 的 bucket | 静默成功 | 拒绝 + `InvalidStorageClass` |
| **Tiering 循环** | 生命周期规则：STANDARD→GLACIER→STANDARD→GLACIER ... | 不支持 | 每个对象记录 `last_transition_at`，防止抖动 |
| **最小存储期违规** | 30 天最小存储期，删除发生在第 5 天 | 立即删除（产生存储费用） | 删除时计费 `remaining_storage_period * rate` |

### 架构蓝图

```
┌─ 新增: storage.RestoreObject ──────────────────────────────────┐
│ type RestoreRequest struct {                                    │
│     Key              string                                     │
│     Tier             RestoreTier // "Bulk" (免费) | "Standard"  │
│     Days             int        // 临时副本存活天数              │
│     PermanentRestore bool       // true = 永久迁移到 STANDARD   │
│ }                                                               │
│                                                                  │
│ Storage 接口新增:                                                 │
│   RestoreObject(ctx, req RestoreRequest) (RestoreInfo, error)    │
│   RestoreInfo { InProgress bool, Expiry time.Time }             │
│                                                                  │
│ local 实现:                                                      │
│   GLACIER 对象 → 改名标记 + 延迟触发解冻（模拟）                   │
│   "恢复" 实际是：shallow copy 到 hot tier + TTL 标记              │
│                                                                  │
│ S3 实现:                                                         │
│   调用 S3 RestoreObject API                                      │
│   轮询 HEAD 的 x-amz-restore header 感知完成                     │
└────────────────────────────────────────────────────────────────┘

┌─ 新增: 异步恢复状态机 ─────────────────────────────────────────┐
│                                                                  │
│  请求恢复                                                         │
│     │                                                             │
│     ▼                                                             │
│  ┌──────────────┐                                                │
│  │  Pending      │ ← 等待队列（job pool 调度）                     │
│  └──────┬───────┘                                                │
│         │                                                         │
│         ▼                                                         │
│  ┌──────────────┐   HEAD 返回 x-amz-restore: ongoing-request="true"│
│  │  InProgress   │───────────────────────┐                        │
│  └──────┬───────┘                        │retry-after            │
│         │ restore complete               │                        │
│         ▼                                │                        │
│  ┌──────────────┐   HEAD 返回 x-amz-restore: expiry-date="..."   │
│  │  Restored     │───────────────────────┘                        │
│  │  (hot copy)   │                                                │
│  └──────┬───────┘                                                │
│         │ after expiry                                           │
│         ▼                                                         │
│  ┌──────────────┐                                                │
│  │  Expired      │ 需要重新恢复                                    │
│  └──────────────┘                                                │
│                                                                  │
│ 存储: restore_jobs 表（复用 jobs 表扩展）                          │
│   表: restore_requests                                            │
│     object_id, tenant, tier, days, status, completed_at, expiry  │
│   后台: restore_reaper 定期清理过期临时副本                          │
└────────────────────────────────────────────────────────────────┘

┌─ 新增: 自动分层调度器 ──────────────────────────────────────────┐
│ type TieringTransition struct {                                   │
│     AfterDays      int         // 写入后 N 天触发                 │
│     TargetClass    string      // STANDARD_IA / GLACIER / DA      │
│ }                                                                 │
│                                                                   │
│ BucketConfig 扩展:                                                 │
│   TieringRules []TieringTransition                                 │
│                                                                   │
│ reconcile.Lifecycle 扩展:                                          │
│   扫描 bucket 的 TieringRules                                     │
│   对满足条件的对象执行 Transition（storage key 不动，仅更新 class） │
│   若 Transition 到 GLACIER：验证对象是否已解冻，再归档              │
│   每个 transition 记录审计日志                                      │
│                                                                   │
│ 配置示例:                                                          │
│   Lifecycle rule:                                                  │
│     after 30 days → STANDARD_IA                                    │
│     after 90 days → GLACIER                                        │
│     after 365 days → DEEP_ARCHIVE                                  │
│     after 730 days → expire                                        │
└────────────────────────────────────────────────────────────────┘

### 为什么现在做

存储分层+异步恢复是对象存储产品化核心竞争力的前提。当前 `StorageClass` 字段和 `reconcile.Lifecycle` 已经是现成的扩展点。冷存储策略是企业客户**数据量大之后 3-6 个月内必提的需求**，现在做比客户提再做从容得多。

---

## 3. Enterprise Event Streaming（Kafka / NATS / RabbitMQ）

### 为什么需要它

当前事件系统（`internal/events/bus.go`）有完善的 in-process 总线 + 持久化 DB + SSE 流 + Webhook + Postgres LISTEN/NOTIFY 传输。但对于**企业事件驱动架构**，这些选项远远不够：

- **Kafka 生态集成缺失**：Confluent 平台、Debezium CDC、KSQL 流处理、Kafka Connect 连接器生态完全无法接入。
- **无事件过滤/路由**：Webhook 接收所有事件（`bus.Subscribe()` 直接消费），无法按 event type / bucket / prefix 过滤——导致下游服务收到大量不相关事件。
- **无死信队列（DLQ）**：`NextUnconsumedEvents` 轮询模式无法区分"消费失败"和"尚未消费"。
- **无 at-least-once / exactly-once 语义**：`MarkEventConsumed` 只有一个状态位，不能处理幂等重放。
- **无 CloudEvents 规范**：事件 payload 没有标准化 `specversion` / `source` / `id` / `datacontenttype` 字段，无法与 Knative / Azure Event Grid / Google Eventarc 互操作。

这是从"单机事件通知"到"云原生事件网格"之间的断裂。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/events/bus.go` | in-process bus + subscriber channels | 无外部消息队列集成 |
| `internal/events/postgres_transport.go` | LISTEN/NOTIFY，单 channel | 无 Kafka/RabbitMQ/NATS 传输 |
| `internal/events/webhook.go` | 单一 URL 的 HTTP POST | 无多目标路由、无事件过滤 |
| `internal/repository/sql_events.go` | events 表 + unconsumed polling | 无 DLQ 表、无重试次数限制 |
| `internal/api/rest/sse.go` | SSE 流（全部事件） | 无过滤参数（?type=created&prefix=docs/） |
| `internal/api/rest/router.go` | `/v1/events/stream` 路由 | 无消费组、无断线重连的序列号 |
| `internal/config/config.go:EventsConfig` | 只有 webhook + transport | 无 kafka/nats 配置 |

### Edge Cases 暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **消费端背压持续** | Kafka 消费慢 → 偏移量落后 | DB 增长无限 | 支持 compaction + 生产者 flow control |
| **事件顺序重排** | 同一对象的 `created` 和 `deleted` 乱序到达 | 下游可能先处理 deleted 再处理 created | 每个 object 的 partition key 保证顺序 |
| **重放风暴** | Webhook 不可达 → 重试队列积压 → 恢复后大量重放 | 可能打垮下游 | 渐进 backoff + 重放限流 |
| **跨数据中心事件复制** | Kafka 集群跨 AZ 复制延迟 | 不支持 | 主动延迟 + 一致性边界配置 |
| **事件 Schema 演化** | 新增 Payload 字段 → 老消费者收到未知字段 | 静默忽略或 panic | 向后兼容 + CloudEvents schema registry |
| **断线重连** | SSE 客户端断线，错过中间事件 | 永久丢失 | 支持 `Last-Event-ID` 序列号回追 |

### 架构蓝图

```
┌─ 新增: events 传输层抽象 ──────────────────────────────────────┐
│ type Transport interface {                                      │
│     Publish(ctx, topic string, e repository.Event) error        │
│     Subscribe(ctx, topic string, handler Handler) error         │
│     Close() error                                               │
│ }                                                                │
│                                                                  │
│ 现有: PostgresTransport（LISTEN/NOTIFY）→ 实现 Transport         │
│ 新增: KafkaTransport                                            │
│ 新增: NATSTransport                                             │
│ 新增: RabbitMQTransport                                         │
│                                                                  │
│ 注册方式: EVENTS_TRANSPORT=kafka://broker:9092/topic            │
│           EVENTS_TRANSPORT=nats://nats:4222/aero_events          │
└────────────────────────────────────────────────────────────────┘

┌─ 新增: 事件路由引擎 ───────────────────────────────────────────┐
│ type EventRule struct {                                         │
│     ID           string                                         │
│     Name         string    // "notify-on-upload"                │
│     EventTypes   []string  // ["created", "deleted"]            │
│     BucketFilter string    // "images/*"                        │
│     PrefixFilter string    // "uploads/"                        │
│     TagFilter    map[string]string                              │
│     Target       EventTarget                                     │
│     RetryPolicy  RetryPolicy                                    │
│     DLQ          string    // failed→events route               │
│ }                                                                │
│                                                                  │
│ EventTarget (interface):                                         │
│   WebhookTarget{URL, Secret}                                    │
│   KafkaTarget{Topic}                                            │
│   SESTarget{Channel} // SSE stream consumer group               │
│                                                                  │
│ REST API:                                                        │
│   GET/POST/PUT/DELETE /v1/events/rules                           │
└────────────────────────────────────────────────────────────────┘

┌─ CloudEvents 适配器 ───────────────────────────────────────────┐
│ 输入: repository.Event（内部格式）                                │
│ 输出: CloudEvents 1.0 JSON（标准化格式）                          │
│                                                                  │
│ specversion: 1.0                                                 │
│ source: aero-vault/{tenant}/{bucket}                             │
│ id: {event.ID}                                                   │
│ type: io.aero-vault.object.created                               │
│ datacontenttype: application/json                                │
│ data: { standard event payload }                                 │
│ subject: {key}                                                   │
│ time: {created_at}                                               │
│ tenantid: {tenant} (extension)                                   │
│                                                                  │
│ 集成: Knative Eventing → 连接到 Knative 服务 / 函数              │
│        Azure Event Grid → 连接 Azure Functions / Logic Apps      │
│        Google Eventarc → 连接 Cloud Run / Cloud Functions        │
└────────────────────────────────────────────────────────────────┘

### 为什么现在做

Webhook 和 Postgres LISTEN/NOTIFY 满足 80% 的小规模场景，但企业客户的事件规模（event volume > 10K/s）和集成需求（Kafka 生态）是现有架构无法满足的。**CloudEvents 标准化**是让 AeroVault 事件成为"一等公民"的关键一步——它打通了 Knative / Azure / GCP 的无服务器计算平台。

---

## 4. AI Pipeline Quality & Search Observability Framework

### 为什么需要它

AeroVault 拥有完整的 RAG 流水线（提取→分块→嵌入→检索→排序→生成），但**没有任何机制来系统性地衡量其质量**：

- **无检索质量指标**：没有人知道 BM25 的 MAP（Mean Average Precision）是多少，向量检索的 Recall@K 是多高，混合搜索是否真比纯向量好。
- **无用户反馈回路**：用户搜索后是否满意？点击了第几条结果？Chat 的回答是否有帮助？完全没有收集。
- **无 A/B 测试框架**：修改 chunk 大小、重叠窗口、embedding 模型、reranker——改变后无法做对照实验。
- **无 Embedding 漂移监控**：升级 embedding 模型后，旧 chunk 和新 chunk 的向量不可比较——当前 `search.go` 根据 `EmbedModel` 名过滤旧 chunk，但没有任何告警提示用户"50% 的 chunk 已被排除"。
- **无查询日志分析**：热门查询是什么？搜索失败（零结果）的查询有哪些？用户重复搜相同的查询？无法回答。

这是从"RAG 能用"到"RAG 值得信任"之间的最后一段距离。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/ai/search.go:Query` | 搜索入口，记录 Usage | 无检索质量指标统计 |
| `internal/ai/search.go:rerank` | Reranker 失败静默降级 | 无降级率监控告警 |
| `internal/ai/search.go:drift` | `EmbedModel != queryModel` 过滤 | 无覆盖率指标暴露 |
| `internal/ai/indexer.go:Run` | 事件驱动索引 | 无索引延迟分布 |
| `internal/ai/bm25.go:Search` | BM25 检索 | 无词频/文档频统计 |
| `internal/ai/result_cache.go` | 结果缓存 | 无命中率/驱逐率指标 |
| `internal/ai/cost.go` | 费用跟踪 | 无 cost-per-query 分布 |
| `internal/repository/repository.go:Usage` | 记录搜索/聊天使用 | 无用户反馈字段 |
| `internal/telemetry/metrics.go` | OTel metrics | 无 search-quality 指标 |
| `internal/api/rest/search.go` | REST 搜索端点 | 无 `X-Search-Quality-Score` header |

### Edge Cases 暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **Embedding 漂移无感知** | 升级 text-embedding-3-small → text-embedding-3-large | 新 chunk 用大模型，旧 chunk 被过滤掉，搜索质量下降但无告警 | `indexer_skip_total{reason="model_drift"}` 仪表盘告警 → 触发 reindex |
| **零结果查询** | 用户搜了一个没有匹配词项的词 | 返回空数组 | 触发 spelling correction + 同义词扩展 fallback |
| **Reranker 静默死亡** | Reranker HTTP 端点返回 500 | 降级到原始排序 + warn log | alert on `rerank_failure_total > threshold` |
| **Chunk 污染** | 一个大型 PDF 提取后 chunk 全是乱码 | 这些 chunk 沉默地参与搜索并拉低质量 | 文本质量评分（字符熵、语言检测）→ 低质量 chunk 标记 |
| **搜索延迟爆炸** | BM25 索引在大型租户上重建时 | 搜索阻塞 | 影子索引 + warm standby |
| **低质量回答** | LLM 产生幻觉但引用了真实 chunk | 用户认为可信，实际回答有误 | 回答质量评分（factuality classifier → inline warning） |
| **热门查询热点** | 一个查询被同一用户重复 1000 次/秒 | 正常搜索（不命中缓存则代价极高） | 查询频率感知缓存 + 相似查询去重 |
| **搜索偏见** | 索引中 90% 的文档来自一个来源 | 搜索结果偏向该来源 | 结果多样性评分 + Diversifier reranker |

### 架构蓝图

```
┌─ 新增: Search Quality Metrics ────────────────────────────────┐
│ 指标                                                             │
│ search_quality_recall_at_k{tenant, mode}        // Recall@K     │
│ search_quality_map{tenant, mode}                // Mean Avg Prec │
│ search_quality_ndcg{tenant, mode}               // NDCG@K        │
│ search_result_count{tenant}                     // 结果数分布     │
│ search_zero_result_total{tenant}                // 零结果计数器   │
│ search_latency_p99{tenant, mode}                // 检索延迟 P99   │
│ chunk_coverage_ratio{tenant, reason}            // 可用 chunk %  │
│ rerank_failure_total{tenant, reason}            // 重排失败计数   │
│                                                                  │
│ 收集方式:                                                         │
│   1. 离线评估: A/B 测试框架，预标注的 query<->relevant_doc 对      │
│   2. 在线评估: 隐式反馈（点击 + dwell time）+ 显式反馈（赞/踩）     │
│   3. 无参考评估: 结果熵 + 多样性评分                                │
└────────────────────────────────────────────────────────────────┘

┌─ 新增: A/B 测试框架 ──────────────────────────────────────────┐
│ type ExperimentConfig struct {                                   │
│     ID           string                                          │
│     Name         string    // "chunk-window-600-vs-800"          │
│     TrafficPct   float64   // 0.10 (10% 流量进入实验组)          │
│     Mode         string    // "header" | "tenant"                │
│     Variants     []Variant                                       │
│ }                                                                 │
│                                                                  │
│ type Variant struct {                                             │
│     Name   string     // "control" | "treatment"                │
│     Config map[string]any  // {"chunk_window": 600}             │
│ }                                                                 │
│                                                                  │
│ 实现方式:                                                         │
│   请求随机分配 → header `X-Aero-Experiment: name=variant`        │
│   变体参数覆盖 config（chunk_window / embed_model / reranker）   │
│   指标记录时带上 experiment tag                                   │
│   报告: experiment_results 表 → web UI 仪表盘                     │
└────────────────────────────────────────────────────────────────┘

┌─ 新增: 用户反馈收集 ───────────────────────────────────────────┐
│ Search API 扩展:                                                 │
│   POST /v1/search → 响应中包含 result_id (search session UUID)   │
│                                                                  │
│   POST /v1/search/feedback                                       │
│     { "result_id": "...",                                        │
│       "query": "vector database",                                │
│       "clicked": [2, 5],          // clicked chunk indices       │
│       "dwell_ms": 32000,          // time spent on result        │
│       "rating": 4,                // explicit 1-5 star           │
│       "zero_result": false,       // user felt result was empty  │
│       "notes": "..." }                                           │
│                                                                  │
│ Chat API 扩展:                                                   │
│   POST /v1/chat/feedback                                         │
│     { "chat_id": "...",                                          │
│       "answer_rating": 3,                                        │
│       "citation_helpful": [true, false, true],                   │
│       "comment": "the third citation was misleading" }           │
│                                                                  │
│ 存储: feedback 表（独立的 feedback schema，分表存储）              │
│ 聚合: 每小时 job 计算 quality delta → 写入 SearchQualityTrend     │
└────────────────────────────────────────────────────────────────┘

┌─ 新增: Query Understanding ────────────────────────────────────┐
│ 当前: query 直接传给 embedder / BM25                              │
│ 目标:                                                             │
│   1. Query 预处理层:                                              │
│      - 拼写纠正（symspell / 字典）                                │
│      - 中文分词（jieba / 基于词典的 max-match）                     │
│      - 停用词过滤（多语言）                                        │
│      - 同义词扩展（同义词字典 → 拆成 OR 子查询）                    │
│      - 查询改写（query2doc / HyDE 模式）                           │
│                                                                  │
│   2. 查询建议（search-as-you-type）：                              │
│      - 基于热门的已有查询（query log 最频繁前缀匹配）               │
│      - 基于索引内容（BM25 的 term 字典中的前缀）                    │
│      - API: GET /v1/search/suggest?q=vect → ["vector","vectors"] │
│                                                                  │
│   3. Query 分类:                                                  │
│      - 事实型 ("什么是量子计算") → 需要精确回答                    │
│      - 探索型 ("2024 年科技趋势")  → 需要多样化结果                │
│      - 导航型 ("上传日志文件")    → 需要文件定位而非搜索            │
│      → 不同查询类型选择不同检索策略                                 │
└────────────────────────────────────────────────────────────────┘

### 为什么现在做

`ai.Search` 和 `ai.Chat` 的接口已经稳定，`telemetry` 基础设施已就位。现在是添加质量框架的**最佳时机**：功能定型后加入评估体系，避免"先做功能后补质量"时遇到颠覆性改动。搜索质量是企业客户评估 AI 能力时的首要问题——他们需要从"RAG 存在"到"RAG 有效"的证明。

---

## 5. Multi-Cluster Federation & Global Namespace

### 为什么需要它

当前架构假设**一个 AeroVault 实例管理自己的存储 + 自己的元数据**。`replication.Worker` 实现了跨 backend 的异步副本写入，但存在根本限制：

- **复制是单向的**：A 集群→B 集群，B 不会回复到 A。
- **无全局命名空间**：客户端需要知道对象在哪个集群。没有统一的接入点。
- **无冲突解决**：两个集群同时写入同一 key 时，最后写入者胜出——但时间戳在不同时钟域不可靠。
- **无跨集群搜索**：对象是本地索引的，不能跨集群搜索。
- **无读亲和性路由**：写往 us-east-1，读也在 us-east-1——欧洲用户延迟 200ms。

这与 ROADMAP #3（Horizontal scale-out & HA）和 expansion-v4 #4（Active-Active Multi-Region）**方向不同**：
- ROADMAP #3 聚焦于**数据库 + 作业池的单实例扩展性与高可用**（Postgres / 集群单例 / leases）。
- expansion-v4 #4 聚焦于**双活多区域复制 + 地理分布**（读路由、冲突解决、数据主权）。
- 本方向聚焦于**全局命名空间 + 元数据 + 集群间独立自治**——每个集群自有 metadata DB，通过元数据同步达成全局视图。

换句话说，前两者解决的是"一个逻辑集群如何扩展和容灾"，本方向解决的是**"多个独立集群如何组成一个逻辑整体"**。

### 当前代码锚点

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/cluster/singleton.go` | 单例协调（Postgres lease） | 无集群间通信 |
| `internal/replication/replication.go` | 单向异步复制 | 无双向、无冲突解决 |
| `internal/repository/repository.go` | Repository 接口 | 无全局路由视图 |
| `internal/auth/auth.go` | 本地 auth | 无跨集群信任联合 |
| `internal/ai/search.go` | 本地搜索 | 无跨集群搜索 |
| `internal/storage/storage.go` | 后端抽象 | 无地理位置标签 |
| `internal/api/rest/router.go` | 本地路由 | 无读路由 / 全局接入 |

### Edge Cases 暴露

| Edge Case | 场景 | 当前行为 | 应该行为 |
|-----------|------|---------|---------|
| **跨集群时钟偏差** | 集群 A 时钟快 5s，集群 B 时钟慢 3s | `updated_at` 对比不可靠 | 使用混合逻辑时钟（Hybrid Logical Clock, HLC） |
| **网络分区** | 集群 A 和 B 失去连接 30 分钟 | 各自独立写入，恢复后冲突 | CRDT 增量状态合并或基于版本向量的冲突检测 |
| **同 key 并行写入** | 两个集群同时 PUT `docs/report.pdf` | 最后写入但失去数据 | 向量时钟 → 标记冲突 → 人工/自动合并 |
| **跨集群搜索延迟** | 查询发到集群 A，但相关文档在集群 B 索引 | 无结果 | 扇出查询（fan-out）到所有集群 + 全局 RRF 融合 |
| **客户端网关故障** | 全局入口点（GSLB）探测集群 A 不健康 | 客户端收到 502 | 自动将流量路由到集群 B（健康集群） |
| **集群间证书轮换** | 集群间 mTLS 证书过期 | 复制全部失败 | 证书到期前自动轮换 + 双证书信任期重叠 |
| **对象搬迁** | 将数据从 us-east-1 搬到 eu-west-1 | `cp` 后手动更新 metadata | 异步搬迁 job（rebalance）+ 原集群保留重定向标记 |
| **一致性级别选择** | 读取刚写入的数据（需要读 own writes） | 可能路由到旧集群 | `X-Aero-Consistency: strong` → 强制读主集群 |

### 架构蓝图

```
┌─ Global Namespace Layer ───────────────────────────────────────┐
│ 新增 internal/federation/ 包                                     │
│                                                                  │
│ type ClusterInfo struct {                                        │
│     ID          string    // "us-east-1-a"                      │
│     Region      string    // "us-east-1"                        │
│     BaseURL     string    // "https://aero-us-east.example.com"  │
│     Public      bool      // 是否对外提供服务                     │
│     Status      string    // "active" | "draining" | "failed"   │
│     Storage     []string  // 存储后端列表                         │
│     Latency     int       // 到其他集群的 ping ms                │
│ }                                                                 │
│                                                                  │
│ type GlobalRegistry struct {                                     │
│     clusters  map[string]*ClusterInfo                            │
│     resolver  RouteResolver // 读路由策略                         │
│ }                                                                 │
│                                                                  │
│ 路由策略:                                                         │
│   - LatencyOptimized：路由到 ping 最低的集群                      │
│   - LocalityPreferred：按客户端 IP 的地理位置路由                  │
│   - LeaderCohort：强一致性读必须走主集群                          │
│   - Random：负载均衡                                              │
│                                                                  │
│ 集群发现:                                                         │
│   静态：配置文件列举集群                                           │
│   动态：基于 etcd / Consul / DNS SRV 记录                         │
└────────────────────────────────────────────────────────────────┘

┌─ 跨集群元数据同步 ─────────────────────────────────────────────┐
│ 每个集群维护自己的 metadata DB（独立 SQLite / Postgres）          │
│                                                                  │
│ 元数据复制方式:                                                   │
│   1. 事件驱动（推荐）：本地创建/更新/删除 → 发布到全局事件流       │
│      → 其他集群消费事件 → 更新本地缓存视图                         │
│   2. 周期性同步（fallback）：定期拉取对等集群的 `updated_at > last` │
│                                                                  │
│ 同步内容:                                                         │
│   - Object 元数据（key, size, etag, version_id, storage_class）   │
│   - Tags                                                         │
│   - ACL                                                          │
│   - 不同步: storage_key（物理存储路径是集群私有的）                 │
│                                                                  │
│ 全局视图存储:                                                      │
│   federation_objects 表:                                          │
│     global_key, tenant, bucket, cluster_id,                      │
│     local_key, version_id, size, etag, storage_class,            │
│     created_at, updated_at, hlc_timestamp                        │
│                                                                  │
│ 查询路由:                                                          │
│   对全局 key 的 GET → GlobalRegistry 路由到最近可用集群            │
│   对全局 key 的 PUT → 路由到主集群（由一致性哈希决定）              │
└────────────────────────────────────────────────────────────────┘

┌─ 冲突解决 ──────────────────────────────────────────────────────┐
│ 场景: 网络分区后两个集群独立修改同一 key                           │
│                                                                  │
│ 策略层级（可配置 per-bucket）：                                    │
│   1. LWW（Last Writer Wins）— 最简单，使用 HLC 时间戳             │
│   2. Version Vector — 每个集群维护版本向量                        │
│      检测冲突 → 标记 `CONFLICT` → 暴露两个版本                    │
│      → 用户调用 `ResolveConflict(key, resolution_strategy)`      │
│   3. CRDT — 使用 Conflict-Free Replicated Data Types             │
│      适用于 tag 合并（map merge）、ACL 合并                        │
│   4. Declarative — 用户定义冲突合并函数（自定义脚本）               │
│                                                                  │
│ API:                                                              │
│   GET /v1/federation/conflicts → [{key, cluster_a_version, ...}]  │
│   POST /v1/federation/conflicts/{key}/resolve                    │
│     { "strategy": "use_cluster", "cluster_id": "us-east-1" }     │
│   POST /v1/federation/conflicts/{key}/resolve                    │
│     { "strategy": "merge_tags", "merge_from": "us-west-2" }      │
└────────────────────────────────────────────────────────────────┘

┌─ 跨集群搜索 ───────────────────────────────────────────────────┐
│ 当前: 每个集群独立索引 chunk                                       │
│ 目标: 跨集群统一搜索                                              │
│                                                                  │
│ 方案 A: 中心化索引（仅元数据同步）                                 │
│   - 选择一个集群作为"搜索聚合节点"                                │
│   - 所有集群将 chunk 元数据复制到聚合节点                          │
│   - 搜索请求统一发到聚合节点                                      │
│   - 局限性：单点、延迟                                            │
│                                                                  │
│ 方案 B: 分布式搜索扇出                                           │
│   1. 客户端将搜索请求发到就近集群                                  │
│   2. 该集群作为 "coordinator"                                     │
│   3. 将查询扇出到所有集群的搜索服务                                │
│   4. 每个集群返回各自的 top-k 结果                                 │
│   5. coordinator 执行全局 RRF 融合                                │
│   6. 返回最终结果                                                  │
│   - 优点：无单点，每个集群自治                                    │
│   - 代价：搜索延迟 = max(集群延迟) + 扇出 RTT                      │
│                                                                  │
│ 方案 C: 全局向量索引                                             │
│   - 使用 Qdrant 的多集群支持（分布式 collection 跨节点）           │
│   - 所有集群向同一 Qdrant 集群写入 chunk                          │
│   - 搜索直接在 Qdrant 上进行                                       │
│   - 前提：Qdrant 部署必须全局可达                                  │
└────────────────────────────────────────────────────────────────┘

### 为什么现在做

当前全球分布式部署需求正在从"少数大公司"变成"中等规模团队"的标配。AeroVault 已经有 `replication.Worker` 和 `cluster.singleton` 的基础，集群间的通信和信任可以将这些基础设施扩展为多集群联邦。这是一个**架构层投资**，不需要立即完成，但需要设计对的方向以避免未来大量的 break change。

---

## 总结：实施优先级建议

| 方向 | 前期准备 | 影响面 | 建议时机 |
|------|---------|--------|---------|
| **1. Federated Identity** | SCIM 表结构 + OIDC 库 | 认证流程 | 下一轮（立即） |
| **2. Cold Storage & Async Restore** | migration 加 `restore_requests` 表 + Storage 接口扩展 | 存储/成本 | 2-3 个月内 |
| **3. Enterprise Event Streaming** | Transport 接口抽象 + Kafka 客户端 | 事件架构 | 3-6 个月 |
| **4. AI Search Quality** | 质量指标定义 + feedback 表 | AI/UX | 迭代式（可立即开始 metrics） |
| **5. Multi-Cluster Federation** | 架构设计文档 + 全局路由代理 | 基础设施 | 长远规划（6-12 个月） |

**核心建议：** 从方向 1（SSO）和方向 4（搜索质量）开始——方向 1 是企业采购的"门票"，方向 4 是让已有 AI 能力变得可信的"放大器"。方向 2 可以在 2 个月内完成核心（async restore state machine），方向 3 和方向 5 需要更长的设计和实现周期。
