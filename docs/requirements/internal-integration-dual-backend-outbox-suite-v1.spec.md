# 方向：双后端 outbox 投递 + `vault.file.deleted@1.1` schema 一致性验收套件（`internal/integration` 模块 · 验收规格 · 已验证现状）

> **模块：** `internal/integration`（+ `internal/repository` 事务/claim 侧 · `internal/events` 构建/relay 侧 · `internal/auditgovernance` relay 侧）
> **来源分析：** `docs/auto/analyses/internal-integration-7479f0a2.json`（方向 #2）· **日期：** 2026-08-06 · **HEAD：** `acfaaf4`
> **评分：** 价值 9 / 风险降低 9 / 工作量 6 / 置信度 9
> **状态声明：** 方向的问题陈述**部分已过时**——`vault.file.deleted@1.1` 的 schema 锁定、SQLite 门禁内的 claim/complete/cleanup、以及“relay 故障不阻塞删除”的组合证明**均已存在**（`internal/events` 侧，见 §2/§3）。方向把**两个并行 outbox**（审计治理 outbox 与事件 outbox）混为一谈；本文按仓库现状拆分核验，**原样保留四条验收检查**并逐条映射到已存在测试或登记为真实缺口（§4 未覆盖项即本套件的交付物）。超范围项一律不做（§5）。
> **前置文档：** `docs/requirements/transactional-outbox-delete-events-v1.spec.md`（事件 outbox 本体规格，已实现合入）；`docs/requirements/transactional-outbox-kernel-v1.md`。

---

## 1. 证据核验（方向引证逐条对照当前 HEAD）

| # | 方向引证（分析时行号） | 当前 HEAD 位置 | 核验结论 |
|---|----------------------|----------------|---------|
| E1 | `internal/integration/audit_governance_postgres_test.go`（`TestPostgresAuditGovernanceConcurrentClaimsAndLeaseRecovery`、`lockGovernanceRows` SKIP LOCKED、`CompleteAuditGovernance`、`CleanupDeliveredAuditGovernance`、`ListAuditGovernanceGaps`） | 文件存在，`:1` 为 `//go:build integration`；测试 `:18-80`；`lockGovernanceRows` `:130`（**`FOR UPDATE`，非 SKIP LOCKED**）；`ClaimAuditGovernance` 的 SKIP LOCKED 在 `internal/repository/audit_governance_claim.go:31-49`（PG 分支 `FOR UPDATE OF o SKIP LOCKED`） | ✅ 引证成立，细节修正：测试内的行锁用 `FOR UPDATE`，claim 谓词内嵌 `SKIP LOCKED`。**所有 seed 事实 `Action:"tenant.status"`（:121/:123）——无任何 delete 起源行**（G2b 缺口） |
| E2 | `internal/auditgovernance/repository.go`（`WrapRepository`、`RecordAuditWithGovernance`、`InsertEventWithGovernance`） | `WrapRepository` :15-21（nil-safe）；`RecordAudit` :27-38 → `RecordAuditWithGovernance`；`InsertEvent` :40-55 → `InsertEventWithGovernance` | ✅ 全部存在。**注意：`RecordAuditWithGovernance` 强制 `fact.OriginKind = AuditOriginAdmin`（`audit_governance_write.go:30`）**——delete 的 L0 审计行由 `HardDeleteObjectWithEvent` 直插（`event_outbox.go:102-145`），**不经** `RecordAudit`；delete 起源治理行只经 `InsertEventWithGovernance`（bus `Publish` → 包装后的 `InsertEvent`） |
| E3 | `internal/auditgovernance/relay.go`（claim/lease/gap rebuild） | `reconcile` :16-56（gap 重建：`ListAuditGovernanceGaps` + `EnqueueAuditGovernance`）；`deliverBatch` :59-87（claim→per-fact goroutine→complete）；`retryFact` :124-138（`boundedBackoff` :163-186）；`failFact` :111-122（终态，Contract A）；`cleanupDelivered` :139-166 | ✅ 全部存在 |
| E4 | `internal/repository/audit_governance_{types,write,claim,cleanup,binding}.go` | 五文件俱在（95/273/209/141/160 行） | ✅ 存在。`types.go:49-96` 定义 `AuditGovernanceStore` 全接口；`write.go:96-130` `EnqueueAuditGovernance` 幂等（`ON CONFLICT (origin_kind,origin_id) DO NOTHING` + `WHERE NOT EXISTS` tombstone）；`claim.go:16-29` 按方言分派 |
| E5 | `internal/events/bus.go` `Publish` | `Publish` :80-98（先 `repo.InsertEvent` 持久化 :84，再非阻塞广播 :89；错误仅 warn，**绝不向调用方传播** :85-89） | ✅ 引证成立（行号 104→80 漂移）。方向所述“先持久化再广播”属实 |
| E6 | `internal/repository/repository.go:175-199` — `Event` 无版本字段，type 仅 created/updated/deleted/accessed | `// Event lifecycle event.` :174；`Event` :175-184；`EventType` :187-194（四常量） | ✅ 引证成立（行号精确）。**版本化命名空间不在 `Event` 上**，而在 `event_outbox.go:19-26` 的 `OutboxEventType`（`EventTypeFileDeleted11 = "vault.file.deleted@1.1"`、`EventTypeFileNotify11 = "vault.file.notify@1.1"`）与 payload JSON 的 `schema_version` 字段——方向将此表述为“Event 无版本字段”，语义上正确但机制上指向了错误载体（见 E7） |
| E7 | （补充核验）方向隐含假定：`vault.file.deleted@1.1` 由 `RecordAuditWithGovernance` 写入 | 实为**两个并行 outbox**：① `event_outbox`（迁移 0041，`event_outbox.go`，**承载** `vault.file.deleted@1.1`/`notify@1.1` 原始自包含 payload）；② `audit_governance_outbox`（迁移 0039，**只存红actored 摘要**——`types.go:28-30` 明示 “Raw audit details and object paths never enter this table”，bucket/key 经 HMAC 进 `target_digest`） | ⚠️ **方向概念混淆（关键修正）**：“一行 outbox 带全部 `vault.file.deleted@1.1` 字段”只可能指 `event_outbox`；审计治理行天然不带原始字段（§3 AC-1 按此拆分） |
| E8 | `internal/repository/audit_governance_test.go` — “SQLite 单测仅覆盖通用 fact” | 文件存在（371 行），全部 SQLite 门禁内：`TestAuditGovernanceAtomicCaptureAndClaimFencing` :45（`file.created`）、`TestAuditGovernanceDeliveredCleanupLeavesOriginTombstone` :300（`key.add`）、`TestAuditGovernanceReconcileFindsAndDeduplicatesLocalFacts`（gap 路径含 `EventDeleted`） | ✅ **引证成立**：无任何“delete 起源行”（`Action="file.deleted"`、`OriginKind=file`）的 写→claim→complete→cleanup 闭环测试（G1/G2a 缺口） |
| E9 | （补充核验）方向断言“无 schema 一致性/投递测试”“无 relay 故障下删除延迟不受影响的测试”“无 SQLite 门禁 delete 起源 claim/complete/cleanup 覆盖” | 见 §2 —— 对 **`event_outbox` 全部已实现**（`schema_test.go`、`event_outbox_test.go`、`admin_files_delete_test.go`、`fullserver_test.go`）；对 **`audit_governance_outbox` 的 delete 起源行**仍为真；对 **`event_outbox` 的 Postgres 分支**仍为真（SKIP LOCKED 路径零 PG 覆盖） | ⚠️ **问题陈述部分过时**：真实缺口收敛为 §4 的 G1–G4 四项 |

**方向问题陈述核验：** “durable_async 机制已实现、Postgres 仅在 opt-in tag 下验证（`tenant.status`）”——**属实且是缺口核心**：Postgres 侧唯一 outbox 测试只走通用 fact，delete 起源行与 `event_outbox` 的 PG claim 分支（`event_outbox.go:266-296` SKIP LOCKED）**零覆盖**。“`repository.Event` 无版本字段”——属实，但版本化事实在 `OutboxFact.Payload`（§3 AC-3 的锁定对象）。

---

## 2. 已落地机制（验收对象基线，全部实测通过）

### 2.1 事件 outbox（`vault.file.deleted@1.1` 的载体，已实现+已测）
- **原子写入**：`HardDeleteObjectWithEvent`（`event_outbox.go:102-145`）/`SoftDeleteObjectWithEvent`（:147-184）/`SoftDeleteObjectByIDWithEvent`（:186-222）= legal-hold 检查 + access-state 清理 + 对象删除（0 行 → `ErrNotFound` 回滚，GAP-4）+ L0 `audit_log` 行 + 两条事实（deleted@1.1 + notify@1.1）**同一事务**；事实校验 `validateOutboxFacts`（:35-60）：type 白名单、`origin_id>0`、payload 1..1 MiB 且 JSON `schema_version=="1.1"`（`validOutboxPayload` :62-71）——任一失败整体回滚。
- **构建**：`internal/events/payload.go` `BuildDeletedFact` :109-135 / `BuildNotifyFact` :137-165，字段序固定；sequencer emit 时刻 `crypto/rand`（不用 `obj.ID`，RestoreObject 复用行 id）。
- **relay**：`internal/events/event_outbox_relay.go` `Run` :126 / `deliverDeleted` :205-229（L2 `AuditSink`，`ErrSinkUnauthorized` → 立即 failed）/ `deliverNotify` :251-286（**payload 原样 POST**，投递期不重派生）。
- **claim/ack**：`event_outbox.go` `ClaimEventOutbox` :251-283（PG `FOR UPDATE SKIP LOCKED` :266-283 / SQLite 事务 :285-310；谓词 = pending 到期 **OR** inflight 租约过期）、`CompleteEventOutbox` :336-361（owner+token+lease 栅栏 + 保真记录同事务）、`RetryEventOutbox` :364-391（`attempts>=maxAttempts` → 终态 failed）、`PruneEventOutbox` :393-435（DELETE 式，无 tombstone）。
- **既有测试（门禁内）**：`internal/events/schema_test.go`（golden 字节 + 必填字段，`TestEventSchema_GoldenJSON` :31、`TestEventSchema_RequiredFields` :42）；`internal/repository/event_outbox_test.go`（单事务/回滚、claim→complete 闭环、租约过期重投、退避终态、prune、按 type 恰一）；`internal/integration/admin_files_delete_test.go`（无 build tag，门禁内：`TestAC2_AdminDelete_EventTypeFilteredState` :112、`TestComposition_AdminFilesDeleteEndToEnd` :167 leg1 阻塞 L2 的 4s 信号守卫 + leg2 relay 宕机→延迟启动恢复）；`internal/integration/fullserver_test.go`（`TestDeleteResponse_DoesNotBlockOnDelivery` :702、`TestComposition_DeleteDeliversBothFacts` :893 字节精确+恰好一次+D2 无重复窗口、`TestComposition_MidClaimRestartRedeliversOnce` :1032 崩溃重启重投）。

### 2.2 审计治理 outbox（redacted 摘要，已实现；delete 起源覆盖缺失）
- **写入**：`internal/repository/audit_governance_write.go` `RecordAuditWithGovernance` :14-42 / `InsertEventWithGovernance` :45-90（事件行 + 治理行同事务；`fact.Action = "file."+event.Type`，`factFromEvent` 见 `internal/auditgovernance/facts.go:30-48`）；`EnqueueAuditGovernance` :96-130（gap 重建，`ON CONFLICT` + tombstone `WHERE NOT EXISTS`）。
- **claim/ack**：`audit_governance_claim.go` `ClaimAuditGovernance` :16-29（PG `SKIP LOCKED` :31-49 / SQLite 事务 :51-90）、`CompleteAuditGovernance`、`RetryAuditGovernance`、`FailAuditGovernance`（终态+保留窗口）、`CleanupDeliveredAuditGovernance`、`CleanupFailedAuditGovernance`（`audit_governance_cleanup.go`）。
- **relay**：`internal/auditgovernance/relay.go`（§1 E3）；`http.go` `Publisher.Publish`（POST `api/v1/events?wait_for=ledgered`，回执校验，Contract A conflict 终态）；wire schema = `model.go` `SchemaID="aero.vault.security"`/`SchemaVersion=1`（**与 `vault.file.deleted@1.1` 是两套 schema**）。
- **既有测试**：SQLite 门禁 `internal/repository/audit_governance_test.go`（通用 fact）；`internal/auditgovernance/runtime_test.go`（`TestRuntimeRelaysAtomicAdminAndFileFactsAndDrains` :52 用 `EventCreated`、`TestRuntimeCloseDrainsBlockedHTTPDelivery` :295 阻塞 POST + Close drain——**均非 delete 路径、非 internal/integration、非 Postgres**）；Postgres `audit_governance_postgres_test.go`（仅 `tenant.status`）。
- **装配**：`cmd/server/main.go:81` `WrapRepository(repo, auditRuntime)`（生产形状，组合 harness 未接线——G4 缺口的装配参照）。

---

## 3. 验收标准（方向原文四条，原样保留并测试化）

> 每条的“方向原文”逐字保留；“现状”引用已存在测试；“缺口→测试化检查点”是本套件的可执行交付物。位置标注遵循仓库惯例：门禁 = 无 build tag（`go test ./...`），Postgres = `//go:build integration`（`make test-integration`）。

### AC-1 单元：delete 动作写恰一行 outbox（全字段）+ 同源二次入队被抑制（tombstone/dedupe）

> 方向原文：*unit: repository-level test that RecordAuditWithGovernance for a delete action writes one outbox row with all vault.file.deleted@1.1 fields and that a second enqueue of the same origin is suppressed (tombstone/dedupe)*

**现状：**
- `event_outbox` 侧：“恰一行”已测（`event_outbox_test.go` `TestDeleteObjectWithEvent_EventTypeFilteredRows`、`TestAC2_AdminDelete_EventTypeFilteredState`）；但存储 payload 的**全字段**断言缺失（现仅 `schema_version`+`object_id`，或 tenant/key 子串）。
- `audit_governance_outbox` 侧：通用 fact 的 dedupe/tombstone 已测（`audit_governance_test.go:300`）；**delete 起源行**（`Action="file.deleted"`、`OriginKind=file`、`FactKind=file`）经 `InsertEventWithGovernance(Event{Type: EventDeleted})` 的写路径 + 同源抑制**未测**。

**机制澄清（必须写进测试注释，防止写错断言）：**
- “带全部 `vault.file.deleted@1.1` 字段”只对 `event_outbox` 成立（payload 为原始自包含 JSON）。
- 审计治理行按设计**只存 redacted 摘要**（`types.go:28-30`）——断言对象是 `actor_digest`/`target_digest`/`request_id`（HMAC，`facts.go`）、`object_size_bytes`、`storage_backend`、`occurred_at_ns`。
- “同源二次入队抑制”只对审计治理 outbox 成立（`UNIQUE(origin_kind,origin_id)` + `audit_governance_delivered_origins` tombstone，迁移 0039/0040）；`event_outbox` **刻意无 tombstone**（restore→re-delete 须产生新行，去重 = 行 id + status 谓词，`event_outbox.go:16-17` 注释）——不得为其写“二次入队被抑制”测试。

**缺口→测试化检查点（交付物 1，位置：`internal/repository/audit_governance_delete_test.go`，SQLite 门禁）：**
1. `InsertEventWithGovernance(ctx, Event{TenantID, Bucket, Key, Type: EventDeleted, RequestID, Payload: {"size","backend"}}, fact)` 提交后：
   - `audit_governance_outbox` 恰 1 行；`action="file.deleted"`、`fact_kind="file"`、`origin_kind="file"`、`origin_id ==` 返回的事件行 id、`tenant_id` 归一化、`actor_digest/target_digest/request_id` 非空且与 redactor 对 `bucket+"\x00"+key`/actor/requestID 的 HMAC 一致（用同一 HMACKey 重建期望值）、`object_size_bytes == payload["size"]`、`storage_backend` ∈ {local,s3,oss,cos}、`occurred_at_ns > 0`、`attempts=0`、`delivered_at_ns=0`。
2. 同源抑制（未投递时）：对同一 `OriginKind/OriginID` 调 `EnqueueAuditGovernance` → 返回 `(false, nil)`（`ON CONFLICT DO NOTHING`）。
3. 同源抑制（投递+清理后，tombstone）：claim→complete→`CleanupDeliveredAuditGovernance(now+1h)` → 再 `EnqueueAuditGovernance` → `(false, nil)`；`ListAuditGovernanceGaps(tenant)` 无此起源的 gap。
4. 事件行侧：`object_events` 恰 1 行，`type="deleted"`，`request_id`/`payload` 原样落库（`InsertEventWithGovernance` 的事务另一侧）。
5. 负数用例：`RecordAuditWithGovernance` 对 `Action:"file.delete"` 的行 `origin_kind="admin"`（write.go:30 强制覆盖）——用断言锁定该语义，防止未来“把 delete 审计也路由进治理 outbox”时静默改变 origin 语义。

**缺口→测试化检查点（交付物 1b，位置：`internal/integration/event_schema_conformance_test.go` 或扩展 `admin_files_delete_test.go`，门禁）：** 真实 DELETE 后读 `event_outbox` 中 `event_type='vault.file.deleted@1.1'` 的存储 payload，断言**全部 12 个必填字段**（见 AC-3 schema）逐字段存在且类型正确（非子串匹配）。

### AC-2 outbox 投递：claim → complete → `CleanupDeliveredAuditGovernance` 闭环，SQLite 门禁 + Postgres integration，含崩溃 claim 的租约过期恢复

> 方向原文：*outbox delivery: end-to-end claim -> complete -> CleanupDeliveredAuditGovernance round-trip on SQLite in CI gate and Postgres under -tags=integration, including lease-expiry recovery of a crashed claim*

**现状：**
- 审计治理：SQLite 闭环已测但**仅通用 fact**（`audit_governance_test.go:45/:300`）；Postgres 闭环已测但**仅 `tenant.status`**（`audit_governance_postgres_test.go:18-56`，含并发 claim、租约过期恢复、stale 完成栅栏、cleanup、tombstone）。
- 事件 outbox：SQLite 闭环全覆盖（`event_outbox_test.go`）；**Postgres 分支（`claimEventOutboxPostgres` SKIP LOCKED 路径）零覆盖**。

**缺口→测试化检查点：**

*交付物 2a（位置：`internal/repository/audit_governance_delete_test.go`，SQLite 门禁）——delete 起源行闭环：*
1. `InsertEventWithGovernance(EventDeleted)` → `ClaimAuditGovernance("worker-a","token-a",revision,1,time.Minute)` 恰 1 行，`attempts=1`、`claim_owner/token` 落库、`lease_expires_at_ns > now`。
2. 并发栅栏：第二次 claim 返回 0 行（leased 行不被重领）。
3. 崩溃恢复：`ClaimAuditGovernance("crashed","stale",1,1,150ms)`（不 complete）→ 等租约过期 → `ClaimAuditGovernance("recovery","fresh",1,1,time.Second)` 重领同一行且 `attempts=2`；stale 的 `CompleteAuditGovernance(id,"crashed","stale")` 失败（栅栏）。
4. `CompleteAuditGovernance` → `CleanupDeliveredAuditGovernance(now.Add(-1h))` 计 0（保留窗口内）、`(now.Add(1h))` 计 1；清理后 `ListAuditGovernanceGaps` 无 gap（tombstone 生效）。

*交付物 2b（位置：`internal/integration/audit_governance_postgres_test.go` 扩展或同文件新测试，`//go:build integration`）——delete 起源行 PG 并发/恢复：*
1. seed 用 `InsertEventWithGovernance(EventDeleted)` 的 delete 起源事实（替换/补充现 `tenant.status` seed，`seedGovernanceFacts` 增加 delete 变体）。
2. 复用现测试形状：`lockGovernanceRows`（`FOR UPDATE`）占 4 行 → 另一连接 `ClaimAuditGovernance`（`SKIP LOCKED`）只领剩余行；`assertDistinctGovernanceClaims` 无重复；租约过期恢复 + `Attempts==2`；stale complete 被拒；cleanup 计数精确；tombstone 抑制再入队。
3. 断言 `action="file.deleted"` 行参与 claim/complete（不按 action 过滤错行）。

*交付物 2c（位置：`internal/integration/event_outbox_postgres_test.go` 新建，`//go:build integration`）——`event_outbox` PG 全闭环（SKIP LOCKED 路径首次真库覆盖）：*
1. `pgDSN()`/`freshRepo(t)`（`postgres_integration_test.go:27/:36`）开库 + Migrate；`HardDeleteObjectWithEvent` 写 2 行。
2. 两连接并发 `ClaimEventOutbox`：SKIP LOCKED 行不相交（`assertDistinct` 形状）；`CompleteEventOutbox` 后 `event_outbox_delivered` 保真行同事务写入；delivered 行不再被领。
3. 租约过期恢复：短 TTL claim 后不 complete → 第二 claimer 重领 `attempts=2`；stale complete → `ErrClaimLost`。
4. `RetryEventOutbox` 退避门（未到期不可领）→ 背调 `available_at_ns` 后重领 → `attempts>=maxAttempts` 终态 `failed`，不可再领。
5. `PruneEventOutbox(now-24h, now-7d)` 计 0 → 背调 `delivered_at_ns` 后计 2，`event_outbox_delivered` 同步清空。
6. （如 PG 环境提供）双副本并发 claim 断言 SKIP LOCKED 语义（对照 `audit_governance_postgres_test.go` 的 `repoOne/repoTwo` 双连接形状）。

### AC-3 事件 schema：1.1 的 JSON-schema 一致性（必填字段、自包含 payload、无兄弟项目标识符——schema 需定义并由测试锁定）

> 方向原文：*event schema: JSON-schema conformance for version 1.1 (required fields, self-contained payload, no sibling-project identifiers — proposed schema, must be defined and locked by test)*

**现状：** golden 字节锁定已存在（`schema_test.go`，与 AC-3 兼容）；`validOutboxPayload` 只校验 `schema_version=="1.1"`；**无机器可读 schema、无对 DB 存储 payload 的全量一致性校验、无“无兄弟项目标识符”显式断言**。

**缺口→测试化检查点（交付物 3，位置：`internal/integration/event_schema_conformance_test.go` 新建，门禁）：**
1. **schema 定义**：在测试内嵌入 JSON Schema（draft-07 风格）作为唯一事实源，锁定 `vault.file.deleted@1.1`：必填 `schema_version`（enum `["1.1"]`）、`event_type`（enum `["vault.file.deleted@1.1"]`）、`tenant`、`bucket`、`key`（string）、`object_id`（integer, `>0`）、`version_id`、`size`（integer, `>=0`）、`etag`、`backend`（enum `["local","s3","oss","cos"]` 或空）、`request_id`、`actor`；可选 `reason`（string）。`additionalProperties:false`。
2. **正例**：`events.BuildDeletedFact` 输出通过校验；真实 DELETE 后从 `event_outbox` 读回的存储 payload 通过校验（builder 输出与 DB 往返双入口）。
3. **负例**：缺 `key`、缺 `object_id`、`object_id` 非整数、`schema_version:"2.0"`、`event_type` 错值、出现 `share_id`/`chunk_ids`/`project_id`/`usage` 任一键（additionalProperties 拒绝）→ 校验失败。
4. **自包含**：`version_id`/`size`/`etag`/`backend` 非空时与删除时刻对象一致（用 `h.repo.GetObject` 前置值比对，证明投递期不重派生）；`notify@1.1` 的 `records[0].s3.object.{key,size,eTag,versionId,sequencer}` 全字段 emit 时刻捕获（sequencer 匹配 `^[0-9a-f]{32}$`）。
5. **与既有 golden 兼容**：本测试不得修改 `schema_test.go` 的 golden 常量；若 schema 与 golden 冲突，以 golden 为准并更新 schema（schema 是测试夹具，不是生产校验——见 §5）。

### AC-4 组合 e2e：relay endpoint 宕机/挂起时删除文件 → 删除仍 2xx，且 outbox 行在下一 claim 周期投递（durable_async 永不阻塞证明）

> 方向原文：*composition e2e: delete a file while the relay endpoint is down/hanging -> delete still returns 2xx and the outbox row is delivered on the next claim cycle (durable_async never-blocks proof)*

**现状（事件 outbox 侧，门禁内已全部满足，不得重复建设）：**
- L2 挂起：`TestDeleteResponse_DoesNotBlockOnDelivery`（fullserver_test.go:702，信号式：L2 POST 阻塞期间 DELETE 必须在 4s 内返回，4s < 5s relay HTTP 超时为确定性判别器；释放后 ≤15s 内 `delivered`，L2 POST 数 ≥1）。
- relay 宕机 + 延迟启动：`TestComposition_AdminFilesDeleteEndToEnd` leg2（admin_files_delete_test.go:167）：无 relay 时删除 0 退出码、两事实 `pending`；启动 relay 后 ≤15s 双 `delivered`，JOIN 计数恰一。
- 崩溃重启重投：`TestComposition_MidClaimRestartRedeliversOnce`（fullserver_test.go:1032）：pending 行由第二个 relay 重领，L2 恰 1 次、notify 1×500+1×200，1s 无重复窗口。

**缺口→测试化检查点（交付物 4，位置：`internal/integration/audit_governance_composition_test.go` 新建，门禁）——审计治理 relay 的组合证明（当前零覆盖）：**

*装配（先决）：* `startFullServerOpts`（fullserver_test.go:72）新增可选 `auditgovernance.Runtime` 注入（默认 nil，现有测试零影响；镜像 `cmd/server/main.go:81` 的 `WrapRepository` 时序——**必须在 `events.New(repo,…)` 与 `service.NewFileService(store, repo,…)` 之前包装**，使 bus 与 service 同用包装后的 repo），并 `ApplyAuditGovernanceBindings`（经 `runtime.New` 的 `applyDesiredBindings`）绑定测试租户。

1. **挂起端点**：治理端点（`httptest.Server`，路径 `/api/v1/events`，`/token` 发 access_token）POST 阻塞（chan 门闩，参照 `TestRuntimeCloseDrainsBlockedHTTPDelivery` 形状）；REST DELETE 文件（`deleteWithTenant` 形状）→ 断言 204 且 ≤4s 返回（信号式，与 `TestDeleteResponse_DoesNotBlockOnDelivery` 同判别器）；同时断言治理 outbox 行存在且 `delivered_at_ns=0`（pending/inflight 合法）。
2. **恢复投递**：释放门闩（回执 `{receipt:{event_id:<回显>,tenant_id,status:"ledgered",accepted_at}}`）→ 轮询 ≤15s 至 `delivered_at_ns>0`；断言治理端点收到**恰 1 次**事件 POST（无重复）；wire body 的 `event_id==fact.ID`、`action=="file.deleted"`、`actor.id` 非 `aero-vault` 前缀时的 digest 形状（`governanceWire`，http.go）。
3. **宕机端点**（子场景）：端点直接 5xx（或 `http.NotFoundHandler`）→ DELETE 仍 204；行经退避重试（`attempts>=2` 可观测）；端点恢复为 202+回执 → 下一 claim 周期投递。
4. **删除延迟不受影响**：断言（1）中 DELETE 响应时间与基线（无治理装配的 `TestFullServer_REST_CRUD` DELETE）同量级——用信号式判别器即可，不做墙钟微基准。
5. **否定面**：治理端点 401/令牌失效 → 行终态/重试路径不阻塞删除（可选，参照 `TestRuntimeCredentialRotationRequiresHigherRevisionAndUsesNewSecret` 形状，若超时预算则登记为观察项不阻塞 AC-4 通过）。

---

## 4. 已验证缺口与偏差（登记，交付物清单）

| ID | 缺口 | 交付物位置 | 门禁 | 对应验收 |
|----|------|-----------|------|---------|
| G1 | 审计治理 outbox：delete 起源行（`file.deleted`）写路径全字段断言 + 同源二次入队抑制（未投递 & tombstone 后） | `internal/repository/audit_governance_delete_test.go`（新建） | SQLite `go test ./...` | AC-1 |
| G1b | `event_outbox` 存储 payload 全字段一致性（现仅子串/两字段） | `internal/integration/event_schema_conformance_test.go`（新建，含 AC-3） | SQLite 门禁 | AC-1/AC-3 |
| G2a | 审计治理 delete 起源行 claim→complete→cleanup 闭环 + 租约过期恢复（SQLite） | `internal/repository/audit_governance_delete_test.go` | SQLite 门禁 | AC-2 |
| G2b | 审计治理 delete 起源行 PG 并发 claim/租约恢复/cleanup/tombstone | `internal/integration/audit_governance_postgres_test.go`（扩展） | `-tags=integration` | AC-2 |
| G2c | `event_outbox` PG 全闭环（SKIP LOCKED 路径首测）：claim/complete/retry/prune/租约恢复 | `internal/integration/event_outbox_postgres_test.go`（新建） | `-tags=integration` | AC-2 |
| G3 | `vault.file.deleted@1.1` 机器可读 JSON Schema + 双入口一致性 + 无兄弟标识符锁定 | `internal/integration/event_schema_conformance_test.go` | SQLite 门禁 | AC-3 |
| G4 | 审计治理 relay 组合 e2e：端点挂起/宕机时删除 2xx + 恢复后下一 claim 周期投递（含 harness 可选 Runtime 装配） | `internal/integration/audit_governance_composition_test.go`（新建）+ `fullserver_test.go` harness 增量 | SQLite 门禁 | AC-4 |

**已登记偏差（与方向陈述不符，不改现状）：**
- 方向称“无 schema 一致性/投递测试”“无延迟不受影响测试”“无 SQLite 门禁 delete 起源 claim/complete/cleanup”——对 `event_outbox` 已不成立（§2.1 测试清单）；本文不重复建设，仅在 G1b 补全字段级断言。
- 方向隐含“`RecordAuditWithGovernance` 承载 deleted@1.1 全字段”——实际 delete 起源治理行为 redacted 摘要（E7）；AC-1 已按真实机制拆分。

---

## 5. 范围边界（明确不做，与方向一致）

1. **`event_outbox` 的生产侧 schema 强制**：不在 repository 校验中引入完整 JSON-Schema 校验器（现 `validOutboxPayload` 保持 schema_version 检查；schema 一致性由测试锁定——方向只要求“定义并由测试锁定”）。
2. **`DeleteVersion`/delete-marker/隔离（E14 路径）**：无 outbox 行、保留 bus 路径（`notifier.go` D2 注释），本套件不扩展（前置规格已登记）。
3. **审计治理 Contract A（conflict 终态+保留窗口）**：已有 `TestAuditGovernanceConflictFailIsTerminalAndRetentionPruned`（SQLite）与 `TestRuntimeConflictingReceiptIsTerminalWithRetention`，不重复。
4. **billing outbox / pgvector / Qdrant / WebDAV / S3 网关**：与本方向无关的 opt-in 面一律不触碰。
5. **不做墙钟微基准**：AC-4 用信号式判别器（4s < 5s 超时）证明不阻塞，不引入 flaky 延迟断言。
6. **不改生产装配语义**：`startFullServerOpts` 的 Runtime 注入为可选参数（默认 nil），现有调用点与既有测试零改动。

---

## 6. 复现命令

```bash
# 门禁（全部交付物 G1/G1b/G2a/G3/G4 在此跑）
make check            # = gofmt -l / go build ./... / go vet ./... / go test ./... / cli-check

# Postgres 集成（交付物 G2b/G2c；探活失败自动 skip，见 postgres_integration_test.go:27 pgDSN）
make test-integration # 或：go test -tags=integration ./internal/integration/...

# 单测定位
go test ./internal/repository/ -run 'AuditGovernance|EventOutbox' -v
go test ./internal/integration/ -run 'TestComposition|TestAC2|TestDeleteResponse|TestEventSchema' -v
go test -race ./internal/repository/ ./internal/integration/ -run 'AuditGovernance|EventOutbox' -v
```

**合入门禁提示（AGENTS.md）：** 单文件 ≤500 行（`audit_governance_delete_test.go`/`event_schema_conformance_test.go` 预计 200-400 行，超限则按主题拆分）；不引入断言框架（`testing` only，I6）；Postgres 测试遵循 `freshRepo`/`pgDSN` 既有 helper 与“探活失败自动 skip”惯例；SQL 占位符遵守 I1（`s.rebind`）。
