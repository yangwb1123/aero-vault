# 方向：fail-closed `vault.file.delete` AuthorizationProvider 端口 + AuditSink 端口（L0 本地 / L1 协议 / L2 governance）—— 取代硬编码 Snaplink 耦合

> **模块：** `internal/billing`（组合面：`internal/access` + `internal/service` + `internal/events` + `internal/repository` + `internal/api/s3compat` + `cmd/server`）· **来源分析：** `docs/auto/analyses/internal-billing-d0d7ddd3.json` · **日期：** 2026-08-06
> **评分：** 价值 10 / 风险降低 9 / 工作量 5 / 置信度 8
> **验证基准：** 工作树 = HEAD `acfaaf4` + **未提交 WIP**（campaign 已落地的 s3compat fail-closed 门禁、AuditSink L2、删除事务性 outbox）。本文所有引用均已对照该基准逐行验证；`go build ./...` 退出码 0（§2.1）。
>
> **本文是增量规格：** 方向的两个核心断言各有一半已被先期轮次落地（S3 边界 fail-closed、AuditSink L0/L2、outbox/relay/schema）；本文的 FR 是**剩余缺口增量**（REST/core 删除门禁 fail-closed、契约 pin、验收测试化），不是绿地设计。与已 gate 通过的 sibling 规格 `authorizationprovider-vault-file-delete-core-v1.md`（+ `.design.md`）为同一 campaign 的同一组合契约，本文在其 D3/D4 语义上对齐，不重复设计 Manager 内部阶梯。

---

## 1. 问题陈述

组合契约（COMPOSE-2026-017）要求：权限 `vault.file.delete` 经 **AuthorizationProvider 端口**以 **fail_closed** 语义强制；审计经 **AuditSink 端口**分层（L0 本地 / L1 协议 / L2 governance），且**不硬编码 sibling 项目名**。方向文验证的现状在两个点上都冲突：

1. **删除授权 fail-open：** 现有端口 `internal/access/authorizer.go` 的 `Authorizer` 默认 fail-open——`Manager.Authorize` 在 `ACCESS_CONTROL_ENABLED=false` 时返回 `Decision{Allowed:true, Reason:"access_control_disabled"}`；`FileService.WithAuthorizer` 文档明示 *"A nil authorizer preserves the CI/MVP baseline"*（`internal/service/file.go:96-99`），即**无 provider 时删除照常放行**；fail-closed 仅发生在 store 错误路径。
2. **审计 relay 硬编码 sibling 项目：** `internal/auditgovernance/http.go` 的 `Publisher` POST 到 snaplink governance 路径（`governancePath = "api/v1/events"` + `wait_for=ledgered`）；`internal/auditgovernance/token.go` 与 `internal/billing/token.go` 导入 `github.com/yangwb1123/snaplink/interfaces/ssoclient`；错误串含 `"snaplink billing ..."`（`internal/billing/client.go`）；迁移 `0038_snaplink_billing` 嵌入项目名——**均不在适配器端口之后**，因此 L0（本地 `audit_log`，`internal/repository/audit.go` `RecordAudit`）与 L1（协议 relay）**无法在不导入 sibling SDK 的情况下被选择**。

**可复用的 fail-closed 先例（方向文引用，已验证）：** billing `Runtime.CheckQuota` 在租户绑定/投影缺失时返回 `service.ErrEntitlementUnavailable`（`internal/billing/runtime.go:147-172`），`Ready()` 门控启动（:136-145）。

### 触发场景（真实工作流）

1. `DELETE /v1/files/doc.pdf?hard=1`（REST）或 S3 `DeleteObject`：默认配置（access 未启用）下删除**不经任何 provider 判定**——S3 侧已修复（adapter 门禁），REST/CLI/MCP/WebDAV 侧仍 fail-open。
2. 审计合规查询 `ListAudit`：删除审计行已随删除事务写入（WIP 落地），但 L2 governance 消费者（外部审计/法规系统）需要**可配置端点 + 零 sibling SDK 依赖**的投递。
3. 运维希望在 access 启用的部署里以**字面量权限**（而非 admin/owner 粗粒度）控制谁能删对象。

---

## 2. 现状与代码证据（已逐条验证）

### 2.1 工作树状态修正（分析快照之后的关键事实）

方向文引用的分析快照（`internal-billing-d0d7ddd3.json`）早于 campaign 先期轮次。对照当前工作树：

| 方向断言 | 当前状态 | 验证 |
|---|---|---|
| (1a) S3 删除无 fail-closed 门禁 | **已修复**：`internal/api/s3compat/authz.go` `AuthorizationProvider` 端口（nil ⇒ deny，provider error ⇒ deny，`authorizeDelete`），装配 `handler.go:23-30` / `router.go:14` / `cmd/server/http.go:120`（`s3compat.NewRouter(svc, logger, accessManager)`）；7 个门禁测试 `authz_gate_test.go`（:174/:209/:237/:272/:324/:420/:478） | ✅ 读全文 + grep |
| (1b) service 层（REST/CLI/MCP/WebDAV）删除 fail-open | **仍成立（本方向剩余缺口）**：`internal/service/access.go:91` `if s.authorizer == nil { return nil }`；`internal/access/authorizer.go:20` `if !m.cfg.Enabled` → `Allowed:true` | ✅ 读全文 |
| (2) 审计 relay 硬编码 snaplink | **删除审计路径已零耦合**：`internal/events/audit_sink.go`（端口）+ `audit_sink_l2.go`（HTTP 适配器）+ `internal/config/config_audit_sink_l2.go`（`AUDIT_SINK_L2_*`）+ `cmd/server/workers.go:166-175`（装配）——**全 stdlib，零 snaplink 标识符**（§2.3 编译级检查现即通过）。遗留的 `auditgovernance/*` 与 `billing/*` relay 仍含 sibling 标识符（§5 范围边界） | ✅ 读全文 + grep |
| 删除事务性 outbox / L0 审计 / @1.1 schema | **已落地**：迁移 `0041_event_outbox`（+`event_outbox_delivered`）、`internal/repository/event_outbox.go`（`HardDeleteObjectWithEvent`/`SoftDeleteObjectWithEvent` 删除+审计+事实同事务、claim/complete/retry 栅栏）、`internal/events/event_outbox_relay.go`（claim→deliver→complete、退避+jitter、L2 sink 分发）、`internal/events/payload.go`（`BuildDeletedFact`/`BuildNotifyFact`，`schema_version:"1.1"`）、`internal/service/file_delete.go`（`deleteAuditEntry` + `deleteFacts`） | ✅ 读全文 + 测试名 grep |
| admin file-delete 路由 | **不存在**：`internal/api/rest/router.go:187-203` admin 组仅 tenants/keys/jwt/jobs/config/webhook-failures/audit/departments | ✅ grep |
| `fail_closed` 配置 flag | **无独立 env flag**：全仓无 `FAIL_CLOSED`/`DeleteGate` 配置键；campaign 已确立的机制 = **端口契约本身**（nil provider ⇒ deny，s3compat 先例 + core-v1 设计 D3） | ✅ grep |

**推论：** 方向文 verified_claim "删除在无 provider 时被允许" **对 REST/core 仍成立**（§2.4 G1），对 S3 已不成立；"AuditSink 端口不存在" **已不成立**（L0/L2 已落地）；"L1 协议面" 沿用既有 webhook/bus（不改）。本规格的 FR 是：**REST/core 门禁 fail-closed + 契约 pin + 验收测试化**。

### 2.2 方向文证据验证表

| # | 方向文引用 | 验证结果 |
|---|-----------|---------|
| E1 | `internal/access/authorizer.go`：`Authorizer` 接口；`Manager.Authorize` 在 `cfg.Enabled=false` 时返回 `Allowed:true` | ✅ 接口 :11-13；disabled 早退 :20-22（`Decision{Allowed:true, Reason:"access_control_disabled"}`） |
| E2 | `internal/service/file.go:96-99` `WithAuthorizer`；nil authorizer 保留 permissive 基线 | ✅ 原文 :96-99 *"A nil authorizer preserves the CI/MVP baseline"* |
| E3 | `internal/service/file_delete.go:96-140` `Delete`/`DeleteVersion` 调 `authorizeObject(ctx, access.ActionDelete, obj)` | ✅ 实际 `Delete` :147（门禁 :159）、`DeleteVersion` :174（门禁 :179）；引用行号漂移（+51） |
| E4 | `internal/auditgovernance/http.go`：`Publisher`、`governancePath`、`wait_for=ledgered` | ✅ `newPublisher` :34-57（`base.JoinPath(governancePath)` + `query.Set("wait_for","ledgered")` :37-40）；`governancePath = "api/v1/events"` 定义于 `model.go:19` |
| E5 | `auditgovernance/token.go` + `billing/token.go` 导入 `github.com/yangwb1123/snaplink/interfaces/ssoclient` | ✅ 两文件均导入 `ssoclient` + `ssoclient/remote`（token.go:15-16 / billing/token.go:10-11） |
| E6 | `billing/client.go`：`'snaplink billing'` 错误串、User-Agent `'aero-vault/billing'` | ✅ 错误串 :83/:102/:113/:123/:137（另有 projector.go:36/:65/:70、models.go:66）；User-Agent :128 |
| E7 | `billing/runtime.go`：`CheckQuota` → `ErrEntitlementUnavailable`；`Ready()` 门控启动 | ✅ `Ready` :136-145（:141 返回 `ErrEntitlementUnavailable`）；`CheckQuota` :147-172（:152/:156/:159）；引用行号漂移（140-160 → 147-172） |
| E8 | `internal/repository/audit.go`：`InsertAudit`/`RecordAudit` 本地 audit_log sink | ⚠️ **修正：`InsertAudit` 不存在**（全仓 grep 零命中）；实际为 `RecordAudit`（:33-42，单条 INSERT）+ 事务内 `insertAuditEntry`（:15-23）；删除路径经 `insertAuditEntry`（同事务，`HardDeleteObjectWithEvent`） |
| E9 | `internal/api/rest/admin.go:410-427` `audit()` best-effort swallow | ✅ 实际 :417-433（`audit`/`auditForTenant`，`_ = h.repo.RecordAudit(...)` 吞错）；引用行号基本吻合 |

### 2.3 补充验证（本规格新增）

- **S3 门禁已落地且测试完备**：`internal/api/s3compat/authz_gate_test.go` — `TestDeleteDeniedWithoutBucketPolicy`(:174)、`TestDeleteDeniedWhenNoPrincipal`(:209)、`TestDeleteProviderErrorIs403Not500`(:237)、`TestBatchDeletePerKeyDenial`(:272)、`TestDeniedDeleteWritesNoOutboxRows`(:324)、`TestDeniedDeleteEmitsNoEvent`(:420)、`TestDeleteDeniedWhenProviderUnset`(:478)。
- **L2 适配器安全基线**：`internal/events/audit_sink_l2.go` — 端点校验（HTTPS 或 loopback HTTP，`validateAuditSinkL2Endpoint` :81-91）、redirect 禁用（H6，:63-65）、401/403 立即终态（`ErrSinkUnauthorized`，:150）、2xx + `X-Audit-Fact-Id` 回显为 commit point（:153-157）、错误不泄漏端点/载荷（H5）。
- **relay 投递语义测试**：`internal/events/event_outbox_relay_test.go` — `TestOutboxRelay_DeliveryLifecycle`(:144)、`TestOutboxRelay_RetriesOn5xx`(:181)、`TestOutboxRelay_ClaimLostLeadsToReclaimNotDoubleSchedule`(:229)、`TestOutboxRelay_DeletedFactCompletesWithoutDelivery`(:272)、`TestOutboxRelay_DeliversDeletedFactToL2`(:432)、`TestOutboxRelay_L2UnauthorizedFailsImmediately`(:609)。
- **e2e harness 已存在**：`internal/integration/fullserver_test.go` `startFullServerWithRelay`（:73-112 装配 bus+notifier+relay，s3compat 以 nil authz 挂载 :115）与 `TestDeleteResponse_DoesNotBlockOnDelivery`（:764 起，含 `assertAuditRowFor` 断言 L0 审计行 + sink 宕机恢复 + 业务流不阻塞）。
- **编译级检查现即通过**：`grep -rln "snaplink\|yangwb1123" internal/events/ internal/service/file_delete.go internal/repository/event_outbox.go internal/config/config_audit_sink_l2.go cmd/server/workers.go` → **零命中**（退出码 1）。
- **403 映射**：`internal/api/rest/handler_helpers.go:55` `ErrForbidden → AccessDenied/403`；REST 删除路由 `r.Delete("/files/*", h.deleteKey)`（`router.go:243`，handler :445）。
- **origin 契约**：迁移 `0039_audit_governance_outbox.up.sql` `UNIQUE(origin_kind, origin_id)` + `CHECK (origin_kind IN ('admin','file'))`；迁移 `0041_event_outbox.up.sql` **无 UNIQUE 约束**（设计决定），origin 身份 = `(origin_id, event_type)` 索引 + 插入期 `HasEventOutboxFact` EXISTS 去重（`internal/repository/event_outbox.go:439-441`）+ notifier 条件跳过（`internal/events/notifier.go:74`）；payload 携带 `object_id`（delete 时 objects 行 id）供接收方身份识别（`internal/events/payload.go:33-47`）。
- **fail-closed 先例（billing）**：`runtime.go:136-145` `Ready` 在绑定/投影缺失时返回错误门控启动；:147-172 `CheckQuota` 一律 `ErrEntitlementUnavailable`（fail-closed），与方向文一致。

### 2.4 缺陷机理（剩余缺口）

| # | 缺口 | 现状 | 后果 |
|---|------|------|------|
| **G1** | REST/CLI/MCP/WebDAV 删除无 provider 时 fail-open | `service/access.go:91` nil⇒allow；`authorizer.go:20` disabled⇒allow | 默认部署（`ACCESS_CONTROL_ENABLED=false`）删除不经任何权限判定——正是方向验收 "verified assertion that today it is allowed" 要翻转的点 |
| **G2** | service 侧 provider 错误呈现为 500 而非 403 | `service/access.go` `authorize()` 对 error 仅 `fmt.Errorf("authorization decision: %w")`，不包 `ErrForbidden` | 与 S3 侧（error⇒403）不对称；"misconfigured → denied" 不成立 |
| **G3** | admin-delete op 不存在 | `router.go:187-203` admin 组无文件删除 | 方向验收 "new admin-delete op return 403" 今天不可验证；须 pin 契约（FR-5） |
| **G4** | 遗留 governance/billing relay 的 sibling 耦合 | `auditgovernance/http.go` 硬编码 `api/v1/events`+`ledgered`；`token.go` 导入 snaplink SDK | 已不在删除审计路径内（新 AuditSink 路径零耦合）；属模块自身目的，不在本方向拆除范围（§5） |

---

## 3. 需求规格

### FR-1：删除授权经 AuthorizationProvider 端口，fail-closed（三边界）

- **FR-1a（S3 边界，已落地，保持）**：`internal/api/s3compat` `AuthorizationProvider` 端口；provider 未设置 ⇒ 拒绝；provider error ⇒ 拒绝（403 `AccessDenied`，非 500）；per-key 门禁（batch delete 逐 key，单 key 拒绝不中断整批）。既有 7 测试不得回归。
- **FR-1b（service 边界，缺口 G1，核心变更）**：`FileService.authorize`（`internal/service/access.go`）对 `action == access.ActionDelete` 在 `s.authorizer == nil` 时返回 `ErrForbidden`（403）；`PrincipalSystem` 豁免（AV quarantine 等内部路径，`internal/service/object_worker.go`）；**读/写路径保持 I5 基线不变**（nil 仍放行）。
- **FR-1c（Manager 边界）**：`access.Manager.Authorize` 在 `cfg.Enabled=false` 时对 `ActionDelete` 返回 denied（`access_control_disabled`），读动作保持今日 `Allowed:true` 行为；ACL store 错误 ⇒ denied + err（fail-closed，与既有阶梯同形）。语义与已 gate 的 core-v1 设计 FR-2a/FR-2b/FR-2c/D3 逐字对齐（grant = 字面量 `vault.file.delete` scope/role ∪ `ActionDelete` ACL allow；`ActionAll` 不再授删除；system 早退不动）。
- **FR-1d（tenant 边界）**：principal 租户与 resource 租户不匹配 ⇒ denied（`tenant_mismatch`，`authorizer.go` `tenantMatches`）。
- **FR-1e（错误分类）**：provider error 对删除一律呈现 403（fail-closed），不泄漏 provider 内部细节；与 S3 侧先例一致。

### FR-2：AuditSink 端口契约（L0 / L1 / L2，已落地，pin）

- **L0（本地）**：删除事务内写 `audit_log` 行（`AuditActionFileDelete = "file.delete"`，detail `hard`/`soft`；`internal/repository/audit.go:9-11` + `insertAuditEntry`），与业务删除、outbox 事实同一事务（`HardDeleteObjectWithEvent`/`SoftDeleteObjectWithEvent`）。L0 恒开，是 L2 不可用时的权威记录。
- **L1（协议面）**：`notify@1.1` 经既有 webhook 中继投递（`internal/events/webhook.go`，`WithSecret` HMAC-SHA256 + `EVENTS_WEBHOOK_URL`）；`deleted@1.1` **不**做本地重播（relay D3 决定——重播会双发 webhook/indexer/AV/replication/SSE）；SSE/bus 传输不变。
- **L2（governance）**：`AuditSink` 端口（`internal/events/audit_sink.go`，`DeliverDeleted(ctx, tenant, factID, payload)`）+ 配置驱动适配器（`AUDIT_SINK_L2_ENDPOINT`/`AUDIT_SINK_L2_BINDINGS_FILE`，`internal/config/config_audit_sink_l2.go`；装配 `cmd/server/workers.go:166-175`）。语义：`ErrSinkNotBound` ⇒ complete（记录保留，L0 权威）；`ErrSinkUnauthorized`（401/403）⇒ 立即终态 failed；其余错误（5xx/传输/回显缺失）⇒ 退避重试至 `maxAttempts`。成功 = 2xx **且** 响应回显 `X-Audit-Fact-Id`。
- **投递语义**：at-least-once（claim→deliver→complete；lease 过期重 claim；指数退避 + jitter；`delivered` 24h / `failed` 7d 修剪）；**业务流永不等待投递**（删除响应不依赖 sink）。

### FR-3：零 sibling 项目耦合（编译级检查）

删除审计路径的端口、适配器、装配与核心删除代码**不得出现 sibling 项目标识符**（`snaplink` / `yangwb1123`）。检查集与豁免集见 §4 AC-7；该检查**当前即通过**（§2.3）。核心代码（`internal/service`、`internal/events` 端口、`internal/repository`）不导入 sibling SDK；sibling 依赖只允许存在于**豁免适配器**（§5 范围边界）。

### FR-4：被拒请求零副作用

授权判定先于删除事务：deny ⇒ 无 outbox 行、无 audit 事件、无 EventBus 事件、对象元数据与 blob 完好。S3 侧已由 `authz_gate_test.go` `TestDeniedDeleteWritesNoOutboxRows`/`TestDeniedDeleteEmitsNoEvent` 锁定；REST 侧随 FR-1b 落地后以同型 e2e 锁定。

### FR-5：事件 schema 与 origin 契约

`vault.file.deleted@1.1` envelope：`schema_version:"1.1"` + `event_type` + `tenant/bucket/key/object_id/version_id/size/etag/backend/request_id/actor`（`internal/events/payload.go:33-47`），自包含、字节稳定（golden 测试钉死）。origin 身份：`event_outbox.origin_id = objects.id`（delete 时行 id），对应 governance outbox `UNIQUE(origin_kind, origin_id)` 契约（`origin_kind='file'`，迁移 0039）；`event_outbox` 以 `(event_type, origin_id)` 索引 + 插入期 EXISTS 去重实现同语义（迁移 0041 无 UNIQUE 为既有设计决定，不改）。

### FR-6：admin-delete op 契约 pin

sibling 方向（admin file-deletion surface）新增的 admin 删除操作**必须**经 `FileService.Delete`/`authorizeObject(ctx, access.ActionDelete, obj)` 门禁（与 REST/S3 同源），使 FR-1 的 deny ⇒ 403 对 admin 路径同样成立。本方向**不实现**该 op（§5）；其验收在 sibling 方向落地时执行，本规格锁定契约（compile 级：admin delete handler 不得绕过 service 直连 repo/store）。

### 非功能约束

- 门禁不得引入新的网络往返或阻塞删除业务流（纯本地判定；L2 投递异步）。
- 错误文本不泄漏 provider/端点内部细节（H5 原则，与 `audit_sink_l2.go` 一致）。
- I5：除 FR-1 显式翻转的 `ActionDelete` 外，默认关闭组合的其余行为不变（读/写/上传/查询不受 provider 缺失影响）。
- I1/I2：若新增 SQL（预期不需要），遵守 rebind 占位符与双迁移文件规则。

---

## 4. 验收标准（可测试）

> 方向文验收逐条保留，拆为可执行断言。标注 **[已落地]** = 现有测试即满足；**[新增]** = FR-1/FR-5 落地后需补的测试。

### AC-1（deny → 403，含 admin-delete op）—— unit + e2e

- **[新增]** unit（service）：`internal/service/file_delete_test.go` 以 deny stub 实现 `access.Authorizer`（`Decision{Allowed:false, Reason:"explicit_deny"}`），断言 `svc.Delete(ctx,"default","b","k",false)` 返回 `ErrForbidden`；`DeleteVersion` 同型。正控：allow stub ⇒ nil 错误、删除继续（对照 AC-3）。
- **[新增]** e2e（REST）：valid API key（`AUTH_KEYS` 或持久化键，scope 含 `write`）+ deny provider ⇒ `DELETE /v1/files/{key}` 返回 **403**（`handler_helpers.go:55` 映射），响应体 `AccessDenied`。
- **[已落地]** S3：`TestDeleteDeniedWithoutBucketPolicy`（:174）、`TestDeleteDeniedWhenNoPrincipal`（:209）、`TestBatchDeletePerKeyDenial`（:272）。
- **[契约 pin]** admin-delete op：FR-6——op 落地时其测试必须断言 valid admin key + deny policy ⇒ 403；本方向以 FR-6 文本 + §5 范围边界锁定，不新建路由。

### AC-2（provider unset/misconfigured + fail_closed → denied）—— 翻转断言

> 方向文原句："provider unset or misconfigured + fail_closed flag → delete denied (fail-closed, verified assertion that today it is allowed)"。"fail_closed flag" 的落地形态 = **端口契约本身**（nil provider ⇒ deny），沿用 s3compat 轮先例（`authz.go` 注释）与 core-v1 设计 D3；不新增 env flag（全仓无此配置键，§2.1）。

- **[新增]** unit（service，翻转点）：**无** `WithAuthorizer` 的 `FileService`，`Delete`/`DeleteVersion` ⇒ `ErrForbidden`（今日 :91 返回 nil = 放行，本断言翻转之）；读路径（`Get`/`List`）仍放行（I5 正控）。
- **[新增]** unit（Manager）：`access.Manager` 以 `cfg.Enabled=false` 构造 ⇒ `Authorize(ActionDelete)` 返回 `Allowed:false`（reason `access_control_disabled`）；同配置下 `ActionRead` 仍 `Allowed:true`（对照断言）。**注意**：生产组合在 disabled 时 `buildAccessManager` 返回 nil（`cmd/server/access.go`），该断言覆盖直连构造 Manager 的测试路径 + FR-1c 语义。
- **[新增]** unit（misconfigured）：provider 返回 error（如 ACL store 故障桩）⇒ service 删除返回 `ErrForbidden`（403）而非 500（对照 G2）。
- **[已落地]** S3：`TestDeleteDeniedWhenProviderUnset`（:478）、`TestDeleteProviderErrorIs403Not500`（:237）。

### AC-3（provider allow → delete proceeds）—— unit + e2e

- **[新增]** unit（service）：allow stub ⇒ `Delete` 走通（软删：元数据标记 + 同事务 audit/outbox 事实；硬删：blob 移除）。
- **[已落地]** 双协议 parity：`internal/api/s3compat/authz_gate_test.go` 真实 `access.Manager` 用例（`PutACL(Action:ActionDelete, Effect:Allow)` 授予 ⇒ S3 DELETE 204 / REST 删除成功，`authz_parity_test.go` :180）——core-v1 设计 R1 的 parity 活断言，本方向保持通过。

### AC-4（tenant mismatch → denied）—— unit

- **[已落地/扩展]** `access.Manager.Authorize`：principal 租户 `A` × resource 租户 `B` ⇒ `Allowed:false`（reason `tenant_mismatch`，`authorizer.go` `tenantMatches`）；`*`（operator）不受限。既有 `access_test.go` 覆盖即满足；若缺该断言则补 1 条。
- **[已落地]** S3：`TestDeleteDeniedWhenNoPrincipal` 同族（missing principal ⇒ deny）。

### AC-5（AuditSink L0：audit_log 行）—— unit + e2e

- **[已落地]** `DELETE /v1/files/{key}?hard=1` ⇒ `audit_log` 新增行 `action='file.delete'`、`detail='hard'`、`tenant_id` 正确，与删除**同一事务**（`HardDeleteObjectWithEvent`）；soft 同型（`detail='soft'`）。集成断言已存在：`fullserver_test.go` `assertAuditRowFor`（:764 用例）。repository 层：`internal/repository/event_outbox_test.go` 覆盖同事务原子性（零行删除回滚）。

### AC-6（AuditSink L1：配置端点 + HMAC 转发）—— unit + e2e

- **[已落地]** notify@1.1 经 webhook 目标投递：`internal/events/webhook_test.go`（HMAC-SHA256 签名头、`WithSecret`）+ `TestDeleteResponse_DoesNotBlockOnDelivery`（集成：webhook 目标收到 `vault.file.notify@1.1` 载荷，`s3:ObjectRemoved:Delete`；目标宕机时删除响应不阻塞）。
- **[已落地]** relay 分发：`TestOutboxRelay_DeliveryLifecycle`（:144）覆盖 notify 事实 claim→deliver→complete 全链路。

### AC-7（AuditSink L2：配置选择 + 零 sibling import）—— compile 级 + unit

- **[已落地]** 配置选择：设置 `AUDIT_SINK_L2_ENDPOINT` + `AUDIT_SINK_L2_BINDINGS_FILE` ⇒ `workers.go:166-175` 构造 `events.AuditSinkL2` 注入 relay；不设置 ⇒ L2 禁用（relay 仅 complete，L0 权威）。unit：`internal/events/audit_sink_l2_test.go` + `internal/config/config_audit_sink_l2_test.go`。
- **[已落地]** 编译级检查（零 sibling 标识符）：对**检查集**执行
  `grep -rln "snaplink\|yangwb1123" internal/events/ internal/service/file_delete.go internal/repository/event_outbox.go internal/repository/audit.go internal/config/config_audit_sink_l2.go cmd/server/workers.go`
  ⇒ **零命中**（本规格验证时通过，§2.3）。**豁免集**（§5）：`internal/auth/snaplink.go`、`internal/auth/oidc.go`（AGENTS.md §2.5 强制复用 Snaplink SSO SDK）、`internal/billing/*`（模块自身即 Snaplink billing 集成）、`internal/auditgovernance/*`（遗留 governance relay，不在删除审计路径）。检查以 CI 命令或脚本固化，防回归。

### AC-8（outbox 投递：sink outage → 退避重投 → delivered）—— unit + e2e

- **[已落地]** `TestOutboxRelay_RetriesOn5xx`（:181）：sink 5xx ⇒ 退避重试 ⇒ 最终 delivered。
- **[已落地]** `TestOutboxRelay_DeliversDeletedFactToL2`（:432）：L2 绑定 ⇒ deleted@1.1 投递 + complete。
- **[已落地]** `TestOutboxRelay_L2UnauthorizedFailsImmediately`（:609）：401/403 ⇒ 立即终态 failed（不重投敏感载荷）。
- **[已落地]** 崩溃恢复：`TestOutboxRelay_ClaimLostLeadsToReclaimNotDoubleSchedule`（:229）——lease 过期事实被重 claim，不双调度。
- **[已落地]** 业务流不阻塞：`TestDeleteResponse_DoesNotBlockOnDelivery`（集成，:764）——sink 挂起时删除 204 立即返回，恢复后投递完成（~15s 上界）。

### AC-9（事件 schema：1.1 + origin 契约）—— unit

- **[已落地]** `internal/events/schema_test.go`：golden 字节钉死 `BuildDeletedFact`/`BuildNotifyFact`（`schema_version:"1.1"`、必填字段齐全、自包含）；严格解码器校验。
- **[已落地]** origin：`HardDeleteObjectWithEvent` 写入的 `event_outbox.origin_id == objects.id`（`internal/events/event_outbox_relay_test.go:99-101` 显式构造断言）；payload `object_id` 与行 `origin_id` 一致；dedupe 语义 = governance `UNIQUE(origin_kind, origin_id)`（`origin_kind='file'`）契约在 `event_outbox` 的等价实现（`(event_type, origin_id)` + EXISTS，§2.3）。

### AC-10（composition e2e：deny/allow 全链路）—— e2e

- **[已落地，S3]** deny：`TestDeniedDeleteWritesNoOutboxRows`（:324）+ `TestDeniedDeleteEmitsNoEvent`（:420）——403、零 outbox 行、零 audit/事件、对象完好（随后 GET 200/204 正控）。
- **[新增，REST]** deny：FR-1b 落地后，同型 e2e：deny provider + valid key ⇒ 403、`SELECT COUNT(*) FROM event_outbox` 为 0、`audit_log` 无新行、GET 仍 200。
- **[已落地]** allow：`TestDeleteResponse_DoesNotBlockOnDelivery` —— 204、`audit_log` 行（L0）、notify 投递（L1）；L2 侧以 `httptest` sink 断言 deleted@1.1 送达（扩展 harness 或复用 `TestOutboxRelay_DeliversDeletedFactToL2` + 集成组合，二选一即可满足 "audit delivered to L0 and L2 sinks, notify delivered"）。

---

## 5. 范围边界（明确不做）

| 不做 | 理由 |
|------|------|
| admin file-delete 路由/op 本体 | sibling 方向（analysis 方向 3）；本规格仅 pin 门禁契约（FR-6） |
| 共享事务性 outbox kernel 抽取（billing/auditgovernance/events 三处合并） | analysis 方向 1（未选中）；既有 `event_outbox` 独立成立 |
| 移除 `internal/billing/*`、`internal/auditgovernance/*` 遗留 relay 中的 snaplink 标识符/导入 | billing 模块自身即 Snaplink billing 集成（client.go/projector.go/token.go 为其目的）；遗留 governance relay 已不在删除审计路径；FR-3 检查集明确豁免 |
| `internal/auth/snaplink.go` / `oidc.go` 的 Snaplink SSO | AGENTS.md §2.5 强制复用 sibling SSO SDK（外部令牌验证） |
| 权限词汇（`vault.file.delete` scope token）、`checkScope`、`PersistedKey` schema | core-v1 方向（internal/auth）所有，已 gate；本方向沿用 `access.Authorizer` 形状与 `ActionDelete` |
| WebDAV MOVE 预检、webhook/bus/SSE 传输改造 | sibling webdav 方向；L1 保持既有传输 |
| 迁移 0041 增加 UNIQUE 约束、reconcile GC 路径改造 | 既有设计决定（D6/GAP-3）；GC 不经 service 门禁（core-v1 R6） |

## 6. 基线影响

1. **FR-1b 是行为翻转**：默认配置（`ACCESS_CONTROL_ENABLED=false`）下，REST/CLI/MCP/WebDAV 的对象删除从"无门禁放行"变为"无 provider ⇒ 403"。必须保留 `PrincipalSystem` 豁免，否则 AV quarantine（`internal/service/object_worker.go` → `QuarantineObjectByID` → `authorizeObject(ActionDelete)`）在默认配置下断裂。
2. **既有测试迁移**：无 authorizer 执行删除的测试（s3compat 轮先例 + core-v1 §5.2 盘点 ~23 文件）需按 harness 单点注入 allow-all stub；`internal/integration/fullserver_test.go` 挂载点已存在（:115 传 nil authz，需改为 allow-all 桩或显式 provider）。
3. **操作顺序（P0→P1→P2）**：先预授权（`AUTH_KEYS` 字面量 `vault.file.delete` / `PutACL(ActionDelete)`，P0 可选），再启用 access（P1，旧二进制上可验证），最后交换二进制（P2，admin/owner/tenant_default 删除翻转 403 为预期）。详见 core-v1 `.design.md` §5.3。
4. **S3 已合入轮验收保持通过**：FR-1c 的 grant 语义（`ActionDelete` ACL allow 保留）与 `authz_parity_test.go` 兼容（core-v1 R1 已论证）。

## 7. 实现指引（供验收后落地，非本规格交付物）

- **FR-1b/1c/1e**：`internal/service/access.go` `authorize()` nil 分支 + `internal/access/authorizer.go` disabled 分支与 `authorizeDelete` 分支——**语义按 core-v1 设计 D3/D1 逐字落地**（本文不重复设计）；`internal/service/file.go:97` 注释更新。
- **测试新增**：AC-1/AC-2/AC-3/AC-10 的 [新增] 项；`make check`（gofmt/build/vet/test）全绿；`make test-race` 无竞争。
- **FR-3 固化**：把 §4 AC-7 的 grep 检查写成 CI 步骤（或文档脚本），检查集 + 豁免集按本文清单。
- **验收执行**：先跑 [已落地] 项确认基线，再跑 [新增] 项；e2e 用 `internal/integration` harness 扩展 L2 httptest sink。
