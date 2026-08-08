# 设计：Create 产出事务一致的单文件 SQLite 镜像（VACUUM INTO）+ trailer 写错误响亮失败

> **模块：** `internal/snapshot` · **HEAD：** `acfaaf4`（工作树含 69 个未提交改动，与 `internal/snapshot` 无关）· **日期：** 2026-08-07
> **输入契约：** `docs/requirements/snapshot-create-consistent-image-v1.spec.md`（FR-1/2/3，AC-1..4）
> **本设计的关键实测依据：** 全部 VACUUM INTO 语义用 modernc.org/sqlite v1.50.1 + Go 1.26.5 在 `/tmp/vacuum-exp` 实测（见 §3.2 证据表），非纸面推断。

---

## 0. 前置处置（gate 要求：prior attempts 逐一 disposition）

| 来源 | 内容 | 处置 |
|------|------|------|
| 本 run `DECISIONS.md` | 仅一条：requirements PASS（2026-08-07 01:01:33），无 open findings | ✅ 无待办；本设计以该 spec 为输入契约 |
| 本 run `archive/` | 空 | ✅ 无历史轮次 |
| 本 run 尚无 design/adversarial_review/design_gate 产物 | design 阶段即本任务 | ✅ 本设计是首个 design 产物 |
| 兄弟 run `fix-gostarted-…-8fc438b0/DECISIONS.md` | design_gate PASS；**implement 阶段 VALIDATION_FAILED (exit=1)**，产物文件已不在盘上 | ⚠️ **经验教训（非本方向 findings）：** implement 验证是 exit-code 驱动的（`make check` + `python3 cli.py check`）。本设计必须给出可执行的绿门路径与精确命令（§8），并保持改动面 ≤ 500 行/文件 |
| 兄弟 run 全集 grep `internal/snapshot` | 无任何 run 触及本模块（命中仅为无关 prose，如 keyset 分页、middleware 链） | ✅ 无同方向先例 |
| memory index `pb-…-210`（本方向 requirements） | PASS，无 findings | ✅ |
| memory index `pb-…-209`（fix-gostarted implement） | VALIDATION_FAILED，与本题无关 | ✅ 同上经验教训 |

**对 requirements spec 的 1 处修正（新发现，已并入设计）：** spec §2 迁移表列了 5 条假字节测试，但漏列第 6 条 —— `TestRestoreDoesNotFollowEscapingDestinationSymlink`（`snapshot_test.go:285`，假字节 `"database"` 经 `Create` 构造归档）。本设计迁移矩阵（§6.2）补齐为 6 条。其余 spec 断言全部复核成立。

---

## 1. 证据复核（untrusted claims → 独立验证，HEAD `acfaaf4`）

| 引证 | 独立复核 | 结论 |
|------|---------|------|
| `snapshot.go:22` 文档注释 = 原始拷贝 db + -wal + -shm | 注释 :19-25，`:21` `./db/aero.db (+ -wal, -shm)`、`:22` `./objects/...` | ✅ 精确 |
| `snapshot.go:46` 原始拷贝语义 | 调用 :41 `addDBFiles(tw, dbFile)`；实现 :50-67 stat（:51-53）+ `addFile` db（:54）+ 存在的 `-wal`/`-shm`（:57-65）。**全程无 SQLite 介入** | ✅ 行号漂移 46→41/50-67，语义成立 |
| `snapshot.go:31` `os.Create(outPath)` + 裸 defer 丢 Close 错误 | :31 `os.Create`；:35/:37/:39 三个裸 defer（`f.Close`/`gz.Close`/`tw.Close`）错误全丢 | ✅ 精确；"full disk → nil 返回截断归档"成立 |
| `go.mod:27` modernc.org/sqlite v1.50.1 | require 块 :27 精确命中；`repository/sqlite.go:11` 空导入 | ✅ 零新依赖（I6） |
| `docs/deployment.md:220` CLI 直接调用 | :220 `aero-vault cli snapshot create backup.tgz \`；:216-217 "while the server is stopped" 仅文档软约束，CLI 无停服检测（`cli_snapshot.go:33-38` 只读 `DB_DSN`/`STORAGE_LOCAL_ROOT`） | ✅ 精确 |
| `repository/sqlite.go:31` WAL 生产路径 | :29-31 `PRAGMA foreign_keys = ON; PRAGMA journal_mode = WAL;` | ✅ WAL 即默认部署形态 |
| `snapshot_test.go` 无 SQLite、假字节喂 Create | 文件头 import 无 `database/sql`；`TestRoundTrip` 假头 + 假 wal/shm | ✅ "无 live-DB 测试"成立 |
| 调用面 | `Create`/`Restore` 仅 `internal/cli/cli.go:30-31` 调用；`cmd/server` 不调用 | ✅ 改造影响面 = CLI + 测试 |

**基线：** `go test ./internal/snapshot` 在 HEAD 全绿（cached ok）。

---

## 2. 机制决策：VACUUM INTO（否决 checkpoint-then-copy）

| 判据 | VACUUM INTO（选定） | `PRAGMA wal_checkpoint(TRUNCATE)` 后拷贝 |
|------|---------------------|------------------------------------------|
| live 服务器兼容 | ✅ 只读快照：与并发读者/写者共存；**实测**：写事务悬挂中仍 0s 完成且排除未提交行（§3.2 exp-6） | ❌ TRUNCATE 要求无并发读者才能完成 → busy=1 → 按 FR-1 必须响亮报错 → 对"服务运行中备份"场景几乎必然失败（服务持续读） |
| 对服务端 WAL 的扰动 | 无（不动源库任何状态） | 截断/重置服务端 WAL，有副作用 |
| journal mode 无关性 | 任意 mode 均可（含 WAL） | 仅 WAL 有意义 |
| freshness 契约 | 快照起点 = 语句执行时的读事务起点，晚于 `Create` 调用 → 调用前已提交必含 | checkpoint 在 Create 调用后执行 → 同样满足 |
| 实测 | ✅（§3.2） | 未实测（已否决） |

**结论：** 采用 VACUUM INTO（spec FR-1 "机制二选一"中的推荐项），配 `busy_timeout(5000)`；busy 超限 → SQLITE_BUSY 响亮报错（FR-1 的"checkpoint 忙则报错"契约以等价形式覆盖）。

---

## 3. 实测证据（设计关键前提，全部在 `/tmp/vacuum-exp` 用仓库同版本驱动实测）

| # | 实验 | 结果 | 设计含义 |
|---|------|------|---------|
| exp-1 | `Exec("VACUUM INTO ?", path)` 绑定参数 | **err=nil 但文件未创建（静默 no-op！）** | **禁止绑定参数**；必须字面量 SQL：`"VACUUM INTO '" + strings.ReplaceAll(path,"'","''") + "'"` |
| exp-2 | 字面量路径 + 并发写者（WAL） | 成功；恢复库 `COUNT=25 ≥ committedBefore=17`；`integrity_check=ok` | freshness 契约成立；AC-2 断言可行 |
| exp-3 | 目标文件已存在 | `SQL logic error: output file already exists` | 临时文件必须用不存在的唯一路径（`os.MkdirTemp` 目录内，目录保证唯一） |
| exp-4 | 路径含单引号 | `''` 转义后成功 | 转义方案成立 |
| exp-5 | 垃圾 db 文件 | `file is not a database (26)` 响亮报错 | FR-1 "非合法 SQLite → 报错"由驱动天然提供 |
| exp-6 | WAL 下源库持开写事务（未提交行） | VACUUM INTO 0s 完成，快照排除未提交行，integrity ok | WAL 读者不阻塞；快照一致 |
| exp-7 | `mode=rw` 打开不存在的文件 | `unable to open database file`，**不自动建库** | S5 防线：mode=rw 保留"缺文件即报错"；**裸 DSN 会静默建空库（exp-8）→ 必须显式 mode=rw** |
| exp-8 | 裸 DSN（默认 rwc）打开不存在文件 | ping 成功且**文件被自动创建** | 印证 spec S5 陷阱，实现必须避免 |
| exp-9 | `mode=ro` | 服务持库时可用；干净关闭后也可用 | 不用 ro：崩溃遗留 WAL 需恢复时 ro 会失败；**选 rw**（rw 不自动建库，exp-7） |
| exp-10 | **AC-4 陷阱验证**：`failingWriter` 若采用"跨限部分写入 + 返回 nil"（写满 limit 后返回 `(room, nil)`），`limit=total-1` 时 `createArchive` 返回 **nil** | `compress/gzip` 的 `Close` 只检查 `err != nil`，不检查短写计数 → footer 最后一次 `Write` 被部分接受（`(7, nil)`）即静默截断 | **failingWriter 必须"拒绝整次跨越 write"**（`n+len > limit` → `(0, io.ErrShortWrite)`），绝不做部分写入；如此 `limit=total-1` 稳定触发 `snapshot: finalize archive: short write`（实测） |
| exp-11 | **AC-4 确定性证明**：同一源库连续 3 次 VACUUM INTO | 三次输出 **16384 字节逐字节相同** | 计数 run 与失败 run 的字节流长度一致 → `total` 可复用；tar mtime 的 PAX/USTAR 表示长度与秒值位数无关（世纪内恒定），objects 文件未变 → 总长确定。附带：`limit=total`（不跨限）→ nil，作对照断言 |

---

## 4. API 变化

**公开面（零变化）：**

| 符号 | 状态 |
|------|------|
| `Create(outPath, dbPath, objectsRoot string) error` | 签名不变（FR-2 明确"公开签名不变"） |
| `Restore(snapPath, dbPath, objectsRoot string) error` | 不动 |
| `FormatBytes(int64) string` | 不动 |
| `internal/cli/cli.go:30-31` `snapshotCreate`/`snapshotRestore` | 零改动 |
| 归档布局 | `db/<原始 basename>` + `objects/...`（条目名用**原始** db basename，不是临时文件名——`TestRestoreMapsDatabaseToRequestedBasename` 语义与旧归档观感一致） |

**内部面（新增/重构，均 unexported）：**

```go
// createArchive 是 FR-2/AC-4 的 io.Writer 缝：除 outPath 开/关外的全部字节
// （一致性镜像创建 → db 条目 → objects → tar trailer → gzip footer）流入 w。
func createArchive(w io.Writer, dbFile, objectsRoot string) error

// createConsistentImage 对 dbFile 执行 VACUUM INTO 到新临时目录（目录内固定名
// snapshot.db），返回目录；调用方负责 RemoveAll。含 S5 stat 前置。
func createConsistentImage(dbFile string) (string, error)
```

`Create` 重构（改动后完整形态）：

```go
// Create writes a tar.gz to outPath containing:
//
//	./db/<basename>  — transactionally consistent SQLite image (VACUUM INTO)
//	./objects/...    — object storage bytes
//
// The image includes every transaction committed before Create is invoked;
// it never contains -wal/-shm entries. dbPath is the SQLite DSN path
// (`file:./var/aero.db?...` is parsed). objectsRoot is the local-FS storage root.
func Create(outPath, dbPath, objectsRoot string) error {
	dbFile := dbFileFromDSN(dbPath)
	if dbFile == "" {
		return errors.New("snapshot: cannot derive sqlite file from DSN; only sqlite local snapshots are supported")
	}
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	err = createArchive(f, dbFile, objectsRoot)
	closeErr := f.Close()
	if err != nil {
		return err                       // 正文错误优先（FR-2：任一非 nil 即可）
	}
	if closeErr != nil {
		return fmt.Errorf("snapshot: finalize archive: %w", closeErr) // FR-2
	}
	return nil
}

func createArchive(w io.Writer, dbFile, objectsRoot string) error {
	imgDir, err := createConsistentImage(dbFile)
	if err != nil {
		return err
	}
	defer os.RemoveAll(imgDir) // FR-1：失败路径清理（成功路径同样清理）
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	if err := addDBFiles(tw, filepath.Join(imgDir, "snapshot.db"), filepath.Base(dbFile)); err != nil {
		_ = tw.Close()
		_ = gz.Close()
		return err
	}
	if err := addObjectFiles(tw, objectsRoot); err != nil {
		_ = tw.Close()
		_ = gz.Close()
		return err
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("snapshot: finalize archive: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("snapshot: finalize archive: %w", err)
	}
	return nil
}

// addDBFiles 简化为单条目（旧 sidecar 逻辑仅存在于 Restore 侧，保留向后兼容）。
func addDBFiles(tw *tar.Writer, imgPath, entryName string) error {
	return addFile(tw, imgPath, "db/"+entryName)
}

func createConsistentImage(dbFile string) (string, error) {
	if _, err := os.Stat(dbFile); err != nil { // S5：缺文件即报错（驱动 rw 也不会自建）
		return "", fmt.Errorf("snapshot: database file %q not found: %w", dbFile, err)
	}
	dir, err := os.MkdirTemp("", "aero-snapshot-*")
	if err != nil {
		return "", fmt.Errorf("snapshot: create temp dir: %w", err)
	}
	cleanup := func(err error) (string, error) { os.RemoveAll(dir); return "", err }
	target := filepath.Join(dir, "snapshot.db")
	db, err := sql.Open("sqlite", "file:"+dbFile+"?mode=rw&_pragma=busy_timeout(5000)")
	if err != nil {
		return cleanup(fmt.Errorf("snapshot: open database: %w", err))
	}
	defer db.Close()
	// 实测：VACUUM INTO 不支持绑定参数（静默 no-op），必须字面量 + '' 转义。
	q := strings.ReplaceAll(target, "'", "''")
	if _, err := db.Exec("VACUUM INTO '" + q + "'"); err != nil {
		return cleanup(fmt.Errorf("snapshot: create consistent db image: %w", err))
	}
	return dir, nil
}
```

新增 import：`database/sql`、`_ "modernc.org/sqlite"`（仓库既有依赖，I6 合规，与 `repository/sqlite.go:11` 同款空导入）。

**行数预算（硬门禁 ≤500/文件；Makefile:162 实测豁免 `*_test.go`，但本设计仍全部压线）：** `snapshot.go` 308 → ~370 · `snapshot_test.go` 367 → ~390（迁移） · 新文件 `consistent_test.go` ~240（AC-2/3/4 + helpers）。

---

## 5. 兼容性约束

| # | 约束 | 保证方式 |
|---|------|---------|
| C1 | 公开 API / CLI 零改动 | §4 仅内部重构 |
| C2 | 新归档布局 = `db/<原始 basename>` 单条目 + `objects/...`；无 `-wal`/`-shm` 条目 | `addDBFiles` 单条目；条目名用 `filepath.Base(dbFile)`（原始名） |
| C3 | 旧归档（含 sidecar 条目）继续可恢复 | `unpackSnapshot`/`classifySnapshotEntry`/`validateSnapshot` 的 `entryDBWAL`/`entryDBSHM` 分支**原样保留**（Restore 侧零改动） |
| C4 | DSN 解析错误契约不变（`TestCreate_BadDSNErrors` :55） | `dbFileFromDSN` 不动 |
| C5 | 三个有意收紧（spec §6）：垃圾 db → 报错；trailer 失败 → 报错；撕裂镜像 → 不可能 | FR-1/FR-2 实现 |
| C6 | 新归档恢复到 WAL 服务端：服务端 `journal_mode=WAL` 自动重建 sidecar | `repository/sqlite.go:31` 既有行为，无需快照侧处理 |
| C7 | 驱动版本能力：VACUUM INTO 需 SQLite ≥ 3.27；modernc v1.50.1（内嵌 3.50.x） | exp-2/6 实测通过 |
| C8 | `file:` DSN 的既有边界不变（`#` 不剥离、相对路径按 CWD） | 沿用 `dbFileFromDSN` 现状 |

---

## 6. 失败模式、迁移步骤

### 6.1 失败模式（F1–F9）

| # | 场景 | 行为 | 与现状对比 |
|---|------|------|-----------|
| F1 | db 文件缺失 | `snapshot: database file %q not found`（stat 前置，同文案） | 不变（S5 契约） |
| F2 | db 是垃圾字节 | `file is not a database` 包裹报错 | **收紧**：今日静默归档垃圾 |
| F3 | 源库写锁 > 5s（rollback-journal 长写事务） | SQLITE_BUSY 包裹报错；outPath 已开但为空/半成品（Restore 侧 `validateSnapshot` 拒绝） | **收紧**：今日静默撕裂拷贝 |
| F4 | 正文写入失败（objects `io.Copy`/tar header） | 错误传播（既有路径，不被 Close 遮蔽） | 不变 |
| F5 | trailer 阶段磁盘满/短写 | `tw.Close()`/`gz.Close()` 错误 → `snapshot: finalize archive: %w` 非 nil | **收紧**：今日裸 defer 丢错误返回 nil |
| F6 | outPath 创建失败 | `os.Create` 错误原样返回 | 不变 |
| F7 | VACUUM INTO 失败（busy/垃圾/磁盘满） | 临时目录 `RemoveAll` 保证清理（defer + 失败分支双保险） | **新增**：今日无临时文件概念 |
| F8 | objectsRoot 不存在 | 成功（`filepath.Walk` 的 `ErrNotExist` 吞掉） | 不变（`TestCreate_MissingObjectsRootIsOK` 契约） |
| F9 | WAL 下源库持开写事务 | 不阻塞：快照排除未提交行（exp-6，WAL 多版本） | **修复**：今日拷贝可能取到半帧 |

### 6.2 迁移步骤（implement 顺序）

1. **改 `snapshot.go`：** 按 §4 重构 `Create` + 新增 `createArchive`/`createConsistentImage`；`addDBFiles` 简化；更新 `Create` 文档注释（去掉 `(+ -wal, -shm)`，写一致性保证与"不含 sidecar 条目"）。`gofmt` 后 `go build ./...` + `go vet ./...`。
2. **迁移 6 条假字节测试**（`snapshot_test.go`，`openTestDB` helper 建真库）：

   | 测试（行号 = grep 实测） | 迁移内容 |
   |------------------------|---------|
   | `TestRestore_BadDSNErrors` (:66) | setup 的 `Create` 改喂 `openTestDB` 真库；断言不变 |
   | `TestCreate_MissingObjectsRootIsOK` (:93) | 假字节 → 真库；断言不变 |
   | `TestRoundTrip` (:124) | 真库 + 真对象树；**删除** sidecar 字节往返断言（:139-140 区）；新增：恢复库可打开、`integrity_check=ok`、marker 行存在 |
   | `TestRoundTrip_OnlyDBNoSidecars` (:156) | 假字节 → 真库；"恢复目录无 `-wal`"断言保留 |
   | `TestRestore_OverwritesExisting` (:182) | 假字节 → 真库；目标库断言由字节比较改为"打开 + marker 行存在"（VACUUM 镜像字节不恒定，禁止字节比较） |
   | `TestRestoreDoesNotFollowEscapingDestinationSymlink` (:285) | 假字节 → 真库（**spec 漏列，本设计补入**） |

   `TestCreate_MissingMainDBFails` (:79) 语义天然保留，**零迁移**；`TestRestoreRejects*` 五条手工归档，零迁移。
3. **新增 `internal/snapshot/consistent_test.go`**（AC-2/3/4 + FR-1 附加 + helpers，见 §7）。该包无 `t.Parallel`，`os.TempDir()` 前缀扫描残留的断言是确定性的（保留注释防未来误加 Parallel）。
4. **文档同步（FR-3）：** `docs/deployment.md:215-222` 备份段改写 —— create 取消 "while the server is stopped" 前提（改为"Create 经 VACUUM INTO 产出事务一致镜像，可在服务运行中执行；restore 仍建议停服"）；同时更新 `docs/requirements/snapshot-create-consistent-image-v1.spec.md` 之外无其他 spec 漂移（该 spec 是契约本体，不改）。
5. **门禁：** `make check`（fmt/build/vet/test/cli-check 全绿）+ `go test -race -count=1 ./internal/snapshot`（AC-2 写者 goroutine 仅经 atomic/channel 共享状态，须 race-clean）。

---

## 7. 可测试验收映射（方向四条原文 → 测试 → 断言 → 命令）

| 验收 | 测试（`consistent_test.go`，命名与 spec §4 完全一致） | 关键断言 | 命令 |
|------|------|---------|------|
| **AC-1** `go test ./internal/snapshot passes` | 全部新旧测试 | — | `go test ./internal/snapshot -count=1`；`make check` |
| **AC-2** live WAL db + 并发写者 → Create+Restore integrity ok | `TestCreate_LiveWALDB_ConcurrentWriter_Consistent` | ① 恢复库可打开；② `PRAGMA integrity_check` = `ok`；③ `COUNT(*) ≥ committedBefore`（原子计数，写者提交 ≥1 帧后取样并调 `Create`，写者持续提交至 Create 返回）；④ 恢复目录无 `-wal`/`-shm`（**先断言再开库**，避免开库生成 sidecar） | `go test ./internal/snapshot -run TestCreate_LiveWALDB -count=1 -race` |
| **AC-3** 归档无 `-wal`/`-shm` 条目 | `TestCreate_ArchiveHasNoSidecarEntries` | tar 遍历（复用包内 `classifySnapshotEntry`）：恰 1 个 `entryDBMain`、0 个 `entryDBWAL`/`entryDBSHM`；db 条目内容以 `SQLite format 3\x00` 开头；对照：手工含 sidecar 的旧式归档经 `Restore` 仍成功（复用 `writeSnapshotEntries`，FR-3/C3） | `go test ./internal/snapshot -run TestCreate_ArchiveHasNoSidecarEntries` |
| **AC-4** 强制 gzip/tar Close 失败 → 非 nil | `TestCreate_TrailerWriteError_ReturnsError` | `countingWriter` 测总字节 `total`；`failingWriter{limit: total-1}`（**拒绝式**：任何跨越 limit 的 write 整体返回 `(0, io.ErrShortWrite)`，绝无部分写入——exp-10 陷阱）再调 `createArchive` → **非 nil 且含 "finalize archive"**；对照 `limit=total` → nil；确定性由 exp-11 保证（VACUUM INTO 输出逐字节恒定 + mtime 长度稳定）；末字节（footer 首字节，位置 total-8..total-1）必然落在 `gz.Close()` 路径（`tw.Close()` trailer 与 flate flush 亦在 Close 链内）→ 必命中包装错误 | `go test ./internal/snapshot -run TestCreate_TrailerWriteError -count=1` |
| FR-1 附加 | `TestCreate_GarbageDBFile_Fails` | 垃圾 db → 非 nil；临时目录无残留（`os.TempDir()` 前缀扫描前后对比，包内无 Parallel） | `go test ./internal/snapshot -run 'TestCreate_(Garbage|MissingMain)'` |
| FR-1/FR-2 附加 | 残留清理并入 AC-4 与垃圾测试 | 失败路径后 `os.TempDir()` 无 `aero-snapshot-*` 新增 | 同上 |

**测试 helper（`consistent_test.go`）：** `openTestDB(t, dbFile)`（`sql.Open("sqlite", "file:"+dbFile+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")` + `CREATE TABLE t(id INTEGER PRIMARY KEY, payload TEXT)` + marker 行 + Close）；`countingWriter`；`failingWriter`（**拒绝式**：`if w.n+int64(len(p)) > w.limit { return 0, io.ErrShortWrite }`，禁止部分写入——exp-10 实测部分写入会被 `compress/gzip` 静默容忍）。

**AC-2 测试注意：** 写者 goroutine 内不得调用 `t.Fatal`（非测试 goroutine），错误经 channel 传出；`committed` 用 `atomic.Int64`；写者停后用有界 select 收错误。恢复目标为全新 `t.TempDir()`。

---

## 8. 门禁与验收自检清单（implement 用）

- [ ] `gofmt -l .` 无输出（新代码先 gofmt）
- [ ] `go build ./...` · `go vet ./...` · `go test ./...` 全绿（零网络/零 Docker）
- [ ] 单文件行数：`snapshot.go` ≤ 500（~370）、`snapshot_test.go` ≤ 500（~390）、`consistent_test.go` ≤ 500（~240）；Makefile:162 实测豁免 `*_test.go`，双保险
- [ ] `python3 cli.py check` 通过（implement 阶段验证器 `gorepo,clirepo,completion_go`）
- [ ] 零新增 go.mod 依赖（I6）；测试仅 `testing`（无断言框架）
- [ ] 6 条既有测试迁移 + 3 条 AC 新测试 + 2 条 FR 附加测试，命名可供 `-run` 过滤
- [ ] `docs/deployment.md` 备份段 + `snapshot.go` Create 注释同步
- [ ] 明确不做（spec §5 原样）：Restore 陈旧 sidecar 清理（方向 2）、outPath 自包含（方向 3）、停服检测、Postgres/S3、格式版本化、gzip 级别/manifest

## 9. 风险与残余项

| 项 | 评估 |
|----|------|
| VACUUM INTO 产生完整 db 副本 → 临时磁盘占用 ≈ db 大小 | 可接受；`os.MkdirTemp` 默认在 `os.TempDir()`；文档注明大库备份需同量临时空间。checkpoint 方案在 live 场景不可用（§2），无更优替代 |
| `busy_timeout=5000` 硬编码 | 与 spec AC-2 基建一致；超限 = 响亮失败而非静默。将来可加配置，不在本方向范围 |
| 目标目录存在陈旧 `-wal`/`-shm`（Restore 侧） | 方向 2 明确排除；SQLite salt 不匹配会忽略陈旧 WAL（方向 2 分析原话，未验证——由方向 2 验收覆盖） |
| 恢复后立即开库生成 sidecar 的断言顺序 | AC-2 已规定"先断言无 sidecar 再开库"（§7） |
