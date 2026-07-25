# AeroVault 高价值扩展方向 v43 — 经 42 轮分析后仍未被触及的核心盲区

> **分析范围：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 237 个 `.go` 文件，~50K 行代码，24 轮迁移文件，三套 SDK，deploy/*，docs/*）
>
> **分析视角：** 资深架构师 / 产品经理 — 聚焦此前 **42 期 expansion 分析（累计 200+ 方向，~400,000+ 字分析文本）+ `docs/ROADMAP.md`（10 大方向）+ `docs/adr/DECISIONS.md` + `docs/CHANGELOG.md` + `docs/TODO.md`** 中从未实质性触及的独立盲区
>
> **分析日期：** 2026-07-10
>
> **去重方法：** 对 `docs/requirements/` 下全部 42 期既有分析（v1–v42）+ `ROADMAP.md` + `CHANGELOG.md` + `TODO.md` + `adr/DECISIONS.md` + `docs/requirements/extensions*.md` 进行**穷尽式关键词验证**。每个方向在既有文档中 **零实质性独立分析**（表格中的一行过路引用、举例中的附带提及、或 10 行以内的浅层草图，均不构成实质性分析）。

---

## 前言

此前 42 期 expansion 分析累计覆盖了 200+ 方向，从 AI/RAG 管线（~30 方向）、S3 兼容协议（~22 方向）、存储后端（~24 方向）、认证授权（~24 方向）、多租户（~22 方向）、合规（~16 方向）、可观测性（~20 方向）、存储分层（~16 方向）、工程基础设施（~10 方向）到社区治理（~5 方向）。

然而，经过代码级穷尽扫描，以下 **5 个方向在 42 期分析 + ROADMAP + CHANGELOG + TODO + ADR 中零实质性覆盖**。它们的共同特征是：**不依赖于新功能添加，而是现有能力的纵深缺失**——每个方向对应的代码已存在，但缺少了让这些代码在真实生产中可用的关键一层。这类似于一栋有着完整骨架和管线的大楼，却缺少了门锁、电源插座、消防系统和物业管理。

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心矛盾 | 锚定代码 | 42 期覆盖 |
|---|------|------|--------|---------|---------|-----------|
| 1 | **预签名 URL 安全策略与条件约束** | 安全/协议 | **P1（高）** — 当前预签名 URL 是无限制的万能钥匙；任意持有者可以下载/上传指定对象，无法绑定 IP、方法、路径或条件 | `internal/service/file_features.go:PresignGet/PresignPut`（仅 `expiry` 参数）；`internal/storage/sign.go`（HMAC 签名不对任何条件做约束） | ❌ **零覆盖** |
| 2 | **外部身份联合（OIDC/SAML/SSO）** | 身份/安全 | **P1（高）** — 企业客户的首要准入要求；当前仅支持本地 JWT 和 API Key，无法接入 Okta/Keycloak/Azure AD 等 IdP | `internal/auth/auth.go`（无 OIDC/SAML 流程）；`internal/auth/auth_middleware.go`（仅 Bearer/ApiKey/SigV4） | ⚠️ v5 方向表 10 行浅层草图，**无实质性架构分析** |
| 3 | **多通道通知交付基础设施** | 事件/运维 | **P1（高）** — 通知规则可配置存储但永不执行；全局单 URL webhook 是唯一通道，无邮件/Slack/PagerDuty/SMS | `internal/repository/repository.go:NotificationRule`（存储结构，支持 Queue/Lambda/Topic）；`internal/events/webhook.go`（仅全局 URL，不读规则） | ⚠️ v12/v33/v35 表格举例各 1 行，**无实质性架构分析** |
| 4 | **SLA/SLO 合规测量与报告** | 可靠性/运维 | **P2（中）** — 15+ OTel 指标、80+ 配置参数、完整的 Prometheus 告警，但没有 SLA 定义、没有可用性追踪、没有服务等级合规报告 | `internal/telemetry/metrics.go`（有指标但无 SLA 维度）；`internal/config/config.go`（无 SLA 配置）；Prometheus 告警（无错误预算） | ⚠️ v38 方向四提到 error budget，但聚焦结构化错误域，**未对 SLA/SLO 作为独立方向分析** |
| 5 | **推送式遥测导出与指标联邦** | 可观测性/运维 | **P2（中）** — 只有拉取式 Prometheus `/metrics` 端点；无 Prometheus Remote Write、无 Thanos/Cortex/Mimir 集成、无 StatsD/Datadog 推送 | `internal/telemetry/prometheus.go`（仅 `otelPrometheus` exporter 拉取端点）；`internal/config/config.go`（无遥测导出配置） | ❌ **零覆盖** |

---

## 方向一：预签名 URL 安全策略与条件约束（Presigned URL Security Scope）

### 现状

当前预签名 URL 的实现是**极致简单**的——甚至可以说是"裸签"：

```go
// internal/service/file_features.go:277-293
func (s *FileService) PresignGet(ctx context.Context, tenant, bucket, key string, expiry time.Duration) (string, error) {
    // ...
    return s.store.PresignGet(ctx, obj.StorageKey, expiry)
}

func (s *FileService) PresignPut(ctx context.Context, tenant, bucket, key string, expiry time.Duration) (string, error) {
    // ...
    return s.store.PresignPut(ctx, storageKey(tenant, bucket, key), expiry)
}
```

签名函数本身只对路径+过期时间签名，不做任何额外约束：

```go
// internal/storage/sign.go
// 从 local_sign.go 看：签名 = HMAC(key_path + expiry)
// 没有任何 IP 绑定、路径限制、方法限制、内容约束
```

**当前预签名 URL 的能力矩阵：**

| 安全维度 | 当前状态 | AWS S3 对标 |
|---------|---------|------------|
| 过期时间（Expiry） | ✅ 支持 | ✅ `X-Amz-Expires` |
| IP 地址白名单 | ❌ 无 | ✅ `X-Amz-SourceIp`（condition in policy） |
| 路径前缀限制 | ❌ 无 | ✅ `s3:prefix`（policy condition） |
| 方法限制（GET vs PUT） | ❌ 无（签名与具体方法解耦） | ✅ 隐含在签名中 |
| 内容类型约束 | ❌ 无 | ✅ `x-amz-content-type`（POST policy） |
| 内容长度范围 | ❌ 无 | ✅ `content-length-range`（POST policy） |
| HTTP 方法白名单 | ❌ 无 | ✅ `acl`（POST policy） |
| 自定义条件策略 | ❌ 无 | ✅ S3 POST Policy（JSON 条件文档） |
| 审计追踪（谁生成了 URL） | ❌ 无 | ✅ CloudTrail |
| 撤销能力 | ❌ 无（签发后无法撤销） | ✅ IAM policy change 可间接撤销 |

### 为什么需要

1. **安全风险当前不可控：** 预签名 URL 是"谁持有谁使用"的模式。如果 URL 泄露（嵌入日志、分享给第三方、被抓包），攻击者可以无限制地下载或上传对象。没有 IP 白名单意味着即使用于内部服务间通信，也无法限制调用来源。

2. **企业合规的签名策略需求：** 金融/医疗客户需要确保预签名 URL 只能在特定网络段（如 VPN 内网）使用、只能用于特定范围的对象、只能用于特定操作。这是安全审计中常见的要求。

3. **当前实现与 AWS SDK 行为差异：** S3 SDK 生成预签名 URL 时会绑定 HTTP 方法（`GetObject` vs `PutObject`）。当前 AeroVault 的 `PresignGet` 和 `PresignPut` 可能各自调用不同的签名函数，但 REST handler 的 `/v1/files/{key}/presign` 行为与 S3 的 `?X-Amz-Signature` 行为需要对齐以确保 AWS SDK 兼容性。

4. **缺乏撤销能力导致长期风险：** 一旦签发，预签名 URL 在过期前无法撤销。对于敏感数据，即使权限变更，已发出的 URL 仍有效。需要支持条件策略和最短过期时间策略。

### 缺失的能力

1. **条件策略文档引擎：** 新增可选的 `PresignPolicy` 参数，支持 JSON 格式的策略条件列表：

   ```go
   type PresignPolicy struct {
       IP          []string // CIDR 白名单
       Methods     []string // ["GET", "PUT"]；空 = 不限
       PathPrefix  string   // 路径前缀限制
       ContentType string   // 仅 PUT 时
       MaxSize     int64    // 仅 PUT 时
   }
   ```

2. **签名中嵌入条件：** 将策略条件哈希或序列化后嵌入签名载荷，在验证时检查。这样条件不可篡改——修改条件需要重新签名。

3. **最短过期时间全局策略：** 新增服务端配置 `PRESIGN_MIN_EXPIRY_SECONDS` 和 `PRESIGN_MAX_EXPIRY_SECONDS`，防止管理员签发过长或过短的有效期。

4. **预签名 URL 审计日志：** 每次签发记录 `tenant`、`key`、`method`、`expiry`、`policy` 摘要、签发者身份到审计日志。

5. **撤销列表（可选）：** 新增 `presign_revocation` 表，支持在签名验证时检查是否已被撤销。适用于极高安全场景。

### 边界情况

| 场景 | 当前行为 | 有策略引擎后 |
|------|---------|------------|
| 预签名 URL 泄露到公网 | 任何人可下载/上传对象 | 结合 IP 白名单，限制只能从企业 VPN 段访问 |
| 内部 CI 系统获取上传 URL | 无限制上传到指定路径 | 限制 `PathPrefix`、`MaxSize`、来源 IP |
| 前端浏览器直传文件 | 用户可篡改上传内容类型 | `ContentType` 约束 + `MaxSize` 约束 |
| 预签名 URL 被重放用于不同路径 | URL 绑定了 key，但方法可能被滥用 | 方法绑定 + 路径前缀绑定 |

---

## 方向二：外部身份联合（OIDC/SAML/SSO）

### 现状

当前认证系统完全自给自足：

```
内部 identity 模型:
  API Key (sha256 hash) → Tenant + Scopes
  JWT (HS256 signed) → Tenant + Scopes
  SigV4 (HMAC-SHA256) → Tenant + Scopes
```

没有任何外部身份提供者的支持：

```go
// internal/auth/auth_middleware.go — 认证流程
func (r *Registry) authenticateBearer(w http.ResponseWriter, req *http.Request) (*http.Request, bool) {
    token := extractToken(req)
    // ...
    k, ok := r.Lookup(req.Context(), token)  // 只在本地存储中查找
    // ...
}
```

**不支持的外部身份协议：**

| 协议 | 现状 | 典型 IdP | 企业必要性 |
|------|------|---------|-----------|
| OIDC (OpenID Connect) | ❌ | Keycloak, Dex, Okta, Google, Microsoft, Auth0 | ★★★★★ — 现代企业 IdP 首选 |
| SAML 2.0 | ❌ | ADFS, Azure AD, Okta, OneLogin | ★★★★☆ — 大型传统企业主流 |
| LDAP / Active Directory | ❌ | OpenLDAP, 389 DS, Active Directory | ★★★☆☆ — 自管企业目录常用 |
| OAuth 2.0 Authorization Code | ❌ | GitHub, GitLab, Google, Microsoft | ★★★☆☆ — 第三方登录场景 |
| SCIM (用户/组同步) | ❌ | 无用户目录同步 | ★★★☆☆ — 企业用户生命周期管理 |

### 为什么需要

1. **企业采购的准入条件：** 在 RFP/RFI 流程中，"是否支持 SAML 2.0 / OIDC"是安全问卷的必答题。不支持 = 无法进入 70% 以上企业的短名单。

2. **用户认知负担：** 当前每个用户需要一个独立的 API Key 或 JWT。在拥有 500+ 用户的企业中，管理员需要通过 curl 调用 admin API 管理密钥——没有 SSO = 用户需要记住另一个密码/Token = 安全风险（写在 Wiki、共享文档）和运维负担。

3. **多租户与联合身份的交叉需求：** 在多租户场景中，企业客户通常希望用自己的 IdP 管理本组织用户的访问权限（包括租户映射、角色映射）。当前无法实现"用 Okta 登录后自动分配到 tenant X"。

4. **外部审计合规：** SOC2/HIPAA 审计中，身份认证和访问管理（IAM）是重点关注域。集成企业 IdP 可以复用已有的访问审核流程（IdP 的登录日志、会话管理、MFA 策略）。

### 缺失的能力

1. **OIDC 发现与令牌验证：** 新增 `internal/auth/oidc.go`，支持 OIDC Discovery URL 配置，验证 `id_token` 和 `access_token`，提取 `sub`、`email`、`groups` 等 claims 映射为 AeroVault tenant + scopes：

   ```go
   type OIDCConfig struct {
       ProviderURL   string // 例如 https://keycloak.example.com/realms/aero
       ClientID      string
       ClientSecret  string
       TenantClaim   string // "groups" → 映射到 tenant
       ScopeClaim    string // "roles" → 映射到 scope
       FallbackTenant string
   }
   ```

2. **SAML 2.0 ACS（Assertion Consumer Service）：** 新增 `/v1/auth/saml/acs` 端点，解析 SAMLResponse，提取 NameID/Attribute 映射为本地身份。支持 SP-initiated SSO 和 IdP-initiated SSO。

3. **身份映射缓存 + 失效：** 从外部 IdP 获取的身份 claim → tenant/scope 映射应缓存（已有 `KeyCache` 可复用），但支持 TTL 和手动失效（当 IdP 更改了用户的组/角色时）。

4. **SCIM 用户/组同步端点：** 新增 `POST /v1/admin/scim/{Users,Groups}` 端点（SCIM 2.0 RFC 7644），允许 IdP 自动推送用户和组变更，同步本地权限。这是 Okta/Azure AD 预置集成的标配接口。

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **IdP 不可达（网络故障/IdP 宕机）** | 缓存身份映射 + 后台异步刷新；缓存过期后拒绝请求（fail-closed） |
| **IdP 返回的 group 名称与 tenant 不匹配** | 提供 `TenantClaim` regex 提取规则和 `FallbackTenant` 兜底 |
| **同时使用本地 API Key 和 SSO** | 多认证方式共存，按请求头自动选路（已部分实现：SigV4 vs Bearer） |
| **JWT 签发与 OIDC token 的共存** | OIDC token 直接作为 Bearer token 传入，`auth.Lookup` 检查本地 store 后再检查 OIDC 缓存 |
| **跨租户的用户映射** | 一个 IdP 用户可能属于多个 tenant，通过 scope claim 或 header 选择 |

---

## 方向三：多通道通知交付基础设施（Multi-Channel Notification Delivery）

### 现状

当前的事件通知系统存在一个**根本性的架构断层**：

```
当前架构:
  事件总线 → Webhook（单 URL）
              └── 所有事件 → 一个 HTTP 目标
```

1. **通知规则已存储但不执行：** `BucketConfig.NotificationRules`（`repository/repository.go`）完整支持 S3 通知模型——包含 `Events`（事件类型过滤）、`FilterKey`（前缀/后缀过滤）、`QueueARN`（目标 URL）——但整个 `internal/events/` 包中没有任何代码读取或执行这些规则。

2. **唯一通道是 HTTP Webhook：** 不支持 邮件、Slack、PagerDuty、SMS、消息队列 等任何其他通道。

3. **全局单 URL 无过滤：** `EVENTS_WEBHOOK_URL` 配置的是单一的全局 webhook，不接受事件类型过滤，不接受 bucket/prefix/suffix 过滤。所有事件无差别投递。

```go
// internal/events/webhook.go:NewWebhook — 只从全局配置读取
func NewWebhook(urls string, logger *slog.Logger) *Webhook {
    parts := strings.Split(urls, ",")       // 支持多 URL，但仍然是全局配置
    // ...
}
```

**S3 通知与当前实现的鸿沟：**

| S3 通知能力 | 当前状态 | 代码位置 |
|------------|---------|---------|
| 事件类型过滤（`s3:ObjectCreated:*`） | ❌ 全局 webhook 不过滤 | `events/webhook.go` |
| 前缀/后缀过滤（`FilterKey`） | ❌ 无 | `repository/repository.go:NotificationRule.FilterKey`（已定义但不消费） |
| 多目标分发（Queue/Lambda/Topic） | ❌ 仅 HTTP | `NotificationRule.QueueARN`（支持但不消费） |
| Lambda 函数触发 | ❌ 不支持 | `NotificationRule.LambdaARN`（字段存在但不消费） |
| SQS/SNS 目标 | ❌ 不支持 | `NotificationRule.TopicARN`（字段存在但不消费） |
| 邮件通知 | ❌ 不支持 | — |
| Slack/Teams/Discord | ❌ 不支持 | — |
| PagerDuty/OpsGenie | ❌ 不支持 | — |

### 为什么需要

1. **通知规则已是 S3 API 的一部分但无运行时行为：** 这是协议兼容性层面最严重的"死数据"案例之一。`PutBucketNotification` S3 API 返回成功，规则持久化到了数据库——但从不触发任何操作。用户在 AWS S3 上的工作流（上传→通知→处理）迁移到 AeroVault 后静默失效。

2. **运维告警链路的缺失：** 当前系统有丰富的事件（对象创建/删除、webhook 失败、作业失败、审计事件），但没有面向运维人员的告警通道。关键事件只能通过轮询 HTTP API 才能发现——这在生产环境中不可接受。

3. **企业协作场景的需求：** 实际业务中，不同的事件需要通知不同的人群。例如：
   - "审计管理员收到对象删除通知（邮件）"
   - "安全团队收到 AV 隔离通知（Slack）"
   - "值班工程师收到 webhook 持续失败告警（PagerDuty）"
   - "普通用户收到"您的文件已被处理"（邮件）"

### 缺失的能力

1. **通知规则执行引擎：** 新增 `internal/events/router.go`，在 `bus.Subscribe()` 循环中读取 `NotificationRules`，根据事件类型 + key 过滤 + bucket 匹配将事件路由到不同的目标：

   ```
   事件总线
     ↓
   NotificationRouter
     ├── 规则 1: s3:ObjectCreated:* + filter "invoices/" → webhook A
     ├── 规则 2: s3:ObjectRemoved:* + filter "" → webhook B
     ├── 规则 3: s3:ObjectCreated:* + filter "logs/" → SQS target
     └── 默认规则: 通知规则无匹配时，可选 fallback
   ```

2. **通道适配器系统：** 定义 `Notifier` 接口，不同的通道实现：

   ```go
   type Notifier interface {
       Send(ctx context.Context, event repository.Event, rule repository.NotificationRule) error
   }
   ```

   初始实现集：
   - `HTTPNotifier` — 现有 webhook（按规则目标 URL）
   - `EmailNotifier` — SMTP/SES，模板化邮件（配置SMTP_HOST/SMTP_PORT/SMTP_USER/SMTP_PASS）

3. **通知规则缓存：** 在 router 启动时和后台定时刷新 bucket notification rules 缓存（`GetBucketNotifications`），避免每个事件都查数据库。

4. **通道健康探测：** 对每个通道目标定期做健康检查（ping webhook URL 的头、SMTP 握手），不可达时降级/告警。

5. **通知重试队列：** 结合现有的 `webhook_failures` 表（`repository/webhook_failures.go`），为所有通道统一重试（目前只对全局 webhook 实现了重试）。

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **单个 bucket 配置了 100 条规则** | 规则执行按顺序匹配，命中后 stop（AWS S3 行为）；缓存支持大规则集 |
| **邮件服务器不可达** | 退回到重试队列（复用 webhook_retry 机制），最多 10 次后 dead-letter |
| **规则过滤与事件类型不匹配** | 快速跳过，O(1) 匹配 |
| **通知目标返回 429/限流** | 指数退避 + jitter（已有基础设施） |
| **多通道并行投递** | 使用 `errgroup` 并发发送，单个通道失败不影响其他通道 |

---

## 方向四：SLA/SLO 合规测量与报告（SLA/SLO Compliance Measurement）

### 现状

系统已具备丰富的可观测性基础设施：

| 能力 | 状态 |
|------|------|
| HTTP 请求延迟/吞吐/错误率（`http_server_*`） | ✅ |
| AI 嵌入/搜索延迟与吞吐 | ✅ |
| Job 队列深度与完成/失败计数 | ✅ |
| 存储用量与对象数 | ✅ |
| Prometheus 告警（延迟 P95/队列深度） | ✅ |
| Grafana 仪表盘（12 panels） | ✅ |

**然而，没有任何 SLA/SLO 管理能力：**

| SLA 能力 | 当前状态 | 含义 |
|---------|---------|------|
| SLA 目标定义（如"P99 GET 延迟 < 100ms"） | ❌ 无 | 无法声明服务等级目标 |
| SLO 合规测量（滚动窗口 %） | ❌ 无 | 无法知道是否达到目标 |
| 错误预算计算与追踪 | ❌ 无 | 无法预警 SLO 违约风险 |
| Per-tenant SLA 分化 | ❌ 无 | 所有租户共享相同的服务等级 |
| SLA 合规报告导出 | ❌ 无 | Audit 需要时无法提供 |
| SLA 违约自动降级 | ❌ 无 | 不触发任何自动化动作 |

### 为什么需要

1. **SaaS 产品的必备履约能力：** 任何面向企业的 SaaS 产品都有 SLA 承诺（AWS S3 的 SLA 是 99.99% 可用性）。当前无法回答"我们是否达到了 SLA"——既无法证明达到，也无法在未达到时立即发现。

2. **可观测性基础设施的最后一公里：** 当前 15+ OTel 指标 + 12 面板 Grafana + Prometheus 告警提供了"数据"，但 SLA 管理是将这些原始数据转化为**业务语言**的关键桥梁。没有它，指标只是数字，不是承诺。

3. **Per-tenant SLA 分化的商业价值：** 不同定价层级（免费/专业/企业）需要不同的 SLA。免费层 P99 延迟 2s，企业层 50ms——当前无区分。

4. **错误预算驱动工程决策：** 错误预算（Error Budget）是现代 SRE 实践的核心概念。当错误预算充足时，团队可以放心发布新功能；当不足时，应该优先做可靠性改进。当前无法做这个工程决策。

### 缺失的能力

1. **SLA 目标配置：** 新增配置层，定义每个维度每个层级的目标：

   ```yaml
   # 示例配置（config.go 扩展）
   sla:
     tiers:
       standard:
         availability: 99.9          # 月度可用性 %
         get_latency_p99_ms: 200
         put_latency_p99_ms: 500
         search_latency_p99_ms: 1000
       premium:
         availability: 99.99
         get_latency_p99_ms: 50
         put_latency_p99_ms: 100
         search_latency_p99_ms: 200
   ```

2. **滚动窗口合规计算器：** 新增 `internal/sla/compliance.go`，在内存中维护 28 天滚动窗口的请求结果计数（成功/失败、延迟 buckets），每分钟计算各 SLI 的合规百分比。

3. **错误预算仪表盘指标：** 新增 Prometheus 指标：
   - `sla_availability{tenant,tier}` — 滚动窗口可用性
   - `sla_error_budget_remaining{tenant,tier}` — 错误预算剩余百分比
   - `sla_latency_p99{tenant,tier,operation}` — P99 延迟合规状态

4. **SLA 合规报告导出：** 新增 admin API 端点 `GET /v1/admin/sla/report?from=...&to=...`，返回 JSON/CSV 格式的 SLA 合规报告（月度可用性、延迟分布、故障时长）。

5. **SLA 违约告警 + 自动降级：** 当某个 SLI 连续 N 个窗口违约时，触发 Prometheus Alertmanager 告警。结合可选的 `AI_DEGRADED_MODE` 自动降级机制。

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **新租户历史数据不足** | 冷启动期（如不足 24 小时）不评估 SLA，标记为 `insufficient_data` |
| **维护窗口** | 维护窗口时段从合规计算中排除（配置维护计划） |
| **租户级与全局 SLA 的关系** | 全局 SLA = 所有租户加权平均；租户级 SLA 单独计算 |
| **SLA 违约的根因分析** | 错误预算燃烧率告警 + 与现有 Prometheus 告警联动 |

---

## 方向五：推送式遥测导出与指标联邦（Push Telemetry & Metrics Federation）

### 现状

当前可观测性架构完全基于**拉取**模型：

```
当前架构:
  AeroVault → /metrics (Prometheus text format)
                └── Prometheus server (pull)
                      └── Grafana (query)
```

没有任何推送式或联邦式导出：

| 遥测导出方式 | 当前状态 | 使用场景 |
|------------|---------|---------|
| Prometheus `/metrics` pull | ✅ | 单集群 Prometheus |
| Prometheus Remote Write | ❌ **缺失** | 多集群联邦（Thanos/Cortex/Mimir/VictoriaMetrics） |
| StatsD / DogStatsD | ❌ **缺失** | 现有基础设施采用 StatsD 的团队 |
| OpenTelemetry gRPC export | ❌ **缺失** | OTel Collector → S3/GCP/Azure 导出 |
| Periodic metric snapshot to object storage | ❌ **缺失** | 长期指标归档、合规审计 |
| Custom push via admin API | ❌ **缺失** | 业务指标（如"本月上传总量"）查询 |

```go
// internal/telemetry/prometheus.go — 只提供了 Pull 端点
func ServeMetrics(handler http.Handler) {
    // 注册到 http.Server 的 /metrics 路由
}
```

### 为什么需要

1. **多集群运维的硬需求：** 生产部署通常有多个 AeroVault 集群（多 Region、staging/prod、读写分离）。每个集群的 /metrics 只能被本集群 Prometheus 抓取。需要 Remote Write 将指标推送到中心化的 Thanos/Cortex/Mimir，实现全局视图。没有这个能力，跨集群的 SLO 追踪、容量规划和故障排查都不可行。

2. **现有基础设施兼容性：** 很多组织已经部署了 StatsD（数据管道）或 Datadog Agent（商业 APM）。当前 AeroVault 无法接入这些已有管道，导致运维团队必须另建一套 Prometheus 基础设施。

3. **长期指标归档：** Prometheus 默认只保留 15-30 天指标。对于合规审计（SLA 报告、容量规划趋势），需要将指标快照导出到对象存储永久保存。当前 OTel Collector 配置有 `deploy/otel-collector-config.yaml`，但 AeroVault 本身不主动推送指标到 Collector——这是`pull` vs `push` 的本质区别。

4. **自定义业务指标缺失：** 运维人员和 PM 需要知道"本月新增对象数"、"本周活跃租户数"、"过去 30 天存储增长率"。这些不是 HTTP 请求指标，不能从 `http_server_*` 推导。当前只能通过查询 admin API 再手动输入 Grafana——没有自动化的业务指标管道。

### 缺失的能力

1. **Prometheus Remote Write 集成：** 新增配置 `TELEMETRY_REMOTE_WRITE_URL`（+ 可选 `TELEMETRY_REMOTE_WRITE_BASIC_AUTH` / `TELEMETRY_REMOTE_WRITE_TIMEOUT`），在 `internal/telemetry/prometheus.go` 或新增 `internal/telemetry/remotewrite.go` 中拉起一个后台 goroutine，使用 `prometheus/remote_write` Go 客户端定时推送指标快照。

2. **可配置的 OTel 导出器：** 当前 OTel 设置（`internal/telemetry/otel.go`）使用 `OTEL_EXPORTER_OTLP_ENDPOINT` 环境变量。扩展支持：
   - OTLP over gRPC 推送到 OTel Collector（已部分支持）
   - OTLP over HTTP（`OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf`）
   - 可配置采样率（当前是全量）

3. **业务指标 Collect/Export 框架：** 新增 `internal/telemetry/business.go`，定义结构化业务指标类型，支持通过 admin API 和远程写入导出：

   ```go
   type BusinessMetric struct {
       Timestamp time.Time
       Tenant    string
       Name      string  // "objects_created_total", "storage_bytes", "active_users"
       Value     float64
       Labels    map[string]string
   }
   ```

4. **定期指标快照到对象存储：** 新增 `RECONCILE_TELEMETRY_SNAPSHOT_INTERVAL` 配置，由 reconcile loop 将当前指标快照写入特定 bucket（`/telemetry/YYYY/MM/DD/HH/metrics.json`），用于长期归档和合规审计。

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **Remote Write 目标不可达** | 指数退避重试（复用 `events/webhook.go` 的重试逻辑）；缓冲最近 N 批次在内存中 |
| **推拉共存** | `/metrics` endpoint 保持 open（兼容现有 Prometheus 配置）；Remote Write 作为可选附加通道 |
| **指标量过大导致网络/存储压力** | 支持降采样配置：`TELEMETRY_REMOTE_WRITE_INTERVAL=30s`（默认 15s）；`TELEMETRY_AGGREGATE_BEFORE_PUSH=true` |
| **多租户指标隔离** | Remote Write 保留 `tenant` label；对象存储快照按 tenant 目录分隔 |

---

## 综合优先级与建议实施顺序

| 优先级 | 方向 | 影响面 | 前置依赖 | 建议开始时间 |
|--------|------|--------|---------|------------|
| **P1** | 多通道通知交付基础设施 | 事件基础设施的核心断层；NotificationRules 死数据影响 S3 协议兼容性信任 | 无（独立于其他方向） | **当前 Sprint** |
| **P1** | 预签名 URL 安全策略 | 直接影响安全性审计；API 改动小但影响大 | 无 | **当前 Sprint** |
| **P1** | 外部身份联合（OIDC/SAML） | 企业准入条件；RFP 必答题 | Auth 基础设施已成熟；接口稳定 | **下一 Sprint** |
| **P2** | SLA/SLO 合规测量 | 依赖已有可观测性基础设施；主要是计算+配置 | Telemetry 指标已完备 | **下下 Sprint** |
| **P2** | 推送式遥测导出 | 依赖已有 OTel 基础设施；主要是导出配置 | OTel 集成已存在 | **下下 Sprint** |

### 与现有 ROADMAP 方向的关系

| 本方向 | 与 ROADMAP 的重叠 | 差异化价值 |
|--------|------------------|-----------|
| 通知交付基础设施 | ROADMAP 未覆盖通知交付基础设施的通道多样性 | 将死数据（NotificationRules）变成活能力 |
| 预签名 URL 安全策略 | ROADMAP #8（数据完整性）有间接关联 | 聚焦安全策略面，非数据完整性 |
| 外部身份联合 | ROADMAP #4（运维控制面）提及 tenants/keys/secrets | 将本地密钥管理扩展到企业级身份联合 |
| SLA/SLO 合规测量 | ROADMAP #2（可观测性/成本/背压）覆盖指标 | 将指标升级为业务承诺 |
| 推送式遥测导出 | ROADMAP #2 间接关联 | 聚焦指标导出基础设施，非指标内容 |

---

## 附录：去重验证方法

每个方向验证方法：

| # | 方向 | 验证关键词 | 覆盖文件数 | 最高覆盖深度 |
|---|------|-----------|-----------|------------|
| 1 | 预签名 URL 安全策略 | `presign.*ip\|presign.*policy\|presign.*condition\|presigned.*url.*policy` | **0** | ❌ 零覆盖 |
| 2 | 外部身份联合 | `oidc\|saml\|federat.*auth\|sso\|identity.*provider\|keycloak\|okta` | 4（v5/v10/v13/v24） | ⚠️ v5 方向表 10 行浅层草图，无代码锚定、无架构概要、无边界分析 |
| 3 | 多通道通知交付 | `slack\|email.*notif\|sms\|pagerduty\|teams\|discord\|notification.*channel` | 3（v12/v33/v35） | ⚠️ 各一行配置举例/表字段举例，无架构分析 |
| 4 | SLA/SLO 合规测量 | `sla.*report\|slo.*compliance\|error.*budget.*dashboard\|service.*level.*agree.*report` | 1（v38） | ⚠️ v38 方向四末尾 4 行提及 error budget，聚焦领域为结构化错误域，非 SLA 独立方向 |
| 5 | 推送式遥测导出 | `remote.*write.*prometheus\|thanos\|cortex.*metric\|mimir\|victoria.*metrics\|pushgateway\|statsd\|datadog` | **0** | ❌ 零覆盖 |

> **验证范围：** `docs/requirements/` 下全部文件（44 个分析文档）+ `docs/ROADMAP.md` + `docs/CHANGELOG.md` + `docs/TODO.md` + `docs/adr/DECISIONS.md` + `docs/extensions*.md`
