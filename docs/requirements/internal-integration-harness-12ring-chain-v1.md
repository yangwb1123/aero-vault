# 方向：弥合 I4 缺口 —— 集成 harness 中间件链必须镜像生产 12 环链（cmd/server/http.go:applyMiddleware）

> **模块：** `internal/integration`（+ `cmd/server` 链构造抽取） · **来源分析：** `docs/auto/analyses/internal-integration-7479f0a2.json`（方向） · **日期：** 2026-08-07
> **评分：** 价值 8 / 风险降低 8 / 工作量 4 / 置信度 9
> **本文所有代码引用均已对照仓库逐条验证**（行号以当前工作树为准；方向引用的行号来自分析快照，漂移已在证据表中修正）。
>
> **范围纪律：** 本规格**只**做方向验收覆盖的五件事（共享链 helper + 4 个行为测试 + CORS 测试重写）。不改生产链语义/顺序、不加新中间件、不引入新的 harness 行为测试（Concurrency/OTel/BucketCORS 的"在场"由共享 helper 与链形测试保证，见 AC-1）。

---

## 1. 问题陈述

`internal/integration/fullserver_test.go` 的"production-shaped" harness 自称与生产同形，但实际只挂载了生产 12 环链（cmd/server/http.go:151-181）中的 **7 环**：

| # | 生产链环节（注册序，http.go:155-177） | harness（fullserver_test.go:149-155） |
|---|--------------------------------------|--------------------------------------|
| 1 | `access_log`（AccessLog） | ✅ AccessLog |
| 2 | `concurrency`（ConcurrencyLimiter/PerTenant） | ❌ |
| 3 | `recoverer`（Recoverer） | ✅ Recoverer |
| 4 | `otel`（telemetry.HTTPMiddleware） | ❌ |
| 5 | `rate_limit`（RateLimiter） | ✅ `rl.Middleware()`（nil rl → 透传，ratelimit.go:141-144） |
| 6 | `tenant`（**TenantWithStatus**，repo 查状态） | ⚠️ 裸 `middleware.Tenant`（= `TenantWithStatus(nil)`，middleware.go:46-48，**无状态门禁**） |
| 7 | `auth`（authReg.Middleware） | ✅ authReg.Middleware() |
| 8 | `max_body`（MaxBodySize） | ❌ |
| 9 | `secure_headers`（SecureHeaders） | ❌ |
| 10 | `cors`（CORS，带生产配置） | ⚠️ `CORS(CORSConfig{})` → **纯透传**（cors.go:32-34） |
| 11 | `cors_bucket`（BucketCORS） | ❌ |
| 12 | `request_id`（RequestID） | ✅ RequestID |

**后果（方向陈述，全部验证属实）：**

1. **零覆盖的回归面：** 413 超大请求体路径（MaxBodySize）、安全响应头（SecureHeaders）、per-tenant 并发限制（Concurrency）、per-bucket CORS（BucketCORS）、禁用租户 403 拒绝（TenantWithStatus）在集成套件中**无任何测试**。从 main.go 删掉 `MaxBodySize` 或 `SecureHeaders` 一行，整个集成套件照常全绿（I4 契约的"链顺序固定"由 main.go 独有实现，无第二处引用校验）。
2. **`TestFullServer_CORS`（fullserver_test.go:439-450）是空测试：** 请求后 `_ = resp.Header.Get("Access-Control-Allow-Origin")` —— 读到的值**被丢弃**，零断言；且 harness 传入 `CORSConfig{}` 使 CORS 中间件为纯透传（cors.go:32-34）——即使有断言也无法测出任何 CORS 回归。当前该测试**不可能失败**。
3. **根因：** `cmd/server` 是 `package main`（http.go:1），`internal/integration` 无法 import 生产链构造；harness 只能手工复制 7 环子集，且复制品与生产实现无编译期绑定，必然漂移。

### 触发场景（真实回归）

1. 开发者从 `applyMiddleware` 删除 `max_body` 环或 `secure_headers` 环（或因重构误删 `cfg.App.MaxBodySize` 装配）→ `make check` 全绿 → 生产 413 路径与安全头静默丢失，上线后客户端收到 200 + 无 `X-Content-Type-Options`。
2. 管理员将租户置为 `disabled`（admin API `PUT /v1/admin/tenants/{t}/status`，admin.go:360-373）→ 生产拒绝 403 `TenantDisabled`（tenant_status.go:33-34）；但 harness 用裸 `Tenant`，**禁用租户门禁在集成套件中从未被执行过一次**。
3. CORS 全局配置（`CORS_ALLOWED_ORIGINS`）回归 → 生产 preflight 行为变化，集成套件零感知（空测试）。

---

## 2. 现状与代码证据（已逐条验证）

| # | 证据 | 验证结果 |
|---|------|---------|
| E1 | `internal/integration/fullserver_test.go:147-157` — harness 链：`var finalHandler = dispatcher` 后依次套 7 环（:149-155 AccessLog → Recoverer → rl.Middleware() → Tenant → authReg.Middleware() → CORS(CORSConfig{}) → RequestID），循环 :157 `finalHandler = m(finalHandler)`（方向引用 :148，漂移 ±1） | ✅ 与引用一致；**确认只 7 环，缺 concurrency/otel/max_body/secure_headers/cors_bucket/tenant-status** |
| E2 | `internal/integration/fullserver_test.go:439-450` — `TestFullServer_CORS`：发 OPTIONS+Origin 后 `resp.Body.Close(); _ = resp.Header.Get("Access-Control-Allow-Origin")` —— **返回值被丢弃，零断言**（方向引用 :439，精确） | ✅ 与引用一致；空测试成立 |
| E3 | `cmd/server/http.go:151-181` — `applyMiddleware`：12 环注册序 `access_log→concurrency→recoverer→otel→rate_limit→tenant(TenantWithStatus, repo 查 status)→auth→max_body→secure_headers→cors→cors_bucket→request_id`；每环经 `telemetry.WithMiddlewareTiming(name, mw)` 包裹（:179）。**执行序（外→内）：RequestID → BucketCORS → CORS → SecureHeaders → MaxBodySize → Auth → Tenant → RateLimit → OTel → Recoverer → Concurrency → AccessLog**（方向引用 :143，实际 :151，漂移） | ✅ 与引用一致（语义）；行号漂移 :143→:151 |
| E4 | `internal/middleware/cors.go:31-34` — `CORS(cfg)`：`len(cfg.AllowedOrigins) == 0` → 返回纯透传（:32-34）——**空配置 = 中间件完全失效**（方向引用 :32，精确） | ✅ 与引用一致 |
| E5 | `internal/middleware/tenant_status.go:15-42` — `TenantWithStatus(lookup)`：:33-34 `found && status == "disabled"` → 403 `{"error":{"code":"TenantDisabled",...}}`；lookup err → 503 `TenantStatusUnavailable`（:27-31）；**未知租户放行**（found=false，向后兼容隐式租户）；:37-42 `tenantStatusBypass`：`/`、`/healthz`、`/readyz`、`/metrics`、`/openapi.json`、`/docs`、`/ui*`、`/auth/oidc/*`、`/share/*`、`/public/assets/*` **跳过状态检查**（方向引用 :15，精确） | ✅ 与引用一致 |
| E6 | `internal/middleware/middleware.go:46-48` — `Tenant(next)` = `TenantWithStatus(nil)(next)`——裸 `Tenant` 无状态门禁；harness 用裸 `Tenant`（E1） | ✅ 补充验证（harness 与生产的唯一 tenant 差异点） |
| E7 | `cmd/server/http.go:1` `package main` + `cmd/server/main.go:165` 生产唯一装配点 `finalHandler := applyMiddleware(dispatcher, repo, authReg, rl, cfg, logger, concurrencyMW, corsProvider)` —— `package main` 不可被 `internal/integration` import，链构造无法复用（方向陈述成立） | ✅ 与引用一致 |
| E8 | 租户持久化设施：`repository_interface.go:169-170` `UpsertTenant(ctx, TenantRecord)` / `GetTenant(ctx, tenantID) (TenantRecord, bool, error)`（实现 tenants.go:37-46）；`TenantRecord.Status`（repository.go:269-277）；admin 端状态值仅 `"active"\|"disabled"`（admin.go:373） | ✅ 补充验证（AC-4 测试装具） |
| E9 | `internal/middleware/validation.go:16-45` — `MaxBodySize`：`r.ContentLength > maxBytes` → **413** `http.Error` + `Connection: close`（:24-30）；`maxBytes <= 0` → 透传（:19-21）；配置 `cfg.App.MaxBodySize` ← `APP_MAX_BODY_SIZE`，**默认 0 = 不限**（config.go:48,83） | ✅ 补充验证（AC-2 语义与默认值） |
| E10 | `internal/middleware/validation.go:47-64` — `SecureHeaders`：恒设 `Strict-Transport-Security`、**`X-Content-Type-Options: nosniff`**、**`X-Frame-Options: DENY`**、`Referrer-Policy`、`Permissions-Policy`，无 bypass 路径 | ✅ 补充验证（AC-3 断言值） |
| E11 | `internal/middleware/cors.go:78-96` — `writeCORSHeaders`：**`Access-Control-Allow-Origin` 恒回显请求 Origin**（:87-88，即使通配配置也回显具体 origin）+ `Vary: Origin`；preflight 命中允许 origin → **204**（:74-76）；preflight 命中不允许 origin → **403**（:62-65）；缺省 AllowedMethods/Headers/MaxAge 有默认值（:35-43） | ✅ 补充验证（AC-5 断言值） |
| E12 | 各缺失环的 nil/0 语义：`BucketCORS(nil provider)` → 透传（cors_bucket.go:150-152）；`NewConcurrencyLimiter(max<=0)` → 透传（middleware.go:124-130）；`RateLimiter.Middleware()` nil receiver → 透传（ratelimit.go:141-144）——**harness 现有 `var rl, aiRL *middleware.RateLimiter`（fullserver_test.go:110-111）能跑正是因为 nil 安全** | ✅ 补充验证（共享 helper 的 harness 侧缺省参数可安全传 nil/0） |
| E13 | import 图：`repository→telemetry`、`middleware→repository`、`auth→access`、`access→repository`、`telemetry/config` 不 import 任何内部包 —— **新建 `internal/server` 包 import {config, auth, middleware, repository, telemetry} 无环**；放 `internal/middleware` 亦无环（auth/config/telemetry 均不 import middleware） | ✅ 补充验证（FR-1 落点可行性） |
| E14 | `internal/integration` 中**无任何测试持久化 status=disabled 的租户**（grep "disabled" 仅命中注释/其他语义）→ harness 换用 `TenantWithStatus`（repo 查询）对既有测试**非破坏**：未知租户仍放行（E5），既有用例无租户持久化 disabled 状态 | ✅ 补充验证（FR-2 行为变更面） |
| E15 | harness 装具：`fullServerHarness{ts, repo, dsn}`（fullserver_test.go:39-43）已暴露 repo；`startFullServerWithRelay`/`startFullServerWithAuthAndRelay` 均收敛到 `startFullServerOpts`（:66-138）——**新 config 注入点只需扩展 `startFullServerOpts` 或新增构造器**，既有调用点零改动 | ✅ 补充验证（FR-2 装具面） |
| E16 | `telemetry.WithMiddlewareTiming`（telemetry/http.go:17-24）与 `telemetry.HTTPMiddleware`（:29+）签名确认——共享 helper 必须保留每环 timing 包裹与 otel 环（生产可观测性行为，harness 继承后无副作用，otel no-op 全局默认） | ✅ 补充验证 |
| E17 | `go vet -tags=integration ./...` 是仓库既有零 Docker 编译门禁（Makefile:113,138-139）；`internal/integration` 中 `qdrant_integration_test.go`/`postgres_integration_test.go`/`audit_governance_postgres_test.go` 带 `//go:build integration`，其余（含 fullserver_test.go）**无 tag** | ✅ 补充验证（AC-1 验收命令与既有 gate 一致） |

### 缺陷机理

```
生产：main.go:165 applyMiddleware(dispatcher, …) ── 12 环（E3）
harness：fullserver_test.go:147-157 手工复制 7 环（E1）—— 复制品与生产无编译期绑定
  ├─ 缺 max_body        → 413 路径零集成覆盖（E9）
  ├─ 缺 secure_headers  → 安全头零集成覆盖（E10）
  ├─ 缺 tenant-status   → 裸 Tenant（E6），禁用租户 403 零集成覆盖（E5）
  ├─ CORSConfig{}       → CORS 纯透传（E4），TestFullServer_CORS 零断言（E2）
  └─ 缺 concurrency/otel/cors_bucket → 三环"在场"无任何校验
根因：cmd/server 为 package main（E7），链构造不可复用 → 必然漂移
```

---

## 3. 需求规格

### FR-1：链构造抽取为可 import 的共享 helper（`internal/server`）

把 `applyMiddleware`（cmd/server/http.go:151-181）整体迁移到新包 `internal/server`（建议；`internal/middleware` 为可接受替代，E13 证明两者均无环），导出同名函数：

```go
// internal/server/chain.go（迁移自 cmd/server/http.go:151-181，正文逐行保留）
func ApplyMiddleware(handler http.Handler, repo repository.Repository,
    authReg *auth.Registry, rl *middleware.RateLimiter, cfg *config.Config,
    logger *slog.Logger, concurrencyMW func(http.Handler) http.Handler,
    corsProvider middleware.BucketCORSProvider) http.Handler
```

- **约束 a（行为零漂移）：** 12 环注册序、每环 `telemetry.WithMiddlewareTiming` 包裹、`TenantWithStatus` 的 repo 查询闭包（`repo.GetTenant` → `record.Status`）、CORS 生产配置（含 `ExposeHeaders` 追加 `ETag/Idempotency-Replayed/Retry-After/X-Request-ID/X-Version-Id`，http.go:169-173）逐行保留。
- **约束 b（nil 安全，仅 harness 受益）：** `concurrencyMW == nil` 时跳过该环（生产恒非 nil，main.go:158-163，行为不变）；`rl == nil`、`corsProvider == nil` 的透传语义由既有中间件保证（E12）。
- **约束 c（链形可测）：** 链定义以数据形式暴露（如 `ChainLinks() []ChainLink{Name, MW}` 或同包测试可见的链接表），配同包单测断言 **12 个名字 + 注册序** 与本文 §1 表一致——使"镜像生产链"成为可回归的断言而非仅靠行为测试间接覆盖。
- **约束 d：** `cmd/server/http.go` 删除原 `applyMiddleware` 定义；`cmd/server/main.go:165` 改调 `server.ApplyMiddleware`。`cmd/server` 不再持有链构造逻辑（唯一真源 = helper）。

### FR-2：harness 改用共享 helper + config 注入点

`internal/integration/fullserver_test.go` `startFullServerOpts`（:66-138）中的手工 7 环循环（:147-157）**整体删除**，改为：

```go
finalHandler := server.ApplyMiddleware(dispatcher, repo, authReg, rl, cfg,
    logger, middleware.NewConcurrencyLimiter(0).Middleware(), nil /* corsProvider */)
```

- **约束 a：** 新增构造器 `startFullServerWithConfig(t, relayOpts, authKeys, cfg *config.Config)`（或等价参数化），既有三个构造器（`startFullServer` / `startFullServerWithRelay` / `startFullServerWithAuthAndRelay`）委托之并传**生产默认配置**（`App.MaxBodySize=0`、`CORS` 全空 —— E9/E4 的默认值）→ 既有测试行为不变（E14/E15）。
- **约束 b：** `rl` 仍传 nil（E12 透传，保持现有无限流语义）；`concurrencyMW` 传 `NewConcurrencyLimiter(0).Middleware()`（E12 透传）——**不改变任何既有测试的限流/并发行为**。
- **约束 c：** tenant 环由裸 `Tenant` 变为 `TenantWithStatus`（repo 查询）——**这是本方向唯一有意的 harness 行为变更**，语义与生产一致（E5）：未知租户放行、已知 disabled 租户 403、查询错误 503。
- **约束 d：** `go vet -tags=integration ./...` 通过（E17，仓库既有 gate 语义）。

### FR-3：集成测试 —— 超大请求体 → 413（MaxBodySize 环）

新测试（`fullserver_test.go` 内）：用 `startFullServerWithConfig` 挂 `cfg.App.MaxBodySize = 1024`（或任意小值）。

- PUT `/v1/files/oversize.txt`，请求体 > 1024 字节且 Content-Length 已知（`http.NewRequest` + 大 body，E9 早拒路径）→ 断言 **413**。
- 控制组：同配置下 body ≤ 1024 → 断言 2xx（证明 413 来自大小而非其他环节）。

### FR-4：集成测试 —— 安全响应头（SecureHeaders 环）

新测试：`GET /healthz`（链上任意非 bypass 路径均可；SecureHeaders 无 bypass，E10）→ 断言响应头 `X-Content-Type-Options == "nosniff"` 且 `X-Frame-Options == "DENY"`（E10 精确值）。

### FR-5：集成测试 —— 禁用租户 → 403 TenantDisabled（TenantWithStatus 环）

新测试（默认无 auth harness 即可，tenant 环先于 auth 环，E3 执行序）：

1. 经 `fullServerHarness.repo`（E15）`repo.UpsertTenant(ctx, repository.TenantRecord{TenantID: "suspended", DisplayName: "Suspended Tenant", Status: "disabled"})`（E8）。
2. `GET /v1/files`（**非 bypass 路径**，E5/E14——`/healthz` 等会跳过状态检查）带 `X-Aero-Tenant: suspended` → 断言 **403** 且响应体 JSON `error.code == "TenantDisabled"`（E5 精确载荷）。
3. 控制组：同路径带 `X-Aero-Tenant: ghost-tenant`（未持久化）→ 断言**非 403**（锁定 E5 的未知租户向后兼容语义）。

### FR-6：`TestFullServer_CORS` 重写（去空测试化）

用 `startFullServerWithConfig` 挂 `cfg.CORS.AllowedOrigins = []string{"http://example.com"}`（**非空**，E4 使 CORS 中间件激活）：

1. `OPTIONS /v1/files` + `Origin: http://example.com` + `Access-Control-Request-Method: GET` → 断言 **204** 且 `Access-Control-Allow-Origin == "http://example.com"`（回显，E11:87-88）且 `Vary: Origin`（E11）。
2. （同一测试内，推荐）`OPTIONS /v1/files` + `Origin: http://evil.example`（不允许 origin）→ 断言 **403**（E11:62-65 deny 路径）。
3. 删除旧测试体中的 `_ = resp.Header.Get(...)` 空断言（E2）；新测试对每个读到的 header 都显式断言（`t.Fatalf` 带期望/实际值）。

---

## 4. 验收标准（可测试）

> 方向提供的 5 条验收全部保留，逐条映射为可执行断言。验收 1 的 `go vet -tags=integration ./...` 与仓库既有 gate（Makefile:113,138-139）一致。

### AC-1 共享链 helper 双向装配（编译期绑定 + 链形断言）

- `cmd/server/http.go` **不再包含** `applyMiddleware` 定义；`cmd/server/main.go:165` 与 `internal/integration/fullserver_test.go`（FR-2 装配点）**调用同一个导出符号**（`server.ApplyMiddleware`）。
- helper 包内单测：`ChainLinks()` 返回 12 个名字，依次为 `access_log, concurrency, recoverer, otel, rate_limit, tenant, auth, max_body, secure_headers, cors, cors_bucket, request_id`（与 §1 表一致），重复调用幂等。
- 门禁命令全部通过：`go build ./...`、`go vet -tags=integration ./...`、`go test ./internal/server/ ./internal/integration/`。
- 可证伪性：若有人从 helper 删除任一环或改序，上述链形单测失败；若 harness 改回手工复制链，编译期符号绑定消失即 review 可发现。

### AC-2 超大请求体 → 413（集成）

- 构造：`startFullServerWithConfig(t, &events.EventOutboxRelayOptions{}, "", &config.Config{App: config.AppConfig{MaxBodySize: 1024}})`（字段名以 config.go:14,37 实际类型为准）。
- `PUT {ts.URL}/v1/files/oversize.txt`，body 4096 字节 → **status == 413**。
- 控制：同配置 `PUT` body 512 字节 → **status ∈ [200, 299]**。

### AC-3 安全响应头（集成）

- `GET {ts.URL}/healthz` → `X-Content-Type-Options == "nosniff"` **且** `X-Frame-Options == "DENY"`（精确字符串，E10）。

### AC-4 禁用租户 → 403 TenantDisabled（集成）

- 前置：`h := startFullServerWithRelay(t, &events.EventOutboxRelayOptions{})`；`h.repo.UpsertTenant(ctx, repository.TenantRecord{TenantID: "suspended", DisplayName: "suspended", Status: "disabled"})`。
- `GET {ts.URL}/v1/files` + `X-Aero-Tenant: suspended` → **status == 403**，响应体 JSON 解码后 `error.code == "TenantDisabled"`。
- 控制：同请求 `X-Aero-Tenant: ghost-tenant` → **status != 403**。

### AC-5 CORS 测试重写（集成）

- 构造：`startFullServerWithConfig` + `cfg.CORS.AllowedOrigins = []string{"http://example.com"}`。
- `OPTIONS {ts.URL}/v1/files` + `Origin: http://example.com` + `Access-Control-Request-Method: GET` → **status == 204**，`Access-Control-Allow-Origin == "http://example.com"`，`Vary == "Origin"`。
- （推荐，同测试）`Origin: http://evil.example` → **status == 403**。
- 旧测试体的 `_ = resp.Header.Get(...)` 空断言**已删除**（grep 确认 fullserver_test.go 不再有该模式）。
- 可证伪性：harness 若改回 `CORSConfig{}`（透传），本测试 204/403 断言立即失败——旧空测试做不到这一点。

---

## 5. 范围边界（明确不做）与决策记录

| 明确不做 | 理由 |
|---------|------|
| 改动生产链的环节/顺序/配置默认值 | 方向只要求 harness 镜像生产，生产语义零漂移（FR-1 约束 a） |
| 为 Concurrency/OTel/BucketCORS 增加**行为**级集成测试 | 方向验收无此项；三环在场由共享 helper + AC-1 链形断言保证（BucketCORS nil provider 透传、Concurrency 0 透传，行为测试无观测面，E12） |
| 扩展 harness 路由/挂载面（rest.NewRouter 的 CORS provider、webdav dispatcher、/info、/docs 等与生产的差异） | 超出本方向；本方向只收敛中间件链 |
| S3/WebDAV/MCP 协议的 CORS/413/403 专项测试 | 方向验收限 REST `/v1` 路径 |
| 改 `APP_MAX_BODY_SIZE`/`CORS_ALLOWED_ORIGINS` 生产默认值 | 默认值维持现状（0 不限 / 空） |
| 为 harness 引入断言框架 | I6：仅 `testing` |

**决策记录：**

- **D1 落点 `internal/server`（新包）而非 `internal/middleware`：** 两者均无环（E13）；`internal/server` 保持 middleware 的叶子性，且包名与 `cmd/server` 呼应、语义即"服务器装配"。若 reviewer 倾向 middleware，FR-1 的迁移面不变。
- **D2 `concurrencyMW` nil 跳过：** 生产恒非 nil（main.go:158-163），跳过分支仅防御 harness 误传 nil；harness 按 FR-2 传 `NewConcurrencyLimiter(0).Middleware()`，双保险。
- **D3 harness 默认配置 = 生产默认值：** 三个既有构造器委托 `startFullServerWithConfig` 时传 `MaxBodySize=0` + CORS 空 → 除 tenant-status 门禁（FR-2 约束 c，本方向核心目的）外，既有测试行为逐字节不变（E14/E15）。
- **D4 禁用租户测试用 `repo.UpsertTenant` 而非 admin API：** 默认 harness 无 auth，`requireAdmin` 恒真但无意义；直接经 harness 已暴露的 repo（E15）设置状态，零额外装配。
- **D5 413 测试用 Content-Length 早拒路径：** `http.NewRequest` 已知长度 → 命中 validation.go:24-30 的 413 早拒（不读 body），断言稳定且与 chunked 流式路径无关。

---

## 6. 实现指引（供验收后落地，非本规格交付物）

1. **抽取：** 新建 `internal/server/chain.go`（FR-1 签名 + 12 环表 + `ChainLinks()` 访问器 + nil 跳过）；`cmd/server/http.go` 删 `applyMiddleware`；`cmd/server/main.go:165` 改调 `server.ApplyMiddleware`。
2. **链形单测：** `internal/server/chain_test.go` —— 12 名字与序、幂等性（AC-1）。
3. **harness：** `fullserver_test.go` 增 `startFullServerWithConfig`（参数化 `*config.Config`），三个既有构造器委托之；删 :147-157 手工链，改调 `server.ApplyMiddleware`（FR-2）。
4. **测试：** FR-3/4/5 三个新测试 + FR-6 重写 `TestFullServer_CORS`，全部用 `httptest` 客户端显式断言（AC-2…AC-5）。
5. **门禁：** `make check`（含 `vet-integration`）与 `go test ./internal/server/ ./internal/integration/` 全绿；确认 `grep -n "_ = resp.Header.Get" internal/integration/` 无输出（AC-5 可证伪性）。

**验收证据（落地后应可复现）：** ① `grep applyMiddleware cmd/server/ internal/integration/` 只命中 `server.ApplyMiddleware` 调用与定义；② AC-2…AC-5 四个测试在删除任一对应环（`max_body`/`secure_headers`/`tenant` 状态查询/CORS 配置）时**必然失败**——这是本方向对"删掉 main.go 一行集成套件仍全绿"的最终反证。
