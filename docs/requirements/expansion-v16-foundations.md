# AeroVault 高价值扩展方向（第十六期）

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全局代码扫描（237+ Go 源码文件，约 50K 行），逐包审阅 `internal/` 全部 23 个子包、`cmd/server/main.go`、配置系统、SDK 三层、全部 24 对迁移文件、全部 Helm chart/Grafana/Prometheus 部署配置。逐一比对前十五期 expansion 文档（`expansion-directions.md` ~ `expansion-v15-cross-cutting-gaps.md`，共约 850KB 分析）、`ROADMAP.md`（10 方向）、`CHANGELOG.md`、`TODO.md`，确认每个方向在**既有文档中零覆盖或方向定位完全不同**。  
> **日期：** 2026-07-10  
> **原则：** 选取 5 个**生产级基础设施 / 安全基线**方向——不是新功能，而是部署到真实生产环境前必须补齐的短板。每个方向附带：代码锚点、当前状态、缺失能力、边界情况、架构概要、实现理由。**不编写任何实现代码。**

---

## 审阅背景：前十五期覆盖的去重矩阵

前十五期（v1–v15）已从约 15 个视角覆盖约 75 个方向。以下大类已深度覆盖，**本期不再重复**：

| 领域 | 期数 | 方向数 |
|------|------|--------|
| AI/RAG 管线（Embed/Search/Chat/Agent/PII/Rerank/Indexer/Cache） | v1~v13, ROADMAP #1~#2 | ~12 |
| S3 兼容性（子资源/ACL/Policy/CORS/Logging/Notification/Batch） | v1, v4, v6, v8~v10, ROADMAP #7 | ~8 |
| 存储后端（S3/OSS/COS/KMS/SSE/Encryption/CircuitBreaker/Multi-Backend） | v4~v15, ROADMAP #5 | ~7 |
| 认证授权（JWT/API Key/SigV4/OIDC/SAML/SCIM/MFA/Policy Engine） | v1, v5, v8, v11~v12, v15 | ~7 |
| 多租户（CRUD/Quota/Budget/Audit/Egress/治理） | v1, v3~v5, v7~v8, v11~v12 | ~6 |
| 事件/通知/Webhook/SSE/Bus/Transport/Kafka | v1, v3~v6, v8~v9, v11~v12 | ~7 |
| 复制/HA/Cluster/Active-Active/Federation | v1, v3~v5, v9, ROADMAP #3, #10 | ~6 |
| Reconcile/GC/Lifecycle/Orphan/Retention/Scrub | v1, v4, v6~v7, ROADMAP #5, #8 | ~5 |
| 合规（WORM/Legal Hold/Disposition/Client Encryption） | v2, v6, v8~v10, v12 | ~5 |
| 存储分层/冷存储/Tier Transition/Lifecycle | v1, v3, v5, v15, ROADMAP #9 | ~5 |
| 内容智能（DLP/分类/格式转换/预览/CAS/元数据 Schema） | v6~v8, v12 | ~4 |
| 可观测性（OTel/Metrics/Grafana/Prometheus/SLO/Tracing） | v11~v14 | ~4 |
| 工程质量（内存安全/并发/压缩/诊断/错误模型/测试） | v11, v14 | ~6 |
| Web UI / Admin UI / 开发者体验 | v3, v6, v10~v11 | ~4 |
| 其他（API 治理/备份/迁移/优雅关闭/CDN/分享链接） | v2, v4, v8, v10~v11 | ~6 |

**本期选点原则：** 选取**生产级基础设施 / 安全基线 / 运维可靠性**方向——不是功能缺失，而是现行系统缺乏的"一个产品要部署到生产环境必须有的东西"。每个方向都是目前**完全或实质性缺失**，但任何真实部署都绕不开的基础能力。

---

## 本期方向总览

| # | 方向 | 类型 | 影响评估 | 核心代码锚点 | 既有覆盖 |
|---|------|------|---------|-------------|---------|
| 1 | **🔴 配置热重载（Config Hot Reload）** | 运维/可靠性 | 每次配置变更需重启进程 → 连接断开、服务中断；生产环境不可接受 | `cmd/server/main.go:config.Load()`（启动时一次性调用）、`internal/config/config.go`（纯静态结构） | **零覆盖** |
| 2 | **🟠 IP 访问控制 / 网络策略中间件** | 安全 | 企业安全准入基线；无源 IP 过滤能力意味着 API 直接暴露给全网 | `internal/middleware/middleware.go`（无 IP 相关中间件）、`internal/api/rest/router.go`、`internal/api/s3compat/router.go` | v4/v8 作为子项提及，非独立安全方向 |
| 3 | **🟠 内置 TLS/HTTPS 与证书生命周期管理** | 安全/运维 | 当前纯 HTTP；生产部署必须依赖外部反向代理；无 ACME 自动续签 | `cmd/server/main.go:runServer()`（`http.Server` 无 TLS 字段）、`internal/config/config.go`（无 TLS 配置段） | **零覆盖** |
| 4 | **🟡 S3 Bucket Inventory / 桶清单报告** | 合规/运维 | SOC2/PCI 合规必备；成本分析与生命周期策略的输入；S3 标准功能缺失 | `internal/api/s3compat/handler.go`（无 `?inventory` 路由）、`internal/repository`（无 inventory 表） | v1/exp 仅提及 manifest 作为批处理输入，非独立方向 |
| 5 | **🟡 对象级 Legal Hold & Retention S3 子资源 API** | 合规/兼容性 | S3 标准子资源 `?legal-hold`/`?retention` 未实现；当前仅 bucket 级配置 + PUT header 注入 | `internal/api/s3compat/handler.go:dispatchObjectLock`（仅 bucket 级 lock）、`internal/service/file_crud.go:checkLockBeforeOverwrite`（仅有 `locked_until`） | v2/exp #2 讨论的是锁模式治理，非 S3 子资源 API 完整性 |

---

## 1. 🔴 配置热重载（Config Hot Reload）

### 为什么需要它

当前系统在启动时调用 `config.Load()` 一次性读取环境变量，将所有配置冻结为静态 `Config` 结构体。此后任何配置变更——无论是临时调大速率限制应对流量高峰、修改日志级别、更换 AI 模型端点、添加/撤销 API Key、更改 CORS 域名——都需要**完全重启进程**。

在生产环境中，重启意味着：
- 所有活跃连接断开（包括 SSE 事件流、正在进行的文件传输）
- 负载均衡器需要重新发现后端（健康检查窗口期内可能返回 503）
- 内存中的缓存（BM25 索引、embedding 缓存、key 缓存）需要重新预热
- 如果多副本滚动更新，每个副本启动时需要重建状态

**这不是"nice to have"，而是任何生产部署在第一天就会遇到的问题。**

### 当前状态

```
启动时                                 运行时
───────                                ──────
config.Load()  ──→  static Config        ❌ 任何变更 → 必须重启
                        ↓
              buildStorage(cfg)
              buildAuthRegistry(cfg)
              buildEmbedder(cfg)
              ...
```

| 配置项 | 热重载需求度 | 当前方式 | 重启影响 |
|--------|------------|---------|---------|
| `RATE_LIMIT_RPS` / `RATE_LIMIT_BURST` | 🔴 高（流量突发应对） | 环境变量 → 重启 | 限流失效窗口内后端被冲垮 |
| `AUTH_*` / API Keys | 🔴 高（安全事件响应） | 持久化 key 已实现但不支持热加载 | 泄漏后无法秒级撤销 |
| `APP_LOG_LEVEL` | 🟠 中（线上排障） | 环境变量 → 重启 | 排障窗口丢失 |
| `CORS_ALLOWED_*` | 🟠 中（前端部署变更） | 环境变量 → 重启 | 前端部署阻塞 |
| `AI_ENDPOINT` / `AI_MODEL` | 🟠 中（模型升级/故障切换） | 环境变量 → 重启 | 模型切换需要分钟级停机 |
| `EVENTS_WEBHOOK_URL` | 🟡 低（配置时固定） | 环境变量 → 重启 | 偶发变更可接受 |

### 边界情况暴露

| Edge Case | 场景 | 后果 |
|-----------|------|------|
| **部分重载失败** | 新配置验证通过 80%，第 81 行非法 | 服务维持旧配置运行，但系统处于"新配置已解析但未应用"的不确定状态 |
| **并发读写** | 重载过程中有请求正在读取 `cfg.RateLimit.RPS` | 竞态条件：请求可能读到新旧混杂的配置视图 |
| **依赖顺序** | AI 端点变更需要重建 embedder/LLM 客户端；Ratelimiter 需要重置 token bucket | 重载顺序错误导致中间状态不可用 |
| **重载触发源** | 文件变更（fsnotify）vs SIGHUP vs HTTP API vs K8s ConfigMap watch | 多源并发触发导致重载风暴 |

### 架构方向

```
┌──────────────────────────────────────────────────────────────┐
│                     Config Manager                            │
│                                                              │
│  触发源:                                                      │
│    • SIGHUP signal handler                                   │
│    • fsnotify on .env / config file                          │
│    • POST /v1/admin/reload (admin scope, with audit)         │
│    • K8s ConfigMap watch (future)                            │
│                                                              │
│  原子语义:                                                    │
│    1. 解析新配置 + 完整校验                                    │
│    2. 快照当前配置（rollback 锚点）                             │
│    3. 逐组件 apply（必须先关后开）                              │
│    4. 全部成功 → 原子切换指针                                   │
│    5. 部分失败 → 回滚 + 日志告警                                │
│                                                              │
│  线程安全:                                                    │
│    • sync.RWMutex + atomic.Value 保护共享配置指针              │
│    • 组件级（rate limiter、auth registry）各自实现 reload()     │
│                                                              │
│  组件注册:                                                    │
│    config.Register("ratelimit", rl.Reload)                    │
│    config.Register("auth", authReg.Reload)                    │
│    config.Register("logger", logLevelReload)                  │
└──────────────────────────────────────────────────────────────┘
```

### 实现理由

**商业价值：** 生产系统宕机的第一大原因不是代码缺陷，而是配置变更。热重载将配置变更从"高危操作"降级为"日常运维"。

**技术必要性：** 安全事件响应（API Key 泄露）要求分钟级甚至秒级撤销；流量高峰应对要求秒级调整限流参数。当前重启模型无法满足。

**代码复杂度：** 中等。核心模式是"整个配置结构体不可变 + 原子指针交换"，避免竞态。每个组件需要暴露一个 `Reload(newCfg) error` 方法给注册表。

---

## 2. 🟠 IP 访问控制 / 网络策略中间件

### 为什么需要它

当前系统对客户端身份完全依赖 API Key / JWT / SigV4 等应用层凭证，**没有对客户端网络层的任何准入控制**。这意味着：
- 任何能访问到服务端口的 IP 都可以发起认证请求
- 无法实现"仅允许公司 VPN CIDR 访问管理端点"
- 无法将特定桶/API 开放给 CDN 回源 IP 白名单
- 多租户场景下无法对恶意 IP 做即时封禁

**这是企业安全架构的基础设施，比应用层认证更底层**——IP 过滤在 TLS 终止后、应用层处理前生效，作为第一道防线。

### 当前状态

```
请求到达
   ↓
TLS? 无（纯 HTTP）
   ↓
IP 过滤? 无（没有任何 IP 检测逻辑）
   ↓
CORS 中间件（检查 Origin header，不检查 IP）
   ↓
Auth 中间件（检查 Bearer / X-Api-Key / SigV4，不检查 IP）
   ↓
RateLimiter（检查 tenant，不检查 IP）
   ↓
Tenant 中间件（提取 X-Aero-Tenant，不检查 IP）
   ↓
Handler
```

| 代码位置 | 当前行为 | 缺失能力 |
|---------|---------|---------|
| `middleware/middleware.go` | 无 IP 检查中间件 | 无 `IPFilter` 或 `NetworkPolicy` 中间件 |
| `internal/config/config.go` | 无 IP 过滤相关配置 | 无 `ALLOWED_IPS` / `BLOCKED_IPS` / `ALLOWED_CIDRS` |
| `internal/auth/policy.go` | IAM 策略引擎支持 `IpAddress` / `NotIpAddress` 条件 | 仅存在于策略模型中，未在请求路径上生效 |
| `internal/api/rest/router.go` | 无 IP 限制 | 无法为 `/v1/admin/*` 设置独立 IP 白名单 |
| `internal/middleware/cors.go` | 基于 Origin header | IP 与 Origin 无关 |

### 边界情况暴露

| Edge Case | 场景 | 后果 |
|-----------|------|------|
| **反向代理部署** | 服务在 Nginx/ALB 后面，`RemoteAddr` 永远是代理 IP | `X-Forwarded-For` 伪造可绕过 IP 限制 |
| **IPv6 双栈** | 客户端来自 IPv6 地址，CIDR 配置只写了 IPv4 | IPv6 流量全部放行或全部拒绝 |
| **动态 IP** | 客户端 IP 频繁变化（移动网络、NAT） | 合法用户被错误阻断 |
| **性能** | 每次请求检查 IP→CIDR 匹配（Go 的 `net.IPNet.Contains` 是 O(1)） | 大量 CIDR 规则下需要 radix tree 优化 |

### 架构方向

```
┌──────────────────────────────────────────────────────────────┐
│               Network Policy Middleware                       │
│                                                              │
│  位置: RequestID → CORS → NETWORK_POLICY → Auth → Tenant →  │
│             ↑ 在 Auth 之前，作为第一道防线                     │
│                                                              │
│  配置:                                                        │
│    NETWORK_POLICY='[                                          │
│      {"action":"allow","cidr":"10.0.0.0/8"},                 │
│      {"action":"allow","cidr":"192.168.0.0/16"},              │
│      {"action":"deny","cidr":"0.0.0.0/0"},                   │
│    ]'                                                         │
│                                                              │
│  端粒度:                                                      │
│    • 全局默认策略                                             │
│    • /v1/admin/* 独立策略                                     │
│    • 按 tenant 的策略（企业租户要求仅公司 IP 可访问）          │
│                                                              │
│  信任模型:                                                    │
│    NETWORK_POLICY_TRUSTED_PROXIES='10.0.0.0/8,172.16.0.0/12' │
│    • 来自信任代理的请求 → 取 X-Forwarded-For 最后一个信任 IP   │
│    • 直接连接的请求 → 取 RemoteAddr                           │
│    • 未知来源 → 拒绝                                          │
│                                                              │
│  性能:                                                        │
│    • CIDR 列表使用 radix tree（net/http 内有现成实现）         │
│    • 缓存解析结果（TTL 级缓存，响应 IP 变化）                  │
└──────────────────────────────────────────────────────────────┘
```

### 实现理由

**商业价值：** 企业安全评估的兜底问题永远是"如何限制网络访问"。无 IP 过滤能力的存储服务在金融、政务、医疗行业的技术评审中被直接淘汰。

**技术必要性：** 应用层认证并不能防御来自攻击者 IP 的暴力破解、DDoS 和 credential stuffing。IP 过滤提供最小成本的第一道防线。

**代码复杂度：** 低到中。一个中间件文件（~150 行）+ 配置解析 + CIDR radix tree（可复用 `go.uber.org/ratelimit` 或第三方库）。已有 `net.IPNet.Contains()` 原生支持 CIDR 匹配。

---

## 3. 🟠 内置 TLS/HTTPS 与证书生命周期管理

### 为什么需要它

当前 `runServer()` 创建了一个纯 `http.Server`，**不支持 TLS**：

```go
srv := &http.Server{
    Addr:    cfg.App.Addr,
    Handler: handler,
    ...
}
_ = srv.ListenAndServe()  // 纯 HTTP，无 TLS
```

任何生产部署必须依赖外部反向代理（Nginx、ALB、HAProxy、Traefik）来终止 TLS。这在以下场景中成为痛点：
- **边缘/嵌入式部署**：用户在一个小机器上直接运行二进制，没有也不想搭反向代理
- **开发/演示环境**：快速启动时希望有 HTTPS 而不是 `http://`
- **Kubernetes 自包含部署**：Helm chart 中用户期望服务自己能处理 TLS 和服务网格配合
- **IoT/设备端**：资源受限，不愿运行额外的 TLS 代理进程

此外，证书的获取、续签、轮换是独立于业务逻辑的运维负担——**ACME（Let's Encrypt）自动证书管理可以消除这个负担**，使 TLS 成为"零配置"体验。

### 当前状态

| 组件 | 现状 | 缺失 |
|------|------|------|
| `cmd/server/main.go:runServer()` | `http.Server` + `ListenAndServe()` | `ListenAndServeTLS()` 未使用 |
| `internal/config/config.go` | 无 TLS 配置段 | `TLS` 结构体完全不存在 |
| `internal/config/config_app.go` | 只有 Addr/LogLevel/Timeout | 无 `TLSEnabled`/`TLSCertPath`/`TLSKeyPath` |
| `deploy/helm/aero-vault/templates/ingress.yaml` | 有 Ingress TLS 配置（外部终止） | 没有 pod 级别的 TLS 选项 |
| `Dockerfile` | 无证书卷挂载 | 无 ACME 客户端 |

### 边界情况暴露

| Edge Case | 场景 | 后果 |
|-----------|------|------|
| **证书过期** | Let's Encrypt 90 天有效期，自动续签失败 | 服务无法建立 TLS 连接，静默降级到 HTTP 或不可达 |
| **混合 HTTP/HTTPS** | 部分客户端用 HTTP，部分用 HTTPS | 重定向策略不当导致 API 客户端断连 |
| **自签名证书** | 内部部署用自签名 CA | 客户端需要分发 CA 证书 |
| **证书 revocation** | 私钥泄露需要立即吊销 | OCSP/CRL 检查未实现 |
| **SNI 多域名** | 同一服务提供多个租户域名 | 目前无域名感知路由 |

### 架构方向

```
┌──────────────────────────────────────────────────────────────┐
│                 TLS Configuration                             │
│                                                              │
│  配置选项:                                                    │
│    TLS_ENABLED=true                                           │
│    TLS_CERT_PATH=/certs/tls.crt   (显式证书)                  │
│    TLS_KEY_PATH=/certs/tls.key                               │
│    或:                                                        │
│    TLS_ACME_ENABLED=true          (自动 ACME)                │
│    TLS_ACME_DOMAINS=example.com,api.example.com              │
│    TLS_ACME_EMAIL=admin@example.com                          │
│    TLS_ACME_STORAGE=/var/aero/acme-certs                     │
│                                                              │
│  HTTP → HTTPS 重定向:                                         │
│    TLS_REDIRECT_HTTP_TO_HTTPS=true                           │
│                                                              │
│  热重载:                                                      │
│    • 证书文件变更 → fsnotify → 自动 reload                   │
│    • ACME 自动续签 → 下载新证书 → reload                    │
│    • POST /v1/admin/tls/reload (手动触发)                    │
│                                                              │
│  健康检查:                                                    │
│    • /readyz 应检查证书是否在 30 天内过期                      │
│    • Prometheus 指标：tls_cert_expires_seconds                │
└──────────────────────────────────────────────────────────────┘
```

### 实现理由

**商业价值：** TLS 已不再是可选。浏览器要求 HTTPS、移动应用强制 HTTPS、合规审计要求传输加密。一个不支持 TLS 的对象存储服务在 2026 年无法向客户交付。

**技术必要性：** 虽然可以通过反向代理实现 TLS，但内置支持带来几个关键好处：① 零依赖部署；② 证书热重载与业务逻辑协同；③ ACME 自动管理消除运维负担；④ 与 HTTP/2 原生集成（Go http.Server 在 TLS 上自动启用 HTTP/2）。

**代码复杂度：** 低。Go 标准库原生支持 `http.Server` + `tls.Config` + `ListenAndServeTLS`。`golang.org/x/crypto/acme/autocert` 提供了开箱即用的 Let's Encrypt 支持。配置段约 30 行，中间件重定向约 20 行。

---

## 4. 🟡 S3 Bucket Inventory / 桶清单报告

### 为什么需要它

AWS S3 Inventory（S3 清单）是一个标准功能：按每日或每周的调度，生成目标桶中**所有对象**的 CSV/Parquet 报告，包含每个对象的 key、大小、ETag、存储类、最后修改时间、是否加密等元数据。该报告输出到同一个桶（或另一个桶）的指定前缀下。

**当前缺失这个功能意味着：**
- 没有标准方式获取桶内所有对象的清单（`ListObjectsV2` 最多返回 1000 条/页，对于百万对象需要多次 API 调用且无一致性保障）
- 合规审计无法获得离线可交付的"当前快照"
- 生命周期策略的输入（"批量设置过期"）需要先知道有哪些对象
- 没有"对象的定期一致性校验"的基础（比对清单 vs 实际存储）
- 成本分析依赖于运行时的 API 调用，无法离线分析

### 当前状态

| 位置 | 现状 | 缺口 |
|------|------|------|
| `internal/api/s3compat/handler.go` | 处理 `?acl` `?versioning` `?lock` `?lifecycle` `?location` `?versions` `?policy` `?cors` `?logging` `?notification` `?accelerate` `?tagging` `?restore` | **无 `?inventory` 路由处理** |
| `internal/repository/sql_buckets.go` | `SELECT` 包含 `policy,cors_rules,logging_target,logging_prefix,notification_rules` | 无 inventory 配置字段 |
| `internal/repository/repository.go` | `BucketConfig` 已含 Policy/CORSRules/Logging/NotificationRules | 无 `InventoryConfig` |
| `internal/service/file_features.go` | `SetBucketPolicy`/`GetBucketCORS`/`SetBucketLogging`/`GetBucketNotifications` 等 | 无 `SetBucketInventory`/`GetInventoryList` |
| `internal/reconcile/` | 处理 lifecycle/retention/orphan/scrub | 无 inventory generation job |
| `internal/repository/migrations/` | 0024 → notifications | 无 inventory 迁移 |

### 边界情况暴露

| Edge Case | 场景 | 后果 |
|-----------|------|------|
| **超大桶（>1 亿对象）** | 生成清单需要扫描整个表 | DB 负载飙升；生成时间超过调度间隔导致重叠 |
| **清单文件断写** | 生成过程中进程崩溃 | 输出部分清单，校验和不匹配 |
| **并发一致性** | 生成过程中对象被创建/删除 | 清单反映的是"某个时间点"的快照，需要定义一致性边界 |
| **存储成本** | 每日清单本身也消耗存储 | 需要生命周期规则自动清理过期清单 |
| **加密桶** | 清单中的 ETag 是加密后的还是原始内容的？ | 需要与 S3 行为对齐（报告的是原始对象 ETag，不是加密后） |

### 架构方向

```
┌──────────────────────────────────────────────────────────────┐
│               Bucket Inventory Service                        │
│                                                              │
│  配置 (S3-compatible):                                       │
│    PUT /{bucket}?inventory 接受 XML:                          │
│      <InventoryConfiguration>                                 │
│        <Destination>                                          │
│          <Bucket>arn:...:target-bucket</Bucket>               │
│          <Prefix>inventory-reports</Prefix>                   │
│        </Destination>                                         │
│        <Schedule>Daily|Weekly</Schedule>                      │
│        <OptionalFields>Size,ETag,StorageClass,...</Fields>    │
│        <Filter><Prefix>documents/</Prefix></Filter>           │
│      </InventoryConfiguration>                                │
│                                                              │
│  存储:                                                        │
│    • 报告表: inventory_configs (per-bucket 配置)              │
│    • 报告文件：存储在目标桶的指定前缀下                        │
│      <prefix>/<bucket>/<YYYY>-<MM>-<DD>T<HH>-<MM>-<Z>.csv    │
│                                                              │
│  生成引擎 (基于 JobPool):                                     │
│    JobType: "generate_inventory"                              │
│    • 调度: reconcile 循环内检查到期的 inventory configs       │
│    • 生成: SELECT * FROM objects WHERE tenant=$1 AND bucket=$2│
│           → 流式写入 CSV                                      │
│    • 校验: 写入完成后计算行数 + SHA256 校验和                  │
│    • 清理: 保留 N 天历史清单（由生命周期规则管理）              │
│                                                              │
│  输出格式:                                                    │
│    CSV header: bucket,key,version_id,size,etag,storage_class, │
│                content_type,last_modified_at,is_delete_marker  │
│    兼容 S3 Inventory CSV schema                               │
└──────────────────────────────────────────────────────────────┘
```

### 实现理由

**商业价值：** S3 Inventory 是企业级对象存储的"隐形必需品"。客户的合规团队、财务团队、运维团队都依赖清单来做数据审计、成本分摊和容量规划。没有清单功能的存储服务在合规评审中被视为"不可审计"。

**技术必要性：** 当前通过 `ListObjectsV2` pagination 获取全量列表不适用于大规模桶（100 万对象需要 1000 次 API 调用，耗时数分钟且无法保证快照一致性）。定期生成的清单文件是更高效、标准化的替代方案。

**代码复杂度：** 中。需要：① schema 迁移 + BucketConfig 扩展（~50 行）；② 清单生成 job（~200 行）；③ S3 API handler for `?inventory`（~150 行）；④ reconcile 调度集成（~30 行）。总体 ~500 行。

---

## 5. 🟡 对象级 Legal Hold & Retention S3 子资源 API

### 为什么需要它

AWS S3 Object Lock 有两个维度的控制：

| 控制方式 | 粒度 | S3 API 端点 | 当前 AeroVault 状态 |
|---------|------|------------|-------------------|
| **桶级默认保留模式** | 桶 | `PUT/GET ?object-lock` | ✅ 已实现（`object_lock_seconds`） |
| **桶级默认保留期限** | 桶 | 同上 | ✅ 已实现 |
| **对象级 Legal Hold** | 对象 | `PUT/GET ?legal-hold` | ❌ **缺失**（仅有 PUT header 注入为 metadata） |
| **对象级 Retention** | 对象 | `PUT/GET ?retention` | ❌ **缺失** |
| **Governance 模式绕过** | 请求 | `x-amz-bypass-governance-retention` header | ❌ **缺失** |

当前代码通过以下方式实现了 *部分* WORM 能力：
- `bucketconfig.go`: 桶级 `object_lock_seconds` + `ObjectLockEnabled`
- `handler.go:93-98`: PUT 时检测 `x-amz-object-lock-legal-hold` header 并注入 metadata `_aero_legal_hold`
- `file_crud.go:301`: 硬删除时检查 `_aero_legal_hold == "ON"`
- `file.go:ErrLocked`: 对象被锁定时的错误类型

**但缺失了标准 S3 子资源 API，导致 S3 SDK 客户端的标准调用方式无法工作：**

```go
// AWS SDK v2 — 这段代码在当前系统上会返回 404
_, err := client.PutObjectLegalHold(ctx, &s3.PutObjectLegalHoldInput{
    Bucket: &bucket,
    Key:    &key,
    LegalHold: &types.ObjectLockLegalHold{
        Status: types.ObjectLockLegalHoldStatusOn,
    },
})
```

### 当前状态

```
S3 PUT /bucket/key?legal-hold        → 404（路由未注册）
S3 GET /bucket/key?legal-hold        → 404（路由未注册）
S3 PUT /bucket/key?retention         → 404（路由未注册）
S3 GET /bucket/key?retention         → 404（路由未注册）
x-amz-bypass-governance-retention     → 未检测（静默忽略）

已有但仅限于 PUT header:
  x-amz-object-lock-legal-hold: ON   → 写为 metadata _aero_legal_hold
  x-amz-object-lock-retain-until-date → 写入 locked_until
  x-amz-object-lock-mode             → 未区分 GOVERNANCE/COMPLIANCE
```

| 代码位置 | 当前能力 | 缺失 |
|---------|---------|------|
| `internal/api/s3compat/handler.go:dispatchObjectLock` | `?object-lock` bucket 级别 | 无 `?legal-hold` 和 `?retention` 对象级别分发 |
| `internal/api/s3compat/handler.go:93-98` | PUT 时解析 legal-hold header → metadata | 无独立的 sub-resource handler |
| `internal/service/file_crud.go:301` | 检查 `_aero_legal_hold` 元数据 | 无基于 retention 日期的检查 |
| `internal/service/file.go:ErrLocked` | 通用"对象被锁"错误 | 无区分 GOVERNANCE 与 COMPLIANCE |
| `internal/repository/repository.go:Object` | `LockedUntil` 时间字段 | 无 `LegalHold` / `RetentionMode` 独立字段 |

### 边界情况暴露

| Edge Case | 场景 | 后果 |
|-----------|------|------|
| **Governance 模式绕过** | 合规管理员需要删除或修改保留期限内的对象 | 当前无 `x-amz-bypass-governance-retention` header 支持，所有保留对象不可变 |
| **Legal Hold 与 Retention 叠加** | 同一个对象既有 Legal Hold 又有 Retention | 需要正确组合语义（任一锁定即禁止删除） |
| **桶级默认 vs 对象级覆盖** | 桶设置 30 天默认保留，对象设置 90 天 | 对象级应覆盖桶级默认 |
| **跨协议一致性** | REST 和 WebDAV 协议操作同一对象 | 锁定状态必须在所有协议上一致生效 |
| **Retention 到期精度** | 保留期限到秒级别，但 DB 存的是时间戳 | 需要精确到秒的比较，防止时区错乱 |

### 架构方向

```
┌──────────────────────────────────────────────────────────────┐
│           Object Lock Sub-Resource APIs                       │
│                                                              │
│  Schema 扩展:                                                 │
│    objects 表新增字段:                                        │
│      legal_hold         TEXT  DEFAULT ''  -- 'ON'|'OFF'|''   │
│      retention_mode     TEXT  DEFAULT ''  -- 'GOVERNANCE'|   │
│                                              'COMPLIANCE'|'' │
│      retention_until    TEXT  DEFAULT ''  -- RFC3339Nano     │
│                                                              │
│  路由 (在 /{bucket}/* 的 BucketDispatch 内):                  │
│    ?legal-hold  → handleObjectLegalHold                      │
│    ?retention   → handleObjectRetention                       │
│                                                              │
│  Legal Hold Handler:                                          │
│    PUT /{bucket}/{key}?legal-hold                            │
│      <LegalHold><Status>ON|OFF</Status></LegalHold>          │
│      → UPDATE objects SET legal_hold=$1 WHERE ...            │
│    GET /{bucket}/{key}?legal-hold                            │
│      → 返回 LegalHold XML                                    │
│                                                              │
│  Retention Handler:                                           │
│    PUT /{bucket}/{key}?retention                             │
│      <Retention><Mode>GOVERNANCE|COMPLIANCE</Mode>           │
│               <RetainUntilDate>2026-12-31T23:59:59Z</Date>  │
│      </Retention>                                             │
│      → 校验 mode + 写入 retention_until                      │
│    GET /{bucket}/{key}?retention                             │
│      → 返回 Retention XML                                    │
│                                                              │
│  删除保护:                                                    │
│    if obj.legal_hold == "ON" → ErrLocked                     │
│    if obj.retention_mode != "" && retention_until > now      │
│      if mode == "COMPLIANCE" → ErrLocked (不可绕过)          │
│      if mode == "GOVERNANCE" && !has_bypass_header → ErrLocked│
│    bypass header: x-amz-bypass-governance-retention: true    │
│                                                              │
│  API 兼容性:                                                  │
│    S3 SDK 的 PutObjectLegalHold / GetObjectLegalHold         │
│    PutObjectRetention / GetObjectRetention 全部可用          │
└──────────────────────────────────────────────────────────────┘
```

### 实现理由

**商业价值：** 对象锁（WORM）是金融、医疗、政务等行业的核心合规需求。没有标准的 S3 sub-resource API，S3 SDK 客户端无法以标准方式设置/查询 Legal Hold 和 Retention。这直接限制了客户从 AWS S3 迁移到 AeroVault 的能力——任何依赖 `PutObjectLegalHold` 的代码都无法工作。

**技术必要性：** 当前通过 metadata hack（`_aero_legal_hold`）实现的 Legal Hold 是非标准的，缺失 RetainUntilDate 的精确比较逻辑，无法区分 GOVERNANCE/COMPLIANCE 两种安全模式的绕过策略。这是合规审计中会被标记的严重缺口。

**代码复杂度：** 中。约 300-400 行新代码：① schema 迁移（双 SQL 文件）；② service 层新方法；③ S3 handler sub-resource dispatch；④ XML 编解码类型。与现有的 `bucketconfig.go:dispatchObjectLock` 和 `file_crud.go:checkLockBeforeOverwrite` 自然衔接。

---

## 总结：实施优先级

| 方向 | 影响 | 复杂度 | 风险 | 建议阶段 |
|------|------|--------|------|---------|
| 1. 配置热重载 | 🔴 运维可靠性 | 中 | 低（原子交换模式成熟） | **Phase 1**（基础设施） |
| 2. IP 访问控制 | 🟠 安全基线 | 低 | 低（独立中间件，无侵入） | **Phase 1**（基础设施） |
| 3. 内置 TLS/HTTPS | 🟠 安全基线 | 低 | 低（标准库原生支持） | **Phase 1**（基础设施） |
| 4. S3 Bucket Inventory | 🟡 合规/运维 | 中 | 低（新增功能，不影响现有） | **Phase 2**（合规增强） |
| 5. 对象级 Legal Hold/Retention API | 🟡 合规/兼容性 | 中 | 中（涉及删除保护语义） | **Phase 2**（合规增强） |

**建议 Phase 1 先行补齐三条基础设施基线**（配置热重载 + IP 访问控制 + TLS），再进入 Phase 2 的合规功能。三条基线彼此独立，可并行开发。
