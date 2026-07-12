# 架构分析报告：AeroVault 企业级盲区评估

---

## 1. 架构评估

### 1.1 当前架构的优势

AeroVault 的架构设计有几个非常扎实的决策值得肯定：

**优势一：四协议共享单一服务层（`FileService`）**

这是迄今为止最正确的架构决策。四个协议适配器（REST、S3、WebDAV、MCP）都是薄封装层，核心业务逻辑集中在 `internal/service` 中。这意味着：
- 写一个对象 = 一次元数据写入 + 一次 blob 写入，不论从哪个入口进入
- 新协议适配器的增量成本很低（只需要适配认证+参数映射）
- 跨协议一致性天然保证（存储 key 生成策略、版本控制逻辑、配额校验全部单一实现）

**优势二：明确的 layered architecture，依赖方向单一**

```
Protocol → Middleware → FileService → {Storage, Repository}
```

没有循环依赖，没有业务逻辑泄漏到 handler 层（验证报告确认 handler 不自挂中间件链 —— 这是正确的设计选择，方便 handler 的隔离测试）。

**优势三：`Storage` 接口体积小、契约清晰**

`storage.Storage` 仅包含 `Put/Get/Stat/Delete/List/Presign*` + 分片上传操作。接口语义简洁，新后端实现成本低。同时配有 `contract_test.go` 作为实现契约的验证套件 — 这是 Go 生态中典型的"interface + contract test"模式，值得保持。

**优势四：可插拔设计始终贯彻**

storage（local/s3/oss/cos × SSE encrypt）× repository（sqlite/postgres）× vector index（in-memory/pgvector/qdrant）全部 flag-gated，默认基线（SQLite + local FS + AI off）零网络零依赖，CI gate 可完全离线执行。

### 1.2 当前架构的局限性

**局限一：单存储后端是架构演进的最大瓶颈**

`buildStorage` 在 `main.go:402` 返回单一 `storage.Storage` 实例。全部租户、全部存储类共享一个后端。这限制了：
- **数据主权：** 无法对欧盟租户使用 `eu-s3`、中国租户使用 `cn-oss`
- **成本分层：** 无法让热数据在本地 NVMe、温数据在 S3、冷数据在 Glacier
- **故障隔离：** 一个后端故障影响所有租户

`storageKey(tenant, bucket, key)` 的 key 生成策略是 `path.Join`，没有后端路由组件介入。这意味着添加后端路由不是简单的"增加一个 wrapper"——它会影响存储 key 的结构、迁移逻辑、GC 扫描策略。这是一项侵入式变更。

**局限二：授权模型在四个协议间碎片化**

验证报告揭示了一个比原文档更微妙的问题：

- REST handler 的 `checkBucketPolicy` **确实存在**（位于 `rest/handler.go:46`），但它硬编码了 `service.DefaultBucket` 而非从请求路径中提取 bucket 名
- S3 handler 的 `checkBucketPolicy` 签名包含 bucket 参数，是正确的
- WebDAV 完全无授权
- MCP 只有 tenant 校验，无 Policy 评估

结果：两个协议有 Policy 检查但评估行为不一致（REST 侧对非 default bucket 使用错误 bucket 名称），两个协议完全没有。这种"半实现"状态比"全无"更危险 —— 安全审计会认为你有控制措施，但措施存在逻辑缺陷。

**局限三：审计日志与业务数据同库，不可独立验证**

`RecordAudit` 直接写入 `audit_log` 表（与 objects/versions/tags 同 DB）。无哈希链、无数字签名、无独立导出机制。**这不仅是合规缺口（SOC2/HIPAA/PCI DSS），更是安全架构的根本缺陷**：任何拥有 admin DB 访问权限的内部人员都可以 `UPDATE audit_log SET detail='...' WHERE ...` 来掩盖操作。

从 Trusted Computing Base（TCB）的角度来看，当前审计日志位于 TCB 内部而非独立于 TCB 之外 —— 这与"审计"的语义相悖。

**局限四：参数系统完全静态，无反馈回路**

80+ 个环境变量全部在启动时加载，运行时不可变更。与此同时系统拥有 15+ 个 OTel 指标（`indexer_chunk_duration_ms`、`job_queue_depth`、`search_cache_hit_ratio` 等），但这些指标只用于"看"，不驱动任何自动化行为。

这是典型的"数据丰富但行动贫乏"模式 —— 指标的成本（采集、存储、告警配置）已经支付，但价值只实现了一半（可观测），另一半（自动化）未实现。

### 1.3 架构债务与技术债

| 债务类型 | 位置 | 风险等级 | 说明 |
|---------|------|---------|------|
| **复制粘贴的 Policy 评估** | `rest/handler.go` ↔ `s3compat/handler.go` | 中 | 两个 handler 各自实现了几乎相同的 `checkBucketPolicy`，签名不同导致行为分歧。应提取到 `auth` 包或 `service` 层 |
| **`DefaultBucket` 硬编码** | `rest/handler.go` 中多处 | 高 | REST handler 通过路径 `/v1/files/*key` 无法提取 bucket，人为限制为单桶。如果要支持多桶，REST 路由设计需要调整（例如改为 `/v1/buckets/{bucket}/files/*key`） |
| **WebDAV 零认证** | `internal/api/webdav/` | 高 | 在四协议统一架构中，一个入口完全无身份验证和授权，等于开了一个后门 |
| **MCP 无操作级授权** | `internal/mcp/server.go` | 中 | 虽然 stdio 模式通常假定本地可信，但 HTTP 模式暴露了相同的工具集但没有授权校验 |
| **审计无哈希链** | `internal/repository/audit.go` | 中 | 架构选择的债务——最初设计时选择了最简单的实现，现在需要回溯添加防篡改机制 |
| **迁移文件不可合并** | `migrations/{sqlite,postgres}/` | 低 | 每次 schema 变更需要双份迁移文件，长期维护成本高。但这是 "I2 硬性不变量"，不可改变 |

---

## 2. 扩展方向

### 方向 A：统一授权引擎（方向四的增强版）

**业务价值：★★★★★**

安全基线——四协议的授权一致性是使用四个协议共享后端的架构前提。当前的不一致状态（两个 handler 有 Policy 检查但行为不同、两个协议完全无检查）是一个安全漏洞，而不是产品缺口。

**技术价值：★★★★☆**

提取 `checkBucketPolicy` 到 FileService 层的 `Authorizer` 接口后，不仅消除碎片化，还打开了更多可能性：
- 支持跨协议一致的 Condition Keys 评估
- 支持更丰富的 Policy 语法（基于角色的访问控制 RBAC）
- 支持租户级 Policy 继承（全局 Policy + 桶 Policy 叠加）
- 审计日志中记录 `protocol` 字段，便于跨协议行为分析

**核心挑战：**

1. **调用者身份的跨协议传递：** 当前 context 携带 tenant 和 request ID，但不携带 caller ARN/IP/role 信息，也不携带 protocol 标识。需要定义 `AuthContext` 结构体并在 middleware 中填充。

2. **WebDAV 的认证适配：** WebDAV 协议在标准 HTTP 层面没有原生的 Bearer Token 语义。需要设计适配方案（`Authorization: Bearer` header 支持 + 可选的 `?auth_token=` 签名 URL 参数）。

3. **Policy 缓存一致性：** 桶 Policy 更新后，正在进行的请求可能用到旧的缓存 Policy。需要一个轻量级的失效策略（基于 `policy_updated_at` 时间戳）。

4. **现有 REST handler 路由的修改：** 当前 REST handler 的路径中包含 bucket 信息吗？`/v1/files/*key` 没有 bucket 参数，Policy 评估时无法确定目标桶。这需要在 REST API 层面增加桶指定能力，或者设计 REST 侧如何映射文件的所属桶。

**架构变更：**

```
新增 internal/authz/ 包：
  Authorizer 接口 {
    Check(ctx, action string, req AccessRequest) error
  }
  AccessRequest {
    Tenant, Bucket, Key string
    CallerARN           string
    CallerIP            net.IP
    Protocol            string  // "rest"|"s3"|"webdav"|"mcp"
    // 可选的额外条件
    Conditions          map[string]string
  }

现有变更：
  internal/service/file.go — FileService 新增 Authorizer 字段
  internal/auth/policy.go — ParsePolicy/Allowed 提升为通用评估器
  四个 handler — 移除各自独立的 checkBucketPolicy，统一调用 FileService 方法
  internal/middleware/ — 新增 AuthContext 填充中间件（在 Auth 中间件之后运行）
```

**对现有系统的影响：**

- **向后兼容性：** 对 S3 handler 无行为变化（Policy 评估逻辑相同，只是位置从 handler 移到 FileService）。对 REST handler 有行为变化 —— 原本 `DefaultBucket` 硬编码的 Policy 评估会修正为真实桶名，这可能是 bugfix 而非 breaking change。
- **测试影响：** 需要为 `Authorizer` 接口编写 mock，替换 handler 级别的 Policy 测试为 service 级别的集成测试。
- **性能影响：** Policy JSON 解析是主要开销。建议：按 `(tenant, bucket)` 缓存已解析的 Policy 语句，LRU 淘汰。

### 方向 B：存储后端路由层（方向一的演进）

**业务价值：★★★★★**

多后端路由是数据主权合规（GDPR、中国数据安全法、CCPA）的刚性需求。对于计划进入企业市场的 SaaS 产品，这是一个准入特性而非可选特性。

**技术价值：★★★★★**

从架构角度看，单后端是当前最深的"抽象泄漏"——`file_crud.go` 中的 `storageKey(tenant,bucket,key)` 拼接了一个绝对路径，但该路径在 S3 后端和 local 后端中有不同的语义（S3 是对象 key，local 是文件路径）。引入路由层正好可以"吸收"这种差异，让 `FileService` 完全透明于后端分布。

**核心挑战：**

1. **存储 key 的跨后端迁移：** 如果一个对象从 local 后端被迁移到 s3 后端，`storageKey` 如何变化？如果 `storageKey` 包含路径信息（如 `local` 的 `/var/objects/tenant/bucket/key`），迁移后还应保留该 key 还是更新为新后端的 key？

   **权衡：** 选项 A — `storageKey` 仅包含 `tenant/bucket/key` 三元组（已如此），后端无关。选项 B — `storageKey` 包含后端标识符用于路由。当前代码采用选项 A，这是正确的选择 —— key 不应编码路由信息。

   但这里有一个隐藏问题：当前 main.go 中的 `buildStorage` 是单实例，所以 key 直接传递给那个唯一的后端。引入路由层后，需要从 key 反推后端。设计建议：`StorageRouter.Put(ctx, tenant, bucket, key, ...)` 中根据 `(tenant, bucket, storage_class)` 路由，而不是从 key 中解析。

2. **ListObjects 的跨后端合并：** S3 的 `ListObjectsV2` 需要列出桶内所有对象。如果桶的数据分散在多个后端，需要从每个后端列出并合并结果。这增加延迟且可能产生分页问题。

   **缓解方案：** metadata DB 已经持有全局对象视图。`ListObjects` 可以直接查询 repository 元数据，无需触及 storage 后端。实际上 current 代码可能就是如此（repository 查询 + storage stat 验证）。路由层对 List 操作的主要影响不是数据获取，而是确保统计准确。

3. **在线迁移的数据一致性：** 方向三和方向一紧密耦合。如果没有方向一的多后端能力，方向三的分层迁移不可能实现。反过来，方向一的实现必须考虑迁移场景 —— 迁移期间的读请求可能需要从新旧两个后端之一返回数据。

   **设计建议：** 引入 `storage_key` 到后端的显式映射存储在 repository 中。对象的 `storage_key` 字段指向后端的逻辑名称，`BackendRegistry` 持有 `map[string]Storage`。这样可以实现"原地迁移"——只更新 repository 中的后端映射，无需改变存储 key。

4. **配置模型复杂度：** 当前 `.env.example` 中的 `STORAGE_BACKEND=local` 单一配置。多后端后需要：
   ```
   BACKENDS=eu-s3,cn-oss,default-local
   BACKEND_EU_S3_ENDPOINT=...
   BACKEND_CN_OSS_ENDPOINT=...
   BACKEND_DEFAULT_LOCAL_ROOT=./var/objects

   TENANT_ROUTING_RULES=eu-*:eu-s3,cn-*:cn-oss,*:default-local
   ```
   这显著增加了配置的复杂度和出错概率。

   **缓解方案：** 引入配置验证工具（启动时自动检查每个后端实际可达性），并提供一个简化的配置模式 —— 单一后端时配置复杂度不变，多后端时才需提供完整配置。

**架构变更：**

```
新增 internal/storage/router.go:
  Router 实现 storage.Storage 接口（适配器模式）
  内部持有 map[string]storage.Storage + []RouteRule

  RouteRule {
    TenantPattern glob.Pattern  // "eu-*", "cn-*"
    BucketPattern glob.Pattern  // "*" 匹配全部
    StorageClass  string         // "STANDARD" | ""匹配全部
    BackendID     string         // "eu-s3"
  }

配置变更：
  config.go 新增 MultiBackend 结构体
  buildStorage 改为 buildStorageRouter
```

**对现有系统的影响：**

- **向后兼容性：** 单一后端场景下，`Router` 退化为直接转发，行为零变化。配置兼容 —— 保持 `STORAGE_BACKEND=local` 语法仍有效（自动填充为单路由规则）。
- **测试影响：** `storage.Router` 需要一套新的行为测试（路由匹配优先级、降级行为、无效路由的处理）。现有的 `contract_test.go` 保持不变。
- **性能影响：** 路由匹配是 `O(#rules)` 查找。规则数量通常 < 100，可以忽略不计。但如果需要更高的路由性能，可以编译为 trie 树。

### 方向 C：不可变审计轨迹（方向二的增强版）

**业务价值：★★★★☆**

SOC2/HIPAA/PCI DSS/FedRAMP 均要求审计日志防篡改。对于以企业客户为目标用户的 AeroVault，这是销售的"成本准入"特性 —— 不能证明审计的完整性，就不在企业采购清单上。

**技术价值：★★★★☆**

从架构层面，不可变审计引入了一个重要的概念 —— **系统的独立可验证性（Independent Verifiability）**。审计日志不再依赖于"信任系统 DB 管理员不会篡改"，而是通过密码学哈希链使得任何第三方都可以独立验证审计日志的完整性。这改变了安全模型的基础假设。

**核心挑战：**

1. **哈希链的并发写入：** 如果两条审计记录在同一毫秒写入，`PrevHash` 链可能分叉。需要原子性地获取前一条记录的哈希。

   **方案对比：**
   - 选项 A（串行化写入）：在 DB 层面使用 `SELECT ... FOR UPDATE` 或 `INSERT ... RETURNING` 保证顺序。适合 SQLite/Postgres，但增加写入延迟。
   - 选项 B（隐式链）：不存储 `PrevHash`，通过 `prev_id` 外键引用 + 对 `(prev_id + payload)` 做哈希。验证时需要遍历链，但写入冲突概率低。
   - 选项 C（UUID v7 排序 + 写入后补偿）：先写入，再异步补偿 `PrevHash` 字段。适用于对写入延迟敏感的场景。
   
   **推荐：** 选项 B。SQL 层面的 `prev_id` 外键可以利用 DB 自身的 ACID 保证顺序。验证时从最新记录开始反向遍历并计算哈希链。

2. **双写架构的外部审计存储：** 如果审计日志写入外部存储（S3 append-only、syslog、HTTP API），该写入不应阻塞主请求。

   **设计建议：** 同步写入本地 DB（带哈希链），异步写入外部 sink。本地 DB 是"源 of truth"，外部 sink 是"备份 + SIEM 消费"。本地 DB 写入失败时仍应拒绝主操作（审计必达），而外部 sink 失败时只记录告警，不阻塞。

3. **审计日志的长期存储成本：** 审计日志无限增长会带来存储成本和查询性能下降。

   **方案：** 引入 `AUDIT_RETENTION_DAYS` 配置。保留期前的数据需要先导出（加密签名后写入 S3/归档存储）再从主 DB 删除。但注意：不可变审计的语义要求删除前的导出必须包含哈希链校验 —— 确保导出的数据可以独立验证。

4. **跨区域审计汇聚：** 多区域部署时需要将各区域的审计日志汇聚到中心审计存储。跨区域延迟和网络故障可能影响汇聚的实时性。需要接受"最终一致性"—— 区域审计日志在本地立即可查，中心汇聚可能有分钟级延迟。

**架构变更：**

```
新增 internal/audit/ 包：
  Service {
    Record(ctx, entry AuditEntry) error    // 同步写入本地 DB + 哈希链
    Verify(ctx, fromID, toID) (*Report, error)  // 遍历链验证完整性
    Export(ctx, filter, sink) error        // 导出到外部存储
  }

  AuditEntry 结构体新增字段：
    PrevHash    string  // SHA-256 of previous entry's (prev_hash + payload)
    PayloadHash string  // SHA-256 of (timestamp + actor + action + detail)

新增 custom type：
  AuditSink 接口 {
    Write(ctx, entries []AuditEntry) error
    Close() error
  }
  实现：S3AuditSink, FileAuditSink, HTTPAuditSink

迁移文件：
  ALTER TABLE audit_log ADD COLUMN prev_hash TEXT;
  ALTER TABLE audit_log ADD COLUMN payload_hash TEXT;
  ALTER TABLE audit_log ADD COLUMN signature TEXT;

配置新增：
  AUDIT_RETENTION_DAYS=365
  AUDIT_EXTERNAL_SINK=s3://audit-bucket/
  AUDIT_HMAC_KEY=  // 用于 signing audit entries
```

**对现有系统的影响：**

- **向后兼容性：** 新增字段可为空（nullable），现有审计行不受影响。验证工具对旧格式行（无 prev_hash）标记为"unverifiable"但不阻断。新增行自动填充哈希链。
- **迁移影响：** `RecordAudit` 调用方无需修改 —— 新 `audit.Service.Record` 实现透明地追加哈希链。admin handler 的 `ListAudit` 查询逻辑不变，只需在返回时附加 `verifiable` 状态。
- **性能影响：** 哈希计算开销可以忽略（SHA-256 在微秒级）。外部 sink 写入是异步的，不阻塞主请求。

### 方向 D：热参数调优框架（方向五的最小可行版本）

**业务价值：★★★☆☆**

降低运维调优成本。对中小型部署（无需专职 SRE 的团队）最有价值。

**技术价值：★★★☆☆**

从架构角度，热参数调优引入了一个新的架构模式 —— **控制回路（Control Loop）**。这与现有架构中的"请求-响应"模式完全不同，需要精心设计以避免震荡和过度耦合。

**核心挑战：**

1. **参数的热更新接口设计：** 每个可调优组件需要实现原子化的参数替换。Go 的 `sync/atomic` 或 `sync.RWMutex` 保护可变参数。

   **设计模式建议：** 每个 `Tunable` 组件暴露一个 `Update(cfg TunableConfig) error` 方法，内部实现"验证 → 快照旧配置 → 原子替换 → 回滚能力"。配置变更必须可审计、可回滚。

2. **控制回路的防震荡：** 如果 chunk window 每 60 秒调整一次，可能在高负载和低负载之间来回摆动（震荡）。

   **缓解方案：** 
   - 引入死区（dead zone）：偏差在阈值内时不触发调整
   - 增量限制：每次调整不得超过 `MaxAdjustment`（如 ±10%）
   - 冷却期：调整后 N 分钟内不再调整同参数
   - 建议使用类似 PID 控制器中的"积分分离"策略 —— 偏差大时快速调整，偏差小时缓慢收敛

3. **参数间的耦合：** 增加 chunk window 会增大内存使用 → 可能触发缓存驱逐 → 降低缓存命中率。改变一个参数可能影响其他参数的最优值。

   **缓解方案：** 初期只对独立性强的参数进行自动化（worker 池大小、缓存 TTL），对耦合度高的参数（chunk window + chunk overlap + embed batch size）保持手动调优 + 告警推荐。

**架构变更：**

```
新增 internal/autotune/ 包：
  Tunable 接口 {
    Name() string
    Value() TunableValue
    Update(ctx, v TunableValue) error
  }

  Controller {
    tunables   map[string]Tunable
    policies   map[string]TuningPolicy
    loop       func(ctx)  // 主控制循环
  }

  TuningPolicy {
    Metric       string   // 指标名称，如 "indexer_chunk_duration_p95"
    TargetMin    float64
    TargetMax    float64
    MinValue     TunableValue  // 参数值下限
    MaxValue     TunableValue  // 参数值上限
    AdjustmentFn func(currentMetric, target, currentValue) TunableValue
  }
```

**建议初始范围：**

| 参数 | 度量指标 | 目标 | 风险 |
|------|---------|------|------|
| Chunk Window | `indexer_chunk_duration_p95` | ≤ 500ms | 中 — 影响检索质量 |
| Worker 池大小 | `job_queue_depth_avg` | ≤ 10 | 低 — 资源限制内 |
| Search Cache TTL | `search_cache_hit_ratio` | ≥ 0.5 | 低 — TTL 变化无安全影响 |
| 全局 RPS 限流 | `storage_latency_p99` | ≤ 200ms | 低 — 仅降级不过载 |

### 方向 E：Server-Side Copy 与跨协议数据移动

**业务价值：★★★★☆**

当前 S3 协议支持 `x-amz-copy-source`，但仅限于同一集群内。扩展为真正的服务端复制（Server-Side Copy）加上跨协议一致的对象复制语义，可以支持：
- 同一桶内、不同桶间的对象复制
- 跨后端的异步复制（方向一的分支）
- 通过 REST/MCP/WebDAV 触发服务端复制

**技术价值：★★★★☆**

当前 `copyObject` 在 `s3compat/handler.go` 中是面向 S3 协议的实现（从 S3 请求参数解析 source 路径）。提取为 `FileService.Copy` 方法后，所有协议都可以触发复制操作。

核心架构上的好处：复制操作可以在服务端完成（数据不经过客户端），这对大文件（GB+）和跨后端的场景至关重要。

**核心挑战：**

1. **跨后端复制的数据流路径：**
   - 同一后端：后端原生支持 copy（S3 的 `CopyObject` API），零数据移动
   - 不同后端：需要下载 → 上传，期间需要流式处理（buffer 管理 + 进度追踪）

2. **元数据处理的语义差异：** S3 协议允许 `x-amz-metadata-directive: COPY|REPLACE`，REST 协议可能没有相同的语义。需要映射到统一的 `CopyOptions`。

3. **版本控制的交互：** 如果源桶和目标桶的版本控制状态不同，复制行为如何？S3 规范有明确定义（复制时创建新版本与目标桶状态一致），需要确保跨协议一致。

---

## 3. 接口设计建议

### 3.1 核心原则

**原则一：接口抽取向"被调用方"而非"调用方"**

当前 `storage.Storage` 接口的设计正确 —— 它基于"backend 提供的能力"而非"FileService 需要的形状"。这意味着当引入新后端时，只需实现接口，无需修改调用方。

新引入的 `Authorizer` 接口也应遵循相同原则：

```
// Good — 基于 authorizer 能力定义
type Authorizer interface {
    Check(ctx context.Context, req AccessRequest) error
}

// Not good — 基于调用方传递的协议特定的信息
type Authorizer interface {
    CheckFromREST(ctx, tenant, action string) error
    CheckFromS3(ctx, bucket, action, callerARN string) error
}
```

**原则二：抽象泄漏的边界可观测**

每个新的抽象层（`Router`、`Authorizer`、`AuditService`）都应该暴露钩子，让调用方可以观测其内部行为。对于 `Router`，当一个请求的路由匹配失败或降级时，应该有日志 + 指标。对于 `Authorizer`，每次拒绝决策应记录审计日志。

**原则三：新接口的默认实现 = "不做额外事情"**

保持 opt-in 安全默认（I5）：
- `Authorizer` 的默认实现放行所有请求（`noopAuthorizer`）—— 与当前行为一致
- `Router` 的默认实现转发到单一后端（`singleBackendRouter`）—— 与当前行为一致
- `AuditService` 的默认实现写入 DB 但不加哈希链（降级模式）

这确保了引入新抽象层时，现有行为零变化。

### 3.2 是否需要新的抽象层

**需要：`Authorizer` 接口（在 `internal/service` 层）**

当前的 `checkBucketPolicy` 分散在两个 handler 中且签名不一致。提取为 `FileService` 级别的 `Authorizer` 是消除碎片化的正确方式。这本质上是将"授权决策"从协议适配层上移到服务层 —— 这正是分层架构的应有之意。

**需要：`StorageRouter`（或在 `internal/storage` 内的路由 wrapper）**

方向一和方向三的前置条件。路由层实现了 `storage.Storage` 接口，因此对 `FileService` 透明。

**需要：`AuditService`（新的 `internal/audit/` 包）**

当前的 `RecordAudit` 是 `repository` 包中的一个方法。将审计职责提取为独立服务，可以：
- 独立演化（哈希链、签名、双写）
- 不污染 repository 接口
- 独立测试（使用 mock audit sink）

**不需要：额外的中间件层**

当前的中间件链顺序固定（I4），并且结构已完成。统一授权不通过新增中间件实现 —— 如果通过中间件检查 Policy，则 FileService 内部的方法（如被 worker 或 MCP 直接调用时）会绕过检查。授权必须在 FileService 层完成。

### 3.3 向后兼容性策略

**配置兼容性：**
- `STORAGE_BACKEND=local` 在新旧架构中都工作。新架构检测到单一后端时，自动使用 `singleBackendRouter`
- `AUDIT_HMAC_KEY` 为空时，审计写入旧格式（无签名行）。不为空时追加哈希链和签名
- 环境变量形式不变，新增配置使用相同的前缀惯例（`AUDIT_*`、`BACKEND_*`、`AUTHZ_*`）

**API 兼容性：**
- REST/S3/WebDAV/MCP 的现有 API 路径不变
- 新特性使用新端点（如 `POST /v1/admin/audit/verify`、`POST /v1/admin/audit/export`）
- Policy 评估行为变更（REST handler 从 `DefaultBucket` 修正为真实桶名）需要发 changelog 标注为 bugfix

**DB schema 兼容性：**
- 新增列使用 `ALTER TABLE ... ADD COLUMN ... DEFAULT NULL`，不破坏现有行
- 新迁移文件使用递增编号，不修改已有迁移

---

## 4. 技术选型

### 4.1 是否需要引入新的技术栈

| 扩展方向 | 需引入 | 推荐方案 | 理由 |
|---------|--------|---------|------|
| 统一授权 | 否 | 纯 Go 实现 | `auth/policy.go` 已有 Policy 解析器，只需提取和增强 |
| 存储路由 | 否 | 纯 Go 实现 | 路由逻辑是配置匹配 + 转发，不需要外部依赖 |
| 不可变审计 | 否 | 纯 Go `crypto/sha256` + `crypto/hmac` | Go 标准库全部覆盖。无外部依赖 |
| 热参数调优 | 否 | 纯 Go 实现 | 控制回路是逻辑密集型，非数据密集型 |
| 跨后端复制 | **可能** | 引入流式 buffer | 大文件跨后端复制需要流式 buffer 管理，但 Go 标准库的 `io` 包已足够 |

**结论：不需要引入新的技术栈或框架。** Go 标准库对以上所有场景都有足够的支持。

### 4.2 第三方依赖的评估标准

如果未来需要引入外部依赖，应遵循以下标准：

| 标准 | 阈值 | 否决条件 |
|------|------|---------|
| 许可证兼容 | Apache 2.0 / MIT / BSD | GPL/AGPL（除非静态链接例外） |
| 依赖深度 | ≤ 3 层传递 | 超过 5 层传递依赖 → 重新评估 |
| CGO 要求 | 无 | 必须 CGO → 否决（破坏纯 Go 构建和交叉编译） |
| 活动维护 | 最近 6 个月有提交 | 超过 1 年无更新 → 否决 |
| Go 版本兼容 | 匹配 go.mod 的 Go 1.25 | 要求更低版本且有已知兼容性问题 → 暂缓 |
| 标准库替代 | 有直接标准库实现 | 如果标准库可替代 → 不要引入依赖 |

当前项目的 `go.mod` 依赖列表已验证是极简的（参见 AGENTS.md 的 I6 约束 "Stdlib 优先"），这一策略应继续保持。

### 4.3 自建 vs 采购/集成的决策依据

对于审计外部存储导出和 SIEM 集成：

| 场景 | 决策 | 理由 |
|------|------|------|
| 审计日志导出到 S3 | **自建** | 代码量小（< 200 行），只需实现 `AuditSink` 接口的 S3 variant 使用已有的 `internal/storage/s3.go` |
| 审计日志导出到 syslog | **自建** | Go 标准库 `log/syslog` 支持 |
| 审计日志导出到 Splunk/ELK | **自建** | 输出 JSON Lines 格式文件，由对方平台消费。AeroVault 不负责对方平台的集成 |
| 配置中心（etcd/consul 后端） | **暂不引入** | 当前参数数量（80+）远未到需要分布式配置中心的规模。热参数更新通过 admin API + 本地内存变更即可 |

---

## 5. 实施路线图

### 5.1 优先级矩阵

评估维度：**影响（安全/合规/成本）** vs **实施复杂度**

```
                    影响
              低 ←──────────→ 高
              ┌──────────────────────────────┐
实施复杂度 高 │                               │ 方向B: 存储路由 ★★★★
  ↑          │                               │  (数据主权, 架构基础)
  中         │ 方向D: 热调优 ★★★              │ 方向C: 不可变审计 ★★★★
             │  (运维效率, 独立可推进)         │  (合规准入)
  低         │                               │ 方向A: 统一授权 ★★★★★
              │                               │  (安全基线, 最小工作量)
              └──────────────────────────────┘
```

### 5.2 阶段划分

**Phase 0 — 修复当前架构碎片（1 周）**

在开始任何新抽象层之前，先解决验证报告发现的具体问题：

| 任务 | 预计时间 | 交付物 |
|------|---------|--------|
| 修正 REST handler 的 `checkBucketPolicy` —— 从 `DefaultBucket` 改为从请求上下文提取真实桶名 | 0.5 天 | 修复 + test |
| 为 WebDAV 增加基础认证中间件（与现有 `AUTH_*` 体系一致） | 1 天 | 认证兼容性 + test |
| 为 MCP HTTP 模式增加 scope 校验（与现有 REST scope 体系一致） | 1 天 | 授权 + test |
| 补充验证报告发现的缺失测试覆盖 | 0.5 天 | 测试用例 |

**Phase 1 — 统一授权引擎（2 周）**

| 里程碑 | 产出 | 验收标准 |
|-------|------|---------|
| M1: `Authorizer` 接口 + `FileService` 集成 | `Authorizer.Check` 在每次业务操作前调用 | 所有四个协议 handler 移除 `checkBucketPolicy` 的独立实现，统一经过 `FileService` |
| M2: WebDAV 认证适配 | 支持 `Authorization: Bearer` 和 `?auth_token=` 参数 | WebDAV 对受 Policy 限制的桶返回 403 |
| M3: MCP 工具级授权 | 每个工具调用经过 `Authorizer.Check` | `list_files` 只列出有读权限的桶 |
| M4: 集成测试套件 | 跨协议的 Policy 评估一致性验证 | 同一 Policy 规则通过四个协议返回相同结果 |

**Phase 2 — 不可变审计轨迹（2 周，可与 Phase 1 并行）**

| 里程碑 | 产出 | 验收标准 |
|-------|------|---------|
| M1: 哈希链写入 | `RecordAudit` 追加 `prev_hash` 和 `payload_hash` | 新写入的审计记录形成可验证链 |
| M2: 审计验证工具 | `audit.Verify` 遍历链返回完整性报告 | 篡改一行后验证报告检测到断裂 |
| M3: 迁移文件 | `audit_log` 新增 `prev_hash`/`signature`/`payload_hash` 列 | 旧行可空，迁移成功 |
| M4: 外部导出 | `GET /v1/admin/audit/export` + S3 sink | 审计数据可导出为 JSON Lines 格式 |

**Phase 3 — 存储后端路由（3-4 周）**

| 里程碑 | 产出 | 验收标准 |
|-------|------|---------|
| M1: 多后端配置模型 | 配置支持多个后端声明 + 路由规则 | 单一后端配置完全向后兼容 |
| M2: `StorageRouter` 实现 | Router 实现 `storage.Storage` 接口 | contract_test 对 Router 变体全部通过 |
| M3: 租户-后端映射 API | `PUT /v1/admin/tenants/{id}/storage` | 更新路由规则后新请求路由到新后端 |
| M4: 存量数据兼容 | 已有数据的存储 key 无需修改 | 路由层对无匹配规则的 key 走 default backend |

**Phase 4 — 分层迁移（3 周，依赖 Phase 3）**

| 里程碑 | 产出 | 验收标准 |
|-------|------|---------|
| M1: Lifecycle transition 动作 | `transition_to_ia` / `transition_to_glacier` 动作实现 | 对象在到期后自动迁移到目标后端 |
| M2: 迁移作业框架 | `jobs` 表 + worker 处理迁移任务 | 失败可重试，进度可查询 |
| M3: 读取时回退 | 正在迁移的对象 GET 请求自动路由到原后端 | 迁移期间读请求 100% 成功 |

**Phase 5 — 热调优最小闭环（持续进行，可独立推进）**

| 里程碑 | 产出 | 验收标准 |
|-------|------|---------|
| M1: `Tunable` 接口 + `Controller` 框架 | `internal/autotune/` 包 | 控制回路可启停，可配置 |
| M2: Worker 池自适应 | `JOBS_WORKERS` 根据 `job_queue_depth` 自动调整 | 高峰期 worker 数增加，低谷期减少 |
| M3: Search Cache TTL 自适应 | TTL 根据 `search_cache_hit_ratio` 调整 | 命中率 < 阈值时 TTL 自动减小 |

### 5.3 风险点和缓解策略

| 风险 | 可能性 | 影响 | 缓解策略 |
|------|--------|------|---------|
| **Phase 1（统一授权）更改了 REST handler 的现有行为** | 中 | 高 | 在所有 handler 中增加行为记录日志 + 新增配置 `AUTHZ_LEGACY_BEHAVIOR=true` 暂时保持旧行为，逐租户迁移 |
| **Phase 3（存储路由）的配置模型过于复杂** | 中 | 中 | 提供多层抽象：简单模式（单一后端，零复杂度）和高级模式（多后端，路由规则）。启动时根据单一后端/多后端自动选择 |
| **Phase 2（审计哈希链）的并发写入导致链分叉** | 低 | 高 | 使用 `INSERT ... RETURNING id`（Postgres）或 `last_insert_rowid()`（SQLite）获取当前行 ID，`prev_id` = 上一行的 `id`。不依赖时间戳排序 |
| **跨方向依赖带来项目阻塞** | 中 | 中 | Phase 1 和 Phase 2 无依赖可并行；Phase 3 是 Phase 4 的前置条件，但在排期中已体现为串行 |
| **热调优控制回路引发生产震荡** | 低 | 中 | Phase 5 从"只读推荐模式"开始（控制器计算推荐值但不自动应用，通过告警通知管理员手动应用），确认稳定后再切换到"自动应用模式" |

### 5.4 阶段依赖关系图

```
Phase 0: 修复碎片化 ──┐
                      ├──→ Phase 1: 统一授权 ──→ (后续无关)
                      │
                      ├──→ Phase 2: 不可变审计 ──→ (后续无关)
                      │
                      └──→ Phase 3: 存储路由 ──→ Phase 4: 分层迁移
                      │
                      └──→ Phase 5: 热调优 (独立)

Phase 1 和 Phase 2 可并行
Phase 3 必须前置 Phase 4
Phase 5 全程独立可并行
```

建议：**从 Phase 0 开始（1 周），然后 Phase 1 和 Phase 2 并行（2 周），之后评估是否进入 Phase 3（3-4 周）。** Phase 5 作为低优先级独立工作项穿插进行。

---

## 总结

AeroVault 当前的架构质量较高 —— 清晰的层次结构、单一服务层设计、可插拔后端、标配契约测试 —— 这些都是经得起长期演化的基础。本文分析的五个方向不是"重建架构"，而是在现有架构基础上 **填补已定义的抽象层缺口**。

最紧急的是方向 A（统一授权），因为它直接修复安全漏洞且工作量最小。最有长期价值的是方向 B（存储路由），因为它打开了数据主权、成本分层和故障隔离三个企业级准入能力。建议按"修复 → 安全 → 合规 → 架构 → 运维"的顺序有序推进。
