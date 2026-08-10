# 方向：把 adapter 治理 e2e 扩展到 fail-closed DELETE 流（多事件捕获、拒绝删除零行、已删除 origin 的 T-4 gap 复用）

> **模块：** `internal/api/s3compat`（测试基建 `audit_governance_e2e_test.go` + `authz_gate_test.go`） · **来源分析：** `docs/auto/analyses/internal-api-s3compat-eeefa063.json`（方向 1） · **日期：** 2026-08-08 · **HEAD：** `15763e28`
> **评分：** 价值 9 / 风险降低 8 / 工作量 3 / 置信度 9
> **状态声明：** 本文是**验收契约**而非绿地设计：逐条核验方向引证（§1）、以**实证**登记 DELETE 流在各表上的真实写入面（§2，含两处对方向断言的修正）、原样保留三条验收检查并映射为可执行测试（§4）。超范围项一律不做（§5）。

---

## 1. 证据核验（方向引证逐条对照当前 HEAD）

| # | 方向引证 | 当前 HEAD 位置 | 核验结论 |
|---|---------|---------------|---------|
| E1 | `audit_governance_e2e_test.go:184-260` — 仅 PUT→file.created 的 fact-ID e2e，gap 复用只覆盖 file.created | `TestS3CompatAuditGovernanceDeterministicFactID` :138-235（REQ-1/AC-1 单行断言、REQ-2 时间规范化、REQ-3 行级 recompute :180-183、F10 claim :187-193、AC-2 prune→gap→byte-identical 复用 :202-234）；`TestS3CompatAuditGovernanceCaptureInactive` :240-269（F4 负向）。**整个文件无任何 DELETE 用例** | ✅ **语义与引用一致，行号漂移**（184-260→138-235）：PUT 路径 pin 齐全，DELETE 流从未对 `audit_governance_outbox` 断言 |
| E2 | `authz_gate_test.go:360,392-394` — `assertZeroSideEffects` 只查 object_events/audit_log，从不查治理行 | 调用点 :360；定义 :392-403：`object_events WHERE type='deleted'` + `audit_log WHERE action='file.delete'` 两计数，**无 `audit_governance_outbox` 查询** | ✅ **与引用一致**（行号精确）。"exactly-N rows" F5 检测器在门禁流上确实未验证 |
| E3 | `authz.go:31-45` — `authorizeDelete` fail-closed 门 | 函数 :27-45（注释 :23-25）：provider nil / provider error / 非 allow 一律 deny；provider error 仅 Warn 日志，拒绝原因 Debug-only | ✅ **行号漂移**（31→27 起），语义成立：`denyAllProvider` → 403，任何 service 调用都不发生 |
| E4 | `file_delete.go:130-140` — 软删除发射恰好 2 个 outbox 事实（deleted@1.1 + notify@1.1，同 origin id） | `deleteFacts` :123-140（:131 `EventTypeFileDeleted11`、:137 `EventTypeFileNotify11`，`OriginID: obj.ID` 相同）——**但这 2 个事实经 `insertOutboxFacts`（event_outbox.go:229-243）写入 `event_outbox` 表，在删除事务内完成，从不经过 `InsertEventWithGovernance`** | ⚠️ **行号精确、载体错位**：2 事实属于 **`event_outbox`**，不是 `audit_governance_outbox`（见 E7 实证） |
| E5 | `audit_governance_write.go:53-84,111-137` — `InsertEventWithGovernance`/`EnqueueAuditGovernance`，`DeterministicFactID` 调用点、event-type 无关 | `InsertEventWithGovernance` :53-105（store-authoritative `DeterministicFactID` :75-78，`RETURNING id, created_at` 规范化 occurred）；`EnqueueAuditGovernance` :111-137（:119-121 store 重算 ID，`ON CONFLICT (origin_kind,origin_id) DO NOTHING` → 重复入队 (false,nil)） | ✅ **与引用一致**。`InsertEventWithGovernance` 确实 event-type 无关——**但每次仅随一个 bus 事件调用一次** |
| E6 | `audit_governance_claim.go:52-56,78-80,216-219` — claim/lag 谓词 | PG claim 子查询 `o.delivered_at_ns=0 AND o.failed_at_ns=0` :52-53；SQLite claim :78-80（同谓词，:110 per-id 更新同谓词）；`OldestPendingAuditGovernance` :211-221（:216-219 同谓词） | ✅ **与引用一致**。`FailAuditGovernance`（:186-204）落 `failed_at_ns` 后，行既不可 claim 也不进 lag——对 delete 行同样成立（§4 T-3 实证） |
| E7 | **方向推断："allowed S3 DELETE → exactly 2 governance rows"** | `audit_governance_outbox` 全库仅 3 个写入点（grep 实证）：`InsertEventWithGovernance`（wrapped `InsertEvent`，auditgovernance/repository.go:43-44）、`RecordAuditWithGovernance`（admin 路径）、`EnqueueAuditGovernance`（relay.go:40 gap 路径）。DELETE 流在 bus 上**只发射 1 个事件**（`emit` file.go:308-325 唯一 `Publish` 调用点；file_delete.go:53/92、delete_marker.go:58、file_delete.go:212 各一次 `EventDeleted`） | ❌ **推断错误**（实证见 §2.1）：DELETE 贡献 **1 行** `file.deleted`（origin=object_events 的 deleted 行），**不是 2 行** |
| E8 | **补充核验（方向未引，影响 T-4 可测性）**：删除的 `audit_log`（file.delete，admin 类）在原子路径**不产生** admin 治理行（`insertAuditEntry` 在删除事务内直写，绕过 `RecordAuditWithGovernance`），但 gap 查询 `listGovernanceAuditGaps`（write.go:193-216）会把它列为 gap | 实证：prune delete 行后 `ListAuditGovernanceGaps` 返回 **2 个 gap**（admin `file.delete` + file `file.deleted`），`len(gaps)==1` 断言会失败 | ⚠️ 影响验收写法：gap 必须按 `Action` 谓词选取，**不得断言 gap 总数**（§4 T-4.2） |
| E9 | **补充核验（测试基建缺口）**：`newGovernanceE2EServer`（e2e :67-111）把 `allowAllProvider{}` 硬编码进 `NewRouter(svc, nil, authz)` 与服务侧 `WithAuthorizer`；`NewRouter` 第三参即 adapter 门禁 provider（router.go:14-15） | 门禁用例需要 router 注入 `denyAllProvider{}` → 需测试侧小重构（§3 FR-5） | ✅ 仅测试基建，无生产代码改动 |
| E10 | **补充核验（状态码）**：`handler.go:265-293` `DeleteObject` → `deleteS3Object`（delete.go:15-38，未版本化桶 → `svc.Delete(..., hard=true)`）→ :292 `WriteHeader(http.StatusNoContent)` | 允许删除 → **204**；版本化桶 → delete-marker / ?versionId 路径同样单次 emit | ✅ 与断言预期一致 |

**方向问题陈述核验（当前状态）：**

| 陈述 | 核验 |
|------|------|
| "The module's only governance e2e exercises PUT→file.created exclusively; the DELETE path is never asserted against audit_governance_outbox" | ✅ **成立**（E1） |
| "authz_gate_test.go assertZeroSideEffects checks object_events/audit_log only, never governance rows" | ✅ **成立**（E2） |
| "a soft delete emits exactly 2 outbox facts (deleted@1.1 + notify@1.1, same origin id)" | ✅ **成立——但载体是 `event_outbox`**（E4），与治理 outbox 无涉 |
| "an allowed S3 DELETE should produce exactly 2 governance rows" | ❌ **不成立**：产生 **1 行** `file.deleted`（E7 + §2.1 实证）。PUT+DELETE 全程后治理表总行数恰好为 2（created+deleted）——方向数字巧合命中总数，但机理归因错误 |
| "T-4 gap reconcile for deleted origins is unpinned at adapter level" | ✅ **成立**：AC-2 只覆盖 file.created；file.deleted 的 byte-identical 复用此前无任何测试 |
| "the origin object_events row survives soft delete" | ✅ **成立**：object_events 只增不删；probe 中 deleted 行 origin_id=2 的 event 行仍可 JOIN（§2.1） |

---

## 2. 现状：DELETE 流在各持久化面上的真实写入（已实证）

以生产形状装配（`newGovernanceE2EServer`，binding=active，router provider=allowAll）：`PUT /b/k.txt` → `DELETE /b/k.txt` 后：

| 表 | 行数 | 内容 |
|----|------|------|
| `audit_governance_outbox` | **2 行总计**（DELETE 贡献 **1 行**） | `file.created`（origin=object_events 行 1，来自 PUT）+ `file.deleted`（origin=object_events 行 2，来自 DELETE）；均 `origin_kind='file'`、`tenant_id='default'`、`fact_kind='file'`；**无 admin 行**（删除的 audit_log 原子路径不产治理行，E8） |
| `object_events` | 2 行 | `created` + `deleted`，各是治理行 origin |
| `event_outbox` | 2 行 | `vault.file.deleted@1.1` + `vault.file.notify@1.1`，`origin_id`=**objects 行 id**（≠治理 origin_id）——方向"2 facts"的真实落点 |
| `audit_log` | 1 行 | `file.delete`（删除事务内直写） |

**已删除 origin 的 T-4 复用（实证）：** prune 治理表 `action='file.deleted'` 行 → `ListAuditGovernanceGaps` 返回 2 个 gap（admin `file.delete` + file `file.deleted`，merge 序 admin 在前，write.go:175-192）→ 按 `Action=="file.deleted"` 重建 fact 入队 → **ID 字节级一致**（`38f58845342672882d35b1adc1c4412a` → `38f58845342672882d35b1adc1c4412a`）；重复入队折返 `(false,nil)`。**claim/lag（实证）：** claim 全部 2 行 → `CompleteAuditGovernance`(created 行) → `FailAuditGovernance`(deleted 行) → 再 claim 0 行、`OldestPendingAuditGovernance` 排除死行。

**拒绝路径（门禁先行）：** `denyAllProvider` → 403 `AccessDenied`，`authorizeDelete` 在 `svc.Delete` 之前返回 → 上述四表**零新增**（event_outbox/object_events/audit_log 零行，治理表维持 PUT 的 1 行不变）。

> 实证方法：临时 probe 测试（复用 `newGovernanceE2EServer` + 裸 SQLite 第二连接）逐项打印以上行数/ID/gap/claim 结果后删除，未落库。所有断言在当前 HEAD 上**一次性通过**——本规格的验收全部可在不改生产代码的前提下实现。

---

## 3. 需求规格

### FR-1：治理捕获在 DELETE 流上的行数与内容契约（修正后）
允许的 S3 DELETE 经生产形状装配（active binding）后，`audit_governance_outbox` 的 DELETE 贡献**恰好 1 行**：`action='file.deleted'`、`origin_kind='file'`、`tenant_id='default'`、`fact_kind='file'`，origin = object_events 的 `deleted` 行，`occurred_at_ns` 规范化自该行 `created_at`（REQ-2 parity），ID = 32-hex `DeterministicFactID` 行级重算。PUT+DELETE 全程后表内**恰好 2 行、且无 admin 类行**（锁定"删除的 audit_log 不产治理行"这一原子路径行为，防止未来误引入双写）。方向"2 facts"的真实载体 `event_outbox`（deleted@1.1 + notify@1.1，origin=objects 行 id）同步锁定，两表不混淆。

### FR-2：被拒 DELETE 零治理副作用（F5 检测器扩展到门禁流）
`denyAllProvider` 下 DELETE → 403；`audit_governance_outbox` 无 `file.deleted` 行、总数不变（在治理装配的 e2e 中即"PUT 后维持 1 行"）；`object_events` 无 `deleted` 行、`audit_log` 无 `file.delete` 行、`event_outbox` 零行（E2 现有断言 + 治理表扩展）。`assertZeroSideEffects` 增加治理表检查，使既有门禁测试（raw server，治理表恒 0）与新的治理 e2e 共用同一检测器。

### FR-3：已删除 origin 的 T-4 gap 复用（adapter 级 AC-2 的 DELETE 对偶）
prune `file.deleted` 治理行 → `ListAuditGovernanceGaps` 的 file 类 gap 携带 byte-identical 字段（OriginKind/OriginID/OccurredAt/action）→ 重建 fact 入队 → 原 ID 字节级重现；重复入队折返 `(false,nil)`。**gap 选取必须按 `Action` 谓词**（E8：admin `file.delete` gap 必然同在结果中，总数断言不可用）。

### FR-4：DELETE 行的 claim/lag 终态（T-3，与 PUT 路径 pin 对偶）
`FailAuditGovernance` 落在 delete 行后：`ClaimAuditGovernance` 不再返回该行（E6 谓词 `failed_at_ns=0`）、`OldestPendingAuditGovernance` 排除该行（与 repository 层 pin `audit_governance_test.go:216,295,511-542`、`audit_governance_pending_idx_test.go:212-234`、`cumulative_window_test.go:186,257` 同构，在 HTTP seam 处复现）。

### FR-5：测试基建（仅测试代码）
`newGovernanceE2EServer` 增加可注入 adapter provider 的变体（`NewRouter(svc, nil, authz)` 第三参，router.go:14-15）；现有签名委托之并保持 `allowAllProvider{}`（零迁移）。无任何生产代码改动。

---

## 4. 验收标准（可测试）

> 落点：新用例放 `internal/api/s3compat/audit_governance_e2e_test.go`（复用 `newGovernanceE2EServer`/`governanceOutboxRow`/`e2eSourceID`/`e2eFactIDPattern`/`do`/`testShareSecret`/`assertAccessDenied`）；`assertZeroSideEffects` 扩展放 `authz_gate_test.go`。全部纯测试改动，`make check` 约束（gofmt/`go test ./...`）适用。

### T-4：允许的 S3 DELETE 流（capture + fact-ID + T-4 复用）

**T-4.1 capture 与确定性 ID：**
1. `newGovernanceE2EServer(t, "active")`；`PUT /b/k.txt` → 200；`DELETE /b/k.txt` → **204**（handler.go:292）。
2. 裸 SQL 断言（第二连接）：
   - `SELECT COUNT(*) FROM audit_governance_outbox` == **2**，且两行均 `origin_kind='file' AND tenant_id='default'`，action 集合恰为 `{file.created, file.deleted}`，`fact_kind` 全为 `file`（无 admin 行 —— FR-1）。
   - 定位 delete 行：`JOIN object_events e ON e.id=o.origin_id WHERE o.action='file.deleted'` → 断言 `e.type='deleted'`、`origin_id>0`、`occurred_at_ns == time.Parse(time.RFC3339Nano, e.created_at).UnixNano()`（REQ-2 parity）。
   - ID 形状：`e2eFactIDPattern`（`^[0-9a-f]{32}$`）且 `id == repository.DeterministicFactID(e2eSourceID(string(testShareSecret), "default"), "default", "file.deleted", "file", originID, created)`（REQ-3 行级重算，PUT 用例 :180-183 同构）；两行 ID 互异。
3. 载体澄清断言（FR-1）：`SELECT COUNT(*) FROM event_outbox` == 2，两行 event_type 为 `vault.file.deleted@1.1`/`vault.file.notify@1.1` 且 `origin_id` == objects 行 id（≠ 治理 origin_id）——锁定"2 facts 在 event_outbox，DELETE 治理贡献 1 行"。

**T-4.2 gap 复用（byte-identical）：**
1. 裸 SQL prune：`DELETE FROM audit_governance_outbox WHERE action='file.deleted'`。
2. `store.ListAuditGovernanceGaps(ctx, "default", 10)` → **不得断言 `len==1`**（E8：admin `file.delete` gap 同在）；按谓词选出 `Action=="file.deleted" && OriginKind=="file"` 的 gap，断言其 `OriginID`/`OccurredAt` 与 T-4.1 行一致（file.created 因未 prune 不出现）。
3. `factFromGap`-等价重建（PUT 用例 AC-2 :218-224 同构）→ `EnqueueAuditGovernance` → `(true, nil)`；重读该行 ID == T-4.1 原 ID **字节级一致**。
4. 同 fact 再次入队 → `(false, nil)`（`ON CONFLICT` 折返，E5）。

### T-3：DELETE 行的 claim/lag parity

1. 新 server，PUT+DELETE 同 T-4.1；`ClaimAuditGovernance(ctx, "e2e-owner", "e2e-token", 1, 10, time.Minute)` → 2 行（created + deleted，按 `Action` 区分）。
2. `CompleteAuditGovernance(created.ID, "e2e-owner", "e2e-token")`（移除活行，消除租约混杂）；`FailAuditGovernance(deleted.ID, "e2e-owner", "e2e-token", "probe-fail")`。
3. 换 owner 再 `ClaimAuditGovernance` → **0 行**（死行被 `failed_at_ns=0` 谓词排除，且无其他 pending）；`OldestPendingAuditGovernance` → `(_, false)`（死行不进 lag）。
4. 与 repository 层既有 pin 的行为一致（E6 引用），本检查在 HTTP seam 处复现。

### Gate：被拒 DELETE → 零治理副作用（F5 检测器上门的门禁流）

1. `newGovernanceE2EServerWithAuthz(t, "active", denyAllProvider{})`（FR-5 变体）；`PUT /b/k.txt` → 200；`DELETE /b/k.txt` → 403 `AccessDenied`（`assertAccessDenied`）。
2. 治理表：`SELECT COUNT(*) FROM audit_governance_outbox` == **1**（仅 PUT 的 `file.created`，**无 `file.deleted` 行**——被拒删除零新增）。
3. `object_events` 无 `type='deleted'` 行；`audit_log` 无 `file.delete` 行；`event_outbox` 对该 origin 0 行；`GET /b/k.txt` → 200（对象存活）。
4. 扩展 `assertZeroSideEffects`（authz_gate_test.go:392-403）：增加 `SELECT COUNT(*) FROM audit_governance_outbox WHERE action='file.deleted'` == 0 断言（E2 现有检查 + 治理表），使 raw-server 既有用例（TestDeniedDeleteWritesNoOutboxRows :324-390，治理表恒 0）与本 e2e 共用同一检测器；批量 `?delete` 双 key 全拒用例同断言。

---

## 5. 范围边界（明确不做）

| 项 | 理由 |
|----|------|
| 修改任何生产代码（service/repository/auditgovernance/s3compat handler） | 实证：当前 HEAD 行为已满足修正后验收（§2.1），全部断言一次性通过；本方向是**测试缺口**，不是行为缺口 |
| 把方向"2 governance rows"按字面实现为删除产 2 行 | 与仓库真实写入面（E7 实证：DELETE 产 1 行 `file.deleted`）矛盾；"2"仅作为 PUT+DELETE 全程总行数保留在 T-4.1 |
| 修复 admin-gap 不对称（删除的 audit_log 原子路径无 admin 治理行、gap 路径会补） | 既有 T-4 行为，方向未要求；仅在验收中以"按 Action 选取 gap、不断言总数"规避 |
| 版本化桶 delete-marker / `?versionId` 删除流的治理断言 | 同为单次 `EventDeleted` 发射（delete_marker.go:58、file_delete.go:212），机理一致，但方向验收仅覆盖单对象删除流；扩展留待后续 |
| 把治理断言下沉到 repository 层 / relay 层 | 方向定位在 **adapter e2e seam**；repository 层已有等价 pin（E6 引用） |

---

## 6. 实现指引（供验收后落地，非本规格交付物）

1. **基建（FR-5）**：`audit_governance_e2e_test.go` 中把 `newGovernanceE2EServer` 的装配抽为 `newGovernanceE2EServerWithAuthz(t, bindingState string, authz AuthorizationProvider)`（router 第三参，router.go:14-15），原函数委托 `allowAllProvider{}`。
2. **T-4/T-3 用例**：同文件新增 `TestS3CompatAuditGovernanceDeleteFlow`（T-4.1+T-4.2 合并或拆分均可，保持 §4 断言逐条对应）与 `TestS3CompatAuditGovernanceDeleteClaimLag`（T-3）；裸 SQL 走 `governanceOutboxRow` 同款第二连接模式（e2e :113-136 先例）。
3. **Gate 用例**：同文件新增 `TestS3CompatGovernanceDeniedDeleteZeroRows`（§4 Gate 1-3）；`authz_gate_test.go` 的 `assertZeroSideEffects` 增加治理表查询（§4 Gate 4）。
4. 复用既有常量/helper（`e2eSourceID` 金锚 D/E 已在 :167-176 pin，不得复制新实现）；`go test ./internal/api/s3compat/ -count=1` + `make check` 全绿。
