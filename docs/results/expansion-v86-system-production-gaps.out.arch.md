现在我已经完成了对代码库的全面审查，并在源代码层面验证了验证报告的声明。下面是我的架构分析。

---

# 架构分析：第 86 轮生产级坚固性盲区报告

## 1. 架构评估

### 1.1 当前架构的优势

**分层正交性正是成熟生产系统的标志。** 验证报告中的事实核查验证了我在代码中观察到的情况——`FileService` 作为四个协议之间的解耦层，`Storage` 和 `Repository` 作为可插拔抽象，以及 `EventBus` 驱动解耦的工作流——所有这些都保持了严格的分层边界。除非我读取实际代码，否则我不会相信这一点的严格程度，所以我要赞扬已经落实的设计。

值得强调的具体优势：

| 优势 | 实现细节 | 重要性 |
|----------|-------------|-------------|
| **事件驱动异常处理** | `reconcile.Job` 已经是双向的——`sweepOrphanRows` 和 `sweepOrphanBlobs`。这是针对报告 #1 核心论点的实际补偿机制，该论点认为“零保护”存在。 | 关键——这是为什么 #1 从 P1 降为 P2 的原因。 |
| **Opt-in 安全默认** | `RECONCILE_DELETE_ORPHAN_BLOBS` 默认 `false`，`AI_INDEX_ENABLED` 默认 `false`，`AUTH_PERSIST_KEYS` 默认 `false`。最安全的基线路径。 | 高——破坏性操作要求在投入生产前进行明确的运维选择。 |
| **可插拔抽象** | `Storage` 接口有 4 个后端。`Repository` 有 2 个变体。`VectorIndex` 现在是 `Search` 中的一个 seam。每个 seam 都是一个扩展点，而不是一个分支。 | 高——在集成点进行抽象，而不是在业务逻辑中进行。 |
| **集群单例门控** | `cluster.Singleton` + `leases` 表防止重复的破坏性扫描。IDempotency 键防止重复写入。 | 高——正确的基础分布式原语。 |
| **可观测基线** | 15 个 OTel instruments、Grafana 仪表盘、Prometheus 告警规则。每个指标的语义都得到了记录和理解。 | 中——覆盖率是正确的；差距在于域可见性。 |

### 1.2 核心局限性

**层级独立性掩盖了一个关键事实：存储和元数据层之间缺失事务边界是架构性的，而不仅仅是实现细节。** 栈上的顺序调用（`store.Put` 在 `writePutObject` 之前）意味着系统*继承*了分布式系统的最后写入者胜出语义，即使对于单节点部署也是如此。这一限制无法通过重构消除——需要一个新的抽象。

### 1.3 架构债务

| # | 债务 | 位置 | 影响 | 评估 |
|---|------|----------|----------|--------|
| D1 | **每个副本的内存限流状态** | `middleware/ratelimit.go` | 多副本部署中限流因子是 N× | 架构性——需要新的分布式原语 |
| D2 | **内存结果缓存，仅 TTL 失效** | `ai/result_cache.go` | 查询缓存可能最多过时 `AI_SEARCH_CACHE_TTL_SECONDS` | 设计性——需要反向索引 |
| D3 | **复制仅 EventCreated** | `replication/replication.go:76-77` | 删除事件永不被复制——灾备副本存在数据残留 | 功能性——修复成本低 |
| D4 | **JSON 解码器无大小限制** | REST & MCP 处理程序 | OOM 攻击面，已被分析发现 | 安全性——修复成本低 |
| D5 | **`request.go` ~425 行**（AI 相关） | `internal/ai/request.go` | 接近 500 行限制，可能已超过 | 结构性问题——需要拆分 |

---

## 2. 架构扩展方向

### 方向 A：分布式断路器与批量头（P0）

**业务价值：** 生产级弹性是 SaaS 产品的准入要求。没有它，后端退化会级联为全站停机。

**挑战：**
- 存储客户端目前使用 `http.DefaultClient`（无超时）——根据 `.env.example` 添加了 `STORAGE_*_TIMEOUT` 配置，但中间件层本身缺失。
- 现有配置已经暴露了 `STORAGE_CB_ENABLED`、`STORAGE_CB_FAILURE_THRESHOLD`、`STORAGE_CB_RECOVERY_TIMEOUT`——这是基础设施的*骨架正在等待实现*。

**架构变更：**
```
当前：handler → FileService → store.Put (无超时，无断路器)

变更后：handler → [ConcurrencyLimiter] → FileService → [CircuitBreaker] → store.Put
                                                    → store.Get
                                                    → store.Delete
```

断路器抽象必须满足以下条件：
- **配置开放后**：`N`% 错误 → 开路 → `M` 秒半开
- **存储后端级**：S3 后端出现故障不会影响本地存储操作
- **回退语义**：开路 = `ErrBackendUnavailable`，不是 panic

**对现有系统的影响：** 最小化。`storage.Storage` 接口保持不变；断路器作为 `FileService` 内部的装饰器应用于存储调用。现有的 `config.go` 已经解析了断路器参数——需要的是实现，而不是新的配置。

---

### 方向 B：复制为通用变更数据捕获管道（P1）

**业务价值：** 仅 `EventCreated` 的复制意味着删改操作（覆盖、删除、生命周期转换）的灾备缺失。完整的 CDC 管道将复制从架构限制转变为平台特性。

**挑战：**
- 当前 `Worker.Run` 过滤严格的 `EventCreated`
- 缺失 `EventDeleted` 处理——软删除和硬删除事件都不会触发复制
- `replicate` 作业处理程序需要处理三种操作：Put（创建）、Delete（移除）、TagUpdate（元数据同步）

**架构变更：**

```
EventBus → ReplicationWorker
    ├── EventCreated  →  JobReplicate (现有)
    ├── EventDeleted  →  JobReplicateDelete (新增)
    └── EventAccessed →  (可选) 访问模式复制
```

删除复制需要特别注意顺序：如果删除作业在主区域之前到达副本区域，副本可能会删除尚未创建的对象。幂等 Put（现有）可以解决这个问题，但需要理解这种排序。

**对现有系统的影响：** 中等。`replication.go` 需要一个新的作业类型 `JobReplicateDelete`，一个新的 `ReplicateDeleteByID` 处理程序，以及 `replication.Worker` 中的事件分派器。模式是相同的——只需为缺失的事件分支分支。幂等性 by design（删除不存在的 key 是安全的）。

---

### 方向 C：搜索缓存失效协议（P1）

**业务价值：** 缓存提升 P95 搜索延迟，但过时数据破坏搜索质量。失效协议在延迟和新鲜度之间取得平衡。

**挑战：**
- 当前缓存键格式：`"<tenant>\x1f<bucket>\x1f<mode>\x1f<k>\x1f<query>"`——没有映射回源对象 key
- 针对对象 key 的失效需要反向索引：`map[tenant+bucket+key] → set[cacheKeys]`
- 不能只清空——必须足够精确以保留高命中率

**架构变更：**

```
EventBus → resultCache.Invalidate(objectKey)
    └── 查找反向索引 → 从缓存映射中删除特定 cacheKey
```

反向索引的实现必须注意以下事项：
1. **内存上限**：每个条目大约 ~64 字节。对于 100K 个缓存条目 × 平均每个 3 个对象 = 300K 个反向映射 → 大约 20 MB。可以接受。
2. **逐出一致性**：当主缓存条目因容量而被逐出时，反向索引也必须相应地更新。
3. **租户隔离**：失效必须限定于租户——一个租户的工作负载不应影响另一个租户的缓存。

**对现有系统的影响：** 低到中。`resultCache` 是 `search.go` 中的可选包装器。失效监听器可以直接连接在 `ai` 包中。没有接口需要更改。

---

### 方向 D：分布式速率限制（P1 – P2）

**业务价值：** 没有分布式限流，扩展到 N 个副本会使限流天花板变为 N× 倍配置值。对于多副本部署，这是安全性的硬性失败。

**三个竞争方案：**

| 方案 | 精度 | 一致性 | 延迟影响 | 实施复杂度 |
|----------|-----------|-------------|-----------------|----------------------|
| **A：集中式**（Redis 滑动窗口） | 像素级 | 强 | 每个请求 +~1ms | 中 |
| **B：同步本地**（原点广播剩余容量） | 好 | 最终 | 每间隔 +~0 | 高 |
| **C：混合**（本地桶，定期与中央同步） | 近似到良好 | 最终 | 每间隔 +~0 | 中 |

**架构建议：方案 C 具有位置感知启发式。**

验证报告正确地识别了方案 C 在同步间隔内过度消耗的问题。一个有效的改进是：
- 每个副本声明其自己的桶，初始容量为 `burst / N`
- 每秒一次，副本将其剩余容量同步到中央存储
- 同步*之前*的本地决策根据*上次已知的全局剩余容量 + 本地耗尽率启发式*进行

这消除了报告警告的“精确性陷阱”，同时避免了每个请求的集中式查找。

**对现有系统的影响：** 中等。速率限制器接口不需要更改（`Allow(tenant) → (bool, wait)`）。实现从 `map[string]*bucket` 变为中央存储感知的包装器。现有的 `rlMaxBuckets` 和 `rlIdleTTL` 逻辑得以保留。

---

### 方向 E：元数据对象协调的 SAGA 原语（P2）

**业务价值：** `store.Put` 与 `writePutObject` 之间的即时补偿缺失是验证报告 #1 的正确核心论点。然而，解决方案不仅仅是在失败路径上添加 `store.Delete`——它需要一个可重试、幂等的补偿原语。

**挑战：**
- `writePutObject` 可能因瞬态 DB 错误、约束冲突或并发问题而失败
- 在失败路径上简单添加 `store.Delete` 会在 `Delete` 本身失败时产生双重错误处理责任
- 目标：补偿应*至少执行一次*，并且*幂等*

**架构变更：**

```
FileService.Put:
  ├── 1. store.Put(ctx, sk, reader, ...)         // 外部资源
  ├── 2. repo.UpsertObject / InsertObjectVersion   // 内部资源
  ├── 3. ✅ 成功：event.Publish
  └── ❌ 步骤 2 失败：
        ├── 尝试即时补偿（store.Delete）
        └── 如果补偿失败 → 写入 orphan_blobs 修复表（而非只是日志）
```

这个“修复表”模式——写入一个专门的 `repair_actions` 行，其 `action="delete_blob"` 和 `storage_key=<key>`——比日志记录更可靠：

```go
// 当前：第 141-143 行
s.logger.Error("repo write failed; storage object orphaned", ...)
return repository.Object{}, fmt.Errorf("repo write: %w", err)

// 建议：
s.logger.Error("repo write failed", ...)
if err2 := s.store.Delete(ctx, sk); err2 != nil {
    // 即时补偿失败——将修复排入队列而不是放弃
    s.repo.EnqueueRepairAction(ctx, repository.RepairAction{
        Action:     "delete_blob",
        StorageKey: sk,
        TenantID:   tenant,
    })
}
return repository.Object{}, fmt.Errorf("repo write: %w", err)
```

修复表由 Reconcile 处理（重用现有的 `sweep()` 循环）。这使得补偿在进程崩溃后也能持续存在。

**对现有系统的影响：** 低。新的 `EnqueueRepairAction` / `ConsumeRepairActions` 仓库方法，加上 Reconcile 中的一个新步骤。`FileService.Put` 中总共大约 10 行新代码。

---

## 3. 接口设计原则

### 3.1 `Storage.Storage` 和 `Repository.Repository` 不应更改

这两个接口是架构的基石。验证报告证实它们是正确的：`Storage` 很薄（~8 个方法）、后端不可知，并且有 4 个经过生产验证的实现。`Repository` 更广泛，但通过 `sql.go` 中的共享 SQL 核心实现了两个变体（SQLite + Postgres）。

**我们添加新抽象，但不修改这些抽象。**

### 3.2 新接口：`RepairStore`

协调补偿需要一个可靠的持久化位置来记录“删除该存储 blob”的操作。我建议：

```go
// RepairAction 记录针对存储后端的确认失败操作。
// 它由写入路径生成，由 Reconcile 消费。
type RepairAction struct {
    ID         int64     // 自动递增
    TenantID   string    // 租户范围
    Action     string    // "delete_blob" | "rewrap_sse"
    StorageKey string    // 受影响的 key
    ObjectKey  string    // 用户可见的 key（日志记录用）
    CreatedAt  time.Time // 为了优雅而延迟
    ConsumedAt *time.Time // 处理时不为 nil
}
```

这不要求 Repository 接口更改——它是一个针对特定关注点的新表和方法。

### 3.3 新接口：`CacheInvalidator`

搜索缓存在对象层级失效，而不是查询层级失效：

```go
// 内部于 ai 包中
type CacheInvalidator interface {
    // Invalidate 使所有引用给定对象 key 的缓存条目无效
    Invalidate(ctx context.Context, tenant, bucket, key string)
}
```

这与 EventBus 的订阅者相匹配，无需搜索知道事件的存在。

### 3.4 向后兼容性

所有提议的扩展都是可选的或默认关闭的：

- **断路器**：默认关闭（`STORAGE_CB_ENABLED=false` + 阈值 = 0）
- **CDC 复制**：`REPLICATION_ENABLED` 已经是 opt-in；默认保持不变
- **缓存失效**：结果缓存默认为 nil（`AI_SEARCH_CACHE_SIZE=0`）；失效是一个次要特性
- **分布式限流**：一个新的配置标志 `RATELIMIT_MODE=local|redis|hybrid` 默认为 `local`
- **SAGA 修复表**：默认打开但非侵入性——仅当写入路径失败时写入

---

## 4. 技术选型

### 4.1 需要 vs. 不需要

| 能力 | 需要新依赖性？ | 评估 |
|-----------|-------------------|--------|
| 存储断路器 | **否**——`gobreaker` 存在，但 stdlib `net/http` 超时 + `sync.RWMutex` 基于状态的实现就足够了 | 在 Go 中实现断路器很简单。引入第三方断路器会增加传递的风险，而几乎没有收益。 |
| 分布式限流 | **暂定**——方案 C（混合）仅需要中央键/值存储 | 如果部署已经使用 Redis 进行缓存，那么方案 C 中的中央存储可以是 Redis（SETNX + TTL）。如果未使用 Redis，pg_advisory_lock 或带自旋的 Postgres 行是一个纯粹的 SQL 替代方案。 |
| 搜索缓存反向索引 | **否**——纯 Go `map[string]map[string]struct{}` | 内存实现。零依赖性。 |
| CDC 复制 | **否**——重用现有的 JobQueue | 仅新事件类型和处理程序。使用现有的基础设施。 |
| 请求体大小限制 | **否**——`http.MaxBytesReader` 是 stdlib | 任何端点约 5 行代码。零足迹。 |
| JSON 解码器保护 | **否**——`json.Decoder` + `io.LimitReader` 是 stdlib | 同上。 |

### 4.2 自建与外部采购决策

**自建案例：** 断路器、缓存失效、请求大小限制。这些是*编排模式*，而不是*业务逻辑*。它们不需要专门的库——它们需要在 *现有* 抽象周围放置正确的包装器。每个自建组件最终不超过 200 行 Go 代码。

**采购案例：** 分布式限流需要一个中央协调器。Redis 被广泛使用、经过实战检验并且已经部署在大多数目标环境中。为限流采购 Redis 而不是自建共识层（Raft/pg_advisory_lock）是正确的选择，原因如下：

| 因素 | 自建 (Postgres 共识) | 采购 (Redis) |
|-------|------------------------|----------------|
| 每个请求延迟 | ~1-3ms（Postgres 往返） | ~0.5-1ms（内存） |
| 连接开销 | 添加到 Postgres 连接池 | 专用连接，低开销 |
| 运维负担 | 无（重用现有 Postgres） | 新服务来管理 |
| 部署复杂度 | 简单（相同的 Postgres） | 中等（新的基础设施依赖） |
| 优雅降级 | 如果 Postgres 宕机，直接回退到本地限流 | 如果 Redis 宕机，回退到本地限制 |

**建议：** 从方案 C 的 Postgres 原生限流开始（使用 `SELECT ... FOR UPDATE NOWAIT` 每间隔轮换桶状态）。如果精度不够，再引入 Redis。这避免了提前引入基础设施依赖。

---

## 5. 实施路线图

### 第一阶段：高影响、低代码变更（1-2 天）

| # | 方向 | 代码变更 | 风险 | 可验证性 |
|---|----------|---------------|------|-----------------|
| 1 | **请求体大小准入控制**（全部 4 个协议 + MCP + JSON 端点） | 每个处理程序约 3 行：`r.Body = http.MaxBytesReader(w, r.Body, limit)` | 极低 | 直接测试：发送大请求 → 预期 413 |
| 2 | **JSON 解码器限制**（搜索、聊天、代理端点） | 每个解码器约 2 行：`json.NewDecoder(io.LimitReader(r.Body, maxJSONSize))` | 极低 | 发送大 JSON → 预期 413 |
| 3 | **写入路径即时补偿**（`file_crud.go` 第 141-143 行） | 将 `s.logger.Error` → `s.store.Delete(ctx, sk)` + 可选的 repair 表写入 | 低——幂等：删除不存在的 key 是安全的 | 模拟 `writePutObject` 失败；验证存储 key 被删除 |
| 4 | **删除路径即时补偿**（`file_crud.go` 硬删除路径） | `repo.HardDeleteObject` 失败 → 记录 repair 操作 | 低——非阻塞 | 模拟仓库失败；验证 repair 行已创建 |
| 5 | **复制 EventDeleted 处理** | `replication.go` 第 76-77 行：添加 `EventDeleted` → `JobReplicateDelete` | 低——幂等：删除不存在的 key 是安全的 | 创建然后删除对象；验证副本上也执行了删除 |

### 第二阶段：缓存与可观测性（1-2 个 Sprint）

| # | 方向 | 核心挑战 | 验证 |
|---|----------|----------------|------------|
| 6 | 结果缓存反向索引 | 对象 key 到 cache key 的正确映射 | 索引内容验证：In"validate → get → miss |
| 7 | EventBus 驱动的缓存失效 | 将 `CacheInvalidator` 连接到事件总线 | 集成：发布事件 → 缓存失效 → 下一个查询是 cache miss |
| 8 | 复制延迟指标 | `replication_lag_seconds`：事件创建时间与复制完成时间 | Grafana 面板 + 告警规则 |
| 9 | 复制水印表 | `object_events.consumed_by` 或新表 | 查询：获取每个副本的最后水印 |

### 第三阶段：弹性（第 3-4 个 Sprint）

| # | 方向 | 风险 | 缓解措施 |
|---|----------|------|---------------|
| 10 | 存储断路器 | 打开状态误报导致不必要的降级 | 保守阈值（`CBFailureThreshold=10`，`CBRecoveryTimeout=30s`），默认关闭 |
| 11 | 请求并发限制器 | 为死锁加权信号量设置正确的权重 | 从 GET=1、PUT/DELETE=2 开始；在基准测试下观察 |
| 12 | 分布式限流：方案 C 使用 Postgres | 中央存储的争用 | 使用 `UPDATE ... RETURNING tokens WHERE key = ? AND version = ?` 乐观锁定；回退到本地 |
| 13 | AI 降级模式 | 由于探测健康检查导致搜索失败 | `AI_DEGRADED_MODE=true` → 如果 `AI_EMBED_PROVIDER=http` 且无法访问，则发回 503 |

### 第四阶段：硬化和默认值调整（Sprint 5+）

| # | 方向 | 更改 | 验证 |
|---|----------|----------|------------|
| 14 | `RECONCILE_DELETE_ORPHAN_BLOBS` 默认 `true` | 将默认值从 `false` 更改为 `true`，附带 30 分钟优雅期限 | 覆盖生产前的冒烟测试 |
| 15 | 元数据协调 SAGA 原语（修复表） | New `repair_actions` 表 + Reconcile 中的消费循环 | 强制写入路径失败 → 验证 repair 行 → 验证 Reconcile 处理它 |
| 16 | 副本完整性检查：ETag 验证在读取时 | 当前存在的 `ETagVerifier`（`file_crud.go`）——需要在配置中默认打开 | 损坏的对象 → 读取时产生 `ErrObjectCorrupt` |

### 风险登记表和缓解措施

| 风险 | 可能性 | 影响 | 缓解措施 |
|----------|------------|----------|----------------|
| 断路器误报导致不必要的降级 | 中等 | 高 | 默认关闭；针对每个后端的行为进行测试；运维人员可以手动重置 |
| 分布式限流失效忽略中央状态 | 低 | 高 | 降级模式：如果中央存储不可用，回退到本地限流 + 告警 |
| 复制删除顺序：在创建之前到达删除 | 低 | 中等 | 幂等处理程序忽略对不存在 key 的删除 |
| 缓存反向索引内存泄漏 | 低 | 低 | 设置 `maxReverseEntries` 限制；使用 `sweep()` 定期清理悬空的条目 |
| `RECONCILE_DELETE_ORPHAN_BLOBS=true` 在生产中删除错误的 blob | 低 | 严重 | 30 分钟优雅期限是程序片状的；外部工具写入的 blob 在 30 分钟内不会被视为孤儿 |

---

## 最终评估

验证报告在事实层面上是合理的，但低估了关键的架构背景：

1. **报告 #1 的错误**不是小问题——它将缺失的即时补偿（真正的漏洞）与缺失的检测（已经实现）混为一谈。仔细阅读 `reconcile/job.go` 展示了一个双向设计，正确地处理了两个方向的孤儿。没有`scanAll` 函数——报告使用了错误的代码锚点——但 `sweep()` 架构是正确的。

2. **报告 #5（请求体大小）是应该已经存在的最明显、最高质量的安全发现。** 全部四个协议都没有 `http.MaxBytesReader`，MCP 中的 JSON 解码器没有大小限制。这是本分析中确定的唯一真正的 P0 发现。

3. **报告显示，该系统作为单节点部署，具有极高的生产就绪度。** 限流（无分布式）、事件（无跨副本广播）和缓存（无失效）的漏洞是扩展性问题，而不是稳健性问题。核心正确性路径——对象写入、版本控制、元数据存储、协调、压缩、加密——具有正确性界限，包括 MD5 验证失败后的补偿删除。

4. **路线图的明智性：** “立即实施”列——请求体大小 + 即时补偿——需要大约 50 行代码更改，并且没有架构风险。这是应该在所有其他架构工作之前发布的快速胜利。
