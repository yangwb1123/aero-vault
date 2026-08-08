# 方向：AuthorizationProvider 端口（fail_closed）在 admin-delete 边界强制 `vault.file.delete`

> **模块：** `internal/jobs`（组合面：`internal/api/rest` + `internal/cli` + `internal/access` + `internal/service` + `internal/events` + `internal/repository` + `cmd/server`）· **来源分析：** `docs/auto/analyses/internal-jobs-3fcb0121.json` · **日期：** 2026-08-06
> **评分：** 价值 8 / 风险降低 10 / 工作量 4 / 置信度 8
> **验证基准：** 工作树 = 当前 HEAD（`go build ./...` 退出码 0；`go test ./internal/api/rest ./internal/jobs ./internal/access` 绿）。本文所有引用均已逐行对照工作树验证；分析快照的 3 处断言已被先期 campaign 轮次**证伪/部分证伪**（§2.2 E1/E2/E7）。
>
> **本文是增量规格：** 方向的 7 条证据中，权限名（`vault.file.delete`）、service 层删除 fail-closed 门禁、admin 文件删除表面、s3compat 边界 AuthorizationProvider 端口先例均已落地；**剩余缺口集中在 admin-delete 边界本身**：无端口、无 default-deny 适配器、provider 错误呈现 500、权限矩阵未强制、审计事实不含权限名、组合硬编码。FR 是**缺口增量**，不是绿地设计；与已 gate 的 sibling 规格 `authorizationprovider-vault-file-delete-core-v1.md` / `authorizationprovider-auditsink-delete-v1.md` 同契约，语义对齐不重复设计。

---

## 1. 问题陈述

组合契约要求：权限 `vault.file.delete` 经 **AuthorizationProvider 端口**以 **fail_closed** 语义在 **admin-delete 边界**强制；admin 文件删除（跨租户、metadata+object 状态）是操作员面新能力，**不得继承对象路径的宽松基线**。方向文验证的现状在 admin 边界上仍成立的部分与已落地部分如下：

1. **admin-delete 边界无端口（剩余核心缺口）：** `AdminHandler.DeleteFile`（`internal/api/rest/admin_files_delete.go:20-35`）只做 `requireAdmin`（auth registry scope 检查）后直调 `svc.Delete`——授权判定**隐式继承**对象路径的 `authorizeObject(ctx, access.ActionDelete, obj)`（`internal/service/file_delete.go:159`），边界本身**没有** AuthorizationProvider 端口、没有 default-deny 适配器、没有 `vault.file.delete` 权限咨询。
2. **provider 错误在 admin 边界呈现 500（fail-open 形态）：** `FileService.authorize` 对 provider error 仅 `fmt.Errorf("authorization decision: %w")`（`internal/service/access.go:106-108`），不包 `ErrForbidden`；`classify`（`internal/api/rest/handler_helpers.go:26-61`）default 分支 → **500 InternalError**。与 s3compat 边界先例（provider error ⇒ 403 `AccessDenied`，`internal/api/s3compat/authz.go:27-36`）不对称。
3. **权限矩阵未在 admin 边界强制：** `vault.file.delete` 权限名已存在（`internal/access/permissions.go:7`）但 admin 边界从不咨询它；对象路径的 `scopeAllows`（`internal/access/authorizer.go:150-177`）会把 write-scope 成员在 `DefaultTenant` 策略下授予 `ActionDelete`（:171-174）——"member denied vault.file.delete" 在 admin 边界不成立。
4. **审计事实不含强制权限名：** `deleteAuditEntry`（`internal/service/file_delete.go:96-104`）只记 `action="file.delete"` + `detail="hard"|"soft"`，无权限名。
5. **组合硬编码：** `cmd/server/http.go:120` `s3compat.NewRouter(svc, logger, accessManager)`、`NewAdminHandler(svc, repo, reg)`（`internal/api/rest/admin.go:34`）——无按名注册的 provider 槽位。

**可复用的 fail-closed 先例（已验证）：** s3compat 边界 `AuthorizationProvider` 端口（`internal/api/s3compat/authz.go:9-36`，nil ⇒ deny、error ⇒ deny、`*access.Manager` 结构化满足、7 个门禁测试）；service 层 FR-1 门禁（`internal/service/access.go:91-102`，nil authorizer + `ActionDelete` ⇒ `ErrForbidden`，`PrincipalSystem` AV 豁免）；`ACCESS_DELETE_FAIL_CLOSED` 默认 true（`internal/config/config.go:220`）。

### 触发场景（真实工作流）

1. `DELETE /v1/admin/files/{tenant}/{key}?hard=1`（REST）或 `aero-vault cli admin files delete <tenant> <key> --hard`（`internal/cli/cli_admin_files.go:27`）：授权判定隐式、无权限名、provider 故障时 500。
2. 合规审计：删除审计行（`audit_log`，`AuditActionFileDelete="file.delete"`，`internal/repository/audit.go:13`）无法按强制权限名 `vault.file.delete` 过滤。
3. 运维要组合**按名**选择 provider 适配器（sibling 项目适配器按名注册），而非硬编码 `accessManager`。

---

## 2. 现状与代码证据（已逐条验证）

### 2.1 工作树状态修正（分析快照之后的关键事实）

| 方向断言 | 当前状态 | 验证 |
|---|---|---|
| (a) `FileService.authorize` 在 `s.authorizer == nil` 时返回 nil（fail-open） | **部分证伪**：`internal/service/access.go:91-102` 已有 FR-1 门禁——nil authorizer + `ActionDelete` ⇒ `ErrForbidden`（:101，除非 `deleteFailOpen` 或 AV 豁免 `IsSystemDeleteExempt`）；**读/写路径** nil ⇒ 放行仍成立（:103） | ✅ 读全文 |
| (b) 无 `vault.file.delete` 权限 | **证伪**：`internal/access/permissions.go:7` `PermissionVaultFileDelete = "vault.file.delete"` + `ActionForPermission`（:16-26，映射到 `ActionDelete`）+ `IsSystemDeleteExempt`（:29-34） | ✅ 读全文 |
| (c) 无 admin 文件删除表面 | **证伪**：REST `AdminHandler.DeleteFile`（`admin_files_delete.go:20-35`，路由 `router.go:352`）+ CLI `admin files delete`（`cli_admin_files.go`）+ 单元测试（`admin_files_delete_test.go`）+ 集成测试（`internal/integration/admin_files_delete_test.go`）均已存在 | ✅ 读全文 |
| (d) 无 deny-by-default 适配器 | **部分证伪**：s3compat 边界已有（`s3compat/authz.go` nil⇒deny）；**admin 边界仍无** | ✅ 读全文 |
| (e) `internal/jobs` 与 admin 删除的关系 | admin 删除**同步执行**（`DeleteFile` → `svc.Delete`），不经过 `jobs.Queue`；`jobs.Queue.Enqueue`（`internal/jobs/jobs.go:90`）是 post-commit INSERT（`repository.EnqueueJob`，`internal/repository/jobs.go:46`）——拒绝必须先于一切持久副作用（含 jobs 行） | ✅ 读全文 + grep |

**推论：** 方向的"无权限名 / 无 admin 表面 / nil⇒allow"三项核心断言已不成立；**剩余缺口 = admin 边界无显式端口 + fail_closed 语义不完整 + 矩阵未强制 + 审计无权限名 + 组合硬编码**（§2.4 G1–G6）。

### 2.2 方向文证据验证表

| # | 方向文引用 | 验证结果 |
|---|-----------|---------|
| E1 | `internal/service/access.go`：`authorize` 在 `s.authorizer == nil` 时 `return nil`（fail-open） | ⚠️ **部分证伪**：:91-102 已有 FR-1 删除 fail-closed 门禁（`deleteFailOpen` + `IsSystemDeleteExempt`，deny 于 :101）；非删除动作 nil⇒allow 仍成立（:103）。"每当 nil 即放行"对 `ActionDelete` 不再成立 |
| E2 | `internal/access/types.go:76`：`ActionDelete = "object:delete"`，无 `vault.file.delete` action | ✅ 行号精确（:76）；❌ "无 vault.file.delete 权限" **证伪**——权限名在 `permissions.go:7`，经 `ActionForPermission`（:16-26）映射到 `ActionDelete` |
| E3 | `internal/access/authorizer.go:10-16`：`Authorizer` 接口 | ✅ 实际 :10-13（接口体 4 行；引用区间略宽） |
| E4 | `internal/access/authorizer.go:131`：role 检查 `vault.tenant_admin`/`vault.file_admin` | ✅ 实际 `isAdministrator` :136-144，role 比较 :141（漂移 +5..+10 行） |
| E5 | `internal/service/file_delete.go`：`Delete` → `authorizeObject(ctx, access.ActionDelete, obj)` | ✅ `Delete` :147，门禁 :159；`DeleteVersion` :174/:179 同型 |
| E6 | `cmd/server/main.go:94`：`WithAuthorizer` 装配 | ✅ 实际 :94-95 `WithAuthorizer(accessManager).WithDeleteFailOpen(!cfg.Access.DeleteFailClosed)`；`buildAccessManager`（`cmd/server/access.go:8-20`）在 `ACCESS_CONTROL_ENABLED=false` 时返回 nil |
| E7 | "no AdminDelete/admin-file-delete symbol exists anywhere" | ❌ **证伪**：`admin_files_delete.go`、`cli_admin_files.go`、路由 `router.go:352`（OpenAPI :203）、`internal/integration/admin_files_delete_test.go` 均存在 |

### 2.3 补充验证（本规格新增）

- **admin-delete 边界现状**：`AdminHandler.DeleteFile`（`admin_files_delete.go:20-35`）→ `requireAdmin`（`internal/api/rest/admin.go`，`auth.Registry.Enabled()` :143 + `ScopeAdmin`）→ `svc.Delete`。已有测试覆盖：requireAdmin 401/403/204（`TestAdminDeleteFile_RequireAdmin`）、hard/soft 语义与审计行（`TestAdminDeleteFile_RouteAndPassthrough`）、错误映射 F1–F8（403/tenant_mismatch/404/409/410/500 存储失败/500 审计失败全事务回滚）、`assertNoWriteSideEffects`（`admin_files_delete_test.go:134`，审计 + outbox 双零断言）。
- **s3compat 端口先例（按名注册的 sibling 形态的最近参照）**：`internal/api/s3compat/authz.go:9-36`——接口形状 = `access.Authorizer`；nil ⇒ deny；error ⇒ deny（403 `AccessDenied`，Warn 记日志，不泄漏）；`*access.Manager` 结构化满足。7 个门禁测试（`authz_gate_test.go`：`TestDeleteDeniedWithoutBucketPolicy` / `TestDeleteDeniedWhenNoPrincipal` / `TestDeleteProviderErrorIs403Not500` / `TestBatchDeletePerKeyDenial` / `TestDeniedDeleteWritesNoOutboxRows` / `TestDeniedDeleteEmitsNoEvent` / `TestDeleteDeniedWhenProviderUnset`）。**装配仍硬编码**（`cmd/server/http.go:120`）。
- **权限矩阵宿主**：`isAdministrator`（`authorizer.go:136-144`）= operator（`TenantID=="*"` 或 `Scopes` 含 `admin`）+ roles `vault.tenant_admin`/`vault.file_admin`；`tenantMatches`（:77-79）租户相等或 `"*"`；`scopeAllows`（:150-177）在 `DefaultTenant` 下按 scope/role 授予——**write-scope 成员可获 `ActionDelete`**（:171-174，对象路径语义，admin 边界不得继承）。
- **零副作用机器**：`event_outbox`/`event_outbox_delivered`（迁移 0041）、`HardDeleteObjectWithEvent`/`SoftDeleteObjectWithEvent`（删除+审计+事实同事务，`file_delete.go:46/:86`）、`HasEventOutboxFact`（rest 测试 `outboxFactCount` :105）、`EventTypeFileDeleted11`/`EventTypeFileNotify11`（`repository/event_outbox.go:22/:25`）。集成 harness：`startFullServerWithAuthAndRelay` + `putObjectAs`/`outboxCountFor`/`deliveredCountFor`/`assertAuditRowFor`。
- **错误映射**：`classify`（`handler_helpers.go:26-61`）：`ErrForbidden` → `AccessDenied`/403（:55）；其余 → 500（:60）。
- **超时**：`mw.RequestTimeout` 仅挂 AI 组（`router.go:258`，`REQUEST_TIMEOUT_SECONDS` 默认 120，`config.go:45/:80`）；admin 边界无 deadline——provider 挂起会无限阻塞删除请求。
- **jobs 模块**：`internal/jobs/jobs.go` Queue/Registry/Pool；job 类型注册 `cmd/server/workers.go:28/:46`、`cmd/server/ai.go:138/:145`；admin 删除不产生 job。**拒绝必须先于 `Queue.Enqueue`**（post-commit INSERT，`repository/jobs.go:46-`）。
- **I4**：全局中间件链（AGENTS.md §2.5 12 环）不变；admin 边界门禁是 handler 边界职责，不新增中间件环。
- **编译级现状**：`grep -rn "vault.file.delete" internal/api/rest/` → 零命中（admin 边界从未引用权限名）。

### 2.4 缺陷机理（剩余缺口）

| # | 缺口 | 现状 | 后果 |
|---|------|------|------|
| **G1** | admin-delete 边界无 AuthorizationProvider 端口 | `NewAdminHandler(svc, repo, reg)`（`admin.go:34`）无 provider 槽位；`DeleteFile` 隐式继承对象路径 `ActionDelete` | 边界不可组合、不可按名替换适配器；"admin 是新能力不得继承宽松基线"不成立 |
| **G2** | provider 错误/panic/超时在 admin 边界非 fail-closed 403 | `authorize` 不包 `ErrForbidden`（`access.go:106-108`）→ `classify` 500；无 deadline（`RequestTimeout` 仅 AI 组）；panic 由 recoverer 兜 500 | 与 s3compat 先例（error⇒403）不对称；"provider outage ⇒ delete blocked (403)"不成立 |
| **G3** | 权限矩阵未在 admin 边界强制 | 边界不咨询 `vault.file.delete`；`scopeAllows` 授 write-scope 成员对象删除（`authorizer.go:150-177`，write 授予 :171-174） | "operator/tenant_admin granted, member denied vault.file.delete"不可验证 |
| **G4** | 审计事实不含强制权限名 | `deleteAuditEntry` 只有 `detail=hard\|soft`（`file_delete.go:96-104`） | "audit fact recorded with the enforcing permission name"不成立 |
| **G5** | 组合硬编码 | `http.go:120` 直传 `accessManager`；admin handler 无槽位 | "registered by name, not hardcoded"无落点 |
| **G6** | 无 deny-by-default 适配器（admin 边界） | 边界无适配器概念（s3compat 有，`authz.go:23-26`） | "default-deny adapter returns 403 when provider absent"不可验证 |

---

## 3. 需求规格

### FR-1：admin-delete 边界 AuthorizationProvider 端口，fail_closed

- **FR-1a（端口形状）**：端口接口与 s3compat 边界同形（`internal/api/s3compat/authz.go:9-16`）：`Authorize(ctx, access.Principal, access.Action, access.Resource) (access.Decision, error)`；`*access.Manager` 结构化满足（零 wrapper）。`AdminHandler` 增加 provider 槽位（`NewAdminHandler` 签名扩展或等价的构造注入）；**不新增中间件环**（I4）。
- **FR-1b（fail-closed 语义）**：① provider 未设置（nil）⇒ 拒绝（default-deny 适配器）；② provider error ⇒ 拒绝，**一律 403 `AccessDenied`**（`ErrForbidden` 包装，`classify` :55 映射），服务端 Warn 日志、不向客户端泄漏 provider 内部细节（H5）；③ provider panic ⇒ 拒绝 403（recover 后按 deny 处理，不落 500）；④ provider 超时（调用受 deadline 约束，见 FR-1e）⇒ 拒绝 403；⑤ 仅显式 `Decision{Allowed:true}` 放行。所有 deny 路径**零副作用**（FR-3）。
- **FR-1c（门禁位置与顺序）**：判定先于 `svc.Delete` 与**一切持久副作用**——`event_outbox` 行、`audit_log` 行、`object_events`、EventBus 事件、`jobs` 表行（`jobs.Queue.Enqueue` 为 post-commit INSERT，`jobs.go:90`；拒绝不得到达 enqueue）。admin 删除保持同步语义（403 在 HTTP 响应边界返回），本方向**不**将其改为异步 job（§5）。
- **FR-1d（权限与资源）**：门禁以 `access.PermissionVaultFileDelete`（`"vault.file.delete"`，`permissions.go:7`）为强制权限名，经 `ActionForPermission`（:16-26）解析到 `ActionDelete`；resource = `{TenantID: 路径租户, Bucket: default, Key: 路径 key, Kind: object}`（跨租户：operator `"*"` 可跨，租户限定 principal 须 `tenantMatches`，`authorizer.go:77-79`）。
- **FR-1e（超时边界）**：provider 调用受请求 deadline 约束（复用请求上下文或边界自带 bound）；到期 ⇒ deny 403。**admin 请求不得因 provider 挂起无限阻塞**（今日 `RequestTimeout` 仅挂 AI 组，`router.go:258`）。

### FR-2：权限矩阵（admin 边界契约）

- **FR-2a（授予集）**：admin 边界的 `vault.file.delete` 仅授予：operator（`Principal.TenantID=="*"` 或 `Scopes` 含 `admin`）与目标租户的 `vault.tenant_admin` / `vault.file_admin` 角色（宿主 = `isAdministrator` 阶梯，`authorizer.go:136-144` + `tenantMatches` :77-79）。
- **FR-2b（拒绝集）**：`vault.member`、write-scope 成员、匿名、未知角色 ⇒ **拒绝**。**对象路径的授权阶梯（`scopeAllows`/owner/ACL/`tenant_default`）在 admin 边界不适用**——`scopeAllows`（:150-177）对 `ActionDelete` 的 write-scope 授予（:171-174）不得透传为 admin 边界授予。
- **FR-2c（委托适配器）**：生产默认适配器可委托 `access.Manager`，但必须实现 FR-2a/2b 矩阵（`Manager.Authorize` 单独不足以满足——其 `scopeAllows` 授予 write-scope 成员）；委托语义与 core-v1 设计 D3 对齐，不重复设计 Manager 内部阶梯。

### FR-3：被拒请求零副作用

授权判定先于删除事务：deny ⇒ 403；对象行 + blob 完好；`event_outbox` 零行（deleted@1.1 / notify@1.1）、`audit_log` 零行、`object_events` 零行、`jobs` 零行、EventBus 零事件。断言锚 = `adminDeleteEnv.assertNoWriteSideEffects`（`admin_files_delete_test.go:134`）扩展 jobs 计数。

### FR-4：审计事实记录强制权限名

admin 删除**获准**时，审计事实（`audit_log` 行，与删除同事务）必须记录强制权限名 `vault.file.delete`（保留既有 `action="file.delete"` + `detail=hard|soft`，`internal/repository/audit.go:13` + `deleteAuditEntry` `file_delete.go:96-104`）；实现可经共享路径注解（admin 边界传入），**其他删除路径行为不变**（I5）。deny 时零审计行（FR-3）。

### FR-5：组合按名注册，不硬编码

- **FR-5a**：admin 边界的 provider 由**名字**从组合注册表解析（name → adapter），边界代码不得硬编码具体实现；sibling 适配器按各自名字注册（"registered by name, not hardcoded"）。
- **FR-5b**：生产默认名解析到 access.Manager 型适配器（结构化满足，同 `http.go:120` s3compat 先例）；`cmd/server/http.go:120` 的硬编码直传与 `NewAdminHandler(svc, repo, reg)` 的裸构造均属需改造点。
- **FR-5c（编译级）**：admin 边界端口/适配器/装配代码不得硬编码 sibling 项目标识符（检查集见 §4 AC-4；沿用 audit-sink 轮的零 sibling grep 先例）。

### FR-6：`internal/jobs` 模块契约不变（顺序不变量）

`internal/jobs` 的 Queue/Registry/Pool 契约（post-commit enqueue、dedupe、指数退避、reaper，`jobs.go`）**本方向不改**。拒绝先于 enqueue 的顺序不变量是硬约束：被拒 admin 删除 ⇒ `jobs` 表零行；未来若 admin 删除改走 job，门禁仍留在请求边界（enqueue 之前）。

### 非功能约束

- 门禁纯本地判定，不引入新网络往返；provider 调用受 deadline 约束（FR-1e）。
- 错误文本不泄漏 provider/端点内部细节（H5）。
- I5：除 admin 边界显式收紧外，其余行为不变——对象 CRUD 路径（REST `files/*`、S3、WebDAV、MCP）的既有授权阶梯、读/写/上传不受影响；其他 admin 路由（tenants/keys/jwt/jobs/config/audit）不变。
- I4：中间件链 12 环不变；门禁在 handler 边界。
- I1/I2/I6：预期无 SQL 变更、无新依赖、无断言框架。
- 文件 ≤500 行（`admin.go` 有 F12 注释的分文件先例；provider 槽位落在 `admin_files_delete.go` 或新小文件）。

---

## 4. 验收标准（可测试）

> 方向文 4 组验收逐条保留，拆为可执行断言。标注 **[已落地]** = 现有测试即满足；**[新增]** = 本方向落地后需补。

### AC-1（unit：default-deny 适配器 + fail_closed + 权限矩阵 + 零副作用）—— [新增]

> 方向文原句："default-deny adapter returns 403 with zero side effects when provider is absent or errors (fail_closed on timeout/panic); permission matrix test — operator/tenant_admin granted, member denied vault.file.delete; no outbox/audit rows on denial."

- **AC-1a（absent ⇒ 403）**：`adminDeleteEnv` 扩展 provider 槽位后，**nil provider** ⇒ `DELETE /v1/admin/files/acme/k.txt?hard=1`（operator key）返回 **403** `AccessDenied`（`classify` :55）；对象存活（`GetObject` 无 `ErrNotFound`）+ `assertNoWriteSideEffects`（含 `jobs` 零行扩展）。
- **AC-1b（error ⇒ 403，非 500）**：`errProvider`（s3compat 桩形状，`authz_gate_test.go:41`）⇒ 403；断言响应体**不含** `InternalError`（对照 s3compat `TestDeleteProviderErrorIs403Not500` :237）。
- **AC-1c（panic ⇒ 403）**：panic provider 桩 ⇒ 403，服务不崩溃，零副作用。
- **AC-1d（timeout ⇒ 403）**：provider 桩在 `ctx.Done()` 后返回错误（或阻塞至 deadline）⇒ 403，删除请求有界返回（不无限阻塞）。
- **AC-1e（权限矩阵）**：以 `principalMiddleware`/`doAs` 模式（`authz_gate_test.go:109/:124` 先例）注入 principal：operator（`TenantID="*"`）⇒ 204；`vault.tenant_admin`（目标租户）⇒ 204；`vault.file_admin`（目标租户）⇒ 204；`vault.member` ⇒ 403；write-scope 成员 ⇒ **403**（对照 `scopeAllows` 对象路径授予）；匿名 ⇒ 403；无 principal ⇒ 403。矩阵经真实 `access.Manager` 臂 + 矩阵适配器桩双路断言（F2 先例：`TestAdminDeleteFile_ErrorMapping` "F2" :309）。
- **AC-1f（零副作用锚）**：上述每个 deny 臂后断言 `event_outbox`（deleted@1.1/notify@1.1）= 0、`audit_log` file.delete 行 = 0、`object_events` = 0、`jobs` = 0（扩展 `assertNoWriteSideEffects`，`admin_files_delete_test.go:134`）；对象随后 `GET` 仍 200 正控。
- 位置：`internal/api/rest/admin_files_delete_test.go`（`newAdminDeleteEnv` 增加 provider 槽位参数）。

### AC-2（outbox 投递：被拒删除零 outbox 条目）—— [新增 + 已落地]

> 方向文原句："denied deletes produce no outbox entries."

- **[新增]** 集成 e2e：deny provider + operator key ⇒ 403 后 `outboxCountFor(t, h.dsn, obj.ID, repository.EventTypeFileDeleted11)` == 0 且 `EventTypeFileNotify11` == 0（harness 助手 `internal/integration/admin_files_delete_test.go`）；`deliveredTotal` == 0。
- **[已落地] 正控**：获准 admin hard delete ⇒ deleted@1.1 + notify@1.1 **各恰好 1** 行（`TestAC2_AdminDelete_EventTypeFilteredState`，`internal/integration/admin_files_delete_test.go:112`）；S3 同型先例 `TestDeniedDeleteWritesNoOutboxRows`（`authz_gate_test.go:324`）。

### AC-3（事件 schema：审计事实记录强制权限名）—— [新增 + 已落地]

> 方向文原句："audit fact recorded with the enforcing permission name."

- **[新增]** 获准 admin hard delete ⇒ `audit_log` 新增行 `action="file.delete"`、`tenant_id` 正确，且该行记录强制权限名 **`vault.file.delete`**（断言行内容含字面量；扩展 `assertAuditRowFor`（`fullserver_test.go:1336`）与 `auditDeleteRows`（`admin_files_delete_test.go:117`））。deny ⇒ 零行（AC-1f）。
- **[已落地]** envelope schema 回归不动：`deleted@1.1` golden 字节钉死（`internal/events/schema_test.go`）；集成载荷断言（`TestAC2_AdminDelete_EventTypeFilteredState` 的 `outboxPayloadFor` 检查 `schema_version:"1.1"`/`tenant`/`key`）。

### AC-4（composition e2e：按名 sibling 适配器 deny ⇒ 403 + 零副作用；provider outage ⇒ fail_closed）—— [新增]

> 方向文原句："sibling AuthorizationProvider adapter (registered by name, not hardcoded) denying -> 403 + no deletion side effects; provider outage -> fail_closed, delete blocked."

- **AC-4a（按名注册）**：集成 harness 提供 name → provider 注册表；以名字 `"deny-all"` 注册 sibling 适配器并装配 ⇒ operator key `DELETE /v1/admin/files/acme/k.txt?hard=1` 返回 **403**；对象存活（随后 GET 404/200 正控语义：`GetObject` 仍成功）+ 零 outbox/audit/delivered 行 + 零 `jobs` 行。
- **AC-4b（组合非硬编码）**：把注册名换为 `"allow-all"` ⇒ 同请求 **204**（正控证明按名解析生效，非代码内硬编码）；边界代码不得出现 sibling 项目标识符——编译级检查：`grep -rln "<sibling-id>" internal/api/rest/ internal/cli/ cmd/server/http.go` ⇒ 零命中（豁免集：无，本方向检查集全量）。
- **AC-4c（provider outage ⇒ fail_closed）**：以 `errProvider`（PDP outage 桩）为命名适配器 ⇒ 403 + 删除被阻断（对象与 blob 完好）+ 零副作用；超时桩同型（AC-1d 的 e2e 臂）。
- 位置：`internal/integration/admin_files_delete_test.go`（复用 `startFullServerWithAuthAndRelay` 家族，扩展 provider 注册表参数）。

---

## 5. 范围边界（明确不做）

| 不做 | 理由 |
|------|------|
| admin 删除改异步 job（`internal/jobs` 新 job 类型） | 本方向验收要求 403 在 HTTP 边界同步返回；jobs 模块契约不变（FR-6 只 pin 拒绝先于 enqueue 的顺序不变量） |
| 对象 CRUD 路径（REST `files/*`/S3/WebDAV/MCP）授权阶梯改造 | core-v1 / s3compat / webdav 轮已 gate；FR-2b 只排除其阶梯在 admin 边界的透传 |
| 新增 `fail_closed` env flag | 全仓无此配置键；fail_closed = 端口契约本身（s3compat 先例 + core-v1 D3）；`ACCESS_DELETE_FAIL_CLOSED`（`config.go:220`）保持为唯一显式 opt-out |
| `requireAdmin` / auth registry 改动 | 已有测试锁定（`TestAdminDeleteFile_RequireAdmin`）；端口是边界内加法，不改 scope 语义 |
| `@1.1` envelope schema / `event_outbox` 表结构 / 迁移改动 | 已 gate 轮次（outbox/audit-sink）；AC-3 只要求审计行记录权限名 |
| AuditSink L1/L2、outbox relay 改造 | audit-sink 轮已落地（`internal/events/audit_sink*.go` + relay） |
| s3compat 既有端口重构为共享类型 | 保持各边界独立同形；本方向新增 admin 边界实例 |
| `vault.file.delete` 词汇/`ActionForPermission` 改动 | core-v1 轮已 gate（`permissions.go`）；本方向只消费 |

## 6. 基线影响

1. **签名变更**：`NewAdminHandler(svc, repo, reg)`（`admin.go:34`）扩展 provider 槽位 → 波及 `router.go:224` 与直构测试（`admin_files_delete_test.go` "F6" 的 `&AdminHandler{...}` 字面量）；`newAdminDeleteEnv` 须在**新槽位**显式装 allow-all 桩以保持既有行为（今日它在 svc authorizer 槽装 `allowAllProvider{}` 绕过 FR-1 门禁，先例同构）。
2. **行为翻转点**：admin 边界从"隐式继承对象路径判定"变为"显式端口 + default-deny"——默认配置下（access 关、registry 关）admin 删除行为不变（FR-1 门禁已 403）；**变化**在 provider 错误（500 → 403）与 write-scope 成员（对象路径放行 → admin 边界 403）。
3. **矩阵与 F2 兼容**：租户限定 admin key 跨租户 403（`tenant_mismatch`）语义不变（`tenantMatches` 仍生效）；`isAdministrator` 阶梯是矩阵宿主，不动。
4. **jobs 模块零变更**：`internal/jobs` 测试套（`jobs_test.go`/`depthcap_test.go`）保持绿；拒绝路径的 `jobs` 零行断言进 AC-1f/AC-4a。
5. **门禁**：`make check`（gofmt/build/vet/test）全绿；`make test-race` 无竞争；文件 ≤500 行。
6. **操作顺序**：先合入端口+矩阵（旧组合下 admin 边界默认 deny 或显式 allow-all 装配，不翻转生产行为），再按名装配生产适配器，最后启用矩阵收紧。

## 7. 实现指引（供验收后落地，非本规格交付物）

- **FR-1a/1b**：`AdminHandler` provider 槽位 + 门禁函数（形状对齐 `s3compat/authz.go:27-36` `authorizeDelete`，deny 一律 `service.ErrForbidden` 包装）；`NewAdminHandler` 签名扩展，`router.go:224` 同步。
- **FR-1e**：门禁内对 provider 调用套 deadline（复用请求 ctx 或边界 bound），超时/panic recover 归一到 deny 403。
- **FR-2**：矩阵适配器（`isAdministrator` + `tenantMatches`，拒绝 `scopeAllows` 透传）或等价的边界判定；测试桩沿 `authz_gate_test.go` 双例（allowAll/denyAll/err/panic/timeout/keyDeny）。
- **FR-4**：`deleteAuditEntry` 注解（admin 边界传入权限名）或等价机制；断言沿 `auditDeleteRows`/`assertAuditRowFor` 扩展。
- **FR-5**：组合注册表（name → adapter）+ `cmd/server/http.go` 装配改造；AC-4 的 grep 检查固化为 CI 或脚本。
- **测试新增**：AC-1/AC-2/AC-3/AC-4 的 [新增] 项；先跑 [已落地] 基线（s3compat 7 门禁 + admin delete 集成套）再跑新增。
