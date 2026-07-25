# AeroVault 高价值扩展方向 v47 — 生产就绪度系统性盲区

> **分析范围：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 全部 23 个子包，46K+ `.go` 代码 + `sdk/*` 三套客户端 + `deploy/*` + 48 对迁移文件 + `Makefile` + 全部 `docs/requirements/` 已有 46 份分析文档）
>
> **分析视角：** 资深架构师 / 产品经理 — 聚焦此前 **46 期 expansion 分析（累计 250+ 方向，~500,000+ 字分析文本）+ `docs/ROADMAP.md`（10 大方向）+ `docs/CHANGELOG.md` + `docs/TODO.md` + `docs/adr/DECISIONS.md`** 中从未作为独立架构方向分析过的生产就绪度盲区
>
> **分析日期：** 2026-07-10
>
> **去重验证：** 对 `docs/requirements/` 下全部 46 份既有分析文档 + `ROADMAP.md` + `CHANGELOG.md` + `TODO.md` + `adr/DECISIONS.md` + `extensions*.md` 进行穷尽式术语 `grep` 验证。每个方向在既有文档中 **零实质性独立架构分析**（表格一行过路引用、举例提及、单一子点均不构成实质性分析）。

---

## 前言

此前 46 期 expansion 分析覆盖了 250+ 方向，从 AI/RAG 管线到 S3 协议实现纵深、从存储后端到认证授权、从多租户到合规、从可观测性到工程基础设施、从产品成熟度到开发者体验。最新几期（v44 系统性架构缺口、v45 交叉架构缺口、v46 产品成熟度）已经触及了大量此前遗漏的连接层和产品层问题。

然而，经过对代码库最后一遍穷举扫描 + 对 46 份分析文档的去重验证，以下 **5 个方向** 依然未被任何一期作为独立架构方向分析。它们的共同特征是：**不是"新功能"的添加，也不是已有功能的"细化"，而是确保已有生产系统可被信任、可被运维、可被依赖的基础保障架构。** 从产品角度看，这些是决定"是否敢把业务数据放到 AeroVault 上"的信任锚点。

```
功能维度（前 43 期）：          ❌ 不支持 → ✅ 已实现
执行层维度（v42/v43/v44）：     ✅ 有 CRUD → ✅ 运行时行为完整
系统性交叉维度（v45/v46）：     ✅ 各功能独立正确 → ⚠️ 功能交叉面一致
生产信任维度（本期 v47）：      ✅ 功能完整+交叉一致 → ❌ 缺乏可验证的生产保障
```

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心矛盾 | 锚定代码 | 46 期覆盖 |
|---|------|------|--------|---------|---------|-----------|
| 1 | **多租户数据隔离验证框架** | 安全/工程质量 | **P0 — 信任基线** — 多租户是核心架构属性，但没有任何自动化测试验证租户 A 无法通过任何协议/API/边界场景访问租户 B 的数据 | `internal/auth/auth_middleware.go`（Tenant 提取）；所有 handler 中 `tenantFromCtx` 的使用；无跨租户渗透测试 | ❌ **零覆盖** |
| 2 | **Webhook 密钥轮换与交付基础设施硬化** | 安全/可靠性 | **P1** — Webhook 签名密钥在进程生命周期内固定，无法轮换；死信与成功记录共用同一状态字段，运维无法区分 | `internal/events/webhook.go:38`（`WithSecret` 一次性注入）；`:208-224`（死信复用 `MarkWebhookSucceeded`） | ❌ **零覆盖** |
| 3 | **对象存储边界治理：大小限制、容量感知与流式完整性** | 可靠性/运维 | **P1** — 系统无最大对象大小限制；无存储后端容量告警；大文件上传过程中无流式完整性校验 | `internal/service/file_crud.go` Put 路径（无大小校验）；`internal/storage/storage.go` Storage 接口（无容量/健康方法） | ❌ **零覆盖** |
| 4 | **后台工作负载统一健康模型与自治愈** | 可靠性/运维 | **P1** — 15+ 后台 goroutine 无统一健康报告、无死锁检测、无自动重启机制；`/readyz` 不反映后台工作负载状态 | `cmd/server/main.go`（15+ 后台 goroutine）；`/readyz` 仅检查 DB + Storage；各 worker 独立 ctx | ❌ **零覆盖** |
| 5 | **租户数据可移植性与自助导出** | 产品/合规 | **P2** — 租户没有自助导出数据的能力；没有标准化数据格式（JSON Lines + 对象文件）的导出管道；vendor lock-in 担忧阻碍企业采纳 | `internal/service/file_crud.go` Get/List；`internal/snapshot/snapshot.go`（仅 SQLite 全局快照）；`internal/cli/`（无 export 命令） | ⚠️ v27/v35 各一行提及"数据可移植性"，**零实质性架构分析** |

---

## 方向一：多租户数据隔离验证框架（Multi-Tenant Data Isolation Verification Framework）

### 现状

当前多租户实现的基本路径是清晰的：

```go
// internal/auth/auth_middleware.go — Tenant 提取
func Tenant(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        tenant := r.Header.Get("X-Aero-Tenant")
        if tenant == "" {
            // 从 JWT claims 或 API Key 绑定的 tenant 提取
        }
        ctx := context.WithValue(r.Context(), tenantKey, tenant)
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}
```

所有 handler 通过 `mw.TenantFrom(ctx)` 获取租户，下游 `FileService` 和 `repository` 的所有查询都带 `WHERE tenant=?` 参数。

**但是，没有任何测试验证这个隔离机制的完整性：**

| 攻击面 | 风险描述 | 代码证据 |
|--------|---------|---------|
| **REST API 全路径枚举** | 是否能通过构造 `tenant=B` 来访问 GET `/v1/files/{key}` 中属于 tenant B 的对象 | `internal/api/rest/handler.go` 中 `h.svc.Get(ctx, mw.TenantFrom(ctx), ...)` — 但无跨租户测试 |
| **S3 协议路径** | S3 URL 路径中 tenant 如何嵌入？当前 `/s3/{bucket}/{key}` → tenant 从 header 提取，但 SigV4 验签时的租户身份与 header 是否一致？ | `internal/api/s3compat/handler.go` 的租户提取路径 |
| **WebDAV 路径** | WebDAV 的 PROPFIND 是否限制在请求租户范围内？ | `internal/api/webdav/dav.go` 中 `tenantFromCtx` 的使用 |
| **MCP 跨租户漏洞** | v45 已指出 `readResource` 从 URI 而非上下文获取租户 | `internal/mcp/server.go:161` `s.svc.Get(ctx, parts[0], ...)` |
| **搜索/聊天 API** | 语义搜索是否可能返回租户 B 的 chunk？ | `internal/ai/search.go` 的 `WHERE tenant=?` 参数化 |
| **Admin API** | 管理员 A（属于 tenant A）的 API Key 能否操作 tenant B？ | `internal/api/rest/admin.go` 的 scope 校验 |
| **SSE 事件流** | 订阅 SSE 后是否收到跨租户事件？ | `internal/api/rest/sse.go` 的事件过滤 |
| **预签名 URL** | 为 tenant A 生成的预签名 URL，能否被 tenant B 重用？ | `internal/service/file_features.go` |
| **审计日志** | 审计日志记录是否包含足够信息来追溯跨租户访问？ | `internal/repository/audit.go` |
| **租户 ID 注入** | 租户 ID 是否可能通过 HTTP header 注入？如果 `X-Aero-Tenant` 被多个 proxy 重写？ | `middleware.Tenant` 的实现 |
| **空/默认租户处理** | 当请求无租户时回退到 `"default"` — 不同 protocol 的空租户行为是否一致？ | `service.DefaultTenant = "default"` |

**所有这些检查点目前零测试覆盖。**

### 为什么需要

1. **多租户是核心架构属性，不是功能。** 如果说"这是一个多租户系统"，那么必须能证明租户隔离在所有协议、所有 API、所有边界场景下都成立。没有验证框架，这只是一个未经证明的声明。

2. **错误可能在任何一个组件中被引入。** 如果未来某次重构在 `search.go` 中漏掉了 `WHERE tenant=?`，或者新 handler 忘了调用 `TenantFrom`，就产生了数据泄露漏洞。没有隔离验证框架，CI 不会捕获这类回归。

3. **合规审计需要可证明的隔离。** SOC2、ISO 27001、GDPR 等合规框架要求"逻辑访问控制已实施并验证"。没有自动化验证框架，合规审计只能依赖人工 code review。

4. **跨协议攻击面增大。** 四个协议意味着四条不同的租户提取路径。如果一个协议（如 MCP）的租户提取逻辑有 bug，数据通过其他协议泄露。单一协议验证不够。

### 缺失的能力

1. **系统化跨租户渗透测试套件** — 一个参数化的、覆盖所有协议和 API 的测试套件：

   ```go
   // 伪代码：隔离测试套件的核心模式
   type isolationTestCase struct {
       name     string
       protocol string          // "rest" | "s3" | "webdav" | "mcp"
       method   string          // "GET" | "PUT" | "DELETE" | "SEARCH" | ...
       path     string          // 模板化路径，含 tenant 参数
       headers  map[string]string // 含 auth header，绑定 tenant A
       expectForbidden bool     // true = 期望 403/404，false = 期望 200
   }

   func TestTenantIsolation(t *testing.T) {
       // 场景：tenant A 创建了对象 X，tenant B 尝试通过所有协议访问
       // 预期：每次访问都返回 403/404（不应暴露"对象是否存在"的信息）
   }
   ```

2. **租户上下文注入验证** — 在测试模式下注入一个"说谎"的租户 header，验证每个 handler 是否使用经过 auth middleware 验证的租户（而非来自请求体/URL 的未经验证值）：

   ```go
   func TestTenantFromMiddlewareNotFromRequest(t *testing.T) {
       // PUT /v1/files/key 请求中 body 包含 {"tenant": "other-tenant"}
       // 验证 handler 使用 middleware 设置的 tenant，而非 body 中的值
       // 这个测试需要验证的文件：handler.go、s3compat/handler.go、dav.go、mcp/server.go
   }
   ```

3. **404 vs 403 一致性（模糊测试）** — 当 tenant B 请求 tenant A 的对象时，系统必须一致返回 `404 Not Found`（而非 `403 Forbidden`），避免暴露对象存在性。模糊测试验证所有协议的一致性。

4. **搜索结果隔离验证** — 为 tenant A 索引一批文档，用 tenant B 的 API Key 搜索关键词，验证返回空结果。

5. **SSE 事件隔离验证** — 使用 tenant A 的凭据订阅 SSE，对 tenant B 执行写操作，验证 tenant A 的 SSE 流中不出现 tenant B 的事件。

6. **预签名 URL 隔离验证** — 为 tenant A 生成预签名 URL，用 tenant B 的凭据（或匿名）请求该 URL，验证失败。

7. **CI 集成** — 隔离测试套件作为 `make test-isolation` 运行，纳入 CI gate：

   ```makefile
   test-isolation:
       go test -tags=isolation -count=1 -timeout 120s ./internal/integration/... -run TestTenantIsolation
   ```

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **空 tenant（默认 tenant）** | 验证空 tenant 请求不会访问到非默认 tenant 的数据 |
| **租户 ID 含特殊字符** | 租户 ID 注入测试（SQL 注入、路径遍历、Unicode 等价） |
| **Admin API Key（tenant="*"）** | operator key 有全租户访问权限，验证 scope 限制正确 |
| **并发隔离** | 并发请求下租户上下文不会串（goroutine 安全） |
| **事件重放** | 对历史 SSE 事件的 replay 是否会泄露跨租户信息 |

---

## 方向二：Webhook 密钥轮换与交付基础设施硬化（Webhook Key Rotation & Delivery Assurance）

### 现状

当前 Webhook 实现（`internal/events/webhook.go`）有一个根本性的运维问题：

```go
// Webhook 签名密钥在进程生命周期内固定
func (w *Webhook) WithSecret(secret string) *Webhook {
    w.secret = []byte(secret)
    return w
}

// 签名在 deliver 和 retryOne 中分别计算
// 如果密钥在 retry 期间轮换，之前的签名消息和当前签名消息使用不同密钥
```

**具体问题：**

| 问题 | 代码证据 | 影响 |
|------|---------|------|
| **密钥无版本** | `w.secret = []byte(secret)` 单个 `[]byte`，无 ID/版本 | 轮换时无法判断用哪个密钥签名；消费者无法区分新旧密钥 |
| **死信混淆** | `retryOne` 中 10 次重试失败后调用 `MarkWebhookSucceeded` 标记为"成功" | 运维无法区分"已成功送达"和"永久失败已终止重试" |
| **无重试元数据** | 重试请求仅在 header 中带 `X-Aero-Retry-Attempt`，无原始事件时间戳、无尝试次数历史 | 消费者无法判断消息的新旧、是否已经处理过 |
| **无法暂停/恢复** | 无 API 暂停特定 URL 的 webhook 投递 | 目标服务维护期间无法优雅暂停 |
| **无速率限制** | `deliver` 直接 `for _, u := range w.urls { w.postOne(...) }` 并行投递 | 如果多个 URL 中有一个响应慢，会阻塞其他 URL 的投递 |
| **重试周期固定** | `RetryLoop` 每 15 秒轮询一次，不受重试队列深度影响 | 低负载时浪费资源，高负载时轮询间隔不够 |

### 为什么需要

1. **密钥轮换是安全基线操作。** 任何生产系统都应该定期轮换签名密钥。当前固定密钥意味着：如果密钥泄露，需要重启整个服务才能更新——并且所有正在重试中的消息都会因为签名验证失败而被消费者拒绝。

2. **死信混淆是运维盲区。** 将"永久失败"标记为"已成功"意味着运维人员在 `ListWebhookFailures` 中看到的状态是不可靠的。他们无法区分"这个消息已经被成功处理了"和"这个消息已经放弃重试了但它还是失败的"。

3. **多租户场景下 URL 管理复杂。** 每个租户可能有独立的 webhook URL。当前将所有 URL 平铺在 `EVENTS_WEBHOOK_URL` 逗号分隔列表中，没有租户级隔离。

### 缺失的能力

1. **多密钥环与版本化签名：**

   ```go
   type WebhookKey struct {
       ID      string    // 密钥唯一标识（如 "whk-20260701"）
       Secret  []byte
       Created time.Time
       Active  bool      // true = 用于签名新事件，false = 仅用于验证旧签名
   }

   type Webhook struct {
       keys    []WebhookKey  // 支持多个密钥同时有效
       activeKey *WebhookKey // 当前用于签名的密钥
       // ...
   }
   ```

   - 新事件用 `activeKey` 签名
   - 响应头中加入 `X-Aero-Signature-Key-Id` 告知消费者使用哪个密钥验证
   - 重试事件保留原始密钥 ID，即使 `activeKey` 已轮换也能用旧密钥验证

2. **死信状态字段：** 在 `webhook_failures` 表中增加 `status` 字段（`pending` / `retrying` / `dead` / `succeeded`），不再复用 `succeeded` 标记永久失败。

3. **Webhook URL 租户绑定：** 支持 `EVENTS_WEBHOOK_{TENANT}_URL` 命名约定，或将 URL 映射存储在存储库中，支持每个租户独立配置 webhook。

4. **Webhook 管理 API：**
   - `POST /v1/admin/webhooks` — 注册 webhook URL（含租户绑定、密钥配置）
   - `GET /v1/admin/webhooks` — 列出 webhook
   - `PUT /v1/admin/webhooks/{id}/pause` — 暂停投递
   - `POST /v1/admin/webhooks/{id}/rotate-key` — 触发密钥轮换
   - `GET /v1/admin/webhooks/{id}/failures` — 查看失败记录

5. **自适应重试节奏：** 重试循环根据 `webhook_failures` 中 `pending` 数量动态调整轮询间隔——无待重试时退避到 60 秒，有大量待重试时加速到 5 秒。

6. **事件重放 API：** `POST /v1/admin/webhooks/{id}/replay?from=...&to=...` 重放指定时间范围内的事件到 webhook URL。

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **密钥轮换期间正在重试的消息** | 保留消息的原始密钥 ID，即使密钥已轮换也能用旧密钥验证签名 |
| **密钥泄露** | 紧急轮换：新密钥立即生效，旧密钥标记为 `revoked` 但保留 24 小时用于验证重试消息 |
| **目标 URL 长时间不可用** | 暂停投递（自动或手动），事件保留在 DB 中，恢复后重放 |
| **webhook 投递幂等** | 事件 ID 作为幂等键（`X-Aero-Event-Id`），消费者可去重 |

---

## 方向三：对象存储边界治理：大小限制、容量感知与流式完整性（Object Storage Boundary Governance）

### 现状

当前代码对对象大小没有任何系统性治理：

```go
// internal/service/file_crud.go — Put 路径
func (s *FileService) Put(ctx context.Context, tenant, bucket, key string, r io.Reader, size int64, opts PutOptions) (repository.Object, error) {
    // ... 校验 tenant, bucket, key ...
    // ... 配额校验（检查总容量）...
    // 无单个对象大小校验
    // 无 size <= 0 的边界处理
    // 无 Content-Length 与读取字节数的一致性校验
    info, err := s.store.Put(ctx, sk, reader, size, storage.PutOptions{...})
    // ...
}
```

| 缺口 | 代码证据 | 影响 |
|------|---------|------|
| **无最大对象大小** | 无 `MaxObjectSize` 配置，无 `validateObjectSize()` 调用 | 用户可以上传任意大小的对象，可能耗尽存储空间或触发 OOM |
| **Object size < 0 无保护** | `size` 为 `int64`，负值不会在 Put 入口被拒绝 | 可能造成存储后端异常或配额系统溢出 |
| **大小与配额不一致** | 配额检查使用 `preflightQuota`，但写入后 `AddTenantUsage` 可能因存储后端实际写入大小与 `size` 不同而不一致 | 配额记账不准确 |
| **无流式完整性校验** | `md5WrapReader` 只在 PUT 结束时校验，无分块校验 | 大文件传输过程中出现错误要到全部传输完成才能发现 |
| **无存储后端容量告警** | `storage.Storage` 接口无 `Capacity()` 方法 | 无法在存储空间耗尽前告警 |
| **无大小指标** | OTel 无对象大小分布指标 | 无法监控大对象占比，无法做容量规划 |
| **Multipart 无总大小限制** | multipart 上传可以无限添加 part | 用户可以创建巨大对象绕过单次上传限制 |

### 为什么需要

1. **资源保护是生产系统的第一道防线。** 没有一个生产级对象存储会允许用户上传无限大小的对象。当前零限制意味着：一个用户的一次错误操作（或恶意操作）就可以耗尽磁盘空间、OOM 进程、或产生无法处理的巨型索引任务。

2. **容量感知是实现智能运维的基础。** 如果系统不知道"当前存储后端使用了多少容量、还剩多少空间"，就无法做自动分层、无法做容量规划、无法在空间耗尽前告警。

3. **Tus/resumable upload 的前置条件。** 如果需要支持断点续传（已在多个 expansion 中被提出），必须先有一个清晰的大小边界和流式完整性校验策略。

4. **多云成本控制的起点。** 对象大小分布直接决定了存储成本。没有对象大小指标就无法做成本优化。

### 缺失的能力

1. **可配置的对象大小限制：**

   ```
   STORAGE_MAX_OBJECT_SIZE=5GB     # 默认 5 GB，0 = 不限制
   STORAGE_MAX_MULTIPART_SIZE=5TB  # multipart 上限，默认 5 TB
   ```

   在 `service.Put` 入口校验：

   ```go
   func (s *FileService) validateObjectSize(size int64) error {
       max := s.maxObjectSize
       if max > 0 && size > max {
           return fmt.Errorf("%w: object size %d exceeds max %d", ErrObjectTooLarge, size, max)
       }
       if size < -1 { // -1 = 未知大小，允许流式上传
           return fmt.Errorf("%w: negative size %d", ErrInvalidArgs, size)
       }
       return nil
   }
   ```

2. **存储后端容量感知：** 在 `storage.Storage` 接口中增加可选方法：

   ```go
   type StorageInfo struct {
       CapacityBytes  int64  // 总容量，-1 = 未知
       UsedBytes      int64  // 已用容量，-1 = 未知
       AvailableBytes int64  // 可用容量，-1 = 未知
       ObjectCount    int64  // 对象数，-1 = 未知
   }

   type Storage interface {
       // ... 现有方法 ...
       Info(ctx context.Context) (StorageInfo, error) // 可选：返回 nil, nil 表示不支持
   }
   ```

3. **流式完整性校验：** 对于流式上传（size=-1），使用 `TeeReader` + `crc32` 分块哈希，每 8 MiB 计算一个分块校验和。上传完成后提供完整校验和列表。

4. **对象大小分布指标：** OTel 新增 `object_size_bytes` 直方图（bucket: 1KB, 10KB, 100KB, 1MB, 10MB, 100MB, 1GB, 10GB），按 tenant 和 storage_class 标签。

5. **Multipart 总大小限制：** 在 `UploadPart` 路径中记录已上传 part 的总字节数，`CompleteMultipart` 时验证不超过限值。

6. **大小治理的可观测性：** 当对象超过配置阈值的 80% 时，产生 `WARN` 日志；超过 100% 时拒绝请求并记录审计。

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **流式上传 size=-1** | 未知大小的流式上传以 0 写入配额（因为不知道最终大小），允许但记录告警日志 |
| **Multipart 聚合后超限** | 在 CompleteMultipart 时校验总大小，超限则返回 400 并中止 |
| **后端容量不足** | `Info()` 返回 `AvailableBytes < 0` 时，PUT 返回 `507 Insufficient Storage` |
| **非精确大小（chunked transfer）** | 使用 CRC32 分块校验和追踪总字节数 |
| **gzip 压缩后大小变化** | 限制基于压缩前大小（`Content-Length`）而非压缩后 |

---

## 方向四：后台工作负载统一健康模型与自治愈（Background Workload Unified Health Model & Self-Healing）

### 现状

当前后台 goroutine 的管理方式（`cmd/server/main.go`）：

```go
// 15+ goroutine 各自独立启动，无统一管理
go indexer.Run(ctx, bus.Subscribe())
go avw.Run(ctx, bus.Subscribe())
go rw.Run(ctx, bus.Subscribe())
go jobs.NewPool(repo, jobReg, cfg.Jobs.Workers, logger).Run(ctx)
go wh.Run(ctx, bus.Subscribe())
go wh.RetryLoop(ctx)
go j.Run(ctx)
go lf.Run(ctx)
go rg.Run(ctx)
go maybeRewrapSSE(ctx, cfg, store, logger)
// ... 更多 goroutine
```

| 问题 | 代码证据 | 影响 |
|------|---------|------|
| **无健康报告** | `/readyz` 仅检查 DB + 存储后端，不反映 worker 状态 | 运维不知道后台工作负载是否在正常运行 |
| **无死锁检测** | worker goroutine 如果死锁或陷入无限循环，没有任何检测机制 | 索引停止更新、webhook 停止投递都无声无息 |
| **无自动重启** | 如果 worker 因为 panic 或临时错误退出，不会自动重启 | 需要人工介入恢复 |
| **无租户级 worker 状态** | indexer、AV、replication 按事件驱动，但无法查询"有哪些租户的索引滞后" | 运维无法针对性排查 |
| **无工作负载延迟指标** | 没有从事件产生到 worker 处理完成的端到端延迟指标 | 无法衡量后台管道健康度 |
| **无逐步降级模型** | worker 故障是二元的（运行/停止），没有部分降级模式 | 微小故障 → 完全停止，没有中间状态 |

### 为什么需要

1. **后台工作负载是系统的暗面。** 用户请求的响应时间可以被监控，但索引器是否在正常工作、复制是否在流动、webhook 是否在投递——这些都不可见。如果索引器因为一个异常对象而 panic 退出，没有告警，没有自动恢复，用户的数据将永远不会被搜索到。

2. **Kubernetes 就绪探针应反映完整健康。** 当前 `/readyz` 说"OK"只意味着 DB 连接正常 + 存储后端存活。后台 worker 停止工作的 Pod 不应该接收流量。K8s 的 `readinessProbe` 应该能够探测到"索引器已经停止工作"。

3. **Worker 故障是渐进式的。** `indexer.Run` 在一个对象上遇到不可恢复的错误并不意味着整个索引器应该停止。逐步降级模型允许部分功能退出来维护核心可用性。

### 缺失的能力

1. **Worker 健康注册与报告系统：**

   ```go
   // WorkerHealthReporter 是每个后台 worker 实现的接口
   type WorkerHealthReporter interface {
       Name() string
       Health(ctx context.Context) WorkerHealth
   }

   type WorkerHealth struct {
       Status    WorkerStatus // healthy | degraded | stalled | dead
       LastOK    time.Time
       Processed int64        // 已处理的事件/作业数
       Lag       time.Duration // 当前延迟（事件产生到处理完成）
       Error     string        // 最近错误
   }

   type WorkerStatus int
   const (
       WorkerHealthy  WorkerStatus = iota // 正常运行
       WorkerDegraded                      // 降级运行（部分能力缺失）
       WorkerStalled                       // 无进展（超过 N 分钟未处理新事件）
       WorkerDead                          // 已退出（goroutine 结束）
   )
   ```

2. **统一健康端点 `/debug/workers`：** 返回所有注册 worker 的健康状态 JSON：

   ```json
   {
     "workers": [
       {"name": "indexer", "status": "healthy", "processed": 15234, "lag": "2.3s"},
       {"name": "antivirus", "status": "stalled", "last_ok": "2026-07-10T10:15:00Z", "lag": "15m"},
       {"name": "replication", "status": "healthy", "processed": 893, "lag": "1.1s"},
       {"name": "webhook", "status": "dead", "error": "panic: runtime error"}
     ]
   }
   ```

3. **就绪探针集成：** 扩展 `/readyz` 的逻辑，当关键工作负载（indexer、job pool）处于 `stalled` 或 `dead` 状态超过配置阈值时返回 `503`。

4. **自治愈框架：** 每个 worker 包装在监督者 goroutine 中，当检测到 panic 或超时退出时自动重启（带指数退避）：

   ```go
   type Supervisor struct {
       worker    func(context.Context) error
       name      string
       maxRetry  int
       backoff   time.Duration
       health    *WorkerHealth
   }

   func (s *Supervisor) Run(ctx context.Context) {
       for attempt := 0; ; attempt++ {
           err := s.worker(ctx)
           if err == nil || errors.Is(err, context.Canceled) {
               return // 正常退出
           }
           if attempt >= s.maxRetry {
               s.health.Status = WorkerDead
               return // 超过重试上限，不再重启
           }
           s.health.Status = WorkerDegraded
           time.Sleep(s.backoff * time.Duration(1<<attempt))
       }
   }
   ```

5. **租户级处理延迟仪表盘：** Prometheus 指标 `worker_lag_seconds{worker="indexer", tenant="acme"}` 暴露每个租户的事件处理延迟，Grafana 面板显示处理最慢的 10 个租户。

6. **Worker 日志归因：** 每个 worker 设置独立的 `slog.Logger` 带 `worker=name` 标签，方便日志聚合时筛选。

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **Worker 全量重启风暴** | Supervisor 重启时使用指数退避（1s → 2s → 4s → 上限 30s），防止疯狂重启 |
| **Panic 重启后状态丢失** | Worker 重启后重新初始化状态（BM25、连接池等），不依赖内存中残存的旧状态 |
| **健康报告的高频轮询** | 健康报告使用缓存（`sync.Map` + 10s TTL），不要每次请求都查询 worker |
| **Worker 正常完成退出** | 如 indexer 的 context 被取消，视为正常退出，不触发重启 |
| **租户级延迟数据量大** | 租户级指标只在 `AI_INDEX_ENABLED` 时暴露，默认不记录 |

---

## 方向五：租户数据可移植性与自助导出（Tenant Data Portability & Self-Service Export）

### 现状

当前系统完全没有租户级数据导出能力：

| 能力 | 当前状态 | 代码证据 |
|------|---------|---------|
| **单对象下载** | ✅ 通过 GET API | `FileService.Get()` |
| **批量列举** | ✅ 通过 List API（带分页） | `FileService.List()` + `repo.ListObjects` |
| **元数据导出** | ❌ 没有元数据结构化导出 | 无 `export metadata` 端点 |
| **全量版本导出** | ❌ 没有包含所有历史版本的导出 | 无版本遍历导出路径 |
| **结构化格式** | ❌ 没有标准化导出格式 | 仅支持原始对象流 |
| **自助导出 API** | ❌ 没有租户可以自己调用的导出端点 | 无异步导出作业 |
| **跨协议导出** | ❌ 不能通过 S3/WebDAV/MCP 导出元数据 | 各协议只支持对象级操作 |
| **Snapshot** | ⚠️ 仅 SQLite，仅管理员可操作 | `internal/snapshot/snapshot.go` |

**企业痛点：**

| 痛点 | 说明 |
|------|------|
| **Vendor lock-in 恐惧** | 企业用户担心一旦使用 AeroVault 存储大量数据，未来无法迁移到其他平台 |
| **合规要求** | GDPR 要求数据控制者能够导出所有个人数据；PCI DSS 要求能够归档交易数据 |
| **审计需求** | 审计师可能要求提供指定时间段内所有对象及其元数据的完整清单 |
| **迁移场景** | 用户可能需要在 AeroVault 实例之间迁移（开发→生产、local→S3、single→cluster） |
| **备份验证** | 用户需要定期验证备份的可恢复性——导出的数据可以用于验证 |

### 为什么需要

1. **数据可移植性是企业采购的门槛条件。** 根据 Gartner 的报告，"数据可移植性"是企业选择云存储服务的前五大考量因素之一。没有自助导出能力意味着：用户的数据被锁定在系统中，这直接降低了 AeroVault 相比 MinIO / AWS S3 的竞争力。

2. **GDPR 合规的法律要求。** 第 20 条"数据可移植权"要求数据控制者能够以结构化、通用、机器可读的格式导出个人数据。虽然 AeroVault 是基础设施层，但企业用户选择 AeroVault 时需要它支持他们的合规义务。

3. **迁移是必须支持的场景。** 用户从 SQLite 迁移到 Postgres、从 local 迁移到 S3、从单机迁移到集群——这些都需要有"将所有数据无损地从 A 搬到 B"的能力。当前 snapshot 工具只覆盖 SQLite，且没有数据格式标准化。

### 缺失的能力

1. **租户自助导出 API：**

   ```text
   POST /v1/admin/tenants/{tenant}/export
   Request: {
     "format": "json-lines",    // 导出格式
     "include_versions": false, // 是否包含历史版本
     "include_metadata": true,  // 是否包含元数据
     "filter_prefix": "",       // 可选前缀过滤
     "filter_after": "2026-01-01T00:00:00Z", // 可选时间过滤
     "notification": {          // 完成通知
       "type": "webhook",
       "url": "https://example.com/export-complete"
     }
   }
   
   Response: 202 Accepted
   {
     "export_id": "exp_a1b2c3d4",
     "status": "pending",
     "estimated_objects": 15234,
     "estimated_size_bytes": 1234567890
   }
   ```

2. **标准化导出格式：** JSON Lines（`.jsonl`）元数据 + 独立对象文件：

   ```
   export/
   ├── manifest.json          # 导出元数据：创建时间、租户、对象数、大小、包含的版本范围
   ├── metadata.jsonl         # 每行一个 JSON 对象（key, size, etag, content_type, metadata, tags, version_id, created_at, locked_until）
   ├── objects/
   │   ├── a1/b2/c3/...       # 对象文件，key 的 hash 路径避免冲突
   │   └── ...
   └── checksums.sha256       # 所有文件的 SHA-256 校验和
   ```

3. **后台导出作业：** 基于现有的 `jobs` 表基础设施，导出作为异步作业运行：

   ```go
   const JobExportTenant jobs.Type = "export.tenant"
   
   type ExportJobPayload struct {
       Tenant       string
       ExportID     string
       Format       string
       IncludeVersions bool
       IncludeMeta  bool
       PrefixFilter string
       TimeAfter    string // RFC3339
       NotifyURL    string
   }
   ```

4. **导出状态跟踪：**

   - `GET /v1/admin/tenants/{tenant}/exports/{export_id}` — 查询导出进度
   - `GET /v1/admin/tenants/{tenant}/exports` — 列出历史导出
   - 导出完成后在指定 URL 发送通知（HMAC 签名）

5. **通过 S3 接口导出：** `GET /s3/{bucket}?export`（S3 Batch 操作风格的导出触发），输出到指定目标 bucket。

6. **导入补集：** 与导出对应的导入端点 `POST /v1/admin/tenants/{tenant}/import`，支持从 JSON Lines + 对象文件的归档恢复。

7. **CLI 集成：** `aero-vault cli export` 命令：

   ```bash
   aero-vault cli export --tenant acme --format json-lines --output ./export/
   ```

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| **超大租户（TB 级数据）** | 导出作业分片执行，每片独立校验和，支持增量导出 |
| **导出期间数据变更** | 导出创建时间点的快照一致性（使用 `ListObjectIDs` + 逐对象导出，非事务一致性） |
| **被锁定对象** | 导出包含 locked_until 字段；导入时恢复锁定状态 |
| **软删除对象** | 按配置决定是否包含已软删除但未被 GC 的对象 |
| **导出中断/恢复** | 导出作业检查点（每完成 1000 个对象记录一次进度），中断后从检查点恢复 |
| **导出文件过大** | 超过单文件限制（默认 2 GB）时自动分片 |

---

## 综合优先级与建议实施顺序

| 优先级 | 方向 | 影响面 | 前置依赖 | 涉及改动量 | 建议开始时间 |
|--------|------|--------|---------|-----------|------------|
| **P0** | 多租户数据隔离验证框架 | 安全/信任——直接影响生产部署的信心 | 无 | 新增 `internal/integration/isolation_test.go`（~500 行测试代码） | **立即** |
| **P1** | 后台工作负载统一健康模型 | 可靠性/运维——影响所有部署的可观测性 | 无 | `internal/worker/` 新包 + `main.go` 改造（~400 行） | **当前 Sprint** |
| **P1** | Webhook 密钥轮换与交付硬化 | 安全/可靠性——直接影响 webhook 功能的可用性 | 无 | `internal/events/webhook.go` 改造 + 新表 migration（~300 行） | **当前 Sprint** |
| **P2** | 对象存储边界治理 | 可靠性——长期资源保护 | 无 | `service/file_crud.go` 校验 + `storage.Info` 可选接口（~200 行） | **下一 Sprint** |
| **P2** | 租户数据可移植性与自助导出 | 产品/合规——影响企业采纳决策 | Job 基础设施已就绪 | 新增 `export/` 包 + CLI 命令 + REST 端点（~600 行） | **下下 Sprint** |

### 建议的 Sprint 计划

```
Sprint N（立即）:
  ├── 方向一：创建多租户隔离测试套件，覆盖 REST + S3 API 基本路径（~200 行测试）
  └── 方向二：Webhook 密钥版本化 + 死信状态独立（安全漏洞，立即修复）

Sprint N+1:
  ├── 方向一：扩展隔离测试到 MCP + WebDAV + SSE 事件 + 搜索
  ├── 方向四：Worker 健康注册 + /debug/workers 端点
  └── 方向三：MaxObjectSize 配置 + Put 路径校验

Sprint N+2:
  ├── 方向四：Supervisor 自治愈框架 + /readyz 集成
  ├── 方向五：导出格式规范 + 后台导出作业
  └── 方向二：Webhook 管理 API（CRUD + 轮换）

Sprint N+3+:
  ├── 方向五：Import 补集功能 + CLI export 命令
  ├── 方向三：Storage.Info 容量感知 + Prometheus 指标
  └── 方向一：CI 集成 `make test-isolation`
```

### 与既有 46 期分析的去重关系

| 方向 | 既有覆盖 | 本分析的新贡献 |
|------|---------|-------------|
| **多租户数据隔离验证框架** | ❌ **零覆盖** | 首次识别 11 个跨协议隔离攻击面 + 系统化渗透测试套件设计 + 404 vs 403 模糊测试 + SSE/搜索/预签名隔离验证 |
| **Webhook 密钥轮换与交付硬化** | ❌ **零覆盖** | 首次设计多密钥环 + 版本化签名 + 死信独立状态 + 自适应重试节奏 + 管理 API |
| **对象存储边界治理** | ❌ **零覆盖** | 首次提出 MaxObjectSize 配置 + Storage.Info 容量感知 + 流式分块完整性 + 大小分布指标 |
| **后台工作负载统一健康模型** | ⚠️ v39 方向二覆盖"Worker 健康管理"（聚焦 SSE channel leak + 单一 Worker Exit Detection） | 首次提供统一健康注册系统 + 自治愈 Supervisor + /debug/workers 端点 + 就绪探针集成 + 租户级延迟指标 |
| **租户数据可移植性与自助导出** | ⚠️ v27/v35 各 1 行提及"数据可移植性" | 首次提供完整架构设计：标准化导出格式 + 后台导出作业 + 状态跟踪 + 导入补集 + CLI 集成 |
