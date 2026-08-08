# 方向：Search over-retrieve（K*2）与 Qdrant/pgvector 硬 100-cap 冲突 —— K≥51 时静默截断结果

> **模块：** `internal/ai`（`search.go` · `qdrant.go` · `pgvector.go` · `vectorindex.go`）+ 关联层 `internal/repository/sql_chunks.go`
> **来源分析：** `docs/auto/analyses/internal-ai-99180452.json`（方向 1） · **日期：** 2026-08-07
> **评分：** 价值 8 / 风险降低 6 / 工作量 2 / 置信度 9
> **本文所有代码引用均已对照当前工作树（HEAD `acfaaf4`）逐条验证；行号与方向快照一致，无漂移。**
> **一处方向断言被证据推翻**（见 E8）："default repoVectorIndex has no such clamp" 不成立 —— clamp 位于其下层 `repository.SearchChunks`，**默认 SQLite/Postgres 基线路径同样中招**。这使 FR-1（searchVector 请求封顶）成为**唯一能同时修复全部后端**的必需项，而非可选替代方案。

---

## 1. 问题陈述

`Search.Query` 校验 `K ≤ 100`（search.go:151-153）后向 `VectorIndex` 请求 **K*2** 个候选（search.go:176）——K=100 时请求 200。而**所有**检索后端对 `limit` 都施加 `limit > 100 → 10` 的硬钳制：

- `repository.SearchChunks`（默认 `repoVectorIndex` 的后端，SQLite/Postgres 通用）：sql_chunks.go:78-80
- `QdrantIndex.SearchVectors`：qdrant.go:147-148
- `PgVectorIndex.SearchVectors`：pgvector.go:122-123

因此 **K ≥ 51 时（K*2 ≥ 102 > 100），任何后端最多只返回 10 个候选**；经 embed-model 过滤（search.go:178-186）后可能更少；后续 trim/rerank 管线（search.go:329 `trimToOverK(merged, req.K*3)`、search.go:363 `applyRerankOrTrim(..., req.K)`）永远无法交付请求的 K 条结果。用户看到的是**静默的 top-K 契约违背**：请求 K=60 得到 ≤10 条，无任何错误。

方向原文称缺陷"仅出现在 opt-in 后端（qdrant|pgvector）"——**该断言错误**：默认暴力扫描后端经 `repoVectorIndex → repo.SearchChunks`（vectorindex.go:29-31）持有**完全相同的钳制**，因此**CI 基线路径（SQLite + local FS，AGENTS.md §1 ★）也受影响**。缺陷是全局的，只是适配器层有独立测试钉住了钳制行为（qdrant_test.go:104），而 Search 交互层无测试，故从未暴露。

### 触发场景（真实工作流）

1. `POST /v1/search {"k": 60, "mode": "vector"}`（或 hybrid）→ embed → `SearchVectors(limit=120)` → 后端钳到 10 → 返回 ≤10 条（若 10 个候选中混有旧 embed-model chunk，过滤后更少）。
2. K ∈ [51, 100] 全覆盖；K ≤ 50 不受影响（K*2 ≤ 100 不触发钳制）。
3. 三个后端同等触发：默认 repoVectorIndex（**含 CI 基线**）、`AI_VECTOR_BACKEND=qdrant`、`AI_VECTOR_BACKEND=pgvector`。

---

## 2. 现状与代码证据（已逐条验证）

| # | 证据 | 验证结果 |
|---|------|---------|
| E1 | `internal/ai/search.go:151-153` — `validate`：`if r.K <= 0 \|\| r.K > 100 { r.K = 10 }` → K 合法上限 100 | ✅ 与方向引用一致（:151） |
| E2 | `internal/ai/search.go:176` — `searchVector`：`s.vindex.SearchVectors(ctx, req.Tenant, req.Bucket, vecs[0], req.K*2)` → 过检索量 = 2K，最大 200 | ✅ 与方向引用一致（:176） |
| E3 | `internal/ai/qdrant.go:147-148` — `if limit <= 0 \|\| limit > 100 { limit = 10 }`（注释 :144-145 "Limit is clamped like PgVectorIndex (<=0 or too large -> default 10)"） | ✅ 与方向引用一致（:147） |
| E4 | `internal/ai/pgvector.go:122-123` — 同一钳制 `limit > 100 → 10` | ✅ 与方向引用一致（:122） |
| E5 | `internal/ai/qdrant_test.go:104-125` — `TestQdrantSearchVectorsClampsLimit`：httptest 断言 wire body `limit`，用例 {0, -5, 99999} 全部期望 10；**只钉适配器自身，不经过 Search 交互** | ✅ 与方向引用一致（:104） |
| E6 | `internal/ai/search.go:329` — `trimToOverK(merged, req.K*3)`；search.go:363 — `applyRerankOrTrim(ctx, req.Query, out, req.K)` → 最终交付被候选数上限卡死 | ✅ 与方向问题描述一致 |
| E7 | `internal/ai/vectorindex.go:29-31` — `repoVectorIndex.SearchVectors` 将 `limit` 原样透传 `r.repo.SearchChunks(...)`；`repository_interface.go:117` 接口签名同 | ✅ 补充验证 |
| E8 | **`internal/repository/sql_chunks.go:76-110`** — `SearchChunks`：:78-80 `if limit <= 0 \|\| limit > 100 { limit = 10 }`；:106-108 按 limit 截断。**与 Qdrant/pgvector 适配器完全相同的钳制**。生产唯一调用方是 `repoVectorIndex`（vectorindex.go:30，已 grep 全仓）→ **方向断言"default repoVectorIndex has no such clamp"不成立**；默认后端（SQLite★/Postgres 均走 sqlStore，sql.go:31）对 K≥51 同样静默截断 | ✅ **新增关键证据（推翻方向断言）** |
| E9 | `cmd/server/ai.go:30-57` — `setupVectorIndexes`：仅 `AI_VECTOR_BACKEND=pgvector`/`qdrant` 且 DSN/URL 非空时才 `WithVectorIndex` 换装；默认 `NewSearch` 装配 `repoVectorIndex`（search.go:38） | ✅ 补充验证（生产装配面） |
| E10 | `internal/ai/search.go:321-326` — `searchAndMerge`：vector 与 hybrid 两种 mode 都走 `searchVector` → 修复一处，两 mode 同愈 | ✅ 补充验证 |
| E11 | `internal/ai/search.go:178-186` — 检索后 `matchesEmbedModel(queryModel, h.Chunk.EmbedModel)` 过滤 → 10 个候选中混入异模型 chunk 时结果进一步缩水 | ✅ 补充验证 |
| E12 | 测试基建：`vectorindex_test.go:9-20` `fakeVectorIndex` **丢弃 limit 参数**（`_ int`）——新测试需 recording 变体；`integration_test.go:25` `newTestEnv`（SQLite TempDir repo，`Query` 尾部 `recordUsage` 需要真实 repo，`NewSearch(nil,…)` 会 nil 解引用）；`NewHashEmbedder`（embedder.go）确定性向量；全仓无现成 `TestSearchVectorLimit` | ✅ 补充验证 |
| E13 | `internal/ai/lexicalindex.go:89-98` — `PgFTSIndex.SearchLexical` 将 limit 直通 SQL（无钳制）；BM25 内存索引无 limit 钳制 → **词法侧无同类缺陷，不改** | ✅ 补充验证（排除项） |
| E14 | `internal/repository/chunks_events_buckets_test.go:251-274` — `TestSearchChunks_limit`（limit=3 → ≤3）钉住 repo 层钳制；本规格**不改** repo 层（见 §5 范围外） | ✅ 补充验证（回归面） |

### 缺陷机理

```
Search.Query(K=60, mode=vector|hybrid)
  ├─ validate: 60 ∈ (0,100] 通过                                    (search.go:151)
  ├─ searchVector: SearchVectors(limit = 60*2 = 120)                (search.go:176)
  │     ├─ 默认: repoVectorIndex → repo.SearchChunks(120)            (vectorindex.go:30)
  │     │        └─ sql_chunks.go:78-80: 120 > 100 → limit = 10  ← 钳制（E8，方向未覆盖）
  │     ├─ qdrant:  QdrantIndex.SearchVectors(120) → 10             (qdrant.go:147)
  │     └─ pgvector: PgVectorIndex.SearchVectors(120) → 10          (pgvector.go:122)
  ├─ matchesEmbedModel 过滤（可能再减）                              (search.go:178-186)
  ├─ trimToOverK(180) 无操作 → applyRerankOrTrim(k=60) 只能给 ≤10   (search.go:329, 363)
  └─ 结果：静默返回 ≤10 条，top-K 契约违背，无错误、无日志
```

---

## 3. 需求规格

### FR-1（必需 · 请求封顶）`Search.searchVector` 将候选请求封顶在后端上限 100

`internal/ai/search.go:176` 的 `req.K*2` 改为 `min(req.K*2, 100)`（实现自由，如 `limit := req.K * 2; if limit > 100 { limit = 100 }`）。理由与约束：

- **这是唯一同时修复全部后端（含默认 SQLite/Postgres 基线路径）的改动**（E8/E9）：封顶后任意后端收到的 limit ≤ 100，其 `> 100 → 10` 分支永不触发。
- K ≤ 50 时 K*2 ≤ 100，行为**逐字节不变**。
- K ∈ [51, 100] 时请求 100：后端返回至多 100 个候选 ≥ K，embed-model 过滤后仍可能 ≥ K，trim/rerank 管线可交付 K 条。
- 覆盖 vector 与 hybrid 两 mode（E10）；bm25 纯词法 mode 不经此路径（E13，不受影响）。

### FR-2（必需 · 适配器 min-clamp）`QdrantIndex` / `PgVectorIndex` 上界钳制由"默认 10"改为"封顶 100"

`internal/ai/qdrant.go:147-148` 与 `internal/ai/pgvector.go:122-123` 从 `limit <= 0 || limit > 100 → 10` 改为：

```
if limit <= 0 { limit = 10 }
if limit > 100 { limit = 100 }
```

- 下界语义（≤0 → 10）保持不变（既有测试钉住 0/-5 → 10）。
- 上界语义：limit=200 → **100**（而非 10）；limit=101..∞ → 100。语义为 `min(limit, 100)`（limit>0 时）。
- **建议机制**：提取包级共享函数（如 `clampSearchLimit(limit int) int`）供两个适配器共用，使 pgvector 半边可在无真实 Postgres 的单元门禁内确定性验证（方向验收要求覆盖 PgVectorIndex）；两适配器注释同步更新（qdrant.go:144-145 的 "clamped like PgVectorIndex" 描述保持成立）。
- 适配器为公开类型，直接调用方（非 Search）传大 limit 时行为变化：200 → 100 而非 10。影响有界（单请求候选 ≤ 100，无资源放大），属修复语义的一部分。

### FR-3（测试）

| # | 需求 | 可测试断言 |
|---|------|-----------|
| FR-3.1 | 新增 `TestSearchVectorLimit`（`internal/ai/search_validation_test.go` 或独立文件）：recording stub VectorIndex（记录 `limit` 实参并返回 ≥ K 个 embed-model 匹配的 canned hits）；装配 `NewSearch(newTestEnv(t).repo, NewHashEmbedder(N), nil).WithVectorIndex(stub)`，`Query{K: 60, Mode: "vector", Query: "x"}` | ① 记录的请求 limit ∈ [60, 100]（修复前为 120）；② 返回 hits 数 == 60（修复前 ≤ 10 的截断可观测化） |
| FR-3.2 | 更新 `TestQdrantSearchVectorsClampsLimit`（qdrant_test.go:104-125）：保留 {0, -5} → 10；**99999 期望翻转为 100**；新增 100 → 100、101 → 100、**200 → 100**（wire body `limit` 断言） | 用例表驱动，逐条断言 wire body limit |
| FR-3.3 | 新增共享钳制函数单元测试（如 `TestClampSearchLimit`）：覆盖 pgvector 半边（无 DB 依赖） | 0/-5 → 10；100 → 100；101/200/99999 → 100 |
| FR-3.4 | 回归：既有 `TestSearch_UsesInjectedVectorIndex`、`TestSearch_DefaultVectorIndexIsBruteForce`（vectorindex_test.go）、repo 层 `TestSearchChunks_limit`（chunks_events_buckets_test.go:251）、词法/rerank 测试全绿 | `go test ./internal/ai ./internal/repository -count=1` |

---

## 4. 验收标准映射（方向验收逐条保留，已可测试化）

| 方向验收原文 | 映射 |
|---|---|
| "New unit test: Search.Query with K=60 in vector mode against a stub VectorIndex that records the requested limit asserts the limit is <=100 and >=K (currently 120 is requested and clamped to 10)" | **AC-1** = FR-3.1①：`TestSearchVectorLimit` 断言 stub 记录的请求 limit 满足 `60 ≤ limit ≤ 100`（修复前 120） |
| "go test ./internal/ai -run TestSearchVectorLimit -count=1 passes" | **AC-2** = 命令原样执行通过（新增测试名唯一，全仓无冲突，E12） |
| "Updated adapter test: QdrantIndex/PgVectorIndex SearchVectors with limit=200 returns min(limit,100)=100 (not 10); go test ./internal/ai -run TestQdrantSearchVectorsClampsLimit passes" | **AC-3** = FR-3.2（Qdrant wire-level，含 200 → 100）+ FR-3.3（共享钳制函数覆盖 pgvector 半边，无需 live Postgres）；两条命令均通过 |

**门禁命令：**

```bash
go test ./internal/ai -run TestSearchVectorLimit -count=1        # AC-2
go test ./internal/ai -run TestQdrantSearchVectorsClampsLimit -count=1   # AC-3（Qdrant 半边）
go test ./internal/ai -run TestClampSearchLimit -count=1         # AC-3（pgvector 半边，FR-3.3）
go test ./internal/ai -count=1                                   # 全包回归（FR-3.4）
make check                                                       # 工程硬门禁（gofmt/build/vet/test，单文件 ≤500 行）
```

---

## 5. 范围外（明确排除，勿扩展）

- **`repository.SearchChunks` 的钳制（sql_chunks.go:78-80）不改**：FR-1 封顶后 Search 永不向其发送 >100；直接外部调用方传 >100 仍得 10（既有行为，非本方向）。`TestSearchChunks_limit` 不触碰。
- **词法检索 `searchLexical` 的 K*2**（search.go:198/:209）：PgFTS 无钳制、BM25 无上限（E13），无同类缺陷。
- **K 校验的静默重置**（search.go:151-153 `K>100 → 10`）：既有行为，非本方向。
- **access-control 过滤后 under-fill**（分析文件方向 3，search.go:329/358/363 的 authz 顺序）：独立缺陷，另行处理。
- **无 API/协议/schema 变更**：纯内部数值钳制与测试，无迁移、无配置项、无文档面改动。
- **不引入新依赖**（AGENTS.md I6）：全部 stdlib（testing/httptest）。

---

## 6. 回归面与风险

| 面 | 影响 |
|---|---|
| K ≤ 50 全部 mode/后端 | 请求量 K*2 ≤ 100，适配器上界分支不触发 → **零行为变化** |
| K ∈ [51,100] 默认后端 | 修复前 ≤10 条 → 修复后至多 K 条（**行为改善，方向核心**） |
| K ∈ [51,100] qdrant/pgvector | 同上 |
| 适配器直接调用方（limit>100） | 10 → 100（语义从"默认值"变"封顶值"，有界） |
| `TestQdrantSearchVectorsClampsLimit` 既有用例 | 99999 期望 10 → 100，**必须随 FR-2 同步更新**，否则门禁红（FR-3.2） |
| 文件规模 | 每文件改动 ≤ 10 行，远低于 500 行硬门禁 |
