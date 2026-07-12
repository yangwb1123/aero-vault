以下为基于 `expansion-v100` 分析文档的架构评审与技术设计建议。

---

# 架构评审：aero-vault 扩展方向分析

> **评审范围：** 四个方向——服务端 COPY/MOVE、Webhook 交付基础设施、跨协议安全架构、分布式追踪与可观测性  
> **评审方法：** 架构债务识别、接口设计评估、扩展点权衡、实施路线建议

---

## 1. 架构评估

### 当前架构的明显优势

aero-vault 采用了**清晰的六边形架构**（Hexagonal Architecture），这是其最大的资产。

| 优势 | 具体表现 |
|------|---------|
| **端口-适配器分离** | `Storage` 接口和 `Repository` 接口是稳定的 port，local/S3/OSS/COS 以及 SQLite/Postgres 是可替换的 adapter |
| **协议适配器薄层** | REST/S3/WebDAV/MCP 都很薄，核心业务逻辑集中在 `internal/service.FileService`，不存在"handler 变胖"的反模式 |
| **Middleare 链顺序固定且显式** | `RequestID→CORS→Auth→Tenant→RateLimit→OTel→Recoverer→AccessLog`——不可变顺序减少了隐式依赖 |
| **EventBus 作为副作用解耦面** | Antivirus/Replication/Webhook 都通过事件驱动，核心 CRUD 路径不关心谁在听 |
| **Opt-in 安全默认** | AI/pgvector/Qdrant/events/cluster 全为 flag-gated，基线测试路径无外部依赖 |

### 架构债务与技术债

债务分为三类：**设计债务**（刻意为之但已过时的决策）、**实现债务**（当初合理但规模扩大后需要演进）、**合规债务**（安全/可观测性未达生产级标准）。

#### 设计债务

| 债务 | 位置 | 为什么当初合理 | 为什么现在痛 | 修复成本 |
|------|------|---------------|-------------|---------|
| **`Storage` 接口无 `Copy`/`Move`** | `internal/storage/storage.go` | YAGNI——MVP 只有单后端 local FS，Get+Put 已够用 | S3 后端服务端 COPY 能力浪费；跨后端副本与 Replication 重复实现；大对象 OOM | **中等**——新增接口方法，local/S3/OSS/COS 分别实现；Replication 可立即复用 |
| **Webhook 死信 = 标记成功** | `internal/events/webhook.go:160` | 简单——失败记录在表里可见，"没问题" | 事件**永久丢失**——运维无法重放死信，审计无法追溯失败事件 | **低**——增加 `status` 枚举字段，区分 `retrying`/`dead_letter`/`delivered` |
| **单根 span 不传播** | `internal/telemetry/http.go:23` | 快速集成——一行代码加完 OTel，能在 Grafana 看到请求量 | 无法分解各组件延迟——排障只能猜"可能是 storage 慢" | **低**——逐层加 `tracer.Start(ctx, ...)`，每层约 5 行代码 |
| **无端点级 body 大小限制** | 全部 handler 直接消费 `r.Body` | Apache/Nginx 反代通常会限制请求体大小 | 无反代环境（如 MCP stdio、内网直连）时 2GB XML 可导致 OOM | **低**——加 `MaxBytesReader` 中间件，按路由组配置阈值 |

#### 上下文缺失债务

| 债务 | 表现 | 后果 |
|------|------|------|
| **`TraceContext` 不跨进程传播** | 无 W3C `traceparent` header 传递 | 多副本请求无法关联，WebDAV/MCP 无 trace |
| **Authz 上下文不跨协议传递** | S3 SigV4 认证后切换到 REST 需重新认证，且无法证明"是同一个操作者" | 审计日志中同用户的不同协议操作没有关联 ID |
| **日志无 `trace_id` 字段** | `slog` 输出只有 `request_id`，没有 `trace_id`/`span_id` | 无法在日志聚合系统中按 trace 查询全链路日志 |

### 关键设计决策合理性评估

**决策：`FileService` 作为唯一核心控制器，所有协议必须通过它。**

→ **合理。** 这是整个架构健康度的根基。S3 COPY 的 Get→Put 路径之所以存在，正是因为 `copyObject` handler 没有跳过 `svc.Get`/`svc.Put` 去直接调 `store.Copy`——它**遵守了架构约束**穿过 service 层，只是 service 层和 storage 层都没有公开 Copy 能力。这不是 handler 的问题，是 storage 接口的问题。

**决策：事件去重靠幂等而非 Idempotency-Key 窗口。**

→ **合理但需演进。** 当前幂等设计（`Idempotency-Key` header + response cache）覆盖了客户端侧重试。但 **Webhook 接收端没有等价的保护**——接收端需要自己基于 Event ID 去重。对于关键事件（如 `object.deleted`），重复投递可能导致接收端误删数据。方向二的去重窗口提案是正确的补充。

**决策：认证机制按协议独立实现。**

→ **MVP 阶段合理，现已到演进拐点。** 四种协议四种认证方式在初期降低了实现复杂度。但随着租户数量增长和企业客户入场，**缺失统一的 authz context** 成为可观测性、审计、跨协议安全操作的最大结构性障碍。方向三的联邦身份提案是必要的，但需要谨慎评估 RS256 迁移对现有 HS256 管理员令牌的影响。

---

## 2. 扩展方向

基于文档的四个方向，我补充架构层面的深度判断。

### 方向 1：服务端 COPY/MOVE——从存储中介到策略驱动的数据移动

**为什么需要：**

这是**性能 + 成本 + 能力**的三重缺口。

| 维度 | 现状 | 目标 | 价值 |
|------|------|------|------|
| **性能** | 1GB 同后端 COPY = 2GB 服务器带宽 | S3 `CopyObject` API = 0 字节经过服务器 | 同后端 COPY 延迟从分钟级降到 <100ms |
| **成本** | S3 请求按 GET+PUT 双次计费 | S3 `CopyObject` 单次 API 调用 | 账单减半，大对象尤为显著 |
| **能力** | 无原子 MOVE、无跨后端 COPY、无进度追踪 | 原子两阶段 MOVE + 进度查询 + 续传 | 可用于 v1 数据迁移、跨区复制、存储分层 |

**核心挑战：**

1. **策略选择器的正确性边界**——必须确保 `ServerSide` 策略只在源和目标**同一后端实例**时启用。当 storage backend 是 S3 但 bucket A 在 us-east-1、bucket B 在 eu-west-1 时，`ServerSide` 策略会失败。策略选择器需要感知 endpoint/region 信息。

2. **加密路径的复杂性**——SSE 加密场景下，服务端 COPY 需要目标与源使用相同的加密密钥（或目标掌握源密钥的解密能力）。跨密钥的服务端 COPY 在 S3 中通过 `x-amz-server-side-encryption-customer-algorithm` 头传递密钥，但在 aero-vault 的 SSE envelope 加密模式下，源和目标可能使用不同的 key ID——这要求服务端具备**解密-重加密**的中间能力，此时 `ServerSide` 策略降级为 `ClientStream`。

3. **原子 MOVE 的"假原子"风险**——文档提出的三阶段 MOVE（源标记→后台 blob 移动→源清除）是**最终一致性**而非真正的 ACID 原子性。在两阶段之间，如果进程崩溃且存储后端损坏了已标记 pending 的对象，恢复逻辑可能丢失源数据。真的原子性**需要后端支持快照/重新命名（rename）** ，这对 local FS 可行（`os.Rename` 在同一文件系统内是原子的），但对 S3 不可能——S3 没有 rename 操作。这意味着原子 MOVE 的保障级别取决于后端能力。

**方案权衡：**

| 策略 | 正确性保证 | 后端依赖 | 复杂度 |
|------|-----------|---------|--------|
| **两阶段提交（事务性协调器）** | 强一致性（2PC） | 所有后端 + Repository 支持 prepare/commit | 高 |
| **Saga 模式（源标记 + 补偿 rollback）** | 最终一致性 + 补偿 | 无特殊要求 | 中 |
| **后端原生原子操作（local `os.Rename`）** | 真正原子 | 仅 local FS | 低 |

**建议：** 对 local 后端优先支持原生 rename（真正原子 MOVE），S3/OSS/COS 使用 Saga 模式（最终一致性 + 补偿）。对外暴露的统一 API 承诺"最终一致，失败时可查询"而非"原子 OCC"。在文档中明确标注不同后端的不同保证级别。

**对现有系统的影响：**
- `Storage` 接口新增 `Copy`/`Move` 方法（可选实现，`ErrUnsupported` 降级为 ClientStream）
- `FileService` 新增 `CopyObject`/`MoveObject` 方法，封装策略选择
- `Replication` worker 重构以复用 `Storage.Copy`
- WebDAV Rename 和 REST rename 接入 `svc.Move`
- Job 表新增 `JobMoveBlob` 类型

---

### 方向 2：Webhook 交付基础设施——从尽力投递到可治理的交付引擎

**为什么需要：**

Webhook 是 aero-vault 的**事件承诺**。当前实现的设计等价于"发一封不保证送达的邮件，没收到就标记为已送达"。这在生产环境中不可接受。

**核心挑战：**

1. **死信的生命周期管理**——死信队列比重试队列更难管理。运维没有 SLA 时，死信会无限累积。需要设计**死信保留策略**（TTL + 自动清理 + 归档导出）。

2. **去重窗口的粒度选择**——5 分钟固定窗口是一个合理的默认值，但不同事件类型对去重的敏感度不同：
   - `object.deleted`：窗口宜短（1 分钟以内），因为用户期望快速同步
   - `object.created`：窗口可长（5-15 分钟），因为创建本身幂等
   - `object.locked`：窗口宜长（30 分钟），因为锁操作重放的风险最高
   
   
   这意味着**去重窗口应该是一个 per-event-type 的可配置参数**，而非全局固定值。

3. **外发限流的反馈循环**——如果接收端返回 429 + `Retry-After`，发送端降低速率。但如果接收端返回大量 429，发送端的重试队列会累积，需要**从 retry 队列溢出的事件走 dead letter**，否则队列无限增长会耗尽内存。

**方案权衡：**

| 维度 | 选项 A：独立 Webhook 服务（sidecar） | 选项 B：嵌入现有进程 + 增强表结构 |
|------|--------------------------------------|-----------------------------------|
| **隔离性** | Webhook 压力不影响主进程 | 重试风暴可能影响核心 CRUD（goroutine/连接池） |
| **复杂度** | 需要独立部署、独立配置、独立监控 | 现有进程内增强，零运维变更 |
| **可靠性** | 进程级隔离，独立的健康检查和恢复 | 共享进程内存和 goroutine 池 |
| **数据访问** | 需要通过 RPC 或共享 DB 访问事件表 | 直接访问同一 Repository |

**建议：** Phase 1 采用选项 B（嵌入增强），因为改动小且能快速解决死信丢失的最严重问题。Phase 2 考虑选项 A（独立 Webhook Worker），当 webhook 流量达到主进程资源消耗 20% 以上的阈值时。当前 webhook 在 `RetryLoop` 中运行时 goroutine 数量很低（单 URL），嵌入模式足以应对。

**对现有系统的影响：**
- `webhook_failures` 表新增 `status` 字段 + `max_attempts` + `delivered_at`——需要新的 migration 对（SQLite + Postgres）
- Webhook 配置从单 URL 扩展为多路由——向后兼容：`EVENTS_WEBHOOK_URL` 对应单路由全事件
- `IncWebhookRetry` 补充为 `IncWebhookDelivery(status, url)` 覆盖成功/失败/死信
- admin API 新增 `POST /admin/webhook-failures/{id}/retry` 和 `/{id}/discard`

---

### 方向 3：跨协议安全架构——统一输入验证先行，身份联邦殿后

**为什么需要：**

这四个方向中，安全架构的影响面最广：**每个请求都经过中间件链，每个协议都涉及身份验证**。漏洞是全局性的。

**核心挑战之关键洞察：**

文档将输入验证和身份联邦放在同一个方向下，但**它们的紧急程度完全不同**：

| 子方向 | 风险等级 | 是否需要等到身份联邦完成？ | 建议 |
|--------|---------|--------------------------|------|
| **输入验证中间件**（body 大小、Content-Type、安全 headers） | 🔴 高——无中间件的端点直接暴露 | 不需要——完全独立 | **立即实施**（Phase 0） |
| **XML 安全解析**（LimitReader） | 🔴 高——6 个无限制 XML 端点可被 OOM 攻击 | 不需要——纯工具函数 | **立即实施**（Phase 0） |
| **安全响应头** | 🟡 中——主要是浏览器安全，API 客户端不受影响 | 不需要 | 跟随输入验证一起做 |
| **OIDC 联邦** | 🟢 低——当前无企业客户要求 | 有依赖性——需要 RS256 支持和 Issuer 校验 | Phase 2 |

**核心挑战：**

1. **输入验证中间件的路由组适配**——不是所有路由组都有相同的验证需求：
   - `/v1/*` REST API：要求 `Content-Type: application/json`
   - `/s3/*` S3 API：Content-Type 由客户端指定，不能强制
   - `/mcp` JSON-RPC：要求 `Content-Type: application/json`
   - `/ui` Web UI：要求 `Content-Type: text/html` 等浏览器标准
   
   
   这意味着中间件必须**感知路由组**，按组配置验证策略。这超出了简单中间件的范畴，需要类似 `routeGroupConfig` 的配置结构。

2. **HS256 到 RS256 的迁移**——当前 JWT 使用 HS256（对称签名），这意味着拥有 `AUTH_JWT_SECRET` 的人可以签发任意身份。在企业集成场景下，这违反了最小权限原则。迁移路径：
   - Phase 1：同时支持 HS256（现有管理员令牌）和 RS256（OIDC 令牌）
   - Phase 2：根据 `iss` 声明判断——`iss=admin` 走 HS256 校验，`iss=<oidc-issuer>` 走 RS256 校验
   - Phase 3：废弃 HS256 管理员令牌，全部迁移到 RS256
   
   
   **关键决策点：** 是否需要支持 HS256 降级模式？建议是**永不降级**——一旦 RS256 就绪，新生成的令牌全部使用 RS256，HS256 仅用于验证存量令牌。这样避免了签名算法降级攻击（alg=none 或 alg=HS256）。

3. **跨协议 authz context 的实现路径**——最简单的统一方式是在认证通过后将 `{principal, tenant, scopes, session_id}` 编码为一个 signed token（类似于 `X-Aero-Authz-Context`），在每个请求内传递。S3 的 SigV4 认证后可以在 middleware 中生成这个 context，后续的 storage/repository 调用通过 `ctx` 携带。**不需要修改 Storage 或 Repository 接口**——context 在 `context.Context` 中隐式传递。

**对现有系统的影响：**
- 新增 `middleware.MaxBodySize()` / `middleware.EnforceContentType()` / `middleware.SecureHeaders()`——纯新增，不修改现有中间件
- `s3compat/handler.go` 中 6 处 `xml.NewDecoder` 替换为 `safeXMLDecoder`——纯替换，不影响逻辑
- `auth.Registry` 新增 `IdentityProvider` 接口——新增实现，现有 key/JWT 继续工作
- `auth.jwt` 新增 RS256 验证能力——需要引入 `crypto/rsa` 或 `crypto/ecdsa` 验证

---

### 方向 4：分布式追踪——纠偏后的正确路径

**为什么需要：**

根据验证报告的纠正——span 生命周期是**正确的**（`defer span.End()` 在 handler 同步返回后触发），但 span **不向下传播**导致无嵌套结构。所以核心痛点不是"span 结束太早"，而是"span 没有孩子"。

**核心挑战：**

1. **采样策略的平衡**——100% 采样 AI 路由（`/v1/search`、`/v1/chat`）是正确的，因为这些请求延迟高、成本高、需要完整 trace。但**不要忽略 `/v1/files/*` 的 GET/PUT 请求**——存储问题是用户最常投诉的性能瓶颈（"下载文件慢"），如果采样率太低（如 1%），慢请求的小样本可能无法触发统计分析。

   **建议动态采样策略：**
   - 正常状态：GET 1%，PUT 10%，AI 100%，Admin 100%
   - 延迟偏离基线 > 2σ 时：自动提升采样率（GET → 10%，PUT → 50%）
   
   
   这需要 OTel `Sampler` 的自定义实现，或通过 Prometheus 告警触发配置变更。

2. **MCP stdio 模式的 trace 传播**——MCP 同时支持 HTTP 和 stdio 模式。HTTP 模式可以通过 `traceparent` header 传播 trace。**stdio 模式没有 HTTP header**——trace context 需要通过 JSON-RPC 请求 `params` 中的额外字段传递。这意味着 MCP 层需要一个**显式的 trace 参数提取逻辑**，当前 MCP 的 `listTools` 和 `callTool` switch 没有这个能力。

3. **SLO 告警的数据依赖性**——multi-window burn-rate 告警需要**可靠的延迟直方图数据**，而这又依赖于正确的 span 传播。在 Phase 1 嵌套 span 就绪前，SLO 告警仅能测量端到端 HTTP 延迟，无法分解到各组件。建议**先有稳定的 trace 数据，再构建 SLO 告警**，避免基于不完整数据的告警疲劳。

**对现有系统的影响：**
- `internal/telemetry/http.go`：无需修改 span 生命周期（已验证正确），但需将 trace context 存入 request context 供日志使用
- 每个 service/storage/repository 方法新增 `tracer.Start(ctx, ...)`——约 5 行/函数，影响面广但模式化
- `internal/middleware/middleware.go`：RequestID 中间件需要同时提取/注入 `traceparent` header
- `deploy/prometheus/alerts.yml`：新增 SLO burn-rate 告警组 + 概率采样配置
- `deploy/grafana/`：新增 trace 面板（基于 Tempo 或 Jaeger 数据源）

---

### 方向 5（补充）：多租户资源治理——从隔离配额到可观测的全局预算

**为什么需要：**

当前租户系统支持**静态配额**（存储上限、对象数量）和**日预算**（AI 费用上限）。但缺失以下关键能力：

| 缺口 | 表现 | 后果 |
|------|------|------|
| **跨协议速率共享** | REST 和 S3 使用不同的速率限制器，同一租户可绕过 REST 限流使用 S3 协议 | 速率限制无效 |
| **全局租户预算** | AI 日费用预算仅作用于 AI，存储费用无预算 | 租户可无限制使用存储直到配额 |
| **租户级可观测性** | 当前指标 `ai_cost_per_tenant` 是唯一的租户级指标 | 无法查看租户的 QPS、延迟、错误率趋势 |
| **预算耗尽通知** | 预算超限只返回 `code:BudgetExceeded`，无提前告警 | 用户突然被限制，无缓冲期 |

**核心挑战：**

1. **全局预算的计量口径一致**——存储费用和 AI 费用的计量单位不同（GB-month vs USD/token），需要统一的换算因子。这个换算因子应该是管理员可配置的（`STORAGE_COST_PER_GB_MONTH`），而非硬编码。

2. **预算耗尽的通知渠道**——当前仅通过 API 响应通知。需要支持**主动通知**（webhook 事件 `budget.exceeded`）和**提前告警**（75%/90%/100% 阈值）。

**对现有系统的影响：**
- `TenantRecord` 新增 `budget_notification_thresholds` 字段
- 新增 `TenantResourceUsage` 聚合（存储 + AI + 带宽）
- Rate limiter 新增租户级共享计数器（跨 REST/S3/WebDAV/MCP）
- Webhook 新增 `budget.alert` 事件类型

---

## 3. 接口设计建议

### Storage 接口扩展

文档提出的 `Storage.Copy` 是必要的，但我建议的接口比文档版本更保守：

```go
type Storage interface {
    // 现有方法不变
    
    // Copy 从 srcKey 复制到 dstKey。
    // 返回 ErrUnsupported 如果后端不支持服务端复制。
    // 调用方收到 ErrUnsupported 后应回退到 Get+Put。
    Copy(ctx context.Context, srcKey, dstKey string, opts CopyOptions) (ObjectInfo, error)
    
    // Move 移动（或重命名）对象。
    // 默认实现：Copy + Delete（非原子）。
    // 支持原子重命名的后端（local 同一文件系统）可覆盖为原子操作。
    Move(ctx context.Context, srcKey, dstKey string, opts MoveOptions) (ObjectInfo, error)
}

type CopyOptions struct {
    MetadataDirective string     // "COPY" | "REPLACE"（默认 "COPY"）
    TaggingDirective  string     // "COPY" | "REPLACE"（默认 "COPY"）
    StorageClass      string     // 目标存储类（空 = 与源相同）
    SourceVersionID   string     // 源版本 ID（空 = 最新版本）
}

type MoveOptions struct {
    MetadataDirective string
    TaggingDirective  string
}
```

**关键设计决策：**
- `Copy` 返回 `ErrUnsupported` 而非 panic——调用方可以降级
- `Move` 的默认实现是 `Copy + Delete`——后端可以通过覆盖实现原子操作
- 不是所有后端都需要实现 `Copy`——local FS 可能选择永远走 Get+Put（因为本地 I/O 便宜），只实现 `Move` 的 `os.Rename`

### 策略选择器的位置

文档建议在 `Storage` 层之上放一个 `CopyStrategy` 选择器。我更倾向于将策略选择放在 **`FileService` 层**：

```
FileService.CopyObject()
    → 检查源/目标后端是否相同
    → 相同且后端支持 Copy → 调 Storage.Copy()
    → 不同或不支持 → 调 Storage.Get() + Storage.Put()
    → 记录 telemetry（用了哪种策略）
```

理由：
- `FileService` 已经持有 `store` 和 `repo`，可以查询元数据得知源/目标后端信息
- `Storage` 接口不应该关心副本策略，它只关心"是否支持服务端复制"
- 策略选择需要业务逻辑（是否加密、是否跨 region），这是 service 层的职责

### Webhook 配置接口

向后兼容是核心约束。当前配置是单字符串 `EVENTS_WEBHOOK_URL`。扩展为多路由时：

```
EVENTS_WEBHOOK_URLS=prod=https://hook.example.com/events,audit=https://hook.example.com/audit
EVENTS_WEBHOOK_ROUTE_prod_FILTER_EVENT_TYPES=created,deleted
EVENTS_WEBHOOK_ROUTE_audit_FILTER_EVENT_TYPES=object.locked,acl.changed
```

**向后兼容保证：**
- `EVENTS_WEBHOOK_URL` 存在时 = 单路由，接收全部事件，等效于 `EVENTS_WEBHOOK_URLS=default=<value>`
- `EVENTS_WEBHOOK_URL` 和 `EVENTS_WEBHOOK_URLS` 同时存在时，前者被忽略（或合并为一条 "legacy" 路由）
- 老配置启动日志 warning："`EVENTS_WEBHOOK_URL` is deprecated, use `EVENTS_WEBHOOK_URLS` for multi-route support"

**接口抽象：**

```go
type WebhookRoute struct {
    Name        string
    URL         string
    Filter      *EventFilter       // nil = accept all
    RateLimit   rate.Limit         // 0 = unlimited
    MaxAttempts int                // 0 = default (10)
    Timeout     time.Duration      // 0 = default (5s)
    RetryPolicy RetryPolicy        // exponential backoff params
}

type EventFilter struct {
    EventMatch  []string           // exact match, empty = all
    TenantMatch []string           // empty = all
    BucketMatch []string           // empty = all
}
```

### Identity Provider 接口

```go
type IdentityProvider interface {
    // Name returns provider identifier for metrics/logging
    Name() string
    
    // Authenticate validates credentials and returns identity.
    // Returns ErrAuthenticationFailed on failure.
    Authenticate(ctx context.Context, credentials Credentials) (Identity, error)
    
    // Configured returns true if this provider is ready to use
    Configured() bool
}

type Identity struct {
    Subject    string
    Issuer     string
    TenantID   string
    Scopes     []string
    Attributes map[string]string
    ExpiresAt  time.Time
}

type Credentials struct {
    Token      string            // JWT, bearer token
    AccessKey  string            // SigV4 access key
    SecretKey  string            // SigV4 secret key (S3 only)
    APIKey     string            // X-Api-Key
}
```

**关键设计决策：**
- `Authenticate` 返回 `Identity` 而非 `bool`——避免后续再查一次数据库来获取 tenant/role 映射
- `Configured` 方法让调度层可以跳过未配置的 provider——不影响启动，某 IdP 挂了其他还能工作

---

## 4. 技术选型

### 是否需要引入新的技术栈

| 方向 | 可能需要引入 | 判断 |
|------|-------------|------|
| **服务端 COPY/MOVE** | 无 | 纯 Go 标准库 + 现有 Storage 接口扩展，零新依赖 |
| **Webhook** | 无 | 纯 Go 标准库 + SQLite/Postgres 已有表结构 |
| **安全架构** | 可能需要：`coreos/go-oidc` 或 stdlib `crypto/x509` | OIDC 客户端库减轻 JWT 验证代码量，但 stdlib `crypto/rsa` 已能 RSA 验签 |
| **分布式追踪** | 建议：**Grafana Tempo** 或 **Jaeger** 作为 trace 后端 | OTel SDK 已引入（`go.opentelemetry.io/otel`），但需要 trace 存储和查询后端 |
| **多租户资源治理** | 无 | 纯增强现有 `TenantRecord` 和 `RateLimiter` |

### OIDC 依赖评估

如果需要 OIDC 联邦，有两个选项：

| 选项 | 库 | 优势 | 风险 |
|------|---|------|------|
| **A：使用 `coreos/go-oidc` v3** | 社区标准，广泛使用 | 自动处理 JWKS 轮换、`iss` 校验、`aud` 校验 | 引入外部依赖（Apache 2.0 许可，兼容）；需要监视上游更新 |
| **B：纯 stdlib 实现** | `crypto/rsa` + `encoding/json` | 零外部依赖，可控性最高 | 需要手动实现 JWKS 获取/缓存/轮换、`iss`/`aud`/`exp`/`nbf` 校验——约 300 行代码 |

**建议：** 选项 A（`coreos/go-oidc`）。JWT 验证的边界情况非常多（`alg=none` 攻击、JWKS 格式兼容、密钥轮换时序），社区库已经处理好了这些。选项 B 的 300 行代码只是为了"零依赖"的教条，不值得。

### Tracing 后端选型

文档没有深入后端选型。当前 OTel 导出器已配置 OTLP，所以以下三者都兼容：

| 后端 | 部署复杂度 | 存储成本 | 查询能力 | 与 Grafana 集成 |
|------|-----------|---------|---------|----------------|
| **Grafana Tempo** | 中（需 S3/GCS 后端） | 低（对象存储） | 中（TraceQL） | ✅ 原生 |
| **Jaeger** | 低（AllInOne Docker） | 中（ES/Badger） | 高（UI + API） | ✅ 通过 Jaeger datasource |
| **SigNoz** | 中（Kubernetes 部署） | 中（ClickHouse） | 高 | ✅ |

**建议：** 如果用户已部署 Prometheus + Grafana，Grafana Tempo 是最自然的扩展——trace ID 可以直接从 metrics 面板跳转到 trace 详情。Jaeger 更适合已有 Jaeger 运维经验的团队。建议在文档中列出这三个选项而非固化为一个。

### Sampler 配置

当前 OTel setup 无显式 sampler（默认 always-on），在生产环境中会导致 trace 数据量爆炸。

| Sampler | 适用场景 | 优势 | 风险 |
|---------|---------|------|------|
| **`sdktrace.AlwaysSample`** | 开发环境 | 100% 可见 | 生产环境数据量过大 |
| **`sdktrace.TraceIDRatioBased(0.01)`** | 生产环境低频路由 | 简单，低开销 | 无法按端点差异化 |
| **`sdktrace.ParentBased(sdktrace.TraceIDRatioBased(0.05))`** | 生产环境 | 继承父采样决策，全链路一致 | 仍无法差异化端点 |
| **自定义 Sampler**（按 URL path/HTTP method 决定） | 所有场景 | 灵活，精确控制 | 需要额外代码维护 |

**建议：** 自定义 Sampler，按路由组配置采样率。这是约 50 行 Go 代码，但为 Phase 2 的 SLO 告警提供精确的数据基础。

---

## 5. 实施路线图

### 优先级评估矩阵

按 **风险降低度 × 客户可见度 × 实现成本** 三维评分（1-5）：

| 方向 | 风险降低 | 客户可见 | 实现成本 | 综合优先级 |
|------|---------|---------|---------|-----------|
| **输入验证中间件 + XML 安全** | 5（OOM 是直接安全风险） | 3（不可见但预防事故） | 1（2 周） | **P0** |
| **Webhook 死信队列** | 4（数据丢失是 service 问题） | 2（运维可见） | 2（3-4 周） | **P0** |
| **服务端 COPY/MOVE（本地 backend）** | 2（不影响现有功能） | 5（大对象 COPY 性能提升立竿见影） | 3（4-6 周） | **P1** |
| **嵌套 span 传播** | 3（排障效率） | 3（运维团队可见） | 2（3-4 周） | **P1** |
| **Webhook 多路由 + 过滤** | 2（不影响现有单 URL） | 3（高级用户诉求） | 3（4 周） | **P1** |
| **OIDC 联邦** | 3（企业合规） | 4（企业客户入门条件） | 5（6-8 周） | **P2** |
| **SLO 告警 + 概率采样** | 3（运维成熟度） | 2（仅运维可见） | 3（4 周） | **P2** |
| **多租户资源治理** | 2（现有配额已覆盖基本场景） | 3（高级用户诉求） | 4（6 周） | **P2** |
| **S3 服务端 COPY** | 1（本地已实现 ClientStream） | 4（S3 兼容客户期待） | 2（2-3 周） | **P1** |

### 阶段划分

```
Phase 0（Sprint 1-2）：安全止血 + 死信修复
├── P0 输入验证中间件（MaxBodySize + SecureHeaders + Content-Type enforcement）
├── P0 XML 安全解析（6 端点 LimitReader 防护）
├── P0 Webhook 死信队列（status 字段 + /admin/retry 端点）
└── ✅ 交付物：中间件链扩展 + Webhook 不死信

Phase 1（Sprint 3-6）：可观测性 + 数据移动
├── P1 嵌套 span 传播（Service → Storage → Repository）
├── P1 结构化日志关联 trace_id
├── P1 本地 Copy/Move（Storage.Copy + FileService.CopyObject + os.Rename MOVE）
├── P1 S3 服务端 COPY（调用 S3 CopyObject API）
└── ✅ 交付物：全链路 trace + 同后端 COPY 零数据移动

Phase 2（Sprint 7-10）：基础设施增强
├── P1 Webhook 多路由 + 事件过滤 + 去重窗口
├── P1 Webhook 交付指标（延迟直方图 + 成功率）
├── P2 概率采样（自定义 Sampler 按路由组）
├── P2 SLO burn-rate 告警（依赖 Phase 1 trace 数据）
└── ✅ 交付物：可治理的 Webhook + SLO 驱动告警

Phase 3（Sprint 11-16）：企业就绪
├── P2 OIDC 联邦（coreos/go-oidc + RS256 验证）
├── P2 HS256→RS256 迁移路径（双算法支持 1-2 个版本）
├── P2 跨协议 authz context（session_id 穿透所有协议）
├── P2 多租户资源治理（全局预算 + 跨协议限流）
└── ✅ 交付物：企业 IdP 集成 + 统一安全上下文
```

### 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| **S3 后端 CopyObject 实现差异** | 中 | 高——跨 provider S3 兼容问题 | 为每个 backend 配 `CanCopy()` 方法；不支持的返回 `ErrUnsupported`；contract test 覆盖 `Storage.Copy` |
| **嵌套 span 增加 p99 延迟** | 低 | 中——`tracer.Start` 本身有开销（纳秒级） | 基准测试验证；如果超标，使用 `trace.WithSampler(sdktrace.NeverSample())` 在生产环境默认关闭，按路由组开启 |
| **OIDC 单点故障** | 中 | 高——IdP 不可用时所有 OIDC 用户无法登录 | 必须实现降级路径：OIDC 不可用时，管理员可通过本地管理员 API key 登录并关闭 OIDC、切换到本地认证；`IdentityProvider.Authenticate` 返回明确错误，Registry 轮询下一个 provider |
| **HS256→RS256 迁移中断** | 低 | 高——错误的迁移脚本可能导致所有 JWT 失效 | 双算法支持至少跨越 2 个版本；迁移期间所有 RS256 令牌同时被 HS256 和 RS256 验证器检查；HS256 的废除通过 `AUTH_JWT_DISABLE_HS256=true` 环境变量触发（默认 false） |
| **Webhook 去重窗口重叠** | 低 | 中——相同事件在去重窗口外被重发，接收端重复处理 | 去重窗口使用 **事件 payload hash**（而非 event ID），因为具有相同 payload 的不同事件可能是重放；接收端仍应自行幂等处理 |

### 不建议做的事

| 做法 | 为什么不在路线图中 | 替代方案 |
|------|-------------------|---------|
| **Webhook 独立部署 sidecar** | Phase 0-2 的嵌入增强已足够；sidecar 增加运维复杂度且收益不大 | 独立 webhook worker 作为 Phase 4 选项（仅当 webhook 流量 > 主进程 20% 资源时） |
| **SAML 身份联邦** | SAML 协议复杂且 OIDC 已覆盖 90% 企业场景 | OIDC 优先；SAML 通过反向代理（如 nginx-saml）桥接 |
| **跨后端原子 MOVE** | 跨后端原子操作在分布式系统中无法真正实现（网络分区场景） | 最终一致性 + 补偿操作 + 清晰承诺 |
| **Event Sourcing + 不可变事件日志** | v94 已覆盖此方向且与 webhook 交付是不同的抽象层 | 先完成 webhook 交付可靠性，再考虑事件存储 |
| **gRPC 协议适配器** | 当前 4 个协议已覆盖用户场景，无明确需求 | 如需，可通过 MCP 提供高性能 RPC |

---

## 总结

`expansion-v100` 文档的四个方向准确识别了 aero-vault 的架构演进关键路径。从架构视角看：

1. **最大的架构债务在 Webhook 交付**——死信丢数据是设计缺陷而非性能问题，修复成本极低，安全收益极大。应作为 **Phase 0 最高优先级**。

2. **最平滑的扩展是 `Storage.Copy`**——接口扩展背靠背可向后兼容，local 和 S3 后端的实现都在各后端内部，不影响现有 Get+Put 路径。`FileService` 层的策略选择器是关键设计决策。

3. **安全架构需要分层推进**——输入验证中间件与 OIDC 联邦的风险级别相差 3 倍。先止血（body 限制、XML 安全、安全 headers），再建设（OIDC、RS256、跨协议 authz context）。两者不应在同一 Sprint。

4. **分布式追踪的瓶颈不在基础设施，在文化**——OTel SDK 已就绪，Tempo/Jaeger 部署简单。真正的挑战是 **每个开发者在写新函数时是否记得加 `tracer.Start(ctx, ...)`**。这需要通过静态检查（lint rule）或代码生成（aspect-oriented middleware wrapping）来保证。推荐前者，因为更轻量且易维护。

5. **多租户资源治理是未被文档覆盖的第五方向**——它不是安全或可观测性的子集，是对 tenant 模型的完整补充。建议在 Phase 2 之后纳入 ROADMAP。
