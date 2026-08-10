# 方向：hard-delete 路径在 legal-hold/WORM 门之前销毁 blob；versioned bucket 上 lifecycle hard_delete 连坐清除全部版本（验收规格 · 已验证现状）

> **模块：** `internal/reconcile`（deletion.go · lifecycle.go · protection.go · retention.go）+ `internal/repository`（sql_objects_maint.go · sql_bucket_lifecycle.go · sql_objects_versions.go）
> **来源分析：** `docs/auto/analyses/internal-reconcile-7a29db11.json`（方向）· **日期：** 2026-08-07 · **HEAD：** `acfaaf4`
> **评分：** 价值 8 / 风险降低 8 / 工作量 3 / 置信度 8
> **状态声明：** 本文是**验收契约**而非绿地设计：逐条核验方向引证（§1）、登记真实缺口（§2）、给出严格限定范围的 FR（§3）、原样保留两条验收检查并映射到可执行测试（§4）。超范围项一律不做（§5）。

---

## 1. 证据核验（方向引证逐条对照当前 HEAD）

| # | 方向引证 | 当前 HEAD 位置 | 核验结论 |
|---|---------|---------------|---------|
| E1 | `internal/reconcile/deletion.go:26-42` — `hardDeleteKey` 先删 storage blob 再调 `repo.HardDeleteObject` | `hardDeleteKey` :13-43：版本兜底 :25-27 · `cleanObjectChunks`+`store.Delete` 循环 :28-36 · **`repo.HardDeleteObject` :37-39（最后才调用）** · usage 调整 :40-41 | ✅ **行号精确**。blob/Chunk 销毁**全部先于** DB 门 |
| E2 | `internal/reconcile/lifecycle.go:79-92` — 保护检查仅一次、早于 `hardDeleteKey` | `handleExpiredObject` :99-120：`objectKeyDeletionProtected` 预检 :101-108，随后 `hardDeleteKey` :109；软删分支 :115-119 | ✅ 语义成立，**行号漂移 +20**（方向快照后无功能变更，仅行距） |
| E3 | `internal/reconcile/lifecycle.go:103-126` — `sweepNonCurrentVersions` 独立尊重 noncurrent 策略 | `sweepNonCurrentVersions` :125-155：`ListExpiredNonCurrentVersions` :126 · 逐版本 `objectDeletionProtected` :133-140 · `hardDeleteVersion` :141 | ✅ 语义成立，**行号漂移 +22**。补充：`noncurrent_days` 窗口在 SQL 层过滤（`sql_bucket_lifecycle.go:228-283`）；**`noncurrent_count` 仅写入 buckets 表（:29/:55/:137），全仓无任何读取** —— 该策略当前本就未执行（独立于本方向，§5） |
| E4 | `internal/reconcile/protection.go:7-25` — 保护判定（LockedUntil / `_aero_legal_hold` / `ObjectHasLegalHold`） | `objectDeletionProtected` :10-16（LockedUntil :11-13 · 元数据 :14-15 · `repo.ObjectHasLegalHold` :16）；`objectKeyDeletionProtected` :20-28（逐版本调用） | ✅ 行号基本精确（7→10 起始） |
| E5 | `internal/repository/sql_objects_maint.go:74-100` — `HardDeleteObject` 是 key 级路径**唯一** legal-hold 强制点，返回 `ErrLegalHoldActive` | `HardDeleteObject` :74-98：`legal_holds` EXISTS 子查询 :82-87 · `ErrLegalHoldActive` :88-90 · key 级 `DELETE FROM objects` :94-95 | ✅ **行号精确**。注意：**只查 `legal_holds`，不查 `locked_until`（WORM）** —— WORM 的唯一强制点在 `protection.go` 预检 |

**方向问题陈述核验（当前状态）：**

| 陈述 | 核验 |
|------|------|
| "hardDeleteKey first deletes storage blobs for every version (store.Delete loop), then calls repo.HardDeleteObject, which is the only place legal_holds is enforced and which returns ErrLegalHoldActive on a held key" | ✅ **完全成立**（E1/E5）。三条调用路径同构：lifecycle 硬删 `lifecycle.go:109`、retention 清除 `retention.go:145`（`purgeOneSoftDeleted` :136-149，预检 :138-143 → `hardDeleteKey`）、非当前版本 `lifecycle.go:141`（`hardDeleteVersion` `deletion.go:60-77`：`store.Delete` :65-68 → `HardDeleteObjectByID` :70-72，后者同样只查 legal_holds `sql_objects_maint.go:107-128`） |
| "Protection is checked only once, earlier in the sweep — so a hold or WORM lock added between the pre-check and the blob deletes leaves held rows with destroyed blobs" | ✅ **成立（核心缺口 G1）**。保护**只**在 sweep 入口预检一次；`hardDeleteKey`/`hardDeleteVersion` 内部零复查。后果分两种：legal hold 竞态 → blob 已毁、`ErrLegalHoldActive` 使行残留（永久丢失受保数据）；**WORM 竞态更糟** —— `HardDeleteObject`/`HardDeleteObjectByID` 根本不查 `locked_until`，行与 blob 一起消失，无任何兜底 |
| "lifecycle expire_action=hard_delete on a versioning-enabled bucket calls hardDeleteKey, whose ListObjectVersions loop deletes ALL versions at once, destroying non-current versions that noncurrent_days policy would retain" | ✅ **成立（核心缺口 G2）**。`ListObjectVersions`（`sql_objects_versions.go:121-137`）**无 `version_tombstone` 过滤**；`hardDeleteKey` 循环不跳过 tombstone（仅跳过 delete marker `deletion.go:30-31`）；`HardDeleteObject` 按 key 整体 `DELETE`（E5）⇒ 非当前版本的行与 blob 一并销毁，绕过 `sweepNonCurrentVersions` 的 `noncurrent_days` 窗口 |

**既有测试覆盖（现状边界）：** `protection_test.go:11` `TestLifecycleHardDeleteLegalHoldPreservesBlobAndRow` 与 `:43` `TestRetentionLegalHoldPreservesBlobAndSoftDeletedRow` 只覆盖 **hold 在 sweep 之前已存在**（预检即拦）——TOCTOU 窗口（预检之后才落 hold）**无任何测试**；`sweepNonCurrentVersions` 无直接测试；versioned bucket 的 lifecycle hard_delete 无测试。

---

## 2. 真实缺口

| # | 缺口 | 位置 |
|---|------|------|
| **G1** | **TOCTOU：保护只预检一次，blob 先于门销毁。** legal hold 落在预检与 blob 删除之间 → `ErrLegalHoldActive` 留下"行在、blob 无"的受保对象；WORM（`locked_until`）落在预检之后 → 连行带 blob 全删（DB 门不查 WORM）。波及全部三条硬删路径（lifecycle / retention / non-current） | `deletion.go:28-39`、`deletion.go:65-72`、`sql_objects_maint.go:82-90,121-123`、`protection.go:10-28` |
| **G2** | **versioned bucket 的 lifecycle hard_delete 连坐清除全部版本。** `ListExpired` 返回过期 current（`deleted_at IS NULL`），`hardDeleteKey` 却遍历所有版本删 blob、按 key 删行，绕过 `sweepNonCurrentVersions` 的 `noncurrent_days` 保留窗口 | `lifecycle.go:99-120`、`deletion.go:21-39`、`sql_objects_versions.go:121-137`、`sql_objects_maint.go:94-95` |

---

## 3. 需求规格（FR，范围严格限定于方向）

### FR-1：删除时刻保护强制（G1 收口）— 修改 `hardDeleteKey` 与 `hardDeleteVersion`

`hardDeleteKey`（`deletion.go:13-43`）改为**先门后毁**：

1. **入口全版本重查**：在任何破坏性动作（`cleanObjectChunks` / `store.Delete`）之前，对 `ListObjectVersions` 返回的全部版本执行一次 `objectKeyDeletionProtected`（复用 `protection.go:20-28`，即 LockedUntil + `_aero_legal_hold` 元数据 + `ObjectHasLegalHold` 三源合一）。任一版本受保护 → 整 key 跳过：**零副作用**（全部行、blob、chunk 原样），不触碰 `repo.HardDeleteObject`。
2. **逐版本复查（纵深防御）**：每个版本的 `store.Delete` 之前对该版本再执行一次 `objectDeletionProtected`（`protection.go:10-16`）；发现受保护 → 立即中止，不再删除任何 blob，不调 `HardDeleteObject`。
3. **跳过必须传播，不得误计数**：被保护跳过的 key 不能算作已删除 —— `handleExpiredObject`（lifecycle.go:99-120）必须返回 false（不增 `hard` 计数）、`purgeOneSoftDeleted`（retention.go:136-149）必须返回 false、`sweepNonCurrentVersions` 不得 `purged++`。实现可采用哨兵错误（如 `errKeyProtected`，包内私有）让调用方区分"跳过"与"真失败"；也可保留调用方预检并让 `hardDeleteKey` 以哨兵返回。调用方既有预检**保留**（廉价过滤，避免无谓的版本列表查询），不要求删除。
4. `hardDeleteVersion`（`deletion.go:60-77`）同样：`store.Delete` 之前对该版本执行 `objectDeletionProtected` 复查；受保护 → 跳过，行与 blob 原样。

**行为契约（不变量）：** *任何版本的 blob/chunk 都不可能在"该版本受保护"的状态下被销毁；`repo.HardDeleteObject`/`HardDeleteObjectByID` 的 `ErrLegalHoldActive` 永远不是第一道防线*。残余竞态（复查与删除之间落 hold）收窄到单版本单次 `store.Delete` 调用宽度，且 WORM 由入口复查覆盖 —— 不引入跨系统事务（§5）。

### FR-2：WORM 兜底（G1 的 locked_until 维度）

FR-1 的复查统一走 `protection.go` 三源判定（含 `LockedUntil`），**不依赖** `HardDeleteObject` 的 legal_holds-only 门。效果：WORM 锁落在预检之后时，入口复查仍能拦下整 key，行与 blob 原样 —— 修复当前"WORM 竞态下无任何兜底"的结构性缺失。**不改 repository SQL**（I1/I2 不动）。

### FR-3：versioned bucket 的 hard_delete 尊重 noncurrent 保留（G2 收口）

lifecycle `expire_action=hard_delete` 作用于版本化 bucket 时（`ListExpired` 命中的 current 版本，`lifecycle.go:80-97` 路径），只清除**过期的 current 版本**（及其 delete marker），**不得**触碰 `version_tombstone=1` 的非当前版本行与 blob；非当前版本的永久清除仍专属 `sweepNonCurrentVersions`（`noncurrent_days` 窗口，`sql_bucket_lifecycle.go:228-283`）。实现自由（如 `hardDeleteKey` 增加"仅 current"模式、或 lifecycle 硬删路径改用 per-version 删除），但行为契约是：*tombstone 行及其 blob 只能因 `noncurrent_days` 窗口到期（经 `sweepNonCurrentVersions`）或显式版本删除而消失，绝不因 current 过期被连坐*。

---

## 4. 验收标准（方向原文两条，原样保留并测试化）

### AC-1 预检之后落 hold → 全部版本 blob 与行原样

> 方向原文：*New test: placing a legal hold after the protection pre-check (but before hardDeleteKey) still leaves every version's blob present in storage (store.Stat succeeds) and the DB row intact*

| 断言 | 测试（新增 `internal/reconcile/deletion_test.go`） | 当前 HEAD 行为 |
|------|------|------|
| **T-1 主用例**（单版本，legal hold 表）：装配 `openTestRepoWithDB`+`openTestStore`；`putTestBlob` + `insertRow`（storageKey 如 `default/default/touhou.txt`）；断言预检通过 `objectKeyDeletionProtected(ctx, repo, obj) == false`；随后 `repo.PutLegalHold{ObjectID, TenantID, VersionID}`（**模拟 hold 落在预检之后**）；调用 `hardDeleteKey(ctx, repo, store, nil, obj, newSilentLogger())`；断言：`store.Stat(storageKey)` **成功** · `ListObjectVersions` 仍 1 行 · `ObjectHasLegalHold(obj.ID)` 为 true · `GetObject` 正常 | ❌ blob 已销毁（`Stat` ErrNotFound），行残留 —— **复现缺陷** |
| **T-1b 子用例**（WORM，方向明示）：同装配，以 `repo.SetLockedUntil(ctx, tenant, bucket, key, time.Now().Add(24*time.Hour))` 替代 PutLegalHold；断言同上（行 + blob 原样） | ❌ blob 与行**全部消失**（DB 门不查 `locked_until`）—— 最严重路径 |
| **T-1c 子用例**（多版本，"every version's blob"）：`InsertObjectVersion` 造 v1（tombstone）+ v2（current）两行、各自 blob；预检通过后对 v2 落 hold；`hardDeleteKey` 后断言 **v1 与 v2 的 blob 均** `store.Stat` 成功、`ListObjectVersions` 仍 2 行 | ❌ 两版本 blob 全毁 |
| 调用方计数（回归护栏）：T-1 主用例另断言 `handleExpiredObject(ctx, obj, "hard_delete") == false`（跳过不计入 `hard` 计数）。注意当前 HEAD 走 `ErrLegalHoldActive` 错误路径也返回 false —— 该断言**不是**红→绿判据，而是锁定 FR-1.3 的实现选择：跳过必须以哨兵/错误传播、不得以 `nil` 返回误计为已删 | ⚠️ 当前也返回 false（但 blob 已毁）；修复后须在**零副作用**下返回 false |

**测试要点**（可复现性）：预检与 `hardDeleteKey` 之间无代码钩子 —— 测试**直接顺序调用**两者即确定性地模拟竞态窗口（预检是纯读、无副作用），无需注入点；这是红→绿验收测试，当前 HEAD 上 T-1/T-1b/T-1c 必须失败（复现缺陷），FR-1/FR-2 落地后转绿。

### AC-2 versioned bucket hard_delete 保留非当前版本

> 方向原文：*New test: expire_action=hard_delete on a versioned bucket with noncurrent_days set preserves non-current version rows and their blobs (only the expired current version is purged)*

| 断言 | 测试（新增 `internal/reconcile/lifecycle_test.go`） | 当前 HEAD 行为 |
|------|------|------|
| **T-2 主用例**：装配 `openTestRepoWithDB`+`openTestStore`；`CreateBucket`；`SetBucketLifecycle(tenant, bucket, 1, "hard_delete")`；`SetBucketNoncurrentVersionLifecycle(tenant, bucket, 30, 3)`（30 天窗口）。v1 = `insertRow`（storageKey k1，`putBlob` k1）→ v2 = `repo.InsertObjectVersion`（同 key，storageKey k2，`putBlob` k2；v1 变为 `version_tombstone=1` 且 `deleted_at` 为**现在**，窗口内）；`h.backdateByID(v2.ID, 72h)`（超过 expire 1 天）。`NewLifecycle(h.repo, store, time.Minute, newSilentLogger()).sweep(ctx)` | ❌ v1 行与 blob 连坐销毁 |
| 断言段 A（行）：`ListObjectVersions` 仅剩 1 行（v1，`VersionTombstone==true`，`DeletedAt` 未被改动）—— 只有过期 current 被清 | ❌ 0 行（key 级 DELETE） |
| 断言段 B（blob）：`store.Stat(k1)` **成功**（非当前 blob 保留）；`store.Stat(k2)` → `storage.ErrNotFound`（过期 current 的 blob 已清） | ❌ 两 blob 均消失 |
| 正控制（窗口语义未被绕过）：v1 的 `deleted_at` 若被 backdate 超过 30 天（窗口到期），`sweep` 后 v1 应被 `sweepNonCurrentVersions` 正常清除（该子用例验证测试装配本身有效、保留行为只归因于修复） | ✅ 当前即如此（`ListExpiredNonCurrentVersions` 命中） |

**测试要点**：`ListExpired`（`sql_bucket_lifecycle.go:144`）只返回 `deleted_at IS NULL` 的行，v2 是唯一被 expire 命中的版本 —— 该测试精确区分"过期 current 清除"与"非当前保留"两条路径。

### AC-3 门禁

> 方向原文：*go test ./internal/reconcile/ passes*

`go test ./internal/reconcile/` 全绿（含既有测试，见 §6）；继续满足 `make check` 硬门禁（gofmt / build / vet / 全量 test）。T-1 族与 T-2 在修复落地前为红（复现），修复后全绿 —— 验收以最终绿为准。

---

## 5. 范围边界（明确不做）

| 不做 | 理由 |
|------|------|
| 跨系统事务化"存储删除 + DB 门" | SQLite/local 与外部后端无法与 DB 组成单事务；残余竞态（复查与单次 `store.Delete` 之间）由逐版本复查收窄到微秒级，不引入分布式锁/两阶段删除 |
| `noncurrent_count` 强制执行 | 该字段当前仅存储从未读取（§1 E3），属独立缺陷；本规格只保证 hard_delete 不绕过 `noncurrent_days` 窗口 |
| 被清 current 后的版本晋升（promotion）语义 | AC-2 只要求"非当前版本行与 blob 保留"；purge 后是否将最新 tombstone 提升为 current 属 S3 语义扩展，方向未要求 |
| 修改 repository 接口/SQL（`HardDeleteObject` 增查 WORM、迁移等） | FR-2 经 `protection.go` 复查实现，DB 门保持现状（I1/I2 不动）；若未来要让 DB 门也查 WORM，属另一方向 |
| 修改调用方预检结构、`sweepNonCurrentVersions` 本体、`scrub.go`/`job.go`/`upload_gc.go` 路径 | 预检保留为廉价过滤；其余路径无此缺陷 |
| delete marker 语义、`DeletionUsage`/配额调整逻辑 | 与缺陷无关 |

---

## 6. 基线影响

**FR-1/FR-3 为行为收紧**（加复查、加版本过滤），不改变无保护场景的既有语义，不改变函数签名（哨兵错误为包内新增）。逐项核对既有测试：

| 既有测试 | 影响 |
|---------|------|
| `deletion_test.go:17` `TestHardDeleteKeyRemovesEveryVersionAndAdjustsUsage` | 无 hold → 入口复查通过、全删、usage 调整不变 ✅ |
| `lifecycle_test.go:151` `TestLifecycleSweep_HardDelete_ExpiredObject`、`:440` `TestLifecycleSweep_HardDelete_StorageMissing`（blob 缺失时 `store.Delete` 容忍 `ErrNotFound` 后仍删行） | 非版本化、无保护 → 不变 ✅ |
| `protection_test.go:11/:43`（hold 在 sweep 前 → 预检拦截） | 预检保留，行为不变（复查只是第二道防线）✅ |
| `lifecycle_test.go:617` `TestLifecycleSweep_ExpiredLocked_HardDeleteBlocked`、`:661`（LockedUntil 在 sweep 前） | 预检已拦，不变 ✅ |
| `lifecycle_test.go:228` `TestLifecycleSweep_LockedObject_NotHardDeleted`（软删路径） | 不涉及 `hardDeleteKey` ✅ |
| 新 T-1/T-1b/T-1c/T-2 | 红→绿（AC-1/AC-2） |

**计数语义**：被保护跳过的 key 不得计入 `hard`/`purged` 计数（FR-1.3）。当前竞态下 `handleExpiredObject` 走 `ErrLegalHoldActive` 错误路径返回 false（计数已正确），但 blob 已毁；修复不得退化为"以 nil 返回跳过 → 误计为已删"——须以哨兵错误传播跳过。`sweep` 日志（lifecycle.go:70-78）语义不变。文件行数：`deletion.go` 89 行 → 加复查逻辑后预计 <150 行，满足单文件 ≤500 行硬门禁。
