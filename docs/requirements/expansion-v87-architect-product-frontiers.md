# AeroVault 资深架构师/产品经理视角 — 第 87 轮：架构与产品纵深盲区

> **视角：** 资深架构师 / 产品经理  
> **方法：** 全代码库深度扫描（`cmd/server/main.go` + `internal/*` 全部 24+ 子包，三套 SDK，MCP 双模式，Web UI，48 对迁移文件，`deploy/` 配置，`Makefile`，CI gate）  
> **去重验证：** 对 `docs/requirements/` 下全部 86 份既有分析文档逐方向进行正则 + 语义交叉验证  
> **日期：** 2026-07-11  
> **核心原则：** 选取代码中存在具体锚点、有明确产品/架构影响、且在 86 轮分析中**零实质性独立分析**或**仅表格级一提而过的盲区**。每个方向包含产品价值、架构权衡、代码锚点、边界情况。

---

## 方向总览

| # | 方向 | 类型 | 优先级 | 核心痛点 | 代码锚点 | 既有分析覆盖 |
|---|------|------|--------|---------|---------|-------------|
| **1** | **EventBus 同步持久化在请求热路径引入非必要写放大与故障耦合** | 架构/性能 | **P1** — 每个业务写操作额外等待一次 DB INSERT 后才返回客户端；事件在被持久化前已明确是 best-effort（失败仅 warn log），但延迟成本由用户无条件承担 | `internal/events/bus.go:51-67`（`Publish` 先 `repo.InsertEvent` 后 `broadcast`，执行在调用者 goroutine）；`internal/service/file_crud.go:196`（`s.emit` 在 `writePutObject` 末尾同步调用）；`cmd/server/main.go`（`bus.Subscribe` 创建的 64-buf channel 消费者异步消费——持久化却是同步的）；`internal/events/bus.go:81-85`（`Publish：err != nil → logger.Warn`——证明 insert 是 best-effort，失败不影响业务） | ✅ **零实质性分析**（v55 方向四覆盖"订阅者健康管理"——聚焦消费者端异常而非生产者端同步等待。v49 方向一覆盖 SSE delivery best-effort semantics——聚焦 SSE 端点而非事件持久化路径。**本方向首次分析 Publish 路径中同步 DB 写入在数据面热路径上的性能代价与故障耦合**） |
| **2** | **S3 SelectObjectContent 协议能力缺失 — 无法对存储对象执行 SQL 级内容查询** | 协议合规/产品 | **P2** — AeroVault 定位为"知识库/文档保险箱"，但无法对存储的结构化/半结构化数据（CSV、JSON、Parquet）执行内容级查询。用户若在 vault 中存储日志文件、报表、数据导出，必须下载整个文件然后本地过滤。S3 Select 是 AWS S3 的标准化数据面查询能力 | `internal/api/s3compat/router.go`（S3 router 无 `select` 内容路由）；`internal/api/s3compat/handler.go`（无 `SelectObjectContent` handler）；`internal/api/s3compat/xml.go`（无 Select 请求/响应 XML 类型）；`internal/service/file_crud.go:Get`（仅支持按 key 读取完整对象）；`internal/service/file_features.go:GetRange`（仅支持字节范围，不支持表达式过滤）；`internal/ai/extractor.go`（已有 Extractor 框架可将对象内容转为文本——但无 SQL 执行引擎） | ✅ **零实质性架构分析**（v27/v53/v63 在 S3 协议合规方向表格中一行列出"S3 Select"作为目标项；v31 在多模型查询引擎方向中一行提及 Select 作为现有 S3 有限 SQL 能力的例证。**零代码锚点、零协议分析、零架构设计方案、零影响量化**） |
| **3** | **Local Storage 侧车元数据 JSON 缺乏并发安全与增量更新能力** | 可靠性/性能 | **P1** — LocalStorage 的元数据以 `.meta.json` 侧车文件存储，每次读取全量反序列化、每次写入全量序列化，无文件锁、无部分更新机制。高并发下读-改-写竞态可导致元数据丢失；大对象复杂元数据场景下序列化开销显著 | `internal/storage/local_meta.go`（`readMeta`/`writeMeta` 全量 JSON 读写——无锁、无增量）；`internal/storage/local_write.go:60-90`（`Put` → `writeMeta(f)` 覆盖写入——并发写可致侧车文件撕裂）；`internal/storage/local_read.go:30-50`（`Get` → `readMeta` 全量反序列化——每次 Stat/Get 都完整解析）；`internal/storage/local_list.go`（`List` 遍历目录读每个侧车文件——OS 级别的 readdir + JSON 解析放大）；`internal/storage/local.go:30-50`（`LocalStorage` 结构体仅 `sync.RWMutex` 保护 uploads map——metadata 文件操作无互斥） | ✅ **零实质性分析**（v48 方向二覆盖 multipart 侧车文件孤儿清理——聚焦清理而非并发安全。v40 方向四一行概念提及"元数据单点故障"指向 admin 功能而非存储格式。**零分析 LocalStorage 侧车 JSON 设计的并发安全缺陷与性能瓶颈**） |
| **4** | **存储类（StorageClass）仅元数据字段无真实分层转换引擎** | 产品/成本优化 | **P2** — `StorageClass` 字段存在于每个 Object 记录中，4 个迁移文件为之增加了 schema 支持，S3 响应正确返回 `x-amz-storage-class`，`BucketConfig` 中的 `ExpireAction`/`ExpireAfterDays` 周期性驱动删除（而非转换）。但没有任何组件根据 `StorageClass` 切换存储后端、移动数据、或调整副本数。用户设置了 `x-amz-storage-class: STANDARD_IA`，存储成本零变化。这是典型的产品功能"半实现" | `internal/repository/repository.go:30`（`Object.StorageClass string` 字段——完全被动存储）；`internal/repository/migrations/sqlite/0021_storage_class.up.sql`（新增 `storage_class` 列——schema 已就位）；`internal/service/file.go:DefaultStorageClass`（`STANDARD` 默认值——可配置但永不转变）；`internal/service/file_crud.go:buildPutObject`（`StorageClass: StorageClassOrDefault(opts.StorageClass)`——元数据写入，零后续动作）；`internal/api/s3compat/handler.go:writeObjectHeaders`（`x-amz-storage-class` 正确返回——协议面完整）；`internal/reconcile/lifecycle.go`（生命周期驱动删除不驱动转换——`ExpireAction` 只有 `soft_delete`/`hard_delete` 两个值）；`internal/storage/factory.go`（存储后端工厂无 `StorageClass` 裁决逻辑） | ✅ **零实质性架构分析**（extensions.md 方向三以 3 段概念描述 + 一个流程图覆盖了 Transition 规则的概念设计——**无代码锚点、无影响量化**。v40 方向表一行"存储类→后端映射"概念性识别。**零分析：已有代码中 StorageClass 字段的完整轨迹（schema→Object→Put→S3 响应→Lifecycle），以及转换引擎需要的基础设施（多后端路由器、数据移动器、RestoreObject 实现）**） |
| **5** | **AI 检索管道的全链路追踪断裂于 EventBus/Job 异步边界** | 运维/可观测性 | **P2** — OTel 正确初始化了 TracerProvider 和 TextMapPropagator，HTTP 中间件为每个入站请求创建根 Span。但当前置请求（如 PUT /v1/files/doc.pdf）最终触发异步 `index_object` job 时，job 中的索引操作启动一个**新的根 trace**。运维人员无法从"PUT 文档"的 trace 跳转到"索引该文档"的 trace，也无法查看从提取→分块→嵌入→写入索引的完整 span 瀑布。同样的问题存在于 Replication 和 Antivirus 的 EventBus→Job 边界 | `internal/telemetry/otel.go:60-75`（设置 TracerProvider + TextMapPropagator——正确但仅 HTTP 层使用）；`internal/telemetry/http.go:20-45`（`HTTPMiddleware` 为请求创建 `trace.Span`——仅此一处消费 tracer）；`internal/events/bus.go:51-67`（`Publish` 将 event 写入 DB——span context 未被序列化到 event payload）；`internal/repository/sql_events.go`（`InsertEvent` schema——`events` 表无 `trace_id`/`span_id` 列）；`internal/jobs/jobs.go:Queue.Enqueue`（job payload 仅含 `type+payload`——无 traceparent 字段）；`internal/reconcile/job.go`、`internal/replication/replication.go`、`internal/antivirus/worker.go`（工作者均从 `sub <-chan repository.Event` 读取事件——不提取或延续 trace context）；`internal/ai/indexer.go:Run`（`IndexObjectByID` 无父 span——新根 trace） | ✅ **零实质性独立分析**（v38 方向一覆盖"Context 传播与链路追踪连续性"——但聚焦 `context.Context` 在 goroutine 间的传递和 bg worker 的 `traceparent` 注入（v38:85-119）。v38 的分析覆盖了**原理层面**、job queue 的 traceparent 序列化思路（v38:119），但**未分析具体 EventBus 的事件→job 路径中 trace 上下文的断裂**、`events` 表无 `trace_id` 列、`Job.Payload` 无 trace 元数据字段等具体代码锚点。v38 是概念设计，本方向是代码锚点驱动的实施分析） |

---

## 方向一：EventBus 同步持久化在请求热路径引入非必要写放大与故障耦合

### 现状

当前 `Bus.Publish` 的执行流程：

```
HTTP PUT → FileService.Put → store.Put (I/O) → repo.UpsertObject (I/O) → 
  s.emit → bus.Publish → repo.InsertEvent (I/O) → HTTP 200
                                                      ↑
                                               用户等待这一笔
```

核心问题在于——`bus.Publish` 中的 `repo.InsertEvent` 是与业务操作**同步、串行、在调用者 goroutine 中**执行的。但从语义上，该事件写入已经是 **best-effort** 了——如果 InsertEvent 失败，Publish 仅打一条 warn log，不影响用户请求的成功返回。

```go
// internal/events/bus.go:51-67
func (b *Bus) Publish(ctx context.Context, e repository.Event) {
    id, err := b.repo.InsertEvent(ctx, e)   // ← 同步 DB 写入
    if err != nil {
        b.logger.Warn("event insert failed", ...)  // ← best-effort: 不影响请求
        return
    }
    e.ID = id
    b.broadcast(e)   // ← 内存 fan-out
    // ... transport
}
```

这意味着：**用户在热路径上为一条 best-effort 的 event 记录付出了确定的 DB 写入延迟**。

### 影响量化

以单次典型 CRUD 操作为例：

| 操作阶段 | 延迟大致范围 | 说明 |
|---------|------------|------|
| `validateKey` + `preflightQuota` | 0–2ms | 内存 + 可选 DB |
| `store.Put` | 1–50ms | 写入后端（local=fsync，S3=网络） |
| `verifyMD5` | ~0ms | 内存 |
| `repo.UpsertObject` | 1–5ms | DB 写入数据行 |
| `repo.AddTenantUsage` | 1–3ms | DB 更新配额 |
| **`repo.InsertEvent`** | **1–5ms** | **DB 写入事件行 → 用户等待** |
| `broadcast` + transport | ~0ms | 内存 fan-out |

在 SQLite 下（CI 基线路径），`InsertEvent` 的 `COMMIT` 是顺序 I/O 的一部分，约占单次 PUT 延迟的 **10–20%**。在 Postgres 下，如果 events 表和 objects 表在同一实例上，额外的 `fsync` 和网络往返会使这个比例更高。对于大批量写入（CI artifacts、日志归档、数据管道输出），这个固定开销会叠加为显著的吞吐瓶颈。

### 竞品行为

| 系统 | 事件持久化策略 | 用户感知延迟 |
|------|-------------|------------|
| AWS S3 + EventBridge | S3 PUT 返回后几秒内事件到达 EventBridge，S3 不等待 EventBridge 确认 | 事件延迟不转化为 PUT 延迟 |
| MinIO + Webhook | 同步写 bucket notification 配置表，事件投递异步 | 仅配置表写入在路径上 |
| **AeroVault 当前** | **同步写入 events 表后才返回** | **Event INSERT 的延迟直接加入 CRT 延迟** |

### 边界情况

| 场景 | 影响 |
|------|------|
| SQLite WAL 模式下高并发写入 | events 表与 objects 表竞争同一个 DB 的 fsync 槽位，Write-Ahead Log 的 checkpoint 延迟叠加 |
| Postgres 远程部署（~5ms RTT） | 每个 InsertEvent 额外消耗一次网络往返 |
| Events 表未清理积累到百万行 | Insert 因 B-tree 页分裂而变慢 |
| 事件写入失败（磁盘满/连接中断） | Warn log 后返回——延迟已花但全浪费 |
| 短连接 burst 写入（1000 PUT/秒） | 每个 PUT 增加 1–5ms event write → 每秒额外 1–5 秒的 DB 时间 |

### 架构建议（仅概念，不实现）

- **异步写入**：event 先发到 buffered channel，由独立 goroutine 批量 flush 到 DB（类似 log-agent 模式）。HTTP handler 无需等待 INSERT 完成。
- **批量写入**：`Publish` 仅追加到 in-memory buffer，累计 N 条或 T 秒后 `INSERT INTO ... VALUES (...), (...), (...)` 批量提交。
- **Unified Transaction**：将 `UpsertObject` + `AddTenantUsage` + `InsertEvent` 合并到同一 DB 事务中，减少一次额外 `COMMIT`。
- **可配置同步/异步**：`EVENTS_WRITE_MODE=sync|async|batch`，默认 sync 保持向后兼容。

---

## 方向二：S3 SelectObjectContent 协议能力缺失

### 现状

当前 S3 兼容协议支持对象级的 CRUD（Get/Put/Delete）、元数据操作（Tagging/ACL/Policy）、生命周期配置、CORS、通知——但完全不支持 **S3 SelectObjectContent** API。这是 AWS S3 中标准的数据面查询接口，允许客户端发送 SQL 表达式直接过滤 CSV、JSON、Parquet 对象的内容，服务端过滤后只返回匹配的行/记录，无需下载整个对象。

AeroVault 的 AI 管道已经具备 `ai.Extractor`（支持 PDF/HTML/JSON/CSV/Parquet 等格式的文本提取）和 `ai.Search`（语义检索）——但这两个能力完全独立于 S3 协议路径，无法通过 S3 Select 协议调用。

### 产品价值

| 用户画像 | 场景 | 当前方案 | S3 Select 方案 |
|---------|------|---------|---------------|
| 数据工程师 | 在 vault 中存储了 500MB CSV 日志，需要查询特定日期范围的记录 | 下载整个 CSV 文件（500MB）到本地 grep/awk | `SELECT * FROM s3object s WHERE s.date >= '2026-01-01'` → 仅返回匹配行（KB级） |
| 财务 | 按月存储的 JSON 格式报表，需按部门汇总 | 编写脚本调用 REST API 获取全部对象，本地解析 | `SELECT department, SUM(amount) FROM s3object[] GROUP BY department` |
| ML 工程师 | 存储 Parquet 格式的特征数据 | 全部下载后使用 Pandas/PyArrow 读取 | S3 Select 直接过滤列 + 行，网络传输量减少 90%+ |
| 运维 | 查询对象中的特定错误模式 | grep 整个文件 | `SELECT * FROM s3object WHERE content LIKE '%ERROR%'` |

### 已有可复用基础设施

AeroVault 的内核中**已有大部分 Select 所需的原材料**：

| 组件 | 位置 | 复用潜力 |
|------|------|---------|
| `ai.Extractor` — 从二进制内容提取文本 | `internal/ai/extractor.go:30-100` | 可直接将 CSV/JSON/Parquet 内容转为可查询的表结构 |
| `ai.Chunker` — 文本分块 | `internal/ai/chunker.go` | CSV 行级别的分块等价于 SQL 筛选的行 |
| `internal/api/s3compat/xml.go` — S3 XML 响应框架 | `internal/api/s3compat/xml.go` | 可扩展 Select 的 `SelectObjectContentRequest`/`SelectObjectContentResult` XML 类型 |
| `internal/service/range.go` — 字节范围读取 | `internal/service/range.go` | 可结合 SQL 过滤实现"边读边过滤"流式响应 |

### 架构复杂度

S3 Select 的实现涉及：

1. **SQL 解析**：支持 `SELECT`、`FROM`、`WHERE`、`LIMIT`、`GROUP BY` 等子集。可嵌入 `github.com/xwb1989/sqlparser` 或自定义简单解析器。
2. **格式感知读**：对 CSV 按行解析、对 JSON 按记录解析、对 Parquet 用 `github.com/xitongsys/parquet-go`/`github.com/apache/arrow-go` 列式读取。
3. **流式过滤**：边读边应用 `WHERE` 条件，仅匹配结果写入 `SelectObjectContent` 的帧格式 `Records`/`Stats`/`End` 事件。
4. **协议帧**：S3 Select 使用特殊的帧编码（`SelectObjectContentEventStream`），不是标准 XML/JSON 响应。这是协议实现中比较棘手的部分。
5. **压缩 + 序列化**：AWS S3 Select 支持以 GZIP 压缩的 CSV/JSON 作为输入，也需要支持输出压缩选项。

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| 对象不是 CSV/JSON（如 PDF、图片） | 返回 `400 InvalidObjectState` |
| SQL 表达式包含不受支持的语法（JOIN、UNION） | 返回 `400 InvalidExpression`（早期版本只实现 `SELECT ... FROM ... WHERE` 子集） |
| 对象大小 > 1GB | 流式读取 + 渐进过滤，不加载全部到内存 |
| 扫描数据量 > 允许配额 | 返回 `400 ScanQuotaExceeded` |
| CSV 包含转义字符/引号嵌套 | 使用 Go `encoding/csv` 的标准行为，接受 `Quote`/`Escape` 参数 |
| 请求指定了 `InputSerialization.CompressionType: GZIP` | 先 gzip 解压在解析过滤 |

---

## 方向三：LocalStorage 侧车元数据 JSON 并发安全与增量更新能力

### 现状

`LocalStorage` 将对象元数据存储在独立于对象 blob 的 `.meta.json` 侧车文件中。这种设计是合理且常见的（MinIO、SeaweedFS 也有类似模式），但当前的实现存在三个相互作用的问题：

#### 问题 1：无文件级锁，并发写导致侧车撕裂

```go
// internal/storage/local_write.go（抽象示意）
func (s *LocalStorage) Put(ctx context.Context, key string, ...) (ObjectInfo, error) {
    // ... 写入数据文件 ...
    writeMeta(metaPath(key), newMeta)   // ← 覆盖写入，无锁
    return info, nil
}
```

- 两个并发 `Put` 到同一 key（非版本化桶覆盖写入）可同时写 `.meta.json` → 数据竞态导致 JSON 格式损坏或内容错乱。
- `LocalStorage` 结构体仅用 `sync.RWMutex` 保护 `uploads` map，**metadata 文件操作本身无互斥**。
- `readMeta` 和 `writeMeta` 是包级函数，不做任何文件锁定（`flock`/`fcntl`）。

#### 问题 2：全量序列化/反序列化，无部分更新

每次 `Get` 或 `Stat` 调用都执行：

```go
func readMeta(path string) (localMeta, error) {
    data, err := os.ReadFile(path)          // ← 全量读取文件 → 内核到用户态拷贝
    var m localMeta
    json.Unmarshal(data, &m)                // ← 全量反序列化
    return m, nil
}
```

- 元数据仅有 7 个字段（`Key`、`Size`、`ETag`、`ContentType`、`LastModified`、`Metadata` map、`Envelope`），大约 200–2000 字节。反序列化开销在单次操作中可忽略。
- 但在 `List` 操作中，一个含 1000 个对象的目录需要：遍历 1000 个文件 → 1000 次 `os.ReadFile` → 1000 次 `json.Unmarshal` → 1000 次内存分配。对于纯元数据操作（如 `ls`），这个开销占绝大多数。
- 对象 `Metadata map[string]string` 如果包含大量用户自定义元数据（合规场景常见：几百个 key/value），反序列化成本会急剧增长。

#### 问题 3：读-改-写（Read-Modify-Write）非原子性

当前没有任何操作需要读取侧车元数据后修改再写回——每次 `Put` 是全新的元数据写入。但如果未来增加类似"更新最后访问时间"（`LastAccessedAt`）或"原子增加版本计数器"的特性，就必须面对读-改-写窗口：

```
T1: readMeta → old.Metadata["downloads"] = "42"
T2: readMeta → old.Metadata["downloads"] = "42"  // 读到同样的旧值
T1: writeMeta → {..., "downloads": "42"}
T2: writeMeta → {..., "downloads": "42"}  // 覆盖了 T1 的结果
```

### 影响量化

| 操作 | 文件数 | 当前路径 | 单次延迟 | 总延迟（1000 对象） |
|------|--------|---------|---------|-------------------|
| `List` | 1000 | `readdir` → 1000× `readMeta` | 0.5–2ms/次 | 0.5–2s |
| `Get` | 1 | `readMeta` → `os.Open` → stream | 0.5–2ms | 0.5–2ms |
| `Stat` | 1 | `readMeta` | 0.5–2ms | 0.5–2ms |
| `Put` (覆盖) | 1 | 写数据 → `writeMeta` | 0.5–2ms | 0.5–2ms |

在 ext4/XFS 文件和目录数量超过 ~10 万时，`readdir` + 大量小文件 open/read 的 POSIX 开销会显著退化。

### 边界情况

| 场景 | 当前行为 | 理想行为 |
|------|---------|---------|
| 并发覆盖同一对象元数据 | 两个 goroutine 同时 `writeMeta` → 可能写入残缺 JSON | 文件级锁定 + 原子重命名 |
| `writeMeta` 中途崩溃（core dump/kill -9） | 侧车文件可能只写了前半部分 → 下次 `readMeta` 返回 `json.SyntaxError` | 先写临时文件再 `os.Rename`（原子替换） |
| 磁盘空间不足时写入元数据 | `writeMeta` 返回 `os.Write` 错误（部分写入） | 写入前预检空间或使用预分配 |
| 大目录向 List（>10K 对象） | 遍历所有侧车文件 → O(n) 全量 JSON 解析 | 支持元数据缓存或索引文件 |
| 对象 key 含特殊字符（`/`、`?`、`#`） | `path.Join` 构造文件路径 → 可能意外进入子目录 | storageKey 用 hash 命名 + 元数据记录原始 key |

---

## 方向四：存储类（StorageClass）仅元数据字段无真实分层转换引擎

### 现状

`StorageClass` 字段在 AeroVault 中的完整轨迹如下：

```
迁移文件 0021 → Object.StorageClass 字段
    ↓
Put 时接受 x-amz-storage-class 或 REST body
    ↓
buildPutObject → repo.UpsertObject 持久化
    ↓
S3 GET/HEAD → writeObjectHeaders 返回 x-amz-storage-class
    ↓
Lifecycle 组件读取但只作用于删除（soft/hard），不作用于转换
```

这条链路**完整覆盖了存储类的元数据生命周期**——但 `StorageClass` 对存储行为**零影响**。无论设置为 `STANDARD_IA`、`GLACIER`、还是 `DEEP_ARCHIVE`，对象始终写入最初选定的存储后端，从未根据不同类调整副本数、延迟特性、或存储介质。

### 产品价值

| 使用者 | 问题 | 价值 |
|--------|------|------|
| DevOps/SRE | 日志保留 90 天后降冷到低成本存储，无需手动迁移 | 存储成本优化 60–80% |
| 财务 | 合规要求财务记录保留 7 年——前 30 天热存储，之后冷存储 | 满足合规的同时控制成本 |
| 产品经理 | 用户上传的媒体文件 30 天后自动归档 | 免运维的分层存储策略 |
| 业务拓展 | 向客户提供选择存储类的能力（"标准"vs"归档"定价分层） | 差异化定价能力 |

### 已就位但未使用的基础设施

| 组件 | 状态 | 缺口 |
|------|------|------|
| `Object.StorageClass` 字段 | ✅ 完整 | 需要 `TransitionEngine` 消费 |
| `BucketConfig.ExpireAfterDays` + `ExpireAction` | ✅ 完整（但 action 仅删除） | 需要 `TransitionAction: "STANDARD_IA"` 支持 |
| `reconcile.LifecycleJob` | ✅ 周期性扫描 + 事件驱动 | 当前只做删除，需扩展为 "动作分为 Delete 和 Transition 两条腿" |
| `storage.Storage` 接口 + `factory.go` | ✅ 多后端工厂 | 需要 `StorageClassRouter`：根据存储类选择后端实例 |
| `Backend()` 标识字符串 | ✅ 每个后端返回名称 | 需要 `BackendCapabilities()` 方法告知"支持的存储类列表" |

### 分层转换架构概念

```
LifecycleJob (定时扫描)
    │
    ├── 匹配 Transition 规则（当前版本的天数 → 存储类）
    │
    ├── [Copy] 对象到目标后端（新后端支持目标存储类）
    │   ├── 读取数据（sourceStore.Get）
    │   └── 写入目标（targetStore.Put）
    │
    ├── [Update] 更新 storage_key + storage_class + backend
    │   └── repo.UpdateObjectStorage(...)
    │
    └── [Delete] 源后端上的旧 blob（异步或参照保留计数）
```

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| 对象处于 WORM 锁定状态 | 转换允许（锁定的是删除动作而非存储类变更） |
| 对象是版本化 bucket 的一个历史版本 | 历史版本可以独立于当前版本设置不同的存储类（非当前版本生命周期） |
| 目标后端是远程 S3 且转换进行到一半失败 | 幂等——转换完成后更新元数据，失败的转换不在元数据中记录；重新扫描会重试 |
| 转换期间对象被访问 | GET 应从当前后端读取（无论存储类），转换完成后更新元数据指向新后端 |
| 存储类设为 GLACIER 后对象被请求 | 需要 `RestoreObject` 从 GLACIER 取回到临时标准存储（目前 REST `/restore` 只处理软删除恢复，不解冻）。需实现 `S3 RestoreObject` |
| 跨后端转换时数据一致性 | 新后端成功写入后再更新元数据并删除旧 blob——类似 Pg 的 `ALTER TABLE ... SET TABLESPACE` |

---

## 方向五：AI 检索管道的全链路追踪断裂于 EventBus/Job 异步边界

### 现状

当前 OTel 追踪仅在 **HTTP 请求-响应周期**内生效：

```
HTTP PUT /v1/files/doc.pdf
  │
  ├── OTel Span: "PUT /v1/files/doc.pdf"  ← ✅ 有 trace
  │
  ├── store.Put [storage I/O]
  │
  ├── repo.UpsertObject [DB write]
  │
  └── bus.Publish → repo.InsertEvent [events 表 INSERT] ← ❌ trace 不序列化
       │
       └── broadcast → Indexer channel
            │
            └── Indexer.Run → indexer.IndexObjectByID
                 ├── extractor.Extract(...)   ← ❌ 新根 trace
                 ├── chunker.Chunk(...)       ← ❌ 新根 trace
                 ├── embedder.Embed(...)      ← ❌ 新根 trace
                 └── sink.UpsertObjectChunks  ← ❌ 新根 trace
```

同样的问题存在于：
- **Replication**：PUT 的 trace 在 `bus.Publish` 处断裂，replication job 以新根 trace 执行
- **Antivirus**：同上
- **Webhook**：同上
- **Reconcile/GC**：JobPool 定期执行，无父 trace

### 为什么这很重要

| 场景 | 问题 | 当前排查方式 | 理想排查方式 |
|------|------|------------|------------|
| 用户上传文件后搜索不到 | "上传→索引"的端到端延迟？trace 在哪？ | 分别查 HTTP 日志和 Indexer 日志，手动关联 | 输入 PUT 的 trace ID → 自动看到 Indexer job 的子 span |
| 索引速度慢 | 瓶颈在 Extract？Chunk？Embed？还是 DB write？ | 查看各组件各自的 metric，无法关联到一次具体的索引操作 | 一次索引 trace 显示每个阶段的耗时瀑布 |
| 复制作业失败 | 失败的复制作业是哪个 PUT 触发的？ | 看 job 日志看 object_id → 去 objects 表查 → 回想什么操作？ | 复制 job 的 trace 直接 link 到源 PUT 的 trace |
| 用户反馈文件删除后仍出现在搜索结果中 | 删除事件是否被 Indexer 正确处理？ | 查看 events 表是否有该删除事件 → 看 indexer 日志 | 从 DELETE 请求的 trace 一路看到 indexer 的 delete_chunks job |

### 具体代码锚点

| 锚点 | 问题 |
|------|------|
| `internal/events/bus.go:51-67` — `Publish` | Span context 不序列化到 `repository.Event` |
| `internal/repository/sql_events.go` — `InsertEvent` | `events` 表无 `trace_id`/`span_id` 列 |
| `internal/jobs/jobs.go:Enqueue` | `repository.Job` 无 traceparent 字段 |
| `internal/repository/repository.go` — `Event`/`Job` 结构体 | 二者均无 `Traceparent string` 字段 |
| `internal/ai/indexer.go:Run` | `IndexObjectByID` 不提取或延续 `traceparent` |
| `internal/replication/replication.go:Run` | 同 |
| `internal/antivirus/worker.go:Run` | 同 |
| `internal/reconcile/job.go:scanAll` | Reconcile 周期性任务无父 span |
| `internal/telemetry/otel.go:35-45` | TextMapPropagator 正确设置了但仅 HTTP handler 使用 |

### Trace 恢复的最小可行方案

1. **`repository.Event` 增加 `Traceparent string` 字段** + 0025 迁移为 `events` 表加列
2. **`bus.Publish` 从 ctx 提取 traceparent**（`otel.GetTextMapPropagator().Extract` → `otel.GetTextMapPropagator().Inject` 到 carrier），序列化为 event 的 `Traceparent` 字段
3. **`repository.Job` 增加 `Traceparent string` 字段** + 迁移为 `jobs` 表加列
4. **Indexer/Antivirus/Replication worker** 从 event/job 中读取 traceparent → `otel.GetTextMapPropagator().Extract` → 启动子 span
5. **JobPool** 执行 handler 前同样提取 traceparent

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| 上游请求没有 trace（旧版本客户端、CLI） | job 不设置 traceparent → worker 正常创建新根 span |
| traceparent 跨天、超过采样间隔 | 由 up-stream sampler 策略决定——Otel 标准从不回收已发出的 trace |
| Events 和 Jobs 表 trace 列空值 | 可为 NULL，worker 读取空时按无父 span 处理 |
| 一次请求触发多个 job（index + replicate + scan） | 所有子 job 共享同一父 trace ID，各自独立的 span ID |
| 大量 job 共享一个 trace 导致该 trace span 过多 | 每个 job 用 `trace_id` 关联，但 `span_id` 独立；trace 后端支持 streaming 采样 |

---

## 总结

| # | 方向 | 类型 | 代码锚点密度 | 实施范围 | 用户可见影响 |
|---|------|------|------------|---------|------------|
| 1 | EventBus 同步持久化 | 架构/性能 | 高（3 文件） | 小（仅 bus.go） | 中（API 延迟降低 10–20%） |
| 2 | S3 SelectObjectContent | 协议合规/产品 | 中（协议层需新建） | 大（SQL 解析 + 帧协议 + 文件格式适配） | 高（新数据面查询能力） |
| 3 | LocalStorage 侧车元数据并发 | 可靠性/性能 | 高（4 文件） | 中（加锁 + 原子写） | 中（大目录 List 性能 + 并发安全） |
| 4 | StorageClass 分层转换 | 产品/成本 | 高（全链路已就位） | 大（Transition Engine + 数据迁移 + Restore） | 高（存储成本优化 60%+） |
| 5 | AI 全链路追踪断裂 | 运维/可观测性 | 高（8 文件涉及） | 中（Event/Job 加字段 + worker 提取） | 中（故障排查时间缩短 10x） |
