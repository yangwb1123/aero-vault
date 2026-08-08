# 方向：BucketCORS 恒以 "default" 解析租户 —— 多租户 per-bucket CORS 失效且默认租户规则跨租户泄漏（验收规格 · 已验证现状）

> **模块：** `internal/middleware`（`cors_bucket.go` · `middleware.go` · `tenant_status.go`）+ 装配点 `internal/server/chain.go`
> **来源分析：** `docs/auto/analyses/internal-middleware-697499e2.json`（方向 1）· **日期：** 2026-08-07 · **HEAD：** `acfaaf4`
> **评分：** 价值 9 / 风险降低 8 / 工作量 3 / 置信度 9
> **状态声明：** 本文是**验收契约**而非绿地设计：逐条核验方向引证（§1）、实证问题后果与边界（§2）、锁定行为要求（§3）、**原样保留三条验收检查**并映射为可执行测试矩阵（§4）。超范围项一律不做（§5）。

---

## 1. 证据核验（方向引证逐条对照当前 HEAD）

**关键更正：** 方向引证的装配点 `cmd/server/http.go:143-169`（`applyMiddleware`）在 HEAD 已**迁移**到 `internal/server/chain.go`（`BuildChain` :33-93 + `ApplyMiddleware` :92-103），该文件头自述为 "the verbatim migration of the former cmd/server/http.go applyMiddleware (zero production drift)"，消费点 `cmd/server/main.go:166`。环顺序被 `internal/server/chain_test.go:66` `TestBuildChain_12RingsInOrder`（`wantRings` :26-38）钉死。**执行序（最外→最内）：** `request_id → cors_bucket → cors → secure_headers → max_body → auth → tenant → rate_limit → otel → recoverer → concurrency → access_log` —— **`cors_bucket` 比 `tenant` 早 9 环执行**，方向核心断言成立。

| # | 方向引证 | 当前 HEAD 位置 | 核验结论 |
|---|---------|----------------|---------|
| E1 | `cmd/server/http.go:143`（applyMiddleware 装配链） | **已迁移**：`internal/server/chain.go` `BuildChain` :33-93 / `ApplyMiddleware` :92-103（最外环 = 切片末元素 `request_id`）；`cmd/server/http.go:143` 现为 `return buildDispatcher(r, davH, cfg)` | ✅ **位置迁移**（链代码整体搬迁，注释明示零漂移迁移），语义成立：`cors_bucket` 先于 `auth`、`tenant` 执行 |
| E2 | `cmd/server/http.go:167`（chain 中 BucketCORS 位置） | `internal/server/chain.go:84` `{RingBucketCORS, middleware.BucketCORS(corsProvider)}`（切片倒数第二元素 = 最外第二环） | ✅ 同上 |
| E3 | `internal/middleware/cors_bucket.go:147` `BucketCORS` | `func BucketCORS` :147；`origin := r.Header.Get("Origin")` :154；**`tenant := TenantFrom(r.Context())` :155** | ✅ **行号精确** |
| E4 | `internal/middleware/cors_bucket.go:155`（租户解析） | :155-156 `tenant := TenantFrom(r.Context())`；`bucket := bucketFromPath(r)`；bucket 为空回落 `"default"` :157-159 | ✅ **行号精确** |
| E5 | `internal/middleware/middleware.go:50` `TenantFrom` 回落 default | `func TenantFrom` :50；:52-53 `if !ok { return "default" }`（引证范围 50-58，回落点在 :53） | ✅ 行号精确（范围微调），语义成立 |
| E6 | `internal/middleware/tenant_status.go:18`（唯一设置 ctxTenantID 处） | `func TenantWithStatus` :15；**`ctx := context.WithValue(r.Context(), ctxTenantID, tenant)` :22** | ✅ **行号漂移**（18→22），语义精确：`ctxTenantID` 全仓库唯一写入点 = `tenant_status.go:22`；`Tenant` 中间件（`middleware.go:46-48`）委托同一实现。`ctxTenantID` 为包内未导出键，外部无法预置 |
| E7 | `internal/middleware/cors_bucket_test.go:37-48`（测试全用 default） | :33 `SetBucketCORS(ctx, "default", "default", …)`；:96/:105 查询/写入均租户 `"default"`；三个现有测试**均不挂 Tenant 中间件、不设 ctxTenantID** | ✅ 成立：现有测试只覆盖回落路径，无法捕获本缺陷 |
| E8 | Auth 只改写 `X-Aero-Tenant` 头、不触 context | `internal/auth/auth_middleware.go:68`/`:92`/`:124`/`:160` `req.Header.Set("X-Aero-Tenant", k.Tenant)`；header 与 key 租户不匹配 → 403（:64-66 等） | ✅ 精确：context 租户仅由 `TenantWithStatus` 写入 |
| E9 | `bucketFromPath` 仅解析 path（跨租户泄漏的结构前提） | `cors_bucket.go:205-218`：`/s3/<bucket>` → parts[1]；`/v1/files/*` → `"default"`；`/v1/buckets/<bucket>/*` → parts[2]；无任何租户成分 | ✅ 精确 |
| 补充 | `repo.SetBucketCORS`/`GetBucketCORS` 存在（验收测试可落地） | `internal/repository/repository_interface.go:75-76`；`sql_bucket_access.go:12`/`:35` | ✅ 方向验收可按原文实现 |
| 补充 | provider 缓存键 | `bucketKey{tenant, bucket}`（`cors_bucket.go:24-26`、:60） | 缓存**已按租户分键**，无跨租户缓存污染；缺陷纯在租户解析点，修复局部化 |

### 1.1 问题陈述逐条核验（当前状态）

| 陈述 | 核验 |
|------|------|
| "BucketCORS 在 Tenant 中间件之前执行（且早于 Auth）" | ✅ E1/E2：执行序 `request_id → cors_bucket → … → auth → tenant`，cors_bucket 比 tenant 早 9 环 |
| "`TenantFrom(r.Context())` 恒回落 `default`" | ✅ E3-E6：`ctxTenantID` 唯一写入点是 9 环之后的 `TenantWithStatus`（:22）；`TenantFrom` :52-53 回落 |
| "后果(1)：非默认租户的 CORS 规则永不生效" | ✅ 成立：BucketCORS 仅以 `("default", bucket)` 查询（E3+E5），`("acme", bucket)` 规则永不命中 |
| "后果(2)：`("default", bucket)` 规则对任意租户同名校请求生效 —— 跨租户信号泄漏" | ✅ 成立：租户恒 `"default"`（E5）、bucket 仅解析 path（E9）⇒ `("default","acme-bucket")` 规则被盖章到 acme 租户的 `/s3/acme-bucket/...` 响应上 |
| "现有测试无法捕获" | ✅ E7：三个测试均租户 `"default"` 且不跑 Tenant 中间件，只走回落路径 |
| "Auth 仅改写 header 不触 context" | ✅ E8 |

---

## 2. 现状、破坏面与边界

**缺陷路径（HEAD 实测链）：**

```
请求 GET /s3/acme-bucket/key  X-Aero-Tenant: acme  Origin: https://app.example.com
  → request_id → cors_bucket（此刻 ctxTenantID 未设置）
      tenant := TenantFrom(ctx) → "default"                    ← 缺陷点
      bucket := bucketFromPath → "acme-bucket"
      GetCORSRules("default", "acme-bucket")                   ← 查错租户
  → … → auth（改写 header 与否均不写 context）→ tenant（此时才写 ctxTenantID=acme，为时已晚）
```

| 影响面 | 结论 |
|--------|------|
| 多租户部署（header 显式） | 非默认租户的 per-bucket CORS **完全失效**（功能惰性）；`("default", bucket)` 规则被跨租户盖章 |
| 单租户部署（无 header） | **不可见**：`"default"` 即正确租户 —— 这正是现有测试全绿的原因 |
| 泄漏严重度 | `Access-Control-Allow-Origin` 无 `Access-Control-Allow-Credentials` 伴随 ⇒ 浏览器不能凭 cookie 跨租户读；但允许默认租户已配置来源的页面**跨租户读取响应体**（租户隔离破坏，属 bucket-policy/ACL 之外的旁路信号面） |
| 修复局部性 | 缓存键已含租户（补充核验）⇒ 缺陷仅在 `cors_bucket.go:155` 的租户解析；无需动 provider/repo |
| 约束 | 12 环链顺序被 `chain_test.go:26-38` + AGENTS.md I4 钉死；`cors_bucket` 环位不可移动（见 §3 R4） |

---

## 3. 行为要求（需求陈述）

> 实现方式自由（如：BucketCORS 直接按 `X-Aero-Tenant` 头解析、或在环内延迟到 tenant 可用后再取），但下列不变量必须同时成立：

- **R1（租户解析正确性）：** BucketCORS 解析出的租户必须与 Tenant 中间件语义一致：`X-Aero-Tenant` 头 → 缺省 `"default"`，且**不依赖环位置** —— 在 `cors_bucket` 当前环位（tenant 中间件之前 9 环）即可得到正确租户。
- **R2（查询键正确性）：** `GetCORSRules` 必须以 `(实际租户, bucket)` 查询；任何非缺省租户请求不得以 `"default"` 替代查询键。
- **R3（单租户回退保留）：** 无 `X-Aero-Tenant` 头的请求仍解析为 `"default"`（back-compat，AGENTS.md Tenant 契约）。
- **R4（链不变量）：** 12 环链的顺序、环名、`internal/server/chain.go` 装配点**不得改动**（`TestBuildChain_12RingsInOrder` 钉死 + I4）；修复只允许落在 `internal/middleware` 包内。
- **R5（缓存语义）：** provider 缓存键 `(tenant, bucket)` 不变；跨租户不得共享缓存条目。

---

## 4. 验收标准（方向三条原样保留 + 可执行映射）

> **回归性声明：** 按 AC-1 实现的测试在**当前 HEAD 必须失败**（这正是回归测试的意义 —— 现有代码会把 `("default","acme-bucket")` 的规则盖章到 acme 租户请求上，断言 "不得盖章" 即失败）；修复后转绿。

### AC-1 —— 新测试 `TestBucketCORS_TenantScopedRules`（`internal/middleware/cors_bucket_test.go`）

构建与生产一致顺序的链，验证租户作用域：

1. **链顺序与生产一致：** 方向原文 "build chain in the same order as cmd/server/http.go:143-169"。HEAD 上该装配点已迁移到 `internal/server/chain.go`（E1/E2），且 `internal/middleware` 包不得 import `internal/server`（循环依赖）。等价映射：`BucketCORS(provider)` 在外、`TenantWithStatus(nil)` 在内 —— 即 `h := BucketCORS(provider)(TenantWithStatus(nil)(next))`，与生产环序 `request_id → cors_bucket → … → tenant` 中 cors_bucket 先于 tenant 执行**逐环等价**（额外外层环 `request_id/cors/…` 不影响租户解析，无必要引入）。
2. **数据准备：** 复用现有 `newTestRepo(t)`（sqlite 临时库 + Migrate），写入：
   - `repo.SetBucketCORS(ctx, "acme", "acme-bucket", [{AllowedOrigins:["https://acme.example.com"], AllowedMethods:["GET"]}])`
   - `repo.SetBucketCORS(ctx, "default", "acme-bucket", [{AllowedOrigins:["https://leak.example.com"], AllowedMethods:["GET"]}])` ← 泄漏源
   - `provider := NewBucketCORSProvider(repo, time.Second)`（`t.Cleanup(provider.Close)`）
3. **断言 A（正向 · 功能恢复）：** `GET /s3/acme-bucket/key`，头 `X-Aero-Tenant: acme` + `Origin: https://acme.example.com` → 响应必须带 `Access-Control-Allow-Origin: https://acme.example.com`（`("acme","acme-bucket")` 规则生效）。HEAD 上失败（租户解析为 "default"）。
4. **断言 B（反向 · 泄漏关闭）：** 同请求但 `Origin: https://leak.example.com` → 响应**不得**带 `Access-Control-Allow-Origin`（`("default","acme-bucket")` 规则不得跨租户盖章）。HEAD 上失败（泄漏存在）。
5. **断言 C（单租户回退不回归）：** 无 `X-Aero-Tenant` 头 + `Origin: https://leak.example.com` → 仍须盖章（R3 回退语义保留）。

### AC-2 —— `go test ./internal/middleware/ -run TestBucketCORS` 通过

方向原文：*"go test ./internal/middleware/ -run TestBucketCORS passes after fix"*。原样保留。执行语义：新增测试（名称前缀 `TestBucketCORS`）+ 现有三个 `TestBucketCORS*` 测试（:28/:63/:88）全部通过；`go test ./internal/server/` 亦须通过（R4 链不变量未被破坏）。

### AC-3 —— `go vet ./... && go build ./...` 通过

方向原文原样保留。仓库硬门禁（AGENTS.md §0）同步适用：`gofmt -l` 无输出、`go test ./...` 全绿、单文件 ≤ 500 行（`cors_bucket.go` 现 225 行，余量充足）。

---

## 5. 范围外（明确不做）

- **不改动 12 环链**（顺序/环名/装配点，R4；`TestBuildChain_12RingsInOrder` 是形状契约）。
- **不处理残余排序限制：** 无 `X-Aero-Tenant` 头 + tenant-scoped key 认证的请求中，Auth（cors_bucket 之后 8 环）才改写 header —— 该场景下 BucketCORS 仍只能看到回落值。验收场景（显式 header 的匿名公读 `/s3` 路径）不受影响；此边界仅记录，不扩大修复。
- **不涉及** global CORS 回退逻辑、OPTIONS 预检行为、WebDAV 分发路径、`bucketFromPath` 的路径解析语义。
- **不处理**同分析文件中的另外两个方向（MaxBodySize 截断、Tenant header 校验）—— 各自独立跟踪。
