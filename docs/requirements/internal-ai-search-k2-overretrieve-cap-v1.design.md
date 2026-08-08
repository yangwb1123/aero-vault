# Design：Search over-retrieve（K*2）与后端 100-cap 冲突修复

> **模块：** `internal/ai`（`search.go` · `qdrant.go` · `pgvector.go` · `searchlimit.go`(新)）+ 测试面
> **依据：** `docs/requirements/internal-ai-search-k2-overretrieve-cap-v1.md`（规格，HEAD `acfaaf4` 逐条核验）
> **日期：** 2026-08-07 · **本设计所有引用均已对照工作树重新验证（见 §9 验证日志）。**

---

## 1. 问题与修复策略（30 秒版）

`Search.Query` 校验 `K ≤ 100` 后向 `VectorIndex` 请求 **K*2** 个候选（search.go:176），K ∈ [51,100] 时请求量 ∈ [102,200] 超过**所有**后端共享的 `limit>100` 钳制点；三个后端（默认 `repoVectorIndex → repository.SearchChunks`、Qdrant、pgvector）各自把超限请求静默降为 **10** 个候选。embed-model 过滤（search.go:178-186）后可能更少，trim/rerank 管线（search.go:329 `trimToOverK(K*3)`、search.go:363 `applyRerankOrTrim(K)`）永远无法交付 K 条 → **top-K 契约静默违背**。

**修复 = 两处数值钳制 + 一处共享助手 + 四个测试：**

| # | 改动 | 位置 |
|---|------|------|
| D1 | 请求封顶 `min(K*2, 100)`（FR-1） | `internal/ai/search.go:176` |
| D2 | 共享 `clampSearchLimit`：`≤0 → 10`，`>100 → 100`（FR-2） | 新文件 `internal/ai/searchlimit.go` |
| D3 | 两适配器改用 D2 助手 + 注释同步（FR-2） | `qdrant.go:144-149` · `pgvector.go:121-124` |
| D4 | 测试：`TestSearchVectorLimit`（recording stub）、`TestSearchVectorK51ReturnsKExactly`（默认路径端到端）、`TestQdrantSearchVectorsClampsLimit` 更新、`TestClampSearchLimit`（FR-3） | `search_validation_test.go` · `integration_test.go` · `qdrant_test.go` · `searchlimit_test.go`(新) |

无 API/协议/schema/配置/依赖变更；无迁移步骤；K ≤ 50 逐字节零行为变化。

---

## 2. 证据核验寄存器（对照 HEAD `acfaaf4`，全部重验）

| # | 声明 | 重验结果 |
|---|------|---------|
| E1 | search.go:151-153 `if r.K <= 0 \|\| r.K > 100 { r.K = 10 }` → K 合法上限 100 | ✅ 逐行一致 |
| E2 | search.go:176 `s.vindex.SearchVectors(ctx, req.Tenant, req.Bucket, vecs[0], req.K*2)` | ✅ 逐行一致 |
| E3 | qdrant.go:147-148 `if limit <= 0 \|\| limit > 100 { limit = 10 }`；注释 :144-145 "clamped like PgVectorIndex (<=0 or too large -> default 10)" | ✅ 逐行一致 |
| E4 | pgvector.go:122-123 同一钳制 | ✅ 逐行一致 |
| E5 | qdrant_test.go:104-125 `TestQdrantSearchVectorsClampsLimit`：用例 {0, -5, 99999} 全部期望 10，wire body `limit` 断言 | ✅ 逐行一致（修复后需同步更新，见 D4/FM-2） |
| E6 | search.go:329 `trimToOverK(merged, req.K*3)`；search.go:363 `applyRerankOrTrim(..., req.K)` | ✅ 逐行一致 |
| E7 | vectorindex.go:29-31 `repoVectorIndex.SearchVectors` 原样透传 `repo.SearchChunks` | ✅ 逐行一致 |
| E8 | **sql_chunks.go:78-80** `if limit <= 0 \|\| limit > 100 { limit = 10 }` + :106-108 按 limit 截断；`SearchChunks` 生产唯一调用方 = `repoVectorIndex`（vectorindex.go:30，`rg -n "SearchChunks" --type go` 仅 3 处非测试命中：定义/接口/透传）→ 默认 SQLite/Postgres 基线路径同样中招 | ✅ **规格关键发现成立**（推翻方向原断言） |
| E9 | cmd/server/ai.go:30-57 `setupVectorIndexes`：仅 opt-in 后端换装；默认 `NewSearch` 装配 `repoVectorIndex`（search.go:38-39） | ✅ 一致 |
| E10 | searchAndMerge（search.go:315-320）：vector/hybrid 两 mode 都走 `searchVector` | ✅ 一致 |
| E11 | search.go:178-186 `matchesEmbedModel(queryModel, h.Chunk.EmbedModel)` 过滤 | ✅ 一致 |
| E12 | `fakeVectorIndex` 丢弃 limit（vectorindex_test.go:16 `_ int`）；`newTestEnv`（integration_test.go:25-40）；`NewHashEmbedder`（embedder.go:37-42）；全仓无 `TestSearchVectorLimit` | ✅ 一致；`search_validation_test.go` 已存在（4 个测试），是 D4 的合理落点 |
| E13 | 词法侧无同类钳制：`PgFTSIndex.SearchLexical`（lexicalindex.go:89-98）limit 直通 SQL `LIMIT $4`；内存 BM25 无 limit 钳制 | ✅ 一致（排除项） |
| E14 | `TestSearchChunks_limit`（chunks_events_buckets_test.go:251-274）钉住 repo 层钳制 → 本设计不改 repo 层 | ✅ 一致 |

**生产调用面（补充核验）：** `SearchVectors` 生产调用点唯一 = search.go:176（经接口）；REST/MCP/agent/chat 全部经 `Search.Query` → `validate` → `searchVector`（agent.go:198 `K: k`，agent 侧自钳 `k<=0||k>100 → 5`；REST `internal/api/rest/search.go` 直传 `k`）。修复 D1 一处即覆盖全部入口。

---

## 3. 先前尝试的裁决处置（gate 会复查，逐条 disposition）

> 已读 `docs/auto/runs/fix-search-over-retrieve-k-2-colliding-with-qdra-0c0a987c/DECISIONS.md`（本管线）及全部同目录 sibling 的 DECISIONS.md 与 design-gate 工件（`rg -il "qdrant|pgvector|SearchChunks|clamp|search" docs/auto/runs/*/{DECISIONS.md,artifacts/*gate*/}`）。

| 来源 | 裁决 | 未决发现 | 本设计处置（附证据） |
|------|------|---------|---------------------|
| 本 run `DECISIONS.md`（requirements，PASS） | — | 唯一未决项：**E8** 默认后端同样钳制 → FR-1 升格为必需 | **已并入设计**：D1 为必需项（非可选替代）；新增 D4 端到端测试 `TestSearchVectorK51ReturnsKExactly` 在**默认 SQLite 路径**上使截断可观测（修复前 K=60 → 10 条，修复后 60 条） |
| `fail-closed-liveness-gate-on-the-rag-read-path` design-gate（PASS） | PASS | B1（SQL 计划劫持）/B2（marker JSON 谓词）/B7（测试覆盖）/B8+B9（并发/运维）——全部属 liveness 门自身范围；残余 nit 与 limit 语义无关 | **不重叠、不冲突**：该 run 未合入（工作树 search.go 无 `filterLiveHits`）；其改动作用于 Query 检索**后**过滤，本设计作用于 `searchVector` 检索**前**候选数——不同函数、无行冲突；双方合入亦正交。无 action |
| `make-rag-chunk-invalidation-durable-async` design-gate（裁决 FAIL） | FAIL | Integration-verifier 0/5 关闭（`InsertDeleteMarkerWithEvent` 预置事实、per-version `deleted@1.1` 重复、`rebind` nit、A1/A2 relay live-ness 复查、§5.1 矩阵）+ ops O1-O8 + perf R1——全部属 `event_outbox`/`delete_marker`/chunk-cleanup 面 | **文件零重叠**：该 run 文件集 = `event_outbox.go`/`delete_marker.go`/`file_delete.go`/`sql_objects_versions.go` 等；本设计文件集 = `search.go`/`qdrant.go`/`pgvector.go`/`searchlimit.go`。其 E6（"pgvector 是 chunks 表上的 `VectorIndex` 而非 `ChunkSink`"）与本设计 E3/E4 一致。该 run 的 FAIL 由其实现阶段负责重跑 gate，不构成本设计阻塞。无 action |
| `build-a-dual-backend-outbox-delivery` design-gate（PASS） | PASS | SQL 占位符 A13-A19 等，均属 `event_outbox` | 无重叠。无 action |
| 本 run 先前 implement 尝试（memory：2026-08-07T00:19:51 validation exit=1） | FAIL | 无 artifacts 留存（`archive/` 为空目录，无 design/implement 工件） | 无残留文件、无未决设计点；其失败不提供任何约束。无 action |
| 其余 sibling（audit/authorizationprovider/outbox/readyz 等 20+ run） | — | DECISIONS.md grep 均未命中 `search.go|qdrant|pgvector|clamp|K*2` | 无关。无 action |

**结论：无任何未决发现指向本设计范围；唯一相关未决项（E8）已作为核心约束并入 D1+D4。**

---

## 4. 具体设计（代码级）

### D1（FR-1 必需）— `internal/ai/search.go:176` 请求封顶

```go
	// Cap the over-retrieval factor at the backends' shared upper bound: for
	// K in (50,100], K*2 exceeds 100 and every retrieval backend (repository
	// scan, Qdrant, pgvector) would silently clamp the request and truncate
	// results before embed-model filtering and rerank/trim.
	hits, err := s.vindex.SearchVectors(ctx, req.Tenant, req.Bucket, vecs[0], min(req.K*2, 100))
```

- 使用内置 `min`（Go ≥ 1.21；go.mod `go 1.26.1` ✓，`internal/ai` 无 `min` 遮蔽 ✓）。等价双行写法（`limit := req.K * 2; if limit > 100 { limit = 100 }`）亦可，语义一致。
- 封顶值 **100** = 后端共享钳制点（E3/E4/E8），保证任意后端收到的 limit ≤ 100，`>100` 分支永不触发。
- 覆盖 vector + hybrid 两 mode（E10）；K ≤ 50 时 `K*2 ≤ 100`，`min` 恒为恒等 → **零行为变化**。

### D2（FR-2 必需）— 新文件 `internal/ai/searchlimit.go`（共享钳制助手）

```go
package ai

// clampSearchLimit bounds a backend candidate request to the contract shared
// by every retrieval backend (repository scan, Qdrant, pgvector):
//   - limit <= 0  -> default 10 (unchanged legacy semantics);
//   - limit > 100 -> capped at 100, the maximum a Search request can produce
//     (K validated <= 100, K*2 capped in searchVector).
//
// The >100 case is a cap (min(limit,100)), not a fallback to the default, so
// K in (50,100] never collapses to 10 candidates.
func clampSearchLimit(limit int) int {
	if limit <= 0 {
		return 10
	}
	if limit > 100 {
		return 100
	}
	return limit
}
```

### D3（FR-2）— 两适配器改用共享助手

`internal/ai/qdrant.go:143-149`：

```go
// SearchVectors runs a Qdrant nearest-neighbour search scoped to the tenant (and
// optional bucket) and maps the points back to repository SearchHits. Limit is
// clamped like PgVectorIndex (<=0 -> default 10; >100 -> capped at 100).
func (q *QdrantIndex) SearchVectors(ctx context.Context, tenant, bucket string, query []float32, limit int) ([]repository.SearchHit, error) {
	limit = clampSearchLimit(limit)
```

`internal/ai/pgvector.go:121-124`：

```go
func (p *PgVectorIndex) SearchVectors(ctx context.Context, tenant, bucket string, query []float32, limit int) ([]repository.SearchHit, error) {
	limit = clampSearchLimit(limit)
```

（各删 2 行 `if limit <= 0 || limit > 100 { limit = 10 }`，替换为 1 行调用。）

### D4（FR-3）— 测试

**D4.1 `TestSearchVectorLimit`**（追加到既有 `internal/ai/search_validation_test.go`；recording stub）：

```go
// recordingVectorIndex records every requested limit so tests can assert the
// over-retrieval factor Search asks of the backend.
type recordingVectorIndex struct {
	limits []int
	hits   []repository.SearchHit
}

func (r *recordingVectorIndex) SearchVectors(_ context.Context, _, _ string, _ []float32, limit int) ([]repository.SearchHit, error) {
	r.limits = append(r.limits, limit)
	return r.hits, nil
}
```

测试体（vector 主用例 + hybrid 副用例）：

```go
func TestSearchVectorLimit(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	emb := NewHashEmbedder(64)
	stub := &recordingVectorIndex{}
	for i := 0; i < 60; i++ {
		stub.hits = append(stub.hits, repository.SearchHit{
			Score: 0.99 - float32(i)*0.001,
			Chunk: repository.Chunk{ID: int64(i + 1), ObjectID: 1, Bucket: testBucket,
				ObjectKey: "k.txt", Seq: 0, Content: "c", EmbedModel: emb.Name()},
		})
	}
	s := NewSearch(env.repo, emb, nil).WithVectorIndex(stub)

	for _, mode := range []string{"vector", "hybrid"} {
		s2 := s
		if mode == "hybrid" {
			s2 = s.WithBM25(NewBM25()) // empty index: lexical half contributes nothing
		}
		hits, err := s2.Query(ctx, Request{Tenant: testTenant, Bucket: testBucket, Query: "x", K: 60, Mode: mode})
		if err != nil {
			t.Fatalf("%s query: %v", mode, err)
		}
		if len(stub.limits) == 0 {
			t.Fatalf("%s: vector index never consulted", mode)
		}
		got := stub.limits[len(stub.limits)-1]
		if got < 60 || got > 100 {
			t.Errorf("%s: requested limit=%d, want in [60,100] (pre-fix value was 120)", mode, got)
		}
		if len(hits) != 60 {
			t.Errorf("%s: got %d hits, want exactly 60 (pre-fix truncation made this <=10 on every backend)", mode, len(hits))
		}
	}
}
```

- `NewBM25()` 空索引 `Search` 安全返回 nil（bm25.go:231-237 实测）。
- 修复前：`limits` 记录 120 → 断言红；修复后：100 → 绿。
- 副用例证明 E10（hybrid 同愈）。

**D4.2 `TestSearchVectorK51ReturnsKExactly`**（追加到 `internal/ai/integration_test.go`；默认后端端到端，钉 E8）：

```go
// K in (50,100] requests K*2 candidates; without the cap in searchVector the
// default repository scan clamps the request to 10 and silently truncates the
// result set below the requested K. This test pins the fix on the default
// (SQLite) path — the CI baseline.
func TestSearchVectorK51ReturnsKExactly(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	emb := NewHashEmbedder(64)
	o := env.putObject(t, "k60.txt", "text/plain", "k60")
	contents := make([]string, 60)
	for i := range contents {
		contents[i] = fmt.Sprintf("seed content line %d", i)
	}
	env.seedChunks(t, o, emb, contents...)

	s := NewSearch(env.repo, emb, nil)
	for _, k := range []int{50, 60} { // 50 = exact boundary (K*2 == 100); 60 = over the cap
		hits, err := s.Query(ctx, Request{Tenant: testTenant, Bucket: testBucket, Query: "seed", K: k, Mode: "vector"})
		if err != nil {
			t.Fatalf("K=%d query: %v", k, err)
		}
		if len(hits) != k {
			t.Fatalf("K=%d: got %d hits, want exactly %d (pre-fix: K>=51 returned <=10)", k, len(hits), k)
		}
	}
}
```

- 修复前：K=60 → `SearchChunks(120)` 钳到 10 → 10 hits → 红；K=50 → 100 不触发 → 50 hits → 绿（零变化边界钉住）。
- 修复后：K=60 → `SearchChunks(100)` → 60 hits → 绿。

**D4.3 `TestQdrantSearchVectorsClampsLimit` 更新**（qdrant_test.go:104-125 重写为表驱动）：

```go
func TestQdrantSearchVectorsClampsLimit(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"result":[]}`))
	}))
	defer srv.Close()

	qi := NewQdrantIndex(QdrantOptions{BaseURL: srv.URL})
	for _, tc := range []struct{ in, want int }{
		{0, 10}, {-5, 10}, {100, 100}, {101, 100}, {200, 100}, {99999, 100},
	} {
		gotBody = nil
		if _, err := qi.SearchVectors(context.Background(), "acme", "", []float32{1}, tc.in); err != nil {
			t.Fatalf("search lim=%d: %v", tc.in, err)
		}
		if v, _ := gotBody["limit"].(float64); int(v) != tc.want {
			t.Errorf("limit=%d should clamp to %d, got %v", tc.in, tc.want, gotBody["limit"])
		}
	}
}
```

**D4.4 `TestClampSearchLimit`**（新文件 `internal/ai/searchlimit_test.go`；pgvector 半边无需 live Postgres）：

```go
package ai

import "testing"

func TestClampSearchLimit(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{0, 10}, {-5, 10}, {1, 1}, {50, 50}, {100, 100}, {101, 100}, {200, 100}, {99999, 100},
	} {
		if got := clampSearchLimit(tc.in); got != tc.want {
			t.Errorf("clampSearchLimit(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
```

---

## 5. API 变更与兼容性约束

- **公开 API 零变更：** `VectorIndex` 接口、`SearchVectors` 签名、`Search.Query`/`Request`、REST/S3/MCP 线格式、配置键、schema 全部不动。`clampSearchLimit` 为包私有函数。
- **K ≤ 50（全部 mode/后端）：** 请求量 K*2 ≤ 100，D1 的 `min` 与 D3 的上界分支均不触发 → 行为逐字节不变；既有测试全绿（§9 已跑基线）。
- **K ∈ [51,100]：** 交付条数从 ≤10 提升到 ≤K（行为改善，方向核心）。候选量上限 100 与 `trimToOverK(K*3)`/rerank 输入上限（3K ≤ 300）兼容：vector 侧至多 100 条进 RRF/trim。
- **适配器直接调用方（limit>100）：** 返回值从 10 → 100。生产影响**零**：`SearchVectors` 生产调用点唯一（search.go:176，§2 补充核验），D1 后 Search 永不发送 >100。受影响的既有测试仅 `TestQdrantSearchVectorsClampsLimit` 自身（D4.3 同步更新，否则门禁红——FM-2）。
- **repo 层 `SearchChunks`（>100 → 10）不改：** 直接外部调用方保持既有语义；D1 保证 Search 永不触发该分支（E14 测试不触碰）。
- **依赖（I6）：** 零新增 import（内置 `min` 是语言内建，非依赖）。
- **文件规模门禁：** search.go/qdrant.go/pgvector.go 各 ±3 行；两个新文件 ≤ 30 行；全部 ≪ 500 行。
- **计量/费用：** `recordUsage` 只计交付 hit（≤K），候选数增加不放大 AI 用量计费（AGENTS.md §2.3）；embedding 成本不变（query 只嵌一次）。

---

## 6. 失败模式（含缓解）

| # | 场景 | 行为 | 缓解 |
|---|------|------|------|
| FM-1 | 只合 D1 或只合 D3 | D1 单独已修复全部后端的 Search 路径（limit ≤ 100 永不过界）；D3 单独只修 opt-in 后端，默认后端仍截断 | D1+D3 同一提交；D4.2（默认路径端到端）+ D4.1（请求量）双向钉住，任何一半缺失即红 |
| FM-2 | 忘记同步更新 `TestQdrantSearchVectorsClampsLimit`（99999 期望 10） | `go test ./internal/ai` 失败，门禁拦截 | D4.3 与 D3 同变更落地（表驱动 {0,-5→10; 100,101,200,99999→100}） |
| FM-3 | embed-model 过滤后候选 < K（corpus 不足或异模型 chunk 混入） | 返回 < K 条——**正确行为**（E11），非截断回归 | 既存语义，规格 §5 明示范围外；D4.2 种子全同模型避免假阳 |
| FM-4 | reranker 失败 | warn + 原始序 trim（applyRerankOrTrim 既有分支） | 既有行为，无新风险 |
| FM-5 | 适配器直接调用方传 limit=1000 | 得 100（原 10）——有界，无资源放大 | 兼容性说明（§5）；生产无此类调用方 |
| FM-6 | hybrid：词法侧 K*2=200 不受钳 | 词法无钳制（E13），RRF 融合/trimToOverK(3K) 不变 | D4.1 hybrid 副用例覆盖 |
| FM-7 | K=100，authz 过滤后 < 100 | under-fill 属分析方向 3 独立缺陷 | 规格 §5 明示范围外 |
| FM-8 | Qdrant 服务端自身限额返回更少 | 后端定义行为，无错误 | 既有行为 |

---

## 7. 迁移步骤

**无。** 无 schema（I2 双迁移文件不涉及）、无配置项（docs/configuration.md 不动）、无线格式、无依赖。上线 = 单提交代码变更；回滚 = `git revert` 该提交，行为回到现状（无数据写差异）。

---

## 8. 验收标准映射（方向 3 条验收全部保留并测试化）

| 方向验收原文 | 映射 | 可执行验证 |
|---|---|---|
| AC-1 "New unit test: Search.Query with K=60 in vector mode against a stub VectorIndex that records the requested limit asserts the limit is <=100 and >=K (currently 120 is requested and clamped to 10)" | D4.1 `TestSearchVectorLimit`：断言 `60 ≤ 请求limit ≤ 100`（修复前 120）+ `len(hits)==60` | `go test ./internal/ai -run TestSearchVectorLimit -count=1` |
| AC-2 "go test ./internal/ai -run TestSearchVectorLimit -count=1 passes" | D4.1 命名唯一（全仓 grep 无冲突，E12） | 同上命令 |
| AC-3 "Updated adapter test: QdrantIndex/PgVectorIndex SearchVectors with limit=200 returns min(limit,100)=100 (not 10); go test ./internal/ai -run TestQdrantSearchVectorsClampsLimit passes" | D4.3（Qdrant wire 级，200→100）+ D4.4（共享助手覆盖 pgvector 半边，无需 live Postgres） | `go test ./internal/ai -run TestQdrantSearchVectorsClampsLimit -count=1` 与 `go test ./internal/ai -run TestClampSearchLimit -count=1` |

**FR-3.4 回归面：** `TestSearch_UsesInjectedVectorIndex` / `TestSearch_DefaultVectorIndexIsBruteForce`（vectorindex_test.go）· repo `TestSearchChunks_limit`（E14）· 词法/rerank/chat/agent 测试 · CLI/e2e 不受影响（无线格式变化）。

**门禁命令（implement 阶段照此执行）：**

```bash
go test ./internal/ai -run TestSearchVectorLimit -count=1          # AC-1/AC-2
go test ./internal/ai -run TestSearchVectorK51ReturnsKExactly -count=1  # E8 端到端
go test ./internal/ai -run TestQdrantSearchVectorsClampsLimit -count=1  # AC-3 Qdrant 半边
go test ./internal/ai -run TestClampSearchLimit -count=1           # AC-3 pgvector 半边
go test ./internal/ai ./internal/repository -count=1               # FR-3.4 回归
make check                                                         # gofmt/build/vet/test + 文件 ≤500 行
```

---

## 9. 验证日志（本设计执行）

- `git log -1` = `acfaaf4`；`internal/ai` 工作树干净（`git status --short internal/ai` 空）。
- 基线 `go test ./internal/ai ./internal/repository -count=1` → `ok`（14.2s / 30.7s）。
- 所有 E1-E14 引用的行号逐一 read/sed 复核（§2 表）。
- `SearchChunks`/`SearchVectors` 全仓调用点 grep；`min` 遮蔽检查；`NewBM25().Search` 空索引安全性检查（bm25.go:231-237）。
- sibling runs：`DECISIONS.md` × N 全量 grep + 两个 AI 相关 run 的 design-gate 工件通读（§3 表）。

## 10. 范围外（与规格 §5 一致，勿扩展）

- `repository.SearchChunks` 钳制（sql_chunks.go:78-80）不改；`TestSearchChunks_limit` 不触碰。
- 词法 `K*2`（search.go:198/:209）——无同类缺陷（E13）。
- K 校验静默重置（search.go:151-153）——既有行为。
- authz under-fill（分析方向 3）——独立缺陷。
- 无新依赖、无文档面改动（本 design.md 除外）。
