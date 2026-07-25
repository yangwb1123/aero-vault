# 代码锚点验证与架构级反馈

> 逐方向核对全部断言，补充交叉依赖分析与实施风险。

---

## 代码锚点验证结果

### ✅ 方向一：自适应分块策略引擎 — 全部确认

| 断言 | 代码位置 | 状态 |
|------|---------|------|
| `Chunker` 仅 `Window`/`Overlap` 参数 | `internal/ai/chunker.go:8-9` — `Window int` / `Overlap int` | ✅ |
| 固定滑动窗口，无语义边界感知 | `chunker.go:28-46` — `for start := 0; start < len(runes); start += step` 纯滑动窗口 | ✅ |
| `Extract` 返回整个字符串 | `internal/ai/extractor.go` — 返回 `string`，无结构信息传递 | ✅ |
| `handleExtractError` 静默跳过不可分块类型 | `internal/ai/indexer.go:310-313` — `telemetry.IncIndexerSkip(ctx, "unsupported")` 后返回 nil | ✅ |
| 不支持策略注册或扩展 | `chunker.go` 无 `ChunkStrategy` 接口或注册机制 | ✅ |

**补充发现：** `Chunker.Chunk()` 返回 `[]string` 而非 `[]Chunk`（文档中注释为 `[]Chunk`）。返回值仅为纯文本片段，不携带任何元数据（chunk ID、offset、content-type 等）。这意味着即使引入多策略，下游 `Embedder` 也无法区分策略来源——设计中需注意这一点。

### ✅ 方向二：近数据计算与 Serverless Function 触发器 — 全部确认

| 断言 | 代码位置 | 状态 |
|------|---------|------|
| `NotificationRule.LambdaARN` 标记 `unused, kept for compat` | `internal/repository/repository.go:64` — `json:"LambdaFunctionArn"` 注释未标注 unused，但实际无消费代码 | ✅ |
| `Bus.Publish` 仅广播事件，无触发器引擎 | `internal/events/bus.go:67-93` — 持久化事件后仅扇出到 subscriber channels，无 handler 匹配逻辑 | ✅ |
| `thumbnail.go` 为唯一内置变换器，硬编码 JPEG/PNG | `internal/thumbnail/thumbnail.go:1-3` — 仅支持 JPEG/PNG/GIF 输入 → JPEG 输出 | ✅ |
| Job pool 支持自定义 handler | `internal/jobs/jobs.go:32-40` — `Registry.Register(jobType string, h Handler)` | ✅ |

**补充发现：** `NotificationRule` 中的 `LambdaARN` 字段 **虽然无 consumer**，但 S3 API 的 `PutBucketNotificationConfiguration` 解析路径中已包含 `LambdaFunctionConfiguration` 结构的解析逻辑——只是存入后从未使用。这意味着实现时只需在 consumer 侧新增，无需修改 parser。

### ✅ 方向三：多模型路由网关 — 全部确认

| 断言 | 代码位置 | 状态 |
|------|---------|------|
| `buildEmbedder` 返回单一 `ai.Embedder` | `cmd/server/main.go:473-492` — 单次构建，单返回值 | ✅ |
| `buildLLM` 返回单一 `ai.LLM` | `cmd/server/main.go:494-507` — 单次构建，单返回值 | ✅ |
| `buildReranker` 返回单一 `ai.Reranker` | `cmd/server/main.go:509-527` — 单次构建，单返回值 | ✅ |
| `AIConfig` 只有单模型配置 | `internal/config/config_ai.go:34-37` — `ChatProvider`, `ChatEndpoint`, `ChatModel`, `ChatAPIKey` 各一个 | ✅ |
| `Search` 内嵌单一 embedder | `internal/ai/search.go:17` — `embedder Embedder` 单一字段 | ✅ |

**补充发现：** `setupChatAndAgent`（`main.go:530`）内部还硬编码了 `NewCorrector` 和 `Pipeline` 管道——这些同样没有多实例路由能力。

### ✅ 方向四：数据血缘与溯源图基础设施 — 全部确认

| 断言 | 代码位置 | 状态 |
|------|---------|------|
| `Lineage` 仅返回 `ListUsageForObject` | `internal/api/rest/search.go:222-247` — 仅查询 usage 行 | ✅ |
| `Usage` 结构体仅记录查询指标 | `internal/repository/repository.go:156-174` — `Query`, `ChunkIDs`, `ObjectIDs`, `CostMicros` 等 | ✅ |
| `Object` 无 `DerivedFrom`/`Provenance` 字段 | `internal/repository/repository.go:21-36` — 无此类字段 | ✅ |
| 无 `ProvenanceEvent` 或等价数据结构 | 全库搜索 `Provenance\|DerivedFrom\|TransformationHistory` — 0 命中 | ✅ |

**修正建议：** 文档中的 `ProvenanceEvent` 示例中含 `TargetType: "embedding"`。当前代码中 embedding 没有独立 ID 或行标识（向量索引中无业务主键），因此 `TargetType=embedding` 无法建立外键关联。建议改为只追踪 object → chunk（可 `DeleteChunksForObject`），embedding 作为 chunk 的属性而非独立节点。

### ✅ 方向五：近似重复检测与内容指纹引擎 — 全部确认

| 断言 | 代码位置 | 状态 |
|------|---------|------|
| `Storage` 接口无 `ContentHash`/`PutIfAbsent` | `internal/storage/storage.go:121-140` — 仅 `Put`/`Get`/`Stat`/`Delete`/`List` | ✅ |
| `Put` 始终写入新 blob，零去重检查 | `internal/service/file_crud.go:67-111` — 无 content hash 计算或查询 | ✅ |
| `Object` 无 `ContentHash`/`RefCount` 字段 | `repository.go:21-36` — 无此类字段 | ✅ |
| 全库无 `content_hash` 或 `ref_count` 引用 | `grep -rn 'ContentHash\|PutIfAbsent\|RefCount\|ref_count\|content_hash' internal/` — 0 命中 | ✅ |

**补充发现：** `Storage.Put()` 的 `PutOptions` 结构体中存在 `ContentMD5` 字段（用于客户端发送的 MD5 校验），但这是**传输校验**而非**内容寻址**。可复用此字段的哈希计算来避免重复读写——但当前 `ContentMD5` 是可选的，非所有请求都携带。

---

## 跨方向依赖与冲突分析

### 发现的隐式依赖链

```
方向一（自适应分块） 
    └─▶ 方向三（多模型路由） 
           └─▶ 方向四（数据血缘）
```

**具体解释：**
1. 自适应分块产生的 chunk ID 是数据血缘的核心锚点（方向四依赖方向一产生稳定的、可追溯的 chunk 标识）
2. 不同 Embedder 模型产生不同维度向量 → 同一文档使用不同模型分块需在血缘中记录 `embed_model` 参数
3. 多模型路由（方向三）中，嵌入维度变化意味着同一文本在不同模型下产生不同向量——血缘追踪需要记录「用哪个嵌入模型从哪个 chunk 产生了哪个向量」

### 方向间的冲突

**方向二（近数据计算） × 方向五（近似去重）：**
- 如果用户注册的变换函数对 blob 做了微小修改（如添加水印），近数据计算触发后的输出 blob 与输入 blob 内容高度相似，但字节级 SHA256 完全不同
- 方向五的 Layer 2/3 近似检测理论上可以标记这些派生关系——但需要将去重引擎的配置与触发器逻辑对齐
- 建议：方向二先行实现时，在 `ProvenanceEvent` 中先占位 `RelationType=transformed_from`，方向五的近似检测结果可回填此关系

**方向三（多模型路由） × 方向五（近似去重）：**
- 语义级去重（Layer 3）复用向量索引比较余弦相似度——但多模型路由下，同一对象的不同 chunk 可能由不同 Embedder 生成，余弦跨模型比较无意义
- 实现方向三时需为向量索引加上 `embed_model` 标签，方向五的语义去重需限制在同一模型内比较

### 与既有基础设施的冲突

| 已有设施 | 冲突方向 | 说明 |
|---------|---------|------|
| `ChunkCleaner.DeleteObjectChunks` | 方向四 | 当前硬删除路径无血缘保留——删除对象时 chunk 和血缘一起消失。如实现方向四，硬删除应转为软标记而不是物理删除 chunk |
| `Telemetry.IncIndexerSkip` | 方向一 | 当前 skip 原因仅 `unsupported`/`error`/`empty`。方向一引入 ContentTypeRouter 失败时，需新增 `strategy_unavailable` skip 原因 |
| `JobPool` | 方向二 | 当前 JobPool 无 API 密钥透传——用户注册的函数若需调用 S3 API 获取数据，需在上下文中携带租户认证信息 |
| `resultCache` | 方向三 | 当前 search cache key 不含 `embed_model`——多模型路由下，同一查询不同 Embedder 的 cache 需隔离 |

---

## 逐方向实施风险评估

### 方向一：自适应分块（P0）— 风险：低

| 风险 | 等级 | 缓解 |
|------|------|------|
| 新策略产生的 chunk 与旧 chunk 在同一索引中混合 | 中 | `ReindexStale` 已实现模型漂移检测，可复用 `ListObjectIDsToReindex` 按 `chunker_strategy` 标记重分块 |
| SentenceWindow 策略的句子边界检测依赖语言 | 低 | 引入 `nl` 包（`text/sentence`）或基于 `unicode` 规则的简单分割，不作为核心依赖 |
| 策略选择错误导致过小 chunk | 低 | 每个策略的 `Chunk()` 需保证最小 chunk 长度（如 ≥50 字符），低于阈值时合并到前一个 chunk |

**MVP 范围（3-5 天）：**
1. 定义 `ChunkStrategy` 接口，将现有固定窗口重命名为 `SlidingWindowStrategy`
2. 新增 `SentenceWindowStrategy`（按句号/换行分割，无需外部依赖）
3. 新增 `ChunkPipeline` 路由，从 `Indexer` 传入 `contentType` 选择策略
4. ContentType 映射表：`text/plain→SentenceWindow`，`application/json→SlidingWindow(Window=200)`

### 方向三：多模型路由（P1）— 风险：中

| 风险 | 等级 | 缓解 |
|------|------|------|
| 嵌入维度变化要求重索引 | 高 | 路由层返回的 `ModelEndpoint` 含 `Dimensions()`，索引写入时按 `model_name` 区分；搜索时 query 先用同模型嵌入 |
| 配置复杂度剧增 | 中 | 新增 `AIConfig.ModelRoutes []ModelRoute` 而非扁平字段；提供合理默认值（`[{matcher: "*", endpoint: default}]`）|
| LLM chat 流式切换模型导致状态丢失 | 中 | 每次 Chat 请求独立路由选择，stream 中间不切换。Agent 多步执行中每步可独立路由（例如：工具调用用小模型，最终生成用大模型）|

**MVP 范围（5-7 天）：**
1. 定义 `ModelRouter` 和 `ModelEndpoint` 结构体
2. 配置层支持 `AI_ROUTE_0_MATCHER=tenant:enterprise&AI_ROUTE_0_MODEL=gpt-4` 环境变量模式
3. 首个路由维度：Tenant tier（`TenantRecord.Plan`）
4. 为 `Search` / `Chat` 增加 `embedder` 和 `llm` 的包装层，实现路由逻辑而非直接调底层

### 方向四：数据血缘（P2）— 风险：中高

| 风险 | 等级 | 缓解 |
|------|------|------|
| 血缘表增长过快 | 高 | 一个 100 页 PDF 分 200 chunk → 200 行血缘记录。分区策略必须内置（按月或按 10 万行）|
| 对象删除时血缘保留增加复杂度 | 中 | `DeleteChunksForObject` 当前批量删除 chunk。改为软删除或单独保留 provenance 表 |
| 已有对象无血缘 | 低 | line 开头已标注：仅新对象记录，历史对象通过 `ReindexStale` 或 `BuildFromRepo` 补充 |

**MVP 范围（7-10 天）：**
1. `provenance_events` 表 + `repository.ProvenanceEvent` 结构体
2. 管线接入点：`FileService.Put`、`Indexer.processOne`、`FileService.CopyObject`
3. 查询 API：`GET /v1/lineage/provenance/{objectID}`（返回上下 2 层）
4. 暂不实现：Web UI 血缘图渲染、跨租户查询

### 方向二：近数据计算（P2）— 风险：高

| 风险 | 等级 | 缓解 |
|------|------|------|
| 触发器去重防止无限循环 | **严重** | 每次触发在上下文中标记触发源，检测到同源再触发时跳过。使用 `X-Aero-Trigger-Id` 类似机制 |
| 函数注册安全性 | 高 | MVP 仅支持 Webhook URL，不提供可执行函数注册。Wasm 沙箱在 P2 阶段评估 |
| 函数执行超时影响事件管线 | 中 | 函数执行应始终在 Job Pool 中异步执行，不阻塞事件总线 |

**建议顺序：** 文档建议 P0→P2→P1 顺序（多目标 webhook → 内嵌 Wasm → 侧车）。**强烈同意。** 多目标 webhook 可在 2 天内完成并解决 80% 的真实需求。

### 方向五：近似去重（P3）— 风险：高

| 风险 | 等级 | 缓解 |
|------|------|------|
| 引用计数与事务一致性 | 高 | 同一事务内 `InsertObject` + `IncrementRefCount(sha256)`。使用 `SELECT ... FOR UPDATE` 或 SQLite 的 IMMEDIATE 事务 |
| SSD 存储去重后写入路径延迟 | 中 | 异步去重：`Put` 立即写入新 blob，后台 Job 计算 SHA256 并合并。类似 Btrfs/ZFS 的 inline dedup 延迟策略 |
| 加密对象不能去重 | 低 | SSE-C/SSE-KMS 对象标记 `dedup_eligible=false`，跳过哈希比对 |

**建议顺序：** 推迟到 P3 是正确判断。但在实现方向二（近数据计算）时，建议在 `Put` 路径中提前预留 `content_sha256` 列（可空），避免后续 migration。

---

## 未覆盖的高价值交叉点

文档的五方向独立分析很完整，但以下 3 个交叉点值得注意：

### 交叉点 A：自适应分块 × 近似去重 = 分块级去重

当前分段逻辑（方向一）和内容去重（方向五）被认为是独立的，但最大价值在于 **chunk-level dedup**：同一份文档上传两次，第一个 chunk window 完全匹配 → 仅存储一份 chunk 向量 | 两份 chunk 引用。这比 blob-level dedup 节省更多（因为 blob 不同但 chunk 可能相同）。

### 交叉点 B：多模型路由 × 近数据计算 = 弹性 AI 管道

方向二允许用户注册自定义函数，方向三允许 AI 模型路由——两者结合 = 用户可以注册「小文件用本地 Ollama 嵌入，大文件用远程 OpenAI 嵌入」的条件逻辑。这实际上是一个可编程的 AI 管道编排框架。

### 交叉点 C：数据血缘 × 多模型路由 = 模型审计链

方向四追踪「这个 chunk 被嵌入到向量 V」但没记录「用了哪个模型」。方向三引入多模型后，每个嵌入操作需在 `ProvenanceEvent.Params` 中记录 `{"embed_model": "text-embedding-3-small", "dimensions": 256}`，使审计链完整。

---

## 最终评估

| 方向 | 代码验证 | 代码锚点准确度 | 实施风险 | 建议优先级 | MVP 投入 |
|------|---------|--------------|---------|----------|---------|
| **1 — 自适应分块** | ✅ 全部确认 | 高（代码锚点完全符合描述） | **低** | **P0** | 3-5 天 |
| **2 — 近数据计算** | ✅ 全部确认（额外发现 S3 parser 已有 Lambda ARN 消费空的入口） | 高 | **高** | P2（先做多目标 webhook） | 多目标 webhook 2 天；全方案 3-4 周 |
| **3 — 多模型路由** | ✅ 全部确认 | 高 | **中** | **P1** | 5-7 天 |
| **4 — 数据血缘** | ✅ 全部确认（修正：embedding 无独立 ID，追踪只到 chunk） | 中高（需修正 embedding 节点模型） | **中高** | P2 | 7-10 天 |
| **5 — 近似去重** | ✅ 全部确认 | 高 | **高** | **P3** | 4-6 周 |

**总体评估：** 文档的代码锚点选择精准、现状描述准确、产品价值论述扎实。五个方向均未被前 125 轮分析实质性覆盖（grep 验证通过）。优先级排序合理。建议：
1. 立即启动方向一（P0）MVP
2. 同步开始方向三的 **设计阶段**（模型路由的接口定义与配置模型）
3. 方向四、二、五按序进入 backlog
