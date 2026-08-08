# 方向：删除事件事务性 outbox —— `vault.file.deleted@1.1` / `vault.file.notify@1.1` 与删除原子写入（验收规格 · 已验证现状）

> **模块：** `internal/events`（+ `internal/repository` 事务侧 · `internal/service` 构建侧 · `cmd/server` 装配）
> **来源分析：** `docs/auto/analyses/internal-events-495038b5.json` · **日期：** 2026-08-06 · **HEAD：** `acfaaf4`
> **评分：** 价值 9 / 风险降低 8 / 工作量 7 / 置信度 9
> **状态声明：** 本方向**已实现并合入**（迁移 `0041`、repo `WithEvent` 方法、`internal/events` relay/payload、`cmd/server` 装配、配置项、全套测试）。本文不是绿地设计，而是**验收契约**：逐条核验方向引证、保留四条验收检查并映射到已存在测试、登记已验证的偏差/缺口。超范围项一律不做（§5）。
> **前置文档：** `docs/requirements/transactional-outbox-delete-events-v1.md`（实现前规格，已被本文取代；实现按 D1–D7 设计决策落地，见其修订记录）。

---

## 1. 证据核验（方向引证逐条对照当前 HEAD）

| # | 方向引证（分析时行号） | 当前 HEAD 位置 | 核验结论 |
|---|----------------------|----------------|---------|
| E1 | `internal/service/file_delete.go:44-51,74-90,143-161` — hardDeleteObject/softDeleteObject/DeleteVersion 仅在 repo 删除返回后调用 `s.emit` | `hardDeleteObject` :18-56（`HardDeleteObjectWithEvent` 调用 :46，`s.emit` :53）；`softDeleteObject` :76-93（`SoftDeleteObjectWithEvent` :86，`s.emit` :92）；`DeleteVersion` :174-218（`DeleteObjectVersion` :205，`s.emit` :212/:215） | ✅ **语义已改变（修复）：** 硬删/软删的**持久化事实现写入删除事务内**（WithEvent）；`s.emit` 保留为提交后的 legacy 尽力广播。`DeleteVersion` 仍走旧路径（E14，明确超范围，§5） |
| E2 | `internal/events/bus.go:104-131` — `Publish → repo.InsertEvent`，非事务、错误吞掉 | `Publish` :80-98（`InsertEvent` :84，错误仅 `logger.Warn` 后 return :85-89） | ✅ 行号漂移（104→80）。**现状不变且是设计行为**：legacy `object_events` 写入仍非事务、仍吞错（不阻塞业务流）；审计/通知保证已由 outbox 承载 |
| E3 | `internal/repository/sql_events.go:16-34` — 单条 INSERT，无事务 | `InsertEvent` :9-31（单语句，无 `BeginTx`） | ✅ 行号漂移（16→9），语义一致 |
| E4 | `internal/repository/sql_objects_maint.go:20-40` — `SoftDeleteObject` 已在事务内跑 `deleteObjectAccessState`（outbox 行的天然接缝） | `SoftDeleteObject` :20-41（`BeginTx` :22，`deleteObjectAccessState` :38，`Commit` :40） | ✅ 行号精确。接缝论断成立：WithEvent 变体正是扩展该事务 |
| E5 | `internal/repository/migrations/sqlite/0039_audit_governance_outbox.up.sql` — 已证 outbox 形状：attempts/lease/幂等 `UNIQUE(origin_kind,origin_id)` | 0039 存在（attempts/available_at_ns/claim_owner/claim_token/lease_expires_at_ns/last_error/delivered_at_ns + `UNIQUE(origin_kind, origin_id)`） | ✅ 形状引证成立，但见 **E5a 偏差**：事件 outbox 用的是**新迁移对 0041**，且**刻意不用 UNIQUE** |
| E5a | （补充核验）事件 outbox 迁移 | `internal/repository/migrations/{sqlite,postgres}/0041_event_outbox.{up,down}.sql`（双方言，I2） | ✅ 表：`event_outbox`（`id` AUTOINCREMENT = **权威去重键**，注释明示；`event_type` CHECK 限 `vault.file.deleted@1.1`/`vault.file.notify@1.1`；`status` CHECK `pending/inflight/delivered/failed`；payload TEXT 保字节原样）+ `event_outbox_delivered`（complete 事务内写，保真记录，功能惰性）。**无 `UNIQUE(event_type, origin_id)`——因 `RestoreObject` 原地 UPDATE 复用 objects 行 id（sql_objects_maint.go:247-273），UNIQUE 会让 restore→re-delete 第二次删除 500；去重 = outbox 行 id + status 谓词（D1/D4/D5）** |
| E6 | `internal/repository/repository.go:176-194` — `Event`/`EventType` 无版本 | `Event` :172-184（`Payload map[string]string`）；`EventType` :187-194（仅 `created/updated/deleted/accessed`） | ✅ 行号漂移（176→172），现状不变：legacy 事件流无版本字段；版本化命名空间独立存在于 `event_outbox.go:17-26`（`OutboxEventType`，`EventTypeFileDeleted11 = "vault.file.deleted@1.1"`，`EventTypeFileNotify11 = "vault.file.notify@1.1"`） |

**方向问题陈述核验：** "崩溃于 commit 与 insert 之间 → deleted 事件永久丢失" —— **已修复**：`deleted@1.1`/`notify@1.1` 事实与 `DELETE FROM objects`（及 L0 `audit_log` 行）同事务提交（`event_outbox.go:102-145/:147-184/:186-222`，`insertAuditEntry` 定义于 `audit.go:21`）。剩余的非事务写入是 legacy `object_events` 流（E2/E3），供 SSE 回放/旧消费者使用，不在本方向范围（§5）。

---

## 2. 已落地机制（验收对象，全部实测通过）

### FR-1 删除 + 事实单事务原子写入
- `HardDeleteObjectWithEvent`（`event_outbox.go:102-145`）= legal-hold 检查 + `deleteObjectAccessState` + `DELETE FROM objects`（**补 RowsAffected 守卫**：0 行 → `ErrNotFound`，回滚 → 无幽灵事实/审计行，GAP-4）+ `insertAuditEntry`（:134）+ `insertOutboxFacts`（:220 处调用）。
- `SoftDeleteObjectWithEvent`（:147-184）、`SoftDeleteObjectByIDWithEvent`（:186-222）：同形状（后者校验在事务内，D-2）。
- 事实校验 `validateOutboxFacts`（:35-60）：type ∈ 允许集、`origin_id>0`、tenant 非空、payload 1..1 MiB 且 JSON `schema_version=="1.1"`（`validOutboxPayload` :62-71）——**任一失败 → 整体回滚**（AC-1）。
- Service 侧：`deleteAuditEntry`（`file_delete.go:100-120`，actor 取 `access.PrincipalFrom(ctx)`）、`deleteFacts`（:123-148，一次删除两条事实：deleted@1.1 + notify@1.1，payload 在删除时刻构建、自包含）。

### FR-2 版本化自包含 schema（`internal/events/payload.go`）
- `deletedFact` :33-52 / `notifyFact` :54-71 / `notifyRecord` :73-99；构建器 `BuildDeletedFact` :109-135、`BuildNotifyFact` :137-165。字段序固定 → golden 字节精确（AC-3）。
- sequencer（:15-22）：emit 时刻 `crypto/rand` 16 字节 hex，测试注入固定值；**不用 `obj.ID`**（RestoreObject 复用行 id，D6）。
- notify@1.1 自包含：`records[0].s3.object.{key,size,eTag,versionId,sequencer}` 全部 emit 时刻捕获，投递期**不重派生**（E7 修复）。

### FR-3 异步 relay（`internal/events/event_outbox_relay.go`）
- `EventOutboxRelay` :33-49；`Run` :88-111（轮询 + 每 60 轮 prune）；`deliverBatch` :144-179（per-fact goroutine，claim token = crypto/rand）；`deliverFact` :182-195 按 type 分发。
- `deliverDeleted` :197-229：L2 `AuditSink` 投递（`ErrSinkNotBound` → complete；`ErrSinkUnauthorized` 401/403 → 立即 failed，H2）；sink 为 nil → complete（L0 权威）。**不重放本地订阅者**（D3）。
- `deliverNotify` :235-265：投递期解析规则（`GetBucketNotifications`），**payload 原样 POST**（`deliverPayload` :268-285，`postEventTo` 与 Notifier 共享）。
- `complete` :290-303 / `retry` :305-327（指数退避 `eventOutboxBackoff` :329-342：1s 基、2×、5min 上限、[0.75,1.0) 抖动）/ `prune` :344-363（delivered 24h / failed 7d）。
- Repo 侧 claim/ack：`ClaimEventOutbox` :251-281（PG `FOR UPDATE SKIP LOCKED` :283-313 / SQLite 事务 :315-334；谓词 = pending 到期 OR inflight 租约过期——**崩溃重投的恢复机制**）；`CompleteEventOutbox` :336-361（owner+token+lease 栅栏，失守 → `ErrClaimLost`，不循环重试）；`RetryEventOutbox` :364-391（`attempts>=maxAttempts` → 终态 failed）；`PruneEventOutbox` :393-435；`HasEventOutboxFact` :437-448（Notifier D2 条件跳过）；`CountEventOutbox` :450-459。
- **幂等键**：outbox 行 id（claim 谓词按 status 排除已投递行）；L2 投递在 `X-Audit-Fact-Id` 请求头携带（`audit_sink_l2.go`，回显确认 = 接收方提交点，`audit_sink.go:27-47` 契约）。

### FR-4 装配 + 配置
- `cmd/server/workers.go:163-202` `startEventOutboxRelay`（:63 调用）；`EVENT_OUTBOX_ENABLED`（默认 true）只 kill relay 循环，**enqueue 永不 gate**（`docs/configuration.md:354-361`，`.env.example:240-248`）；relay 关闭时 `eventOutboxBacklog`（workers.go:206-217）日志报积压深度。
- L2 适配器：`internal/events/audit_sink.go`（端口）+ `audit_sink_l2.go`（配置驱动，TLS-or-loopback 校验 H1、禁重定向 H6、响应读上限 H3）；`AUDIT_SINK_L2_BINDINGS_FILE`（docs/configuration.md:363）。
- Notifier D2 去重：`notifier.go` `deliver` :60-86 —— `EventDeleted` 且 `HasEventOutboxFact(origin_id, notify@1.1)`（任意 status）→ 跳过 bus 投递（WithEvent 事务先于 `s.emit` 提交，无竞态）；E14 路径无 outbox 行 → 保留 bus 路径。

---

## 3. 验收标准（方向原文四条，原样保留并测试化）

### AC-1 单事务回滚：零 outbox 行 + 零广播

> 方向原文：*unit: delete-tx rollback leaves zero outbox rows and triggers no broadcast*

| 断言 | 已存在测试 | 位置 |
|------|-----------|------|
| 合法路径：硬删/软删后对象行消失、`event_outbox` 恰 2 行（deleted@1.1 + notify@1.1）、`event_outbox_delivered` 为空 | `TestDeleteObjectWithEvent_OneTx`（含**强制回滚分支**：非法事实 → error、对象仍在、outbox 0 行） | `internal/repository/event_outbox_test.go:136` |
| 审计行插入失败 → 删除与事实**整体回滚** | `TestHardDeleteAuditInsertFailure_RollsBack` | 同文件 :810 |
| 并发双删竞态：0 行受影响 → `ErrNotFound`、无幽灵事实 | `TestDeleteObjectWithEvent_EventTypeFilteredRows` | 同文件 :765 |
| 拒绝路径：无 outbox 行、对象未动（授权失败不产生事实/事件） | `TestDeleteDenied_NoOutboxRow_ObjectUntouched` | `internal/service/file_delete_test.go:68` |
| **零广播**：WithEvent 返回错误 → service 在 `s.emit` 之前 `return err`（`file_delete.go:46-54`，emit :53 不可达）——机制已由代码结构保证；可测试化配方：挂 bus 订阅者 → 强制回滚（非法 fact 或 audit 插入失败）→ 断言订阅通道零事件 | （新增可选测试；现有 rollback 分支测试 + emit 位置即守卫） | — |

### AC-2 崩溃恢复 + 幂等 claim/ack

> 方向原文：*outbox delivery: kill-after-commit simulation is recovered by an outbox reaper (pattern: jobs reaper/0039 claim), each consumer claims/acks with idempotency key*

| 断言 | 已存在测试 | 位置 |
|------|-----------|------|
| kill-after-commit 模拟：owner A claim 后不 complete（模拟崩溃），租约过期 → owner B 重新 claim 同一事实并投递成功 | `TestEventOutboxClaimLeaseExpiryRedelivers` | `internal/repository/event_outbox_test.go:259` |
| claim-lost（complete/retry 栅栏失守）→ warn+计数，**不循环重试**，由租约重 claim 恢复 | `TestOutboxRelay_ClaimLostLeadsToReclaimNotDoubleSchedule` | `internal/events/event_outbox_relay_test.go:258` |
| 完整生命周期：claim → deliver（httptest 目标恰 1 次 POST，payload 原样）→ complete → `status='delivered'` + delivered 表 1 行；complete 后重复 claim 无行（恰好一次，仅 complete 后成立；deliver→complete 窗口显式 at-least-once） | `TestOutboxRelay_DeliveryLifecycle` | 同文件 :173 |
| 5xx 退避重试：attempts 递增、`available_at_ns` 推后、退避 ∈ [1s,5min] 且抖动有界 | `TestOutboxRelay_RetriesOn5xx` + `TestEventOutboxBackoffBounds`（:328）+ `TestEventOutboxRetryBackoffAndTerminalFailed`（`event_outbox_test.go:300`） | — |
| 中继重启（kill relay → 同 DB 重启）→ 事实仍投递、恰好一次 | `TestComposition_MidClaimRestartRedeliversOnce` | `internal/integration/fullserver_test.go:1030` |
| **幂等键**：claim 身份 = owner+token+lease（E5 0039 claim 形状）；权威去重 = outbox 行 id + status 谓词（E5a） | 上述测试即断言 | — |

### AC-3 golden JSON fixture

> 方向原文：*event schema: JSON fixture asserts vault.file.deleted@1.1 fields (tenant, object_id, key, actor, occurred_at, schema_version) and self-contained notify@1.1 payload*

| 断言 | 已存在测试 | 位置 |
|------|-----------|------|
| 生产构建器输出与 golden 常量**逐字节相等**（固定输入 → 字节稳定） | `TestEventSchema_GoldenJSON`（golden 常量 :14/:17） | `internal/events/schema_test.go:31` |
| deleted@1.1 必填字段集 + `object_id==obj.ID` + 无 `records` | `TestEventSchema_RequiredFields`（字段清单 :50-55） | 同文件 :42 |
| deleted@1.1 信封：`schema_version/event_type/tenant/bucket/key/object_id/actor` 全部携带 | `TestEventSchema_Deleted11Envelope` | 同文件 :96 |
| notify@1.1 自包含：`records[0].s3.object.{key,size,eTag,versionId,sequencer}` 全部在 payload 内；`eventName=="s3:ObjectRemoved:Delete"` | `TestEventSchema_RequiredFields`（notify 分支） | 同文件 :61-94 |
| sequencer 每次调用唯一（非 `obj.ID`） | `TestEventSchema_SequencerUniquePerCall` | 同文件 :132 |
| ⚠️ **`occurred_at` 缺口（方向原文字段）**：已实现 `deletedFact` **无 `occurred_at` 字段**（`payload.go:33-52`），golden/必填清单亦未断言（schema_test.go:14/:50-55）。时间信息仅存在于 outbox 行 `created_at_ns` 与 L0 `audit_log` 时间列 | **未满足** → 见 §4 G-1 | — |

### AC-4 组合 e2e：admin 删除 → 每类型恰一行 → 审计/通知各恰好一次；Publish 不阻塞

> 方向原文：*composition e2e: admin delete → one outbox row → audit + notify consumers each deliver exactly once, and Publish stays non-blocking (never delays the delete response)*

| 断言 | 已存在测试 | 位置 |
|------|-----------|------|
| admin 删除 → `event_outbox` 每类型**恰 1 行**、`pending`、无 relay 时 delivered 表 0 行 | `TestAC2_AdminDelete_EventTypeFilteredState` | `internal/integration/admin_files_delete_test.go:112` |
| 全服务器 + relay：删除 → 两事实均 `delivered`（通知目标收到 notify@1.1）；relay 关闭时删除成功、稍后启动 relay 积压排空恢复 | `TestComposition_AdminFilesDeleteEndToEnd`（leg1 信号式非阻塞 + leg2 relay-down/start-later 恢复） | 同文件 :167 |
| service 级：硬删 → deleted@1.1 与 notify@1.1 各恰 1 行可见 + L0 audit_log 行（actor/detail/tenant） | `TestAdminDelete_EmitsExactlyOneDeletedFact` + `TestFileServiceDelete_WritesAuditRow` | `internal/service/file_delete_test.go:156/:19` |
| L2 审计消费者：deleted@1.1 经 `AuditSink` 投递、`X-Audit-Fact-Id` 幂等头 | `TestOutboxRelay_DeliversDeletedFactToL2` | `internal/events/event_outbox_relay_test.go:461` |
| **Publish 不阻塞**：广播背压时 drop 而非阻塞（bus 级）；删除响应**绝不等待** relay（异步 `Run` 循环，workers.go:163-202）；e2e 信号式（非 wall-clock）断言 blocked-L2 不延迟删除响应 | `bus_test.go:174-192`（"Publish blocked: broadcast is not non-blocking under backpressure"）；`TestDeleteResponse_DoesNotBlockOnDelivery` | `internal/events/bus_test.go`、`internal/integration/fullserver_test.go:700` |

**实测（本规格核验时，SQLite+local FS，`-count=1`）：**
```
go test ./internal/events/ ./internal/repository/ ./internal/service/   → ok
go test ./internal/integration/ -run "TestAC2_AdminDelete_EventTypeFilteredState|TestComposition_AdminFilesDeleteEndToEnd" → ok (0.8s)
```

---

## 4. 已验证缺口与偏差（登记，不扩scope）

| # | 缺口/偏差 | 证据 | 处置建议 |
|---|----------|------|---------|
| G-1 | **AC-3 的 `occurred_at` 字段不在已实现 schema 中** | `payload.go:33-52`（deletedFact 无 occurred_at）；`schema_test.go:14`（golden 无）；必填清单无（:50-55）。时间仅存在于 `event_outbox.created_at_ns` 与 L0 audit_log | 二选一：(a) 加 `occurred_at`（additive，golden 同步更新——方向验收原文要求该字段，倾向此路）；(b) 显式豁免并在规格中注明时间由 outbox 行/audit_log 承载。**此为唯一未满足的验收字面项** |
| G-2 | legacy `object_events` 写入（`bus.go:84` `InsertEvent`）仍非事务、错误吞掉 | E2/E3 核验 | 设计行为（不阻塞业务流）；审计/通知保证已由 outbox 承载；合并/迁移属独立方向（§5） |
| G-3 | E14 删除路径（`DeleteVersion` file_delete.go:212、delete-marker delete_marker.go:58、隔离/保留清除 object_worker.go:85）无 outbox 事实，仅 bus 投递 | 核验于 §1/E14 | 明确不在本方向范围（方向锚定 `FileService.Delete`）；Notifier D2 条件跳过保证这些路径不丢投递（notifier.go:60-86） |
| G-4 | 0039 的 `UNIQUE(origin_kind, origin_id)` 幂等模式**未复制**到事件 outbox | E5a | 有意偏差（D1/D4）：去重键 = outbox 行 id；`RestoreObject` 复用行 id 使 UNIQUE 不可行。0039 的 claim 栅栏形状（owner+token+lease）已沿用 |

---

## 5. 范围边界（明确不做，与方向一致）

| 不做 | 理由/证据 |
|------|----------|
| `DeleteVersion` / delete-marker / 隔离·保留清除路径的 outbox 化 | 方向锚定 `FileService.Delete`（AC-1/AC-4 明文）；机械扩展属后续方向（G-3） |
| `Event`/`EventType` 结构体版本化改造 | 版本化命名空间已在 `OutboxEventType`（event_outbox.go:17-26）；legacy 流保持兼容（E6） |
| `object_events` 与 `event_outbox` 双份持久化合并 | legacy 流服务 SSE 回放等既有行为；合并属独立重构（G-2） |
| Webhook 投递管线、通知规则引擎改造 | 各自已有 durable retry / 规则匹配；relay 仅替换载荷来源（notifier.go `postEventTo` 共享） |
| actor 身份管线、claim 并发特性（超出现有 `SKIP LOCKED`/SQLite 事务实现） | actor 取 `access.PrincipalFrom(ctx)`，空值合法；现有 claim 已覆盖多实例栅栏 |

---

## 6. 复现命令

```bash
go test ./internal/events/ ./internal/repository/ ./internal/service/        # AC-1..AC-3 组件层
go test -count=1 ./internal/integration/ -run "TestAC2_AdminDelete_EventTypeFilteredState|TestComposition_AdminFilesDeleteEndToEnd"  # AC-4 组合
go test -count=1 ./internal/integration/ -run "TestDeleteResponse_DoesNotBlockOnDelivery|TestComposition_MidClaimRestartRedeliversOnce"  # 不阻塞 + 崩溃恢复
make check                                                                   # gofmt/build/vet 全量
```
