好的，以下是我基于这两份文档的深入架构分析。

---

# 架构分析报告：AI 原生智能管线断层、近数据计算与数据血缘基础设施

## 1. 架构评估

### 1.1 当前架构的优势

文档揭示的当前架构有其清晰的设计哲学，值得肯定：

- **分层解耦完成度高**：`Extractor → Chunker → Embedder → Index → Search → Chat` 管线各阶段独立，接口清晰（尽管单实例），属于"简单正确"的架构风格。`internal/ai/` 包的包内耦合度低，这给方向一的策略注入提供了良好基础。

- **警觉的预留设计**：`NotificationRule.LambdaARN` 尽管 unused + kept for compat，但其字段存在表明早期团队预见了事件驱动的函数计算扩展点。S3 parser 层已包含 `LambdaFunctionConfiguration` 的入站解析逻辑，只缺消费端。这种"先解析、存下来、以后再消费"的预留模式在扩展时能省去协议层的重复改造。

- **错误处理模式成熟**：`telemetry.IncIndexerSkip` 带 labeled counter、embedder/llm/reranker 的 `nil` 安全模式、索引器跳过非致命化、reranker 降级为 warn 日志——这些都是经过生产压力验证的错误隔离模式。方向一/三/五都可以复用同一套 skip/meter 惯用法，无需重新设计错误策略。

- **Indexer 的状态机设计正确**：`processOne` 中的 `handleExtractError`、chunk 后的 embed 调用、以及已有的 `ReindexStale` 模型漂移检测路径——这些为方向一的 chunk 策略变更提供了重索引通道，不需额外设计数据迁移策略。

### 1.2 架构的局限性

- **Chunker 返回值类型设计失配**：`.out.md` 补充发现——`Chunker.Chunk()` 返回 `[]string` 而非 `[]Chunk`。这个设计决策使得下游 `Embedder` 和 `Indexer` 无法区分 chunk 的来源策略、无法携带元数据（chunk offset、content-type、strategy name），形成信息黑洞：
  ```
  Chunker ──→ `[]string`（损失：contentType, offset, strategyName）
                │
                ▼
             Embedder ──→ 无法按模型区分
                │
                ▼
             Indexer ──→ 无法按策略标记
  ```
  这是方向一当前的最大技术债——即便引入多策略，返回值协议也必须同步升级。

- **单实例单模型的选择是"过早固化"**：`main.go` 中 `buildEmbedder` → `buildLLM` → `buildReranker` 的装配路径全部返回单一实例的指针，`AIConfig` 的字段也全部是单值。这不是抽象层缺失（方向三的 `ModelRouter` 可以包装），而是**装配点的硬编码**——真正的问题是 `Search` 结构体直接内嵌 `embedder Embedder` 字段，而非持有一个选择器。这意味着修改路由需要改 Search 的构造逻辑和成员字段，扩展点多处散布。

- **血缘与对象模型耦合过紧**：`GET /v1/lineage/objects/{id}` 的实现路径是 REST handler → repo → usage 表——血缘信息被映射为"谁看了什么"的审计日志，而非"数据从哪里来、经过什么变换、到哪里去"的图结构。这是语义层面的架构债务：代码命名（`lineage`）与用户体验（`usage` 表）已经不一致，扩展时可能产生命名混乱。

### 1.3 值得关注的架构债务

| 债务项 | 位置 | 影响范围 | 清偿窗口 |
|--------|------|---------|---------|
| `Chunk` 被退化为 `string` | `chunker.go` | 方向一、三、四 | 方向一 MVP 时 |
| Search cache key 不含 `embed_model` | `search.go` | 方向三 | 方向三设计时 |
| `ChunkCleaner.DeleteObjectChunks` 物理删除 chunk | `service/` | 方向四 | 方向四 MVP 前 |
| 事件总线 subscriber 列表硬编码 | `main.go` | 方向二 | 方向二 MVP 时 |
| 租户认证信息未透传到 JobPool | `jobs.go` | 方向二 | 方向二设计时 |
| SSE 对象标记 `dedup_eligible` 未预留 | `storage/` | 方向五 | 方向一/二增量中 |

这些债务的共同特征是：如果不提前设置预留字段或接口占位，后续扩展会在多个包中来回修改（shotgun surgery）。建议在方向一 MVP 期间一并清偿 `Chunk` 类型和 cache key 两项，成本极低。

---

## 2. 扩展方向

### 方向 A（重优先级）：可分块策略引擎 + 分块元数据协议升级

#### 为什么需要

分块质量和 chunk 元数据是 RAG 管线的"第一公里"——向量召回的上限受限于 chunk 内容的语义完整性。当前固定窗口策略无差别地对待代码、散文、表格、日志，导致下游嵌入和检索的天花板被预先锁定。

更关键的是：`[]string` 返回值丢失了所有上下文——无法追溯 chunk 来源文档偏移、无法区分策略来源、无法在血缘中建立 chunk → embedded 的映射。方向四的血缘基础设施需要稳定的 chunk 标识符，方向三需要按策略拆分 embedding pool。这些都是当前返回值协议无法支撑的。

#### 核心挑战

1. **返回值协议反向兼容**：现有调用者（Indexer、Search）全部消费 `[]string`。升级到 `[]Chunk`（含 ID、offset、strategyName）需要修改所有消费方而不破坏现有逻辑。
2. **语义边界检测的精度**：`SentenceWindow` 策略需要句子边界检测——中文的分句（按句号/问号/感叹号/换行）和英文（按句点+空格+大写）规则不同。是否引入 NLP 依赖？
3. **策略选择的信息来源**：`Extractor` 当前不传递 content-type。策略选择的信息输入在哪里引入——是 Extractor 接口新增返回值，还是 Indexer 从对象元数据的 MIME type 推断？

#### 架构变更

```
当前：
Extractor(string) → Chunker(string, window, overlap) → []string

变更后：
Extractor(string, objectMeta) → {text string, contentType string}
                                  │
                                  ▼
ChunkPipeline.selectStrategy(contentType)
                    │
                    ▼
    ChunkStrategy.Chunk(text, opts) → []Chunk
                                       │
                                       ├── ID: uuid/v7
                                       ├── Text: string
                                       ├── Offset: int
                                       ├── ContentType: string
                                       └── StrategyName: string
```

#### 对现有系统的影响

- `Indexer.processOne` 需适配 `[]Chunk` 而非 `[]string`（改动集中，低风险）
- `Extractor` 接口需新增 content-type 传递通道（可扩展 `ChunkOptions` 结构体而非改接口签名）
- 向量索引的 metadata 字段需新增 `chunk_info` JSON 字段（可选，向后兼容）
- `ReindexStale` 可复用——更换策略后只需标记对象为 stale

---

### 方向 B：Model Router 与查询维度感知路由

#### 为什么需要

从 SaaS 运营角度看，AI 成本是第二大支出（仅次于存储）。小查询用大模型、免费租户用开源模型、复杂合同用高性能模型——这些是商业层的必然需求。从可靠性看，单模型依赖是单点故障——`AI_CHAT_PROVIDER` 指向的 provider 中断 == 全部 AI 功能不可用。

文档和 .out.md 都验证了：当前代码的单一实例不是抽象层缺失，而是**装配时通过配置选择单模型，而非运行时通过路由选择多模型**。

#### 核心挑战

1. **嵌入维度不一致**：不同 Embedder 模型输出不同维度——`text-embedding-3-small`（1536 dim）vs `text-embedding-ada-002`（1536 dim 但语义空间不同）。向量索引需按 `model_name` 分区或加 model 标签。当前 `search.go` 的 `Query` 方法直接调用单一 embedder 产生 query 向量，然后与整个索引比较——多模型下这一跳逻辑不成立。

2. **缓存键膨胀**：搜索缓存 cache key 当前不含 `embed_model`。多模型路由下，同 query 不同模型的缓存必须隔离——这导致缓存命中率下降。

3. **chat 流式切换不可能**：一个 SSE 流中间不能切换 LLM 模型。路由只能在每次 Chat 请求边界决策。Agent 循环中每步可以独立路由，但需要在 `Agent.Run` 的步骤上下文传递路由决策。

#### 架构变更

```go
// 新增路由层，包装现有接口
type ModelRouter struct {
    endpoints map[string]ModelEndpoint  // name → config
    rules     []RoutingRule             // ordered matchers
    default_  string                    // default endpoint name
}

// 路由维度（优先级排序）
type RoutingMatchers struct {
    TenantPlan string  // "free" | "pro" | "enterprise"
    UseCase    string  // "search_embed" | "chat_generate" | "agent_reason"
    MaxTokens  int     // estimated
    BudgetPct  float64 // remaining daily budget %
}
```

**关键决策：路由层需包装还是侵入？**

| 方案 | 优点 | 缺点 |
|------|------|------|
| A：包装（Decorator）模式 | 不改 ai.Search/ai.Chat 内部逻辑，`RouterEmbedder` 包装 `Embedder` 接口并委托给选择后的底层模型 | 无法按路由维度选择性的跳过缓存；代理层多一次调用开销 |
| B：侵入式（Strategy 模式） | ai.Search 内部持有 `EmbedderSelector` 接口，每次 embed 前查询 | 接口变更波及所有调用者；测试 mocking 量增加 |

**推荐：方案 A 作为 MVP，方案 B 作为 v2。** 理由：装饰器可以在不改动任何现有测试的情况下增量引入，且替换逻辑集中在一个包内。

#### 对现有系统的影响

- `AIConfig` 需新增 `[]ModelRoute` 配置（环境变量或 YAML 格式；推荐从环境变量开始，后续加 admin API）
- `buildLLM` / `buildEmbedder` 返回路由包装而非单一实例
- `Search` / `Chat` / `Agent` 构造逻辑不变，只替换注入的实例
- 向量索引元数据需加 `embed_model` 字段——这是唯一"必须改 schema"的点

---

### 方向 C：数据血缘图基础设施（从 lineage 到 provenance）

#### 为什么需要

文档的论证很充分——方向四不是"锦上添花"，而是合规驱动（EU AI Act、GDPR 删除权、训练数据溯源）和运维驱动（删除影响分析、RAG 回答追溯）的交叉需求。当前实现（`ListUsageForObject`）只回答了"谁查了这个对象"，回答不了"这个回答引用了哪些原始文档"。

`.out.md` 修正很重要：**embedding 无独立 ID**，因此追踪应停在 chunk 级别。`ProvenanceEvent.TargetType` 不应包含 `embedding`。

#### 核心挑战

1. **血缘表增长不可控**：一个 100 页 PDF → 2000 sentence chunks → 1 行 ProvenanceEvent（chunked_from）+ 2000 行（extracted_from...）。生产环境日均千万级事件。分区策略必须在 MVP 阶段内置——建议按月或按 50 万行自动分区（Postgres 的 declarative partitioning 或 SQLite 的存分表+视图）。

2. **硬删除 vs 血缘保留**：当前 `DeleteChunksForObject` 物理删除 chunk 行和索引行。方向四引入后，删除对象时血缘记录必须保留（软标记 `object.deleted_at IS NOT NULL` 而非删除 provenance 行）。这要求在触发删除前先快照血缘关系。

3. **血缘图查询的递归深度**：`GET /v1/lineage/provenance/{objectID}` 结果集是 DAG。API 需限制递归深度（默认 2 层，max 5 层），且结果需去环（对关系型数据库的递归 CTE 查询是标准解法但需预防深度过大的查询 timeout）。

#### 架构变更

```go
// 新增 ProvenanceEvent 结构体和对应表
type ProvenanceEvent struct {
    ID           int64
    SourceID     *int64             // nil = 原始上传
    TargetID     int64
    TargetType   string             // "object" | "chunk"
    RelationType string             // "original_upload" | "extracted_from" | "chunked_from" | "copied_from" | "transformed_from" | "ai_queried"
    TransformOp  string             // "extract_text" | "chunk_sentence" | "embed" | "my_custom_webhook"
    Metadata     json.RawMessage    // { "embed_model": "...", "window": 600 }
    Actor        string             // "system:indexer" | "user:alice"
    TenantID     string
    CreatedAt    time.Time
}
```

**关键决策：血缘写入的同步性**

| 方案 | 优点 | 缺点 |
|------|------|------|
| 同步写入（与业务操作同一事务） | 强一致性；异常时自动回滚 | 额外写入增加主路径延迟 |
| 异步写入（事件总线二次消费） | 主路径零影响 | 血缘可能丢失（需 exactly-once 语义）；重建成本高 |

**推荐：同步写入 —** 理由是血缘数据量虽大但单行写入极快（`INSERT` + 唯一索引），且强一致性对合规场景是硬性要求。异步方案只适用于血缘视图刷新（如 Web UI 的图形渲染）。

#### 对现有系统的影响

- `repository.ProvenanceEvent` 表 + 迁移文件（双文件）
- `FileService.Put`：插入 `original_upload` 事件
- `FileService.CopyObject`：插入 `copied_from` 事件
- `FileService.DeleteObject`：**不删除** provenance 行，只标记 `object.deleted_at`
- `Indexer.processOne`：提取后插入 `extracted_from`，分块后批量插入 `chunked_from`
- `Search.Query`：现有 `ListUsageForObject` 路径不变，新增 `ProvenanceQuery` 路径
- REST 路由：新增 `GET /v1/lineage/provenance/{objectID}?depth=2`
- Web UI：新增血缘由下钻标签页（可延迟到后续迭代）

---

### 方向 D：多目标 Webhook / 事件驱动触发器（近数据计算的前置条件）

#### 为什么需要

方向二是正确但距离远的目标（全功能 Wasm 沙箱/侧车容器模型需要 3-4 周）。`.out.md` 强烈建议先做多目标 webhook——这是 80% 企业在"数据触发自定义处理"上的真实需求（通知 CRM、触发 CI/CD 流水线、调用内部 API 做 OCR/格式转换）。

#### 核心挑战

1. **当前 `EVENTS_WEBHOOK_URL` 是单值**：环境变量只允许一个全局 webhook 目标。要实现"桶 A 的 `object.created` → URL1，桶 B 的 `object.deleted` → URL2" 的多目标路由，需要从单 URL 升级为 `[]NotificationRule` 匹配引擎。

2. **S3 parser 已经有 LambdaFunctionArn 字段**，但 `BucketNotification` 的 consumer 是空的（`.out.md` 确认了这一发现）。因此实现路径是：连接 proto parser layer（已解析）→ 新增 consumer layer → event matching 引擎复用已有 `FilterKey` pattern 匹配逻辑。

#### 架构变更

```
当前：EventBus → webhook.go (单一 URL)
变更：EventBus → EventRouter
                  ├── match Bucket
                  │       └── → BucketNotificationRule (多条)
                  ├── match NotificationRule.EventType
                  │       └── → target (URL/LambdaARN)
                  └── match FilterKey pattern
                          └── → dispatch
```

**关键设计决策：消费引擎的位置**

- **选项 A：内嵌在 `events/bus.go` 中** — 新增 `MatchAndDispatch` 方法，每次 `Publish` 后同步匹配。
- **选项 B：独立 `internal/events/trigger.go` Consumer** — 作为 EventBus subscriber 运行，实现 Processor 接口。
- **推荐：选项 B** — 保持 bus.go 不做事件分发扩张；Consumer 模式与已有的 Antivirus/Replication/Webhook 消费者一致，运维模式统一。

#### 对现有系统的影响

- 极低：新增一个 consumer goroutine（`main.go` 加一行 `go triggerEngine.Run(ctx, bus.Subscribe())`）
- `BucketNotification.LambdaFunctionArn` 字段从 unused 变成消费——这是修复而非变更
- `EVENTS_WEBHOOK_URL` 环境变量可保留为"全局 fallback 目标"

---

### 方向 E：三层内容去重（精确字节级 → 近似文本级 → 语义级）

#### 为什么需要

方向五被正确标为 P3，但它的**分块级近似去重**（方向一 × 方向五的交叉点 A）价值高于纯 blob 级去重。理由：

1. 企业场景中，同一文档的不同版本（版本化 bucket + 小修改）会产生 N 个 blob，但这些 blob 的大多数 chunk 完全相同
2. 搜索重复内容降低用户体验——在当前架构中，搜索 "2026 年报" 可能返回 10 条结果但只有 2 个不同的语义片段
3. `.out.md` 的"交叉点 A"指出：**chunk-level dedup 比 blob-level dedup 节省更多存储空间**

#### 核心挑战

1. **三层检测的复杂度陡增**：Layer 1（SHA256）2 天可以完成；Layer 2（SimHash）需要 hash 函数依赖和阈值调优；Layer 3（向量余弦）等待方向一和方向三稳定后再做。

2. **事务一致性**：引用计数 `ref_count` 在事务内增减，同一事务中不能先 `SELECT` 后 `INSERT`（race condition），必须用 `INSERT ... ON CONFLICT DO UPDATE ... SET ref_count = ref_count + 1` 或 `SELECT ... FOR UPDATE` 实现原子操作。

3. **加密对象的去重不可行**：SSE-C/KMS 每个对象有不同密钥 → 加密后字节不同 → SHA256 不同。需在对象元数据中标记 `dedup_eligible=false` 以跳过损失无效的去重尝试。

#### 架构变更

```
Put flow 变更（Layer 1 精确去重）：

1. 计算 object bytes SHA256
2. IF object metadata.sse_type != "" → skip dedup, go to step 5
3. BEGIN TX:
   --- 尝试 insert content_hash (唯一约束)
   --- 若冲突 (content_hash 已存在):
       --- SELECT ref_count FOR UPDATE
       --- UPDATE ref_count += 1
       --- RETURN 现有 object_id（别名引用）
   --- 若无冲突:
       --- INSERT object (新行)
       --- INSERT content_hash_row (sha256, ref_count=1)
   --- COMMIT
4. IF 是别名引用 → 不写入存储（存储 key 复用/引用计数管理）
5. ELSE → 正常写入存储

Search.Query 去重（MMR 多样性选择）：
1. Retrieve top-K results
2. Cluster by content_hash fingerprint
3. Maximal Marginal Relevance 选择
```

**分块级去重**（方向一 × 方向五交叉点）：

```
Indexer path 变更：
1. ChunkPipeline 产生 []Chunk（含 text content）
2. 对每个 chunk text 计算 SHA256（chunk_hash）
3. 查 chunk_hash 索引
   ├── 不命中 → 正常 embed + 索引写
   └── 命中 → 跳过 embed，复用现有向量
4. 写 ProvenanceEvent 时新增 chunk_dedup_ref 关系
```

#### 对现有系统的影响

- `repository.Object` 新增 `content_sha256` 和 `ref_count` 字段（nullable）
- `storage.Storage` 接口新增 `ContentHash(key string) (string, error)` 方法
- `service.file_crud.go:Put` 新增去重检测逻辑（约 30 行）
- 搜索路径新增 `Hit.ContentHash` 去重标记（下游 Web UI 渲染需适配显示优化）
- 迁移文件两套（sqlite + postgres）

---

## 3. 接口设计建议

### 3.1 核心原则

1. **改返回值协议优于改调用接口**：如 `[]string → []Chunk` 的升级，扩展返回值结构体（保持 `Text string` 字段以通过编译）比添加新方法更安全。
2. **装饰器优先于侵入式修改**：方向三的 `ModelRouter` 可以作为 `Embedder` 接口的装饰器实现，而不是修改 `ai.Search` 的成员。
3. **默认降级策略必须在接口中声明**：每个可扩展接口必须提供"当扩展点返回错误时的默认行为"——如 `ChunkStrategy.Chunk` 返回 `error` 时 pipeline 应 fallback 到 `SlidingWindowStrategy`。
4. **所有新增表必须有两套迁移文件**：这是 `AGENTS.md` 的 I2 不变量，方向四的 `provenance_events` 表不例外。

### 3.2 具体接口建议

#### ChunkStrategy 接口

```go
// internal/ai/chunker.go
type Chunk struct {
    ID          string  // uuid/v7 — 方向四的锚点
    Text        string  // 兼容现有 []string 消费方
    Offset      int     // 在原始文本中的字节偏移
    Length      int     // Text 的字节长度
    Strategy    string  // "sentence_window" | "sliding_window" | ...
}

type ChunkOptions struct {
    ContentType string // "text/plain" | "application/json" | ...
    TargetSize  int    // 目标 chunk 大小（chars/tokens）
    Language    string // "en" | "zh" | ...
}

type ChunkStrategy interface {
    Name() string
    Chunk(text string, opts ChunkOptions) ([]Chunk, error)
}

type ChunkPipeline struct {
    strategies map[string]ChunkStrategy  // contentType → strategy
    default    ChunkStrategy
    maxFallback int  // 单次 fallback 恢复限次
}
```

**设计权衡：**

| 决策 | 选项 A（推荐） | 选项 B | 理由 |
|------|------------|-------|------|
| Strategy 注册时机 | 编译期通过 `NewDefaultPipeline` 内置 | 运行时通过 `ChunkPipeline.RegisterStrategy` API | MVP 无用户注册需求；编译期简单且可测试 |
| ContentType 来源 | `Extractor` 扩展返回值 | Indexer 从对象元数据 MIME type 推断 | Extractor 已有 `Extract` 方法签名破坏最小；Indexer 有 metadata 访问能力 |
| Error 传播 | `return nil, err` 后 caller 决定 fallback | 内部自动 fallback 到 `default` | Caller（Indexer）应有 fallback 决策权 |

#### ModelRouter 接口

```go
// internal/ai/router.go
type ModelEndpoint struct {
    Name      string
    Provider  string
    Model     string
    Endpoint  string
    APIKey    string
    Priority  int
    Timeout   time.Duration
}

type ModelRequest struct {
    TenantID  string
    Plan      string  // "free" | "pro" | "enterprise"
    UseCase   string  // "search_embed" | "chat" | "agent"
    TokenEst  int
}

type ModelRouter struct {
    endpoints map[string]ModelEndpoint
    rules     []struct {
        Name     string
        Priority int
        Matcher  func(ctx context.Context, req ModelRequest) bool
        Endpoint string  // endpoint name
    }
    defaultEndpoint string
    metrics         telemetry.ModelMetrics
}

// 装饰器实现 Embedder 接口
type RouterEmbedder struct {
    router  *ModelRouter
    fallback Embedder  // 无可匹配路由时的默认 Embedder
}

func (re *RouterEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
    req := extractModelRequest(ctx) // tenant, useCase from ctx
    ep := re.router.Select(ctx, req)
    return ep.Embedder.Embed(ctx, texts)
}
```

### 3.3 需要引入的新抽象层

| 抽象层 | 用途 | 最佳时机 |
|--------|------|---------|
| `ChunkStrategy` 接口 + `ChunkPipeline` | 分块策略路由与组合 | 方向一 MVP |
| `ModelRouter`（包装器形式） | AI 模型多实例路由 | 方向三 MVP |
| `ProvenanceStore` 接口 | 血缘事件的仓库抽象 | 方向四 MVP |
| `EventRouter` | 事件触发的规则匹配与分发 | 方向二 MVP |

**一个不应引入的新抽象**：`ContentHashResolver`（方向五的精确去重引擎）。方向五的 Layer 1 去重可以（且应该）直接在 `file_crud.go` 中加入 SHA256 计算和唯一约束检测的逻辑，不需要新抽象层——去重是 `Put` 路径的优化，不是独立的业务能力。

### 3.4 向后兼容策略

| 变更 | 兼容策略 | 废弃周期 |
|------|---------|---------|
| `[]string` → `[]Chunk` | `Chunk` 结构体包含 `Text string` 字段；旧消费方可直接取 `.Text` | 无限期（`.Text` 永远保留） |
| `EventBus.Publish` 签名不变 | 不变 | — |
| `Embedder` 接口不变 | `RouterEmbedder` 实现同一接口 | — |
| 搜索 API 返回格式 | 新增 `content_hash` 字段可选；默认不返回 | — |
| 现有 lineage API | v2 路径 `GET /v1/lineage/v2/provenance/{objectID}` 新增；旧 API deprecate → remove in 3 releases | 至少保持 3 个版本 |

---

## 4. 技术选型

### 4.1 不需要的新依赖

审阅两个文档后，我的判断是：**五个方向在 MVP 阶段都不需要引入新的第三方依赖**。

| 方向 | 是否需要新依赖 | 理由 |
|------|-------------|------|
| 自适应分块 | **否** | `SentenceWindow` 可用 `strings.SplitAfter(text, ".。!！?？\n")` 实现；`RecursiveSplit` 不依赖第三方 |
| 多目标 Webhook | **否** | 复用已有 `webhook.go` 的 retry/backoff/durable 机制 |
| 多模型路由 | **否** | `ModelEndpoint` 是配置数据，路由选择是匹配逻辑——已有 embedder 池化，无需新的 HTTP 客户端依赖 |
| 数据血缘 | **否** | `provenance_events` 表 + 递归 CTE 查询——纯 SQL |
| 近似去重 | **条件性** | Layer 1（SHA256）无依赖；Layer 2（SimHash）可选 `github.com/dgryski/go-minhash` 或自建 |

### 4.2 条件性考虑的新依赖

**方向五 Layer 2（SimHash/MinHash）** 如果实施，需要引入一个 hash 函数库。推荐自建（约 50 行）而非引入第三方：

```go
// 自建 SimHash（64-bit variant）
func Simhash(text string) uint64 {
    // 1. Tokenize（按空格/中文分词 — 无需分词器，N-gram 即可）
    // 2. Hash each token（FNV-1a — 标准库已有）
    // 3. 加权求和：每个 token hash 的 bits {1: +weight, 0: -weight}
    // 4. 规约：sum > 0 → bit=1
    return fingerprint
}
```

理由：第三方 minhash 库（`dgryski/go-minhash`、`ekzhu/minhash-lsh`）通常为全文索引设计，功能过重；我们需要的是可嵌入 `Put` 路径的轻量指纹函数，标准库（`hash/fnv`、`math/bits`）足矣。

### 4.3 需要论证的依赖

**方向二（Wasm 沙箱）** 如果进阶到全功能方案，`wazero`（纯 Go Wasm 运行时）是唯一合理的依赖候选——无 CGo、零平台依赖、Apache 2.0 协议。但这是 P2 阶段的决策，当前只需 MVP（多目标 webhook）。

**自建 vs 采购的决策框架：**

| 功能 | 自建 | 采购/第三方 | 决策 |
|------|------|-----------|------|
| ChunkStrategy | 2 天 | N/A（无直接 SaaS 产品） | ✅ 自建 |
| Webhook 路由 | 2 天 | 可挂载 Kafka/Zapier（过度设计） | ✅ 自建 |
| Model Router | 5 天 | 直接采购 AI Gateway（如 Portkey、Helicone） — 月费 $500+ | ⚠️ 初期自建，增长后评估采购 |
| Data Lineage | 7 天 | N/A（OpenLineage 标准但实现重） | ✅ 自建 |
| Content Dedup | Layer1 2天, Layer2/3 各 1周 | 可嫁接 druid/Dell EMC 等存储级去重 | ⚠️ Layer1 自建，Layer2/3 观察 |

**评估标准**：五个方向中只有方向三的 Model Router 存在直接竞品（AI Gateway 类产品）。建议 MVP 自建，当需要企业级仪表盘/用量审计/多 provider 信用管理时再评估采购——但届时也应以模块化接口友好替换。

---

## 5. 实施路线图

### 5.1 优先级矩阵

| 方向 | 业务价值 | 实现成本 | 技术风险 | 外部依赖 | 建议优先级 |
|------|---------|---------|---------|---------|----------|
| 自适应分块 | 高（RAG 质量根本杠杆） | 低（3-5 天） | 低 | 无 | **P0** |
| 多模型路由 | 高（成本+可用性） | 中（5-7 天） | 中 | 无 | **P1** |
| 多目标 Webhook（方向二前序） | 中高（80% 场景） | 低（2 天） | 低 | 无 | **P1** |
| 数据血缘 | 高（合规驱动） | 中（7-10 天） | 中高 | 无 | **P2** |
| 近似去重 Layer1 | 中（存储成本） | 低（2 天） | 中 | 无 | **P2** |
| 近似去重 Layer2/3 | 中（搜索质量） | 高（2-4 周） | 高 | 条件性 | **P3** |
| Wasm 沙箱（方向二全量） | 高（扩展性） | 高（3-4 周） | 高 | wazero | **P3** |

### 5.2 阶段划分

```
Phase 1（Week 1-2）：基础设施升级 + P0
├── [P0] Chunk 返回值协议升级 []string → []Chunk
│     ├── Chunk 结构体定义（含 ID/Offset/Text/StrategyName）
│     ├── ChunkStrategy 接口 + SlidingWindowStrategy（现有策略迁移）
│     ├── ChunkPipeline（contentType → strategy 映射表）
│     └── 消费方（Indexer/IndexerTest）适配 []Chunk
│
├── [P0] SentenceWindowStrategy（无外部依赖句子边界分割）
│
├── [清偿债务] Search cache key 加 embed_model 占位字段
│
└── [P1] 多目标 Webhook（方向二前序）
      ├── NotificationRule 消费引擎（EventRouter consumer goroutine）
      ├── 规则匹配（EventType + FilterKey pattern）
      └── EVENTS_WEBHOOK_URL 作为全局 fallback 保留

Phase 2（Week 3-4）：P1 方向 — 模型路由与成本控制
├── [P1] ModelRouter 定义 + 配置系统
│     ├── ModelEndpoint 结构体
│     ├── 环境变量配置（AI_ROUTE_0_* 模式）
│     └── 路由维度：tenant plan
│
├── [P1] RouterEmbedder（装饰器模式，包装 Embedder 接口）
│
├── [P1] RouterLLM（装饰器模式，包装 LLM 接口）
│
├── [P1] 向量索引 metadata 加 embed_model 标签
│
└── [P1] 缓存隔离（embed_model 加入 cache key）

Phase 3（Week 5-6）：P2 方向 — 数据血缘
├── [P2] provenance_events 表 + 迁移文件（双文件）
├── [P2] FileService 管线接入点：Put/CopyObject/Delete（不删血缘行）
├── [P2] Indexer 管线接入点：Extract/Chunk 阶段分别写入
├── [P2] REST API：GET /v1/lineage/v2/provenance/{objectID}
├── [P2] 递归 CTE 查询 + 深度限制
└── [P2] Web UI 血缘下钻（可由 UI 团队延迟交付）

Phase 4（Week 7-8）：P2 方向增量 — 近似去重 Layer1
├── [P2] repository.Object 加 content_sha256 / ref_count 字段
├── [P2] Put 路径 SHA256 计算 + content_hash 唯一约束检测
├── [P2] 引用计数事务一致性（INSERT ... ON CONFLICT）
├── [P2] SSE 加密对象标记 dedup_eligible=false
└── [P2] Search.Query 按 content_hash 做 MMR 多样性选择

Phase 5（Future — P3）：高级扩展
├── [P3] 近似去重 Layer2：SimHash 指纹 + 阈值配置
├── [P3] 近似去重 Layer3：向量余弦（跨 embed_model 隔离）
├── [P3] 分块级去重（方向一 × 方向五交叉点）
├── [P3] Wasm 沙箱函数注册和执行引擎
└── [P3] 事件触发器完整方案（侧车 gRPC worker）
```

### 5.3 里程碑

| 里程碑 | 时间 | 交付物 | 验收标准 |
|--------|------|--------|---------|
| **M1: Chunk 协议升级** | Week 1 末 | Chunk 结构体定义、ChunkStrategy 接口、SlidingWindow 策略迁移、所有消费方兼容 | `go vet ./...` + `go test ./...` + 旧 API 兼容测试全绿 |
| **M2: 多策略分块** | Week 2 末 | SentenceWindow 策略、ChunkPipeline contentType 路由、Indexer 策略选择 | grep 确认全库无 `[]string` chunk 残存；RAG 测试集准确率提升 ≥10% |
| **M3: 多目标 Webhook** | Week 2 末 | EventRouter consumer、规则匹配、EVENTS_WEBHOOK_URL 保留 | 创建 2 条通知规则，上传文件 → 确认两个目标收到不同 filter 下的事件 |
| **M4: 模型路由上线** | Week 4 末 | ModelRouter + 环境变量配置 + RouterEmbedder + 缓存隔离 | 配置两条路由（租户 A→gpt-4o-mini，其他→gpt-4o），上传文件、搜索、聊天 → 确认不同租户使用不同模型 |
| **M5: 血缘上线** | Week 6 末 | provenance_events 表、管线接入点、REST API | 上传文件 → 确认 `original_upload` 事件；搜索 → 确认 `ai_queried` 事件；lineage API 返回深度 2 的 DAG |
| **M6: 去重 Layer1** | Week 8 末 | 内容寻址 Put、引用计数、搜索结果多样性选择 | 上传相同文件 2 次 → 存储只有 1 份 blob；搜索返回去重后的 N 条结果 |

### 5.4 风险点与缓解策略

| 风险 | 概率 | 影响 | 阶段 | 缓解 |
|------|------|------|------|------|
| Chunk 返回值协议升级导致存量 Indexer 测试失败 | 中 | 高 | M1 | 先在 `Chunk` 中添加 `Text` 字段、旧消费方适配使用 `.Text`、测试覆盖率验证 |
| SentenceWindow 策略的中文句子边界分割不准确 | 中 | 中 | M2 | 简单规则（句号/问号/感叹号/换行）作为 V1；V2 引入 `go.text/runes`；测试集包含中英文混合文档 |
| 多目标 Webhook 引入的规则匹配延迟导致事件积压 | 低 | 中 | M3 | EventRouter 作为独立 consumer 不阻塞主 Publish 路径；规则匹配复杂度 O(n rules) — 建议 n ≤ 100 |
| 模型路由维度选择导致嵌入模型与索引不匹配 | 中 | 高 | M4 | 搜索路径先查索引元数据中的 `embed_model`，不同模型分区搜索后再聚合（RRF 融合类似当前 hybrid search） |
| 血缘表在高吞吐场景下成为写入瓶颈 | 中 | 高 | M5 | 同步写入 BUT 使用延迟写缓冲区：`INSERT` 在事务内但不等待 fsync（SQLite WAL mode + `PRAGMA synchronous=NORMAL`）；Postgres 使用 unlogged table 或 `INSERT ... RETURNING id` 简化路径 |
| 去重引用计数在并发上传（同一内容）时产生 race condition | 低 | 高 | M6 | `INSERT ... ON CONFLICT DO UPDATE SET ref_count = ref_count + 1` 单语句原子操作；放弃 `SELECT ... FOR UPDATE` 模式 |
| 方向一 × 方向五交叉点（chunk-level dedup）的嵌入向量复用导致向量索引稀疏 | 低 | 中 | M6 | 向量索引中的 `chunk_hash` 字段建立索引，查询时 JOIN 去重；SQLite limit 可调，Postgres 无影响 |
| 迁移动态：方向四（血缘保留）要求 `DeleteChunksForObject` 改为软删除，但现有调用的语义不能变 | 中 | 中 | M5 | `DeleteChunksForObject` 新增 `soft bool` 参数（默认 `true`）；硬删除路径（GC）使用显式 `DeleteChunksPermanent` |

---

## 最终总结

这份分析文档及其回应是我见过的最扎实的架构扩展分析之一。核心结论：

1. **代码锚点验证充分**：5 个方向全部经过逐行代码确认，发现 2 个修正（`Chunk` 返回类型和 embedding 无独立 ID）。这是优秀的架构分析纪律。

2. **五个方向中必须立刻做的是方向一和方向三**：方向一（自适应分块）是 RAG 质量的根本杠杆，影响最广、成本最低、风险最低。方向三（多模型路由）是 SaaS 运营的刚需，且设计得当可以在不改动核心 AI 管线的前提下增量引入。

3. **方向二应拆分为两阶段**：多目标 Webhook（P1，2 天）→ Wasm 沙箱（P3，3-4 周）。前者解决 80% 需求，后者是高产品差异化但高风险的长期投资。

4. **最重要的架构债务在 `[]Chunk` 返回值协议**：这是方向一、方向三、方向四共同的阻塞点——没有 Chunk 元数据就没有稳定标识符，没有标识符就没有血缘追踪、没有模型来源标记。Phase 1 清偿此债是后续所有扩展的前提条件。

5. **不要低估方向四的血缘表增长**：日均千万级事件在 SQLite 上不可持续——建议 MVP 阶段就为 `provenance_events` 启用自动分区策略（Postgres 的 declarative partitioning 或 SQLite 的按月分表 + `CREATE VIEW` 统一查询接口）。
