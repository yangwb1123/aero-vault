现在我已经全面了解了代码库。让我提供架构分析。

# 架构分析：AeroVault 扩展方向评估

---

## 1. 架构评估

### 1.1 当前架构优势

AeroVault 展现了值得关注的架构质量，其中许多源于评审的评估文档中强调的内容：

**分层清晰，职责分离明确。** 协议适配器（REST、S3-compat、WebDAV、MCP）非常薄，`FileService` 作为唯一的业务逻辑入口。这种模式在 S3 兼容存储系统中很常见，但执行得很好——每个适配器约为 300-900 行，而服务层处理配额控制、版本控制、对象锁和事件发布。`internal/service/file.go` 将 `storage.Storage` 和 `repository.Repository` 作为唯一依赖，是依赖反转原则的很好的应用。

**配置默认关闭的安全性是一大优点。** AI 功能、pgvector、Qdrant、事件、集群单例、保留和 WebDAV 都有各自独立的激活标志。这意味着默认的 SQLite + local-fs 路径保持最小且可预测——这对 CI 验证和社区上手至关重要。

**IAM 条件引擎的质量高于行业水平。** `auth/condition.go` 中的 `ConditionContext` / `CompileConditionSet` / `ConditionBlock` 设计比当前使用它的策略引擎要成熟得多。它支持 20+ 条件操作符、从 `ConditionContext.Get()` 的标准密钥解析，以及整个编译函数方法，使得评估速度快（尽管动态编译为 `func` 与解释遍历相比）。

### 1.2 关键架构差距

评审正确识别了主要的差距。这里我对*结构性问题*进行优先级排序：

| 问题 | 严重性 | 债务类型 |
|--------|----------|-------------|
| 策略引擎与条件模块出现分歧 | **严重** | 冗余 + 未使用的代码 |
| REST 和 S3 之间的重复桶策略检查 | 中等 | 技术债务 |
| `matchesConditions` 是硬编码的 `switch`，而非使用 20 操作符引擎 | **严重** | 功能债务 |
| `BucketConfig` 缺乏多个协议所需的字段（RequesterPays、ObjectOwnership） | 中等 | 功能债务 |
| S3-compat handler 没有条件写入 | 中等 | 协议缺口 |
| `checkBucketPolicy` 中的 `X-Forwarded-For` 缺失 | 低 | 运维债务 |
| CLI 没有 `--json`/`-o` 标志 | 低 | 开发者体验债务 |

### 1.3 最大的架构债务：策略与条件脱节

技术债务中*最严重的*是 `policy.go` 和 `condition.go` 之间的脱节。

`condition.go` 包含一个全功能的条件引擎，能处理 8 个系列的操作符（String、Numeric、Date、Bool、IpAddress、Arn、Binary、Like），编译为快速求值函数。它演示了在 `condition_test.go`（我尚未详细阅读，但测试存在）中的使用。

然而，`policy.go` 中的 `matchesConditions` 方法**仅**检查 `IpAddress`/`NotIpAddress`。它完全绕过了 `CompileConditionSet`、`ConditionBlock` 和 `ConditionContext` API。相反，它通过 `net.SplitHostPort` 手动提取 IP，调用一个本地 `ipInAnyCIDR` 函数，然后返回 `bool`。

这意味着任何写入 `"Condition": {"StringEquals": {"s3:x-amz-server-side-encryption": ["AES256"]}}` 的桶策略都会静默失败——条件被忽略，访问被允许。

`policy.go` 中有未使用的代码：`CompileConditionSet`、`ConditionContext`、`EvalContext`、`PolicyDecision`——这些类型在完整的 `policy.go` 中完全没有被引用。它们似乎是后来添加的，但集成从未完成。

架构修复很明显但非平凡：`policy.go` 的 `matchesConditions` 需要用一个基于 `CompileConditionSet` 的管道替换，该管道构建一个 `ConditionContext`，编译每个语句的条件块，然后批量评估它们。

---

## 2. 扩展方向

下面，我列出 5 个高价值架构扩展方向。它们故意与评审的方向重叠（为了完整性），但主要关注架构重塑，而非增量功能。

### 方向 1：统一策略引擎（取代 `matchesConditions`）

**为什么需要：** 当前的桶策略实施仅检查 IP 地址。AWS IAM 定义的 60+ 条件键（`s3:x-amz-server-side-encryption`、`s3:prefix`、`aws:SecureTransport`、`aws:CurrentTime`、`s3:RequestObjectTag/...`）完全无法使用。由于 `condition.go` 基础已经存在，差距在于集成，而非构建。

**核心挑战：**

1. **`EvalContext` 填充数据流：** `policy.go` 中的 `checkBucketPolicy` 从 `r.RemoteAddr` 提取 IP，但没有填充其他上下文字段。需要一个新的 `EvalContext` 构建步骤：
   ```
   r → extractRequestContext(r) → EvalContext → condition evaluation
   ```
   这个函数需要从请求中提取：SourceIP（有 X-Forwarded-For 支持）、SecureTransport、UserAgent、CurrentTime、请求标签、资源标签——大约 12-15 个字段。

2. **`X-Forwarded-For` 信任：** 当前的 `net.SplitHostPort(r.RemoteAddr)` 在反向代理后面是错误的。设计需要添加一个可信的代理头部链，可能是 `X-Forwarded-For`，或者一个配置的 `TRUSTED_PROXY_CIDRS` 环境变量。

3. **S3 特定键：** 某些条件键是 S3 特定的（例如 `s3:prefix`、`s3:delimiter`、`s3:x-amz-acl`）。这些键在策略评估点并不总是可用——`prefix` 在 `ListObjectsV2` 中，`x-amz-acl` 在 `PutObject` 请求头中。设计需要一个 `LazyContext` 模式，仅在需要时提取这些值。

4. **性能：** 每个请求评估策略可能会变慢，如果条件使用 `StringLike`（正则编译）或有多个条件块。一个按桶/租户的已编译策略缓存会有所帮助——但由于策略是 `BucketConfig` 的一部分，它们已经在 `repo.GetBucketConfig` 调用中被缓存。但条件评估应该编译（`CompileConditionSet` 已经返回一个 `func`）。

**建议的方法：**

```
Phase 1（1-2 天）：替换 policy.go 中的 matchesConditions 桥接到 condition.go
  1. 在 policy.go 中，使 Statement.Eval 使用 CompileConditionSet
  2. 在 checkBucketPolicy 中构建一个 EvalContext（从请求中填充）
  3. 添加 X-Forwarded-For 提取
  4. 保持现有的 IpAddress 回退以确保兼容性

Phase 2（2-3 天）：添加 S3 特定条件键
  1. 在 rest/s3compat handler 中的策略评估点收集 s3:prefix、s3:delimiter、s3:x-amz-acl
  2. 条件键的惰性求值

Phase 3（1-2 天）：添加条件键 + 已编译策略的指标
```

**对评估的说明：** 这与方向 1（条件键）部分重叠，但集中在对现有代码的架构重组，而非从头编写。`condition.go` 已经存在——它只需要与 `policy.go` 连接。这是一项**架构整合**，而非新功能开发。

### 方向 2：S3 条件写入 + REST-S3 条件统一

**为什么需要：** 当前，REST handler（`internal/api/rest/conditional.go`）对 `If-Match`/`If-None-Match` 有 `checkWritePreconditions`，而 S3 handler 的 `PutObject`、`DeleteObject` 和 `copyObject` 完全忽略条件标头。这意味着使用 S3 API 的客户端（SDK、MinIO 客户端）进行条件写入会静默失败（允许覆盖），而 REST API 客户端则会收到正确的 412。

**这也架构成本**：两个 handler 中有重复的条件逻辑（`rest/conditional.go` 与 `s3compat/conditional.go`），具有不同的 RFC 7232 评估顺序。`rest/conditional.go` 的 `checkWritePreconditions` 从 HEAD 返回的当前对象评估 `If-Match`/`If-None-Match`。S3 端根本没有。理想的架构是将条件写入前置条件检查下推到 `FileService`，这样两个协议都能获得统一的语义。

**核心挑战：**

1. **前置条件与操作原子性：** `FileService.Put` 不提供 "原子检查并覆盖" 原语。它写入字节，然后 upsert 元数据。在此期间，另一个请求可能已经修改了对象。对于条件写入，检查（stat）和修改（put）必须在同一个操作中，或使用版本化乐观锁。正确的实现需要 `FileService.PutIfMatch` / `PutIfNoneMatch` 或一个通用的 `PutWithPrecondition`，它在事务中执行 stat+check+write。

2. **`copyObject` 的 `x-amz-copy-source-if-*`：** S3 复制操作有自己的条件标头集（`x-amz-copy-source-if-match`、`-if-none-match`、`-if-modified-since`、`-if-unmodified-since`），它们独立于目标标头工作。当前 `copyObject` 处理程序发出 `h.svc.Get` + `h.svc.Put`，没有任何前置条件检查。源前置条件检查需要在复制开始时作为单独的 `Stat` 发生，如果失败，则返回 `412`/`304`。

3. **与 `Idempotency-Key` 交互：** REST handler 有一个幂等性中间件缓存。当 `If-Match` 和 `Idempotency-Key` 都存在时，幂等性缓存命中应该跳过条件检查（原始请求已经通过了）。幂等性缓存未命中应该进行全新的条件评估。

**建议的方法：**

```
Phase 1（2-3 天）：在 FileService 中添加条件写入支持
  1. 添加 service.PutOptions.IfMatch / IfNoneMatch 字段
  2. 修改 FileService.Put 以在写入前执行 stat+check+upsert（版本化乐观锁）
  3. 为 DeleteObject 添加相同的处理
  4. 更新 S3 handler 以从标头传递这些选项
  5. 更新 REST handler 以使用 FileService 的条件写入，而非内部检查

Phase 2（1-2 天）：copyObject 源前置条件
  1. 在 copyObject 源读取之前添加 x-amz-copy-source-if-* 评估
  2. 适当的 412/304 响应

Phase 3（1 天）：幂等性集成
  1. 在幂等性中间件中记录是否检查了前置条件
  2. 重用缓存响应时跳过条件检查
```

**对评估的说明：** 这方向验证了评估的结论（方向 3 是正确的）。它增加了一个架构转折：将条件写入检查不是放在 handler 中，而是放在 `FileService` 中，以确保两个协议的一致性。

### 方向 3：存储控制器（将协议适配与桶策略分离）

**为什么需要：** 当前，桶策略检查在协议适配器层重复（`rest/handler.go` 中的 `checkBucketPolicy` 和 `s3compat/handler.go` 中的 `checkBucketPolicy`）。它们是*同一逻辑的两个副本*。此外，WebDAV 和 MCP 适配器完全跳过桶策略检查——这是一个安全漏洞的现状。

正确的架构是将桶策略执行推送到 `FileService` 中，这是*所有*协议共享的唯一入口点。`FileService` 在执行任何操作之前检查策略，因此无论协议如何，策略都会被执行。

**核心挑战：**

1. **注入 `EvalContext`：** `FileService` 方法目前接收 `context.Context`、`tenant`、`bucket`、`key`。策略评估需要额外的请求上下文（源 IP、用户代理、传输安全性）。这些需要从 `context.Context` 中提取——这意味着中间件需要在上下文中设置 `RequestMetadata` 结构体，并且 `FileService` 需要读取它。

2. **在 MCP/WebDAV 层添加请求上下文：** 目前，MCP 通过 `slog` 记录器从 stdio 运行，没有 HTTP 上下文。WebDAV 在 chi 外部运行。两者都需要填充 `RequestMetadata` 以允许策略评估。

3. **操作与数据平面分离：** 一些桶策略操作（例如 `s3:PutBucketPolicy`）修改策略本身。如果 `FileService` 执行策略检查，策略更新操作需要豁免，以避免鸡-蛋问题。

4. **性能：** 为每个操作进行数据库读取策略的策略评估增加了延迟。幸运的是，`BucketConfig` 被频繁操作访问，所以一个配置缓存很常见。

**建议的方法：**

```
Phase 1（3-4 天）：引入 RequestMetadata 上下文
  1. 定义 internal/middleware.RequestMetadata{SourceIP, UserAgent, ...}
  2. 添加一个设置它的中间件（在所有协议中，包括 MCP stdio 和 WebDAV）
  3. 在 FileService 中添加一个可选的策略检查点，如果上下文有元数据则执行检查
  4. 使 REST/S3 handler 委派给 FileService，而非自行检查

Phase 2（2 天）：为 WebDAV 和 MCP 填充上下文
  1. 在 MCP stdio 模式下模拟 HTTP 头部（或使用配置值）
  2. 在 WebDAV handler 中添加 RequestMetadata 注入
```

**为什么这个方向有价值：** 它解决了一个关于评估的未解决问题（"REST handler 是否也需要执行桶策略检查？"），根本性地解决了这个问题。与其在两个 handler 中修复，不如直接解决根本问题：策略评估必须发生在所有协议共享的层。

### 方向 4：结构化对象查询 API（`POST /v1/objects/search`）

**为什么需要：** 当前，`/v1/search` 端点执行语义搜索（vector/BM25/hybrid），这在没有 AI 索引启用时不可用。`ListObjectsV2`（在 S3 handler 中）仅通过前缀/分隔符提供过滤操作。**没有基于对象元数据（如 tags、size、content-type、storage-class）进行过滤的通用方法。** 这意味着：

- REST API 用户在查询对象时无法做"给我所有 size > 1MB 且 tag=production 的对象"
- S3 客户端期望 `ListObjectsV2` 进行后缀过滤（没有前缀过滤）时必须获取所有对象并在本地过滤
- `sql_objects.go` 中的 `ListObjectsByTag` 已经在做客户端过滤（从 DB 获取全部，然后过滤）

**核心挑战：**

1. **SQL-原生与内存过滤：** 如评估所指出的，内存过滤在不同对象数量上的扩展性不同。对于小桶（<100K 个对象），内存过滤效果良好。对于大型桶，需要 SQL 中的 WHERE 子句。架构需要一个分层的过滤引擎：
   - **安全推送层：** 桶、前缀、精确键、`is_deleted`、`version_id`——总是在 SQL 中完成
   - **可选推送层：** tags、metadata、storage-class、size 范围——取决于数据库和索引（Postgres 可以通过 JSON 推送大多数；SQLite 可能需要内存）
   - **回退层：** 所有不匹配安全推送层的条件在 Go 内存中过滤

2. **SQL 注入保护：** 用户提供的查询（例如 Tag 值）必须参数化。库必须强制执行它——没有字符串拼接 `WHERE tag = '` + userInput + `'`。

3. **分页语义：** 内存过滤和 SQL 分页不能很好地组合。如果你在内存中过滤，然后需要第 3 页（按 size DESC 排序），你不能只做 `LIMIT 100 OFFSET 200`—因为你可能已经过滤掉了其中一些。协议必须支持基于游标的分页（例如 `?cursor=last_id` 和 `WHERE id > ? ORDER BY id`，然后是可选的内存过滤）。

**建议的方法：**

```
Phase 1（2-3 天）：构建 REST 查询端点
  1. 定义查询 DSL：{"bucket": "...", "prefix": "...", "filters": [...]}
  2. 实现带游标分页的两阶段过滤引擎
  3. 设置 MaxScan 阈值（例如 100K 行，之后 abort）
  4. 在 router.go 中通过 POST /v1/objects/search 暴露

Phase 2（2 天）：添加 SQL 推送功能
  1. 在 sql_objects.go 中添加 FilterObjects 方法
  2. 推送 tags、metadata、storage-class 的 SQL->JSON 支持（Postgres 和 SQLite）
  3. 如果安全推送层覆盖了所有条件，则跳过内存阶段

Phase 3（2 天）：S3 Select 兼容性
  1. 添加一个 S3 Select 兼容的子集端点：?select&select-type=2
  2. 将 SQL 表达式解析映射到查询 DSL
```

**对评估的说明：** 这直接对应于方向 5（结构化搜索），但为 REST 协议设计了一个特定的 API，而不是一个通用的搜索 API。关键的新见解是两层过滤引擎（SQL + 内存），以及基于游标的分页，而不是偏移量。

### 方向 5：可观察性驱动的弹性：Circuit Breaker & Degradation 框架

**为什么需要：** 系统有多个外部依赖——存储后端（S3/OSS/COS）、数据库（Postgres）、AI 服务（嵌入提供者、LLM、重排序器、远程提取器）。目前，这些服务中的所有故障都会作为错误冒泡给调用者。没有结构化退化：

- 如果嵌入提供者关闭，`/v1/chat` 应该退回到无嵌入模式（仅 BM25），而不是返回 500
- 如果 Postgres 关闭，存储（本地文件系统）应该仍然可以服务于 GET 请求
- 如果 LLM 提供者关闭，文件 CRUD 应该不受影响

当前代码有 `degraded` 布尔值（`AIHandler`），但它是一个手动维护的总开关："degraded = true → 503 on all AI endpoints"。没有自动触发或基于指标的回退。

**核心挑战：**

1. **依赖健康探测：** 用于监视依赖项处于不健康状态的机制。Go 中一种常见模式是 `health.CheckFunc`，它在单独的后台 goroutine 上定期探测（例如 ping Postgres、调用嵌入提供者的 /readyz）。但它需要断路器——不仅仅是健康检查——以便在连续失败后自动停止调用依赖项。

2. **断路器框架：** Go 标准库中有几个选项：
   - `sony/gobreaker` — 简单且成熟，但需要一个外部依赖
   - 使用 `sync/atomic` 和状态机（关闭→打开→半开）的自制实现
   - 一个包装器，将 `Storage` 和 `Repository` 接口与自动错误阈值包装在一起

3. **策略退化规则：** 退化逻辑与业务规则交织。例如："如果嵌入器不可用，则降级搜索以仅使用 BM25" 是一个需要在应用层编码的策略。如果嵌入器和 BM25 都不可用，则搜索应返回清晰的错误消息。

4. **指标传播：** 断路器状态需要暴露在 `/metrics`（跟踪 `up`/`down`/`half_open`），以便运维可以诊断部分服务故障。

**建议的方法：**

```
Phase 1（2-3 天）：核心断路器包装器
  1. 在 internal/resilience 中定义一个 CircuitBreaker 包装器
  2. 将其应用于 Storage 和 Repository 接口（内部包装，外部无影响）
  3. 添加 Prometheus 指标（circuit_breaker_state、circuit_breaker_trips_total）

Phase 2（2 天）：AI 依赖断路器
  1. 包装 Embedder、LLM、Reranker、Extractor
  2. 定义退化策略（search.go、chat.go 中的健康分支）
  3. 当 LLM 断路器打开时，聊天返回清晰的错误而非泄漏 500

Phase 3（1 天）：自动恢复
  1. 基于间隔的半打开→关闭转换
  2. 健康检查集成
  3. 运维通知（日志事件、可选的 webhook）
```

**为什么这个方向很有价值：** 它为经过充分研究但未实现系统归档了架构债务。当前，AI 组件主要是 nil-safe，但如果它们失败，就没有自动恢复路径。这项工作是 *SRE 原则*的系统化。

---

## 3. 接口设计建议

### 3.1 策略引擎接口（替换当前模式）

目前，策略评估的签名是：

```go
// 当前
func (p *Policy) Eval(action, sourceIP string) PolicyEffect
```

应该替换为：

```go
// 目标
func (p *Policy) Eval(ctx EvalContext) PolicyDecision
```

其中 `EvalContext` 包含 `ConditionContext`（已经在 `condition.go` 中定义）加上 `Action` 和 `ResourceARN`。关键变化：

- `sourceIP` 被嵌入在 `ConditionContext` 中，而不是作为一个独立的参数
- 返回类型变为 `PolicyDecision`（包含 `MatchedRule`、`IsExplicit`）以支持审计日志
- 条件评估通过 `CompileConditionSet` 全权委托

**向后兼容性：** 向 `Statement` 添加一个 `conditionFunc func(EvalContext) bool` 字段，如果存在则惰性编译。保持 `matchesConditions` 不变，直到所有调用者迁移。然后删除它。

### 3.2 文件服务条件写入接口

```go
// 当前
type PutOptions struct {
    ContentType  string
    Metadata     map[string]string
    ContentMD5   string
    StorageClass string
}

// 目标
type PutOptions struct {
    ContentType    string
    Metadata       map[string]string
    ContentMD5     string
    StorageClass   string
    IfMatch        *string // nil = 不检查
    IfNoneMatch    *string // nil = 不检查；"*" = 仅创建
    IfUnmodifiedSince *time.Time
    IfModifiedSince   *time.Time
}
```

这保持了对 `FileService.Put` 的向后兼容（添加字段时零值 = 未设置），并统一了 REST 和 S3 handler。当前的 REST handler 自行处理前置条件；我们应该将其下推到 `FileService`，使 `FileService.Put` 在写入 upsert 的事务中执行 stat+check+write。

### 3.3 结构化搜索的查询 DSL

我推荐一个灵活的 DSL，在需要时可以映射到 SQL：

```json
{
  "bucket": "my-bucket",
  "prefix": "logs/",
  "filters": [
    {"field": "size", "op": "gt", "value": 1048576},
    {"field": "content_type", "op": "in", "values": ["text/plain", "text/html"]},
    {"field": "tag:env", "op": "eq", "value": "production"}
  ],
  "order_by": "size",
  "order_dir": "desc",
  "cursor": "eyJsYXN0X2lkIjog...",
  "limit": 50
}
```

设计决策：带 `cursor` 游标的分页，而不是 `offset`，以避免大型结果集上的性能悬崖。

### 3.4 断路器包装器

`Storage` 和 `Repository` 接口已经是纯 Go 接口。断路器应该包装它们：

```go
// 在 internal/resilience 中
type CircuitBreakerStorage struct {
    inner  storage.Storage
    cb     *CircuitBreaker
}

func (c *CircuitBreakerStorage) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
    var rc io.ReadCloser
    var info storage.ObjectInfo
    err := c.cb.Execute(ctx, func() error {
        var innerErr error
        rc, info, innerErr = c.inner.Get(ctx, key)
        return innerErr
    })
    return rc, info, err
}
```

这意味着不需要修改任何服务层代码——只需在 `main.go` 的 `buildStorageFrom`/`buildRepoFrom` 中包装实际的后端。

---

## 4. 技术选型

### 4.1 需要新的依赖吗？

| 组件 | 自建与采购 | 推荐 |
|----------|-------------|---------|
| 断路器 | **自建** | 200 行纯状态机 + `sync/atomic`。标准库有需要的一切。 |
| 策略编译缓存 | **自建** | 一个 `sync.Map` 与定期失效。条件编译函数已经创建为 `func`。 |
| 热重载存储/DB | 无变化 | 当前固定启动。避免外部热重载。 |
| X-Forwarded-For 解析 | 标准库 | `net/http` 包含 `Request.Header`。`net` 包提供 `ParseCIDR`。足够了。 |
| 结构化搜索 SQL 推送 | Postgres JSON 函数 | `pgx` 和 `modernc.org/sqlite` 已有。无外部 JSON 库。 |

**结论：** 不需要新的外部依赖。所有 5 个扩展方向都可以仅使用标准库和当前 `go.mod` 中的内容来实现。这是评估中隐含的约束的一个优势：`I6 — Stdlib first` 在这里指导得很好。

### 4.2 一个值得考虑的外部依赖

如果状态扩展模式（方向 5：断路器）是必要的，我会推荐 **`sony/gobreaker`** 仅因为它经过了生产验证的测试。但这是一种便利，而不是必要——一个 200 行的 + `sync/atomic` 的状态机与有据可查的 `StateOpen`/`StateHalfOpen`/`StateClosed` 同样可靠，并且完全符合标准库优先的约束。

### 4.3 测试策略

每个方向都有不同的测试负担：

- **方向 1（统一策略）：** 重用现有的 `condition_test.go`，添加 `policy_test.go` 测试，验证条件键在策略评估中得到充分应用。关键测试：`StringEquals` 在策略中被忽略（当前行为），添加后拒绝访问。

- **方向 2（条件写入）：** `handler_test.go` 中的集成测试发送带有 `If-Match: "xyz"`（过期的 ETag）的 `PUT` 并期望 `412`。S3 handler 的独立测试，使用 `httptest`。

- **方向 3（存储控制器）：** 跨所有协议的集成测试：通过 REST PUT + 通过 WebDAV GET + 通过 S3 HEAD，验证桶策略是否在所有路径中得到尊重。

- **方向 4（结构化搜索）：** 需要 fixture 数据集（50 个具有不同标签和大小跨度的对象）。测试边界情况：空桶、仅游标有效、无效字段名称、`MaxScan` 触发。

- **方向 5（断路器）：** 需要依赖 InMemoryStorage 的单元测试，以及模拟 `storage/contract_test.go` 中包装器正确传递调用的集成测试。

---

## 5. 实施路线图

我赞同评审的 Phase 1 优先级（先条件写入，再 Requester Pays），但有重排：**方向 1（策略统一）与方向 2（条件写入）捆绑在一起**，因为它们共享代码——条件写入需要 `FileService` 级别的前置条件评估，而策略统一需要 `FileService` 级别的策略检查。它们都应该在 Phase 1 中完成，因为它们是 S3 协议完整性的根本。

### Phase 0 — 基础架构整合（2-3 天）

*“在添加功能之前先建立连接。”*

| 任务 | 范围 |
|------|-------|
| 用 `CompileConditionSet` 替换 `policy.go` 中的 `matchesConditions` | `internal/auth/policy.go` + `condition.go` |
| 在策略评估点构建 `EvalContext` | `internal/auth`, `internal/api/s3compat`, `internal/api/rest` |
| 添加 `X-Forwarded-For` 解析 | `internal/middleware` 或 `internal/auth` |
| 添加 `RequestMetadata` 上下文类型 | `internal/middleware/metadata.go` |

**可交付成果：** 桶策略现在尊重全部 20+ 条件操作符。`X-Forwarded-For` 在反向代理后面可以工作。协调 `policy.go` 与 `condition.go`。

### Phase 1 — 协议完整性与数据保护（4-5 天）

| 任务 | 范围 | 依赖 |
|------|-------|------------|
| 将条件写入前置条件下推到 `FileService` | `internal/service/file.go` + `internal/api/rest` + `s3compat` | 无 |
| 为 PUT/HEAD/DELETE 添加 S3 条件写入处理 | `internal/api/s3compat/handler.go` | Phase 0（策略统一） |
| 在 `FileService` 中添加桶策略执行（所有协议） | `internal/service/file.go` + `internal/middleware` | Phase 0（RequestMetadata） |
| Requester Pays 检查（`x-amz-request-payer`） | `internal/service/file.go` + `BucketConfig` | Phase 0（策略作为基础） |

**可交付成果：** S3 和 REST 上的条件写入均按 RFC 7232 工作。桶策略在所有协议中得到统一执行。为外部用户读取桶添加 Requester Pays 检查。

### Phase 2 — 操作效率（4-6 天）

| 任务 | 范围 | 依赖 |
|------|-------|------------|
| 构建结构化搜索端点 | `internal/api/rest/search.go`（新端点） | 无 |
| 两层过滤引擎（SQL + 内存） | `internal/repository/sql_objects.go` | 无 |
| 基于游标的分页 | `internal/api/rest` + `internal/repository` | 无 |
| CLI 命令扩展（桶策略、cors） | `internal/cli/cli_admin.go` | Phase 0（策略统一） |
| CLI `--json` 标志 | `internal/cli/cli*.go` | 无 |

**可交付成果：** `POST /v1/objects/search` 端点按标签、大小、mime 类型、storage-class 进行过滤。CLI 通过 `--json` 输出可脚本化。新的 `policy set`/`cors set`/`notification set` 命令。

### Phase 3 — 弹性与生产就绪（3-4 天）

| 任务 | 范围 | 依赖 |
|------|-------|------------|
| 依赖的断路器包装器 | `internal/resilience/circuitbreaker.go`（新包） | 无 |
| AI/存储/数据库的退化策略 | `internal/ai` + `internal/service` | 无 |
| 断路器和退化指标的 Prometheus 指标 | `internal/resilience` + `internal/telemetry` | 无 |
| 指标面板的 Grafana dashboard | `deploy/grafana/` | 无 |

**可交付成果：** AI/存储/数据库故障时的自动退化。`/metrics` 上的断路器状态。用于诊断部分故障的新 Grafana 面板。

### 路线图风险矩阵

| 风险 | 可能性 | 影响 | 缓解措施 |
|------|----------|----------|-------------|
| 策略统一破坏了现有策略 | 中等 | **严重**（桶静默拒绝访问） | 添加集成测试，为每个 AWS SDK 桶策略场景调用 `checkBucketPolicy`；使用功能标志分阶段回滚。 |
| 条件写入与幂等性冲突 | 低 | 中等（语义不明确） | 明确定义幂等性缓存命中的优先级规则（见评估建议）。 |
| `FileService` 策略检查为读操作增加了延迟 | 中等 | 低（<2ms） | 每个请求一次策略加载（`BucketConfig` 已经缓存）。如果在高 RPS 下成为瓶颈，添加本地策略编译缓存。 |
| 结构化搜索在小数据集上运行良好，但在大数据集上性能不佳 | 中等 | **高**（超时） | `MaxScan` 阈值 + 清晰的错误消息。在文档中记录扩展限制。 |
| 断路器在请求中保持打开状态 | 低 | 中等（请求失败） | 在检测到恢复后，复用半开→关闭逻辑（间隔基础上的成功）。 |

---

## 总结

评估的文档是正确的：已确定的五个方向是真正未知的、高价值的扩展。我的分析增加了三个架构洞察：

1. **`condition.go`/`policy.go` 脱节是首要的架构债务。** 在添加条件键引擎之前，固定这种集成。`condition.go` 中 20+ 操作符的丰富引擎被一个只检查 `IpAddress` 的策略引擎所闲置。这是遗留设计——修复它。

2. **在 `FileService` 中统一策略评估解决了 REST→S3→WebDAV→MCP 差距。** 与其在两个 handler 中慢慢修复策略执行，不如将其推送到服务层。这使得桶策略真正成为跨协议的执行机制。

3. **条件写入应从 `FileService` 向下推，而不是在 handler 间复制。** 协议之间的重复是一个架构代码异味——`FileService.PutOptions.IfMatch` 使其在所有适配器中统一。

上述路线图将实施分为三个阶段。前两个阶段（Phase 0 + Phase 1）专注于策略一致性和数据完整性——这些是**生产成熟度**领域，缺少它们使得系统容易被默默覆盖和静默策略违规。Phase 2 修复了开发者体验（CLI、结构化搜索）。Phase 3 增加了企业弹性（断路器、退化）。

按当前速度，这些阶段大约需要 **10-14 个开发日** 来完成并通过测试。考虑到约束条件（单文件 ≤500 行、圈复杂度 ≤10），CLI 扩展可能需要文件拆分以保持在限制内——但这是设计决定的，而非问题。
