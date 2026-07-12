现在我有了一个非常扎实的基础。以下是我的架构分析。

---

# 架构分析：存储对象生命周期转换、按桶事件通知和大文件流式加密

## 1. 架构评估

### 当前架构的优势

当前的代码库展示了一个设计良好的分层架构，具备以下优点：

1. **清晰的关注点分离**：`Protocol Adapters → FileService → Storage + Repository` 的分层是简洁且完备的。`FileService` 作为单一的、协议无关的入口点，确保了业务规则不会分散到各个处理程序中。

2. **良好的接口抽象**：
   - `Storage` 接口（6 个核心方法 + presign + multipart）是对任何后端都有明确定义的契约。
   - `Repository` 接口已包含 `GetBucketNotifications`、`SetBucketNotifications` 以及 `BucketConfig.NotificationRules`。
   - `EventSink` 接口足够小（只有一个 `Publish`），允许 `FileService` 保持与底层事件分发机制的解耦。

3. **明智的“选择加入”默认值**：代码库严格遵守“默认安全”原则——AI、pgvector、通知等特性都需要显式配置才能激活。这确保了基线 CI 路径始终保持简洁。

4. **现有的 Webhook 基础设施**是健壮的：它有一个持久化的失败表、指数退避和重试循环死信处理。这是将来更多 sink 的良好基础。

### 当前架构的局限性

1. **装饰型操作没有 Storage 接口方法**：`Storage` 接口缺少 `ChangeStorageClass`、`CopyObject`、`RestoreObject`（用于冰川恢复）等方法。当前，生命周期流水线只能删除（到期/软删除/硬删除），但无法在存储层级之间转换对象。

2. **通知管道中缺失路由层**：`Webhook` 是所有全局配置 URL 的单一接收器。没有“按桶”的路由：`NotificationRules` 被持久化到了数据库中，并且 API 端点是完整的——但实际分发层缺失了。所有事件都被推送到每个配置的 URL，无论桶的事件规则是什么。

3. **流式加密不流式传输**：当前的 SSE 实现使用 `io.ReadAll` 将整个文件读入内存。对于大文件来说这并不可扩展——正如 `encrypt.go` 中注释所承认的那样。GCM 需要完整的密文来验证标签，这使得它天然不适合流式处理。对于当前对象大小来说 ~hundreds MB 还行，但用户不能逐步处理流。

4. **在 FileService 层耦合了存储类意识**：`StorageClass` 保存在 `Object` 结构体和元数据中，但 `FileService` 在对象创建后并不协调跨存储类的移动。生命周期流水线仅应用于到期操作，没有转换操作。

5. **事件类型集有限**：只有 `created`、`deleted` 和 `accessed`。没有 `transitioned`、`restored` 或 `replicated` 类型——而转换工作流将会需要这些类型。

### 架构债务

| 债务 | 位置 | 影响 |
|---------|---------|--------|
| `encryptReader`/`decryptReader` 中的全缓冲 I/O | `storage/encrypt.go` | 大文件的 OOM 风险；无法支持流式解密 |
| Webhook 是单一全局接收器 | `events/webhook.go` 订阅共享的 Bus | 无法进行按桶事件过滤 |
| 缺少 `Storage.ChangeStorageClass()` | `storage/storage.go` 接口 | 阻止生命周期转换 |
| 通知规则存储为 JSON blob | `buckets.notification_rules` JSON 列 | 没有可靠的约束；迁移时可能发生反序列化错误 |
| `Object.StorageClass` 在写入后不被使用 | `repository.Object` 结构体 | 当前是空字段——读取后端时被忽略 |

---

## 2. 扩展方向

基于分析，这些是最高价值的架构扩展，按照收益/成本排序：

### 方向 1：存储类生命周期转换（高价值，架构阻塞）

**为什么需要它**：
存储即服务的核心价值在于自动的成本优化。用户上传数据到 STANDARD，但可以自动转换到 STANDARD_IA（低频访问）或 GLACIER（归档）。这是每个对象存储平台的关键差异化因素。当前，`BucketConfig.ExpireAfterDays` 只能删除对象，无法转换它们——这是用户可配置策略中的一个显著缺口。

**核心挑战**：

1. **Storage 接口扩展**：`Storage` 接口目前没有转换方法。有几种设计选择：

   | 选项 | 方式 | 优点 | 缺点 |
   |--------|--------|----------|-----------|
   | **A) 新增 `ChangeStorageClass` 方法** | 在 `Storage` 上添加 `ChangeStorageClass(ctx, key, newClass)` | 显式意图；清晰的契约 | 每个后端必须实现；对于像 S3 Glacier 这样的冷层，可能需要额外的恢复轮次 |
   | **B) 扩展 `CopyObject` 语义** | 添加 `CopyObject(ctx, srcKey, dstKey, opts)`，其中 opts 包含 `StorageClass` | 更通用；也与 S3 API 对齐 | 对于仅元数据的转换（local storage）来说过于宽泛 |
   | **C) 在 Storage 之外添加一个“转换协调器”** | 创建一个 `TransitionRunner`，它使用 Storage 的原生原语（如 S3 CopyObject） | 保持 Storage 接口干净 | 在服务层泄漏了后端特定的转换逻辑 |

   **推荐**：**选项 A**——在 Storage 上新增一个专用的 `ChangeStorageClass(ctx, key, newClass) (ObjectInfo, error)` 方法。对于 local storage，这只是一次元数据更新。对于 S3/OSS/COS，这调用了底层 SDK 的副本（可能指定存储类）。这使概念保持整洁，与 `Put`/`Get`/`Delete` 处于同一抽象级别。

2. **生命周期规则 DSL**：当前的存储桶配置只有 `expire_after_days` + `expire_action`。生命周期转换需要一个更丰富的规则结构体（类似于 S3 的 `LifecycleRule`）：

   ```go
   type LifecycleRule struct {
       ID        string   // unique rule id
       Status    string   // "Enabled" | "Disabled"
       Filter    *LifecycleFilter // prefix, tags, or object size
       Transitions []LifecycleTransition // e.g. [{Days: 30, StorageClass: "STANDARD_IA"}, {Days: 90, StorageClass: "GLACIER"}]
       Expiration  *LifecycleExpiration  // e.g. {Days: 365}
       NoncurrentVersionTransition *NoncurrentVersionTransition
       NoncurrentVersionExpiration  *NoncurrentVersionExpiration
   }
   ```

   需要一次数据库迁移来扩展 `buckets` 表，使其支持多规则生命周期配置（可能是 JSON 列，或是一个独立的 `lifecycle_rules` 表）。

3. **后台 Job 流水线**：与现有的 `LifecycleJob`（仅处理到期）不同，转换需要一个遍历桶的 Job，评估规则并将匹配的对象排队进行转换。这些 Job 需要可重试、幂等，并且可以并行运行（经过适当的分片）。

4. **恢复（从 GLACIER 恢复）**：当用户尝试读取已归档的对象时，系统必须支持发起恢复操作（解冻需要几小时），并为正在恢复中的对象返回特殊的元数据。`Storage` 接口需要一个 `RestoreObject(ctx, key, days) error` 方法。

**对现有系统的影响**：
- 新增 `storage.ChangeStorageClass` + `storage.RestoreObject` 方法 → 所有后端都需要更新契约测试
- 扩展 `BucketConfig` 使其支持丰富的生命周期规则
- 新增 `internal/reconcile/transition.go`（类似于 `lifecycle.go`）
- 在 `reconcile` 层扩展事件类型（`object.transitioned`、`object.restored`）
- S3 兼容性的 XML 序列化/反序列化

---

### 方向 2：按桶事件通知路由（低增量成本，高影响）

**为什么需要它**：
API 已经存在（`GetBucketNotifications`、`SetBucketNotifications`），数据模型已经定义，事件总线已经在发送持久化事件。缺失的只是路由层——而这个缺口意味着配置了通知规则的用户可能认为它们在工作，但实际上它们什么也没做。正如分析文档所指出的，这种“配置了但没有效果”的问题比功能缺失更快地侵蚀信任。

**核心挑战**：

挑战不在于路由本身——它本质上是一个订阅者模式，入站事件匹配规则，然后分发。真正的困难在于多个 sink 类型：

| Sink 类型 | 当前状态 | 复杂度 |
|-----------|-------------|----------|
| 多 Webhook（同一桶的多个 URL） | 可以复用 `webhook` 包的 HTTP POST + 重试机制 | 低 |
| SNS/SQS | 需要新的 SDK 依赖包（AWS SDK v2） | 中 |
| SMTP | 需要邮件库 + 模板化 | 中 |
| Lambda 调用 | 已经通过 AWS SDK 支持 | 中 |

**推荐路径**：
分两阶段实现：

**阶段 1（立即）**：实现多 Webhook 路由层。用一个新的 `NotificationDispatcher` 来替换当前的全局 `Webhook`，该分发器订阅事件总线，查询桶的通知规则，将它们与事件属性（类型、前缀）进行匹配，然后通过现有的 `webhook.Worker` 分发给匹配的 URL。这个阶段的增量成本确实很低（~300 行核心路由代码），因为：

- `Bus.Publish` 已经包含了存储桶和键
- 存储桶的 `NotificationRules` 已经可查询
- `webhook.Worker` 已经提供了 HTTP + 重试 + HMAC

**阶段 2（后续）**：为 SNS/SQS/SMTP 添加适配器。每个适配器都实现一个通用的 `Notifier` 接口：

```go
type Notifier interface {
    Notify(ctx context.Context, rule repository.NotificationRule, event repository.Event) error
}
```

每个新 sink 都会获得自己的适配器实现、自己的配置和单独的健康指标。

**对现有系统的影响**：
- 在 `events` 包中（或在新的 `events/dispatcher.go` 中）新增 `NotificationDispatcher`
- 从 `events/webhook.go` 中精简全局 Webhook 订阅者（或将其设为可选的兜底策略）
- 新增指标：`notification_dispatched_total{type, bucket}`、`notification_dispatch_error{type}`
- **无 API 变更**——OpenAPI 路由已经就绪
- **无模式变更**——`notification_rules` JSON 列已经就绪

---

### 方向 3：大文件流式加密（AES-256-CTR + 逐块 HMAC）

**为什么需要它**：
当前的 GCM 实现将所有内容缓冲到内存中。对于 512 MiB 以上的对象，这会带来 OOM 风险。对于多 TB 级的对象（这是 S3 兼容系统的主要场景），全缓冲根本不可行。此外，用户可能希望在不将整个文件加载到内存中的情况下进行流式上传和流式下载。

**核心挑战与设计决策**：

1. **每个块包含自己的 HMAC**：而不是在文件末尾使用一个全局的 HMAC 列表（这要求读取者在开始验证之前先读文件末尾），更可取的方案是每个块 `<block_index | iv | ciphertext | hmac>` 都包含元数据。这允许以任意偏移量进行独立的流式读取和验证。

2. **Per-file 随机数**：CTR 模式需要每个流的随机数值以防止密钥流复用。正确的设计是 `per_file_nonce = HKDF(master_key, file_id, chunk_size)`，然后 `per_block_counter = file_nonce || block_index`。这样，即使两个文件使用了同一个信封密钥，它们的随机数也是完全不同的。

3. **算法协商**：需要一个新的 `alg` 字段值（例如 `"AES-256-CTR-HMAC-SHA256"`），与现有的 `"AES-256-GCM"` 并存。读取端根据 `alg` 进行分流。

4. **分块大小**：建议使用 64 KiB 的块（平衡内存使用与 HMAC 计算开销）。较大的块（如 1 MiB）可以降低 HMAC 计算量与存储体量的比率，但会延迟验证。

**信封格式**：

```json
{
  "alg": "AES-256-CTR-HMAC-SHA256",
  "kid": "2026-06",
  "kek": "base64: wrapped data key",
  "iv": "base64: 64-bit per-file salt",
  "chunk_size": 65536
}
```

每个块的存储格式在流中是：
```
[ block_index:4 ][ iv/ctr ][ ciphertext:chunk_size ][ hmac:32 ]
```

**当「alg」不同时的性能影响**：

| 算法 | 规格 | 每块内存 | 流式传输 | 随机访问 |
|---------|--------|------------|-------------|----------------|
| AES-256-GCM（现有） | 完整缓冲 | O(N) | ❌ | ❌ |
| AES-256-CTR + HMAC/SHA256 | 64 KiB 块 | O(64 KiB) | ✅ | ✅ |
| AES-256-GCM（逐块） | 每个块一个 GCM 标签 | O(块大小) | ✅ | ✅ |

> **注意**：GCM 与 CTR+HMAC：AES-GCM 每块使用大约 28 字节的额外空间（随机数 + 标签），而 HMAC-SHA256 需要 32 字节。其主要区别在于 HMAC 提供更强的认证，并且在硬件加速（如 Intel SHA-NI）缺失时在软件中的性能更好。对于大多数情况，**逐块 GCM** 是一个合理的折中方案——它使用现有的 AES-NI 硬件加速，同时根据需要支持流式传输。HMAC-SHA256 变体对于要求高吞吐量的归档工作负载可能更可取。

**对现有系统的影响**：
- 在 `storage/encrypt.go` 中新增 `ChunkedEncrypter` / `ChunkedDecrypter`
- 新增 `alg` 分派：`"AES-256-GCM"` → 现有逻辑；`"AES-256-CTR-HMAC-SHA256"` → 新的块式逻辑
- `Storage.Put` 和 `Storage.Get` 需要包装读取器/写入器，以便在写到块存储后端时进行流式加密
- 在 `Storage.PutOptions` 中新增配置（可能通过环境变量，如 `SSE_CHUNK_SIZE`）
- 自动回退：对于小于 2× chunk_size 的对象，使用现有的 GCM 路径以保持向后兼容

---

### 方向 4：存储类——感知型读取路径

**为什么需要它**：
一旦对象从 STANDARD 转换为 GLACIER，读取路径必须：
1. 用 `x-amz-storage-class: GLACIER` 或 `x-amz-restore` 标头来响应 HEAD/GET 请求
2. 如果对象已熔断，则拒绝完整的 GET 请求（`InvalidObjectState`）
3. 支持发起恢复操作（`RestoreObject`），该操作会触发到 STANDARD（或频繁访问层）的后台拷贝
4. 在数据恢复时用 `ongoing-request="true"` 或 `"true"` 来响应 HEAD 请求

**核心挑战**：
- `FileService.Get` 需要检查存储类，并在对象在 GLACIER 中时阻止读取（除非已恢复）
- HEAD 响应需要包含恢复状态标头
- GetRange 需要类似的处理

**对现有系统的影响**：
- 在 `FileService.Get` 和 `FileService.Head` 中新增存储类检查
- 在 `FileService` 中新增 `RestoreObject` 方法
- 新增一个后台 Job（`internal/reconcile/restoration.go`）用于监测恢复完成情况并更新对象状态
- 用于 GET 响应上 `x-amz-restore` 标头的 S3 兼容性 XML/JSON

---

### 方向 5：通知分发器的可观测性

**为什么需要它**：
通知路由引入了一个新的异步链路。如果没有适当的可观测性，操作员就不知道：
- 哪些事件被路由到了哪里
- 哪些事件被丢弃了（由于速率限制、重试次数过多或退避失败）
- 通知延迟是多少（从事件创建到成功发布的时间）

**核心挑战**：
- 将指标添加到异步流水线中，而不引入对总线下发路径本身的可观测性级联依赖
- 普罗米修斯指标应该与通知规则一一对应（`notification_rule_info{tenant,bucket,rule_id}`）

**推荐度量指标**：

| 指标名称 | 类型 | 标签 |
|-------------|--------|--------|
| `notification_events_total` | Counter | tenant, bucket, rule_id, event_type |
| `notification_dispatch_latency_seconds` | Histogram | tenant, sink_type(webhook/sns/sqs) |
| `notification_dispatch_errors_total` | Counter | tenant, sink_type, error_code |
| `notification_queue_depth` | Gauge | tenant, sink_type |
| `notification_rule_info` | Info | tenant, bucket, rule_id, sink_type |

---

## 3. 接口设计建议

### 3.1 Storage 接口更改

当前接口：

```go
type Storage interface {
    Put(ctx, key, reader, size, opts) (ObjectInfo, error)
    Get(ctx, key) (reader, ObjectInfo, error)
    Stat(ctx, key) (ObjectInfo, error)
    Delete(ctx, key) error
    List(ctx, prefix, marker, limit) (ListResult, error)
    PresignGet(ctx, key, expiry) (string, error)
    PresignPut(ctx, key, expiry) (string, error)
    InitMultipart(ctx, key, opts) (MultipartInit, error)
    UploadPart(ctx, key, uploadID, partNum, reader, size) (MultipartPart, error)
    CompleteMultipart(ctx, key, uploadID, parts) (ObjectInfo, error)
    AbortMultipart(ctx, key, uploadID) error
    Backend() string
}
```

建议扩展：

```go
type Storage interface {
    // ... 所有现有方法 ...

    // ChangeStorageClass transitions an object to a new storage class. For
    // local storage this is a metadata-only operation; for cloud backends it
    // copies the object to the new tier. When the source is GLACIER, a
    // restoration must be initiated first via RestoreObject.
    ChangeStorageClass(ctx, key, newClass string) (ObjectInfo, error)

    // RestoreObject initiates a restoration from GLACIER/deep-archive tiers.
    // Returns the number of days the restored copy will be available.
    RestoreObject(ctx, key string, days int) (ObjectInfo, error)

    // GetRestoreStatus returns the restoration status for a GLACIER object.
    GetRestoreStatus(ctx, key string) (RestoreStatus, error)
}

type RestoreStatus struct {
    InProgress bool      // true while restoration is ongoing
    Expiry     *time.Time // nil while in-progress; set when restoration is complete
}
```

**可选 vs 强制**：这些是可选方法。Local storage 的默认实现可以返回 `ErrNotImplemented`。后台 Job 的路由逻辑应该在调用之前检查类型断言或接口检测。

更好的方法是使用一个接口检查模式：

```go
type StorageClassChanger interface {
    ChangeStorageClass(ctx context.Context, key string, newClass string) (ObjectInfo, error)
}

// In transition.go:
if changer, ok := store.(StorageClassChanger); ok {
    info, err := changer.ChangeStorageClass(ctx, key, newClass)
    // ...
} else {
    // For local storage, this is a metadata-only update.
    // Just update the repository row, no storage operation needed.
}
```

这避免了用所有后端必须存根的方法去污染主要的 `Storage` 接口，同时允许基于具体类型的转换行为。

### 3.2 通知分发器接口

```go
// events/dispatcher.go

// Notifier delivers a single event to an external sink.
type Notifier interface {
    // Type returns the sink type identifier, e.g. "webhook", "sns", "sqs".
    Type() string
    // Notify sends the event. Errors trigger the retry machinery.
    Notify(ctx context.Context, rule repository.NotificationRule, event repository.Event) error
}

// Dispatcher subscribes to the event bus, evaluates bucket notification rules,
// and fans out matched events to registered notifiers.
type Dispatcher struct {
    bus       *Bus
    repo      repository.Repository
    notifiers map[string]Notifier // keyed by sink type
    logger    *slog.Logger
}

func (d *Dispatcher) RegisterNotifier(n Notifier) {
    d.notifiers[n.Type()] = n
}

func (d *Dispatcher) Run(ctx context.Context, sub <-chan repository.Event) {
    for {
        select {
        case <-ctx.Done():
            return
        case e, ok := <-sub:
            if !ok { return }
            d.dispatch(ctx, e)
        }
    }
}

func (d *Dispatcher) dispatch(ctx context.Context, e repository.Event) {
    rules, err := d.repo.GetBucketNotifications(ctx, e.TenantID, e.Bucket)
    if err != nil || len(rules) == 0 {
        return
    }
    for _, rule := range rules {
        if !rule.Matches(e) { continue }
        // Determine sink type from rule: QueueARN → sns, TopicARN → sns, etc.
        n, ok := d.notifiers[rule.SinkType()]
        if !ok { continue }
        go func() { _ = n.Notify(ctx, rule, e) }()
    }
}
```

这使 `Webhook` 重写为适配器模式，而不是作为所有事件的直接订阅者。现有的 `Webhook` 结构体变成一个实现 `Notifier` 接口的 `WebhookNotifier`。

### 3.3 生命周期规则 DSL

```go
// repository/lifecycle.go

type LifecycleRule struct {
    ID        string            `json:"id"`
    Status    string            `json:"status"` // "Enabled" | "Disabled"
    Filter    *LifecycleFilter  `json:"filter,omitempty"`
    
    Transitions []TransitionRule `json:"transitions,omitempty"`
    Expiration  *ExpirationRule  `json:"expiration,omitempty"`
    
    NoncurrentVersionTransitions []NoncurrentVersionTransition `json:"noncurrent_version_transitions,omitempty"`
    NoncurrentVersionExpiration  *NoncurrentVersionExpiration   `json:"noncurrent_version_expiration,omitempty"`
}

type LifecycleFilter struct {
    Prefix string            `json:"prefix,omitempty"`
    Tags   map[string]string `json:"tags,omitempty"`
    MinSize int64            `json:"min_size,omitempty"`
    MaxSize int64            `json:"max_size,omitempty"`
}

type TransitionRule struct {
    Days         int    `json:"days"`
    StorageClass string `json:"storage_class"` // e.g. "STANDARD_IA", "GLACIER"
}

type ExpirationRule struct {
    Days int  `json:"days"`
    DeleteMarker bool `json:"delete_marker,omitempty"` // for versioned buckets
}

type NoncurrentVersionTransition struct {
    NoncurrentDays int    `json:"noncurrent_days"`
    StorageClass   string `json:"storage_class"`
}

type NoncurrentVersionExpiration struct {
    NoncurrentDays int `json:"noncurrent_days"`
}
```

### 3.4 向后兼容性

| 变化 | 兼容性策略 |
|---------|----------------|
| 在 Storage 上新增 `ChangeStorageClass` | 可选接口；回退到仅元数据更新 + 日志 |
| 在 Storage 上新增 `RestoreObject` | 可选；local storage 返回 `ErrNotImplemented` |
| Bucket.NotificationRules | JSON 列格式未变；新规则仍然使用相同的序列化 |
| 生命周期规则 | `expire_after_days` + `expire_action` 成为 `LifecycleRule.Status="Enabled"` 且只有一个 `Expiration.Days` 规则的简写，向下兼容 |
| 加密信封 `alg` | 新增 `"AES-256-CTR-HMAC-SHA256"`；读取路径通过 `alg` 字段识别，处理现有信封不变 |
| 全局 Webhook → Dispatcher | `EVENTS_WEBHOOK_URL` 行为保留为不使用桶规则的全局兜底 |

---

## 4. 技术选型

### 4.1 新增依赖

| 特性 | 必需的依赖 | 理由 |
|---------|----------------|---------|
| 流式加密 | 无（Go 标准库 `crypto/aes`、`crypto/cipher`、`crypto/hmac`、`crypto/sha256`） | 所有必需的加密原语都已经在标准库中可用 |
| SNS/SQS 通知 | `github.com/aws/aws-sdk-go-v2/service/sns` 和 `service/sqs`（与目前的 aero-vault 没有 S3 依赖一致） | 可选（采用时引入） |
| SMTP 通知 | `net/smtp`（标准库） | 无外部依赖 |
| 生命周期规则条件 | 无（`time.Time`、`path.Match`） | 都已经内置 |

**指导原则**：对于现有的 Go 标准库支持的加密原语，不应引入新的加密依赖。SNS/SQS 适配器应该是编译时可选的（build tag）或由配置控制的可选模块。

### 4.2 自定义构建对比采购

| 特性 | 自定义构建 | 采购 | 决策 |
|---------|-------------|--------|--------|
| 通知路由层 | ~300 行核心逻辑 + 每个适配器 ~100 行 | AWS EventBridge（额外基础设施） | **自定义构建**——路由逻辑很简单；采购会增加操作复杂度 |
| 存储类转换 | ~500 行在每个后端 + 协调器 | AWS S3 Lifecycle（平台锁定） | **自定义构建**——每个云后端在 SDK 层已经支持 CopyObject |
| 流式加密 | ~400 行逐块读取器 | 外部加密 SDK（与信封格式接口不兼容） | **自定义构建**——标准库原生支持；自定义有助于与现有信封格式保持兼容 |

### 4.3 自建 vs 采购的决策矩阵

| 因素 | 通知路由 | 存储类转换 | 流式加密 |
|--------|--------------|----------------|----------------|
| 核心复杂度 | 低（有状态路由） | 中（协调器 + 接口） | 中（加密 + 分块 I/O） |
| 与现有代码耦合 | 高（复用 EventBus、webhook、Repository） | 高（扩展 Storage + Repository） | 中（信封格式 + 读取器包装器） |
| 外部替代方案 | AWS EventBridge | AWS S3 Lifecycle | 云 KMS + 服务端加密 |
| 供应商锁定风险 | 低 | 中（迁移到新后端需要重写） | 低（格式标准） |
| **决策** | ✅ **自定义构建** | ✅ **自定义构建** | ✅ **自定义构建** |

---

## 5. 实施路线图

### 5.1 优先级排序

| 优先级 | 特性 | 努力程度 | 影响 | 依赖关系 |
|----------|---------|---------|---------|--------------|
| **P0** | 按桶通知路由（阶段 1：多 Webhook） | ~1-2 天 | 高 | 无（API 已就绪） |
| **P0** | 存储类转换 — Storage 接口扩展 | ~3-5 天 | 高 | Storage 变更、数据库模式变更 |
| **P1** | 大文件流式加密（AES-256-CTR + 逐块 HMAC） | ~5-7 天 | 高 | Storage.Put/Get 包装器 |
| **P1** | 存储类 — 感知型读取路径 | ~2-3 天 | 高 | 转换已实现 |
| **P2** | 通知 SNS/SQS 适配器 | ~3-4 天 | 中 | 通知路由已就绪 |
| **P2** | 丰富的生命周期 DSL（多规则、筛选条件） | ~4-5 天 | 中 | Storage 接口已就绪 |

### 5.2 分阶段里程碑

#### 阶段 1（第 1-2 周）：通知路由 + Storage 接口扩展

**第 1-3 天：通知分发器**
```
events/dispatcher.go          # 核心路由逻辑
events/webhook_notifier.go    # 将现有 Webhook 重写为 Notifier
events/dispatcher_test.go     # 基于规则的匹配测试
internal/reconcile/router.go  # 启动时装配
```

检查点：
- ✅ 多 Webhook URL 用于单个桶（当前改不了）
- ✅ 支持事件类型过滤器（`Events: ["s3:ObjectCreated:*"]`）
- ✅ 支持前缀过滤器（`FilterKey: "logs/"`）
- ✅ 与全局兜底 `EVENTS_WEBHOOK_URL` 的向后兼容性

**第 4-7 天：Storage 接口 + 本地转换**
```
storage/storage.go               # 新增 ChangeStorageClass 接口
storage/local.go                 # 本地实现（仅元数据）
storage/contract_test.go         # 转换契约测试
internal/service/transition.go   # 服务层编排
internal/reconcile/transition.go # 后台 Job 用于批量转换
```

检查点：
- ✅ 本地支持 `ChangeStorageClass`
- ✅ 转换 Job 对匹配规则的对象进行排队
- ✅ 对象行中的 `StorageClass` 已更新
- ✅ 发布的 `object.transitioned` 事件

#### 阶段 2（第 3-4 周）：流式加密 + 云转换

**第 8-12 天：流式加密**
```
storage/chunked_encrypt.go    # CHunkedEncrypter / ChunkedDecrypter
storage/encrypt.go            # alg 分派 + 向后兼容 GCM
storage/chunked_encrypt_test.go
```

检查点：
- ✅ 流式 PUT（加密 + HMAC 每个块）
- ✅ 流式 GET（验证 + 解密每个块）
- ✅ 向后兼容现有的 GCM 信封
- ✅ 对于小于 2× 块大小的对象，自动使用 GCM

**第 13-16 天：云转换 + S3 兼容性**
```
storage/s3.go          # 通过 CopyObject 实现 ChangeStorageClass
storage/oss.go         # 通过 CopyObject 实现
storage/cos.go         # 通过 CopyObject 实现
internal/api/s3compat/lifecycle.go # 丰富的生命周期 XML
internal/api/rest/lifecycle.go     # REST 生命周期端点
```

检查点：
- ✅ S3、OSS 和 COS 的 `ChangeStorageClass`
- ✅ GLACIER ➝ STANDARD 恢复流程
- ✅ S3 GET/PUT `?lifecycle` 带有丰富的规则
- ✅ REST 生命周期 API

#### 阶段 3（第 5-6 周）：丰富的生命周期 + 可观测性

**第 17-19 天：丰富的生命周期规则**
```
repository/lifecycle_rules.go # 新的 lifecycle_rules 表（或扩展的 JSON）
internal/reconcile/lifecycle.go   # 重写以支持多规则 + 转换
repository/migrations/0025_lifecycle_rules.up.sql
```

**第 20-22 天：可观测性 + SNS/SQS 适配器**
```
events/sns_notifier.go      # SNS 适配器（条件编译）
events/sqs_notifier.go      # SQS 适配器（条件编译）
telemetry/notification.go   # 通知指标
```

### 5.3 风险与缓解策略

| 风险 | 可能性 | 影响 | 缓解策略 |
|------|----------|--------|----------------|
| Storage 接口扩展破坏现有后端 | 低 | 高 | 使用可选接口模式（`if changer, ok := store.(StorageClassChanger) { ... }`）|
| 通知规则的数量导致事件总线拥塞 | 中 | 中 | 每个桶增加速率限制；可丢弃事件，但总线上的全局背压可以保护核心 CRUD |
| 流式加密导致 5%+ 的性能开销 | 中 | 中 | 每个块的大小是可配置的；使用 `crypto/aes` 可能受益于硬件 AES-NI；回退到非加密路径 |
| GCM 信封与流式加密之间的密封格式不兼容 | 低 | 高 | 使用 envelope `alg` 字段进行分派——`"AES-256-GCM"` 使用现有逻辑；`"AES-256-CTR-HMAC-SHA256"` 使用新逻辑 |
| 生命周期规则复杂度（多规则优先级） | 中 | 中 | 明确记录规则评估顺序（最早到期日优先）并添加单元测试 |
| 并行转换 Job 导致存储后端限流 | 中 | 低 | 使用 JobPool 进行重试 + 退避；每个 Job 有一个可配置的并发限制 |

---

## 6. 最终建议

**立即开始（第 1-3 天）**：实现按桶通知路由。这是成本最低的增量改进——它在现有 API 和后端之间填补了缺失的环节，而无需更改模式、扩展现有接口或引入新的依赖关系。如分析文档所述，核心路由可以在约 300 行代码内完成，并且“配置了但没有效果”的问题非常紧迫。

**同时启动（第 2-7 天）**：设计并实现 Storage 接口扩展（`ChangeStorageClass`）。这是存储类转换的阻塞性依赖，扩展 Storage 接口会波及所有后端（local、S3、OSS、COS）。尽早设置接口意味着后续实现可以并行进行。

**第二阶段（第 3-4 周）**：流式加密。这是最大的工程投入，技术要求也最高，但它解除了系统中最明确的扩展性限制。`encrypt.go` 中的注释“对于大文件使用 AES-CTR + HMAC 分块”明确承认了这种是需要但尚未实现的东西。
