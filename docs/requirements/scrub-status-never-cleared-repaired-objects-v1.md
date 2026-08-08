# 方向：`_aero_scrub_status=corrupt` 永不清理，修复后的对象被永久锁定（`internal/reconcile`）

> **模块：** `internal/reconcile`（scrub.go + job.go）· **来源分析：** `docs/auto/analyses/internal-reconcile-7a29db11.json`（方向 1/3）· **日期：** 2026-08-07
> **评分：** 价值 8 / 风险降低 8 / 工作量 2 / 置信度 9
> **本文所有代码引用均已对照仓库逐行验证**（`make check` 基线：SQLite + local FS + AI off）。
>
> **修订记录（验证后回写）：**
> - **方向文引用的行号有两处偏移，符号与行为完全属实**：corrupt 标记写入在 `scrub.go:93-94`（分析引 :92/:94）；`scrubScanned, _ := j.scrubAll(...)` 在 `job.go:116`（分析引 :64）。`file_crud.go:291`、`sql_objects_maint.go:268` 精确。
> - **补充验证（问题比方向文所述更广）：** `_aero_scrub_status` 的生产写入者经全仓 grep 确认**仅 `scrub.go:94` 一处**、且只写 `"corrupt"`；`checkCorrupt` 调用点共 4 处，不止 Get/Stat：`file_get.go:48`（Get）、`file_get.go:147`（Stat）、`file_features.go:81`（feature flag 读取）、`file_multipart_copy.go:112`（multipart copy 源对象）。即 corrupt 标记同时阻断**读、stat、特性开关、复制**，修复前对象完全不可用。
> - **补充验证（关键使能项）：** 仓库层已有 `DeleteObjectMetaKey`（接口 `repository_interface.go:44`、实现 `sql_objects_maint.go:358`；无 key 时 no-op、按 key 删除而非置空）——本方向**无需新增任何 Repository API**，修复 = scrubObject 完好分支一行调用。
> - **telemetry 澄清（scope 收敛）：** `IncScrubResult(ctx, status)`（`telemetry/metrics.go:266-269`）已按 status 标签聚合 `mScrubTotal`（"ok"/"corrupt"）——corrupt 计数的**指标**可见性已存在；方向文所称「telemetry 丢失」实际只发生在 `sweep` 汇总**日志**（`job.go:116` 丢弃 corrupt 返回值、:120 日志无该字段）。本方向只补日志字段，**不新增指标**。
> - **验收 3 项全部保留**，落为 AC-1..AC-3（§4）；AC-1 的「恢复 blob 使 MD5 匹配」步骤可行：镜像 `scrub_test.go:26` 既有模式，`store.Put(ctx, object.StorageKey, 原始内容, …)` 直写 local storage 即可复原。

---

## 1. 问题陈述

`scrubObject`（`internal/reconcile/scrub.go`）是 `_aero_scrub_status` 元数据键的唯一写入者，且只写入 `"corrupt"`（:94）；完好分支（:89-91）仅 `IncScrubResult(ctx, "ok")` 后返回，**任何代码路径都不清除该标记**。一旦对象被标记：

1. `internal/service/file_crud.go:290-293` 的 `checkCorrupt` 在 `Metadata["_aero_scrub_status"] == "corrupt"` 时返回 `ErrObjectCorrupt`（哨兵定义 `file.go:39`），且被 Get / Stat / feature-flag / multipart-copy 共 4 处调用——对象**永久不可读**。
2. 若 blob 事后被带外修复（备份恢复、后端自愈），下一轮 scrub 会重新验证 MD5 并发现完好——但标记仍在，对象依旧被锁，**无任何自动化补救**（只能手工改 DB/元数据，或重新上传新版本/新行）。
3. 附带可见性缺口：`Job.sweep`（`job.go:116`）`scrubScanned, _ := j.scrubAll(...)` 丢弃 corrupt 计数，:120-127 的 `reconcile sweep done` 汇总日志无 corrupt 字段——每轮损坏率的聚合视图缺失（单对象 warn 日志无法聚合）。

### 触发场景（真实工作流）

1. 对象 blob 因后端故障/人为误写而损坏；scrub 标记 `_aero_scrub_status=corrupt`，Get/Stat 开始返回 `ErrObjectCorrupt`。
2. 运维从备份恢复 blob（内容与 `_aero_content_md5` 一致），期望对象自动恢复可读。
3. 实际：下一轮 scrub 校验通过（`IncScrubResult("ok")`）但**不清理标记**，对象继续被锁；运维只能手工 SQL 或重传新版本，且 sweep 日志/指标里看不到这次损坏曾经发生过（计数被丢弃）。

---

## 2. 现状与代码证据（已逐条验证）

| # | 证据 | 验证结果 |
|---|------|---------|
| E1 | `internal/reconcile/scrub.go:93-94` — `IncScrubResult(ctx, "corrupt")` + `SetObjectMetaKey(…, "_aero_scrub_status", "corrupt")`；全仓 grep（非测试）确认 `_aero_scrub_status` 写入者仅此一处 | ✅ 与引用一致（行号 :92→:93 微偏）；`repository/sql_objects_maint.go:268` 为 `SetObjectMetaKey` 实现（精确） |
| E2 | `internal/reconcile/scrub.go:89-91` — 完好分支：`bytes.Equal(computed, expected)` → `IncScrubResult(ctx, "ok")` → `return nil`，**不触碰标记** | ✅ 与引用一致——这是修复插入点 |
| E3 | `internal/reconcile/job.go:116` — `scrubScanned, _ := j.scrubAll(ctx, t, j.scrub)`（corrupt 返回值丢弃）；`job.go:55` `WithScrub` 存在 | ✅ 与引用一致（行号 :64→:116 偏移，符号精确） |
| E4 | `internal/reconcile/job.go:120-127` — `reconcile sweep done` 汇总日志字段：`scanned / orphan_rows / orphan_blobs / orphan_blobs_deleted / delete_enabled / duration_ms`，**无 scrub 字段** | ✅ 与引用一致 |
| E5 | `internal/service/file_crud.go:290-293` — `checkCorrupt`：`Metadata["_aero_scrub_status"] == "corrupt"` → `ErrObjectCorrupt`；调用点 4 处：`file_get.go:48`（Get）、`file_get.go:147`（Stat）、`file_features.go:81`、`file_multipart_copy.go:112`；哨兵 `file.go:39` | ✅ 与引用一致（:291 精确）；调用点**比方向文多 2 处**（补充验证） |
| E6 | `internal/repository/sql_objects_maint.go:268` — `SetObjectMetaKey`（读 metadata → 改写 → 单条 UPDATE，`deleted_at IS NULL` 行） | ✅ 行号精确 |
| E7 | **使能项**：`repository_interface.go:44` `DeleteObjectMetaKey(ctx, tenant, bucket, key, metaKey)` + `sql_objects_maint.go:358` 实现（无 key 时 no-op；删 key；行不存在返回 `ErrNotFound`） | ✅ 已存在，**无需新增 API**；`FileService` 已有使用先例（`file_features.go:333`） |
| E8 | 测试基建：`scrub_test.go:26` `TestScrubCorruptionRemovesSearchChunks`（seed=tamper=metadata 断言模式，`store.Put(ctx, object.StorageKey, …)` 直写可复原 blob）；`job_test.go:17` `openTestRepo`、`:70` `newSilentLogger`；`job.go:55` `WithScrub(enabled, batch)` | ✅ 与引用一致，AC-1/AC-2 可镜像 |

### 缺口机理

```
对象损坏 → scrubObject: SetObjectMetaKey("_aero_scrub_status"="corrupt")   [scrub.go:94，唯一写入者]
  → checkCorrupt 命中 → Get/Stat/feature/multipart-copy 全部 ErrObjectCorrupt  [file_crud.go:291]
对象修复（带外恢复，MD5 重新匹配）
  → scrubObject: bytes.Equal ✓ → IncScrubResult("ok") → return nil             [scrub.go:89-91，不清理]
  → 标记永存，对象永久锁定 ✗
sweep 汇总：scrubScanned, _ := j.scrubAll(...)                                 [job.go:116，corrupt 丢弃]
  → "reconcile sweep done" 日志无 corrupt 字段 ✗
```

---

## 3. 需求规格

### FR-1：scrubObject 在 MD5 校验通过时清除 corrupt 标记（`internal/reconcile/scrub.go`）

- 完好分支（`bytes.Equal(computed, expected)`，:89-91）在 `IncScrubResult(ctx, "ok")` 之后、`return nil` 之前新增：
  - **仅当** `obj.Metadata["_aero_scrub_status"] == "corrupt"` 时执行 `j.repo.DeleteObjectMetaKey(ctx, obj.TenantID, obj.Bucket, obj.Key, "_aero_scrub_status")`。完好路径是每轮扫描的热路径（每对象每轮），条件守卫避免无谓的 DB 往返。
  - 成功：`j.logger.Info("scrub: repaired object cleared", "tenant", …, "bucket", …, "key", …, "storage_key", …)` + `telemetry.IncScrubResult(ctx, "repaired")`——复用既有 status 标签指标（`metrics.go:266-269`），使修复事件可聚合，回应方向文「per-object 日志不可聚合」的关切；**不新增指标**。
  - 失败（含行消失 `ErrNotFound`）：`j.logger.Warn("scrub: failed to clear corrupt flag", …, "err", …)`，**仍返回 nil**——内容完整性已确认，不误报 corrupt；标记保留、对象继续锁定（安全默认：清理失败绝不静默放行，下次 scrub 重试）。
- **corrupt 路径零改动**（:93-97）：仍写 `"corrupt"`、仍 `cleanObjectChunks`、仍返回错误计入 corrupt 计数。
- **单一写入者纪律保持**（对齐 AGENTS.md I3 的反解析/单一权威精神）：`_aero_scrub_status` 仍只由 scrub 写入与清除，**不做旁路**（不在 restore/upload 路径清标记）。语义定为「最近一次 scrub 的验证结果缓存」：restore 出的带标记软删对象，由下一轮 scrub 重新验证后自然清除。

### FR-2：`Job.sweep` 汇总日志报告 scrub corrupt 计数（`internal/reconcile/job.go`）

- `job.go:116` 改为 `scrubScanned, scrubCorrupt := j.scrubAll(ctx, t, j.scrub)`；`scanned += scrubScanned` 不变。
- `job.go:120-127` 的 `reconcile sweep done` 日志新增字段 `"scrub_corrupt", scrubCorrupt`；**恒携带**（scrub 关闭时 `scrubAll` 返回 0,0 → 字段值为 0，日志形状稳定）。
- per-object warn 日志（`scrub.go:98-100`）与 `IncScrubResult` 计数不变。
- 不新增 telemetry 指标（`mScrubTotal{status="corrupt"}` 已聚合 corrupt 数；本方向只补日志字段）。

### 非功能约束

- `make check` 全绿（gofmt / build / vet / test）；改动文件 ≤ 500 行、单函数 ≤ 50 行（AGENTS.md §0）。
- 纯 stdlib，无新 `go.mod` 依赖（I6）；无迁移（I2）；不改中间件链（I4）；不改存储 key 布局（I3）；不触碰 opt-in 门禁（I5）。
- 不改 `checkCorrupt` / `ErrObjectCorrupt` 语义、不改 `scrubAll` 签名、不改 `SetObjectMetaKey` / `DeleteObjectMetaKey` 实现、不改 corrupt 路径行为。

---

## 4. 验收标准（可测试）

> 测试基建（已验证）：`scrub_test.go:26`（seed/tamper/恢复模式）、`job_test.go:17` `openTestRepo`、`job_test.go:70` `newSilentLogger`、`job.go:55` `WithScrub`。AC-2 需内联一个 capture logger（`slog.New(slog.NewTextHandler(&buf, nil))`，TextHandler 将 int 属性渲染为 `scrub_corrupt=1`）。

### AC-1 单元：`TestScrub_ClearsFlagWhenIntact`（`internal/reconcile/scrub_test.go`，镜像 `TestScrubCorruptionRemovesSearchChunks` 的 seed/tamper 模式）

```go
func TestScrub_ClearsFlagWhenIntact(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(t.TempDir(), "objects")})
	if err != nil { t.Fatalf("storage: %v", err) }
	fileService := service.NewFileService(store, repo, nil).WithAuthorizer(allowAllAuthz{})
	body := "trusted content"
	digest := md5.Sum([]byte(body))
	object, err := fileService.Put(ctx, "", "", "scrubbed.txt", strings.NewReader(body), int64(len(body)),
		service.PutOptions{ContentMD5: base64.StdEncoding.EncodeToString(digest[:])})
	if err != nil { t.Fatalf("put: %v", err) }
	job := New(repo, store, 0, false, 0, []string{"default"}, newSilentLogger()).
		WithChunkCleaner(repositoryChunkCleaner{repo: repo})

	// ① 篡改 blob → scrub → 标记 = corrupt
	if _, err := store.Put(ctx, object.StorageKey, strings.NewReader("altered content"),
		int64(len("altered content")), storage.PutOptions{}); err != nil {
		t.Fatalf("tamper storage: %v", err)
	}
	if err := job.scrubObject(ctx, object); err == nil { t.Fatal("expected corruption result") }
	reloaded, err := repo.GetObjectByID(ctx, object.ID)
	if err != nil { t.Fatalf("reload: %v", err) }
	if reloaded.Metadata["_aero_scrub_status"] != "corrupt" { t.Fatalf("flag not set: %v", reloaded.Metadata) }

	// ② 恢复 blob（内容与 _aero_content_md5 一致）→ 再次 scrub → 标记被清除
	if _, err := store.Put(ctx, object.StorageKey, strings.NewReader(body), int64(len(body)),
		storage.PutOptions{}); err != nil {
		t.Fatalf("restore blob: %v", err)
	}
	if err := job.scrubObject(ctx, object); err != nil { t.Fatalf("scrub after repair: %v", err) }
	reloaded, err = repo.GetObjectByID(ctx, object.ID)
	if err != nil { t.Fatalf("reload: %v", err) }
	if _, still := reloaded.Metadata["_aero_scrub_status"]; still {
		t.Fatalf("flag not cleared: %v", reloaded.Metadata)
	}

	// ③ Get 不再返回 ErrObjectCorrupt（今日此步必败）
	rc, err := fileService.Get(ctx, "", "", "scrubbed.txt")
	if err != nil { t.Fatalf("Get after repair: %v", err) }
	rc.Close()
}
```

### AC-2 单元：`TestJobSweep_ReportsScrubCorruptCount`（`internal/reconcile/job_test.go`）

```go
func TestJobSweep_ReportsScrubCorruptCount(t *testing.T) {
	// seed：repo.CreateBucket(ctx, "default", "default")（镜像 job_test.go:86）
	//       + 1 个带 ContentMD5 的对象 + 篡改其 blob（镜像 AC-1 的 ①）
	// capture logger：
	//   var buf bytes.Buffer
	//   logger := slog.New(slog.NewTextHandler(&buf, nil))
	// job := New(repo, store, 0, false, 0, []string{"default"}, logger).
	//        WithScrub(true, 100).WithChunkCleaner(repositoryChunkCleaner{repo: repo})
	// job.sweep(ctx)
	// 断言 buf.String() 含 "reconcile sweep done" 且含 "scrub_corrupt=1"
	// 对照（防回归）：不篡改对象时同流程断言 "scrub_corrupt=0"（字段恒携带）
}
```

### AC-3 门禁

- `go test ./internal/reconcile/` 全绿；`gofmt -l` 无输出、`go build ./...`、`go vet ./...` 通过（`make check`）。
