# 高价值扩展方向：多模态 AI 管线、元数据锚定搜索、内容去重、对象缓存层次、主动-主动多区域复制

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描 — 237+ Go 源文件，50 对迁移文件，3 套 SDK（Go/Python/JS），MCP 双模式（HTTP+stdio），Web UI，Helm Chart，Grafana/Prometheus/OTel 配置，`AGENTS.md`，`ROADMAP.md`，`CHANGELOG.md`  
> **日期：** 2026-07-11  
> **核心原则：** 不编写任何代码。选取**代码中存在明确锚点、有实质性产品/架构影响、且在前 97 轮分析 + ROADMAP + TODO 中未被独立深度覆盖**的方向。每个方向包含：现状与代码证据 → 产品价值与典型场景 → 架构权衡与建议方案 → 边界情况。

---

## 去重验证

对 `docs/requirements/` 下全部 97 份既有分析文档 + `ROADMAP.md` 10 大方向 + `TODO.md` 进行全文关键词正则 + 代码锚点交叉验证：

| 方向 | 既往覆盖情况 |
|------|-------------|
| **方向一：多模态 AI 管线（视觉、音频、结构化数据）** | ✅ **零实质性架构分析** — v57 方向二「音频/视频预处理」以 1 段话提及「转码/转录」但无代码锚点、无 Extract 接口扩展分析、无 schema 设计。v63「content-aware processing pipeline」概念性讨论流水线抽象但聚焦预处理 hook 而非多模态。v67「MCP 图像工具」分析了 MCP 层 image 工具但从未触及存储后端的多模态提取管线。`ROADMAP.md` 10 大方向**零提及**。全量搜索 `modality\|multi.modal\|vision\|image.*understand\|transcrib\|audio.*process\|OCR\|table.*extract\|layout.*analysis\|spreadsheet.*pars\|structured.*extract` → 仅 v57 方向二、v67 方向二、v63 方向二有**浅层概念提及，零代码级架构分析** |
| **方向二：元数据锚定语义搜索（结构化条件 + 向量融合）** | ✅ **零实质性架构分析** — v33 方向三「metadata filtering」以 30 行描述要过滤的对象属性列表（size, date, tags）但**零代码分析**、零搜索管线集成设计。v53 方向一「structured metadata」概念性讨论 metadata schema registry 但聚焦 metadata 模型本身而非搜索过滤。v79 方向一「vector + structured hybrid retrieval pipeline」以 60 行讨论了混合搜索架构但停留在概念层面（框图 + 数据流描述），**无 REST API 层分析、无 Qdrant filter 锚点分析、无 pgvector WHERE 子句分析、无 Search.Query 接口扩展分析**。全量搜索 `metadata.*filter.*search\|search.*metadata.*filter\|tag.*filter.*search\|date.*range.*search\|size.*filter.*search\|structured.*search.*hybrid\|facet.*search\|filter.*vector\|SearchVectors.*filter` → **0 命中** |
| **方向三：内容寻址存储与块级去重** | ✅ **零实质性覆盖** — 全量 97 份文档 + ROADMAP 搜索 `dedup\|deduplicat\|content.address\|content.*hash.*store\|CAS.*store\|block.*dedup\|chunk.*dedup\|fingerprint.*dedup\|reference.*count\|hard.*link\|same.*content.*same.*blob\|重删\|去重` → **0 次匹配**。代码库中：`storage/storage.go` 的 `Storage` 接口无 `ContentHash` 方法；`file_crud.go` 的 `Put` 路径每次写入新 blob；repository 无 `content_hash` 或 `object_refcount` 等去重字段 |
| **方向四：对象内容缓存层次（内存→本地磁盘→CDN）** | ✅ **零实质性覆盖** — 全量 97 份文档中仅 v44 方向一「query result cache for search」讨论搜索缓存（`internal/ai/result_cache.go` 已实现），**非对象内容缓存**。ROADMAP 方向 2「observability」下有 embedding/search cache 但**非 object content cache**。v54 方向三「object cache layer」用 40 行讨论了通过反向代理（nginx/Varnish）做缓存的概念但**零代码架构分析**：未分析现有 `storage.Storage` 接口能否包装、未分析范围请求缓存、未分析缓存失效策略、未分析写路径缓存更新。搜索 `content.*cache\|object.*cache.*layer\|read.*through.*cache\|write.*through.*cache\|cache.*hierarch\|CDN.*integrat\|cache.*invalidat.*object\|cache.*stale\|cache.*warming\|对象缓存\|缓存层次` → **0 次匹配** |
| **方向五：主动-主动多区域复制与冲突解决** | ✅ **零实质性覆盖** — v95 方向一「服务端 COPY/MOVE」分析了单区域内的数据移动架构，v57 方向一「multi-region active-active」以 50 行概念框图讨论了 multi-master 但**无代码锚点、无现有 `replication` 包分析、无冲突模型设计、无 CRDT/LWW 方案比较、无元数据合并策略**。ROADMAP 方向 3「horizontal scale-out & HA」覆盖单副本 HA（events、leases、read-replicas）但**零区域级分布**。搜索 `active.active\|multi.master\|multi.region\|geo.replicat\|conflict.*resolut\|CRDT\|LWW.*merge\|last.writer.wins\|bi.directional.*replicat\|双活\|冲突解决` → 仅 v57 方向一有概念提及，**零代码级分析** |

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 关键代码锚点 |
|---|------|------|--------|---------|-------------|
| **1** | **多模态 AI 管线：超越纯文本的智能提取** | AI 能力/产品差异 | **P1** | 当前 `Extractor` 只输出纯文本；图片、音频、PDF 表格、电子表格、JSON/YAML 结构化数据全部被忽略或简化为 `ErrUnsupported` | `internal/ai/extractor.go:Extract` → `func(ctx, contentType string, r io.Reader) (string, error)` 仅返回纯文本；`internal/ai/indexer.go:handleExtractError` → `ErrUnsupported` 即跳过，无 fallback 策略；`internal/ai/extractor.go:NewDefaultExtractor` 仅注册 text/plain, text/markdown, text/csv, application/json 提取器，无图片/音频/PDF/电子表格；`internal/ai/extractor_remote.go` 远程提取器虽可代理给外部服务但协议设计仍为 `(contentType, body) → text` 的单向文本通道 |
| **2** | **元数据锚定语义搜索：结构化条件 + 向量检索融合** | 搜索能力/架构 | **P1** | 搜索只能按 tenant+bucket 过滤；无法按 tag、日期范围、大小、storage class、content type 等元数据条件筛选；Qdrant/pgvector 的 filter 能力未被 REST API 暴露 | `internal/ai/search.go:Request` → 只有 `Tenant`, `Bucket`, `Query`, `K`, `Mode`, 零元数据过滤字段；`internal/ai/vectorindex.go:VectorIndex.SearchVectors` → `(ctx, tenant, bucket, queryVec, limit)` 签名无 filter 参数；`internal/ai/qdrant.go:scopeFilter` → 只构建 `tenant_id` + `bucket` 过滤器，标签/日期/大小等条件硬编码不支持；`internal/ai/pgvector.go` → SQL 中的 WHERE 子句仅 `tenant=$1 AND bucket=$2`；`internal/api/rest/search.go:Search` handler → 只解析 `query`, `k`, `mode` 三个字段；`internal/repository/sql_objects.go:ListObjectsByTag` → 纯 SQL 标签过滤，与搜索管线完全隔离 |
| **3** | **内容寻址存储与块级去重** | 成本/架构 | **P1** | 相同内容重复上传消耗 N 倍存储空间；无内容哈希索引；每文件独立加密导致相同内容的密文不同无法去重 | `internal/storage/storage.go:Storage` 接口 → 无 `ContentHash/PutIfAbsent` 方法；`internal/service/file_crud.go:Put` → 无去重检查，始终写入新 blob；`internal/repository/repository.go:Object` → 无 `ContentHash/RefCount/IsDeduplicated` 字段；`internal/storage/local_write.go:writeObject` → 直接写入磁盘，无内容摘要；`internal/repository/sql_objects.go` → 无 `content_hash` 列或唯一索引 |
| **4** | **对象内容缓存层次：从内存到 CDN 的加速路径** | 性能/成本 | **P2** | 每次 GET 穿透到存储后端，无热点内容加速；云后端（S3/OSS/COS）的每一次 GET 都有网络延迟和出站费用；大文件频繁读取浪费带宽 | `internal/service/file_crud.go:Get` → 始终 `s.store.Get(ctx, obj.StorageKey)`，零缓存包装；`internal/storage/storage.go:Storage` 接口 → 无 `GetCached`/`GetWithCache` 方法；`internal/storage/local.go` → 无读缓存层；`internal/storage/s3.go` → 每次 GET 通过 SDK 发出 HTTP 请求；`internal/thumbnail/thumbnail.go` → 每次从存储读取原图再处理，无缩略图缓存；`internal/storage/factory.go` → 可以创建 `WithCache(backend, cfg)` 包装但未实现 |
| **5** | **主动-主动多区域复制与冲突解决** | 架构/SaaS 运营 | **P2** | 复制是单向的；元数据不复制；无冲突处理；跨区域读取必须访问主区域 | `internal/replication/replication.go:Worker` → 只监 listening for source events, copying to replica，无反向；`internal/replication/replication.go:ReplicateObjectByID` → 仅 `source.Get → replica.Put`，无元数据合并；`internal/events/bus.go` → 事件总线是 instance-local，`PostgresTransport` 用于本地跨实例但非跨区域；`internal/repository/repository.go:Repository` → 单 DB 连接，无区域感知；`internal/reconcile/job.go` → `leases` 表在单个 DB 内，不支持跨区域 |

---

## 方向一：多模态 AI 管线 — 超越纯文本的智能提取

### 现状与代码证据

当前 AI 提取管线定义在 `internal/ai/extractor.go`：

```go
type Extractor interface {
    Extract(ctx context.Context, contentType string, r io.Reader) (string, error)
}
```

这个接口的输出是**纯文本**。`NewDefaultExtractor()` 注册了有限的内置提取器：

| 注册的 Content-Type | 处理方式 |
|---------------------|---------|
| `text/plain` | 直接读为字符串 |
| `text/markdown` | 直接读为字符串 |
| `text/csv` | 直接读为字符串（CSV 的行列结构丢失） |
| `application/json` | 直接读为字符串（JSON 的嵌套结构丢失） |

所有其他类型（包括 `image/*`, `audio/*`, `video/*`, `application/pdf`, `application/vnd.openxmlformats-officedocument.*`, `application/zip` 等）被 `handleExtractError` 标记为 `ErrUnsupported` 并跳过，仅通过 `indexer_skip_total{reason="unsupported"}` 计数（`internal/ai/indexer.go:handleExtractError`）。

`RemoteExtractor`（`internal/ai/extractor_remote.go`）允许将提取委托给外部 HTTP 服务，但协议也是文本通道：POST `(contentType, body)` → 返回 `plainText`。没有结构化输出、没有媒体类型区分、没有二进制流。

### 为什么需要

作为一个 **AI-native 文件平台**，系统应当能理解用户上传的任何文件类型：

| 类型 | 使用场景 | 缺失能力 |
|------|---------|---------|
| `image/jpeg`, `image/png` | 文档扫描件、截图的 OCR 提取 | `Extractor` 不支持图像；`thumbnail` 只做尺寸缩放不提取文字 |
| `audio/*` (mp3, wav, flac, opus) | 会议录音、采访、播客的语音转文字 | 无任何音频处理 |
| `video/*` (mp4, avi, mkv) | 视频会议、课程录像的内容提取 | 无任何视频处理（提取音轨→转录→字幕） |
| `application/pdf` | 企业文档的核心载体 | PDF 作为二进制流传递，提取器报 `ErrUnsupported` |
| `application/vnd.openxmlformats-officedocument.spreadsheetml.sheet` (XLSX) | 财务报表、数据导出 | 无电子表格解析 |
| `application/vnd.openxmlformats-officedocument.presentationml.presentation` (PPTX) | 幻灯片 | 无演示文稿解析 |

### 产品价值

- **差异化竞争力** — 市场上大部分对象存储 + AI 搜索产品只支持纯文本。多模态提取是显著的产品差异化点。
- **用户感知价值** — 用户上传发票图片后立即可以搜索图片中的文字，这比"只支持 PDF 文字提取"强得多。
- **生态兼容** — 通过 `RemoteExtractor` 可以对接 Azure Document Intelligence、Google Document AI、AWS Textract、OpenAI Vision、Whisper 等商业服务，以及 Tesseract、whisper.cpp 等开源方案。

### 架构权衡与建议方案

**方案 A: 扩展 Extractor 接口（推荐）**

```go
type Extractor interface {
    // Extract 提取内容的可搜索文本表示。
    // 支持的类型：text/*, image/*, audio/*, application/pdf, etc.
    // 返回提取的文本和提取的结构化元数据（语言、文档结构等）。
    Extract(ctx context.Context, contentType string, r io.Reader) (ExtractResult, error)
}

type ExtractResult struct {
    Text       string            // 可搜索文本（主输出）
    Metadata   map[string]any    // 提取的结构化元数据（语言、布局、表格等）
    Segments   []Segment         // 可选的分段信息（各部分的边界+类型）
    Structured map[string]any    // 结构化数据（JSON/YAML 解析结果、表格行）
}

type Segment struct {
    Offset int
    Length int
    Type   string // "text", "table", "image", "heading"
    Label  string // 可选标签（如表格标题）
}
```

这允许：
- 纯文本提取器返回 `{Text: body}`（向后兼容）
- PDF 提取器返回 `{Text: extracted_text, Segments: [...], Metadata: {page_count: 12}}`
- 图片 OCR 提取器返回 `{Text: ocr_text, Metadata: {language: "zh-CN"}}`
- 音频提取器返回 `{Text: transcription, Segments: [{Offset: 0, Length: 60, Type: "text", Label: "Speaker A"}, ...]}`
- JSON/YAML 提取器返回 `{Text: ..., Structured: {...}}` 以便结构化搜索
- 电子表格提取器返回 `{Text: "每个单元格的文本串联", Structured: {rows: [...], columns: [...]}}`

**方案 B: Pipeline 化（更灵活但更重）**

引入 `TransformPipeline` 概念：上传对象经过一个可配置的阶段链：

```
upload → [AV scan] → [thumbnail] → [text extract] → [chunk] → [embed] → [index]
                                    → [OCR] → [transcribe] → [translate] → ...
```

每个 stage 独立且可插拔（类似 UNIX pipe），但工作量大幅增加。

**建议：** 短期采用方案 A（扩展 `ExtractResult` 为结构体），长期可向方案 B 演进。`RemoteExtractor` 天然支持方案 A 的扩展（只需扩展 HTTP 协议定义即可）。

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| 超大文件（GB 级） | 提取器应流式处理或设大小限制（如 50 MB 上限，超过则截取前 50 MB 或触发异步作业） |
| 音频/视频过长（>3h） | 超长时间媒体应分段转录，索引器按分段索引 |
| 加密/受保护 PDF | 提取器应返回 `ErrUnsupported` 而非崩溃 |
| 语言检测 | 提取的 `Metadata` 应包含语言信息，以便搜索时按语言过滤 |
| 提取失败降级 | 保持当前行为：skip + `IncIndexerSkip(reason)`，不阻断索引管线 |
| 远程提取器超时 | `RemoteExtractor` 应当有超时控制（从 `io.ReadAll` 改为有限读取） |

---

## 方向二：元数据锚定语义搜索 — 结构化条件 + 向量检索融合

### 现状与代码证据

`Search.Query` 的 Request 结构体（`internal/ai/search.go`）：

```go
type Request struct {
    Tenant string
    Bucket string // optional; empty = all buckets in tenant
    Query  string
    K      int
    Mode   string // "vector" (default) | "bm25" | "hybrid"
    Caller string
    ReqID  string
}
```

不支持任何元数据过滤。所有检索都是**全局语义匹配**后按 tenant+bucket 裁剪。

向量检索路径（`internal/ai/vectorindex.go`）：

```go
type VectorIndex interface {
    SearchVectors(ctx context.Context, tenant, bucket string, queryVec []float32, limit int) ([]SearchHit, error)
}
```

没有 `filter` 参数。Qdrant（`internal/ai/qdrant.go`）的 `scopeFilter` 硬编码了 `tenant_id` + `bucket` 条件：

```go
func scopeFilter(tenant, bucket string) qdrantFilter {
    f := qdrantFilter{
        Must: []qdrantMatch{{Key: "tenant_id", Match: qdrantMatchVal{Value: tenant}}},
    }
    if bucket != "" {
        f.Must = append(f.Must, qdrantMatch{Key: "bucket", Match: qdrantMatchVal{Value: bucket}})
    }
    return f
}
```

pgvector（`internal/ai/pgvector.go`）的 SQL 同样硬编码了 `WHERE tenant=$1 AND bucket=$2`。

REST API handler（`internal/api/rest/search.go`）只解析：

```go
type searchReq struct {
    Query  string `json:"query"`
    K      int    `json:"k"`
    Mode   string `json:"mode"`
}
```

无 tag filter、无 date range、无 size range、无 storage class 过滤。

### 为什么需要

用户实际的问题是：

| 用户提问（自然语言） | 当前系统行为 | 期望行为 |
|---------------------|-------------|---------|
| "找上个月上传的财务 PDF" | 返回所有包含"财务"语义的 chunks，从所有月份 | 搜索"财务"+ **时间范围**+ **contentType=pdf** |
| "查询 env=prod 的配置文档" | 搜索"配置文档"，混合所有环境的文档 | 搜索"配置文档"+ **tag:env=prod** |
| "给我看大于 10MB 的设计图" | 搜索"设计图"，返回所有文件（包括小图） | 搜索"设计图"+ **size>10MB** |
| "有哪些包含用户注册流程的新闻稿" | 搜索"用户注册流程"，返回技术文档和新闻稿混杂 | 搜索"用户注册流程"+ **文件类型=newsletter** |

### 产品价值

- **精准度飞跃** — 在特定域（时间、标签、类型、大小）内搜索，语义精度大幅提升
- **企业级搜索体验** — 对标 Google Drive、SharePoint、Confluence 的搜索体验（facets + filters）
- **减少 RAG 噪声** — LLM context window 有限，过滤掉无关文档意味着更好的回答质量

### 架构权衡与建议方案

**核心设计：** 在 `Search.Query` 中引入可选的 filter 参数，在向量检索阶段**下推过滤条件**到向量后端（Qdrant filter / pgvector WHERE），而非在应用层做后过滤。

```go
type SearchFilter struct {
    Bucket      string            // 现有
    Tags        map[string]string // tag key → value (精确匹配)
    ContentType string            // content type 前缀匹配
    MinSize     int64             // ≥
    MaxSize     int64             // ≤
    CreatedFrom string            // RFC3339
    CreatedTo   string            // RFC3339
    StorageClass string           // STANDARD, STANDARD_IA, etc.
}
```

**关键决策：** 过滤条件在 chunk 写入时嵌入 chunk metadata（使能向量后端的 filter），或在线 Join objects 表。

推荐**嵌入策略**：在 `InsertChunks` 时，将 Object 的 tags、content_type、storage_class、created_at 等关键字段**作为结构化字段写入 chunk 存储**：

- **Qdrant：** payload 中添加 `tags.{key}`, `content_type`, `storage_class`, `created_at_unix` 字段，Qdrant 的 filter 原生支持这些条件的组合
- **pgvector：** chunks 表新增 `content_type`, `storage_class`, `created_at` 列，在 SQL 的 WHERE 子句中同时过滤
- **BM25/in-memory：** 在应用层做后过滤（数据规模较小时可接受）

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| filter + hybrid 模式 | vector 侧用 filter 检索，BM25 侧同样用 filter（下推或后过滤），两边都过滤后 RRF 融合 |
| 空 filter | 当前行为（向后兼容） |
| 所有条件同时设置 | AND 语义，不同类型条件（tag, date, size）之间是 AND |
| 过滤后结果数为 0 | 返回空结果 + `hits: []`，不走 rerank |
| 向量模型漂移 | filter 条件在 EmbedModel 过滤之前评估，减少不必要的不匹配 |
| REST API 参数溢出 | 使用 `searchReq` 扩展字段 + `omitempty`，避免破坏现有客户端 |

---

## 方向三：内容寻址存储与块级去重

### 现状与代码证据

当前每次 `Put` 都创建一个新的存储 blob：

`internal/service/file_crud.go:Put` 直接调用 `s.store.Put(ctx, sk, reader, size, opts)`，从不检查内容是否已存在。

`internal/storage/storage.go:Storage` 接口没有内容寻址方法：

```go
type Storage interface {
    Put(ctx, key, r, size, opts) (ObjectInfo, error)
    Get(ctx, key) (io.ReadCloser, ObjectInfo, error)
    ...
}
```

没有 `PutIfAbsent(contentHash, size, opts) (ObjectInfo, created bool, error)` 或类似语义。

元数据模型（`internal/repository/repository.go`）也没有去重相关字段：

```go
type Object struct {
    ID           int64
    TenantID     string
    Bucket       string
    Key          string
    StorageKey   string     // 每个对象唯一，永不共享
    ...
}
```

没有 `ContentHash`、`RefCount`、`IsDeduplicated` 字段。

### 为什么需要

考虑以下场景：

| 场景 | 当前行为 | 期望行为 |
|------|---------|---------|
| 100 个团队各上传同一个 Docker 镜像层 | 100 × 2.3 GB = **230 GB** | **2.3 GB**（一次存储，100 个引用） |
| CI 系统每次构建上传同一个 node_modules.tar.gz | 每次构建 × 500 MB = 大量冗余 | 只存一次 |
| 一个文件的 20 个版本内容高度相似（如 Office 文档） | 20 × 完整 blob | 块级去重后只存变化部分（类似 Git） |
| 用户上传 `photo.jpg` 然后改名重新上传 | 两个完整拷贝 | 引用同一个 blob |

对于备份/归档/DevOps（CI artifacts, Docker layers, npm packages, build outputs）场景，去重是核心功能。

### 产品价值

- **存储成本降低 50-95%** — 取决于工作负载类型（CI artifacts 可达 95%，用户文件约 30-50%）
- **写入速度提升** — 内容已存在时不必传输和加密，只需要创建一个引用
- **合规辅助** — 相同内容的审计痕迹更清晰（所有引用共享同一个 content hash）

### 架构权衡与建议方案

**核心设计：** 引入 `content_hash` 概念，使用 SHA-256 作为默认哈希算法。

```go
// 写入路径
func (s *FileService) putWithDedup(ctx, tenant, bucket, key string, r io.Reader, size int64, opts PutOptions) (Object, error) {
    // 1. 流式计算 SHA-256
    hasher := sha256.New()
    tee := io.TeeReader(r, hasher)
    
    // 2. 写入临时 blob（需要先存才能知道 hash... 这有鸡生蛋问题）
    //    方案：两层 — 先用 tempfile 读流、计算 hash、再决定
    tmpFile, _ := os.CreateTemp(...)
    written, _ := io.Copy(tmpFile, tee)
    tmpFile.Seek(0, 0)
    contentHash := hex.EncodeToString(hasher.Sum(nil))
    
    // 3. 查 content_hash 表
    existing, found := s.repo.GetObjectByContentHash(ctx, contentHash)
    if found {
        // 内容已存在，创建引用
        tmpFile.Close()
        os.Remove(tmpFile.Name()) // 删除临时文件
        obj := Object{
            Key: key, ContentHash: contentHash, 
            StorageKey: existing.StorageKey, // 共享同一个 blob！
            RefCount:   s.repo.IncrementRefCount(ctx, contentHash),
        }
        return s.repo.InsertObject(ctx, obj)
    }
    
    // 4. 内容不存在，持久化到 storage
    info, _ := s.store.Put(ctx, contentHash, tmpFile, written, opts)
    // 注意：storage key 现在是 contentHash 而非 tenant/bucket/key 路径
    obj := Object{Key: key, ContentHash: contentHash, StorageKey: info.Key}
    return s.repo.InsertObject(ctx, obj)
}
```

**存储模型变化：**

| 层级 | 当前 | 去重后 |
|------|------|--------|
| `storage.Storage` | `Put(key, ...)` key = `tenant/bucket/key` | `Put(key, ...)` key = `<SHA-256>`，对象 metadata 记录 storage key |
| `repository.Object` | `StorageKey` = 唯一路径 | `StorageKey` = content hash（多个 Object 共享同一 key） |
| 删除 | 硬删除直接删除 blob | 硬删除先 `DecrementRefCount`，仅当引用计数降到 0 才删除 blob |
| 加密 | 每个 blob 独立加密（同一内容不同 key → 不同密文） | 去重与 SSE 冲突：同一内容必须生成相同密文才能共享。**解决：** 使用**确定性加密**（如 AES-SIV）或**加密级别下调至库级**（先加密再哈希/去重） |

**重要权衡：SSE 与去重的矛盾**

- 当前 SSE 使用 per-object 随机 nonce（AES-GCM），同一明文内容生成不同密文 → 无法去重
- **方案 1：** 先对明文做内容寻址（content hash on plaintext），去重，再加密存储（每个副本独立加密？不行，storage key 共享但加密 key 不同 → blob 不同）
- **方案 2：** 仅对**未加密**的 bucket 启用去重（实际可行：大部分 CI/备份工作负载不加密）
- **方案 3：** 使用确定性加密（AES-SIV），同一明文 + 同一 key → 同一密文

**建议：** 先支持非 SSE 模式下的去重，加密和去重的组合作为后续优化。

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| 大文件（>5GB）流式上传 | 使用临时文件 + hash 计算；或分块哈希 + Merkle tree |
| 并发上传同一内容 | 使用 `content_hash` 唯一约束 + `INSERT ... ON CONFLICT DO NOTHING` 的 advisory lock |
| 一个 blob 的所有引用被删除 | `RefCount` 降到 0 → 异步 GC 删除 blob |
| 版本控制 + 去重 | 版本 ID 不共享 blob；不同版本即使内容相同也需保留（versioned bucket 可选择性关闭去重） |
| 内容哈希冲突 | SHA-256 碰撞概率可忽略；可额外校验 `size` 作为二次确认 |

---

## 方向四：对象内容缓存层次 — 从内存到 CDN 的加速路径

### 现状与代码证据

当前 GET 路径（`internal/service/file_crud.go`）：

```go
func (s *FileService) Get(ctx, tenant, bucket, key string) (io.ReadCloser, Object, error) {
    obj, _ := s.repo.GetObject(ctx, tenant, bucket, key)     // DB 查询
    rc, _, _ := s.store.Get(ctx, obj.StorageKey)              // 存储读取
    return rc, obj, nil
}
```

**每次读取都穿透到存储后端。** 没有对象级别的缓存。

现有缓存仅用于 AI 管线：
- `internal/ai/caching_embedder.go` — embedding 结果缓存（`AI_EMBED_CACHE_SIZE`）
- `internal/ai/result_cache.go` — 搜索结果缓存（`AI_SEARCH_CACHE_SIZE`）

均**不是对象内容缓存**。

缩略图处理（`internal/thumbnail/thumbnail.go`）同样每次从存储读取原始图像。

文件系统（local）的 `Get` 直接 `os.Open`；S3 的 `Get` 每次都走一个 HTTP 请求。

### 为什么需要

| 场景 | 当前行为 | 期望行为 |
|------|---------|---------|
| 热门 PDF 文档被 1000 个用户同时阅读 | 1000 次 S3 GET 请求（高延迟 + 出站带宽费用） | 前几次穿透后缓存命中，后续直接从缓存返回 |
| 同一页面嵌入 20 张缩略图 | 20 次存储读取 + 缩略图计算 | 缩略图缓存直接返回 |
| 大文件（100 MB）频繁读取 | 每次完整读取，浪费后端/网络带宽 | 缓存热点部分（文件头、尾部）或整个文件 |

**成本分析：** 对于 S3 后端，每次 GET 都有出站数据传输费用（通常 $0.09/GB）。一个每天被读取 1000 次的 10 MB 文件，每月出站费用 ≈ $27。缓存可以减少 90%+ 的出站流量。

### 产品价值

- **显著的延迟降低** — 缓存命中时从 100-300ms（S3 GET）降到 1-5ms（内存读取）
- **出站带宽成本节省** — 对云存储后端（S3/OSS/COS）尤其显著
- **后端负载降低** — 存储层 QPS 大幅下降，延长后端寿命/减少限流可能
- **CDN 集成** — 与 CloudFront/CloudFlare 等 CDN 的 presigned URL 集成，实现全局加速

### 架构权衡与建议方案

**推荐：Cache-aside（延迟加载）模式包装 Storage 接口。**

```go
// storage/cache.go — 可选的缓存包装器
type CachedStorage struct {
    backend Storage           // 实际存储后端
    local   *cache.LocalCache // 本地缓存（LRU, TTL）
    config  CacheConfig
}

func (c *CachedStorage) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
    // 1. 尝试从本地缓存读取
    if entry, ok := c.local.Get(key); ok {
        c.metrics.Hit(key)
        return entry.Reader, entry.Info, nil
    }
    c.metrics.Miss(key)
    
    // 2. 缓存未命中 → 从后端读取
    rc, info, err := c.backend.Get(ctx, key)
    if err != nil {
        return nil, ObjectInfo{}, err
    }
    
    // 3. 如果对象在缓存策略范围内（大小、content-type、访问频率），写入缓存
    if c.shouldCache(info) {
        body, _ := io.ReadAll(rc)
        rc.Close()
        c.local.Set(key, cachedEntry{body: body, info: info}, ttlFor(info))
        return io.NopCloser(bytes.NewReader(body)), info, nil
    }
    return rc, info, nil
}
```

**缓存层次建议：**

| 层级 | 介质 | 容量 | 延迟 | 适用对象 |
|------|------|------|------|---------|
| L1: 内存 LRU | RAM | 可配置（100 MB - 2 GB） | < 1ms | 小对象（< 1MB）、高频访问 |
| L2: 本地磁盘 | NVMe/SSD | 可配置（10 GB - 200 GB） | ~1ms | 中等对象（1-100MB）、低频访问 |
| L3: CDN | CloudFront/CloudFlare | 无限 | ~50ms | 静态内容、媒体文件（全球分发） |

**CDN 集成路径：** 不使用 `storage.CachedStorage` 包装，而是在 `PresignGet` 返回 CDN URL + 缓存行为声明（Cache-Control, ETag）——让 CDN 决定缓存策略。当前 `PresignGet` 返回的是直接访问 URL，加个 CDN 域包装层即可。

**缩略图缓存：** 在 `internal/thumbnail/thumbnail.go` 中，当前每次读取原图重新计算。可以增加缩略图缓存层，以 `{storageKey}_{w}_{h}` 为 key 缓存已处理的缩略图（内存或本地磁盘）。

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| 大文件（> 缓存大小上限） | 不缓存完整文件；支持 Range 请求时只缓存热区（前 4MB + 后 4MB）或旁路缓存 |
| 缓存一致性与写操作 | PUT/Delete 后使对应缓存失效（`CachedStorage` 包装 `Put`/`Delete` 方法） |
| SSE 加密对象 | 解密后的明文不应缓存到非加密存储；只有在**加密缓存**中才安全。方案：缓存只对未加密对象启用 |
| 预签名 URL 绕过 | 预签名 URL 不走 `FileService.Get`，直接由 storage 处理 → 缓存不生效。需要 presign 路径也支持缓存（本地 presign 可直接返回缓存内容） |
| 缓存过期策略 | LRU + TTL（小文件短 TTL，大/静态文件长 TTL）；主动失效 + SIGUSR1 刷新 |
| 流式 Range 请求 | 缓存应支持 `Range` 请求的 byte-range 子集读取 |

---

## 方向五：主动-主动多区域复制与冲突解决

### 现状与代码证据

当前复制实现（`internal/replication/replication.go`）：

```go
type Worker struct {
    repo    repository.Repository
    src     storage.Storage
    dst     storage.Storage
    ...
}

func (w *Worker) ReplicateObjectByID(ctx, objectID) error {
    obj, _ := w.repo.GetObjectByID(ctx, objectID)
    rc, _, _ := w.src.Get(ctx, obj.StorageKey)   // 从源存储读取
    _, _ = w.dst.Put(ctx, obj.StorageKey, rc, obj.Size, ...) // 写入目标存储
    return nil
}
```

特点：
- **单向**：源 → 目标，无反方向
- **仅复制 blob**：元数据（tags, ACL, storage class）通过 storage 的 Metadata 保留，但 DB 层面的元数据（quota, bucket config）不复制
- **无冲突解决**：不处理同一对象在两个区域同时写入的场景
- **无拓扑感知**：不感知跨区域延迟、带宽、可用区

事件总线（`internal/events/bus.go`）的 `PostgresTransport` 用于本地跨实例广播，但：
- Postgres LISTEN/NOTIFY 的传播范围受限于同一 Postgres 实例（不支持 WAN 传输）
- 没有跨区域的事件转发机制

### 为什么需要

| 场景 | 当前行为 | 期望行为 |
|------|---------|---------|
| 美欧亚三地团队同时工作 | 所有写入必须回主区域（美），欧亚用户有 ~200ms 延迟 | 用户写入本地区域，数据异步复制到其他区域 |
| 主区域故障 | 系统只读（DB 是 SPOF），复制方向固定 | 其他区域自动接管写入，故障恢复后合并数据 |
| CDN 类的全局读取 | 跨区域读取需要回源区域 | 读本地副本（写扩展也读扩展，无需回源） |
| 全球分布式团队共享大文件 | 上传到美区，欧区用户读取延迟高 | 上传后自动复制到最近区域 |

### 产品价值

- **全球读取延迟降低** — 用户在最近区域读取数据，延迟从 200-300ms 降到 <30ms
- **区域故障容灾** — 任一区域故障不影响整体服务可用性
- **合规数据本地化** — 数据可以保留在特定区域（EU 数据不出 EU），同时全球可搜索元数据
- **写入吞吐翻倍** — 每个区域都可以写入，总写入吞吐量随区域数线性增长

### 架构权衡与建议方案

**基础架构变更：**

1. **元数据复制 + 事件传播**

当前元数据（`repository`）是单 DB。要实现主动-主动，需要：
- 每个区域有自己的 DB 实例
- 跨区域事件传播层（替代 `PostgresTransport` 的局限）

```
US-East Pod:                    EU-West Pod:
  FileService                     FileService
    |                                |
  Repository (PG US) ←→ [CDC/tunnel] →→ Repository (PG EU)
    |                                |
  Storage (S3 US)                 Storage (S3 EU)
    |                                |
  EventBus US ─── [Kafka/NATS] ──→ EventBus EU
```

事件在区域内走 Postgres LISTEN/NOTIFY，区域间走 Kafka/NATS（或简单的 HTTP 转发）：
- `object.created` 事件 → US EventBus → region transport → EU EventBus → EU indexer/replication worker

2. **冲突模型选择**

| 模型 | 优点 | 缺点 | 适用 |
|------|------|------|------|
| **Last-Writer-Wins (LWW)** | 简单、无协调、S3 风格 | 丢失旧写入、无回滚 | 大多数对象存储场景 |
| **CRDT** (Conflict-free Replicated Data Type) | 无冲突合并（tags, ACL, metadata 可合并） | 实现复杂、对象不适合 CRDT | 元数据（tags, ACL, bucket config） |
| **版本向量 + 显式合并** | 无数据丢失、可审计 | 需要用户介入合并、使用门槛高 | 合规/监管场景 |

**推荐：** 默认 LWW + 基于修改时间戳的序（拒绝 `updated_at < local_updated_at` 的复制事件），tags/metadata 用 CRDT（map merge：`map[string]string` 的 union）。

3. **读取一致性**

主动-主动的核心挑战："在 EU 写入后立即在 US 读取不到"。
- **不保证跨区域读己写**（`read-after-write` 最终一致）
- **单区域内保证**：区域内通过 events + immediate index update 实现
- **应用层可选**：关键读取使用 `x-aero-consistency-level: strong` header → 等待跨区域确认或回源读取

### 边界情况

| 场景 | 处理方式 |
|------|---------|
| 同一对象在两个区域同时写入 | LWW + timestamp（`UpdatedAt`），后写入者覆盖；audit_log 记录两个写入 |
| 网络分区恢复后的数据合并 | 分区期间每个区域的独立写入 → 按 timestamp LWW 比对；被覆盖的版本转为 version 保留（若 versioning 启用） |
| 删除冲突 | 区域 A 删除了对象，区域 B 同时修改了它 → LWW 决定删除赢还是修改赢。建议 **tombstone + grace period**（删除不立即传播，先标记，15min 后确认） |
| 区域级联故障 | 区域 A 故障 → 流量切到区域 B → 区域 B 的 storage 中可能没有区域 A 的最新数据（RPO = 事件传播延迟） |
| 跨区域带宽成本 | 复制事件 + blob 数据 = 双向出站流量。可限制单方向复制、按存储类配置、或使用区域级 CDN 缓存减少回源读取 |
| 已有单区域集群扩展为多区域 | 初始全量复制（类似 `ReindexStale` 但针对所有对象），然后增量事件同步 |

---

## 总结与优先级建议

| 方向 | 预估工作量 | 影响范围 | 风险 | 建议实施顺序 |
|------|-----------|---------|------|------------|
| **1. 多模态 AI 管线** | **M** (3-6 周，含 PDF/图片提取器 + extractor 接口扩展 + 远程协议扩展) | AI 层：`ai/extractor.go`, `ai/indexer.go` | 低（向后兼容，不破坏现有提取器） | **②** |
| **2. 元数据锚定搜索** | **M** (2-4 周，含 Request/Response 扩展 + Qdrant filter + pgvector WHERE + REST API) | 搜索层：`ai/search.go`, `ai/vectorindex.go`, `ai/qdrant.go`, `ai/pgvector.go`, `api/rest/search.go` | 中（Qdrant/pgvector 兼容性需验证） | ① **（最高 ROI，搜索是产品核心）** |
| **3. 内容去重** | **L** (4-8 周，含 storage 接口扩展 + repository schema 变更 + 迁移 + 加密协调) | 存储层 + 仓库层：`storage/storage.go`, `repository/`, `service/file_crud.go` | 高（SSE 冲突、并发控制、大文件流式哈希） | **③** |
| **4. 对象缓存层次** | **M** (2-4 周，含 cache wrapper + 配置 + 缩略图缓存 + CDN presign) | 存储层：`storage/cache.go`, `thumbnail/` | 中低（cache-aside 模式不改变现有接口） | ④ **（有缓存后用带宽费用降低）** |
| **5. 主动-主动多区域** | **XL** (8-16 周，含 CRDT/LWW 实现 + 跨区域事件传输 + DB 复制 + 网络) | 全局架构：`replication/`, `events/`, `repository/` | 高（网络拓扑、冲突模型、数据一致性） | **⑤**（需方向 4 就绪后） |

**建议的首批工作：** 方向二（元数据锚定搜索，最高 ROI，搜索体验提升最直接）+ 方向一（多模态管线，产品差异化）。这两个可以在不触及存储层的情况下独立推进。
