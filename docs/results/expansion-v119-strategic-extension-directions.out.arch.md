以下是我基于代码库深度阅读和策略扩展文档的架构分析。

---

# AeroVault 架构深度分析

## 1. 架构评估

### 1.1 设计质量

项目整体架构质量非常高，体现了成熟的工程决策。核心设计特征：

**一流的抽象边界。** `storage.Storage` 和 `repository.Repository` 接口定义简洁，方法集恰好完备，没有泄露底层实现的语义。协议适配器（REST、S3、WebDAV、MCP）薄且无状态，`FileService` 是唯一的业务逻辑入口。这种布局符合*六边形架构*（端口/适配器）模式，但通过共享进程减少间接调用，而非通过 gRPC/HTTP 进行过进程通信。

**事件溯源就绪。** `events.Bus` 将持久化（通过 `repo.InsertEvent`）与广播分离，订阅者可被丢弃但 DB 仍然是权威事实来源。`Deliver`/`Publish` 的分离通过 transport 钩子显式地预防了跨实例的广播风暴，这是一种远见的做法。

**作业与内联路径分离。** 索引器、AV、复制作业均可在作业池开启时异步执行，关闭时以内联方式运行。这维持了基线测试路径的简单性（零基础设施），同时为生产部署提供了耐久性。

**干净的启动装配。** `main.go` 中的阶段序列——`config → storage → repo → service → workers → middleware → router`——每个阶段可在早期失败，且不存在循环依赖。

### 1.2 架构债务与受限因素

尽管整体健康，但有几个限制因素将在 v0.2 规模下成为问题：

| 受限因素 | 严重程度 | 根本原因 |
|-----------|---------|----------|
| **单进程，无共享队列** | 高 | `jobs.Pool` 基于本地 `sync.WaitGroup`——第二实例会争抢 `jobs` 表，但无锁机制防止重复执行。作业内部存在副作用（`store.Delete`），这让幂等性变得脆弱。 |
| **`Storage` 无层级/恢复语义** | 高 | 接口假定所有对象都是热门的、可立即获取的。一旦引入 GLACIER 层，`Get(ctx, key) (io.ReadCloser, …)` 的签名将被打破。 |
| **`Repository.Object` 的锁模型过于简单** | 中 | `LockedUntil *time.Time` 将 COMPLIANCE（不可撤销）与 GOVERNANCE（可通过 scope 撤销）混为一谈。“锁”是二元的，但合规要求三值逻辑：{无, GOVERNANCE, COMPLIANCE}。 |
| **事件过滤在交付管道之外** | 中 | `NotificationRule.FilterKey` 已存储但未读取。在每个发布事件上添加前缀/后缀过滤是简单的，但延迟到调度时才做过滤意味着总线负载高于必要值。 |
| **索引器管道是同步且单一的** | 中 | `indexer.go` 中的 `extract → chunk → embed → sink` 是一个链。要注入 AI 富化，必须添加一个不影响索引器可用性的异步侧支（富化队列）。 |
| **BM25 索引仅存在于内存中** | 低 | `internal/ai/bm25.go` 基于 sync.Map。当节点重启时重建。pgFTS 后端存在但非默认。对于静态级数据量，这将是一个问题。 |
| **单层元数据查询** | 中 | 对象查询完全基于 SQL（`SELECT … WHERE bucket=? AND key LIKE ?`）。元数据/标签上的过滤不存在。对于多租户 UI，任何实质性的“搜索对象”功能都需要全文元数据索引。 |

### 1.3 值得肯定和应避免的做法

| ✅ 应继续做 | ❌ 不应引入 |
|---|---|
| 用接口分离协议、服务和持久化层 | 全局服务定位器或 DI 框架——显式装配更易于追踪 |
| 默认关闭的特性标志（I5） | 长寿命的特性分支——所有四个方向都应是累积性迁移 |
| 对 SQL 占位符的原子约束（I1） | ORM——当前的双 SQL 方言模式可进行可控的 SQL |
| 用 `HARNESS.md` 门禁进行 CI 优先验证 | 添加 Docker Compose 作为集成测试的必要条件——基线使用 `file:…` SQLite + tmpdir |

---

## 2. 扩展方向

### 2.1 对文档四个方向的评估

策略文档中的四个方向选择得当。以下是架构层面的评估和每个方向的完善建议：

---

### 方向 1：冷存储分层与生命周期转换

**为什么需要：** 核心 S3 差异化因素——无此功能则平台对于备份/归档在经济上不可行。`storage_class` 字段的模式就绪程度表明这是预期的路线图项目。

**架构挑战：**

1. **`Storage` 接口的契约破坏。** 当前的 `Get(ctx, key) (io.ReadCloser, ObjectInfo, error)` 承诺数据立即可用。GLACIER 恢复需要：
   - 一个新的结果状态：“对象已归档，需先恢复”
   - 一个 `Restore(key, days, tier) (RestoreRequestID, error)` 方法
   - 一个 `CheckRestore(key) (status, expiry, error)` 方法

   **选项 A（推荐）：** 向 `Storage` 接口添加 `RestoreInfo` 和 `RestoreObject` 方法。这使得 `FileService` 无需知晓后端恢复语义。
   **选项 B：** 在 `FileService` 层引入一个 `RestoreService` 抽象，将恢复编排与存储后端分离。对于期望也支持本地 FS 分层的系统来说，这种方式更清洁。

2. **生命周期引擎需要第二个维度。** `lifecycle.go` 目前只有逐出（`expire_after_days`）。要添加迁移，需要：

   ```
   BucketConfig.LifecycleRules → [{ID, Filter, Transitions: [{Days, StorageClass}], Expiration: {Days}}]
   ```

   目前的 `LifecycleJob` 是一个计数器驱动的逐出器。新增一个 `LifecycleTransitionJob` 或创建一个复合作业。

3. **本地 FS 的恢复语义。** 对于 `local` 后端，GLACIER 意味着什么？选项包括：压缩 + 异地盘，或滚出到归档目录。`local` 实现需要一种恢复语义，该语义在进程重启后仍然保留——可能是一个恢复状态文件和后台解压作业。

**对现有系统的影响：** 中低。存储接口新增方法，生命周期模式扩展，S3 兼容处理新增 `POST ?restore` 子资源。新增特性标记：`COLD_TIERING_ENABLED`。

**架构决策点：**

```
决策：Storage 接口是扩展还是包装远程后端？

选项 1：向 Storage 添加 Restore()/CheckRestore()
  优点：后端封装恢复逻辑；FileService 保持不变
  缺点：所有后端必须实现占位符；local 需要人为的 restic 层

选项 2：添加一个 ColdTierMiddleware 来包装 Storage
  优点：无接口污染；可在现有后端上分层
  缺点：额外的间接层；恢复编排与分层交织

建议：选择选项 1——local 实现可使用一个基于文件的侧车记录恢复状态，所有
  云后端使用原生 API。当需要时可添加包装器。
```

---

### 方向 2：对象锁合规监管 + SSE-C

**为什么需要：** SEC 17a-4、FINRA、HIPAA。无此功能则监管使用案例为零。

**架构挑战：**

1. **锁的三值模型。** `repository.Object.LockedUntil *time.Time` 需要迁移为：
    ```go
    type RetentionMode string
    const (
        RetentionNone        RetentionMode = ""
        RetentionGovernance  RetentionMode = "GOVERNANCE"
        RetentionCompliance  RetentionMode = "COMPLIANCE"
    )
    // 新增字段
    RetentionMode  *RetentionMode // nil = 未锁定
    RetentionUntil *time.Time     // nil = 未锁定
    LegalHold      bool
    ```
    迁移 0025。这是可空的三列新增，向后兼容。

2. **强制力层级。** COMPLIANCE 模式意味着即使是 `hardDeleteObject` 也必须被阻止，包括管理员操作。目前的 `FileService.hardDeleteObject` 路径是：
    ```
    service → repo.HardDeleteObject → store.Delete
    ```
    COMPLIANCE 强制力必须位于 `service` 层（位于 `repo.HardDeleteObject` 调用之前），因为 `repo.HardDeleteObject` 不知道 retention mode。这意味着 `FileService` 必须读取 retention mode 并做出决策，可能在 `LockedUntil` 检查之上增加一层。

3. **SSE-C 的密钥生命周期。** SSE-C 密钥每次请求都需要携带，不能存储在任意位置。这影响着：
    - `PutOptions`/`GetOptions`——每个都需要加密上下文
    - 多分片上传——每个 `UploadPart` 调用都需要密钥
    - 复制/版本恢复——目标可能使用不同密钥
    - 事件通知——*不*应包含密钥材料

    SSE-C 密钥必须在上下文链结束后立即从内存中清除。`PutOptions` 和 `GetOptions` 中的 `crypto.Key [32]byte` 应采用 `runtime.KeepAlive`/显式清零模式。

4. **存储后端签名变更。** 目前 `storage.PutOptions` 不加密。SSE-C 需要在存储层添加 AES-GCM 包装/解包，而 `local` 后端已经通过 `encrypt.go` 支持 SSES3。SSE-C 需要一个 `EncryptWithKey(key [32]byte, plaintext io.Reader) io.Reader` 包装器和对应的解密包装器。

**对现有系统的影响：** 中。模式迁移（可空列）、服务层强制逻辑、存储层加解密路径。SSE-C 对每个后端都是代码变更（包括 OSS/COS，它们各有自己的客户密钥字段）。

---

### 方向 3：真正的事件通知管道（SQS/SNS/Lambda）

**为什么需要：** 无此功能则存储平台是孤立的。`NotificationRule` 架构已显式建模了 ARN，但交付层为零。这是最易实现、具最高集成价值的增量。

**架构挑战：**

1. **AWS SDK 依赖决策。** 该二进制文件目前没有 AWS SDK 依赖（S3 后端使用它，但通过工厂）。对于 SQS/SNS/Lambda 交付，有两种选项：
    **选项 A（推荐）：** 为每个 AWS 服务使用 `github.com/aws/aws-sdk-go-v2` 子模块。成熟、经过实战检验、支持凭证链。
    **选项 B：** 使用通用 HTTP 封装器，将 SQS/SNS/Lambda 视为 HTTP 端点，适用于自托管场景。对于 AWS 原生来说更复杂，但不需要 SDK。

    选项 A 需要新增依赖项 `aws-sdk-go-v2/service/sqs`、`aws-sdk-go-v2/service/sns`、`aws-sdk-go-v2/service/lambda`。这是一个可接受的重量级依赖。

2. **交付需要新包。** `internal/notifications` 包需要三个交付器和一个路由器。路由器是 `events.Bus` 的订阅者，执行过滤，然后分发。
    ```
    EventBus → NotificationRouter → filter(key, eventType) → deliveryPlugin
    ```
    路由器不得阻塞事件总线（异步交付，可选重试）。

3. **DLQ 基础设施。** `webhook_failures` 表可作为模板——创建一个 `notification_failures` 表（或通过交付器类型进行泛化）。

**对现有系统的影响：** 低。新包，无模式变更（`NotificationRule` 已存储 ARN），事件总线不变，S3 兼容处理不变。新增特性标记：`EVENTS_SQS_ENABLED`、`EVENTS_SNS_ENABLED`、`EVENTS_LAMBDA_ENABLED`。

---

### 方向 4：AI 原生元数据管道

**为什么需要：** 这是产品的主要差异化优势。无竞品提供此功能。但需要负责任地构建——如果不能优雅地失败，则 LLM 调用不得阻塞索引。

**架构挑战：**

1. **富化与索引的分离。** 目前索引器是线性的：
    ```
    extract → chunk → embed → sink
    ```
    要添加 LLM 富化，需要：
    ```
    extract → [chunk → embed → sink] 并行 [enqueue enrichment → enrichment queue → LLM classify/summarize/tag → repo.SetObjectMetaKey]
    ```
    富化队列必须是*优先于索引器*的独立队列。索引器不得等待富化。

    **建议的架构：**
    ```
    事件 → IndexerJob → extract → chunk → embed → sink（同步，无 LLM）
        ↓（并行）
        富化队列 → EnricherWorker → {classify, summarize, tag} → repo（异步，有重试）
    ```

2. **`IndexEnricher` 接口。** 策略文档中定义为：
    ```go
    type IndexEnricher interface {
        Enrich(ctx context.Context, text string, obj Object) (*Enrichment, error)
    }
    ```
    这是一个好的开始，但需要：
    - 通过 `EnricherResult` 进行异步结果交付（富化作业可独立完成）
    - 按 bucket 配置的富化程序链（例如，bucket A = 分类 + 摘要，bucket B = 自定义提取）
    - 桶级 `AI_ENRICHMENT_ENABLED` + `EnrichmentRules []EnrichmentRule`

3. **富化结果的持久化。** 富化结果（标签、摘要、分类）必须持久化到可搜索的位置。选项：
    - 作为系统元数据键存储（`_aero_ai_summary`、`_aero_ai_tags`）——与现有模式兼容
    - 存储到单独的 `object_enrichment` 表中——支持更丰富的数据结构
    - 注入回向量索引的元数据中——混合检索可受益

    推荐：JSONB 列 `object_enrichment`（或用于 SQLite 的 `TEXT` 存储 JSON 格式），包含 `{summary, tags, classifications, custom}`，并在富化完成后通过 `repo.SetObjectMetaKey` 进行原子更新。

4. **敏感数据超限。** 文档正确标识了富化应在 PII 之后进行。添加：
    ```
    extract → PII redact → [chunk → embed] 并行 [enqueue enrichment after PII]
    ```
    PII 检测器管线已存在（`ai.PIIDetector`），但必须配置为先脱敏后富化，除非用户明确选择不脱敏。

**对现有系统的影响：** 中低。新富化队列（复用 `jobs.Queue`/`jobs.Pool` 模式）、新包或现有包中的接口、桶配置中的新字段、模式变更（JSONB 列）。AI 特性标记已存在（`AI_INDEX_ENABLED`），无需新标志。

---

### 方向 5（新增）：元数据查询引擎与 UI 搜索

策略文档未将*对象发现*作为一个独立方向，但它是一个跨领域的能力，影响所有四个方向。

**为什么需要：** 目前，用户可通过键前缀列出对象（`GET /v1/files?bucket=X&prefix=Y`）或通过向量/BM25 搜索。不存在元数据+标签+全文的混合查询。用户无法“搜索 2026 年 3 月之后创建、标签包含 'invoice'、大小大于 1MB 的对象”。此限制降低了 Web UI 的实用性。

**架构建议：**

1. **在 `repository.Repository` 中添加 `SearchObjects(ctx, filter ObjectFilter) ([]Object, error)` 方法。** `ObjectFilter` 包含 `Bucket`、`KeyPrefix`、`Tags`、`MetadataFields`、`CreatedAfter`/`Before`、`SizeRange`、`StorageClass`。

2. **向 `ObjectFilter` 添加 `Limit` + `Offset` + `OrderBy`。** 基于 SQL 实现可用于 SQLite（JSON 提取用于元数据过滤）和 Postgres（JSONB 操作符 + GIN 索引）。

3. **向 S3 兼容移植添加标准 `GET ?list-type=2` 扩展名。** 目前只有 `?list-type=1`。

4. **将此搜索能力暴露给 Web UI——** WebUI 目前通过语义搜索 UI 展示搜索，缺少基于元数据的浏览。此变更使其能与标签、类别等进行交互式过滤。

**对现有系统的影响：** 中。Repository 层面的变更，需新增迁移以添加元数据索引。无新依赖。可渐进式添加。

---

## 3. 接口设计建议

### 3.1 现有接口评估

| 接口 | 质量 | 建议变更 |
|---------|-------|-------------|
| `storage.Storage` | 良好，但将过期 | 添加 `Restore(ctx, key, days, tier)`、`RestoreStatus(ctx, key)`、`SupportsTiering() bool` |
| `repository.Repository` | 良好，但增长 | 将领域概念拆分为接口：`ObjectRepo`、`BucketRepo`、`EventRepo`、`JobRepo`、`AIChunkRepo`、`TenantRepo`。`Repository` 是组合接口 |
| `ai.Extractor` | 极好 | 保持原样。对于富化，添加 `ai.Enricher`，它是 `ai.Extractor` 的后处理 |
| `ai.Embedder` | 极好 | 保持原样 |
| `events.Bus` 的 `EventSink` | 极好 | 保持原样。过滤应被推入 `NotificationRouter`，而非总线 |

### 3.2 新接口定义

**冷存储：**

```go
// 可选接口；后端通过类型断言暴露
type ColdStorage interface {
    Restore(ctx context.Context, key string, days int32, tier string) (restoreID string, eta time.Duration, err error)
    RestoreStatus(ctx context.Context, key string) (status RestoreStatus, expiry time.Time, err error)
    SupportsTiering() bool
}

type RestoreStatus string
const (
    RestorePending  RestoreStatus = "PENDING"
    RestoreInFlight RestoreStatus = "IN_PROGRESS"
    Restored        RestoreStatus = "RESTORED"
    RestoreExpired  RestoreStatus = "EXPIRED"
)
```

**富化管道：**

```go
type Enricher interface {
    Enrich(ctx context.Context, text string, obj repository.Object) (*Enrichment, error)
}

type Enrichment struct {
    Tags            map[string]string `json:"tags,omitempty"`
    Summary         string            `json:"summary,omitempty"`
    Classifications []string          `json:"classifications,omitempty"`
    Custom          map[string]any    `json:"custom,omitempty"`    // 通过用户可编程提取程序
}

// 用于桶级别配置的富化规则
type EnrichmentRule struct {
    ID         string   `json:"id"`
    Prefix     string   `json:"prefix,omitempty"`
    Enrichers  []string `json:"enrichers"`   // "classifier", "summarizer", "tagger"
    PromptOverrides map[string]string `json:"prompt_overrides,omitempty"`
}

// 通过 Decorate 进行组合：
// combined := ai.DecorateEnricher(classifier, summarizer, tagger)
type DecoratedEnricher []Enricher
func (d DecoratedEnricher) Enrich(ctx context.Context, text string, obj repository.Object) (*Enrichment, error)
```

**通知交付：**

```go
type DeliveryPlugin interface {
    // String 用于诊断和指标标签
    String() string
    // Deliver 发送单一事件。不得阻塞调用方超过 5 秒。
    Deliver(ctx context.Context, rule repository.NotificationRule, evt repository.Event) error
}
```

### 3.3 向后兼容性策略

| 变更 | 兼容性策略 |
|----------|----------------|
| 向 `storage.Storage` 添加新方法 | 接口扩展——实现需要新方法。对于不支持分层的后端，`Restore` 返回 `ErrNotSupported`。编译时检查新增。 |
| 向 `repository.Object` 添加列 | 可空列——现有行取回 `nil`。服务代码必须处理 `RetentionMode == nil` 作为“无锁”。|
| 向 `BucketConfig` 添加字段 | 迁移新增列，默认 `""`，现有桶继续使用旧生命周期规则结构。 |
| 新 `internal/notifications` 包 | 无现有代码影响——所有路径通过新文件。 |

---

## 4. 技术选型

### 4.1 新增依赖评估

| 方向 | 候选依赖 | 评估 |
|-----------|-------------------|------|
| 通知（SQS） | `github.com/aws/aws-sdk-go-v2/service/sqs` | **必须。** 这是唯一正确的 AWS SQS SDK。对于非 AWS 队列，可在 DeliveryPlugin 抽象下通过通用 HTTP 层支持自托管 NATS/Redis。 |
| 通知（SNS） | `github.com/aws/aws-sdk-go-v2/service/sns` | **必须。** 如果添加 SQS，则 SNS 也应添加——它们使用相同的凭证链和 HTTP 客户端。 |
| 通知（Lambda） | `github.com/aws/aws-sdk-go-v2/service/lambda` | **可选。** 自托管场景已足够使用通用 HTTP 调用。如果 80% 的部署是自托管，则跳过 SDK，改用 HTTP 客户端，并将 Lambda 视为调用函数 URL 或 API 网关。 |
| AI 富化 | 无新增依赖 | LLM、提取器、嵌入器均已就位。仅需编排。 |
| 冷存储 | 无新增依赖 | 生命周期格式化扩展通用。后端原生 SDK 处理恢复。 |

### 4.2 AWS SDK 版本策略

库目前未锁定 AWS SDK 版本。S3 后端使用 `v2`。应使用 **单个 aws-sdk-go-v2 模块** 作为 `go.mod` 中的共享依赖：

```go
require (
    github.com/aws/aws-sdk-go-v2 v1.x
    github.com/aws/aws-sdk-go-v2/config v1.x
    github.com/aws/aws-sdk-go-v2/credentials v1.x
    github.com/aws/aws-sdk-go-v2/service/s3 v1.x
    // 当需要时：
    github.com/aws/aws-sdk-go-v2/service/sqs v1.x
    github.com/aws/aws-sdk-go-v2/service/sns v1.x
)
```

二进制文件大小将增加约 6 MB（每个服务子模块约 2 MB）。对于 v0.1 基线来说可接受。

### 4.3 自建 vs 采购

| 能力 | 自建 | 采购 | 决定 |
|-----------|---------|----------|---------|
| 事件通知端点 | SQS/SNS/Lambda 的轻型封装器 | 使用 Confluent Kafka | **自建。** 方向 3 中的 DeliveryPlugin 模式约 500 行。Kafka 对于用例来说过度了。 |
| LLM 分类 | 使用现有 ai.LLM 接口 | Anthropic/GPT API | **自建。** LLM 接口已就位。分类/摘要提示是模板——不到 200 行代码。 |
| SSE-C 加密 | 使用现有 encrypt.go 模式 | HashiCorp Vault | **自建。** 已在 SSES3 中使用的信封加密模式。SSE-C 是 AES-GCM 包装器的变体。 |
| 自定义提取器 DSL | JSONPath + LLM 提示 | AWS Textract | **自建。** Textract 的覆盖面远超需求。JSONPath + LLM 提示对于模式化非结构化数据提取来说是一个可接受的首次方案。 |

### 4.4 需要仔细考虑的中间件

- **`grokwild` — `github.com/aws/aws-sdk-go-v2/feature/s3/manager`** — 用于大型对象。目前多分片是手动的。如果添加 SQS/Lambda，此管理器可能会使事件代码更简洁，但当前处理是可接受的。

- **`tidwall/bhash` / 用于富化的纯 Go BM25 库**——目前的 `internal/ai/bm25.go` 简单但足够。仅在规模证明需要之前，无需替换。

---

## 5. 实施路线图

### 5.1 优先级矩阵

| 方向 | 业务价值 | 技术复杂度 | 依赖 | 优先排序 |
|-----------|-------------|----------------|----------------|----------|
| 方向 3：事件通知 | 高 | 低 | AWS SDK（可控） | **P1 — 第一** |
| 方向 1：冷存储 | 高 | 中 | Storage 接口扩展 | **P1 — 第二** |
| 方向 4：AI 元数据 | 非常高 | 中低 | 现有 LLM | **P2** |
| 方向 2：锁 + SSE-C | 低（小众合规） | 高 | 模式迁移 + 加密 | **P2** |
| 方向 5：元数据查询 | 中 | 低 | 无 | **P1 — 与方向 3 同步** |

调整后的顺序：

1. **元数据查询引擎（方向 5）**——2-3 天。WebUI 和 SDK 使用案例的直接影响。使其他所有方向更可用。
2. **事件通知（方向 3）**——5-7 天。集成商价值高。最安全的增量——零模式变更。
3. **冷存储分层（方向 1）**——7-10 天。架构风险最高（Storage 接口变体）。应在 v0.2 前完成。
4. **AI 原生元数据（方向 4）**——6-8 天。高差异化价值但取决于稳定的 AI 管道。
5. **对象锁合规 + SSE-C（方向 2）**——10-14 天。对于目标合规客户进入/留存的进入门槛。

### 5.2 阶段划分

**阶段 A（2 周）：元数据查询 + 事件通知**

```
里程碑 A1：repository.SearchObjects 实现（SQLite + Postgres）
里程碑 A2：GET /v1/files?search 端点公开对象过滤
里程碑 A3：WebUI 搜索选项卡支持元数据+标签过滤
里程碑 A4：NotificationDeliveryPlugin 接口
里程碑 A5：SQS 交付实现
里程碑 A6：SNS 交付实现
里程碑 A7：通用 HTTP 交付（自托管）实现
里程碑 A8：EventFilter 在前缀/后缀交付之前应用
```

**阶段 B（2 周）：冷存储分层**

```
里程碑 B1：向 Storage 接口添加 ColdStorage 扩展
里程碑 B2：local 后端恢复状态机（侧车文件）
里程碑 B3：S3 后端 GLACIER/DEEP_ARCHIVE 恢复
里程碑 B4：OSS/COS 后端分层支持
里程碑 B5：生命周期规则扩展支持 Transitions[]
里程碑 B6：S3 POST ?restore 子资源
里程碑 B7：恢复作业 + 轮询端点
里程碑 B8：存储类指标（telemetry.StorageClassGauge 已存在——扩展）
```

**阶段 C（2 周）：AI 原生元数据**

```
里程碑 C1：IndexEnricher 接口 + 富化作业队列
里程碑 C2：LLMClassifier、LLMSummarizer、LLMTagger 实现
里程碑 C3：桶级富化规则（BucketConfig.EnrichmentRules）
里程碑 C4：操作顺序：extract → PII → chunk|enrich（并行）
里程碑 C5：富化结果持久化 + SetObjectMetaKey
里程碑 C6：搜索 + WebUI 中的富化结果暴露
里程碑 C7：ai_enrichment_total{enricher, status} 指标
```

**阶段 D（2 周）：对象锁合规 + SSE-C**

```
里程碑 D1：模式迁移（retention_mode、retention_until、legal_hold 列）
里程碑 D2：GOVERNANCE/COMPLIANCE 强制逻辑（FileService 层）
里程碑 D3：S3 子资源：?legal-hold、?retention
里程碑 D4：x-amz-bypass-governance-retention 标头
里程碑 D5：SSE-C：存储层 AES-GCM 包装/解包
里程碑 D6：SSE-C：multipart + 复制
里程碑 D7：SSE-C：所有协议的标头解析
里程碑 D8：SSE-C：密钥清零 + 内存安全审查
```

### 5.3 风险与缓解策略

| 风险 | 可能性 | 影响 | 缓解 |
|------|------|---------|-----------|
| 冷存储恢复 API 对于 local 后端来说过于复杂 | 中 | 高 | 对于 local，将 GLACIER 定义为透明层——无需恢复，或者直接 ERROR。在文档中记录为“local 限制”。 |
| SSE-C 密钥在错误路径中泄露 | 低 | 严重 | 对 `PutOptions`/`GetOptions` 中的密钥字节使用 `runtime.KeepAlive` + `memclr`。使用 `runtime.SetFinalizer` 作为安全网。必须进行代码审查。 |
| LLM 富化成本超过价值 | 中 | 中 | 为 AI 富化使用独立的速率限制器 + 预算限制器（复用 `AI_TENANT_DAILY_BUDGET_USD`）。桶级富化规则默认关闭。 |
| SQS/SNS 交付增加事件延迟 | 低 | 中 | 交付器使用非阻塞通道。失败的交付被推送到 DLQ（独立表），不会阻塞事件总线。 |
| `retention_mode` 模式迁移在 SQLite 上失败 | 低 | 高 | 添加可空列（`ALTER TABLE … ADD COLUMN … TEXT`）。无默认值。现有行 = "未锁定"（nil）。高覆盖率的测试。 |

### 5.4 推荐的第一周细分

如果团队在阶段 A 开始（优先排序恰当的一步）：

| 日 | 任务 |
|---|------|
| 1 | `repository.SearchObjects` + 双 SQL 实现。兼容 SQLite/Postgres。 |
| 2 | `GET /v1/files?bucket=&prefix=&tags=&meta=&after=&before=&size_gt=&size_lt=&storage_class=` 端点。 |
| 3 | WebUI 搜索过滤 UI。`pkg/` 端点的 SDK 方法。 |
| 4 | `internal/notifications` 包结构和 `DeliveryPlugin` 接口。单元测试。 |
| 5 | SQS 交付器。当前作业池可用的 SQS 客户端工厂。 |
| 6 | SNS 交付器。自托管 HTTP 回退交付。 |
| 7 | 事件过滤（前缀/后缀匹配）。集成测试。度量标准（`notification_delivery_total`）。 |

**关键风险缓解：** 在所有 PR 中，无 CI 回归——`make check` 必须通过。基线测试使用 SQLite + local + 无网络 `ai.HashEmbedder`。

---

## 结束语

此代码库进入此迭代时具备异常干净的基础。四个（加上一个已识别的）方向每个开始于稳固的基础，策略文档准确地将现有能力映射到缺失的交付物。架构风险最低的地方是方向 3（通知，纯增量），最高的是方向 2（锁 + SSE-C，触及加密和强制力深度）。推荐的顺序——首先面向用户的价值，然后存储效率，然后 AI 差异化，然后合规——能够在不对稳定基线引入风险的情况下提供早期可见的价值。
