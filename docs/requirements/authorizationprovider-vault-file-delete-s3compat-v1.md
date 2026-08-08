# 方向：vault.file.delete 的 fail-closed 强制 —— s3compat 边界的 AuthorizationProvider 端口（覆盖 S3 policy 门禁与 FileService 删除路径）

> **模块：** `internal/api/s3compat`（+ `cmd/server` 装配点） · **来源分析：** `docs/auto/analyses/internal-api-s3compat-eeefa063.json`（方向 2） · **日期：** 2026-08-06
> **评分：** 价值 8 / 风险降低 9 / 工作量 5 / 置信度 8
> **本文所有代码引用均已对照仓库逐条验证**（行号以当前工作树为准，HEAD `acfaaf4`）。
>
> **与既有 campaign 的关系：** `docs/auto/runs/authorizationprovider-port-enforcing-vault-file--59697301/` 的旧 design 把端口放在 **service 边界**，因 `s.authorizer == nil` 提前返回（access.go:91-93）的 P0 缺陷在 design_gate 被拒。本规格按所选方向把端口放在 **adapter 边界（s3compat）**——门禁先于任何 service 调用执行，service 侧 nil-authorizer 基线（file.go:96-98）不构成绕过路径。

---

## 1. 问题陈述

S3 对象删除被门禁两次，而两次门禁对 `vault.file.delete` 权限**都不是 fail-closed**：

1. **Adapter 本地 IAM bucket-policy 门禁**（`authorizeS3Request`，policy.go:43）从不咨询 `access.Authorizer`；`checkBucketPolicy` 在 `cfg.Policy == ""` 时直接放行（policy.go:107-109）——**默认打开**，仅在 policy 解析/查找错误时 fail-closed。因此一个未配置 bucket policy 的桶，其对象删除完全不经任何权限判定。
2. **Service 门禁**（`authorizeObject → s.authorizer.Authorize`，access.go:95）在 `WithAuthorizer` 未被调用时为 no-op allow（access.go:91-93；file.go:96-98 注释 "A nil authorizer preserves the CI/MVP baseline"）。默认部署 `cfg.Access.Enabled=false` 时 `buildAccessManager` 返回 nil（cmd/server/access.go:12-14）→ `WithAuthorizer(nil)`（main.go:215）→ **生产默认路径同样是 allow**。
3. **批量 `?delete`** 额外只在 bucket 级做一次 `s3:DeleteObject` policy 检查（policy.go:39 的 `?delete` 规则），然后 `extra.go:430` 的循环逐 key 调 `deleteS3Object` 依赖 service 内授权——service 门禁打开时即整批放行。
4. **provider error 非 fail-closed**：`service.authorize` 对 `Authorize` 返回的 error 只做 `fmt.Errorf("authorization decision: %w", err)`（access.go:100），**不包 `ErrForbidden`** → S3 侧 `s3ErrorCode` 落到 `InternalError` → **500**，而非 403。

方向选定：在 s3compat 边界新增 **AuthorizationProvider 端口**（包装 `access.Authorizer` 的形状），删除路径在调用任何 FileService 方法**之前**咨询 provider；**provider 未设置时默认拒绝**（fail-closed）；provider 拒绝与 provider 错误一律呈现为 S3 `AccessDenied` 403；被拒请求零 outbox/audit/event 副作用。

### 触发场景（真实工作流）

1. 部署启用了 S3 网关但未启用 access 模块（`ACCESS_ENABLED=false`，默认）；桶无 bucket policy。
2. 持有任意 S3 凭据（SigV4 key / API key）的调用方 `DELETE /s3/<bucket>/<key>` → adapter 门禁放行（无 policy）→ service 门禁放行（nil authorizer）→ 对象被删除，`event_outbox` 写入 `vault.file.deleted@1.1` 事实。
3. 管理员以为 `vault.file.delete`（域内映射 `access.ActionDelete` = `"object:delete"`，types.go:76；角色 `vault.file_admin`，authorizer.go:131）已保护对象——实际两个门禁都未执行该判定。
4. 当 access 模块启用但 provider（access.Manager）对某主体返回**错误**（如 ACL store 故障）时，删除以 500 `InternalError` 呈现——失败模式不可区分，且不是 fail-closed。

---

## 2. 现状与代码证据（已逐条验证）

| # | 证据 | 验证结果 |
|---|------|---------|
| E1 | `s3compat/policy.go:43` — `authorizeS3Request`（方向引用 :33）：expected-bucket-owner → `validatePolicyWrite` → `checkBucketPolicy` → copy-source 检查；**从不咨询 `access.Authorizer`** | ✅ 与引用一致（:33→:43） |
| E2 | `s3compat/policy.go:107-109` — `if cfg.Policy == "" { return true }`（方向引用 :108-110）：无 bucket policy 即放行 | ✅ 与引用一致 |
| E3 | `s3compat/policy.go:39` — `{query: "delete", putAction: "s3:DeleteObject", deleteAction: "s3:DeleteObject"}`：批量 `?delete` 只做 bucket 级 `s3:DeleteObject` 检查 | ✅ 与引用一致 |
| E4 | `s3compat/extra.go:430` — `deleteObjects`：逐 key 调 `deleteS3Object`，adapter 层无 per-key 授权 | ✅ 与引用一致 |
| E5 | `s3compat/errors.go:118` — `{service.ErrForbidden, "AccessDenied"}`；errors.go:61 — `"AccessDenied" → http.StatusForbidden` | ✅ 与引用一致 |
| E6 | `service/access.go:91-93` — `if s.authorizer == nil { return nil }`：nil authorizer 时 service 门禁 no-op allow；`service/file.go:96-98` — `WithAuthorizer` 注释 "A nil authorizer preserves the CI/MVP baseline"（方向引用 :88-90） | ✅ 与引用一致（:88-90→:96-98） |
| E7 | `service/access.go:95` — `s.authorizer.Authorize(...)`；:99-101 — 仅 `!decision.Allowed` 包 `ErrForbidden`；**`Authorize` 返回 error 时只 `fmt.Errorf("authorization decision: %w", err)`，不包 `ErrForbidden`** → `s3ErrorCode`（errors.go:118 无匹配）落到 `InternalError` → **500** | ✅ 修正性验证：方向"provider error→403 not 500"的机制缺口在此 |
| E8 | `service/file_delete.go:159,179` — `Delete`/`DeleteVersion` 均 `authorizeObject(access.ActionDelete, ...)`；`service/delete_marker.go:34,37` — `CreateDeleteMarker` 对现版本 `authorizeObject`、否则 `authorizePath`（方向引用 :32） | ✅ 与引用一致（:32→:34/:37） |
| E9 | `access/authorizer.go:10-12` — `Authorizer` 接口形状 `Authorize(ctx, Principal, Action, Resource) (Decision, error)`（方向引用 :10）；:25-27 — 无 principal → `denied("missing_principal")`；:131 — `vault.file_admin` 角色在 `isAdministrator` 内 | ✅ 与引用一致 |
| E10 | `access/types.go:76` — `ActionDelete Action = "object:delete"`：`vault.file.delete` 权限的域内映射；仓库无字面量 `"vault.file.delete"` 权限字符串（`grep -rn "vault.file.delete" internal/` 仅命中事件类型/审计动作，属不同命名空间） | ✅ 补充验证 |
| E11 | `cmd/server/access.go:12-14` — `buildAccessManager`：`cfg.Access.Enabled=false` → 返回 nil → main.go:215 `WithAuthorizer(nil)` → 默认部署 service 门禁 no-op allow | ✅ 补充验证 |
| E12 | `cmd/server/http.go:120` — `s3compat.NewRouter(svc, logger)`：S3 网关唯一装配点，当前无 provider 概念 | ✅ 补充验证 |
| E13 | `repository/event_outbox.go:22` — `EventTypeFileDeleted11 = "vault.file.deleted@1.1"`（**outbox 已存在**，分析快照"无 @1.1"已过时）；:76-83 — `validOutboxPayload` 要求 `schema_version == "1.1"` | ✅ 补充验证 |
| E14 | `service/file_delete.go:111-127` — `deleteFacts` 构建 deleted@1.1 + notify@1.1 载荷；:131-134 — 经 `HardDeleteObjectWithEvent`/`SoftDeleteObjectWithEvent`（event_outbox.go:90-190）在**删除事务内**写 outbox 行 → **outbox 行只在 service 授权通过后的删除事务内产生** | ✅ 补充验证 |
| E15 | `api/rest/handler.go:245` — REST admin delete → `h.svc.Delete(...)`：REST 路径与 S3 路径共享同一 service 门禁（parity 载体） | ✅ 补充验证 |
| E16 | 测试基建：`s3compat/versioning_test.go:194`、`handler_test.go:34` 均 `NewRouter(svc, nil)`（无 provider）；versioning_test.go:260-283（delete-marker）、handler_test.go:265（对象删除）等既有测试直接删除 → 默认拒绝需显式注入 allow-all provider 迁移 | ✅ 补充验证 |

### 缺陷机理

```
S3 DELETE /{bucket}/{key}（桶无 bucket policy，access 未启用）
  ├─ adapter 门禁 authorizeS3Request → checkBucketPolicy: Policy=="" → allow   ← 打开（E1/E2）
  ├─ service 门禁 authorizeObject → s.authorizer==nil → return nil             ← 打开（E6/E11）
  └─ svc.Delete → 删除事务 → event_outbox 写入 vault.file.deleted@1.1           ← 副作用（E13/E14）
```

批量 `?delete`：`authorizeS3Request`（key==""，action=`s3:DeleteObject`，E3）→ `deleteObjects` 循环逐 key 无 adapter 级授权（E4）→ 依赖 service 门禁（默认打开）。

---

## 3. 需求规格

### FR-1：AuthorizationProvider 端口（adapter 边界，默认拒绝）

s3compat 定义自己的端口类型，形状与 `access.Authorizer` 一致（E9），使 `access.Manager` 结构化满足、无需包装器：

```go
// s3compat 边界端口：包装 access.Authorizer 的形状。
type AuthorizationProvider interface {
	Authorize(ctx context.Context, principal access.Principal,
		action access.Action, resource access.Resource) (access.Decision, error)
}
```

- **约束 a（fail-closed 默认）：** Handler 未设置 provider 时，删除门禁一律拒绝（403 `AccessDenied`）。这是对 service 侧 "nil authorizer preserves the CI/MVP baseline"（E6）的**刻意反转**，且仅作用于删除路径——GET/PUT/HEAD 等非删除操作不受影响（I5 基线保护，见 AC-7）。
- **约束 b：** provider 的决策是**权威且唯一**的 `vault.file.delete` 判定点之一：`provider.Authorize(ctx, principal, access.ActionDelete, resource{tenant,bucket,key})`。权限映射：产品权限 `vault.file.delete` ↔ `access.ActionDelete`（`"object:delete"`，E10）↔ S3 policy action `s3:DeleteObject`（E3）↔ 角色 `vault.file_admin`（E9）。**不新增字面量权限字符串**。
- **约束 c：** provider 从请求上下文取 principal（`access.PrincipalFrom(ctx)`，auth 中间件已注入）；无 principal 时不得合成放行主体——把零值 principal 原样交给 provider（E9 的 `missing_principal` 拒绝路径生效）。
- **约束 d：** 装配点唯一：`cmd/server/http.go:120` 将 `accessManager`（非 nil 时）传入 `NewRouter`/`NewHandler`（E12）。service 侧 `WithAuthorizer(accessManager)`（main.go:215）保持不变。

### FR-2：单对象删除门禁（`s3:DeleteObject` 对象级）

在 `authorizeS3Request`（E1）内、`checkBucketPolicy` 通过**之后**，当满足 **`key != ""` 且计算出的 action == `"s3:DeleteObject"`**（即 `objectPolicyAction` 对无 `?tagging`/`?uploadId` 的 DELETE 的返回值，policy.go:172-183）时，以 `resource{tenant, bucket, key}` 咨询 provider：

- 覆盖三条单删路径（均经 `deleteS3Object`，delete.go:11-31）：无版本删除（`svc.Delete`）、`?versionId` 删除（`svc.DeleteVersion`）、版本化桶的 delete-marker 创建（`svc.CreateDeleteMarker`）——三者映射同一 action（E8）。
- **约束 a：** 门禁先于**任何** service 调用（含 `GetObjectRetention`/`GetBucketConfig` 预检）；拒绝/错误 → `writeS3Error(w, r, service.ErrForbidden)` → `AccessDenied` 403（E5），直接 return，不进入 `DeleteObject` 主体（handler.go:264-288）。
- **约束 b：** provider 返回 error 与返回 deny 同等待遇——一律 403（fail-closed）。**禁止**把 provider error 传播为 500（E7 现状的修正）。

### FR-3：批量 `?delete` per-key 门禁

`deleteObjects`（extra.go:430）循环内、对每个 key 调 `deleteS3Object` **之前**，以 `resource{tenant, bucket, o.Key}` 咨询 provider：

- **约束 a：** 被拒 key → 不调用 `deleteS3Object`，在 XML 响应 `Errors` 中输出 `Code:"AccessDenied"`（沿用现有 `deleteErrItem` 结构），继续处理其余 key（与现有 per-key 错误循环语义一致，extra.go:443-455）。**被拒 key 绝不被删除**（"without partially deleting denied keys"）；允许的 key 正常删除。
- **约束 b：** provider error 对单个 key 同样按 `AccessDenied` 处理该 key（fail-closed per-key），不中断整批。
- **约束 c：** 既有 bucket 级 `s3:DeleteObject` policy 检查（E3）保持为第一层门禁；per-key provider 门禁为第二层，二者**合取**（AND-composition）。FR-2 的门禁在 `key==""`（bucket 级）时不触发，避免对整批做错误的对象级判定。

### FR-4：错误呈现（403 单一化）

- 所有 provider deny/error 统一经 `service.ErrForbidden` 呈现：单删 → 整个请求 403 `AccessDenied`；批删 → 逐 key `AccessDenied`（200 XML 外壳不变，AWS 兼容）。
- `s3ErrorCode`/`classify`（errors.go）**零改动**：`ErrForbidden → AccessDenied → 403` 映射已存在（E5）。本方向不新增 S3 错误码。

### FR-5：被拒请求零副作用（outbox/audit/event）

不变量：**任何被 provider 拒绝（或 provider 未设置）的删除请求不得产生任何持久化副作用**。

- 由于门禁先于 service 调用（FR-2/FR-3），被拒请求不进入删除事务 → 零 `event_outbox` 行（`vault.file.deleted@1.1` / `vault.file.notify@1.1`，E14）、零 `object_events` 行（`EventDeleted` 广播）、零 `audit_log` file-delete 行、零 storage blob 变更。
- 该不变量须以测试显式锁定（AC-4/AC-5），防止未来把门禁移到 service 调用之后。

### FR-6：组合/parity（S3 与 REST 共享决策源）

- 生产装配：同一 `access.Manager` 实例既作 service `WithAuthorizer`（main.go:215）又作 S3 provider（FR-1 d）→ S3 删除与 REST admin 删除（E15）共享同一决策源与 store。
- `access.Manager.Authorize` 每次请求实时读 store（authorizer.go:32 `ListApplicableACL`）→ **mid-session 撤销即时生效**（无会话级缓存）：删除 ACL allow / 新增显式 deny 后，S3 与 REST 同时 403；恢复后同时放行且删除写入有效 outbox 事件。
- service 侧 nil-authorizer 基线（E6）**保持不动**：REST/CLI/MCP 在 access 未启用时的行为不变；S3 的 fail-closed 由 adapter 门禁独立保证（这是本方向把端口放 adapter 边界的理由，见头部与旧 campaign 的关系）。

### 非功能约束

- `make check` 全绿（gofmt / build / vet / test，AGENTS.md §0）；新增代码限于 `internal/api/s3compat`（新文件 + policy.go/extra.go/handler.go 门禁点）+ `cmd/server/http.go` 一行装配；单文件 ≤ 500 行。
- 纯 stdlib，无新 `go.mod` 依赖（I6）；无 SQL/schema/迁移改动（I2 不涉及）；不触碰存储 key 校验（I3）与中间件链（I4）。
- service 层零改动（`internal/service`、`internal/access`、`internal/events`、`internal/repository` 均不改）。

---

## 4. 验收标准（可测试）

> 测试基建（已验证）：s3compat 测试经 `NewRouter(svc, logger)` + `httptest`（versioning_test.go:194 先例）；principal 经 `access.WithPrincipal(ctx, principal)` 注入（access/context.go:7-10，auth 中间件同款，auth_middleware.go:183）；`access.Manager` 需要 `access.Store`（repository 实现，sql_access_acl.go:55），且 `NewManager` 硬校验 `cfg.ShareSecret ≥ 32B`（manager.go:44-45，缺省会直接 error）——测试常量取 `[]byte("0123456789abcdef0123456789abcdef")`（32B，access_test.go:32 先例）；outbox 行可经 repo 查询（`event_outbox` 表，event_outbox.go:22）。测试替身：allow-all / deny-all / error / 无 principal 拒绝四种 stub provider。

### AC-1 provider wired：无 principal 或 provider-deny → 403，即使无 bucket policy（unit）

> 对应方向验收 "with provider wired, delete with no principal or provider-deny → S3 AccessDenied 403 even when no bucket policy exists"。

```go
// 新文件 internal/api/s3compat/authz_gate_test.go（示意）
func TestDeleteDeniedWithoutBucketPolicy(t *testing.T) {
	// 桶不设 bucket policy（checkBucketPolicy 走 allow-on-empty 路径，E2）
	// provider = deny-all stub；principal 经 WithPrincipal 注入
	for _, tc := range []struct{ name, method, url string }{
		{"plain", "DELETE", "/b/k.txt"},
		{"versioned", "DELETE", "/b/k.txt?versionId=v1"}, // 先 Put 两版本
		{"delete-marker", "DELETE", "/b/mk.txt"},          // 版本化桶 → CreateDeleteMarker
	} {
		resp := do(t, tc.method, base+tc.url, nil, nil)
		if resp.StatusCode != http.StatusForbidden { /* fail */ }
		if body code != "AccessDenied" { /* fail */ } // errors.go:118/61 → 403
	}
	// 无 principal：ctx 不含 WithPrincipal → 零值 principal → provider 拒绝
	// （真实 access.Manager 走 missing_principal，E9:25-27）
}

func TestDeleteDeniedWhenNoPrincipal(t *testing.T) {
	// provider = access.Manager（SQLite repo + DefaultPolicy=deny + ShareSecret ≥32B，§4 基建）
	// 请求上下文不注入 principal → 403 AccessDenied
}
```

### AC-2 provider error → 403（fail_closed），不是 500（unit）

> 对应方向验收 "provider error → 403 (fail_closed), not 500"。

```go
func TestDeleteProviderErrorIs403Not500(t *testing.T) {
	provider := errStubProvider{err: errors.New("pdp outage")} // Authorize 返回 error
	// 单删：DELETE /b/k.txt → 403，XML Code=AccessDenied
	// 批删：PUT /b?delete（2 keys）→ 200，每 key Error.Code=AccessDenied
	if resp.StatusCode == http.StatusInternalServerError { /* fail: E7 现状回归 */ }
}
```

### AC-3 批量 `?delete`：per-key AccessDenied，被拒 key 不删除（unit）

> 对应方向验收 "batch ?delete reports per-key AccessDenied without partially deleting denied keys"。

```go
func TestBatchDeletePerKeyDenial(t *testing.T) {
	// 两个 key 均存在；provider 对 "b/denied.txt" 拒绝、对 "b/allowed.txt" 放行
	// PUT /b?delete → 200；XML 含 1×Deleted(allowed.txt) + 1×Error(denied.txt, Code=AccessDenied)
	// 被拒 key 仍可 GET（200）；允许 key 已删（404）—— "without partially deleting denied keys"
	// 反向：两 key 均拒绝 → 0×Deleted + 2×Error；两 key 均未被删除
}
```

### AC-4 outbox：被拒请求写零 `vault.file.deleted@1.1` 行（unit）

> 对应方向验收 "outbox delivery: denied requests write zero vault.file.deleted outbox rows"。

```go
func TestDeniedDeleteWritesNoOutboxRows(t *testing.T) {
	// repo.Open("sqlite", ...) + Migrate；经 adapter 发起：
	//  (a) 单删被拒（provider-deny）  (b) 批删 2 keys 全被拒
	// repo 查询断言（event_outbox 表，E13/E14）：
	//   SELECT COUNT(*) FROM event_outbox
	//    WHERE event_type='vault.file.deleted@1.1' AND tenant_id=? AND bucket=? AND key=?
	//   → 0（vault.file.notify@1.1 同理）
	//   SELECT COUNT(*) FROM object_events WHERE type='deleted' AND key=? → 0
	//   SELECT COUNT(*) FROM audit_log WHERE action='file.delete' AND target=? → 0
	// 对照：allow provider 下同 key 删除 → deleted@1.1 恰 1 行 + notify@1.1 恰 1 行
}
```

### AC-5 event schema：拒绝时无 @1.1 事件（unit）

> 对应方向验收 "event schema: no @1.1 event on denial"。@1.1 的唯一持久化载体是 `event_outbox` 行（payload 含 `schema_version:"1.1"`，E13 校验）——AC-4 的零行断言即"无 @1.1 事件"；另加 bus 级断言：

```go
func TestDeniedDeleteEmitsNoEvent(t *testing.T) {
	// 订阅 EventBus（bus.Subscribe 先例，events/bus_test.go）
	// provider-deny 删除后：订阅通道 0 事件；HasEventOutboxFact(originID, EventTypeFileDeleted11) == false
}
```

### AC-6 组合 e2e：mid-session 撤销 → S3 与 REST 均 403；恢复 → 删除 + 有效 outbox 事件（composition e2e）

> 对应方向验收 "revoking vault.file.delete mid-session turns both S3 delete and REST admin delete into 403 (parity check), and re-granting restores delete with a valid outbox event"。

```go
func TestCompositionRevokeRestoreParity(t *testing.T) {
	// 装配：repo(SQLite) + access.Manager(DefaultPolicy=deny, store=repo, ShareSecret ≥32B)
	// principal P：PrincipalUser，无 scopes/roles（非 admin——isAdministrator 短路会让撤销失效，authorizer.go:53）
	//   （WithPrincipal 注入，与 auth_middleware.go:183 同款）
	//   svc.WithAuthorizer(manager)（main.go:215 同款）
	//   NewRouter(svc, logger, manager)（FR-1 d：同一实例作 provider）
	// 1) 为 P 授予 allow ACL（ActionDelete）→ S3 DELETE /b/k.txt → 204
	//    且 event_outbox 恰 1 行 vault.file.deleted@1.1，payload 校验：
	//    schema_version=1.1、tenant/bucket/key/version_id/etag/size/request_id/actor 齐全（E13/E14）
	// 2) mid-session 撤销：删除 allow ACL（或新增 explicit deny）→ 不换 token、不重建 ctx
	//    S3 DELETE → 403 AccessDenied；REST admin delete（rest handler → svc.Delete，E15）→ 403 —— parity
	//    event_outbox 计数不变（AC-4 断言复用）
	// 3) 恢复：移除 deny / 重新授予 → S3 DELETE → 204 + 新的有效 vault.file.deleted@1.1 outbox 行
}
```

### AC-7 provider 未设置 → 默认拒绝（仅删除路径）+ 既有回归

> 对应方向 FR-1 约束 a 的 "default-deny when provider unset"。

```go
func TestDeleteDeniedWhenProviderUnset(t *testing.T) {
	// NewRouter(svc, nil)（不传 provider）→ 单删、?versionId、delete-marker → 403 AccessDenied
	// 批删 → 200 外壳 + 每 key Error(AccessDenied)（per-key 门禁，外壳不变，非整批 403）
	// 回归保护（I5）：同 server GET（200）/ PUT（200）/ HEAD（200）不受影响 —— 门禁仅作用于删除
	// bucket 级负向断言（门禁不误触发于 key=="" 路径）：
	//   PUT /{b} + PUT /{b}/obj（非空桶）→ DELETE /{b} → 409 BucketNotEmpty
	//   （服务层非空检查被到达；门禁若误触发将是 403 而非 409）
	//   DELETE /{b}?lifecycle → 204（bucket 子资源删除不在触发面）
}
```

既有测试迁移（E16）：`versioning_test.go`（delete-marker 用例 :260-283）、`handler_test.go:265`（对象删除）等经 `NewRouter(svc, nil)` 执行删除的用例必须显式注入 allow-all provider 后保持原期望（204/`x-amz-delete-marker` 等）；bucket 级删除（`handler_test.go:727/928-930`、`cors_dispatch_test.go:32`）与 `?tagging`/`?uploadId` 删除不受影响（FR-2 的 action 判定天然排除）；AC-7 以 nil-server 负向断言锁定：非空桶 `DELETE /{b}` → 409 `BucketNotEmpty`、`DELETE /{b}?lifecycle` → 204。

---

## 5. 范围边界（明确不做）

| 不做 | 理由 |
|------|------|
| service 侧 nil-authorizer 基线改动（access.go:91-93 / file.go:96-98） | 本方向把端口放 adapter 边界：S3 的 fail-closed 不依赖 service 装配；REST/CLI/MCP 行为不变（FR-6） |
| `DeleteObjectTagging`（`?tagging`）、`AbortMultipartUpload`（`?uploadId`）、bucket 删除（`s3:DeleteBucket`）及 bucket 子资源删除 | 非 `s3:DeleteObject` 对象删除，不在方向证据/验收内；FR-2 的 action 判定天然排除，无需代码处理 |
| `checkBucketPolicy` 的 allow-on-empty 语义（policy.go:107） | IAM bucket-policy 门禁保持现状；provider 门禁是独立第二层（FR-2/FR-3 合取），空 policy 不再等于"删除放行" |
| 新增 `"vault.file.delete"` 字面量权限字符串/注册表 | 域内映射已存在：`ActionDelete="object:delete"`（E10）+ `vault.file_admin` 角色（E9）；不引入第三套命名空间 |
| @1.1 事件 schema / outbox 的新建或改造 | 已存在（E13/E14）；本方向只做"被拒请求零行"断言 |
| 新增 S3 错误码 / 改变 `errors.go` 映射 | `ErrForbidden → AccessDenied 403` 已满足（E5）；零改动 |
| provider 门禁扩展到非删除操作（GET/PUT/HEAD/list） | 超出方向；AC-7 反向锁定"仅删除路径受门禁" |
| SigV4/auth 中间件、中间件链（I4）改动 | 与方向无关；principal 注入已由 auth 中间件完成 |

---

## 6. 实现指引（供验收后落地，非本规格交付物）

- **端口定义**（新文件 `internal/api/s3compat/authz.go`）：`AuthorizationProvider` 接口（FR-1 形状）+ `Handler` 字段 `authz AuthorizationProvider`（handler.go:21-24 结构体扩充）+ 构造入口：`NewHandler(svc, logger)` 增补为 `NewHandler(svc, logger, authz AuthorizationProvider)`（或 option 风格），`NewRouter` 同步透传；`cmd/server/http.go:120` 传 `accessManager`（nil 安全：nil = 未设置 = 默认拒绝）。
- **门禁辅助**：`func (h *Handler) authorizeDelete(ctx, tenant, bucket, key) bool` —— `h.authz == nil → false`（默认拒绝）；否则 `Authorize(ctx, principal, access.ActionDelete, access.Resource{...})`；`!Allowed || err != nil → false`。单删在 `authorizeS3Request` 内 `key != "" && action == "s3:DeleteObject"` 分支调用（policy.go:43-79 尾部、checkBucketPolicy 之后）；批删在 `deleteObjects` 循环内逐 key 调用（extra.go:430-456，`deleteS3Object` 之前）。
- **错误呈现**：`writeS3Error(w, r, service.ErrForbidden)`（与 checkBucketPolicy 拒绝路径同款，policy.go:113-115）；批删在循环内 `out.Errors = append(..., deleteErrItem{Key, VersionID, Code: "AccessDenied", Message: ...})`。
- **测试**：`internal/api/s3compat/authz_gate_test.go` 落地 AC-1…AC-5、AC-7（四种 stub provider + 真实 access.Manager）；AC-6 放 `internal/integration` 或 `service` 包 e2e（fullserver_test.go 先例）；既有测试按 §4 AC-7 迁移；`go test ./internal/api/s3compat/ ./internal/integration/` 与 `make check` 确认全绿。
