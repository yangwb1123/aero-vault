# 方向：元数据 read-modify-write 原子化 —— SetObjectMetaKeys/SetObjectMetaKey/DeleteObjectMetaKey 丢失更新竞态（验收规格 · 已验证现状）

> **模块：** `internal/repository`（`sql_objects_maint.go` · `sqlite.go` · 调用方 `internal/service/file_features.go` · `internal/reconcile/scrub.go`）
> **来源分析：** `docs/auto/analyses/internal-repository-0c7531e4.json`（方向 1）· **日期：** 2026-08-06 · **HEAD：** `acfaaf4`
> **评分：** 价值 8 / 风险降低 8 / 工作量 3 / 置信度 9
> **状态声明：** 本文是**验收契约**而非绿地设计：逐条核验方向引证（§1）、登记三个方法的现状代码形态与调用面（§2）、原样保留三条验收检查并映射为可执行测试规格（§4）、登记修复方向的约束与必须保留的行为语义（§3）。超范围项一律不做（§5）。

---

## 1. 证据核验（方向引证逐条对照当前 HEAD）

| # | 方向引证 | 当前 HEAD 位置 | 核验结论 |
|---|---------|----------------|---------|
| E1 | `internal/repository/sql_objects_maint.go:268` — `SetObjectMetaKey` read-modify-write | `SetObjectMetaKey` :268-295：`SELECT metadata …`（:271）→ `unmarshalKV`（:275）→ Go 侧 merge（:278）→ `json.Marshal`（:279）→ 独立 `UPDATE objects SET metadata=…`（:283-291）→ **两条非事务语句，无锁** | ✅ **行号精确**，语义成立 |
| E2 | `internal/repository/sql_objects_maint.go:297` — `SetObjectMetaKeys` read-modify-write | `SetObjectMetaKeys` :297-329：`len(meta)==0 → return nil`（:299-301，空 patch 零 DB 访问）→ 同 E1 形态（SELECT :304 → merge :313-315 → UPDATE :317-325） | ✅ **行号精确**，语义成立 |
| E3 | `internal/repository/sql_objects_maint.go:358` — `DeleteObjectMetaKey` read-modify-write | `DeleteObjectMetaKey` :358-385：SELECT :361 → merge（delete）:369 → UPDATE :371-379；`len(current)==0 → return nil`（:367-368，对象须已存在，SELECT 在早退之前） | ✅ **行号精确**，语义成立 |
| E4 | `internal/service/file_features.go:305` — `PatchMetadata → SetObjectMetaKeys` 公共 REST 路径 | `PatchMetadata` :296-307（`PATCH /v1/files/…/metadata`）；:305 恰为 `return s.repo.SetObjectMetaKeys(ctx, tenant, bucket, key, meta)` | ✅ **行号精确**。同文件另有 `DeleteMetadataKey` :333 → `DeleteObjectMetaKey`（`DELETE …/metadata/{key}` 路径） |
| E5 | `internal/repository/sqlite.go:28` — `SetMaxOpenConns(1)` 只串行化语句、不串行化 read-then-write 窗口 | `sqlite.go:26`：`db.SetMaxOpenConns(1) // serialize writes to avoid SQLITE_BUSY` | ⚠️ **行号漂移**（28→26），语义成立：`database/sql` 池把**语句**排队到唯一连接上，但一个调用者的 SELECT 归还连接后、其 UPDATE 获得连接前，另一调用者可完整执行其 RMW——窗口未被消除 |

**方向问题陈述核验（当前状态）：**

| 陈述 | 核验 |
|------|------|
| "两个并发调用者（REST PATCH 与 scrub 标记 `_aero_scrub_status`）都读到 v0，第二个 UPDATE 静默覆盖第一个的键" | ✅ **成立**。两个真实并发来源均已实证：`PatchMetadata`（E4，用户侧）与 `internal/reconcile/scrub.go:95`（`SetObjectMetaKey(…, "_aero_scrub_status", "corrupt")`）及 :113（`DeleteObjectMetaKey(…, "_aero_scrub_status")`，修复后清除标记）——scrub 与用户 PATCH 并发时，`_aero_scrub_status` 与用户键相互覆盖/复活 |
| "SQLite 上也不串行化" | ✅ **成立**（E5）。单连接只保证语句原子，不保证"读-改-写"复合原子 |
| "Postgres 路径竞态窗口更宽" | ✅ **成立**：`openPostgres`（`postgres.go:11`）未设置任何池上限（`SetMaxOpenConns` 仅存在于 `sqlite.go:26`），多连接并行执行 ⇒ 丢失更新概率显著更高 |
| 修复选项：SQL 侧单语句 merge（`json_patch`/`jsonb_set`）或事务（Postgres `SELECT … FOR UPDATE` / SQLite `BEGIN IMMEDIATE`） | ✅ **可行**：`modernc.org/sqlite v1.50.1`（go.mod）捆绑 SQLite 3.50.x，`json_patch`/`json_remove`（3.45+/3.9+）可用；`BeginTx` + `FOR UPDATE SKIP LOCKED` 事务先例已存在于本包（`audit_governance_claim.go:41,54`、`billing_outbox.go:36`） |

**补充核验（方向未引、影响实现选择的现状）：**

| 位置 | 现状 | 结论 |
|------|------|------|
| `sql_objects_maint.go:331-356` `ReplaceObjectMetadata` | **单条** UPDATE，无 read-modify-write | ⚠️ **不在本方向范围内**（replace 语义下 last-write-wins 无丢失更新问题） |
| `repository_interface.go:41-44` | 三个方法在 `Repository` 接口上（另有 `ReplaceObjectMetadata` :43） | 修复不改接口签名 |
| `sql_objects_maint.go:283-291,317-325,371-379` | Postgres 分支 `$1::jsonb` 强转；SQLite 分支存 TEXT | SQL 侧 merge 须按方言分别实现 |
| `internal/repository` 现有测试 | **无任何 `TestSetObjectMetaKeys*` / `TestConcurrent*` 前缀测试**（仅 `TestReplaceObjectMetadataMissingReturnsNotFound`，`correctness_regression_test.go:46`）；无 `sync.WaitGroup` 并发测试先例 | ⇒ 验收过滤器的三条 `-run` 当前**真空通过**，新测试必须按 §4 命名使过滤器非空（同 CLI 规格先例） |
| 测试基建 | `openTestRepo`（`buckets_keys_test.go:282`，Open+Migrate+Cleanup）· `openQuotaTestRepo`（`quota_test.go:11`）· `UpsertObject`（`sql_objects.go:20`，写 `Metadata map[string]string`）· `GetObject`（`sql_objects.go:164`，返回 `Object.Metadata`）· `Object.Metadata` 类型 `map[string]string`（`repository.go:25`） | 三条验收均可仅用现有基建实现 |
| `unmarshalKV` 错误被忽略（`:275,:310,:364` 均为 `_, _ :=`） | 损坏 JSON 元数据当前被静默覆盖为合法 JSON | 行为差异登记 §3，不锁死任何一侧 |

---

## 2. 问题陈述（丢失更新竞态）

三个元数据变更方法（`SetObjectMetaKey` :268 · `SetObjectMetaKeys` :297 · `DeleteObjectMetaKey` :358）全部实现为**两条独立、非事务的语句**：

```
SELECT metadata FROM objects WHERE tenant_id=$1 AND bucket=$2 AND key=$3 AND deleted_at IS NULL   -- ① 读 v0
（Go 侧 unmarshal → merge/delete → json.Marshal）                                                 -- ② 无锁窗口
UPDATE objects SET metadata=$N WHERE …                                                            -- ③ 整块写回
```

**丢失更新路径：** 调用者 A 完成 ①（读到 v0），调用者 B 完成 ①-③（写回 v1 = v0+{b}），A 随后执行 ③（写回 v0+{a}）——B 的键被**静默覆盖**，无任何错误返回。反向亦可：B 的 DeleteObjectMetaKey 删除键 k 后，A 的陈旧 v0（含 k）整块写回复活 k。

**实际并发来源（均已实证）：**
- **用户侧**：REST `PATCH /v1/files/{key}/metadata` → `FileService.PatchMetadata`（`file_features.go:305`）→ `SetObjectMetaKeys`；`DELETE /v1/files/{key}/metadata/{k}` → `DeleteMetadataKey`（:333）→ `DeleteObjectMetaKey`。
- **系统侧**：reconcile scrub 标记损坏对象 `_aero_scrub_status=corrupt`（`scrub.go:95`）与修复后清除（`scrub.go:113`）——与用户 PATCH 并发即触发"用户键丢失"或"corrupt 标记被覆盖（损坏对象解锁）"。

**串行化现状：** SQLite `SetMaxOpenConns(1)`（`sqlite.go:26`）只把语句排队到唯一连接，② 窗口不设防；Postgres 无池限制，窗口更宽。竞态在两个方言下均真实存在，SQLite 只是更难触发（非不可触发）。

---

## 3. 需求规格（FR，范围严格限定于方向）

### FR-1：三个方法的 read-modify-write 必须原子

`SetObjectMetaKey` · `SetObjectMetaKeys` · `DeleteObjectMetaKey` 的"读-改-写"必须在**同一把锁/同一次原子操作**下完成，二选一（方向给出的两种选项，实现择一）：

- **A. SQL 侧单语句 merge：** SQLite `json_patch(metadata, ?)`（merge）/ `json_remove(metadata, ?)`（delete）；Postgres `metadata || ?::jsonb`（merge）/ `metadata - ?`（delete）。合并与更新在 DB 内原子完成，无 Go 侧窗口。
- **B. 事务：** `BEGIN IMMEDIATE`（SQLite，写锁立即获取）/ `BeginTx` + `SELECT … FOR UPDATE`（Postgres），SELECT-merge-UPDATE 同事务同连接。

无论选择哪种，**合并语义必须与现状 Go 侧 merge 完全一致**：patch 键覆盖旧值、未在 patch 中的键保留、delete 只移除目标键。**key 可能含任意字符**（空格、引号等），SQL 侧路径构造必须正确转义（JSON path 引用），不得引入新限制。

### FR-2：错误语义零回归（现状契约逐条保留）

| 现状行为 | 位置 | 必须保留 |
|---------|------|---------|
| 对象不存在 → `ErrNotFound` | 三方法 SELECT 的 `sql.ErrNoRows` 分支 | 保留。**不得**改用 `RowsAffected==0` 判定：Postgres 下"写回相同值"的 UPDATE 计 0 行被改（SQLite 计 1 行），方言不一致会引入误报 `ErrNotFound`。A 方案用 `RETURNING id` 判定存在性，B 方案用 `SELECT … FOR UPDATE` 的 ErrNoRows |
| `SetObjectMetaKeys` 空 patch（`len(meta)==0`）→ `nil`，**零 DB 访问**（对象不存在也返回 nil） | :299-301 | 保留早退，不得引入存在性检查 |
| `DeleteObjectMetaKey` 元数据为空 → `nil`（对象已存在） | :367-368 | 保留 |
| 设置键为相同值 → `nil`（非错误） | 现状 UPDATE 恒成功 | 保留（见上，`RowsAffected` 陷阱） |
| 写入侧无返回值语义：三方法均只返回 error | 接口 :41-44 | 接口签名不变 |

### FR-3：事务/语句形态符合仓库既有约定

- SQL 占位符遵守 **I1**（`s.rebind` 按文本序改写 `$N`，同值不得复用占位符）；SQL 侧 merge 的 patch 参数用独立占位符。
- Postgres 分支保持 `::jsonb` 强转语义；SQLite 分支保持 TEXT 存储。
- **不新增迁移文件**（I2：schema 不变，仅 DML 形态变化）；**不新增 go.mod 依赖**（I6：SQLite JSON 函数内置于捆绑的 3.50.x）。
- `sql_objects_maint.go` 当前 385 行，修复后仍须满足**单文件 ≤ 500 行**硬门禁（超出则拆独立文件，不违反本方向范围）。

### FR-4：修复不得改变其余路径

`ReplaceObjectMetadata`（:331，单语句 replace）、`GetObject`/`Stat` 读路径、tags/ACL/事件发布不受影响；`_aero_scrub_status` 的读侧守卫（`scrub.go:110` 检查 `obj.Metadata`）无需改动——修复只保证标记写入不再被并发覆盖。

---

## 4. 验收标准（方向原文三条，原样保留并测试化）

> 全部落在 `internal/repository/*_test.go`（包 `repository_test`），复用 `openTestRepo`（`buckets_keys_test.go:282`）。种子对象一律 `UpsertObject(ctx, repository.Object{TenantID:"default", Bucket:"default", Key:…, Backend:"local", StorageKey:…, ETag:…, Metadata:…})`，读回一律 `GetObject` 后检查 `obj.Metadata`。当前仓库**零匹配**这三条 `-run` 过滤器（§1 补充核验）⇒ 新测试命名必须按下表前缀，使过滤器非空。

### AC-1 `go test ./internal/repository -run TestConcurrentMetadataMerge -race passes`

> 方向原文：*N goroutines each SetObjectMetaKeys a distinct key on the same object; afterwards all N keys are present in GetObject metadata*

**测试化规格：** 新增 `TestConcurrentMetadataMerge`（命名即过滤器匹配项）：
- 种子：`UpsertObject`，`Metadata: {"seed":"0"}`，`tenant/bucket/key` 取 `"default"/"default"/"race-merge.txt"`。
- 并发：`N=16` 个 goroutine，`sync.WaitGroup` + 关闭 channel 作**启动屏障**（保证同时起跑，最大化 SELECT-UPDATE 窗口交叠）；每个 goroutine 用**唯一键名** `k{i}` 循环调用 `SetObjectMetaKeys` **25 次**（多轮放大丢失更新命中概率——单轮在 SQLite 单连接下窗口极窄，断言不变）。
- 断言：全部 goroutine join 后，`GetObject` 返回 `nil` error；`obj.Metadata` 含 `seed` 及**全部 16 个 `k{i}`**（`len==17`）。修复后（任一 FR-1 方案）该断言**确定性通过**；修复前只要发生一次丢失更新即失败（键缺失）。
- 运行：`go test ./internal/repository -run TestConcurrentMetadataMerge -race`（`-race` 按方向原文强制；`make test-race` 亦覆盖）。
- 补充约束：goroutine 内错误经 channel 收集，join 后统一 `t.Fatalf`；测试不得 `t.Parallel()`（与同包其他 SQLite 测试争用无意义）。

### AC-2 `go test ./internal/repository -run TestConcurrentMetaDelete -race passes`

> 方向原文：*concurrent SetObjectMetaKeys and DeleteObjectMetaKey on the same object never resurrects a deleted key*

**测试化规格：** 新增 `TestConcurrentMetaDelete`：
- 种子：`Metadata: {"victim":"x", "seed":"0"}`。
- 并发：16 个 setter goroutine（各写唯一键 `k{i}`，循环 25 次 `SetObjectMetaKeys`）+ 16 个 deleter goroutine（循环 25 次 `DeleteObjectMetaKey(…, "victim")`），同一启动屏障。
- 断言：join 后 `GetObject`：`victim` **必须不存在**（"never resurrects"）；全部 `k{i}` 存在（setter 无丢失）。修复前，setter 陈旧读（v0 含 victim）整块写回即复活 victim → 断言失败。
- 运行：同 AC-1（`-race` 强制）。
- 注：deleter 循环中后续调用为幂等 no-op（`len(current)==0 → nil` 或重复 delete），不引入错误路径。

### AC-3 `go test ./internal/repository -run TestSetObjectMetaKeys keeps existing behavior`

> 方向原文：*merge preserves keys not in the patch map*

**测试化规格：** 新增 `TestSetObjectMetaKeysPreservesUnpatchedKeys`（**必须以 `TestSetObjectMetaKeys` 为前缀命名**——当前仓库无任何匹配测试，过滤器真空）：
- 种子：`Metadata: {"a":"1", "b":"2", "weird key":"keep"}`（含空格键名，锁 FR-1 的任意字符路径转义）。
- 执行：`SetObjectMetaKeys(…, {"b":"3", "c":"4"})`。
- 断言：`GetObject` 后 `Metadata` 精确等于 `{"a":"1", "b":"3", "c":"4", "weird key":"keep"}`——未 patch 的键（`a`、`weird key`）保留，patch 键覆盖（`b`），新键加入（`c`）；`SetObjectMetaKey` 单键变体与 `DeleteObjectMetaKey` 的既有语义由同一断言族覆盖（同测试内追加：`SetObjectMetaKey("a","9")` 后 `a=="9"`；`DeleteObjectMetaKey("c")` 后 `c` 消失）。
- 运行：`go test ./internal/repository -run 'TestSetObjectMetaKeys'`（普通运行即可，无需 `-race`）。

**回归矩阵（现有测试不得破坏）：** `TestReplaceObjectMetadataMissingReturnsNotFound`（`correctness_regression_test.go:46`）、`internal/reconcile/scrub_test.go:152`（`TestScrub_ClearFlagFailureKeepsFlag`，scrub 标记/清除路径）、`internal/api/rest/admin_files_delete_test.go:420`、`internal/service/usage_consistency_test.go:210`（均经 `SetObjectMetaKey` 设 `_aero_scrub_status`）——`make check` 全量通过为合入门禁。

---

## 5. 范围边界（明确不做）

| 不做 | 理由 |
|------|------|
| `ReplaceObjectMetadata` 改造 | 单语句 replace，无 read-modify-write，无丢失更新问题（§1 补充核验） |
| 事件发布/审计/索引路径改造 | 本方向只涉元数据 DML 原子性；事件发布在 FileService 层，不在此三方法内 |
| `unmarshalKV` 错误处理变更（损坏 JSON 元数据行为） | 现状为静默覆盖；SQL 侧 merge 对非法 JSON 会返回错误——**行为差异登记**（§1 补充核验、§3），但不作为本方向验收项，不为此新增测试 |
| 新增迁移 / schema 变更 / 新依赖 | I2/I6：schema 不变、SQLite JSON 函数内置 |
| `Repository` 接口签名变更 / 新公开 API | 修复完全封装在 `sqlStore` 内部 |
| Postgres 集成测试（`//go:build integration`） | 方向验收只要求 SQLite 基线（CI 唯一被 `go test` 验证的路径）；Postgres 正确性由 FR-1 的方案要求（`RETURNING`/`FOR UPDATE`、`::jsonb`）在实现时按既有集成基建自证，不扩入本规格 |
| 其他方向的 EnqueueJob 去重 / 租户删除清理 | 同源分析文件中的方向 2/3，独立立项 |
