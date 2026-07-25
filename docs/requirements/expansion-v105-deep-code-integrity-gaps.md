# 高价值扩展方向：BM25 索引持久性缺口、Multipart 版本键分歧、WebDAV 锁挥发性、Tag 搜索分页正确性、优雅关闭 in-flight 排空

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — `cmd/server/main.go` + `internal/` 全部 30+ 子包（237 个 Go 源文件，~47K 行），3 套 SDK（Go/Python/JS），MCP 双模式（HTTP+stdio），Web UI，50 对迁移文件，`deploy/` 全套配置（Helm/Grafana/Prometheus/OTel），`HARNESS.md`，`AGENTS.md`  
> **去重验证：** 对 `docs/requirements/` 下全部 104 份既有分析文档（`expansion-directions.md` ~ `expansion-v104-architect-systemic-gaps.md`）逐方向进行代码锚点级关键词正则 + 语义交叉验证  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。选取**代码中存在明确锚点、有实质性产品/架构影响、且在 104 轮既有分析中未被独立深度覆盖**的方向。每个方向包含：产品价值 → 现状与代码证据 → 架构权衡 → 边界情况。

---

## 去重验证

对 `docs/requirements/` 下全部 104 份既有分析文档进行逐方向交叉验证：

| 方向 | 既往覆盖情况 | 结论 |
|------|-------------|------|
| **方向一：BM25 索引持久性缺口 — 混合搜索每次重启后服务降级** | v10 第 4 行在一次 graceful shutdown 表中提及"内存索引未持久化；（BM25）启动时重建（代价高）"——**一行概念，无代码锚点、无影响分析、无架构方案**。其余 103 份文档零覆盖。**正则搜索 `BM25.*persist\|BM25.*checkpoint\|BM25.*save\|BM25.*restore\|BM25.*dump\|BM25.*warmup\|bm25.*disk`** → 仅 v10 表格行命中 | ✅ **全新方向**（v10 仅一行提及，无任何深度分析） |
| **方向二：Multipart Upload 版本 ID 与存储键分歧** | v32 覆盖版本化桶的成本爆炸但聚焦存储膨胀；v52 在备份方案中涉及 storage_key+version_id 但为单行举例；v4/v19 涉及版本化桶的一般操作。**零分析 `InitMultipart` 生成的 storage key version 后缀与 `InsertObjectVersion` 的 version_id 之间的潜在分歧**。**正则搜索 `multipart.*version.*mismatch\|multipart.*storage.key.*diverg\|CompleteMultipart.*version_id\|storage_key.*version.*gap\|version.*suffix.*mismatch`** → 0 命中 | ✅ **全新方向** |
| **方向三：WebDAV 锁系统的内存挥发性与跨协议锁不协调** | v8 方向四表格提及"WebDAV 锁"但聚焦**长期持有锁的清理**而非崩溃重启丢失；v28 在跨协议一致性表中一行提及"Lock 与 Object Lock 冲突"但**零代码锚点分析**。**零分析 `NewMemLS()` 重启丢失、无跨协议锁协调、无管理面锁检视**。**正则搜索 `MemLS\|NewMemLS\|webdav.*lock.*loss\|webdav.*lock.*restart\|lock.*inspect\|lock.*admin.*API`** → 0 命中 | ✅ **全新方向** |
| **方向四：ListObjectsByTag 客户端过滤分页正确性缺陷** | v104 方向三在"DB 驱动特性不对称"表格中一行提及"List/Count 性能退化（SQLite 无 JSON 索引）"——**聚焦性能而非正确性**。其余 103 份文档零分析 `ListObjectsByTag` 的实现。**正则搜索 `ListObject.*Tag.*page\|tag.*filter.*page.*miss\|tag.*search.*pagina\|ListByTag.*page.*limit\|client.side.*filter.*page`** → 0 命中 | ✅ **全新方向** |
| **方向五：优雅关闭 in-flight 请求排空与后台工作者终结顺序** | v10 方向二全覆盖优雅关闭方案设计（24 行描述 + 8 种场景表 + 状态机蓝图），但**不包含代码级现状：当前 `runServer` 仅调用 `srv.Shutdown` + 15s 超时，不追踪 in-flight 请求完成、不通知 SSE 客户端、不持久化 BM25 checkpoint、后台工作者无顺序终结**。**正则搜索 `srv.Shutdown\|in-flight.*shutdown\|request.*draining\|graceful.*shutdown.*code\|Shutdown.*request\|Shutdown.*context`** → v10 有方案设计但无 `cmd/server/main.go:runServer` 的代码级现状分析 | ✅ **方案设计存在但无代码级现状分析**（本方向提供 `runServer` 的精确代码锚点与缺口清单） |

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 |
|---|------|------|--------|---------|---------|
| **1** | **BM25 索引持久性缺口：混合搜索在重启后服务不可用窗口** | 可用性/性能 | **P1** | BM25 索引在内存中重建于启动时，无磁盘持久化。服务器重启后，BM25/hybrid 搜索在索引完全重建前（大语料库数分钟）返回零结果或降级结果。无增量 checkpoint、无 warm-up、无优雅降级到纯向量搜索 | `internal/ai/bm25.go`（`type BM25 struct` — 全部字段 `map[string]*index \| []*Document` 为内存结构）；`internal/ai/bm25.go:BuildFromRepo`（唯一初始化路径——全量遍历 `ListObjects` + chunk 构建）；`cmd/server/main.go:577-586`（`setupBM25Search` 中 `go func() { _ = bm.BuildFromRepo(ctx, repo, t) }()` — goroutine 内全量构建，无 checkpoint 恢复）；`internal/ai/search.go:searchLexical`（`s.bm25.Search(...)` — BM25 未就绪时返回空结果）；`internal/ai/search.go:validate`（`bm25Available()` 检查 `s.bm25 != nil` 但 `s.bm25` 对象在 `BuildFromRepo` 完成前存在——返回空结果而非错误）；`internal/ai/bm25_test.go`（测试使用小数据不涉及持久化） |
| **2** | **Multipart Upload 版本 ID 与存储键后缀分歧** | 数据完整性 | **P1** | 版本化桶中 `InitMultipart` 在 storage_key 中嵌入版本后缀 `@v<id1>`，但 `CompleteMultipart` 的 `InsertObjectVersion` 生成不同的 `version_id = <id2>`。storage_key 中的版本 ID 与 DB 的 `version_id` 列不一致。版本特定的 `GetObjectVersion` 找到 DB 行（version_id = `<id2>`）但试图读取 storage_key = `...@v<id1>`——错位的 blob | `internal/service/file_multipart.go:31-35`（`InitMultipart` — `sk = sk + "@v" + repository.NewVersionID()` — 生成 `id1`）；`internal/service/file_multipart.go:141-144`（`buildObjectFromUpload` — `StorageKey: u.StorageKey` — 保留含 `@v<id1>` 的 storage_key；`VersionID` 未被赋值 → 空字符串）；`internal/service/file_multipart.go:149-166`（`saveMultipartObject` — `InsertObjectVersion(ctx, obj)` — 因 `obj.VersionID == ""` 调用 `NewVersionID()` 生成 `id2` ≠ `id1`）；`internal/repository/sql_objects.go:69-73`（`InsertObjectVersion` — `if obj.VersionID == "" { obj.VersionID = NewVersionID() }` — 无条件覆盖空 ID）；`internal/service/file_features.go:32-40`（`GetVersion` — `repo.GetObjectVersion` 使用 `versionID` 参数查找，找到 row 后 `store.Get(ctx, obj.StorageKey)` — storage_key 含 `@v<id1>` 不匹配 DB 的 `version_id=<id2>`）；`internal/service/file_crud.go:90-100`（作为对比：单次 `Put` 中 `versionID = repo.NewVersionID()` 后直接传参给 `InsertObjectVersion` — **同步路径无此 Bug**） |
| **3** | **WebDAV 锁系统的内存挥发性与跨协议锁不协调** | 可用性/数据完整性 | **P2** | WebDAV 锁存活于进程内存中，服务器重启后全部丢失。客户端（macOS Finder、Windows Explorer）认为文件仍被锁定，实际锁已消失；或反之——客户端认为未锁定但 WORM 锁 (`locked_until`) 仍阻止写入。无管理 API 查看或清除活动锁。锁不与 Object Lock (`_aero_legal_hold`, `locked_until`) 或并发写入协调 | `internal/api/webdav/dav.go:30`（`LockSystem: xwebdav.NewMemLS()` — 纯内存、无持久化、重启即失）；`internal/api/webdav/dav.go:50-57`（`Handler` 函数 — WebDAV handler 无 `sync.Locker` 或 repository 锁协调）；`internal/repository/repository.go:Object.LockedUntil`（Object Lock `locked_until` — WebDAV 路径无任何读取此字段）；`internal/service/file_crud.go:checkLockBeforeOverwrite`（写入前检查 `locked_until` 但仅用于 `Put`/`Delete` — WebDAV `OpenFile` 无等价检查）；`internal/service/acl.go:SetObjectACL`（ACL 系统也独立于 WebDAV 锁）；`internal/service/file.go:_aero_legal_hold`（Legal Hold 系统——WebDAV 路径完全忽略）；`internal/api/rest/admin.go`（admin API 有 `ListTenants`/`ListKeys` 等但无 `ListWebDAVLocks`/`DeleteWebDAVLock` 端点） |
| **4** | **ListObjectsByTag 客户端过滤分页正确性缺陷** | 数据完整性/产品正确性 | **P2** | `ListObjectsByTag` 先调用 `ListObjects` 获取一页（最多 1000 条），然后在内存中按 tag key/value 过滤。若匹配对象跨越多个分页页——例如第 1 页有 1000 个非匹配对象，全部匹配 tag 的对象在第 2 页——则第 2 页永不会被查询，匹配对象完全丢失。在拥有大量对象的桶中，这是一个**静默数据不可见**的 bug | `internal/repository/sql_objects.go:213-239`（`ListObjectsByTag` — 先 `ListObjects` 获取整页，再 `for _, obj := range page.Objects { if obj.Tags == nil { continue } ... }` 过滤。**关键行：没有循环分页**。`tagValue != "" && v != tagValue` 过滤后，结果数若小于等于 limit 则 `page.HasMore = false`——即使后端还有匹配项也被标记为无更多）；`internal/repository/sql_objects.go:235-237`（`if len(page.Objects) > limit { ... page.HasMore = true } else { page.HasMore = false }` — 此逻辑在**过滤后**执行，但 `page.Objects` 已被过滤缩小。大桶中非匹配对象占满首页 → HasMore 为 true → 但 filtered 结果数小于 limit → 被错误设为 false）；`internal/api/rest/handler.go:ListByTag` 和 `s3compat` 路径（下游调用方依赖 HasMore 决定分页——信任虚假的 `HasMore: false`）；`internal/service/file_features.go:31-38`（`ListByTag` — 透明透传 repo 结果，不验证 HasMore 正确性） |
| **5** | **优雅关闭 in-flight 请求排空与后台工作者终结顺序缺口** | 可靠性/运维 | **P2** | 当前 `runServer` 在收到信号后直接调用 `srv.Shutdown(15s 超时)`，不追踪 in-flight HTTP 请求完成、不通知 SSE 客户端（`event: shutdown`）、不持久化 BM25 索引 checkpoint、不按依赖顺序关闭后台工作者（事件总线 → 消费者）。15s 超时后 `srv.Close` 硬杀连接，截断响应。13 个后台 goroutine 随 `ctx` 取消被动退出，无顺序控制 | `cmd/server/main.go:247-268`（`runServer` — `srv.Shutdown(shutdownCtx)` → `bus.Close()` → `_ = shutdownOtel(shutdownCtx)` — 无顺序、无等待）；`internal/events/bus.go:100-103`（`Close` — `close(ch)` 关闭所有 subscriber channel — 不等待消费者排空）；`internal/jobs/jobs.go:Pool.Run`（Job pool 随 `ctx.Done()` 退出 — 不等待正在执行的 job 完成）；`internal/events/webhook.go:80-100`（`Run` — 随 `ctx.Done()` 退出 — 不等待 in-flight HTTP POST 完成）；`internal/reconcile/job.go:42-65`（`Run` — 随 `ctx.Done()` 退出 — 可能截断正在进行的 scrub 对象）；`internal/antivirus/worker.go:48-63`（`Run` — 随 `ctx.Done()` 退出 — 不等待扫描完成）；`internal/replication/replication.go:92-108`（`Run` — 同）；`internal/ai/indexer.go:Run`（Indexer 随 ctx 退出—不等待分块写入完成）；`internal/api/rest/sse.go:69-85`（SSE handler — 不监听关闭事件，直接关闭连接→客户端收到 FIN 而非 `event: shutdown`）；`internal/api/rest/admin.go`（Admin API 无 `POST /v1/admin/shutdown` 管理端点）；`internal/cluster/singleton.go:leaseLoop`（租约续期在 ctx 取消时停止——但可能因来不及释放双副本同时持有锁） |

---

## 方向一：BM25 索引持久性缺口

### 产品价值

系统在 `AI_HYBRID_SEARCH=true` 时提供向量 + BM25 混合检索。BM25 提供**关键词精确匹配**能力，对产品名称、代码片段、缩写（如 "ACL" "REST" "MCP"）的检索质量显著优于纯向量搜索。然而当前 BM25 索引是纯进程内内存结构：

| 场景 | 当前行为 | 期望行为 |
|------|---------|---------|
| 服务器重启（计划内） | BM25 从零重建（遍历全部对象 + 读取所有 chunk） | 从磁盘 checkpoint 恢复（秒级 vs 分钟级） |
| 非版本化桶对象更新 | BM25 不增量更新（`Indexer.Run` 通过事件更新 BM25，但 `BuildFromRepo` 是唯一初始化——`Indexer` 在启动后接管增量更新） | ❌ 如果服务器在 `BuildFromRepo` 完成前收到查询，BM25 返回空；增量更新需要 `Indexer` 已启动并处理了事件 |
| 版本化桶多版本 | BM25 不跟踪哪个版本是当前版本（`BuildFromRepo` 可能索引到过期版本） | 仅索引 active 版本 |
| 大规模部署（10⁶+ objects） | 启动后几分钟内无混合搜索能力 | 30 秒 checkpoint 加载，随后后台异步重建 |
| 滚动更新（Kubernetes） | 每批 Pod 启动均触发全量 BM25 重建 | 新 Pod 从持久化 checkpoint 快速接管搜索 |

### 现状

```go
// internal/ai/bm25.go — 完整内存数据结构
type BM25 struct {
    mu        sync.RWMutex
    docs      []*Document          // 全部文档（内存）
    index     map[string]*Index    // 词项 → { 文档频率, 倒排列表 }（内存）
    buckets   map[string]struct{}  // 桶白名单（内存）
    ready     atomic.Bool          // 构建完成标志（构建完成前返回空）
}

// cmd/server/main.go:577-586 — 唯一初始化路径
func setupBM25Search(ctx context.Context, cfg *config.Config, repo repository.Repository, search *ai.Search) *ai.BM25 {
    var bm *ai.BM25
    if cfg.AI.HybridSearch {
        bm = ai.NewBM25()
        search.WithBM25(bm)
        // ...
        go func() {
            for _, t := range warmTenants {
                _ = bm.BuildFromRepo(ctx, repo, t)  // <--- 全量重建，无 checkpoint
            }
        }()
    }
    return bm
}
```

**`BuildFromRepo` 全量遍历：**
1. 遍历每个 bucket 的 `ListObjects`（分页）
2. 对每个对象调 `ListChunksForObject`（每个对象的 chunk 列表）
3. 对每个 chunk 执行 BM25 分词索引（`AddDocument`）

对于 100K 对象的语料库，平均每个对象 5 个 chunk → 500K 文档的 BM25 索引，内存构建约需 30-120s。在此期间：
- `s.bm25.Search()` 返回空结果（`bm25.Search` 在 `ready` false 时返回空切片）
- `searchLexical` 将空结果传入 `rrfMerge` → 混合搜索退化为纯向量搜索（即使用户明确请求 `mode=hybrid`）
- 无日志或指标告知用户搜索处于降级模式

**增量更新与持久性的断裂：**
- `Indexer`（`internal/ai/indexer.go`）通过事件 `object.created` → `extract → chunk → embed → sink` 链更新 BM25（`Sink.AddChunk`）
- 但这要求 Indexer 在 BM25 初始化**完成之后**运转
- Indexer 启动需要 `bus.Subscribe()`，但 bus 在事件发布前就创建——Indexer 可能比 `BuildFromRepo` goroutine 先启动
- 无 checkpoint：`BuildFromRepo` 不能从上次中断处恢复（大语料库+服务器反复重启 → 永不完结）

### 架构权衡

**方案 A：周期性磁盘 checkpoint（推荐优先级最高）**
- 每 N 个文档变更后或每 M 分钟将 `BM25.index` + `BM25.docs` 序列化为 protobuf/gob 写入 storage
- 启动时先读取 checkpoint（毫米级），然后增量回放 `object_events` 表中 `BuildFromRepo` 之后发生的事件
- 增量事件回放可通过 `NextUnconsumedEvents` 完成（复用现有事件回放机制）
- 权衡：写入 checkpoint 本身有 I/O 开销；`object_events` 表需要保留足够回放长度的事件

**方案 B：使用 Postgres pgFTS 作为 BM25 持久化后端（已有基础设施）**
- `pgFTS` lexical index 已经存在（`ai.PgFTSIndex` 实现 `LexicalIndex`）
- 将 `AI_LEXICAL_BACKEND=pgfts` 作为 BM25 持久化方案，与内存 BM25 互斥或互补
- 权衡：需要 Postgres；pgFTS 排序算法与内存 BM25 不同；需要迁移已有索引

**方案 C：增量 WAL（Write-Ahead Log）**
- 每次 Indexer 更新 BM25 时，同时追加 WAL 记录到本地文件或 storage
- 启动时重放 WAL（而非全量 `BuildFromRepo`）
- 权衡：WAL 重放仍需时间；历史 WAL 需要 compaction；与集群部署的共享存储交互

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| checkpoint 文件损坏 | 跳过 checkpoint，回退全量 `BuildFromRepo`（目前就是此方式） |
| 多副本同时写入 checkpoint | 基于 storage 原子写（先写 `.tmp` 再 rename）或使用 DB 存储 checkpoint |
| 增量回放时对象已被删除 | `InsertObjectVersion` → soft-delete → `HardDeleteObject` —— BM25 需处理文档移除（`RemoveDocument` 或标记删除） |
| 版本化桶中的对象被新版本覆盖 | BM25 应只索引当前（active）版本，旧版本 chunk 需从 BM25 移除 |
| BM25 构建中进程崩溃 | Checkpoint 到达上次成功写入点；未 checkpoint 的变更通过事件回放补充 |
| 纯向量搜索用户不需要 BM25 | `AI_HYBRID_SEARCH=false` 时 BM25 不构建，无影响 |

---

## 方向二：Multipart Upload 版本 ID 与存储键后缀分歧

### 产品价值

版本化桶是合规保留和变更追踪的核心功能。当对象通过分段上传（multipart upload）创建时，其 `version_id` 用于：
- `GET /v1/files/key?version=<id>`（REST）
- `GET /s3/bucket/key?versionId=<id>`（S3）
- 版本列表中的标识（`ListObjectVersions`）
- 跨区复制时的事件追踪

若 storage_key 中的版本后缀与 DB 的 `version_id` 字段不一致：

| 场景 | 影响 |
|------|------|
| 版本特定 GET | 存储层查找 `@v<id1>` blob，但请求传 `version_id=<id2>` — **找到错误的 blob 或 404** |
| 版本列表显示 | 显示 `version_id=<id2>` 但实际读取的是 `@v<id1>` 的内容（或反之） |
| GC/Reconcile 清理 | 清理逻辑依赖 storage_key 匹配——可能清理错误版本 |
| 跨区复制 | 副本中 `version_id` 与 storage_key 后缀也可能不一致 |

### 现状

代码中存在两条创建版本化对象的路径：

**路径 A：`Put`（单次上传）— 正确**

```go
// file_crud.go:90-100
versionID = repository.NewVersionID()     // 生成版本ID: idA
sk = sk + "@v" + versionID               // storage_key = .../key@vidA
// ...
obj := buildPutObject(..., sk, versionID, ...)  // obj.VersionID = "idA"
// ...
saved, err := s.repo.InsertObjectVersion(ctx, obj)
// InsertObjectVersion: obj.VersionID != "" → 直接使用 "idA"
// DB: version_id = "idA", storage_key = ...@vidA ✅ 一致
```

**路径 B：`CompleteMultipart`（分段上传）— 不正确**

```go
// file_multipart.go:31-35 (InitMultipart)
sk = sk + "@v" + repository.NewVersionID()   // storage_key = .../key@v<id1>
// StorageKey持久化在uploads表: u.StorageKey = .../key@v<id1>

// file_multipart.go:141-144 (CompleteMultipart → buildObjectFromUpload)
obj := repository.Object{
    StorageKey:  u.StorageKey,              // .../key@v<id1>
    // VersionID: 未赋值 → ""
}

// file_multipart.go:149-166 (saveMultipartObject → InsertObjectVersion)
// InsertObjectVersion: obj.VersionID == "" → 生成新的 NewVersionID() = <id2>
// DB: version_id = <id2>, storage_key = .../key@v<id1> ❌ 不匹配！
```

**结果：**
- `storage_key` 包含 `@v<id1>` 
- DB 行 `version_id = <id2>`（`id1` ≠ `id2`）
- `GetObjectVersion(ctx, ..., versionID="<id2>")` 找到行 → `store.Get(ctx, obj.StorageKey)` → storage_key `.../key@v<id1>` 来取 blob—与 DB 行中的 version 信息不一致

在 `UpsertObject`（非版本化桶）路径中，同样不会设置 `VersionID`：

```go
// sql_objects.go:14
if obj.VersionID == "" {
    obj.VersionID = NewVersionID()     // 生成新ID
}
```

但非版本化桶的 storage_key 无 `@v<id>` 后缀（`InitMultipart` 仅在 `bcfg.Versioning` 为 true 时添加），所以非版本化桶不受影响。

**唯一受影响场景：版本化桶 + 分段上传。**

### 架构权衡

**方案 A：将 InitMultipart 生成的版本 ID 传递给 CompleteMultipart（最小修复）**
- `InitMultipart` 生成的 `versionID` 存储在 `Upload` 结构体或 metadata 中
- `buildObjectFromUpload` 从此处读取并设置 `obj.VersionID = savedVersionID`
- `InsertObjectVersion` 因 `VersionID != ""` 直接使用
- 权衡：需要 DB migration 在 `uploads` 表新增 `version_id` 列；需保证 `InitMultipart`·和 `CompleteMultipart` 之间的 `version_id`·不重复

**方案 B：CompleteMultipart 重新生成 storage_key（更健壮）**
- `CompleteMultipart` 在得知最终 `version_id` 后重新构建 storage_key 或重命名 blob
- 权衡：重命名 blob 代价高（跨后端不可原子）；部分云后端不支持重命名

**方案 C：延迟版本分配（最大改动）**
- `InitMultipart` 不为 storage_key 附加版本后缀
- `CompleteMultipart` 完成存储合并后，在知道最终 `version_id` 的瞬间构建 storage_key
- 权衡：storage layering 需支持在 complete 后重新关联 key；multipart 期间无版本隔离

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| `InitMultipart` 后 Upload 记录丢失（DB 坏了但 storage 有 parts） | `AbortMultipart` 已清理——但无 GC 兜底 |
| 版本化桶 + 并发 CompleteMultipart 相同 key | 事务序列化确保只有一个成功 |
| storage key 中的版本 ID 与 `ListObjectVersions` 返回的不同 | 客户端检测到不一致——数据完整性受损 |
| 跨区复制中途完成的分段上传 | 复制的副本也继承错误的 storage key—连锁影响 |

---

## 方向三：WebDAV 锁系统的内存挥发性与跨协议锁不协调

### 产品价值

WebDAV 是操作系统桌面集成的主要协议（macOS Finder "Connect to Server"、Windows Explorer "Map network drive"）。用户期望当他们的文件管理器打开一个文件时，其他客户端不会意外覆盖它。当前实现有三大问题：

| 问题 | 影响 |
|------|------|
| **锁在重启后丢失** | 服务器重启后所有 LOCK 消失——其他客户端可以在用户编辑时覆盖文件；或客户端认为已锁定而实际锁已消失导致 IO 错误 |
| **锁不与 WORM/Object Lock 协调** | WebDAV 可以对设置了 `locked_until` 或 `_aero_legal_hold=ON` 的对象成功申请 LOCK——绕过合规保留 |
| **无锁管理面** | 管理员无法查看活跃锁列表、无法强制清除过期锁、无法识别哪个客户端持有哪些锁 |

### 现状

```go
// internal/api/webdav/dav.go:30
dav := &xwebdav.Handler{
    Prefix:     prefix,
    FileSystem: fsys,
    LockSystem: xwebdav.NewMemLS(),  // <--- 仅内存，重启丢失
    Logger:     func(r *http.Request, err error) { ... },
}
```

`xwebdav.NewMemLS()` 是 golang.org/x/net/webdav 提供的标准实现，使用 `sync.Mutex` + `map` 存储锁状态。正常运行时功能完备，但：

1. **重启丢失**：无 `save`/`restore` 机制
2. **无锁持久化**：不写入 DB
3. **无锁协调**：`davFS.OpenFile`（`dav.go:84`）打开文件用于写入时，`xwebdav.Handler` 会在持有 LOCK 的前提下调用 `OpenFile`——但 `davFS.OpenFile` 内部不检查 `locked_until` 或 `_aero_legal_hold`
4. **无锁管理**：DB 中无 `webdav_locks` 表，admin API 无锁相关端点

```go
// internal/service/file_crud.go:checkLockBeforeOverwrite
func (s *FileService) checkLockBeforeOverwrite(ctx context.Context, tenant, bucket, key string, versioning bool) error {
    // Put路径有此检查——但 WebDAV OpenFile 不调用此函数
    if !versioning {
        if cur, err := s.repo.GetObject(ctx, tenant, bucket, key); err == nil {
            if cur.LockedUntil != nil && cur.LockedUntil.After(time.Now()) {
                return fmt.Errorf("%w: overwrite blocked until %s", ErrLocked, ...)
            }
        }
    }
    return nil
}
```

**类似问题在 `hardDeleteObject` 中**——检查 `locked_until` 和 `_aero_legal_hold`——但 WebDAV 路径完全绕过这些检查。

### 架构权衡

**方案 A：持久化锁存储（DB-backed LockSystem）**
- 实现 `xwebdav.LockSystem` 接口（`Create`/`Refresh`/`Unlock`/`UnlockByToken`）的 DB 后端
- 新增 `webdav_locks` 表（token, path, owner, depth, expiry, created_at）
- 权衡：锁操作变为 DB 写，增加延迟但获得持久性

**方案 B：在 FileService 层统一锁检查**
- `davFS.OpenFile` 在首次写入前检查 `checkLockBeforeOverwrite` 等效逻辑
- LOCK 操作通过 `davFS.OpenFile` 而非 xwebdav.Handler 自动拦截
- 权衡：不解决锁持久性问题，但至少防止合规绕过

**方案 C：混合——DB 持久化 LOCK + WebDAV handler 集成**
- 方案 A + `xwebdav.Handler` 的锁回调中检查 Object Lock
- LOCK 请求 → DB 持久化 + check `locked_until`+ `_aero_legal_hold`
- 重启后锁自动过期（基于 expiry 时间戳）

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| 客户端崩溃未 Unlock | 锁在 expiry 后自动过期（`xwebdav.NewMemLS` 默认无限期——需要实现 expiry） |
| 两个客户端同时 LOCK 相同文件 | DB 锁行 + 悲观锁（`FOR UPDATE`） |
| macOS Finder 自动锁定行为 | Finder 在打开文件前 LOCK——锁持久化确保重启后 Finder 正确恢复 |
| 锁与 S3 PUT 冲突 | S3 PUT（通过 REST/S3 协议）也需要检查 WebDAV 锁——需统一锁检查接口 |
| `x-amz-object-lock-legal-hold: ON` 的对象被 WebDAV LOCK | 应拒绝 LOCK（返回 423 Locked） |

---

## 方向四：ListObjectsByTag 客户端过滤分页正确性缺陷

### 产品价值

基于标签的对象检索对于云资源管理至关重要——标记允许用户按自定义维度（项目、环境、团队、合规级别）组织对象。当 `ListObjectsByTag` 在大型桶中静默丢失匹配对象时：

| 场景 | 影响 |
|------|------|
| 标签驱动的工作流（如"列出所有 `env:production` 的对象"） | 仅返回第一页内的匹配对象；`HasMore: false` → 用户认为列表已完整，实际有更多对象 |
| 清点/合规审计 | 因标签过滤的正确性缺陷，遗漏合规覆盖范围内的对象 |
| SDK 的自动分页迭代器 | Python/JS SDK 的 `iter_objects(tag_key=..., tag_value=...)` 信任 `has_more` 标志——静默截断 |

### 现状

```go
// internal/repository/sql_objects.go:213-239
func (s *sqlStore) ListObjectsByTag(ctx context.Context, tenant, bucket, prefix, marker string, limit int, tagKey, tagValue string) (ListPage, error) {
    page, err := s.ListObjects(ctx, tenant, bucket, prefix, marker, limit)   // 1. 获取完整一页（最多1000条）
    if err != nil {
        return page, err
    }
    var filtered []Object
    for _, obj := range page.Objects {                                        // 2. 客户端过滤
        if obj.Tags == nil { continue }
        v, ok := obj.Tags[tagKey]
        if !ok { continue }
        if tagValue != "" && v != tagValue { continue }
        filtered = append(filtered, obj)
    }
    page.Objects = filtered
    if len(page.Objects) > limit {                                            // 3. 判断 HasMore（错误！）
        page.Objects = page.Objects[:limit]
        page.HasMore = true
        page.NextMarker = page.Objects[len(page.Objects)-1].Key
    } else {
        page.HasMore = false                                                  // ← 错误！过滤后的对象数 <= limit 不代表后端没有更多匹配对象
    }
    return page, nil
}
```

**并发安全：** `ListObjects`（SQL `WHERE key LIKE $3 AND key > $4 ORDER BY key ASC`）在分页之间不存在快照隔离——在分页循环中插入/删除对象可能导致对象被跳过或重复。但这是 `ListObjects` 本身的已知限制（大多数对象存储列表操作是尽力一致的）。

**性能：** 即使有正确的分页，每页都需全量扫描 1000 行然后在应用层丢弃 99%。在大桶中（10⁶ 对象），遍历所有页面的代价极高。索引缺失的 SQLite 上尤为严重（v104 方向三覆盖）。

**正确的分页实现应包含：** 在 DB 层进行标签过滤（而非应用层）或 使用游标分页 + 循环直到找到足够匹配对象。

### 架构权衡

**方案 A：DB 层标签过滤（推荐，修复正确性 + 性能）**
- 将 tags 列从 `JSON` 扁平化为 `tag_key`/`tag_value` 索引表
- `ListObjectsByTag` 使用 SQL `JOIN` 或 `EXISTS` 过滤
- 权衡：需要 migration 新增 `object_tags` 表；写入路径需维护双表

**方案 B：循环分页（最小正确性修复）**
- `ListObjectsByTag` 内部循环调用 `ListObjects` 直到收集够 `limit` 个匹配对象或后端无更多数据
- 权衡：每次调用需消耗更多 DB 查询；`NextMarker` 需追踪真实的分页位置

**方案 C：JSON 索引 + 虚拟列（Postgres-only）**
- Postgres 中在 `objects.tags` 上建立 GIN 索引
- `SELECT ... WHERE tags @> '{"key": "value"}'` 在 DB 层过滤
- 权衡：仅 Postgres；SQLite 不支持

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| 所有 1000 个对象都有匹配标签 | 正确返回 `HasMore: true` + `NextMarker` |
| 桶中仅前 500 对象有标签，后 500 无标签 | filtered 为 500，大于 limit（例 100）→ `HasMore: true`。但 NextMarker 为**过滤后的第 limit 个对象的 key**，下一次 `ListObjects` 从该 key 开始——这将跳过第 100~500 之间的非标签对象，可能直接跳到第 501 个之后，从而丢失第 101~500 之间的标签对象。**这是第二个分页正确性 bug。** |
| 空标签值（未设置 Tags） | `obj.Tags == nil` → 跳过，正确 |
| `tagValue = ""` | 只检查 key 存在，正确 |
| 并发标签更新 | 已在第一页范围内的对象的标签在分页间隙被更新——其他页扫描到该对象的旧标签状态（不一致但可接受） |

---

## 方向五：优雅关闭 in-flight 请求排空与后台工作者终结顺序缺口

### 产品价值

生产环境中的服务器重启（滚动更新、Scale-in、故障恢复）要求现有请求被完整处理而非截断：

| 场景 | 当前行为 | 影响 |
|------|---------|------|
| 客户端正在下载 5GB 文件 | `srv.Shutdown` 15s 后硬中断连接 | 客户端收到截断数据 → 文件损坏 |
| 100 个 SSE 客户端连接 | 连接被关闭，无通知 | 客户端收到 FIN 包，无上下文 → 不会自动重连 |
| BM25 索引处于内存，未 checkpoint | `ctx.Done()` 触发所有 goroutine 退出 | 索引丢失，启动后全量重建 |
| Webhook 正在 POST | `ctx.Done()` → bus.Close() → webhook.Run 退出 | 退出的瞬间可能丢失 in-flight POST 响应 |
| Job worker 正在执行复制 | Job pool 退出 | 正在执行的 job 被截断，标记为 pending → 重试 |
| 跨副本锁持有者 (lease holder) 退出 | Lease 到期前新副本可能无法接管 | 双重执行的窗口或服务中断 |

### 现状

```go
// cmd/server/main.go:247-268
func runServer(ctx context.Context, handler http.Handler, cfg *config.Config, logger *slog.Logger, bus *events.Bus, shutdownOtel func(context.Context) error) error {
    srv := &http.Server{Addr: ...}

    // ...ListenAndServe...

    select {
    case <-ctx.Done():             // SIGTERM/SIGINT → 直接开始关闭
    case err := <-errCh:
    }

    shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
    defer cancel()
    if err := srv.Shutdown(shutdownCtx); err != nil {  // ① 15s 硬超时
        return err
    }
    bus.Close()                                         // ② 关闭所有订阅者通道——未排空
    _ = shutdownOtel(shutdownCtx)                       // ③ OTel 关闭
    return nil
}
```

**问题清单：**

| # | 问题 | 代码锚点 | 严重度 |
|---|------|---------|--------|
| 1 | `srv.Shutdown` 不追踪 in-flight 请求 | `net/http.Server.Shutdown` — 停服后不接受新请求但已处理中的请求被 15s 超时硬中断 | 高 |
| 2 | SSE 客户端无关闭通知 | `sse.go:liveStream` — 在 `sub` 通道关闭后 `for { select { case e := <-sub: ... } }` 退出，不发送 `event: shutdown` | 中 |
| 3 | BM25 索引不 checkpoint | `main.go:577-586` — `setupBM25Search` 的 goroutine 随 ctx 退出 | 中 |
| 4 | 后台工作者无序关闭（总线→消费者依赖） | `bus.Close()` 关闭所有订阅者通道 → 但 antuvirus/replication/indexer/webhook 还在运行中，被通道关闭打断 | 高 |
| 5 | Job pool 不等待正在执行的 job | `jobs.go:Pool.Run` — `for { select { case <-ctx.Done(): return ...` — 不调 `srv.Shutdown` 等效的排空 | 中 |
| 6 | Lease 持有者不释放租约 | `singleton.go:Guard` — `for { select { case <-ctx.Done(): return` — 租约到期才自然释放（最多 TTL） | 低 |
| 7 | 无法提前触发关闭（管理 API） | admin.go 无 `POST /v1/admin/shutdown` | 低 |
| 8 | 重复 SIGTERM 无保护 | `signal.NotifyContext` 仅接收第一次信号——第二次 SIGTERM 直接 `SIGKILL` | 高 |

### 架构权衡

**方案 A：in-flight 请求追踪 + 分阶段关闭（推荐）**
- 使用 `sync.WaitGroup` 在 `http.Server.ConnContext` 或 middleware 中追踪活跃请求
- 分阶段：① `/readyz` 返回 503 → ② 等待 LB 排空（可配 5-30s）→ ③ `srv.Shutdown` + 等待 in-flight 完成 → ④ 发送 SSE shutdown 事件 → ⑤ 关闭工作者（反依赖顺序）→ ⑥ BM25 checkpoint → ⑦ 释放 lease → ⑧ 关闭总线

**方案 B：工作者注册表 + 有序关闭**
- `main.go` 维护 `[]Shutdowner` 列表（各工作者注册关闭回调）
- 按依赖顺序（先消费者、再生产者和总线）关闭
- 每个工作者有可配的排空超时

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| 15s 超时后仍有 in-flight 请求 | 记录 metrics（`shutdown_in_flight_killed`）；对截断请求发送 `Connection: close` |
| SSE 客户端重连 | 客户端收到 `event: shutdown` → 指数退避重连（当前 SDK 无此逻辑） |
| 关闭中新的 SIGTERM | 忽略（`signal.Notify` 只处理一次或通过 `signal.NotifyContext` 重置） |
| Job pool 排空超时 | `FailJob` 正在执行的 job（允许 retry） |
| BM25 checkpoint 写入失败 | 记录 warn，不阻塞关闭——启动时全量重建 |
| 集群中其他副本接管 | Lease holder 关闭前释放租约 → 其他副本 `Guard` 快速获取 |

---

## 影响矩阵

| # | 方向 | 影响面 | 修复成本估计 | 用户可见性 | 与既有功能交集 |
|---|------|--------|------------|-----------|-------------|
| 1 | BM25 索引持久性 | 所有启用 `AI_HYBRID_SEARCH` 的部署 | 中（checkpoint 序列化 + 事件回放，~3-5 工作日） | 每次重启后的混合搜索恢复时间 | Indexer、EventBus、BM25 |
| 2 | Multipart 版本键分歧 | 启用版本化 + 分段上传的所有对象 | 低（修复 `buildObjectFromUpload` 传递 version ID，~1-2 工作日） | 版本特定 GET 对 multipart 对象的一致性 | FileService multipart、Repository、Storage |
| 3 | WebDAV 锁挥发性 | 使用 WebDAV 协议的所有部署 | 高（DB-backed LockSystem 实现 + 跨协议检查，~5-10 工作日） | WebDAV 客户端在重启后的锁状态 | WebDAV、Object Lock、Legal Hold |
| 4 | Tag 搜索分页缺陷 | 使用 tag 搜索的大桶场景 | 中（DB 层标签索引或循环分页，~3-5 工作日） | 标签搜索返回结果的完整性 | ListObjects, Tags, S3 listing |
| 5 | 优雅关闭排空 | 所有生产部署 | 高（in-flight 追踪 + 有序关闭 + worker 注册表，~8-15 工作日） | 关闭期间的请求完成率、数据完整性 | 全后台工作者、SSE、BM25、Lease |
