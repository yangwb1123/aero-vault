现在我有足够的信息来进行一份基于代码验证的深度审阅。让我提供正式反馈。

---

## 审阅：AeroVault v22 扩展方向文档 — 代码验证报告

整体评价：**这是一份高质量的战略分析文档**，五个方向选择合理、代码锚点精确度高、风险分析全面。以下是我通过逐一比对当前代码库后发现的**事实性错误、精度缺失和遗漏因素**。

---

### 🔴 事实性错误（需立即修正）

#### 1. 方向四（Multi-Modal）影响范围 — `Extractor` 接口已包含 `contentType`

> 文档声称：`Extractor` 接口需要扩展为 `Extract(ctx, r io.Reader, contentType string) (string, error)`

**实际代码** (`internal/ai/extractor.go:16-18`)：
```go
type Extractor interface {
    Extract(ctx context.Context, contentType string, r io.Reader) (string, error)
}
```

`contentType` **已经是接口的一部分**。`DefaultExtractor` 也已经在使用它进行路由（`text/*` vs `application/json` vs 二进制）。这个方向的「影响范围」中有严重的假阳性——所需接口变更不存在。实际上，你需要的是**替换 `DefaultExtractor` 的实现在二进制的 content-type 上 fallthrough 到 OCR**，而不是改接口签名。

#### 2. Appendix — `ListObjects` 已在使用 keyset pagination

> 文档声称：`ListObjects` 使用 `LIMIT/OFFSET` 翻页，建议改为 `WHERE id > last_id LIMIT N`

**实际代码** (`internal/repository/sql_objects.go:176`)：
```sql
WHERE tenant_id=$1 AND bucket=$2 AND deleted_at IS NULL AND key LIKE $3 AND key > $4
ORDER BY key ASC
LIMIT $5
```

**已经是 keyset pagination**（`key > $4`）。这完全是假阳性。文档应删除或更正此条目。

---

### 🟡 精度缺失（分析不精确但方向性正确）

#### 3. 方向四：Chunker 返回类型

> 文档多处说 `Chunk(text string) []Chunk`，例如 `Chunk` 结构体「只有 `Text` 字段」

**实际代码**：
```go
// internal/ai/chunker.go:14
func (c *Chunker) Chunk(text string) []string  // 返回 string 切片

// internal/repository/repository.go:141
type Chunk struct {
    Content    string        // 字段名是 Content 而非 Text
    Embedding  []float32
    Dim        int
    EmbedModel string
    ...
}
```

影响范围描述要求「`ai.Search` 检索的对象是 `Chunk`（只有 `Text` 字段）」— 实际该字段是 `Content`。小问题但不精确会降低分析可信度。

#### 4. 方向一：`ChunkCleaner` 模式引用模糊

> 文档：`ChunkCleaner` 模式已验证 → 新增 `EnrichmentHook` 类似模式

`ChunkCleaner` 实际上是 `Indexer.DeleteObjectChunks`，文件服务通过 `FileService.WithChunkCleaner(cleaner)` 注入——这是**一个回调方法而非可组合模式**。如果要新增 `EnrichmentHook`，建议直接引用 `FileService.WithChunkCleaner` 的现有注入模式，避免模糊的「类似模式」表述。

#### 5. 方向二的 API 提案缺少租户命名空间

> `POST /v1/vectors` — 「外部应用注册自己的向量」

提案中的 API 不包含租户隔离维度。当前系统的所有数据路径都是 `tenant → bucket → key`。外部向量需要 `source: external` 标记，但也应要求 `tenant` 上下文（从 middleware 取）。API 提案没有展示这一点——外部向量可能成为多租户隔离的漏洞。

---

### 🟠 遗漏的关键考量

#### 6. 方向一（AI Enrichment）：未讨论 LLM 调用成本管理

写入 `_aero_summary`、`_aero_category` 等 metadata 需要调用 LLM。每个文件上传=1+ LLM 调用。对于大批量上传的场景（如迁移导入、replication），成本会飞速增长。现有的 `AI_TENANT_DAILY_BUDGET_USD` (`internal/config/config_ai.go`) 用于 chat 层面的限流，但 enrichment 不受此控制。

**建议补充**：EnrichmentWorker 应：
- 读取 `AI_TENANT_DAILY_BUDGET_USD` 并计入同一预算池
- 提供 `AI_ENRICHMENT_MAX_FILE_SIZE` 跳过大型文件
- 提供 `AI_ENRICHMENT_SAMPLE_RATE 0-1` 用于概率采样

#### 7. 方向二（Vector API）：外部向量绕过了整个 FileService 事件管线

如果用户通过 `POST /v1/vectors` 直接写入向量，这些向量不和任何对象关联 → 不会触发 `object.created` 事件 → 不会被 Webhook、replication、antivirus 覆盖。文档提到的边界情况「外部应用删除向量但保留文件 → 引用断裂」是不足的——**真正的问题是没有事件**。

**建议补充**：新增 `vector.external.created` / `vector.external.deleted` 事件类型，让 Webhook 和审计日志也能覆盖外部向量操作。

#### 8. 方向三（User Functions）：WASM 的内存模型风险未讨论

> 文档：第一版可以只支持 WebAssembly 沙箱…安全、跨语言、可限制资源

Go 的 wasm 运行时（如 `wazero`）每个实例分配**独立线性内存**。默认 ~10MB/实例。如果有 100 个函数同时触发（例如 100 个文件批量上传），就是 1GB 内存瞬时占用。文档建议的 `MAX_MEMORY` 配置很好，但未讨论**函数实例池化**机制。

**建议补充**：职能 Worker 需要 `MaxConcurrentFunctions` 限制 + 等待队列（复用 `jobs.Queue` 的模式），避免突发 OOM。

#### 9. 方向四（Multi-Modal）：`DefaultExtractor` 已标记 content-type — 但 `Indexer` 未使用它做路由

当前 `indexer.go` 的处理流程：

```go
// internal/ai/indexer.go (line ~260)
text, err := ix.extractor.Extract(ctx, obj.ContentType, reader)
```

`ContentType` 已经从对象元数据取得并传入 `Extract` 接口，`DefaultExtractor` 用它在 `text/*` vs 二进制类型之间做选择。**问题不在接口，在实现**——`DefaultExtractor` 对 `application/pdf`、`image/` 等直接返回 `ErrUnsupported`。

方向四的分析应该聚焦于**替换/增强 extractor 实现**（Tika OCR、Tesseract、Whisper），而非声称需要改接口。

#### 10. 方向五（Collaboration）：`PresignGet` 已经是功能完备的分享机制，问题在管理层

> 文档：`PresignGet` 生成下载 URL，但无权限控制（任何知道 URL 的人都可以下载）

这是预签名 URL 的本质特征——它的设计就是「持有即访问」。文档所述的「分享链接管理」核心需求是：
- **生成层**：支持密码/次数/IP 限制（扩展 `PresignGet` 或新 API）
- **管理层**：CRUD 分享记录、撤销、追踪（需要 `shares` 表）
- **UI 层**：Web UI 中的分享对话框

文档正确地识别了这些，但将 `PresignGet` 标记为「无权限控制」有点误导——这是功能而非缺失。

---

### ✅ 验证通过的高价值分析

以下分析经代码验证**完全准确**：

| 方向 | 核心主张 | 验证结论 |
|------|---------|---------|
| 方向一 | Indexer 是只读的，永不写回 storage | ✅ `IndexObject` 路径确实只读，验证通过 |
| 方向一 | `SetObjectMetaKey` 存在且支持单键更新 | ✅ `repository/sql_objects.go:362` 验证通过 |
| 方向一 | Event Bus 就绪，Indexer 已是消费者 | ✅ `Bus.Subscribe` + `Indexer.Run` channel 模式 |
| 方向二 | `Embedder` 接口无外部 REST API 暴露 | ✅ 全库搜 `/v1/embed` / `/v1/vectors` 零命中 |
| 方向二 | `ai.Search` 只检索文件分块 | ✅ `SearchVectors` 调用路径验证通过 |
| 方向三 | 订阅者列表在 main.go 中硬编码 | ✅ `main.go:Subscribe` 注册模式可见 |
| 方向三 | jobs.Registry 只被 main.go 内部用 | ✅ `Registry` 无外部注册 API |
| 方向五 | 无文件分享系统 | ✅ 全库无 `shares` 表、无分享端点 |
| 附录 | `RestoreObject` 不触发事件 | ✅ `file_features.go:169` 仅委托 repo 无 publish |
| 附录 | `DeleteTenant` 不级联 | ✅ `tenants.go:73` 仅 `DELETE FROM tenants WHERE` |
| 附录 | `SetBucketLogging` 无实际写入 | ✅ 配置存了但无 `writeAccessLog` 消费者 |
| 附录 | MCP 工具列表硬编码 | ✅ `listTools()` 与 `callTool()` 在 `dispatch()` 中各自 switch |

---

### 📊 综合评分与建议

| 维度 | 评分 (1-5) | 备注 |
|------|-----------|------|
| 方向选择的战略价值 | ★★★★★ | 五个方向都精准对应 AI-Native 平台的关键跳跃点 |
| 代码锚点准确性 | ★★★★☆ | ~90% 准确，但方向四有接口签名的关键错误 |
| 边界情况覆盖 | ★★★★★ | 每个方向的边界表都很全面 |
| 工程估算可信度 | ★★★★☆ | 估算合理，但未考虑 WASM 内存模型和 LLM 成本管理 |
| 风险识别 | ★★★★☆ | 方向二三缺少事件循环防护和成本风险 |
| 附录扫描补充 | ★★★☆☆ | 三个条目的两个有事实错误 |

**建议动作**：
1. 修正方向四的「影响范围」——从「扩展接口签名」改为「扩展 extractor 实现层」
2. 删除附录中 `ListObjects`/`LIMIT/OFFSET` 条目（已是 keyset pagination）
3. 方向二 API 提案显式声明 `X-Aero-Tenant` header 必传
4. 方向一补充 LLM 费用上限与 `AI_TENANT_DAILY_BUDGET_USD` 联动
5. 方向三补充 `MaxConcurrentFunctions` 池化机制
