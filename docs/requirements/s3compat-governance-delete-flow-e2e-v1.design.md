# Design v1 (implementation-ready) — adapter governance e2e: fail-closed DELETE flow

**Module:** `internal/api/s3compat` (test infra `audit_governance_e2e_test.go` + `authz_gate_test.go`)
**Basis:** `docs/requirements/s3compat-governance-delete-flow-e2e-v1.spec.md`（验收契约）· **HEAD:** `15763e28`（worktree **非干净**：~202 modified/untracked 文件，属 campaign 状态；本设计全部引用均已对照工作树内容逐行核验）
**Scope:** 纯测试改动。**零生产代码变更、零 schema 迁移、零配置变更、零 go.mod 依赖。**
**Verification status:** 证据中全部可证伪断言已在本轮独立复验——含一次性 probe（生产形状装配、裸 SQLite 第二连接）实证 §2 四表行数、T-4 byte-identical 复用、T-3 claim/lag 排除、Gate 零副作用，随后移除 probe，`go test ./internal/api/s3compat/ ./internal/repository/` 全绿。
**Review amendments（v1.1）：** 合入 reviewers 强制意见后重读全部生产锚点：① **Finding A**——删除行读取一律 type-filtered（§1/§2.3/§5 T-4.2 ③）；② **Flag A**——Gate 绑定实际调用 `assertZeroSideEffects`（§4 step 5 / §5 Gate ④）；③ **B–D 加固注释**（Gate fixture 归因注释、T-3 正对照、total==1 消息双义）；④ 行号勘误：T-4.1 ① 锚点 `delete.go:15-38` → `handler.go:265-293`（204 写于 :292）、`event_outbox.go:220` → `:223`、`file_delete.go:123-140` → `:123-145`、e2e 调用点 `:138/:240` → `:140/:241`、`audit_governance_test.go:511-542` → `:519-545`、§0 各证据行号；⑤ R-2 dialect 分支措辞修正（三处，非仅 claim）。

---

## 0. Evidence disposition（证据逐条核验结果）

| 证据断言 | 核验方式 | 结果 |
|---------|---------|------|
| `authz_gate_test.go:360,392-394` — `assertZeroSideEffects` 只查 object_events + audit_log | 读源码 :394-416（comment :392、func :394） | ✅ 精确。两计数查询，无 `audit_governance_outbox` |
| `authz.go:27-45` — `authorizeDelete` fail-closed（nil/error/deny → 403，service 调用之前） | 读源码 + 调用点（extra.go:441、policy.go:71） | ✅ 精确。函数体 27-45；返回 false 即 403 |
| `file_delete.go:123-145` — `deleteFacts` 恰好 2 facts（deleted@1.1+notify@1.1，同 origin） | 读源码 :123-145 | ✅ 精确；且确认载体为 `event_outbox`（event_outbox.go:223 事务内 `insertOutboxFacts`，func :229），不经 `InsertEventWithGovernance` |
| `audit_governance_write.go:53-97,111-137` — event-type 无关插入、store-authoritative `DeterministicFactID` | 读源码 :53-97（PG `$7::jsonb` dialect 分支 :68-71）、:111-137 | ✅ 精确。`InsertEventWithGovernance` 每次仅随一个 bus 事件调用一次 |
| `audit_governance_claim.go:35-65,76-96,107-122,211-221` — `delivered_at_ns=0 AND failed_at_ns=0` 谓词 | 读源码（PG claim :35-65〔谓词 :55〕、SQLite select :76-96〔谓词 :78-80〕、per-id 更新 :107-122〔谓词 :110〕、`OldestPendingAuditGovernance` :211-221〔谓词 :216-218〕） | ✅ 精确 |
| **核心纠正**：允许的 DELETE → 恰好 **1 行** `file.deleted`（非 2 行）；PUT+DELETE 全程 2 行 | **probe 实证**：total=2（file.created + file.deleted）、file 行 2 / admin 行 0、object_events=2、event_outbox=2、audit_log file.delete=1 | ✅ **成立**（DELETE 在 bus 上单次 `emit(EventDeleted)`，file.go:308-325 → 包装 repo 的 `InsertEvent` → `InsertEventWithGovernance`） |
| T-4 byte-identical gap 复用（spec 引 `38f58845…`→`38f58845…`） | **probe 实证**：prune delete 行 → gaps=2（admin `file.delete` + file `file.deleted`）→ 按 Action 重建入队 → `ce405b6f…` → `ce405b6f…` 字节一致；重复入队 `(false,nil)` | ✅ 属性成立。**具体哈希值随运行时间戳变化**（occurred_at 派生）——验收 pin 的是 byte-identical 属性，不 pin 具体值；既有金锚 E（`3494289b…`）不动 |
| E8 陷阱：删除的 audit_log 产生 **admin-kind gap**，`len(gaps)==1` 不可用 | **probe 实证**：prune 后 gaps=2，admin gap 在前（merge 序） | ✅ 成立。验收必须按 `Action` 谓词选取 |
| e2e harness 硬编码 `allowAllProvider{}` | 读 `audit_governance_e2e_test.go:104-105`（svc 侧 :104、router 第三参 :105） | ✅ 成立。`NewRouter(svc, nil, authz)`（router.go:14）第三参即 adapter 门禁 provider |
| `make check` 约束 | 读 Makefile :172-177 | ✅ 500 行门禁排除 `*_test.go`（CI 不拦测试文件；但 house 惯例新测试文件仍应 ≤500 行——v2 design 发现 (d)）。`authz_gate_test.go` 已 570 行（存量超限，本次不扩大） |
| 拒绝路径四表零新增 | **probe 实证**：denyAllProvider → DELETE 403，gov total 维持 1、无 `file.deleted` 行 | ✅ 成立 |

**结论：证据可信，spec 的"关键纠正"被独立复验确认。** 本设计按 spec §4 验收落地，全部断言在 HEAD 上已通过 probe 预跑。复核轮另确认：`deleteFacts` 的两 facts 载体与治理行分离（`event_outbox` vs `audit_governance_outbox`）、`InsertEventWithGovernance` 的 `ON CONFLICT DO NOTHING` 折叠（`audit_governance_write.go:139-166`，`ON CONFLICT` 追加于 :160）、claim 谓词全套位置、`assertZeroSideEffects` 在治理 e2e 中**非空转**的绑定方式（§4 step 5 / §5 Gate ④）。

---

## 1. API changes（变更面）

**生产 API：零变更。** 不触碰 `internal/service`、`internal/repository`、`internal/auditgovernance`、`internal/api/s3compat` handler/路由的任何导出符号。本方向是测试缺口（行为已正确），不是行为缺口（spec §5 边界同此）。

**测试面（同包内，不导出）：**

| ID | 变更 | 性质 |
|----|------|------|
| T0 | `audit_governance_e2e_test.go`：把 `newGovernanceE2EServer(t, bindingState)` 装配抽为 `newGovernanceE2EServerWithAuthz(t, bindingState string, authz AuthorizationProvider)`；原函数一行委托 `allowAllProvider{}` | 测试基建重构（FR-5），零行为变化 |
| T1 | 新文件 `internal/api/s3compat/audit_governance_delete_e2e_test.go`：`TestS3CompatAuditGovernanceDeleteFlow`（T-4.1+T-4.2）、`TestS3CompatAuditGovernanceDeleteClaimLag`（T-3）、`TestS3CompatGovernanceDeniedDeleteZeroRows`（Gate） | 新增用例 |
| T1a | `audit_governance_e2e_test.go`：新增 type-filtered 删除行读取 helper `governanceOutboxRowForAction(t, dsn, bucket, key, action string)`（与 `governanceOutboxRow` 同构，额外 `AND o.action=?` 谓词）；`governanceOutboxRow` 保持原签名原语义 | 新增 helper（Finding A） |
| T2 | `authz_gate_test.go`：`assertZeroSideEffects` 追加一条 `audit_governance_outbox WHERE action='file.deleted'` == 0 断言 | 检测器扩展，追加式（见 §2 兼容性） |

**放置决策（对 spec §6 的偏离，需记录）：** spec 建议新用例放 `audit_governance_e2e_test.go`（现 269 行）。T-4/T-3/Gate 三用例 + 基建约 230-280 行，并入后 ~500-550 行，逼近 review 级 500 行惯例（v2 design 发现 (d) 明确要求新测试文件守线）。故拆为同包新文件，复用既有 helper（`e2eSourceID`/`governanceOutboxRow`/`do`/`testShareSecret`/`assertAccessDenied`/`denyAllProvider`），同包跨文件可见，零重复实现。金锚 D/E（e2e :167-176）仍留在原文件，不得复制。**helper 复用清单（Finding A 修订）：** `e2eSourceID`/`do`/`testShareSecret`/`assertAccessDenied`/`denyAllProvider`/`governanceOutboxRowForAction`（新增）；`governanceOutboxRow` 仅保留给 counts/found-agnostic 检查（§2.3），**删除行读取一律禁用它**。

---

## 2. Compatibility constraints（兼容性约束）

1. **既有调用点零迁移**：`newGovernanceE2EServer` 的两个既有调用（`TestS3CompatAuditGovernanceDeterministicFactID` :140、`TestS3CompatAuditGovernanceCaptureInactive` :241）经委托保持 `allowAllProvider{}`，语义逐字节不变。
2. **`assertZeroSideEffects` 追加断言对既有调用安全**：raw-server 门禁用例（`TestDeniedDeleteWritesNoOutboxRows` :324-390 等）不装配治理，`audit_governance_outbox` 表存在但恒空 → 新增 `COUNT(*) WHERE action='file.deleted'` == 0 恒真。追加式扩展不改变现有断言语义。
3. **I1 占位符纪律 + 删除行读取专用化（Finding A）**：所有裸 SQL 走 sqlite 第二连接，一律 `?` 占位符；**不得**在测试 SQL 中出现 `$N`（不经过 rebind，sql.go:42）。**删除行读取（含 T-4.2 post-re-enqueue byte-identical 重读）必须**经新增 type-filtered 变体 `governanceOutboxRowForAction(t, dsn, bucket, key, "deleted")`（JOIN 查询加 `AND o.action=?`，QueryRow 只可能命中一行）；**显式禁止**用无过滤 `governanceOutboxRow` 读删除行——PUT+DELETE 后同 bucket/key 的 `file.created` 与 `file.deleted` 两行并存，无过滤 `QueryRow` 返回未指定行（scan 序大概率低 rowid 的 `file.created`），产生假 `reID != id` 或错行 action 断言。无过滤 helper 仅保留给 counts/found-agnostic 检查（capture-inactive :250 的 found/count 语义）。先例模式：`governanceOutboxRow` :113-136，新变体同构（`?` 占位符）。
4. **I2 迁移纪律**：零迁移文件。断言依赖的表/列全部存在于当前 schema（`audit_governance_outbox`、`object_events`、`event_outbox`、`audit_log`）。
5. **F9 确定性纪律**：期望 ID 计算只用从 DB 行读回的 `created_at`（`RETURNING id, created_at` 规范化值），不引入 `time.Now()` 参与期望值数学。金锚 D/E 直接复用既有常量与断言（e2e :167-176），不新建复制品。
6. **E8 陷阱固化**：`ListAuditGovernanceGaps` 的断言**必须**按 `Action=="file.deleted" && OriginKind=="file"` 谓词选取，`len==1` 断言禁用（prune 后 admin `file.delete` gap 必然同在，merge 序 admin 在前）。
7. **Gate 装配形状固定 + fixture 归因注释（Finding B）**：被拒 fixture 中 service 侧 `WithAuthorizer(allowAllProvider{})`、router 第三参 `denyAllProvider{}` —— 与生产装配一致（门禁在 adapter 层、service 授权器是 REST 侧基线，两者独立）。任何一侧翻转都会使 Gate 用例失去对 adapter 门禁的指向性。**fixture 必须携带注释写明归因**：403 必须来自 router 侧（adapter）门禁而非 service 侧——service 翻 `denyAllProvider{}` 时用例仍 403 + 零行（现在来自 service 门禁），adapter 门禁被静默跳过（自中和）；router 翻 `allowAllProvider{}` 时泄漏被放行。两侧同 deny 或同 allow 均使 detector 失明。
8. **线路预算**：新文件 ≤ 500 行（review 级）；`authz_gate_test.go` 的存量 570 行超限**不**在本方向扩增（仅 +1 断言），记入 §5 遗留。
9. **`make check` 全绿**：`gofmt`、`go vet`、`go build`、`go test ./...`（SQLite+local FS）均为最终验收门槛；本方向无网络/Docker 依赖。

---

## 3. Failure modes（失败模式与检测映射）

| # | 失败模式 | 后果（若未检测） | 检测断言（落点） |
|---|---------|----------------|----------------|
| FM-1 | 事件路径装配破损（包装 repo 未接 bus / `InsertEventWithGovernance` 被绕过）——`Bus.Publish` 设计上吞错，表现为 **HTTP 200 + 0 治理行** | 治理静默漏采，PUT/DELETE 均无审计事实 | T-4.1 断言 `COUNT(*)==2`（F5 检测器扩展到 DELETE 流；既有 PUT 单行检测器同构） |
| FM-2 | DELETE 路径回归为多次 `emit`（或 delete-marker 路径误发双事件） | 治理表出现 3 行 / action 集合漂移 | T-4.1 action 集合恰为 `{file.created, file.deleted}` + 总行数 == 2 |
| FM-3 | 删除事务内 `insertAuditEntry`（audit.go:21，直写）回归为 `RecordAuditWithGovernance` 双写 | 治理表出现 admin 行，接收端双记账 | T-4.1 `origin_kind='admin'` 计数 == 0（锁定原子路径不对称行为） |
| FM-4 | occurred 规范化漂移（`RETURNING created_at` 与 gap 解析路径分叉） | 原子路径与 gap 路径 ID 分桶不一致 → 重复投递 | T-4.1 `occurred_at_ns == created.UnixNano()` + ID 行级重算 |
| FM-5 | `EnqueueAuditGovernance` 的 `ON CONFLICT (origin_kind,origin_id) DO NOTHING` 折叠回归 | 重复入队产生第二行 / 返回语义漂移 | T-4.2 重复入队 `(false, nil)` + byte-identical 重读（经 `governanceOutboxRowForAction(...,"deleted")`）`recount==1` |
| FM-6 | claim 谓词 `failed_at_ns=0` 回归（死行可再 claim / 进 lag） | 终态行被重复投递 | T-3 换 owner 再 claim == 0 行 + `OldestPendingAuditGovernance` 排除 |
| FM-7 | 被拒 DELETE 泄漏副作用（门禁后移、或授权器被误换为 allow） | fail-closed 契约失效（403 保对象/零行） | Gate：403 + 治理表维持 1 行 + `object_events`/`audit_log`/`event_outbox` 零新增 + GET 200 |
| FM-8 | `mergeGovernanceGaps` 排序/合并回归（admin/file 交替序变化） | gap 断言依赖序 → 脆弱 | 缓解：全部 gap 断言按 Action 谓词选取，不依赖返回序（T-4.2、Gate 均如此） |
| FM-9 | 测试自身 flake：sqlite 第二连接与主连接写并发 | 偶发 `database is locked` | 沿用既有模式（写后读、读只读查询、`-count=1`）；不引入并发写 |

---

## 4. Migration steps（迁移/落地步骤）

**生产迁移：无。** 无 schema、配置、密钥、部署步骤；本方向不改变任何运行时行为，可独立于发布周期合入。

**代码落地顺序（每步后跑 `go test ./internal/api/s3compat/ -count=1`）：**

1. **T0 基建重构**：`newGovernanceE2EServerWithAuthz` 抽出（router 第三参注入）；既有两用例经委托零改动通过。
2. **T1a T-4.1**：新文件建 `TestS3CompatAuditGovernanceDeleteFlow` 前半（PUT+DELETE → 行数/action 集合/ID 重算/event_outbox 载体澄清）。
3. **T1b T-4.2**：同用例后半（prune → gap 按 Action 选取 → byte-identical 复用 → 重复入队 `(false,nil)`）。
4. **T1c T-3**：`TestS3CompatAuditGovernanceDeleteClaimLag`（claim 2 行 → complete created / fail deleted → 再 claim 0 → lag 排除）。
5. **T1d Gate**：`TestS3CompatGovernanceDeniedDeleteZeroRows`（denyAllProvider fixture → 403 + 四表零新增 + GET 存活；fixture 携带 §2.7 归因注释）。**必须实际调用 `assertZeroSideEffects(t, dsn, obj)`（spec Gate.4 绑定）**——使 +1 治理表断言的唯一非空转执行点（raw-server 调用点治理表恒空，见 §2.2）。批量 `?delete` 双 key 全拒变体并入。
6. **T2**：`authz_gate_test.go` `assertZeroSideEffects` +1 断言（治理表 `file.deleted` == 0）。
7. **收尾**：`make check` 全绿（gofmt / build / vet / test ./...）；新文件 `wc -l` ≤ 500。

**验证口径（与 §5 验收一一对应）**：所有断言已在 HEAD 上经 probe 预跑一次性通过——本步骤序列不预期触碰任何生产代码，若某步触发生产改动需求即为范围外信号（spec §5 边界）。

---

## 5. Testable acceptance mapping（验收映射）

| Spec §4 项 | 测试函数 | 关键断言 → 代码/证据锚点 |
|-----------|---------|------------------------|
| **T-4.1** capture + 确定性 ID | `TestS3CompatAuditGovernanceDeleteFlow`（第一部分） | ① DELETE 204（handler.go:265-293 `DeleteObject`，204 写于 :292；`deleteS3Object` 实现 delete.go:11-38）；② gov 表 total==2、action 集合 `{file.created,file.deleted}`、`origin_kind='file'`×2 且 admin==0（FM-3 锁定）；③ delete 行（经 `governanceOutboxRowForAction(...,"deleted")` 读取）JOIN `object_events e` → `e.type='deleted'`、`occurred_at_ns == Parse(e.created_at).UnixNano()`（REQ-2 parity）；④ `id == DeterministicFactID(e2eSourceID(testShareSecret…), "default", "file.deleted", "file", originID, created)` 且 32-hex、两行互异（REQ-3 行级重算，e2e :180-183 同构）；⑤ `event_outbox`==2（`vault.file.deleted@1.1`+`vault.file.notify@1.1`，origin=objects 行 id ≠ 治理 origin_id）——载体澄清 |
| **T-4.2** 已删 origin 的 T-4 gap 复用 | `TestS3CompatAuditGovernanceDeleteFlow`（第二部分） | ① prune delete 行；② `ListAuditGovernanceGaps` → 按 `Action=="file.deleted" && OriginKind=="file"` 谓词选取（**禁 `len==1`**，E8 陷阱）；③ factFromGap 等价重建（e2e AC-2 :218-228 同构）→ `EnqueueAuditGovernance` → `(true,nil)` 且**经 `governanceOutboxRowForAction(...,"deleted")` 重读 ID 字节级一致**（Finding A：此处两行并存，无过滤 `governanceOutboxRow` 返回未指定行——禁用）；④ 再入队 → `(false,nil)`（FM-5） |
| **T-3** claim/lag parity | `TestS3CompatAuditGovernanceDeleteClaimLag` | ① **正对照（Finding C）**：初始 claim（revision=1，与 fixture binding `Revision: 1` 一致，e2e :187 同构）→ **恰 2 行**、按 Action 区分 created/deleted、`revision=1` —— claim-0 负断言仅在此时非空转，实现不得在"简化"负断言时删掉正对照；② `CompleteAuditGovernance(created)` + `FailAuditGovernance(deleted, "probe-fail")`；③ 换 owner claim → 0 行、`OldestPendingAuditGovernance` → `(_, false)`（E6 谓词在 HTTP seam 复现，与 repository pin `audit_governance_test.go:216,295,519-545` 同构） |
| **Gate** 被拒 DELETE 零副作用 | `TestS3CompatGovernanceDeniedDeleteZeroRows` | ① `newGovernanceE2EServerWithAuthz(t, "active", denyAllProvider{})`；fixture 注释写明 service=`allowAllProvider{}` / router=`denyAllProvider{}` 的 adapter-gate 归因（§2.7，防翻转自中和）；② DELETE → 403 `AccessDenied`（authz.go:27-45 在 svc 调用前拒绝，policy.go:70-71 调用点）；③ gov 表 total==1（仅 `file.created`，无 `file.deleted` 行）——断言消息须区分 **'capture active'**（FM-1 布线正常，PUT 已采 1 行）与 **'adapter gate'**（FM-7 归因：被拒 DELETE 未新增行）两义，避免 0 行时误导为门禁问题（Finding D）；④ **调用 `assertZeroSideEffects(t, dsn, obj)`**（spec Gate.4 绑定；治理 e2e 中治理表非空 → +1 断言非空转）`+ event_outbox` 双类型零计数 + GET → 200（对象存活）；⑤ 批量 `?delete` 双 key 全拒同断言 |
| **Gate（检测器扩展）** | `assertZeroSideEffects`（authz_gate_test.go） | 追加 `COUNT(*) FROM audit_governance_outbox WHERE action='file.deleted'` == 0；raw-server 既有用例（治理表恒空）与治理 e2e 共用同一检测器 |

---

## 6. Scope boundaries（明确不做）

| 项 | 理由 |
|----|------|
| 修改任何生产代码 | 实证：HEAD 行为已满足修正后验收，方向是测试缺口（spec §2/§5 同此） |
| 按字面实现"DELETE 产 2 行" | 与实证写入面矛盾（DELETE 单事件 → 1 行 `file.deleted`；"2"仅作为 PUT+DELETE 全程总数保留） |
| 修复 admin-gap 不对称（删除的 audit_log 原子路径无 admin 治理行） | 既有 T-4 行为、方向未要求；验收以 Action 谓词规避 |
| 版本化桶 delete-marker / `?versionId` 流 | 单次 `EventDeleted` 机理一致，方向范围外，留后续 |
| `authz_gate_test.go` 570 行存量超限 | 存量问题，本方向仅 +1 行断言；新文件守 500 行 |
| 治理断言下沉 repository/relay 层 | 方向定位 adapter e2e seam；repository 层已有等价 pin |
| 修改既有金锚 D/E 或新增哈希金锚 | 具体事实 ID 随 occurred_at 变化，pin 属性（byte-identical）而非值 |

## 7. Residual risks（遗留风险）

- **R-1（既有，非本方向引入）**：`listGovernanceAuditGaps` 忽略 `created_at` 解析错误、`factFromGap` 在零 `OccurredAt` 时回退 live clock（v2 design 发现 (gt-2)）——delete 流同样暴露于此；修复超出本测试方向。
- **R-2**：本方向全部断言在 sqlite 路径验证；PG 路径的 delete-flow 治理行为依赖同一 `InsertEventWithGovernance`，仍建议后续 PG 集成测试对偶（`make test-integration` 域外）。dialect 分支实际存在于**三处**（修正此前"仅在 claim 处"的措辞）：claim 分发 `audit_governance_claim.go:29`、`InsertEventWithGovernance` 的 `$7::jsonb` cast `audit_governance_write.go:68-71`、cleanup 函数 `audit_governance_cleanup.go:23,119`；其中 jsonb cast 与 claim/fail/lease 谓词已由既有 PG 集成测试覆盖（`TestPostgresAuditGovernanceInsertEventRoundTrip` / `TestPostgresAuditGovernanceConcurrentClaimsAndLeaseRecovery` / `TestPostgresAuditGovernancePruneReenqueueSameID`），结论不变。
