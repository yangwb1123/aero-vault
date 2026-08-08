# 设计：REST/core 删除门禁 fail-closed（service 边界翻转）+ AuditSink 契约 pin

> **配套规格：** `docs/requirements/authorizationprovider-auditsink-delete-v1.md` · **模块：** `internal/billing`（组合面：`internal/access` + `internal/service` + `internal/api/rest` + `internal/integration` + `cmd/server`） · **状态：** 设计已定稿，未实现 · **日期：** 2026-08-06
> 本文是增量规格的落地设计：证据复核结论（含设计期新发现 D1–D5）、API 变更、设计决策、兼容性约束、失败模式、迁移步骤、验收映射、最终代码形态。
> **与兄弟轮的关系：** S3 边界端口已落地（`internal/api/s3compat/authz.go`，上轮 campaign）；AuditSink 端口 + L2 适配器 + 删除事务性 outbox + @1.1 payload 已落地（上轮 campaign）。本设计的**唯一未落地变更**是规格 FR-1b/1c/1e（REST/core 门禁翻转），其余 FR 为契约 pin；随附落地 D9/D10 契约 pin（governance 重复 re-POST/conflict 终态 + L2 echo-on-re-POST + 7d SLA 告警，见 §2/§8）。与已 gate 的 `authorizationprovider-vault-file-delete-core-v1.design.md` 共享同一决策源（`*access.Manager`）与同一漏斗——**必须不破坏已合入的 `internal/integration/authz_parity_test.go`（AC-3 验收）**，这是本设计的第一约束。

---

## 0. 证据复核结论（对增量规格逐条核验 + 设计期新发现）

### 0.1 逐条复核（对照工作树 HEAD `acfaaf4` + 已落地 WIP）

规格 E1–E9 全部与工作树一致；E8 的修正成立。`go build ./...` 退出码 0；`go test` 六个相关包（access/service/events/s3compat/repository/billing + integration）全绿。

| # | 规格引用 | 复核 | 结论 |
|---|---------|------|------|
| E1 | `authorizer.go:11-13,:20-22` 接口 + disabled→`Allowed:true` | 实际 :11-13 接口；:20-21 `access_control_disabled` 早退（:22 为 `}`） | ✅ |
| E2 | `file.go:96-99` `WithAuthorizer` nil-baseline 注释 | 实际 :96-99 原文 *"A nil authorizer preserves the CI/MVP baseline"* | ✅ |
| E3 | `file_delete.go` `Delete`/`DeleteVersion` → `authorizeObject(ActionDelete)` | 实际 `Delete` :147（门禁 :159）、`DeleteVersion` :174（门禁 :179） | ✅（漂移 +51 成立） |
| E4 | `auditgovernance/http.go` `Publisher` + `wait_for=ledgered`；`model.go:19` | 实际 `newPublisher` :34-57，JoinPath+`wait_for=ledgered` :37-40；`governancePath="api/v1/events"` :19 | ✅ |
| E5 | 两个 `token.go` 导入 `ssoclient` | 实际 auditgovernance/token.go:15-16、billing/token.go:10-11 均导入 `yangwb1123/snaplink/interfaces/ssoclient`(+remote) | ✅ |
| E6 | `client.go` `'snaplink billing'` + UA | 实际 :83/:102/:113/:123/:137 错误串；UA `aero-vault/billing` :128 | ✅ |
| E7 | `runtime.go` `CheckQuota`→`ErrEntitlementUnavailable` + `Ready()` | 实际 `Ready` :136-145（:141）；`CheckQuota` :147-172（:152/:156/:159） | ✅ |
| E8 | `InsertAudit` L0 sink | **修正成立**：全仓 grep 零命中（exit 1）；实际 L0 = `RecordAudit` :33-42 + 事务内 `insertAuditEntry` :15-23；`AuditActionFileDelete="file.delete"` :9-11 | ✅ 修正 |
| E9 | `admin.go:417-433` `audit()` swallow | 实际 `auditForTenant` :417-433，`_ = h.repo.RecordAudit(...)` 吞错 | ✅ |

**已落地断言复核（规格 §2.1/§2.3）：** S3 端口（`s3compat/authz.go`：nil⇒deny、error⇒deny、`authorizeDelete`；装配 handler.go:23-30/router.go:14）✅；7 个门禁测试 :174/:209/:237/:272/:324/:420/:478 ✅；AuditSink 端口（`events/audit_sink.go` `ErrSinkNotBound`/`ErrSinkUnauthorized`）+ L2 适配器（`audit_sink_l2.go` `validateAuditSinkL2Endpoint` :81、回显 commit :153-157）+ `AUDIT_SINK_L2_*` 配置（workers.go:166-175 装配）✅；outbox（`event_outbox.go` `HardDeleteObjectWithEvent` :102/`SoftDeleteObjectWithEvent` :147、`HasEventOutboxFact` :437）+ relay（:144/:181/:229/:272/:432/:609 测试）✅；payload `schema_version:"1.1"` ✅；迁移 0039 `UNIQUE(origin_kind,origin_id)` / 0041 无 UNIQUE ✅；零 sibling 编译级 grep 零命中 ✅；REST 删除路由 router.go:243 + `deleteKey` :445 + `ErrForbidden→403`（handler_helpers.go:55）✅；admin 组 router.go:187-203 无文件删除 ✅；`ACCESS_CONTROL_ENABLED` 默认 false（config.go:216）、`buildAccessManager` disabled→nil（cmd/server/access.go:11-19）✅；全仓无 `FAIL_CLOSED`/`DeleteGate` 配置键 ✅。

### 0.2 设计期新发现（D1–D5）

| # | 发现 | 处理 |
|---|------|------|
| **D1（规格引用路径错误）** | 规格 AC-3 引 `internal/api/s3compat/authz_parity_test.go:180`——该文件**不存在**；parity 测试实际位于 `internal/integration/authz_parity_test.go`（`TestCompositionRevokeRestoreParity` :93，`PutACL(ActionDelete, EffectAllow)` grant :177-180，S3 204/REST 403 parity 断言 :192-241）。测试存在且行为与断言一致，仅路径误引 | §2 D8 |
| **D2（规格欠定——PrincipalSystem 豁免位置）** | 规格 FR-1b 称"`PrincipalSystem` 豁免（AV quarantine 等内部路径，`internal/service/object_worker.go`）"，但 service 层 `authorize()` **今天无 system 早退**——豁免只在 `access.Manager.Authorize`（:23-25 `trusted_system`）与 `requireActiveTenant`（access.go:109）存在。AV 路径注入 System principal（antivirus/worker.go:177-180；cmd/server/workers.go:33 `access.SystemContext`）后经 `authorizeObject(ActionDelete)`（object_worker.go:58）走 `authorize()`：**若 system 早退不先于 nil 检查，默认配置（nil authorizer）下 AV quarantine 直接 403**。规格只说了"保留豁免"未说位置/顺序 | §2 D2：豁免必须插在 `authorize()` 的 nil 检查**之前** |
| **D3（漏斗面补全）** | `ActionDelete` 漏斗共 **7 个调用点**（原文计 6 处系计数口径误差，安全复核计 7 处）：`file_delete.go:159/179`（Delete/DeleteVersion）、`object_worker.go:58`（Quarantine）、`delete_marker.go:34/37`（DeleteMarker，:37 走 `authorizePath`）、`file_bucket_settings.go:40/48`（DeleteBucket，bucket 级 + 逐对象）。规格 FR-1b 只点名前两类。**好消息：全部经 `authorize()` 单点**，翻转一处即覆盖全漏斗（含 bucket 删除） | §2 D3 |
| **D4（行号漂移）** | 规格引 `fullserver_test.go:764 起` 的 `TestDeleteResponse_DoesNotBlockOnDelivery` 实际定义于 :685（:764 是 `assertAuditRowFor` 断言行）；harness `startFullServerWithRelay` 实际 :55（规格引 :73-112） | 无行为影响 |
| **D5（读路径 nil 检查混淆）** | `service/access.go` 的 `s.authorizer == nil` 共 3 处：:66（`filterAuthorizedVersions` 读过滤）、:91（`authorize` 门禁）、:206（list 取数）。**只有 :91 是门禁**；:66/:206 是读路径，I5 下必须保持 nil⇒allow，翻转不得触碰 | §2 D3 |

### 0.3 错误映射复核（修订轮，对工作树 + x/net/webdav v0.55.0 源码逐行验证）

| # | 复核项 | 结论 |
|---|--------|------|
| **E10（a）** | WebDAV DELETE：`ErrForbidden→fs.ErrPermission` 是否让 v0.55.0 `handleDelete` 返回 403 | **否，该映射单独不足以得到 403。** `handleDelete`（`webdav.go:242-269`）对 `Stat` 错误仅区分 `os.IsNotExist`（404）/其余（405），对 `RemoveAll` 的**任意**错误无条件返回 405——全包 grep `ErrPermission` 仅命中 `file.go:282/286`（目录遍历）与 `webdav.go:665`（`handlePropfindError`，PROPFIND），**DELETE 路径无任何权限分支**。现有 `davFS.RemoveAll`（dav.go:141-146）原样透出 `ErrForbidden` ⇒ 翻转后 DELETE 呈 405。`fs.ErrPermission` 映射必须搭配适配器层 405→403 改写（§7）；这是本设计唯一需要适配器改动的协议。**对照：** MOVE 的 `moveFiles`（`file.go:655-658`）对任意 `Rename` 错误返回 403——dav_test.go:414/:874 现有断言已依赖此行为，MOVE 零改动 | §1/§4 C9/F1/F5 + §7 WebDAV 形态 |
| **E11（b）** | `ErrForbidden` 包裹的作用域 | **必须限定 `ActionDelete`。** `appendAuthorizedObject`（access.go:260）与 `filterAuthorizedVersions`（:72）均以 `errors.Is(err, ErrForbidden)` 决定**静默跳过**对象、其余错误上抛（loud）。今日 provider error 是裸 `fmt.Errorf("authorization decision: %w", err)`（:99）——非 ErrForbidden ⇒ List/Versions loud 500。若按原代码形态对所有动作包裹 ErrForbidden，provider 故障时 List/Versions 会静默少数据（藏数据），违反 C1/FR-1b 的"读路径不变" | §2 D3/D4 + §7 |
| **E12（c）** | 错误载荷的消息形态 | **必须静态消息 + `%w` 链（仅含 ErrForbidden）。** 原形态 `%w: authorization decision: %s` 把 provider 细节嵌入消息；MCP `errResult`（server.go:401-405）把 `err.Error()` 逐字放进工具结果（`toolDeleteFile` :311-313；`readResource` rpcError :382 同型）——泄漏面是 **MCP**。REST 已静态（classify :55 `"access denied"`）、S3 已静态（errors.go:118 `AccessDenied` + `s3CodeMessage`，仅 InvalidArgument/InternalError 透 err.Error()）。设计自带的"不含 `acl store down` 子串"断言与原形态自相矛盾。**另：** "细节仅服务端日志（调用方已 warn）"前提不成立——今日删除路径无任何调用方记录该错误（file_delete.go/object_worker.go/delete_marker.go 仅 chunk 清理 warn）；warn 须在 `authorize()` 内新增（FileService 有 logger，file.go:87/:119-123；s3compat `authorizeDelete` authz.go:35 是既有先例） | §2 D4 + §7 |
| **E13（d）** | `ErrForbidden→403` 映射在 4 个适配器的成立性 | REST ✅ classify :55（`AccessDenied`/`access denied`/403，静态）；S3 ✅ errors.go:118（`AccessDenied`/403，静态消息）；CLI 走 REST ✅；MCP ⚠️ 无 HTTP 状态概念——经 `errResult`（`IsError`）呈现，要求消息静态；WebDAV ❌ 呈 405，须 §7 改写后才是 403 | §4 F1 + §6 |

---

## 1. API 变更

**零配置变更、零 schema 变更（除 D9 的迁移 0042）、零 `go.mod` 变更（I6）、零协议响应变更（错误码复用既有 `ErrForbidden→403` 映射）。** 不新增接口、不改构造器签名、不新增 env flag（"fail_closed flag" 的落地形态 = 端口契约本身，沿用 s3compat 先例）。外部可见变更仅三处行为语义 + 一处注释 + 两处 AuditSink 契约 pin（D9/D10，见 §2/§4）：

| 层 | 旧 | 新 |
|----|----|----|
| `internal/service/access.go` `authorize()`（:83-101） | `s.authorizer == nil → return nil`（全动作放行，:91-93） | System principal 早退（置于 nil 检查前）→ nil 且 `action == access.ActionDelete` ⇒ `ErrForbidden`（文案 `"no authorization provider configured"`）；**读/写/查询动作 nil ⇒ allow 不变**（I5）；provider error 且 `ActionDelete` ⇒ 静态消息 `ErrForbidden`（403，替代今日裸 error → REST 500），provider 细节仅 warn log（E12）；**非删除动作 provider error 保持今日 loud 500**（不包 ErrForbidden，E11） |
| `internal/access/authorizer.go` `Manager.Authorize`（:20-22） | disabled → 全动作 `Allowed:true` | disabled 且 `action == ActionDelete` ⇒ `denied("access_control_disabled")`；其余动作不变（core-v1 设计 FR-2c/D3 对齐） |
| `internal/service/file.go:96-99` 注释 | *"A nil authorizer preserves the CI/MVP baseline"* | 更新：基线保留仅限非删除动作；`ActionDelete` 无 provider ⇒ 403（指向 FR-1b） |
| REST/S3/CLI/MCP | — | **零 handler 改动**：全部经既有 `ErrForbidden→403`（handler_helpers.go:55）与 `authorizeObject` 漏斗继承新语义；MCP 经 `errResult` 呈现静态消息（无 HTTP 状态） |
| WebDAV（`internal/api/webdav/dav.go`） | — | **唯一需要适配器变更的协议**（E10）：`RemoveAll` 把 `ErrForbidden` 映射为 `os.ErrPermission`（= `fs.ErrPermission`，零新导入）并通过 ctx 打标 + 外层包装把 x/net 的 405 改写为 403（§7）。**映射到 `fs.ErrPermission` 本身不足以得到 403**——v0.55.0 `handleDelete` 对任意 `RemoveAll` 错误都返回 405 |

> **核心简化：** 翻转落在 `authorize()` 单点（D3），7 个漏斗入口（Delete/DeleteVersion/Quarantine/DeleteMarker×2/DeleteBucket×2）自动继承，无 per-handler 变更（WebDAV 405→403 改写属协议层，见 E10）。S3 边界已独立 fail-closed（s3compat 端口），**不在本次变更内**。

---

## 2. 设计决策

- **D1（fail_closed 语义 = 端口契约）**：不引入 `FAIL_CLOSED`/`DeleteGate` env（全仓无此键，规格 §2.1 已证）。nil provider ⇒ deny 是契约本身，与 s3compat `authz.go` 注释语义逐字一致。
- **D2（System 豁免顺序，规格缺陷修正）**：`authorize()` 内插 `if principal.Kind == access.PrincipalSystem { return nil }`，位置在 `requireActiveTenant` 之后、**nil 检查之前**。理由：nil authorizer 时不存在 Manager 提供 `trusted_system` 早退；AV quarantine（System principal）必须在默认配置下照常工作（规格基线影响 #1 的硬要求）。Manager 内既有 system 早退（:23-25）不动。
- **D3（动作限定 + 单点翻转）**：`ActionDelete` 专用分支——**nil 翻转与 provider-error 包裹都限定到删除动作**。`:66/:206` 读路径 nil 检查不触碰（I5 正控）。包裹若不限定，`appendAuthorizedObject`（:260）/`filterAuthorizedVersions`（:72）会以 `errors.Is(err, ErrForbidden)` 静默跳过对象——List/Versions 在 provider 故障时从 loud 500 变成静默藏数据（E11）。漏斗 6 入口经 `authorize()` 继承（D3 发现）。
- **D4（错误分类 = fail-closed，静态消息 + %w 链）**：删除动作的 provider error ⇒ `fmt.Errorf("%w: authorization decision failed", ErrForbidden)`——**消息静态、`%w` 链仅含 ErrForbidden**，provider 内部细节在 `authorize()` 内 warn log（s.logger；FileService 有 logger，file.go:87/:119-123）。**不得用 `%s` 内联 err**：MCP `errResult`（server.go:401-405）把 `err.Error()` 逐字透出（E12），REST/S3 虽已静态化（classify :55 / errors.go:118）但 MCP 是泄漏面。与 s3compat `authorizeDelete`（error⇒deny + Warn，authz.go:35）对称，落实规格 FR-1e。G2 闭合。deny 分支的 `decision.Reason` 保留 `%s`——Manager 的 Reason 是受控静态词表（authorizer.go:27-62 `denied()` 常量串）；自定义 provider 须遵守同一契约（Reason 仅低基数、非内部细节）。
- **D5（Manager disabled 分支）**：`Manager.Authorize` disabled 时对 `ActionDelete` 拒绝。生产装配 disabled ⇒ Manager 为 nil（走 D2/D3 service 分支），本分支仅测试直连构造可达——仍须翻转（规格 FR-1c，与 core-v1 D3 逐字对齐）。
- **D6（grant 语义对齐 core-v1，不重复设计）**：字面量 `vault.file.delete` scope/role ∪ `ActionDelete` ACL allow；`ActionAll` 不再授删除；`capability` 阶梯结构上不可能携带 delete（core-v1 R7）——全部按已 gate 的 core-v1 设计 D1/D2 落地，本文不重述。
- **D7（FR-2/3/5/6 为 pin 不实现）**：AuditSink L0/L1/L2、schema 1.1、origin 契约、零 sibling 编译检查均已落地；本设计只负责（a）AC-7 grep 检查脚本化（CI 步骤），（b）FR-6 admin-delete op 契约以文本 pin（admin handler 不得绕过 `FileService.Delete` 直连 repo/store——op 本体属 sibling 方向）。
- **D8（parity 测试路径修正）**：验收引用以 `internal/integration/authz_parity_test.go` 为准（D1 发现）；该测试注入真实 `access.Manager`（:113-119 `svc.WithAuthorizer(manager)`），非 nil——不受本设计 nil 翻转影响，是迁移期的活护栏。
- **D9（契约 A pin——governance 重复 re-POST 语义，外部队列阻塞项）**：governance 接收方对幂等 re-POST（租约重 claim / 崩溃重投 / at-least-once）应答 **`{duplicate:true, conflict:false, status:ledgered}`**，relay 必须与首 POST 同等接受——receipt 模型（model.go）的 `Duplicate` 字段**按契约不参与接受判定**（contract test `TestReceiptDuplicateSemanticsContract` 断言：翻转 `Duplicate` 不得改变 `receiptMatches` 结果）。**`conflict:true` = 终态**：接收方永远不会账本化该事件，重试不可达成功——新哨兵 `ErrReceiptConflict`（区别于 `ErrInvalidReceipt`），relay 调 `FailAuditGovernance` 落终态（迁移 0042 `failed_at_ns`）：永不 re-claim、永不 re-POST，行保留至 retention prune（默认 7d，terminal-with-retention）——**替换今日的 bounded-backoff 无限重试**（无 maxAttempts、行累积、每周期 re-POST）。清理 `CleanupFailedAuditGovernance` 不写 origin tombstone（failed 行从未账本化，prune 后同 origin 新事实可重新入队）。
- **D10（契约 B pin——L2 接收方 echo 契约 + 7d SLA，外部队列阻塞项）**：events-L2 接收方必须在**每次** 2xx 回显 `X-Audit-Fact-Id`，**含租约丢失后的 re-POST**（receiver contract test：`TestOutboxRelay_DeliversDeletedFactToL2` 新增子用例——首 POST echo、lease-loss re-POST 2xx 无 echo ⇒ 不 complete、调度退避；echo 恢复后完成）。既有 :432 lease 子用例已 pin 稳定 fact-id + verbatim 字节；本 pin 补上 "echo 必须覆盖 re-POST"。另：`PruneEventOutbox` 7d failed 行修剪配套**告警**（`IncEventOutboxFailed` → `deploy/prometheus/alerts.yml` `EventOutboxTerminalFailures`）+ **7d delivery-recovery SLA 入失败模式表（F8）**：failed 行保留 7d 是人工重放窗口，超窗后唯一持久痕迹 = 删除事务内的 L0 audit_log。

---

## 3. 兼容性约束

| 约束 | 内容 |
|------|------|
| **C1（I5 基线）** | 除 `ActionDelete` 外一切行为不变：读/写/上传/查询在 provider 缺失时照常（`:66/:206` 不动）；`nil` embedder/llm/reranker 等既有组合不受影响 |
| **C2（已合入轮验收）** | S3 7 门禁测试 + `authz_parity_test.go` 不得回归。parity 测试经真实 Manager 注入（非 nil），与翻转正交——翻转后它仍是 204/403 断言（grant 语义保留，core-v1 R1）。**回归保护依赖 §5 步骤 6b 的 `newAuthzServer` 无条件 service allow-all stub**——否则其中 3 个（`TestBatchDeletePerKeyDenial` 允许半、`TestDeniedDeleteWritesNoOutboxRows` 控制组、`TestDeleteDeniedWhenProviderUnset` bucket 负控）翻转后破 |
| **C3（默认配置翻转是意图）** | 默认部署（`ACCESS_CONTROL_ENABLED=false`、无 provider）下 REST/CLI/MCP/WebDAV 删除从 204 → **403**。这是 G1 翻转（规格验收断言），非回归；必须与 ops 预授权顺序（§5 P0→P1→P2）配套发布 |
| **C4（AV quarantine 豁免）** | System principal 早退先于 nil 检查（D2）——默认配置 AV 隔离不中断 |
| **C5（审计路径零变更）** | L0 同事务 audit_log（恒开）· L1 webhook/notify@1.1（`deleted@1.1` 不做本地重播）· L2 配置驱动 `AUDIT_SINK_L2_*`——语义、at-least-once、never-blocking 全部保持；业务流不等待投递 |
| **C6（schema/迁移）** | 无新增 SQL、无迁移文件（I1/I2 不触发）；0041 无 UNIQUE 为既有设计决定，不改。**D9 例外：** 迁移 0042（sqlite/postgres 双文件）为 `audit_governance_outbox` 增 `failed_at_ns` 列——governance 终态（conflict:true）的承载，claim/ready 谓词加 `failed_at_ns=0`，不与既有 0039/0041 语义冲突 |
| **C7（reconcile/GC）** | GC/lifecycle/retention 直连 repo+store，不经 service 门禁（core-v1 R6）——不受翻转影响，零迁移面 |
| **C8（WebDAV MOVE）** | copy-then-delete：MOVE 的预拷贝先于 delete 门禁提交，翻转后默认配置下 MOVE 可能留下副本残留（`moveFiles` 对任意 `Rename` 错误返回 403，file.go:655-658，dav_test.go:874 已断言）。属 sibling webdav 轮范围（core-v1 R5/D8），本设计仅文档化（见 §4 F5） |
| **C9（协议层）** | REST 403 沿用 `AccessDenied`/403（handler_helpers.go:55）；S3 沿用既有 403 `AccessDenied`（errors.go:118）；CLI 走 REST；MCP 无 HTTP 状态——错误经 `errResult`（`IsError`）呈现，消息必须静态（E12）；**WebDAV 是唯一需要适配器改动的协议**：v0.55.0 `handleDelete` 将任意 `RemoveAll` 错误映射为 405 且 DELETE 路径无权限分支（E10），需 dav.go 的 `os.ErrPermission` 映射 + 外层 405→403 改写（§7）。无新状态码、无新响应字段 |

---

## 4. 失败模式

| # | 模式 | 行为 | 处置 |
|---|------|------|------|
| F1 | provider 未配置（nil）+ 非 System principal 删除 | REST/CLI/bucket：**403** `ErrForbidden` `"no authorization provider configured"`；MCP：**error 工具结果**（`IsError`，静态消息，无 HTTP 状态）；WebDAV DELETE：**403**（适配器 405→403 改写，E10）；WebDAV MOVE：403（既有 `moveFiles` 映射，源删残留见 F5） | **意图行为**（G1 翻转）；ops 按 §5 P0 预授权 |
| F2 | provider error（ACL store / directory store 故障） | **删除动作 403**（`ErrForbidden` 包裹，静态消息，E12）；provider 细节仅服务端 warn log（`authorize()` 内新增）；**读/写/查询动作保持 loud 500**（包裹限定 `ActionDelete`，E11——`appendAuthorizedObject`/`filterAuthorizedVersions` 不得静默跳过） | FR-1e；与 s3compat（error⇒deny）对称；**不**再呈现 500（G2 闭合） |
| F3 | AV quarantine（System principal，默认配置 nil provider） | 豁免早退 → 删除照常（audit `detail='av_infected'` + outbox 同事务） | D2 顺序保证；回归测试锁定 |
| F4 | `Manager` disabled 直连构造（测试路径） | `ActionDelete` → denied(`access_control_disabled`)，读动作放行 | FR-1c；单元测试锁定 |
| F5 | WebDAV MOVE 预拷贝残留 | MOVE → copy 已提交（ActionWrite 放行）、源 delete 403 → 目标副本残留；且 `Rename` 内 rollback `Delete(dst)` 亦 403（dav.go warn 后返回）→ 双副本并存 | 已知模式，sibling webdav 轮范围；本设计不修，规格 §5 已排除（迁移期 `TestMoveRollbackOnDeleteFailure` :886 的 dst 残留断言因此破，随 stub 注入恢复） |
| F6 | reconcile GC 删除 | 不经门禁，不受翻转影响 | 既有设计（C7），零处置 |
| F7 | L2 sink 401/403 | 立即终态 failed（不重投敏感载荷） | 已落地（`TestOutboxRelay_L2UnauthorizedFailsImmediately` :609），pin |
| F8 | L2 sink 5xx / 无 `X-Audit-Fact-Id` 回显 | 指数退避 + jitter → maxAttempts → failed；`delivered` 24h/`failed` 7d 修剪 | 已落地（:181/:432），pin。**7d delivery-recovery SLA（D10）：** `failed` 行保留 7d = 人工重放窗口（`PruneEventOutbox`，eventOutboxFailedRetain）；终态触发 `IncEventOutboxFailed` → 告警 `EventOutboxTerminalFailures`（`deploy/prometheus/alerts.yml`）；超窗后唯一持久痕迹 = 删除事务内 L0 audit_log（权威记录，C11） |
| F9 | L2 未绑定 | complete（记录保留，L0 权威） | 已落地（`ErrSinkNotBound`），pin |
| F10 | relay 崩溃（lease 未 complete） | 重 claim 不双调度 | 已落地（:229），pin |
| F11 | **governance 幂等 re-POST**（租约重 claim / 崩溃重投；契约 A） | 接收方应答 `{duplicate:true, conflict:false, status:ledgered}` → relay **与首 POST 同等接受**（`Duplicate` 字段不参与判定，model.go 注释 + contract test）→ complete | **[新增]** `TestReceiptDuplicateSemanticsContract`（http_test.go：`receiptMatches` 翻转 `Duplicate` 不变 + e2e duplicate re-POST 接受） |
| F12 | **governance `conflict:true`**（接收方永不账本化；契约 A） | **terminal-with-retention**：`ErrReceiptConflict` 哨兵 → `FailAuditGovernance`（`failed_at_ns`，迁移 0042）→ 永不 re-claim/re-POST、不参与 ready 待处理判定；行保留至 retention prune（默认 7d）后删除（无 origin tombstone，prune 后同 origin 可重新入队）——**替换 bounded-backoff 无限重试** | **[新增]** `TestReceiptConflictIsTerminalSentinel`（http_test）+ `TestRuntimeConflictingReceiptIsTerminalWithRetention`（runtime_test）+ `TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned`（repository_test：fencing/终态/保留/prune/重入队） |

---

## 5. 迁移步骤

**阶段 0 — 预授权（发布前，可选但推荐）**
1. 需保留删除能力的部署：`AUTH_KEYS` 键追加字面量 scope `vault.file.delete`（`t:acme:write+vault.file.delete`，core-v1 落地后）或 `PutACL(Action: ActionDelete, Effect: Allow)`（P0）。纯匿名/无 access 部署 = 接受删除 403。

**阶段 1 — 代码变更（本设计全部代码面，约 12 行生产代码 + dav.go 适配器变更）**
2. `internal/service/access.go` `authorize()`：System 早退 + nil 分支动作限定 + **动作限定的** error 包裹（静态消息 + warn log）（§7 代码形态）。
3. `internal/access/authorizer.go` :20-22：disabled 分支动作限定。
4. `internal/service/file.go:96-99`：注释更新。
5. `internal/api/webdav/dav.go`：`RemoveAll` 的 `ErrForbidden→os.ErrPermission` 映射 + ctx 打标，外层包装 405→403 改写（E10；§7）。
5b. **契约 pin 随附落地（D9/D10，非翻转本体）：** governance `validateReceipt` conflict 分支 + relay `failFact` + repository `FailAuditGovernance`/`CleanupFailedAuditGovernance` + 迁移 0042（§7 附代码形态）；L2 echo-on-re-POST 零生产代码（仅测试）；`deploy/prometheus/alerts.yml` 新增 `EventOutboxTerminalFailures`。

**阶段 2 — 测试迁移（翻转的连带面）**
6. **service 包单点注入 + 矛盾消解（(a)）：** `internal/service/service_test.go` `newTestSvc`（:16）与 `internal/service/quota_test.go` `newQuotaTestSvc`（:30，usage_consistency_test.go 专用）注入 allow-all authorizer stub（仿 s3compat `authz_gate_test.go:28` `allowAllProvider` 先例：`Authorize → Decision{Allowed:true}`，无状态，`make test-race` 安全）。**真实波及 7 个 service 测试文件（9→8→7 两轮修正）**：file_delete / delete_marker / object_version_delete / object_protection / object_retention / usage_consistency / service_test。
   - **8-vs-9 修正：** `metadata_security_test.go` 不受影响——`DeleteMetadata`/`DeleteMetadataKey` 走 `ActionWrite`（file_features.go:309/:322），非删除漏斗。
   - **本审计再剔除：** `key_validation_test.go` 不受影响——非法 key 在 `checkedObjectDefaults`/`validateKey` 先于 authorize 失败（file_delete.go:150-152）；`delete_marker_test.go` 的 `TestCreateDeleteMarkerFailsClosedOnCurrentLookupError` 不受影响——lookup 错误先于 authorize 返回（delete_marker.go:29-32）。
   - **新增 `newBareSvc(t)`**（= 今日裸构造原样）专供 FR-1b 翻转点测试。**FR-1b 测试不得经 `newTestSvc`**：allow-all stub 会让 AC-2 的 nil 断言永不触达 nil 分支——`TestDeleteFailClosedWithoutProvider` 假红（Delete 意外成功），System 豁免断言恒真空转（stub 放行与豁免无关）。§7 测试形态已同步改 `newBareSvc`。
6a. **全仓裸-svc 删除路径穷尽清单（本审计核验，19 文件 / 40 测试；含前两轮复核全部遗漏）：**

| 包 | 文件（构造点） | 破坏的测试（锚点） | 漏斗入口 |
|---|---|---|---|
| service | file_delete_test.go（newTestSvc） | TestFileServiceDelete_WritesAuditRow（:20/:31；:40 missing 不受影响，404 先于 authorize） | Delete |
| service | delete_marker_test.go（newTestSvc） | TestDeleteMarkerPreservesVersionsAndUsage（:42/:70）、TestDeletingHistoricalDeleteMarkerDoesNotReemitCurrentVersion（:126/:138） | CreateDeleteMarker/DeleteVersion |
| service | object_version_delete_test.go（newTestSvc） | TestDeleteVersionRemovesOnlyTargetAndPromotesPrevious（:29/:37） | DeleteVersion |
| service | object_protection_test.go（newTestSvc） | TestHardDeleteChecksProtectedHistoricalVersion（:80）、TestDeleteBucketRejectsProtectedObject（:94）——authorize 先于保护检查，ErrLocked→ErrForbidden | Delete/DeleteBucket |
| service | object_retention_test.go（newTestSvc） | TestObjectRetentionTargetsExactVersionAndCannotShorten（:48/:90） | Delete/DeleteVersion |
| service | usage_consistency_test.go（**newQuotaTestSvc**） | TestHardDeleteRemovesEveryVersionAndUsage（:136）、TestDeleteBucketRemovesBlobsAndUsage（:158） | Delete/DeleteBucket |
| service | service_test.go（newTestSvc） | TestWithChunkCleaner（:295）、TestDelete_soft（:361）、TestDelete_hard（:376）、TestDelete_hardLocked（:410——:404 的 err≠nil 断言假通过、:410 软删必须成功）、TestHardDelete_emitEvent（:602）、TestSoftDelete_emitEvent（:622）；TestDelete_notFound（:389）不受影响 | Delete |
| rest | handlers_test.go（setupTest :40） | TestPutGetDelete（:107 期望 204） | REST DELETE → svc.Delete |
| rest | management_test.go（newManagementRESTTest :34） | TestLockObject（:131-135，期望 409→403） | REST DELETE?hard=1（authorize 先于保护检查） |
| rest | bucket_versions_test.go（复用 setupTest） | TestListBucketVersionsIncludesHistoryAndDeleteMarkers（:23 `svc.CreateDeleteMarker`） | CreateDeleteMarker |
| mcp | server_test.go（newTestServer :721） | TestCallTool_DeleteFile_Success（:384） | delete_file → svc.Delete |
| webdav | dav_test.go（newTestServerWithSvc :58、newRollbackServer :856） | TestDeleteRemovesResource（:143）、TestMoveRenamesFile（:287）、TestMoveLargeFile（:329）、TestMovePreservesMetadata（:369）、TestMoveRollbackOnDeleteFailure（:875——status 仍 403 但 rollback `Delete(dst)` 亦 403 → dst 残留断言破） | DELETE / MOVE 源删 |
| webdav | dav_audit_test.go（newTestServerWithSvcDSN :52） | TestWebDAVDelete_CommitsAuditAndBothFacts（:74）、TestWebDAVMove_EmitsSourceDeleteFacts（:136） | 同上 |
| webdav | dav_relay_test.go（newTestServerWithRelay :69） | TestWebDAVDelete_ResponseDoesNotBlockOnDelivery（:109）、TestWebDAVDelete_CompositionL1L2（:229） | 同上 |
| s3compat | authz_gate_test.go（newAuthzServer :82） | TestBatchDeletePerKeyDenial（:252-273 允许半）、TestDeniedDeleteWritesNoOutboxRows（:324-420 控制组）、TestDeleteDeniedWhenProviderUnset（:545-560 bucket 负控：409/204→403） | S3 层放行后 service nil ⇒ 403；bucket 删除无 S3 层门禁（policy.go:71 仅 key≠""）直入 svc.DeleteBucket |
| s3compat | handler_test.go（newTestServer :33） | TestBatchDelete（:229-251）、TestDeleteBucketRequiresEmptyBucket（:253-275） | 同上（S3 层已注入 allowAllProvider） |
| s3compat | versioning_test.go（:192 直构） | TestS3GetObject_VersionId（:242/:257/:280） | DeleteVersion/CreateDeleteMarker/DeleteVersion |
| reconcile | deletion_test.go（:30 直构） | TestHardDeleteKeyRemovesEveryVersionAndAdjustsUsage（:42 设置段软删）——**前轮复核误判为"reconcile 不受影响"** | svc.Delete |
| integration | fullserver_test.go（startFullServerWithRelay :55/:75） | TestFullServer_REST_CRUD（:264）、TestDeleteResponse_DoesNotBlockOnDelivery（:739）、TestComposition_AuditSinkL2BoundTenant（:820/:832）、TestComposition_DeleteDeliversBothFacts（:913）、TestComposition_MidClaimRestartRedeliversOnce（:1220） | REST DELETE e2e |

   **复核为不受影响（负控清单）：** TestNonExistentObjectReturns404 / TestDelete_notFound / TestDeleteMissingResource（404/ErrNotFound 先于 authorize）· TestBucketPolicyDenyDelete（handler 层 checkBucketPolicy）· TestMoveMissingSource（copy 侧 Get 失败先于 delete）· TestWebDAVDelete_LockConflictedNoOutbox（423 confirmLocks 先于 RemoveAll，零 service 调用）· legal-hold/metadata/tags/ACL/multipart-abort（ActionWrite 或子资源）· admin_tenants/admin_ops（admin 直连 repo）· enterprise_access_test + authz_parity_test.go（真实 Manager，活护栏）· s3compat policy/sigv4/managed_sse 测试（无删除用例）· **antivirus 全量**（worker.go:177-180 注入 System principal → D2 豁免；同时是 D2 顺序的活回归网）· reconcile/scrub、repository、storage、replication、events、billing、SDK（stub server）· TestFullServer_S3Compat（无 DELETE——设计"harness 无 S3 DELETE 用例"声明核验成立）。
6b. **逐文件显式注入表（(b)，每个受影响构造点一行，杜绝遗漏）：**

| 构造点 | 注入 |
|---|---|
| newTestSvc（service_test.go:16） | `svc.WithAuthorizer(allowAllAuthorizer{})`（service 包自备 3 行 stub） |
| newQuotaTestSvc（quota_test.go:30） | 同上 |
| setupTest（rest/handlers_test.go:40；覆盖 handlers_test + bucket_versions_test） | `svc.WithAuthorizer(allowAllAuthorizer{})`（rest 包 stub） |
| newManagementRESTTest（rest/management_test.go:34） | 同上 |
| newTestServer（mcp/server_test.go:721） | 同上（mcp 包 stub） |
| newTestServerWithSvc（webdav/dav_test.go:58）+ newRollbackServer（:856） | 同上（webdav 包 stub） |
| newTestServerWithSvcDSN（webdav/dav_audit_test.go:52） | 同上 |
| newTestServerWithRelay（webdav/dav_relay_test.go:69） | 同上 |
| newAuthzServer（s3compat/authz_gate_test.go:82） | **无条件** `svc.WithAuthorizer(allowAllProvider{})`——**不是** `WithAuthorizer(authz)`：TestDeleteDeniedWhenProviderUnset 的 bucket 负控（409/204）要求 service 层放行 + S3 层保持 nil⇒deny 并存（R1 修正：单行注入且无条件） |
| newTestServer（s3compat/handler_test.go:33） | `svc.WithAuthorizer(allowAllProvider{})`（S3 层已有 allowAllProvider） |
| versioning_test.go:192 直构 | 同上 |
| reconcile/deletion_test.go:30 直构 | `svc.WithAuthorizer(allowAllAuthorizer{})`（reconcile 包 stub） |
| startFullServerWithRelay（integration/fullserver_test.go:75） | `svc.WithAuthorizer(allowAllAuthorizer{})`（integration 包 stub）；**S3 挂载保持 nil**（:115） |

   每个包自备 3 行 test-only stub（s3compat 已有 `allowAllProvider`；service/rest/mcp/webdav/reconcile/integration 各加同名 stub，无状态）。
7. **FR-1b/AC-1 新断言（§7 测试形态）：** deny stub ⇒ `Delete`/`DeleteVersion` 403 · `newBareSvc` nil ⇒ 403（AC-2 翻转点本体）· System 豁免（`newBareSvc`）⇒ 放行（F3 回归）· error stub ⇒ 静态 403 + 同 stub 下 `List`/`Versions` 仍 loud 负控（E11/E12）。WebDAV：deny/nil ⇒ DELETE 403（§7 改写链：`RemoveAll→os.ErrPermission` + 外层 405→403）；allow stub 正控 204。
8. `internal/integration/fullserver_test.go` harness（:55 起）：`svc` 注入 allow-all stub（REST 删除 e2e :264/:739/:820/:832/:913/:1220 依赖放行）；**S3 挂载保持 `nil`**（:115）——S3 nil⇒deny 是既有语义且该 harness 无 S3 DELETE 用例（已核验），零改动。
9. `internal/integration/authz_parity_test.go`：**零改动**——注入真实 Manager，翻转后仍 204/403（活护栏，D8）。
10. 新增断言：AC-1/AC-2/AC-3/AC-10 的 [新增] 项（§6 映射）。

**阶段 3 — 固化 + 验收**
11. AC-7 编译级 grep 写成 CI 步骤，**双 grep**：
   - **sibling 检查**（既有）：检查集 + 豁免集按规格 §4 清单，当前零命中（exit 1）。
   - **漏斗白名单 grep（(c)，本审计新增）——AC-7 单点门禁 + FR-6 的编译级强制：** 生产代码每个 `ActionDelete` 出现必须命中白名单（定义 2 + 门禁 2 + 漏斗 7 + S3 端口 1；设计原文 "6 call sites" 系 D3 计数误差，实为 7——安全复核口径一致）。任何新命中 = 新增门禁（必须经 `authorize()` 单点，D3）或新增绕过（admin 直连 repo/store 的删除若引用 ActionDelete 即被抓；不引用则被 sibling grep 抓）：

```bash
# 白名单（翻转后形态；输出必须为空，任何命中即 CI 失败）
grep -rn "ActionDelete" --include="*.go" internal/ cmd/ | grep -v "_test.go" \
  | grep -vE '^internal/access/types\.go:(76|88):' \
  | grep -vE '^internal/access/authorizer\.go:' \
  | grep -vE '^internal/service/access\.go:' \
  | grep -vE '^internal/service/(file_delete|delete_marker|file_bucket_settings)\.go:(159|179|34|37|40|48):' \
  | grep -vE '^internal/service/object_worker\.go:58:' \
  | grep -vE '^internal/api/s3compat/authz\.go:'
```

   白名单语义：`types.go:76/:88`（常量定义 + AllActions）· `authorizer.go`（翻转后 Manager disabled 分支）· `service/access.go`（翻转后 `authorize()` nil 分支）· 漏斗 7 调用点（file_delete:159/:179 · delete_marker:34/:37 · file_bucket_settings:40/:48 · object_worker:58）· `s3compat/authz.go` 整文件（S3 端口本体，含 :10 注释与 :32 门禁）。测试文件整类豁免（tests 类）。
12. `make check`（gofmt/build/vet/test）+ `make test-race` 全绿；先跑 [已落地] 项确认基线，再跑 [新增] 项。

**阶段 4 — 运营上线（P1→P2）**
13. 旧二进制上启用 access（P1，可验证预授权是否就绪）→ 交换新二进制（P2，默认配置删除 403 为预期；admin/owner/tenant_default 删除翻转亦为预期）。顺序与 core-v1 `.design.md` §5.3 一致。

---

## 6. 验收映射（规格 AC → 测试 → 状态）

| 验收 | 断言落点 | 测试载体 | 状态 |
|------|---------|---------|------|
| AC-1 deny→403 | `file_delete_test.go`：deny stub ⇒ `Delete`/`DeleteVersion` 返回 `ErrForbidden`；allow stub 正控 | **[新增]** unit | 新 |
| AC-1 e2e | REST：valid key + deny provider ⇒ `DELETE /v1/files/{key}` 403 `AccessDenied`（handler_helpers.go:55，已核验：`"AccessDenied"/"access denied"/403` 静态消息） | **[新增]** e2e（rest handler 或 integration harness 挂 deny stub） | 新 |
| AC-1 WebDAV | deny/nil provider ⇒ DELETE 403（§7 改写链：`RemoveAll→os.ErrPermission` + 外层 405→403）；MOVE ⇒ 403（既有 `moveFiles` 映射，:874 同型）；allow stub 正控 204 | **[新增]** e2e（`dav_test.go`） | 新 |
| AC-1 S3 | `TestDeleteDeniedWithoutBucketPolicy` :174 / `TestDeleteDeniedWhenNoPrincipal` :209 / `TestBatchDeletePerKeyDenial` :272 | 已落地 | 保持 |
| AC-2 翻转（nil⇒deny） | 裸 `FileService`（无 `WithAuthorizer`）`Delete` ⇒ `ErrForbidden`（今日 :91 放行）；`Get`/`List` 仍放行（I5 正控） | **[新增]** unit（`file_delete_test.go` + `service_test.go`，**专用 `newBareSvc`**——`newTestSvc` 已注入 allow-all，不得复用（§5 步骤 6）） | 新 |
| AC-2 Manager disabled | `access.Manager{Enabled:false}`：`Authorize(ActionDelete)` ⇒ `Allowed:false`（`access_control_disabled`）；`ActionRead` ⇒ `Allowed:true` | **[新增]** unit（`access_test.go`） | 新 |
| AC-2 misconfigured | provider error stub ⇒ service 删除 `ErrForbidden`（403）非 500；**错误消息静态、不含 provider 细节**（E12）；同 stub 下 `List`/`Versions` 仍 loud（非 ErrForbidden，500 语义保持——E11 负控） | **[新增]** unit | 新（G2 闭合） |
| AC-2 S3 | `TestDeleteDeniedWhenProviderUnset` :478 / `TestDeleteProviderErrorIs403Not500` :237 | 已落地 | 保持 |
| AC-3 allow→proceeds | allow stub ⇒ 软删（audit/outbox 同事务）/硬删（blob 移除）走通；parity：`internal/integration/authz_parity_test.go` :93（grant ⇒ S3 204 / REST 204，revoke ⇒ 双 403） | **[新增]** unit + 已落地 parity（路径修正 D1） | 新+保持 |
| AC-4 tenant mismatch | `tenantMatches` 拒绝跨租户（`authorizer.go`），`*` operator 放行；既有 `access_test.go` 覆盖，缺则补 1 条 | 已落地（补 1 条可选项） | 保持 |
| AC-5 L0 audit_log | `DELETE?hard=1` ⇒ `audit_log` 行 `action='file.delete'`/`detail='hard'`，同事务；`assertAuditRowFor`（fullserver_test.go:1319） | 已落地（:685/:764 用例） | 保持 |
| AC-6 L1 webhook | notify@1.1 HMAC 投递；sink 宕机删除不阻塞 | 已落地（webhook_test.go + :685） | 保持 |
| AC-7 L2 配置选择 + 零 sibling | `AUDIT_SINK_L2_*` 装配（workers.go:166-175）；sibling grep 检查集零命中；**漏斗白名单 grep**（生产 `ActionDelete` ∈ 定义 2 + 门禁 2 + 漏斗 7 + S3 端口 1，§5 步骤 11） | 已落地 + **CI 脚本化（双 grep）** | 保持+固化 |
| AC-8 outbox 投递 | 5xx 重试 :181 / L2 送达 :432 / 401 立即终态 :609 / claim 重拾 :229 / 不阻塞 :685 | 已落地 | 保持 |
| AC-9 schema 1.1 + origin | `schema_test.go` golden（:31/:96）；`origin_id == objects.id`（relay 测试 :99-101） | 已落地 | 保持 |
| AC-10 组合 e2e deny | S3：:324/:420（零 outbox 行、零事件、GET 200 正控）；REST：**[新增]** 同型（403、`event_outbox` 计数 0、`audit_log` 无新行、GET 200） | 已落地 + 新增 | 新+保持 |
| AC-10 组合 e2e allow | :685 全链路（204、L0 行、L1 投递、L2 以 httptest sink 断言送达） | 已落地 | 保持 |
| **契约 A-1（重复 re-POST 接受）** | `receiptMatches` 对 `{duplicate:true, conflict:false, status:ledgered}` 与 `{duplicate:false,…}` 结果恒等（`Duplicate` 字段**不得**进入接受判定）；e2e：duplicate re-POST → nil | **[新增]** `internal/auditgovernance/http_test.go` `TestReceiptDuplicateSemanticsContract` | 新 |
| **契约 A-2（conflict 终态）** | `conflict:true` → `ErrReceiptConflict`（非 `ErrInvalidReceipt`）；runtime：恰 1 次 POST 后零 re-POST、不可 re-claim、不属 pending；行保留至 retention prune 后被删；repo：fencing（stale owner/token 拒绝）、prune 后同 origin 可重入队 | **[新增]** `TestReceiptConflictIsTerminalSentinel` + `TestRuntimeConflictingReceiptIsTerminalWithRetention` + `TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned` | 新 |
| **契约 B（L2 echo 覆盖 re-POST）** | lease-loss re-POST 2xx 无 echo ⇒ 不 complete + 退避；echo 恢复后完成；fact-id/载荷字节跨 re-POST 恒等 | **[新增]** `internal/events/event_outbox_relay_test.go` receiver-contract 子用例 | 新 |
| **SLA 告警** | `IncEventOutboxFailed` 有 Prometheus 告警规则；7d delivery-recovery SLA 入 F8 | **[新增]** `deploy/prometheus/alerts.yml` `EventOutboxTerminalFailures` | 新 |

**验收不变量：** 全部 [新增] 断言以"翻转前的旧行为"为负控对照（如 AC-2 先证今日 :91 放行）；`make test-race` 无竞争（harness 注入的 stub 无状态）。

---

## 7. 最终代码形态（FR-1 生产代码全量）

```go
// internal/service/access.go — authorize()（:83-101 的变更后形态）
func (s *FileService) authorize(
	ctx context.Context,
	action access.Action,
	resource access.Resource,
) error {
	if err := s.requireActiveTenant(ctx, resource.TenantID); err != nil {
		return err
	}
	if principal, ok := access.PrincipalFrom(ctx); ok && principal.Kind == access.PrincipalSystem {
		return nil // D2: system 早退先于 nil 检查（AV quarantine 默认配置可用）
	}
	if s.authorizer == nil {
		if action == access.ActionDelete { // FR-1b：fail-closed，仅删除动作
			return fmt.Errorf("%w: no authorization provider configured", ErrForbidden)
		}
		return nil // I5：读/写/查询保持 CI/MVP 基线
	}
	principal, _ := access.PrincipalFrom(ctx)
	decision, err := s.authorizer.Authorize(ctx, principal, action, resource)
	if err != nil {
		if action != access.ActionDelete {
			// 读/写/查询路径保持今日 loud 语义（E11）：原始错误上抛（500）。
			// appendAuthorizedObject（:260）/filterAuthorizedVersions（:72）以
			// errors.Is(ErrForbidden) 决定跳过——误包会让 List/Versions 在
			// provider 故障时静默藏数据。
			return fmt.Errorf("authorization decision: %w", err)
		}
		// FR-1e：删除动作 provider 错误呈现 403；消息静态、%w 链仅含 ErrForbidden，
		// 细节只进服务端日志（MCP errResult 逐字透出 err.Error()，E12）。
		s.logger.Warn("delete authorization provider error; denying",
			"tenant", resource.TenantID, "key", resource.Key, "err", err)
		return fmt.Errorf("%w: authorization decision failed", ErrForbidden)
	}
	if !decision.Allowed {
		// Reason 是 PDP 受控静态词表（authorizer.go denied() 常量串）；
		// 自定义 provider 须遵守同一契约（低基数、非内部细节）。
		return fmt.Errorf("%w: %s", ErrForbidden, decision.Reason)
	}
	return nil
}
```

```go
// internal/access/authorizer.go — Manager.Authorize 头部（:20-22 的变更后形态）
	if !m.cfg.Enabled {
		if action == ActionDelete { // FR-1c：disabled ⇒ 删除拒绝（与 core-v1 D3 逐字对齐）
			return denied("access_control_disabled"), nil
		}
		return Decision{Allowed: true, Reason: "access_control_disabled"}, nil
	}
```

```go
// internal/service/file.go:96-99 — 注释更新
// WithAuthorizer enables resource-level authorization at the FileService
// boundary. A nil authorizer preserves the CI/MVP baseline for read/write
// paths; ActionDelete is fail-closed: no provider ⇒ ErrForbidden (FR-1b).
// PrincipalSystem principals are exempt before the provider check (AV
// quarantine runs unauthenticated in default configs).
```

```go
// internal/api/webdav/dav.go — RemoveAll（改造后，E10）
// v0.55.0 handleDelete 对任意 RemoveAll 错误返回 405 且无权限分支（webdav.go:266-267）。
// 映射为 os.ErrPermission（= fs.ErrPermission，零新导入）表达 WebDAV 语义，同时经 ctx
// 打标，供外层包装把“授权拒绝所致的 405”改写为 403。
type deleteErrKey struct{}
type deleteErrFlag struct{ forbidden bool }

func (f *davFS) RemoveAll(ctx context.Context, name string) error {
	name = strings.TrimPrefix(name, "/")
	err := f.svc.Delete(ctx, f.tenant(ctx), service.DefaultBucket, name, true)
	if errors.Is(err, service.ErrNotFound) {
		return os.ErrNotExist
	}
	if errors.Is(err, service.ErrForbidden) {
		if flag, ok := ctx.Value(deleteErrKey{}).(*deleteErrFlag); ok {
			flag.forbidden = true
		}
		return os.ErrPermission
	}
	return err
}

// Handler 外层包装（改造后）：DELETE 用缓冲 recorder 捕获状态码；405 且 RemoveAll
// 打过拒绝标 ⇒ 改写 403 后落盘。保留 x/net 的 confirmLocks/404 语义（DELETE 响应体
// 仅 StatusText，缓冲无风险）。对照：MOVE 的 moveFiles（file.go:655-658）对任意
// Rename 错误已返回 403，无需改动（dav_test.go:414/:874 既有断言）。
type statusRecorder struct {
	http.ResponseWriter
	status int
	body   []byte
}

func (r *statusRecorder) WriteHeader(code int) { r.status = code }
func (r *statusRecorder) Write(p []byte) (int, error) {
	r.body = append(r.body, p...)
	return len(p), nil
}
func (r *statusRecorder) flush() {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.ResponseWriter.WriteHeader(r.status)
	if r.status != http.StatusNoContent {
		_, _ = r.ResponseWriter.Write(r.body)
	}
}

// ... 在现有 Handler 返回的 http.HandlerFunc 内：
	if r.Method == http.MethodDelete {
		flag := &deleteErrFlag{}
		rec := &statusRecorder{ResponseWriter: w}
		dav.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), deleteErrKey{}, flag)))
		if rec.status == http.StatusMethodNotAllowed && flag.forbidden {
			rec.status = http.StatusForbidden // 授权拒绝 ⇒ 403（x/net 无此分支）
			rec.body = []byte(http.StatusText(http.StatusForbidden))
		}
		rec.flush()
		return
	}
	dav.ServeHTTP(w, r)
```

**测试形态（新增断言的最小集）：**

```go
// internal/service/file_delete_test.go
func TestDeleteFailClosedWithoutProvider(t *testing.T) {
	svc, _ := newBareSvc(t) // 裸 svc：翻转点断言专用（newTestSvc 已注入 allow-all stub，§5 步骤 6）
	obj := putTestObject(t, svc, "k", "body")
	if err := svc.Delete(context.Background(), "default", "default", obj.Key, false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Delete without provider: err=%v want ErrForbidden", err)
	}
}
func TestDeleteSystemExemptWithoutProvider(t *testing.T) { // F3 回归
	svc, _ := newBareSvc(t)
	obj := putTestObject(t, svc, "k", "body")
	ctx := access.SystemContext(context.Background(), "default")
	if err := svc.Delete(ctx, "default", "default", obj.Key, false); err != nil {
		t.Fatalf("system delete without provider: %v", err)
	}
}
func TestDeleteProviderErrorIsForbidden(t *testing.T) { // G2 闭合（E12）
	svc, _ := newTestSvc(t)
	svc.WithAuthorizer(errStub{}) // Authorize → Decision{}, errors.New("acl store down")
	// ... Delete ⇒ ErrForbidden，且 err.Error() 不含 "acl store down"（静态消息，%w 链仅 ErrForbidden）
	// ... 同 stub 下 List/Versions 仍 loud（非 ErrForbidden）——E11 负控
}

// internal/api/webdav/dav_test.go — [新增]（E10）
// 裸 svc（无 authorizer）DELETE ⇒ 403（RemoveAll→os.ErrPermission + 外层 405→403 改写）；
// 正控 allow-all stub ⇒ 204；MOVE ⇒ 403（既有 moveFiles 映射，:874 同型）
```

**契约 pin 代码形态（D9，随附落地；D10 仅测试 + 告警，零生产代码）：**

```go
// internal/auditgovernance/http.go — validateReceipt 尾段（改造后）
	var envelope receiptEnvelope
	if json.Unmarshal(body, &envelope) != nil {
		return ErrInvalidReceipt
	}
	if envelope.Receipt.Conflict {
		// 契约 A：conflict = 终态——接收方永不账本化，重试不可达成功。
		// 独立哨兵，relay 借此落 terminal-with-retention 而非无限退避。
		return ErrReceiptConflict
	}
	if !receiptMatches(envelope, fact) {
		return ErrInvalidReceipt
	}
	return nil
// receiptMatches 不读 Duplicate 字段（契约 A：幂等 re-POST 与首 POST 同等接受）

// internal/auditgovernance/relay.go — deliverFact 头部（改造后）
	if errors.Is(err, ErrReceiptConflict) {
		r.failFact(fact, err) // 终态：FailAuditGovernance（failed_at_ns），永不 re-claim/re-POST
		return
	}

// internal/repository/audit_governance_claim.go — FailAuditGovernance（新增）
// UPDATE audit_governance_outbox
// SET failed_at_ns=$1,claim_owner='',claim_token='',lease_expires_at_ns=0,last_error=$2
// WHERE id=$3 AND delivered_at_ns=0 AND failed_at_ns=0 AND claim_owner=$4
//   AND claim_token=$5 AND lease_expires_at_ns > $6   -- claim 身份 fencing
// claim/ready 谓词全部加 failed_at_ns=0；CleanupFailedAuditGovernance 按
// retention 窗口 prune 终态行（无 origin tombstone）。迁移 0042（双文件）：
// ALTER TABLE audit_governance_outbox ADD COLUMN failed_at_ns INTEGER/BIGINT
// NOT NULL DEFAULT 0;
```

---

## 8. 范围边界（承接规格 §5，无新增）

不实现 admin-delete op 本体（FR-6 文本 pin）· 不抽取共享 outbox kernel · 不拆遗留 `billing/*`/`auditgovernance/*` sibling 耦合（豁免集）· 不动 `auth/snaplink.go`/`oidc.go` SSO · 不引入权限词汇（core-v1 方向）· 不改 WebDAV MOVE / 迁移 0041 / reconcile GC。

**本轮随附落地（契约 pin，D9/D10，非 authz 翻转本体）：** governance 终态迁移 0042 + `FailAuditGovernance`/`CleanupFailedAuditGovernance` + relay 终态分支（~30 行生产代码）；L2 echo-on-re-POST 仅测试 pin 零生产代码；`alerts.yml` 新增 `EventOutboxTerminalFailures`。**WebDAV DELETE 适配层（`dav.go` RemoveAll 映射 + 405→403 改写，§7）是本设计唯一超出 service/Manager 两层的变更**（E10 强制）。
