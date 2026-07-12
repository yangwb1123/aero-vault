现在我已全面了解代码库。这是我作为架构师的分析。

---

# 架构分析：aero-vault v21 状态与扩展路径

## 1. 架构评估

### 1.1 当前架构的优势

该架构在几个方面做出了出色的设计决策：

**清晰的层级分离。** 五层（协议 → 中间件 → 服务 → 存储 + 仓库 → 事件驱动）定义清晰，职责分割严格。`FileService` 作为唯一的 CRUD 入口点是一个强有力的选择——它确保写路径无论协议如何都应用相同的不变量（配额、锁检查、事件发布）。

**存储与仓库的双重抽象。** 将 blob 操作（`storage.Storage`）和元数据（`repository.Repository`）分离为两个独立接口是正确的决定。它实现了真正的后端可移植性：本地 FS → S3 → OSS → COS 无需更改仓库即可切换，SQLite → Postgres 无需更改存储即可切换。测试模式（内存 SQLite + 临时本地存储）直接证明了这种正交性。

**Option-in 安全默认。** AI、pgvector、Qdrant、事件、集群单例、保留、WebDAV 均通过 flag 门控，默认关闭。`nil` 的嵌入器/LLM/重排序器不会破坏核心 CRUD。这种防御性设计保护了基线部署路径。

**用户空间事件总线。** `events.Bus`（内存发布/订阅）被正确设计为尽力而为——错误被记录，永不破坏用户请求。订阅者（索引器、杀毒软件、复制、Webhook）独立运行；如果某个订阅者阻塞，只有该订阅者会掉队。

**集群单例。** `cluster.Singleton` 基于 `leases` 表提供了一种简洁的 fencing 模式，无需外部共识（etcd / ZooKeeper）。在 Postgres 下，它利用行级锁提供安全的分布式互斥。

**多租户是头等公民。** 每个存储键都带有租户前缀，所有元数据行按租户隔离，API 密钥限定租户范围。这不是事后添加的功能——它是从一开始就内置的。

### 1.2 局限性

该架构在抽象边界上存在明显的**完整性空白**：

**`storage.Storage` 接口有泄漏。** 它知道多部分上传（`InitMultipart`、`UploadPart`、`CompleteMultipart`、`AbortMultipart`），但不知道存储类别或生命周期转换。没有 `SetTier()` 或生命周期方法。这意味着存储分层转换无法实现可插拔的后端——每个实现都需要自己的转换路径。

**`WriteAccessLog` 是一个声明性占位符。** repo 接口上有方法签名，仓库实现完成，但零调用者。这是 I3（接口与实际使用不匹配）——一个明确的接口设计缺陷。

**`NotificationRule` CRUD 完整但无消费者。** 数据模型、S3 兼容处理程序、仓库实现都在那里——但没有任何订阅者读取这些规则并投递事件。这是一个功能性的断头路。

**`LegalHold` 是一个元数据标签，而不是头等实体。** 它作为 `_aero_legal_hold: ON` 存储在 `metadata` JSON 中。无法按法律保留对象进行查询，没有批量操作 API，没有人检查生命周期扫除中的法律保留（只有 `LockedUntil` 被检查）。删除前的检查只发生在 `FileService.hardDeleteObject` 中——如果绕过 FileService 直接调用仓库，法律保留就无效。

**`ListSoftDeletedBefore` SQL 对版本控制不感知。** 它过滤 `WHERE deleted_at IS NOT NULL AND deleted_at < $1`。在启用版本控制的桶中，旧版本恰好就是这样标记的（见 `InsertObjectVersion`）。这会导致**数据丢失**：保留 GC 会物理删除旧的版本化行及其 blob。这是一个 P0 安全缺陷。

**`ListObjectsByTag` 在 Go 层过滤。** 它拉取 1000 行，然后在客户端过滤。对于 >1000 个对象的大桶，标签过滤要么中断，要么无法扩展。这应该在 SQL 中完成（JSON 包含或按键值上的 GIN 索引进行 Postgres JSONB 过滤）。

**`StorageClassGauge` 硬编码 `"default"`。** 多租户部署在没有租户维度的情况下获得 storage-class 统计。这是 OTel 指标中的一个信息空洞。

### 1.3 技术债务

| 项目 | 严重性 | 影响 |
|------|--------|------|
| 保留 GC 移除版本化的行 | **P0** | 启用版本控制时的数据丢失 |
| Legal hold 是元数据标签 | P1 | 合规保留无效 |
| 标签过滤在客户端进行 | P2 | 大桶搜索性能 |
| 无搁置多部分 GC | P1 | 存储泄漏 |
| 存储类是无操作元数据 | P1 | 成本管理无效 |
| AccessLog 声明性但不活动 | P1 | 缺少合规足迹 |
| 通知规则无消费者 | P1 | 事件驱动架构空白 |
| Webhook 目标无速率限制 | P2 | 下游过载风险 |
| Presign 方法约束* | — | *已验证：方法通过 HMAC 签名强制执行* |

---

## 2. 扩展方向

### 方向一：存储分层与生命周期转换

#### 为什么需要

当前每个对象都有 `storage_class`，但本地 SSD、S3、OSS 或 COS 都不在乎。你可以在元数据中标记 `GLACIER`，但数据仍然坐在本地 SSD 上，成本不变。对于生产部署，自动降冷是：

- **成本控制**：30 天后 90% 的对象变为冷数据。在没有转换的情况下，所有数据都停留在热存储上。
- **合规性**：S3 兼容用户期望 `LifecycleConfiguration.Transition` 生效。
- **差异化**：多后端分层（本地 NVMe → S3 Standard → Glacier / OSS Archive）是一个强有力的卖点。

#### 核心挑战

1. **存储层接口侵入：** `storage.Storage` 需要一种生命周期感知方法或 `SetTier` 操作。每个后端必须以不同方式实现：
   - `local`：没有固有的分层概念——需要存储后端代理，该代理根据 storage class 将请求路由到不同的目录或后端。
   - `S3`：修改 `StorageClass` 参数或为智能分层调用 `CopyObject`。
   - `OSS`/`COS`：具有原生归档 tier API，但接口不同。

2. **跨后端转换：** 从本地 → S3 Glacier 意味着从 `storage.Storage` 的一个实现读取并写入另一个。当前架构不支持这一点——`FileService` 只持有一个 `storage.Storage{}` 实例。你需要一个路由层（**StorageRouter**），该路由层根据 storage class 将 GET/PUT 分派到正确的后端。

3. **恢复（解冻）：** 从 Glacier 读取需要先恢复。S3 恢复可能需要 12-48 小时。客户端需要一个 `RestoreObject` API（当前 `restore` 仅处理软删除恢复）和读取时的降级 semantics。

4. **重新加密：** 如果每个后端使用不同的 SSE 密钥信封，则转换数据必须解密然后重新加密。

#### 预期的架构变更

```
当前:
  FileService → storage.Storage (单一后端)

新:
  FileService → StorageRouter → storage.Storage (后端 A — 热)
                                → storage.Storage (后端 B — 冷)
```

- 新增 `tier-to-backend` 配置映射
- `reconcile/lifecycle.go`：从仅扫过期的到扫过期的 → 转换
- 新增 `RestoreObject` 到 `FileService` 和 `storage.Storage`（至少本地需要）
- `ListExpired` SQL：需要 `storage_class != last_tier` 和 `last_accessed < threshold` 过滤

#### 对现有系统的影响

**中到高。** StorageRouter 抽象是`storage.Storage`接口的突破性变更。数据迁移（将现有对象重新定位到正确的 tier）是一个单独的关注点——最好通过计划的 GC 扫描处理，而不是在写路径上。

---

### 方向二：搁置多部分上传 GC

#### 为什么需要

这是明显的资源泄漏。如果在 `CompleteMultipart` 或 `AbortMultipart` 之前客户端断开连接，上传数据和记录将永远停留。AWS S3 在 7 天后自动清理。大型 CI/CD 用户会在一周内积累 GB 级的僵尸数据。

#### 核心挑战

1. **存储层协调：** `storage.Storage` 接口没有 `CleanupParts(uploadID)` 方法。S3 后端已有通过 AWS SDK 的原生清理。本地后端需要一种方法来列出和删除每个上传 ID 的分段数据。对于 S3/OSS/COS，你需要映射 repo 的 `uploadId` 到存储提供商的 `uploadId`。

2. **时间边界准确性：** 仅仅说“超过 7 天未完成的上传”可能会过早地抓取上传完成但尚未完成最后一次 `CompleteMultipart` 调用的情况。你需要考虑 `part_updated_at`（最后一部分的时间）以及 `created_at`。

3. **存储端僵尸：** 如果 repo 记录说“已完成”但存储提供商仍然有活跃的上传，则会产生存储级僵尸。最好从存储提供商列出活跃的上传并将其与 repo 上传记录进行协调 — 但 S3 `ListMultipartUploads` 最多只能列出 1000 条，并且可能被分页。

#### 预期的架构变更

这是一个低影响、高价值的变更。它可以完全融入现有的 `reconcile` 框架：

- 新增 `reconcile/upload_gc.go` — 具有集群单例门控的 ticker 循环
- 新增 `storage.Storage` 接口方法：`CleanupParts(uploadID string) error`（所有实现都需要它）
- 新增 `repository.ListExpiredUploads(before time.Time)` 和 `DeleteUploadCascade(uploadID)`
- 配置：`UPLOAD_GC_TTL_HOURS`（默认 168 = 7 天），`UPLOAD_GC_INTERVAL`

#### 对现有系统的影响

**低。** 没有对写路径的变更。reconcile 包已经具有该脚手架（集群单例 + ticker）。这两个新仓库方法直接了当。

---

### 方向三：访问日志投递与通知调度引擎

#### 为什么需要

从合规性角度来看，这个是必要的。SOC2 / HIPAA / 金融监管要求访问日志。当前日志（`audit_log` 表）只记录管理员操作——没有记录普通的 GET/PUT/DELETE。通知调度是 S3 兼容性的差异化功能：用户在对象创建事件上设置从 S3 到 SQS/SNS 的管道。

#### 核心挑战

1. **日志卷：** 高吞吐系统每秒可能产生数千个访问日志条目。写入同一存储后端会产生递归（日志桶的写入事件 → 更多日志条目 → ...）。你需要：
   - 用于缓冲和批量写入的专用日志路径（可能有一个单独的 `access_logs` 表，具有自动轮换或直接写入后端桶的路径）
   - 日志桶的**无限递归保护**（通过 `sourceBucket != targetBucket` 或不触发写入事件的特殊日志 API）

2. **通知目标多样性：** 通知规则指定 `QueueARN`、`TopicARN` 或 `LambdaARN`。这需要：
   - AWS SQS/SNS HTTP API 代理
   - 多个云提供商的 equivalent（阿里云 MNS、腾讯 CMQ）
   - 每个提供商的认证机制（AWS SigV4、阿里云 AccessKey、腾讯 SecretKey）
   - 死信队列、投递重试、监控（当前 webhook 模式已经有重试样板——复用）

3. **通知引擎可能需要比 Webhook 更严格的订购：** SQS FIFO 队列需要消息组 ID 和去重 ID。当前事件总线不保证订购（Go 通道广播是随机的）。

#### 预期的架构变更

分析报告建议新建 `notification.Worker` 和 `accesslog.Worker` 包。我同意但有一个警告：**不要将两者作为独立的 goroutine 构建**。它们都应该订阅相同的事件总线，但关注点不同：

```
EventBus → notification.Matcher (将规则与事件匹配) → QueueARN 投递程序
         → accesslog.Flusher (缓冲并写入 access_logs 桶)
```

`accesslog` 处理程序应该是一个中间件，将日志行写入有界通道，`Flusher` 从中读取并批量写入。这会比订阅总线的对象寿命事件更及时地捕获 GET/*HEAD 访问（当前总线不发布 `EventAccessed` 类型——只发布 `EventCreated`、`EventDeleted`）。

**关于分析推荐的评论：** 报告说 3-4 周 → 降级为 2-3 周，因为 `Webhook` 逻辑可以被通知复用。我同意复用策略，但工期估算忽略了一个事实，即 `WriteAccessLog` 当前是一个 repo 方法（写入 SQL 表），而报告建议写入后端桶。这两种策略具有不同的操作特性。写入 SQL 表对于吞吐量来说更容易（批量插入）但查询历史日志会很痛苦（OLTP 与 OLAP 工作负载）。写入对象存储桶更接近 AWS 的做法（S3 访问日志 → 同一个或不同的桶），并且将日志与主元数据数据库解耦。我建议写入后端桶而不是 SQL——但这也意味着日志写路径上的存储抽象。**我估算 3 周**。

#### 对现有系统的影响

**中。** 需要在 middleware 层和事件消费者层进行新代码编写。没有现有的协议处理程序需要更改，除了新增 `WriteAccessLog` 调用。最大的风险是日志桶的无限递归，这需要仔细设计。

---

### 方向四：多活跨区复制与故障切换

#### 为什么需要

当前复制是单向且无法验证的——它写入副本时只打一个 `repl_status=replicated` 标签，而不检查对象大小或 etag。没有自动故障切换，没有一致性保证。对于生产高可用性，你需要：

- **可验证的复制**（在副本上对比 etag + size）
- **故障检测**（对主存储和副本存储进行健康检查）
- **切换/提升**（手动 promote API，未来自动故障切换）
- **读一致性**（写入后读取，预读副本）

#### 核心挑战

1. **裂脑（split-brain）：** 这是最大的风险。如果主 Region 和副本 Region 之间的网络分区发生时，两个 Region 都开始接受写入，当分区恢复时，它们具有分歧的数据。缓解措施：
   - 每个 Region 需要一个唯一的 `replicaIdentity`
   - 写操作需要 fencing token（基于租约的序列号）
   - 第三方仲裁器（或 `leases` 表）来拒绝过期 token
   - 单个配置值：`REPLICATION_ROLE=active|standby`，且从不自动切换

2. **复制延迟读一致性：** 用户写入主 Region，立即尝试从副本读取 > 读取旧数据。选项：
   - **写后读一致性标记**：`Put` 返回一个序列号；`Get` 可以使用同一租户的 `X-Aero-Read-After` 头来指定最小序列号。
   - **除非降级否则读主 Region**：路由 GET 到主 Region，除非主 Region 的健康检查失败。
   - 这是需要权衡的：强一致性会牺牲写入延迟。

3. **SSE 密钥同步：** 当前 SSE 是每个存储后端的本地配置。副本需要访问与主 Region 相同的密钥环。如果密钥是本地密钥文件，它们必须是相同的。如果密钥是 KMS 访问的，副本 Region 必须有权访问主 Region 的 KMS。

4. **`replication.Worker` 当前只有中等测试覆盖率：** 当前复制是通过工作池中的作业完成的，没有集成测试。在多 Region 场景可信之前，需要编写故障注入测试。

#### 预期的架构变更

- `replication/`：新增 `ConsistencyCheck()`（对比 etag + size）、`ReverseSync()`（将更改从副本同步回主 Region）
- `cluster/`：新增健康检测器（对两个存储后端进行 ping）、切换状态机（空闲 → 检测故障 → 提升 → 恢复）
- `config`：新增 `REPLICATION_MODE`（`async` / `sync` / `active-active`，虽然 active-active 确实需要裂脑预防）、`REPLICATION_HEALTH_CHECK_INTERVAL`
- `admin API`：`POST /admin/replication/promote`、`GET /admin/replication/status`
- `FileService.Put` 在同步模式下等待副本确认

#### 对现有系统的影响

**高。** 这是所有五个扩展方向中最复杂的。它会影响写路径（同步模式中的延迟）、存储抽象（一致性检查需要 `Stat` 或 `Get` 对象内容）、集群成员资格（新的检测器/切换状态机）和配置模型。对于首次迭代，我强烈建议**仅手动 promote**（无自动故障切换）和**异步复制**。在原型稳定性经过验证之前，避免主动-主动。

---

### 方向五：对象版本生命周期管理与合规保留

#### 为什么需要

这是我**迄今为止发现的最关键问题**。

1. **P0 数据丢失错误：** `ListSoftDeletedBefore`（在 `sql_buckets.go:245`）的 SQL 是版本控制不感知的。它查询 `WHERE deleted_at IS NOT NULL AND deleted_at < $1`。在启用版本控制的桶中，`InsertObjectVersion` 设置旧版本的 `deleted_at`。这意味着保留 GC 会物理删除与旧版本对应的行和 blob。**这是在没有明确警告的情况下丢失历史版本数据**。

2. **法律保留是元数据标签，而不是实体：** 当前 `_aero_legal_hold: ON` 存储在对象的 `Metadata` JSON 中。任何人都可以使用 `PUT /tags` 删除它。没有查询 API，没有批量操作。对于 SEC 17a-4 或 HIPAA 来说，这是无效的。

3. **非当前版本到期不存在。** S3 生命周期有 `NoncurrentVersionExpiration` 和 `NoncurrentVersionTransition`。没有它，每次上传都会产生一个永不过期的新版本——小文件存储成本无限期增长。

#### 核心挑战

1. **修复保留 GC / Versioning 冲突：** 需要迁移到在版本控制桶中感知版本的 SQL：
   ```sql
   -- 当前（有缺陷）:
   WHERE deleted_at IS NOT NULL AND deleted_at < $1
   -- 修复:
   WHERE deleted_at IS NOT NULL AND deleted_at < $1
     AND (version_id IS NULL OR version_id = '')  -- 跳过版本化的行
   ```
   但这还不够——版本化的行也需要自己的保留策略（`NoncurrentDays`）。生命周期 GC 需要第二个扫过器，它会物理删除早于 `NoncurrentDays` 的版本化行。

2. **法律保留作为头等实体：** 需要一个独立的 `legal_holds` 表（`object_id`、`tenant`、`hold_reason`、`created_by`、`created_at`）：
   - 在法律保留处于活动状态时，阻止所有删除路径（包括 GC 扫过器）
   - 每个版本独立运作
   - 提供查询 API（`GET /v1/files/{key}/legal-hold`、`PUT ...`）
   - 防止 `LockedUntil` 到期删除——法律保留覆盖 WORM 到期
   - 可能需要**治理模式**，其中法律保留只能由具有 `admin` 权限的用户放置/移除（以保护 eDiscovery 保留免受恶意用户侵害）

3. **批量法律保留：** eDiscovery 需要对许多对象同时施加保留。逐个 PUT 是不可行的。需要将 `POST /v1/admin/legal-hold` 与查询过滤器（前缀、标签、日期范围）结合使用。

#### 预期的架构变更

- 迁移：新增 `legal_holds` 表
- `repository`：新增 `LegalHold` CRUD、修改 `HardDeleteObject` 以检查 legal_holds 表（不仅仅是 metadata 标签）
- `reconcile/lifecycle.go`：新增 `sweepNonCurrent()` 方法；修改 `sweepExpired()` 以跳过 legal holds
- `reconcile/retention.go`：修复 `ListSoftDeletedBefore` 查询（跳过版本化的行）；添加 legal hold 检查
- `service`：新增 `PutLegalHold` / `GetLegalHold` / `ListLegalHolds`
- `api/rest`：新增 `/v1/files/*/legal-hold` 端点
- `api/s3compat`：完善 `x-amz-object-lock-legal-hold` 的 `get`/`put` 支持

#### 对现有系统的影响

**中。** 写路径更改最少（法律保留已存在于 S3 put header 中）。更改主要是数据模型更改（新表）、扫描器修复（保留 GC SQL）和新增 API（法律保留 CRUD）。修复 P0 数据丢失错误是修复代码的 3 行 SQL，加上测试覆盖。

---

## 3. 接口设计建议

### 3.1 `storage.Storage` 的未来演变

当前接口对于 blob CRUD 很简洁，但对于生命周期操作来说太狭窄了。我建议在**不破坏现有实现**的情况下进行渐进式扩展：

**选项 A（我推荐的）：向 Storage 添加可选接口**

```go
// TieredStorage is an optional interface that storage backends can implement
// to support storage-class transitions.
type TieredStorage interface {
    SetTier(ctx context.Context, key string, class string) error
    // Restore initiates a restore from a cold tier. duration is the requested
    // restore period (e.g. 24h for Glacier). Returns a channel that signals
    // when the object is readable.
    Restore(ctx context.Context, key string, duration time.Duration) (<-chan struct{}, error)
}

// MultipartCleaner is an optional interface for orphaned multipart GC.
type MultipartCleaner interface {
    CleanupParts(ctx context.Context, uploadID string) error
    // ListActiveUploads lists upload IDs still tracked by the storage backend.
    ListActiveUploads(ctx context.Context) ([]ActiveUpload, error)
}
```

**为什么是可选接口而不是扩展主接口？**
- 零破坏性更改：现有后端继续编译
- 明确的责任边界：分层和恢复是冷 tier 后端特有的行为
- 在 `FileService` 中的简单类型断言：`if tiered, ok := s.store.(TieredStorage); ok { ... }`

**选项 B：在主接口上的新方法（不推荐）**

将方法添加到 `storage.Storage` 打破了所有四种实现。Go 的接口契约意味着每种新方法都需要在 `local`、`s3`、`oss`、`cos` 以及 `circuitbreaker` 包装器上实现。对于大多数实现将返回 `ErrNotImplemented` 的操作来说，这是不必要的成本。

### 3.2 保留 GC SQL 修复（P0 阻止程序）

这需要立即修复。当前查询：

```sql
WHERE deleted_at IS NOT NULL AND deleted_at < $1
```

必须成为：

```sql
WHERE deleted_at IS NOT NULL AND deleted_at < $1
  AND (version_id IS NULL OR version_id = '')
```

但请注意，这引入了一个新的不变量：版本化的行永远不会被保留 GC 拾起，而是由生命周期扫过器拾起（它以不同的谓词运行）。这是正确的，但需要记录在 `ListSoftDeletedBefore` 的契约中。

### 3.3 法律保留作为头等实体

当前元数据标签方法对于合规工作负载来说不够好。法律保留需要：

```go
type LegalHold struct {
    ObjectID   int64     `json:"object_id"`
    VersionID  string    `json:"version_id,omitempty"` // empty = all versions
    Reason     string    `json:"reason"`
    CreatedBy  string    `json:"created_by"`
    CreatedAt  time.Time `json:"created_at"`
}
```

法律保留检查必须在删除路径（`HardDeleteObject`、`softDeleteObject`、生命周期扫过器、保留扫过器）中检查 `legal_holds` 表，而不仅仅是对象的 `Metadata` 字段。治理模式（仅 admin 可放/取法律保留）是可选的，但强烈推荐。

### 3.4 访问日志路径

我建议不同于当前 repo 接口设计（`WriteAccessLog` 写入 SQL 表）。替代方案：

**选项 A（AWS 方式）：写入后端桶**

```go
// FileService 获得一个 AccessLogSink
type AccessLogSink interface {
    Write(ctx context.Context, entry AccessLogEntry) error
}
```

实现使用 `storage.Storage` 将一个对象写入日志桶（`{bucket}/logs/{YYYY}-{MM}-{DD}/{HH}-{UUID}.log`）。这避免了重复存储（不将日志存储在 SQL 中），与后端选择解耦，并启用 S3 兼容桶的现有日志工具。

**选项 B（SQL 方式）：批量插入到 access_logs 表**

更简单，事务性，适合中等吞吐量（≤1K req/s），但通过日志增长使主元数据数据库膨胀，并且在需要长期日志保留时可能需要较差的查询性能。

我推荐**选项 A**，但注意日志桶的无限递归保护：访问日志 sink 必须检查 `targetBucket != entry.Bucket` 并跳过或使用不发布事件的单独存储路径。

---

## 4. 技术选型

### 4.1 不需要新框架

当前技术栈（Go 标准库 + `chi` 路由 + `modernc.org/sqlite`/`pgx` + AWS SDK v2 + 原生 OSS/COS SDK）适用于该架构。我未看到引入任何新框架（gRPC、Kafka、etcd）的理由。

**需要评估的依赖项：**

- **SQS/SNS HTTP 代理**用于通知调度。Go 标准库 `net/http` 足够——无需新 SDK。但 AWS、阿里云 MNS 和腾讯 CMQ 的认证方案不同（AWS SigV4、阿里云 AccessKey header、腾讯 secret 签名）。这可以通过三个小的认证适配器处理。

- **死信队列**用于通知：当前 Webhook 重试模式（`webhook_failures` 表 + `RetryLoop`）可以复用。不需要外部消息代理。

### 4.2 自建 vs 采购决策

| 功能 | 推荐 | 理由 |
|------|------|------|
| 通知调度 | **自建** | 与当前事件总线集成；现有的重试/死信模式可复用；每个提供商的 SQS/SNS HTTP 代理很简单 |
| 存储分层 | **自建** | StorageRouter 抽象很直接；S3/OSS/COS tier API 已经存在 |
| 多活复制 | **自建** | 基础架构（复制 Worker + 存储抽象）已经存在；需要的是故障检测/提升/验证 |
| 法律保留 | **自建** | 数据模型很简单；外部 legal hold 服务（如 Azure Information Protection）与对象存储架构不匹配 |
| 访问日志分析 | **采购/集成** | 不重新构建日志分析。写入后端桶，使用现有工具（ELK、Loki、S3 Select）进行查询和分析 |

### 4.3 配置设计模式

五个扩展方向中的每一个都引入了新的配置参数。现有模式（环境变量转换为 `*Config` 结构体 + `With*` 函数构建器方法）运行良好并且应该保持。一个值得关注的模式：**`Storage` 后端选择的配置现在是在 `main.go` 中通过 `buildStorageFrom`（一个函数）完成的**——如果需要，这应该被提取为一个更明确的工厂，以支持多后端设置。

---

## 5. 实施路线图

### 5.1 优先级和阶段

```
P0（紧急性：数据丢失或合规违规）
├── 修复 P0：保留 GC × 版本化冲突  [1 周]
│   ├── 修复 ListSoftDeletedBefore SQL   [1 天]
│   ├── 为非当前版本版本添加保留 GC 跳过   [1 天]
│   └── 添加测试覆盖                   [3 天]
│
├── 搁置多部分上传 GC                  [1-2 周]
│   ├── storage.Storage：添加 CleanupParts 接口（可选） [2 天]
│   ├── repository：ListExpiredUploads + DeleteUploadCascade [3 天]
│   ├── reconcile/upload_gc.go          [3 天]
│   ├── 本地实现：在临时区域清理部分   [2 天]
│   └── 配置 + 测试                    [2 天]
│
├── 法律保留作为头等实体                [2 周]
│   ├── 迁移：legal_holds 表            [1 天]
│   ├── repository：LegalHold CRUD      [2 天]
│   ├── service：PutLegalHold / GetLegalHold [2 天]
│   ├── 将所有删除路径更改为检查 legal_holds 表 [3 天]
│   ├── REST + S3 兼容处理程序          [2 天]
│   └── 测试                           [2 天]
│
P1（生产完整性）
├── 访问日志投递                       [2-3 周]
│   ├── AccessLogSink 接口              [1 天]
│   ├── 中间件/处理程序集成              [3 天]
│   ├── 后端桶写入程序实现               [3 天]
│   ├── 递归保护（跳过日志桶事件）        [2 天]
│   ├── 缓冲 + 批量写入 + 轮换          [3 天]
│   └── 测试                           [3 天]
│
├── 通知调度                           [2-3 周]
│   ├── 将 NotificationRule 桥接到事件总线   [2 天]
│   ├── 队列/主题投递程序（AWS SQS + 阿里云 MNS） [5 天]
│   ├── 复用 Webhook 的重试/死信模式      [2 天]
│   ├── 配置 + auth                     [2 天]
│   └── 测试                           [3 天]
│
├── 存储分层（简单：无跨后端 CopyObject） [3-4 周]
│   ├── SetTier 可选接口                 [2 天]
│   ├── LocalStorage：基于 storage class 的目录分离  [3 天]
│   ├── S3：StorageClass 参数            [2 天]
│   ├── reconcile/lifecycle：扫描 → 转换   [3 天]
│   ├── 恢复 API                        [2 天]
│   └── 测试                           [5 天]
│
P2（差异化 / 可扩展性）
├── 多活复制与故障切换（仅手动 promote） [6-8 周]
│   ├── 一致性检查（etag + size 验证）    [3 天]
│   ├── 集群健康检测器                    [3 天]
│   ├── 提升 API 和切换状态机             [5 天]
│   ├── 读一致性写入后读标记              [5 天]
│   ├── SSE 密钥跨 region 同步            [5 天]
│   ├── 裂脑预防（fencing）              [5 天]
│   ├── 集成测试（故障注入）              [1 周]
│   └── 文档 + 运营手册                  [3 天]
│
├── SQL 级标签过滤                      [1 周]
├── StorageClassGauge 租户维度           [2 天]
└── Webhook 速率限制器                  [3 天]
```

### 5.2 里程碑

| 里程碑 | 交付物 | 预计时间 |
|--------|--------|----------|
| **M1：安全基石** | P0 修复（保留 GC + 上传 GC + 法律保留） | 4 周 |
| **M2：合规完整性** | 访问日志 + 通知调度 | +3-4 周 |
| **M3：成本优化** | 存储分层转换 | +3-4 周 |
| **M4：高可用性** | 多活复制 + 故障切换 | +6-8 周 |

### 5.3 风险与缓解措施

| 风险 | 可能性 | 影响 | 缓解措施 |
|------|--------|------|----------|
| 保留 GC 修复为版本化行创建新路径以被保留扫过器删除 | 中 | 高 | 添加显式版本控制生命周期扫过器，具有自己的租约和配置；添加集成测试 |
| 通知调度中的 AWS API 速率限制 | 低 | 中 | 采用指数退避 + 通过 Webhook 模式进行去重；从第一天开始监控 `sqs_send_message` 限制 |
| 存储分层增加了写路径延迟 | 中 | 中 | 保持写路径同步用于热存储；异步用于冷存储；通过可观测性进行监控 |
| 多活复制中的裂脑 | 高（如果自动故障切换） | 严重 | 对于 M4 仅手动 promote；为解雇建立 fencing 令牌机制；`REPLICATION_ROLE` 永不自动切换 |
| 访问日志的递归日志写入 | 高 | 高（如果无保护） | `sourceBucket != targetBucket` 检查，且对于通知是硬性要求；添加集成测试 |

---

## 6. 其他建议

### 6.1 运营手册就绪度

五个方向中的每一个都引入了需要运营知识的新配置参数。我建议创建一个 `docs/operations/` 目录，其中包含：

- `docs/operations/lifecycle-management.md` — 生命周期策略配置、storage class 映射、成本跟踪
- `docs/operations/disaster-recovery.md` — 复制设置、promote 程序、恢复时间目标/恢复点目标声明
- `docs/operations/compliance.md` — 法律保留、访问日志、通知、审计追踪
- `docs/operations/troubleshooting.md` — 常见问题排查指南（上传 GC 泄漏、通知失败等）

### 6.2 不变量注册表

根据 AGENTS.md 中定义的模型，这些新功能中的每一个都有必须记录和测试的不变量。示例：

| 新不变量 | 应用于 |
|----------|---------|
| 保留扫过器永远不能删除 version_id 不为 null 的行 | 方向五 |
| 有法律保留的对象不能被任何删除路径删除（包括生命周期扫过器） | 方向五 |
| 访问日志写入一定不能生成事件 | 方向三 |
| 存储分层转换必须保留 etag | 方向一 |
| 故障切换必须只在手动确认后发生（M4） | 方向四 |

### 6.3 关于分析报告的说明

v21 分析正确地识别了五个扩展方向，并验证了 31/32 的代码锚点。Presign URL 方法绑定错误（分析声称没有——但 `signLocal` 确实将 HTTP 方法编码到 HMAC 中）是唯一的错误，这是一个小错误。方向五的版本控制冲突发现是**在现有的文档系列中首次发现**，单独证明分析有价值。

我支持分析的优先级优先级（P0：方向五 + 方向二；P1：方向三 + 方向一；P2：方向四），原因在上面的实施路线图中给出。**但方向五中的 P0 数据丢失错误是优先于所有其他事项的阻止程序——应该在本周修复，而不是“下一个 Sprint”。**
