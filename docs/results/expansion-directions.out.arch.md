All key code anchors verified accurate. Now I have complete context. Let me provide the comprehensive architecture analysis.

---

# 架构分析：5 个高价值扩展方向

> **分析基准文档:** `docs/requirements/expansion-v142-multimodal-ai-metadata-search-dedup-cache-active-active.md`
> **代码锚点验证:** ✅ 全部 8 个关键锚点已对照源码确认准确
> **文件状态:** 文档已存在于 `expansion-v142.md`，无需另存

---

## 1. 当前架构评估

### 优势

**清晰的分层结构与单一职责。** 现有架构的 core insight 是所有协议适配器（REST/S3/WebDAV/MCP）共享一个 `FileService` 核心，且 `Storage` 与 `Repository` 两个抽象层提供了出色的后端可替换性。这是一个经过实战验证的六边形架构（Hexagonal Architecture）变体。

**事件驱动 + Job 池的异步基础设施已就绪。** `internal/events` + `internal/jobs` 的组合是架构中最重要的「杠杆点」——方向一（多模态提取需要异步处理大文件）、方向二（索引写入时同步嵌入元数据）、方向三（GC 引用计数）、方向五（跨区域事件传播）均可复用这套基础设施。

**AI 管线的接口抽象正确。** `Extractor` → `Chunker` → `Embedder` → `VectorIndex` + `LexicalIndex` 的管线链设计良好，每个阶段有明确的 interface 边界。这为多模态扩展提供了自然的插入点。

### 局限性

**Search 管线的接口过于狭窄，缺乏元数据感知能力。** `Request` 结构体只带 `Tenant+Bucket+Query+K+Mode` 五个字段，将「搜索」单纯定义为语义文本检索，忽略了结构化过滤维度。这是整个产品搜索体验最大的人为瓶颈——不是技术限制，是接口设计的默认假设。

**Storage 接口缺少内容感知语义。** 当前 `Storage` 接口本质是「键值对存储」——`Put(key, body)`、`Get(key)`、`Delete(key)`。它不提供任何内容寻址（GetByHash）、引用计数（RefCount）、或缓存包装（WithCache）的原语。这意味着方向三（去重）和方向四（缓存）必须要么在 Storage 层之上实现（FileService 层绕开 Storage 抽象），要么扩展 Storage 接口本身——后者更符合架构设计，但涉及所有 4 个 Storage 后端的变更。

**复制架构是单向的，缺少拓扑抽象。** 当前 `Replication` 只有 source→target 的推模式。没有区域拓扑感知、没有反向复制、没有冲突模型。事件总线依赖 Postgres LISTEN/NOTIFY，其传播范围天然限制在同一 Postgres 实例内。这使得方向五（主动-主动多区域）需要引入新的跨区域传输层。

**无对象级缓存抽象层。** 数据路径 `FileService.Get → Storage.Get` 是直通的。尽管 `Storage` 接口的封装性允许插入缓存包装器，但缺少以下关键支撑：缓存粒度（按对象 vs 按 range）、TTL 策略、缓存失效的写路径联动、缩略图缓存。

### 架构债务/技术债

| 债务 | 严重度 | 影响的方向 |
|------|--------|-----------|
| BM25 索引仍在内存中重建（虽已增量维护，但启动时仍全量加载） | 中 | 方向二（filter 后的 BM25 检索需保持同样性能） |
| `Search.Request` 与 `searchRequest`（REST handler）结构体重复定义 | 低 | 方向二扩展搜索参数时需同步修改两处 |
| `Qdrant scopeFilter` 硬编码 tenant+bucket 过滤器，毫无扩展性 | 中 | 方向二的 filter 下推需要完全重写该函数 |
| 缩略图无缓存，每次从 storage 读原图 | 低 | 方向四扩展时需独立处理 |
| 迁移文件已到 50 对，方向三需要新增 `content_hash` / `ref_count` 列 | 低 | 有成熟迁移框架可复用 |

---

## 2. 扩展方向架构分析

### 方向一：多模态 AI 管线

#### 为什么需要（架构角度）

当前架构的最大产品缺口是 **Extractor 接口的单态假设**——`Extract(ctx, contentType, body) → (string, error)` 隐含了「一切输入最终都会变成纯文本」的假设。打破这个假设的收益不仅是多模态能力，而是解锁以下架构模式：

- **结构化元数据管线**：`ExtractResult.Structured` 使 JSON/YAML/表格解析结果可以直接喂入元数据锚定搜索（方向二）
- **分段索引**：`ExtractResult.Segments` 使大文档（会议录音、PDF）的分块索引可以保留语义边界
- **提取链组合**：`RemoteExtractor` 扩展为可配置的提取链（先 OCR 图片得到文字，再对文字做 NER）

#### 核心挑战

| 挑战 | 技术难点 | 缓解策略 |
|------|---------|---------|
| 接口向后兼容 | 现有 `Extractor` 返回 `(string, error)`，改为 `(ExtractResult, error)` 需更新所有调用方 | 定义 `ExtractResult` 时确保 `Text` 字段序列化为和旧 `string` 兼容的格式；用类型别名过渡 |
| 超大类媒体（GB 级视频）流式处理 | 内存上限瓶颈 | 引入大小阈值 + 异步作业（复用 `internal/jobs`）；50MB 以上触发出队处理 |
| 远程提取服务可靠性 | 外部服务超时/不可用导致索引阻塞 | 独立超时控制 + `RemoteExtractor` 降级为跳过（同 `ErrUnsupported` 路径） |
| 提取结果异构性 | 不同提取器产生不同结构的 Metadata | 定义 `Metadata` 为 `map[string]any`，不强求 schema 一致 |

#### 架构变更

```
变更前:
  Extractor { Extract(ctx, contentType, r) → (string, error) }
  
变更后:
  Extractor { Extract(ctx, contentType, r) → (ExtractResult, error) }
  
  新增 ExtractResult { Text, Metadata, Segments, Structured }
  
  Indexer: 提取错误时原有 ErrUnsupported → 多了一个 ErrTooLarge 分支，
           触发异步 Job 处理（非致命，与现有 skip 逻辑一致）
```

**影响范围:** 仅 AI 层——`internal/ai/extractor.go` + `indexer.go` + `extractor_remote.go`。零改变到 Storage/Repository/API 层。

#### 与现有系统的关系

- **无耦合至方向二**：方向一的 `Structured` 输出可为方向二提供数据源，但两者可独立推进
- **FileService 零改动**：提取管线只在索引时触发，不影响 CRUD 路径
- **Worker 池复用**：大文件的异步提取可复用现有 `internal/jobs` 实现

---

### 方向二：元数据锚定语义搜索

#### 为什么需要（架构角度）

这是**总拥有成本（TCO）最高**的架构改动。原因不是工作量最大，而是它触碰了搜索管线的核心抽象边界——`ai.Request` + `VectorIndex.SearchVectors` + `REST searchRequest` 三处需要同步扩展。同时它是最直接影响用户感知搜索质量的改动。

#### 核心挑战

| 挑战 | 技术难点 | 缓解策略 |
|------|---------|---------|
| 过滤条件下推到向量后端 vs 应用层后过滤 | 后过滤在大 K 值时严重降低召回率 | **必须下推**：Qdrant 原生支持 filter（已有 `scopeFilter` 函数可扩展），pgvector 的 SQL WHERE 可加条件；仅在 BM25/内存模式下使用后过滤 |
| Chunk 写入时嵌入过滤字段 | 当前 `InsertChunks` 不感知对象元数据 | 在 Indexer 的 `indexObject` 路径中，读取对象 tags/content_type/size 等字段，写入 chunk metadata（Qdrant payload / pgvector 列） |
| 过滤字段的数据新鲜度 | 标签/ACL 变更后，已索引的 chunk 中过滤字段过时 | 写路径联动：更新 tag 时触发 chunk 元数据更新（懒更新+定期刷新两阶段） |
| REST API 兼容性 | 新增 filter 参数不破坏现有客户端 | `searchRequest` 新增 `Filter` 字段用 `omitempty` 标记；零值 filter = 当前行为 |

#### 架构变更

```
ai.Request:
  + Filter *SearchFilter    // 新增可选字段
  SearchFilter {
    Tags        map[string]string
    ContentType string
    MinSize, MaxSize int64
    CreatedFrom, CreatedTo string
    StorageClass string
  }

VectorIndex.SearchVectors:
  (ctx, tenant, bucket, queryVec, limit)       // 当前
  (ctx, tenant, bucket, queryVec, limit, filter) // 新增 filter 参数

Qdrant scopeFilter:
  从硬编码 tenant+bucket 改为接收 filter 构建完整的 Qdrant filter tree

PgVector SQL:
  WHERE tenant=$1 AND bucket=$2                // 当前
  WHERE tenant=$1 AND bucket=$2 AND ...        // 按 filter 条件动态拼接

REST searchRequest:
  + Filter json.RawMessage  // 客户端传 {"tags":{"env":"prod"}, "content_type":"application/pdf"}
```

**关键设计决策：嵌入策略 vs 在线 Join**

| 选项 | 优点 | 缺点 | 建议 |
|------|------|------|------|
| **A: 嵌入策略**（chunk 写入时将对象元数据冗余存储到向量后端） | 向量后端原生 filter、无 Join 开销、延迟最低 | 数据冗余、元数据更新时需要同步更新 chunk | **推荐主路径**——性能最优 |
| **B: 在线 Join**（向量检索后 SQL Join objects 表做 filter） | 无冗余、元数据始终最新 | 大 K 值检索+后过滤有性能损失、无法利用 Qdrant 的索引 filter | 备选方案——BM25/内存模式下可用 |

#### 与现有系统的关系

- **方向一的 Structured 输出** 可扩展为过滤字段（如 `content_type=application/vnd.ms-excel` 自动推导），但非依赖
- **FileService 的 Tag/ACL 写路径** 需要在更新标签时触发 chunk 元数据同步
- **SDK 层**（Go/Python/JS）的 `Search` 方法需要扩展 filter 参数，保持向后兼容

---

### 方向三：内容寻址存储与块级去重

#### 为什么需要（架构角度）

这是**触及架构最深**的改动——它挑战了 `Storage` 接口的核心假设：`key` 是 `tenant/bucket/key` 路径且一一映射到 blob。引入内容寻址意味着 `StorageKey` 不再唯一：多个 Object 共享同一个 blob。

**架构矛盾的核心：** 当前 `FileService` 的职责是「对象 CRUD + 元数据管理」。去重引入后，`Put` 路径变为：

```
CreateObject → CheckContentHash → (found: 只创建引用) / (not found: Put blob + 创建引用)
```

这意味着 `FileService` 的 `Put` 方法从「始终写入」变为「条件写入」。这个语义变化需要清晰的接口契约。

#### 核心挑战

| 挑战 | 技术难点 | 缓解策略 |
|------|---------|---------|
| **鸡生蛋问题**：流式上传需要先计算 hash 才能知道是否已存在，但流只能读一次 | 缓冲到临时文件或内存 | 使用 `io.TeeReader` + 临时文件；设大小阈值（<5MB 用内存缓冲，>5MB 用临时文件） |
| **SSE 与去重的根本矛盾**：确定性加密 vs 随机 nonce | AES-GCM 的随机 nonce 导致同一明文 → 不同密文，无法去重 | **方案1**：去重仅在未加密 bucket 启用（默认）；**方案2**：使用 AES-SIV（确定性加密），但需要新加密实现 |
| **并发安全**：两个请求同时上传同一内容 | 同一 content_hash 被写入两次 | `Repository` 层 `content_hash` 列做 UNIQUE 约束 + `INSERT ... ON CONFLICT` 原子语义 |
| **GC 复杂性**：引用计数归零后删除 blob | 需要原子操作：DecrementRefCount → if 0 → DeleteBlob | 异步 GC Job（复用 `internal/reconcile` 框架），类似软删除清理 |

#### 架构变更

**Storage 接口扩展：**
```go
type Storage interface {
    // 现有方法不变
    Put(ctx, key, r, size, opts) (ObjectInfo, error)
    ...
    // 新增：内容寻址方法
    PutIfAbsent(ctx, contentHash, r, size, opts) (exists bool, info ObjectInfo, err error)
}
```

**Object 模型扩展：**
```go
type Object struct {
    ...              // 现有字段
    ContentHash string   // SHA-256 hex
    RefCount    int      // 引用计数（仅去重时可 >1）
    IsDedup     bool     // 是否启用去重
}
```

**迁移：** `migrations/{sqlite,postgres}/0011_dedup.up.sql` 新增 `content_hash` + `ref_count` 列 + 唯一索引。

**写路径变更（FileService.Put）：**
```
1. 流式计算 SHA-256（通过 TeeReader）
2. 查询 repo.GetObjectByContentHash(ctx, hash)
3. 若存在 → repo.IncrementRefCount → 删除临时文件 → 返回
4. 若不存在 → store.Put(ctx, hash, ...) → repo.InsertObject
```

**硬删除路径变更（FileService.Delete）：**
```
1. repo.DecrementRefCount(ctx, hash)
2. if RefCount == 0 → store.Delete(ctx, storageKey) + repo.DeleteObject
3. else → 仅删除元数据行，保留 blob
```

#### 与现有系统的关系

- **SSE 加密需要单独处理**：建议第一阶段仅在 `STORAGE_SSE_KEY` 未设置时启用去重
- **版本控制兼容**：versioned bucket 中每版本有独立 `version_id`，即使内容相同也不应去重（版本保留语义）
- **范围请求**：去重后多个 Object 共享 blob，但 Range 请求只需按 `ContentHash` 读取即可，无需变更
- **复制**：复制时目标端的去重表需同步维护，否则不同区域引用计数不一致

---

### 方向四：对象内容缓存层次

#### 为什么需要（架构角度）

这是**纯架构层面的优化**——现行 `FileService.Get` → `Storage.Get` 直通路径在本地存储时延迟尚可，但在云存储（S3/OSS/COS）场景下存在两个系统性问题：

1. **每次 GET 都是远程 HTTP 请求**（~50-300ms 延迟 + 出站带宽费用）
2. **热点对象无加速**（高并发读取同一对象时重复穿透）

缓存层插入在 `Storage` 接口之上是最干净的方案——`Storage` 的接口设计允许**装饰器模式**：`CachedStorage` 包装任意 backend，对外呈现同一个 `Storage` 接口。

#### 核心挑战

| 挑战 | 技术难点 | 缓解策略 |
|------|---------|---------|
| **缓存一致性**：PUT/DELETE 后的缓存失效 | 写路径需要联动清除缓存 | `CachedStorage` 包装 `Put`/`Delete` 后在缓存中删除对应 key |
| **大文件缓存**：GB 级对象不适合全量缓存 | 有限内存 | 设定大小阈值（默认 10MB）；超大文件仅缓存前 4MB + 尾部（优化 Range 请求的部分场景） |
| **Range 请求的缓存**：缓存层需理解 byte-range | 缓存 key 需要包含 range 信息 | 简单策略：全量缓存的对象直接返回 `bytes.NewReader(body[offset:offset+length])`；不缓存的 Range 请求穿透到后端 |
| **SSE 加密对象**：缓存中存储密文还是明文 | 密文缓存无安全风险但需要解密再返回；明文缓存效率高但不安全 | 仅缓存未加密对象（同方向三的策略） |

#### 架构变更

```
新增: internal/storage/cache.go
  type CachedStorage struct {
      backend Storage
      local   *lru.Cache   // 内存缓存
      config  CacheConfig
  }
  
  实现 Storage 接口全部方法，代理给 backend（Put/Delete 时联动清除）

新增: internal/storage/cache_config.go
  type CacheConfig struct {
      Enabled     bool
      MemorySize  int64  // bytes
      MaxObjSize  int64  // bytes, 大于此值不缓存
      DefaultTTL  time.Duration
  }

缩略图缓存:
  在 internal/thumbnail/thumbnail.go 新增一个本层缓存层，
  key = fmt.Sprintf("%s_%d_%d", storageKey, width, height)
```

**CDN 集成路径更简单**：不修改 Storage 层，而是在 `PresignGet` 路径上配置 CDN 域名前缀（`CDN_DOMAIN`），返回 `https://cdn.example.com/presigned...` 而不是直连 URL。CDN 的热点缓存由 CDN 自身管理，aero-vault 仅需配置 Cache-Control header。

#### 与现有系统的关系

- **零耦合至其他四个方向**——缓存是纯性能优化，不改变功能性行为
- **FileService 零改动**——缓存包装在 Storage 层之下，FileService 无感知
- **监控指标已就绪**：`internal/telemetry` 的 counter/latency instruments 可扩展 `cache_hit`/`cache_miss` 指标

---

### 方向五：主动-主动多区域复制与冲突解决

#### 为什么需要（架构角度）

这是**架构复杂度最高的方向**，因为它打破了一个根本假设：系统是运行在单个逻辑位置的。主动-主动需要将 `Repository`（元数据）、`Storage`（数据）、`EventBus`（事件）三者同时区域化。

当前复制架构是**单向推送**（source event → push to replica），而主动-主动要求的是**双向同步 + 冲突解决**——两者存在设计鸿沟。

#### 核心挑战

| 挑战 | 技术难点 | 缓解策略 |
|------|---------|---------|
| **跨区域事件传输** | Postgres LISTEN/NOTIFY 不支持 WAN | 引入 Kafka/NATS 作为区域间传输层（或简单的 HTTP 转发 + 去重） |
| **元数据双向同步** | 各区域 DB 独立，修改冲突 | LWW + timestamp（对象级别）；tags/ACL 用 CRDT map merge |
| **数据一致性模型** | 读写一致性在不同区域不同步 | 明确约束：**跨区域不保证 read-after-write**；应用层可选择 `x-aero-consistency-level: strong` 会源读取 |
| **删除冲突** | 区域 A 删除，区域 B 修改 | Tombstone + grace period（对象标记删除但元数据保留 15 分钟后才传播） |
| **全量初始同步** | 已有数据需要从主区域复制到新区域 | 扩展 `ReindexStale` 为 `ReplicateAll`（按分页遍历所有对象） |

#### 架构变更

```
新增: internal/events/region.go (跨区域事件传输)
  type RegionTransport interface {
      Publish(ctx, region, event) error
      Subscribe(ctx, region, handler) error
  }
  实现: HTTPTransport (简单), KafkaTransport (可靠)

新增: internal/replication/conflict.go (冲突解决)
  type ConflictResolver interface {
      Resolve(ctx, local, remote Object) Object
  }
  实现: LWWResolver, CRDTMergeResolver

修改: internal/replication/replication.go
  - Worker 从单向变为双向（监听源 + 目标事件）
  - ReplicateObjectByID 增加冲突检测逻辑

修改: internal/repository/repository.go
  - 新增区域感知方法：GetObjectWithRegion
  - 每个 Object 记录 last_updated_by_region

新增: 配置项
  REGIONS=us-east,eu-west,ap-southeast
  REGION=us-east              // 本实例区域标识
  REGION_TRANSPORT=kafka      // kafka | http
  REGION_BOOTSTRAP_SERVERS=...
```

#### 与现有系统的关系

- **依赖方向四的缓存**：跨区域读取的场景下，本地缓存可减少对远端 storage 的穿透
- **依赖方向三的引用计数**：去重 + 多区域时，引用计数需要跨区域同步或最终一致
- **FileService 写路径需加 region 标记**：`Object` 元数据中记录 `region` + `region_updated_at`
- **Job 池全局唯一性**：`RECONCILE_CLUSTER_SINGLETON` 需要改造为区域级（每个区域独立）或全局（跨区域选主）

---

## 3. 接口设计建议

### 关键原则

| 原则 | 说明 | 体现方向 |
|------|------|---------|
| **向后兼容优先** | 新字段用 `omitempty` + 指针/可选；接口扩展用新方法而非修改旧方法签名 | 全部方向 |
| **接口最小化** | 每个 interface ≤ 5 个方法；新接口独立，不合并旧接口 | 方向三、四 |
| **工厂模式统一** | 后端选择统一用 factory（类似 `storage.NewFromConfig`） | 方向四缓存后端、方向五区域传输 |
| **零值安全** | 零配置 = 当前行为（默认不缓存、不去重、不跨区域） | 全部方向 |

### 是否需要新抽象层

| 方向 | 需要新抽象 | 理由 |
|------|-----------|------|
| 一：多模态 | **否** | 扩展现有 `Extractor` 接口的返回类型即可；新增 `ExtractResult` 结构体 |
| 二：元数据搜索 | **否** | 扩展现有 `Request`/`SearchVectors` 签名；新增 `SearchFilter` 结构体 |
| 三：内容去重 | **谨慎** | 新增 `Storage.PutIfAbsent` 方法 + `Repository.GetObjectByContentHash` 方法；不引入新抽象层 |
| 四：对象缓存 | **是** | 新增 `CachedStorage`（装饰器模式实现 `Storage` 接口）；新增 `CacheConfig` / `CacheMetrics` |
| 五：主动-主动 | **是** | 新增 `RegionTransport` 接口（跨区域事件传输）；新增 `ConflictResolver` 接口（冲突解决策略） |

### 新抽象层的接口草案

```go
// 方向四：缓存装饰器
type CachedStorage struct {
    backend Storage          // 被包装的后端
    cache   *lru.Cache       // 内存缓存（groupcache 或原生 lru）
    stats   CacheStats       // 缓存命中率等
    config  CacheConfig
}
// 实现 Storage 全部方法，不对外暴露新接口

// 方向五：跨区域事件传输
type RegionTransport interface {
    PublishEvent(ctx context.Context, event Event, targetRegion string) error
    SubscribeEvents(ctx context.Context, handler func(Event), sourceRegions ...string) error
    Close() error
}

// 方向五：冲突解决策略
type ConflictResolver interface {
    Resolve(ctx context.Context, local, remote *Object) (*Object, ResolutionAction, error)
}
type ResolutionAction int
const (
    ActionUseLocal  ResolutionAction = iota // 使用本地版本
    ActionUseRemote                          // 使用远端版本
    ActionMerge                              // 合并（CRDT）
    ActionConflict                           // 需要人工确认
)
```

---

## 4. 技术选型

### 各方向的技术选型建议

| 方向 | 推荐技术 | 替代方案 | 选型依据 |
|------|---------|---------|---------|
| **一：多模态提取 PDF** | `pkg/pdf` (Go pdf library: `ledongthuc/pdf` 或 `pdfcpu`) | `unstructured.io` 远程服务 / `Apache Tika` | 内建提取器零网络依赖；Tika/Unstructured 可做远程 fallback |
| **一：多模态提取 OCR** | `gosseract` (Tesseract 绑定) 或远程 `Azure Document Intelligence` | Google Cloud Vision / AWS Textract | OCR 最好在远程提取器实现（避免 CGO）；内建提取器用 Tesseract |
| **一：多模态提取音频** | `whisper.cpp` Go binding 或 `OpenAI Whisper API` 远程 | Vosk / DeepSpeech | 语音转文字是 compute-intensive 任务，建议异步 Job + 远程 API |
| **四：本地缓存** | `hashicorp/golang-lru` 或 `groupcache` | Redis (外部依赖重) | LRU + TTL 足够；零网络依赖；groupcache 自带分布式缓存能力（后续扩容） |
| **五：跨区域事件** | NATS JetStream (轻量) 或 Kafka (重量) | 简单的 HTTP 转发 + 去重队列 | NATS 更轻，适合 Go 生态；Kafka 成熟但部署复杂 |
| **五：元数据同步** | 基于 wal-level 的 CDC (pglogical / Debezium) | 应用层双写 | 应用层双写一致性难保证；CDC 是标准方案但部署复杂度高 |

### 第三方依赖评估标准

```
评估矩阵（优先级由高到低）：
  1. 零 CGO 依赖（当前项目只有 modernc.org/sqlite 用了纯 Go SQLite）
  2. 纯 Go 实现（与项目 tech stack 一致）
  3. 活跃维护（GitHub 最近 6 月有提交）
  4. 测试覆盖 ≥ 70%
  5. 无过度传递依赖（go mod graph 深度 ≤ 3）
  6. Apache 2.0 / MIT / BSD 许可
```

**严格禁止的依赖场景：**
- 强制 CGO 的新库（`gosseract` 是边界情况，建议包装为 remote extractor 而非内建）
- 需要外部安装的 runtime（如 Python 解释器、JVM）
- GPL/AGPL 许可

### 自建 vs 采购

| 组件 | 决策 | 理由 |
|------|------|------|
| PDF 文本提取 | **自建**（`ledongthuc/pdf` 128 行实现） | 纯 Go，零外部依赖，PDF 文本提取已有成熟库 |
| OCR | **采购/远程**（Azure Document Intelligence 或 Google Cloud Vision） | OCR 是 ML 密集任务，自建质量远低于云服务；`RemoteExtractor` 接口已存在 |
| 语音转文字 | **采购/远程**（OpenAI Whisper API） | 同上，自建 whisper.cpp 部署复杂，远程 API 更经济 |
| 对象缓存库 | **自建**（基于标准库 lru + mutex） | 需求明确（LRU + TTL），代码量 < 200 行，无需外部依赖 |
| 跨区域传输 | **自建轻量版**（HTTP 转发 + 去重）→ 后续可迁移到 NATS | 初期可仅用 HTTP endpoint 转发事件，生产化后再引入 NATS/Kafka |
| 冲突解决算法 | **自建**（LWW + CRDT map merge） | 纯逻辑，无外部依赖 |

---

## 5. 实施路线图

### 优先级排序

```
P0: 方向二 (元数据锚定搜索)  →  方向一 (多模态 AI 管线)
P1: 方向四 (对象缓存层次)   →  方向三 (内容去重)
P2: 方向五 (主动-主动多区域)
```

**排序理由：**

- **方向二是最高 ROI**：搜索是产品核心价值主张，且改动集中（AI 层 + REST API），不影响存储层。提升搜索体验直接提升用户感知质量。
- **方向一紧接其后**：多模态提取是产品差异化的最大杠杆点。与方向二配合使用（提取结构化元数据 → 锚定搜索）产生乘数效应。
- **方向四排在 P1 头部**：缓存对云存储成本影响显著，且与所有方向正交，可独立交付。
- **方向三排在 P1 尾部**：去重存储成本高，但涉及 SSE 协调、Storage 接口扩展、迁移文件修改，风险大于缓存。
- **方向五是 P2 长期项目**：需要前四个方向的部分能力（缓存、事件地基）后才安全实施；冲突解决模型需要在有实际多区域需求时验证。

### 阶段划分

#### Phase 1 (Week 1-4): 搜索体验提升

```
里程碑: 元数据锚定搜索上线

Week 1: 扩展 ai.Request + ai.SearchFilter 结构体
        REST API 扩展 searchRequest (新增 Filter 字段)
        SDK 同步更新 (Go/Python/JS)
Week 2: Qdrant scopeFilter 重写 + pgvector WHERE 扩展
        索引器 indexObject 路径扩展（写入 chunk 时嵌入元数据）
Week 3: BM25 过滤器（应用层后过滤）
        RRF 融合时 filter 双路径下推
Week 4: 集成测试 + 迁移扩展
        文档更新 / OpenAPI 更新
```

**风险**: Qdrant/pgvector filter 语法差异 → 需要在抽象层做适配器。缓解：`VectorIndex.SearchVectors` 的 filter 参数用内部 `SearchFilter` → 每个 adapter 自行转换为对应语法。

**产出**: 用户可以 POST 搜索请求嵌套 `{"query": "财务", "filter": {"tags": {"env":"prod"}, "content_type":"application/pdf", "created_from": "2026-01-01T00:00:00Z"}}`

#### Phase 2 (Week 5-10): 多模态 AI 管线

```
里程碑: Extractor 支持 PDF + 图片 OCR + JSON 结构化

Week 5:  Extractor 接口扩展: string → ExtractResult
          所有现有提取器迁移到新的返回类型（向后兼容）
Week 6-7: PDF 文本提取器（ledongthuc/pdf）
          图片 OCR 提取器（远程：Azure Document Intelligence / Google Cloud Vision）
Week 8:   远程提取器协议扩展（ExtractResult 的 JSON 序列化）
          大文件异步提取 Job（复用 internal/jobs）
Week 9-10: 音频转录提取器（远程 Whisper API）
            电子表格解析（内建 xlsx/csv 结构化提取）
            集成测试 + 文档
```

**风险**: 远程 AI 服务 vendor lock-in。缓解：`RemoteExtractor` 的协议是通用的，切换服务只需改 URL 和认证配置。

**产出**: 上传 PDF 发票 → 自动 OCR 提取文字 → 可搜索到发票编号、日期、金额。上传会议录音 → 转录为文字 → 可搜索会议内容。

#### Phase 3 (Week 11-14): 对象缓存层次

```
里程碑: 内存缓存 + CDN 集成

Week 11:  CachedStorage 装饰器实现 (LRU + TTL)
          CacheConfig 配置项 (STORAGE_CACHE_SIZE, STORAGE_CACHE_TTL)
Week 12:  写路径缓存失效联动 (Put/Delete 后清除)
          缓存指标 (cache_hit/cache_miss counter + latency)
Week 13:  缩略图缓存层 (thumbnail 层独立 LRU)
          Range 请求的缓存支持
Week 14:  CDN 集成: PresignGet 返回 CDN 域 URL
          Cache-Control header 配置
          集成测试 + 基准测试 (量化延迟降低)
```

**风险**: 内存使用失控。缓解：`STORAGE_CACHE_SIZE` 硬上限 + LRU 淘汰；通过 `max_obj_size` 过滤大文件。

**产出**: 热点文件 GET 延迟从 ~200ms(S3) 降至 ~1ms(内存)。CDN 开启后全球用户延迟 ~50ms。

#### Phase 4 (Week 15-22): 内容寻址去重

```
里程碑: 对象级去重上线

Week 15-16: Storage 接口扩展: PutIfAbsent
            repository 迁移: content_hash + ref_count 列
Week 17-18: FileService Put 路径改造 (TeeReader → tempfile → hash check)
            FileService Delete 路径改造 (DecrementRefCount → GC)
Week 19:    并发安全 (INSERT ON CONFLICT + advisory lock)
            GC Job (复用 internal/reconcile, 扫描 ref_count=0 的 blob)
Week 20:    SSE 与去重的协调 (仅非加密 bucket 启用)
            版本控制与去重的兼容 (versioned bucket 跳过)
Week 21-22: 全面集成测试
            性能基准测试 (写入吞吐 vs 去重率)
```

**风险**: SSE + 去重矛盾是最大架构风险点。缓解：第一阶段仅支持未加密 bucket；AES-SIV 作为第二阶段优化。

**产出**: CI 构建目录上传后，重复 artifacts 仅存一次，存储节省可达 50-95%。

#### Phase 5 (Week 23-38): 主动-主动多区域

```
里程碑: 双区域主动-主动部署

Week 23-25: 跨区域事件传输层 (HTTP Transport)
            RegionTransport 接口 + HTTP 实现
Week 26-28: 冲突解决框架 (LWV + CRDT tag merge)
            Replication Worker 双向改造
Week 29-31: 元数据 CDC (pglogical 或应用层双写 + 回填)
            Repository 区域感知方法
Week 32-34: 删除冲突处理 (tombstone + grace period)
            全量初始同步 (ReplicateAll)
Week 35-36: 集成测试 + 混沌工程 (网络分区 + 延迟注入)
Week 37-38: 文档 + 运维手册 + Grafana 多区域仪表盘
```

**风险**: 最高——CDC 部署复杂、冲突模型需要实际验证、跨区域网络不可靠。缓解：初始阶段只做 LWW 不做 CRDT；HTTP Transport 是起步版，生产化后切换到 NATS；严格测试读-写-传播延迟。

**产出**: 用户在美西上传文件 → 秒级复制到欧洲 → 欧洲用户读取延迟 <30ms。

### 风险矩阵

| 风险 | 概率 | 影响 | 方向 | 缓解策略 |
|------|------|------|------|---------|
| Qdrant/pgvector filter 语法差异导致实现碎片 | 高 | 中 | 二 | 统一内部 `SearchFilter` → adapter 模式转换；单元测试覆盖每种后端的 filter SQL/filter struct |
| SSE 与去重冲突无完美解决方案 | 高 | 高 | 三 | 第一阶段仅在非 SSE 模式启用；AES-SIV 作为后期优化；文档明确限制 |
| 跨区域 CDC 部署复杂度超预期 | 中 | 高 | 五 | 初始阶段用应用层双写（最终一致），CDC 作为可选升级路径 |
| 远程提取服务 cost 超预期 | 中 | 中 | 一 | `RemoteExtractor` 可配置调用频率限制；内建提取器承担常见格式（PDF 文本）；远程仅用于图片/音频 |
| LRU 缓存内存竞态 | 低 | 中 | 四 | 使用 `sync.RWMutex` + 分片 LRU；配置硬内存上限；OOM 保护 |
| 搜索过滤条件下推后 K 值过大导致性能退化 | 中 | 中 | 二 | 设置 `max_filter_k` 上限（默认 1000）；过滤后结果过少时自动回退到无过滤搜索 + post-filter |

---

## 总结

| 评估维度 | 结论 |
|---------|------|
| **文档准确性** | 代码锚点已验证准确，去重验证结论可靠。`expansion-v142.md` 已存在于 `docs/requirements/`，无需再保存 |
| **当前架构优势** | 分层清晰、事件/Job 基础完善、AI 管线接口合理 |
| **架构债务** | 搜索接口过窄（无 filter 维度）、Storage 无内容感知、事件总线局限单区域 |
| **最高 RO 方向** | 方向二（元数据锚定搜索）→ 方向一（多模态 AI）→ 方向四（缓存）→ 方向三（去重）→ 方向五（多区域） |
| **最大架构风险** | SSE 与去重的矛盾（方向三）、跨区域 CDC 复杂度（方向五） |
| **建议首次提交** | 扩展 `ai.Request` 新增 `Filter` 字段，6 小时原型，无需改动后端存储 |

如果希望我继续深入某个方向的架构细节（如更详细的接口定义、OOM 防护方案、冲突解决算法的 Go 伪代码），请告知。
