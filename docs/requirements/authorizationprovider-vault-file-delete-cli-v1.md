# 方向：fail-closed 权限的 CLI 表面 —— vault.file.delete 拒绝原因的透出（读 DELETE 错误体、渲染 403 拒绝原因、非零退出）

> **模块：** `internal/cli`（伴随最小服务端契约：`internal/api/rest` classify 消息 + `docs/api.md`）· **来源分析：** `docs/auto/analyses/internal-cli-17314662.json`（方向 3）· **日期：** 2026-08-06
> **评分：** 价值 7 / 风险降低 8 / 工作量 3 / 置信度 9
> **验证基准：** HEAD `acfaaf4` + WIP 工作树（`git status`：cmd/server/http.go、s3compat 等未提交改动，不触及 `internal/cli` / `internal/access` / `internal/service/access.go`）。本文所有代码引用均已逐行对照验证；`go build ./...`、`go vet ./internal/cli ./internal/service ./internal/access` 全绿。
>
> **本文是增量规格：** 服务端 fail-closed 门（auth 词汇 `vault.file.delete`、`access.Manager` 对 `ActionDelete` 的 fail-closed、REST admin 删除端点）属 sibling 规格 `authorizationprovider-vault-file-delete-core-v1.md`（设计已出、**未实现**）；S3 边界 AuthorizationProvider 端口已落地（`internal/api/s3compat/authz.go`）。本文只覆盖**操作端 CLI 的证据保真**：拒绝原因必须可诊断、可测试——并钉死"拒绝先于事件发射"的零副作用顺序。

---

## 1. 问题陈述

`vault.file.delete` 经 AuthorizationProvider 端口 fail-closed 强制后，被拒的删除将返回 403。但 CLI 目前**销毁证据**：

1. **`cmdRemove` 不读响应体**（`internal/cli/cli_crud.go:103` `resp.Body.Close()`，无 `io.ReadAll`）——操作员只看到裸 `HTTP 403`（:105 `fmt.Fprintf(os.Stderr, "HTTP %d\n", ...)`），没有拒绝原因。
2. **错误路径不统一**：`admin` 子命令 14 处走 `readSuccessfulResponse`（`internal/cli/response.go:10-20`；cli_admin.go 9 + cli_admin_buckets.go 5），5 处 ad-hoc `io.ReadAll`（`cli_admin.go:355/:375/:400/:428/:449`，其中 :449 即 `cmdDeleteBucket`（cli_admin.go:437）的 body 读，无 cli_crud.go 同型点）；且**所有**现有路径都只是把 body 原文（或丢弃）打印，**没有解析** `{error:{code,message,request_id}}` 信封。
3. **服务端把原因剥掉了**：`internal/api/rest/handler_helpers.go:55-56` `classify` 对 `service.ErrForbidden` 返回硬编码消息 `"access denied"`——即使 CLI 读了 body，也拿不到 `Decision.Reason`（如 `default_deny` / `tenant_mismatch` / `explicit_deny`）。

后果：fail-closed 部署下操作员无法区分"权限未授予（403 可恢复）"与"对象不存在（404）/ 服务故障（5xx）"，被拒删除的诊断只能靠服务端日志。本文要求一个 **denial-reason-aware 的共享错误路径**：读错误体 → 解析信封 → 渲染 `code + message`（message 携带 AuthorizationProvider 拒绝原因）→ 非零退出；并把"被拒请求零 outbox/事件副作用"钉为回归测试。

### 触发场景（真实工作流）

1. 操作员执行 `aero-vault cli rm docs/a.txt`，对象存在、bucket policy 通过，但 AuthorizationProvider 无 `vault.file.delete` 授予 → `DELETE /v1/files/{key}` → FileService `authorizeObject(ActionDelete)` 拒绝 → 403。**今天**：CLI 打印 `HTTP 403` 后退出 1，原因不可见。
2. 操作员误以为对象被删，重试 `get` 发现对象仍在——403 与 404 无法从 CLI 区分。
3. 组合测试/合规审计需要从操作端验证 fail-closed：无授予 → 拒绝 + 零 outbox 行；授予 → 删除 + `vault.file.deleted@1.1` 投递；运行中撤销 → 立即 403 且服务器健康。CLI 必须能打印原因以支撑该验证。

---

## 2. 现状与代码证据（已逐条验证）

### 2.1 方向文证据验证表

| # | 方向文引用 | 验证结果 |
|---|-----------|---------|
| E1 | `internal/cli/cli_crud.go`（cmdRemove 丢弃 body） | ✅ `cmdRemove` :93-107；:103 `resp.Body.Close()` 紧接 `c.do` 之后、无 `io.ReadAll`；:105 打印裸 `HTTP %d`。`cmdUpload`/`cmdGet`/`cmdList` 均读 body（:41/:62/:84）。**注意：** `cmdDeleteBucket` 不在 cli_crud.go，位于 cli_admin.go:437（body 读 :449） |
| E2 | `internal/cli/response.go`（readSuccessfulResponse） | ✅ :10-20 `io.ReadAll` → ≥300 时打印 `HTTP %d: %s`（body 原文）→ 返回 `(nil, false)`。**只原样打印，不解析信封**；成功路径语义良好 |
| E3 | `internal/access/authorizer.go:10`（Authorizer 接口） | ✅ :10-12 `type Authorizer interface { Authorize(context.Context, Principal, Action, Resource) (Decision, error) }` |
| E3b | `:65`（denied(reason)） | ✅ :65 `func denied(reason string) Decision { return Decision{Allowed: false, Reason: reason} }`；原因集合：`missing_principal`/`tenant_mismatch`/`acl_store_error`/`directory_store_error`/`explicit_deny`/`resource_acl_no_match`/`default_deny`（:27-62） |
| E3c | `:169`（authorizeOrDenied） | ✅ :169-177：`!decision.Allowed` → `errors.Join(ErrDenied, errors.New(decision.Reason))`——原因进入错误文本（`ErrDenied` 定义于 `types.go:13`） |
| E4 | `internal/access/types.go:76`（ActionDelete = `"object:delete"`） | ✅ 精确行号 :76；`Action` 常量集 :70-82（`object:list`…`asset:publish`、`object:export`、`*`）。**无** `vault.file.delete` Action 常量——全库 grep（internal/ cmd/）仅命中注释（`s3compat/authz.go:10`、`policy.go:67`）。方向文 "proposed, not verified" 准确 |
| E5 | `internal/service/file.go:98`（WithAuthorizer） | ✅ :96-100（nil authorizer 保留 CI/MVP 基线）；装配 `cmd/server/main.go:94,215`；判定实现 `internal/service/access.go` |
| E6 | `internal/auth/auth_middleware.go`（Bearer/tenant 头） | ✅ Middleware 链：Bearer/`ApiKey` 头认证 + `X-Aero-Tenant` 处理；CLI 侧 `cli.go:44-50` `do()` 设 `Authorization: Bearer …` 与 `X-Aero-Tenant`（`NewClient` :24-37 读 `AERO_ENDPOINT`/`AERO_API_KEY`/`AERO_TENANT`） |
| E7 | `internal/cli/cli_test.go:126`（TestDo_SetsAuthAndTenantHeaders） | ✅ 精确行号 :126-148：断言 `Authorization == "Bearer test-key"`、`X-Aero-Tenant == "acme"`；`newTestClient` :66-74 以 `t.Setenv` 设三 env。stdlib-only（I6） |

### 2.2 补充验证（本规格新增，与验收直接相关）

| # | 事实 | 证据 |
|---|------|------|
| E8 | **REST 403 映射剥掉原因**：`classify` 对 `service.ErrForbidden` 返回硬编码 `("AccessDenied", "access denied", 403)`，不用 `err.Error()` | `internal/api/rest/handler_helpers.go:55-56`；`writeError` :20-25 输出 `errorBody{Error:{Code,Message,RequestID}}` 信封（`dto.go:10-18`）；`docs/api.md:53` 文档化信封、:59 403 行 |
| E9 | **拒绝原因在 service 层已携带**：`authorize` 对 `!decision.Allowed` 返回 `fmt.Errorf("%w: %s", ErrForbidden, decision.Reason)` | `internal/service/access.go:100`（:91-94 nil authorizer ⇒ allow 基线不变；:97 provider error 不包 ErrForbidden——属 core 方向修复范围，本文不动） |
| E10 | **删除路径授权先于一切副作用**：`Delete` :147 → `authorizeObject(ctx, access.ActionDelete, obj)` :157 → 之后才 `softDeleteObject`/`hardDeleteObject`（:76/:18，内嵌 `deleteFacts` :123-146 写 outbox + `deleteAuditEntry` :100 写审计 + `emit`） | `internal/service/file_delete.go:147-172`；outbox 表 `0041_event_outbox.up.sql`；`EventTypeFileDeleted11 = "vault.file.deleted@1.1"`（`internal/repository/event_outbox.go:22`） |
| E11 | **零副作用断言基建现成**：`HasEventOutboxFact`（`internal/service/file_delete_test.go:87`）、`outboxStatus`（`internal/integration/fullserver_test.go:1226`）、`outboxCountFor`（`internal/integration/authz_parity_test.go`） | 均验证存在；`authz_parity_test.go:93` `TestCompositionRevokeRestoreParity` 即 mid-session 撤销/恢复组合测试的现成 harness（`access.Manager` 每请求实时读 store，`authorizer.go:32 ListApplicableACL` ⇒ 撤销即时生效） |
| E12 | **admin 子命令不一致**：`readSuccessfulResponse` 14 处调用点（cli_admin.go 9 + cli_admin_buckets.go 5 全覆盖）+ 5 处 ad-hoc `io.ReadAll`（cli_admin.go:355、375、400、428、449——**:449 即 `cmdDeleteBucket`（cli_admin.go:437）的 body 读**） | 方向文 "inconsistently reuse …" 属实 |
| E13 | **S3 边界先例**：fail-closed `AuthorizationProvider` 端口已落地；拒绝原因 Debug-only（R2），不回客户端（AWS 兼容） | `internal/api/s3compat/authz.go:10-32`（`authorizeDelete` 拒绝/错误一律 false → `AccessDenied` 403）。REST/admin 边界无此约束——本文的 message 契约只作用于 REST 信封 |
| E14 | **auth 词汇缺口**（依赖，非本文交付）：`auth.Scope` 封闭集 {read, write, admin}（`internal/auth/auth.go:31-33`），`knownScope` :139-140——AUTH_KEYS 目前**语法上无法**携带 `vault.file.delete`；属 core 方向 FR-1 | 方向文 "a distinct vault.file.delete permission constant is proposed, not verified" 在 auth 词汇与 Action 常量两处均未落地 |
| E15 | `ACCESS_CONTROL_ENABLED`（默认 false）⇒ `buildAccessManager` 返回 nil ⇒ `WithAuthorizer(nil)`（main.go:94,215）⇒ 删除 fail-open——core 方向 FR-2/FR-3 的反转目标，本文**不改变**该基线 | `internal/config/config.go:216`；`cmd/server/access.go:11-19` |

### 2.3 缺陷机理（端到端）

```
cli rm key  →  DELETE /v1/files/{key}
  → REST Handler.Delete（handler.go:239-249，checkBucketPolicy 后）
  → svc.Delete（file_delete.go:147）
  → authorizeObject(ActionDelete)（:157）→ denied（access.go:100 携带 reason）
  → writeError → classify（handler_helpers.go:55-56）→ 403 "AccessDenied" + 硬编码 "access denied"   ← 原因在此被剥掉
  → CLI cmdRemove（cli_crud.go:103-105）→ resp.Body.Close() 不读 → stderr "HTTP 403"                  ← 证据在此被销毁
```

两个独立缺口：**服务端**把 reason 换成硬编码文本；**CLI** 把 body 整个丢弃。只修 CLI 只能拿到 "access denied"；只修服务端操作员仍看不到。本文两者都覆盖，但**交付物以 CLI 为主**（FR-1/FR-2/FR-4），服务端仅一处契约性伴随改动（FR-3，1 行 + 文档）。

---

## 3. 需求规格

### FR-1：共享 denial-aware 错误路径（`internal/cli/response.go`）

新增包级 `renderError(resp *http.Response) string`（命名以落地实现为准，契约如下）：

- **职责：** 消费（`io.ReadAll`）`resp.Body`，返回单行操作员可读错误文本；**调用方不得再读 body**（body 的消费是 helper 的硬契约）。
- **渲染规则（按序）：**
  1. body 可解析为 REST 信封 `{"error":{"code","message","request_id"}}`（形状与 `docs/api.md:53` 一致，`dto.go:10-18` 字段名）→ `HTTP <status> <code>: <message>`（`message` 即拒绝原因所在；`request_id` 非空时追加 ` (request <id>)`）。
  2. body 非空但不可解析（代理 HTML、纯文本等）→ `HTTP <status>: <raw body>`（保留现有 `readSuccessfulResponse` :17 的输出形状，不吞 body）。
  3. body 为空 → `HTTP <status>`。
- **禁止：** 任何路径在 body 非空时只打印裸 `HTTP <status>`；任何路径 panic（解析失败降级，不报错）。
- `readSuccessfulResponse` 的失败分支（response.go:16-18）改为复用 `renderError`（输出形状不变，消除两套渲染），成功路径语义不动。

### FR-2：删除路径迁移 + admin ad-hoc 读取统一（`internal/cli/cli_crud.go`、`internal/cli/cli_admin.go`）

- `cmdRemove`（:93-107）改用 FR-1 路径：≥300 → `fmt.Fprintln(os.Stderr, renderError(resp))` + `return 1`；204 → 既有 `return 0`。**回归钉：** 裸 `HTTP 403` 输出从此不存在于删除路径。
- `cli_admin.go` 5 处 ad-hoc `io.ReadAll`（:355/:375/:400/:428/:449，含 `cmdDeleteBucket` :437/:449）统一到 FR-1 路径——纯重构：2xx 输出逐字节不变，≥300 输出从 `HTTP %d: %s`（raw JSON）变为 `HTTP %d <code>: <message>`（信封渲染）。
- 本方向**不新增**任何 CLI 命令；未来 `admin files delete`（方向 1 的交付）落地时必须消费同一共享路径（组合验收钉此要求，见 AC-4）。

### FR-3：拒绝响应形状契约（denial response shape，伴随服务端改动）

- **契约：** 授权拒绝的 403 响应必须符合 `docs/api.md` 信封，且 `message` **携带 AuthorizationProvider 的拒绝原因**（`Decision.Reason` 原文，如 `default_deny`；`access.go:100` 的 `"forbidden: <reason>"` 文本即可，CLI 视为不透明文本原样渲染，不解析前缀）。
- **服务端伴随改动（最小）：** `handler_helpers.go:56` `classify` 的 `ErrForbidden` 分支消息由硬编码 `"access denied"` 改为 `err.Error()`（reason 已在错误链中）；`docs/api.md` 403 行补充说明"授权拒绝时 message 携带拒绝原因"。**S3 边界不改**（E13：R2 Debug-only 保持，AWS 兼容）。
- **CLI 侧验证：** FR-1 解析器针对契约做矩阵测试（合法信封 / 空 body / 非 JSON / 缺 `error` 字段），解析失败降级而非报错（AC-3）。

### FR-4：退出码契约

- 任何 ≥300 响应（含 403 拒绝）→ **退出码 1**；参数/用法错误仍为 2（既有约定，`cli.go Run` :49-56）。共享路径固化该映射，`cmdRemove` 现状（:104-106 已 return 1）不变，但由测试显式锁定（AC-1）。

### FR-5：fail_closed 零副作用顺序（回归钉，服务端行为锁）

- 被拒删除**不产生** outbox 行、**不发布**事件、对象状态不动——`file_delete.go:157` 授权先于 :76/:18 删除事务（E10 已成立，E17 先例）。本方向把它从"实现细节"提升为**被测试显式锁定的契约**（AC-2/AC-3c），防止未来把门禁移到 service 调用之后。

### 非功能约束

- 单测 stdlib-only（I6，无断言框架）；`make check` 全绿；`internal/cli` 各文件 ≤500 行（`cli_admin.go` 现 455 行，统一重构为纯替换、净增 0 行级别；`response.go` 现 27 行，增 ~60 行后仍远低于限制）。
- 向后兼容：`readSuccessfulResponse` 调用点（16 处）的**成功路径**输出不变；失败路径输出从 raw JSON 变为信封渲染属有意改善，非破坏。

---

## 4. 验收标准（可测试）

> 方向文四组验收逐一保留；每组映射到具名测试。断言基建：`captureStderr`（cli_test.go:30-53）、`newTestClient`（:66-74，`AERO_TENANT=acme`）、`HasEventOutboxFact`（file_delete_test.go:87）、`outboxCountFor`/`principalMW`/`TestCompositionRevokeRestoreParity` harness（integration/authz_parity_test.go:93）。

### AC-1 单元：httptest 403 + JSON 拒绝 → CLI 打印 AuthorizationProvider 原因、退出 1、body 被消费、tenant 头断言（`internal/cli/cli_test.go`）

- **T1 `TestCmdRemove_403Denial_PrintsReasonExits1`：** stub 返回 403 + `{"error":{"code":"AccessDenied","message":"permission vault.file.delete denied for principal alice","request_id":"r-1"}}` → `cmdRemove` 退出码 1；`captureStderr` 含 `permission vault.file.delete denied for principal alice`（方向文示例原因，原样渲染）；**不含**裸 `HTTP 403\n`。
- **T2 `TestCmdRemove_403Denial_BodyConsumed`（body 被消费的回归钉）：** 以计数 reader 包裹 stub body（`io.NopCloser` + 计数包装），断言 helper 返回后已读字节 == body 全长；并断言 stderr 含原因文本（原因只存在于 body，出现即证明读过）。
- **T3 `TestCmdRemove_403Denial_TenantHeaderAsserted`：** stub 断言 `X-Aero-Tenant == "acme"`、`Authorization == "Bearer test-key"`（`newTestClient` 注入；扩展 `TestDo_SetsAuthAndTenantHeaders` :126 模式到 DELETE 路径）。
- **T4 `TestCmdRemove_403Denial_NonJSONBodyDegrades`：** stub 返回 403 + 纯文本 `Forbidden` → 退出 1、stderr 含 `HTTP 403: Forbidden`、无 panic（FR-1 规则 2）。
- **T5 `TestCmdRemove_204_StillExit0`：** 既有 `TestCmdRemove_Success_Returns0`（:443）保持——迁移不改成功路径。

### AC-2 outbox 交付：被拒删除产生 **零** outbox 行（fail_closed 先于事件发射）（`internal/service/file_delete_test.go` + `internal/api/rest`）

- **T6 `TestDeleteDenied_NoOutboxRow_ObjectUntouched`（service 级）：** 注入拒绝型 stub authorizer（`access.Authorizer` 返回 `Decision{Allowed:false, Reason:"default_deny"}`）→ `svc.Delete` 返回 `errors.Is(err, service.ErrForbidden)`；`HasEventOutboxFact(originID, EventTypeFileDeleted11) == false`（:87 基建）；`repo.GetObject` 仍成功（对象未动）；`deleteAuditEntry` 零行。
- **T7 `TestRESTDeleteDenied_403_NoOutbox`（REST 级）：** `NewRouter(...)` + `h.WithAccessManager(manager, …)`（parity harness 同款）拒绝场景 → `DELETE /v1/files/{key}` 403 + `outboxCountFor == 0`——把"403 之后 outbox 表为空"钉在 HTTP 边界。

### AC-3 事件模式：拒绝响应形状文档化/按授权错误契约验证；无事件发布（`internal/cli` + `docs/api.md`）

- **T8 `TestRenderError_EnvelopeMatrix`：** 对 `docs/api.md` 信封做解析矩阵——合法信封（含 `request_id` 渲染）/ 空 body / 非 JSON / `{"error":{}}` 缺字段 / 5xx 信封；每种断言渲染文本与降级行为（FR-1 规则 1-3）。
- **T9（契约文档）：** 本规格 §3 FR-3 + `docs/api.md` 403 行（message 携带拒绝原因）——文档即验收物；T7 的 403 断言包含 `code == "AccessDenied"`。
- **T10（无事件发布）：** T6 同一测试内，删除路径拒绝后 `repo.ListOutbox`（或 `HasEventOutboxFact`）零行 + 事件总线零广播（`events.New(repo, logger)` 无订阅者计数）；`file_delete.go:157` 顺序由 T6 的 ErrForbidden 早退证明（拒绝返回先于任何写路径）。

### AC-4 组合 e2e：无授予 → fail-closed 拒绝 + 原因 + 对象可 GET；授予 → 删除成功 + `vault.file.deleted@1.1` 投递；运行中撤销 → 再拒且服务器健康（`internal/integration/authz_cli_failclosed_test.go`，新文件）

harness 复用 `authz_parity_test.go:93` 模式（repo+store+`svc.WithAuthorizer(manager)`+chi 路由+`principalMW`），CLI 侧以 `t.Setenv("AERO_ENDPOINT"/"AERO_API_KEY"/"AERO_TENANT")` + `cli.NewClient()`（或进程内 `cli.Run`）驱动真实 `cmdRemove` 共享路径：

1. **无授予：** 对象 PUT（admin principal）后，无 `vault.file.delete` 授予的 operator 键执行删除 → 退出码 1、stderr 含拒绝原因（当前 Manager 语义下为 `default_deny`/`explicit_deny` 等 `Decision.Reason` 原文）、`GET` 仍 200（状态未动）、`outboxCountFor == 0`。
2. **授予：** `manager.PutACL(... ActionDelete, EffectAllow ...)`（core 方向 FR-1/FR-2 落地后即 `vault.file.delete` 授予；落地前 ACL 授予是 parity 先例的等价机制）→ 同一删除 → 退出码 0、`outboxCountFor(deleted@1.1) == 1`。
3. **运行中撤销：** 撤销授予（`manager.DeleteACL` 或等价）→ 重传对象后再删 → 退出码 1、原因再次出现、对象可 GET、服务器健康（随后任一请求 200——"remove the permission mid-run → denied while server stays healthy"）。
4. **`cli admin files delete` 表面：** 方向文指名该命令；其落地（方向 1）后同一测试以 `admin files delete` 驱动同一共享路径（断言不变量与 1-3 相同）。本方向不实现该命令，测试文件在命令不存在时以 `rm` 执行等价断言并注明依赖。

---

## 5. 范围边界（明确不做）

| 不做 | 归属 |
|------|------|
| `admin files delete` 命令本体 / admin 文件资源 | 方向 1（`internal/cli/cli_admin.go` 资源 switch 扩展） |
| auth 词汇 `vault.file.delete`（Scope/AUTH_KEYS 语法） | core 方向 FR-1（`internal/auth/auth.go:31-33,139-140`） |
| `access.Manager` 对 `ActionDelete` 的 fail-closed + 细粒度授予 | core 方向 FR-2（`internal/access/authorizer.go`） |
| FileService nil-authorizer ⇒ 拒绝（默认部署删除 403） | core 方向 FR-3——**本文不改变** `access.go:91-94` 基线 |
| REST admin 删除端点 `/v1/admin/files/...` | core 方向（`fail-closed-vault-file-delete-v1.design.md`） |
| S3 边界拒绝原因透出 | **保持 R2**（`s3compat/authz.go:26` Debug-only，AWS 兼容）——本文契约仅 REST 信封 |
| provider error 映射 403（现为 500） | core 方向（s3compat 规格 E7 已述机制缺口） |
| UI/SDK/WebDAV 的失败表面 | 其他方向 |
| 新 CLI 命令、退出码体系重构（2/1/0 语义） | 维持既有（FR-4 只固化） |

**伴随改动（本文交付的一部分，最小集）：** `handler_helpers.go:56` 消息携带 reason（1 行 + T7 断言）；`docs/api.md` 403 行说明；`internal/cli` FR-1/FR-2 重构。

## 6. 依赖与顺序

1. **FR-3 服务端契约**可与 CLI 侧独立落地（各自单测先行；组合测试需两侧齐备）。
2. **AC-4 组合测试**依赖：服务端 fail-closed 门（core 方向，或 parity harness 的 `manager` 直挂——**当前代码即可运行**，见 E11）；`vault.file.delete` 授予词汇（core FR-1，落地后替换 ACL 授予机制）；`admin files delete`（方向 1，落地后替换 `rm` 驱动）。
3. **顺序建议：** FR-1/FR-2（CLI 共享路径 + 单测）→ FR-3（classify 1 行 + api.md）→ T6/T7（零副作用钉）→ AC-4 组合测试（harness 复用 parity 文件，零新基建）。

## 7. 实现指引（供验收后落地，非本规格交付物）

- `internal/cli/response.go`：`apiErrorBody` 结构（json tag 对齐 `dto.go`）+ `renderError`（~60 行）；`readSuccessfulResponse` 失败分支委托。
- `internal/cli/cli_crud.go`：`cmdRemove` :103-106 替换为 `renderError` 调用。
- `internal/cli/cli_admin.go`：5 处 ad-hoc 读取（:355/:375/:400/:428/:449，含 `cmdDeleteBucket` :449）统一；净增 0 行级别，保持 ≤500 行门禁。
- `internal/api/rest/handler_helpers.go:56`：`"access denied"` → `err.Error()`。
- 测试：T1-T8 进 `internal/cli/cli_test.go`（stdlib-only）；T6 进 `internal/service/file_delete_test.go`；T7 进 `internal/api/rest`；T9 为 `docs/api.md` 编辑；T10 并入 T6；AC-4 新文件 `internal/integration/authz_cli_failclosed_test.go`（`//go:build integration` 外，随 `make test` 常规路径，parity 先例同款）。
