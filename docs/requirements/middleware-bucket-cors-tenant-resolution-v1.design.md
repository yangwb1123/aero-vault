# Design — `internal/middleware`: BucketCORS 按真实租户解析（per-bucket CORS 多租户修复）

**Module:** `internal/middleware`（`cors_bucket.go` · `tenant_status.go` · `middleware.go`）+ 测试 `cors_bucket_test.go`
**Source spec:** `docs/requirements/middleware-bucket-cors-tenant-resolution-v1.spec.md`（R1–R5，AC-1–AC-3）
**HEAD:** `acfaaf4` — 以下所有符号均在本 checkout 重新核验；行号随并发 campaign 的未提交改动可能漂移，契约以符号为准。
**Hard gates:** `make check`（= `fmt vet vet-integration build test cli-check`；单文件 ≤ 500 行；零新依赖；零迁移文件）。

---

## 0. Evidence verification（untrusted claims → 已核验 + 实证）

规格 §1 的九条引证与三项补充全部在 HEAD `acfaaf4` 复核：

| 规格引证 | 复核结果（HEAD `acfaaf4`） |
|---|---|
| 装配点迁移到 `internal/server/chain.go` | `BuildChain` :33-93、`ApplyMiddleware` :92-103；消费点 `cmd/server/main.go:166`；环序由 `chain_test.go` `wantRings` :26-38 钉死。**执行序 request_id → cors_bucket → … → auth → tenant**（cors_bucket 比 tenant 早 9 环）✅ |
| `cors_bucket.go:155` `tenant := TenantFrom(r.Context())` | ✅ 行号精确（:155） |
| `middleware.go:50` `TenantFrom` → `"default"` 回落 | ✅ `TenantFrom` :50，回落 :52-53 |
| `tenant_status.go` 唯一写 `ctxTenantID` | ✅ `TenantWithStatus` :22（行号漂移 18→22 如规格所述）；`ctxTenantID` 为包内未导出键，无其他写入者（grep 全仓确认） |
| 现有测试只用 `"default"`、不挂 Tenant 环 | ✅ `cors_bucket_test.go` :33/:96/:105，三测试均不设 `ctxTenantID`、无 `X-Aero-Tenant` 头 |
| Auth 只改写 header 不触 context | ✅ `internal/auth/auth_middleware.go:68/92/124/160` `req.Header.Set("X-Aero-Tenant", k.Tenant)`；header 与 key 不匹配 → 403 |
| `bucketFromPath` 仅解析 path | ✅ `cors_bucket.go:205-218`（`/s3/<bucket>`、`/v1/buckets/<b>`、`/v1/files/*`→`"default"`） |
| `GetBucketCORS`/`SetBucketCORS` 存在 | ✅ `repository_interface.go:75-76`、`sql_bucket_access.go:12/:35` |
| 缓存键含租户 | ✅ `bucketKey{tenant,bucket}` :24-26/:60；缺陷纯在 :155 解析点 |

**实证（回归探针，我本人在 HEAD 运行后删除）：** 按 AC-1 环等价结构 `BucketCORS(provider)(TenantWithStatus(nil)(next))` + `repo.SetBucketCORS` 写入 `("acme","acme-bucket")→[acme.example.com]` 与 `("default","acme-bucket")→[leak.example.com]`，实测：

```
A: X-Aero-Tenant: acme + Origin https://acme.example.com  → Allow-Origin=""            ← 断言 A 失败（acme 规则不生效）
B: X-Aero-Tenant: acme + Origin https://leak.example.com  → Allow-Origin="https://leak.example.com" ← 断言 B 失败（默认租户规则跨租户盖章）
C: 无头 + Origin https://leak.example.com                 → Allow-Origin="https://leak.example.com" ← 断言 C 通过（回落保留）
```

→ 验收映射**具区分度**：AC-1 在当前 HEAD 必失败（回归证明），修复后转绿。

**并发工作区注意：** `internal/middleware/middleware_test.go` 有并发 campaign 的未提交追加（`TestTenantWithStatus_BypassTable`，纯增量、与 CORS 无关）；`cmd/server/*`、`Makefile` 等亦有未提交改动。本设计不触碰这些文件（`chain.go`/`chain_test.go` 工作区干净）。

---

## 1. Design overview

**修复思路：** `BucketCORS` 环不再从 context 取租户（该值在 9 环之后才被 `TenantWithStatus` 写入），改为**按请求自解析**，且与 Tenant 环共用同一解析语义（单一事实源）：

1. 新增包内辅助函数 `tenantFromRequest(r)`：`X-Aero-Tenant` 头 → 缺省 `"default"` —— 与 `TenantWithStatus` 现行逻辑逐字符等价；
2. `TenantWithStatus` 改为调用该辅助（行为零变化，防止两处语义日后漂移 —— 这正是本缺陷的根因类别）；
3. `BucketCORS` 改为 **ctx 优先、请求头回退**：`TenantFromContext` 命中则用已解析租户（若未来链重排使 Tenant 环提前，自动取权威值），否则 `tenantFromRequest(r)`（当前环位即取正确租户）。

**不变量满足（R1–R5）：** R1 ✓ 解析不依赖环位置（ctx 优先 + 头回退，两种链形态都正确）；R2 ✓ 以 `(实际租户, bucket)` 查询；R3 ✓ 无头请求仍解析 `"default"`（探针断言 C 实证）；R4 ✓ `internal/server/chain.go` 与 12 环零改动；R5 ✓ 缓存键 `(tenant,bucket)` 不变、无跨租户共享。

**附带修复（同一缺陷面）：** REST PUT/DELETE bucket CORS 的失效化调用 `InvalidateBucket(ctx, mw.TenantFrom(ctx), bucket)`（`api/rest/bucket_handlers.go:90/:103`，Tenant 环之后执行 → 真实租户）——修复前查找以 `("default",bucket)` 入缓存、失效以真实租户删键，**两者永不相交**：默认租户键的缓存条目永不失效，规则变更后最多 60 s TTL 陈旧。修复后键对齐，失效化首次真正生效。这是同一点修复的正面副作用，非新范围。

---

## 2. API changes

**对外 API / 接口 / 配置 / schema：零变更。** 无新增依赖（I6）、无迁移文件（I2）、无 config 键。`BucketCORSProvider` 接口、`bucketCORSProvider` 缓存、`TenantWithStatus` 签名、`BuildChain`/`ApplyMiddleware` 签名均不动（R4）。

### 2.1 `internal/middleware/middleware.go` — 新增包内辅助（`TenantFrom` 之后，~:55）

```go
// tenantFromRequest resolves the tenant exactly as the Tenant ring does: the
// X-Aero-Tenant header, falling back to "default". BucketCORS runs nine rings
// before Tenant, so it must resolve from the request, not from the context;
// sharing this helper keeps both resolutions from drifting apart.
func tenantFromRequest(r *http.Request) string {
	if t := r.Header.Get(TenantHeader); t != "" {
		return t
	}
	return "default"
}
```

（`TenantHeader = "X-Aero-Tenant"` 常量 :25 已有，由 `TestTenantHeaderConstant` 钉死。）

### 2.2 `internal/middleware/tenant_status.go` — `TenantWithStatus` 复用辅助（行为零变化）

`:18-20` 三行（`tenant := r.Header.Get(TenantHeader); if tenant == "" { tenant = "default" }`）替换为 `tenant := tenantFromRequest(r)`。语义逐字符等价 —— 纯去重重构，防止两处解析语义漂移。

### 2.3 `internal/middleware/cors_bucket.go` — `BucketCORS` 解析点修复（:155）

```go
tenant, ok := TenantFromContext(r.Context())
if !ok {
	tenant = tenantFromRequest(r)
}
```

替换 `tenant := TenantFrom(r.Context())`。当前环位（ctx 未设置）→ 头回退 = 正确租户；若链未来重排使 Tenant 先执行 → ctx 权威值优先（含 Auth 改写后的租户），R1 的字面实现。

---

## 3. Compatibility constraints

| 场景 | 修复前 | 修复后 | 兼容性结论 |
|---|---|---|---|
| 单租户部署（无 `X-Aero-Tenant` 头） | `("default",bucket)` 规则生效 | 同左（`tenantFromRequest` 回落） | **零变化**（探针断言 C 实证） |
| 多租户显式头 | 非默认租户规则惰性；`("default",bucket)` 跨租户盖章 | 各自租户规则生效；跨租户盖章停止 | **预期修复**；需 release note（见 §5） |
| OPTIONS 预检 / `/s3`、`/v1/buckets`、`/v1/files` | — | 同一解析路径 | 无新增分支 |
| WebDAV `/webdav/*`（`bucketFromPath` → `""` → `"default"` bucket） | 恒以 `("default","default")` 查询 | 租户按头解析、bucket 仍 `"default"` | 与 /s3 同向的预期行为变化；单租户不变 |
| 匿名公读 + 无头 | `"default"` | `"default"` | 零变化 |
| 无头 + tenant-scoped key | `"default"`（Auth 在 8 环后改写 header） | 同左 | **残余边界**，规格 §5 明示不扩大修复，见 §8 |
| 缓存失效化（REST PUT/DELETE CORS） | 失效键 `(真实租户,bucket)` 与查找键 `("default",bucket)` 永不交集 → 陈旧至 TTL | 键对齐 → 失效即生效 | 附带修复（见 §1） |
| 链形状 | — | — | `chain_test.go`/`TestBuildChain_12RingsInOrder` 零改动通过（R4） |

**行为契约复核：** `TenantFromContext` 对 `""` 值返回 `!ok`（`middleware.go:60-63`）→ 空头值落入 `tenantFromRequest` 的 `"default"` 分支，与 `TenantWithStatus` 完全一致；多头值场景 `Header.Get` 取首个，两处一致。

---

## 4. Failure modes

| # | 场景 | 行为 | 评估 |
|---|---|---|---|
| F1 | 已禁用租户的请求命中其 CORS 规则 | BucketCORS 盖章后，8 环后 `TenantWithStatus` 返回 403；浏览器拿不到 2xx 响应/预检 → 无跨租户读取 | 无泄漏；与修复前同构（盖章在鉴权拒绝之前本就存在） |
| F2 | provider/repo 错误 | `GetCORSRules` err → 跳过盖章，回落 global CORS（`cors.go`） | 与修复前一致（降级路径不变） |
| F3 | 大量租户 × bucket 触发缓存增长 | 缓存键从 `1×bucket` 扩到 `租户×bucket`；有 60 s TTL + `evictLoop` 定期清理（`cors_bucket.go:104-117`），条目仅由带 Origin 的请求驱动，且全局/每租户限流环在前 | 内存包络有界；记录为残余项（多租户下条目数随活跃租户线性增长，60 s 过期） |
| F4 | 畸形/超长 header 值 | 与 `TenantWithStatus` 现行接受面一致（`Header.Get`），进缓存键字符串；服务器级 header 大小限制兜底 | 无新增放大：修复前恒 `"default"`，修复后每租户一键，同为 TTL 淘汰 |
| F5 | 并发 `SetBucketCORS` 与读取竞争 | 缓存语义不变（R5），`InvalidateBucket` 幂等删键 | 与修复前一致 |
| F6 | S3 适配器 PUT/DELETE bucket CORS 不调 `InvalidateBucket` | 规则变更后最多 60 s 陈旧（TTL 自愈） | **既有缺陷，与本方向正交**，明确超范围（§8），不扩大修复 |

---

## 5. Migration steps

**代码/数据迁移：无。** 无 schema、无 config、无 API 变更；缓存为进程内存（60 s TTL），重启即清空，无需预热。

**运维发布说明（多租户部署必读）：** 部署本修复后，`("default", bucket)` 的 CORS 规则**不再**作用于其他租户的同名 bucket 响应；各租户须配置自己的规则（S3 `PUT ?cors` / REST `PUT /v1/buckets/{b}/cors`）。这正是缺陷修复本身 —— 属预期的安全行为变更，写入 release notes；单租户部署无感知。

---

## 6. Testable acceptance mapping

### AC-1 → `TestBucketCORS_TenantScopedRules`（`internal/middleware/cors_bucket_test.go`，新增）

结构与规格 §4 AC-1 一致（`internal/middleware` 不得 import `internal/server`，环等价映射：`h := BucketCORS(provider)(TenantWithStatus(nil)(next))`）：

```go
func TestBucketCORS_TenantScopedRules(t *testing.T) {
	repo := newTestRepo(t) // 既有 helper（sqlite + Migrate）
	ctx := context.Background()
	// 数据准备：租户规则 + 泄漏源规则（同 bucket 名、不同租户）
	_ = repo.SetBucketCORS(ctx, "acme", "acme-bucket", []repository.CORSRule{
		{AllowedOrigins: []string{"https://acme.example.com"}, AllowedMethods: []string{"GET"}},
	})
	_ = repo.SetBucketCORS(ctx, "default", "acme-bucket", []repository.CORSRule{
		{AllowedOrigins: []string{"https://leak.example.com"}, AllowedMethods: []string{"GET"}},
	})
	provider := NewBucketCORSProvider(repo, time.Second)
	t.Cleanup(provider.Close)

	h := BucketCORS(provider)(TenantWithStatus(nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	// 断言 A（正向·功能恢复）：acme 租户规则必须盖章。
	// 断言 B（反向·泄漏关闭）：默认租户规则不得跨租户盖章。
	// 断言 C（单租户回退不回归）：无头请求仍命中 ("default","acme-bucket") 规则。
}
```

三断言**均已实证区分度**（§0 探针：HEAD 上 A、B 失败、C 通过；修复后全绿）。断言 A/B 显式注释为 "must-fail on current HEAD"（回归证明）。

### AC-2 → `go test ./internal/middleware/ -run TestBucketCORS`

现有三个 `TestBucketCORS*`（:28/:63/:88，无头路径）修复前后均通过（行为不变）；新增测试前缀 `TestBucketCORS` 纳入同一过滤。另须 `go test ./internal/server/`（`TestBuildChain_12RingsInOrder` 等，R4 链不变量）与 `go test ./internal/integration/ -run TestFullServer`（harness 传 nil provider → BucketCORS 直通，零影响）。

### AC-3 → 仓库硬门禁

`gofmt -l` 无输出 · `go vet ./...` · `go build ./...` · `make check` 全绿（`go test ./...` 含上）。文件行数预算：`cors_bucket.go` 225 → ~230；`middleware.go` 298 → ~306；`tenant_status.go` 55（净 0）；`cors_bucket_test.go` 120 → ~195 —— 均 ≪ 500 ✅。

---

## 7. Previous-attempts findings disposition（gate 将复查）

| 来源 | 状态 | 处置（附证据） |
|---|---|---|
| **本管线** `docs/auto/runs/bucketcors-resolves-tenant-as-default-for-every--31b43d4f/` | DECISIONS.md 仅 `requirements` PASS（2026-08-06 22:35）；**尚无 design-gate 判定**（本阶段即 design） | 规格本身即验收契约，无遗留 finding；§0 逐条复核 + §6 探针实证。无未决项 |
| **兄弟 run** `bucket-policy-parsing-fails-open-on-invalid-effe-9edc16cb`（design-gate **FAIL**）：P1-1/2/3a/3b/3c、F1（Action 字段 fail-open）、F2（REST ungated endpoints）、F3（policy body 无界）、F5（garbage Principal） | 全部已被兄弟 run `enforce-bucket-policy-resource-constraints-on-th-462e998e` 的 design-gate（**PASS**，2026-08-06 18:26）逐项处置：P1-1/2/3a ✅ Resolved（`TestBucketPolicyRejectsInvalidEffectAndPrincipal` 等）；P1-3b/3c ⏭ 代码核验无分歧；F1/F5 ⏭ policy 层缺陷、FR-4 禁触 `policy.go`、探针复现并记录；F2 ⏭ s3compat 全路由 gated vs REST 7 条、规格 §5 排除、记录为已知局限 + 后续方向 | **与本方向无交集**：F1/F2/F3/F5 全部位于 `internal/auth/policy.go` 与 REST 适配器，本设计改动文件集 = `internal/middleware/{cors_bucket,tenant_status,middleware,cors_bucket_test}.go`，零重叠；引用兄弟 run 的处置结论（其上 verdict: PASS），本设计不重新打开 |
| **兄弟 run** `add-missing-tests-for-the-two-security-critical--47789066`（adversarial_review "agent exited 1"） | 失败原因为 runner 超时（infra），非 finding；其设计中的唯一观察是 MCP tenant-boundary 修复仅存在于工作区（`internal/service/access.go`） | 属 MCP 方向、`internal/service` 模块，与本方向无关；由该 campaign 自身跟踪 |
| **工作区并发改动**（`middleware_test.go` +47 行 `TestTenantWithStatus_BypassTable`、`cmd/server/*` 等未提交） | 其他 campaign 在途 | 本设计不触碰这些文件/区域；行号以符号为准，防漂移 |
| 分析文件其余两个方向（MaxBodySize 截断、Tenant header 校验） | 规格 §5 明示各自独立跟踪 | 本设计不涉及 |

---

## 8. Out of scope（明确不做）

- **12 环链零改动**（R4）：不重排、不改名、不动 `internal/server/chain.go`。
- **残余排序边界**（规格 §5 原样保留）：无 `X-Aero-Tenant` 头 + tenant-scoped key 的请求，Auth（cors_bucket 后 8 环）才改写 header —— 该场景 BucketCORS 仍解析 `"default"`。文档化边界，不扩大修复。
- **S3 适配器 CORS 写路径不失效化缓存**（F6）：既有 60 s TTL 自愈陈旧，正交缺陷，不纳入。
- **global CORS 回退 / OPTIONS 预检语义 / `bucketFromPath` 解析 / WebDAV 分发路径**：均不动。
- **分析文件另两个方向**（MaxBodySize、Tenant header 校验）：独立跟踪。
