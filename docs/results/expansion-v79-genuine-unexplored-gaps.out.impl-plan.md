# Tech Lead 分析报告：工程完整性盲区修复

---

## 1. 任务分解

将 5 个盲区拆解为 14 个可执行任务，每个任务 1–6 小时，均含明确的验收标准。

### 1.1 任务清单

| Task ID | 标题 | 方向 | 涉及文件 | 前置依赖 | 工时 | 验收标准 |
|---------|------|------|---------|---------|------|---------|
| **T1.1** | RetentionJob 增加 ChunkCleaner 调用 | 方向一 | `internal/reconcile/retention.go` + `cmd/server/main.go` | 无 | 2h | `purgeSoftDeleted` 在 `HardDeleteObject` 前调用 `chunkCleaner.DeleteObjectChunks`；现有 test 全绿 |
| **T1.2** | softDeleteObject 增加 ChunkCleaner 兜底 | 方向一 | `internal/service/file_crud.go` | 无 | 1h | `softDeleteObject` 同步调用 `chunkCleaner.DeleteObjectChunks`（幂等，失败仅 warn log）；`make check` 全绿 |
| **T1.3** | ReconcileChunksJob — chunk 孤儿巡检 | 方向一 | `internal/reconcile/chunk_reconcile.go`（新文件） + `internal/repository/*` + `cmd/server/main.go` | T1.1（接口模式） | 5h | 启动后扫描 chunk 表，识别硬删除/软删除对象的孤儿 chunk 并清理；暴露 `chunk_orphan_count` 指标 |
| **T2.1** | CompleteMultipartUpload ETag 交叉验证 | 方向二 | `internal/api/s3compat/extra.go` + `internal/service/file_multipart.go` | 无 | 3h | 解析客户端 part 列表；Client ETag ≠ Server ETag → 返回 `InvalidPart`；客户端缺失 part → 返回 `InvalidPart`；`ListParts` 测试增强 |
| **T2.2** | 客户端 part 列表作为 CompleteMultipart 权威来源 | 方向二 | `internal/api/s3compat/extra.go` + `internal/service/file_multipart.go` | T2.1 | 2h | 以客户端列表为准合并（AWS S3 行为）；服务端列表仅做兜底验证；幂等调用返回相同结果 |
| **T3.1** | Web UI 管理标签页（只读监控） | 方向三 | `internal/webui/static/index.html` + `internal/webui/web.go`（必要时） | 无 | 5h | 新增「管理」tab，含：存储统计、租户列表、Job 队列状态、审计日志流（最近 50 条）；响应式布局 |
| **T3.2** | Web UI 桶管理 + API Key 管理 | 方向三 | `internal/webui/static/index.html` | T3.1 | 5h | 桶创建/删除（含确认对话框）；桶配置编辑器（versioning/CORS/策略）；API Key 列表/创建/吊销 |
| **T3.3** | Web UI 对象管理增强 | 方向三 | `internal/webui/static/index.html` | T3.2 | 5h | 对象上传（指定 key + 进度条）；下载按钮；删除（软/硬删除选项）；版本列表浏览；标签编辑 |
| **T4.1** | S3 Select CSV 最小实现 | 方向四 | `internal/api/s3compat/select.go`（新文件） + `router.go` + `handler.go` + `xml.go` | 无 | 7h | `?select` 路由注册；CSV Select: 支持 SELECT + WHERE + LIMIT；流式读取/写入；SQL 注入防护 |
| **T4.2** | S3 Select JSON 支持 | 方向四 | `internal/api/s3compat/select.go` | T4.1 | 4h | JSON Lines 和 Document 模式；点号路径投影（`SELECT user.name`） |
| **T5.1** | Object 乐观锁（Version 列 + CAS） | 方向五 | `migrations/{sqlite,postgres}/NNNN_*` + `internal/repository/sql_objects.go` + `internal/repository/repository.go` | 无 | 5h | 新增 `version` 列（默认 1，单调递增）；`UpsertObject` 改为 `UPDATE ... WHERE version = oldVersion`；冲突返回 `ErrConflict`；迁移文件 I2 合规 |
| **T5.2** | FileService.Put CAS 冲突重试 | 方向五 | `internal/service/file_crud.go` | T5.1 | 2h | Put 检测 `ErrConflict` → 读取当前对象 → 重试（最多 3 次）；并发 Put 同一 key 不再产生孤儿 blob |
| **T5.3** | hardDeleteObject 事务补偿 | 方向五 | `internal/service/file_crud.go` | 无 | 3h | hardDelete 三步失败时幂等重试（最多 3 次）；重试无法恢复 → warn log + 保留删除标记供 Reconcile 补偿 |

### 1.2 任务关系总结

```
T1.1  T1.2  T2.1  T3.1  T4.1  T5.1  T5.3    ← 第一波：零依赖或低风险
  │     │     │     │      │     │     │
  │     │     │     │      │     │     └── 独立 P2（方向五 Phase 2）
  │     │     │     │      │     │
  │     │     │     │      │     └──── T5.2 ← 依赖 T5.1
  │     │     │     │      │
  │     │     │     │      └────── T4.2 ← 依赖 T4.1
  │     │     │     │
  │     │     │     └──────── T3.2 → T3.3 ← Web UI 递进
  │     │     │
  │     │     └────────── T2.2 ← 依赖 T2.1
  │     │
  │     └──────────────── T1.3 ← 依赖 T1.1（接口模式复用）
  │
  └──────────────────────────────────── 方向一其他任务无依赖
```

---

## 2. 执行顺序与依赖图

```mermaid
graph TB
    subgraph Phase1["Phase 1: Data Integrity (Days 1-3)"]
        T11[T1.1 - Retention ChunkCleaner 🔴 P1]
        T12[T1.2 - softDelete ChunkCleaner 🔴 P1]
        T21[T2.1 - ETag Cross-Validation 🔴 P1]
        T51[T5.1 - Object Version + CAS 🔴 P1]
        T53[T5.3 - hardDelete Compensation 🔴 P1]
    end

    subgraph Phase2["Phase 2: Concurrency + Admin (Days 4-7)"]
        T52[T5.2 - Put CAS Retry]
        T13[T1.3 - ReconcileChunksJob]
        T22[T2.2 - Client List Authoritative]
        T31[T3.1 - Web UI Admin Tab (Read-Only)]
    end

    subgraph Phase3["Phase 3: Web UI + Select (Days 8-14)"]
        T32[T3.2 - Bucket + Key Management UI]
        T33[T3.3 - Object Management UI]
        T41[T4.1 - CSV S3 Select]
    end

    subgraph Phase4["Phase 4: Polish (Days 15-17)"]
        T42[T4.2 - JSON S3 Select]
    end

    T11 --> T13
    T21 --> T22
    T51 --> T52
    T31 --> T32
    T32 --> T33
    T41 --> T42

    T11 -.->|same interface| T12

    linkStyle 0,1,2,3,4,5,6 stroke-width:2px
```

**可并行执行的组：**

| 并行组 | 任务 | 预计总工时 |
|--------|------|-----------|
| **组 A**（Day 1-3） | T1.1, T1.2, T2.1, T5.1, T5.3, T3.1 的前期设计 | 14h（2 人并行 ≈ 1.5 天） |
| **组 B**（Day 3-5） | T5.2, T1.3, T2.2, T3.1 开发 | 14h（2 人并行 ≈ 1.5 天） |
| **组 C**（Day 6-10） | T3.2, T4.1 | 12h（2 人并行 ≈ 1.5 天） |
| **组 D**（Day 10-14） | T3.3, T4.2 | 9h（2 人并行 ≈ 1 天） |

---

## 3. 技术风险

### 3.1 风险矩阵

| 风险 | 方向 | 概率 | 影响 | 缓解策略 |
|------|------|------|------|---------|
| **Version 列 CAS 导致已有并发模式崩溃** | 方向五 | 中 | **高** — `UpsertObject` 在 lifecycle、reconcile、multipart 等多处调用，新增 `version` 验证可能破坏现有逻辑 | ① 渐进式改造：先只为 `FileService.Put` 路径加 CAS，保留兼容的 `UpsertObject` 签名；② 所有调用方审计后统一升级；③ 新增 `UpsertObjectWithCAS` 方法，旧 `UpsertObject` 弃用 |
| **ChunkCleaner 注入 RetentionJob 破坏 cluster singleton** | 方向一 | 低 | **中** — `RetentionJob` 当前无外部依赖，引入 `ChunkCleaner` 接口后测试需 mock | 使用和 `FileService.WithChunkCleaner` 相同的 fluent builder 模式，零侵入现有构造 |
| **EventBus dropped 计数已存在但无补偿** | 方向一 | 低 | **低** — T1.2 的同步调用已解决软删除路径 | 无额外缓解需要 |
| **S3 Select 流式 CSV 解析器对大对象的内存压力** | 方向四 | 中 | **中** — 10GB CSV 不能一次性读入内存 | ① 行级迭代器模式（`bufio.Scanner`）；② `MaxScanRowLimit` 配置（默认 1M 行）；③ 分阶段测试 100MB+ 文件 |
| **Web UI 管理操作鉴权** | 方向三 | 低 | **中** — 管理 UI 暴露写操作后，非 admin scope 用户可能误操作 | ① UI 隐藏管理 tab（权限检测 → 隐藏）；② API 侧 scope 校验已存在；③ 所有写操作二次确认对话框 |
| **pgvector/Qdrant 孤儿 chunk 清理的跨服务一致性** | 方向一 | 低 | **中** — T1.3 的 ReconcileChunksJob 需要连接外部向量存储 | ① 利用已有的 `ChunkSink.DeleteObjectChunks` 接口；② Qdrant/pgvector 接口层已是幂等设计；③ 出问题只 warn log，不阻塞主流程 |
| **迁移双文件合规（I2）** | 方向五 | 低 | **中** — version 列迁移需同时编写 `{sqlite,postgres}/NNNN_*.{up,down}.sql` | 模板化迁移文件，严格执行 `I2` 规则；`make test-migrations` 验证升降级 |

### 3.2 外部依赖

| 依赖 | 方向 | 版本要求 | 备注 |
|------|------|---------|------|
| `expr` 库（或 SQL parser） | T4.1 | 无现有依赖 | 建议使用轻量 `expr` 库解析 SQL WHERE 子句，而非引入完整 SQL 解析器 |
| Qdrant / pgvector | T1.3 | — | ReconcileChunksJob 通过 `ChunkSink` 接口操作，不直接依赖外部存储 |
| 无其他新 Go 依赖 | 全部 | — | 所有任务均可在标准库 + 现有依赖范围内完成 |

### 3.3 性能瓶颈

| 场景 | 瓶颈点 | 分析 |
|------|--------|------|
| T1.3 首次运行扫描全量 chunk 表 | Chunk 表扫描 | SQLite 下万级 chunk 扫描 < 100ms；pgvector/Qdrant 需 API 分页遍历。建议首次运行分批（1000/batch）+ 进度 log |
| T4.1 大对象 CSV Select | 行级解析 + 内存分配 | 使用 `bufio.Scanner` + 预分配行 buffer；`LIMIT` 提前截断 |
| T5.1 CAS 重试 | 冲突频繁时的重试开销 | 预期冲突极低（非版本化桶的并发 Put 在生产中不常见）。若冲突率 > 1%，引入指数退避 |

---

## 4. 资源评估

### 4.1 人员需求

| 角色 | 技能要求 | 负责任务 | 人数 |
|------|---------|---------|------|
| **Go 后端工程师 A** | Go 1.25、SQLite/Postgres、并发编程、存储系统 | T1.1, T1.2, T1.3, T5.1, T5.2, T5.3 | 1 |
| **Go 后端工程师 B** | Go 1.25、S3 协议、XML 处理、流式处理 | T2.1, T2.2, T4.1, T4.2 | 1 |
| **全栈工程师** | HTML/CSS/JS（vanilla）、REST API 集成、响应式设计 | T3.1, T3.2, T3.3 | 1 |
| **QA 工程师** | Go testing、集成测试、CI gate、性能基准 | 全量任务 | 1（可兼职） |

> **最小团队：2 人（Go 后端 ×1 + 全栈 ×1），但 3 人并行可将工期压缩 40%。**
>
> 工程约束提示：所有修改文件须遵守行数限制（≤500行/file）、圈复杂度（≤10/func）、God 类型（≤300行/type）。`file_crud.go` 当前 420 行，新增 T1.2 + T5.2 + T5.3 后接近上限，需准备提取 `file_delete.go` 分离 Delete 逻辑。

### 4.2 关键里程碑

| 里程碑 | 时间 | 交付物 | 验证方式 |
|--------|------|--------|---------|
| **M1：P1 修复完成** | Day 3 EOD | T1.1, T1.2, T2.1, T5.1, T5.3 全部合并 | `make check` 全绿；ETag 交叉验证 manual test |
| **M2：数据一致性加固** | Day 7 EOD | T5.2, T1.3, T2.2 合并 | 并发 Put/Delete 场景的 integration test 通过 |
| **M3：管理 UI 可用** | Day 12 EOD | T3.1, T3.2, T3.3 可操作 | Web UI 管理 tab 可创建桶、管理 API Key、上传/下载文件 |
| **M4：S3 Select MVP** | Day 14 EOD | T4.1 合并 | CSV SELECT 查询返回正确结果 |
| **M5：全量发布** | Day 17 EOD | T4.2 + 集成测试 + 文档 | 全部 feature gate 保护；发布说明完成 |

### 4.3 阻塞点与解决策略

| 阻塞点 | 影响 | 解决策略 |
|--------|------|---------|
| `file_crud.go` 行数接近 500 行限制 | T5.2/T5.3/T1.2 需在此文件增加 ~90 行 → 超限 | **立即执行：** 开始 T5.1 前先创建 `file_delete.go`，将 `hardDeleteObject`、`softDeleteObject`、`Delete` 迁入，作为零号任务（30min） |
| T5.1 迁移文件版本号冲突 | 多人并行开发时迁移编号 | 每人锁定一个迁移编号范围（如 Engineer A: 0066-0068, Engineer B: 0069-0070）；Code Review 时检查编号递增 |
| T4.1 `expr` 库引入评审 | Go 依赖新增需论证 | 查阅 `go.mod` 现有依赖，若 `expr` 不可接受则使用手写简单 SQL tokenizer + ast（+150 行但零依赖） |

---

## 5. 质量保证

### 5.1 单元测试覆盖

| 任务 | 测试重点 | 新增测试文件/函数 | 覆盖要求 |
|------|---------|-----------------|---------|
| T1.1 | RetentionJob 注入 mock ChunkCleaner → 验证调用 | `internal/reconcile/retention_test.go` 补充 | 100% 路径覆盖 |
| T1.2 | softDeleteObject 调用 ChunkCleaner（成功 + 失败） | `internal/service/file_crud_test.go`（或 `file_delete_test.go`） | 2 个 case |
| T1.3 | ReconcileChunksJob 扫描 + 清理孤儿 chunk | `internal/reconcile/chunk_reconcile_test.go` | 4 cases: 正常清理/空表/软删除块/查询失败 |
| T2.1 | CompleteMultipart 客户端 ETag vs 服务端 ETag 匹配/不匹配/缺失 part | `internal/api/s3compat/extra_test.go` | 5 cases: 匹配/不匹配/缺失/重复/空 |
| T2.2 | 客户端 part 列表权威性验证 | 同上 | 3 cases |
| T3.1-T3.3 | JS 组件测试（可选，非阻塞） | `internal/webui/static/` 中可选 | 功能验证通过 manual testing |
| T4.1 | CSV Select: 列投影/行过滤/空结果/错误格式/SQL 注入 | `internal/api/s3compat/select_test.go` | 8 cases |
| T4.2 | JSON Select: 点号路径/文档模式/JSON Lines | 同上 | 4 cases |
| T5.1 | UpsertObject version CAS 命中/未命中/冲突返回 | `internal/repository/sql_objects_test.go` | 6 cases |
| T5.2 | Put CAS 重试成功/重试耗尽/非冲突错误不重试 | `internal/service/file_crud_test.go`（或 `file_put_test.go`） | 4 cases |
| T5.3 | hardDelete 重试成功/重试失败后 warn log | `internal/service/file_crud_test.go` | 3 cases |

### 5.2 集成测试策略

| 场景 | 测试方式 | 备注 |
|------|---------|------|
| Retention + ChunkCleaner 组合 | `//go:build integration` + SQLite + BM25 index | 验证全链路：软删除 → 保留期到 → chunk 被清理 |
| CompleteMultipart ETag 验证 | handler test with `httptest` | 模拟客户端发送 part 列表，验证错误返回 |
| 并发 Put + Delete CAS | `go test -race -count=5` | 3 个 goroutine 并发 Put + 2 个 goroutine 并发 Delete → 数据一致性 |
| S3 Select CSV | handler test with `httptest` | 小 CSV（10 行）→ SELECT/WHERE/LIMIT/LIMIT 0 |
| Web UI 管理 API | 人工 + Puppeteer 脚本（可选） | 通过 REST API 验证 UI 交互结果 |

### 5.3 代码审查要点

| 关注点 | 详细要求 |
|--------|---------|
| **I1 — SQL 占位符** | 新加的 `$N` 不得复用，`rebind` 正确处理 |
| **I2 — 迁移双文件** | 必须同时提交 `{sqlite,postgres}/NNNN_*.{up,down}.sql` |
| **I4 — Middleware 顺序** | 任何新增路由不得绕过 middleware 链 |
| **I5 — Opt-in 安全** | T1.3 的 ReconcileChunksJob 默认 off；T4.1/T4.2 的 S3 Select 默认 off |
| **I6 — Stdlib 优先** | 新依赖需要明确论证（特别是 `expr` 库） |
| **`ChunkCleaner` 失败非致命** | 所有调用点失败时只 warn log，不阻断主流程 |
| **`file_crud.go` 行数** | 修改后 > 500 行需先拆分（提取 `file_delete.go`） |

### 5.4 性能测试需求

| 场景 | 基准 | 工具 |
|------|------|------|
| T1.3 全表 chunk 扫描（10K chunks） | < 5s | `go test -bench` |
| T4.1 CSV Select 100MB 文件 | 流式 < 30s 首行返回时间 | `wrk` 或 `curl + time` |
| T5.1 CAS 重试（1% 冲突率） | 无显著 P50/P99 变化 | 并发 `go test -bench` |

---

## 6. 实施计划

### 甘特图（日历时间，2 人并行）

```
Day  1  2  3  4  5  6  7  8  9 10 11 12 13 14 15 16 17
     │  │  │  │  │  │  │  │  │  │  │  │  │  │  │  │  │
     ├── Phase 1: P1 Critical ──┤
T1.1  ■■                         │  Retention ChunkCleaner
T1.2  ■                          │  softDelete ChunkCleaner
T2.1  ■■■                        │  ETag Cross-Validation
T5.1  ■■■■■                      │  Version + CAS (migration)
T5.3  ■■■                        │  hardDelete Compensation
     │
     ├── Phase 2: Hardening + Admin ─────┤
T5.2     ■■                               │  Put CAS Retry
T1.3     ■■■■■                            │  ReconcileChunksJob
T2.2     ■■                               │  Client List Authoritative
T3.1     ■■■■■                            │  Web UI Admin (Read-Only)
     │
     ├── Phase 3: Web UI Full + Select ──────────┤
T3.2           ■■■■■                            │  Bucket + Key Management
T3.3           ■■■■■                            │  Object Management
T4.1           ■■■■■■■                          │  CSV S3 Select
     │
     ├── Phase 4: Polish ────┤
T4.2                    ■■■■ │  JSON S3 Select
     │                        │
     ╞═ M1 ═╡    ╞═ M2 ═╡         ╞═ M3 ═╡    ╞═ M5 ═╡
     P1 Done  Data Consist.       UI Done      Release
```

### 6.1 阶段 1：数据完整性关键修复（Day 1–4）

**目标：** 堵住 P1 级别的数据损坏/残留风险，确保数据完整性不退化。

| 任务 | 开始 | 结束 | 负责人 |
|------|------|------|--------|
| **零号任务：** 拆分 `file_crud.go` → 创建 `file_delete.go` 容纳 Delete 族函数 | Day 1 AM | Day 1 AM | A |
| T1.1: RetentionJob 增加 ChunkCleaner 接口 | Day 1 AM | Day 1 PM | A |
| T1.2: softDeleteObject 增加 ChunkCleaner 调用 | Day 1 PM | Day 1 PM | A |
| T2.1: CompleteMultipart ETag 交叉验证 | Day 1 AM | Day 2 AM | B |
| T5.1: Object version 列 migration + CAS UpsertObject | Day 1 AM | Day 3 PM | A |
| T5.3: hardDeleteObject 幂等重试 | Day 2 AM | Day 3 PM | A |
| **里程碑 M1** 验收：P1 修复全部合并，`make check` 全绿 | Day 3 EOD | | All |

**风险缓解：**
- 零号任务优先：`file_crud.go` 当前 420 行，T1.2 + T5.3 合计 ~60 行 → 不拆分则超限。必须在任何修改前提取 `file_delete.go`。
- T5.1 迁移文件双文件需同步评审，防止版本冲突。

### 6.2 阶段 2：并发加固 + 监控（Day 4–8）

**目标：** CAS 乐观锁上线 + 孤儿 chunk 巡检 + Web UI 只读管理面板。

| 任务 | 开始 | 结束 | 负责人 |
|------|------|------|--------|
| T5.2: Put CAS 冲突重试循环 | Day 4 AM | Day 4 PM | A |
| T1.3: ReconcileChunksJob 实现 | Day 4 AM | Day 6 PM | A |
| T2.2: 客户端 part 列表权威性改造 | Day 4 AM | Day 4 PM | B |
| T3.1: Web UI 管理标签页（只读） | Day 4 AM | Day 7 PM | C |
| 集成测试：并发 Put/Delete CAS | Day 6 AM | Day 7 PM | A+B |
| **里程碑 M2** 验收：数据一致性加固完成 | Day 7 EOD | | All |

**风险缓解：**
- T5.2 依赖 T5.1 完成，不能提前开始。可以利用 T5.1 等待 CR 的时间做其他事。
- T1.3 与 T1.1 共享 `ChunkCleaner` 接口，保证接口一致。

### 6.3 阶段 3：Web UI 全管理 + S3 Select MVP（Day 8–14）

**目标：** 补齐产品级管理能力，提供 S3 Select MVP。

| 任务 | 开始 | 结束 | 负责人 |
|------|------|------|--------|
| T3.2: 桶管理 + API Key 管理 UI | Day 8 AM | Day 10 PM | C |
| T3.3: Web UI 对象管理增强 | Day 10 AM | Day 12 PM | C |
| T4.1: CSV S3 Select（最小实现） | Day 8 AM | Day 13 PM | B |
| **里程碑 M3** 验收：Web UI 管理功能完整可操作 | Day 12 EOD | | C |
| **里程碑 M4** 验收：S3 Select MVP 可用 | Day 14 EOD | | B |

**风险缓解：**
- T3.3 依赖 T3.2 完成，但 T3.2 和 T3.1 之间 UI 框架已就绪，JS 代码可复用。
- T4.1 可能需要 `expr` 库评审。提前一天准备依赖论证文档。

### 6.4 阶段 4：完善 + 发布（Day 15–17）

**目标：** JSON S3 Select + 集成测试 + 文档 + 发布。

| 任务 | 开始 | 结束 | 负责人 |
|------|------|------|--------|
| T4.2: JSON S3 Select | Day 15 AM | Day 16 PM | B |
| 端到端集成测试（全 5 方向） | Day 15 AM | Day 16 PM | A |
| 文档更新（`docs/` + OpenAPI） | Day 16 AM | Day 17 AM | C |
| 发布说明 + CHANGELOG | Day 17 AM | Day 17 PM | A |
| **里程碑 M5** 全量发布 | Day 17 EOD | | All |

---

## 附录 A：零号任务 — `file_crud.go` 拆分方案

**问题：** `file_crud.go` 当前 420 行，加上 T1.2（10 行）+ T5.2（30 行）+ T5.3（50 行）后超出 500 行限制。

**方案：** 创建 `file_delete.go` 提取以下函数：

| 函数 | 当前行数 | 迁入目标 |
|------|---------|---------|
| `hardDeleteObject` | ~25 行 | `file_delete.go` |
| `softDeleteObject` | ~15 行 | `file_delete.go` |
| `Delete` | ~15 行 | `file_delete.go` |

**工程约束遵守：**
- `file_crud.go` 保留 `Put`、`Get`、`Stat`、`buildPutObject`、`writePutObject`、`ETagVerifier` 等
- `file_delete.go` 新建后约 80 行，远低于 500 行上限
- `file.go`（FileService 定义）当前 248 行，不移动

**执行时机：** T5.1 开始前（Day 1 AM），作为零号任务。

---

## 附录 B：变更汇总

| 方向 | 新增文件 | 修改文件 | 净增行数（预估） |
|------|---------|---------|----------------|
| 方向一 | `internal/reconcile/chunk_reconcile.go` | `retention.go`, `file_crud.go`(→`file_delete.go`), `main.go` | ~190 |
| 方向二 | — | `extra.go`, `file_multipart.go` | ~70 |
| 方向三 | — | `index.html` | ~1000 |
| 方向四 | `internal/api/s3compat/select.go` | `router.go`, `handler.go`, `xml.go` | ~450 |
| 方向五 | — | `file_crud.go`(→`file_delete.go`), `sql_objects.go`, `repository.go`, `migrations/` × 4 | ~180 |
| **合计** | **3 个新文件** | **12+ 个文件修改** | **~1890 行** |

> **所有修改均遵守** `AGENTS.md` §0 工程约束：单文件 ≤ 500 行、单函数 ≤ 50 行、圈复杂度 ≤ 10、禁止 `utils/` 包、每次修改后 `make check`。
