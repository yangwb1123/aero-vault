# 架构分析报告：AeroVault v140 高价值扩展方向

## 1. 架构评估

### 1.1 当前架构优势

| 维度 | 优势 | 评价 |
|------|------|------|
| **分层解耦** | 协议层(thin) → Service → Storage/Repository 三层清晰分离 | 各层职责明确，替换成本低 |
| **接口抽象** | `Storage` 和 `Repository` 两个核心接口定义了完整的 provider 契约 | 已支撑 4 种存储后端 + 2 种数据库 |
| **单一入口** | `FileService` 是所有协议和后台 worker 的唯一切入点 | 业务规则一次编写到处生效，杜绝协议行为差异 |
| **Opt-in 安全默认** | AI/pgvector/Qdrant/events/cluster 均 flag-gated | 基线路径零依赖，生产安全 |
| **事件驱动骨架** | EventBus + JobPool 的异步基础设施完整 | 易于扩展新的 subscriber |
| **迁移双轨制** | 结构化迁移系统支持 SQLite + Postgres 独立演进 | 兼容开发和生产两种部署形态 |

### 1.2 当前架构局限性

| 局限 | 具体表现 | 技术债等级 |
|------|---------|-----------|
| **Storage 接口缺少生命周期语义** | `Storage` 接口无 `TransitionStorageClass` / `RestoreFromGlacier` 等方法；`Copy` 语义也未标准化 storage_class 变更 | ⚠️ 中 — 阻碍方向一 |
| **事件总线无路由能力** | `Bus.Publish` 是同步广播，所有 subscriber 接收全部事件；`notification_rules` schema 已存在但零执行引擎 | ⚠️ 中 — 阻碍方向三 |
| **读路径零缓存** | 每次 GET/HEAD 直连底层存储，无内存缓存装饰器、无 HTTP 缓存头、无副本路由 | ⚠️ 中 — 阻碍方向四 |
| **查询能力单一** | `ListObjects` 仅支前缀匹配，tags/metadata/日期/大小等字段全部无法作为查询条件 | ⚠️ 低 — 阻碍方向二 |
| **快照仅限 SQLite+local** | `internal/snapshot` 对 Postgres DSN 返回空字符串，无法用于生产 | ⚠️ 低 — 阻碍方向五 |
| **BucketConfig 扁平化** | `BucketConfig.ExpireAfterDays` 是单一 int 字段，不支持多阶段 transition、不支持 noncurrent 版本策略、不支持 multipart 超时清理 | ⚠️ 低 — 方向一要求重构 |

### 1.3 关键设计决策合理性审查

| 决策 | 合理性 | 说明 |
|------|--------|------|
| `FileService` 作为唯一入口 | ✅ **优秀** | 没有设计替代方案的必要——这是整个架构最核心的正确决策。所有协议保持一致行为，无 bypass 风险。 |
| Storage 接口最小化 | ✅ 合理但有演进空间 | 当初的设计取舍正确(避免过度工程)，但现在 4 个后端已稳定，可以安全地增加新方法。新增方法时提供默认 fallback 实现即可保持向后兼容。 |
| EventBus 同步广播 | ⚠️ 当前合理，扩展需谨慎 | 小规模部署时足够。但方向三如果直接在 `Publish` 路径做规则匹配将增加 latency。**方案：将通知规则匹配移到异步 worker，`Publish` 只负责入 `events` 表。** |
| 元数据查询能力为前缀模式 | ⚠️ 合理的最简起步 | SQLite/Postgres 都可以通过 JSON 函数扩展查询能力而无需改 schema。核心风险是**无索引的大表全表扫描**——必须有 DBA 可干预的索引策略。 |
| 多租户 model (header-driven) | ✅ **成熟决策** | 与 S3 的 tenant 概念不同但更简洁。`*` operator key 的设计灵活且安全。 |
| Middleware 链顺序固定且 handler 不自挂链 | ✅ **强制正确性** | 防止了无数安全隐患。isolated handler test 无 tenant/auth 的设计是工程取舍而非 bug。 |

### 1.4 架构债务量化

```
Storage 接口缺失方法:    3 (TransitionStorageClass, RestoreFromGlacier, 或扩展 Copy 语义)
Repository 缺失查询能力: 5+ (tag/metadata/content_type/size/date 过滤 + sort)
EventBus 缺失路由能力:   1 (NotificationRouter)
Schema 存在但无消费者:    1 (notification_rules)
快照层生产覆盖度:        0% (仅 SQLite+local)
```

---

## 2. 扩展方向

### 方向一：存储生命周期分层引擎 (P1)

#### 为什么需要

**业务价值：**

- 直接降低用户存储成本 40–70%。当前所有对象永久保留在 `STANDARD` 类，对于日志/备份/审计数据这是巨大的浪费。
- S3 协议完备度的最后一个主要缺口。AWS S3 Lifecycle 是最常用的 bucket 配置之一，缺失 `Transition` 意味着 S3 兼容性停留在"基本对象操作"级别。
- 版本控制启用后非当前版本无限堆积——当前无任何策略控制。这不是成本问题而是合规风险。

**技术价值：**

- 验证 `Storage` 接口的扩展能力。如果在不破坏现有 4 个后端的情况下成功添加 `TransitionStorageClass`，证明接口设计弹性好。
- reconcile 引擎将从一个单一 job (`sweepExpired`) 扩展为通用调度框架，为后续 GC/retention 等功能提供架构基础。

#### 核心挑战与技术难点

| 挑战 | 难度 | 分析 |
|------|------|------|
| **冷存储不可读** | ⭐⭐⭐ | GLACIER/DEEP_ARCHIVE 类存储需要 restore 才能读取。当前 `Get` 路径必须检测 `storage_class` 并返回 `InvalidObjectState` 或自动触发 restore。后者增加了异步复杂度。 |
| **后端 Transition 语义差异大** | ⭐⭐⭐⭐ | Local: 重命名或复制到子目录；S3: `CopyObject` with `StorageClass` 参数；OSS/COS: 各自 API。抽象难度高于普通的 `Get`/`Put`。需要精细设计 `TransitionStorageClass` 接口参数。 |
| **事务一致性问题** | ⭐⭐⭐ | `repo.UpdateStorageClass` 和 `store.Transition` 是两阶段操作，无分布式事务。故障导致 metadata-storage_class 与实际存储类不一致。可接受的权衡——reconcile 是幂等的后台 job。 |
| **NoncurrentVersion 管理复杂度** | ⭐⭐⭐ | 非当前版本的 transition 和 expiration 需要考虑版本顺序、保留数量等复杂策略。S3 的 `NoncurrentVersionExpiration` 和 `NewerNoncurrentVersions` 组合逻辑需要仔细实现。 |
| **AbortIncompleteMultipartUpload** | ⭐⭐ | 多分片上传的残留数据清理需要与 multipart 状态机集成。S3 语义是 "upload 不存在"，CompleteMultipart 返回 NoSuchUpload。 |

#### 预期的架构变更

```
Storage 接口扩展:
  TransitionStorageClass(ctx, key, targetClass) (ObjectInfo, error)
  RestoreFromGlacier(ctx, key, days) error
  // 或扩展 Copy 语义:
  Copy(ctx, srcKey, dstKey, opts) (ObjectInfo, error)  // opts 含 StorageClass 字段

BucketConfig 重构:
  type LifecycleRule struct {
      ID                                     string
      Status                                 string // "Enabled" | "Disabled"
      Filter                                 *LifecycleFilter
      Expiration                             *LifecycleExpiration
      NoncurrentVersionExpiration            *LifecycleNoncurrentExpiration
      Transitions                            []LifecycleTransition
      NoncurrentVersionTransitions           []LifecycleNoncurrentTransition
      AbortIncompleteMultipartUpload         *LifecycleAbortMultipart
  }
  
reconcile/lifecycle.go 重构:
  sweep() → sweepExpired() + sweepTransitions() + sweepNoncurrent() + sweepAbortedMPU()

S3 XML 解析:
  bucketconfig.go: 完整解析 LifecycleConfiguration XML, 包括 Transition 的所有子元素
  S3 返回: 反向序列化 LifecycleRule → GetBucketLifecycleConfiguration XML
```

#### 对现有系统的影响

| 影响范围 | 程度 | 缓解措施 |
|---------|------|---------|
| `Storage` 接口 | **Breaking change** | 提供 `TransitionStorageClass` 的默认 stub 实现（返回 `ErrNotImplemented`），各后端逐步实现 |
| `BucketConfig` 结构体 | **Breaking change** | 向后兼容：保留 `ExpireAfterDays` 和 `ExpireAction` 字段，将新的 LifecycleRule 解析优先于旧字段 |
| reconcile 引擎 | 内部重构 | `sweepExpired` 逻辑不变，新增的 sweep 方法独立调度 |
| `Get` 路径 | 新增分支 | `storage_class == GLACIER` 时返回 `InvalidObjectState`。S3 协议已有此语义。 |
| 迁移文件 | 新增 0025 | `ALTER TABLE objects ADD COLUMN restore_status`（GLACIER restore 跟踪） |
| 复制/备份路径 | 无影响 | replication 和 snapshot 只需要同步 metadata 中的 storage_class，不需要理解其语义 |

#### 方案选择：A vs B

| 维度 | 方案 A (reconcile 扩展) | 方案 B (Job 队列) |
|------|------------------------|-------------------|
| **复杂度** | 中 | 中高（需定义新 job type + JobPool handler） |
| **重试粒** | 粗（下次 sweep 才重试） | 细（单对象级重试） |
| **可观察性** | 日志 + 指标 | 完整 job 生命周期追踪 |
| **实现速度** | 快（2 周） | 慢（3 周+） |
| **一致性** | 最终一致 | 最终一致（重试更快收敛） |

**建议：** 采用方案 A 快速交付核心功能，方向三完成后再将 lifecycle transition 迁移到 Job 队列（方案 B）。

---

### 方向二：对象元数据与标签查询引擎 (P1)

#### 为什么需要

**业务价值：**

- 这是最显著的"功能空白产品价值比"方向。数据模型完全就绪（`tags` 和 `metadata` 字段在 `Object` 结构体中完整存在），但查询能力为零。导致用户在 UI 上能看到 tags 却无法搜索。
- 填补 S3 能力之外的差异化增值特性。S3 不支持按 tag 过滤对象列表——这是 AeroVault 可以超越 S3 兼容性、形成自主产品优势的领域。
- 为 MCP tools、Web UI 搜索、Admin 审计等使用场景提供基础设施。

**技术价值：**

- 低风险快速交付（1-2 周），是验证"功能增量式迭代"流程的最佳候选。
- 为 Repository 层引入条件查询构建器——可复用于其他查询场景（如审计日志、事件查询）。

#### 核心挑战与技术难点

| 挑战 | 难度 | 分析 |
|------|------|------|
| **SQLite 与 Postgres JSON 函数差异** | ⭐⭐⭐ | SQLite: `json_extract(tags, '$.key')`；Postgres: `tags @> '{"key":"val"}'` 或 `tags->>'key'`。查询构建器需要双实现。 |
| **索引设计** | ⭐⭐⭐ | JSON 字段上的过滤性能严重依赖索引。SQLite 不支持 Postgres GIN 索引，只能用表达式索引。需要提供索引创建指南。 |
| **SQL 注入** | ⭐⭐⭐ | `tag.<k>` 中的 key 必须严格校验。任何用户输入拼接到 JSON path 字符串中都是注入风险。 |
| **分页一致性** | ⭐⭐ | 带过滤条件的查询在数据变更时的游标稳定性。marker 分页 + ORDER BY 在过滤条件下需要 `(sort_column, key)` 复合索引才能保证稳定。 |
| **多条件组合性能** | ⭐⭐⭐ | 5个以上过滤条件组合时，查询规划器可能选择非最优索引。需要在 Service 层实现查询预算（`maxQueryCost` 阈值）。 |

#### 预期的架构变更

```
Repository 接口扩展:
  ListObjectsFiltered(ctx, tenant, bucket, filter ObjectFilter) ([]Object, error)
  
  type ObjectFilter struct {
      Prefix        string
      Marker        string
      Limit         int
      Tags          map[string]string    // 精确匹配（AND）
      Metadata      map[string]string    // 精确匹配（AND）
      ContentType   string               // 精确匹配
      SizeMin       *int64
      SizeMax       *int64
      CreatedAfter  *time.Time
      CreatedBefore *time.Time
      UpdatedAfter  *time.Time
      UpdatedBefore *time.Time
      SortBy        string               // 白名单: key/size/created_at/updated_at/content_type
      SortOrder     string               // asc/desc
  }

REST API 扩展:
  GET /v1/files?prefix=...&tag.dept=finance&metadata.project=audit&size_min=...&sort_by=created_at&sort_order=desc

MCP tool:
  "query_objects" — 结构化查询能力

SQL 映射层:
  sql.go 新增 BuildFilterQuery(filter ObjectFilter) (string, []interface{}, error)
  — 内部处理 SQLite vs Postgres JSON 函数差异
  — 所有用户输入通过参数化查询绑定
  — sort_by 白名单校验
```

#### 对现有系统的影响

| 影响范围 | 程度 | 缓解措施 |
|---------|------|---------|
| `repository` 接口 | **新增方法** | 非 breaking change：新的 `ListObjectsFiltered` 作为可选扩展，旧 `ListObjects` 保留且行为不变 |
| SQL 层 | 内部扩展 | `sql_objects.go` 新增 `buildFilterQuery`；使用 `rebind` 统一参数占位符 |
| REST Handler | 扩展参数解析 | `List` handler 新增可选 query 参数；向后兼容——不带参数时行为与当前完全一致 |
| MCP | 新增 tool | 不影响现有 tool 列表 |
| SDK | 新增方法 | 向后兼容的扩展 |
| AI 搜索 | 无影响 | 结构化查询与语义搜索是正交能力 |

#### 边界情况处理

| 场景 | 策略 |
|------|------|
| `tag.<k>` 的 key 含特殊字符(点号、引号) | 严格校验 `^[a-zA-Z0-9_-]{1,128}$`；超限返回 400 |
| 大表无索引的全表扫描 | `sql.go` 中 `EXPLAIN QUERY PLAN` 检测全表扫描 → metric 告警 + 日志 |
| 查询超时 | repository 层使用 context deadline + `LIMIT` 上限（1000）+ query timeout |
| 分页 cursor 在过滤条件下不稳定 | 必须包含 `ORDER BY` 列和主键列的复合索引 |
| 大量 tag key 的组合索引爆炸 | 不自动建索引——提供 DBA 级别的索引创建指南 |

---

### 方向三：事件驱动的可配置工作流触发器 (P2)

#### 为什么需要

**业务价值：**

- 这是从"存储系统"向"存储平台"演进的关键一步。可配置的通知规则（非硬编码 subscriber）使外部系统可以原生集成。
- `notification_rules` schema 已在 migration 0024 中存在，REST API 端点已注册(Get/Put/Delete Bucket Notifications)，但全部为空壳——**这是一笔待清偿的技术债**。
- 与当前全局 webhook（单一 URL 推全部事件）的关系：全局 webhook 保持系统级监控，通知规则实现业务级集成。两者共存互补。

**技术价值：**

- 完整的事件过滤和路由引擎将为未来的 Filtered Event Bus 提供基础。
- 规则引擎的设计模式（规则→匹配→分发）可复用于其他配置驱动的行为（如生命周期规则、ACL 规则）。

#### 核心挑战与技术难点

| 挑战 | 难度 | 分析 |
|------|------|------|
| **事件过滤性能** | ⭐⭐⭐⭐ | 如果规则匹配在 `Bus.Publish` 路径同步执行，将增加核心写入路径的延迟。每个事件需要匹配 N 条规则（事件类型+前缀+后缀）。**核心决策：同步 vs 异步匹配。** |
| **多目标分发可靠性** | ⭐⭐⭐ | HTTP endpoint 不可达时的重试策略需要与全局 webhook 的重试模式一致但隔离（互不影响）。 |
| **规则级限流** | ⭐⭐⭐ | 需要令牌桶 per-rule。`RateLimitRPS` 在规则数量多时的维护成本。 |
| **规则更新与正在处理的事件之间的竞态** | ⭐⭐ | 最终一致性——不阻塞 `Publish`，接受短暂不一致。 |
| **与全局 Webhook 的关系** | ⭐⭐ | 功能重叠但场景不同：全局 webhook（系统级监控）vs 通知规则（业务级集成）。需要确保两者不冲突。 |

#### 架构决策：同步 vs 异步规则匹配

| 维度 | 同步匹配 | 异步匹配（推荐） |
|------|---------|----------------|
| `Publish` 延迟 | 增加 O(规则数) | 无影响（仅入 events 表） |
| 事件持久化 | 已入 events 表 | 已入 events 表 |
| 规则匹配时机 | publish 时 | notification worker 从 events 表消费时 |
| 匹配失败影响 | 阻塞写入路径 | 只阻塞通知路径 |
| 实现复杂度 | 低（直接调用） | 中（新增 worker + 消费逻辑） |
| 恢复能力 | server 重启后重新匹配 | events 表消费指针可恢复 |

**推荐：异步匹配。** 方向一已经需要在 reconcile 中做循环扫描，方向三的 notification worker 可以借鉴相同模式。EventBus 的 `Publish` 继续保持轻量（持久化 + 内存广播），不增加规则匹配逻辑。

#### 预期的架构变更

```
新增文件:
  internal/events/notification.go
    type NotificationRouter struct {
        repo repository.Repository
        limiter *RateLimiter  // per-rule 令牌桶
    }
    func (n *NotificationRouter) Start(ctx)  // goroutine: 轮询 events 表
    func (n *NotificationRouter) processEvent(ctx, event)
      → 查询 bucket notification_rules
      → FOR EACH rule:
           matchEventType(event, rule.Events)
           matchPrefixSuffix(event.Key, rule.Filter)
           sendToDestination(event, rule.Destination)

  internal/events/destination.go
    type DestinationHandler interface {
        Send(ctx, event, dest) error
    }
    type HTTPDestination struct { client *http.Client }
    // future: SQSDestination, SNSDestination, LambdaDestination

现有文件修改:
  internal/events/bus.go
    — subscriber 列表新增 NotificationRouter（类型是 EventBus subscriber，但不是硬编码 listener）

  internal/repository/sql_buckets.go
    — 反序列化 notification_rules JSON → []NotificationRule（当前是 text 字段无解析）
    — rulesCache：减少每次事件发布都查询数据库

  cmd/server/main.go
    — 初始化 NotificationRouter
    — 将 rulesCache 注入 FileService 或事件路径
```

#### 对现有系统的影响

| 影响范围 | 程度 | 缓解措施 |
|---------|------|---------|
| EventBus | 新增 subscriber | 非侵入式：注册新的 subscriber 即可 |
| `Publish` 延迟 | **无影响**（异步匹配） | 通知规则匹配不在 publish 路径 |
| 已有 Webhook | 无行为变化 | 共存策略——全局 webhook 保持现有行为 |
| Notification API | 从空壳变为真实执行 | 向后兼容：现有 `GetBucketNotifications` 返回的 JSON 格式不做变化 |

---

### 方向四：分布式读路径扩展——内容缓存层与只读副本路由 (P2)

#### 为什么需要

**业务价值：**

- 高频读取场景（同文件每秒数百次 GET）的延迟从 10ms+ 降至 <1ms（内存缓存命中）。
- 减少存储后端（特别是 S3/OSS/COS 等有 API 调用费用的云存储）的请求量。每次 GET 调用都有金钱成本。
- 为多区域部署做准备。当前所有请求路由到主区域，跨区域延迟高。

**技术价值：**

- 验证 Storage 接口的**装饰器模式**扩展性。`CacheStorage` 作为 `Storage` 接口的 wrapper，是 Go 接口设计的经典应用。
- 为 RateLimiter 的模式（装饰器模式）在缓存场景的复用提供范本。

#### 核心挑战与技术难点

| 挑战 | 难度 | 分析 |
|------|------|------|
| **缓存一致性** | ⭐⭐⭐⭐ | 写入后立即读取（写后读一致性）在多副本场景下的挑战。write-through 保证强一致但增加写入延迟；write-around 简单但缓存可能 stale。 |
| **大对象内存压力** | ⭐⭐⭐ | 5GB 模型文件如果被缓存，内存将迅速耗尽。需要 `MaxObjectBytes` 配置 + LRU 逐出 + 流式检测（大文件不缓存 body 但缓存 metadata）。 |
| **分布式缓存失效** | ⭐⭐⭐ | 多实例部署时，一个实例写入新版本后其他实例的缓存需要失效。集中式缓存（Redis）解决但增加依赖。无 Redis 时使用短 TTL。 |
| **缓存层故障降级** | ⭐⭐ | 缓存不可用不应导致服务不可用。断路器模式 + fallback 到直接读取。 |
| **只读副本延迟** | ⭐⭐⭐ | 数据库只读副本的复制延迟导致读取到旧元数据。需要通过标记（如 `?consistency=strong`）区分一致性级别。 |

#### 架构方案对比

| 维度 | 单实例内存缓存（First Step） | Redis 集中缓存（Intermediate） | 只读副本路由（Advanced） |
|------|---------------------------|-------------------------------|------------------------|
| **复杂度** | 低 | 中 | 高 |
| **一致性** | 强（单进程） | 弱（网络延迟） | 弱（复制延迟） |
| **依赖** | 无（纯内存） | Redis | 数据库只读实例 + 存储副本 |
| **延迟改善** | 10ms → 0.1ms | 10ms → 0.5ms | 跨区域 200ms → 5ms |
| **适用部署** | 单实例 | 多实例同区域 | 多区域 |
| **实现时间** | 1 周 | 1-2 周 | 3-4 周 |

**建议三步走：** 先做单实例内存缓存（装饰器模式，`internal/storage/cache.go`），验证接口扩展性；有需求时升级到 Redis；最后做只读副本。

#### 预期的架构变更

```
新增文件:
  internal/storage/cache.go
    type CacheStorage struct {
        next   Storage          // 被装饰的后端
        cache  *lru.Cache       // 内存 LRU
        config CacheConfig
    }
    // 实现 Storage 接口的所有方法
    // Get:  先查缓存 → 未命中查 next → 写入缓存
    // Stat: 缓存 ObjectInfo（TTL 控制）
    // Put:  write-around（写入 next → 失效缓存 key）
    // Delete: 删除 next → 失效缓存 key

  internal/config/config_app.go
    type CacheConfig struct {
        Enabled     bool   // CACHE_ENABLED
        MaxBytes    int64  // CACHE_MAX_BYTES (0 = unlimited)
        MaxObjects  int    // CACHE_MAX_OBJECTS
        MaxObjBytes int64  // CACHE_MAX_OBJECT_BYTES (超过不缓存 body)
        TTLSeconds  int    // CACHE_TTL_SECONDS (元数据 TTL)
        Backend     string // CACHE_BACKEND: "memory" | "redis"
    }

现有文件修改:
  internal/storage/factory.go
    — NewFromConfig() 中: if cfg.Cache.Enabled → wrap with CacheStorage
  
  internal/service/file_crud.go:Get
    — 无变化（CacheStorage 对 FileService 透明）

  internal/middleware/cache.go (新增)
    — 设置 Cache-Control / ETag / Expires HTTP 头
    — 当前已有 conditional request 支持，但缺少 Cache-Control 输出
```

#### 对现有系统的影响

| 影响范围 | 程度 | 缓解措施 |
|---------|------|---------|
| `Storage` 接口 | 无影响 | 装饰器模式——CacheStorage 实现同一接口，对新老调用方透明 |
| `factory.go` | 新增 wrapper 逻辑 | 不影响无缓存配置的部署（`Enabled=false` 时不包装） |
| 已有后端实现 | 无影响 | CacheStorage 在 factory 层包装，不修改已有后端 |
| FileService | 无感知 | 通过接口透明访问 |

---

### 方向五：分布式部署的一致快照与时间点恢复 (P2)

#### 为什么需要

**业务价值：**

- 填补生产运维工具链的唯一空白。当前系统有完整的部署工具（Helm chart、Dockerfile）、监控（Prometheus + Grafana）、日志（AccessLog + OTel tracing），但缺少数据保护层。
- 对于 Postgres + S3 生产部署，灾难恢复依赖外部工具链（pg_dump + s3 sync），没有统一的一键快照恢复能力。
- 合规场景（法律保留、取证）需要可验证的时间点数据拷贝。

**技术价值：**

- 验证 Repository 接口的事务级快照能力。这需要与底层数据库的事务隔离机制交互（Postgres 的 `SERIALIZABLE` 隔离级别或 `pg_export_snapshot`）。
- Manifest-only 快照的增量模式为后续的 CDC（变更数据捕获）和跨区域复制提供基础。

#### 三阶段实施路径分析

```
Phase 1: Manifest-only （1 周 | 低复杂度）
  └─ 事务级查询当前所有对象 → manifest.json → tar.gz
  └─ 本质上是对 Repository 查询结果的一次全量导出
  └─ 恢复基于 etag 校验（非内容复制）
  └─ 价值：审计清单 + 对象目录备份

Phase 2: Metadata Snapshot （2 周 | 中复杂度）
  └─ 为 Repository 接口新增 Snapshot/Restore 方法
  └─ SQLite: backup API（已有）
  └─ Postgres: pg_dump 内部调用或 COPY 命令
  └─ 价值：元数据完全恢复（数据库崩溃场景）
  
Phase 3: Full Content Snapshot （4 周 | 高复杂度）
  └─ 复制所有对象内容到备份存储
  └─ 增量：基于 etag 或 updated_at 只复制变更对象
  └─ 价值：完全独立于主存储的灾难恢复
```

**核心权衡：Manifest-only 足够吗？**

| 场景 | Manifest-only | Metadata+Manifest | Full |
|------|:------------:|:-----------------:|:----:|
| 审计/合规·数据清单 | ✅ | ✅ | ✅ |
| 数据库崩溃·元数据丢失 | ❌ | ✅ | ✅ |
| 存储层崩溃·数据全部丢失 | ❌ | ❌ | ✅ |
| 恢复到独立环境做测试 | ❌ | ⚠️ 需原始存储可读 | ✅ |
| 升级回滚 | ⚠️ 需原始存储可读 | ✅ | ✅ |

**建议：** Phase 1 快速交付，Phase 2 和 Phase 3 视用户需求实施。大多数生产场景中，metadata 丢失（数据库崩溃）比存储内容丢失（S3 多 AZ 冗余）更常见，所以 Phase 2 有独立价值。

#### 预期的架构变更

```
新增:
  internal/snapshot/snapshot.go 完全重写
    type Snapshotter interface {
        Create(ctx, opts) (Manifest, error)
        Restore(ctx, manifest Manifest, opts) error
    }
    
    type Manifest struct {
        Version     int
        CreatedAt   time.Time
        Level       SnapshotLevel  // 0=manifest-only, 1=metadata+manifest, 2=full
        ObjectCount int
        TotalBytes  int64
        Objects     []ManifestEntry
    }
    
    type ManifestEntry struct {
        Bucket      string
        Key         string
        VersionID   string
        ETag        string
        StorageClass string
        Size        int64
    }

  internal/snapshot/pg.go
    — PostgresSnapshotter: 使用 pg_dump 或 pgx COPY 导出

  internal/snapshot/s3.go
    — S3Snapshotter: 基于 manifest 复制对象到备份 bucket

  internal/cli/cli_snapshot.go 扩展
    — aero-vault snapshot create [--level {0,1,2}] [--output s3://...]
    — aero-vault snapshot restore --manifest manifest.json [--strategy {skip,overwrite,version}]

Repository 接口扩展:
    SnapshotTx(ctx) (Transaction, error)  // 可序列化事务
    ExportObjects(ctx, tx, cursor) ([]Object, error) // 分页导出
```

#### 对现有系统的影响

| 影响范围 | 程度 | 缓解措施 |
|---------|------|---------|
| CLI 接口 | 扩展 | 向后兼容——现有 snapshot 子命令保持 SQLite 行为 |
| Repository 接口 | 新增方法 | 非 breaking——SnapshotTx/ExportObjects 是可选的扩展方法 |
| `internal/snapshot` | 完全重写 | 旧 SQLite-only 快照代码可保留为 Phase 2 的一个分支 |
| 主启动路径 | 无影响 | snapshot 是 CLI 子命令，不进入 server 启动路径 |

---

## 3. 接口设计建议

### 3.1 Storage 接口扩展原则

当前接口（`Get/Put/Delete/Stat/List/Copy/PresignGet/PresignPut/Multipart...`）的设计优良之处在于**方法级粒度**，但缺少生命周期语义。建议遵循以下原则扩展：

**原则一：新增方法而非重载已有方法**

```
✅ TransitionStorageClass(ctx, key, targetClass) (ObjectInfo, error)
❌ Copy(ctx, src, dst, opts)  // opts 中隐含 storage_class 变更

理由：Transition 有明确的语义意图，与 Copy 是不同的操作。Copy 可能涉及
元数据更新、ACL 继承等；Transition 仅改变存储层。接口清晰明确。
```

**原则二：提供默认 fallback 避免破坏现有后端**

```go
// 在 storage.go 中定义
func (n *notImplemented) TransitionStorageClass(ctx, key, targetClass) (ObjectInfo, error) {
    return ObjectInfo{}, ErrNotImplemented
}
```

**原则三：Storage 的装饰器模式作为扩展入口**

当前 `RateLimiter` 已经使用装饰器模式包装 Storage。建议 `CacheStorage` 和未来的 `MetricsStorage` 使用相同模式。

```
Storage interface
  ├── LocalStorage (原始)
  ├── S3Storage (原始)
  ├── RateLimiter (装饰器: 限流)
  ├── CacheStorage (装饰器: 缓存) ← 方向四
  ├── MetricsStorage (装饰器: 指标收集)
  └── RetryStorage (装饰器: 重试)
  
factory.go 负责按配置组合装饰器链:
  Storage = MetricsStorage(CacheStorage(RateLimiter(S3Storage(config))))
```

### 3.2 Repository 查询扩展原则

**原则一：条件构建器与列式查询分离**

```go
// 不推荐——每个新条件都要改方法签名
ListObjectsFiltered(ctx, tenant, bucket, prefix, tags, metadata, contentType, sizeMin, ...)

// 推荐——Filter 作为参数对象，扩展时只加字段不改签名
ListObjectsFiltered(ctx, tenant, bucket, filter ObjectFilter) ([]Object, error)
```

**原则二：SQL 抽象层隐藏数据库差异**

```go
// internal/repository/sql.go
func (s *sqlHelper) jsonExtract(column, path string) string {
    if s.dialect == "postgres" {
        return fmt.Sprintf("%s->>'%s'", column, path)  // 或 @> 操作符
    }
    return fmt.Sprintf("json_extract(%s, '$.%s')", column, path)  // SQLite
}
```

**原则三：禁止在 Repository 层做排序和过滤的内存操作**

所有条件必须下推到 SQL WHERE 子句。Repository 层只做参数化查询的构建和执行，不做内存过滤/排序（大表 OOM 风险）。

### 3.3 EventBus 扩展原则

**原则一：`Publish` 保持轻量**

`Bus.Publish` 的职责应限于：
1. 持久化事件到 `events` 表
2. 内存广播到已注册的 subscriber

不应在 `Publish` 路径做规则匹配、过滤或分发。

**原则二：NotificationRouter 作为独立 subscriber 而非总线内置功能**

```go
// EventBus 的 subscriber 注册
bus.Subscribe("notification", NewNotificationRouter(repo, limiter))

// NotificationRouter 内部异步消费 events 表
func (n *NotificationRouter) Start(ctx) {
    go n.pollLoop(ctx)  // 轮询未处理的事件
}
```

**原则三：Subscriber 接口标准化**

```go
type Subscriber interface {
    HandleEvent(ctx, event Event) error
    // 可选的启动/停止钩子
    Start(ctx) error
    Stop(ctx) error
}
```

### 3.4 新抽象层评估

| 抽象层 | 需要 | 理由 |
|--------|------|------|
| **CacheStorage 装饰器** | ✅ 必要 | 不修改 Storage 接口即可添加缓存能力，是接口隔离原则的经典运用 |
| **DestinationHandler** | ✅ 必要 | 通知规则的目标类型扩展（HTTP→SQS→SNS→Lambda）需要统一接口 |
| **Snapshotter 接口** | ⚠️ 建议 | Phase 1 可先用函数实现，Phase 2 再抽象 |
| **规则引擎 DSL** | ❌ 不必要 | S3 兼容的 JSON 规则格式（notification_rules）已充分；不需要额外的 DSL |

### 3.5 向后兼容性策略

| 变更类型 | 兼容策略 |
|---------|---------|
| Storage 接口新增方法 | 默认返回 `ErrNotImplemented`；已有后端逐步实现 |
| Repository 接口新增方法 | 新方法不影响旧调用方 |
| BucketConfig 结构体重构 | 保留旧字段 + 废弃标记；新代码使用新字段 |
| REST API 新增参数 | 可选参数——不加时行为与当前完全一致 |
| CLI 新增子命令/参数 | 旧命令行为不变 |
| 迁移文件 | 单向新增，不修改已应用文件（现有规范） |
| SDK | 新增方法/参数不影响已有调用 |

---

## 4. 技术选型

### 4.1 是否引入新技术栈

| 方向 | 需要新技术 | 推荐 | 评估 |
|------|-----------|------|------|
| **方向一(生命周期)** | ⚠️ 可选 | 无新依赖 | `Storage` 接口新增方法 + reconcile 循环扫描，纯现有技术栈 |
| **方向二(元数据查询)** | ❌ 不需要 | 无新依赖 | SQLite JSON 函数和 Postgres JSONB 都是数据库内置功能 |
| **方向三(事件工作流)** | ⚠️ 可选 | 无新依赖 | HTTP 目标用 `net/http` 即可；SQS/SNS/Lambda 目标未来引入 AWS SDK |
| **方向四(读缓存)** | ⚠️ 可选 | LRU cache (stdlib) | **推荐 stdlib 方案**：`container/list` + `sync.RWMutex` 构建 LRU，零依赖；Redis 作为可选的进阶后端 |
| **方向五(PITR)** | ⚠️ 可选 | pg_dump (外部工具) | Postgres 快照调用 `pg_dump` 命令行，不引入 Go 包 |

### 4.2 依赖评估标准

当前 `go.mod` 依赖较少（核心只有 `modernc.org/sqlite`、`jackc/pgx/v5`、`aws-sdk-go-v2`、阿里/腾讯 SDK）。新依赖引入标准：

```
优先级 1: Go 标准库实现
优先级 2: 纯 Go、零 CGO、零平台依赖
优先级 3: 活跃维护、go.mod 声明、Apache 2/MIT 许可
优先级 4: 最小传递依赖（每条传递依赖签署评估）
优先级 5: 仅一个版本的依赖（无 vendor 冲突）
```

**具体建议：**

| 候选依赖 | 推荐 | 理由 |
|---------|------|------|
| `hashicorp/golang-lru` | ⚠️ 建议自建 | 项目只需要简单 LRU，`container/list` + `sync.RWMutex` 约 50 行实现，无需引入外部包 |
| `go-redis/redis` | ✅ 需要时引入 | 如果 `CACHE_BACKEND=redis` 是必选功能，这是标准选择 |
| `google/uuid` | ❌ 不需要 | 已有 `xid` 或其他生成方式 |
| 新的云存储 SDK | ⚠️ 按需 | 方向一需要 OSS/COS 的 Transition API——但那是未来扩展 |

### 4.3 自建 vs 采购决策

| 决策 | 建议 | 权衡 |
|------|------|------|
| LRU 缓存实现 | **自建** | 50 行代码 vs 外部依赖——自建明显更优 |
| 通知规则引擎 | **自建** | 这是核心竞争力的一部分，没有合适的开源替代品 |
| pg_dump 集成 | **调用外部命令** | 生产级 Postgres 备份已有 15 年成熟方案，不需要重新实现 |
| Redis 缓存后端 | **使用开源库** | Redis 集成不是差异化功能，标准库实现维护成本低 |

---

## 5. 实施路线图

### 5.1 优先级总排序

```
P0: 当前 Sprint 目标（端到端集成测试）——持续完成中
P1: 方向二·元数据查询 + 方向一·生命周期分层
P2: 方向三·事件工作流 + 方向四·读缓存
P3: 方向五·分布式一致快照
```

**排序逻辑：**

| 方向 | 优先级 | 理由 |
|------|--------|------|
| **方向二(元数据查询)** | **P1** | 低风险(1-2周)·高产品价值·schema 就绪·零新依赖。最容易快速交付并产生体验提升。 |
| **方向一(生命周期)** | **P1** | 直接降低用户成本·显著提升 S3 兼容性·reconcile 引擎已有骨架。需要谨慎的接口设计但实现路径清晰。 |
| **方向三(事件工作流)** | **P2** | schema 存在但核心架构决策(同步vs异步)需要方向一/二的工程经验反馈。异步匹配模式与方向一的 reconcile 模式类似，可以先积累方向一的经验。 |
| **方向四(读缓存)** | **P2** | 装饰器模式值得先验证(方向一的 Storage 接口扩展经验)。单实例内存缓存可以快速交付，但实际价值依赖多实例或多区域部署场景。 |
| **方向五(PITR)** | **P3** | 技术复杂度最高·操作频率低(每月一次)·外部工具链(pg_dump+s3 sync)可作为 workaround。Phase 1(manifest-only)可以提前做，但完整生产级快照可以等待更多用户反馈。 |

### 5.2 阶段划分和里程碑

```
里程碑 M0: 方向二交付 (Sprint N+1)
  交付物:
    - REST API: GET /v1/files?tag.*&metadata.*&size_min&created_after&sort_by
    - MCP tool: query_objects
    - Repository: ListObjectsFiltered + buildFilterQuery
    - SQLite + Postgres JSON 双实现
    - 索引创建指南文档
  完成标准:
    - go test ./internal/... 通过
    - 集成测试覆盖过滤查询
    - 不带过滤参数时行为与旧 API 完全一致

里程碑 M1: 方向一交付 (Sprint N+2)
  交付物:
    - Storage 接口: TransitionStorageClass + RestoreFromGlacier
    - BucketConfig: LifecycleRules (完整 S3 兼容)
    - reconcile: sweepTransitions + sweepNoncurrent + sweepAbortedMPU
    - S3 XML: 完整生命周期解析和响应
    - 迁移文件: 0025
  完成标准:
    - Local 后端 transition 测试通过(文件重命名)
    - S3 后端 transition 测试通过(CopyObject with StorageClass)
    - GLACIER Get → InvalidObjectState
    - 旧 ExpireAfterDays 配置兼容

里程碑 M2: 方向三交付 (Sprint N+3)
  交付物:
    - NotificationRouter (异步 events 表消费)
    - HTTP destination handler
    - 规则级限流(RateLimitRPS)
    - 规则级重试策略
    - 与全局 webhook 共存测试
  完成标准:
    - 事件匹配 + 前缀/后缀过滤 + HTTP POST 发送
    - 重试持久化(notification_failures 表)
    - 限流命中时不阻塞其他规则
    - 每桶最多 10 条规则

里程碑 M3: 方向四交付 (Sprint N+3/N+4)
  交付物:
    - CacheStorage (内存 LRU 装饰器)
    - 缓存配置(CacheConfig)
    - HTTP 缓存头(Cache-Control/ETag)
    - 缓存指标(cache_hit_ratio, cache_size)
  完成标准:
    - 顺序读取同对象 → 第二次从缓存读取
    - Put → 后续 Get 看到新内容(write-around)
    - 大对象(>MaxObjBytes)跳 body 缓存但缓存 metadata
    - cache miss → fallback 到原始 Storage

里程碑 M4: 方向五 Phase 1 交付 (Sprint N+4)
  交付物:
    - CLI: aero-vault snapshot create --manifest-only
    - CLI: aero-vault snapshot restore --manifest
    - manifest.json 格式定义
    - etag 校验机制
  完成标准:
    - SQLite + Postgres 都能生成 manifest
    - manifest 包含所有活动对象
    - restore 验证 etag 并报告不匹配
```

### 5.3 风险点和缓解策略

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| **Storage 接口新增 TransitionStorageClass 破坏现有后端** | 中 | 高 | 提供 `ErrNotImplemented` 默认实现；CI 测试覆盖所有后端 |
| **JSON 字段查询在 SQLite 大表上性能灾难** | 高 | 中 | 查询计划检测 + 索引指南 + 自动分页上限(1000) |
| **方向三同步规则匹配增加 Publish 延迟** | 中 | 高 | **设计决策：异步匹配**——不增加 Publish 延迟 |
| **缓存层引入内存 OOM** | 中 | 高 | `MaxBytes` + `MaxObjBytes` 双重保护 + 监控告警 |
| **Postgres 快照需要超级权限** | 高 | 中 | Phase 1 manifest-only 不需要；Phase 2 使用 `pg_dump` 外部命令，权限需求明确告知用户 |
| **多个方向同时开发导致上下文切换成本** | 中 | 中 | 严格控制并行方向数量（最多 2 个方向同时进行）；方向二(低风险)先交付积累 momentum |
| **通知规则 Schema 已有(0024)但与新设计不兼容** | 低 | 中 | 旧 schema 是自由格式 JSON text，新设计以相同格式存储。迁移兼容：解析时增加默认值。 |

### 5.4 不做的事（明确的 No-Go）

| 被排除 | 理由 |
|--------|------|
| 方向四的 Redis 缓存（第一步） | LRU 足够，Redis 在多实例场景才有意义，可以后续增量添加 |
| 方向五的 Phase 2/3（第一步） | Manifest-only 已提供审计价值，Phase 2/3 根据用户需求决定 |
| 方向三的 SQS/SNS/Lambda 目标 | HTTP 目标覆盖且灵活；ARN 目标依赖云厂商绑定，可以作为 v2 扩展 |
| 独立缓存集群/CDN | 超出当前架构范围。可选方向：通过 Cache-Control header 支持前端 CDN |
| 增量快照/CDC | 复杂度太高。先做全量 manifest，增量作为 Phase 4 |

---

## 总结

| 维度 | 结论 |
|------|------|
| **当前架构健康度** | 优秀。三层解耦 + 接口抽象 + 单一入口的核心决策经得起扩展考验。 |
| **最大的架构债务** | `notification_rules` schema 已存在但无执行引擎——这是最早需要清偿的债务（方向三）。 |
| **最高价值最快交付** | 方向二（元数据查询）——1-2 周完成，零架构风险，高用户感知。 |
| **最需要谨慎设计** | 方向一（生命周期）——Storage 接口扩展的 ripple effect 最大。推荐从 Local 后端开始验证再扩展到 S3/OSS/COS。 |
| **最大架构影响的设计决策** | EventBus `Publish` 路径保持同步 vs 引入异步匹配——**强烈推荐异步匹配**，这是防止方向三反向影响核心写入路径的关键。 |
| **推荐实施顺序** | 方向二 → 方向一 → 方向三 → 方向四 → 方向五(Phase 1) |
