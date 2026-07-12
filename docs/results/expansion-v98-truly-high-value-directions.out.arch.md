现在我已全面掌握了代码库现行状态和分析文档。以下是我的架构分析。

---

# 架构分析：多模态 AI、元数据搜索、去重、缓存和主动-主动复制

## 1. 架构评估：当前设计的优势与局限

### 优势

分析文档中确定的五个方向均**在现有 seam 上正确选择**。代码库的架构展现了值得认可的约束分离——这是验证每个提议可行性之前需要先说明的背景。

1. **Seam 驱动的可扩展性** —— `VectorIndex`（`internal/ai/vectorindex.go`）和 `ChunkSink` 接口使嵌入的读写路径可以互换。`QdrantIndex` 同时实现两者，通过 `NewSearch` → `WithVectorIndex` 注入，`Search` 服务本身保持不变。这就形成了一个结构良好的扩展模式。

2. **Storage 接口足够泛化** —— `Storage`（`internal/storage/storage.go`）作为稳定边界，涵盖 `Put`、`Get`、`Stat`、`Delete`、`List`、`PresignGet`、`PresignPut` 和多分片上传方法。任何缓存或内容寻址包装器都可以在此接口之下透明地组合使用。

3. **事件驱动管道工作流** —— `EventBus` + `JobPool` 解耦提取/嵌入/索引工作。`Indexer` 在处理前通过 `Enqueuer` 接口将工作排队——这为异步多模态管道提供了自然的基础。

4. **Opt-in 安全默认** —— AI 管道、pgvector、Qdrant、复制均为开关控制，默认关闭。这与分布式系统幂等的 Twelve-Factor 原则相符。

5. **无全局状态** —— 无全局变量、无单例注册表、无隐藏的 init() 依赖。所有内容通过依赖注入在 `main.go` 中显式连接。这使组件级测试和并行未来实现变得简单。

### 关键限制

分析文档正确地识别了这些，但我在代码中确认了几个值得强调的其他限制：

1. **`Extractor` 接口过于受限** —— `Extract(ctx, contentType, reader) (string, error)` 只返回纯文本，无法传递结构化元数据。任何多模态处理（OCR、转录、表格提取）都会丢失结构化信息。PDF 提取不仅返回文本，还返回布局信息、页面分割和表格结构——在当前设计中这些信息没有出口。

2. **`Search.Query` 没有上下文感知** —— `Request` 结构体包含 `Tenant`、`Bucket`、`Query`、`K`、`Mode`。没有 `Filter`、`Facet`、`Sort`、`Cursor` 或 `Pagination`。这意味着所有搜索都是“全局语义” → 在向量后端裁剪。这在可用性上是一个根本限制——不仅在功能上受限，而且对于层级式数据组织，搜索**必须**与元数据约束结合才能达到可用的精度。

3. **复制是单向且仅限于 blob** —— `replication.Worker` 从源 blob 存储读取并写入目标。元数据（标签、ACL、存储类别、桶配置）不会跨区域追踪。当前没有冲突解决的概念——如果同一个对象在两个区域同时被修改，复制路径简单地覆盖。

4. **事件总线仅限于单区域** —— 即使有 `PostgresTransport`，LISTEN/NOTIFY 也不能跨越 WAN 延迟。没有跨区域事件转发机制，因此任何主动-主动复制方案都需要新的传输层。

5. **缩略图处理每次重新计算** —— `thumbnail.Generate` 每次从存储读取原始图像并重新生成。没有以 `{storageKey}_{w}_{h}` 为键的产物缓存。虽然不算严重问题，但这是分析文档有效识别的性能问题。

### 架构决策评估

| 决策 | 评估 | 上下文 |
|--------|----------|---------|
| `Extractor` 返回纯文本 | **合理，但限制性过强** | 适用于 2024 年的初始范围；当前需求要求结构化输出 |
| `Search.Query` 保持最小化 | **合理，作为 1.0 版本** | 缺少 filter 现在是一个功能差距；向量后端的过滤应该下推，而非在应用层实现 |
| `ScopeFilter` 在 Qdrant/pgvector 中硬编码 tenant+bucket | **正确的安全边界** | 展开时，这必须扩展以纳入用户提供的 filter 条件，同时始终保持 tenant 隔离 |
| 复制为单向且无冲突处理 | **正确的 1.0 版本** | 主动-主动是下一个合理的演进方向 |
| local FS + SQLite 作为默认基线 | **对 CI 至关重要** | 测试必须零网络、零 Docker、零外部依赖 |

---

## 2. 扩展方向：架构深入分析

### 方向一：多模态 AI 管线（P1）

#### 为什么需要

文档中的业务论证是合理的。从架构角度看，核心动机是：**当前的 `Extractor` 接口是一个信息瓶颈**。它返回 `string`，缺少元数据、分段信息、结构化和置信度分数。这意味着：

- PDF 表格提取 → 丢失单元格坐标和页眉
- 图像 OCR → 丢失文本位置、语言和置信度
- 音频转录 → 丢失说话人分割和时间戳
- 电子表格提取 → 丢失行列结构

此管线产生的每个下游工件（chunk、embedding、search hit）都受到此信息瓶颈的约束。

#### 核心技术挑战

1. **`ExtractResult` 设计** —— 返回类型必须同时满足多种模态，而无需为每种模态提供特殊字段。类似 `map[string]any` 的解决方案对静态类型的 Go 代码来说过于灵活且不安全。文档中类型化的 `ExtractResult` 结构体是正确的方案，但 `Segments` 字段暗示时间感知分段——这是音频/视频处理的特性，可能不应该进入通用结果类型。

   **替代方案：** 使用已检查的联合体（sum type）模式——每个模态一种类型，共享公共字段：

   ```
   ExtractResult = TextResult | OCRResult | TranscriptionResult | SpreadsheetResult | StructuredResult
   ```

   在 Go 中，这意味着一个带有类型鉴别器的接口：

   ```go
   type ExtractResult interface { resultKind() string }
   type TextResult struct { Text string; Metadata map[string]any }
   type OCRResult struct { Text string; Language string; Segments []OCRSegment }
   type TranscriptionResult struct { Text string; Segments []TranscriptSegment; Language string }
   ```

   这强制开发者明确处理模态，但增加了调度复杂性。推荐**扩展的简单结构体**方法作为 V1，因为它可以演化——`ExtractResult` 中的可选 Segment 字段在不破坏接口的情况下处理大多数情况。

2. **流式处理 vs. 支持完整读取** —— 大多数提取器（OCR、音频转录）需要完整的输入流以进行上下文分析。你不能逐步 OCR 图像或管道转录音频。这意味着 `Extract` 必须能够处理完整的输入流（通过 `io.ReadAll` 或限制读取器）。文档建议的 50MB 限制对音频/视频来说可能太严格——3 小时 128kbps 的 MP3 约为 ~170MB。

3. **远程提取器协议演进** —— 当前 `RemoteExtractor` 发送 `(contentType, body) → plainText`。扩展到结构化输出意味着升级 HTTP 协议以返回 JSON `ExtractResult`，同时保持向后兼容原有端点。建议的协议：`POST /extract` 接受 `Content-Type: multipart/form-data`，返回 `Content-Type: application/json` 的 `ExtractResult` 体。

#### 预期的架构变更

| 变更 | 范围 | 影响 |
|---------|-------|--------|
| `Extractor` 返回 `ExtractResult` | 接口变更 | 中断所有现有提取器实现 |
| 新的提取器（PDF、图像、音频、电子表格） | 新的包或外部集成 | 低（新文件） |
| `chunker` 消费 `ExtractResult.Segments` | `internal/ai/chunker.go` | 中等——分段感知的分块 |
| 索引器写入 `Structured` 字段用于搜索 | repository schema | 低——新的可选列 |
| `RemoteExtractor` 协议升级 | HTTP 接口 | 向后兼容原有端点 |

#### 对现有系统的影响

对未使用多模态的租户和用户没有影响。新提取器是附加注册，非取代。默认提取器保持为纯文本。

由于 `Extractor` 接口变更，现有提取器需要最小适配——将现有 `(string, error)` 签名包装为 `(ExtractResult{Text: str}, err)`。

**风险：** 结构化元数据的索引模型——`ExtractResult.Structured` 包含嵌套映射，不能轻易转换为关系型 `WHERE` 子句。在早期阶段，最好将结构化字段索引为增强的搜索文本，而不是尝试关系型过滤。

---

### 方向二：元数据锚定语义搜索（P1）

#### 为什么需要

作为文件平台，搜索不仅需要语义相关，还需要**精确**。没有元数据过滤，搜索变成了场景问题——"查找财务 PDF" 返回所有包含"财务"语义的内容片段，无论时间、类型或来源如何。

文档对此理由充分。下面我从架构而非产品角度进行分析。

#### 核心技术挑战

1. **过滤下推 vs. 应用层后过滤**

   这是根本性的设计决策：

   | 方法 | 优点 | 缺点 |
   |--------|--------|-------|
   | **下推到向量后端**（Qdrant 的 filter、pgvector 的 WHERE） | 预过滤可减少向量搜索空间；Qdrant 的 filter 在索引级别过滤；后过滤无法避免的脏数据问题已消除 | Qdrant 需要每个过滤条件有一个 payload 字段；pgvector 需要对 filter 列有复合索引 |
   | **应用层后过滤**（先向量搜索，然后按 metadata 过滤） | 无需更改 chunk schema；在纯文本/B/M25 模式下工作 | 浪费——向量搜索可能返回 100 个结果，过滤后只剩 1 个；搜索无效 chunk 的比例达到 50:1 |

   **推荐：** 对于 Qdrant 和 pgvector 进行**下推**，对于内存/B/M25 索引进行**后过滤**。原因是 Qdrant 的 filter 在所有点上预过滤，完全避免了脏数据问题。pgvector 通过具有 `(tenant, bucket)` + filter 列的复合索引支持 WHERE 下推。

2. **Chunk 写入时 metadata 嵌入 vs. 运行时 Join**

   | 方法 | 优点 | 缺点 |
   |--------|--------|-------|
   | **写入时嵌入过滤字段** | 向量后端的 filter 在索引级别工作；无运行时 Join 开销；无耦合到 objects 表的 schema | 如果 Object 的 metadata 变更，chunk metadata 会过期（例如，重命名标签） |
   | **运行时 Join objects 表** | 无过期 metadata；Filter 在对象当前状态上应用 | 对每个搜索代价高昂；在向量搜索 *之后* 过滤向量之间需要 N+1 Join 或 IN 子句 |

   **推荐：** 两种方法混合。对于搜索时间敏感的字段（`content_type`、`storage_class`、`created_at`），写入时嵌入到 Qdrant payload / pgvector 列中。这些字段很少变更。对于经常会变的 Tags，使用写入时嵌入 Qdrant，但以 objects 表作为标签真相来源，通过定期重新索引使 chunk metadata 与对象状态保持同步。

3. **搜索结果的 Facet 支持**

   当用户过滤时，他们还需要关于**可用过滤值**的反馈——"还有哪些标签匹配？"、"日期范围是多少？"。这需要每个搜索的 facet 聚合，这反过来意味着 Qdrant 的 `aggregate` API 或 pgvector 的 `GROUP BY`。分析文档没有提到 facet，但对于企业搜索体验来说这是关键。

#### 预期的架构变更

| 变更 | 范围 | 影响 |
|---------|-------|--------|
| `Search.Request` 中的 `Filter` 字段 | `internal/ai/search.go` | 向后兼容（零值时忽略） |
| `VectorIndex.SearchVectors` 中的 `Filter` 参数 | `internal/ai/vectorindex.go` | 需要新的接口签名或可选的 filter |
| Qdrant `scopeFilter` → 可组合的条件 | `internal/ai/qdrant.go` | 中等——从硬编码条件到动态 filter 构建器 |
| pgvector WHERE 条件构建 | `internal/ai/pgvector.go` | 中等——安全地构建动态 WHERE 子句（无 SQL 注入） |
| REST API 的 filter 参数 | `internal/api/rest/search.go` | 低——新 JSON 字段 |
| Chunk schema 中的过滤字段 | 写入时 + Qdrant payload / pgvector 列 | 低——附加字段 |
| Facet 支持 | 可选——新的 `Facet` 字段 | 中等——Qdrant facet 支持 |

#### 接口设计挑战

`VectorIndex.SearchVectors` 当前签名为：

```go
SearchVectors(ctx, tenant, bucket string, query []float32, limit int) ([]SearchHit, error)
```

添加 filter 参数最包容的方式是使用可选选项结构体：

```go
type SearchOptions struct {
    Bucket string
    Filter *SearchFilter // nil = no filter (backward compat)
    Limit  int
}

func (vi *QdrantIndex) SearchVectors(ctx, tenant string, query []float32, opts SearchOptions) ([]SearchHit, error)
```

这避免了签名膨胀，并为每个新选项保持向后兼容。

---

### 方向三：内容寻址存储与块级去重（P1）

#### 为什么需要

文档的存储成本分析是正确的——但还有一个我注意到但文档未充分强调的架构优势：**内容哈希作为天然的对象标识符**，简化了复制和缓存。如果你知道同一内容的 SHA-256 在全球所有节点上完全相同，你就可以：

- 在区域间通过 content hash 安全地复制 blob（跳过已存在的）
- 通过 content hash 缓存（缓存键与存储后端无关）
- 在两阶段写入中验证写入完整性（客户端发送 content hash 作为承诺）

#### 核心技术挑战

1. **鸡生蛋问题：哈希需要所有数据，但数据需要写入后才能读取**

   Document 的 `teeReader` + 临时文件方法是正确的，但引入了复杂性：
   - **流式上传：** 客户端发送内容 → 你将数据写入临时文件 → 计算哈希 → 检查已有 → 要么删除临时文件（命中）要么移动/上传（未命中）
   - **流式开销：** 对于大文件（>5GB），临时文件 I/O 加倍
   - **上游性能：** 读取器来自 S3 `MultipartUpload` 最终读取——你不希望缓冲完整文件

   **缓解措施：** 使用分块内容哈希（Merkle tree）。客户端发送分块指纹宣言 → 服务器只上传缺失的分块。这是 git 的行为方式。对于 1.0 版本，保留完整文件哈希 + 临时文件；对于 2.0 版本，为流式上传实现分块级。

2. **SSE + 去重矛盾**

   文档正确识别了 AES-SIV（确定性加密）作为解决方案。但还有一个更简单的方案：**在加密之前进行内容寻址**。

   架构模型应为：

   ```
   客户端明文 → SHA-256(plaintext) → [加密] → 密文 → 存储为 blob 键 content_hash
                                                              ↓
                                                     将 key 映射的记录
                                                     content_hash = sha256(plaintext)
   ```

   加密发生在内容寻址之后，因此去重引擎*知道*内容是重复的，即使密文不同。但这对共享 blob 没有帮助——每个客户的密钥不同，因此具有相同明文的不同客户需要不同的密文。

   **务实解决方案：** 仅对**未加密**的 bucket 启用去重，或者对**不加密**的工作负载启用。加密和去重的组合可以等待，因为它需要密钥管理编排（共享密钥用于去重 bucket，或按客户加密用于合规）。

3. **引用计数和 GC**

   删除路径必须变为：

   ```
   Delete → decrement refcount → if refcount == 0 → 删除 storage blob
   ```

   这引入了**竞态条件**：如果两个并发请求在同一个 blob 上，一个创建，一个删除，refcount 可能变为 0，而另一个请求认为它引用了一个存在的 blob。

   **缓解措施：** 使用 `SELECT ... FOR UPDATE` 或应用层 `refcount` 表的 `advisory lock`。Document 建议使用 `INSERT ... ON CONFLICT DO NOTHING` 的 `content_hash` 唯一约束——这对于创建是正确的，但删除需要仔细的悲观锁定。

#### 预期的架构变更

| 变更 | 范围 | 影响 |
|---------|-------|--------|
| `Storage` 中的 `PutIfAbsent` | `internal/storage/storage.go` | 新的可选方法；现有后端返回 `ErrNotImplemented` |
| Objects 表中的 `content_hash` 列 | repository schema + migration | 需要双迁移文件（sqlite/postgres） |
| `refcounts` 表 | 新的 repository 表 | 所有者：`repository` 包 |
| `Put` 路径中的去重逻辑 | `internal/service/file_crud.go` | 高影响——重新排序写入路径 |
| `Delete` 路径中的 GC 逻辑 | `internal/service/file_crud.go` | 高影响——条件性 blob 删除 |
| 加密协调 | `internal/storage` | 仅当 SSE 启用时 |

---

### 方向四：对象内容缓存层次（P2）

#### 架构格局

文档中的缓存方案建议使用包装器 `CachedStorage` + 可选的 CDN 层。我赞同这个方向，但想澄清一个细微的差别：**'storage.Storage' 接口包装器与 'FileService' 级缓存。**

文档推荐包装器方法：
```
Storage.Get → CachedStorage.Get → [cache hit? 返回] → 后端.Get
```

替代方法是在 `FileService` 级别缓存：
```
FileService.Get → [缓存命中? 返回] → Storage.Get → 由缓存更新
```

| 方法 | 优点 | 缺点 |
|--------|--------|-------|
| **Storage 包装器** | 对 FileService 透明；自动包装所有后端；易于测试 | 无法感知对象（无法根据 content-type 或大小有选择地缓存） |
| **FileService 级别** | 可以访问 Object metadata 用于缓存策略（例如，仅缓存 <1MB 的图像） | 需要修改 FileService；不缓存 S3 presign 路径 |

**推荐：** 两者都用——在 Storage 级别进行泛型读-写缓存（基于 `Cache-Control` 标头和大小阈值），在 FileService 级别进行特定的策略驱动缓存（缩略图、高频文件）。

#### 核心挑战：缓存一致性

没有一致性协议，写入不会使缓存失效：

```
1. Client A 写入 PUT /doc.pdf  → 更新 Storage → 缓存保持不变（包含旧内容）
2. Client B 读取 GET /doc.pdf  → 缓存的读命中 → 返回旧内容
```

**解决方案：** `CachedStorage` 包装器**必须**也包装 `Delete` 和 `Put`，使每个写入的 `storageKey` 失效：

```go
func (c *CachedStorage) Put(ctx, key string, r io.Reader, size int64, opts PutOptions) (ObjectInfo, error) {
    info, err := c.backend.Put(ctx, key, r, size, opts)
    if err == nil {
        c.local.Remove(key) // 使本地缓存条目失效
    }
    return info, err
}
```

这对于内存/L1 缓存有效，但对于分布式缓存（Redis、CDN）需要更复杂的失效协议（主动失效或 TTL 超时）。

#### CDN 集成

文档建议 `PresignGet` → CDN URL。这是一个好模式，但 CDN 集成通常需要专用 DNS + 证书编排，这超出了 Storage 接口的范围。

**建议：** 使 CDN 集成成为可选的 `service` 层特性，而非 `storage` 层特性。`FileService.Get` 后跟一个 `cdn.Prefetch` 调用，或者 presign 端点使用 CDN 域替换 storage 域。

---

### 方向五：主动-主动多区域复制与冲突解决（P2）

#### 架构格局

这是变更面最大、风险最高的方向。正确实现需要三个不同的系统层：

| 层 | 当前状态 | 期望状态 |
|-------|-------------|--------------|
| **元数据（repository）** | 单 DB | 每个区域一个 DB，变更数据捕获（CDC）复制 |
| **Blob（storage）** | 单向 blob 复制 | 双向 blob 复制（双向） |
| **事件（event bus）** | 单区域 Postgres LISTEN/NOTIFY | 跨区域传输（Kafka、NATS 或基于 HTTP 的转发） |

#### 核心架构决策：无共享 vs. 区域对

| 模型 | 优点 | 缺点 |
|---------|--------|-------|
| **无共享**（每个区域完全独立，CDC 双向同步） | 全区域可用（任何区域死亡，其他区域存活）；每个区域无共享 DB | 复杂的数据管道；需要冲突解决；RPO=传输延迟 |
| **区域对**（主备配对，备用区域只读转发到主区） | 更简单的冲突模型（始终一主）；更简单的事件组播 | 任一区域死亡意味着 50% 集群不可用；转发延迟 |
| **读副本**（写入全部到一个主区，读取从每个区域） | 最简单的数据管道；无冲突；S3 一致 | 对写入的单区域依赖；写入路径延迟 |

**推荐：** 从**区域对**或**读副本**开始（分析文档未提及），然后演进到**无共享**。原因是冲突解决是主动-主动最难的部分——使用 LWW 的简单模型（每个区域是自包含的，复制所有内容，后写入者覆盖）在缺乏版本向量完整实现的情况下容易出现数据丢失。

文档中的冲突模型比较是合理的。我想补充一个务实方案：

**对于 V1：最后写入者胜出 (LWW) + 墓碑**

```
区域 A 写入对象 X 于 T1 → 区域 B 写入对象 X 于 T2 (T2 > T1)
→ LWW 策略：T2 胜出 → 区域 A 覆盖其本地 X 为 T2 值
→ 区域 B 删除对象 X 于 T3 → LWW 策略：删除胜出
→ 墓碑创建（标记为已删除）→ 在 grace period（15 分钟）后传播
```

这处理了 90% 的情况。对于时间戳无法解决的冲突（例如，区域 A 和 B 在完全隔离时写入），版本向量并显式介入可以稍后添加。

#### 跨区域事件传输

当前 `PostgresTransport` 不适合 WAN。推荐路径：

1. **V1：基于 HTTP 的区域间转发** —— 每个区域暴露 `/v1/events/forward`，接收其他区域的事件。简单、可测试、零依赖。
2. **V2：Kafka/NATS 用于高吞吐量** —— 当区域超过 3 个时，多对多事件组播变得复杂；Kafka 作为全球聚合器简化了拓扑。

#### 预期的架构变更

| 变更 | 范围 | 影响 |
|---------|-------|--------|
| 每个区域的 Repository | 全局——需要 DB 集群 | **架构变更——风险最高** |
| CDC 管道（Debezium、pglogical） | 新包或在 `repository` 内 | 高——需要 DBA 专业知识 |
| 冲突解决（LWW + 可选 IVC） | `internal/replication/conflict.go` | 中等——新包 |
| 跨区域事件传输 | `internal/events/transport.go` | 中等——新包 |
| 配置：`REGION_ID`、`PEER_REGIONS` | `internal/config/` | 低 |

---

## 3. 接口设计建议

### 关键原则

1. **对扩展开放，对修改封闭** —— 每个接口变更必须针对新的实现（而不是重构现有实现）。文档正确识别了这一点，但值得明确：
   - `Extractor` → 添加 `ExtractResult`，不改变纯文本提取器
   - `VectorIndex.SearchVectors` → 添加可选的 `SearchOptions`，不需要 Qdrant/pgvector 实现
  
2. **保持向后兼容** —— 任何 JSON/REST API 变更必须使用 `omitempty` 和可选字段。对同一 API 版本不破坏现有客户端。如果无法避免，延长 API 版本（`/v2`）。

3. **配置是接口** —— 布尔标志和字符串应通过配置公开，而不是在代码中硬编码。分析文档中的 `AI_EMBED_CACHE_SIZE` 模式是正确的。

### 推荐的抽象层

| 抽象 | 当前状态 | 建议 |
|------------|-------------|---------|
| **包级 `ai.ExtractResult`** | 不存在 | 新的返回类型，可选字段 |
| **包级 `ai.SearchFilter`** | 不存在 | 新的 filter 结构体 |
| **包级 `ai.SearchOptions`** | 不存在 | 可选选项结构体用于 `SearchVectors` |
| **`storage.CachedStorage`** | 不存在 | 新的包装器，可组合 |
| **`replication.ConflictResolver`** | 不存在 | 新的接口用于可插拔冲突解决 |
| **`storage.ContentHash`** 方法 | 不存在 | `Storage` 接口上的新可选方法 |
| **`events.Transport`** 接口 | 存在但仅限 `PostgresTransport` | 扩展为通用接口以实现 WAN |

### 应避免的事项

- 避免在 Filter 中使用 `map[string]any`——它导致运行时类型错误。使用类型化的 `SearchFilter` 结构体。
- 避免在 `ExtractResult` 中使用 `[]any`——模态特定类型更安全。
- 避免在提取器中使用共享状态——每个 `Extract` 调用应该能够独立运行，无并发锁定。

---

## 4. 技术选型

### 评估框架

每个新依赖项应根据以下条件进行评估：

| 标准 | 权重 | 注释 |
|---------|--------|---------|
| **零网络**依赖 | **强制** | 必须在仅测试 local FS + SQLite 的 CI 中工作 |
| **Apache 2.0 / MIT 许可** | 要求 | 不引入 AGPL/SSPL |
| **libc FFI 依赖** | 避免 | 必须使用静态编译的 Go 二进制文件 |
| **主动维护** | 要求 | 过去 6 个月内至少有版本发布 |
| **Go 标准库可用** | 偏好 | 如果可能使用 `net/http` + `encoding/json` |

### 每个方向的特定建议

**方向一（多模态管线）：**

| 能力 | 推荐 | 理由 |
|--------|--------|---------|
| **PDF 文本提取** | `github.com/ledongthuc/pdf`（Go 原生）或外部 `tika` | PDF 解析需要原生 C；Go 包避免了 FFI |
| **图像 OCR** | `gosseract`（Tesseract 绑定）或 `RemoteExtractor` → 云 | Gosseract 使用 libtesseract（FFI）——可能过于脆弱。**推荐：** 在容器中部署 `tesseract`，通过 `RemoteExtractor` 访问 |
| **音频转录** | `whisper.cpp` Go 绑定或外部 API | 无好的 Go 原生转录库。使用 `RemoteExtractor` + `whisper.cpp` 是务实方案 |
| **电子表格解析** | `excelize`（XLSX 解析器） | 纯 Go，MIT 许可，维护活跃 |
| **文档布局分析** | `unstructured.io` 作为远程提取器 | API 驱动，消除解析复杂 PDF 本地化的需求 |

**关键指导：** 尽可能使用 `RemoteExtractor` 模式。它保持 AI 层与重量级提取依赖分离，并使系统能通过简单 REST 调用集成商业 AI 服务（Azure Document Intelligence、Google Document AI）。

**方向二（元数据过滤）：**

| 技术 | 对于 Qdrant | 对于 pgvector |
|---------|----------------|----------------|
| **Filter 引擎** | Qdrant 原生 payload 过滤（推荐） | PostgreSQL `WHERE` 子句 |
| **全文/日期过滤** | Qdrant 的 range 过滤用于日期 | PostgreSQL 的 `BETWEEN` 用于日期 |
| **分面聚合** | Qdrant 的 `scroll` + 应用层聚合 | PostgreSQL `GROUP BY` |

**不需要新依赖。** Qdrant 和 pgvector 都已实现 filter 支持。只需要围绕 filter 构建逻辑添加 Go 包装器。

**方向三（内容去重）：**

| 技术 | 推荐 | 理由 |
|---------|--------|---------|
| **哈希算法** | SHA-256 | Go 标准库，业界标准，无依赖 |
| **分块哈希**（用于大文件） | `github.com/cespare/xxhash` 或 Go 的内置 `hash` | xxhash 对于大文件更快，但仅用于分块指纹。完整文件哈希使用 SHA-256 |
| **确定性加密** | `github.com/google/tink/go/aead` 使用 AES-SIV | Tink 是 Google 的加密库，AES-SIV 是确定性加密的推荐方案 |
| **引用计数** | objects 表中的 `refcounts` 表 | 无依赖——纯 SQL |

**不需要新依赖。** Go 的 `crypto/sha256` 足够用于 V1。

**方向四（对象缓存）：**

| 技术 | 推荐 | 理由 |
|---------|--------|---------|
| **内存缓存** | `hashicorp/golang-lru` | 并发安全，固定大小，O(1) 操作 |
| **本地磁盘缓存** | `allegro/bigcache` 用于进程内，或 `dgraph-io/badger` 用于持久化 | BigCache 是零 GC 的，对于对象缓存更简单。Badger 提供持久化。对于 V1，如果不需要进程重启持久化，推荐 BigCache |
| **CDN 集成** | CloudFront/CloudFlare API | 无 Go 依赖——仅 presign URL 转换 |

**推荐：** 从 `golang-lru` 用于内存缓存开始。它简单、经过实战测试、非侵入性。直到你需要跨实例一致性（例如，多 pod 部署）之前，不需要 Redis。

**方向五（主动-主动）：**

| 技术 | 推荐 | 理由 |
|---------|--------|---------|
| **跨区域事件传输** | 基于 HTTP 的转发（V1），Apache Kafka（V2） | Kafka 用于 > 3 区域的拓扑 |
| **CDC 复制** | `debezium` + Kafka Connect（Postgres 来源） | Debezium 是标准工具。需要运维开销 |
| **冲突解决** | 自定义 LWW + metadata CRDT | 无依赖——纯 Go 逻辑 |
| **跨区域监控** | 区域级 Prometheus + 全局联合 | 无新依赖——Prometheus 已就位 |

### 自建 vs. 采购决策

| 能力 | 自建 | 采购/集成 | 决策 |
|----------|--------|----------------|--------|
| PDF 提取 | 使用 Go PDF 库解析 | `RemoteExtractor` → Azure/AWS/Google Document AI | **自建（定制）**——对于 OSS/私有部署无需外部 API |
| 图像 OCR | 使用 Tesseract（libc 约束） | `RemoteExtractor` → 云 Vision API | **集成**——将 Tesseract 部署在 sidecar 容器中 |
| 音频转录 | 使用 whisper.cpp | 外部 API（Deepgram、Rev） | **集成**——whisper.cpp 在容器中，通过 `RemoteExtractor` |
| 内容去重 | 核心存储逻辑 | 无采购选项 | **自建**——需要核心存储更改 |
| 缓存层 | 使用 Go 库构建 | Redis、Varnish | **自建**于 Storage 接口之上，可平移至 Redis |
| 跨区域复制 | CDC 管道 + 冲突解决 | Confluent Cloud、AWS DMS | **混合**——CDC 使用 Debezium（不可省略），事件组播采用自建 HTTP 转发 |

---

## 5. 实施路线图

### 优先级

| 优先级 | 方向 | 理由 |
|----------|-----------|---------|
| **P0** | 方向二：元数据锚定搜索 | 最高 ROI；对搜索体验的根本促进；最低风险（现有向量后端的附加功能） |
| **P0** | 方向一：多模态管线 | 产品差异化；扩展提取器不破坏纯文本提取；并行开发 |
| **P1** | 方向三：内容去重 | 显著的成本影响；核心存储变更需要仔细测试；顺序上需要独立工作 |
| **P1** | 方向四：对象缓存 | 性能和成本；方向三完成前的独立工作 |
| **P2** | 方向五：主动-主动 | 最复杂的变更面；所有其他方向完成后 |

### 阶段划分

**阶段 1（第 1-2 周）：元数据锚定搜索**

| 里程碑 | 可交付物 |
|-----------|-----------|
| M1.1 | `SearchFilter` 结构体 + `SearchOptions` 添加到 `VectorIndex.SearchVectors` |
| M1.2 | `QdrantIndex.SearchVectors` 中的 filter 下推 |
| M1.3 | `PgVectorIndex.SearchVectors` 中的 WHERE 子句 filter |
| M1.4 | REST API `/v1/search` 接受 `filter` 参数 |
| M1.5 | 向后兼容性测试——当 filter 为空时零行为变化 |
| M1.6 | Chunk 写入时 payload enrichment——在索引器写入路径中填充过滤字段 |

**阶段 2（第 3-6 周）：多模态提取**

| 里程碑 | 可交付物 |
|-----------|-----------|
| M2.1 | `ExtractResult` 结构体 + `Extractor` 接口迁移 |
| M2.2 | PDF 提取器（`ledongthuc/pdf`） |
| M2.3 | 远程提取器协议升级 → `{text, metadata, segments, structured}` |
| M2.4 | 分段感知分块器（按 Segment 边界分块） |
| M2.5 | 索引器写入 `ExtractResult.Structured` |
| M2.6 | 为 OCR/音频/电子表格提取器预留扩展点 |

**阶段 3（第 7-10 周）：内容寻址存储**

| 里程碑 | 可交付物 |
|-----------|-----------|
| M3.1 | `content_hash` 列 + `refcounts` 双迁移 |
| M3.2 | 基于临时文件的哈希管道用于上传 |
| M3.3 | `Put` 路径中的去重检查 + 引用计数 |
| M3.4 | `Delete` 路径中的 GC 逻辑（refcount → 0 → 删除 blob） |
| M3.5 | 并发控制——`refcounts` 的悲观锁定 |
| M3.6 | 条件性 SSE + 去重冲突（仅非加密） |
| M3.7 | 版本化 bucket 禁用去重 |

**阶段 4（第 11-14 周）：对象缓存**

| 里程碑 | 可交付物 |
|-----------|-----------|
| M4.1 | `CachedStorage` 包装器实现（L1 内存 LRU） |
| M4.2 | L2 磁盘缓存（`bigcache` 或 `badger`） |
| M4.3 | 写失效——`Put`/`Delete` 包装的清空缓存 |
| M4.4 | CDN presign 集成（CDN 域替换） |
| M4.5 | 缩略图缓存以 `{storageKey}_{w}_{h}` 为键 |
| M4.6 | 条件性 SSE 绕过缓存 |

**阶段 5（第 15-24 周）：主动-主动多区域**

| 里程碑 | 可交付物 |
|-----------|-----------|
| M5.1 | 每个区域的 DB 集群 + CDC 管道（Debezium + Kafka） |
| M5.2 | 跨区域事件传输（V1：HTTP 转发） |
| M5.3 | LWW 冲突解决引擎 |
| M5.4 | 墓碑 + 优雅期用于删除 |
| M5.5 | 为跨区域双向复制扩展的 `replication.Worker` |
| M5.6 | 通过 `X-Aero-Consistency-Level` 标头的读取一致性控制 |
| M5.7 | 区域健康检查 + 故障转移管道 |
| M5.8 | 启动时全量初始同步 |

### 风险与缓解

| 风险 | 方向 | 可能性 | 影响 | 缓解 |
|------|-----------|----------|---------|-------------|
| **提取器接口中断** | 方向一 | 中等 | 高 | V1 保持纯文本提取器不变；仅扩展签名 |
| **Qdrant filter 兼容性** | 方向二 | 低 | 中等 | 针对 Qdrant 1.7+ filter API 编写测试 |
| **去重 + SSE 索引冲突** | 方向三 | 高 | 高 | 开始时不加密 bucket；稍后通过 AES-SIV 迁移 |
| **GC 竞态条件** | 方向三 | 中等 | 高 | `refcounts` 上的悲观行级锁定；测试并发工作负载 |
| **非敏感路径上的缓存污染** | 方向四 | 低 | 中等 | TTL + 写入失效；基于 Content-Type 的粒度控制 |
| **跨区域网络分区** | 方向五 | 高 | 高 | 为部分故障设计（每个区域独立运行）；故障后手动仲裁 |
| **CDC 延迟** | 方向五 | 中等 | 中等 | 通过 RPO 延迟警报监控 Prometheus；可调同步间隔 |
| **团队不熟悉所选技术** | 方向一、五 | 中等 | 中等 | 在容器化 sidecar 中包装 Tesseract/whisper.cpp；与 CDC 管道分离 |

---

## 6. 结论与建议

### 当前架构的健康状况

代码库显示了深思熟虑的架构设计。接口边界（`Storage`、`VectorIndex`、`Extractor`、`EventSink`）定义良好。提议的五方向代表了自然的演进——它们不是改造，而是在结构和接口层面自洽的扩展。

### 关键建议

1. **开始方向二** —— 元数据过滤是对搜索体验的根本促进，需要最少的架构变更。它教会了团队关于 Qdrant/pgvector filter 语义的知识，这些知识在方向五中需要。

2. **使用方向一进行产品差异化** —— 多模态管线是真正的产品护城河。大多数对象存储 + AI 搜索产品只处理文本。视觉、音频和结构化文档提取是值得投资的显著差异点。

3. **不要在方向三的加密问题上纠缠** —— 去除 SSE 约束。先实现未加密工作负载的去重。加密 + 去重可以在以后解决；密钥管理是一个完全不同的问题。

4. **方向四使用简单缓存，完成后演进** —— `golang-lru` 用于内存缓存。不需要 Redis 用于 V1。如果需要跨实例缓存，稍后更换实现。

5. **方向五使用区域对而非无共享** —— 对于 2 区域拓扑，区域对更简单。如果增长到 3+ 区域，再演进到无共享。

### 最终的再次

这份分析文档是对 `aero-vault` 代码库最全面、代码锚定最准的扩展分析之一。我读过的 97+ 文档中，这是第一个在以下方面都达到高水平的产品：

1. 逐个文件的代码锚定
2. 现实的权衡分析
3. 边界情况和失败模式
4. 并行工作的实施顺序

文档中建议的顺序——方向二 → 方向一 → 方向三 → 方向四 → 方向五——是正确的，通过 P0 元数据过滤 + 多模态提取实际上可以作为独立路径并行开发。
