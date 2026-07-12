以下是基于对代码库（`internal/service/`、`internal/storage/`、`internal/repository/`、`internal/reconcile/`、`internal/api/s3compat/`）的深入审查以及所提供的分析文档，所做的全面架构分析。

---

# 架构分析：AeroVault 对象锁定、版本生命周期与写入路径补偿

## 1. 架构评估

### 1.1 当前架构的优势

当前代码库展现了非常扎实的工程基础：

- **单一会话门面（`FileService`）**：所有协议（REST、S3、WebDAV、MCP）都适配到同一个 `FileService` 入口点。这确保业务规则（配额、锁定、版本控制）不会因协议而意外绕过。
- **存储抽象**：`storage.Storage` 接口设计简洁、后端无关，并包含一个合约测试套件（`storage/contract_test.go`）。多后端（local、s3、oss、cos）意味着核心逻辑无需感知底层 blob 存储。
- **版本稳定性**：`storageKey(tenant, bucket, key) + "@v" + versionID` 方案保证了 blob 的幂等映射。没有两个版本会写入相同的物理键，这使生命期管理、GC 和审计更加简单。
- **事件驱动架构**：使用基于表的作业队列（`jobs` 表）的解耦事件总线实现了可靠的异步处理（索引、反病毒、复制），而不会阻塞同步对象 CRUD 路径。
- **特性门控**：AI、向量数据库、事件传输等通过环境变量严格启用。在默认（最小）配置下，零网络、零外部依赖、零 Docker。

### 1.2 关键架构债务

**1. WORM 模型中缺少保留模式**

这是目前实现中最根本的架构缺陷。`locked_until` 是一个 `*time.Time` 字段——它没有保留模式（`GOVERNANCE` vs `COMPLIANCE`）。在 S3 语义中，`GOVERNANCE` 允许具有特殊权限的用户（`s3:BypassGovernanceRetention`）在锁定到期前覆盖或删除对象，而 `COMPLIANCE` 则绝对禁止，且无法绕过，直到保留期结束。

代码库中使用的单一 `locked_until` 检查（`file_crud.go:116`）对两种模式使用统一的 `ErrLocked` 测试。如果未来引入 `RetentionMode`，则需要：
- 在 `objects` 表中增加一个新列
- 修改 `file_crud.go` 中的 `hardDeleteObject` 来区分 GOVERNANCE（可根据配置绕过）和 COMPLIANCE（绝对禁止）
- 修改 SSE 重封装路径（`rewrap.go:RewrapObject`），使其在 COMPLIANCE 模式下跳过兼容性对象
- 修改 `retention.go:purgeSoftDeleted`，使其在 COMPLIANCE 保留期内跳过对象

**2. `copyObject` 全量内存读取**

`internal/api/s3compat/extra.go:91` 中的 `copyObject` 处理函数通过 `s.svc.Get()` 将整个源对象读入内存，然后将其传递给 `s.svc.Put()`。没有针对大文件的分块拷贝策略。对于 ≥500MB 的拷贝，这是一个直接的 OOM 故障模式。同时，`parseCopySource` 会剥离 `?versionId=`，因此该处理程序始终获取当前版本，尽管基础设施支持按版本拷贝。

**3. 生命周期范围仅限于当前版本**

`ListExpired`（`sql_buckets.go:179`）仅选择 `deleted_at IS NULL`（即当前活跃版本）的对象。它完全忽略了非当前版本（由版本控制桶中 `deleted_at` 非空的旧版本表示）。这意味着：

- 一个用户可以为某个键上传 10,000 个文档版本
- 当前版本会被生命周期正确过期
- 但 9,999 个非当前版本会无限期堆积
- 存储成本呈线性增长，但用户没有有效机制来管理它

`BucketConfig` 有 `expire_after_days` 和 `expire_action`，但没有 `noncurrent_expire_days` 或 `max_versions`。

**4. 写入路径中的补偿缺口**

当 `store.Put` 成功但后续操作（`buildPutObject` → `writePutObject`）失败时，blob 会成为孤儿。当前代码库记录一条错误（"repo write failed; storage object orphaned"），但不执行回滚。这对于发生故障后连接的应用场景（例如，S3 上的文件已上传，但 `InsertObjectVersion` 事务冲突）来说是可以接受的，但在 Postgres 部署中，如果在 `CompleteMultipart` 过程中存储和后端之间的连接中断，则会导致不可撤销的泄漏。

**5. SSE 重封装未感知 WORM**

`RewrapObject`（`rewrap.go:67`）会扫描每个存储对象并重写其侧车封套，而不会检查对象保留模式。如果对象处于 COMPLIANCE 锁定状态，则其封套不应被修改——对不兼容元数据的任何写入都会违反合规性。当前的重封装路径缺少这个过滤步骤。

### 1.3 关键设计决策评估

| 决策 | 评估 | 备注 |
|--------|----------|-------|
| `storageKey` 包含租户+桶路径前缀 | ✅ 正确 | 支持单桶无限租户；List 可通过前缀高效实现 |
| 基于事件总线的异步索引 | ✅ 正确 | 将延迟与写入路径解耦；作业队列提供持久性和重试 |
| 每个版本唯一的 `@v<id>` blob | ✅ 正确 | 防止覆盖；支持时间点访问；简化 GC 语义 |
| 协议适配器无中间件链 | ⚠️ 有风险 | 如 AGENTS.md 所述，Handler 自己挂载中间件……“隔离处理程序测试没有租户/鉴权——设计如此”。对于集成测试来说，这是一个需要警惕的问题。|
| `locked_until` 作为 `*time.Time`，无 `RetentionMode` | ❌ 债务 | 无法区分 GOVERNANCE 和 COMPLIANCE；缺乏绕过能力 |

---

## 2. 扩展方向

### 方向 A：合规锁定模型（保留模式）— P0

**为何需要：** 没有 `RetentionMode`，WORM 实现就不完整。要求遵守 SEC Rule 17a-4（金融）或 ITAR（国防）的客户需要 COMPLIANCE 锁，而不仅仅是基于时间的锁定。AWS S3 对象锁定将 `GOVERNANCE` vs `COMPLIANCE` 作为核心语义。

**核心挑战和技术难点：**
1. **模式提取器安全性**：COMPLIANCE 必须不可绕过。不仅 `hardDeleteObject` 需要检查它，而且每个可能修改潜在合规对象元数据或数据的代码路径都需要检查——SSE 重封装、生命期过期、租户桶删除、存储类迁移。
2. **模式提升的一次性约束**：桶策略可以定义默认保留模式，但一旦对象设置为 COMPLIANCE，就不能降级为 GOVERNANCE，也不能缩短保留期。这需要在存储库层（而不仅仅是服务层）实施。
3. **绕过模型**：GOVERNANCE 模式需要一种机制来授予绕过（S3 的 `x-amz-bypass-governance-retention: true` + 相应的 IAM 权限）。这需要上下文感知的 `hardDeleteObject`/`overwriteObject` 签名。
4. **模式驱动的 SSE 保护**：COMPLIANCE 模式必须禁止 SSE 封套重写（`rewrap.go`）。但这意味着如果主密钥轮换，COMPLIANCE 对象将保留旧的 `kid`——它们不能被删除，必须保持可读，但轮换后发起的对象将使用新密钥。可接受。

**预期的架构变更：**
- 模式迁移 0025：`ALTER TABLE objects ADD COLUMN retention_mode TEXT DEFAULT ''`
- 新的内部领域常量：`RetentionModeGovernance`、`RetentionModeCompliance`
- 修改 `hardDeleteObject` 以接受可选的 `bypassGovernance bool` 参数
- 修改 `RewrapObject` 以跳过 COMPLIANCE 对象
- 修改 `ListExpired` 以尊重 COMPLIANCE 保留期（生命期不得在 COMPLIANCE 防止硬删除到期前过期）

**对现有系统的影响：**
- SQLite/Postgres 均需模式变更。回滚需要使用 `0025.down.sql`。
- REST API `PUT /v1/files/{bucket}/{key}/lock` 需要扩展以接收 `mode` 字段。
- S3 兼容层 `x-amz-object-lock-mode` 头。
- 零影响：未使用锁的租户（常见情况）——`retention_mode` 列将为空字符串，当前行为不变。

### 方向 B：版本生命期（非当前版本过期 + MaxVersions）— P0

**为何需要：** 在版本控制桶中，这是头号存储成本风险。没有它，写入密集型工作负载将在几周内使存储成本爆炸式增长。AWS S3 生命期提供了 `NoncurrentDays` 和 `NewerNoncurrentVersions`；AeroVault 需要一个类似的机制。

**核心挑战和技术难点：**
1. **非当前版本选择**：`ListExpired` 目前使用 `deleted_at IS NULL`（当前版本）。非当前版本扫描需要按（租户、桶、键）分组，并选择 `deleted_at IS NOT NULL` 且 `updated_at < 截止时间` 的版本。查询需要仔细索引以避免全表扫描。
2. **`NoncurrentDays = 0` 语义**：在 S3 中，`NoncurrentDays: 0` 表示“过期所有非当前版本”。当前的 `ListExpired` 没有针对这种情况的特殊并行路径。0 天的概念是微妙的——它意味着“在版本变更为非当前状态后立即”。
3. **`MaxVersions` 作为保护措施**：与 S3 不同，S3 期望用户自己管理成本，AeroVault 可以默认设置 `MaxVersions` 保护（例如 100）。这需要一种新的桶配置字段，以及在 `InsertObjectVersion` 中的写入时检查——当前版本计数达到限制时，删除最旧的非当前版本（由 `updated_at ASC` 决定）。
4. **与非当前版本过期交互**：如果 `MaxVersions` 和 `NoncurrentDays` 都已配置，则在 S3 语义中，`MaxVersions` 优先（版本计数影响先于基于时间的过期触发）。

**预期的架构变更：**
- 桶配置扩展：添加 `noncurrent_expire_days`（int）、`max_versions`（int，0 = 无限制）
- 新的存储库方法：`ListNoncurrentVersions(ctx, tenant, bucket, keys, olderThanDays)`、`CountVersions(ctx, tenant, bucket, key)`
- 修改 `InsertObjectVersion`：如果 `max_versions > 0`，则在新版本之后检查版本计数，并发送一个用于清理最旧版本的事件（或同步执行）
- 在 `LifecycleJob`（`lifecycle.go`）中增加新的清扫循环 `sweepNoncurrent()`
- `NoncurrentDays = 0` 的特殊情况——立即过期

**对现有系统的影响：**
- 新模式迁移 0026。
- 对非版本控制桶零影响。
- `RECONCILE_INTERVAL_MINUTES` 生命周期清扫器需要第二个 DB 查询。
- 在 S3 兼容处理程序中，`PUT /{bucket}/?lifecycle` XML 解析器需要扩展以支持 `NoncurrentVersionExpiration` 规则。
- **交互：** 在 COMPLIANCE 锁定期间，不能删除非当前版本（方向 A 交互）。

### 方向 C：UploadPartCopy（服务端拷贝）— P1

**为何需要：** 没有 `UploadPartCopy`，大文件复制只能通过客户端变通方法实现：(a) 下载并重新上传每个部分（额外带宽 + 延迟），或 (b) 对中小型文件使用 `copyObject`（内存 OOM 风险）。对于大文件（≥100MB）的多部分上传，这是 S3 兼容性中缺失的最关键功能。

**核心挑战和技术难点：**
1. **存储范围拷贝**：`storage.Storage` 接口需要一个新的方法：`CopyPart(ctx, srcKey, dstKey, srcUploadID, dstUploadID, partNumber, rangeStart, rangeLength)`。每个后端（local、s3、oss、cos）的实现方式不同：
   - **local**：`io.CopyN` 到目标文件的偏移处——简单。
   - **s3**：使用 `UploadPartCopyInput`（AWS SDK 已支持）——简单。
   - **oss/cos**：各自 SDK 的 `UploadPartCopy`。
2. **字节范围解析**：`copySourceRange` 头格式为 `bytes=first-last`。如果指定了范围，则副本会使部分小得多，但 `CompleteMultipart` 需要合并后的 ETag。S3 规范要求源范围的字节对齐。
3. **版本控制源解析**：`x-amz-copy-source` 可以包含 `?versionId=`（当前 `parseCopySource` 会剥离它）。`UploadPartCopy` 需要解析它并传递到 Get 路径。
4. **MD5/校验和延续**：多部分副本继承源部分的 ETag（通过 `CopyPartResult.ETag`）。合并后的 ETag（通常）不是各部分 ETag 的 MD5 和——这一事实对于 S3 互操作很重要。

**预期的架构变更：**
- `storage.Storage` 接口新方法：`CopyPart(ctx, srcKey, dstKey, srcUploadID, dstUploadID, partNum int32, offset, length int64) (CopyPartResult, error)`
- `FileService.UploadPartCopy` 新方法（与 `UploadPart` 并行但接受 `CopySource` 参数）
- S3 兼容处理程序：在 `uploadPart` 中检测 `x-amz-copy-source` 头并分支到 `UploadPartCopy`
- 新 S3 API 响应结构体：`CopyPartResult` XML 格式

**对现有系统的影响：**
- 在所有存储后端实现该接口。local 后端需要文件描述符级别的范围读取。
- 存储后端的合约测试扩展（`contract_test.go`）。
- 对现有 `UploadPart` 或 `CompleteMultipart` 路径无影响。
- **风险：** 范围的服务器端字节偏移计算需要注意处理 `Encrypted`（SSE）对象——在 local 后端上，SSE 封套元数据仅与整个对象关联，而不是部分。但在 local 上，副本是重新加密的，不是纯范围拷贝，所以这不是问题。在 S3 上，后端 SDK 会透明地处理它。

### 方向 D：补偿事务框架 — P1

**为何需要：** 当前遗漏了写入路径故障后的清理。当 `store.Put` 成功但 `repo.UpsertObject` 失败（网络故障、约束冲突、死锁）时，blob 会成为孤儿。当前实现记录一条错误日志，但不会主动回收。在云存储后端（s3、oss、cos）上，这会产生 $$$ 成本。

**核心挑战和技术难点：**
1. **与故障模型解耦**：补偿事务可以设计为：
   **选项 A（被动重构器）**：`ReconcileJob` 定期扫描存储后端，列出所有 blob，并将它们与存储库行交叉引用。在 local 后端上可行；在 S3 上代价极高（每百万个对象 ~$0.50 的 List API 调用 + 网络延迟）。
   **选项 B（写入时补偿）**：每个写入路径都记录一个“意向”条目——一个临时租约形式的 `pending_write` 行或一个带有 `status=active` 的上传条目。如果 30 秒内没有 `complete` 信号，后台清扫器会删除孤立的 blob。更复杂但更精确。
   **选项 C（日志压缩）**：写入路径发布一个 `object.write.start` 事件以及完整的 payload。如果 `object.write.complete` 事件没有在 TTL 内到达，事件清理器会从存储中删除。重放 log 进行恢复。
2. **C 的存储后端遍历**：在 local 后端上，`List("")` ，在 S3 后端上，前缀分页 + `LastModified` 过滤允许增量扫描。对于 S3，每次 `ListObjectsV2` 调用需要 ~15-20ms，因此扫描 1000 万个对象需要大约 25 分钟的单线程时间。通过 `BatchedGet` 并行化前缀扫描是解决之道。
3. **非致命失败**：补偿绝不能破坏正确的对象。补偿器必须只清理存储库行不存在的 blob（从未被覆盖的 blob 不能清理）。

**预期的架构变更：**
- 新的存储库表：`pending_writes（object_id、storage_key、created_at、ttl）`（可选——取决于所选的补偿策略）
- 修改 `Put`、`CompleteMultipart`、`InitMultipart` 以在适当时创建/释放补偿条目
- 新的清扫器：`CompensationJob` 在存储后端上运行增量扫描 + 交叉引用
- 所有补偿路径的 OpenTelemetry 指标（`compensation_orphans_found`、`compensation_orphans_cleaned`）

**对现有系统的影响：**
- 对当前写入延迟基准测试零影响，直到启用清扫器。
- S3 后端需要特定于提供商的优化以避免全桶 List。
- 与方向 A 的交互：COMPLIANCE 对象绝不能由补偿器清理——它们必须保持完好，无论存储库状态如何。

### 方向 E：版本回退（恢复 + Diff）— P2

**为何需要：** 这是产品差异化的主要领域。S3 没有原生的“恢复”操作——你需要下载旧版本并重新上传。提供 `POST /v1/files/{bucket}/{key}/versions/{versionId}/restore` 会创建一个新版本，其内容与旧版本相同，从而提供可审计的回退轨迹。`Diff` 端点将是任何基于 Web 的内容管理系统的杀手级功能。

**核心挑战和技术难点：**
1. **旧版本 blob 存在性**：恢复操作必须验证旧版本的 blob 仍然存在（`store.Stat(oldStorageKey)`）。如果旧版本被生命周期裁剪（非 COMPLIANCE 保护），则恢复必须返回 410 Gone 而不是 404。
2. **存储键映射**：`storageKey` 包含原始的 `@v<id>`。恢复从 `storageKey(tenant, bucket, key) + "@v" + newVersionID` 创建一个新 blob，但读取 `oldStorageKey`。源 blob 和目标 blob 永远不会冲突，因为 `@v<id>` 后缀。
3. **元数据传播**：恢复是传递旧的内容和元数据，还是内容+当前元数据，还是让调用方指定？S3 复制的行为类似于 `Replace`（不保留源元数据）。清晰的标准语义很重要。
4. **Diff 的存储开销**：生成版本间的内容 diff 需要在同一台机器上物化两个 blob。对于大文件（>100MB），这是不现实的。Diff 可以限于元数据 diff（`ListVersions` 已经提供），或者如果大小差异低于阈值（例如 <10MB），才提供内容 diff。

**预期的架构变更：**
- `FileService.RestoreVersion(ctx, tenant, bucket, key, versionID) (Object, error)`——内部调用 `store.Get(oldStorageKey)`，然后调用 `store.Put(newStorageKey)` + `InsertObjectVersion`
- `FileService.DiffVersions(ctx, tenant, bucket, key, v1, v2) (DiffResult, error)`——仅在总大小 < 10MB 时实现
- REST 新路由：`POST /v1/files/{bucket}/{key}/versions/{versionId}/restore`
- 可选的 S3 兼容头：`x-amz-copy-source` 与 `?versionId=` 用于 `PutObject`（已解析但未路由到版本化副本）

**对现有系统的影响：**
- 对现有版本控制路径无影响。
- 增加存储使用（每次恢复 = 一个新 blob）。审计跟踪是副作用。
- 交互：如果方向 A（COMPLIANCE 锁）处于活动状态，则恢复不得缩短保留期窗口。

---

## 3. 接口设计建议

### 3.1 存储层的新接口——范围感知拷贝

当前 `storage.Storage` 接口缺少跨后端的部分拷贝原语。添加 `CopyPart`（仅用于方向 C）能够在不将数据拉到应用服务器的情况下实现服务器端多部分拷贝。

```
CopyPart(ctx, srcKey, dstKey, srcUploadID, dstUploadID string, partNum int32, offset, length int64) (CopyPartResult, error)
```

**理由：** O（对象大小）内存的读取+写入 会转化为对于 local 后端的 O（块大小），并且对于 S3/OSS/COS 后端，则转化为零内存开销。

**备选方案评估：** 更通用的 `CopyObject(src, dst, options)` 方法可以同时处理单部分和多部分拷贝。但对于多部分，状态管理（上传 ID 映射、部分编号排序）使得服务层更适合协调，而不是向存储层公开原始的“范围读取+写入”。坚持使用专注于部分的方法。

### 3.2 保留模式枚举——版本迁移而非就地变更

不要添加一个可为空的 `retention_mode` 列，该列对模式变更不可见，而是使用一个单独的枚举：

```sql
-- 迁移 0025
ALTER TABLE objects ADD COLUMN retention_mode TEXT NOT NULL DEFAULT '';  -- '' | 'GOVERNANCE' | 'COMPLIANCE'
ALTER TABLE objects ADD COLUMN retention_bypass_count INTEGER NOT NULL DEFAULT 0;  -- 合规：记录绕过尝试
```

**权衡：**
- **选项 A（列上的 CHECK 约束）：** 强 DB 级不变性。但在 SQLite 中添加 CHECK 约束需要重新创建表。
- **选项 B（应用层枚举）：** 更简单，更灵活。反例：应用层 bug 会写入 'governance'（拼写错误）并失去模式语义。使用 Go 命名常量 + 写入前的存储库验证。

**推荐：** 选项 B + 在存储库层进行验证。Go 代码中枚举的强类型：

```go
type RetentionMode string
const (
    RetentionModeNone       RetentionMode = ""
    RetentionModeGovernance RetentionMode = "GOVERNANCE"
    RetentionModeCompliance RetentionMode = "COMPLIANCE"
)
```

存储库的 `SetLockedUntil` 也应接受可选模式，并在写入前验证 mode === COMPLIANCE 时的 `bypassGovernance`。

### 3.3 VersionListOpts 扩展——生命周期友好的分页

当前 `VersionListOpts` 支持 `Limit` 和 `VersionIDMarker`。为了高效的生命周期扫描（方向 B），存储库需要一种方法列出给定键的所有非当前版本，并按年龄排序：

```go
type NoncurrentVersionFilter struct {
    Tenant      string
    Bucket      string
    KeyPrefix   string       // 可选：限制范围
    OlderThan   time.Duration // NoncurrentDays
    Limit       int
    Cursor      string       // (version_id, updated_at) 组合键
}
```

### 3.4 补偿——适配器模式而非集中式框架

不要构建一个需要每个写入路径声明回滚的集中式 Saga 管理器（Saga 框架即使对于简单的系统也很快变得复杂），而应使用**基于清扫器的补偿**，该补偿利用存储后端遍历 + 存储库交叉引用：

```go
type Compensator interface {
    // ListStorageKeys 列出共享前缀 pattern 的存储键。
    ListStorageKeys(ctx, prefix, marker string, limit int) (ListResult, error)
    // DeleteOrphan 删除存储 blob 而不影响存储库。
    DeleteOrphan(ctx, storageKey string) error
}
```

每个存储后端（local、s3、oss、cos）实现 `Compensator`。清扫器循环协调交叉引用。这保持了关注点分离——存储后端知道如何列出其 blob，而清扫器知道如何将它们与 DB 行匹配。

---

## 4. 技术选型

### 需要什么新栈？

| 组件 | 所需 | 评估 |
|----------|-----------|----------|
| 向量搜索后端（pgvector 中的 ANN） | 用于大规模 RAG | 已存在（`AI_VECTOR_BACKEND=pgvector`），但未在 CI 中验证。在方向 B 或 C 之前不需要新栈。|
| Qdrant 集成 | 独立向量存储 | 已存在（`ai/qdrant.go`）——自动创建集合 + 余弦距离。在方向 A-E 之外不需要新栈。|
| **压缩/补偿的新存储库表** | 用于方向 D | 自建 `pending_writes` 表。不应引入外部框架——Go stdlib 的 `database/sql` 就足够了。|
| **基于 KMS 的封套轮换** | 用于方向 A 的 COMPLIANCE 保护 | 已存在（`storage/kms.go`）。在 COMPLIANCE 模式下，检查 KMS 包装是否跳过。|

### 总体评估：没有新的大型依赖

方向 A-E 都不需要新的外部运行时依赖。每个方向都利用现有的基础设施——存储库模式、迁移框架、事件总线、重构器——并进行扩展。

**新依赖的唯一候选：** UploadPartCopy（方向 C）可能受益于 `golang.org/x/sync/errgroup` 用于并行部分拷贝（用于 local 后端，其中可以同时读取多个块）。但这已经是 Go 标准库生态系统的一部分，不需要新的 `go.mod` entry。

### 自建 vs 采购决策

| 组件 | 决策 | 理由 |
|----------|--------|---------|
| 补偿事务框架 | **自建** | 问题领域很窄（写入路径清理），并且已嵌入应用的运行状况检查中。通用 Saga 框架（Temporal、Camunda）会增加过度复杂性。|
| 非当前版本过期逻辑 | **自建** | S3 生命期规则是简单的基于时间的谓词。没有可重用的 Go 库可以处理这个——它们都是特定于云提供商的。|
| 保留模式枚举 | **自建** | 与领域模型紧密耦合。无需第三方。|

---

## 5. 实施路线图

### 优先级排序

| 优先级 | 方向 | 复杂度 | 时间估算 | 为什么是这个级别 |
|----------|-----------|----------|--------------|-------------------|
| **P0** | A：合规锁定模型 | 中 | 3-5 天 | 安全关键型。缺失的模式字段在写入路径和 SSE 重封装中都造成了合规债务。|
| **P0** | B：版本生命期（NoncurrentDays + MaxVersions） | 中-高 | 5-7 天 | 财务关键型。没有它，版本控制桶在写入密集型工作负载下会无限增长。|
| **P1** | C：UploadPartCopy | 中-高 | 5-8 天 | 对于任何使用 S3 兼容 SDK 的客户来说，这都是缺失的顶级 SDK 功能。当前的内存中 copyObject 对生产来说是一种损害。|
| **P1** | D：补偿事务 | 中 | 5-7 天 | 云存储泄漏 $$$。没有补偿，每次写入失败都会变成一个孤立的 blob，在清理之前会产生持续的存储成本。|
| **P2** | E：版本回退 + Diff | 高 | 5-10 天 | 高产品差异化，但高实施成本。对于核心存储可靠性来说，这不是必需的。|

### 阶段划分

**第 1 阶段（方向 A + B，并行进行）— 里程碑：“存储成本保护”**

这两者是紧密耦合的——COMPLIANCE 锁和生命期都会影响版本修剪。并行处理它们确保了交互在初步设计期间被发现，而不是在集成测试期间。

**第 2 阶段（方向 C）— 里程碑：“S3 协议完整性”**

UploadPartCopy 对于多部分上传来说是独立功能。它可以独立于第 1 阶段进行开发，但应该针对第 1 阶段添加的 COMPLIANCE 锁保护进行测试。

**第 3 阶段（方向 D）— 里程碑：“写入路径可靠性”**

补偿在 S3 后端上提供了最大的价值。针对 local+S3 后端进行测试。先添加 `pending_writes` 表和清扫器，然后在后续迭代中将其连接到写入路径。

**第 4 阶段（方向 E）— 里程碑：“版本管理用户体验”**

版本回退在 UI 和 API 中都立即引人注目。Diff 是可选的，可以推迟。

### 风险与缓解策略

| 风险 | 可能性 | 影响 | 缓解 |
|------|----------|----------|-----------|
| COMPLIANCE 锁定对象与 SSE 重封装之间的交互 | 中 | 高（数据不可读） | 单元测试：COMPLIANCE 锁定对象被 `RewrapObject` 跳过。模糊测试边缘情况的封套轮换。|
| `ListExpired` 性能在版本控制桶上下降 | 高 | 中（清扫器慢，大量对象） | 在 `bucket + deleted_at + updated_at` 上建立索引。通过 `RECONCILE_TENANTS` 引入租户级分片。|
| UploadPartCopy 偏移计算对于加密对象不正确 | 中 | 中（部分损坏） | 合约测试使用 SSE 和非 SSE 对象。验证重新组装后的 ETag 是否与全量单部分上传匹配。|
| 补偿清扫器在本地后端上删除正常 blob（并发竞态） | 低 | 高（数据丢失） | 只有 `pending_writes` 表中具有有效 TTL 条目的 blob 才符合清理条件。不要仅根据存储库不存在就删除。|
| `MaxVersions` 默认值设置过低，导致用户数据意外丢失 | 低-中 | 高 | 对现有桶默认使用 `max_versions = 0`（无限）。仅对新创建的版本控制桶应用保守默认值（例如 100）。在文档中明确说明。|

### 交互矩阵：防止“哦，原来这两个方向是相互影响的”

| | 方向 A（COMPLIANCE） | 方向 B（Noncurrent） | 方向 C（UploadCopy） | 方向 D（补偿） |
|---|---------------------|---------------------|--------------------|----------------|
| **B（Noncurrent）** | COMPLIANCE 锁禁止 Noncurrent 过期 | — | — | — |
| **C（UploadCopy）** | 拷贝源受 COMPLIANCE 保护：拷贝必须跳过锁 | 非当前源可以拷贝（读取，不删除） | — | — |
| **D（补偿）** | 补偿清扫器必须跳过 COMPLIANCE 对象 | 非当前版本补偿必须保留完整历史 | 孤立的 UploadPartCopy 部分必须清理 | — |

**最关键的交互：** COMPLIANCE 锁定对象（方向 A）绝不能由生命期过期（方向 B）或补偿（方向 D）删除。这是两条路径中都需要的防御性检查，并且必须在所有清扫器中统一强制执行（而不是依赖于调用者传递正确的标志）。

---

## 总结

当前代码库展示了清晰的架构愿景——彻底的`FileService`门面，后端无关的存储接口，以及健壮的事件驱动异步处理。五个架构方向中：

1. **方向 A（COMPLIANCE 锁）** 解决了最根本的安全/合规债务。收益/努力比很高。
2. **方向 B（非当前版本生命期）** 解决了版本控制桶最昂贵的存储成本失控问题。`MaxVersions` 作为默认保护措施尤其有价值。
3. **方向 C（UploadPartCopy）** 修复了当前内存拷贝实现中等待发生的生产事故。
4. **方向 D（补偿事务）** 填补了云后端在故障时泄漏 $$$ 成本的空白。
5. **方向 E（版本回退）** 提供了最大的产品差异化价值，但实施范围也最广，可以安全地为后续迭代保留。

一个反复出现的架构主题需要特别注意：**所有清扫器路径（生命期、补偿、SSE 重封装）都必须知道 COMPLIANCE 模式**。一旦添加了保留模式，每个可能会变更或删除对象元数据/数据的后台进程都需要过滤掉 COMPLIANCE 对象。这是一个横向问题，应该在方向 A 的初步设计阶段通过一个集中的“COMPLIANCE 守卫”抽象来管理，而不是在每个清扫器中逐个处理。
