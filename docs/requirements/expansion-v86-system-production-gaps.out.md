现在我对所有 5 个方向的代码锚点进行了逐行验证。以下是全面验证分析。

---

## 验证报告：对第 86 轮生产级坚固性盲区分析的事实核查与架构评估

> **核查方法：** 对每个方向的每个代码锚点进行精确行号验证，检查函数签名、控制流、条件分支、缺失特性。对既有分析覆盖声明进行正则交叉验证（`grep -rn` 全 `docs/requirements/` 目录 262 文件）。

---

### ⚠️ 方向一：存储-元数据写入路径孤儿与幽灵 — **核心论点有效，但存在重大事实偏差和低估现有保护**

#### 事实核查摘要

| 声明 | 代码锚点 | 状态 | 详情 |
|------|---------|------|------|
| `store.Put` 与 `writePutObject` 间无事务边界 | `file_crud.go:71-131` | ✅ 确认 | `Put` 第 111 行 `store.Put` → 第 131 行 `writePutObject` — 栈上顺序调用，无 DB 事务 wrapper |
| `store.Delete` 与 `repo.HardDeleteObject` 间无事务边界 | `file_crud.go:367-398` | ✅ 确认 | `hardDeleteObject` 第 384 行 `store.Delete` → 第 387 行 `repo.HardDeleteObject` — 原子性无保证 |
| Reconcile 仅处理版本保留与软删除清除——零孤儿检测 | `reconcile/job.go` | ❌ **严重事实错误** | **实际代码已实现双向孤儿扫描：** `sweepOrphanRows`（第 125 行）检测 DB 行→存储缺失 + `sweepOrphanBlobs`（第 164 行）检测存储 blob→DB 行缺失。通过 `RECONCILE_DELETE_ORPHAN_BLOBS`（默认 false → safe default）门控。代码注释第 1-12 行明确说明双向检测设计 |
| `internal/reconcile/job.go:scanAll` | `reconcile/job.go` | ❌ **代码锚点不存在** | 函数名实际为 `sweep()`（第 90 行），非 `scanAll` |

#### 关键架构发现

**现有保护已被严重低估。** 实际 Reconcile 实现：

```
sweep() 每 RECONCILE_INTERVAL_MINUTES 运行一次：
├── sweepOrphanRows() → ListObjects → store.Stat → SoftDeleteObject
│   （检测 DB 存在但存储缺失的幽灵行 → 软删除）
└── sweepOrphanBlobs() → collectReferencedKeys → List → findStaleBlobs → Delete
    （检测存储存在但 DB 无引用的孤儿 blob → 可选删除）
```

**真正的缺口（缩小后）：**

| 缺口 | 严重度 | 细节 |
|------|--------|------|
| 写入路径无即时补偿 | **P2** | `writePutObject` 失败后，blob 需等下一次 Reconcile sweep（最多 `RECONCILE_INTERVAL_MINUTES` 分钟）才被清理。在此期间占用空间、计入配额，且 SLI 显示 `storage_put` 成功但用户看到 500 |
| 删除路径无即时补偿 | **P2** | `repo.HardDeleteObject` 失败后，一致性问题将持续到下一次扫描 |
| Reconcile 默认 safety 倾向 | **P3** | `RECONCILE_DELETE_ORPHAN_BLOBS` 默认 `false` — 生产环境中需显式开启才自动清理孤儿 |
| 补偿 Delete 在 MD5 验证失败路径上已实现 | **✅ 部分覆盖** | `Put` 第 119-120 行：`if err := verifyMD5(); err != nil { s.store.Delete(ctx, sk); return ... }` — MD5 不匹配时确实有补偿。但 `writePutObject` 失败路径无补偿 |

#### 修正后的建议

1. **写入后补偿（P2 而非 P1）：** 在 `writePutObject` 失败路径增加 `s.store.Delete(ctx, sk)` —— 低复杂度、低风险。**这是真正该做但尚未做的事。**
2. **删除后补偿（P2）：** 在 `repo.HardDeleteObject` 失败路径做反向补偿 —— 将 blob 写回（或记录到专门的 repair 表）。
3. **Reconcile 默认开启（P3）：** 考虑将 `RECONCILE_DELETE_ORPHAN_BLOBS` 默认改为 `true`（配合 `ORPHAN_GRACE_MINUTES` 默认 30 分钟作为安全网）。
4. **度量（P3）：** 现有 `telemetry.RecordReconcileBlobs(ctx, orphanBlobs, deletedBlobs)` 已存在，但需要 Prometheus 仪表盘暴露。

#### 边界情况再验证

| B# | 场景 | 分析文档结论 | 实际状态 |
|----|------|------------|---------|
| B1 | 补偿 Delete 也失败 | 等待定时 GC | ✅ 正确 — Reconcile sweep 兜底（若开启 deleteOrphanBlobs） |
| B2 | 孤儿属于版本控制桶 | @v\<id\> 后缀可追踪 | ✅ 正确 |
| B3 | 存储不支持跨租户 List | 按 bucket prefix 枚举 | ✅ 正确 — 现有 `sweepOrphanBlobs` 按 `tenant/` prefix 扫描 |
| B4 | 并发写入补偿误删 | @v\<id\> 后缀防误伤 | ✅ 正确 — 版本化 key 天然安全 |
| B5 | 外部工具直写 blob | 下一轮被当孤儿清理 | ✅ 正确 — 这正是设计的预期行为 |

---

### ✅ 方向二：搜索结果缓存无事件驱动失效 — **事实准确，验证通过**

#### 事实核查结果

| 代码锚点 | 行号 | 状态 | 验证详情 |
|---------|------|------|---------|
| `resultCache.get` 仅检查 TTL | `result_cache.go:56-65` | ✅ 确认 | `time.Now().After(e.expiry)` — 唯一的过期检测，零事件驱动失效 |
| `WithResultCache` 不关联事件总线 | `search.go:59-64` | ✅ 确认 | 仅设置 TTL+capacity，不接收 EventBus 参数 |
| EventBus 发射 EventCreated/EventDeleted | `bus.go:67-74`, `file_crud.go` Put/Delete | ✅ 确认 | `Publish` + `broadcast` 工作机制完整 |
| Indexer 处理事件不通知缓存 | `indexer.go:80-85` | ✅ 确认 | 消费事件用于索引更新，不涉及 resultCache |
| 测试零事件驱动覆盖 | `result_cache_test.go` | ✅ 确认 | 仅覆盖 TTL 过期 + 并发安全 |

#### 补充技术细节

现有代码中的 `resultCache` 实际上有 `cache map[string]resultEntry` 按查询 key 索引。失效策略可以非常精准：

```go
// 当前 cache key 格式：
"<tenant>\x1f<bucket>\x1f<mode>\x1f<k>\x1f<query>"
```

**失效映射关系缺失：** 给定一个 `(tenant, bucket, key)` 的对象变更，需要找到**哪些缓存条目包含了该对象的 chunk**。这不是直接的 key→entry 映射，因为 cache key 包含 query 字符串而非对象 key。

**推荐的精确失效方案是建立反向索引：** `map[string]map[string]struct{}` — 对象 key → 包含该对象的 cache key 集合。当对象变更时，根据反向索引找到所有受影响的缓存条目并逐条失效。

这与分析文档的推荐方案不冲突，但补充了一个关键技术细节：**没有反向索引的情况下，只能做粗粒度失效（TTL 到期）或全量清空。**

---

### ✅ 方向三：复制工作者无进度跟踪与滞后监控 — **事实准确，验证通过**

#### 事实核查结果

| 代码锚点 | 行号 | 状态 | 验证详情 |
|---------|------|------|---------|
| `Worker.Run` 无 offset 追踪 | `replication.go:69-93` | ✅ 确认 | `for { select { case e := <-sub: queue.Enqueue } }` — 消费后即忘 |
| `ReplicateObjectByID` 不写 watermark | `replication.go:97-126` | ✅ 确认 | 成功/失败仅 log，不写任何进度状态 |
| `telemetry/metrics.go` 零复制指标 | `metrics.go` | ✅ 确认 | 15+ 指标中无 replication 相关 |
| 无 `replication_watermarks` 表或接口 | `repository/repository.go` | ✅ 确认 | 仓库层无复制进度接口 |

#### 补充架构分析

**当前复制路径的数据丢失风险链：**

```
EventBus.Publish (持久化到 object_events) 
  → broadcast (内存 channel)
    → Worker.sub (通道)
      → if not EventCreated → continue (静默丢弃！)
      → Enqueue (可能失败)
```

其中 `Worker.Run` 第 76-77 行：
```go
if e.Type != repository.EventCreated || e.ObjectID == nil {
    continue  // ← EventDeleted 事件被静默忽略！
}
```

分析文档未覆盖但重要的一点：**删除事件的复制丢失**。当前 Worker 只响应 `EventCreated`，因此：
- 主区域的硬删除不会复制到副本 → 副本的已删除 blob 永不被删除
- 如果桶在主区域被删除，副本不会收到通知
- 灾备切换时，副本包含主区域已删除的对象 → 灾难后恢复的数据超出预期（数据残留而非数据丢失）

这对灾备场景有实质性影响，建议扩展到 `EventDeleted` + `EventAccessed`（用于访问模式复制）的考虑。

---

### ✅ 方向四：速率限制器每进程独立 — **事实准确，验证通过**

#### 事实核查结果

| 代码锚点 | 行号 | 状态 | 验证详情 |
|---------|------|------|---------|
| `tokens`/`lastRefill` 是内存字段 | `ratelimit.go:31-37` | ✅ 确认 | `mu sync.Mutex` + `buckets map[string]*bucket` — 纯进程内 |
| `rl.Allow()` 检查本地桶 | `ratelimit.go:88-` | ✅ 确认 | 无跨副本查询逻辑 |
| `main.go` 每副本独立创建 | `main.go:157-160` | ✅ 确认 | `rl := middleware.NewRateLimiter(cfg.RateLimit.RPS, cfg.RateLimit.Burst)` |
| `config.go` 无分布式限流配置 | `config.go:RateLimitCfg` | ✅ 确认 | 仅有 `RPS`/`Burst`/`AIRPS`/`AIBurst` |

#### 补充技术细节

分析文档明确提到 `rlMaxBuckets = 50_000` 的桶数量上限用于防止恶意 tenant header 导致的 memory DoS。这是分析文档中提到的设计细节，确认存在。

**部署拓扑影响矩阵（补充分析文档）：**

| 部署拓扑 | 限流效果 | 分析 |
|---------|---------|------|
| 单副本 | ✅ 100% 有效 | 当前设计完全适配 |
| 2 副本 + K8s Service（加权轮询） | ⚠️ 约 2× 预期 | 请求均匀分布时每副本获得 ~50% 流量，限流约 2× 配置 |
| N 副本 + 无状态水平扩缩 | ❌ N× 预期 | 扩容直接放大限流天花板 |
| 多区域（global deployment） | ❌ 完全失效 | 每区域独立进程，没有全局共识 |

**方案 C（混合模式）的一个关键陷阱：** 本地桶定期同步从中央存储同步剩余容量，如果同步间隔内本地桶消耗完额度，后续请求会被拒绝——即使在中央桶中仍有额度。这会导致限流变得不精确且更保守，而非分析文档描述的"宽松模式"。

---

### ✅ 方向五：缺少请求体大小准入控制 — **事实准确，验证通过**

#### 事实核查结果

| 代码锚点 | 行号 | 状态 | 验证详情 |
|---------|------|------|---------|
| `config.go` 无 MaxRequestBodySize | `config.go:33-40` | ✅ 确认 | `AppConfig` 有 6 个字段，零个 body-size 相关 |
| REST `Put` 直传 `r.Body` | `handler.go:42-56` | ✅ 确认 | `h.svc.Put(r.Context(), ..., r.Body, size, ...)` |
| S3 `PutObject` 直传 `r.Body` | `s3compat/handler.go:69-` | ✅ 确认 | 同上模式，无 `MaxBytesReader` |
| S3 `uploadPart` 直传 | `s3compat/handler.go` → `extra.go` | ✅ 确认 | `h.svc.UploadPart(..., r.Body, ...)` |
| MCP `write_file` 无大小上限 | `mcp/server.go:289-304` | ✅ 确认 | `strings.NewReader(content)` 无限检查 |
| `file_multipart.UploadPart` 无 `maxPartSize` | `file_multipart.go:60` | ✅ 确认 | `size int64` 参数直传 `store.Put` |

#### 补充细节

**非 Object 端点的 JSON 解析暴露：** 分析文档列出的搜索/聊天端点（`search.go:57` 等）使用 `json.NewDecoder(r.Body).Decode(&req)` 无大小限制。Go 的 `json.Decoder` 默认不限制输入大小——一个 200MB JSON 请求会由 `Decode` 完整分配内存。这些端点也需要添加 `http.MaxBytesReader` 或 `json.Decoder.DisallowUnknownFields()` 结合 `Decoder.UseNumber()` 的控制。

**现有部分保护（分析文档已正确提及）：** S3 bucket 子资源配置端点（`bucketConfigBodyLimit = 64KB`）是唯一受到限制的端点。这证实了分析文档的核心论点——只有非对象端点有大小控制，对象端点全部裸奔。

**S3 标准合规细节：** AWS S3 的 PutObject 上限是 5GB（单次），UploadPart 上限也是 5GB。如果 AeroVault 要完全兼容 S3，除了防止 OOM 外还需要实现这些标准上限的返回语义（`EntityTooLarge` 错误码）。

---

## 逐方向综合评分

| 方向 | 事实准确度 | 代码锚点准确度 | 边界场景覆盖 | 建议质量 | 整体评价 |
|------|-----------|---------------|------------|---------|---------|
| **一：写入路径孤儿与幽灵** | ⚠️ 65% | ❌ 60% | ✅ 90% | ✅ 80% | 核心论点（无事务边界）有效，但严重低估了 Reconcile 已实现的孤儿检测机制。`scanAll` 锚点不存在。P1→**降级为 P2** |
| **二：搜索缓存失效** | ✅ 100% | ✅ 100% | ✅ 90% | ✅ 85% | 准确。建议增加反向索引的技术细节后可推进实施 |
| **三：复制进度跟踪** | ✅ 100% | ✅ 100% | ✅ 80% | ✅ 85% | 准确。额外发现 `EventDeleted` 未被复制是分析文档遗漏的 |
| **四：分布式限流** | ✅ 100% | ✅ 100% | ✅ 85% | ✅ 88% | 准确。混合模式的精度陷阱是值得深入的一点 |
| **五：请求体准入** | ✅ 100% | ✅ 100% | ✅ 95% | ✅ 90% | 最准确、最高质量的方向。全部代码锚点精确匹配 |

## 错误和遗漏汇总

| # | 位置 | 类型 | 错误/遗漏描述 | 修正 |
|---|------|------|-------------|------|
| **E1** | 方向一 · 核心声明 | ❌ 严重事实错误 | "Reconcile 仅处理版本保留与软删除清除，不处理写入路径故障残留" | Reconcile `sweep()` 已实现 `sweepOrphanRows` + `sweepOrphanBlobs` 双向孤儿检测 |
| **E2** | 方向一 · 代码锚点 | ❌ 锚点不存在 | `internal/reconcile/job.go:scanAll` — 无此函数 | 应为 `sweep()`（第 90 行） |
| **E3** | 方向一 · 既有覆盖声明 | ⚠️ 不完整 | 声称"零实质性架构分析"——但 Reconcile orphan 代码正是针对此问题的实现 | 既有分析可能已足够，需重新审查 |
| **E4** | 方向一 · 优先级 | ⚠️ 高估 | P1——然而周期性 Reconcile sweep（已实现）已提供保护 | 应降为 P2（即时补偿缺失但定时检测存在） |
| **E5** | 方向三 · 遗漏场景 | ⚠️ 不完整 | 仅讨论 `EventCreated` 的复制，未分析 `EventDeleted` 未被 Worker 处理 | Worker 第 76-77 行过滤了非 `EventCreated` 事件 → 删除不复制 |
| **E6** | 方向三 · Watermark 方案遗漏 | ⚠️ 不完整 | 建议的 watermark 表未考虑 `object_events` 已存在的 `consumed_by` 标记 | 可在现有 `object_events` 表增加 `replicated` 布尔列，而非新建表 |
| **E7** | 方向五 · JSON 端点遗漏 | ⚠️ 不完整 | 未讨论搜索/聊天端点 JSON decoder 的内存分配风险 | `json.Decoder.Decode` 无大小限制，需额外保护 |

## 修正后的优先级排序

| 优先级 | 方向 | 核心风险 | 影响面 | 实施复杂度 |
|--------|------|---------|-------|-----------|
| **P1** | **五：请求体大小准入控制** | OOM / disk full / 放大攻击 | 全部 4 协议 + MCP + JSON API | 低 |
| **P1** | **四：分布式限流** | 多副本限流完全失效 | 全部请求路径 | 中-高 |
| **P2** | **一：存储-元数据写入孤儿（即时补偿缺失）** | 故障间隔内的数据残骸 / 幽灵对象 | 全部写入路径 | **低**（仅加 2 行补偿 delete） |
| **P2** | **二：搜索缓存无事件驱动失效** | 检索结果陈旧 | AI 搜索路径 | 中（需反向索引） |
| **P2** | **三：复制无进度跟踪** | 复制健康不可观测 | 多区域部署 | 中 |
| **P3** | **一：Reconcile 默认开启 Orphan 删除** | 孤儿 blob 仅在手动开启后才清理 | 存储层 | 极低（配置默认值） |

**关键变化：** 方向一从 P1 降为 P2 的核心理由是——已有 Reconcile sweep 提供的定时保护。真正缺失的是即时补偿（2 行代码），而非分析文档声称的"零保护"。

## 推荐执行顺序

```
立即实施（1-2 天）：
└── 方向五 → 方向一的即时补偿（2 行代码）

短期实施（1-2 sprint）：
├── 方向四（分布式限流方案选型 + 实现）
├── 方向二（resultCache 反向索引 + Invalidate 方法）
└── 方向三（Watermark 表 + Metrics + EventDeleted 复制）

中期优化（技术债务）：
└── 方向一的 Reconcile 默认开启 Orphan 删除（低风险）
```
