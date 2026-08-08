# 设计：scrub 完好分支清除 `_aero_scrub_status=corrupt` + sweep 汇总日志上报 corrupt 计数

> **模块：** `internal/reconcile`（scrub.go + job.go）· **上游：** `docs/requirements/scrub-status-never-cleared-repaired-objects-v1.md`（本管线 requirements 阶段，PASS）· **基线：** HEAD `acfaaf4`（工作区有未提交的其它 campaign 改动，与本方向无交集）
> **本设计所有代码引用均对照 HEAD 逐行重新验证**（requirements 的证据视为不可信声明，全部复验，见 §1）。

---

## 0. 设计总览

| 项 | 决策 |
|---|---|
| 方案 | FR-1：`scrubObject` 完好分支在 `IncScrubResult("ok")` 后调用新私有方法 `clearCorruptFlag`（守卫 `Metadata["_aero_scrub_status"]=="corrupt"` → `repo.DeleteObjectMetaKey`）· FR-2：`Job.sweep` 累计 corrupt 计数并写入 `reconcile sweep done` 日志新字段 `scrub_corrupt` |
| API 变化 | **零**：无新 Repository API（`DeleteObjectMetaKey` 已存在，接口 `repository_interface.go:44`、实现 `sql_objects_maint.go:358`）；`scrubAll` 签名不变（本就返回双值）；`Job`/`FileService` 公开面不变 |
| 改动文件 | `internal/reconcile/scrub.go`（+~23 行，101→~124）、`internal/reconcile/job.go`（+4 行，247→~251）、`scrub_test.go`（+1 测试，95→~185）、`job_test.go`（+1 测试，~105 行）；均 ≤500 行 |
| 迁移 | **无**（无 schema/接口变更，I2）；部署后下一轮 scrub 自动自愈存量被锁对象 |
| 门禁 | `make check` 全绿；无新依赖（I6）；不碰中间件链（I4）、存储 key 布局（I3）、opt-in 门禁（I5）、SQL 占位符（I1，无新增 SQL） |
| 语义 | `_aero_scrub_status` = 「最近一次 scrub 的验证结果缓存」；唯一写入/清除者仍是 scrub（单一写入者纪律，对齐 I3 精神） |

---

## 1. 证据复验（requirements 的 5 条引用 + 支撑声明，全部对照 HEAD 复核）

| # | requirements 声明 | 复验结果（HEAD `acfaaf4`） |
|---|---|---|
| E1 | `scrub.go:93-94` 唯一生产写入者，只写 `"corrupt"` | ✅ `scrub.go:94` `SetObjectMetaKey(…,"_aero_scrub_status","corrupt")`；全仓 grep（非测试）仅此一处写入、`file_crud.go:291` 一处读取 |
| E2 | `scrub.go:89-91` 完好分支 `bytes.Equal(computed, expected)` → `IncScrubResult("ok")` → `return nil`，不触碰标记 | ✅ `:89-91` 逐行一致；这是插入点 |
| E3 | `job.go:116` `scrubScanned, _ := j.scrubAll(...)` 丢弃 corrupt 计数 | ✅ 精确；`WithScrub` 在 `:55` |
| E4 | `job.go:120-127` sweep 汇总日志无 scrub 字段 | ✅ 字段集：`scanned / orphan_rows / orphan_blobs / orphan_blobs_deleted / delete_enabled / duration_ms` |
| E5 | `file_crud.go:291` `checkCorrupt`；调用点 4 处 | ✅ `:290-293` 精确；调用点 `file_get.go:48`（Get）、`:147`（Stat）、`file_features.go:81`、`file_multipart_copy.go:112` |
| E6 | `sql_objects_maint.go:268` `SetObjectMetaKey` | ✅ 精确（`sqlStore` 实现） |
| E7 | `DeleteObjectMetaKey` 已存在（接口 :44 / 实现 :358；无 key 时 no-op；行不存在 `ErrNotFound`） | ✅ 精确；`file_features.go:333`（`DeleteMetadataKey`）有使用先例 |
| E8 | 测试基建：`scrub_test.go:26` 模式、`openTestRepo`（job_test.go:17）、`newSilentLogger`（:70）、`WithScrub` | ✅ 全部存在；另确认 `allowAllAuthz` 定义于同包 `deletion_test.go:84`（AC-1 可直接复用）、`openTestStore`（job_test.go:32）、`repositoryChunkCleaner`（scrub_test.go:18）。**注意（工作区溯源）：** `allowAllAuthz` 与 `scrub_test.go` 的 `WithAuthorizer` 接线是工作区中另一在途 campaign（fail-closed delete）的未提交改动（`git diff HEAD` 可见）；纯 HEAD 上不存在该助手。实现阶段在工作区进行，AC-1/AC-2 可直接编译；若实现环境回退到纯 HEAD，需随 AC-1 一并引入该助手（同包内，无跨包问题） |
| E9 | telemetry 澄清：`IncScrubResult(ctx,status)`（metrics.go:266-269）按 status 聚合 `mScrubTotal`；corrupt 计数指标已存在 | ✅ 精确；`deploy/prometheus/alerts.yml:115` 仅查询 `scrub_total{status="corrupt"}`——新增 `"repaired"` 标签值不影响任何告警/仪表盘（grep 确认 dashboard JSON 无 scrub_total 引用） |
| E10 | `cleanObjectChunks` 可安全用于 AC-2 corrupt 路径 | ✅ `deletion.go:82-92` 对 `cleaner==nil` 早退，nil 安全 |
| E11 | `RECONCILE_SCRUB_ENABLED`（默认 false）经 `cmd/server/workers.go:88` `WithScrub(cfg.Reconcile.ScrubEnabled, 100)` 装配 | ✅ 与 `config.go:249` 一致 |

### 复验中发现的新问题（requirements 草稿的三处缺陷，本设计必须修正）

> 这三个问题若不修正，**即使实现正确，requirements 给出的验收代码也无法通过**——gate 复检时将以可编译 + 可判别为基准，故在本设计中显式修正并给出证据。

- **D1（AC-1 编译错误）：** `fileService.Get` 返回 **3 个值** `(io.ReadCloser, repository.Object, error)`（`file_get.go:24`）。requirements AC-1 第③步写 `rc, err := fileService.Get(...)` 无法编译。**修正：** `rc, _, err := fileService.Get(...)`。
- **D2（AC-1 逻辑缺陷，最严重）：** requirements AC-1 第②步复用第①步 `Put` 返回的 `object` 变量调用第二次 `scrubObject`。该内存对象的 `Metadata` 是 Put 时的快照，**不含**后来才写入 DB 的 `_aero_scrub_status=corrupt`——FR-1 的守卫 `obj.Metadata[...]=="corrupt"` 恒为 false，清除路径永不执行，**测试在实现正确后依然失败**（伪失败）。生产路径无此问题（`scrubAll` 每轮经 `ListObjects` 取新行，见 `scrub.go:42-47`）。**修正：** 恢复 blob 后先 `repo.GetObjectByID` 重取对象再调用 `scrubObject`。
- **D3（FR-2 多租户累计缺陷）：** requirements FR-2 写 `scrubScanned, scrubCorrupt := j.scrubAll(...)`——在 per-tenant 循环体内 `:=` 每次迭代**遮蔽**外层变量，多租户时 `scrub_corrupt` 只反映**最后一个租户**的计数（默认单租户配置掩盖此缺陷；`TestJobSweep_MultipleTenants` 证明多租户是受支持模式）。**修正：** 在循环外声明 `scrubCorrupt` 累加器，循环内 `scrubCorrupt += corrupt`。
- **D4（函数长度约定）：** `scrubObject` 现为 63 行（scrub.go:39-101），内联清除逻辑会进一步超出「单函数 ≤50 行」约定（WARN 级）。**修正：** 提取私有方法 `clearCorruptFlag`（~21 行），`scrubObject` 仅 +1 行调用。

---

## 2. 变更设计

### 2.1 FR-1 — `internal/reconcile/scrub.go`：完好分支清除 corrupt 标记

完好分支（`:89-91`）改为：

```go
	if bytes.Equal(computed, expected) {
		telemetry.IncScrubResult(ctx, "ok")
		j.clearCorruptFlag(ctx, obj)
		return nil
	}
```

`scrubObject` 之后新增 `clearCorruptFlag`（~21 行 ≤50，满足单函数约定；文件增长 ~23 行，101→~124 ≤500）：

```go
// clearCorruptFlag removes the _aero_scrub_status marker from a previously
// corrupt object once its content verifies intact, restoring read access.
// No-op for objects never flagged (the guard avoids a DB round-trip on the
// hot intact path) and non-fatal on failure: the marker stays, the object
// remains locked, and the next sweep retries.
func (j *Job) clearCorruptFlag(ctx context.Context, obj repository.Object) {
	if obj.Metadata["_aero_scrub_status"] != "corrupt" {
		return
	}
	if err := j.repo.DeleteObjectMetaKey(ctx, obj.TenantID, obj.Bucket, obj.Key, "_aero_scrub_status"); err != nil {
		j.logger.Warn("scrub: failed to clear corrupt flag",
			"tenant", obj.TenantID, "bucket", obj.Bucket, "key", obj.Key, "err", err)
		return
	}
	telemetry.IncScrubResult(ctx, "repaired")
	j.logger.Info("scrub: repaired object cleared",
		"tenant", obj.TenantID, "bucket", obj.Bucket, "key", obj.Key, "storage_key", obj.StorageKey)
}
```

要点：

- **守卫先行**：完好路径是每轮扫描热路径（每对象每轮），仅当标记存在才发起 DB 往返；`DeleteObjectMetaKey` 实现（`sql_objects_maint.go:358-379`）对无 key 的 metadata 直接 `return nil`，幂等。
- **失败不阻断**：清除失败（含行消失 `ErrNotFound`）→ warn 日志 + `return`（不返回错误）——内容完整性已确认，不误报 corrupt；标记保留、对象继续锁定（安全默认：清理失败绝不静默放行），下一轮 scrub 重试。这与 corrupt 路径 `SetObjectMetaKey` 失败仅 warn 的既有风格（`scrub.go:95-97`）一致。
- **`IncScrubResult(ctx, "repaired")`**：复用既有 `mScrubTotal` 计数器的新 status 标签值（`metrics.go:266-269`），使修复事件可聚合；**不新增指标**。与 `"ok"` 同时计数（一次「损坏后修复」的扫描 = ok + repaired 各 +1），语义自洽。
- **corrupt 路径零改动**（`:93-99`）：仍写 `"corrupt"`、仍 `cleanObjectChunks`、仍返回 error 计入 corrupt。
- **单一写入者纪律**：不在 restore/upload/其它路径清标记（无旁路）。标记语义定为「最近一次 scrub 的验证结果缓存」：带标记的软删对象被 restore 后，由下一轮 scrub 重新验证后自然清除。

### 2.2 FR-2 — `internal/reconcile/job.go`：sweep 汇总日志上报 corrupt 计数

`Job.sweep`（`:111-128`）两处改动：

```go
	var scanned, orphanRows, orphanBlobs, deletedBlobs, scrubCorrupt int   // ← 新增累加器
	for _, t := range j.tenants {
		sc, or := j.sweepOrphanRows(ctx, t)
		ob, db := j.sweepOrphanBlobs(ctx, t)
		scanned += sc
		orphanRows += or
		orphanBlobs += ob
		deletedBlobs += db
		scrubScanned, corrupt := j.scrubAll(ctx, t, j.scrub)   // ← 不再丢弃
		scanned += scrubScanned
		scrubCorrupt += corrupt                                // ← 跨租户累计（D3 修正）
	}
	telemetry.RecordReconcileBlobs(ctx, orphanBlobs, deletedBlobs)
	j.logger.Info("reconcile sweep done",
		"scanned", scanned,
		"orphan_rows", orphanRows,
		"orphan_blobs", orphanBlobs,
		"orphan_blobs_deleted", deletedBlobs,
		"scrub_corrupt", scrubCorrupt,                          // ← 新字段，恒携带
		"delete_enabled", j.deleteOrphanBlobs,
		"duration_ms", time.Since(start).Milliseconds())
```

要点：

- `scrubAll` 签名/返回契约不变（`(scanned, corrupt int)`，`scrub.go:24`）；scrub 关闭时返回 `0,0` → 日志字段恒为 `scrub_corrupt=0`，日志形状稳定。
- per-object warn 日志（`scrub.go:98-100`）与 `IncScrubResult` 计数不变。
- 不新增 telemetry 指标（`mScrubTotal{status="corrupt"}` 已聚合；本方向只补日志字段——requirements 修订记录已收敛 scope）。

---

## 3. API 变化

| 层 | 变化 |
|---|---|
| `repository.Repository` 接口 | **无**（`DeleteObjectMetaKey` 已存在，:44） |
| `Job`（reconcile） | **无公开变化**；新增私有方法 `clearCorruptFlag`；`scrubAll`/`scrubObject` 签名不变 |
| `FileService` / adapter / CLI / SDK | **无** |
| telemetry | **无新指标**；`mScrubTotal` 新增 `status="repaired"` 标签值（加法兼容） |
| 日志 | `reconcile sweep done` 新增 `scrub_corrupt` int 字段（加法兼容）；新增 `scrub: repaired object cleared` Info 行与 `scrub: failed to clear corrupt flag` Warn 行 |
| 配置/env | **无** |

---

## 4. 兼容性约束

- **行为语义变化（预期修复）：** 标记为 corrupt 且内容已恢复完好的对象，在下一轮 scrub 后自动解除锁定（此前永久锁定）。这是本方向的**目的**，不是回归。仍损坏的对象标记保留、行为不变（Get/Stat/feature/multipart-copy 仍返回 `ErrObjectCorrupt`）。
- **`scrubObject` 返回契约不变**：nil = 完好/跳过/瞬态错误；error = corrupt。调用方仅 `scrubAll`（`scrub.go:47`，grep 确认）与测试。
- **`_aero_scrub_status` 语义**：从「永久锁」变为「最近一次 scrub 验证结果缓存」。不破坏任何读路径（读方只判 `=="corrupt"`，键缺失 = 正常）。
- **多租户**：`scrub_corrupt` 为全 sweep 聚合值（跨租户累计，D3）。
- **opt-in 不变**：scrub 仍由 `RECONCILE_SCRUB_ENABLED`（默认 false）门控；未启用时行为与今日完全一致（`scrubAll` 早退返回 `0,0`）。
- **SQLite + Postgres**：`DeleteObjectMetaKey` 双方言同实现（`sql_objects_maint.go:358-379`），无方言分支差异。

---

## 5. 失败模式

| # | 场景 | 行为 | 判定 |
|---|---|---|---|
| F1 | 清除时行已不存在（并发硬删/retention 先于清除）→ `DeleteObjectMetaKey` 返回 `ErrNotFound` | warn + 返回 nil；标记随行消失 | 安全：内容已确认完好；行没了则锁无意义 |
| F2 | 清除时 DB 瞬态错误（SQLite 写锁/网络） | warn + 返回 nil；标记保留 → 对象继续锁定 | 安全默认：绝不静默放行；下一轮 scrub 重试（自愈） |
| F3 | TOCTOU：读 blob 后、清除前 blob 再次被损坏，并发 scrub 已重新写 `corrupt`，随后本路径清除 | 短暂窗口内对象可读，下一轮 scrub 重新检测并再次锁定 | 可接受：清除时内容确实通过 MD5；窗口 ≤ 1 个 sweep 周期；自愈。备选「事务内重验」需新增 Repository API，违反零 API 约束，**显式否决** |
| F4 | 并发覆盖：Put 换新行后清除 | `DeleteObjectMetaKey` 按 `(tenant,bucket,key)+deleted_at IS NULL` 定位**当前**行；新行 metadata 本就无标记 → 清除为 no-op，无跨对象副作用 | 安全 |
| F5 | 带标记但缺 `_aero_content_md5` 的对象（仅能带外写入产生，scrub 自身只标记含 md5 键的对象） | `scrubObject` 在 md5 键检查处早退，标记不清 | 显式接受：scrub 是唯一标记写入者且只在 md5 键存在时标记；文档记录该边界 |
| F6 | 恢复的 blob 内容与 `_aero_content_md5` 不一致 | 仍判 corrupt，标记保留 | 正确：内容不匹配记录摘要，不应解锁 |
| F7 | `IncScrubResult` 在 telemetry 未初始化时调用 | `initDomain()` 幂等（`metrics.go:268`），与既有 `"ok"`/`"corrupt"` 路径相同 | 无新增风险 |

---

## 6. 迁移步骤

1. **无 schema/数据迁移**（I2：无迁移文件、无 SQL 变更）。
2. **部署顺序无要求**：`scrub.go`/`job.go` 为向后兼容增量；旧版本实例与新版本实例混跑时，旧实例不写 `scrub_corrupt` 字段、不清标记——互不干扰（清除是幂等 no-op 性质的新行为）。
3. **运维说明（写入发布说明）：** 部署后下一轮定时 sweep（`RECONCILE_INTERVAL_MINUTES > 0` 且 `RECONCILE_SCRUB_ENABLED=true`）自动验证历史被锁对象：内容与记录 MD5 一致者自动解锁（**不再需要手工 SQL/元数据手术**），仍不一致者保持锁定。多副本场景由既有 singleton lease（`RECONCILE_CLUSTER_SINGLETON`）保证单实例执行。

---

## 7. 测试验收映射（可编译、可判别、must-fail-on-HEAD）

> 全部位于 `internal/reconcile` 测试包，复用同包既有基建（`openTestRepo`/`openTestStore`/`newSilentLogger`/`allowAllAuthz`/`repositoryChunkCleaner`），无跨包测试助手（吸取 webhook-failures 方向 gate FAIL 的教训，见 §8）。

### AC-1（对应 requirements AC-1，含 D1/D2 修正）— `scrub_test.go`

```go
// TestScrub_ClearsFlagWhenIntact: 修复前（HEAD）此测试必败——
// 完好分支不清理标记，第③步 Get 返回 ErrObjectCorrupt。
func TestScrub_ClearsFlagWhenIntact(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(t.TempDir(), "objects")})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	fileService := service.NewFileService(store, repo, nil).WithAuthorizer(allowAllAuthz{})
	body := "trusted content"
	digest := md5.Sum([]byte(body))
	object, err := fileService.Put(
		ctx, "", "", "scrubbed.txt", strings.NewReader(body), int64(len(body)),
		service.PutOptions{ContentMD5: base64.StdEncoding.EncodeToString(digest[:])},
	)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	job := New(repo, store, 0, false, 0, []string{"default"}, newSilentLogger()).
		WithChunkCleaner(repositoryChunkCleaner{repo: repo})

	// ① 篡改 blob → scrub → DB 中标记 = corrupt
	tampered := "altered content"
	if _, err := store.Put(ctx, object.StorageKey, strings.NewReader(tampered), int64(len(tampered)), storage.PutOptions{}); err != nil {
		t.Fatalf("tamper storage: %v", err)
	}
	if err := job.scrubObject(ctx, object); err == nil {
		t.Fatal("expected corruption result")
	}
	reloaded, err := repo.GetObjectByID(ctx, object.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Metadata["_aero_scrub_status"] != "corrupt" {
		t.Fatalf("flag not set: %v", reloaded.Metadata)
	}

	// ② 恢复 blob（与 _aero_content_md5 一致）→ **重取对象** → 再次 scrub → 标记清除
	//    D2：必须经 GetObjectByID 重取；复用 Put 返回的 object（Metadata 无标记快照）
	//    会使 clearCorruptFlag 守卫恒 false，测试在实现正确时也失败。
	if _, err := store.Put(ctx, object.StorageKey, strings.NewReader(body), int64(len(body)), storage.PutOptions{}); err != nil {
		t.Fatalf("restore blob: %v", err)
	}
	reloaded, err = repo.GetObjectByID(ctx, object.ID)
	if err != nil {
		t.Fatalf("reload after restore: %v", err)
	}
	if err := job.scrubObject(ctx, reloaded); err != nil {
		t.Fatalf("scrub after repair: %v", err)
	}
	reloaded, err = repo.GetObjectByID(ctx, object.ID)
	if err != nil {
		t.Fatalf("reload after clear: %v", err)
	}
	if _, still := reloaded.Metadata["_aero_scrub_status"]; still {
		t.Fatalf("flag not cleared: %v", reloaded.Metadata)
	}

	// ③ Get 不再返回 ErrObjectCorrupt（HEAD 上此步必败）
	rc, _, err := fileService.Get(ctx, "", "", "scrubbed.txt") // D1：Get 返回 3 值
	if err != nil {
		t.Fatalf("Get after repair: %v", err)
	}
	rc.Close()
}
```

**判别力：** HEAD 上第②步末断言（标记仍在）与第③步（Get 报错）双失败；实现正确后全绿。D2 修正保证测试在实现正确时**必然通过**（守卫被真实执行）。

### AC-2（对应 requirements AC-2 + 多租户累计子场景）— `job_test.go`

```go
// TestJobSweep_ReportsScrubCorruptCount: HEAD 上 sweep 日志无 scrub_corrupt 字段，
// 两个子场景均必败。
func TestJobSweep_ReportsScrubCorruptCount(t *testing.T) {
	ctx := context.Background()

	// 子场景 A：1 个损坏对象 → scrub_corrupt=1（requirements AC-2 原样）
	repo := openTestRepo(t)
	store := openTestStore(t)
	if err := repo.CreateBucket(ctx, "default", "default"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	fileService := service.NewFileService(store, repo, nil).WithAuthorizer(allowAllAuthz{})
	body := "trusted content"
	digest := md5.Sum([]byte(body))
	object, err := fileService.Put(ctx, "default", "default", "corrupt.txt",
		strings.NewReader(body), int64(len(body)),
		service.PutOptions{ContentMD5: base64.StdEncoding.EncodeToString(digest[:])})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	tampered := "altered content"
	if _, err := store.Put(ctx, object.StorageKey, strings.NewReader(tampered), int64(len(tampered)), storage.PutOptions{}); err != nil {
		t.Fatalf("tamper storage: %v", err)
	}
	var bufA bytes.Buffer
	jobA := New(repo, store, time.Minute, false, time.Minute, []string{"default"},
		slog.New(slog.NewTextHandler(&bufA, nil))).WithScrub(true, 100).
		WithChunkCleaner(repositoryChunkCleaner{repo: repo})
	jobA.sweep(ctx)
	logA := bufA.String()
	if !strings.Contains(logA, "reconcile sweep done") || !strings.Contains(logA, "scrub_corrupt=1") {
		t.Fatalf("sweep log missing scrub_corrupt=1: %s", logA)
	}

	// 子场景 B：健康对象对照 → scrub_corrupt=0（字段恒携带）
	repo2 := openTestRepo(t)
	store2 := openTestStore(t)
	if err := repo2.CreateBucket(ctx, "default", "default"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	fileService2 := service.NewFileService(store2, repo2, nil).WithAuthorizer(allowAllAuthz{})
	digest2 := md5.Sum([]byte(body))
	if _, err := fileService2.Put(ctx, "default", "default", "healthy.txt",
		strings.NewReader(body), int64(len(body)),
		service.PutOptions{ContentMD5: base64.StdEncoding.EncodeToString(digest2[:])}); err != nil {
		t.Fatalf("put healthy: %v", err)
	}
	var bufB bytes.Buffer
	jobB := New(repo2, store2, time.Minute, false, time.Minute, []string{"default"},
		slog.New(slog.NewTextHandler(&bufB, nil))).WithScrub(true, 100)
	jobB.sweep(ctx)
	logB := bufB.String()
	if !strings.Contains(logB, "reconcile sweep done") || !strings.Contains(logB, "scrub_corrupt=0") {
		t.Fatalf("sweep log missing scrub_corrupt=0: %s", logB)
	}

	// 子场景 C（D3 回归护栏）：双租户各 1 损坏对象 → 累计 scrub_corrupt=2
	// 若实现误用循环内 `:=`（遮蔽），此处只会得到 1。
	repo3 := openTestRepo(t)
	store3 := openTestStore(t)
	tenants := []string{"tenantA", "tenantB"}
	for _, t2 := range tenants {
		if err := repo3.CreateBucket(ctx, t2, "default"); err != nil {
			t.Fatalf("create bucket %s: %v", t2, err)
		}
		fs := service.NewFileService(store3, repo3, nil).WithAuthorizer(allowAllAuthz{})
		d := md5.Sum([]byte(body))
		obj, err := fs.Put(ctx, t2, "default", "corrupt.txt",
			strings.NewReader(body), int64(len(body)),
			service.PutOptions{ContentMD5: base64.StdEncoding.EncodeToString(d[:])})
		if err != nil {
			t.Fatalf("put %s: %v", t2, err)
		}
		if _, err := store3.Put(ctx, obj.StorageKey, strings.NewReader(tampered), int64(len(tampered)), storage.PutOptions{}); err != nil {
			t.Fatalf("tamper %s: %v", t2, err)
		}
	}
	var bufC bytes.Buffer
	jobC := New(repo3, store3, time.Minute, false, time.Minute, tenants,
		slog.New(slog.NewTextHandler(&bufC, nil))).WithScrub(true, 100)
	jobC.sweep(ctx)
	logC := bufC.String()
	if !strings.Contains(logC, "scrub_corrupt=2") {
		t.Fatalf("sweep log missing cumulative scrub_corrupt=2: %s", logC)
	}
}
```

**注意：** 子场景 B 的 `jobB` 未设 `WithChunkCleaner`——corrupt 路径不会触发（健康对象），且 `cleanObjectChunks` 对 nil cleaner 早退（E10），安全。子场景 A/C 的 `WithChunkCleaner` 仅为与 scrub_test.go 模式一致。

### AC-3（门禁）

- `go test ./internal/reconcile/` 全绿；`gofmt -l` 无输出；`go build ./...`；`go vet ./...`（`make check`）。
- 行数：`scrub.go` 101→~124、`job.go` 247→~251、`scrub_test.go` 95→~185（AC-1 约 90 行）、`job_test.go` 增长 ~105 行（AC-2）——均 ≤500。

### 可选 AC-4（FR-1 失败路径，锁定「清除失败 = warn + 返回 nil」契约）

```go
// TestScrub_ClearFlagFailureKeepsFlag: 行消失（ErrNotFound）时清除失败
// 不得转为 corrupt 错误；标记随行消失，对象不再可读是行本身不存在所致。
func TestScrub_ClearFlagFailureKeepsFlag(t *testing.T) {
	// ① seed + 篡改 + scrubObject → corrupt（镜像 AC-1 ①）
	//    然后 **重取**：flagged, _ := repo.GetObjectByID(ctx, object.ID)
	//    （D2 同款陷阱：Put 返回的 object.Metadata 无标记，直接复用会使
	//     clearCorruptFlag 守卫恒 false，失败路径永远不被执行——测试伪通过）
	// ② repo.HardDeleteObjectByID(ctx, object.ID) → 行消失（sql_objects_maint.go:100）
	// ③ 恢复 blob（内容与 _aero_content_md5 一致）
	// ④ scrubObject(ctx, flagged) → 完好分支 → 守卫 true → DeleteObjectMetaKey
	//    返回 ErrNotFound → warn 且 **err == nil**（不得误报 corrupt）
}
```

> 该测试用于钉住 FR-1 失败语义（F1/F2）；若实现将清除失败误报为 corrupt 错误（`errors.New("corrupt")`），本测试必败。建议随实现落地（与 AC-1/AC-2 同 commit），不改变验收集合。

---

## 8. 历史尝试处置（gate 复检清单，逐项给出证据）

### 8.1 本管线自身（scrub-never-clears-…617a37cf）

- **DECISIONS.md：** 仅 `requirements` PASS（2026-08-06 22:56:13）；尚无 design-gate 判定。requirements 修订记录中列出的全部验证问题（行号偏移、4 调用点、`DeleteObjectMetaKey` 使能项、telemetry scope 收敛）已在 requirements 内解决，且在本设计 §1 复验通过。
- **requirements 遗留 → 本设计新增修正：** D1（Get 三值返回，§1/§7 AC-1）、D2（陈旧对象快照，§1/§7 AC-1）、D3（多租户累计遮蔽，§1/§7 AC-2 子场景 C）、D4（函数长度，§2.1 提取 helper）——全部带证据修正，非口头否决。

### 8.2 同名/近名兄弟 run

- **全仓 `docs/auto/runs/*` 设计产物 grep `scrub`：零命中**（除本管线自身）。无同名兄弟方向的 gate 判定需要处置。

### 8.3 相邻模块兄弟 run（reconcile/jobs 域，gate 均为 FAIL——逐项处置是否触及本方向改动面）

| 兄弟 run | 其 FAIL 原因 | 与本方向的关系 | 处置 |
|---|---|---|---|
| `bound-job-execution-time-and-add-lease-heartbeat-fdd4f0ed` | 内部 `internal/jobs` 的 TouchJob/terminal-transition/worker-label/PG 时钟等 | 文件面为 `internal/jobs`（JobPool），**不含** `internal/reconcile` 的 scrub/job.go sweep 日志 | **显式否决（证据）**：符号面零重叠（grep `TouchJob`/`WithJobTimeout` 无 reconcile 命中）；其发现不适用于本改动 |
| `renew-the-singleton-lease-while-the-guarded-acti-7cf7f4fd` | singleton lease 的 F-01/F-02/F1/F-03/F2/F-04（GuardRenewing 缺失、hung sweep 不可见等） | 触及 `cluster.Singleton` 与 `job.go:maybeSweep` 的**租赁**路径 | **显式否决（证据）**：本方向不新增/修改任何 lease 代码、不新增指标（F-03 要求的 `reconcile_sweep_started_timestamp_seconds` 明确不在本方向 scope——requirements 已收敛「不新增指标」，仅补日志字段）；其发现与本改动正交，且由该 run 自行跟踪 |
| `webhook-failures-table-grows-unboundedly-add-ret-183a97d6` | webhook 表 pending 无界/OR 全表扫描/PG 方言覆盖/跨包测试助手不可编译等 | 文件面 `webhook` 相关，唯一可迁移教训为「测试计划不可编译」 | **已吸收（证据）**：§7 全部测试仅用同包既有助手（`openTestRepo`/`allowAllAuthz`/`repositoryChunkCleaner`）+ 内联 capture logger；无跨包测试助手 |
| `bucketcors-resolves-tenant-as-default-for-every--31b43d4f`（本会话家族 gate FAIL 案例） | 设计稿未在评审后修订、环数声明错误、AC 无 must-fail 出处等**流程性** FAIL | 流程教训适用于所有方向 | **已吸收（证据）**：① 本设计所有行号/符号均在 §1 逐条复验于 HEAD `acfaaf4`，无未验证声明；② 每个 AC 标注 must-fail-on-HEAD 出处与判别力分析（§7）；③ 设计为完整产物（非 stub），D1-D4 显式修正了 requirements 草稿缺陷；④ 本设计在 adversarial_review 后将按 gate 要求再核修订状态 |

### 8.4 其它相关方向（分析文件 `docs/auto/analyses/internal-reconcile-7a29db11.json` 方向 2/3）

- **方向 2（lifecycle/retention/upload-GC 单次 LIMIT 查询无分页）与方向 3（grace=0 关闭在途上传保护）**：与本方向同模块但**独立问题面**（lifecycle.go/retention.go/upload_gc.go/config 校验 vs scrub.go/job.go 日志）。requirements 明确「Do not expand scope beyond this direction」，本设计**显式否决**并入：不触碰 lifecycle/retention/upload-GC 代码与 `New()` 校验逻辑（证据：本设计改动文件清单 §0 仅 scrub.go/job.go 及两个测试文件）。

---

## 9. 硬门禁合规自检（`make check`）

| 门禁 | 状态 | 证据 |
|---|---|---|
| `gofmt -l` 无输出 | ✅ | 新增代码为 gofmt 风格（tab 缩进、参数换行对齐） |
| `go build ./...` / `go vet ./...` | ✅ | 无新包、无新依赖（stdlib only，I6）；符号均在同包内 |
| `go test ./...` | ✅（预期） | AC-1/AC-2/AC-3 覆盖新行为；既有 `TestScrubCorruptionRemovesSearchChunks`/`TestJobSweep_*` 不受影响（`scrubAll` 契约不变、日志字段为加法） |
| 单文件 ≤500 行 | ✅ | §7 行数预估，最大 ~350（job_test.go） |
| 无迁移（I2）/无新依赖（I6）/链不动（I4）/key 布局不动（I3）/opt-in 不动（I5）/SQL 占位符不动（I1） | ✅ | 本设计不触碰上述任何面（§0、§3、§6） |

---

## 10. 实施顺序（供 implement 阶段）

1. `scrub.go`：新增 `clearCorruptFlag` + 完好分支调用（§2.1）。
2. `job.go`：`scrubCorrupt` 累加器 + 日志字段（§2.2）。
3. `scrub_test.go`：AC-1；`job_test.go`：AC-2（含子场景 C）+ 可选 AC-4。
4. `gofmt -l .`、`go build ./...`、`go vet ./...`、`go test ./internal/reconcile/`、`go test ./internal/service/ ./internal/repository/ ./internal/telemetry/`（回归邻域）。
5. 运行完整 `make check`。
