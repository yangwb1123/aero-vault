# 设计：vault.file.delete 的 fail-closed 强制 —— admin-delete 边界 AuthorizationProvider 端口（配套设计文档 v1）

> **配套规格：** `docs/requirements/authorizationprovider-vault-file-delete-admin-v1.md` · **模块：** `internal/api/rest`（+ `internal/service` + `cmd/server` + `internal/integration`；`internal/jobs` 契约不变，FR-6） · **状态：** 设计已定稿，未实现 · **日期：** 2026-08-06 · **rev 2（security F1 修订）：** 决策表 §3.1 · C7 真实翻转目录（④ auth-off 403-forever、⑤ 404→403 oracle） · C8/§8/§0.2.3 修正（零配置 500-panic 而非 403） · D8 typed-nil 前置修复 · AC-4f/4g + AC-5 新腿 · configuration.md:207 修正案
> 本文是规格的落地设计：证据复核结论（§0）、API 变更（§1）、设计决策与逐路径证明（§2）、兼容性约束（§3）、失败模式（§4）、迁移步骤（§5）、验收映射（§6，含 fc46c395 N8–N15 反空洞硬化）、最终代码形态（§7）、范围边界（§8）。

---

## 0. 证据复核结论（对规格逐条核验）

规格全部引用已逐行对照工作树复核；**无一处需要修正**。额外核验了规格未列的 4 个装配链事实（§0.2），并做了 4 条规格精确化（§0.3，不改变语义）。

### 0.1 逐条复核（G1–G6 缺口 + 证据锚点）

| # | 规格引用 | 复核 | 结论 |
|---|---------|------|------|
| G1 | `NewAdminHandler(svc, repo, reg)` 无 provider 槽位（admin.go:34） | 实际 :28-35 `AdminHandler{svc,repo,reg}` + 3 参构造；`DeleteFile`（admin_files_delete.go:20-35）只 `requireAdmin` 后直调 `svc.Delete` | ✅ 缺口成立 |
| G2 | provider error → 500（access.go:106-108 不包 `ErrForbidden`） | 实际 access.go:106-108 `fmt.Errorf("authorization decision: %w", err)`；`classify`（handler_helpers.go:55-56）`ErrForbidden`→`AccessDenied`/403，default（:60-61）→`InternalError`/500 | ✅ 缺口成立 |
| G3 | 权限矩阵未在 admin 边界强制；`scopeAllows` 授 write-scope 成员 | 实际 authorizer.go:150-177 `scopeAllows`；`ActionDelete` 非 read 组 → `wanted="write"` → write-scope 成员在 `DefaultTenant` 下获 `ActionDelete`（:171-174） | ✅ 缺口成立 |
| G4 | 审计事实无权限名（deleteAuditEntry :96-104） | 实际 file_delete.go:100-104：`Action=AuditActionFileDelete` + `Detail="hard"\|"soft"`，无权限名字段 | ✅ 缺口成立 |
| G5 | 组合硬编码（http.go:120） | 实际 :120 `r.Mount(cfg.S3Compat.Prefix, s3compat.NewRouter(svc, logger, accessManager))`；rest.NewRouter 内 :224 `adm := NewAdminHandler(svc, repo, reg)` 裸构造 | ✅ 缺口成立 |
| G6 | admin 边界无 default-deny 适配器 | s3compat 有（authz.go:9-36，nil⇒deny）；rest 边界无任何 provider 概念（`grep "vault.file.delete" internal/api/rest/` 零命中） | ✅ 缺口成立 |
| E1 | service `authorize` nil⇒allow 部分证伪 | 实际 access.go:83-111：:91-102 FR-1 门禁（nil + `ActionDelete` + `!deleteFailOpen` ⇒ `ErrForbidden` :101；AV 豁免 :98-100 `IsSystemDeleteExempt`）；读/写 nil⇒allow 仍成立 :103 | ✅ 与规格一致 |
| E2 | `types.go:76` `ActionDelete="object:delete"`；权限名已存在 | :76 精确；permissions.go:7 `PermissionVaultFileDelete` + :16-26 `ActionForPermission` | ✅ |
| E3/E4 | `Authorizer` 接口 :10-16；role 检查 :131 | 实际 :10-13 接口；`isAdministrator` :136-144（roles :141）；**`IsAdministrator` :145 已导出**（矩阵适配器可复用） | ✅ |
| E5 | `file_delete.go` Delete→`authorizeObject(ActionDelete)` | 实际 `Delete` :147，门禁 :159；`DeleteVersion` :174/:179 同型 | ✅ |
| E6 | `main.go:94` WithAuthorizer；`ACCESS_DELETE_FAIL_CLOSED` 默认 true | :94-95 `.WithAuthorizer(accessManager).WithDeleteFailOpen(!cfg.Access.DeleteFailClosed)`；config.go:220 默认 true；buildAccessManager（cmd/server/access.go:8-20）`!cfg.Access.Enabled → nil,nil` | ✅ |
| E7 | "no AdminDelete symbol" 证伪 | admin_files_delete.go:20、router.go:352（OpenAPI :203）、cli_admin_files.go:27、单测 + 集成测试套均存在 | ✅ 与规格一致 |
| 锚 | `authz_gate_test.go:41/:109/:124` | :41 `errProvider`、:109 `principalMiddleware`、:124 `doAs` —— **精确命中** | ✅ |
| 锚 | `assertNoWriteSideEffects` `admin_files_delete_test.go:134`；`auditDeleteRows` :117 | 实际 :132-134（注释 :132，func :134）；`auditDeleteRows` :117 | ✅ |
| 锚 | `TestAC2_AdminDelete_EventTypeFilteredState:112`；`outboxCountFor` | 集成 admin_files_delete_test.go:112；`outboxCountFor` authz_parity_test.go:51；`deliveredCountFor`/`deliveredTotal`/`deliveredAt` 集成 :35/:52/:67 | ✅ |
| 锚 | `startFullServerWithAuthAndRelay` 家族 | fullserver_test.go:65（→ `startFullServerOpts` :72，:131 处 `rest.NewRouter(...)`，即 harness 转发装配点，§5.5） | ✅ |

### 0.2 独立补充核验（规格未列，设计依赖的装配链事实）

1. **principal 注入链**（矩阵适配器的数据来源）：`auth.Registry.Middleware`（auth_middleware.go:15）→ key 路径 `contextWithKey` → `access.WithPrincipal(ctx, PrincipalForKey(key))`（auth/principal.go:38）；匿名 GET/HEAD 路径 `withAnonymousPrincipal`（auth_middleware.go:177-186，principal 注入 :183）。operator key `opsecret:*:admin` → `Principal{TenantID:"*", Scopes:["admin"]}`；租户限定 key `adm:acme:admin` → `Principal{TenantID:"acme", Scopes:["admin"]}`；匿名 → `PrincipalAnonymous`。**REST 边界在 registry 启用时必有 principal 可读**（`access.PrincipalFrom`）。
2. **Manager 的 `DeleteFailOpen` 字段**：manager.go:24-31 `Config{Enabled, DefaultPolicy, ShareSecret, DeleteFailOpen}`；buildAccessManager 以 `!cfg.Access.DeleteFailClosed` 装配（access.go:19）——**service 门禁与 Manager 门禁共享同一 opt-out**，边界矩阵不新增配置键（§5 范围）。
3. **默认配置下的现状语义（rev 2 修正：HEAD 是 500 panic，不是 403）**：`buildAccessManager`（cmd/server/access.go:10-13）在 access 关时返回**类型化 nil** `(*access.Manager)`；`main.go:94` `.WithAuthorizer(accessManager)`（接口参数）将其包装为**非 nil 接口** ⇒ 生产上 `s.authorizer == nil`（service/access.go:91）恒为 false，FR-1 门禁（:101）是**生产死代码**；实际走 `s.authorizer.Authorize` → `Manager.Authorize`（authorizer.go:29 `!m.cfg.Enabled`）nil 接收者 **panic → Recoverer → 500**。零配置实机复现（2026-08-06 实测，空 env + SQLite + local FS）：`PUT /v1/files/docs/a.txt` → 500；对象存在时 `DELETE /v1/admin/files/acme/docs/a.txt?hard=1` → **500 + panic 日志**；`ACCESS_DELETE_FAIL_CLOSED=false` 同 500（`deleteFailOpen` 只作用于不可达的 nil 分支）⇒ **configuration.md:207 的"false 恢复 legacy allow"契约在 HEAD 即已破坏**（由 D8 前置修复恢复）。CI 全绿原因：**无测试引导生产装配**（单测/集成 harness 均显式 `WithAuthorizer(allowAllProvider{})` 或直构）。边界门禁把默认 admin 删除 500→403（AC-1a′ 覆盖的正是该真实路径）；但 access 关 + registry 开 + operator 仍会被边界放行后撞上同一个 service 层 panic —— 由 D8 一并消除（§3.1 决策表行 4/5）。
4. **jobs 表与 enqueue 顺序**：`jobs` 表（迁移 0009_jobs.up.sql）；`EnqueueJob`（repository/jobs.go:46）post-commit INSERT；`Queue.Enqueue`（internal/jobs/jobs.go:90）。admin 删除同步执行、不产生 job；拒绝先于 enqueue 是硬顺序（FR-6）。
5. **审计断言工具**：`assertAuditRowFor`（fullserver_test.go:1336-1351）按 `Action==AuditActionFileDelete && TenantID==tenant` 匹配并**严格断言 Detail 相等**——FR-4 注解会使其对 `"hard"` 的既有调用（集成 :156）变红，迁移必须同步（§5.6）。

### 0.3 规格精确化（R1–R4，不改变规格语义）

- **R1（requireAdmin 与矩阵的分层）**：`requireAdmin`（registry 启用时检查 key 的 `ScopeAdmin`）**先于** provider 门禁，本设计不改动（§5 范围）。推论：write-scope 成员在 registry 启用时于 requireAdmin 即 403——矩阵的"member 拒绝"臂（AC-1e）必须经 **principal 覆盖中间件**（s3compat `doAs`/`principalMiddleware` 同型，authz_gate_test.go:109/:124）注入非 admin principal 才能触达矩阵；key 保持 operator（过 requireAdmin）。该臂验证的是矩阵适配器契约本身（FR-2），不是 requireAdmin。
- **R2（deny reason 词汇归一）**：矩阵拒绝 reason 复用 Manager 词汇——租户不匹配 ⇒ `"tenant_mismatch"`（保住既有 F2 测试 `strings.Contains(body, "tenant_mismatch")` 断言，admin_files_delete_test.go:326-327；子测试起于 :309）；其余 ⇒ `"default_deny"`。provider 缺失/错误/超时/panic ⇒ 固定文案（不含 provider 内部细节，H5）。
- **R3（审计 Detail 注解格式）**：`detail = "hard|soft" + ";permission=vault.file.delete"`——追加式后缀，既有 `Detail=="hard"` 消费方按前缀匹配兼容；machine-parseable。其他删除路径（REST files、S3、WebDAV、MCP、AV quarantine）不注解 ⇒ Detail 不变（I5）。
- **R4（超时边界为包级 var）**：`var adminDeleteAuthzTimeout = 10 * time.Second`（rest 包）——生产默认 10s；单测（同包）可缩短以保测试快速（AC-1d 的 timeout 桩不必真等 10s）。不新增配置键（§5 范围）。该 bound 是 **ctx 协作式**而非强制（provider 无视 ctx 仍可挂起；生产默认 Manager→SQLite 遵守 ctx，可接受；兄弟适配器须自带 ctx 协作，§8）。
- **R5（rev 2：配置维度决策表 + 前置修复）**：admin 门禁在 {registry × access × `ACCESS_DELETE_FAIL_CLOSED` × principal} 全空间的行为见 **§3.1 决策表**；registry 关 ⇒ 零 principal ⇒ 边界恒 403（F1(a)），`ACCESS_DELETE_FAIL_CLOSED=false` 的 legacy-allow 对 admin 面**不可恢复**（configuration.md:207 修正案，§3.1）；typed-nil authorizer 装配缺陷（HEAD 默认数据面 500 panic）由 **D8 前置修复**，不改变门禁语义（§0.2.3-3 修正）。

---

## 1. API 变更

**零配置变更、零 schema/迁移变更、零 `go.mod` 变更（I2/I6）、零中间件链变更（I4）、`internal/jobs` 零变更（FR-6）。** 新类型 2 个、签名变更 3 处（编译期强制所有调用点显式表态）、行为变更 3 处（§3）。

| 层 | 旧 | 新 |
|----|----|----|
| **新端口** `rest.AuthorizationProvider`（新文件 `internal/api/rest/admin_authz.go`） | — | `Authorize(ctx, access.Principal, access.Action, access.Resource) (access.Decision, error)` —— 与 s3compat 端口（s3compat/authz.go:9-16）**同形**；`*access.Manager` 结构化满足（零 wrapper） |
| **新适配器** `rest.AdminMatrixProvider`（同文件） | — | 零字段 struct；`Authorize` = FR-2 矩阵（§2 D3）；生产默认名解析目标（FR-5b） |
| `NewAdminHandler(svc, repo, reg)`（admin.go:34） | 3 参 | `NewAdminHandler(svc, repo, reg, authz AuthorizationProvider, logger *slog.Logger)`（5 参；logger nil ⇒ `slog.Default()`，对齐 NewHandler） |
| `AdminHandler` struct（admin.go:28） | `{svc, repo, reg}` | + `authz AuthorizationProvider` + `logger *slog.Logger` |
| `NewRouter(svc, repo, search, chat, agent, bus, reg, logger, idemHashBody, aiRL, adminRL, aiTimeout, aiDegraded, opts...)`（router.go:214） | 14 参 + opts | + `adminAuthz AuthorizationProvider`（置于 `aiDegraded` 与 `opts` 之间；router.go:224 `NewAdminHandler(svc, repo, reg, adminAuthz, logger)`） |
| `FileService` | — | + `WithDeletePermission(ctx, string) context.Context` + `DeletePermissionFrom(ctx) (string, bool)`（`internal/service/file_delete.go`，ctx 注解，FR-4） |
| `cmd/server/http.go:105` 装配 | 直传 | 经 `adminDeleteProviders` 名字注册表解析（§2 D6） |
| `DeleteFile`（admin_files_delete.go:20） | requireAdmin → svc.Delete | requireAdmin → 校验 → **provider 门禁（403 fail-closed）** → 注解 ctx → svc.Delete（§2 D2） |

**不改的签名**：`svc.Delete`（:147）、`deleteAuditEntry`（内部）、CLI `adminFilesDelete`（cli_admin_files.go:27，零 diff——CLI 只消费 HTTP 语义，403 ⇒ `readSuccessfulResponse` 失败 ⇒ exit 1）、s3compat 端口（保持独立同形，不重构共享类型）。

---

## 2. 设计决策

### D1 — 端口形状：rest 包独立接口，与 s3compat 同形

`rest.AuthorizationProvider` 与 `access.Authorizer`/`s3compat.AuthorizationProvider` 方法集完全一致。**不在 rest 包 import s3compat**（兄弟边界独立，AC-4 编译级 grep 的检查集）；`*access.Manager` 结构化满足，零 wrapper。这是先例的忠实复制（s3compat 轮已 gate），非绿地设计。

### D2 — 门禁位置与顺序（逐路径证明）

`DeleteFile` 新顺序：`requireAdmin`（既有，registry 关 ⇒ 放行）→ 空 tenant 400（既有 F13）→ **`authorizeFileDelete` 门禁** → `svc.Delete(ctx注解, ...)` → 204。

**门禁先于一切持久副作用的证明**：`svc.Delete` 是 admin 删除唯一写路径（file_delete.go:147-165：`GetObject` → `authorizeObject` → WORM/legal-hold 检查 → `HardDeleteObjectWithEvent`/`SoftDeleteObjectWithEvent`，事务内写 outbox+audit+object_events）。门禁位于 `svc.Delete` 调用之前 ⇒ deny 路径零写、零事件、零 job（jobs 行只可能来自 `Queue.Enqueue`，post-commit，本路径从不 enqueue）。**软删（无 `?hard=1`）同样过门禁**——软删也写 outbox/audit（file_delete.go:86），规格 FR-1c 的"一切持久副作用"含软删。

**双门禁共查是特性不是缺陷**：边界门禁（`AdminMatrixProvider`）+ service 门禁（`svc.authorizer`=Manager）对 operator/tenant_admin/file_admin 均放行（矩阵 ⊂ Manager 的 `isAdministrator` 阶梯——矩阵是阶梯的**管理面裁剪**）；被边界拒绝的请求到不了 service。双查为纯本地判定，无新网络往返（非功能约束）。

**匿名/系统 principal**：矩阵按构造拒绝——匿名 `TenantID`=header 租户且无 admin scope/role；系统 principal 不经过 HTTP 边界（AV 豁免是 service 门禁的 quarantine 路径专属，`IsSystemDeleteExempt`，admin 边界不适用——admin 面是操作员信任面 C3）。

### D3 — 矩阵适配器：独立实现，不委托 Manager（显式偏离 FR-2c 的"可"字）

```go
type AdminMatrixProvider struct{}
func (AdminMatrixProvider) Authorize(_ context.Context, p access.Principal, _ access.Action, res access.Resource) (access.Decision, error) {
    if p.TenantID != "*" && p.TenantID != res.TenantID {
        return access.Decision{Allowed: false, Reason: "tenant_mismatch"}, nil
    }
    if access.IsAdministrator(p) { // = TenantID=="*" || Scopes∋admin || Roles∋vault.tenant_admin|vault.file_admin（authorizer.go:136-144）
        return access.Decision{Allowed: true, Reason: "administrator"}, nil
    }
    return access.Decision{Allowed: false, Reason: "default_deny"}, nil
}
```

- **矩阵 = `tenantMatches ∧ isAdministrator`**（authorizer.go:77-79/:136-144），完全满足 FR-2a/2b：operator（`"*"` 或 admin scope，租户匹配时）/目标租户 tenant_admin/file_admin 授予；member、write-scope、匿名、未知角色拒绝；`scopeAllows`（:150-177）、owner、ACL、capability、`tenant_default` **一律不参与**——对象路径阶梯被显式排除（FR-2b）。
- **不委托 `access.Manager` 的理由（规格 FR-2c "可委托"的取舍，非违约；rev 2 修正①）**：① **裸委托违反 FR-2a/2b 的排他矩阵**：Manager 额外授予 capability/owner/tenant_default（authorizer.go:61-85，FR-2a "仅授予"之禁）且 `scopeAllows` 在 `DefaultTenant` 下授 write-scope 成员 `ActionDelete`（:169-175，FR-2b 之禁）——委托会使 AC-1e 的 member/write-scope 拒绝臂失效；② Manager 的 `cfg.Enabled==false` 分支（:22-26）使边界行为耦合 PDP 启停状态；③ 矩阵仅需 principal 字段，委托无信息增益。（rev 2 删去原"对象级 deny ACL 语义泄漏"理由：service 门禁双查下 delegation 与独立实现对 deny-ACL 同为 403，非差异项——见 D2。差异只在授予侧，即①。）规格的硬要求（FR-2a/2b 矩阵）由独立实现精确满足；FR-5b 的"access.Manager 型适配器"指**形状**（结构化满足端口，同 Manager），非具体类型。
- **FR-2c 的 core-v1 D3 对齐点**：service 门禁的 nil-provider + system 豁免（core-v1 D3）在 service 层保持不动；边界矩阵不复制 `PrincipalSystem` 豁免（admin 面无 system 路径）。

### D4 — 超时/panic 归一 deny（FR-1b③④/FR-1e）

门禁内：`ctx, cancel := context.WithTimeout(r.Context(), adminDeleteAuthzTimeout)`（包级 var，R4）→ provider 调用包在 `defer/recover` 内 → panic ⇒ 按 deny 处理（Warn 日志含 panic 值，服务不崩——Recoverer 中间件只兜未捕获 panic，此处是**边界内捕获转 403**，不是 500）。超时 ⇒ provider 调用返回 ctx 错误 ⇒ deny 403。**admin 请求不得因 provider 挂起无限阻塞**（今日 `RequestTimeout` 仅挂 AI 组，router.go:258；本设计为边界自带 bound）。

### D5 — 审计权限名注解（FR-4）

`DeleteFile` 在调用 `svc.Delete` 前：`ctx := service.WithDeletePermission(r.Context(), access.PermissionVaultFileDelete)`。`deleteAuditEntry`（file_delete.go:100-104）追加：`detail += ";permission=" + perm`（perm 来自 `DeletePermissionFrom(ctx)`，无注解 ⇒ 原样，I5）。审计行与删除同事务（`HardDeleteObjectWithEvent`/`SoftDeleteObjectWithEvent`），获准删除的权限名**原子**落盘（AC-3）；deny 无审计行（FR-3）。ctx 注解是仓内既有惯用法（RequestID/Principal/TenantStatusVerified）。

### D6 — 组合按名注册（FR-5）

组合根（`cmd/server`）持有名字注册表；边界代码零名字引用：

```go
// cmd/server/admin_delete_providers.go（新独立文件；audit_governance.go 先例，http.go 保持零 sibling 标识符，AC-4d）
var adminDeleteProviders = map[string]rest.AuthorizationProvider{
    "admin-matrix": rest.AdminMatrixProvider{}, // 生产默认（FR-5b）
}
// buildRouter 签名 + adminAuthz 参数；装配点：
adminAuthz := adminDeleteProviders["admin-matrix"]
r.Mount("/v1", rest.NewRouter(..., adminAuthz, opts...))
```

- 名字→适配器的**解析发生在组合根**；`internal/api/rest`、`internal/cli`、`cmd/server/http.go` 不出现兄弟项目适配器标识符（AC-4 grep 检查集，§6）；兄弟适配器装配只允许落在独立文件 `cmd/server/admin_delete_providers.go`（§6 AC-4d ② 驻留点唯一性）。
- 集成 harness 同型：`startFullServerNamed(t, relayOpts, authKeys, providers map[string]rest.AuthorizationProvider, name string)`——按名解析，未知名字 ⇒ `t.Fatalf`（防空洞：名字必须真实参与解析）。
- 未来兄弟适配器（audit-governance 等）在组合根按名注册即可，边界零改动（注册条目落 `cmd/server/admin_delete_providers.go`）。

### D7 — 测试替身默认值（迁移保行为，§5.4/§5.5）

- 单测 harness `newAdminDeleteEnv(t, authKeys, manager, store, authz)`：**nil `authz` ⇒ 门禁默认 deny**（端口语义，非隐式 allow）；既有 **12 个调用点**（§5.4 全列）：11 个显式 `allowAllProvider{}` + F2（:317）显式 `AdminMatrixProvider{}`（rest 测试包已有 allowAll 桩，handlers_test.go:694-696；补 denyAll/err/panic/timeout/counting 桩，对齐 authz_gate_test.go:22-53 形状）。
- 集成 harness：既有三个构造器（`startFullServer`/`startFullServerWithRelay`/`startFullServerWithAuthAndRelay`）内部传 `allowAllProvider{}`（测试替身，保既有 204 行为）；新 `startFullServerNamed` 显式传注册表+名字。**生产路径 nil ⇒ deny 与测试替身默认 allow 的差异是有意为之**（测试替身=已配置 provider，非"未配置"状态）。rev 2：`startFullServerNamed(t, relayOpts, authKeys, providers, name, svcShape)` 增加可选 `svcShape{authorizer access.Authorizer; deleteFailOpen bool}`（缺省 = 当前 harness 形状 allowAll+false；authorizer nil ⇒ 不调 `WithAuthorizer`、`deleteFailOpen` ⇒ `WithDeleteFailOpen`）——用于装配 access-关生产形状（AC-4f/4g、AC-5 依赖）。

### D8 — 前置缺陷修复：typed-nil authorizer 装配（rev 2，security F1 决策，必须先于门禁接入）

**缺陷（2026-08-06 实机复现，源自 `3cf3dfd`，CI 盲区）**：access 关时 `buildAccessManager`（cmd/server/access.go:10-13）返回类型化 nil `(*access.Manager)`；`main.go:94` `.WithAuthorizer(accessManager)`（接口参数）将其包装为**非 nil 接口** ⇒ `service/access.go:91` 的 nil 判断在生产上恒假、FR-1 门禁（:101）是**生产死代码** ⇒ `Manager.Authorize`（authorizer.go:29 `!m.cfg.Enabled`）nil 接收者 **panic → Recoverer → 500**。零配置实机复现：`PUT /v1/files/...` → 500；对象存在时 admin 删除 → **500 + panic 日志**；`ACCESS_DELETE_FAIL_CLOSED=false` 同 500（`deleteFailOpen` 只作用于不可达的 nil 分支）⇒ **configuration.md:207 的"false 恢复 legacy allow"契约在 HEAD 即已破坏**。CI 全绿原因：**无测试引导生产装配**（单测/集成 harness 均显式注入 authorizer）。

**决策（选 A：入本设计范围，1 行守卫）**：`cmd/server/main.go` 改 `svc.WithAuthorizer(accessManager)` 为条件调用——`if accessManager != nil { svc.WithAuthorizer(accessManager) }`。access 关 ⇒ authorizer 保持 nil ⇒ FR-1 门禁成为真实路径：删除在 fc=true 时 403（文档语义）、fc=false 时放行（legacy allow 恢复）；读/写 legacy allow；AV 系统豁免（`IsSystemDeleteExempt`）不变。**修复后 wire 语义**：REST/WebDAV/MCP 数据面 GET/PUT 由 500 恢复 200/201；REST/WebDAV/MCP/admin 删除在 access 关 + fc=true 由 500 恢复 403；S3 网关不动（自身 gate：无 access 恒拒删除，文档已述）。**选 B（仅标记、另行修复）被否**：不修则 C8 对 access 关 + registry 开 + operator 臂落地后仍 500、configuration.md:207 契约继续失效、AC-4g/AC-5 无法落地。**残差标记**：CI 无生产装配冒烟（全 harness 显式注入 authorizer）——AC-5 数据面回归腿作为第一道防线（§6）。

---

## 3. 兼容性约束

| # | 约束 | 依据 |
|---|------|------|
| C1 | 无配置键/无 env/无 schema/无迁移/无 `go.mod` 变更 | I2/I6；`ACCESS_DELETE_FAIL_CLOSED` 保持唯一显式 opt-out，**但其 admin 面效果限于 registry 开（可归因 principal）时**（F1(a)，configuration.md:207 修正案，§3.1） |
| C2 | 中间件链 12 环不变（I4）；门禁在 handler 边界，不新增中间件环 | AGENTS.md §2.5 |
| C3 | `internal/jobs` 契约零变更（FR-6）；拒绝先于 enqueue 的顺序不变量由门禁位置保证 | jobs.go:90 为 post-commit INSERT |
| C4 | 对象 CRUD 路径（REST files/S3/WebDAV/MCP）授权阶梯、读/写/上传、其他 admin 路由（tenants/keys/jwt/jobs/config/audit）不变 | I5；门禁只挂在 `DeleteFile` |
| C5 | `requireAdmin`/auth registry 语义不变（§0.3 R1） | 既有测试锁定（`TestAdminDeleteFile_RequireAdmin`） |
| C6 | s3compat 端口不动；边界独立同形，不重构共享类型 | 规格 §5 |
| C7 | **行为翻转点（rev 2 修订为真实目录）**：① admin 边界 provider 错误 500→403（G2 修复，wire）；② write-scope member 204→403 **重新定性为契约收紧**——生产 wire 不可达（`requireAdmin` 需 `ScopeAdmin`，admin.go:457-467，member 即 403；匿名 401 先行；矩阵臂仅经 principal 注入在单测可观测，AC-1e），非 wire 翻转；③ admin 获准删除的审计 `Detail` 由 `"hard"/"soft"` 变 `"hard;permission=vault.file.delete"/"soft;permission=vault.file.delete"`（G4 修复，wire-visible，`docs/api.md` 同步）；④ **（新增，F1(a)）registry 关（auth-off）⇒ 零 principal ⇒ 边界恒 403**——HEAD 对象存在时 500(panic)/缺失时 404 → 403；`ACCESS_DELETE_FAIL_CLOSED=false` 的 legacy-allow 对 admin 面**不可恢复**（§3.1 决策表；configuration.md:207 修正案；S3 网关先例：该 flag 本就不作用于 S3）；⑤ **（新增，F5）跨租户未授权 key 对缺失对象 404→403**（门禁先于 `GetObject`，存在性 oracle 闭合；含零配置缺失对象 404→403） | 规格 §6.2 + rev 2 实测 |
| C8 | **行为不变（rev 2 修正，含 registry 维度；完整表见 §3.1）**：默认配置（registry 关 + access 关）admin 删除 **HEAD 为 500 panic（非 403，§0.2.3-3 实测），D8 修复后 403、边界落地后恒 403**；registry 开 + access 开：operator / 同租户 tenant-admin 204（实测）、跨租户 403 `tenant_mismatch`（对象存在，不变）；软删/硬删语义；WORM/legal-hold 409；存储失败 500；审计失败事务回滚 | §0.2.3（修正）+ rev 2 实测 |
| C9 | 双门禁共查为纯本地判定；矩阵零状态、并发安全（无共享可变状态） | §2 D2 |
| C10 | deny 响应体不含 provider 内部错误文本/panic 值（H5） | s3compat 先例（R2 同款姿态） |

### 3.1 行为决策表（admin-delete 门禁，rev 2）

> 完整空间 {registry on/off × access on/off × `ACCESS_DELETE_FAIL_CLOSED` × principal}。目标租户 acme；"对象存在"为默认，"对象缺失"仅在与存在时结果不同才单列。HEAD 列 = **2026-08-06 实机复现**（空 env 实启动 + 种对象 + curl；行 3 为结构推导）；落地列 = D8 前置修复 + 边界门禁后。软删/硬删同门禁（D2），不单列。

| registry | access | FAIL_CLOSED | principal | HEAD（实测） | 落地后 | 说明 |
|---|---|---|---|---|---|---|
| off | off | true | —（零 principal） | 500 panic（存在）/ 404（缺失） | **403** | F1(a) 基础行；HEAD 已违背文档（文档称 403） |
| off | off | **false** | —（零 principal） | 500 panic（存在）/ 404（缺失） | **403-forever** | **F1(a)**：文档 legacy-allow（204）对 admin 面不可恢复 ⇒ configuration.md:207 修正案 |
| off | on | true/false（无关） | —（零 principal） | 403 `missing_principal`（存在，服务门禁）/ 404（缺失） | 403 | reason 词汇变 `tenant_mismatch`（R2）；缺失对象 404→403 |
| on | off | true | operator / tenant-admin / file_admin（同租户） | 500 panic | **403** | D8 后 FR-1 门禁生效（文档语义） |
| on | off | **false** | 同上 | 500 panic | **204** | legacy allow 恢复（configuration.md:207）——**前提 D8**，否则仍 500 |
| on | on | true/false（无关） | operator / tenant-admin / file_admin（同租户） | 204 | 204 | 不变（实测；Manager enabled 时 DeleteFailOpen 不参与） |
| on | on | true/false（无关） | tenant-admin 跨租户（对象存在） | 403 `tenant_mismatch` | 403 | 不变（实测） |
| on | on | true/false（无关） | tenant-admin 跨租户（对象缺失） | **404** | **403** | **F5**：存在性 oracle 闭合（实测） |
| on | on/off | true/false | member / write-scope | 403（`requireAdmin`，admin.go:457-467） | 403 | C7② 契约收紧：wire 不可达，仅单测可观测（AC-1e） |
| on | on/off | true/false | 匿名（无 token） | 401 | 401 | 认证中间件先行（实测） |

**决策表要点**：① 唯一"可配置"分支是 registry 开时的 fc=false legacy-allow（204）——它要求先有**可归因 principal**；② registry 关恒 403 是**有意为之**（admin 面=操作员信任面 C3，不经鉴权的远程管理删除在任何 fc 值下都不可接受），因此不新增名字/键/flag 恢复（D6/§8），configuration.md 必须修正而非绕行；③ 全部 403 分支零副作用（门禁先于 svc.Delete，D2）；④ 行 5/行 8 的差异是 C7 ④⑤ 两个真实 wire 翻转；行 2 的 403-forever 是本文档对规格之外配置空间的**显式声明**（规格 §5 的 opt-out 语义在 admin 面被收窄）。

**docs/configuration.md:207 修正案（落地时同步执行，C7④ 的配套）**——单元格描述追加 admin 边界条款：

> Fail-closed delete gate: with access control disabled (`ACCESS_CONTROL_ENABLED=false`), object deletes (REST/WebDAV/admin/MCP/quarantine via the service gate) are denied unless this is explicitly `false` (restores the legacy allow). **The S3 gateway is unaffected** — it always denies deletes without access control, and this flag does not re-enable it; only `ACCESS_CONTROL_ENABLED=true` does. **The admin-delete boundary (`DELETE /v1/admin/files/...`) requires an attributable principal: when the auth registry is disabled, admin deletes are denied (403) regardless of this flag — the legacy allow applies only to authenticated principals with admin scope (registry enabled).** See breaking-change notes.

---

## 4. 失败模式

| # | 失败 | 行为 | 副作用 |
|---|------|------|--------|
| F1 | provider 未设置（nil） | 403 `AccessDenied`，文案 `"forbidden: no authorization provider configured"` | 零（门禁先于 svc.Delete） |
| F2 | provider 返回 error | 403 + 服务端 `Warn` 日志（tenant/key/err，**err 不进响应体**） | 零 |
| F3 | provider panic | recover → 403 + `Warn`（panic 值）；服务不崩、后续请求正常（正控：随后 GET 200） | 零 |
| F4 | provider 超时（ctx deadline） | 403 + `Warn`；请求有界返回（`adminDeleteAuthzTimeout`，生产 10s） | 零 |
| F5 | provider 返回 deny | 403 + `Debug` 日志（reason）；reason 进响应文案（R2 词汇） | 零 |
| F6 | 边界放行但 service 门禁拒绝（如租户 disabled） | 既有 `classify` 映射（`ErrTenantDisabled`→403 等），语义不变 | 零（service 门禁先于写） |
| F7 | 组合注册表未知名字 | 组合根解析失败 ⇒ 启动期暴露（harness `t.Fatalf`/生产静态单条目不可达），绝不落到运行时 500 | — |
| F8 | 门禁与 svc 双查的 provider 双调用 | 本地、零状态、幂等；代价可忽略（矩阵 O(1)） | — |
| F9 | deny 后重试（provider 恢复） | 无残留（F3 语义）⇒ 重试成功；对象/outbox/audit/jobs 全零行保证幂等 | 零 |
| F10 | 并发删除同 key | 门禁 per-request 无共享状态；PDP 侧既有多写语义（版本化/软删）不变 | — |
| F11 | **service 门禁 typed-nil panic（access 关时生产装配，3cf3dfd 引入）** | D8 前置修复（cmd/server 1 行 nil 守卫）消除；边界 deny 臂先于 service 永不触达；allow 臂（registry 开 + operator）修复前 500 —— **实现顺序：D8 必须先于门禁接入（§5 步骤 2.5）** | 修复前：500 + panic 日志（实测）；AC-5 冒烟腿锁回归 |

---

## 5. 迁移步骤

每步以编译+测试为闸门；第 1 步与最后一步验证 `make check` 全绿。

1. **基线**：`go build ./...` + `go test ./internal/jobs ./internal/access ./internal/api/rest ./internal/integration` 绿（HEAD `acfaaf4`）。**可复现性注记（Finding 5）**：工作树 = HEAD + 未提交 WIP——`admin_files_delete_test.go`、`authz_gate_test.go`、`authz_delete_denied_test.go`、`authz_parity_test.go`、`authz_cli_failclosed_test.go`、`integration/admin_files_delete_test.go`、`permissions.go` 等文件**未跟踪**，若干已跟踪文件相对 HEAD 有修改。门禁在**工作树**上执行，本文全部行号锚点以工作树为准（已逐一核对）；仅从 commit 复现须先落地全部 WIP。
2. **新文件 `internal/api/rest/admin_authz.go`**：`AuthorizationProvider` 接口 + `AdminMatrixProvider` + `adminDeleteAuthzTimeout` var（R4）+ 测试桩类型（allowAll/denyAll/err/panic/timeout/counting provider，形状对齐 authz_gate_test.go:22-53；panic 桩 panic 值固定哨兵 `"pdp panic sentinel"`，供 AC-1c H5 断言）。纯增量，编译绿。
2.5 **D8 前置修复（必须先于第 3 步，F11，rev 2）**：`cmd/server/main.go` 对 `WithAuthorizer` 加 nil 守卫（`if accessManager != nil`）；configuration.md:207 文档语义恢复（access 关：fc=true ⇒ 删除 403 / fc=false ⇒ legacy allow 204；读/写 allow；AV 豁免不变）。回归测试：AC-4g + AC-5（§6）。此步独立可合入（修复面=全部删除/读写数据面 500 panic）。
3. **门禁接入**：`AdminHandler` + `authz`/`logger` 字段；`NewAdminHandler` 5 参；`authorizeFileDelete`（§7 形态）；`DeleteFile` 插入门禁 + ctx 注解；`NewRouter` + `adminAuthz` 参数，router.go:224 转发。**编译驱动**更新调用点（**计数已按工作树复核**）：
   - `NewAdminHandler` 共 **10 处**（9 处测试直构 + router.go:224 生产 1 处）：admin_audit_test.go:34、admin_keys_test.go:27、admin_ops_test.go:37/:77/:124、admin_tenants_test.go:35/:59、buckets_test.go:39/:71 —— 非删除路由测试传 `nil, nil`；admin_files_delete_test.go:407 是 F6 的 `&AdminHandler{...}` **字面量**（非构造调用），传 `allowAllProvider{}, slog.Default()`；router.go:224 转发 `adminAuthz`。
   - `NewRouter` 共 **9 处**：http.go:105（生产，§5.7 注册表解析）；**admin_files_delete_test.go:77 与 fullserver_test.go:131 是 harness 装配点，转发 harness provider**（分别转发 `newAdminDeleteEnv` 的 `authz` 参数与 `startFullServerOpts` 的 `adminAuthz` 参数——**不得传 nil**，否则集成 admin 套件 403 红）；其余 6 处纯编译驱动、不触达 admin-delete 路由，传 `nil`：handlers_test.go:42、authz_delete_denied_test.go:59、authz_cli_failclosed_test.go:108、authz_parity_test.go:124、admin_rate_limit_test.go:19、enterprise_access_test.go:60。
   **此时行为已翻转**（nil ⇒ deny）：既有 admin-delete 测试红，进入第 4 步。
4. **单测 harness 迁移**：`newAdminDeleteEnv` + `authz` 槽位（nil ⇒ deny）；**12 个既有调用点**（:151/:200/:219/:245/:261/:299/:317/:361/:375/:403/:438/:452）——**11 个**显式 `allowAllProvider{}`，**F2 调用点（:317）**传 `AdminMatrixProvider{}`（F2 断言 `tenant_mismatch` 由矩阵产生，语义不变；断言位于 :326-327）；F6 字面量（:407）同步。`TestAdminDeleteFile_*` 恢复绿。
5. **集成 harness 迁移**：`startFullServerOpts` + `adminAuthz` 参数，**:131 装配点转发该参数**（非 nil）；既有三个构造器（`startFullServer`/`startFullServerWithRelay`/`startFullServerWithAuthAndRelay`）内部传 `allowAllProvider{}`；新增 `startFullServerNamed(t, relayOpts, authKeys, providers, name)`（按名解析，未知名字 `t.Fatalf`）。既有集成测试恢复绿。
6. **FR-4 审计注解**：service 层 `WithDeletePermission`/`DeletePermissionFrom` + `deleteAuditEntry` 后缀；同步**变红的断言（完整清单，两个新 Detail 值均钉死）**：
   - 单测 `TestAdminDeleteFile_RouteAndPassthrough` 两个子测试（admin_files_delete_test.go:213/:239 精确断言）→ hard 臂 `"hard;permission=vault.file.delete"`（:213）、**soft 臂 `"soft;permission=vault.file.delete"`（:239，此前未写明）**；
   - 集成 `assertAuditRowFor(t, h.repo, "acme", "hard")`（integration/admin_files_delete_test.go:156 调用点）→ `"hard;permission=vault.file.delete"`；
   - 集成 leg2 审计列表检查（integration/admin_files_delete_test.go:346 `row.Detail == "hard"` 严格相等）→ 改 `"hard;permission=vault.file.delete"` 字面量（或前缀匹配）；
   - `docs/api.md`（:606 审计样例 `"detail":"…"` 保持占位，另在 admin 删除节注明注解格式）与 `internal/repository/audit.go:11-12` 常量注释（“Detail carries \"hard\"/\"soft\"”）同步（注释仅文档性，不构成测试红）。
   **其他删除路径测试不得变红**（未注解路径 Detail 不变，I5 回归护栏）。
6b. **F6 审计消费方普查（P3，已完成）**：树宽审计证明**无任何消费者假设 `Detail ∈ {hard, soft}`**——生产代码对 Detail 零比较、零解析：`audit_governance_claim.go:120`（`DetailSHA256` 摘要列扫描，不透明字符串）、`audit_governance_write.go:235` gap 扫描 → `facts.go:64→:26` 仅摘要化、`auditgovernance/http.go:166-167` 只外发 `detail_sha256`（L2 事实表注释不变量“raw details 不入表”保持成立——注解只改摘要输入，属预期）；webhook 载荷（`events/webhook.go`）与 outbox 事实（`OutboxFact{EventType,OriginID,TenantID,Payload}` 无 Detail 字段；`BuildDeletedFact`/`BuildNotifyFact` 载荷无 mode/detail）结构上不含审计 Detail；CLI `cmdAdminAudit`（cli_admin.go:387）与 SDK（types.go:177）均原样透传；无 golden 钉住 Detail（唯一字节级 golden 是 `events/schema_test.go` 的 deleted@1.1/notify@1.1 载荷，不受影响）；仓库层本身已证 Detail 为开放字符串（`object_worker.go:104` 写 `"av_infected"`）。**注解涟漪仅限 admin 路由**：注解只在 `AdminHandler.DeleteFile` 注入（router.go:352 唯一 admin 对象删除路由；`DeleteTenant`/batch/folder 均非经此 handler），WebDAV（dav.go:143/:198）、MCP（server.go:311）、REST files、AV quarantine 全部无注解 ctx 直调 `svc.Delete` ⇒ Detail 不变；`DeletePermissionFrom` 仅在 `deleteAuditEntry` 内读取，无注解即无后缀。
7. **组合装配**：新文件 `cmd/server/admin_delete_providers.go`（audit_governance.go 先例）承载 `adminDeleteProviders` 注册表 + http.go 解析装配（D6，AC-4d）；**不新增任何名字/键/flag 来恢复 registry-off legacy allow**（F1(a) 决策，§3.1/§8）。
8. **新测试**：AC-1（单测 **7 臂**：absent/error/panic/timeout/矩阵 + soft-deny（GAP1）+ registry-off nil-provider（GAP3）；H5 非泄漏断言并入 error/panic 臂，GAP2）+ AC-2/AC-3/AC-4（集成，§6，含 **CLI denied-path e2e**（GAP4）、**AC-4f auth-off+opt-out**、**AC-4g access-off+operator e2e**）+ **AC-5 数据面回归腿**（D8，rev 2）；`assertNoWriteSideEffects` 扩展 jobs 零行（DSN `SELECT COUNT(*) FROM jobs`）。
9. **全量闸门**：`make check`（gofmt/build/vet/test）· `make test-race`（AC-1d 不得 `t.Parallel()`——包级 var 缩短/还原非并发安全）· AC-4 编译级 grep（§6）· OpenAPI 无需同步（无新路由）。

---

## 6. 验收映射（可测试）

> 全部断言带**正控**与**调用计数**（fc46c395 N8–N15 反空洞硬化：每 deny 臂必须有非 403 对照与 provider 被实际咨询的证明）。

### AC-1（unit，`internal/api/rest/admin_files_delete_test.go`，包 rest）

| 臂 | 测试 | 锚点断言 |
|----|------|---------|
| 1a absent⇒403 | `TestAC1_AdminDelete_ProviderAbsent_Deny` | `newAdminDeleteEnv(t, "opsecret:*:admin", nil, nil, nil)`（**nil 槽位**，registry 开）→ `DELETE /v1/admin/files/acme/k.txt?hard=1`（operator key）⇒ 403 + body code `AccessDenied`；`GetObject` 成功；`assertNoWriteSideEffects`（扩展 jobs） |
| 1a′ 默认配置（registry 关 + nil，GAP3） | `TestAC1_AdminDelete_DefaultConfig_Deny`（新） | `newAdminDeleteEnv(t, "", nil, nil, nil)`（`authKeys=""` 关 registry + access 关 + **nil authz** = CI/生产默认形态，C8）→ 同请求 ⇒ 403 + `AccessDenied`；正控：同 env 换 `allowAllProvider{}` ⇒ 204 |
| 1b error⇒403 非 500 | `TestAC1_AdminDelete_ProviderError_Is403Not500` | `errProvider{errors.New("pdp outage")}`（authz_gate_test.go:41 形状）⇒ 403；body **不含** `InternalError` **且不含** `"pdp outage"`（H5 非泄漏，GAP2）；counting 桩 `calls==1`；正控：同请求换 allowAll ⇒ 204 |
| 1c panic⇒403 | `TestAC1_AdminDelete_ProviderPanic_Deny` | `panicProvider`（panic 值固定哨兵 `"pdp panic sentinel"`）⇒ 403；body **不含** `"pdp panic sentinel"`（H5，GAP2）；服务存活（随后 GET 200 正控）；`calls==1` |
| 1d timeout⇒403 | `TestAC1_AdminDelete_ProviderTimeout_Deny` | timeout 桩阻塞至 `ctx.Done()`；测试内缩短 `adminDeleteAuthzTimeout`（t.Cleanup 还原，R4）⇒ 403；请求有界返回（墙钟断言）；**不得 `t.Parallel()`**（包级 var 缩短/还原非并发安全，`make test-race` 门禁） |
| 1e 矩阵 | `TestAC1_AdminDelete_PermissionMatrix` | `AdminMatrixProvider{}` + principal 覆盖中间件（authz_gate_test.go:109/:124 同型，装于 `reg.Middleware()` 之后）：operator（`TenantID:"*"`）⇒ 204；`vault.tenant_admin`（目标租户）⇒ 204；`vault.file_admin` ⇒ 204；`vault.member` ⇒ 403；write-scope 成员 ⇒ **403**（对照 `scopeAllows` 对象路径 :171-174 会放行）；匿名 ⇒ 403；零 principal（显式 `Principal{}` 覆盖）⇒ 403。每 deny 臂：`calls==1` + 对象 GET 200 + 零副作用；每 allow 臂：204 + 正控 outbox 1+1（经既有 `TestAC2_AdminDelete_EventTypeFilteredState` 锚定，:112） |
| 1f soft-deny（门禁不在 hard 分支内，GAP1） | `TestAC1_AdminDelete_SoftDeleteDenied`（新） | `denyAllProvider{}` + `DELETE /v1/admin/files/acme/k.txt`（**无 `?hard=1`**）⇒ 403；`repo.GetObject` 成功且 **`DeletedAt == nil`**（`deleted_at` 未变）；`auditDeleteRows`（:117）== 0、outbox 零行（DSN）；正控：同请求换 `allowAllProvider{}` ⇒ 204 且 `DeletedAt != nil`（软删生效） |

### AC-2（integration，outbox 零条目）

| 臂 | 测试 | 锚点断言 |
|----|------|---------|
| deny⇒0 | `TestAC2_AdminDelete_Denied_NoOutbox`（新） | `startFullServerNamed(t, nil, "opsecret:*:admin", {"deny-all": denyAllProvider{}}, "deny-all")` + `putObjectAs` ⇒ 403 后 `outboxCountFor(deleted@1.1)==0`、`(notify@1.1)==0`、`deliveredTotal==0`、`assertNoWriteSideEffects`（jobs 扩展） |
| 正控 | `TestAC2_AdminDelete_EventTypeFilteredState`（既有 :112） | 获准 hard delete ⇒ deleted@1.1 + notify@1.1 **各恰 1**（"exactly-1+1 control"） |

### AC-3（integration，审计事实含权限名）

| 臂 | 测试 | 锚点断言 |
|----|------|---------|
| 获准⇒权限名 | `TestAC3_AdminDelete_AuditPermissionName`（新） | allow-all 装配 + operator hard delete ⇒ `audit_log` 行 `action=="file.delete"`、`tenant_id=="acme"`、`Detail` **含字面量 `vault.file.delete`**（扩展 `assertAuditRowFor` 或前缀匹配）；envelope 回归不动（schema_test.go golden + `outboxPayloadFor` 的 `"schema_version":"1.1"` 检查既有） |
| deny⇒零行 | AC-1 deny 臂锚 | 每个 deny 臂（含 1f soft-deny）`auditDeleteRows`（:117）== 0 |

### AC-4（integration，composition e2e + 编译级）

| 臂 | 测试 | 锚点断言 |
|----|------|---------|
| 4a 按名 deny | `TestAC4_AdminDelete_NamedProvider`（新） | 注册表 `{"deny-all":..., "allow-all":..., "err-provider":...}`；名字 `"deny-all"` 装配 ⇒ operator 403；对象存活（`GetObject` 成功）+ 零 outbox/audit/delivered/jobs |
| 4b 按名翻转（非硬编码证明） | 同上（第二 leg） | 注册名换 `"allow-all"` ⇒ 同请求 **204** —— 名字真实参与解析，非代码内硬编码（N8 类反空洞） |
| 4c outage fail-closed | 同上（第三 leg） | 名字 `"err-provider"` ⇒ 403 + 对象与 blob 完好 + 零副作用；timeout 桩同型（AC-1d 的 e2e 臂） |
| 4d 编译级（可执行定义，替换 `<sibling-id>` 占位） | `grep` 检查（CI 步骤固化） | **标识符**：`snaplink\|yangwb1123`（sibling 项目标识符集，沿用 audit-sink 轮 AC-7 先例）。**① 检查集零命中**：`grep -rln "snaplink\|yangwb1123" internal/api/rest/ internal/cli/ cmd/server/http.go` ⇒ 空输出（检查集全量，无豁免）。**② 组合根驻留点唯一（排除规则）**：`cmd/server/` 下任何命中必须恰为 `cmd/server/admin_delete_providers.go`——sibling 适配器注册表文件（`audit_governance.go` 独立文件先例，http.go 保持零标识符）；落地前零命中亦通过（不空转），落地后唯一合法驻留点 = 该注册表条目。CI 断言：`hits=$(grep -rln "snaplink\|yangwb1123" cmd/server/); [ -z "$(printf '%s\n' "$hits" | grep -v '^cmd/server/admin_delete_providers.go$')" ]` |
| 4e CLI denied-path e2e（GAP4） | `TestAC4_AdminDelete_CLIDeniedPath`（新） | `startFullServerNamed(t, nil, "opsecret:*:admin", {"deny-all": denyAllProvider{}}, "deny-all")` + `putObjectAs(t, h, "acme", "docs/a.txt")` + env（`AERO_ENDPOINT`/`AERO_API_KEY=opsecret`/`AERO_TENANT=""`）→ `cli.Run([]string{"admin","files","delete","acme","docs/a.txt","--hard"})` ⇒ **exit 1**（`readSuccessfulResponse` 对 403 返回 false，无 `"deleted\n"` stdout）；对象存活 + 零 outbox/audit/delivered/jobs；正控：注册名换 `"allow-all"` ⇒ exit 0 + `"deleted\n"`（对偶既有 `TestComposition_AdminFilesDeleteEndToEnd`） |
| 4f **auth-off + opt-out（F1(a) 必测腿，rev 2）** | `TestAC4_AdminDelete_AuthOff_OptOutStillDenied`（新） | `startFullServerNamed(t, nil, ""（authKeys 空 ⇒ registry 关）, {"admin-matrix": rest.AdminMatrixProvider{}}, "admin-matrix", svcShape{authorizer: nil, deleteFailOpen: true}（= fc=false 生产形状）)` + `putObjectAs` ⇒ `DELETE /v1/admin/files/acme/...`（无 Authorization）⇒ **403**（零 principal，恒拒）；对象存活 + 零 outbox/audit/delivered/jobs；**正控**：同装配换 authKeys=`"opsecret:*:admin"` ⇒ 204（差异来自 registry/principal 归因，非装配错误）。反空洞：svcShape 服务门禁全放行（fail-open）仍 403 ⇒ 403 确系边界矩阵产出。**现有腿全部 registry 开**（`startFullServerWithAuthAndRelay` 家族 + fullserver_test.go:131 harness 均传非空 authKeys）——此腿补 registry-关维度 |
| 4g **access-off + operator e2e（D8 契约腿，rev 2）** | `TestAC4_AdminDelete_AccessOff_Operator_LegacyAllow`（新） | registry 开 + operator + svcShape{authorizer:nil, deleteFailOpen:**true**} ⇒ **204**（configuration.md:207 legacy-allow 落地）；同装配 deleteFailOpen:**false** ⇒ **403**（FR-1 门禁）。两臂证明 `ACCESS_DELETE_FAIL_CLOSED` 对 admin 面在 registry 开时**真实生效**（D8 前提；HEAD 上同为 500 panic，§3.1 决策表行 4/5） |

### AC-5（integration，D8 数据面回归——typed-nil 缺陷冒烟，rev 2）

| 臂 | 测试 | 锚点断言 |
|----|------|---------|
| 数据面恢复 | `TestAC5_DataPlane_AccessOff_NoPanic`（新） | svcShape{authorizer:nil, deleteFailOpen:false} + registry 开 ⇒ REST `GET /v1/files/*` 200、`PUT` 201、`DELETE /v1/files/*` **403**（文档语义，非 500）；同装配 deleteFailOpen:true ⇒ `DELETE /v1/files/*` 204；全程无 panic 日志断言。**此腿补生产装配 CI 盲区**（HEAD 上同配置 500 panic，3cf3dfd，§0.2.3-3） |

### 回归锁定（迁移后必须保持绿）

`TestAdminDeleteFile_RequireAdmin`（401/403/204）· `TestAdminDeleteFile_RouteAndPassthrough`（Detail 已更新）· `TestAdminDeleteFile_ErrorMapping` F1–F8（F2 经矩阵、F6 经 allowAll 字面量）· `TestComposition_AdminFilesDeleteEndToEnd`（CLI 204 两 leg）· s3compat 7 门禁 · `authz_delete_denied_test.go` · `internal/jobs` 全套。

---

## 7. 最终代码形态（§6 验收后的落地骨架）

**`internal/api/rest/admin_authz.go`**（新文件，~80 行）：

```go
package rest

// AuthorizationProvider 是 admin-delete 边界的 fail-closed 端口（FR-1a）。
// 形状与 s3compat 端口同构；*access.Manager 结构化满足。nil ⇒ 拒绝。
type AuthorizationProvider interface {
	Authorize(ctx context.Context, principal access.Principal,
		action access.Action, resource access.Resource) (access.Decision, error)
}

// adminDeleteAuthzTimeout 约束 provider 调用（FR-1e）。包级 var 供单测缩短（R4）。
var adminDeleteAuthzTimeout = 10 * time.Second

// AdminMatrixProvider 是生产默认适配器（FR-2a/2b，D3）：
// 授予 = tenantMatches ∧ isAdministrator；scopeAllows/owner/ACL/tenant_default 不参与。
type AdminMatrixProvider struct{}

func (AdminMatrixProvider) Authorize(_ context.Context, p access.Principal, _ access.Action, res access.Resource) (access.Decision, error) {
	if p.TenantID != "*" && p.TenantID != res.TenantID {
		return access.Decision{Allowed: false, Reason: "tenant_mismatch"}, nil
	}
	if access.IsAdministrator(p) {
		return access.Decision{Allowed: true, Reason: "administrator"}, nil
	}
	return access.Decision{Allowed: false, Reason: "default_deny"}, nil
}
```

**`internal/api/rest/admin_files_delete.go`** 门禁（对齐 s3compat/authz.go:27-36 形状，deny 一律 `ErrForbidden` 包装 → classify :55 → 403）：

```go
// authorizeFileDelete 是 admin 删除门禁：absent/error/panic/timeout/deny 一律 403。
func (h *AdminHandler) authorizeFileDelete(w http.ResponseWriter, r *http.Request, tenant, key string) bool {
	deny := func(reason string, warn bool, err error) bool {
		if warn {
			h.logger.Warn("admin file delete authorization denied",
				"tenant", tenant, "key", key, "err", err)
		} else {
			h.logger.Debug("admin file delete denied", "tenant", tenant, "key", key, "reason", reason)
		}
		h.writeError(w, r, fmt.Errorf("%w: %s", service.ErrForbidden, reason))
		return false
	}
	if h.authz == nil {
		return deny("no authorization provider configured", false, nil)
	}
	principal, _ := access.PrincipalFrom(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), adminDeleteAuthzTimeout)
	defer cancel()
	var decision access.Decision
	var err error
	func() { // panic ⇒ deny（FR-1b③）
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("authorization provider panic: %v", p)
			}
		}()
		decision, err = h.authz.Authorize(ctx, principal, access.ActionDelete,
			access.Resource{TenantID: tenant, Bucket: service.DefaultBucket, Key: key})
	}()
	if err != nil {
		return deny("authorization provider error", true, err)
	}
	if !decision.Allowed {
		return deny(decision.Reason, false, nil)
	}
	return true
}
```

**`internal/api/rest/admin_files_delete.go` `DeleteFile` 插入点**（:20-35 中，校验之后、`svc.Delete` 之前）：

```go
	if !h.authorizeFileDelete(w, r, tenant, key) {
		return
	}
	ctx := service.WithDeletePermission(r.Context(), access.PermissionVaultFileDelete)
	if err := h.svc.Delete(ctx, tenant, service.DefaultBucket, key, hard); err != nil {
		h.writeError(w, r, err)
		return
	}
```

**`internal/service/file_delete.go`** 注解（FR-4）：

```go
type deletePermissionKey struct{}
func WithDeletePermission(ctx context.Context, permission string) context.Context {
	return context.WithValue(ctx, deletePermissionKey{}, permission)
}
func DeletePermissionFrom(ctx context.Context) (string, bool) {
	p, ok := ctx.Value(deletePermissionKey{}).(string)
	return p, ok
}
// deleteAuditEntry 内：
//   if perm, ok := DeletePermissionFrom(ctx); ok { detail += ";permission=" + perm }
```

**`cmd/server/admin_delete_providers.go`**（新文件，audit_governance.go 先例——http.go 保持零 sibling 标识符，AC-4d）：`adminDeleteProviders` 注册表（D6）；**`cmd/server/http.go`**：`buildRouter` 增 `adminAuthz` 参数，装配点按名解析（D6），`rest.NewRouter(..., adminAuthz, opts...)`。

**测试桩**（`admin_files_delete_test.go`，形状对齐 authz_gate_test.go:22-53）：`allowAllProvider` 已有（handlers_test.go:694-696，复用）；新增 `denyAllProvider`/`errProvider`/`panicProvider`（**panic 值固定哨兵 `"pdp panic sentinel"`**，AC-1c 的 H5 断言载体）/`timeoutProvider`/`countingProvider{inner, calls *atomic.Int32}`（AC-1 的 `calls==1` 断言载体）。

---

## 8. 范围边界（明确不做）

| 不做 | 理由 |
|------|------|
| `internal/jobs` 任何改动、admin 删除改异步 job | FR-6；403 须在 HTTP 边界同步返回 |
| 对象 CRUD 路径授权阶梯改造 | core-v1/s3compat/webdav 轮已 gate；FR-2b 只排除其透传（D8 的 nil 守卫是装配修复，非阶梯改造，不属此行） |
| 新 env flag / 配置键（含超时值） | 规格 §5；`adminDeleteAuthzTimeout` 为代码常量（R4）；`ACCESS_DELETE_FAIL_CLOSED` 保持唯一 opt-out，**但其 admin 面效果限于 registry 开（可归因 principal）时——registry 关恒 403，不新增名字/键/flag 恢复**（F1(a)，§3.1 决策表 + configuration.md:207 修正案；S3 网关先例：该 flag 本就不作用于 S3） |
| typed-nil authorizer 装配缺陷（cmd/server/main.go:94，D8） | **入本设计范围的前置修复**（1 行 nil 守卫）：不修则 access 关 + registry 开 + operator 臂落地后仍 500，configuration.md:207 契约继续失效；替代方案（仅标记、另行修复）被否（§2 D8） |
| REST files/* 数据面 500 panic（同源 typed-nil，3cf3dfd 引入，CI 盲区） | 随 D8 一并消除；设计显式标记为**既有缺陷**并新增 AC-5 冒烟腿；不做授权阶梯改造 |
| `requireAdmin`/auth registry/scope 语义改动 | 既有测试锁定；角色 principal 需 admin scope 才能触达矩阵是既有分层，非本方向缺陷 |
| s3compat 端口重构共享类型 | 边界独立同形（先例忠实复制） |
| `vault.file.delete` 词汇/`ActionForPermission` 改动 | core-v1 已 gate；本方向只消费 |
| CLI 改动 | 零 diff；403 ⇒ exit 1 为既有 HTTP 语义 |
| OpenAPI/schema/迁移改动 | 无新路由、无新列（权限名进 `Detail` 文本） |
