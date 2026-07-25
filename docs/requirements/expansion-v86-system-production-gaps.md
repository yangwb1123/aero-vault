# AeroVault 资深架构师/产品经理视角 — 第 86 轮：生产级坚固性盲区

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 全部 23+ 子包，三套 SDK，MCP 双模式，Web UI，48 对迁移文件，`deploy/` 配置，Makefile，CI gate）  
> **去重验证：** 对 `docs/requirements/` 下全部 85 份既有分析文档逐方向进行正则交叉验证 + 语义比对 + 代码锚点映射  
> **日期：** 2026-07-11  
> **核心原则：** 选取代码中存在具体锚点、可量化生产影响、且在前 85 轮分析中**零实质性独立分析**或**仅有路过提及**的系统盲区。每个方向包含代码锚点、影响分析、既有覆盖证明、边界情况枚举。

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 | 既有分析覆盖 |
|---|------|------|--------|---------|---------|-------------|
| **1** | **存储-元数据写入路径孤儿与幽灵（Storage-Metadata Write Path Orphan Gap）** | 数据完整性/韧性 | **P1** — `storage.Put` 与 `repo.UpsertObject` 之间无事务边界：前者成功后者失败 → 永久孤儿 blob；`storage.Delete` 与 `repo.HardDeleteObject` 同理 → 幽灵元数据。**无 GC 回收故障诱导的孤儿 blob**，Reconcile 仅处理版本保留与软删除清除，不处理写入路径故障残留 | `internal/service/file_crud.go:173-205`（Put 路径：`store.Put` 在前 → `writePutObject` 在后）；`internal/service/file_crud.go:285-316`（hardDeleteObject 路径：`store.Delete` → `repo.HardDeleteObject`）；`internal/reconcile/job.go:scanAll`（仅处理 `ListExpired`/`ListSoftDeletedBefore`——零孤儿检测）；`internal/repository/repository.go`（`ListActiveObjects` 不在 Storage 中交叉验证） | ✅ **零实质性架构分析**（v48 方向二覆盖 multipart 并发一致性但无 Put/Delete 路径的分析；v79 方向五覆盖并发删除/覆盖的版本裂痕但聚焦竞态条件而非故障隔离；v40 方向四以概念方向标识"孤儿存储对象检测"但无代码锚点、无影响量化。**本方向首次以代码锚点驱动的写入路径事务边界分析**） |
| **2** | **搜索结果缓存无事件驱动失效（Search Result Cache Has No Event-Driven Invalidation）** | 检索质量/数据新鲜度 | **P2** — `resultCache` 仅依赖 TTL 被动过期。对象被创建/更新/删除后，相同查询继续返回过期结果直到 TTL 到期。EventBus 已正确发射 `EventCreated`/`EventDeleted`，但缓存不订阅任何事件做主动失效。索引器每秒处理的事件量正好可用于做精确的缓存条目失效 | `internal/ai/result_cache.go:12-60`（`resultCache.get` 仅检查 `time.Now().After(e.expiry)`——零事件驱动失效逻辑）；`internal/ai/search.go:69-80`（`WithResultCache` 注册缓存但不关联事件总线）；`internal/events/bus.go:74-82`（`EventCreated`/`EventDeleted` 发射——缓存零订阅）；`internal/ai/indexer.go:80-85`（Indexer 消费事件但仅用于索引操作，不通知缓存）；`internal/ai/result_cache_test.go`（测试仅覆盖 TTL 过期和并发安全——零事件驱动测试） | ✅ **零实质性架构分析**（v84 方向四在 CDN 集成上下文中以一行"缓存失效事件"概念性提及；v78 方向五一行提及"Postgres NOTIFY 用作 key cache 缓存失效"——聚焦 auth key cache 而非 search result cache；v6/v13/v37/v40/v42/v45/v57/v63 在 S3 / 性能方向中一行路过提及"需要缓存失效机制"。**零独立分析 resultCache 的事件驱动失效设计与实现路径**） |
| **3** | **复制工作者无进度跟踪与滞后监控（Replication Worker Lacks Progress Tracking & Lag Monitoring）** | 运维/数据一致性 | **P2** — Replication Worker 消费 EventBus 并投递 job，但完全无可见性：无最近成功复制的事件 ID 记录（watermark）、无复制滞后度量（lag）、无待複制对象队列统计、无失败/成功比率。多区域部署中，运维人员无法判断复制是否健康。事件总线广播丢弃的复制事件永久丢失，且操作者不知道已丢失 | `internal/replication/replication.go:48-82`（`Worker.Run` —— 消费事件流 `sub <-chan repository.Event`，不做任何 offset 追踪）；`internal/replication/replication.go:84-126`（`ReplicateObjectByID` —— 仅返回 error，不写 watermark、不更新状态、不记录 lag）；`internal/telemetry/metrics.go`（15+ 领域指标——**零复制相关**：无 `replication_lag_seconds`、`replication_queue_depth`、`replication_failures_total`）；`internal/repository/repository.go`（无 `replication_watermarks` 表或 `GetReplicationWatermark` 接口）；`internal/events/bus.go:96-99`（`broadcast` 静默丢弃事件——复制永久丢失）；`internal/repository/sql_events.go`（`object_events` 表无 consumed_by 消费标记） | ✅ **零实质性独立架构分析**（`grep -rn "replication.*progress\|replication.*watermark\|replication.*lag\|replication.*status\|replication.*stat\|replication.*monitor\|replication.*health\|replication.*metric\|replication.*gauge\|replication.*offset" docs/requirements/` → **8 次命中**，其中 extensions.md 的"复制拓扑"小节以一行提及「复制滞后度量」，v65 方向二以一行概念性列出「replication lag metric」——两者均为单行标题级提及，**零代码锚点、零影响分析、零实施路径**。v21/v35/v47/v4 涉及复制功能但均聚焦复制引擎实现而非可观测性） |
| **4** | **速率限制器每进程独立——无分布式协调（Rate Limiter Is Per-Process — No Distributed Coordination）** | 多副本/多租户可靠性 | **P1** — `RateLimiter` 是纯内存令牌桶，每个进程独立。N 副本部署中，租户通过轮询各副本可实现 N× 配置上限的请求速率。全局 `RATE_LIMIT_RPS` 仅在单副本场景有效，多副本时形同虚设。无 Redis/Postgres 背书的集中式限流、无自适应限流、无基于负载的限流、无限流配置运行时热更新 | `internal/middleware/ratelimit.go:30-80`（`RateLimiter` —— `tokens` 和 `lastRefill` 是 `sync.Mutex` 保护的内存字段——纯进程内）；`cmd/server/main.go:157-160`（`rl := middleware.NewRateLimiter(cfg.RateLimit.RPS, cfg.RateLimit.Burst)`——每副本独立创建）；`internal/config/config.go`（`RateLimit` 结构仅 `RPS`/`Burst`/`AIRPS`/`AIBurst`——无 `RateLimitBackend` 或 `RateLimitSyncInterval` 配置）；`internal/middleware/ratelimit_test.go`（仅测试单进程桶的 token 生产/消费——零分布式协调测试） | ✅ **零实质性独立架构分析**（v57 方向三以概念方向标识"分布式限流与跨副本协调缺失"并列出代码锚点——但聚焦 JobPool 和 idempotency key 的跨副本协调作为主要场景，限流器本身仅作为附属观点；v31/v53/v55 在 S3 与多区域上下文中一行提及"分布式限流"概念。**本方向为首次以代码锚点全面覆盖 RateLimiter 作为纯内存实现的多副本必然失效、影响量化、与三种可能的分布式后端方案的完整架构分析**） |
| **5** | **缺少请求体大小准入控制（No Request Body Size Admission Control）** | 可靠性/抗放大攻击 | **P1** — 任何客户端可向任意 PUT/POST 端点发送任意大小的请求体，服务器在解析过程中即开始缓冲。无 `MaxRequestBodySize`、无 `MaxUploadSize`、无 `Content-Length` 范围校验。一个恶意客户端（或一个出错的 SDK）发送一个声明 5GB 的单个 PUT 请求，即可迫使服务器分配大量内存/磁盘，触发 OOM 或磁盘满。分片上传的 `UploadPart` 同样无单部分大小上限 | `internal/config/config.go:AppConfig`（`WriteTimeoutSec`/`IdleTimeoutSec`/`RequestTimeoutSec`——但**无 `MaxRequestBodySize`**）；`internal/service/file_crud.go:Put`（`size int64` 参数——由调用方提供，`store.Put` 传递给实现层，但**无 `size > MaxObjectSize` 的拒绝**）；`internal/storage/storage.go`（`Put` 签名接收 `size int64`，零检查）；`internal/api/rest/handler.go:handlePut`（`r.Body` 直接传递给 `h.svc.Put`——无 `http.MaxBytesReader` 包装）；`internal/api/s3compat/handler.go:PutObject`（同——裸 `r.Body` 传递给 `h.svc.Put`）；`internal/api/webdav/dav.go:Put`（同——无大小限制）；`internal/mcp/server.go:CallTool`（`write_file` 工具直接接收 `[]byte` 无大小上限）；`internal/service/file_multipart.go:UploadPart`（`size int64`——无 `MaxPartSize` 检查） | ✅ **零实质性架构分析**（`grep -rn "request.*body.*size\|request.*body.*limit\|max.*body\|max.*upload\|upload.*size.*limit\|request.*size.*limit\|body.*size\|max.*request\|MaxRequestBodySize\|maxRequestBodySize\|body.*admission\|size.*admission\|amplification\|anti.*amplification\|request.*amplif" docs/requirements/` → **3 次命中**。v34 方向表一行列出"请求体大小限制"作为方向概念；v67 方向一 MCP 安全分析中以一行提及 `write_file` 无大小限制作为 MCP 安全子点；v53 方向三自适应过载保护提及"入口反压"作为负载告警而非请求体准入。**零独立代码锚点分析、零跨协议（REST/S3/WebDAV/MCP）一致性分析、零边界情况枚举**） |

---

## 方向一：存储-元数据写入路径孤儿与幽灵

### 现状

当前写入路径中存储后端与元数据仓库之间**不存在事务边界**，每个成功/失败状态的排列都可能产生不一致：

```go
// internal/service/file_crud.go — Put 路径
func (s *FileService) Put(ctx context.Context, tenant, bucket, key string, r io.Reader, size int64, opts PutOptions) (repository.Object, error) {
    // … preflight, checkLock, build storage key …
    info, err := s.store.Put(ctx, sk, reader, size, ...)   // ← 第 1 步：存储写入
    if err != nil {
        return repository.Object{}, err
    }
    // … verifyMD5, storeContentMD5 …
    obj := s.buildPutObject(key, tenant, bucket, bcfg, opts, sk, versionID, info)
    return s.writePutObject(ctx, obj, bcfg)                  // ← 第 2 步：元数据写入
}
```

```go
// internal/service/file_crud.go — hardDeleteObject 路径
func (s *FileService) hardDeleteObject(ctx context.Context, obj repository.Object, tenant, bucket, key string) error {
    if s.chunkCleaner != nil {
        _ = s.chunkCleaner.DeleteObjectChunks(ctx, obj.ID)  // ← 第 1 步
    }
    if err := s.store.Delete(ctx, obj.StorageKey); err != nil {  // ← 第 2 步
        return fmt.Errorf("storage delete: %w", err)
    }
    if err := s.repo.HardDeleteObject(ctx, tenant, bucket, key); err != nil { // ← 第 3 步
        return err
    }
    // …
}
```

### 四种不一致状态

| # | storage.Put | repo.UpsertObject | 结果 | 当前处理 |
|---|-------------|-------------------|------|---------|
| ✅ | 成功 | 成功 | 正常 | ✅ 正常路径 |
| ❌ | 失败 | 未执行 | 正常拒绝 | ✅ `Put` 返回 error |
| ⚠️ | **成功** | **失败（网络/约束/死锁）** | **孤儿 Blob** —— 存储中有内容但元数据行不存在，永不访问，永不回收 | ❌ **无 GC** |
| ⚠️ | 未执行 | 失败（前置校验） | 正常拒绝 | ✅ 前置校验 |

| # | chunkCleaner | store.Delete | repo.HardDeleteObject | 结果 | 当前处理 |
|---|-------------|--------------|----------------------|------|---------|
| ✅ | 成功/失败 | 成功 | 成功 | 正常 | ✅ 正常路径 |
| ❌ | N/A | 失败 | 未执行 | 正常拒绝 | ✅ `Delete` 返回 error |
| ⚠️ | N/A | **成功** | **失败** | **幽灵元数据** —— 元数据行指向一个不存在的存储 blob，所有 GET 返回 404，但 List 继续显示该对象 | ❌ **无修复** |

类似的，分片上传的 `CompleteMultipart` 中，`store.CompleteMultipart` 成功后 `InsertObjectVersion` 失败会导致合并后的 blob 成为孤儿。

### 为什么重要

| 场景 | 后果 |
|------|------|
| Put 写入 S3 成功但 Postgres 约束违例致 `UpsertObject` 失败 | 存储桶中有一个永远不被引用的对象。SLI 显示 100% 写入成功率（`store.Put` 成功了），但用户看到的是 500 |
| 硬删除时 `store.Delete` 成功但 `repo.HardDeleteObject` 失败（DB 连接瞬断） | 对象列表持续显示已删除对象，生命周期规则无法清理（`storage_key` 不指向任何 blob），List 响应中包含大量"幽灵"对象 |
| 磁盘满导致 local storage 的 `store.Put` 部分写入了 4KB 后成功返回，但 `writePutObject` 失败 | 孤儿 blob 占用磁盘空间，加速磁盘满的恶性循环 |
| 集群副本在 `store.Put` 成功后、`writePutObject` 前崩溃 | 重启后 blob 完全丢失引用——需要人工扫描存储目录恢复 |

### 代码锚点

| 位置 | 当前状态 | 缺口 |
|------|---------|------|
| `internal/service/file_crud.go:173-176`（`Put` 中的 `store.Put`） | 成功后才调用 `writePutObject` | 无事务边界，无回滚补偿 |
| `internal/service/file_crud.go:285-287`（`hardDeleteObject` 中的 `store.Delete`） | 成功后调用 `repo.HardDeleteObject` | 同 |
| `internal/reconcile/job.go:scanAll` | 遍历 `ListExpired` + `ListSoftDeletedBefore` | 不扫描孤儿 blob，不比对 storage vs repo |
| `internal/repository/repository.go:Object` | `StorageKey` 是物理引用 | 无 `orphan_scan` 表或 `last_verified_at` 字段 |
| `internal/storage/factory.go` | 创建 Storage 后端 | 无 `orphanGC` 包装器 |
| `internal/storage/storage.go:List` | 存储后端可按 prefix 列出对象 | 可用于发现孤儿但从未用于此目的 |
| `internal/telemetry/metrics.go` | 15+ 领域指标 | 无 `storage_orphan_blobs_total` / `storage_metadata_ghosts_total` |
| `internal/storage/local_list.go:List` | Local 后端完整列出目录 | 可作为孤儿检测的基础 |

### 推荐方案（概念级）

1. **写入路径补偿操作**：`FileService.Put` 在 `writePutObject` 失败时自动调用 `store.Delete(sk)` 回滚 blob —— 最少补偿、最大安全性
2. **周期性孤儿扫描**：Reconcile Job 新增 `OrphanScan` 任务。遍历 `storage.List(tenant)` 与 `repo.ListActive()` 取差集，删除存储中无元数据的 blob
3. **幽灵修复**：Reconcile 同时检测 `repo.ListActive()` 中存在但 `storage.Stat(key)` 404 的对象，自动清理元数据行或标记 `_aero_storage_missing`
4. **指标**：`storage_orphan_blobs_total{backend}` + `storage_metadata_ghosts_total` + 扫描耗时
5. **迁移**：`0025_orphan_tracking` 为 `objects` 表加 `last_verified_at` 列

### 边界情况

| # | 场景 | 预期行为 |
|---|------|---------|
| B1 | 补偿 `store.Delete` 也失败 | `Put` 返回原始 `writePutObject` 错误，孤儿 blob 等待定时 GC 回收 |
| B2 | 孤儿属于启用了版本控制的桶 | 孤儿可能是特定版本——`@v<id>` 后缀可追踪版本归属，GC 只删除超时孤儿 |
| B3 | 存储后端不提供跨租户 List（S3/OSS/COS 限制 prefix 枚举） | 按 bucket prefix 枚举（`tenant/bucket/`），每次一个 bucket |
| B4 | 并发写入同一 key 时补偿误删另一个 Put 的 blob | 补偿仅删除 `storageKey`，版本化桶中每个 blob key 唯一含 `@v<id>`，不会误伤 |
| B5 | 用户有意通过外部工具将 blob 写入存储后端 | 新 blob 在下一轮孤儿扫描中被识别并作为孤儿删除——这是预期行为，要求写入必须通过服务层 |

---

## 方向二：搜索结果缓存无事件驱动失效

### 现状

`resultCache` 的设计注释明确警告了缓存陈旧性：

```go
// internal/ai/result_cache.go:15-20
// STALENESS: cached results can go stale as the corpus changes (new/edited/
// deleted chunks are not reflected until the entry expires). The TTL bounds how
// stale a result can be, which is why result caching is opt-in and defaults to
// a short TTL.
```

这并非一个隐藏的坑——而是代码注释中主动承认的设计局限。然而当前 TTL 是唯一的失效机制。对于以下场景：

| 场景 | TTL 时效 | 用户感知 |
|------|---------|---------|
| 用户上传新文档后立即搜索新内容 | 最长等待 TTL（默认？秒） | "我刚上传的文件搜索不到" |
| 敏感文档被删除后立即搜索 | 最长等待 TTL | "已删除的文档还在搜索结果中" |
| 索引器更新已变更文档的 chunk 后搜索 | 最长等待 TTL | "修改前的旧内容还在搜索结果中" |
| 租户 A 的对象变化影响租户 B 的跨租户搜索（若配置） | 最长等待 TTL | 隔离性错觉 |

### 事件驱动失效的可行性

EventBus 已发射 `EventCreated` 和 `EventDeleted` —— 这正是缓存失效所需的信号：

```go
// internal/service/file_crud.go:Put
s.emit(ctx, saved, repository.EventCreated)   // 新/更新对象 → 需失效该对象的 cache entries

// internal/service/file_crud.go:hardDeleteObject
s.emit(ctx, obj, repository.EventDeleted)      // 删除对象 → 需失效该对象的 cache entries
```

索引器处理事件时恰好知道哪些 chunk 变更了，以及这些 chunk 对应哪些搜索查询的 cache key。缓存失效可以做到**精准到 entry** 而非全量清空。

### 代码锚点

| 位置 | 当前状态 | 缺口 |
|------|---------|------|
| `internal/ai/result_cache.go:40-55`（`get`/`put`） | 仅 TTL 失效 | 无 `invalidate(key string)` / `invalidateByPrefix(prefix string)` 方法 |
| `internal/ai/result_cache.go`（入口） | 无 `NewResultCacheWithBus(bus.EventBus)` 构造器 | 缓存不知道 EventBus 的存在 |
| `internal/ai/search.go:59-64`（`WithResultCache`） | 仅设置 TTL+capacity | 无事件总线参数 |
| `internal/ai/indexer.go:80-85`（`handle`） | 处理事件后更新索引 | 不通知缓存 |
| `internal/events/bus.go:74-82`（`Publish`） | 发射事件 | 缓存不订阅 |

### 影响分析

| 场景 | 当前用户体验 | 加入事件驱动失效后 |
|------|-------------|-----------------|
| 上传文档 → 搜索刚上传的术语 | 等待 TTL（数秒~数分钟） → 才出现 | 索引器完成索引 → 缓存失效 → 即时可见 |
| 删除包含敏感信息的文档 → 确认删除 | 等待 TTL → 依然可见 | 索引器处理删除事件 → 缓存失效 → 立即不可见 |
| 10 个用户同时搜索相同热词 | 第一次搜索慢（embed+retrieval），后续快（命中缓存） | 行为一致 + 任何用户修改语料后热词缓存精准失效 |

### 推荐方案（概念级）

1. **给 `resultCache` 增加 `Invalidate(tenant, bucket, key)` 方法**：当给定 (tenant, bucket, key) 的 chunk 变化时，删除所有包含该对象 chunk 的缓存条目
2. **在 Indexer 处理完事件后调用 `cache.Invalidate`**：索引器已持有 `tenant`/`bucket`/`key` 信息
3. **更激进的方案**：选择性地对 `cache.key` 做三态标记——(a) 精准匹配 key 的失效、(b) 匹配 bucket 的部分失效、(c) 匹配 tenant 的全局失效——避免全量清空
4. **指标**：`search_cache_hits_total` / `search_cache_misses_total` / `search_cache_invalidations_total`

### 边界情况

| # | 场景 | 预期行为 |
|---|------|---------|
| B1 | 大量小对象快速创建/删除 → 缓存频繁失效 | 失效成本低于索引+嵌入+检索成本；若失效频率 > 缓存命中收益，可走 TTL-only 兜底 |
| B2 | TTL 到期与事件驱动失效同时触发 | 无害——已失效的条目再次标记失效是幂等操作 |
| B3 | 缓存条目因 TTL 已过期（`get` 时已删除） | `invalidate` 需要处理 `key not found`——幂等 |
| B4 | 多副本部署中缓存失效广播 | 每副本独立缓存——每副本的 Indexer 独立消费事件，自然独立失效（无需跨副本协调） |

---

## 方向三：复制工作者无进度跟踪与滞后监控

### 现状

`Replication Worker` 的架构是典型的 EventBus → JobPool 模式，但**完全缺乏可观测性**：

```go
// internal/replication/replication.go:48-82
func (w *Worker) Run(ctx context.Context, sub <-chan repository.Event) {
    for {
        select {
        case <-ctx.Done(): return
        case e, ok := <-sub:
            if !ok { return }
            if e.Type != repository.EventCreated || e.ObjectID == nil { continue }
            job := repository.Job{
                Type:    JobReplicate,
                Payload: EncodeObjectID(*e.ObjectID),
            }
            w.queue.Enqueue(ctx, job)   // ← 入队后，再无跟踪
        }
    }
}
```

整个复制管线的可观测性现状：

| 维度 | 状态 | 输出 |
|------|------|------|
| 消费了哪些事件 | ❌ 无追踪 | 每个事件进 `sub` 就无记录 |
| 已入队多少复制 job | ❌ 无计数 | `jobs` 表中有 `type=replicate` 的行 |
| 成功复制了多少对象 | ❌ 无计数 | 仅每个成功对象打一行 INFO log |
| 复制滞后（lag） | ❌ 无度量 | `jobs` 表只有 `created_at` 对比当前时间 |
| 复制失败率 | ❌ 无度量 | 仅每个失败打一行 WARN log |
| 复制目标后端状态 | ❌ 无探测 | 从无健康检查 |

### 为什么重要

| 场景 | 后果 |
|------|------|
| 主区域写入 1000 个对象但副本存储不可用 5 分钟 | 复制 job 全部失败入重试队列，管理员无法知道"距完全同步还有多远" |
| 事件总线丢弃复制事件（buffer 满） | 相关对象**永不被复制**，且无人知道哪几个丢失了 |
| 因网络分区导致复制 job 持续失败 | `jobs` 表积压数百万 replicate job，但无告警触发 |
| 灾备切换前需要确认复制是否追上 | 无任何方式知道"复制滞后为 0"——只能逐个 bucket 比对 |

### 代码锚点

| 位置 | 当前状态 | 缺口 |
|------|---------|------|
| `internal/replication/replication.go:48-82`（`Worker.Run`） | 消费事件→入队列 | 不入队后不记录 event ID，无 watermark |
| `internal/replication/replication.go:84-126`（`ReplicateObjectByID`） | 复制成功/失败 | 不写 `replication_lag`，不更新 watermark |
| `internal/repository/repository.go` | 无 `replication_watermarks` 接口 | 无持久化复制进度 |
| `internal/telemetry/metrics.go` | 15+ 领域指标 | 零复制指标 |
| `internal/repository/jobs.go` | `jobs` 表支持 job 状态查询 | 无 `GetReplicationLag` 查询 |
| `internal/events/bus.go:96-99`（`broadcast`） | 静默丢弃 | 丢弃事件 → 永久丢失 → 零告警 |
| `internal/repository/sql_events.go:object_events` | 事件持久化 | 无 `consumed_by` 标记无法判断复制消费到哪条 |

### 推荐方案（概念级）

1. **Watermark 表**：`replication_watermarks(tenant_id, backend, last_event_id, last_success_at, lag_seconds)`。每次成功复制后更新
2. **Lag 度量**：`replication_lag_seconds{backend, tenant}` gauge = `now() - event.created_at` 的最近值
3. **指标**：`replication_events_total{status(consumed/enqueued/succeeded/failed)}` + `replication_queue_depth` + `replication_lag_seconds`
4. **副本健康探测**：`replica.Storage.Stat("@healthz/replica")` 周期性探测目标后端可达性
5. **Grafana 面板**：复制健康面板——lag 时间线 + 成功率 + 队列深度 + 丢事件计数
6. **Prometheus 告警**：`ReplicationLagHigh`（lag > 5m）、`ReplicationFailureRateHigh`（失败率 > 10% for 5m）

### 边界情况

| # | 场景 | 预期行为 |
|---|------|---------|
| B1 | 配置了多个复制目标（多区域） | 每个目标独立 watermark 和 lag——`backend` 标签区分 |
| B2 | `EventCreated` 事件在复制 job 入队前奔溃 | 事件已在 `object_events` 表持久化。重启后 Worker 重新消费——但 `NextUnconsumedEvents` 已消费标记未设置时回放会重复复制。要求复制操作自身幂等（当前 `ReplicateObjectByID` 是幂等的——覆盖写入） |
| B3 | 复制目标永久不可达 | 所有 job 最终进入 `failed` 状态 → 告警触发 → 人工介入 |
| B4 | Lag 暴增（目标存储限流） | `replication_lag_seconds` 持续上升 → 告警 → 运维扩容目标后端或降级为异步同步 |
| B5 | 事件被广播丢弃 | `dropped` 计数器已存在（`bus.Dropped()`）→ 新增 `replication_events_dropped_total` 指标关联 |

---

## 方向四：速率限制器每进程独立——无分布式协调

### 现状

速率限制完全基于单进程内存令牌桶：

```go
// internal/middleware/ratelimit.go:30-35
type RateLimiter struct {
    mu        sync.Mutex
    tokens    float64       // ← 内存字段，仅本进程可见
    lastRefill time.Time    // ← 内存字段，仅本进程可见
    rate      float64
    burst     int
}
```

在 N 副本部署中的行为：

```
副本 1 (令牌桶 100 RPS)    副本 2 (令牌桶 100 RPS)    副本 3 (令牌桶 100 RPS)
     │                          │                          │
     └── 每秒最多 100 req ──┘── 每秒最多 100 req ──┘── 每秒最多 100 req ──
                                    │
                    租户实际可达速率: 300 RPS (配置的 3×)
```

### 为什么重要

| 场景 | 配置 | 实际可达 | 后果 |
|------|------|---------|------|
| 单副本 | `RATE_LIMIT_RPS=100` | 100 RPS | ✅ 正常工作 |
| 3 副本 | `RATE_LIMIT_RPS=100` | 300 RPS | ❌ 限流形同虚设 |
| 10 副本 | `RATE_LIMIT_RPS=100` | 1000 RPS | ❌ 限流完全失效 |
| K8s HPA 自动扩缩 | `RATE_LIMIT_RPS=100` | N×100 RPS | ❌ 扩容 → 限流天花板同步升高 |

### 代码锚点

| 位置 | 当前状态 | 缺口 |
|------|---------|------|
| `internal/middleware/ratelimit.go:30-35` | `tokens`/`lastRefill` 是内存字段 | 无分布式后端（Redis/Postgres）支持 |
| `internal/middleware/ratelimit.go:139`（`Middleware`） | `rl.Allow()` 检查本地桶 | 无跨副本桶查询 |
| `cmd/server/main.go:157-160` | `rl := middleware.NewRateLimiter(cfg.RateLimit.RPS, cfg.RateLimit.Burst)` | 每副本独立创建 |
| `internal/config/config.go`（`RateLimitCfg`） | 仅 `RPS` / `Burst` / `AIRPS` / `AIBurst` | 无 `Backend` / `SyncInterval` / `KeyPrefix` |
| `internal/middleware/ratelimit_test.go` | 仅测单进程桶行为 | 无分布式协调测试 |

### 影响分析

| 场景 | 当前行为 | 期许行为 |
|------|---------|---------|
| 恶意租户轮询 N 个 K8s pod | 实际速率 = N × `RATE_LIMIT_RPS` | 实际速率 = `RATE_LIMIT_RPS`（全局一致） |
| 流量突发全部发往同一 pod（非均匀分布） | 该 pod 可能限流而其他 pod 空闲 | 限流决策应感知全局负载 |
| 扩容至 5 副本 | 限流能力降为配置的 20% 效率 | 扩容不影响限流边界 |

### 推荐方案（概念级）

三种方案按复杂度/延迟权衡：

**方案 A：Postgres 集中式桶（低延迟要求不严）**
```
Allow() → UPDATE token_buckets SET tokens = tokens - cost WHERE ... AND tokens >= cost RETURNING tokens
```
适合 `RPS < 1000` 场景，利用 Postgres 行级锁做原子读-改-写。
代价：每次请求增加 1-3ms DB 延迟。

**方案 B：Redis 集中式桶（低延迟、高 RPS）**
```
EVAL "local tokens = redis.call('GET', KEYS[1]) ... " 1 bucket_key
```
使用 Lua 脚本在 Redis 端原子执行令牌桶算法。延迟 < 1ms。
代价：引入 Redis 依赖。

**方案 C：混合模式（默认本地，定期同步）**
```
本地桶衰减 + 每 T 秒从中央存储同步一次剩余容量。
中央不可用时降级为纯本地模式（graceful degradation）。
```

### 边界情况

| # | 场景 | 预期行为 |
|---|------|---------|
| B1 | 集中式后端不可用 | 降级为本地限流（宽松模式）或 429 全部请求（严格模式）——通过配置可选择 |
| B2 | 副本间时钟不同步 | 令牌桶算法依赖时间——时钟 skew < 100ms 时误差可接受；NTP 需就位 |
| B3 | Redis/Postgres 延迟抖动导致限流决策变慢 | 异步桶的同步间隔可配置——`RATE_LIMIT_SYNC_INTERVAL=100ms` |
| B4 | 高并发时 Postgres 行锁成为瓶颈 | `SELECT ... FOR UPDATE SKIP LOCKED` 模式——或升级到方案 B |

---

## 方向五：缺少请求体大小准入控制

### 现状

当前所有 PUT/POST 端点均**不对请求体大小做任何限制**。在四种协议和 MCP 中，裸 `r.Body` 被直接传递给下层：

| 协议/端点 | 代码位置 | 保护措施 |
|-----------|---------|---------|
| REST PUT `/v1/files/{key}` | `internal/api/rest/handler.go:handlePut` | `r.Body` 直传——❌ 无 `http.MaxBytesReader` |
| S3 PUT `/{bucket}/{key}` | `internal/api/s3compat/handler.go:PutObject` | `r.Body` 直传——❌ 无限制 |
| S3 分片上传 `?uploadId=` & `?partNumber=` | `internal/api/s3compat/extra.go:uploadPart` | `r.Body` 直传——❌ 无尺寸限制 |
| WebDAV PUT | `internal/api/webdav/dav.go:Put` | `r.Body` 直传——❌ 无限制 |
| MCP `write_file` 工具 | `internal/mcp/server.go:CallTool` | `[]byte` 全部读入内存——❌ 无限制 |
| REST POST `/v1/search` | `internal/api/rest/search.go` | JSON body——❌ 无大小限制 |
| REST POST `/v1/chat` / `/v1/chat/stream` | `internal/api/rest/search.go` | JSON body——❌ 无大小限制 |

配置文件中的 `WriteTimeoutSec` 提供间接保护（大请求在超时时间内未完成即断开），但这不能阻止一个能**在超时时间内完成**的大请求耗尽内存或磁盘。

### 为什么重要

| 场景 | 后果 |
|------|------|
| 发送 Content-Length: 10GB 的 PUT 到 1GB 内存的服务器 | OOM kill（`r.Body` 未被逐块限流） |
| 发送 100MB 表单至 `/v1/chat`（期望 ~1KB body） | JSON 解析器分配 100MB 内存→ GC 压力→ 延迟飙升 |
| 分片上传的 10000 个 partNumber 各发 5GB | 每个 part 单独消耗读缓冲→ 合计容量远超内存边界 |
| MCP `write_file` 写入 500MB 文件 | `[]byte` 全部读入内存→ OOM |
| 将大文件直传至 local storage | 磁盘无预留空间→ 写入到一半 disk full→ 文件系统损伤 |

### 代码锚点

| 位置 | 当前状态 | 缺口 |
|------|---------|------|
| `internal/config/config.go:AppConfig` | 5 个字段（Addr/LogLevel/WriteTimeoutSec/IdleTimeoutSec/RequestTimeoutSec/MaxInFlight/PerTenantMax） | 无 `MaxRequestBodySize` / `MaxUploadSize` |
| `internal/api/rest/handler.go:handlePut` | `r.Body` 直接传递给 `h.svc.Put` | 无 `http.MaxBytesReader(w, r.Body, cfg.App.MaxRequestBodySize)` |
| `internal/api/s3compat/handler.go:PutObject` | 同 | 同 |
| `internal/api/webdav/dav.go:Put` | 同 | 同 |
| `internal/mcp/server.go:CallTool` | `write_file` 字段 | 无大小检查 |
| `internal/service/file_multipart.go:UploadPart` | 接收 `r io.Reader, size int64` | 无 `maxPartSize` 检查 |
| `internal/service/file_crud.go:Put` | 接收 `size int64` | 无单次写入大小上限 |

### 推荐方案（概念级）

1. **配置项**：`APP_MAX_BODY_SIZE`（默认 0 = 不限制，建议默认 100MB）、`APP_MAX_UPLOAD_SIZE`（默认 0 = 不限制）、`S3_MAX_PART_SIZE`（S3 标准：每个 part < 5GB，建议默认 5GB）
2. **REST handler**: `r.Body = http.MaxBytesReader(w, r.Body, cfg.App.MaxBodySize)` 在 `handlePut`/`PostObject` 入口
3. **S3 handler**: 同理，在 `PutObject`/`uploadPart` 入口
4. **WebDAV**: 同上
5. **MCP `write_file`**: 检查 `len(content)` 是否超过 `cfg.App.MaxUploadSize`，超过则返回工具错误
6. **指标**：`request_body_size_bytes{protocol(rpc/s3/webdav/mcp)}` 直方图

### 边界情况

| # | 场景 | 预期行为 |
|---|------|---------|
| B1 | 分片上传的单个 part 超过 `S3_MAX_PART_SIZE` | 返回 `400 EntityTooLarge`（符合 S3 语义） |
| B2 | REST handler 收到 Content-Length > `MaxBodySize` 的 PUT | `http.MaxBytesReader` 返回 error → handler 写 413 Request Entity Too Large |
| B3 | 客户端发送不指定 Content-Length 的 chunked 上传 | `http.MaxBytesReader` 在读取超过限制时返回 error——仍能保护 |
| B4 | Admin API 的大写操作（如批量导入）超过 `MaxBodySize` | 配置 `APP_MAX_BODY_SIZE=0` 可关闭限制（or 单独 admin route 不限制） |
| B5 | 超过限制的请求已经部分写入 storage | `http.MaxBytesReader` 会终止读取，`store.Put` 收到 `io.ErrUnexpectedEOF` → 写入失败 → 存储后端清理 |

---

## 总结：优先级排序与影响矩阵

| 优先级 | 方向 | 核心风险 | 影响面 | 实施复杂度 |
|--------|------|---------|-------|-----------|
| **P1** | 五：请求体大小准入控制 | OOM / disk full / 放大攻击 | 全部 4 协议 + MCP | 低——每 handler 加 `http.MaxBytesReader` + 配置项 |
| **P1** | 一：存储-元数据写入孤儿 | 永久数据残骸 / 幽灵对象 | 全部写入路径 | 中——补偿 + 定时 GC |
| **P1** | 四：分布式限流 | 多副本限流完全失效 | 全部请求路径 | 中-高——需 Redis/Postgres 集中式桶 |
| **P2** | 二：搜索缓存无事件驱动失效 | 检索结果陈旧 / 刚上传文件搜不到 | AI 搜索路径 | 低——Cache Invalidate + Indexer 回调 |
| **P2** | 三：复制无进度跟踪 | 复制健康不可观测 / 数据丢失不可知 | 多区域部署 | 中——Watermark 表 + metrics |

**推荐执行顺序**：方向五（低复杂度高影响）→ 方向一（基础数据完整性）→ 方向二（检索质量）→ 方向三（可观测性）→ 方向四（多副本协调，依赖组件的选型和引入）。
