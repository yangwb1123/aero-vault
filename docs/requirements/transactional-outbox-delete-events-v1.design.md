# 方向：删除事件事务性 outbox —— 验证式设计契约（`vault.file.deleted@1.1` / `vault.file.notify@1.1`）

> **模块：** `internal/events` · `internal/repository` · `internal/service` · `internal/config` · `cmd/server`
> **日期：** 2026-08-06 · **HEAD：** `acfaaf4` · **前置：** `docs/requirements/transactional-outbox-delete-events-v1.spec.md`（验收契约）
> **状态：** 方向主体**已实现并合入**（迁移 0041、repo `WithEvent` 方法、relay/payload、装配、配置、测试）。本文 = **对交付证据的独立核验**（每条引证重查 HEAD）+ 落地机制的设计契约 + **唯一未满足项 G-1 的补齐设计**。

---

## 0. 证据核验审计（untrusted claims → verdicts）

| 证据引证 | 重查位置（HEAD `acfaaf4`） | 判定 |
|---|---|---|
| 删除经 `HardDeleteObjectWithEvent`/`SoftDeleteObjectWithEvent` 提交，`s.emit` 为 legacy 尽力广播 | `file_delete.go:18-56`（WithEvent 调用 :46、emit :53）、:76-93（:86/:92）；`event_outbox.go:102-145/:147-184/:186-222` | ✅ **真**（语义已修复；`DeleteVersion` :174-218 仍走旧路径 = 登记的 G-3 范围外项） |
| `bus.go` `Publish` 非事务、错误吞掉 | `bus.go:80-98`（`InsertEvent` :84，Warn 后 return :85-89） | ✅ **真**，且是设计行为（不阻塞业务流） |
| `InsertEvent` 单条 INSERT 无事务 | `sql_events.go:9-31`（无 `BeginTx`） | ✅ **真** |
| `sql_objects_maint.go` 的 tx 接缝 | `SoftDeleteObject` :20-41（BeginTx :22 / deleteObjectAccessState :38 / Commit :40） | ✅ **真**（WithEvent 变体即扩展此事务） |
| outbox 迁移为 0041、双方言、**无** `UNIQUE(origin_kind,origin_id)`，去重 = 行 id | `migrations/{sqlite,postgres}/0041_event_outbox.{up,down}.sql`（`id` AUTOINCREMENT 注释明示权威去重键；无任何 UNIQUE） | ✅ **真**。根因验证：`RestoreObject`（`sql_objects_maint.go:247-273`）按 id 原地 UPDATE 复用行 → UNIQUE(event_type,origin_id) 会让 restore→re-delete 第二次删除 500 |
| `repository.go` `Event` 无版本；版本命名空间在 `OutboxEventType` | `repository.go:172-194`；`event_outbox.go:17-26`（`EventTypeFileDeleted11="vault.file.deleted@1.1"`、`EventTypeFileNotify11="vault.file.notify@1.1"`） | ✅ **真** |
| G-1：`occurred_at` 不在 `deletedFact` 也不在 goldens | `payload.go:33-52`（deletedFact 字段序无 occurred_at）；`schema_test.go:15/:17`（golden 常量）、:50-55（必填清单） | ✅ **真**（唯一未满足的验收字面项，§4 给出补齐设计） |
| AC 映射的 10 个测试存在且绿 | `event_outbox_test.go`（3）、`event_outbox_relay_test.go`（3）、`schema_test.go`（3）、`integration/fullserver_test.go`（2）、`integration/admin_files_delete_test.go`（2） | ✅ **真**。实测 `go test -count=1 ./internal/events/ ./internal/repository/ ./internal/service/` 全绿（12.9s/35.9s/31.0s） |

**结论：6/6 引证为真，1 个缺口（G-1）属实。** 本设计在其上只做两件事：把已落地机制固化为可复验的设计契约（§1–§3），并为 G-1 给出最小补齐设计（§4–§7）。

---

## 1. API 变更（已合入 = 契约；G-1 增量为 §4）

### 1.1 Repository 接口（`internal/repository/repository_interface.go`，全部已合入）

```go
// 事务侧（删除 + 审计 + outbox 事实同事务，FR-1）
SoftDeleteObjectWithEvent(ctx, tenant, bucket, key, entry AuditEntry, facts []OutboxFact) error            // :32
SoftDeleteObjectByIDWithEvent(ctx, id int64, entry AuditEntry, facts []OutboxFact) error                    // :33
HardDeleteObjectWithEvent(ctx, tenant, bucket, key, entry AuditEntry, facts []OutboxFact) error            // :35

// relay 侧（claim → deliver → complete，FR-3）
ClaimEventOutbox(ctx, owner, token string, limit int, ttl time.Duration) ([]EventOutboxRow, error)         // :106
CompleteEventOutbox(ctx, id int64, owner, token string) error                                               // :107
RetryEventOutbox(ctx, id int64, owner, token, lastErr string, next time.Time, maxAttempts int) error       // :108
PruneEventOutbox(ctx, deliveredBefore, failedBefore time.Time) (int64, error)                               // :109
HasEventOutboxFact(ctx, originID int64, eventType OutboxEventType) (bool, error)                            // :110
CountEventOutbox(ctx) (int64, error)                                                                        // :111
```

新增类型：`OutboxEventType`（`event_outbox.go:17-26`，CHECK 限两个值）、`OutboxFact`（:31-37）、`EventOutboxRow`（:40-50）。事实校验 `validateOutboxFacts`（:35-60）在删除事务**内**执行：type 白名单、`origin_id>0`、tenant 非空、payload ∈ 1..1 MiB 且 JSON `schema_version=="1.1"` —— 任一失败整体回滚。

### 1.2 Service 侧（`file_delete.go`）

- `deleteFacts`（:123-148）：一次删除两条事实（deleted@1.1 + notify@1.1），payload 删除时刻构建、自包含；`deleteAuditEntry`（:100-120）提供 L0 审计行（actor 取 `access.PrincipalFrom(ctx)`，空值合法）。
- `s.emit`（:53/:92）保留为提交后 legacy 尽力广播（SSE 回放等既有消费者依赖 `object_events` 流，见 §3.6）。

### 1.3 Events 包（`internal/events`）

- `BuildDeletedFact`/`BuildNotifyFact`（`payload.go:109-165`）：字节稳定构建器（字段序固定 → golden 精确）。
- `EventOutboxRelay`（`event_outbox_relay.go:33-49`）+ `EventOutboxRelayOptions`：claim → deliver → complete → prune 循环。
- L2 端口 `AuditSink`（`audit_sink.go:36-38`）：`DeliverDeleted(ctx, tenant, factID, payload)`，at-least-once 契约（C9-C11），`X-Audit-Fact-Id` 幂等头。

### 1.4 配置（`internal/config/config_event_outbox.go`，9 项）

`EVENT_OUTBOX_ENABLED`(默认 true，只 gate relay 循环，**enqueue 永不 gate**) · `POLL_INTERVAL_MILLIS`(1000) · `BATCH_SIZE`(32) · `CLAIM_TTL_SECONDS`(30) · `HTTP_TIMEOUT_SECONDS`(5) · `MAX_ATTEMPTS`(10) · `DELIVERED_RETENTION_HOURS`(24) · `FAILED_RETENTION_HOURS`(168)。`Validate()` 强制 **TTL > 2×timeout**（防无崩溃并发重复 POST 的阻塞项）。

### 1.5 G-1 增量（唯一新 API 变更）

`deletedFact` 增加**必填**字段（§4）：

```go
// payload.go deletedFact :33-52，插在 actor 与 reason 之间（方向原文 actor/occurred_at 相邻）
OccurredAt string `json:"occurred_at"` // 删除事务内的发射时刻，RFC3339Nano
```

`BuildDeletedFact` 内 `OccurredAt: time.Now().UTC().Format(time.RFC3339Nano)`（与仓库时间格式统一，I1）。**schema_version 保持 "1.1"**（理由见 §3.3）。

---

## 2. 兼容性约束

| # | 约束 | 依据/强制点 |
|---|------|------------|
| C1 | `event_type` 白名单（CHECK）锁死两个值；新增事实类型 = 迁移 + `validateOutboxFacts` 双改 | `0041 up.sql` CHECK；`event_outbox.go:40-42` |
| C2 | `RestoreObject` 复用 objects 行 id ⇒ **禁止**给 event_outbox 加 `UNIQUE(event_type,origin_id)`；去重键 = outbox 行 id + status 谓词 | `sql_objects_maint.go:247-273`；`event_outbox.go:17-26` 注释 |
| C3 | payload 字节原样（TEXT 非 jsonb）；relay **永不重派生**（投递即 enqueue 时刻的字节）——升级期间新旧 payload 形状可共存 | `0041 up.sql` 注释；`event_outbox_relay.go:235-265`（E7 修复） |
| C4 | 恰好一次仅在 complete 后成立；deliver→complete 窗口显式 at-least-once —— 接收方必须幂等（`X-Audit-Fact-Id` / S3 sequencer） | `event_outbox_relay.go:16-22`（D7 显式语义） |
| C5 | L0 `audit_log` 恒 on（FR-1 同事务）；L2 sink 为 nil 或 `ErrSinkNotBound` → complete，L0 仍权威 | `audit_sink.go:19-34`；relay `deliverDeleted` |
| C6 | `EVENT_OUTBOX_ENABLED=false` 仅停 relay：enqueue 继续、积压 FIFO 排空；不可把 false 当未设置（`withDefaults` 故意不 default Enabled） | `config_event_outbox.go:31-42`；`workers.go:163-202` |
| C7 | TTL 必须 > 2×timeout（启动即失败，`Validate()`）；文档化在飞上限 targets×timeout < TTL（默认 30s 覆盖 ≤3 顺序 POST） | `config_event_outbox.go:67-80` |
| C8 | 多副本共享 event_outbox 表：prune 保留期 = **全舰队最小值**（最激进者胜出）；各副本须配置一致 | `docs/configuration.md:360` |
| C9 | legacy 路径（`DeleteVersion`/delete-marker/隔离保留清除）**不产生 outbox 行**；Notifier `HasEventOutboxFact` 条件跳过保证这些路径不丢 bus 投递、也不双投 | `notifier.go:60-86`；G-3 |
| C10 | SQL 新语句遵守 I1（`$N` 不可复用、rebind）；无新 schema 变更时不碰迁移文件（I2 单向、down 永不自动执行） | AGENTS.md §4 |

---

## 3. 失败模式（含处置，全部有落地机制或测试）

| # | 故障 | 行为 | 恢复/处置 | 验证 |
|---|------|------|----------|------|
| F1 | enqueue 事务内任何失败（非法 payload、审计插入失败、0 行竞态） | **整体回滚**：对象未删、0 outbox 行、0 审计行 | 调用方得到 error；重试整个删除 | `TestDeleteObjectWithEvent_OneTx` 回滚分支、`TestHardDeleteAuditInsertFailure_RollsBack`、`TestDeleteObjectWithEvent_EventTypeFilteredRows` |
| F2 | 崩溃于 commit 后、claim 前 | 行滞留 `pending` | relay 下次轮询 claim（`available_at_ns<=now`） | `TestEventOutboxClaimLeaseExpiryRedelivers`（owner A 不 complete → 租约过期 → B 重 claim） |
| F3 | 崩溃于 claim 后、complete 前 | 行 `inflight`、租约过期 | 重 claim → **可能重复 POST**（at-least-once 窗口，C4）；接收方幂等 | `TestComposition_MidClaimRestartRedeliversOnce` |
| F4 | complete/retry 栅栏失守（`ErrClaimLost`） | warn + `IncEventOutboxClaimLost`，**不循环重试** | 租约重 claim 是唯一恢复路径（循环重试会双重排程） | `TestOutboxRelay_ClaimLostLeadsToReclaimNotDoubleSchedule` |
| F5 | L2 401/403（`ErrSinkUnauthorized`） | **立即终态 failed**，不退避（claim 已耗 attempts 预算） | 7 天保留后 prune；运维修凭据后手动处置 | `failImmediately`（relay :231-251） |
| F6 | L2/通知目标 5xx、传输错误 | 指数退避 1s→2×→5min 上限 + [0.75,1.0) 抖动，10 次 → 终态 failed | prune（failed 168h）；告警遥测 | `TestOutboxRelay_RetriesOn5xx`、`TestEventOutboxBackoffBounds` |
| F7 | 通知规则解析失败（DB 错误） | 整条事实退避重试（不部分投递） | 同上 | `deliverNotify` :235-265 |
| F8 | 无匹配规则 / 无 L2 sink | complete（事实仍被消费，规则可后配） | 设计行为；`CountEventOutbox` 启动日志 = relay 关闭时唯一深度信号 | `workers.go:206-217` |
| F9 | relay 关闭期间删除 | enqueue 不 gate，积压累积 | 重启 relay → FIFO（available_at_ns, created_at_ns, id）排空 | `TestComposition_AdminFilesDeleteEndToEnd` leg2（relay-down/start-later） |
| F10 | TTL ≤ 2×timeout 配置 | **启动失败**（`Validate`），不进入降级运行 | 修正配置；文档警告 targets×timeout < TTL | `config_event_outbox.go:67-80` |
| F11 | 未知 fact type 出现在表中 | relay default 分支退避重试 → 终态 failed | 属编程错误（CHECK 应拦截）；观测 `IncEventOutboxFailed` | relay `deliverFact` :182-195 |
| F12 | 多副本并发 claim | PG `FOR UPDATE SKIP LOCKED` / SQLite 事务内逐行 fencing UPDATE | 返回短批，调用方容忍 | `claimEventOutboxPostgres/SQLite` |
| F13 | Postgres claim OR 谓词走 seq-scan | 接受（outbox 规模有限，迁移注释明示） | 规模增长时改分区/索引 | `0041 up.sql` 注释 |
| F14 | restore→re-delete 复用行 id | sequencer 每次发射 `crypto/rand` 重生成（**不用 obj.ID**）；去重 = outbox 行 id | 无 UNIQUE 冲突（C2） | `TestEventSchema_SequencerUniquePerCall` |
| F15 | 删除响应被 relay 拖慢 | **不可能**：relay 异步 `Run`，删除路径只 commit 事务即返回；bus 广播背压 drop 不阻塞 | — | `TestDeleteResponse_DoesNotBlockOnDelivery`（信号式）、`bus_test.go:174-192` |

---

## 4. G-1 补齐设计（唯一未满足验收字面项）

**现状证据：** `deletedFact`（`payload.go:33-52`）无 `occurred_at`；golden 常量（`schema_test.go:15/:17`）与必填清单（:50-55）均无。时间仅存于 `event_outbox.created_at_ns` 与 L0 `audit_log` 时间列。

**设计决策（倾向证据 spec §4 的路线 (a)，additive）：**

1. **字段**：`deletedFact` 增 `OccurredAt string \`json:"occurred_at"\``（必填、无 omitempty），字段序固定插在 `actor` 与 `reason` 之间（方向原文字段清单中 actor/occurred_at 相邻；`reason` 保持末位 omitempty）。
2. **取值**：`BuildDeletedFact` 内 `time.Now().UTC().Format(time.RFC3339Nano)`（发射时刻；与仓库时间格式统一，I1）。**不得**在投递期改填 `created_at_ns`——payload 字节稳定性（C3）与"删除时刻自包含"（FR-2）是根本约束。
3. **schema_version 保持 "1.1"**：
   - `validateOutboxFacts` 要求 `schema_version=="1.1"`（:62-71）—— bump 到 1.2 会改变"合法 payload"谓词，属破坏性；
   - 该事实类型与 outbox 同发布窗口合入、无已部署的字节精确消费者（deleted@1.1 是 forward-compat 记录，D3；L2 为 opt-in 且把 payload 当不透明字节 + `X-Audit-Fact-Id` 去重）—— 发布前加必填字段安全；
   - 接收方按**宽容读**处理：`occurred_at` 缺失不得导致 L2/接收方失败（见 §5 迁移步骤的混合形状窗口）。
4. **同步更新**：golden 常量（`schema_test.go:15/:17`）、必填清单（:50-55）、`Deleted11Envelope`（:96）加 `occurred_at`；`TestEventSchema_GoldenJSON` 字节断言随之更新。notify@1.1 **不加**（方向原文字段清单仅限 deleted@1.1；S3 形状已有 `sequencer` 时序语义）。

**不做（明确）：** 不 bump schema 版本；不改 `event_outbox` 表（无需 SQL 迁移，I2 零触碰）；不给 notifyFact 加 eventTime（独立方向候选）。

---

## 5. 迁移步骤

**A. 已合入状态的部署（从 <0041 升级）：**
1. 迁移 0041/0042 由 `repo.Migrate` 启动时按版本自动应用（双方言 embed，I2）；**单二进制部署，无顺序问题**。
2. 注意：`insertOutboxFacts` 依赖 `event_outbox` 表——迁移缺失会导致**删除整体失败**（回滚），不会静默丢事件；先升级二进制即同时带迁移。
3. `EVENT_OUTBOX_*` 配置按 `docs/configuration.md:354-361` 核验（尤其 TTL > 2×timeout，F10 启动即失败）。

**B. G-1 补齐的发布（payload-only，无 schema 迁移）：**
1. 单提交改 `payload.go` + `schema_test.go` goldens/必填清单/信封断言 + 新断言（§6 T-4）。
2. **滚动升级安全**：旧版本已 enqueue 的 pending 行按原字节投递（relay 不重派生，C3）——升级窗口内存在 `occurred_at` 缺失/存在的混合形状；接收方须把该字段当可选（宽容读），L2 去重不受影响（`X-Audit-Fact-Id`）。
3. 无需 down.sql（I2：down 永不自动执行；payload 变更无可回滚的 schema）。

**C. 回滚（如需要）：**
1. 二进制降级：旧代码无 outbox 事实写入（用回 `DeleteObject`/`SoftDeleteObject` 纯事务），pending 行滞留 `event_outbox`。
2. 若运维手工执行 `0041 down`：pending 事实丢失（delete 事件属生命周期通知，非计费数据，可接受）；`0042` 在 0041 之后，down 顺序为 0042 → 0041。

---

## 6. 可测试验收映射（AC-1..AC-4 原样保留 + G-1 新增）

| 验收（方向原文） | 测试断言 | 位置 | 状态 |
|---|---|---|---|
| **AC-1** 单事务回滚：零 outbox 行 + 零广播 | 合法路径硬/软删 → 对象行消失、恰 2 事实行、delivered 空；非法事实 → 整体回滚（对象在、0 outbox 行）；审计插入失败 → 整体回滚；并发双删 → `ErrNotFound` 无幽灵行；拒绝路径零行 | `TestDeleteObjectWithEvent_OneTx`、`TestHardDeleteAuditInsertFailure_RollsBack`、`TestDeleteObjectWithEvent_EventTypeFilteredRows`、`TestDeleteDenied_NoOutboxRow_ObjectUntouched` | ✅ 已绿 |
| **AC-1 零广播** | **新增 T-1（可选→建议必做）**：service 层挂 bus 订阅者 + 强制回滚（非法 fact）→ 断言订阅通道零事件（`s.emit` 在 WithEvent error 之后不可达，`file_delete.go:46-54` 结构保证，测试固化为防回归） | `internal/service/file_delete_test.go`（新） | 🔶 新增 |
| **AC-2** 崩溃恢复 + 幂等 claim/ack | 租约过期重 claim；claim-lost 不双排程；claim→POST 恰 1 次→complete→重复 claim 无行；5xx 退避有界；relay 重启后仍恰一次 | `TestEventOutboxClaimLeaseExpiryRedelivers`、`TestOutboxRelay_ClaimLostLeadsToReclaimNotDoubleSchedule`、`TestOutboxRelay_DeliveryLifecycle`、`TestOutboxRelay_RetriesOn5xx`、`TestEventOutboxBackoffBounds`、`TestEventOutboxRetryBackoffAndTerminalFailed`、`TestComposition_MidClaimRestartRedeliversOnce` | ✅ 已绿 |
| **AC-3** golden JSON（字段含 **occurred_at**） | golden 逐字节相等；必填字段集 + `object_id==obj.ID`；信封字段全携带；notify 自包含（key/size/eTag/versionId/sequencer）；sequencer 每调用唯一 | `TestEventSchema_GoldenJSON`、`TestEventSchema_RequiredFields`、`TestEventSchema_Deleted11Envelope`、`TestEventSchema_SequencerUniquePerCall` | ✅ 已绿（**除 occurred_at**） |
| **AC-3 补齐** | **新增 T-2/T-3/T-4**：T-2 golden 常量含 `"occurred_at"` 且与构建器输出逐字节一致；T-3 必填清单含 occurred_at；T-4 断言 `occurred_at` 为可解析 RFC3339Nano 且非零（发射时刻） | `internal/events/schema_test.go`（更新 + 新） | 🔶 新增 |
| **AC-4** 组合 e2e：每类型恰一行 + 审计/通知各恰一次 + Publish 不阻塞 | admin 删除 → 每类型恰 1 行 pending；全服务器 + relay → 两事实 delivered、通知目标收到 notify@1.1；relay 关闭 → 删除成功 + 稍后启动排空；L2 收到 deleted@1.1 + `X-Audit-Fact-Id`；bus 背压 drop 不阻塞、删除响应不等待 relay | `TestAC2_AdminDelete_EventTypeFilteredState`、`TestComposition_AdminFilesDeleteEndToEnd`、`TestAdminDelete_EmitsExactlyOneDeletedFact`、`TestFileServiceDelete_WritesAuditRow`、`TestOutboxRelay_DeliversDeletedFactToL2`、`TestDeleteResponse_DoesNotBlockOnDelivery`、`bus_test.go:174-192` | ✅ 已绿 |

**复现命令：**
```bash
go test -count=1 ./internal/events/ ./internal/repository/ ./internal/service/   # AC-1..AC-3 组件层（实测全绿）
go test -count=1 ./internal/integration/ -run "TestAC2_AdminDelete_EventTypeFilteredState|TestComposition_AdminFilesDeleteEndToEnd"
go test -count=1 ./internal/integration/ -run "TestDeleteResponse_DoesNotBlockOnDelivery|TestComposition_MidClaimRestartRedeliversOnce"
make check
```

---

## 7. 范围边界（与 spec §5 一致，明确不做）

- `DeleteVersion`/delete-marker/隔离·保留清除路径的 outbox 化（G-3）——方向锚定 `FileService.Delete`。
- `Event`/`EventType` 结构体版本化；`object_events` 与 `event_outbox` 双写合并（G-2，legacy 流服务 SSE 回放）。
- Webhook 管线/通知规则引擎改造；actor 身份管线；claim 并发特性扩展。
- notifyFact 增 `eventTime`/`occurred_at`（S3 形状已有 sequencer；独立方向候选）。
