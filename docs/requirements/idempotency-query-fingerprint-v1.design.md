# 设计：Idempotency 指纹纳入 query string —— 配套设计文档 v1

> **配套规格：** `docs/requirements/idempotency-query-fingerprint-v1.md` · **模块：** `internal/api/rest` · **状态：** 已实现，`make check` 全绿（exit=0）· **日期：** 2026-08-06
> 本文是规格的落地设计：证据复核结论（含 3 处修正）、API 变更、兼容性约束、失败模式、迁移步骤、验收映射。

---

## 0. 证据复核结论（对规格逐条核验）

规格全部代码引用（E1–E9）与仓库逐行一致，无需修正；**复核发现规格有 3 处不准确/不完整**：

| # | 规格原文 | 复核结论 |
|---|---------|---------|
| C1 | §5 范围守卫："`?hard=1` is the only query-sensitive *write* inside the idempotency group" | **不完整。** 组内 query 敏感写路径实际有 **3 条**：① `handler.go:244` `DELETE ?hard=1`；② `presign.go:18-22` `POST /files/*/presign?op=&expires=`（经 `postKey` 分派，在组内 `router.go:240`）；③ `handler_metadata.go:63` `DELETE /files/*/metadata?key=`（经 `deleteKey` 分派，在组内 `router.go:243`）。**影响：** 本修复的实际覆盖面大于规格声称——预签名同 key 下 `?op=put` 与 `?op=get` 不再互相回放（旧行为会回放过期/错误 op 的 URL），metadata 定点删除与全量清除不再混淆；三处均由同一指纹修复覆盖，无需额外改动 |
| C2 | AC-2 断言 `calls != 2`（"handler must run for req1 and req3 only"） | **自相矛盾。** req3 是回放（`handleIdempotentRequest` 回放分支在 `next.ServeHTTP` 之前 return），回放**不**重跑 handler；既有 `TestIdempotency_ReplaysOnRetry` 断言同 key 两次请求 `calls == 1`。已修正为 `calls != 1`（更强：回放与冲突均不得重跑 handler） |
| C3 | §6 实现指引：无条件追加 `+ " " + RawQuery` | **改为条件追加**（query 非空才拼入 `?`+RawQuery），理由见 §2。AC-1..3 在两种实现下均通过，验收映射不变 |

其余引用（fingerprint :172-174、bodyFingerprint :180-182、handler.go:244-245、router.go:238/243/445-455、13 个既有测试无 query 用例、config.go:243 默认 false、management_test.go:131-134 先例、软/硬删可观测差异）全部 ✅ 与仓库一致。

---

## 1. API 变更

**无公开 API 变更：** 路由、参数、响应码定义、`idempotency_keys` schema、配置项全部不变（FR-1 约束 d）。变更面仅 `internal/api/rest/idempotency.go` 的指纹计算。

**行为变更（幂等语义，/v1 REST 写请求携带 `Idempotency-Key` 时）：**

| 场景 | 旧行为 | 新行为 |
|------|--------|--------|
| 同 key 软删后重试 `DELETE ?hard=1` | 回放软删 204（`Idempotency-Replayed: true`），`svc.Delete(hard=true)` 从不执行 | **409 `IdempotencyConflict`**，不回放 |
| 同 key 同 URL（含同 query）重试 | 回放原始响应 | 回放原始响应（不变） |
| `?hard=1` 使用新 key | 执行硬删 204 | 执行硬删 204（不变，AC-3 验证） |
| 同 key `POST /presign` 的 `?op=`/`?expires=` 变化 | 回放旧 URL | 409（附带修复，见 C1） |
| 同 key `DELETE /metadata` 的 `?key=` 变化 | 回放旧响应 | 409（附带修复，见 C1） |

---

## 2. 设计决策：条件包含 vs 无条件包含 query

规格 §6 建议无条件 `method + " " + path + " " + RawQuery`。本设计采用**条件包含**：query 为空时指纹与旧格式**逐字节一致**（`sha256(method + " " + path)`），非空时拼入 `"?" + RawQuery`。

**理由：**

1. **跨部署去重保证不中断（决定性）。** 无条件包含会使**所有**部署前 claimed 的 key 在部署后重试一律 409（query 为空时新旧指纹也因尾随空格不同而不等）——包括占绝对多数的 PUT/POST 上传与普通删除。客户端被迫换新 key 重试 → 原请求已完成时产生**重复版本**（幂等本要防的正是这个）。条件包含下，query-less 重试继续命中存量记录回放。
2. **漏洞类对存量记录同样关闭。** 存量软删记录（fp=sha256(M+P)）与新硬删请求（fp=sha256(M+P?hard=1)）永不相等 → 409。规格描述的"软删被回放为硬删成功"在部署后对旧记录也成立地不可复现。
3. **残余边角在安全侧。** 条件包含唯一残余：存量**硬删**记录被 query-less 软删重试匹配（回放旧 204）。此时对象本就被客户端自己的请求硬删了，损失方向是"软删语义落空"（无害），不是合规相关的"硬删丢失"（规格修复的正是后者）。窗口由 TTL GC 界定（§4 FM-3）。
4. **符合仓库既有先例。** `docs/configuration.md:367` 已记载指纹变更的运维模型："keys claimed before the flip will `409` on retry until they expire (`IDEMPOTENCY_TTL_HOURS`) or new keys are used"——条件包含把该窗口收窄到仅 query 变体一类。

**拒绝无条件包含**：无残余边角，但把 §2.1 的代价施加到全部存量 key（含滚动部署中 in-flight 的 query-less 重试），且与仓库既有先例不一致。

**不引入配置开关**（如 `IDEMPOTENCY_HASH_QUERY`）：缺陷修复必须默认生效；回滚开关等于静默恢复数据生命周期缺陷。与 `IDEMPOTENCY_HASH_BODY`（v2 body 指纹）不同——后者有真实成本（body 缓冲/散列）且是语义扩展，本修复零成本、纯正确性。

---

## 3. 兼容性约束

| 维度 | 约束 |
|------|------|
| 指纹格式 | query 为空 → 与旧格式逐字节一致；query 非空 → `?`+`RawQuery` 原文（不规范化、不排序、不解码，FR-1 约束 c） |
| 存量记录 | query-less 记录继续回放；query 变体记录对一切新指纹不匹配 → 409 fail-closed |
| 键空间/流程 | `(tenant, Idempotency-Key)`、`ClaimIdempotencyKey`/`CompleteIdempotencyKey`/`DeleteIdempotencyKey` 不变（FR-1 约束 d） |
| 中间件链 | 不触碰（I4）；handler 不自挂链 |
| key 校验边界 | 不触碰（I3） |
| 其他适配器 | S3/WebDAV/MCP 无 Idempotency-Key，不受影响；GET/HEAD 惰性（`isWriteMethod`）不受影响 |
| 组外端点 | `/multipart/*`、`/legal-hold` 仍在幂等组外（规格 §5 既定边界） |

---

## 4. 失败模式

| # | 模式 | 行为 | 处置/缓解 |
|---|------|------|----------|
| FM-1 | 部署窗口内，存量 key 的 **query 变体**重试 | 409 `IdempotencyConflict`（fail-closed，绝不错误回放） | 与 `configuration.md:367` 既有先例一致；`IDEMPOTENCY_TTL_HOURS > 0` 界定窗口；可选部署前清表（§5 步骤 3） |
| FM-2 | 滚动部署中 in-flight 的 query 变体 claim 被新代码重试 | 409（消息为 "reused for a different request" 而非 "in progress"——安全侧误报） | 客户端等待原请求完成或换新 key；窗口仅限部署瞬间的 query 变体写 |
| FM-3 | 存量硬删记录 + 新软删重试（残余边角，仅部署前记录） | 回放旧 204，对象实际已硬删 | 无害方向（软删落空，非合规硬删丢失）；TTL GC 清除后消失；无条件实现可消除但代价见 §2.1 |
| FM-4 | query 编码/顺序/重复差异（`hard=%31`、`a=1&b=2` vs `b=2&a=1`） | 指纹不同 → 409 | 规格 §5 既定边界：RawQuery 原文参与哈希，规范化属独立设计 |
| FM-5 | 同 key 预签名 `?op=`/`?expires=` 变化 | 409（旧行为回放过期/错误 op URL） | 附带修复（C1），严格更安全 |
| FM-6 | 幂等存储错误 | 500 fail-closed（不变） | 既有路径，未触碰 |

---

## 5. 迁移步骤

1. **部署新二进制**（含 `internal/api/rest/idempotency.go` 与新增测试文件）。无 schema 变更、无配置变更、无公开 API 变更。
2. **（可选）** 将 `IDEMPOTENCY_TTL_HOURS` 设为 > 0（当前默认 0 = 永不清除），界定存量幂等记录的过渡窗口。
3. **（可选，消除全部存量边角）** 部署前清空 `idempotency_keys` 表（SQLite/Postgres 通用：`DELETE FROM idempotency_keys`）。不执行也不影响安全性——FM-1/FM-3 均为 fail-closed 或无害方向。
4. **文档：** CHANGELOG 记录行为变更（同 key 异 query → 409）；无需改 `docs/configuration.md`（无新配置项）。
5. **合入门禁：** `make check`（gofmt / build / vet / test / filesize）全绿。

---

## 6. 验收映射（已实现，全部通过）

| AC | 测试（`internal/api/rest/idempotency_query_test.go`，新增 111 行） | 断言要点 |
|----|------|---------|
| AC-1 | `TestIdempotency_FingerprintIncludesQuery`（:19） | `fingerprint`/`bodyFingerprint` 区分 `/v1/files/x` 与 `?hard=1`（含 `IDEMPOTENCY_HASH_BODY=true` 路径，空 body sha256 常量）；相同请求（含相同 query）指纹稳定 |
| AC-2 | `TestIdempotency_QueryVariantConflicts`（:43） | 同 key 软删→`?hard=1` 重试 → 409 + `IdempotencyConflict` 码 + 无 `Idempotency-Replayed` + handler 不重跑（`calls==1`，C2 修正）；相同重试仍回放 204 |
| AC-3 | `TestIdempotency_HardDeleteWithFreshKey`（:78） | `setupTest` 端到端：PUT 201 → 新 key `?hard=1` → 204 → `ListObjectVersions == 0`（硬删区别于软删 tombstone）→ `svc.Get` → `ErrNotFound` |
| 回归 | 13 个既有 `TestIdempotency*`（`idempotency_test.go`） | method/path/body 维度语义不变，全绿 |
| 门禁 | `make check` exit=0 | gofmt ✓ vet ✓ build ✓ test ✓ filesize ✓；文件行数 336 / 496 / 111 均 ≤ 500；无新依赖（I6） |

---

## 7. 实现（最终代码形态）

`internal/api/rest/idempotency.go`（315 → 336 行）：新增共享 helper `fingerprintInput(r, bodyHash)`，`fingerprint`/`bodyFingerprint` 两个调用点各自收敛为一行调用：

```go
func fingerprintInput(r *http.Request, bodyHash string) string {
	in := r.Method + " " + r.URL.Path
	if q := r.URL.RawQuery; q != "" {
		in += "?" + q
	}
	if bodyHash != "" {
		in += " " + bodyHash
	}
	return in
}
```

- `fingerprint(r)` = `sha256(fingerprintInput(r, ""))`（v1 路径，`IDEMPOTENCY_HASH_BODY=false` 默认）
- `bodyFingerprint(r, bodyHash)` = `sha256(fingerprintInput(r, bodyHash))`（v2 路径，两模式同时修复，FR-1 约束 b）
- 既有注释同步更新，说明 query 纳入与条件追加的兼容性理由。
