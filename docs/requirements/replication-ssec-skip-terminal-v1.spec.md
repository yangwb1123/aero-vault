# 方向：SSE-C 对象永久失败复制任务 —— 归类为终态跳过（验收规格 · 已验证现状）

> **模块：** `internal/replication`（`replication.go` · `cmd/server/workers.go` · `internal/jobs/jobs.go`）
> **来源分析：** `docs/auto/analyses/internal-replication-9317f27a.json`（方向 1）· **日期：** 2026-08-07 · **HEAD：** `acfaaf4`
> **评分：** 价值 8 / 风险降低 7 / 工作量 2 / 置信度 9
> **状态声明：** 本文是**验收契约**而非绿地设计：逐条核验方向引证（§1）、登记 SSE-C 对象在复制管线中的完整失败链（§2）、把"终态跳过"锁定为 handler 内分类（§3，JobPool 语义零改动）、原样保留三条验收检查并映射为可执行测试规格（§4）。超范围项一律不做（§5）。

---

## 1. 证据核验（方向引证逐条对照当前 HEAD）

| # | 方向引证 | 当前 HEAD 位置 | 核验结论 |
|---|---------|----------------|---------|
| E1 | `internal/replication/replication.go:115` — SSE-C 对象返回硬错误 | `ReplicateObjectByID` :114-117：`if _, _, ok := service.SSECustomerInfo(obj.Metadata); ok { return errors.New("replication: SSE-C object requires an unavailable customer key") }` | ✅ **行号精确**。错误返回发生在任何 tag 写入之前（tag 写回在 :136-144）⇒ SSE-C 对象当前**从不**获得 `repl_status` |
| E2 | `internal/replication/replication.go:116` — 同上（错误文案） | :116 恰为 `return errors.New(...)` | ✅ **行号精确** |
| E3 | `internal/jobs/jobs.go:207` — 无条件重试→失败路径 | `runOne` :207 `if job.Attempts >= job.MaxAttempts {` → :208-211 `FailJob` + `IncJobFailed`。**无任何 handler 错误分类**：`Handler` 注释（:26-28）明示"Returning an error triggers a retry with backoff until MaxAttempts, after which it is marked failed"；`MaxAttempts` 默认 **5**（`repository/jobs.go:48-49`） | ✅ **行号精确**。语义成立：每个 handler error 一律退避重试（:206-212），耗尽后 `status='failed'` 永久终态 |
| E4 | `internal/jobs/jobs.go:208` — `FailJob` 调用 | :208 `p.logger.Error("job failed permanently", ...)` → :209 `_ = p.repo.FailJob(ctx, job.ID, runErr.Error())` | ✅ **行号精确**。`FailJob` 写 `status='failed'` + `last_error`（`repository/jobs.go:166-182`），无区分依据 |
| E5 | `cmd/server/workers.go:46` — handler wiring | :46-52：`jobReg.Register(replication.JobReplicate, func(...) error { id, err := replication.DecodeObjectID(job.Payload); ...; return rw.ReplicateObjectByID(access.SystemContext(ctx, job.TenantID), id) })` | ✅ **行号精确**。handler 把 `ReplicateObjectByID` 的错误**原样**交回 Pool ⇒ E1 的错误必然进入 E3 的重试→失败链 |

**方向问题陈述核验（当前状态）：**

| 陈述 | 核验 |
|------|------|
| "SSE-C 上传是一等公民特性（S3 SSE-C headers 在 adapter 契约内）" | ✅ **成立**：`internal/api/s3compat/ssec.go`（完整 SSE-C 头解析/校验）、`storage.SupportsSSEC` 下 local（`local_read.go:131`）与 s3（`s3.go:409`）均支持；`service.prepareSSECWrite`（`ssec.go:17-38`）把 `_aero_sse_c_algorithm=AES256` + `_aero_sse_c_key_md5` 写入对象元数据；`service.SSECustomerInfo`（`ssec.go:91-99`）据此判定 |
| "复制错误与真实失败无法区分" | ✅ **成立**：SSE-C 错误的终态与真实复制失败完全同形——`status='failed'`、`last_error` 文本、`IncJobFailed` 遥测，无任何可区分标记 |
| "无 `repl_status` 记录" | ✅ **成立**：错误先于 tag 写回（E1），SSE-C 对象永远没有 `repl_status` 标签 |
| "每次手动 admin 重试再次失败" | ✅ **成立**：`POST /v1/admin/jobs/{id}/retry` 重入同一 handler，`:115` 判定确定性成立，错误必现（幂等地失败） |

**补充核验（方向未引、影响实现与测试选择的现状）：**

| 位置 | 现状 | 结论 |
|------|------|------|
| `internal/jobs/jobs_test.go:147` `TestPoolFailsAfterMaxAttempts` | handler 恒错 → 2 次 attempts 后 `status='failed'`、`last_error` 非空 | 重试→失败语义已有测试锁定；本方向**不改**该语义 |
| `internal/replication/replication_test.go` | 现有 6 个测试：`TestReplicateObject` · `TestReplicateManagedEncryption` · `TestReplicationStatusTagsExactVersion` · `TestDecodeObjectID` · `TestEncodeObjectID` · `TestRun`；**无任何 SSE-C 路径测试** | `-run TestReplicateSSECSkip` 当前**真空通过**，新测试命名必须按下表前缀使过滤器非空（同 CLI/元数据规格先例） |
| `TestReplicateManagedEncryption`（SSE-S3/KMS 路径） | `ServerSideEncryptionInfo` → `putOptions.SSEAlgorithm/SSEKMSKeyID`（:124-127），与 SSE-C 判定互斥（`ssec.go:20`） | 修复不影响该测试（§4 回归矩阵） |
| 测试基建 | local storage `SupportsSSEC()==true`；`svc.Put` 支持 `PutOptions.SSECustomerKey`（`file.go:231`），32 字节密钥经 `prepareSSECWrite` 写入元数据 ⇒ `SSECustomerInfo` 判定可达 | 三条验收均可仅用现有基建实现，**零新增依赖/迁移/配置** |
| `repl_status` 取值枚举 | 仓库与文档均不枚举取值（AGENTS.md §2.4 仅写"JobPool 重试"；`docs/configuration.md:324-331` 只有 `REPLICATION_*` 环境变量） | 新增 `skipped` 取值无契约冲突，无需文档/配置改动 |

---

## 2. 问题陈述（SSE-C 对象的完整失败链）

**现状链路（E1-E5 拼合）：**

```
S3 PUT 带 x-amz-server-side-encryption-customer-* 头
  → prepareSSECWrite 写入 _aero_sse_c_algorithm / _aero_sse_c_key_md5 元数据
  → created 事件 → Worker.Run 桥接 replicate job（replication.go:98-102，payload 仅含 object_id，
    入队侧对 SSE-C 无感知）
  → Pool.ClaimJob（attempts+1）→ handler（workers.go:46-52）→ ReplicateObjectByID
  → :115-116 硬错误 "replication: SSE-C object requires an unavailable customer key"
  → Pool: runErr != nil → 退避重试（jobs.go:206-212，默认 MaxAttempts=5）
  → 5 次耗尽 → FailJob → status='failed'，last_error=SSE-C 文案（jobs.go:207-209）
```

**后果：**
- **永久污染**：任一部署只要服务 SSE-C 对象（一等公民特性，§1）并开启复制，jobs 表被确定性、不可执行的失败任务填满；每次手动 admin retry（`/v1/admin/jobs/{id}/retry`）幂等地再失败一次。
- **不可区分**：与真实复制失败（primary 不可达、replica 写失败等）同为 `status='failed'` + `IncJobFailed`，告警无法过滤。
- **状态缺失**：对象上无任何 `repl_status` 标记（错误先于 :136-144 的 tag 写回），运维无法分辨"已复制/已跳过/未尝试"。
- **本质不可修复**：SSE-C 客户密钥按设计不落盘（`SSECustomerInfo` 只暴露 algorithm/keyMD5，`ssec.go:91-99`），复制**永远**无法携带客户密钥 ⇒ 重试只会反复失败，任何重试次数/退避策略都无法改变终态。

**修复方向（方向原文）：** 将 SSE-C 归类为**终态跳过**——handler 内判定（判定点已存在于 :115），记录 `repl_status=skipped` 后返回成功，job 以 `succeeded` 完结。跳过分类完全落在 `internal/replication`，**JobPool 语义零改动**（真实错误保持可重试）。

---

## 3. 需求规格（FR，范围严格限定于方向）

### FR-1：SSE-C 对象归类为终态跳过（核心行为）

`ReplicateObjectByID` 的 SSE-C 分支（`replication.go:115-116`）由"返回硬错误"改为"**终态跳过**"：

1. **不得触碰 replica 后端**：跳过路径不执行 primary `Get`、不执行 replica `Put`（当前错误返回已在任何存储访问之前，保持该位置不变）。
2. **记录状态标签**：以 `obj.Tags` 快照 + `repl_status=skipped` 经 tagger 写回（`SetObjectTagsByID`），与 replicated 路径（:136-144）**同构**——仅替换状态值 `"replicated"` → `"skipped"` 与日志文案。
3. **返回 `nil`**：JobPool 视为成功 → `CompleteJob` → `status='succeeded'`（`repository/jobs.go:147-163`）。

### FR-2：跳过路径的 tag 写失败不得失败 job

`tagger` 为 `nil` 或 `SetObjectTagsByID` 失败时：warn log（含 object_id）后**仍返回 `nil`**——与 replicated 路径 :142-147 完全一致（"Replica write already succeeded; a tag failure shouldn't fail the job"）。若此处返回错误，跳过错题会以新错误重新进入重试→失败链，污染照旧——**这是本方向最容易引入的回归点，必须锁死**。

### FR-3：JobPool / jobs.go 语义零改动（非附带，而是硬要求）

- `internal/jobs` **不做任何修改**：无哨兵错误、无 terminal-error 分类 API、无 `MaxAttempts` 语义变化。
- 真实复制错误（`GetObjectByID` 失败、primary `Get` 失败、replica `Put` 失败、payload 解码失败）**保持可重试**，默认 `MaxAttempts=5` → `failed` 的语义由 `TestPoolFailsAfterMaxAttempts` 等既有测试锁定。
- `workers.go:46-52` 的 handler wiring **不变**（仍直接 `return rw.ReplicateObjectByID(...)`）——跳过分类发生在 `ReplicateObjectByID` 内部，wiring 无需感知。

### FR-4：可观测性

- 跳过路径输出 **info 级日志**，字段含 `object_id`、`tenant`、`key`、原因（如 `reason=SSE-C`）——与 replicated 路径的 `w.logger.Info("replicated", ...)`（:148-149）对称。
- `repl_status=skipped` 经 `SetObjectTagsByID` **按 objectID 精确写**，版本化语义与 replicated 路径一致（既有 `TestReplicationStatusTagsExactVersion` 模式）；`TagStatus`/`JobReplicate` 常量不变。

### FR-5：非 SSE-C 路径零行为变化

| 路径 | 现状 | 必须保留 |
|------|------|---------|
| SSE-S3/KMS（`ServerSideEncryptionInfo` → `putOptions.SSEAlgorithm/SSEKMSKeyID`，:124-127） | 正常复制 + `repl_status=replicated` | 不变（`TestReplicateManagedEncryption` 锁定） |
| 明文对象 | 正常复制 + `repl_status=replicated` | 不变（`TestReplicateObject` 锁定） |
| 错误语义 | 对象不存在 / primary 不可达 / replica 写失败 → 可重试错误 | 不变（FR-3） |
| 事件桥接 | `created` 事件 → replicate job（:98-102） | 不变（入队侧不做 SSE-C 过滤——跳过判定只在执行侧） |

### FR-6：无新增面

不新增 API 端点、配置项、迁移文件（I2）、go.mod 依赖（I6）；`replication.go` 现有 149 行，改动后仍满足单文件 ≤ 500 行硬门禁。

---

## 4. 验收标准（方向原文三条，原样保留并测试化）

> 全部落在 `internal/replication/replication_test.go`（包 `replication`，既有基建：`repository.Open`+`Migrate`+`storage.NewLocal`+`service.NewFileService`）。当前仓库 `-run TestReplicateSSECSkip` **零匹配**（§1 补充核验）⇒ 新测试命名必须按下表前缀，使过滤器非空；新测试在修复前必须**红**（行为契约）。

### AC-1 `go test ./internal/replication -run TestReplicateSSECSkip`：SSE-C 对象成功完成任务并打 `repl_status=skipped`

> 方向原文：*SSE-C object completes the job successfully and tags repl_status=skipped*

**测试化规格：** 新增 `TestReplicateSSECSkip`（命名即过滤器匹配项）：
- **种子**：`svc.Put(ctx, "default", "default", "ssec.txt", body, size, service.PutOptions{SSECustomerKey: []byte("0123456789abcdef0123456789abcdef")})`——32 字节密钥，local 支持 SSE-C（`local_read.go:131`），`prepareSSECWrite` 写入 `_aero_sse_c_algorithm`/`_aero_sse_c_key_md5` ⇒ `SSECustomerInfo` 判定可达。
- **执行**：`w := NewWorker(repo, primary, replica, nil, logger).WithObjectTagger(svc)`；`err := w.ReplicateObjectByID(ctx, obj.ID)`。
- **断言**：`err == nil`（修复前：非 nil，测试红）；`repo.GetObjectByID(ctx, obj.ID)` 后 `obj.Tags[TagStatus] == "skipped"`（修复前：无该标签，红）。
- **补充断言（锁 FR-1 不触碰 replica）**：`replica.Get(ctx, obj.StorageKey)` 返回错误——跳过路径不得写 replica。
- 运行：`go test ./internal/replication -run TestReplicateSSECSkip`。

### AC-2 复制 SSE-C 对象后，repository 无 `status='failed'` 且 `type='replicate'` 的 job 行

> 方向原文：*After replicating an SSE-C object, repository has no job row with status='failed' and type='replicate'*

**测试化规格：** 在 `TestReplicateSSECSkip` 内追加**完整 Pool 路径**（对齐 `internal/jobs/jobs_test.go` 的 `fastPool`/`waitFor` 模式）：
- 同一 SSE-C 种子对象，`Enqueue` 一条 `repository.Job{Type: JobReplicate, Payload: EncodeObjectID(obj.ID), MaxAttempts: 2}`（`MaxAttempts=2` 仅为加速修复前红态运行；语义与默认 5 一致，FR-3 不改）。
- `jobs.NewRegistry()` + `Register(JobReplicate, func(...) error { … DecodeObjectID → ReplicateObjectByID … })`——handler 与 `workers.go:46-52` **逐行等价**。
- `jobs.NewPool(repo, reg, 1, logger)` + 可取消 ctx 启动；`waitFor` 轮询直到 job 到达终态（超时 `t.Fatalf`）。
- **断言**：`ListJobs(ctx, repository.JobFailed, JobReplicate, 10)` 为空；`ListJobs(ctx, repository.JobSucceeded, JobReplicate, 10)` 恰 1 条；该 job `LastError == ""`；对象 `Tags[TagStatus] == "skipped"`。（状态常量 `repository.JobPending/JobSucceeded/JobFailed` 见 `repository/jobs.go:13-16`。）
- 修复前：handler 恒错 → 2 次 attempts 后 `status='failed'`、`last_error` 含 "SSE-C" → 红（且 `-run TestReplicateSSECSkip` 会匹配本测试名——Go `-run` 为正则前缀匹配）。

### AC-3 `go test ./internal/replication` 全量通过（既有 `TestReplicateManagedEncryption` 不受影响）

> 方向原文：*go test ./internal/replication passes (existing TestReplicateManagedEncryption unaffected)*

**回归矩阵：**

| 测试 | 覆盖 | 修复后必须 |
|------|------|-----------|
| `TestReplicateManagedEncryption` | SSE-S3/KMS 复制路径（FR-5） | 绿（**逐字不动**） |
| `TestReplicateObject` | 明文对象复制 + `repl_status=replicated`（FR-5） | 绿 |
| `TestReplicationStatusTagsExactVersion` | 按 objectID 精确 tag 写（版本化语义，FR-4） | 绿 |
| `TestRun` / `TestEncodeObjectID` / `TestDecodeObjectID` | 事件桥接与 payload（FR-5） | 绿 |
| `TestReplicateSSECSkip`（新增） | AC-1 + AC-2 | 绿 |

**合入门禁：** `make check` 全量通过（`gofmt` · `go build` · `go vet` · `go test ./...`）；`go test ./internal/replication -race` 通过（本方向无新增并发，防回归）。

---

## 5. 范围边界（明确不做）

| 不做 | 理由 |
|------|------|
| JobPool 哨兵错误 / terminal-error 分类 API（jobs.go 任何改动） | FR-3：跳过分类在 handler 内完成即满足验收；改 Pool 会触碰全部 job 类型语义，违反最小范围 |
| replica 漂移检测/修复扫描（同分析方向 2） | 独立方向，与本问题正交 |
| tag read-modify-write 竞态修复（同分析方向 3：`SetObjectTagsByID` 全量替换） | 独立方向；本规格沿用 replicated 路径既有的同构写法，不改写机制 |
| SSE-C 对象"可复制"的深层能力（携带客户密钥跨后端复制） | 客户密钥按设计不落盘（`ssec.go:91-99`），方向明确接受 skip 语义 |
| 跳过计数遥测/告警规则（新增 `IncReplicationSkipped` 等） | 方向未要求；job 以 `succeeded` 完结即复用 `IncJobCompleted`，跳过路径另有 info 日志（FR-4）。metric 增加留待独立方向 |
| 文档/OpenAPI/配置变更 | 无 API 面变化；`repl_status` 取值仓库未枚举（§1），无契约需更新 |
