# 设计：hard-delete 路径删除时刻保护强制 + lifecycle hard_delete 尊重 noncurrent 保留（reconcile 门控收口）

> **配套规格：** `docs/requirements/internal-reconcile-hard-delete-protection-gate-v1.spec.md`（G1/G2 · FR-1…FR-3 · AC-1…AC-3）· **模块：** `internal/reconcile`（deletion.go · lifecycle.go · retention.go · protection.go）+ `internal/repository`（只读引用，**零改动**）· **状态：** 设计已实现并 A/B 验证（红→绿证据见 §7，工作区未提交）· **基线：** HEAD `acfaaf4`
> **门禁：** `make check` 全绿（本次实测，见 §7.4）· 单文件 ≤ 500 行（deletion.go 158 / lifecycle.go 193 / retention.go 155 / deletion_test.go 232 / lifecycle_test.go 788）· I1/I2 不动（**零 SQL 变更**）· I6 纯 stdlib · 无新 `go.mod` 依赖 · **无 REST/S3/MCP 路由与 OpenAPI 变更**（纯内部行为修复）

---

## 1. 证据复核（规格全部主张独立复验，行号对照 HEAD `acfaaf4`）

| # | 规格引用 | 本次复核（`git show HEAD` + `cat -n`） | 结论 |
|---|---------|--------------------------------------|------|
| E1 | `deletion.go:26-42` 先删 blob 后调 `HardDeleteObject` | `hardDeleteKey` :13-43：版本兜底 :25-27 · `cleanObjectChunks`+`store.Delete` 循环 :28-36 · `repo.HardDeleteObject` :37-39 · usage :40-41 | ✅ 精确 |
| E2 | `lifecycle.go:79-92` 预检 :101-108 先于 `hardDeleteKey` :109 | `handleExpiredObject` :99-120：`objectKeyDeletionProtected` :101-108 → `hardDeleteKey` :109 · 软删 :115-119 | ✅ 精确 |
| E3 | `sweepNonCurrentVersions` :125-155 独立尊重 noncurrent 策略 | :126 `ListExpiredNonCurrentVersions` · :133-140 逐版本 `objectDeletionProtected` · :141 `hardDeleteVersion` | ✅ 精确；**补充：** SQL 层 :241 还有 `NOT EXISTS (legal_holds)` 过滤；`noncurrent_count` 仅写不读（见 §9 处置） |
| E4 | `protection.go:7-25` 三源判定 | `objectDeletionProtected` :10-16（LockedUntil :11-13 · `_aero_legal_hold` 元数据 :14-15 · `ObjectHasLegalHold` :16）；`objectKeyDeletionProtected` :20-28 | ✅ 精确 |
| E5 | `HardDeleteObject` :74-98 是唯一 legal-hold 强制点 | legal_holds EXISTS :82-87 · `ErrLegalHoldActive` :88-90 · key 级 DELETE :94-95；**只查 legal_holds，不查 `locked_until`** | ✅ 精确；WORM 无 DB 兜底确认 |
| E6 | retention 路径同构 | `purgeOneSoftDeleted` :136-150：预检 :137-144 → `hardDeleteKey` :145 | ✅ 精确 |
| E7 | `ListObjectVersions` 无 tombstone 过滤 | `sql_objects_versions.go:121-139`，WHERE 仅 tenant/bucket/key | ✅ 精确（G2 成立） |
| E8 | `ListExpired` 只返回 current | `sql_bucket_lifecycle.go:144-154`：`o.deleted_at IS NULL` + `expire_after_days > 0` | ✅ 精确（FR-3 的前提） |

**A/B 前置验证（未修复树）：** 4 个新验收测试在**仅含哨兵声明、无修复逻辑**的树上全部 FAIL，失败断言与规格 §4 预测逐字一致（见 §7.2）——红侧已实证。

---

## 2. 设计决策（D1–D5）

| # | 决策 | 理由（含对规格 FR 的落点） |
|---|------|---------------------------|
| **D1** | 包内私有哨兵 `errKeyProtected`，`hardDeleteKey`/`hardDeleteVersion` 在**任何破坏性动作之前**（`cleanObjectChunks` / `store.Delete`）对每个版本执行 `objectDeletionProtected` 三源复查；命中 → 立即返回哨兵，**零副作用** | FR-1.1/1.2/1.4 与 FR-1.3 的"跳过必须传播"：哨兵让调用方区分"受保护跳过"与"真失败"，杜绝"以 nil 返回 → 误计为已删"。三源含 `LockedUntil` ⇒ FR-2（WORM 兜底）不经 SQL 达成 |
| **D2** | `hardDeleteKey` 保留整体 key 语义（retention/orphan 路径），**入口全版本重查**复用已拉取的 `versions` 切片逐版本检查（不二次 `ListObjectVersions`，等价于 `objectKeyDeletionProtected` 的内联） | retention 清除的是整 key 已软删状态，语义不变；入口重查覆盖"预检之后落 hold/WORM"的完整窗口 |
| **D3** | lifecycle `handleExpiredObject` 硬删分支由 `hardDeleteKey` 改为 **`hardDeleteVersion`**（按过期 current 单版本清除） | FR-3：`ListExpired` 只返回 current（E8）；`hardDeleteVersion` 已具备 per-version 行+blob+usage 清除与 access-state 兜底（`HardDeleteObjectByID` 剩余版本为 0 时清理 ACL，`sql_objects_maint.go:129-137`）。非版本化桶上单行 ⇒ 语义与旧路径等价（§8 逐测验证）。函数签名零变化 |
| **D4** | 调用方既有预检（`objectKeyDeletionProtected` / `objectDeletionProtected`）**全部保留**为廉价过滤；错误分支统一加 `errKeyProtected` 识别日志 | FR-1.3 明确要求保留；避免无谓的版本列表查询；跳过不计数（lifecycle `hard` / retention `purged` / noncurrent `purged++` 均只在 nil 错误时递增——现状已正确，仅日志分级） |
| **D5** | **零 repository 变更**：不加 SQL、不迁移、不改接口；`ErrLegalHoldActive` 保持可达（复查与 DB 门之间的残余竞态兜底） | I1/I2 纪律；规格 §5 明示"DB 门保持现状"；WORM 由 reconcile 层复查闭环 |

---

## 3. 代码变更（本次已实现，工作区未提交）

### 3.1 `internal/reconcile/deletion.go`（89 → 158 行）

```go
// 新增包内哨兵：
var errKeyProtected = errors.New("reconcile: key protected by legal hold or WORM lock")
```

`hardDeleteKey` 在版本兜底之后、原有删除循环之前插入**入口重查**；原有循环内每个版本 `cleanObjectChunks` 之前插入**逐版本复查**：

```go
// 入口重查（FR-1.1）——任何破坏性动作之前，任一版本受保护 → 零副作用跳过：
for _, version := range versions {
    protected, err := objectDeletionProtected(ctx, repo, version)
    if err != nil { return err }
    if protected { return errKeyProtected }
}
// 逐版本复查（FR-1.2）——把残余 TOCTOU 窗口收窄到单次 store.Delete 调用宽度：
for _, version := range versions {
    protected, err := objectDeletionProtected(ctx, repo, version)
    if err != nil { return err }
    if protected { return errKeyProtected }
    cleanObjectChunks(ctx, cleaner, version, logger)
    ...
}
```

`hardDeleteVersion` 在 `cleanObjectChunks` 之前插入同构复查（FR-1.4）。**原有语义不变**：无保护时入口重查恒通过、逐版本复查恒通过，删除顺序、`HardDeleteObject`/`HardDeleteObjectByID` 调用、usage 调整逐字节不变。

### 3.2 `internal/reconcile/lifecycle.go`（181 → 193 行）

- `handleExpiredObject` 硬删分支：`hardDeleteKey` → **`hardDeleteVersion`**（FR-3，注释说明 tombstone 保留语义）；错误分支新增 `errKeyProtected` → `WARN "lifecycle hard delete skipped: protected"`，仍返回 false（不计数）。
- `sweepNonCurrentVersions`：错误分支在 `ErrLegalHoldActive` 之后新增 `errKeyProtected` → `WARN "lifecycle non-current version skipped: protected"`，`continue` 不 `purged++`。

### 3.3 `internal/reconcile/retention.go`（149 → 155 行）

- 新增 `errors` import；`purgeOneSoftDeleted` 错误分支新增 `errKeyProtected` → `WARN "retention hard delete skipped: protected"`，仍返回 false（不计数）。

**不改动：** `protection.go`（三源判定即复查实现）· `softDeleteKey`（软删不毁 blob）· `scrub.go` / `job.go` / `upload_gc.go` · repository 全部。

---

## 4. API 变更与兼容性约束

| 面 | 变更 | 兼容性 |
|----|------|--------|
| 对外 API（REST/S3/MCP/CLI/SDK/WebDAV） | **无** | 完全不变；无路由/OpenAPI 变更（无扩展入口动作） |
| repository 接口 / SQL / 迁移 | **无** | I1/I2 不动；`HardDeleteObject`/`HardDeleteObjectByID` 的 `ErrLegalHoldActive` 语义原样 |
| storage 接口 | **无** | 无 |
| `hardDeleteKey` / `hardDeleteVersion` / `handleExpiredObject` / `purgeOneSoftDeleted` / `sweepNonCurrentVersions` | 签名不变；新增包内哨兵 + 日志分支；lifecycle 硬删改走 per-version | 包内调用点仅 lifecycle.go / retention.go（grep 确认无其他调用方），全部同步更新 |
| 行为（受保护场景） | 收紧：预检之后落 hold/WORM → 整 key/单版本**零副作用**跳过（原先 blob 已毁） | 修复方向即行为收紧；无保护场景逐字节等价（§8） |
| 行为（versioned bucket lifecycle hard_delete） | **收紧：只清过期 current，tombstone 行+blob 保留**（原先连坐全删） | 规格 FR-3 契约；tombstone 仅经 `sweepNonCurrentVersions`（noncurrent_days 窗口）或显式版本删除消失 |
| usage 记账 | lifecycle 硬删从"扣全版本"变为"扣 current 单版本" | 与保留 tombstone 的事实一致（tombstone 行仍占用量，`deletionUsage` 显式计入 `VersionTombstone`）；retention 路径不变 |

**兼容性论证（关键）：** `handleExpiredObject` 切换 `hardDeleteVersion` 后，非版本化桶单行场景下：blob 删除（`store.Delete` 幂等容忍 `ErrNotFound`）、行删除（`HardDeleteObjectByID` 在剩余版本为 0 时同样清理 access state）、usage 调整（`deletionUsage([obj])` 对 `DeletedAt==nil` 的 current 计 `Size`+1，与旧路径一致）——三处均等价（§8 既有测试全绿实证）。

---

## 5. 失败模式

| 场景 | 行为 | 判定 |
|------|------|------|
| 入口/逐版本复查 DB 错误（`ObjectHasLegalHold` 失败） | 返回错误 → 调用方 WARN + 跳过不计数 | **fail-closed**：检查失败不删除，安全 |
| 复查命中 hold/WORM | `errKeyProtected` → 调用方 WARN "skipped: protected" + 跳过不计数；零副作用 | fail-closed，正确 |
| 逐版本复查之间落 hold（残余竞态） | 此前版本 blob 已删、行未删（不调 `HardDeleteObject`）；当前版本起中止 | 已收窄到单版本单次 `store.Delete` 宽度；规格 §5 明示接受，不引入跨系统事务 |
| 复查与 `HardDeleteObject`/`ByID` 之间落 hold | `ErrLegalHoldActive`（现状兜底，行残留、blob 已删） | 同上，残余窗口；日志分支已存在 |
| WORM 锁在预检后、入口复查前落 | 入口复查 `LockedUntil` 命中 → 整 key 零副作用跳过 | **修复点**：原先行+blob 全删（T-1b 实证 `<nil>` 返回） |
| `store.Delete` 失败 | 现状不变：中止、不调 DB 门、行残留 | 既有语义 |
| `cleanObjectChunks` 失败 | warn 后继续（既有契约，§2.1③） | 不变 |
| 计数 | 跳过/失败均不计入 `hard`/`purged`；仅 nil 错误递增 | 现状已正确，哨兵使其显式化 |

---

## 6. 迁移步骤

1. **无 schema 迁移、无配置变更、无数据重写**（零 SQL，I1/I2 不动）。
2. **部署顺序：** 普通代码发布（单二进制）。行为变化对运行中系统的影响仅发生在下次 reconcile/lifecycle/retention 定时扫描：受保护 key 从"blob 已毁"变为"原样保留"；versioned 桶的过期 current 不再连坐 tombstone。
3. **运维可见性：** 新 WARN 日志三类：`lifecycle hard delete skipped: protected` · `lifecycle non-current version skipped: protected` · `retention hard delete skipped: protected` —— 可 grep 定位受保护跳过事件（无 telemetry 变更，避免扩面）。
4. **文档：** `docs/CHANGELOG.md` 补一条 Fixed 记录（行为变化 + 受影响场景），见 §9.5。

---

## 7. 可测试验收映射（红→绿 A/B 已实证）

### 7.1 映射表

| 验收（规格 §4 原样） | 测试 | 位置 |
|---------------------|------|------|
| AC-1 主用例：预检后落 legal hold → blob 与行原样 | **T-1** `TestHardDeleteKey_LegalHoldAfterPrecheck_PreservesBlobAndRow`：预检（纯读）→ `PutLegalHold` → `hardDeleteKey`；断言 `errKeyProtected` · `store.Stat` 成功 · `ListObjectVersions`=1 · `ObjectHasLegalHold`=true · `GetObject` 正常 · **回归护栏** `handleExpiredObject(...)=="false"`（跳过不计数，非红→绿判据） | `deletion_test.go` |
| AC-1 WORM 子用例 | **T-1b** `TestHardDeleteKey_WORMLockAfterPrecheck_PreservesBlobAndRow`：`SetLockedUntil(now+24h)` 替代 hold；断言行+blob 原样 | `deletion_test.go` |
| AC-1 多版本子用例（"every version's blob"） | **T-1c** `TestHardDeleteKey_MultiVersion_HoldOnCurrent_PreservesAllBlobs`：`InsertObjectVersion` 造 v1(tombstone)+v2(current) 两行两 blob，对 v2 落 hold；断言两 blob 均 `Stat` 成功、2 行保留 | `deletion_test.go` |
| AC-2 versioned 桶 hard_delete 保留非当前版本 | **T-2** `TestLifecycleSweep_HardDelete_VersionedBucket_PreservesNonCurrentVersions`：versioning 开 + `expire_after_days=1,hard_delete` + `noncurrent_days=30`；v1 由 `InsertObjectVersion` 自动 tombstone（deleted_at=now，窗口内）、v2 backdate 72h；`sweep` 后断言 v1 行（tombstone、deleted_at 未动）+ blob 保留、v2 行与 blob 清除；**正控制**：v1 deleted_at 再 backdate 31 天 → 二次 sweep → v1 被 `sweepNonCurrentVersions` 清除 | `lifecycle_test.go` |
| AC-3 `go test ./internal/reconcile/` | 全包 20.85s 全绿（含全部既有测试） | — |

### 7.2 红侧证据（未修复树，仅加哨兵声明保证编译）

```
--- FAIL: TestHardDeleteKey_LegalHoldAfterPrecheck_PreservesBlobAndRow
    deletion_test.go:116: hardDeleteKey must skip with errKeyProtected, got object is under legal hold and cannot be deleted
--- FAIL: TestHardDeleteKey_WORMLockAfterPrecheck_PreservesBlobAndRow
    deletion_test.go:162: hardDeleteKey must skip with errKeyProtected, got <nil>
--- FAIL: TestHardDeleteKey_MultiVersion_HoldOnCurrent_PreservesAllBlobs
    deletion_test.go:215: hardDeleteKey must skip with errKeyProtected, got object is under legal hold and cannot be deleted
--- FAIL: TestLifecycleSweep_HardDelete_VersionedBucket_PreservesNonCurrentVersions
    lifecycle_test.go:761: expected exactly 1 remaining version, got 0
```

四条失败原因与规格 §4 预测逐字一致：T-1/T-1c 复现"blob 先毁、DB 门后拦"（`ErrLegalHoldActive`）；**T-1b 复现最严重路径**（`<nil>` 返回 —— WORM 竞态下连行带 blob 静默全删，无任何兜底）；T-2 复现连坐（0 行剩余）。

### 7.3 绿侧证据（修复后）

```
--- PASS: TestHardDeleteKey_LegalHoldAfterPrecheck_PreservesBlobAndRow (0.31s)
--- PASS: TestHardDeleteKey_WORMLockAfterPrecheck_PreservesBlobAndRow (0.32s)
--- PASS: TestHardDeleteKey_MultiVersion_HoldOnCurrent_PreservesAllBlobs (0.32s)
--- PASS: TestLifecycleSweep_HardDelete_VersionedBucket_PreservesNonCurrentVersions (0.37s)
ok  github.com/aero-vault/aero-vault/internal/reconcile  1.329s   (4 new tests, -count=1)
ok  github.com/aero-vault/aero-vault/internal/reconcile  20.850s  (full package)
ok  github.com/aero-vault/aero-vault/internal/reconcile  4.393s   (-race 复跑 5 个相关测试)
```

### 7.4 硬门禁

```
make check：gofmt 无输出 · go build ./... · go vet ./... · go test ./... 全 ok · 单文件 ≤500 行 PASS
```

---

## 8. 基线影响（既有测试逐项）

| 既有测试 | 影响 |
|---------|------|
| `TestHardDeleteKeyRemovesEveryVersionAndAdjustsUsage`（直接调 `hardDeleteKey`，versioned 桶全清） | 无保护 → 入口重查通过、行为逐字节不变；usage 归零断言保持 ✅ |
| `TestLifecycleSweep_HardDelete_ExpiredObject` / `_StorageMissing` | 非版本化单行；改走 `hardDeleteVersion` 后 blob/行清除语义等价（`store.Delete` 幂等容忍 `ErrNotFound`）✅ |
| `TestLifecycleSweep_ExpiredLocked_HardDeleteBlocked` / `_ExpiredExpiredLock_HardDeleteProceeds` | 预检拦截/过期锁放行，语义不变（复查只是第二道防线）✅ |
| `TestLifecycleSweep_LockedObject_NotHardDeleted`（软删路径） | 不涉及硬删 ✅ |
| `TestLifecycleHardDeleteLegalHoldPreservesBlobAndRow` / `TestRetentionLegalHoldPreservesBlobAndSoftDeletedRow` | hold 在 sweep 前 → 预检拦截，不变 ✅ |
| 其余 job/scrub/upload_gc/idempotency_gc 测试 | 未触碰路径 ✅ |

**计数语义：** 被保护跳过不计数由既有"仅 nil 错误递增"保证，哨兵使其显式可测（T-1 回归护栏锁定）。

---

## 9. 既有发现处置（门禁将复查，逐条给出证据）

| # | 来源 | 发现 | 处置 | 证据 |
|---|------|------|------|------|
| F1 | 规格 §1 E3 注记 | `noncurrent_count` 仅写不读 | **明确拒绝（超范围）**：维持规格 §5 决定；本方向只保证 hard_delete 不绕过 `noncurrent_days` 窗口。grep 复核：全仓仅 `sql_bucket_lifecycle.go:29,55,137` 写入 + `sql_buckets.go:115`/`admin.go:213`/`bucket_handlers.go:341` 透传读出，无任何策略过滤读取 | `grep -rn noncurrent_count internal/`（本次复核） |
| F2 | 规格 §4 注记 | "skip 计数断言是回归护栏，非红→绿判据" | **采纳**：T-1 的 `handleExpiredObject==false` 断言按回归护栏定位；红侧失败因 `errKeyProtected` 断言与 blob 保留断言（§7.2），非该护栏 | 红侧输出 §7.2 |
| F3 | 规格 §1 行号漂移注记 | 方向快照行号漂移 +20 | **已闭合**：本次逐条以 HEAD `acfaaf4` 复核（§1 表），E1–E8 全部精确或语义成立 | §1 表 |
| F4 | 规格 §1 E5 补充发现 | `HardDeleteObject`/`ByID` 不查 `locked_until`（WORM 无 DB 兜底） | **已在 reconcile 层修复**：FR-2 经 `protection.go` 三源复查（含 `LockedUntil`）在删除时刻强制；T-1b 红→绿实证。DB 门保持现状（规格 §5 明示） | §7.2/§7.3 |
| F5 | 兄弟方向 scrub 设计门 P1③ | `allowAllAuthz` 为未提交的跨 campaign 工作区改动，存在提交顺序风险 | **显式处置（隔离）**：本设计**零依赖** `allowAllAuthz`——生产改动仅触及 HEAD 干净文件（deletion.go/lifecycle.go/retention.go）；新增测试只用 HEAD 已提交的 harness（`openTestRepoWithDB`/`openTestStore`/`putTestBlob`/`insertRow`/`backdateByID`/`newSilentLogger`，均确认存在于 `git show HEAD`）。唯一共享文件 `deletion_test.go` 自带 staged 的 `allowAllAuthz` 定义（同文件自洽，独立提交亦可编译）；T-2 落在 HEAD 干净的 `lifecycle_test.go`。建议提交顺序：scrub WIP 先落或与整体快照同提交 | `git show HEAD:internal/reconcile/job_test.go`（harness 在 HEAD）· `git status`（deletion_test.go 为 staged M） |
| F6 | 设计门惯例 | 验收测试不得空转/空洞断言 | **满足**：4 测试均为确定性红→绿判别（§7.2 红侧失败断言非空洞）；无仅 `err==nil` 断言；无捕获日志需求（AC 不含 warn 契约，日志分支为运维可见性，非验收项） | §7.2/§7.3 |

---

## 10. 范围边界（与规格 §5 一致，重申）

不做：跨系统事务 · `noncurrent_count` 强制执行（F1）· 版本晋升（promotion）语义 · repository 接口/SQL/迁移变更 · 调用方预检结构改动 · delete marker 语义扩展 · telemetry/指标新增 · 文档重写既有测试。

---

## 11. 交付物清单

| 文件 | 类型 |
|------|------|
| `internal/reconcile/deletion.go`（89→158 行） | 生产：哨兵 + 入口/逐版本复查（FR-1.1/1.2/1.4） |
| `internal/reconcile/lifecycle.go`（181→193 行） | 生产：FR-3 per-version 清除 + 哨兵日志 |
| `internal/reconcile/retention.go`（149→155 行） | 生产：哨兵日志 |
| `internal/reconcile/deletion_test.go`（+158 行） | 测试：T-1 / T-1b / T-1c（AC-1） |
| `internal/reconcile/lifecycle_test.go`（+87 行） | 测试：T-2 + 正控制（AC-2） |
| `docs/requirements/internal-reconcile-hard-delete-protection-gate-v1.design.md` | 本文档 |
| `docs/CHANGELOG.md` | Fixed 记录（含行为变化说明，本次已追加） |
