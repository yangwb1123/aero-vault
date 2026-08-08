# 设计：vault.file.delete 权限词汇 + 核心 fail-closed —— AuthorizationProvider 端口（internal/auth 词汇 + access.Manager 门禁 + FileService 删除门禁）

> **配套规格：** `docs/requirements/authorizationprovider-vault-file-delete-core-v1.md` · **模块：** `internal/auth`（组合面：`internal/access` + `internal/service` + `internal/api/rest` + `cmd/server`） · **状态：** 设计已定稿，未实现 · **日期：** 2026-08-06
> 本文是规格的落地设计：证据复核结论（含设计期新发现 R1–R7）、API 变更、设计决策、兼容性约束、失败模式、迁移步骤、验收映射、最终代码形态。
> **与兄弟轮的关系：** S3 边界端口已落地（`internal/api/s3compat/authz.go`，上轮 campaign）；本设计是其核心层对称增量——共享同一决策源（`*access.Manager`），**必须不破坏已合入的 `authz_parity_test.go`（AC-6 验收）**，这是本设计的第一约束。

---

## 0. 证据复核结论（对规格逐条核验 + 设计期新发现）

规格全部 20 条代码引用（E1–E20）与仓库 HEAD 逐行核验一致，**无需修正**（行号差异均已在规格内标注）。设计阶段新增独立核验 R1–R7（其中 R1/R3 是**规格语义缺陷，本设计已修正**，见 §2）。

### 0.1 逐条复核（对照工作树）

| # | 规格引用 | 复核 | 结论 |
|---|---------|------|------|
| E1 | `authorizer.go:10-12` `Authorizer` 接口 | 实际 :10-11，签名精确一致 | ✅ |
| E2 | `authorizer.go:24-26` disabled → `Allowed:true` | 实际 :19-21 `access_control_disabled` 早退 | ✅ |
| E3 | `service/access.go:91` nil authorizer → allow | 实际 :91-93（`authorize()` 内，`requireActiveTenant` 之后） | ✅ |
| E4 | `main.go:94,215` `WithAuthorizer(accessManager)` | 实际 :94（HTTP）/ :215（worker）；`buildRouter` 经参传递 | ✅ |
| E5 | `cmd/server/access.go:11-19` `buildAccessManager` 唯一实现，`!Enabled → nil` | 实际 :11-19；`ACCESS_CONTROL_ENABLED` 默认 false（config.go:216） | ✅ |
| E6 | `auth.go` `knownScope` 封闭集合 | 实际 :139；Scope const :31-38；Parse 未知 scope fail-closed :112-125 | ✅ |
| E7 | `auth_middleware.go:189-193` `checkScope` 方法派生 | 实际 :189-196：DELETE 恒 `ScopeWrite` 档；`Has` 对 admin 短路（auth.go:50-54） | ✅ |
| E8 | `store.go` `PersistedKey` 无 Roles；`parseScopeString` 不校验 | 实际 :12-20 无 Roles；:57-64 任意 token 往返 | ✅ |
| E9 | `jwt.go:124-133` Roles 来自 claims；snaplink.go:86 | 实际 :124-133 / :86 | ✅ |
| E10 | `principal.go:10-35` `PrincipalForKey` | 实际 :10-35：Scopes map → 排序 []string，新 token 自动流入 | ✅ |
| E11 | `rest/admin.go:455-466` `requireAdmin` ScopeAdmin | 实际 :455-466 | ✅ |
| E12 | `authorizer.go:131` `vault.tenant_admin`/`vault.file_admin` | 实际 `isAdministrator` :126-135 | ✅ |
| E13 | "no match for 'vault.file.delete'" 被推翻 | ✅ s3compat 端口已落地（authz.go:10、policy.go:67 注释） | ✅ |
| E14 | `types.go:76` `ActionDelete = "object:delete"` | 实际 :76；`ActionAll` :82 | ✅ |
| E15 | 删除路径单一漏斗 `authorizeObject(ActionDelete)` | 实际：`file_delete.go:159,179`、`delete_marker.go:34,37`、`object_worker.go:58`、`file_bucket_settings.go:40,48`；**另确认** `BatchDelete`（file_features.go:167 → `s.Delete`）与 `DeleteBucket`（:40 bucket 级 + :48 逐对象）同漏斗 | ✅ |
| E16 | REST 删除无 adapter 级授权端口 | 实际 handler.go:239-249 仅 `checkBucketPolicy` → `svc.Delete`；`WithAccessManager`（:32）只服务 `/v1/access` 管理端点 | ✅ |
| E17 | outbox 行只在授权通过后的删除事务内 | 实际 `deleteFacts` :113-127（deleted@1.1 + notify@1.1）；authorize :159 先于事务 | ✅ |
| E18 | AV quarantine 是 system-principal 路径 | 实际 `object_worker.go:58`；worker 装配 `access.SystemContext(ctx, job.TenantID)`（cmd/server/workers.go:33） | ✅ |
| E19 | `NewManager` 构造约束 | 实际 manager.go:34-48：store 非 nil、ShareSecret ≥32B 硬校验（:44-45） | ✅ |
| E20 | `file.go:96-99` `WithAuthorizer` 已是接口 | 实际 :96-99；集成 harness 有注入点（authz_parity_test.go:117） | ✅ |

### 0.2 设计期新发现（R1–R7）

| # | 发现 | 处理 |
|---|------|------|
| **R1（规格语义缺陷，已修正）** | FR-2b 字面解读"显式授予唯一判据 = Scopes/Roles 字面量"会**打破已合入的 s3compat 轮验收**：`authz_parity_test.go:180` 以 `PutACL(Action: ActionDelete, Effect: Allow)` 授予普通 principal（无 scopes/roles）并断言 S3 DELETE 204、REST 撤销后 403。若 ActionDelete ACL 允许项不再授予删除，该测试失败，且 S3/REST 共享决策源的 parity 承诺（AC-6）破裂。规格排除清单点名的是 **`ActionAll` ACL 授予**，未点名 `ActionDelete` ACL 授予。**修正**：显式授予集合 = ① Scopes/Roles 字面量 `vault.file.delete`（新）∪ ② ACL 允许项 `Action == ActionDelete`（保留既有细粒度授予机制）；`ActionAll` ACL 允许项**不再**授予删除（行为收窄，见 D1/§3-2） | §2 D1 |
| **R2（命名修正）** | 规格 E18 称 `WithSystemPrincipal`，实际函数为 `access.SystemContext(ctx, tenant)`（context.go:17-21），语义一致 | §2 D6 |
| **R3（规格验收缺陷，已修正）** | AC-4 正控 token `deletetok:default:vault.file.delete` **在传输门禁即被拒**：`checkScope` 对 DELETE 恒要求 `ScopeWrite` 档（auth_middleware.go:189-196），`Has(ScopeWrite)` 对无 write/admin 的键返回 false → 403，provider 根本不可达。且 FR-1 明示不得把 `vault.file.delete` 并入方法 scope（`Has` 对 admin 短路会使 admin 键"拥有"该权限）。**修正**：正控 token 必须为 `deletetok:default:write+vault.file.delete`（write 满足传输档，字面量满足授予）；负控 admin token `admintok:default:admin` 经 `Has` 短路过传输、被 provider 拒绝——正是 FR-2b 的判别点 | §2 D7/§6 AC-4 |
| **R4（漏斗面补全）** | `DeleteBucket`（file_bucket_settings.go:40 `authorizeBucket(ActionDelete)` + :48 逐对象）在漏斗内——**bucket 删除同样 fail-closed**；规格"对象删除"措辞偏窄。迁移面须含 bucket 删除用例（rest buckets_test.go 等） | §2 D4/§5.2 |
| **R5（副作用面）** | WebDAV MOVE（dav.go:198 copy-then-delete）：copy 事务先于 delete 门禁提交。默认配置下 MOVE → copy 落盘、delete 403 → **留下副本的局部副作用**。规格 FR-5 的"零副作用"锁定的是删除事务本身（outbox/事件），MOVE 的预拷贝不在其内——须作为已知失败模式显式记录（sibling webdav 轮范围，本设计只文档化 + 可选一行预检，见 D8） | §2 D8/§4 |
| **R6（GC 不在漏斗）** | reconcile（retention/lifecycle/scrub/upload-gc）直连 `repo.SoftDeleteObject` + `store.Delete`（reconcile/deletion.go:53,70），**不经 service 门禁**——GC 不受 FR-3 影响，零迁移面。系统级清理绕过用户权限判定是既有设计，本方向不触碰 | §4 |
| **R7（capability 只读）** | capability 铸点共 3 处（auth_middleware.go:94 presigned-GET、shares.go:102/:197、rest/acl.go:26 canned-public-read），Actions 均为 read/preview/download——**capability 结构上不可能携带 delete**。FR-2 删除分支跳过 capability 阶梯零回归 | §2 D2 |

---

## 1. API 变更

**零配置变更、零 schema 变更、零 `go.mod` 变更（I6）、零协议响应变更（错误码/状态码复用既有面）。** 不新增接口、不改构造器签名——门禁落位在既有 `access.Authorizer` 接口的实现内部与既有 `FileService.authorize()` 内部。外部可见变更仅三处行为语义 + 一处注释：

| 层 | 旧 | 新 |
|----|----|----|
| `internal/auth/auth.go` | `Scope` 集合 {read, write, admin}；`knownScope`(:139) 拒绝 `vault.file.delete` | + `ScopeFileDelete Scope = "vault.file.delete"`；`knownScope` 接受之。`Parse` 的 AUTH_KEYS 记录可携带（`t:acme:write+vault.file.delete`）；未知 scope 仍 fail-closed。`Has`、`checkScope` **零改动**（FR-1：权限非方法 scope） |
| `internal/access/authorizer.go` | `Authorize`：disabled → 全动作 `Allowed:true`（:19-21）；删除走通用阶梯（ACL/owner/admin/tenant_default 均可放行） | disabled 时 `ActionDelete` → `denied("access_control_disabled")`（读动作不变）；`action == ActionDelete` 走专用 `authorizeDelete` 分支：explicit-deny → 授予检查（字面量 ∪ ActionDelete ACL 允许，D1）→ 拒绝。system 早退（:27-29）不动 |
| `internal/service/access.go` `authorize()`(:88-101) | `s.authorizer == nil → return nil`（全动作放行） | nil 且 `ActionDelete` 且非 `PrincipalSystem` → `ErrForbidden`（`"no authorization provider configured"`）；读/写动作保持 nil ⇒ allow（I5）；system 豁免（R2） |
| `internal/service/file.go:97` 注释 | "A nil authorizer preserves the CI/MVP baseline" | 更新：对删除不再成立（fail-closed when provider absent） |
| **行为：全部删除路径**（REST `DELETE /v1/files/{key}[?hard=1]`、`POST /v1/batch/delete`、folder 删除、`DELETE /v1/buckets/{b}`、WebDAV DELETE、MCP `delete_file`、CLI/SDK） | provider 缺失/未授予 → 放行（默认配置全放行） | provider 缺失 → 403；provider 在场但无授予 → 403；403 路径零 outbox/audit/事件/blob 副作用 |
| **行为：读/写路径**（Stat/Get/List/PUT/Restore/presign/分享） | — | **逐字节不变**（I5） |

**不新增 API 面**：`AuthorizationProvider` 端口形状已在 s3compat 轮定义（s3compat/authz.go:10-16），REST/核心层经 service 的 `access.Authorizer`（file.go:96-99，已是接口）与同一 Manager 决策——**同构复用、零包装器**（FR-4 的组合缝已存在，只需证明可替换，AC-5）。

---

## 2. 设计决策

### D1 — 授予集合精确化（R1 修正）：显式 `ActionDelete` ACL 允许项保留为授予；`ActionAll` 收窄

FR-2b 的授予检查（`authorizeDelete` 内，顺序固定）：

1. **explicit-deny 优先**（既有 `hasEffect(EffectDeny)` 语义，对 ActionDelete 匹配项——`actionMatches(ActionAll, ActionDelete)==true`，故 deny ActionAll 仍拒绝删除）。
2. **授予 = ①字面量**：`principal.Scopes` 或 `principal.Roles` 含 `"vault.file.delete"`（`slices.Contains`，**不用** `Key.Has`——admin 短路；Manager 侧 principal 是 []string，天然无短路）；**② ACL 允许项**：`matchingEntries(...)` 命中 `EffectAllow` 且 `actionMatches(entry.Action, ActionDelete)` **且 `entry.Action != ActionAll`**。
3. **不授予**：`ScopeAdmin`、owner 匹配、`tenant_default`（`scopeAllows`）、`isAdministrator` 角色（`vault.tenant_admin`/`vault.file_admin`）、`ActionAll` ACL 允许项、capability（结构上只读，R7）。
4. 无命中 → 沿用既有拒绝原因：`len(entries)>0 → resource_acl_no_match`，否则 `default_deny`。

理由：① 已合入的 `authz_parity_test.go:180`（PutACL ActionDelete → 204）是上轮验收的活契约，字面量-only 会打破 S3/REST parity；② 规格排除清单点名 `ActionAll` 而非 `ActionDelete`，字面量-only 使该点名冗余；③ ACL 层是**按对象/前缀**的细粒度机制，字面量是**全局**授予——两者互补（部署形态：有 ACL 管理用 ACL，无 ACL 管理（env 键/JWT）用字面量）。`ActionAll` 收窄是唯一的行为收缩，仓库现存测试零使用（`grep ActionAll *_test.go` 无删除断言），§3-2 记录运维影响。

### D2 — 分支落位：`Authorize` 内 system 早退之后、通用阶梯之前；专用方法 `authorizeDelete`

```go
func (m *Manager) Authorize(ctx, principal, action, resource) (Decision, error) {
	if !m.cfg.Enabled {
		if action == ActionDelete {
			return denied("access_control_disabled"), nil      // FR-2a
		}
		return Decision{Allowed: true, Reason: "access_control_disabled"}, nil
	}
	if principal.Kind == PrincipalSystem { ... trusted_system ... }   // :27-29 不动（FR-2c）
	if principal.SubjectID == "" { ... missing_principal ... }        // 不动
	if !tenantMatches(...) { ... tenant_mismatch ... }                // 不动
	if action == ActionDelete {
		return m.authorizeDelete(ctx, principal, resource)            // 专用分支
	}
	// 既有通用阶梯逐字节不动（读/写/restore/manage_acl/...）
}
```

`authorizeDelete` 内部：`ListApplicableACL` + `ListSubjectDepartments`（error → `denied("acl_store_error")` + err，fail-closed，与既有阶梯同形）→ D1 顺序。**前置条件（missing_principal/tenant_mismatch）在分支前由既有代码保证**——最小 diff，不复制校验。capability 阶梯对 delete 天然跳过（R7 证明零回归）。

### D3 — service 门禁：`authorize()` nil-provider 分支 + system 豁免

```go
if s.authorizer == nil {
	if action == access.ActionDelete {
		if principal, ok := access.PrincipalFrom(ctx); ok && principal.Kind == access.PrincipalSystem {
			return nil // system 豁免：AV quarantine（workers.go:33 SystemContext）等内部路径
		}
		return fmt.Errorf("%w: no authorization provider configured", ErrForbidden)
	}
	return nil // 读/写路径保持 CI/MVP 基线（I5）
}
```

- `ErrForbidden` → REST classify 403 `AccessDenied`（handler_helpers.go:55）、S3 errors.go:118 `AccessDenied`（adapter 门禁已先拒，service 侧仅竞态兜底）、MCP `errResult`、WebDAV 透传（x/net/webdav 既有映射）。
- 消息文本不泄漏内部细节（与既有 `decision.Reason` 表面化行为一致，非新增暴露面）。
- **system 豁免必须存在**：否则默认配置（无 provider）下 AV quarantine（`QuarantineObjectByID` → `authorizeObject(ActionDelete)`）403，隔离断裂（E18/R2）。豁免面仅限 `PrincipalSystem`——该 principal 只能由内部代码注入（workers.go:33、main.go:212、cmd/server/ai.go:127）。

### D4 — 漏斗面（R4 补全）：一处 `authorize()` 覆盖全部删除路径

| 路径 | 调用点 | 门禁落位 |
|------|--------|---------|
| REST `DELETE /v1/files/{key}[?hard=1]` | handler.go:245 → `svc.Delete` | service `authorize()`（file_delete.go:159） |
| REST `DELETE /v1/files/{key}?versionId=` / `{key}/versions/{v}` | → `svc.DeleteVersion` | :179 |
| REST `POST /v1/batch/delete`、folder 删除 | batch_handlers.go → `svc.BatchDelete` → 逐 key `s.Delete` | file_features.go:167 内 |
| **REST/S3 `DELETE` bucket** | rest/bucket_handlers.go:227、s3compat/s3_bucket_handlers.go:333 → `svc.DeleteBucket` | file_bucket_settings.go:40（bucket 级 ActionDelete）+ :48（逐对象）——**bucket 级同样 fail-closed**（R4） |
| S3 单删/版本删/delete-marker | delete.go:19/:29/:32（adapter 门禁已先拒） | service 侧为第二 AND 层 |
| WebDAV DELETE | dav.go:143 → `svc.Delete(hard=true)` | 同上 |
| WebDAV MOVE | dav.go:198 copy-then-delete | 见 D8（局部副作用） |
| MCP `delete_file` | server.go:311 → `svc.Delete(soft)` | 同上 |
| AV quarantine | object_worker.go:58 → `QuarantineObjectByID` | system 豁免（D3） |
| reconcile GC | 直连 repo/store（deletion.go:53,70） | **不在漏斗**（R6），不受影响 |

### D5 — S3/REST parity 不变量（第一约束）

S3 adapter 门禁（已落地）+ service 门禁（本设计）**AND 合取**，两者咨询同一 `*access.Manager`（main.go:94/215 + http.go:120 同对象）→ 同一请求在两侧的决策**逐字节一致**；mid-session 撤销（无缓存，每请求读 store）两侧同步翻转——`authz_parity_test.go` 已证明该性质，本设计**必须保持其通过**（D1 的直接后果）。任何一层 deny 不被另一层覆盖；S3 adapter 先拒时 service 门禁不可达（零性能叠加）。

### D6 — system 豁免对称性

Manager 侧（FR-2c）：`:27-29 trusted_system` 在删除分支**之前**，不动。service 侧（FR-3）：nil-provider 时 `PrincipalSystem` 放行（D3）。两处豁免语义对齐：`PrincipalSystem` 是内部信任边界（workers/main/ai 注入，R2 修正命名 `SystemContext`）。AC-2/AC-3 各带 system 正控断言防回归。

### D7 — 传输门禁与授予门禁分层（R3 修正）

- **传输层（不变）**：`checkScope`（auth_middleware.go:189-196）对 DELETE 恒要求 `ScopeWrite` 档；`Has` 对 admin 短路。`vault.file.delete` **不是**方法 scope，不进入 checkScope——admin 键经 `Has` 短路过传输，但**不获授予**（FR-1 guardrail：不得用 `Has(ScopeFileDelete)` 作授予判定）。
- **授予层（新）**：`authorizeDelete` 字面量检查（D1）。两层 AND：删除请求必须**同时**满足 ① 传输档（write/admin）② 授予（字面量 ∨ ActionDelete ACL）。
- 推论（R3）：正控 token 形如 `write+vault.file.delete`；`vault.file.delete` 单独不足以过传输（这不是缺陷，是 FR-1"权限非方法 scope"的必然结果，AC-4 据此构造）。

### D8 — WebDAV MOVE 局部副作用（R5）：文档化 + 可选一行预检

`dav.go:198` copy-then-delete：copy 事务先提交，随后 `svc.Delete` 被 FR-3 门禁拒绝 → **副本残留**（dest 已存在、src 未删）。这不是 FR-5 违例（FR-5 锁定删除事务本身，MOVE 预拷贝是独立写事务），但默认配置下 MOVE 从"完整成功"变为"copy 成功 + 403"——**已知失败模式**（§4），sibling webdav 轮负责正式修复。**可选缓解（本设计不强制）**：`dav.go:198` copy 之前调用 `svc.AuthorizeObjectAction(ctx, access.ActionDelete, srcObj)` 预检（该导出方法已存在，service/access.go:35-39），一行即可消除副作用面——若采纳，加 1 条 webdav 负向测试；范围守卫见 §8。

---

## 3. 兼容性约束

1. **生产默认行为翻转（本方向核心，刻意为之）**：`ACCESS_CONTROL_ENABLED=false`（默认，config.go:216）部署中，**所有对象删除（REST/S3/WebDAV/MCP/CLI/batch）与 bucket 删除一律 403**（R4 含 bucket 级）。读/写不受影响。依赖删除的存量默认部署必须执行 §5.3 运维迁移。
2. **`ActionAll` ACL 允许项不再授予删除**（D1 收窄）：现存部署若以 `Action: ActionAll` 授权删除，需改授 `Action: ActionDelete` 或字面量 token。仓库现存测试零使用该组合（已验证），风险仅限生产 ACL 数据；`ActionDelete` 精确允许项**不受影响**（与已合入 parity 验收一致）。
3. **access 启用部署的管理面收窄**：`ScopeAdmin`、owner 匹配、`tenant_default`、`vault.tenant_admin`/`vault.file_admin` 角色**不再**授予删除（FR-2b 排除清单）——今日这些主体可删，升级后 403。运维必须预授权（§5.3 步骤 1）。**这是比"provider 缺失"更隐蔽的翻转**：access 已启用的部署同样会断。
4. **读/写/restore/presign/分享/管理端点逐字节不变**（I5）；`requireAdmin`（admin.go:455-466）不动；bucket policy（IAM）语义不动（service 门禁是独立 AND 层）。
5. **传输门禁不变**：`checkScope`/`Has`/`ScopeRead/Write/Admin` 语义不动；`vault.file.delete` 仅作为权限词汇进入 `knownScope`/`parseScopeString`（持久化键 Scopes 列可往返任意 token，E8——**无 DB 迁移**）。
6. **错误面不变**：无新 HTTP 状态、无新 S3 错误码；403 复用 `ErrForbidden` → REST `AccessDenied`（handler_helpers.go:55）/ S3 `AccessDenied`（errors.go:118）/ MCP `errResult`；批删 per-key 报错沿用既有 `BatchDeleteResult.Error` 语义（200 外壳 + 逐 key 结果，REST batch 现状）。
7. **已合入 s3compat 轮验收保持通过**（第一约束）：`authz_parity_test.go`（PutACL ActionDelete 授予 + mid-session 撤销 + 双协议 parity）在 D1 下无需改动即通过；`s3compat/authz_gate_test.go`（AC-1..AC-7）中真实 Manager 用例（admin-scope principal 无授予 → 403）在 FR-2 下语义不变。**唯需复核**：`TestDeleteDeniedWhenNoPrincipal`（`missing_principal`）等用例——分支前置条件未动，保持通过。
8. **工程约束**：改动限于 `internal/auth`（+2 行 const +1 行集合）、`internal/access/authorizer.go`（≈40 行新方法）、`internal/service/access.go`（≈10 行分支）+ 注释；单文件 <500 行余量充足；纯 stdlib（I6）；无 SQL/schema（I2）；不触碰 key 校验（I3）与中间件链（I4）。

---

## 4. 失败模式

| 场景 | 行为 | 缓解 |
|------|------|------|
| provider 未设置（生产默认 `ACCESS_CONTROL_ENABLED=false`） | **全部删除 403**（fail-closed，方向核心） | §5.3 运维迁移；403 有明确错误码（REST `AccessDenied`）可观测 |
| access 启用但主体无授予（admin scope / owner / tenant_default / vault.*_admin 均不授予，FR-2b） | 403 `default_deny`/`resource_acl_no_match`（既有原因，Debug 日志含 reason） | §5.3 步骤 1 预授权：字面量 token（env 键/JWT roles）或 `PutACL(ActionDelete)` |
| `Manager` disabled 直连（仅测试可达：`buildAccessManager` 在配置关闭时返回 nil，生产组合拿不到 disabled Manager） | `ActionDelete` → 403 `access_control_disabled`（FR-2a）；读动作仍 `Allowed:true` | 语义变更仅影响直接构造 disabled Manager 的测试；AC-2 锁定 |
| ACL store 故障（`ListApplicableACL` error） | `denied("acl_store_error")` + err → service 包装后**非 ErrForbidden → 500**；删除不发生、零持久副作用（fail-safe） | 与今日读/写阶梯同形（非新暴露面）；store 健康由 /healthz/readyz 覆盖；窗口=请求内单次查询 |
| AV quarantine（默认配置） | `QuarantineObjectByID` → system 豁免放行（D3/D6） | **无豁免则隔离断裂**——AC-2/AC-3 system 正控回归护栏 |
| WebDAV MOVE（默认配置） | copy 提交、delete 403 → **dest 副本残留**（R5/D8） | 文档化已知限制（sibling webdav 轮正式修复）；可选一行预检（D8）；不影响 delete 事务零副作用（FR-5 断言范围=outbox/audit/事件/blob） |
| REST `batch/delete` 部分拒绝 | 200 外壳 + 逐 key `Error`（既有 `BatchDeleteResult` 语义）；被拒 key 不删 | 客户端必须检查逐 key 结果（文档注明）；与 S3 批删 per-key `AccessDenied`（已落地）同姿态 |
| `DeleteBucket` | bucket 级 `authorizeBucket(ActionDelete)` 先拒（file_bucket_settings.go:40 先于 :48）→ **零逐对象副作用** | 结构性成立（门禁先于对象循环） |
| 双门禁竞态（S3 adapter 放行后、service 门禁遇 ACL 变更） | service `ErrForbidden` → 403（既有路径） | AND 合取（D5）；任何一层 deny 不被覆盖 |
| 非删除路径误触发（实现回归） | 读/写被 403 破坏 | D2 分支条件 `action == ActionDelete` 精确；AC-2/AC-3 带读路径正控断言；回归锁定 |
| reconcile GC / replication / indexer | **不受影响**（直连 repo/store，R6；system 上下文，workers.go:33/51） | 零代码；AC 套件不覆盖 GC（范围外） |
| 回滚（env + 二进制**一起**，§5.3/§5.4） | 门禁消失：删除恢复旧语义 | 仅应急；正向部署优先；**仅回滚二进制不恢复放行**——若 AUTH_KEYS 仍含新 token：旧二进制 `Parse` 遇未知 scope → `failClosedRegistry`（enabled 注册表 + 零 env 凭据；`buildAuthRegistry` **log-and-continue，进程照常启动**，cmd/server/auth.go:14-18）→ **env 键主体认证 401/403**（store/JWT 部署的持久化键/JWT 键经 `Lookup` 落底仍可认证，F2；纯 env 部署全量锁死）。回滚必须**先清 AUTH_KEYS 再换二进制**（§5.4 顺序强制） |

> **回滚警告（本方向特有）**：旧二进制不认识 `vault.file.delete` scope。若 AUTH_KEYS（env）含该 token 配旧二进制运行，旧 `Parse` 遇未知 scope（auth.go:119）→ `failClosedRegistry`（:135-137）→ **env 键主体全部 401/403**（启动日志 `parse auth keys failed; authentication locked down`，进程照常启动，非 boot 失败——runbook 诊断依据）。**持久化键 Scopes 列 / JWT·Snaplink roles 惰性安全**（`parseScopeString` store.go:54-64 / `claimsToKey` jwt.go 零校验），旧二进制上仅是不被查询的 token——回滚只需清 AUTH_KEYS token，**勿删持久化键**（删键破坏认证）。比 s3compat 轮更严格（env 键无降级路径）。

---

## 5. 迁移步骤

### 5.1 代码迁移（单一提交，4 处改动 + 1 注释）

1. `internal/auth/auth.go`：`ScopeFileDelete Scope = "vault.file.delete"` const（:31-38 区）+ `knownScope`（:139）扩集合。
2. `internal/access/authorizer.go`：`Authorize` disabled 早退加删除分支（FR-2a）+ system 之后插入 `if action == ActionDelete { return m.authorizeDelete(...) }` + 新方法 `authorizeDelete`（§7.2）。
3. `internal/service/access.go`：`authorize()` nil-provider 分支（§7.3，含 system 豁免）。
4. `internal/service/file.go:97` 注释更新。
5. `make check` 全绿 + §6 新测试全绿。

### 5.2 测试迁移（FR-3 使无 authorizer 的删除一律 403；allow-all stub 按 harness 单点注入）

**stub 形态**（每包定义一次，8 行；s3compat 轮先例 allowAllProvider 同款；不新增生产符号——`_test.go` 跨包不可见，共享需导出生产符号，违背最小面原则，故按包内联）：

```go
type allowAllAuthz struct{}
func (allowAllAuthz) Authorize(context.Context, access.Principal, access.Action, access.Resource) (access.Decision, error) {
	return access.Decision{Allowed: true, Reason: "test_allow_all"}, nil
}
```

| 包 | 注入点（单点优先） | 受影响文件 |
|----|------------------|-----------|
| `internal/service` | harness：`service_test.go:31` 与 `quota_test.go:30` 的构造 helper（`NewFileService(store, repo, nil)` → `.WithAuthorizer(allowAllAuthz{})`） | file_delete_test.go、delete_marker_test.go、key_validation_test.go、object_protection_test.go、object_retention_test.go、object_version_delete_test.go、usage_consistency_test.go、service_test.go、quota_test.go（依赖 helper 的文件自动获得） |
| `internal/api/rest` | harness：`handlers_test.go:40` 构造点 | handlers_test.go（`TestPutGetDelete` :47、`TestBucketPolicyDenyDelete` :527）、legal_hold_test.go、enterprise_access_test.go、idempotency_test.go、idempotency_query_test.go、bucket_website_test.go、bucket_versions_test.go、buckets_test.go（`DELETE /v1/buckets`，R4）、acl_test.go、management_test.go、tags_test.go、conditional_test.go、admin_ops_test.go（凡经共享 harness 者自动获得；个别直接构造者按 `grep -n "NewFileService" internal/api/rest/*_test.go` 补） |
| `internal/api/webdav` | 4 个构造点：`dav_test.go:58`、`dav_test.go:856`、`dav_audit_test.go:52`、`dav_relay_test.go:69` | dav_test.go、dav_audit_test.go、dav_relay_test.go |
| `internal/mcp` | `helpers_test.go` 构造点 | server_test.go（delete_file 用例 :382-431） |
| `internal/integration` | `fullserver_test.go:73`（`svc := service.NewFileService(...)`） | fullserver_test.go（REST_CRUD DELETE :244-252）；**`authz_parity_test.go` 不动**（真实 Manager + PutACL ActionDelete 授予，D1 下通过——**迁移后必须全绿，这是 D1 的活断言**） |
| `internal/api/s3compat` | **零改动**（harness 已注入 allow-all stub，上轮完成） | — |

> 迁移后执行 `go test ./...` 全绿（硬门禁）。**禁改既有用例期望**：fail-closed 语义由 AC-2/AC-3/AC-4/AC-5 负向断言锁定（s3compat 轮同款纪律）。

### 5.3 运维迁移（生产部署）

1. **盘点**：`ACCESS_CONTROL_ENABLED` 当前取值（config.go:216，默认 false）；access 启用部署需盘点**当前依赖删除的主体**（admin scope 键、owner、tenant_default、`vault.file_admin`/`vault.tenant_admin` 角色——FR-2b 后全部失效，§3-3）。
2. **需要删除能力的部署**（二选一）：
   - (a) **启用 access 模块 + 预授权**：`ACCESS_CONTROL_ENABLED=true` + `ACCESS_SHARE_SECRET` ≥32 字节（缺失则 `NewManager` 硬失败 fail-loud，manager.go:44-45；config_validate.go:93 同检）+ 为删除主体授予，**按二进制阶段拆两臂**：
     - **交换前（旧二进制运行期，仅持久化面——唯一合法时点）**：Admin API 持久化键加字面量 scope `write+vault.file.delete`（`POST /v1/admin/keys`，`AddKey` 逐字接受 scopes、parseScopeString 零校验 → 旧二进制惰性往返，E8——**无迁移脚本**）；JWT/Snaplink roles 加 `vault.file.delete`；启用后 `PutACL(Action: ActionDelete, Effect: Allow)`（D1，细粒度按对象/前缀）。
     - **换新二进制后（可选）**：AUTH_KEYS 加字面量 token `write+vault.file.delete`——**唯一合法时点**（§5.3 步骤 3 P3）。
     - `ActionAll` ACL 授权需改授（§3-2）。
     - **不变量（本方向唯一的关键时序约束）**：字面量 `vault.file.delete` token 只能存在于**认识该 scope 的二进制（新二进制）运行期**的 AUTH_KEYS 中；旧二进制运行期仅允许经持久化键 Scopes 列 / JWT·Snaplink roles / ActionDelete ACL 授予。AUTH_KEYS 含新 token 跑旧二进制 ⇒ `Parse` 遇未知 scope → `failClosedRegistry`（auth.go:119,135-137）→ env 键认证锁死（§4 回滚警告；`buildAuthRegistry` log-and-continue，cmd/server/auth.go:14-18）。
   - (b) **接受删除被拒**：保持默认配置——删除 403 是刻意的安全姿态；依赖删除的工作流迁移到受控面或改造调用方。
3. **部署顺序（option (a)）——先后次序即安全与否**（P0→P3，每步有可执行验证点）：

   | 阶段 | 二进制 | 环境 | 动作 | 验证点 |
   |------|--------|------|------|--------|
   | **P0（交换前，可选）** | 旧 | 不变（仍 disabled） | `POST /v1/admin/keys`：持久化键 scopes=`["write","vault.file.delete"]`（`/v1/admin/keys` 注册于 `h.access` 门之外，router.go:339——授权可先于启用；`AddKey` 逐字接受 scopes，admin.go:114-142；**持久化要求 `AUTH_PERSIST_KEYS=true`**，否则键仅存内存、重启即失）；JWT/Snaplink roles 同理 | 新键认证 200；删除无门禁、行为不变（惰性，E8） |
   | **P1（旧二进制上）** | 旧 | `ACCESS_CONTROL_ENABLED=true` + `ACCESS_SHARE_SECRET`≥32B → 重启 | `PutACL(Action: ActionDelete, Effect: Allow)`（ACL 路由仅在 `h.access != nil` 时注册，router.go:301-303——启用后可达）；`ActionAll` allow 改授 `ActionDelete`（§3-2） | 旧阶梯已授权主体（admin/owner/tenant_default）删除仍 204——**无决策断点（仅对此类主体，F3）**；`ActionDelete` ACL 204（`actionMatches` 精确匹配，authorizer.go:81——**交换前即可验证**）；未授权主体 403——enable 窗口 = **读+写+删全动作** fail-closed 预警（同 s3compat 轮；DefaultPolicy=deny 时读/写也 403，非仅删除）；**FR-2b 收窄（admin/owner 不再授删）在窗口内不可见，只能在 P2 后验证**（F6）；可选 `ACCESS_DEFAULT_POLICY=tenant` 收窄窗口 |
   | **P2（交换）** | **新** | 不变 | — | 持久化字面量键删除 204 + outbox `vault.file.deleted@1.1`；admin scope 键翻转 403（FR-2b，预期）；ActionDelete ACL 204（parity 活断言） |
   | **P3（可选）** | 新 | **此刻起**才允许 AUTH_KEYS 加 `write+vault.file.delete`（可与 P2 同次重启合并，也可单独重启） | — | env 键删除 204 |

   - 代码+测试先合入，二进制随后；无数据迁移（I2 不涉及）、无配置格式变更、无 API 版本化。
4. **文档同步（随本版本合入）**：`AGENTS.md` §2.5 的 auth 描述与 §4 的 I5 基线说明（"nil authorizer preserves CI/MVP baseline" 对删除不再成立）；`docs/api.md` 若写明删除鉴权语义处同步。

### 5.4 部署后验证

- §6 全部新测试 + 既有套件全绿（`go test ./internal/auth/ ./internal/access/ ./internal/service/ ./internal/api/... ./internal/integration/` + `make check`）。
- 抽查：默认配置 `DELETE /v1/files/{k}?hard=1` → 403；`write+vault.file.delete` 键 → 204 且 `event_outbox` 出现 `vault.file.deleted@1.1`；`admin` scope 键 → 403 且零 outbox 行；access 启用 + `PutACL(ActionDelete)` → 204（parity 活断言）。
- **回滚（env + 二进制一起，§4 末行；顺序强制）**：① **先**从 AUTH_KEYS（env）移除 `vault.file.delete` token——换回旧二进制时 AUTH_KEYS 必须已无新 token（旧 `knownScope` 拒绝未知 scope → fail-closed 注册表 → env 键主体认证 401/403；持久化键/JWT 键仍可认证，F2）；② `ACCESS_CONTROL_ENABLED=false`；③ 换旧二进制。**持久化键 scope token / ActionDelete ACL / JWT roles 数据一律保留**（旧二进制惰性或同语义——删持久化键本体反而破坏认证，F5）。过渡期（新二进制 + disabled）删除仍 403（FR-3 fail-closed 瞬态，认证不受影响，system 豁免在），非锁死。回滚方向是放宽（旧二进制 + disabled → 删除恢复旧语义）。

---

## 6. 验收映射（测试 ↔ AC）

新测试落点：`internal/auth/auth_test.go`（AC-1）、`internal/access/access_test.go`（AC-2）、`internal/service/access_failclosed_test.go`（AC-3，新文件）、`internal/integration/`（AC-4/AC-5，复用 `authz_parity_test.go` 基建：`testShareSecret32`/`principalMW`/`outboxCountFor`/`outboxPayloadFor`）。stub 与真实 Manager 构造同 s3compat 轮（`ShareSecret` 32 字节字面量 `"0123456789abcdef0123456789abcdef"`，manager.go:44-45 硬校验）。

| AC（规格 §4） | 测试 | 判别断言 | HEAD 现状 | 修复后预期 |
|---|---|---|---|---|
| AC-1 词汇：`Parse` 接受 `vault.file.delete`，未知 scope 仍拒绝 | `TestParseScopeFileDelete`（auth_test.go 既有 `TestParse_*` 模式） | ① `auth.Parse("t:acme:vault.file.delete")` → err==nil、`reg.Enabled()`、`Lookup` 命中、`key.Has(auth.ScopeFileDelete)==true`；② `"t:acme:read+vault.file.delete"` → 双 scope；③ `"t:acme:read+bogus"` → err 含 `"unknown scope"` + fail-closed（`Enabled()==true`、Lookup miss）——**语义不变**；④ `knownScope(ScopeFileDelete)==true` 直接断言 | ① ② ③ 中 `vault.file.delete` 被拒 | ①②通过、③不变 |
| AC-2 Manager：disabled → 删除拒绝/读不变；启用后授予矩阵 | `TestManagerDeleteGate`（access_test.go，`testManager` harness :17-40 同款） | 表驱动：`Enabled:false` + ActionDelete → `Allowed==false`（reason `access_control_disabled`）；`Enabled:false` + ActionRead → `Allowed==true`（读不回归）；`Enabled:true`：Scopes`["admin"]` → **denied**（FR-2b）；Scopes`["vault.file.delete"]` → allowed；Roles`["vault.file.delete"]` → allowed；`PrincipalSystem` → allowed（FR-2c 护栏）；owner 匹配（`resource.OwnerID==SubjectID`）→ **denied**；`tenant_default` policy + write scope → **denied**（ActionDelete 不进 `scopeAllows`）；ACL `EffectDeny`（ActionDelete）→ denied 优先于字面量授予；ACL `ActionDelete` allow → **allowed**（D1）；ACL `ActionAll` allow → **denied**（D1 收窄）；无条目 → `default_deny` | 无删除分支：admin/owner/tenant_default/ActionAll 全放行 | 按矩阵 |
| AC-3 FileService：nil provider ⇒ 删除拒绝 + 读不回归 + system 豁免 | `TestDeleteFailsClosedWithoutProvider`（新文件 `internal/service/access_failclosed_test.go`；装配：SQLite repo + Migrate + local store + `NewFileService` 无 `WithAuthorizer`；`repo.PutObject` 直插预置；ctx `access.WithPrincipal(user)`） | ① `svc.Delete(..., hard=true)` → `errors.Is(err, ErrForbidden)`；`repo.GetObject` 仍命中；`HasEventOutboxFact(objID, EventTypeFileDeleted11)==false`（E17）；② 读不回归：`svc.Stat` 正常、`svc.ListObjects` 正常；③ system 豁免：`access.SystemContext(ctx, tenant)` 下 Delete → 成功（AV quarantine 护栏，E18/R2）；④ 正控：`WithAuthorizer(allowAllAuthz{})` 后 Delete → 成功 | ① 204 成功 | ① 403 + 零 outbox |
| AC-4 e2e：admin-scope 键无授予 ⇒ 403 + 零 outbox；`write+vault.file.delete` 键 ⇒ 204 + outbox | `TestRestDeletePermissionVocabulary`（`internal/integration/`；装配：`auth.Parse("admintok:default:admin,deletetok:default:write+vault.file.delete")` 注册表 + 真实 Manager（Enabled=true、DefaultDeny、ShareSecret 32B）+ `svc.WithAuthorizer(manager)` + `rest.NewRouter(..., authReg, ...)`；PUT 预置用 admin 键） | **负向**：`DELETE /v1/files/doc.txt?hard=1`（Bearer admintok）→ **403**（传输档 `Has(ScopeWrite)` 通过 → provider 拒绝——FR-2b 判别点）；`HasEventOutboxFact==false`；`GET` 仍 200。**正控（R3 修正）**：Bearer deletetok → **204** + `HasEventOutboxFact==true` + payload 含 `schema_version=1.1`/actor | 无词汇、无门禁：两键均 204 | 负向 403 零 outbox；正向 204 + outbox |
| AC-5 composition e2e：stub provider 替换 Manager，决策端到端生效 | `TestDeleteProviderReplaceable`（同 AC-4 harness，`svc.WithAuthorizer(stub)` 替换 `manager`——**FileService/REST 装配代码零改动**） | stub `Authorize` 捕获 `gotAction==access.ActionDelete`、`gotResource=={default,default,"doc.txt"}`；`allow=false` → 403 + 零 outbox + 对象仍在；`allow=true` → 204 + outbox 行存在；两轮间仅换 stub，装配代码逐字节不动（FR-4 可替换性证明） | 无缝可注入（file.go:96-99 已是接口） | 双态断言 |

**文件尺寸预算（硬门禁 <500 行/文件）**：`authorizer.go` 201→≈250（`authorizeDelete` ≈45 行）；`access.go`(service) 238→≈250；`auth.go` +2 行；`access_failclosed_test.go` ≈120；AC-4/AC-5 并入 integration 新文件 `delete_permission_test.go` ≈220。均余量充足。

---

## 7. 实现（最终代码形态）

### 7.1 `internal/auth/auth.go`

```go
const (
	ScopeRead         Scope = "read"
	ScopeWrite        Scope = "write"
	ScopeAdmin        Scope = "admin"
	ScopeFileDelete   Scope = "vault.file.delete" // 权限词汇：对象删除授予（FR-1）
)

func knownScope(scope Scope) bool {
	return scope == ScopeRead || scope == ScopeWrite || scope == ScopeAdmin || scope == ScopeFileDelete
}
```

> `Has`/`checkScope`/`Parse` 其余路径零改动：`vault.file.delete` 仅是注册表词汇（AUTH_KEYS 可携带、持久化 Scopes 列可往返）；**不得**在 `Has` 中并入（admin 短路），也不得在 `checkScope` 中用作方法 scope（FR-1 guardrail，R3）。

### 7.2 `internal/access/authorizer.go`

```go
func (m *Manager) Authorize(ctx context.Context, principal Principal, action Action, resource Resource) (Decision, error) {
	if !m.cfg.Enabled {
		if action == ActionDelete {
			return denied("access_control_disabled"), nil // FR-2a：删除 fail-closed
		}
		return Decision{Allowed: true, Reason: "access_control_disabled"}, nil
	}
	if principal.Kind == PrincipalSystem { /* trusted_system，不动 */ }
	if principal.SubjectID == "" { /* missing_principal，不动 */ }
	if !tenantMatches(principal, resource) { /* tenant_mismatch，不动 */ }
	if action == ActionDelete {
		return m.authorizeDelete(ctx, principal, resource) // FR-2b/c
	}
	// 既有通用阶梯（ACL/capability/owner/admin/tenant_default）逐字节不动
}

// authorizeDelete is the dedicated ActionDelete ladder (FR-2b): fail-closed,
// grant = literal "vault.file.delete" scope/role OR explicit ActionDelete ACL
// allow (D1); ActionAll ACL allow, admin scope, owner, tenant_default and
// administrator roles do NOT confer. Explicit deny always wins.
func (m *Manager) authorizeDelete(ctx context.Context, principal Principal, resource Resource) (Decision, error) {
	entries, err := m.store.ListApplicableACL(ctx, resource.TenantID, resource.Bucket, resource.Key)
	if err != nil {
		return denied("acl_store_error"), err
	}
	departments, err := m.store.ListSubjectDepartments(ctx, resource.TenantID, principal.SubjectID)
	if err != nil {
		return denied("directory_store_error"), err
	}
	matching := matchingEntries(entries, principal, departments, ActionDelete)
	if hasEffect(matching, EffectDeny) {
		return denied("explicit_deny"), nil
	}
	literal := slices.Contains(principal.Scopes, "vault.file.delete") ||
		slices.Contains(principal.Roles, "vault.file.delete")
	aclAllow := slices.ContainsFunc(matching, func(e ACLEntry) bool {
		return e.Effect == EffectAllow && e.Action != ActionAll
	})
	if literal || aclAllow {
		return Decision{Allowed: true, Reason: "delete_granted"}, nil
	}
	if len(entries) > 0 {
		return denied("resource_acl_no_match"), nil
	}
	return denied("default_deny"), nil
}
```

### 7.3 `internal/service/access.go` — `authorize()` nil-provider 分支

```go
	if s.authorizer == nil {
		if action == access.ActionDelete {
			if principal, ok := access.PrincipalFrom(ctx); ok && principal.Kind == access.PrincipalSystem {
				return nil // system 豁免：AV quarantine（workers.go:33）等内部路径（FR-3）
			}
			return fmt.Errorf("%w: no authorization provider configured", ErrForbidden)
		}
		return nil // 读/写路径保持 CI/MVP 基线（I5）
	}
```

### 7.4 `internal/service/file.go:97` 注释

```go
// WithAuthorizer enables resource-level authorization at the FileService
// boundary. A nil authorizer preserves the CI/MVP baseline for read/write;
// object deletion is fail-closed when no provider is configured (FR-3).
```

### 7.5 验证序列

```
gofmt -l internal/auth/ internal/access/ internal/service/   # 无输出
go build ./...
go vet ./...
go test ./internal/auth/ ./internal/access/ ./internal/service/ ./internal/api/rest/ ./internal/api/webdav/ ./internal/mcp/
go test ./internal/integration/   # 含 authz_parity_test.go（D1 活断言）与 s3compat 套件
make check
```

---

## 8. 范围守卫（与规格 §5 一致，不扩展）

不做：s3compat 端口改造（已落地，本设计只保持共享决策源与 parity）；`checkScope`/方法派生 scope 语义改动（FR-1 明示）；`PersistedKey` 加 Roles / DB 迁移（Scopes 列可往返，E8）；`requireAdmin` 改动（admin 路由权限与对象权限正交，E11）；bucket policy（IAM）语义改动；WebDAV/MOVE 正式改造（sibling 规格；本设计仅文档化 R5 局部副作用 + 可选一行预检 D8——若采纳预检，属 webdav 轮合入面，需其测试）；reconcile GC 路径（直连 repo/store，R6，系统级清理绕过用户门禁是既有设计）；@1.1 outbox/事件 schema；`access.Manager` 通用阶梯的非删除行为（含 `scopeAllows`、`capabilityAllows`、`actionMatches` 的 ActionPreview/Download 规则——`actionMatches` 仅新增 `ActionAll` 排除面，见 D1 实现处）。

**强制边界 = 删除动作（`ActionDelete`，types.go:76）**：读（Stat/Get/List/Preview/Download）、写（PUT/Create/Write）、Restore、Share、ManageACL、Publish、Export 及全部 bucket 子资源在 provider 缺失/禁用时的行为不变（I5）——AC-2/AC-3 各带读路径正控断言锁定。
