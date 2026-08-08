# 方向：`vault.file.delete` 权限 —— 覆盖全部删除路径的 fail-closed AuthorizationProvider 端口（验收规格 · 已验证现状）

> **模块：** `internal/events`（事件/审计/outbox 副作用契约）+ `internal/access`（PDP/端口）· `internal/service`（删除漏斗）· `internal/api/rest`（admin 删除端点）· `cmd/server`（装配）
> **来源分析：** `docs/auto/analyses/internal-events-495038b5.json`（方向 3）· **日期：** 2026-08-06 · **HEAD：** `acfaaf4`（核验基准 = HEAD + 工作树未提交改动；本文引用的 `admin_files_delete.go` 等为工作树新增文件）
> **评分：** 价值 7 / 风险降低 9 / 工作量 5 / 置信度 8
> **前置文档：** `docs/requirements/fail-closed-vault-file-delete-v1.design.md`、`authorizationprovider-vault-file-delete-{core,cli,s3compat,webdav}-v1.md`（同方向族设计，未实现/部分实现）；S3 边界端口已由前轮 campaign 落地（§2.1）
> **状态声明：** 本文是**验收契约**而非绿地设计：逐条核验方向引证（§1）、登记已落地机制与真实缺口（§2）、原样保留六条验收检查并映射到已存在/新增测试（§4）。超范围项一律不做（§5）。

---

## 1. 证据核验（方向引证逐条对照当前 HEAD）

| # | 方向引证 | 当前 HEAD 位置 | 核验结论 |
|---|---------|----------------|---------|
| E1 | `internal/access/authorizer.go:10-12` — `Authorizer` 接口；配置路径经 `denied('default_deny')` fail-closed | `Authorizer` 接口 :10-12；`Manager.Authorize` :14-63，末级 `denied("default_deny")` :62 | ✅ **精确**。注意 :20-21 另有一处 **fail-open 早退**（`cfg.Enabled==false` → `Allowed:true, reason "access_control_disabled"`）——即方向所指 `manager.go` 门禁，见 E6 |
| E2 | `internal/access/types.go:67-82` — `ActionDelete 'object:delete'`，映射到 `vault.file.delete` 为提议 | `ActionDelete Action = "object:delete"` :76（常量块 :66-83，`ValidAction` :85-93） | ✅ **行号精确**。代码中**无** `"vault.file.delete"` 字面量（全仓 grep 仅命中 `internal/api/s3compat/authz.go:10` 与 `policy.go:67` 的注释）——映射仍属提议（FR-3） |
| E3 | `internal/service/access.go:66-101` — `authorize`：nil authorizer → allow；deny → `ErrForbidden` | `authorize` :83-101：`s.authorizer == nil` → `return nil` :91；provider error → 包装为非 `ErrForbidden` 错误 :95-97；`!decision.Allowed` → `ErrForbidden` :99-100 | ✅ **行号漂移**（66→83），语义一致。**provider error 不包 `ErrForbidden`** ⇒ REST `classify` 落入默认分支 → **500**（`handler_helpers.go:26-61`，default :59-61）——"error → 500, never allow" 结构性成立（AC-3） |
| E4 | `internal/service/file_delete.go:109-117,143-149` — Delete/DeleteVersion 已路由经 `authorizeObject(ActionDelete)` | 行漂移：:109-116 现为 `deleteAuditEntry`、:123-143 现为 `deleteFacts`；**强制点**：`Delete` :159（先于 hard/soft 分支）、`DeleteVersion` :179 | ✅ 语义成立。当前**全部**删除路径都经 `authorizeObject(ActionDelete)` 单一漏斗：`Delete` :159 · `DeleteVersion` :179 · `CreateDeleteMarker` :34-37 · `QuarantineObjectByID`（AV）`object_worker.go:58` · bucket 设置 `file_bucket_settings.go:48` · `BatchDelete` → 逐 key `Delete`（`file_features.go:164-180`） |
| E5 | `internal/service/delete_marker.go:58` — delete marker 也发 `EventDeleted`，须过同一检查 | `s.emit(ctx, marker, repository.EventDeleted)` :58；授权检查 :34-37（current 存在 → `authorizeObject`，否则 `authorizePath`），先于 `InsertDeleteMarker` :44 与 chunk 清理 :53-56 | ✅ **行号精确**。检查在副作用之前 ⇒ 拒绝路径零副作用（AC-5） |
| E6 | `internal/access/manager.go` — `cfg.Enabled` `'access_control_disabled'` 门禁，提议反转为显式 opt-out | `Config.Enabled` `manager.go:22-25`；门禁实际在 `authorizer.go:20-21`（`!m.cfg.Enabled` → `Allowed:true`） | ✅ 语义一致。**生产装配下该门禁不可达**：`cmd/server/access.go:11-19` `buildAccessManager` 在 `!cfg.Access.Enabled` 时返回 **nil**（`ACCESS_CONTROL_ENABLED` 默认 false，`config.go:215-217`）⇒ 实际到达 service 的是 **nil provider**（E3），Manager-disabled 分支仅测试可达。两个 fail-open 点都须反转（FR-1/FR-2） |
| E7 | `internal/repository/sql_objects_maint.go:30-40` — share/public_asset/ACL 失效已随删除事务化；RAG chunk 清理在事务外、非阻断 | `SoftDeleteObject` :20-41（`BeginTx` :22，`deleteObjectAccessState` :36，`Commit` :40）；失效语句在 `internal/repository/sql_access_cleanup.go:27-40`（shares/public_assets `DELETE` :31-33 + `resource_acls` :35-40）；`WithEvent` 变体同事务（`event_outbox.go:102-145/:147-184`）；`ChunkCleaner` 接口 `service/file.go:59-63`，硬删调用 `file_delete.go:27-30`（**事务前**、`Warn` 后继续） | ✅ **行号精确**，语义一致。推论：授权（E4）在删除事务与 chunk 清理**之前** ⇒ 拒绝路径对象、share、chunk 全部原样（AC-6） |
| E8 | `internal/api/rest/router.go:187-197` — "no admin file-delete route — verified" | ❌ **被推翻**：`DELETE /v1/admin/files/{tenant}/{key}` 已存在——路由表 `router.go:203`、注册 `:352`（`r.Delete("/admin/files/{tenant}/*", adm.DeleteFile)`）、handler `internal/api/rest/admin_files_delete.go`（工作树新增）、测试 `admin_files_delete_test.go:143-216` | ❌ **引证失效**（方向快照之后落地）。端点经 `svc.Delete` → `authorizeObject(ActionDelete)`，**不是绕过**；"新的 admin 删除"剩余工作是证明其服从外部 provider（AC-6） |

**方向问题陈述核验（当前状态）：**

| 陈述 | 核验 |
|------|------|
| "Delete authorization is not fail-closed by default: FileService.authorize returns allow when authorizer is nil (baseline CI path)" | ✅ **仍成立（核心缺口）**：`access.go:91` 未改。默认装配（`ACCESS_CONTROL_ENABLED=false` → nil provider）下 REST/WebDAV/admin/batch 删除零权限判定 |
| "access.Manager is a concrete ACL store implementation rather than a replaceable provider port" | ⚠️ **部分成立**：service 边界已是接口（`WithAuthorizer(access.Authorizer)` `file.go:96-99`；装配 `main.go:94,215` 注入 `*access.Manager` 或 nil）；S3 边界端口已落地（§2.1）。**组合缝可替换性未被端到端证明**（AC-6） |
| "permission name 'vault.file.delete' does not exist (actions are 'object:delete' — mapping proposed)" | ✅ **仍成立**：无字面量；S3 端口注释 `authz.go:10` 已命名该映射（FR-3 收口） |
| "no admin file-deletion endpoint exists today" | ❌ **已被推翻**（E8） |

---

## 2. 现状：已落地机制 vs 真实缺口

### 2.1 前轮 campaign 已落地（本方向组成部分，不再重复实现）

| 机制 | 位置 | 验证 |
|------|------|------|
| **S3 边界 fail-closed 端口** `AuthorizationProvider`（形状 = `access.Authorizer`，`*access.Manager` 结构化满足）：nil → deny、provider error → deny（Warn 不泄露）、非 allow → deny | `internal/api/s3compat/authz.go:9-36`；装配 `cmd/server/http.go:120`；单对象门 `policy.go:67-75`（`s3:DeleteObject`）；批量 `?delete` per-key 门 `extra.go:439-444` | ✅ 读取全文；测试 `authz_gate_test.go`（AC-1 nil→403、AC-2 error→403 **非 500**、批量 200 壳 + per-key AccessDenied :235-260） |
| **admin 文件删除端点**：`DELETE /v1/admin/files/{tenant}/{key}?hard=1`，`requireAdmin`、tenant 显式必填（空 → 400，防默认租户误删）、经 `svc.Delete` 走 `authorizeObject(ActionDelete)` | `internal/api/rest/admin_files_delete.go`（工作树）；路由 `router.go:203,352`；测试 `admin_files_delete_test.go`（匿名 401 / 路由透传 / hard 语义） | ✅ |
| **拒绝路径零副作用**（service 与 REST 边界已锁定）：deny → `ErrForbidden`、零 outbox 事实、零 bus 广播、零 audit 行、对象原样 | `TestDeleteDenied_NoOutboxRow_ObjectUntouched` `internal/service/file_delete_test.go:68-98`；`TestRESTDeleteDenied_403_NoOutbox` `internal/api/rest/authz_delete_denied_test.go:23-107`（403 + `AccessDenied` + reason 进 message + GET 200 存活） | ✅ 两测试均断言 `HasEventOutboxFact` 两类型为假 |
| **删除事务化副作用**：audit（`AuditActionFileDelete="file.delete"` `repository/audit.go:10-13`）+ deleted@1.1/notify@1.1 事实 + share/public_asset/ACL 失效同事务（0041 `WithEvent` 变体） | `event_outbox.go:102-222`；`deleteAuditEntry`/`deleteFacts` `file_delete.go:100-143` | ✅（上轮 outbox 规格已验收） |

### 2.2 真实缺口（本规格 FR 范围）

| # | 缺口 | 位置 |
|---|------|------|
| **G1** | service 侧 fail-open：nil provider + `ActionDelete` → allow（默认装配全删除路径零判定） | `internal/service/access.go:91` |
| **G2** | PDP fail-open：`Manager` `cfg.Enabled=false` → 全动作 `Allowed:true`（含删除），且无"显式 opt-out"通道 | `internal/access/authorizer.go:20-21` |
| **G3** | 权限映射 `vault.file.delete` ↔ `ActionDelete` 无代码表达、无测试 | `types.go:76`（仅注释层提及） |
| **G4** | deny 路径单测只覆盖 `Delete`；`DeleteVersion`、delete-marker 路径无 deny 断言 | `file_delete_test.go:68` 仅 Delete；`delete_marker_test.go`/`object_version_delete_test.go` 无 deny 用例 |
| **G5** | 组合缝可替换性（非 Manager 的外部 provider 拒绝 **admin** 删除 → 对象/share/chunk/audit 全原样）无端到端证明 | 无（`admin_files_delete_test.go` 只测 auth/透传） |

---

## 3. 需求规格（FR，范围严格限定于方向）

### FR-1：service 侧 fail-closed 默认（G1）

`FileService.authorize`（`access.go:83-101`）在 `s.authorizer == nil` 且 `action == access.ActionDelete` 时返回 `ErrForbidden`（原因串建议 `"no authorization provider configured"`）；**其他 action 保持 nil ⇒ allow**（CI/MVP 读路径基线不回归）。

- **显式 opt-out（方向原文 "unless access_control_disabled is explicit"）**：新增配置 `ACCESS_DELETE_FAIL_CLOSED`（bool，**默认 true**；`docs/configuration.md` + `.env.example` 同步）。`false` = 操作者显式声明 access control 关闭，恢复 legacy 行为（nil → allow）。
- **system 豁免保留**：ctx principal 为 `PrincipalSystem` 时 nil-provider 亦放行（与 `Manager` 的 `trusted_system` 早退 `authorizer.go:23-24` 对称）——AV quarantine 路径 `object_worker.go:58` 由 `access.SystemContext` 驱动（`workers.go:33`），默认配置下必须继续工作。
- 单一漏斗（E4）⇒ 一处修改覆盖 REST/S3(service 层)/WebDAV/admin/batch/delete-marker/DeleteVersion/quarantine 全部路径。

### FR-2：PDP fail-closed 反转（G2）

`Manager.Authorize`（`authorizer.go:14-63`）的 disabled 早退（:20-21）对 `ActionDelete` 改为 `denied("access_control_disabled")`；读动作保持今日行为（disabled ⇒ `Allowed:true`）。同一显式 opt-out（FR-1）关闭时恢复 allow。`PrincipalSystem` 豁免保持最优先（顺序：opt-out → system → disabled 分支 → 既有阶梯）。

### FR-3：权限映射收口（G3）

`internal/access` 增加唯一映射常量 + 表：`PermissionVaultFileDelete = "vault.file.delete"` ↔ `ActionDelete`（映射函数/表一处定义，`s3compat/authz.go` 注释所指的同一映射）。删除决策**必须**以 `access.ActionDelete` 送达 provider（捕获断言，AC-4）；不做权限注册表、不改 `auth.Scope` 封闭集合（§5）。

### FR-4：provider error → 500，never allow（REST/service 侧）

保持 `authorize` :95-97 的包装（非 `ErrForbidden`）⇒ `classify` 默认分支 500（`handler_helpers.go:59-61`）；以测试锁定：error 不转为 allow、不产生任何副作用、HTTP 层为 500 而非 403 泄露语义。S3 边界已落地 error→403（`authz_gate_test.go:235`，R2 决定），两边界都满足 "never allow"，**不改已落地 S3 行为**（§5）。

### FR-5：被拒删除零副作用（全三路径）

拒绝路径（`Delete`/`DeleteVersion`/delete-marker/admin）零 outbox 行、零 `vault.file.deleted@1.1` 事实、零 audit 行、零 bus 广播、对象/share/chunk 原样。`Delete` 已有测试（§2.1）；`DeleteVersion` 与 delete-marker 为 legacy 路径（**从不**写 outbox 事实，事务性 outbox 规格 E14），断言锁定"denied 不改变该不变量"。

### FR-6：组合缝端到端证明（G5）

composition e2e：向 `svc.WithAuthorizer`（`file.go:96-99`，接口）注入**非 `*access.Manager`** 的外部 provider（L1 适配器 test-double），admin 删除端点全程服从该 provider 的 deny/allow（AC-6）。

---

## 4. 验收标准（方向原文六条，原样保留并测试化）

### AC-1 provider deny → `ErrForbidden`（Delete、DeleteVersion、delete-marker 三路径）

> 方向原文：*unit: provider deny → ErrForbidden for Delete, DeleteVersion, and delete-marker paths*

| 断言 | 测试 | 位置 |
|------|------|------|
| deny provider + `Delete` → `ErrForbidden`，零副作用 | `TestDeleteDenied_NoOutboxRow_ObjectUntouched`（`denyAuthorizer` :57-63） | ✅ 已存在 `internal/service/file_delete_test.go:68` |
| deny provider + `DeleteVersion` → `ErrForbidden`；对象版本原样、零 outbox 事实、零 bus 广播、零 audit 行 | **新增 T-2**（`object_version_delete_test.go`，`svc.WithAuthorizer(denyAuthorizer{})` → `DeleteVersion` → `errors.Is(err, ErrForbidden)`；`ListObjectVersions` 仍 1 行；`HasEventOutboxFact` 双类型 false；订阅通道零事件） | 待实现 |
| deny provider + `CreateDeleteMarker` → `ErrForbidden`（两分支：current 存在 → `authorizeObject`；current 缺失 → `authorizePath`）；无 marker 行 | **新增 T-3**（`delete_marker_test.go`：两子用例；`GetObject` 后断言无 tombstone 行、`HasEventOutboxFact` false） | 待实现 |

### AC-2 nil provider → deny（fail_closed），显式 opt-out 例外

> 方向原文：*nil provider → deny (fail_closed) unless access_control_disabled is explicit*

| 断言 | 测试 | 位置 |
|------|------|------|
| 无 `WithAuthorizer`（默认 CI 装配）+ `Delete` → `ErrForbidden`，对象原样 | **新增 T-4**（`file_delete_test.go`，`newTestSvc` 不注入 authorizer；负向断言 + 对象存活） | 待实现 |
| 同装配 + `DeleteVersion` / `CreateDeleteMarker` → `ErrForbidden` | **新增 T-4b**（并入 T-4 表驱动） | 待实现 |
| 显式 opt-out（`ACCESS_DELETE_FAIL_CLOSED=false` 语义注入点）→ 恢复 allow（legacy 基线） | **新增 T-5**（装配层/配置解析单测：opt-out 为 true 时 FR-1/FR-2 生效、false 时放行；config 解析默认 true） | 待实现 |
| `Manager` `cfg.Enabled=false` + `ActionDelete` → `denied("access_control_disabled")`；同 Manager 读动作 → `Allowed:true`（I5 读路径不回归） | **新增 T-5b**（`internal/access` 直构 Manager，沿用 `authorizationprovider-auditsink-delete-v1.design.md` F4 配方） | 待实现 |
| system 豁免：`PrincipalSystem` + nil provider 删除 → 放行（AV quarantine `object_worker.go:58` 不回归） | **新增 T-6**（`service`：`access.SystemContext(ctx, tenant)` + 无 authorizer → `QuarantineObjectByID` 成功路径不受 FR-1 影响） | 待实现 |

### AC-3 provider error → 500，never allow

> 方向原文：*provider error → 500, never allow*

| 断言 | 测试 | 位置 |
|------|------|------|
| error provider（`errProvider`：`Authorize` 返回错误）+ `Delete` → 返回**非** `ErrForbidden` 错误；对象、outbox、audit 全原样 | **新增 T-7**（service 层，`file_delete_test.go`） | 待实现 |
| REST 边界：同一装配 DELETE `/v1/files/{key}` → HTTP **500** `InternalError`（`classify` 默认分支），非 403、非 allow | **新增 T-7b**（httptest，镜像 `authz_delete_denied_test.go` 装配但注入 error provider） | 待实现 |
| S3 边界 error → 403 已锁定，不改（不对称是 R2 决定） | `TestDeleteProviderErrorIs403Not500` | ✅ 已存在 `internal/api/s3compat/authz_gate_test.go:235` |

### AC-4 权限映射测试：`vault.file.delete` → `ActionDelete`

> 方向原文：*permission mapping test: vault.file.delete → ActionDelete*

| 断言 | 测试 | 位置 |
|------|------|------|
| 映射单测：`PermissionVaultFileDelete` ↔ `ActionDelete`（FR-3 常量/表）；映射表唯一、无其他 action 撞名 | **新增 T-8**（`internal/access` 映射所在文件 + 测试） | 待实现 |
| 捕获断言：provider 在 `Delete`/`DeleteVersion`/delete-marker/admin 删除上收到的 action **恒为** `access.ActionDelete`（捕获型 stub 记录 `gotAction`） | **新增 T-8b**（并入 T-2/T-3/T-11 的 stub，各自断言 `gotAction == access.ActionDelete`） | 待实现 |

### AC-5 outbox：denied 删除零 outbox 行、零 `vault.file.deleted@1.1`

> 方向原文：*outbox: denied delete produces no outbox row and no vault.file.deleted@1.1*

| 断言 | 测试 | 位置 |
|------|------|------|
| deny + `Delete` → `event_outbox` 零行、`HasEventOutboxFact` 双类型 false、零 bus 广播、零 audit | `TestDeleteDenied_NoOutboxRow_ObjectUntouched`（:83-98 断言 outbox + `ListAudit` 空） | ✅ 已存在 `internal/service/file_delete_test.go:68` |
| REST 边界 deny → 同断言 + 对象可继续 GET | `TestRESTDeleteDenied_403_NoOutbox` | ✅ 已存在 `internal/api/rest/authz_delete_denied_test.go:23` |
| deny + `DeleteVersion` / delete-marker → 零 outbox 行（legacy 路径本不写事实；断言 denied 不改变不变量） | T-2/T-3 内嵌断言（`CountEventOutbox` 或 `HasEventOutboxFact` 双 false） | 待实现 |

### AC-6 composition e2e：外部 provider 拒绝 admin 删除 → 对象/share/chunk 原样、无 audit

> 方向原文：*composition e2e: external AuthorizationProvider (L1 adapter) denies an admin file deletion → object, shares, and chunks remain intact and no audit event is published*

| 断言 | 测试 | 位置 |
|------|------|------|
| 装配：`svc.WithAuthorizer(externalStub)`（**非 `*access.Manager`**）+ REST router（含 `/admin/files`）+ recording `ChunkCleaner`；admin PUT 建对象 + `POST /v1/shares` 建 share（断言 share 行存在）；**stub deny** + `DELETE /v1/admin/files/{tenant}/{key}?hard=1` → 403 | **新增 T-11**（镜像 `admin_files_delete_test.go` 装配 + `authz_delete_denied_test.go` 的 manager 替换为外部 stub） | 待实现 |
| deny 后：`GetObject` 存活 · share 行仍在（`ListShares`）· `ChunkCleaner.DeleteObjectChunks` **零调用**（recording stub）· `ListAudit` 空 · `event_outbox` 零行 | 同上（T-11 断言段） | 待实现 |
| 正控制：stub allow → 204；对象消失、share 清理（`deleteObjectAccessState` 事务内）、audit 1 行（`file.delete`）、outbox 2 事实（deleted@1.1 + notify@1.1） | 同上（T-11 正控制段） | 待实现 |
| 捕获断言：stub 收到 `tenant`（路径值，非默认租户）、`ActionDelete`、`Resource{Kind: object}`（AC-4 联动） | 同上 | 待实现 |

---

## 5. 范围边界（明确不做）

| 不做 | 理由 |
|------|------|
| S3 边界端口改造（error→403 等） | 已落地（§2.1）；本规格只要求 REST/service 与它共享同一决策源与 "never allow" 不变量 |
| `auth.Scope` 词汇/`checkScope`/`PersistedKey` 改动 | `vault.file.delete` 是 provider 层权限，非方法 scope（core 设计 FR-1 属另一分析方向，不在本方向验收内） |
| `Manager` 允许阶梯改造（owner/admin/tenant_default 是否授予删除、字面量 role 授予） | 本方向验收仅覆盖 nil-provider 与 disabled 门禁反转；阶梯语义属 `authorizationprovider-vault-file-delete-core-v1.md` 的 FR-2b 范围 |
| delete-marker/`DeleteVersion` 路径补写 outbox 事实 | 事务性 outbox 规格已明示 legacy 路径超范围（其 E14）；本规格只要求 denied 不改变现状 |
| `requireAdmin` 语义、bucket policy、WebDAV/MOVE 适配层 | 属既有设计文档范围；经 FileService 漏斗（E4）自动获得 FR-1 门禁 |
| outbox 交付/relay/notify@1.1/AI 管线 | 与本方向无关 |

---

## 6. 基线影响（FR-1 的既有测试迁移，须随实现一并处理）

FR-1 使**无 authorizer 的删除一律 403**（默认装配）。当前直接构造 `service.NewFileService(store, repo, logger)`（无 `WithAuthorizer`）并执行删除的测试包：`internal/service`（file_delete_test.go、delete_marker_test.go、object_version_delete_test.go、object_protection_test.go、usage_consistency_test.go、quota_test.go 等）、`internal/api/rest`（files 删除用例）、`internal/api/webdav`（dav_relay_test.go、dav_audit_test.go）、`internal/integration`（fullserver_test.go）。

**迁移模式**（沿用 s3compat 轮先例，`authz_gate` 已对既有删除用例显式注入 allow-all provider）：

1. 测试装配统一注入 allow-all test-double（`svc.WithAuthorizer(allowAllStub{})`，对任意 action 放行，保持基线行为）。
2. 新 fail-closed 语义由 AC-1/AC-2/AC-3 负向断言锁定，不改既有用例期望。
3. 全仓 `go test ./...` + `make check` 保持全绿（硬门禁）；`docs/configuration.md`/`.env.example` 同步 `ACCESS_DELETE_FAIL_CLOSED`。
