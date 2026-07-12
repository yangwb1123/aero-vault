# 架构分析报告：aero-vault 四个高价值扩展方向

> **分析基准：** 全代码库扫描（231 Go 源文件 + 3 SDK + MCP + Web UI + 50 迁移文件 + 部署配置）  
> **输入文档：** `docs/requirements/expansion-v141-server-copy-webhook-security-tracing.md`  
> **分析视角：** 架构权衡、设计决策评估、扩展路径、接口设计、技术选型、实施路线图

---

## 一、架构评估

### 1.1 当前架构的优势

aero-vault 在架构层面展现出几个值得肯定的设计决策：

| 优势 | 依据 | 价值 |
|------|------|------|
| **协议适配器模式** | REST/S3/WebDAV/MCP 四个协议层薄而独立，共享同一个 `FileService` | 新增协议（如 NFS、SFTP）只需新增适配器，无需修改核心逻辑 |
| **Opt-in 安全默认** | AI/pgvector/Qdrant/事件/集群 WebDAV 全部 flag-gated，`nil` 守卫贯穿 CRUD 路径 | 基线路径零依赖运行，新加入组件不会破坏既有功能 |
| **分层清晰** | `config → storage → repo → service → workers → middleware → router` 装配顺序严格 | 依赖方向明确，模块可独立测试 |
| **可测试性设计** | SQLite + local FS 作为默认基线；`ai.MockLLM` / `HashEmbedder` 等 mock 夹具 | 单元测试零网络零 Docker，CI gate 可快速执行 |
| **DAG 工作流** | EventBus → Workers/Webhook/JobPool 的事件驱动架构 | 异步解耦核心操作与副作用（AV扫描、复制、webhook） |

### 1.2 关键设计决策评估

**决策 A：`Storage` 接口不含 `Copy` 方法** — 需重新评估

当前 `Storage` 接口只有 `Put`/`Get`/`Delete`/`List`，COPY 操作只能通过 `Get → Put` 组合实现。这在项目初期（小对象为主）是合理的最小化接口设计，但随着大对象场景增加，这个决策正成为架构瓶颈：

- **S3 后端浪费服务端 Copy API** — 同一 bucket 内的 1GB 对象复制要经过 2GB 的服务器带宽
- **Local 后端无法做原子重命名** — 文件系统 `rename(2)` 是原子的，但当前通过 `Read → Write → Unlink` 模拟
- **跨后端复制逻辑与 Replication worker 重复** — 两个独立实现相同的数据移动模式

**建议：** 将 `Copy` 提升为 `Storage` 接口的第一等公民（可选方法），这是合理的晚期泛化（late generalization）——只有在使用场景成熟后才抽象。

**决策 B：中间件链固定顺序 + Handler 不自挂中间件** — 优秀设计

```
RequestID → CORS → Auth → Tenant → RateLimit → OTel → Recoverer → AccessLog
```

明确禁止 handler 自行挂载中间件，同时明确隔离 handler 测试无需 tenant/auth 上下文。这是**架构纪律**——减少了认知负载和测试的间接依赖。

**决策 C：Webhook 死信即标记成功** — 合理但已过时

在 MVP 阶段，将死信标记为成功避免了运维复杂度和数据膨胀。但随着上生产环境，这个决策导致事件永久丢失且无法补救。这是**技术债**而非架构错误——需要从「尽力交付」升级到「至少一次交付 + 死信可补救」。

**决策 D：OTel 初始化但 span 不传播** — 架构缺失

当前只有 HTTP middleware 创建根 span，Service/Storage/Repository 层没有任何 span。这是可观测性设计的**半成品**——OTel 初始化了（消耗了启动时间），但 trace 的可诊断价值打了 90% 折扣。

### 1.3 架构债务与技术债

| 债务类型 | 描述 | 严重程度 | 修复成本 |
|---------|------|---------|---------|
| **功能债** | COPY 操作无服务端优化，大对象有 OOM 风险 | 高（生产阻塞） | 中（4-6 周） |
| **可靠性债** | Webhook 死信永久丢失事件 | 高（数据丢失） | 低（2-3 周） |
| **安全债** | 无输入验证中间件、无外部身份联邦 | 中（合规缺口） | 低~高（分阶段） |
| **可观测性债** | Trace 不传播、日志无 trace_id | 中（诊断困难） | 低（2-3 周） |
| **设计债** | `copyObject` 与 `Replication` 重复实现数据移动 | 中（维护成本） | 中（需统一抽象） |

---

## 二、扩展方向分析

### 方向 A：服务端 Copy 策略模式（P1）

**为什么需要：**

存储层次存在一个范式冲突——`Storage` 接口面向「对象存取」（`Put`/`Get`/`Delete`），而 COPY/MOVE 是「数据移动」（data movement）。两者是不同的抽象层级。当前用组合（composition）模拟移动，在以下场景成为性能瓶颈：

- 同后端 COPY（S3 → S3）浪费 2x 带宽
- 大对象（>5GB）全量内存缓冲导致 OOM
- 跨后端 COPY 无进度/续传支持
- WebDAV MOVE 和 REST rename 各自实现相同模式

**核心挑战：**

1. **复制策略选择器（Copy Strategy Selector）的复杂性** — 策略取决于源后端类型、目标后端类型、对象大小、是否加密、版本化状态。决策矩阵的维度组合可能导致策略选择成为新的复杂度来源
2. **加密上下文传递** — SSE-C 加密的对象跨后端 COPY 时，需要在内存中解密再加密，但服务器端 COPY（S3）可以直接传递加密上下文。这两种路径的「密钥生命周期管理」不同
3. **原子 MOVE 的三个阶段** — 元数据层原子 + blob 移动异步 + 源清除。三个阶段之间的故障恢复是最难处理的部分

**建议的架构变更：**

```
Storage 接口扩展（可选方法）：
  - CanCopy(srcKey, dstKey) bool         → 是否支持服务端复制
  - Copy(ctx, srcKey, dstKey, opts) Info  → 服务端复制
  - Move(ctx, srcKey, dstKey) Info        → 原子移动（local rename）
  
FileService 新增 CopyStrategy 选择器：
  - ChooseStrategy(srcStore, dstStore, size) → strategy
  - strategy 接口：Copy(ctx, srcInfo, dstInfo) (Info, bool completed)
  
分段复制支持：
  - CopyPart(ctx, srcKey, dstKey, offset, length) Info
  - 复用现有的 multipart upload 基础设施
```

**对现有系统的影响：**

- `Storage` 接口需要添加新方法，所有后端（local/s3/oss/cos）需要实现——至少加一个 `noopCopy` 或 `fallbackToStream` 实现
- `FileService.CopyObject` 需要重构为策略调度器
- WebDAV `rename` 和 `copyObject` 的路由需要改道
- 正面：`Replication` worker 可以复用 `CopyStrategy`，消除重复

### 方向 B：Webhook 交付引擎成熟化（P1）

**为什么需要：**

当前 webhook 是一个「尽力而为的直发器」。从事件驱动架构的角度看，事件交付是系统的**契约边界**——消费者依赖事件通知来触发下游处理（AV 扫描、复制、外部系统集成）。如果交付层不可靠，整个事件驱动架构的可信度就坍塌了。

**核心挑战：**

1. **死信队列的管理面** — 死信事件如何查询、重新入队、丢弃、导出？这需要设计 admin API、UI 面板、操作流程。痛点不在实现 `dead_letter` 状态转换，而在**运维体验**
2. **外发速率限制的背压传播** — 接收端变慢时，per-URL token bucket 会积累等待者，但 `deliver` 是事件驱动的（EventBus 发布后立即调用），这种情况下应该降级到「写入重试队列」而不是阻塞 EventBus 的事件循环
3. **密钥轮换的双密钥过渡** — 轮换过程中，旧密钥签名的 webhook 还在重试队列中。接收端需要同时支持两个密钥验证签名。这个过渡期窗口的时间窗口选择会影响安全性

**建议的架构变更：**

```
Webhook 配置分层：
  - 全局级：EVENTS_WEBHOOK_URLS（兼容旧配置）
  - 路由级：WebhookRoute{URL, Filter, RateLimit, MaxAttempts, Timeout}
  - 存储级：notification_rules（通过 migration 0024 已有 TEXT 列）

死信管理：
  - webhook_failures 表新增 status (retrying|dead_letter|delivered)
  - Admin API: POST /admin/webhook-failures/{id}/retry
  - Admin API: POST /admin/webhook-failures/{id}/discard

交付引擎重构：
  - deliver() 不再直接 post，而是写入 delivery_queue 表
  - RetryLoop 轮询 delivery_queue（待投递）+ webhook_failures（重试）
  - per-URL rate limiter + 有界 goroutine pool
```

**对现有系统的影响：**

- `webhook_failures` 表 schema 需要 migration（新增 `status`、`max_attempts`、`delivered_at` 列）
- `MarkWebhookSucceeded` 的行为变更——不再覆盖失败历史
- `RetryLoop` 的轮询策略从「15s 固定」改为「自适应 + 按 URL 分组」
- 向后兼容：`EVENTS_WEBHOOK_URL` 单 URL 配置继续工作，视为单路由规则
- 指标 instrumentation 需要 5 个新 counter/histogram

### 方向 C：跨协议安全架构（P1）

**为什么需要：**

从安全架构的角度，当前的设计遵循的是**协议级安全**（每个协议用自己的认证机制）而非**系统级安全**（统一身份模型 + 分层纵深防御）。在协议数量从 2（REST + S3）增长到 4（+WebDAV + MCP）时，分散的安全模型会导致以下问题：

- 审计日志中四个协议的认证记录格式不同，无法做跨协议的安全分析
- 每个新协议需要单独实现认证，增加了安全漏洞的风险面
- 缺少输入验证的纵深防御层——所有安全依赖协议适配器自身的校验
- 无法集成企业身份联邦（OIDC/LDAP/SAML），限制了上生产环境的能力

**核心挑战：**

1. **JWT 签名算法的安全权衡** — 当前 HS256 对称签名要求服务器持有密钥即可签发任意身份，这在管理员自签场景下可接受。但 OIDC 联邦要求 RS256/ES256（非对称），且需要 `iss` 声明校验来阻止跨 IdP 令牌重用。两种算法需要在 `Registry` 中共存，且 HS256 必须通过 `alg` 头识别并降级
2. **输入验证中间件按路由组差异化** — REST `/v1/` 可以强制 `Content-Type: application/json`，但 S3 `/s3/` PUT 对象必须接受任意 Content-Type。这意味着输入验证中间件不能全局挂载，而需要路由组级别的配置。这增加了中间件的复杂度
3. **安全响应头与 S3 协议的兼容性** — `X-Content-Type-Options: nosniff` 在浏览器下载 S3 对象时可能被误解。某些 S3 客户端依赖浏览器行为的兼容性

**建议的架构变更：**

```
安全中间件链扩展（在 Auth 后、Handler 前）：
  - MaxBodySize(routeGroup default config)
  - EnforceContentType(routeGroup-specific)
  - SecureHeaders()
  - SafeXMLDecoder（封装 io.LimitReader + Strict mode）

身份联邦扩展：
  Registry.IdentityProviders []IdentityProvider
  IdentityProvider 接口：Name() + Authenticate(ctx, credentials) (Identity, error)
  OIDCProvider：验证 RS256 JWT + iss/aud/exp
  LDAPProvider：bind + search

统一认证上下文：
  AuthContext{Identity, Tenant, SessionID, Scopes}
  跨协议传递（通过 context.Context）
```

**对现有系统的影响：**

- 纯新增中间件——不修改现有 handler 代码
- 安全 headers 在 chi middleware 链中新增即可
- OIDC 联邦新增 `internal/auth/oidc.go` 和 `internal/auth/ldap.go`，不影响现有 `Registry`
- XML 安全解析需要替换 `s3compat/handler.go` 中所有直接的 `xml.NewDecoder(r.Body).Decode()` 调用
- 主要风险点：Content-Type 强制校验对 S3 PUT 的兼容性——需要路由组级别的白名单

### 方向 D：分布式追踪成熟化（P2）

**为什么需要：**

虽然 OTel 初始化了，但 trace 的架构价值完全没有被利用。当前的状态相当于「安装了传感器但没有连接数据线」——从延迟分解、跨组件诊断、跨进程追踪的角度看，可观测性存在结构性缺口。

**核心挑战：**

1. **采样策略的配置复杂性** — head-based sampling 需要在 span 创建时就决定是否采样，但不同路由组的采样率不同（search/chat 100%、files GET 1%）。这需要在中间件中区分路由组，并为下游 span 的 `IsRecording()` 提供一致的结果
2. **WebDAV/MCP 的 trace 传播** — WebDAV 在 chi 路由外独立分发，MCP stdio 模式无 HTTP header。这些「非标准」传输路径需要用适配器注入 trace context
3. **高基数 attributes 的成本控制** — `storage.Get` 中把完整对象路径作为 attribute 会导致指标基数爆炸。需要限制为 `bucket` + `prefix` 级别

**建议的架构变更：**

```
Phase 1: 嵌套 span + 日志关联
  - Service/Storage/Repository 层创建子 span
  - slog 自动注入 trace_id/span_id
  - 修复当前 span 结束前无子调用的误解（实际 span 生命周期正确，但无嵌套）

Phase 2: 概率采样
  - 基于路由组的 head sampler
  - 100% 采样：search/chat/admin
  - 1-10% 采样：files GET/S3
  - 0%：healthz/metrics

Phase 3: SLO 告警
  - 基于 http.server.duration 直方图构建 SLO
  - multi-window multi-burn-rate 告警规则
  - Grafana trace 面板（trace 搜索 + span 瀑布图）
```

**对现有系统的影响：**

- span 创建是纯新增代码——不修改现有逻辑
- 日志关联需要修改 `slog.With` 的初始化和日志上下文传递
- SLO 告警规则是纯配置变更（YAML）
- 主要风险点：span attributes 的高基数导致指标存储膨胀——需要设计 attribute 白名单
- 采样决策需要 context 传递——确保所有下游组件使用相同的 `sampled` 标志

---

## 三、接口设计建议

### 3.1 模块级接口设计原则

| 原则 | 说明 | 应用场景 |
|------|------|---------|
| **可选接口（Optional Interface）** | 新方法用独立接口约定，不强制所有实现 | `CanCopy() bool` + `Copy(ctx, ...)` 作为可选接口；`Storage` 主接口不变 |
| **策略模式（Strategy）** | 核心逻辑 + 可替换策略 | `CopyStrategy` 选择器；`IdentityProvider` 联邦 |
| **组合优于装饰** | 中间件链用组合而非层层包装 | 安全中间件作为独立 handler wrapper，而非嵌入 chi 的 Use() 链 |
| **向后兼容的配置进化** | 新配置以增量方式添加，旧配置视为简写 | `EVENTS_WEBHOOK_URL` → `EVENTS_WEBHOOK_URLS` + route-level 配置 |

### 3.2 关键接口设计

**Storage 扩展接口：**

```go
// 可选实现——Storage 实现者可以只 implement 一部分
type CopyCapable interface {
    CanCopy(srcKey, dstKey string) bool          // 确定是否可执行服务端复制
    Copy(ctx, srcKey, dstKey string, opts) Info  // 零数据移动的服务端复制
}

type MoveCapable interface {
    Move(ctx, srcKey, dstKey string) Info   // ⚡ 原子操作（local rename(2)）
}

type CopyStream interface {
    CopyStream(ctx, srcKey, dstKey string, opts) Info  // 流式复制（当前实现）
}
```

**设计理由：** Go 的接口隔离原则——不是所有 Storage 后端都需要实现 `Copy`。Local 后端可以实现 `Move`（`os.Rename` 是原子的），S3 后端可以实现 `Copy`（调用 S3 CopyObject API）。`CopyStream` 作为兜底实现。

**身份联邦接口：**

```go
type IdentityProvider interface {
    Name() string                                       // "oidc" | "ldap" | "saml"
    Authenticate(ctx, credentials json.RawMessage) (Identity, error)
    Passive() bool                                      // true = 无需客户端交互（如 mTLS 证书认证）
}

type Identity struct {
    Subject    string
    Issuer     string
    TenantID   string
    Scopes     []string
    AuthMethod string           // "jwt" | "apikey" | "oidc" | "ldap" | "sigv4"
    SessionID  string           // 跨协议关联
    Attributes map[string]string
    ExpiresAt  time.Time
}
```

**设计理由：** `Passive()` 让 `Registry` 区分「需要客户端提供凭据的 IdP」（OIDC/LDAP）和「凭据已在传输层隐含的 IdP」（mTLS、SigV4）。`AuthMethod` 让审计日志可以追踪认证路径。

### 3.3 抽象层评估

| 是否需要新抽象层 | 判断 | 理由 |
|-----------------|------|------|
| **CopyStrategy** | ✅ 需要 | 不同后端/大小/加密状态的复制路径不同，策略模式可避免 if-else 丛林 |
| **WebhookRoute** | ✅ 需要 | 单 URL → 多路由的演进需要抽象层来封装过滤、限流、重试策略 |
| **IdentityProvider** | ✅ 需要 | 多 IdP 共存的统一接口 |
| **SLO 配置 DSL** | ❌ 不需要 | Prometheus 规则直接表达，无需自定义 DSL |

### 3.4 向后兼容策略

| 变更类型 | 兼容策略 | 示例 |
|---------|---------|------|
| Storage 接口新增方法 | 可选接口 + 降级 | 后端未实现 `CopyCapable` → `CopyStream` 兜底 |
| Config 格式变更 | 旧配置视为新配置的简写 | `EVENTS_WEBHOOK_URL` → 单路由规则 |
| DB schema 变更 | 新增列默认值 + 旧状态转换 | `status` 默认 `'retrying'` = 旧记录兼容 |
| API 新增端点 | 新端点不影响旧客户端 | `POST /admin/webhook-failures/{id}/retry` 纯新增 |
| Trace context 传播 | W3C Trace Context 标准 | 新增 `traceparent` header，不修改现有 header |

---

## 四、技术选型评估

### 4.1 是否需要引入新技术

| 方向 | 建议引入 | 理由 | 可选方案 |
|------|---------|------|---------|
| OIDC 联邦 | `github.com/coreos/go-oidc/v3` | OIDC 发现端点、JWKS 轮换、token 验证——自行实现易出错 | vs. stdlib `crypto/rsa` + `net/http`（可行但工作量大） |
| SLO 告警 | Prometheus rules（已有） | 不需要新工具，只需新增 YAML 规则 | 标准 Prometheus recording rules + alerting rules |
| Trace 采样 | OpenTelemetry 内置 Sampler | OTel SDK 已内建 `TraceIDRatioBased`、`ParentBased` 等 | 自定义 sampler vs OTel 内建 sampler |
| 死信队列 | SQLite/Postgres 表（已有） | 不需要消息队列中间件——事件量级预计 < 1000/s，表足够 | vs. RabbitMQ/NATS/Redis Stream（过度设计） |

**核心决策：不要引入消息队列中间件用于 webhook 死信。**

理由：
- 当前事件量级：对象 CRUD 事件 ≈ 请求 QPS —— 估计不超过 1000/s，SQLite/Postgres 表足以
- 消息队列增加运维复杂度：需要部署、监控、备份 RabbitMQ/NATS
- 事件交付 SLA 当前不需要 < 100ms 延迟（Webhook 本身的网络延迟是瓶颈）
- 但如果未来 EventBus 成为**跨实例事件通道**（不只是进程内 pub/sub），消息队列就变得必要

### 4.2 第三方依赖评估标准

| 维度 | 强制标准 | 可选标准 |
|------|---------|---------|
| **许可证** | Apache 2.0 / MIT / BSD | MPL 2.0 可考虑 |
| **Go 版本兼容性** | 支持 Go 1.25（当前版本） | — |
| **活跃度** | 最近 6 个月有提交 | — |
| **依赖深度** | 引入不超过 3 层传递依赖 | — |
| **版本稳定性** | ≥ v1.0 或经过广泛使用 | — |
| **安全审计** | 无已知 CVE | — |

**推荐库评估：**

| 库 | 用途 | 许可证 | 风险 | 建议 |
|----|------|--------|------|------|
| `github.com/coreos/go-oidc/v3` | OIDC 客户端 | Apache 2.0 | 维护良好、Red Hat 支持 | ✅ 接受 |
| `github.com/go-ldap/ldap/v3` | LDAP 客户端 | MIT | 维护良好 | ✅ 接受 |
| 无新 MQ 库 | — | — | — | ❌ 拒绝 |
| OTEL 内置（已有） | 采样 | Apache 2.0 | 已在 go.mod | ✅ 已存在 |

### 4.3 自建 vs 采购决策

| 能力 | 自建 | 采购/集成 | 决策 |
|------|------|----------|------|
| OIDC 认证 | 自行实现 JWT 验证 + OpenID Discovery | 集成 `go-oidc` | **集成** — 安全协议自行实现风险高 |
| LDAP 认证 | 直接 LDAP 查询 | 集成 `ldap/v3` | **集成** — LDAP 协议细节繁琐 |
| 安全头部生成 | 几个 `w.Header().Set()` | 无需采购 | **自建** — 十行代码 |
| Body 大小限制 | `http.MaxBytesReader` | 无需采购 | **自建** — Go 标准库已有 |
| Webhook 交付 | 全自建 | 可考虑第三方 webhook 网关（Svix、Knock） | **自建** — 核心基础设施，不可外包 |
| 分布式追踪后端 | Jaeger/Tempo（自建） | Grafana Cloud / Datadog | **Tempo（开源自建）** — 符合现有 OTel 架构 |

**关于 Webhook 网关的特别评估：**

第三方 Webhook 网关（Svix、Knock、SendGrid Inbound Parse）提供了开箱即用的可靠交付、重试、死信、分析面板。但自建的理由：

1. **架构耦合** — 事件交付是 aero-vault 的核心契约（用户通过 webhook 集成自己的系统），外包给第三方增加外部依赖
2. **数据主权** — webhook payload 包含租户数据，经过第三方网关可能违反合规要求
3. **成本控制** — 网关通常按投递次数计费，高事件量场景成本不可忽略
4. **但**：如果 aero-vault 需要管理 1000+ 租户的复杂 webhook 路由拓扑，第三方网关的成熟管理面可能是值得的

→ **建议：自建 v1（死信队列 + 重试 + 指标），v2 评估第三方网关。**

---

## 五、实施路线图

### 5.1 优先级总览

| 优先级 | 方向 | 时间 | 依赖 | 价值权重 |
|--------|------|------|------|---------|
| **P0** | 方向三 Phase 1：输入验证 + 安全 headers | 3-4 周 | 无 | 🛡️ 安全基线（合规前置） |
| **P1** | 方向一 Phase 1：Storage.Copy + 策略模式 | 4-6 周 | 无 | 🚀 性能（生产阻塞） |
| **P1** | 方向二 Phase 1：死信队列 + 交付指标 | 4-6 周 | 无 | 📡 可靠性（数据丢失） |
| **P2** | 方向四 Phase 1：嵌套 span + 日志关联 | 2-3 周 | OTel 已就绪 | 🔍 诊断（快速见效） |
| **P2** | 方向三 Phase 2：OIDC 联邦 | 6-8 周 | Phase 1 安全 | 🔐 合规（企业 SSO） |
| **P3** | 方向一 Phase 2：原子 MOVE + 分段 COPY | 6-8 周 | Phase 1 Copy | 🚀 性能（高级特性） |
| **P3** | 方向四 Phase 2+3：SLO + 采样 + Grafana trace | 4-6 周 | Phase 1 trace | 📊 运维（持续改进） |
| **P3** | 方向二 Phase 2：Webhook 路由过滤 + 密钥轮换 | 4-6 周 | Phase 1 死信 | 📡 功能完整度 |

### 5.2 阶段划分

**Phase A — 安全堡垒（Week 1-4）**

```
目标：建立纵深防御安全基线
交付：
  - middleware.MaxBodySize（端点级配置）
  - middleware.SecureHeaders（安全响应头）
  - middleware.SafeXMLDecoder（XML 解析保护）
  - SafeXMLDecoder 替换 s3compat 中 4 处 xml.NewDecoder 调用
  - 渗透测试基础报告
风险：低 — 纯新增中间件，不影响现有业务逻辑
里程碑：安全中间件链通过审核 + 渗透测试通过
```

**Phase B — 双线并行（Week 3-8）**

```
并行启动两条独立轨道：

轨道 B1（方向一）：Server-side Copy
  交付：
    - Storage 新增 CopyCapable/MoveCapable 可选接口
    - CopyStrategy 选择器
    - Local 后端 Copy 实现（file copy）
    - S3 后端 Copy 实现（S3 CopyObject API）
    - FileService.CopyObject 重构为策略调度器
    - 单元测试 + contract test（storage.ContractSuite 扩展）
  风险：中 — Storage 接口扩展影响所有后端，需要每个后端实现兜底

轨道 B2（方向二）：Webhook 死信
  交付：
    - webhook_failures 表 schema 变更（status/max_attempts/delivered_at）
    - MarkWebhookFailed 改为 set status='dead_letter'
    - RetryLoop 只轮询 status='retrying'
    - Admin API: POST /admin/webhook-failures/{id}/retry
    - Admin API: POST /admin/webhook-failures/{id}/discard
    - 5 个 webhook 交付指标（counter + histogram）
  风险：低 — 表变更向后兼容（status 默认 'retrying'）
```

**Phase C — 可观测性 + 联邦（Week 6-12）**

```
交付：
  嵌套 span 传播：
    - service/ 层 5 个关键方法创建子 span
    - storage/ 层 3 个关键方法创建子 span
    - repository/ 层 5 个关键方法创建子 span
    - slog 上下文关联 trace_id
    - W3C traceparent 跨协议传播
  
  概率采样：
    - 基于路由组的 head sampler
    - 配置化（AI_SEARCH_TRACE_SAMPLE_RATE=1.0 / FILES_TRACE_SAMPLE_RATE=0.01）
  
  OIDC 联邦（并行）：
    - IdentityProvider 接口
    - go-oidc 集成
    - RS256 JWT 验证 + iss 校验
    - tenant 映射
    - HS256 限制为管理员自签（通过 alg 检测）
  
  风险：中 — trace span 创建在性能路径上；OIDC 涉及安全
  里程碑：trace 瀑布图在 Jaeger 可见 + OIDC 登录流程通过内测
```

**Phase D — 高级特性（Week 10-16）**

```
交付：
  原子 MOVE（方向一 Phase 2）：
    - 三个阶段：元数据复制 → blob 移动（job）→ 源清除
    - 故障恢复：reconcile 处理标记行
    - WebDAV MOVE / REST rename 迁移
    
  SLO 告警（方向四 Phase 3）：
    - SLO 配置定义
    - Multi-window multi-burn-rate 告警规则
    - Grafana trace 面板
    
  Webhook 路由 + 密钥轮换（方向二 Phase 2）：
    - WebhookRoute 多路由
    - 事件过滤（event_type/tenant/prefix）
    - 双密钥轮换
    - per-URL 外发限流
    
  风险：中高 — 原子 MOVE 的三阶段故障恢复最复杂
  里程碑：全部四个方向的产品化完成
```

### 5.3 风险管理

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| **CopyStrategy 决策矩阵过于复杂** | 中 | 高（技术复杂度） | 先实现最简单的策略（同后端 → 服务端 Copy；跨后端 → ClientStream），后续再优化 |
| **OIDC 集成引入 JWT 验证漏洞** | 低 | 极高（安全） | 使用经过安全审计的 `go-oidc`，不自行实现 JWKS 验证；加测试验证 `alg` 篡改攻击 |
| **Trace span 创建增加请求延迟** | 中 | 低（性能） | OTel span 创建开销 < 1µs；性能测试验证 p50/p99 变化 |
| **Webhook 死信重新入队导致事件风暴** | 低 | 中（接收端过载） | per-URL rate limiter + 手动重放（非自动） |
| **安全 headers 破坏 Web UI 功能** | 低 | 中（用户体验） | 在开发环境用安全 headers 测试 Web UI 全部功能 |
| **原子 MOVE 三阶段文件丢失** | 中 | 极高（数据丢失） | 第一阶段先复制再删除（不是「移动」而是「复制+源清除的原子性保证」） |
| **Content-Type 校验破坏 S3 兼容性** | 中 | 高（兼容性回归） | S3 路由组白名单豁免 Content-Type 校验 |

### 5.4 不建议做的事（避免范围蔓延）

| 不建议 | 理由 | 替代路径 |
|--------|------|---------|
| 引入消息队列中间件 | 过度设计，运维复杂度 > 收益 | 当前表结构已足够 |
| 自建 OIDC 实现（不使用库） | 安全协议自行实现风险极高 | 使用 `go-oidc` + 封装薄层 |
| SLO 告警的自定义 DSL | 重复造轮子 | Prometheus rules 直接表达 |
| 完全的跨协议统一认证 | 四个协议的认证机制差异过大，统一接口层的抽象收益有限 | 统一 **认证上下文（AuthContext）** 而非统一认证机制 |
| 替换现有中间件链 | 现有链顺序经过生产验证 | 安全中间件插入链中 Auth 后 Handler 前 |

---

## 六、总结与关键建议

### 核心结论

aero-vault 的架构基础是扎实的——协议适配器模式、分层清晰的 DAG、opt-in 安全默认都是正确的架构决策。四个扩展方向识别出的主要问题是**架构演化滞后于使用场景的复杂性增长**：

| 架构阶段 | 当前状态 | 目标状态 |
|---------|---------|---------|
| **数据移动** | 面向对象存取（Put/Get） | 面向数据移动（Copy/Move） |
| **事件交付** | 尽力交付 | SLA 驱动的可靠交付 |
| **安全架构** | 协议级安全 | 系统级纵深防御 |
| **可观测性** | 指标监控 | 全链路分布式追踪 + SLO 驱动运维 |

### 关键推荐

1. **先做安全（Phase A）** — 输入验证中间件和安全 headers 成本最低、收益最高、影响范围最小。这是所有其他方向的安全基础
2. **Copy 和 Webhook 并行（Phase B）** — 两个方向无共享依赖，可各自独立推进。Copy 解决性能瓶颈，Webhook 解决可靠性缺口——两者都是生产环境阻塞项
3. **Trace 快速见效（Phase C 先行子任务）** — 嵌套 span 的代码成本极低（每个方法 2-3 行），但诊断价值极高。可以在 Phase B 进行的同时，并行 1 名工程师花 2 周完成
4. **OIDC 放在第三位（Phase C）** — 虽然重要的企业特性，但 LLM 成本估算显示引入身份联邦的工程风险较高（JWT 安全），需要前面的安全中间件基础
5. **不要引入消息队列** — 当前架构的事件量和延迟需求不支撑 MQ 的 ROI
6. **不要完全统一四种协议的认证** — 抽象层的收益有限，反而增加复杂度。用统一的 **AuthContext** 在各协议间传递已经认证的身份信息即可
