# 架构分析：AeroVault 高价值扩展方向

## 1. 架构评估

### 当前架构优势

AeroVault 当前的架构核心层（FileService + Storage + Repository + EventBus）的设计是扎实的：

- **协议适配器（Protocol Adapters）作为薄层**：REST/S3/WebDAV/MCP 作为外部协议适配器，核心业务逻辑集中在 `internal/service`，这是经典的**六边形架构（Hexagonal Architecture）**变体，方向正确。
- **确定性装配顺序**：`main.go` 的 `config → storage → repo → service → workers → middleware → router` 是显式依赖注入，避免了 `init()` 地狱和循环依赖，是 Go 工程化实践中值得推荐的模式。
- **接口化持久层**：`storage.Storage` 和 `repository.Repository` 接口清晰，支持多种后端（local/S3/OSS/COS + SQLite/Postgres），且 contract test 保障了实现一致性。
- **Opt-in 安全默认**：AI/pgvector/Qdrant/events/cluster 均标志门控，`nil` embedder/llm 不破坏核心路径，这是生产级系统的必要条件。

### 关键局限性

| 维度 | 当前状态 | 隐患 |
|------|---------|------|
| **协议依赖** | S3 API 与 REST API 是两套 handler，但底层共用 FileService | S3 handler 和 REST handler 中可能存在重复的元数据校验逻辑（违反 AGENTS.md 禁止 handler 自行校验 key 合法性的规则） |
| **可观测性 → 可控制性** | OTel 指标丰富（15+），但**没有闭环回路**（指标→告警→自动降级/扩容） | 运维靠人工盯 Grafana，而非系统自治 |
| **多租户隔离深度** | 租户在元数据层和 storage key 前缀层隔离 | storage key = `path.Join(tenant, bucket, key)` — 正确的设计。但不存在**租户级别资源配额硬限制**（当前配额只是字段记录，非执行层面的 throttle） |
| **AI 管线耦合** | `indexer` 在 `object.created` 事件中同步触发 | 大文件嵌入时可能阻塞事件总线或 FileService 返回 |
| **迁移与导出** | `snapshot.go` 与 Storage/Repository 接口同层但未用接口 | 代码锚点文档已指出，snapshot 硬编码 SQLite DSN 解析 + 本地 FS 路径遍历，与抽象层无关 |

### 架构债务

1. **不存在「租户拓扑」抽象**：当前多租户靠 storage key 前缀 + repository 行级 tenant 列。当需要「从 S3 导入到租户 A」或「把租户 B 的数据导出为 tar.gz」时，缺少一个**统一的数据枚举/拷贝/校验引擎**——每个新迁移场景都在重复 `ListObjects → GetObject → PutObject` 循环。这违反了 DRY 原则，且是未来 CDC 和导入导出的潜在技术债来源。

2. **S3 handler 路径过长**：`internal/api/s3compat/handler.go` 以单个 handler 文件加 dispatch switch 处理 15+ 子资源。AGENTS.md 限制单文件 ≤ 500 行，检查此文件是否已接近或超过阈值——如果已超，S3 子资源扩展（如 `?select`、`?tagging`）会加剧问题。

3. **迁移文件与 DSN 耦合**：`snapshot.go` 的 SQL DSN 解析逻辑应当抽象为 `Storage.Export(ctx, sink)` / `Repository.Export(ctx, sink)` 接口方法，而非在 snapshot 工具中硬编码路径构造。

---

## 2. 扩展方向（5 项深度分析）

### 方向 A：基础设施集成生态（对应文档方向一）

**为什么需要：**
Terraform Provider + GitHub Action 是 P0 是正确的判断。从架构视角看，当前的 SDK 层（Go/Python/JS）本质上是**被动客户端**——用户必须编写代码调用它们。Terraform Provider 将**被动 SDK 转为声明式状态管理**，这是产品从「API 服务」跃迁为「基础设施组件」的关键一步。K8s CSI + FUSE 虽然工作量更大（6000-8000 行），但直接影响容器化工作负载的采纳率。

**核心挑战与技术难点：**

| 集成 | 技术难点 |
|------|---------|
| Terraform Provider | 状态漂移检测（用户同时通过 S3 API 和 Terraform 修改桶策略 → `terraform plan` 需正确检测 drift）；Terraform Plugin Framework vs SDK v2 选型 |
| K8s CSI Driver | 身份模型：K8s ServiceAccount → aero-vault tenant → bucket 的映射链（安全最小化）；节点故障时的 volume 漂移恢复；CSI `NodePublishVolume` + `NodeUnpublishVolume` 的生命周期管理 |
| FUSE 挂载 | 内核缓存一致性（`attr_cache_timeout` + `dentry_cache_timeout` 配置）；大文件（>5GB）分段读取；并发写入冲突（last-writer-wins，非 POSIX 语义） |
| GitHub Action | 凭证安全：支持 OIDC（`id-token: write`）避免明文 API Key；artifact 上传匹配 aero-vault 的分片上传 |

**架构变更：**

```
现有： Go SDK → REST/S3 API → FileService → Storage/Repo

Terraform Provider 路径：
  Terraform CLI → terraform-plugin-go → Provider (复用 Go SDK) → REST API

更优方案：
  Terraform Provider → terraform-plugin-go → Provider (直接调用 FileService) → Storage/Repo
  （避免 HTTP 回环，减少延迟和部署依赖）
```

**决策：Terraform Provider 应该「本地直连」还是「通过 REST API」？**

| 选项 | 优点 | 缺点 |
|------|------|------|
| **直连 FileService** | 低延迟（无 HTTP 开销）、无需部署 REST 端点 | Provider 必须与 aero-vault 同进程或作为 library 嵌入；非标准模式 |
| **通过 REST API** | 标准部署模式（Provider 独立进程）、可远程管理 | 额外 HTTP 延迟、需认证、API 版本对齐成本 |
| **推荐：通过 REST API** | — | — |

推荐通过 REST API，因为 Terraform Provider 的典型使用场景是**从 CI/CD 机器或开发者工作站管理远端集群**，而非同进程管理。Provider 发布后用户无法控制 aero-vault 进程的内嵌方式。

**对现有系统的影响：**

- Terraform Provider 作为独立 Go module（`terraform-provider-aerovault`），不修改 aero-vault 核心代码
- K8s CSI Driver 需要新增 `cmd/csi-plugin/main.go` 和 `internal/csi/` 包，但通过 Go SDK 调用 REST/S3 API，不侵入核心
- FUSE 挂载可基于 `internal/storage` 接口实现，**但需要注意 path convention**：现有 storage key 格式为 `tenant/bucket/key`，FUSE 呈现时需决定是 `mount/<tenant>/<bucket>/` 还是 `mount/<bucket>/`（单租户模式）
- **风险点**：FUSE 路径约定与现有 `storageKey` 格式耦合，未来 storage key 格式变更时会破坏 FUSE

---

### 方向 B：通用数据迁移引擎（对应文档方向二）

**为什么需要：**
文档的判断正确——`snapshot.go` 是硬编码的单点方案，没有复用 `storage.Storage` 和 `repository.Repository` 接口。但我的分析更进一步：**缺失的不是「S3 导入器」或「导出 API」这些独立功能，而是一个通用的「数据管道抽象层」**。

**架构现状（问题所在）：**

```go
// snapshot.go — 当前模式
func (s *Snapshotter) Create(ctx context.Context, outPath string) error {
    // 硬编码：SQLite DSN → db file path
    // 硬编码：local FS → tar
    // 不可扩展：无 storage.Storage 接口调用
}

// 未来每个迁移场景都在重复：
func importFromS3(ctx, source, dest) { List → Get → Put }
func exportToTar(ctx, tenant, dest)  { List → Get → tar }
func migrateTenant(ctx, source, dest) { List → Get → Put }
```

**核心挑战与技术难点：**

1. **统一数据管道接口**：
   ```go
   type DataSource interface {
       List(ctx, prefix) → ([]ObjectMeta, error)
       Get(ctx, key) → (io.ReadCloser, error)
   }
   type DataSink interface {
       Put(ctx, key, metadata, reader) error
       Delete(ctx, key) error
   }
   type MigrationJob struct {
       Source    DataSource   // Storage backend / S3 / tar / snapshot
       Sink      DataSink     // Storage backend / tar / S3
       Filter    ObjectFilter // by tenant / bucket / prefix / time range
       Verifier  Verifier     // checksum comparison
       Reporter  ProgressReporter
   }
   ```

2. **断点续传与幂等性**：已迁移的对象标记（自定义 tag 或 `migration_<job_id>` 元数据列），中断后扫描 `not (migrated)` 继续

3. **对象锁/WORM 语义保留**：
   ```go
   type ObjectMeta struct {
       LockedUntil time.Time  // 必须保留
       VersionID   string     // 必须保留
       Retention   RetentionMode // GOVERNANCE / COMPLIANCE
   }
   ```
   迁移引擎必须读取源端 WORM 状态并在目标端重建。不同 storage backend 对 WORM 的支持程度不同——local FS 无 WORM，S3 有对象锁。这需要在迁移过程中**检查兼容性并给出警告**。

4. **性能节流**：迁移不能压垮目标端。需实现「自适应节流」——基于目标端的 `429 Too Many Requests` 或延迟增加自动减速，而非固定 QPS。

**架构变更：**

```
新增 internal/migration/ 包：
  migration/
  ├── engine.go       # 调度器：List → Filter → Copy → Verify → 进度上报
  ├── source.go       # DataSource 接口 + 实现（storage.Storage / S3 / tar）
  ├── sink.go         # DataSink 接口 + 实现（storage.Storage / tar / S3）
  ├── filter.go       # 租户/桶/前缀/时间范围/正则 过滤
  ├── verify.go       # ETag / SHA256 / 对象数比对
  └── throttle.go     # 自适应节流 + 速率限制
```

**对现有系统的影响：**

- `internal/snapshot/snapshot.go` 应逐步弃用，用 `migration.Engine{Source: repoLocal, Sink: tarSink}` 替代
- `DataSource` 的 `List` 方法需要存储后端实现**高效的前缀枚举**——当前 `local_list.go` 的递归扫描在大桶（百万+对象）下可能过慢，需游标分页
- `DataSink` 的 `Put` 需支持 multipart upload（>5GB 对象）——当前 `storage.Storage` 接口的 `Put` 接受 `io.Reader`，但 multipart 需要分片感知
- **向后兼容性**：`DataSource` 和 `DataSink` 接口新增于 `internal/migration`，不修改 `storage.Storage` 接口，零影响

---

### 方向 C：元数据查询引擎与分析生态（对应文档方向五）

**为什么需要：**
文档将方向五评为 P2，但我认为在以下条件下应升级为 **P1（Q2 与静态网站托管同优先级）**：

- 数据湖集成（Trino/Hive）确实是 P2 级别的生态扩展
- 但 **元数据查询 API**（结构化的 SQL-like 过滤 + 聚合）应当是 P1，因为：
  - 它是所有外部分析集成的**前置依赖**（Trino 连接器本质上是元数据查询 + 对象读取）
  - 它以极低成本提供极高价值（复用现有 repository 查询 + 现有 REST 路由）
  - 它与方向四（基准套件）存在协同：元数据查询 API 的延迟本身就是基准测量点

**核心挑战与技术难点：**

1. **谓词下推（Predicate Pushdown）**：
   ```sql
   SELECT COUNT(*) FROM objects 
   WHERE tenant = 'acme' 
     AND bucket = 'logs' 
     AND size > 1048576   -- >1MB
     AND content_type LIKE 'image/%'
   ```
   当前 `repository` 的 `StorageClassCounts` 和 `BucketStats` 是**硬编码聚合查询**。通用元数据查询 API 需要：
   - 一个**表达式解析器**（将 `size > 1MB AND content_type LIKE 'image/%'` 转为 SQL WHERE 子句或存储层过滤器）
   - 支持索引查询（假设 `size` 有数据库索引）vs 全表扫描的 fallback

2. **聚合器设计**：
   - `COUNT`, `SUM`, `AVG`, `GROUP BY content_type`, `GROUP BY storage_class` 等
   - 是否支持 `HISTOGRAM(size, 100)` 按区间统计？
   - 是否需要 `APPROX_COUNT_DISTINCT` 用于高基数统计？

3. **列裁剪（Column Pruning）**：
   查询只请求 `count_by_content_type` → 只需读 `content_type` 列 + 行计数 → 避免读取 `size`, `etag`, `metadata` 等不相关列。当前 `repository` 接口没有列选择概念——所有查询返回完整 `Object` 结构体。

4. **分页与大结果集**：
   查询可能匹配数百万对象 → 需游标分页（cursor-based，非 offset-based），避免 OFFSSET 在 deep pagination 下的性能下降。

5. **安全过滤**：
   元数据查询 API 必须自动注入 `tenant = ?` 和 ACL 过滤。最安全的模式是**查询引擎在 repository 层附加过滤条件**，而非在应用层二次过滤（避免泄露数据后再删除）。

**架构变更：**

```
新增 internal/query/ 包：
  query/
  ├── parser.go       # SQL-like 表达式解析（过滤条件 + 聚合函数）
  ├── planner.go      # 查询计划：确定使用哪些索引 / 是否需要全表扫描
  ├── executor.go     # 执行计划：调用 repository 的对应查询方法
  └── security.go     # 自动注入租户 + ACL 过滤
```

**也可不新增包**，而是扩展 `internal/api/rest/admin.go` 中已有的 `Usage` 端点，添加 `POST /v1/search/meta`。但从架构清晰性角度看，将查询逻辑独立为 `internal/query` 更优——它本身可被 Trino Connector 复用。

**对现有系统的影响：**

- `internal/repository/sql_objects.go` 可能需要新增 `QueryObjects(ctx, QuerySpec)` 方法，支持动态 WHERE、GROUP BY、ORDER BY
- 当前的 `Object` 结构体（包含所有元数据字段）需要支持序列化子集（`SELECT size, etag` → 只返回这两个字段）
- `internal/service/file_features.go` 可能新增 `QueryObjects` 入口
- **无向后兼容性问题**：新增 API 端点 + 新增 repository 方法，不影响现有调用者

---

### 方向 D：静态网站托管基础设施（对应文档方向三）

**为什么需要：**
文档评为 P1 正确。但架构视角下**静态网站托管不仅仅是添加 `index_document` 配置**——它需要重构路由层以支持**虚拟主机（vhost）路由**。当前请求分发模式是：

```
Request → chi Router → handler (path-based routing)
```

静态网站托管需要：

```
Request → Host Header Detection → VirtualHost Router → Tenant+Bucket Resolution → StaticHost Handler
```

**核心挑战与技术难点：**

1. **多监听器 vs 虚拟主机路由**：

   | 方案 | 优点 | 缺点 |
   |------|------|------|
   | **单监听器 + Host 解析**（推荐） | 简单，无需多端口管理，与现有 Ingress 兼容 | 需要 chi `Route` 级别的 Host 匹配或自定义 middleware |
   | **多监听器**（每个域名一个端口或 Unix socket） | 安全隔离强，可绑定不同 TLS | 配置复杂，端口管理成本高，不适合大规模多租户 |
   | **反向代理委派**（如 nginx + proxy_pass） | 0 代码变更 | 用户需自己管理代理，aero-vault 失去对网站配置的控制 |

   **推荐方案：单监听器 + 自定义 `VirtualHostMiddleware`**，在 chi 路由注册前按 `Host` 头解析 `tenant.bucket` 映射。

2. **目录索引**：
   - `GET /` → `GET /index.html`（S3 的 `IndexDocument`）
   - `GET /subdir/` → `GET /subdir/index.html`
   - `GET /subdir` → 301 到 `GET /subdir/`（S3 行为）
   需在 `handler.go:Get` 中新增逻辑：当请求目标是桶且路径最后为 `/` 时，追加 `index_document` 并重新检索对象。

3. **自定义错误页**：
   404 响应体应替换为桶配置的 `error_document`（如 `404.html`）。但当前错误处理路径是统一的 `errorResponse` JSON——需要让**桶未命中 + 静态网站模式**跳过标准错误响应。

4. **重定向规则**：
   S3 支持复杂的 `RoutingRules`：
   ```json
   {
     "RoutingRules": [{
       "Condition": {"KeyPrefixEquals": "docs/"},
       "Redirect": {"ReplaceKeyPrefixWith": "documents/"}
     }]
   }
   ```
   这需要策略引擎已有类似的路径匹配能力（`checkBucketPolicy` 已有条件评估），但需扩展为「重定向」而不是「允许/拒绝」。

5. **HTTPS 自动证书**：
   如果 aero-vault 直接管理 TLS（不委派给 Ingress），需集成 Let's Encrypt ACME。可选项：
   - `certmagic`（推荐，成熟 Go 库，自动续期）
   - `lego`（更底层，灵活性更高但工作量更大）
   - 委托给 Ingress（最简单，但用户需要额外配置）

**架构变更：**

```
internal/middleware/vhost.go (新增):
  VirtualHostMiddleware: 
    1. 解析 Host 头 → 查询桶绑定域名映射表（新表 bucket_domains）
    2. 将 tenant + bucket 注入 context
    3. 替换路由 context，使 chi 能按虚拟桶分发

internal/api/rest/handler.go:Get (修改):
  if request is bucket root + has IndexDocument → return index.html
  if error is 404 + bucket has ErrorDocument → return error.html

internal/repository/sql_buckets.go (扩展):
  CRUD for bucket_domains table
```

**对现有系统的影响：**

- `internal/config/config_app.go` 新增 `StaticHostingEnabled` 配置项（默认关闭 — 符合 opt-in 安全默认原则）
- `internal/repository/sql_buckets.go` 新增 `bucket_domains` 表迁移（注意 I2 规则：双迁移文件）
- `internal/api/s3compat/handler.go` 的 `getBucketLocation` 等子资源需要适配虚拟主机路由：S3 静态网站模式下桶的请求不通过 `/v1` 或 `/s3` 前缀，而是直接通过 `Host` 头发送

---

### 方向 E：性能基准套件与 CI 反馈闭环（对应文档方向四）

**为什么需要：**
文档评为 P1 是正确的，但我认为从架构视角来看，**基准套件不仅是工程工具，更是架构决策的量化基础**。当需要决定「是否引入缓存层」「是否重构 storage 接口」「是否改用流式处理 API」时，当前的架构决策依赖直觉——基准套件让这些决策可量化验证。

**核心挑战与技术难点：**

1. **基准隔离与可重复性**：
   ```makefile
   bench-ci:
       docker run --rm \
         --cpus=2 --memory=4g \
         -v $(PWD):/workspace \
         golang:1.25 \
         go test -bench=. -benchtime=10s -count=5 ./bench/
   ```
   关键技术问题：
   - `-count=5` 取中位数还是取均值？（建议中位数，抗 outlier）
   - 基准数据持久化在哪里？推荐 `bench/results/<commit-hash>.json` + CI artifact
   - Go GC 的 `GOGC` 设置（默认 100，基准时固定为 100 或 200 避免 GC 抖动）

2. **退化检测算法**：
   ```go
   func CheckRegression(baseline, current map[string]Result, threshold float64) []Regression {
       for key, cur := range current {
           base := baseline[key]
           ratio := cur.P50 / base.P50
           if ratio > threshold { // threshold = 2.0 → 2x 退化
               regressions = append(regressions, ...)
           }
       }
   }
   ```
   但阈值不应该是固定的 2x——不同衡量指标应有不同阈值：
   - `http.server.duration_ms` P95 → 1.5x（用户敏感）
   - `ai_embed_duration_ms` P95 → 3x（嵌入是异步管道，容忍度高）
   - `jobs_pending` → 绝对阈值（>100 持续 30s）

3. **负载模型**：
   `bench/bench_test.go` 应从**简单线性负载**（1 并发 → 10 → 100 → 1000）而非固定并发开始，以发现**拐点**——即系统吞吐量不再随并发增长的点。拐点数据比绝对 QPS 值更有架构意义。

4. **存储后端基准配对**：
   每个存储后端需要独立基准——local FS 的 P50 GET 延迟可能在 2ms，但 S3 后端可能是 20ms。基准报告应按后端分档。

**架构变更：**

- 新增 `bench/` 目录（与 `internal/` 同级），不修改核心包
- 可选新增 `internal/telemetry/bench.go`：将 OTel 指标原地输出为基准测量结果，避免基准工具重复采集

**对现有系统的影响：**

- `Makefile` 增加 `bench` 和 `bench-ci` 目标，不影响现有 `check` / `test` 目标
- `HARNESS.md` 可能需扩展：在 `make check` 后增加 `make bench-ci` 为非阻断性检查（`|| echo "Bench regression detected"`，不阻断提交但输出警告）
- **零核心代码变更**（符合文档的「不修改核心」原则）

---

## 3. 接口设计建议

### 3.1 设计原则

| 原则 | 说明 | 违反示例 |
|------|------|---------|
| **最少知识原则** | 外层包只依赖内层包的接口，不依赖实现 | `migration.Engine` 依赖 `storage.Storage` 接口而非 `storage.Local` |
| **接口归调用方定义** | `DataSource` / `DataSink` 定义在 `migration` 包内，而非 `storage` 包内 | 不应在 `storage` 包定义「导出用接口」 |
| **错误即类型** | 迁移引擎的错误应定义类型（`ErrLockConflict`, `ErrIncompatibleBackend`）而非字符串 | `errors.New("lock conflict")` |
| **配置即结构体** | 每个扩展方向的配置集中定义在 `config_app.go` 或新 `config_ext.go`，而非散落在 flag 和 env 中 | 当前 `config_app.go` 已有好的模式，保持即可 |

### 3.2 是否需要新的抽象层

**需要：** `internal/migration` 包的 `DataSource`/`DataSink` 接口。

```go
// internal/migration/source.go
type DataSource interface {
    // List returns object metadata matching the filter.
    // The implementation MUST support cursor-based pagination.
    List(ctx context.Context, prefix string, opts ...ListOption) (ObjectIterator, error)
    
    // Get returns the object content stream.
    // Caller MUST close the reader.
    Get(ctx context.Context, key string) (*ObjectContent, error)
    
    // Type returns the backend type for compatibility checks.
    Type() BackendType
}

type ObjectIterator interface {
    Next(ctx context.Context) bool
    Value() *ObjectMeta
    Err() error
    Close() error
}
```

为什么要新接口而非复用 `storage.Storage`？因为：

| 维度 | `storage.Storage` | `migration.DataSource` |
|------|-------------------|----------------------|
| 语义 | 写优先（Put/Delete/List） | 读优先（List/Get + 迭代器） |
| 一致性保证 | 无 | 需要源端快照一致性 |
| 性能模式 | 延迟敏感（在线请求） | 吞吐量优先（批量迁移） |
| 错误处理 | 立即失败 | 重试 + 跳过 + 记录 |

两个接口服务于不同目的，**不应该耦合**。

### 3.3 向后兼容性

| 变更类型 | 兼容策略 |
|---------|---------|
| **新增 API 端点**（`POST /v1/search/meta`、`POST /v1/admin/import/s3`） | 零影响：新端点不修改现有路由 |
| **新增配置项**（`STATIC_HOSTING_ENABLED`、`MIGRATION_*`） | 默认关闭（opt-in），零影响现有部署 |
| **Repository 接口扩展**（新增 `QueryObjects(ctx, QuerySpec)` 方法） | **Go 接口向后不兼容**——但 `repository.Repository` 是内部接口，通过 `internal` 包限定使用范围；新增方法只需更新所有实现（`sql_objects.go` + mock） |
| **新表迁移**（`bucket_domains`、`migration_jobs`） | 符合 I2 规则，双迁移文件，自动执行，零影响 |
| **弃用 `snapshot.go`** | 标记 `@Deprecated` + 文档说明 → 2 个版本后删除 |

**关键决策**：当向现有接口新增方法时，是否使用 Go 1.18+ 的**接口默认方法**？

- Go 1.18 的接口默认方法（通过 `interface { Method() default_impl }`）在 Go 1.25 中是否正式可用？**尚未完全确定**——Go 官方在 `golang.org/x/exp/constraints` 之外未广泛推行默认方法。因此所有 Go 接口扩展应**同步修改所有实现**，或使用 **functional options** 模式避免接口方法签名变更。

---

## 4. 技术选型

### 4.1 各方向技术栈评估

| 方向 | 推荐技术 | 备选 | 决策依据 |
|------|---------|------|---------|
| **Terraform Provider** | `terraform-plugin-framework`（推荐）vs `terraform-plugin-sdk v2` | — | Framework 是 Terraform 官方推荐的下一代 SDK，支持完整的类型系统和验证器。SDK v2 将在 2026 年后逐步弃用 |
| **K8s CSI Driver** | Kubernetes CSI spec v1.9+ + `sigs.k8s.io/secrets-store-csi-driver` 模式参考 | — | CSI spec 已成熟（v1.9 于 2024 年发布），无需引入额外 framework |
| **FUSE 挂载** | `github.com/jacobsa/fuse`（Go 的 FUSE 库，活跃维护，支持 macOS + Linux） | `cgofuse`（更底层，工作量大） | `jacobsa/fuse` 已被 `gcsfuse`（Google Cloud Storage FUSE）生产验证 |
| **HTTPS 自动证书** | `github.com/caddyserver/certmagic` | `go-acme/lego` | certmagic 提供零配置自动续期，与 aero-vault 的「opt-in 安全默认」理念一致；lego 更底层但灵活性更高的需求暂不存在 |
| **表达式解析器** | `expr-lang/expr`（元数据查询 API 的过滤条件解析） | 手写递归下降解析器、`goyacc` | `expr-lang/expr` 支持类型安全、纯 Go、无反射、可沙箱化，适合安全地解析用户输入的过滤条件 |
| **基准数据存储** | JSON 文件 + CI artifact | PostgreSQL、BigQuery | 基准数据量不大（每次 < 100KB），JSON 文件易于 diff 和版本控制 |

### 4.2 自建 vs 采购决策

| 决策项 | 自建 | 采购/集成 | 依据 |
|-------|------|----------|------|
| **性能基准套件** | ✅ **自建** | — | 需要定制适配 aero-vault 特有协议（REST + S3 + WebDAV + MCP）和存储后端 |
| **元数据查询引擎** | ✅ **自建** | — | 需要深度集成 aero-vault 的 repository 层和 ACL 安全模型；现有开源查询引擎（如 Trino）太重 |
| **Trino 连接器** | ✅ **自建** | — | Trino 的 SPI 接口（`ConnectorFactory`、`ConnectorSplitManager`、`ConnectorPageSourceProvider`）在 Java 中；只需实现 ~5 个 Java 类，工作量小 |
| **Iceberg 表格式** | — | ❌ **推迟** | 需要修改存储层以支持 ACID 兼容的 manifest + snapshot 文件；当前架构以对象存储为核心而非表格式为核心；建议延迟到 Q4 或更晚 |
| **CDC 迁移引擎** | ✅ **自建** | — | 需要深度集成 EventBus + 断点续传 + 校验逻辑；自定义需求高 |
| **Docker Volume Plugin** | ❌ **推迟** | — | 可通过 FUSE 间接实现，社区需求证据不足时不应投入专门开发 |

### 4.3 第三方依赖评估标准

根据 AGENTS.md 的 I6（Stdlib 优先），所有新依赖需满足：

1. **必要性**：标准库或现有依赖无法实现同等功能（如 `jacobsa/fuse` 是 Go 中 FUSE 的唯一成熟选项）
2. **稳定性**：v1.0+ 且过去 2 年有至少 1 次 release（排除废弃项目）
3. **许可证**：Apache 2.0 / MIT / BSD（排除 GPL-like copyleft 许可证）
4. **审计成本**：核心路径的依赖需经过安全审计（`certmagic`、`expr-lang/expr` 有安全记录）
5. **依赖膨胀**：每个新方向最多引入 3 个新 `go.mod` 依赖（超过需架构评审）

---

## 5. 实施路线图（修订版）

### 5.1 优先级与阶段划分

| 阶段 | 方向 | 里程碑 | 工作量 | 前置依赖 |
|------|------|--------|--------|---------|
| **Q1 (W1-3)** | Terraform Provider + GitHub Action | 桶 CRUD + 策略 + 生命周期；Action 发布 | 2-3 周 | `sdk/go` 已完备 |
| **Q1 (W3-5)** | 基准套件 v1 | `make bench` 可运行（PUT/GET/List）；CI 退化检测 | 2 周 | 无（复用 testing 框架） |
| **Q2 (W1-4)** | S3 导入器 + 租户导出 | `internal/migration` 包 + `POST /v1/admin/import/s3` + `GET /v1/admin/export` | 3-4 周 | 基准套件提供性能基线 |
| **Q2 (W3-5)** | 静态网站托管 MVP | `bucket_domains` 表 + 虚拟主机路由 + `index_document` + `error_document` | 2 周 | 无 |
| **Q2 (W5-6)** | **元数据查询 API**（升级为 P1） | `POST /v1/search/meta` + 表达式解析 + 聚合 | 2 周 | repository 接口扩展（参见 §3.3 兼容性） |
| **Q3 (W1-4)** | K8s CSI Driver | CSI identity + node + controller 服务；PVC 创建/挂载/卸载 | 6-8 周 | 静态网站托管的虚拟主机路由经验可复用 |
| **Q3 (W5-8)** | FUSE 挂载 beta | 只读 + 大文件支持 + attr/dentry 缓存 | 6-8 周 | CSI Driver 的凭证模型经验可复用 |
| **Q3 (W3-4)** | 基准套件 v2 | AI 管线基准（embed + search + chat）+ 存储后端对比 | 2 周 | 基准套件 v1 |
| **Q4 (W1-4)** | 增量 CDC 迁移 | 基于 EventBus 的变更跟踪 + 断点续传 + 校验 | 4 周 | S3 导入器 + 租户导出 |
| **Q4 (W5-8)** | Trino 连接器 | Java SPI 实现（ConnectorFactory + ConnectorSplitManager + ConnectorPageSourceProvider） | 4 周 | 元数据查询 API（复用查询路由逻辑） |

### 5.2 与原文档路线图的差异

| 原文档 | 修订后 | 理由 |
|--------|--------|------|
| Q1 = Terraform + `make bench` | 同 | 一致 |
| Q2 = 迁移 + 静态网站 | Q2 = 迁移 + 静态网站 + **元数据查询 API**（升级为 P1） | 元数据查询是 Trino 连接器前置依赖，Q2 做比 Q3 更经济 |
| Q3 = 元数据查询 API + K8s CSI + FUSE | Q3 = K8s CSI + FUSE + 基准套件 v2 | 元数据查询 API 提前到 Q2；CSI/FUSE 工作量匹配 Q3 |
| Q4 = CDC 迁移 + Trino 连接器 | 同 | 一致 |

### 5.3 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| **Terraform Provider 状态漂移** | 高 | 用户困惑 → 投诉 | 提供 `terraform refresh` 建议 + `PreventDestroy` 标记 + 文档明确声明哪些属性支持 drift detection |
| **CSI Driver 安全模型设计缺陷** | 中 | 租户隔离被突破 | 在 Q3 前完成安全审计 + 凭证注入使用 K8s `Secret` ref 而非明文 |
| **FUSE 缓存一致性问题** | 高 | 多节点写入 → 数据丢失 | v1 只支持只读挂载；v2 支持最终一致写入并明确文档声明 |
| **元数据查询 API 性能瓶颈** | 中 | 大租户查询超时 | 实施查询超时（`MAX_META_QUERY_SECONDS` 配置）+ LIMIT 默认 1000 + 游标分页 |
| **CDC 迁移引擎与 EventBus 耦合** | 中 | EventBus 重构 → CDC 损坏 | CDC 依赖 EventBus 接口而非实现；EventBus 接口应有独立契约测试 |
| **基准测试环境不一致** | 高 | 退化检测误报 | CI runner 规格标注在基准报告元数据中；仅比较同规格 runner 的基准结果 |

### 5.4 里程碑验收标准

| 里程碑 | 验收标准 |
|--------|---------|
| **Terraform Provider v1** | `terraform apply` 创建 bucket + 设置 CORS + `terraform destroy` 删除 → 通过 Postgres + Qdrant 环境 |
| **`make bench`** | 在 `cpus=2, mem=4g` 环境下输出 P50/P95 GET/PUT 延迟 + 退化检测报告 > 阈值时 exit 1 |
| **S3 导入器** | 将一个 10GB 的 S3 bucket（含 500 个对象，3 个 > 5GB）导入 aero-vault → 对象数匹配 + checksum 全部通过 |
| **静态网站 MVP** | bucket 绑定域名 → `curl -H "Host: static.example.com"` 返回 `index.html` |
| **元数据查询 API** | `POST /v1/search/meta {"filter":"size > 1048576","aggregate":"count_by_content_type"}` 返回正确聚合结果 |
| **K8s CSI Driver** | Pod YAML 声明 PVC → PVC 绑定额定 → Pod 内读写对象 → Pod 删除后 PVC 清理 |
| **Trino 连接器** | Trino `SELECT COUNT(*) FROM aerovault.acme."logs/*" WHERE _size > 1000` 返回正确行数 |

---

## 总结

AeroVault 的当前架构在核心路径（CRUD + 多协议适配 + 事件驱动 + AI 管线）上设计合理，但在**生态集成**、**数据迁移**和**分析能力**三个维度存在明确的架构缺口。

五个扩展方向中，最关键的架构决策是：

1. **Terraform Provider 走 REST API 而非直连** — 保持部署模式灵活性
2. **新增 `internal/migration` 抽象层** — 替代 `snapshot.go`，一劳永逸解决所有迁移场景
3. **元数据查询 API 提前到 Q2** — 既是独立功能，也是 Trino 连接器的前置依赖
4. **FUSE 只读起步** — 避免陷入缓存一致性泥潭
5. **基准套件重在 CI 检测而非全面压测** — 用最小的工程投入捕获性能退化

这些方向在 Q2 之前**不需要修改核心 `storage.Storage` 或 `repository.Repository` 接口**，风险可控。从 Q2 的元数据查询 API 开始才涉及核心接口扩展，但也遵循「新增方法而非修改签名」的向后兼容策略。
