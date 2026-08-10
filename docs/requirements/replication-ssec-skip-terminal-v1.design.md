# Design：SSE-C 复制任务 —— 归类为终态跳过（replication-ssec-skip-terminal-v1）

> **模块：** `internal/replication`（唯一生产改动文件）
> **上游规格：** `docs/requirements/replication-ssec-skip-terminal-v1.spec.md`（FR-1…FR-6 为约束输入，本文不扩张）
> **HEAD：** `acfaaf4` · **门禁：** `make check`（gofmt/build/vet/test、单文件 ≤ 500 行）

---

## 0. 结论摘要

| 项 | 结论 |
|---|---|
| 生产代码改动 | **仅 `internal/replication/replication.go` 一个文件**：SSE-C 分支（:114-117）由硬错误改为终态跳过；抽出共享 `recordStatus` helper（replicated 路径 :133-147 纯抽取，行为逐字不变） |
| API / 配置 / 迁移 / 依赖 | **零新增**（FR-6；I2/I6 不触碰） |
| 经验证据 | 探针测试 `TestReplicateSSECSkip`（AC-1+AC-2 完整形态）已在当前 HEAD 实证 **红**（`replication: SSE-C object requires an unavailable customer key`）；临时应用本设计后同探针 **绿**（0.97s，直接+Pool 两段），全量 `go test ./internal/replication` 绿、`go vet` 干净、`gofmt` 干净。探针已删除、`replication.go` 已恢复，`git diff internal/replication/` 为空 |
| 兼容性 | 非 SSE-C 路径行为逐位不变（抽取被 `TestReplicateObject` / `TestReplicateManagedEncryption` / `TestReplicationStatusTagsExactVersion` 锁定，实证全绿）；JobPool 语义零改动（FR-3） |

---

## 1. 证据复核（不信任引证，逐条对照树）

规格 §1 的 5 条引证全部复核通过（本设计另加 4 条新核验）：

| # | 引证 | 复核 |
|---|---|---|
| E1/E2 | `replication.go:114-117` SSE-C 硬错误，先于 tag 写回（:133-147） | ✅ 精确；错误返回在 `primary.Get` 之前，即跳过路径天然满足"不触碰存储" |
| E3 | `jobs.go:206-212` 无条件重试→`FailJob`；`MaxAttempts` 默认 5（`repository/jobs.go:48-49` `EnqueueJob` 内 `if j.MaxAttempts <= 0 { j.MaxAttempts = 5 }`） | ✅ 精确 |
| E4 | `workers.go:46-52` handler 原样回传错误 | ✅ 精确；`access.SystemContext(ctx, job.TenantID)` 包装不影响错误流 |
| E5 | `TestPoolFailsAfterMaxAttempts`（`jobs_test.go:147-168`）锁定重试→失败语义 | ✅ 精确；本设计不改该语义 |

**新增核验（本设计独有）：**

- **SSE-S3/KMS 与 SSE-C 元数据键互斥**：`ServerSideEncryptionInfo` 读 `_aero_sse_algorithm`/`_aero_sse_kms_key_id`（`server_side_encryption.go:65-75`），`SSECustomerInfo` 读 `_aero_sse_c_algorithm`/`_aero_sse_c_key_md5`（`ssec.go:91-99`）——两套键不相交 ⇒ 跳过分支**不可能**误伤托管加密对象；写侧亦有校验拒绝两者同时携带（`ssec.go:44-50`）。
- **`storage.ErrNotFound = errors.New("object not found")`**（`storage/storage.go:14`）——replica 未触碰断言用 `err != nil` 即可。
- **`repl_status` 取值无枚举契约**：仓库与文档均不枚举（规格 §1 已验证），新增 `skipped` 无冲突。
- **`TestReplicateSSECSkip` 当前真空**：`go test ./internal/replication -run TestReplicateSSECSkip` → `[no tests to run]`（实证）。新测试命名使过滤器非空（实证：探针被匹配）。

---

## 2. 代码设计（`internal/replication/replication.go`，唯一生产改动）

### 2.1 新 helper：`recordStatus`（~22 行，纯抽取）

replicated 路径的 tag 快照+合并+写回（现 :133-147）抽为共享方法，**逻辑逐字不变**：

```go
// recordStatus writes repl_status=<status> onto the exact object version via
// the tagger. Tagger failures are never fatal: the underlying replication
// state has already converged (object copied or deliberately skipped), so a
// tag write failure must not fail the job (it would re-enter the retry chain).
func (w *Worker) recordStatus(ctx context.Context, objectID int64, obj repository.Object, status string) {
	tags := make(map[string]string, len(obj.Tags)+1)
	for k, v := range obj.Tags {
		tags[k] = v
	}
	tags[TagStatus] = status
	if w.tagger == nil {
		w.logger.Warn("replication: object tagger unavailable", "object_id", objectID)
		return
	}
	if err := w.tagger.SetObjectTagsByID(ctx, objectID, tags); err != nil {
		w.logger.Warn("replication: tag update", "object_id", objectID, "err", err)
	}
}
```

replicated 路径改为一行 `w.recordStatus(ctx, objectID, obj, "replicated")`。

### 2.2 SSE-C 分支（:114-117）替换为终态跳过

```go
	if _, _, ok := service.SSECustomerInfo(obj.Metadata); ok {
		// SSE-C customer keys are never persisted (SSECustomerInfo exposes only
		// algorithm/keyMD5), so replication can never carry the key: classify
		// as a terminal skip instead of a retryable failure.
		w.recordStatus(ctx, objectID, obj, "skipped")
		w.logger.Info("replication: skipped", "tenant", obj.TenantID, "key", obj.Key, "object_id", objectID, "reason", "SSE-C")
		return nil
	}
```

**结构保证（FR-1/FR-2）：**

- 判定点保持在 `GetObjectByID` 之后、`primary.Get` **之前**（原位置不动）⇒ 跳过路径不执行 primary `Get`、不执行 replica `Put`、不触碰任何存储。
- `recordStatus` 返回 `void` ⇒ **tag 写失败在结构上不可能失败 job**（FR-2 锁死：错误无处可传）。
- `return nil` ⇒ Pool `runOne` 走 `CompleteJob` → `status='succeeded'`（`jobs.go:198-203`），复用 `IncJobCompleted`（FR-4 可观测性，零新遥测）。
- `errors` import 仍被 `DecodeObjectID` 使用，**import 无变化**。

### 2.3 尺寸核算

| 文件 | 现状 | 改动后 | 门禁 500 |
|---|---|---|---|
| `replication.go` | 149 | ≈165（+24 helper，+9 跳过块，−14 内联 tag 块，−4 错误块） | ✅ |
| `replication_test.go` | 217 | ≈350（+2 测试 + 本地 fastPool/waitFor 克隆 ≈ 130） | ✅ |

`recordStatus` ≈22 行、`ReplicateObjectByID` 修复后 ≈41 行，均 ≤ 50 行函数约定。

---

## 3. API 变更与兼容性约束

| 面 | 状态 | 说明 |
|---|---|---|
| 与未来 resync 方向（方向 2）的交叉 | **登记约束，不改代码** | `repl_status=skipped` 为终态：方向 2 实施时 sweep 跳过条件须包含 `skipped`（§8.2），否则产生 interval 周期 succeeded 空转 |
| 公开 API（`replication` 包） | **零变更** | `JobReplicate`、`TagStatus`、`Worker`、`NewWorker`、`WithObjectTagger`、`ReplicateObjectByID`、`Encode/DecodeObjectID` 签名与导出面不动；无新导出符号 |
| REST / S3 / MCP / CLI / SDK / OpenAPI | **零变更** | 无新端点、无新参数；`openapi.json` 不漂移 |
| 配置 / 环境变量 | **零变更** | 无新 `REPLICATION_*`；`docs/configuration.md` 不动 |
| 数据库 | **零变更** | 无迁移（I2）；jobs 表、objects 表结构不动；`repl_status` 为对象 tag（map），新取值 `skipped` 无 schema 面 |
| 语义兼容 | **非 SSE-C 路径逐位不变** | 抽取+替换被 4 个既有测试锁定（§6 回归矩阵）；`make check` 实证 |
| 版本化对象 | **不变** | `SetObjectTagsByID` 按 objectID 精确写（`object_worker.go:15-33`），跳过 tag 只落在该版本；新版本重传（无 SSE-C）→ 新 `created` 事件 → 正常复制，旧版本 `skipped` tag 不泄漏（`TestReplicationStatusTagsExactVersion` 模式） |
| 幂等性 | **不变且更强** | 跳过确定性（元数据持久、判定纯函数）；job 崩溃重跑 → 再次跳过+重打 tag，幂等 |
| 既有失败行 | **可收敛** | 修复前已 `failed` 的 SSE-C job：手动 `POST /v1/admin/jobs/{id}/retry` 重入 handler 现返回 nil → `succeeded` + 打 `skipped` tag——**运维无需数据迁移即可清洗旧污染**（非自动修复，见 §5） |

---

## 4. 失败模式与降级路径

| # | 场景 | 行为 | 判定 |
|---|---|---|---|
| FM-1 | tagger 为 nil（worker 未 `WithObjectTagger`） | warn log + 返回 nil；job `succeeded`，对象无 tag | 符合 FR-2；skip 不污染 jobs 表 |
| FM-2 | `SetObjectTagsByID` 失败（对象并发被删→`ErrNotFound`、authorize 失败、DB 错误） | warn log（含 object_id、err）+ 返回 nil | 符合 FR-2；**若此处返回错误，跳过错题将以新错误重入重试→失败链，污染照旧——本设计最大回归点，已结构锁死**（helper 无错误返回通道） |
| FM-3 | 对象在入队与执行间被删 | `GetObjectByID` → `ErrNotFound` → 可重试错误 → MaxAttempts 后 `failed` | **保持原语义（真实失败可重试）**，FR-3 |
| FM-4 | payload 解码失败 / primary 不可达 / replica Put 失败 | 可重试错误 → 退避 → `failed` | 不变（FR-3/FR-5） |
| FM-5 | 跳过执行中崩溃 | job `running` → reaper（`reapAfter` 10min）重排 → 重跑 → 确定性再跳过 | 幂等收敛，无新状态 |
| FM-6 | 误判跳过（对象同时带 SSE-C 与托管加密元数据） | **不可能**：两套元数据键互斥 + 写侧校验拒绝并存（§1 新核验） | 无此路径 |
| FM-7 | 部署后运维重试旧 `failed` 行 | handler 返回 nil → job 收敛为 `succeeded`；`repl_status=skipped` 写入 | 兼容升级路径（§3） |

**无新增并发面**：不新增锁/状态/goroutine；Pool、claim、reaper、dedupe（`replicate:<object_id>` DedupeKey）均不触碰。

---

## 5. 迁移与部署步骤

1. **无 schema 迁移**（I2 零触碰）：无 `NNNN_*.{up,down}.sql`。
2. **无配置迁移**：无 `.env.example` 变更。
3. **部署 = 构建 + 重启**：单二进制；旧进程与新进程并存期行为差异仅限 SSE-C 对象（旧：重试→failed；新：skip→succeeded），无数据格式互操作问题。
4. **（可选，运维）旧污染清洗**：对既存 `status='failed' AND type='replicate'` 且 `last_error` 含 "SSE-C" 的行执行 admin retry，逐行收敛为 `succeeded`；或直接删除该类行（jobs 表管理属既有运维面，不在本方向范围）。
5. **回滚**：还原上一二进制即可；无迁移状态需回滚（本方向无任何持久化格式变更）。

---

## 6. 验收映射（规格 AC-1…AC-3 原样 → 可执行测试）

全部落在 `internal/replication/replication_test.go`（包 `replication`，既有基建：`repository.Open`+`Migrate`+`storage.NewLocal`+`service.NewFileService`，对齐 `TestReplicateObject` 种子模式）。两个新测试名均匹配 `-run TestReplicateSSECSkip` 过滤器（实证非空）；本地克隆 `jobs_test.go:34-49` 的 `fastPool`（2ms 轮询）与 `waitFor`（超时 `t.Fatalf`）——`jobs` 包的版本未导出，须在 `replication` 包内复制 ~20 行（对齐先例）。

### AC-1：`go test ./internal/replication -run TestReplicateSSECSkip` —— SSE-C 对象成功完成任务并打 `repl_status=skipped`

`TestReplicateSSECSkip` 阶段 1（直接 Worker 路径）：

- 种子：`svc.Put(ctx, "default", "default", "ssec.txt", body, size, service.PutOptions{SSECustomerKey: []byte("0123456789abcdef0123456789abcdef")})`（32 字节；local `SupportsSSEC` 真，`prepareSSECWrite` 写 `_aero_sse_c_*` 元数据 ⇒ 判定可达）。
- 执行：`w := NewWorker(repo, primary, replica, nil, quietLogger()).WithObjectTagger(svc)`；`w.ReplicateObjectByID(ctx, obj.ID)`。
- 断言（精确、有序，防 F11 式假阳性）：
  1. `err == nil`（修复前：非 nil → **红**，实证）；
  2. `repo.GetObjectByID` 后 `Tags[TagStatus] == "skipped"`（修复前：无此 tag → **红**）；
  3. `replica.Get(ctx, obj.StorageKey)` **返回错误**（跳过不得写 replica，FR-1）。

### AC-2：复制 SSE-C 对象后 repository 无 `status='failed'` 且 `type='replicate'` 的 job 行

`TestReplicateSSECSkip` 阶段 2（完整 Pool 路径，探针实证形态）：

- 同一种子对象，`repo.EnqueueJob(ctx, repository.Job{Type: JobReplicate, Payload: EncodeObjectID(obj.ID), MaxAttempts: 2})`（MaxAttempts=2 仅为加速修复前红态；语义与默认 5 一致，FR-3 不改）。
- `jobs.NewRegistry()` + `Register(JobReplicate, func(...) error { id, err := DecodeObjectID(job.Payload); if err != nil { return err }; return w.ReplicateObjectByID(access-less ctx, id) })` —— 与 `workers.go:46-52` 逐行等价（本测试无 `access` 依赖，用 `context.Background()`）。
- 本地 `fastPool(repo, reg, 1)` + 可取消 ctx 启动；`waitFor` 轮询至终态（超时 `t.Fatalf`）。
- **断言（精确、有序）：**
  1. `ListJobs(ctx, repository.JobFailed, JobReplicate, 10)` 长度 == 0（修复前：恰 1 条 `failed`、`last_error` 含 "SSE-C" → **红**）；
  2. `ListJobs(ctx, repository.JobSucceeded, JobReplicate, 10)` 长度 == 1 且 `[0].LastError == ""`；
  3. 对象 `Tags[TagStatus] == "skipped"`。

### FR-2 锁死测试：`TestReplicateSSECSkipTaggerFailure`（对齐规格 FR-2"必须锁死"）

- 同一种子；`WithObjectTagger(failTagger{err: errors.New("boom")})`（5 行 stub 实现 `SetObjectTagsByID`）。
- 断言：`ReplicateObjectByID` 返回 **nil**（修复前：SSE-C 硬错误先于 tagger → **红**）。此测试回应对口 run 的 F2 教训——**新行为必须被测试钉死，防 reviewer 回退**。

### AC-3：`go test ./internal/replication` 全量通过（既有 `TestReplicateManagedEncryption` 不受影响）

回归矩阵（修复后必须全绿，**实证已绿**）：

| 测试 | 覆盖 | 状态 |
|---|---|---|
| `TestReplicateManagedEncryption`（**逐字不动**） | SSE-S3/KMS 复制路径（FR-5） | ✅ 实证绿 |
| `TestReplicateObject` | 明文复制 + `repl_status=replicated`（FR-5） | ✅ 实证绿 |
| `TestReplicationStatusTagsExactVersion` | objectID 精确 tag 写（FR-4/版本化） | ✅ 实证绿 |
| `TestRun` / `TestEncodeObjectID` / `TestDecodeObjectID` | 事件桥接与 payload | ✅ 实证绿 |
| `TestReplicateSSECSkip` / `TestReplicateSSECSkipTaggerFailure`（新增） | AC-1 + AC-2 + FR-2 | ✅ 实证绿（临时修复态） |

### 门禁命令

```
gofmt -l internal/replication/        # 无输出（实证）
go vet ./internal/replication/        # 干净（实证）
go test ./internal/replication        # 全绿（实证）
go test ./internal/replication -race  # 无新增并发，防回归
make check                            # 合入门禁（gofmt/build/vet/test ./...）
```

---

## 7. 硬门禁合规

| 门禁 | 状态 |
|---|---|
| `gofmt -l` | ✅ 实证（临时修复态） |
| `go build ./...` | ✅ 实证（测试编译运行） |
| `go vet ./...` | ✅ 实证 `internal/replication`；改动仅 1 文件无跨包引用 |
| `go test ./...` | ✅ 实证 `internal/replication` 全绿；其余包零触碰（diff 仅 replication 两文件） |
| 单文件 ≤ 500 行 | ✅ `replication.go` ≈165、`replication_test.go` ≈350（§2.3） |
| 单函数 ≤ 50 行 | ✅ `recordStatus` ≈22、`ReplicateObjectByID` ≈41 |
| I6 零新依赖 / 无断言框架 | ✅ 仅 `testing` + 既有包 |
| I2 零迁移 | ✅ 无 schema 变更 |
| I1 占位符 | ✅ 零 SQL 改动 |

---

## 8. 历史尝试与既往发现处置（gate 复检清单）

### 8.1 本 run 自身（`sse-c-objects-turn-into-permanently-failing-repl-33ddda83`）

- `DECISIONS.md`：仅 requirements 阶段，**PASS，零 finding**。无 design-gate 记录（本设计为首份）。
- 本设计与前序规格无冲突；规格 FR-1…FR-6、AC-1…AC-3、§5 非目标全部原样遵守。

### 8.2 对口 run（`add-a-self-healing-backfill-resync-path-for-obje-0a70354c`，design_gate **FAIL**，同源 resync 方向）

**与本方向关系：** 同出自 `docs/auto/analyses/internal-replication-9317f27a.json` 方向 2（漂移修复），规格 §5 列为非目标——本设计不实现它。但两者共享 `repl_status` tag 语义，**存在一处必须登记的交叉约束**。

**交叉约束（本设计的兼容性影响，证据链）：**

- resync 设计扫描条件为 `obj.Tags[TagStatus] == "replicated"` → 跳过（`internal-replication-resync-backfill-v1.design.md:182`），即**只要 tag 缺失或非 `replicated` 就重入队**；其 D8 明确拒绝在 sweep 中过滤 SSE-C 对象（:75），FM-4 将 SSE-C 对象归类为"每次 sweep 产生 1 条 failed 行"的既有失败循环（:253，escape hatch：手动 tag `replicated` 或调低 interval）。
- **本设计落地后，该 FM-4 的后果改变**：SSE-C 对象执行后得 `repl_status=skipped` + job `succeeded`（不再是 `failed`）——但 `skipped != "replicated"` ⇒ 若按原样实施，sweep 每次 tick 都会对同一 SSE-C 对象重入队 → 再跳过 → 再 succeed，形成**以 interval 为周期的 succeeded-job 空转**（危害低于修复前的 failed 污染与告警噪音，但仍是空转）。
- **登记为方向 2 实施时的硬约束**：sweep 跳过条件必须将 `skipped` 视为终态——`Tags[TagStatus] == "replicated" || Tags[TagStatus] == "skipped"` → 跳过（或等价地：仅对 tag 缺失的对象入队）。`repl_status` 写点由 1 处（现 `replication.go:140`）变为 2 处（replicated/skipped，均经 `recordStatus`），方向 2 的 E3 证据行号（:136-147）随之漂移——均为未合入文档，无代码影响。
- **本设计不为此改代码**（resync 零实现、属独立方向）；约束仅记录于此，方向 2 实施时 gate 会复检。

**gate FAIL 的 6 项 principal-reviewer 条件逐条处置：**

| 条件 | 级别 | 处置（对本方向） |
|---|---|---|
| F-DB1：隐式租户未覆盖（`{"default"} ∪ ListTenants()` 缺陷） | P0/High | **不适用**：本设计零扫描、零租户枚举；只对既有 job 的既有 objectID 执行单对象操作 |
| FM-table 修正（FM-7 dedupe 无 `FOR UPDATE`；FM-4 框架） | P1 | FM-7 **不适用**（零 `EnqueueJob` 改动）；FM-4 **交叉约束已登记**（见上）——方向 2 实施时须按新语义重述 FM-4 |
| page 1000 + marker 守卫 | P1 | **不适用**：本设计无分页循环 |
| FM-5/FM-6 错误路径 stub 测试 | P1 | **不适用**：resync 专属错误路径；本方向有对等的 FR-2 锁死测试（`TestReplicateSSECSkipTaggerFailure`） |
| `startReplicationResync` 抽取 + disabled 测试 | P1 | **不适用**：本设计 wiring 零改动（FR-3，`workers.go:46-52` 原样） |
| `make check` + `TestRun` 不动 | — | **本方向可验证**：AC-1/AC-2/AC-3 与 `make check` 已实证（§0/§6）；`TestRun` 零改动 |

**该 run 其他 adversarial findings（F-DB2..F-DB4、distributed F1-F4、QA F-1..F-4）**：均系 resync 自身实现细节（failed 行累积、跨实例 dedupe 竞态、页大小、marker 循环、错误路径、tenant 缺陷测试、wiring 约定），与 SSE-C 跳过路径无交集——除 F-DB2"failed 行累积"在**本设计落地后对 SSE-C 对象自动消失**（转为 succeeded+skipped，见上），其余一律**不适用**。

### 8.3 对口 run（`add-terminal-failure-handling-max-attempts-dead--c33c33cf`，adversarial **FAIL**，billing-outbox 终态处理）

该 run 与本方向共享"终态 vs 可重试"主题，其全部 findings 逐一处置（该 run 无 design-gate 记录，adversarial 即其最后裁决）：

| 对口 finding | 类别/级别 | 处置 | 证据 |
|---|---|---|---|
| QA F1 / BA B-F1 / SEC S1 / COMP F-3（429 分类矛盾） | P0 | **不适用** | 本设计无分类表、无 HTTP 状态判定：跳过条件是单一确定性谓词（SSE-C 元数据存在），且跳过**不是错误**（返回 nil），不存在"某状态码被误判终态"的语义空间 |
| QA F2（新行为零测试） | P1 | **已解决** | 新跳过路径由 `TestReplicateSSECSkip`（直接+Pool 两段）与 `TestReplicateSSECSkipTaggerFailure` 双重钉死；修复前实证红——reviewer 回退任何一段都会被 `-run TestReplicateSSECSkip` 与全量套件抓住 |
| QA F3（零值 guard 未测） | P1 | **不适用** | 本设计不新增任何 guard/默认值 |
| QA F4（New→字段 wiring 未测） | P1 | **不适用（构造性排除）** | `workers.go:46-52` wiring **零改动**（FR-3）；Pool 路径由 AC-2 端到端实证（handler 与 wiring 逐行等价，job 真实经 claim→execute→complete） |
| QA F5（fencing/claim 丢失） | P1 | **不适用** | 零 claim 语句改动（`jobs.go`/`repository/jobs.go` 不触碰） |
| QA F6（401+token 失效） | P2 | **不适用** | 无 HTTP 客户端面 |
| QA F7（last_error 截断） | P2 | **不适用** | 跳过路径写零错误文本；AC-2 断言 `LastError == ""` |
| QA F8（超 cap 成功路径） | P2 | **不适用** | 无 attempts/cap 语义改动 |
| QA F9（down 迁移零自动化） | P2 | **不适用** | 零迁移（FR-6/I2） |
| QA F10（并发 claim/TTL） | P2 | **不适用** | claim/reaper 路径零改动；`TestPoolFailsAfterMaxAttempts` 继续锁定 Pool 语义；AC-2 以真实单 worker Pool 运行 |
| QA F11（recording-store 断言歧义） | P2 | **已解决** | AC-2 断言全部精确有序：`failed==0`、`succeeded==1`、`LastError==""`、`tag=="skipped"`，无"任意一次调用满足"式弱断言 |
| BA B-F2 / SEC S2 / COMP F-2（终态不可见） | — | **不适用 + 本方向更优** | 本设计**不产生**终态错误：跳过错题以 `succeeded` 完结（复用 `IncJobCompleted`），对象带 `repl_status=skipped` tag，另有 info 日志（FR-4）——三通道可见，无静默状态；跳过 metric 属规格 §5 非目标 |
| BA B-F3（默认 cap 静默丢失） | — | **不适用** | 无 cap；跳过在 attempt 1 确定性成功 |
| BA B-F4 / DB F1 / COMP F-1（不朽行类） | — | **不适用** | 不新增行类；跳过 job 与所有成功 job 同生命周期。jobs 表 retention 是既有平台面（AGENTS.md §2.4 reconcile/GC），非本方向引入；且本修复**消除**一类污染行（永久 failed） |
| BA B-F5 / DB F4 / SEC S4（crash-reclaim 烧 attempts） | — | **不适用** | 零 attempts/claim 改动；跳过不消耗重试（首次即成功）；崩溃重跑再跳过（FM-5） |
| BA B-F6（终态触发不可恢复/未文档化） | — | **不适用** | 跳过条件即"对象元数据含 `_aero_sse_c_*`"，写于代码注释 + info 日志 `reason=SSE-C`；语义由 `SSECustomerInfo` 文档化 |
| BA B-F7 / COMP F-4（down 迁移毁数据） | — | **不适用** | 零迁移 |
| BA B-F8（无对账） | — | **显式接受** | 漂移修复 = 分析方向 2，规格 §5 非目标；`repl_status=skipped` 恰好为未来对账任务提供所需标记 |
| SEC S3（无界增长 DoS） | — | **不适用** | 无新增持久化行类；`succeeded` 行与既有同类同生命周期 |
| DB F2/F3/F5/F6（索引/重建/约束名/ops 索引） | — | **不适用** | 零 schema/查询改动 |
| COMP F-5/F-6/F-7（DPA/最小权限/BC-DR） | — | **不适用** | 不触碰数据治理面、无新表访问 |
| code-implementer review（该 run 内） | — | **不适用** | 其审查对象是 bucket-policy REST 方向，与本模块无交集 |

### 8.4 分析文件同源方向（`docs/auto/analyses/internal-replication-9317f27a.json`）

| 方向 | 处置 |
|---|---|
| 方向 2：replica 漂移检测/修复扫描 | 规格 §5 非目标，维持；本设计不扩大 |
| 方向 3：tag read-modify-write 竞态（`SetObjectTagsByID` 全量替换） | 规格 §5 非目标，维持；`recordStatus` 与 replicated 路径同构（同调用模式、同频率），**不新增竞态面**——该方向若实施，本设计的两处调用点（replicated/skipped）是同一迁移对象，无互斥 |

### 8.5 其他同名候选

- `replace-the-hardcoded-audit-governance-block-wit-cd58c0a7`、`chatstream-*`、`fix-chunk-boundary-*` 等：主题无关（audit/SSE-chat/parser），无交集，不复核。

---

## 9. 范围边界（重申规格 §5，不扩张）

| 不做 | 依据 |
|---|---|
| JobPool 哨兵错误 / terminal-error 分类 API（`internal/jobs` 零改动） | FR-3 |
| 漂移检测/修复、tag RMW 竞态修复、skip 计数遥测、SSE-C 深层复制能力 | 规格 §5（各自独立方向） |
| 文档/OpenAPI/配置变更 | 无 API 面变化 |
| 自动修复既有 `failed` 行 | 运维手动 retry 可收敛（§3/§5），自动化属独立方向 |
| 本设计不触碰 `access.SystemContext` 上下文流 | `workers.go` wiring 原样 |
