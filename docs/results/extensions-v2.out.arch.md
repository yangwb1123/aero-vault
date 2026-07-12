Now I have a thorough understanding of the architecture. Let me provide the architectural analysis.

---

# 架构分析报告：aero-vault 存储平台

## 1. 架构评估

### 1.1 现有架构优势

**清晰的分层边界。** 系统在协议适配器（REST/S3/WebDAV/MCP）和核心 `FileService` 之间有一条明确的界线，而 `FileService` 又委托给 `Storage` 接口和 `Repository` 接口。这种分层在代码中是一致的——handler 从不直接调用存储层。

**接口驱动的最小依赖。** `FileService` 通过 `EventSink`（3 个方法）和 `ChunkCleaner`（1 个方法）的接口与事件系统耦合，而不是直接引用 `events.Bus`。`Replication.Worker` 同样定义了自己的 `Enqueuer` 接口。这种模式使得单元测试无需引导整个事件基础设施。

**Event Bus 的耐用性设计正确。** `Publish` 在广播前持久化事件，确保崩溃安全。`Deliver` 方法防止多副本回显风暴。每个订阅者一个缓冲通道的设计意味着慢消费者不会阻塞生产者——这是正确的取舍。

**Storage 接口的演进恰到好处。** 接口包含 `Get/Put/Delete/Stat/List/PresignGet/PresignPut` 以及完整的 multipart 原语（`InitMultipart`/`UploadPart`/`CompleteMultipart`/`AbortMultipart`）。这覆盖了 S3 兼容所需的所有操作。

**功能门控模式设计良好。** AI/pgvector/Qdrant/webhook/复制等功能通过配置 flag 激活，`nil` 组件不会破坏核心 CRUD 路径。这符合 AGENTS.md 中 I5 的硬性不变量。

### 1.2 关键架构债务

**债务 1：Storage 接口缺少 `Copy`/`Move`/`RestoreFromStorage`。** 分析文档中已指出这一点。当前所有跨后端操作都必须走服务层（`Get` + `Put`），这对于大对象来说效率低下且缺乏原子性。随着存储类分层（方向二）的推进，这将成为性能瓶颈。

**债务 2：跨协议的安全策略不一致。** S3 handler 在 `PutObject`/`GetObject`/`DeleteObject`/`HeadObject` 中调用 `checkBucketPolicy`，但 REST handler 从未调用。同样，ACL 检查在 REST handler 中仅以 `allowAnonymous` 的形式存在（仅用于 `Get`/`Head`，不用于 `Put`/`Delete`/`List`）。已验证用户的访问在 REST 中完全没有 ACL 限制。这是**架构层面的安全风险**，因为 REST API 和 S3 API 挂载在同一个二进制文件中的同一 `FileService` 上。

**债务 3：法律保留作为元数据 hack。** `_aero_legal_hold` 作为元数据键值对存储，没有专用的数据库列。这意味着：无法通过 SQL 高效查询、没有索引、生命周期 GC 不知道法律保留状态、没有专用的 API 端点（仅依赖于 `PUT /v1/files/*/tags {“_aero_legal_hold”:“ON”}` 这样的副作用）。

**债务 4：对象锁定缺少 governance/compliance 模式。** `BucketConfig.ObjectLockSeconds` 存在，但缺少 `ObjectLockMode`（`governance` vs `compliance`）。在 governance 模式下，具有适当权限的用户可以在保留期结束前覆盖锁——这需要 `bypass-governance-retention` 头。这在 S3 兼容性方面是一个明显的空白。

**债务 5：生命周期仅支持过期，不支持转换。** `BucketConfig` 有 `ExpireAfterDays` + `ExpireAction`（`soft_delete`/`hard_delete`），但没有 `Transitions []TransitionRule`。存储类分层（`STANDARD` → `STANDARD_IA` → `GLACIER`）是生产级 S3 兼容系统的标配功能。

**债务 6：通知分发器缺失。** `BucketConfig.NotificationRules` 数据模型支持 `QueueArn`/`TopicArn`/`LambdaFunctionArn`，但没有分发逻辑。当前 `Bus.Publish` 将事件持久化后广播给本地订阅者——没有外部 HTTP 分发（按事件类型/前缀过滤）。Webhook 是独立的（全局，非按 bucket），且没有响应分发功能。

**债务 7：Middleware 链顺序在代码和文档之间不一致。** AGENTS.md 中声明的顺序是 `RequestID→CORS→Auth→Tenant→RateLimit→OTel→Recoverer→AccessLog`，但 `applyMiddleware` 中的实际顺序是反向的（构建时先添加最后一个中间件）：`access_log` → `concurrency` → `recoverer` → `otel` → `rate_limit` → `tenant` → `auth` → `cors` → `request_id`。由于中间件是作为外到内的洋葱模型包裹的，执行顺序是 `request_id → cors → auth → tenant → rate_limit → otel → recoverer → concurrency → access_log`。这与文档所说的一致。但需要注意，`concurrency` limiter 在 `auth` 之后但在 `rate_limit` 之前——这与文档的 `RateLimit → ... → Recoverer → AccessLog` 不同。

---

## 2. 高价值扩展方向

### 方向 A：统一访问控制层（P0 — 安全基线）

**为什么需要：** 目前 REST API 缺少策略执行和全面的 ACL 检查。考虑到 REST API 是默认的管理接口，这构成了一个被利用即可能导致数据泄露或越权写入的漏洞。

**核心挑战：**
- 在不破坏现有已验证用户流程的情况下添加策略检查
- 在 REST 和 S3 之间保持策略语义一致（但 REST 的路径结构与 S3 的 `{bucket}/{key}` 不同）
- 性能：每个请求的策略解析（JSON 反序列化 + 评估）不应引入显著延迟

**预期架构变更：**
1. 将 `checkBucketPolicy` 逻辑从 S3 handler 提升到共享层——要么作为 `FileService` 上的方法，要么作为独立的 `auth.PolicyEvaluator` 服务
2. 在 REST `Get`/`Put`/`Delete`/`List` handler 中添加策略检查调用
3. 在 REST handler 的已验证路径中添加 ACL 检查（当前缺失）
4. 考虑在 `FileService` 层添加统一的授权 gating（`canRead`/`canWrite`/`canDelete`），这样所有协议适配器自动继承

**对现有系统的影响：** 低风险，高度局部化。S3 handler 已有参考实现。主要工作是：在 `FileService` 中添加 `Authorize(ctx, action, bucket, key)` 方法，并更新 REST handler 以调用它。

**选项分析：**

| 选项 | 复杂性 | 安全性 | 性能影响 |
|------|--------|--------|---------|
| A1：在 FileService 添加 `Authorize` gating | 低 | 高（统一） | 最小 |
| A2：仅拷贝 S3 的 checkBucketPolicy 到 REST | 低 | 中（REST 仍缺少 ACL） | 最小 |
| A3：添加完整的 ABAC/RBAC 引擎 | 高 | 最高 | 中等 |

**建议：A1**，因为它为所有协议提供了统一的入口点，并且可以逐步扩展。

### 方向 B：S3 事件通知分发器（P0 — S3 兼容性影响最大）

**为什么需要：** 这是与 AWS S3 API 的最大差距之一。没有通知，用户就无法构建事件驱动的工作流。现有的 `BucketConfig.NotificationRules` 模式已经存在——实现缺失的部分是需要填补的部分。

**核心挑战：**
- 通知按 bucket 过滤（事件类型 + 前缀），而当前 webhook 是全局的
- SQS/SNS/Lambda 目标需要各自的 SDK（SQS 需要 SigV4 签名）
- 分发必须可靠（失败队列 + 重试）

**预期架构变更：**
1. 新的 `internal/events/dispatcher.go`：消费 Bus 事件，根据 `BucketConfig.NotificationRules` 过滤，分发给目标
2. HTTP 适配器（阶段 1）：对 HTTP 端点复用 webhook 的递送逻辑，但增加按 bucket 过滤
3. SQS 适配器（阶段 2）：实现 `transport` 接口，使用 AWS SDK 发送
4. 集成点：`main.go` 中 `bus.Subscribe()` + `dispatcher.Run(ctx, sub)`

**架构影响：**

```
EventBus.Publish → dispatcher.Run(ctx, sub)
    ↓ 过滤 (事件类型 + 前缀)
    ↓
    ├─ HTTP 端点 → webhook 复用
    ├─ SQS (未来) → AWS SDK
    └─ SNS (未来) → AWS SDK
```

报告中的建议是正确的——从仅 HTTP 分发开始，再处理 SQS/SNS。

### 方向 C：存储类分层与生命周期转换（P1 — 成本优化引擎）

**为什么需要：** 这是生产级存储系统中节省成本的主要手段。没有 `Transitions`，所有对象只能有一个存储类，用户需要进行手动工作流。

**核心挑战：**
- `Storage` 接口当前没有 `Copy` 或 `Move`——跨后端分层需要扩展接口
- S3 内部的分层（`STANDARD` → `STANDARD_IA`）可以使用原生的 `CopyObject`，但对等后端分层（local → S3）需要 `Get + Put`
- 恢复（Glacier → 可读）需要与软删除恢复不同的端点
- `S3 RestoreObject` 操作（`POST /{bucket}/{key}?restore`）完全缺失——当前实现的是软删除恢复

**预期架构变更：**

```go
// 新增 Storage 接口方法
type Storage interface {
    // ... 现有方法 ...
    Copy(ctx, srcKey, dstKey string) (ObjectInfo, error)       // 内部后端拷贝
    Move(ctx, srcKey, dstKey string) (ObjectInfo, error)       // 内部后端重命名（可选）
    RestoreFromArchive(ctx, key string, days int) error        // Glacier 恢复
}
```

`BucketConfig` 新增：
```go
type TransitionRule struct {
    Days          int    // 对象创建后的天数
    StorageClass  string // 目标存储类
}
type BucketConfig struct {
    // ... 现有字段 ...
    Transitions []TransitionRule  // 新增
    // RestoreObject 需要：
}
```

生命周期作业需要扩展以处理转换（而不仅仅是过期）。

**对现有系统的影响：** 中等。`Storage` 接口变化需要所有后端实现。`BucketConfig` schema 变更需要双迁移文件。

### 方向 D：多模态 AI Pipeline（P1 — 功能扩展）

**为什么需要：** 当前索引器仅处理文本/JSON/XML。图像、音频和 PDF 文档是知识库中的主要内容类型。添加多模态支持可以使搜索适用于 PDF 扫描件、产品图片和录制的会议。

**核心挑战：**
- 图像嵌入需要 CLIP/SigLIP 模型（非标准 embeddings）和图像解码（`image/jpeg` 等）
- PDF 提取需要 PDF 解析器（`go-text`/`unidoc` 或调用外部服务）
- 向量索引需要兼容多模态 embedding 维度（CLIP 通常输出 512 维，而当前默认是 256 维）
- Agent 工具需要新增 `search_images`、`extract_text_from_image` 等

**预期架构变更：**
1. 新的 `ai.MultiModalExtractor`：按 MIME 类型分发（`text/*` → 现有提取器；`image/*` → 图像解码 + 标题生成；`application/pdf` → 文本提取）
2. `Storage` 接口在当前 Embedder 之上新增 `EmbedImage(ctx, image []byte) ([]float32, error)`
3. 索引器更新为按对象类型选择提取器
4. Agent 新增 `search_images` 和 `extract_text` 工具

**选项分析：**

| 选项 | 优势 | 劣势 |
|------|------|------|
| CLIP 本地嵌入 | 延迟低，无外部依赖 | 需要加载模型（大内存） |
| 远程提取器（`AI_EXTRACTOR_ENDPOINT`） | 支持任意格式，轻量级 | 依赖外部服务，增加延迟 |
| `go/ast` + `go/parser` 用于 Go 代码分块 | 标准库，零依赖 | 仅限于 Go，语法感知有限 |

建议从远程提取器扩展开始（系统中已有 `RemoteExtractor` 接口），再添加本地 CLIP 支持作为第二阶段。

### 方向 E：Storage 接口的多后端 Copy/Move 基础设施（P2 — 平台扩展性）

**为什么需要：** 这是一个基础性的基础设施增强，使得方向 B（复制）和方向 C（分层）都需要它。当前的 `Get+Put` 循环对于大于几 MB 的对象来说效率低下，且缺乏服务器端拷贝的原子性保证。

**核心挑战：**
- 在后端内（S3→S3）：可以使用 SDK 的原生 `CopyObject`——高效、原子、服务器端
- 跨后端（local→S3）：必须 `Get` + `Put`，效率较低——但可以流式传输而非缓冲到磁盘
- 跨后端操作的错误语义（部分拷贝的清理）

**预期架构变更：**

`Storage` 接口扩展：
```go
type CopyOptions struct {
    Metadata      map[string]string
    ContentType   string
    StorageClass  string
}

type Storage interface {
    // ... 现有方法 ...
    Copy(ctx, srcKey, dstKey string, opts CopyOptions) (ObjectInfo, error)
    // CopyAcross 用于跨后端拷贝，由服务层管理
}
```

新增 `Storage.Copy` 的可选支持——后端在不支持时可以返回 `ErrNotImplemented`，然后服务层回退到 `Get+Put`。

**架构模式：** 对于跨后端操作，在 `service.FileService` 中实现 `BackendCopy(ctx, srcBackend, dstBackend, srcKey, dstKey)`，检查是否是同一后端（委托给 `Copy`），否则使用流式 `Get+Put`。

---

## 3. 接口设计建议

### 3.1 接口设计原则

**原则 1：面向最小有用接口编程。** 当前模式——`FileService` 定义 `EventSink`（3 个方法）和 `ChunkCleaner`（1 个方法）——是正确的。应对 `Copy`/`Move` 采用相同方式：定义小接口，接受可选实现，提供默认回退。

**原则 2：授权应该是核心横切关注点。** `FileService` 应该有一个 `Authorizer` 接口：

```go
type Authorizer interface {
    Authorize(ctx context.Context, action Action, resource Resource) error
}
```

`FileService` 中的每个 CRUD 方法都应该调用 `s.auth.Authorize(ctx, action, resource)`。默认实现（`AllowAllAuthorizer`）保持向后兼容性。`auth.Registry` 或 `PolicyAuthorizer` 提供实际执行。

**原则 3：Storage 接口应该分为核心方法和可选扩展。** 当前接口是两者混合的。更好的做法：

```go
// 定义核心接口
type Storage interface {
    Get(ctx, key) → (io.ReadCloser, ObjectInfo, error)
    Put(ctx, key, reader, size, opts) → (ObjectInfo, error)
    Delete(ctx, key) → error
    Stat(ctx, key) → (ObjectInfo, error)
    List(ctx, prefix, marker, limit) → (ListResult, error)
    Backend() string
}

// 可选扩展
type Presigner interface { PresignGet/PresignPut }
type MultipartUploader interface { InitMultipart/UploadPart/CompleteMultipart/AbortMultipart }
type Copier interface { Copy(ctx, src, dst, opts) → (ObjectInfo, error) }
type Archiver interface { RestoreFromArchive(ctx, key, days) → error }
```

这使得后端可以逐步实现功能，而 `FileService` 可以使用类型断言来检查能力。但这会增加复杂性——当前统一接口方法的模式对 7 个后端（local/S3/OSS/COS）来说是可管理的，应该保持。

### 3.2 需要引入的新抽象层

**抽象 1：`Authorizer`。** 如上所述。应在 `FileService` 级别引入，作为所有 CRUD 操作的安全门。

**抽象 2：`EventDispatcher`。** 消费 Bus 事件并根据规则分发。应位于 `internal/events/dispatcher.go`，与现有的 `webhook.go` 并行。

**抽象 3：`LifecycleManager`。** 取代当前的 `LifecycleJob`（它只处理过期）。应管理转换和过期，并可利用 `TransitionRule` 列表。

### 3.3 向后兼容

- **Storage 接口扩展：** 通过为 `Copy`/`Move`/`RestoreFromArchive` 提供默认实现（记录 "not implemented" 并返回 `nil`）来保持兼容性
- **Authorizer 接口：** 默认实现（`AllowAllAuthorizer`）确保现有流程无变化
- **BucketConfig 模式扩展：** `Transitions` 字段应序列化为 JSON 存储，默认值为空切片——读取旧行的 SQL 兼容性
- **法律保留作为列：** 新增 `legal_hold` 列，默认为 `false`，迁移填充现有 `_aero_legal_hold` 元数据值

---

## 4. 技术选型

### 4.1 需要评估的外部依赖

| 领域 | 候选方案 | 评估标准 |
|------|---------|---------|
| SQS/SNS SDK | `aws-sdk-go-v2`（已有依赖？） | 是否需要 SQS/SNS 通知目标 |
| PDF 提取 | `unidoc`（商业）/ `pdfcpu`（开源） | 许可证、DPI 保持、Unicode 支持 |
| 图像嵌入 | `clip-go` 绑定 / 远程服务 | ONNX 运行时兼容性、模型大小 |
| 策略引擎 | 自建（当前） / `casbin` | 规则复杂性、规则数量 |

**建议：** Stay stdlib-first（I6）。对于 SQS，复用项目中已有的 `aws-sdk-go-v2`（如果 S3 后端已使用）。对于 PDF 和图像嵌入，优先使用远程提取器端点（`AI_EXTRACTOR_ENDPOINT`）而不是本地库。

### 4.2 自建 vs 集成

**策略引擎（自建 ✅）：** 当前策略语法是基本的 JSON 规则，含 `Principal`/`Effect`/`Action`/`Resource`/`Condition`。对于 S3 兼容性来说已经足够——AWS IAM 策略语法是一个合理的超集，casbin 的模型过于通用。

**事件分发（自建 ✅）：** 对于 HTTP 端点，Webhook 的递送逻辑可以直接复用。SQS/SNS 适配器可以使用标准 AWS SDK。不需要事件流平台（Kafka/RabbitMQ）——现有的 `Event` 表和后端 `LISTEN/NOTIFY` 就足够了。

**PDF 提取（远程优先 ✅）：** 通过 `RemoteExtractor` 端点。保留在 `go.mod` 之外添加 `pdfcpu` 作为备选方案。

---

## 5. 实施路线图

### 5.1 优先级排序与阶段划分

```
P0（当前 Sprint / 下个 Sprint）    P1（2-3 个 Sprint）         P2（待定）
───────────────────────────────    ────────────────────       ────────────
③a: REST 策略执行                   ①: S3 事件分发             ②: 存储类分层
③b: 法律保留列 + API                ④a: 多模态嵌入             ④b: Agent 工具扩展
③c: 对象锁定模式                      ⑤: Storage Copy 基础设施
```

### 5.2 详细分阶段里程碑

**Phase 1 — Security Hardening（~1 周）**
- ✅ 方向③a：将 `checkBucketPolicy` 策略检查添加到 REST `Get`/`Put`/`Delete`/`List` handler 中
- 🎯 成果：REST API 中的策略执行（与 S3 一致）
- 🎯 成果：已验证用户的 ACL 检查（目前缺失）
- **风险：** 可能的回归——现有客户端依赖无策略的访问。缓解措施：配置 flag `ENFORCE_BUCKET_POLICY` 默认关闭（opt-in）

**Phase 2 — Legal Hold & Object Lock（1-2 周）**
- ✅ 方向③b：Schema 迁移——在 objects 表新增 `legal_hold BOOLEAN`
- ✅ 新增 REST 端点 `PUT/DELETE /v1/files/{key}/legal-hold`
- ✅ 方向③c：在 `BucketConfig` 新增 `ObjectLockMode`（`governance`/`compliance`）
- ✅ 更新 S3 handler 以解析 `x-amz-object-lock-mode` 和 `x-amz-bypass-governance-retention`
- 🎯 成果：S3 对象锁定功能正确
- **风险：** governance bypass 头解析可能被滥用。在 S3 中需要 `s3:BypassGovernanceRetention` 策略权限——我们当前的条件引擎需要通过 `Condition` 处理器来支持。

**Phase 3 — Event Notification Dispatcher（~1 周）**
- 🎯 新文件：`internal/events/dispatcher.go`
- 🎯 `Dispatcher.Run(ctx, sub)`：消费 Bus 事件，加载 bucket 通知规则，过滤
- 🎯 HTTP 通知目标（阶段 1）：复用 `Webhook.deliver` 逻辑
- 🎯 集成：`main.go` + 测试
- **风险：** webhook URL 目标与通知目标之间的配置冲突。缓解措施：通知规则在逻辑上优先，但将事件路由到两者

**Phase 4 — Multi-Modal AI & Agent（1-2 周）**
- 🎯 方向④a：扩展 `Extractor` 接口为 `SupportsMIME(mime string) bool`
- 🎯 图像提取器：使用远程端点或 CLIP
- 🎯 向量索引兼容性：支持可变维度（目前固定为 256）
- 🎯 方向④b：Agent 新增 `search_images`、`extract_text` 工具
- **风险：** 维度不匹配导致索引损坏。缓解措施：嵌入时存储 `EmbedModel` + `Dim`，检索时跳过不匹配的 chunk（已有模式）

**Phase 5 — Storage Class Tiering（~2 周）**
- ✅ 扩展 `Storage` 接口：`Copy`、`RestoreFromArchive`
- ✅ Schema 迁移：`BucketConfig` 新增 `Transitions []TransitionRule`
- ✅ 扩展 `LifecycleJob` 为 `LifecycleManager`：处理转换和过期
- ✅ 新增 `POST /v1/files/{key}/restore?days=N` Glacier 恢复端点
- @影响：所有 Storage 后端需要 `Copy` 实现。Local backend：重命名/拷贝文件。S3 backend：`CopyObject` SDK 调用。OSS/COS backend：各自的拷贝 API。

### 5.3 风险矩阵

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| REST 策略执行导致现有客户端中断 | 中 | 高 | Opt-in flag；Beta 期详细日志 |
| Storage `Copy` 接口断裂变更 | 低 | 高 | 为所有 4 个后端提供默认回退实现 |
| PDF/图像嵌入增加索引延迟 | 中 | 中 | 异步队列 + 可配置超时 |
| SQS 通知目标需要 IAM 凭证 | 中 | 低 | 与现有的 S3 后端 AWS 配置复用 |
| 多模态 embedding 维度与现有索引不兼容 | 中 | 高 | 存储 `Dim` 和 `EmbedModel` 在每个 chunk 中；索引时过滤 |
| go/ast 代码分块在大型代码库中性能差 | 低 | 中 | 设 max-size 限制，对大文件回退到滑动窗口 |

### 5.4 跨方向同步点

方向①（通知）和方向②（分层）在生命周期事件上有重叠。通知可以触发分层工作流（例如，Glacier 恢复完成 → 通知应用程序）。建议的实现顺序考虑到了这一点——方向①（通知）应该先实现，这样方向②（分层）就可以使用它。

方向③（访问控制）为方向④（AI Agent）提供了安全保障。如果 Agent 绕过 bucket 策略执行，多租户环境中的 `list_files`/`read_file` 工具就会带来安全风险。方向③实现的优先级反映了这一点。

方向⑤（Storage Copy 基础设施）是方向②（分层）的先决条件。应在方向②之前或与其并行实施。两种策略：

1. **先行扩展**：先实现 Storage Copy 基础设施，在此基础上构建分层
2. **并行推进**：方向②的范围限制在 S3 后端内（使用原生 `CopyObject`），方向⑤单独推进

对于首次迭代建议使用策略 2——跨后端分层仍然是罕见的配置。策略 2 将方向②的范围限制为同一后端内的转换，这使用后端本地的 `Copy` 机制即可实现，无需扩展 Storage 接口。

---

## 总结

当前架构具有坚实的分层基础和良好的接口模式。关键架构债务围绕**安全不一致**（跨协议的策略/ACL 检查）、**功能差距**（通知分发器、存储类转换、对象锁定模式）和**接口约束**（缺少 Storage Copy/Restore）。五个高价值扩展方向已识别并按风险和影响排序。建议立即修复安全债务（方向③a）——这是最高 ROI 且最低风险的工作。
