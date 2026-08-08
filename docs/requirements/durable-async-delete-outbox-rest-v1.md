# 方向：删除事件事务性 outbox（durable_async）——`vault.file.deleted@1.1` 审计 + `vault.file.notify@1.1` 通知（internal/api/rest 模块验收契约）

> **模块：** `internal/api/rest`（组合面：`internal/service` + `internal/repository` + `internal/events` + `cmd/server`）· **来源分析：** `docs/auto/analyses/internal-api-rest-8e390260.json`（方向 1）· **日期：** 2026-08-06
> **评分：** 价值 10 / 风险降低 9 / 工作量 7 / 置信度 8
> **验证基准：** 工作树 = HEAD `acfaaf4` + **未提交 WIP**（quarantine 批次：`reason`/`signature` 载荷字段、`SoftDeleteObjectByIDWithEvent`、`QuarantineObjectByID(signature)`，见 §2.3）。本文所有引用均已对照该基准逐行验证；实测 `go build ./...` 退出码 0、`gofmt -l` 无输出、关键包测试全绿（§2.3）。
>
> **本文是增量验收规格：** 方向 acceptance 要求的机制（删除+审计+outbox 事实单事务、claim/lease/retry relay、@1.1 自包含 schema、durable_async 不阻塞）**已在 round-1/round-2 campaign 落地**（提交 `fb74b19`、`4cca6db`）。本文的职责 = ①逐条验证方向文 8 条引用并修正已过时主张；②把 4 条 acceptance 映射为**可执行测试**（现状已覆盖项 + 2 条真实缺口 G1/G2）；③钉死范围边界。**不是绿地设计。**

---

## 1. 问题陈述（方向文 vs 仓库现状）

方向文写于分析快照（基于 `docs/auto/analyses/internal-api-rest-8e390260.json`），其问题描述的核心主张在**当前仓库已不成立**：

| 方向文主张 | 现状（已验证） |
|-----------|---------------|
| "FileService.Delete 在 DB commit 之后、经独立 `InsertEvent` 写才发出 EventDeleted，错误被吞掉" | 该描述对**遗留 `object_events` 流**仍成立（`s.emit` 仍在删除事务提交后执行、错误吞掉），但**权威持久路径已转移**：删除事务现在 = 元数据删除 + `audit_log` 行 + 两条 outbox 事实（`deleted@1.1`/`notify@1.1`）**单事务提交**（`HardDeleteObjectWithEvent`/`SoftDeleteObjectWithEvent`，`event_outbox.go:102-184`）；`s.emit` 退化为本地广播（SSE/indexer/webhook 等既有订阅者，E2 语义不变） |
| "no such generic outbox for file events exists" | **已过时**：`internal/repository/event_outbox.go`（已提交）是完整的事件 outbox（表 `event_outbox` + `event_outbox_delivered`，迁移 `0041_event_outbox.{up,down}.sql` 双方言） |
| "The event/audit schemas for @1.1 … are proposed" | **已过时**：`internal/events/payload.go` 的 `BuildDeletedFact`/`BuildNotifyFact` 已实现自包含 @1.1 载荷，golden 字节测试钉死（`schema_test.go`） |
| "admin audit is equally best-effort with swallowed errors (admin.go:410-428)" | **仍成立**（`auditForTenant` 的 `_ = h.repo.RecordAudit(...)`）——但那是 **admin 操作审计**面，删除审计已走事务路径（`audit.go:13` `AuditActionFileDelete`），admin 面改造属**方向 3**，不在本方向范围（§5） |

**仍然真实的问题残余（本方向验收要钉死的语义）：**
1. 删除**响应路径**与投递进度必须零耦合（durable_async）——已由 `TestDeleteResponse_DoesNotBlockOnDelivery` 信号式钉死（§4 AC-1b）；
2. **组合面 e2e**：`DELETE /v1/files/{key}` → 两条事实各**恰好一次**投递、崩溃（租约过期/进程重启）→ 重投不重复——`deleted@1.1`→L2 的 e2e 已存在，**`notify@1.1` 的 REST e2e 与进程级重启 e2e 是真实缺口**（§4 G1/G2）。

---

## 2. 现状与代码证据（方向文 8 条引用逐条验证）

### 2.1 验证表

| # | 方向文引用 | 验证结果（行号以当前基准为准） |
|---|-----------|------------------------------|
| E1 | `internal/service/file_delete.go:51,90,161`（`s.emit` after delete） | ✅ 行号精确：`hardDeleteObject` 的 `s.emit(ctx, obj, repository.EventDeleted)` 在 :51（`HardDeleteObjectWithEvent` 提交之后）、`softDeleteObject` 在 :90（`SoftDeleteObjectWithEvent` 之后）、`DeleteVersion` 在 :161。**语义修正：** `deleteAuditEntry`（:100-113）+ `deleteFacts`（:123-137，构造 `EventTypeFileDeleted11`/`EventTypeFileNotify11` 两条 `OutboxFact`）在删除**之前**构建，随删除**同一事务**提交；`s.emit` 仅剩本地广播 |
| E2 | `internal/service/file.go:297`（emit 最小 payload） | ✅ 行号精确：`emit` :297-314，payload = `{backend, size, etag, content_type}`；sink 错误吞掉（:312-313 注释 *"lifecycle events are best-effort and must never break a user request"*）。**对遗留 `object_events` 流仍成立**；outbox 事实携带丰富 @1.1 载荷 |
| E3 | `internal/events/bus.go:80-104`（Publish: async, non-atomic, errors swallowed） | ✅ 行号精确：`Publish` :80-104 = `repo.InsertEvent`（:84）→ `broadcast`（:95）→ transport（:100-103）；错误仅 `logger.Warn`（:86-88、:101-103）。非原子、不阻塞——遗留流语义原样保留 |
| E4 | `internal/api/rest/admin.go:410-428`（best-effort audit） | ✅ 行号精确：`audit` :410-413、`auditForTenant` :414-428，`_ = h.repo.RecordAudit(...)`（:421）错误吞掉。**范围说明：** 这是 admin 操作审计（tenant/key/jwt 变更），删除审计已由 E1 的事务路径覆盖；admin 面经 outbox 投递属方向 3 |
| E5 | `internal/repository/audit_governance_write.go:13-79`（事务化 audit+outbox 先例） | ✅ `RecordAuditWithGovernance` :14-43（BeginTx → `INSERT audit_log` RETURNING id → `insertAuditGovernance` → Commit）、`InsertEventWithGovernance` :45-79（同形状）——"业务行+outbox 事实同事务"先例成立，事件 outbox 复用其形状 |
| E6 | `internal/repository/audit_governance_claim.go`（claim/attempts/lease 投递） | ✅ `ClaimAuditGovernance` :17-33（owner+token+revision+lease 栅栏）、`CompleteAuditGovernance` :102-110、`RetryAuditGovernance` :112-125、`requireGovernanceClaim` :127-136——事件 outbox 的 claim/complete/retry 镜像此形状 |
| E7 | `internal/auditgovernance/relay.go:33-69`（reconcile/deliver 循环） | ✅ `reconcile` :33-69（gap 扫描 → `EnqueueAuditGovernance`）；`deliverBatch` :71-85、`deliverFact` :87-98、`retryFact` :100-117、`boundedBackoff` :130-145（2× 指数、max 封顶、±25% jitter）——事件 relay 的退避形状来源 |
| E8 | `internal/repository/billing_outbox.go`（第二 outbox 先例） | ✅ `ClaimBillingUsage` :8-25（**status 形状**谓词：`(pending AND next_attempt_at_ns<=now) OR (inflight AND claim_until_ns<=now)`，:24-27）——`event_outbox` claim 谓词直接采用此形状（`event_outbox.go:251-264`），区别于 audit 的 `delivered_at_ns=0` 形状 |

### 2.2 方向文主张修正（已过时项）

| # | 主张 | 修正（证据） |
|---|------|-------------|
| C1 | "no such generic outbox for file events exists" | ❌ 已存在：`event_outbox.go` 全量实现——`OutboxEventType`（:18-31，`vault.file.deleted@1.1` :22 / `vault.file.notify@1.1` :25）、`OutboxFact`（:37-48）、`validateOutboxFacts`（:61-79，要求 `schema_version=="1.1"` + ≤1MiB）、`HardDeleteObjectWithEvent`（:102）、`SoftDeleteObjectWithEvent`（:147）、`SoftDeleteObjectByIDWithEvent`（:186，WIP）、`ClaimEventOutbox`（:251）、`CompleteEventOutbox`（:336，同事务写 `event_outbox_delivered`）、`RetryEventOutbox`（:364，`attempts>=maxAttempts` → `failed` 终态）、`PruneEventOutbox`（:393）、`HasEventOutboxFact`（:437）；迁移 `0041_event_outbox.{up,down}.sql`（sqlite+postgres 双份，I2） |
| C2 | "@1.1 schemas are proposed" | ❌ 已实现：`payload.go` `deletedFact`（:33-52）/`notifyFact`（:54-76）/`notifyRecord`（:79-106）+ `BuildDeletedFact`（:109-127）/`BuildNotifyFact`（:137-160）；`newSequencer`（:21-27，`crypto/rand` 16B hex，测试注入固定值）；golden 字节钉死 `schema_test.go:31-132` |
| C3 | "delete 事件的持久化是第三个非原子事务" | ❌ 已消除：WithEvent 单事务（E1）；崩溃窗口（删除提交后、事件提交前）已不存在——outbox 事实与删除行同生共死 |

### 2.3 当前可执行状态（实测）

- `go build ./...` → 退出码 0；`gofmt -l internal/{events,repository,service,api/rest} cmd/server` → 无输出。
- `go test ./internal/repository/ ./internal/events/ ./internal/service/` → 全部 `ok`（缓存命中，基线无回归）。
- `go test ./internal/integration/ -run 'TestDeleteResponse_DoesNotBlockOnDelivery|TestComposition_AuditSinkL2BoundTenant' -count=1` → `ok 2.146s`（AC-1b/AC-4 的 deleted@1.1 e2e 实测通过）。
- WIP（quarantine 批次，未提交）：`payload.go` 增 `reason`（deleted@1.1，值域如 `"av_infected"`）/`signature`（notify@1.1）；`SoftDeleteObjectByIDWithEvent`（`event_outbox.go:186-239`）；`object_worker.go` `QuarantineObjectByID(ctx, id, signature)` 改走 WithEvent——**本规格不对此批次提要求**（§5），但 `reason` 字段已纳入 AC-3 断言（见 §4 修订）。

---

## 3. 需求规格（FR 已落地；此处为验收契约与语义钉死）

### FR-1：删除行 + audit_log + 两条 outbox 事实单事务原子（已满足）

`FileService.Delete`（硬删/软删，`file_delete.go:143-166`）经 `deleteAuditEntry` + `deleteFacts` → `HardDeleteObjectWithEvent`/`SoftDeleteObjectWithEvent`（`event_outbox.go:102-184`）：
- 单事务 = legal-hold 检查 + access-state 清理 + `DELETE FROM objects`（硬删，零行 → `ErrNotFound` 回滚，**无幽灵事实**）/ `UPDATE deleted_at`（软删，`n==0` → `ErrNotFound`）+ `insertAuditEntry`（`audit.go:21-30`，`Action="file.delete"`，`Detail="hard"/"soft"`）+ `insertOutboxFacts`（两条事实，`event_outbox.go:229-238`）。
- 事务内任一失败（事实校验 `event_outbox.go:61-79`、约束冲突）→ **整体回滚**：对象行不删、audit 不落、outbox 不落（AC-1a 断言）。
- REST 组合面（本模块）：`handler.go:239-250` `Delete`（`?hard=1`，bucket policy 检查后）→ `svc.Delete`；`batch_handlers.go:22-41` `BatchDelete` → `svc.BatchDelete`（`file_features.go:164-178`，逐 key 调 `s.Delete(..., false)` 软删）→ **继承同一事务语义**（非新面，无绕过）。
- 删除提交后 `s.emit` 照旧（本地广播；Notifier 侧按 `HasEventOutboxFact` 条件跳过 notify 面重复投递，`notifier.go:73-74`）。

### FR-2：@1.1 自包含 schema（已满足；含一处方向文字段修订）

- **`vault.file.deleted@1.1`**（`deletedFact`，字段序 = 字节稳定）：`schema_version:"1.1"`、`event_type`、`tenant`、`bucket`、`key`、`object_id`（= `obj.ID`）、`version_id`、`size`、`etag`、`backend`、`request_id`、`actor`（`access.PrincipalFrom(ctx)`，空值合法——不新增身份管线）、`reason,omitempty`（WIP 值域如 `"av_infected"`；omitempty 保 REST 路径 golden 字节不变）。
- **`vault.file.notify@1.1`**（`notifyFact` + `notifyRecord`）：S3 通知形状，`records[0]` 自包含 `eventVersion:"2.1"`/`eventSource:"aws:s3"`/`eventName:"s3:ObjectRemoved:Delete"`/`userIdentity.principalId`/`s3.object.{key,size,eTag,versionId,sequencer}`——**全部在 emit 时刻捕获**（E7 修复：投递期不再重派生）；`sequencer` = `crypto/rand` 16B hex（**不可取 `obj.ID`**——`RestoreObject` 原地 UPDATE 复用行 id，restore→re-delete 会重复，`TestEventSchema_SequencerUniquePerCall` 钉死）。
- **无 sibling 项目引用（不变量）：** `payload.go` import 仅 stdlib（`crypto/rand`/`encoding/hex`/`encoding/json`）+ `internal/repository`（已核）；`"aws:s3"`/`"arn:aws:s3:::"`/`"us-east-1"` 是 S3 协议形状字面量，非 sibling 标识。golden 字节测试（AC-3）从字节层钉死该不变量。
- **⚠️ 方向文 acceptance 字段修订（`occurred_at`）：** 方向文 AC-3 列 `occurred_at`——**已核实不在 @1.1 信封内**（`payload.go`/`event_outbox.go` 全文无此字段；`occurred_at_ns` 仅存在于 sibling 的 `audit_governance_outbox` 与 `billing_usage_outbox` 表，`audit_governance_claim.go:14`、`billing_usage.go:13`）。事件 outbox 的时间语义由**行级列**承载：`created_at_ns`/`available_at_ns`（插入时，`event_outbox.go:244-248`）与 `delivered_at_ns`（complete，:340-353）。**验收断言相应修订**：信封字段按实现集断言（含 `reason`），`occurred_at` 改为断言 outbox 行的时序列（AC-3 ②）。不在信封内加 `occurred_at` 字段——那属 schema 演进，超出本方向（§5）。

### FR-3：异步 relay —— claim → deliver → complete，租约重 claim 即崩溃恢复（已满足）

`EventOutboxRelay`（`event_outbox_relay.go:71-368`，装配 `cmd/server/workers.go:158-180`，**常开**——删除原子性不 gate）：
- **claim**（`ClaimEventOutbox`，status 形状，E8）：`(pending AND available_at_ns<=now) OR (inflight AND lease_expires_at_ns<=now)`；owner+token+lease 栅栏；排序 best-effort；短批次容忍。
- **分发**（`deliverFact` :171-188）：`deleted@1.1` → `deliverDeleted` :190-214（AuditSink L2 绑定租户 → POST 完整信封；`ErrSinkNotBound` → complete+计数；401/403 → 立即 `failed`；5xx/传输 → 退避）；`notify@1.1` → `deliverNotify` :236-270（按既有 bucket 通知规则，**载荷 byte-exact** 投递，`parseNotifyPayload` 仅取元数据）。
- **complete**（:305-318）：同事务 `status='delivered'` + 插 `event_outbox_delivered`（键 = outbox 行 id）→ **complete 后恰好一次**；**deliver→complete 窗口显式 at-least-once**（S3 等价语义）。
- **retry**（:320-341）：`eventOutboxBackoff` :343-358（2×、上限 5min、±25% jitter 形状对齐 E7）；`attempts>=maxAttempts` → `failed` 终态；**claim-lost → warn + telemetry，无循环内重试**（租约重 claim 是恢复机制）。
- **配置**（`config_event_outbox.go`）：`EVENT_OUTBOX_POLL_INTERVAL_MILLIS`(1000)/`BATCH_SIZE`(32)/`CLAIM_TTL_SECONDS`(30)/`HTTP_TIMEOUT_SECONDS`(5)/`MAX_ATTEMPTS`(10)；校验 `CLAIM_TTL > 2×HTTP_TIMEOUT`（:67，防无崩溃并发重复 POST）。

### FR-4：durable_async —— 删除响应永不等待投递（已满足并已钉死）

relay 独立 goroutine；删除事务只插行、不调用任何投递。`TestDeleteResponse_DoesNotBlockOnDelivery`（`fullserver_test.go:665-766`）用**信号式判别**（L2 目标阻塞 + 4s 挂起守卫 < 5s relay timeout——同步实现不可能在 release 前返回）钉死：响应返回时 outbox 行必须 `pending|inflight`（`delivered` 不可达）、`audit_log` 行已存在；恢复目标后 relay 完成投递。

---

## 4. 验收标准（可测试；逐条映射方向 acceptance，含修订与缺口）

> 测试基建（已验证）：`repository.Open("sqlite","file:…")`+`Migrate`（`event_outbox_test.go` 先例）；relay 测试 `internal/events/event_outbox_relay_test.go`（httptest 目标 + 注入 sequencer/token）；组合 e2e `internal/integration/fullserver_test.go`（`startFullServerWithRelay` + `outboxStatus` 轮询 + `waitForBodies`）。

### AC-1 单事务原子 + durable_async（方向 acceptance ① —— 二选一分支，**现状两条都满足**）

方向文允许二选一："outbox 行插入失败与元数据删除同事务回滚" **或** "outbox 写留在 commit 后 + 请求路径永不等待投递"。现状**同时满足两者**（in-tx + 独立 worker）：

- **AC-1a（同事务回滚）** → `TestDeleteObjectWithAudit_OneTx`（`event_outbox_test.go:71-134`）：有效路径硬删 → `GetObject`=ErrNotFound、`event_outbox` 恰 2 行（deleted@1.1 + notify@1.1）、`audit_log` 恰 1 行；**强制回滚**：非法事实（`event_type` 不在允许集 → `validateOutboxFacts` 报错）→ 方法返回 error、对象行仍在、outbox 0 行、audit 无新增。`TestDeleteObjectWithEvent_OneTx`（:136-218）软/硬删同形状；`TestSoftDeleteObjectByIDWithEvent_OneTx`（:398-480，WIP）按 id 变体。
- **AC-1b（durable_async）** → `TestDeleteResponse_DoesNotBlockOnDelivery`（`fullserver_test.go:665-766`）：REST `DELETE /v1/files/{key}?hard=1` 在 L2 目标阻塞期间返回 204；outbox 行 `pending|inflight`；恢复后 ≤15s 内 `delivered` 且 L2 收到 ≥1 POST（FR-4）。

### AC-2 relay claim/retry/dedup（方向 acceptance ② —— 镜像 `audit_governance_test.go` 模式）

→ `audit_governance_test.go` 模式源：`TestAuditGovernanceAtomicCaptureAndClaimFencing`（:45）、`TestAuditGovernanceAtomicFailureRollsBackLocalAudit`（:83）。事件 outbox 对应实现：

- claim owner/attempts/lease：`TestEventOutboxClaimCompleteLifecycle`（`event_outbox_test.go:220-258`，claim 置 owner+token、attempts+1、complete 后 re-claim 空）。
- **租约过期 → re-claim（崩溃恢复）**：`TestEventOutboxClaimLeaseExpiryRedelivers`（:259-299，owner-A 短租约不 complete → owner-B 重新取到同一行）。
- retry/终态：`TestEventOutboxRetryBackoffAndTerminalFailed`（:300-354，5xx 后 attempts 递增、`available_at_ns>now`、退避 ∈ 边界、`attempts>=max` → `failed` 不再可 claim）；`TestEventOutboxBackoffBounds`（`relay_test.go:298`）。
- 去重/claim-lost：`TestOutboxRelay_DeliveryLifecycle`（`relay_test.go:144-179`，**byte-exact** 投递 + complete 后 re-claim 空 + 第二批不重投）；`TestOutboxRelay_ClaimLostLeadsToReclaimNotDoubleSchedule`（:229-271，complete/retry 栅栏失败 → 不双调度）。

### AC-3 schema 测试（方向 acceptance ③ —— 修订后断言实现字段集）

→ `internal/events/schema_test.go`（生产构建函数输出，非手写 JSON）：

- `TestEventSchema_GoldenJSON`（:31-40）：golden 常量**逐字节**钉死两条信封（含 `object_id:42`、notify `records[0]` 全自包含 + 固定 sequencer）。
- `TestEventSchema_RequiredFields`（:42-94）：deleted@1.1 必填 = `schema_version/event_type/tenant/bucket/key/object_id/version_id/size/etag/backend/request_id/actor`（方向文 acceptance 的 tenant/bucket/key/actor/version refs 全含；**`reason` 为 omitempty 可选**，WIP 值域由 quarantine 批次测试覆盖）+ 无 `records`；notify@1.1 `records[0].s3.object.{key,size,eTag,versionId,sequencer}` 全部存在且匹配（**自包含**）。
- `TestEventSchema_Deleted11Envelope`（:96-130）：JSON-schema 风格身份断言（object_id==obj.ID、actor、tenant/bucket/key）。
- `TestEventSchema_SequencerUniquePerCall`（:132-143）：同进程两次构建 sequencer 必不同（restore→re-delete 不重复，D6）。
- **修订记录（`occurred_at`）**：见 §FR-2——信封内**无** `occurred_at`；时序断言改为 outbox 行列：AC-1a 断言 `created_at_ns/available_at_ns` 非零（插入时刻），AC-1b 断言 `delivered_at_ns` 在响应之后（已由 `outboxStatus` 的 `pending|inflight` 断言覆盖）。
- **无 sibling 引用（可测试）**：golden 字节测试已从字节层排除任何 sibling 标识；review 级断言 = `payload.go` import 面仅 stdlib + `internal/repository`（§2.3 已核，新增测试为 `go list -deps` 检查可选，不强制）。

### AC-4 组合 e2e（方向 acceptance ④ —— 已覆盖 1 条、**缺口 2 条**）

- **已满足（deleted@1.1 e2e）**：`TestComposition_AuditSinkL2BoundTenant`（`fullserver_test.go:768-842`）：REST PUT→DELETE（tenant t1）→ 轮询 → L2 收到**恰 1 次** POST、载荷含 `"event_type":"vault.file.deleted@1.1"`/`"tenant":"t1"`/`"object_id":<obj.ID>`；`audit_log` 有 t1 行；未绑定租户 t2 → L2 零 POST 但 audit 照常（L0 per-tenant always-on）；无 L2 配置的对照服务器 → 删除 2xx + relay 正常 complete（记录保留降级）。
- **缺口 G1（本规格要求补齐）—— REST DELETE → notify@1.1 投递 e2e 不存在**：单元级 `TestOutboxRelay_DeliveryLifecycle`（`relay_test.go:144`）已证明 notify 的 byte-exact 投递 + complete 后恰好一次，但**无 integration 级** REST→notify 组合断言（`fullserver_test.go` 无 `notify@1.1` 引用，已核）。要求（§6 实现指引）：
  ```go
  // internal/integration/fullserver_test.go
  func TestComposition_DeleteDeliversBothFacts(t *testing.T) {
      // 1) 装配：startFullServerWithRelay（默认 options）+ 通知目标
      //    （setDeleteRule 形状，httptest）+ AuditSink L2（绑定 "default"）
      // 2) PUT /v1/files/k → DELETE /v1/files/k?hard=1 → 204
      // 3) 轮询 DB（outboxStatus helper）：deleted@1.1 与 notify@1.1 均 'delivered'
      // 4) 通知目标收到 notify@1.1 POST 恰 1 次，body == event_outbox.payload 原样
      //    （byte-exact，含 records[0].s3.object.sequencer == 行内 payload）
      // 5) L2 收到 deleted@1.1 POST 恰 1 次（AC-4 信封字段）
      // 6) 再等一个 poll 周期：两目标计数不变（complete 后恰好一次）
  }
  ```
- **缺口 G2（本规格要求补齐）—— 进程级崩溃重启 e2e 不存在**：租约过期重 claim 已有单元级证明（`EventOutboxClaimLeaseExpiryRedelivers`，机制 = 崩溃恢复原语：claim 落 `inflight`+租约，重启后 `lease_expires_at_ns<=now` → 重新 claim），但**无"kill/restart 进程"级 e2e**。要求（§6）：
  ```go
  // internal/integration/fullserver_test.go
  func TestComposition_MidClaimRestartRedeliversOnce(t *testing.T) {
      // 1) 服务器 A：PUT → DELETE（目标配置为一直 500 → 事实停在 inflight/重试）
      //    ——或更直接：用 PollInterval=time.Hour 的 relay 完成 DELETE（事实 pending）
      // 2) 模拟崩溃：停掉服务器 A（保留同一 SQLite DB 文件）
      // 3) 同 DB 启动服务器 B（同 owner/新实例；claim 谓词命中
      //    'inflight AND lease_expires_at_ns<=now' 或 'pending'）
      // 4) 目标恢复 200 → notify 目标 + L2 各收到恰 1 次 POST（无重复）
      // 5) 两事实 status=='delivered'；再次重启/轮询 → 无新 POST
  }
  ```
- **make check 绿**：基线要求 `gofmt`/`go build`/`go vet`/`go test ./...`（SQLite+local，零网络）。现状实测：build 0、gofmt 干净、`repository/events/service` 全 ok、integration 两个既有 e2e `ok 2.146s`（§2.3）。G1/G2 落地后全量复跑。

---

## 5. 范围边界（明确不做）

| 不做 | 理由 |
|------|------|
| `admin.go:410-428` admin 审计面改造（经 outbox 投递） | 方向 3（admin 文件删除能力）的范围；本方向只锚定对象删除事件的 outbox |
| `DeleteVersion`（`file_delete.go:161`）、delete marker（`delete_marker.go:58`）、保留清除路径的 outbox 化 | 非 `FileService.Delete` 锚点；同一 WithEvent 模式的机械扩展，属后续方向 |
| quarantine 批次（`object_worker.go`/`SoftDeleteObjectByIDWithEvent`/`reason`/`signature`，未提交 WIP） | sibling campaign（`quarantine-outbox-producer-v1`）；本规格仅在其字段上做 AC-3 断言，不提行为要求 |
| `@1.1` 信封增加 `occurred_at` 字段 | 方向文 acceptance 所列字段经核不在实现内；时间语义由 outbox 行级列承载（FR-2 修订）。加字段 = schema 演进，超出本方向 |
| `repository.Event`/`object_events` 流改造（payload 结构化、合并双份持久化） | 遗留流是 SSE 回放/本地广播的既有行为（E2/E3）；outbox 事实已携带自包含 JSON |
| Webhook 管线 / 通知规则引擎（`GetBucketNotifications`/目标解析）改动 | 既有 durable retry + DLQ；本方向只覆盖 outbox 事实的生成与 relay 投递 |
| 新迁移 / 新 `go.mod` 依赖 | 0041 已落地；纯 stdlib（I6） |

---

## 6. 实现指引（仅两条缺口；其余验收均已由现状测试满足）

- **G1**（`internal/integration/fullserver_test.go` 新增 `TestComposition_DeleteDeliversBothFacts`）：复用 `startFullServerWithRelay`（:44 起既有 helper，relay options 注入 `AuditSink`）+ `outboxStatus`/`waitForBodies` 轮询 helper；通知目标按 `setDeleteRule` 形状（`relay_test.go` 先例）配置 `?notification` 规则；断言 notify body 与 `event_outbox.payload` **byte-exact**（relay 的 verbatim 不变量，`deliverNotify` :236-270）。
- **G2**（`internal/integration/fullserver_test.go` 新增 `TestComposition_MidClaimRestartRedeliversOnce`）：两个服务器实例共享同一 SQLite DSN 文件；用 `PollInterval: time.Hour` 的 relay 完成"DELETE 后事实滞留 pending"的崩溃窗口模拟（或短 `ClaimTTL` 制造 inflight+过期）；实例 B 以 `PollInterval: 50ms` 接管同一 DB → 断言重投恰 1 次。若共享 DSN 的 `startFullServerWithRelay` 有 fixture 冲突，改在同一 test 内显式 `repository.Open` + `events.NewEventOutboxRelay` + httptest 服务器（不要求复用 helper，要求同 DB 文件 + 真实 relay 实例）。
- 落地后：`go test ./internal/integration/ -count=1` + `make check` 全量复跑（§4 AC-4 make-check 项）。
