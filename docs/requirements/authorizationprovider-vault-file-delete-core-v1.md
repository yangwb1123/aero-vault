# 方向：vault.file.delete 权限词汇 + 核心 fail-closed —— AuthorizationProvider 端口（internal/auth 词汇 + access.Manager 门禁 + FileService 删除门禁）

> **模块：** `internal/auth`（组合面：`internal/access` + `internal/service` + `internal/api/rest` + `cmd/server`）· **来源分析：** `docs/auto/analyses/internal-auth-ae3d8e54.json`（方向 1）· **日期：** 2026-08-06
> **评分：** 价值 10 / 风险降低 9 / 工作量 6 / 置信度 9
> **验证基准：** 当前工作树。本文所有代码引用均已逐行对照验证（行号以当前工作树为准，方向文行号漂移已逐条标注）；`go build ./...` 当前全绿。
>
> **本文是增量规格：** 本方向的 **S3 边界部分已在上轮 campaign 落地**（`internal/api/s3compat/authz.go` 的 fail-closed `AuthorizationProvider` 端口，§2.1）——方向文 "no match for 'vault.file.delete'" 的声明已被该轮落地推翻。本文 FR 是**核心层增量**：① auth 权限词汇表达 `vault.file.delete`；② `access.Manager` 对 `ActionDelete` fail-closed + 细粒度授予；③ `FileService` 在 provider 缺失时对删除 fail-closed；④ 组合缝可替换性证明。

---

## 1. 问题陈述

COMPOSE-2026-017 要求对象删除权限 `vault.file.delete` 经 AuthorizationProvider 端口强制（fail_closed）。现状四处缺口：

1. **权限词汇缺失（`internal/auth`）**：`auth.Scope` 是封闭集合 {read, write, admin}（`internal/auth/auth.go:31-38`；`knownScope` 判定 :139，方向文引 :98-100 有漂移）。`Registry.Parse` 对未知 scope 直接 fail-closed 拒绝（auth.go:112-125），因此 AUTH_KEYS 环境键**语法上无法**携带 `vault.file.delete`；`PersistedKey`（`internal/auth/store.go:13-21`）无 Roles 字段——仅 JWT（`jwt.go` `claimsToKey` :124-133，Roles 来自 claims；方向文引 :129-130 有漂移）与 Snaplink（`snaplink.go:86`）能携带任意角色字符串。⇒ 运行时/环境键永远无法获得该授予。
2. **核心 fail-open（两处）**：
   - `FileService.authorize`：`if s.authorizer == nil { return nil }`（`internal/service/access.go:91`）——provider 未注入即放行。默认部署 `ACCESS_CONTROL_ENABLED=false`（`internal/config/config.go:215-217`）→ `buildAccessManager` 返回 nil（`cmd/server/access.go:11-19`）→ `WithAuthorizer(accessManager)` 注入 nil（`cmd/server/main.go:94,215`）→ 生产默认删除路径零权限判定。
   - `access.Manager.Authorize`：`if !m.cfg.Enabled { return Decision{Allowed: true, ...} }`（`internal/access/authorizer.go:24-26`）——Manager 在场但 Access 关闭时同样全放行。
3. **粗粒度管理授权 ≠ vault.file.delete**：REST admin 门禁仅 `ScopeAdmin`（`internal/api/rest/admin.go:455-466`）；Manager `isAdministrator`（`internal/access/authorizer.go:126-135`，方向文引 :131）把 scope "admin" 与字符串角色 `vault.tenant_admin`/`vault.file_admin` 视为全动作放行——不构成对象删除的细粒度授予。
4. **REST 边界无端口**：S3 边界已有 fail-closed `AuthorizationProvider` 端口（§2.1），但 REST `/v1/files` 删除（`internal/api/rest/handler.go:239-249`，无 adapter 级门禁，仅 `checkBucketPolicy`）与 FileService 核心仍 fail-open——**S3 与 REST 的删除安全基线不对称**。

### 触发场景（真实工作流）

1. 默认配置部署（无 AUTH_KEYS、ACCESS 未启用）：任意调用方 `DELETE /v1/files/{key}?hard=1` → auth 透传 → service nil⇒allow → 对象硬删 + `vault.file.deleted@1.1` outbox 行。
2. 启用 access 模块、键仅持 admin scope：删除 → Manager `isAdministrator` ⇒ allow。COMPOSE-2026-017 要求：无 `vault.file.delete` 授予 ⇒ 403。
3. 兄弟项目 PDP 接入：组合层硬连 `*access.Manager`（main.go:94,215；http.go:120），无法注入替代实现——需要可替换缝证明。

---

## 2. 现状与代码证据（已逐条验证）

### 2.1 工作树状态修正（分析快照之后的关键事实）

| 文件 | 内容 | 验证 |
|---|---|---|
| `internal/api/s3compat/authz.go:9-36` | `AuthorizationProvider` 端口（形状 = `access.Authorizer`，`*access.Manager` 结构化满足）+ `authorizeDelete`：`h.authz == nil ⇒ deny`、provider error ⇒ deny（Warn 日志，不向客户端暴露）、非 allow ⇒ deny | ✅ 读取全文 |
| `cmd/server/http.go:120` | `s3compat.NewRouter(svc, logger, accessManager)` — 端口已装配（`handler.go:23-30` / `router.go:14-15`） | ✅ |
| `internal/api/s3compat/policy.go:67-75`、`extra.go:441` | 单对象与批量 `?delete` 路径接入门禁 | ✅ |

**推论：** 方向文 "Verified: no match for 'vault.file.delete' in internal/ or cmd/ (grep)" **已被推翻**——grep 命中 `internal/api/s3compat/authz.go:10` 与 `policy.go:67`（权限字面量）及大量 `vault.file.deleted@1.1`（事件类型，属 outbox 命名空间，`internal/repository/event_outbox.go:22`）。S3 边界本轮已完成；**本规格的增量是 REST/核心层 + auth 词汇**。

### 2.2 方向文证据验证表

| # | 方向文引用 | 验证结果 |
|---|-----------|---------|
| E1 | `access/authorizer.go:10-12` — `Authorizer` 接口 `Authorize(ctx, Principal, Action, Resource) (Decision, error)` | ✅ 与引用一致（接口 + `Manager` 是唯一实现，见 E5） |
| E2 | `access/authorizer.go:24-26` — `cfg.Enabled=false` → `Allowed:true`（fail-open） | ✅ 与引用一致；其后依次 system/principal/tenant/ACL/capability/owner/admin/default 阶梯（:27-101） |
| E3 | `service/access.go` `authorize()` — nil authorizer → allow | ✅ `if s.authorizer == nil { return nil }` 在 :91；另 :66（filterAuthorizedVersions）与 :206（listAuthorizedObjects）对 nil 走无过滤路径（读路径） |
| E4 | `cmd/server/main.go:94,215` — `WithAuthorizer(accessManager)` | ✅ 两处（HTTP 与 worker 装配）均为 `*access.Manager` 或 nil |
| E5 | `cmd/server/access.go:11-19` — `buildAccessManager`：唯一 Manager 实现；`!cfg.Access.Enabled` → nil | ✅ 与引用一致；`ACCESS_CONTROL_ENABLED` 默认 false（config.go:215-217） |
| E6 | `auth/auth.go` `knownScope` 封闭集合 {read,write,admin} | ✅ 符号验证；行漂移：:98-100 → **:139**；`ScopeRead/Write/Admin` const :31-38；`Parse` 未知 scope → `failClosedRegistry`（auth.go:112-125） |
| E7 | `auth/auth_middleware.go` `checkScope` — scope 仅由 HTTP 方法派生 | ✅ :189-193：默认 `ScopeWrite`，GET/HEAD/OPTIONS/PROPFIND → `ScopeRead`；DELETE 恒为 write 档 |
| E8 | `auth/store.go` `PersistedKey` 无 Roles 字段 | ✅ :13-21（TokenHash/TenantID/Scopes/Label/CreatedAt/ExpiresAt/LastUsedAt）；`parseScopeString`（:54-63）**不校验** token → 持久化键的 Scopes 列可往返任意 token，**无需迁移** |
| E9 | `auth/jwt.go:129-130` — Roles 仅来自 JWT claims | ✅ `claimsToKey`（:124-133）Roles ← claims；Snaplink 同样提供 Roles（snaplink.go:86） |
| E10 | `auth/principal.go` `PrincipalForKey` | ✅ :10-35：Scopes map → 排序字符串列表 + Roles/Groups 拷贝 → `access.Principal`；新 token 经此自动进入 `Principal.Scopes`（零改动） |
| E11 | `api/rest/admin.go:455-466` — `requireAdmin` 粗粒度 ScopeAdmin | ✅ 与引用一致；auth 禁用时隐式 admin |
| E12 | `access/authorizer.go:131` — 字符串角色 `vault.tenant_admin`/`vault.file_admin` | ✅ `isAdministrator` :126-135（含 scope "admin" 与 tenant "*"） |
| E13 | "no match for 'vault.file.delete' in internal/ or cmd/" | ❌ **被推翻**：s3compat 端口已落地（§2.1） |

### 2.3 补充验证（本规格新增）

| # | 证据 | 验证结果 |
|---|------|---------|
| E14 | `access/types.go:76` — `ActionDelete Action = "object:delete"`：`vault.file.delete` 的域内映射 | ✅ 与引用一致；`Action`/`Decision`/`Resource` 定义 :59-99 |
| E15 | 删除路径全部经 `authorizeObject(ctx, access.ActionDelete, …)` 单一漏斗 | ✅ `file_delete.go:159,179`（Delete hard/soft、DeleteVersion）、`delete_marker.go:34,37`、`object_worker.go:58`（AV quarantine）、`file_bucket_settings.go:40,48` ——一处改 `authorize()`，全协议生效 |
| E16 | REST `DELETE /v1/files/{key}` 无 adapter 级授权端口 | ✅ `handler.go:239-249`：`checkBucketPolicy` → `svc.Delete`；rest 侧 `WithAccessManager`（handler.go:32）只服务 access 管理端点，删除判定完全依赖 service 门禁 |
| E17 | outbox 行只在授权通过后的删除事务内产生 | ✅ `file_delete.go:159`（授权）先于 :131-134（`EventTypeFileDeleted11` + `BuildDeletedFact` 写入）；403 路径零 outbox 行；断言基建：`HasEventOutboxFact`（file_delete_test.go:87）、`outboxStatus`（integration/fullserver_test.go） |
| E18 | AV quarantine 是 system-principal 删除路径 | ✅ `object_worker.go:51-60` `QuarantineObjectByID` → `authorizeObject(ActionDelete)`；`access/context.go:17-20` `WithSystemPrincipal`（`PrincipalSystem`）——**Manager 的 `trusted_system` 豁免（authorizer.go:27-29）必须保留**，否则默认配置下 AV 隔离断裂 |
| E19 | `Manager` 构造约束 | ✅ `manager.go:34-48`：`store` 非 nil、`ShareSecret` ≥ 32 字节；仓库满足 `access.Store`（buildAccessManager 判定）→ AC-2/AC-4 可用真实 repo 或 test store |
| E20 | 组合缝现状 | ✅ `file.go:96-99` `WithAuthorizer(authorizer access.Authorizer)` **已是接口**；`integration/fullserver_test.go:62-64` 直接构造 svc —— test-double 注入点存在 |

### 2.4 缺陷机理

```
REST DELETE /v1/files/{key}?hard=1（默认配置：无 AUTH_KEYS、ACCESS 未启用）
  ├─ auth Middleware: registry 禁用 → 透传
  ├─ svc.Delete → authorizeObject(ActionDelete) → s.authorizer==nil → return nil   ← 打开（E3/E5）
  ├─ checkScope（注册表启用时）: DELETE → ScopeWrite 档；admin scope 键通过        ← 粗粒度（E7）
  └─ 删除事务 → event_outbox 写入 vault.file.deleted@1.1                           ← 副作用（E17）

启用 access 模块后：Manager.Authorize → isAdministrator(scope "admin") → Allowed     ← 仍打开（E2/E12）
S3 同请求：s3compat.authorizeDelete → provider 在场/拒绝 → 403                        ← 已 fail-closed（§2.1）
```

---

## 3. 需求规格

### FR-1：权限词汇 —— `vault.file.delete` 可表达（`internal/auth`）

- `auth.Scope` 增加 `ScopeFileDelete Scope = "vault.file.delete"`；`knownScope`（auth.go:139）接受该值；`Registry.Parse` 的 AUTH_KEYS 记录可携带 `vault.file.delete`（如 `token:tenant:read+vault.file.delete`），**未知 scope 仍 fail-closed**（现有 `failClosedRegistry` 语义不变，E6）。
- 持久化键经既有 `Scopes` 列表达（`scopesToString`/`parseScopeString` 往返，E8）——**PersistedKey 不加 Roles 字段、无 DB 迁移**（范围边界 §5）；Admin API `AddKey` 已能写入任意 scope token（E8，parseScopeString 不校验）。
- `checkScope`（E7）**不改**：方法派生 scope 仍为 {read, write}，DELETE 传输门禁仍要求 write/admin 档——`vault.file.delete` 是**权限（permission）而非方法 scope**，只在 provider 层判定（FR-2）。
- `PrincipalForKey`（E10）零改动：新 token 经 Scopes map → `Principal.Scopes` 自动进入 provider 判定。
- **实现注意：** 不得用 `Key.Has(ScopeFileDelete)` 作授予判定——`Has` 对 admin 短路（auth.go:50-54），会使 admin 键"拥有"该权限；授予判定必须在 Manager 侧检查字面量 token（FR-2b）。

### FR-2：`access.Manager.Authorize` —— `ActionDelete` fail-closed + 细粒度授予

在 `Authorize`（authorizer.go:19-101）的 disabled 早退（:24-26）之后、通用允许阶梯之前插入 `ActionDelete` 专用分支，分支内部顺序固定：**disabled → system 豁免 → explicit-deny ACL → 授予检查 → 拒绝**：

- **(a) 禁用即拒绝**：`cfg.Enabled == false` 且 action == `ActionDelete` → `denied`（Reason 建议 `"access_control_disabled"`）；**读动作保持今日行为**（disabled ⇒ `Allowed:true`，E2 不回归）。
- **(b) 显式授予唯一判据**：`Enabled == true` 时，`ActionDelete` 放行**仅当** `principal.Scopes` 或 `principal.Roles` 含字面量 `"vault.file.delete"`；explicit-deny ACL 条目仍优先拒绝（既有 `hasEffect(EffectDeny)` 语义保留）；`ScopeAdmin`、owner 匹配、`tenant_default`、`isAdministrator` 角色（`vault.tenant_admin`/`vault.file_admin`）、`ActionAll` ACL 授予**均不**授予删除。
- **(c) system 豁免保留**：`PrincipalKind == PrincipalSystem` → `Allowed`（`trusted_system`，:27-29 不动）——AV quarantine 路径依赖（E18）。

### FR-3：`FileService.authorize` —— provider 缺失 ⇒ `ActionDelete` 拒绝

- `authorize()`（service/access.go:88-101）中 `s.authorizer == nil` 且 action == `ActionDelete` → 返回 `ErrForbidden`（带原因，如 `"no authorization provider configured"`）；**其他 action 保持 nil ⇒ allow**（CI/MVP 基线，E3 读路径不回归）。
- 所有删除路径经 `authorizeObject(ActionDelete)` 单一漏斗（E15）——一处修改，REST/S3/WebDAV/MCP 全协议生效。
- **system 豁免对称保留**：ctx 中 principal 为 `PrincipalSystem` 时 nil-provider 亦放行（与 FR-2c 对称；防默认配置下 AV quarantine `QuarantineObjectByID` 403，E18）。AC-3 用普通 user principal 断言拒绝路径。
- 语义后果（明示，非缺陷）：默认配置（无 provider）下**所有对象删除 403**——这是方向的刻意反转（fail-closed when provider absent），基线测试迁移见 §6。

### FR-4：组合缝 —— 删除授权决策唯一来源为注入的 `access.Authorizer`

- `FileService.WithAuthorizer` 已是接口（E20）；组合层（main.go:94,215）与 REST 删除路径（handler.go:239-249）不得在删除判定上引用具体 `*access.Manager`。
- 装配点可注入任意实现：`access.Authorizer`（FileService/REST）与 `s3compat.AuthorizationProvider`（结构性同形，http.go:120）——兄弟项目 PDP 适配器经此接入，无需触碰 FileService。
- 以 composition e2e 证明可替换性（AC-5）。

### FR-5：被拒请求零副作用

- 403 路径不写 `vault.file.deleted@1.1` outbox 行、对象保持存在、无事件发布——授权先于删除事务（E17）的既有顺序以测试显式锁定（AC-4/AC-5），防止未来把门禁移到 service 调用之后。

### 非功能约束

- **I5 保护**：读路径（Stat/Get/List，E3 的 :66/:206）与 PUT 写路径在 provider 缺失/禁用时的行为不变；AC-2/AC-3 各带读路径正控断言。
- 硬门禁：`gofmt` / `go build ./...` / `go vet ./...` / `go test ./...` 全绿；单文件 ≤ 500 行、单函数 ≤ 50 行（改动均为小函数级）。

---

## 4. 验收标准（可测试）

> 方向文 5 条 acceptance 全部保留；每条给出测试文件、装配与断言。

### AC-1 词汇：`Registry.Parse` 接受 `vault.file.delete`，未知 scope 仍拒绝（unit，`internal/auth/auth_test.go`）

对应方向验收 (1)。沿用现有 `TestParse_*` 模式（auth_test.go:13-120）：

```go
// 正例
reg, err := auth.Parse("t:acme:vault.file.delete")
// err == nil; reg.Enabled(); key, ok := reg.Lookup(ctx, "t"); ok;
// key.Has(auth.ScopeFileDelete) == true
reg2, _ := auth.Parse("t:acme:read+vault.file.delete")
// key.Has(auth.ScopeRead) && key.Has(auth.ScopeFileDelete)

// 反例（语义不变）
reg3, err := auth.Parse("t:acme:read+bogus")
// err != nil 且消息含 "unknown scope"；reg3.Enabled() == true（fail-closed）；
// reg3.Lookup(ctx, "t") == (Key{}, false)
```

同时断言 `knownScope(auth.ScopeFileDelete) == true` 且既有 `TestParse_*` 全绿（回归）。

### AC-2 Manager：`cfg.Enabled=false` 时 `ActionDelete` 拒绝、读动作不变（unit，`internal/access/access_test.go`）

对应方向验收 (2)。**文件修正：** 方向文写 `authorizer_test.go`，实际 Manager 测试文件是 `internal/access/access_test.go`（:30 已有 `NewManager(store, …)` 模式）。

```go
// 装配（E19 约束：store 非 nil、ShareSecret ≥32B）
store := /* 既有 test store 或 repository（满足 access.Store） */
m, err := access.NewManager(store, access.Config{
    Enabled: false, DefaultPolicy: access.DefaultDeny, ShareSecret: bytes.Repeat([]byte("s"), 32),
})

p := access.Principal{SubjectID: "u1", TenantID: "default", Kind: access.PrincipalUser, Scopes: []string{"admin"}}
r := access.Resource{TenantID: "default", Bucket: "default", Key: "a.txt"}

// ActionDelete → denied（Disabled 早退对删除失效）
d, err := m.Authorize(ctx, p, access.ActionDelete, r)
// err == nil && d.Allowed == false

// 读动作 → 今日行为不变
d2, _ := m.Authorize(ctx, p, access.ActionRead, r)   // d2.Allowed == true（access_control_disabled）

// 正控（Enabled=true + 授予）
m2, _ := access.NewManager(store, access.Config{Enabled: true, DefaultPolicy: access.DefaultDeny, ShareSecret: /* ≥32B */})
// p2 := Scopes:["admin"]                     → ActionDelete denied（FR-2b：admin 不授予）
// p3 := Scopes:["vault.file.delete"]         → ActionDelete allowed（FR-2b 正控）
// p4 := Roles:["vault.file.delete"]          → ActionDelete allowed
// p5 := PrincipalSystem                      → ActionDelete allowed（FR-2c 回归护栏，E18）
```

### AC-3 FileService：nil authorizer ⇒ `ActionDelete` 拒绝（unit，`internal/service/`）

对应方向验收 (3)。新测试（建议 `internal/service/access_failclosed_test.go`，或并入既有 service test）：

```go
repo, _ := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "x.db")); _ = repo.Migrate(ctx)
store, _ := storage.NewLocal(storage.LocalConfig{Root: t.TempDir()})
svc := service.NewFileService(store, repo, logger)          // 无 WithAuthorizer
// 预置对象（repo.PutObject 直插，绕过授权）
ctxU := access.WithPrincipal(ctx, access.Principal{SubjectID: "u1", TenantID: "default", Kind: access.PrincipalUser})

// 删除 → ErrForbidden
err := svc.Delete(ctxU, "default", "default", "a.txt", true) // err 为 errors.Is(err, service.ErrForbidden)
// 对象仍存在（repo.GetObject ok）、HasEventOutboxFact(objID, EventTypeFileDeleted11) == false（E17）

// 读路径不回归（E3）
// svc.Stat(ctxU, …) 正常返回；svc.ListObjects 正常返回

// system 豁免（FR-3）：WithSystemPrincipal(ctx) 下 Delete → 成功（若采纳豁免；AC-3 主断言用 user principal）
```

### AC-4 e2e：admin-scope 键无授予 ⇒ 硬删 403 + 零 outbox 行（outbox/e2e）

对应方向验收 (4)。装配（`internal/integration/fullserver_test.go` 先例，或 rest 包 httptest + `mw.Tenant(mw.Auth(h))`）：

```go
// 1) auth：auth.Parse("admintok:default:admin,deletetok:default:vault.file.delete") 注册表（env 等价）
// 2) provider：真实 access.Manager，Enabled=true、DefaultPolicy=deny、ShareSecret ≥32B（E19）
//    （仓库满足 access.Store —— 与 buildAccessManager 同判据，E5）
// 3) svc := NewFileService(...).WithAuthorizer(manager)；rest router + auth 中间件
// 4) 预置：PUT /v1/files/doc.txt（admintok，201）→ 记录 objID

// 负向：admin scope、无 vault.file.delete 授予
resp := DELETE /v1/files/doc.txt?hard=1  (Authorization: Bearer admintok)
// resp == 403 Forbidden（write 档 checkScope 通过，provider 拒绝）
// HasEventOutboxFact(objID, repository.EventTypeFileDeleted11) == false
// GET /v1/files/doc.txt → 200（对象仍在）

// 正控：授予是唯一判据
resp2 := DELETE /v1/files/doc.txt?hard=1  (Authorization: Bearer deletetok)
// resp2 == 204；HasEventOutboxFact(objID, EventTypeFileDeleted11) == true（E17）
```

### AC-5 composition e2e：test-double provider 替换本地 Manager，决策端到端生效（composition e2e）

对应方向验收 (5)。证明缝可替换、FileService 零改动：

```go
type stubAuthz struct { allow bool; gotAction access.Action; gotResource access.Resource }
func (s *stubAuthz) Authorize(ctx context.Context, p access.Principal, a access.Action, r access.Resource) (access.Decision, error) {
    s.gotAction, s.gotResource = a, r
    return access.Decision{Allowed: s.allow, Reason: "stub"}, nil
}

// 同一装配（svc + rest router + auth），仅把 WithAuthorizer(manager) 换成 WithAuthorizer(stub)
// stub.allow=false → DELETE ?hard=1 → 403 + 零 outbox 行 + 对象仍在
// stub.allow=true  → DELETE ?hard=1 → 204 + outbox 行存在
// 两轮之间 FileService/REST 装配代码零改动 —— 证明 provider 可替换
// 捕获断言：stub.gotAction == access.ActionDelete；stub.gotResource == {default, default, "doc.txt"}
```

---

## 5. 范围边界（明确不做）

| 不做 | 理由 |
|------|------|
| s3compat 端口改造（重复实现） | 已落地（§2.1）；本方向只保证 REST/核心与它共享同一决策源 |
| `checkScope`/方法派生 scope 语义改动 | FR-1 明示：`vault.file.delete` 是权限，非方法 scope（E7） |
| `PersistedKey` 加 Roles 字段 / DB 迁移 | 不必要：既有 Scopes 列可往返任意 token（E8） |
| `requireAdmin`（admin 路由）改动 | `vault.file.delete` 是对象级权限，非 admin 路由权限（E11） |
| bucket policy（IAM）语义改动 | 属于 s3compat 轮规格（`authorizationprovider-vault-file-delete-s3compat-v1.md`） |
| WebDAV/MOVE 删除路径改造 | 属 sibling 规格（`authorizationprovider-vault-file-delete-webdav-v1.md`）；但经 FileService 漏斗（E15）自动获得 FR-3 门禁 |
| notify@1.1 / webhook / AI 管线 | 无关 |

---

## 6. 基线影响（既有测试迁移，必须随 FR-3 一并处理）

FR-3 使**无 authorizer 的删除一律 403**（默认 CI 基线配置）。当前 23 个测试文件经 `svc.Delete`/REST DELETE/WebDAV DELETE 执行删除，其中直接构造 `service.NewFileService(store, repo, logger)`（无 `WithAuthorizer`）的包：`internal/service`（file_delete_test.go、delete_marker_test.go、object_version_delete_test.go、object_protection_test.go、usage_consistency_test.go、quota_test.go、multipart_versioning_test.go 等）、`internal/api/rest`（files 删除用例）、`internal/api/webdav`（dav_relay_test.go、dav_audit_test.go）、`internal/integration`（fullserver_test.go 的 outboxStatus/删除用例）。

**迁移模式**（沿用 s3compat 轮先例，其 `authz_gate` 规格 AC-7 已要求对既有删除用例显式注入 allow-all provider）：

1. 测试装配统一注入 test-double authorizer（allow-all，非 ActionDelete 亦放行以保持基线行为）——`svc.WithAuthorizer(allowAllStub{})`；S3 侧沿用 `NewRouter(svc, nil)` → 注入同形 stub。
2. 新增的 fail-closed 语义由 AC-3/AC-4/AC-5 负向断言锁定，不靠删改既有用例期望。
3. 全仓 `go test ./...` 必须保持全绿（硬门禁）。

---

## 7. 实现指引（供验收后落地，非本规格交付物）

- **改动面**（均为小改动，遵守单文件 ≤500 行）：
  1. `internal/auth/auth.go`：`ScopeFileDelete` const + `knownScope` 扩集合（FR-1）。
  2. `internal/access/authorizer.go`：`Authorize` 插入 `ActionDelete` 分支（FR-2a/b，保留 :27-29 system 早退）。
  3. `internal/service/access.go`：`authorize()` nil-provider 分支（FR-3，含 system 豁免）。
  4. `internal/service/file.go:97` 注释更新（"nil authorizer preserves the CI/MVP baseline" 对删除不再成立）。
- **测试落地顺序**：AC-1（auth_test.go）→ AC-2（access_test.go）→ AC-3（service）→ §6 基线迁移 → AC-4/AC-5（integration 或 rest httptest）。
- **验证命令**：`go test ./internal/auth/ ./internal/access/ ./internal/service/ ./internal/api/rest/` → `go test ./internal/integration/` → `make check`。
- **文档同步**：`AGENTS.md` §2.5/§4 的 fail-open 描述（如 "缺省 default"、I5 基线说明）随落地更新。
