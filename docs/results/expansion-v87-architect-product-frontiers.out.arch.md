以下是我的架构分析报告。

---

# 架构分析报告：基于第 87 轮验证的纵深评估

> **视角：** 资深架构师  
> **分析依据：** `docs/requirements/expansion-v87-architect-product-frontiers.md` + `.out.md` 验证报告  
> **参考上下文：** `AGENTS.md`（工程约束）、`ROADMAP.md`（已规划路线）、`CURRENT_SPRINT.md`（当前 sprint 边界）、`docs/architecture.md`（系统架构基线）

---

## 1. 架构评估

### 1.1 核心优势

AeroVault 的整体架构在 142 轮分析迭代后已经相当成熟，其设计质量可以从以下几个维度判断：

| 维度 | 表现 | 依据 |
|------|------|------|
| **分层清晰度** | ✅ 优秀 | 协议层→Service→Storage/Repository 的三层模型经过反复验证，每个适配器（REST/S3/WebDAV/MCP）都是 `FileService` 的薄封装 |
| **抽象一致性** | ✅ 良好 | `storage.Storage` 和 `repository.Repository` 接口在 5 个 backend 和 2 个 DB 中保持稳定，Contract Test 在 `storage/contract_test.go` 保障 |
| **可测试性** | ✅ 优秀 | 覆盖率 70.2%，SQLite+local FS 零网络基线，AI mock (`MockLLM`, `HashEmbedder`) 零外链 |
| **Opt-in 安全** | ✅ 优秀 | AI/pgvector/Qdrant/events/cluster/WebDAV 全默认关闭，`nil` embedder/llm 不破 CRUD |
| **迁移系统** | ✅ 成熟 | 48 对迁移文件，sqlite/postgres 双轨制，`s.rebind` 解决占位符差异 |

### 1.2 架构债务与薄弱点

尽管整体架构质量很高，v87 验证揭示了几个值得关注的架构债务：

#### 债务 1：EventBus 同步持久化的隐含假设矛盾（低危，已验证修正）

**问题：** `Bus.Publish` 在调用者 goroutine 中同步执行 `repo.InsertEvent`，但同一函数的错误路径只用 `logger.Warn` 处理。设计语义（best-effort）与实际执行策略（synchronous DB write on hot path）不匹配。

**严重程度：** P2（已验证降级）——因为它**不是故障路径**，它是性能优化。10-20% 的额外延迟在 CI 基线路径（SQLite）上可接受，但在 Postgres 远程部署下随 RTT 放大。

**根本原因：** 该设计最初可能假设事件持久化是"几乎免费"的（SQLite 本地文件），但忽略了：
- Postgres 场景下的网络往返（~5ms RTT = ~100% 的 PUT 延迟放大）
- `events` 表与 `objects` 表争用同一 DB 的 fsync 槽位
- 百万级 events 表 B-tree 页分裂

**修复状态：** 已验证为 P2（非阻塞），ROADMAP 无专项。建议纳入 v87 之后的优先级排期。

#### 债务 2：LocalStorage 侧车元数据的脆性已大幅缓解（低危，已验证修正）

**问题：** 原文声称侧车 JSON 无原子写入、无读-改-写能力。验证报告指出：
- **E1（已修正）**：`writeMeta` 已实现 `os.CreateTemp` + `os.Rename` 原子写
- **E2（已修正）**：`rewrap.go` 已存在 `RewrapObject` 读-改-写

**剩余薄弱点：**
- 侧车文件仍**无文件级锁**（`flock`/`fcntl`），虽然 `os.Rename` 是 POSIX 原子的，但两个并发 `writeMeta` 到同一文件的**数据准备阶段**仍存在竞态窗口
- 全量 JSON 序列化/反序列化对 `List` 操作（1000 个对象 = 1000 次 `os.ReadFile` + `json.Unmarshal`）的 O(n) 开销仍然存在
- ext4/XFS 在目录超过 10 万条目时的 readdir 退化不受 AeroVault 控制

**建议：** 降低优先级至 P3（长尾优化），在 `List` 成为生产瓶颈时再处理。不阻塞当前 Sprint。

#### 债务 3：AI 全链路追踪的断裂——唯一"验证确认且未修正"的中危项

**问题：** OTel 追踪断裂于 EventBus/Job 异步边界，Indexer/Antivirus/Replication 各以新根 trace 执行，运维无法端到端追踪 "PUT 文档→索引完成"。

**为什么这是架构债务：**
- 违反了**可观测性的一致性原则**——HTTP 层有 trace，异步层没有，导致"事故时追查路径断裂"
- 跨异步边界的 context 传播是分布式系统的经典难点，越晚修复，越多的 worker 类型会固化无 trace 模式
- v87 验证补充了 Postgres NOTIFY 中 trace 丢失的维度——说明问题比原文档描述的更广

**建议：** 保持 **P1**（验证报告未降级此方向），但实施范围可收窄：只做 `Event` + `Job` 表加 `traceparent` 列 + bus/worker 提取，不做复杂采样。详见 §2。

### 1.3 关键设计决策合理性回顾

| 决策 | 评估 | 理由 |
|------|------|------|
| `FileService` 作为唯一 CRUD 入口 | ✅ 正确 | 四个协议共享同一业务逻辑，版本控制/配额/WORM 在单点执行 |
| 侧车元数据（`.meta.json`） | ⚠️ 合理的权衡 | 适合本地 FS 的简单性，但不适合大规模集群；验证已确认原子写就位 |
| 事件总线 in-process | ⚠️ 需演进 | 单进程正确，但多 replica 场景下 `64-buf channel` 丢事件已验证；Postgres NOTIFY transport 已实现 |
| In-memory BM25 全量重建 | ✅ 已修正 | ROADMAP #1 已验证增量 `ChunkSink` 替代了 30s 全重建 |
| OTel 仅 HTTP 层消费 | ❌ 需修复 | v38 概念层面覆盖了原理，但 concrete 实施锚点（events/jobs 表无 trace 列）验证为真 |

---

## 2. 扩展方向

基于 v87 验证的 5 个方向 + ROADMAP 的 10 个方向 + 验证对其事实准确性的裁定，我提炼出 **5 个高价值架构扩展方向**：

### 方向 A：跨异步边界的全链路追踪（P1 — 高）

**原 v87 方向五，验证确认有效。**

| 维度 | 描述 |
|------|------|
| **为什么需要** | 每次 PUT 触发索引→搜索不到的问题，运维需交叉比对 HTTP 日志和 Indexer 日志。这是可观测性的"最后一公里"，解决了 trace 断裂就解决了"文件上传后找不到"的排障周期从小时级降到分钟级 |
| **核心挑战** | ① Event/Job 结构体需加 `traceparent` 字段但不破坏已有序列化；② `bus.Publish` 在 publisher goroutine 中提取 ctx，但此时可能已有异步 goroutine 丢失 context；③ events 表加列需要双轨迁移（I2） |
| **架构变更** | ① `repository.Event` + `repository.Job` 增加 `Traceparent *string`；② `migrations/{sqlite,postgres}/NNNN_add_traceparent.{up,down}.sql`；③ `bus.Publish` 从 `ctx` 提取 traceparent → 序列化到 Event；④ Indexer/Antivirus/Replication worker 读取 traceparent → 延续 span |
| **影响范围** | 涉及 8+ 文件但每个改动小（5-15 行/文件）；无新依赖；无 proto/API 破坏；无性能损耗（空串为 nil，非 tracing 请求无开销） |
| **验证修正** | v87 验证补充了 Postgres NOTIFY transport 中的 trace 丢失——`events.Deliver` 不延续父 span，需在 `PostgresTransport.receive` 中提取 NOTIFY payload 中的 traceparent |

### 方向 B：S3 SelectObjectContent 协议实施（P1 — 产品差异化）

**原 v87 方向二，验证确认有效，且指出文档低估了 event-stream 帧编码复杂度。**

| 维度 | 描述 |
|------|------|
| **为什么需要** | AeroVault 定位"知识库/文档保险箱"，S3 Select 允许对存储的 CSV/JSON/Parquet 执行 SQL 过滤。这是数据面查询能力，可直接增强 RAG 管道的"结构化数据查询"缺失环节——用户无需下载整个文件再本地 grep |
| **核心挑战** | 验证报告特别指出：event-stream 帧编码的复杂度被文档低估。S3 Select 响应不是标准 XML，而是**分帧的二进制事件流**（`SelectObjectContentEventStream`），需实现 `Records`/`Stats`/`End`/`Cont` 帧的帧编码器+解码器 |
| **架构变更** | ① `internal/api/s3compat/select.go` — SQL 解析（可复用 `encoding/csv` + `encoding/json`，纯 Go 零新依赖）；② `internal/api/s3compat/select_stream.go` — event-stream 帧编码；③ `internal/api/s3compat/router.go` — 注册路由；④ `internal/api/s3compat/xml.go` — Select 请求/响应 XML 类型 |
| **影响范围** | 新文件 2-3 个，~500 行；不修改现有 handler；不破坏 S3 兼容性 |
| **复用** | 已有 `ai.Extractor` 可复用做格式解析（Parquet 用 `github.com/xitongsys/parquet-go`），`ai.Chunker` 的行级分块逻辑可复用于 CSV 行过滤 |

### 方向 C：StorageClass 真实分层转换引擎（P1 — 成本优化）

**原 v87 方向四，验证确认有效，同时补充了 Transition 规则存储模式的遗漏。**

| 维度 | 描述 |
|------|------|
| **为什么需要** | `StorageClass` 字段完整覆盖了元数据生命周期（schema→Put→S3 响应→Lifecycle），但对存储行为**零影响**。用户设置 `STANDARD_IA` 成本零变化。验证确认全链路锚点已到位，但 Transition 规则存储模式确实未实现。这是典型的产品功能"半交付" |
| **核心挑战** | ① Transition 规则存储模式——验证确认 ROADMAP 未覆盖此细节，需要 `BucketConfig.LifecycleRules` 扩展 `TargetStorageClass` 字段；② 跨后端数据移动需要**幂等**（新后端写入成功后再更新元数据）；③ RestoreObject 实现（GLACIER 取回）|
| **架构变更** | ① `repository.BucketConfig.LifecycleRules[].TargetStorageClass` 扩展；② `storage.Storage` 接口增加 `BackendCapabilities() []StorageClass`；③ `reconcile.LifecycleJob` 扩展 Transition 分支；④ `internal/service/file_features.go` 新增 `RestoreObject` |
| **影响范围** | 中大型（6-8 文件），需新迁移文件；但核心基础设施已就位（`reconcile.LifecycleJob` 周期性扫描、`storage.Storage` 多后端工厂、双轨迁移系统） |
| **风险** | 跨后端转换失败的数据一致性：建议双阶段提交风格——写新→验证→更新元数据→删旧 |

### 方向 D：EventBus 异步持久化抽象（P2 — 性能优化）

**原 v87 方向一，验证确认有效但降级为 P2。**

| 维度 | 描述 |
|------|------|
| **为什么需要** | 尽管是 P2 优化而非关键故障，但在 Postgres 远程部署场景下，每个 `InsertEvent` 的额外 ~5ms RTT 叠加为显著吞吐瓶颈。验证确认核心论点有效（行号偏差不影响主线）|
| **核心挑战** | ① 批量 flush 的 crash 安全——内存 buffer 中未 flush 的事件在进程崩溃时丢失（当前同步写虽慢但保证不丢）；② 批量插入的 SQL 占位符生成（`$N` → `?` rebind 逻辑）；③ 配置项设计（`EVENTS_WRITE_MODE=sync|async|batch`）|
| **架构变更** | ① `Bus.Publish` 改为写 buffer channel；② `internal/events/flusher.go` — 独立 goroutine 批量 flush（大小/时间双触发）；③ `Bus.Flush()` — 优雅关停前 flush，确保事件持久化 |
| **可选方案对比** | **A. in-memory buffer + 定时 flush**（简单，crash 丢 ≤T 秒事件）vs **B. ring buffer + WAL + 异步 flush**（复杂，零丢失）vs **C. Unified Transaction**（将 InsertEvent 并入 PUT 的同一 DB 事务，减少一次 fsync） |
| **权衡** | 方案 A 实现代价最低（~100 行），crash 丢失 ≤1 秒 event 可接受（已有 webhook 重试机制兜底）；方案 C 更简单但仅适用于 SQLite（Postgres 事务太长可能导致锁升级） |

### 方向 E：存储后端熔断器与限流层（P1 — 生产弹性）

**来自 ROADMAP #6，v87 未覆盖但验证上下文强烈相关。**

| 维度 | 描述 |
|------|------|
| **为什么需要** | 当前 `s3.go`/`oss.go`/`cos.go` 全使用 `http.DefaultClient`（无 timeout）。V87 验证了 LocalStorage 有原子写保护，但远端后端无熔断保护。一个 S3 网络分区 → 所有 in-flight 请求阻塞 → 全服务级联故障 |
| **核心挑战** | ① 熔断器参数调优——错误率滑动窗口大小、半开超时；② 熔断器在架构层次中的位置——是放在 `storage.Storage` 接口外层包装，还是放入 `FileService`；③ 熔断与 retry-backoff 的交互（避免 retry storm）|
| **架构变更** | ① `internal/storage/circuitbreaker.go` — `CircuitBreaker` 包装 `Storage` 接口；② `internal/service/file_service.go` — 注入熔断器；③ `internal/middleware/inflight.go` — 并发请求限流中间件（weighted semaphore）|
| **影响范围** | 中（3-4 文件），无新依赖（标准库 `sync` + 滑动窗口算法即可，无需 gobreaker 库）|
| **验证上下文** | v87 未直接覆盖此方向，但方向一的"事件写入失败仅 warn log"和方向三的**Atomic write 验证成功**构成佐证：系统在故障降级路径的防御深度是不均衡的——存储层有原子写（已验证修正），但远端后端无熔断。这应该是下一优先级 |

---

## 3. 接口设计建议

### 3.1 "钉住"已稳定的接口，避免频繁变动的成本

根据 v87 验证和 ROADMAP，以下接口应 **冻结** 至少到下一个大版本：

| 接口 | 状态 | 依据 |
|------|------|------|
| `storage.Storage` | ✅ **冻结** | 所有 5 个 backend 已实现；CURRENT_SPRINT 禁止修改 |
| `repository.Repository` | ✅ **冻结** | 同上；所有 CRUD 操作稳定 |
| `ai.VectorIndex` + `ai.LexicalIndex` | ⚠️ **观察中** | ROADMAP #1 刚完成 pgvector/Qdrant 适配，但后续 transition engine 可能需 `BackendCapabilities()` 方法 |
| `events.Bus` | ⚠️ **需演进** | 异步持久化抽象（方向 D）和 Postgres transport `Deliver`（ROADMAP #3）已证明需要扩展 |
| `ai.Embedder` / `ai.LLM` / `ai.Reranker` | ✅ **冻结** | CURRENT_SPRINT 明确禁止修改 |

### 3.2 有必要引入的新抽象层

#### 建议一：`TraceCarrier` 接口（轻量，跨异步传播）

```go
// internal/telemetry/trace.go
type TraceCarrier interface {
    GetTraceparent() string  // 可能为空
    SetTraceparent(tp string)
}
```

- `repository.Event` 和 `repository.Job` 实现此接口
- `bus.Publish` 从 `ctx` 提取 traceparent 后调用 `carrier.SetTraceparent`
- worker 启动前调用 `carrier.GetTraceparent()` → `otel.GetTextMapPropagator().Extract`
- **不再添加第三个 tracer provider**——所有异步 worker 复用 HTTP 层的 TracerProvider

**为什么不是直接加字段？** 接口允许 future 添加更多可传播的上下文（如 baggage、sampling priority）而不破坏字段命名约定。

#### 建议二：`BackendCapabilities` 接口（支撑 StorageClass 转换）

```go
// internal/storage/capabilities.go
type StorageClass string

const (
    StorageClassStandard    StorageClass = "STANDARD"
    StorageClassInfrequent  StorageClass = "STANDARD_IA"
    StorageClassGlacier     StorageClass = "GLACIER"
)

type BackendCapabilities interface {
    SupportedClasses() []StorageClass
    DefaultClass() StorageClass
}
```

- 不破坏 `storage.Storage` 接口（不添加方法到已有接口）
- 通过类型断言查询：`if cap, ok := store.(BackendCapabilities); ok { ... }`
- local backend → `[]StorageClass{StorageClassStandard}`
- s3 backend → `[]StorageClass{Standard, Infrequent, Glacier}`（利用 S3 原生）

#### 建议三（可选）：`EventSink` 接口（异步持久化抽象）

如果方向 D 进入实施，建议避免在 `Bus.Publish` 中硬编码 flush 策略：

```go
type EventSink interface {
    Write(ctx context.Context, events []repository.Event) error
    Close(ctx context.Context) error
}
```

内置实现：
- `SyncSink` — 每个事件立即写入（当前行为）
- `BatchSink` — 按大小/时间批量 flush
- `NoopSink` — 事件不持久化（dev/debug）

### 3.3 向后兼容性保障

| 场景 | 策略 |
|------|------|
| Event `traceparent` 字段为空 | Worker 按无父 span 处理，正常创建新根 trace——零行为变化 |
| 旧版事件表无 trace 列 | 新 migration 列可为 NULL（`ALTER TABLE events ADD COLUMN traceparent TEXT`），应用后旧行自动为 nil |
| 新 worker 读取旧 event | `carrier.GetTraceparent()` 返回空字符串 → 无延续 span |
| S3 Select 在未注册的路由上请求 | 返回 `501 NotImplemented`（S3 协议标准做法），不破坏已有路由 |
| StorageClass 转换中对象被访问 | GET 从当前后端读取——转换完成前元数据未更新，不存在中间态 |

---

## 4. 技术选型

### 4.1 不需要引入新技术栈的场景

基于 v87 验证结论，以下方向**不推荐引入新依赖**：

| 方向 | 推荐方案 | 理由 |
|------|---------|------|
| 全链路追踪（方向五） | OTel TextMapPropagator + 标准库 | 已有 TracerProvider + TextMapPropagator，仅需在 Event/Job 间传播 traceparent 字符串 |
| S3 Select（方向二） | 标准库 `encoding/csv` + `encoding/json` | 纯 Go，零依赖。Parquet 需 `github.com/xitongsys/parquet-go`（已在 go.mod 可选） |
| 熔断器（方向 E） | 标准库 `sync` + 滑动窗口 | ~100 行实现，无需 `gobreaker`/`hystrix-go` 等第三方库 |
| EventBus 异步持久化（方向一） | Channel + `time.Ticker` | Go 标准库原生支持，无需消息队列中间件 |

### 4.2 需审慎评估依赖的场景

| 组件 | 候选方案 | 评估标准 |
|------|---------|---------|
| SQL 解析器（S3 Select） | `github.com/xwb1989/sqlparser` / `pingcap/tidb/parser` | ① 纯 Go 无需 CGO；② 子集支持（`SELECT ... FROM ... WHERE` 而非完整 SQL）；③ 许可证兼容（BSD/MIT/Apache-2.0） |
| Parquet 列式读取（S3 Select） | `github.com/xitongsys/parquet-go` / `github.com/apache/arrow-go` | ① 流式读取而非全量加载；② 支持按列投影；③ 社区活跃度 |
| 分布式锁/选举 | Postgres advisory lock（已在 `cluster.Singleton` 实现） | 无需 ZK/etcd/Redis——当前 DB 优先原则（I6）|

### 4.3 自建 vs 采购决策矩阵

| 场景 | 自建 | 采购/集成 | 决策 |
|------|------|----------|------|
| Trace 传播 | 标准库 otel 传播 + 表字段 | Datadog APM / Honeycomb | **自建**——otel 已就位，仅需在异步边界补齐 |
| S3 帧编码 | 标准库 `encoding/binary` + 手写帧编码器 | 无适合的 Go 库 | **自建**——协议帧格式简单，AWS SDK 未暴露内部帧 |
| 熔断器 | 标准库 `sync` | `gobreaker`/`resilience4j` | **自建**——~100 行，依赖开销大于实现代价 |
| SQL 解析 | `pingcap/tidb/parser`（大）vs `xwb1989/sqlparser`（小） | AWS S3 Select 兼容 API（无需自己解析） | **轻量自建**——但需严格限定 SQL 子集（`SELECT ... FROM ... WHERE ... LIMIT`），非通用 SQL |
| 分布式协调 | Postgres advisory lock | etcd / Redis / NATS | **Postgres**——已有 postgres 后端，`cluster.Singleton` 已验证 |

---

## 5. 实施路线图

### 5.1 优先级总览

基于 v87 验证的优先级修正（方向一 P1→P2，方向三 P1→P2），结合 ROADMAP 的现有排期：

| 优先级 | 方向 | 原 P | 修正后 | 理由 |
|--------|------|------|--------|------|
| **P0** | AI 全链路追踪（方向五） | P2 | **P0** | 唯一验证确认且未修正的中危项；跨 8+ 文件但每个改动小；ROADMAP 有基础设施但缺少实施计划 |
| **P0** | 存储后端熔断器与超时（方向 E） | P1 | **P0** | 当前远端完全无保护，ROADMAP #6 确认；v86 验证 LocalStorage 有原子写但远端无兜底 |
| **P1** | StorageClass 转换（方向四） | P2 | **P1** | 全链路代码已就位的半交付功能；验证确认 Transition 规则存储模式缺失——填补此缺口即可 |
| **P1** | S3 Select（方向二） | P2 | **P1** | 产品差异化能力；复用已有 `Extractor` 框架；验证补充了帧编码复杂度——需额外实现但风险可控 |
| **P2** | EventBus 异步持久化（方向一） | P1→P2 | **P2** | 性能优化而非关键故障；验证确认异步写入不会影响正确性 |
| **P2** | LocalStorage 侧车锁（方向三） | P1→P2 | **P2** | 验证已确认原子写就位、rewrap 就位；剩余锁问题非紧急 |
| **P3** | 大目录 List 性能优化（方向三遗留） | — | **P3** | 仅在 >10 万对象时成为问题，可延后 |

### 5.2 阶段划分

#### 阶段 1：可观测性 + 弹性基础设施（2-3 周）

**目标：** 补齐追踪断裂 + 防止级联故障

```
Week 1-2: AI 全链路追踪
  ├── repository.Event 加 Traceparent 字段 + 迁移文件
  ├── repository.Job 加 Traceparent 字段 + 迁移文件
  ├── bus.Publish 提取 traceparent → 序列化到 Event
  ├── Indexer/Antivirus/Replication worker 延续 trace
  ├── Postgres NOTIFY transport 延续 trace（v87 验证补充）
  └── integration test: PUT → 发事件 → job → trace 跨边界

Week 2-3: 存储后端熔断器 + 超时
  ├── storage.Storage 包装 CircuitBreaker
  ├── s3/oss/cos HTTP Client timeout 配置化
  ├── inflight 限流中间件
  └── contract test: 熔断器模拟 backend 故障
```

**里程碑 M1：** `PUT /v1/files/doc.pdf` 的 trace ID 可在 Indexer/Replication/Antivirus 的 span 中搜索到。所有远端后端有 5s connect / 30s read 超时。

#### 阶段 2：存储价值优化（3-4 周）

**目标：** StorageClass 转换引擎落地 + S3 Select 协议补齐

```
Week 4-5: StorageClass 转换
  ├── BucketConfig.LifecycleRules[].TargetStorageClass 扩展（验证补充）
  ├── storage.Storage BackendCapabilities 接口
  ├── reconcile.LifecycleJob Transition 分支
  ├── RestoreObject API
  └── e2e test: STANDARD → GLACIER → Restore → STANDARD

Week 5-7: S3 Select
  ├── SQL 解析（子集：SELECT ... FROM ... WHERE ... LIMIT）
  ├── CSV/JSON 流式过滤
  ├── event-stream 帧编码器（v87 验证指出的额外复杂度）
  ├── S3 路由注册 + XML 类型
  ├── Parquet 支持（可选，延后至 P1.5）
  └── integration test: SELECT WHERE 过滤 + Records 帧输出
```

**里程碑 M2：** Bucket 配置生命周期规则可使 30 天前对象自动转换到 STANDARD_IA。S3 客户端可对 CSV 对象执行 `SELECT ... WHERE`。

#### 阶段 3：性能优化与批量处理（2 周，可选）

**目标：** EventBus 异步持久化 + 侧车性能优化

```
Week 8-9: EventBus 异步持久化
  ├── EventSink 接口（SyncSink / BatchSink）
  ├── Bus.Publish 写 channel + flusher goroutine
  ├── 优雅关停（Bus.Flush + waitgroup）
  └── benchmark: sync vs batch 在 SQLite/Postgres 下的延迟对比

可选：LocalStorage 侧车缓存
  ├── readMeta cache（bounded LRU）
  ├── List 批量读取批量解析
  └── benchmark: 10000 对象 List 延迟
```

**里程碑 M3（可选）：** PUT 延迟在 Postgres 场景下下降 10-20%。`List 10000` 延迟降低 5x。

### 5.3 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| 全链路追踪的 traceparent 列 + 迁移被 CI gate 拒绝（I2 迁移规则：sqlite+postgres 双文件） | 中 | 低 | 预创建迁移文件模板，`make check` 在 dev loop 中验证 |
| Postgres NOTIFY 的 `traceparent` 传播触发 payload 超长 | 低 | 中 | NOTIFY payload 最大 8000 字节；traceparent 仅 ~55 字节（`00-{32hex}-{16hex}-01`），安全 |
| S3 Select 的 event-stream 帧编码与现有 S3 SDK 不兼容（Go 客户端使用 `aws-sdk-go-v2` 内置 Select 解码器）| 中 | 中 | 严格遵循 AWS S3 Select 帧规范；使用 `aws-sdk-go-v2` 做集成测试验证 |
| StorageClass 转换中断（后端在新后端写入成功前崩溃）| 低 | 高 | 双阶段提交：新 backend 写入成功 → 更新元数据 → 删旧 blob。转换不更改元数据之前，GET 仍从原 backend 读取 |
| 熔断器误打开（短暂网络抖动触发熔断）| 中 | 中 | 滑动窗口指数衰减 + 最小请求数门槛（如 5 秒窗口内至少 10 个请求才判断）|

### 5.4 与现有 ROADMAP 的对齐

| 阶段 | 新增 | 与 ROADMAP 重叠 | 冲突 |
|------|------|----------------|------|
| 阶段 1 | AI 全链路追踪（ROADMAP 无专项） | ROADMAP #2（O11y）有指标但无 trace | 无冲突——trace 是 #2 的补充而非替代 |
| 阶段 2 | StorageClass 转换 | ROADMAP #9（Storage tiering） | 高度对齐——#9 的 tiering 是本方向的 super-set |
| 阶段 2 | S3 Select | ROADMAP #7（S3 feature parity）中的第 5 项 | 对齐——本分析将其升为 P1 |
| 阶段 3 | EventBus 异步持久化 | ROADMAP 无专项 | 新增方向，不冲突 |

---

## 6. 总结

v87 验证报告是一次高质量的事实验证，它纠正了原文在 LocalStorage 侧车元数据上的 2 个事实错误（原子写已实现、rewrap 已存在），确认了其他 3 个方向的准确性，并补充了 Postgres NOTIFY 中 trace 传播和 S3 Select 帧编码复杂度等关键细节。

**对我架构分析的核心影响：**

1. **优先级重排**：AI 全链路追踪从原来分散的 P2 升为 **P0**——它是唯一验证确认"问题存在且无修复"的中危方向。存储后端熔断器从 ROADMAP 的抽象项升为 **P0**——这是在远端后端无保护的现状下必须解决的弹性缺口。

2. **StorageClass 转换的"最后三块砖"已经清晰**：验证确认 Transition 规则存储模式是唯一未实现的环节。补齐 `TargetStorageClass` 后整个链路就完整了。

3. **S3 Select 可以做了**：验证确认了实现复杂度（event-stream 帧编码）但将该复杂度定性为"文档低估但可控"——不是不可逾越的障碍。

4. **不要碰已冻结的接口**：`storage.Storage`、`repository.Repository`、`ai.Embedder`/`ai.LLM`/`ai.Reranker` 在 CURRENT_SPRINT 禁止修改，ROADMAP 也无计划修改。新能力应通过**接口组合**（`BackendCapabilities`、`TraceCarrier`）而非接口扩展来实现。
