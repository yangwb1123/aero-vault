现在我已对代码库有充分了解，以下是从架构角度的深入分析。

---

# 架构分析报告：aero-vault 五方向验证

## 1. 架构评估

### 1.1 现有架构的主要优势

当前系统具有非常扎实的架构基础，这一点在验证过程中得到了确认：

**分层清晰，关注点分离到位。** 四层架构（Protocol → Service → Storage/Repository → Async/Eventing）中的每一层都有严格的范围界定。`FileService` 是唯一的对象 CRUD 入口，这意味着任何协议适配器（REST、S3、WebDAV、MCP）都可以对写入的对象立即可见，无需重复业务逻辑。这是对象存储系统最正确的架构选择。

**依赖注入而非全局状态。** `NewFileService(store, repo, logger)` 及其 `WithEventSink` / `WithChunkCleaner` / `WithReadVerification` 构造器模式使测试和扩展变得简单而不侵入现有契约。`storage.Storage` 接口很小（~12 个方法），足以让多个后端实现简洁且彼此独立。

**持久化层的 SQL 抽象内聚性强。** `Repository` 接口（~60 个方法）虽然庞大，但它是**按主题分组**的（Objects、Buckets、Chunks、Events、Jobs、Quotas、Tenants、Audit），很容易模块化为结构体组合。双文件迁移系统（`migrations/{sqlite,postgres}`）是健壮数据库演进的标准做法。

**AI/RAG 流程的 Opt-in 安全默认。** `AI_INDEX_ENABLED` 标志意味着核心 CRUD 路径在零配置的 AI 集成情况下永远不受影响。`nil` embedder/llm/reranker 的防御性检查确保了这一点。

### 1.2 架构局限性（基于验证发现）

| 局限性 | 影响 | 严重程度 |
|---------|--------|----------|
| **S3 安全模型不完整** — 无 `PublicAccessBlock`、无 `ObjectOwnership` | AWS 兼容的关键缺口；数据泄露；ACL 覆盖无法撤销 | **P1** |
| **无 S3 Select** — 缺少 `SelectObjectContent` API | 阻止与 Spark/Presto/Athena 的大数据集成 | **P3** |
| **无性能基准测试** — `_test.go` 中无 `func Benchmark` | 无性能回归门禁；重构风险不可衡量 | **P2** |
| **配置验证不完整** — `Validate()` 方法存在但未覆盖交叉字段约束 | 部署时错误配置可能通过，仅在运行时失败 | **P2** |
| **Repository 接口膨胀** — `repository.go` 中超过 60 个方法 | 实现新的存储后端（替代 pgvector、etcd 等）变得越来越昂贵 | **P2** |
| **`internal/service/file.go` 接近 300 行** — 触及 AGENTS.md 中的 500 行警告阈值 | 可能需要在 1-2 个 Sprint 内进行文件拆分 | **P3（即将发生）** |

### 1.3 关键设计决策验证

| 决策 | 评估 |
|------|--------|
| **`FileService` 作为唯一入口** | ✅ **正确。** 这就是 S3 兼容对象存储的构建方式。它消除了 write-PUT 和 read-GET 中的重复逻辑和竞争条件。 |
| **单前缀存储键方案 `tenant/bucket/key`** | ✅ **正确。** 一个简单的路径连接，满足无限租户和逻辑桶，无需后端改变。 |
| **内存中的 BM25 / 暴力向量搜索作为默认** | ✅ 对于开发和 CI 来说是正确的。**架构债务**位于 pgvector/Qdrant 的可插拔性不足 — 切换需要改变环境变量、迁移流程，以及 SQL 后端的编译时依赖。 |
| **事件驱动索引** | ✅ 对于异步处理来说是正确的。回退到内联处理（`JOBS_WORKERS=0`）在没有作业队列基础设施的部署中提供了灵活性。 |
| **Middleware 链顺序固定且不可变** | ✅ 严格强制 `Auth → Tenant` 顺序对于安全模型（API 键固定租户）至关重要。 |
| **ACL 无条件接受 `x-amz-acl`** | ❌ **不安全。** 这是验证报告确认的具体安全缺口。如果没有 `PublicAccessBlock` 作为防护网，任何通过 ACL 的公开读取都是立即的数据泄露向量。 |

### 1.4 技术债务及架构债务

| 类型 | 项目 | 成本 |
|------|------|------|
| **架构债务** | `Repository` 接口大小（60+ 方法） | 新实现（例如，替换 pgvector、etcd 为租户存储）需要数百行样板代码。添加迁移和查询重写很繁琐。 |
| **架构债务** | S3 子资源路由在 `BucketDispatch` 中通过 switch 语句处理 | 每个新的子资源（`?publicAccessBlock`、`?ownership`、`?select`）都会增加分支复杂度。当前函数达到约 80 行。 |
| **技术债务** | `hash` 嵌入器作为默认值 | 适用于测试和 CI，但生成的是语义无意义的向量。生产部署必须切换到 `http` 嵌入器（Ollama、OpenAI）。当切换发生时，没有优雅的回退链。 |
| **测试债务** | 无基准测试 | 对 AI 管道的重构（嵌入器交换、搜索算法更改）无法针对回归进行测量。 |
| **文档债务** | 分析文档中的行号与当前代码库不同步 | `v128` 分析报告引用了 `handler.go:425-432` 和 `handler.go:499-510`，但这些在当前代码库中是 `handler.go:568` 和 `handler.go:384`。这种漂移使得文档在代码移动时不再可验证。 |

---

## 2. 扩展方向

### 方向 A：S3 安全模型补全（PublicAccessBlock + Object Ownership）

**为何需要：** 这是验证报告中标识的 **P1 安全缺口**。没有它：
- 任何调用方的 `x-amz-acl: public-read` 都会立即创建一个可公开访问的对象，且无法撤销
- AWS SDK 客户端在 2023 年 4 月起默认启用 `BucketOwnerEnforced` 策略，当前代码无条件接受 ACL 会违反 AWS 兼容性
- 多租户环境（有信任度低的租户）需要全局公有访问开关

**核心挑战：**
- 交叉访问检查： `FileService.PutObject` / `FileService.CreateBucket` 在写入 ACL 之前需要检查桶级和账户级的 `PublicAccessBlock`，这意味着 ACL 操作需要两次 repo 查询
- 迁移路径：从"任何 ACL 都允许"到"ACL 被全局开关阻断"需要向现有租户提供显式的 opt-out 期，否则默认开启 `BlockPublicAcls = true` 会破坏现有的公有桶
- 界面：`PublicAccessBlock` 是账户级设置（全部租户或默认租户？），还是桶级设置？AWS 两种都有。对于 aero-vault，账户级（operator key 设置）可能是首次实现的正确模型

**预期的架构变更：**
- 在 `BucketConfig` 中添加 `PublicAccessBlock` 字段（四个布尔值） — 可空，继承默认值
- 添加全局默认 `PublicAccessBlock` 配置（`S3_PUBLIC_ACCESS_BLOCK_*` 环境变量）
- 在 `internal/service/s3_security.go` 中添加 `enforcePublicAccessBlock(ctx, ...)` 检查（新增文件，保持 `file.go` < 500 行）
- 在 `BucketDispatch` switch 语句中添加 `?publicAccessBlock` 和 `?ownership` 子资源路由

**对现有系统的影响：**
- 影响范围小：仅限于 `CreateBucket`、`PutObject` 和 ACL 写入辅助函数
- 新配置变量的 `Validate()` 检查：如果 `S3_PUBLIC_ACCESS_BLOCK_BLOCK_PUBLIC_ACLS=true` 且已有桶设置了公有 ACL，则发出警告

---

### 方向 B：S3 Select 实现

**为何需要：** 验证报告标识了这一点为 P3。虽然优先级较低，但这打开了大数据生态系统的采用——Spark/Presto/Athena 使用 `SelectObjectContent` 在服务器端过滤对象数据，而无需将整个对象流式传输给客户端。对于 AI 工作负载尤其相关，其中文件通常很大且需要通过服务器端过滤减少。

**核心挑战：**
- SQL 解析：需要实现或嵌入一个 SQL 子集解析器（SELECT 列表、FROM、WHERE 子集、LIMIT）。一个路径是使用 `expr` / `govaluate` 库，或者使用 `vitess` SQL 解析器（但有依赖膨胀问题）
- 流式序列化：响应是 SQL 行作为事件流，带有基于偏移量的继续标记。这需要不同于标准 REST JSON 序列化的方案
- 架构推断：对于 CSV/JSON，需要在查询之前从对象的开头几行推断列类型。对于 Parquet/ORC，需要嵌入列式读取器

**预期的架构变更：**
- 新包：`internal/select/` 包含 `Parser`、`RowFilter`、`Projection` 类型
- 在 `internal/api/s3compat/handler.go` 中添加 `selectObjectContent` 方法 — 在 `BucketDispatch` switch 中的 `?select-type` 子资源
- `Selectable` 接口（可选的 `Storage` 扩展）：`ReadRange(ctx, key, offset, length) (io.ReadCloser, error)` — 大多数后端需要这个来高效进行范围选择

**对现有系统的影响：**
- 假设配置中禁用，则为零。新的 `S3_SELECT_ENABLED` 门控标志
- 一旦启用，请将 `FileService` 暴露一个 `Select(ctx, tenant, bucket, key, expression, inputSerialization, outputSerialization)` 方法

---

### 方向 C：基准测试框架 + 性能门禁

**为何需要：** 验证报告确认不仅没有基准测试，**而且没有性能回归门禁**。对于提供 AI 工作负载（嵌入生成、向量搜索、大文件流式传输）的对象存储来说，性能缺失在发货时是不可接受的。

**核心挑战：**
- 需要可重复的数据集。嵌入基准测试需要一个已知的文件语料库
- 基准测试必须隔离存储后端（`local` 临时目录）、AI 模型（`hash` 嵌入器，确定性）和网络（`httptest` 服务）
- 无法在 CI 中测试真正的 LLM 调用（网络依赖、API 成本），因此 AI 基准测试需要模拟后端

**预期的架构变更：**
- 在 `internal/bench/` 中或直接在被测试文件旁边新建基准函数。关键目标：
  - `BenchmarkFileServicePut` — 使用 local 存储 + SQLite 的各种对象大小（1KB、1MB、10MB）
  - `BenchmarkFileServiceGet` — 相同
  - `BenchmarkSearchHybrid` — 已知文档集的向量/BM25/混合
  - `BenchmarkEmbedderHTTP` — 模拟 HTTP 服务器，已知响应时间
  - `BenchmarkMultipartUpload` — 各部分并发
- 在 `Makefile` 中添加基准目标：`bench`、`bench-ci`。`bench-ci` 在 `go test -bench` 上使用 `-benchtime=1x -count=1` 用于快速 CI

**对现有系统的影响：**
- 零代码更改到生产路径。纯粹的 `_test.go` 添加
- 可能需要一个 `benchdata/` 目录用于测试语料库

---

### 方向 D：配置验证系统 — JSON Schema + 交叉约束 + `--dry-run`

**为何需要：** 验证报告发现现有的 `Validate()` 方法比文档所承认的更完善（它对存储和后端进行枚举检查，检查时间限制，检查速率限制一致性），但交叉字段约束**确实不完整**。此外，没有 JSON Schema 生成，这意味着 Helm chart values 无法自动验证，也没有 `--dry-run` 模式。

**核心挑战：**
- 交叉约束空间：清单每个组合都需要一个测试用例。例如：
  - `AI_INDEX_ENABLED=true` + `AI_EMBED_PROVIDER=hash`（应警告：hash 对于生产不安全）
  - `AI_CHAT_PROVIDER=http` + `AI_CHAT_ENDPOINT=""`（失败）
  - `REPLICATION_ENABLED=true` + `REPLICATION_BACKEND=s3` + `REPLICATION_S3_BUCKET=""`（失败）
  - `AI_VECTOR_BACKEND=pgvector` + `DB_DRIVER=sqlite`（失败 — pgvector 需要 Postgres）
  - `STORAGE_SSE_KEY` + `STORAGE_BACKEND=s3`（可能冲突 — SSE 与 S3 的服务器端加密）
- JSON Schema 生成需要遍历 `Config` 结构体并发出 `if/then/else` 依赖约束 — 这可以自动化（反射或代码生成）
- `--dry-run` 需要 `config.Load()` 在不触及其他全局副作用的情况下运行（godotenv.Load、启动日志）

**预期的架构变更：**
- 在 `internal/config/schema.go` 中添加 `GenerateJSONSchema() string` — 从反射构建的 `map[string]any` 生成 JSON Schema Draft 2020-12
- 在每个 `Validate()` 方法中的交叉约束检查 —— 当前 `Validate()` 方法将其分解为按组件划分的子方法；添加 `validateAICrossConstraints()` 等
- 创建 `cmd/server/main.go` `--dry-run` 标志，输出配置并退出 0（成功）/ 1（验证失败）
- Helm chart val Schema 挂接到 `templates/`

**对现有系统的影响：**
- 对 `Validate()` 的最小扩展 — 添加子方法，不要改变现有的方法签名
- 已有的 `config_test.go` 用例应扩展而不是重写

---

### 方向 E：Repository 接口分解

**为何需要：** 这是一个**架构债务**项目。`Repository` 接口有 60+ 个方法。任何新实现（etcd 替代 SQLite/Postgres、基于内存的测试实现、缓存的 Repository 包装器）都需要实现每一个方法。当前有两个实现（SQLite + Postgres），共享一个共同的 `sql.go`，因此它们之间的行为差异很小。但引入第三个后端（例如，pgvector 的 chunks/embeddings 的专用存储）将使实现变得痛苦。

**核心挑战：**
- Go 不支持隐式接口实现的多重继承，因此分解不能走得太远。但我们可以按域划分接口。
- 向 `FileService` 注入多个 Repository 接口会增加构造函数参数的数量
- SQLite 和 Postgres 实现共享：它们都在 `sql.go` 中实现 SQL 查询。如果我们将它们分解，每个子接口都需要自己的 `sql_$DOMAIN.go` 文件。

**预期的架构变更：**
- 将当前 `Repository` 接口拆分为一组较小的接口，所有接口都在 `internal/repository/` 中：
  - `ObjectRepository` — 对象 CRUD、版本、软删除
  - `BucketRepository` — 桶 CRUD、配置 ACL、CORS、日志
  - `ChunkRepository` — 块 CRUD、搜索
  - `JobRepository` — 作业队列
  - `TenantRepository` — 租户 CRUD、配额
  - `EventRepository` — 事件和 webhook 失败
  - `AdminRepository` — API 键、审计、租约、幂等性
- 主 `Repository` 接口成为这些子接口的组合：`type Repository interface { ObjectRepository; BucketRepository; ... }`
- `sql.go` 保留公共的 SQL 工具（`rebind`、事务辅助函数），但查询被分散到 `sql_objects.go`、`sql_buckets.go` 等

**对现有系统的影响：**
- 如果保留组合接口，则调用方零代码更改（`repo.GetObject(ctx, ...)` 将继续工作）
- 如果单个存储实现需要，将支持部分 Repository 注入
- 重构风险低：这是一个纯 Go 接口重组，不需要更改数据库模式

---

## 3. 接口设计建议

### 3.1 当前接口的评估

| 接口 | 评估 | 建议 |
|---------|--------|--------|
| `storage.Storage` | ✅ 大小合适（12 个方法），每个都是原语操作。 | 添加 `ReadRange(ctx, key, offset, length)` 用于 S3 Select 和高效范围操作。可选的扩展接口。 |
| `repository.Repository` | ❌ **过大** — 60+ 个方法。 | 如上文方向 E 所述进行分解。 |
| `FileService` | ✅ 精心设计的门面。 | 监视文件大小（当前约 280 行）。接下来 1-2 个功能应该进入它们自己的文件（`file_acl.go`、`file_security.go`）。 |
| `EventSink` / `ChunkCleaner` | ✅ 对非关键副作用使用可选钩子的最小接口。 | 保持不变。 |

### 3.2 新的抽象层

**不需要全新的层。** 当前的四层架构（Protocol → Service → Storage/Repository → Async）已经足够。

然而，有两个地方从轻量级抽象中受益：

1. **用于 AI 管道的 `IndexBackend` 接口。** 当前索引后端（内存 BM25、内存暴力、pgvector、Qdrant）通过环境变量进行开关切换，但在接口级别没有统一。一个 `IndexBackend` 接口具有 `InsertChunks`、`Search`、`Delete` 方法将使添加新后端（Elasticsearch、Pinecone、Weaviate）变得直接。

2. **`SecurityEnforcer` 用于 S3 安全模型。** 而不是将 `PublicAccessBlock` 和 `ObjectOwnership` 检查内联到 `FileService` 中，`SecurityEnforcer` 可以在写入前拦截 ACL/策略修改调用，应用桶级和账户级策略。这保持了 ACL 操作的关注点分离。

### 3.3 向后兼容性

对于所有建议的更改：

- **配置：** 新环境变量默认为「不安全」以获得向后兼容性（`S3_BLOCK_PUBLIC_ACLS=false`）。文档应该记录未来版本将默认切换为 true。
- **S3 协议：** 新的子资源端点（`?publicAccessBlock`、`?ownership`）在服务器接收请求之前对现有客户端不可见。现有的 `x-amz-acl` 路径继续工作。
- **Repository 接口：** 在将组合接口作为主 `Repository` 类型保留的同时，将其分解为子接口意味着编译依赖项保持不变。

---

## 4. 技术选型

### 4.1 是否引入新的依赖

| 潜在依赖项 | 目的 | 评估 |
|------------------|---------|--------|
| SQL 解析库（`expr`、`govaluate`、`vitess`） | S3 Select — 解析 `SELECT ... FROM ... WHERE ...` | ⚠️ **仅用于 S3 Select。** 如果实施，`expr` 是最轻量的（零外部依赖）。但如果 S3 Select 被延迟至 P3，则无关。 |
| 列式数据格式解析器（Parquet/ORC） | S3 Select — 读取列式数据 | ⚠️ 巨大依赖（Parquet 是 Go 中 ~50K 行）。**建议：** 在 CSV/JSON 上实施 S3 Select，将列式解析延迟至需求出现时。 |
| `testcontainers-go` | 针对 Postgres/pgvector/Qdrant 的集成测试 | ✅ **高价值。** 减轻手动 Docker 编排的负担。将 `make test-integration` 从 shell 脚本转换为 Go `TestMain`。不需要生产依赖。 |
| 指标上的 Prometheus 客户端 | 已经完成（`internal/telemetry/prometheus.go`） | — |

**总体结论：** 没有令人信服的理由——为了这五个验证方向——添加新的生产依赖。现有的标准库 + AWS SDK v2 + chi + pgx 集足以满足所有工作。

### 4.2 自建 vs 采购评估

对于验证报告中确定的功能缺口，所有路径都倾向于**自建**：

| 功能 | 自建 | 采购/集成 | 决策 |
|--------|---------|--------------|--------|
| `PublicAccessBlock` | 4 个布尔字段 + 1 个检查函数 | 不适用（存储行为，非外部服务） | **自建** — ~50 行代码 |
| `ObjectOwnership` | 1 个枚举字段 + ACL 条件检查 | 不适用 | **自建** — ~30 行代码 |
| S3 Select | SQL 解析器 + CSV/JSON 行迭代器 + 流式序列化 | 无现成的 Go S3 Select 引擎 | **自建** — ~500 行代码 |
| 性能基准测试 | `_test.go` 中的 `func Benchmark*` | 不适用 | **自建** — ~300 行代码 |
| JSON Schema 生成 | 基于反射的遍历 `Config` 结构体 | `jsonschema` 生成库（抑制了大量规则） | **自建** — 反射生成，确保模式完整 |

### 4.3 第三方依赖的评估标准

鉴于项目当前没有外部 Go 依赖（chi、pgx、godotenv 之外），**任何新依赖都应该满足所有条件**：

1. **安全关键：** Apache 2.0 / MIT 许可
2. **没有传递的 C 依赖：** 纯 Go
3. **经过实战检验：** GitHub 星标 > 1000，且在过去 12 个月内提交过
4. **API 稳定：** semver >= 1.0 或事实上的稳定
5. **模块化：** 仅导入需要的部分（无单体库）

如果 S3 Select 达到 P1 状态，`expr`（`expr-lang/expr`）满足所有这些条件。

---

## 5. 实施路线图

### 5.1 优先级排序

| 优先级 | 方向 | 工作量估计 | 风险 | 依赖关系 |
|----------|-----------|--------------|------|--------------|
| **P0** | 方向 A：S3 PublicAccessBlock + Object Ownership | 2-3 天 | 🔴 安全 | 需要迁移（桶表），ACL 行为改变 |
| **P1** | 方向 D：配置验证（交叉约束） | 1-2 天 | 🟡 中 | 无 |
| **P1** | 方向 C：基准测试框架（测试 + CI 门禁） | 2-3 天 | 🟢 低 | 无 |
| **P2** | 方向 E：Repository 接口分解 | 2-3 天 | 🟢 低（仅重构） | 无 |
| **P3** | 方向 B：S3 Select | 5-10 天 | 🟡 中 | 方向 E（干净的接口使 Repository 封装更容易） |

### 5.2 阶段划分

**阶段 1 — 安全加固**（第 1 周）

| 里程碑 | 交付物 |
|-----------|----------|
| M1.1 | `PublicAccessBlock` 模型 + 迁移 + 存储/检索 |
| M1.2 | 桶级和账户级 `enforcePublicAccessBlock` 检查，内联到 `FileService` ACL 操作中 |
| M1.3 | `ObjectOwnership` 模型 + `BucketOwnerEnforced` 强制实施 |
| M1.4 | `?publicAccessBlock`、`?ownership` 的 S3 子资源路由 |
| M1.5 | 新配置变量的 `Validate()` 交叉约束 |

**阶段 2 — 配置 + 基准测试**（第 2 周）

| 里程碑 | 交付物 |
|-----------|----------|
| M2.1 | `GenerateJSONSchema()` 函数 + `--dry-run` CLI 标志 |
| M2.2 | Helm chart 值验证使用生成的 JSON Schema |
| M2.3 | `BenchmarkFileServicePut`、`BenchmarkFileServiceGet`、`BenchmarkSearchHybrid` |
| M2.4 | `BenchmarkEmbedderHTTP`（模拟 HTTP 服务器） |
| M2.5 | `make bench-ci` 目标 + CI 集成 |

**阶段 3 — 重构 + 深度功能**（第 3-4 周）

| 里程碑 | 交付物 |
|-----------|----------|
| M3.1 | Repository 子接口（`ObjectRepository`、`BucketRepository`、`ChunkRepository` 等） |
| M3.2 | 保留了组合接口，因此调用方无需更改 |
| M3.3 | `IndexBackend` 接口（可选 — 如果 pgvector/Qdrant 采用率增长，则加分） |

**阶段 4 — S3 Select**（TBD — 由需求驱动）

| 里程碑 | 交付物 |
|-----------|----------|
| M4.1 | SQL 子集解析器（expr 或自建） |
| M4.2 | CSV 和 JSON 行迭代器 |
| M4.3 | `SelectObjectContent` 端点，流式响应序列化 |
| M4.4 | `S3_SELECT_ENABLED` 门控标志 + `Storage.ReadRange` 扩展接口 |

### 5.3 风险与缓解策略

| 风险 | 可能性 | 影响 | 缓解措施 |
|------|----------|--------|--------------|
| **阶段 1 的生产回归：** 启用 `BlockPublicAcls=true` 破坏现有的公开访问工作流 | 高 | 高 | 阶段 1 以**宽松模式**交付：默认 `BlockPublicAcls=false`，在所有 ACL 操作上记录警告。后续版本将默认切换为 true。 |
| **Repository 接口分解导致编译失败：** 大量接口更改导致合并冲突 | 中 | 高 | 在专门的 `refactor/repository-sub-interfaces` 分支上执行。使用 `git diff --stat` 追踪。在 Go 1.25 中，使用 `interfaces` 上的 `//go:generate` 来验证所有实现是否满足组合接口。 |
| **CI 中基准测试不稳定：** 由于 CI 负载波动导致基准测试时间变化 | 中 | 中 | 使用 `-benchtime=1x` 用于 CI 门禁，并设置宽松的回归阈值（>50% 降级触发警告）。使用专用基准 CI 运行程序进行精确测量。 |
| **S3 Select 需求范围蔓延：** 从简单的 CSV/JSON 扩展到 Parquet/ORC | 中 | 中 | 将 CSV/JSON 设为阶段 4 的硬编码范围。Parquet/ORC 是一个单独的功能请求，具备自己的门控。 |

---

## 总结

验证报告排除了以下五个方向：

| 方向 | 验证状态 | 行动 |
|----------|-------------|--------|
| S3 PublicAccessBlock | ✅ 确认缺口 | **立即实施**（P0） |
| S3 Object Ownership | ✅ 确认缺口 | **与 PublicAccessBlock 并行实施**（P0） |
| 性能基准测试 | ✅ 确认缺口 | **在阶段 2 实施**（P1） |
| 配置验证 | ⚠️ 现有基础设施，验证不完整 | **扩展已存在的基础设施**（P1） |
| S3 Select | ✅ 确认缺口 | **排入需求路线图**（P3） |

该代码库拥有坚实、可维护的架构。这些缺口大多是**功能缺失**，而不是**架构腐烂**——这是 18 个月之久的项目在经历快速功能增长后的自然状态。前三个阶段的变更可以在不重写任何核心抽象的情况下实施。这是架构健康状况的良好信号。

需要关注的一个指标：**`internal/service/file.go`** 接近 300 行，而 AGENTS.md 限制为 500 行。未来 2-3 个功能的添加应该以 `file_security.go`（针对 PublicAccessBlock 检查）、`file_acl.go`（针对 ACL 操作）或类似文件的文件命名约定，将主文件保持在限制以下。
