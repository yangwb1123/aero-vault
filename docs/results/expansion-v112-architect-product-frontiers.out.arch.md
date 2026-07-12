Now I have a thorough understanding of the current architecture. Here is the architectural analysis.

---

# 架构分析报告：AeroVault S3 协议深度、存储分层、合规扩展

## 1. 架构评估

### 1.1 当前架构的优势

**分层抽象清晰。** 当前架构的核心决策——`FileService` 作为唯一 CRUD 入口——是一个非常稳健的设计。所有协议适配器（REST/S3/WebDAV/MCP）共享同一业务逻辑层，这意味着：

- 在每个适配器中一致地执行配额、版本控制、对象锁定；
- 新增协议只需实现一个薄层，无需复制业务规则；
- 集成测试可以绕过所有协议适配器直接针对 `FileService`。

`storage.Storage` 接口的抽象级别恰到好处。它足够小以便轻松实现新后端，又足够丰富以涵盖 S3 生态系统中的所有对象存储模式（multipart、presign、list）。`repository.Repository` 接口同样如此——它通过相同的 `sql.go` 核心为 SQLite 和 Postgres 提供统一接口，迁移层是经过实战检验的设计。

**Opt-in 安全默认。** 所有 AI 功能、事件、复制、集群单例均默认关闭。这意味着基线的 CI 门控是轻量的、确定性的、零网络的。这是一项经过深思熟虑的可操作性决定。

### 1.2 当前架构的局限性

**局限 1：`storage.Storage` 缺少范围级拷贝操作。**
接口包含 `Get`（流式读取）和 `Put`（流式写入），但没有 `CopyRange(srcKey, dstKey, offset, length)` 或类似的低成本后端内拷贝语义。当源和目标位于同一后端时（如 local→local 或 S3→S3），从同一后端读取再重新写入会造成吞吐量和成本的浪费。这一问题在 UploadPartCopy（方向 2）和存储分层迁移（方向 4）中都会出现。

**局限 2：`BucketConfig` 的标量寿命模型。**
`BucketConfig` 使用单个 `ExpireAfterDays` + `ExpireAction` 标量对来建模生命周期。S3 的生命周期是一个有序的转换规则数组，每个规则包含多个 `Transition` + 一个 `Expiration`。当前的标量模型无法在不彻底改变存储结构的情况下表达分层转换。这是方向 4 的核心架构障碍。

**局限 3：对象锁定模型缺少保留模式。**
`repository.Object` 有 `LockedUntil *time.Time` 但没有 `RetentionMode` 字段。当前实现将所有锁定视为 COMPLIANCE（不可绕过），这虽然是安全的默认，但缺少 GOVERNANCE 模式的绕过机制。这违反了 S3 协议语义——真实 S3 中管理员可通过 `s3:BypassGovernanceRetention` 和 `x-amz-bypass-governance-retention: true` 绕过 GOVERNANCE 保留。

**局限 4：S3 兼容层的请求头解析是现用现取的。**
`PutObject` handler 只读取它已知的请求头子集（`Content-Type`、`Metadata`、`Content-MD5`、`StorageClass`、`x-amz-acl`）。所有 SSE 头、`x-amz-object-lock-mode`、`x-amz-object-lock-retain-until-date` 以及 `x-amz-bypass-governance-retention` 均被静默丢弃。这种做法不是架构层面的失误，而是增量开发的积累——随着每个新请求头的添加而没有统一的请求头映射框架。

**局限 5：Legal Hold 被存为元数据。**
`x-amz-object-lock-legal-hold: ON` 被放入元数据的 `_aero_legal_hold` 键中。这是一种 hack——它无法通过 SQL 查询、无法被索引、也不是 S3 协议中的一等公民。缺少独立的 `GET /{key}?legal-hold` 和 `PUT /{key}?legal-hold` 端点。

### 1.3 架构债务

| 债务项 | 严重程度 | 说明 |
|--------|----------|------|
| Legal Hold 元数据 hack | 中等 | `_aero_legal_hold` 元数据键使 Legal Hold 无法被索引，且不属于对象 schema。重构为 `objects` 表中的 `legal_hold` 列是必须的。 |
| `ObjectLockSeconds` 为 `int` | 低 | 当前为整数秒数；S3 协议在配置中按天建模。`int` 类型丢失了"天"的语义，且无法区分 MODE（GOVERNANCE vs COMPLIANCE）。 |
| `ExpireAfterDays` 为标量 | 低 | 当前足以满足基线需求，但在支持分层转换时需改为结构化 JSON 或关联表。 |
| SSE 请求头被静默忽略 | 高（安全） | 并非真正的数据泄露，但造成了安全幻觉——客户端认为启用了加密而实际并未启用。 |
| `copyObject` 使用 `io.Copy` | 中等 | 对大于 RAM 的对象会造成 OOM 或磁盘溢出。虽然未设置明确的缓冲区大小限制，但在典型部署中 `r.Body` 受 `http.MaxBytesReader` 或 `ReadTimeout` 的限制。 |

---

## 2. 扩展方向

以下扩展方向从需求文档中提取，但以架构优先的视角加以重构。

### 方向 A：S3 请求头映射框架（P0 — 方向 1/3/5 的先决条件）

**为何需要：**

S3 兼容层的核心问题是一个重复出现的模式：每个 S3 请求头都是单独添加的，没有统一的框架。这种现用现取的做法导致 SSE 头、对象锁头和 Legal Hold 语义被静默丢失。在对 ListObjects UploadPartCopy 等进行增量修复之前，架构需要先解决根本原因。

**核心挑战：**

S3 协议请求头在不同的操作中承载不同的语义。例如，`x-amz-server-side-encryption` 在 `PutObject` 中是必需的，但在 `GetObject` 中也应被回显。请求头还需要在协议层、`FileService` 的 `PutOptions` 和 `repository.Object` 之间传播。每一层的字段是正交的，它们之间的映射必须精确。

**技术方案——两层抽象：**

```
s3compat handler layer (request headers ↔ s3Headers struct)
        ↓
FileService layer (PutOptions / GetOptions)
        ↓
Repository layer (Object columns)
```

引入一个 `s3Headers` 结构体，集中处理所有 S3 请求头的解析、验证和传播：

```go
// 概念设计——非实际代码
type s3Headers struct {
    // SSE
    SSEAlgorithm          string // AES256 | aws:kms
    SSEKMSKeyID           string
    SSECustomerAlgorithm  string
    SSECustomerKey        string
    SSECustomerKeyMD5     string

    // Object Lock
    LockMode              string // GOVERNANCE | COMPLIANCE
    LockRetainUntilDate   string
    LockLegalHold         string // ON | OFF
    BypassGovernance      bool

    // 其他
    StorageClass string
    ACL          string
    ContentMD5   string
}
```

**预期的架构变更：**

1. 在 `s3compat` 包中新增 `s3headers.go`，包含 `parseS3Headers(r)` → `s3Headers` 函数
2. `PutObject`、`GetObject`、`HeadObject` 的路由改为共享此解析逻辑
3. `FileService.PutOptions` 扩展 `SSEAlgorithm`、`LockMode`、`LockUntil` 字段
4. `repository.Object` 新增 `SSEAlgorithm`、`LockMode`、`LockUntil`、`LegalHold` 列
5. 迁移文件（SQLite + Postgres）添加列

**对现有系统的影响：**

- 低侵入性——当前的路由模式完全兼容
- 向后兼容——无 SSE 头的旧请求继续使用默认值
- Lock Legal Hold 从元数据 hack 迁移至结构化列是一次一次性迁移

### 方向 B：存储后端范围拷贝操作（P0 — 方向 2 和 4 的先决条件）

**为何需要：**

当前的 `copyObject` 实现使用 `FileService.Get`（流式读取整个对象）+ `FileService.Put`（流式写入）。对于同一后端内的拷贝（local→local、S3→S3），这浪费了网络和读写 IOPS。对于 >5GB 的对象，这是不可行的。

更重要的是，UploadPartCopy 需要复制字节范围而非整个对象的能力。

**核心挑战：**

`storage.Storage` 接口目前没有范围级拷贝操作。每个后端实现范围拷贝的方式不同：
- `local`：`io.CopyN` 与偏移量
- `s3`：`CopyObject` 与 `CopySourceRange`
- `oss`：`CopyObject` 与范围参数
- `cos`：同

**技术方案——扩展 `Storage` 接口：**

```go
// 概念设计——非实际代码
type CopyRangeOptions struct {
    SourceKey      string
    SourceOffset   int64
    SourceLength   int64 // 0 = 到文件末尾
    DestKey        string
    DestBucket     string // 如果是跨桶拷贝
}

type Storage interface {
    // ... 现有方法

    // CopyRange 在同一后端内复制字节范围。
    // 当后端不支持范围拷贝时返回 ErrNotImplemented，
    // 调用者应回退到 Get + Put。
    CopyRange(ctx, opts CopyRangeOptions) (ObjectInfo, error)
}
```

**预期的架构变更：**

1. `storage.Storage` 接口新增 `CopyRange`（带有回退到 `Get`+`Put` 的默认实现）
2. `FileService` 新增 `CopyRange(ctx, tenant, srcBucket, srcKey, dstBucket, dstKey, offset, length)` 方法
3. S3 handler 的 `uploadPart` 扩展以处理 `x-amz-copy-source` + `x-amz-copy-source-range`
4. `copyObject` 改为对大对象使用 multipart copy，而非流式读取

**对现有系统的影响：**

- `CopyRange` 的回退默认实现使所有现有后端立即可用
- 每个后端可以选择性地覆盖以提供高效的内部拷贝
- 此变更还使生命周期分层转换（方向 4）能够跨后端迁移对象

### 方向 C：Bucket 配置的规则引擎（P1 — 方向 4）

**为何需要：**

当前的 `BucketConfig.ExpireAfterDays` / `ExpireAction` 标量模型适合过期删除，但无法表达分层转换。S3 的生命周期规则引擎是一个转换 + 过期规则的排序列表。打破它意味着需要重新设计 bucket 配置模型。

**核心挑战：**

生命周期规则具有状态机语义：
1. 规则按 `Days` 升序应用（时间推移）
2. 每个转换将 StorageClass 从一个值变为另一个值
3. 规则既作用于当前对象，也作用于新上传的对象（基于创建日期）
4. 转换可以跨不同存储后端（从本地 → S3，或从 STANDARD → GLACIER）

此外，bucket 配置的存储方式目前是列式的（`Versioning`、`ObjectLockSeconds`、`ExpireAfterDays`、`ExpireAction` 都是独立的列）。将其改为包含规则数组的结构化 JSON 或关联表需要仔细规划迁移路径。

**技术方案——两种路径：**

| 路径 | 实现方式 | 优点 | 缺点 |
|------|----------|------|------|
| A：JSON 列 | `bucket_config` 增加 `lifecycle_rules TEXT` JSON 列，连同 `Versioning`、`Lock` 等现有列 | 迁移量小；可在业务逻辑层解析 JSON | 无法通过 SQL 查询规则；验证必须发生在应用层 |
| B：关联表 | `lifecycle_rules` 表，每行一个转换/过期，通过桶做 FK | 可查询；可用 `ORDER BY days ASC` 排序 | 迁移量大；需要 JOIN；生命周期规则的增删改更复杂 |

**建议：路径 A，按需延迟迁移到路径 B。**

路径 A 满足已知要求，且与当前基于列的存储方式兼容。当规则数量增多或需要跨规则查询时，可迁移到路径 B。

**预期的架构变更：**

1. `repository.BucketConfig` 新增 `LifecycleRules []LifecycleRule` 字段
2. 迁移：`ALTER TABLE buckets ADD COLUMN lifecycle_rules TEXT NOT NULL DEFAULT '[]'`
3. `internal/reconcile/lifecycle.go` 中的 `LifecycleJob` 扩展为：
   - 读取规则 → 找到到期的转换 → 执行转换
   - 转换的执行器：`FileService.TransitionObject(ctx, tenant, bucket, key, targetStorageClass)`
4. 添加 `TransitionObject` 方法到 `FileService`，用于执行实际的对象迁移（同后端标记或跨后端复制）
5. 更新 S3 兼容层的 `lifecycleRule` XML 结构体以解析/序列化 `Transition` 元素

**对现有系统的影响：**

- 向后兼容：未设置新 JSON 列的旧 bucket 继续使用标量 `ExpireAfterDays`/`ExpireAction`
- 迁移期间，`LifecycleJob` 检查新旧两种格式，优先读取 JSON 数组

### 方向 D：对象锁定合规与绕过模型（P2 — 方向 5）

**为何需要：**

当前实现将所有锁定对象视为 COMPLIANCE 模式（不可绕过）。对于大多数用例这是安全的默认，但阻止了管理员在紧急情况下删除 GOVERNANCE 锁定的对象。根据 S3 协议，GOVERNANCE 模式允许具有 `s3:BypassGovernanceRetention` 权限的特权用户绕过保留。

**核心挑战：**

绕过机制需要两个组件：
1. **协议层**：读取 `x-amz-bypass-governance-retention: true` 请求头
2. **权限层**：检查调用者是否具有绕过权限（基于 API key scope 或 IAM policy）

当前 AeroVault 的鉴权模型（API key scopes + bucket policies）足以支持这一点，但需要新增一个 `s3:BypassGovernanceRetention` scope。

**技术路径：**

1. **数据模型**：`objects` 表新增 `retention_mode TEXT`（`GOVERNANCE`/`COMPLIANCE`/NULL）和 `legal_hold BOOLEAN` 列
2. **迁移**：现有 `locked_until` 非空的对象默认设为 `COMPLIANCE`（维持当前安全基线）
3. **`FileService` 的 `hardDeleteObject`**扩展为：
   - 检查 `retention_mode` 为 `COMPLIANCE` → 拒绝（当前行为）
   - 检查 `retention_mode` 为 `GOVERNANCE` → 仅当调用者显式提供绕过权限时才允许
4. **鉴权**：`auth` 包新增 `ScopeBypassGovernanceRetention`；API key 管理中添加该 scope
5. **Legal Hold**：将 `_aero_legal_hold` 元数据魔术字符串迁移为 `objects.legal_hold` 列；新增 `GET /{key}?legal-hold` 和 `PUT /{key}?legal-hold` 端点
6. **Bucket 默认锁**：`BucketConfig.ObjectLockSeconds` 改为结构化对象 `ObjectLockConfig{Mode, Days}`

**对现有系统的影响：**

- **重要**：现有锁定对象的默认 `COMPLIANCE` 意味着迁移后所有现有锁定对象仍然不可绕过。这是一个正确的选择——不应在迁移时降低安全性。
- 独立于方向 A 和 B；可在任何时间点实现

### 方向 E：异步操作框架（架构改进，跨领域）

**为何需要：**

方向 2（UploadPartCopy）和方向 4（分层转换）都涉及长时间运行的操作。UploadPartCopy 可能包含数百个 5MB 的分片。分层转换可能需要将 TB 级别的数据从一个后端迁移到另一个后端。同步处理这些操作是不切实际的——会导致 HTTP 超时和连接池耗尽。

当前，只有索引和复制使用了工作队列。生命周期和 Antivirus 是定时轮询的。没有统一框架来建模长时间运行的操作。

**核心挑战：**

S3 没有原生异步操作框架（除了 `POST ?restore`，它返回 `202 Accepted`）。大多数客户端期望 `UploadPartCopy` 是同步的——每个分片请求返回一个 `CopyPartResult`。但一些操作（如 Glacier 恢复、批量删除、分层转换）需要异步状态跟踪。

**技术方案——扩大 `internal/jobs` 的使用范围：**

```go
// 概念设计——非实际代码
type AsyncOperationStatus struct {
    ID        string
    Type      string // "transition" | "restore" | "replicate"
    Status    string // "pending" | "in_progress" | "completed" | "failed"
    Progress  float64 // 0.0 - 1.0
    CreatedAt time.Time
    UpdatedAt time.Time
}
```

**适用于异步执行的候选项：**

| 操作 | 当前实现 | 建议 |
|------|----------|------|
| UploadPartCopy (>5GB) | 不存在 | 同步（每个分片）；整体拷贝异步？否——S3 协议限制 |
| 分层转换 | 不存在 | 异步——每个对象的转换应通过工作队列 |
| Glacier 恢复 | `restoreObject`（同步） | 异步——返回 `202`；后台从 Glacier 恢复 |
| 批量删除 | `deleteObjects`（同步内联） | 异步——对大批量的删除提交为 job |
| Replication | 已用 job 队列 | 保持——符合此模式 |

**预期的架构变更：**

1. 新增 `AsyncOperation` 表和 `repository` 方法
2. `POST /{key}?restore` 改为提交恢复 job 并返回 `202`
3. 分层转换 (`LifecycleJob`) 为每个对象提交 job，而非同步执行
4. 新增 `GET /{bucket}?async-status&id=...` 端点，供客户端轮询长时间运行的操作状态

**对现有系统的影响：**

- 工作队列已经存在且经过实战检验。此方向扩大了其适用范围。
- 对 `restoreObject` 的变更需要更新 `s3compat` handler

---

## 3. 接口设计建议

### 3.1 关键模块的接口设计原则

**原则 1：`Service` 层是唯一的事实来源。**
协议适配器（s3compat、REST、WebDAV、MCP）不应包含任何业务逻辑。每条业务规则必须位于 `internal/service` 中的 `FileService` 方法内。这意味着：

- S3 handler 的 `checkBucketPolicy` 应委派给 `FileService.CheckBucketPolicy(ctx, tenant, bucket, action, remoteAddr)`，而非在 handler 中调用 `auth.Allowed` 并获取 bucket 配置
- 当前代码将 `auth.ParsePolicy` 和 `auth.Allowed` 调用放在 handler 中，应将其推入 `FileService` 层

**原则 2：每个协议适配器需要对请求头做无损失的集中解析。**
方向 A 中描述的 `parseS3Headers` 函数应在适配器入口处解析所有 S3 请求头，将结果传递给 `FileService` 的 `PutOptions` / `GetOptions`。这消除了跨多个 handler 函数重复解析的问题。

**原则 3：`Storage` 接口的扩展应始终提供回退。**
`CopyRange` 的回退默认实现（使用 `Get`+`Put`）确保新方法不会破坏现有后端。这是当前 `Storage` 接口设计中已验证的模式（例如 `PresignGet` 可以通过生成一个临时 `Get` URL 来实现）。

### 3.2 是否需要新的抽象层

**引入 `internal/s3compat/headers.go` 是必须的。** 这是对现有代码的提取重构，而非新增抽象。

**引入 `internal/operations` 包**（用于长时间运行的操作）是值得的，但不应是第 0 步。首先在 `internal/jobs` 内部扩展，当复杂度超过单个包的能力时再提取。

**不需要引入额外的数据访问层。** 当前的 `repository.Repository` 接口 + `sql.go` 共享内核的模式是 Go 生态中的成熟模式。Repo 接口已经很大（40+ 方法），但这是特性丰富的对象存储系统的自然产物。按领域拆分为子接口（`ObjectRepository`、`BucketRepository`、`JobRepository`）是一种可能的未来演进，但不是必须的。

### 3.3 向后兼容性

| 变更 | 兼容性策略 |
|------|------------|
| `BucketConfig` 新增 `LifecycleRules` | 新增 JSON 列，默认 `[]`；读取时若为空则回退到 `ExpireAfterDays`/`ExpireAction` |
| `Object` 新增 `SSEAlgorithm`/`RetentionMode`/`LegalHold` | 全为 NULLable，默认 NULL；读取时 NULL = 未加密/未锁定 |
| Legal Hold 从元数据迁移到列 | 启动时将 `_aero_legal_hold` 元数据写入迁移到 `legal_hold` 列 |
| `Storage` 接口新增 `CopyRange` | 提供接口默认实现；不要求后端实现 |
| JWT/API key scopes 新增 `BypassGovernanceRetention` | 旧 key 无此 scope → 无法绕过（安全默认） |

---

## 4. 技术选型

### 4.1 是否需要引入新的技术栈

**不需要新框架。** 当前的 chi router、SQLite (modernc)、pgx、AWS SDK v2 已满足所有要求。以下为新能力的建议：

| 能力 | 推荐方法 | 替代方案 |
|------|----------|----------|
| 生命周期分层的规则存储 | `bucket_config` 中的 JSON 列 | 独立关联表 |
| SSE-KMS 的密钥轮换 | 已有机制 (`STORAGE_SSE_REWRAP_ON_START`) | 不变 |
| 跨后端复制（分层方向） | 复用现有的 `replication` 引擎和 job 队列 | 新增专门 worker |
| SSE-C 客户密钥 | 内存缓存，不持久化 | 不在磁盘存储密钥 |

**关于 SSE-C 的一个说明：** SSE-C 要求系统**不持久化**客户提供的密钥。当前的数据模型（`Object.SSEAlgorithm` 等）足以存储加密元数据，但密钥应作为 `PutOptions` 参数传递，不存入数据库。这是 S3 协议的要求。

### 4.2 第三方依赖评估标准

当前代码库对第三方依赖的限制严格，这是正确的。新增依赖的评估标准如下：

| 标准 | 阈值 | 说明 |
|------|------|------|
| **必要性** | 无法用 200 行以内标准库实现 | 任何新的依赖必须满足此条件 |
| **稳定性** | 语义化版本 ≥1.0，发布时间 ≥1 年 | 依赖项在 1.0 版本之前不可用 |
| **许可证** | 必须是 MIT/BSD/Apache 2.0 | GPL/LGPL/AGPL 不兼容 |
| **大小** | `go.mod` 的传递依赖 ≤5 个 | 拒绝"一个依赖带 20 个间接依赖"的情况 |
| **测试** | 测试运行不需要外部服务 | 不能需要 Docker/网络 |

满足这些条件的候选依赖极少。当前代码库几乎完全只用 Go 标准库和 AWS/Oss/Cos SDK——后三者是必需的（否则需要自行重新实现完整的 S3 API）。

### 4.3 自建 vs 采购

所有五个方向都必须是自建的。这不是一项可以采购的能力——S3 协议兼容层、存储分层和对象锁定都是核心业务逻辑，不属于任何 SaaS 产品或开源库的范围。唯一可能的例外是**密钥管理系统（KMS）**，但 `STORAGE_SSE_KMS_*` 配置已经存在，意味着这一决策早已做出。

---

## 5. 实施路线图

### 5.1 优先级排序（按依赖顺序）

```
Phase 0 (P0 — 基础)
  ├── 方向 A：S3 请求头映射框架
  ├── 方向 1：ListObjects delimiter
  └── 方向 B：CopyRange + UploadPartCopy

Phase 1 (P1 — 成本/安全)
  ├── 方向 3：SSE 请求头桥接
  └── 方向 C：生命周期分层规则引擎

Phase 2 (P2 — 合规)
  └── 方向 D：对象锁定合规模式

Phase 3 (跨领域)
  └── 方向 E：异步操作框架
```

### 5.2 阶段划分和里程碑

#### 阶段 0（核心协议兼容性）— 估算：2-3 周

**里程碑 0.1：S3 请求头框架**
- 创建 `internal/api/s3compat/headers.go`，包含 `parseS3Headers` 和 `s3Headers` 结构体
- 将 `PutObject`、`GetObject`、`HeadObject`、`DeleteObject` 重构为使用统一解析
- 为 SSE 字面量（`AES256`、`aws:kms`）和锁模式（`GOVERNANCE`、`COMPLIANCE`）定义常量
- 编写请求头解析的单元测试（不需要运行时集成）

**里程碑 0.2：ListObjects delimiter**
- 将 `ListPage` 扩展为包含 `CommonPrefixes []string`
- 在 `listObjectsV2` handler 中实现应用层分组（方案 2）
- 为 `listObjectsV1` 同步实现
- `listBucketResult` 和 `listBucketResultV1` 的 XML 结构体新增 `CommonPrefixes`
- 测试：带有虚拟文件夹的 bucket，跨页边界的公共前缀，与 marker 的组合
- 预期代码量：~150 行

**里程碑 0.3：CopyRange + UploadPartCopy**
- `storage.Storage` 接口新增 `CopyRange`，回退实现使用 `Get`+`Put`
- `FileService` 新增 `CopyRange` 和 `UploadPartCopy` 方法
- S3 handler 新增 `uploadPartCopy`，通过 `x-amz-copy-source` + `x-amz-copy-source-range` 与 `uploadPart` 区分
- 每个后端（local、s3、oss、cos）的可选优化
- 测试：同一后端内拷贝，跨后端拷贝，>5GB 对象的使用 multipart

**验证：** `aws s3 cp largefile s3://bucket/key` 成功；`aws s3api list-objects-v2 --delimiter /` 返回正确的公共前缀

#### 阶段 1（成本优化与安全）— 估算：2-3 周

**里程碑 1.1：SSE 请求头桥接**
- 基于阶段 0.1 的请求头框架实现
- `PutOptions` 新增 `SSEAlgorithm` 字段
- `PutObject` 验证 `x-amz-server-side-encryption`：仅接受 `AES256`
- 当 `STORAGE_SSE_KEY` 已配置时，确保 SSE 请求的对象通过本地加密路径
- `GetObject`/`HeadObject` 响应回显 `x-amz-server-side-encryption: AES256`
- `objects` 表新增 `sse_algorithm` 列（迁移）
- 预期代码量：~200 行

**里程碑 1.2：生命周期分层规则引擎**
- `repository.BucketConfig.LifecycleRules []LifecycleRule` + JSON 迁移
- 读取生命周期规则的 `Transition`（S3 XML → 内部结构体）
- `LifecycleJob` 扩展为处理转换规则：
  - `ListEligibleForTransition(tenant, rule)` → 查询 `storage_class != target AND created_at <= now - days`
  - 执行器：`FileService.TransitionObject` → 同后端标记或跨后端流式复制
- 写入 `sweep` 日志（`objects transitioned: count`）
- 测试：转换到 STANDARD_IA，转换到 GLACIER，跳过锁定对象
- 预期代码量：~400 行

#### 阶段 2（合规深度）— 估算：2 周

**里程碑 2.1：对象锁定合规模式**
- `objects` 表新增 `retention_mode TEXT`、`legal_hold BOOLEAN` 列 + 迁移
- `BucketConfig.ObjectLockSeconds` 改为结构化 `ObjectLockConfig{Mode, Days}`
- `hardDeleteObject` 扩展为区分 GOVERNANCE/COMPLIANCE
- 鉴权层新增 `scope:bypass-governance-retention`
- 删除流程：检查 `x-amz-bypass-governance-retention: true` + 权限评估
- Legal Hold 独立端点：`GET /{key}?legal-hold` + `PUT /{key}?legal-hold`
- 将 `_aero_legal_hold` 元数据迁移到 `objects.legal_hold` 列
- 预期代码量：~300 行

#### 阶段 3（异步操作框架）— 估算：1-2 周

**里程碑 3.1：异步操作基础**
- `AsyncOperation` 表（`id`、`type`、`status`、`progress`、`created_at`、`updated_at`）
- `repository` 方法：`CreateAsyncOperation`、`UpdateAsyncOperation`、`ListAsyncOperations`
- `POST /{key}?restore` 改为返回 `202 Accepted` + `AsyncOperationID`
- `GET /{bucket}?async-status&id=...` 端点，用于轮询
- 分层转换为每个对象转换提交 job，而非同步执行

### 5.3 风险点和缓解策略

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| **JSON 列中的生命周期规则变得过大**（数千条规则） | 低 | 中 | 初始支持最多 100 条规则/桶。若需要更多，迁移到关联表。 |
| **复制 + 分层之间的竞态条件**：分层规则与复制线程同时操作同一对象 | 中 | 高 | 分层转换应为幂等的。对正在被分层中的对象跳过复制。在对象级别使用乐观锁（版本 ID）。 |
| **UploadPartCopy 与 SSE-C 组合**：复制加密对象需要传递客户密钥 | 低 | 中 | 在方向上暂不实现 SSE-C（仅在未加密对象上支持 UploadPartCopy）。SSE-C 留待后续独立方向。 |
| **存储分层转换中断**：转换过程中服务重启导致对象处于不一致状态 | 中 | 高 | 分层应使用两阶段提交：1) 标记为 "transitioning_in_progress" → 2) 复制 → 3) 更新 storage_class → 4) 删除旧的 blob。第 1 步是幂等的。如果第 2 步失败，对象回退到旧的 storage_class。 |
| **Legal Hold 迁移**：将 `_aero_legal_hold` 从元数据迁移到列时，如果启动期间未完成迁移，可能会出现短暂的不一致 | 中 | 低 | 启动扫描：检查所有对象是否拥有 `_aero_legal_hold` 元数据但 `legal_hold` 列未设置。运行后台迁移。设置为一次性修复，如需可重复。 |
| **COMPLIANCE 模式的数据丢失**：人员错误地将 COMPLIANCE 锁定的对象标记为 "ready for deletion" | 低 | 高（不可逆） | COMPLIANCE 模式意味着即使在审计日志中也不可恢复。这是设计行为，与 AWS S3 一致。应在每次 COMPLIANCE 删除尝试（即使是失败的尝试）时在审计日志中记录告警。 |

### 5.4 关于测试策略的说明

所有五个方向都应遵循现有测试模式：

| 测试类型 | 标准 |
|----------|------|
| **单元测试（面向协议）** | `s3Headers` 解析、`ListObjects` 分组、`CopyRange` 回退逻辑 |
| **单元测试（面向服务）** | 使用 mock storage + mock repo 的 `FileService.TransitionObject` |
| **Repo 集成测试** | 针对 SQLite 的内存测试（新迁移 + 查询） |
| **Storage 合约测试** | 针对 local backend 的 `CopyRange` 合约测试 |
| **S3 handler HTTP 测试** | `httptest.NewRecorder` + 带有 delimiter 的 mock `FileService.List` |
| **端到端（可选）** | `make test-integration` 使用 Postgres（方向 B 的 Docker 测试） |

方向 A（请求头框架）没有要求新的集成测试——所有请求头解析都是纯函数，可以用标准 `testing` 包完整测试。

---

## 总结

核心架构诊断很明确：**AeroVault 的架构设计是正确的，但它在 S3 协议兼容性方面运行到了一个关键边界。** 每个报告的协议缺口（delimiter、UploadPartCopy、SSE、分层、合规）都是一个独立的切入点，但它们都有一个共同的根源：S3 请求头处理没有一个统一的架构，以及 bucket 配置模型在向表达性生命周期规则演进方面受到限制。

本文档提出的是一个架构计划，而不是功能计划。建议的五个方向按正确的顺序排列——解决请求头框架（方向 A）和拷贝范围操作（方向 B）的架构债务，然后再处理增量功能。这种排序降低了每个后续方向的技术风险，并确保重构完成后端到端的一致性。
