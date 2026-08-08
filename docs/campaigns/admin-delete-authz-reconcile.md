# Reconcile — admin-files-delete 授权语义 × fail-closed 权限不变量 × access 层契约

**基线：** HEAD `acfaaf4` · **方法：** 全源码逐行复核（file:line），无仓库改动 · **对象：** `docs/requirements/admin-files-delete-cli-v1.design.md`（设计交付物）
**上游输入：** `docs/campaigns/admin-delete-authz-audit.md`（security_reviewer 6 项建议）· test_plan_reviewer §7 · api_compat_reviewer E-1/G-3 · fail-closed 方向设计 `docs/requirements/fail-closed-vault-file-delete-v1.design.md`（I5 基线 / vault.file.delete 门）

## Verdict

| # | 领域 | 结论 | 处置 |
|---|------|------|------|
| 1 | 空租户拒绝（CLI exit 2 / handler 400） | ✅ **验证成立**——机制全链路 traced；两个拒绝点**都需要**（CLI 单点不足：直接 REST 调用方；handler 单点不足：CLI UX）；sibling 不一致确认 | 设计 §3.1/§3.2/§5 F13/§7 已改 |
| 2 | operator 等价模型（`ACCESS_CONTROL_ENABLED=false`） | ✅ **文档化**——代码路径端到端验证；与全部既有 admin 路由及 fail-closed 方向 I5 基线一致 | 设计 C3/F2/N2 已改 |
| 3 | bucket-policy 绕过行 | ✅ **确认**——`checkBucketPolicy("s3:DeleteObject")` 仅存在于 REST `Handler.Delete`；admin 路由不检查，属显式决策 | 设计 §5 F12 已加 |
| 4 | F2 测试缝（`access.Manager` / `denyAuthorizer`） | ✅ **确认与真实契约一致**——`denyAuthorizer` 无法判别 `tenant_mismatch`，F2 必须用真实 `Manager`；缝的装配契约（NewManager 三校验）逐条核对 | 设计 §7 F2 注已加 |

---

## 1. 空租户拒绝 —— 验证（audit 建议 3/5）

### 1.1 机制追踪（footgun 成立）

1. `cli.do`（`internal/cli/cli.go:53-67`）：仅当 `AERO_TENANT != ""` 时发送 `X-Aero-Tenant`（:62）——CLI 的 **tenant 参数与 header 完全独立**，空参数不会被 header 机制拦截。
2. 设计 §3.1 路径构造：`"/v1/admin/files/" + escapeKey(args[0] + "/" + args[1])`；`args[0]==""` → `escapeKey("/docs/a.txt")` → **`/v1/admin/files//docs/a.txt`**（双斜杠）。
3. chi v5.1.0（go.mod）对该路径匹配 `{tenant}=""`（audit 经验 harness 实测，与本 repo 既有 `{param}/*` 用法一致）。
4. `svc.Delete`（`file_delete.go:147-148`）→ `checkedObjectDefaults` → `defaults("")`（`file.go:264-269`）→ `tenant = DefaultTenant = "default"`（`file.go:23-24`）。
5. **结果：`admin files delete "" docs/a.txt --hard` 静默永久删除默认租户对象**——非 fail-closed。
6. Sibling 不一致：`DeleteTenant("")`（cli_admin.go:237 同构命令）→ `GetTenant("")` → **404**（admin-tenant 路径无归一化）；新端点是唯一「空串 = default」的 admin 面。

### 1.2 两个拒绝点均必要（验证结论）

| 拒绝点 | 覆盖面 | 不可替代原因 |
|--------|--------|-------------|
| CLI exit 2（§3.1，path 构造前） | CLI 用户 | 不发双斜杠请求；与 `TestCmdAdminTenants_Delete_TooFewArgs_Returns2` 同形状的 UX 级错误 |
| handler 400（§3.2，`svc.Delete` 调用前） | 直接 REST 调用方（curl） | 服务层按契约归一化（`defaults("")` 是 header 面服务的全局约定——REST/S3/WebDAV 的租户来自中间件/header，空串到不了服务层；**admin 路径租户面是唯一能把 `""` 送进 `svc.Delete` 的入口**），守卫必须在 adapter 层 |

- 400 复用 `classify` 对 `ErrInvalidArgs` 的既有映射（`handler_helpers.go:41-42` → `InvalidArgument`/400）——**无新错误码**，§5「无新失败面」结论保持。
- 放置纪律：这是**寻址规则**（tenant 非空），不是 key 校验——I3 的 key 校验位置（service `validateKey` file.go:191-203 + storage）零触碰；handler 只查租户非空，不重验 key。
- **X-Aero-Tenant 交互（验证）**：`authenticateBearer`（`auth_middleware.go:148-158`）仅在 tenant-scoped key 与**非空** header 冲突时 403；operator key 不钉；空 header 钉到 key 租户。该机制只约束**调用方身份**，从不携带/约束目标租户（目标只来自路径）——因此「空租户拒绝」**无法**委托给 header/中间件路径，必须在 handler 内检查路径参数。若改成 header 推导租户，则跨租户管理删除功能本身消失（C3 约束）。

### 1.3 设计改动落地

§3.1 `adminFilesDelete` 空租户 guard（usage + exit 2）· §3.2 `DeleteFile` 空租户 guard（`writeError(ErrInvalidArgs)` → 400）· §5 F13 行 · §7 AC-1 新增 `TestCmdAdminFiles_Delete_EmptyTenant_Returns2` / `TestAdminDeleteFile_EmptyTenant_Returns400` · §9 步骤 1/3 同步。

---

## 2. operator 等价模型 —— 文档化（audit 建议 1/2）

### 2.1 验证的代码路径

- `ACCESS_CONTROL_ENABLED` 默认 **false**（`internal/config/config.go:216`）。
- `buildAccessManager` 禁用时返回 `nil, nil`（`cmd/server/access.go:11-15`）→ `svc.WithAuthorizer(nil)`（`main.go:215-216`）。
- `FileService.authorize`（`internal/service/access.go:83-103`）：先 `requireActiveTenant`（:88-90，**无条件**），再 `if s.authorizer == nil { return nil }`（:91-93）。
- `Manager.Authorize`（`authorizer.go:14-31`）：disabled 分支 `Allowed:true`（:20-22）；`tenantMatches`（:29、:67-69）= `p.TenantID == "*" || (p.TenantID != "" && p.TenantID == r.TenantID)`。
- `authenticateBearer`（`auth_middleware.go:148-158`）只钉 **header**；admin handler 目标租户取自 **path**（设计 D5）。

### 2.2 模型陈述（设计 C3 定稿文本）

> 默认配置下，**没有任何机制把路径租户绑定到 key 租户** → 租户限定 admin key（`acme:admin`）+ `DELETE /v1/admin/files/other/<key>?hard=1` 成功。key 的 tenant 是**身份属性，不是权威边界**；租户限定 admin key 与 operator key **等价**。路径租户是唯一目标权威。

- **一致性**：与全部既有 admin 路由同模型——SetQuota/PutBucketQuota/SetBudget/DeleteTenant/SetTenantStatus 均为 path-tenant + `requireAdmin`-only（`router.go:333-355`）。本设计**不引入分叉校验**（audit 推荐 (a) 成立）。
- **新增的破坏半径（为何必须文档化）**：REST（`handler.go:243` `mw.TenantFrom`）、S3、WebDAV 的租户都来自钉定 header——此前 tenant-scoped key **不可能**删到别租户对象；admin files 端点是第一个 path-tenant 的对象删除面。
- **无条件保留的约束**（验证）：① `requireActiveTenant` 禁用租户 403（`main.go:95` + `access.go:105-121`，在 nil 早退**之前**执行）；② `requireAdmin`（registry 启用时，`admin.go:457-465`）；③ write-scope 门（`router.go:363-378`；`Key.Has` admin 蕴含全 scope，`auth.go:46-51`）。
- **access 管理器启用时**（`ACCESS_CONTROL_ENABLED=true`）：跨租户 → 403 `tenant_mismatch`（authorizer.go:29）；本租户内 `isAdministrator`（:126-135，admin scope）放行，对象 ACL 不影响；**显式 deny 优先**（:41-43，deny 检查先于 allow）。

### 2.3 与 fail-closed 方向设计的关系（不变量对账）

`docs/requirements/fail-closed-vault-file-delete-v1.design.md`（方向 4，**未实现**）定义的是**另一形态**：新 `vault.file.delete` action + `FileService.AdminDelete` + `authorizeVaultFileDelete` 门（authorizer nil → **403**，fail-closed）+ 路由形状 `/admin/files/{tenant}/{bucket}/*`。

| 不变量 | fail-closed 方向（未实现） | 本设计（admin-files-delete） | 对账 |
|--------|---------------------------|------------------------------|------|
| authorizer nil（I5 基线，Access 禁用） | `AdminDelete` → **403**（fail-closed 门的代价，其 F1） | `svc.Delete` → `object:delete` → **放行**（既有 I5 基线） | **一致**：本设计显式选择复用 `svc.Delete`（D8 零服务层改动），故继承 `object:delete` 的 nil→allow 语义；fail-closed 门只属于另一个端点形态 |
| `requireActiveTenant` | 其 D4：治理删除**不**调（须覆盖 disabled 租户） | **始终调用**（access.go:88-90）——admin 删除 disabled 租户对象 → 403 `TenantDisabled`（设计 F4） | **分歧，已按本设计基线定稿**：本设计零服务层改动 ⇒ 沿用既有 `Delete` 语义；若未来合入 fail-closed 形态，须在彼设计内决策 |
| 路由形状 | `{tenant}/{bucket}/*`（含 bucket 参数） | `{tenant}/*`（恒 DefaultBucket，D7） | 两形态若并存，`/admin/files/acme/default/k` 会被两条模式同时匹配（chi 按注册序取首个）——**合入顺序/形状须对账**，非本设计可决 |
| 错误码 | 409 `ObjectLocked`（其 F4，`classifyLock`） | 409 `ObjectLocked`（本设计 F5，复核修正后**一致**） | ✅ 同源：`checkObjectProtection` 统一 `ErrLocked` → `classifyLock`（management.go:224-229） |

**结论：** 本设计的授权语义 = 既有 `object:delete` 语义（I5 nil→allow + 条件 tenant_mismatch + 无条件 requireActiveTenant），**不是** fail-closed `vault.file.delete` 门；二者不矛盾（门是 opt-in 的彼端点专属），文档须避免把本设计描述成 fail-closed。

---

## 3. bucket-policy 绕过行 —— 确认（audit 发现 5 / api_compat G-3）

- REST `Handler.Delete`（`handler.go:238-241`）：`checkBucketPolicy(w, r, "s3:DeleteObject")` 在 `svc.Delete` **之前**执行。
- `checkBucketPolicy`（`handler.go:65-90`）：policy lookup 失败 / parse 失败 → **fail-closed 403**；`auth.Allowed(p, action, host)` 评估含**源 IP 条件**；policy 为空 → 放行。租户取自 `mw.TenantFrom(r.Context())`（:66，**header 租户**）。
- 设计 `DeleteFile`：**不执行**该检查 → 保护同一对象的 IAM 式 bucket policy（含源 IP deny）在 admin 面被绕过；access 层资源 ACL（启用时）仍生效（`svc.Delete` → `authorizeObject`）。
- 两个删除面的差异不止「查不查」：即便要查，policy 的租户源（header）与目标租户源（path）也不同——进一步说明绕过是**结构性**的。
- **决策（设计 §5 F12，文档化）**：bucket policy 是数据面协议守卫，admin 面为运维信任面（与全部既有 admin 路由一致：无任何 admin 路由执行 bucket policy）；需要策略保护时用 access 层 ACL deny。显式成行 ⇒ 契约而非事故。

---

## 4. F2 测试缝 —— 与真实契约核对（test_plan「F2 无测试」+ audit 建议 6）

| 缝 | 真实契约（逐行核对） | F2 适配 |
|----|----------------------|---------|
| `denyAuthorizer`（`internal/service/file_delete_test.go:57-69`） | `Authorize(context.Context, access.Principal, access.Action, access.Resource) (access.Decision, error)` —— 与 `access.Authorizer` 接口（`authorizer.go:10-12`）**逐字一致**；经 `svc.WithAuthorizer`（`file.go:96-98`）装配 | 只能证明「拒绝 → `ErrForbidden` + 零写入」（既有 `TestDeleteDenied_NoOutboxRow_ObjectUntouched` :71+ 已覆盖）——**全拒，无法判别 `tenant_mismatch`** |
| `access.Manager`（`authz_cli_failclosed_test.go:99-106`、`authz_parity_test.go:114` 先例） | `NewManager(store, cfg)`（`manager.go:34-45`）三校验：① store 非 nil——`repo.(access.Store)` 与生产 `buildAccessManager` 同一断言（`cmd/server/access.go:17-19`）；② `DefaultPolicy` ∈ {`DefaultDeny`,`DefaultTenant`}；③ `ShareSecret` ≥ 32 字节——`testShareSecret32`（`authz_parity_test.go:31`）恰 32 字节 ✅。Manager 满足 `Authorizer`（`authorizer.go:14`） | **F2 必需**：`Config{Enabled: true}` → `tenantMatches` 运行；tenant-scoped principal 跨租户 → `denied("tenant_mismatch")`（:29-31）→ service 包 `ErrForbidden: tenant_mismatch`（`access.go:100`）→ 403 |
| principal 注入 | `access.WithPrincipal` / `PrincipalFrom`（`context.go:7-11`） | 服务级 F2：`{SubjectID:"root", TenantID:"acme", Scopes:["admin"]}`（`SubjectID` 非空为前置，authorizer.go:26-28）vs 目标租户 `other`；正臂同租户 → `isAdministrator`（:126-135）放行 |
| REST 级 | `WithAccessManager(manager, "")`（`handler.go:32`） | 先例已验证（`authz_cli_failclosed_test.go:99-111`），无需新缝 |

**PDP 错误面（F2 邻接，fail-closed 确认）**：`Manager.Authorize` store 查询失败 → `denied("acl_store_error"), err`（:32-35）→ service 仍包 `ErrForbidden`（`access.go:97-99`）——PDP 出错即拒绝，可作额外表驱动臂。

**§7 映射落地**：`TestAdminDelete_TenantScopedKey_CrossTenantDenied`（service 级，真实 Manager，正反两臂）；§7 表下新增「F2 测试缝」注（契约 + 装配形状 + `denyAuthorizer` 的边界说明）。

---

## 5. 设计文档改动清单（已应用，11+2 处）

1. **N2** 行：`tenantMatches` 行号修正 +「纵深防御有配置前提」复核修正。
2. **D3** 行：「上游校验」声明删除 → 租户名路径安全为文档化使用约束（仓库层无租户名校验，admin.go:292-296 仅非空）。
3. **D6** 行：错误映射集合改为 **404/409/410/403/400/500**（412 剔除，409/400 补入）。
4. **§3.1** CLI：空租户 guard（exit 2，F13）。
5. **§3.2** REST：空租户 guard（400 `InvalidArgument`，F13）。
6. **C3** 行：operator 等价模型全文定稿（含无条件部分清单 + X-Aero-Tenant 身份-only 语义 + CLI 使用约束）。
7. **F2** 行：条件化（默认配置等价模型 vs access 管理器启用时 403）。
8. **F5** 行：412 → **409 `ObjectLocked`**（`classifyLock`，management.go:224-229）。
9. **§5 新增 F12**（bucket-policy 绕过，文档化决策）+ **F13**（空租户拒绝）。
10. **无新失败面结论**：补充 F12/F13 的既有分类复用说明。
11. **§7 AC-1**：新增空租户/409 测试 + reg 断言行为化 + hard 透传行为断言。
12. **§7 F2 注**：测试缝契约核对 + 测试映射。
13. **§9** 步骤 1/3：空租户拒绝落地项。

## 6. 范围外（其他 reviewer 已跟踪，非授权语义）

- test_plan_reviewer §7：AC-2 迁至 repository/integration 包、AC-3 措辞、AC-4 event_type join + absence-based 非阻塞证明（可执行性，非语义）。
- api_compat E-2：基线 make-check 声明（pristine HEAD gofmt 两测试文件）——构建/CI 声明。
- api_compat G-1/G-2：F7/F8「无部分状态」措辞（post-commit quota 窗口）、N6 preflightQuota 精确性——服务语义措辞。

## 7. 可证伪断言（本 reconciliation 的验收）

1. `admin files delete "" <key>` → stderr usage + **exit 2**，且**不发任何 HTTP 请求**。
2. `DELETE /v1/admin/files//<key>`（直接 REST）→ **400** `InvalidArgument`；`DELETE /v1/admin/files/acme/<key>` 正常 204。
3. `ACCESS_CONTROL_ENABLED=false`（默认）下，`acme:admin` key 经 `DELETE /v1/admin/files/other/<key>?hard=1` → **204**（operator 等价）；`ACCESS_CONTROL_ENABLED=true` + `DefaultDeny` 下同请求 → **403** `tenant_mismatch`。
4. 同对象：REST `DELETE /v1/files/<key>` 受 bucket policy `s3:DeleteObject` deny（含源 IP）拦截（403）；admin `DELETE /v1/admin/files/<tenant>/<key>` **不受**该 policy 影响（204）——文档化差异。
5. locked 对象 admin 硬删 → **409** `ObjectLocked`（非 412）。
6. `TestAdminDelete_TenantScopedKey_CrossTenantDenied` 用真实 `access.NewManager(repo.(access.Store), Config{Enabled:true, DefaultPolicy:access.DefaultDeny, ShareSecret:testShareSecret32})` 装配（三校验全部满足），正臂（本租户 admin scope）放行、反臂（跨租户）403。
