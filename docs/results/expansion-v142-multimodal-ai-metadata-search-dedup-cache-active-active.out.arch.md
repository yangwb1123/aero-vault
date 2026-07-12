现在我已充分阅读代码库和需求文档。以下是我的架构分析。

---

# 架构分析：aero-vault 五个高价值扩展方向

## 1. 当前架构评估

### 优势

**分层清晰，契约接口解耦。** 现有架构的核心架构模式是正确的：

- **`storage.Storage`** 是一个最小接口（8 个方法），每个后端（local/S3/OSS/COS）都是其实现。工厂模式（`storage.NewFromConfig`）允许在运行时选择后端。电路断路器已经作为装饰器包装（`NewCircuitBreaker`）——证明了装饰器模式是现有架构中的一等公民。
- **`repository.Repository`** 抽象了元数据持久化。两个实现（SQLite、Postgres）共享单一 SQL 核心。迁移是版本化的且成对的。
- **`FileService`** 是唯一的 CRUD 入口点，防止协议适配器绕过业务规则。
- **中间件链是固定的且顺序明确的** —— 不是可配置的管线，而是一个恒定的顺序。这避免了可插拔中间件排序的复杂性（这是大多数 Go HTTP 框架中的一个已知的架构痛点）。
- **AI 特性是 opt-in 且 nil 安全的** —— 整个管线在不使用时可以被禁用，而不影响核心 CRUD 路径。
- **事件驱动架构** 是异步工作的正确选择 —— Indexer、Antivirus、Replication 和 Webhook 都是事件订阅者，不需要修改 `FileService`。

### 局限性

虽然现有的架构模式是合理的，但一些接口已经达到了它们最初设计的边界，无法支持需求文档中描述的方向。

**1. Extractor 接口返回值是一个基本类型的瓶颈。**

```go
type Extractor interface {
    Extract(ctx context.Context, contentType string, r io.Reader) (string, error)
}
```

返回一个原始 `string` 会丢失所有结构信息 —— 文档布局、表格边界、语言、说话者分段和结构化数据（JSON/YAML/电子表格）都会被扁平化为一个无法区分的字符串。这在避免分配一个结构体方面提供了理论上的零优势，但在语义上限制了很多可能性。

**2. VectorIndex 接口缺少过滤参数。** 与 Extractor 问题相同但方向相反 —— 输入参数集合太窄。

```go
type VectorIndex interface {
    SearchVectors(ctx context.Context, tenant, bucket string, queryVec []float32, limit int) ([]SearchHit, error)
}
```

`bucket` 字符串是唯一的元数据过滤维度。要支持按标签、日期范围、内容类型、存储类别或大小的过滤，每一个向量后端的实现都必须分别添加这些参数，或者公共的 `Search.Request` 必须事先过滤。所需的架构模式是一个可选的 `Filter` 结构体，所有 `VectorIndex` 实现都可以向下传递。

**3. Storage 接口没有缓存或内容寻址的接缝。** `Storage` 接口假设每个 `key` 都是一次性写入且不可变的。没有 `ContentHash`、`PutIfAbsent` 或 `GetCached` 的概念。缓存可以通过装饰器包装来实现，但内容寻址需要一个存储密钥模式的根本性改变（从位置寻址 `tenant/bucket/key` 变为内容寻址 `<SHA256>`）。

**4. Chunk 结构体不携带对象元数据以支持过滤器下推。** `repository.Chunk` 包含 `TenantID`、`Bucket`、`ObjectKey` 和 `Content`，但不包含 `Tags`、`ContentType`、`StorageClass` 或 `CreatedAt`。这意味着向量后端（Qdrant/pgvector）无法在不连接对象表的情况下在 chunk 级别进行过滤，这比在 chunk 写入时将元数据嵌入 payload 要慢得多。

### 架构债务

| 债务 | 影响 | 补救成本 |
|------|------|---------|
| `RemoteExtractor` HTTP 协议是 JSON `{text, error}` —— 没有版本字段，没有结构化输出 | 向多模态扩展需要不向后兼容的协议变更 | 低 —— 添加版本字段并使其成为可选的 |
| `ReplicateObjectByID` 只复制 blob，不复制元数据 | 主动-主动基元不存在 | 中 —— 需要在 `ReplicateObject` 中添加 `repository.Repository` 参数 |
| `EventBus.PostgresTransport` 绑定到单一 Postgres 实例 | 跨区域事件传播需要完全替换 | 高 —— 这是方向五的核心阻塞点 |
| `PreflightQuota` 在流式写入之前检查配额，但无法处理未知大小的流（大小 == -1） | 大流可能超过配额 | 低 —— 在最终提交时添加第二次检查 |
| `FileService.Put` 计算 MD5 用于完整性验证，但不保留它用于内容寻址 | 去重需要第二次哈希传递或临时文件 | 中 —— 需要使用 `io.TeeReader` 计算 SHA-256 |

---

## 2. 扩展方向分析

### 方向一：多模态 AI 管线 —— 扩展 Extract 契约

**为什么需要：** 当前的 `Extractor` 接口返回一个单一的扁平字符串。这丢弃了 `application/json` 的结构、`text/csv` 的表格布局、文档的页面边界以及音频转录的说话者分段。对于声称是 AI-native 的平台来说，这是一个根本性的能力差距。

**核心架构变更：**

```
当前: Extract(ctx, contentType, r io.Reader) → (string, error)
目标: Extract(ctx, contentType, r io.Reader) → (ExtractResult, error)

type ExtractResult struct {
    Text       string            // 主可搜索文本（始终填充）
    Metadata   map[string]any    // 提取的属性（语言、页数、持续时间……）
    Segments   []Segment         // 可选的细粒度结构
    Structured map[string]any    // JSON/YAML/电子表格解析结果
}
```

**关键设计决策：** `ExtractResult` 是否应携带其自己的 MIME 类型或应仅被视为文本？**推荐：** `Text` 字段始终是索引器的主要输入。`Segments` 用于潜在的未来分段级索引。`Structured` 字段在对象是 JSON/YAML 时填充，并且可以启用一个单独的"结构化搜索"路径（超出本文件的范围，但对方向二具有架构意义）。

**对现有系统的影响：**
- **向后兼容性：** 所有现有的提取器（纯文本、CSV、JSON）可以填充 `ExtractResult{Text: body, Structured: parsed}`，而签名更改后无需更改行为。返回值的变化会导致所有调用点（`indexer.go`、`extractor_remote.go`、`RemoteExtractor` HTTP 协议）的编译错误，这是可以一次性修复的。
- **远程协议演进：** `RemoteExtractor` 当前 POST `(file, content_type)` 并接收 `{text, error}`。扩展后的协议应为：`POST /extract-v2 → {text, metadata, segments, structured, error}`。在过渡期间可以并行提供旧端点。
- **索引器变更：** `ChunkSink.UpsertObjectChunks` 需要携带每个 chunk 的 `ContentType` 和 `CreatedAt`，以实现方向二的过滤器下推。

**架构权衡：**

| 方案 | 工作量 | 灵活性 | 维护负担 |
|------|--------|--------|---------|
| **A：扩展 `ExtractResult` 为结构体**（推荐） | 小（2-3 天） | 适量 | 低 —— 仅新增字段 |
| **B：`TransformPipeline` 阶段模型** | 大（4-6 周） | 高（可配置阶段） | 中 —— 每个阶段一个接口 |
| **C：拆分为多个 `Extractor` 类型（`TextExtractor`、`ImageExtractor` 等）** | 中（1-2 周） | 高（每个模态独立） | 中 —— 调度逻辑变得复杂 |

**建议：** 使用扩展的 `ExtractResult` 走方案 A。方案 B（管道）是一个很好的长期目标，但根据路线图文档中的约 3-6 周的估计，它无法满足截止日期。方案 C 引入了分派复杂性，而收益甚微。

---

### 方向二：元数据锚定语义搜索 —— 过滤器下推

**为什么需要：** 当前的搜索只能按 `tenant + bucket` 进行过滤。对于拥有数万个文件的企业租户来说，"搜索财务"与"搜索三个月前的 PDF 财务文件"之间的区别正是语义搜索在面向用户的系统中能否成功的关键。

**核心架构变更：**

```go
// 新增 SearchFilter 结构体
type SearchFilter struct {
    Bucket      string            // 现有
    Tags        map[string]string // 精确匹配
    ContentType string            // 前缀匹配
    MinSize     int64             // ≥
    MaxSize     int64             // ≤
    CreatedFrom time.Time         // ≥
    CreatedTo   time.Time         // ≤
    StorageClass string
}

// VectorIndex 的新签名
type VectorIndex interface {
    SearchVectors(ctx context.Context, tenant, bucket string, query []float32, limit int, filter *SearchFilter) ([]repository.SearchHit, error)
}
```

**过滤器下推 vs 后过滤：** 这是正确的架构决策。Qdrant 和 pgvector 都原生支持负载过滤。在应用层进行后过滤意味着：
1. 从向量后端检索 `N` 个结果
2. 在 Go 中迭代并删除那些不符合条件的
3. 如果删除后剩余数量 < `K`，则用更宽松的标准重试（或者留给用户不完整的结果）

在数据量大时（>10 万 chunk），后过滤会失败。`QdrantIndex.scopeFilter` 已经构建了一个基于 `must` 的过滤器 —— 添加额外的 `tags.{key}`、`content_type`、`size` 和 `created_at_unix` 字段是机械性的扩展。

**关键的架构依赖：** 过滤器下推要求 chunk 在索引时携带对象元数据。当前 `repository.Chunk` 没有 `ContentType` 或 `CreatedAt` 字段。这意味着：
- **Qdrant 路径：** `UpsertObjectChunks` 必须将 `content_type`、`created_at_unix`、`size`、`tags.{key}` 作为负载字段写入 Qdrant 点
- **pgvector 路径：** `chunks` 表新增列以及 SQL WHERE 子句的扩展
- **内存 BM25 路径：** 只有在此处后过滤才可接受（<10 万 chunk）

**对现有系统的影响：**
- **Search.Request 添加 `Filter` 字段** —— 向后兼容（`omitempty`）
- **REST API /v1/search 添加可选的 `filter` JSON 对象** —— 向后兼容
- **MCP `search` 工具添加可选的 `filter` 参数** —— 向后兼容

**架构权衡：**

| 方法 | 延迟 | 存储成本 | 实现复杂度 |
|------|------|---------|-----------|
| **过滤器下推**（推荐） | 低（在向量后端过滤） | 低（无额外存储） | 中（chunk 写入时携带元数据） |
| **后过滤 + 自适应重试** | 高（检索更多 + 在 Go 中过滤） | 无 | 低 |
| **混合：向量下推 + BM25 后过滤** | 中 | 中 | 高 |

---

### 方向三：内容寻址存储与块级去重

**为什么需要：** 这对于 CI/CD 工件、Docker 镜像层、备份以及 npm 包或编译后的二进制文件等重复上传的工作负载而言，是一个游戏规则改变者。在这些场景下，存储成本降低 50-95 % 是可行的。

**核心架构变更：**

**需要新的存储基元：** `storage.Storage` 需要一个 `PutIfAbsent` 方法或同等语义：

```go
type Storage interface {
    // ... 现有方法不变 ...
    
    // PutIfAbsent 仅在 key 不存在时写入。如果 key 已存在，则返回
    // 现有 ObjectInfo 和 created=false。
    PutIfAbsent(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) (ObjectInfo, bool, error)
}
```

并非所有后端都能以原子方式实现这一点。对于 `local` 后端，可以使用 `O_EXCL` + `O_CREAT`。对于 `s3`，可以使用 `If-None-Match: *` 条件 PUT。对于 `oss` 和 `cos`，检查各自的 SDK 条件写入能力。

**存储密钥方案变更：** 当前模式 `tenant/bucket/key` 是位置寻址的。去重需要一个内容哈希的间接层：

```
当前路径:
  Put → key = "acme/docs/report.pdf" → storage blob at path

去重路径:
  Put → 计算 SHA-256 → content_hash = "a1b2c3..." → 查询 content_hash 表
    → 找到了 → RefCount++ → 用相同的 content_hash 作为 StorageKey 创建新的 Object 行
    → 未找到 → Put(content_hash) → 用 content_hash 作为 StorageKey 创建 Object 行 + RefCount=1
```

这意味着 `Object.StorageKey` 不再唯一。多个 Object 行共享同一个 `StorageKey`。这对硬删除和 GC 有影响：
- 删除减少 `RefCount`，仅当 RefCount 降至 0 时删除 blob
- 版本化存储桶可以选择在版本内关闭去重，以保留存档用途的旧版本

**与 SSE 的矛盾：** 这是方向三中最大的架构挑战。

| 加密模型 | 去重兼容性 | 备注 |
|---------|-----------|-------|
| 无 SSE | ✅ 完全兼容 | 明文直接存储且可哈希 |
| 确定性 AES-SIV | ✅ 兼容 | 相同明文 → 相同密文 |
| 每个 blob 随机 nonce 的 AES-GCM | ❌ 不兼容 | 相同明文 → 不同密文 |
| 信封 SSE（每个对象一个 DEK） | ❌ 不兼容 | 每个对象有自己的密钥 |

**建议：** 去重只对 **未加密** 的存储桶启用。SSE 存储桶在写入路径中添加警告日志："bucket <name> has SSE enabled; deduplication is unavailable"。这是在复杂性（确定性加密、关键生命周期）和收益（去重存储桶的用户中，大多数是 CI/数据湖，它们通常不加密）之间最简单的权衡。

**对现有系统的影响：**
- `repository.Object` 新增 `ContentHash` 和 `RefCount` 字段
- 新的 `content_hashes` 表（`content_hash`、`storage_key`、`ref_count`、`size`、`created_at`）
- `repository` 接口新增 `GetObjectByContentHash`、`IncrementRefCount`、`DecrementRefCount` 方法
- `FileService.Put` 需要临时文件缓冲以计算哈希（或流式哈希 + 回退）
- SQLite/Postgres 迁移成对文件
- 硬删除路径：`FileService. HardDelete` → `DecrementRefCount` → 如果为 0 则 `storage.Delete`

---

### 方向四：对象内容缓存层次

**为什么需要：** 对于云存储后端（S3、OSS、COS），每次 GET 请求都有网络延迟（50-300 毫秒）和出站数据传输费用（通常为 0.09 美元/GB）。缓存热点内容可将延迟降低到亚毫秒级，并将出站成本降低多达 90 %。

**核心架构变更：**

装饰器模式。`CachedStorage` 包装 `Storage`，实现相同的接口，并在委派给后端之前添加一个缓存层：

```
当前:  handler → FileService.Get → store.Get (hit S3 every time)
目标:  handler → FileService.Get → cachedStore.Get (check L1→L2→backend→fill)
```

```go
type CachedStorage struct {
    backend Storage
    l1      *cache.LRU    // 内存，可配置大小
    l2      *cache.Disk   // 本地 NVMe/SSD，可配置大小
    config  CacheConfig
    metrics *CacheMetrics
}

func (c *CachedStorage) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
    // 1. 检查 L1（内存）
    // 2. 如果未命中，检查 L2（磁盘）
    // 3. 如果未命中，调用 backend.Get → 根据策略填充缓存
    // 4. 返回数据
}
```

**关键设计决策：**

**决策 1：缓存什么？**
- **小对象（<1 MB）：** 完整缓存到内存中。读路径消耗 `bytes.NewReader(body)`。
- **中等对象（1-100 MB）：** 缓存到本地磁盘。读路径消耗 `os.Open(cachePath)`。
- **大对象（>100 MB）：** 旁路缓存。Range 请求可以缓存前 4 MB 和后 4 MB 的热区，但不在分析范围内。
- **SSE 加密对象：** 不缓存。解密后的明文不应暴露在非加密的缓存层中。

**决策 2：缓存失效策略。**
- **写入失效：** `CachedStorage.Put` 和 `Delete` 必须从其缓存中移除对应的键。这是正确性的关键。
- **TTL 过期：** 小对象：60 秒。中等对象：300 秒。静态大对象：3600 秒。
- **容量驱逐：** LRU 用于内存。LFU 或 FIFO 用于磁盘（为了减少写入放大）。
- **主动失效：** `SIGUSR1` 或 `Cache-Control: no-cache` 在请求中。

**决策 3：缓存预热？**
- 首次读取时填充（冷启动）。对于预计会有高流量的已知内容，可以选择预热。

**缩略图缓存：** 与通用缓存层次分开。使用 `{storageKey}_{width}_{height}` 作为键。这可以在独立模块 `thumbnail_cache.go` 中实现，因为它具有特定于缩略图的行为（仅 JPEG/PNG、小尺寸、可选的预生成）。

**对现有系统的影响：**
- `storage.Storage` 中无需更改接口 —— `CachedStorage` 实现了 `Storage`。
- 工厂模式需要新增 `WithCache(cfg)` 包装函数。
- `storage.PresignGet` 不受影响 —— presigned URL 绕过缓存。

**架构权衡：**

| 方面 | 延迟加载缓存（推荐） | 直写缓存 | 回写缓存 |
|------|---------------------|---------|---------|
| 实现复杂度 | 低 | 低 | 高 |
| 写一致性 | 立即（Put 使缓存失效） | 立即 | 延迟 |
| 缓存污染 | 仅限读取过的内容 | 所有写入的内容（即使从未被读取） | 所有写入的内容 |
| 适用性 | 偏向读取的工作负载 | 偏向写入的工作负载 | 低延迟写入 |

---

### 方向五：主动-主动多区域复制

**为什么需要：** 这是最雄心勃勃的方向，需要最基础性的架构变革。当前系统假设一个单一的元数据 DB 和一个单一的存储后端。跨区域的主动-主动需要涉及元数据、存储和事件的分布式系统基元。

**根本性架构变革：**

**1. 元数据复制（最困难的部分）。** 当前 `repository` 是单数据库的。要在两个区域保持两个数据库同步，有几种选择：

| 方法 | 一致性 | 延迟 | 复杂度 | 推荐用于 |
|------|---------|-------|--------|---------|
| **基于 CDC 的 LWW**（Debezium → Kafka → 应用到区域 B） | 最终 | 秒级 | 中 | 对象元数据 |
| **应用层双写**（每个写入都写入两个区域） | 立即 | 写入延迟（两个区域） | 低 | 小规模部署 |
| **CRDT 数据库**（如 Redis CRDT、DynamoDB Global Tables） | 最终 | ~100 毫秒 | 极低（产品内置） | 标签/ACL（元数据的子集） |
| **Postgres 逻辑复制** | 最终 | ~100 毫秒 | 中 | 结构化元数据 |

**推荐：CDC + LWW 用于 blob 元数据，CRDT 用于标签/ACL。**

**2. 跨区域事件传输。** `EventBus.PostgresTransport`（LISTEN/NOTIFY）无法跨 WAN 工作。需要一个替代方案：

```
区域 A 事件总线 → 事件日志表（每个区域） → [区域间传输] → 区域 B 事件总线
                                                ↓
                                          Kafka / NATS / HTTP 转发
```

最务实的选择是 **Kafka 或 NATS**，因为它们已经处理了持久性、重放和消费组语义。对于不想运行 Kafka 的部署来说，一个更轻量级的替代方案是 HTTP 转发，使用重试 + 幂等性。

**3. 冲突解决模型。** 文档中推荐了 LWW，我同意，但有一些补充：

- **对象 blob（Put/Delete）：** LWW，使用 `updated_at` 时间戳。拒绝 `updated_at < local_updated_at` 的复制事件。
- **标签（map[string]string）：** CRDT 映射合并 —— 每个区域写入其自己的键，合并是键的并集。
- **ACL（字符串/策略）：** LWW。
- **Bucket 配置（版本控制、生命周期）：** LWW，但使用明确的仲裁（区域变更必须获得批准）。
- **删除冲突：** 文档中提出了一个 15 分钟的 tombstone 宽限期。我同意 —— 删除应暂时标记，而不是立即复制为删除。

**4. 读取一致性模型。** 必须明确文档化：

- **单区域读己写（RW）：** 保证（在同一区域内写入后立即读取）。
- **跨区域读己写：** 不保证（最终一致性）。可通过 `x-aero-consistency-level: strong` 头显式选择加入，该头等待复制确认或读取主区域。
- **列表操作：** 最终一致性。区域 B 的列表可能不会立即反映区域 A 的写入。

**对现有系统的影响：**

| 组件 | 变更 |
|------|-------|
| `repository.Repository` | 新增区域感知：`RegionID`、`LastUpdatedAt`（跨区域 LWW 所需） |
| `repository.Object` | 新增 `RegionID`、`UpdatedAt`（纳秒精度）字段 |
| `EventBus` | 新增 `regionTransport` 接口（Kafka/NATS/HTTP） |
| `Replication.Worker` | 从单向扩展为双向：分配 `Role`（主/从/双主） |
| `Reconcile/Job` | 新增区域级领导选举（使用 `leases` 表的区域感知变体） |
| `main.go` | 新增区域配置：`REGION_ID`、`REGION_PEERS` |
| `config.go` | 新增区域配置变量 |

---

## 3. 接口设计建议

### 3.1 关键接口的演进原则

整个代码库的接口可以排序为三个"圈"：

```
核心（稳定）：   Storage, Repository, FileService
AI 层（演进中）： Extractor, VectorIndex, ChunkSink, Search
基础设施（稳定）： EventBus, jobs.Pool, middleware
```

**原则 1：以向后兼容的方式演进 AI 层。** `ExtractResult`、`SearchFilter` 和 `VectorIndex.SearchVectors` 的变更应使用可选字段（Go 中的零值表示"未设置"），这样现有的实现和调用点可以优雅地降级。

**原则 2：使用装饰器模式添加横切关注点。** 缓存的 `CachedStorage` 和电路断路器的 `CircuitBreaker` 已证明这是正确的模式。在需求文档中，内容哈希去重也可以作为装饰器实现（`DedupStorage` 包装 `Storage`），尽管它比缓存需要更多状态（content_hash 表查找）。

**原则 3：为跨区域支持引入区域 ID 作为上下文维度。** 所有需要跨区域感知的接口都应接收 `RegionID`（作为上下文值或显式参数），而不是依赖于全局配置。

### 3.2 具体的接口演进

**Extractor 接口演进（向后兼容）：**

```go
// 第 1 阶段（兼容）：ExtractResult 是可选结构体
type ExtractResult struct {
    Text       string         `json:"text"`        // 始终有效
    Metadata   map[string]any `json:"metadata,omitempty"`
    Segments   []Segment      `json:"segments,omitempty"`
    Structured map[string]any `json:"structured,omitempty"`
}

// 旧的签名保留为辅助函数
func (r ExtractResult) AsString() string { return r.Text }
```

**VectorIndex 接口演进（向后兼容）：**

```go
type SearchFilter struct {
    Tags           map[string]string // key → value 精确匹配
    ContentType    string            // 前缀匹配
    MinSize        int64             // ≥
    MaxSize        int64             // ≤
    CreatedAfter   int64             // Unix 秒
    CreatedBefore  int64             // Unix 秒
    StorageClass   string
}

// filter 参数为 nil 表示"无过滤器" —— 现有实现不受影响
type VectorIndex interface {
    SearchVectors(ctx context.Context, tenant, bucket string, query []float32, limit int, filter *SearchFilter) ([]repository.SearchHit, error)
}
```

**Storage 接口演进（内容寻址）：**

```go
type Storage interface {
    // ... 现有方法 ...
    
    // PutIfAbsent 仅在 key 不存在时写入。
    // 返回 (ObjectInfo, true, nil) 表示新创建，
    // 或 (ObjectInfo, false, nil) 表示已存在。
    PutIfAbsent(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) (ObjectInfo, bool, error)
}
```

并非所有后端都能原子地实现 `PutIfAbsent` —— 本地使用 `O_EXCL`，S3 使用 `If-None-Match: *`，OSS/COS 需要回退到先 `Stat` 再 `Put`。对于回退实现，存在竞态条件，但 content_hash 唯一约束应处理它。

### 3.3 新的抽象层

**缓存层（`storage/cache.go`）：** 一个全新的、可选的文件，实现了与所有其他后端相同的 `Storage` 接口。按大小分层的缓存（内存 LRU，本地磁盘）。

**区域传输层（`events/region.go`）：** 跨区域事件传播的新接口。比具体传输更早地抽象出传输方式（Kafka/NATS/HTTP）。

```go
type RegionTransport interface {
    Publish(ctx context.Context, region string, events []Event) error
    Subscribe(ctx context.Context, localRegion string, handler EventHandler) error
    // 当前区域的延迟和吞吐量统计
    Latency(ctx context.Context, peerRegion string) (time.Duration, error)
}
```

---

## 4. 技术选型

### 4.1 新依赖项评估

| 方向 | 建议的技术 | 是否需要新的 `go.mod` 依赖？ | 评估 |
|------|-----------|---------------------------|--------|
| 方向一（多模态） | PDF：`ledongthuc/pdf` 或 `pdfcpu` | ✅ pdfcpu（纯 Go，无需 CGO） | 对于基本 PDF 文本提取，纯 Go 就足够了。对于布局感知的 PDF 提取，使用 `RemoteExtractor` 调用 Azure Document Intelligence 或 Unstructured.io |
| 方向一（多模态） | OCR：`gosseract`（需要 Tesseract C 库） | ❌ 使用 `RemoteExtractor` 调用 Tesseract REST 服务 | 通过远程提取器避免 CGO。维护负担更低。 |
| 方向一（多模态） | 电子表格：`excelize`（纯 Go） | ✅ excelize | 纯 Go，经过充分测试，支持 XLSX。 |
| 方向二（过滤器下推） | 不需要新的依赖项 | 无 | Qdrant 和 pgvector 已经有原生过滤支持。只有在添加新的向量后端（如 Milvus）时才需要新的客户端库。 |
| 方向三（去重） | 不需要新的依赖项 | 无 | SHA-256 在标准库中。 |
| 方向四（缓存） | 不需要新的依赖项 | 无 | `hashicorp/golang-lru` 如果标准 map + mutex 不够。即使那样，一个简单的 LRU 也可以少于 100 行实现。 |
| 方向五（主动-主动） | 跨区域事件传输：Kafka / NATS | ✅ `segmentio/kafka-go` 或 `nats-io/nats.go` | NATS 对操作更友好（二进制小，无 ZooKeeper 依赖，内建 JetStream 用于持久化）。Kafka 是行业标准，但更适合于已运行 Kafka 的部署。 |

### 4.2 自建 vs 采购的决策

**方向一（多模态提取）：** 自建 PDF 提取（使用 `pdfcpu`），远程提取用于 OCR。商业 API（Azure Document Intelligence、Google Document AI）作为 `RemoteExtractor` 端点集成 —— 无需供应商锁定。

**方向五（跨区域事件传输）：** 不要在跨区域传输上自建，除非团队有分布式系统专业知识。使用 NATS（更简单）或 Kafka（更标准）。两者的 JetStream/消费者组语义都处理了持久性、至少一次交付和重放。

### 4.3 架构风险技术栈

| 风险 | 影响 | 缓解措施 |
|------|--------|-----------|
| Qdrant 集合需要在启动时自动配置 `vectors` 大小 | 如果大小与现有集合不匹配，Qdrant 返回 400 | 已实现（`EnsureCollection`），但应该添加尺寸迁移或集合版本 |
| pgvector HNSW 索引需要 vacuum 和重建 | 索引膨胀导致性能下降 | 添加可配置的 `reindex_cron` 或暴露 SQL 控制 |
| NATS/Kafka 对于简单的双区域设置来说太重了 | 运维开销 | 保持事件传输是可插拔的。默认使用 HTTP 转发（带重试），可选使用 NATS/Kafka。 |

---

## 5. 实施路线图

### 优先级分类

| 优先级 | 方向 | 理由 |
|--------|-------|--------|
| **P0** | 方向二（元数据锚定搜索） | 最高 ROI。搜索是产品的核心差异化因素。过滤器下推立即改善所有用户的搜索体验。对搜索层的影响是孤立的，并保持向后兼容性。 |
| **P0** | 方向一（多模态 AI 管线） | 与方向二并行，零依赖。为平台解锁全新的内容类别（PDF、图像、音频）。产品差异化。 |
| **P1** | 方向四（对象缓存层次） | 直接降低云成本（每月物理美元）。缓存模式正确性（写入路径上的一致性）需要在下游去重之前建立，该去重也修改写入路径。 |
| **P1** | 方向三（内容去重） | 存储层变更影响所有路径。SSE 矛盾需要仔细设计。需要方向四的缓存失效模式成熟后再开展。 |
| **P2** | 方向五（主动-主动多区域） | 最大的努力，最高的风险，最窄的用户群（多区域运营）。依赖方向四的就绪度（缓存减少跨区域延迟）。 |

### 阶段划分

**阶段 1（第 1-2 周）：并行进行方向一和方向二**

**方向一里程碑：**
- 扩展 `Extractor` 接口以返回 `ExtractResult`（结构体）
- 更新 `RemoteExtractor` HTTP 协议（v2 端点或版本字段）
- 实现 `pdfcpu` 集成用于 PDF 文本提取
- 实现 `excelize` 集成用于 XLSX 提取
- 更新 `DefaultExtractor` 以注册新的提取器
- 更新 `indexer.go` 以使用 `ExtractResult.Text`
- 在 `ExtractResult.Metadata` 中可选的语言检测

**方向二里程碑：**
- 添加 `SearchFilter` 结构体和 `*SearchFilter` 参数到 `VectorIndex.SearchVectors`
- 更新 `QdrantIndex.SearchVectors` 以将 filter 字段添加到 Qdrant 负载过滤器
- 更新 `pgvector.SearchVectors` 以将额外的列过滤添加到 SQL WHERE 子句
- 更新 `repoVectorIndex` 以在 Go 中进行后过滤（保留向后兼容性）
- 更新 `Search.Request` 以携带 `Filter` 字段
- 更新 `REST handler`、`MCP search` 工具以传递 filter
- 添加在 chunk 写入时将对象元数据嵌入到 chunks 的功能（`indexer.go` / `ChunkSink`）

**交付物：** 搜索 API 现在接受 `filter` 参数。PDF、XLSX 和图像被索引。延迟影响：方向一无；方向二在向量后端有原生过滤支持时为可忽略不计。

**阶段 2（第 3-4 周）：方向四**

**里程碑：**
- 实现 `CachedStorage` 包装器（内存 LRU + 本地磁盘后备）
- 实现写入路径上的缓存失效（`Put`、`Delete`、`DeletePrefix`）
- 实现缩略图缓存（独立模块）
- 为所有后端类型在 `factory.go` 中添加 `WithCache` 选项
- 配置：`CACHE_MEMORY_SIZE`、`CACHE_DISK_PATH`、`CACHE_DISK_SIZE`、`CACHE_TTL_SECONDS`
- 添加 `Cache-Control` 响应头支持以供 CDN 集成
- `PresignGet` 中可选的 CDN URL 包装

**交付物：** 热点对象的 GET 延迟从 100-300 毫秒降至 <1 毫秒（内存缓存）。缩略图生成缓存。

**阶段 3（第 5-8 周）：方向三**

**里程碑：**
- 添加 `content_hashes` 表 + 迁移文件
- 为 `storage.Storage` 接口添加 `PutIfAbsent` 方法
- 为每个后端实现 `PutIfAbsent`（local、S3、OSS、COS；对于不支持原子条件后端的回退）
- `repository.Object` 新增 `ContentHash`、`RefCount` 字段
- 新增 `repository` 方法：`GetObjectByContentHash`、`IncrementRefCount`、`DecrementRefCount`
- `FileService.Put` 中的去重逻辑（临时文件缓冲 → 哈希 → 查找 → 创建或共享）
- 硬删除路径：`FileService.HardDelete` → 检查 RefCount → 如果为 0 则删除 blob
- 通过每个存储桶配置弃用 SSE 存储桶的去重功能
- GC worker 用于陈旧的 content_hash 行（RefCount=0 但 blob 仍然存在）

**交付物：** 重复上传自动去重。存储成本降低 30-95%（取决于工作负载）。

**阶段 4（第 9-16 周）：方向五**

**里程碑：**
- 添加 `RegionID` 到配置和上下文
- 实现 `RegionTransport` 接口（从 HTTP 开始，NATS 可选）
- 用区域感知的事件传播替换 `PostgresTransport`
- 在 `Replication.Worker` 中实现双向复制
- 实现 LWW 冲突解决（blob）
- 实现 CRDT 映射合并（标签）
- 添加区域级领导选举（用于 Reconcile/GC 工作者）
- 跨区域读取一致性模型（最终一致性，可选强一致性头）
- 向存储桶配置添加区域复制策略
- 配置：`REGION_ID`、`REGION_PEERS`、`REGION_REPLICATION_MODE`（主动-主动 / 主动-被动）

**交付物：** 多区域主动-主动部署。任一区域的写入都会异步复制到所有其他区域。

### 风险与缓解措施

| 阶段 | 风险 | 概率 | 影响 | 缓解措施 |
|-------|------|---------|--------|-----------|
| 1 | 为 PDF 引入 CGO 依赖性 | 低 | 中 | 使用纯 Go 的 `pdfcpu`（无需 CGO） |
| 1 | Qdrant 过滤器与现有集合不兼容 | 中 | 中 | 为过滤器字段使用新的 payload 键，它们对于不存在 payload 键的现有点是可选的（Qdrant 优雅地处理缺失的键） |
| 2 | 写入路径上的缓存失效竞态条件 | 中 | 高 | 对缓存 + 存储操作使用互斥锁；接受最终一致性 |
| 3 | SSE 用户的去重行为不当 | 高 | 高 | 明确禁用已加密存储桶的去重，并发出警告日志 |
| 3 | `PutIfAbsent` 竞态条件（后端不支持原子条件操作） | 中 | 中 | 在 content_hash 行上使用数据库级唯一约束作为备份。在 SQL INSERT 失败时回退到共享。 |
| 4 | 跨区域网络分区导致数据不一致 | 中 | 高 | LWW 意味着分区期间的数据丢失是可以接受的。为审计日志添加版本向量以便在恢复后手动合并时使用。 |
| 4 | 跨区域延迟影响写入延迟 | 高 | 中 | 写入延迟 = 本地写入时间 + 异步复制。不加到关键路径。 |

---

## 6. 最终架构建议摘要

1. **存储库已经正确 —— 不要重写。** `Storage` 接口是稳定的。内容去重和缓存的装饰器模式保留了这一点，同时在不改变现有后端的情况下添加了功能。

2. **AI 管线接口需要演进，而不是革命。** `ExtractResult`（结构体）和 `SearchFilter`（可选参数）的添加与现有实现向后兼容。索引器或搜索中不需要新的抽象层。`RemoteExtractor` 协议需要版本控制（`/extract-v2`），但可以并行提供旧端点。

3. **最大的架构风险是去重 + SSE 矛盾。** 在文档中承认这一点（该需求文档已经做到），在决定不对 SSE 存储桶启用去重时，要明确这一硬性边界。不要试图实现确定性加密，除非有一个明确的用例和针对该用例的加密审计。

4. **缓存和去重是独立的，但写入路径的变更顺序最为关键。** 先缓存后去重，这样两个团队都可以在 `storage.Storage` 接缝上独立工作，而不需要合并写入路径变更。

5. **主动-主动应该是最后一个。** 这不是 2026 年下半年 80% 以上部署的需求。在第一个版本中用 HTTP 转发（而非 Kafka/NATS）实现它，以保持操作简单。对于 2027 年的部署，将其作为可选的生产级传输保留 NATS。

6. **并行开发工作。** 方向一和方向二（阶段 1）没有依赖关系 —— 两个团队（或一个人并行开发）可以同时处理它们。这是整个路线图中最大的效率机会。
