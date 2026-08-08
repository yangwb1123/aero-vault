# 设计：双后端 outbox 投递 + `vault.file.deleted@1.1` schema 一致性验收套件（`internal/integration`）

> **上游：** `docs/requirements/internal-integration-dual-backend-outbox-suite-v1.spec.md`（验收规格，181 行，已核验 HEAD `acfaaf4`）
> **本设计结论先行：** 交付物 **G1–G4 全部为新增测试文件 + 一处测试 harness 增量（可选参数）**；**生产代码零改动、零新迁移、零新依赖**。唯一"API 变更"是 `fullserver_test.go` 的测试构造器增量（additive，默认 nil，现有 3 个构造器与全部调用点零影响）。
> **依赖前置：** `transactional-outbox-delete-events-v1.spec.md`（已合入）、`transactional-outbox-kernel-v1.md`。
> **修订记录（v1.1 — 三审合并）：** 本版合并三轮 adversarial review 的修正集——① SQL 可移植性（`adversarial_review-9c87f3a7`）、② 时序确定性（`adversarial_review-timing-determinism`）、③ 变异注入（`adversarial_review-mutation-injection`）。全部修正已内联到对应章节；§0.1 为修正登记表与**四组 pairwise 冲突核验**结论；§6 检查单含新增约束 C-11…C-15 与三审全部新增项；§7 为 SQL 审查加固建议的落地（`make check` 零 Docker 编译门 + opt-in PG 任务，配套 `Makefile`/`ci.yml`/`integration-pg.yml` 变更已存在且 `go vet -tags=integration ./...` 实测绿）。

---

## 0. 证据核验结论（本设计的依据，全部已对照 HEAD 复核）

| 证据断言 | 核验结果 |
|---------|---------|
| spec 181 行中文、镜像 sibling 格式 | ✅ 文件存在，恰 181 行 |
| `TestPostgresAuditGovernanceConcurrentClaimsAndLeaseRecovery` :18-80、`//go:build integration` :1、seed 仅 `tenant.status` :121/:123 | ✅ 逐条相符 |
| `lockGovernanceRows` :130 为 `FOR UPDATE`；SKIP LOCKED 在 `audit_governance_claim.go:31-49` | ✅ 相符（claim 谓词 `FOR UPDATE OF o SKIP LOCKED`） |
| `WrapRepository`/`RecordAuditWithGovernance`/`InsertEventWithGovernance` | ✅ `repository.go:15-21/27-38/40-55` |
| relay.go claim/lease/gap 重建六函数 | ✅ `:16/:59/:80/:111/:124/:139/:163` |
| `bus.go Publish` :80-98 先持久化后广播、错误仅 warn | ✅ 相符 |
| `repository.Event` :174-194 无版本字段、四常量 | ✅ 相符 |
| `vault.file.deleted@1.1` 在 event_outbox（迁移 0041） | ✅ sqlite+postgres 双方言 CHECK 约束 |
| 审计治理 outbox 只存 redacted 摘要 | ✅ 注释在 `types.go:35`（spec 引 :28-30，5 行漂移，非实质） |
| 既有测试清单（schema_test :31/:42、fullserver :702/:893/:1032、admin_files_delete :112/:167、audit_governance_test :45/:300/:334、runtime_test :52/:117/:235/:295） | ✅ 全部存在于引证行号 |
| G1–G4 交付物文件不存在 | ✅ 四文件均 absent，缺口属实 |
| `main.go:81` 包装时序（Start → WrapRepository → `bus.WithRepository` → 下游全用包装后 repo） | ✅ 相符（:81/:82/:214） |
| `startFullServerOpts` 为共享体、3 个构造器委托（`startFullServer` :50 / `startFullServerWithRelay` :58 / `startFullServerWithAuthAndRelay` :66，共享体 :72） | ✅ 相符（原设计引 :58/:66，漏 :50，已修正） |
| `pgDSN` :27 / `freshRepo` :36；探活失败自动 skip 惯例 | ✅ 相符 |
| go.mod 无 JSON-Schema 校验库 | ✅ 无（I6：测试内手写校验器，不引依赖） |

**发现的两处 spec 未明说、但影响设计的机制事实：**
1. **`runtime.New` 内部已执行 `applyDesiredBindings`**（`runtime.go:66-70`）——G4 harness 装配**不需要**手动调 `ApplyAuditGovernanceBindings`，只需在 config.Bindings 里给出测试租户。
2. **`audit_governance_test.go` 是 `package repository_test`（黑盒），`event_outbox_test.go` 是 `package repository`（白盒）**——G1/G2a 沿用黑盒形状（走 `AuditGovernanceStore` 接口）；G2c 走 `EventOutboxStore` 接口（`repository_interface.go:106-111`）+ `freshRepo` 的裸 `*sql.DB` 做状态断言（`admin_files_delete_test.go:31-61` 既有 DSN 直读形状）。

### 0.1 三审修正登记表（A1–A18）与冲突核验

**修正登记**（"必须/建议"取 mutation-injection §3 的 5+1 分组；其余为两审的硬性修正）：

| # | 来源 | 修正 | 落点 |
|---|------|------|------|
| A1 | mutation **V1**（必须） | G1 dedupe 子断言前置"首次 enqueue 必须 `(true,nil)`"（缺失绑定下 `(false,nil)` 假真通过，探针 A2a 实测） | §5 AC-1 G1 表 |
| A2 | mutation **V2**（必须） | G1 增 wrapper 路径 digest 子测试：`auditgovernance.New`→`WrapRepository`→`wrapped.InsertEvent` 驱动，杀 MUT-B/MUT-D；黑盒 store 接口+手造 fact 是同义反复 | §5 AC-1 G1 表 |
| A3 | mutation **V3**（必须） | G1b/G3 字段循环前加**行数前置断言**：deleted@1.1 恰 1 + notify@1.1 恰 1（0 行时空真，探针 D2 实测） | §5 AC-1 G1b 表 |
| A4 | mutation **V4**（必须） | G2c 并发 claim 前断言 `COUNT(*)==2`（0 行时平凡不相交，探针 D3 实测） | §5 AC-2 G2c 表 |
| A5 | mutation **V5**（必须） | G4 wire 断言补 digest 相等（`aggregate_id`/`operation_id`）+ POST body 原始串缺席（MUT-D 实测四包全绿不可见） | §5 AC-4 表 |
| A6 | mutation **V6**（必须） | G4 门闩两相化（handler 先发 received 再阻塞）+ 投递轮询按 `fact.ID` + POST 计数按 `event_id` | §5 AC-4 表 |
| A7 | mutation **V7**（建议） | G2b 独立 delete-variant seed 行数自检 | §5 AC-2 G2b 表 |
| A8 | timing **C-11** | G4 配置 `ClaimTTLSeconds:30`（原 3）、`MaxLagSeconds:60`（原 4）——恢复生产校验；`HTTPTimeoutSeconds:5` 与 4s 守卫不动 | §2 C-11 + §5 AC-4 配置表 |
| A9 | timing **C-12** | "恰 1 次 POST"改事件计数：主断言 `delivered ∧ attempts==1`；POST 计数降为诊断 | §2 C-12 + §5 AC-4 表 |
| A10 | timing **C-13** | 阻塞相加 `started` 信号（先例 `runtime_test.go:295`）；等 started 后才发 DELETE；`delivered_at_ns==0` 断言在释放前；释放 cleanup 注册在 `runtime.Close` cleanup 之后（LIFO）；`/token` 不 gate | §2 C-13 + §5 AC-4 表 |
| A11 | timing **C-14** | 租约过期一律回拨 `lease_expires_at_ns=0`（模板 `event_outbox_test.go:693`，`$N` 单版本）；负断言打长 TTL claim，不复制 PG 150ms 形状 | §2 C-14 + §5 AC-2 |
| A12 | timing **C-15** | G2a 拆两条事实（A=fencing 长 TTL、B=崩溃恢复回拨驱动）；删除"150ms TTL"表述 | §2 C-15 + §5 AC-2 G2a 表 |
| A13 | SQL **S1** | F4 回拨 SQL `WHERE id=?` → `WHERE id=$1`（pgx 要求 `$N`、modernc 原生接受；删"PG/SQLite 各一版"） | §3 F4 + §6 |
| A14 | SQL **S2** | G2b 独立 seed + 独立测试，**不碰既有 8/4/13 计数** | §4 Step 4 + §5 AC-2 G2b 表 |
| A15 | SQL **S3** | G2b 不得对 `object_events.payload` 做字节断言（PG jsonb 规范化） | §5 AC-2 G2b 表 |
| A16 | SQL **S4** | G3 校验器 integer 判定 `math.Trunc(v)==v`（JSON 数字解码为 float64） | §5 AC-3 G3 表 |
| A17 | SQL **S5** | G2c stale-complete 断言 `err != nil`（`errEventOutboxClaimLost` 未导出，黑盒不能 `errors.Is`） | §5 AC-2 G2c 表 |
| A18 | SQL **S6** | 门禁 `go build -tags=integration ./...` 替换为 `go vet -tags=integration ./...`（build 不编译 `_test.go`，vet 会）——已落地：`make check` 新增 `vet-integration` 步 + CI Vet 步升级（§7.1，实测 rc=0） | §3 F7 + §6 + §7.1 |
| A19 | SQL 建议 (b) | opt-in PG 服务任务落地：`.github/workflows/integration-pg.yml`（`workflow_dispatch` 手动触发、零 skip 断言、`TestPostgres*`/`TestPg*` 命名约定） | §7.2 + §6 检查单 |

**四组 pairwise 冲突核验（全部无冲突，理由如下）：**

1. **V6 两相门闩 vs 承重 4s<5s 守卫 + C-11…C-15 — 互补，判别器更强。** V6/A10 的 `started` 信号把"阻塞窗口起点"钉在 handler 收到 POST 之后、DELETE 之前——原设计的窗口起点（"DELETE 早于 runtime 首次 claim"）可能空真，两相化后窗口内**必有在途被阻塞 POST**。4s 守卫保持不动（承重：同步实现最早 ~5s 返回，守卫必须 < 5s）；C-11 使 30s 租约 ⊃ ≤4s 窗口 → complete 不丢租约 → `attempts==1` → 恰 1 POST → delivered，全确定性。**序链：阻塞窗口 ≤4s < HTTPTimeout 5s < ClaimTTL 30s < MaxLag 60s**（timing 审查原文"30s ⊂ 5s"为笔误，正确关系如上）。
2. **V2 WrapRepository+InsertEvent 驱动 vs F4 回拨 + F6 — 不同表、互补，无冲突。** V2 走 `audit_governance_outbox`（SQLite 门禁，repository_test 黑盒包 import `internal/auditgovernance`——外部测试包无环）；F4 回拨走 `event_outbox`（G2c，PG tag）——不相交。V2 是 F6 testDigest 公式的**驱动机制**：F6 提供期望字节（锚 `redaction.go:29-45` `writeMACFields`），V2 提供非空真路径；黑盒接口+手造 fact 下 F6 断言是同义反复。`auditgovernance.New` 构造安全（`applyDesiredBindings` 只写 DB 控制行，token 惰性获取，零网络 I/O；最小合法 cfg 模板 = `runtime_test.go:52` `runtimeConfig`：TTL 3/HTTP 1/MaxLag 4/retention 3600）。
3. **V4/V7 行数前置断言 vs 硬编码 8/4/13 seed — 作用域分离，无冲突。** V4 的 `COUNT(*)==2` 是 G2c **自己 seed** 的 2 行（一次 `HardDeleteObjectWithEvent` = deleted@1.1 + notify@1.1 各 1），与 PG `tenant.status` 12 行 seed 无交集；V7 自检作用于**新独立** `seedDeleteGovernanceFacts`（A14），既有 `TestPostgresAuditGovernanceConcurrentClaimsAndLeaseRecovery` 的 12→8/4/13 算术**零改动**。规则：新增 seed 必带行数自检；既有 seed 计数不因本套件改变。
4. **$1 占位符修正 vs G1b DSN-read 形状 — 按门禁/方言分界，无冲突。** G1b/G3 是 SQLite 门禁，沿用既有 `?` 形状（`authz_parity_test.go:51/:69` `outboxCountFor`/`outboxPayloadFor`、`admin_files_delete_test.go:31-61`）——modernc 下 `?` 正确；所有需在 PG 执行的新裸 SQL（G2c/G2b 回拨）用 `$N` 单版本（pgx 扩展协议要求 `$N`）；G2a（SQLite）回拨同样 `$N`（模板 `backdateEventOutbox`）——**双引擎单版本，删去任何"各一版"分支**。

**三审一致结论复核：** SQL 审查确认 `runtime.New` 内建 applyDesiredBindings、G4 harness 单点重赋值满足 C2、F6 公式逐字节成立；timing 审查确认 F4/F5/F10 与全部单调等待判别器 CI 确定；mutation 审查确认设计写侧行断言 + bindings-before-claim 顺序经探针 A1/D1 验证成立。三轮无相互推翻项。

---

## 1. API 变更（Change Surface）

### 1.1 生产 API：**零变更**
本套件不触碰：`internal/repository`（生产方法不动）、`internal/events`、`internal/auditgovernance`、`cmd/server`、schema/migration 文件。G3 的 JSON Schema 是**测试夹具**，不是生产校验器（spec §5.1 明确排除生产侧强制）。

### 1.2 测试 harness API：一处增量（唯一代码改动）
`internal/integration/fullserver_test.go`（现 1373 行；增量 ~20 行，机械 500 行门禁只查非测试文件，见 §6）：

```go
// 现有（不动，3 个构造器全部委托共享体 :72）：
func startFullServer(t *testing.T) *httptest.Server                                                                                // :50
func startFullServerWithRelay(t *testing.T, relayOpts *events.EventOutboxRelayOptions) *fullServerHarness                           // :58
func startFullServerWithAuthAndRelay(t *testing.T, relayOpts *events.EventOutboxRelayOptions, authKeys string) *fullServerHarness   // :66
// 变更：共享体 :72 加第 4 参（nil 安全），三个现有构造器传 nil —— 调用点零改动
func startFullServerOpts(t *testing.T, relayOpts *events.EventOutboxRelayOptions,
    authKeys string, auditRuntime *auditgovernance.Runtime) *fullServerHarness
// 新增（G4 用）：
func startFullServerWithAuditGovernance(t *testing.T, relayOpts *events.EventOutboxRelayOptions,
    authKeys string, auditRuntime *auditgovernance.Runtime) *fullServerHarness
```

共享体内装配顺序（镜像 `cmd/server/main.go:76-82` + `:214`）：

```
repo.Migrate(ctx) 之后、service.NewFileService 之前：
  if auditRuntime != nil {
      auditRuntime.Start(ctx)
      t.Cleanup(auditRuntime.Close)          // LIFO：Close 先于 repo.Close
      repo = auditgovernance.WrapRepository(repo, auditRuntime)
  }
  // 此后 events.New(repo,…) / service.NewFileService(store, repo,…) 天然用包装后 repo
  // （等价于生产 :82 bus.WithRepository(wrapped) 的最终状态；harness 中 bus 在 wrap 之后创建，无需再调 WithRepository）
```

`repo` 是共享体局部接口变量，Migrate 后**单点重赋值**即覆盖 svc/bus/notifier/relay/router 全部下游（SQL 审查附带核验确认）。

### 1.3 被测试的既有 API 面（契约锚点，不改签名）
- `repository.AuditGovernanceStore`（`audit_governance_types.go:75-93`）：`InsertEventWithGovernance(ctx, Event, AuditGovernanceFact) (int64, error)`、`EnqueueAuditGovernance → (bool, error)`、`ClaimAuditGovernance(ctx, owner, token, revision uint64, limit int, ttl) []Fact`、`CompleteAuditGovernance(ctx, id, owner, token)`、`RetryAuditGovernance`、`CleanupDeliveredAuditGovernance(ctx, before, limit) (int64, error)`、`ListAuditGovernanceGaps`。
- `repository.EventOutboxStore`（`repository_interface.go:106-111`）：`ClaimEventOutbox`/`CompleteEventOutbox`/`RetryEventOutbox`/`PruneEventOutbox`/`HasEventOutboxFact`/`CountEventOutbox`。
- `events.BuildDeletedFact` / `BuildNotifyFact`（`payload.go:109-135/:137-165`）——G3 双入口的 builder 侧。
- 生产装配参照：`cmd/server/main.go:81`（WrapRepository）、`runtime.New`（`auditgovernance/runtime.go:54`，含 applyDesiredBindings）。

---

## 2. 兼容性约束（Compatibility Constraints）

| # | 约束 | 强制方式 |
|---|------|---------|
| C1 | `startFullServerOpts` 加参必须 **additive**：`*auditgovernance.Runtime` 为 nil 时行为与现状逐字节一致 | 现有 3 个构造器传 nil；G4 装配在 `if auditRuntime != nil` 分支内；`WrapRepository` 本身 nil-safe（`repository.go:15-21`） |
| C2 | 包装顺序硬约束：**wrap 必须先于 `events.New` 与 `service.NewFileService`**，否则 bus/service 持未包装 repo → 治理行永不落库 → G4 测试**空真通过** | 装配注释 + G4 写侧断言（§5 AC-4 检查点 1 先断言 outbox 行存在再谈投递） |
| C3 | 租户必须已绑定：`ClaimAuditGovernance` 谓词 JOIN `audit_governance_bindings` 且 `b.revision=$6`；未绑定租户 claim 恒 0 行 | G1/G2a/G2b 每个测试开头 `ApplyAuditGovernanceBindings(ctx, revision, digest, [{TenantID, State: active}])`（沿用 `audit_governance_test.go:30` 形状）；revision 与 claim 调用参数一致 |
| C4 | **不修改 `schema_test.go` golden 常量**；G3 的 JSON Schema 与 golden 冲突时 schema 让位（schema 是夹具，golden 是已合入事实） | G3 检查点 5 显式断言；本设计登记该优先级 |
| C5 | 不引入断言框架（I6）；`testing` only | review 门禁 |
| C6 | 单文件 ≤500 行（机械门禁查**非测试文件**，Makefile:150 `-not -name '*_test.go'`；测试文件按 review 约定同样遵守）：新增 4 个测试文件均按拆分预案规划（估算见 §6），超 450 行即触发拆分 | 拆分预案见 §6 |
| C7 | Postgres 测试遵循 `pgDSN`/`freshRepo` + 探活失败自动 skip 惯例；不得因 PG 不可用而挂门禁 | `//go:build integration`（G2b/G2c）；门禁内等价覆盖由 G1/G2a（SQLite）兜底；skip 后任何步骤失败为 Fatal（响亮） |
| C8 | 语义红线（spec §3 机制澄清，写进测试注释）：**`event_outbox` 刻意无 tombstone**——禁止为其写"同源二次入队被抑制"测试；restore→re-delete 必须产生新行。dedupe/tombstone 断言只属于审计治理 outbox | 测试注释 + review |
| C9 | SQL 占位符遵守 I1（`s.rebind`）且按方言分界：SQLite 门禁 DSN 直读沿用 `?` 形状；**新裸 SQL（回拨）一律 `$N` 单版本双引擎**（pgx 要求、modernc 原生接受）；时间统一 RFC3339Nano/UnixNano 混合处沿用现有列语义（`*_ns` 为 UnixNano） | G2a/G2b/G2c 回拨模板 = `event_outbox_test.go:693` `backdateEventOutbox`；G1b/G3 模板 = `outboxCountFor`/`outboxPayloadFor` |
| C10 | 现有 3 个 harness 构造器与全部调用点零改动；`TestDeleteResponse_DoesNotBlockOnDelivery` 等既有测试不得因本套件产生行为差异 | additive 参数 + `make check` 全量回归 |
| **C-11**（新） | G4 配置必须过生产校验：`ClaimTTLSeconds:30`（> 2×`HTTPTimeoutSeconds`=10，`config_audit_governance.go:232-237`）、`MaxLagSeconds:60`（> ClaimTTL，`validAuditGovernanceRetry`）；**4s 守卫与 `HTTPTimeoutSeconds:5` 保持不动**（4<5 承重，同步检测方向——守卫不能单独放大） | §5 AC-4 配置表逐字采用；装配处 `runtime.New` 返回 err 即 Fatal（校验失败=测试失败，非 flake） |
| **C-12**（新） | "恰 1 次 POST"改事件计数：主断言 `delivered_at_ns>0 ∧ attempts==1`（DB 状态、单调、抖动免疫——attempts==1 ⟺ 恰一次 claim ⟺ 恰一次成功 POST）；端点 POST 计数降级为诊断输出 | §5 AC-4 恢复投递断言 |
| **C-13**（新） | 阻塞相加 `started` 两相信号（handler 在 `<-release` 前 close，先例 `runtime_test.go:295`）：DELETE 返回后先有界等 `started`（≤15s）再断言 `delivered_at_ns==0`，**再**释放门闩；释放门闩的 `t.Cleanup` 注册在 `t.Cleanup(auditRuntime.Close)` **之后**（LIFO → 先释放，否则测试失败时 Close 阻塞 `claimTTL+httpTimeout`=35s）；测试 server **只 gate POST 路径**，`/token` 正常应答（Publisher 每次 POST 前取 token） | §5 AC-4 挂起端点实现注释 |
| **C-14**（新） | 租约过期一律**回拨**：裸 SQL `UPDATE audit_governance_outbox/event_outbox SET lease_expires_at_ns=0 WHERE id=$1`（模板 `event_outbox_test.go:693`，`$N` 单版本；G2a 黑盒在已知 DSN 上 `sql.Open("sqlite", …)`，驱动已由 `repository/sqlite.go:11` 注册）；负断言（未到期不得重领）打在**长 TTL**（`time.Minute`）claim 上；**不**复制 PG 既有"150ms TTL + 立即负断言"形状（调度停摆 >150ms 即翻转） | §5 AC-2 G2a/G2b/G2c |
| **C-15**（新） | G2a 拆两条事实：A=fencing（minute TTL claim→负断言→complete）；B=崩溃恢复（长 TTL claim→回拨租约→`"recovery"` 重领 attempts=2→stale complete 拒绝→complete）。minute-TTL claim 持有租约 60s，同一行上"崩溃恢复重领"恒 0 行——原"150ms TTL 恢复同 id"是确定性死路，设计文本删除该表述 | §5 AC-2 G2a 表 |

---

## 3. 失败模式（Failure Modes & 缓解）

| # | 失败模式 | 场景 | 缓解（测试内建） |
|---|---------|------|-----------------|
| F1 | **空真通过**（vacuous pass）：G4 装配错序/未绑定 → 治理行从不产生 → "投递成功"断言恒真 | wrap 晚于 events.New；租户未绑定 | 写侧前置断言：DELETE 后先查 `audit_governance_outbox` 恰 1 行且 `delivered_at_ns=0`，再进入投递阶段；`runtime.Bound(tenant)` 可作辅助（仅查内存 config.Bindings，**不是** DB 护栏——真正护栏是写侧行断言） |
| F2 | 4s<5s 判别器在 CI 负载下抖动 | DELETE 被慢启动拖过 4s | 判别器是**信号式**：POST 阻塞用 chan 门闩（非 sleep）；两相门闩（C-13）把窗口起点钉在 `started` 之后，判别器必真；4s 为宽裕上界；断言 POST 仍在阻塞（chan 状态）时 DELETE 已 204。**若 CI 真 flake：成对放大 `HTTPTimeoutSeconds→10` 与守卫→8**（保持守卫<超时、租约≫守卫），绝不单独放大守卫 |
| F3 | 租约过期恢复测试的时间竞态 | 时钟漂移/调度停摆 | **回拨 `lease_expires_at_ns=0` 强制过期**（C-14，零墙钟、无条件到期）；重领成功用轮询+deadline（≤2s，单调只超时不翻转）；负断言打长 TTL claim（调度停摆不翻转）；`attempts==2` 断言在重领行上做 |
| F4 | `RetryEventOutbox`/退避门：`available_at_ns` 在未来 → 不可领 → 测试挂死 | 用真实 backoff 等待 | **回拨时间**而非 sleep：`UPDATE event_outbox SET available_at_ns=0 WHERE id=$1`（**`$N` 单版本双引擎**，模板 `backdateEventOutbox`，A13）后重领；终态 `failed` 是纯行状态（`attempts >= maxAttempts`，event_outbox.go:364-391），零墙钟 |
| F5 | G2c/G2b SKIP LOCKED 并发断言偶发（两连接竞速天然一快一慢） | 双连接并发 claim | 确定性形状：先 `lockGovernanceRows`（`FOR UPDATE`）占 4 行 → 另一连接只领剩余行；**锁事务跨双 claimer 全程打开**（rollback 在 claim+complete 断言**之后**）；claim LIMIT ≥ 剩余行数（锁序与 claim 序可能选出不同"前 4"，计数断言仍稳健）；`assertDistinctGovernanceClaims` 式断言**计数+不相交**，不断言行身份 |
| F6 | G1 HMAC 期望值重建与 `redactor.digest` 漂移 | redaction 公式变更 | 测试内 `testDigest(t, key, tenant, field, value)` 复刻 `writeMACFields`（`redaction.go:29-45`）字节公式：`hmac-sha256:` + RawURLEncoding(HMAC-SHA256(key, "aero-vault/audit-governance/v1" + \x00 + tenant + \x00 + field + \x00 + value + \x00))；注释锚定 `redaction.go`。**驱动机制（A2/V2）：digest 断言必须经 redactor 路径（`WrapRepository`+`InsertEvent` 或 reconcile 路径）**——黑盒 store 接口+手造 fact 是同义反复，杀不死 MUT-B/MUT-D（探针实测） |
| F7 | G2b/G2c 因 PG 探活失败静默 skip → 回归漏网 | 本地无 PG | skip 仅发生在 Ping 失败（post-probe 失败是 Fatal，`-v` 可见）；**编译门（A18，已落地 §7.1）：`go vet -tags=integration ./...`**（build 不编译 `_test.go`，vet 会类型检查测试文件）——现为 `make check` 的 `vet-integration` 步 + CI Vet 步（零 Docker，实测 rc=0），G2b/G2c 编译错误对门禁必然可见；**执行门**：本地 `make test-integration`（有 Docker）+ CI opt-in `integration-pg.yml`（§7.2，零 skip 断言——skip 行即 job 失败，杜绝全 skip 绿票） |
| F8 | G3 负例误伤合法 payload（`backend` 允许空、`reason` 可选） | schema 过严 | schema 定义照 `deletedFact`（`payload.go:33-49`）实字段：`backend` enum 含空串、`reason` 不在 required；golden 兼容优先（C4） |
| F9 | 审计治理端点 401/令牌失效路径超时预算 | G4 否定面子场景 | 标记为**观察项**（warn 后 continue），不阻塞 AC-4 通过（spec §3 AC-4 检查点 5）；"401 不阻塞删除"本身是信号断言，无时钟 |
| F10 | 清理保留窗口断言错位（`CleanupDeliveredAuditGovernance(now-1h)` 计 0 依赖行 delivered_at 是"现在"） | 时间语义 | 检查点固定：complete 之后捕获**一个** `now`，`now.Add(-1h)` 与 `now.Add(1h)` **两调用共用**（谓词 `delivered_at_ns>0 AND <=$1` 不碰未投递行）；G2c prune 回拨值取窗内深值（`now-2d` 对 `now-24h`、`now-30d` 对 `now-7d`） |

---

## 4. 迁移步骤（Migration Steps）

**数据库迁移：零。** 不新增 `.up/.down` 文件（I2 双文件规则不触发——无 schema 变更）。0039/0040/0041/0042 已含全部所需表/约束。

代码侧落地顺序（每步独立可合入、可回滚）：

1. **Step 1（G1/G2a）**：新建 `internal/repository/audit_governance_delete_test.go`（`package repository_test`，黑盒，沿 `audit_governance_test.go` 形状）。含 A1（dedupe 前置）、A2（wrapper digest 子测试——黑盒包 import `internal/auditgovernance` 无环）、A11/A12（回拨 + 双事实）。无生产改动。
2. **Step 2（G3/G1b）**：新建 `internal/integration/event_schema_conformance_test.go`（门禁）。内含：JSON Schema 常量（draft-07 风格字符串）+ 手写 `validateAgainstSchema(t, payload, schema)`（~50 行：required 存在性、类型、enum、integer 用 `math.Trunc(v)==v`、`additionalProperties:false`——**不引第三方库**，I6）+ A3 行数前置。无生产改动。
3. **Step 3（G2c）**：新建 `internal/integration/event_outbox_postgres_test.go`（`//go:build integration`）。沿用 `freshRepo`（`postgres_integration_test.go:36`）+ DSN 直读断言；含 A4（COUNT==2 前置）、A11/A13（`$N` 回拨）、A17（`err != nil`）。回拨 SQL 为 `$N` 单版本。
4. **Step 4（G2b）**：扩展 `internal/integration/audit_governance_postgres_test.go`——**新建独立 seed 函数 `seedDeleteGovernanceFacts`**（`InsertEventWithGovernance(Event{Type: EventDeleted,…})` 变体）+ **独立测试**（A14）；**既有 `seedGovernanceFacts` 12 行 seed 与 `TestPostgresAuditGovernanceConcurrentClaimsAndLeaseRecovery` 的 8/4/13 计数零改动**；seed 后先自检 delete 变体行数（A7）；复用 `lockGovernanceRows`/双 repo 形状（锁事务跨双 claimer 打开，F5 条件）。
5. **Step 5（G4）**：`fullserver_test.go` 共享体加第 4 参（§1.2）+ 新建 `internal/integration/audit_governance_composition_test.go`（A5/A6/A8/A9/A10）。
6. **Step 6**：全量回归 `make check`（现含 `vet-integration` 编译门，A18/§7.1）+ `make test-integration`（有 Docker 时）+ `make test-race`（`internal/repository`/`internal/integration` 两包）；有 CI 时跑 opt-in `integration-pg.yml`（§7.2，零 skip 断言）。

回滚策略：每步均为纯新增文件/参数，删除即回滚；无数据面影响。

---

## 5. 可测试验收映射（Acceptance Mapping）

### AC-1 → G1 + G1b（门禁）

**G1 — `internal/repository/audit_governance_delete_test.go`（黑盒，`go test ./internal/repository/ -run AuditGovernance`）：**

| 断言 | 通过标准 |
|------|---------|
| 写路径 | `InsertEventWithGovernance(ctx, Event{TenantID:"default", Bucket, Key, Type: EventDeleted, RequestID, Payload:{"size":"123","backend":"local"}}, fact)` 后：治理 outbox 恰 1 行；`action="file.deleted"`、`fact_kind="file"`、`origin_kind="file"`、`origin_id==` 返回值；`tenant_id=="default"`；`actor_digest/target_digest/request_id` 与 `testDigest` 期望一致（`target` = `bucket+"\x00"+key`，`field` = `"file-target"`）；`object_size_bytes==123`；`storage_backend=="local"`；`occurred_at_ns>0`；`attempts=0`；`delivered_at_ns=0` |
| **wrapper 路径 digest（A2/V2，必须）** | `auditgovernance.New(最小合法 cfg, store, logger)`（cfg 模板 `runtime_test.go:52`，构造安全零网络）→ `WrapRepository` → `wrapped.InsertEvent(Event{…deleted…}, nil)` → 读回治理行 `actor_digest/target_digest/request_id == testDigest`（F6 公式）→ 行数恰 1。注释锚定 `redaction.go`——**本子测试是 AC-1"redacted 摘要"契约唯一非空真落点**（MUT-B/MUT-D 下必 FAIL） |
| dedupe（未投递）（A1/V1，必须） | **前置：首次 `EnqueueAuditGovernance` 必须 `(true,nil)` 且行数仍为 1**；再 enqueue → `(false,nil)`（缺失绑定下 `(false,nil)` 假真通过，探针 A2a 实测；`governanceCaptureActive==false` 短路返回 `(false,nil)`） |
| tombstone（投递+清理后） | claim→complete→`CleanupDeliveredAuditGovernance(now+1h,1)` 计 1 → 再 enqueue → `(false,nil)`；`ListAuditGovernanceGaps` 无此起源 |
| 事件行侧 | `object_events` 恰 1 行、`type="deleted"`、`request_id/payload` 原样（SQLite 门禁内安全；PG 侧禁字节断言，见 G2b） |
| 负数用例（锁定语义） | `RecordAuditWithGovernance` 对 `Action:"file.delete"` 的行 `origin_kind=="admin"`（write.go:30 强制覆盖） |
| **前置** | 每测试先 `ApplyAuditGovernanceBindings(ctx, rev, "digest", [{tenant, active}])`（C3）；revision 与 claim 一致 |

**G1b — `internal/integration/event_schema_conformance_test.go`（门禁）：**

| 断言 | 通过标准 |
|------|---------|
| **行数前置（A3/V3，必须）** | 真实 DELETE 后、字段校验**之前**：`event_type='vault.file.deleted@1.1'` 恰 1 行 **且** `notify@1.1` 恰 1 行（复用 `outboxCountFor` 形状 + `EventTypeFileDeleted11`/`EventTypeFileNotify11` 常量），`origin_id==` 删除对象 id——0 行时读回循环空真（探针 D2 实测） |
| 12 必填字段 | DSN 读 `event_outbox.payload`，12 必填字段逐字段存在且类型正确（非子串匹配；`?` 占位符形状，C9） |

### AC-2 → G2a + G2b + G2c

**G2a（SQLite 门禁，黑盒，同 G1 文件）：**

| 断言 | 通过标准 |
|------|---------|
| **事实 A = fencing（C-15）** | `ClaimAuditGovernance("worker-a","token-a",rev,1,time.Minute)` 恰 1 行（`attempts=1`、owner/token/lease 落库）→ 二次 claim 0 行（**负断言打在长 TTL claim 上**，C-14——调度停摆不翻转）→ complete |
| **事实 B = 崩溃恢复（C-15）** | 新事实 claim（长 TTL）→ **回拨 `lease_expires_at_ns=0`**（`$N`，G2a 黑盒在已知 DSN 上 `sql.Open("sqlite", …)`，驱动已注册）→ `"recovery"` 重领同 id、`attempts=2` → stale `CompleteAuditGovernance(id,"crashed","stale")` 报错（身份栅栏 claim.go:113，无时序）→ complete |
| cleanup 保留窗口（F10） | complete 后捕获**单一 `now`**：`CleanupDeliveredAuditGovernance(now.Add(-1h),1)` 计 0 / `(now.Add(1h),1)` 计 1 |
| gaps | `ListAuditGovernanceGaps` 无 gap |

**G2b（`-tags=integration`，扩展 `audit_governance_postgres_test.go`）：**

| 断言 | 通过标准 |
|------|---------|
| **独立 seed + 自检（A14/A7）** | 新增 `seedDeleteGovernanceFacts`（delete 变体：`Action:"file.deleted"`、`OriginKind:"file"`），**不修改既有 12 行 `tenant.status` seed**；seed 后先断言 delete 变体行数恰 N（自检，建议项 A7）——既有测试 8/4/13 计数零改动 |
| SKIP LOCKED 形状（F5 三条件） | `lockGovernanceRows` 锁 4 行（**锁事务跨双 claimer 全程打开**，rollback 在 claim+complete 断言之后）→ 第二连接 `SKIP LOCKED` 只领剩余（claim LIMIT ≥ 剩余行数）；`assertDistinctGovernanceClaims` 计数+不相交（不断言行身份） |
| delete 行参与 claim | 断言 delete 起源行被领到（claim 谓词不按 action 过滤） |
| 租约过期恢复 | 回拨 `lease_expires_at_ns=0`（`$N`）→ 重领 `Attempts==2`；stale complete 拒绝（身份栅栏，无时序） |
| cleanup/tombstone | cleanup 计数精确；tombstone 抑制再入队 |
| **禁止（A15/S3）** | 不对 `object_events.payload` 做字节断言（PG `$7::jsonb` 规范化，字节断言必挂）；不复制 PG 既有 150ms+立即负断言形状（C-14） |

**G2c（`-tags=integration`，新文件 `event_outbox_postgres_test.go`）：**

| 断言 | 通过标准 |
|------|---------|
| **写 2 行前置（A4/V4，必须）** | 并发 claim **之前**：`COUNT(*)==2`（按 event_type 各 1——0 行时并发 claim 平凡不相交，探针 D3 实测） |
| 并发 claim | 双连接并发 `ClaimEventOutbox` 行不相交（集合断言；**禁止**改"每连接恰 1 行"——单连接合法拿 2 行） |
| complete | `CompleteEventOutbox` 后 `event_outbox_delivered` 保真行同事务写入、delivered 行不再被领 |
| 租约恢复 | 回拨 `lease_expires_at_ns=0` → 第二 claimer 重领 `attempts=2`；stale complete → **`err != nil`**（`errEventOutboxClaimLost` 未导出，黑盒不能 `errors.Is`，A17/S5） |
| retry 退避门（F4） | 回拨 `available_at_ns=0`（`$1` 单版本，A13）→ 重领；`attempts>=maxAttempts` → `failed` 不可再领（纯行状态，零墙钟） |
| prune（F10/T6） | `PruneEventOutbox(now-24h, now-7d)` 计 0；回拨 `delivered_at_ns` 取窗内深值（`now-2d`/`now-30d`）后计 2；`event_outbox_delivered` 同步清空 |

### AC-3 → G3（门禁）

`event_schema_conformance_test.go`：内嵌 draft-07 风格 JSON Schema（required = `schema_version`(enum["1.1"])、`event_type`(enum["vault.file.deleted@1.1"])、`tenant`、`bucket`、`key`、`object_id`(integer,>0)、`version_id`、`size`(integer,>=0)、`etag`、`backend`(enum["local","s3","oss","cos",""])、`request_id`、`actor`；optional `reason`；`additionalProperties:false`）。

| 断言 | 通过标准 |
|------|---------|
| 校验器实现（A16/S4） | integer 判定用 `math.Trunc(v)==v`（JSON 数字解码为 float64，非 `reflect` 类型断言）；~50 行手写，不引库（I6） |
| 正例（双入口） | `BuildDeletedFact` 输出 + DELETE 后 DB 读回 payload 过检（**读回入口前先过 G1b 行数前置 A3**） |
| 自包含 | `version_id/size/etag/backend` 与删除前 `h.repo.GetObject` 一致（投递期不重派生） |
| notify 例 | `notify@1.1` `records[0].s3.object` 全字段 + `sequencer` 匹配 `^[0-9a-f]{32}$` |
| 负例 | 缺 `key`/缺 `object_id`/`object_id` 非整/`schema_version:"2.0"`/`event_type` 错值/出现 `share_id|chunk_ids|project_id|usage` 任一键 → 拒绝 |
| 兼容 | 不改 golden 常量（C4） |

### AC-4 → G4（门禁）

`internal/integration/audit_governance_composition_test.go` + harness 增量（§1.2）。

**配置（C-11 修正后，逐字采用——全部通过 `cfg.Validate()`）：**

```go
config.AuditGovernanceConfig{Enabled:true, BaseURL:<ts.URL>, TokenURL:<ts.URL>/token,
    HMACKey:32B, HTTPTimeoutSeconds:5, PollMilliseconds:10, BatchSize:10,
    ClaimTTLSeconds:30, // 原 3 —— 恢复生产校验 ClaimTTL > 2×HTTPTimeout（30 > 10）
    InitialBackoffSeconds:1, MaxBackoffSeconds:2, MaxLagSeconds:60, // 原 4 —— 需 > ClaimTTL
    ReconcileBatchSize:20, DeliveredRetentionSeconds:3600, CleanupIntervalSeconds:60,
    CleanupBatchSize:20, Revision:1, Bindings:[{TenantID:<tenant>, ClientID, ClientSecret}]}
```

**序链（承重关系）：** 阻塞窗口 ≤4s < HTTPTimeout 5s < ClaimTTL 30s < MaxLag 60s——单 claim ⇒ `attempts==1` ⇒ 恰 1 POST ⇒ complete 必成功 ⇒ delivered，全确定性。

| 子场景 | 断言 |
|--------|------|
| 装配 | `runtime.New(cfg, repo.(repository.AuditGovernanceStore), logger)`（err 即 Fatal——配置被校验拒绝=测试失败，非 flake）→ `startFullServerWithAuditGovernance`；`runtime.Bound(tenant)` 辅助（仅内存配置，非 DB 护栏——真正护栏是下一行） |
| 挂起端点（A6/A10，两相门闩） | handler 先 `close(started)` 再 `<-release`；**测试等 `started`（≤15s）后才发 DELETE**（窗口起点钉定，判别器必真）；REST DELETE → 204 且 ≤4s；断言治理行 `delivered_at_ns=0`（**在释放门闩之前**，pending/inflight 合法）；`/token` 不 gate（正常应答） |
| 恢复投递（A9，事件计数） | 释放门闩（回执 `{receipt:{event_id:回显, tenant_id, status:"ledgered", accepted_at}}`）→ 轮询 ≤15s **写侧断言记录的 `fact.ID`**：`delivered_at_ns>0 ∧ attempts==1`（主断言，单调抖动免疫）；端点 POST 计数按 **`event_id`** 计（诊断，恰 1） |
| wire（A5，必须） | `aggregate_id == testDigest(tenant,"file-target",bucket+"\x00"+key)`、`operation_id == testDigest(tenant,"request",requestID)`（`http.go:149-151` 映射：OperationID←fact.RequestID、AggregateID←fact.TargetDigest）；POST body **不含**原始 bucket/key/actor 子串 |
| 宕机端点 | 端点 5xx → DELETE 仍 204；行退避重试（`attempts>=2` 单调可观测）→ 端点恢复 202+回执 → 下一 claim 周期投递 |
| 延迟不受影响 | DELETE 响应与基线同量级（信号式判别器，不做墙钟微基准——spec §5.5） |
| 否定面（观察项） | 端点 401 → 不阻塞删除；超时预算则登记观察项，不阻塞 AC-4（F9） |

### 验收映射总表

| 验收 | 交付物 | 文件 | 门禁 | 复现 |
|------|--------|------|------|------|
| AC-1 | G1 + G1b | `audit_governance_delete_test.go` + `event_schema_conformance_test.go` | SQLite | `go test ./internal/repository/ -run AuditGovernance` |
| AC-2 | G2a/G2b/G2c | 上述 + `audit_governance_postgres_test.go`(扩展) + `event_outbox_postgres_test.go`(新) | SQLite + `-tags=integration`（编译门 `vet-integration`，零 Docker，A18/§7.1） | `make test-integration`（本地）/ CI `integration-pg.yml`（§7.2） |
| AC-3 | G3 | `event_schema_conformance_test.go` | SQLite | `go test ./internal/integration/ -run EventSchema` |
| AC-4 | G4 | `audit_governance_composition_test.go` + `fullserver_test.go` 增量 | SQLite | `go test ./internal/integration/ -run AuditGovernanceComposition` |

---

## 6. 落地检查单（含门禁提示，三审全部新增项已并入）

- [ ] 生产代码零 diff（唯一改动：`fullserver_test.go` 共享体加第 4 参 + 新构造器，~20 行）
- [ ] 零新迁移文件、零新 go.mod 依赖（G3 手写校验器）
- [ ] 新增测试文件 ≤500 行（估算：`audit_governance_delete_test.go` ~295 / `event_schema_conformance_test.go` ~255 / `event_outbox_postgres_test.go` ~220 / `audit_governance_composition_test.go` ~290 / `audit_governance_postgres_test.go` 扩展 +~120→311）。**超 450 行拆分触发**：delete_test → `…_write_test.go`/`…_claim_test.go`（G1/G2a 主题分界）；schema → `event_schema_fixture_test.go`（常量+校验器）；composition → `…_wire_test.go`（wire 断言）。（机械 500 行门禁只查非测试文件，Makefile:150——测试文件按 review 约定执行）
- [ ] 函数 ≤50 行、`gofmt`、`go vet ./...`
- [ ] `make check` 全绿（门禁等价：gofmt/build/vet/test/cli-check）
- [ ] **编译门（A18/§7.1，已落地）：** `make check` 含 `vet-integration` 步（`go vet -tags=integration ./...`——build 不编译 `_test.go`，vet 会类型检查全部 tagged 测试文件；零 Docker 实测 rc=0）；CI `ci.yml` Vet 步已同步升级
- [ ] **执行门（A19/§7.2）：** 本地 `make test-integration`（Docker）；CI opt-in `integration-pg.yml`（`workflow_dispatch`，零 skip 断言——`--- SKIP` 行 → `::error::` + exit 1）
- [ ] **命名约定（§7.2 依赖）：** G2b/G2c 新测试一律 `TestPostgres*`/`TestPg*` 前缀（沿用既有惯例），否则 opt-in PG job 的 `-run 'TestPostgres|TestPg'` 过滤静默不跑
- [ ] `make test-race`（repository + integration 两包——三审全部翻转点在 `-race` 下概率最高）
- [ ] C-11…C-15 内联确认：G4 配置逐字采用（30/60）、事件计数主断言、两相门闩 + LIFO cleanup、回拨 `$N`、G2a 双事实
- [ ] 禁止项复查：无 `event_outbox` 二次入队抑制测试（C8）；未改 `schema_test.go` golden（C4）；未引断言框架（C5）；G2b 未碰既有 8/4/13 计数（A14）；G2b 无 `object_events.payload` 字节断言（A15）；无"150ms TTL"表述残留（C-15）；无"PG/SQLite 各一版"回拨分支残留（A13）

---

## 7. CI 集成（零 Docker 编译门 + opt-in PG 任务）

### 7.1 零 Docker 编译门（必需 gate，已入 `make check`）

- **命令：** `go vet -tags=integration ./...`。落地两处：Makefile 新增 `vet-integration` 步（`check: fmt vet vet-integration build test cli-check`）；CI 必需 job 的 `Vet` 步同步升级为该命令（tag 只增文件，覆盖是 `go vet ./...` 的超集）。
- **为何是 vet 不是 build（实测证据，本设计核验时注入损坏的 `//go:build integration` 测试文件）：**

  | 命令 | 损坏 tagged 测试文件存在时 |
  |------|--------------------------|
  | `go build -tags=integration ./...` | rc=0 —— `go build` 不编译任何 `_test.go`，G2b/G2c 编译错误完全不可见 |
  | `go vet -tags=integration ./...` | rc=1 —— 捕获编译错误（vet 编译测试文件） |

- **为何可进 `make check`：** 纯编译/静态分析，零 Docker、零网络、零 DB 连接；tagged 文件引用的 pgx 等依赖已在 go.mod —— 满足 AGENTS 硬门禁（SQLite+local FS，零网络/零 Docker）。
- **作用域：** `internal/integration` 全包（含 G2b 扩展文件与 G2c 新文件）的**编译期**回归。探活 skip 的**运行期**回归由 7.2 兜底。

### 7.2 opt-in PG 服务任务（`workflow_dispatch` 手动触发，不进必需 gate）

新增 `.github/workflows/integration-pg.yml`，仅 `workflow_dispatch` 触发（push/PR 永不自动跑，不参与"失败即拒绝合入"的必需 gate）：

| 项 | 取值 |
|----|------|
| 触发 | `workflow_dispatch`（opt-in，手动） |
| 服务容器 | `pgvector/pgvector:pg16`（与本地 `make test-integration` 镜像一致）；`POSTGRES_USER/PASSWORD/DB=aero`；health `pg_isready -U aero -d aero`；端口 5432 |
| 环境 | `AERO_PG_DSN=postgres://aero:aero@localhost:5432/aero?sslmode=disable` |
| 步骤 1 | 编译门 `go vet -tags=integration ./...`（与 7.1 同命令，双保险） |
| 步骤 2 | `go test -tags=integration ./internal/integration/ -v -count=1 -run 'TestPostgres\|TestPg'`（`-v` 使探活 skip 以 `--- SKIP` 行可见） |
| 步骤 3 | **零 skip 断言**：日志含任何 `--- SKIP` 行 → `::error::` + exit 1。探活失败在本地是"日志响亮、退出码 0"（实测：坏 DSN 下 `--- SKIP: TestPostgresMigrationsApply (0.00s)` + rc=0），CI 任务必须把 skip 转为失败，杜绝"DSN 配错 → 全 skip 绿票" |
| 作用域 | G2b/G2c 的 PG SQL（0041 双方言表、claim 谓词、退避门）与 `FOR UPDATE … SKIP LOCKED` 路径（`lockGovernanceRows` 占 4 行 → 第二连接只领剩余）**真实执行**；服务起不来/DB 连不上 → job 失败（loud），绝不静默通过 |

**命名约定（步骤 2 的 `-run` 过滤依赖）：** G2b 新测试与 G2c 新文件内测试一律 `TestPostgres*`/`TestPg*` 前缀（沿用 `postgres_integration_test.go`/`audit_governance_postgres_test.go` 既有惯例），否则 opt-in 任务静默不跑——列入 §6 检查单。

**skip 可见性（本地复现）：** `AERO_PG_DSN=<坏DSN> go test -tags=integration ./internal/integration/ -v -run 'TestPostgres|TestPg'` 输出 `--- SKIP: … (no Postgres at …)` 且 rc=0；`make test-integration` 自带 Docker PG——本地有 Docker 即真跑，无 Docker 时 `docker run` 先行失败（shell rc≠0，同样 loud）。
