# 方向：复用审计-governance 事务性 outbox 机制 —— `vault.file.deleted@1.1` 持久异步投递（AuditSink 端口）

> **模块：** `internal/access`（组合面：`internal/service` + `internal/repository` + `internal/events` + `cmd/server`）· **来源分析：** `docs/auto/analyses/internal-access-f4571c58.json` · **日期：** 2026-08-06
> **评分：** 价值 9 / 风险降低 9 / 工作量 7 / 置信度 8
> **验证基准：** 工作树 = HEAD `fb74b19` + **未提交 WIP**（round-2 campaign `transactional-outbox-delete-events-v1.md` 的落地代码，见 §2.1）。本文所有引用均已对照该基准逐行验证；`go build ./...` 与 outbox 相关测试当前全绿（§2.1）。
>
> **本文是增量规格：** 方向 acceptance 要求的机制（事务性 outbox、claim/retry/complete relay、@1.1 载荷 schema）**已在 WIP 中落地**；本文的 FR 是**缺口增量**（§2.3 G1–G4），不是绿地设计。

---

## 1. 问题陈述

删除事件的审计投递今天**既不持久也不原子，且根本没有审计记录**：

1. `RecordAudit` 直接写 `audit_log`（`internal/repository/repository_interface.go:173`，方向文引 :164 有漂移），无 outbox；且**删除路径根本不调用它**——全仓 `RecordAudit` 调用方只有 `api/rest/admin.go:421`（admin 操作审计）与 `auditgovernance/repository.go` 包装器（租户绑定）。`FileService.Delete` 对一次删除**不产生任何 audit_log 行**。
2. `Bus.Publish` 的文档注释明示 *"Errors are logged but never propagated — lifecycle events must not break user requests"*（`internal/events/bus.go:81-85`）——`vault.file.deleted` 在崩溃窗口（删除事务提交后、事件事务提交前）**永久丢失**，调用方无从感知。
3. 仓库已有完整、已验证的事务性 outbox 先例：`RecordAuditWithGovernance`/`InsertEventWithGovernance` 在**同一事务**写业务行 + `audit_governance_outbox` 行（`internal/repository/audit_governance_write.go:14-89`），`internal/auditgovernance/relay.go` 提供 claim→deliver→complete + 退避抖动投递，迁移 `0039_audit_governance_outbox.up.sql` 定义重试字段。但该机制**租户绑定 + 脱敏形状**（`WrapRepository` 按 `runtime.Capture(tenantID)` 门控，`AuditGovernanceFact` 只存 digest），非通用版本化事件 outbox。
4. **事件 schema 无版本**：`repository.Event`（`repository.go:175-189`）无 version 字段——按方向文，"@1.1 必须加到 envelope"（即持久化 outbox 事实的载荷，而非 Event 结构体）。

**本方向要求：** 把该模式泛化为**常开（always-on）的 AuditSink 端口**——L0 本地 `audit_log`（与删除同事务）/ L1 协议面（既有 bus/webhook/SSE，不改）/ L2 governance 适配器（经端口配置，核心代码无 sibling 项目硬编码），使 `vault.file.deleted@1.1` 获得 durable_async 投递：**删除响应永不等待投递，投递在崩溃后由租约重 claim 恢复**。

### 触发场景（真实工作流）

1. `DELETE /v1/files/doc.pdf?hard=1` → WIP 中 `hardDeleteObject` 已把 `DELETE FROM objects` 与两条 outbox 事实放进同一事务（`HardDeleteObjectWithEvent`），但 **audit_log 无行**、**deleted@1.1 投递是空操作**（relay 直接 complete，`event_outbox_relay.go:deliverFact`）。
2. 审计合规查询 `ListAudit`：今天查不到任何对象删除记录（只有 admin 操作）。
3. L2 governance 消费者（外部审计/法规系统）：从未收到 `vault.file.deleted@1.1`。

---

## 2. 现状与代码证据（已逐条验证）

### 2.1 工作树状态修正（分析之后的关键事实）

round-2 campaign（`docs/requirements/transactional-outbox-delete-events-v1.md`，cmd-server 方向）的落地代码**已存在于工作树（未提交）**，且当前全绿（本规格验证时实际运行）：

| WIP 文件 | 内容 | 验证 |
|---|---|---|
| `internal/repository/event_outbox.go` | `HardDeleteObjectWithEvent`/`SoftDeleteObjectWithEvent`（删除 + 事实同事务，零行删除回滚）、`ClaimEventOutbox`/`CompleteEventOutbox`/`RetryEventOutbox`（owner+token+lease 栅栏）、`PruneEventOutbox`、`HasEventOutboxFact`、`EventTypeFileDeleted11="vault.file.deleted@1.1"`/`EventTypeFileNotify11`、payload 校验要求 `schema_version=="1.1"` | ✅ 读取全文 |
| `internal/repository/migrations/{sqlite,postgres}/0041_event_outbox.{up,down}.sql` | `event_outbox`（status/attempts/available_at_ns/claim_owner/claim_token/lease_expires_at_ns/last_error/delivered_at_ns）+ `event_outbox_delivered` | ✅ |
| `internal/events/event_outbox_relay.go` + `event_outbox_relay_test.go` | 轮询 relay：claim→按 event_type 分发→complete/retry；退避 + jitter；claim-lost warn 不循环重试；`deleted@1.1` 当前**仅 complete 不投递**（D3 决定） | ✅ |
| `internal/events/payload.go` + `schema_test.go` | `BuildDeletedFact`/`BuildNotifyFact`（`schema_version:"1.1"` 自包含 JSON）+ golden 字节钉死 + 必填字段测试 | ✅ |
| `internal/config/config_event_outbox.go` | `EVENT_OUTBOX_*` 配置（poll/batch/claim TTL/http timeout/max attempts） | ✅ |
| 装配 | `cmd/server/workers.go:startEventOutboxRelay`（relay 常开）、`file_delete.go`（`deleteFacts` + WithEvent 调用）、`notifier.go`（`HasEventOutboxFact` 去重跳过）、`telemetry`（`event_outbox.*` 计数器）、`repository_interface.go`（新方法） | ✅ |

实测：`go build ./...` 退出码 0；`go test ./internal/repository/ -run 'TestEventOutbox|TestDeleteObjectWithEvent|TestAuditGovernanceAtomic'` 与 `go test ./internal/events/ -run 'Outbox|EventSchema'` 全部 `ok`。

**推论：** 方向 verified_claim #2（"Always-on outbox for delete events … do not exist (proposed)"）**前半已过时**——删除 outbox 机制已存在；**"AuditSink 端口不存在"仍成立**（全仓无 `AuditSink` 符号，grep 仅命中 `EventSink`）。

### 2.2 方向文证据验证表

| # | 方向文引用 | 验证结果 |
|---|-----------|---------|
| E1 | `repository_interface.go:164` — `RecordAudit` 直写 `audit_log`，无 outbox | ✅ 实际 :173（漂移）。`internal/repository/audit.go:14-22` 单条 INSERT，无事务封装；调用方仅 admin.go:421 与 governance 包装器 |
| E2 | `events/bus.go` — `Publish` 从不传播错误 | ✅ 注释 :81-85 原文 *"Errors are logged but never propagated"*；`InsertEvent` 在业务事务提交后执行（WIP 之前的三事务窗口已由 WithEvent 消除） |
| E3 | `audit_governance_write.go` — `RecordAuditWithGovernance` 同事务写 + outbox claim 查询 | ✅ `RecordAuditWithGovernance`(:14-43)：BeginTx → 插 `audit_log` RETURNING id → 插 outbox → Commit；`InsertEventWithGovernance`(:45-89) 同形状。**修正：** claim 查询不在本文件，在 `audit_governance_claim.go`（write.go 是 enqueue 侧 `INSERT … WHERE NOT EXISTS` 去重） |
| E4 | `0039_audit_governance_outbox.up.sql` — outbox 表、重试字段 | ✅ attempts/available_at_ns/claim_owner/claim_token/lease_expires_at_ns/last_error/delivered_at_ns + `UNIQUE(origin_kind,origin_id)` |
| E5 | `auditgovernance/relay.go` — Claim/Complete/Retry + boundedBackoff+jitter | ✅ `ClaimAuditGovernance` 调用 :62、`CompleteAuditGovernance` :89、`RetryAuditGovernance` :100；`boundedBackoff` :130-145（2× 指数、max 封顶、sha256(id) 派生的 ±25% jitter） |
| E6 | `auditgovernance/runtime.go` + `repository.go` — `WrapRepository` 适配器缝 | ✅ `WrapRepository`(repository.go:17-21) 返回 `auditedRepository`，拦截 `RecordAudit`/`InsertEvent` 且按 `runtime.Capture(tenantID)` 门控——**租户绑定 + 脱敏（`AuditGovernanceFact` 只有 digest）**，这正是需要泛化的点；`Publisher`(http.go:97-131) 是配置驱动（URL + 每租户 token，纯 stdlib HTTP）的 L2 投递先例，**无 sibling 导入** |
| E7 | `repository.go:175-185` — `Event` 无 version 字段 | ✅ 结构体 :175-189（ID/TenantID/Bucket/Key/Type/ObjectID/RequestID/Payload/CreatedAt），无 version；`EventType` 仅 created/updated/deleted/accessed |
| E8 | `audit_governance_test.go` — 既有投递/重试测试模式 | ✅ 327 行；`TestAuditGovernanceAtomicCaptureAndClaimFencing`(:45)、`TestAuditGovernanceAtomicFailureRollsBackLocalAudit`(:83)、reconcile 去重(:98) 等可镜像 |
| E9 | `file_delete.go:91-117` — `HardDeleteObject` | ⚠️ **行号漂移：** HEAD 下 hardDeleteObject 在 :16-71（:91-117 是 softDeleteObject+Delete）。**语义主张成立：** repo 删除事务是原子边界（legal-hold 检查 + access-state 清理 + DELETE，`sql_objects_maint.go`），storage blob 删除在事务外（`store.Delete` 先行）——"事务性"仅覆盖 metadata+audit+outbox |

### 2.3 缺口分析（本方向 acceptance vs 现状）

| # | 缺口 | 现状证据 | 后果 |
|---|------|---------|------|
| G1 | **删除事务内无 audit_log 行** | 删除路径零 `RecordAudit` 调用；`audit.go` 无 tx 内插入变体 | AC-1（"一条 audit_log 行与删除原子提交"）不满足 |
| G2 | **无 AuditSink 端口；deleted@1.1 无实际投递** | `event_outbox_relay.go:deliverFact` 对 `EventTypeFileDeleted11` 直接 `r.complete(fact)`；全仓无 `AuditSink` 符号 | AC-5（"L2 适配器收到 deleted@1.1"）不满足 |
| G3 | **@1.1 envelope 缺 `object_id`** | `payload.go` 的 `deletedFact` 有 version_id 无 object_id；`OriginID` 列携带 objects.id（仅参考列） | AC-4（envelope 必含 tenant/bucket/key/**object_id**/actor/version）不满足 |
| G4 | **无 durable_async 时序断言测试** | relay 独立 goroutine（结构上不阻塞），但无测试钉死 | AC-3（"永不阻塞 DELETE 响应"）无可执行证明 |
| — | （记录）`repository.Event` 无 version 字段 | 按方向文措辞 "@1.1 必须加到 envelope" → 版本化落在 outbox 载荷（WIP 已做 `schema_version`），**Event 结构体不动**（与 sibling 规格范围决定一致，§5） | 非缺口 |

---

## 3. 需求规格

### FR-1：删除事务 = 元数据删除 + audit_log 行 + `vault.file.deleted@1.1` outbox 行（单事务原子）

`FileService.Delete`（硬删与软删）必须把**审计行与删除行同事务提交**：

- 在 WIP 的 `HardDeleteObjectWithEvent`/`SoftDeleteObjectWithEvent`（`event_outbox.go`，删除 + outbox 事实单事务）基础上，把**一条 `audit_log` 行插入并入同一事务**。推荐签名：两方法增加 `entry AuditEntry` 参数（或等价 `WithAuditEvent` 变体；实现侧可自行选择，要求是**同一 tx**、`I1` 占位符规范）。
- 审计行字段（`AuditEntry`，repository.go:293-301）：`Action` 用删除动作常量（如 `"file.delete"`，硬/软删共用或区分均可，spec 不钉死字面量——测试与实现同步即可）；`Actor` 取 `access.PrincipalFrom(ctx)` 的 principal（无上下文允许空串，**不新增身份管线**）；`Target` 为 `bucket/key`；`TenantID` 为租户；`CreatedAt` 空则由 repository 盖时间戳（audit.go:11-13 既有行为）。
- **事务内任一失败（含事实校验失败、约束冲突）→ 整体回滚：对象行不删、audit_log 不落、outbox 事实不落**（AC-1 强制回滚断言；WIP 已有零行删除回滚守卫，扩展覆盖审计插入）。
- storage blob 删除保持事务外（`store.Delete` 先行）——"事务性"仅覆盖 metadata+audit+outbox（方向文 E9 语义）。
- L0 适配器 = 本地 `audit_log` 写入，**始终激活**（不 gate，与 `AI_*`/`REPLICATION_*` 等 opt-in 不同——审计是基线义务；方向文 "always-on AuditSink port"）。

### FR-2：AuditSink 端口（L0 本地 audit_log / L1 协议面 / L2 governance 适配器，无 sibling 硬编码）

新增**常开端口**，FileService 只依赖端口、适配器经 `cmd/server` 装配：

- **端口定义**（建议放 `internal/service`，与 `EventSink`（file.go:55）并列；或 `internal/events`——实现侧自选）：删除路径的审计/事件事实**同步进事务、异步投递**。端口不承载"删除后调用"语义——原子性由 FR-1 的 repo 事务保证，端口负责把 L2 适配器接入投递面。
- **L0 适配器 = 本地 `audit_log`**（FR-1 事务内插入，常开）。
- **L1 适配器 = 协议面**（既有 `Bus.Publish` 广播 → SSE/webhook/indexer/AV/replication/Notifier）：**保持现状，本方向不改**（WIP 已由 `notifier.go` 的 `HasEventOutboxFact` 去重跳转覆盖通知面；SSE 回放依赖 `object_events`）。
- **L2 适配器 = governance 投递**：配置驱动的 HTTP 端点（镜像 `auditgovernance/http.go` 的 `Publisher` 形状：`BaseURL` + 每租户 token/binding，Bearer POST JSON，receipt 校验），投递**完整 @1.1 envelope**（非脱敏 digest——`AuditGovernanceFact` 的 redacted 形状属于既有 governance 机制，本端口是独立面）。**每租户绑定**：绑定租户的 deleted@1.1 事实投递到 L2；未绑定租户不投递（L0 照常）。
- **核心代码无 sibling 项目硬编码（不变量）：** L2 适配器只经配置（URL/密钥）接入，不得 import sibling 项目（如 snaplink）SDK；配置键落 `docs/configuration.md`，命名建议 `AUDIT_SINK_L2_*`（enabled/base URL/token URL/每租户 bindings），默认关（L2 是 opt-in；L0 常开）。
- 端口**常开**：即使 L2 未配置，L0 + outbox + relay 照常运行（deleted@1.1 行照写、relay 照 complete——记录保留，前向兼容）。

### FR-3：@1.1 envelope 版本化 schema（`object_id` 加入）

- 在 WIP `deletedFact` 载荷（`payload.go`）基础上**增加 `object_id` 字段**（值 = `obj.ID`，emit 时刻可得）：envelope 必含 `schema_version:"1.1"`、`event_type:"vault.file.deleted@1.1"`、`tenant`、`bucket`、`key`、**`object_id`**、`actor`（方向文 acceptance 明列）+ 既有 `version_id/size/etag/backend/request_id`。
- `repository.Event` 结构体**不加** version 字段——版本化落在 outbox envelope（方向文原文"@1.1 必须加到 envelope"；`object_events` 遗留流不动，§5）。
- 校验与测试：`schema_test.go` 的 golden 常量与必填字段列表同步更新；`validateOutboxPayload`（event_outbox.go，要求 `schema_version=="1.1"`）不变。**无需新迁移**——`event_outbox.payload` 是 TEXT，schema 演进在应用层。
- 兼容性：WIP 已落库的旧载荷（无 `object_id`）仍可被 relay **原样投递**（deleted@1.1 无解析路径——relay 不解析、`parseNotifyPayload` 仅 notify）；接收方以**载荷自身为身份**（事实 id 经请求头 `X-Audit-Fact-Id` 携带，echo receipt），`object_id` 缺失不阻断、不 enrich（verbatim 不变量；G5 修订：`OriginID` 列对接收方不可见，不作权威引用）。

### FR-4：relay 分发扩展 —— deleted@1.1 投递到 L2 适配器；durable_async 永不阻塞删除响应

- `event_outbox_relay.go:deliverFact` 对 `EventTypeFileDeleted11` 从"直接 complete"改为：**解析 envelope → 查 L2 绑定 → 命中则 POST 完整 @1.1 载荷（Bearer token）→ complete；未命中或无配置 → complete（记录保留，现状语义）**。目标失败（5xx/传输错误）→ 既有 `RetryEventOutbox` 退避 + jitter（WIP 已实现，`maxAttempts` 到顶 → `status='failed'` 终态）；租约过期 → 重 claim 重投（reaper 语义 = lease-expiry redelivery，`TestEventOutboxClaimLeaseExpiryRedelivers` 已覆盖同形状）。
- **claim→publish→complete 状态推进**复用 WIP 机制（`pending→inflight→delivered`），零新表。
- **durable_async（不变量）：** relay 独立 goroutine（`cmd/server/workers.go:startEventOutboxRelay`）；删除事务只插行，**不调用任何投递**；DELETE HTTP 响应与 L2 投递进度**零耦合**（AC-3 时序断言钉死）。`claim TTL > 2×HTTP timeout` 校验沿用（config_event_outbox.go 与 audit 先例）。
- L2 投递失败**不影响** L0（audit_log 已同事务落库）与其它已 claim 事实（per-fact goroutine，WIP 形状）。

### 非功能约束

- `make check` 全绿（`gofmt`/`go build`/`go vet`/`go test`，AGENTS.md §0）；新增/修改文件 ≤ 500 行（WIP `event_outbox.go` 414 行已近上限——**L2 适配器建议独立文件**）。
- 纯 stdlib，无新 `go.mod` 依赖（I6）；SQL 遵守 I1（占位符不复用）/I2（迁移双文件、`.down.sql` 不自动执行——本方向**预期无新迁移**，若 L2 绑定表需持久化则走 0042 双文件对，先论证）；不触碰中间件链（I4）。
- 基线路径（SQLite + local FS + AI off）全绿；Postgres 方言差异沿用 WIP（claim SQL 双方言版）。

---

## 4. 验收标准（可测试；逐条映射方向 acceptance）

> 测试基建（已验证）：`repository.Open("sqlite", "file:…")` + `Migrate`（`audit_governance_test.go`/`event_outbox_test.go` 先例）；service 测试 `service.NewFileService(store, repo, …)`；relay 测试 `internal/events/event_outbox_relay_test.go` 先例（httptest 目标 + 注入 token）。

### AC-1 单事务原子：删除 + 恰一条 deleted@1.1 outbox 行 + 恰一条 audit_log 行；注入失败 → 三者皆不落

```go
// internal/repository/event_outbox_test.go（扩展既有 TestDeleteObjectWithEvent_OneTx）
func TestDeleteObjectWithAudit_OneTx(t *testing.T) {
	// 1) 有效路径：HardDeleteObjectWithEvent(…, entry{Actor:"alice", Action:"file.delete",
	//    Target:"default/k", TenantID:"default"}, facts{deleted@1.1}) → nil
	//    断言（同事务提交后）：GetObject → ErrNotFound；
	//    event_outbox 中 vault.file.deleted@1.1 恰 1 行，
	//    payload.schema_version=="1.1" 且 payload.object_id==obj.ID；
	//    audit_log 恰 1 行（tenant_id/Actor/Target 匹配 entry，id > 删除前最大 id）
	// 2) 强制回滚：传入非法事实（event_type 不在允许集 → 校验错误）→ 方法返回 error；
	//    GetObject 仍存在（行未删）；event_outbox 0 行；audit_log 无新增行
	// 3) SoftDeleteObjectWithEvent 同形状：deleted_at 置位 + deleted@1.1 行 + audit_log 行；
	//    回滚断言同上
}

// internal/service/file_delete_test.go
func TestFileServiceDelete_WritesAuditRow(t *testing.T) {
	// 硬删与软删各一（actor 经 access 上下文注入；无上下文 → Actor==""）
	// 断言：audit_log 恰 1 行 / 次删除，Action 含 "delete"（实现字面量），
	// TenantID/ Target 匹配；event_outbox 中 deleted@1.1 恰 1 行（payload 按 AC-4 schema）
}
```

### AC-2 relay：claim→publish→complete 推进；publisher 错误 → 退避重试；崩溃（租约过期）→ 重投

```go
// internal/events/event_outbox_relay_test.go（扩展；L2 目标 = httptest.Server）
func TestOutboxRelay_DeliversDeletedFactToL2(t *testing.T) {
	// 1) 成功路径（绑定租户）：claim → relay 向 L2 目标 POST 恰 1 次，
	//    载荷 == event_outbox.payload 原样（@1.1 envelope，AC-4 字段齐全，
	//    Authorization: Bearer <binding token>）→ complete → status=='delivered'；
	//    再次 claim → 无行返回
	// 2) publisher 5xx：目标返回 500 → RetryEventOutbox 已调度：
	//    attempts==1、available_at_ns > now、退避 ∈ [initial/2, max] 且
	//    jitter ±25%（镜像既有 TestEventOutboxBackoffBounds）
	// 3) 崩溃重投（reaper）：claim 后不 complete、租约过期 → 重新 claim 取回同一事实并投递；
	//    deliver→complete 窗口为显式 at-least-once（重投可重复 POST——S3 等价，接收方幂等）
	// 4) 未绑定租户：L2 零 POST，事实仍被 complete（status=='delivered'）
}
```

### AC-3 durable_async 时序断言：DELETE 响应永不等待投递

```go
// cmd/server/…e2e 或 internal/events/…（httptest 全服务器 + SQLite；L2 目标 = 可暂停的 httptest）
func TestDeleteResponse_DoesNotBlockOnDelivery(t *testing.T) {
	// 1) L2 目标置为 down（关闭的 listener 或挂起 handler）
	// 2) REST DELETE /v1/files/k → 在 L2 目标仍阻塞（release channel 未关闭）期间
	//    断言响应已返回（信号式，判别确定性：同步实现不可能在 release 前返回）；
	//    挂起守卫 < relay 的 HTTP timeout 默认 5s（如 4s）
	// 3) 删除后：event_outbox 中 deleted@1.1 行 status ∈ {pending, inflight}
	//    （未 delivered——投递仍在重试/租约内）；audit_log 行已存在（L0 不受影响）
	// 4) 恢复目标 → relay 下一轮投递成功 → status=='delivered'；期间无第二次删除
}
```

### AC-4 JSON-Schema 测试：@1.1 envelope 必含 tenant/bucket/key/object_id/actor/version

```go
// internal/events/schema_test.go（扩展）
func TestEventSchema_Deleted11Envelope(t *testing.T) {
	// 1) 必填字段（JSON-schema 风格断言，非字节级）：
	//    {schema_version:"1.1", event_type:"vault.file.deleted@1.1",
	//     tenant, bucket, key, object_id, actor} 全部存在且类型正确
	//    （object_id 为整数 = obj.ID；actor 允许 ""）；version_id/size/etag/
	//    backend/request_id 仍必填（WIP 既有断言保留）
	// 2) 无 records 字段（deleted@1.1 不是 S3 通知形状）
	// 3) golden 字节钉死更新：BuildDeletedFact 输出 == 新 golden 常量
	//    （含 object_id 字段，字段序固定 → 字节稳定）
	// 4) 旧载荷兼容（已随设计迁移至 AC-2 ⑤）：deleted@1.1 无解析路径，"解析不
	//    panic"是空断言——真实兼容契约 = 旧载荷（无 object_id）经 relay 原样
	//    投递且字节恒等（verbatim 不变量，不 enrich）；接收方以载荷自身为身份
	//    （事实 id 经 X-Audit-Fact-Id 请求头携带，echo receipt）
}
```

### AC-5 组合 e2e：L2 适配器经端口配置收到 deleted@1.1；未绑定租户 L0 照常

```go
// cmd/server/…e2e（httptest 全服务器 + SQLite + local FS，镜像 internal/integration/fullserver_test.go）
func TestComposition_AuditSinkL2BoundTenant(t *testing.T) {
	// 1) 装配：L2 适配器仅经配置指向 httptest URL（+ 每租户 token）；
	//    核心代码零 sibling 导入（review 级 grep：适配器文件 import 仅 stdlib/
	//    internal 包——e2e 本身以"URL 配置驱动"证明端口化）
	// 2) 绑定租户 t1：PUT → DELETE → L2 目标收到 1 次 POST，
	//    载荷 = @1.1 envelope（AC-4 字段）；audit_log 有 t1 行
	// 3) 未绑定租户 t2：PUT → DELETE → L2 目标零 POST（计数断言）；
	//    audit_log 仍有 t2 行（L0 对其它租户保持激活）
	// 4) 无 L2 配置的对照服务器：删除照常（2xx + audit_log + outbox 行），
	//    relay 正常 complete —— always-on 端口在 L2 缺席时降级为记录保留
}
```

### AC-6 既有行为不回归

- `go test ./internal/repository/ ./internal/events/ ./internal/service/ ./cmd/server/` 全绿；`make check` 全绿。
- `Bus.Publish` 签名与"错误吞掉、不阻塞"语义不变；`object_events`/SSE 回放路径不变；WIP 的 `notify@1.1` relay 行为不变（本方向只改 deleted@1.1 分发）。
- `auditgovernance`（租户绑定、脱敏、revision/draining）机制**零改动**——L2 是新端口面，不复用其绑定表。

---

## 5. 范围边界（明确不做）

| 不做 | 理由 |
|------|------|
| `repository.Event` 结构体加 version 字段 / `object_events` schema 改造 | 方向文原文 "@1.1 必须加到 envelope"——版本化在 outbox 载荷（WIP 已落地 `schema_version`）；遗留流保持兼容（E7） |
| `notify@1.1`（S3 通知面）与 share/asset 失效（方向 #3） | sibling 方向；WIP 已有 notify relay，本规格不对其提要求 |
| `auditgovernance` 机制改动（绑定表/revision/draining/redacted 形状复用） | 既有 governance 是租户绑定 + 脱敏面；L2 是新端口面（E6 修正点） |
| `RecordAudit` 签名/`audit_log` 表结构改动 | FR-1 用 tx 内插入变体，不碰既有单写方法（admin 路径不受影响） |
| `DeleteVersion`/delete marker/隔离保留清除路径的 outbox 化 | 本方向锚定 `FileService.Delete`（AC-1/AC-5）；其余是同一模式的机械扩展 |
| Webhook 管线 / `webhook_failures` 改造 | 既有 durable retry + DLQ；L2 是独立投递面 |
| L2 绑定表的持久化/管理 API | 先以配置驱动（每租户 URL/token）；若需动态管理，属后续方向（届时走 0042 迁移双文件对） |
| actor 身份管线（鉴权之外的新身份注入） | actor 取 `access.PrincipalFrom(ctx)`，空值合法（WIP 既有决定） |

---

## 6. 实现指引（供验收后落地，非本规格交付物）

- **Repo**（扩展 `internal/repository/event_outbox.go` 或新增 `audit_sink_tx.go`，≤500 行）：`HardDeleteObjectWithEvent`/`SoftDeleteObjectWithEvent` 增 `entry AuditEntry` 参数，`audit_log` INSERT 并入既有事务（`INSERT … VALUES (…)` 形状对齐 `audit.go:14-22`，`CreatedAt` 空则盖 RFC3339Nano）；签名变更同步 `repository_interface.go` 与 `file_delete.go` 调用点（`deleteFacts` 旁新增 `deleteAuditEntry(ctx, obj, tenant)`）。
- **Payload**（`internal/events/payload.go`）：`deletedFact` 增 `ObjectID int64 \`json:"object_id"\``；`BuildDeletedFact` 签名增 `objectID`（或从 `obj` 取）；`schema_test.go` golden/必填字段更新（AC-4）。
- **Relay**（`internal/events/event_outbox_relay.go` 或新 `audit_sink_l2.go`）：`deliverFact` 的 deleted@1.1 分支改为查 L2 绑定 → POST（token 取自配置，镜像 `auditgovernance/http.go` 的 `Publisher`：Bearer + receipt 校验）→ complete；未绑定 → 现状 complete。退避/重试/claim-lost 全部复用 WIP 路径。
- **装配**（`cmd/server` + `internal/config`）：L2 配置键（`AUDIT_SINK_L2_*`：enabled/base URL/token URL/每租户 binding）进 `config.go` + `docs/configuration.md`；relay 构造时注入 L2 client（nil → 降级记录保留）；**不改** `auditgovernance` 装配。
- **测试**：AC-1 扩展 `event_outbox_test.go`；AC-2/AC-4 扩展 `event_outbox_relay_test.go`/`schema_test.go`；AC-3/AC-5 新 e2e（httptest 全服务器）；AC-6 全量回归。
