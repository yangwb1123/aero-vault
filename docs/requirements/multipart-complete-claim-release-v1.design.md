# 设计：multipart 完成 idempotency claim 在用量记账失败时泄漏 —— 记账前移 + 失败统一释放

> **模块：** `internal/service`（multipart 完成路径；零改动 repository/storage/adapter 契约） · **上游：** `docs/requirements/multipart-complete-claim-release-v1.md`（本管线 requirements 阶段 PASS，2026-08-07 00:25:03） · **基线：** HEAD `acfaaf4`；工作区有其它 campaign 的未提交改动，与本方向**零交集**（`git status` 确认：本设计改动文件 `file_multipart_complete.go`、新测试文件均不在修改列表；间接依赖的 `file.go:125 WithUsageAccountant` 仅被 `WithDeleteFailOpen` 追加偏移、`quota_test.go` 的 `newQuotaTestSvc` 追加 `WithAuthorizer(allowAllProvider{})`，语义不变）
> **本设计所有代码引用均对照当前工作树逐行重新验证**（requirements 证据视为不可信声明，E1-E9 全部复验，见 §1；基线 `go build ./...` + `go test ./internal/service -run 'Multipart|Usage|Quota'` 实测全绿，§8）。

---

## 0. 设计总览

| 项 | 决策 |
|---|---|
| 方案 | **FR-1 + FR-2：`accountObjectUsage` 从 `persistMultipartCompletion` 前移到 `prepareMultipartCompletion` 与 `completeStoredMultipart` 之间**（用 `total` 记账，E8 证明 ≡ `saved.Size`）；失败路径统一经 `releaseMultipartCompletion` 释放 claim。这是满足方向验收 1（重试成功 + 用量恰好一次）的**唯一**形态（E6/E7 排除 claim-release-only，见 §2.3） |
| API 变化 | **零公开 API**：`FileService` 方法签名不变、`Repository`/`Storage` 接口不变、`UsageAccountant` 接口不变、无新导出符号。`persistMultipartCompletion` 私有签名保留（D4，`usage` 形参变为未用——Go 允许未用形参，gofmt/vet 无感） |
| 改动文件 | `internal/service/file_multipart_complete.go`（309 → **≈306 行**，≤500 硬门禁 ✅，净减 ~3 行）+ 新测试文件 `internal/service/multipart_claim_test.go`（~150 行） |
| 迁移 | **无**（I2：零 schema/迁移文件；I6：零新依赖——测试双只用 `errors`/`sync`/`testing`）· 无新配置项 · 无 adapter/OpenAPI/SDK/文档改动 |
| 门禁 | `make check` 全绿（gofmt/build/vet/test 基线已实证，§8）；AC-3 含 `-race` 专项命令（race 属独立 `make test-race` 目标，见 §9 处置） |
| 语义 | 成功路径逐位等价（E8：`total ≡ saved.Size`）；唯一行为差异在**失败路径**：记账失败 ⇒ 零持久足迹 + claim 释放 + 重试可完整重跑（§5 FM-1） |

---

## 1. 证据复验（requirements E1–E9，全部对照工作树复核）

| # | requirements 引证 | 复验结果（工作树） |
|---|---|---|
| E1 | `finishMultipartCompletion`（file_multipart_complete.go:201）：prepare 失败→release（:211-213）、`completeStoredMultipart` 失败→release（:215-218）、`saveMultipartObject` 失败→release（:220-223）、**`persistMultipartCompletion` 失败→裸 return（:225-227）** | ✅ **精确**。:201-229 逐行核对；:225-227 `if err := s.persistMultipartCompletion(...); err != nil { return repository.Object{}, err }` 是唯一不释放分支 |
| E2 | `persistMultipartCompletion`（:273）：`accountObjectUsage` 在最前（:280-282），其后 `CompleteIdempotencyKey`（warn-only，:284-286）、`DeleteUpload`（warn-only，:288-290）、`emit(EventCreated)`（:292） | ✅ **精确**。:280 实参为 `saved.Size`（设计据此改为 `total`，E8）；warn-only 语义确认 |
| E3 | `releaseMultipartCompletion`（:138）→ `DeleteIdempotencyKey`（idempotency.go:78）；`replayMultipartCompletion`（:94-113）：status≠completed 或非 2xx → `ErrPreconditionFailed: multipart completion is already in progress` | ✅ **精确**。:138-145、:94-113 逐行核对；`file.go:34 ErrPreconditionFailed` 确认 |
| E4 | `UsageAccountant` 接口（usage_accounting.go:20-27 `CheckQuota`/`Apply`）；`addTenantUsage`（:34）在注入后**独占路由**到 `Apply`（:35-39）；注入点 `file.go:125 WithUsageAccountant` | ✅ **精确**。接口 :29-32；路由独占确认（`usageAccountant != nil` 时 `repo.AddTenantUsage` 不可达——AC-1 委托双由此成立） |
| E5 | `ClaimIdempotencyKey`（idempotency.go:24）：`INSERT … 'in_progress' … ON CONFLICT (tenant_id, idem_key) DO NOTHING`（:32）；行已存在→`claimed=false`；repository 接口无读取方法 | ✅ **精确**。:24-58 逐行核对；`repository_interface.go:152-156` 仅 Claim/Complete/Delete/DeleteBefore 四方法（AC-2 用 `claimed==true` 语义，D3） |
| E6 | **（决定修复形态①）** `LocalStorage.CompleteMultipartWithOptions`（local_multipart.go:88-110）首次调用即消费上传：`delete(s.uploads, uploadID)`（:92，**merge 之前**）+ `defer os.RemoveAll(up.dir)`（:102，**出错路径也执行**） | ✅ **精确**。:88-110 逐行核对——`delete` 在 `validateMultipartSSEC`/`mergeParts` 之前；重试必死 `"unknown upload %s"`（:96-97）。S3/OSS/COS 后端的 multipart ID 同样完成即失效（provider 语义） |
| E7 | **（决定修复形态②）** `objectWriteUsage`（usage.go:37）从当前对象行读 `previousSize`（:44-48）；`deltas`（:30-36）：`newObject → (newSize,1)`，否则 `(newSize-previousSize,0)` | ✅ **精确**。失败尝试已提交 size=N 对象行 → 重试 `GetObject` 读到该行 → `newObject=false`、`previousSize=N` → delta=0 → "用量恰好等于对象大小一次"永不成立（除非失败尝试零持久足迹） |
| E8 | **（等价性）** `accountObjectUsage`（usage.go:51）只用 `usage.deltas(size)`；multipart 路径 `saved.Size ≡ total`：`buildObjectFromUpload` 在 `info.Size==0` 时回落 `total`（file_multipart.go:194-196）；`writePartsTo`（local_multipart.go:324-340）/`mergeEncrypted`（:285-288）产物恰为 Σ part 字节数；`prepareMultipartCompletion` 配额预检已用 `usage.deltas(total)`（:245） | ✅ **精确**。`PartRecord.Size` 经 `UploadPartFor` 的 `validateConsumedSize` 与真实写入字节对账（file_multipart.go:143-146）⇒ Σ part.Size = 落盘字节数 = `ObjectInfo.Size`；`info.Size==0` 回落分支兜底 ⇒ `saved.Size ≡ total` 恒成立（含 SSE-C 加密路径：`mergeEncrypted` 返回明文 total） |
| E9 | **（回归面）** `TestCompleteMultipartOverwriteAccountsOnlyUsageDelta`（usage_consistency_test.go:44-65，期望 3/1）、`TestMultipartRespectsBucketQuota`（:67-88，prepare 阶段拒绝）、`TestMultipartCompletionReplaySurvivesUploadDeletion`（multipart_correctness_test.go:87-102，重放读缓存）——记账前移后结果不变 | ✅ **精确**。三测试只断言最终用量/重放对象，不断言记账时机；`assertTenantUsage`（usage_consistency_test.go:221）与 `newQuotaTestSvc`（quota_test.go:15，现含 `WithAuthorizer(allowAllProvider{})`）可用 |

**问题陈述复核：** "记账失败 → claim 卡死 in_progress → 重试永久 412 → 对象已可见但事件/用量缺失"——✅ 成立且机理完整（E1+E2+E3+E5）；"仅释放 claim 不满足验收 1"——✅ 成立（E6+E7，见 §2.3）。

---

## 2. 代码级变更

### 2.1 `finishMultipartCompletion`（file_multipart_complete.go:201-229）——记账前移

**变更前（:204-227）：**

```go
	bcfg, usage, err := s.prepareMultipartCompletion(ctx, u, total)
	if err != nil {
		return repository.Object{}, s.releaseMultipartCompletion(ctx, scope, idemKey, err)
	}
	info, err := s.completeStoredMultipart(ctx, u, storageParts, opts)
	if err != nil {
		wrapped := fmt.Errorf("storage complete: %w", err)
		return repository.Object{}, s.releaseMultipartCompletion(ctx, scope, idemKey, wrapped)
	}
	saved, err := s.saveMultipartObject(
		ctx, buildObjectFromUpload(u, info, total, bcfg), bcfg,
	)
	if err != nil {
		return repository.Object{}, s.releaseMultipartCompletion(ctx, scope, idemKey, err)
	}
	if err := s.persistMultipartCompletion(ctx, u, saved, usage, idemKey); err != nil {
		return repository.Object{}, err          // ← 唯一不释放分支（E1）
	}
	return saved, nil
```

**变更后：**

```go
	bcfg, usage, err := s.prepareMultipartCompletion(ctx, u, total)
	if err != nil {
		return repository.Object{}, s.releaseMultipartCompletion(ctx, scope, idemKey, err)
	}
	// FR-1a：记账先于 storage 上传消费（E6）与对象行提交（file_multipart.go:221-236）。
	// 失败 ⇒ 零持久足迹 + 释放 claim；重试 = 全新 claim + 完整重跑（FR-2c）。
	if err := s.accountObjectUsage(ctx, u.TenantID, usage, total); err != nil {
		return repository.Object{}, s.releaseMultipartCompletion(ctx, scope, idemKey, err)
	}
	info, err := s.completeStoredMultipart(ctx, u, storageParts, opts)
	if err != nil {
		wrapped := fmt.Errorf("storage complete: %w", err)
		return repository.Object{}, s.releaseMultipartCompletion(ctx, scope, idemKey, wrapped)
	}
	saved, err := s.saveMultipartObject(
		ctx, buildObjectFromUpload(u, info, total, bcfg), bcfg,
	)
	if err != nil {
		return repository.Object{}, s.releaseMultipartCompletion(ctx, scope, idemKey, err)
	}
	if err := s.persistMultipartCompletion(ctx, u, saved, usage, idemKey); err != nil {
		return repository.Object{}, err          // 保留（最小 diff）；persist 恒返 nil 后实际不可达（FM-3）
	}
	return saved, nil
```

### 2.2 `persistMultipartCompletion`（file_multipart_complete.go:273-292）——瘦身

删除 :280-282 的 `accountObjectUsage` 调用及其 if 分支。函数体只剩 warn-only 三连（顺序与 warn 语义不变）：

```go
func (s *FileService) persistMultipartCompletion(
	ctx context.Context,
	u repository.Upload,
	saved repository.Object,
	usage objectWriteUsage, // D4：签名保留；FR-1c 后不再使用（Go 允许未用形参，gofmt/vet 无感）
	idemKey string,
) error {
	body, _ := json.Marshal(saved)
	if err := s.repo.CompleteIdempotencyKey(
		ctx, u.TenantID, idemKey, http.StatusOK, body, "application/json", nil,
	); err != nil {
		s.logger.Warn("cache multipart completion", "upload_id", u.ID, "err", err)
	}
	if err := s.repo.DeleteUpload(ctx, u.ID); err != nil {
		s.logger.Warn("delete completed multipart upload", "upload_id", u.ID, "err", err)
	}
	s.emit(ctx, saved, repository.EventCreated)
	return nil
}
```

### 2.3 为什么必须是这个形态（E6/E7 的排除法）

| 候选形态 | 为什么不行 |
|---|---|
| 仅释放 claim（方向标题字面） | E6：storage 上传已在失败尝试中消费 → 重试死在 `completeStoredMultipart`（"unknown upload"）；E7：对象行已提交 → 重试 `objectWriteUsage` 算出 delta=0 → 验收 1（用量恰好一次）数学上不可能 |
| 释放 claim + 补偿删除（回滚对象行/blob） | D2 明确不做：新机制（回滚路径本身需幂等/审计），超出"工作量 2"边界 |
| storage 完成幂等化（保存已完成记录） | D2 明确不做：改 storage 契约，五个后端 + 加密路径全部波及 |
| 记账留在 persist 但先删 claim | 顺序矛盾：`DeleteIdempotencyKey` 后 `CompleteIdempotencyKey` 会落 completed 行 → 重试走 replay 而非重跑，用量仍为 0 |
| 记账移入 `prepareMultipartCompletion` | D-scope：语义相同但使 prepare 职责膨胀；置于 finish 内与三个兄弟 release 路径并列更清晰（spec §5） |

⇒ **记账前移（FR-1）+ 失败统一释放（FR-2）是满足方向验收 1/2/3 的最小充分形态**；`total ≡ saved.Size`（E8）保证成功路径零行为差异。

---

## 3. 不变量论证（FR-1/FR-2/FR-3 映射）

| 需求 | 设计满足方式 | 论证 |
|---|---|---|
| FR-1a 记账先于 storage 消费与对象行提交 | §2.1 插入点：`prepare` → **`accountObjectUsage`** → `completeStoredMultipart` → `saveMultipartObject` | 记账失败时：`mergeParts` 未运行（无 blob）、`UpsertObject`/`InsertObjectVersion` 未运行（无对象行）、`CompleteMultipartWithOptions` 未调用（storage 上传完好）→ **零持久足迹**；重试 = 全新 claim + 完整重跑 |
| FR-1b 记账参数 `total` | `accountObjectUsage(ctx, u.TenantID, usage, total)` | E8：`saved.Size ≡ total`（`buildObjectFromUpload` 回落 + `writePartsTo` 求和 + `validateConsumedSize` 对账）；与预检 `usage.deltas(total)`（:245）一致 |
| FR-1c persist 瘦身 | §2.2：只留 `CompleteIdempotencyKey`（warn）→ `DeleteUpload`（warn）→ `emit`；签名/返回值不变 | 恒返 `nil` ⇒ :225-227 的裸 return 分支实际不可达（FM-3）；warn-only 语义与事件契约（AGENTS.md §2.4）不动 |
| FR-1d 配额预检先行 | 插入点在 `prepareMultipartCompletion`（含 `preflightQuota`/`preflightBucketQuota`，:231-258）之后 | check-then-act 顺序不变；`TestMultipartRespectsBucketQuota`（E9）行为不变 |
| FR-2a 失败统一释放 | 记账失败分支与三兄弟分支同型（`return repository.Object{}, s.releaseMultipartCompletion(...)`） | `ClaimIdempotencyKey` 成功后**所有**错误路径 4/4 经 `DeleteIdempotencyKey`（:138-145） |
| FR-2b 成功路径不变 | persist 顺序与内容不变（除记账外） | `CompleteIdempotencyKey` 落 completed + 缓存体 → 重试 replay（`TestMultipartCompletionReplaySurvivesUploadDeletion` 锁定） |
| FR-2c 重试语义 | 记账失败释放 → 重试全新 claim（`claimed==true`，E5）→ FR-1a 保证无残留 → 完整重跑 | 成功时用量恰好一次 + `EventCreated` 恰好一次（AC-1 断言 5/6） |
| FR-3 安全不变量 | 零持久足迹（FR-1a）+ 既有 `DeleteIdempotencyKey`（无新 repo 方法）+ 预检顺序（FR-1d）+ 412 重放语义不动 + 零 schema/配置/adapter/telemetry 变更 | §5 FM 表 + §6 迁移节 + §9 范围节 |

---

## 4. API 变化与兼容性约束

1. **公开 API 零变化**：`Repository`（repository_interface.go）、`Storage`、`UsageAccountant`（usage_accounting.go:29）、`FileService` 全部方法签名不变；无新导出符号；无配置项；无 adapter/handler/OpenAPI/SDK/文档改动。
2. **成功路径逐位等价**：唯一调用差异是 `accountObjectUsage` 实参从 `saved.Size` 变为 `total`——E8 证明恒等；时序差异（记账提前）在成功路径不可观测（最终态一致：对象行、用量、completed 行、upload 删除、事件）。
3. **失败路径语义变化（有意为之，§5）**：记账失败从"对象在/用量缺/claim 卡死"变为"零足迹/claim 释放/可重试"。storage 完成失败从"用量 0/卡死"变为"用量 +N/快速失败"（D2 接受，见 FM-2）。
4. **并发兼容**：`completeMultipart` 的 claim/replay 竞态语义不变（FM-5）；`ClaimIdempotencyKey` 的 `ON CONFLICT DO NOTHING` 未被触碰。
5. **I1/I2/I3/I4/I5/I6 全不触发**：无 SQL 改动（I1）、无迁移（I2）、无存储 key 布局改动（I3）、无中间件链改动（I4）、无 flag 门禁改动（I5）、无新依赖（I6）。

---

## 5. 失败模式与处置

| # | 场景 | 修复前 | 修复后 | 处置 |
|---|---|---|---|---|
| FM-1 | **记账失败**（`AddTenantUsage` 瞬时 DB 错误 / 会计方 `Apply` 瞬时失败，或 apply 时配额拒绝） | claim 卡死 `in_progress`；对象行+blob 已提交；事件永不发；重试永久 412；运维手工清行 | claim 释放；**零持久足迹**；重试全新 claim 完整重跑；成功时用量/事件恰好一次 | **本方向修复目标**（AC-1/AC-2 直接断言）。apply 时配额拒绝同样走释放——预检（FR-1d）会在重试时重新拒绝，不产生卡死 |
| FM-2 | storage 完成失败 / 对象行保存失败（记账已成功） | 上传已被消费（E6）、用量 0、重试死于 "unknown upload"（同样永久失败） | 用量 +N 已入账、无对象行（漂移方向反转）；重试：有配额时被预检 double-count 拒绝（快速失败，不再重复入账）；无配额时再入账 +N 后死于 "unknown upload" | **D2 明确接受**（spec §5）：补偿性删除、幂等 storage 完成均不做。孤儿 blob 由 reconcile scrub GC 回收；用量漂移由运维校正（§6） |
| FM-3 | persist 阶段失败 | `accountObjectUsage` 是唯一错误源 → 返回 5xx 且 claim 卡死 | persist 恒返 `nil`（warn-only + emit 非致命）→ 该错误路径整体消失 | FR-1c 推论；`if err := s.persistMultipartCompletion(...)` 保留（最小 diff，恒 false） |
| FM-4 | `releaseMultipartCompletion` 的 `DeleteIdempotencyKey` 失败 | warn 后返回原始错误；claim 残留 → 重试 412 | 不变（预存行为） | 残留行由幂等键 TTL GC（`DeleteIdempotencyKeysBefore`，reconcile retention job）最终回收；与修复前一致 |
| FM-5 | 并发重复 CompleteMultipart | 一个 claim 胜出，其余 replay（completed）或 412（in_progress） | 不变 | 无变更 |
| FM-6 | 进程崩溃于 request 中途 | `in_progress` 残留，TTL GC 回收；对象/用量状态取决于崩溃点 | 不变（崩溃不执行 release 是既有语义；崩溃点窗口因记账前移而收窄——记账后到 persist 前无 DB 失败源） | 无变更 |

---

## 6. 迁移与运维步骤

1. **无迁移**：零 schema 变更（I2）、零配置项、零依赖（I6）；单 binary 滚动部署即可，行为差异仅在失败路径。
2. **存量卡死行清理（部署前一次性运维，仅当生产存在 FM-1 残留）**：
   ```sql
   DELETE FROM idempotency_keys
    WHERE idem_key LIKE '_mp_complete:%' AND status = 'in_progress';
   ```
   安全论证：`replayMultipartCompletion`（:94-113）只读取 `completed` 行（status 过滤），删除 `in_progress` 行不影响任何重放；受影响 upload 的对象已提交者可正常读取，`uploads` 行由 upload GC 或 `DeleteUpload` 回收。
3. **FM-2 漂移校正（如发生）**：对象缺、用量多 → 用 admin 面调整用量；孤儿 blob 由 reconcile scrub（`RECONCILE_DELETE_ORPHAN_BLOBS` opt-in）回收。
4. **无回滚特殊处理**：本变更可干净 revert（两个 hunk）；revert 后回到修复前语义（含缺陷），不产生新不一致。

---

## 7. 验收映射（方向验收 1-3 全部测试化）

新文件 `internal/service/multipart_claim_test.go`（package `service` 白盒，~150 行）。测试双与断言助手全部复用既有基建：`newQuotaTestSvc`（quota_test.go:15）、`WithUsageAccountant`（file.go:125）、`assertTenantUsage`（usage_consistency_test.go:221）、`multipartCompleteKey`（`_mp_complete:<uploadID>`，file_multipart_complete.go:79）。不引入断言框架（I6）。

```go
// failOnceAccountant —— AC-1/AC-2 测试双：CheckQuota 恒通过；
// Apply 第 1 次返回哨兵错误，此后委托 repo.AddTenantUsage 并计数。
// 委托是注入后用量可见的唯一途径（usage_accounting.go:35-39 独占路由）。
type failOnceAccountant struct {
	mu         sync.Mutex
	repo       repository.Repository
	applyCalls int
}

func (f *failOnceAccountant) CheckQuota(context.Context, string, repository.TenantQuota, int64, int64) error {
	return nil
}

func (f *failOnceAccountant) Apply(ctx context.Context, m service.UsageMutation) (repository.TenantQuota, error) {
	f.mu.Lock()
	f.applyCalls++
	first := f.applyCalls == 1
	f.mu.Unlock()
	if first {
		return repository.TenantQuota{}, errors.New("transient accounting failure")
	}
	return f.repo.AddTenantUsage(ctx, m.TenantID, m.DeltaBytes, m.DeltaObjects)
}
```

### AC-1 — `TestMultipartCompleteClaimReleasedOnUsageFailure`（方向验收 1）

| 步 | 操作 | 断言 |
|---|---|---|
| 1 | `svc, repo := newQuotaTestSvc(t)`；`svc.WithUsageAccountant(failOnce{repo})` | 注入成功 |
| 2 | `InitMultipart("","","ac1.bin")` + `UploadPart(1, "12345", 5)` | 无错 |
| 3 | 首次 `CompleteMultipart` | error 非 nil 且 `!errors.Is(err, ErrPreconditionFailed)`（排除重放路径） |
| 4 | 立即重试 `CompleteMultipart` | 成功；`obj.Size == 5`；`svc.Get(ctx,"","","ac1.bin")`（file_get.go:24，返回 `(io.ReadCloser, repository.Object, error)`）读回 `"12345"` |
| 5 | `assertTenantUsage(t, repo, 5, 1)` | **最终用量恰好等于对象大小一次** |
| 6 | `failOnce.applyCalls == 2` | 第 1 次失败 + 第 2 次成功 = 用量恰好入账一次的直接证据 |

### AC-2 — `TestMultipartCompleteNoStuckClaimAfterUsageFailure`（方向验收 2）

| 步 | 操作 | 断言 |
|---|---|---|
| 1 | 独立 upload（`ac2.bin`）：`InitMultipart` + `UploadPart(1, body, 5)` | 无错 |
| 2 | 首次 `CompleteMultipart` | error（同 AC-1 步 3） |
| 3 | **失败后、重试前**：`repo.ClaimIdempotencyKey(ctx, "default", "_mp_complete:"+upload.ID, "", "")` | **`claimed == true`**（E5：`ON CONFLICT DO NOTHING` 下仅当行不存在才返回 true）——无 `in_progress` 残留 |
| 4 | `repo.DeleteIdempotencyKey(...)`（防御性 no-op）后重试 `CompleteMultipart` | 成功（重试语义同 AC-1，用量断言不重复） |

### AC-3 — 门禁（方向验收 3，逐条可执行）

```bash
go test ./internal/service -run Multipart        # 既有 multipart 测试全部通过（含新增两个）
go test ./internal/service -run 'Usage|Quota'    # 记账前移回归面（E9 四测试）
go test -race ./internal/service -run 'Multipart|Usage'  # 新增 + 既有无竞态
gofmt -l internal/service                        # 期望无输出
go vet ./internal/service                        # 期望无输出
make check                                       # 硬门禁：gofmt/build/vet/test/cli-check 全绿
```

---

## 8. 门禁合规（已实证基线）

| 门禁 | 状态 |
|---|---|
| `gofmt` | 变更 hunk 均 house 风格；基线 `gofmt -l internal/service` 空（变更前已验） |
| `go build ./...` | ✅ 实测通过（本设计验证时运行） |
| `go vet ./...` | 无新告警面（零 SQL、零新包、未用形参不触发 vet） |
| `go test ./...` | ✅ `go test ./internal/service -run 'Multipart|Usage|Quota'` 实测 `ok`（4.96s）；全包基线不受影响 |
| 单文件 ≤500 行 | `file_multipart_complete.go` 309 → 309 行（删 3 增 3，净 0）✅；`multipart_claim_test.go` ~150 行 ✅ |
| 圈复杂度/函数长度 | 无新函数（仅插入 3 行调用 + 删除 3 行）；`finishMultipartCompletion` 仍 ≤50 行 ✅ |
| I1-I6 | 全不触发（§4.5） |

### 8.1 实证（本设计验证时运行，红/绿两面）

设计定稿前已把 §2 变更**临时应用到工作树**并实测：

| 验证 | 结果 |
|---|---|
| 绿面（补丁已应用） | `gofmt -l internal/service` 空 · `go build ./...` ✅ · `go vet ./internal/service` ✅ · `go test ./internal/service -run 'Multipart|Usage|Quota'` ✅（10.8s）· **§7 的 AC-1/AC-2 测试双原样跑通**（`TestScratchAC1…`/`TestScratchAC2…` 各 0.5s PASS，断言含 `applyCalls==2`、`claimed==true`、用量 5/1） |
| 红面（补丁已还原） | 同一测试：AC-1 死于 `retry complete: precondition failed: multipart completion is already in progress`；AC-2 死于 `claimed=false, stuck in_progress row exists for _mp_complete:<id>` —— 与 spec §1 缺陷机理逐字一致 |
| 还原 | 临时补丁与 scratch 测试均已删除，`file_multipart_complete.go` 回到 HEAD（`git status` 确认）；工作树仅剩本设计文档 |

---

## 9. 先前尝试与发现处置（BEFORE-finalizing 检查）

### 9.1 本管线 run（release-multipart-completion-idempotency-claim-w-45806754）

`DECISIONS.md` 仅一条：**requirements 阶段 PASS**（2026-08-07 00:25:03），无 design-gate 记录（本设计是首个 design 交付物）。requirements 工件（`artifacts/requirements-10762e10/requirements.md`）即 `docs/requirements/multipart-complete-claim-release-v1.md`，其 E1-E9 已在 §1 逐条复验，零漂移；FR-1/2/3、AC-1/2/3、D1-D5 为本设计直接依据。**无未决发现。**

### 9.2 同批次 sibling（make-metadata-read-modify-write-atomic-lost-upda-c1a3b497，design_gate **FAIL**）

该 run 的 4 条 blocking findings 逐一处置（gate 会复查，故每条给证据）：

| Sibling finding | 处置 | 证据 |
|---|---|---|
| **F1**：服务层 RMW 漏洞在 `PutMetadata`/`DeleteMetadata`（file_features.go:257-276,310-321），设计未覆盖 | **明确不适用（out of scope）**：那是该方向（metadata RMW 原子化）自身的范围缺陷，属其 design 必须修订的内容，与本方向（multipart 完成记账失败）零交集。本设计改动文件 `file_multipart_complete.go` + 新测试文件，`git status` 确认 `file_features.go`/`sql_objects_maint.go` 不在本方向改动集；该方向需在其自身 run 中修订设计并重跑 gate | 本设计 §2（改动面）；`git status`（工作树未列出上述文件） |
| **Required #3**：FR-2 契约表 + `jsonPath` 转义表缺失 | **不适用**：该要求针对其 SQL `json_patch`/`json_remove` 变更的方言/转义契约。本方向**零 SQL、零 repository 变更**（I1），无 jsonPath 面。等价物已提供：本设计 §5 FM 表、§7 验收断言表 | 本设计 §4.5（零 SQL）、§5、§7 |
| **Required #4**：`-race` 未入 `make check`（AC-1/2 用 `-race` 而 gate 跑普通 `go test`） | **显式拒绝（附证据）**：AGENTS.md §0 定义 `make check` = gofmt/build/vet/test（`test: go test ./...`，Makefile:18）；`-race` 是独立目标 `make test-race`（Makefile:106-111）。把 race 并入 `make check` 属 CI 行为变更，超出本方向范围（spec §5 明确"无 Makefile/CI 变更"）。**替代保证**：AC-3 显式执行 `go test -race ./internal/service -run 'Multipart|Usage'`（acceptance 阶段会运行）；新增测试单线程顺序执行 + 测试双 `sync.Mutex` 保护计数，race-clean by construction | Makefile:18,106-111；spec §5 |
| **Required #5**：`metadata - $1` 未钉 `::text` | **不适用**：该 finding 针对其 Postgres `jsonb` 运算符类型推断。本方向不新增任何 SQL（I1） | 本设计 §4.5 |

### 9.3 其它同名 sibling

`docs/auto/runs/` 中 multipart 相关目录仅有本 run 一个（`ls docs/auto/runs | grep -i multipart`）；无其它设计门禁记录需要处置。

---

## 10. 范围边界与明确不做（继承 spec §5 的 D1-D5，附设计侧理由）

| 决策 | 理由（设计侧复核） |
|---|---|
| **D1：claim-release-only 不足，必须同步前移记账** | E6/E7 排除法（§2.3）：不前移则重试死于 "unknown upload" 且 delta=0，验收 1 数学上不可满足 |
| **D2：接受镜像漂移（storage/save 失败时用量 +N 无对象）** | FM-2：修复前该场景同样永久失败（上传已消费、用量 0）；前移只是漂移方向反转，不引入补偿删除/幂等完成等新机制 |
| **D3：不新增 repository 方法** | AC-2 用 `ClaimIdempotencyKey` 返回 `claimed==true` 证明行不存在（E5）；`idempotency_keys` 表零 schema 变更（I2） |
| **D4：`persistMultipartCompletion` 签名保留（恒返 nil）** | 最小 diff；`usage` 形参未用但合法（Go 允许未用形参，gofmt/vet 无感）；warn-only 与事件契约不动 |
| **D5：记账用 `total` 而非 `saved.Size`** | E8 恒等证明；与配额预检 `usage.deltas(total)`（:245）一致 |
| 不做：配额 check-then-act TOCTOU、DeleteBucket 快照竞态 | 独立分析方向，spec §5 明确排除 |
| 不做：`prepareMultipartCompletion` 内嵌记账 | 职责膨胀；finish 内与三兄弟 release 路径并列更清晰 |
| 不做：Makefile/CI 变更（含 `-race` 入 check） | §9.2 Required #4 处置 |
| 不做：文档/配置/telemetry/适配层变更 | 无新配置、无行为契约变化；OpenAPI/SDK/`docs/configuration.md` 均不涉及 |

**交付物：** 本设计 + §2 变更（实现阶段落地）+ §7 两个测试（`multipart_claim_test.go`）。实现完成后按 §7 AC-3 命令与 `make check` 验证。
