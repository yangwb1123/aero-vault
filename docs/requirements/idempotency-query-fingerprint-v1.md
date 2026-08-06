# 方向：Idempotency 指纹忽略 query string —— `DELETE ?hard=1` 与软删除共享指纹

> **模块：** `internal/api`（`internal/api/rest`） · **来源分析：** `docs/auto/analyses/internal-api-a0abd005.json` · **日期：** 2026-08-06
> **评分：** 价值 8 / 风险降低 8 / 工作量 2 / 置信度 9
> **本文所有代码引用均已对照仓库逐行验证**（行号以当前 HEAD 为准）。

---

## 1. 问题陈述

`/v1` 幂等指纹 `fingerprint()` 仅对 `r.Method + " " + r.URL.Path` 取 SHA-256，**`RawQuery` 被排除**。因此：

- `DELETE /v1/files/x`（软删除）
- `DELETE /v1/files/x?hard=1`（硬删除，`handler.go:244`）

在同一个 `(tenant, Idempotency-Key)` 下产生**完全相同的指纹**。客户端先以某 key 发起软删除、再以同一 key 重试硬删除时，中间件会把软删除的响应（204）**逐字回放**（`Idempotency-Replayed: true`），`svc.Delete(hard=true)` 永远不会被调用，对象不会被硬删除，而客户端认为破坏性操作已成功。

硬删除是数据生命周期/合规相邻操作（清除 tombstone 与存储 blob，受 retention lock 约束，见 `file_delete.go:94-95`），静默跳过即数据生命周期正确性缺陷，而非纯协议怪癖。现有测试（`idempotency_test.go`）只覆盖 method/path/body 三种变化，**无 query 变化用例**，缺陷无测试保护。

### 触发场景（真实工作流）

1. 运维/合规流程先对对象做软删除（`DELETE /v1/files/x`，key `K`）。
2. 数小时后同一流程用同一 key `K` 重试 `DELETE /v1/files/x?hard=1`（客户端按幂等键重试破坏性操作是 Stripe 风格幂等语义的典型用法）。
3. 中间件判定 `rec.Fingerprint == fp` 且 `completed` → 回放 204，声称成功。
4. 对象永远停留在软删除状态；tombstone 与 blob 保留；若该 key 的请求随后被 Reconcile 生命周期策略清理，行为取决于 GC 配置，但**本次硬删除动作本身已静默丢失**。

---

## 2. 现状与代码证据（已逐条验证）

| # | 证据 | 验证结果 |
|---|------|---------|
| E1 | `internal/api/rest/idempotency.go:172-174` — `fingerprint()` 仅哈希 `r.Method + " " + r.URL.Path`，`RawQuery` 排除 | ✅ 与引用一致（:173 为哈希行） |
| E2 | `internal/api/rest/idempotency.go:180-182` — `bodyFingerprint()` 仅追加 bodyHash，query 同样排除 | ✅ 与引用一致（:181 为哈希行） |
| E3 | `internal/api/rest/handler.go:244-245` — `hard := r.URL.Query().Get("hard") == "1"` → `h.svc.Delete(..., hard)` | ✅ 与引用一致（244 读参数、245 传递） |
| E4 | `internal/api/rest/router.go:236-244` — idempotency 中间件组包裹 `r.Delete("/files/*", h.deleteKey)`（`r.Use(idempotency(repo, logger, idemHashBody))` 在 :238；`r.Delete` 在 :243） | ✅ 与引用一致 |
| E5 | `internal/api/rest/router.go:445-455` — `deleteKey` 分派：非 `/tags`、非 `/metadata` 子路径一律走 `h.Delete`（`?hard=1` 命中默认分支） | ✅ 补充验证 |
| E6 | `internal/api/rest/idempotency_test.go` — 13 个测试仅变化 method/path/body：`TestIdempotency_DifferentRequestConflict`(:105) 变 path；`TestIdempotencyHashBody_DifferentBodyConflict`(:239) 变 body；**无 query 变化用例** | ✅ 与引用一致 |
| E7 | `internal/service/file_delete.go:96-120` — `Delete(ctx, tenant, bucket, key string, hard bool)`；`hard=true` → `hardDeleteObject`（删存储 blob + `repo.HardDeleteObject` 清行，:16-50）；`hard=false` → `softDeleteObject`（`repo.SoftDeleteObject` 置 `deleted_at` 留 tombstone，:74-92） | ✅ 补充验证：软/硬删可观测差异成立 |
| E8 | `internal/config/config.go:243` — `IDEMPOTENCY_HASH_BODY` 默认 `false` | ✅ 补充验证 |
| E9 | 既有 `?hard=1` 处理器级测试先例：`internal/api/rest/management_test.go:131-134`（`req(t, "DELETE", putURL+"?hard=1", nil, nil)` 断言 409） | ✅ 补充验证：handler 级 `?hard=1` 测试路径已确立 |

### 缺陷机理（两种指纹模式均命中）

- **默认模式（`hashBody=false`）：** 指纹 = method+path。`DELETE /v1/files/x` 与 `DELETE /v1/files/x?hard=1` 完全相等 → `handleIdempotentRequest`（`idempotency.go:223-241`）走 `rec.Fingerprint == fp && completed` 分支 → 回放。
- **`IDEMPOTENCY_HASH_BODY=true` 模式：** DELETE 无 body（空字节串），两次请求的 bodyHash 相同 → `bodyFingerprint` 仍相等 → 同样回放。**修复必须同时覆盖两个指纹函数**，否则仅修 `fingerprint()` 会在开启 body hash 时残留缺陷。

### 影响链

```
DELETE ?hard=1 被幂等回放为软删除 204
  → svc.Delete(hard=true) 从不执行
  → tombstone + storage blob 残留（合规清除失败）
  → 客户端无任何错误信号（204 + Idempotency-Replayed: true）
  → 数据生命周期策略静默失效
```

---

## 3. 需求规格

### FR-1：指纹包含 query string（缺陷修复）

`fingerprint(r)` 与 `bodyFingerprint(r, bodyHash)` 的哈希输入必须包含请求的 query string（`r.URL.RawQuery`），使**不同 query 的写请求产生不同指纹**。

- 约束 a：**相同请求（含相同 query）指纹必须保持稳定** —— 既有重放语义（同 key 同请求回放原始响应）不得破坏。
- 约束 b：两个指纹函数**同时**修复（见缺陷机理）；`bodyFingerprint` 的输入变为 `method + path + RawQuery + bodyHash`。
- 约束 c：仅用 `RawQuery` 原始串参与哈希即可；**query 参数规范化/排序不在本方向范围**（见 §5）。
- 约束 d：键空间 `(tenant, Idempotency-Key)` 与 claim/complete/replay 流程（`ClaimIdempotencyKey` / `CompleteIdempotencyKey` / `DeleteIdempotencyKey`）**不变**；只改指纹计算。

### FR-2：语义回归保护

- 同 key 同 URL（含同 query）重试 → 仍回放原始响应（`Idempotency-Replayed: true`，204）。
- 同 key 不同 query → **409 `IdempotencyConflict`**（"Idempotency-Key reused for a different request"），绝不回放、绝不静默吞掉硬删除。
- `?hard=1` 使用**新 key** 时 → 正常执行硬删除（204），对象行与 blob 均被清除。

### 非功能约束

- 单文件 ≤ 500 行、`gofmt`/`go vet`/`go test` 全绿（AGENTS.md §0 硬门禁）；改动预计仅 2 行生产代码 + 新增测试，无新依赖（I6）。
- 不触碰 adapter/handler 层的 key 校验边界（I3）、中间件链顺序（I4）——本方向仅限 `internal/api/rest` 幂等指纹计算。

---

## 4. 验收标准（可测试，遵循既有测试基建）

> 测试基建（已验证）：中间件级测试用 `idempotency(repo, idemSilentLogger(), false)(h)` + `httptest.NewRequest`（`idempotency_test.go:40-82` 模式）；处理器级测试用 `setupTest(t)` 的 `httptest.NewServer(router)` + `req(t, method, url, body, hdr)`（`handlers_test.go:25-60`、`conditional_test.go:44`）；`idemTestRepo`（`idempotency_test.go:25-38`）提供 SQLite 仓库。

### AC-1 单元测试：指纹对 query 敏感（`internal/api/rest/idempotency_test.go` 新增）

```go
// TestIdempotency_FingerprintIncludesQuery: 指纹必须区分 ?hard=1，且相同请求指纹稳定。
func TestIdempotency_FingerprintIncludesQuery(t *testing.T) {
	soft := httptest.NewRequest(http.MethodDelete, "/v1/files/x", nil)
	hard := httptest.NewRequest(http.MethodDelete, "/v1/files/x?hard=1", nil)
	if fingerprint(soft) == fingerprint(hard) {
		t.Fatal("fingerprint must distinguish DELETE /v1/files/x from DELETE /v1/files/x?hard=1")
	}
	if fingerprint(soft) != fingerprint(httptest.NewRequest(http.MethodDelete, "/v1/files/x", nil)) {
		t.Fatal("identical requests must produce identical fingerprints (replay stability)")
	}
	if fingerprint(hard) != fingerprint(httptest.NewRequest(http.MethodDelete, "/v1/files/x?hard=1", nil)) {
		t.Fatal("identical query strings must produce identical fingerprints")
	}
	// bodyFingerprint 同样必须区分（IDEMPOTENCY_HASH_BODY=true 路径）
	const bh = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" // sha256("")
	if bodyFingerprint(soft, bh) == bodyFingerprint(hard, bh) {
		t.Fatal("bodyFingerprint must also distinguish the two URLs")
	}
}
```

### AC-2 中间件级行为测试：同 key 软删后重试 `?hard=1` → 409，不重放、不重跑（`idempotency_test.go` 新增，仿 `TestIdempotency_DifferentRequestConflict` :105）

```go
// TestIdempotency_QueryVariantConflicts: 同一 Idempotency-Key 下 query 不同的
// 请求必须 409，而不是回放；同 query 重试仍回放。
func TestIdempotency_QueryVariantConflicts(t *testing.T) {
	repo := idemTestRepo(t)
	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent) // 204，与 Delete handler 一致
	})
	mwh := idempotency(repo, idemSilentLogger(), false)(h)

	do := func(url string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, url, nil)
		req.Header.Set("Idempotency-Key", "shared")
		rr := httptest.NewRecorder()
		mwh.ServeHTTP(rr, req)
		return rr
	}

	rr1 := do("/v1/files/x")          // 软删除
	rr2 := do("/v1/files/x?hard=1")   // 硬删除重试 → 必须 409，不得回放
	rr3 := do("/v1/files/x")          // 完全相同重试 → 仍回放（回归保护）

	if rr1.Code != http.StatusNoContent {
		t.Fatalf("first delete: status=%d want 204", rr1.Code)
	}
	if rr2.Code != http.StatusConflict {
		t.Fatalf("query variant must be 409 Conflict, got %d", rr2.Code)
	}
	if !strings.Contains(rr2.Body.String(), "IdempotencyConflict") {
		t.Fatalf("409 must carry code IdempotencyConflict, body=%s", rr2.Body.String())
	}
	if rr2.Header().Get("Idempotency-Replayed") == "true" {
		t.Fatal("query variant must not replay the soft-delete response")
	}
	if calls != 2 {
		t.Fatalf("handler must run for req1 and req3 only (calls=%d, want 2)", calls)
	}
	if rr3.Code != http.StatusNoContent || rr3.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("identical retry must still replay (status 204, Idempotency-Replayed: true)")
	}
}
```

### AC-3 处理器级端到端测试：新 key 下 `?hard=1` 可验证地硬删除（`handlers_test.go` 或新文件，仿 `management_test.go:131-134`）

```go
// TestIdempotency_HardDeleteWithFreshKey: ?hard=1 使用新 key 时真正执行硬删除
// （204），对象行与存储 blob 均被清除——软删会留下 tombstone 行，硬删不会。
func TestIdempotency_HardDeleteWithFreshKey(t *testing.T) {
	svc, repo, ts := setupTest(t)
	ctx := context.Background()

	if resp, _ := req(t, "PUT", ts.URL+"/files/x", []byte("data"), nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT: status=%d want 201", resp.StatusCode)
	}
	// 新 key 硬删除
	resp, _ := req(t, "DELETE", ts.URL+"/files/x?hard=1", nil,
		map[string]string{"Idempotency-Key": "purge-1"})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("hard delete: status=%d want 204", resp.StatusCode)
	}
	// 可观测验证：行被清除（软删会保留 deleted_at tombstone）
	versions, err := repo.ListObjectVersions(ctx, "default", "default", "x")
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 0 {
		t.Fatalf("hard delete must purge all rows, got %d versions (soft delete leaves a tombstone)", len(versions))
	}
	if _, _, err := svc.Get(ctx, "default", "default", "x"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("object must be gone after hard delete, err=%v", err)
	}
}
```

> 对照断言：若将第二步换成 `DELETE /v1/files/x`（软删），`ListObjectVersions` 应返回 ≥1 行 tombstone（`SoftDeleteObject` 置 `deleted_at`，`sql_objects_maint.go:20-38`）——AC-3 断言 `len(versions) == 0` 正是软/硬删差异点，确保断言的是**硬删**而非"任意删除"。

### 既有测试回归

- `TestIdempotency_ReplaysOnRetry`（:40）、`TestIdempotency_DifferentRequestConflict`（:105）、`TestIdempotencyHashBody_*`（:204-467）必须全绿——修复不得改变 method/path/body 维度语义。

---

## 5. 范围边界（明确不做）

| 不做 | 理由 |
|------|------|
| query 参数规范化/排序（`a=1&b=2` vs `b=2&a=1` 视为不同请求） | 超出本方向；`RawQuery` 原文哈希已消除硬删除静默丢失；规范化属独立设计决策 |
| 多分片上传四端点无幂等保护（`/multipart/*` 在 idempotency group 外，`router.go:246-249`） | 独立方向（已见于 `docs/requirements/deep-production-gaps-v1.md` 方向四），非本缺陷 |
| `/legal-hold` 端点（`router.go:320-322`，`?key`/`?versionId` 传参）无幂等保护 | 同样在 idempotency group 之外，不受指纹缺陷影响；是否纳入幂等属另一方向 |
| `?version=` 查询（仅 GET 路径使用，`handler.go:179`） | idempotency 对读请求惰性（`isWriteMethod`），无影响 |
| 幂等存储 schema / claim 流程变更 | 本方向仅改指纹计算（FR-1 约束 d） |

---

## 6. 实现指引（供验收后落地，非本规格交付物）

- `idempotency.go:173`：`[]byte(r.Method + " " + r.URL.Path + " " + r.URL.RawQuery)`
- `idempotency.go:181`：`[]byte(r.Method + " " + r.URL.Path + " " + r.URL.RawQuery + " " + bodyHash)`
- 新增测试按 §4 三组验收落地；跑 `make check`（`gofmt` / `go build` / `go vet` / `go test ./...`）确认全绿。
