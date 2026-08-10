# 审计：确定性 fact-ID 设计的集成时序（B3-1 共存 / 在途行不重写 / 7 步 rollout）

> 审计对象：`internal-ai-audit-governance-deterministic-fact-ids-v1.design.md`（下称「设计」）
> 审计基线：工作树（HEAD `acfaaf4` + B3-1 未提交变更：0042/0043 迁移、relay/claim/cleanup 编辑）
> 方法：逐文件核对 `git status`/`git diff`、消费者 grep（uuid/ID 格式假设/长度/正则）、Makefile 与 `.github/workflows/integration-pg.yml` 门禁核对、测试钉死格式扫描。

---

## 1. 零重叠主张（C1 / §8）— 实质成立，措辞两处不准确

| 设计触碰面 | 工作树状态 | 结论 |
|---|---|---|
| `internal/auditgovernance/facts.go` | **干净** ✓（无 M） | 零重叠成立 |
| `internal/repository/audit_governance_write.go` | **干净** ✓（无 M） | 零重叠成立 |
| `internal/repository/audit_governance_types.go` | **B3-1 已改**（接口块 :82-96 新增 `FailAuditGovernance`/`CleanupFailedAuditGovernance`） | 设计 §3.2 在**同一文件** struct 区（:36-53，`ID` 字段后）加 `SourceID`——**区域不相交**，patch 无冲突；但「零重叠」按文件粒度不成立 |
| 0042（`failed_at_ns` 列）/ 0043（两个 partial index） | 新增迁移 | 只触碰 `failed_at_ns`/索引谓词，与 `id` 列语义零交互 ✓ |
| B3-1 claim/cleanup/relay 编辑 | 已改 | claim 谓词加 `failed_at_ns=0`、cleanup 新增 prune——与 ID 零交互 ✓ |

**修正 1（文档准确性）：** 设计 §1 C1「只触碰这两个干净文件 + 类型文件，与未提交变更零重叠」与 §8「只改三个干净文件 + 新文件」自相矛盾——干净文件只有**两个**（facts.go、write.go）；types.go 是 B3-1 脏文件，仅**区域**不相交。建议措辞改为：「两个干净文件 + types.go（与 B3-1 共享文件、区域不相交：struct 字段 vs 接口方法）」。

**修正 2（方向性依赖）：** 设计 §7 AC-1 测试依赖 B3-1 的 `FailAuditGovernance`/`CleanupFailedAuditGovernance` 签名（claim→fail→prune→gap 生命周期）。「合入顺序无耦合」对代码区域成立，但**设计的测试套件要求 B3-1 已在树中**——两者须同提交或 B3-1 先落。当前二者同处工作树、单提交叠加上去即满足；若 B3-1 被回退，设计的 §7 测试将无法编译。

---

## 2. 在途行不重写 + 新旧 ID 混合 — 保证成立，逐项核实

**不重写：** 设计全部改动落在三个 INSERT 侧方法（RecordAuditWithGovernance / InsertEventWithGovernance / EnqueueAuditGovernance），无任何对 outbox `id` 的 UPDATE、无迁移、无回填。升级前入队的行 UUID 恒保留到投递/失败。✓

**混合 ID 的消费者安全性（全部为不透明字符串语义，grep 证实无格式假设）：**

| 消费者 | 用法 | 混合安全 |
|---|---|---|
| claim/complete/retry/fail（claim.go） | `WHERE id=$X AND owner AND token` 相等匹配 | ✓ 不解析格式 |
| `auditGovernanceCols` + scan | `id` 按 string 扫描 | ✓ |
| `boundedBackoff(fact.ID,…)`（relay.go:186） | sha256 字节哈希（抖动） | ✓ 内容无关 |
| `opaqueFact`（redaction.go:51） | digest(fact.ID) | ✓ 内容无关 |
| `governanceWire`/`receiptMatches`（http.go:148/153/216） | `EventID`/`IdempotencyKey = fact.ID`，相等比较 | ✓ 契约 A 不透明字符串（B3-1 http.go 改动仅重构 conflict 处理，未动身份映射） |
| 去重 | `ON CONFLICT (origin_kind,origin_id)`（write.go:140，UNIQUE 0039） | ✓ **按 origin 元组**，与 ID 格式无关——混合行不会碰撞/双投 |
| gap 查询 | `JOIN … ON o.origin_kind=… AND o.origin_id=…` | ✓ 格式无关 |
| 0042/0043 | `failed_at_ns DEFAULT 0` + partial index 谓词 `delivered_at_ns=0 AND failed_at_ns=0` | ✓ 旧 UUID 行默认 0 → 可 claim、可索引 |
| 0039 `id TEXT PRIMARY KEY` | 无长度/格式约束 | ✓ 36 字符 UUID 与 32-hex 共存 |

**字母表不相交：** 旧 ID 含 `-`（36 字符），新 ID 为纯 `[0-9a-f]{32}`——无跨格式碰撞。✓

**测试钉死扫描：** 全仓 `len(…ID)==36` / uuid 正则零命中；B3-1 新测试（relay_terminal/relay_metrics/pending_idx）用 `uuid.NewString()` 构造但只经返回值 round-trip 断言（claim/fail 按返回 ID），设计「入队时无条件覆写 ID」后这些 round-trip 仍成立；`TestAuditGovernanceReconcileFindsAndDeduplicatesLocalFacts`（当前树 :98-135，与设计引用一致）断言 `!inserted` 二次入队——同 origin 元组 + 同 hex ID 下 `(true,nil)/(false,nil)` 保持。✓

---

## 3. 7 步 rollout — 一个悬空命令 + 一个真实覆盖缺口（可修复）

### 3.1 缺陷 A：`make test-integration-pg` 不存在（§5 F3 / §6 步骤 6 / §8 均引用）

Makefile `.PHONY` 与 recipe 只有 `test-integration`（本地 Docker pgvector，跑全部 `-tags=integration`）与 `test-integration-qdrant`。真正的 PG 门禁是 `.github/workflows/integration-pg.yml`（**仅 workflow_dispatch**，跑 `go test -tags=integration ./internal/integration/ -run 'TestPostgres|TestPg'`）。**修正：** §6 步骤 6 / §8 / §5 F3 的 `make test-integration-pg` 应改为 `make test-integration`（本地）或 integration-pg.yml 手动触发（CI）。注意 workflow 为 opt-in、CI 从不自动跑 PG——实施者必须主动执行。

### 3.2 缺陷 B：设计唯一 SQL 变更（`RETURNING id, created_at`）的 **PG 分支零覆盖**（F3 缓解句事实不成立）

全仓 grep：`InsertEventWithGovernance` 只有 `audit_governance_test.go:56`（SQLite，make check 覆盖）与接口声明。PG 集成套件（`audit_governance_postgres_test.go`）只经 `RecordAuditWithGovernance`/`Claim`/`Complete`/`CleanupDelivered`/`ListGaps`/`Enqueue`——**没有任何测试在 PG 上执行被改的 SQL**。`make test-integration` 与 integration-pg.yml 跑完也覆盖不到该分支。设计 §5 F3「PG 分支由 make test-integration-pg 覆盖」**按现状为假**。

**修复（二选一，建议 a）：**
- (a) 在 `audit_governance_postgres_test.go` 增 `TestPostgresInsertEventWithGovernanceReturnsCanonicalizedOccurred`：零 `CreatedAt` 事件 → `InsertEventWithGovernance` → 断言返回 id、`RETURNING created_at` 扫描成功、fact.ID 为 32-hex 且与 gap 重建一致（即 §7 AC-1 file 生命周期的 PG 镜像）。此测试同时被 `make test-integration` 与 integration-pg.yml（`-run 'TestPostgres…'`）覆盖。
- (b) 将 F3 缓解句降级为「文档化残余」并写明理由（multi-column RETURNING 为 PG 标准语法；`flexTime.Scan(time.Time)` 已在 PG 上被 quota/jobs 路径证明——风险低）。

### 3.3 顺序充分性结论

`make check`（fmt → vet → **vet-integration（`go vet -tags=integration`，零 Docker 下编译全部集成测试）** → build → test → test-race-meta → cli-check）先于 PG 门禁的顺序**正确且充分**——前提是 3.2 的 PG 测试补上（否则顺序再正确也测不到被改的 PG SQL）。SQLite 侧断言（RETURNING 多列 ≥ SQLite 3.35：`modernc.org/sqlite v1.50.1` ✓；`flexTime` 兼容 time.Time/[]byte/string，sql_helpers.go:196-231 ✓）由 `make check` 全覆盖。

### 3.4 非阻断措辞修正

- §7「无 REQ-2 时此断言必败」**夸大**：无 REQ-2 时 file AC-1 的失败是概率性的（仅当 ns `now` 与 DB ms 存储值落在不同秒桶才失败，多数运行会过）——有 REQ-2 后为确定性通过。建议改「无 REQ-2 时为秒边界概率性失败（flaky），有 REQ-2 后确定性通过」。
- §7 AC-1 admin 的 REQ-2 机制核实无误：write.go:18-21 已兜底 `entry.CreatedAt` 非空 → INSERT 存储同一字符串 → gap 读回同值（SQLite 字节一致 / PG µs 截断），公式秒级截断吸收 µs/ns 差（除精确秒边界，即 F1 已文档化残余）。✓

---

## VERDICT：PASS（带 2 项必改 + 2 项措辞修正）

- 零重叠：hunk 级成立，文件级措辞不准确（C1/§8 需改「两个干净文件 + 与 B3-1 共享 types.go 但区域不相交」）；设计的 §7 测试依赖 B3-1 API，须同提交。
- 在途行不重写 + 新旧 ID 混合：**保证成立**——消费者全部按不透明字符串处理，去重/gap/索引均按 origin 元组，0042/0043 与 ID 语义零交互，字母表不相交，无测试钉死格式。
- 7 步 rollout：顺序正确但 **§6/§8/F3 引用的 `make test-integration-pg` 是悬空目标**（实为 `make test-integration` 或 integration-pg.yml workflow_dispatch），且被改 PG SQL 无任何集成测试执行——补 3.2(a) 的 PG 测试后，`make check` + PG 门禁即充分。
