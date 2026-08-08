# 方向：Create 产出事务一致的单文件 SQLite 镜像（checkpoint/VACUUM INTO）+ trailer 写错误响亮失败（验收规格 · 已验证现状）

> **模块：** `internal/snapshot`（`snapshot.go` · `snapshot_test.go` · `internal/cli/cli_snapshot.go`）
> **来源分析：** `docs/auto/analyses/internal-snapshot-42eae7cc.json`（方向 1）· **日期：** 2026-08-07 · **HEAD：** `acfaaf4`
> **评分：** 价值 8 / 风险降低 7 / 工作量 5 / 置信度 7
> **状态声明：** 本文是**验收契约**而非绿地设计：逐条核验方向引证（§1）、登记现状与既有测试影响面（§2）、严格限定范围的需求（§3）、原样保留四条验收检查并映射为可执行测试矩阵（§4）。超范围项一律不做（§5）。

---

## 1. 证据核验（方向引证逐条对照当前 HEAD）

| # | 方向引证 | 当前 HEAD 位置 | 核验结论 |
|---|---------|----------------|---------|
| E1 | `internal/snapshot/snapshot.go:22` — Create 原始拷贝 `aero.db + -wal + -shm` | `Create` 文档注释 :19-25（`:21` `./db/aero.db (+ -wal, -shm)`、`:22` `./objects/...`） | ✅ **行号精确**。注释即契约：当前格式 = db + 两个 sidecar 的原始拷贝 |
| E2 | `internal/snapshot/snapshot.go:46` — 原始拷贝 db/wal/shm | 拷贝调用在 `Create` :41 `addDBFiles(tw, dbFile)`；实现 `addDBFiles` :50-67：stat（:51-53）后逐文件 `addFile`（db 本体 :54 + 存在的 `-wal`/`-shm` :57-65） | ✅ **行号漂移**（46 → 41 / 50-67），语义成立：**无 SQLite 介入，纯文件拷贝** |
| E3 | `internal/snapshot/snapshot.go:31` — Create 先开 outPath | `f, err := os.Create(outPath)` :31 | ✅ **行号精确**。配合 :35/:37/:39 三个裸 defer（`defer f.Close()` / `defer gz.Close()` / `defer tw.Close()`）——**trailer 关闭错误全部丢弃**，方向"full disk 时返回 nil 却产出截断归档"断言成立 |
| E4 | `go.mod:27` — modernc.org/sqlite 已在依赖 | `go.mod:27` `modernc.org/sqlite v1.50.1`（require 块）；仓库唯一 SQLite 驱动，`internal/repository/sqlite.go:11` 空导入注册 | ✅ **行号精确**。checkpoint/VACUUM INTO 方案零新增依赖（I6 合规） |
| E5 | `docs/deployment.md:220` — CLI 直接调用、无停服强制 | `docs/deployment.md:220` `aero-vault cli snapshot create backup.tgz \`；文档 :216-217 仅声明 "while the server is stopped"，**无任何强制/检测**；`internal/cli/cli_snapshot.go` 不检查服务进程/DB 锁 | ✅ **行号精确**。文档级约束 = 软约束，Create 必须在服务存活时也能产出一致镜像 |

**补充核验（方向未引、判定实现约束的关键现状）：**

| # | 位置 | 现状 | 结论 |
|---|------|------|------|
| S1 | `internal/repository/sqlite.go:31` | 服务端打开 DB 时 `PRAGMA journal_mode = WAL` | **WAL 是生产主路径**，方向场景（WAL + 并发写）即默认部署形态 |
| S2 | `internal/snapshot/snapshot_test.go:1-8` | 测试仅导入 `archive/tar` `compress/gzip` `os` `path/filepath` `testing`——**零 SQLite 依赖**；`TestRoundTrip` :103 用假字节 `"SQLite format 3\x00main-db-bytes"` + 假 `-wal`/`-shm` 文件 | ✅ 方向"no test exercises Create against a live DB"成立：**无真实 DB、无并发写、无 integrity_check** |
| S3 | `internal/snapshot/snapshot_test.go:315-316` | 仅 `TestRestoreRejects*` 系（:200/:227/:246/:257/:280）手工构造 tar 归档 | Restore 校验路径测试不经 Create，**不受 Create 改造影响** |
| S4 | 全仓 grep `wal_checkpoint` / `VACUUM INTO` | 仓库**无任何** checkpoint/VACUUM 先例 | 新机制为首次引入，无既有行为可依赖 |
| S5 | `internal/snapshot/snapshot_test.go:76` | `TestCreate_MissingMainDBFails`：db 文件缺失 → Create 必须报错（注释明言"undetected empty backup"是既有守护目标） | **实现约束：** SQLite 驱动打开不存在的文件会**新建空库**——新机制必须保留"文件缺失即报错"（stat 前置或只读打开），否则该守护丢失 |
| S6 | `internal/cli/cli_snapshot.go:33-38` | CLI 默认 `DB_DSN` 环境变量否则 `file:./var/aero.db` | CLI 层零改动（`Create`/`Restore` 签名不变），改造全在 `internal/snapshot` |

**方向问题陈述核验：**

| 陈述 | 核验 |
|------|------|
| "Create raw-copies aero.db + -wal + -shm while the server may hold them open" | ✅ 成立（E1/E2/E5） |
| "concurrent writer can yield a torn db/wal pair that restores as corrupt or silently loses recent commits" | ✅ 机制成立（拷贝与写者无同步）；**未实测**（S2，无 live-DB 测试） |
| "-shm is archived/restored even though SQLite rebuilds it; stale -shm/-wal mixing adds inconsistency surface" | ✅ 成立（E2 归档 `-shm`；Restore `unpackToRoots` :162 写回 `-wal`/`-shm`） |
| "tw.Close()/gz.Close() errors are dropped via bare defers → full disk at trailer time returns nil" | ✅ 成立（E3，:35/:37/:39 三个裸 defer） |
| "modernc.org/sqlite already a go.mod dependency → no new deps" | ✅ 成立（E4） |

---

## 2. 现状：破坏面与既有防线

**Create 现状流程（`snapshot.go:26-50`）：** `os.Create(outPath)` → `gzip.NewWriter` → `tar.NewWriter`（三处裸 defer，错误全丢）→ `addDBFiles`（stat 后逐字节拷贝 db + `-wal` + `-shm`）→ `addObjectFiles`（`filepath.Walk` 全量归档）。**全程无 SQLite 参与**：写者提交的 WAL 帧与 db 文件拷贝无任何同步点；`-wal` 与 db 各取一时快照，二者天然可能不匹配。

**部署文档（`docs/deployment.md:215-222`）：** 以 "while the server is stopped" 为前提给出 CLI 命令，但 CLI 无任何停服检测（S6）——约束纯靠运维自觉，违反时产生静默损坏备份（方向核心）。

**既有测试影响面（改造后必须迁移，见 §6）：**

| 测试 | 位置 | 现状数据 | 改造后状态 |
|------|------|---------|-----------|
| `TestCreate_MissingMainDBFails` | :76 | db 文件不存在 | **保留语义**（S5：缺失 → 报错），测试本身无需迁移 |
| `TestCreate_MissingObjectsRootIsOK` | :84 | 假字节 `[]byte("dbcontent")` | **需迁移**：Create 将经 SQLite 打开 db，假字节 → 报错 |
| `TestRoundTrip` | :103 | 假 SQLite 头 + 假 wal/shm，断言 sidecar 字节往返 | **需迁移**：真实 DB；sidecar 往返断言被新格式取代（§4 AC-2） |
| `TestRoundTrip_OnlyDBNoSidecars` | :148 | 假字节 `[]byte("just-the-db")` | **需迁移**：真实 DB（其余断言不变） |
| `TestRestore_OverwritesExisting` | :174 | 假字节 `[]byte("new-contents")` | **需迁移**：真实 DB |
| `TestRestore_BadDSNErrors` | :60 | 经 Create 构造归档（假字节 `"x"`） | **需迁移**：真实 DB |
| `TestRestoreRejects*` 五条 | :200-:315 | 手工构造归档 | **不受影响**（S3） |

**Restore 侧无需改动：** `validateSnapshot`/`unpackSnapshot` 已支持无 sidecar 归档（`TestRoundTrip_OnlyDBNoSidecars` :148 即为先例），新格式天然兼容；旧归档（含 `-wal`/`-shm` 条目）继续可恢复——`entryDBWAL`/`entryDBSHM` 分支保留即向后兼容。

---

## 3. 需求规格（FR，范围严格限定于方向）

### FR-1：Create 产出事务一致的单文件 SQLite 镜像

`Create` 不再原始拷贝 `aero.db`/`-wal`/`-shm`，改为经 `modernc.org/sqlite`（既有依赖，E4）打开 DB 并产出**单个、自洽**的 db 镜像归档：

- 打开方式必须容忍**服务进程同时持有该 DB**（SQLite 多连接/多进程锁语义），配 busy-timeout，瞬时写锁不失败；
- 机制二选一（实现自由，契约一致）：
  - **VACUUM INTO**（推荐）：对任意 journal mode 有效，读取连接所见完整已提交状态，天然一致；
  - **checkpoint 后拷贝**：`PRAGMA wal_checkpoint(TRUNCATE)` 完成后拷贝 db 单文件；**checkpoint 因读者 busy 无法完整完成时必须响亮报错**（busy 状态下拷贝 = 静默丢弃未 checkpoint 的近期提交，恰是本方向要消灭的失效模式）；
- 镜像必须包含 **Create 被调用前已提交的全部事务**（freshness 契约，见 AC-1 计数断言）；
- 归档中 db 条目**唯一**，且**不得出现 `-wal`/`-shm` 条目**（AC-2）；
- 机制产生的临时中间文件（如 VACUUM INTO 目标）在失败路径必须清理，不得残留；
- **保留既有错误契约：** db 文件不存在 → 报错（S5，stat 前置或只读打开，不得让驱动静默新建空库）；objectsRoot 不存在 → 仍成功（既有 `TestCreate_MissingObjectsRootIsOK` 契约）；
- **新增响亮失败：** db 文件存在但不是合法 SQLite 库（今日会被静默归档进备份）→ 报错。

### FR-2：trailer 写入错误响亮失败（全链 Close 错误传播）

- 消除 `snapshot.go:35/37/39` 三个裸 defer：`f.Close()`、`gz.Close()`、`tw.Close()` 的返回错误全部检查并传播，任何一步失败 → `Create` 返回**非 nil 错误**（wrap 上下文，如 `snapshot: finalize archive: %w`），即使正文数据已写完；
- 正文 `io.Copy` 错误（既有路径）保持传播，不被 Close 错误遮蔽（任一非 nil 即可）；
- **可测试性缝：** 归档写出逻辑下沉为接受 `io.Writer` 的内部函数（如 `createArchive(w io.Writer, dbFile, objectsRoot string) error`），公开 `Create` 保持签名 `Create(outPath, dbPath, objectsRoot string) error` 不变（`os.Create` 后调缝）——AC-3 据此注入失败 writer。

### FR-3：格式与接口兼容

- 归档**布局不变**：`db/<basename>` + `objects/...`；`Restore`/`validateSnapshot` 不动；
- `Restore` 继续接受含 `-wal`/`-shm` 条目的**旧归档**（`entryDBWAL`/`entryDBSHM` 分支保留）——既有备份不失效；
- `Create`/`Restore` 公开签名、CLI（`internal/cli/cli_snapshot.go`）零改动；
- 同步更新 `Create` 文档注释（:19-25 仍宣称 `(+ -wal, -shm)`）与 `docs/deployment.md:215-222` 备份段（"while the server is stopped" 前提取消，改为描述一致性保证）。

---

## 4. 验收标准（方向原文四条，原样保留并测试化）

### AC-1 `go test ./internal/snapshot passes`

> 方向原文：*go test ./internal/snapshot passes*

依赖 AC-1/2/3 三条新测试 + 全部既有测试迁移（§6 迁移矩阵）后全绿；同时 `make check`（gofmt/build/vet/test、单文件 ≤ 500 行）保持通过。新测试命名约定（供未来 `-run` 过滤器）：`TestCreate_LiveWALDB_ConcurrentWriter_Consistent` · `TestCreate_ArchiveHasNoSidecarEntries` · `TestCreate_TrailerWriteError_ReturnsError`。

### AC-2 新测试：live WAL DB + 并发写者 → Create+Restore 后 integrity_check 通过

> 方向原文：*New test: with a SQLite db opened in WAL mode and a concurrent writer committing, Create+Restore yields a db that opens and passes PRAGMA integrity_check*

**测试 `TestCreate_LiveWALDB_ConcurrentWriter_Consistent`（`internal/snapshot/snapshot_test.go`，包内测试）：**

| 步骤 | 细节 |
|------|------|
| 基建 | `sql.Open("sqlite", "file:"+db+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")`（`_ "modernc.org/sqlite"` 空导入——既有 go.mod 依赖，I6 合规，无断言框架）；建表 `t(id INTEGER PRIMARY KEY, payload TEXT)` 并插入种子行 |
| 并发写者 | goroutine 循环 `BEGIN`/`INSERT`/`COMMIT`（每次提交 = 一个 WAL 帧），直到 stop channel 关闭；`sync.WaitGroup` + 原子计数 |
| 同步点 | 写者提交 ≥ 1 帧后，记录 `committedBefore = 当前计数`，**此刻调用 `Create`**；写者在 Create 期间持续提交 |
| 恢复 | Create 返回 → 停写者 → `Restore` 到全新目录 → 用现代码开库 |
| 断言 | ① 打开成功；② `PRAGMA integrity_check` 返回 `ok`；③ `SELECT COUNT(*) FROM t` ≥ `committedBefore`（**freshness 契约**：Create 调用前已提交的提交不得丢失——方向核心"silently loses recent commits"的回归护栏）；④ 恢复目录无陈旧 sidecar（新格式天然满足，防御性断言） |

> 断言 ③ 是"可测试化"的关键补强：仅 integrity_check 通过无法区分"一致但丢帧"与"一致且完整"，计数断言直接钉死 freshness 契约。VACUUM INTO 与 TRUNCATE checkpoint 两机制均满足（checkpoint 在 Create 被调用之后执行，故此前提交必被纳入）。

### AC-3 新测试：Create 归档不含 `-wal`/`-shm` 条目

> 方向原文：*New test: Create archive contains no -wal/-shm entries after checkpointing*

**测试 `TestCreate_ArchiveHasNoSidecarEntries`：**

| 步骤 | 断言 |
|------|------|
| 真实 WAL DB（复用 AC-2 基建的建库 helper）+ 少量对象文件 → `Create` | 用 `tar.NewReader` + `classifySnapshotEntry`（包内可用）遍历归档：**无** `entryDBWAL`/`entryDBSHM` 条目；**恰一个** `entryDBMain`（`validateSnapshot` 的"主库缺失即拒"契约延续）；db 条目内容以 `SQLite format 3\x00` 头开头（真实库证据） |
| 对照：改造前格式（含 sidecar 条目）经 `Restore` 仍可恢复 | 手工构造含 `db/x.db-wal`/`db/x.db-shm` 的旧式归档 → `Restore` 成功（FR-3 向后兼容，复用 :315 手工构造 helper） |

### AC-4 新测试：强制 gzip/tar Close 失败 → Create 返回非 nil 错误

> 方向原文：*New test: forcing gzip/tar Close to fail (e.g., full-disk via short-write writer) makes Create return a non-nil error*

**测试 `TestCreate_TrailerWriteError_ReturnsError`（依赖 FR-2 的 writer 缝）：**

| 步骤 | 细节 |
|------|------|
| ① 基线计数 | 以 `countingWriter` 调 `createArchive(w, dbFile, objectsRoot)`，得总字节数 `total` |
| ② 阈值失败 | 以 `failingWriter{limit: total - 1}`（前 `total-1` 字节成功、最后一字节报 `io.ErrShortWrite`/`ENOSPC` 类错误）再调 `createArchive` |
| ③ 确定性论证 | 最后一次 `addFile` 返回后，writer 上不再有正文写入；**最后一字节必然落在 `tw.Close()`（tar trailer 1024 零块）或 `gz.Close()`（gzip footer + 刷新）路径**——阈值 = total-1 精确命中 trailer 关闭阶段，而非正文 `io.Copy`（后者今日已传播错误，非本测试目标） |
| 断言 | ② 返回**非 nil 错误**；同时验证正常 writer（①）返回 nil |
| 附加 | 同一 failing writer 经公开 `Create` 的全路径（outPath 为真实文件时无法注入，故以缝为锚；公开 `Create` 的非 nil 传播由缝单测 + 代码审查覆盖） |

> 若实现采用"命名返回值 + 显式关闭"而非缝，测试必须仍能以等价手段注入 trailer 失败；缝方案（FR-2）是本验收的推荐实现路径。

---

## 5. 范围边界（明确不做）

| 不做 | 理由 |
|------|------|
| Restore 清除目标目录陈旧 sidecar/孤儿对象（"destination must mirror snapshot"） | 同分析文件**方向 2**，独立验收与实现，不并入 |
| Create 输出路径位于 objectsRoot 内的自包含防护 | 同分析文件**方向 3**，独立验收与实现，不并入 |
| CLI/服务端"停服检测"或 DB 独占锁强制 | 方向意图是**容忍** live DB（checkpoint/VACUUM INTO），而非强制停服；停服检测属另一问题 |
| Postgres/S3 快照支持 | 方向明示仅 SQLite+local-FS；`Create` 的 DSN 解析错误契约（`TestCreate_BadDSNErrors` :53）保持 |
| 归档格式版本化/迁移（magic header、v2 布局） | FR-3 已保证旧归档可恢复，无需版本机制 |
| gzip 压缩级别、manifest/校验和条目、增量快照 | 不在方向四步验收内 |
| Restore 侧任何行为改动 | 方向验收全部在 Create 侧（AC-1 的 Restore 仅作为验证步骤） |

---

## 6. 基线影响

- **测试迁移（AC-1 前置条件）：** §2 表中 5 条用假字节经 `Create` 的既有测试（:60/:84/:103/:148/:174）迁移为真实 SQLite DB（`modernc.org/sqlite` 建库 helper 复用）；`TestRoundTrip` :139-140 的 sidecar 字节往返断言**删除**（被 AC-3 的新格式断言取代）；`TestCreate_MissingMainDBFails` :76 语义保留（S5 约束的实现保证）；`TestRestoreRejects*` 五条不动（S3）。
- **行为收紧（有意为之）：** ① 今日"db 文件是垃圾字节仍静默归档" → 改造后响亮报错（FR-1）；② 今日"磁盘满 → 返回 nil 的截断归档" → 非 nil 错误（FR-2）；③ 今日"活库 + 并发写 → 撕裂镜像" → 一致单文件镜像（FR-1）。
- **行为不变：** 归档布局、`Create`/`Restore` 签名、CLI 输出、旧归档恢复能力（FR-3）。
- **依赖：** 零新增 go.mod 依赖（E4，I6 合规）；测试仅用 `testing`（无断言框架）。
- **文档：** `snapshot.go` Create 注释 + `docs/deployment.md` 备份段同步（FR-3）。
- **门禁：** `go test ./internal/snapshot` + 全仓 `go test ./...` + `make check` 全绿；`snapshot.go` 改动后仍 ≤ 500 行。
