# 规格：quarantine 形状 admin gap 的扫描稳定确定性 fact ID（T-4 pin）

> **模块：** `internal/antivirus`（quarantine 行生产者）→ `internal/auditgovernance` + `internal/repository`（gap 路径与 fact ID 公式）
> **方向：** T-4 pin —— *quarantine-shaped admin gap must yield a scan-stable deterministic fact ID (guard the RFC3339Nano round-trip / now() fallback)*
> **状态：** 规格（未实现）· **基线：** HEAD `15763e2` + 未提交工作树（本文所有引用以**当前工作树**为准，漂移项已在 §1 逐条标注）
> **门禁：** `make check` 全绿 · 纯 stdlib（I6）· 单文件 ≤ 500 行 · **无生产代码变更**（纯测试 pin；"fails loudly" 由测试显式断言承担，见 REQ-3 与 §4 边界）

---

## 1. 证据复核（所有引用逐条对当前工作树复验）

| # | 规格引用 | 复核结论 |
|---|---------|---------|
| E1 | `internal/auditgovernance/fact_id_test.go:63-178` —— `assertGapEqualsAtomic` + admin/file 收敛 pin，**仅 synthetic 形状** | ✅ 精确。`assertGapEqualsAtomic` :63-76；`…_Admin` :77-109（`RecordAuditWithGovernance` 种子）、`…_File` :111-149（`InsertEventWithGovernance` 种子）、`PruneReenqueueSameID` :151-181、`TestNoUUIDInFactsGo` :183+。**全部种子经 governance 包装写路径（API-write 形状）；quarantine 的 direct-SQL 行（`SoftDeleteObjectByIDWithEvent`）零覆盖；`assertGapEqualsAtomic` 每次只传一个 `time.Now()`，同一 gap 从未用两个不同 now 复跑** —— 双 now 扫描稳定性无 pin |
| E2 | `internal/auditgovernance/facts.go:15-20` —— `now()` fallback | ✅ 精确。:17-19 `occurred, err := time.Parse(time.RFC3339Nano, entry.CreatedAt); if err != nil \|\| occurred.IsZero() { occurred = now.UTC() }` —— **静默吞掉解析失败** |
| E3 | `internal/auditgovernance/facts.go:102-112` —— `factFromGap` 单调用点公式复用 | ⚠️ **行漂移**：`factFromGap` 现位于 :48-72（ID 公式 :66-69，"Single call site for the formula" 注释 :64-65）。符号与语义不变 |
| E4 | `internal/repository/audit_governance_factid.go:10-38` —— `DeterministicFactID`，`time_bucket = Truncate(second)` | ✅ 精确。函数 :14-38；:31 `bucket := occurredAt.UTC().Truncate(time.Second).Unix()`；纯函数（无时钟/随机） |
| E5 | `internal/repository/audit_governance_write.go:38-44` —— store-authoritative 重算 | ✅ 精确。:42-44 原子路径重解析 `entry.CreatedAt`（REQ-2 注释）后重算 `fact.ID = DeterministicFactID(...)` |
| E6 | `internal/repository/audit_governance_write.go:126-133` —— Enqueue 重算 | ✅ 精确。`EnqueueAuditGovernance` :113-133；store-authoritative 重算 :124-126；:131-132 `return rows == 1, tx.Commit()`（RowsAffected 即 inserted 布尔） |
| E7 | `internal/repository/audit_governance_write.go:159-160` —— ON CONFLICT 去重 | ✅ 精确。`insertAuditGovernanceResult` :158-160：`WHERE NOT EXISTS (delivered_origins)` + `ON CONFLICT (origin_kind,origin_id) DO NOTHING`（仅 `ignoreDuplicate` 时追加；Enqueue 恒传 `true`，:127） |
| E8 | `internal/repository/event_outbox.go:21-30` —— `insertAuditEntry` RFC3339Nano | ⚠️ **文件引用错误**。`insertAuditEntry` 在 **`internal/repository/audit.go:17-34`**（:23 空值打 RFC3339Nano 戳）；`event_outbox.go:21-30` 现为 `OutboxEventType` 常量。**主张本身成立**：`SoftDeleteObjectByIDWithEvent`（event_outbox.go:186-227）在 :220 调 `insertAuditEntry`；quarantine 行即由此写入 |
| E9 | quarantine 行形状（actor=system:antivirus, action=file.delete, detail=av_infected） | ✅ `internal/service/object_worker.go:94-109` `quarantineAuditEntry`：`Action=repository.AuditActionFileDelete`（audit.go:13 "file.delete"）、`Target=bucket+"/"+key`、`Detail="av_infected"`（:41 `quarantineReason`）；actor 由 `internal/access/permissions.go:12` `SystemActorAntivirus` 钉死（antivirus/worker.go:153 注释） |
| E10 | gap 扫描解析（fallback 的使能点） | ✅ **补证**：`audit_governance_write.go:258` `gap.OccurredAt, _ = time.Parse(time.RFC3339Nano, occurred)` —— **解析错误被静默丢弃** → zero → `factFromGap` 置 `createdAt=""`（facts.go:57-60）→ `factFromAudit` 解析 "" 失败 → `now()` fallback（E2）。完整静默链成立 |
| E11 | 生产 reconcile 每次扫描传新 now | ✅ **补证**：`internal/auditgovernance/relay.go:38` `fact := r.redactor.factFromGap(gap, time.Now().UTC())` —— 逐 gap 现取时钟；确定性完全依赖解析成功 |
| E12 | 现有 pin 的覆盖边界（AC-1 仅 synthetic） | ✅ `fact_id_test.go` 三测试全部经 `RecordAuditWithGovernance`/`InsertEventWithGovernance`（store 自己打戳或显式 RFC3339Nano 字符串）；**无一行经 `insertAuditEntry`/direct-SQL**；`assertGapEqualsAtomic` 的 `time.Now()` 单值意味着即便 fallback 触发，单次断言也可能因同秒桶巧合通过（now 与 DB 戳同秒时 ID 不变）——**双 now 异桶约束缺失** |

**结论：问题主张全部成立。** 现有 AC-1 pin 只覆盖 synthetic API-write 形状；quarantine 行（唯一 born-on-gap-path 的 admin 行）的扫描稳定性、去重稳定性、解析成功路径均无 pin。`event_outbox.go:21-30` 与 `facts.go:102-112` 两处引用为**基线与工作树漂移**，语义不受影响，本文按工作树修正。

---

## 2. 需求

### REQ-1（对应验收 a）—— quarantine 形状 gap 的 fact ID 扫描稳定且等于公式重算

对一条 quarantine 形状的 audit_log 行（`actor=system:antivirus, action=file.delete, target=bucket/key, detail=av_infected`，`created_at` 为 RFC3339Nano），**任意两次** `ListAuditGovernanceGaps` + `factFromGap`（now 取不同秒桶）必须产出**逐字节相同**的 fact.ID，且该 ID 等于以 DB 字段重算的 `DeterministicFactID(source, tenant, "file.delete", "admin", rowID, parse(created_at).Truncate(time.Second))`。

**测试性验收（新增测试 `TestDeterministicFactID_QuarantineGapScanStable`，放入 `internal/auditgovernance/fact_id_test.go`，复用 `factIDStore` + `newRedactor(factIDHMACKey)` 既有 harness）：**
1. 种子经**真实 quarantine 写路径**：`UpsertObject`（event_outbox_test.go:32 同款）→ `SoftDeleteObjectByIDWithEvent(ctx, obj.ID, AuditEntry{Actor: "system:antivirus", Action: AuditActionFileDelete, Target: "b/k", TenantID: "acme", Detail: "av_infected"}, validDeleteFacts 同款 ≥1 条 1.1 payload)`（`validateOutboxFacts` 要求非空，event_outbox.go:61-64；event_outbox 行**不**抑制 admin gap——gap 扫描只 LEFT JOIN `audit_governance_outbox`/`delivered_origins`，audit_governance_write.go:234-243）。binding 用 harness 既有 "acme" active。
2. `nowA := time.Now()`、`nowB := nowA.Add(5*time.Minute)`（**必须异秒桶**——公式按秒截断，同桶 now 会让 fallback 路径也产出相同 ID，负控失效）。
3. 第一次扫描 `gapsA := store.ListAuditGovernanceGaps(ctx, "acme", 10)`：断言恰 1 条、`OriginKind=="admin"`、`OriginID==audit 行 id`、`OccurredAt` 非零且等于 `time.Parse(RFC3339Nano, created_at)`；`factA := redactor.factFromGap(gapsA[0], nowA)`。
4. **在 Enqueue 之前**复扫 `gapsB`（同一行仍为 gap），`factB := redactor.factFromGap(gapsB[0], nowB)`。
5. 断言 `factA.ID == factB.ID`（均匹配 `^[0-9a-f]{32}$`）；`factA.OccurredAt.Equal(factB.OccurredAt)`。
6. 重算断言：`want := repository.DeterministicFactID(factA.SourceID, "acme", "file.delete", repository.AuditOriginAdmin, gapsA[0].OriginID, parsed.Truncate(time.Second))`；`factA.ID == want`。

### REQ-2（对应验收 b）—— EnqueueAuditGovernance 对同一 gap 幂等去重

对 REQ-1 产出的 fact 连续两次 `EnqueueAuditGovernance`：第一次必须返回 `inserted==true`（RowsAffected==1），第二次必须返回 `inserted==false`（RowsAffected==0）；`audit_governance_outbox` 中 `(origin_kind='admin', origin_id=rowID)` 恰一行且 `id` 等于 REQ-1 的 fact.ID。

**测试性验收（同一测试函数内续接）：**
1. `inserted1, err := store.EnqueueAuditGovernance(ctx, factA)` → `inserted1==true, err==nil`。
2. `inserted2, err := store.EnqueueAuditGovernance(ctx, factB)` → `inserted2==false, err==nil`（第二个 fact 由第二次扫描产出，ID 已由 REQ-1 钉为相同——去重键 `(origin_kind, origin_id)` 与 ID 稳定共同被 pin）。
3. `claimed, err := store.ClaimAuditGovernance(ctx, "owner", "token", 1, 10, time.Minute)` → 恰 1 行、`claimed[0].ID == factA.ID`（outbox 行 ID 即交付 ID；唯一公开计数面）。

### REQ-3（对应验收 c）—— RFC3339Nano 形状走解析成功路径；截断时间戳必须"响亮失败"而非静默铸造时钟依赖 ID

(a) 对 RFC3339Nano 形状，pin 必须证明 `now()` fallback **未被触发**：`factA.OccurredAt` 等于 DB `created_at` 的解析值且**不等于** `nowA`/`nowB`。
(b) 对一条**故意截断/不可解析**的 `created_at`（如 SQLite 空格分隔形状 `"2026-08-08 01:17:41.123456789+00:00"` —— `time.Parse(time.RFC3339Nano, s)` 必失败，正是问题陈述点名的格式漂移形状；注：`"2026-08-08T01:17:41Z"` 秒截断**可**被 RFC3339Nano 解析，不作负控），测试必须显式断言 fallback 已触发且**时钟依赖可观测**：两次不同 now 的 `factFromGap` 产出**不同** ID（`t.Fatalf` 报错信息写明"不可解析 created_at 使 fact ID 变为时钟依赖，格式漂移必须在此失败而非静默改 ID"）。该断言即"响亮失败"的承担者——任何未来格式漂移翻转到 fallback，REQ-1 与 REQ-3 的组合必红一个，杜绝静默交付第二 ID。

**测试性验收（新增 `TestDeterministicFactID_QuarantineGapParseFallbackLoud`，或同一函数内 t.Run 负控段）：**
1. 解析成功路径：REQ-1 中追加显式断言 `factA.OccurredAt.Equal(parsed) && !factA.OccurredAt.Equal(nowA.UTC())`（fallback 未被咨询的直接证据）。
2. 负控种子：`store.RecordAudit(ctx, AuditEntry{TenantID: "acme", Actor: "system:antivirus", Action: AuditActionFileDelete, Target: "b/k", Detail: "av_infected", CreatedAt: "2026-08-08 01:17:41.123456789+00:00"})`（audit.go:30-33：非空 CreatedAt 原样入库，不重打戳——负控形状得以注入）。
3. 扫描定位该行 gap（按 `OriginID` 过滤，勿假设顺序），断言前置条件 `gap.OccurredAt.IsZero()==true`（store 层解析失败可观测，:258 吞错行为被显式记录）。
4. `factBadA := factFromGap(badGap, nowA)`、`factBadB := factFromGap(badGap, nowB)` → 断言 `factBadA.ID != factBadB.ID`，且 `factBadA.OccurredAt.Equal(nowA.UTC())`、`factBadB.OccurredAt.Equal(nowB.UTC())`（fallback 触发且时钟依赖，消息需含失败语义）。
5. 断言负控行**不**污染 REQ-1/REQ-2 的主断言（按 `OriginID` 隔离，或负控置于主流程之后）。

---

## 3. 验收对照（方向原文 → 本文）

| 方向验收 | 承载 | 可测试性说明 |
|---------|------|-------------|
| (a) 两次不同 now 扫描 + factFromGap 产相同 fact.ID，等于 `DeterministicFactID(source, tenant, 'file.delete', 'admin', rowID, occurredBucket)` DB 字段重算 | REQ-1 §2.1-2.6 | `nowA`/`nowB` 异秒桶硬约束（公式按秒截断）；重算用 `parsed.Truncate(time.Second)` 与公式 :31 一致；tenant 非空故 `normalizedTenant`/`defaultTenant` 恒等 |
| (b) EnqueueAuditGovernance 两次 → 恰一行 outbox（id 稳定，RowsAffected 1 后 0） | REQ-2 | 返回值即 `rows == 1`（:131-132）；行数断言走公开面 `ClaimAuditGovernance` |
| (c) RFC3339Nano 形状走解析成功路径（无 now fallback）；故意截断时间戳响亮失败而非铸造第二 ID | REQ-3 | 解析成功 = `OccurredAt.Equal(parsed) && !Equal(now)` 显式断言；截断 = 负控断言**可观测的 ID 分歧**（t.Fatalf 显式报错），fallback 永不可能静默 |

---

## 4. 范围边界（非目标）

- **不做生产代码变更**：不改 `factFromAudit`/`factFromGap`/gap 扫描的吞错行为（:258）、不改 `DeterministicFactID`。让 store 层解析错误"响亮"（向调用方传播错误）或加 gap 滞留指标属 **B3-4**（staleness observability）方向，不在此 pin 内。
- **不做端到端**：不驱动真实 EICAR → `ScanObjectByID` → relay 投递链（**B3-6** 方向范围）；本 pin 只钉 gap 路径的 ID 代数与去重。
- **不扩形状**：只钉 quarantine 形状（admin origin, `file.delete`）；file origin 的收敛已由既有 `…_File` 测试覆盖。
- **不改 schema / 不新增 go.mod 依赖 / 不动 openapi.json**（I2/I6/无 HTTP 面）。
- **负控形状固定为空格分隔 + 时区**（SQLite 默认形态）：`"2026-08-08T01:17:41Z"` 可被 RFC3339Nano 解析，不是负控。

---

## 5. 门禁自查

- [ ] 新增测试位于 `internal/auditgovernance/fact_id_test.go`（harness：`factIDStore` + `newRedactor` + `SourcePrefix`，全部既有）
- [ ] `go test ./internal/auditgovernance/ -run 'DeterministicFactID'` 绿
- [ ] `make check` 全绿（gofmt/build/vet/test；无新依赖）
- [ ] 未改动任何生产文件（`git status` 仅新增本文 + 测试文件内新增测试函数）
