# 方向：multipart 完成 idempotency claim 在用量记账失败时泄漏（不释放）

> **模块：** `internal/service`（multipart 完成路径；零改动 repository/storage 契约） · **来源分析：** `docs/auto/analyses/internal-service-2bd58324.json`（方向 1） · **日期：** 2026-08-07
> **评分：** 价值 8 / 风险降低 8 / 工作量 2 / 置信度 9
> **本文所有代码引用均已对照仓库逐条验证**（行号以当前工作树为准；方向 5 条证据引用全部命中，零漂移，另有 4 条补充验证 E6-E9 改变了修复形态，见 §2/D1）。
>
> **与既有规格/在途实现的关系：** 无冲突——不触碰 schema/迁移（I2），不新增配置项，不改 adapter、不改 repository 接口（AC-2 用既有 `ClaimIdempotencyKey` 断言，见 FR-3/D3）。兄弟方向（quota TOCTOU、DeleteBucket 快照竞态）**明确不在本规格范围**（§5）。

---

## 1. 问题陈述

`finishMultipartCompletion`（file_multipart_complete.go:201-229）的失败路径**全部**经 `releaseMultipartCompletion`（:138-145 → `repo.DeleteIdempotencyKey`，idempotency.go:78）释放 claim，**唯独 `persistMultipartCompletion` 失败除外**（:225-227 裸 return）：

```go
if err := s.persistMultipartCompletion(ctx, u, saved, usage, idemKey); err != nil {
    return repository.Object{}, err   // ← 不释放 claim
}
```

`persistMultipartCompletion`（:273-292）的第一步就是 `accountObjectUsage`（:280-282）。当它失败（如 `AddTenantUsage` 的瞬时 DB 错误，或 `WithUsageAccountant` 注入的会计方 `Apply` 错误）时：

1. `saveMultipartObject` **已经提交了对象行**（finishMultipartCompletion :219 先于 persist 执行；UpsertObject/InsertObjectVersion，file_multipart.go:221-236）；
2. `CompleteIdempotencyKey`（:284-286）**未执行** → `idempotency_keys` 行停留在 `in_progress`；
3. `DeleteUpload`（:288-290）与 `s.emit(EventCreated)`（:292）**均未执行**。

此后**所有重试**都走 `replayMultipartCompletion`（:94-113）：`ClaimIdempotencyKey` 返回 `claimed=false`（idempotency.go:24 的 `ON CONFLICT DO NOTHING` 保证行存在），`rec.Status != IdempotencyCompleted` → 返回 `ErrPreconditionFailed: multipart completion is already in progress`。上传被**永久卡死**：对象行与 blob 已提交（客户其实"成功"了），但事件永不发出、用量永不入账、重试永远失败。

### 触发场景（真实工作流）

1. 配置了 `WithUsageAccountant`（file.go:125，订阅制商业配额）时，会计方 `Apply` 瞬时失败（DB 连接抖动、外部计量服务超时）→ 完成请求报错，claim 卡死。
2. 未配置会计方时，`repo.AddTenantUsage`（SQLite/Postgres 写放大下瞬时锁/繁忙错误）→ 同样卡死。
3. 客户端按 S3/REST 语义重试 `CompleteMultipart` → 得到 412 `ErrPreconditionFailed`，而对象实际已可见——**客户端看到"失败"且无法重试**，运维只能手工清 `idempotency_keys` 行。
4. 事件链断裂：`EventCreated` 永不发出 → 病毒扫描/复制/webhook 均不触发该对象（§2.4 契约）。

---

## 2. 现状与代码证据（已逐条验证）

| # | 证据 | 验证结果 |
|---|------|---------|
| E1 | `file_multipart_complete.go:201` — `finishMultipartCompletion`：prepare 失败 → `releaseMultipartCompletion`（:211-213）；`completeStoredMultipart` 失败 → release（:215-218）；`saveMultipartObject` 失败 → release（:220-223）；**`persistMultipartCompletion` 失败 → 裸 return，不释放**（:225-227） | ✅ 与方向引用一致，零漂移——缺陷机理逐行确认 |
| E2 | `file_multipart_complete.go:273` — `persistMultipartCompletion`：`accountObjectUsage` 在最前（:280-282），失败直接 return；其后才是 `CompleteIdempotencyKey`（warn-only，:284-286）、`DeleteUpload`（warn-only，:288-290）、`s.emit(EventCreated)`（:292） | ✅ 与方向引用一致——记账失败 ⇒ claim 未完成、upload 未删、事件未发 |
| E3 | `file_multipart_complete.go:138` — `releaseMultipartCompletion`：`repo.DeleteIdempotencyKey`（idempotency.go:78，纯 DELETE），失败仅 warn；`file_multipart_complete.go:94-113` — `replayMultipartCompletion`：status ≠ completed 或非 2xx → `ErrPreconditionFailed: multipart completion is already in progress` | ✅ 与方向引用一致——卡死机理（重试必 412） |
| E4 | `usage_accounting.go:20-27` — `UsageAccountant` 接口（`CheckQuota`/`Apply`）；`addTenantUsage`（:34-43）：`s.usageAccountant != nil` 时**独占路由**到 `Apply`；注入点 `file.go:125 WithUsageAccountant` | ✅ 与方向引用一致——AC-1 的注入面存在 |
| E5 | `idempotency.go:24` — `ClaimIdempotencyKey`：`INSERT … 'in_progress' … ON CONFLICT (tenant_id, idem_key) DO NOTHING`；行已存在 → `claimed=false` | ✅ 与方向引用一致；另确认 repository 接口（repository_interface.go:152-156）**无读取方法**——AC-2 用 `claimed==true` 断言行不存在（D3） |
| E6 | **补充验证（决定修复形态①）**：`LocalStorage.CompleteMultipartWithOptions`（local_multipart.go:88-110）**首次调用即消费上传**——`delete(s.uploads, uploadID)` 在 merge 之前（:96-100），且 `defer os.RemoveAll(up.dir)`（:104）删除全部 part 文件（**出错路径也执行**）。重试再调 → `"unknown upload %s"`。S3 后端同理（provider 的 multipart ID 完成即失效） | ✅ **claim 释放不足以让重试成功**——失败的尝试已消费 storage 上传，重试必然死在 `completeStoredMultipart`（file_multipart_complete.go:214-217） |
| E7 | **补充验证（决定修复形态②）**：`objectWriteUsage`（usage.go:14-39）从**当前对象行**读 `previousSize`；`deltas`（:30-36）：`newObject` → `(newSize, 1)`，否则 `(newSize-previousSize, 0)`。失败的尝试已提交 size=N 的对象行 → 重试算出 delta=0 → **"用量恰好等于对象大小一次"永远不成立**（除非失败尝试零持久足迹） | ✅ 与 E6 合起来证明：**仅释放 claim 不满足方向验收 1**（D1） |
| E8 | **补充验证（修复形态的等价性）**：`accountObjectUsage(ctx, tenant, usage, size)` 只使用 `usage.deltas(size)`（usage.go:51-58）；multipart 路径中 `saved.Size ≡ total`（Σ part.Size）——`buildObjectFromUpload`（file_multipart.go:193-197，`info.Size==0` 时回落 `total`）且 `mergeParts`/`writePartsTo`（local_multipart.go:118-136）产物恰为 Σ part 大小；`prepareMultipartCompletion` 的配额预检**已经**用 `usage.deltas(total)`（file_multipart_complete.go:245-246） | ✅ 把记账移到 `completeStoredMultipart` **之前**并用 `total` 记账，与现语义逐位等价（FR-1） |
| E9 | **补充验证（回归面）**：现有用量断言测试 `TestCompleteMultipartOverwriteAccountsOnlyUsageDelta`（usage_consistency_test.go:44-65，期望 3/1）、`TestMultipartRespectsBucketQuota`（:67-88，prepare 阶段拒绝，路径不变）、重放测试 `TestMultipartCompletionReplaySurvivesUploadDeletion`（multipart_correctness_test.go:87-102，重放发生在 execute 之前，不触记账）——记账前移后结果全部不变；`quota_failure_test.go` 无 multipart 覆盖 | ✅ 记账前移不破坏现有测试（AC-3 基线） |

### 缺陷机理

```
CompleteMultipart ── ClaimIdempotencyKey（in_progress 行落库）
  └─ executeMultipartCompletion
      └─ finishMultipartCompletion
          ├─ prepareMultipartCompletion 失败 ──→ release ✓
          ├─ completeStoredMultipart  失败 ──→ release ✓（但 storage 上传已被消费，E6）
          ├─ saveMultipartObject      失败 ──→ release ✓
          └─ persistMultipartCompletion
              ├─ accountObjectUsage 失败 ──→ ✗ 不释放 ← 本方向缺陷
              │    （对象行已提交 / blob 已落盘 / 事件未发 / upload 未删）
              ├─ CompleteIdempotencyKey（warn-only）
              ├─ DeleteUpload（warn-only）
              └─ emit(EventCreated)
重试 ── ClaimIdempotencyKey → claimed=false → replayMultipartCompletion
      └─ status=in_progress → ErrPreconditionFailed "already in progress"（永久 412）
```

---

## 3. 需求规格

### FR-1：用量记账前移到 storage 完成消费与对象行提交之前

`finishMultipartCompletion`（file_multipart_complete.go:201）中，把 `accountObjectUsage` 从 `persistMultipartCompletion` **移到 `prepareMultipartCompletion` 与 `completeStoredMultipart` 之间**，用 `total`（Σ part 大小）记账：

```go
bcfg, usage, err := s.prepareMultipartCompletion(ctx, u, total)
if err != nil {
    return repository.Object{}, s.releaseMultipartCompletion(ctx, scope, idemKey, err)
}
if err := s.accountObjectUsage(ctx, u.TenantID, usage, total); err != nil {
    return repository.Object{}, s.releaseMultipartCompletion(ctx, scope, idemKey, err)
}
info, err := s.completeStoredMultipart(ctx, u, storageParts, opts)
...
```

- **约束 a（位置语义）：** 记账必须发生在 `completeStoredMultipart`（consumes storage 上传，E6）与 `saveMultipartObject`（提交对象行，file_multipart.go:221-236）**之前**——记账失败时零持久足迹（无 blob、无对象行、storage 上传完好），重试可完整重跑（E6/E7 的唯一满足形态，D1）。
- **约束 b（等价性）：** 记账参数用 `total` 而非 `saved.Size`——两者恒等（E8）；记账值与配额预检的 `usage.deltas(total)`（:245）一致，预检仍先行。
- **约束 c（persist 瘦身）：** `persistMultipartCompletion` 删除 `accountObjectUsage` 调用，只保留 `CompleteIdempotencyKey`（warn-only）→ `DeleteUpload`（warn-only）→ `emit(EventCreated)`。签名不变（返回 `nil`），warn-only 语义不变（§2.4 事件契约），最小 diff。
- **约束 d（配额预检不变）：** `prepareMultipartCompletion` 的 `preflightQuota`/`preflightBucketQuota` 仍在记账之前、不动。

### FR-2：claim 失败路径统一释放（本方向标题的直接要求）

- **约束 a（不变量）：** `completeMultipart`（:70）成功 `ClaimIdempotencyKey` 之后，**任何**错误返回都必须先经 `releaseMultipartCompletion`（:138，即 `DeleteIdempotencyKey`），包括记账失败路径——与现有三个兄弟错误路径（E1）同型。
- **约束 b（成功路径不变）：** 成功后仍走 `CompleteIdempotencyKey` 落 completed + 响应体缓存 → 重试重放（`TestMultipartCompletionReplaySurvivesUploadDeletion` 锁定的行为不变）。
- **约束 c（重试语义）：** 记账失败释放 claim 后，重试 = 全新 claim + 完整重跑（FR-1 保证上传/对象/用量均无残留）→ 成功时用量恰好入账一次、`EventCreated` 恰好发出一次（AC-1 断言）。

### FR-3：安全不变量

| 规则 | 依据 |
|------|------|
| 记账失败必须发生在任何持久变更（storage 上传消费、blob、对象行）之前 | E6/E7——这是"重试成功 + 用量恰好一次"成立的唯一修复形态（D1） |
| 失败释放 claim 用既有 `releaseMultipartCompletion`，不新增 repository 方法 | E5——`DeleteIdempotencyKey` 已存在；AC-2 用 `ClaimIdempotencyKey` 返回 `claimed==true` 断言行不存在（D3） |
| 配额预检（check-then-act）与记账顺序保持：预检 → 记账 → 完成 | FR-1d；`TestMultipartRespectsBucketQuota`（prepare 阶段拒绝）行为不变（E9） |
| 不触碰 `replayMultipartCompletion` 的 412 语义（完成状态的重放仍返回缓存对象） | 与 AC-3 基线测试一致（multipart_correctness_test.go:87-102） |
| 无迁移、无配置项、无 adapter/handler 变更、无 telemetry 变更 | 缺陷与修复均在 FileService 内部（I5 基线保护） |

---

## 4. 验收标准（方向验收 1-3 全部保留并测试化）

测试基建（全部已存在）：`newQuotaTestSvc`（quota_test.go:15，SQLite+local FS）、`WithUsageAccountant`（file.go:125）、`assertTenantUsage`（usage_consistency_test.go:221）、`repo.ClaimIdempotencyKey`/`DeleteIdempotencyKey`。新增测试放 `internal/service/usage_consistency_test.go`（package `service` 白盒）或同包新文件 `multipart_claim_test.go`。

### AC-1 — 记账失败一次后重试成功，用量恰好一次（对应方向验收 1）

**测试双（`failOnceAccountant`）：** 实现 `UsageAccountant`——`CheckQuota` 恒通过；`Apply` 第 1 次返回哨兵 error（如 `errors.New("transient accounting failure")`），**此后每次委托 `repo.AddTenantUsage(ctx, m.TenantID, m.DeltaBytes, m.DeltaObjects)` 并记录调用次数**（`addTenantUsage` 在注入后独占路由，usage_accounting.go:34-43——委托是 repo 用量可见的唯一途径）。

**断言序列**（全部确定性可复现）：

1. `svc, repo := newQuotaTestSvc(t)`；`svc.WithUsageAccountant(failOnce)`。
2. `InitMultipart` + `UploadPart(1, body, N)`（N=5，内容 `"12345"`）。
3. 首次 `CompleteMultipart` → **error 非 nil**，且 `!errors.Is(err, ErrPreconditionFailed)`（排除"already in progress"重放路径）。
4. 立即重试 `CompleteMultipart` → **成功**；`obj.Size == N`；`svc.Get` 可读回 `"12345"`。
5. `assertTenantUsage(t, repo, N, 1)`——**最终租户用量恰好等于对象大小一次**。
6. `failOnce.applyCalls == 2`（第 1 次失败 + 第 2 次成功）——用量恰好一次的直接证据。

### AC-2 — 记账失败后无 stuck in-progress 行（对应方向验收 2）

同一测试双，独立 upload：

1. `InitMultipart` + `UploadPart(1, body, N)`。
2. 首次 `CompleteMultipart` → error（同 AC-1 步 3）。
3. **失败后、重试前**：`rec, claimed, err := repo.ClaimIdempotencyKey(ctx, "default", "_mp_complete:"+upload.ID, "", "")` → **`claimed == true`**（idempotency.go:24 的 ON CONFLICT 语义：行不存在才返回 claimed=true）——即 `_mp_complete:<uploadID>` 无 `in_progress` 残留行。
4. 清理：`repo.DeleteIdempotencyKey(ctx, "default", "_mp_complete:"+upload.ID)`，随后重试 `CompleteMultipart` 成功（与 AC-1 相同的重试语义，可不重复断言用量）。

（注：repository 接口无 `GetIdempotencyKey`（repository_interface.go:152-156），`claimed==true` 是既有 API 上最直接的"行不存在"证明，D3。）

### AC-3 — 门禁（对应方向验收 3）

```bash
go test ./internal/service -run Multipart          # 方向验收原文：现有 multipart 测试全部通过
go test ./internal/service -run 'Usage|Quota'      # 记账前移的回归面（E9 列出 4 个既有断言测试）
go test -race ./internal/service -run 'Multipart|Usage'   # 白盒新增测试 + 既有测试无竞态
gofmt -l internal/service                          # 期望无输出（make check 同款）
go vet ./internal/service                          # 期望无输出
```

---

## 5. 范围边界（明确不做）与决策记录

| 不做 / 决策 | 理由 |
|------|------|
| **D1：仅释放 claim 不够——必须同步前移记账**（本规格最重要的决策） | 方向验收 1 要求"重试成功 + 最终用量恰好等于对象大小一次"。两条硬事实：① `LocalStorage.CompleteMultipartWithOptions` 首次调用即消费上传（E6，`delete(s.uploads,…)` + `os.RemoveAll(up.dir)`，出错也执行）→ 释放 claim 后重试仍死在 `completeStoredMultipart`；② `objectWriteUsage` 从已提交对象行读 previousSize（E7）→ 重试 delta=0，用量永不为 N。**唯一同时满足两条验收的形态是：记账失败时零持久足迹（记账先于 storage 消费与对象行提交）**。方向标题（释放 claim）是必要不充分条件，FR-1+FR-2 合起来才是充分条件 |
| **D2：接受镜像漂移——storage 完成失败时用量已入账（+N 无对象）** | 记账前移后，若 `completeStoredMultipart`/`saveMultipartObject` 失败，用量已 +N 而无对象行。该模式**当前已同样卡死**（上传被消费，E6，重试永远失败、用量 0）——前移不使其更坏，只是漂移方向从"对象在、用量缺"变为"用量在、对象缺"。方向验收只钉死记账失败路径；补偿性删除（回滚对象行+blob）或 storage 完成幂等化（保存已完成记录）属**明确不做**的新机制，违反"工作量 2"的边界 |
| **D3：不新增 repository 方法**（否决 `GetIdempotencyKey`） | AC-2 用 `ClaimIdempotencyKey` 的 `claimed==true` 语义即可证明行不存在（E5）；`idempotency_keys` 表无 schema 变更（I2） |
| **D4：`persistMultipartCompletion` 签名保留（恒返 nil）** | 最小 diff；`CompleteIdempotencyKey`/`DeleteUpload` 的 warn-only 语义（:284-290）与事件契约（§2.4）不动 |
| **D5：记账用 `total` 而非 `saved.Size`** | E8 证明恒等（`buildObjectFromUpload` 回落逻辑 + `mergeParts` 产物 = Σ part 大小）；配额预检本就使用 `usage.deltas(total)`，记账与其一致 |
| 不做：配额 check-then-act TOCTOU（分析方向 2） | 独立方向，属另一个修复（加锁/事务化读改写），不在本规格 |
| 不做：DeleteBucket 快照竞态（分析方向 3） | 独立方向 |
| 不做：`prepareMultipartCompletion` 内嵌记账 | 语义相同但使"prepare"职责膨胀；放在 finishMultipartCompletion 内、与三个兄弟 release 路径并列更清晰（FR-1） |
| 不做：文档/配置/telemetry/适配层变更 | 无新配置、无行为契约变化；`docs/configuration.md`、OpenAPI、SDK 均不涉及 |

**proposed_vs_verified 对照：** verified——缺陷路径（E1）、记账失败即事件缺失（E2）、重试 412 卡死（E3）、会计注入面（E4）、claim 落库语义（E5）；proposed（方向未预见的补充验证）——E6/E7 证明"仅释放 claim"不满足验收 1，E8 证明记账前移的等价性，E9 证明回归面为空。FR-1（前移+`total` 记账）、FR-2（失败统一释放）、FR-3（零持久足迹不变量）均由上述证据驱动。

---

## 6. 实现指引（供验收后落地，非本规格交付物）

1. **`internal/service/file_multipart_complete.go`**：
   - `finishMultipartCompletion`（:201-229）：在 `prepareMultipartCompletion` 与 `completeStoredMultipart` 之间插入 `accountObjectUsage(ctx, u.TenantID, usage, total)` 失败分支（`return repository.Object{}, s.releaseMultipartCompletion(ctx, scope, idemKey, err)`），与兄弟错误路径同型（FR-1a）。
   - `persistMultipartCompletion`（:273-292）：删除 `accountObjectUsage` 调用（:280-282 及其 if 分支）；函数体只剩 `CompleteIdempotencyKey`（warn）→ `DeleteUpload`（warn）→ `emit`（FR-1c）。签名与返回值保持不变。
2. **测试**（`internal/service/usage_consistency_test.go` 追加或同包新文件）：`failOnceAccountant` 测试双（委托 `repo.AddTenantUsage` + 计数）；`TestMultipartCompleteClaimReleasedOnUsageFailure`（AC-1 六步）；`TestMultipartCompleteNoStuckClaimAfterUsageFailure`（AC-2 四步）。断言助手复用 `assertTenantUsage`/`newQuotaTestSvc`，不引入断言框架（I6）。
3. **验证**：AC-3 五条命令 + `make check`（单文件 ≤ 500 行、gofmt/vet/test 全绿）。
