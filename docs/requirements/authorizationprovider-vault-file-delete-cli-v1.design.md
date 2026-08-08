# 设计：fail-closed 权限的 CLI 表面 —— 拒绝原因透出（读 DELETE 错误体、渲染 403 原因、非零退出）

> **配套规格：** `docs/requirements/authorizationprovider-vault-file-delete-cli-v1.md`（FR-1…FR-5 / AC-1…AC-4）· **模块：** `internal/cli`（主）+ `internal/api/rest`（1 行伴随）+ `docs/api.md` · **状态：** 设计（未实现）· **基线：** HEAD `acfaaf4` + WIP 工作树（不触及 `internal/cli` / `internal/access` / `internal/service/access.go`）
> **门禁：** `make check` 全绿 · 单文件 ≤ 500 行（`cli_admin.go` 现 455 行）· 纯 stdlib（I6）· 无新 `go.mod` 依赖 · I1/I2 纪律 · **无新 HTTP 端点 / 无 OpenAPI / 无 schema 迁移**（本设计零 DB 变更）

---

## 1. 证据复核（规格全部主张独立复验；本次实测 build/vet 全绿）

| # | 规格主张 | 复核结论 |
|---|---------|---------|
| E1 | `cmdRemove`（`cli_crud.go:93-107`）：`:103` `resp.Body.Close()` 无 `io.ReadAll`，`:105` 裸 `HTTP %d` | ✅ **精确**。93-107 行逐字命中；`cmdGet`/`cmdList`/`cmdUpload` 均读 body（:62/:84/:41），唯独删除路径销毁证据 |
| E2 | `response.go:10-20` `readSuccessfulResponse` 读 body 但从不解析信封；16 调用点 | ⚠️ **主体成立，计数修正：14 个调用点** + 1 处定义（`cli_admin.go` 9 处 + `cli_admin_buckets.go` 5 处 = 14，非 16）；"只原样打印、不解析信封"✅ |
| E2b | `cli_admin.go` 5 处 ad-hoc `io.ReadAll`（:354/:374/:399/:427/:447） | ⚠️ **行号漂移 +1**：实际 :355/:375/:400/:428/:449（同一 5 处） |
| E3 | `authorizer.go:10`（Authorizer 接口）/ `:65`（`denied(reason)`）/ `:169`（`authorizeOrDenied`） | ✅ 精确。原因集合 `missing_principal`/`tenant_mismatch`/`explicit_deny`/`resource_acl_no_match`/`default_deny` 等（:27-62）；`authorizeOrDenied` :169-177 将 reason 装入错误文本 |
| E4 | `types.go:76` `ActionDelete="object:delete"`；`vault.file.delete` 无常量 | ✅ 精确 :76；全库 grep 仅命中注释（`s3compat/authz.go:10`、`policy.go:67`）与事件类型 `vault.file.deleted@1.1`（`event_outbox.go:22`，过去式，非权限动作） |
| E5 | `file.go:98` `WithAuthorizer` | ✅ 精确 :96-100；装配 `main.go:94,215`；nil authorizer ⇒ 基线 fail-open 不变（`cmd/server/access.go:11-19`，E15） |
| E6 | `cli_test.go:126` `TestDo_SetsAuthAndTenantHeaders` | ✅ 精确 :126-148；`newTestClient` :66-74 以 `AERO_*` 三 env 注入 `test-key`/`acme` |
| E7 | `handler_helpers.go:55-56` `classify` 硬编码 `"access denied"`；`service/access.go:100` 已包 reason | ✅ 双点精确。`access.go:100` `fmt.Errorf("%w: %s", ErrForbidden, decision.Reason)` ⇒ reason 在错误链中、被 classify 剥掉；**无任何 REST 测试断言旧消息文本**（本次 grep 零命中）⇒ 1 行改动无测试破坏面 |
| E8 | `file_delete.go:157` 授权先于删除事务/outbox 事实 | ⚠️ **行号漂移 -2**：`authorizeObject(ctx, access.ActionDelete, obj)` 实际在 **:159**（:155-158 为 GetObject + 错误分支）；顺序主张成立——授权在 `softDeleteObject`(:76)/`hardDeleteObject`(:18，内嵌 `deleteFacts` :123-146 写 outbox + `deleteAuditEntry` :100) 之前 |
| E9 | `authz_parity_test.go:93` `TestCompositionRevokeRestoreParity` 组合 harness | ✅ 精确 :93-；含 `principalMW`（:28-45，X-Test-Principal 注入，对齐 `auth_middleware.go:183`）、`outboxCountFor`（:51-63）、`outboxPayloadFor`（:65-87）；REST 层经 `WithAccessManager(manager, …)`（handler.go:32；`enterprise_access_test.go:63` 同款先例） |
| E10 | 信封形状 `{"error":{"code","message","request_id"}}` | ✅ `dto.go:10-18`；`docs/api.md:53` 文档化；`writeError`（handler_helpers.go:19-25）输出 |
| E11 | `HasEventOutboxFact` / `EventTypeFileDeleted11` | ✅ `event_outbox.go:437` / `:22`；service 测试先例 `file_delete_test.go:87` |
| E12 | 构建/vet 绿 | ✅ 本次实测 `go build ./...` 与 `go vet ./internal/cli ./internal/service ./internal/access ./internal/api/rest` 退出码 0 |

**复核结论：规格全部主张成立；仅 3 处行号/计数微漂移（E2/E2b/E8），不影响任何设计决策。** 规格为增量规格：两个独立证据缺口（服务端 classify 剥 reason + CLI 丢弃 body）与"拒绝先于事件发射"的既有顺序（E8）均被锚定为可测试契约。

---

## 2. 设计总览

```mermaid
flowchart LR
    subgraph CLI["internal/cli（主交付）"]
        R["renderError(resp) 新函数<br/>response.go"]
        RS["readSuccessfulResponse<br/>失败分支委托 R"]
        RM["cmdRemove :103-106 迁移"]
        DB["cmdDeleteBucket 迁移"]
        AD["cli_admin.go 5 处 ad-hoc 统一"]
        R --> RS & RM & DB & AD
    end
    subgraph SVC["internal/service（不改）"]
        A1["Delete :147 → authorizeObject(ActionDelete) :159"]
        A1 -->|denied| E1["access.go:100<br/>ErrForbidden + reason"]
    end
    subgraph REST["internal/api/rest（1 行伴随）"]
        C["classify :56<br/>'access denied' → err.Error()"]
    end
    E1 --> C -->|"403 {error:{code,message,request_id}}"| R
    R -->|"stderr 单行 + exit 1"| OP["操作员可诊断"]
    A1 -->|allowed| D["softDelete/hardDelete<br/>（outbox 事实在此之后）"]
    T["AC-4 组合测试（parity harness + 测试局部 bearerPrincipalMW）"] -. 驱动 cli.Run .-> RM
```

**核心不变量：** ① 任何 ≥300 响应经**三层渲染**（规则见 §3.1）：信封 → `HTTP <status> <code>: <message>`（code 为空时省略 `<code> ` 段）；非信封非空 body → `HTTP <status>: <raw>`（raw **折叠为单行，渲染行总长 ≤ 512 字节**）；空 body → `HTTP <status>`——body 非空时绝不输出裸 `HTTP <status>`；② body 消费是 helper 的硬契约（调用方不再读）；③ 拒绝请求零 outbox/事件/审计副作用（既有顺序 :159 由测试钉死）。

---

## 3. API 变更

### 3.1 CLI 内部 API（唯一新增面）

`internal/cli/response.go` 新增：

```go
// apiErrorBody 与 rest/dto.go:10-18 信封对齐（docs/api.md:53）。
type apiErrorBody struct {
    Error struct {
        Code      string `json:"code"`
        Message   string `json:"message"`
        RequestID string `json:"request_id"`
    } `json:"error"`
}

// renderError 消费 resp.Body（调用方不得再读），返回单行操作员可读错误文本。
// 渲染规则（按序）：
//   1. body 可解析为信封且 code/message 至少一项非空 → "HTTP <s> <code>: <message>"
//      （code 为空时省略 "<code> " 段 → "HTTP <s>: <message>"，杜绝双空格；
//        request_id 非空时追加 " (request <id>)"）
//   2. body 非空但不可解析（代理 HTML/纯文本）→ "HTTP <s>: <raw>"
//      （raw 有界：连续 \r\n/\n/\t/空白折叠为单空格并去首尾空白；
//        若前缀 + raw 超 512 字节，raw 截为 (512 − len(前缀) − 3) 字节并追加 "…"——
//        渲染行总长 ≤ 512，杜绝多 KB 原始 dump；真实 reason/状态文本远短于此，正常不触发）
//   3. body 为空 → "HTTP <s>"
// message 视为不透明文本原样渲染（不解析 "forbidden: " 前缀）。
// 永不 panic：解析失败降级规则 2；读 body 失败降级规则 3。
func renderError(resp *http.Response) string
```

- `readSuccessfulResponse`（response.go:16-18）失败分支改为 `fmt.Fprintln(os.Stderr, renderError(resp)); return nil, false`——**签名不变**，14 个调用点零改动、成功路径逐字节不变。
- `cmdRemove`（cli_crud.go:103-106）：`resp.Body.Close()` → `defer resp.Body.Close()`；`if resp.StatusCode >= 300 { fmt.Fprintln(os.Stderr, renderError(resp)); return 1 }`；204 成功路径不变（return 0）。
- `cli_admin.go` 5 处 ad-hoc `io.ReadAll`（:355/:375/:400/:428/:449，其中 **:449 即 `cmdDeleteBucket`（cli_admin.go:437）的 body 读**——全库仅此 5 处，无 cli_crud.go 同型点）统一走 `renderError`——纯重构：2xx 输出逐字节不变；≥300 输出从 raw JSON 变为信封渲染（有意改善）。

### 3.2 REST 伴随变更（1 行 + 文档）

```go
// internal/api/rest/handler_helpers.go:55-56
case errors.Is(err, service.ErrForbidden):
    return "AccessDenied", err.Error(), http.StatusForbidden   // 原: "access denied"
```

`err.Error()` 即 `"forbidden: <Decision.Reason>"`（access.go:100 已在链中）⇒ `message` 携带拒绝原因原文。影响面为**所有** REST `ErrForbidden` 响应（get/put/delete/stat 等授权拒绝路径）——一致且期望；`docs/api.md` 403 行补充"授权拒绝时 message 携带拒绝原因"。**S3 边界不动**（`s3compat/authz.go:26` R2 Debug-only，AWS 兼容）。

### 3.3 显式无变更清单

| 面 | 状态 |
|----|------|
| 新 CLI 命令 / 退出码体系（0/1/2） | 无（FR-4 仅固化既有映射） |
| 新 HTTP 端点 / OpenAPI / `docs/api.md` 表结构 | 无（仅 403 行说明文字） |
| DB schema / 迁移文件（I2） | 无 |
| `readSuccessfulResponse` 签名 / 成功路径输出 | 不变 |
| `internal/service` / `internal/access` 生产码 | 零改动（FR-5 只钉测试） |
| S3 / WebDAV / MCP / UI / SDK 失败表面 | 不动 |

---

## 4. 兼容性约束

1. **成功路径字节级不变：** `cmdRemove` 204 → exit 0 无输出（T5 钉）；`readSuccessfulResponse` 2xx 分支、admin 5 处（含 `cmdDeleteBucket`）2xx 输出逐字节不变——既有 CLI 脚本（管道/退出码断言）不破坏，**T12（`bucket-rm` 204 → stdout == `bucket deleted\n` + exit 0）/ T13（一处 admin 2xx body 逐字节）显式钉住**（F-D：既有测试零命中成功字节，保证从“假定”变“钉死”）。
2. **向后兼容的失败路径渐变：** ≥300 输出从 `HTTP <status>: <raw JSON>`（admin 路径）或裸 `HTTP <status>`（rm 路径）变为 `HTTP <status> <code>: <message>`。老服务器 + 新 CLI：body 无信封时降级规则 2 输出 `HTTP <s>: <raw>`（**有界不吞证据**：折叠单行、渲染行总长 ≤ 512 字节，真实 reason/状态文本远短于上限，正常不触发截断）；新服务器 + 旧 CLI：admin 路径仍打印 raw JSON（信息更多但不解析），rm 路径仍裸 403（回归前现状）——**双向滚动兼容**，无协调部署要求。
3. **协议契约仅在 REST 信封：** S3 403 仍为 `AccessDenied` XML 无 reason（R2）；CLI 不解析 message 前缀，服务端 reason 措辞演进不破坏 CLI。
4. **门禁合规：** `response.go` 增 ~60 行（27 → ~90）；`cli_admin.go` 净增 0 行级别（455 → ≤460）；测试全 stdlib（I6）；单函数 ≤ 50 行（`renderError` 拆 解析/渲染两个 ≤25 行 helper 若超限）。

---

## 5. 失败模式

| # | 场景 | 行为 | 验证 |
|---|------|------|------|
| F1 | body 非 JSON（代理 HTML、纯文本 `Forbidden`） | 降级规则 2：`HTTP <s>: <raw>`（**折叠单行，渲染行总长 ≤ 512 字节**）；不 panic | T4/T8 |
| F2 | body 空 / 读 body 出错 | 降级规则 3：`HTTP <s>`；不 panic | T8（**含恒错 ReadCloser 用例**，F-E） |
| F3 | JSON 但缺 `error` 字段 / 字段空 | 降级规则 2（无 code/message 可渲染） | T8 |
| F4 | 连接错误（c.do 失败） | 既有路径：stderr 打印 err + exit 1（`renderError` 不介入，body 不存在） | **T11（新钉）** |
| F5 | `request_id` 为空 | 省略 `(request <id>)` 后缀 | T8 |
| F6 | 服务端未落地 FR-3（旧 classify） | CLI 渲染 `HTTP 403 AccessDenied: access denied`——无回归，仅少信息；双向兼容 | T1 矩阵变体 |
| F7 | `renderError` 被调用两次（调用方仍读 body） | helper 消费 body 后第二次读返回空 → 降级规则 3；测试 T2 钉"只读一次且读全" | T2 |
| F8 | 拒绝后 outbox 行/事件泄漏（未来回归：门禁后移） | T6/T7/T10 显式断言零行 + 零广播，CI 拦截 | T6/T7/T10 |

---

## 6. 迁移步骤（每步独立可回滚）

| 步 | 动作 | 产出/验证 | 回滚 |
|----|------|----------|------|
| S1 | `response.go`：`apiErrorBody` + `renderError` + `readSuccessfulResponse` 失败分支委托 | T8 信封矩阵绿；14 调用点行为不变（既有测试全绿） | 删函数、恢复 :16-18 原文 |
| S2 | `cli_crud.go`：`cmdRemove` 迁移（defer Close + renderError） | T1/T2/T3/T4/T5/T11 绿 | 恢复 :103-106 原文 |
| S3 | `cli_admin.go` 5 处统一（:355/:375/:400/:428/:449，含 `cmdDeleteBucket` :437/:449） | 既有 admin 测试全绿（成功路径逐字节）；T12/T13 钉 2xx 字节；`make check` 绿 | 逐处还原 |
| S4 | `handler_helpers.go:56` 1 行（`err.Error()`）+ `docs/api.md` 403 行说明 | T7 绿（REST 级 403 + outbox 零行）；REST 既有测试全绿 | 1 行还原 |
| S5 | T6（service 级零副作用钉）进 `file_delete_test.go` | `go test ./internal/service/` 绿 | 删测试 |
| S6 | AC-4 组合 e2e：新文件 `internal/integration/authz_cli_failclosed_test.go`——**测试局部 `bearerPrincipalMW`**（映射 `Authorization: Bearer <key>` → `Principal{SubjectID:"alice", TenantID:"default", Kind:PrincipalUser, 无 scopes}`，key=`alice`；parity 的 `principalMW` 只认 `X-Test-Principal`，CLI 的 `do()` 不发该头，**不可复用**）+ **AERO_TENANT 不设（缺省 `default`；`TenantFrom` 回退）** + `AERO_API_KEY=alice` + 驱动**导出 `cli.Run([]string{"rm", key})`**（`cmdRemove` 未导出）+ **复制 `captureStderr`**（`cli_test.go:47-62` 同款；`internal/integration` 无此 helper） | 随 `make test` 常规路径（parity 先例同款，非 `//go:build integration`） | 删文件 |

**顺序依赖：** S1→S2→S3 纯 CLI 侧、可独立先行；S4 与 S1-S3 互不依赖（各自单测先行）；S5/S6 需 S2/S4 齐备（S6 用 parity harness 的 `manager` 直挂即可运行，不依赖 core 方向落地，见规格 §6）。**零 DB 迁移**（I2 不触及）。

---

## 7. 可测试验收映射

| 验收 | 测试 | 文件 | 确定性断言 |
|------|------|------|-----------|
| AC-1 单元 | **T1** `TestCmdRemove_403Denial_PrintsReasonExits1` | `cli_test.go` | stub 403 + 信封（message=`permission vault.file.delete denied for principal alice`）→ exit 1；`captureStderr` 含该原因文本；**不含**裸 `HTTP 403\n` |
| AC-1 | **T2** `TestCmdRemove_403Denial_BodyConsumed` | `cli_test.go` | 计数 reader 包 body → helper 返回后已读字节 == 全长；stderr 含原因（原因只存在于 body ⇒ 读过即证明） |
| AC-1 | **T3** `TestCmdRemove_403Denial_TenantHeaderAsserted` | `cli_test.go` | stub 断言 `X-Aero-Tenant == "acme"`、`Authorization == "Bearer test-key"`（`newTestClient` 注入；扩展 :126 模式到 DELETE） |
| AC-1 | **T4** `TestCmdRemove_403Denial_NonJSONBodyDegrades` | `cli_test.go` | 403 + 纯文本（**600+ 字节、含换行**）→ exit 1、stderr **单行**以 `HTTP 403: ` 开头、**整行 == 512 字节且以 `…` 结尾（截断钉，F-A）**、无 panic（F1） |
| AC-1 | **T5** `TestCmdRemove_204_StillExit0` | `cli_test.go` | 既有 `TestCmdRemove_Success_Returns0`（:443）保持——迁移不改成功路径 |
| AC-1 | **T11** `TestCmdRemove_ConnectionError_PrintsErrExits1` | `cli_test.go` | `t.Setenv("AERO_ENDPOINT", 已 Close() 的 httptest.Server URL)`（loopback 连接拒绝；确定性、零网络、无固定端口）+ `Run([]string{"rm", key})`（包内直接调导出入口）→ exit 1 + stderr 含连接错误文本（**F4 钉**：`c.do` 错误分支；`renderError` 不介入） |
| AC-2 | **T6** `TestDeleteDenied_NoOutboxRow_ObjectUntouched` | `file_delete_test.go` | 拒绝型 stub authorizer（`Decision{Allowed:false, Reason:"default_deny"}`）→ `errors.Is(err, service.ErrForbidden)`；`HasEventOutboxFact(originID, EventTypeFileDeleted11) == false`；`GetObject` 仍成功；审计零行 |
| AC-2 | **T7** `TestRESTDeleteDenied_403_NoOutbox` | `api/rest`（`enterprise_access_test.go:63` 同款 harness） | `WithAccessManager(manager, …)` 拒绝场景 → DELETE 403 + `code=="AccessDenied"` + `outboxCountFor == 0`（HTTP 边界钉） |
| AC-3 | **T8** `TestRenderError_EnvelopeMatrix` | `cli_test.go` | 合法信封（含 request_id 渲染）/ 空 body / **读 body 失败（恒错 ReadCloser，F2/F-E 钉）** / 非 JSON（**600+ 字节多行 → 单行、整行 ≤ 512 且以 `…` 结尾，F1 截断钉**）/ `{"error":{}}` 缺字段 / **信封 code 空 message 非空 → `HTTP <s>: <message>` 无双空格（F-F 钉）** / 5xx 信封——逐例断言渲染文本与降级（F1/F2/F3/F5） |
| AC-3 | **T12** `TestCmdDeleteBucket_204_SuccessBytes` | `cli_admin_test.go` | stub 204 → exit 0 + **stdout == `bucket deleted\n`**（F-D：`cmdDeleteBucket` 2xx 字节钉；实测 `cli_admin.go:453`） |
| AC-3 | **T13** `TestCmdAdminKeys_List_2xx_SuccessBytes` | `cli_admin_test.go` | stub 200 + JSON body → exit 0 + **stdout 逐字节 == body**（F-D：`readSuccessfulResponse` 2xx 分支未迁移的字节钉） |
| AC-3 | **T9** 契约文档 | `docs/api.md` + 本规格 §3.2 | 403 行说明 message 携带拒绝原因；文档即验收物（review 检查项） |
| AC-3 | **T10** 无事件发布 | 并入 T6 | 拒绝后 `HasEventOutboxFact` 零行 + `events.New(repo, logger)` 零订阅者计数；`:159` 早退由 ErrForbidden 返回证明（拒绝先于任何写路径） |
| AC-4 | **AC-4 组合 e2e**（3 场景 + `admin files delete` 依赖注记） | `internal/integration/authz_cli_failclosed_test.go`（新） | ①无授予：`t.Setenv`（`AERO_ENDPOINT=ts.URL`、`AERO_API_KEY=alice`、**AERO_TENANT 不设**）+ 复制 `captureStderr` + 驱动**导出 `cli.Run([]string{"rm", key})`**（`cmdRemove` 未导出；经 `bearerPrincipalMW` 映射 Bearer alice → alice 主体验证）→ exit 1 + stderr 含**真实 reason（`forbidden: default_deny`，T1 的 stub message 为伪造示例，AC-4 断言真实词表）** + GET 200 + `outboxCountFor==0`；②`manager.PutACL(...ActionDelete, EffectAllow...)` → exit 0 + `outboxCountFor==1` + `outboxPayloadFor` 校验 `deleted@1.1` 信封；③`manager.DeleteACL` 撤销 → 重传再删 → exit 1 + 原因 + GET 200 + 服务器健康（随后任一请求 200）；④`admin files delete` 落地后以同断言驱动（命令不存在时以 `rm` 执行并注明依赖，方向 1） |

**验收→测试覆盖闭合性：** AC-1×6（T1–T5、T11）、AC-2×2（T6/T7）、AC-3×5（T8/T9/T10/T12/T13）、AC-4×1（复合 3 场景）——规格四组验收全映射；F1-F8 失败模式每项至少一个测试钉住（**F4=T11 新钉**——原“既有测试”声称经全量 grep 证伪，现以已关闭的 httptest server 钉住连接错误分支；F6 归入 T1 变体；F7 归入 T2）。

---

## 8. 范围边界（规格 §5 复核，无新增/收缩）

| 不做 | 归属 |
|------|------|
| `admin files delete` 命令本体 / admin 文件资源 | 方向 1（`internal/cli/cli_admin.go` 资源 switch 扩展） |
| auth 词汇 `vault.file.delete`（Scope/AUTH_KEYS 语法） | core 方向 FR-1（`internal/auth/auth.go:31-33,139-140`） |
| `access.Manager` 对 `ActionDelete` 的 fail-closed + 细粒度授予 | core 方向 FR-2（`internal/access/authorizer.go`） |
| FileService nil-authorizer ⇒ 拒绝（默认部署删除 403） | core 方向 FR-3——本设计**不改变** `access.go:91-94` fail-open 基线 |
| REST admin 删除端点 `/v1/admin/files/...` | core 方向（`authorizationprovider-vault-file-delete-core-v1.design.md`） |
| S3 边界拒绝原因透出 | **保持 R2**（AWS 兼容）；本设计契约仅 REST 信封 |
| provider error 映射 403（现 500） | core 方向 |
| `cmdGet`/`cmdList`/`cmdUpload`/`cmdSearch`/`cmdTag`/`cmdVersions`/`cmdLineage` ≥300 原始输出保持现状 | 本次范围仅删除路径（`rm`/admin/bucket-rm）；删除路径统一后为全 CLI 统一渲染留钩子 |
| UI/SDK/WebDAV 失败表面 | 其他方向 |

**伴随改动（本设计交付）：** `handler_helpers.go:56` 1 行 + `docs/api.md` 403 行说明 + `internal/cli` FR-1/FR-2 重构 + 测试 T1-T13 + AC-4 组合文件 + **清理 `cli_test.go:1419-1428` 过时 BUG 注释**（cmdList/cmdTag/cmdVersions/cmdLineage/cmdSearch“从不检查 HTTP 状态”——实测均已检查：cli_crud.go:84-89、cli_search.go:33-36/:64/:84/:104；仅 cmdSnapshot 注释仍有效，保留）。
