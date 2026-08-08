# 方向：vault.file.delete 的 fail-closed 强制 —— AuthorizationProvider 端口 + admin-delete action（WebDAV 删除面）

> **模块：** `internal/api/webdav`（+ `internal/service` 删除路径 + `internal/access` action 词汇） · **来源分析：** `docs/auto/analyses/internal-api-webdav-c346cab0.json`（方向 1） · **日期：** 2026-08-06
> **评分：** 价值 9 / 风险降低 9 / 工作量 6 / 置信度 9
> **本文所有代码引用均已对照仓库逐条验证**（行号以当前工作树为准，HEAD `acfaaf4`；方向引用的行号来自分析快照，略有漂移，以本文证据表为准）。
>
> **与既有规格/在途实现的关系：** `docs/requirements/authorizationprovider-vault-file-delete-s3compat-v1.md` 为 **s3compat adapter 边界**的同一方向（工作树中已落地 `internal/api/s3compat/authz.go` + `handler.go` 装配，未提交）。本规格按所选方向把 enforcement point 放在 **FileService 边界**（方向验收明确要求 "FileService.Delete with nil provider returns ErrForbidden"），与 s3compat 规格**不冲突**：本方向仅对**新增 action**（`ActionAdminDelete`）做 fail-closed 翻转，`ActionDelete`（`object:delete`）的 nil-authorizer 基线语义不变，s3compat 规格"service 侧基线不动"的承诺对既有 action 依然成立。两规格在**生产装配**上共享同一 `access.Manager`（main.go:94），决策一致；在**测试装配**上存在联动（见 §4 迁移面）。

---

## 1. 问题陈述

COMPOSE-2026-017 要求权限 `vault.file.delete` 经 **AuthorizationProvider 端口**以 **fail_closed** 语义强制，但今天删除 enforcement point 是 fail-open 的：

1. **端口 fail-open：** `FileService.authorize()`（internal/service/access.go:83-104）在 `s.authorizer == nil` 时直接 `return nil`（access.go:91-93）——"A nil authorizer preserves the CI/MVP baseline"（file.go:96-98）。默认部署 `cfg.Access.Enabled=false` → `buildAccessManager` 返回 nil（cmd/server/access.go:12-14）→ `WithAuthorizer(nil)`（main.go:94；:215 为测试装配同款）→ **生产默认路径删除无任何权限判定**。
2. **单 action、无 admin/forced-delete 区分：** `access.ActionDelete = "object:delete"`（types.go:76）是唯一删除 action；无对应 COMPOSE 权限 `vault.file.delete` 的字面量（`grep -rn "vault.file.delete" internal/` 仅命中事件类型/审计动作，属不同命名空间），无第二个 action 供 PDP 区分"普通删除"与"强制/硬删除"。
3. **无注入点：** `s.authorizer` 字段类型是接口 `access.Authorizer`（file.go:91；authorizer.go:10-12），sibling-project PDP 理论上可实现它——但 nil 时放行使"未装配 provider"与"允许"不可区分，端口形同虚设。
4. **WebDAV 删除面依赖同一条路径：** `davFS.RemoveAll → svc.Delete(hard=true)`（dav.go:141-145）。未认证请求在 auth 注册表禁用（默认，`Registry.Enabled()` 无任何凭据源时为 false，auth.go:143-147）时**无 principal** 直达 FileService；在注册表启用且匿名公读路径外时被 401 拦截（auth_middleware.go:138-146）。任何一条路径下 `access.Manager` 都只在 `cfg.Access.Enabled` 时拒绝（authorizer.go:20-22），否则放行。

### 触发场景（真实工作流）

1. 默认部署（`ACCESS_ENABLED=false`、auth 未配置凭据源）：WebDAV 客户端（Finder/Explorer/rclone）`DELETE /webdav/<key>` → 无 principal、无 authorizer → `svc.Delete(hard=true)` 放行 → 对象硬删除，`event_outbox` 写入 `vault.file.deleted@1.1` + `vault.file.notify@1.1` 事实（file_delete.go:46-48、:53）。
2. 管理员以为 `vault.file.delete`（域内映射 `ActionDelete`，角色 `vault.file_admin`，authorizer.go:126-133）已保护对象——实际默认部署两个门禁（access.go:91-93 与 authorizer.go:20-22）都未执行该判定。
3. 经 MOVE 重命名（copy-then-delete，dav.go:150-205）与 `?hard=1` REST 硬删（rest/handler.go:244-245）同样失效。

---

## 2. 现状与代码证据（已逐条验证）

| # | 证据 | 验证结果 |
|---|------|---------|
| E1 | `service/access.go:83-104` — `authorize()`：:91 `if s.authorizer == nil { return nil }` → **nil authorizer 即 allow（fail-open）**；:95 `s.authorizer.Authorize(...)`；:99-101 仅 `!decision.Allowed` 包 `ErrForbidden`（方向引用 :110-121，快照漂移） | ✅ 与引用一致（语义）；行号漂移 :110-121→:83-104 |
| E2 | `access/authorizer.go:10-12` — 端口形状 `Authorizer interface { Authorize(ctx, Principal, Action, Resource) (Decision, error) }`；:14-55 `Manager.Authorize`；:20-22 `if !m.cfg.Enabled { Allowed:true }`（方向引用 :22-55） | ✅ 与引用一致 |
| E3 | `access/types.go:67-83` — Action 常量块；:76 `ActionDelete = "object:delete"` **唯一删除 action**；:84-93 `ValidAction` 无 admin/forced-delete（方向引用 :69-82） | ✅ 与引用一致 |
| E4 | `service/file_delete.go:147-169` — `Delete`：:154-156 GetObject（NotFound→ErrNotFound）→ :159 `authorizeObject(ActionDelete, obj)` → :161 quota 预检 → :162-168 hard/soft（方向引用 :96-121） | ✅ 与引用一致（语义）；行号漂移 :96-121→:147-169 |
| E5 | `service/file_delete.go:174-214` — `DeleteVersion`：:179 `authorizeObject(ActionDelete, obj)`（永久删除同 action） | ✅ 补充验证 |
| E6 | `api/webdav/dav.go:141-145` — `RemoveAll`：`svc.Delete(ctx, f.tenant(ctx), DefaultBucket, name, true)` **恒 hard=true**；ErrNotFound→`os.ErrNotExist`（方向引用 :103-110） | ✅ 与引用一致（语义）；行号漂移 :103-110→:141-145 |
| E7 | `api/webdav/dav.go:150-205` — `Rename`（MOVE）= copy-then-delete：:198 源删除失败 → :199 回滚删除目标（同 hard 路径）；回滚失败仅 warn（:201-202） | ✅ 补充验证（fail-closed 后回滚删除同样会被拒——见 FR-6） |
| E8 | `cmd/server/http.go:143-172` — 全局链（12 环，I4）：`access_log→concurrency→recoverer→otel→rate_limit→tenant→auth→max_body→secure_headers→cors→cors_bucket→request_id`；main.go:164 `applyMiddleware(dispatcher, ...)` 包裹 `buildDispatcher`（http.go:70-77，WebDAV 前缀分发）→ **WebDAV 请求过同一 auth/tenant 链**（方向引用 :143-173） | ✅ 与引用一致 |
| E9 | `auth/auth_middleware.go:138-146` — `authenticateBearer`：无 token 且非匿名公读路径 → 401；:177-186 `withAnonymousPrincipal`：`Principal{SubjectID:"anonymous", TenantID, Kind:PrincipalAnonymous}`（:184）（方向引用 :142-184） | ✅ 与引用一致（语义）；行号漂移 |
| E10 | `cmd/server/access.go:11-20` — `buildAccessManager`：`!cfg.Access.Enabled` → 返回 `nil, nil` → main.go:92-95 `WithAuthorizer(accessManager)`；`service/file.go:91` 字段 + :96-99 `WithAuthorizer` 注释 "A nil authorizer preserves the CI/MVP baseline" | ✅ 补充验证（生产默认 = nil provider） |
| E11 | **x/net/webdav v0.55.0（go.mod:26，模块源码已核对）** `webdav.go:242-270` `handleDelete`：Stat 错误 → `os.IsNotExist` ? 404 : **405**；`RemoveAll` 任意错误 → **405 MethodNotAllowed**（硬编码，无 StatusCode 解包；`ServeHTTP` :61-101 原样写 status）→ **经 FileSystem 接口无法渲染 403** | ✅ 新增关键证据（决定 FR-5 的 adapter 预检设计） |
| E12 | **x/net/webdav v0.55.0** `file.go:614-646` `moveFiles`：`Rename` 错误 → **403 Forbidden**（:632-635）；Overwrite=T 时目标 `RemoveAll` 错误 → 403（:626-629）→ **MOVE 的拒绝可原生渲染 403** | ✅ 新增关键证据（FR-6） |
| E13 | `service/file_get.go:127-153` — `Stat`/`statObject`：:151 `authorizeObject(ActionRead, ...)` —— Stat **带读权限**；WebDAV DELETE 预检若经 Stat 会误伤"可删不可读"主体（delete 授权不蕴含 read 授权，authorizer.go:81-88 `actionMatches`）→ 需独立非读授权方法（FR-4） | ✅ 补充验证 |
| E14 | 副作用次序：`hardDeleteObject`（file_delete.go:18-56）—— chunk 清理 :29-31 → **storage blob 删除 :41-45** → `HardDeleteObjectWithEvent`（**事务内写 outbox** deleted@1.1/notify@1.1 + audit_log，:46-48，event_outbox.go:22 `EventTypeFileDeleted11`）→ 配额 :50-51 → `s.emit(EventDeleted)` :53。`Delete` 的授权 :159 在这一切**之前** → **deny 时零副作用**（方向验收第 3 条成立） | ✅ 补充验证 |
| E15 | 其余删除面：`rest/handler.go:244-245`（`?hard=1` 硬删）；`mcp/server.go:311`（恒软删 `hard=false`）；`s3compat/delete.go:19/29/32`（`DeleteVersion`/`CreateDeleteMarker`/`Delete(hard=true)`） | ✅ 补充验证（fail-closed 仅作用于硬删路径的波及面） |
| E16 | `access/context.go:7-10` — `PrincipalFrom`；`access/authorizer.go:126-133` — `isAdministrator` 含角色 `vault.file_admin`；`authorizer.go:81-88` — `actionMatches`（`ActionAll`/相等/read 特例，无 admin-delete 映射） | ✅ 补充验证 |
| E17 | 测试基建：`service/service_test.go:16` `newTestSvc`（无 authorizer）；`api/webdav/dav_test.go:53-73` `newTestServerWithSvc`（`NewFileService(store, repo, nil)` + `mw.Tenant`，无 auth 中间件）；delete 用例：dav_test.go:139 `TestDeleteRemovesResource`、:673 `TestDeleteMissingResource`、:282/:315/:355/:415/:863 MOVE 族（:415 期望**403**——moveFiles Rename 错误映射）、:823 `deleteFailStorage`；`service/object_version_delete_test.go`（`TestDeleteVersionRemovesOnlyTargetAndPromotesPrevious` 等） | ✅ 补充验证（回归面与迁移面） |
| E18 | 其他删除测试装配（**迁移面**，均 `NewFileService(store, repo, nil)` 直连）：`s3compat/handler_test.go:33`、`versioning_test.go:192`、`sigv4_test.go:38`、`policy_test.go:223`、`managed_sse_test.go:32`、**在途** `s3compat/authz_gate_test.go:82/217/341/437`；`rest/acl_test.go:32`、`admin_ops_test.go:35`、`buckets_test.go:36` | ✅ 补充验证（见 §4 迁移表） |

### 缺陷机理

```
WebDAV DELETE /webdav/{key}（默认部署：ACCESS_ENABLED=false，auth 无凭据源）
  ├─ auth 中间件：注册表禁用 → 透传，无 principal（E8/E9）
  ├─ davFS.RemoveAll → svc.Delete(hard=true)（E6）
  ├─ service 门禁 authorizeObject → s.authorizer==nil → return nil（E1/E10）  ← fail-open
  ├─ storage blob 删除 + HardDeleteObjectWithEvent（outbox @1.1）+ emit（E14） ← 副作用
```

---

## 3. 需求规格

### FR-1：AuthorizationProvider 端口（= `access.Authorizer`，fail-closed 语义化）

方向要求 "AuthorizationProvider port to inject a sibling-project PDP"。仓库中端口**已存在**且是接口：`access.Authorizer`（E2，authorizer.go:10-12），`FileService.authorizer` 字段类型即此接口（file.go:91），`WithAuthorizer` 是唯一装配点（file.go:98；main.go:94）。**本规格不新增接口类型**——`access.Authorizer` 即 AuthorizationProvider 端口：`access.Manager` 是默认实现，sibling-project PDP 实现同一接口即可注入（决策见 §5 决策记录）。

- **约束 a（fail-closed 默认）：** `authorize()`（access.go:83-104，nil 判定 :91-93）改为——**当 `s.authorizer == nil` 且 `action == access.ActionAdminDelete`（FR-2）时返回 `ErrForbidden`**（包装信息如 `fmt.Errorf("%w: no authorization provider configured", ErrForbidden)`，与 :100 风格一致）；其余 action 保持 nil-allow 基线不动（I5：nil 依赖不得破坏 core CRUD；MCP/CLI/REST 软删不受影响，E15）。
- **约束 b：** provider 的决策是删除的**权威判定点**：`s.authorizer.Authorize(ctx, principal, action, resource{tenant,bucket,key,kind,ownerID})`（access.go:95，principal 经 `access.PrincipalFrom`，E16）。`Authorize` 返回 error 与 deny 同待遇（:99-101 现状已包 `ErrForbidden`，无需改动）。
- **约束 c：** `requireActiveTenant` 保持先于 nil 判定（access.go:85-90 现状不动）。

### FR-2：admin-delete action（`vault.file.delete`，与 `object:delete` 区分）

`internal/access/types.go` Action 常量块（E3）新增：

```go
ActionAdminDelete Action = "vault.file.delete"
```

- **约束 a：** 加入 `ValidAction`（types.go:84-93）——ACL 端点可为其建 allow/deny 条目。
- **约束 b（Manager 超集映射）：** `actionMatches`（authorizer.go:81-88）新增：`wanted == ActionAdminDelete` 时 `granted == ActionDelete` 亦满足（`object:delete` 授权蕴含 `vault.file.delete`；显式 `vault.file.delete` 条目与 `ActionAll` 照旧命中）。**理由：** 既有 `object:delete` ACL 在硬删路径上不得失效（回归验收）；"admin 区分"的表达载体是**显式 deny**：`vault.file.delete=deny` 条目使硬删被拒而软删放行（E16 `matchingEntries` :71 / `hasEffect` :110 现状即可，无新逻辑）；sibling PDP 可自行实施更严策略。
- **约束 c：** `capabilityAllows` :114 / `scopeAllows` :140（authorizer.go）无需改动——均经 `actionMatches`，且 `scopeAllows` 对非读 action 走 `write` scope（与今天 `object:delete` 行为一致）。

### FR-3：service 删除路径的 action 选择（hard=admin / soft=object）

| 调用点 | 现 action | 新 action |
|--------|-----------|-----------|
| `Delete(hard=true)`（E4 :159） | `ActionDelete` | **`ActionAdminDelete`** |
| `Delete(hard=false)`（E4） | `ActionDelete` | `ActionDelete`（不变） |
| `DeleteVersion`（E5 :179，永久删除） | `ActionDelete` | **`ActionAdminDelete`** |
| `CreateDeleteMarker`（delete_marker.go:34/:37，可逆） | `ActionDelete` | `ActionDelete`（不变） |
| `DeleteBucket` 等 bucket 级（file_bucket_settings.go:38-41） | `ActionDelete` | `ActionDelete`（不变，方向范围外） |

- **约束 a：** `Delete` 的授权前置次序不变（GetObject → 授权 → quota 预检 → 删除；E4/E14）——deny 在一切 mutation 之前。
- **约束 b：** 与 in-flight s3compat 规格的映射一致：s3compat adapter 门禁用 `ActionDelete`（其规格 FR-2），service 层对硬删用 `ActionAdminDelete`；生产装配同一 Manager（FR-1），经 FR-2b 超集映射**合取后决策一致**（adapter allow ∧ service allow）。

### FR-4：非变更、非读授权的判定方法 `AuthorizeDelete`

新增导出方法（`internal/service/file_delete.go`，供 WebDAV 预检与 `Delete` 内部复用，单一事实源）：

```go
// AuthorizeDelete 判定删除是否会被允许，不做任何变更。hard 选择权限：
// 硬删要求 vault.file.delete（ActionAdminDelete，fail-closed：nil provider 即拒），
// 软删要求 object:delete（ActionDelete，保持 CI 基线）。
func (s *FileService) AuthorizeDelete(ctx context.Context, tenant, bucket, key string, hard bool) error
```

- 语义 = `Delete` 的授权前半段（GetObject，NotFound→`ErrNotFound`）+ `authorizeObject(相应 action)`；**不做**读授权（`Stat` 带 ActionRead，E13，误伤"可删不可读"）、不做 quota/删除。
- `Delete` 重构为调用本方法（行为等价，E4 次序不变）；`DeleteVersion` 直接换 action（E5）。

### FR-5：WebDAV DELETE 的 403 渲染（x/net/webdav 405 限制的适配）

x/net/webdav v0.55.0 `handleDelete` 将 `RemoveAll` 任意错误硬编码为 **405**、Stat 非 NotFound 错误为 **405**（E11）——service 层 fail-closed 的 `ErrForbidden` 经 `davFS.RemoveAll` 回传会渲染成 405，不满足验收 "DELETE returns 403"。因此：

- **约束 a（预检门禁）：** `Handler()` 现有包装器（dav.go:41-51，GET/HEAD Content-Type 预置同层）增补：`r.Method == DELETE` 且路径截取成功时，先调 `fsys.svc.AuthorizeDelete(r.Context(), tenant, DefaultBucket, name, true)`：
  - `ErrForbidden` → `http.Error(w, "forbidden", http.StatusForbidden)`，**不进入 `dav.ServeHTTP`**；
  - `ErrNotFound`/其他错误 → 透传（webdav 渲染 404/405，现状不变；目录/空 key 路径同理，E11 Stat-first 语义不破坏）；
  - 通过 → 进入 `dav.ServeHTTP`（其内部 Stat→RemoveAll 由 service 再次权威判定，纵深防御）。
- **约束 b：** `davFS.RemoveAll`（dav.go:141-145）不改——service 判定仍是权威；预检只负责 403 渲染。预检与 `RemoveAll` 使用同一 `AuthorizeDelete`，同一请求内 principal/provider 不变，无判定漂移。
- **约束 c：** 锁语义说明：预检先于 webdav 的锁确认（handleDelete :247-251）——被锁且被拒的资源返回 403 而非 423，可接受（资源本就无权删除）；既有锁测试不受影响（allow 路径不触发预检拒绝）。

### FR-6：WebDAV MOVE 的源删除预检（防 copy 后回滚双删失败）

`Rename`（copy-then-delete，E7）在 fail-closed 下会出现：源删除被拒 → 回滚删除目标**同样被拒**（dav.go:198-202）→ 目标残留 + 双名并存。因此：

- **约束 a：** `davFS.Rename` 在 copy（Get/填充）**之后、`svc.Put` 之前**插入 `svc.AuthorizeDelete(ctx, tenant, DefaultBucket, 源key, true)`；拒绝 → 直接 `return err`（moveFiles 渲染 **403**，E12；与 `TestMoveMissingSource` 期望 :415 一致——缺失源时 `AuthorizeDelete` 返回 `ErrNotFound`，错误类型与现路径相同，状态码不变）。
- **约束 b：** 通过 → 维持现状 copy→delete→（失败时）回滚；回滚失败仍仅 warn（E7）。

### FR-7：被拒请求零副作用

不变量：**任何被拒绝的删除（provider deny / provider 未设置 / 显式 deny）不得产生任何持久化副作用**——零 `event_outbox` 行（`vault.file.deleted@1.1`/`notify@1.1`，E14）、零 `object_events` 广播、零 `audit_log` file-delete 行、零 storage blob 变更、零配额变动。由授权前置次序（E4/E14）保证，并以 AC-3 显式锁定。

### FR-8：匿名/未认证主体的行为契约

- 默认部署（auth 注册表禁用，E8/E9）：请求**无 principal** 直达 FileService → nil provider → 硬删 `ErrForbidden` → WebDAV 预检渲染 **403**（FR-5）。
- auth 注册表启用：未认证 DELETE 在 auth 中间件即 **401**（auth_middleware.go:138-146，**不改**）；带凭据但 provider 拒绝 → 403（FR-5）。401/403 分层保持现状，本方向只保证"到达 WebDAV 的请求"fail-closed。

### 非功能约束

- `make check` 全绿（gofmt/build/vet/test，AGENTS.md §0）；新增/改动限于 `internal/access`（types.go、authorizer.go）、`internal/service`（access.go、file_delete.go）、`internal/api/webdav`（dav.go + 新测试文件）+ 各包测试装配一行；单文件 ≤ 500 行、单函数 ≤ 50 行。
- 纯 stdlib + 既有 `golang.org/x/net`；无新 `go.mod` 依赖（I6）；无 SQL/schema/迁移（I2）；不触碰中间件链（I4）与存储 key 校验（I3）。
- 不新增 `"vault.file.delete"` 之外的权限字符串/注册表（FR-2 常量即权限名）。

---

## 4. 验收标准（可测试）

> 测试替身（各包 `_test.go` 内定义，Go 测试不可跨包复用）：`allowAllAuthorizer`（恒 Allowed）、`denyAuthorizer`（恒 `ErrForbidden` 风格 deny）、`recordingAuthorizer`（记录 `(action, resource)` 后放行）、`errAuthorizer`（返回 error）。principal 注入经 `access.WithPrincipal`（access/context.go:7-10，auth 中间件同款，E9）；HTTP 侧用包装中间件（对齐 `mw.Tenant` 用法，dav_test.go:70-72 先例）。outbox 行经 repo 查询 `event_outbox` 表（E14）。

### AC-1 FileService.Delete 硬删 + nil provider → ErrForbidden（fail-closed，unit）

> 对应方向验收 "FileService.Delete with nil provider returns ErrForbidden (fail-closed) instead of proceeding"（hard=true 路径，即 WebDAV 删除面 E6；方向问题陈述原文即 `davFS.RemoveAll → svc.Delete hard=true`）。

```go
func TestDeleteFailClosedWithoutProvider(t *testing.T) {
	// 直接构造（不用 newTestSvc——其将装配 allow 替身，见 AC-6）：
	//   svc := NewFileService(store, repo, nil)   // nil provider
	// PUT "k.txt"
	// (a) err := svc.Delete(ctx, "", "", "k.txt", true)  → errors.Is(err, ErrForbidden)
	// (b) 对象仍可 Stat（未删除）
	// (c) DeleteVersion(ctx, "", "", "k.txt", vID)      → errors.Is(err, ErrForbidden)
	// 边界锁定（防过度 fail-closed）：
	// (d) svc.Delete(ctx, "", "", "k.txt", false)        → nil（软删基线不变，E15）
}
```

### AC-2 新 action 被 provider 端口使用，且与 object:delete 区分（unit）

> 对应方向验收 "new access action (e.g. 'vault.file.delete') distinct from object:delete is honored by the provider port"。

```go
func TestAdminDeleteActionHonoredByProvider(t *testing.T) {
	// svc.WithAuthorizer(recordingAuthorizer{})（record 到 t 变量）
	// svc.Delete(ctx, "", "", "k.txt", true)  → recorded action == access.ActionAdminDelete（"vault.file.delete"）
	// svc.Delete(ctx, "", "", "k.txt", false) → recorded action == access.ActionDelete（"object:delete"）
	// svc.DeleteVersion(...)                  → recorded action == access.ActionAdminDelete
	// 区分语义（internal/access 包内，Manager + SQLite repo）：
	//   条目 {ActionDelete, allow}            → Manager.Authorize(ActionAdminDelete)  → Allowed（FR-2b 超集）
	//   条目 {ActionAdminDelete, deny}        → Manager.Authorize(ActionAdminDelete)  → denied；
	//                                           Manager.Authorize(ActionDelete)       → Allowed（admin 区分）
	//   ValidAction(ActionAdminDelete) == true
}
```

### AC-3 provider-deny 在 repo/storage mutation 之前中止且零事件（unit）

> 对应方向验收 "provider-deny aborts before repo/storage mutation and emits no event"。

```go
func TestDeniedDeleteHasNoSideEffects(t *testing.T) {
	// svc.WithAuthorizer(denyAuthorizer{})；PUT "k.txt"
	// err := svc.Delete(ctx, "", "", "k.txt", true) → ErrForbidden
	// 断言（E14 次序锁定）：
	//   store.ListStorageKeys → blob 仍在（storage 未删）
	//   repo.GetObject → 行仍在（metadata 未删）
	//   SELECT COUNT(*) FROM event_outbox WHERE event_type='vault.file.deleted@1.1' AND key='k.txt' → 0
	//   SELECT COUNT(*) FROM event_outbox WHERE event_type='vault.file.notify@1.1' AND key='k.txt' → 0
	//   SELECT COUNT(*) FROM object_events  WHERE type='deleted' AND key='k.txt' → 0
	//   SELECT COUNT(*) FROM audit_log     WHERE action='file.delete' AND target='default/k.txt' → 0
	// 对照：allowAuthorizer 下同 key 硬删 → deleted@1.1 恰 1 行 + notify@1.1 恰 1 行（E14）
}
```

### AC-4 WebDAV e2e：未认证 DELETE → 403，对象存活（HTTP）

> 对应方向验收 "WebDAV e2e: unauthenticated DELETE via davFS returns 403"（dav 测试装配无 auth 中间件 = 默认部署直通形态，E8/E9/E17）。

```go
func TestWebDavUnauthenticatedDeleteIs403(t *testing.T) {
	// 装配：newTestServerWithSvc 同款但 **不** WithAuthorizer（nil provider，E17）
	// PUT /webdav/k.txt → 200
	// DELETE /webdav/k.txt → 403（FR-5 预检；x/net/webdav 原生会 405，E11——预检是 403 的唯一来源）
	// GET  /webdav/k.txt → 200（对象存活；AC-1 的 HTTP 形态）
	// DELETE /webdav/missing.txt → <500（404/405，现状不变，dav_test.go:673 语义）
	// 反向锁定：GET /webdav/k.txt → 200（门禁仅作用于 DELETE，I5）
}
```

### AC-5 WebDAV e2e：带 principal + allow provider → DELETE 204，MOVE 正常（HTTP）

> 对应方向验收 "authenticated-with-provider-allow DELETE succeeds"。

```go
func TestWebDavAuthenticatedDeleteSucceeds(t *testing.T) {
	// 装配：svc.WithAuthorizer(allowAllAuthorizer{})；handler 外包一层
	//   access.WithPrincipal(ctx, Principal{SubjectID:"alice", TenantID:"default", Kind:PrincipalUser})
	//   （auth_middleware.go:183 同款注入，E9）
	// PUT /webdav/k.txt → 200；DELETE /webdav/k.txt → 204（E6 RemoveAll 成功路径）
	// MOVE /webdav/k.txt → /webdav/m.txt（Overwrite:T）→ 204（FR-6 预检通过，E12）
	//   GET /webdav/k.txt → 404；GET /webdav/m.txt → 200（重命名完成）
}
```

### AC-6 回归：既有删除测试在 mock provider 装配后原样通过

> 对应方向验收 "Regression: existing delete tests (dav_test.go, object_version_delete_test.go) pass unchanged when a mock provider is wired"。**测试文件零改动**；装配层（helper）各加一行 `WithAuthorizer(allowAllAuthorizer{})`：

- `internal/service/service_test.go:16` `newTestSvc` → 覆盖 `file_delete_test.go`、`object_version_delete_test.go`、`delete_marker_test.go` 全部删除用例；
- `internal/api/webdav/dav_test.go:53-73` `newTestServerWithSvc` → 覆盖 `TestDeleteRemovesResource`(:139)、`TestDeleteMissingResource`(:673)、MOVE 族(:282/:315/:355/:415/:863，含 `deleteFailStorage` 回滚路径)、`TestTenantIsolation`(:455) 等；
- 显式装配 authorizer 的既有测试（`service/access_integration_test.go`、`rest/enterprise_access_test.go`）不受影响（自行覆盖字段）。

**联动迁移面（本方向 service 边界改动的诚实波及，与 s3compat 在途实现协调）：**

| 包 | 装配点（均 `NewFileService(store, repo, nil)`） | 迁移 |
|----|------|------|
| s3compat（在途实现 E18） | `handler_test.go:33`、`versioning_test.go:192`、`sigv4_test.go:38`、`policy_test.go:223`、`managed_sse_test.go:32`、`authz_gate_test.go:82/217/341/437` | svc 侧加 allow 替身（adapter 门禁与 service 门禁合取，FR-3b）；其 allow 用例的硬删/DeleteVersion 依赖 service 放行 |
| rest | `acl_test.go:32`、`admin_ops_test.go:35`、`buckets_test.go:36` 等 | 同上（`?hard=1` 用例，handler.go:244） |
| integration | `fullserver_test.go`（经 cmd/server 生产装配，manager 已注入） | 无需迁移；`authz_parity_test.go`（在途）同理 |

---

## 5. 范围边界（明确不做）与决策记录

| 不做 / 决策 | 理由 |
|------|------|
| **软删（`ActionDelete`）不 fail-closed** | 方向权限是 `vault.file.delete`（新 action）；软删保持 CI 基线（I5；MCP server.go:311、CLI/REST 默认软删）。方向验收的 "FileService.Delete" 按其问题陈述即 hard=true 路径（E6） |
| 不改 auth 中间件 / 中间件链（I4） | 未认证请求 401（注册表启用时）与 403（fail-closed）的分层保持现状（FR-8） |
| `DeleteBucket`/bucket 子资源/bucket policy | 非对象删除，不在方向证据/验收内；`ActionDelete` 语义不动 |
| `CreateDeleteMarker` 换 action | 可逆标记，非强制删除；s3compat 规格亦将其映射 `object:delete`（FR-3） |
| 新增 `AuthorizationProvider` 接口类型 | 端口已存在（`access.Authorizer`，E2）；Manager 与 sibling PDP 均实现同一接口（FR-1）。若 COMPOSE 命名必须在代码出现，可加 `type AuthorizationProvider = access.Authorizer` 别名（可选，非必需） |
| Manager 对 `ActionAdminDelete` 要求显式授权（严格模式） | 会令既有 `object:delete` ACL 的硬删全部失效，违背回归验收；超集映射 + 显式 deny 已提供区分能力（FR-2b） |
| WebDAV 预检扩展至 GET/PUT/PROPFIND/OPTIONS | 门禁仅作用于删除（AC-4 反向锁定） |
| 修复 x/net/webdav 的 405 硬编码（fork/patch） | 超出范围；预检渲染 403 是唯一改动点（FR-5） |
| MOVE Overwrite=T 的目标预删（moveFiles 内部 `RemoveAll(dst)`） | 由 service 权威判定 + moveFiles 403 渲染（E12）覆盖，无需 adapter 预检 |
| @1.1 事件 schema / outbox / audit 改造 | 已存在（E14）；本方向只做"被拒零行"断言 |
| `AuthorizeDelete` 被其他 adapter 复用 | REST/S3/CLI/MCP 经 `svc.Delete` 自身判定，无需预检（其错误映射无 405 问题） |

**proposed_vs_verified 对照：** verified——fail-open 端口（E1/E10）、单 action（E3）、单 Manager 实现（E2）；proposed——action 命名 `ActionAdminDelete = "vault.file.delete"`（FR-2）、端口形状复用 `access.Authorizer`（FR-1）、Manager 超集映射（FR-2b）、WebDAV 403 预检 + MOVE 预检（FR-5/FR-6，后者由 x/net/webdav 405/回滚证据驱动，属方向验收的必然推论）。

---

## 6. 实现指引（供验收后落地，非本规格交付物）

1. **`internal/access/types.go`**：Action 常量块加 `ActionAdminDelete Action = "vault.file.delete"`（E3）；`ValidAction` 加 case。
2. **`internal/access/authorizer.go`**：`actionMatches`（:81-88）加 `if wanted == ActionAdminDelete { return granted == ActionDelete }`（FR-2b）。
3. **`internal/service/access.go`**：`authorize()`（:83-104）nil 分支改为 `if action == access.ActionAdminDelete { return fmt.Errorf("%w: no authorization provider configured", ErrForbidden) }; return nil`（FR-1a）。
4. **`internal/service/file_delete.go`**：新增导出 `AuthorizeDelete`（FR-4，GetObject+authorizeObject(hard 选 action)）；`Delete` 改调之（:154-159 收敛）；`DeleteVersion` 换 `ActionAdminDelete`（:179）。
5. **`internal/api/webdav/dav.go`**：`Handler()` 包装器（:41-51）加 DELETE 预检（FR-5a，`errors.Is(err, service.ErrForbidden)` → `http.Error(w, ..., http.StatusForbidden)`）；`Rename`（:150-205）在 `svc.Put` 前加源删除预检（FR-6a）。
6. **测试**：`internal/service/authz_gate_test.go`（AC-1/2/3 + 替身）、`internal/api/webdav/authz_gate_test.go`（AC-4/5 + 替身）、`internal/access` 补 `ActionAdminDelete` 映射用例（AC-2）；`newTestSvc`/`newTestServerWithSvc` 各加一行 allow 替身（AC-6）；§4 迁移表各装配点加 allow 替身；`go test ./internal/service/ ./internal/api/webdav/ ./internal/access/ ./internal/api/s3compat/ ./internal/api/rest/` 与 `make check` 确认全绿。
